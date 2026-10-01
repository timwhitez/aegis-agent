package webconsole

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aegis-agent/internal/runtime"
	"aegis-agent/internal/session"
)

func TestPlanModeActionsWaitForSettlingHandle(t *testing.T) {
	for _, action := range []string{"approve", "revise", "mission"} {
		t.Run(action, func(t *testing.T) {
			provider := newFinishServer()
			defer provider.Close()
			cfg := testConfig(t, provider.URL)
			svc, err := New(cfg, Options{WorkerCount: 0})
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			meta := testSessionMetadata(t, "settle_"+action)
			meta.Mode = session.ModeExec
			meta.RootSessionID = meta.ID
			meta.CompletionPolicy = session.CompletionPolicyAutonomous
			if err := svc.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_approval"}); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.store.CreatePlanMode(meta.ID, session.PlanModeDraft{Enabled: true, Objective: "settle plan"}); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.store.SubmitPlanMode(meta.ID, session.PlanModeSubmitInput{Title: "Plan", Summary: "ready", Verification: []string{"check"}, PlanMarkdown: "# Plan\nDo it.", Source: session.PlanModeSourceTool}); err != nil {
				t.Fatal(err)
			}
			if action == "mission" {
				goal, err := svc.store.CreateGoal(meta.ID, session.GoalDraft{Enabled: true, Mode: session.GoalModeMission, Objective: "linked mission"})
				if err != nil {
					t.Fatal(err)
				}
				plan, err := svc.store.LoadPlanMode(meta.ID)
				if err != nil {
					t.Fatal(err)
				}
				plan.LinkedGoalID = goal.GoalID
				if err := svc.store.SavePlanMode(meta.ID, plan); err != nil {
					t.Fatal(err)
				}
			}
			state, err := svc.store.LoadState(meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			handle := &launchHandle{sessionID: meta.ID, runner: runtime.NewRunner(cfg), cancel: func() {}, startedAt: meta.CreatedAt}
			svc.handles[meta.ID] = handle
			body := `{}`
			path := "/api/sessions/" + meta.ID + "/planmode/" + action
			if action == "mission" {
				path = "/api/sessions/" + meta.ID + "/mission/plan/approve"
				body = `{"override_coverage":true}`
			}
			if action == "revise" {
				body = `{"message":"revise once"}`
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
				req.Host = "127.0.0.1"
				req.Header.Set("X-Aegis-Agent-Web", "1")
				req.Header.Set("Content-Type", "application/json")
				svc.ServeHTTP(w, req)
				done <- w
			}()
			select {
			case response := <-done:
				t.Fatalf("returned before original handle released: %d %s", response.Code, response.Body)
			case <-time.After(30 * time.Millisecond):
			}
			svc.mu.Lock()
			delete(svc.handles, meta.ID)
			svc.mu.Unlock()
			select {
			case response := <-done:
				if response.Code != http.StatusAccepted {
					t.Fatalf("status=%d body=%s state=%#v", response.Code, response.Body, state)
				}
			case <-time.After(time.Second):
				t.Fatal("did not observe original handle release")
			}
		})
	}
}

func TestPlanInputWaitsForOriginalRunnerReadiness(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "delayed_plan_input")
	if err := svc.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_input"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.CreatePlanMode(meta.ID, session.PlanModeDraft{Enabled: true, Objective: "ask once"}); err != nil {
		t.Fatal(err)
	}
	request := session.PlanModeInputRequest{RequestID: "request_one", ToolCallID: "call_one", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Questions: []session.PlanModeInputQuestion{{ID: "choice", Header: "Scope", Question: "Choose scope", Options: []session.PlanModeInputOption{{Label: "Narrow", Description: "Small scope"}, {Label: "Broad", Description: "Broad scope"}}}}}
	if _, err := svc.store.SetPlanModePendingRequest(meta.ID, request, session.PlanModeSourceTool); err != nil {
		t.Fatal(err)
	}
	runner := runtime.NewRunner(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handle := &launchHandle{sessionID: meta.ID, runner: runner, cancel: cancel, startedAt: meta.CreatedAt}
	svc.handles[meta.ID] = handle
	answers := []session.PlanModeInputAnswer{{QuestionID: "choice", Label: "Narrow", Value: "Narrow"}}
	payload, err := json.Marshal(PlanModeInputRequest{RequestID: request.RequestID, Answers: answers})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+meta.ID+"/planmode/input", strings.NewReader(string(payload)))
		req.Host = "127.0.0.1"
		req.Header.Set("X-Aegis-Agent-Web", "1")
		req.Header.Set("Content-Type", "application/json")
		svc.ServeHTTP(w, req)
		done <- w
	}()
	select {
	case response := <-done:
		t.Fatalf("input fell back before live waiter readiness: %d %s", response.Code, response.Body)
	case <-time.After(30 * time.Millisecond):
	}
	delivered := make(chan []session.PlanModeInputAnswer, 1)
	go func() { got, _ := runner.RequestPlanInput(ctx, meta.ID, request); delivered <- got }()
	select {
	case response := <-done:
		if response.Code != http.StatusAccepted {
			t.Fatalf("status=%d body=%s", response.Code, response.Body)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter registration not observed")
	}
	select {
	case got := <-delivered:
		if len(got) != 1 || got[0].Value != "Narrow" {
			t.Fatalf("answers=%#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("answer not delivered")
	}
	if runner.AnswerActivePlanInput(meta.ID, request.RequestID, answers) {
		t.Fatal("answer was deliverable twice")
	}
	svc.mu.RLock()
	current := svc.handles[meta.ID]
	svc.mu.RUnlock()
	if current != handle {
		t.Fatal("input readiness spawned a replacement runner")
	}
}

func TestPlanInputReadinessRejectsNewOrReplacedHandle(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "input_generation")
	if err := svc.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_input"}); err != nil {
		t.Fatal(err)
	}
	request := session.PlanModeInputRequest{RequestID: "request", CreatedAt: meta.CreatedAt}
	handle := &launchHandle{sessionID: meta.ID, runner: runtime.NewRunner(cfg), cancel: func() {}, startedAt: time.Now().Add(time.Second).Format(time.RFC3339Nano)}
	svc.handles[meta.ID] = handle
	if delivered, err := svc.waitForActivePlanInput(context.Background(), handle, request, nil); delivered || err != errSessionAlreadyActive {
		t.Fatalf("newer handle: delivered=%v err=%v", delivered, err)
	}
	handle.startedAt = meta.CreatedAt
	done := make(chan error, 1)
	go func() { _, err := svc.waitForActivePlanInput(context.Background(), handle, request, nil); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("wait returned before replacement: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	replacement := &launchHandle{sessionID: meta.ID, runner: runtime.NewRunner(cfg), cancel: func() {}, startedAt: meta.CreatedAt}
	svc.mu.Lock()
	svc.handles[meta.ID] = replacement
	svc.mu.Unlock()
	select {
	case err := <-done:
		if err != errSessionAlreadyActive {
			t.Fatalf("replacement: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement not rejected")
	}
	svc.mu.RLock()
	current := svc.handles[meta.ID]
	svc.mu.RUnlock()
	if current != replacement {
		t.Fatal("replacement handle was changed")
	}
}

func TestPlanInputReadinessAcceptsRecoveredRequestWithNewHandle(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "recovered_waiter")
	if err := svc.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_input", UpdatedAt: time.Now().Add(time.Second).Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := runtime.NewRunner(cfg)
	handle := &launchHandle{sessionID: meta.ID, runner: runner, cancel: cancel, startedAt: meta.CreatedAt}
	svc.handles[meta.ID] = handle
	request := session.PlanModeInputRequest{RequestID: "old_request", CreatedAt: "2000-01-01T00:00:00Z"}
	answers := []session.PlanModeInputAnswer{{QuestionID: "scope", Value: "Narrow"}}
	done := make(chan []session.PlanModeInputAnswer, 1)
	go func() { got, _ := runner.RequestPlanInput(ctx, meta.ID, request); done <- got }()
	if delivered, err := svc.waitForActivePlanInput(ctx, handle, request, answers); err != nil || !delivered {
		t.Fatalf("recovered waiter rejected: delivered=%v err=%v", delivered, err)
	}
	select {
	case got := <-done:
		if len(got) != 1 || got[0].Value != "Narrow" {
			t.Fatalf("answers=%#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("recovered answer not delivered")
	}
}

func TestPlanInputReadinessCancellationDoesNotAnswerAnotherRequest(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "cancel_wrong_input")
	if err := svc.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_input"}); err != nil {
		t.Fatal(err)
	}
	runner := runtime.NewRunner(cfg)
	runCtx, stop := context.WithCancel(context.Background())
	defer stop()
	handle := &launchHandle{sessionID: meta.ID, runner: runner, cancel: stop, startedAt: meta.CreatedAt}
	svc.handles[meta.ID] = handle
	delivered := make(chan []session.PlanModeInputAnswer, 1)
	go func() {
		got, _ := runner.RequestPlanInput(runCtx, meta.ID, session.PlanModeInputRequest{RequestID: "new_request"})
		delivered <- got
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if ok, err := svc.waitForActivePlanInput(ctx, handle, session.PlanModeInputRequest{RequestID: "old_request"}, nil); ok || err != errSessionAlreadyActive {
		t.Fatalf("cancelled wrong request: ok=%v err=%v", ok, err)
	}
	answers := []session.PlanModeInputAnswer{{Value: "kept"}}
	if !runner.AnswerActivePlanInput(meta.ID, "new_request", answers) {
		t.Fatal("correct waiter was consumed by wrong/cancelled request")
	}
	select {
	case got := <-delivered:
		if len(got) != 1 || got[0].Value != "kept" {
			t.Fatalf("answers=%#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("correct waiter did not receive answer")
	}
}
