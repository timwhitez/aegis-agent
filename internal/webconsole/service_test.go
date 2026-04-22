package webconsole

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
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
	"testing"
	"time"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/session"

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
	if len(messages) == 0 || messages[0].Role != "user" {
		t.Fatalf("expected user message to be persisted, got %#v", messages)
	}
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
	if !strings.Contains(indexBody, "Agent Console") || !strings.Contains(indexBody, "Ask anything...") || !strings.Contains(indexBody, "new-session-btn") || !strings.Contains(indexBody, "interrupt-session-btn") || !strings.Contains(indexBody, "stop-session-btn") || !strings.Contains(indexBody, "interrupt-toggle-btn") || !strings.Contains(indexBody, "chat-messages") || !strings.Contains(indexBody, "toast-rack") || !strings.Contains(indexBody, "workspace-subtitle") {
		t.Fatalf("unexpected shell body: %s", indexBody)
	}
	if !strings.Contains(indexBody, "Enter to send, Shift+Enter / Ctrl+Enter for new line") {
		t.Fatalf("expected updated input shortcut hint, got shell body: %s", indexBody)
	}
	if strings.Contains(indexBody, "theme-toggle") || strings.Contains(indexBody, "Toggle theme") {
		t.Fatalf("expected dark mode toggle to be removed, got shell body: %s", indexBody)
	}

	jsBody := checkBody(server.URL + "/app.js")
	if !strings.Contains(jsBody, "setupWebSocket") || !strings.Contains(jsBody, "resetChatSession") || !strings.Contains(jsBody, "renderPendingStageCard") || !strings.Contains(jsBody, "showToast") || !strings.Contains(jsBody, "requestJSON") {
		t.Fatalf("unexpected app.js body: %s", jsBody)
	}
	if !strings.Contains(jsBody, "shouldSubmitChatInput") {
		t.Fatalf("expected explicit chat input submit helper, got app.js body: %s", jsBody)
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
	if !strings.Contains(cssBody, "--accent") || !strings.Contains(cssBody, ".sidebar") || !strings.Contains(cssBody, ".chat-shell") || !strings.Contains(cssBody, ".pending-stage-card") || !strings.Contains(cssBody, ".toast-rack") || !strings.Contains(cssBody, "Noto Sans SC") {
		t.Fatalf("unexpected styles.css body: %s", cssBody)
	}
	if strings.Contains(cssBody, "[data-theme=\"dark\"]") || strings.Contains(cssBody, ".theme-toggle") {
		t.Fatalf("expected dark mode styles to be removed, got styles.css body: %s", cssBody)
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

func TestServiceWebSocketChatReusesSessionAndStreamsAssistantMessage(t *testing.T) {
	server := newTextReplyServer("chat reply")
	defer server.Close()

	cfg := testConfig(t, server.URL)
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

	sendChat := func(sessionID, message string) {
		t.Helper()
		if err := conn.WriteJSON(map[string]any{
			"type":      "chat",
			"sessionId": sessionID,
			"message":   message,
		}); err != nil {
			t.Fatalf("write websocket message: %v", err)
		}
	}

	readUntil := func(deadline time.Duration, fn func(map[string]any) bool) {
		t.Helper()
		if err := conn.SetReadDeadline(time.Now().Add(deadline)); err != nil {
			t.Fatalf("set read deadline: %v", err)
		}
		for {
			var msg map[string]any
			if err := conn.ReadJSON(&msg); err != nil {
				t.Fatalf("read websocket message: %v", err)
			}
			if fn(msg) {
				return
			}
		}
	}

	clientSessionID := "0xWSCHAT"
	assistantMessages := 0
	backendSessionID := ""

	sendChat(clientSessionID, "hello")
	readUntil(5*time.Second, func(msg map[string]any) bool {
		switch msg["type"] {
		case "session":
			payload, _ := msg["payload"].(map[string]any)
			backendSessionID, _ = payload["sessionId"].(string)
		case "message":
			payload, _ := msg["payload"].(map[string]any)
			if payload["role"] == "assistant" && payload["content"] == "chat reply" {
				assistantMessages++
			}
		}
		return backendSessionID != "" && assistantMessages == 1
	})

	waitFor(t, 4*time.Second, func() bool {
		state, err := svc.store.LoadState(backendSessionID)
		return err == nil && state.Status == session.StatusAwaitingInput
	}, func() string {
		state, err := svc.store.LoadState(backendSessionID)
		if err != nil {
			return err.Error()
		}
		data, marshalErr := json.Marshal(state)
		if marshalErr != nil {
			return marshalErr.Error()
		}
		return string(data)
	})

	sendChat(backendSessionID, "again")
	readUntil(5*time.Second, func(msg map[string]any) bool {
		if msg["type"] != "message" {
			return false
		}
		payload, _ := msg["payload"].(map[string]any)
		if payload["role"] == "assistant" && payload["content"] == "chat reply" {
			assistantMessages++
		}
		return assistantMessages == 2
	})

	items, err := svc.store.List(10)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one backend session, got %#v", items)
	}
	if items[0].ID != backendSessionID {
		t.Fatalf("expected backend session %q, got %#v", backendSessionID, items)
	}
}

func TestServiceWebSocketDisconnectDuringActiveRunDoesNotBreakSession(t *testing.T) {
	server := newDelayedFinishServer(150 * time.Millisecond)
	defer server.Close()

	cfg := testConfig(t, server.URL)
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
	if err := conn.WriteJSON(map[string]any{
		"type":      "chat",
		"sessionId": "0xDISCONNECT",
		"message":   "finish after the client disconnects",
	}); err != nil {
		_ = conn.Close()
		t.Fatalf("write websocket message: %v", err)
	}

	backendSessionID := ""
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		_ = conn.Close()
		t.Fatalf("set read deadline: %v", err)
	}
	for backendSessionID == "" {
		var msg map[string]any
		if err := conn.ReadJSON(&msg); err != nil {
			_ = conn.Close()
			t.Fatalf("read websocket message: %v", err)
		}
		if msg["type"] != "session" {
			continue
		}
		payload, _ := msg["payload"].(map[string]any)
		backendSessionID, _ = payload["sessionId"].(string)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close websocket: %v", err)
	}

	waitFor(t, 4*time.Second, func() bool {
		state, err := svc.store.LoadState(backendSessionID)
		return err == nil && state.Status == session.StatusCompleted
	}, func() string {
		state, err := svc.store.LoadState(backendSessionID)
		if err != nil {
			return err.Error()
		}
		data, marshalErr := json.Marshal(state)
		if marshalErr != nil {
			return marshalErr.Error()
		}
		return string(data)
	})
	if _, ok := svc.handleForSession(backendSessionID); ok {
		t.Fatalf("expected disconnected session handle to be cleaned up for %s", backendSessionID)
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
	svc.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/sessions/clear", nil))
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
	svc.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/sessions/clear", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected clear status with stale handle: %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestServiceClearSessionsIgnoresStaleRunningSessionsWithoutLiveOwners(t *testing.T) {
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
	svc.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/sessions/clear", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected clear status with stale running session: %d body=%s", recorder.Code, recorder.Body.String())
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
	svc.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/sessions/clear", nil))
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
}

func TestServiceWorkspaceRoutesListReadAndRejectEscape(t *testing.T) {
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

	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "hello.txt"), []byte("hello workspace"), 0o644); err != nil {
		t.Fatalf("write nested file: %v", err)
	}
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
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
	if firstType, _ := tree[0]["type"].(string); firstType != "directory" {
		t.Fatalf("expected directories to sort first, got %#v", tree[0])
	}

	var nested []map[string]any
	postGetJSON(t, ts.URL+"/api/files?path="+url.QueryEscape("nested"), &nested)
	if len(nested) != 1 || nested[0]["name"] != "hello.txt" {
		t.Fatalf("unexpected nested directory listing: %#v", nested)
	}

	var readResp map[string]string
	postGetJSON(t, ts.URL+"/api/file/read?path="+url.QueryEscape("nested/hello.txt"), &readResp)
	if readResp["content"] != "hello workspace" {
		t.Fatalf("unexpected file content: %#v", readResp)
	}

	resp, err := http.Get(ts.URL + "/api/file/read?path=" + url.QueryEscape("../outside.txt"))
	if err != nil {
		t.Fatalf("escape read request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected forbidden for escape read, got %d body=%s", resp.StatusCode, string(body))
	}

	resp, err = http.Get(ts.URL + "/api/files?path=" + url.QueryEscape("../"))
	if err != nil {
		t.Fatalf("escape list request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected forbidden for escape list, got %d body=%s", resp.StatusCode, string(body))
	}
}

func TestServiceMetaReportsCurrentWorkspaceOnly(t *testing.T) {
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

	var meta MetaResponse
	postGetJSON(t, ts.URL+"/api/meta", &meta)
	if meta.WorkspaceRoot != root {
		t.Fatalf("expected workspace root %q, got %#v", root, meta)
	}
	if meta.WorkspaceSwitchSupported {
		t.Fatalf("expected workspace switching to be disabled, got %#v", meta)
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

	resp, err = http.Post(ts.URL+"/api/skills/demo-skill/install", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("install request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected install to be unsupported, got %d body=%s", resp.StatusCode, string(body))
	}

	resp, err = http.Post(ts.URL+"/api/skills/demo-skill/uninstall", "application/json", strings.NewReader("{}"))
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
	file, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	defer file.Close()

	zipWriter := zip.NewWriter(file)
	entry, err := zipWriter.Create(filepath.ToSlash(filepath.Join(skillDir, "SKILL.md")))
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := entry.Write([]byte(skillMD)); err != nil {
		t.Fatalf("write skill md: %v", err)
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
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
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
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
