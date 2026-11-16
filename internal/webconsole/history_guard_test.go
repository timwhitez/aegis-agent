package webconsole

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"aegis-agent/internal/session"
)

const historyRaceResponseBound = 3 * time.Second

// Issues #160 and #161: delete and clear must never remove a session that a
// live run in this process still owns, whether that run's handle was taken
// before the mutation or races it. A launch that races a delete or clear is
// refused with 409 at once; it never queues behind the mutation.

func historyGuardRequest(svc *Service, method, path, body string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	recorder := httptest.NewRecorder()
	request := newLocalWebRequest(method, path, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set(webMutationHeader, "1")
	svc.ServeHTTP(recorder, request)
	return recorder
}

// blockingProvider holds every model request open until release is called, so
// a run that reached the provider stays running.
func blockingProvider(t *testing.T) (url string, hit <-chan struct{}, release func()) {
	t.Helper()
	hitCh := make(chan struct{})
	releaseCh := make(chan struct{})
	var hitOnce, releaseOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitOnce.Do(func() { close(hitCh) })
		select {
		case <-releaseCh:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","status":"completed","output":[{"type":"function_call","call_id":"call_finish","name":"finish","arguments":"{\"message\":\"done\"}"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	release = func() { releaseOnce.Do(func() { close(releaseCh) }) }
	t.Cleanup(func() {
		release()
		server.Close()
	})
	return server.URL, hitCh, release
}

// pauseClearAfterPreflight stops POST /api/sessions/clear after its conflict
// checks and before it moves any history.
func pauseClearAfterPreflight(t *testing.T) (entered <-chan struct{}, release func()) {
	t.Helper()
	enteredCh := make(chan struct{})
	releaseCh := make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	previous := beforeWebHistoryBackupRootCreate
	beforeWebHistoryBackupRootCreate = func(string) error {
		enterOnce.Do(func() { close(enteredCh) })
		<-releaseCh
		return nil
	}
	release = func() { releaseOnce.Do(func() { close(releaseCh) }) }
	t.Cleanup(func() {
		release()
		beforeWebHistoryBackupRootCreate = previous
	})
	return enteredCh, release
}

func waitClosed(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("%s did not finish", what)
	}
}

func TestServiceDeleteSessionRejectsOwnActiveHandle(t *testing.T) {
	svc, err := New(testConfig(t, ""), Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := session.SessionMetadata{SchemaVersion: 1, ID: "delete_own_handle", CreatedAt: now, Workdir: t.TempDir(), RequestedWorkdir: t.TempDir(), Mode: session.ModeRun, Provider: "openai", Model: "gpt-5.4", CompletionPolicy: session.CompletionPolicyInteractive, RootSessionID: "delete_own_handle"}
	// A Plan Mode run blocked in request_user_input is awaiting_input while
	// its in-process runner and Web handle stay live.
	if err := svc.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_input", UpdatedAt: now}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	svc.mu.Lock()
	svc.handles[meta.ID] = &launchHandle{sessionID: meta.ID, cancel: func() {}, startedAt: now, processStartID: "test-process", pid: os.Getpid()}
	svc.mu.Unlock()

	recorder := historyGuardRequest(svc, http.MethodDelete, "/api/sessions/"+meta.ID, "")
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "active session tree") {
		t.Fatalf("deleting the owner of a live handle must conflict, got %d %s", recorder.Code, recorder.Body.String())
	}
	if _, err := svc.store.LoadMetadata(meta.ID); err != nil {
		t.Fatalf("session must remain after the refused delete: %v", err)
	}
}

// launchDuringPausedClear pauses clear after its preflight, sends one launch
// request and requires it to return before clear is released.
func launchDuringPausedClear(t *testing.T, svc *Service, method, path, body string) (launch, clear *httptest.ResponseRecorder) {
	t.Helper()
	clearEntered, releaseClear := pauseClearAfterPreflight(t)
	clearDone := make(chan struct{})
	go func() {
		defer close(clearDone)
		clear = historyGuardRequest(svc, http.MethodPost, "/api/sessions/clear", "")
	}()
	waitClosed(t, clearEntered, "clear preflight")
	launchDone := make(chan struct{})
	go func() {
		defer close(launchDone)
		launch = historyGuardRequest(svc, method, path, body)
	}()
	select {
	case <-launchDone:
	case <-time.After(historyRaceResponseBound):
		releaseClear()
		waitClosed(t, launchDone, path)
		t.Fatalf("%s waited for clear instead of failing fast: %d %s", path, launch.Code, launch.Body.String())
	}
	releaseClear()
	waitClosed(t, clearDone, "clear")
	return launch, clear
}

func awaitingInputSession(t *testing.T, svc *Service, id string) session.SessionMetadata {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	workdir := t.TempDir()
	meta := session.SessionMetadata{SchemaVersion: 1, ID: id, CreatedAt: now, Workdir: workdir, RequestedWorkdir: workdir, Mode: session.ModeExec, Provider: "openai", Model: "gpt-5.4", CompletionPolicy: session.CompletionPolicyAutonomous}
	if err := svc.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "awaiting_input", UpdatedAt: now}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return meta
}

func requireSessionRemoved(t *testing.T, svc *Service, id string) {
	t.Helper()
	if svc.hasActiveHandle(id) {
		t.Fatalf("no handle may remain for cleared session %s", id)
	}
	if _, err := os.Stat(svc.store.SessionDir(id)); !errors.Is(err, os.ErrNotExist) {
		entries, _ := os.ReadDir(svc.store.SessionDir(id))
		t.Fatalf("cleared session %s left a directory behind: err=%v entries=%d", id, err, len(entries))
	}
}

func TestServiceClearSessionsRefusesConcurrentContinue(t *testing.T) {
	providerURL, _, releaseProvider := blockingProvider(t)
	svc, err := New(testConfig(t, providerURL), Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	defer releaseProvider()
	meta := awaitingInputSession(t, svc, "clear_vs_continue")
	launch, clear := launchDuringPausedClear(t, svc, http.MethodPost, "/api/sessions/"+meta.ID+"/continue", `{"message":"continue during clear"}`)
	if launch.Code != http.StatusConflict || clear.Code != http.StatusOK {
		t.Fatalf("continue during clear must get 409 and clear must finish: continue=%d %s clear=%d %s", launch.Code, launch.Body.String(), clear.Code, clear.Body.String())
	}
	requireSessionRemoved(t, svc, meta.ID)
}

func TestServiceClearSessionsRefusesConcurrentStart(t *testing.T) {
	providerURL, _, releaseProvider := blockingProvider(t)
	svc, err := New(testConfig(t, providerURL), Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	defer releaseProvider()
	body := `{"prompt":"start during clear","mode":"exec","workdir":` + strconv.Quote(t.TempDir()) + `}`
	launch, clear := launchDuringPausedClear(t, svc, http.MethodPost, "/api/sessions/start", body)
	if launch.Code != http.StatusConflict || clear.Code != http.StatusOK {
		t.Fatalf("start during clear must get 409 and clear must finish: start=%d %s clear=%d %s", launch.Code, launch.Body.String(), clear.Code, clear.Body.String())
	}
	items, _, err := svc.store.ListPage(10, 0)
	if err != nil || len(items) != 0 {
		t.Fatalf("no session may be created across clear: items=%d err=%v", len(items), err)
	}
}

// Approval writes run claims and receipts before it registers its handle, and
// revise launches through the Plan Mode continue path; both must be refused
// while clear is moving history.
func TestServiceClearSessionsRefusesConcurrentPlanModeLaunches(t *testing.T) {
	for _, action := range []string{"approve", "revise"} {
		t.Run(action, func(t *testing.T) {
			provider := newFinishServer()
			defer provider.Close()
			svc, err := New(testConfig(t, provider.URL), Options{WorkerCount: 0})
			if err != nil {
				t.Fatalf("new service: %v", err)
			}
			defer svc.Close()
			meta := testSessionMetadata(t, "clear_vs_"+action)
			meta.Mode = session.ModeExec
			meta.RootSessionID = meta.ID
			meta.CompletionPolicy = session.CompletionPolicyAutonomous
			if err := svc.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_approval"}); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.store.CreatePlanMode(meta.ID, session.PlanModeDraft{Enabled: true, Objective: "clear race plan"}); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.store.SubmitPlanMode(meta.ID, session.PlanModeSubmitInput{Title: "Plan", Summary: "ready", Verification: []string{"check"}, PlanMarkdown: "# Plan\nDo it.", Source: session.PlanModeSourceTool}); err != nil {
				t.Fatal(err)
			}
			body := `{"message":"revise during clear"}`
			if action == "approve" {
				payload, err := json.Marshal(reviewedApprovalPayload(t, svc.store, meta.ID, false))
				if err != nil {
					t.Fatal(err)
				}
				body = string(payload)
			}
			launch, clear := launchDuringPausedClear(t, svc, http.MethodPost, "/api/sessions/"+meta.ID+"/planmode/"+action, body)
			if launch.Code != http.StatusConflict || clear.Code != http.StatusOK {
				t.Fatalf("%s during clear must get 409 and clear must finish: %s=%d %s clear=%d %s", action, action, launch.Code, launch.Body.String(), clear.Code, clear.Body.String())
			}
			requireSessionRemoved(t, svc, meta.ID)
		})
	}
}

func TestServiceClearSessionsRefusesPendingStart(t *testing.T) {
	svc, err := New(testConfig(t, ""), Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	id, err := svc.registerPendingStart(func() {})
	if err != nil {
		t.Fatalf("register pending start: %v", err)
	}
	if recorder := historyGuardRequest(svc, http.MethodPost, "/api/sessions/clear", ""); recorder.Code != http.StatusConflict {
		t.Fatalf("clear with a start in flight must conflict, got %d %s", recorder.Code, recorder.Body.String())
	}
	svc.removePendingStart(id)
	if recorder := historyGuardRequest(svc, http.MethodPost, "/api/sessions/clear", ""); recorder.Code != http.StatusOK {
		t.Fatalf("clear after the start settled: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestServiceDeleteAndLaunchRefuseEachOther(t *testing.T) {
	provider := newFinishServer()
	defer provider.Close()
	svc, err := New(testConfig(t, provider.URL), Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	target := awaitingInputSession(t, svc, "delete_target")
	other := awaitingInputSession(t, svc, "delete_bystander")

	// A launch still validating the target makes the delete conflict.
	release, err := svc.reserveLaunch(target.ID)
	if err != nil {
		t.Fatalf("reserve launch: %v", err)
	}
	if recorder := historyGuardRequest(svc, http.MethodDelete, "/api/sessions/"+target.ID, ""); recorder.Code != http.StatusConflict {
		t.Fatalf("delete during a launch must conflict, got %d %s", recorder.Code, recorder.Body.String())
	}
	release()

	// While a delete is removing the target, its launches fail fast and
	// launches of unrelated sessions proceed.
	finish, err := svc.beginSessionTreeRemoval(map[string]struct{}{target.ID: {}})
	if err != nil {
		t.Fatalf("begin removal: %v", err)
	}
	started := time.Now()
	if recorder := historyGuardRequest(svc, http.MethodPost, "/api/sessions/"+target.ID+"/continue", `{"message":"continue during delete"}`); recorder.Code != http.StatusConflict {
		t.Fatalf("continue of a session being deleted must conflict, got %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := historyGuardRequest(svc, http.MethodPost, "/api/sessions/"+other.ID+"/continue", `{"message":"unrelated continue"}`); recorder.Code != http.StatusAccepted {
		t.Fatalf("continue of an unrelated session must not be blocked, got %d %s", recorder.Code, recorder.Body.String())
	}
	if elapsed := time.Since(started); elapsed > historyRaceResponseBound {
		t.Fatalf("launches waited %s for the delete", elapsed)
	}
	finish()
}
