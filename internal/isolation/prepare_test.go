package isolation

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPrepareAutoFallsBackToCopyOutsideGit(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "hello.txt"), []byte("world"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	result, err := Prepare(Request{
		SessionID:     "session-copy",
		ParentWorkdir: src,
		RequestedMode: "auto",
		RootDir:       t.TempDir(),
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if result.Mode != "copy" {
		t.Fatalf("expected copy mode, got %#v", result)
	}
	if _, err := os.Stat(filepath.Join(result.Workdir, "hello.txt")); err != nil {
		t.Fatalf("expected copied file: %v", err)
	}
}

func TestPrepareGitModeRequiresRepository(t *testing.T) {
	_, err := Prepare(Request{
		SessionID:     "session-git",
		ParentWorkdir: t.TempDir(),
		RequestedMode: "git",
		RootDir:       t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected git mode to fail outside repository")
	}
}

func TestPrepareRejectsSymlinkedRootInsideParent(t *testing.T) {
	parent := t.TempDir()
	if err := os.MkdirAll(filepath.Join(parent, ".go-cli-agent", "_worktrees"), 0o700); err != nil {
		t.Fatalf("mkdir internal root: %v", err)
	}
	rootLink := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(filepath.Join(parent, ".go-cli-agent", "_worktrees"), rootLink); err != nil {
		t.Fatalf("symlink root: %v", err)
	}
	_, err := Prepare(Request{
		SessionID:     "session-copy",
		ParentWorkdir: parent,
		RequestedMode: "copy",
		RootDir:       rootLink,
	})
	if err == nil {
		t.Fatal("expected symlinked isolation root inside parent to be rejected")
	}
}

func TestPrepareAutoUsesGitWorktreeInsideRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for worktree test")
	}
	repo := t.TempDir()
	runGit(t, repo, "init")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "-c", "user.email=test@example.com", "-c", "user.name=test", "commit", "-m", "init")

	result, err := Prepare(Request{
		SessionID:     "session-git-auto",
		ParentWorkdir: repo,
		RequestedMode: "auto",
		RootDir:       t.TempDir(),
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if result.Mode != "git" {
		t.Fatalf("expected git mode, got %#v", result)
	}
	if _, err := os.Stat(filepath.Join(result.Workdir, "README.md")); err != nil {
		t.Fatalf("expected README in worktree: %v", err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, string(output))
	}
}
