package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ngen/internal/artifact"
	"ngen/internal/multica"
	"ngen/internal/task"
)

func TestCommandProviderCLIHelperProcess(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "app-command-provider-helper" {
		return
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read stdin: %v", err)
		os.Exit(2)
	}
	raw := string(data)
	switch os.Getenv("NGEN_PROVIDER_OPERATION") {
	case "decision":
		if !strings.Contains(raw, `"kind":"coding"`) {
			fmt.Fprint(os.Stderr, "missing coding task input")
			os.Exit(3)
		}
		if capturePath := os.Getenv("NGEN_CAPTURE_DECISION_INPUT"); capturePath != "" {
			if err := os.WriteFile(capturePath, data, 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "write decision capture: %v", err)
				os.Exit(7)
			}
		}
		fmt.Fprint(os.Stdout, `{"action":"run","summary":"Run coding task now","watch_interval":"","watch_reason":"","approval_scope":"","approval_reason":""}`)
	case "workspace_observation":
		if capturePath := os.Getenv("NGEN_CAPTURE_WORKSPACE_OBSERVATION_INPUT"); capturePath != "" {
			if err := os.WriteFile(capturePath, data, 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "write observation capture: %v", err)
				os.Exit(8)
			}
		}
		fmt.Fprint(os.Stdout, `{"summary":"Inspect the broken function","commands":[{"argv":["sed","-n","1,40p","calc.go"],"reason":"Read the current Add implementation"}]}`)
	case "workspace_edit":
		if capturePath := os.Getenv("NGEN_CAPTURE_WORKSPACE_EDIT_INPUT"); capturePath != "" {
			if err := os.WriteFile(capturePath, data, 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "write edit capture: %v", err)
				os.Exit(9)
			}
		}
		fmt.Fprint(os.Stdout, `{"summary":"Patch Add in place","patch":"*** Begin Patch\n*** Update File: calc.go\n@@\n-func Add(a, b int) int { return a - b }\n+func Add(a, b int) int { return a + b }\n*** End Patch","writes":[],"deletes":[],"commands":[]}`)
	default:
		fmt.Fprintf(os.Stderr, "unexpected operation: %s", os.Getenv("NGEN_PROVIDER_OPERATION"))
		os.Exit(4)
	}
	os.Exit(0)
}

func helperAppCommandProviderCommand(t *testing.T) []string {
	t.Helper()
	return []string{os.Args[0], "-test.run=TestCommandProviderCLIHelperProcess", "--", "app-command-provider-helper"}
}

func TestRootHelpExitsZeroBeforeLoadingWorkspaceConfig(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ngen.json"), "{")
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			result := runCLI(t, dir, args)
			if result.exitCode != 0 {
				t.Fatalf("expected help exit 0, got %d stderr=%s stdout=%s", result.exitCode, result.stderr, result.stdout)
			}
			if !strings.Contains(result.stdout, "usage:") || !strings.Contains(result.stdout, "ngen tui") {
				t.Fatalf("expected root usage on stdout, got %q", result.stdout)
			}
			if result.stderr != "" {
				t.Fatalf("expected empty stderr, got %q", result.stderr)
			}
		})
	}
}

func TestVersionAndModelsBypassUnrelatedMalformedWorkspaceConfig(t *testing.T) {
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, "ngen.json"), "{")
	target := t.TempDir()
	writeFile(t, filepath.Join(target, "ngen.json"), `{
  "provider": {
    "mode": "openai-response",
    "model": "gpt-5.5",
    "thinking_level": "xhigh"
  }
}`)

	versionText := runCLI(t, cwd, []string{"--version"})
	if versionText.exitCode != 0 || versionText.stderr != "" || !strings.Contains(versionText.stdout, "ngen 0.1.0") {
		t.Fatalf("expected --version to bypass malformed config, got code=%d stdout=%q stderr=%q", versionText.exitCode, versionText.stdout, versionText.stderr)
	}
	versionJSON := runCLI(t, cwd, []string{"version", "--json"})
	if versionJSON.exitCode != 0 {
		t.Fatalf("expected version --json success, got %d stderr=%s", versionJSON.exitCode, versionJSON.stderr)
	}
	var info struct {
		Name            string `json:"name"`
		Version         string `json:"version"`
		Protocol        string `json:"protocol"`
		ProtocolVersion int    `json:"protocol_version"`
	}
	if err := json.Unmarshal([]byte(versionJSON.stdout), &info); err != nil {
		t.Fatalf("unmarshal version: %v", err)
	}
	if info.Name != "ngen" || info.Protocol != multica.ProtocolName || info.ProtocolVersion != multica.ProtocolVersion {
		t.Fatalf("unexpected version info: %+v", info)
	}

	modelsResult := runCLI(t, cwd, []string{"models", "--json", "--workdir", target})
	if modelsResult.exitCode != 0 {
		t.Fatalf("expected models --json to use target workdir, got %d stderr=%s", modelsResult.exitCode, modelsResult.stderr)
	}
	var models []multica.ModelInfo
	if err := json.Unmarshal([]byte(modelsResult.stdout), &models); err != nil {
		t.Fatalf("unmarshal models: %v", err)
	}
	if len(models) != 1 || models[0].ID != "openai-response/gpt-5.5" || models[0].Thinking == nil || models[0].Thinking.ConfiguredLevel != "xhigh" {
		t.Fatalf("unexpected models: %+v", models)
	}
}

func TestMulticaExecCreatesMetadataGuidanceAndFinalResult(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ngen.json"), "{")
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/mexec\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package mexec\n\nfunc Add(a, b int) int { return a + b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package mexec\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")
	writeFile(t, filepath.Join(dir, "AGENTS.md"), "# Workspace Guidance\n\nNGEN must consume this injected brief.")
	writeFile(t, filepath.Join(dir, "skills", "audit", "SKILL.md"), "# Audit Skill\n\nUse audit evidence.")

	envelope := `{"protocol":"ngen-stream-json","protocol_version":1,"type":"user","role":"user","content":[{"type":"text","text":"Implement code verification by running tests"}],"system_prompt":"system brief","metadata":{"issue_id":"LOC-1"}}`
	result := runExecForTest(t, t.TempDir(), []string{
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--config-scope", "daemon",
		"--workdir", dir,
		"--role", "worker",
	}, envelope)
	if result.exitCode != 0 {
		t.Fatalf("expected exec completion, got %d stderr=%s stdout=%s", result.exitCode, result.stderr, result.stdout)
	}
	lines := decodeStreamOutput(t, result.stdout)
	if len(lines) < 3 {
		t.Fatalf("expected system/status/result lines, got %+v", lines)
	}
	if lines[0].Type != "system" || lines[0].Protocol != multica.ProtocolName || lines[0].RunRole != "worker" {
		t.Fatalf("unexpected system line: %+v", lines[0])
	}
	final := lines[len(lines)-1]
	if final.Type != "result" || final.Status != "completed" || final.SessionID == "" || final.SessionID != final.TaskID || final.Result == "" {
		t.Fatalf("expected final completed result with task session id, got %+v", final)
	}
	taskID := final.TaskID
	var metadata task.MulticaRunMetadata
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "multica", "run_metadata.json"))), &metadata); err != nil {
		t.Fatalf("unmarshal run metadata: %v", err)
	}
	if metadata.ModelRoute != "builtin/default" || metadata.PermissionModeID != task.PermissionModeYolo || metadata.SessionID != taskID {
		t.Fatalf("unexpected run metadata: %+v", metadata)
	}
	var guidance task.WorkspaceGuidanceArtifact
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "multica", "workspace_guidance.json"))), &guidance); err != nil {
		t.Fatalf("unmarshal workspace guidance: %v", err)
	}
	if len(guidance.Documents) != 1 || !strings.Contains(guidance.Documents[0].Content, "injected brief") {
		t.Fatalf("expected AGENTS.md guidance to be captured, got %+v", guidance.Documents)
	}
	if len(guidance.Skills) != 1 || guidance.Skills[0].Name != "audit" || !strings.Contains(guidance.Skills[0].Content, "audit evidence") {
		t.Fatalf("expected workspace skill to be captured, got %+v", guidance.Skills)
	}
}

func TestMulticaExecMetadataAndSystemPromptArePassThrough(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "multica-calls.log")
	writeFile(t, filepath.Join(dir, "multica"), `#!/bin/sh
printf '%s\n' "$*" >> multica-calls.log
echo "adapter should not call multica directly" >&2
exit 9
`)
	if err := os.Chmod(filepath.Join(dir, "multica"), 0o755); err != nil {
		t.Fatalf("chmod multica stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeFile(t, filepath.Join(dir, "README.md"), "# Multica pass-through fixture\n")
	writeFile(t, filepath.Join(dir, "ngen.json"), `{
  "permission": {"default_mode": "yolo"},
  "provider": {
    "mode": "builtin",
    "coding_repair_budget": 1,
    "coding_execution_command_budget": 1
  }
}`)

	envelope := `{
  "protocol":"ngen-stream-json",
  "protocol_version":1,
  "type":"user",
  "role":"user",
  "content":[{"type":"text","text":"你是一名资深全栈安全工程师、架构师和测试负责人。请分析研究设计并逐步开发一个 Web First 的智能渗透测试系统。\n\n密钥：OPENAI_API_KEY:\"sk-test-redaction-00000000000000000000000000000000\""}],
  "system_prompt":"You are running as a quick-create assistant for a Multica workspace. This task was triggered by quick-create. There is NO existing Multica issue.",
  "metadata":{
    "type":"quick_create",
    "run_role":"assignee",
    "squad_id":"3b0ca27f-5db0-42ff-98ea-7750fc40500a",
    "project_id":"ae886a17-0ef6-4b02-b154-3ac601df7239",
    "delegation_boundary":"quick-create-issue",
    "validation_contract_ref":"quick-create.issue_create",
    "expected_public_artifacts":"created_issue"
  }
}`
	result := runExecForTest(t, t.TempDir(), []string{
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--config-scope", "daemon",
		"--workdir", dir,
		"--role", "leader",
	}, envelope)
	if result.exitCode != 0 {
		t.Fatalf("expected pass-through completion, got %d stderr=%s stdout=%s", result.exitCode, result.stderr, result.stdout)
	}
	lines := decodeStreamOutput(t, result.stdout)
	final := lines[len(lines)-1]
	if final.Type != "result" || final.Status != "completed" || final.Result == "" {
		t.Fatalf("expected completed pass-through result, got %+v", final)
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Fatalf("expected no adapter-issued multica calls, got:\n%s", readFile(t, logPath))
	}
}

func TestMulticaExecExplicitCommandRunsThroughGenericCommandLane(t *testing.T) {
	dir := t.TempDir()
	binDir := t.TempDir()
	logPath := filepath.Join(dir, "externalctl.log")
	writeFile(t, filepath.Join(binDir, "externalctl"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \""+logPath+"\"\nprintf '{\"status\":\"created\"}\\n'\n")
	if err := os.Chmod(filepath.Join(binDir, "externalctl"), 0o755); err != nil {
		t.Fatalf("chmod externalctl stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	envelope := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Run exactly one ` + "`externalctl create --name Smoke --output json`" + ` invocation."}]}}`
	result := runExecForTest(t, t.TempDir(), []string{
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--config-scope", "daemon",
		"--workdir", dir,
	}, envelope)
	if result.exitCode != 0 {
		t.Fatalf("expected explicit command exec completion, got %d stderr=%s stdout=%s", result.exitCode, result.stderr, result.stdout)
	}
	lines := decodeStreamOutput(t, result.stdout)
	final := lines[len(lines)-1]
	if final.Type != "result" || final.Status != "completed" {
		t.Fatalf("expected completed explicit command result, got %+v", final)
	}
	if log := readFile(t, logPath); strings.Count(log, "create --name Smoke --output json") != 1 {
		t.Fatalf("expected one generic external command invocation, got %q", log)
	}
	records, err := artifact.NewStore(dir, ".ngen").ReadCommandRuns(final.TaskID)
	if err != nil {
		t.Fatalf("read command runs: %v", err)
	}
	if len(records) != 1 || records[0].PolicyDecision != "allow_yolo" || records[0].ReplaySafety == nil || !records[0].ReplaySafety.OpenWorld {
		t.Fatalf("expected generic external command policy evidence, got %+v", records)
	}
}

func TestMulticaExecResumeBlocksOnMissingMetadataAndConfigDrift(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/drift\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "demo.go"), "package drift\n\nfunc OK() bool { return true }\n")

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "general_execution", "--preset", "docs_lite", "--title", "manual", "--objective", "manual", "--criterion", "manual"}).stdout)
	missing := runExecForTest(t, t.TempDir(), []string{
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--config-scope", "daemon",
		"--workdir", dir,
		"--resume", taskID,
	}, `{"protocol":"ngen-stream-json","protocol_version":1,"type":"user","role":"user","content":[{"type":"text","text":"resume"}]}`)
	if missing.exitCode != 10 {
		t.Fatalf("expected missing metadata blocked exit, got %d stdout=%s stderr=%s", missing.exitCode, missing.stdout, missing.stderr)
	}
	missingLines := decodeStreamOutput(t, missing.stdout)
	if got := missingLines[len(missingLines)-1].Metadata["status_reason_code"]; got != "multica_run_metadata_missing" {
		t.Fatalf("expected metadata missing reason, got %#v", got)
	}

	envelope := `{"protocol":"ngen-stream-json","protocol_version":1,"type":"user","role":"user","content":[{"type":"text","text":"Implement code verification by running tests"}]}`
	first := runExecForTest(t, t.TempDir(), []string{
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--config-scope", "daemon",
		"--workdir", dir,
	}, envelope)
	if first.exitCode != 0 {
		t.Fatalf("expected initial exec completion, got %d stderr=%s stdout=%s", first.exitCode, first.stderr, first.stdout)
	}
	created := decodeStreamOutput(t, first.stdout)
	createdTaskID := created[len(created)-1].TaskID
	configPath := filepath.Join(t.TempDir(), "ngen.json")
	writeFile(t, configPath, `{"provider":{"mode":"command","command":["printf","{}"]}}`)
	drift := runExecForTest(t, t.TempDir(), []string{
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--config-scope", "daemon",
		"--config", configPath,
		"--workdir", dir,
		"--resume", createdTaskID,
	}, envelope)
	if drift.exitCode != 10 {
		t.Fatalf("expected drift blocked exit, got %d stdout=%s stderr=%s", drift.exitCode, drift.stdout, drift.stderr)
	}
	driftLines := decodeStreamOutput(t, drift.stdout)
	driftFinal := driftLines[len(driftLines)-1]
	if got := driftFinal.Metadata["status_reason_code"]; got != "multica_model_config_drift" {
		t.Fatalf("expected config drift reason, got %#v", got)
	}
	if driftFinal.ModelRoute != "builtin/default" || driftFinal.Handoff == nil || driftFinal.Handoff.ModelRoute != "builtin/default" {
		t.Fatalf("expected drift result to preserve original run metadata model route, got %+v", driftFinal)
	}
	if got := driftFinal.Metadata["current_model_route"]; got != "command/default" {
		t.Fatalf("expected current drift metadata, got %#v", got)
	}
}

func TestMulticaExecProviderFailurePersistsFailedRuntimeState(t *testing.T) {
	dir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected provider path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"simulated provider outage"}}`))
	}))
	defer server.Close()
	writeFile(t, filepath.Join(dir, "ngen.json"), fmt.Sprintf(`{
  "permission": {"default_mode": "yolo"},
  "provider": {
    "mode": "openai-response",
    "base_url": %q,
    "model": "gpt-fail",
    "api_key_env": "OPENAI_API_KEY",
    "decision_timeout_seconds": 5
  }
}`, server.URL+"/v1"))
	t.Setenv("OPENAI_API_KEY", "test-key")

	envelope := `{"protocol":"ngen-stream-json","protocol_version":1,"type":"user","role":"user","content":[{"type":"text","text":"Publish reports/mission-plan.md and progress/mission-status.md for this handoff."}]}`
	result := runExecForTest(t, t.TempDir(), []string{
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--workdir", dir,
		"--role", "leader",
	}, envelope)
	if result.exitCode != 11 {
		t.Fatalf("expected provider failure exit 11, got %d stderr=%s stdout=%s", result.exitCode, result.stderr, result.stdout)
	}
	lines := decodeStreamOutput(t, result.stdout)
	final := lines[len(lines)-1]
	if final.Type != "result" || final.Status != "failed" || !final.IsError {
		t.Fatalf("expected failed final result, got %+v", final)
	}
	if strings.Contains(final.Result, "NGEN task finished with state Active") || !strings.Contains(final.Result, "simulated provider outage") {
		t.Fatalf("expected provider error result without Active handoff summary, got %q", final.Result)
	}
	if final.Handoff == nil || final.Handoff.State != string(task.StateFailed) || final.Handoff.StatusReasonCode != "failed_runtime" {
		t.Fatalf("expected failed_runtime handoff, got %+v", final.Handoff)
	}

	var state task.State
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, ".ngen", "tasks", final.TaskID, "state.json"))), &state); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	if state.State != task.StateFailed || state.StatusReasonCode != "failed_runtime" || !strings.HasPrefix(state.StatusDetailRef, "diagnostics/DIAG-") {
		t.Fatalf("expected persisted failed runtime state, got %+v", state)
	}
	handoff := readFile(t, filepath.Join(dir, ".ngen", "tasks", final.TaskID, "handoff.md"))
	if !strings.Contains(handoff, "simulated provider outage") || strings.Contains(handoff, "State: Active") {
		t.Fatalf("expected failure handoff to record provider outage without active state, got:\n%s", handoff)
	}
	assertFileExists(t, filepath.Join(dir, ".ngen", "tasks", final.TaskID, "harness", "latest.json"))
}

func TestCodingTaskCreateRunAndStatus(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "demo.go"), "package demo\n\nfunc Add(a, b int) int { return a + b }\n")

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "coding", "--title", "demo", "--objective", "verify coding", "--criterion", "go test passes"}).stdout)
	if taskID == "" {
		t.Fatal("expected task id")
	}
	progressPath := filepath.Join(dir, ".ngen", "tasks", taskID, "progress.md")
	assertContains(t, readFile(t, progressPath), "## Current Status")
	assertContains(t, readFile(t, progressPath), "## Next Step")

	run := runCLI(t, dir, []string{"run", taskID, "--json"})
	if run.exitCode != 0 {
		t.Fatalf("expected done exit code, got %d stderr=%s", run.exitCode, run.stderr)
	}
	if !strings.Contains(run.stdout, "\"verification_passed\"") {
		t.Fatalf("expected verification_passed event, got %s", run.stdout)
	}

	status := runCLI(t, dir, []string{"status", taskID, "--json"})
	if status.exitCode != 0 {
		t.Fatalf("expected status exit code 0, got %d stderr=%s", status.exitCode, status.stderr)
	}
	var snapshot task.StatusSnapshot
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal status snapshot: %v", err)
	}
	if snapshot.State != task.StateDone {
		t.Fatalf("expected done state, got %s", snapshot.State)
	}

	store := artifact.NewStore(dir, ".ngen")
	assertFileExists(t, filepath.Join(store.TaskRoot(taskID), "baseline.json"))
	assertFileExists(t, filepath.Join(store.TaskRoot(taskID), "verification", "latest.json"))
	assertFileExists(t, filepath.Join(store.TaskRoot(taskID), "reviews", "latest.json"))
	assertFileExists(t, filepath.Join(store.TaskRoot(taskID), "completion", "latest.json"))
	assertFileExists(t, filepath.Join(store.TaskRoot(taskID), "harness", "latest.json"))
	assertFileExists(t, filepath.Join(store.TaskRoot(taskID), "harness", "history.jsonl"))
	assertFileExists(t, filepath.Join(store.TaskRoot(taskID), "handoff.md"))

	harnessEval := runCLI(t, dir, []string{"harness", "eval", taskID, "--json"})
	if harnessEval.exitCode != 0 {
		t.Fatalf("expected harness eval exit code 0, got %d stderr=%s", harnessEval.exitCode, harnessEval.stderr)
	}
	var eval task.HarnessEvaluation
	if err := json.Unmarshal([]byte(harnessEval.stdout), &eval); err != nil {
		t.Fatalf("unmarshal harness eval: %v", err)
	}
	if eval.RuntimeAction != "run" || eval.ProviderMode == "" || eval.VerificationStatus != "passed" || eval.ReviewStatus != "clear" || eval.CompletionStatus != "accepted" {
		t.Fatalf("unexpected harness evaluation: %+v", eval)
	}
	if eval.ContextPackRef != "context/latest-pack.json" || eval.ContinuityRef != "continuity/latest.json" || eval.SprintRef != "sprint/latest.json" {
		t.Fatalf("expected context refs in harness evaluation, got %+v", eval)
	}

	progress := readFile(t, filepath.Join(store.TaskRoot(taskID), "progress.md"))
	assertContains(t, progress, "## Objective")
	assertContains(t, progress, "## Current Status")
	assertContains(t, progress, "## Latest Verification")
	assertContains(t, progress, "## Review And Completion")
	assertContains(t, progress, "## Recent Repairs")
	assertContains(t, progress, "## Latest Evidence")
	assertContains(t, progress, "## Next Step")

	handoff := readFile(t, filepath.Join(store.TaskRoot(taskID), "handoff.md"))
	assertContains(t, handoff, "## Task Summary")
	assertContains(t, handoff, "## Status")
	assertContains(t, handoff, "## Evidence")
	assertContains(t, handoff, "## Changed Files Or Touched Areas")
	assertContains(t, handoff, "## Open Risks")
	assertContains(t, handoff, "## Resume Instructions")

	var ctxSummary task.ContextSummary
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(store.TaskRoot(taskID), "context", "latest-pack.json"))), &ctxSummary); err != nil {
		t.Fatalf("unmarshal context summary: %v", err)
	}
	if strings.TrimSpace(ctxSummary.PackID) == "" || strings.TrimSpace(ctxSummary.BuiltAt) == "" {
		t.Fatalf("expected rich context pack metadata, got %+v", ctxSummary)
	}
	if !contains(ctxSummary.BasedOnRefs, "verification/latest.json") {
		t.Fatalf("expected verification ref in context based_on_refs, got %+v", ctxSummary.BasedOnRefs)
	}
	if !contains(ctxSummary.IncludedRefs, "completion/latest.json") {
		t.Fatalf("expected completion ref in context included_refs, got %+v", ctxSummary.IncludedRefs)
	}
	if ctxSummary.Compaction.Performed != true || ctxSummary.Compaction.SummaryRef != "context/summary.md" {
		t.Fatalf("expected compaction summary ref in context pack, got %+v", ctxSummary.Compaction)
	}
	if len(ctxSummary.Sections) == 0 {
		t.Fatalf("expected context pack sections, got %+v", ctxSummary)
	}
	compacted := readFile(t, filepath.Join(store.TaskRoot(taskID), "context", "summary.md"))
	assertContains(t, compacted, "## Task Focus")
	assertContains(t, compacted, "## Criteria Snapshot")
	assertContains(t, compacted, "## Recent Repairs")
	assertContains(t, compacted, "## Continuity Refs")
	if !containsSection(ctxSummary.Sections, "memory_summary") {
		t.Fatalf("expected memory_summary section in context pack, got %+v", ctxSummary.Sections)
	}
}

func TestHarnessEvaluationRecordsFailedRunAndReviewPass(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/fail\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "broken.go"), "package fail\n\nfunc Broken() {\n")

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "coding", "--title", "failing", "--objective", "capture failing harness", "--criterion", "go test passes"}).stdout)
	run := runCLI(t, dir, []string{"run", taskID, "--json"})
	if run.exitCode == 0 {
		t.Fatalf("expected failing verifier exit code, got stdout=%s", run.stdout)
	}

	var failed task.HarnessEvaluation
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "harness", "latest.json"))), &failed); err != nil {
		t.Fatalf("unmarshal failed harness eval: %v", err)
	}
	if failed.RuntimeAction != "run" || failed.VerificationStatus != "failed" || failed.BlockedReasonCode != "failed_verification" {
		t.Fatalf("expected failed run harness evaluation, got %+v", failed)
	}

	reviewTaskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "general_execution", "--preset", "docs_lite", "--title", "review first", "--objective", "capture review harness", "--criterion", "review reports missing verification"}).stdout)
	review := runCLI(t, dir, []string{"review", reviewTaskID, "--json"})
	if review.exitCode == 0 {
		t.Fatalf("expected review before verification to block, got stdout=%s", review.stdout)
	}
	var reviewed task.HarnessEvaluation
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, ".ngen", "tasks", reviewTaskID, "harness", "latest.json"))), &reviewed); err != nil {
		t.Fatalf("unmarshal review harness eval: %v", err)
	}
	if reviewed.RuntimeAction != "review" || reviewed.ReviewStatus != "blocking" || reviewed.BlockedReasonCode != "blocked_review" {
		t.Fatalf("expected blocking review harness evaluation, got %+v", reviewed)
	}
}

func TestMissionCreateValidateRunAndPlan(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/mission\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package mission\n\nfunc Add(a, b int) int { return a + b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package mission\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")

	created := runCLI(t, dir, []string{"mission", "create", "--title", "mission demo", "--objective", "complete a mission-backed coding task", "--criterion", "go test passes", "--json"})
	if created.exitCode != 0 {
		t.Fatalf("expected mission create exit 0, got %d stderr=%s", created.exitCode, created.stderr)
	}
	var createView task.MissionView
	if err := json.Unmarshal([]byte(created.stdout), &createView); err != nil {
		t.Fatalf("unmarshal mission create view: %v", err)
	}
	missionID := createView.Mission.MissionID
	if missionID == "" || createView.Mission.RootTaskID == "" || createView.Contract.ContractID == "" {
		t.Fatalf("expected mission/root/contract ids, got %+v", createView)
	}
	if createView.Mission.PlanApprovalStatus != task.MissionPlanApprovalPending || createView.Mission.StatusReasonCode != "awaiting_plan_approval" {
		t.Fatalf("expected new mission to await plan approval, got %+v", createView.Mission)
	}
	if len(createView.Contract.Assertions) != 1 || createView.Contract.Assertions[0].AssertionID != "ASSERT-001" {
		t.Fatalf("expected stable assertion id in mission contract, got %+v", createView.Contract.Assertions)
	}
	if createView.Contract.Assertions[0].NegativeCase == "" || createView.Contract.Assertions[0].ManualCheck == "" || len(createView.Contract.NegativeCases) == 0 || len(createView.Contract.ManualChecks) == 0 {
		t.Fatalf("expected mission contract to include assertion-level negative/manual checks, got %+v", createView.Contract)
	}
	store := artifact.NewStore(dir, ".ngen")
	assertFileExists(t, filepath.Join(store.MissionRoot(missionID), "mission.json"))
	assertFileExists(t, filepath.Join(store.MissionRoot(missionID), "validation_contract.json"))
	assertFileExists(t, filepath.Join(store.MissionRoot(missionID), "features.json"))
	assertFileExists(t, filepath.Join(store.MissionRoot(missionID), "milestones.json"))
	assertFileExists(t, filepath.Join(store.MissionRoot(missionID), "notes.md"))

	plan := runCLI(t, dir, []string{"mission", "plan", missionID, "--json"})
	if plan.exitCode != 0 {
		t.Fatalf("expected mission plan exit 0, got %d stderr=%s", plan.exitCode, plan.stderr)
	}
	var planView task.MissionPlanView
	if err := json.Unmarshal([]byte(plan.stdout), &planView); err != nil {
		t.Fatalf("unmarshal mission plan: %v", err)
	}
	if len(planView.Features.Features) != 1 || len(planView.Milestones.Milestones) != 1 {
		t.Fatalf("expected one feature and one milestone, got %+v", planView)
	}
	if planView.Features.Features[0].BoundTaskID != createView.Mission.RootTaskID {
		t.Fatalf("expected feature bound to root task, got %+v", planView.Features.Features[0])
	}
	if len(planView.Features.Features[0].ContractCoverage) != 1 || planView.Features.Features[0].ContractCoverage[0] != "ASSERT-001" {
		t.Fatalf("expected feature coverage to reference assertion id, got %+v", planView.Features.Features[0].ContractCoverage)
	}
	if planView.Milestones.CurrentFeatureID == "" {
		t.Fatalf("expected mission plan to refresh feature scheduling pointers, got %+v", planView.Milestones)
	}

	blocked := runCLI(t, dir, []string{"mission", "validate", missionID, "--json"})
	if blocked.exitCode != 10 {
		t.Fatalf("expected blocked validation exit 10, got %d stderr=%s stdout=%s", blocked.exitCode, blocked.stderr, blocked.stdout)
	}
	var blockedView task.MissionView
	if err := json.Unmarshal([]byte(blocked.stdout), &blockedView); err != nil {
		t.Fatalf("unmarshal blocked mission validate: %v", err)
	}
	if blockedView.Mission.Status != task.MissionStatusBlocked || blockedView.LatestValidation == nil || blockedView.LatestValidation.Status != "blocking" || len(blockedView.LatestValidation.Findings) == 0 {
		t.Fatalf("expected blocking validation findings, got %+v", blockedView)
	}
	if blockedView.LatestValidation.Findings[0].Category != "plan_unapproved" {
		t.Fatalf("expected unapproved plan finding before execution, got %+v", blockedView.LatestValidation.Findings)
	}

	approved := runCLI(t, dir, []string{"mission", "approve", missionID, "--json"})
	if approved.exitCode != 0 {
		t.Fatalf("expected mission approve exit 0, got %d stderr=%s stdout=%s", approved.exitCode, approved.stderr, approved.stdout)
	}
	var approvedView task.MissionView
	if err := json.Unmarshal([]byte(approved.stdout), &approvedView); err != nil {
		t.Fatalf("unmarshal mission approve: %v", err)
	}
	if approvedView.Mission.PlanApprovalStatus != task.MissionPlanApprovalApproved || approvedView.Mission.PlanApprovedContractRef == "" {
		t.Fatalf("expected approved mission plan, got %+v", approvedView.Mission)
	}
	if approvedView.Mission.LatestValidationRef != "" || approvedView.LatestValidation != nil {
		t.Fatalf("expected approval to clear stale blocking validation pointer, got mission=%+v latest=%+v", approvedView.Mission, approvedView.LatestValidation)
	}

	run := runCLI(t, dir, []string{"mission", "run", missionID, "--json"})
	if run.exitCode != 0 {
		t.Fatalf("expected mission run exit 0, got %d stderr=%s stdout=%s", run.exitCode, run.stderr, run.stdout)
	}
	var runView task.MissionView
	if err := json.Unmarshal([]byte(run.stdout), &runView); err != nil {
		t.Fatalf("unmarshal mission run: %v", err)
	}
	if runView.Mission.Status != task.MissionStatusDone || runView.LatestValidation == nil || runView.LatestValidation.Status != "passed" {
		t.Fatalf("expected done mission with passed validation, got %+v", runView)
	}
	if runView.RootTaskStatus == nil || runView.RootTaskStatus.State != task.StateDone {
		t.Fatalf("expected root task done status in mission view, got %+v", runView.RootTaskStatus)
	}
	assertFileExists(t, filepath.Join(store.MissionRoot(missionID), "validation_runs.jsonl"))
}

func TestMissionCreatePersistsRolePlanSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/missionroleplan\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "ngen.json"), `{
  "provider": {
    "mode": "builtin",
    "model": "global-model"
  },
  "mission": {
    "role_models": {
      "orchestrator": "orchestrator-model",
      "workers": "worker-model",
      "validators": "validator-model"
    }
  }
}`)

	created := runCLI(t, dir, []string{"mission", "create", "--title", "role plan", "--objective", "persist mission role plan", "--criterion", "role plan is visible", "--json"})
	if created.exitCode != 0 {
		t.Fatalf("expected mission create exit 0, got %d stderr=%s", created.exitCode, created.stderr)
	}
	var createView task.MissionView
	if err := json.Unmarshal([]byte(created.stdout), &createView); err != nil {
		t.Fatalf("unmarshal mission create view: %v", err)
	}
	if createView.Mission.RolePlan[task.MissionRoleOrchestrator].Model != "orchestrator-model" ||
		createView.Mission.RolePlan[task.MissionRoleWorkers].Model != "worker-model" ||
		!createView.Mission.RolePlan[task.MissionRoleValidators].Explicit {
		t.Fatalf("expected role plan snapshot in mission create view, got %+v", createView.Mission.RolePlan)
	}

	writeFile(t, filepath.Join(dir, "ngen.json"), `{
  "provider": {
    "mode": "builtin",
    "model": "changed-global"
  },
  "mission": {
    "role_models": {
      "orchestrator": "changed-orchestrator",
      "workers": "changed-worker",
      "validators": "changed-validator"
    }
  }
}`)
	got := runCLI(t, dir, []string{"mission", "get", createView.Mission.MissionID, "--json"})
	if got.exitCode != 0 {
		t.Fatalf("expected mission get exit 0, got %d stderr=%s", got.exitCode, got.stderr)
	}
	var getView task.MissionView
	if err := json.Unmarshal([]byte(got.stdout), &getView); err != nil {
		t.Fatalf("unmarshal mission get view: %v", err)
	}
	if getView.Mission.RolePlan[task.MissionRoleOrchestrator].Model != "orchestrator-model" ||
		getView.Mission.RolePlan[task.MissionRoleWorkers].Model != "worker-model" ||
		getView.Mission.RolePlan[task.MissionRoleValidators].Model != "validator-model" {
		t.Fatalf("expected role plan snapshot to remain unchanged after config edit, got %+v", getView.Mission.RolePlan)
	}
}

func TestMissionAndGoalPromptShortcutsCreateMission(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/missionprompt\n\ngo 1.24.0\n")

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "mission positional", args: []string{"mission", "ship", "docs", "with", "tests"}, want: "ship docs with tests"},
		{name: "mission create positional", args: []string{"mission", "create", "ship", "cli", "shortcut"}, want: "ship cli shortcut"},
		{name: "goal alias", args: []string{"goal", "finish", "the", "release"}, want: "finish the release"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := runCLI(t, dir, tc.args)
			if result.exitCode != 0 {
				t.Fatalf("expected shortcut exit 0, got %d stderr=%s stdout=%s", result.exitCode, result.stderr, result.stdout)
			}
			missionID := missionIDFromShortcutOutput(t, result.stdout)
			store := artifact.NewStore(dir, ".ngen")
			mission, err := store.LoadMission(missionID)
			if err != nil {
				t.Fatalf("load mission: %v", err)
			}
			if mission.Objective != tc.want {
				t.Fatalf("expected objective %q, got %+v", tc.want, mission)
			}
		})
	}
}

func missionIDFromShortcutOutput(t *testing.T, output string) string {
	t.Helper()
	output = strings.TrimSpace(output)
	if strings.HasPrefix(output, "MIS-") {
		return output
	}
	fields := strings.Fields(output)
	if len(fields) > 0 && strings.HasPrefix(fields[0], "mission=") {
		return strings.TrimPrefix(fields[0], "mission=")
	}
	t.Fatalf("expected mission id or compact mission output, got %q", output)
	return ""
}

func TestCodingTaskUsesCriterionScopedVerifierCommand(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/repo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "cmd", "demo", "main.go"), "package main\n\nimport (\n\t\"fmt\"\n\t\"example.com/repo/internal/sum\"\n)\n\nfunc main() { fmt.Println(sum.Add(2, 3)) }\n")
	writeFile(t, filepath.Join(dir, "internal", "sum", "sum.go"), "package sum\n\nfunc Add(a, b int) int { return a + b }\n")
	writeFile(t, filepath.Join(dir, "internal", "sum", "sum_test.go"), "package sum\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")
	writeFile(t, filepath.Join(dir, "build.sh"), "#!/usr/bin/env bash\nset -euo pipefail\ngo test ./cmd/... ./internal/...\n")
	writeFile(t, filepath.Join(dir, "codex", "archive", "bad.go"), "package broken\n\nfunc Broken() {\n")
	if err := os.Chmod(filepath.Join(dir, "build.sh"), 0o755); err != nil {
		t.Fatalf("chmod build.sh: %v", err)
	}

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "coding", "--title", "scoped verifier", "--objective", "use build verifier", "--criterion", "`./build.sh test` passes"}).stdout)
	run := runCLI(t, dir, []string{"run", taskID, "--json"})
	if run.exitCode != 0 {
		t.Fatalf("expected criterion-scoped verifier run done, got %d stderr=%s stdout=%s", run.exitCode, run.stderr, run.stdout)
	}
	assertContains(t, run.stdout, "\"verification_passed\"")
	assertContains(t, readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "verification", "latest.json")), "./build.sh test")
}

func TestCodingTaskUsesConfiguredVerifierCommandSequence(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/repo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "cmd", "demo", "main.go"), "package main\n\nimport (\n\t\"fmt\"\n\t\"example.com/repo/internal/sum\"\n)\n\nfunc main() { fmt.Println(sum.Add(2, 3)) }\n")
	writeFile(t, filepath.Join(dir, "internal", "sum", "sum.go"), "package sum\n\nfunc Add(a, b int) int { return a + b }\n")
	writeFile(t, filepath.Join(dir, "internal", "sum", "sum_test.go"), "package sum\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")
	writeFile(t, filepath.Join(dir, "build.sh"), "#!/usr/bin/env bash\nset -euo pipefail\n\ncase \"${1:-}\" in\n  test)\n    go test ./internal/...\n    ;;\n  build)\n    go build -buildvcs=false ./cmd/demo\n    ;;\n  *)\n    echo \"unsupported\" >&2\n    exit 2\n    ;;\n esac\n")
	writeFile(t, filepath.Join(dir, "ngen.json"), `{
  "verification": {
    "coding_commands": [
      ["./build.sh", "test"],
      ["./build.sh", "build"]
    ]
  }
}`)
	writeFile(t, filepath.Join(dir, "codex", "archive", "bad.go"), "package broken\n\nfunc Broken() {\n")
	if err := os.Chmod(filepath.Join(dir, "build.sh"), 0o755); err != nil {
		t.Fatalf("chmod build.sh: %v", err)
	}

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "coding", "--title", "configured verifier sequence", "--objective", "use configured verifier sequence", "--criterion", "`./build.sh test` passes", "--criterion", "`./build.sh build` passes"}).stdout)
	run := runCLI(t, dir, []string{"run", taskID, "--json"})
	if run.exitCode != 0 {
		t.Fatalf("expected configured verifier sequence run done, got %d stderr=%s stdout=%s", run.exitCode, run.stderr, run.stdout)
	}
	verification := readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "verification", "latest.json"))
	var report task.VerificationReport
	if err := json.Unmarshal([]byte(verification), &report); err != nil {
		t.Fatalf("unmarshal verification report: %v", err)
	}
	if len(report.Checks) != 2 {
		t.Fatalf("expected two verifier checks, got %+v", report.Checks)
	}
	if got := report.Checks[0].Command; len(got) != 2 || got[0] != "./build.sh" || got[1] != "test" {
		t.Fatalf("expected first verifier command to be build.sh test, got %+v", report.Checks[0].Command)
	}
	if got := report.Checks[1].Command; len(got) != 2 || got[0] != "./build.sh" || got[1] != "build" {
		t.Fatalf("expected second verifier command to be build.sh build, got %+v", report.Checks[1].Command)
	}
}

func TestOpenAIResponsesCodingTaskRepairsWorkspaceAndPersistsEditArtifact(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package main\n\n// Add should sum two numbers.\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		text, ok := body["text"].(map[string]any)
		if !ok {
			t.Fatalf("expected text body, got %#v", body["text"])
		}
		format, ok := text["format"].(map[string]any)
		if !ok {
			t.Fatalf("expected format body, got %#v", text["format"])
		}
		switch format["name"] {
		case "ngen_provider_decision":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"action\":\"run\",\"summary\":\"Run coding task now\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
					}]
				}]
			}`))
		case "ngen_workspace_observation":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"summary\":\"No extra observation needed\",\"commands\":[]}"
					}]
				}]
			}`))
		case "ngen_workspace_edit":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"summary\":\"Implement Add in calc.go\",\"writes\":[{\"path\":\"calc.go\",\"content\":\"package main\\n\\n// Add should sum two numbers.\\nfunc Add(a, b int) int { return a + b }\\n\"}],\"deletes\":[]}"
					}]
				}]
			}`))
		default:
			t.Fatalf("unexpected schema name: %#v", format["name"])
		}
	}))
	defer server.Close()

	writeProviderConfig(t, dir, map[string]any{
		"mode":        "responses",
		"base_url":    server.URL + "/v1",
		"model":       "gpt-5.4",
		"api_key_env": "OPENAI_API_KEY",
	})

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "coding", "--title", "repair add", "--objective", "implement Add", "--constraint", "Do not modify *_test.go files.", "--criterion", "go test passes"}).stdout)
	auto := runCLI(t, dir, []string{"auto", taskID, "--json"})
	if auto.exitCode != 0 {
		t.Fatalf("expected coding auto repair to finish, got %d stderr=%s stdout=%s", auto.exitCode, auto.stderr, auto.stdout)
	}
	assertContains(t, auto.stdout, "\"workspace_edit_started\"")
	assertContains(t, auto.stdout, "\"workspace_edit_applied\"")

	status := runCLI(t, dir, []string{"status", taskID, "--json"})
	var snapshot task.StatusSnapshot
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal status snapshot: %v", err)
	}
	if snapshot.State != task.StateDone {
		t.Fatalf("expected done state after repair, got %s", snapshot.State)
	}

	edited := readFile(t, filepath.Join(dir, "calc.go"))
	assertContains(t, edited, "func Add(a, b int) int")
	assertContains(t, readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "workspace_edits.jsonl")), "\"workspace_edit\"")
	assertContains(t, readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "handoff.md")), "calc.go (write)")
}

func TestOpenAIResponsesCodingTaskBlocksReviewWhenCriterionEvidenceIsMissing(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package main\n\nfunc Add(a, b int) int { return a - b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")
	writeFile(t, filepath.Join(dir, "README.md"), "# Demo\n\nThis guide is still incomplete.\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		text, ok := body["text"].(map[string]any)
		if !ok {
			t.Fatalf("expected text body, got %#v", body["text"])
		}
		format, ok := text["format"].(map[string]any)
		if !ok {
			t.Fatalf("expected format body, got %#v", text["format"])
		}
		switch format["name"] {
		case "ngen_provider_decision":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"action\":\"run\",\"summary\":\"Run coding task now\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
					}]
				}]
			}`))
		case "ngen_workspace_observation":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"summary\":\"No extra observation needed\",\"commands\":[]}"
					}]
				}]
			}`))
		case "ngen_workspace_edit":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"summary\":\"Fix Add only\",\"writes\":[{\"path\":\"calc.go\",\"content\":\"package main\\n\\nfunc Add(a, b int) int { return a + b }\\n\"}],\"deletes\":[]}"
					}]
				}]
			}`))
		default:
			t.Fatalf("unexpected schema name: %#v", format["name"])
		}
	}))
	defer server.Close()

	writeProviderConfig(t, dir, map[string]any{
		"mode":        "responses",
		"base_url":    server.URL + "/v1",
		"model":       "gpt-5.4",
		"api_key_env": "OPENAI_API_KEY",
	})

	taskID := strings.TrimSpace(runCLI(t, dir, []string{
		"task", "create",
		"--kind", "coding",
		"--title", "criteria gap",
		"--objective", "implement Add and update README.md",
		"--constraint", "Do not modify *_test.go files.",
		"--criterion", "go test ./... passes",
		"--criterion", "README.md mentions `Add`",
	}).stdout)
	auto := runCLI(t, dir, []string{"auto", taskID, "--json"})
	if auto.exitCode != 10 {
		t.Fatalf("expected blocked_review exit code 10, got %d stderr=%s stdout=%s", auto.exitCode, auto.stderr, auto.stdout)
	}
	assertContains(t, auto.stdout, "\"completion_rejected\"")

	status := runCLI(t, dir, []string{"status", taskID, "--json"})
	var snapshot task.StatusSnapshot
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal status snapshot: %v", err)
	}
	if snapshot.State != task.StateBlocked || snapshot.StatusReasonCode != "blocked_review" {
		t.Fatalf("expected blocked_review after missing README evidence, got %+v", snapshot)
	}

	criteria := readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "criteria", "latest.json"))
	assertContains(t, criteria, `"criterion_id": "SC-002"`)
	assertContains(t, criteria, `"status": "open"`)
	assertContains(t, readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "completion", "latest.json")), `"status": "rejected"`)
	var plan task.Plan
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "plan.json"))), &plan); err != nil {
		t.Fatalf("unmarshal plan: %v", err)
	}
	foundExecution := false
	foundOpenCriterion := false
	for _, step := range plan.Steps {
		if step.Kind == task.StepKindExecution {
			foundExecution = true
		}
		if step.Kind == task.StepKindCriterion && len(step.Covers) == 1 && step.Covers[0] == "SC-002" && step.Status == task.StepStatusInProgress {
			foundOpenCriterion = true
		}
	}
	if !foundExecution || !foundOpenCriterion {
		t.Fatalf("expected open README criterion to be current plan step, got %+v", plan.Steps)
	}
	progress := readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "progress.md"))
	assertContains(t, progress, "Current Gate:")
	assertContains(t, progress, "README.md mentions `Add`")
}

func TestTaskUpdateGetAndListExposeMutableExecutionPlan(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# planning\n")

	taskID := strings.TrimSpace(runCLI(t, dir, []string{
		"task", "create",
		"--kind", "general_execution",
		"--preset", "docs_lite",
		"--title", "mutable plan",
		"--objective", "review docs and hand off cleanly",
		"--criterion", "docs reviewed",
		"--criterion", "handoff captured",
	}).stdout)

	planFile := filepath.Join(dir, "plan-update.json")
	writeFile(t, planFile, `{
  "explanation": "Track long-horizon execution in a mutable checklist.",
  "steps": [
    {
      "id": "epic.repo_truth",
      "priority": "high",
      "title": "Inspect repo truth",
      "status": "in_progress",
      "covers": ["SC-001"],
      "notes": "Start with README and docs."
    },
    {
      "id": "handoff.closeout",
      "parent_step_id": "epic.repo_truth",
      "depends_on": ["epic.repo_truth"],
      "title": "Refresh handoff and close",
      "status": "pending",
      "covers": ["SC-002"],
      "notes": ""
    }
  ]
}`)

	update := runCLI(t, dir, []string{"task", "update", taskID, "--plan-file", planFile, "--json"})
	if update.exitCode != 0 {
		t.Fatalf("task update failed: %d stderr=%s stdout=%s", update.exitCode, update.stderr, update.stdout)
	}
	var updated task.TaskView
	if err := json.Unmarshal([]byte(update.stdout), &updated); err != nil {
		t.Fatalf("unmarshal task update result: %v", err)
	}
	if updated.Plan.Explanation != "Track long-horizon execution in a mutable checklist." {
		t.Fatalf("expected persisted plan explanation, got %+v", updated.Plan)
	}
	if updated.Plan.CurrentExecutionStepID != "epic.repo_truth" {
		t.Fatalf("expected first execution step to be current, got %+v", updated.Plan)
	}
	if updated.Plan.Revision != 1 || !strings.Contains(updated.Plan.LastMutationRef, "plan_updates.jsonl#mutation_id=") {
		t.Fatalf("expected first mutable-plan revision and mutation ref, got %+v", updated.Plan)
	}
	if strings.Join(updated.Plan.ReadyExecutionStepIDs, ",") != "epic.repo_truth" || strings.Join(updated.Plan.BlockedExecutionStepIDs, ",") != "handoff.closeout" {
		t.Fatalf("expected ready/block execution graph summary, got %+v", updated.Plan)
	}
	if updated.Plan.CurrentSystemStepID != "STEP-001" {
		t.Fatalf("expected baseline to remain current gate before first run, got %+v", updated.Plan)
	}
	if updated.Status.PlanRevision != 1 || updated.Status.CurrentStepID != "epic.repo_truth" {
		t.Fatalf("expected status current step to point at mutable execution step, got %+v", updated.Status)
	}
	if len(updated.Plan.Steps) != 6 {
		t.Fatalf("expected baseline + 2 execution + 2 criteria + final gate, got %+v", updated.Plan.Steps)
	}
	if updated.Plan.Steps[1].ID != "epic.repo_truth" || updated.Plan.Steps[2].ID != "handoff.closeout" || updated.Plan.Steps[2].ParentStepID != "epic.repo_truth" {
		t.Fatalf("expected stable graph ids to persist in plan steps, got %+v", updated.Plan.Steps)
	}

	get := runCLI(t, dir, []string{"task", "get", taskID, "--json"})
	if get.exitCode != 0 {
		t.Fatalf("task get failed: %d stderr=%s stdout=%s", get.exitCode, get.stderr, get.stdout)
	}
	var view task.TaskView
	if err := json.Unmarshal([]byte(get.stdout), &view); err != nil {
		t.Fatalf("unmarshal task get result: %v", err)
	}
	if view.Plan.CurrentExecutionStepID != "epic.repo_truth" || view.Status.CurrentSystemStepID != "STEP-001" {
		t.Fatalf("expected task get to expose execution/system step ids, got %+v", view)
	}

	list := runCLI(t, dir, []string{"task", "list", "--json"})
	if list.exitCode != 0 {
		t.Fatalf("task list failed: %d stderr=%s stdout=%s", list.exitCode, list.stderr, list.stdout)
	}
	var entries []task.TaskListEntry
	if err := json.Unmarshal([]byte(list.stdout), &entries); err != nil {
		t.Fatalf("unmarshal task list: %v", err)
	}
	if len(entries) != 1 || entries[0].TaskID != taskID {
		t.Fatalf("expected one task list entry, got %+v", entries)
	}
	if entries[0].PlanRevision != 1 || entries[0].CurrentExecutionStepID != "epic.repo_truth" || entries[0].CurrentSystemStepID != "STEP-001" {
		t.Fatalf("expected task list entry to expose mutable/system steps, got %+v", entries[0])
	}

	progress := readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "progress.md"))
	assertContains(t, progress, "Current Execution Step: epic.repo_truth (Inspect repo truth)")
	assertContains(t, progress, "Plan Revision: 1")
	assertContains(t, progress, "Ready Execution Steps: epic.repo_truth (Inspect repo truth)")
	assertContains(t, progress, "Blocked Execution Steps: handoff.closeout (Refresh handoff and close)")
	assertContains(t, progress, "[>] epic.repo_truth Inspect repo truth")
	assertContains(t, progress, "depends_on: epic.repo_truth")
	assertContains(t, progress, "Current Gate: STEP-001 (Capture baseline)")
}

func TestTaskPatchPersistsIncrementalExecutionGraphMutation(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")

	taskID := strings.TrimSpace(runCLI(t, dir, []string{
		"task", "create",
		"--kind", "general_execution",
		"--preset", "docs_lite",
		"--title", "mutable plan patch",
		"--objective", "review docs",
		"--criterion", "docs reviewed",
		"--criterion", "handoff captured",
	}).stdout)

	initialPlanFile := filepath.Join(dir, "plan-update.json")
	writeFile(t, initialPlanFile, `{
  "explanation": "Track long-horizon execution in a mutable checklist.",
  "steps": [
    {
      "id": "epic.repo_truth",
      "priority": "high",
      "title": "Inspect repo truth",
      "status": "in_progress",
      "covers": ["SC-001"],
      "notes": "Start with README and docs."
    },
    {
      "id": "legacy.closeout",
      "depends_on": ["epic.repo_truth"],
      "title": "Refresh handoff and close",
      "status": "pending",
      "covers": ["SC-002"],
      "notes": ""
    }
  ]
}`)
	initial := runCLI(t, dir, []string{"task", "update", taskID, "--plan-file", initialPlanFile, "--json"})
	if initial.exitCode != 0 {
		t.Fatalf("task update failed: %d stderr=%s stdout=%s", initial.exitCode, initial.stderr, initial.stdout)
	}

	patchFile := filepath.Join(dir, "plan-patch.json")
	writeFile(t, patchFile, `{
  "operations": [
    {
      "op": "set_explanation",
      "explanation": "Shift from bootstrap checklist to an incrementally patched execution graph."
    },
    {
      "op": "upsert_step",
      "after_step_id": "epic.repo_truth",
      "step": {
        "id": "handoff.closeout",
        "parent_step_id": "epic.repo_truth",
        "depends_on": ["epic.repo_truth"],
        "priority": "high",
        "title": "Refresh handoff with current findings",
        "status": "pending",
        "covers": ["SC-002"],
        "notes": "Prefer patching the closeout node over rewriting the whole plan."
      }
    },
    {
      "op": "remove_step",
      "step_id": "legacy.closeout"
    }
  ]
}`)

	patch := runCLI(t, dir, []string{"task", "patch", taskID, "--patch-file", patchFile, "--json"})
	if patch.exitCode != 0 {
		t.Fatalf("task patch failed: %d stderr=%s stdout=%s", patch.exitCode, patch.stderr, patch.stdout)
	}
	var updated task.TaskView
	if err := json.Unmarshal([]byte(patch.stdout), &updated); err != nil {
		t.Fatalf("unmarshal task patch result: %v", err)
	}
	if updated.Plan.Revision != 2 {
		t.Fatalf("expected plan revision 2 after patch, got %+v", updated.Plan)
	}
	if updated.Plan.Explanation != "Shift from bootstrap checklist to an incrementally patched execution graph." {
		t.Fatalf("expected patched plan explanation, got %+v", updated.Plan)
	}
	if updated.Plan.CurrentExecutionStepID != "epic.repo_truth" {
		t.Fatalf("expected current execution step to remain epic.repo_truth, got %+v", updated.Plan)
	}
	if len(updated.Plan.Steps) != 6 || updated.Plan.Steps[2].ID != "handoff.closeout" {
		t.Fatalf("expected patched execution step to replace legacy.closeout, got %+v", updated.Plan.Steps)
	}
	for _, step := range updated.Plan.Steps {
		if step.ID == "legacy.closeout" {
			t.Fatalf("expected legacy.closeout to be removed, got %+v", updated.Plan.Steps)
		}
	}

	mutations := readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "plan_updates.jsonl"))
	assertContains(t, mutations, `"mutation_kind":"patch"`)
	assertContains(t, mutations, `"op":"remove_step"`)
	assertContains(t, mutations, `"id":"handoff.closeout"`)

	progress := readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "progress.md"))
	assertContains(t, progress, "Plan Revision: 2")
	assertContains(t, progress, "[ ] handoff.closeout Refresh handoff with current findings")
}

func TestProjectGetUpdateAndPatchExposeWorkspaceGraph(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# project\n")

	taskID1 := strings.TrimSpace(runCLI(t, dir, []string{
		"task", "create",
		"--kind", "general_execution",
		"--preset", "docs_lite",
		"--title", "repo truth",
		"--objective", "inspect the repository",
		"--criterion", "repo truth captured",
	}).stdout)
	taskID2 := strings.TrimSpace(runCLI(t, dir, []string{
		"task", "create",
		"--kind", "general_execution",
		"--preset", "docs_lite",
		"--title", "patch lane",
		"--objective", "apply the follow-up patch",
		"--criterion", "patch applied",
	}).stdout)
	blocked := runCLI(t, dir, []string{"input", "request", taskID2, "--prompt", "Provide patch path", "--field", "target_path"})
	if blocked.exitCode != 10 {
		t.Fatalf("input request for second task failed: %d stderr=%s stdout=%s", blocked.exitCode, blocked.stderr, blocked.stdout)
	}

	projectFile := filepath.Join(dir, "project-update.json")
	writeFile(t, projectFile, fmt.Sprintf(`{
  "explanation": "Coordinate two durable tasks through a workspace project graph.",
  "steps": [
    {
      "id": "epic.repo_truth",
      "priority": "high",
      "title": "Inspect repo truth",
      "status": "in_progress",
      "branch_id": "branch.repo",
      "task_id": "%s",
      "notes": "Track the root repo task."
    },
    {
      "id": "epic.patch",
      "parent_step_id": "epic.repo_truth",
      "depends_on": ["epic.repo_truth"],
      "priority": "medium",
      "title": "Apply patch after repo truth",
      "status": "pending",
      "branch_id": "branch.patch",
      "task_id": "%s",
      "notes": ""
    }
  ],
  "branches": [
    {
      "id": "branch.repo",
      "title": "Repo truth lane",
      "status": "active",
      "task_id": "%s",
      "notes": "Primary lane."
    },
    {
      "id": "branch.patch",
      "title": "Patch lane",
      "status": "pending",
      "task_id": "%s",
      "notes": ""
    }
  ]
}`, taskID1, taskID2, taskID1, taskID2))

	update := runCLI(t, dir, []string{"project", "update", "--project-file", projectFile, "--json"})
	if update.exitCode != 0 {
		t.Fatalf("project update failed: %d stderr=%s stdout=%s", update.exitCode, update.stderr, update.stdout)
	}
	var updated task.ProjectView
	if err := json.Unmarshal([]byte(update.stdout), &updated); err != nil {
		t.Fatalf("unmarshal project update result: %v", err)
	}
	if updated.Project.Revision < 3 {
		t.Fatalf("expected project revision to advance beyond auto-tracked task mutations, got %+v", updated.Project)
	}
	if updated.Project.CurrentStepID != "epic.repo_truth" || strings.Join(updated.Project.BlockedStepIDs, ",") != "epic.patch" {
		t.Fatalf("expected current/blocked project graph summary, got %+v", updated.Project)
	}

	patchFile := filepath.Join(dir, "project-patch.json")
	writeFile(t, patchFile, fmt.Sprintf(`{
  "operations": [
    {
      "op": "set_explanation",
      "explanation": "Promote the patch lane once repo truth is done."
    },
    {
      "op": "set_step_dependencies",
      "step_id": "epic.patch",
      "depends_on": []
    },
    {
      "op": "bind_step_task",
      "step_id": "epic.patch",
      "task_id": ""
    },
    {
      "op": "bind_branch_task",
      "branch_id": "branch.patch",
      "task_id": ""
    },
    {
      "op": "set_branch_status",
      "branch_id": "branch.patch",
      "status": "active"
    }
  ]
}`))

	patch := runCLI(t, dir, []string{"project", "patch", "--patch-file", patchFile, "--json"})
	if patch.exitCode != 0 {
		t.Fatalf("project patch failed: %d stderr=%s stdout=%s", patch.exitCode, patch.stderr, patch.stdout)
	}
	var patched task.ProjectView
	if err := json.Unmarshal([]byte(patch.stdout), &patched); err != nil {
		t.Fatalf("unmarshal project patch result: %v", err)
	}
	if patched.Project.Explanation != "Promote the patch lane once repo truth is done." {
		t.Fatalf("expected patched project explanation, got %+v", patched.Project)
	}
	if !strings.Contains(strings.Join(patched.Project.ActiveBranchIDs, ","), "branch.patch") {
		t.Fatalf("expected patch branch to become active, got %+v", patched.Project)
	}
	if patched.Project.Steps[1].TaskID != "" {
		t.Fatalf("expected patch step to be unbound from task after edge patch, got %+v", patched.Project.Steps[1])
	}

	get := runCLI(t, dir, []string{"project", "get", "--json"})
	if get.exitCode != 0 {
		t.Fatalf("project get failed: %d stderr=%s stdout=%s", get.exitCode, get.stderr, get.stdout)
	}
	var view task.ProjectView
	if err := json.Unmarshal([]byte(get.stdout), &view); err != nil {
		t.Fatalf("unmarshal project get result: %v", err)
	}
	if len(view.Project.Steps) != 2 || len(view.Project.Branches) != 2 {
		t.Fatalf("expected explicit project graph from project get, got %+v", view.Project)
	}

	mutations := readFile(t, filepath.Join(dir, ".ngen", "project", "project_updates.jsonl"))
	assertContains(t, mutations, `"mutation_kind":"replace"`)
	assertContains(t, mutations, `"mutation_kind":"patch"`)
	assertContains(t, mutations, `"op":"set_step_dependencies"`)
}

func TestAutoTaskUpdateDoesNotConsumeConfiguredTurnBudget(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package demo\n\nfunc Add(a, b int) int { return a + b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package demo\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	decisionCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(bodyBytes, &body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		text, ok := body["text"].(map[string]any)
		if !ok {
			t.Fatalf("expected text body, got %#v", body["text"])
		}
		format, ok := text["format"].(map[string]any)
		if !ok {
			t.Fatalf("expected format body, got %#v", text["format"])
		}
		if format["name"] != "ngen_provider_decision" {
			t.Fatalf("unexpected schema name: %#v", format["name"])
		}
		decisionCalls++
		switch decisionCalls {
		case 1:
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"action\":\"task_update\",\"summary\":\"Seed execution plan first\",\"plan_explanation\":\"Establish a mutable execution checklist before running.\",\"plan_steps\":[{\"title\":\"Verify repo truth before execution\",\"status\":\"in_progress\",\"covers\":[\"SC-001\"],\"notes\":\"Check the module and tests first.\"}],\"worker_id\":\"\",\"worker_role\":\"\",\"worker_objective\":\"\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
					}]
				}]
			}`))
		case 2:
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"action\":\"run\",\"summary\":\"Run the task now\",\"worker_id\":\"\",\"worker_role\":\"\",\"worker_objective\":\"\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
					}]
				}]
			}`))
		default:
			t.Fatalf("unexpected decision call %d", decisionCalls)
		}
	}))
	defer server.Close()

	writeFile(t, filepath.Join(dir, "ngen.json"), fmt.Sprintf(`{
  "provider": {
    "mode": "responses",
    "base_url": %q,
    "model": "gpt-5.4",
    "api_key_env": "OPENAI_API_KEY",
    "auto_run_max_turns": 1
  }
}`, server.URL+"/v1"))

	taskID := strings.TrimSpace(runCLI(t, dir, []string{
		"task", "create",
		"--kind", "coding",
		"--title", "task update budget",
		"--objective", "prove task_update can happen before run",
		"--criterion", "go test ./... passes",
	}).stdout)

	auto := runCLI(t, dir, []string{"auto", taskID, "--json"})
	if auto.exitCode != 0 {
		t.Fatalf("expected auto to reach done after task_update + run under one turn budget, got %d stderr=%s stdout=%s", auto.exitCode, auto.stderr, auto.stdout)
	}
	if decisionCalls != 2 {
		t.Fatalf("expected task_update and run decisions in a single auto pass, got %d", decisionCalls)
	}
	assertContains(t, auto.stdout, `"type":"task_plan_updated"`)
	status := runCLI(t, dir, []string{"status", taskID, "--json"})
	var snapshot task.StatusSnapshot
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal status snapshot: %v", err)
	}
	if snapshot.State != task.StateDone {
		t.Fatalf("expected done state after task_update + run, got %+v", snapshot)
	}
	var view task.TaskView
	get := runCLI(t, dir, []string{"task", "get", taskID, "--json"})
	if err := json.Unmarshal([]byte(get.stdout), &view); err != nil {
		t.Fatalf("unmarshal task get result: %v", err)
	}
	if view.Plan.Explanation == "" || len(view.Plan.Steps) != 4 {
		t.Fatalf("expected execution plan to persist alongside system plan, got %+v", view.Plan)
	}
	if view.Plan.Steps[1].Status != task.StepStatusCancelled {
		t.Fatalf("expected open execution step to be cancelled once task reached done, got %+v", view.Plan.Steps)
	}
}

func TestAutoTaskCreateDoesNotConsumeConfiguredTurnBudget(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	decisionCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(bodyBytes, &body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		text, ok := body["text"].(map[string]any)
		if !ok {
			t.Fatalf("expected text body, got %#v", body["text"])
		}
		format, ok := text["format"].(map[string]any)
		if !ok {
			t.Fatalf("expected format body, got %#v", text["format"])
		}
		if format["name"] != "ngen_provider_decision" {
			t.Fatalf("unexpected schema name: %#v", format["name"])
		}
		decisionCalls++
		switch decisionCalls {
		case 1:
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"action\":\"task_create\",\"summary\":\"Create a durable follow-up docs task first.\",\"task_kind\":\"general_execution\",\"task_preset_id\":\"docs_lite\",\"task_title\":\"follow-up docs\",\"task_objective\":\"capture a durable docs follow-up\",\"task_criteria\":[\"docs reviewed\"],\"task_constraints\":[\"do not edit generated files\"],\"task_permission_mode_id\":\"\",\"project_step_id\":\"\",\"project_branch_id\":\"\",\"plan_explanation\":\"\",\"plan_steps\":[],\"plan_patch_operations\":[],\"project_explanation\":\"\",\"project_steps\":[],\"project_branches\":[],\"project_patch_operations\":[],\"memory_kind\":\"\",\"memory_refs\":[],\"worker_id\":\"\",\"worker_role\":\"\",\"worker_objective\":\"\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
					}]
				}]
			}`))
		case 2:
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"action\":\"run\",\"summary\":\"Run the parent task now\",\"task_kind\":\"\",\"task_preset_id\":\"\",\"task_title\":\"\",\"task_objective\":\"\",\"task_criteria\":[],\"task_constraints\":[],\"task_permission_mode_id\":\"\",\"project_step_id\":\"\",\"project_branch_id\":\"\",\"plan_explanation\":\"\",\"plan_steps\":[],\"plan_patch_operations\":[],\"project_explanation\":\"\",\"project_steps\":[],\"project_branches\":[],\"project_patch_operations\":[],\"memory_kind\":\"\",\"memory_refs\":[],\"worker_id\":\"\",\"worker_role\":\"\",\"worker_objective\":\"\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
					}]
				}]
			}`))
		default:
			t.Fatalf("unexpected decision call %d", decisionCalls)
		}
	}))
	defer server.Close()

	writeFile(t, filepath.Join(dir, "ngen.json"), fmt.Sprintf(`{
  "provider": {
    "mode": "responses",
    "base_url": %q,
    "model": "gpt-5.4",
    "api_key_env": "OPENAI_API_KEY",
    "auto_run_max_turns": 1
  }
}`, server.URL+"/v1"))

	parentID := strings.TrimSpace(runCLI(t, dir, []string{
		"task", "create",
		"--kind", "general_execution",
		"--preset", "docs_lite",
		"--title", "parent docs",
		"--objective", "prove task_create can happen before run",
		"--criterion", "docs reviewed",
	}).stdout)

	auto := runCLI(t, dir, []string{"auto", parentID, "--json"})
	if auto.exitCode != 0 {
		t.Fatalf("expected auto to reach done after task_create + run under one turn budget, got %d stderr=%s stdout=%s", auto.exitCode, auto.stderr, auto.stdout)
	}
	if decisionCalls != 2 {
		t.Fatalf("expected task_create and run decisions in a single auto pass, got %d", decisionCalls)
	}
	assertContains(t, auto.stdout, `"type":"project_task_created"`)

	status := runCLI(t, dir, []string{"status", parentID, "--json"})
	var snapshot task.StatusSnapshot
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal status snapshot: %v", err)
	}
	if snapshot.State != task.StateDone {
		t.Fatalf("expected done state after task_create + run, got %+v", snapshot)
	}

	var listed []task.TaskListEntry
	taskList := runCLI(t, dir, []string{"task", "list", "--json"})
	if err := json.Unmarshal([]byte(taskList.stdout), &listed); err != nil {
		t.Fatalf("unmarshal task list: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("expected parent plus created durable task, got %+v", listed)
	}
}

func TestAutoTaskPatchDoesNotConsumeConfiguredTurnBudget(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package demo\n\nfunc Add(a, b int) int { return a + b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package demo\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	decisionCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(bodyBytes, &body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		text, ok := body["text"].(map[string]any)
		if !ok {
			t.Fatalf("expected text body, got %#v", body["text"])
		}
		format, ok := text["format"].(map[string]any)
		if !ok {
			t.Fatalf("expected format body, got %#v", text["format"])
		}
		if format["name"] != "ngen_provider_decision" {
			t.Fatalf("unexpected schema name: %#v", format["name"])
		}
		decisionCalls++
		switch decisionCalls {
		case 1:
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"action\":\"task_patch\",\"summary\":\"Patch the existing mutable execution plan before running\",\"plan_explanation\":\"\",\"plan_steps\":[],\"plan_patch_operations\":[{\"op\":\"set_explanation\",\"explanation\":\"Refine the seeded plan before execution.\",\"step_id\":\"\",\"after_step_id\":\"\",\"step\":{\"id\":\"\",\"parent_step_id\":\"\",\"depends_on\":[],\"priority\":\"\",\"title\":\"\",\"status\":\"\",\"covers\":[],\"notes\":\"\"}},{\"op\":\"upsert_step\",\"explanation\":\"\",\"step_id\":\"\",\"after_step_id\":\"epic.repo_truth\",\"step\":{\"id\":\"verify.tests\",\"parent_step_id\":\"epic.repo_truth\",\"depends_on\":[\"epic.repo_truth\"],\"priority\":\"high\",\"title\":\"Run focused verifier and close remaining work\",\"status\":\"pending\",\"covers\":[\"SC-001\"],\"notes\":\"Use patch instead of full rewrite.\"}}],\"worker_id\":\"\",\"worker_role\":\"\",\"worker_objective\":\"\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
					}]
				}]
			}`))
		case 2:
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"action\":\"run\",\"summary\":\"Run the task now\",\"plan_explanation\":\"\",\"plan_steps\":[],\"plan_patch_operations\":[],\"worker_id\":\"\",\"worker_role\":\"\",\"worker_objective\":\"\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
					}]
				}]
			}`))
		default:
			t.Fatalf("unexpected decision call %d", decisionCalls)
		}
	}))
	defer server.Close()

	writeFile(t, filepath.Join(dir, "ngen.json"), fmt.Sprintf(`{
  "provider": {
    "mode": "responses",
    "base_url": %q,
    "model": "gpt-5.4",
    "api_key_env": "OPENAI_API_KEY",
    "auto_run_max_turns": 1
  }
}`, server.URL+"/v1"))

	taskID := strings.TrimSpace(runCLI(t, dir, []string{
		"task", "create",
		"--kind", "coding",
		"--title", "task patch budget",
		"--objective", "prove task_patch can happen before run",
		"--criterion", "go test ./... passes",
	}).stdout)

	seedPlanFile := filepath.Join(dir, "seed-plan.json")
	writeFile(t, seedPlanFile, `{
  "explanation": "Initial mutable execution plan before provider patching.",
  "steps": [
    {
      "id": "epic.repo_truth",
      "title": "Inspect repo truth",
      "status": "in_progress",
      "covers": ["SC-001"],
      "notes": ""
    }
  ]
}`)
	seed := runCLI(t, dir, []string{"task", "update", taskID, "--plan-file", seedPlanFile, "--json"})
	if seed.exitCode != 0 {
		t.Fatalf("seed task update failed: %d stderr=%s stdout=%s", seed.exitCode, seed.stderr, seed.stdout)
	}

	auto := runCLI(t, dir, []string{"auto", taskID, "--json"})
	if auto.exitCode != 0 {
		t.Fatalf("expected auto to reach done after task_patch + run under one turn budget, got %d stderr=%s stdout=%s", auto.exitCode, auto.stderr, auto.stdout)
	}
	if decisionCalls != 2 {
		t.Fatalf("expected task_patch and run decisions in a single auto pass, got %d", decisionCalls)
	}
	assertContains(t, auto.stdout, `"type":"task_plan_updated"`)
	get := runCLI(t, dir, []string{"task", "get", taskID, "--json"})
	var view task.TaskView
	if err := json.Unmarshal([]byte(get.stdout), &view); err != nil {
		t.Fatalf("unmarshal task get result: %v", err)
	}
	if view.Plan.Revision != 2 || view.Plan.Explanation != "Refine the seeded plan before execution." {
		t.Fatalf("expected patched execution plan to persist alongside system plan, got %+v", view.Plan)
	}
	if len(view.Plan.Steps) != 5 || view.Plan.Steps[2].ID != "verify.tests" {
		t.Fatalf("expected patched execution node to persist, got %+v", view.Plan.Steps)
	}
	if view.Plan.Steps[1].Status != task.StepStatusCancelled || view.Plan.Steps[2].Status != task.StepStatusCancelled {
		t.Fatalf("expected open execution steps to be cancelled once task reached done, got %+v", view.Plan.Steps)
	}
}

func TestAutoBootstrapsSystemExecutionPlanWhenRemoteProviderSkipsTaskUpdate(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package main\n\nfunc Add(a, b int) int { return a - b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")
	writeFile(t, filepath.Join(dir, "README.md"), "# Demo\n\nThis guide is still incomplete.\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	workspaceEditCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(bodyBytes, &body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		text, ok := body["text"].(map[string]any)
		if !ok {
			t.Fatalf("expected text body, got %#v", body["text"])
		}
		format, ok := text["format"].(map[string]any)
		if !ok {
			t.Fatalf("expected format body, got %#v", text["format"])
		}
		switch format["name"] {
		case "ngen_provider_decision":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"action\":\"run\",\"summary\":\"Run coding task now\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
					}]
				}]
			}`))
		case "ngen_workspace_observation":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"summary\":\"No extra observation needed\",\"commands\":[]}"
					}]
				}]
			}`))
		case "ngen_workspace_edit":
			workspaceEditCalls++
			switch workspaceEditCalls {
			case 1:
				_, _ = w.Write([]byte(`{
					"output": [{
						"type": "message",
						"role": "assistant",
						"content": [{
							"type": "output_text",
							"text": "{\"summary\":\"Fix Add first\",\"writes\":[{\"path\":\"calc.go\",\"content\":\"package main\\n\\nfunc Add(a, b int) int { return a + b }\\n\"}],\"deletes\":[]}"
						}]
					}]
				}`))
			case 2:
				_, _ = w.Write([]byte(`{
					"output": [{
						"type": "message",
						"role": "assistant",
						"content": [{
							"type": "output_text",
							"text": "{\"summary\":\"Document Add in README\",\"writes\":[{\"path\":\"README.md\",\"content\":\"# Demo\\n\\nUse Add to sum two values.\\n\"}],\"deletes\":[]}"
						}]
					}]
				}`))
			default:
				t.Fatalf("unexpected workspace edit call %d", workspaceEditCalls)
			}
		default:
			t.Fatalf("unexpected schema name: %#v", format["name"])
		}
	}))
	defer server.Close()

	writeProviderConfig(t, dir, map[string]any{
		"mode":        "responses",
		"base_url":    server.URL + "/v1",
		"model":       "gpt-5.4",
		"api_key_env": "OPENAI_API_KEY",
	})

	taskID := strings.TrimSpace(runCLI(t, dir, []string{
		"task", "create",
		"--kind", "coding",
		"--title", "system bootstrap plan",
		"--objective", "implement Add and update README.md",
		"--constraint", "Do not modify *_test.go files.",
		"--criterion", "go test ./... passes",
		"--criterion", "README.md mentions `Add`",
	}).stdout)

	auto := runCLI(t, dir, []string{"auto", taskID, "--json"})
	if auto.exitCode != 0 {
		t.Fatalf("expected auto command to complete after bootstrapping the execution plan, got %d stderr=%s stdout=%s", auto.exitCode, auto.stderr, auto.stdout)
	}
	assertContains(t, auto.stdout, `"type":"provider_decided"`)
	assertContains(t, auto.stdout, `"type":"task_plan_updated"`)

	get := runCLI(t, dir, []string{"task", "get", taskID, "--json"})
	var view task.TaskView
	if err := json.Unmarshal([]byte(get.stdout), &view); err != nil {
		t.Fatalf("unmarshal task get result: %v", err)
	}
	if strings.TrimSpace(view.Plan.Explanation) == "" {
		t.Fatalf("expected bootstrapped execution plan explanation, got %+v", view.Plan)
	}
	if view.Status.CurrentSystemStepID == "" {
		t.Fatalf("expected current system step id in task status, got %+v", view.Status)
	}
	foundExecution := false
	for _, step := range view.Plan.Steps {
		if step.Kind != task.StepKindExecution {
			continue
		}
		foundExecution = true
		if step.Source != task.StepSourceSystem {
			t.Fatalf("expected system-sourced bootstrap execution step, got %+v", step)
		}
	}
	if !foundExecution {
		t.Fatalf("expected bootstrapped execution steps in plan, got %+v", view.Plan)
	}
}

func TestOpenAIResponsesCodingTaskRepairsCriterionGapAfterVerificationPasses(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package main\n\nfunc Add(a, b int) int { return a - b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")
	writeFile(t, filepath.Join(dir, "README.md"), "# Demo\n\nThis guide is still incomplete.\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	workspaceEditCalls := 0
	sawCriteriaRepair := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(bodyBytes, &body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		text, ok := body["text"].(map[string]any)
		if !ok {
			t.Fatalf("expected text body, got %#v", body["text"])
		}
		format, ok := text["format"].(map[string]any)
		if !ok {
			t.Fatalf("expected format body, got %#v", text["format"])
		}
		switch format["name"] {
		case "ngen_provider_decision":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"action\":\"run\",\"summary\":\"Run coding task now\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
					}]
				}]
			}`))
		case "ngen_workspace_observation":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"summary\":\"No extra observation needed\",\"commands\":[]}"
					}]
				}]
			}`))
		case "ngen_workspace_edit":
			workspaceEditCalls++
			if workspaceEditCalls == 2 && strings.Contains(string(bodyBytes), "Open success criteria remain") {
				sawCriteriaRepair = true
			}
			switch workspaceEditCalls {
			case 1:
				_, _ = w.Write([]byte(`{
					"output": [{
						"type": "message",
						"role": "assistant",
						"content": [{
							"type": "output_text",
							"text": "{\"summary\":\"Fix Add first\",\"writes\":[{\"path\":\"calc.go\",\"content\":\"package main\\n\\nfunc Add(a, b int) int { return a + b }\\n\"}],\"deletes\":[]}"
						}]
					}]
				}`))
			case 2:
				_, _ = w.Write([]byte(`{
					"output": [{
						"type": "message",
						"role": "assistant",
						"content": [{
							"type": "output_text",
							"text": "{\"summary\":\"Document Add in README\",\"writes\":[{\"path\":\"README.md\",\"content\":\"# Demo\\n\\nUse Add to sum two values.\\n\"}],\"deletes\":[]}"
						}]
					}]
				}`))
			default:
				t.Fatalf("unexpected workspace edit call %d", workspaceEditCalls)
			}
		default:
			t.Fatalf("unexpected schema name: %#v", format["name"])
		}
	}))
	defer server.Close()

	writeProviderConfig(t, dir, map[string]any{
		"mode":        "responses",
		"base_url":    server.URL + "/v1",
		"model":       "gpt-5.4",
		"api_key_env": "OPENAI_API_KEY",
	})

	taskID := strings.TrimSpace(runCLI(t, dir, []string{
		"task", "create",
		"--kind", "coding",
		"--title", "criteria repair",
		"--objective", "implement Add and update README.md",
		"--constraint", "Do not modify *_test.go files.",
		"--criterion", "go test ./... passes",
		"--criterion", "README.md mentions `Add`",
	}).stdout)
	auto := runCLI(t, dir, []string{"auto", taskID, "--json"})
	if auto.exitCode != 0 {
		t.Fatalf("expected criteria-aware repair to finish Done, got %d stderr=%s stdout=%s", auto.exitCode, auto.stderr, auto.stdout)
	}
	if workspaceEditCalls != 2 {
		t.Fatalf("expected two workspace edit calls, got %d", workspaceEditCalls)
	}
	if !sawCriteriaRepair {
		t.Fatalf("expected second workspace edit call to be driven by criteria gap")
	}

	status := runCLI(t, dir, []string{"status", taskID, "--json"})
	var snapshot task.StatusSnapshot
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal status snapshot: %v", err)
	}
	if snapshot.State != task.StateDone {
		t.Fatalf("expected done state after criteria-aware repair, got %+v", snapshot)
	}

	assertContains(t, readFile(t, filepath.Join(dir, "README.md")), "Use Add to sum two values.")
	criteria := readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "criteria", "latest.json"))
	assertContains(t, criteria, `"criterion_id": "SC-002"`)
	assertContains(t, criteria, `workspace_edits.jsonl#edit_record_id=`)
	assertContains(t, readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "workspace_edits.jsonl")), `"path":"README.md"`)
	var plan task.Plan
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "plan.json"))), &plan); err != nil {
		t.Fatalf("unmarshal plan: %v", err)
	}
	foundExecution := false
	for _, step := range plan.Steps {
		if step.Kind == task.StepKindExecution {
			foundExecution = true
			if step.Status != task.StepStatusCompleted && step.Status != task.StepStatusCancelled {
				t.Fatalf("expected execution steps to settle after successful criteria repair, got %+v", plan.Steps)
			}
			continue
		}
		if step.Status != task.StepStatusCompleted {
			t.Fatalf("expected every system plan step completed after successful criteria repair, got %+v", plan.Steps)
		}
	}
	if !foundExecution {
		t.Fatalf("expected bootstrapped execution plan alongside system lane, got %+v", plan.Steps)
	}
	progress := readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "progress.md"))
	assertContains(t, progress, "STEP-004 (Review evidence, refresh handoff, and close the task)")
	handoff := readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "handoff.md"))
	assertContains(t, handoff, "STEP-004 (Review evidence, refresh handoff, and close the task)")
}

func TestOpenAIResponsesCodingTaskRepairsSemanticCriteriaWithoutPathHints(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "timeout.go"), "package main\n\nfunc DefaultTimeoutSeconds() int { return 0 }\n")
	writeFile(t, filepath.Join(dir, "timeout_test.go"), "package main\n\nimport \"testing\"\n\nfunc TestDefaultTimeoutSeconds(t *testing.T) {\n\tif got := DefaultTimeoutSeconds(); got != 15 {\n\t\tt.Fatalf(\"expected 15, got %d\", got)\n\t}\n}\n")
	writeFile(t, filepath.Join(dir, "config.sample.json"), "{\n  \"port\": 8080\n}\n")
	writeFile(t, filepath.Join(dir, "docs", "config.md"), "# Config\n\nPending timeout docs.\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	workspaceEditCalls := 0
	sawSemanticCriteriaRepair := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(bodyBytes, &body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		text, ok := body["text"].(map[string]any)
		if !ok {
			t.Fatalf("expected text body, got %#v", body["text"])
		}
		format, ok := text["format"].(map[string]any)
		if !ok {
			t.Fatalf("expected format body, got %#v", text["format"])
		}
		promptText := ""
		if inputItems, ok := body["input"].([]any); ok && len(inputItems) > 0 {
			if message, ok := inputItems[0].(map[string]any); ok {
				if content, ok := message["content"].([]any); ok && len(content) > 0 {
					if item, ok := content[0].(map[string]any); ok {
						promptText, _ = item["text"].(string)
					}
				}
			}
		}
		switch format["name"] {
		case "ngen_provider_decision":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"action\":\"run\",\"summary\":\"Run coding task now\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
					}]
				}]
			}`))
		case "ngen_workspace_observation":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"summary\":\"No extra observation needed\",\"commands\":[]}"
					}]
				}]
			}`))
		case "ngen_workspace_edit":
			workspaceEditCalls++
			if workspaceEditCalls == 2 &&
				strings.Contains(promptText, "\"open_criteria\"") &&
				strings.Contains(promptText, "sample config mentions `timeout_seconds`") &&
				strings.Contains(promptText, "guide mentions `timeout_seconds`") {
				sawSemanticCriteriaRepair = true
			}
			switch workspaceEditCalls {
			case 1:
				_, _ = w.Write([]byte(`{
					"output": [{
						"type": "message",
						"role": "assistant",
						"content": [{
							"type": "output_text",
							"text": "{\"summary\":\"Fix timeout default first\",\"writes\":[{\"path\":\"timeout.go\",\"content\":\"package main\\n\\nfunc DefaultTimeoutSeconds() int { return 15 }\\n\"}],\"deletes\":[]}"
						}]
					}]
				}`))
			case 2:
				_, _ = w.Write([]byte(`{
					"output": [{
						"type": "message",
						"role": "assistant",
						"content": [{
							"type": "output_text",
							"text": "{\"summary\":\"Document timeout_seconds in config artifacts\",\"writes\":[{\"path\":\"config.sample.json\",\"content\":\"{\\n  \\\"timeout_seconds\\\": 15,\\n  \\\"port\\\": 8080\\n}\\n\"},{\"path\":\"docs/config.md\",\"content\":\"# Config\\n\\nUse timeout_seconds to control the default timeout.\\n\"}],\"deletes\":[]}"
						}]
					}]
				}`))
			default:
				t.Fatalf("unexpected workspace edit call %d", workspaceEditCalls)
			}
		default:
			t.Fatalf("unexpected schema name: %#v", format["name"])
		}
	}))
	defer server.Close()

	writeProviderConfig(t, dir, map[string]any{
		"mode":        "responses",
		"base_url":    server.URL + "/v1",
		"model":       "gpt-5.4",
		"api_key_env": "OPENAI_API_KEY",
	})

	taskID := strings.TrimSpace(runCLI(t, dir, []string{
		"task", "create",
		"--kind", "coding",
		"--title", "semantic criteria repair",
		"--objective", "fix timeout defaults and document timeout_seconds",
		"--constraint", "Do not modify *_test.go files.",
		"--criterion", "go test ./... passes",
		"--criterion", "sample config mentions `timeout_seconds`",
		"--criterion", "guide mentions `timeout_seconds`",
	}).stdout)
	auto := runCLI(t, dir, []string{"auto", taskID, "--json"})
	if auto.exitCode != 0 {
		t.Fatalf("expected semantic-criteria repair to finish Done, got %d stderr=%s stdout=%s", auto.exitCode, auto.stderr, auto.stdout)
	}
	if workspaceEditCalls != 2 {
		t.Fatalf("expected two workspace edit calls, got %d", workspaceEditCalls)
	}
	if !sawSemanticCriteriaRepair {
		t.Fatalf("expected second workspace edit call to carry open semantic criteria context")
	}

	status := runCLI(t, dir, []string{"status", taskID, "--json"})
	var snapshot task.StatusSnapshot
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal status snapshot: %v", err)
	}
	if snapshot.State != task.StateDone {
		t.Fatalf("expected done state after semantic-criteria repair, got %+v", snapshot)
	}

	assertContains(t, readFile(t, filepath.Join(dir, "config.sample.json")), "timeout_seconds")
	assertContains(t, readFile(t, filepath.Join(dir, "docs", "config.md")), "timeout_seconds")
	criteria := readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "criteria", "latest.json"))
	assertContains(t, criteria, `"criterion_id": "SC-002"`)
	assertContains(t, criteria, `"criterion_id": "SC-003"`)
	assertContains(t, criteria, `workspace_edits.jsonl#edit_record_id=`)
	workspaceEdits := readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "workspace_edits.jsonl"))
	assertContains(t, workspaceEdits, `"path":"config.sample.json"`)
	assertContains(t, workspaceEdits, `"path":"docs/config.md"`)
}

func TestOpenAIResponsesCodingTaskUsesMultipleRepairAttempts(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package main\n\nfunc Add(a, b int) int { return a - b }\n\nfunc Multiply(a, b int) int { return a + b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n\nfunc TestMultiply(t *testing.T) {\n\tif got := Multiply(3, 4); got != 12 {\n\t\tt.Fatalf(\"expected 12, got %d\", got)\n\t}\n}\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	workspaceEditCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		text, ok := body["text"].(map[string]any)
		if !ok {
			t.Fatalf("expected text body, got %#v", body["text"])
		}
		format, ok := text["format"].(map[string]any)
		if !ok {
			t.Fatalf("expected format body, got %#v", text["format"])
		}
		switch format["name"] {
		case "ngen_provider_decision":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"action\":\"run\",\"summary\":\"Run coding task now\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
					}]
				}]
			}`))
		case "ngen_workspace_observation":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"summary\":\"No extra observation needed\",\"commands\":[]}"
					}]
				}]
			}`))
		case "ngen_workspace_edit":
			workspaceEditCalls++
			switch workspaceEditCalls {
			case 1:
				_, _ = w.Write([]byte(`{
					"output": [{
						"type": "message",
						"role": "assistant",
						"content": [{
							"type": "output_text",
							"text": "{\"summary\":\"Fix Add first\",\"writes\":[{\"path\":\"calc.go\",\"content\":\"package main\\n\\nfunc Add(a, b int) int { return a + b }\\n\\nfunc Multiply(a, b int) int { return a + b }\\n\"}],\"deletes\":[]}"
						}]
					}]
				}`))
			case 2:
				_, _ = w.Write([]byte(`{
					"output": [{
						"type": "message",
						"role": "assistant",
						"content": [{
							"type": "output_text",
							"text": "{\"summary\":\"Fix Multiply next\",\"writes\":[{\"path\":\"calc.go\",\"content\":\"package main\\n\\nfunc Add(a, b int) int { return a + b }\\n\\nfunc Multiply(a, b int) int { return a * b }\\n\"}],\"deletes\":[]}"
						}]
					}]
				}`))
			default:
				t.Fatalf("unexpected workspace edit call %d", workspaceEditCalls)
			}
		default:
			t.Fatalf("unexpected schema name: %#v", format["name"])
		}
	}))
	defer server.Close()

	writeProviderConfig(t, dir, map[string]any{
		"mode":        "responses",
		"base_url":    server.URL + "/v1",
		"model":       "gpt-5.4",
		"api_key_env": "OPENAI_API_KEY",
	})

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "coding", "--title", "repair twice", "--objective", "fix Add and Multiply", "--constraint", "Do not modify *_test.go files.", "--criterion", "go test passes"}).stdout)
	auto := runCLI(t, dir, []string{"auto", taskID, "--json"})
	if auto.exitCode != 0 {
		t.Fatalf("expected multi-attempt coding auto repair to finish, got %d stderr=%s stdout=%s", auto.exitCode, auto.stderr, auto.stdout)
	}
	if workspaceEditCalls != 2 {
		t.Fatalf("expected two workspace edit calls, got %d", workspaceEditCalls)
	}
	if got := strings.Count(auto.stdout, "\"workspace_edit_applied\""); got != 2 {
		t.Fatalf("expected two workspace_edit_applied events, got %d stdout=%s", got, auto.stdout)
	}

	status := runCLI(t, dir, []string{"status", taskID, "--json"})
	var snapshot task.StatusSnapshot
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal status snapshot: %v", err)
	}
	if snapshot.State != task.StateDone {
		t.Fatalf("expected done state after multi-attempt repair, got %s", snapshot.State)
	}

	edited := readFile(t, filepath.Join(dir, "calc.go"))
	assertContains(t, edited, "func Add(a, b int) int { return a + b }")
	assertContains(t, edited, "func Multiply(a, b int) int { return a * b }")
	workspaceEdits := readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "workspace_edits.jsonl"))
	if got := strings.Count(workspaceEdits, "\"status\":\"applied\""); got != 2 {
		t.Fatalf("expected two applied workspace edit records, got %d file=%s", got, workspaceEdits)
	}
}

func TestBuiltinCodingTaskUsesMultipleRepairAttempts(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package main\n\nfunc Add(a, b int) int { return a - b }\n\nfunc Multiply(a, b int) int { return a + b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n\nfunc TestMultiply(t *testing.T) {\n\tif got := Multiply(3, 4); got != 12 {\n\t\tt.Fatalf(\"expected 12, got %d\", got)\n\t}\n}\n")

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "coding", "--title", "builtin repair twice", "--objective", "fix Add and Multiply", "--constraint", "Do not modify *_test.go files.", "--criterion", "go test passes"}).stdout)
	auto := runCLI(t, dir, []string{"auto", taskID, "--json"})
	if auto.exitCode != 0 {
		t.Fatalf("expected builtin coding auto repair to finish, got %d stderr=%s stdout=%s", auto.exitCode, auto.stderr, auto.stdout)
	}
	if got := strings.Count(auto.stdout, "\"workspace_edit_applied\""); got != 2 {
		t.Fatalf("expected two builtin workspace_edit_applied events, got %d stdout=%s", got, auto.stdout)
	}

	status := runCLI(t, dir, []string{"status", taskID, "--json"})
	var snapshot task.StatusSnapshot
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal status snapshot: %v", err)
	}
	if snapshot.State != task.StateDone {
		t.Fatalf("expected done state after builtin repair, got %s", snapshot.State)
	}
	assertContains(t, readFile(t, filepath.Join(dir, "calc.go")), "func Add(a, b int) int")
	assertContains(t, readFile(t, filepath.Join(dir, "calc.go")), "return a + b")
	assertContains(t, readFile(t, filepath.Join(dir, "calc.go")), "func Multiply(a, b int) int")
	assertContains(t, readFile(t, filepath.Join(dir, "calc.go")), "return a * b")
	workspaceEdits := readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "workspace_edits.jsonl"))
	assertContains(t, workspaceEdits, "\"provider_mode\":\"builtin\"")
	if got := strings.Count(workspaceEdits, "\"status\":\"applied\""); got != 2 {
		t.Fatalf("expected two applied builtin workspace edit records, got %d file=%s", got, workspaceEdits)
	}
}

func TestOpenAIResponsesCodingTaskContinuesAfterPatchApplyFailure(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package main\n\nfunc Add(a, b int) int { return a - b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	workspaceEditCalls := 0
	sawPreviousFailures := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(bodyBytes, &body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		text, ok := body["text"].(map[string]any)
		if !ok {
			t.Fatalf("expected text body, got %#v", body["text"])
		}
		format, ok := text["format"].(map[string]any)
		if !ok {
			t.Fatalf("expected format body, got %#v", text["format"])
		}
		promptText := ""
		if inputItems, ok := body["input"].([]any); ok && len(inputItems) > 0 {
			if message, ok := inputItems[0].(map[string]any); ok {
				if content, ok := message["content"].([]any); ok && len(content) > 0 {
					if item, ok := content[0].(map[string]any); ok {
						promptText, _ = item["text"].(string)
					}
				}
			}
		}
		switch format["name"] {
		case "ngen_provider_decision":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"action\":\"run\",\"summary\":\"Run coding task now\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
					}]
				}]
			}`))
		case "ngen_workspace_observation":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"summary\":\"No extra observation needed\",\"commands\":[]}"
					}]
				}]
			}`))
		case "ngen_workspace_edit":
			workspaceEditCalls++
			switch workspaceEditCalls {
			case 1:
				_, _ = w.Write([]byte(`{
					"output": [{
						"type": "message",
						"role": "assistant",
						"content": [{
							"type": "output_text",
							"text": "{\"summary\":\"Try a patch first\",\"patch\":\"*** Begin Patch\\n*** Update File: calc.go\\n@@\\n-func Add(a, b int) int { return a * b }\\n+func Add(a, b int) int { return a + b }\\n*** End Patch\",\"writes\":[],\"deletes\":[]}"
						}]
					}]
				}`))
			case 2:
				if strings.Contains(promptText, "\"previous_failures\"") &&
					strings.Contains(promptText, "patch hunk context not found") &&
					strings.Contains(promptText, "\"attempt\": 1") {
					sawPreviousFailures = true
				}
				_, _ = w.Write([]byte(`{
					"output": [{
						"type": "message",
						"role": "assistant",
						"content": [{
							"type": "output_text",
							"text": "{\"summary\":\"Fall back to a direct write after the failed patch\",\"patch\":\"\",\"writes\":[{\"path\":\"calc.go\",\"content\":\"package main\\n\\nfunc Add(a, b int) int { return a + b }\\n\"}],\"deletes\":[]}"
						}]
					}]
				}`))
			default:
				t.Fatalf("unexpected workspace edit call %d prompt=%s body=%s", workspaceEditCalls, promptText, string(bodyBytes))
			}
		default:
			t.Fatalf("unexpected schema name: %#v", format["name"])
		}
	}))
	defer server.Close()

	writeProviderConfig(t, dir, map[string]any{
		"mode":        "responses",
		"base_url":    server.URL + "/v1",
		"model":       "gpt-5.4",
		"api_key_env": "OPENAI_API_KEY",
	})

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "coding", "--title", "continue after patch failure", "--objective", "fix Add", "--constraint", "Do not modify *_test.go files.", "--criterion", "go test passes"}).stdout)
	auto := runCLI(t, dir, []string{"auto", taskID, "--json"})
	if auto.exitCode != 0 {
		t.Fatalf("expected auto to recover from patch apply failure, got %d stderr=%s stdout=%s", auto.exitCode, auto.stderr, auto.stdout)
	}
	if workspaceEditCalls != 2 {
		t.Fatalf("expected two workspace edit calls in a single auto run, got %d", workspaceEditCalls)
	}
	if !sawPreviousFailures {
		t.Fatalf("expected second workspace edit prompt to include previous_failures context")
	}
	assertContains(t, auto.stdout, "\"workspace_edit_failed\"")
	assertContains(t, auto.stdout, "\"workspace_edit_applied\"")

	status := runCLI(t, dir, []string{"status", taskID, "--json"})
	var snapshot task.StatusSnapshot
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal status snapshot: %v", err)
	}
	if snapshot.State != task.StateDone {
		t.Fatalf("expected done state after recovering from patch failure, got %s", snapshot.State)
	}

	assertContains(t, readFile(t, filepath.Join(dir, "calc.go")), "return a + b")
	workspaceEdits := readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "workspace_edits.jsonl"))
	if got := strings.Count(workspaceEdits, "\"status\":\"failed\""); got != 1 {
		t.Fatalf("expected one failed workspace edit record, got %d file=%s", got, workspaceEdits)
	}
	if got := strings.Count(workspaceEdits, "\"status\":\"applied\""); got != 1 {
		t.Fatalf("expected one applied workspace edit record, got %d file=%s", got, workspaceEdits)
	}
	assertContains(t, workspaceEdits, "patch hunk context not found")
}

func TestOpenAIResponsesCodingTaskRunsRepairCommandAfterWrite(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package main\n\nfunc Add(a, b int) int { return a - b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		text, ok := body["text"].(map[string]any)
		if !ok {
			t.Fatalf("expected text body, got %#v", body["text"])
		}
		format, ok := text["format"].(map[string]any)
		if !ok {
			t.Fatalf("expected format body, got %#v", text["format"])
		}
		switch format["name"] {
		case "ngen_provider_decision":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"action\":\"run\",\"summary\":\"Run coding task now\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
					}]
				}]
			}`))
		case "ngen_workspace_observation":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"summary\":\"No extra observation needed\",\"commands\":[]}"
					}]
				}]
			}`))
		case "ngen_workspace_edit":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"summary\":\"Rewrite Add and run gofmt\",\"patch\":\"\",\"writes\":[{\"path\":\"calc.go\",\"content\":\"package main\\n\\nfunc Add(a,b int)int{return a+b}\\n\"}],\"deletes\":[],\"commands\":[{\"phase\":\"post\",\"argv\":[\"gofmt\",\"-w\",\"calc.go\"],\"reason\":\"Format calc.go after rewriting it\"}]}"
					}]
				}]
			}`))
		default:
			t.Fatalf("unexpected schema name: %#v", format["name"])
		}
	}))
	defer server.Close()

	writeProviderConfig(t, dir, map[string]any{
		"mode":                            "responses",
		"base_url":                        server.URL + "/v1",
		"model":                           "gpt-5.4",
		"api_key_env":                     "OPENAI_API_KEY",
		"coding_execution_command_budget": 2,
		"coding_execution_command_timeout_seconds": 30,
	})

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "coding", "--title", "repair with post command", "--objective", "fix Add", "--constraint", "Do not modify *_test.go files.", "--criterion", "go test passes"}).stdout)
	auto := runCLI(t, dir, []string{"auto", taskID, "--json"})
	if auto.exitCode != 0 {
		t.Fatalf("expected auto to finish after running a repair command, got %d stderr=%s stdout=%s", auto.exitCode, auto.stderr, auto.stdout)
	}
	assertContains(t, auto.stdout, "\"repair_command_completed\"")
	assertContains(t, auto.stdout, "\"workspace_edit_applied\"")

	status := runCLI(t, dir, []string{"status", taskID, "--json"})
	var snapshot task.StatusSnapshot
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal status snapshot: %v", err)
	}
	if snapshot.State != task.StateDone {
		t.Fatalf("expected done state after repair command run, got %s", snapshot.State)
	}

	if got := readFile(t, filepath.Join(dir, "calc.go")); !strings.Contains(got, "func Add(a, b int) int { return a + b }") {
		t.Fatalf("expected calc.go to be gofmt-formatted after repair command, got %q", got)
	}
	commandRuns := readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "command_runs.jsonl"))
	assertContains(t, commandRuns, "\"kind\":\"repair_command\"")
	assertContains(t, commandRuns, "\"argv\":[\"gofmt\",\"-w\",\"calc.go\"]")
	assertContains(t, commandRuns, "\"permission_mode_id\":\"standard\"")
	assertContains(t, commandRuns, "\"policy_decision\":\"allow\"")
	assertContains(t, commandRuns, "\"status\":\"completed\"")
}

func TestOpenAIResponsesCodingTaskRejectsTestFileMutationPlan(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package main\n\n// Add should sum two numbers.\n")
	originalTest := "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n"
	writeFile(t, filepath.Join(dir, "calc_test.go"), originalTest)
	t.Setenv("OPENAI_API_KEY", "test-key")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		text, ok := body["text"].(map[string]any)
		if !ok {
			t.Fatalf("expected text body, got %#v", body["text"])
		}
		format, ok := text["format"].(map[string]any)
		if !ok {
			t.Fatalf("expected format body, got %#v", text["format"])
		}
		switch format["name"] {
		case "ngen_provider_decision":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"action\":\"run\",\"summary\":\"Run coding task now\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
					}]
				}]
			}`))
		case "ngen_workspace_observation":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"summary\":\"No extra observation needed\",\"commands\":[]}"
					}]
				}]
			}`))
		case "ngen_workspace_edit":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"summary\":\"Cheat by editing the test\",\"writes\":[{\"path\":\"calc_test.go\",\"content\":\"package main\\n\\nimport \\\"testing\\\"\\n\\nfunc TestAdd(t *testing.T) {}\\n\"}],\"deletes\":[]}"
					}]
				}]
			}`))
		default:
			t.Fatalf("unexpected schema name: %#v", format["name"])
		}
	}))
	defer server.Close()

	writeProviderConfig(t, dir, map[string]any{
		"mode":        "responses",
		"base_url":    server.URL + "/v1",
		"model":       "gpt-5.4",
		"api_key_env": "OPENAI_API_KEY",
	})

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "coding", "--title", "reject test mutation", "--objective", "implement Add", "--constraint", "Do not modify *_test.go files.", "--criterion", "go test passes"}).stdout)
	auto := runCLI(t, dir, []string{"auto", taskID, "--json"})
	if auto.exitCode != 11 {
		t.Fatalf("expected failed exit code after rejected test-file plan, got %d stderr=%s stdout=%s", auto.exitCode, auto.stderr, auto.stdout)
	}
	assertContains(t, auto.stdout, "\"workspace_edit_failed\"")

	status := runCLI(t, dir, []string{"status", taskID, "--json"})
	var snapshot task.StatusSnapshot
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal status snapshot: %v", err)
	}
	if snapshot.State != task.StateFailed || snapshot.StatusReasonCode != "failed_verification" {
		t.Fatalf("expected failed_verification after rejected plan, got state=%s reason=%s", snapshot.State, snapshot.StatusReasonCode)
	}

	if got := readFile(t, filepath.Join(dir, "calc_test.go")); got != originalTest {
		t.Fatalf("expected test file to remain unchanged, got %q", got)
	}
	workspaceEdits := readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "workspace_edits.jsonl"))
	assertContains(t, workspaceEdits, "\"status\":\"failed\"")
	assertContains(t, workspaceEdits, "violates task constraint")

	var quality task.QualityDiagnostic
	qualityPath := filepath.Join(dir, ".ngen", "tasks", taskID, "diagnostics", "quality-latest.json")
	if err := json.Unmarshal([]byte(readFile(t, qualityPath)), &quality); err != nil {
		t.Fatalf("unmarshal quality diagnostic: %v", err)
	}
	if quality.Status != "blocking" || !quality.BlockCompletion || !contains(quality.TestFileChanges, "calc_test.go") {
		t.Fatalf("expected blocking test-file quality diagnostic, got %+v", quality)
	}
	if len(quality.Findings) == 0 || quality.Findings[0].Category != "confirmed_defect" {
		t.Fatalf("expected confirmed_defect quality finding, got %+v", quality.Findings)
	}
	assertContains(t, readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "progress.md")), "## Quality Diagnostics")
}

func TestOpenAIResponsesCodingTaskUsesObservationCommandAndPatchPlan(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package main\n\nfunc Add(a, b int) int { return a - b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		text, ok := body["text"].(map[string]any)
		if !ok {
			t.Fatalf("expected text body, got %#v", body["text"])
		}
		format, ok := text["format"].(map[string]any)
		if !ok {
			t.Fatalf("expected format body, got %#v", text["format"])
		}
		switch format["name"] {
		case "ngen_provider_decision":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"action\":\"run\",\"summary\":\"Run coding task now\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
					}]
				}]
			}`))
		case "ngen_workspace_observation":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"summary\":\"Inspect the broken function\",\"commands\":[{\"argv\":[\"sed\",\"-n\",\"1,40p\",\"calc.go\"],\"reason\":\"Read the current Add implementation\"}]}"
					}]
				}]
			}`))
		case "ngen_workspace_edit":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"summary\":\"Patch Add in place\",\"patch\":\"*** Begin Patch\\n*** Update File: calc.go\\n@@\\n-func Add(a, b int) int { return a - b }\\n+func Add(a, b int) int { return a + b }\\n*** End Patch\",\"writes\":[],\"deletes\":[]}"
					}]
				}]
			}`))
		default:
			t.Fatalf("unexpected schema name: %#v", format["name"])
		}
	}))
	defer server.Close()

	writeProviderConfig(t, dir, map[string]any{
		"mode":        "responses",
		"base_url":    server.URL + "/v1",
		"model":       "gpt-5.4",
		"api_key_env": "OPENAI_API_KEY",
	})

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "coding", "--title", "observation patch", "--objective", "fix Add", "--constraint", "Do not modify *_test.go files.", "--criterion", "go test passes"}).stdout)
	auto := runCLI(t, dir, []string{"auto", taskID, "--json"})
	if auto.exitCode != 0 {
		t.Fatalf("expected coding auto observation+patch flow to finish, got %d stderr=%s stdout=%s", auto.exitCode, auto.stderr, auto.stdout)
	}
	assertContains(t, auto.stdout, "\"observation_command_completed\"")
	assertContains(t, auto.stdout, "\"workspace_edit_applied\"")

	commandRuns := readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "command_runs.jsonl"))
	assertContains(t, commandRuns, "\"status\":\"completed\"")
	assertContains(t, commandRuns, "\"argv\":[\"sed\",\"-n\",\"1,40p\",\"calc.go\"]")
	assertContains(t, commandRuns, "commands/CMD-")
	assertContains(t, readFile(t, filepath.Join(dir, "calc.go")), "return a + b")
}

func TestOpenAICompatibleCodingTaskUsesObservationAndPatchPlan(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package main\n\nfunc Add(a, b int) int { return a - b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		toolChoice, ok := body["tool_choice"].(map[string]any)
		if !ok {
			t.Fatalf("expected tool_choice body, got %#v", body["tool_choice"])
		}
		function, ok := toolChoice["function"].(map[string]any)
		if !ok {
			t.Fatalf("expected tool_choice.function body, got %#v", toolChoice["function"])
		}
		switch function["name"] {
		case "submit_decision":
			_, _ = w.Write([]byte(`{
				"choices": [{
					"message": {
						"tool_calls": [{
							"function": {
								"name": "submit_decision",
								"arguments": "{\"action\":\"run\",\"summary\":\"Run coding task now\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
							}
						}]
					}
				}]
			}`))
		case "submit_workspace_observation":
			_, _ = w.Write([]byte(`{
				"choices": [{
					"message": {
						"tool_calls": [{
							"function": {
								"name": "submit_workspace_observation",
								"arguments": "{\"summary\":\"Inspect the broken function\",\"commands\":[{\"argv\":[\"sed\",\"-n\",\"1,40p\",\"calc.go\"],\"reason\":\"Read the current Add implementation\"}]}"
							}
						}]
					}
				}]
			}`))
		case "submit_workspace_edit":
			_, _ = w.Write([]byte(`{
				"choices": [{
					"message": {
						"tool_calls": [{
							"function": {
								"name": "submit_workspace_edit",
								"arguments": "{\"summary\":\"Patch Add in place\",\"patch\":\"*** Begin Patch\\n*** Update File: calc.go\\n@@\\n-func Add(a, b int) int { return a - b }\\n+func Add(a, b int) int { return a + b }\\n*** End Patch\",\"writes\":[],\"deletes\":[]}"
							}
						}]
					}
				}]
			}`))
		default:
			t.Fatalf("unexpected tool choice name: %#v", function["name"])
		}
	}))
	defer server.Close()

	writeProviderConfig(t, dir, map[string]any{
		"mode":        "openai-comp",
		"base_url":    server.URL + "/v1",
		"model":       "gpt-5.4",
		"api_key_env": "OPENAI_API_KEY",
	})

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "coding", "--title", "openai-comp observation patch", "--objective", "fix Add", "--constraint", "Do not modify *_test.go files.", "--criterion", "go test passes"}).stdout)
	auto := runCLI(t, dir, []string{"auto", taskID, "--json"})
	if auto.exitCode != 0 {
		t.Fatalf("expected openai-comp coding auto flow to finish, got %d stderr=%s stdout=%s", auto.exitCode, auto.stderr, auto.stdout)
	}
	assertContains(t, auto.stdout, "\"observation_command_completed\"")
	assertContains(t, auto.stdout, "\"workspace_edit_applied\"")
	assertContains(t, readFile(t, filepath.Join(dir, "calc.go")), "return a + b")
}

func TestAnthropicCodingTaskRepairsWorkspace(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package main\n\nfunc Add(a, b int) int { return a - b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		toolChoice, ok := body["tool_choice"].(map[string]any)
		if !ok {
			t.Fatalf("expected tool_choice body, got %#v", body["tool_choice"])
		}
		switch toolChoice["name"] {
		case "submit_decision":
			_, _ = w.Write([]byte(`{
				"content": [{
					"type": "tool_use",
					"name": "submit_decision",
					"input": {
						"action": "run",
						"summary": "Run coding task now",
						"watch_interval": "",
						"watch_reason": "",
						"approval_scope": "",
						"approval_reason": ""
					}
				}]
			}`))
		case "submit_workspace_observation":
			_, _ = w.Write([]byte(`{
				"content": [{
					"type": "tool_use",
					"name": "submit_workspace_observation",
					"input": {
						"summary": "No extra observation needed",
						"commands": []
					}
				}]
			}`))
		case "submit_workspace_edit":
			_, _ = w.Write([]byte(`{
				"content": [{
					"type": "tool_use",
					"name": "submit_workspace_edit",
					"input": {
						"summary": "Implement Add in calc.go",
						"patch": "",
						"writes": [{
							"path": "calc.go",
							"content": "package main\n\nfunc Add(a, b int) int { return a + b }\n"
						}],
						"deletes": []
					}
				}]
			}`))
		default:
			t.Fatalf("unexpected tool choice name: %#v", toolChoice["name"])
		}
	}))
	defer server.Close()

	writeProviderConfig(t, dir, map[string]any{
		"mode":        "anthropic",
		"base_url":    server.URL + "/v1",
		"model":       "gpt-5.4",
		"api_key_env": "OPENAI_API_KEY",
	})

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "coding", "--title", "anthropic repair add", "--objective", "implement Add", "--constraint", "Do not modify *_test.go files.", "--criterion", "go test passes"}).stdout)
	auto := runCLI(t, dir, []string{"auto", taskID, "--json"})
	if auto.exitCode != 0 {
		t.Fatalf("expected anthropic coding auto flow to finish, got %d stderr=%s stdout=%s", auto.exitCode, auto.stderr, auto.stdout)
	}
	assertContains(t, auto.stdout, "\"workspace_edit_applied\"")
	assertContains(t, readFile(t, filepath.Join(dir, "calc.go")), "return a + b")
	assertContains(t, readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "workspace_edits.jsonl")), "\"provider_mode\":\"anthropic\"")
}

func TestObservationCommandRejectsPathOutsideWorkspace(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package main\n\nfunc Add(a, b int) int { return a - b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		text, ok := body["text"].(map[string]any)
		if !ok {
			t.Fatalf("expected text body, got %#v", body["text"])
		}
		format, ok := text["format"].(map[string]any)
		if !ok {
			t.Fatalf("expected format body, got %#v", text["format"])
		}
		switch format["name"] {
		case "ngen_provider_decision":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"action\":\"run\",\"summary\":\"Run coding task now\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
					}]
				}]
			}`))
		case "ngen_workspace_observation":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"summary\":\"Attempt an external read\",\"commands\":[{\"argv\":[\"cat\",\"/etc/passwd\"],\"reason\":\"Try to inspect a file outside workspace\"}]}"
					}]
				}]
			}`))
		case "ngen_workspace_edit":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"summary\":\"Fix Add after rejected observation\",\"patch\":\"*** Begin Patch\\n*** Update File: calc.go\\n@@\\n-func Add(a, b int) int { return a - b }\\n+func Add(a, b int) int { return a + b }\\n*** End Patch\",\"writes\":[],\"deletes\":[]}"
					}]
				}]
			}`))
		default:
			t.Fatalf("unexpected schema name: %#v", format["name"])
		}
	}))
	defer server.Close()

	writeProviderConfig(t, dir, map[string]any{
		"mode":        "responses",
		"base_url":    server.URL + "/v1",
		"model":       "gpt-5.4",
		"api_key_env": "OPENAI_API_KEY",
	})

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "coding", "--title", "reject external observation path", "--objective", "fix Add", "--constraint", "Do not modify *_test.go files.", "--criterion", "go test passes"}).stdout)
	auto := runCLI(t, dir, []string{"auto", taskID, "--json"})
	if auto.exitCode != 0 {
		t.Fatalf("expected coding auto flow to finish after rejecting external observation path, got %d stderr=%s stdout=%s", auto.exitCode, auto.stderr, auto.stdout)
	}
	commandRuns := readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "command_runs.jsonl"))
	assertContains(t, commandRuns, "\"status\":\"failed\"")
	assertContains(t, commandRuns, "escapes workspace")
	assertContains(t, readFile(t, filepath.Join(dir, "calc.go")), "return a + b")
}

func TestOpenAIResponsesCodingTaskRejectsHiddenObservationPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package main\n\nfunc Add(a, b int) int { return a - b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		text, ok := body["text"].(map[string]any)
		if !ok {
			t.Fatalf("expected text body, got %#v", body["text"])
		}
		format, ok := text["format"].(map[string]any)
		if !ok {
			t.Fatalf("expected format body, got %#v", text["format"])
		}
		switch format["name"] {
		case "ngen_provider_decision":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"action\":\"run\",\"summary\":\"Run coding task now\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
					}]
				}]
			}`))
		case "ngen_workspace_observation":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"summary\":\"Attempt to read runtime state\",\"commands\":[{\"argv\":[\"cat\",\".ngen/private.txt\"],\"reason\":\"Try to inspect hidden runtime state\"}]}"
					}]
				}]
			}`))
		case "ngen_workspace_edit":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"summary\":\"Fix Add after rejecting hidden read\",\"patch\":\"*** Begin Patch\\n*** Update File: calc.go\\n@@\\n-func Add(a, b int) int { return a - b }\\n+func Add(a, b int) int { return a + b }\\n*** End Patch\",\"writes\":[],\"deletes\":[]}"
					}]
				}]
			}`))
		default:
			t.Fatalf("unexpected schema name: %#v", format["name"])
		}
	}))
	defer server.Close()

	writeProviderConfig(t, dir, map[string]any{
		"mode":        "responses",
		"base_url":    server.URL + "/v1",
		"model":       "gpt-5.4",
		"api_key_env": "OPENAI_API_KEY",
	})

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "coding", "--title", "reject hidden observation path", "--objective", "fix Add", "--constraint", "Do not modify *_test.go files.", "--criterion", "go test passes"}).stdout)
	writeFile(t, filepath.Join(dir, ".ngen", "private.txt"), "secret\n")
	auto := runCLI(t, dir, []string{"auto", taskID, "--json"})
	if auto.exitCode != 0 {
		t.Fatalf("expected coding auto flow to finish after rejecting hidden observation path, got %d stderr=%s stdout=%s", auto.exitCode, auto.stderr, auto.stdout)
	}
	commandRuns := readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "command_runs.jsonl"))
	assertContains(t, commandRuns, "\"status\":\"failed\"")
	assertContains(t, commandRuns, "denied by visibility rules")
	assertContains(t, readFile(t, filepath.Join(dir, "calc.go")), "return a + b")
}

func TestCommandProviderCodingTaskUsesObservationAndPatchPlan(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package main\n\nfunc Add(a, b int) int { return a - b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")

	writeProviderConfig(t, dir, map[string]any{
		"mode":    "command",
		"command": helperAppCommandProviderCommand(t),
	})

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "coding", "--title", "command observation patch", "--objective", "fix Add", "--constraint", "Do not modify *_test.go files.", "--criterion", "go test passes"}).stdout)
	auto := runCLI(t, dir, []string{"auto", taskID, "--json"})
	if auto.exitCode != 0 {
		t.Fatalf("expected command-provider coding auto flow to finish, got %d stderr=%s stdout=%s", auto.exitCode, auto.stderr, auto.stdout)
	}
	assertContains(t, auto.stdout, "\"observation_command_completed\"")
	assertContains(t, auto.stdout, "\"workspace_edit_applied\"")
	assertContains(t, readFile(t, filepath.Join(dir, "calc.go")), "return a + b")
	commandRuns := readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "command_runs.jsonl"))
	assertContains(t, commandRuns, "\"argv\":[\"sed\",\"-n\",\"1,40p\",\"calc.go\"]")
	assertContains(t, commandRuns, "\"side_effect_class\":\"read_only_command\"")
	assertContains(t, commandRuns, "\"replay_policy\":\"safe_to_replay\"")
	workspaceEdits := readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "workspace_edits.jsonl"))
	assertContains(t, workspaceEdits, "\"provider_mode\":\"command\"")
	assertContains(t, workspaceEdits, "\"side_effect_class\":\"workspace_file_edit\"")
	assertContains(t, workspaceEdits, "\"replay_policy\":\"do_not_auto_replay\"")
}

func TestCommandProviderCodingTaskRepairInputsCarryCriteriaLedger(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package main\n\nfunc Add(a, b int) int { return a - b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")

	writeProviderConfig(t, dir, map[string]any{
		"mode":    "command",
		"command": helperAppCommandProviderCommand(t),
	})

	observationCapture := filepath.Join(dir, "workspace_observation_input.json")
	editCapture := filepath.Join(dir, "workspace_edit_input.json")
	t.Setenv("NGEN_CAPTURE_WORKSPACE_OBSERVATION_INPUT", observationCapture)
	t.Setenv("NGEN_CAPTURE_WORKSPACE_EDIT_INPUT", editCapture)

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "coding", "--title", "command criteria ledger", "--objective", "fix Add", "--criterion", "go test passes"}).stdout)
	auto := runCLI(t, dir, []string{"auto", taskID, "--json"})
	if auto.exitCode != 0 {
		t.Fatalf("expected command-provider coding auto flow to finish, got %d stderr=%s stdout=%s", auto.exitCode, auto.stderr, auto.stdout)
	}

	observationInput := readFile(t, observationCapture)
	assertContains(t, observationInput, `"criteria":{"schema_version":1`)
	assertContains(t, observationInput, `"current_criterion_id":"SC-001"`)
	assertContains(t, observationInput, `"passes":false`)
	assertContains(t, observationInput, `"sprint":{"schema_version":1`)
	assertContains(t, observationInput, `"primary_criterion_id":"SC-001"`)
	assertContains(t, observationInput, `"completion_signals":`)
	assertContains(t, observationInput, `"project_focus":{"workspace_current_step_id":"task:`)
	assertContains(t, observationInput, `"primary_step_id":"task:`)
	assertContains(t, observationInput, `"primary_branch_id":"branch:`)
	assertContains(t, observationInput, `"workspace:.ngen/project/project.json"`)

	editInput := readFile(t, editCapture)
	assertContains(t, editInput, `"criteria":{"schema_version":1`)
	assertContains(t, editInput, `"current_criterion_id":"SC-001"`)
	assertContains(t, editInput, `"passes":false`)
	assertContains(t, editInput, `"sprint":{"schema_version":1`)
	assertContains(t, editInput, `"primary_criterion_id":"SC-001"`)
	assertContains(t, editInput, `"completion_signals":`)
	assertContains(t, editInput, `"project_focus":{"workspace_current_step_id":"task:`)
	assertContains(t, editInput, `"primary_step_id":"task:`)
	assertContains(t, editInput, `"primary_branch_id":"branch:`)
	assertContains(t, editInput, `"workspace:.ngen/project/project.json"`)
}

func TestCodingRunTimesOutInsteadOfHanging(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/hang\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "worker.go"), "package main\n\nfunc Value() string { return \"ok\" }\n")
	writeFile(t, filepath.Join(dir, "worker_test.go"), "package main\n\nimport \"testing\"\n\nfunc TestBlocksForever(t *testing.T) {\n\tselect {}\n}\n")
	writeFile(t, filepath.Join(dir, "ngen.json"), `{
  "verification": {
    "coding_timeout_seconds": 1
  }
}`)

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "coding", "--title", "timeout", "--objective", "exercise verifier timeout", "--criterion", "go test passes"}).stdout)
	start := time.Now()
	run := runCLI(t, dir, []string{"run", taskID, "--json"})
	if run.exitCode != 11 {
		t.Fatalf("expected verifier timeout to fail with exit 11, got %d stderr=%s stdout=%s", run.exitCode, run.stderr, run.stdout)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("expected verifier timeout to fail quickly, took %s", elapsed)
	}
	assertContains(t, run.stdout, "\"verification_failed\"")

	status := runCLI(t, dir, []string{"status", taskID, "--json"})
	var snapshot task.StatusSnapshot
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal status snapshot: %v", err)
	}
	if snapshot.State != task.StateFailed || snapshot.StatusReasonCode != "failed_verification" {
		t.Fatalf("expected failed_verification, got state=%s reason=%s", snapshot.State, snapshot.StatusReasonCode)
	}
	assertContains(t, readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "verification", "latest.json")), "timed out after 1s")
}

func TestDocsLiteTaskRun(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "general_execution", "--preset", "docs_lite", "--objective", "review docs", "--criterion", "docs reviewed"}).stdout)
	run := runCLI(t, dir, []string{"run", taskID})
	if run.exitCode != 0 {
		t.Fatalf("expected docs_lite task to finish, got %d stderr=%s", run.exitCode, run.stderr)
	}
	status := runCLI(t, dir, []string{"status", taskID, "--json"})
	var snapshot task.StatusSnapshot
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal status snapshot: %v", err)
	}
	if snapshot.State != task.StateDone {
		t.Fatalf("expected done state, got %s", snapshot.State)
	}
}

func TestDocsLiteVerifierRequiresMarkdown(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "notes.txt"), "no markdown here\n")

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "general_execution", "--preset", "docs_lite", "--objective", "review docs", "--criterion", "docs reviewed"}).stdout)
	run := runCLI(t, dir, []string{"run", taskID})
	if run.exitCode != 11 {
		t.Fatalf("expected docs_lite verifier failure exit code 11, got %d stderr=%s", run.exitCode, run.stderr)
	}

	status := runCLI(t, dir, []string{"status", taskID, "--json"})
	var snapshot task.StatusSnapshot
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal status snapshot: %v", err)
	}
	if snapshot.State != task.StateFailed || snapshot.StatusReasonCode != "failed_verification" {
		t.Fatalf("expected failed_verification, got state=%s reason=%s", snapshot.State, snapshot.StatusReasonCode)
	}
}

func TestAutoCommandUsesBuiltinProvider(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "general_execution", "--preset", "docs_lite", "--objective", "review docs", "--criterion", "docs reviewed"}).stdout)
	auto := runCLI(t, dir, []string{"auto", taskID, "--json"})
	if auto.exitCode != 0 {
		t.Fatalf("expected auto command to complete, got %d stderr=%s stdout=%s", auto.exitCode, auto.stderr, auto.stdout)
	}
	assertContains(t, auto.stdout, "\"provider_decided\"")
	assertContains(t, auto.stdout, "\"review_completed\"")
}

func TestAutoCommandUsesOpenAICompatibleProviderFromConfig(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	var seenPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("expected bearer auth, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices": [{
				"message": {
					"tool_calls": [{
						"function": {
							"name": "submit_decision",
							"arguments": "{\"action\":\"run\",\"summary\":\"Execute now\"}"
						}
					}]
				}
			}]
		}`))
	}))
	defer server.Close()

	writeProviderConfig(t, dir, map[string]any{
		"mode":        "openai-comp",
		"base_url":    server.URL + "/v1",
		"model":       "gpt-5.4",
		"api_key_env": "OPENAI_API_KEY",
	})

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "general_execution", "--preset", "docs_lite", "--objective", "review docs", "--criterion", "docs reviewed"}).stdout)
	auto := runCLI(t, dir, []string{"auto", taskID, "--json"})
	if auto.exitCode != 0 {
		t.Fatalf("expected auto command to complete, got %d stderr=%s stdout=%s", auto.exitCode, auto.stderr, auto.stdout)
	}
	if seenPath != "/v1/chat/completions" {
		t.Fatalf("expected chat/completions path, got %s", seenPath)
	}
	assertContains(t, auto.stdout, "\"provider_decided\"")
	status := runCLI(t, dir, []string{"status", taskID, "--json"})
	var snapshot task.StatusSnapshot
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal status snapshot: %v", err)
	}
	if snapshot.State != task.StateDone {
		t.Fatalf("expected done state, got %s", snapshot.State)
	}
}

func TestAutoCommandUsesAnthropicProviderFromConfig(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	var seenPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Fatalf("expected x-api-key auth, got %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got == "" {
			t.Fatal("expected anthropic-version header")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"content": [{
				"type": "tool_use",
				"name": "submit_decision",
				"input": {
					"action": "run",
					"summary": "Execute now"
				}
			}]
		}`))
	}))
	defer server.Close()

	writeProviderConfig(t, dir, map[string]any{
		"mode":        "anthropic",
		"base_url":    server.URL + "/v1",
		"model":       "gpt-5.4",
		"api_key_env": "OPENAI_API_KEY",
	})

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "general_execution", "--preset", "docs_lite", "--objective", "review docs", "--criterion", "docs reviewed"}).stdout)
	auto := runCLI(t, dir, []string{"auto", taskID, "--json"})
	if auto.exitCode != 0 {
		t.Fatalf("expected auto command to complete, got %d stderr=%s stdout=%s", auto.exitCode, auto.stderr, auto.stdout)
	}
	if seenPath != "/v1/messages" {
		t.Fatalf("expected messages path, got %s", seenPath)
	}
	assertContains(t, auto.stdout, "\"provider_decided\"")
	status := runCLI(t, dir, []string{"status", taskID, "--json"})
	var snapshot task.StatusSnapshot
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal status snapshot: %v", err)
	}
	if snapshot.State != task.StateDone {
		t.Fatalf("expected done state, got %s", snapshot.State)
	}
}

func TestSecurityReviewAndReviewerProfiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# security review\n")
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "demo.go"), "package demo\n\nfunc Add(a, b int) int { return a + b }\n")
	writeFile(t, filepath.Join(dir, "config.secret"), "PASSWORD=test\n")

	securityTaskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "security_review", "--objective", "inspect workspace", "--criterion", "security inventory captured"}).stdout)
	securityRun := runCLI(t, dir, []string{"run", securityTaskID})
	if securityRun.exitCode != 0 {
		t.Fatalf("expected security_review to complete, got %d stderr=%s", securityRun.exitCode, securityRun.stderr)
	}

	reviewerTaskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "reviewer", "--objective", "review repo", "--criterion", "reviewer verifier ran"}).stdout)
	reviewerRun := runCLI(t, dir, []string{"run", reviewerTaskID})
	if reviewerRun.exitCode != 0 {
		t.Fatalf("expected reviewer profile to complete, got %d stderr=%s", reviewerRun.exitCode, reviewerRun.stderr)
	}
}

func TestApprovalLifecycle(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# pending approval\n")

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "general_execution", "--preset", "docs_lite", "--objective", "approval flow", "--criterion", "approval durable"}).stdout)
	approvalID := strings.TrimSpace(runCLI(t, dir, []string{"approval", "request", taskID, "--scope", "manual step", "--reason", "test"}).stdout)
	if approvalID == "" {
		t.Fatal("expected approval id")
	}
	list := runCLI(t, dir, []string{"approval", "ls", taskID})
	if list.exitCode != 0 {
		t.Fatalf("expected approval ls success, got %d stderr=%s", list.exitCode, list.stderr)
	}
	var records []task.ApprovalRecord
	if err := json.Unmarshal([]byte(list.stdout), &records); err != nil {
		t.Fatalf("unmarshal approval records: %v", err)
	}
	if len(records) != 1 || records[0].ApprovalID != approvalID || records[0].Status != "pending" {
		t.Fatalf("expected pending approval record, got %+v", records)
	}

	status := runCLI(t, dir, []string{"status", taskID, "--json"})
	var snapshot task.StatusSnapshot
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal status snapshot: %v", err)
	}
	if snapshot.State != task.StateBlocked || snapshot.StatusReasonCode != "blocked_policy" {
		t.Fatalf("expected blocked_policy, got state=%s reason=%s", snapshot.State, snapshot.StatusReasonCode)
	}
	progress := readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "progress.md"))
	assertContains(t, progress, "blocked_policy")
	assertContains(t, progress, "## Current Status")

	approve := runCLI(t, dir, []string{"approve", taskID, "--request", approvalID})
	if approve.exitCode != 0 {
		t.Fatalf("expected approve success, got %d stderr=%s", approve.exitCode, approve.stderr)
	}
	list = runCLI(t, dir, []string{"approval", "ls", taskID})
	if list.exitCode != 0 {
		t.Fatalf("expected approval ls after approve success, got %d stderr=%s", list.exitCode, list.stderr)
	}
	if err := json.Unmarshal([]byte(list.stdout), &records); err != nil {
		t.Fatalf("unmarshal approval records after approve: %v", err)
	}
	if len(records) != 2 || records[len(records)-1].ApprovalID != approvalID || records[len(records)-1].Status != "approved" {
		t.Fatalf("expected approved approval record history, got %+v", records)
	}
	status = runCLI(t, dir, []string{"status", taskID, "--json"})
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal status snapshot: %v", err)
	}
	if snapshot.State != task.StateActive {
		t.Fatalf("expected active after approval, got %s", snapshot.State)
	}
}

func TestParentOwnedWorkerApprovalLifecycle(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "demo.go"), "package demo\n\nfunc Add(a, b int) int { return a + b }\n")

	parentTaskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "coding", "--title", "parent", "--objective", "verify coding", "--criterion", "go test passes"}).stdout)
	parentRun := runCLI(t, dir, []string{"run", parentTaskID})
	if parentRun.exitCode != 0 {
		t.Fatalf("expected parent task done, got %d stderr=%s", parentRun.exitCode, parentRun.stderr)
	}

	spawn := runCLI(t, dir, []string{"worker", "spawn", parentTaskID, "--role", "reviewer", "--objective", "review parent changes"})
	if spawn.exitCode != 0 {
		t.Fatalf("worker spawn failed: %d stderr=%s stdout=%s", spawn.exitCode, spawn.stderr, spawn.stdout)
	}
	var contract task.WorkerContract
	if err := json.Unmarshal([]byte(spawn.stdout), &contract); err != nil {
		t.Fatalf("unmarshal worker contract: %v", err)
	}
	if contract.WorkerID == "" || contract.ChildTaskID == "" {
		t.Fatalf("expected worker and child ids, got %+v", contract)
	}

	request := runCLI(t, dir, []string{"approval", "request", contract.ChildTaskID, "--scope", "manual step", "--reason", "worker asks parent"})
	if request.exitCode != 10 {
		t.Fatalf("expected child approval request to block, got %d stderr=%s", request.exitCode, request.stderr)
	}
	approvalID := strings.TrimSpace(request.stdout)
	if approvalID == "" {
		t.Fatal("expected approval id from child request")
	}

	direct := runCLI(t, dir, []string{"approval", "ls", parentTaskID})
	if direct.exitCode != 0 {
		t.Fatalf("expected parent direct approval ls success, got %d stderr=%s", direct.exitCode, direct.stderr)
	}
	var records []task.ApprovalRecord
	if err := json.Unmarshal([]byte(direct.stdout), &records); err != nil {
		t.Fatalf("unmarshal parent direct approval records: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expected no direct parent approvals, got %+v", records)
	}

	owned := runCLI(t, dir, []string{"approval", "ls", parentTaskID, "--owned"})
	if owned.exitCode != 0 {
		t.Fatalf("expected parent owned approval ls success, got %d stderr=%s", owned.exitCode, owned.stderr)
	}
	if err := json.Unmarshal([]byte(owned.stdout), &records); err != nil {
		t.Fatalf("unmarshal parent owned approval records: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected one owned approval record, got %+v", records)
	}
	if records[0].ApprovalID != approvalID || records[0].TaskID != contract.ChildTaskID || records[0].OwnerTaskID != parentTaskID || records[0].OwnerWorkerID != contract.WorkerID || records[0].Status != "pending" {
		t.Fatalf("expected owned child approval record, got %+v", records[0])
	}

	approve := runCLI(t, dir, []string{"approve", parentTaskID, "--request", approvalID})
	if approve.exitCode != 0 {
		t.Fatalf("expected parent approve success, got %d stderr=%s stdout=%s", approve.exitCode, approve.stderr, approve.stdout)
	}

	owned = runCLI(t, dir, []string{"approval", "ls", parentTaskID, "--owned"})
	if owned.exitCode != 0 {
		t.Fatalf("expected owned approval ls after parent approve success, got %d stderr=%s", owned.exitCode, owned.stderr)
	}
	if err := json.Unmarshal([]byte(owned.stdout), &records); err != nil {
		t.Fatalf("unmarshal parent owned approval records after approve: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected request and decision in owned approval history, got %+v", records)
	}
	last := records[len(records)-1]
	if last.ApprovalID != approvalID || last.TaskID != contract.ChildTaskID || last.OwnerTaskID != parentTaskID || last.OwnerWorkerID != contract.WorkerID || last.Status != "approved" {
		t.Fatalf("expected approved owned child approval record, got %+v", last)
	}

	status := runCLI(t, dir, []string{"status", contract.ChildTaskID, "--json"})
	var snapshot task.StatusSnapshot
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal child status snapshot: %v", err)
	}
	if snapshot.State != task.StateActive || snapshot.StatusReasonCode != "" {
		t.Fatalf("expected child task active after parent-owned approval, got state=%s reason=%s", snapshot.State, snapshot.StatusReasonCode)
	}
}

func TestYoloPermissionModeAutoApproves(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# yolo\n")

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "general_execution", "--preset", "docs_lite", "--permission-mode", "yolo", "--objective", "approval flow", "--criterion", "approval durable"}).stdout)
	request := runCLI(t, dir, []string{"approval", "request", taskID, "--scope", "manual step", "--reason", "test"})
	if request.exitCode != 10 {
		t.Fatalf("expected approval request command exit 10, got %d stderr=%s", request.exitCode, request.stderr)
	}
	status := runCLI(t, dir, []string{"status", taskID, "--json"})
	var snapshot task.StatusSnapshot
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal status snapshot: %v", err)
	}
	if snapshot.State == task.StateBlocked {
		t.Fatalf("expected yolo permission mode to avoid blocked_policy, got state=%s reason=%s", snapshot.State, snapshot.StatusReasonCode)
	}
}

func TestWatchAndSchedulerTick(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# watch\n")

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "general_execution", "--preset", "docs_lite", "--objective", "watch flow", "--criterion", "watch resumes"}).stdout)
	watchID := strings.TrimSpace(runCLI(t, dir, []string{"watch", "set", taskID, "--interval", "1s", "--reason", "tick"}).stdout)
	progress := readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "progress.md"))
	assertContains(t, progress, "waiting_watch")
	assertContains(t, progress, watchID)

	store := artifact.NewStore(dir, ".ngen")
	watch, err := store.LoadWatch(watchID)
	if err != nil {
		t.Fatalf("load watch: %v", err)
	}
	watch.NextWakeAt = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	if err := store.SaveWatch(watch); err != nil {
		t.Fatalf("save watch: %v", err)
	}

	tick := runCLI(t, dir, []string{"scheduler", "tick", "--once"})
	if tick.exitCode != 0 {
		t.Fatalf("expected scheduler tick success, got %d stderr=%s", tick.exitCode, tick.stderr)
	}
	status := runCLI(t, dir, []string{"status", taskID, "--json"})
	var snapshot task.StatusSnapshot
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal status snapshot: %v", err)
	}
	if snapshot.State == task.StateWaiting {
		t.Fatalf("expected task to leave waiting state after tick")
	}
}

func TestWatchSetReplacesExistingActiveWatch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# watch\n")

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "general_execution", "--preset", "docs_lite", "--objective", "watch flow", "--criterion", "watch resumes"}).stdout)
	firstWatchID := strings.TrimSpace(runCLI(t, dir, []string{"watch", "set", taskID, "--interval", "1s", "--reason", "first"}).stdout)
	secondWatchID := strings.TrimSpace(runCLI(t, dir, []string{"watch", "set", taskID, "--interval", "2s", "--reason", "second"}).stdout)

	store := artifact.NewStore(dir, ".ngen")
	first, err := store.LoadWatch(firstWatchID)
	if err != nil {
		t.Fatalf("load first watch: %v", err)
	}
	second, err := store.LoadWatch(secondWatchID)
	if err != nil {
		t.Fatalf("load second watch: %v", err)
	}
	if first.Status != "cancelled" {
		t.Fatalf("expected first watch cancelled, got %s", first.Status)
	}
	if second.Status != "active" {
		t.Fatalf("expected second watch active, got %s", second.Status)
	}
}

func TestInputRequestLifecycle(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# input\n")

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "general_execution", "--preset", "docs_lite", "--objective", "input flow", "--criterion", "input durable"}).stdout)
	request := runCLI(t, dir, []string{"input", "request", taskID, "--field", "target_path", "--prompt", "Provide target path"})
	if request.exitCode != 10 {
		t.Fatalf("expected input request to block, got %d stderr=%s", request.exitCode, request.stderr)
	}
	requestID := strings.TrimSpace(request.stdout)
	if requestID == "" {
		t.Fatal("expected input request id")
	}

	status := runCLI(t, dir, []string{"status", taskID, "--json"})
	var snapshot task.StatusSnapshot
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal status snapshot: %v", err)
	}
	if snapshot.State != task.StateBlocked || snapshot.StatusReasonCode != "blocked_missing_input" {
		t.Fatalf("expected blocked_missing_input, got state=%s reason=%s", snapshot.State, snapshot.StatusReasonCode)
	}
	if !strings.Contains(snapshot.StatusDetailRef, "input_requests.jsonl#input_record_id=") {
		t.Fatalf("expected input request detail ref, got %s", snapshot.StatusDetailRef)
	}

	progress := readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "progress.md"))
	assertContains(t, progress, "blocked_missing_input")
	assertContains(t, progress, "Provide the requested input")

	list := runCLI(t, dir, []string{"input", "ls", taskID})
	if list.exitCode != 0 {
		t.Fatalf("expected input ls success, got %d stderr=%s", list.exitCode, list.stderr)
	}
	var records []task.InputRequestRecord
	if err := json.Unmarshal([]byte(list.stdout), &records); err != nil {
		t.Fatalf("unmarshal input records: %v", err)
	}
	if len(records) != 1 || records[0].Status != "pending" {
		t.Fatalf("expected one pending input record, got %+v", records)
	}

	respond := runCLI(t, dir, []string{"input", "respond", taskID, "--request", requestID, "--value", "/tmp/target"})
	if respond.exitCode != 0 {
		t.Fatalf("expected input respond success, got %d stderr=%s", respond.exitCode, respond.stderr)
	}
	status = runCLI(t, dir, []string{"status", taskID, "--json"})
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal status snapshot: %v", err)
	}
	if snapshot.State != task.StateActive || snapshot.StatusReasonCode != "" {
		t.Fatalf("expected active state after input response, got state=%s reason=%s", snapshot.State, snapshot.StatusReasonCode)
	}

	list = runCLI(t, dir, []string{"input", "ls", taskID})
	if err := json.Unmarshal([]byte(list.stdout), &records); err != nil {
		t.Fatalf("unmarshal input records after response: %v", err)
	}
	if len(records) != 2 || records[1].Status != "answered" {
		t.Fatalf("expected answered input record appended, got %+v", records)
	}
}

func TestReviewCommandRegeneratesMissingHandoffAfterDrift(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "demo.go"), "package demo\n\nfunc Add(a, b int) int { return a + b }\n")

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "coding", "--title", "demo", "--objective", "verify coding", "--criterion", "go test passes"}).stdout)
	run := runCLI(t, dir, []string{"run", taskID})
	if run.exitCode != 0 {
		t.Fatalf("expected done run, got %d stderr=%s", run.exitCode, run.stderr)
	}

	handoffPath := filepath.Join(dir, ".ngen", "tasks", taskID, "handoff.md")
	if err := os.Remove(handoffPath); err != nil {
		t.Fatalf("remove handoff: %v", err)
	}

	review := runCLI(t, dir, []string{"review", taskID, "--json"})
	if review.exitCode != 0 {
		t.Fatalf("expected review to regenerate the missing handoff and clear, got %d stderr=%s stdout=%s", review.exitCode, review.stderr, review.stdout)
	}

	status := runCLI(t, dir, []string{"status", taskID, "--json"})
	var snapshot task.StatusSnapshot
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal status snapshot: %v", err)
	}
	if snapshot.State != task.StateDone || snapshot.StatusReasonCode != "" {
		t.Fatalf("expected done after regenerating handoff, got state=%s reason=%s", snapshot.State, snapshot.StatusReasonCode)
	}

	var completion task.CompletionReport
	completionPath := filepath.Join(dir, ".ngen", "tasks", taskID, "completion", "latest.json")
	if err := json.Unmarshal([]byte(readFile(t, completionPath)), &completion); err != nil {
		t.Fatalf("unmarshal completion report: %v", err)
	}
	if completion.Status != "accepted" {
		t.Fatalf("expected accepted completion after handoff regeneration, got %s", completion.Status)
	}

	handoff := readFile(t, handoffPath)
	assertContains(t, handoff, "## Evidence")
	assertContains(t, handoff, "## Resume Instructions")

	progress := readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "progress.md"))
	assertContains(t, progress, "Done")
	assertContains(t, progress, "## Latest Evidence")
}

func TestReviewCommandBlocksBeforeVerificationRuns(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")

	taskID := strings.TrimSpace(runCLI(t, dir, []string{
		"task", "create",
		"--kind", "general_execution",
		"--preset", "docs_lite",
		"--title", "docs gate",
		"--objective", "review docs",
		"--criterion", "docs reviewed",
	}).stdout)

	review := runCLI(t, dir, []string{"review", taskID, "--json"})
	if review.exitCode != 10 {
		t.Fatalf("expected missing verification review to block with exit code 10, got %d stderr=%s stdout=%s", review.exitCode, review.stderr, review.stdout)
	}

	var report task.ReviewReport
	if err := json.Unmarshal([]byte(review.stdout), &report); err != nil {
		t.Fatalf("unmarshal review report: %v", err)
	}
	if report.Status != "blocking" || !strings.Contains(report.Summary, "verification has not run yet") {
		t.Fatalf("expected missing-verification blocker summary, got %+v", report)
	}

	status := runCLI(t, dir, []string{"status", taskID, "--json"})
	var snapshot task.StatusSnapshot
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal status snapshot: %v", err)
	}
	if snapshot.State != task.StateBlocked || snapshot.StatusReasonCode != "blocked_review" {
		t.Fatalf("expected blocked_review after missing verification review, got %+v", snapshot)
	}

	var completion task.CompletionReport
	completionPath := filepath.Join(dir, ".ngen", "tasks", taskID, "completion", "latest.json")
	if err := json.Unmarshal([]byte(readFile(t, completionPath)), &completion); err != nil {
		t.Fatalf("unmarshal completion report: %v", err)
	}
	if completion.Status != "rejected" || !strings.Contains(completion.Summary, "verification has not run yet") {
		t.Fatalf("expected rejected completion with verification blocker summary, got %+v", completion)
	}

	progress := readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "progress.md"))
	assertContains(t, progress, "blocked_review")
	assertContains(t, progress, "verification has not run yet")
}

func TestReviewCommandRegeneratesMissingHandoffButKeepsCriteriaBlocker(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs gate\n")

	taskID := strings.TrimSpace(runCLI(t, dir, []string{
		"task", "create",
		"--kind", "general_execution",
		"--preset", "docs_lite",
		"--title", "docs gate",
		"--objective", "review docs",
		"--criterion", "README.md mentions `alpha`",
	}).stdout)
	run := runCLI(t, dir, []string{"run", taskID})
	if run.exitCode != 10 {
		t.Fatalf("expected blocked review run, got %d stderr=%s stdout=%s", run.exitCode, run.stderr, run.stdout)
	}

	handoffPath := filepath.Join(dir, ".ngen", "tasks", taskID, "handoff.md")
	if err := os.Remove(handoffPath); err != nil {
		t.Fatalf("remove handoff: %v", err)
	}
	review := runCLI(t, dir, []string{"review", taskID, "--json"})
	if review.exitCode != 10 {
		t.Fatalf("expected criteria-blocked review to remain blocked, got %d stderr=%s stdout=%s", review.exitCode, review.stderr, review.stdout)
	}

	status := runCLI(t, dir, []string{"status", taskID, "--json"})
	var snapshot task.StatusSnapshot
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal status snapshot: %v", err)
	}
	if snapshot.State != task.StateBlocked || snapshot.StatusReasonCode != "blocked_review" {
		t.Fatalf("expected blocked_review after failed verification review, got %+v", snapshot)
	}

	var completion task.CompletionReport
	completionPath := filepath.Join(dir, ".ngen", "tasks", taskID, "completion", "latest.json")
	if err := json.Unmarshal([]byte(readFile(t, completionPath)), &completion); err != nil {
		t.Fatalf("unmarshal completion report: %v", err)
	}
	if completion.Status != "rejected" {
		t.Fatalf("expected rejected completion after criteria-blocked review, got %s", completion.Status)
	}

	handoff := readFile(t, handoffPath)
	assertContains(t, handoff, "## Evidence")
	assertContains(t, handoff, "## Resume Instructions")
}

func TestReviewBlocksWorkerCriterionWithoutRuntimeEvidence(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "demo.go"), "package demo\n\nfunc Add(a, b int) int { return a + b }\n")

	parentTaskID := strings.TrimSpace(runCLI(t, dir, []string{
		"task", "create",
		"--kind", "coding",
		"--title", "parent worker trust",
		"--objective", "verify parent worker evidence",
		"--criterion", "go test passes",
		"--criterion", "reviewer worker exists",
	}).stdout)
	spawn := runCLI(t, dir, []string{"worker", "spawn", parentTaskID, "--role", "reviewer", "--objective", "review parent changes"})
	if spawn.exitCode != 0 {
		t.Fatalf("worker spawn failed: %d stderr=%s stdout=%s", spawn.exitCode, spawn.stderr, spawn.stdout)
	}

	run := runCLI(t, dir, []string{"run", parentTaskID, "--json"})
	if run.exitCode != 10 {
		t.Fatalf("expected parent run to block on worker trust gap, got %d stderr=%s stdout=%s", run.exitCode, run.stderr, run.stdout)
	}
	var report task.ReviewReport
	reviewPath := filepath.Join(dir, ".ngen", "tasks", parentTaskID, "reviews", "latest.json")
	if err := json.Unmarshal([]byte(readFile(t, reviewPath)), &report); err != nil {
		t.Fatalf("unmarshal review report: %v", err)
	}
	if report.Status != "blocking" || !contains(report.BlockingCategories, "worker_trust_gap") || report.RiskSummary.WorkerTrustGaps == 0 {
		t.Fatalf("expected worker trust gap review report, got %+v", report)
	}
	assertContains(t, readFile(t, filepath.Join(dir, ".ngen", "tasks", parentTaskID, "findings.jsonl")), `"category":"worker_trust_gap"`)
}

func TestWorkerSpawnSyncAndMemoryShow(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "demo.go"), "package demo\n\nfunc Add(a, b int) int { return a + b }\n")

	parentTaskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "coding", "--title", "parent", "--objective", "verify coding", "--criterion", "go test passes"}).stdout)
	parentRun := runCLI(t, dir, []string{"run", parentTaskID})
	if parentRun.exitCode != 0 {
		t.Fatalf("expected parent task done, got %d stderr=%s", parentRun.exitCode, parentRun.stderr)
	}

	spawn := runCLI(t, dir, []string{"worker", "spawn", parentTaskID, "--role", "reviewer", "--objective", "review parent changes"})
	if spawn.exitCode != 0 {
		t.Fatalf("worker spawn failed: %d stderr=%s stdout=%s", spawn.exitCode, spawn.stderr, spawn.stdout)
	}
	var contract task.WorkerContract
	if err := json.Unmarshal([]byte(spawn.stdout), &contract); err != nil {
		t.Fatalf("unmarshal worker contract: %v", err)
	}
	if contract.ChildTaskID == "" {
		t.Fatal("expected child task id")
	}

	childRun := runCLI(t, dir, []string{"run", contract.ChildTaskID})
	if childRun.exitCode != 0 {
		t.Fatalf("child run failed: %d stderr=%s", childRun.exitCode, childRun.stderr)
	}

	sync := runCLI(t, dir, []string{"worker", "sync", parentTaskID, contract.WorkerID})
	if sync.exitCode != 0 {
		t.Fatalf("worker sync failed: %d stderr=%s stdout=%s", sync.exitCode, sync.stderr, sync.stdout)
	}
	if err := json.Unmarshal([]byte(sync.stdout), &contract); err != nil {
		t.Fatalf("unmarshal synced worker contract: %v", err)
	}
	if contract.Status != "done" {
		t.Fatalf("expected synced worker status done, got %s", contract.Status)
	}

	memory := runCLI(t, dir, []string{"memory", "show"})
	if memory.exitCode != 0 {
		t.Fatalf("memory show failed: %d stderr=%s", memory.exitCode, memory.stderr)
	}
	assertContains(t, memory.stdout, parentTaskID)
}

func TestWorkerContinueAfterOwnedApproval(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "demo.go"), "package demo\n\nfunc Add(a, b int) int { return a + b }\n")

	parentTaskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "coding", "--title", "parent", "--objective", "verify coding", "--criterion", "go test passes"}).stdout)
	parentRun := runCLI(t, dir, []string{"run", parentTaskID})
	if parentRun.exitCode != 0 {
		t.Fatalf("expected parent task done, got %d stderr=%s", parentRun.exitCode, parentRun.stderr)
	}

	spawn := runCLI(t, dir, []string{"worker", "spawn", parentTaskID, "--role", "reviewer", "--objective", "review parent changes"})
	if spawn.exitCode != 0 {
		t.Fatalf("worker spawn failed: %d stderr=%s stdout=%s", spawn.exitCode, spawn.stderr, spawn.stdout)
	}
	var contract task.WorkerContract
	if err := json.Unmarshal([]byte(spawn.stdout), &contract); err != nil {
		t.Fatalf("unmarshal worker contract: %v", err)
	}

	request := runCLI(t, dir, []string{"approval", "request", contract.ChildTaskID, "--scope", "manual step", "--reason", "worker asks parent"})
	if request.exitCode != 10 {
		t.Fatalf("expected approval request to block child, got %d stderr=%s stdout=%s", request.exitCode, request.stderr, request.stdout)
	}
	approvalID := strings.TrimSpace(request.stdout)
	if approvalID == "" {
		t.Fatal("expected approval id from child approval request")
	}

	approve := runCLI(t, dir, []string{"approve", parentTaskID, "--request", approvalID})
	if approve.exitCode != 0 {
		t.Fatalf("approve failed: %d stderr=%s stdout=%s", approve.exitCode, approve.stderr, approve.stdout)
	}

	sync := runCLI(t, dir, []string{"worker", "sync", parentTaskID, contract.WorkerID})
	if sync.exitCode != 0 {
		t.Fatalf("worker sync failed: %d stderr=%s stdout=%s", sync.exitCode, sync.stderr, sync.stdout)
	}
	if err := json.Unmarshal([]byte(sync.stdout), &contract); err != nil {
		t.Fatalf("unmarshal synced worker contract: %v", err)
	}
	if contract.ParentActionType != "continue_child" || contract.RequiresParentAction != true {
		t.Fatalf("expected worker sync to advertise continuation, got %+v", contract)
	}

	continueWorker := runCLI(t, dir, []string{"worker", "continue", parentTaskID, contract.WorkerID})
	if continueWorker.exitCode != 0 {
		t.Fatalf("worker continue failed: %d stderr=%s stdout=%s", continueWorker.exitCode, continueWorker.stderr, continueWorker.stdout)
	}
	var continued task.WorkerContract
	if err := json.Unmarshal([]byte(continueWorker.stdout), &continued); err != nil {
		t.Fatalf("unmarshal continued worker contract: %v", err)
	}
	if continued.Status != "done" || continued.RequiresParentAction {
		t.Fatalf("expected continued worker to finish cleanly, got %+v", continued)
	}
}

func TestAutoContinuesWorkerAfterOwnedApproval(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "demo.go"), "package demo\n\nfunc Add(a, b int) int { return a + b }\n")
	writeFile(t, filepath.Join(dir, "README.md"), "# reviewer docs\n")

	parentTaskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "coding", "--title", "parent", "--objective", "verify coding", "--criterion", "go test passes"}).stdout)
	parentRun := runCLI(t, dir, []string{"run", parentTaskID})
	if parentRun.exitCode != 0 {
		t.Fatalf("expected parent task done, got %d stderr=%s", parentRun.exitCode, parentRun.stderr)
	}

	spawn := runCLI(t, dir, []string{"worker", "spawn", parentTaskID, "--role", "reviewer", "--objective", "review parent changes"})
	if spawn.exitCode != 0 {
		t.Fatalf("worker spawn failed: %d stderr=%s stdout=%s", spawn.exitCode, spawn.stderr, spawn.stdout)
	}
	var contract task.WorkerContract
	if err := json.Unmarshal([]byte(spawn.stdout), &contract); err != nil {
		t.Fatalf("unmarshal worker contract: %v", err)
	}

	request := runCLI(t, dir, []string{"approval", "request", contract.ChildTaskID, "--scope", "manual step", "--reason", "worker asks parent"})
	if request.exitCode != 10 {
		t.Fatalf("expected approval request to block child, got %d stderr=%s stdout=%s", request.exitCode, request.stderr, request.stdout)
	}
	approvalID := strings.TrimSpace(request.stdout)
	if approvalID == "" {
		t.Fatal("expected approval id from child approval request")
	}

	approve := runCLI(t, dir, []string{"approve", parentTaskID, "--request", approvalID})
	if approve.exitCode != 0 {
		t.Fatalf("approve failed: %d stderr=%s stdout=%s", approve.exitCode, approve.stderr, approve.stdout)
	}

	auto := runCLI(t, dir, []string{"auto", parentTaskID, "--json"})
	if auto.exitCode != 0 {
		t.Fatalf("expected auto to continue the approved worker, got %d stderr=%s stdout=%s", auto.exitCode, auto.stderr, auto.stdout)
	}
	if !strings.Contains(auto.stdout, `"type":"worker_continued"`) {
		t.Fatalf("expected auto output to surface worker_continued event, got %s", auto.stdout)
	}

	sync := runCLI(t, dir, []string{"worker", "sync", parentTaskID, contract.WorkerID})
	if sync.exitCode != 0 {
		t.Fatalf("worker sync failed: %d stderr=%s stdout=%s", sync.exitCode, sync.stderr, sync.stdout)
	}
	var updated task.WorkerContract
	if err := json.Unmarshal([]byte(sync.stdout), &updated); err != nil {
		t.Fatalf("unmarshal synced worker contract: %v", err)
	}
	if updated.Status != "done" || updated.RequiresParentAction {
		t.Fatalf("expected auto-continued worker to finish cleanly, got %+v", updated)
	}
}

func TestParentRunRecomputesWorkerCriteriaEvidence(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# reviewer docs\n")

	parentTaskID := strings.TrimSpace(runCLI(t, dir, []string{
		"task", "create",
		"--kind", "general_execution",
		"--preset", "docs_lite",
		"--objective", "manage reviewer child",
		"--criterion", "reviewer child produces a compiled result",
		"--criterion", "reviewer child review is clear",
	}).stdout)

	parentRun := runCLI(t, dir, []string{"run", parentTaskID})
	if parentRun.exitCode != 10 {
		t.Fatalf("expected initial parent run to block on review, got %d stderr=%s stdout=%s", parentRun.exitCode, parentRun.stderr, parentRun.stdout)
	}
	parentStatus := runCLI(t, dir, []string{"status", parentTaskID, "--json"})
	var blocked task.StatusSnapshot
	if err := json.Unmarshal([]byte(parentStatus.stdout), &blocked); err != nil {
		t.Fatalf("unmarshal blocked parent status: %v", err)
	}
	if blocked.State != task.StateBlocked || blocked.StatusReasonCode != "blocked_review" {
		t.Fatalf("expected blocked_review before worker evidence, got %+v", blocked)
	}

	spawn := runCLI(t, dir, []string{"worker", "spawn", parentTaskID, "--role", "reviewer", "--objective", "review parent docs"})
	if spawn.exitCode != 0 {
		t.Fatalf("worker spawn failed: %d stderr=%s stdout=%s", spawn.exitCode, spawn.stderr, spawn.stdout)
	}
	var contract task.WorkerContract
	if err := json.Unmarshal([]byte(spawn.stdout), &contract); err != nil {
		t.Fatalf("unmarshal worker contract: %v", err)
	}

	continueWorker := runCLI(t, dir, []string{"worker", "continue", parentTaskID, contract.WorkerID})
	if continueWorker.exitCode != 0 {
		t.Fatalf("worker continue failed: %d stderr=%s stdout=%s", continueWorker.exitCode, continueWorker.stderr, continueWorker.stdout)
	}
	var updated task.WorkerContract
	if err := json.Unmarshal([]byte(continueWorker.stdout), &updated); err != nil {
		t.Fatalf("unmarshal continued worker contract: %v", err)
	}
	if updated.Status != "done" || updated.ResultRef == "" {
		t.Fatalf("expected completed reviewer worker with compiled result, got %+v", updated)
	}

	parentRerun := runCLI(t, dir, []string{"run", parentTaskID})
	if parentRerun.exitCode != 0 {
		t.Fatalf("expected parent rerun to complete after worker evidence, got %d stderr=%s stdout=%s", parentRerun.exitCode, parentRerun.stderr, parentRerun.stdout)
	}
	parentStatus = runCLI(t, dir, []string{"status", parentTaskID, "--json"})
	var done task.StatusSnapshot
	if err := json.Unmarshal([]byte(parentStatus.stdout), &done); err != nil {
		t.Fatalf("unmarshal done parent status: %v", err)
	}
	if done.State != task.StateDone {
		t.Fatalf("expected parent to reach Done after worker evidence, got %+v", done)
	}

	criteria := readFile(t, filepath.Join(dir, ".ngen", "tasks", parentTaskID, "criteria", "latest.json"))
	assertContains(t, criteria, "\"worker_runtime/"+contract.WorkerID+".result.json\"")
	assertContains(t, criteria, "\"../"+contract.ChildTaskID+"/reviews/latest.json\"")
}

func TestOpenAIResponsesAutoSpawnsAndRunsWorker(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "demo.go"), "package demo\n\nfunc Add(a, b int) int { return a + b }\n")
	writeFile(t, filepath.Join(dir, "demo_test.go"), "package demo\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")
	writeFile(t, filepath.Join(dir, "README.md"), "# worker review target\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		text, ok := body["text"].(map[string]any)
		if !ok {
			t.Fatalf("expected text body, got %#v", body["text"])
		}
		format, ok := text["format"].(map[string]any)
		if !ok {
			t.Fatalf("expected format body, got %#v", text["format"])
		}
		if format["name"] != "ngen_provider_decision" {
			t.Fatalf("unexpected schema name: %#v", format["name"])
		}
		_, _ = w.Write([]byte(`{
			"output": [{
				"type": "message",
				"role": "assistant",
				"content": [{
					"type": "output_text",
					"text": "{\"action\":\"worker_spawn\",\"summary\":\"Spawn a reviewer child for bounded review.\",\"worker_id\":\"\",\"worker_role\":\"reviewer\",\"worker_objective\":\"review the parent output\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
				}]
			}]
		}`))
	}))
	defer server.Close()

	writeProviderConfig(t, dir, map[string]any{
		"mode":        "responses",
		"base_url":    server.URL + "/v1",
		"model":       "gpt-5.4",
		"api_key_env": "OPENAI_API_KEY",
	})

	parentTaskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "coding", "--title", "parent", "--objective", "verify coding", "--criterion", "go test passes"}).stdout)
	parentRun := runCLI(t, dir, []string{"run", parentTaskID})
	if parentRun.exitCode != 0 {
		t.Fatalf("expected parent task done, got %d stderr=%s", parentRun.exitCode, parentRun.stderr)
	}

	auto := runCLI(t, dir, []string{"auto", parentTaskID, "--json"})
	if auto.exitCode != 0 {
		t.Fatalf("expected auto to spawn and run the worker, got %d stderr=%s stdout=%s", auto.exitCode, auto.stderr, auto.stdout)
	}
	if !strings.Contains(auto.stdout, `"type":"worker_spawned"`) || !strings.Contains(auto.stdout, `"type":"worker_continued"`) {
		t.Fatalf("expected auto output to surface worker spawn+continue events, got %s", auto.stdout)
	}
	if requestCount != 1 {
		t.Fatalf("expected one provider decision request, got %d", requestCount)
	}

	workers := runCLI(t, dir, []string{"worker", "ls", parentTaskID})
	if workers.exitCode != 0 {
		t.Fatalf("worker ls failed: %d stderr=%s stdout=%s", workers.exitCode, workers.stderr, workers.stdout)
	}
	var listed []task.WorkerContract
	if err := json.Unmarshal([]byte(workers.stdout), &listed); err != nil {
		t.Fatalf("unmarshal workers: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected one spawned worker, got %+v", listed)
	}
	if listed[0].Role != string(task.KindReviewer) || listed[0].Status != "done" || listed[0].RequiresParentAction {
		t.Fatalf("expected reviewer worker to finish cleanly, got %+v", listed[0])
	}

	childStatus := runCLI(t, dir, []string{"status", listed[0].ChildTaskID, "--json"})
	if childStatus.exitCode != 0 {
		t.Fatalf("child status failed: %d stderr=%s stdout=%s", childStatus.exitCode, childStatus.stderr, childStatus.stdout)
	}
	var snapshot task.StatusSnapshot
	if err := json.Unmarshal([]byte(childStatus.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal child snapshot: %v", err)
	}
	if snapshot.State != task.StateDone {
		t.Fatalf("expected child to be done after provider-spawned worker run, got %+v", snapshot)
	}
}

func TestTypedHookRegistryRunsAndSurfacesSoftFailure(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")
	writeFile(t, filepath.Join(dir, "ngen.json"), `{
  "hooks": {
    "registry": [
      {
        "hook_id": "typed_pre",
        "stage": "pre_run",
        "actions": ["run"],
        "command": ["bash", "-lc", "printf typed-pre"]
      },
      {
        "hook_id": "typed_post_soft",
        "stage": "post_run",
        "command": ["bash", "-lc", "echo typed-post-soft 1>&2; exit 7"],
        "allow_failure": true
      },
      {
        "hook_id": "typed_done",
        "stage": "on_done",
        "command": ["bash", "-lc", "printf typed-done"]
      }
    ]
  }
}`)

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "general_execution", "--preset", "docs_lite", "--objective", "review docs", "--criterion", "docs reviewed"}).stdout)
	run := runCLI(t, dir, []string{"run", taskID, "--json"})
	if run.exitCode != 0 {
		t.Fatalf("expected hook-enabled task done, got %d stderr=%s stdout=%s", run.exitCode, run.stderr, run.stdout)
	}
	assertContains(t, run.stdout, `"hook_executed"`)
	assertContains(t, run.stdout, `"hook_failed"`)
	events := readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "events.jsonl"))
	assertContains(t, events, "typed_pre")
	assertContains(t, events, "typed_post_soft")
	assertContains(t, events, "typed_done")
}

func TestTypedHookRegistryBlocksOnHardFailure(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")
	writeFile(t, filepath.Join(dir, "ngen.json"), `{
  "hooks": {
    "registry": [
      {
        "hook_id": "typed_pre_fail",
        "stage": "pre_run",
        "actions": ["run"],
        "command": ["bash", "-lc", "echo hard-stop 1>&2; exit 9"]
      }
    ]
  }
}`)

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "general_execution", "--preset", "docs_lite", "--objective", "review docs", "--criterion", "docs reviewed"}).stdout)
	run := runCLI(t, dir, []string{"run", taskID})
	if run.exitCode != 13 {
		t.Fatalf("expected hard hook failure exit 13, got %d stderr=%s stdout=%s", run.exitCode, run.stderr, run.stdout)
	}
	assertContains(t, run.stderr, "typed_pre_fail hook failed")
}

func TestReviewerRunsGoAndDocsChecks(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# reviewer docs\n")
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "demo.go"), "package demo\n\nfunc Add(a, b int) int { return a + b }\n")

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "reviewer", "--objective", "review repo", "--criterion", "reviewer verifier ran"}).stdout)
	run := runCLI(t, dir, []string{"run", taskID})
	if run.exitCode != 0 {
		t.Fatalf("expected reviewer run done, got %d stderr=%s", run.exitCode, run.stderr)
	}
	var report task.VerificationReport
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "verification", "latest.json"))), &report); err != nil {
		t.Fatalf("unmarshal verification report: %v", err)
	}
	var names []string
	for _, check := range report.Checks {
		names = append(names, check.Name)
	}
	if !contains(names, "reviewer_go_test") || !contains(names, "reviewer_docs_review") {
		t.Fatalf("expected reviewer checks for go and docs, got %+v", names)
	}
}

func TestBaselineVisibilityIncludesAdditionalRootsAndDeniesHiddenRefs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# root docs\n")
	writeFile(t, filepath.Join(dir, "shared", "README.md"), "# shared docs\n")
	writeFile(t, filepath.Join(dir, "shared", ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(dir, "ngen.json"), `{
  "visibility": {
    "additional_roots": ["shared"],
    "deny_patterns": [".git", ".ngen"]
  }
}`)

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "general_execution", "--preset", "docs_lite", "--objective", "baseline roots", "--criterion", "baseline captured"}).stdout)
	run := runCLI(t, dir, []string{"run", taskID})
	if run.exitCode != 0 {
		t.Fatalf("expected visibility run done, got %d stderr=%s", run.exitCode, run.stderr)
	}
	var baseline task.Baseline
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "baseline.json"))), &baseline); err != nil {
		t.Fatalf("unmarshal baseline: %v", err)
	}
	if !contains(baseline.RepoTruthRefs, "workspace:README.md") {
		t.Fatalf("expected workspace README ref, got %+v", baseline.RepoTruthRefs)
	}
	if !contains(baseline.RepoTruthRefs, "root:extra_1/README.md") {
		t.Fatalf("expected additional root README ref, got %+v", baseline.RepoTruthRefs)
	}
	for _, ref := range baseline.RepoTruthRefs {
		if strings.Contains(ref, ".git") {
			t.Fatalf("expected deny filtering to remove .git refs, got %+v", baseline.RepoTruthRefs)
		}
	}
}

func TestBaselineCapturesCommandHintsAndGitWorkspaceSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# repo bearings\n")
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/repo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package repo\n\nfunc Add(a, b int) int { return a + b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package repo\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")
	writeFile(t, filepath.Join(dir, "build.sh"), "#!/usr/bin/env bash\nset -euo pipefail\ngo test ./...\n")
	writeFile(t, filepath.Join(dir, "init.sh"), "#!/usr/bin/env bash\nset -euo pipefail\necho bootstrap ready\n")
	if err := os.Chmod(filepath.Join(dir, "build.sh"), 0o755); err != nil {
		t.Fatalf("chmod build.sh: %v", err)
	}
	if err := os.Chmod(filepath.Join(dir, "init.sh"), 0o755); err != nil {
		t.Fatalf("chmod init.sh: %v", err)
	}
	initGitRepoForAppTest(t, dir)
	writeFile(t, filepath.Join(dir, "README.md"), "# repo bearings\n\npending edit\n")

	taskID := strings.TrimSpace(runCLI(t, dir, []string{
		"task", "create",
		"--kind", "coding",
		"--objective", "capture repo bearings",
		"--criterion", "`./build.sh test` passes",
	}).stdout)
	run := runCLI(t, dir, []string{"run", taskID})
	if run.exitCode != 0 {
		t.Fatalf("expected coding run done, got %d stderr=%s stdout=%s", run.exitCode, run.stderr, run.stdout)
	}
	var baseline task.Baseline
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "baseline.json"))), &baseline); err != nil {
		t.Fatalf("unmarshal baseline: %v", err)
	}
	if !containsCommandHint(baseline.CommandHints, "setup", []string{"bash", "./init.sh"}) {
		t.Fatalf("expected setup command hint for init.sh, got %+v", baseline.CommandHints)
	}
	if !containsCommandHint(baseline.CommandHints, "verify", []string{"./build.sh", "test"}) {
		t.Fatalf("expected verifier command hint for build.sh test, got %+v", baseline.CommandHints)
	}
	if baseline.WorkspaceSnapshot == nil || baseline.WorkspaceSnapshot.Git == nil {
		t.Fatalf("expected workspace snapshot git summary, got %+v", baseline.WorkspaceSnapshot)
	}
	if !baseline.WorkspaceSnapshot.Git.IsRepository || !baseline.WorkspaceSnapshot.Git.Dirty {
		t.Fatalf("expected dirty git repository snapshot, got %+v", baseline.WorkspaceSnapshot.Git)
	}
	if !contains(baseline.WorkspaceSnapshot.Git.ChangedPaths, "README.md") {
		t.Fatalf("expected README.md in changed paths, got %+v", baseline.WorkspaceSnapshot.Git.ChangedPaths)
	}
	if len(baseline.WorkspaceSnapshot.Git.RecentCommits) == 0 {
		t.Fatalf("expected recent commits in workspace snapshot, got %+v", baseline.WorkspaceSnapshot.Git)
	}
}

func TestCLIWritingContinuitySnapshotAndHistory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# continuity cli\n")
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/repo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package repo\n\nfunc Add(a, b int) int { return a + b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package repo\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")
	writeFile(t, filepath.Join(dir, "build.sh"), "#!/usr/bin/env bash\nset -euo pipefail\ngo test ./...\n")
	writeFile(t, filepath.Join(dir, "init.sh"), "#!/usr/bin/env bash\nset -euo pipefail\necho bootstrap ready\n")
	if err := os.Chmod(filepath.Join(dir, "build.sh"), 0o755); err != nil {
		t.Fatalf("chmod build.sh: %v", err)
	}
	if err := os.Chmod(filepath.Join(dir, "init.sh"), 0o755); err != nil {
		t.Fatalf("chmod init.sh: %v", err)
	}
	initGitRepoForAppTest(t, dir)
	writeFile(t, filepath.Join(dir, "README.md"), "# continuity cli\n\ndirty workspace\n")

	taskID := strings.TrimSpace(runCLI(t, dir, []string{
		"task", "create",
		"--kind", "coding",
		"--title", "continuity cli",
		"--objective", "write continuity artifacts through the CLI flow",
		"--criterion", "`./build.sh test` passes",
	}).stdout)
	run := runCLI(t, dir, []string{"run", taskID})
	if run.exitCode != 0 {
		t.Fatalf("expected coding run done, got %d stderr=%s stdout=%s", run.exitCode, run.stderr, run.stdout)
	}

	var continuity task.ContinuitySnapshot
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "continuity", "latest.json"))), &continuity); err != nil {
		t.Fatalf("unmarshal continuity: %v", err)
	}
	if continuity.SnapshotID == "" || continuity.CurrentFocus.CurrentStepID == "" {
		t.Fatalf("expected continuity snapshot ids and focus, got %+v", continuity)
	}
	if len(continuity.StartupChecklist) < 4 {
		t.Fatalf("expected startup checklist with repo bearings, got %+v", continuity.StartupChecklist)
	}
	if !containsContinuityCLIRef(continuity.StartupChecklist, "criteria/latest.json") {
		t.Fatalf("expected acceptance-ledger checklist ref, got %+v", continuity.StartupChecklist)
	}
	if !containsContinuityCLICommand(continuity.StartupChecklist, []string{"git", "status", "--short"}) {
		t.Fatalf("expected git status checklist command, got %+v", continuity.StartupChecklist)
	}
	if !containsContinuityCLICommand(continuity.StartupChecklist, []string{"bash", "./init.sh"}) {
		t.Fatalf("expected setup checklist command, got %+v", continuity.StartupChecklist)
	}
	if !contains(continuity.CurrentFocus.WorkingSetPaths, "README.md") {
		t.Fatalf("expected README.md in continuity working set, got %+v", continuity.CurrentFocus)
	}
	if contains(continuity.CurrentFocus.WorkingSetPaths, ".ngen/") || contains(continuity.CurrentFocus.WorkingSetPaths, "./") {
		t.Fatalf("expected continuity working set to omit runtime/noise paths, got %+v", continuity.CurrentFocus)
	}
	historyPath := filepath.Join(dir, ".ngen", "tasks", taskID, "continuity", "history.jsonl")
	history := strings.TrimSpace(readFile(t, historyPath))
	if len(strings.Split(history, "\n")) < 2 {
		t.Fatalf("expected append-only continuity history across create/run, got %s", history)
	}
}

func TestCLIWritingCriteriaAcceptanceLedgerAndHistory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# alpha\n")

	taskID := strings.TrimSpace(runCLI(t, dir, []string{
		"task", "create",
		"--kind", "general_execution",
		"--preset", "docs_lite",
		"--title", "criteria cli",
		"--objective", "track criteria as an acceptance ledger",
		"--criterion", "README.md mentions `alpha`",
		"--criterion", "reviewer child exists",
	}).stdout)
	run := runCLI(t, dir, []string{"run", taskID})
	if run.exitCode != 0 && run.exitCode != 10 {
		t.Fatalf("expected docs_lite run to reach a stable runtime state, got %d stderr=%s stdout=%s", run.exitCode, run.stderr, run.stdout)
	}
	if run.exitCode == 10 && !strings.Contains(run.stdout, "reason=blocked_review") {
		t.Fatalf("expected blocked docs_lite run to explain blocked_review, got %d stderr=%s stdout=%s", run.exitCode, run.stderr, run.stdout)
	}

	var criteria task.CriteriaSnapshot
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "criteria", "latest.json"))), &criteria); err != nil {
		t.Fatalf("unmarshal criteria: %v", err)
	}
	if criteria.CurrentCriterionID != "SC-002" || criteria.CurrentCriterionStatement != "reviewer child exists" {
		t.Fatalf("expected current criterion to advance after README pass, got %+v", criteria)
	}
	if criteria.MetCount != 1 || criteria.OpenCount != 1 {
		t.Fatalf("expected met/open counts in acceptance ledger, got %+v", criteria)
	}
	if len(criteria.Criteria) != 2 || !criteria.Criteria[0].Passes || !criteria.Criteria[1].Selected {
		t.Fatalf("expected passes/selected fields in acceptance ledger, got %+v", criteria.Criteria)
	}
	historyPath := filepath.Join(dir, ".ngen", "tasks", taskID, "criteria", "history.jsonl")
	history := strings.TrimSpace(readFile(t, historyPath))
	if len(strings.Split(history, "\n")) < 2 {
		t.Fatalf("expected append-only criteria history across create/run, got %s", history)
	}
}

func TestMemoryShowRedactsSecretsAndBuildsTopics(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")

	taskOne := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "general_execution", "--preset", "docs_lite", "--objective", "feature api_key=supersecret rollout", "--criterion", "docs reviewed"}).stdout)
	if run := runCLI(t, dir, []string{"run", taskOne}); run.exitCode != 0 {
		t.Fatalf("expected first task done, got %d stderr=%s", run.exitCode, run.stderr)
	}
	taskTwo := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "general_execution", "--preset", "docs_lite", "--objective", "feature password=hunter2 followup", "--criterion", "docs reviewed"}).stdout)
	if run := runCLI(t, dir, []string{"run", taskTwo}); run.exitCode != 0 {
		t.Fatalf("expected second task done, got %d stderr=%s", run.exitCode, run.stderr)
	}

	memory := runCLI(t, dir, []string{"memory", "show"})
	if memory.exitCode != 0 {
		t.Fatalf("memory show failed: %d stderr=%s", memory.exitCode, memory.stderr)
	}
	if strings.Contains(memory.stdout, "supersecret") || strings.Contains(memory.stdout, "hunter2") {
		t.Fatalf("expected memory redaction, got %s", memory.stdout)
	}
	assertContains(t, memory.stdout, "## Consolidated Topics")
	assertContains(t, memory.stdout, "feature (2)")
}

func TestMemoryShowCountsRecurringTopicsPerEntry(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")

	taskOne := strings.TrimSpace(runCLI(t, dir, []string{
		"task", "create",
		"--kind", "general_execution",
		"--preset", "docs_lite",
		"--title", "scheduler one",
		"--objective", "record scheduler milestone",
		"--criterion", "docs reviewed",
	}).stdout)
	promoteOne := runCLI(t, dir, []string{
		"memory", "promote", taskOne,
		"--summary", "Scheduler milestone wake policy documented.",
		"--kind", "milestone",
	})
	if promoteOne.exitCode != 0 {
		t.Fatalf("first memory promote failed: %d stderr=%s", promoteOne.exitCode, promoteOne.stderr)
	}

	taskTwo := strings.TrimSpace(runCLI(t, dir, []string{
		"task", "create",
		"--kind", "general_execution",
		"--preset", "docs_lite",
		"--title", "scheduler two",
		"--objective", "record scheduler blocker",
		"--criterion", "docs reviewed",
	}).stdout)
	promoteTwo := runCLI(t, dir, []string{
		"memory", "promote", taskTwo,
		"--summary", "Scheduler blocker next_wake_at still needs review.",
		"--kind", "blocker",
	})
	if promoteTwo.exitCode != 0 {
		t.Fatalf("second memory promote failed: %d stderr=%s", promoteTwo.exitCode, promoteTwo.stderr)
	}

	memory := runCLI(t, dir, []string{"memory", "show"})
	if memory.exitCode != 0 {
		t.Fatalf("memory show failed: %d stderr=%s", memory.exitCode, memory.stderr)
	}
	assertContains(t, memory.stdout, "scheduler (2)")
	if strings.Contains(memory.stdout, "scheduler (4)") {
		t.Fatalf("expected per-entry topic counting, got %s", memory.stdout)
	}
}

func TestMemoryPromoteCLIWritesRedactedEntry(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")

	taskID := strings.TrimSpace(runCLI(t, dir, []string{
		"task", "create",
		"--kind", "general_execution",
		"--preset", "docs_lite",
		"--title", "memory promote",
		"--objective", "capture a reusable repo decision",
		"--criterion", "docs reviewed",
	}).stdout)

	promote := runCLI(t, dir, []string{
		"memory", "promote", taskID,
		"--summary", "Decision api_key=supersecret use project graph before spawning more tasks.",
		"--kind", "decision",
		"--ref", "progress.md",
		"--ref", " context/summary.md ",
		"--json",
	})
	if promote.exitCode != 0 {
		t.Fatalf("memory promote failed: %d stderr=%s stdout=%s", promote.exitCode, promote.stderr, promote.stdout)
	}

	var entry task.MemoryEntry
	if err := json.Unmarshal([]byte(promote.stdout), &entry); err != nil {
		t.Fatalf("unmarshal memory entry: %v", err)
	}
	if entry.Kind != task.MemoryKindTaskDecision || entry.Source != task.MemorySourceOperator {
		t.Fatalf("expected operator decision memory entry, got %+v", entry)
	}
	if strings.Contains(entry.Summary, "supersecret") {
		t.Fatalf("expected redacted summary, got %+v", entry)
	}
	if strings.Join(entry.Refs, ",") != "progress.md,context/summary.md" {
		t.Fatalf("expected normalized refs, got %+v", entry.Refs)
	}
	if entry.Scope != "task" || !contains(entry.Profiles, string(task.KindGeneral)) || entry.FreshnessStatus != "fresh" || entry.Confidence != "observed" || entry.LastValidatedRef == "" {
		t.Fatalf("expected memory governance metadata, got %+v", entry)
	}

	entries := readFile(t, filepath.Join(dir, ".ngen", "memory", "entries.jsonl"))
	assertContains(t, entries, `"kind":"task_decision"`)
	assertContains(t, entries, `"source":"operator"`)
	assertContains(t, entries, `"freshness_status":"fresh"`)
	if strings.Contains(entries, "supersecret") {
		t.Fatalf("expected entries.jsonl to redact secrets, got %s", entries)
	}

	events := readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "events.jsonl"))
	assertContains(t, events, `"type":"memory_promoted"`)

	memory := runCLI(t, dir, []string{"memory", "show"})
	if memory.exitCode != 0 {
		t.Fatalf("memory show failed: %d stderr=%s", memory.exitCode, memory.stderr)
	}
	assertContains(t, memory.stdout, "[task_decision/operator/fresh]")

	pathPromote := runCLI(t, dir, []string{
		"memory", "promote", taskID,
		"--summary", "README path remains relevant.",
		"--kind", "note",
		"--ref", "workspace:README.md",
		"--json",
	})
	if pathPromote.exitCode != 0 {
		t.Fatalf("path memory promote failed: %d stderr=%s stdout=%s", pathPromote.exitCode, pathPromote.stderr, pathPromote.stdout)
	}
	var pathEntry task.MemoryEntry
	if err := json.Unmarshal([]byte(pathPromote.stdout), &pathEntry); err != nil {
		t.Fatalf("unmarshal path memory entry: %v", err)
	}
	if !contains(pathEntry.Paths, "README.md") {
		t.Fatalf("expected path-scoped memory entry, got %+v", pathEntry)
	}
	if err := os.Remove(filepath.Join(dir, "README.md")); err != nil {
		t.Fatalf("remove README: %v", err)
	}
	memory = runCLI(t, dir, []string{"memory", "show"})
	if memory.exitCode != 0 {
		t.Fatalf("memory show after path removal failed: %d stderr=%s", memory.exitCode, memory.stderr)
	}
	assertContains(t, memory.stdout, "[task_note/operator/stale]")
}

func TestEventsTailAfterCursor(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# events\n")
	created := runCLI(t, dir, []string{
		"task", "create",
		"--kind", "general_execution",
		"--preset", "docs_lite",
		"--title", "events cursor",
		"--objective", "prove event cursor replay",
		"--criterion", "docs reviewed",
	})
	if created.exitCode != 0 {
		t.Fatalf("create task failed: %d stderr=%s", created.exitCode, created.stderr)
	}
	taskID := strings.TrimSpace(created.stdout)
	promoted := runCLI(t, dir, []string{
		"memory", "promote", taskID,
		"--summary", "Event cursor replay marker.",
		"--kind", "note",
	})
	if promoted.exitCode != 0 {
		t.Fatalf("memory promote failed: %d stderr=%s", promoted.exitCode, promoted.stderr)
	}
	lines := strings.Split(strings.TrimSpace(readFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "events.jsonl"))), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least two events, got %d lines: %+v", len(lines), lines)
	}
	var first task.Event
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("unmarshal first event: %v", err)
	}

	tailed := runCLI(t, dir, []string{"events", "tail", taskID, "--json", "--after", first.EventID})
	if tailed.exitCode != 0 {
		t.Fatalf("events tail after failed: %d stderr=%s", tailed.exitCode, tailed.stderr)
	}
	if strings.Contains(tailed.stdout, `"type":"task_created"`) {
		t.Fatalf("expected cursor replay to omit first event, got %s", tailed.stdout)
	}
	assertContains(t, tailed.stdout, `"type":"memory_promoted"`)

	missing := runCLI(t, dir, []string{"events", "tail", taskID, "--after", "EVT-missing"})
	if missing.exitCode == 0 || !strings.Contains(missing.stderr, "event cursor not found") {
		t.Fatalf("expected stale cursor diagnostic, code=%d stderr=%s", missing.exitCode, missing.stderr)
	}
}

func TestAutoMemoryPromoteDoesNotConsumeConfiguredTurnBudget(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	decisionCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(bodyBytes, &body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		text, ok := body["text"].(map[string]any)
		if !ok {
			t.Fatalf("expected text body, got %#v", body["text"])
		}
		format, ok := text["format"].(map[string]any)
		if !ok {
			t.Fatalf("expected format body, got %#v", text["format"])
		}
		if format["name"] != "ngen_provider_decision" {
			t.Fatalf("unexpected schema name: %#v", format["name"])
		}
		decisionCalls++
		switch decisionCalls {
		case 1:
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"action\":\"memory_promote\",\"summary\":\"Repo truth confirmed; keep project graph current before wider delegation.\",\"plan_explanation\":\"\",\"plan_steps\":[],\"plan_patch_operations\":[],\"project_explanation\":\"\",\"project_steps\":[],\"project_branches\":[],\"project_patch_operations\":[],\"memory_kind\":\"task_milestone\",\"memory_refs\":[\"progress.md\",\"context/summary.md\"],\"worker_id\":\"\",\"worker_role\":\"\",\"worker_objective\":\"\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
					}]
				}]
			}`))
		case 2:
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"action\":\"run\",\"summary\":\"Run the task now\",\"plan_explanation\":\"\",\"plan_steps\":[],\"plan_patch_operations\":[],\"project_explanation\":\"\",\"project_steps\":[],\"project_branches\":[],\"project_patch_operations\":[],\"memory_kind\":\"\",\"memory_refs\":[],\"worker_id\":\"\",\"worker_role\":\"\",\"worker_objective\":\"\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
					}]
				}]
			}`))
		default:
			t.Fatalf("unexpected decision call %d", decisionCalls)
		}
	}))
	defer server.Close()

	writeFile(t, filepath.Join(dir, "ngen.json"), fmt.Sprintf(`{
  "provider": {
    "mode": "responses",
    "base_url": %q,
    "model": "gpt-5.4",
    "api_key_env": "OPENAI_API_KEY",
    "auto_run_max_turns": 1
  }
}`, server.URL+"/v1"))

	taskID := strings.TrimSpace(runCLI(t, dir, []string{
		"task", "create",
		"--kind", "general_execution",
		"--preset", "docs_lite",
		"--title", "memory budget",
		"--objective", "prove memory_promote can happen before run",
		"--criterion", "docs reviewed",
	}).stdout)

	auto := runCLI(t, dir, []string{"auto", taskID, "--json"})
	if auto.exitCode != 0 {
		t.Fatalf("expected auto to reach done after memory_promote + run under one turn budget, got %d stderr=%s stdout=%s", auto.exitCode, auto.stderr, auto.stdout)
	}
	if decisionCalls != 2 {
		t.Fatalf("expected memory_promote and run decisions in a single auto pass, got %d", decisionCalls)
	}
	assertContains(t, auto.stdout, `"type":"memory_promoted"`)

	status := runCLI(t, dir, []string{"status", taskID, "--json"})
	var snapshot task.StatusSnapshot
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal status snapshot: %v", err)
	}
	if snapshot.State != task.StateDone {
		t.Fatalf("expected done state after memory_promote + run, got %+v", snapshot)
	}

	entries := readFile(t, filepath.Join(dir, ".ngen", "memory", "entries.jsonl"))
	assertContains(t, entries, `"kind":"task_milestone"`)
	assertContains(t, entries, `"source":"provider"`)
	assertContains(t, entries, `"kind":"task_completion"`)
	assertContains(t, entries, `"source":"runtime"`)
}

func TestFailedStateRecovery(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# broken state\n")

	taskID := strings.TrimSpace(runCLI(t, dir, []string{"task", "create", "--kind", "general_execution", "--preset", "docs_lite", "--objective", "recover state", "--criterion", "status recovery"}).stdout)
	statePath := filepath.Join(dir, ".ngen", "tasks", taskID, "state.json")
	writeFile(t, statePath, "{not-json")

	status := runCLI(t, dir, []string{"status", taskID, "--json"})
	var snapshot task.StatusSnapshot
	if err := json.Unmarshal([]byte(status.stdout), &snapshot); err != nil {
		t.Fatalf("unmarshal status snapshot: %v", err)
	}
	if snapshot.State != task.StateFailed || snapshot.StatusReasonCode != "failed_state" {
		t.Fatalf("expected failed_state snapshot, got state=%s reason=%s", snapshot.State, snapshot.StatusReasonCode)
	}
	assertHasFile(t, filepath.Join(dir, ".ngen", "tasks", taskID, "diagnostics"))
}

func TestRunWebRejectsUnauthenticatedNonLoopback(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NGEN_WEB_TOKEN", "")

	result := runCLI(t, dir, []string{"web", "serve", "--listen", "0.0.0.0:8765"})
	if result.exitCode == 0 {
		t.Fatalf("expected unauthenticated non-loopback web serve to fail")
	}
	if !strings.Contains(result.stderr, "refuses unauthenticated non-loopback") {
		t.Fatalf("unexpected stderr: %s", result.stderr)
	}
}

func TestWebListenRequiresTokenPolicy(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8765", "localhost:8765", "[::1]:8765"} {
		if webListenRequiresToken(addr) {
			t.Fatalf("loopback address should not require token by address policy: %s", addr)
		}
	}
	for _, addr := range []string{"0.0.0.0:8765", ":8765", "[::]:8765", "192.168.1.10:8765", "example.com:8765"} {
		if !webListenRequiresToken(addr) {
			t.Fatalf("non-loopback address should require token by address policy: %s", addr)
		}
	}
}

func TestBuildFmtIgnoresRunArtifacts(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	ignored := filepath.Join(repoRoot, "internal", "tui", "real_task_tests", "issue-test", "runs", "bad", "workspace", "bad.go")
	writeFile(t, ignored, "package bad\nfunc invalid(\n")
	t.Cleanup(func() {
		_ = os.RemoveAll(filepath.Join(repoRoot, "internal", "tui", "real_task_tests", "issue-test"))
	})

	cmd := exec.Command("bash", "./build.sh", "fmt")
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build.sh fmt should ignore run artifacts, err=%v output=%s", err, string(out))
	}
}

type cliResult struct {
	exitCode int
	stdout   string
	stderr   string
}

func runCLI(t *testing.T, dir string, args []string) cliResult {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() {
		if err := os.Chdir(prev); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	}()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := RunCLI(context.Background(), args, &stdout, &stderr)
	return cliResult{
		exitCode: code,
		stdout:   strings.TrimSpace(stdout.String()),
		stderr:   strings.TrimSpace(stderr.String()),
	}
}

func runExecForTest(t *testing.T, cwd string, args []string, stdin string) cliResult {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() {
		if err := os.Chdir(prev); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	}()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runExec(context.Background(), args, &stdout, &stderr, strings.NewReader(stdin))
	return cliResult{
		exitCode: code,
		stdout:   strings.TrimSpace(stdout.String()),
		stderr:   strings.TrimSpace(stderr.String()),
	}
}

func decodeStreamOutput(t *testing.T, stdout string) []multica.StreamOutputMessage {
	t.Helper()
	var messages []multica.StreamOutputMessage
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg multica.StreamOutputMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("unmarshal stream line %q: %v", line, err)
		}
		messages = append(messages, msg)
	}
	return messages
}

func writeProviderConfig(t *testing.T, dir string, provider map[string]any) {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"provider": provider,
	})
	if err != nil {
		t.Fatalf("marshal provider config: %v", err)
	}
	writeFile(t, filepath.Join(dir, "ngen.json"), string(data))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file %s: %v", path, err)
	}
}

func assertHasFile(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected files in %s", dir)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file %s: %v", path, err)
	}
	return string(data)
}

func assertContains(t *testing.T, body, want string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Fatalf("expected %q in %q", want, body)
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func containsCommandHint(items []task.CommandHint, wantKind string, wantCommand []string) bool {
	for _, item := range items {
		if strings.TrimSpace(item.Kind) != strings.TrimSpace(wantKind) {
			continue
		}
		if strings.Join(item.Command, "\x00") == strings.Join(wantCommand, "\x00") {
			return true
		}
	}
	return false
}

func containsContinuityCLICommand(items []task.ContinuityChecklistItem, wantCommand []string) bool {
	for _, item := range items {
		if strings.Join(item.Command, "\x00") == strings.Join(wantCommand, "\x00") {
			return true
		}
	}
	return false
}

func containsContinuityCLIRef(items []task.ContinuityChecklistItem, wantRef string) bool {
	for _, item := range items {
		if item.Ref == wantRef {
			return true
		}
	}
	return false
}

func containsSection(items []task.ContextSection, want string) bool {
	for _, item := range items {
		if item.Name == want {
			return true
		}
	}
	return false
}

func initGitRepoForAppTest(t *testing.T, dir string) {
	t.Helper()
	runGitForAppTest(t, dir, "init")
	runGitForAppTest(t, dir, "config", "user.email", "ngen@example.com")
	runGitForAppTest(t, dir, "config", "user.name", "NGEN Test")
	runGitForAppTest(t, dir, "add", ".")
	runGitForAppTest(t, dir, "commit", "-m", "init")
}

func runGitForAppTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(output))
	}
}
