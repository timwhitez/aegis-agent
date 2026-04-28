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

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/session"
	"go-cli-agent/internal/skills"
)

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
	listResult, err := registry.Execute(context.Background(), "task_list", execCtx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("task_list: %v", err)
	}
	if listResult.Metadata["tasks_dir"] != filepath.Join(store.SessionDir(meta.ID), "tasks") {
		t.Fatalf("expected task_list tasks_dir metadata, got %#v", listResult.Metadata)
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
		"agent_role": "evaluator fits review",
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
	registry, err := NewRegistry(cfg, catalog, nil, nil)
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
	} {
		if !strings.Contains(result.LLMOutput, needle) {
			t.Fatalf("expected load_skill output to contain %q, got %q", needle, result.LLMOutput)
		}
	}
}
