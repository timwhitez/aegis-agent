package webconsole

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"aegis-agent/internal/session"
)

// Issue #160: delete must never remove a session that a live run in this
// process still owns.

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
