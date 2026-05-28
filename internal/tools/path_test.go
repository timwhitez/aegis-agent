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
	tests := []struct {
		path    string
		pattern string
	}{
		{path: ".ssh/config", pattern: ".ssh/"},
		{path: ".aws/credentials", pattern: ".aws/"},
		{path: ".azure/accessTokens.json", pattern: ".azure/"},
		{path: ".oci/config", pattern: ".oci/"},
		{path: ".config/gcloud/configurations/config_default", pattern: ".config/gcloud/"},
		{path: ".gnupg/private-keys-v1.d/key", pattern: ".gnupg/"},
		{path: ".kube/config", pattern: ".kube/"},
		{path: ".docker/config.json", pattern: ".docker/"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			assertWriteDenied(t, tt.path, tt.pattern)
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
	if err := CheckWorkspaceWriteAllowed(root, resolved); err == nil || !strings.Contains(err.Error(), "resolves to deny pattern '.git/'") {
		t.Fatalf("expected resolved .git target to be denied, got %v", err)
	}
	err = CheckWorkspaceWriteInputAllowed(root, ".git/config")
	if err == nil || !strings.Contains(err.Error(), ".git/") {
		t.Fatalf("expected lexical .git alias to be denied, got %v", err)
	}
}

func TestWriteDeniedSensitiveSymlinkFileTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "secrets"), 0o700); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}
	target := filepath.Join(root, "secrets", "env-real")
	if err := os.WriteFile(target, []byte("SECRET=old\n"), 0o600); err != nil {
		t.Fatalf("write env target: %v", err)
	}
	if err := os.Symlink("secrets/env-real", filepath.Join(root, ".env")); err != nil {
		t.Fatalf("symlink .env: %v", err)
	}
	resolved, err := ResolveWorkspacePath(root, "secrets/env-real")
	if err != nil {
		t.Fatalf("resolve env target: %v", err)
	}
	if err := CheckWorkspaceWriteAllowed(root, resolved); err == nil || !strings.Contains(err.Error(), "resolves to deny pattern '.env'") {
		t.Fatalf("expected resolved .env target to be denied, got %v", err)
	}
}

func TestWriteDeniedCloudCredentialPathSymlinkAlias(t *testing.T) {
	root := t.TempDir()
	targetDir := filepath.Join(root, "cloud-real", "configurations")
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		t.Fatalf("mkdir cloud target: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".config"), 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "cloud-real"), filepath.Join(root, ".config", "gcloud")); err != nil {
		t.Fatalf("symlink .config/gcloud: %v", err)
	}
	resolved, err := ResolveWorkspacePath(root, "cloud-real/configurations/config_default")
	if err != nil {
		t.Fatalf("resolve cloud target: %v", err)
	}
	if err := CheckWorkspaceWriteAllowed(root, resolved); err == nil || !strings.Contains(err.Error(), "resolves to deny pattern '.config/gcloud/'") {
		t.Fatalf("expected resolved .config/gcloud target to be denied, got %v", err)
	}
}

func TestWriteDeniedBrokenSensitiveSymlinkDoesNotBlockUnrelatedFile(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink("missing-env-target", filepath.Join(root, ".env")); err != nil {
		t.Fatalf("symlink .env: %v", err)
	}
	resolved, err := ResolveWorkspacePath(root, "reports/result.md")
	if err != nil {
		t.Fatalf("resolve normal file: %v", err)
	}
	if err := CheckWorkspaceWriteAllowed(root, resolved); err != nil {
		t.Fatalf("expected broken sensitive alias not to block unrelated writes: %v", err)
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
