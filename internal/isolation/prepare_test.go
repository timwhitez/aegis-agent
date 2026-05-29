package isolation

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestPrepareRejectsSymlinkedSessionTargetOutsideRoot(t *testing.T) {
	parent := t.TempDir()
	if err := os.WriteFile(filepath.Join(parent, "hello.txt"), []byte("world"), 0o600); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "session-copy")); err != nil {
		t.Fatalf("symlink session target: %v", err)
	}

	_, err := Prepare(Request{
		SessionID:     "session-copy",
		ParentWorkdir: parent,
		RequestedMode: "copy",
		RootDir:       root,
	})
	if err == nil {
		t.Fatal("expected symlinked session target outside isolation root to be rejected")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "hello.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("outside target was modified, stat err: %v", statErr)
	}
}

func TestPrepareCopyRejectsPreexistingOutputSymlink(t *testing.T) {
	parent := t.TempDir()
	if err := os.WriteFile(filepath.Join(parent, "hello.txt"), []byte("world"), 0o600); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	root := t.TempDir()
	target := filepath.Join(root, "session-copy")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatalf("mkdir session target: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(target, "hello.txt")); err != nil {
		t.Fatalf("symlink output file: %v", err)
	}

	_, err := Prepare(Request{
		SessionID:     "session-copy",
		ParentWorkdir: parent,
		RequestedMode: "copy",
		RootDir:       root,
	})
	if err == nil {
		t.Fatal("expected preexisting output symlink to be rejected")
	}
	data, readErr := os.ReadFile(outside)
	if readErr != nil {
		t.Fatalf("read outside file: %v", readErr)
	}
	if string(data) != "keep" {
		t.Fatalf("outside file was overwritten: %q", data)
	}
}

func TestCopyFileRejectsSymlinkedDestinationParentDuringRename(t *testing.T) {
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "hello.txt")
	if err := os.WriteFile(src, []byte("world"), 0o600); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	dstRoot := t.TempDir()
	dstParent := filepath.Join(dstRoot, "target")
	if err := os.MkdirAll(dstParent, 0o700); err != nil {
		t.Fatalf("mkdir destination parent: %v", err)
	}
	outside := t.TempDir()

	restore := beforeCopyFileRename
	beforeCopyFileRename = func(tmpPath, dst string) error {
		if err := os.RemoveAll(dstParent); err != nil {
			return err
		}
		if err := os.Symlink(outside, dstParent); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(outside, filepath.Base(tmpPath)), []byte("outside"), 0o600)
	}
	defer func() {
		beforeCopyFileRename = restore
	}()

	err := copyFile(src, filepath.Join(dstParent, "hello.txt"), 0o600)
	if err == nil {
		t.Fatal("expected symlinked destination parent to be rejected")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "hello.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("outside target should not be created, stat err=%v", statErr)
	}
}

func TestCopyFileRejectsSymlinkedDestinationParentBeforeTempCreate(t *testing.T) {
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "secret.txt")
	if err := os.WriteFile(src, []byte("copy-secret"), 0o600); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	dstRoot := t.TempDir()
	dstParent := filepath.Join(dstRoot, "target")
	if err := os.MkdirAll(dstParent, 0o700); err != nil {
		t.Fatalf("mkdir destination parent: %v", err)
	}
	outside := t.TempDir()
	symlinkProbe := filepath.Join(dstRoot, "symlink-probe")
	if err := os.Symlink(outside, symlinkProbe); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.Remove(symlinkProbe); err != nil {
		t.Fatalf("remove symlink probe: %v", err)
	}

	restore := beforeCopyFileTempCreate
	beforeCopyFileTempCreate = func(parent string) error {
		if parent != dstParent {
			t.Fatalf("unexpected copy temp parent %s", parent)
		}
		if err := os.RemoveAll(dstParent); err != nil {
			return err
		}
		return os.Symlink(outside, dstParent)
	}
	defer func() {
		beforeCopyFileTempCreate = restore
	}()

	err := copyFile(src, filepath.Join(dstParent, "secret.txt"), 0o600)
	if err == nil {
		t.Fatal("expected symlinked destination parent to be rejected before temp create")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink path error, got %v", err)
	}
	entries, readErr := os.ReadDir(outside)
	if readErr != nil {
		t.Fatalf("read outside dir: %v", readErr)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".secret.txt.") {
			t.Fatalf("copy temp file must not be created through symlinked parent, found %s", entry.Name())
		}
	}
	if _, statErr := os.Stat(filepath.Join(outside, "secret.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("outside target should not be created, stat err=%v", statErr)
	}
}

func TestCopyFileRejectsSymlinkedSourceParentBeforeOpen(t *testing.T) {
	srcRoot := t.TempDir()
	srcParent := filepath.Join(srcRoot, "source")
	if err := os.MkdirAll(srcParent, 0o700); err != nil {
		t.Fatalf("mkdir source parent: %v", err)
	}
	src := filepath.Join(srcParent, "secret.txt")
	if err := os.WriteFile(src, []byte("inside-secret"), 0o600); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside-secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "secret.txt")

	restore := beforeCopyFileSourceOpen
	beforeCopyFileSourceOpen = func(path string) error {
		if path != src {
			t.Fatalf("unexpected copy source %s", path)
		}
		if err := os.RemoveAll(srcParent); err != nil {
			return err
		}
		return os.Symlink(outside, srcParent)
	}
	defer func() {
		beforeCopyFileSourceOpen = restore
	}()

	err := copyFile(src, dst, 0o600)
	if err == nil {
		t.Fatal("expected symlinked source parent to be rejected")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink path error, got %v", err)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatalf("destination should not be created from outside source, stat err=%v", statErr)
	}
}

func TestPrepareCopyPreservesSourceSymlinkWithoutFollowing(t *testing.T) {
	parent := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(parent, "linked.txt")); err != nil {
		t.Fatalf("symlink source file: %v", err)
	}

	result, err := Prepare(Request{
		SessionID:     "session-copy",
		ParentWorkdir: parent,
		RequestedMode: "copy",
		RootDir:       t.TempDir(),
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	target := filepath.Join(result.Workdir, "linked.txt")
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatalf("lstat copied link: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected copied entry to remain a symlink, got mode %v", info.Mode())
	}
	link, err := os.Readlink(target)
	if err != nil {
		t.Fatalf("read copied symlink: %v", err)
	}
	if link != outside {
		t.Fatalf("unexpected copied symlink target %q, want %q", link, outside)
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

func TestPrepareAutoCopiesIgnoredWorkspaceInsideRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for worktree detection")
	}
	repo := t.TempDir()
	runGit(t, repo, "init")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("validation/runs/\n"), 0o600); err != nil {
		t.Fatalf("write gitignore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("tracked root"), 0o600); err != nil {
		t.Fatalf("write root readme: %v", err)
	}
	runGit(t, repo, "add", ".gitignore", "README.md")
	runGit(t, repo, "-c", "user.email=test@example.com", "-c", "user.name=test", "commit", "-m", "init")

	workspace := filepath.Join(repo, "validation", "runs", "current", "workspaces", "platform_py")
	if err := os.MkdirAll(filepath.Join(workspace, "tests"), 0o700); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("staged workspace"), 0o600); err != nil {
		t.Fatalf("write workspace readme: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "tests", "test_report.py"), []byte("def test_report(): pass\n"), 0o600); err != nil {
		t.Fatalf("write workspace test: %v", err)
	}

	result, err := Prepare(Request{
		SessionID:     "session-ignored-workspace",
		ParentWorkdir: workspace,
		RequestedMode: "auto",
		RootDir:       t.TempDir(),
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if result.Mode != "copy" {
		t.Fatalf("expected ignored workspace to use copy mode, got %#v", result)
	}
	if data, err := os.ReadFile(filepath.Join(result.Workdir, "README.md")); err != nil || string(data) != "staged workspace" {
		t.Fatalf("expected copied workspace README, data=%q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(result.Workdir, "tests", "test_report.py")); err != nil {
		t.Fatalf("expected copied workspace test file: %v", err)
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
