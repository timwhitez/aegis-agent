package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"aegis-agent/internal/config"
	"aegis-agent/internal/session"
	"aegis-agent/internal/skills"
)

func TestExplorerToolProfileUsesOneExactSchemaAndExecutionAllowlist(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	skillDir := filepath.Join(root, "skills", "helpers")
	if err := os.MkdirAll(filepath.Join(skillDir, "tools"), 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: helpers\ndescription: helper skill\n---\nRead-only guidance.\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "tools", "touch.yaml"), []byte("name: touch_forbidden\ndescription: Must stay unavailable to explorer.\ncommand: [\"/bin/sh\", \"-lc\", \"touch explorer-command-ran\"]\ninput_schema:\n  type: object\n  properties: {}\n"), 0o644); err != nil {
		t.Fatalf("write skill tool: %v", err)
	}
	catalog, err := skills.Scan([]string{filepath.Join(root, "skills")})
	if err != nil {
		t.Fatalf("scan skills: %v", err)
	}

	cfg := config.Default()
	cfg.Runtime.MultiAgent.Enabled = true
	store := session.NewStore(filepath.Join(root, "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        now,
		Workdir:          workdir,
		Mode:             session.ModeExec,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyAutonomous,
		AgentRole:        "explorer",
		ToolProfile:      session.ToolProfileExplorerReadOnly,
	}
	state := session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: now}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create explorer session: %v", err)
	}

	registry, err := NewRegistryForToolProfile(cfg, catalog, store, nil, session.ToolProfileExplorerReadOnly, workdir)
	if err != nil {
		t.Fatalf("new explorer registry: %v", err)
	}
	gotNames := definitionNames(registry.Definitions())
	wantNames := []string{"finish", "glob", "grep", "grep_files", "load_skill", "read_file"}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("explorer provider schema names=%v want=%v", gotNames, wantNames)
	}

	defaultRegistry, err := NewRegistry(cfg, catalog, store, nil, workdir)
	if err != nil {
		t.Fatalf("new default registry: %v", err)
	}
	allowed := make(map[string]struct{}, len(wantNames))
	for _, name := range wantNames {
		allowed[name] = struct{}{}
	}
	execCtx := ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg, Catalog: catalog}
	loadedSkill, err := registry.Execute(context.Background(), "load_skill", execCtx, json.RawMessage(`{"name":"helpers"}`))
	if err != nil || loadedSkill.IsError {
		t.Fatalf("load explorer skill guidance: result=%#v err=%v", loadedSkill, err)
	}
	if strings.Contains(loadedSkill.LLMOutput, "shell tool") || strings.Contains(loadedSkill.LLMOutput, "shell_workdir") || loadedSkill.Metadata["shell_workdir"] != nil || loadedSkill.Metadata["tool_profile"] != session.ToolProfileExplorerReadOnly {
		t.Fatalf("explorer skill load advertised command capability: %#v", loadedSkill)
	}
	beforeState, err := store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state before denials: %v", err)
	}
	for _, def := range defaultRegistry.Definitions() {
		if _, ok := allowed[def.Name]; ok {
			continue
		}
		result, executeErr := registry.Execute(context.Background(), def.Name, execCtx, json.RawMessage(`{}`))
		if executeErr != nil {
			t.Fatalf("execute denied tool %s: %v", def.Name, executeErr)
		}
		if !result.IsError || result.Metadata[MetadataFailureClass] != FailureClassSchemaReject || result.Metadata["error_code"] != ErrorCodeToolNotAllowedForRole || result.Metadata["tool_profile"] != session.ToolProfileExplorerReadOnly {
			t.Fatalf("tool %s did not return stable capability denial: %#v", def.Name, result)
		}
	}
	afterState, err := store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state after denials: %v", err)
	}
	if !reflect.DeepEqual(beforeState, afterState) {
		t.Fatalf("denied explorer tools mutated session state: before=%#v after=%#v", beforeState, afterState)
	}
	for _, forbiddenPath := range []string{"explorer-command-ran", "forbidden-write.txt"} {
		if _, err := os.Stat(filepath.Join(workdir, forbiddenPath)); !os.IsNotExist(err) {
			t.Fatalf("denied explorer tool created %s: %v", forbiddenPath, err)
		}
	}
}

func definitionNames(defs []Definition) []string {
	names := make([]string, 0, len(defs))
	for _, def := range defs {
		names = append(names, def.Name)
	}
	sort.Strings(names)
	return names
}

func TestAgentSpawnSchemaDescribesExplorerAsModelLedContextIsolation(t *testing.T) {
	def := defAgentSpawn(nil)
	properties, _ := def.InputSchema["properties"].(map[string]any)
	roleSchema, _ := properties["agent_role"].(map[string]any)
	enum, _ := roleSchema["enum"].([]string)
	if !containsString(enum, "explorer") {
		t.Fatalf("agent_spawn agent_role enum does not include explorer: %#v", roleSchema)
	}
	description := def.Description + " " + metadataString(roleSchema, "description")
	for _, want := range []string{"model-led", "raw search", "context isolation", "agent_wait"} {
		if !strings.Contains(strings.ToLower(description), want) {
			t.Fatalf("agent_spawn explorer guidance missing %q: %s", want, description)
		}
	}
	for _, forbidden := range []string{"must delegate", "always spawn", "runtime automatically"} {
		if strings.Contains(strings.ToLower(description), forbidden) {
			t.Fatalf("agent_spawn description introduced mandatory orchestration %q: %s", forbidden, description)
		}
	}
}
