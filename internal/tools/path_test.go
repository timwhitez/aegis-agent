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
	assertWriteDenied(t, ".aegis-agent/config.yaml", ".aegis-agent/")
}

func TestWriteDeniedDotEnv(t *testing.T) {
	assertWriteDenied(t, ".env", ".env")
}

func TestWriteDeniedEnvVariantsAndSensitivePathComponents(t *testing.T) {
	tests := []struct {
		path    string
		pattern string
	}{
		{path: ".env.local", pattern: ".env.*"},
		{path: ".env/token", pattern: ".env"},
		{path: "configs/.env.production/token", pattern: ".env.*"},
		{path: "credentials/token", pattern: "credentials"},
		{path: "nested/deploy.pem/token", pattern: "*.pem"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			assertWriteDenied(t, tt.path, tt.pattern)
		})
	}
}

func TestWriteAllowsEnvTemplateFiles(t *testing.T) {
	for _, path := range []string{
		".env.example",
		".env.sample",
		".env.template",
		"docs/.env.example",
	} {
		t.Run(path, func(t *testing.T) {
			assertWriteAllowed(t, path)
		})
	}
}

func TestWriteDeniedPrivateKeyAndCredentialFiles(t *testing.T) {
	tests := []struct {
		path    string
		pattern string
	}{
		{path: "id_ecdsa", pattern: "id_*"},
		{path: "identity", pattern: "identity"},
		{path: "deploy.pem", pattern: "*.pem"},
		{path: "private-key.txt", pattern: "*private-key*"},
		{path: "service_private_key.json", pattern: "*private_key*"},
		{path: "client_secret.json", pattern: "*client_secret*"},
		{path: "client-secret.json", pattern: "*client-secret*"},
		{path: "service_account.json", pattern: "*service_account*"},
		{path: "service-account.json", pattern: "*service-account*"},
		{path: "credentials.json", pattern: "credentials.*"},
		{path: "service-account_credentials.json", pattern: "*_credentials.json"},
		{path: "prod.credentials", pattern: "*.credentials"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			assertWriteDenied(t, tt.path, tt.pattern)
		})
	}
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

func TestWriteDeniedCredentialRCAndPackageCredentialFiles(t *testing.T) {
	tests := []struct {
		path    string
		pattern string
	}{
		{path: ".npmrc", pattern: ".npmrc"},
		{path: ".envrc", pattern: ".envrc"},
		{path: ".netrc", pattern: ".netrc"},
		{path: "_netrc", pattern: "_netrc"},
		{path: ".pypirc", pattern: ".pypirc"},
		{path: ".git-credentials", pattern: ".git-credentials"},
		{path: ".dockercfg", pattern: ".dockercfg"},
		{path: ".yarnrc", pattern: ".yarnrc"},
		{path: ".yarnrc.yml", pattern: ".yarnrc.yml"},
		{path: ".pnpmrc", pattern: ".pnpmrc"},
		{path: ".m2/settings.xml", pattern: ".m2/settings.xml"},
		{path: ".m2/settings-security.xml", pattern: ".m2/settings-security.xml"},
		{path: ".gradle/gradle.properties", pattern: ".gradle/gradle.properties"},
		{path: ".nuget/NuGet.Config", pattern: ".nuget/NuGet.Config"},
		{path: ".pip/pip.conf", pattern: ".pip/pip.conf"},
		{path: ".config/pip/pip.conf", pattern: ".config/pip/pip.conf"},
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

func TestWriteDeniedPrivateKeyPatternSymlinkFileTargets(t *testing.T) {
	tests := []struct {
		name    string
		alias   string
		target  string
		pattern string
	}{
		{
			name:    "pem alias",
			alias:   "deploy.pem",
			target:  "key-real",
			pattern: "*.pem",
		},
		{
			name:    "credentials json alias",
			alias:   "credentials.json",
			target:  "creds-real",
			pattern: "credentials.*",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "secrets"), 0o700); err != nil {
				t.Fatalf("mkdir secrets: %v", err)
			}
			target := filepath.Join(root, "secrets", tt.target)
			if err := os.WriteFile(target, []byte("SECRET=old\n"), 0o600); err != nil {
				t.Fatalf("write sensitive target: %v", err)
			}
			if err := os.Symlink(filepath.Join("secrets", tt.target), filepath.Join(root, tt.alias)); err != nil {
				t.Fatalf("symlink %s: %v", tt.alias, err)
			}
			resolved, err := ResolveWorkspacePath(root, filepath.ToSlash(filepath.Join("secrets", tt.target)))
			if err != nil {
				t.Fatalf("resolve sensitive target: %v", err)
			}
			if err := CheckWorkspaceWriteAllowed(root, resolved); err == nil || !strings.Contains(err.Error(), "resolves to deny pattern '"+tt.pattern+"'") {
				t.Fatalf("expected resolved %s target to be denied, got %v", tt.alias, err)
			}
		})
	}
}

func TestWriteDeniedNestedPrivateKeyPatternSymlinkFileTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "configs"), 0o700); err != nil {
		t.Fatalf("mkdir configs: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "secrets"), 0o700); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}
	target := filepath.Join(root, "secrets", "key-real")
	if err := os.WriteFile(target, []byte("SECRET=old\n"), 0o600); err != nil {
		t.Fatalf("write sensitive target: %v", err)
	}
	if err := os.Symlink(filepath.Join("..", "secrets", "key-real"), filepath.Join(root, "configs", "deploy.pem")); err != nil {
		t.Fatalf("symlink nested deploy.pem: %v", err)
	}
	resolved, err := ResolveWorkspacePath(root, "secrets/key-real")
	if err != nil {
		t.Fatalf("resolve sensitive target: %v", err)
	}
	if err := CheckWorkspaceWriteAllowed(root, resolved); err == nil || !strings.Contains(err.Error(), "resolves to deny pattern '*.pem'") {
		t.Fatalf("expected nested resolved deploy.pem target to be denied, got %v", err)
	}
}

func TestWriteDeniedNestedCredentialNameSymlinkFileTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "configs"), 0o700); err != nil {
		t.Fatalf("mkdir configs: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "secrets"), 0o700); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}
	target := filepath.Join(root, "secrets", "env-real")
	if err := os.WriteFile(target, []byte("TOKEN=old\n"), 0o600); err != nil {
		t.Fatalf("write sensitive target: %v", err)
	}
	if err := os.Symlink(filepath.Join("..", "secrets", "env-real"), filepath.Join(root, "configs", ".env")); err != nil {
		t.Fatalf("symlink nested .env: %v", err)
	}
	resolved, err := ResolveWorkspacePath(root, "secrets/env-real")
	if err != nil {
		t.Fatalf("resolve sensitive target: %v", err)
	}
	if err := CheckWorkspaceWriteAllowed(root, resolved); err == nil || !strings.Contains(err.Error(), "resolves to deny pattern '.env'") {
		t.Fatalf("expected nested resolved .env target to be denied, got %v", err)
	}
}

func TestWriteDeniedNestedSecretDirectorySymlinkTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "configs"), 0o700); err != nil {
		t.Fatalf("mkdir configs: %v", err)
	}
	targetDir := filepath.Join(root, "secrets", "ssh-real")
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		t.Fatalf("mkdir ssh target: %v", err)
	}
	if err := os.Symlink(filepath.Join("..", "secrets", "ssh-real"), filepath.Join(root, "configs", ".ssh")); err != nil {
		t.Fatalf("symlink nested .ssh: %v", err)
	}
	resolved, err := ResolveWorkspacePath(root, "secrets/ssh-real/config")
	if err != nil {
		t.Fatalf("resolve sensitive target child: %v", err)
	}
	if err := CheckWorkspaceWriteAllowed(root, resolved); err == nil || !strings.Contains(err.Error(), "resolves to deny pattern '.ssh/'") {
		t.Fatalf("expected nested resolved .ssh target child to be denied, got %v", err)
	}
}

func TestWriteDeniedNestedPackageCredentialPathSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "configs", ".m2"), 0o700); err != nil {
		t.Fatalf("mkdir nested maven config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "secrets"), 0o700); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}
	target := filepath.Join(root, "secrets", "settings-real.xml")
	if err := os.WriteFile(target, []byte("<settings><password>old</password></settings>\n"), 0o600); err != nil {
		t.Fatalf("write sensitive target: %v", err)
	}
	if err := os.Symlink(filepath.Join("..", "..", "secrets", "settings-real.xml"), filepath.Join(root, "configs", ".m2", "settings.xml")); err != nil {
		t.Fatalf("symlink nested maven settings: %v", err)
	}
	resolved, err := ResolveWorkspacePath(root, "secrets/settings-real.xml")
	if err != nil {
		t.Fatalf("resolve sensitive target: %v", err)
	}
	if err := CheckWorkspaceWriteAllowed(root, resolved); err == nil || !strings.Contains(err.Error(), "resolves to deny pattern '.m2/settings.xml'") {
		t.Fatalf("expected nested resolved .m2/settings.xml target to be denied, got %v", err)
	}
}

func TestWriteDeniedPrivateKeyPatternSymlinkDirectoryTargets(t *testing.T) {
	tests := []struct {
		name    string
		alias   string
		target  string
		child   string
		pattern string
	}{
		{
			name:    "pem directory alias",
			alias:   "deploy.pem",
			target:  "key-dir",
			child:   "token",
			pattern: "*.pem",
		},
		{
			name:    "credentials directory alias",
			alias:   "credentials.json",
			target:  "creds-dir",
			child:   "token",
			pattern: "credentials.*",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			targetDir := filepath.Join(root, "secrets", tt.target)
			if err := os.MkdirAll(targetDir, 0o700); err != nil {
				t.Fatalf("mkdir sensitive target dir: %v", err)
			}
			if err := os.Symlink(filepath.Join("secrets", tt.target), filepath.Join(root, tt.alias)); err != nil {
				t.Fatalf("symlink %s: %v", tt.alias, err)
			}
			resolved, err := ResolveWorkspacePath(root, filepath.ToSlash(filepath.Join("secrets", tt.target, tt.child)))
			if err != nil {
				t.Fatalf("resolve sensitive target child: %v", err)
			}
			if err := CheckWorkspaceWriteAllowed(root, resolved); err == nil || !strings.Contains(err.Error(), "resolves to deny pattern '"+tt.pattern+"'") {
				t.Fatalf("expected resolved %s directory target child to be denied, got %v", tt.alias, err)
			}
		})
	}
}

func TestWriteDeniedPackageCredentialPathSymlinkAlias(t *testing.T) {
	root := t.TempDir()
	targetDir := filepath.Join(root, "maven-real")
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		t.Fatalf("mkdir maven target: %v", err)
	}
	if err := os.Symlink("maven-real", filepath.Join(root, ".m2")); err != nil {
		t.Fatalf("symlink .m2: %v", err)
	}
	resolved, err := ResolveWorkspacePath(root, "maven-real/settings.xml")
	if err != nil {
		t.Fatalf("resolve maven target: %v", err)
	}
	if err := CheckWorkspaceWriteAllowed(root, resolved); err == nil || !strings.Contains(err.Error(), "resolves to deny pattern '.m2/settings.xml'") {
		t.Fatalf("expected resolved .m2/settings.xml target to be denied, got %v", err)
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

func TestWriteDeniedBrokenSensitiveSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "secrets"), 0o700); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}
	if err := os.Symlink(filepath.Join("secrets", "env-real"), filepath.Join(root, ".env")); err != nil {
		t.Fatalf("symlink .env: %v", err)
	}
	resolved, err := ResolveWorkspacePath(root, "secrets/env-real")
	if err != nil {
		t.Fatalf("resolve env target: %v", err)
	}
	if err := CheckWorkspaceWriteAllowed(root, resolved); err == nil || !strings.Contains(err.Error(), "resolves to deny pattern '.env'") {
		t.Fatalf("expected missing .env symlink target write to be denied, got %v", err)
	}
}

func TestWriteDeniedBrokenPackageCredentialSymlinkParentTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink("maven-real", filepath.Join(root, ".m2")); err != nil {
		t.Fatalf("symlink .m2: %v", err)
	}
	resolved, err := ResolveWorkspacePath(root, "maven-real/settings.xml")
	if err != nil {
		t.Fatalf("resolve maven target: %v", err)
	}
	if err := CheckWorkspaceWriteAllowed(root, resolved); err == nil || !strings.Contains(err.Error(), "resolves to deny pattern '.m2/settings.xml'") {
		t.Fatalf("expected missing .m2/settings.xml symlink target write to be denied, got %v", err)
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

func assertWriteAllowed(t *testing.T, inputPath string) {
	t.Helper()
	root := t.TempDir()
	path, err := ResolveWorkspacePath(root, inputPath)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := CheckWorkspaceWriteInputAllowed(root, inputPath); err != nil {
		t.Fatalf("expected lexical path %s to be allowed: %v", inputPath, err)
	}
	if err := CheckWorkspaceWriteAllowed(root, path); err != nil {
		t.Fatalf("expected resolved path %s to be allowed: %v", inputPath, err)
	}
}
