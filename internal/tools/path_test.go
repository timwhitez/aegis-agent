package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveWorkspacePathRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := ResolveWorkspacePath(root, "link/secret.txt"); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
}

func TestResolveWorkspacePathAllowsNestedFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "nested", "file.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, []byte("ok"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := ResolveWorkspacePath(root, "nested/file.txt")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != target {
		t.Fatalf("expected %s, got %s", target, got)
	}
}

func TestWriteDeniedDotGit(t *testing.T) {
	assertWriteDenied(t, ".git/config", ".git/")
}

func TestWriteDeniedGoCliAgentState(t *testing.T) {
	assertWriteDenied(t, ".go-cli-agent/config.yaml", ".go-cli-agent/")
}

func TestWriteDeniedDotEnv(t *testing.T) {
	assertWriteDenied(t, ".env", ".env")
}

func TestWriteDeniedSecretDirs(t *testing.T) {
	for _, path := range []string{".ssh/config", ".aws/credentials", ".gnupg/private-keys-v1.d/key", ".kube/config", ".docker/config.json"} {
		t.Run(path, func(t *testing.T) {
			assertWriteDenied(t, path, strings.Split(path, "/")[0]+"/")
		})
	}
}

func TestWriteDeniedAllowsNormalWorkspaceFile(t *testing.T) {
	root := t.TempDir()
	path, err := ResolveWorkspacePath(root, "reports/result.md")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := CheckWorkspaceWriteAllowed(root, path); err != nil {
		t.Fatalf("expected normal workspace file to be allowed: %v", err)
	}
}

func TestWriteDeniedSensitiveSymlinkAlias(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "git-real"), 0o700); err != nil {
		t.Fatalf("mkdir git-real: %v", err)
	}
	if err := os.Symlink("git-real", filepath.Join(root, ".git")); err != nil {
		t.Fatalf("symlink .git: %v", err)
	}
	resolved, err := ResolveWorkspacePath(root, ".git/config")
	if err != nil {
		t.Fatalf("resolve symlink alias: %v", err)
	}
	if strings.Contains(filepath.ToSlash(resolved), "/.git/") {
		t.Fatalf("expected symlink to resolve away from .git alias, got %s", resolved)
	}
	if err := CheckWorkspaceWriteAllowed(root, resolved); err != nil {
		t.Fatalf("resolved path alone should not carry lexical alias, got %v", err)
	}
	err = CheckWorkspaceWriteInputAllowed(root, ".git/config")
	if err == nil || !strings.Contains(err.Error(), ".git/") {
		t.Fatalf("expected lexical .git alias to be denied, got %v", err)
	}
}

func assertWriteDenied(t *testing.T, inputPath, pattern string) {
	t.Helper()
	root := t.TempDir()
	path, err := ResolveWorkspacePath(root, inputPath)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	err = CheckWorkspaceWriteInputAllowed(root, inputPath)
	if err == nil {
		err = CheckWorkspaceWriteAllowed(root, path)
	}
	if err == nil {
		t.Fatalf("expected %s to be denied", inputPath)
	}
	want := "write denied: path '" + filepath.ToSlash(inputPath) + "' matches deny pattern '" + pattern + "'"
	if err.Error() != want {
		t.Fatalf("unexpected denial error:\n got: %s\nwant: %s", err.Error(), want)
	}
}
