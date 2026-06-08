package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ngenrt "ngen/internal/runtime"
	"ngen/internal/task"
)

func TestServerHealthDoesNotRequireBearerToken(t *testing.T) {
	svc := ngenrt.New(t.TempDir(), task.DefaultConfig())
	handler := Server{Service: svc, Token: "secret"}.Handler()

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if resp.Code != http.StatusOK {
		t.Fatalf("expected healthz 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal healthz: %v", err)
	}
	if body["ok"] != true || body["service"] != "ngen" {
		t.Fatalf("unexpected healthz body: %#v", body)
	}
}

func TestServerRequiresBearerTokenForAPI(t *testing.T) {
	svc := ngenrt.New(t.TempDir(), task.DefaultConfig())
	handler := Server{Service: svc, Token: "secret"}.Handler()

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))

	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized API response, got %d body=%s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "valid bearer token required") {
		t.Fatalf("expected explicit auth diagnostic, got %s", resp.Body.String())
	}
}

func TestServerTaskAndSessionLifecycle(t *testing.T) {
	svc := ngenrt.New(t.TempDir(), task.DefaultConfig())
	handler := Server{Service: svc, Token: "secret"}.Handler()

	createResp := serveJSON(t, handler, http.MethodPost, "/api/tasks", "secret", map[string]any{
		"kind":      "coding",
		"title":     "web task",
		"objective": "exercise web backend",
		"criteria":  []string{"web backend creates and reads a task"},
	})
	if createResp.Code != http.StatusCreated {
		t.Fatalf("expected task create 201, got %d body=%s", createResp.Code, createResp.Body.String())
	}
	var view task.TaskView
	if err := json.Unmarshal(createResp.Body.Bytes(), &view); err != nil {
		t.Fatalf("unmarshal task view: %v", err)
	}
	if view.Task.TaskID == "" || view.Task.Title != "web task" {
		t.Fatalf("unexpected created task: %+v", view.Task)
	}

	listResp := serveJSON(t, handler, http.MethodGet, "/api/tasks", "secret", nil)
	if listResp.Code != http.StatusOK {
		t.Fatalf("expected task list 200, got %d body=%s", listResp.Code, listResp.Body.String())
	}
	var entries []task.TaskListEntry
	if err := json.Unmarshal(listResp.Body.Bytes(), &entries); err != nil {
		t.Fatalf("unmarshal task entries: %v", err)
	}
	if len(entries) != 1 || entries[0].TaskID != view.Task.TaskID {
		t.Fatalf("unexpected task entries: %+v", entries)
	}

	getResp := serveJSON(t, handler, http.MethodGet, "/api/tasks/"+view.Task.TaskID, "secret", nil)
	if getResp.Code != http.StatusOK {
		t.Fatalf("expected task get 200, got %d body=%s", getResp.Code, getResp.Body.String())
	}

	sessionResp := serveJSON(t, handler, http.MethodPost, "/api/sessions", "secret", map[string]any{
		"task_id": view.Task.TaskID,
	})
	if sessionResp.Code != http.StatusCreated {
		t.Fatalf("expected session start 201, got %d body=%s", sessionResp.Code, sessionResp.Body.String())
	}
	var session task.Session
	if err := json.Unmarshal(sessionResp.Body.Bytes(), &session); err != nil {
		t.Fatalf("unmarshal session: %v", err)
	}
	if session.SessionID == "" || session.Mode != "web" {
		t.Fatalf("expected web session, got %+v", session)
	}

	promptResp := serveJSON(t, handler, http.MethodPost, "/api/sessions/"+session.SessionID+"/prompt", "secret", map[string]any{
		"prompt": "hello",
	})
	if promptResp.Code != http.StatusOK {
		t.Fatalf("expected session prompt 200, got %d body=%s", promptResp.Code, promptResp.Body.String())
	}
	var promptResult promptSessionResponse
	if err := json.Unmarshal(promptResp.Body.Bytes(), &promptResult); err != nil {
		t.Fatalf("unmarshal prompt result: %v", err)
	}
	if promptResult.Session.LastPrompt != "hello" {
		t.Fatalf("expected prompt to update session, got %+v", promptResult.Session)
	}

	readResp := serveJSON(t, handler, http.MethodGet, "/api/sessions/"+session.SessionID, "secret", nil)
	if readResp.Code != http.StatusOK {
		t.Fatalf("expected session read 200, got %d body=%s", readResp.Code, readResp.Body.String())
	}
	var readResult sessionReadResponse
	if err := json.Unmarshal(readResp.Body.Bytes(), &readResult); err != nil {
		t.Fatalf("unmarshal read result: %v", err)
	}
	if len(readResult.Messages) < 2 {
		t.Fatalf("expected operator and runtime messages, got %+v", readResult.Messages)
	}
}

func TestServerMissionStatusRouteUsesRuntimeView(t *testing.T) {
	svc := ngenrt.New(t.TempDir(), task.DefaultConfig())
	created, err := svc.CreateMission(context.Background(), task.MissionCreateRequest{
		Title:     "web mission",
		Objective: "expose mission status over web",
		Criteria:  []string{"mission status route returns derived snapshot"},
	})
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}
	handler := Server{Service: svc, Token: "secret"}.Handler()

	resp := serveJSON(t, handler, http.MethodGet, "/api/missions/"+created.Mission.MissionID, "secret", nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected mission status 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	var view task.MissionView
	if err := json.Unmarshal(resp.Body.Bytes(), &view); err != nil {
		t.Fatalf("unmarshal mission view: %v", err)
	}
	if view.Mission.MissionID != created.Mission.MissionID || view.MissionStatusSnapshot == nil {
		t.Fatalf("expected mission view with status snapshot, got %+v", view)
	}
}

func TestServerTaskEventsStreamSendsSSESnapshot(t *testing.T) {
	svc := ngenrt.New(t.TempDir(), task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     "stream task",
		Objective: "stream runtime events",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "event stream emits existing task events"},
		},
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	event := task.Event{
		SchemaVersion: task.SchemaVersion,
		EventID:       "EVT-test-stream",
		TaskID:        spec.TaskID,
		TS:            task.Now(),
		Phase:         task.PhasePlan,
		State:         task.StateActive,
		Type:          "test_event",
		Summary:       "streamed event",
	}
	if err := svc.Store.AppendEvent(event); err != nil {
		t.Fatalf("append event: %v", err)
	}
	second := event
	second.EventID = "EVT-test-stream-after"
	second.Type = "test_event_after"
	second.Summary = "streamed event after cursor"
	if err := svc.Store.AppendEvent(second); err != nil {
		t.Fatalf("append second event: %v", err)
	}
	handler := Server{Service: svc, Token: "secret"}.Handler()

	jsonResp := serveJSON(t, handler, http.MethodGet, "/api/tasks/"+spec.TaskID+"/events?after=EVT-test-stream&limit=5", "secret", nil)
	if jsonResp.Code != http.StatusOK {
		t.Fatalf("expected events after cursor 200, got %d body=%s", jsonResp.Code, jsonResp.Body.String())
	}
	var events []task.Event
	if err := json.Unmarshal(jsonResp.Body.Bytes(), &events); err != nil {
		t.Fatalf("unmarshal events after cursor: %v", err)
	}
	if len(events) != 1 || events[0].EventID != "EVT-test-stream-after" {
		t.Fatalf("expected only later event from JSON cursor replay, got %+v", events)
	}
	staleResp := serveJSON(t, handler, http.MethodGet, "/api/tasks/"+spec.TaskID+"/events?after=EVT-missing", "secret", nil)
	if staleResp.Code != http.StatusNotFound || !strings.Contains(staleResp.Body.String(), "event cursor not found") {
		t.Fatalf("expected stale JSON cursor diagnostic, got %d body=%s", staleResp.Code, staleResp.Body.String())
	}

	resp := serveJSON(t, handler, http.MethodGet, "/api/tasks/"+spec.TaskID+"/events/stream?follow=false&limit=5", "secret", nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected events stream 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	if got := resp.Header().Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("expected text/event-stream content type, got %q", got)
	}
	body := resp.Body.String()
	for _, want := range []string{
		"id: EVT-test-stream",
		"id: EVT-test-stream-after",
		"event: task_event",
		`"task_id":"` + spec.TaskID + `"`,
		`"summary":"streamed event"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected SSE body to contain %q, got:\n%s", want, body)
		}
	}

	resp = serveJSON(t, handler, http.MethodGet, "/api/tasks/"+spec.TaskID+"/events/stream?follow=false&after=EVT-test-stream", "secret", nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected events stream after cursor 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	body = resp.Body.String()
	if strings.Contains(body, "streamed event\"") || !strings.Contains(body, "streamed event after cursor") {
		t.Fatalf("expected after cursor stream to include only later event, got:\n%s", body)
	}
}

func TestServerTaskEventsStreamRejectsUnknownTask(t *testing.T) {
	svc := ngenrt.New(t.TempDir(), task.DefaultConfig())
	handler := Server{Service: svc, Token: "secret"}.Handler()

	resp := serveJSON(t, handler, http.MethodGet, "/api/tasks/TASK-missing/events/stream?follow=false", "secret", nil)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("expected missing task stream to return 404, got %d body=%s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "task_get_failed") {
		t.Fatalf("expected explicit missing task diagnostic, got %s", resp.Body.String())
	}
}

func serveJSON(t *testing.T, handler http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		reader = bytes.NewReader(payload)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req.WithContext(context.Background()))
	return resp
}
