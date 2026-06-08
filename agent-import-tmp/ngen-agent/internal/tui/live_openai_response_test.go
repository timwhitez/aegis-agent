package tui

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	ngenrt "ngen/internal/runtime"
	"ngen/internal/task"
)

type liveProviderEnv struct {
	BaseURL string
	Model   string
	APIKey  string
}

func TestTUIOpenAIResponseLiveRunPTYCompletesTask(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponseRepairService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 100, 28, 4*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.SendLine("/run")
	pty.WaitForText(30*time.Second, "Running prompt...")
	waitForTaskState(t, svc, spec.TaskID, task.StateDone, 3*time.Minute)
	pty.WaitForText(30*time.Second, "Done")
	pty.cleanup()
}

func TestTUIOpenAIResponseLiveRunPTYRepairsWorkspaceBackedCriteria(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponseDocsCriteriaService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 104, 30, 4*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.SendLine("/run")
	pty.WaitForText(30*time.Second, "Running prompt...")
	waitForTaskState(t, svc, spec.TaskID, task.StateDone, 3*time.Minute)
	readmeBytes, err := os.ReadFile(filepath.Join(svc.Store.WorkspaceRoot, "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	readme := string(readmeBytes)
	if !strings.Contains(readme, "timeout_seconds") {
		t.Fatalf("expected README.md to mention timeout_seconds, got:\n%s", readme)
	}
	report, err := svc.Store.LoadVerification(spec.TaskID)
	if err != nil {
		t.Fatalf("load verification: %v", err)
	}
	if report.Status != "passed" {
		t.Fatalf("expected verification passed after workspace-backed repair, got %+v", report)
	}
	pty.WaitForText(30*time.Second, "Done")
	pty.cleanup()
}

func TestTUIOpenAIResponseLiveRunPTYVerifierSequenceTask(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponseVerifierSequenceService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 6*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.SendLine("/run")
	pty.WaitForText(30*time.Second, "Running prompt...")
	waitForTaskState(t, svc, spec.TaskID, task.StateDone, 5*time.Minute)

	report, err := svc.Store.LoadVerification(spec.TaskID)
	if err != nil {
		t.Fatalf("load verification: %v", err)
	}
	if !hasVerificationCommand(report, "./build.sh", "test") {
		t.Fatalf("expected verification report to include ./build.sh test, got %+v", report.Checks)
	}
	if !hasVerificationCommand(report, "./build.sh", "build") {
		t.Fatalf("expected verification report to include ./build.sh build, got %+v", report.Checks)
	}
	pty.WaitForText(30*time.Second, "Done")
	pty.cleanup()
}

func TestTUIOpenAIResponseLiveWorkerSpawnReviewerPTY(t *testing.T) {
	live := requireLiveOpenAIResponseEnv(t)
	svc, spec := newLiveOpenAIResponseWorkerReviewService(t, live)
	pty := startTUIHarnessSizedWithTimeout(t, svc, 110, 30, 4*time.Minute, spec.TaskID)
	pty.WaitForText(20*time.Second, "Chat", "TUI session started.")
	pty.SendLine("/run")
	pty.WaitForText(30*time.Second, "Running prompt...")
	waitForTaskState(t, svc, spec.TaskID, task.StateDone, 3*time.Minute)
	pty.WaitForText(30*time.Second, "Done")

	pty.FocusComposer()
	pty.SendLine("/worker_spawn reviewer review the current parent output for correctness")
	var worker task.WorkerContract
	waitForCondition(t, 3*time.Minute, func() (bool, error) {
		workers, err := svc.ListWorkers(context.Background(), spec.TaskID)
		if err != nil {
			return false, err
		}
		if len(workers) != 1 {
			return false, nil
		}
		worker = workers[0]
		return true, nil
	})
	var result task.WorkerResult
	waitForCondition(t, 3*time.Minute, func() (bool, error) {
		loaded, err := svc.Store.LoadWorkerResult(spec.TaskID, worker.WorkerID)
		if err != nil {
			return false, err
		}
		result = loaded
		return result.CompletionStatus == "accepted" && result.ReviewStatus == "clear" && result.VerificationStatus == "passed", nil
	})

	pty.WaitForText(45*time.Second, "WORKER_SPAWNED", worker.WorkerID, "reviewer")
	pty.cleanup()
}

func requireLiveOpenAIResponseEnv(t *testing.T) liveProviderEnv {
	t.Helper()
	if os.Getenv("NGEN_RUN_TUI_OPENAI_RESPONSE_LIVE") != "1" {
		t.Skip("set NGEN_RUN_TUI_OPENAI_RESPONSE_LIVE=1 to run the live openai-response PTY tests")
	}
	baseURL := trimmedEnvAny("NGEN_OPENAI_RESPONSE_BASE_URL", "NGEN_BASE_URL")
	if baseURL == "" {
		baseURL = "http://69.63.215.40:24634/v1"
	}
	model := trimmedEnvAny("NGEN_OPENAI_RESPONSE_MODEL", "NGEN_MODEL")
	if model == "" {
		model = "gpt-5.4"
	}
	apiKey := trimmedEnv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Fatal("OPENAI_API_KEY must be set for live openai-response testing")
	}
	return liveProviderEnv{
		BaseURL: baseURL,
		Model:   model,
		APIKey:  apiKey,
	}
}

func newLiveOpenAIResponseRepairService(t *testing.T, live liveProviderEnv) (*ngenrt.Service, task.Spec) {
	t.Helper()
	dir := liveScenarioWorkspace(t)
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/live\n\ngo 1.24.2\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package live\n\nfunc Add(a, b int) int { return a - b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package live\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"Add(2,3)=%d, want 5\", got)\n\t}\n}\n")
	return newLiveOpenAIResponseService(t, dir, live, func(cfg *task.Config) task.TaskFile {
		cfg.Verification.CodingCommands = [][]string{{"go", "test", "./..."}}
		return task.TaskFile{
			Kind:      task.KindCoding,
			Title:     "live openai-response tui run",
			Objective: "fix the failing Add implementation so go test passes",
			SuccessCriteria: []task.SuccessCriterion{
				{ID: "SC-001", Statement: "go test passes"},
			},
			WorkspaceRoot: dir,
		}
	})
}

func newLiveOpenAIResponseDocsCriteriaService(t *testing.T, live liveProviderEnv) (*ngenrt.Service, task.Spec) {
	t.Helper()
	dir := liveScenarioWorkspace(t)
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/live\n\ngo 1.24.2\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package live\n\nfunc Add(a, b int) int { return a + b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package live\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"Add(2,3)=%d, want 5\", got)\n\t}\n}\n")
	writeFile(t, filepath.Join(dir, "README.md"), "# Live Docs Scenario\n\nThis README is missing the required config token.\n")
	return newLiveOpenAIResponseService(t, dir, live, func(cfg *task.Config) task.TaskFile {
		cfg.Verification.CodingCommands = [][]string{{"go", "test", "./..."}}
		return task.TaskFile{
			Kind:      task.KindCoding,
			Title:     "workspace-backed criteria repair",
			Objective: "keep tests passing and update README.md so it mentions timeout_seconds",
			SuccessCriteria: []task.SuccessCriterion{
				{ID: "SC-001", Statement: "go test passes"},
				{ID: "SC-002", Statement: "README.md mentions `timeout_seconds`"},
			},
			WorkspaceRoot: dir,
		}
	})
}

func newLiveOpenAIResponseVerifierSequenceService(t *testing.T, live liveProviderEnv) (*ngenrt.Service, task.Spec) {
	t.Helper()
	dir := liveScenarioWorkspace(t)
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/live\n\ngo 1.24.2\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package live\n\nfunc Add(a, b int) int { return a - b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package live\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"Add(2,3)=%d, want 5\", got)\n\t}\n}\n")
	writeFile(t, filepath.Join(dir, "build.sh"), "#!/usr/bin/env bash\nset -euo pipefail\ncase \"${1:-}\" in\n  test)\n    go test ./...\n    ;;\n  build)\n    go build ./...\n    ;;\n  *)\n    echo \"usage: ./build.sh [test|build]\" >&2\n    exit 2\n    ;;\nesac\n")
	if err := os.Chmod(filepath.Join(dir, "build.sh"), 0o755); err != nil {
		t.Fatalf("chmod build.sh: %v", err)
	}
	return newLiveOpenAIResponseService(t, dir, live, func(cfg *task.Config) task.TaskFile {
		cfg.Verification.CodingCommands = [][]string{
			{"./build.sh", "test"},
			{"./build.sh", "build"},
		}
		cfg.Verification.CodingTimeoutSeconds = 90
		return task.TaskFile{
			Kind:      task.KindCoding,
			Title:     "verifier sequence live tui",
			Objective: "fix Add so ./build.sh test and ./build.sh build both pass",
			SuccessCriteria: []task.SuccessCriterion{
				{ID: "SC-001", Statement: "`./build.sh test` passes"},
				{ID: "SC-002", Statement: "`./build.sh build` passes"},
			},
			WorkspaceRoot: dir,
		}
	})
}

func newLiveOpenAIResponseWorkerReviewService(t *testing.T, live liveProviderEnv) (*ngenrt.Service, task.Spec) {
	t.Helper()
	dir := liveScenarioWorkspace(t)
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/live\n\ngo 1.24.2\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package live\n\nfunc Add(a, b int) int { return a + b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package live\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"Add(2,3)=%d, want 5\", got)\n\t}\n}\n")
	writeFile(t, filepath.Join(dir, "README.md"), "# Live Review Target\n\nParent workspace prepared for reviewer inspection.\n")
	return newLiveOpenAIResponseService(t, dir, live, func(cfg *task.Config) task.TaskFile {
		cfg.Verification.CodingCommands = [][]string{{"go", "test", "./..."}}
		return task.TaskFile{
			Kind:      task.KindCoding,
			Title:     "worker review live tui",
			Objective: "keep the passing workspace stable and allow a reviewer worker to inspect it",
			SuccessCriteria: []task.SuccessCriterion{
				{ID: "SC-001", Statement: "go test passes"},
			},
			WorkspaceRoot: dir,
		}
	})
}

func newLiveOpenAIResponseService(t *testing.T, dir string, live liveProviderEnv, customize func(cfg *task.Config) task.TaskFile) (*ngenrt.Service, task.Spec) {
	t.Helper()

	cfg := task.DefaultConfig()
	cfg.Provider.Mode = "openai-response"
	cfg.Provider.BaseURL = live.BaseURL
	cfg.Provider.APIKeyEnv = "OPENAI_API_KEY"
	cfg.Provider.Model = live.Model
	cfg.Provider.DecisionTimeoutSeconds = 90
	cfg.Provider.AutoRunMaxTurns = 1
	cfg.Provider.CodingExecutionCommandBudget = 2
	cfg.Provider.CodingExecutionCommandTimeoutSeconds = 60
	taskFile := customize(&cfg)
	writeConfigFile(t, dir, cfg)
	t.Setenv("OPENAI_API_KEY", live.APIKey)

	svc := ngenrt.New(dir, cfg)
	spec, err := svc.Create(context.Background(), taskFile)
	if err != nil {
		t.Fatalf("create live task: %v", err)
	}
	return svc, spec
}

func trimmedEnv(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

func trimmedEnvAny(keys ...string) string {
	for _, key := range keys {
		if value := trimmedEnv(key); value != "" {
			return value
		}
	}
	return ""
}

func liveScenarioWorkspace(t *testing.T) string {
	t.Helper()
	if dir := trimmedEnv("NGEN_TUI_LIVE_WORKSPACE_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir live workspace: %v", err)
		}
		return dir
	}
	if root := trimmedEnv("NGEN_TUI_LIVE_ARTIFACT_DIR"); root != "" {
		dir := filepath.Join(root, "workspace")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir live artifact workspace: %v", err)
		}
		return dir
	}
	return t.TempDir()
}

func hasVerificationCommand(report task.VerificationReport, command ...string) bool {
	for _, check := range report.Checks {
		if reflect.DeepEqual(check.Command, command) && check.Status == "passed" {
			return true
		}
	}
	return false
}

func TestRequireLiveOpenAIResponseEnvAcceptsLegacyBaseModelAliases(t *testing.T) {
	t.Setenv("NGEN_RUN_TUI_OPENAI_RESPONSE_LIVE", "1")
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("NGEN_BASE_URL", "http://legacy.example/v1")
	t.Setenv("NGEN_MODEL", "legacy-model")

	live := requireLiveOpenAIResponseEnv(t)
	if live.BaseURL != "http://legacy.example/v1" {
		t.Fatalf("expected legacy base url alias, got %q", live.BaseURL)
	}
	if live.Model != "legacy-model" {
		t.Fatalf("expected legacy model alias, got %q", live.Model)
	}
	if live.APIKey != "test-key" {
		t.Fatalf("expected api key passthrough, got %q", live.APIKey)
	}
}

func TestRequireLiveOpenAIResponseEnvPrefersTUISpecificVars(t *testing.T) {
	t.Setenv("NGEN_RUN_TUI_OPENAI_RESPONSE_LIVE", "1")
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("NGEN_BASE_URL", "http://legacy.example/v1")
	t.Setenv("NGEN_MODEL", "legacy-model")
	t.Setenv("NGEN_OPENAI_RESPONSE_BASE_URL", "http://tui.example/v1")
	t.Setenv("NGEN_OPENAI_RESPONSE_MODEL", "tui-model")

	live := requireLiveOpenAIResponseEnv(t)
	if live.BaseURL != "http://tui.example/v1" {
		t.Fatalf("expected tui-specific base url to win, got %q", live.BaseURL)
	}
	if live.Model != "tui-model" {
		t.Fatalf("expected tui-specific model to win, got %q", live.Model)
	}
}
