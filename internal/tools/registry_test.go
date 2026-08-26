package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"aegis-agent/internal/config"
	"aegis-agent/internal/session"
	"aegis-agent/internal/skills"
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

func TestCreateGoalSchemaUsesGoalObjectiveLimit(t *testing.T) {
	definition := defCreateGoal()
	properties, ok := definition.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("create_goal properties missing from schema: %#v", definition.InputSchema)
	}
	objective, ok := properties["objective"].(map[string]any)
	if !ok {
		t.Fatalf("create_goal objective missing from schema: %#v", properties)
	}
	if objective["maxLength"] != session.MaxGoalObjectiveChars {
		t.Fatalf("create_goal maxLength=%#v want %d", objective["maxLength"], session.MaxGoalObjectiveChars)
	}
	description, _ := objective["description"].(string)
	if !strings.Contains(description, fmt.Sprintf("max %d characters", session.MaxGoalObjectiveChars)) {
		t.Fatalf("create_goal description does not expose the current limit: %q", description)
	}
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

func newSearchToolTestRegistry(t *testing.T) (*Registry, ExecContext, string) {
	t.Helper()
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
		t.Fatalf("create search tool test session: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new search tool test registry: %v", err)
	}
	execCtx := ExecContext{
		SessionID: meta.ID,
		Workdir:   workdir,
		Store:     store,
		Config:    cfg,
	}
	return registry, execCtx, workdir
}

func assertSearchResultMetadata(t *testing.T, result session.ToolResult, returnedCount, requestedLimit, effectiveLimit int, hasMore, limitCapped bool, truncatedSnippetCount int) {
	t.Helper()
	want := map[string]any{
		"returned_count":          returnedCount,
		"requested_limit":         requestedLimit,
		"effective_limit":         effectiveLimit,
		"has_more":                hasMore,
		"limit_capped":            limitCapped,
		"truncated_snippet_count": truncatedSnippetCount,
	}
	for key, wantValue := range want {
		if got := result.Metadata[key]; got != wantValue {
			t.Errorf("metadata[%q]=%#v, want %#v; full metadata=%#v", key, got, wantValue, result.Metadata)
		}
	}
}

func countOutputLinesContaining(output, fragment string) int {
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if strings.Contains(line, fragment) {
			count++
		}
	}
	return count
}

type recordingControlPlane struct {
	spawnCalls  int
	spawnReq    AgentSpawnRequest
	stopCalls   int
	stopReq     AgentStopRequest
	promptCalls int
	promptReq   AgentPromptRequest
	statusCalls int
	statusReq   AgentStatusRequest
	listCalls   int
	listParent  string
}

func (r *recordingControlPlane) SpawnAgent(_ context.Context, req AgentSpawnRequest) (AgentSpawnResult, error) {
	r.spawnCalls++
	r.spawnReq = req
	if req.Background {
		return AgentSpawnResult{QueueJobID: "job_child_1", Status: session.QueueStatusQueued}, nil
	}
	return AgentSpawnResult{SessionID: "child_1", Status: session.StatusCompleted}, nil
}

func (r *recordingControlPlane) StopAgent(_ context.Context, req AgentStopRequest) (AgentStopResult, error) {
	r.stopCalls++
	r.stopReq = req
	return AgentStopResult{QueueJobID: req.QueueJobID, Status: session.QueueStatusFailed, LastError: "stopped"}, nil
}

func (r *recordingControlPlane) PromptAgent(_ context.Context, req AgentPromptRequest) (AgentPromptResult, error) {
	r.promptCalls++
	r.promptReq = req
	return AgentPromptResult{SessionID: firstNonEmptyString(req.SessionID, "child_prompted"), QueueJobID: req.QueueJobID, Accepted: true, Behavior: "queued"}, nil
}

func (r *recordingControlPlane) AgentStatus(_ context.Context, req AgentStatusRequest) (AgentStatusResult, error) {
	r.statusCalls++
	r.statusReq = req
	return AgentStatusResult{}, nil
}

func (r *recordingControlPlane) AgentList(_ context.Context, parent string) (AgentListResult, error) {
	r.listCalls++
	r.listParent = parent
	return AgentListResult{}, nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
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

func TestTodoItemSchemaAllowsParallelInProgressDescription(t *testing.T) {
	properties, _ := todoItemSchema()["properties"].(map[string]any)
	status, _ := properties["status"].(map[string]any)
	description, _ := status["description"].(string)
	if !strings.Contains(description, "Multiple independent or parallel items may be in_progress") || strings.Contains(description, "at most one") {
		t.Fatalf("todo status description still implies a single active item: %q", description)
	}
}

func TestToolDescriptionsMentionOnlyDeclaredParameters(t *testing.T) {
	cfg := config.Default()
	registry, err := NewRegistry(cfg, nil, session.NewStore(t.TempDir()), nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	parameterNames := []string{
		"command",
		"content",
		"include",
		"limit",
		"new_text",
		"offset",
		"old_text",
		"path",
		"pattern",
		"timeout",
		"workdir",
	}
	for _, def := range registry.Definitions() {
		properties := schemaPropertyNames(def.InputSchema)
		for _, parameter := range parameterNames {
			if !mentionsParameterName(def.Description, parameter) {
				continue
			}
			if _, ok := properties[parameter]; !ok {
				t.Fatalf("%s description mentions parameter %q but schema properties are %v", def.Name, parameter, sortedPropertyNames(properties))
			}
		}
	}
}

func TestDiscoveryToolParameterSetsStayAligned(t *testing.T) {
	cfg := config.Default()
	registry, err := NewRegistry(cfg, nil, session.NewStore(t.TempDir()), nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	want := map[string]struct{}{
		"pattern": {},
		"path":    {},
		"include": {},
		"limit":   {},
	}
	for _, name := range []string{"glob", "grep", "grep_files"} {
		def := registry.Get(name)
		if def == nil {
			t.Fatalf("%s definition missing", name)
		}
		properties := schemaPropertyNames(def.InputSchema)
		for parameter := range want {
			if _, ok := properties[parameter]; !ok {
				t.Fatalf("%s missing discovery parameter %q; properties are %v", name, parameter, sortedPropertyNames(properties))
			}
		}
	}
}

func TestDiscoveryToolDescriptionsRouteSessionArtifactsToReadFile(t *testing.T) {
	registry, err := NewRegistry(config.Default(), nil, session.NewStore(t.TempDir()), nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	for _, name := range []string{"glob", "grep", "grep_files"} {
		def := registry.Get(name)
		if def == nil {
			t.Fatalf("%s definition missing", name)
		}
		for _, want := range []string{"artifacts/tool-outputs", "read_file", "exact artifact path"} {
			if !strings.Contains(def.Description, want) {
				t.Fatalf("expected %s description to mention %q, got %q", name, want, def.Description)
			}
		}
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

func TestBuiltinToolExecutionRejectsHarnessReplayMarkersWithTargetedHint(t *testing.T) {
	cfg := config.Default()
	registry, err := NewRegistry(cfg, nil, session.NewStore(t.TempDir()), nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "shell", ExecContext{}, json.RawMessage(`{"compacted_for_context":true,"head_tail":"old args","original_chars":123}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected replay marker rejection, got %#v", result)
	}
	for _, want := range []string{"context replay marker", "compacted historical tool arguments", "real fields"} {
		if !strings.Contains(result.DisplayOutput, want) {
			t.Fatalf("expected targeted replay marker hint %q in %q", want, result.DisplayOutput)
		}
	}
	if got := result.Metadata[MetadataFailureClass]; got != FailureClassSchemaReject {
		t.Fatalf("expected schema_reject metadata, got %#v", result.Metadata)
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

func TestBuiltinToolExecutionRejectsMissingRequiredField(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	store := session.NewStore(filepath.Join(root, "sessions"))
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	path := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(path, []byte("keep\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	result, err := registry.Execute(context.Background(), "write_file", ExecContext{
		Workdir: root,
		Store:   store,
		Config:  cfg,
	}, json.RawMessage(`{"path":"notes.txt"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "content is required") {
		t.Fatalf("expected missing required field rejection, got %#v", result)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if string(data) != "keep\n" {
		t.Fatalf("missing content mutated file: %q", string(data))
	}
}

func TestFinishRejectsBlankMessage(t *testing.T) {
	cfg := config.Default()
	registry, err := NewRegistry(cfg, nil, session.NewStore(t.TempDir()), nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	cases := []struct {
		name string
		args json.RawMessage
	}{
		{name: "missing", args: json.RawMessage(`{}`)},
		{name: "blank", args: json.RawMessage(`{"message":" \n\t "}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := registry.Execute(context.Background(), "finish", ExecContext{}, tc.args)
			if err != nil {
				t.Fatalf("finish: %v", err)
			}
			if !result.IsError || result.Final || !strings.Contains(result.DisplayOutput, "message is required") {
				t.Fatalf("expected blank finish message rejection, got %#v", result)
			}
		})
	}
}

func TestAwaitInputReturnsStructuredParkingMetadata(t *testing.T) {
	cfg := config.Default()
	registry, err := NewRegistry(cfg, nil, session.NewStore(t.TempDir()), nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "await_input", ExecContext{}, json.RawMessage(`{
		"kind":"needs_input",
		"reason":"A deployment target must be selected.",
		"blockers":["No target environment was specified."],
		"resume_condition":"Continue after the user chooses staging or production."
	}`))
	if err != nil {
		t.Fatalf("await_input: %v", err)
	}
	if result.IsError || result.Final {
		t.Fatalf("await_input must park without completing, got %#v", result)
	}
	if result.Metadata[MetadataAwaitInput] != true || result.Metadata[MetadataAwaitInputKind] != "needs_input" || result.Metadata[MetadataAwaitInputReason] != "A deployment target must be selected." {
		t.Fatalf("unexpected await_input metadata: %#v", result.Metadata)
	}
	blockers, ok := result.Metadata[MetadataAwaitInputBlockers].([]string)
	if !ok || len(blockers) != 1 || blockers[0] != "No target environment was specified." {
		t.Fatalf("unexpected blockers metadata: %#v", result.Metadata)
	}
	if result.Metadata[MetadataAwaitInputResume] != "Continue after the user chooses staging or production." {
		t.Fatalf("unexpected resume condition: %#v", result.Metadata)
	}
	if !strings.Contains(result.DisplayOutput, "Execution parked (needs_input)") || !strings.Contains(result.DisplayOutput, "Resume when:") {
		t.Fatalf("unexpected await_input display: %q", result.DisplayOutput)
	}
}

func TestAwaitInputRejectsBlankReason(t *testing.T) {
	registry, err := NewRegistry(config.Default(), nil, session.NewStore(t.TempDir()), nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "await_input", ExecContext{}, json.RawMessage(`{"reason":"  "}`))
	if err != nil {
		t.Fatalf("await_input: %v", err)
	}
	if !result.IsError || result.Final || !strings.Contains(result.DisplayOutput, "reason is required") {
		t.Fatalf("expected blank reason rejection, got %#v", result)
	}
}

func schemaPropertyNames(schema map[string]any) map[string]struct{} {
	out := map[string]struct{}{}
	properties, _ := schema["properties"].(map[string]any)
	for name := range properties {
		out[name] = struct{}{}
	}
	return out
}

func sortedPropertyNames(properties map[string]struct{}) []string {
	out := make([]string, 0, len(properties))
	for name := range properties {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func mentionsParameterName(description, parameter string) bool {
	text := strings.ToLower(description)
	parameter = strings.ToLower(parameter)
	markers := []string{
		"`" + parameter + "`",
		parameter + " parameter",
		"optional " + parameter,
		parameter + " filter",
		parameter + " filters",
		"raise " + parameter,
		parameter + " must",
		parameter + "=",
		parameter + ":",
	}
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
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

func TestTaskListHonorsFilters(t *testing.T) {
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
		t.Fatalf("create: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{SessionID: meta.ID, Workdir: meta.Workdir, Store: store, Config: cfg}

	ready, err := session.CreateTask(store, meta.ID, session.TaskCreateInput{Subject: "Ready task"})
	if err != nil {
		t.Fatalf("create ready task: %v", err)
	}
	blocker, err := session.CreateTask(store, meta.ID, session.TaskCreateInput{Subject: "Blocking task"})
	if err != nil {
		t.Fatalf("create blocker task: %v", err)
	}
	blocked, err := session.CreateTask(store, meta.ID, session.TaskCreateInput{Subject: "Blocked task", BlockedBy: []string{blocker.ID}})
	if err != nil {
		t.Fatalf("create blocked task: %v", err)
	}
	inProgress, err := session.CreateTask(store, meta.ID, session.TaskCreateInput{Subject: "Current task"})
	if err != nil {
		t.Fatalf("create in-progress task: %v", err)
	}
	if _, err := session.UpdateTask(store, meta.ID, session.TaskUpdateInput{TaskID: inProgress.ID, Status: "in_progress"}); err != nil {
		t.Fatalf("mark in progress: %v", err)
	}
	completed, err := session.CreateTask(store, meta.ID, session.TaskCreateInput{Subject: "Completed task"})
	if err != nil {
		t.Fatalf("create completed task: %v", err)
	}
	if _, err := session.UpdateTask(store, meta.ID, session.TaskUpdateInput{TaskID: completed.ID, Status: "completed"}); err != nil {
		t.Fatalf("mark completed: %v", err)
	}
	cancelled, err := session.CreateTask(store, meta.ID, session.TaskCreateInput{Subject: "Cancelled task"})
	if err != nil {
		t.Fatalf("create cancelled task: %v", err)
	}
	if _, err := session.UpdateTask(store, meta.ID, session.TaskUpdateInput{TaskID: cancelled.ID, Status: "cancelled"}); err != nil {
		t.Fatalf("mark cancelled: %v", err)
	}

	assertListedIDs := func(name string, raw json.RawMessage, want []string) {
		t.Helper()
		result, err := registry.Execute(context.Background(), "task_list", execCtx, raw)
		if err != nil {
			t.Fatalf("%s task_list: %v", name, err)
		}
		if result.IsError {
			t.Fatalf("%s task_list returned error: %s", name, result.DisplayOutput)
		}
		var board session.TaskBoard
		if err := json.Unmarshal([]byte(result.LLMOutput), &board); err != nil {
			t.Fatalf("%s decode board: %v\n%s", name, err, result.LLMOutput)
		}
		got := make([]string, 0, len(board.Tasks))
		for _, task := range board.Tasks {
			got = append(got, task.ID)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s expected task ids %v, got %v", name, want, got)
		}
	}

	assertListedIDs("default", json.RawMessage(`{}`), []string{ready.ID, blocker.ID, blocked.ID, inProgress.ID, completed.ID, cancelled.ID})
	assertListedIDs("active only", json.RawMessage(`{"include_completed":false}`), []string{ready.ID, blocker.ID, blocked.ID, inProgress.ID})
	assertListedIDs("ready", json.RawMessage(`{"status":"ready"}`), []string{ready.ID, blocker.ID})
	assertListedIDs("blocked", json.RawMessage(`{"status":"blocked"}`), []string{blocked.ID})
	assertListedIDs("done", json.RawMessage(`{"status":"done","include_completed":false}`), []string{completed.ID, cancelled.ID})

	result, err := registry.Execute(context.Background(), "task_list", execCtx, json.RawMessage(`{"status":"waiting"}`))
	if err != nil {
		t.Fatalf("invalid status task_list: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "invalid status") {
		t.Fatalf("expected invalid status error, got %#v", result)
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

func TestRequestUserInputLiveAnswerIncludesPlanModeID(t *testing.T) {
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
	created, err := store.CreatePlanMode(meta.ID, session.PlanModeDraft{Enabled: true, Objective: "Resolve one planning decision"})
	if err != nil {
		t.Fatalf("create plan mode: %v", err)
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
	if result.IsError {
		t.Fatalf("expected successful live answer result, got %#v", result)
	}
	if result.Metadata["planmode"] != true {
		t.Fatalf("expected planmode metadata, got %#v", result.Metadata)
	}
	if strings.TrimSpace(fmt.Sprint(result.Metadata["request_id"])) == "" {
		t.Fatalf("expected request_id metadata, got %#v", result.Metadata)
	}
	if result.Metadata["plan_mode_id"] != created.PlanModeID {
		t.Fatalf("expected live answer result to carry plan_mode_id %q, got %#v", created.PlanModeID, result.Metadata)
	}
	if responder.calls != 1 {
		t.Fatalf("expected one responder call, got %d", responder.calls)
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
	planMode, err := store.LoadPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("load plan mode: %v", err)
	}
	if planMode.Status != session.PlanModeStatusPlanning || planMode.PendingRequest != nil {
		t.Fatalf("state save failure must restore planning Plan Mode, got %#v", planMode)
	}
	history, err := store.LoadPlanModeHistory(meta.ID)
	if err != nil {
		t.Fatalf("load plan mode history: %v", err)
	}
	for _, item := range history {
		if item.Type == "planmode.input_requested" {
			t.Fatalf("state save failure must not leave input_requested history, got %#v", history)
		}
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

func TestRequestUserInputReportsCancellationEventErrorAndRestoresPendingRequest(t *testing.T) {
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
	eventErr := errors.New("events.jsonl blocked")
	result, err := registry.Execute(context.Background(), "request_user_input", ExecContext{
		SessionID:          meta.ID,
		ToolCallID:         "call_plan_input",
		Workdir:            meta.Workdir,
		Store:              store,
		Config:             cfg,
		PlanInputResponder: failingPlanInputResponder{err: ErrPlanInputCancelled},
		EmitRequired: func(eventType string, _ map[string]any) error {
			if eventType != "planmode.input_requested" {
				t.Fatalf("unexpected single required event %q", eventType)
			}
			return nil
		},
		EmitBatchRequired: func(items []ToolEvent) error {
			if len(items) != 2 || items[0].Type != "planmode.input_cancelled" || items[1].Type != "planmode.cancelled" {
				t.Fatalf("unexpected required event batch %#v", items)
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
	if !result.IsError || !strings.Contains(result.DisplayOutput, "planmode.input_cancelled") || !strings.Contains(result.DisplayOutput, eventErr.Error()) {
		t.Fatalf("expected cancellation event error result, got %#v", result)
	}
	planMode, err := store.LoadPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("load plan mode: %v", err)
	}
	if planMode.Status != session.PlanModeStatusAwaitingUserInput || planMode.PendingRequest == nil {
		t.Fatalf("expected pending request restored after cancellation event failure, got %#v", planMode)
	}
	if planMode.PendingRequest.Status != "pending" || planMode.PendingRequest.CancelledAt != "" {
		t.Fatalf("expected uncancelled pending request restored, got %#v", planMode.PendingRequest)
	}
	history, err := store.LoadPlanModeHistory(meta.ID)
	if err != nil {
		t.Fatalf("load plan mode history: %v", err)
	}
	for _, entry := range history {
		if entry.Type == "planmode.cancelled" || entry.Type == "planmode.input_cancelled" {
			t.Fatalf("failed cancellation event append must not leave cancellation history, got %#v", history)
		}
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

func TestSubmitPlanRejectsBlankRequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		wantErr string
	}{
		{
			name: "blank title",
			payload: `{
				"title":"   ",
				"summary":"Plan safely.",
				"plan_markdown":"# Summary\n\nPlan safely.\n\n# Verification\n\nRun focused tests.",
				"verification":["go test ./internal/tools"]
			}`,
			wantErr: "title is required",
		},
		{
			name: "blank verification item",
			payload: `{
				"title":"Safe plan",
				"summary":"Plan safely.",
				"plan_markdown":"# Summary\n\nPlan safely.\n\n# Verification\n\nRun focused tests.",
				"verification":["   "]
			}`,
			wantErr: "verification is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
			registry, err := NewRegistry(cfg, nil, store, nil)
			if err != nil {
				t.Fatalf("new registry: %v", err)
			}
			execCtx := ExecContext{
				SessionID: meta.ID,
				Workdir:   root,
				Store:     store,
				Config:    cfg,
				EmitRequired: func(eventType string, _ map[string]any) error {
					t.Fatalf("invalid submit must not emit event %q", eventType)
					return nil
				},
			}

			result, err := registry.Execute(context.Background(), "submit_plan", execCtx, json.RawMessage(tt.payload))
			if err != nil {
				t.Fatalf("submit_plan execute: %v", err)
			}
			if !result.IsError || !strings.Contains(result.DisplayOutput, tt.wantErr) {
				t.Fatalf("expected %q error result, got %#v", tt.wantErr, result)
			}
			planMode, err := store.LoadPlanMode(meta.ID)
			if err != nil {
				t.Fatalf("load plan mode: %v", err)
			}
			if planMode.Status != session.PlanModeStatusPlanning || planMode.PlanVersion != 0 || planMode.PlanMarkdown != "" || len(planMode.Verification) != 0 {
				t.Fatalf("invalid submit should not advance plan mode, got %#v", planMode)
			}
			planPath := filepath.Join(store.SessionDir(meta.ID), "artifacts", "planmode-plan.md")
			if _, err := os.Stat(planPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid submit should not create plan markdown, err=%v", err)
			}
		})
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
		"provider_time_budget_minutes":5,
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
	if !strings.Contains(readResult.LLMOutput, `"provider_time_budget_seconds": 300`) || !strings.Contains(readResult.LLMOutput, `"time_budget_seconds": 300`) {
		t.Fatalf("expected canonical and compatibility provider-time fields, got %s", readResult.LLMOutput)
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

func TestRecordGoalProgressSchemaExplainsSystemAssignedIDs(t *testing.T) {
	registry, err := NewRegistry(config.Default(), nil, session.NewStore(t.TempDir()), nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	def := registry.Get("record_goal_progress")
	if def == nil {
		t.Fatal("record_goal_progress definition missing")
	}
	if !strings.Contains(def.Description, "Read get_goal") || !strings.Contains(def.Description, "system-assigned") {
		t.Fatalf("record_goal_progress description must identify the id source, got %q", def.Description)
	}
	schemaJSON, err := json.Marshal(def.InputSchema)
	if err != nil {
		t.Fatalf("marshal record_goal_progress schema: %v", err)
	}
	schemaText := string(schemaJSON)
	for _, want := range []string{
		"get_goal.validation_plan",
		"get_goal.mission.validation_contract",
		"get_goal.mission.features",
		"get_goal.mission.milestones",
		"Omit feature_updates",
		"Omit milestone_updates",
		"Omit validation_updates",
	} {
		if !strings.Contains(schemaText, want) {
			t.Fatalf("record_goal_progress schema must mention %q, got %s", want, schemaText)
		}
	}
}

func TestRecordGoalProgressValidationIDsRecoverFromGetGoal(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	createSession := func(id string) session.SessionMetadata {
		meta := session.SessionMetadata{
			SchemaVersion:    1,
			ID:               id,
			CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
			Workdir:          t.TempDir(),
			Mode:             session.ModeRun,
			Provider:         "fake",
			Model:            "fake",
			CompletionPolicy: session.CompletionPolicyInteractive,
		}
		if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
			t.Fatalf("create session %s: %v", id, err)
		}
		return meta
	}
	execute := func(meta session.SessionMetadata, payload string) session.ToolResult {
		result, err := registry.Execute(context.Background(), "record_goal_progress", ExecContext{
			SessionID: meta.ID,
			Workdir:   meta.Workdir,
			Store:     store,
			Config:    cfg,
		}, json.RawMessage(payload))
		if err != nil {
			t.Fatalf("record_goal_progress execute: %v", err)
		}
		return result
	}

	withValidation := createSession("goal_progress_validation_recovery")
	goal, err := store.CreateGoal(withValidation.ID, session.GoalDraft{
		Enabled:        true,
		Mode:           session.GoalModeGoal,
		Objective:      "Validate progress ids",
		ValidationPlan: []string{"go test ./..."},
		Source:         session.GoalSourceTool,
	})
	if err != nil {
		t.Fatalf("create goal with validation: %v", err)
	}
	validID := goal.ValidationPlan[0].ID
	for _, tc := range []struct {
		name    string
		payload string
	}{
		{name: "validation update", payload: `{"validation_updates":[{"id":"invented-validation","status":"verified"}]}`},
		{name: "progress validation reference", payload: `{"summary":"validation evidence","validation_ids":["invented-validation"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := execute(withValidation, tc.payload)
			if !result.IsError || !strings.Contains(result.DisplayOutput, validID) || !strings.Contains(result.DisplayOutput, "get_goal") {
				t.Fatalf("expected valid validation ids and get_goal recovery, got %#v", result)
			}
		})
	}

	withoutValidation := createSession("goal_progress_validation_empty")
	if _, err := store.CreateGoal(withoutValidation.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeGoal,
		Objective: "No validation items",
		Source:    session.GoalSourceTool,
	}); err != nil {
		t.Fatalf("create goal without validation: %v", err)
	}
	for _, tc := range []struct {
		name      string
		payload   string
		omitField string
	}{
		{name: "validation update", payload: `{"validation_updates":[{"id":"invented-validation","status":"verified"}]}`, omitField: "omit validation_updates"},
		{name: "progress validation reference", payload: `{"summary":"validation evidence","validation_ids":["invented-validation"]}`, omitField: "omit validation_ids"},
		{name: "feature update", payload: `{"feature_updates":[{"id":"invented-feature","status":"completed"}]}`, omitField: "omit feature_updates"},
		{name: "milestone update", payload: `{"milestone_updates":[{"id":"invented-milestone","status":"completed"}]}`, omitField: "omit milestone_updates"},
	} {
		t.Run("empty "+tc.name, func(t *testing.T) {
			result := execute(withoutValidation, tc.payload)
			if !result.IsError || !strings.Contains(result.DisplayOutput, tc.omitField) || !strings.Contains(result.DisplayOutput, "get_goal") {
				t.Fatalf("expected empty-id omission recovery %q, got %#v", tc.omitField, result)
			}
		})
	}
}

func TestRecordGoalProgressMissionIDsRecoverFromGetGoal(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "goal_progress_mission_recovery",
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
	goal, err := store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:        true,
		Mode:           session.GoalModeMission,
		Objective:      "Validate mission progress ids",
		Features:       []string{"Feature one"},
		Milestones:     []string{"Milestone one"},
		ValidationPlan: []string{"Mission validation"},
		Source:         session.GoalSourceTool,
	})
	if err != nil {
		t.Fatalf("create mission goal: %v", err)
	}
	if goal.Mission == nil || len(goal.Mission.Features) != 1 || len(goal.Mission.Milestones) != 1 || len(goal.Mission.ValidationContract) != 1 {
		t.Fatalf("expected mission ids, got %#v", goal)
	}
	featureID := goal.Mission.Features[0].ID
	milestoneID := goal.Mission.Milestones[0].ID
	validationID := goal.Mission.ValidationContract[0].ID
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{SessionID: meta.ID, Workdir: meta.Workdir, Store: store, Config: cfg}

	tests := []struct {
		name    string
		payload string
		wantID  string
		field   string
	}{
		{name: "feature id", payload: `{"feature_updates":[{"id":"invented-feature","status":"completed"}]}`, wantID: featureID, field: "feature_updates"},
		{name: "milestone id", payload: `{"milestone_updates":[{"id":"invented-milestone","status":"completed"}]}`, wantID: milestoneID, field: "milestone_updates"},
		{name: "validation id", payload: `{"validation_updates":[{"id":"invented-validation","status":"verified"}]}`, wantID: validationID, field: "validation_updates"},
		{name: "feature assertion", payload: fmt.Sprintf(`{"feature_updates":[{"id":%q,"claimed_assertions":["invented-validation"]}]}`, featureID), wantID: validationID, field: "claimed_assertions"},
		{name: "milestone validation", payload: fmt.Sprintf(`{"milestone_updates":[{"id":%q,"validation_ids":["invented-validation"]}]}`, milestoneID), wantID: validationID, field: "validation_ids"},
		{name: "milestone feature", payload: fmt.Sprintf(`{"milestone_updates":[{"id":%q,"feature_ids":["invented-feature"]}]}`, milestoneID), wantID: featureID, field: "feature_ids"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := registry.Execute(context.Background(), "record_goal_progress", execCtx, json.RawMessage(tc.payload))
			if err != nil {
				t.Fatalf("record_goal_progress execute: %v", err)
			}
			if !result.IsError || !strings.Contains(result.DisplayOutput, tc.wantID) || !strings.Contains(result.DisplayOutput, "get_goal") || !strings.Contains(result.DisplayOutput, tc.field) {
				t.Fatalf("expected mission id recovery for %s, got %#v", tc.field, result)
			}
		})
	}
}

func TestGoalToolsRejectMissingSessionMetadata(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{
		SessionID: "missing_goal_tool_session",
		Workdir:   t.TempDir(),
		Store:     store,
		Config:    cfg,
	}

	result, err := registry.Execute(context.Background(), "create_goal", execCtx, json.RawMessage(`{
		"objective":"Do not create an orphan goal"
	}`))
	if err != nil {
		t.Fatalf("create_goal execute: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "session.json") {
		t.Fatalf("expected missing session metadata error, got %#v", result)
	}
	if _, err := store.LoadGoal(execCtx.SessionID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing-session create_goal should not leave goal snapshot, got %v", err)
	}

	readResult, err := registry.Execute(context.Background(), "get_goal", execCtx, nil)
	if err != nil {
		t.Fatalf("get_goal execute: %v", err)
	}
	if readResult.IsError || readResult.LLMOutput != "null" || readResult.Metadata["empty_state"] != true || readResult.Metadata["goal_present"] != false {
		t.Fatalf("expected missing-session get_goal to return semantic empty state, got %#v", readResult)
	}

	for _, tc := range []struct {
		name string
		raw  json.RawMessage
	}{
		{name: "record_goal_progress", raw: json.RawMessage(`{"summary":"progress should not write"}`)},
		{name: "update_goal", raw: json.RawMessage(`{"status":"complete"}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := registry.Execute(context.Background(), tc.name, execCtx, tc.raw)
			if err != nil {
				t.Fatalf("%s execute: %v", tc.name, err)
			}
			if !result.IsError || !strings.Contains(result.DisplayOutput, "session.json") {
				t.Fatalf("expected missing session metadata error for %s, got %#v", tc.name, result)
			}
		})
	}
}

func TestSessionScopedToolsRejectMissingSessionMetadata(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{
		SessionID: "missing_tool_scoped_session",
		Workdir:   t.TempDir(),
		Store:     store,
		Config:    cfg,
	}

	for _, tc := range []struct {
		name       string
		wantOutput string
		wantMeta   string
	}{
		{name: "get_plan_mode", wantOutput: "null", wantMeta: "enabled"},
		{name: "todo_read", wantOutput: "[]", wantMeta: "count"},
	} {
		t.Run(tc.name+"_empty_state", func(t *testing.T) {
			result, err := registry.Execute(context.Background(), tc.name, execCtx, nil)
			if err != nil {
				t.Fatalf("%s execute: %v", tc.name, err)
			}
			if result.IsError || result.LLMOutput != tc.wantOutput || result.Metadata["empty_state"] != true {
				t.Fatalf("expected semantic empty state for %s, got %#v", tc.name, result)
			}
			if _, ok := result.Metadata[tc.wantMeta]; !ok {
				t.Fatalf("expected %s metadata %q, got %#v", tc.name, tc.wantMeta, result.Metadata)
			}
		})
	}

	for _, tc := range []struct {
		name string
		raw  json.RawMessage
	}{
		{name: "task_list", raw: nil},
		{name: "feature_list_read", raw: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := registry.Execute(context.Background(), tc.name, execCtx, tc.raw)
			if err != nil {
				t.Fatalf("%s execute: %v", tc.name, err)
			}
			if !result.IsError || !strings.Contains(result.DisplayOutput, "session.json") {
				t.Fatalf("expected missing session metadata error for %s, got %#v", tc.name, result)
			}
		})
	}

	for _, tc := range []struct {
		name string
		raw  json.RawMessage
	}{
		{name: "todo_write", raw: json.RawMessage(`{"todos":[{"content":"Do not orphan todo","status":"pending"}]}`)},
		{name: "task_create", raw: json.RawMessage(`{"subject":"Do not orphan task"}`)},
		{name: "feature_list_create", raw: json.RawMessage(`{"features":[{"description":"Do not orphan feature list"}]}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := registry.Execute(context.Background(), tc.name, execCtx, tc.raw)
			if err != nil {
				t.Fatalf("%s execute: %v", tc.name, err)
			}
			if !result.IsError || !strings.Contains(result.DisplayOutput, "session.json") {
				t.Fatalf("expected missing session metadata error for %s, got %#v", tc.name, result)
			}
		})
	}

	if _, err := os.Stat(filepath.Join(store.SessionDir(execCtx.SessionID), "todo.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing-session todo_write should not leave todo snapshot, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.SessionDir(execCtx.SessionID), "tasks")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing-session task_create should not leave task directory, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.SessionDir(execCtx.SessionID), "feature_list.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing-session feature_list_create should not leave feature list snapshot, got %v", err)
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

func TestCreateGoalReportsLinkedPlanModeRelinkEventErrorAndRestoresGoal(t *testing.T) {
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
	initialPlanMode, err := store.CreatePlanMode(meta.ID, session.PlanModeDraft{
		Enabled:   true,
		Objective: "Existing unlinked planning gate",
		Source:    session.PlanModeSourceCLI,
	})
	if err != nil {
		t.Fatalf("create initial plan mode: %v", err)
	}
	if initialPlanMode.LinkedGoalID != "" {
		t.Fatalf("test setup expected unlinked Plan Mode, got %#v", initialPlanMode)
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
	previousPlanModeHistory, err := store.LoadPlanModeHistory(meta.ID)
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
			switch eventType {
			case "goal.created":
				return nil
			case "planmode.linked_goal":
				return eventErr
			default:
				t.Fatalf("unexpected required event %q", eventType)
				return nil
			}
		},
	}

	result, err := registry.Execute(context.Background(), "create_goal", execCtx, json.RawMessage(`{
		"objective":"Create a mission that reuses an existing pending Plan Mode gate",
		"mode":"mission",
		"require_plan_approval":true
	}`))
	if err != nil {
		t.Fatalf("create_goal execute: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "planmode.linked_goal") || !strings.Contains(result.DisplayOutput, eventErr.Error()) {
		t.Fatalf("expected linked goal event error result, got %#v", result)
	}
	if _, err := store.LoadGoal(meta.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected failed linked Plan Mode event to remove goal snapshot, got %v", err)
	}
	restoredPlanMode, err := store.SnapshotPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("snapshot restored plan mode: %v", err)
	}
	if restoredPlanMode.HasState != previousPlanMode.HasState || restoredPlanMode.State.LinkedGoalID != previousPlanMode.State.LinkedGoalID || restoredPlanMode.State.PlanModeID != previousPlanMode.State.PlanModeID {
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
		t.Fatalf("load restored plan mode history: %v", err)
	}
	if len(planHistory) != len(previousPlanModeHistory) {
		t.Fatalf("expected plan mode history restored to %d entries, got %d: %#v", len(previousPlanModeHistory), len(planHistory), planHistory)
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
		SessionID:             meta.ID,
		ToolCallID:            "call_shell_compaction_metadata",
		Workdir:               meta.Workdir,
		EphemeralArtifactRoot: filepath.Join(store.SessionDir(meta.ID), "artifacts", "tool-outputs"),
		Store:                 store,
		Config:                cfg,
	}

	shellResult, err := registry.Execute(context.Background(), "shell", execCtx, json.RawMessage(`{
		"command":"yes A | head -n 20000"
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
	if shellResult.Metadata["command"] != "yes A | head -n 20000" {
		t.Fatalf("expected command metadata, got %#v", shellResult.Metadata)
	}
	if shellResult.Metadata["artifact_complete"] != true || shellResult.Metadata["recoverable"] != true {
		t.Fatalf("expected current-result command artifact, got %#v", shellResult.Metadata)
	}
	for _, want := range []string{"[command_result tool=shell", "exit_code=0", "raw_output_bytes=", "truncated=true"} {
		if !strings.Contains(shellResult.LLMOutput, want) {
			t.Fatalf("expected shell LLM output to include %q, got %q", want, shellResult.LLMOutput)
		}
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

func TestShellRejectsBlankCommand(t *testing.T) {
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
	result, err := registry.Execute(context.Background(), "shell", ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}, json.RawMessage(`{
		"command":" \n\t "
	}`))
	if err != nil {
		t.Fatalf("shell: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "command is required") {
		t.Fatalf("expected shell to reject blank command, got %#v", result)
	}
}

func TestDiscoveryToolDescriptionsDoNotMandateRetrievalOrder(t *testing.T) {
	definitions := []Definition{defReadFile(), defGrepFiles(), defGrep(), defGlob()}
	for _, definition := range definitions {
		lowered := strings.ToLower(definition.Description)
		for _, forbidden := range []string{"grep_files first", "before reading", "before read_file"} {
			if strings.Contains(lowered, forbidden) {
				t.Fatalf("%s description fixes a model workflow with %q: %s", definition.Name, forbidden, definition.Description)
			}
		}
	}
	grepDescription := strings.ToLower(defGrep().Description)
	if !strings.Contains(grepDescription, "when paths are unknown") || !strings.Contains(grepDescription, "exact snippets or line numbers") {
		t.Fatalf("grep description must explain capabilities without ordering them: %s", defGrep().Description)
	}
}

func TestShellDoesNotLoadLoginOrInteractiveStartupFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash startup-file contract is Unix-specific")
	}
	for _, sandbox := range []string{"off", "bwrap"} {
		t.Run(sandbox, func(t *testing.T) {
			if sandbox == "bwrap" {
				bwrapPath, err := exec.LookPath("bwrap")
				if err != nil {
					t.Skip("bwrap is not installed")
				}
				if err := exec.Command(bwrapPath, "--die-with-parent", "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "/bin/true").Run(); err != nil {
					t.Skipf("bwrap namespaces are unavailable: %v", err)
				}
			}
			cfg := config.Default()
			cfg.Runtime.ShellEnvAllowlist = []string{"PATH", "HOME", "LANG", "TERM"}
			if sandbox == "bwrap" {
				cfg.Runtime.Shell.Sandbox = "bwrap"
			}
			store := session.NewStore(t.TempDir())
			workdir := t.TempDir()
			profileHome := filepath.Join(workdir, "profile-home")
			if err := os.MkdirAll(profileHome, 0o700); err != nil {
				t.Fatalf("create profile home: %v", err)
			}
			sentinel := filepath.Join(profileHome, "startup-side-effect")
			startup := "export AUDIT_PROFILE_SECRET=profile-leak\nprintf loaded > " + strconv.Quote(sentinel) + "\n"
			for _, name := range []string{".bash_profile", ".profile", ".bashrc"} {
				if err := os.WriteFile(filepath.Join(profileHome, name), []byte(startup), 0o600); err != nil {
					t.Fatalf("write %s: %v", name, err)
				}
			}
			t.Setenv("HOME", profileHome)
			t.Setenv("AUDIT_PROFILE_SECRET", "parent-leak")
			t.Setenv("BASH_ENV", filepath.Join(profileHome, ".bashrc"))
			meta := session.SessionMetadata{SchemaVersion: 1, ID: session.NewSessionID(), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: workdir, Mode: session.ModeRun, Provider: "fake", Model: "fake", CompletionPolicy: session.CompletionPolicyInteractive}
			if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
				t.Fatalf("create session: %v", err)
			}
			registry, err := NewRegistry(cfg, nil, store, nil)
			if err != nil {
				t.Fatalf("new registry: %v", err)
			}
			result, err := registry.Execute(context.Background(), "shell", ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}, json.RawMessage(`{"command":"printf '%s' \"${AUDIT_PROFILE_SECRET-unset}\""}`))
			if err != nil || result.IsError {
				t.Fatalf("shell err=%v result=%#v", err, result)
			}
			if strings.TrimSpace(result.DisplayOutput) != "unset" {
				t.Fatalf("startup or parent environment bypassed allowlist: %q", result.DisplayOutput)
			}
			if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
				t.Fatalf("startup file executed side effect, stat err=%v", err)
			}
			if got := result.Metadata["sandbox"]; got != sandbox {
				t.Fatalf("shell reported sandbox=%#v, want %q", got, sandbox)
			}
		})
	}
}

func TestWriteAndEditToolsApplyWorkspaceWriteDenylist(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, ".aegis-agent"), 0o700); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, ".aegis-agent", "config.yaml"), []byte("old"), 0o600); err != nil {
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
		"path":".aegis-agent/config.yaml",
		"old_text":"old",
		"new_text":"new"
	}`))
	if err != nil {
		t.Fatalf("edit_file: %v", err)
	}
	if !editResult.IsError || !strings.Contains(editResult.LLMOutput, "write denied: path '.aegis-agent/config.yaml' matches deny pattern '.aegis-agent/'") {
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

func TestEditFileRejectsEmptyOldText(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	target := filepath.Join(workdir, "notes.txt")
	if err := os.WriteFile(target, []byte("alpha\nbeta\n"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
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
	result, err := registry.Execute(context.Background(), "edit_file", ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}, json.RawMessage(`{
		"path":"notes.txt",
		"old_text":"",
		"new_text":"prefix\n"
	}`))
	if err != nil {
		t.Fatalf("edit_file: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "old_text is required") {
		t.Fatalf("expected edit_file to reject empty old_text, got %#v", result)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(data) != "alpha\nbeta\n" {
		t.Fatalf("empty old_text edit mutated file: %q", string(data))
	}
}

func TestEditFileOldTextNotFoundGivesDiagnosticHint(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	target := filepath.Join(workdir, "code.go")
	// Real content uses a tab for indentation.
	if err := os.WriteFile(target, []byte("func Foo() {\n\treturn 1\n}\n"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
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

	// Whitespace-only drift: old_text uses spaces where the file uses a tab.
	result, err := registry.Execute(context.Background(), "edit_file", execCtx, json.RawMessage(`{
		"path":"code.go",
		"old_text":"func Foo() {\n    return 1\n}",
		"new_text":"func Foo() {\n\treturn 2\n}"
	}`))
	if err != nil {
		t.Fatalf("edit_file: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "whitespace-insensitive match exists") {
		t.Fatalf("expected whitespace-drift hint, got %#v", result)
	}

	// First line matches but the block drifted: report the line number.
	result, err = registry.Execute(context.Background(), "edit_file", execCtx, json.RawMessage(`{
		"path":"code.go",
		"old_text":"func Foo() {\n\treturn 999\n}",
		"new_text":"x"
	}`))
	if err != nil {
		t.Fatalf("edit_file: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "first line matches at line 1") {
		t.Fatalf("expected drifted-block hint, got %#v", result)
	}
}

func TestFileToolsRejectBlankPath(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "notes.txt"), []byte("alpha\n"), 0o600); err != nil {
		t.Fatalf("write notes: %v", err)
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
	execCtx := ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}
	cases := []struct {
		name string
		args json.RawMessage
	}{
		{name: "read_file", args: json.RawMessage(`{"path":"  "}`)},
		{name: "write_file", args: json.RawMessage(`{"path":"  ","content":"bad"}`)},
		{name: "edit_file", args: json.RawMessage(`{"path":"  ","old_text":"alpha","new_text":"bad"}`)},
	}
	for _, tc := range cases {
		result, err := registry.Execute(context.Background(), tc.name, execCtx, tc.args)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !result.IsError || !strings.Contains(result.DisplayOutput, "path is required") {
			t.Fatalf("expected %s to reject blank path, got %#v", tc.name, result)
		}
	}
	data, err := os.ReadFile(filepath.Join(workdir, "notes.txt"))
	if err != nil {
		t.Fatalf("read notes: %v", err)
	}
	if string(data) != "alpha\n" {
		t.Fatalf("blank-path tools mutated notes.txt: %q", string(data))
	}
}

func TestTaskCreateRejectsBlankSubject(t *testing.T) {
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
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "task_create", ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}, json.RawMessage(`{"subject":" \n\t ","priority":"high"}`))
	if err != nil {
		t.Fatalf("task_create: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "subject is required") {
		t.Fatalf("expected task_create to reject blank subject, got %#v", result)
	}
	tasks, err := store.ListTasks(meta.ID)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("blank subject created durable task: %#v", tasks)
	}
}

func TestTaskToolsRejectInvalidPriority(t *testing.T) {
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
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}
	createResult, err := registry.Execute(context.Background(), "task_create", execCtx, json.RawMessage(`{"subject":"existing","priority":"urgent"}`))
	if err != nil {
		t.Fatalf("task_create: %v", err)
	}
	if !createResult.IsError || !strings.Contains(createResult.DisplayOutput, "invalid priority") {
		t.Fatalf("expected task_create invalid priority error, got %#v", createResult)
	}
	tasks, err := store.ListTasks(meta.ID)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("invalid create priority created task: %#v", tasks)
	}
	task, err := session.CreateTask(store, meta.ID, session.TaskCreateInput{Subject: "existing", Priority: "high"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	updateResult, err := registry.Execute(context.Background(), "task_update", execCtx, json.RawMessage(`{"task_id":"task_0001","priority":"urgent"}`))
	if err != nil {
		t.Fatalf("task_update: %v", err)
	}
	if !updateResult.IsError || !strings.Contains(updateResult.DisplayOutput, "invalid priority") {
		t.Fatalf("expected task_update invalid priority error, got %#v", updateResult)
	}
	unchanged, err := store.GetTask(meta.ID, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if unchanged.Priority != "high" {
		t.Fatalf("invalid update priority mutated task: %#v", unchanged)
	}
}

func TestTaskUpdateRejectsBlankInputs(t *testing.T) {
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
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	task, err := session.CreateTask(store, meta.ID, session.TaskCreateInput{Subject: "existing"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "blank task id",
			payload: `{"task_id":" \n\t ","status":"completed"}`,
			want:    "task_id is required",
		},
		{
			name:    "blank subject",
			payload: `{"task_id":"task_0001","subject":" \n\t "}`,
			want:    "subject is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := registry.Execute(context.Background(), "task_update", execCtx, json.RawMessage(tc.payload))
			if err != nil {
				t.Fatalf("task_update: %v", err)
			}
			if !result.IsError || !strings.Contains(result.DisplayOutput, tc.want) {
				t.Fatalf("expected %q error, got %#v", tc.want, result)
			}
			unchanged, err := store.GetTask(meta.ID, task.ID)
			if err != nil {
				t.Fatalf("get task: %v", err)
			}
			if unchanged.Subject != "existing" || unchanged.Status != "pending" {
				t.Fatalf("invalid task_update mutated task: %#v", unchanged)
			}
		})
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

func TestShellDenyPolicyBlocksSecretPathWriteCommand(t *testing.T) {
	for _, tt := range []struct {
		name    string
		command string
		target  string
	}{
		{name: "direct", command: "cp token.txt .env.local", target: ".env.local"},
		{name: "env wrapped", command: "env cp token.txt .env.local", target: ".env.local"},
		{name: "command wrapped", command: "command cp token.txt .env.command.local", target: ".env.command.local"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Runtime.ExecPolicy.Mode = "deny"
			store := session.NewStore(t.TempDir())
			workdir := t.TempDir()
			sourcePath := filepath.Join(workdir, "token.txt")
			if err := os.WriteFile(sourcePath, []byte("token\n"), 0o600); err != nil {
				t.Fatalf("write source token: %v", err)
			}
			meta := session.SessionMetadata{SchemaVersion: 1, ID: session.NewSessionID(), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: workdir, Mode: session.ModeRun, Provider: "fake", Model: "fake", CompletionPolicy: session.CompletionPolicyInteractive}
			if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
				t.Fatalf("create session: %v", err)
			}
			registry, err := NewRegistry(cfg, nil, store, nil)
			if err != nil {
				t.Fatalf("new registry: %v", err)
			}
			args, err := json.Marshal(map[string]string{"command": tt.command})
			if err != nil {
				t.Fatalf("marshal shell args: %v", err)
			}
			result, err := registry.Execute(context.Background(), "shell", ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}, args)
			if err != nil {
				t.Fatalf("shell: %v", err)
			}
			if !result.IsError || !strings.Contains(result.LLMOutput, "shell command denied by exec policy") {
				t.Fatalf("expected common write command to be denied, got %#v", result)
			}
			if _, err := os.Stat(filepath.Join(workdir, tt.target)); !os.IsNotExist(err) {
				t.Fatalf("deny mode should not create %s, stat err=%v", tt.target, err)
			}
		})
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
	if result.DisplayOutput != InterruptedToolExecutionMessage || !strings.Contains(result.LLMOutput, "partially executed") || !strings.Contains(result.LLMOutput, "Verify state before re-running") {
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

func TestShellToolKeepsResolvedWorkdirWhenPathReplacedBeforeStart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("stable shell workdir uses /proc/self/fd on non-sandboxed linux commands")
	}
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	root := t.TempDir()
	workdir := filepath.Join(root, "workspace")
	safeDir := filepath.Join(workdir, "safe")
	renamedSafeDir := filepath.Join(workdir, "safe-renamed")
	outsideDir := filepath.Join(root, "outside")
	for _, dir := range []string{safeDir, outsideDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
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

	restoreHook := beforeShellCommandStart
	beforeShellCommandStart = func(commandWorkdir string) error {
		if commandWorkdir != safeDir {
			return nil
		}
		if err := os.Rename(safeDir, renamedSafeDir); err != nil {
			return err
		}
		return os.Symlink(outsideDir, safeDir)
	}
	defer func() {
		beforeShellCommandStart = restoreHook
	}()

	result, err := registry.Execute(context.Background(), "shell", execCtx, json.RawMessage(`{
		"command":"printf stable > marker.txt",
		"workdir":"safe"
	}`))
	if err != nil {
		t.Fatalf("execute shell: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected shell to run in originally resolved workdir, got %#v", result)
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "marker.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shell followed replaced workdir symlink outside workspace, stat err=%v", err)
	}
	data, err := os.ReadFile(filepath.Join(renamedSafeDir, "marker.txt"))
	if err != nil {
		t.Fatalf("expected marker in originally resolved workdir: %v", err)
	}
	if strings.TrimSpace(string(data)) != "stable" {
		t.Fatalf("unexpected marker content: %q", data)
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
	for _, name := range []string{"agent_spawn", "agent_wait", "agent_stop", "agent_prompt", "agent_status", "agent_list"} {
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
	for _, name := range []string{"agent_spawn", "agent_wait", "agent_stop", "agent_prompt", "agent_status", "agent_list"} {
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
		"prompt":        "objective, scope, boundaries",
		"agent_role":    "Choose exactly one of planner, generator, or evaluator",
		"background":    "agent_status or agent_list",
		"resume_parent": "automatically resume",
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
	waitDef := registry.Get("agent_wait")
	if waitDef == nil {
		t.Fatal("agent_wait definition missing")
	}
	if !strings.Contains(waitDef.Description, "Park the parent agent") || !strings.Contains(waitDef.Description, "does not cancel") {
		t.Fatalf("expected agent_wait description to explain parking semantics, got %q", waitDef.Description)
	}
	if !strings.Contains(waitDef.Description, "any background child job") || !strings.Contains(waitDef.Description, "queue_job_id is optional") {
		t.Fatalf("expected agent_wait description to explain any-result wake semantics, got %q", waitDef.Description)
	}
	stopDef := registry.Get("agent_stop")
	if stopDef == nil {
		t.Fatal("agent_stop definition missing")
	}
	if !strings.Contains(stopDef.Description, "Cancel child work") || !strings.Contains(stopDef.Description, "budget-paused") || !strings.Contains(stopDef.Description, "running children") || !strings.Contains(stopDef.Description, "Cancellation is distinct from execution failure") {
		t.Fatalf("expected agent_stop description to explain stop boundary, got %q", stopDef.Description)
	}
	promptDef := registry.Get("agent_prompt")
	if promptDef == nil {
		t.Fatal("agent_prompt definition missing")
	}
	if !strings.Contains(promptDef.Description, "Send a prompt/steer") || !strings.Contains(promptDef.Description, "request a progress update") || !strings.Contains(promptDef.Description, "budget_extension") || !strings.Contains(promptDef.Description, "interrupt defaults to false") {
		t.Fatalf("expected agent_prompt description to explain child steer semantics, got %q", promptDef.Description)
	}
}

func TestAgentSpawnRejectsBlankPromptBeforeControlPlane(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	control := &recordingControlPlane{}
	registry, err := NewRegistry(cfg, nil, store, control)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "agent_spawn", ExecContext{
		SessionID: "sess_parent",
		Store:     store,
		Config:    cfg,
	}, json.RawMessage(`{"prompt":" \n\t "}`))
	if err != nil {
		t.Fatalf("agent_spawn execute: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "prompt is required") {
		t.Fatalf("expected blank prompt rejection, got %#v", result)
	}
	if control.spawnCalls != 0 {
		t.Fatalf("blank prompt reached control plane: calls=%d req=%#v", control.spawnCalls, control.spawnReq)
	}
}

func TestAgentSpawnResumeParentMetadata(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	meta := session.SessionMetadata{SchemaVersion: 1, ID: "sess_parent", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: t.TempDir(), Mode: session.ModeRun, Provider: "fake", Model: "fake", CompletionPolicy: session.CompletionPolicyInteractive}
	state := session.State{Status: session.StatusRunning, Phase: "tool_execute", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	control := &recordingControlPlane{}
	registry, err := NewRegistry(cfg, nil, store, control)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "agent_spawn", ExecContext{
		SessionID: meta.ID,
		Store:     store,
		Config:    cfg,
	}, json.RawMessage(`{"prompt":"child work","background":true,"resume_parent":true}`))
	if err != nil {
		t.Fatalf("agent_spawn execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected successful agent_spawn, got %#v", result)
	}
	if !control.spawnReq.ResumeParent || !control.spawnReq.Background {
		t.Fatalf("expected resume_parent to reach control plane, got %#v", control.spawnReq)
	}
	if result.Metadata["background_wait"] != true {
		t.Fatalf("expected background_wait metadata, got %#v", result.Metadata)
	}
}

func TestAgentWaitReturnsBackgroundWaitMetadata(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	meta := session.SessionMetadata{SchemaVersion: 1, ID: "sess_parent_wait", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: t.TempDir(), Mode: session.ModeRun, Provider: "fake", Model: "fake", CompletionPolicy: session.CompletionPolicyInteractive}
	state := session.State{Status: session.StatusRunning, Phase: "tool_execute", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	control := &recordingControlPlane{}
	registry, err := NewRegistry(cfg, nil, store, control)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "agent_wait", ExecContext{
		SessionID: meta.ID,
		Store:     store,
		Config:    cfg,
	}, json.RawMessage(`{"queue_job_id":"job_child_1"}`))
	if err != nil {
		t.Fatalf("agent_wait execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected successful agent_wait, got %#v", result)
	}
	if result.Metadata["background_wait"] != true || result.Metadata["queue_job_id"] != "job_child_1" {
		t.Fatalf("expected background wait metadata, got %#v", result.Metadata)
	}
	if control.statusCalls != 1 || control.statusReq.QueueJobID != "job_child_1" || control.statusReq.ParentSessionID != meta.ID {
		t.Fatalf("expected agent_wait to verify queue ownership, calls=%d req=%#v", control.statusCalls, control.statusReq)
	}
}

func TestAgentWaitAllowsEmptyInputAndDoesNotVerifySpecificJob(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	meta := session.SessionMetadata{SchemaVersion: 1, ID: "sess_parent_wait_any", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: t.TempDir(), Mode: session.ModeRun, Provider: "fake", Model: "fake", CompletionPolicy: session.CompletionPolicyInteractive}
	state := session.State{Status: session.StatusRunning, Phase: "tool_execute", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	control := &recordingControlPlane{}
	registry, err := NewRegistry(cfg, nil, store, control)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "agent_wait", ExecContext{
		SessionID: meta.ID,
		Store:     store,
		Config:    cfg,
	}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("agent_wait execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected successful agent_wait without queue_job_id, got %#v", result)
	}
	if result.Metadata["background_wait"] != true || result.Metadata["queue_job_id"] != nil {
		t.Fatalf("expected generic background wait metadata, got %#v", result.Metadata)
	}
	if control.statusCalls != 0 {
		t.Fatalf("generic agent_wait should not verify a specific queue job, calls=%d req=%#v", control.statusCalls, control.statusReq)
	}
	if !strings.Contains(result.DisplayOutput, "any_background_result") {
		t.Fatalf("expected generic wait output to document wake semantics, got %q", result.DisplayOutput)
	}
}

func TestAgentSpawnRejectsNestedSubAgentSession(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "sess_child",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyAutonomous,
		ParentSessionID:  "sess_master",
		RootSessionID:    "sess_master",
		Depth:            1,
	}
	state := session.State{Status: session.StatusRunning, Phase: "tool_execute", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	control := &recordingControlPlane{}
	registry, err := NewRegistry(cfg, nil, store, control)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "agent_spawn", ExecContext{
		SessionID: meta.ID,
		Store:     store,
		Config:    cfg,
	}, json.RawMessage(`{"prompt":"nested work","background":true}`))
	if err != nil {
		t.Fatalf("agent_spawn execute: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "nested sub-agents are not allowed") {
		t.Fatalf("expected nested sub-agent rejection, got %#v", result)
	}
	if control.spawnCalls != 0 {
		t.Fatalf("nested agent_spawn reached control plane: calls=%d req=%#v", control.spawnCalls, control.spawnReq)
	}
}

func TestAgentStopPassesCurrentSessionToControlPlane(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	meta := session.SessionMetadata{SchemaVersion: 1, ID: "sess_parent_stop", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: t.TempDir(), Mode: session.ModeRun, Provider: "fake", Model: "fake", CompletionPolicy: session.CompletionPolicyInteractive}
	state := session.State{Status: session.StatusRunning, Phase: "tool_execute", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	control := &recordingControlPlane{}
	registry, err := NewRegistry(cfg, nil, store, control)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "agent_stop", ExecContext{
		SessionID: meta.ID,
		Store:     store,
		Config:    cfg,
	}, json.RawMessage(`{"queue_job_id":"job_child_1"}`))
	if err != nil {
		t.Fatalf("agent_stop execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected successful agent_stop, got %#v", result)
	}
	if control.stopCalls != 1 || control.stopReq.QueueJobID != "job_child_1" || control.stopReq.ParentSessionID != meta.ID {
		t.Fatalf("expected agent_stop to use current parent session, calls=%d req=%#v", control.stopCalls, control.stopReq)
	}
}

func TestAgentToolsRejectMissingSessionMetadataBeforeControlPlane(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	control := &recordingControlPlane{}
	registry, err := NewRegistry(cfg, nil, store, control)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{
		SessionID: "missing_agent_tool_parent",
		Store:     store,
		Config:    cfg,
	}

	cases := []struct {
		name string
		tool string
		args json.RawMessage
	}{
		{name: "spawn", tool: "agent_spawn", args: json.RawMessage(`{"prompt":"audit child slice"}`)},
		{name: "wait", tool: "agent_wait", args: json.RawMessage(`{"queue_job_id":"job_missing_parent"}`)},
		{name: "stop", tool: "agent_stop", args: json.RawMessage(`{"queue_job_id":"job_missing_parent"}`)},
		{name: "prompt", tool: "agent_prompt", args: json.RawMessage(`{"session_id":"child_missing_parent","message":"stop discovery"}`)},
		{name: "status_session", tool: "agent_status", args: json.RawMessage(`{"session_id":"child_missing_parent"}`)},
		{name: "status_queue", tool: "agent_status", args: json.RawMessage(`{"queue_job_id":"job_missing_parent"}`)},
		{name: "list", tool: "agent_list", args: json.RawMessage(`{}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := registry.Execute(context.Background(), tc.tool, execCtx, tc.args)
			if err != nil {
				t.Fatalf("%s execute: %v", tc.tool, err)
			}
			if !result.IsError || !strings.Contains(result.DisplayOutput, "session.json") {
				t.Fatalf("expected missing session metadata error, got %#v", result)
			}
		})
	}
	if control.spawnCalls != 0 || control.stopCalls != 0 || control.promptCalls != 0 || control.statusCalls != 0 || control.listCalls != 0 {
		t.Fatalf("missing current session reached control plane: spawn=%d stop=%d prompt=%d status=%d list=%d spawnReq=%#v stopReq=%#v promptReq=%#v statusReq=%#v listParent=%q", control.spawnCalls, control.stopCalls, control.promptCalls, control.statusCalls, control.listCalls, control.spawnReq, control.stopReq, control.promptReq, control.statusReq, control.listParent)
	}
}

func TestAgentPromptPassesCurrentSessionToControlPlane(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	meta := session.SessionMetadata{SchemaVersion: 1, ID: "sess_parent_prompt", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: t.TempDir(), Mode: session.ModeRun, Provider: "fake", Model: "fake", CompletionPolicy: session.CompletionPolicyInteractive}
	state := session.State{Status: session.StatusRunning, Phase: "tool_execute", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	control := &recordingControlPlane{}
	registry, err := NewRegistry(cfg, nil, store, control)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "agent_prompt", ExecContext{
		SessionID: meta.ID,
		Store:     store,
		Config:    cfg,
	}, json.RawMessage(`{"queue_job_id":"job_child_1","message":"stop discovery and write the handoff","interrupt":false}`))
	if err != nil {
		t.Fatalf("agent_prompt execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected successful agent_prompt, got %#v", result)
	}
	if control.promptCalls != 1 || control.promptReq.QueueJobID != "job_child_1" || control.promptReq.ParentSessionID != meta.ID || control.promptReq.Message != "stop discovery and write the handoff" {
		t.Fatalf("expected agent_prompt to use current parent session, calls=%d req=%#v", control.promptCalls, control.promptReq)
	}
	if control.promptReq.Interrupt == nil || *control.promptReq.Interrupt {
		t.Fatalf("expected explicit interrupt=false to reach control plane, got %#v", control.promptReq)
	}
}

func TestAgentStatusPassesCurrentSessionToControlPlane(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	control := &recordingControlPlane{}
	registry, err := NewRegistry(cfg, nil, store, control)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	parentID := session.NewSessionID()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := store.Create(session.SessionMetadata{
		SchemaVersion:    1,
		ID:               parentID,
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
		RootSessionID:    parentID,
	}, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create parent session: %v", err)
	}

	result, err := registry.Execute(context.Background(), "agent_status", ExecContext{
		SessionID: parentID,
		Store:     store,
		Config:    cfg,
	}, json.RawMessage(`{"session_id":"child_from_tool"}`))
	if err != nil {
		t.Fatalf("agent_status execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected agent_status to reach control plane, got %#v", result)
	}
	if control.statusCalls != 1 {
		t.Fatalf("expected one status control call, got %d", control.statusCalls)
	}
	if control.statusReq.ParentSessionID != parentID {
		t.Fatalf("expected current session parent id %q, got %#v", parentID, control.statusReq)
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
		"read_file":   {"known text file", "Registered skill bundle", "capped at 120 lines", "owning path is unknown"},
		"write_file":  {"Create or overwrite", "prefer edit_file"},
		"edit_file":   {"Replace exact text", "after reading"},
		"grep":        {"matching lines", "single file or directory", "use include"},
		"grep_files":  {"owning path is unknown", "return only files"},
		"finish":      {"required artifacts", "unrun/failed validation"},
		"todo_write":  {"progress ledger", "preserved", "does not perform or verify"},
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

func TestFeatureListCreateRejectsInvalidItems(t *testing.T) {
	cfg := config.Default()
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "empty list",
			payload: `{"features":[]}`,
			want:    "at least one feature is required",
		},
		{
			name:    "blank description",
			payload: `{"features":[{"description":" \n\t "}]}`,
			want:    "feature 1 description is required",
		},
		{
			name:    "blank step",
			payload: `{"features":[{"description":"Valid feature","steps":["plan","  "]}]}`,
			want:    "feature 1 step 2 is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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
			execCtx := ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}
			result, err := registry.Execute(context.Background(), "feature_list_create", execCtx, json.RawMessage(tc.payload))
			if err != nil {
				t.Fatalf("feature_list_create: %v", err)
			}
			if !result.IsError || !strings.Contains(result.DisplayOutput, tc.want) {
				t.Fatalf("expected feature_list_create error containing %q, got %#v", tc.want, result)
			}
			if _, err := store.LoadFeatureList(meta.ID); err == nil {
				t.Fatal("invalid feature_list_create wrote feature_list.json")
			}
		})
	}
}

func TestFeatureListUpdateRejectsInvalidInputs(t *testing.T) {
	cfg := config.Default()
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "blank id",
			payload: `{"id":" \n\t ","status":"completed"}`,
			want:    "id is required",
		},
		{
			name:    "invalid status",
			payload: `{"id":"feature_0001","status":"blocked"}`,
			want:    "invalid feature status",
		},
		{
			name:    "negative passes",
			payload: `{"id":"feature_0001","passes":-1}`,
			want:    "passes must be non-negative",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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
			execCtx := ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}
			createResult, err := registry.Execute(context.Background(), "feature_list_create", execCtx, json.RawMessage(`{"features":[{"description":"Keep original","steps":["validate"]}]}`))
			if err != nil || createResult.IsError {
				t.Fatalf("feature_list_create err=%v result=%#v", err, createResult)
			}
			result, err := registry.Execute(context.Background(), "feature_list_update", execCtx, json.RawMessage(tc.payload))
			if err != nil {
				t.Fatalf("feature_list_update: %v", err)
			}
			if !result.IsError || !strings.Contains(result.DisplayOutput, tc.want) {
				t.Fatalf("expected feature_list_update error containing %q, got %#v", tc.want, result)
			}
			snapshot, err := store.LoadFeatureList(meta.ID)
			if err != nil {
				t.Fatalf("load feature list: %v", err)
			}
			if len(snapshot.Features) != 1 || snapshot.Features[0].Status != "pending" || snapshot.Features[0].Passes != 0 {
				t.Fatalf("invalid feature_list_update mutated feature list: %#v", snapshot.Features)
			}
		})
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
	// A per-command timeout (parent ctx not cancelled) is a recoverable tool
	// error, not a propagated interrupt: err is nil, the result is flagged with
	// the command_timeout failure class and a timeout-specific message.
	if err != nil {
		t.Fatalf("expected nil error for recoverable command timeout, got %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected timeout result to be an error, got %#v", result)
	}
	if got := result.Metadata[MetadataFailureClass]; got != FailureClassTimeout {
		t.Fatalf("expected %q failure class, got %#v", FailureClassTimeout, result.Metadata)
	}
	if result.DisplayOutput != TimedOutToolExecutionMessage || !strings.Contains(result.LLMOutput, "timed out") {
		t.Fatalf("expected timeout message, got %#v", result)
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

func TestReadFileAllowsSessionEphemeralArtifactWithinWindow(t *testing.T) {
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
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	artifactRoot := filepath.Join(store.SessionDir(meta.ID), "artifacts", "tool-outputs")
	if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
		t.Fatalf("mkdir artifact root: %v", err)
	}
	lines := make([]string, 0, 180)
	for i := 1; i <= 180; i++ {
		lines = append(lines, fmt.Sprintf("artifact line %03d", i))
	}
	artifactPath := filepath.Join(artifactRoot, "grep_files-turn4.txt")
	if err := os.WriteFile(artifactPath, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	execCtx := ExecContext{
		SessionID:             meta.ID,
		Workdir:               meta.Workdir,
		EphemeralArtifactRoot: artifactRoot,
		Store:                 store,
		Config:                cfg,
	}

	result, err := registry.Execute(context.Background(), "read_file", execCtx, json.RawMessage(fmt.Sprintf(`{
		"path":%q,
		"limit":240
	}`, artifactPath)))
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected artifact read to succeed, got %#v", result)
	}
	if result.Metadata["path_source"] != "session_ephemeral_artifact" {
		t.Fatalf("expected session artifact source, got %#v", result.Metadata)
	}
	if !strings.Contains(result.DisplayOutput, "artifact line 120") || strings.Contains(result.DisplayOutput, "artifact line 121") {
		t.Fatalf("expected capped artifact window, got %q", result.DisplayOutput)
	}
	if !strings.Contains(result.DisplayOutput, "requested_limit=240 capped_to=120") {
		t.Fatalf("expected capped read annotation, got %q", result.DisplayOutput)
	}

	relativeArtifactPath := filepath.ToSlash(filepath.Join("artifacts", "tool-outputs", filepath.Base(artifactPath)))
	relativeResult, err := registry.Execute(context.Background(), "read_file", execCtx, json.RawMessage(fmt.Sprintf(`{
		"path":%q,
		"offset":121,
		"limit":20
	}`, relativeArtifactPath)))
	if err != nil {
		t.Fatalf("read_file relative artifact: %v", err)
	}
	if relativeResult.IsError || relativeResult.Metadata["path_source"] != "session_ephemeral_artifact" {
		t.Fatalf("expected relative session artifact read to succeed, got %#v", relativeResult)
	}
	if !strings.Contains(relativeResult.DisplayOutput, "artifact line 121") || strings.Contains(relativeResult.DisplayOutput, "artifact line 120") {
		t.Fatalf("expected relative artifact read window, got %q", relativeResult.DisplayOutput)
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

func TestDiscoveryToolsRejectSessionArtifactPathsBeforeWorkspaceResolution(t *testing.T) {
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
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}

	for _, toolName := range []string{"glob", "grep", "grep_files"} {
		for _, artifactPath := range []string{"artifacts/tool-outputs", "artifacts/tool-outputs/shell-call_OX4.txt"} {
			t.Run(toolName+"/"+filepath.Base(artifactPath), func(t *testing.T) {
				payload := fmt.Sprintf(`{"pattern":"needle","path":%q}`, artifactPath)
				var firstOutput string
				for attempt := 0; attempt < 2; attempt++ {
					result, err := registry.Execute(context.Background(), toolName, execCtx, json.RawMessage(payload))
					if err != nil {
						t.Fatalf("%s attempt %d: %v", toolName, attempt+1, err)
					}
					if !result.IsError ||
						!strings.Contains(result.DisplayOutput, "not searchable by discovery tools") ||
						!strings.Contains(result.DisplayOutput, "read_file") ||
						!strings.Contains(result.DisplayOutput, artifactPath) {
						t.Fatalf("expected directed artifact recovery for %s, got %#v", artifactPath, result)
					}
					if strings.Contains(strings.ToLower(result.DisplayOutput), "not found") || strings.Contains(strings.ToLower(result.DisplayOutput), "does not exist") {
						t.Fatalf("artifact path must not be misclassified as missing: %q", result.DisplayOutput)
					}
					if result.Metadata[MetadataFailureClass] != FailureClassUnsupportedPath ||
						result.Metadata["path"] != artifactPath ||
						result.Metadata["path_source"] != "session_ephemeral_artifact" {
						t.Fatalf("expected unsupported path metadata, got %#v", result.Metadata)
					}
					if attempt == 0 {
						firstOutput = result.DisplayOutput
					} else if result.DisplayOutput != firstOutput {
						t.Fatalf("repeated artifact discovery must return the same recovery error, first=%q second=%q", firstOutput, result.DisplayOutput)
					}
				}
			})
		}
	}
}

func TestGrepToolsReportMissingExplicitPath(t *testing.T) {
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
	execCtx := ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}

	for _, name := range []string{"glob", "grep", "grep_files"} {
		result, err := registry.Execute(context.Background(), name, execCtx, json.RawMessage(`{
			"pattern":"needle",
			"path":"missing.txt"
		}`))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !result.IsError || !strings.Contains(result.DisplayOutput, "missing.txt") {
			t.Fatalf("expected %s to report missing explicit path, got %#v", name, result)
		}
		if strings.Contains(result.DisplayOutput, "(no matches)") {
			t.Fatalf("expected %s missing path not to be reported as no matches, got %q", name, result.DisplayOutput)
		}
		if result.Metadata[MetadataFailureClass] != FailureClassNotFound {
			t.Fatalf("expected ordinary missing workspace path to remain not_found for %s, got %#v", name, result.Metadata)
		}
	}
}

func TestReadFileNotFoundSuggestsDiscoveryFirst(t *testing.T) {
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
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "read_file", ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}, json.RawMessage(`{
		"path":"vllm/vllm/lora/models.py"
	}`))
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected read_file not-found error, got %#v", result)
	}
	for _, want := range []string{"Do not keep guessing source paths", "when the path is unknown", "exact path supplied by the user or a prior tool result"} {
		if !strings.Contains(result.DisplayOutput, want) {
			t.Fatalf("expected read_file not-found hint %q, got %q", want, result.DisplayOutput)
		}
	}
	if got := result.Metadata[MetadataFailureClass]; got != FailureClassNotFound {
		t.Fatalf("expected not_found failure class, got %#v", result.Metadata)
	}
	if got := result.Metadata["path"]; got != "vllm/vllm/lora/models.py" {
		t.Fatalf("expected missing path metadata, got %#v", result.Metadata)
	}
}

func TestGrepToolsRejectEmptyPattern(t *testing.T) {
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
	execCtx := ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}

	if err := os.WriteFile(filepath.Join(workdir, "notes.txt"), []byte("first\nsecond\n"), 0o644); err != nil {
		t.Fatalf("write notes: %v", err)
	}
	for _, name := range []string{"grep", "grep_files"} {
		result, err := registry.Execute(context.Background(), name, execCtx, json.RawMessage(`{
			"pattern":""
		}`))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !result.IsError || !strings.Contains(result.DisplayOutput, "pattern is required") {
			t.Fatalf("expected %s to reject empty pattern, got %#v", name, result)
		}
		if strings.Contains(result.DisplayOutput, "notes.txt") {
			t.Fatalf("expected %s empty pattern not to dump workspace matches, got %q", name, result.DisplayOutput)
		}
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

func TestGlobRejectsEmptyPattern(t *testing.T) {
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
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "glob", ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}, json.RawMessage(`{
		"pattern":""
	}`))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "pattern is required") {
		t.Fatalf("expected glob to reject empty pattern, got %#v", result)
	}
}

func TestGlobAcceptsPathAndScopesResults(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	for _, dir := range []string{"vllm", "other"} {
		if err := os.MkdirAll(filepath.Join(workdir, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	for path, content := range map[string]string{
		"vllm/keep.go":   "package vllm\n",
		"vllm/readme.md": "# vllm\n",
		"other/keep.go":  "package other\n",
	} {
		if err := os.WriteFile(filepath.Join(workdir, filepath.FromSlash(path)), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
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
	execCtx := ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}

	scoped, err := registry.Execute(context.Background(), "glob", execCtx, json.RawMessage(`{"path":"vllm","pattern":"*.go"}`))
	if err != nil {
		t.Fatalf("glob with path: %v", err)
	}
	if scoped.IsError {
		t.Fatalf("expected glob to accept path argument, got %#v", scoped)
	}
	if !strings.Contains(scoped.DisplayOutput, "vllm/keep.go") || strings.Contains(scoped.DisplayOutput, "other/keep.go") || strings.Contains(scoped.DisplayOutput, "vllm/readme.md") {
		t.Fatalf("expected path-scoped glob result, got %q", scoped.DisplayOutput)
	}

	dotScoped, err := registry.Execute(context.Background(), "glob", execCtx, json.RawMessage(`{"path":".","pattern":"vllm/*.go"}`))
	if err != nil {
		t.Fatalf("glob with dot path: %v", err)
	}
	if dotScoped.IsError || !strings.Contains(dotScoped.DisplayOutput, "vllm/keep.go") {
		t.Fatalf("expected path=. to match workspace-root glob semantics, got %#v", dotScoped)
	}

	includeScoped, err := registry.Execute(context.Background(), "glob", execCtx, json.RawMessage(`{"path":".","pattern":"**/*","include":"**/*.md"}`))
	if err != nil {
		t.Fatalf("glob with include: %v", err)
	}
	if includeScoped.IsError || !strings.Contains(includeScoped.DisplayOutput, "vllm/readme.md") || strings.Contains(includeScoped.DisplayOutput, "vllm/keep.go") {
		t.Fatalf("expected include-scoped glob result, got %#v", includeScoped)
	}
}

func TestGlobHonorsLimitAndReportsTruncation(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(workdir, fmt.Sprintf("match-%d.txt", i)), []byte("x"), 0o600); err != nil {
			t.Fatalf("write match file: %v", err)
		}
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
	execCtx := ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}

	limited, err := registry.Execute(context.Background(), "glob", execCtx, json.RawMessage(`{"pattern":"*.txt","limit":2}`))
	if err != nil {
		t.Fatalf("glob with limit: %v", err)
	}
	if limited.IsError {
		t.Fatalf("expected glob to accept limit argument, got error result %#v", limited)
	}
	matchLines := 0
	for _, line := range strings.Split(limited.DisplayOutput, "\n") {
		if strings.HasSuffix(line, ".txt") {
			matchLines++
		}
	}
	if matchLines != 2 {
		t.Fatalf("expected 2 matches under limit, got %d in %q", matchLines, limited.DisplayOutput)
	}
	if !strings.Contains(limited.DisplayOutput, "Truncated at limit=2") {
		t.Fatalf("expected truncation notice, got %q", limited.DisplayOutput)
	}

	full, err := registry.Execute(context.Background(), "glob", execCtx, json.RawMessage(`{"pattern":"*.txt"}`))
	if err != nil {
		t.Fatalf("glob without limit: %v", err)
	}
	if strings.Contains(full.DisplayOutput, "Truncated") {
		t.Fatalf("did not expect truncation for full result, got %q", full.DisplayOutput)
	}

	exact, err := registry.Execute(context.Background(), "glob", execCtx, json.RawMessage(`{"pattern":"*.txt","limit":5}`))
	if err != nil {
		t.Fatalf("glob at exact limit: %v", err)
	}
	if strings.Contains(exact.DisplayOutput, "Truncated") {
		t.Fatalf("did not expect truncation when matches equal limit, got %q", exact.DisplayOutput)
	}
}

func TestGrepLimitBoundaryMetadata(t *testing.T) {
	const limit = 3
	for _, matchCount := range []int{0, limit - 1, limit, limit + 1} {
		matchCount := matchCount
		t.Run(fmt.Sprintf("matches_%d", matchCount), func(t *testing.T) {
			registry, execCtx, workdir := newSearchToolTestRegistry(t)
			var content strings.Builder
			for i := 0; i < matchCount; i++ {
				fmt.Fprintf(&content, "needle line %02d\n", i)
			}
			if matchCount == 0 {
				content.WriteString("haystack only\n")
			}
			if err := os.WriteFile(filepath.Join(workdir, "matches.txt"), []byte(content.String()), 0o600); err != nil {
				t.Fatalf("write grep fixture: %v", err)
			}

			result, err := registry.Execute(context.Background(), "grep", execCtx, json.RawMessage(`{
				"pattern":"needle",
				"path":".",
				"include":"*.txt",
				"limit":3
			}`))
			if err != nil {
				t.Fatalf("grep: %v", err)
			}
			if result.IsError {
				t.Fatalf("unexpected grep error result: %#v", result)
			}
			returnedCount := matchCount
			if returnedCount > limit {
				returnedCount = limit
			}
			hasMore := matchCount > limit
			assertSearchResultMetadata(t, result, returnedCount, limit, limit, hasMore, false, 0)
			if got := countOutputLinesContaining(result.DisplayOutput, ":needle line"); got != returnedCount {
				t.Errorf("returned grep match lines=%d, want %d; output=%q", got, returnedCount, result.DisplayOutput)
			}
			if got := strings.Contains(result.LLMOutput, "narrow path, include, or pattern"); got != hasMore {
				t.Errorf("overflow narrowing notice present=%v, want %v; output=%q", got, hasMore, result.LLMOutput)
			}
		})
	}
}

func TestGrepFilesLimitBoundaryMetadata(t *testing.T) {
	const limit = 3
	for _, matchCount := range []int{0, limit - 1, limit, limit + 1} {
		matchCount := matchCount
		t.Run(fmt.Sprintf("matches_%d", matchCount), func(t *testing.T) {
			registry, execCtx, workdir := newSearchToolTestRegistry(t)
			for i := 0; i < matchCount; i++ {
				path := filepath.Join(workdir, fmt.Sprintf("match-%02d.txt", i))
				if err := os.WriteFile(path, []byte("needle\n"), 0o600); err != nil {
					t.Fatalf("write grep_files fixture: %v", err)
				}
			}
			if matchCount == 0 {
				if err := os.WriteFile(filepath.Join(workdir, "haystack.txt"), []byte("no match\n"), 0o600); err != nil {
					t.Fatalf("write nonmatching grep_files fixture: %v", err)
				}
			}

			result, err := registry.Execute(context.Background(), "grep_files", execCtx, json.RawMessage(`{
				"pattern":"needle",
				"path":".",
				"include":"*.txt",
				"limit":3
			}`))
			if err != nil {
				t.Fatalf("grep_files: %v", err)
			}
			if result.IsError {
				t.Fatalf("unexpected grep_files error result: %#v", result)
			}
			returnedCount := matchCount
			if returnedCount > limit {
				returnedCount = limit
			}
			hasMore := matchCount > limit
			assertSearchResultMetadata(t, result, returnedCount, limit, limit, hasMore, false, 0)
			if got := countOutputLinesContaining(result.DisplayOutput, "match-"); got != returnedCount {
				t.Errorf("returned grep_files paths=%d, want %d; output=%q", got, returnedCount, result.DisplayOutput)
			}
			if got := strings.Contains(result.LLMOutput, "narrow path, include, or pattern"); got != hasMore {
				t.Errorf("overflow narrowing notice present=%v, want %v; output=%q", got, hasMore, result.LLMOutput)
			}
		})
	}
}

func TestGrepAndGrepFilesOrderingAndIncludeAreDeterministic(t *testing.T) {
	registry, execCtx, workdir := newSearchToolTestRegistry(t)
	fixtures := map[string]string{
		"a/01.txt": "needle alpha\n",
		"a/02.go":  "needle skipped\n",
		"b/01.txt": "needle bravo\n",
		"b/02.txt": "needle charlie\n",
	}
	for name, content := range fixtures {
		path := filepath.Join(workdir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir fixture parent for %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}

	tests := []struct {
		name       string
		tool       string
		wantOutput string
	}{
		{
			name:       "grep",
			tool:       "grep",
			wantOutput: "a/01.txt:1:needle alpha\nb/01.txt:1:needle bravo\nb/02.txt:1:needle charlie",
		},
		{
			name:       "grep_files",
			tool:       "grep_files",
			wantOutput: "a/01.txt\nb/01.txt\nb/02.txt",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := json.RawMessage(`{"pattern":"needle","path":".","include":"*.txt","limit":10}`)
			first, err := registry.Execute(context.Background(), test.tool, execCtx, raw)
			if err != nil {
				t.Fatalf("first %s: %v", test.tool, err)
			}
			second, err := registry.Execute(context.Background(), test.tool, execCtx, raw)
			if err != nil {
				t.Fatalf("second %s: %v", test.tool, err)
			}
			if first.DisplayOutput != test.wantOutput {
				t.Fatalf("%s ordered include output=%q, want %q", test.tool, first.DisplayOutput, test.wantOutput)
			}
			if second.DisplayOutput != first.DisplayOutput {
				t.Fatalf("%s output changed across identical executions: first=%q second=%q", test.tool, first.DisplayOutput, second.DisplayOutput)
			}
			assertSearchResultMetadata(t, first, 3, 10, 10, false, false, 0)
			assertSearchResultMetadata(t, second, 3, 10, 10, false, false, 0)
		})
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

func TestGrepTruncatesLongMatchingLines(t *testing.T) {
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
	execCtx := ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}

	longLine := "needle-\u4e2d\u6587\U0001f642-" + strings.Repeat("x", maxGrepLineOutputBytes*4) + "-needle-tail-\u7ed3\u5c3e"
	if err := os.WriteFile(filepath.Join(workdir, "bundle.html"), []byte(longLine), 0o644); err != nil {
		t.Fatalf("write bundle: %v", err)
	}

	result, err := registry.Execute(context.Background(), "grep", execCtx, json.RawMessage(`{
		"pattern":"needle",
		"path":"bundle.html"
	}`))
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if result.Metadata["truncated"] != true || result.Metadata["truncated_matching_lines"] != 1 {
		t.Fatalf("expected grep truncation metadata, got %#v", result.Metadata)
	}
	assertSearchResultMetadata(t, result, 1, 0, defaultGrepMatchesLimit, false, false, 1)
	if !strings.Contains(result.DisplayOutput, "bundle.html:1:needle-") {
		t.Fatalf("expected grep output to include path, line, and prefix, got %q", result.DisplayOutput)
	}
	if !strings.Contains(result.DisplayOutput, "[truncated:") || !strings.Contains(result.DisplayOutput, "needle-tail-\u7ed3\u5c3e") {
		t.Fatalf("expected grep output to preserve truncation marker and tail, got %q", result.DisplayOutput)
	}
	if !utf8.ValidString(result.DisplayOutput) {
		t.Fatalf("expected grep snippet truncation to preserve valid UTF-8, got %q", result.DisplayOutput)
	}
	if strings.Contains(result.LLMOutput, "narrow path, include, or pattern") {
		t.Fatalf("did not expect result-set overflow notice for one truncated snippet, got %q", result.LLMOutput)
	}
	if len(result.DisplayOutput) > maxGrepLineOutputBytes+len("bundle.html:1:")+256 {
		t.Fatalf("expected grep output to stay bounded, got %d bytes", len(result.DisplayOutput))
	}
}

func TestGrepOverflowSentinelDoesNotCountSnippetTruncation(t *testing.T) {
	registry, execCtx, workdir := newSearchToolTestRegistry(t)
	content := "needle returned\nneedle-" + strings.Repeat("x", maxGrepLineOutputBytes*4) + "-overflow-only\n"
	if err := os.WriteFile(filepath.Join(workdir, "matches.txt"), []byte(content), 0o600); err != nil {
		t.Fatalf("write grep overflow sentinel fixture: %v", err)
	}

	result, err := registry.Execute(context.Background(), "grep", execCtx, json.RawMessage(`{
		"pattern":"needle",
		"path":"matches.txt",
		"limit":1
	}`))
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	assertSearchResultMetadata(t, result, 1, 1, 1, true, false, 0)
	if result.Metadata["truncated"] != false || result.Metadata["truncated_matching_lines"] != 0 {
		t.Fatalf("overflow sentinel must not count as a returned truncated snippet: %#v", result.Metadata)
	}
	if strings.Contains(result.DisplayOutput, "overflow-only") {
		t.Fatalf("overflow sentinel leaked into returned output: %q", result.DisplayOutput)
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

func TestGrepAllowsRegisteredSkillReferencesOutsideWorkspace(t *testing.T) {
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

	grepResult, err := registry.Execute(context.Background(), "grep", execCtx, json.RawMessage(`{
		"path":"skills/pentest-toolset/references/full-audit-workflow.md",
		"pattern":"missing"
	}`))
	if err != nil {
		t.Fatalf("grep missing explicit skill file: %v", err)
	}
	if !grepResult.IsError || !strings.Contains(grepResult.DisplayOutput, "skills/pentest-toolset/references/full-audit-workflow.md") || strings.Contains(grepResult.DisplayOutput, "workspace/skills") {
		t.Fatalf("expected missing skill path error without workspace fallback, got %#v", grepResult)
	}

	grepResult, err = registry.Execute(context.Background(), "grep", execCtx, json.RawMessage(`{
		"path":"skills/pentest-toolset/references/01-cli-contract.md",
		"pattern":"schema",
		"include":"**/*.md"
	}`))
	if err != nil {
		t.Fatalf("grep skill path: %v", err)
	}
	if grepResult.IsError {
		t.Fatalf("expected skill grep to succeed, got %#v", grepResult)
	}
	if !strings.Contains(grepResult.DisplayOutput, "skills/pentest-toolset/references/01-cli-contract.md:3:Use schema first.") {
		t.Fatalf("expected skill-relative grep output, got %q", grepResult.DisplayOutput)
	}

	grepResult, err = registry.Execute(context.Background(), "grep", execCtx, json.RawMessage(`{
		"path":"skills/pentest-toolset/references",
		"pattern":"schema",
		"include":"**/*.txt"
	}`))
	if err != nil {
		t.Fatalf("grep skill path with include filter: %v", err)
	}
	if grepResult.IsError || strings.TrimSpace(grepResult.DisplayOutput) != "(no matches)" {
		t.Fatalf("expected include filter to suppress md match, got %#v", grepResult)
	}

	grepFilesResult, err := registry.Execute(context.Background(), "grep_files", execCtx, json.RawMessage(`{
		"path":"references",
		"pattern":"schema"
	}`))
	if err != nil {
		t.Fatalf("grep_files skill-relative path: %v", err)
	}
	if grepFilesResult.IsError {
		t.Fatalf("expected skill-relative grep_files to succeed, got %#v", grepFilesResult)
	}
	if strings.TrimSpace(grepFilesResult.DisplayOutput) != "skills/pentest-toolset/references/01-cli-contract.md" {
		t.Fatalf("expected skill-relative grep_files output, got %q", grepFilesResult.DisplayOutput)
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

func TestGrepFilesLimitIsCapped(t *testing.T) {
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
	execCtx := ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}

	if err := os.MkdirAll(filepath.Join(workdir, "pkg"), 0o755); err != nil {
		t.Fatalf("mkdir pkg: %v", err)
	}
	for i := 0; i < 205; i++ {
		path := filepath.Join(workdir, "pkg", fmt.Sprintf("match-%03d.txt", i))
		if err := os.WriteFile(path, []byte("needle\n"), 0o644); err != nil {
			t.Fatalf("write match file: %v", err)
		}
	}

	result, err := registry.Execute(context.Background(), "grep_files", execCtx, json.RawMessage(`{
		"pattern":"needle",
		"include":"*.txt",
		"limit":1000000
	}`))
	if err != nil {
		t.Fatalf("grep_files: %v", err)
	}
	if got := countOutputLinesContaining(result.DisplayOutput, "match-"); got != maxGrepFilesLimit {
		t.Fatalf("expected grep_files to cap oversized limits at %d results, got %d results", maxGrepFilesLimit, got)
	}
	assertSearchResultMetadata(t, result, maxGrepFilesLimit, 1000000, maxGrepFilesLimit, true, true, 0)
	if !strings.Contains(result.LLMOutput, "narrow path, include, or pattern") {
		t.Fatalf("expected capped grep_files overflow notice, got %q", result.LLMOutput)
	}
	if strings.Contains(result.DisplayOutput, "match-200.txt") {
		t.Fatalf("expected capped grep_files output not to include match-200.txt, got %q", result.DisplayOutput)
	}
}

func TestGrepLimitIsHonoredAndCapped(t *testing.T) {
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
	execCtx := ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}

	if err := os.MkdirAll(filepath.Join(workdir, "pkg"), 0o755); err != nil {
		t.Fatalf("mkdir pkg: %v", err)
	}
	var builder strings.Builder
	for i := 0; i < maxGrepMatches+5; i++ {
		fmt.Fprintf(&builder, "needle line %03d\n", i)
	}
	if err := os.WriteFile(filepath.Join(workdir, "pkg", "matches.txt"), []byte(builder.String()), 0o644); err != nil {
		t.Fatalf("write matches: %v", err)
	}

	result, err := registry.Execute(context.Background(), "grep", execCtx, json.RawMessage(`{
		"pattern":"needle",
		"path":"pkg",
		"include":"*.txt",
		"limit":3
	}`))
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if got := countOutputLinesContaining(result.DisplayOutput, ":needle line"); got != 3 {
		t.Fatalf("expected grep limit to return 3 matching lines, got %d: %q", got, result.DisplayOutput)
	}
	assertSearchResultMetadata(t, result, 3, 3, 3, true, false, 0)
	if strings.Contains(result.DisplayOutput, "needle line 003") {
		t.Fatalf("expected grep small limit to stop before fourth match, got %q", result.DisplayOutput)
	}

	result, err = registry.Execute(context.Background(), "grep", execCtx, json.RawMessage(`{
		"pattern":"needle",
		"path":"pkg",
		"include":"*.txt",
		"limit":1000000
	}`))
	if err != nil {
		t.Fatalf("grep capped: %v", err)
	}
	if got := countOutputLinesContaining(result.DisplayOutput, ":needle line"); got != maxGrepMatches {
		t.Fatalf("expected grep oversized limit capped at %d matching lines, got %d", maxGrepMatches, got)
	}
	assertSearchResultMetadata(t, result, maxGrepMatches, 1000000, maxGrepMatches, true, true, 0)
	if strings.Contains(result.DisplayOutput, "needle line 200") {
		t.Fatalf("expected capped grep output not to include match beyond cap, got %q", result.DisplayOutput)
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

func TestSkillCommandToolRejectsDuplicateToolNames(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	for _, skillName := range []string{"alpha", "beta"} {
		skillDir := filepath.Join(root, "skills", skillName)
		if err := os.MkdirAll(filepath.Join(skillDir, "tools"), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", skillName, err)
		}
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: "+skillName+"\ndescription: helper skill\n---\nbody\n"), 0o644); err != nil {
			t.Fatalf("write skill %s: %v", skillName, err)
		}
		if err := os.WriteFile(filepath.Join(skillDir, "tools", "echo.yaml"), []byte("name: shared_echo\ncommand: [\"echo\", \""+skillName+"\"]\ninput_schema:\n  type: object\n  properties: {}\n"), 0o644); err != nil {
			t.Fatalf("write tool %s: %v", skillName, err)
		}
	}
	catalog, err := skills.Scan([]string{filepath.Join(root, "skills")})
	if err != nil {
		t.Fatalf("scan skills: %v", err)
	}
	_, err = NewRegistry(cfg, catalog, nil, nil, filepath.Join(root, "workspace"))
	if err == nil || !strings.Contains(err.Error(), "duplicate skill tool name: shared_echo") {
		t.Fatalf("expected duplicate skill tool name rejection, got %v", err)
	}
}

func TestSkillCommandToolRejectsInvalidToolNames(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		want     string
	}{
		{name: "blank", toolName: "", want: "skill tool name must not be empty"},
		{name: "surrounding whitespace", toolName: " shared_echo ", want: "skill tool name must not contain surrounding whitespace"},
		{name: "space", toolName: "shared echo", want: "skill tool name must match provider-compatible pattern"},
		{name: "dot", toolName: "shared.echo", want: "skill tool name must match provider-compatible pattern"},
		{name: "unicode", toolName: "工具", want: "skill tool name must match provider-compatible pattern"},
		{name: "starts with digit", toolName: "1shared_echo", want: "skill tool name must match provider-compatible pattern"},
		{name: "too long", toolName: strings.Repeat("a", 65), want: "skill tool name must match provider-compatible pattern"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			root := t.TempDir()
			skillDir := filepath.Join(root, "skills", "helpers")
			if err := os.MkdirAll(filepath.Join(skillDir, "tools"), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: helpers\ndescription: helper skill\n---\nbody\n"), 0o644); err != nil {
				t.Fatalf("write skill: %v", err)
			}
			if err := os.WriteFile(filepath.Join(skillDir, "tools", "echo.yaml"), []byte("name: \""+tt.toolName+"\"\ncommand: [\"echo\", \"ok\"]\ninput_schema:\n  type: object\n  properties: {}\n"), 0o644); err != nil {
				t.Fatalf("write tool: %v", err)
			}
			catalog, err := skills.Scan([]string{filepath.Join(root, "skills")})
			if err != nil {
				t.Fatalf("scan skills: %v", err)
			}
			_, err = NewRegistry(cfg, catalog, nil, nil, filepath.Join(root, "workspace"))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q, got %v", tt.want, err)
			}
		})
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

func TestSkillCommandToolKeepsResolvedWorkdirWhenPathReplacedBeforeStart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("stable command workdir uses /proc/self/fd on linux")
	}
	cfg := config.Default()
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "helpers")
	renamedSkillDir := filepath.Join(root, "skills", "helpers-renamed")
	outsideDir := filepath.Join(root, "outside")
	if err := os.MkdirAll(filepath.Join(skillDir, "tools"), 0o755); err != nil {
		t.Fatalf("mkdir tools: %v", err)
	}
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: helpers\ndescription: helper skill\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "tools", "marker.yaml"), []byte("name: skill_marker\ndescription: Write marker\ncommand: [\"bash\", \"-lc\", \"printf stable > marker.txt\"]\ninput_schema:\n  type: object\n  properties: {}\n"), 0o644); err != nil {
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

	hookCalled := false
	restoreHook := beforeShellCommandStart
	beforeShellCommandStart = func(commandWorkdir string) error {
		if commandWorkdir != skillDir {
			return nil
		}
		hookCalled = true
		if err := os.Rename(skillDir, renamedSkillDir); err != nil {
			return err
		}
		return os.Symlink(outsideDir, skillDir)
	}
	defer func() {
		beforeShellCommandStart = restoreHook
	}()

	result, err := registry.Execute(context.Background(), "skill_marker", ExecContext{
		Workdir: root,
		Config:  cfg,
	}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !hookCalled {
		t.Fatal("expected command-start hook to replace the skill directory")
	}
	if result.IsError {
		t.Fatalf("expected skill command to run in originally resolved workdir, got %#v", result)
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "marker.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("skill command followed replaced workdir symlink outside skill root, stat err=%v", err)
	}
	data, err := os.ReadFile(filepath.Join(renamedSkillDir, "marker.txt"))
	if err != nil {
		t.Fatalf("expected marker in originally resolved skill dir: %v", err)
	}
	if strings.TrimSpace(string(data)) != "stable" {
		t.Fatalf("unexpected marker content: %q", data)
	}
}

func TestSkillCommandToolRejectsReplacedSymlinkWorkdirBeforeOpen(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "helpers")
	renamedSkillDir := filepath.Join(root, "skills", "helpers-renamed")
	outsideDir := filepath.Join(root, "outside")
	if err := os.MkdirAll(filepath.Join(skillDir, "tools"), 0o755); err != nil {
		t.Fatalf("mkdir tools: %v", err)
	}
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: helpers\ndescription: helper skill\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "tools", "marker.yaml"), []byte("name: skill_marker\ndescription: Write marker\ncommand: [\"bash\", \"-lc\", \"printf escaped > marker.txt\"]\ninput_schema:\n  type: object\n  properties: {}\n"), 0o644); err != nil {
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
	if err := os.Rename(skillDir, renamedSkillDir); err != nil {
		t.Fatalf("rename skill dir: %v", err)
	}
	if err := os.Symlink(outsideDir, skillDir); err != nil {
		t.Fatalf("symlink replaced skill dir: %v", err)
	}

	result, err := registry.Execute(context.Background(), "skill_marker", ExecContext{
		Workdir: root,
		Config:  cfg,
	}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected replaced symlink skill dir to fail closed, got %#v", result)
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "marker.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("skill command ran through replaced symlink workdir, stat err=%v", err)
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
	if err := os.WriteFile(filepath.Join(skillDir, "tools", "echo.yaml"), []byte("name: skill_echo\ndescription: Echo skill name\ncommand: [\"bash\", \"-lc\", \"printf $AEGIS_AGENT_SKILL_NAME\"]\ninput_schema:\n  type: object\n  properties: {}\n"), 0o644); err != nil {
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

func TestSkillCommandToolUsesRuntimeDefaultTimeoutWhenUnset(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.CommandTimeoutSec = 1
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "helpers")
	if err := os.MkdirAll(filepath.Join(skillDir, "tools"), 0o755); err != nil {
		t.Fatalf("mkdir tools: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: helpers\ndescription: helper skill\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "tools", "slow.yaml"), []byte("name: slow_skill\ndescription: Sleep too long\ncommand: [\"bash\", \"-lc\", \"sleep 2\"]\ninput_schema:\n  type: object\n  properties: {}\n"), 0o644); err != nil {
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
	if err != nil {
		t.Fatalf("expected nil error for recoverable skill command timeout, got result=%#v err=%v", result, err)
	}
	if !result.IsError || result.Metadata[MetadataFailureClass] != FailureClassTimeout || result.Metadata["timeout"] != 1 {
		t.Fatalf("expected structured command_timeout metadata from runtime default, got %#v", result)
	}
}

func TestSkillCommandToolMetadataUsesExecutionSandboxConfig(t *testing.T) {
	registryCfg := config.Default()
	execCfg := config.Default()
	execCfg.Runtime.ExecPolicy.Mode = "deny"
	execCfg.Runtime.Shell.Sandbox = "firejail"
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "helpers")
	if err := os.MkdirAll(filepath.Join(skillDir, "tools"), 0o755); err != nil {
		t.Fatalf("mkdir tools: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: helpers\ndescription: helper skill\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "tools", "write_env.yaml"), []byte("name: write_env\ndescription: Try to write env\ncommand: [\"bash\", \"-lc\", \"echo token > .env\"]\ninput_schema:\n  type: object\n  properties: {}\n"), 0o644); err != nil {
		t.Fatalf("write tool: %v", err)
	}
	catalog, err := skills.Scan([]string{filepath.Join(root, "skills")})
	if err != nil {
		t.Fatalf("scan skills: %v", err)
	}
	registry, err := NewRegistry(registryCfg, catalog, nil, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}

	result, err := registry.Execute(context.Background(), "write_env", ExecContext{
		Workdir: root,
		Config:  execCfg,
	}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.IsError || !strings.Contains(result.DisplayOutput, "denied by exec policy") {
		t.Fatalf("expected exec policy denial, got %#v", result)
	}
	if result.Metadata["sandbox"] != "unsupported" {
		t.Fatalf("expected sandbox metadata from execution config, got %#v", result.Metadata)
	}
	if _, err := os.Stat(filepath.Join(skillDir, ".env")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("skill command should not have written .env, stat err=%v", err)
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
	if err != nil {
		t.Fatalf("expected nil error for recoverable skill command timeout, got result=%#v err=%v", result, err)
	}
	if !result.IsError {
		t.Fatalf("expected timeout result to be an error, got %#v", result)
	}
	if result.Metadata[MetadataFailureClass] != FailureClassTimeout {
		t.Fatalf("expected command_timeout failure class, got %#v", result.Metadata)
	}
	if result.Metadata["timeout"] != 1 || result.Metadata["exit_code"] == nil || result.Metadata["raw_length"] == nil || result.Metadata["truncated"] == nil {
		t.Fatalf("expected structured timeout metadata, got %#v", result.Metadata)
	}
	for _, want := range []string{"[command_result tool=slow_skill", "exit_code=", "timeout_sec=1", "workdir_source=skill", "truncated=false"} {
		if !strings.Contains(result.LLMOutput, want) {
			t.Fatalf("expected skill command LLM output to include %q, got %q", want, result.LLMOutput)
		}
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

func TestReadFileAllowsRepeatedWindowsWithoutWarning(t *testing.T) {
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
	if _, ok := second.Metadata["repeat_count"]; ok || strings.Contains(second.LLMOutput, "repeat_count=") || strings.Contains(second.LLMOutput, "read_file warning") {
		t.Fatalf("expected repeated read_file to stay unannotated, got metadata=%#v output=%q", second.Metadata, second.LLMOutput)
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
	originalUpdatedAt := "2026-05-28T00:00:00Z"
	if err := store.SaveTodo(meta.ID, []session.TodoItem{{Content: "Do work", Status: "in_progress", Priority: "high", UpdatedAt: originalUpdatedAt}}); err != nil {
		t.Fatalf("seed todo: %v", err)
	}
	second, err := registry.Execute(context.Background(), "todo_write", execCtx, json.RawMessage(`{"todos":[{"content":"Do work","status":"in_progress","priority":"high","updated_at":"2020-01-01T00:00:00Z"}]}`))
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
	if len(todo) != 1 || todo[0].UpdatedAt != originalUpdatedAt {
		t.Fatalf("expected no-op write to preserve original timestamp, got %#v", todo)
	}
}

func TestTodoWriteRejectsInvalidItems(t *testing.T) {
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
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "blank content",
			payload: `{"todos":[{"content":" \n\t ","status":"pending","priority":"high"}]}`,
			want:    "content is required",
		},
		{
			name:    "missing status",
			payload: `{"todos":[{"content":"Do work","priority":"high"}]}`,
			want:    "status is required",
		},
		{
			name:    "invalid priority",
			payload: `{"todos":[{"content":"Do work","status":"pending","priority":"urgent"}]}`,
			want:    `invalid todo priority: urgent`,
		},
		{
			name:    "invalid updated_at",
			payload: `{"todos":[{"content":"Do work","status":"pending","priority":"high","updated_at":"not-a-time"}]}`,
			want:    "updated_at must be RFC3339Nano",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := registry.Execute(context.Background(), "todo_write", execCtx, json.RawMessage(tc.payload))
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if !result.IsError || !strings.Contains(result.DisplayOutput, tc.want) {
				t.Fatalf("expected %q error, got %#v", tc.want, result)
			}
			todo, err := store.LoadTodo(meta.ID)
			if err != nil {
				t.Fatalf("load todo: %v", err)
			}
			if len(todo) != 0 {
				t.Fatalf("invalid todo item was persisted: %#v", todo)
			}
		})
	}
}

func TestTodoWritePreservesExistingItemsAndOnlyAppendsOrAdvances(t *testing.T) {
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
	initial := []session.TodoItem{
		{Content: "Inspect current implementation", Status: "completed", Priority: "high", UpdatedAt: "2026-05-28T00:00:00Z"},
		{Content: "Patch todo semantics", Status: "pending", Priority: "high", UpdatedAt: "2026-05-28T00:01:00Z"},
	}
	if err := store.SaveTodo(meta.ID, initial); err != nil {
		t.Fatalf("save initial todo: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	execCtx := ExecContext{SessionID: meta.ID, Workdir: root, Store: store, Config: cfg}

	rejected := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "delete completed item",
			payload: `{"todos":[{"content":"Patch todo semantics","status":"pending","priority":"high"}]}`,
			want:    "must preserve existing todo items",
		},
		{
			name:    "rewrite existing text",
			payload: `{"todos":[{"content":"Inspect current implementation again","status":"completed","priority":"high"},{"content":"Patch todo semantics","status":"pending","priority":"high"}]}`,
			want:    "cannot reword existing todo 1",
		},
		{
			name:    "change priority",
			payload: `{"todos":[{"content":"Inspect current implementation","status":"completed","priority":"low"},{"content":"Patch todo semantics","status":"pending","priority":"high"}]}`,
			want:    "cannot change existing todo 1 priority",
		},
		{
			name:    "terminal status regression",
			payload: `{"todos":[{"content":"Inspect current implementation","status":"in_progress","priority":"high"},{"content":"Patch todo semantics","status":"pending","priority":"high"}]}`,
			want:    "cannot change existing todo 1 status from completed to in_progress",
		},
		{
			name:    "new completed item",
			payload: `{"todos":[{"content":"Inspect current implementation","status":"completed","priority":"high"},{"content":"Patch todo semantics","status":"pending","priority":"high"},{"content":"Done without work","status":"completed","priority":"medium"}]}`,
			want:    "cannot add new todo 3 directly as completed",
		},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			result, err := registry.Execute(context.Background(), "todo_write", execCtx, json.RawMessage(tc.payload))
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if !result.IsError || !strings.Contains(result.DisplayOutput, tc.want) {
				t.Fatalf("expected %q error, got %#v", tc.want, result)
			}
			todo, err := store.LoadTodo(meta.ID)
			if err != nil {
				t.Fatalf("load todo: %v", err)
			}
			if !normalizedTodosEqual(todo, initial) {
				t.Fatalf("rejected update mutated todo: %#v", todo)
			}
		})
	}

	accepted, err := registry.Execute(context.Background(), "todo_write", execCtx, json.RawMessage(`{"todos":[{"content":"Inspect current implementation","status":"completed","priority":"high"},{"content":"Patch todo semantics","status":"in_progress","priority":"high"},{"content":"Run focused tests","status":"in_progress","priority":"medium"}]}`))
	if err != nil || accepted.IsError {
		t.Fatalf("expected append/progress update to succeed, err=%v result=%#v", err, accepted)
	}
	todo, err := store.LoadTodo(meta.ID)
	if err != nil {
		t.Fatalf("load accepted todo: %v", err)
	}
	if len(todo) != 3 || todo[1].Status != "in_progress" || todo[2].Content != "Run focused tests" || todo[2].Status != "in_progress" {
		t.Fatalf("unexpected accepted todo snapshot: %#v", todo)
	}
	if todo[0].UpdatedAt != initial[0].UpdatedAt {
		t.Fatalf("unchanged item timestamp refreshed: %#v", todo[0])
	}
	if todo[1].UpdatedAt == initial[1].UpdatedAt || todo[1].UpdatedAt == "" {
		t.Fatalf("advanced item timestamp was not refreshed: %#v", todo[1])
	}
	if todo[2].UpdatedAt == "" {
		t.Fatalf("new item timestamp was not assigned: %#v", todo[2])
	}
}

func TestTodoWriteOverridesStaleCallerTimestampOnStatusAdvance(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	store := session.NewStore(filepath.Join(root, "sessions"))
	meta := session.SessionMetadata{
		SchemaVersion: 1, ID: session.NewSessionID(), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Workdir: root, Mode: session.ModeRun, Provider: "fake", Model: "fake", CompletionPolicy: session.CompletionPolicyInteractive,
	}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	stale := "2020-01-01T00:00:00Z"
	if err := store.SaveTodo(meta.ID, []session.TodoItem{{Content: "Advance me", Status: "pending", Priority: "high", UpdatedAt: stale}}); err != nil {
		t.Fatalf("seed todo: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), "todo_write", ExecContext{SessionID: meta.ID, Workdir: root, Store: store, Config: cfg}, json.RawMessage(`{"todos":[{"content":"Advance me","status":"in_progress","priority":"high","updated_at":"2020-01-01T00:00:00Z"}]}`))
	if err != nil || result.IsError {
		t.Fatalf("advance todo err=%v result=%#v", err, result)
	}
	todo, err := store.LoadTodo(meta.ID)
	if err != nil {
		t.Fatalf("load todo: %v", err)
	}
	if len(todo) != 1 || todo[0].UpdatedAt == stale {
		t.Fatalf("stale caller timestamp survived status advance: %#v", todo)
	}
	if _, err := time.Parse(time.RFC3339Nano, todo[0].UpdatedAt); err != nil {
		t.Fatalf("runtime timestamp is invalid: %q: %v", todo[0].UpdatedAt, err)
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
	originalUpdatedAt := "2026-05-28T00:00:00Z"
	initial := []session.TodoItem{{Content: "Keep original", Status: "in_progress", Priority: "high", UpdatedAt: originalUpdatedAt}}
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

	result, err := registry.Execute(context.Background(), "todo_write", execCtx, json.RawMessage(`{"todos":[{"content":"Keep original","status":"completed","priority":"high"}]}`))
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
	if !normalizedTodosEqual(todo, initial) || todo[0].UpdatedAt != originalUpdatedAt {
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
	originalUpdatedAt := "2026-05-28T00:00:00Z"
	initial := []session.TodoItem{{Content: "Do work", Status: "in_progress", Priority: "high", UpdatedAt: originalUpdatedAt}}
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
	if !normalizedTodosEqual(todo, initial) || todo[0].UpdatedAt != originalUpdatedAt {
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

func TestRenderCommandOmitsPurelyConditionalArgvSlots(t *testing.T) {
	tests := []struct {
		name    string
		command []string
		args    map[string]any
		want    []string
	}{
		{
			name:    "assignment action before conditional does not pin the slot",
			command: []string{"tool", "{{$v := .verbose}}{{if $v}}--verbose{{end}}", "target.txt"},
			args:    map[string]any{"verbose": false},
			want:    []string{"tool", "target.txt"},
		},
		{
			name:    "assignment action with explicit empty string omits the slot",
			command: []string{"tool", "{{$limit := .limit}}{{if $limit}}--limit={{$limit}}{{end}}", "target.txt"},
			args:    map[string]any{"limit": ""},
			want:    []string{"tool", "target.txt"},
		},
		{
			name:    "assignment action feeding an empty range omits the slot",
			command: []string{"tool", "{{$items := .items}}{{range $items}}{{.}}{{end}}", "target.txt"},
			args:    map[string]any{"items": []any{}},
			want:    []string{"tool", "target.txt"},
		},
		{
			name:    "reassignment action does not pin the slot",
			command: []string{"tool", "{{$v := true}}{{$v = .verbose}}{{if $v}}--verbose{{end}}", "target.txt"},
			args:    map[string]any{"verbose": false},
			want:    []string{"tool", "target.txt"},
		},
		{
			name:    "variable emitted only inside a conditional does not pin the slot",
			command: []string{"tool", "{{$name := .name}}{{if .verbose}}{{$name}}{{end}}", "target.txt"},
			args:    map[string]any{"name": "", "verbose": false},
			want:    []string{"tool", "target.txt"},
		},
		{
			name:    "variable emitted outside every conditional keeps the slot",
			command: []string{"tool", "{{$name := .name}}{{$name}}", "target.txt"},
			args:    map[string]any{"name": ""},
			want:    []string{"tool", "", "target.txt"},
		},
		{
			name:    "purely conditional fragment omits the slot",
			command: []string{"tool", "{{if .verbose}}--verbose{{end}}", "target.txt"},
			args:    map[string]any{"verbose": false},
			want:    []string{"tool", "target.txt"},
		},
		{
			name:    "conditional mixed with a plain slot keeps the slot",
			command: []string{"tool", "{{if .verbose}}--verbose{{end}}{{.name}}", "target.txt"},
			args:    map[string]any{"verbose": false, "name": ""},
			want:    []string{"tool", "", "target.txt"},
		},
		{
			name:    "explicit empty plain slot keeps its position",
			command: []string{"tool", "{{.name}}", "target.txt"},
			args:    map[string]any{"name": ""},
			want:    []string{"tool", "", "target.txt"},
		},
		{
			name:    "omitted field drops the slot",
			command: []string{"tool", "{{.name}}", "target.txt"},
			args:    map[string]any{},
			want:    []string{"tool", "target.txt"},
		},
		{
			name:    "explicit zero renders its value",
			command: []string{"tool", "{{.count}}", "target.txt"},
			args:    map[string]any{"count": 0},
			want:    []string{"tool", "0", "target.txt"},
		},
		{
			name:    "conditional that holds renders its branch",
			command: []string{"tool", "{{if .verbose}}--verbose{{end}}", "target.txt"},
			args:    map[string]any{"verbose": true},
			want:    []string{"tool", "--verbose", "target.txt"},
		},
		{
			name:    "empty range omits the slot",
			command: []string{"tool", "{{range .items}}{{.}}{{end}}", "target.txt"},
			args:    map[string]any{"items": []any{}},
			want:    []string{"tool", "target.txt"},
		},
		{
			name:    "nil with omits the slot",
			command: []string{"tool", "{{with .opt}}--opt={{.}}{{end}}", "target.txt"},
			args:    map[string]any{"opt": nil},
			want:    []string{"tool", "target.txt"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := renderCommand(test.command, test.args)
			if err != nil {
				t.Fatalf("renderCommand: %v", err)
			}
			if fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", test.want) {
				t.Fatalf("renderCommand argv=%#v, want %#v", got, test.want)
			}
		})
	}
}
