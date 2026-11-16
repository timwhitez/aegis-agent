package webconsole

import (
	"encoding/json"
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

// Issues #160 and #161: delete and clear must never remove a session that a
// live run in this process still owns, whether that run's handle was taken
// before the mutation or races it.

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

func TestServiceClearSessionsSerializesConcurrentContinue(t *testing.T) {
	providerURL, providerHit, releaseProvider := blockingProvider(t)
	svc, err := New(testConfig(t, providerURL), Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	defer releaseProvider()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	workdir := t.TempDir()
	meta := session.SessionMetadata{SchemaVersion: 1, ID: "clear_vs_continue", CreatedAt: now, Workdir: workdir, RequestedWorkdir: workdir, Mode: session.ModeExec, Provider: "openai", Model: "gpt-5.4", CompletionPolicy: session.CompletionPolicyAutonomous}
	if err := svc.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "awaiting_input", UpdatedAt: now}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	clearEntered, releaseClear := pauseClearAfterPreflight(t)
	var clearRecorder, continueRecorder *httptest.ResponseRecorder
	clearDone := make(chan struct{})
	go func() {
		defer close(clearDone)
		clearRecorder = historyGuardRequest(svc, http.MethodPost, "/api/sessions/clear", "")
	}()
	waitClosed(t, clearEntered, "clear preflight")
	continueDone := make(chan struct{})
	go func() {
		defer close(continueDone)
		continueRecorder = historyGuardRequest(svc, http.MethodPost, "/api/sessions/"+meta.ID+"/continue", `{"message":"continue during clear"}`)
	}()
	// Unserialized, the continue reaches the model while clear is past its
	// checks, and releasing clear then deletes a running session.
	select {
	case <-providerHit:
	case <-time.After(time.Second):
	}
	releaseClear()
	waitClosed(t, clearDone, "clear")
	waitClosed(t, continueDone, "continue")
	if clearRecorder.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", clearRecorder.Code, clearRecorder.Body.String())
	}
	if continueRecorder.Code != http.StatusNotFound {
		t.Fatalf("a continue that races clear must observe the cleared session (404), got %d %s", continueRecorder.Code, continueRecorder.Body.String())
	}
	if svc.hasActiveHandle(meta.ID) {
		t.Fatal("no handle may remain for the cleared session")
	}
}

func TestServiceClearSessionsSerializesConcurrentStart(t *testing.T) {
	providerURL, providerHit, releaseProvider := blockingProvider(t)
	svc, err := New(testConfig(t, providerURL), Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	defer releaseProvider()
	clearEntered, releaseClear := pauseClearAfterPreflight(t)
	var clearRecorder, startRecorder *httptest.ResponseRecorder
	clearDone := make(chan struct{})
	go func() {
		defer close(clearDone)
		clearRecorder = historyGuardRequest(svc, http.MethodPost, "/api/sessions/clear", "")
	}()
	waitClosed(t, clearEntered, "clear preflight")
	startDone := make(chan struct{})
	body := `{"prompt":"start during clear","mode":"exec","workdir":` + strconv.Quote(t.TempDir()) + `}`
	go func() {
		defer close(startDone)
		startRecorder = historyGuardRequest(svc, http.MethodPost, "/api/sessions/start", body)
	}()
	select {
	case <-providerHit:
	case <-time.After(time.Second):
	}
	releaseClear()
	waitClosed(t, clearDone, "clear")
	waitClosed(t, startDone, "start")
	if clearRecorder.Code != http.StatusOK || startRecorder.Code != http.StatusAccepted {
		t.Fatalf("clear=%d %s start=%d %s", clearRecorder.Code, clearRecorder.Body.String(), startRecorder.Code, startRecorder.Body.String())
	}
	var launch LaunchResponse
	if err := json.Unmarshal(startRecorder.Body.Bytes(), &launch); err != nil || launch.SessionID == "" {
		t.Fatalf("start response: %v %s", err, startRecorder.Body.String())
	}
	if _, err := svc.store.LoadMetadata(launch.SessionID); err != nil {
		t.Fatalf("a session started across clear must survive it: %v", err)
	}
}
