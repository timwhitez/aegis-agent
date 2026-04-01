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

func TestAgentToolsAreHiddenUnlessMultiAgentEnabled(t *testing.T) {
	cfg := config.Default()
	store := session.NewStore(t.TempDir())

	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	for _, name := range []string{"agent_spawn", "agent_status", "agent_list"} {
		if _, ok := registry.defs[name]; ok {
			t.Fatalf("expected %s to be hidden by default", name)
		}
	}

	cfg.Runtime.MultiAgent.Enabled = true
	registry, err = NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry with multi-agent enabled: %v", err)
	}
	for _, name := range []string{"agent_spawn", "agent_status", "agent_list"} {
		if _, ok := registry.defs[name]; !ok {
			t.Fatalf("expected %s to be registered when multi-agent is enabled", name)
		}
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
