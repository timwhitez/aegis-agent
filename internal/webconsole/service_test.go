package webconsole

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/events"
	"go-cli-agent/internal/fileutil"
	agentruntime "go-cli-agent/internal/runtime"
	"go-cli-agent/internal/session"
	skillcatalog "go-cli-agent/internal/skills"

	"github.com/gorilla/websocket"
)

func TestServiceSteerWritesWebSource(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	store := svc.store
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "session_steer_web",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	state := session.State{
		Status:    session.StatusRunning,
		Phase:     "prepare",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/sessions/"+meta.ID+"/steer", bytes.NewBufferString(`{"message":"focus on the failing test","interrupt":true}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(webMutationHeader, "1")
	svc.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("unexpected status: %d body=%s", recorder.Code, recorder.Body.String())
	}

	requests, err := store.LoadSteerRequests(meta.ID)
	if err != nil {
		t.Fatalf("load steer requests: %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("expected one steer request, got %d", len(requests))
	}
	if requests[0].Source != "web" {
		t.Fatalf("expected source=web, got %q", requests[0].Source)
	}
}

func countWebEventType(items []events.Event, target string) int {
	var count int
	for _, item := range items {
		if item.Type == target {
			count++
		}
	}
	return count
}

func TestServiceSteerReportsStoreAppendFailureAsServerError(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	meta := testSessionMetadata(t, "session_steer_store_error")
	if err := svc.store.Create(meta, testSessionState(session.StatusRunning)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	steerPath := filepath.Join(svc.store.SessionDir(meta.ID), "control", "steer.jsonl")
	if err := os.Remove(steerPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove steer file: %v", err)
	}
	if err := os.Mkdir(steerPath, 0o700); err != nil {
		t.Fatalf("block steer path: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/sessions/"+meta.ID+"/steer", bytes.NewBufferString(`{"message":"focus on the failing test","interrupt":false}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(webMutationHeader, "1")
	svc.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected status: %d want %d body=%s", recorder.Code, http.StatusInternalServerError, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "is a directory") {
		t.Fatalf("expected steer store error in response, got body=%s", recorder.Body.String())
	}
}

func TestServiceSessionSubresourcesRejectUnknownSession(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	sessionID := "missing_session_subresource"
	for _, path := range []string{
		"/api/sessions/" + sessionID + "/children",
		"/api/sessions/" + sessionID + "/tasks",
		"/api/sessions/" + sessionID + "/messages",
		"/api/sessions/" + sessionID + "/goal",
	} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("expected %s to reject unknown session with 404, got %d body=%s", path, resp.StatusCode, string(body))
		}
	}

	errResp := postJSONError(t, ts.URL+"/api/sessions/"+sessionID+"/goal", map[string]any{
		"objective": "should not create a partial session tree",
	}, http.StatusNotFound)
	if errResp.Error == "" {
		t.Fatalf("expected missing-session goal create error response")
	}
	if _, err := os.Stat(svc.store.SessionDir(sessionID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing-session goal create to leave no session directory, got err=%v", err)
	}
}

func TestServiceGoalEndpointsMutateDurableGoal(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "session_goal_api",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		RootSessionID:    "session_goal_api",
	}
	if err := svc.store.Create(meta, testSessionState(session.StatusRunning)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	ts := httptest.NewServer(svc)
	defer ts.Close()

	var goal session.SessionGoal
	postJSON(t, ts.URL+"/api/sessions/"+meta.ID+"/goal", map[string]any{
		"objective":             "Converge the API",
		"mode":                  "mission",
		"success_criteria":      []string{"API creates goal"},
		"validation_plan":       []string{"manual: inspect events"},
		"features":              []string{"goal endpoints"},
		"milestones":            []string{"initial patch"},
		"require_plan_approval": true,
	}, http.StatusCreated, &goal)
	if goal.GoalID == "" || goal.Source != session.GoalSourceWeb || goal.Mission == nil {
		t.Fatalf("expected web mission goal, got %#v", goal)
	}

	postJSON(t, ts.URL+"/api/sessions/"+meta.ID+"/goal/pause", map[string]any{}, http.StatusOK, &goal)
	if goal.Status != session.GoalStatusPaused {
		t.Fatalf("expected paused goal, got %#v", goal)
	}
	postJSON(t, ts.URL+"/api/sessions/"+meta.ID+"/goal/resume", map[string]any{}, http.StatusOK, &goal)
	if goal.Status != session.GoalStatusActive {
		t.Fatalf("expected active goal, got %#v", goal)
	}
	patchBody := bytes.NewBufferString(`{"features":[{"id":"feature_api","title":"Goal API","status":"in_progress"}],"plan_status":"needs_approval"}`)
	patchReq, err := http.NewRequest(http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/mission/plan", patchBody)
	if err != nil {
		t.Fatalf("new patch request: %v", err)
	}
	patchReq.Header.Set("Content-Type", "application/json")
	patchReq.Header.Set(webMutationHeader, "1")
	patchResp, err := http.DefaultClient.Do(patchReq)
	if err != nil {
		t.Fatalf("patch mission plan: %v", err)
	}
	defer patchResp.Body.Close()
	if patchResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(patchResp.Body)
		t.Fatalf("unexpected patch status: %d body=%s", patchResp.StatusCode, string(body))
	}
	if err := json.NewDecoder(patchResp.Body).Decode(&goal); err != nil {
		t.Fatalf("decode patched goal: %v", err)
	}
	if goal.Mission == nil || len(goal.Mission.Features) != 1 || goal.Mission.Features[0].ID != "feature_api" {
		t.Fatalf("expected patched mission features, got %#v", goal.Mission)
	}
	planMode, err := svc.store.LoadPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("expected linked plan mode after mission plan needs approval: %v", err)
	}
	if planMode.LinkedGoalID != goal.GoalID || planMode.Status != session.PlanModeStatusPlanning {
		t.Fatalf("unexpected linked plan mode: %#v goal=%#v", planMode, goal)
	}
	approveReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/sessions/"+meta.ID+"/mission/plan/approve", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("new approve request: %v", err)
	}
	approveReq.Header.Set("Content-Type", "application/json")
	approveReq.Header.Set(webMutationHeader, "1")
	approveResp, err := http.DefaultClient.Do(approveReq)
	if err != nil {
		t.Fatalf("approve mission plan: %v", err)
	}
	defer approveResp.Body.Close()
	if approveResp.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(approveResp.Body)
		t.Fatalf("expected linked Plan Mode approval conflict before submit, got %d body=%s", approveResp.StatusCode, string(body))
	}
	postJSON(t, ts.URL+"/api/sessions/"+meta.ID+"/goal/complete", map[string]any{}, http.StatusOK, &goal)
	if goal.Status != session.GoalStatusComplete || goal.CompletedAt == "" {
		t.Fatalf("expected completed goal, got %#v", goal)
	}

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/sessions/"+meta.ID+"/goal", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("new delete request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(webMutationHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete goal: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("unexpected delete status: %d body=%s", resp.StatusCode, string(body))
	}
	if _, err := svc.store.LoadGoal(meta.ID); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected goal to be cleared, got %v", err)
	}

	history, err := svc.store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load goal history: %v", err)
	}
	if len(history) < 5 {
		t.Fatalf("expected goal history entries, got %#v", history)
	}
}

func TestServiceGoalCreateReportsEventAppendErrorAndRollsBack(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_goal_create_event_error")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	eventsPath := filepath.Join(svc.store.SessionDir(meta.ID), "events.jsonl")
	if err := os.Remove(eventsPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove events: %v", err)
	}
	if err := os.Mkdir(eventsPath, 0o700); err != nil {
		t.Fatalf("block events path: %v", err)
	}

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	postJSON(t, ts.URL+"/api/sessions/"+meta.ID+"/goal", map[string]any{
		"objective":              "Create goal with required event",
		"mode":                   "mission",
		"require_plan_approval":  true,
		"create_tasks_from_plan": true,
		"features":               []string{"created task should roll back"},
	}, http.StatusInternalServerError, &apiErr)
	if !strings.Contains(apiErr.Error, "events.jsonl") {
		t.Fatalf("expected event append error, got %#v", apiErr)
	}
	if _, loadErr := svc.store.LoadGoal(meta.ID); !errors.Is(loadErr, fs.ErrNotExist) {
		t.Fatalf("failed goal create should not leave goal snapshot, got %v", loadErr)
	}
	history, historyErr := svc.store.LoadGoalHistory(meta.ID)
	if historyErr != nil {
		t.Fatalf("load goal history: %v", historyErr)
	}
	if len(history) != 0 {
		t.Fatalf("failed goal create should remove created history, got %#v", history)
	}
	tasks, taskErr := svc.store.ListTasks(meta.ID)
	if taskErr != nil {
		t.Fatalf("list tasks: %v", taskErr)
	}
	if len(tasks) != 0 {
		t.Fatalf("failed goal create should not leave generated tasks, got %#v", tasks)
	}
	if planMode, planErr := svc.store.LoadPlanMode(meta.ID); !errors.Is(planErr, fs.ErrNotExist) {
		t.Fatalf("failed goal create should not leave linked plan mode, got plan=%#v err=%v", planMode, planErr)
	}
}

func TestServiceGoalCreateReportsLinkedPlanModeRelinkEventErrorAndRollsBack(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_goal_create_planmode_relink_event_error")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	planMode, err := svc.store.CreatePlanMode(meta.ID, session.PlanModeDraft{
		Enabled:   true,
		Objective: "Existing unlinked planning gate",
		Source:    session.PlanModeSourceWeb,
	})
	if err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	beforePlanHistory, err := svc.store.LoadPlanModeHistory(meta.ID)
	if err != nil {
		t.Fatalf("load plan mode history: %v", err)
	}
	blockWebEventsPath(t, svc.store, meta.ID)

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	postJSON(t, ts.URL+"/api/sessions/"+meta.ID+"/goal", map[string]any{
		"objective":             "Create mission with existing planning gate",
		"mode":                  "mission",
		"require_plan_approval": true,
	}, http.StatusInternalServerError, &apiErr)
	if !strings.Contains(apiErr.Error, "planmode.linked_goal") || !strings.Contains(apiErr.Error, "events.jsonl") {
		t.Fatalf("expected linked Plan Mode event append error, got %#v", apiErr)
	}
	if _, loadErr := svc.store.LoadGoal(meta.ID); !errors.Is(loadErr, fs.ErrNotExist) {
		t.Fatalf("failed linked Plan Mode event should not leave goal snapshot, got %v", loadErr)
	}
	loadedPlanMode, err := svc.store.LoadPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("load restored plan mode: %v", err)
	}
	if loadedPlanMode.PlanModeID != planMode.PlanModeID || loadedPlanMode.LinkedGoalID != "" {
		t.Fatalf("failed linked Plan Mode event should restore unlinked plan mode, got %#v", loadedPlanMode)
	}
	afterPlanHistory, err := svc.store.LoadPlanModeHistory(meta.ID)
	if err != nil {
		t.Fatalf("load restored plan mode history: %v", err)
	}
	if len(afterPlanHistory) != len(beforePlanHistory) {
		t.Fatalf("expected plan mode history restored to %d entries, got %d: %#v", len(beforePlanHistory), len(afterPlanHistory), afterPlanHistory)
	}
}

func TestServiceGoalCreateRollsBackWhenLinkedPlanModeCreationFails(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_goal_create_planmode_error")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	blockWebPlanModeHistoryPath(t, svc.store, meta.ID)

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	postJSON(t, ts.URL+"/api/sessions/"+meta.ID+"/goal", map[string]any{
		"objective":              "Create goal with linked plan mode",
		"mode":                   "mission",
		"require_plan_approval":  true,
		"create_tasks_from_plan": true,
		"features":               []string{"created task should roll back"},
	}, http.StatusInternalServerError, &apiErr)
	if !strings.Contains(apiErr.Error, "planmode-history.jsonl") {
		t.Fatalf("expected plan mode history error, got %#v", apiErr)
	}
	if _, loadErr := svc.store.LoadGoal(meta.ID); !errors.Is(loadErr, fs.ErrNotExist) {
		t.Fatalf("failed linked plan mode creation should roll back goal snapshot, got %v", loadErr)
	}
	history, historyErr := svc.store.LoadGoalHistory(meta.ID)
	if historyErr != nil {
		t.Fatalf("load goal history: %v", historyErr)
	}
	if len(history) != 0 {
		t.Fatalf("failed linked plan mode creation should roll back goal history, got %#v", history)
	}
	tasks, taskErr := svc.store.ListTasks(meta.ID)
	if taskErr != nil {
		t.Fatalf("list tasks: %v", taskErr)
	}
	if len(tasks) != 0 {
		t.Fatalf("failed linked plan mode creation should roll back generated tasks, got %#v", tasks)
	}
	if planMode, planErr := svc.store.LoadPlanMode(meta.ID); !errors.Is(planErr, fs.ErrNotExist) {
		t.Fatalf("failed linked plan mode creation should not leave plan mode, got plan=%#v err=%v", planMode, planErr)
	}
}

func TestServiceMissionPlanPatchReportsLinkedPlanModeRelinkEventErrorAndRollsBack(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_mission_patch_planmode_relink_event_error")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	planMode, err := svc.store.CreatePlanMode(meta.ID, session.PlanModeDraft{
		Enabled:   true,
		Objective: "Existing unlinked planning gate",
		Source:    session.PlanModeSourceWeb,
	})
	if err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	original, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeMission,
		Objective: "Patch mission plan with existing Plan Mode",
		Source:    session.GoalSourceWeb,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	beforePlanHistory, err := svc.store.LoadPlanModeHistory(meta.ID)
	if err != nil {
		t.Fatalf("load plan mode history: %v", err)
	}
	blockWebEventsPath(t, svc.store, meta.ID)

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	postJSONWithMethod(t, http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/mission/plan", map[string]any{
		"plan_status": session.MissionPlanStatusNeedsApproval,
		"features":    []map[string]any{{"id": "feature_plan", "title": "Feature", "status": "pending"}},
	}, http.StatusInternalServerError, &apiErr)
	if !strings.Contains(apiErr.Error, "planmode.linked_goal") || !strings.Contains(apiErr.Error, "events.jsonl") {
		t.Fatalf("expected linked Plan Mode event append error, got %#v", apiErr)
	}
	loaded, err := svc.store.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load restored goal: %v", err)
	}
	if loaded.GoalID != original.GoalID || len(loaded.Mission.Features) != 0 || session.NormalizeMissionPlanStatus(loaded.Mission.PlanStatus) == session.MissionPlanStatusNeedsApproval {
		t.Fatalf("failed linked Plan Mode event should restore goal, before=%#v after=%#v", original, loaded)
	}
	loadedPlanMode, err := svc.store.LoadPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("load restored plan mode: %v", err)
	}
	if loadedPlanMode.PlanModeID != planMode.PlanModeID || loadedPlanMode.LinkedGoalID != "" {
		t.Fatalf("failed linked Plan Mode event should restore unlinked plan mode, got %#v", loadedPlanMode)
	}
	afterPlanHistory, err := svc.store.LoadPlanModeHistory(meta.ID)
	if err != nil {
		t.Fatalf("load restored plan mode history: %v", err)
	}
	if len(afterPlanHistory) != len(beforePlanHistory) {
		t.Fatalf("expected plan mode history restored to %d entries, got %d: %#v", len(beforePlanHistory), len(afterPlanHistory), afterPlanHistory)
	}
}

func TestServiceMissionPlanPatchRollsBackWhenLinkedPlanModeCreationFails(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_mission_patch_planmode_error")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	original, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeGoal,
		Objective: "Plain goal before mission patch",
		Source:    session.GoalSourceWeb,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	blockWebPlanModeHistoryPath(t, svc.store, meta.ID)

	ts := httptest.NewServer(svc)
	defer ts.Close()

	patchReq, err := http.NewRequest(http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/mission/plan", bytes.NewBufferString(`{
		"plan_status":"needs_approval",
		"features":[{"id":"feature_plan","title":"Feature","status":"pending"}],
		"create_tasks_from_plan":true
	}`))
	if err != nil {
		t.Fatalf("new patch request: %v", err)
	}
	patchReq.Header.Set("Content-Type", "application/json")
	patchReq.Header.Set(webMutationHeader, "1")
	patchResp, err := http.DefaultClient.Do(patchReq)
	if err != nil {
		t.Fatalf("patch mission plan: %v", err)
	}
	defer patchResp.Body.Close()
	if patchResp.StatusCode != http.StatusInternalServerError {
		body, _ := io.ReadAll(patchResp.Body)
		t.Fatalf("expected plan mode creation failure, got %d body=%s", patchResp.StatusCode, string(body))
	}
	body, _ := io.ReadAll(patchResp.Body)
	if !strings.Contains(string(body), "planmode-history.jsonl") {
		t.Fatalf("expected plan mode history error, got body=%s", string(body))
	}
	loaded, err := svc.store.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load goal after failed patch: %v", err)
	}
	if loaded.Mode != original.Mode || loaded.Mission != nil || loaded.GoalID != original.GoalID {
		t.Fatalf("failed linked plan mode creation should restore original goal, got %#v original=%#v", loaded, original)
	}
	tasks, taskErr := svc.store.ListTasks(meta.ID)
	if taskErr != nil {
		t.Fatalf("list tasks: %v", taskErr)
	}
	if len(tasks) != 0 {
		t.Fatalf("failed linked plan mode creation should roll back generated tasks, got %#v", tasks)
	}
	if planMode, planErr := svc.store.LoadPlanMode(meta.ID); !errors.Is(planErr, fs.ErrNotExist) {
		t.Fatalf("failed linked plan mode creation should not leave plan mode, got plan=%#v err=%v", planMode, planErr)
	}
}

func TestServiceGoalStatusPreservesAccountingAndProgressFacts(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "session_goal_status_preserves_facts",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		RootSessionID:    "session_goal_status_preserves_facts",
	}
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	tokenBudget := int64(5)
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:      true,
		Objective:    "Preserve facts while pausing",
		TokenBudget:  &tokenBudget,
		StopOnBudget: true,
		Source:       session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	if _, limited, err := svc.store.UpdateGoalAccounting(meta.ID, session.GoalUsageDelta{TokensUsedDelta: 6, SourceTurn: 1}); err != nil || !limited {
		t.Fatalf("update accounting limited=%v err=%v", limited, err)
	}
	_, progress, err := svc.store.RecordGoalProgress(meta.ID, session.GoalProgressInput{
		Source:   session.GoalSourceTool,
		Kind:     "budget_wrapup",
		Summary:  "facts recorded",
		Evidence: []string{"progress evidence"},
	})
	if err != nil {
		t.Fatalf("record progress: %v", err)
	}

	ts := httptest.NewServer(svc)
	defer ts.Close()
	var paused session.SessionGoal
	postJSON(t, ts.URL+"/api/sessions/"+meta.ID+"/goal/pause", map[string]any{}, http.StatusOK, &paused)
	if paused.Status != session.GoalStatusPaused {
		t.Fatalf("expected paused goal, got %#v", paused)
	}
	if paused.TokensUsed != 6 || paused.BudgetLimitedAt == "" || paused.BudgetWrapUpRequestedAt == "" {
		t.Fatalf("expected accounting facts to survive status change, got %#v", paused)
	}
	if len(paused.Progress) != 1 || paused.Progress[0].ID != progress.ID {
		t.Fatalf("expected progress facts to survive status change, got %#v", paused.Progress)
	}
}

func TestServiceGoalClearReportsHistoryAppendError(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_goal_clear_history_error")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Objective: "Clear goal with history",
		Source:    session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	blockWebGoalHistoryPath(t, svc.store, meta.ID)

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	requestJSONWithMethod(t, http.MethodDelete, ts.URL+"/api/sessions/"+meta.ID+"/goal", map[string]any{}, http.StatusInternalServerError, &apiErr)
	if !strings.Contains(apiErr.Error, "goal-history.jsonl") {
		t.Fatalf("expected goal history append error, got %#v", apiErr)
	}
	loaded, loadErr := svc.store.LoadGoal(meta.ID)
	if loadErr != nil {
		t.Fatalf("failed goal clear should restore goal snapshot, got load error: %v", loadErr)
	}
	if loaded.GoalID == "" || loaded.Status != session.GoalStatusActive {
		t.Fatalf("failed goal clear should restore active goal snapshot, got %#v", loaded)
	}
}

func TestServiceGoalClearRollsBackHistoryWhenEventAppendFails(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_goal_clear_event_error")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Objective: "Clear goal with blocked events",
		Source:    session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	beforeHistory, err := svc.store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load goal history: %v", err)
	}
	blockWebEventsPath(t, svc.store, meta.ID)

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	requestJSONWithMethod(t, http.MethodDelete, ts.URL+"/api/sessions/"+meta.ID+"/goal", map[string]any{}, http.StatusInternalServerError, &apiErr)
	if !strings.Contains(apiErr.Error, "events.jsonl") {
		t.Fatalf("expected event append error, got %#v", apiErr)
	}
	loaded, loadErr := svc.store.LoadGoal(meta.ID)
	if loadErr != nil {
		t.Fatalf("failed goal clear should restore goal snapshot, got load error: %v", loadErr)
	}
	if loaded.GoalID == "" || loaded.Status != session.GoalStatusActive {
		t.Fatalf("failed goal clear should restore active goal snapshot, got %#v", loaded)
	}
	afterHistory, err := svc.store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load goal history after failed clear: %v", err)
	}
	if len(afterHistory) != len(beforeHistory) || goalHistoryContainsType(afterHistory, "goal.cleared") {
		t.Fatalf("failed goal clear should restore goal history, before=%#v after=%#v", beforeHistory, afterHistory)
	}
}

func TestServiceGoalStatusReportsHistoryAppendError(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_goal_status_history_error")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Objective: "Pause goal with history",
		Source:    session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	blockWebGoalHistoryPath(t, svc.store, meta.ID)

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	postJSON(t, ts.URL+"/api/sessions/"+meta.ID+"/goal/pause", map[string]any{}, http.StatusInternalServerError, &apiErr)
	if !strings.Contains(apiErr.Error, "goal-history.jsonl") {
		t.Fatalf("expected goal history append error, got %#v", apiErr)
	}
	loaded, loadErr := svc.store.LoadGoal(meta.ID)
	if loadErr != nil {
		t.Fatalf("load goal: %v", loadErr)
	}
	if loaded.Status != session.GoalStatusActive {
		t.Fatalf("failed goal pause should not advance goal snapshot, got %#v", loaded)
	}
}

func TestServiceGoalStatusRollsBackHistoryWhenEventAppendFails(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_goal_status_event_error")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Objective: "Pause goal with blocked events",
		Source:    session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	beforeHistory, err := svc.store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load goal history: %v", err)
	}
	blockWebEventsPath(t, svc.store, meta.ID)

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	postJSON(t, ts.URL+"/api/sessions/"+meta.ID+"/goal/pause", map[string]any{}, http.StatusInternalServerError, &apiErr)
	if !strings.Contains(apiErr.Error, "events.jsonl") {
		t.Fatalf("expected event append error, got %#v", apiErr)
	}
	loaded, loadErr := svc.store.LoadGoal(meta.ID)
	if loadErr != nil {
		t.Fatalf("load goal: %v", loadErr)
	}
	if loaded.Status != session.GoalStatusActive {
		t.Fatalf("failed goal pause should not advance goal snapshot, got %#v", loaded)
	}
	afterHistory, err := svc.store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load goal history after failed pause: %v", err)
	}
	if len(afterHistory) != len(beforeHistory) || goalHistoryContainsType(afterHistory, "goal.paused") {
		t.Fatalf("failed goal pause should restore goal history, before=%#v after=%#v", beforeHistory, afterHistory)
	}
}

func TestServiceGoalPatchPreservesRuntimeProgressFacts(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "session_goal_patch_preserves_runtime_facts",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		RootSessionID:    "session_goal_patch_preserves_runtime_facts",
	}
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	tokenBudget := int64(5)
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:      true,
		Objective:    "Patch goal safely",
		TokenBudget:  &tokenBudget,
		StopOnBudget: true,
		Source:       session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	if _, limited, err := svc.store.UpdateGoalAccounting(meta.ID, session.GoalUsageDelta{TokensUsedDelta: 6, SourceTurn: 1}); err != nil || !limited {
		t.Fatalf("update accounting limited=%v err=%v", limited, err)
	}
	_, progress, err := svc.store.RecordGoalProgress(meta.ID, session.GoalProgressInput{
		Source:   session.GoalSourceTool,
		Kind:     "budget_wrapup",
		Summary:  "runtime progress",
		Evidence: []string{"progress evidence"},
	})
	if err != nil {
		t.Fatalf("record progress: %v", err)
	}

	ts := httptest.NewServer(svc)
	defer ts.Close()
	var patched session.SessionGoal
	postJSONWithMethod(t, http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/goal", map[string]any{
		"success_criteria": []map[string]any{{
			"id":       "criterion_web",
			"text":     "Updated criterion",
			"status":   "pending",
			"required": true,
		}},
	}, http.StatusOK, &patched)
	if len(patched.SuccessCriteria) != 1 || patched.SuccessCriteria[0].ID != "criterion_web" {
		t.Fatalf("expected patched criteria, got %#v", patched.SuccessCriteria)
	}
	if patched.TokensUsed != 6 || patched.BudgetLimitedAt == "" || patched.BudgetWrapUpRequestedAt == "" {
		t.Fatalf("expected accounting facts to survive goal patch, got %#v", patched)
	}
	if len(patched.Progress) != 1 || patched.Progress[0].ID != progress.ID {
		t.Fatalf("expected progress facts to survive goal patch, got %#v", patched.Progress)
	}
}

func TestServiceGoalPatchRejectsMalformedStructuredItems(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_goal_patch_malformed_items")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Objective: "Reject malformed structured goal items",
		Source:    session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	postJSONWithMethod(t, http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/goal", map[string]any{
		"success_criteria": []map[string]any{{"id": "   ", "text": "cannot be referenced", "status": "pending"}},
	}, http.StatusBadRequest, &apiErr)
	if !strings.Contains(apiErr.Error, "success criteria id is required") {
		t.Fatalf("expected malformed criteria error, got %#v", apiErr)
	}
	loaded, err := svc.store.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load goal: %v", err)
	}
	if len(loaded.SuccessCriteria) != 0 {
		t.Fatalf("failed malformed goal patch should not advance snapshot, got %#v", loaded.SuccessCriteria)
	}
}

func TestServiceGoalPatchRejectsEmptyRequest(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_goal_patch_empty_request")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Objective: "Reject empty goal patch",
		Source:    session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	before, err := svc.store.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load goal baseline: %v", err)
	}
	beforeHistory, err := svc.store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load goal history: %v", err)
	}
	beforeEvents, err := svc.store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	postJSONWithMethod(t, http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/goal", map[string]any{}, http.StatusBadRequest, &apiErr)
	if !strings.Contains(apiErr.Error, "at least one field") {
		t.Fatalf("expected empty goal patch error, got %#v", apiErr)
	}
	after, err := svc.store.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load goal: %v", err)
	}
	if after.UpdatedAt != before.UpdatedAt || len(after.SuccessCriteria) != len(before.SuccessCriteria) || len(after.ValidationPlan) != len(before.ValidationPlan) || after.Mission != nil {
		t.Fatalf("empty goal patch should not advance goal snapshot, before=%#v after=%#v", before, after)
	}
	afterHistory, err := svc.store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load goal history after empty patch: %v", err)
	}
	if len(afterHistory) != len(beforeHistory) || goalHistoryContainsType(afterHistory, "goal.updated") {
		t.Fatalf("empty goal patch should not append history, before=%#v after=%#v", beforeHistory, afterHistory)
	}
	afterEvents, err := svc.store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load events after empty patch: %v", err)
	}
	if len(afterEvents) != len(beforeEvents) {
		t.Fatalf("empty goal patch should not append events, before=%#v after=%#v", beforeEvents, afterEvents)
	}
}

func TestServiceGoalPatchReportsHistoryAppendError(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_goal_patch_history_error")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Objective: "Patch goal with history",
		Source:    session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	blockWebGoalHistoryPath(t, svc.store, meta.ID)

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	postJSONWithMethod(t, http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/goal", map[string]any{
		"success_criteria": []map[string]any{{
			"id":       "criterion_web",
			"text":     "Updated criterion",
			"status":   "pending",
			"required": true,
		}},
	}, http.StatusInternalServerError, &apiErr)
	if !strings.Contains(apiErr.Error, "goal-history.jsonl") {
		t.Fatalf("expected goal history append error, got %#v", apiErr)
	}
	loaded, loadErr := svc.store.LoadGoal(meta.ID)
	if loadErr != nil {
		t.Fatalf("load goal: %v", loadErr)
	}
	if len(loaded.SuccessCriteria) != 0 {
		t.Fatalf("failed goal patch should not advance goal snapshot, got %#v", loaded.SuccessCriteria)
	}
}

func TestServiceGoalPatchReportsGoalWriteFailureAsServerError(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_goal_patch_goal_write_error")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Objective: "Patch goal with blocked goal write",
		Source:    session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	blockWebGoalLockPath(t, svc.store, meta.ID)

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	postJSONWithMethod(t, http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/goal", map[string]any{
		"success_criteria": []map[string]any{{
			"id":       "criterion_web",
			"text":     "Updated criterion",
			"status":   "pending",
			"required": true,
		}},
	}, http.StatusInternalServerError, &apiErr)
	if !strings.Contains(apiErr.Error, "is a directory") {
		t.Fatalf("expected goal write error, got %#v", apiErr)
	}
	loaded, loadErr := svc.store.LoadGoal(meta.ID)
	if loadErr != nil {
		t.Fatalf("load goal: %v", loadErr)
	}
	if len(loaded.SuccessCriteria) != 0 {
		t.Fatalf("failed goal patch should not advance goal snapshot, got %#v", loaded.SuccessCriteria)
	}
}

func TestServiceGoalPatchRollsBackLinkedPlanModeWhenEventAppendFails(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_goal_patch_link_event_error")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	planMode, err := svc.store.CreatePlanMode(meta.ID, session.PlanModeDraft{
		Enabled:   true,
		Objective: "Existing pending plan",
		Source:    session.PlanModeSourceWeb,
	})
	if err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	if planMode.LinkedGoalID != "" {
		t.Fatalf("expected unlinked plan mode before goal create, got %#v", planMode)
	}
	goal, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Objective: "Patch goal with existing plan mode",
		Source:    session.GoalSourceWeb,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	beforePlanHistory, err := svc.store.LoadPlanModeHistory(meta.ID)
	if err != nil {
		t.Fatalf("load plan mode history: %v", err)
	}
	blockWebEventsPath(t, svc.store, meta.ID)

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	postJSONWithMethod(t, http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/goal", map[string]any{
		"control": map[string]any{"require_plan_approval": true},
	}, http.StatusInternalServerError, &apiErr)
	if !strings.Contains(apiErr.Error, "planmode.linked_goal") || !strings.Contains(apiErr.Error, "events.jsonl") {
		t.Fatalf("expected event append error, got %#v", apiErr)
	}
	loadedGoal, loadErr := svc.store.LoadGoal(meta.ID)
	if loadErr != nil {
		t.Fatalf("load goal: %v", loadErr)
	}
	if loadedGoal.GoalID != goal.GoalID || loadedGoal.Control.RequirePlanApproval {
		t.Fatalf("failed goal patch should restore goal control, got %#v", loadedGoal.Control)
	}
	loadedPlanMode, err := svc.store.LoadPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("load plan mode: %v", err)
	}
	if loadedPlanMode.PlanModeID != planMode.PlanModeID || loadedPlanMode.LinkedGoalID != "" {
		t.Fatalf("failed goal patch should restore unlinked plan mode, got %#v", loadedPlanMode)
	}
	afterPlanHistory, err := svc.store.LoadPlanModeHistory(meta.ID)
	if err != nil {
		t.Fatalf("load restored plan mode history: %v", err)
	}
	if len(afterPlanHistory) != len(beforePlanHistory) {
		t.Fatalf("expected plan mode history restored to %d entries, got %d: %#v", len(beforePlanHistory), len(afterPlanHistory), afterPlanHistory)
	}
}

func TestServiceMissionPlanPatchTaskSyncPreservesRuntimeProgressFacts(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "session_mission_patch_sync_preserves_facts",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		RootSessionID:    "session_mission_patch_sync_preserves_facts",
	}
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	tokenBudget := int64(5)
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:      true,
		Mode:         session.GoalModeMission,
		Objective:    "Patch mission safely",
		TokenBudget:  &tokenBudget,
		StopOnBudget: true,
		Source:       session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	if _, limited, err := svc.store.UpdateGoalAccounting(meta.ID, session.GoalUsageDelta{TokensUsedDelta: 6, SourceTurn: 1}); err != nil || !limited {
		t.Fatalf("update accounting limited=%v err=%v", limited, err)
	}
	_, progress, err := svc.store.RecordGoalProgress(meta.ID, session.GoalProgressInput{
		Source:   session.GoalSourceTool,
		Kind:     "budget_wrapup",
		Summary:  "runtime progress",
		Evidence: []string{"progress evidence"},
	})
	if err != nil {
		t.Fatalf("record progress: %v", err)
	}

	ts := httptest.NewServer(svc)
	defer ts.Close()
	var patched session.SessionGoal
	postJSONWithMethod(t, http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/mission/plan", map[string]any{
		"create_tasks_from_plan": true,
		"features": []map[string]any{{
			"id":     "feature_web",
			"title":  "Web mission feature",
			"status": "pending",
		}},
	}, http.StatusOK, &patched)
	if patched.Mission == nil || len(patched.Mission.Features) != 1 || len(patched.Mission.Features[0].TaskIDs) != 1 {
		t.Fatalf("expected task-linked mission feature, got %#v", patched.Mission)
	}
	if patched.TokensUsed != 6 || patched.BudgetLimitedAt == "" || patched.BudgetWrapUpRequestedAt == "" {
		t.Fatalf("expected accounting facts to survive mission patch, got %#v", patched)
	}
	if len(patched.Progress) != 1 || patched.Progress[0].ID != progress.ID {
		t.Fatalf("expected progress facts to survive mission patch, got %#v", patched.Progress)
	}
}

func TestServiceMissionPlanPatchReportsHistoryAppendError(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_mission_plan_patch_history_error")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeMission,
		Objective: "Patch mission plan with history",
		Source:    session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	blockWebGoalHistoryPath(t, svc.store, meta.ID)

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	postJSONWithMethod(t, http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/mission/plan", map[string]any{
		"features": []map[string]any{{"id": "feature_web", "title": "Web feature", "status": "pending"}},
	}, http.StatusInternalServerError, &apiErr)
	if !strings.Contains(apiErr.Error, "goal-history.jsonl") {
		t.Fatalf("expected goal history append error, got %#v", apiErr)
	}
	loaded, loadErr := svc.store.LoadGoal(meta.ID)
	if loadErr != nil {
		t.Fatalf("load goal: %v", loadErr)
	}
	if loaded.Mission != nil && len(loaded.Mission.Features) != 0 {
		t.Fatalf("failed mission plan patch should not advance goal snapshot, got %#v", loaded.Mission.Features)
	}
}

func TestServiceMissionPlanPatchRejectsMalformedStructuredItems(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_mission_patch_malformed_items")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeMission,
		Objective: "Reject malformed mission plan items",
		Source:    session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	postJSONWithMethod(t, http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/mission/plan", map[string]any{
		"features": []map[string]any{
			{"id": "feature_api", "title": "API", "status": "pending"},
			{"id": "feature_api", "title": "Duplicate API", "status": "pending"},
		},
	}, http.StatusBadRequest, &apiErr)
	if !strings.Contains(apiErr.Error, "duplicate mission feature id") {
		t.Fatalf("expected duplicate feature error, got %#v", apiErr)
	}
	loaded, err := svc.store.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load goal: %v", err)
	}
	if loaded.Mission != nil && len(loaded.Mission.Features) != 0 {
		t.Fatalf("failed malformed mission patch should not advance snapshot, got %#v", loaded.Mission.Features)
	}
}

func TestServiceMissionPlanPatchRejectsEmptyRequest(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_mission_plan_patch_empty_request")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Objective: "Reject empty mission plan patch",
		Source:    session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	before, err := svc.store.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load goal baseline: %v", err)
	}
	beforeHistory, err := svc.store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load goal history: %v", err)
	}
	beforeEvents, err := svc.store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	postJSONWithMethod(t, http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/mission/plan", map[string]any{}, http.StatusBadRequest, &apiErr)
	if !strings.Contains(apiErr.Error, "at least one field") {
		t.Fatalf("expected empty mission plan patch error, got %#v", apiErr)
	}
	after, err := svc.store.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load goal: %v", err)
	}
	if after.Mode != before.Mode || after.UpdatedAt != before.UpdatedAt || after.Mission != nil {
		t.Fatalf("empty mission plan patch should not advance goal snapshot, before=%#v after=%#v", before, after)
	}
	afterHistory, err := svc.store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load goal history after empty patch: %v", err)
	}
	if len(afterHistory) != len(beforeHistory) || goalHistoryContainsType(afterHistory, "mission.plan.updated") {
		t.Fatalf("empty mission plan patch should not append history, before=%#v after=%#v", beforeHistory, afterHistory)
	}
	afterEvents, err := svc.store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load events after empty patch: %v", err)
	}
	if len(afterEvents) != len(beforeEvents) {
		t.Fatalf("empty mission plan patch should not append events, before=%#v after=%#v", beforeEvents, afterEvents)
	}
}

func TestServiceMissionPlanPatchReportsGoalWriteFailureAsServerError(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_mission_plan_patch_goal_write_error")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeMission,
		Objective: "Patch mission plan with blocked goal write",
		Source:    session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	blockWebGoalLockPath(t, svc.store, meta.ID)

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	postJSONWithMethod(t, http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/mission/plan", map[string]any{
		"features": []map[string]any{{"id": "feature_web", "title": "Web feature", "status": "pending"}},
	}, http.StatusInternalServerError, &apiErr)
	if !strings.Contains(apiErr.Error, "is a directory") {
		t.Fatalf("expected goal write error, got %#v", apiErr)
	}
	loaded, loadErr := svc.store.LoadGoal(meta.ID)
	if loadErr != nil {
		t.Fatalf("load goal: %v", loadErr)
	}
	if loaded.Mission != nil && len(loaded.Mission.Features) != 0 {
		t.Fatalf("failed mission plan patch should not advance goal snapshot, got %#v", loaded.Mission.Features)
	}
}

func TestServiceMissionPlanPatchTaskSyncReportsHistoryAppendError(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_mission_plan_task_history_error")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeMission,
		Objective: "Patch mission plan tasks with history",
		Source:    session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	blockWebGoalHistoryPath(t, svc.store, meta.ID)

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	postJSONWithMethod(t, http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/mission/plan", map[string]any{
		"create_tasks_from_plan": true,
		"features":               []map[string]any{{"id": "feature_web", "title": "Web feature", "status": "pending"}},
	}, http.StatusInternalServerError, &apiErr)
	if !strings.Contains(apiErr.Error, "goal-history.jsonl") {
		t.Fatalf("expected goal history append error, got %#v", apiErr)
	}
	loaded, loadErr := svc.store.LoadGoal(meta.ID)
	if loadErr != nil {
		t.Fatalf("load goal: %v", loadErr)
	}
	if loaded.Mission != nil && (len(loaded.Mission.Features) != 0 || loaded.Mission.CreateTasksFromPlan) {
		t.Fatalf("failed mission plan task patch should not advance goal snapshot, got %#v", loaded.Mission)
	}
	tasks, taskErr := svc.store.ListTasks(meta.ID)
	if taskErr != nil {
		t.Fatalf("list tasks: %v", taskErr)
	}
	if len(tasks) != 0 {
		t.Fatalf("failed mission plan task patch should not create tasks, got %#v", tasks)
	}
}

func TestServiceMissionPlanPatchRollsBackWhenTaskSyncFails(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_mission_plan_task_sync_error")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	original, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeMission,
		Objective: "Mission before task sync failure",
		Source:    session.GoalSourceWeb,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	blockWebTaskboardLockPath(t, svc.store, meta.ID)

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	postJSONWithMethod(t, http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/mission/plan", map[string]any{
		"create_tasks_from_plan": true,
		"features": []map[string]any{{
			"id":     "feature_task_sync",
			"title":  "Feature requiring task sync",
			"status": "pending",
		}},
	}, http.StatusInternalServerError, &apiErr)
	if !strings.Contains(apiErr.Error, "taskboard.lock") && !strings.Contains(apiErr.Error, "is a directory") {
		t.Fatalf("expected taskboard lock error, got %#v", apiErr)
	}
	loaded, loadErr := svc.store.LoadGoal(meta.ID)
	if loadErr != nil {
		t.Fatalf("load goal: %v", loadErr)
	}
	if loaded.Mode != original.Mode || loaded.GoalID != original.GoalID {
		t.Fatalf("failed task sync should restore original goal identity, got %#v original=%#v", loaded, original)
	}
	if loaded.Mission != nil && (len(loaded.Mission.Features) != 0 || loaded.Mission.CreateTasksFromPlan) {
		t.Fatalf("failed task sync should restore original mission plan, got %#v", loaded.Mission)
	}
	tasks, taskErr := svc.store.ListTasks(meta.ID)
	if taskErr != nil {
		t.Fatalf("list tasks: %v", taskErr)
	}
	if len(tasks) != 0 {
		t.Fatalf("failed task sync should not leave tasks, got %#v", tasks)
	}
}

func TestServiceMissionPlanPatchPlanModeReportsHistoryAppendError(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_mission_plan_planmode_history_error")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeMission,
		Objective: "Patch mission plan mode with history",
		Source:    session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	blockWebGoalHistoryPath(t, svc.store, meta.ID)

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	postJSONWithMethod(t, http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/mission/plan", map[string]any{
		"plan_status": session.MissionPlanStatusNeedsApproval,
		"features":    []map[string]any{{"id": "feature_web", "title": "Web feature", "status": "pending"}},
	}, http.StatusInternalServerError, &apiErr)
	if !strings.Contains(apiErr.Error, "goal-history.jsonl") {
		t.Fatalf("expected goal history append error, got %#v", apiErr)
	}
	loaded, loadErr := svc.store.LoadGoal(meta.ID)
	if loadErr != nil {
		t.Fatalf("load goal: %v", loadErr)
	}
	if loaded.Mission != nil && (len(loaded.Mission.Features) != 0 || loaded.Mission.PlanStatus == session.MissionPlanStatusNeedsApproval) {
		t.Fatalf("failed plan-mode mission patch should not advance goal snapshot, got %#v", loaded.Mission)
	}
	if planMode, planErr := svc.store.LoadPlanMode(meta.ID); !errors.Is(planErr, fs.ErrNotExist) {
		t.Fatalf("failed plan-mode mission patch should not create plan mode, got planMode=%#v err=%v", planMode, planErr)
	}
}

func TestServiceMissionPlanPatchRollsBackLinkedPlanModeEventWhenGoalMutationFails(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	svc.beforeAppendGoalMutation = func(sessionID string, goal session.SessionGoal, eventType string) error {
		if eventType != "mission.plan.updated" {
			t.Fatalf("unexpected goal mutation event type: %s", eventType)
		}
		return errors.New("forced goal mutation append failure")
	}
	meta := testSessionMetadata(t, "session_mission_plan_planmode_event_rollback")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeMission,
		Objective: "Patch mission plan mode and roll back event",
		Source:    session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	beforeEvents, err := svc.store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load events before patch: %v", err)
	}

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	postJSONWithMethod(t, http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/mission/plan", map[string]any{
		"plan_status": session.MissionPlanStatusNeedsApproval,
		"features":    []map[string]any{{"id": "feature_web", "title": "Web feature", "status": "pending"}},
	}, http.StatusInternalServerError, &apiErr)
	if !strings.Contains(apiErr.Error, "forced goal mutation append failure") {
		t.Fatalf("expected forced mutation append error, got %#v", apiErr)
	}
	afterEvents, err := svc.store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load events after failed patch: %v", err)
	}
	if len(afterEvents) != len(beforeEvents) {
		t.Fatalf("failed patch should roll back linked plan mode events, before=%#v after=%#v", beforeEvents, afterEvents)
	}
	if countWebEventType(afterEvents, "planmode.created") != countWebEventType(beforeEvents, "planmode.created") ||
		countWebEventType(afterEvents, "planmode.linked_goal") != countWebEventType(beforeEvents, "planmode.linked_goal") {
		t.Fatalf("failed patch left linked plan mode event behind, before=%#v after=%#v", beforeEvents, afterEvents)
	}
}

func TestServiceMissionPatchCannotApproveWithoutApprovalEndpoint(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "session_mission_patch_approve_blocked",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		RootSessionID:    "session_mission_patch_approve_blocked",
	}
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	goal, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeMission,
		Objective: "Ship mission approval safely",
		Source:    session.GoalSourceWeb,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	ts := httptest.NewServer(svc)
	defer ts.Close()

	patchReq, err := http.NewRequest(http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/mission/plan", bytes.NewBufferString(`{"plan_status":"approved"}`))
	if err != nil {
		t.Fatalf("new mission patch request: %v", err)
	}
	patchReq.Header.Set("Content-Type", "application/json")
	patchReq.Header.Set(webMutationHeader, "1")
	patchResp, err := http.DefaultClient.Do(patchReq)
	if err != nil {
		t.Fatalf("mission patch request: %v", err)
	}
	defer patchResp.Body.Close()
	if patchResp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(patchResp.Body)
		t.Fatalf("expected direct mission plan approval patch to fail, got %d body=%s", patchResp.StatusCode, string(body))
	}

	goalPatchReq, err := http.NewRequest(http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/goal", bytes.NewBufferString(`{"mission":{"plan_status":"approved"}}`))
	if err != nil {
		t.Fatalf("new goal patch request: %v", err)
	}
	goalPatchReq.Header.Set("Content-Type", "application/json")
	goalPatchReq.Header.Set(webMutationHeader, "1")
	goalPatchResp, err := http.DefaultClient.Do(goalPatchReq)
	if err != nil {
		t.Fatalf("goal patch request: %v", err)
	}
	defer goalPatchResp.Body.Close()
	if goalPatchResp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(goalPatchResp.Body)
		t.Fatalf("expected direct goal mission approval patch to fail, got %d body=%s", goalPatchResp.StatusCode, string(body))
	}

	loaded, err := svc.store.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load goal: %v", err)
	}
	if loaded.GoalID != goal.GoalID || loaded.Mission == nil || loaded.Mission.PlanStatus == session.MissionPlanStatusApproved {
		t.Fatalf("mission patch should not approve goal, got %#v", loaded.Mission)
	}
}

func TestServiceMissionPlanPatchResetsApprovedPlanToPendingGate(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "session_mission_patch_resets_gate",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		RootSessionID:    "session_mission_patch_resets_gate",
	}
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	goal, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeMission,
		Objective: "Reset approval when plan changes",
		Source:    session.GoalSourceWeb,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	goal.Mission.PlanStatus = session.MissionPlanStatusApproved
	goal.Mission.ApprovedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := svc.store.SaveGoal(meta.ID, goal); err != nil {
		t.Fatalf("save approved goal: %v", err)
	}
	firstPlan, err := svc.store.CreatePlanMode(meta.ID, session.PlanModeDraft{Enabled: true, Objective: "Approved plan", Source: session.PlanModeSourceWeb})
	if err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	if _, err := svc.store.SubmitPlanMode(meta.ID, session.PlanModeSubmitInput{
		Title:        "Approved plan",
		Summary:      "Approved plan",
		PlanMarkdown: "## Summary\nApproved.\n\n## Implementation Steps\nDo it.\n\n## Interfaces and Data Model\nNone.\n\n## Verification\nManual.\n\n## Risks\nNone.\n\n## Assumptions\nNone.",
		Verification: []string{"manual"},
		Source:       session.PlanModeSourceTool,
	}); err != nil {
		t.Fatalf("submit plan mode: %v", err)
	}
	if _, err := svc.store.ApprovePlanMode(meta.ID, session.PlanModeSourceWeb); err != nil {
		t.Fatalf("approve plan mode: %v", err)
	}
	if _, err := svc.store.MarkPlanModeExecuting(meta.ID, session.PlanModeSourceWeb); err != nil {
		t.Fatalf("mark executing: %v", err)
	}
	ts := httptest.NewServer(svc)
	defer ts.Close()

	var patched session.SessionGoal
	patchBody := map[string]any{
		"features": []map[string]any{{"id": "feature_after_approval", "title": "Changed scope", "status": "pending"}},
	}
	postJSONWithMethod(t, http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/mission/plan", patchBody, http.StatusOK, &patched)
	if patched.Mission == nil || patched.Mission.PlanStatus != session.MissionPlanStatusNeedsApproval || patched.Mission.ApprovedAt != "" {
		t.Fatalf("expected plan patch to reset approved mission, got %#v", patched.Mission)
	}
	secondPlan, err := svc.store.LoadPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("load reset plan mode: %v", err)
	}
	if secondPlan.PlanModeID == firstPlan.PlanModeID || secondPlan.LinkedGoalID != goal.GoalID || secondPlan.Status != session.PlanModeStatusPlanning {
		t.Fatalf("expected fresh pending linked plan mode, first=%#v second=%#v", firstPlan, secondPlan)
	}
}

func TestServiceMissionPlanPatchNoopKeepsApprovedPlan(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "session_mission_patch_noop_keeps_approved",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		RootSessionID:    "session_mission_patch_noop_keeps_approved",
	}
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	goal, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeMission,
		Objective: "Keep approval on no-op patch",
		Source:    session.GoalSourceWeb,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	approvedAt := time.Now().UTC().Format(time.RFC3339Nano)
	goal.Mission.PlanStatus = session.MissionPlanStatusApproved
	goal.Mission.ApprovedAt = approvedAt
	if err := svc.store.SaveGoal(meta.ID, goal); err != nil {
		t.Fatalf("save approved goal: %v", err)
	}
	firstPlan, err := svc.store.CreatePlanMode(meta.ID, session.PlanModeDraft{Enabled: true, Objective: "Approved plan", Source: session.PlanModeSourceWeb})
	if err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	if _, err := svc.store.SubmitPlanMode(meta.ID, session.PlanModeSubmitInput{
		Title:        "Approved plan",
		Summary:      "Approved plan",
		PlanMarkdown: "## Summary\nApproved.\n\n## Verification\nManual.",
		Verification: []string{"manual"},
		Source:       session.PlanModeSourceTool,
	}); err != nil {
		t.Fatalf("submit plan mode: %v", err)
	}
	if _, err := svc.store.ApprovePlanMode(meta.ID, session.PlanModeSourceWeb); err != nil {
		t.Fatalf("approve plan mode: %v", err)
	}
	if _, err := svc.store.MarkPlanModeExecuting(meta.ID, session.PlanModeSourceWeb); err != nil {
		t.Fatalf("mark executing: %v", err)
	}
	ts := httptest.NewServer(svc)
	defer ts.Close()

	var patched session.SessionGoal
	postJSONWithMethod(t, http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/mission/plan", map[string]any{
		"create_tasks_from_plan": false,
	}, http.StatusOK, &patched)
	if patched.Mission == nil || patched.Mission.PlanStatus != session.MissionPlanStatusApproved || patched.Mission.ApprovedAt != approvedAt {
		t.Fatalf("no-op patch should preserve approved mission, got %#v", patched.Mission)
	}
	secondPlan, err := svc.store.LoadPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("load plan mode: %v", err)
	}
	if secondPlan.PlanModeID != firstPlan.PlanModeID || secondPlan.Status != session.PlanModeStatusExecuting {
		t.Fatalf("no-op patch should not create a new pending gate, first=%#v second=%#v", firstPlan, secondPlan)
	}
}

func TestServiceGoalPatchMissionResetsApprovedPlanToPendingGate(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "session_goal_patch_resets_gate",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		RootSessionID:    "session_goal_patch_resets_gate",
	}
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	goal, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeMission,
		Objective: "Reset approval through generic goal patch",
		Source:    session.GoalSourceWeb,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	goal.Mission.PlanStatus = session.MissionPlanStatusApproved
	goal.Mission.ApprovedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := svc.store.SaveGoal(meta.ID, goal); err != nil {
		t.Fatalf("save approved goal: %v", err)
	}
	firstPlan, err := svc.store.CreatePlanMode(meta.ID, session.PlanModeDraft{Enabled: true, Objective: "Approved plan", Source: session.PlanModeSourceWeb})
	if err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	if _, err := svc.store.SubmitPlanMode(meta.ID, session.PlanModeSubmitInput{
		Title:        "Approved plan",
		Summary:      "Approved plan",
		PlanMarkdown: "## Summary\nApproved.\n\n## Implementation Steps\nDo it.\n\n## Interfaces and Data Model\nNone.\n\n## Verification\nManual.\n\n## Risks\nNone.\n\n## Assumptions\nNone.",
		Verification: []string{"manual"},
		Source:       session.PlanModeSourceTool,
	}); err != nil {
		t.Fatalf("submit plan mode: %v", err)
	}
	if _, err := svc.store.ApprovePlanMode(meta.ID, session.PlanModeSourceWeb); err != nil {
		t.Fatalf("approve plan mode: %v", err)
	}
	if _, err := svc.store.MarkPlanModeExecuting(meta.ID, session.PlanModeSourceWeb); err != nil {
		t.Fatalf("mark executing: %v", err)
	}
	ts := httptest.NewServer(svc)
	defer ts.Close()

	var patched session.SessionGoal
	postJSONWithMethod(t, http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/goal", map[string]any{
		"mission": map[string]any{
			"features": []map[string]any{{"id": "feature_goal_patch", "title": "Changed scope", "status": "pending"}},
		},
	}, http.StatusOK, &patched)
	if patched.Mission == nil || patched.Mission.PlanStatus != session.MissionPlanStatusNeedsApproval || patched.Mission.ApprovedAt != "" {
		t.Fatalf("expected goal mission patch to reset approved mission, got %#v", patched.Mission)
	}
	secondPlan, err := svc.store.LoadPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("load reset plan mode: %v", err)
	}
	if secondPlan.PlanModeID == firstPlan.PlanModeID || secondPlan.LinkedGoalID != goal.GoalID || secondPlan.Status != session.PlanModeStatusPlanning {
		t.Fatalf("expected fresh pending linked plan mode, first=%#v second=%#v", firstPlan, secondPlan)
	}
}

func TestServiceGoalPatchMissionPlanModeReportsHistoryAppendError(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_goal_patch_planmode_history_error")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeMission,
		Objective: "Patch generic mission with history",
		Source:    session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	blockWebGoalHistoryPath(t, svc.store, meta.ID)

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	postJSONWithMethod(t, http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/goal", map[string]any{
		"mission": map[string]any{
			"plan_status": session.MissionPlanStatusNeedsApproval,
			"features":    []map[string]any{{"id": "feature_goal_patch", "title": "Changed scope", "status": "pending"}},
		},
	}, http.StatusInternalServerError, &apiErr)
	if !strings.Contains(apiErr.Error, "goal-history.jsonl") {
		t.Fatalf("expected goal history append error, got %#v", apiErr)
	}
	loaded, loadErr := svc.store.LoadGoal(meta.ID)
	if loadErr != nil {
		t.Fatalf("load goal: %v", loadErr)
	}
	if loaded.Mission != nil && (len(loaded.Mission.Features) != 0 || loaded.Mission.PlanStatus == session.MissionPlanStatusNeedsApproval) {
		t.Fatalf("failed generic mission patch should not advance goal snapshot, got %#v", loaded.Mission)
	}
	if planMode, planErr := svc.store.LoadPlanMode(meta.ID); !errors.Is(planErr, fs.ErrNotExist) {
		t.Fatalf("failed generic mission patch should not create plan mode, got planMode=%#v err=%v", planMode, planErr)
	}
}

func TestServiceGoalPatchRollsBackWhenTaskSyncFails(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_goal_patch_task_sync_error")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	original, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeGoal,
		Objective: "Plain goal before task sync failure",
		Source:    session.GoalSourceWeb,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	blockWebTaskboardLockPath(t, svc.store, meta.ID)

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	postJSONWithMethod(t, http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/goal", map[string]any{
		"mission": map[string]any{
			"create_tasks_from_plan": true,
			"features": []map[string]any{{
				"id":     "feature_task_sync",
				"title":  "Feature requiring task sync",
				"status": "pending",
			}},
		},
	}, http.StatusInternalServerError, &apiErr)
	if !strings.Contains(apiErr.Error, "taskboard.lock") && !strings.Contains(apiErr.Error, "is a directory") {
		t.Fatalf("expected taskboard lock error, got %#v", apiErr)
	}
	loaded, loadErr := svc.store.LoadGoal(meta.ID)
	if loadErr != nil {
		t.Fatalf("load goal: %v", loadErr)
	}
	if loaded.Mode != original.Mode || loaded.Mission != nil || loaded.GoalID != original.GoalID {
		t.Fatalf("failed task sync should restore original goal, got %#v original=%#v", loaded, original)
	}
	tasks, taskErr := svc.store.ListTasks(meta.ID)
	if taskErr != nil {
		t.Fatalf("list tasks: %v", taskErr)
	}
	if len(tasks) != 0 {
		t.Fatalf("failed task sync should not leave tasks, got %#v", tasks)
	}
}

func TestServiceMissionValidationPatchResetsApprovedPlanToPendingGate(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "session_mission_validation_resets_gate",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		RootSessionID:    "session_mission_validation_resets_gate",
	}
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	goal, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeMission,
		Objective: "Reset approval when validation changes",
		Source:    session.GoalSourceWeb,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	goal.Mission.PlanStatus = session.MissionPlanStatusApproved
	goal.Mission.ApprovedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := svc.store.SaveGoal(meta.ID, goal); err != nil {
		t.Fatalf("save approved goal: %v", err)
	}
	ts := httptest.NewServer(svc)
	defer ts.Close()

	var patched session.SessionGoal
	postJSONWithMethod(t, http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/mission/validation", map[string]any{
		"validation_contract": []map[string]any{{"id": "validation_new", "kind": "manual", "description": "new validation", "status": "pending"}},
	}, http.StatusOK, &patched)
	if patched.Mission == nil || patched.Mission.PlanStatus != session.MissionPlanStatusNeedsApproval || patched.Mission.ApprovedAt != "" {
		t.Fatalf("expected validation patch to reset approved mission, got %#v", patched.Mission)
	}
	planMode, err := svc.store.LoadPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("expected linked plan mode after validation reset: %v", err)
	}
	if planMode.LinkedGoalID != goal.GoalID || planMode.Status != session.PlanModeStatusPlanning {
		t.Fatalf("unexpected reset plan mode: %#v", planMode)
	}
}

func TestServiceMissionValidationPatchRejectsEmptyRequest(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_mission_validation_patch_empty_request")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Objective: "Reject empty validation patch",
		Source:    session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	before, err := svc.store.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load goal baseline: %v", err)
	}
	beforeHistory, err := svc.store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load goal history: %v", err)
	}
	beforeEvents, err := svc.store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	postJSONWithMethod(t, http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/mission/validation", map[string]any{}, http.StatusBadRequest, &apiErr)
	if !strings.Contains(apiErr.Error, "at least one field") {
		t.Fatalf("expected empty validation patch error, got %#v", apiErr)
	}
	after, err := svc.store.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load goal: %v", err)
	}
	if after.UpdatedAt != before.UpdatedAt || len(after.ValidationPlan) != len(before.ValidationPlan) || after.Mission != nil {
		t.Fatalf("empty validation patch should not advance goal snapshot, before=%#v after=%#v", before, after)
	}
	afterHistory, err := svc.store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load goal history after empty patch: %v", err)
	}
	if len(afterHistory) != len(beforeHistory) || goalHistoryContainsType(afterHistory, "mission.validation.updated") {
		t.Fatalf("empty validation patch should not append history, before=%#v after=%#v", beforeHistory, afterHistory)
	}
	afterEvents, err := svc.store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load events after empty patch: %v", err)
	}
	if len(afterEvents) != len(beforeEvents) {
		t.Fatalf("empty validation patch should not append events, before=%#v after=%#v", beforeEvents, afterEvents)
	}
}

func TestServiceMissionValidationPlanPatchReportsHistoryAppendError(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_mission_validation_plan_history_error")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Objective: "Patch validation with history",
		Source:    session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	blockWebGoalHistoryPath(t, svc.store, meta.ID)

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	postJSONWithMethod(t, http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/mission/validation", map[string]any{
		"validation_plan": []map[string]any{{"id": "validation_web", "kind": "manual", "description": "web validation", "status": "pending"}},
	}, http.StatusInternalServerError, &apiErr)
	if !strings.Contains(apiErr.Error, "goal-history.jsonl") {
		t.Fatalf("expected goal history append error, got %#v", apiErr)
	}
	loaded, loadErr := svc.store.LoadGoal(meta.ID)
	if loadErr != nil {
		t.Fatalf("load goal: %v", loadErr)
	}
	if len(loaded.ValidationPlan) != 0 {
		t.Fatalf("failed validation plan patch should not advance goal snapshot, got %#v", loaded.ValidationPlan)
	}
}

func TestServiceMissionValidationPatchReportsGoalWriteFailureAsServerError(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_mission_validation_goal_write_error")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Objective: "Patch validation with blocked goal write",
		Source:    session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	blockWebGoalLockPath(t, svc.store, meta.ID)

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	postJSONWithMethod(t, http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/mission/validation", map[string]any{
		"validation_plan": []map[string]any{{"id": "validation_web", "kind": "manual", "description": "web validation", "status": "pending"}},
	}, http.StatusInternalServerError, &apiErr)
	if !strings.Contains(apiErr.Error, "is a directory") {
		t.Fatalf("expected goal write error, got %#v", apiErr)
	}
	loaded, loadErr := svc.store.LoadGoal(meta.ID)
	if loadErr != nil {
		t.Fatalf("load goal: %v", loadErr)
	}
	if len(loaded.ValidationPlan) != 0 {
		t.Fatalf("failed validation plan patch should not advance goal snapshot, got %#v", loaded.ValidationPlan)
	}
}

func TestServiceMissionValidationContractPatchReportsHistoryAppendError(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_mission_validation_contract_history_error")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	goal, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeMission,
		Objective: "Patch validation contract with history",
		Source:    session.GoalSourceWeb,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	goal.Mission.PlanStatus = session.MissionPlanStatusApproved
	goal.Mission.ApprovedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := svc.store.SaveGoal(meta.ID, goal); err != nil {
		t.Fatalf("save approved goal: %v", err)
	}
	blockWebGoalHistoryPath(t, svc.store, meta.ID)

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	postJSONWithMethod(t, http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/mission/validation", map[string]any{
		"validation_contract": []map[string]any{{"id": "validation_contract_web", "kind": "manual", "description": "web validation", "status": "pending"}},
	}, http.StatusInternalServerError, &apiErr)
	if !strings.Contains(apiErr.Error, "goal-history.jsonl") {
		t.Fatalf("expected goal history append error, got %#v", apiErr)
	}
	loaded, loadErr := svc.store.LoadGoal(meta.ID)
	if loadErr != nil {
		t.Fatalf("load goal: %v", loadErr)
	}
	if loaded.Mission != nil && (len(loaded.Mission.ValidationContract) != 0 || loaded.Mission.PlanStatus != session.MissionPlanStatusApproved || loaded.Mission.ApprovedAt == "") {
		t.Fatalf("failed validation contract patch should not advance goal snapshot, got %#v", loaded.Mission.ValidationContract)
	}
	if planMode, planErr := svc.store.LoadPlanMode(meta.ID); !errors.Is(planErr, fs.ErrNotExist) {
		t.Fatalf("failed validation contract patch should not create plan mode, got planMode=%#v err=%v", planMode, planErr)
	}
}

func TestServiceMissionApproveExecutingPlanModeAppendsApprovalFact(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "session_mission_approve_executing_fact",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		RootSessionID:    "session_mission_approve_executing_fact",
	}
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	goal, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeMission,
		Objective: "Append approval facts",
		Source:    session.GoalSourceWeb,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	if _, err := svc.store.CreatePlanMode(meta.ID, session.PlanModeDraft{Enabled: true, Objective: "Approved plan", Source: session.PlanModeSourceWeb}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	if _, err := svc.store.SubmitPlanMode(meta.ID, session.PlanModeSubmitInput{
		Title:        "Approved plan",
		Summary:      "Approved plan",
		PlanMarkdown: "## Summary\nApproved.\n\n## Implementation Steps\nDo it.\n\n## Interfaces and Data Model\nNone.\n\n## Verification\nManual.\n\n## Risks\nNone.\n\n## Assumptions\nNone.",
		Verification: []string{"manual"},
		Source:       session.PlanModeSourceTool,
	}); err != nil {
		t.Fatalf("submit plan mode: %v", err)
	}
	if _, err := svc.store.ApprovePlanMode(meta.ID, session.PlanModeSourceWeb); err != nil {
		t.Fatalf("approve plan mode: %v", err)
	}
	if _, err := svc.store.MarkPlanModeExecuting(meta.ID, session.PlanModeSourceWeb); err != nil {
		t.Fatalf("mark executing: %v", err)
	}
	ts := httptest.NewServer(svc)
	defer ts.Close()

	var approved session.SessionGoal
	postJSON(t, ts.URL+"/api/sessions/"+meta.ID+"/mission/plan/approve", map[string]any{}, http.StatusOK, &approved)
	if approved.GoalID != goal.GoalID || approved.Mission == nil || approved.Mission.PlanStatus != session.MissionPlanStatusApproved {
		t.Fatalf("expected executing plan approval to sync mission snapshot, got %#v", approved.Mission)
	}
	history, err := svc.store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load goal history: %v", err)
	}
	if !goalHistoryContainsType(history, "mission.plan.approved") {
		t.Fatalf("expected mission.plan.approved history after executing plan approval, got %#v", history)
	}
}

func TestServiceMissionPlanApproveRollsBackHistoryWhenEventAppendFails(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_mission_approve_event_error")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	goal, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeMission,
		Objective: "Approve mission plan with blocked events",
		Source:    session.GoalSourceWeb,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	beforeHistory, err := svc.store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load goal history: %v", err)
	}
	blockWebEventsPath(t, svc.store, meta.ID)

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	postJSON(t, ts.URL+"/api/sessions/"+meta.ID+"/mission/plan/approve", map[string]any{}, http.StatusInternalServerError, &apiErr)
	if !strings.Contains(apiErr.Error, "events.jsonl") {
		t.Fatalf("expected event append error, got %#v", apiErr)
	}
	loaded, loadErr := svc.store.LoadGoal(meta.ID)
	if loadErr != nil {
		t.Fatalf("load goal: %v", loadErr)
	}
	if loaded.GoalID != goal.GoalID || loaded.Mission == nil || session.NormalizeMissionPlanStatus(loaded.Mission.PlanStatus) == session.MissionPlanStatusApproved || loaded.Mission.ApprovedAt != "" {
		t.Fatalf("failed mission approval should restore unapproved goal snapshot, got %#v", loaded.Mission)
	}
	afterHistory, err := svc.store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load goal history after failed approval: %v", err)
	}
	if len(afterHistory) != len(beforeHistory) || goalHistoryContainsType(afterHistory, "mission.plan.approved") {
		t.Fatalf("failed mission approval should restore goal history, before=%#v after=%#v", beforeHistory, afterHistory)
	}
}

func TestServiceMissionPlanApproveReportsHistoryLoadErrorAsServerError(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_mission_approve_history_load_error")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeMission,
		Objective: "Approve mission plan with unreadable history",
		Source:    session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	blockWebGoalHistoryPath(t, svc.store, meta.ID)

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var apiErr ErrorResponse
	postJSON(t, ts.URL+"/api/sessions/"+meta.ID+"/mission/plan/approve", map[string]any{}, http.StatusInternalServerError, &apiErr)
	if !strings.Contains(apiErr.Error, "goal-history.jsonl") {
		t.Fatalf("expected goal history load error, got %#v", apiErr)
	}
}

func TestServiceMissionPlanApproveReportsLinkedPlanModeEventAppendError(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_mission_approve_planmode_event_error")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:             true,
		Mode:                session.GoalModeMission,
		Objective:           "Require linked plan mode event",
		ValidationPlan:      []string{"manual: validate approval"},
		Features:            []string{"approval gate"},
		RequirePlanApproval: true,
		Source:              session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	if _, _, err := svc.store.RecordGoalProgress(meta.ID, session.GoalProgressInput{
		Source: session.GoalSourceTool,
		Kind:   "validation",
		FeatureUpdates: []session.MissionFeatureProgressUpdate{{
			ID:                "feature_0001",
			ClaimedAssertions: []string{"validation_0001"},
		}},
		ValidationUpdates: []session.GoalValidationProgressUpdate{{
			ID:     "validation_0001",
			Status: "verified",
		}},
	}); err != nil {
		t.Fatalf("record coverage progress: %v", err)
	}
	beforeGoal, err := svc.store.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load goal before failed approval: %v", err)
	}
	blockWebEventsPath(t, svc.store, meta.ID)

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/sessions/"+meta.ID+"/mission/plan/approve", map[string]any{}, http.StatusInternalServerError)
	if !strings.Contains(errResp.Error, "events.jsonl") {
		t.Fatalf("expected events path in error, got %#v", errResp)
	}
	loaded, loadErr := svc.store.LoadGoal(meta.ID)
	if loadErr != nil {
		t.Fatalf("load goal after failed approval: %v", loadErr)
	}
	if loaded.GoalID != beforeGoal.GoalID || loaded.Mission == nil || loaded.Mission.PlanStatus != beforeGoal.Mission.PlanStatus {
		t.Fatalf("failed linked plan mode event should restore pre-request goal, before=%#v after=%#v", beforeGoal, loaded)
	}
	if planMode, planErr := svc.store.LoadPlanMode(meta.ID); !errors.Is(planErr, fs.ErrNotExist) {
		t.Fatalf("failed linked plan mode event should restore absent plan mode, got plan=%#v err=%v", planMode, planErr)
	}
}

func TestServiceMissionPlanApproveRejectsGoalWithoutMissionPlan(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "session_mission_approve_plain_goal",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		RootSessionID:    "session_mission_approve_plain_goal",
	}
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeGoal,
		Objective: "Plain goal should not approve a missing mission plan",
		Source:    session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/sessions/"+meta.ID+"/mission/plan/approve", map[string]any{}, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "mission plan is required") {
		t.Fatalf("expected missing mission plan error, got %#v", errResp)
	}
	loaded, err := svc.store.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load goal: %v", err)
	}
	if loaded.Mode != session.GoalModeGoal || loaded.Mission != nil {
		t.Fatalf("approval mutated plain goal: mode=%s mission=%#v", loaded.Mode, loaded.Mission)
	}
	history, err := svc.store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load goal history: %v", err)
	}
	if goalHistoryContainsType(history, "mission.plan.approved") {
		t.Fatalf("unexpected mission approval history for plain goal: %#v", history)
	}
}

func TestServiceGoalFactsAndMissionCoverageApproval(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "session_goal_facts",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		RootSessionID:    "session_goal_facts",
	}
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	goal, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:        true,
		Mode:           session.GoalModeMission,
		Objective:      "Expose mission facts",
		ValidationPlan: []string{"manual: validate coverage"},
		Features:       []string{"facts panel"},
		Milestones:     []string{"validation pass"},
		Source:         session.GoalSourceWeb,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	ts := httptest.NewServer(svc)
	defer ts.Close()
	approveReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/sessions/"+meta.ID+"/mission/plan/approve", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("new approve request: %v", err)
	}
	approveReq.Header.Set("Content-Type", "application/json")
	approveReq.Header.Set(webMutationHeader, "1")
	approveResp, err := http.DefaultClient.Do(approveReq)
	if err != nil {
		t.Fatalf("approve mission plan: %v", err)
	}
	defer approveResp.Body.Close()
	if approveResp.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(approveResp.Body)
		t.Fatalf("expected coverage conflict, got %d body=%s", approveResp.StatusCode, string(body))
	}
	if _, _, err := svc.store.RecordGoalProgress(meta.ID, session.GoalProgressInput{
		Source:  session.GoalSourceTool,
		Kind:    "handoff",
		Summary: "Evaluator evidence recorded.",
		FeatureUpdates: []session.MissionFeatureProgressUpdate{{
			ID:                "feature_0001",
			ClaimedAssertions: []string{"validation_0001"},
			ChildSessionIDs:   []string{"child_eval"},
		}},
		MilestoneUpdates: []session.MissionMilestoneProgressUpdate{{
			ID:            "milestone_0001",
			ValidationIDs: []string{"validation_0001"},
		}},
		ValidationUpdates: []session.GoalValidationProgressUpdate{{
			ID:     "validation_0001",
			Status: "verified",
			EvaluatorEvidence: []session.GoalEvaluatorEvidence{{
				ChildSessionID: "child_eval",
				Summary:        "independent evaluator passed",
				Status:         "verified",
			}},
		}},
	}); err != nil {
		t.Fatalf("record progress: %v", err)
	}
	detail, err := svc.sessionDetail(meta.ID, 40)
	if err != nil {
		t.Fatalf("session detail: %v", err)
	}
	if detail.Goal == nil || detail.GoalFacts == nil {
		t.Fatalf("expected goal facts in detail: %#v", detail)
	}
	if detail.GoalFacts.Coverage.ApprovalBlocked || detail.GoalFacts.Coverage.CoveredAssertions != 1 {
		t.Fatalf("expected covered validation facts, got %#v", detail.GoalFacts.Coverage)
	}
	if detail.GoalFacts.EvaluatorEvidenceCount != 1 || len(detail.GoalFacts.Progress) != 1 {
		t.Fatalf("expected evaluator evidence and progress facts, got %#v", detail.GoalFacts)
	}
	var approved session.SessionGoal
	postJSON(t, ts.URL+"/api/sessions/"+meta.ID+"/mission/plan/approve", map[string]any{}, http.StatusOK, &approved)
	if approved.GoalID != goal.GoalID || approved.Mission.PlanStatus != "approved" {
		t.Fatalf("expected mission approved after coverage, got %#v", approved.Mission)
	}
}

func TestServiceSessionDetailReportsGoalHistoryLoadError(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	meta := testSessionMetadata(t, "session_detail_goal_history_error")
	if err := svc.store.Create(meta, testSessionState(session.StatusCompleted)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeGoal,
		Objective: "Expose goal facts",
		Source:    session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	historyPath := filepath.Join(svc.store.SessionDir(meta.ID), "artifacts", "goal-history.jsonl")
	if err := os.WriteFile(historyPath, []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatalf("write invalid goal history: %v", err)
	}

	ts := httptest.NewServer(svc)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions/" + meta.ID)
	if err != nil {
		t.Fatalf("get session detail: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected goal history load error to return 500, got %d body=%s", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), "goal-history.jsonl") {
		t.Fatalf("expected goal history path in response, got body=%s", string(body))
	}
}

func TestServiceSessionDetailReportsSnapshotLoadErrors(t *testing.T) {
	cases := []struct {
		name      string
		pathParts []string
		setup     func(t *testing.T, svc *Service, sessionID string)
		wantPath  string
	}{
		{
			name:      "contract",
			pathParts: []string{"contract.json"},
			setup: func(t *testing.T, svc *Service, sessionID string) {
				t.Helper()
				if err := svc.store.SaveContract(sessionID, session.SessionContract{
					SchemaVersion: 1,
					ContractID:    "contract_test",
					Source:        "user_instruction",
					TrustSource:   "builtin",
					Profile:       "default",
					CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
					UpdatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
				}); err != nil {
					t.Fatalf("save contract: %v", err)
				}
			},
			wantPath: "contract.json",
		},
		{
			name:      "checkpoint",
			pathParts: []string{"checkpoints", "longrun-latest.json"},
			setup: func(t *testing.T, svc *Service, sessionID string) {
				t.Helper()
				if err := svc.store.SaveLongRunCheckpoint(sessionID, session.LongRunCheckpoint{
					SchemaVersion: 1,
					SessionID:     sessionID,
					Provider:      "openai",
					Model:         "gpt-5.4",
					Workdir:       t.TempDir(),
					CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
				}); err != nil {
					t.Fatalf("save checkpoint: %v", err)
				}
			},
			wantPath: "longrun-latest.json",
		},
		{
			name:      "parent_coordination",
			pathParts: []string{"parent-coordination.json"},
			setup: func(t *testing.T, svc *Service, sessionID string) {
				t.Helper()
				if err := svc.store.SaveParentCoordination(sessionID, session.ParentCoordination{
					SchemaVersion:   1,
					ParentSessionID: sessionID,
					WaitMode:        "wait-all",
					UpdatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
				}); err != nil {
					t.Fatalf("save parent coordination: %v", err)
				}
			},
			wantPath: "parent-coordination.json",
		},
		{
			name:      "goal",
			pathParts: []string{"goal.json"},
			setup: func(t *testing.T, svc *Service, sessionID string) {
				t.Helper()
				if _, err := svc.store.CreateGoal(sessionID, session.GoalDraft{
					Enabled:   true,
					Mode:      session.GoalModeGoal,
					Objective: "Expose goal snapshot",
					Source:    session.GoalSourceWeb,
				}); err != nil {
					t.Fatalf("create goal: %v", err)
				}
			},
			wantPath: "goal.json",
		},
		{
			name:      "planmode",
			pathParts: []string{"planmode.json"},
			setup: func(t *testing.T, svc *Service, sessionID string) {
				t.Helper()
				if _, err := svc.store.CreatePlanMode(sessionID, session.PlanModeDraft{
					Enabled:   true,
					Objective: "Plan the next step",
					Source:    session.PlanModeSourceWeb,
				}); err != nil {
					t.Fatalf("create plan mode: %v", err)
				}
			},
			wantPath: "planmode.json",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t, "")
			svc, err := New(cfg, Options{WorkerCount: 0})
			if err != nil {
				t.Fatalf("new service: %v", err)
			}
			defer svc.Close()

			meta := testSessionMetadata(t, "session_detail_"+tc.name+"_load_error")
			if err := svc.store.Create(meta, testSessionState(session.StatusCompleted)); err != nil {
				t.Fatalf("create session: %v", err)
			}
			tc.setup(t, svc, meta.ID)
			corruptPath := filepath.Join(append([]string{svc.store.SessionDir(meta.ID)}, tc.pathParts...)...)
			if err := os.WriteFile(corruptPath, []byte("{not-json}\n"), 0o600); err != nil {
				t.Fatalf("write invalid snapshot: %v", err)
			}

			ts := httptest.NewServer(svc)
			defer ts.Close()

			resp, err := http.Get(ts.URL + "/api/sessions/" + meta.ID)
			if err != nil {
				t.Fatalf("get session detail: %v", err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusInternalServerError {
				t.Fatalf("expected snapshot load error to return 500, got %d body=%s", resp.StatusCode, string(body))
			}
			if !strings.Contains(string(body), tc.wantPath) {
				t.Fatalf("expected %s in response, got body=%s", tc.wantPath, string(body))
			}
		})
	}
}

func TestServicePlanModeGetAndParentQueueGate(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "session_planmode_api",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyAutonomous,
		RootSessionID:    "session_planmode_api",
	}
	if err := svc.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_approval", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	planMode, err := svc.store.CreatePlanMode(meta.ID, session.PlanModeDraft{Enabled: true, Objective: "Plan before queue", Source: session.PlanModeSourceWeb})
	if err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	ts := httptest.NewServer(svc)
	defer ts.Close()

	var loaded session.PlanModeState
	resp, err := http.Get(ts.URL + "/api/sessions/" + meta.ID + "/planmode")
	if err != nil {
		t.Fatalf("get planmode: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("unexpected get status: %d body=%s", resp.StatusCode, string(body))
	}
	if err := json.NewDecoder(resp.Body).Decode(&loaded); err != nil {
		t.Fatalf("decode planmode: %v", err)
	}
	if loaded.PlanModeID != planMode.PlanModeID || loaded.Status != session.PlanModeStatusPlanning {
		t.Fatalf("unexpected planmode response: %#v", loaded)
	}

	errResp := postJSONError(t, ts.URL+"/api/queue/jobs", map[string]any{
		"parent_session_id": meta.ID,
		"prompt":            "child work",
	}, http.StatusConflict)
	if !strings.Contains(errResp.Error, "plan mode is pending") {
		t.Fatalf("expected pending plan mode queue error, got %#v", errResp)
	}
}

func TestServiceQueueSubmitReportsStoreAppendFailureAsServerError(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	queueRoot := filepath.Join(svc.store.Root(), "_queue")
	if err := os.MkdirAll(filepath.Dir(queueRoot), 0o700); err != nil {
		t.Fatalf("create session root parent: %v", err)
	}
	if err := os.WriteFile(queueRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("block queue root: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/queue/jobs", bytes.NewBufferString(`{"prompt":"queued child work"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(webMutationHeader, "1")
	svc.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected status: %d want %d body=%s", recorder.Code, http.StatusInternalServerError, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "not a directory") {
		t.Fatalf("expected queue store error in response, got body=%s", recorder.Body.String())
	}
}

func TestServiceQueueSubmitRejectsUnknownProviderWithStructuredError(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/queue/jobs", map[string]any{
		"prompt":   "queued child work",
		"provider": "missing-provider",
	}, http.StatusBadRequest)
	if errResp.Code != errorCodeUnknownProvider || errResp.Action == "" {
		t.Fatalf("expected structured unknown provider error, got %#v", errResp)
	}
}

func TestServiceQueueSubmitRejectsUnsupportedWaitMode(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/queue/jobs", map[string]any{
		"prompt":    "queued child work",
		"wait_mode": "eventually",
	}, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "unsupported wait mode") {
		t.Fatalf("expected unsupported wait mode error, got %#v", errResp)
	}
	jobs, listErr := svc.store.ListJobs(10)
	if listErr != nil {
		t.Fatalf("list jobs: %v", listErr)
	}
	if len(jobs) != 0 {
		t.Fatalf("unsupported wait mode should not persist queue jobs, got %#v", jobs)
	}
}

func TestServiceStartSessionWithPlanModePersistsPlanAndDetail(t *testing.T) {
	server := newSubmitPlanServer()
	defer server.Close()

	cfg := testConfig(t, server.URL)
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var result LaunchResponse
	postJSON(t, ts.URL+"/api/sessions/start", map[string]any{
		"prompt": "Plan this change before editing.",
		"mode":   "exec",
		"plan_mode": map[string]any{
			"enabled": true,
		},
	}, http.StatusAccepted, &result)

	waitFor(t, 4*time.Second, func() bool {
		state, err := svc.store.LoadState(result.SessionID)
		return err == nil && state.Status == session.StatusAwaitingInput && state.Phase == "plan_approval"
	}, func() string {
		state, err := svc.store.LoadState(result.SessionID)
		if err != nil {
			return err.Error()
		}
		data, marshalErr := json.Marshal(state)
		if marshalErr != nil {
			return marshalErr.Error()
		}
		return string(data)
	})
	planMode, err := svc.store.LoadPlanMode(result.SessionID)
	if err != nil {
		t.Fatalf("load plan mode: %v", err)
	}
	if planMode.Status != session.PlanModeStatusAwaitingApproval || planMode.PlanVersion != 1 || !strings.Contains(planMode.PlanMarkdown, "Plan Mode test plan") {
		t.Fatalf("expected submitted plan mode, got %#v", planMode)
	}
	detail, err := svc.sessionDetail(result.SessionID, 20)
	if err != nil {
		t.Fatalf("session detail: %v", err)
	}
	if detail.PlanMode == nil || detail.PlanMode.PlanModeID != planMode.PlanModeID {
		t.Fatalf("expected detail plan mode snapshot, got %#v", detail.PlanMode)
	}
}

func TestServicePlanModeInputDetailKeepsLiveHandle(t *testing.T) {
	server := newPlanInputThenSubmitPlanServer()
	defer server.Close()

	cfg := testConfig(t, server.URL)
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var result LaunchResponse
	postJSON(t, ts.URL+"/api/sessions/start", map[string]any{
		"prompt": "Ask for Plan Mode input before editing.",
		"mode":   "exec",
		"plan_mode": map[string]any{
			"enabled": true,
		},
	}, http.StatusAccepted, &result)

	waitFor(t, 4*time.Second, func() bool {
		state, err := svc.store.LoadState(result.SessionID)
		if err != nil || state.Status != session.StatusAwaitingInput || state.Phase != "plan_input" {
			return false
		}
		planMode, err := svc.store.LoadPlanMode(result.SessionID)
		return err == nil && planMode.Status == session.PlanModeStatusAwaitingUserInput && planMode.PendingRequest != nil
	}, func() string {
		state, err := svc.store.LoadState(result.SessionID)
		if err != nil {
			return err.Error()
		}
		planMode, _ := svc.store.LoadPlanMode(result.SessionID)
		data, marshalErr := json.Marshal(map[string]any{"state": state, "plan_mode": planMode})
		if marshalErr != nil {
			return marshalErr.Error()
		}
		return string(data)
	})

	var detail SessionDetailResponse
	postGetJSON(t, ts.URL+"/api/sessions/"+result.SessionID, &detail)
	if !detail.ActiveHandle || detail.ActiveHandleOwner.State != "current_process" {
		t.Fatalf("expected Plan Mode input detail read to retain current-process handle, got %#v", detail.ActiveHandleOwner)
	}
	planMode, err := svc.store.LoadPlanMode(result.SessionID)
	if err != nil {
		t.Fatalf("load plan mode: %v", err)
	}
	if planMode.PendingRequest == nil {
		t.Fatalf("expected pending request after detail read, got %#v", planMode)
	}

	errResp := postJSONError(t, ts.URL+"/api/sessions/"+result.SessionID+"/planmode/input", map[string]any{
		"answers": []map[string]any{{
			"question_id": "scope_choice",
			"label":       "Narrow",
			"value":       "Narrow",
		}},
	}, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "request_id is required") {
		t.Fatalf("expected missing request id rejection before live input delivery, got %#v", errResp)
	}

	errResp = postJSONError(t, ts.URL+"/api/sessions/"+result.SessionID+"/planmode/input", map[string]any{
		"request_id": planMode.PendingRequest.RequestID,
		"answers": []map[string]any{{
			"question_id": "unknown_choice",
			"label":       "Narrow",
			"value":       "Narrow",
		}},
	}, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "unknown plan input question id") {
		t.Fatalf("expected invalid answer rejection before live input delivery, got %#v", errResp)
	}

	errResp = postJSONError(t, ts.URL+"/api/sessions/"+result.SessionID+"/planmode/input", map[string]any{
		"request_id": planMode.PendingRequest.RequestID,
		"answers": []map[string]any{{
			"question_id": "scope_choice",
			"label":       "Surprise",
			"value":       "Surprise",
		}},
	}, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "must match an offered option") {
		t.Fatalf("expected invalid option rejection before live input delivery, got %#v", errResp)
	}

	var inputLaunch LaunchResponse
	postJSON(t, ts.URL+"/api/sessions/"+result.SessionID+"/planmode/input", map[string]any{
		"request_id": planMode.PendingRequest.RequestID,
		"answers": []map[string]any{{
			"question_id": "scope_choice",
			"label":       "Narrow",
			"value":       "Narrow",
		}},
	}, http.StatusAccepted, &inputLaunch)
	waitFor(t, 4*time.Second, func() bool {
		state, err := svc.store.LoadState(result.SessionID)
		return err == nil && state.Status == session.StatusAwaitingInput && state.Phase == "plan_approval"
	}, func() string {
		state, err := svc.store.LoadState(result.SessionID)
		if err != nil {
			return err.Error()
		}
		data, marshalErr := json.Marshal(state)
		if marshalErr != nil {
			return marshalErr.Error()
		}
		return string(data)
	})
	planMode, err = svc.store.LoadPlanMode(result.SessionID)
	if err != nil {
		t.Fatalf("load submitted plan mode: %v", err)
	}
	if planMode.Status != session.PlanModeStatusAwaitingApproval || !strings.Contains(planMode.PlanMarkdown, "Plan after input") {
		t.Fatalf("expected submitted plan after live input, got %#v", planMode)
	}
}

func TestServicePlanModeApproveAppendsReplayableUserMessage(t *testing.T) {
	server := newFinishServer()
	defer server.Close()

	cfg := testConfig(t, server.URL)
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_planmode_approve")
	meta.Mode = session.ModeExec
	meta.CompletionPolicy = session.CompletionPolicyAutonomous
	meta.RootSessionID = meta.ID
	if err := svc.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_approval", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreatePlanMode(meta.ID, session.PlanModeDraft{Enabled: true, Objective: "Approve plan", Source: session.PlanModeSourceWeb}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	if _, err := svc.store.SubmitPlanMode(meta.ID, session.PlanModeSubmitInput{
		Title:        "Plan",
		Summary:      "Approved plan should execute.",
		PlanMarkdown: "# Plan\n\nExecute after approval.",
		Verification: []string{"go test ./internal/webconsole"},
		Source:       session.PlanModeSourceTool,
	}); err != nil {
		t.Fatalf("submit plan: %v", err)
	}
	ts := httptest.NewServer(svc)
	defer ts.Close()

	var launch LaunchResponse
	postJSON(t, ts.URL+"/api/sessions/"+meta.ID+"/planmode/approve", map[string]any{}, http.StatusAccepted, &launch)
	waitFor(t, 4*time.Second, func() bool {
		state, err := svc.store.LoadState(meta.ID)
		return err == nil && state.Status == session.StatusCompleted
	}, func() string {
		state, err := svc.store.LoadState(meta.ID)
		if err != nil {
			return err.Error()
		}
		data, marshalErr := json.Marshal(state)
		if marshalErr != nil {
			return marshalErr.Error()
		}
		return string(data)
	})
	messages, err := svc.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	var foundApproval bool
	for _, msg := range messages {
		if msg.Role == "user" && msg.Meta["source"] == "planmode_approval" && strings.Contains(msg.Text, "Implement the approved Plan Mode plan") {
			foundApproval = true
		}
	}
	if !foundApproval {
		t.Fatalf("expected replayable planmode approval user message, got %#v", messages)
	}
}

func TestServicePlanModeApproveRejectsPlanningBeforeLaunch(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_planmode_approve_planning")
	meta.Mode = session.ModeExec
	meta.CompletionPolicy = session.CompletionPolicyAutonomous
	meta.RootSessionID = meta.ID
	if err := svc.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_input", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreatePlanMode(meta.ID, session.PlanModeDraft{Enabled: true, Objective: "Approve too early", Source: session.PlanModeSourceWeb}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/sessions/"+meta.ID+"/planmode/approve", map[string]any{}, http.StatusConflict)
	if !strings.Contains(errResp.Error, "not awaiting approval") {
		t.Fatalf("expected plan status conflict, got %#v", errResp)
	}
	state, err := svc.store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.Status != session.StatusAwaitingInput || state.Phase != "plan_input" {
		t.Fatalf("invalid approve should not claim or fail session, got %#v", state)
	}
	planMode, err := svc.store.LoadPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("load plan mode: %v", err)
	}
	if planMode.Status != session.PlanModeStatusPlanning || planMode.PlanVersion != 0 {
		t.Fatalf("invalid approve should not mutate plan mode, got %#v", planMode)
	}
	if svc.hasActiveHandle(meta.ID) {
		t.Fatalf("invalid approve should not launch background continue")
	}
}

func TestServicePlanModeApproveReturnsConflictWhenLinkedMissionCoverageBlocks(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_planmode_approve_coverage_conflict")
	meta.Mode = session.ModeExec
	meta.CompletionPolicy = session.CompletionPolicyAutonomous
	meta.RootSessionID = meta.ID
	if err := svc.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_approval", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	goal, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:             true,
		Mode:                session.GoalModeMission,
		Objective:           "Reject uncovered mission approval",
		ValidationPlan:      []string{"go test ./internal/webconsole"},
		Features:            []string{"web approval"},
		RequirePlanApproval: true,
		Source:              session.GoalSourceWeb,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	planMode, created, err := svc.store.EnsurePlanModeForGoal(meta.ID, goal, session.PlanModeSourceWeb)
	if err != nil {
		t.Fatalf("ensure plan mode: %v", err)
	}
	if !created || planMode.LinkedGoalID != goal.GoalID {
		t.Fatalf("expected linked plan mode, created=%v plan=%#v goal=%#v", created, planMode, goal)
	}
	if _, err := svc.store.SubmitPlanMode(meta.ID, session.PlanModeSubmitInput{
		Title:        "Plan",
		Summary:      "Coverage is still incomplete.",
		PlanMarkdown: "# Plan\n\nDo it.",
		Verification: []string{"go test ./internal/webconsole"},
		Source:       session.PlanModeSourceTool,
	}); err != nil {
		t.Fatalf("submit plan: %v", err)
	}
	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/sessions/"+meta.ID+"/planmode/approve", map[string]any{}, http.StatusConflict)
	if !strings.Contains(errResp.Error, "mission validation coverage blocks approval") {
		t.Fatalf("expected coverage conflict, got %#v", errResp)
	}
	after, err := svc.store.LoadPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("load plan mode after conflict: %v", err)
	}
	if after.Status != session.PlanModeStatusAwaitingApproval {
		t.Fatalf("coverage conflict should not advance plan mode, got %#v", after)
	}
	if svc.hasActiveHandle(meta.ID) {
		t.Fatalf("coverage conflict should not launch background continue")
	}
}

func TestServicePlanModeReviseRejectsPendingInputBeforeLaunch(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_planmode_revise_input")
	meta.Mode = session.ModeExec
	meta.CompletionPolicy = session.CompletionPolicyAutonomous
	meta.RootSessionID = meta.ID
	if err := svc.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_input", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreatePlanMode(meta.ID, session.PlanModeDraft{Enabled: true, Objective: "Revise while input pending", Source: session.PlanModeSourceWeb}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	pendingRequest := session.PlanModeInputRequest{
		RequestID:  "pmq_revise_input",
		ToolCallID: "call_revise_input",
		Questions: []session.PlanModeInputQuestion{{
			ID:       "scope_choice",
			Header:   "Scope",
			Question: "Which scope?",
			Options: []session.PlanModeInputOption{
				{Label: "Narrow (Recommended)", Description: "Keep it focused."},
				{Label: "Broad", Description: "Include cleanup."},
			},
		}},
		Status:    "pending",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if _, err := svc.store.SetPlanModePendingRequest(meta.ID, pendingRequest, session.PlanModeSourceTool); err != nil {
		t.Fatalf("set pending request: %v", err)
	}
	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/sessions/"+meta.ID+"/planmode/revise", map[string]any{
		"message": "Revise before answering input.",
	}, http.StatusConflict)
	if !strings.Contains(errResp.Error, "cannot be revised") {
		t.Fatalf("expected revise status conflict, got %#v", errResp)
	}
	state, err := svc.store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.Status != session.StatusAwaitingInput || state.Phase != "plan_input" {
		t.Fatalf("invalid revise should not claim or fail session, got %#v", state)
	}
	planMode, err := svc.store.LoadPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("load plan mode: %v", err)
	}
	if planMode.Status != session.PlanModeStatusAwaitingUserInput || planMode.PendingRequest == nil || planMode.PendingRequest.RequestID != pendingRequest.RequestID {
		t.Fatalf("invalid revise should keep pending input, got %#v", planMode)
	}
	messages, err := svc.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("invalid revise should not append continuation messages, got %#v", messages)
	}
	if svc.hasActiveHandle(meta.ID) {
		t.Fatalf("invalid revise should not launch background continue")
	}
}

func TestServicePlanModeCancelWithoutPlanModeDoesNotFailSession(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_planmode_cancel_missing")
	meta.Mode = session.ModeExec
	meta.CompletionPolicy = session.CompletionPolicyAutonomous
	meta.RootSessionID = meta.ID
	original := session.State{Status: session.StatusAwaitingInput, Phase: "idle", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := svc.store.Create(meta, original); err != nil {
		t.Fatalf("create session: %v", err)
	}
	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/sessions/"+meta.ID+"/planmode/cancel", map[string]any{}, http.StatusNotFound)
	if strings.TrimSpace(errResp.Error) == "" {
		t.Fatalf("expected missing plan mode error, got %#v", errResp)
	}
	state, err := svc.store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.Status != original.Status || state.Phase != original.Phase || state.LastError != "" {
		t.Fatalf("missing plan mode cancel should not claim or fail session, before=%#v after=%#v", original, state)
	}
	if svc.hasActiveHandle(meta.ID) {
		t.Fatalf("missing plan mode cancel should not launch background continue")
	}
}

func TestServicePlanModeCancelPrunesFailedStaleHandleBeforeRecovery(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "session_planmode_cancel_stale_failed_handle")
	meta.Mode = session.ModeExec
	meta.CompletionPolicy = session.CompletionPolicyAutonomous
	meta.RootSessionID = meta.ID
	if err := svc.store.Create(meta, session.State{Status: session.StatusFailed, Phase: "plan_input", LastError: "previous input event failed", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreatePlanMode(meta.ID, session.PlanModeDraft{Enabled: true, Objective: "Cancel stale failed plan input", Source: session.PlanModeSourceWeb}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	pendingRequest := session.PlanModeInputRequest{
		RequestID:  "pmq_stale_cancel",
		ToolCallID: "call_stale_cancel",
		Questions: []session.PlanModeInputQuestion{{
			ID:       "scope_choice",
			Header:   "Scope",
			Question: "Which scope?",
			Options: []session.PlanModeInputOption{
				{Label: "Narrow", Description: "Keep it focused."},
				{Label: "Broad", Description: "Include cleanup."},
			},
		}},
		Status:    "pending",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if _, err := svc.store.SetPlanModePendingRequest(meta.ID, pendingRequest, session.PlanModeSourceTool); err != nil {
		t.Fatalf("set pending request: %v", err)
	}
	staleRunner := agentruntime.NewRunner(cfg)
	if err := svc.addHandle(newLaunchHandle(meta.ID, staleRunner, func() {})); err != nil {
		t.Fatalf("add stale handle: %v", err)
	}
	ts := httptest.NewServer(svc)
	defer ts.Close()

	postJSON(t, ts.URL+"/api/sessions/"+meta.ID+"/planmode/cancel", map[string]any{}, http.StatusOK, nil)
	if svc.hasActiveHandle(meta.ID) {
		t.Fatal("expected failed stale handle to be pruned before recovered cancel")
	}
	cancelled, err := svc.store.LoadPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("load cancelled plan mode: %v", err)
	}
	if cancelled.Status != session.PlanModeStatusCancelled {
		t.Fatalf("expected cancelled plan mode, got %#v", cancelled)
	}
	state, err := svc.store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.Status != session.StatusAwaitingInput || state.Phase != "plan_cancelled" {
		t.Fatalf("expected recovered cancel to leave awaiting plan_cancelled, got %#v", state)
	}
	messages, err := svc.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	var foundCancelResult bool
	for _, msg := range messages {
		for _, result := range msg.ToolResults {
			if result.ToolCallID == pendingRequest.ToolCallID && result.Name == "request_user_input" && result.IsError {
				foundCancelResult = true
			}
		}
	}
	if !foundCancelResult {
		t.Fatalf("expected recovered cancellation tool result, got %#v", messages)
	}
}

func TestServicePlanModeReviseInputAndCancelControls(t *testing.T) {
	server := newSubmitPlanServer()
	defer server.Close()

	cfg := testConfig(t, server.URL)
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	ts := httptest.NewServer(svc)
	defer ts.Close()

	revisionMeta := testSessionMetadata(t, "session_planmode_revise")
	revisionMeta.Mode = session.ModeExec
	revisionMeta.CompletionPolicy = session.CompletionPolicyAutonomous
	revisionMeta.RootSessionID = revisionMeta.ID
	if err := svc.store.Create(revisionMeta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_approval", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create revision session: %v", err)
	}
	if _, err := svc.store.CreatePlanMode(revisionMeta.ID, session.PlanModeDraft{Enabled: true, Objective: "Revise plan", Source: session.PlanModeSourceWeb}); err != nil {
		t.Fatalf("create revision plan mode: %v", err)
	}
	if _, err := svc.store.SubmitPlanMode(revisionMeta.ID, session.PlanModeSubmitInput{
		Title:        "Plan",
		Summary:      "Needs revision.",
		PlanMarkdown: "# Plan\n\nNeeds revision.",
		Verification: []string{"go test ./internal/webconsole"},
		Source:       session.PlanModeSourceTool,
	}); err != nil {
		t.Fatalf("submit revision plan: %v", err)
	}
	var revisionLaunch LaunchResponse
	postJSON(t, ts.URL+"/api/sessions/"+revisionMeta.ID+"/planmode/revise", map[string]any{"message": "Use a narrower implementation."}, http.StatusAccepted, &revisionLaunch)
	waitFor(t, 4*time.Second, func() bool {
		planMode, err := svc.store.LoadPlanMode(revisionMeta.ID)
		return err == nil && planMode.Status == session.PlanModeStatusAwaitingApproval && planMode.PlanVersion >= 2
	}, func() string {
		planMode, err := svc.store.LoadPlanMode(revisionMeta.ID)
		if err != nil {
			return err.Error()
		}
		data, marshalErr := json.Marshal(planMode)
		if marshalErr != nil {
			return marshalErr.Error()
		}
		return string(data)
	})
	messages, err := svc.store.LoadMessages(revisionMeta.ID)
	if err != nil {
		t.Fatalf("load revision messages: %v", err)
	}
	var foundRevision bool
	for _, msg := range messages {
		if msg.Role == "user" && msg.Meta["source"] == "planmode_revision" && strings.Contains(msg.Text, "narrower") {
			foundRevision = true
		}
	}
	if !foundRevision {
		t.Fatalf("expected plan revision user message, got %#v", messages)
	}

	inputMeta := testSessionMetadata(t, "session_planmode_input")
	inputMeta.Mode = session.ModeExec
	inputMeta.CompletionPolicy = session.CompletionPolicyAutonomous
	inputMeta.RootSessionID = inputMeta.ID
	if err := svc.store.Create(inputMeta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_input", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create input session: %v", err)
	}
	if _, err := svc.store.CreatePlanMode(inputMeta.ID, session.PlanModeDraft{Enabled: true, Objective: "Answer plan input", Source: session.PlanModeSourceWeb}); err != nil {
		t.Fatalf("create input plan mode: %v", err)
	}
	inputRequest := session.PlanModeInputRequest{
		RequestID:  "pmq_input",
		ToolCallID: "call_input",
		Questions: []session.PlanModeInputQuestion{{
			ID:       "scope_choice",
			Header:   "Scope",
			Question: "Which scope?",
			Options: []session.PlanModeInputOption{
				{Label: "Narrow (Recommended)", Description: "Keep it focused."},
				{Label: "Broad", Description: "Include cleanup."},
			},
		}},
		Status:    "pending",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if _, err := svc.store.SetPlanModePendingRequest(inputMeta.ID, inputRequest, session.PlanModeSourceTool); err != nil {
		t.Fatalf("set input pending request: %v", err)
	}
	var inputLaunch LaunchResponse
	postJSON(t, ts.URL+"/api/sessions/"+inputMeta.ID+"/planmode/input", map[string]any{
		"request_id": inputRequest.RequestID,
		"answers": []map[string]any{{
			"question_id": "scope_choice",
			"label":       "Narrow (Recommended)",
			"value":       "Narrow (Recommended)",
		}},
	}, http.StatusAccepted, &inputLaunch)
	waitFor(t, 4*time.Second, func() bool {
		planMode, err := svc.store.LoadPlanMode(inputMeta.ID)
		return err == nil && planMode.Status == session.PlanModeStatusAwaitingApproval
	}, func() string {
		planMode, err := svc.store.LoadPlanMode(inputMeta.ID)
		if err != nil {
			return err.Error()
		}
		data, marshalErr := json.Marshal(planMode)
		if marshalErr != nil {
			return marshalErr.Error()
		}
		return string(data)
	})
	inputMessages, err := svc.store.LoadMessages(inputMeta.ID)
	if err != nil {
		t.Fatalf("load input messages: %v", err)
	}
	var foundInputResult bool
	for _, msg := range inputMessages {
		for _, result := range msg.ToolResults {
			if result.ToolCallID == inputRequest.ToolCallID && result.Name == "request_user_input" {
				foundInputResult = true
			}
		}
	}
	if !foundInputResult {
		t.Fatalf("expected recovered request_user_input tool result, got %#v", inputMessages)
	}

	cancelMeta := testSessionMetadata(t, "session_planmode_cancel")
	cancelMeta.Mode = session.ModeExec
	cancelMeta.CompletionPolicy = session.CompletionPolicyAutonomous
	cancelMeta.RootSessionID = cancelMeta.ID
	if err := svc.store.Create(cancelMeta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_input", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create cancel session: %v", err)
	}
	if _, err := svc.store.CreatePlanMode(cancelMeta.ID, session.PlanModeDraft{Enabled: true, Objective: "Cancel plan input", Source: session.PlanModeSourceWeb}); err != nil {
		t.Fatalf("create cancel plan mode: %v", err)
	}
	cancelRequest := inputRequest
	cancelRequest.RequestID = "pmq_cancel"
	cancelRequest.ToolCallID = "call_cancel"
	if _, err := svc.store.SetPlanModePendingRequest(cancelMeta.ID, cancelRequest, session.PlanModeSourceTool); err != nil {
		t.Fatalf("set cancel pending request: %v", err)
	}
	postJSON(t, ts.URL+"/api/sessions/"+cancelMeta.ID+"/planmode/cancel", map[string]any{}, http.StatusOK, nil)
	cancelled, err := svc.store.LoadPlanMode(cancelMeta.ID)
	if err != nil {
		t.Fatalf("load cancelled plan mode: %v", err)
	}
	if cancelled.Status != session.PlanModeStatusCancelled {
		t.Fatalf("expected cancelled plan mode, got %#v", cancelled)
	}
	cancelMessages, err := svc.store.LoadMessages(cancelMeta.ID)
	if err != nil {
		t.Fatalf("load cancel messages: %v", err)
	}
	var foundCancelResult bool
	for _, msg := range cancelMessages {
		for _, result := range msg.ToolResults {
			if result.ToolCallID == cancelRequest.ToolCallID && result.Name == "request_user_input" && result.IsError {
				foundCancelResult = true
			}
		}
	}
	if !foundCancelResult {
		t.Fatalf("expected cancellation tool result, got %#v", cancelMessages)
	}
}

func TestServicePlanModeContinueIsTrackedByLaunchWaitGroup(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	var releaseOnce sync.Once
	releaseProvider := func() {
		releaseOnce.Do(func() {
			close(release)
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedOnce.Do(func() {
			close(started)
		})
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_tracked_continue",
			"status":"completed",
			"output":[
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]},
				{"type":"function_call","call_id":"call_finish_tracked","name":"finish","arguments":"{\"message\":\"done\"}"}
			],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()

	cfg := testConfig(t, server.URL)
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	defer releaseProvider()
	ts := httptest.NewServer(svc)
	defer ts.Close()

	meta := testSessionMetadata(t, "session_planmode_tracked_continue")
	meta.Mode = session.ModeExec
	meta.CompletionPolicy = session.CompletionPolicyAutonomous
	meta.RootSessionID = meta.ID
	if err := svc.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_approval", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreatePlanMode(meta.ID, session.PlanModeDraft{Enabled: true, Objective: "Track continue", Source: session.PlanModeSourceWeb}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	if _, err := svc.store.SubmitPlanMode(meta.ID, session.PlanModeSubmitInput{
		Title:        "Tracked continue",
		Summary:      "Keep the launch wait group active.",
		PlanMarkdown: "# Tracked continue",
		Verification: []string{"go test ./internal/webconsole"},
		Source:       session.PlanModeSourceTool,
	}); err != nil {
		t.Fatalf("submit plan mode: %v", err)
	}

	var launch LaunchResponse
	postJSON(t, ts.URL+"/api/sessions/"+meta.ID+"/planmode/approve", map[string]any{}, http.StatusAccepted, &launch)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for blocked provider request")
	}

	waitDone := make(chan struct{})
	go func() {
		svc.launchWG.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
		t.Fatal("launch wait group returned while Plan Mode continue was still blocked in the provider request")
	case <-time.After(150 * time.Millisecond):
	}

	releaseProvider()
	select {
	case <-waitDone:
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for tracked Plan Mode continue to finish")
	}
}

func TestServiceStartSessionReturnsSessionID(t *testing.T) {
	server := newFinishServer()
	defer server.Close()

	cfg := testConfig(t, server.URL)
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var result LaunchResponse
	postJSON(t, ts.URL+"/api/sessions/start", map[string]any{
		"prompt": "Write a short completion summary and call finish.",
		"mode":   "exec",
	}, http.StatusAccepted, &result)

	if result.SessionID == "" {
		t.Fatal("expected session id")
	}
	waitFor(t, 4*time.Second, func() bool {
		state, err := svc.store.LoadState(result.SessionID)
		return err == nil && state.Status == session.StatusCompleted
	}, func() string {
		state, err := svc.store.LoadState(result.SessionID)
		if err != nil {
			return err.Error()
		}
		data, marshalErr := json.Marshal(state)
		if marshalErr != nil {
			return marshalErr.Error()
		}
		return string(data)
	})
	messages, err := svc.store.LoadMessages(result.SessionID)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	meta, err := svc.store.LoadMetadata(result.SessionID)
	if err != nil {
		t.Fatalf("load metadata: %v", err)
	}
	if meta.AgentRole != "" {
		t.Fatalf("expected default web start to leave agent role empty, got %#v", meta.AgentRole)
	}
	if len(messages) == 0 || messages[0].Role != "user" {
		t.Fatalf("expected user message to be persisted, got %#v", messages)
	}
}

func TestWaitForSessionIDAllowsSlowSessionCreation(t *testing.T) {
	sub := make(chan events.Event, 1)
	outcomeCh := make(chan launchOutcome, 1)

	go func() {
		time.Sleep(25 * time.Millisecond)
		sub <- events.New("session_slow_start", "session.created", "prepare", nil)
	}()

	sessionID, early, err := waitForSessionIDWithTimeout(sub, outcomeCh, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("expected delayed session creation to be accepted, got %v", err)
	}
	if sessionID != "session_slow_start" {
		t.Fatalf("expected delayed session id, got %q", sessionID)
	}
	if early != nil {
		t.Fatalf("expected no early launch outcome, got %#v", early)
	}
}

func TestServiceMissionRolePlanAppliesExactRoleProviderOverrides(t *testing.T) {
	cfg := testConfig(t, "")
	cfg.Providers["planner-profile"] = cfg.Providers["openai"]
	planner := cfg.Providers["planner-profile"]
	planner.Model = "planner-profile-model"
	cfg.Providers["planner-profile"] = planner
	cfg.RoleProviders.Planner = config.RoleProviderOverride{
		Provider: "planner-profile",
	}
	cfg.RoleProviders.Generator = config.RoleProviderOverride{
		Model: "generator-role-model",
	}
	cfg.RoleProviders.Evaluator = config.RoleProviderOverride{
		Provider: "planner-profile",
		Model:    "evaluator-role-model",
	}
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "session_role_plan_exact",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		RootSessionID:    "session_role_plan_exact",
	}
	if err := svc.store.Create(meta, testSessionState(session.StatusRunning)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeMission,
		Objective: "Ship role-aware mission plan",
		Source:    session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}

	ts := httptest.NewServer(svc)
	defer ts.Close()

	patchBody := bytes.NewBufferString(`{
		"role_plan": [
			{"name":"Plan","role":"planner","scope":"split milestones"},
			{"name":"Build","role":"generator","scope":"implement feature"},
			{"name":"Review","role":"evaluator","scope":"validate result"},
			{"name":"Legacy","role":"worker","scope":"should not be matched"}
		],
		"plan_status": "draft"
	}`)
	req, err := http.NewRequest(http.MethodPatch, ts.URL+"/api/sessions/"+meta.ID+"/mission/plan", patchBody)
	if err != nil {
		t.Fatalf("new patch request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(webMutationHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("patch mission plan: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("unexpected patch status: %d body=%s", resp.StatusCode, string(body))
	}
	var goal session.SessionGoal
	if err := json.NewDecoder(resp.Body).Decode(&goal); err != nil {
		t.Fatalf("decode goal: %v", err)
	}
	if goal.Mission == nil || len(goal.Mission.RolePlan) != 4 {
		t.Fatalf("expected four role plan entries, got %#v", goal.Mission)
	}
	roles := goal.Mission.RolePlan
	if roles[0].Role != "planner" || roles[0].Provider != "planner-profile" || roles[0].Model != "planner-profile-model" {
		t.Fatalf("expected planner provider profile defaults, got %#v", roles[0])
	}
	if roles[1].Role != "generator" || roles[1].Provider != "openai" || roles[1].Model != "generator-role-model" {
		t.Fatalf("expected generator role model with default provider, got %#v", roles[1])
	}
	if roles[2].Role != "evaluator" || roles[2].Provider != "planner-profile" || roles[2].Model != "evaluator-role-model" {
		t.Fatalf("expected evaluator role override, got %#v", roles[2])
	}
	if roles[3].Role != "worker" || roles[3].Provider != "" || roles[3].Model != "" {
		t.Fatalf("expected unsupported role to remain unmatched, got %#v", roles[3])
	}
}

func TestServiceMissionRolePlanReportsCorruptSessionMetadata(t *testing.T) {
	cfg := testConfig(t, "")
	cfg.Providers["fallback"] = cfg.Providers["openai"]
	fallback := cfg.Providers["fallback"]
	fallback.Model = "fallback-model"
	cfg.Providers["fallback"] = fallback
	cfg.DefaultProvider = "fallback"
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	meta := testSessionMetadata(t, "session_role_plan_corrupt_meta")
	meta.Provider = "openai"
	meta.Model = "gpt-5.4"
	if err := svc.store.Create(meta, testSessionState(session.StatusRunning)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeMission,
		Objective: "Reject role defaults without session metadata",
		Source:    session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	sessionPath := filepath.Join(svc.store.SessionDir(meta.ID), "session.json")
	if err := os.WriteFile(sessionPath, []byte(`{"id":`), 0o600); err != nil {
		t.Fatalf("write corrupt session metadata: %v", err)
	}

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/sessions/"+meta.ID+"/mission/plan", map[string]any{
		"role_plan": []map[string]any{
			{"name": "Plan", "role": "planner", "scope": "split milestones"},
		},
	}, http.StatusInternalServerError)
	if !strings.Contains(errResp.Error, "session.json") {
		t.Fatalf("expected session.json error, got %#v", errResp)
	}
	goal, loadErr := svc.store.LoadGoal(meta.ID)
	if loadErr != nil {
		t.Fatalf("load goal: %v", loadErr)
	}
	if goal.Mission != nil && len(goal.Mission.RolePlan) != 0 {
		t.Fatalf("failed role plan patch should not mutate goal, got %#v", goal.Mission.RolePlan)
	}
}

func TestServiceStartSessionWithGoalPersistsGoal(t *testing.T) {
	server := newGoalCompleteServer()
	defer server.Close()

	cfg := testConfig(t, server.URL)
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var result LaunchResponse
	postJSON(t, ts.URL+"/api/sessions/start", map[string]any{
		"prompt": "Implement the goal-aware path.",
		"mode":   "exec",
		"goal": map[string]any{
			"enabled": true,
		},
	}, http.StatusAccepted, &result)

	goal, err := svc.store.LoadGoal(result.SessionID)
	if err != nil {
		t.Fatalf("load goal: %v", err)
	}
	if goal.Mode != session.GoalModeGoal || goal.Objective != "Implement the goal-aware path." {
		t.Fatalf("expected prompt-derived simple goal, got %#v", goal)
	}
	if goal.Mission != nil || goal.TokenBudget != nil || goal.TimeBudgetSeconds != nil || len(goal.SuccessCriteria) != 0 || len(goal.ValidationPlan) != 0 {
		t.Fatalf("web start should not require user-authored mission/budget fields, got %#v", goal)
	}
	detail, err := svc.sessionDetail(result.SessionID, 20)
	if err != nil {
		t.Fatalf("session detail: %v", err)
	}
	if detail.Goal == nil || detail.Goal.Objective != "Implement the goal-aware path." {
		t.Fatalf("expected detail goal, got %#v", detail.Goal)
	}
	waitFor(t, 4*time.Second, func() bool {
		state, err := svc.store.LoadState(result.SessionID)
		return err == nil && state.Status == session.StatusCompleted
	}, func() string {
		state, err := svc.store.LoadState(result.SessionID)
		if err != nil {
			return err.Error()
		}
		data, marshalErr := json.Marshal(state)
		if marshalErr != nil {
			return marshalErr.Error()
		}
		return string(data)
	})
}

func TestServiceStartSessionRejectsUnsupportedAgentRole(t *testing.T) {
	server := newFinishServer()
	defer server.Close()

	cfg := testConfig(t, server.URL)
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	postJSON(t, ts.URL+"/api/sessions/start", map[string]any{
		"prompt":     "Write a short completion summary and call finish.",
		"mode":       "exec",
		"agent_role": "assistant",
	}, http.StatusBadRequest, nil)
}

func TestServiceStartSessionPersistsAgentIdentity(t *testing.T) {
	server := newFinishServer()
	defer server.Close()

	cfg := testConfig(t, server.URL)
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var result LaunchResponse
	postJSON(t, ts.URL+"/api/sessions/start", map[string]any{
		"prompt":     "Write a short completion summary and call finish.",
		"mode":       "exec",
		"agent_name": "web-reviewer",
		"agent_role": "evaluator",
	}, http.StatusAccepted, &result)

	if result.SessionID == "" {
		t.Fatal("expected session id")
	}
	waitFor(t, 4*time.Second, func() bool {
		state, err := svc.store.LoadState(result.SessionID)
		return err == nil && state.Status == session.StatusCompleted
	}, func() string {
		state, err := svc.store.LoadState(result.SessionID)
		if err != nil {
			return err.Error()
		}
		data, marshalErr := json.Marshal(state)
		if marshalErr != nil {
			return marshalErr.Error()
		}
		return string(data)
	})

	meta, err := svc.store.LoadMetadata(result.SessionID)
	if err != nil {
		t.Fatalf("load metadata: %v", err)
	}
	if meta.AgentName != "web-reviewer" {
		t.Fatalf("expected agent name to persist, got %#v", meta.AgentName)
	}
	if meta.AgentRole != "evaluator" {
		t.Fatalf("expected agent role to persist, got %#v", meta.AgentRole)
	}
}

func TestServiceCloseCancelsPendingStartBeforeSessionID(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	cancelled := make(chan struct{})
	pendingID, err := svc.registerPendingStart(func() {
		close(cancelled)
	})
	if err != nil {
		t.Fatalf("register pending start: %v", err)
	}
	if pendingID == 0 {
		t.Fatal("expected non-zero pending start id")
	}

	closeDone := make(chan struct{})
	go func() {
		svc.Close()
		close(closeDone)
	}()

	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return while start was pending before session id observation")
	}

	select {
	case <-cancelled:
	default:
		t.Fatal("expected pending start cancel function to run during Close")
	}

	if _, err := svc.registerPendingStart(func() {}); err == nil {
		t.Fatal("expected pending start registration to fail after Close")
	}
}

func TestServiceRejectsDuplicateHandleAndPreservesOwner(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	meta := testSessionMetadata(t, "session_duplicate_handle")
	if err := svc.store.Create(meta, testSessionState(session.StatusRunning)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	var firstCancelled bool
	first := &launchHandle{
		sessionID:      meta.ID,
		cancel:         func() { firstCancelled = true },
		startedAt:      "2026-05-08T00:00:00Z",
		processStartID: "first-process",
		pid:            111,
	}
	if err := svc.addHandle(first); err != nil {
		t.Fatalf("add first handle: %v", err)
	}
	second := &launchHandle{
		sessionID:      meta.ID,
		cancel:         func() {},
		startedAt:      "2026-05-08T00:00:01Z",
		processStartID: "second-process",
		pid:            222,
	}
	if err := svc.addHandle(second); !errors.Is(err, errSessionAlreadyActive) {
		t.Fatalf("expected duplicate handle rejection, got %v", err)
	}

	svc.finishHandle(second, launchOutcome{err: errors.New("duplicate continue rejected")})
	current, ok := svc.handleForSession(meta.ID)
	if !ok || current != first {
		t.Fatalf("expected original handle to remain active, got handle=%#v ok=%v", current, ok)
	}

	svc.finishHandle(first, launchOutcome{})
	if !firstCancelled {
		t.Fatal("expected original handle cancel to run on finish")
	}
	if _, ok := svc.handleForSession(meta.ID); ok {
		t.Fatal("expected original handle release to remove active handle")
	}
}

func TestServiceAddHandleRequiresAcquiredEvent(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	meta := testSessionMetadata(t, "session_add_handle_requires_event")
	if err := svc.store.Create(meta, testSessionState(session.StatusRunning)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	blockWebEventsPath(t, svc.store, meta.ID)

	err = svc.addHandle(&launchHandle{
		sessionID:      meta.ID,
		cancel:         func() {},
		startedAt:      "2026-05-08T00:00:00Z",
		processStartID: "blocked-acquire-process",
		pid:            111,
	})
	if err == nil || !strings.Contains(err.Error(), "webconsole.handle.acquired") || !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected acquired event append error, got %v", err)
	}
	if _, ok := svc.handleForSession(meta.ID); ok {
		t.Fatal("expected failed acquired event to leave no active handle")
	}
}

func TestServicePromotePendingStartRequiresAcquiredEvent(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	meta := testSessionMetadata(t, "session_promote_handle_requires_event")
	if err := svc.store.Create(meta, testSessionState(session.StatusRunning)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	pendingID, err := svc.registerPendingStart(func() {})
	if err != nil {
		t.Fatalf("register pending start: %v", err)
	}
	blockWebEventsPath(t, svc.store, meta.ID)

	err = svc.promotePendingStart(pendingID, &launchHandle{
		sessionID:      meta.ID,
		cancel:         func() {},
		startedAt:      "2026-05-08T00:00:00Z",
		processStartID: "blocked-promote-process",
		pid:            222,
	})
	if err == nil || !strings.Contains(err.Error(), "webconsole.handle.acquired") || !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected acquired event append error, got %v", err)
	}
	if _, ok := svc.handleForSession(meta.ID); ok {
		t.Fatal("expected failed acquired event to leave no active handle")
	}
	svc.mu.RLock()
	_, pending := svc.pendingStarts[pendingID]
	svc.mu.RUnlock()
	if pending {
		t.Fatal("expected failed promotion to consume pending start")
	}
}

func TestServicePruneInactiveHandlesRecordsReleaseEvent(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	meta := testSessionMetadata(t, "session_prune_release_event")
	if err := svc.store.Create(meta, testSessionState(session.StatusRunning)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := svc.addHandle(&launchHandle{
		sessionID:      meta.ID,
		cancel:         func() {},
		startedAt:      "2026-05-08T00:00:00Z",
		processStartID: "prune-release-process",
		pid:            5150,
	}); err != nil {
		t.Fatalf("add handle: %v", err)
	}
	if err := svc.store.SaveState(meta.ID, testSessionState(session.StatusCompleted)); err != nil {
		t.Fatalf("save completed state: %v", err)
	}

	if svc.hasAnyActiveHandle() {
		t.Fatal("expected completed handle to be pruned")
	}
	eventsList, err := svc.store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	owner := latestActiveOwnerFromEvents(eventsList)
	if owner.EventType != "webconsole.handle.released" || owner.ProcessStartID != "prune-release-process" || owner.ReleasedAt == "" {
		t.Fatalf("expected prune to record release owner clue, got %#v", owner)
	}
}

func TestContinueRESTCarriesRuntimeFields(t *testing.T) {
	captured := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		select {
		case captured <- body:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_1",
			"status":"completed",
			"output":[
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"continued"}]},
				{"type":"function_call","call_id":"call_finish_1","name":"finish","arguments":"{\"message\":\"continued\"}"}
			],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()

	cfg := testConfig(t, server.URL)
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	workdir := t.TempDir()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "session_continue_rest",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		RequestedWorkdir: workdir,
		Mode:             session.ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyAutonomous,
	}
	state := session.State{
		Status:    session.StatusAwaitingInput,
		Phase:     "awaiting_input",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := svc.store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}

	postJSON(t, ts.URL+"/api/sessions/"+meta.ID+"/continue", map[string]any{
		"message":  "continue with the requested runtime fields",
		"provider": "openai",
		"model":    "gpt-5.5",
		"system":   "Prefer concise answers for this resumed turn.",
	}, http.StatusAccepted, nil)

	waitFor(t, 4*time.Second, func() bool {
		state, err := svc.store.LoadState(meta.ID)
		return err == nil && state.Status == session.StatusCompleted
	}, func() string {
		state, err := svc.store.LoadState(meta.ID)
		if err != nil {
			return err.Error()
		}
		data, marshalErr := json.Marshal(state)
		if marshalErr != nil {
			return marshalErr.Error()
		}
		return string(data)
	})

	updatedMeta, err := svc.store.LoadMetadata(meta.ID)
	if err != nil {
		t.Fatalf("load metadata: %v", err)
	}
	if updatedMeta.Provider != "openai" || updatedMeta.Model != "gpt-5.5" {
		t.Fatalf("expected REST continue to persist provider/model override, got provider=%q model=%q", updatedMeta.Provider, updatedMeta.Model)
	}

	var body map[string]any
	select {
	case body = <-captured:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for provider request")
	}
	if body["model"] != "gpt-5.5" {
		t.Fatalf("expected provider request model override, got %#v", body)
	}
	instructions, _ := body["instructions"].(string)
	if !strings.Contains(instructions, "Prefer concise answers for this resumed turn.") {
		t.Fatalf("expected system override in provider instructions, got %q", instructions)
	}
	input, ok := body["input"].([]any)
	if !ok || len(input) == 0 {
		t.Fatalf("expected continued user input in provider request, got %#v", body)
	}
}

func TestContinueRESTModelDefaultUsesProviderDefault(t *testing.T) {
	captured := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		select {
		case captured <- body:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_default_model",
			"status":"completed",
			"output":[{"type":"function_call","call_id":"call_finish","name":"finish","arguments":"{\"message\":\"continued\"}"}],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()

	cfg := testConfig(t, server.URL)
	providerCfg := cfg.Providers["openai"]
	providerCfg.Model = "gpt-provider-default"
	cfg.Providers["openai"] = providerCfg
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	workdir := t.TempDir()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "session_continue_default_model",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		RequestedWorkdir: workdir,
		Mode:             session.ModeExec,
		Provider:         "openai",
		Model:            "gpt-old",
		CompletionPolicy: session.CompletionPolicyAutonomous,
	}
	state := session.State{
		Status:    session.StatusAwaitingInput,
		Phase:     "awaiting_input",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := svc.store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}

	postJSON(t, ts.URL+"/api/sessions/"+meta.ID+"/continue", map[string]any{
		"message":  "continue with provider default model",
		"provider": "openai",
		"model":    "default",
	}, http.StatusAccepted, nil)

	waitFor(t, 4*time.Second, func() bool {
		state, err := svc.store.LoadState(meta.ID)
		return err == nil && state.Status == session.StatusCompleted
	}, func() string {
		state, err := svc.store.LoadState(meta.ID)
		if err != nil {
			return err.Error()
		}
		data, marshalErr := json.Marshal(state)
		if marshalErr != nil {
			return marshalErr.Error()
		}
		return string(data)
	})

	updatedMeta, err := svc.store.LoadMetadata(meta.ID)
	if err != nil {
		t.Fatalf("load metadata: %v", err)
	}
	if updatedMeta.Model != "gpt-provider-default" {
		t.Fatalf("expected REST continue to persist provider default model, got %#v", updatedMeta)
	}

	var body map[string]any
	select {
	case body = <-captured:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for provider request")
	}
	if body["model"] != "gpt-provider-default" {
		t.Fatalf("expected provider request model default to resolve to provider default, got %#v", body)
	}
}

func TestContinueRESTBackfillsMissingProviderOptions(t *testing.T) {
	captured := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		select {
		case captured <- body:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_legacy_continue",
			"status":"completed",
			"output":[{"type":"function_call","call_id":"call_finish","name":"finish","arguments":"{\"message\":\"continued\"}"}],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()

	cfg := testConfig(t, server.URL)
	providerCfg := cfg.Providers["openai"]
	providerCfg.APIProvider = "openai-compatible"
	providerCfg.WireAPI = "responses"
	cfg.Providers["openai"] = providerCfg
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	workdir := t.TempDir()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "session_continue_legacy_options",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		RequestedWorkdir: workdir,
		Mode:             session.ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyAutonomous,
	}
	state := session.State{
		Status:    session.StatusAwaitingInput,
		Phase:     "awaiting_input",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := svc.store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}

	postJSON(t, ts.URL+"/api/sessions/"+meta.ID+"/continue", map[string]any{
		"message": "continue legacy session",
	}, http.StatusAccepted, nil)

	waitFor(t, 4*time.Second, func() bool {
		state, err := svc.store.LoadState(meta.ID)
		return err == nil && state.Status == session.StatusCompleted
	}, func() string {
		state, err := svc.store.LoadState(meta.ID)
		if err != nil {
			return err.Error()
		}
		data, marshalErr := json.Marshal(state)
		if marshalErr != nil {
			return marshalErr.Error()
		}
		return string(data)
	})

	updatedMeta, err := svc.store.LoadMetadata(meta.ID)
	if err != nil {
		t.Fatalf("load metadata: %v", err)
	}
	if updatedMeta.ProviderOptions.Store == nil || *updatedMeta.ProviderOptions.Store {
		t.Fatalf("expected REST continue to persist backfilled store=false, got %#v", updatedMeta.ProviderOptions)
	}

	var body map[string]any
	select {
	case body = <-captured:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for provider request")
	}
	if got := body["store"]; got != false {
		t.Fatalf("expected provider request to include backfilled store=false, got %#v", body)
	}
}

func TestStartSessionRejectsUnknownField(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/sessions/start", map[string]any{
		"prompt":       "hello",
		"unknown_flag": true,
	}, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "unknown field") {
		t.Fatalf("expected unknown field error, got %#v", errResp)
	}
}

func TestStartSessionRejectsUnsupportedModeAsBadRequest(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/sessions/start", map[string]any{
		"prompt": "hello",
		"mode":   "sideways",
	}, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "unsupported run mode") {
		t.Fatalf("expected unsupported mode error, got %#v", errResp)
	}
	sessions, listErr := svc.store.List(10)
	if listErr != nil {
		t.Fatalf("list sessions: %v", listErr)
	}
	if len(sessions) != 0 {
		t.Fatalf("unsupported mode should not create a session, got %#v", sessions)
	}
}

func TestStartSessionRejectsInvalidGoalDraftAsBadRequest(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/sessions/start", map[string]any{
		"prompt": "start with invalid goal",
		"goal": map[string]any{
			"enabled":   true,
			"objective": strings.Repeat("x", session.MaxGoalObjectiveChars+1),
		},
	}, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "goal objective exceeds") {
		t.Fatalf("expected goal objective validation error, got %#v", errResp)
	}
	sessions, listErr := svc.store.List(10)
	if listErr != nil {
		t.Fatalf("list sessions: %v", listErr)
	}
	if len(sessions) != 0 {
		t.Fatalf("invalid goal draft should not create a session, got %#v", sessions)
	}
}

func TestStartSessionRejectsInvalidPlanModeDraftAsBadRequest(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/sessions/start", map[string]any{
		"prompt": "start with invalid Plan Mode",
		"plan_mode": map[string]any{
			"enabled":   true,
			"objective": strings.Repeat("x", session.MaxPlanModeObjectiveChars+1),
		},
	}, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "plan mode objective exceeds") {
		t.Fatalf("expected Plan Mode objective validation error, got %#v", errResp)
	}
	sessions, listErr := svc.store.List(10)
	if listErr != nil {
		t.Fatalf("list sessions: %v", listErr)
	}
	if len(sessions) != 0 {
		t.Fatalf("invalid Plan Mode draft should not create a session, got %#v", sessions)
	}
}

func TestStartSessionRejectsGitIsolationOutsideRepoAsBadRequest(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	workdir := t.TempDir()
	errResp := postJSONError(t, ts.URL+"/api/sessions/start", map[string]any{
		"prompt":         "hello",
		"workdir":        workdir,
		"mode":           "exec",
		"isolation_mode": "git",
		"isolation_root": filepath.Join(t.TempDir(), "isolated"),
	}, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "git isolation requested") {
		t.Fatalf("expected git isolation error, got %#v", errResp)
	}
	sessions, listErr := svc.store.List(10)
	if listErr != nil {
		t.Fatalf("list sessions: %v", listErr)
	}
	if len(sessions) != 0 {
		t.Fatalf("invalid git isolation should not create a session, got %#v", sessions)
	}
}

func TestStartSessionRejectsTrailingJSONValue(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postRawJSONError(t, ts.URL+"/api/sessions/start", `{"prompt":"hello"} {"prompt":"ignored"}`, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "single JSON value") {
		t.Fatalf("expected trailing JSON error, got %#v", errResp)
	}
}

func TestContinueSessionRejectsUnknownField(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	meta := testSessionMetadata(t, "session_continue_unknown_field")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}

	errResp := postJSONError(t, ts.URL+"/api/sessions/"+meta.ID+"/continue", map[string]any{
		"message":       "continue",
		"unknown_field": "bad",
	}, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "unknown field") {
		t.Fatalf("expected unknown field error, got %#v", errResp)
	}
}

func TestContinueSessionRejectsInvalidPlanModeDraftBeforeClaim(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	meta := testSessionMetadata(t, "session_continue_invalid_planmode_draft")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	tooLongObjective := strings.Repeat("x", session.MaxPlanModeObjectiveChars+1)
	errResp := postJSONError(t, ts.URL+"/api/sessions/"+meta.ID+"/continue", map[string]any{
		"plan_mode": map[string]any{
			"enabled":   true,
			"objective": tooLongObjective,
		},
	}, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "plan mode objective exceeds") {
		t.Fatalf("expected plan mode objective validation error, got %#v", errResp)
	}
	state, err := svc.store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.Status != session.StatusAwaitingInput {
		t.Fatalf("invalid Plan Mode draft should not claim the session, got state %#v", state)
	}
	if _, err := svc.store.LoadPlanMode(meta.ID); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("invalid Plan Mode draft should not create planmode.json, err=%v", err)
	}
	if svc.hasActiveHandle(meta.ID) {
		t.Fatal("invalid Plan Mode draft should not create an active web handle")
	}
}

func TestContinueNonResumableSessionReturnsStructuredError(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	meta := testSessionMetadata(t, "session_continue_completed")
	if err := svc.store.Create(meta, testSessionState(session.StatusCompleted)); err != nil {
		t.Fatalf("create session: %v", err)
	}

	errResp := postJSONError(t, ts.URL+"/api/sessions/"+meta.ID+"/continue", map[string]any{
		"message": "continue",
	}, http.StatusConflict)
	if errResp.Code != errorCodeSessionNotResumable || errResp.Action == "" {
		t.Fatalf("expected structured non-resumable error, got %#v", errResp)
	}
}

func TestInterruptNonOwnedSessionReturnsStructuredError(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	meta := testSessionMetadata(t, "session_interrupt_not_owned")
	if err := svc.store.Create(meta, testSessionState(session.StatusRunning)); err != nil {
		t.Fatalf("create session: %v", err)
	}

	errResp := postJSONError(t, ts.URL+"/api/sessions/"+meta.ID+"/interrupt", map[string]any{}, http.StatusConflict)
	if errResp.Code != errorCodeActiveHandleNotOwned || errResp.Action == "" {
		t.Fatalf("expected structured active-handle error, got %#v", errResp)
	}
}

func TestInterruptPrunesTerminalStaleHandleBeforeOwnershipCheck(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	meta := testSessionMetadata(t, "session_interrupt_stale_terminal_handle")
	if err := svc.store.Create(meta, testSessionState(session.StatusFailed)); err != nil {
		t.Fatalf("create failed session: %v", err)
	}
	if err := svc.addHandle(newLaunchHandle(meta.ID, agentruntime.NewRunner(cfg), func() {})); err != nil {
		t.Fatalf("add stale handle: %v", err)
	}

	errResp := postJSONError(t, ts.URL+"/api/sessions/"+meta.ID+"/interrupt", map[string]any{}, http.StatusConflict)
	if errResp.Code != errorCodeActiveHandleNotOwned || errResp.Action == "" {
		t.Fatalf("expected stale terminal handle to be pruned into structured active-handle error, got %#v", errResp)
	}
	if svc.hasActiveHandle(meta.ID) {
		t.Fatal("expected terminal stale handle to be pruned")
	}
}

func TestStopNonOwnedSessionReturnsStructuredError(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	meta := testSessionMetadata(t, "session_stop_not_owned")
	if err := svc.store.Create(meta, testSessionState(session.StatusRunning)); err != nil {
		t.Fatalf("create session: %v", err)
	}

	errResp := postJSONError(t, ts.URL+"/api/sessions/"+meta.ID+"/stop", map[string]any{}, http.StatusConflict)
	if errResp.Code != errorCodeActiveHandleNotOwned || errResp.Action == "" {
		t.Fatalf("expected structured active-handle error, got %#v", errResp)
	}
}

func TestStopPrunesTerminalStaleHandleBeforeOwnershipCheck(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	meta := testSessionMetadata(t, "session_stop_stale_terminal_handle")
	if err := svc.store.Create(meta, testSessionState(session.StatusCompleted)); err != nil {
		t.Fatalf("create completed session: %v", err)
	}
	if err := svc.addHandle(newLaunchHandle(meta.ID, agentruntime.NewRunner(cfg), func() {})); err != nil {
		t.Fatalf("add stale handle: %v", err)
	}

	errResp := postJSONError(t, ts.URL+"/api/sessions/"+meta.ID+"/stop", map[string]any{}, http.StatusConflict)
	if errResp.Code != errorCodeActiveHandleNotOwned || errResp.Action == "" {
		t.Fatalf("expected stale terminal handle to be pruned into structured active-handle error, got %#v", errResp)
	}
	if svc.hasActiveHandle(meta.ID) {
		t.Fatal("expected terminal stale handle to be pruned")
	}
}

func TestSessionDetailReportsActiveHandleOwner(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	current := testSessionMetadata(t, "session_current_owner")
	if err := svc.store.Create(current, testSessionState(session.StatusRunning)); err != nil {
		t.Fatalf("create current session: %v", err)
	}
	if err := svc.addHandle(&launchHandle{
		sessionID:      current.ID,
		cancel:         func() {},
		startedAt:      "2026-05-08T00:00:00Z",
		processStartID: "current-process",
		pid:            4242,
	}); err != nil {
		t.Fatalf("expected active handle to be accepted: %v", err)
	}
	var currentDetail SessionDetailResponse
	postGetJSON(t, ts.URL+"/api/sessions/"+current.ID, &currentDetail)
	if !currentDetail.ActiveHandle || currentDetail.ActiveHandleOwner.State != "current_process" || currentDetail.ActiveHandleOwner.ProcessStartID != "current-process" || currentDetail.ActiveHandleOwner.PID != 4242 {
		t.Fatalf("expected current-process owner detail, got %#v", currentDetail.ActiveHandleOwner)
	}

	external := testSessionMetadata(t, "session_external_owner")
	if err := svc.store.Create(external, testSessionState(session.StatusRunning)); err != nil {
		t.Fatalf("create external session: %v", err)
	}
	if err := svc.store.AppendEvent(external.ID, events.New(external.ID, "webconsole.handle.acquired", "webconsole", map[string]any{
		"source":           "webconsole",
		"process_start_id": "external-process",
		"pid":              31337,
		"started_at":       "2026-05-08T00:01:00Z",
	})); err != nil {
		t.Fatalf("append external owner event: %v", err)
	}
	var externalDetail SessionDetailResponse
	postGetJSON(t, ts.URL+"/api/sessions/"+external.ID, &externalDetail)
	if externalDetail.ActiveHandle || externalDetail.ActiveHandleOwner.State != "running_not_owned" || externalDetail.ActiveHandleOwner.ProcessStartID != "external-process" || externalDetail.ActiveHandleOwner.Action == "" {
		t.Fatalf("expected running-not-owned owner detail, got %#v", externalDetail.ActiveHandleOwner)
	}
	if err := svc.store.AppendEvent(external.ID, events.New(external.ID, "webconsole.handle.released", "webconsole", map[string]any{
		"source":           "webconsole",
		"process_start_id": "external-process",
		"pid":              31337,
		"started_at":       "2026-05-08T00:01:00Z",
		"released_at":      "2026-05-08T00:02:00Z",
	})); err != nil {
		t.Fatalf("append external owner release event: %v", err)
	}
	var releasedDetail SessionDetailResponse
	postGetJSON(t, ts.URL+"/api/sessions/"+external.ID, &releasedDetail)
	if releasedDetail.ActiveHandle || releasedDetail.ActiveHandleOwner.State != "settled" || releasedDetail.ActiveHandleOwner.ProcessStartID != "external-process" || releasedDetail.ActiveHandleOwner.ReleasedAt == "" {
		t.Fatalf("expected released owner detail to be settled, got %#v", releasedDetail.ActiveHandleOwner)
	}

	settled := testSessionMetadata(t, "session_settled_owner")
	if err := svc.store.Create(settled, testSessionState(session.StatusCompleted)); err != nil {
		t.Fatalf("create settled session: %v", err)
	}
	var settledDetail SessionDetailResponse
	postGetJSON(t, ts.URL+"/api/sessions/"+settled.ID, &settledDetail)
	if settledDetail.ActiveHandle || settledDetail.ActiveHandleOwner.State != "settled" {
		t.Fatalf("expected settled owner detail, got %#v", settledDetail.ActiveHandleOwner)
	}
}

func TestUpdateConfigRejectsUnknownProvider(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/config", map[string]any{
		"provider": "missing-provider",
	}, http.StatusBadRequest)
	if errResp.Code != errorCodeUnknownProvider || errResp.Action == "" {
		t.Fatalf("expected unknown provider error, got %#v", errResp)
	}
	if cfg.DefaultProvider != "openai" {
		t.Fatalf("unknown provider should not mutate default provider, got %q", cfg.DefaultProvider)
	}
}

func TestUpdateConfigRejectsNullBodyBeforeConfigWrite(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)

	cfg := testConfig(t, "")
	configPath := filepath.Join(cwd, "config.yaml")
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postRawJSONError(t, ts.URL+"/api/config", `null`, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "JSON object") {
		t.Fatalf("expected JSON object error, got %#v", errResp)
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("null config body should not persist config; stat err=%v", err)
	}
	auditPath := webAuditLogPath(cfg.Session.Dir)
	if data, err := os.ReadFile(auditPath); err == nil && strings.Contains(string(data), "web.config.write") {
		t.Fatalf("null config body should not append config audit event, got %s", string(data))
	}
}

func TestUpdateConfigRejectsEmptyRequestBeforeConfigWrite(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)

	cfg := testConfig(t, "")
	configPath := filepath.Join(cwd, "config.yaml")
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/config", map[string]any{}, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "settings update must include at least one field") {
		t.Fatalf("expected empty settings update error, got %#v", errResp)
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty config update should not persist config; stat err=%v", err)
	}
	auditPath := webAuditLogPath(cfg.Session.Dir)
	if data, err := os.ReadFile(auditPath); err == nil && strings.Contains(string(data), "web.config.write") {
		t.Fatalf("empty config update should not append config audit event, got %s", string(data))
	}
}

func TestUpdateConfigRejectsUnsupportedGuardrailsModeBeforeConfigWrite(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)

	cfg := testConfig(t, "")
	cfg.Runtime.GuardrailsMode = "yolo"
	configPath := filepath.Join(cwd, "config.yaml")
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/config", map[string]any{
		"guardrails_mode": "standrad",
	}, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "unsupported guardrails_mode") {
		t.Fatalf("expected guardrails mode error, got %#v", errResp)
	}
	snapshot, err := svc.configSnapshot()
	if err != nil {
		t.Fatalf("config snapshot: %v", err)
	}
	if snapshot.Runtime.GuardrailsMode != "yolo" {
		t.Fatalf("invalid guardrails mode should not mutate active config, got %q", snapshot.Runtime.GuardrailsMode)
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid guardrails mode should not persist config; stat err=%v", err)
	}
	auditPath := webAuditLogPath(cfg.Session.Dir)
	if data, err := os.ReadFile(auditPath); err == nil && strings.Contains(string(data), "web.config.write") {
		t.Fatalf("invalid guardrails mode should not append config audit event, got %s", string(data))
	}
}

func TestUpdateConfigReportsMissingCurrentDirectoryBeforeDefaultPathPersistence(t *testing.T) {
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	cwd := t.TempDir()
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir service cwd: %v", err)
	}
	cfg := testConfig(t, "")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: configPath})
	if err != nil {
		_ = os.Chdir(originalWD)
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	if err := os.Remove(cwd); err != nil {
		_ = os.Chdir(originalWD)
		t.Fatalf("remove service cwd: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()

	errResp := postJSONError(t, ts.URL+"/api/config", map[string]any{
		"provider":        "openai",
		"guardrails_mode": "standard",
	}, http.StatusInternalServerError)
	if !strings.Contains(errResp.Error, "getwd") {
		t.Fatalf("expected missing current directory error, got %#v", errResp)
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing cwd settings save should not persist config; stat err=%v", err)
	}
}

func TestAPIKeyWriteDoesNotLogSecretValue(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)

	cfg := testConfig(t, "")
	provider := cfg.Providers["openai"]
	provider.APIKeyEnv = "OPENAI_API_KEY"
	cfg.Providers["openai"] = provider
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: filepath.Join(cwd, "config.yaml")})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	postJSON(t, ts.URL+"/api/config", map[string]any{
		"provider": "openai",
		"api_key":  "sk-test-secret-value",
	}, http.StatusOK, nil)

	auditPath := webAuditLogPath(cfg.Session.Dir)
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	if strings.Contains(string(data), "sk-test-secret-value") {
		t.Fatalf("audit log must not contain secret value: %s", string(data))
	}
	events := loadWebAuditEvents(t, auditPath)
	if !hasWebAuditEvent(events, "web.config.api_key_write") {
		t.Fatalf("expected api key audit event, got %#v", events)
	}
	if !strings.Contains(string(data), "OPENAI_API_KEY") {
		t.Fatalf("expected env key in audit log, got %s", string(data))
	}
}

func TestAPIKeyWriteWaitsForConfigWriteSuccess(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	t.Setenv("OPENAI_API_KEY", "")
	envPath := filepath.Join(cwd, ".env")
	t.Setenv("GO_CLI_AGENT_ENV_FILE", envPath)

	cfg := testConfig(t, "")
	provider := cfg.Providers["openai"]
	provider.APIKeyEnv = "OPENAI_API_KEY"
	cfg.Providers["openai"] = provider
	configDir := filepath.Join(cwd, "config-dir")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: configDir})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/config", map[string]any{
		"provider": "openai",
		"api_key":  "sk-should-not-persist",
	}, http.StatusInternalServerError)
	if errResp.Error == "" {
		t.Fatalf("expected config write error, got %#v", errResp)
	}
	if got := os.Getenv("OPENAI_API_KEY"); got != "" {
		t.Fatalf("failed config write should not mutate process API key, got %q", got)
	}
	if data, err := os.ReadFile(envPath); err == nil && strings.Contains(string(data), "sk-should-not-persist") {
		t.Fatalf("failed config write should not persist API key, got %q", string(data))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read env file: %v", err)
	}
	auditPath := webAuditLogPath(cfg.Session.Dir)
	if data, err := os.ReadFile(auditPath); err == nil {
		if strings.Contains(string(data), "web.config") || strings.Contains(string(data), "sk-should-not-persist") {
			t.Fatalf("failed config write should not append audit event or secret, got %q", string(data))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read audit log: %v", err)
	}
}

func TestAPIKeyWritePreflightsEnvTargetBeforeConfigWrite(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	t.Setenv("CUSTOM_API_KEY", "")
	envPath := filepath.Join(cwd, ".env")
	t.Setenv("GO_CLI_AGENT_ENV_FILE", envPath)

	cfg := testConfig(t, "")
	cfg.Providers["custom"] = config.Provider{
		APIProvider: "openai-compatible",
		BaseURL:     "http://example.invalid/v1",
		Model:       "custom-model",
		TimeoutSec:  3,
	}
	configPath := filepath.Join(cwd, "config.yaml")
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/config", map[string]any{
		"provider": "custom",
		"model":    "custom-mutated-model",
		"api_key":  "sk-should-not-partially-save",
	}, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "env key is required") {
		t.Fatalf("expected env-key preflight error, got %#v", errResp)
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed API key preflight should not persist config; stat err=%v", err)
	}
	if data, err := os.ReadFile(envPath); err == nil && strings.Contains(string(data), "sk-should-not-partially-save") {
		t.Fatalf("failed API key preflight should not persist API key, got %q", string(data))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read env file: %v", err)
	}
	if got := os.Getenv("CUSTOM_API_KEY"); got != "" {
		t.Fatalf("failed API key preflight should not mutate process API key, got %q", got)
	}
	auditPath := webAuditLogPath(cfg.Session.Dir)
	if _, err := os.Stat(auditPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed API key preflight should not create audit log; stat err=%v", err)
	}
}

func TestAPIKeyWriteRollsBackConfigWhenEnvWriteFails(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	t.Setenv("OPENAI_API_KEY", "")
	configPath := filepath.Join(cwd, "config-as-env-parent")
	envPath := filepath.Join(configPath, ".env")
	t.Setenv("GO_CLI_AGENT_ENV_FILE", envPath)

	cfg := testConfig(t, "")
	provider := cfg.Providers["openai"]
	provider.APIKeyEnv = "OPENAI_API_KEY"
	cfg.Providers["openai"] = provider
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/config", map[string]any{
		"provider": "openai",
		"model":    "should-not-persist",
		"api_key":  "sk-should-not-persist",
	}, http.StatusInternalServerError)
	if errResp.Error == "" {
		t.Fatalf("expected env write error, got %#v", errResp)
	}
	if got := os.Getenv("OPENAI_API_KEY"); got != "" {
		t.Fatalf("failed env write should not mutate process API key, got %q", got)
	}
	if data, err := os.ReadFile(configPath); err == nil && strings.Contains(string(data), "should-not-persist") {
		t.Fatalf("failed env write should roll back config persistence, got %q", string(data))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read config file: %v", err)
	}
}

func TestAPIKeyWriteRollsBackWhenProcessEnvSetFails(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	t.Setenv("OPENAI_API_KEY", "sk-original-process")
	envPath := filepath.Join(cwd, ".env")
	t.Setenv("GO_CLI_AGENT_ENV_FILE", envPath)
	if err := os.WriteFile(envPath, []byte("OPENAI_API_KEY=sk-original-file\n"), 0o600); err != nil {
		t.Fatalf("write original env file: %v", err)
	}
	configPath := filepath.Join(cwd, "config.yaml")

	cfg := testConfig(t, "")
	provider := cfg.Providers["openai"]
	provider.APIKeyEnv = "OPENAI_API_KEY"
	provider.Model = "original-model"
	cfg.Providers["openai"] = provider
	if err := config.WriteFile(configPath, cfg); err != nil {
		t.Fatalf("write original config: %v", err)
	}

	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	svc.setProcessEnv = func(key, value string) error {
		if key == "OPENAI_API_KEY" && value == "sk-mutated-secret" {
			return errors.New("blocked process env set")
		}
		return os.Setenv(key, value)
	}
	svc.unsetProcessEnv = os.Unsetenv

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/config", map[string]any{
		"provider": "openai",
		"model":    "mutated-model",
		"api_key":  "sk-mutated-secret",
	}, http.StatusInternalServerError)
	if !strings.Contains(errResp.Error, "blocked process env set") {
		t.Fatalf("expected process env error, got %#v", errResp)
	}

	loaded, err := config.Load(configPath, cwd)
	if err != nil {
		t.Fatalf("load config after failed process env set: %v", err)
	}
	if loaded.Providers["openai"].Model != "original-model" {
		t.Fatalf("failed process env set should roll back config, got %#v", loaded.Providers["openai"])
	}
	envData, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read env after failed process env set: %v", err)
	}
	if string(envData) != "OPENAI_API_KEY=sk-original-file\n" || strings.Contains(string(envData), "sk-mutated-secret") {
		t.Fatalf("failed process env set should roll back env file, got %q", string(envData))
	}
	if got := os.Getenv("OPENAI_API_KEY"); got != "sk-original-process" {
		t.Fatalf("failed process env set should restore process env, got %q", got)
	}
	snapshot, err := svc.configSnapshot()
	if err != nil {
		t.Fatalf("config snapshot after failed process env set: %v", err)
	}
	if snapshot.Providers["openai"].Model != "original-model" || snapshot.APIKey("openai") != "sk-original-process" {
		t.Fatalf("failed process env set should not update in-memory settings, got provider=%#v key=%q", snapshot.Providers["openai"], snapshot.APIKey("openai"))
	}
	auditPath := webAuditLogPath(cfg.Session.Dir)
	if data, err := os.ReadFile(auditPath); err == nil {
		if strings.Contains(string(data), "web.config.write") ||
			strings.Contains(string(data), "web.config.api_key_write") ||
			strings.Contains(string(data), "sk-mutated-secret") {
			t.Fatalf("failed process env set should not append audit event or secret, got %q", string(data))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read audit log: %v", err)
	}
}

func TestAPIKeyWriteRejectsInvalidEnvKeyBeforePersistence(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	envPath := filepath.Join(cwd, ".env")
	t.Setenv("GO_CLI_AGENT_ENV_FILE", envPath)

	cfg := testConfig(t, "")
	cfg.Providers["badkey"] = config.Provider{
		APIProvider: "openai-compatible",
		APIKeyEnv:   "BAD=KEY_API_KEY",
		BaseURL:     "http://example.invalid/v1",
		Model:       "badkey-model",
		TimeoutSec:  3,
	}
	configPath := filepath.Join(cwd, "config.yaml")
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/config", map[string]any{
		"provider": "badkey",
		"model":    "should-not-persist",
		"api_key":  "sk-should-not-persist",
	}, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "invalid env key") {
		t.Fatalf("expected invalid env-key preflight error, got %#v", errResp)
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed API key preflight should not persist config; stat err=%v", err)
	}
	if data, err := os.ReadFile(envPath); err == nil && strings.Contains(string(data), "sk-should-not-persist") {
		t.Fatalf("failed API key preflight should not persist API key, got %q", string(data))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read env file: %v", err)
	}
	auditPath := webAuditLogPath(cfg.Session.Dir)
	if data, err := os.ReadFile(auditPath); err == nil {
		if strings.Contains(string(data), "web.config") || strings.Contains(string(data), "sk-should-not-persist") {
			t.Fatalf("failed API key preflight should not append audit event or secret, got %q", string(data))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read audit log: %v", err)
	}
}

func TestAPIKeyWriteRejectsInvalidEnvValueBeforePersistence(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	envPath := filepath.Join(cwd, ".env")
	t.Setenv("GO_CLI_AGENT_ENV_FILE", envPath)
	t.Setenv("OPENAI_API_KEY", "")

	cfg := testConfig(t, "")
	provider := cfg.Providers["openai"]
	provider.APIKeyEnv = "OPENAI_API_KEY"
	cfg.Providers["openai"] = provider
	configPath := filepath.Join(cwd, "config.yaml")
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	secret := "sk-invalid\x00value"
	errResp := postJSONError(t, ts.URL+"/api/config", map[string]any{
		"provider": "openai",
		"model":    "should-not-persist",
		"api_key":  secret,
	}, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "invalid env value") {
		t.Fatalf("expected invalid env value preflight error, got %#v", errResp)
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed API key preflight should not persist config; stat err=%v", err)
	}
	if data, err := os.ReadFile(envPath); err == nil && strings.Contains(string(data), "sk-invalid") {
		t.Fatalf("failed API key preflight should not persist API key, got %q", string(data))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read env file: %v", err)
	}
	if got := os.Getenv("OPENAI_API_KEY"); got != "" {
		t.Fatalf("failed API key preflight should not mutate process API key, got %q", got)
	}
}

func TestAPIKeyWriteRejectsBlankEnvValueBeforePersistence(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	envPath := filepath.Join(cwd, ".env")
	t.Setenv("GO_CLI_AGENT_ENV_FILE", envPath)
	t.Setenv("OPENAI_API_KEY", "")

	cfg := testConfig(t, "")
	provider := cfg.Providers["openai"]
	provider.APIKeyEnv = "OPENAI_API_KEY"
	cfg.Providers["openai"] = provider
	configPath := filepath.Join(cwd, "config.yaml")
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/config", map[string]any{
		"provider": "openai",
		"model":    "should-not-persist",
		"api_key":  "   \t  ",
	}, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "blank env value") {
		t.Fatalf("expected blank env value preflight error, got %#v", errResp)
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed API key preflight should not persist config; stat err=%v", err)
	}
	if data, err := os.ReadFile(envPath); err == nil && strings.Contains(string(data), "OPENAI_API_KEY") {
		t.Fatalf("failed API key preflight should not persist API key, got %q", string(data))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read env file: %v", err)
	}
	if got := os.Getenv("OPENAI_API_KEY"); got != "" {
		t.Fatalf("failed API key preflight should not mutate process API key, got %q", got)
	}
}

func TestAPIKeyWriteRejectsConfigPathAsEnvFile(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	configPath := filepath.Join(cwd, "config.yaml")
	t.Setenv("GO_CLI_AGENT_ENV_FILE", configPath)
	t.Setenv("OPENAI_API_KEY", "")

	cfg := testConfig(t, "")
	provider := cfg.Providers["openai"]
	provider.APIKeyEnv = "OPENAI_API_KEY"
	cfg.Providers["openai"] = provider
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/config", map[string]any{
		"provider": "openai",
		"model":    "should-not-persist",
		"api_key":  "sk-should-not-persist",
	}, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "env file must be separate") {
		t.Fatalf("expected target alias preflight error, got %#v", errResp)
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed API key preflight should not persist config/env file; stat err=%v", err)
	}
	if got := os.Getenv("OPENAI_API_KEY"); got != "" {
		t.Fatalf("failed API key preflight should not mutate process API key, got %q", got)
	}
}

func TestUpdateConfigRollsBackWhenConfigAuditAppendFails(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	configPath := filepath.Join(cwd, "config.yaml")

	cfg := testConfig(t, "")
	originalProvider := cfg.Providers["openai"]
	originalProvider.Model = "original-model"
	originalProvider.BaseURL = "http://127.0.0.1:1/v1"
	cfg.Providers["openai"] = originalProvider
	if err := config.WriteFile(configPath, cfg); err != nil {
		t.Fatalf("write original config: %v", err)
	}

	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	svc.beforeAppendAuditEvent = func(eventType string, data map[string]any) error {
		if eventType == "web.config.write" {
			return errors.New("blocked config audit append")
		}
		return nil
	}

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/config", map[string]any{
		"provider": "openai",
		"model":    "mutated-model",
		"base_url": "http://127.0.0.1:2/v1",
	}, http.StatusInternalServerError)
	if !strings.Contains(errResp.Error, "blocked config audit append") {
		t.Fatalf("expected audit append error, got %#v", errResp)
	}

	loaded, err := config.Load(configPath, cwd)
	if err != nil {
		t.Fatalf("load config after failed audit append: %v", err)
	}
	if loaded.Providers["openai"].Model != "original-model" || loaded.Providers["openai"].BaseURL != "http://127.0.0.1:1/v1" {
		t.Fatalf("failed audit append should roll back persisted config, got %#v", loaded.Providers["openai"])
	}
	snapshot, err := svc.configSnapshot()
	if err != nil {
		t.Fatalf("config snapshot after failed audit append: %v", err)
	}
	if snapshot.Providers["openai"].Model != "original-model" || snapshot.Providers["openai"].BaseURL != "http://127.0.0.1:1/v1" {
		t.Fatalf("failed audit append should not update in-memory config, got %#v", snapshot.Providers["openai"])
	}
	auditPath := webAuditLogPath(cfg.Session.Dir)
	if data, err := os.ReadFile(auditPath); err == nil && strings.Contains(string(data), "web.config.write") {
		t.Fatalf("failed audit append should not append config audit event, got %q", string(data))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read audit log: %v", err)
	}
}

func TestAPIKeyWriteRollsBackWhenAPIKeyAuditAppendFails(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	t.Setenv("OPENAI_API_KEY", "sk-original-process")
	envPath := filepath.Join(cwd, ".env")
	t.Setenv("GO_CLI_AGENT_ENV_FILE", envPath)
	if err := os.WriteFile(envPath, []byte("OPENAI_API_KEY=sk-original-file\n"), 0o600); err != nil {
		t.Fatalf("write original env file: %v", err)
	}
	configPath := filepath.Join(cwd, "config.yaml")

	cfg := testConfig(t, "")
	provider := cfg.Providers["openai"]
	provider.APIKeyEnv = "OPENAI_API_KEY"
	provider.Model = "original-model"
	cfg.Providers["openai"] = provider
	if err := config.WriteFile(configPath, cfg); err != nil {
		t.Fatalf("write original config: %v", err)
	}

	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	svc.beforeAppendAuditEvent = func(eventType string, data map[string]any) error {
		if eventType == "web.config.api_key_write" {
			return errors.New("blocked api key audit append")
		}
		return nil
	}

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/config", map[string]any{
		"provider": "openai",
		"model":    "mutated-model",
		"api_key":  "sk-mutated-secret",
	}, http.StatusInternalServerError)
	if !strings.Contains(errResp.Error, "blocked api key audit append") {
		t.Fatalf("expected api key audit append error, got %#v", errResp)
	}

	loaded, err := config.Load(configPath, cwd)
	if err != nil {
		t.Fatalf("load config after failed api key audit append: %v", err)
	}
	if loaded.Providers["openai"].Model != "original-model" {
		t.Fatalf("failed API key audit append should roll back config, got %#v", loaded.Providers["openai"])
	}
	envData, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read env after failed api key audit append: %v", err)
	}
	if string(envData) != "OPENAI_API_KEY=sk-original-file\n" || strings.Contains(string(envData), "sk-mutated-secret") {
		t.Fatalf("failed API key audit append should roll back env file, got %q", string(envData))
	}
	if got := os.Getenv("OPENAI_API_KEY"); got != "sk-original-process" {
		t.Fatalf("failed API key audit append should restore process env, got %q", got)
	}
	snapshot, err := svc.configSnapshot()
	if err != nil {
		t.Fatalf("config snapshot after failed API key audit append: %v", err)
	}
	if snapshot.Providers["openai"].Model != "original-model" || snapshot.APIKey("openai") != "sk-original-process" {
		t.Fatalf("failed API key audit append should not update in-memory settings, got provider=%#v key=%q", snapshot.Providers["openai"], snapshot.APIKey("openai"))
	}
	auditData, err := os.ReadFile(webAuditLogPath(cfg.Session.Dir))
	if err != nil {
		t.Fatalf("read audit log after failed API key audit append: %v", err)
	}
	if strings.Contains(string(auditData), "web.config.write") ||
		strings.Contains(string(auditData), "web.config.api_key_write") ||
		strings.Contains(string(auditData), "sk-mutated-secret") {
		t.Fatalf("failed API key audit append should not record rolled-back config/API key audit events or secret, got %q", string(auditData))
	}
}

func TestSkillUploadRollsBackWhenAuditAppendFails(t *testing.T) {
	cfg := testConfig(t, "")
	skillsDir := filepath.Join(t.TempDir(), "skills")
	cfg.Skills.Dirs = []string{skillsDir}
	existing := filepath.Join(skillsDir, "demo-skill")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatalf("mkdir existing skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(existing, "SKILL.md"), []byte("---\nname: demo-skill\n---\nold body\n"), 0o600); err != nil {
		t.Fatalf("write existing skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(existing, "old.txt"), []byte("old payload\n"), 0o600); err != nil {
		t.Fatalf("write existing payload: %v", err)
	}

	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	svc.beforeAppendAuditEvent = func(eventType string, data map[string]any) error {
		if eventType == "web.skill.install" {
			return errors.New("blocked skill install audit append")
		}
		return nil
	}

	ts := httptest.NewServer(svc)
	defer ts.Close()

	zipPath := filepath.Join(t.TempDir(), "skill.zip")
	createZipEntries(t, zipPath, map[string]string{
		"demo-skill/SKILL.md": "---\nname: demo-skill\n---\nnew body\n",
		"demo-skill/new.txt":  "new payload\n",
	})
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filepath.Base(zipPath))
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	zipBytes, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatalf("read zip: %v", err)
	}
	if _, err := part.Write(zipBytes); err != nil {
		t.Fatalf("write zip to multipart: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/skills/upload", body)
	if err != nil {
		t.Fatalf("new upload request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set(webMutationHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload request: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError || !strings.Contains(string(respBody), "blocked skill install audit append") {
		t.Fatalf("expected blocked audit append response, got status=%d body=%s", resp.StatusCode, string(respBody))
	}

	data, err := os.ReadFile(filepath.Join(existing, "SKILL.md"))
	if err != nil {
		t.Fatalf("read restored existing skill: %v", err)
	}
	if !strings.Contains(string(data), "old body") || strings.Contains(string(data), "new body") {
		t.Fatalf("failed skill install audit append should restore existing SKILL.md, got %q", string(data))
	}
	if data, err := os.ReadFile(filepath.Join(existing, "old.txt")); err != nil || string(data) != "old payload\n" {
		t.Fatalf("failed skill install audit append should restore existing payload, data=%q err=%v", string(data), err)
	}
	if _, err := os.Stat(filepath.Join(existing, "new.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed skill install audit append should remove new payload, err=%v", err)
	}
	auditPath := webAuditLogPath(cfg.Session.Dir)
	if data, err := os.ReadFile(auditPath); err == nil && strings.Contains(string(data), "web.skill.install") {
		t.Fatalf("failed skill install audit append should not append install event, got %q", string(data))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read audit log: %v", err)
	}
}

func TestSkillUninstallRollsBackWhenAuditAppendFails(t *testing.T) {
	cfg := testConfig(t, "")
	skillsDir := filepath.Join(t.TempDir(), "skills")
	cfg.Skills.Dirs = []string{skillsDir}
	target := filepath.Join(skillsDir, "demo-skill")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("---\nname: demo-skill\n---\nbody\n"), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "payload.txt"), []byte("payload\n"), 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	svc.beforeAppendAuditEvent = func(eventType string, data map[string]any) error {
		if eventType == "web.skill.uninstall" {
			return errors.New("blocked skill uninstall audit append")
		}
		return nil
	}

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/skills/demo-skill/uninstall", map[string]any{}, http.StatusInternalServerError)
	if !strings.Contains(errResp.Error, "blocked skill uninstall audit append") {
		t.Fatalf("expected blocked uninstall audit append error, got %#v", errResp)
	}
	data, err := os.ReadFile(filepath.Join(target, "SKILL.md"))
	if err != nil {
		t.Fatalf("failed skill uninstall audit append should restore SKILL.md: %v", err)
	}
	if !strings.Contains(string(data), "body") {
		t.Fatalf("failed skill uninstall audit append should restore skill body, got %q", string(data))
	}
	if data, err := os.ReadFile(filepath.Join(target, "payload.txt")); err != nil || string(data) != "payload\n" {
		t.Fatalf("failed skill uninstall audit append should restore payload, data=%q err=%v", string(data), err)
	}
	auditPath := webAuditLogPath(cfg.Session.Dir)
	if data, err := os.ReadFile(auditPath); err == nil && strings.Contains(string(data), "web.skill.uninstall") {
		t.Fatalf("failed skill uninstall audit append should not append uninstall event, got %q", string(data))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read audit log: %v", err)
	}
}

func TestUpdateConfigRejectsConfigPathAsAuditLog(t *testing.T) {
	cfg := testConfig(t, "")
	configPath := webAuditLogPath(cfg.Session.Dir)
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/config", map[string]any{
		"provider": "openai",
		"model":    "should-not-persist",
	}, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "audit log must be separate") {
		t.Fatalf("expected audit alias preflight error, got %#v", errResp)
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed config preflight should not persist config/audit log; stat err=%v", err)
	}
}

func TestAPIKeyWriteRejectsEnvFileAsAuditLog(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)

	cfg := testConfig(t, "")
	auditPath := webAuditLogPath(cfg.Session.Dir)
	t.Setenv("GO_CLI_AGENT_ENV_FILE", auditPath)
	t.Setenv("OPENAI_API_KEY", "")
	provider := cfg.Providers["openai"]
	provider.APIKeyEnv = "OPENAI_API_KEY"
	cfg.Providers["openai"] = provider
	configPath := filepath.Join(cwd, "config.yaml")
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/config", map[string]any{
		"provider": "openai",
		"model":    "should-not-persist",
		"api_key":  "sk-should-not-persist",
	}, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "env file and audit log must be separate") {
		t.Fatalf("expected env/audit alias preflight error, got %#v", errResp)
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed API key preflight should not persist config; stat err=%v", err)
	}
	if data, err := os.ReadFile(auditPath); err == nil && strings.Contains(string(data), "sk-should-not-persist") {
		t.Fatalf("failed API key preflight should not persist secret to audit log, got %q", string(data))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read audit log: %v", err)
	}
	if got := os.Getenv("OPENAI_API_KEY"); got != "" {
		t.Fatalf("failed API key preflight should not mutate process API key, got %q", got)
	}
}

func TestAppendAuditEventRejectsSymlinkedAuditLog(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: filepath.Join(t.TempDir(), "config.yaml")})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	auditPath := webAuditLogPath(cfg.Session.Dir)
	if err := os.MkdirAll(filepath.Dir(auditPath), 0o700); err != nil {
		t.Fatalf("mkdir audit dir: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside-audit.jsonl")
	if err := os.WriteFile(outside, []byte("keep\n"), 0o600); err != nil {
		t.Fatalf("write outside audit: %v", err)
	}
	if err := os.Symlink(outside, auditPath); err != nil {
		t.Fatalf("symlink audit log: %v", err)
	}

	if err := svc.appendAuditEvent("web.test", nil); err == nil {
		t.Fatal("expected symlinked audit log append to fail")
	}
	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("read outside audit: %v", err)
	}
	if string(data) != "keep\n" {
		t.Fatalf("outside audit log was modified: %q", data)
	}
}

func TestAppendAuditEventRejectsReplacedParent(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: filepath.Join(t.TempDir(), "config.yaml")})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	auditPath := webAuditLogPath(cfg.Session.Dir)
	auditDir := filepath.Dir(auditPath)
	if err := os.MkdirAll(auditDir, 0o700); err != nil {
		t.Fatalf("mkdir audit dir: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside-audit-dir")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatalf("mkdir outside audit dir: %v", err)
	}
	outsideAudit := filepath.Join(outside, filepath.Base(auditPath))
	originalOutside := `{"schema_version":1,"id":"audit_outside","type":"web.outside","time":"2026-05-30T00:00:00Z"}` + "\n"
	if err := os.WriteFile(outsideAudit, []byte(originalOutside), 0o600); err != nil {
		t.Fatalf("write outside audit: %v", err)
	}

	restore := beforeOpenAuditLog
	swapped := false
	beforeOpenAuditLog = func(openPath string) error {
		if swapped || filepath.Clean(openPath) != filepath.Clean(auditPath) {
			return nil
		}
		swapped = true
		if err := os.Rename(auditDir, auditDir+".real"); err != nil {
			return err
		}
		return os.Symlink(outside, auditDir)
	}
	defer func() {
		beforeOpenAuditLog = restore
	}()

	err = svc.appendAuditEvent("web.test", nil)
	if err == nil {
		data, readErr := os.ReadFile(outsideAudit)
		if readErr != nil {
			t.Fatalf("read outside audit after unexpected success: %v", readErr)
		}
		t.Fatalf("expected replaced audit parent append to fail, but it succeeded and outside log became %q", data)
	}
	if !strings.Contains(err.Error(), "symlink") && !strings.Contains(err.Error(), "changed") {
		t.Fatalf("expected symlink/path-change error, got %v", err)
	}
	data, readErr := os.ReadFile(outsideAudit)
	if readErr != nil {
		t.Fatalf("read outside audit: %v", readErr)
	}
	if string(data) != originalOutside {
		t.Fatalf("outside audit log should not be modified, got %q", data)
	}
}

func TestAppendAuditEventRejectsMalformedExistingLog(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: filepath.Join(t.TempDir(), "config.yaml")})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	auditPath := webAuditLogPath(cfg.Session.Dir)
	if err := os.MkdirAll(filepath.Dir(auditPath), 0o700); err != nil {
		t.Fatalf("mkdir audit dir: %v", err)
	}
	malformed := `{"schema_version":1,"id":"","type":"web.session.delete","time":"2026-05-28T00:00:00Z"}` + "\n"
	if err := os.WriteFile(auditPath, []byte(malformed), 0o600); err != nil {
		t.Fatalf("write audit log: %v", err)
	}

	if err := svc.appendAuditEvent("web.test", nil); err == nil {
		t.Fatal("expected malformed existing audit log append to fail")
	}
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("malformed existing audit log should not be extended, got %d records: %q", len(lines), string(data))
	}
}

func TestAppendAuditEventRejectsDuplicateExistingAuditIDs(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: filepath.Join(t.TempDir(), "config.yaml")})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	auditPath := webAuditLogPath(cfg.Session.Dir)
	if err := os.MkdirAll(filepath.Dir(auditPath), 0o700); err != nil {
		t.Fatalf("mkdir audit dir: %v", err)
	}
	existing := strings.Join([]string{
		`{"schema_version":1,"id":"audit_duplicate","type":"web.session.delete","time":"2026-05-28T00:00:00Z"}`,
		`{"schema_version":1,"id":"audit_duplicate","type":"web.sessions.clear","time":"2026-05-28T00:00:01Z"}`,
		"",
	}, "\n")
	if err := os.WriteFile(auditPath, []byte(existing), 0o600); err != nil {
		t.Fatalf("write audit log: %v", err)
	}

	if err := svc.appendAuditEvent("web.test", nil); err == nil {
		t.Fatal("expected duplicate existing audit id append to fail")
	}
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("duplicate existing audit log should not be extended, got %d records: %q", len(lines), string(data))
	}
}

func TestSensitiveActionsPreflightAuditBeforeMutating(t *testing.T) {
	cfg := testConfig(t, "")
	base := t.TempDir()
	skillsDir := filepath.Join(base, "skills")
	cfg.Skills.Dirs = []string{skillsDir}
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: filepath.Join(t.TempDir(), "config.yaml")})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	meta := testSessionMetadata(t, "session_audit_preflight")
	if err := svc.store.Create(meta, testSessionState(session.StatusCompleted)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(skillsDir, "demo-skill"), 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "demo-skill", "SKILL.md"), []byte("---\nname: demo-skill\n---\n"), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	auditPath := webAuditLogPath(cfg.Session.Dir)
	if err := os.MkdirAll(auditPath, 0o700); err != nil {
		t.Fatalf("create audit path directory: %v", err)
	}

	ts := httptest.NewServer(svc)
	defer ts.Close()

	deleteReq, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/sessions/"+meta.ID, nil)
	if err != nil {
		t.Fatalf("new delete request: %v", err)
	}
	deleteReq.Header.Set(webMutationHeader, "1")
	deleteResp, err := http.DefaultClient.Do(deleteReq)
	if err != nil {
		t.Fatalf("delete request: %v", err)
	}
	deleteBody, _ := io.ReadAll(deleteResp.Body)
	deleteResp.Body.Close()
	if deleteResp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected delete audit preflight failure, got %d body=%s", deleteResp.StatusCode, string(deleteBody))
	}
	if _, err := svc.store.LoadMetadata(meta.ID); err != nil {
		t.Fatalf("session should remain after audit preflight failure: %v", err)
	}

	errResp := postJSONError(t, ts.URL+"/api/skills/demo-skill/uninstall", map[string]any{}, http.StatusInternalServerError)
	if errResp.Error == "" {
		t.Fatalf("expected uninstall audit preflight error, got %#v", errResp)
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "demo-skill", "SKILL.md")); err != nil {
		t.Fatalf("skill should remain after audit preflight failure: %v", err)
	}
}

func TestSensitiveWebActionsEmitAuditEvents(t *testing.T) {
	cfg := testConfig(t, "")
	skillsDir := filepath.Join(t.TempDir(), "skills")
	cfg.Skills.Dirs = []string{skillsDir}
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: filepath.Join(t.TempDir(), "config.yaml")})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	deleteMeta := testSessionMetadata(t, "session_delete_audit")
	if err := svc.store.Create(deleteMeta, testSessionState(session.StatusCompleted)); err != nil {
		t.Fatalf("create delete session: %v", err)
	}
	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/sessions/"+deleteMeta.ID, nil)
	if err != nil {
		t.Fatalf("new delete request: %v", err)
	}
	req.Header.Set(webMutationHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete session request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected delete status: %d", resp.StatusCode)
	}

	clearMeta := testSessionMetadata(t, "session_clear_audit")
	if err := svc.store.Create(clearMeta, testSessionState(session.StatusCompleted)); err != nil {
		t.Fatalf("create clear session: %v", err)
	}
	postJSON(t, ts.URL+"/api/sessions/clear", map[string]any{}, http.StatusOK, nil)

	zipPath := filepath.Join(t.TempDir(), "skill.zip")
	createSkillZip(t, zipPath, "demo-skill", "---\nname: demo-skill\ndescription: uploaded demo\n---\n")
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filepath.Base(zipPath))
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	file, err := os.Open(zipPath)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	if _, err := io.Copy(part, file); err != nil {
		file.Close()
		t.Fatalf("copy zip: %v", err)
	}
	file.Close()
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req, err = http.NewRequest(http.MethodPost, ts.URL+"/api/skills/upload", body)
	if err != nil {
		t.Fatalf("new upload request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set(webMutationHeader, "1")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload skill: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected upload status: %d", resp.StatusCode)
	}
	postJSON(t, ts.URL+"/api/skills/demo-skill/uninstall", map[string]any{}, http.StatusOK, nil)

	events := loadWebAuditEvents(t, webAuditLogPath(cfg.Session.Dir))
	for _, eventType := range []string{"web.session.delete", "web.sessions.clear", "web.skill.install", "web.skill.uninstall"} {
		if !hasWebAuditEvent(events, eventType) {
			t.Fatalf("expected audit event %s, got %#v", eventType, events)
		}
	}
}

func TestServiceServesEmbeddedShellAndAssets(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	server := httptest.NewServer(svc)
	defer server.Close()

	checkBody := func(url string) string {
		resp, err := http.Get(url)
		if err != nil {
			t.Fatalf("get %s: %v", url, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("unexpected status for %s: %d", url, resp.StatusCode)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body %s: %v", url, err)
		}
		return string(body)
	}

	indexBody := checkBody(server.URL + "/")
	if !strings.Contains(indexBody, "Agent Console") || !strings.Contains(indexBody, "Describe the task for this session...") || !strings.Contains(indexBody, "new-session-btn") || !strings.Contains(indexBody, "interrupt-session-btn") || !strings.Contains(indexBody, "stop-session-btn") || !strings.Contains(indexBody, "interrupt-toggle-btn") || !strings.Contains(indexBody, "chat-messages") || !strings.Contains(indexBody, "toast-rack") || !strings.Contains(indexBody, "workspace-subtitle") {
		t.Fatalf("unexpected shell body: %s", indexBody)
	}
	if !strings.Contains(indexBody, "plan-toggle-btn") || !strings.Contains(indexBody, "<span>Plan</span>") {
		t.Fatalf("expected shell to expose Plan Mode toggle beside Goal, got shell body: %s", indexBody)
	}
	if !strings.Contains(indexBody, `id="agent-name-input"`) || !strings.Contains(indexBody, `id="agent-role-select"`) || !strings.Contains(indexBody, `<option value="evaluator">Evaluator</option>`) {
		t.Fatalf("expected role-aware start form to expose agent_name and agent_role controls, got shell body: %s", indexBody)
	}
	if strings.Contains(indexBody, "provider-override-panel") || strings.Contains(indexBody, "session-provider-override") || strings.Contains(indexBody, "session-model-override") || strings.Contains(indexBody, "Advanced provider") {
		t.Fatalf("expected shell composer to omit per-session provider/model override controls, got shell body: %s", indexBody)
	}
	if strings.Contains(indexBody, "Background Jobs") || strings.Contains(indexBody, `data-view="queue"`) || strings.Contains(indexBody, "queue-view") || !strings.Contains(indexBody, "<span>Sessions</span>") || strings.Contains(indexBody, "<span>Queue</span>") || strings.Contains(indexBody, "<span>History</span>") {
		t.Fatalf("expected shell navigation to remove standalone Background Jobs and keep Sessions label, got shell body: %s", indexBody)
	}
	if !strings.Contains(indexBody, "Enter to send, Shift+Enter / Ctrl+Enter for new line") {
		t.Fatalf("expected updated input shortcut hint, got shell body: %s", indexBody)
	}
	if !strings.Contains(indexBody, `id="send-btn" type="button" aria-label="Send message" title="Send message"`) {
		t.Fatalf("expected icon-only send button to expose an accessible name, got shell body: %s", indexBody)
	}
	if strings.Contains(indexBody, "theme-toggle") || strings.Contains(indexBody, "Toggle theme") {
		t.Fatalf("expected dark mode toggle to be removed, got shell body: %s", indexBody)
	}
	if strings.Contains(indexBody, `data-view="overview"`) || strings.Contains(indexBody, "Overview</span>") || strings.Contains(indexBody, "overview-view") {
		t.Fatalf("expected standalone overview page to be removed from shell, got shell body: %s", indexBody)
	}
	if strings.Contains(indexBody, "https://") || !strings.Contains(indexBody, "utils.js") || !strings.Contains(indexBody, "icons.js") || !strings.Contains(indexBody, "api.js") || !strings.Contains(indexBody, "events.js") || !strings.Contains(indexBody, "settings-view.js") || !strings.Contains(indexBody, "workspace-view.js") || !strings.Contains(indexBody, "session-view.js") {
		t.Fatalf("expected shell to use local assets only, got shell body: %s", indexBody)
	}
	if !strings.Contains(indexBody, "utils.js") || !strings.Contains(indexBody, "icons.js") || !strings.Contains(indexBody, "api.js") || !strings.Contains(indexBody, "events.js") || !strings.Contains(indexBody, "settings-view.js") || !strings.Contains(indexBody, "workspace-view.js") || !strings.Contains(indexBody, "session-view.js") || !strings.Contains(indexBody, "app.js") {
		t.Fatalf("expected shell to load utils/icons/api/events/settings/workspace/session/app assets, got shell body: %s", indexBody)
	}
	iconsBody := checkBody(server.URL + "/icons.js")
	if !strings.Contains(iconsBody, "window.lucide") || !strings.Contains(iconsBody, "createIcons") {
		t.Fatalf("unexpected icons.js body: %s", iconsBody)
	}
	utilsBody := checkBody(server.URL + "/utils.js")
	if !strings.Contains(utilsBody, "safeMarkdown") || !strings.Contains(utilsBody, "escapeHTML") || !strings.Contains(utilsBody, "collectShellRedirectPaths") || strings.Contains(utilsBody, "unpkg.com") || strings.Contains(utilsBody, "cdn.jsdelivr.net") {
		t.Fatalf("unexpected utils.js body: %s", utilsBody)
	}
	apiBody := checkBody(server.URL + "/api.js")
	if !strings.Contains(apiBody, "class APIError") || !strings.Contains(apiBody, "function requestJSON") || !strings.Contains(apiBody, "function startSession") || !strings.Contains(apiBody, "function continueSession") || !strings.Contains(apiBody, "function steerSession") {
		t.Fatalf("unexpected api.js body: %s", apiBody)
	}
	if !strings.Contains(apiBody, "function getPlanMode") || !strings.Contains(apiBody, "function approvePlanMode") || !strings.Contains(apiBody, "function revisePlanMode") || !strings.Contains(apiBody, "function cancelPlanMode") || !strings.Contains(apiBody, "function answerPlanModeInput") || !strings.Contains(apiBody, "request_id: payload.requestID") {
		t.Fatalf("expected api.js to expose Plan Mode helpers and snake_case input payload, got api.js body: %s", apiBody)
	}
	if strings.Contains(apiBody, "unpkg.com") || strings.Contains(apiBody, "cdn.jsdelivr.net") {
		t.Fatalf("expected api.js to avoid external dependencies, got api.js body: %s", apiBody)
	}
	eventsBody := checkBody(server.URL + "/events.js")
	if !strings.Contains(eventsBody, "describeTimelineItem") || !strings.Contains(eventsBody, "describeEventDescriptor") || !strings.Contains(eventsBody, "shouldRefreshAfterEvent") || !strings.Contains(eventsBody, "Background results accepted") {
		t.Fatalf("unexpected events.js body: %s", eventsBody)
	}
	if !strings.Contains(eventsBody, "planmode.plan_submitted") || !strings.Contains(eventsBody, "planmode.input_requested") || !strings.Contains(eventsBody, "planmode.execution_started") {
		t.Fatalf("expected events.js to describe Plan Mode timeline events, got events.js body: %s", eventsBody)
	}
	settingsBody := checkBody(server.URL + "/settings-view.js")
	if !strings.Contains(settingsBody, "renderSettings") || !strings.Contains(settingsBody, "saveConfig") || !strings.Contains(settingsBody, "testConfig") || !strings.Contains(settingsBody, "settings-provider") || !strings.Contains(settingsBody, "settings-reasoning-mode") || !strings.Contains(settingsBody, "settings-test-btn") {
		t.Fatalf("unexpected settings-view.js body: %s", settingsBody)
	}
	if !strings.Contains(settingsBody, "apiProviderSelect.addEventListener('change'") || !strings.Contains(settingsBody, "reasoningModesForAPIProvider") || !strings.Contains(settingsBody, "reasoningSummaryModesForAPIProvider") {
		t.Fatalf("expected settings view to refresh reasoning controls when API Provider changes, got settings-view.js body: %s", settingsBody)
	}
	if !strings.Contains(settingsBody, "confirmSettingsSave") || !strings.Contains(settingsBody, "write the entered API key to the local env file") {
		t.Fatalf("expected settings save to require explicit local config/API key confirmation, got settings-view.js body: %s", settingsBody)
	}
	if !strings.Contains(settingsBody, "function settingsErrorMessage") || !strings.Contains(settingsBody, "panel.textContent = message") || !strings.Contains(settingsBody, "showToast(message, 'error')") {
		t.Fatalf("expected settings load failures to surface backend API errors, got settings-view.js body: %s", settingsBody)
	}
	workspaceBody := checkBody(server.URL + "/workspace-view.js")
	if !strings.Contains(workspaceBody, "fetchWorkspace") || !strings.Contains(workspaceBody, "renderFileTree") || !strings.Contains(workspaceBody, "selectedWorkspaceWorkdir") {
		t.Fatalf("unexpected workspace-view.js body: %s", workspaceBody)
	}
	if !strings.Contains(workspaceBody, "function workspaceErrorMessage") || !strings.Contains(workspaceBody, "const message = workspaceErrorMessage(err") || !strings.Contains(workspaceBody, "nodes.editorContent.innerText = message") || !strings.Contains(workspaceBody, "showToast(message, 'error')") {
		t.Fatalf("expected workspace frontend failures to surface backend API errors, got workspace-view.js body: %s", workspaceBody)
	}
	if !strings.Contains(workspaceBody, "currentWorkspacePath()") || !strings.Contains(workspaceBody, "setCurrentWorkspacePath(normalized)") || !strings.Contains(workspaceBody, "currentWorkspaceTree()") || strings.Contains(workspaceBody, "state.workspacePath") || strings.Contains(workspaceBody, "state.fileTree") {
		t.Fatalf("expected Workspace directory display state to stay out of durable app state, got workspace-view.js body: %s", workspaceBody)
	}
	sessionBody := checkBody(server.URL + "/session-view.js")
	if !strings.Contains(sessionBody, "renderCurrentSession") || !strings.Contains(sessionBody, "renderPendingStageCard") || !strings.Contains(sessionBody, "renderMessageText") || !strings.Contains(sessionBody, "renderBackgroundResultsMessage") || !strings.Contains(sessionBody, "renderQueueJobCard") {
		t.Fatalf("unexpected session-view.js body: %s", sessionBody)
	}
	if !strings.Contains(sessionBody, "renderPlanPanel") || !strings.Contains(sessionBody, "renderPlanInputRequest") || !strings.Contains(sessionBody, "data-plan-action=\"approve\"") || !strings.Contains(sessionBody, "data-plan-input-action=\"select\"") || !strings.Contains(sessionBody, "data-plan-input-action=\"submit\"") {
		t.Fatalf("expected session-view.js to render Plan Mode inspector and pending input controls, got session-view.js body: %s", sessionBody)
	}
	if !strings.Contains(sessionBody, "renderSessionStopButton") || !strings.Contains(sessionBody, "data-stop-session-id") {
		t.Fatalf("expected session and sub-session cards to expose inline stop controls, got session-view.js body: %s", sessionBody)
	}
	if !strings.Contains(sessionBody, "Session is not running") {
		t.Fatalf("expected non-running session stop controls to render disabled explanatory text, got session-view.js body: %s", sessionBody)
	}
	if strings.Contains(sessionBody, "marked.parse") || strings.Contains(sessionBody, "unpkg.com") || strings.Contains(sessionBody, "cdn.jsdelivr.net") {
		t.Fatalf("expected session-view.js to avoid external markdown/icon dependencies, got session-view.js body: %s", sessionBody)
	}

	jsBody := checkBody(server.URL + "/app.js")
	if !strings.Contains(jsBody, "setupWebSocket") || !strings.Contains(jsBody, "resetChatSession") || !strings.Contains(jsBody, "showToast") || !strings.Contains(jsBody, "requestJSON") || !strings.Contains(jsBody, "refreshSelectedQueueJobDetail") {
		t.Fatalf("unexpected app.js body: %s", jsBody)
	}
	if !strings.Contains(jsBody, "navigationViewState") || !strings.Contains(jsBody, "currentViewName()") || !strings.Contains(jsBody, "setCurrentViewName(viewName)") || strings.Contains(jsBody, "state.currentView") {
		t.Fatalf("expected current navigation view state to stay out of durable app state, got app.js body: %s", jsBody)
	}
	if strings.Contains(jsBody, "state.workspacePath") || strings.Contains(jsBody, "state.fileTree") {
		t.Fatalf("expected app.js durable state to exclude Workspace directory display fields, got app.js body: %s", jsBody)
	}
	if strings.Contains(jsBody, "renderQueueView") || strings.Contains(jsBody, "fetchQueue") || strings.Contains(jsBody, "queueData") || strings.Contains(jsBody, "refreshingQueue") {
		t.Fatalf("expected app.js to remove standalone Background Jobs view state and renderer, got app.js body: %s", jsBody)
	}
	if !strings.Contains(jsBody, "togglePlanMode") || !strings.Contains(jsBody, "collectPlanModeDraft") || !strings.Contains(jsBody, "handlePlanModeAction") || !strings.Contains(jsBody, "handlePlanInputAction") {
		t.Fatalf("expected app.js to wire Plan Mode toggle and controls, got app.js body: %s", jsBody)
	}
	if !strings.Contains(jsBody, "collectAgentDraft") || !strings.Contains(jsBody, "agentName: agentDraft.agentName || undefined") || !strings.Contains(jsBody, "agentRole: agentDraft.agentRole || undefined") {
		t.Fatalf("expected app.js to pass role-aware start form fields through startSession, got app.js body: %s", jsBody)
	}
	if strings.Contains(jsBody, "collectProviderOverride") || strings.Contains(jsBody, "renderProviderOverrideControls") || strings.Contains(jsBody, "provider: providerOverride.provider || undefined") || strings.Contains(jsBody, "model: providerOverride.model || undefined") {
		t.Fatalf("expected app.js to rely on Settings provider/model instead of composer overrides, got app.js body: %s", jsBody)
	}
	if !strings.Contains(jsBody, "isCoverageApprovalBlock") || !strings.Contains(jsBody, "confirmCoverageOverride") || !strings.Contains(jsBody, "override_coverage: true") {
		t.Fatalf("expected app.js to require explicit coverage override confirmation, got app.js body: %s", jsBody)
	}
	if !strings.Contains(jsBody, "confirmGoalClear") || !strings.Contains(jsBody, "confirmSkillUninstall") || !strings.Contains(jsBody, "Skill uninstall cancelled") {
		t.Fatalf("expected risky goal clear and skill uninstall actions to require confirmation, got app.js body: %s", jsBody)
	}
	if !strings.Contains(jsBody, "skillsViewState.uploadInFlight") || !strings.Contains(jsBody, "setSkillUploadPending") {
		t.Fatalf("expected skill upload to track and restore pending controls, got app.js body: %s", jsBody)
	}
	if !strings.Contains(jsBody, "currentSkills()") || !strings.Contains(jsBody, "setCurrentSkills(skills)") || strings.Contains(jsBody, "state.skills") {
		t.Fatalf("expected Skills catalog display state to stay out of durable app state, got app.js body: %s", jsBody)
	}
	if !strings.Contains(jsBody, "const message = err?.message || 'Failed to load local skills.'") || !strings.Contains(jsBody, "showToast(message, 'error')") {
		t.Fatalf("expected skills list failures to surface backend errors, got app.js body: %s", jsBody)
	}
	if !strings.Contains(jsBody, "function historyErrorMessage") || !strings.Contains(jsBody, "const message = historyErrorMessage(err)") || !strings.Contains(jsBody, "panel.textContent = message") || !strings.Contains(jsBody, "showToast(message, 'error')") {
		t.Fatalf("expected history load failures to surface backend errors, got app.js body: %s", jsBody)
	}
	if !strings.Contains(jsBody, "setComposerMode") || !strings.Contains(jsBody, "normalizeComposerMode") || !strings.Contains(jsBody, "mode === 'goal'") || !strings.Contains(jsBody, "mode === 'plan'") {
		t.Fatalf("expected Goal and Plan composer controls to be mutually exclusive, got app.js body: %s", jsBody)
	}
	if strings.Contains(jsBody, "renderOverviewView") || strings.Contains(jsBody, "data-worker-scale") || strings.Contains(jsBody, "Worker Pool") {
		t.Fatalf("expected default UI to hide overview and worker-pool controls, got app.js body: %s", jsBody)
	}
	if strings.Contains(jsBody, "type: 'chat'") || strings.Contains(jsBody, `type: "chat"`) {
		t.Fatalf("expected frontend session control to avoid websocket chat payloads, got app.js body: %s", jsBody)
	}
	if strings.Contains(jsBody, "marked.parse") || strings.Contains(jsBody, "unpkg.com") || strings.Contains(jsBody, "cdn.jsdelivr.net") {
		t.Fatalf("expected app.js to avoid external markdown/icon dependencies, got app.js body: %s", jsBody)
	}
	if !strings.Contains(jsBody, "shouldSubmitChatInput") {
		t.Fatalf("expected explicit chat input submit helper, got app.js body: %s", jsBody)
	}
	if !strings.Contains(jsBody, "shouldInsertChatNewline") || !strings.Contains(jsBody, "insertChatInputNewline") {
		t.Fatalf("expected explicit Ctrl+Enter newline helpers, got app.js body: %s", jsBody)
	}
	if !strings.Contains(jsBody, "sessionDetailHasActiveDescendants") || !strings.Contains(jsBody, "sessionViewState.needsRefresh") {
		t.Fatalf("expected current session polling to track active descendants and coalesced refreshes, got app.js body: %s", jsBody)
	}
	if !strings.Contains(jsBody, "queueJobViewState") || !strings.Contains(jsBody, "setSelectedQueueJob") || !strings.Contains(sessionBody, "selectedQueueJobId()") {
		t.Fatalf("expected selected queue job inspector state to stay out of durable app state, got app.js body: %s", jsBody)
	}
	if !strings.Contains(jsBody, "overviewErrorMessage") || !strings.Contains(jsBody, "setOverviewError(message)") || !strings.Contains(jsBody, "showToast(message, 'error')") || strings.Contains(jsBody, "state.overviewError") {
		t.Fatalf("expected initial overview failures to surface backend API errors, got app.js body: %s", jsBody)
	}
	if !strings.Contains(jsBody, "async function refreshCurrentSession(options = {})") || !strings.Contains(jsBody, "if (options.surfaceError)") || !strings.Contains(jsBody, "showSessionLoadError(err, { toast: options.toastError !== false })") || !strings.Contains(jsBody, "refreshCurrentSession({ surfaceError: true })") {
		t.Fatalf("expected direct session opens to surface backend detail load errors while keeping polling quiet, got app.js body: %s", jsBody)
	}
	if !strings.Contains(jsBody, "function earlierMessagesErrorMessage") || !strings.Contains(jsBody, "const message = earlierMessagesErrorMessage(err)") || !strings.Contains(jsBody, "Failed to load earlier messages.") || !strings.Contains(jsBody, "showToast(message, 'error')") {
		t.Fatalf("expected load-earlier message failures to surface backend API errors, got app.js body: %s", jsBody)
	}
	if !strings.Contains(jsBody, "isLaunchInFlight()") || !strings.Contains(jsBody, "setLaunchInFlight(true)") || !strings.Contains(jsBody, "Session launch is already in progress") || !strings.Contains(jsBody, "launchPendingWithoutSession") {
		t.Fatalf("expected initial session launch to have a pending-submit guard, got app.js body: %s", jsBody)
	}
	if !strings.Contains(jsBody, "runViewState") || !strings.Contains(jsBody, "isGenerating()") || !strings.Contains(jsBody, "setGeneratingViewState") || strings.Contains(jsBody, "state.isGenerating") {
		t.Fatalf("expected current-run generating view state to stay out of durable app state, got app.js body: %s", jsBody)
	}
	if !strings.Contains(jsBody, "messagePagingViewState") || !strings.Contains(jsBody, "isLoadingEarlierMessages()") || !strings.Contains(jsBody, "preserveScrollAfterRenderHeight()") || strings.Contains(jsBody, "state.loadingEarlier") || strings.Contains(jsBody, "state.preserveScrollAfterRender") {
		t.Fatalf("expected message paging in-flight view state to stay out of durable app state, got app.js body: %s", jsBody)
	}
	if !strings.Contains(jsBody, "currentHistoryPage()") || !strings.Contains(jsBody, "currentHistoryData()") || !strings.Contains(jsBody, "currentHistoryPageSize()") || strings.Contains(jsBody, "state.historyData") || strings.Contains(jsBody, "state.historyPage") || strings.Contains(jsBody, "state.historyPageSize") {
		t.Fatalf("expected history pagination display state to stay out of durable app state, got app.js body: %s", jsBody)
	}
	if !strings.Contains(jsBody, "STOP_FALLBACK_STEER_MESSAGE") || !strings.Contains(jsBody, "requestStopViaBestAvailablePath") || !strings.Contains(jsBody, "data-stop-session-id") {
		t.Fatalf("expected inline session stop handling with interrupt-steer fallback, got app.js body: %s", jsBody)
	}
	if !strings.Contains(sessionBody, "renderMessageText") || !strings.Contains(sessionBody, "message-bubble-plaintext") {
		t.Fatalf("expected explicit plaintext user-message renderer, got session-view.js body: %s", sessionBody)
	}
	if !strings.Contains(sessionBody, "renderBackgroundResultsMessage") || !strings.Contains(sessionBody, "messageSource") || !strings.Contains(sessionBody, "Background agents") {
		t.Fatalf("expected background agent results to have a dedicated renderer, got session-view.js body: %s", sessionBody)
	}
	if !strings.Contains(sessionBody, "buildDisplayMessageStream") || !strings.Contains(sessionBody, "partitionMatchingToolResults") || !strings.Contains(sessionBody, "primaryFinalFinishResult") || !strings.Contains(sessionBody, "Final response captured") {
		t.Fatalf("expected session renderer to merge matching tool call/results without duplicating final output, got session-view.js body: %s", sessionBody)
	}
	if !strings.Contains(sessionBody, "Click to open queue job") || !strings.Contains(sessionBody, "data-open-job") {
		t.Fatalf("expected orphan background jobs to open queue jobs instead of child sessions, got session-view.js body: %s", sessionBody)
	}
	if !strings.Contains(sessionBody, "currentOverviewError()") || !strings.Contains(sessionBody, "No durable sessions yet.") {
		t.Fatalf("expected session rail to distinguish overview load errors from an empty session list, got session-view.js body: %s", sessionBody)
	}
	if !strings.Contains(sessionBody, "collectShellRedirectPaths(parsed?.command)") {
		t.Fatalf("expected files panel to include shell-created workspace files, got session-view.js body: %s", sessionBody)
	}
	if !strings.Contains(sessionBody, "Approval override requires explicit confirmation") {
		t.Fatalf("expected goal facts to explain explicit coverage override, got session-view.js body: %s", sessionBody)
	}
	if strings.Contains(jsBody, "Ctrl+Enter', 'Submit message") || strings.Contains(jsBody, "'ctrl+enter': 'submit'") {
		t.Fatalf("expected Ctrl+Enter submit shortcut to be removed, got app.js body: %s", jsBody)
	}
	if strings.Contains(jsBody, "toggleTheme") || strings.Contains(jsBody, "prefers-color-scheme") || strings.Contains(jsBody, "data-theme") {
		t.Fatalf("expected dark mode script to be removed, got app.js body: %s", jsBody)
	}
	if strings.Contains(jsBody, "k downloads") || strings.Contains(jsBody, "skill.downloads") {
		t.Fatalf("expected skills download badge to be removed, got app.js body: %s", jsBody)
	}

	cssBody := checkBody(server.URL + "/styles.css")
	if !strings.Contains(cssBody, "--accent") || !strings.Contains(cssBody, ".sidebar") || !strings.Contains(cssBody, ".chat-shell") || !strings.Contains(cssBody, ".pending-stage-card") || !strings.Contains(cssBody, ".toast-rack") || !strings.Contains(cssBody, "Noto Sans SC") || !strings.Contains(cssBody, ".session-rail") || !strings.Contains(cssBody, ".selected-queue-job-panel") {
		t.Fatalf("unexpected styles.css body: %s", cssBody)
	}
	if strings.Contains(cssBody, ".queue-primer") || strings.Contains(cssBody, ".queue-submit-panel") || strings.Contains(cssBody, "#queue-view") {
		t.Fatalf("expected standalone Background Jobs styles to be removed, got styles.css body: %s", cssBody)
	}
	if strings.Contains(cssBody, "[data-theme=\"dark\"]") || strings.Contains(cssBody, ".theme-toggle") {
		t.Fatalf("expected dark mode styles to be removed, got styles.css body: %s", cssBody)
	}
	if !strings.Contains(cssBody, ".message-bubble-plaintext") {
		t.Fatalf("expected plaintext user-message styles, got styles.css body: %s", cssBody)
	}
	if !strings.Contains(cssBody, ".agent-result-panel") || !strings.Contains(cssBody, ".message.background-results") {
		t.Fatalf("expected dedicated background agent result styles, got styles.css body: %s", cssBody)
	}
	if !strings.Contains(cssBody, "#chat-view .chat-container") || !strings.Contains(cssBody, "padding: 24px 0 24px") || !strings.Contains(cssBody, "overflow-y: auto") || !strings.Contains(cssBody, "scrollbar-gutter: stable both-edges") {
		t.Fatalf("expected session chat container to keep a visible own scroll area, got styles.css body: %s", cssBody)
	}
	if !strings.Contains(cssBody, "#chat-view .input-area") || !strings.Contains(cssBody, "position: relative") || !strings.Contains(cssBody, "flex: 0 0 auto") {
		t.Fatalf("expected session input area to participate in the chat flex layout, got styles.css body: %s", cssBody)
	}
	for _, selector := range []string{"#skills-view.view", "#workspace-view.view", "#history-view.view", "#settings-view.view"} {
		if !strings.Contains(cssBody, selector) {
			t.Fatalf("expected %s to remain scrollable, got styles.css body: %s", selector, cssBody)
		}
	}
	if !strings.Contains(cssBody, "overflow-y: auto") {
		t.Fatalf("expected non-session views to remain scrollable, got styles.css body: %s", cssBody)
	}
}

func TestServiceWebSocketRejectsChatControl(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]any{
		"type":      "chat",
		"sessionId": "0xWSCHAT",
		"message":   "hello",
	}); err != nil {
		t.Fatalf("write websocket chat: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	var msg map[string]any
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("read websocket error: %v", err)
	}
	if msg["type"] != "error" {
		t.Fatalf("expected error message, got %#v", msg)
	}
	payload, _ := msg["payload"].(map[string]any)
	if payload["code"] != "WEBSOCKET_CONTROL_DEPRECATED" {
		t.Fatalf("expected deprecated control code, got %#v", msg)
	}

	items, err := svc.store.List(10)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("websocket chat must not create sessions, got %#v", items)
	}
}

func TestServiceWebSocketRejectsForeignOrigin(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	headers := http.Header{}
	headers.Set("Origin", "https://evil.example")
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("expected websocket foreign origin to be rejected")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("expected forbidden websocket upgrade, got status=%d err=%v", status, err)
	}
}

func TestServiceWebSocketRejectsCrossSchemeOrigin(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	host := strings.TrimPrefix(ts.URL, "http://")
	wsURL := "ws://" + host + "/ws"
	headers := http.Header{}
	headers.Set("Origin", "https://"+host)
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("expected websocket cross-scheme origin to be rejected")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("expected forbidden websocket upgrade, got status=%d err=%v", status, err)
	}
}

func TestServiceSessionRouteRejectsEncodedSeparatorInSessionID(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "encoded_separator_session",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	if err := svc.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "awaiting_input", UpdatedAt: now}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/sessions/"+meta.ID+"%2Fmessages", nil)
	svc.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected encoded separator in session id to be rejected, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "path separators and traversal") {
		t.Fatalf("expected malformed session id error, got body=%s", recorder.Body.String())
	}
}

func TestServiceSessionRouteRejectsEncodedSeparatorWhenPrefixIsEncoded(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "encoded_prefix_session",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	if err := svc.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "awaiting_input", UpdatedAt: now}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := svc.store.AppendMessage(meta.ID, session.NewMessage("user", "hidden message")); err != nil {
		t.Fatalf("append message: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/%61pi/sessions/"+meta.ID+"%2Fmessages", nil)
	svc.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected encoded separator in session id to be rejected, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "hidden message") {
		t.Fatalf("encoded-prefix route returned session messages: body=%s", recorder.Body.String())
	}
}

func TestServiceSessionDetailReconcilesLinkedQueueJob(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	childMeta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_detail_reconcile",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyAutonomous,
		ParentSessionID:  "parent_detail_reconcile",
		RootSessionID:    "parent_detail_reconcile",
		AgentName:        "detail-child",
		AgentRole:        "evaluator",
		QueueJobID:       "job_detail_reconcile",
		Depth:            1,
	}
	if err := svc.store.Create(childMeta, session.State{
		Status:    session.StatusRunning,
		Phase:     "provider_call",
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := svc.store.SaveJob(session.QueueJob{
		SchemaVersion:   1,
		ID:              childMeta.QueueJobID,
		CreatedAt:       now,
		Status:          session.QueueStatusFailed,
		ParentSessionID: childMeta.ParentSessionID,
		RootSessionID:   childMeta.RootSessionID,
		AgentName:       childMeta.AgentName,
		AgentRole:       childMeta.AgentRole,
		Prompt:          "fail",
		Mode:            session.ModeExec,
		Background:      true,
		LastError:       "queue failure",
	}); err != nil {
		t.Fatalf("save failed job: %v", err)
	}

	detail, err := svc.sessionDetail(childMeta.ID, 100)
	if err != nil {
		t.Fatalf("session detail: %v", err)
	}
	if detail.State.Status != session.StatusFailed || detail.State.LastError != "queue failure" {
		t.Fatalf("expected detail to reconcile linked failed queue job, got %#v", detail.State)
	}
}

func TestServiceSessionDetailAllowsMissingMetadataOnlyQueueJob(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	meta := testSessionMetadata(t, "session_detail_missing_queue_job")
	meta.QueueJobID = "job_missing_metadata_only_detail"
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}

	ts := httptest.NewServer(svc)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions/" + meta.ID)
	if err != nil {
		t.Fatalf("get session detail: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected metadata-only missing queue job to keep detail readable, got %d body=%s", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), meta.QueueJobID) {
		t.Fatalf("expected detail to preserve metadata queue job id, got body=%s", string(body))
	}
}

func TestServiceSessionDetailReportsLinkedQueueReconcileError(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	childMeta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_detail_reconcile_error",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyAutonomous,
		ParentSessionID:  "parent_detail_reconcile_error",
		RootSessionID:    "parent_detail_reconcile_error",
		QueueJobID:       "job_detail_reconcile_error",
		Depth:            1,
	}
	if err := svc.store.Create(childMeta, session.State{
		Status:    session.StatusRunning,
		Phase:     "provider_call",
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := svc.store.SaveJob(session.QueueJob{
		SchemaVersion:   1,
		ID:              childMeta.QueueJobID,
		CreatedAt:       now,
		Status:          session.QueueStatusFailed,
		ParentSessionID: childMeta.ParentSessionID,
		RootSessionID:   childMeta.RootSessionID,
		Prompt:          "fail",
		Mode:            session.ModeExec,
		Background:      true,
		LastError:       "queue failure",
	}); err != nil {
		t.Fatalf("save failed job: %v", err)
	}
	stateLockPath := filepath.Join(svc.store.SessionDir(childMeta.ID), "state.lock")
	if err := os.Mkdir(stateLockPath, 0o700); err != nil {
		t.Fatalf("block state lock: %v", err)
	}

	ts := httptest.NewServer(svc)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions/" + childMeta.ID)
	if err != nil {
		t.Fatalf("get session detail: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected linked queue reconcile error to return 500, got %d body=%s", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), "persist linked session state") {
		t.Fatalf("expected linked queue reconcile error in response, got body=%s", string(body))
	}
}

func TestServiceSessionDetailReportsProviderAttemptsLoadError(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	meta := testSessionMetadata(t, "session_detail_provider_attempts_error")
	if err := svc.store.Create(meta, testSessionState(session.StatusCompleted)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	attemptsPath := filepath.Join(svc.store.SessionDir(meta.ID), "provider-attempts.jsonl")
	if err := os.WriteFile(attemptsPath, []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatalf("write invalid provider attempts: %v", err)
	}

	ts := httptest.NewServer(svc)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions/" + meta.ID)
	if err != nil {
		t.Fatalf("get session detail: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected provider attempts load error to return 500, got %d body=%s", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), "provider-attempts.jsonl") {
		t.Fatalf("expected provider attempts path in response, got body=%s", string(body))
	}
}

func TestServiceQueueWorkersProcessJob(t *testing.T) {
	server := newFinishServer()
	defer server.Close()

	cfg := testConfig(t, server.URL)
	svc, err := New(cfg, Options{WorkerCount: 1})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	jobWorkdir := t.TempDir()
	var job session.QueueJob
	postJSON(t, ts.URL+"/api/queue/jobs", map[string]any{
		"prompt":         "Summarize the working directory and call finish.",
		"workdir":        jobWorkdir,
		"isolation_mode": "off",
	}, http.StatusAccepted, &job)
	if job.ID == "" {
		t.Fatal("expected queue job id")
	}

	var loaded session.QueueJob
	waitFor(t, 5*time.Second, func() bool {
		current, err := svc.store.LoadJob(job.ID)
		if err == nil {
			loaded = current
		}
		return err == nil && current.Status == session.QueueStatusCompleted && current.SessionID != ""
	}, func() string {
		current, err := svc.store.LoadJob(job.ID)
		if err != nil {
			return err.Error()
		}
		payload := map[string]any{"job": current}
		if current.SessionID != "" {
			if state, stateErr := svc.store.LoadState(current.SessionID); stateErr == nil {
				payload["state"] = state
			}
			if messages, msgErr := svc.store.LoadMessages(current.SessionID); msgErr == nil {
				payload["messages"] = messages
			}
			if eventsList, evtErr := svc.store.LoadEvents(current.SessionID); evtErr == nil {
				payload["events"] = eventsList
			}
		}
		data, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return marshalErr.Error()
		}
		return string(data)
	})

	if loaded.SessionID == "" {
		t.Fatalf("expected child session id to be populated, job=%#v", loaded)
	}
}

func TestServiceQueueJobDetailRequiresGet(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	job := session.QueueJob{
		SchemaVersion: 1,
		ID:            "queue_method_guard",
		CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		Status:        session.QueueStatusQueued,
		Prompt:        "queued prompt",
		Mode:          session.ModeExec,
		Background:    true,
	}
	if err := svc.store.SaveJob(job); err != nil {
		t.Fatalf("save job: %v", err)
	}

	ts := httptest.NewServer(svc)
	defer ts.Close()

	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		req, err := http.NewRequest(method, ts.URL+"/api/queue/jobs/"+job.ID, strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("new %s job detail request: %v", method, err)
		}
		req.Header.Set(webMutationHeader, "1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s job detail request: %v", method, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("expected %s job detail to be 405, got %d body=%s", method, resp.StatusCode, string(body))
		}
		if strings.Contains(string(body), job.Prompt) {
			t.Fatalf("expected %s job detail not to return job JSON, got body=%s", method, string(body))
		}
	}

	var got session.QueueJob
	postGetJSON(t, ts.URL+"/api/queue/jobs/"+job.ID, &got)
	if got.ID != job.ID || got.Prompt != job.Prompt {
		t.Fatalf("unexpected GET job detail: %#v", got)
	}
}

func TestServiceQueueJobDetailRejectsMalformedJobID(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/queue/jobs/bad%2Fjob", nil)
	svc.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status: %d want %d body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "path separators and traversal") {
		t.Fatalf("expected malformed job id error, got body=%s", recorder.Body.String())
	}

	missing := httptest.NewRecorder()
	missingReq := httptest.NewRequest(http.MethodGet, "/api/queue/jobs/missing_job_detail", nil)
	svc.ServeHTTP(missing, missingReq)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unexpected missing-job status: %d want %d body=%s", missing.Code, http.StatusNotFound, missing.Body.String())
	}
}

func TestServiceParallelQueueWorkersPersistAllJobs(t *testing.T) {
	server := newDelayedFinishServer(150 * time.Millisecond)
	defer server.Close()

	cfg := testConfig(t, server.URL)
	svc, err := New(cfg, Options{WorkerCount: 2})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	parentWorkdir := t.TempDir()
	parentMeta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "parent_parallel_queue",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          parentWorkdir,
		RequestedWorkdir: parentWorkdir,
		Mode:             session.ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyAutonomous,
		RootSessionID:    "parent_parallel_queue",
	}
	parentState := session.State{
		Status:    session.StatusCompleted,
		Phase:     "turn_decide",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := svc.store.Create(parentMeta, parentState); err != nil {
		t.Fatalf("create parent session: %v", err)
	}

	var job1 session.QueueJob
	var job2 session.QueueJob
	postJSON(t, ts.URL+"/api/queue/jobs", map[string]any{
		"prompt":            "Finish child one.",
		"parent_session_id": parentMeta.ID,
		"workdir":           parentWorkdir,
		"isolation_mode":    "auto",
		"agent_name":        "child-one",
		"agent_role":        "generator",
	}, http.StatusAccepted, &job1)
	postJSON(t, ts.URL+"/api/queue/jobs", map[string]any{
		"prompt":            "Finish child two.",
		"parent_session_id": parentMeta.ID,
		"workdir":           parentWorkdir,
		"isolation_mode":    "auto",
		"agent_name":        "child-two",
		"agent_role":        "evaluator",
	}, http.StatusAccepted, &job2)

	waitFor(t, 10*time.Second, func() bool {
		current1, err1 := svc.store.LoadJob(job1.ID)
		current2, err2 := svc.store.LoadJob(job2.ID)
		if err1 != nil || err2 != nil {
			return false
		}
		return current1.Status == session.QueueStatusCompleted &&
			current2.Status == session.QueueStatusCompleted &&
			current1.SessionID != "" &&
			current2.SessionID != ""
	}, func() string {
		current1, err1 := svc.store.LoadJob(job1.ID)
		current2, err2 := svc.store.LoadJob(job2.ID)
		detail, detailErr := svc.sessionDetail(parentMeta.ID, 100)
		payload := map[string]any{
			"job1":       current1,
			"job1_err":   err1,
			"job2":       current2,
			"job2_err":   err2,
			"detail_err": detailErr,
			"detail":     detail,
			"workers":    svc.workers.Snapshot(),
		}
		data, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return marshalErr.Error()
		}
		return string(data)
	})

	detail, err := svc.sessionDetail(parentMeta.ID, 100)
	if err != nil {
		t.Fatalf("session detail: %v", err)
	}
	if len(detail.Children.Sessions) != 2 {
		t.Fatalf("expected 2 child sessions, got %d", len(detail.Children.Sessions))
	}
	if len(detail.Children.Jobs) != 2 {
		t.Fatalf("expected 2 child jobs, got %d", len(detail.Children.Jobs))
	}
	if len(detail.BackgroundNotifications) != 2 {
		t.Fatalf("expected 2 background notifications, got %d", len(detail.BackgroundNotifications))
	}

	for _, job := range detail.Children.Jobs {
		if job.Status != session.QueueStatusCompleted {
			t.Fatalf("expected completed child job, got %#v", job)
		}
		if job.SessionID == "" {
			t.Fatalf("expected child job session id, got %#v", job)
		}
		if job.EffectiveWorkdir == "" {
			t.Fatalf("expected effective workdir, got %#v", job)
		}
		if _, err := os.Stat(job.EffectiveWorkdir); err != nil {
			t.Fatalf("expected effective workdir to exist: %v", err)
		}
	}
}

func TestServiceWorkerScaling(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/workers", bytes.NewBufferString(`{"desired_count":2}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(webMutationHeader, "1")
	svc.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("unexpected status: %d body=%s", recorder.Code, recorder.Body.String())
	}
	var snapshot WorkerPoolSnapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snapshot.DesiredCount != 2 || snapshot.ActiveCount != 2 {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	if snapshot.MaxCount != maxWorkerCount {
		t.Fatalf("expected max worker count in snapshot, got %#v", snapshot)
	}
}

func TestServiceWorkerScalingRejectsExcessiveCount(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/workers", bytes.NewBufferString(`{"desired_count":999}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(webMutationHeader, "1")
	svc.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request for excessive worker count, got %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestServiceWorkerScalingRejectsMissingDesiredCount(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 1})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/workers", bytes.NewBufferString(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(webMutationHeader, "1")
	svc.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request for missing desired_count, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if snapshot := svc.workers.Snapshot(); snapshot.DesiredCount != 1 || snapshot.ActiveCount != 1 {
		t.Fatalf("missing desired_count should not scale workers, got %#v", snapshot)
	}
}

func TestServiceRejectsForeignOriginMutation(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/config", bytes.NewBufferString(`{"provider":"openai"}`))
	request.Header.Set("Origin", "http://evil.invalid")
	request.Header.Set("Content-Type", "text/plain")
	svc.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected forbidden cross-origin mutation, got %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestServiceRejectsSameOriginNonLocalHostMutation(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: filepath.Join(t.TempDir(), "config.yaml")})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://evil.invalid/api/config", bytes.NewBufferString(`{"provider":"openai","model":"should-not-save"}`))
	request.Header.Set("Origin", "http://evil.invalid")
	request.Header.Set("Content-Type", "application/json")
	svc.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected forbidden same-origin non-local-host mutation, got %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestServiceAllowsSameOriginLoopbackMutation(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/workers", bytes.NewBufferString(`{"desired_count":0}`))
	request.Header.Set("Origin", "http://127.0.0.1")
	request.Header.Set("Content-Type", "application/json")
	svc.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected accepted same-origin loopback mutation, got %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestServiceRejectsCrossSchemeOriginMutation(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: filepath.Join(t.TempDir(), "config.yaml")})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://go-cli.local:8080/api/config", bytes.NewBufferString(`{"provider":"openai"}`))
	request.Header.Set("Origin", "https://go-cli.local:8080")
	request.Header.Set("Content-Type", "application/json")
	svc.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected forbidden cross-scheme mutation, got %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestServiceRejectsJSONMutationSubtypeContentType(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/workers", bytes.NewBufferString(`{"desired_count":0}`))
	request.Header.Set(webMutationHeader, "1")
	request.Header.Set("Content-Type", "application/json-patch+json")
	svc.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected forbidden JSON subtype mutation, got %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestServiceRejectsOversizedJSONMutationBody(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	body := `{"prompt":"` + strings.Repeat("x", int(maxWebJSONBodyBytes)+1) + `"}`
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/sessions/start", strings.NewReader(body))
	request.Header.Set(webMutationHeader, "1")
	request.Header.Set("Content-Type", "application/json")
	svc.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request for oversized JSON mutation body, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "request body too large") {
		t.Fatalf("expected request body too large error, got body=%s", recorder.Body.String())
	}
}

func TestServiceOptionalJSONMutationsAllowUnknownLengthEmptyBodyWithoutContentType(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	planMeta := testSessionMetadata(t, "session_optional_empty_plan_approve")
	planMeta.Mode = session.ModeExec
	planMeta.CompletionPolicy = session.CompletionPolicyAutonomous
	if err := svc.store.Create(planMeta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create plan mode session: %v", err)
	}
	if _, err := svc.store.CreatePlanMode(planMeta.ID, session.PlanModeDraft{
		Enabled:   true,
		Objective: "Approve with empty unknown-length body",
		Source:    session.PlanModeSourceWeb,
	}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}

	missionMeta := testSessionMetadata(t, "session_optional_empty_mission_approve")
	if err := svc.store.Create(missionMeta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create mission session: %v", err)
	}
	if _, err := svc.store.CreateGoal(missionMeta.ID, session.GoalDraft{
		Enabled:   true,
		Objective: "Plain goal cannot approve mission",
		Source:    session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create plain goal: %v", err)
	}

	for _, tc := range []struct {
		name       string
		path       string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "plan mode approve",
			path:       "/api/sessions/" + planMeta.ID + "/planmode/approve",
			wantStatus: http.StatusConflict,
			wantBody:   "not awaiting approval",
		},
		{
			name:       "mission plan approve",
			path:       "/api/sessions/" + missionMeta.ID + "/mission/plan/approve",
			wantStatus: http.StatusBadRequest,
			wantBody:   "mission plan is required before approval",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, tc.path, nil)
			request.Body = io.NopCloser(strings.NewReader(""))
			request.ContentLength = -1
			request.Header.Set(webMutationHeader, "1")
			svc.ServeHTTP(recorder, request)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("unexpected status: %d want %d body=%s", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), tc.wantBody) {
				t.Fatalf("expected handler-level response containing %q, got body=%s", tc.wantBody, recorder.Body.String())
			}
		})
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/sessions/"+planMeta.ID+"/planmode/approve", nil)
	request.Body = io.NopCloser(strings.NewReader(`{"override_coverage":true}`))
	request.ContentLength = -1
	request.Header.Set(webMutationHeader, "1")
	svc.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected forbidden for non-empty optional JSON body without content type, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "Content-Type: application/json") {
		t.Fatalf("expected JSON content-type error, got body=%s", recorder.Body.String())
	}
}

func TestServiceBodylessMutationsDoNotRequireJSONContentType(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	ts := httptest.NewServer(svc)
	defer ts.Close()

	clearMeta := testSessionMetadata(t, "session_bodyless_goal_clear")
	if err := svc.store.Create(clearMeta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create clear session: %v", err)
	}
	if _, err := svc.store.CreateGoal(clearMeta.ID, session.GoalDraft{
		Enabled:   true,
		Objective: "Clear without body",
		Source:    session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create clear goal: %v", err)
	}
	respBody := requestWithoutJSONContentType(t, http.MethodDelete, ts.URL+"/api/sessions/"+clearMeta.ID+"/goal", http.StatusOK)
	if _, err := svc.store.LoadGoal(clearMeta.ID); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected goal to be cleared body=%s err=%v", respBody, err)
	}

	planMeta := testSessionMetadata(t, "session_bodyless_planmode_approve")
	planMeta.Mode = session.ModeExec
	planMeta.CompletionPolicy = session.CompletionPolicyAutonomous
	if err := svc.store.Create(planMeta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create plan mode session: %v", err)
	}
	if _, err := svc.store.CreatePlanMode(planMeta.ID, session.PlanModeDraft{
		Enabled:   true,
		Objective: "Approve without body",
		Source:    session.PlanModeSourceWeb,
	}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	planBody := requestWithoutJSONContentType(t, http.MethodPost, ts.URL+"/api/sessions/"+planMeta.ID+"/planmode/approve", http.StatusConflict)
	if !strings.Contains(planBody, "not awaiting approval") {
		t.Fatalf("expected handler-level plan mode conflict, got body=%s", planBody)
	}

	missionMeta := testSessionMetadata(t, "session_bodyless_mission_approve")
	if err := svc.store.Create(missionMeta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create mission session: %v", err)
	}
	if _, err := svc.store.CreateGoal(missionMeta.ID, session.GoalDraft{
		Enabled:   true,
		Objective: "Plain goal cannot approve mission",
		Source:    session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create plain goal: %v", err)
	}
	missionBody := requestWithoutJSONContentType(t, http.MethodPost, ts.URL+"/api/sessions/"+missionMeta.ID+"/mission/plan/approve", http.StatusBadRequest)
	if !strings.Contains(missionBody, "mission plan is required before approval") {
		t.Fatalf("expected handler-level mission approval error, got body=%s", missionBody)
	}
}

func TestServiceNoInputMutationRoutesRejectNonJSONBodies(t *testing.T) {
	type routeSetup struct {
		method string
		path   string
		verify func(*testing.T, *Service)
	}
	cases := []struct {
		name  string
		setup func(*testing.T, *Service) routeSetup
	}{
		{
			name: "clear sessions",
			setup: func(t *testing.T, svc *Service) routeSetup {
				meta := testSessionMetadata(t, "session_noinput_clear_sessions")
				if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
					t.Fatalf("create session: %v", err)
				}
				return routeSetup{
					method: http.MethodPost,
					path:   "/api/sessions/clear",
					verify: func(t *testing.T, svc *Service) {
						if _, err := svc.store.LoadMetadata(meta.ID); err != nil {
							t.Fatalf("expected malformed clear request to leave session metadata intact: %v", err)
						}
					},
				}
			},
		},
		{
			name: "delete session",
			setup: func(t *testing.T, svc *Service) routeSetup {
				meta := testSessionMetadata(t, "session_noinput_delete_session")
				if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
					t.Fatalf("create session: %v", err)
				}
				return routeSetup{
					method: http.MethodDelete,
					path:   "/api/sessions/" + meta.ID,
					verify: func(t *testing.T, svc *Service) {
						if _, err := svc.store.LoadMetadata(meta.ID); err != nil {
							t.Fatalf("expected malformed delete request to leave session metadata intact: %v", err)
						}
					},
				}
			},
		},
		{
			name: "clear goal",
			setup: func(t *testing.T, svc *Service) routeSetup {
				meta := testSessionMetadata(t, "session_noinput_clear_goal")
				if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
					t.Fatalf("create session: %v", err)
				}
				if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
					Enabled:   true,
					Objective: "Keep goal when malformed body is supplied",
					Source:    session.GoalSourceWeb,
				}); err != nil {
					t.Fatalf("create goal: %v", err)
				}
				return routeSetup{
					method: http.MethodDelete,
					path:   "/api/sessions/" + meta.ID + "/goal",
					verify: func(t *testing.T, svc *Service) {
						if _, err := svc.store.LoadGoal(meta.ID); err != nil {
							t.Fatalf("expected malformed goal clear request to leave goal intact: %v", err)
						}
					},
				}
			},
		},
		{
			name: "complete goal",
			setup: func(t *testing.T, svc *Service) routeSetup {
				meta := testSessionMetadata(t, "session_noinput_complete_goal")
				if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
					t.Fatalf("create session: %v", err)
				}
				if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
					Enabled:   true,
					Objective: "Keep active goal when malformed body is supplied",
					Source:    session.GoalSourceWeb,
				}); err != nil {
					t.Fatalf("create goal: %v", err)
				}
				return routeSetup{
					method: http.MethodPost,
					path:   "/api/sessions/" + meta.ID + "/goal/complete",
					verify: func(t *testing.T, svc *Service) {
						goal, err := svc.store.LoadGoal(meta.ID)
						if err != nil {
							t.Fatalf("load goal: %v", err)
						}
						if goal.Status == session.GoalStatusComplete {
							t.Fatalf("expected malformed goal complete request to leave goal status unchanged")
						}
					},
				}
			},
		},
		{
			name: "cancel plan mode",
			setup: func(t *testing.T, svc *Service) routeSetup {
				meta := testSessionMetadata(t, "session_noinput_cancel_planmode")
				meta.Mode = session.ModeExec
				meta.CompletionPolicy = session.CompletionPolicyAutonomous
				if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
					t.Fatalf("create session: %v", err)
				}
				if _, err := svc.store.CreatePlanMode(meta.ID, session.PlanModeDraft{
					Enabled:   true,
					Objective: "Keep plan mode when malformed body is supplied",
					Source:    session.PlanModeSourceWeb,
				}); err != nil {
					t.Fatalf("create plan mode: %v", err)
				}
				return routeSetup{
					method: http.MethodPost,
					path:   "/api/sessions/" + meta.ID + "/planmode/cancel",
					verify: func(t *testing.T, svc *Service) {
						planMode, err := svc.store.LoadPlanMode(meta.ID)
						if err != nil {
							t.Fatalf("load plan mode: %v", err)
						}
						if planMode.Status == session.PlanModeStatusCancelled {
							t.Fatalf("expected malformed plan mode cancel request to leave plan mode status unchanged")
						}
					},
				}
			},
		},
		{
			name: "interrupt",
			setup: func(t *testing.T, svc *Service) routeSetup {
				meta := testSessionMetadata(t, "session_noinput_interrupt")
				if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
					t.Fatalf("create session: %v", err)
				}
				return routeSetup{
					method: http.MethodPost,
					path:   "/api/sessions/" + meta.ID + "/interrupt",
				}
			},
		},
		{
			name: "stop",
			setup: func(t *testing.T, svc *Service) routeSetup {
				meta := testSessionMetadata(t, "session_noinput_stop")
				if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
					t.Fatalf("create session: %v", err)
				}
				return routeSetup{
					method: http.MethodPost,
					path:   "/api/sessions/" + meta.ID + "/stop",
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t, "")
			svc, err := New(cfg, Options{WorkerCount: 0})
			if err != nil {
				t.Fatalf("new service: %v", err)
			}
			defer svc.Close()

			route := tc.setup(t, svc)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(route.method, route.path, strings.NewReader("not-json"))
			request.Header.Set(webMutationHeader, "1")
			request.Header.Set("Content-Type", "text/plain")
			svc.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("expected forbidden for non-JSON body, got %d body=%s", recorder.Code, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), "Content-Type: application/json") {
				t.Fatalf("expected JSON Content-Type error, got body=%s", recorder.Body.String())
			}
			if route.verify != nil {
				route.verify(t, svc)
			}
		})
	}
}

func TestServiceOptionalEmptyJSONRoutesRejectNullBodies(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	meta := testSessionMetadata(t, "session_noinput_null_goal")
	if err := svc.store.Create(meta, testSessionState(session.StatusAwaitingInput)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Objective: "Keep this goal",
		Source:    session.GoalSourceWeb,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}

	var apiErr ErrorResponse
	requestJSONWithMethod(t, http.MethodDelete, ts.URL+"/api/sessions/"+meta.ID+"/goal", nil, http.StatusBadRequest, &apiErr)
	if !strings.Contains(apiErr.Error, "JSON object") {
		t.Fatalf("expected JSON object error, got %#v", apiErr)
	}
	if _, err := svc.store.LoadGoal(meta.ID); err != nil {
		t.Fatalf("null clear-goal body should leave goal intact: %v", err)
	}
}

func TestServiceInterruptUsesManualPauseReason(t *testing.T) {
	server := newSleepToolServer()
	defer server.Close()

	cfg := testConfig(t, server.URL)
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var result LaunchResponse
	postJSON(t, ts.URL+"/api/sessions/start", map[string]any{
		"prompt": "Long-running task that will be interrupted.",
		"mode":   "exec",
	}, http.StatusAccepted, &result)

	waitFor(t, 2*time.Second, func() bool {
		state, err := svc.store.LoadState(result.SessionID)
		return err == nil && state.Status == session.StatusRunning && state.Phase == "tool_execute" && svc.hasActiveHandle(result.SessionID)
	}, func() string {
		state, err := svc.store.LoadState(result.SessionID)
		if err != nil {
			return err.Error()
		}
		data, marshalErr := json.Marshal(state)
		if marshalErr != nil {
			return marshalErr.Error()
		}
		return string(data)
	})

	postJSON(t, ts.URL+"/api/sessions/"+result.SessionID+"/interrupt", map[string]any{}, http.StatusAccepted, nil)

	waitFor(t, 4*time.Second, func() bool {
		state, err := svc.store.LoadState(result.SessionID)
		return err == nil && state.Status == session.StatusPaused && state.PauseReason == "manual_interrupt"
	}, func() string {
		state, err := svc.store.LoadState(result.SessionID)
		if err != nil {
			return err.Error()
		}
		data, marshalErr := json.Marshal(state)
		if marshalErr != nil {
			return marshalErr.Error()
		}
		return string(data)
	})
}

func TestServiceEmptySlicesEncodeAsArrays(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	overviewRecorder := httptest.NewRecorder()
	svc.ServeHTTP(overviewRecorder, httptest.NewRequest(http.MethodGet, "/api/overview", nil))
	if overviewRecorder.Code != http.StatusOK {
		t.Fatalf("unexpected overview status: %d body=%s", overviewRecorder.Code, overviewRecorder.Body.String())
	}
	var overview map[string]any
	if err := json.Unmarshal(overviewRecorder.Body.Bytes(), &overview); err != nil {
		t.Fatalf("decode overview: %v", err)
	}
	if recentJobs, ok := overview["recent_jobs"].([]any); !ok || len(recentJobs) != 0 {
		t.Fatalf("expected recent_jobs to encode as empty array, got %#v", overview["recent_jobs"])
	}
	if recentFailures, ok := overview["recent_failures"].([]any); !ok || len(recentFailures) != 0 {
		t.Fatalf("expected recent_failures to encode as empty array, got %#v", overview["recent_failures"])
	}
	if feed, ok := overview["feed"].([]any); !ok || len(feed) != 0 {
		t.Fatalf("expected feed to encode as empty array, got %#v", overview["feed"])
	}

	workersRecorder := httptest.NewRecorder()
	svc.ServeHTTP(workersRecorder, httptest.NewRequest(http.MethodGet, "/api/workers", nil))
	if workersRecorder.Code != http.StatusOK {
		t.Fatalf("unexpected workers status: %d body=%s", workersRecorder.Code, workersRecorder.Body.String())
	}
	var workers map[string]any
	if err := json.Unmarshal(workersRecorder.Body.Bytes(), &workers); err != nil {
		t.Fatalf("decode workers: %v", err)
	}
	if items, ok := workers["workers"].([]any); !ok || len(items) != 0 {
		t.Fatalf("expected workers to encode as empty array, got %#v", workers["workers"])
	}

	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyAutonomous,
		RootSessionID:    "",
	}
	meta.RootSessionID = meta.ID
	state := session.State{
		Status:    session.StatusCompleted,
		Phase:     "turn_decide",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := svc.store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}

	detailRecorder := httptest.NewRecorder()
	svc.ServeHTTP(detailRecorder, httptest.NewRequest(http.MethodGet, "/api/sessions/"+meta.ID, nil))
	if detailRecorder.Code != http.StatusOK {
		t.Fatalf("unexpected detail status: %d body=%s", detailRecorder.Code, detailRecorder.Body.String())
	}
	var detail map[string]any
	if err := json.Unmarshal(detailRecorder.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	for _, key := range []string{"background_notifications", "steer_requests", "messages", "events", "timeline"} {
		items, ok := detail[key].([]any)
		if !ok || len(items) != 0 {
			t.Fatalf("expected %s to encode as empty array, got %#v", key, detail[key])
		}
	}
}

func TestServiceStopSessionPausesWithManualStopReason(t *testing.T) {
	server := newSleepToolServer()
	defer server.Close()

	cfg := testConfig(t, server.URL)
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var result LaunchResponse
	postJSON(t, ts.URL+"/api/sessions/start", map[string]any{
		"prompt": "Long-running task that will be stopped.",
		"mode":   "exec",
	}, http.StatusAccepted, &result)

	waitFor(t, 2*time.Second, func() bool {
		state, err := svc.store.LoadState(result.SessionID)
		return err == nil && state.Status == session.StatusRunning && state.Phase == "tool_execute" && svc.hasActiveHandle(result.SessionID)
	}, func() string {
		state, err := svc.store.LoadState(result.SessionID)
		if err != nil {
			return err.Error()
		}
		data, marshalErr := json.Marshal(state)
		if marshalErr != nil {
			return marshalErr.Error()
		}
		return string(data)
	})

	postJSON(t, ts.URL+"/api/sessions/"+result.SessionID+"/stop", map[string]any{}, http.StatusAccepted, nil)

	waitFor(t, 4*time.Second, func() bool {
		state, err := svc.store.LoadState(result.SessionID)
		return err == nil && state.Status == session.StatusPaused && state.PauseReason == "manual_stop"
	}, func() string {
		state, err := svc.store.LoadState(result.SessionID)
		if err != nil {
			return err.Error()
		}
		data, marshalErr := json.Marshal(state)
		if marshalErr != nil {
			return marshalErr.Error()
		}
		return string(data)
	})
}

func TestServiceHistoryPagination(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	for i := 0; i < 3; i++ {
		meta := session.SessionMetadata{
			SchemaVersion:    1,
			ID:               "history_page_" + strconv.Itoa(i),
			CreatedAt:        time.Now().UTC().Add(time.Duration(i) * time.Minute).Format(time.RFC3339Nano),
			Workdir:          t.TempDir(),
			RequestedWorkdir: t.TempDir(),
			Mode:             session.ModeExec,
			Provider:         "openai",
			Model:            "gpt-5.4",
			CompletionPolicy: session.CompletionPolicyAutonomous,
			RootSessionID:    "history_page_" + strconv.Itoa(i),
		}
		state := session.State{
			Status:    session.StatusCompleted,
			Phase:     "turn_decide",
			UpdatedAt: time.Now().UTC().Add(time.Duration(i) * time.Minute).Format(time.RFC3339Nano),
		}
		if err := svc.store.Create(meta, state); err != nil {
			t.Fatalf("create session %d: %v", i, err)
		}
	}

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/history?page=2&page_size=2", nil)
	svc.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode history response: %v", err)
	}
	if payload["total"].(float64) != 3 {
		t.Fatalf("expected total 3, got %#v", payload["total"])
	}
	items, ok := payload["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("expected one item on page 2, got %#v", payload["items"])
	}
	if payload["page"].(float64) != 2 {
		t.Fatalf("expected page 2, got %#v", payload["page"])
	}

	maxIntQuery := strconv.Itoa(int(^uint(0) >> 1))
	recorder = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/history?page=2&page_size="+maxIntQuery, nil)
	svc.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected oversized page_size status: %d body=%s", recorder.Code, recorder.Body.String())
	}
	payload = map[string]any{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode oversized page_size response: %v", err)
	}
	if payload["page_size"].(float64) != 10 {
		t.Fatalf("expected oversized page_size to fall back to 10, got %#v", payload["page_size"])
	}
	if payload["total_pages"].(float64) != 1 {
		t.Fatalf("expected total_pages to stay positive after oversized page_size fallback, got %#v", payload["total_pages"])
	}

	recorder = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/history?page="+maxIntQuery+"&page_size=2", nil)
	svc.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected oversized page status: %d body=%s", recorder.Code, recorder.Body.String())
	}
	payload = map[string]any{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode oversized page response: %v", err)
	}
	items, ok = payload["items"].([]any)
	if !ok || len(items) != 0 {
		t.Fatalf("expected oversized page offset to return an empty page, got %#v", payload["items"])
	}
}

func TestServiceListLimitsRejectOversizedQueryValues(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	parent := testSessionMetadata(t, "session_limit_parent")
	parent.CreatedAt = time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	parent.RootSessionID = parent.ID
	if err := svc.store.Create(parent, testSessionState(session.StatusCompleted)); err != nil {
		t.Fatalf("create parent session: %v", err)
	}
	for i := 0; i < 55; i++ {
		meta := testSessionMetadata(t, "session_limit_child_"+strconv.Itoa(i))
		meta.CreatedAt = time.Now().UTC().Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano)
		meta.ParentSessionID = parent.ID
		meta.RootSessionID = parent.ID
		meta.Depth = 1
		if err := svc.store.Create(meta, testSessionState(session.StatusCompleted)); err != nil {
			t.Fatalf("create child session %d: %v", i, err)
		}
		job := session.QueueJob{
			SchemaVersion:   1,
			ID:              "job_limit_" + strconv.Itoa(i),
			CreatedAt:       time.Now().UTC().Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
			UpdatedAt:       time.Now().UTC().Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
			Status:          session.QueueStatusCompleted,
			ParentSessionID: parent.ID,
			RootSessionID:   parent.ID,
			Prompt:          "queued prompt",
			Mode:            session.ModeExec,
			Background:      true,
		}
		if err := svc.store.SaveJob(job); err != nil {
			t.Fatalf("save queue job %d: %v", i, err)
		}
	}

	maxIntQuery := strconv.Itoa(int(^uint(0) >> 1))

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions?limit="+maxIntQuery, nil)
	svc.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected sessions status: %d body=%s", recorder.Code, recorder.Body.String())
	}
	var sessions []session.SessionSummary
	if err := json.Unmarshal(recorder.Body.Bytes(), &sessions); err != nil {
		t.Fatalf("decode sessions response: %v", err)
	}
	if len(sessions) != 50 {
		t.Fatalf("expected oversized session limit to fall back to 50, got %d", len(sessions))
	}

	recorder = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/sessions/"+parent.ID+"/children?limit="+maxIntQuery, nil)
	svc.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected children status: %d body=%s", recorder.Code, recorder.Body.String())
	}
	var children ChildrenResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &children); err != nil {
		t.Fatalf("decode children response: %v", err)
	}
	if len(children.Sessions) != 50 || len(children.Jobs) != 50 {
		t.Fatalf("expected oversized children limit to fall back to 50 sessions/jobs, got %d/%d", len(children.Sessions), len(children.Jobs))
	}

	recorder = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/queue/jobs?limit="+maxIntQuery, nil)
	svc.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected jobs status: %d body=%s", recorder.Code, recorder.Body.String())
	}
	var jobs []session.QueueJob
	if err := json.Unmarshal(recorder.Body.Bytes(), &jobs); err != nil {
		t.Fatalf("decode queue jobs response: %v", err)
	}
	if len(jobs) != 50 {
		t.Fatalf("expected oversized queue job limit to fall back to 50, got %d", len(jobs))
	}
}

func TestServiceSessionListReportsSummarySnapshotLoadErrors(t *testing.T) {
	cases := []struct {
		name string
		file string
	}{
		{name: "goal", file: "goal.json"},
		{name: "planmode", file: "planmode.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t, "")
			svc, err := New(cfg, Options{WorkerCount: 0})
			if err != nil {
				t.Fatalf("new service: %v", err)
			}
			defer svc.Close()

			meta := testSessionMetadata(t, "summary_snapshot_error_"+tc.name)
			if err := svc.store.Create(meta, testSessionState(session.StatusCompleted)); err != nil {
				t.Fatalf("create session: %v", err)
			}
			if err := os.WriteFile(filepath.Join(svc.store.SessionDir(meta.ID), tc.file), []byte("{not-json}\n"), 0o600); err != nil {
				t.Fatalf("write invalid %s: %v", tc.file, err)
			}

			for _, path := range []string{"/api/sessions", "/api/history"} {
				recorder := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, path, nil)
				svc.ServeHTTP(recorder, req)
				if recorder.Code != http.StatusInternalServerError {
					t.Fatalf("expected %s load error from %s, got %d body=%s", tc.file, path, recorder.Code, recorder.Body.String())
				}
				if !strings.Contains(recorder.Body.String(), tc.file) {
					t.Fatalf("expected %s in %s response, got body=%s", tc.file, path, recorder.Body.String())
				}
			}
		})
	}
}

func TestServiceTaskBoardReportsCorruptTaskFile(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	meta := testSessionMetadata(t, "corrupt_task_board")
	if err := svc.store.Create(meta, testSessionState(session.StatusCompleted)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := session.CreateTask(svc.store, meta.ID, session.TaskCreateInput{Subject: "existing"}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	taskPath := filepath.Join(svc.store.SessionDir(meta.ID), "tasks", "task_0002.json")
	if err := os.WriteFile(taskPath, []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatalf("write corrupt task: %v", err)
	}

	for _, path := range []string{"/api/sessions/" + meta.ID, "/api/sessions/" + meta.ID + "/tasks"} {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		svc.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("expected corrupt task load error from %s, got %d body=%s", path, recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "tasks/task_0002.json") {
			t.Fatalf("expected task filename in %s response, got body=%s", path, recorder.Body.String())
		}
	}
}

func TestServiceDeleteSessionRouteRemovesSessionTreeAndJobs(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	parentMeta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "parent_session",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		RootSessionID:    "parent_session",
	}
	parentState := session.State{
		Status:    session.StatusCompleted,
		Phase:     "turn_decide",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := svc.store.Create(parentMeta, parentState); err != nil {
		t.Fatalf("create parent session: %v", err)
	}

	childMeta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_session",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyAutonomous,
		ParentSessionID:  parentMeta.ID,
		RootSessionID:    parentMeta.ID,
		QueueJobID:       "job_history_delete",
		Depth:            1,
	}
	childState := session.State{
		Status:    session.StatusCompleted,
		Phase:     "turn_decide",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := svc.store.Create(childMeta, childState); err != nil {
		t.Fatalf("create child session: %v", err)
	}
	if err := svc.store.SaveJob(session.QueueJob{
		SchemaVersion:   1,
		ID:              "job_history_delete",
		Status:          session.QueueStatusCompleted,
		ParentSessionID: parentMeta.ID,
		RootSessionID:   parentMeta.ID,
		SessionID:       childMeta.ID,
		AgentName:       "history-reviewer",
		AgentRole:       "evaluator",
		Prompt:          "done",
		Mode:            "exec",
	}); err != nil {
		t.Fatalf("save queue job: %v", err)
	}

	req, err := http.NewRequest(http.MethodDelete, "/api/sessions/"+parentMeta.ID, nil)
	if err != nil {
		t.Fatalf("new delete request: %v", err)
	}
	req.Header.Set(webMutationHeader, "1")
	recorder := httptest.NewRecorder()
	svc.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected delete status: %d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, err := svc.store.LoadMetadata(parentMeta.ID); !os.IsNotExist(err) {
		t.Fatalf("expected parent session to be deleted, got err=%v", err)
	}
	if _, err := svc.store.LoadMetadata(childMeta.ID); !os.IsNotExist(err) {
		t.Fatalf("expected child session to be deleted, got err=%v", err)
	}
	if _, err := svc.store.LoadJob("job_history_delete"); !os.IsNotExist(err) {
		t.Fatalf("expected queue job to be deleted, got err=%v", err)
	}
}

func TestServiceClearSessionsRouteRemovesHistory(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "clear_history_session",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyAutonomous,
		RootSessionID:    "clear_history_session",
	}
	state := session.State{
		Status:    session.StatusCompleted,
		Phase:     "turn_decide",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := svc.store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := svc.store.SaveJob(session.QueueJob{
		SchemaVersion:   1,
		ID:              "job_history_clear",
		Status:          session.QueueStatusCompleted,
		ParentSessionID: meta.ID,
		RootSessionID:   meta.ID,
		AgentName:       "history-reviewer",
		AgentRole:       "evaluator",
		Prompt:          "done",
		Mode:            "exec",
	}); err != nil {
		t.Fatalf("save queue job: %v", err)
	}

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/clear", nil)
	req.Header.Set(webMutationHeader, "1")
	svc.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected clear status: %d body=%s", recorder.Code, recorder.Body.String())
	}

	items, err := svc.store.List(10)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected no sessions after clear, got %#v", items)
	}
	jobs, err := svc.store.ListJobs(10)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected no jobs after clear, got %#v", jobs)
	}
}

func TestDeleteSessionRollsBackWhenAuditAppendFails(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	parentMeta := testSessionMetadata(t, "delete_audit_failure_parent")
	parentMeta.RootSessionID = parentMeta.ID
	if err := svc.store.Create(parentMeta, testSessionState(session.StatusCompleted)); err != nil {
		t.Fatalf("create parent session: %v", err)
	}
	childMeta := testSessionMetadata(t, "delete_audit_failure_child")
	childMeta.ParentSessionID = parentMeta.ID
	childMeta.RootSessionID = parentMeta.ID
	childMeta.QueueJobID = "delete_audit_failure_job"
	childMeta.Depth = 1
	if err := svc.store.Create(childMeta, testSessionState(session.StatusCompleted)); err != nil {
		t.Fatalf("create child session: %v", err)
	}
	if err := svc.store.SaveJob(session.QueueJob{
		SchemaVersion:   1,
		ID:              childMeta.QueueJobID,
		Status:          session.QueueStatusCompleted,
		ParentSessionID: parentMeta.ID,
		RootSessionID:   parentMeta.ID,
		SessionID:       childMeta.ID,
		Prompt:          "done",
		Mode:            "exec",
	}); err != nil {
		t.Fatalf("save queue job: %v", err)
	}
	svc.beforeAppendAuditEvent = func(eventType string, data map[string]any) error {
		if eventType == "web.session.delete" {
			return errors.New("blocked session delete audit append")
		}
		return nil
	}

	req, err := http.NewRequest(http.MethodDelete, "/api/sessions/"+parentMeta.ID, nil)
	if err != nil {
		t.Fatalf("new delete request: %v", err)
	}
	req.Header.Set(webMutationHeader, "1")
	recorder := httptest.NewRecorder()
	svc.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), "blocked session delete audit append") {
		t.Fatalf("expected blocked delete audit append response, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, err := svc.store.LoadMetadata(parentMeta.ID); err != nil {
		t.Fatalf("failed session delete audit append should restore parent session: %v", err)
	}
	if _, err := svc.store.LoadMetadata(childMeta.ID); err != nil {
		t.Fatalf("failed session delete audit append should restore child session: %v", err)
	}
	if _, err := svc.store.LoadJob(childMeta.QueueJobID); err != nil {
		t.Fatalf("failed session delete audit append should restore queue job: %v", err)
	}
	auditPath := webAuditLogPath(cfg.Session.Dir)
	if data, err := os.ReadFile(auditPath); err == nil && strings.Contains(string(data), "web.session.delete") {
		t.Fatalf("failed session delete audit append should not append delete event, got %q", string(data))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read audit log: %v", err)
	}
}

func TestHistoryMutationRejectsSymlinkedSessionRootDuringMove(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "sessions")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("mkdir session root: %v", err)
	}
	outsideRoot := filepath.Join(t.TempDir(), "outside-sessions")
	if err := os.Mkdir(outsideRoot, 0o700); err != nil {
		t.Fatalf("mkdir outside root: %v", err)
	}
	outsideSession := filepath.Join(outsideRoot, "session_outside")
	if err := os.Mkdir(outsideSession, 0o700); err != nil {
		t.Fatalf("mkdir outside session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outsideSession, "session.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write outside session: %v", err)
	}

	tx, err := newWebHistoryMutationTransaction(root, "delete")
	if err != nil {
		t.Fatalf("new history transaction: %v", err)
	}
	defer tx.Rollback()

	if err := os.Remove(root); err != nil {
		t.Fatalf("remove empty session root: %v", err)
	}
	if err := os.Symlink(outsideRoot, root); err != nil {
		t.Fatalf("symlink session root: %v", err)
	}

	err = tx.MovePath(filepath.Join(root, "session_outside"))
	if err == nil || !strings.Contains(err.Error(), "symlinked") {
		t.Fatalf("expected symlinked session root rejection, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outsideSession, "session.json")); statErr != nil {
		t.Fatalf("outside session should not be moved through symlinked root: %v", statErr)
	}
}

func TestHistoryMutationRejectsSymlinkedBackupParentBeforeCreate(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "managed")
	root := filepath.Join(parent, "sessions")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir session root: %v", err)
	}
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	symlinkProbe := filepath.Join(base, "symlink-probe")
	if err := os.Symlink(outside, symlinkProbe); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.Remove(symlinkProbe); err != nil {
		t.Fatalf("remove symlink probe: %v", err)
	}

	beforeWebHistoryBackupRootCreate = func(backupParent string) error {
		if backupParent != parent {
			return fmt.Errorf("unexpected backup parent: %s", backupParent)
		}
		if err := os.Rename(parent, parent+".real"); err != nil {
			return err
		}
		return os.Symlink(outside, parent)
	}
	defer func() {
		beforeWebHistoryBackupRootCreate = nil
	}()

	tx, err := newWebHistoryMutationTransaction(root, "delete")
	if err == nil {
		defer tx.Rollback()
		t.Fatalf("expected symlinked history backup parent rejection, got backup root %s", tx.backupRoot)
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink path error, got %v", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatalf("read outside dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".sessions.delete-") {
			t.Fatalf("history backup root must not be created through symlinked parent, found %s", entry.Name())
		}
	}
	if _, err := os.Stat(parent + ".real"); err != nil {
		t.Fatalf("real history parent should remain available, got %v", err)
	}
}

func TestClearSessionsRollsBackWhenAuditAppendFails(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	meta := testSessionMetadata(t, "clear_audit_failure_session")
	meta.RootSessionID = meta.ID
	if err := svc.store.Create(meta, testSessionState(session.StatusCompleted)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := svc.store.SaveJob(session.QueueJob{
		SchemaVersion:   1,
		ID:              "clear_audit_failure_job",
		Status:          session.QueueStatusCompleted,
		ParentSessionID: meta.ID,
		RootSessionID:   meta.ID,
		SessionID:       meta.ID,
		Prompt:          "done",
		Mode:            "exec",
	}); err != nil {
		t.Fatalf("save queue job: %v", err)
	}
	svc.beforeAppendAuditEvent = func(eventType string, data map[string]any) error {
		if eventType == "web.sessions.clear" {
			return errors.New("blocked sessions clear audit append")
		}
		return nil
	}

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/clear", nil)
	req.Header.Set(webMutationHeader, "1")
	svc.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), "blocked sessions clear audit append") {
		t.Fatalf("expected blocked clear audit append response, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, err := svc.store.LoadMetadata(meta.ID); err != nil {
		t.Fatalf("failed session clear audit append should restore session: %v", err)
	}
	if _, err := svc.store.LoadJob("clear_audit_failure_job"); err != nil {
		t.Fatalf("failed session clear audit append should restore queue job: %v", err)
	}
	auditPath := webAuditLogPath(cfg.Session.Dir)
	if data, err := os.ReadFile(auditPath); err == nil && strings.Contains(string(data), "web.sessions.clear") {
		t.Fatalf("failed session clear audit append should not append clear event, got %q", string(data))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read audit log: %v", err)
	}
}

func TestServiceClearSessionsIgnoresStaleHandles(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "stale_handle_session",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyAutonomous,
		RootSessionID:    "stale_handle_session",
	}
	state := session.State{
		Status:    session.StatusCompleted,
		Phase:     "turn_decide",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := svc.store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}

	svc.handles[meta.ID] = &launchHandle{
		sessionID: meta.ID,
		cancel:    func() {},
	}

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/clear", nil)
	req.Header.Set(webMutationHeader, "1")
	svc.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected clear status with stale handle: %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestServicePruneInactiveHandlesKeepsUnreadableStateHandle(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "unreadable_state_active_handle",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		RootSessionID:    "unreadable_state_active_handle",
	}
	if err := svc.store.Create(meta, session.State{Status: session.StatusRunning, Phase: "provider_call", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	var cancelled bool
	handle := &launchHandle{
		sessionID:      meta.ID,
		cancel:         func() { cancelled = true },
		startedAt:      time.Now().UTC().Format(time.RFC3339Nano),
		processStartID: "test-process",
		pid:            os.Getpid(),
	}
	if err := svc.addHandle(handle); err != nil {
		t.Fatalf("add handle: %v", err)
	}
	statePath := filepath.Join(svc.store.SessionDir(meta.ID), "state.json")
	if err := os.WriteFile(statePath, []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatalf("corrupt state: %v", err)
	}

	if !svc.hasAnyActiveHandle() {
		t.Fatal("unreadable state must not prune the current-process active handle")
	}
	current, ok := svc.handleForSession(meta.ID)
	if !ok || current != handle {
		t.Fatalf("expected original handle to remain, got handle=%#v ok=%v", current, ok)
	}

	svc.Close()
	if !cancelled {
		t.Fatal("expected Close to cancel handle retained across unreadable state")
	}
}

func TestServiceClearSessionsRejectsRunningSessionsWithoutLiveOwners(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "stale_running_session",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		RootSessionID:    "stale_running_session",
	}
	state := session.State{
		Status:    session.StatusRunning,
		Phase:     "provider_call",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := svc.store.Create(meta, state); err != nil {
		t.Fatalf("create stale running session: %v", err)
	}

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/clear", nil)
	req.Header.Set(webMutationHeader, "1")
	svc.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected conflict for running session without current owner, got: %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestServiceDeleteSessionRejectsRunningSessionWithoutLiveOwner(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "running_not_owned_delete",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		RootSessionID:    "running_not_owned_delete",
	}
	state := session.State{Status: session.StatusRunning, Phase: "provider_call", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := svc.store.Create(meta, state); err != nil {
		t.Fatalf("create running session: %v", err)
	}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/"+meta.ID, nil)
	req.Header.Set(webMutationHeader, "1")
	svc.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected conflict deleting running session, got %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestServiceDeleteSessionRejectsActiveDeepDescendantHandle(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	rootMeta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "delete_tree_root",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		RootSessionID:    "delete_tree_root",
	}
	childMeta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "delete_tree_child",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyAutonomous,
		ParentSessionID:  rootMeta.ID,
		RootSessionID:    rootMeta.ID,
		Depth:            1,
	}
	grandchildMeta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "delete_tree_grandchild",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyAutonomous,
		ParentSessionID:  childMeta.ID,
		RootSessionID:    rootMeta.ID,
		Depth:            2,
	}
	greatGrandchildMeta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "delete_tree_great_grandchild",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyAutonomous,
		ParentSessionID:  grandchildMeta.ID,
		RootSessionID:    rootMeta.ID,
		Depth:            3,
	}
	for _, meta := range []session.SessionMetadata{rootMeta, childMeta, grandchildMeta, greatGrandchildMeta} {
		state := session.State{Status: session.StatusCompleted, Phase: "turn_decide", UpdatedAt: now}
		if meta.ID == greatGrandchildMeta.ID {
			state.Status = session.StatusAwaitingInput
			state.Phase = "plan_input"
		}
		if err := svc.store.Create(meta, state); err != nil {
			t.Fatalf("create session %s: %v", meta.ID, err)
		}
	}

	svc.handles[greatGrandchildMeta.ID] = &launchHandle{
		sessionID:      greatGrandchildMeta.ID,
		cancel:         func() {},
		startedAt:      now,
		processStartID: "test-process",
		pid:            os.Getpid(),
	}

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/"+childMeta.ID, nil)
	req.Header.Set(webMutationHeader, "1")
	svc.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected conflict deleting tree with active deep descendant, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "active session tree") {
		t.Fatalf("unexpected response body: %s", recorder.Body.String())
	}
	if _, err := svc.store.LoadMetadata(childMeta.ID); err != nil {
		t.Fatalf("expected target child session to remain, got %v", err)
	}
	if _, err := svc.store.LoadMetadata(greatGrandchildMeta.ID); err != nil {
		t.Fatalf("expected active deep descendant session to remain, got %v", err)
	}
}

func TestServiceClearSessionsRejectsRunningQueueJobs(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	if err := svc.store.SaveJob(session.QueueJob{
		SchemaVersion:   1,
		ID:              "job_running_clear_block",
		Status:          session.QueueStatusRunning,
		ParentSessionID: "parent_running_clear_block",
		RootSessionID:   "parent_running_clear_block",
		Prompt:          "busy",
		Mode:            "exec",
	}); err != nil {
		t.Fatalf("save running queue job: %v", err)
	}

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/clear", nil)
	req.Header.Set(webMutationHeader, "1")
	svc.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected conflict while running queue job exists, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "queue jobs are still running") {
		t.Fatalf("unexpected response body: %s", recorder.Body.String())
	}
}

func TestServiceConfigRoutesUpdateActiveConfig(t *testing.T) {
	cfg := testConfig(t, "")
	provider := cfg.Providers["openai"]
	provider.APIKeyEnv = "OPENAI_API_KEY"
	cfg.Providers["openai"] = provider
	envPath := filepath.Join(t.TempDir(), ".env")
	t.Setenv("GO_CLI_AGENT_ENV_FILE", envPath)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var before map[string]any
	postGetJSON(t, ts.URL+"/api/config", &before)
	if before["default_provider"] != "openai" {
		t.Fatalf("unexpected default provider: %#v", before)
	}
	if before["guardrails_mode"] != "yolo" {
		t.Fatalf("unexpected default guardrails mode: %#v", before)
	}

	postJSON(t, ts.URL+"/api/config", map[string]any{
		"provider":                "openai",
		"base_url":                "http://example.invalid/v1",
		"model":                   "gpt-test",
		"reasoning_mode":          "xhigh",
		"api_key":                 "secret-key",
		"guardrails_mode":         "standard",
		"disable_hard_turn_limit": true,
	}, http.StatusOK, nil)

	var after map[string]any
	postGetJSON(t, ts.URL+"/api/config", &after)
	if after["default_provider"] != "openai" {
		t.Fatalf("unexpected default provider after update: %#v", after)
	}
	if after["guardrails_mode"] != "standard" {
		t.Fatalf("expected standard guardrails mode after update, got %#v", after["guardrails_mode"])
	}
	if after["disable_hard_turn_limit"] != true {
		t.Fatalf("expected hard turn limit to be disabled, got %#v", after)
	}
	providers, _ := after["providers"].(map[string]any)
	openaiProvider, _ := providers["openai"].(map[string]any)
	if openaiProvider["base_url"] != "http://example.invalid/v1" {
		t.Fatalf("expected updated base_url, got %#v", openaiProvider)
	}
	if openaiProvider["model"] != "gpt-test" {
		t.Fatalf("expected updated model, got %#v", openaiProvider)
	}
	if openaiProvider["reasoning_mode"] != "xhigh" || openaiProvider["reasoning_effort"] != "xhigh" {
		t.Fatalf("expected xhigh reasoning mode, got %#v", openaiProvider)
	}
	if got := os.Getenv("OPENAI_API_KEY"); got != "secret-key" {
		t.Fatalf("expected OPENAI_API_KEY to update, got %q", got)
	}
	envBytes, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read persisted env file: %v", err)
	}
	if !strings.Contains(string(envBytes), "OPENAI_API_KEY=secret-key") {
		t.Fatalf("expected OPENAI_API_KEY to persist to env file, got %q", string(envBytes))
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read persisted config file: %v", err)
	}
	if !strings.Contains(string(configBytes), "max_turns_hard: -1") {
		t.Fatalf("expected disabled hard turn limit to persist to config, got %q", string(configBytes))
	}
	if !strings.Contains(string(configBytes), "guardrails_mode: standard") {
		t.Fatalf("expected updated guardrails mode to persist to config, got %q", string(configBytes))
	}
	if !strings.Contains(string(configBytes), "reasoning_effort: xhigh") {
		t.Fatalf("expected updated reasoning effort to persist to config, got %q", string(configBytes))
	}
}

func TestServiceConfigDefaultsProviderScopedFieldsToCurrentDefault(t *testing.T) {
	cfg := testConfig(t, "")
	provider := cfg.Providers["openai"]
	provider.APIKeyEnv = "OPENAI_API_KEY"
	provider.BaseURL = "http://old.example.invalid/v1"
	provider.Model = "gpt-old"
	cfg.Providers["openai"] = provider
	envPath := filepath.Join(t.TempDir(), ".env")
	t.Setenv("GO_CLI_AGENT_ENV_FILE", envPath)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	postJSON(t, ts.URL+"/api/config", map[string]any{
		"base_url":       "http://default.example.invalid/v1",
		"model":          "gpt-default-updated",
		"reasoning_mode": "high",
		"api_key":        "default-secret",
	}, http.StatusOK, nil)

	updated, err := svc.configSnapshot()
	if err != nil {
		t.Fatalf("config snapshot: %v", err)
	}
	if updated.DefaultProvider != "openai" {
		t.Fatalf("expected default provider to remain openai, got %q", updated.DefaultProvider)
	}
	updatedProvider := updated.Providers["openai"]
	if updatedProvider.BaseURL != "http://default.example.invalid/v1" {
		t.Fatalf("expected default provider base_url to update, got %#v", updatedProvider)
	}
	if updatedProvider.Model != "gpt-default-updated" {
		t.Fatalf("expected default provider model to update, got %#v", updatedProvider)
	}
	if updatedProvider.ReasoningEffort != "high" {
		t.Fatalf("expected default provider reasoning effort to update, got %#v", updatedProvider)
	}
	if got := os.Getenv("OPENAI_API_KEY"); got != "default-secret" {
		t.Fatalf("expected OPENAI_API_KEY to update, got %q", got)
	}
	envBytes, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read persisted env file: %v", err)
	}
	if !strings.Contains(string(envBytes), "OPENAI_API_KEY=default-secret") {
		t.Fatalf("expected default provider API key to persist to env file, got %q", string(envBytes))
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read persisted config file: %v", err)
	}
	configText := string(configBytes)
	for _, want := range []string{
		"base_url: http://default.example.invalid/v1",
		"model: gpt-default-updated",
		"reasoning_effort: high",
	} {
		if !strings.Contains(configText, want) {
			t.Fatalf("expected %q to persist to config, got %q", want, configText)
		}
	}
}

func TestServiceConfigSaveClearsExplicitProviderFields(t *testing.T) {
	cfg := testConfig(t, "")
	provider := cfg.Providers["openai"]
	provider.APIProvider = "openai-compatible"
	provider.BaseURL = "http://example.invalid/v1"
	provider.Model = "gpt-old"
	cfg.Providers["openai"] = provider
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	ts := httptest.NewServer(svc)
	defer ts.Close()

	postJSON(t, ts.URL+"/api/config", map[string]any{
		"provider":     "openai",
		"api_provider": "",
		"base_url":     "",
		"model":        "",
	}, http.StatusOK, nil)
	updated, err := svc.configSnapshot()
	if err != nil {
		t.Fatalf("config snapshot: %v", err)
	}
	p := updated.Providers["openai"]
	if p.APIProvider != "" || p.BaseURL != "" || p.Model != "" {
		t.Fatalf("expected explicit provider fields to be cleared, got %#v", p)
	}
}

func TestServiceConfigRejectsUnsupportedAPIProvider(t *testing.T) {
	cfg := testConfig(t, "")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/config", map[string]any{
		"provider":     "openai",
		"api_provider": "not-real",
	}, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "unsupported api_provider") {
		t.Fatalf("expected unsupported api_provider error, got %#v", errResp)
	}
	updated, err := svc.configSnapshot()
	if err != nil {
		t.Fatalf("config snapshot: %v", err)
	}
	if got := updated.Providers["openai"].APIProvider; got != cfg.Providers["openai"].APIProvider {
		t.Fatalf("unsupported api_provider should not mutate active config, got %q", got)
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsupported api_provider should not persist config; stat err=%v", err)
	}
}

func TestServiceConfigRoutesPersistRoleProviderOverrides(t *testing.T) {
	cfg := testConfig(t, "")
	cfg.Providers["validator"] = cfg.Providers["openai"]
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	ts := httptest.NewServer(svc)
	defer ts.Close()

	postJSON(t, ts.URL+"/api/config", map[string]any{
		"provider": "openai",
		"role_providers": map[string]any{
			"planner": map[string]any{
				"model": "planner-only-model",
			},
			"generator": map[string]any{},
			"evaluator": map[string]any{
				"provider":     "validator",
				"api_provider": "openai-compatible",
				"base_url":     "http://validator.invalid/v1",
				"model":        "validator-model",
			},
		},
	}, http.StatusOK, nil)

	var after map[string]any
	postGetJSON(t, ts.URL+"/api/config", &after)
	roleProviders, _ := after["role_providers"].(map[string]any)
	planner, _ := roleProviders["planner"].(map[string]any)
	evaluator, _ := roleProviders["evaluator"].(map[string]any)
	generator, _ := roleProviders["generator"].(map[string]any)
	if planner["model"] != "planner-only-model" || planner["provider"] != "" {
		t.Fatalf("expected planner model-only override, got %#v", planner)
	}
	if evaluator["provider"] != "validator" || evaluator["base_url"] != "http://validator.invalid/v1" || evaluator["model"] != "validator-model" {
		t.Fatalf("expected evaluator override, got %#v", evaluator)
	}
	if generator["provider"] != "" || generator["model"] != "" {
		t.Fatalf("expected empty generator override, got %#v", generator)
	}
	updated, err := svc.configSnapshot()
	if err != nil {
		t.Fatalf("config snapshot: %v", err)
	}
	if updated.RoleProviders.Planner.Model != "planner-only-model" || updated.RoleProviders.Evaluator.Provider != "validator" {
		t.Fatalf("expected role overrides in active config, got %#v", updated.RoleProviders)
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read persisted config file: %v", err)
	}
	configText := string(configBytes)
	for _, want := range []string{
		"role_providers:",
		"planner:",
		"model: planner-only-model",
		"evaluator:",
		"provider: validator",
		"base_url: http://validator.invalid/v1",
	} {
		if !strings.Contains(configText, want) {
			t.Fatalf("expected %q to persist to config, got %q", want, configText)
		}
	}
}

func TestServiceConfigRejectsUnknownRoleProviderOverride(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/config", map[string]any{
		"provider": "openai",
		"role_providers": map[string]any{
			"evaluator": map[string]any{
				"provider": "missing-provider",
			},
		},
	}, http.StatusBadRequest)
	if errResp.Code != errorCodeUnknownProvider {
		t.Fatalf("expected unknown provider code, got %#v", errResp)
	}
}

func TestServiceConfigRejectsRoleProviderOverrideWithoutEffectiveAPIProvider(t *testing.T) {
	cfg := testConfig(t, "")
	cfg.Providers["vendor-x"] = config.Provider{
		APIKeyEnv:  "VENDOR_X_API_KEY",
		BaseURL:    "http://vendor.invalid/v1",
		Model:      "vendor-model",
		TimeoutSec: 3,
	}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/config", map[string]any{
		"provider": "openai",
		"role_providers": map[string]any{
			"evaluator": map[string]any{
				"provider": "vendor-x",
			},
		},
	}, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "requires api_provider") {
		t.Fatalf("expected missing api_provider error, got %#v", errResp)
	}
	updated, err := svc.configSnapshot()
	if err != nil {
		t.Fatalf("config snapshot: %v", err)
	}
	if updated.RoleProviders.Evaluator.Provider != "" {
		t.Fatalf("invalid role provider should not mutate active config, got %#v", updated.RoleProviders.Evaluator)
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid role provider should not persist config; stat err=%v", err)
	}
}

func TestServiceConfigRoleProviderOverrideUsesSameRequestAPIProviderUpdate(t *testing.T) {
	cfg := testConfig(t, "")
	cfg.Providers["vendor-x"] = config.Provider{
		APIKeyEnv:  "VENDOR_X_API_KEY",
		BaseURL:    "http://vendor.invalid/v1",
		Model:      "vendor-model",
		TimeoutSec: 3,
	}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	ts := httptest.NewServer(svc)
	defer ts.Close()

	postJSON(t, ts.URL+"/api/config", map[string]any{
		"provider":     "vendor-x",
		"api_provider": "openai-compatible",
		"role_providers": map[string]any{
			"evaluator": map[string]any{
				"provider": "vendor-x",
			},
		},
	}, http.StatusOK, nil)

	updated, err := svc.configSnapshot()
	if err != nil {
		t.Fatalf("config snapshot: %v", err)
	}
	if updated.Providers["vendor-x"].APIProvider != "openai-compatible" {
		t.Fatalf("expected same request to update provider API provider, got %#v", updated.Providers["vendor-x"])
	}
	if updated.RoleProviders.Evaluator.Provider != "vendor-x" {
		t.Fatalf("expected evaluator role provider to persist, got %#v", updated.RoleProviders.Evaluator)
	}
}

func TestServiceConfigRoutesPersistThinkingMaxMode(t *testing.T) {
	cfg := testConfig(t, "")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	postJSON(t, ts.URL+"/api/config", map[string]any{
		"provider":       "anthropic",
		"base_url":       "http://example.invalid",
		"model":          "claude-test",
		"reasoning_mode": "max",
	}, http.StatusOK, nil)

	var after map[string]any
	postGetJSON(t, ts.URL+"/api/config", &after)
	if after["default_provider"] != "anthropic" {
		t.Fatalf("unexpected default provider after update: %#v", after)
	}
	providers, _ := after["providers"].(map[string]any)
	anthropicProvider, _ := providers["anthropic"].(map[string]any)
	if anthropicProvider["reasoning_mode"] != "max" {
		t.Fatalf("expected max thinking mode, got %#v", anthropicProvider)
	}
	if anthropicProvider["include_thoughts"] != true {
		t.Fatalf("expected include_thoughts=true, got %#v", anthropicProvider)
	}
	if got, _ := anthropicProvider["thinking_budget"].(float64); int(got) != settingsThinkingMaxBudget {
		t.Fatalf("expected max thinking budget, got %#v", anthropicProvider)
	}
	if got, _ := anthropicProvider["max_output_tokens"].(float64); int(got) != settingsThinkingMaxOutputTokens {
		t.Fatalf("expected max output tokens to fit max thinking budget, got %#v", anthropicProvider)
	}

	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read persisted config file: %v", err)
	}
	configText := string(configBytes)
	for _, want := range []string{
		"thinking_budget: 32000",
		"include_thoughts: true",
		"max_output_tokens: 32768",
	} {
		if !strings.Contains(configText, want) {
			t.Fatalf("expected %q to persist to config, got %q", want, configText)
		}
	}
}

func TestServiceConfigClearsStaleReasoningFieldsWhenSwitchingAdapterFamily(t *testing.T) {
	cfg := testConfig(t, "")
	openaiProvider := cfg.Providers["openai"]
	openaiProvider.APIProvider = "openai-compatible"
	openaiProvider.ReasoningEffort = "xhigh"
	openaiProvider.ReasoningSummary = "auto"
	cfg.Providers["openai"] = openaiProvider
	anthropicProvider := cfg.Providers["anthropic"]
	enabled := true
	anthropicProvider.APIProvider = "anthropic-compatible"
	anthropicProvider.ThinkingBudget = settingsThinkingMaxBudget
	anthropicProvider.IncludeThoughts = &enabled
	anthropicProvider.MaxOutputTokens = settingsThinkingMaxOutputTokens
	cfg.Providers["anthropic"] = anthropicProvider
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	postJSON(t, ts.URL+"/api/config", map[string]any{
		"provider":          "openai",
		"api_provider":      "anthropic-compatible",
		"reasoning_mode":    "default",
		"reasoning_summary": "default",
	}, http.StatusOK, nil)

	postJSON(t, ts.URL+"/api/config", map[string]any{
		"provider":          "anthropic",
		"api_provider":      "openai-compatible",
		"reasoning_mode":    "default",
		"reasoning_summary": "default",
	}, http.StatusOK, nil)

	updated, err := svc.configSnapshot()
	if err != nil {
		t.Fatalf("config snapshot: %v", err)
	}
	switchedThinking := updated.Providers["openai"]
	if switchedThinking.ReasoningEffort != "" || switchedThinking.ReasoningSummary != "" {
		t.Fatalf("expected OpenAI reasoning fields to clear for thinking adapter, got %#v", switchedThinking)
	}
	switchedOpenAI := updated.Providers["anthropic"]
	if switchedOpenAI.ThinkingBudget != 0 || switchedOpenAI.IncludeThoughts != nil {
		t.Fatalf("expected thinking fields to clear for OpenAI adapter, got %#v", switchedOpenAI)
	}

	var after map[string]any
	postGetJSON(t, ts.URL+"/api/config", &after)
	providers, _ := after["providers"].(map[string]any)
	openaiPayload, _ := providers["openai"].(map[string]any)
	if openaiPayload["effective_api_provider"] != "anthropic-compatible" || openaiPayload["reasoning_effort"] != "" || openaiPayload["reasoning_summary"] != "default" {
		t.Fatalf("expected thinking adapter payload without stale OpenAI reasoning fields, got %#v", openaiPayload)
	}
	anthropicPayload, _ := providers["anthropic"].(map[string]any)
	if anthropicPayload["effective_api_provider"] != "openai-compatible" || anthropicPayload["thinking_budget"].(float64) != 0 || anthropicPayload["include_thoughts"] != nil {
		t.Fatalf("expected OpenAI adapter payload without stale thinking fields, got %#v", anthropicPayload)
	}

	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read persisted config file: %v", err)
	}
	configText := string(configBytes)
	for _, stale := range []string{
		"reasoning_effort: xhigh",
		"reasoning_summary: auto",
		"thinking_budget: 32000",
		"include_thoughts: true",
	} {
		if strings.Contains(configText, stale) {
			t.Fatalf("expected stale adapter-family field %q to be absent from persisted config, got %q", stale, configText)
		}
	}
}

func TestServiceConfigCustomAnthropicProviderGetsThinkingModes(t *testing.T) {
	cfg := testConfig(t, "")
	cfg.Providers["kimi"] = config.Provider{
		APIProvider:      "anthropic-compatible",
		APIKeyEnv:        "KIMI_API_KEY",
		BaseURL:          "http://example.invalid",
		Model:            "kimi-test",
		TimeoutSec:       3,
		AnthropicVersion: "2023-06-01",
	}
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var payload map[string]any
	postGetJSON(t, ts.URL+"/api/config", &payload)
	providers, _ := payload["providers"].(map[string]any)
	kimiProvider, _ := providers["kimi"].(map[string]any)
	if kimiProvider["api_provider"] != "anthropic-compatible" || kimiProvider["effective_api_provider"] != "anthropic-compatible" {
		t.Fatalf("expected custom provider to expose anthropic-compatible API provider, got %#v", kimiProvider)
	}
	modes, _ := kimiProvider["reasoning_modes"].([]any)
	if got := anySliceStrings(modes); strings.Join(got, ",") != "default,standard,max,off" {
		t.Fatalf("expected thinking modes for custom anthropic-compatible provider, got %#v", kimiProvider)
	}
}

func TestServiceConfigRejectsCustomProviderWithoutAPIProvider(t *testing.T) {
	cfg := testConfig(t, "")
	cfg.Providers["vendor-x"] = config.Provider{
		APIKeyEnv:  "VENDOR_X_API_KEY",
		BaseURL:    "http://example.invalid",
		Model:      "vendor-test",
		TimeoutSec: 3,
	}
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/config/test", map[string]any{
		"provider": "vendor-x",
	}, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "requires api_provider") {
		t.Fatalf("expected api_provider error, got %#v", errResp)
	}
}

func TestServiceConfigTestRejectsUnsupportedAPIProvider(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/config/test", map[string]any{
		"provider":     "openai",
		"api_provider": "not-real",
	}, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "unsupported api_provider") {
		t.Fatalf("expected unsupported api_provider error, got %#v", errResp)
	}
}

func TestServiceConfigTestRejectsInvalidAPIKeyBeforeProbe(t *testing.T) {
	var probeCalled bool
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probeCalled = true
		w.WriteHeader(http.StatusTeapot)
	}))
	defer providerServer.Close()

	cfg := testConfig(t, providerServer.URL)
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/config/test", map[string]any{
		"provider": "openai",
		"base_url": providerServer.URL,
		"api_key":  "sk-invalid\x00value",
	}, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "invalid env value") {
		t.Fatalf("expected invalid env value error, got %#v", errResp)
	}
	if probeCalled {
		t.Fatalf("provider probe should not run with invalid transient API key")
	}
}

func TestServiceConfigTestRejectsSaveOnlyFieldsBeforeProbe(t *testing.T) {
	var probeCalled bool
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probeCalled = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_probe_1",
			"status":"completed",
			"output":[
				{"type":"function_call","call_id":"call_finish_1","name":"finish","arguments":"{\"message\":\"provider probe ok\"}"}
			],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer providerServer.Close()

	cfg := testConfig(t, providerServer.URL)
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/config/test", map[string]any{
		"provider":        "openai",
		"base_url":        providerServer.URL,
		"guardrails_mode": "standrad",
	}, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "unknown field") || !strings.Contains(errResp.Error, "guardrails_mode") {
		t.Fatalf("expected config-test to reject save-only field, got %#v", errResp)
	}
	if probeCalled {
		t.Fatalf("provider probe should not run with save-only config-test fields")
	}
}

func TestServiceConfigRejectsUnsupportedRoleAPIProviderOverride(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	errResp := postJSONError(t, ts.URL+"/api/config", map[string]any{
		"provider": "openai",
		"role_providers": map[string]any{
			"evaluator": map[string]any{
				"api_provider": "not-real",
			},
		},
	}, http.StatusBadRequest)
	if !strings.Contains(errResp.Error, "unsupported api_provider") {
		t.Fatalf("expected unsupported api_provider error, got %#v", errResp)
	}
	updated, err := svc.configSnapshot()
	if err != nil {
		t.Fatalf("config snapshot: %v", err)
	}
	if updated.RoleProviders.Evaluator.APIProvider != "" {
		t.Fatalf("unsupported role api_provider should not mutate active config, got %#v", updated.RoleProviders.Evaluator)
	}
}

func TestServiceConfigTestAppliesReasoningModeWithoutPersisting(t *testing.T) {
	seenRequest := make(chan map[string]any, 1)
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("unexpected provider path: %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode provider request: %v", err)
		}
		seenRequest <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_probe_1",
			"status":"completed",
			"output":[
				{"type":"function_call","call_id":"call_finish_1","name":"finish","arguments":"{\"message\":\"provider probe ok\"}"}
			],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer providerServer.Close()

	cfg := testConfig(t, providerServer.URL)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var result TestConfigResponse
	postJSON(t, ts.URL+"/api/config/test", map[string]any{
		"provider":          "openai",
		"base_url":          providerServer.URL,
		"model":             "gpt-test",
		"reasoning_mode":    "xhigh",
		"reasoning_summary": "auto",
	}, http.StatusOK, &result)

	if !result.Success || result.Provider != "openai" || result.EffectiveAPIProvider != "openai-compatible" || result.Model != "gpt-test" || result.ReasoningMode != "xhigh" || result.ReasoningEffort != "xhigh" || result.ReasoningSummary != "auto" || result.ThinkingStrategy != "responses_reasoning_summary" {
		t.Fatalf("unexpected test config response: %#v", result)
	}
	seen := <-seenRequest
	if seen["model"] != "gpt-test" {
		t.Fatalf("expected provider probe model override, got %#v", seen)
	}
	reasoning, _ := seen["reasoning"].(map[string]any)
	if reasoning["effort"] != "xhigh" {
		t.Fatalf("expected provider probe to send reasoning effort xhigh, got %#v", seen)
	}
	if reasoning["summary"] != "auto" {
		t.Fatalf("expected provider probe to send reasoning summary auto, got %#v", seen)
	}
	include, _ := seen["include"].([]any)
	if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("expected provider probe to request encrypted reasoning include, got %#v", seen)
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config test should not persist config; stat err=%v", err)
	}
}

func TestServiceConfigTestUsesAnthropicPromptCacheDefault(t *testing.T) {
	seenRequest := make(chan map[string]any, 1)
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected provider path: %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode provider request: %v", err)
		}
		seenRequest <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_probe_1",
			"stop_reason":"end_turn",
			"content":[{"type":"text","text":"provider probe ok"}],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer providerServer.Close()

	cfg := testConfig(t, providerServer.URL)
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	apiProvider := "anthropic-compatible"
	var result TestConfigResponse
	postJSON(t, ts.URL+"/api/config/test", map[string]any{
		"provider":     "openai",
		"api_provider": apiProvider,
		"base_url":     providerServer.URL,
		"model":        "claude-test",
	}, http.StatusOK, &result)

	if !result.Success || result.EffectiveAPIProvider != "anthropic-compatible" || result.Model != "claude-test" {
		t.Fatalf("unexpected test config response: %#v", result)
	}
	seen := <-seenRequest
	if !webAnthropicCacheControlOnFirstSystemBlock(seen) || !webAnthropicCacheControlOnLastMessageBlock(seen) {
		t.Fatalf("expected Web config-test probe to send default prompt cache markers, got %#v", seen)
	}
}

func TestServiceConfigTestClearsStaleReasoningFieldsWhenSwitchingAdapterFamily(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/messages":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id":"msg_probe_1",
				"stop_reason":"end_turn",
				"content":[{"type":"text","text":"provider probe ok"}],
				"usage":{"input_tokens":10,"output_tokens":5}
			}`))
		case "/responses":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode provider request: %v", err)
			}
			if _, ok := body["reasoning"]; ok {
				t.Errorf("unexpected stale reasoning payload in OpenAI request: %#v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id":"resp_probe_1",
				"status":"completed",
				"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"provider probe ok"}]}],
				"usage":{"input_tokens":10,"output_tokens":5}
			}`))
		default:
			t.Errorf("unexpected provider path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer providerServer.Close()

	cfg := testConfig(t, providerServer.URL)
	openaiProvider := cfg.Providers["openai"]
	openaiProvider.APIProvider = "openai-compatible"
	openaiProvider.BaseURL = providerServer.URL
	openaiProvider.ReasoningEffort = "xhigh"
	openaiProvider.ReasoningSummary = "auto"
	cfg.Providers["openai"] = openaiProvider
	anthropicProvider := cfg.Providers["anthropic"]
	enabled := true
	anthropicProvider.BaseURL = providerServer.URL
	anthropicProvider.ThinkingBudget = settingsThinkingMaxBudget
	anthropicProvider.IncludeThoughts = &enabled
	cfg.Providers["anthropic"] = anthropicProvider
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var thinkingResult TestConfigResponse
	postJSON(t, ts.URL+"/api/config/test", map[string]any{
		"provider":     "openai",
		"api_provider": "anthropic-compatible",
		"base_url":     providerServer.URL,
		"model":        "claude-test",
	}, http.StatusOK, &thinkingResult)
	if thinkingResult.EffectiveAPIProvider != "anthropic-compatible" || thinkingResult.ReasoningEffort != "" || thinkingResult.ReasoningSummary != "default" {
		t.Fatalf("expected thinking probe response without stale OpenAI reasoning fields, got %#v", thinkingResult)
	}

	var openAIResult TestConfigResponse
	postJSON(t, ts.URL+"/api/config/test", map[string]any{
		"provider":     "anthropic",
		"api_provider": "openai-compatible",
		"base_url":     providerServer.URL,
		"model":        "gpt-test",
	}, http.StatusOK, &openAIResult)
	if openAIResult.EffectiveAPIProvider != "openai-compatible" || openAIResult.ThinkingBudget != 0 || openAIResult.IncludeThoughts != nil {
		t.Fatalf("expected OpenAI probe response without stale thinking fields, got %#v", openAIResult)
	}
}

func TestServiceSessionMessagesPagination(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	meta := testSessionMetadata(t, "session_messages_page")
	if err := svc.store.Create(meta, testSessionState(session.StatusCompleted)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	var written []session.Message
	for i := 0; i < 45; i++ {
		msg := session.NewMessage("user", "message "+strconv.Itoa(i))
		if err := svc.store.AppendMessage(meta.ID, msg); err != nil {
			t.Fatalf("append message %d: %v", i, err)
		}
		written = append(written, msg)
	}

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var detail SessionDetailResponse
	postGetJSON(t, ts.URL+"/api/sessions/"+meta.ID+"?limit=2", &detail)
	if !detail.HasMoreMessages || len(detail.Messages) != 2 || detail.Messages[0].ID != written[43].ID || detail.Messages[1].ID != written[44].ID {
		t.Fatalf("unexpected session detail pagination metadata: %#v", detail.Messages)
	}
	postGetJSON(t, ts.URL+"/api/sessions/"+meta.ID+"?limit=-1", &detail)
	if !detail.HasMoreMessages || len(detail.Messages) != 40 || detail.Messages[0].ID != written[5].ID || detail.Messages[39].ID != written[44].ID {
		t.Fatalf("negative session detail limit should fall back to default window, got %#v", detail.Messages)
	}
	postGetJSON(t, ts.URL+"/api/sessions/"+meta.ID+"?limit=1000", &detail)
	if !detail.HasMoreMessages || len(detail.Messages) != 40 || detail.Messages[0].ID != written[5].ID || detail.Messages[39].ID != written[44].ID {
		t.Fatalf("oversized session detail limit should fall back to default window, got %#v", detail.Messages)
	}

	var page MessagesResponse
	postGetJSON(t, ts.URL+"/api/sessions/"+meta.ID+"/messages?before_id="+url.QueryEscape(written[43].ID)+"&limit=2", &page)
	if !page.HasMore || len(page.Messages) != 2 || page.Messages[0].ID != written[41].ID || page.Messages[1].ID != written[42].ID {
		t.Fatalf("unexpected previous page: %#v", page)
	}

	postGetJSON(t, ts.URL+"/api/sessions/"+meta.ID+"/messages?before_id="+url.QueryEscape(written[1].ID)+"&limit=2", &page)
	if page.HasMore || len(page.Messages) != 1 || page.Messages[0].ID != written[0].ID {
		t.Fatalf("unexpected first page: %#v", page)
	}

	postGetJSON(t, ts.URL+"/api/sessions/"+meta.ID+"/messages?before_id="+url.QueryEscape(written[3].ID)+"&limit=-1", &page)
	if len(page.Messages) != 3 {
		t.Fatalf("negative limit should fall back safely, got %#v", page)
	}
}

func TestServiceWorkspaceRoutesListReadAndRejectEscape(t *testing.T) {
	root := t.TempDir()
	workspaceRoot := filepath.Join(root, "workspace")
	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir root: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(previousWD)
	})

	if err := os.MkdirAll(filepath.Join(workspaceRoot, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, "nested", "hello.txt"), []byte("hello workspace"), 0o644); err != nil {
		t.Fatalf("write nested file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, ".env"), []byte("WORKSPACE_SECRET=1"), 0o600); err != nil {
		t.Fatalf("write workspace env file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, "id_ecdsa"), []byte("WORKSPACE_PRIVATE_KEY=1"), 0o600); err != nil {
		t.Fatalf("write workspace private key: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, "credentials.json"), []byte(`{"token":"workspace"}`), 0o600); err != nil {
		t.Fatalf("write workspace credentials file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, "client_secret.json"), []byte(`{"client_secret":"workspace"}`), 0o600); err != nil {
		t.Fatalf("write workspace client secret file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, "service_account.json"), []byte(`{"private_key":"workspace"}`), 0o600); err != nil {
		t.Fatalf("write workspace service account file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, "env-real"), []byte("WORKSPACE_SECRET_ALIAS=1"), 0o600); err != nil {
		t.Fatalf("write workspace env alias target: %v", err)
	}
	if err := os.Symlink("env-real", filepath.Join(workspaceRoot, ".env.local")); err != nil {
		t.Fatalf("symlink workspace env alias: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspaceRoot, "ssh-real"), 0o755); err != nil {
		t.Fatalf("mkdir workspace ssh alias target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, "ssh-real", "config"), []byte("Host workspace"), 0o600); err != nil {
		t.Fatalf("write workspace ssh alias target: %v", err)
	}
	if err := os.Symlink("ssh-real", filepath.Join(workspaceRoot, ".ssh")); err != nil {
		t.Fatalf("symlink workspace ssh alias: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspaceRoot, ".azure"), 0o755); err != nil {
		t.Fatalf("mkdir workspace azure credential dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, ".azure", "accessTokens.json"), []byte(`{"token":"azure"}`), 0o600); err != nil {
		t.Fatalf("write workspace azure credential file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspaceRoot, ".oci"), 0o755); err != nil {
		t.Fatalf("mkdir workspace oci credential dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, ".oci", "config"), []byte("[DEFAULT]\nuser = ocid1.user.oc1..example\n"), 0o600); err != nil {
		t.Fatalf("write workspace oci credential file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspaceRoot, ".config", "gcloud", "configurations"), 0o755); err != nil {
		t.Fatalf("mkdir workspace gcloud credential dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, ".config", "gcloud", "configurations", "config_default"), []byte("[core]\naccount = operator@example.com\n"), 0o600); err != nil {
		t.Fatalf("write workspace gcloud config file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "root-only.txt"), []byte("server cwd file"), 0o644); err != nil {
		t.Fatalf("write root-only file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("ROOT_SECRET=1"), 0o600); err != nil {
		t.Fatalf("write root env file: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, ".env"), filepath.Join(workspaceRoot, "root-env-alias")); err != nil {
		t.Fatalf("symlink root env alias: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "deploy.pem"), []byte("ROOT_PRIVATE_KEY=1"), 0o600); err != nil {
		t.Fatalf("write root private key: %v", err)
	}
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(workspaceRoot, "outside-alias.txt")); err != nil {
		t.Fatalf("symlink outside alias: %v", err)
	}

	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var tree []map[string]any
	postGetJSON(t, ts.URL+"/api/files", &tree)
	if len(tree) == 0 {
		t.Fatal("expected file tree entries")
	}
	for _, item := range tree {
		if item["name"] == "root-only.txt" {
			t.Fatalf("workspace listing leaked server cwd file: %#v", tree)
		}
		if item["name"] == ".env" {
			t.Fatalf("workspace listing leaked env file: %#v", tree)
		}
		if item["name"] == "id_ecdsa" || item["name"] == "credentials.json" || item["name"] == "client_secret.json" || item["name"] == "service_account.json" {
			t.Fatalf("workspace listing leaked credential-like file: %#v", tree)
		}
		if item["name"] == ".azure" {
			t.Fatalf("workspace listing leaked cloud credential directory: %#v", tree)
		}
		if item["name"] == ".oci" {
			t.Fatalf("workspace listing leaked cloud credential directory: %#v", tree)
		}
		if item["name"] == "root-env-alias" {
			t.Fatalf("workspace listing leaked sensitive symlink alias: %#v", tree)
		}
		if item["name"] == "outside-alias.txt" {
			t.Fatalf("workspace listing leaked escaping symlink alias: %#v", tree)
		}
	}
	if firstType, _ := tree[0]["type"].(string); firstType != "directory" {
		t.Fatalf("expected directories to sort first, got %#v", tree[0])
	}

	var nested []map[string]any
	postGetJSON(t, ts.URL+"/api/files?path="+url.QueryEscape("nested"), &nested)
	if len(nested) != 2 || nested[0]["name"] != ".." || nested[0]["navigation"] != "parent" || nested[1]["name"] != "hello.txt" {
		t.Fatalf("unexpected nested directory listing: %#v", nested)
	}

	var readResp map[string]string
	postGetJSON(t, ts.URL+"/api/file/read?path="+url.QueryEscape("nested/hello.txt"), &readResp)
	if readResp["content"] != "hello workspace" {
		t.Fatalf("unexpected file content: %#v", readResp)
	}

	largeFile := filepath.Join(workspaceRoot, "nested", "large.log")
	large, err := os.Create(largeFile)
	if err != nil {
		t.Fatalf("create large workspace file: %v", err)
	}
	if _, err := large.WriteString("large-start"); err != nil {
		_ = large.Close()
		t.Fatalf("write large workspace file prefix: %v", err)
	}
	if err := large.Truncate(fileutil.MaxRegularFileReadBytes + 4096); err != nil {
		_ = large.Close()
		t.Fatalf("truncate large workspace file: %v", err)
	}
	if err := large.Close(); err != nil {
		t.Fatalf("close large workspace file: %v", err)
	}

	var pageResp struct {
		Content    string `json:"content"`
		Offset     int64  `json:"offset"`
		Limit      int64  `json:"limit"`
		Size       int64  `json:"size"`
		Truncated  bool   `json:"truncated"`
		NextOffset int64  `json:"next_offset"`
	}
	postGetJSON(t, ts.URL+"/api/file/read?path="+url.QueryEscape("nested/hello.txt")+"&offset=0&limit=5", &pageResp)
	if pageResp.Content != "hello" || pageResp.Offset != 0 || pageResp.Limit != 5 || pageResp.Size != int64(len("hello workspace")) || !pageResp.Truncated || pageResp.NextOffset != 5 {
		t.Fatalf("unexpected first paged read response: %#v", pageResp)
	}
	pageResp = struct {
		Content    string `json:"content"`
		Offset     int64  `json:"offset"`
		Limit      int64  `json:"limit"`
		Size       int64  `json:"size"`
		Truncated  bool   `json:"truncated"`
		NextOffset int64  `json:"next_offset"`
	}{}
	postGetJSON(t, ts.URL+"/api/file/read?path="+url.QueryEscape("nested/hello.txt")+"&offset=6&limit=32", &pageResp)
	if pageResp.Content != "workspace" || pageResp.Offset != 6 || pageResp.Limit != 32 || pageResp.Size != int64(len("hello workspace")) || pageResp.Truncated || pageResp.NextOffset != 0 {
		t.Fatalf("unexpected second paged read response: %#v", pageResp)
	}
	pageResp = struct {
		Content    string `json:"content"`
		Offset     int64  `json:"offset"`
		Limit      int64  `json:"limit"`
		Size       int64  `json:"size"`
		Truncated  bool   `json:"truncated"`
		NextOffset int64  `json:"next_offset"`
	}{}
	postGetJSON(t, ts.URL+"/api/file/read?path="+url.QueryEscape("nested/hello.txt")+"&offset=99&limit=8", &pageResp)
	if pageResp.Content != "" || pageResp.Offset != 99 || pageResp.Size != int64(len("hello workspace")) || pageResp.Truncated || pageResp.NextOffset != 0 {
		t.Fatalf("unexpected past-end paged read response: %#v", pageResp)
	}
	pageResp = struct {
		Content    string `json:"content"`
		Offset     int64  `json:"offset"`
		Limit      int64  `json:"limit"`
		Size       int64  `json:"size"`
		Truncated  bool   `json:"truncated"`
		NextOffset int64  `json:"next_offset"`
	}{}
	postGetJSON(t, ts.URL+"/api/file/read?path="+url.QueryEscape("nested/large.log")+"&offset=0&limit=10", &pageResp)
	if pageResp.Content != "large-star" || pageResp.Offset != 0 || pageResp.Limit != 10 || pageResp.Size != fileutil.MaxRegularFileReadBytes+4096 || !pageResp.Truncated || pageResp.NextOffset != 10 {
		t.Fatalf("unexpected large paged read response: %#v", pageResp)
	}
	for _, query := range []string{
		"&offset=-1&limit=5",
		"&offset=0&limit=0",
	} {
		resp, err := http.Get(ts.URL + "/api/file/read?path=" + url.QueryEscape("nested/hello.txt") + query)
		if err != nil {
			t.Fatalf("invalid paged read request %s: %v", query, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected bad request for invalid paged read %s, got %d body=%s", query, resp.StatusCode, string(body))
		}
	}

	postGetJSON(t, ts.URL+"/api/file/read?path="+url.QueryEscape("../root-only.txt"), &readResp)
	if readResp["content"] != "server cwd file" {
		t.Fatalf("expected browser parent read to stay within server cwd, got %#v", readResp)
	}

	resp, err := http.Get(ts.URL + "/api/file/read?path=" + url.QueryEscape("missing.txt"))
	if err != nil {
		t.Fatalf("missing workspace file read request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected not found for missing workspace file read, got %d body=%s", resp.StatusCode, string(body))
	}

	resp, err = http.Get(ts.URL + "/api/file/read?path=" + url.QueryEscape("nested"))
	if err != nil {
		t.Fatalf("directory-as-file workspace read request: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected bad request for directory-as-file workspace read, got %d body=%s", resp.StatusCode, string(body))
	}

	resp, err = http.Get(ts.URL + "/api/files?path=" + url.QueryEscape("missing-dir"))
	if err != nil {
		t.Fatalf("missing workspace directory list request: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected not found for missing workspace directory list, got %d body=%s", resp.StatusCode, string(body))
	}

	resp, err = http.Get(ts.URL + "/api/file/read?path=" + url.QueryEscape(".env"))
	if err != nil {
		t.Fatalf("workspace env read request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected forbidden for workspace env read, got %d body=%s", resp.StatusCode, string(body))
	}

	for _, deniedPath := range []string{"id_ecdsa", "credentials.json", "client_secret.json", "service_account.json"} {
		resp, err = http.Get(ts.URL + "/api/file/read?path=" + url.QueryEscape(deniedPath))
		if err != nil {
			t.Fatalf("workspace credential read request %s: %v", deniedPath, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected forbidden for workspace credential read %s, got %d body=%s", deniedPath, resp.StatusCode, string(body))
		}
	}
	for _, deniedAlias := range []string{".env.local", ".ssh/config", ".azure/accessTokens.json", ".oci/config", ".config/gcloud/configurations/config_default"} {
		resp, err = http.Get(ts.URL + "/api/file/read?path=" + url.QueryEscape(deniedAlias))
		if err != nil {
			t.Fatalf("workspace sensitive symlink read request %s: %v", deniedAlias, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected forbidden for workspace sensitive symlink read %s, got %d body=%s", deniedAlias, resp.StatusCode, string(body))
		}
	}
	resp, err = http.Get(ts.URL + "/api/files?path=" + url.QueryEscape(".ssh"))
	if err != nil {
		t.Fatalf("workspace sensitive symlink list request: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected forbidden for workspace sensitive symlink list, got %d body=%s", resp.StatusCode, string(body))
	}
	for _, deniedDir := range []string{".azure", ".oci", ".config/gcloud"} {
		resp, err = http.Get(ts.URL + "/api/files?path=" + url.QueryEscape(deniedDir))
		if err != nil {
			t.Fatalf("workspace cloud credential list request %s: %v", deniedDir, err)
		}
		body, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected forbidden for workspace cloud credential list %s, got %d body=%s", deniedDir, resp.StatusCode, string(body))
		}
	}

	resp, err = http.Get(ts.URL + "/api/file/read?path=" + url.QueryEscape("../.env"))
	if err != nil {
		t.Fatalf("root env read request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected forbidden for root env read, got %d body=%s", resp.StatusCode, string(body))
	}

	var rootTree []map[string]any
	postGetJSON(t, ts.URL+"/api/files?path="+url.QueryEscape(".."), &rootTree)
	for _, item := range rootTree {
		if item["name"] == ".env" {
			t.Fatalf("root listing leaked env file: %#v", rootTree)
		}
		if item["name"] == "deploy.pem" {
			t.Fatalf("root listing leaked private key file: %#v", rootTree)
		}
	}

	resp, err = http.Get(ts.URL + "/api/file/read?path=" + url.QueryEscape("../deploy.pem"))
	if err != nil {
		t.Fatalf("root private key read request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected forbidden for root private key read, got %d body=%s", resp.StatusCode, string(body))
	}

	resp, err = http.Get(ts.URL + "/api/file/read?path=" + url.QueryEscape("../../outside.txt"))
	if err != nil {
		t.Fatalf("escape read request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected forbidden for escape read, got %d body=%s", resp.StatusCode, string(body))
	}

	resp, err = http.Get(ts.URL + "/api/files?path=" + url.QueryEscape("../../"))
	if err != nil {
		t.Fatalf("escape list request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected forbidden for escape list, got %d body=%s", resp.StatusCode, string(body))
	}
}

func TestServiceWorkspaceRoutesRejectsCredentialRCFiles(t *testing.T) {
	root := t.TempDir()
	workspaceRoot := filepath.Join(root, "workspace")
	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir root: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(previousWD)
	})

	for _, name := range []string{".envrc", ".npmrc", ".netrc", "_netrc", ".pypirc", ".git-credentials", ".dockercfg"} {
		if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
			t.Fatalf("mkdir workspace: %v", err)
		}
		if err := os.WriteFile(filepath.Join(workspaceRoot, name), []byte(name+"=secret\n"), 0o600); err != nil {
			t.Fatalf("write credential rc file %s: %v", name, err)
		}
	}

	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var tree []map[string]any
	postGetJSON(t, ts.URL+"/api/files", &tree)
	for _, item := range tree {
		name, _ := item["name"].(string)
		for _, denied := range []string{".envrc", ".npmrc", ".netrc", "_netrc", ".pypirc", ".git-credentials", ".dockercfg"} {
			if name == denied {
				t.Fatalf("workspace listing leaked credential rc file %s: %#v", denied, tree)
			}
		}
	}
	for _, denied := range []string{".envrc", ".npmrc", ".netrc", "_netrc", ".pypirc", ".git-credentials", ".dockercfg"} {
		resp, err := http.Get(ts.URL + "/api/file/read?path=" + url.QueryEscape(denied))
		if err != nil {
			t.Fatalf("workspace credential rc read request %s: %v", denied, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected forbidden for workspace credential rc read %s, got %d body=%s", denied, resp.StatusCode, string(body))
		}
	}
}

func TestServiceWorkspaceRoutesRejectsPackageCredentialFiles(t *testing.T) {
	root := t.TempDir()
	workspaceRoot := filepath.Join(root, "workspace")
	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir root: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(previousWD)
	})

	credentialFiles := map[string]string{
		".yarnrc.yml":               "npmAuthToken: yarn-secret\n",
		".pnpmrc":                   "//registry.npmjs.org/:_authToken=pnpm-secret\n",
		".m2/settings.xml":          "<settings><servers><password>maven-secret</password></servers></settings>\n",
		".gradle/gradle.properties": "repoPassword=gradle-secret\n",
		".nuget/NuGet.Config":       "<configuration><packageSourceCredentials><add key=\"ClearTextPassword\" value=\"nuget-secret\" /></packageSourceCredentials></configuration>\n",
		".config/pip/pip.conf":      "[global]\nindex-url = https://user:pip-secret@example.invalid/simple\n",
	}
	for rel, content := range credentialFiles {
		path := filepath.Join(workspaceRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir package credential parent %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write package credential file %s: %v", rel, err)
		}
	}

	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var tree []map[string]any
	postGetJSON(t, ts.URL+"/api/files", &tree)
	for _, item := range tree {
		name, _ := item["name"].(string)
		for _, denied := range []string{".yarnrc.yml", ".pnpmrc"} {
			if name == denied {
				t.Fatalf("workspace listing leaked package credential file %s: %#v", denied, tree)
			}
		}
	}
	for dir, deniedName := range map[string]string{
		".m2":         "settings.xml",
		".gradle":     "gradle.properties",
		".nuget":      "NuGet.Config",
		".config/pip": "pip.conf",
	} {
		var nested []map[string]any
		postGetJSON(t, ts.URL+"/api/files?path="+url.QueryEscape(dir), &nested)
		for _, item := range nested {
			if item["name"] == deniedName {
				t.Fatalf("workspace listing leaked package credential file %s/%s: %#v", dir, deniedName, nested)
			}
		}
	}
	for denied := range credentialFiles {
		resp, err := http.Get(ts.URL + "/api/file/read?path=" + url.QueryEscape(denied))
		if err != nil {
			t.Fatalf("workspace package credential read request %s: %v", denied, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected forbidden for workspace package credential read %s, got %d body=%s", denied, resp.StatusCode, string(body))
		}
	}
}

func TestServiceWorkspaceRoutesRejectCredentialSymlinkRealTargets(t *testing.T) {
	root := t.TempDir()
	workspaceRoot := filepath.Join(root, "workspace")
	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir root: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(previousWD)
	})

	if err := os.MkdirAll(filepath.Join(workspaceRoot, "maven-real"), 0o755); err != nil {
		t.Fatalf("mkdir maven target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, "maven-real", "settings.xml"), []byte("<settings><password>maven-secret</password></settings>\n"), 0o600); err != nil {
		t.Fatalf("write maven target: %v", err)
	}
	if err := os.Symlink("maven-real", filepath.Join(workspaceRoot, ".m2")); err != nil {
		t.Fatalf("symlink .m2: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, "npm-real"), []byte("//registry.npmjs.org/:_authToken=npm-secret\n"), 0o600); err != nil {
		t.Fatalf("write npm target: %v", err)
	}
	if err := os.Symlink("npm-real", filepath.Join(workspaceRoot, ".npmrc")); err != nil {
		t.Fatalf("symlink .npmrc: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspaceRoot, "configs"), 0o755); err != nil {
		t.Fatalf("mkdir configs: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspaceRoot, "secrets"), 0o755); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, "secrets", "key-real"), []byte("private key material\n"), 0o600); err != nil {
		t.Fatalf("write private key target: %v", err)
	}
	if err := os.Symlink(filepath.Join("..", "secrets", "key-real"), filepath.Join(workspaceRoot, "configs", "deploy.pem")); err != nil {
		t.Fatalf("symlink nested deploy.pem: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, "secrets", "env-real"), []byte("TOKEN=secret\n"), 0o600); err != nil {
		t.Fatalf("write env target: %v", err)
	}
	if err := os.Symlink(filepath.Join("..", "secrets", "env-real"), filepath.Join(workspaceRoot, "configs", ".env")); err != nil {
		t.Fatalf("symlink nested .env: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspaceRoot, "secrets", "ssh-real"), 0o755); err != nil {
		t.Fatalf("mkdir ssh target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, "secrets", "ssh-real", "config"), []byte("Host secret\n"), 0o600); err != nil {
		t.Fatalf("write ssh config target: %v", err)
	}
	if err := os.Symlink(filepath.Join("..", "secrets", "ssh-real"), filepath.Join(workspaceRoot, "configs", ".ssh")); err != nil {
		t.Fatalf("symlink nested .ssh: %v", err)
	}

	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var mavenTree []map[string]any
	postGetJSON(t, ts.URL+"/api/files?path="+url.QueryEscape("maven-real"), &mavenTree)
	for _, item := range mavenTree {
		if item["name"] == "settings.xml" {
			t.Fatalf("workspace listing leaked package credential symlink target: %#v", mavenTree)
		}
	}

	var rootTree []map[string]any
	postGetJSON(t, ts.URL+"/api/files", &rootTree)
	for _, item := range rootTree {
		if item["name"] == "npm-real" {
			t.Fatalf("workspace listing leaked credential rc symlink target: %#v", rootTree)
		}
	}

	var secretsTree []map[string]any
	postGetJSON(t, ts.URL+"/api/files?path="+url.QueryEscape("secrets"), &secretsTree)
	for _, item := range secretsTree {
		if item["name"] == "key-real" {
			t.Fatalf("workspace listing leaked nested private key symlink target: %#v", secretsTree)
		}
		if item["name"] == "env-real" {
			t.Fatalf("workspace listing leaked nested env symlink target: %#v", secretsTree)
		}
	}

	sshListResp, err := http.Get(ts.URL + "/api/files?path=" + url.QueryEscape("secrets/ssh-real"))
	if err != nil {
		t.Fatalf("workspace nested ssh target list request: %v", err)
	}
	sshListBody, _ := io.ReadAll(sshListResp.Body)
	sshListResp.Body.Close()
	if sshListResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected forbidden for workspace nested ssh target listing, got %d body=%s", sshListResp.StatusCode, string(sshListBody))
	}

	for _, denied := range []string{"maven-real/settings.xml", "npm-real", "secrets/key-real", "secrets/env-real", "secrets/ssh-real/config"} {
		resp, err := http.Get(ts.URL + "/api/file/read?path=" + url.QueryEscape(denied))
		if err != nil {
			t.Fatalf("workspace credential symlink target read request %s: %v", denied, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected forbidden for workspace credential symlink target read %s, got %d body=%s", denied, resp.StatusCode, string(body))
		}
	}
}

func TestServiceWorkspaceRootIncludesParentNavigationWhenEmpty(t *testing.T) {
	root := t.TempDir()
	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir root: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(previousWD)
	})

	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var tree []map[string]any
	postGetJSON(t, ts.URL+"/api/files", &tree)
	if len(tree) != 1 || tree[0]["name"] != ".." || tree[0]["path"] != ".." || tree[0]["navigation"] != "parent" {
		t.Fatalf("expected empty workspace to expose parent navigation, got %#v", tree)
	}
}

func TestServiceMetaReportsDefaultWorkspaceSubdirOnly(t *testing.T) {
	root := t.TempDir()
	workspaceRoot := filepath.Join(root, "workspace")
	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir root: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(previousWD)
	})

	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var meta MetaResponse
	postGetJSON(t, ts.URL+"/api/meta", &meta)
	if meta.WorkspaceRoot != workspaceRoot {
		t.Fatalf("expected workspace root %q, got %#v", workspaceRoot, meta)
	}
	if info, err := os.Stat(workspaceRoot); err != nil || !info.IsDir() {
		t.Fatalf("expected workspace root to be created, info=%#v err=%v", info, err)
	}
	if meta.WorkspaceSwitchSupported {
		t.Fatalf("workspace root switching should not be advertised, got %#v", meta)
	}
}

func TestServiceMetaReportsWorkspaceRootError(t *testing.T) {
	root := t.TempDir()
	workspacePath := filepath.Join(root, "workspace")
	if err := os.WriteFile(workspacePath, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write workspace placeholder: %v", err)
	}
	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir root: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(previousWD)
	})

	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/meta")
	if err != nil {
		t.Fatalf("get meta: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("unexpected status %d want %d body=%s", resp.StatusCode, http.StatusInternalServerError, string(body))
	}
	if !strings.Contains(string(body), "default workspace path is not a directory") {
		t.Fatalf("expected workspace root error, got %s", string(body))
	}
}

func TestServiceWebSocketResetSessionDoesNotEmitDurableEcho(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]any{
		"type":      "reset_session",
		"sessionId": "0xRESET",
	}); err != nil {
		t.Fatalf("write websocket reset: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	var msg map[string]any
	err = conn.ReadJSON(&msg)
	if err == nil {
		t.Fatalf("expected no durable reset echo, got %#v", msg)
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("expected read timeout after reset, got %v", err)
	}
}

func TestServiceSkillRoutesUploadListUninstallAndInstallUnsupported(t *testing.T) {
	cfg := testConfig(t, "")
	skillsDir := filepath.Join(t.TempDir(), "skills")
	cfg.Skills.Dirs = []string{skillsDir}
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	zipPath := filepath.Join(t.TempDir(), "skill.zip")
	createSkillZip(t, zipPath, "demo-skill", "---\nname: demo-skill\ndescription: uploaded demo\n---\n")

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filepath.Base(zipPath))
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	zipBytes, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatalf("read zip: %v", err)
	}
	if _, err := part.Write(zipBytes); err != nil {
		t.Fatalf("write zip to multipart: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/skills/upload", body)
	if err != nil {
		t.Fatalf("new upload request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set(webMutationHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		uploadBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("unexpected upload status %d body=%s", resp.StatusCode, string(uploadBody))
	}

	var listed []map[string]any
	postGetJSON(t, ts.URL+"/api/skills", &listed)
	if len(listed) != 1 {
		t.Fatalf("expected one uploaded skill, got %#v", listed)
	}
	if listed[0]["id"] != "demo-skill" {
		t.Fatalf("unexpected listed skill: %#v", listed[0])
	}
	if listed[0]["read_only"] == true || listed[0]["disabled_reason"] != nil {
		t.Fatalf("managed uploaded skill should remain mutable, got %#v", listed[0])
	}

	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		req, err := http.NewRequest(method, ts.URL+"/api/skills/demo-skill/uninstall", nil)
		if err != nil {
			t.Fatalf("new %s uninstall request: %v", method, err)
		}
		if method != http.MethodGet {
			req.Header.Set(webMutationHeader, "1")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s uninstall request: %v", method, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("expected %s uninstall to be 405, got %d body=%s", method, resp.StatusCode, string(body))
		}
		if _, err := os.Stat(filepath.Join(skillsDir, "demo-skill")); err != nil {
			t.Fatalf("expected %s uninstall not to remove skill dir: %v", method, err)
		}
	}

	req, err = http.NewRequest(http.MethodPost, ts.URL+"/api/skills/demo-skill/install", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("new install request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(webMutationHeader, "1")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("install request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected install to be unsupported, got %d body=%s", resp.StatusCode, string(body))
	}

	req, err = http.NewRequest(http.MethodPost, ts.URL+"/api/skills/demo-skill/uninstall", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("new uninstall request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(webMutationHeader, "1")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("uninstall request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("unexpected uninstall status %d body=%s", resp.StatusCode, string(body))
	}

	postGetJSON(t, ts.URL+"/api/skills", &listed)
	if len(listed) != 0 {
		t.Fatalf("expected skill list to be empty after uninstall, got %#v", listed)
	}
}

func TestServiceSkillActionRoutesRejectNonJSONBodies(t *testing.T) {
	cfg := testConfig(t, "")
	skillsDir := filepath.Join(t.TempDir(), "skills")
	cfg.Skills.Dirs = []string{skillsDir}
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	skillDir := filepath.Join(skillsDir, "demo-skill")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatalf("create skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: demo-skill\n---\n"), 0o600); err != nil {
		t.Fatalf("write skill manifest: %v", err)
	}

	ts := httptest.NewServer(svc)
	defer ts.Close()

	for _, action := range []string{"install", "uninstall"} {
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/skills/demo-skill/"+action, strings.NewReader("not-json"))
		if err != nil {
			t.Fatalf("new %s request: %v", action, err)
		}
		req.Header.Set(webMutationHeader, "1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s request: %v", action, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected %s to reject non-JSON body, got %d body=%s", action, resp.StatusCode, string(body))
		}
		if !strings.Contains(string(body), "Content-Type: application/json") {
			t.Fatalf("expected %s response to mention JSON Content-Type, got %s", action, string(body))
		}
		if _, err := os.Stat(skillDir); err != nil {
			t.Fatalf("%s with rejected body must not remove skill dir: %v", action, err)
		}
	}
}

func TestServiceSkillUploadRejectsMissingBody(t *testing.T) {
	cfg := testConfig(t, "")
	cfg.Skills.Dirs = []string{filepath.Join(t.TempDir(), "skills")}
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/skills/upload", nil)
	req.Body = nil
	req.Header.Set("Content-Type", "multipart/form-data; boundary=missing-body")
	req.Header.Set(webMutationHeader, "1")
	recorder := httptest.NewRecorder()
	svc.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected missing upload body to be rejected, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "request body is required") {
		t.Fatalf("expected missing body error, got body=%s", recorder.Body.String())
	}
}

func TestSkillUploadUsesOriginalTempFileWhenPathIsReplaced(t *testing.T) {
	cfg := testConfig(t, "")
	skillsDir := filepath.Join(t.TempDir(), "skills")
	cfg.Skills.Dirs = []string{skillsDir}
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	replacementZip := filepath.Join(t.TempDir(), "replacement.zip")
	createSkillZip(t, replacementZip, "replacement-skill", "---\nname: replacement-skill\n---\nreplacement\n")

	beforeSkillUploadTempProcess = func(tmpPath string) error {
		if err := os.Remove(tmpPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		replacement, err := os.ReadFile(replacementZip)
		if err != nil {
			return err
		}
		return os.WriteFile(tmpPath, replacement, 0o600)
	}
	defer func() {
		beforeSkillUploadTempProcess = nil
	}()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	zipPath := filepath.Join(t.TempDir(), "skill.zip")
	createSkillZip(t, zipPath, "demo-skill", "---\nname: demo-skill\n---\noriginal\n")
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filepath.Base(zipPath))
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	zipBytes, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatalf("read zip: %v", err)
	}
	if _, err := part.Write(zipBytes); err != nil {
		t.Fatalf("write zip to multipart: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/skills/upload", body)
	if err != nil {
		t.Fatalf("new upload request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set(webMutationHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload request: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected upload status %d body=%s", resp.StatusCode, string(respBody))
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "demo-skill", "SKILL.md")); err != nil {
		t.Fatalf("expected original uploaded skill to be installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "replacement-skill", "SKILL.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement path payload should not be installed, got %v", err)
	}
}

func TestServiceSkillUninstallRejectsEncodedSeparatorInSkillID(t *testing.T) {
	cfg := testConfig(t, "")
	skillsDir := filepath.Join(t.TempDir(), "skills")
	cfg.Skills.Dirs = []string{skillsDir}
	skillDir := filepath.Join(skillsDir, "demo-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: demo-skill\n---\nbody\n"), 0o600); err != nil {
		t.Fatalf("write skill manifest: %v", err)
	}

	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/skills/demo-skill%2Funinstall", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(webMutationHeader, "1")
	recorder := httptest.NewRecorder()
	svc.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected encoded separator in skill id to be rejected, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "path separators and traversal") {
		t.Fatalf("expected malformed skill id error, got body=%s", recorder.Body.String())
	}
	if _, err := os.Stat(skillDir); err != nil {
		t.Fatalf("encoded separator route must not remove skill dir: %v", err)
	}
}

func TestServiceSkillUninstallRejectsEncodedSeparatorWhenPrefixIsEncoded(t *testing.T) {
	cfg := testConfig(t, "")
	skillsDir := filepath.Join(t.TempDir(), "skills")
	cfg.Skills.Dirs = []string{skillsDir}
	skillDir := filepath.Join(skillsDir, "demo-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: demo-skill\n---\nbody\n"), 0o600); err != nil {
		t.Fatalf("write skill manifest: %v", err)
	}

	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/%61pi/%73kills/demo-skill%2Funinstall", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(webMutationHeader, "1")
	recorder := httptest.NewRecorder()
	svc.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected encoded separator in skill id to be rejected, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(skillDir); err != nil {
		t.Fatalf("encoded-prefix route must not remove skill dir: %v", err)
	}
}

func TestServiceSkillListMarksNonManagedSkillDirsReadOnly(t *testing.T) {
	cfg := testConfig(t, "")
	managedDir := filepath.Join(t.TempDir(), "managed-skills")
	externalDir := filepath.Join(t.TempDir(), "external-skills")
	cfg.Skills.Dirs = []string{managedDir, externalDir}
	externalSkill := filepath.Join(externalDir, "external-skill")
	if err := os.MkdirAll(externalSkill, 0o755); err != nil {
		t.Fatalf("mkdir external skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(externalSkill, "SKILL.md"), []byte("---\nname: external-skill\ndescription: external skill\n---\nbody\n"), 0o600); err != nil {
		t.Fatalf("write external skill: %v", err)
	}

	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var listed []map[string]any
	postGetJSON(t, ts.URL+"/api/skills", &listed)
	if len(listed) != 1 {
		t.Fatalf("expected one external skill, got %#v", listed)
	}
	if listed[0]["id"] != "external-skill" || listed[0]["read_only"] != true {
		t.Fatalf("external skill should be listed read-only, got %#v", listed[0])
	}
	if listed[0]["disabled_reason"] == "" {
		t.Fatalf("external read-only skill should explain why uninstall is disabled, got %#v", listed[0])
	}
}

func TestServiceSkillListMarksUnmanageableManagedSkillIDsReadOnly(t *testing.T) {
	cfg := testConfig(t, "")
	skillsDir := filepath.Join(t.TempDir(), "skills")
	cfg.Skills.Dirs = []string{skillsDir}
	skillDir := filepath.Join(skillsDir, "demo.skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: demo.skill\ndescription: dot id skill\n---\nbody\n"), 0o600); err != nil {
		t.Fatalf("write skill manifest: %v", err)
	}

	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var listed []map[string]any
	postGetJSON(t, ts.URL+"/api/skills", &listed)
	if len(listed) != 1 {
		t.Fatalf("expected one installed skill, got %#v", listed)
	}
	if listed[0]["id"] != "demo.skill" {
		t.Fatalf("unexpected listed skill: %#v", listed[0])
	}
	if listed[0]["read_only"] != true {
		t.Fatalf("unmanageable managed-root skill id should be read-only, got %#v", listed[0])
	}
	if listed[0]["disabled_reason"] == "" {
		t.Fatalf("unmanageable managed-root skill should explain why uninstall is disabled, got %#v", listed[0])
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/skills/demo.skill/uninstall", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("new uninstall request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(webMutationHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("uninstall request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected direct uninstall to reject unmanageable id, got %d body=%s", resp.StatusCode, string(body))
	}
}

func TestServiceSkillRoutesUseFirstNonBlankManagedSkillDir(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	workdir := t.TempDir()
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	cfg := testConfig(t, "")
	managedDir := filepath.Join(workdir, "managed-skills")
	externalDir := filepath.Join(workdir, "external-skills")
	cfg.Skills.Dirs = []string{"", managedDir, externalDir}
	managedSkill := filepath.Join(managedDir, "managed-skill")
	externalSkill := filepath.Join(externalDir, "external-skill")
	if err := os.MkdirAll(managedSkill, 0o755); err != nil {
		t.Fatalf("mkdir managed skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(managedSkill, "SKILL.md"), []byte("---\nname: managed-skill\ndescription: managed skill\n---\nbody\n"), 0o600); err != nil {
		t.Fatalf("write managed skill: %v", err)
	}
	if err := os.MkdirAll(externalSkill, 0o755); err != nil {
		t.Fatalf("mkdir external skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(externalSkill, "SKILL.md"), []byte("---\nname: external-skill\ndescription: external skill\n---\nbody\n"), 0o600); err != nil {
		t.Fatalf("write external skill: %v", err)
	}

	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	ts := httptest.NewServer(svc)
	defer ts.Close()

	var listed []map[string]any
	postGetJSON(t, ts.URL+"/api/skills", &listed)
	byID := map[string]map[string]any{}
	for _, item := range listed {
		if id, ok := item["id"].(string); ok {
			byID[id] = item
		}
	}
	if byID["managed-skill"] == nil || byID["external-skill"] == nil {
		t.Fatalf("expected managed and external skill cards, got %#v", listed)
	}
	if byID["managed-skill"]["read_only"] == true || byID["managed-skill"]["disabled_reason"] != nil {
		t.Fatalf("first non-blank skill dir should be managed, got %#v", byID["managed-skill"])
	}
	if byID["external-skill"]["read_only"] != true || byID["external-skill"]["disabled_reason"] == "" {
		t.Fatalf("later skill dir should be read-only, got %#v", byID["external-skill"])
	}

	zipPath := filepath.Join(t.TempDir(), "skill.zip")
	createSkillZip(t, zipPath, "uploaded-skill", "---\nname: uploaded-skill\ndescription: uploaded\n---\nbody\n")
	zipBytes, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatalf("read zip: %v", err)
	}
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filepath.Base(zipPath))
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(zipBytes); err != nil {
		t.Fatalf("write zip to multipart: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/skills/upload", body)
	if err != nil {
		t.Fatalf("new upload request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set(webMutationHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload request: %v", err)
	}
	uploadBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected upload status %d body=%s", resp.StatusCode, string(uploadBody))
	}
	if _, err := os.Stat(filepath.Join(managedDir, "uploaded-skill", "SKILL.md")); err != nil {
		t.Fatalf("uploaded skill should install under first non-blank managed dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workdir, "uploaded-skill")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("blank skill dir entry must not install under server cwd, got err=%v", err)
	}

	postJSON(t, ts.URL+"/api/skills/managed-skill/uninstall", map[string]any{}, http.StatusOK, nil)
	if _, err := os.Stat(managedSkill); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed skill should be uninstalled from first non-blank managed dir, got err=%v", err)
	}
}

func TestServiceSkillListUsesCanonicalManagedSkillDir(t *testing.T) {
	cfg := testConfig(t, "")
	base := t.TempDir()
	realRoot := filepath.Join(base, "real-root")
	linkRoot := filepath.Join(base, "link-root")
	skillsDir := filepath.Join(realRoot, "skills")
	skillDir := filepath.Join(skillsDir, "demo-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: demo-skill\ndescription: canonical managed skill\n---\nbody\n"), 0o600); err != nil {
		t.Fatalf("write skill manifest: %v", err)
	}
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	cfg.Skills.Dirs = []string{filepath.Join(linkRoot, "skills")}

	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	ts := httptest.NewServer(svc)
	defer ts.Close()

	var listed []map[string]any
	postGetJSON(t, ts.URL+"/api/skills", &listed)
	if len(listed) != 1 {
		t.Fatalf("expected one managed skill, got %#v", listed)
	}
	if listed[0]["id"] != "demo-skill" {
		t.Fatalf("unexpected listed skill: %#v", listed[0])
	}
	if listed[0]["read_only"] == true || listed[0]["disabled_reason"] != nil {
		t.Fatalf("canonical managed skill should remain mutable, got %#v", listed[0])
	}
	if listed[0]["discovery_path"] != filepath.Clean(skillsDir) {
		t.Fatalf("expected canonical discovery path %s, got %#v", filepath.Clean(skillsDir), listed[0])
	}
}

func TestServiceSkillListUsesCanonicalReadOnlySkillDir(t *testing.T) {
	cfg := testConfig(t, "")
	base := t.TempDir()
	managedDir := filepath.Join(base, "managed-skills")
	realRoot := filepath.Join(base, "real-root")
	linkRoot := filepath.Join(base, "link-root")
	externalDir := filepath.Join(realRoot, "external-skills")
	externalSkill := filepath.Join(externalDir, "external-skill")
	if err := os.MkdirAll(externalSkill, 0o755); err != nil {
		t.Fatalf("mkdir external skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(externalSkill, "SKILL.md"), []byte("---\nname: external-skill\ndescription: canonical read-only skill\n---\nbody\n"), 0o600); err != nil {
		t.Fatalf("write external skill manifest: %v", err)
	}
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	cfg.Skills.Dirs = []string{managedDir, filepath.Join(linkRoot, "external-skills")}

	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	ts := httptest.NewServer(svc)
	defer ts.Close()

	var listed []map[string]any
	postGetJSON(t, ts.URL+"/api/skills", &listed)
	if len(listed) != 1 {
		t.Fatalf("expected one external skill, got %#v", listed)
	}
	if listed[0]["id"] != "external-skill" {
		t.Fatalf("unexpected listed skill: %#v", listed[0])
	}
	if listed[0]["read_only"] != true || listed[0]["disabled_reason"] == "" {
		t.Fatalf("external skill should remain read-only, got %#v", listed[0])
	}
	if listed[0]["discovery_path"] != filepath.Clean(externalDir) {
		t.Fatalf("expected canonical discovery path %s, got %#v", filepath.Clean(externalDir), listed[0])
	}
}

func TestServiceSkillListRequiresReadableSkillManifest(t *testing.T) {
	cfg := testConfig(t, "")
	skillsDir := filepath.Join(t.TempDir(), "skills")
	cfg.Skills.Dirs = []string{skillsDir}
	good := filepath.Join(skillsDir, "good-skill")
	notSkill := filepath.Join(skillsDir, "notes")
	if err := os.MkdirAll(good, 0o755); err != nil {
		t.Fatalf("mkdir good skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(good, "SKILL.md"), []byte("---\nname: good-skill\ndescription: good skill\n---\nbody\n"), 0o600); err != nil {
		t.Fatalf("write good skill: %v", err)
	}
	if err := os.MkdirAll(notSkill, 0o755); err != nil {
		t.Fatalf("mkdir non-skill dir: %v", err)
	}

	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var listed []map[string]any
	postGetJSON(t, ts.URL+"/api/skills", &listed)
	if len(listed) != 1 || listed[0]["id"] != "good-skill" {
		t.Fatalf("skill list should only include directories with readable SKILL.md, got %#v", listed)
	}

	symlinked := filepath.Join(skillsDir, "symlinked-skill")
	if err := os.MkdirAll(symlinked, 0o755); err != nil {
		t.Fatalf("mkdir symlinked skill: %v", err)
	}
	outsideManifest := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(outsideManifest, []byte("---\nname: symlinked-skill\n---\n"), 0o600); err != nil {
		t.Fatalf("write outside manifest: %v", err)
	}
	if err := os.Symlink(outsideManifest, filepath.Join(symlinked, "SKILL.md")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	resp, err := http.Get(ts.URL + "/api/skills")
	if err != nil {
		t.Fatalf("get skills with symlinked manifest: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected unreadable skill manifest to return 500, got %d body=%s", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), "SKILL.md") {
		t.Fatalf("expected skill manifest error to name SKILL.md, got %s", string(body))
	}
}

func TestSkillUploadRejectsMalformedPackageAsBadRequest(t *testing.T) {
	cfg := testConfig(t, "")
	skillsDir := filepath.Join(t.TempDir(), "skills")
	cfg.Skills.Dirs = []string{skillsDir}
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	zipPath := filepath.Join(t.TempDir(), "not-a-skill.zip")
	createZipEntries(t, zipPath, map[string]string{
		"README.md": "not a skill package\n",
	})
	zipBytes, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatalf("read zip: %v", err)
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filepath.Base(zipPath))
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(zipBytes); err != nil {
		t.Fatalf("write zip to multipart: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/skills/upload", body)
	if err != nil {
		t.Fatalf("new upload request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set(webMutationHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload request: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(respBody), "no SKILL.md found") {
		t.Fatalf("expected malformed package to return 400 with validation error, got status=%d body=%s", resp.StatusCode, string(respBody))
	}
	if entries, err := os.ReadDir(skillsDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read skills dir after malformed upload: %v", err)
	} else if len(entries) != 0 {
		t.Fatalf("malformed upload should not install skills, got %d entries", len(entries))
	}
	if data, err := os.ReadFile(webAuditLogPath(cfg.Session.Dir)); err == nil && strings.Contains(string(data), "web.skill.install") {
		t.Fatalf("malformed upload should not append install audit event, got %q", string(data))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read audit log: %v", err)
	}
}

func TestServiceSkillUninstallRejectsSymlinkedSkillDir(t *testing.T) {
	cfg := testConfig(t, "")
	base := t.TempDir()
	skillsDir := filepath.Join(base, "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatalf("mkdir skills: %v", err)
	}
	outside := filepath.Join(base, "outside-skill")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "SKILL.md"), []byte("---\nname: demo-skill\n---\n"), 0o600); err != nil {
		t.Fatalf("write outside skill: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(skillsDir, "demo-skill")); err != nil {
		t.Fatalf("symlink skill dir: %v", err)
	}
	cfg.Skills.Dirs = []string{skillsDir}
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/skills/demo-skill/uninstall", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("new uninstall request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(webMutationHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("uninstall request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("expected symlinked uninstall to be rejected, got %d body=%s", resp.StatusCode, string(body))
	}
	if _, err := os.Lstat(filepath.Join(skillsDir, "demo-skill")); err != nil {
		t.Fatalf("expected symlink to remain, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "SKILL.md")); err != nil {
		t.Fatalf("expected outside skill to remain, got %v", err)
	}
}

func TestServiceSkillUninstallRejectsSymlinkedSkillManifest(t *testing.T) {
	cfg := testConfig(t, "")
	base := t.TempDir()
	skillsDir := filepath.Join(base, "skills")
	skillDir := filepath.Join(skillsDir, "demo-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	outsideManifest := filepath.Join(base, "outside-SKILL.md")
	if err := os.WriteFile(outsideManifest, []byte("---\nname: demo-skill\n---\n"), 0o600); err != nil {
		t.Fatalf("write outside manifest: %v", err)
	}
	if err := os.Symlink(outsideManifest, filepath.Join(skillDir, "SKILL.md")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	cfg.Skills.Dirs = []string{skillsDir}
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/skills/demo-skill/uninstall", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("new uninstall request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(webMutationHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("uninstall request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("expected symlinked manifest uninstall to be rejected, got %d body=%s", resp.StatusCode, string(body))
	}
	if _, err := os.Lstat(skillDir); err != nil {
		t.Fatalf("expected skill directory to remain, got %v", err)
	}
	if _, err := os.Lstat(filepath.Join(skillDir, "SKILL.md")); err != nil {
		t.Fatalf("expected symlinked manifest to remain, got %v", err)
	}
	if _, err := os.Stat(outsideManifest); err != nil {
		t.Fatalf("expected outside manifest to remain, got %v", err)
	}
	if data, err := os.ReadFile(webAuditLogPath(cfg.Session.Dir)); err == nil && strings.Contains(string(data), "web.skill.uninstall") {
		t.Fatalf("rejected symlinked manifest must not append uninstall audit event, got %q", string(data))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read audit log: %v", err)
	}
}

func TestProcessSkillZipRejectsTraversalEntries(t *testing.T) {
	for _, entryName := range []string{"../../zip-slip.txt", "..\\escape.txt", "/absolute.txt", "C:/absolute.txt"} {
		t.Run(entryName, func(t *testing.T) {
			base := t.TempDir()
			dest := filepath.Join(base, "skills")
			zipPath := filepath.Join(base, "skill.zip")
			createZipEntries(t, zipPath, map[string]string{
				"SKILL.md":  "---\nname: demo\n---\n",
				entryName:   "escaped",
				"notes.txt": "ok",
			})

			if _, err := processSkillZip(zipPath, dest); err == nil {
				t.Fatal("expected traversal zip to be rejected")
			}
			for _, candidate := range []string{
				filepath.Join(base, "zip-slip.txt"),
				filepath.Join(base, "escape.txt"),
				filepath.Join(dest, "absolute.txt"),
			} {
				if _, err := os.Stat(candidate); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("expected %s to remain absent, got %v", candidate, err)
				}
			}
		})
	}
}

func TestProcessSkillZipRejectsSymlinkDestination(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	dest := filepath.Join(base, "skills-link")
	if err := os.Symlink(outside, dest); err != nil {
		t.Fatalf("symlink destination: %v", err)
	}
	zipPath := filepath.Join(base, "skill.zip")
	createSkillZip(t, zipPath, "demo-skill", "---\nname: demo-skill\n---\nbody\n")

	if _, err := processSkillZip(zipPath, dest); err == nil {
		t.Fatal("expected symlinked skill destination to be rejected")
	}
	if _, err := os.Stat(filepath.Join(outside, "demo-skill", "SKILL.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected outside skill not to be written, got %v", err)
	}
}

func TestProcessSkillZipRejectsSymlinkEntriesBeforeMutation(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "skills")
	zipPath := filepath.Join(base, "skill-symlink-entry.zip")
	createZipEntriesInOrder(t, zipPath, []zipTestEntry{
		{name: "demo-skill/SKILL.md", content: "real-manifest.md", mode: os.ModeSymlink | 0o777},
		{name: "demo-skill/real-manifest.md", content: "---\nname: demo-skill\n---\nbody\n"},
	})

	if _, err := processSkillZip(zipPath, dest); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink zip entry to be rejected, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "demo-skill", "SKILL.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink entry should not be installed as a regular file, got %v", err)
	}
}

func TestProcessSkillZipRejectsNonRegularEntriesBeforeMutation(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "skills")
	zipPath := filepath.Join(base, "skill-nonregular-entry.zip")
	createZipEntriesInOrder(t, zipPath, []zipTestEntry{
		{name: "demo-skill/SKILL.md", content: "---\nname: demo-skill\n---\nbody\n"},
		{name: "demo-skill/tools/fifo", content: "payload\n", mode: os.ModeNamedPipe | 0o644},
	})

	if _, err := processSkillZip(zipPath, dest); err == nil || !strings.Contains(err.Error(), "non-regular") {
		t.Fatalf("expected non-regular zip entry to be rejected, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "demo-skill", "tools", "fifo")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("non-regular entry should not be installed as a regular file, got %v", err)
	}
}

func TestProcessSkillZipRejectsDirectoryManifestEntriesBeforeMutation(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "skills")
	zipPath := filepath.Join(base, "skill-directory-manifest.zip")
	createZipEntriesInOrder(t, zipPath, []zipTestEntry{
		{name: "demo-skill/SKILL.md/", mode: os.ModeDir | 0o755},
		{name: "demo-skill/references/a.md", content: "reference\n"},
	})

	if _, err := processSkillZip(zipPath, dest); err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("expected directory manifest entry to be rejected, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "demo-skill")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("directory manifest upload should not install a skill, got %v", err)
	}
}

func TestProcessSkillZipRejectsSymlinkedManagedRootBeforeCommit(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "skills")
	existing := filepath.Join(dest, "demo-skill")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatalf("mkdir existing skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(existing, "SKILL.md"), []byte("---\nname: existing-demo\n---\nbody\n"), 0o600); err != nil {
		t.Fatalf("write existing skill: %v", err)
	}
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	zipPath := filepath.Join(base, "skill.zip")
	createSkillZip(t, zipPath, "demo-skill", "---\nname: demo-skill\n---\nnew body\n")

	tx, err := processSkillZipTransaction(zipPath, dest)
	if err != nil {
		t.Fatalf("process skill zip transaction: %v", err)
	}
	if len(tx.committed) != 1 || tx.committed[0].backupPath == "" {
		t.Fatalf("expected committed backup path, got %#v", tx.committed)
	}
	outsideBackup := filepath.Join(outside, filepath.Base(tx.committed[0].backupPath))
	if err := os.MkdirAll(outsideBackup, 0o755); err != nil {
		t.Fatalf("mkdir outside backup alias: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outsideBackup, "SKILL.md"), []byte("---\nname: outside-alias\n---\nbody\n"), 0o600); err != nil {
		t.Fatalf("write outside backup alias: %v", err)
	}
	if err := os.Rename(dest, dest+".real"); err != nil {
		t.Fatalf("move managed skill root: %v", err)
	}
	if err := os.Symlink(outside, dest); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	err = tx.Rollback()
	if err == nil {
		t.Fatal("expected rollback to reject symlinked managed skill root")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink path error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "demo-skill", "SKILL.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback should not restore skill through symlinked managed root, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(outsideBackup, "SKILL.md")); err != nil {
		t.Fatalf("outside backup alias should remain untouched, got %v", err)
	}
	realBackup := filepath.Join(dest+".real", filepath.Base(tx.committed[0].backupPath))
	if data, err := os.ReadFile(filepath.Join(realBackup, "SKILL.md")); err != nil || !strings.Contains(string(data), "existing-demo") {
		t.Fatalf("original managed root backup should retain existing skill, data=%q err=%v", string(data), err)
	}
}

func TestProcessSkillZipRejectsSymlinkedManagedRootBeforeStaging(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "skills")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("mkdir skills: %v", err)
	}
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	symlinkProbe := filepath.Join(base, "symlink-probe")
	if err := os.Symlink(outside, symlinkProbe); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.Remove(symlinkProbe); err != nil {
		t.Fatalf("remove symlink probe: %v", err)
	}

	zipPath := filepath.Join(base, "skill.zip")
	createSkillZip(t, zipPath, "demo-skill", "---\nname: demo-skill\n---\nbody\n")

	beforeSkillUploadStagingRootCreate = func(globalDest string) error {
		if globalDest != dest {
			return fmt.Errorf("unexpected skill destination: %s", globalDest)
		}
		if err := os.Rename(dest, dest+".real"); err != nil {
			return err
		}
		return os.Symlink(outside, dest)
	}
	defer func() {
		beforeSkillUploadStagingRootCreate = nil
	}()

	if _, err := processSkillZip(zipPath, dest); err == nil {
		t.Fatal("expected symlinked managed root to be rejected before staging")
	} else if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink path error, got %v", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatalf("read outside dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".skill-upload-") {
			t.Fatalf("upload staging must not be created through symlinked managed root, found %s", entry.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "demo-skill", "SKILL.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected outside skill not to be written, got %v", err)
	}
	if _, err := os.Stat(dest + ".real"); err != nil {
		t.Fatalf("real managed root should remain available, got %v", err)
	}
}

func TestReserveSkillBackupPathRejectsSymlinkedParentDuringCleanup(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "skills")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir skill parent: %v", err)
	}
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	symlinkProbe := filepath.Join(base, "symlink-probe")
	if err := os.Symlink(outside, symlinkProbe); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.Remove(symlinkProbe); err != nil {
		t.Fatalf("remove symlink probe: %v", err)
	}

	var originalBackupPath string
	var outsideBackupPath string
	beforeReserveSkillBackupCleanup = func(backupPath string) error {
		originalBackupPath = backupPath
		outsideBackupPath = filepath.Join(outside, filepath.Base(backupPath))
		if err := os.MkdirAll(outsideBackupPath, 0o755); err != nil {
			return err
		}
		if err := os.Rename(parent, parent+".real"); err != nil {
			return err
		}
		return os.Symlink(outside, parent)
	}
	defer func() {
		beforeReserveSkillBackupCleanup = nil
	}()

	backupPath, err := reserveSkillBackupPath(parent, "demo-skill")
	if err == nil {
		t.Fatalf("expected symlinked backup parent cleanup to fail, got backup path %s", backupPath)
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink path error, got %v", err)
	}
	if outsideBackupPath == "" {
		t.Fatal("test hook did not record outside backup path")
	}
	if _, err := os.Stat(outsideBackupPath); err != nil {
		t.Fatalf("outside backup alias should remain untouched, got %v", err)
	}
	realBackupPath := filepath.Join(parent+".real", filepath.Base(originalBackupPath))
	if _, err := os.Stat(realBackupPath); err != nil {
		t.Fatalf("original reservation under real parent should remain after failed cleanup, got %v", err)
	}
}

func TestReserveSkillBackupPathRejectsSymlinkedParentBeforeCreate(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "skills")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := os.Symlink(outside, parent); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	backupPath, err := reserveSkillBackupPath(parent, "demo-skill")
	if err == nil {
		t.Fatalf("expected symlinked backup parent to fail, got %s", backupPath)
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink path error, got %v", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatalf("read outside dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".demo-skill.backup-") {
			t.Fatalf("backup reservation must not be created through symlinked parent, found %s", entry.Name())
		}
	}
}

func TestProcessSkillZipRejectsOversizedEntry(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "skills")
	zipPath := filepath.Join(base, "skill.zip")
	createZipEntries(t, zipPath, map[string]string{
		"demo-skill/SKILL.md": "---\nname: demo-skill\n---\nbody\n",
		"demo-skill/huge.txt": strings.Repeat("x", maxSkillZipEntryBytes+1),
	})

	if _, err := processSkillZip(zipPath, dest); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected oversized skill zip entry to be rejected, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "demo-skill", "huge.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected oversized entry not to be extracted, got %v", err)
	}
}

func TestProcessSkillZipRejectsDuplicateTargetNamesBeforeMutation(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "skills")
	existing := filepath.Join(dest, "existing-skill")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatalf("mkdir existing skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(existing, "SKILL.md"), []byte("---\nname: existing-skill\n---\nbody\n"), 0o600); err != nil {
		t.Fatalf("write existing skill: %v", err)
	}
	zipPath := filepath.Join(base, "duplicate-name.zip")
	createZipEntries(t, zipPath, map[string]string{
		"one/SKILL.md": "---\nname: demo!\n---\none\n",
		"one/a.txt":    "one\n",
		"two/SKILL.md": "---\nname: demo?\n---\ntwo\n",
		"two/b.txt":    "two\n",
	})

	if _, err := processSkillZip(zipPath, dest); err == nil || !strings.Contains(err.Error(), "duplicate skill target directory demo_") {
		t.Fatalf("expected duplicate target name to be rejected, got %v", err)
	}
	for _, candidate := range []string{
		filepath.Join(dest, "demo_", "a.txt"),
		filepath.Join(dest, "demo_", "b.txt"),
	} {
		if _, err := os.Stat(candidate); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected %s not to be extracted, got %v", candidate, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(existing, "SKILL.md"))
	if err != nil {
		t.Fatalf("expected existing skill to remain after rejected upload: %v", err)
	}
	if !strings.Contains(string(data), "existing-skill") {
		t.Fatalf("existing skill was unexpectedly modified: %q", string(data))
	}
}

func TestProcessSkillZipRejectsFileDirectoryConflictBeforeMutation(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "skills")
	existing := filepath.Join(dest, "demo-skill")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatalf("mkdir existing skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(existing, "SKILL.md"), []byte("---\nname: existing-demo\n---\nbody\n"), 0o600); err != nil {
		t.Fatalf("write existing skill: %v", err)
	}
	zipPath := filepath.Join(base, "file-directory-conflict.zip")
	createZipEntriesInOrder(t, zipPath, []zipTestEntry{
		{name: "demo-skill/SKILL.md", content: "---\nname: demo-skill\n---\nbody\n"},
		{name: "demo-skill/conflict", content: "file first\n"},
		{name: "demo-skill/conflict/child.txt", content: "child\n"},
	})

	if _, err := processSkillZip(zipPath, dest); err == nil || !strings.Contains(err.Error(), "path conflict") {
		t.Fatalf("expected file/directory path conflict to be rejected, got %v", err)
	}
	data, err := os.ReadFile(filepath.Join(existing, "SKILL.md"))
	if err != nil {
		t.Fatalf("expected existing skill to remain after rejected upload: %v", err)
	}
	if !strings.Contains(string(data), "existing-demo") {
		t.Fatalf("existing skill was unexpectedly modified: %q", string(data))
	}
	if _, err := os.Stat(filepath.Join(existing, "conflict")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("conflicting entry should not be extracted, got %v", err)
	}
}

func TestProcessSkillZipRejectsDuplicateNormalizedEntryPathsBeforeMutation(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "skills")
	existing := filepath.Join(dest, "demo-skill")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatalf("mkdir existing skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(existing, "SKILL.md"), []byte("---\nname: existing-demo\n---\nbody\n"), 0o600); err != nil {
		t.Fatalf("write existing skill: %v", err)
	}
	zipPath := filepath.Join(base, "duplicate-normalized-path.zip")
	createZipEntriesInOrder(t, zipPath, []zipTestEntry{
		{name: "demo-skill/SKILL.md", content: "---\nname: demo-skill\n---\nbody\n"},
		{name: "demo-skill/tools/run.sh", content: "first\n"},
		{name: "demo-skill/tools/./run.sh", content: "second\n"},
	})

	if _, err := processSkillZip(zipPath, dest); err == nil || !strings.Contains(err.Error(), "duplicate path") {
		t.Fatalf("expected duplicate normalized path to be rejected, got %v", err)
	}
	data, err := os.ReadFile(filepath.Join(existing, "SKILL.md"))
	if err != nil {
		t.Fatalf("expected existing skill to remain after rejected upload: %v", err)
	}
	if !strings.Contains(string(data), "existing-demo") {
		t.Fatalf("existing skill was unexpectedly modified: %q", string(data))
	}
	if _, err := os.Stat(filepath.Join(existing, "tools", "run.sh")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("duplicate normalized entry should not be extracted, got %v", err)
	}
}

func TestProcessSkillZipRejectsNestedSkillRootsBeforeMutation(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "skills")
	existing := filepath.Join(dest, "outer-skill")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatalf("mkdir existing skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(existing, "SKILL.md"), []byte("---\nname: outer-skill\n---\nexisting body\n"), 0o600); err != nil {
		t.Fatalf("write existing skill: %v", err)
	}
	zipPath := filepath.Join(base, "nested-skill-roots.zip")
	createZipEntriesInOrder(t, zipPath, []zipTestEntry{
		{name: "outer/SKILL.md", content: "---\nname: outer-skill\n---\nouter body\n"},
		{name: "outer/tools/run.sh", content: "#!/bin/sh\n"},
		{name: "outer/inner/SKILL.md", content: "---\nname: inner-skill\n---\ninner body\n"},
	})

	if _, err := processSkillZip(zipPath, dest); err == nil || !strings.Contains(err.Error(), "nested skill root") {
		t.Fatalf("expected nested skill roots to be rejected, got %v", err)
	}
	data, err := os.ReadFile(filepath.Join(existing, "SKILL.md"))
	if err != nil {
		t.Fatalf("existing skill should remain after rejected upload: %v", err)
	}
	if !strings.Contains(string(data), "existing body") {
		t.Fatalf("existing skill was unexpectedly modified: %q", string(data))
	}
	if _, err := os.Stat(filepath.Join(dest, "inner-skill")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inner skill should not be installed after rejected upload, got %v", err)
	}
	if catalog, err := skillcatalog.Scan([]string{dest}); err != nil {
		t.Fatalf("rejected upload must leave runtime skill catalog scannable, got catalog=%#v err=%v", catalog, err)
	}
}

func TestProcessSkillZipAllowsSiblingSkillRoots(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "skills")
	zipPath := filepath.Join(base, "sibling-skill-roots.zip")
	createZipEntriesInOrder(t, zipPath, []zipTestEntry{
		{name: "one/SKILL.md", content: "---\nname: one-skill\n---\none body\n"},
		{name: "one/references/a.md", content: "one reference\n"},
		{name: "two/SKILL.md", content: "---\nname: two-skill\n---\ntwo body\n"},
		{name: "two/references/b.md", content: "two reference\n"},
	})

	count, err := processSkillZip(zipPath, dest)
	if err != nil {
		t.Fatalf("process sibling skill roots: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected two installed skills, got %d", count)
	}
	if _, err := os.Stat(filepath.Join(dest, "one-skill", "references", "a.md")); err != nil {
		t.Fatalf("expected first sibling skill reference: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "two-skill", "references", "b.md")); err != nil {
		t.Fatalf("expected second sibling skill reference: %v", err)
	}
	if catalog, err := skillcatalog.Scan([]string{dest}); err != nil {
		t.Fatalf("sibling roots should leave runtime skill catalog scannable, got catalog=%#v err=%v", catalog, err)
	}
}

func TestProcessSkillZipSanitizesUploadedFileModes(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "skills")
	zipPath := filepath.Join(base, "skill.zip")
	createZipEntriesInOrder(t, zipPath, []zipTestEntry{
		{name: "demo-skill/SKILL.md", content: "---\nname: demo-skill\n---\nbody\n", mode: 0o666},
		{name: "demo-skill/tools/run.sh", content: "#!/bin/sh\n", mode: 0o777},
	})

	if _, err := processSkillZip(zipPath, dest); err != nil {
		t.Fatalf("process skill zip: %v", err)
	}
	for _, path := range []string{
		filepath.Join(dest, "demo-skill"),
		filepath.Join(dest, "demo-skill", "tools"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat uploaded directory %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got&0o077 != 0 {
			t.Fatalf("uploaded directory %s should not retain group/other permissions, got %03o", path, got)
		}
	}
	manifestInfo, err := os.Stat(filepath.Join(dest, "demo-skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("stat uploaded manifest: %v", err)
	}
	if got := manifestInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("uploaded manifest should be owner read/write only, got %03o", got)
	}
	scriptInfo, err := os.Stat(filepath.Join(dest, "demo-skill", "tools", "run.sh"))
	if err != nil {
		t.Fatalf("stat uploaded executable: %v", err)
	}
	if got := scriptInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("uploaded executable should preserve owner execute only, got %03o", got)
	}
}

func TestProcessSkillZipPreservesExistingSkillOnLateExtractionError(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "skills")
	existing := filepath.Join(dest, "demo-skill")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatalf("mkdir existing skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(existing, "SKILL.md"), []byte("---\nname: existing-demo\n---\nbody\n"), 0o600); err != nil {
		t.Fatalf("write existing skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(existing, "keep.txt"), []byte("keep me\n"), 0o600); err != nil {
		t.Fatalf("write existing skill payload: %v", err)
	}
	zipPath := filepath.Join(base, "corrupt-late.zip")
	createCorruptStoredZip(t, zipPath, []zipTestEntry{
		{name: "demo-skill/SKILL.md", content: "---\nname: demo-skill\n---\nbody\n"},
		{name: "demo-skill/good.txt", content: "new content\n"},
		{name: "demo-skill/bad.txt", content: "late payload"},
	}, "late payload", "late payloae")

	if _, err := processSkillZip(zipPath, dest); err == nil {
		t.Fatal("expected corrupt zip payload to fail during extraction")
	}
	data, err := os.ReadFile(filepath.Join(existing, "SKILL.md"))
	if err != nil {
		t.Fatalf("existing skill should remain after failed extraction: %v", err)
	}
	if !strings.Contains(string(data), "existing-demo") {
		t.Fatalf("existing skill was unexpectedly replaced: %q", string(data))
	}
	keepData, err := os.ReadFile(filepath.Join(existing, "keep.txt"))
	if err != nil {
		t.Fatalf("existing payload should remain after failed extraction: %v", err)
	}
	if string(keepData) != "keep me\n" {
		t.Fatalf("existing payload was unexpectedly modified: %q", keepData)
	}
	if _, err := os.Stat(filepath.Join(existing, "good.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed upload should not leave partial extracted file, got %v", err)
	}
}

func TestProcessSkillZipAllowsNestedSkillFiles(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "skills")
	zipPath := filepath.Join(base, "skill.zip")
	createZipEntries(t, zipPath, map[string]string{
		"demo-skill/SKILL.md":        "---\nname: demo-skill\n---\nbody\n",
		"demo-skill/references/a.md": "reference\n",
	})

	count, err := processSkillZip(zipPath, dest)
	if err != nil {
		t.Fatalf("process skill zip: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one skill, got %d", count)
	}
	data, err := os.ReadFile(filepath.Join(dest, "demo-skill", "references", "a.md"))
	if err != nil {
		t.Fatalf("read nested reference: %v", err)
	}
	if string(data) != "reference\n" {
		t.Fatalf("unexpected reference content %q", string(data))
	}
}

func TestListSkillsReportsWorkspaceExtensionTrustStatus(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, ".agent", "reviewer"), 0o700); err != nil {
		t.Fatalf("mkdir extension: %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	cfg := testConfig(t, "")
	cfg.Skills.Dirs = []string{filepath.Join(t.TempDir(), "skills")}
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	var listed []map[string]any
	postGetJSON(t, ts.URL+"/api/skills", &listed)
	if len(listed) != 1 {
		t.Fatalf("expected one extension card, got %#v", listed)
	}
	item := listed[0]
	if item["id"] != "workspace/reviewer" || item["read_only"] != true || item["trust"] != "untrusted" {
		t.Fatalf("unexpected extension trust card: %#v", item)
	}
	if item["disabled_reason"] == "" || item["extension_path"] == "" || item["discovery_path"] != filepath.Join(workdir, ".agent") {
		t.Fatalf("expected extension trust details, got %#v", item)
	}
}

func TestListSkillsReportsWorkspaceExtensionDiscoveryError(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, ".agent"), []byte("not a directory\n"), 0o600); err != nil {
		t.Fatalf("write malformed .agent path: %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	ts := httptest.NewServer(svc)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/skills")
	if err != nil {
		t.Fatalf("get skills: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected extension discovery error, got %d body=%s", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), ".agent") {
		t.Fatalf("expected error to identify malformed .agent path, got %s", string(body))
	}
}

func TestServiceConfigUpdateSwapsSnapshotInsteadOfMutatingSharedConfig(t *testing.T) {
	cfg := testConfig(t, "")
	cfg.Runtime.GuardrailsMode = "standard"
	cfg.Providers["openai"] = config.Provider{
		APIProvider:       "openai-compatible",
		APIKeyEnv:         "OPENAI_API_KEY",
		BaseURL:           "http://127.0.0.1:1/v1",
		Model:             "gpt-5.4",
		RequestTimeoutSec: 3,
	}
	svc, err := New(cfg, Options{WorkerCount: 0, ConfigPath: filepath.Join(t.TempDir(), "config.yaml")})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	original := svc.cfg

	ts := httptest.NewServer(svc)
	defer ts.Close()

	postJSON(t, ts.URL+"/api/config", map[string]any{
		"provider":        "openai",
		"guardrails_mode": "yolo",
		"max_turns_hard":  4,
		"api_provider":    "openai-compatible",
		"base_url":        "http://127.0.0.1:2/v1",
		"model":           "gpt-5.4",
		"reasoning_mode":  "default",
	}, http.StatusOK, nil)

	if original == svc.cfg {
		t.Fatal("expected service config update to swap config pointer")
	}
	if original.Runtime.GuardrailsMode != "standard" || original.Providers["openai"].BaseURL != "http://127.0.0.1:1/v1" {
		t.Fatalf("expected old config snapshot to remain immutable, got %#v", original)
	}
	updated, err := svc.configSnapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if updated.Runtime.GuardrailsMode != "yolo" || updated.Providers["openai"].BaseURL != "http://127.0.0.1:2/v1" {
		t.Fatalf("expected updated config snapshot, got %#v", updated)
	}
}

func testConfig(t *testing.T, baseURL string) *config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.DefaultProvider = "openai"
	provider := cfg.Providers["openai"]
	provider.BaseURL = baseURL
	provider.APIKeyEnv = ""
	provider.Model = "gpt-5.4"
	provider.TimeoutSec = 3
	cfg.Providers["openai"] = provider
	cfg.Session.Dir = filepath.Join(t.TempDir(), "sessions")
	cfg.Skills.Dirs = nil
	cfg.Runtime.Queue.PollIntervalMS = 20
	cfg.Runtime.Queue.AutoWorker = false
	return cfg
}

func anySliceStrings(values []any) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		text, _ := value.(string)
		out = append(out, text)
	}
	return out
}

func webHasAnthropicCacheControl(value any) bool {
	obj, ok := value.(map[string]any)
	if !ok {
		return false
	}
	cache, ok := obj["cache_control"].(map[string]any)
	return ok && cache["type"] == "ephemeral"
}

func webAnthropicCacheControlOnFirstSystemBlock(body map[string]any) bool {
	system, ok := body["system"].([]any)
	if !ok || len(system) == 0 {
		return false
	}
	return webHasAnthropicCacheControl(system[0])
}

func webAnthropicCacheControlOnLastMessageBlock(body map[string]any) bool {
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) == 0 {
		return false
	}
	msg, ok := messages[len(messages)-1].(map[string]any)
	if !ok {
		return false
	}
	content, ok := msg["content"].([]any)
	if !ok || len(content) == 0 {
		return false
	}
	return webHasAnthropicCacheControl(content[len(content)-1])
}

func testSessionMetadata(t *testing.T, id string) session.SessionMetadata {
	t.Helper()
	workdir := t.TempDir()
	return session.SessionMetadata{
		SchemaVersion:    1,
		ID:               id,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		RequestedWorkdir: workdir,
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
}

func testSessionState(status string) session.State {
	return session.State{
		Status:    status,
		Phase:     string(status),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func newFinishServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_1",
			"status":"completed",
			"output":[
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]},
				{"type":"function_call","call_id":"call_finish_1","name":"finish","arguments":"{\"message\":\"done\"}"}
			],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
}

func newSubmitPlanServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_plan_1",
			"status":"completed",
			"output":[
				{"type":"function_call","call_id":"call_submit_plan_1","name":"submit_plan","arguments":"{\"title\":\"Plan Mode test plan\",\"summary\":\"Submit a plan for approval.\",\"plan_markdown\":\"# Plan Mode test plan\\n\\n## Summary\\n\\nSubmit a plan for approval.\\n\\n## Verification\\n\\n- go test ./internal/webconsole\",\"verification\":[\"go test ./internal/webconsole\"],\"assumptions\":[\"service test\"],\"risks\":[\"none\"]}"}
			],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
}

func newPlanInputThenSubmitPlanServer() *httptest.Server {
	var mu sync.Mutex
	requests := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		requestNo := requests
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if requestNo == 1 {
			_, _ = w.Write([]byte(`{
				"id":"resp_plan_input_1",
				"status":"completed",
				"output":[
					{"type":"function_call","call_id":"call_plan_input_1","name":"request_user_input","arguments":"{\"questions\":[{\"id\":\"scope_choice\",\"header\":\"Scope\",\"question\":\"Which scope?\",\"options\":[{\"label\":\"Narrow\",\"description\":\"Keep it focused.\"},{\"label\":\"Broad\",\"description\":\"Include more cleanup.\"}]}]}"}
				],
				"usage":{"input_tokens":10,"output_tokens":5}
			}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"id":"resp_plan_submit_2",
			"status":"completed",
			"output":[
				{"type":"function_call","call_id":"call_submit_plan_after_input_1","name":"submit_plan","arguments":"{\"title\":\"Plan after input\",\"summary\":\"Use the selected narrow scope.\",\"plan_markdown\":\"# Plan after input\\n\\n## Summary\\n\\nUse the selected narrow scope.\\n\\n## Verification\\n\\n- go test ./internal/webconsole\",\"verification\":[\"go test ./internal/webconsole\"],\"assumptions\":[\"live input was answered\"],\"risks\":[\"none\"]}"}
			],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
}

func newGoalCompleteServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_1",
			"status":"completed",
			"output":[
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]},
				{"type":"function_call","call_id":"call_goal_1","name":"update_goal","arguments":"{\"status\":\"complete\",\"evidence\":[\"service test\"]}"},
				{"type":"function_call","call_id":"call_finish_1","name":"finish","arguments":"{\"message\":\"done\"}"}
			],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
}

func newDelayedFinishServer(delay time.Duration) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_1",
			"status":"completed",
			"output":[
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]},
				{"type":"function_call","call_id":"call_finish_1","name":"finish","arguments":"{\"message\":\"done\"}"}
			],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
}

func newSleepToolServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_1",
			"status":"completed",
			"output":[
				{"type":"function_call","call_id":"call_shell_1","name":"shell","arguments":"{\"command\":\"sleep 10\"}"}
			],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
}

func newTextReplyServer(reply string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_1",
			"status":"completed",
			"output":[
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + reply + `"}]}
			],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
}

func createSkillZip(t *testing.T, zipPath, skillDir, skillMD string) {
	t.Helper()
	createZipEntries(t, zipPath, map[string]string{
		filepath.ToSlash(filepath.Join(skillDir, "SKILL.md")): skillMD,
	})
}

func createZipEntries(t *testing.T, zipPath string, entries map[string]string) {
	t.Helper()
	ordered := make([]zipTestEntry, 0, len(entries))
	for name, content := range entries {
		ordered = append(ordered, zipTestEntry{name: name, content: content})
	}
	createZipEntriesInOrder(t, zipPath, ordered)
}

type zipTestEntry struct {
	name    string
	content string
	mode    os.FileMode
}

func createZipEntriesInOrder(t *testing.T, zipPath string, entries []zipTestEntry) {
	t.Helper()
	file, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	defer file.Close()

	zipWriter := zip.NewWriter(file)
	for _, item := range entries {
		var entry io.Writer
		var err error
		if item.mode != 0 {
			header := &zip.FileHeader{Name: item.name}
			header.SetMode(item.mode)
			entry, err = zipWriter.CreateHeader(header)
		} else {
			entry, err = zipWriter.Create(item.name)
		}
		if err != nil {
			t.Fatalf("create zip entry %s: %v", item.name, err)
		}
		if _, err := entry.Write([]byte(item.content)); err != nil {
			t.Fatalf("write zip entry %s: %v", item.name, err)
		}
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
}

func createCorruptStoredZip(t *testing.T, zipPath string, entries []zipTestEntry, oldPayload, newPayload string) {
	t.Helper()
	if len(oldPayload) == 0 || len(oldPayload) != len(newPayload) {
		t.Fatalf("corrupt payload replacement must be non-empty and length-preserving")
	}
	file, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	zipWriter := zip.NewWriter(file)
	for _, item := range entries {
		header := &zip.FileHeader{Name: item.name, Method: zip.Store}
		header.SetMode(0o644)
		entry, err := zipWriter.CreateHeader(header)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", item.name, err)
		}
		if _, err := entry.Write([]byte(item.content)); err != nil {
			t.Fatalf("write zip entry %s: %v", item.name, err)
		}
	}
	if err := zipWriter.Close(); err != nil {
		_ = file.Close()
		t.Fatalf("close zip writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}
	data, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatalf("read zip: %v", err)
	}
	idx := bytes.Index(data, []byte(oldPayload))
	if idx < 0 {
		t.Fatalf("payload %q not found in stored zip", oldPayload)
	}
	copy(data[idx:idx+len(oldPayload)], []byte(newPayload))
	if err := os.WriteFile(zipPath, data, 0o600); err != nil {
		t.Fatalf("write corrupt zip: %v", err)
	}
}

func postGetJSON(t *testing.T, url string, target any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("unexpected get status %d body=%s", resp.StatusCode, string(body))
	}
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
}

func postJSON(t *testing.T, url string, payload any, wantStatus int, target any) {
	t.Helper()
	requestJSONWithMethod(t, http.MethodPost, url, payload, wantStatus, target)
}

func requestJSONWithMethod(t *testing.T, method string, url string, payload any, wantStatus int, target any) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req, err := http.NewRequest(method, url, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("new %s %s: %v", method, url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(webMutationHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		var body bytes.Buffer
		_, _ = body.ReadFrom(resp.Body)
		t.Fatalf("unexpected status %d want %d body=%s", resp.StatusCode, wantStatus, body.String())
	}
	if target != nil {
		if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
			t.Fatalf("decode response: %v", err)
		}
	}
}

func requestWithoutJSONContentType(t *testing.T, method string, url string, wantStatus int) string {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("new %s %s: %v", method, url, err)
	}
	req.Header.Set(webMutationHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("unexpected status %d want %d body=%s", resp.StatusCode, wantStatus, string(body))
	}
	return string(body)
}

func postJSONWithMethod(t *testing.T, method string, url string, payload any, wantStatus int, target any) {
	t.Helper()
	requestJSONWithMethod(t, method, url, payload, wantStatus, target)
}

func goalHistoryContainsType(history []session.GoalHistoryEntry, target string) bool {
	for _, entry := range history {
		if entry.Type == target {
			return true
		}
	}
	return false
}

func blockWebGoalHistoryPath(t *testing.T, store *session.Store, sessionID string) {
	t.Helper()
	historyPath := filepath.Join(store.SessionDir(sessionID), "artifacts", "goal-history.jsonl")
	if err := os.Remove(historyPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove goal history: %v", err)
	}
	if err := os.Mkdir(historyPath, 0o700); err != nil {
		t.Fatalf("block goal history path: %v", err)
	}
}

func blockWebGoalLockPath(t *testing.T, store *session.Store, sessionID string) {
	t.Helper()
	lockPath := filepath.Join(store.SessionDir(sessionID), "goal.lock")
	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove goal lock: %v", err)
	}
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatalf("block goal lock path: %v", err)
	}
}

func blockWebEventsPath(t *testing.T, store *session.Store, sessionID string) {
	t.Helper()
	eventsPath := filepath.Join(store.SessionDir(sessionID), "events.jsonl")
	if err := os.Remove(eventsPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove events: %v", err)
	}
	if err := os.Mkdir(eventsPath, 0o700); err != nil {
		t.Fatalf("block events path: %v", err)
	}
}

func blockWebPlanModeHistoryPath(t *testing.T, store *session.Store, sessionID string) {
	t.Helper()
	historyPath := filepath.Join(store.SessionDir(sessionID), "artifacts", "planmode-history.jsonl")
	if err := os.Remove(historyPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove plan mode history: %v", err)
	}
	if err := os.Mkdir(historyPath, 0o700); err != nil {
		t.Fatalf("block plan mode history path: %v", err)
	}
}

func blockWebTaskboardLockPath(t *testing.T, store *session.Store, sessionID string) {
	t.Helper()
	lockPath := filepath.Join(store.SessionDir(sessionID), "tasks", "taskboard.lock")
	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove taskboard lock: %v", err)
	}
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatalf("block taskboard lock path: %v", err)
	}
}

func postJSONError(t *testing.T, url string, payload any, wantStatus int) ErrorResponse {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return postRawJSONError(t, url, string(data), wantStatus)
}

func postRawJSONError(t *testing.T, url string, body string, wantStatus int) ErrorResponse {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new post %s: %v", url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(webMutationHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		var body bytes.Buffer
		_, _ = body.ReadFrom(resp.Body)
		t.Fatalf("unexpected status %d want %d body=%s", resp.StatusCode, wantStatus, body.String())
	}
	var errResp ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	return errResp
}

func loadWebAuditEvents(t *testing.T, path string) []webAuditEvent {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	var events []webAuditEvent
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event webAuditEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode audit event %q: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

func hasWebAuditEvent(events []webAuditEvent, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, timeout time.Duration, fn func() bool, describe func() string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatalf("condition was not satisfied before timeout: %s", describe())
}

func mustEmbeddedAsset(t *testing.T, name string) []byte {
	t.Helper()
	assets, err := assetFS()
	if err != nil {
		t.Fatalf("asset fs: %v", err)
	}
	data, err := fs.ReadFile(assets, name)
	if err != nil {
		t.Fatalf("read asset %s: %v", name, err)
	}
	return data
}

func TestServeEmbeddedFileETagAndGzip(t *testing.T) {
	assets, err := assetFS()
	if err != nil {
		t.Fatalf("asset fs: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/styles.css", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	serveEmbeddedFileRequest(rec, req, assets, "styles.css")
	resp := rec.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" || !strings.HasPrefix(etag, "\"") || !strings.HasSuffix(etag, "\"") {
		t.Fatalf("missing or malformed ETag: %q", etag)
	}
	if got := resp.Header.Get("Vary"); !strings.Contains(strings.ToLower(got), "accept-encoding") {
		t.Fatalf("Vary header = %q, want Accept-Encoding", got)
	}
	if got := resp.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := resp.Header.Get("Content-Type"); !strings.Contains(got, "text/css") {
		t.Fatalf("Content-Type = %q, want text/css", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) == 0 {
		t.Fatal("gzip response body is empty")
	}

	// If-None-Match returns 304 with the same ETag.
	rec304 := httptest.NewRecorder()
	req304 := httptest.NewRequest(http.MethodGet, "/styles.css", nil)
	req304.Header.Set("If-None-Match", etag)
	req304.Header.Set("Accept-Encoding", "gzip")
	serveEmbeddedFileRequest(rec304, req304, assets, "styles.css")
	if rec304.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match status = %d, want 304", rec304.Code)
	}
	if got := rec304.Header().Get("ETag"); got != etag {
		t.Fatalf("304 ETag = %q, want %q", got, etag)
	}

	// Without gzip support the original bytes are returned.
	recPlain := httptest.NewRecorder()
	reqPlain := httptest.NewRequest(http.MethodGet, "/styles.css", nil)
	serveEmbeddedFileRequest(recPlain, reqPlain, assets, "styles.css")
	if recPlain.Code != http.StatusOK {
		t.Fatalf("plain status = %d, want 200", recPlain.Code)
	}
	if got := recPlain.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("plain Content-Encoding = %q, want empty", got)
	}
	plain, _ := io.ReadAll(recPlain.Result().Body)
	original := mustEmbeddedAsset(t, "styles.css")
	if !bytes.Equal(plain, original) {
		t.Fatalf("plain body length = %d, want %d", len(plain), len(original))
	}

	for _, tc := range []struct {
		name           string
		acceptEncoding string
		wantGzip       bool
	}{
		{name: "q zero disables gzip", acceptEncoding: "gzip;q=0, identity;q=1", wantGzip: false},
		{name: "wildcard permits gzip", acceptEncoding: "br;q=0, *;q=0.5", wantGzip: true},
		{name: "explicit gzip beats wildcard", acceptEncoding: "gzip;q=0, *;q=1", wantGzip: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/styles.css", nil)
			req.Header.Set("Accept-Encoding", tc.acceptEncoding)
			serveEmbeddedFileRequest(rec, req, assets, "styles.css")
			gotGzip := rec.Header().Get("Content-Encoding") == "gzip"
			if gotGzip != tc.wantGzip {
				t.Fatalf("Content-Encoding gzip = %v, want %v for %q", gotGzip, tc.wantGzip, tc.acceptEncoding)
			}
		})
	}
}
