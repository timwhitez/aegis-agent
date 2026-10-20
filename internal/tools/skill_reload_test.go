package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aegis-agent/internal/config"
	"aegis-agent/internal/session"
	"aegis-agent/internal/skills"
)

func TestLoadSkillFailedRefreshRetainsLoadedStateAndBody(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	path := filepath.Join(root, "skills", "helpers", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	manifest := "---\nname: helpers\ndescription: stable\n---\n"
	if err := os.WriteFile(path, []byte(manifest+"V1"), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := skills.Scan([]string{filepath.Join(root, "skills")})
	if err != nil {
		t.Fatal(err)
	}
	store := session.NewStore(filepath.Join(root, "sessions"))
	id := session.NewSessionID()
	meta := session.SessionMetadata{SchemaVersion: 1, ID: id, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: root, Mode: session.ModeRun, Provider: "fake", Model: "fake", CompletionPolicy: session.CompletionPolicyInteractive}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistryForToolProfile(cfg, catalog, store, nil, session.ToolProfileExplorerReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	execCtx := ExecContext{SessionID: id, Workdir: root, Store: store, Config: cfg, Catalog: catalog, ToolProfile: session.ToolProfileExplorerReadOnly}
	execute := func(force bool) session.ToolResult {
		t.Helper()
		result, err := registry.Execute(context.Background(), "load_skill", execCtx, json.RawMessage(fmt.Sprintf(`{"name":"helpers","force_reload":%t}`, force)))
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if first := execute(false); first.IsError || !strings.Contains(first.LLMOutput, "V1") {
		t.Fatalf("initial load: %+v", first)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	failed := execute(true)
	if !failed.IsError || !strings.Contains(failed.LLMOutput, "cached_body_retained=true") || strings.Contains(failed.LLMOutput, "V1") {
		t.Fatalf("refresh failed but returned old success: %+v", failed)
	}
	if body, err := catalog.LoadBody("helpers"); err != nil || body != "V1" {
		t.Fatalf("old body lost: %q %v", body, err)
	}
	if state, err := store.LoadState(id); err != nil || fmt.Sprint(state.LoadedSkills) != "[helpers]" {
		t.Fatalf("failure changed loaded state: %+v %v", state.LoadedSkills, err)
	}
	if result := execute(false); result.IsError || result.Metadata["already_loaded"] != true {
		t.Fatalf("ordinary duplicate no longer compact: %+v", result)
	}
	if err := os.WriteFile(path, []byte(manifest+"V2"), 0600); err != nil {
		t.Fatal(err)
	}
	fresh := execute(true)
	if fresh.IsError || !strings.Contains(fresh.LLMOutput, "V2") || fresh.Metadata["tool_profile"] != session.ToolProfileExplorerReadOnly || strings.Contains(fresh.LLMOutput, "shell_workdir=") {
		t.Fatalf("read-only refresh failed/profile expanded: %+v", fresh)
	}
	if state, err := store.LoadState(id); err != nil || fmt.Sprint(state.LoadedSkills) != "[helpers]" {
		t.Fatalf("refresh duplicated loaded state: %+v %v", state.LoadedSkills, err)
	}
}

func TestLoadSkillRefreshedBodyReportsSessionStateSaveFailure(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	dir := filepath.Join(root, "skills", "helpers")
	path := filepath.Join(dir, "SKILL.md")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := "---\nname: helpers\ndescription: stable\n---\n"
	if err := os.WriteFile(path, []byte(manifest+"V1"), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := skills.Scan([]string{filepath.Join(root, "skills")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.LoadBody("helpers"); err != nil {
		t.Fatal(err)
	}
	store := session.NewStore(filepath.Join(root, "sessions"))
	id := session.NewSessionID()
	meta := session.SessionMetadata{SchemaVersion: 1, ID: id, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: root, Mode: session.ModeRun, Provider: "fake", Model: "fake", CompletionPolicy: session.CompletionPolicyInteractive}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	// Use the existing real Store save-failure contract: pending-steer reader
	// fails before state publication, after the Catalog has refreshed its body.
	steer := filepath.Join(store.SessionDir(id), "control", "steer.jsonl")
	if err := os.Remove(steer); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(steer, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(manifest+"V2"), 0600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(cfg, catalog, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := registry.Execute(context.Background(), "load_skill", ExecContext{SessionID: id, Workdir: root, Store: store, Config: cfg, Catalog: catalog}, json.RawMessage(`{"name":"helpers","force_reload":true}`))
	if err != nil || !result.IsError || result.Metadata["body_reloaded"] != true || result.Metadata["session_state_saved"] != false || !strings.Contains(result.LLMOutput, "session loaded state save failed") || strings.Contains(result.LLMOutput, "V2") {
		t.Fatalf("partial publication not reported: %+v %v", result, err)
	}
	if body, err := catalog.LoadBody("helpers"); err != nil || body != "V2" {
		t.Fatalf("successful refresh was misreported/rolled back: %q %v", body, err)
	}
	if state, err := store.LoadState(id); err != nil || len(state.LoadedSkills) != 0 {
		t.Fatalf("failed save falsely published loaded state: %+v %v", state.LoadedSkills, err)
	}
}
