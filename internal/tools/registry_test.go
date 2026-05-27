package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/session"
	"go-cli-agent/internal/skills"
)

type failingPlanInputResponder struct {
	err error
}

func (f failingPlanInputResponder) RequestPlanInput(context.Context, string, session.PlanModeInputRequest) ([]session.PlanModeInputAnswer, error) {
	return nil, f.err
}

type recordingPlanInputResponder struct {
	calls int
}

func (r *recordingPlanInputResponder) RequestPlanInput(context.Context, string, session.PlanModeInputRequest) ([]session.PlanModeInputAnswer, error) {
	r.calls++
	return []session.PlanModeInputAnswer{{
		QuestionID: "scope_choice",
		Label:      "Narrow (Recommended)",
		Value:      "Keep the implementation focused.",
	}}, nil
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func TestBuiltinToolSchemasDisallowUnknownProperties(t *testing.T) {
	cfg := config.Default()
	registry, err := NewRegistry(cfg, nil, session.NewStore(t.TempDir()), nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	for _, def := range registry.Definitions() {
		assertObjectSchemasClosed(t, def.Name, def.InputSchema)
	}
}

func TestBuiltinToolExecutionRejectsUnknownTopLevelField(t *testing.T) {
	cfg := config.Default()
	registry, err := NewRegistry(cfg, nil, session.NewStore(t.TempDir()), nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "shell", ExecContext{}, json.RawMessage(`{"command":"pwd","extra":true}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, `unexpected field "extra"`) {
		t.Fatalf("expected unknown top-level field rejection, got %#v", result)
	}
}

func TestBuiltinToolExecutionRejectsTrailingJSONValue(t *testing.T) {
	cfg := config.Default()
	registry, err := NewRegistry(cfg, nil, session.NewStore(t.TempDir()), nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "todo_read", ExecContext{}, json.RawMessage(`{} {}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "single JSON value") {
		t.Fatalf("expected trailing JSON rejection, got %#v", result)
	}
}

func TestBuiltinToolExecutionRejectsNestedUnknownField(t *testing.T) {
	cfg := config.Default()
	registry, err := NewRegistry(cfg, nil, session.NewStore(t.TempDir()), nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "todo_write", ExecContext{}, json.RawMessage(`{
		"todos":[{"content":"x","status":"pending","extra":true}]
	}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, `unexpected field "todos[0].extra"`) {
		t.Fatalf("expected nested unknown field rejection, got %#v", result)
	}
}

func assertObjectSchemasClosed(t *testing.T, path string, value any) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		if schemaType, _ := typed["type"].(string); schemaType == "object" {
			if typed["additionalProperties"] != false {
				t.Fatalf("%s object schema must set additionalProperties=false, got %#v", path, typed["additionalProperties"])
			}
		}
		for key, child := range typed {
			assertObjectSchemasClosed(t, path+"."+key, child)
		}
	case []any:
		for i, child := range typed {
			assertObjectSchemasClosed(t, fmt.Sprintf("%s[%d]", path, i), child)
		}
	}
}

func TestTodoAndTaskToolsEmitStructuredEvents(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
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
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}

	var eventTypes []string
	execCtx := ExecContext{
		SessionID: meta.ID,
		Workdir:   meta.Workdir,
		Store:     store,
		Config:    cfg,
		Emit: func(eventType string, _ map[string]any) {
			eventTypes = append(eventTypes, eventType)
		},
	}

	todoResult, err := registry.Execute(context.Background(), "todo_write", execCtx, json.RawMessage(`{
		"todos":[{"content":"Audit provider contracts","status":"in_progress","priority":"high"}]
	}`))
	if err != nil {
		t.Fatalf("todo_write: %v", err)
	}
	if todoResult.Metadata["path"] != filepath.Join(store.SessionDir(meta.ID), "todo.json") {
		t.Fatalf("expected todo_write path metadata, got %#v", todoResult.Metadata)
	}
	if todoResult.Metadata["count"] != 1 {
		t.Fatalf("expected todo_write count metadata, got %#v", todoResult.Metadata)
	}
	createResult, err := registry.Execute(context.Background(), "task_create", execCtx, json.RawMessage(`{
		"subject":"Implement steer watcher coverage",
		"priority":"high"
	}`))
	if err != nil {
		t.Fatalf("task_create: %v", err)
	}
	if createResult.Metadata["path"] != filepath.Join(store.SessionDir(meta.ID), "tasks", "task_0001.json") {
		t.Fatalf("expected task_create path metadata, got %#v", createResult.Metadata)
	}
	if createResult.Metadata["task_id"] != "task_0001" {
		t.Fatalf("expected task_create task_id metadata, got %#v", createResult.Metadata)
	}
	updateResult, err := registry.Execute(context.Background(), "task_update", execCtx, json.RawMessage(`{
		"task_id":"task_0001",
		"status":"completed",
		"append_note":"covered by unit test"
	}`))
	if err != nil {
		t.Fatalf("task_update: %v", err)
	}
	if updateResult.Metadata["path"] != filepath.Join(store.SessionDir(meta.ID), "tasks", "task_0001.json") {
		t.Fatalf("expected task_update path metadata, got %#v", updateResult.Metadata)
	}
	if updateResult.Metadata["task_id"] != "task_0001" {
		t.Fatalf("expected task_update task_id metadata, got %#v", updateResult.Metadata)
	}
	cancelled, err := session.CreateTask(store, meta.ID, session.TaskCreateInput{Subject: "Cancelled task"})
	if err != nil {
		t.Fatalf("create cancelled task: %v", err)
	}
	if _, err := session.UpdateTask(store, meta.ID, session.TaskUpdateInput{TaskID: cancelled.ID, Status: "cancelled"}); err != nil {
		t.Fatalf("cancel task: %v", err)
	}
	listResult, err := registry.Execute(context.Background(), "task_list", execCtx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("task_list: %v", err)
	}
	if listResult.Metadata["tasks_dir"] != filepath.Join(store.SessionDir(meta.ID), "tasks") {
		t.Fatalf("expected task_list tasks_dir metadata, got %#v", listResult.Metadata)
	}
	if listResult.Metadata["completed_count"] != 1 || listResult.Metadata["cancelled_count"] != 1 || listResult.Metadata["done_count"] != 2 {
		t.Fatalf("expected task_list to report completed/cancelled/done counts separately, got %#v", listResult.Metadata)
	}
	getResult, err := registry.Execute(context.Background(), "task_get", execCtx, json.RawMessage(`{"task_id":"task_0001"}`))
	if err != nil {
		t.Fatalf("task_get: %v", err)
	}
	if getResult.Metadata["path"] != filepath.Join(store.SessionDir(meta.ID), "tasks", "task_0001.json") {
		t.Fatalf("expected task_get path metadata, got %#v", getResult.Metadata)
	}

	expected := []string{"todo.updated", "task.created", "task.updated"}
	if len(eventTypes) != len(expected) {
		t.Fatalf("expected %d events, got %d: %#v", len(expected), len(eventTypes), eventTypes)
	}
	for i, want := range expected {
		if eventTypes[i] != want {
			t.Fatalf("expected event %q at index %d, got %#v", want, i, eventTypes)
		}
	}
}

func TestRequestUserInputResponderErrorKeepsRecoverablePendingRequest(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
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
	if _, err := store.CreatePlanMode(meta.ID, session.PlanModeDraft{Enabled: true, Objective: "Resolve one planning decision"}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{
		SessionID:          meta.ID,
		ToolCallID:         "call_plan_input",
		Workdir:            meta.Workdir,
		Store:              store,
		Config:             cfg,
		PlanInputResponder: failingPlanInputResponder{err: errors.New("web input handle lost")},
	}
	result, err := registry.Execute(context.Background(), "request_user_input", execCtx, json.RawMessage(`{
		"questions":[{
			"id":"scope_choice",
			"header":"Scope",
			"question":"Which scope should the plan use?",
			"options":[
				{"label":"Narrow (Recommended)","description":"Keep the implementation focused."},
				{"label":"Broad","description":"Include adjacent cleanup."}
			]
		}]
	}`))
	if err != nil {
		t.Fatalf("request_user_input execute: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "web input handle lost") {
		t.Fatalf("expected responder error tool result, got %#v", result)
	}
	planMode, err := store.LoadPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("load plan mode: %v", err)
	}
	if planMode.Status != session.PlanModeStatusAwaitingUserInput || planMode.PendingRequest == nil {
		t.Fatalf("expected recoverable pending request to remain, got %#v", planMode)
	}
	if planMode.PendingRequest.ToolCallID != "call_plan_input" {
		t.Fatalf("expected persisted tool call id, got %#v", planMode.PendingRequest)
	}
	loadedState, err := store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if loadedState.Status != session.StatusAwaitingInput || loadedState.Phase != "plan_input" {
		t.Fatalf("expected plan input awaiting state, got %#v", loadedState)
	}
}

func TestRequestUserInputReportsStateLoadErrorBeforeResponder(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
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
	if _, err := store.CreatePlanMode(meta.ID, session.PlanModeDraft{Enabled: true, Objective: "Resolve one planning decision"}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	statePath := filepath.Join(store.SessionDir(meta.ID), "state.json")
	if err := os.Remove(statePath); err != nil {
		t.Fatalf("remove state.json: %v", err)
	}
	if err := os.Mkdir(statePath, 0o700); err != nil {
		t.Fatalf("replace state.json with directory: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	responder := &recordingPlanInputResponder{}
	result, err := registry.Execute(context.Background(), "request_user_input", ExecContext{
		SessionID:          meta.ID,
		ToolCallID:         "call_plan_input",
		Workdir:            meta.Workdir,
		Store:              store,
		Config:             cfg,
		PlanInputResponder: responder,
	}, json.RawMessage(`{
		"questions":[{
			"id":"scope_choice",
			"header":"Scope",
			"question":"Which scope should the plan use?",
			"options":[
				{"label":"Narrow (Recommended)","description":"Keep the implementation focused."},
				{"label":"Broad","description":"Include adjacent cleanup."}
			]
		}]
	}`))
	if err != nil {
		t.Fatalf("request_user_input execute: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "state.json") {
		t.Fatalf("expected state load error result, got %#v", result)
	}
	if responder.calls != 0 {
		t.Fatalf("responder must not be called after state load failure, got %d calls", responder.calls)
	}
}

func TestRequestUserInputReportsStateSaveErrorBeforeResponder(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
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
	if _, err := store.CreatePlanMode(meta.ID, session.PlanModeDraft{Enabled: true, Objective: "Resolve one planning decision"}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	steerPath := filepath.Join(store.SessionDir(meta.ID), "control", "steer.jsonl")
	if err := os.Remove(steerPath); err != nil {
		t.Fatalf("remove steer.jsonl: %v", err)
	}
	if err := os.Mkdir(steerPath, 0o700); err != nil {
		t.Fatalf("replace steer.jsonl with directory: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	responder := &recordingPlanInputResponder{}
	result, err := registry.Execute(context.Background(), "request_user_input", ExecContext{
		SessionID:          meta.ID,
		ToolCallID:         "call_plan_input",
		Workdir:            meta.Workdir,
		Store:              store,
		Config:             cfg,
		PlanInputResponder: responder,
	}, json.RawMessage(`{
		"questions":[{
			"id":"scope_choice",
			"header":"Scope",
			"question":"Which scope should the plan use?",
			"options":[
				{"label":"Narrow (Recommended)","description":"Keep the implementation focused."},
				{"label":"Broad","description":"Include adjacent cleanup."}
			]
		}]
	}`))
	if err != nil {
		t.Fatalf("request_user_input execute: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "steer.jsonl") {
		t.Fatalf("expected state save error result, got %#v", result)
	}
	if responder.calls != 0 {
		t.Fatalf("responder must not be called after state save failure, got %d calls", responder.calls)
	}
}

func TestRequestUserInputReportsRequiredEventErrorBeforeResponder(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := store.CreatePlanMode(meta.ID, session.PlanModeDraft{Enabled: true, Objective: "Resolve one planning decision"}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	responder := &recordingPlanInputResponder{}
	eventErr := errors.New("events.jsonl blocked")
	result, err := registry.Execute(context.Background(), "request_user_input", ExecContext{
		SessionID:          meta.ID,
		ToolCallID:         "call_plan_input",
		Workdir:            meta.Workdir,
		Store:              store,
		Config:             cfg,
		PlanInputResponder: responder,
		EmitRequired: func(eventType string, _ map[string]any) error {
			if eventType != "planmode.input_requested" {
				t.Fatalf("unexpected required event %q", eventType)
			}
			return eventErr
		},
	}, json.RawMessage(`{
		"questions":[{
			"id":"scope_choice",
			"header":"Scope",
			"question":"Which scope should the plan use?",
			"options":[
				{"label":"Narrow (Recommended)","description":"Keep the implementation focused."},
				{"label":"Broad","description":"Include adjacent cleanup."}
			]
		}]
	}`))
	if err != nil {
		t.Fatalf("request_user_input execute: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "planmode.input_requested") || !strings.Contains(result.DisplayOutput, eventErr.Error()) {
		t.Fatalf("expected required event error result, got %#v", result)
	}
	if responder.calls != 0 {
		t.Fatalf("responder must not be called after input_requested event failure, got %d calls", responder.calls)
	}
	planMode, err := store.LoadPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("load plan mode: %v", err)
	}
	if planMode.Status != session.PlanModeStatusAwaitingUserInput || planMode.PendingRequest == nil {
		t.Fatalf("expected recoverable pending request after event failure, got %#v", planMode)
	}
	loadedState, err := store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if loadedState.Status != session.StatusAwaitingInput || loadedState.Phase != "plan_input" {
		t.Fatalf("expected awaiting input state after event failure, got %#v", loadedState)
	}
}

func TestRequestUserInputReportsAnsweredEventErrorAndRestoresPendingRequest(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := store.CreatePlanMode(meta.ID, session.PlanModeDraft{Enabled: true, Objective: "Resolve one planning decision"}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	responder := &recordingPlanInputResponder{}
	eventErr := errors.New("events.jsonl blocked")
	result, err := registry.Execute(context.Background(), "request_user_input", ExecContext{
		SessionID:          meta.ID,
		ToolCallID:         "call_plan_input",
		Workdir:            meta.Workdir,
		Store:              store,
		Config:             cfg,
		PlanInputResponder: responder,
		EmitRequired: func(eventType string, _ map[string]any) error {
			switch eventType {
			case "planmode.input_requested":
				return nil
			case "planmode.input_answered":
				return eventErr
			default:
				t.Fatalf("unexpected required event %q", eventType)
				return nil
			}
		},
	}, json.RawMessage(`{
		"questions":[{
			"id":"scope_choice",
			"header":"Scope",
			"question":"Which scope should the plan use?",
			"options":[
				{"label":"Narrow (Recommended)","description":"Keep the implementation focused."},
				{"label":"Broad","description":"Include adjacent cleanup."}
			]
		}]
	}`))
	if err != nil {
		t.Fatalf("request_user_input execute: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "planmode.input_answered") || !strings.Contains(result.DisplayOutput, eventErr.Error()) {
		t.Fatalf("expected planmode.input_answered event error result, got %#v", result)
	}
	if responder.calls != 1 {
		t.Fatalf("responder should be called once before answered event failure, got %d calls", responder.calls)
	}
	planMode, err := store.LoadPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("load plan mode: %v", err)
	}
	if planMode.Status != session.PlanModeStatusAwaitingUserInput || planMode.PendingRequest == nil {
		t.Fatalf("expected pending request restored after answered event failure, got %#v", planMode)
	}
	if planMode.PendingRequest.RequestID == "" || planMode.PendingRequest.ToolCallID != "call_plan_input" {
		t.Fatalf("expected original request metadata restored, got %#v", planMode.PendingRequest)
	}
	if planMode.PendingRequest.Status != "pending" || len(planMode.PendingRequest.Answers) != 0 {
		t.Fatalf("expected unanswered pending request restored, got %#v", planMode.PendingRequest)
	}
	history, err := store.LoadPlanModeHistory(meta.ID)
	if err != nil {
		t.Fatalf("load plan mode history: %v", err)
	}
	for _, entry := range history {
		if entry.Type == "planmode.input_answered" {
			t.Fatalf("failed answered event append must not leave input_answered history, got %#v", history)
		}
	}
	loadedState, err := store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if loadedState.Status != session.StatusAwaitingInput || loadedState.Phase != "plan_input" {
		t.Fatalf("expected awaiting input state after answered event failure, got %#v", loadedState)
	}
}

func TestRequestUserInputWithoutResponderFailsBeforePendingRequest(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyAutonomous,
	}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := store.CreatePlanMode(meta.ID, session.PlanModeDraft{Enabled: true, Objective: "Plan without an interactive responder"}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "request_user_input", ExecContext{
		SessionID:  meta.ID,
		ToolCallID: "call_no_responder",
		Workdir:    meta.Workdir,
		Store:      store,
		Config:     cfg,
	}, json.RawMessage(`{
		"questions":[{
			"id":"scope_choice",
			"header":"Scope",
			"question":"Which scope should the plan use?",
			"options":[
				{"label":"Narrow (Recommended)","description":"Keep the implementation focused."},
				{"label":"Broad","description":"Include adjacent cleanup."}
			]
		}]
	}`))
	if err != nil {
		t.Fatalf("request_user_input execute: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "interactive responder") {
		t.Fatalf("expected no-responder error result, got %#v", result)
	}
	planMode, err := store.LoadPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("load plan mode: %v", err)
	}
	if planMode.Status != session.PlanModeStatusPlanning || planMode.PendingRequest != nil {
		t.Fatalf("no-responder path must not leave pending request, got %#v", planMode)
	}
}

func TestSubmitPlanReportsRequiredEventErrorAndRestoresPlanMode(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	store := session.NewStore(filepath.Join(root, "sessions"))
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          root,
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := store.CreatePlanMode(meta.ID, session.PlanModeDraft{Enabled: true, Objective: "Plan safely"}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	previousPlanMode, err := store.LoadPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("load previous plan mode: %v", err)
	}
	previousHistory, err := store.LoadPlanModeHistory(meta.ID)
	if err != nil {
		t.Fatalf("load previous plan mode history: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	eventErr := errors.New("events.jsonl blocked")
	execCtx := ExecContext{
		SessionID: meta.ID,
		Workdir:   root,
		Store:     store,
		Config:    cfg,
		EmitRequired: func(eventType string, _ map[string]any) error {
			if eventType != "planmode.plan_submitted" {
				t.Fatalf("unexpected required event %q", eventType)
			}
			return eventErr
		},
	}

	result, err := registry.Execute(context.Background(), "submit_plan", execCtx, json.RawMessage(`{
		"title":"Safe plan",
		"summary":"Plan safely.",
		"plan_markdown":"# Summary\n\nPlan safely.\n\n# Verification\n\nRun focused tests.",
		"verification":["go test ./internal/tools"]
	}`))
	if err != nil {
		t.Fatalf("submit_plan execute: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "planmode.plan_submitted") || !strings.Contains(result.DisplayOutput, eventErr.Error()) {
		t.Fatalf("expected planmode.plan_submitted event error result, got %#v", result)
	}
	planMode, err := store.LoadPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("load restored plan mode: %v", err)
	}
	if planMode.Status != previousPlanMode.Status || planMode.PlanVersion != previousPlanMode.PlanVersion || planMode.PlanMarkdown != previousPlanMode.PlanMarkdown {
		t.Fatalf("expected previous plan mode restored, got %#v", planMode)
	}
	history, err := store.LoadPlanModeHistory(meta.ID)
	if err != nil {
		t.Fatalf("load restored plan mode history: %v", err)
	}
	if len(history) != len(previousHistory) {
		t.Fatalf("expected history restored to %d entries, got %d: %#v", len(previousHistory), len(history), history)
	}
	for _, entry := range history {
		if entry.Type == "planmode.plan_submitted" {
			t.Fatalf("failed event append must not leave planmode.plan_submitted history, got %#v", history)
		}
	}
	planPath := filepath.Join(store.SessionDir(meta.ID), "artifacts", "planmode-plan.md")
	if _, err := os.Stat(planPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected failed submit to remove generated plan markdown, err=%v", err)
	}
}

func TestGoalToolsCreateReadRejectInvalidStatusAndComplete(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
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
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	var eventTypes []string
	execCtx := ExecContext{
		SessionID: meta.ID,
		Workdir:   meta.Workdir,
		Store:     store,
		Config:    cfg,
		Emit: func(eventType string, _ map[string]any) {
			eventTypes = append(eventTypes, eventType)
		},
	}

	createResult, err := registry.Execute(context.Background(), "create_goal", execCtx, json.RawMessage(`{
		"objective":"Ship goal support",
		"mode":"mission",
		"token_budget":100,
		"time_budget_minutes":5,
		"success_criteria":["goal persisted"],
		"validation_plan":["go test ./internal/tools"],
		"features":["web api"],
		"milestones":["first validation"],
		"require_plan_approval":true
	}`))
	if err != nil {
		t.Fatalf("create_goal execute: %v", err)
	}
	if createResult.IsError {
		t.Fatalf("create_goal returned error: %s", createResult.DisplayOutput)
	}
	if createResult.Metadata["status"] != session.GoalStatusActive {
		t.Fatalf("expected active goal metadata, got %#v", createResult.Metadata)
	}

	readResult, err := registry.Execute(context.Background(), "get_goal", execCtx, nil)
	if err != nil {
		t.Fatalf("get_goal execute: %v", err)
	}
	if !strings.Contains(readResult.LLMOutput, `"mode": "mission"`) || !strings.Contains(readResult.LLMOutput, `"needs_approval"`) {
		t.Fatalf("expected mission goal JSON, got %s", readResult.LLMOutput)
	}

	rejectResult, err := registry.Execute(context.Background(), "update_goal", execCtx, json.RawMessage(`{"status":"paused"}`))
	if err != nil {
		t.Fatalf("update_goal paused execute: %v", err)
	}
	if !rejectResult.IsError || !strings.Contains(rejectResult.DisplayOutput, "can only mark the existing goal complete") {
		t.Fatalf("expected paused update rejection, got %#v", rejectResult)
	}

	progressResult, err := registry.Execute(context.Background(), "record_goal_progress", execCtx, json.RawMessage(`{
		"kind":"handoff",
		"summary":"feature implemented and handed off",
		"linked_artifacts":["reports/progress.md"],
		"commands":[{"command":"go test ./internal/tools","exit_code":0,"summary":"passed"}],
		"blockers":["browser smoke pending"],
		"feature_updates":[{"id":"feature_0001","status":"completed","claimed_assertions":["validation_0001"],"evidence":["feature evidence"],"child_session_ids":["child_eval"]}],
		"milestone_updates":[{"id":"milestone_0001","status":"completed","validation_ids":["validation_0001"]}],
		"validation_updates":[{"id":"validation_0001","status":"verified","evidence":["validator evidence"],"evaluator_evidence":[{"child_session_id":"child_eval","summary":"independent evaluator passed","status":"verified"}]}]
	}`))
	if err != nil {
		t.Fatalf("record_goal_progress execute: %v", err)
	}
	if progressResult.IsError {
		t.Fatalf("record_goal_progress returned error: %s", progressResult.DisplayOutput)
	}
	progressGoal, err := store.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load progress goal: %v", err)
	}
	if len(progressGoal.Progress) != 1 || progressGoal.Progress[0].Kind != "handoff" {
		t.Fatalf("expected progress record in goal snapshot, got %#v", progressGoal.Progress)
	}
	if progressGoal.Mission.Features[0].Status != "completed" || len(progressGoal.Mission.ValidationContract[0].EvaluatorEvidence) != 1 {
		t.Fatalf("expected feature and evaluator evidence updates, got goal=%#v", progressGoal)
	}

	completeResult, err := registry.Execute(context.Background(), "update_goal", execCtx, json.RawMessage(`{
		"status":"complete",
		"completion_summary":"Tools goal support is complete.",
		"evidence":["go test ./internal/tools"],
		"criteria_statuses":[{"id":"criterion_0001","status":"verified","evidence":["goal persisted"]}],
		"validation_statuses":[{"id":"validation_0001","status":"verified","evidence":["go test ./internal/tools"]}]
	}`))
	if err != nil {
		t.Fatalf("update_goal complete execute: %v", err)
	}
	if completeResult.IsError {
		t.Fatalf("update_goal complete returned error: %s", completeResult.DisplayOutput)
	}
	goal, err := store.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load goal: %v", err)
	}
	if goal.Status != session.GoalStatusComplete || goal.CompletedAt == "" {
		t.Fatalf("expected completed goal, got %#v", goal)
	}
	if goal.CompletionAudit == nil || !containsString(goal.CompletionAudit.Evidence, "go test ./internal/tools") {
		t.Fatalf("expected completion audit evidence in goal snapshot, got %#v", goal.CompletionAudit)
	}
	if goal.SuccessCriteria[0].Status != "verified" || goal.ValidationPlan[0].Status != "verified" {
		t.Fatalf("expected item status evidence in goal snapshot, got criteria=%#v validation=%#v", goal.SuccessCriteria, goal.ValidationPlan)
	}
	if strings.Join(eventTypes, ",") != "goal.created,planmode.created,goal.progress.recorded,goal.completed" {
		t.Fatalf("expected goal event emissions, got %#v", eventTypes)
	}
}

func TestCreateGoalReportsRequiredEventErrorAndRestoresGoal(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	store := session.NewStore(filepath.Join(root, "sessions"))
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          root,
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	previousHistory, err := store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load previous goal history: %v", err)
	}
	previousTasks, err := store.ListTasks(meta.ID)
	if err != nil {
		t.Fatalf("load previous tasks: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	eventErr := errors.New("events.jsonl blocked")
	execCtx := ExecContext{
		SessionID: meta.ID,
		Workdir:   root,
		Store:     store,
		Config:    cfg,
		EmitRequired: func(eventType string, _ map[string]any) error {
			if eventType != "goal.created" {
				t.Fatalf("unexpected required event %q", eventType)
			}
			return eventErr
		},
	}

	result, err := registry.Execute(context.Background(), "create_goal", execCtx, json.RawMessage(`{
		"objective":"Create a plain goal with a required event"
	}`))
	if err != nil {
		t.Fatalf("create_goal execute: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "goal.created") || !strings.Contains(result.DisplayOutput, eventErr.Error()) {
		t.Fatalf("expected goal.created event error result, got %#v", result)
	}
	if _, err := store.LoadGoal(meta.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected failed create_goal to remove goal snapshot, got %v", err)
	}
	history, err := store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load restored goal history: %v", err)
	}
	if len(history) != len(previousHistory) {
		t.Fatalf("expected history restored to %d entries, got %d: %#v", len(previousHistory), len(history), history)
	}
	tasks, err := store.ListTasks(meta.ID)
	if err != nil {
		t.Fatalf("list restored tasks: %v", err)
	}
	if len(tasks) != len(previousTasks) {
		t.Fatalf("expected tasks restored to %d entries, got %d: %#v", len(previousTasks), len(tasks), tasks)
	}
}

func TestCreateGoalReportsLinkedPlanModeEventErrorAndRestoresGoal(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	store := session.NewStore(filepath.Join(root, "sessions"))
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          root,
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	previousHistory, err := store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load previous goal history: %v", err)
	}
	previousTasks, err := store.ListTasks(meta.ID)
	if err != nil {
		t.Fatalf("load previous tasks: %v", err)
	}
	previousPlanMode, err := store.SnapshotPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("snapshot previous plan mode: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	eventErr := errors.New("events.jsonl blocked")
	execCtx := ExecContext{
		SessionID: meta.ID,
		Workdir:   root,
		Store:     store,
		Config:    cfg,
		EmitRequired: func(eventType string, _ map[string]any) error {
			switch eventType {
			case "goal.created":
				return nil
			case "planmode.created":
				return eventErr
			default:
				t.Fatalf("unexpected required event %q", eventType)
				return nil
			}
		},
	}

	result, err := registry.Execute(context.Background(), "create_goal", execCtx, json.RawMessage(`{
		"objective":"Create a mission that requires a linked Plan Mode gate",
		"mode":"mission",
		"require_plan_approval":true
	}`))
	if err != nil {
		t.Fatalf("create_goal execute: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "planmode.created") || !strings.Contains(result.DisplayOutput, eventErr.Error()) {
		t.Fatalf("expected linked planmode.created event error result, got %#v", result)
	}
	if _, err := store.LoadGoal(meta.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected failed linked Plan Mode event to remove goal snapshot, got %v", err)
	}
	restoredPlanMode, err := store.SnapshotPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("snapshot restored plan mode: %v", err)
	}
	if restoredPlanMode.HasState != previousPlanMode.HasState || restoredPlanMode.State.PlanModeID != previousPlanMode.State.PlanModeID {
		t.Fatalf("expected previous Plan Mode snapshot restored, before=%#v after=%#v", previousPlanMode, restoredPlanMode)
	}
	history, err := store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load restored goal history: %v", err)
	}
	if len(history) != len(previousHistory) {
		t.Fatalf("expected goal history restored to %d entries, got %d: %#v", len(previousHistory), len(history), history)
	}
	tasks, err := store.ListTasks(meta.ID)
	if err != nil {
		t.Fatalf("list restored tasks: %v", err)
	}
	if len(tasks) != len(previousTasks) {
		t.Fatalf("expected tasks restored to %d entries, got %d: %#v", len(previousTasks), len(tasks), tasks)
	}
	planHistory, err := store.LoadPlanModeHistory(meta.ID)
	if err != nil {
		t.Fatalf("load plan mode history: %v", err)
	}
	for _, entry := range planHistory {
		if entry.Type == "planmode.created" {
			t.Fatalf("failed linked Plan Mode event must not leave planmode.created history, got %#v", planHistory)
		}
	}
}

func TestUpdateGoalReportsRequiredEventErrorAndRestoresGoal(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	store := session.NewStore(filepath.Join(root, "sessions"))
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          root,
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:         true,
		Objective:       "Keep the goal active when completion event persistence fails",
		SuccessCriteria: []string{"criterion remains pending"},
		ValidationPlan:  []string{"validation remains pending"},
		Source:          session.GoalSourceTool,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	previousGoal, err := store.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load previous goal: %v", err)
	}
	previousHistory, err := store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load previous history: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	eventErr := errors.New("events.jsonl blocked")
	execCtx := ExecContext{
		SessionID: meta.ID,
		Workdir:   root,
		Store:     store,
		Config:    cfg,
		EmitRequired: func(eventType string, _ map[string]any) error {
			if eventType != "goal.completed" {
				t.Fatalf("unexpected required event %q", eventType)
			}
			return eventErr
		},
	}

	result, err := registry.Execute(context.Background(), "update_goal", execCtx, json.RawMessage(`{
		"status":"complete",
		"completion_summary":"This should roll back.",
		"evidence":["blocked events path"],
		"criteria_statuses":[{"id":"criterion_0001","status":"verified","evidence":["should not persist"]}],
		"validation_statuses":[{"id":"validation_0001","status":"verified","evidence":["should not persist"]}]
	}`))
	if err != nil {
		t.Fatalf("update_goal execute: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "goal.completed") || !strings.Contains(result.DisplayOutput, eventErr.Error()) {
		t.Fatalf("expected goal.completed event error result, got %#v", result)
	}
	goal, err := store.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load restored goal: %v", err)
	}
	if goal.Status != previousGoal.Status || goal.CompletedAt != "" || goal.CompletionAudit != nil {
		t.Fatalf("expected active goal restored after event failure, got %#v", goal)
	}
	if goal.SuccessCriteria[0].Status != previousGoal.SuccessCriteria[0].Status || goal.ValidationPlan[0].Status != previousGoal.ValidationPlan[0].Status {
		t.Fatalf("expected goal item statuses restored, got criteria=%#v validation=%#v", goal.SuccessCriteria, goal.ValidationPlan)
	}
	history, err := store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load restored history: %v", err)
	}
	if len(history) != len(previousHistory) {
		t.Fatalf("expected history restored to %d entries, got %d: %#v", len(previousHistory), len(history), history)
	}
	for _, entry := range history {
		if entry.Type == "goal.completed" {
			t.Fatalf("failed event append must not leave goal.completed history, got %#v", history)
		}
	}
}

func TestShellAndFileToolsEmitCompactionMetadata(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
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
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}

	execCtx := ExecContext{
		SessionID: meta.ID,
		Workdir:   meta.Workdir,
		Store:     store,
		Config:    cfg,
	}

	shellResult, err := registry.Execute(context.Background(), "shell", execCtx, json.RawMessage(`{
		"command":"yes A | head -n 7000"
	}`))
	if err != nil {
		t.Fatalf("shell: %v", err)
	}
	if shellResult.Metadata["truncated"] != true {
		t.Fatalf("expected truncated shell output metadata, got %#v", shellResult.Metadata)
	}
	if shellResult.Metadata["raw_length"] == nil {
		t.Fatalf("expected raw_length metadata, got %#v", shellResult.Metadata)
	}
	if shellResult.Metadata["command"] != "yes A | head -n 7000" {
		t.Fatalf("expected command metadata, got %#v", shellResult.Metadata)
	}

	writeResult, err := registry.Execute(context.Background(), "write_file", execCtx, json.RawMessage(`{
		"path":"notes.txt",
		"content":"hello"
	}`))
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if writeResult.Metadata["path"] == nil {
		t.Fatalf("expected write_file path metadata, got %#v", writeResult.Metadata)
	}

	editResult, err := registry.Execute(context.Background(), "edit_file", execCtx, json.RawMessage(`{
		"path":"notes.txt",
		"old_text":"hello",
		"new_text":"hello world"
	}`))
	if err != nil {
		t.Fatalf("edit_file: %v", err)
	}
	if editResult.Metadata["path"] == nil {
		t.Fatalf("expected edit_file path metadata, got %#v", editResult.Metadata)
	}
}

func TestWriteAndEditToolsApplyWorkspaceWriteDenylist(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, ".go-cli-agent"), 0o700); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, ".go-cli-agent", "config.yaml"), []byte("old"), 0o600); err != nil {
		t.Fatalf("write state file: %v", err)
	}
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
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
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}

	writeResult, err := registry.Execute(context.Background(), "write_file", execCtx, json.RawMessage(`{
		"path":".git/config",
		"content":"bad"
	}`))
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if !writeResult.IsError || !strings.Contains(writeResult.LLMOutput, "write denied: path '.git/config' matches deny pattern '.git/'") {
		t.Fatalf("expected write deny result, got %#v", writeResult)
	}

	editResult, err := registry.Execute(context.Background(), "edit_file", execCtx, json.RawMessage(`{
		"path":".go-cli-agent/config.yaml",
		"old_text":"old",
		"new_text":"new"
	}`))
	if err != nil {
		t.Fatalf("edit_file: %v", err)
	}
	if !editResult.IsError || !strings.Contains(editResult.LLMOutput, "write denied: path '.go-cli-agent/config.yaml' matches deny pattern '.go-cli-agent/'") {
		t.Fatalf("expected edit deny result, got %#v", editResult)
	}

	caseFoldResult, err := registry.Execute(context.Background(), "write_file", execCtx, json.RawMessage(`{
		"path":".ENV",
		"content":"bad"
	}`))
	if err != nil {
		t.Fatalf("write_file case-fold: %v", err)
	}
	if !caseFoldResult.IsError || !strings.Contains(caseFoldResult.LLMOutput, "write denied") {
		t.Fatalf("expected case-folded deny result, got %#v", caseFoldResult)
	}
}

func TestWriteFileRejectsSymlinkedTempAlias(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(workdir, "note.txt.tmp")); err != nil {
		t.Fatalf("symlink predictable tmp: %v", err)
	}
	meta := session.SessionMetadata{SchemaVersion: 1, ID: session.NewSessionID(), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: workdir, Mode: session.ModeRun, Provider: "fake", Model: "fake", CompletionPolicy: session.CompletionPolicyInteractive}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "write_file", ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}, json.RawMessage(`{
		"path":"note.txt",
		"content":"safe"
	}`))
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if result.IsError {
		t.Fatalf("random temp write should not follow predictable symlink alias, got %#v", result)
	}
	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("read outside: %v", err)
	}
	if string(data) != "keep" {
		t.Fatalf("outside symlink target was modified: %q", string(data))
	}
}

func TestShellReturnsExecPolicyMetadataInWarningMode(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.ExecPolicy.Mode = "warn"
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "shell", ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}, json.RawMessage(`{
		"command":"echo token > .env"
	}`))
	if err != nil {
		t.Fatalf("shell: %v", err)
	}
	if result.IsError {
		t.Fatalf("warning mode should not block command, got %#v", result)
	}
	policy, ok := result.Metadata["exec_policy"].(map[string]any)
	if !ok || policy["mode"] != "warn" {
		t.Fatalf("expected warn exec policy metadata, got %#v", result.Metadata)
	}
	violations, ok := policy["violations"].([]ExecPolicyViolation)
	if !ok || !hasExecPolicyCategory(violations, "secret_path_write") {
		t.Fatalf("expected secret path violation metadata, got %#v", policy)
	}
}

func TestShellBlocksViolationInDenyMode(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.ExecPolicy.Mode = "deny"
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "shell", ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}, json.RawMessage(`{
		"command":"echo token > .env"
	}`))
	if err != nil {
		t.Fatalf("shell: %v", err)
	}
	if !result.IsError || !strings.Contains(result.LLMOutput, "shell command denied by exec policy") {
		t.Fatalf("expected deny result, got %#v", result)
	}
	if _, err := os.Stat(filepath.Join(workdir, ".env")); !os.IsNotExist(err) {
		t.Fatalf("deny mode should not create .env, stat err=%v", err)
	}
	policy, ok := result.Metadata["exec_policy"].(map[string]any)
	if !ok || policy["mode"] != "deny" {
		t.Fatalf("expected deny exec policy metadata, got %#v", result.Metadata)
	}
}

func TestShellDenyPolicyBlocksNoSpaceSecretRedirect(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.ExecPolicy.Mode = "deny"
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	meta := session.SessionMetadata{SchemaVersion: 1, ID: session.NewSessionID(), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: workdir, Mode: session.ModeRun, Provider: "fake", Model: "fake", CompletionPolicy: session.CompletionPolicyInteractive}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "shell", ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}, json.RawMessage(`{
		"command":"echo token 2>.env"
	}`))
	if err != nil {
		t.Fatalf("shell: %v", err)
	}
	if !result.IsError || !strings.Contains(result.LLMOutput, "shell command denied by exec policy") {
		t.Fatalf("expected no-space redirect to be denied, got %#v", result)
	}
	if _, err := os.Stat(filepath.Join(workdir, ".env")); !os.IsNotExist(err) {
		t.Fatalf("deny mode should not create .env, stat err=%v", err)
	}
}

func TestShellToolUsesRegistryConfigWhenExecContextConfigMissing(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	meta := session.SessionMetadata{SchemaVersion: 1, ID: session.NewSessionID(), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: workdir, Mode: session.ModeRun, Provider: "fake", Model: "fake", CompletionPolicy: session.CompletionPolicyInteractive}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "shell", ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store}, json.RawMessage(`{
		"command":"printf ok"
	}`))
	if err != nil {
		t.Fatalf("shell: %v", err)
	}
	if result.IsError || strings.TrimSpace(result.DisplayOutput) != "ok" {
		t.Fatalf("expected shell to use registry config fallback, got %#v", result)
	}
}

func TestShellToolUsesDefaultConfigWhenRegistryConfigMissing(t *testing.T) {
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	meta := session.SessionMetadata{SchemaVersion: 1, ID: session.NewSessionID(), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: workdir, Mode: session.ModeRun, Provider: "fake", Model: "fake", CompletionPolicy: session.CompletionPolicyInteractive}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	registry, err := NewRegistry(nil, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "shell", ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store}, json.RawMessage(`{
		"command":"printf default-ok"
	}`))
	if err != nil {
		t.Fatalf("shell: %v", err)
	}
	if result.IsError || strings.TrimSpace(result.DisplayOutput) != "default-ok" {
		t.Fatalf("expected shell to use default config fallback, got %#v", result)
	}
}

func TestShellToolRejectsUnsupportedSandboxConfig(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.Shell.Sandbox = "firejail"
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	meta := session.SessionMetadata{SchemaVersion: 1, ID: session.NewSessionID(), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: workdir, Mode: session.ModeRun, Provider: "fake", Model: "fake", CompletionPolicy: session.CompletionPolicyInteractive}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "shell", ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}, json.RawMessage(`{
		"command":"printf should-not-run"
	}`))
	if err != nil {
		t.Fatalf("shell: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "unsupported shell sandbox") {
		t.Fatalf("expected unsupported sandbox error, got %#v", result)
	}
	if result.Metadata["sandbox"] != "unsupported" {
		t.Fatalf("expected unsupported sandbox metadata, got %#v", result.Metadata)
	}
}

func TestShellToolTreatsKilledProcessAsInterrupted(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
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
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{
		SessionID: meta.ID,
		Workdir:   meta.Workdir,
		Store:     store,
		Config:    cfg,
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	result, err := registry.Execute(ctx, "shell", execCtx, json.RawMessage(`{
		"command":"sleep 5"
	}`))
	if err == nil {
		t.Fatal("expected interrupt error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if result.DisplayOutput != "[Tool execution was interrupted]" {
		t.Fatalf("unexpected interrupted result: %#v", result)
	}
}

func TestShellToolSupportsRelativeWorkdirOverride(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	skillDir := filepath.Join(workdir, "skills", "bundle")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
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
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{
		SessionID: meta.ID,
		Workdir:   workdir,
		Store:     store,
		Config:    cfg,
	}

	result, err := registry.Execute(context.Background(), "shell", execCtx, json.RawMessage(`{
		"command":"pwd",
		"workdir":"skills/bundle"
	}`))
	if err != nil {
		t.Fatalf("execute shell: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got %#v", result)
	}
	if result.Metadata["workdir"] != skillDir {
		t.Fatalf("expected shell metadata workdir %q, got %#v", skillDir, result.Metadata)
	}
	if !strings.Contains(result.DisplayOutput, skillDir) {
		t.Fatalf("expected pwd output to include %q, got %q", skillDir, result.DisplayOutput)
	}
}

func TestShellToolAllowsRegisteredSkillWorkdirOutsideWorkspace(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	root := t.TempDir()
	workdir := filepath.Join(root, "workspace")
	skillDir := filepath.Join(root, "skills", "bundle")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: bundle\ndescription: bundled commands\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	catalog, err := skills.Scan([]string{filepath.Join(root, "skills")})
	if err != nil {
		t.Fatalf("scan skills: %v", err)
	}
	registry, err := NewRegistry(cfg, catalog, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{Workdir: workdir, Store: store, Config: cfg, Catalog: catalog}

	result, err := registry.Execute(context.Background(), "shell", execCtx, json.RawMessage(`{
		"command":"pwd",
		"workdir":"../skills/bundle"
	}`))
	if err != nil {
		t.Fatalf("execute shell: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got %#v", result)
	}
	if result.Metadata["workdir"] != skillDir {
		t.Fatalf("expected shell metadata workdir %q, got %#v", skillDir, result.Metadata)
	}
	if result.Metadata["workdir_source"] != "skill" || result.Metadata["skill"] != "bundle" {
		t.Fatalf("expected skill workdir metadata, got %#v", result.Metadata)
	}
	if !strings.Contains(result.DisplayOutput, skillDir) {
		t.Fatalf("expected pwd output to include %q, got %q", skillDir, result.DisplayOutput)
	}
}

func TestAgentToolsAreEnabledByDefaultAndCanBeDisabled(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())

	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	for _, name := range []string{"agent_spawn", "agent_status", "agent_list"} {
		if _, ok := registry.defs[name]; !ok {
			t.Fatalf("expected %s to be registered by default", name)
		}
	}
	for _, name := range []string{"feature_list_create", "feature_list_update", "feature_list_read"} {
		if _, ok := registry.defs[name]; !ok {
			t.Fatalf("expected %s to be registered as a built-in tool", name)
		}
	}

	cfg.Runtime.MultiAgent.Enabled = false
	registry, err = NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry with multi-agent disabled: %v", err)
	}
	for _, name := range []string{"agent_spawn", "agent_status", "agent_list"} {
		if _, ok := registry.defs[name]; ok {
			t.Fatalf("expected %s to be hidden when multi-agent is disabled", name)
		}
	}
	for _, name := range []string{"feature_list_create", "feature_list_update", "feature_list_read"} {
		if _, ok := registry.defs[name]; !ok {
			t.Fatalf("expected %s to remain registered when multi-agent is disabled", name)
		}
	}
}

func TestAgentToolsDescribeModelLedDelegation(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	def := registry.Get("agent_spawn")
	if def == nil {
		t.Fatal("agent_spawn definition missing")
	}
	for _, want := range []string{"model decides delegation", "broad investigations", "independent validation", "tiny single-file checks"} {
		if !strings.Contains(def.Description, want) {
			t.Fatalf("expected agent_spawn description to mention %q, got %q", want, def.Description)
		}
	}
	properties, ok := def.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties schema, got %#v", def.InputSchema["properties"])
	}
	for name, want := range map[string]string{
		"prompt":     "objective, scope, boundaries",
		"agent_role": "Choose exactly one of planner, generator, or evaluator",
		"background": "agent_status or agent_list",
	} {
		schema, ok := properties[name].(map[string]any)
		if !ok {
			t.Fatalf("expected %s schema, got %#v", name, properties[name])
		}
		description := fmt.Sprint(schema["description"])
		if !strings.Contains(description, want) {
			t.Fatalf("expected %s description to mention %q, got %q", name, want, description)
		}
	}
	statusDef := registry.Get("agent_status")
	if statusDef == nil {
		t.Fatal("agent_status definition missing")
	}
	if !strings.Contains(statusDef.Description, "collect final_text") {
		t.Fatalf("expected agent_status description to guide result collection, got %q", statusDef.Description)
	}
	listDef := registry.Get("agent_list")
	if listDef == nil {
		t.Fatal("agent_list definition missing")
	}
	if !strings.Contains(listDef.Description, "recover delegated work") {
		t.Fatalf("expected agent_list description to guide delegated work recovery, got %q", listDef.Description)
	}
}

func TestCoreToolDescriptionsGuideSelection(t *testing.T) {
	cfg := config.Default()
	registry, err := NewRegistry(cfg, nil, session.NewStore(t.TempDir()), nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	checks := map[string][]string{
		"shell":       {"build, test, package, git", "Prefer dedicated tools", "workdir parameter"},
		"read_file":   {"known text file", "Registered skill bundle", "capped at 120 lines", "use grep_files or grep first"},
		"write_file":  {"Create or overwrite", "prefer edit_file"},
		"edit_file":   {"Replace exact text", "after reading"},
		"grep_files":  {"default discovery step", "return only files"},
		"finish":      {"required artifacts", "unrun/failed validation"},
		"todo_write":  {"non-trivial multi-step work", "skip trivial one-step"},
		"task_create": {"durable task-graph node", "do not use it for trivial"},
	}
	for name, needles := range checks {
		def := registry.Get(name)
		if def == nil {
			t.Fatalf("%s definition missing", name)
		}
		for _, needle := range needles {
			if !strings.Contains(def.Description, needle) {
				t.Fatalf("expected %s description to contain %q, got %q", name, needle, def.Description)
			}
		}
	}
}

func TestFeatureListToolsPersistUpdateAndReadSnapshot(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
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
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{
		SessionID: meta.ID,
		Workdir:   meta.Workdir,
		Store:     store,
		Config:    cfg,
	}

	createResult, err := registry.Execute(context.Background(), "feature_list_create", execCtx, json.RawMessage(`{
		"features":[
			{"description":"Audit current contract","steps":["read spec","read tests"]},
			{"description":"Close verified drift","steps":["patch","validate"]}
		]
	}`))
	if err != nil {
		t.Fatalf("feature_list_create: %v", err)
	}
	if createResult.Name != "feature_list_create" || !strings.Contains(createResult.DisplayOutput, "2 features") {
		t.Fatalf("unexpected create result: %#v", createResult)
	}

	updateResult, err := registry.Execute(context.Background(), "feature_list_update", execCtx, json.RawMessage(`{
		"id":"feature_0002",
		"status":"completed",
		"passes":3
	}`))
	if err != nil {
		t.Fatalf("feature_list_update: %v", err)
	}
	if updateResult.Name != "feature_list_update" || !strings.Contains(updateResult.DisplayOutput, "feature_0002") {
		t.Fatalf("unexpected update result: %#v", updateResult)
	}

	readResult, err := registry.Execute(context.Background(), "feature_list_read", execCtx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("feature_list_read: %v", err)
	}
	var snapshot session.FeatureList
	if err := json.Unmarshal([]byte(readResult.LLMOutput), &snapshot); err != nil {
		t.Fatalf("unmarshal feature list snapshot: %v\n%s", err, readResult.LLMOutput)
	}
	if len(snapshot.Features) != 2 {
		t.Fatalf("expected two features, got %#v", snapshot.Features)
	}
	if snapshot.Features[0].ID != "feature_0001" || snapshot.Features[0].Status != "pending" {
		t.Fatalf("unexpected first feature: %#v", snapshot.Features[0])
	}
	if snapshot.Features[1].ID != "feature_0002" || snapshot.Features[1].Status != "completed" || snapshot.Features[1].Passes != 3 {
		t.Fatalf("unexpected updated feature: %#v", snapshot.Features[1])
	}
}

func TestFeatureListToolsRejectSymlinkedSnapshot(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
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
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{
		SessionID: meta.ID,
		Workdir:   meta.Workdir,
		Store:     store,
		Config:    cfg,
	}

	outside := filepath.Join(t.TempDir(), "outside-feature-list.json")
	original := []byte(`{"features":[{"id":"feature_0001","status":"pending"}]}` + "\n")
	if err := os.WriteFile(outside, original, 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(store.SessionDir(meta.ID), "feature_list.json")); err != nil {
		t.Fatalf("symlink feature list: %v", err)
	}

	readResult, err := registry.Execute(context.Background(), "feature_list_read", execCtx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("feature_list_read execute: %v", err)
	}
	if !readResult.IsError || !strings.Contains(readResult.LLMOutput, "symlinked session path") {
		t.Fatalf("expected symlink read error, got %#v", readResult)
	}
	updateResult, err := registry.Execute(context.Background(), "feature_list_update", execCtx, json.RawMessage(`{"id":"feature_0001","status":"completed"}`))
	if err != nil {
		t.Fatalf("feature_list_update execute: %v", err)
	}
	if !updateResult.IsError || !strings.Contains(updateResult.LLMOutput, "symlinked session path") {
		t.Fatalf("expected symlink update error, got %#v", updateResult)
	}
	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("read outside: %v", err)
	}
	if string(data) != string(original) {
		t.Fatalf("outside symlink target was modified: %q", string(data))
	}
}

func TestShellTimeoutIsCappedByRuntimeCommandTimeout(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.CommandTimeoutSec = 1
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		Mode:             session.ModeExec,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyAutonomous,
	}
	state := session.State{
		Status:    session.StatusRunning,
		Phase:     "prepare",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}

	execCtx := ExecContext{
		SessionID: meta.ID,
		Workdir:   meta.Workdir,
		Store:     store,
		Config:    cfg,
	}

	start := time.Now()
	result, err := registry.Execute(context.Background(), "shell", execCtx, json.RawMessage(`{
		"command":"sleep 2",
		"timeout":10000
	}`))
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
	if got := result.Metadata["timeout"]; got != 1 {
		t.Fatalf("expected capped timeout metadata, got %#v", result.Metadata)
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("expected capped timeout to stop shell quickly, took %s", elapsed)
	}
}

func TestGeneratedArtifactsAreHiddenFromFileDiscovery(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, ".artifacts", "tool-outputs"), 0o755); err != nil {
		t.Fatalf("mkdir artifacts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, ".artifacts", "tool-outputs", "shell.txt"), []byte("secret artifact"), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "visible.txt"), []byte("visible source"), 0o600); err != nil {
		t.Fatalf("write visible: %v", err)
	}
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
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
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{
		SessionID: meta.ID,
		Workdir:   meta.Workdir,
		Store:     store,
		Config:    cfg,
	}

	readResult, err := registry.Execute(context.Background(), "read_file", execCtx, json.RawMessage(`{
		"path":".artifacts/tool-outputs/shell.txt"
	}`))
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if !readResult.IsError || !strings.Contains(readResult.DisplayOutput, "internal generated artifact") {
		t.Fatalf("expected internal artifact read to be blocked, got %#v", readResult)
	}

	globResult, err := registry.Execute(context.Background(), "glob", execCtx, json.RawMessage(`{
		"pattern":"**/*.txt"
	}`))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if strings.Contains(globResult.DisplayOutput, ".artifacts") || !strings.Contains(globResult.DisplayOutput, "visible.txt") {
		t.Fatalf("expected glob to skip generated artifacts and keep source files, got %q", globResult.DisplayOutput)
	}

	grepFilesResult, err := registry.Execute(context.Background(), "grep_files", execCtx, json.RawMessage(`{
		"pattern":"secret|visible"
	}`))
	if err != nil {
		t.Fatalf("grep_files: %v", err)
	}
	if strings.Contains(grepFilesResult.DisplayOutput, ".artifacts") || !strings.Contains(grepFilesResult.DisplayOutput, "visible.txt") {
		t.Fatalf("expected grep_files to skip generated artifacts and keep source files, got %q", grepFilesResult.DisplayOutput)
	}
}

func TestReadFileBlocksSymlinkedArtifactsAlias(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, "artifact-target"), 0o755); err != nil {
		t.Fatalf("mkdir artifact target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "artifact-target", "secret.txt"), []byte("secret artifact"), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if err := os.Symlink(filepath.Join(workdir, "artifact-target"), filepath.Join(workdir, ".ARTIFACTS")); err != nil {
		t.Fatalf("symlink artifacts: %v", err)
	}
	meta := session.SessionMetadata{SchemaVersion: 1, ID: session.NewSessionID(), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: workdir, Mode: session.ModeRun, Provider: "fake", Model: "fake", CompletionPolicy: session.CompletionPolicyInteractive}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "read_file", ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}, json.RawMessage(`{
		"path":".ARTIFACTS/secret.txt"
	}`))
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "internal generated artifact") {
		t.Fatalf("expected symlinked artifact alias to be blocked, got %#v", result)
	}
}

func TestGrepToolsBlockExplicitInternalArtifacts(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, ".artifacts", "tool-outputs"), 0o755); err != nil {
		t.Fatalf("mkdir artifacts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, ".artifacts", "tool-outputs", "shell.txt"), []byte("secret artifact needle"), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	meta := session.SessionMetadata{SchemaVersion: 1, ID: session.NewSessionID(), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: workdir, Mode: session.ModeRun, Provider: "fake", Model: "fake", CompletionPolicy: session.CompletionPolicyInteractive}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}

	grepResult, err := registry.Execute(context.Background(), "grep", execCtx, json.RawMessage(`{
		"pattern":"needle",
		"path":".artifacts/tool-outputs/shell.txt"
	}`))
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if !grepResult.IsError || !strings.Contains(grepResult.DisplayOutput, "internal generated artifact") {
		t.Fatalf("expected explicit artifact grep to be blocked, got %#v", grepResult)
	}

	grepFilesResult, err := registry.Execute(context.Background(), "grep_files", execCtx, json.RawMessage(`{
		"pattern":"needle",
		"path":".artifacts/tool-outputs"
	}`))
	if err != nil {
		t.Fatalf("grep_files: %v", err)
	}
	if !grepFilesResult.IsError || !strings.Contains(grepFilesResult.DisplayOutput, "internal generated artifact") {
		t.Fatalf("expected explicit artifact grep_files to be blocked, got %#v", grepFilesResult)
	}
}

func TestGlobSkipsSymlinkEscapes(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret-name.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(workdir, "outside-link")); err != nil {
		t.Fatalf("symlink outside dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "visible.txt"), []byte("visible"), 0o600); err != nil {
		t.Fatalf("write visible file: %v", err)
	}
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "glob", ExecContext{
		SessionID: meta.ID,
		Workdir:   workdir,
		Store:     store,
		Config:    cfg,
	}, json.RawMessage(`{"pattern":"**/*.txt"}`))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if strings.Contains(result.DisplayOutput, "secret-name.txt") || strings.Contains(result.DisplayOutput, "outside-link") {
		t.Fatalf("expected glob to skip symlink escape, got %q", result.DisplayOutput)
	}
	if !strings.Contains(result.DisplayOutput, "visible.txt") {
		t.Fatalf("expected glob to keep workspace file, got %q", result.DisplayOutput)
	}
}

func TestGrepSkipsBuildArtifactsAndBinaryNoiseByDefault(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
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
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{
		SessionID: meta.ID,
		Workdir:   meta.Workdir,
		Store:     store,
		Config:    cfg,
	}

	if err := os.MkdirAll(filepath.Join(workdir, "pkg"), 0o755); err != nil {
		t.Fatalf("mkdir pkg: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workdir, "bin"), 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workdir, "artifacts"), 0o755); err != nil {
		t.Fatalf("mkdir artifacts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "pkg", "keep.txt"), []byte("needle from source\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "bin", "readme.txt"), []byte("needle from build artifact\n"), 0o644); err != nil {
		t.Fatalf("write build artifact: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "artifacts", "blob.bin"), []byte("needle\x00binary"), 0o644); err != nil {
		t.Fatalf("write binary: %v", err)
	}

	result, err := registry.Execute(context.Background(), "grep", execCtx, json.RawMessage(`{
		"pattern":"needle"
	}`))
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if !strings.Contains(result.DisplayOutput, "pkg/keep.txt:1:needle from source") {
		t.Fatalf("expected source match, got %q", result.DisplayOutput)
	}
	if strings.Contains(result.DisplayOutput, "bin/readme.txt") {
		t.Fatalf("expected broad grep to skip bin/, got %q", result.DisplayOutput)
	}
	if strings.Contains(result.DisplayOutput, "artifacts/blob.bin") {
		t.Fatalf("expected broad grep to skip binary file, got %q", result.DisplayOutput)
	}

	explicit, err := registry.Execute(context.Background(), "grep", execCtx, json.RawMessage(`{
		"pattern":"needle",
		"path":"bin/readme.txt"
	}`))
	if err != nil {
		t.Fatalf("explicit grep: %v", err)
	}
	if !strings.Contains(explicit.DisplayOutput, "bin/readme.txt:1:needle from build artifact") {
		t.Fatalf("expected explicit path grep to read file, got %q", explicit.DisplayOutput)
	}
}

func TestReadFileCapsLargeRequestsAndAnnotatesWindow(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
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
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{
		SessionID: meta.ID,
		Workdir:   meta.Workdir,
		Store:     store,
		Config:    cfg,
	}

	lines := make([]string, 0, 180)
	for i := 1; i <= 180; i++ {
		lines = append(lines, fmt.Sprintf("line %03d", i))
	}
	if err := os.WriteFile(filepath.Join(workdir, "long.txt"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("write long file: %v", err)
	}

	result, err := registry.Execute(context.Background(), "read_file", execCtx, json.RawMessage(`{
		"path":"long.txt",
		"limit":240
	}`))
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if !strings.Contains(result.DisplayOutput, "[read_file path=long.txt lines=1-120 of 180; requested_limit=240 capped_to=120]") {
		t.Fatalf("expected annotated capped window, got %q", result.DisplayOutput)
	}
	if !strings.Contains(result.DisplayOutput, "line 120") {
		t.Fatalf("expected capped content to include line 120, got %q", result.DisplayOutput)
	}
	if strings.Contains(result.DisplayOutput, "line 121") {
		t.Fatalf("expected capped content to exclude line 121, got %q", result.DisplayOutput)
	}
}

func TestReadFileAllowsRegisteredSkillReferencesOutsideWorkspace(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	root := t.TempDir()
	workdir := filepath.Join(root, "workspace")
	skillDir := filepath.Join(root, "skills", "pentest-toolset")
	referenceDir := filepath.Join(skillDir, "references")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.MkdirAll(referenceDir, 0o755); err != nil {
		t.Fatalf("mkdir references: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: pentest-toolset\ndescription: pentest helper\n---\nSee references/01-cli-contract.md\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(referenceDir, "01-cli-contract.md"), []byte("# CLI Contract\n\nUse schema first.\n"), 0o644); err != nil {
		t.Fatalf("write reference: %v", err)
	}
	catalog, err := skills.Scan([]string{filepath.Join(root, "skills")})
	if err != nil {
		t.Fatalf("scan skills: %v", err)
	}
	registry, err := NewRegistry(cfg, catalog, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{Workdir: workdir, Store: store, Config: cfg, Catalog: catalog}

	for _, inputPath := range []string{
		"skills/pentest-toolset/references/01-cli-contract.md",
		"references/01-cli-contract.md",
		filepath.Join(skillDir, "references", "01-cli-contract.md"),
	} {
		result, err := registry.Execute(context.Background(), "read_file", execCtx, json.RawMessage(fmt.Sprintf(`{"path":%q}`, inputPath)))
		if err != nil {
			t.Fatalf("read_file %s: %v", inputPath, err)
		}
		if result.IsError {
			t.Fatalf("expected skill reference read to succeed for %s, got %#v", inputPath, result)
		}
		if result.Metadata["path_source"] != "skill" || result.Metadata["skill"] != "pentest-toolset" {
			t.Fatalf("expected skill metadata for %s, got %#v", inputPath, result.Metadata)
		}
		if !strings.Contains(result.DisplayOutput, "[read_file path=skills/pentest-toolset/references/01-cli-contract.md") {
			t.Fatalf("expected skill-relative annotation for %s, got %q", inputPath, result.DisplayOutput)
		}
		if !strings.Contains(result.DisplayOutput, "Use schema first.") {
			t.Fatalf("expected reference content for %s, got %q", inputPath, result.DisplayOutput)
		}
	}
}

func TestReadFileRejectsRegisteredSkillSymlinkEscape(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	root := t.TempDir()
	workdir := filepath.Join(root, "workspace")
	skillDir := filepath.Join(root, "skills", "pentest-toolset")
	referenceDir := filepath.Join(skillDir, "references")
	outside := filepath.Join(root, "outside.txt")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.MkdirAll(referenceDir, 0o755); err != nil {
		t.Fatalf("mkdir references: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: pentest-toolset\ndescription: pentest helper\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(outside, []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(referenceDir, "escape.md")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	catalog, err := skills.Scan([]string{filepath.Join(root, "skills")})
	if err != nil {
		t.Fatalf("scan skills: %v", err)
	}
	registry, err := NewRegistry(cfg, catalog, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{Workdir: workdir, Store: store, Config: cfg, Catalog: catalog}

	result, err := registry.Execute(context.Background(), "read_file", execCtx, json.RawMessage(`{
		"path":"skills/pentest-toolset/references/escape.md"
	}`))
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "path escapes registered skill root") {
		t.Fatalf("expected skill symlink escape error, got %#v", result)
	}
}

func TestGrepFilesReturnsPathsOnlyAndSkipsArtifacts(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
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
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{
		SessionID: meta.ID,
		Workdir:   meta.Workdir,
		Store:     store,
		Config:    cfg,
	}

	if err := os.MkdirAll(filepath.Join(workdir, "pkg"), 0o755); err != nil {
		t.Fatalf("mkdir pkg: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workdir, "bin"), 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "pkg", "keep.go"), []byte("needle in source\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "bin", "noise.txt"), []byte("needle in artifact\n"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	result, err := registry.Execute(context.Background(), "grep_files", execCtx, json.RawMessage(`{
		"pattern":"needle",
		"include":"*.go"
	}`))
	if err != nil {
		t.Fatalf("grep_files: %v", err)
	}
	if result.DisplayOutput != "pkg/keep.go" {
		t.Fatalf("expected only matching source file path, got %q", result.DisplayOutput)
	}
}

func TestGrepAndGrepFilesSkipValidationRunArtifactsByDefault(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
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
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{
		SessionID: meta.ID,
		Workdir:   meta.Workdir,
		Store:     store,
		Config:    cfg,
	}

	if err := os.MkdirAll(filepath.Join(workdir, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workdir, "validation", "runs", "old", "artifacts"), 0o755); err != nil {
		t.Fatalf("mkdir validation runs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "docs", "guide.md"), []byte("core tool surface stays aligned\n"), 0o644); err != nil {
		t.Fatalf("write source doc: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "validation", "runs", "old", "artifacts", "summary.md"), []byte("core tool surface from old validation\n"), 0o644); err != nil {
		t.Fatalf("write validation artifact: %v", err)
	}

	grepResult, err := registry.Execute(context.Background(), "grep", execCtx, json.RawMessage(`{
		"pattern":"core tool surface"
	}`))
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if !strings.Contains(grepResult.DisplayOutput, "docs/guide.md:1:core tool surface stays aligned") {
		t.Fatalf("expected source grep match, got %q", grepResult.DisplayOutput)
	}
	if strings.Contains(grepResult.DisplayOutput, "validation/runs/old/artifacts/summary.md") {
		t.Fatalf("expected grep to skip validation/runs artifact, got %q", grepResult.DisplayOutput)
	}

	grepFilesResult, err := registry.Execute(context.Background(), "grep_files", execCtx, json.RawMessage(`{
		"pattern":"core tool surface",
		"include":"*.md"
	}`))
	if err != nil {
		t.Fatalf("grep_files: %v", err)
	}
	if grepFilesResult.DisplayOutput != "docs/guide.md" {
		t.Fatalf("expected grep_files to skip validation/runs artifact, got %q", grepFilesResult.DisplayOutput)
	}
}

func TestSkillCommandToolDescriptionAddsDirectCallGuidance(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "helpers")
	if err := os.MkdirAll(filepath.Join(skillDir, "tools"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: helpers\ndescription: helper skill\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "tools", "echo.yaml"), []byte("name: echo_args\ndescription: Echo args\ncommand: [\"/bin/sh\", \"-lc\", \"cat\"]\ninput_schema:\n  type: object\n  properties:\n    message:\n      type: string\n"), 0o644); err != nil {
		t.Fatalf("write tool: %v", err)
	}

	catalog, err := skills.Scan([]string{filepath.Join(root, "skills")})
	if err != nil {
		t.Fatalf("scan skills: %v", err)
	}
	registry, err := NewRegistry(cfg, catalog, nil, nil, filepath.Join(root, "workspace"))
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}

	var description string
	for _, def := range registry.Definitions() {
		if def.Name == "echo_args" {
			description = def.Description
			break
		}
	}
	if description == "" {
		t.Fatal("expected echo_args definition")
	}
	for _, needle := range []string{
		"Direct-call skill command tool from skill helpers.",
		"Call this tool directly by name",
		"Echo args",
	} {
		if !strings.Contains(description, needle) {
			t.Fatalf("expected description to contain %q, got %q", needle, description)
		}
	}
}

func TestWorkspaceSkillCommandToolsAreNotAutoRegistered(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldCwd)

	skillDir := filepath.Join(root, "skills", "helpers")
	if err := os.MkdirAll(filepath.Join(skillDir, "tools"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: helpers\ndescription: helper skill\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "tools", "echo.yaml"), []byte("name: echo_args\ndescription: Echo args\ncommand: [\"/bin/sh\", \"-lc\", \"cat\"]\ninput_schema:\n  type: object\n"), 0o644); err != nil {
		t.Fatalf("write tool: %v", err)
	}
	catalog, err := skills.Scan([]string{filepath.Join(root, "skills")})
	if err != nil {
		t.Fatalf("scan skills: %v", err)
	}
	registry, err := NewRegistry(cfg, catalog, nil, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	if def := registry.Get("echo_args"); def != nil {
		t.Fatalf("workspace skill command tool should not auto-register before trust/load, got %#v", def)
	}
}

func TestSkillCommandToolRejectsMissingRequiredField(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "helpers")
	if err := os.MkdirAll(filepath.Join(skillDir, "tools"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: helpers\ndescription: helper skill\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "tools", "pretty.yaml"), []byte("name: pretty_json_args\ndescription: Pretty-print JSON\ncommand: [\"/usr/bin/env\", \"python3\", \"-m\", \"json.tool\"]\ninput_schema:\n  type: object\n  properties:\n    payload:\n      type: object\n  required:\n    - payload\n  additionalProperties: false\n"), 0o644); err != nil {
		t.Fatalf("write tool: %v", err)
	}

	catalog, err := skills.Scan([]string{filepath.Join(root, "skills")})
	if err != nil {
		t.Fatalf("scan skills: %v", err)
	}
	registry, err := NewRegistry(cfg, catalog, nil, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "pretty_json_args", ExecContext{
		Workdir: root,
		Config:  cfg,
	}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, `missing required field "payload"`) {
		t.Fatalf("expected missing payload validation error, got %#v", result)
	}
}

func TestSkillCommandToolClosesSchemaByDefault(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "helpers")
	if err := os.MkdirAll(filepath.Join(skillDir, "tools"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: helpers\ndescription: helper skill\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "tools", "echo.yaml"), []byte("name: echo_args\ncommand: [\"/bin/sh\", \"-lc\", \"cat\"]\ninput_schema:\n  type: object\n  properties:\n    message:\n      type: string\n"), 0o644); err != nil {
		t.Fatalf("write tool: %v", err)
	}
	catalog, err := skills.Scan([]string{filepath.Join(root, "skills")})
	if err != nil {
		t.Fatalf("scan skills: %v", err)
	}
	registry, err := NewRegistry(cfg, catalog, nil, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "echo_args", ExecContext{Workdir: root, Config: cfg}, json.RawMessage(`{"message":"ok","extra":true}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, `unexpected field "extra"`) {
		t.Fatalf("expected unknown field rejection from closed schema, got %#v", result)
	}
}

func TestSkillCommandToolRejectsTrailingJSONValue(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "helpers")
	if err := os.MkdirAll(filepath.Join(skillDir, "tools"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: helpers\ndescription: helper skill\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "tools", "echo.yaml"), []byte("name: echo_args\ncommand: [\"/bin/sh\", \"-lc\", \"cat\"]\ninput_schema:\n  type: object\n  additionalProperties: true\n  properties:\n    message:\n      type: string\n"), 0o644); err != nil {
		t.Fatalf("write tool: %v", err)
	}
	catalog, err := skills.Scan([]string{filepath.Join(root, "skills")})
	if err != nil {
		t.Fatalf("scan skills: %v", err)
	}
	registry, err := NewRegistry(cfg, catalog, nil, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "echo_args", ExecContext{Workdir: root, Config: cfg}, json.RawMessage(`{"message":"ok"} {"message":"ignored"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "single JSON value") {
		t.Fatalf("expected trailing JSON rejection, got %#v", result)
	}
}

func TestSkillCommandToolPreservesExplicitAdditionalPropertiesTrue(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "helpers")
	if err := os.MkdirAll(filepath.Join(skillDir, "tools"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: helpers\ndescription: helper skill\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "tools", "echo.yaml"), []byte("name: echo_args\ncommand: [\"/bin/sh\", \"-lc\", \"cat\"]\ninput_schema:\n  type: object\n  additionalProperties: true\n  properties:\n    message:\n      type: string\n"), 0o644); err != nil {
		t.Fatalf("write tool: %v", err)
	}
	catalog, err := skills.Scan([]string{filepath.Join(root, "skills")})
	if err != nil {
		t.Fatalf("scan skills: %v", err)
	}
	registry, err := NewRegistry(cfg, catalog, nil, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "echo_args", ExecContext{Workdir: root, Config: cfg}, json.RawMessage(`{"message":"ok","extra":true}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected explicit additionalProperties=true to allow extra args, got %#v", result)
	}
}

func TestSkillCommandToolExecutesWithValidRequiredPayload(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "helpers")
	if err := os.MkdirAll(filepath.Join(skillDir, "tools"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: helpers\ndescription: helper skill\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "tools", "pretty.yaml"), []byte("name: pretty_json_args\ndescription: Pretty-print JSON\ncommand: [\"/usr/bin/env\", \"python3\", \"-m\", \"json.tool\"]\ninput_schema:\n  type: object\n  properties:\n    payload:\n      type: object\n  required:\n    - payload\n  additionalProperties: false\n"), 0o644); err != nil {
		t.Fatalf("write tool: %v", err)
	}

	catalog, err := skills.Scan([]string{filepath.Join(root, "skills")})
	if err != nil {
		t.Fatalf("scan skills: %v", err)
	}
	registry, err := NewRegistry(cfg, catalog, nil, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "pretty_json_args", ExecContext{
		Workdir: root,
		Config:  cfg,
	}, json.RawMessage(`{"payload":{"ok":true,"count":2}}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected valid payload to succeed, got %#v", result)
	}
	if !strings.Contains(result.DisplayOutput, `"payload": {`) || !strings.Contains(result.DisplayOutput, `"ok": true`) {
		t.Fatalf("expected pretty-printed payload, got %q", result.DisplayOutput)
	}
}

func TestSkillCommandToolExecutesFromSkillDirectory(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "helpers")
	if err := os.MkdirAll(filepath.Join(skillDir, "tools"), 0o755); err != nil {
		t.Fatalf("mkdir tools: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(skillDir, "scripts"), 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: helpers\ndescription: helper skill\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "scripts", "pwd.sh"), []byte("#!/usr/bin/env bash\npwd\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "tools", "pwd.yaml"), []byte("name: skill_pwd\ndescription: Print skill cwd\ncommand: [\"bash\", \"scripts/pwd.sh\"]\ninput_schema:\n  type: object\n  properties: {}\n"), 0o644); err != nil {
		t.Fatalf("write tool: %v", err)
	}

	catalog, err := skills.Scan([]string{filepath.Join(root, "skills")})
	if err != nil {
		t.Fatalf("scan skills: %v", err)
	}
	registry, err := NewRegistry(cfg, catalog, nil, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "skill_pwd", ExecContext{
		Workdir: root,
		Config:  cfg,
	}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected skill command to succeed, got %#v", result)
	}
	if result.Metadata["workdir"] != skillDir {
		t.Fatalf("expected skill command metadata workdir %q, got %#v", skillDir, result.Metadata)
	}
	if !strings.Contains(result.DisplayOutput, skillDir) {
		t.Fatalf("expected skill command output to include %q, got %q", skillDir, result.DisplayOutput)
	}
}

func TestSkillCommandToolUsesRegistryConfigWhenExecContextConfigMissing(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "helpers")
	if err := os.MkdirAll(filepath.Join(skillDir, "tools"), 0o755); err != nil {
		t.Fatalf("mkdir tools: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: helpers\ndescription: helper skill\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "tools", "echo.yaml"), []byte("name: skill_echo\ndescription: Echo skill name\ncommand: [\"bash\", \"-lc\", \"printf $GO_CLI_AGENT_SKILL_NAME\"]\ninput_schema:\n  type: object\n  properties: {}\n"), 0o644); err != nil {
		t.Fatalf("write tool: %v", err)
	}
	catalog, err := skills.Scan([]string{filepath.Join(root, "skills")})
	if err != nil {
		t.Fatalf("scan skills: %v", err)
	}
	registry, err := NewRegistry(cfg, catalog, nil, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "skill_echo", ExecContext{
		Workdir: root,
	}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.IsError || strings.TrimSpace(result.DisplayOutput) != "helpers" {
		t.Fatalf("expected skill command to use registry config fallback, got %#v", result)
	}
}

func TestSkillCommandToolTimeoutIncludesStructuredMetadata(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "helpers")
	if err := os.MkdirAll(filepath.Join(skillDir, "tools"), 0o755); err != nil {
		t.Fatalf("mkdir tools: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: helpers\ndescription: helper skill\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "tools", "slow.yaml"), []byte("name: slow_skill\ndescription: Sleep too long\ncommand: [\"bash\", \"-lc\", \"sleep 2\"]\ntimeout_sec: 1\ninput_schema:\n  type: object\n  properties: {}\n"), 0o644); err != nil {
		t.Fatalf("write tool: %v", err)
	}
	catalog, err := skills.Scan([]string{filepath.Join(root, "skills")})
	if err != nil {
		t.Fatalf("scan skills: %v", err)
	}
	registry, err := NewRegistry(cfg, catalog, nil, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}

	result, err := registry.Execute(context.Background(), "slow_skill", ExecContext{
		Workdir: root,
		Config:  cfg,
	}, json.RawMessage(`{}`))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got result=%#v err=%v", result, err)
	}
	if !result.IsError {
		t.Fatalf("expected timeout result to be an error, got %#v", result)
	}
	if result.Metadata["timeout"] != 1 || result.Metadata["exit_code"] == nil || result.Metadata["raw_length"] == nil || result.Metadata["truncated"] == nil {
		t.Fatalf("expected structured timeout metadata, got %#v", result.Metadata)
	}
}

func TestLoadSkillSchemaRestrictsNamesAndReportsAvailableSkills(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "timwhite-security-review")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: timwhite-security-review\ndescription: Security review workflow\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	catalog, err := skills.Scan([]string{filepath.Join(root, "skills")})
	if err != nil {
		t.Fatalf("scan skills: %v", err)
	}
	registry, err := NewRegistry(cfg, catalog, nil, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	def := registry.Get("load_skill")
	if def == nil {
		t.Fatal("load_skill definition missing")
	}
	properties, ok := def.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties schema, got %#v", def.InputSchema["properties"])
	}
	nameSchema, ok := properties["name"].(map[string]any)
	if !ok {
		t.Fatalf("expected name schema, got %#v", properties["name"])
	}
	if got := fmt.Sprint(nameSchema["enum"]); !strings.Contains(got, "timwhite-security-review") {
		t.Fatalf("expected load_skill name enum to include registered skill, got %#v", nameSchema["enum"])
	}
	if !strings.Contains(def.Description, "timwhite-security-review") {
		t.Fatalf("expected description to list registered skills, got %q", def.Description)
	}

	result, err := registry.Execute(context.Background(), "load_skill", ExecContext{
		Workdir: root,
		Config:  cfg,
		Catalog: catalog,
	}, json.RawMessage(`{"name":"code-audit"}`))
	if err != nil {
		t.Fatalf("execute load_skill: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected unknown skill to return tool error, got %#v", result)
	}
	if !strings.Contains(result.DisplayOutput, `unknown skill "code-audit"`) || !strings.Contains(result.DisplayOutput, "available skills: timwhite-security-review") {
		t.Fatalf("expected available skill hint in error, got %q", result.DisplayOutput)
	}
}

func TestLoadSkillIncludesShellWorkdirHint(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "helpers")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: helpers\ndescription: helper skill\n---\nRun `bash scripts/demo.sh` from the skill root.\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	catalog, err := skills.Scan([]string{filepath.Join(root, "skills")})
	if err != nil {
		t.Fatalf("scan skills: %v", err)
	}
	registry, err := NewRegistry(cfg, catalog, nil, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "load_skill", ExecContext{
		Workdir: root,
		Config:  cfg,
		Catalog: catalog,
	}, json.RawMessage(`{"name":"helpers"}`))
	if err != nil {
		t.Fatalf("execute load_skill: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected load_skill to succeed, got %#v", result)
	}
	if result.Metadata["shell_workdir"] != "skills/helpers" {
		t.Fatalf("expected shell workdir hint, got %#v", result.Metadata)
	}
	for _, needle := range []string{
		`shell_workdir="skills/helpers"`,
		"`workdir=\"skills/helpers\"`",
		"Skill bundle files are registered read-only resources",
		"`skills/helpers/references/...`",
	} {
		if !strings.Contains(result.LLMOutput, needle) {
			t.Fatalf("expected load_skill output to contain %q, got %q", needle, result.LLMOutput)
		}
	}
}

func TestLoadSkillReturnsAlreadyLoadedOnRepeatAndForceReload(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "helpers")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: helpers\ndescription: helper skill\n---\nFULL SKILL BODY\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	catalog, err := skills.Scan([]string{filepath.Join(root, "skills")})
	if err != nil {
		t.Fatalf("scan skills: %v", err)
	}
	store := session.NewStore(filepath.Join(root, "sessions"))
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          root,
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	state := session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	registry, err := NewRegistry(cfg, catalog, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{SessionID: meta.ID, Workdir: root, Store: store, Config: cfg, Catalog: catalog}

	first, err := registry.Execute(context.Background(), "load_skill", execCtx, json.RawMessage(`{"name":"helpers"}`))
	if err != nil || first.IsError {
		t.Fatalf("first load_skill err=%v result=%#v", err, first)
	}
	if !strings.Contains(first.LLMOutput, "FULL SKILL BODY") {
		t.Fatalf("expected first load to return full body, got %q", first.LLMOutput)
	}
	loaded, err := store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if fmt.Sprint(loaded.LoadedSkills) != "[helpers]" {
		t.Fatalf("expected state loaded skill, got %#v", loaded.LoadedSkills)
	}

	second, err := registry.Execute(context.Background(), "load_skill", execCtx, json.RawMessage(`{"name":"helpers"}`))
	if err != nil || second.IsError {
		t.Fatalf("second load_skill err=%v result=%#v", err, second)
	}
	if second.Metadata["already_loaded"] != true || strings.Contains(second.LLMOutput, "FULL SKILL BODY") {
		t.Fatalf("expected compact already_loaded result, got %#v output=%q", second.Metadata, second.LLMOutput)
	}

	reloaded, err := registry.Execute(context.Background(), "load_skill", execCtx, json.RawMessage(`{"name":"helpers","force_reload":true}`))
	if err != nil || reloaded.IsError {
		t.Fatalf("force reload err=%v result=%#v", err, reloaded)
	}
	if reloaded.Metadata["force_reload"] != true || !strings.Contains(reloaded.LLMOutput, "FULL SKILL BODY") {
		t.Fatalf("expected force reload to return full body, got %#v output=%q", reloaded.Metadata, reloaded.LLMOutput)
	}
}

func TestLoadSkillReportsLoadedSkillStateSaveError(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "helpers")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: helpers\ndescription: helper skill\n---\nFULL SKILL BODY\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	catalog, err := skills.Scan([]string{filepath.Join(root, "skills")})
	if err != nil {
		t.Fatalf("scan skills: %v", err)
	}
	store := session.NewStore(filepath.Join(root, "sessions"))
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          root,
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	steerPath := filepath.Join(store.SessionDir(meta.ID), "control", "steer.jsonl")
	if err := os.Remove(steerPath); err != nil {
		t.Fatalf("remove steer.jsonl: %v", err)
	}
	if err := os.Mkdir(steerPath, 0o700); err != nil {
		t.Fatalf("replace steer.jsonl with directory: %v", err)
	}
	registry, err := NewRegistry(cfg, catalog, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "load_skill", ExecContext{
		SessionID: meta.ID,
		Workdir:   root,
		Store:     store,
		Config:    cfg,
		Catalog:   catalog,
	}, json.RawMessage(`{"name":"helpers"}`))
	if err != nil {
		t.Fatalf("load_skill execute: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "steer.jsonl") {
		t.Fatalf("expected state save error result, got %#v", result)
	}
	if strings.Contains(result.LLMOutput, "FULL SKILL BODY") {
		t.Fatalf("load_skill must not return full body after failing to record loaded skill state: %q", result.LLMOutput)
	}
}

func TestReadFileRepeatObservation(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	store := session.NewStore(filepath.Join(root, "sessions"))
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          root,
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{SessionID: meta.ID, Workdir: root, Store: store, Config: cfg}
	args := json.RawMessage(`{"path":"notes.txt","offset":1,"limit":2}`)
	first, err := registry.Execute(context.Background(), "read_file", execCtx, args)
	if err != nil || first.IsError {
		t.Fatalf("first read_file err=%v result=%#v", err, first)
	}
	if err := store.AppendMessage(meta.ID, session.NewToolMessage([]session.ToolResult{first})); err != nil {
		t.Fatalf("append first result: %v", err)
	}
	second, err := registry.Execute(context.Background(), "read_file", execCtx, args)
	if err != nil || second.IsError {
		t.Fatalf("second read_file err=%v result=%#v", err, second)
	}
	if second.Metadata["repeat_count"] != 2 || !strings.Contains(second.LLMOutput, "repeat_count=2") {
		t.Fatalf("expected repeat observation, got metadata=%#v output=%q", second.Metadata, second.LLMOutput)
	}
}

func TestTruncateOutputPreservesHeadAndTail(t *testing.T) {
	input := strings.Repeat("HEAD-", 20) + "middle-only-content" + strings.Repeat("-TAIL", 20)
	output, rawLength, truncated := truncateOutput(input, 80)
	if !truncated {
		t.Fatal("expected output to be truncated")
	}
	if rawLength != len(input) {
		t.Fatalf("expected raw length %d, got %d", len(input), rawLength)
	}
	if !strings.HasPrefix(output, "HEAD-") {
		t.Fatalf("expected truncated output to preserve head, got %q", output)
	}
	if !strings.HasSuffix(output, "-TAIL") {
		t.Fatalf("expected truncated output to preserve tail, got %q", output)
	}
	if !strings.Contains(output, "bytes omitted") {
		t.Fatalf("expected omitted-byte marker, got %q", output)
	}
	if strings.Contains(output, "middle-only-content") {
		t.Fatalf("expected interior content to be omitted, got %q", output)
	}
	if len(output) > 80 {
		t.Fatalf("expected output to stay within limit, got len=%d output=%q", len(output), output)
	}
}

func TestTruncateOutputKeepsUTF8Boundaries(t *testing.T) {
	input := strings.Repeat("前缀", 20) + "middle" + strings.Repeat("后缀", 20)
	output, _, truncated := truncateOutput(input, 90)
	if !truncated {
		t.Fatal("expected output to be truncated")
	}
	if !utf8.ValidString(output) {
		t.Fatalf("expected valid UTF-8 output, got %q", output)
	}
	if !strings.Contains(output, "bytes omitted") {
		t.Fatalf("expected omitted-byte marker, got %q", output)
	}
}

func TestRelativeOrAbsoluteAllowsDotPrefixedChildPath(t *testing.T) {
	base := t.TempDir()
	child := filepath.Join(base, "..reports", "output.md")
	if got, want := relativeOrAbsolute(base, child), filepath.Join("..reports", "output.md"); got != want {
		t.Fatalf("expected relative dot-prefixed child path %q, got %q", want, got)
	}

	outside := filepath.Join(filepath.Dir(base), "outside.md")
	if got := relativeOrAbsolute(base, outside); got != outside {
		t.Fatalf("expected outside path to stay absolute, got %q", got)
	}
}

func TestTodoWriteNoopDoesNotLookLikeProgress(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	store := session.NewStore(filepath.Join(root, "sessions"))
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          root,
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{SessionID: meta.ID, Workdir: root, Store: store, Config: cfg}
	first, err := registry.Execute(context.Background(), "todo_write", execCtx, json.RawMessage(`{"todos":[{"content":"Do work","status":"in_progress","priority":"high","updated_at":"original"}]}`))
	if err != nil || first.IsError {
		t.Fatalf("first todo_write err=%v result=%#v", err, first)
	}
	second, err := registry.Execute(context.Background(), "todo_write", execCtx, json.RawMessage(`{"todos":[{"content":"Do work","status":"in_progress","priority":"high"}]}`))
	if err != nil || second.IsError {
		t.Fatalf("second todo_write err=%v result=%#v", err, second)
	}
	if second.Metadata["noop"] != true || second.Metadata["changed"] != false {
		t.Fatalf("expected noop metadata, got %#v", second.Metadata)
	}
	todo, err := store.LoadTodo(meta.ID)
	if err != nil {
		t.Fatalf("load todo: %v", err)
	}
	if len(todo) != 1 || todo[0].UpdatedAt != "original" {
		t.Fatalf("expected no-op write to preserve original timestamp, got %#v", todo)
	}
}

func TestTodoWriteReportsLoadErrorBeforeNoop(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	store := session.NewStore(filepath.Join(root, "sessions"))
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          root,
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	todoPath := filepath.Join(store.SessionDir(meta.ID), "todo.json")
	if err := os.Remove(todoPath); err != nil {
		t.Fatalf("remove todo: %v", err)
	}
	if err := os.Mkdir(todoPath, 0o700); err != nil {
		t.Fatalf("block todo path: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{SessionID: meta.ID, Workdir: root, Store: store, Config: cfg}
	result, err := registry.Execute(context.Background(), "todo_write", execCtx, json.RawMessage(`{"todos":[]}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "todo.json") {
		t.Fatalf("expected todo load error, got %#v", result)
	}
}

func TestTodoWriteReportsRequiredEventErrorAndRestoresPreviousSnapshot(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	store := session.NewStore(filepath.Join(root, "sessions"))
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          root,
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	initial := []session.TodoItem{{Content: "Keep original", Status: "in_progress", Priority: "high", UpdatedAt: "original"}}
	if err := store.SaveTodo(meta.ID, initial); err != nil {
		t.Fatalf("save initial todo: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	eventErr := errors.New("events.jsonl blocked")
	execCtx := ExecContext{
		SessionID: meta.ID,
		Workdir:   root,
		Store:     store,
		Config:    cfg,
		EmitRequired: func(eventType string, _ map[string]any) error {
			if eventType != "todo.updated" {
				t.Fatalf("unexpected required event %q", eventType)
			}
			return eventErr
		},
	}

	result, err := registry.Execute(context.Background(), "todo_write", execCtx, json.RawMessage(`{"todos":[{"content":"New work","status":"in_progress","priority":"medium"}]}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "todo.updated") || !strings.Contains(result.DisplayOutput, eventErr.Error()) {
		t.Fatalf("expected todo.updated error result, got %#v", result)
	}
	todo, err := store.LoadTodo(meta.ID)
	if err != nil {
		t.Fatalf("load todo: %v", err)
	}
	if !normalizedTodosEqual(todo, initial) || todo[0].UpdatedAt != "original" {
		t.Fatalf("expected failed event append to restore initial todo, got %#v", todo)
	}
}

func TestTodoWriteNoopReportsRequiredEventError(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	store := session.NewStore(filepath.Join(root, "sessions"))
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          root,
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	initial := []session.TodoItem{{Content: "Do work", Status: "in_progress", Priority: "high", UpdatedAt: "original"}}
	if err := store.SaveTodo(meta.ID, initial); err != nil {
		t.Fatalf("save initial todo: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{
		SessionID: meta.ID,
		Workdir:   root,
		Store:     store,
		Config:    cfg,
		EmitRequired: func(string, map[string]any) error {
			return errors.New("events.jsonl blocked")
		},
	}

	result, err := registry.Execute(context.Background(), "todo_write", execCtx, json.RawMessage(`{"todos":[{"content":"Do work","status":"in_progress","priority":"high"}]}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "todo.updated") || !strings.Contains(result.DisplayOutput, "events.jsonl blocked") {
		t.Fatalf("expected todo.updated event error, got %#v", result)
	}
	todo, err := store.LoadTodo(meta.ID)
	if err != nil {
		t.Fatalf("load todo: %v", err)
	}
	if !normalizedTodosEqual(todo, initial) || todo[0].UpdatedAt != "original" {
		t.Fatalf("expected no-op event failure to preserve initial todo, got %#v", todo)
	}
}

func TestTaskToolsReportRequiredEventErrorAndRestoreTaskGraph(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	store := session.NewStore(filepath.Join(root, "sessions"))
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          root,
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}

	eventErr := errors.New("events.jsonl blocked")
	createCtx := ExecContext{
		SessionID: meta.ID,
		Workdir:   root,
		Store:     store,
		Config:    cfg,
		EmitRequired: func(eventType string, _ map[string]any) error {
			if eventType != "task.created" {
				t.Fatalf("unexpected create event %q", eventType)
			}
			return eventErr
		},
	}
	createResult, err := registry.Execute(context.Background(), "task_create", createCtx, json.RawMessage(`{"subject":"Should roll back","priority":"high"}`))
	if err != nil {
		t.Fatalf("task_create execute: %v", err)
	}
	if !createResult.IsError || !strings.Contains(createResult.DisplayOutput, "task.created") || !strings.Contains(createResult.DisplayOutput, eventErr.Error()) {
		t.Fatalf("expected task.created event error, got %#v", createResult)
	}
	tasks, err := store.ListTasks(meta.ID)
	if err != nil {
		t.Fatalf("list tasks after failed create: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected failed task_create to restore empty graph, got %#v", tasks)
	}

	createOK, err := registry.Execute(context.Background(), "task_create", ExecContext{SessionID: meta.ID, Workdir: root, Store: store, Config: cfg}, json.RawMessage(`{"subject":"Keep original","priority":"high"}`))
	if err != nil || createOK.IsError {
		t.Fatalf("successful task_create err=%v result=%#v", err, createOK)
	}
	beforeUpdate, err := store.ListTasks(meta.ID)
	if err != nil {
		t.Fatalf("list tasks before update: %v", err)
	}
	updateCtx := ExecContext{
		SessionID: meta.ID,
		Workdir:   root,
		Store:     store,
		Config:    cfg,
		EmitRequired: func(eventType string, _ map[string]any) error {
			if eventType != "task.updated" {
				t.Fatalf("unexpected update event %q", eventType)
			}
			return eventErr
		},
	}
	updateResult, err := registry.Execute(context.Background(), "task_update", updateCtx, json.RawMessage(`{"task_id":"task_0001","status":"completed","append_note":"done"}`))
	if err != nil {
		t.Fatalf("task_update execute: %v", err)
	}
	if !updateResult.IsError || !strings.Contains(updateResult.DisplayOutput, "task.updated") || !strings.Contains(updateResult.DisplayOutput, eventErr.Error()) {
		t.Fatalf("expected task.updated event error, got %#v", updateResult)
	}
	afterUpdate, err := store.ListTasks(meta.ID)
	if err != nil {
		t.Fatalf("list tasks after update: %v", err)
	}
	if fmt.Sprintf("%#v", afterUpdate) != fmt.Sprintf("%#v", beforeUpdate) {
		t.Fatalf("expected failed task_update to restore graph\nbefore=%#v\nafter=%#v", beforeUpdate, afterUpdate)
	}
}
