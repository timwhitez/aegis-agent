package webconsole

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	if !strings.Contains(indexBody, "Agent Console") || !strings.Contains(indexBody, "Ask anything...") || !strings.Contains(indexBody, "clear-chat-btn") {
		t.Fatalf("unexpected shell body: %s", indexBody)
	}

	jsBody := checkBody(server.URL + "/app.js")
	if !strings.Contains(jsBody, "setupWebSocket") || !strings.Contains(jsBody, "resetChatSession") || !strings.Contains(jsBody, "fetchSkills") {
		t.Fatalf("unexpected app.js body: %s", jsBody)
	}

	cssBody := checkBody(server.URL + "/styles.css")
	if !strings.Contains(cssBody, "--accent") || !strings.Contains(cssBody, ".sidebar") || !strings.Contains(cssBody, ".chat-container") {
		t.Fatalf("unexpected styles.css body: %s", cssBody)
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
