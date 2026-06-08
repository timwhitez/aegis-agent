package verify

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ngen/internal/task"
)

func TestCodingVerifierTimesOut(t *testing.T) {
	dir := t.TempDir()
	writeVerifyFile(t, filepath.Join(dir, "go.mod"), "module example.com/hang\n\ngo 1.24.0\n")
	writeVerifyFile(t, filepath.Join(dir, "worker.go"), "package main\n\nfunc Value() string { return \"ok\" }\n")
	writeVerifyFile(t, filepath.Join(dir, "worker_test.go"), "package main\n\nimport \"testing\"\n\nfunc TestBlocksForever(t *testing.T) {\n\tselect {}\n}\n")

	cfg := task.DefaultConfig()
	cfg.Verification.CodingTimeoutSeconds = 1
	pipeline := New(cfg)

	report := pipeline.Run(context.Background(), task.Spec{
		TaskID:        "TASK-001",
		Kind:          task.KindCoding,
		WorkspaceRoot: dir,
	})
	if report.Status != "failed" {
		t.Fatalf("expected failed report, got %+v", report)
	}
	if len(report.Checks) == 0 {
		t.Fatalf("expected at least one check, got %+v", report)
	}
	if report.Checks[0].Status != "failed" {
		t.Fatalf("expected failed go_test check, got %+v", report.Checks[0])
	}
	if !strings.Contains(report.Checks[0].Summary, "timed out after 1s") {
		t.Fatalf("expected timeout summary, got %+v", report.Checks[0])
	}
}

func TestCodingVerifierUsesCriterionCommandWhenSpecified(t *testing.T) {
	dir := t.TempDir()
	writeVerifyFile(t, filepath.Join(dir, "go.mod"), "module example.com/repo\n\ngo 1.24.0\n")
	writeVerifyFile(t, filepath.Join(dir, "cmd", "demo", "main.go"), "package main\n\nimport (\n\t\"fmt\"\n\t\"example.com/repo/internal/sum\"\n)\n\nfunc main() { fmt.Println(sum.Add(2, 3)) }\n")
	writeVerifyFile(t, filepath.Join(dir, "internal", "sum", "sum.go"), "package sum\n\nfunc Add(a, b int) int { return a + b }\n")
	writeVerifyFile(t, filepath.Join(dir, "internal", "sum", "sum_test.go"), "package sum\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")
	writeVerifyFile(t, filepath.Join(dir, "codex", "archive", "bad.go"), "package broken\n\nfunc Broken() {\n")
	writeVerifyFile(t, filepath.Join(dir, "build.sh"), "#!/usr/bin/env bash\nset -euo pipefail\ngo test ./cmd/... ./internal/...\n")
	if err := os.Chmod(filepath.Join(dir, "build.sh"), 0o755); err != nil {
		t.Fatalf("chmod build.sh: %v", err)
	}

	pipeline := New(task.DefaultConfig())
	report := pipeline.Run(context.Background(), task.Spec{
		TaskID:        "TASK-criterion",
		Kind:          task.KindCoding,
		WorkspaceRoot: dir,
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "`./build.sh test` passes"},
		},
	})
	if report.Status != "passed" {
		t.Fatalf("expected passed report, got %+v", report)
	}
	if len(report.Checks) == 0 || report.Checks[0].Status != "passed" {
		t.Fatalf("expected passed check, got %+v", report.Checks)
	}
	if !strings.Contains(report.Checks[0].Summary, "./build.sh test") {
		t.Fatalf("expected verifier summary to mention build.sh test, got %+v", report.Checks[0])
	}
	if !sameStrings(report.Checks[0].Command, []string{"./build.sh", "test"}) {
		t.Fatalf("expected verifier check command to match build.sh test, got %+v", report.Checks[0].Command)
	}
}

func TestCodingVerifierRunsCriterionCommandSequence(t *testing.T) {
	dir := t.TempDir()
	writeVerifyFile(t, filepath.Join(dir, "go.mod"), "module example.com/reposeq\n\ngo 1.24.0\n")
	writeVerifyFile(t, filepath.Join(dir, "cmd", "demo", "main.go"), "package main\n\nimport (\n\t\"fmt\"\n\t\"example.com/reposeq/internal/sum\"\n)\n\nfunc main() { fmt.Println(sum.Add(2, 3)) }\n")
	writeVerifyFile(t, filepath.Join(dir, "internal", "sum", "sum.go"), "package sum\n\nfunc Add(a, b int) int { return a + b }\n")
	writeVerifyFile(t, filepath.Join(dir, "internal", "sum", "sum_test.go"), "package sum\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")
	writeVerifyFile(t, filepath.Join(dir, "build.sh"), "#!/usr/bin/env bash\nset -euo pipefail\n\ncase \"${1:-}\" in\n  test)\n    go test ./internal/...\n    ;;\n  build)\n    go build -buildvcs=false ./cmd/demo\n    ;;\n  *)\n    echo \"unsupported\" >&2\n    exit 2\n    ;;\n esac\n")
	if err := os.Chmod(filepath.Join(dir, "build.sh"), 0o755); err != nil {
		t.Fatalf("chmod build.sh: %v", err)
	}

	pipeline := New(task.DefaultConfig())
	report := pipeline.Run(context.Background(), task.Spec{
		TaskID:        "TASK-sequence",
		Kind:          task.KindCoding,
		WorkspaceRoot: dir,
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "`./build.sh test` passes"},
			{ID: "SC-002", Statement: "`./build.sh build` passes"},
		},
	})
	if report.Status != "passed" {
		t.Fatalf("expected passed report, got %+v", report)
	}
	if len(report.Checks) != 2 {
		t.Fatalf("expected two verifier checks, got %+v", report.Checks)
	}
	if !sameStrings(report.Checks[0].Command, []string{"./build.sh", "test"}) {
		t.Fatalf("expected first verifier command to be build.sh test, got %+v", report.Checks[0].Command)
	}
	if !sameStrings(report.Checks[1].Command, []string{"./build.sh", "build"}) {
		t.Fatalf("expected second verifier command to be build.sh build, got %+v", report.Checks[1].Command)
	}
}

func TestCodingVerifierUsesConfiguredCommandSequence(t *testing.T) {
	dir := t.TempDir()
	writeVerifyFile(t, filepath.Join(dir, "go.mod"), "module example.com/reposeqcfg\n\ngo 1.24.0\n")
	writeVerifyFile(t, filepath.Join(dir, "cmd", "demo", "main.go"), "package main\n\nfunc main() {}\n")
	writeVerifyFile(t, filepath.Join(dir, "internal", "sum", "sum.go"), "package sum\n\nfunc Add(a, b int) int { return a + b }\n")
	writeVerifyFile(t, filepath.Join(dir, "internal", "sum", "sum_test.go"), "package sum\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")
	writeVerifyFile(t, filepath.Join(dir, "README.md"), "# configured verifier sequence\n")

	cfg := task.DefaultConfig()
	cfg.Verification.CodingCommands = [][]string{
		{"go", "test", "./internal/..."},
		{"go", "build", "-buildvcs=false", "./cmd/demo"},
	}
	pipeline := New(cfg)
	report := pipeline.Run(context.Background(), task.Spec{
		TaskID:        "TASK-config-sequence",
		Kind:          task.KindCoding,
		WorkspaceRoot: dir,
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "`go test ./internal/...` passes"},
			{ID: "SC-002", Statement: "`go build -buildvcs=false ./cmd/demo` passes"},
		},
	})
	if report.Status != "passed" {
		t.Fatalf("expected passed report, got %+v", report)
	}
	if len(report.Checks) != 2 {
		t.Fatalf("expected two verifier checks, got %+v", report.Checks)
	}
	if !sameStrings(report.Checks[0].Command, []string{"go", "test", "./internal/..."}) {
		t.Fatalf("expected first configured command to be go test ./internal/..., got %+v", report.Checks[0].Command)
	}
	if !sameStrings(report.Checks[1].Command, []string{"go", "build", "-buildvcs=false", "./cmd/demo"}) {
		t.Fatalf("expected second configured command to be go build -buildvcs=false ./cmd/demo, got %+v", report.Checks[1].Command)
	}
}

func TestCaptureWorkspaceSnapshotLimitsGitStatusToWorkspaceRoot(t *testing.T) {
	repoRoot := filepath.Join(t.TempDir(), "repo")
	workspaceRoot := filepath.Join(repoRoot, "nested", "workspace")
	writeVerifyFile(t, filepath.Join(repoRoot, "root.txt"), "root\n")
	writeVerifyFile(t, filepath.Join(workspaceRoot, "README.md"), "# workspace\n")
	runVerifyGit(t, repoRoot, "init")
	runVerifyGit(t, repoRoot, "config", "user.email", "ngen@example.com")
	runVerifyGit(t, repoRoot, "config", "user.name", "NGEN Verify")
	runVerifyGit(t, repoRoot, "add", ".")
	runVerifyGit(t, repoRoot, "commit", "-m", "init")
	writeVerifyFile(t, filepath.Join(repoRoot, "root.txt"), "root dirty\n")
	writeVerifyFile(t, filepath.Join(workspaceRoot, "README.md"), "# workspace\n\ndirty\n")

	snapshot := CaptureWorkspaceSnapshot(context.Background(), workspaceRoot)
	if snapshot == nil || snapshot.Git == nil {
		t.Fatalf("expected git snapshot, got %+v", snapshot)
	}
	if !snapshot.Git.Dirty {
		t.Fatalf("expected dirty workspace snapshot, got %+v", snapshot.Git)
	}
	foundReadme := false
	for _, path := range snapshot.Git.ChangedPaths {
		if strings.HasPrefix(path, "../") {
			t.Fatalf("expected changed paths to stay within workspace root, got %+v", snapshot.Git.ChangedPaths)
		}
		if path == "README.md" {
			foundReadme = true
		}
	}
	if !foundReadme {
		t.Fatalf("expected README.md in changed paths, got %+v", snapshot.Git.ChangedPaths)
	}
}

func writeVerifyFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func runVerifyGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v output=%s", args, err, string(output))
	}
}
