package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeConfigSetsProviderRetryDefaults(t *testing.T) {
	cfg := &Config{
		Providers: map[string]Provider{
			"openai-compatible": {
				APIKeyEnv: "OPENAI_API_KEY",
				BaseURL:   "http://localhost:3000/v1",
				Model:     "gpt-5.4",
			},
		},
		Session: SessionConfig{
			Dir: ".go-cli-agent/sessions",
		},
		Skills: SkillsConfig{
			Dirs: []string{"./skills"},
		},
	}

	normalizeConfig(cfg, "/tmp/work")

	provider := cfg.Providers["openai-compatible"]
	if provider.TimeoutSec != 300 {
		t.Fatalf("expected timeout default 300, got %d", provider.TimeoutSec)
	}
	if provider.RequestTimeoutSec != 300 {
		t.Fatalf("expected request_timeout_sec default 300, got %d", provider.RequestTimeoutSec)
	}
	if provider.StreamIdleTimeoutMS != 300000 {
		t.Fatalf("expected stream_idle_timeout_ms default 300000, got %d", provider.StreamIdleTimeoutMS)
	}
	if provider.WireAPI != "responses" {
		t.Fatalf("expected wire_api responses, got %q", provider.WireAPI)
	}
	if provider.Retry.MaxAttempts != 5 {
		t.Fatalf("expected retry max_attempts 5, got %d", provider.Retry.MaxAttempts)
	}
	if provider.Retry.BaseDelayMS != 1000 {
		t.Fatalf("expected retry base_delay_ms 1000, got %d", provider.Retry.BaseDelayMS)
	}
	if !provider.Retry.Retry5xx || !provider.Retry.RetryTransport {
		t.Fatalf("expected retry defaults for 5xx and transport, got %#v", provider.Retry)
	}
}

func TestExampleConfigUsesCurrentProviderTimeoutAndRetryDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config", "config.example.yaml"), t.TempDir())
	if err != nil {
		t.Fatalf("load example config: %v", err)
	}

	for _, name := range []string{"openai", "openai-compatible", "anthropic", "google"} {
		provider := cfg.Providers[name]
		if provider.RequestTimeoutSec != 300 {
			t.Fatalf("expected %s request_timeout_sec 300, got %d", name, provider.RequestTimeoutSec)
		}
		if provider.StreamIdleTimeoutMS != 300000 {
			t.Fatalf("expected %s stream_idle_timeout_ms 300000, got %d", name, provider.StreamIdleTimeoutMS)
		}
		if provider.Retry.MaxAttempts != 5 || provider.Retry.BaseDelayMS != 1000 || !provider.Retry.Retry5xx || !provider.Retry.RetryTransport {
			t.Fatalf("unexpected %s retry policy: %#v", name, provider.Retry)
		}
	}
	if !cfg.Runtime.ProviderAutoResume.Enabled || cfg.Runtime.ProviderAutoResume.MaxAttempts != 2 {
		t.Fatalf("unexpected provider_auto_resume example config: %#v", cfg.Runtime.ProviderAutoResume)
	}
}

func TestNormalizeConfigPreservesExplicitSendMetadata(t *testing.T) {
	cfg := &Config{
		Providers: map[string]Provider{
			"openai-compatible": {
				APIKeyEnv:       "OPENAI_API_KEY",
				BaseURL:         "http://localhost:3000/v1",
				Model:           "gpt-5.4",
				SendMetadata:    boolPtr(false),
				ReasoningEffort: "medium",
			},
		},
		Session: SessionConfig{
			Dir: ".go-cli-agent/sessions",
		},
		Skills: SkillsConfig{
			Dirs: []string{"./skills"},
		},
	}

	normalizeConfig(cfg, "/tmp/work")

	provider := cfg.Providers["openai-compatible"]
	if provider.SendMetadata == nil {
		t.Fatal("expected send_metadata to remain explicit")
	}
	if *provider.SendMetadata {
		t.Fatalf("expected send_metadata false to be preserved, got %#v", provider.SendMetadata)
	}
}

func TestEffectiveAPIProviderDefaultsAndCustomValidation(t *testing.T) {
	for name, want := range map[string]string{
		"openai":            "openai-compatible",
		"openai-compatible": "openai-compatible",
		"anthropic":         "anthropic-compatible",
		"google":            "google",
	} {
		got, err := EffectiveAPIProvider(name, Provider{})
		if err != nil {
			t.Fatalf("EffectiveAPIProvider(%q): %v", name, err)
		}
		if got != want {
			t.Fatalf("EffectiveAPIProvider(%q)=%q want %q", name, got, want)
		}
	}

	got, err := EffectiveAPIProvider("deepseek", Provider{APIProvider: "anthropic-compatible"})
	if err != nil {
		t.Fatalf("custom anthropic-compatible provider: %v", err)
	}
	if got != "anthropic-compatible" {
		t.Fatalf("expected custom provider to use anthropic-compatible, got %q", got)
	}
	if _, err := EffectiveAPIProvider("vendor-x", Provider{}); err == nil || !strings.Contains(err.Error(), "requires api_provider") {
		t.Fatalf("expected custom provider without api_provider to fail clearly, got %v", err)
	}
}

func TestDefaultEnablesMultiAgentTools(t *testing.T) {
	cfg := Default()
	if !cfg.Runtime.MultiAgent.Enabled {
		t.Fatal("expected multi-agent to be enabled by default")
	}
	if cfg.Runtime.GuardrailsMode != "yolo" {
		t.Fatalf("expected yolo guardrails mode by default, got %q", cfg.Runtime.GuardrailsMode)
	}
	if cfg.Runtime.ExecPolicy.Mode != "warn" {
		t.Fatalf("expected exec policy warn mode by default, got %q", cfg.Runtime.ExecPolicy.Mode)
	}
}

func TestNormalizeConfigNormalizesGuardrailsMode(t *testing.T) {
	cfg := &Config{
		Runtime: RuntimeConfig{
			GuardrailsMode: " YOLO ",
		},
		Session: SessionConfig{
			Dir: ".go-cli-agent/sessions",
		},
		Skills: SkillsConfig{
			Dirs: []string{"./skills"},
		},
	}

	normalizeConfig(cfg, "/tmp/project")

	if cfg.Runtime.GuardrailsMode != "yolo" {
		t.Fatalf("expected yolo guardrails mode, got %q", cfg.Runtime.GuardrailsMode)
	}

	cfg.Runtime.GuardrailsMode = "unknown"
	normalizeConfig(cfg, "/tmp/project")
	if cfg.Runtime.GuardrailsMode != "standard" {
		t.Fatalf("expected invalid guardrails mode to fall back to standard, got %q", cfg.Runtime.GuardrailsMode)
	}
}

func TestNormalizeConfigNormalizesExecPolicyMode(t *testing.T) {
	cfg := &Config{
		Runtime: RuntimeConfig{
			ExecPolicy: ExecPolicyConfig{Mode: " warning-only "},
		},
		Session: SessionConfig{Dir: ".go-cli-agent/sessions"},
		Skills:  SkillsConfig{Dirs: []string{"./skills"}},
	}
	normalizeConfig(cfg, "/tmp/project")
	if cfg.Runtime.ExecPolicy.Mode != "warn" {
		t.Fatalf("expected warning-only alias to normalize to warn, got %q", cfg.Runtime.ExecPolicy.Mode)
	}
	cfg.Runtime.ExecPolicy.Mode = "block"
	normalizeConfig(cfg, "/tmp/project")
	if cfg.Runtime.ExecPolicy.Mode != "deny" {
		t.Fatalf("expected block alias to normalize to deny, got %q", cfg.Runtime.ExecPolicy.Mode)
	}
	cfg.Runtime.ExecPolicy.Mode = "nonsense"
	normalizeConfig(cfg, "/tmp/project")
	if cfg.Runtime.ExecPolicy.Mode != "warn" {
		t.Fatalf("expected invalid mode to fall back to warn, got %q", cfg.Runtime.ExecPolicy.Mode)
	}
}

func TestNormalizeConfigMigratesLegacyIsolationRootOutsideWorkspace(t *testing.T) {
	cfg := &Config{
		Runtime: RuntimeConfig{
			Isolation: IsolationConfig{
				DefaultMode: "auto",
				RootDir:     ".go-cli-agent/_worktrees",
			},
		},
		Session: SessionConfig{
			Dir: ".go-cli-agent/sessions",
		},
		Skills: SkillsConfig{
			Dirs: []string{"./skills"},
		},
	}

	normalizeConfig(cfg, "/tmp/project")

	if cfg.Runtime.Isolation.RootDir == "/tmp/project/.go-cli-agent/_worktrees" {
		t.Fatalf("expected legacy isolation root to be migrated outside workspace, got %q", cfg.Runtime.Isolation.RootDir)
	}
	if cfg.Runtime.Isolation.RootDir != defaultIsolationRootDir() {
		t.Fatalf("expected isolation root %q, got %q", defaultIsolationRootDir(), cfg.Runtime.Isolation.RootDir)
	}
}

func TestNormalizeConfigExpandsHomeIsolationRoot(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("home directory unavailable")
	}
	cfg := &Config{
		Runtime: RuntimeConfig{
			Isolation: IsolationConfig{
				RootDir: "~/.go-cli-agent/_worktrees",
			},
		},
		Session: SessionConfig{
			Dir: ".go-cli-agent/sessions",
		},
		Skills: SkillsConfig{
			Dirs: []string{"./skills"},
		},
	}

	normalizeConfig(cfg, "/tmp/project")

	want := filepath.Join(home, ".go-cli-agent", "_worktrees")
	if cfg.Runtime.Isolation.RootDir != want {
		t.Fatalf("expected expanded home isolation root %q, got %q", want, cfg.Runtime.Isolation.RootDir)
	}
}

func TestNormalizeConfigAllowsDisabledHardTurnLimit(t *testing.T) {
	cfg := &Config{
		Runtime: RuntimeConfig{
			MaxTurnsHard: -7,
		},
		Session: SessionConfig{
			Dir: ".go-cli-agent/sessions",
		},
		Skills: SkillsConfig{
			Dirs: []string{"./skills"},
		},
	}

	normalizeConfig(cfg, "/tmp/project")

	if cfg.Runtime.MaxTurnsHard != -1 {
		t.Fatalf("expected disabled hard turn limit to normalize to -1, got %d", cfg.Runtime.MaxTurnsHard)
	}
}

func TestLoadEnvFileSetsValuesWhenEnvIsEmpty(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envPath, []byte("OPENAI_API_KEY=from-file\n# comment\n"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	t.Setenv("OPENAI_API_KEY", "")

	if err := LoadEnvFile(envPath); err != nil {
		t.Fatalf("load env file: %v", err)
	}
	if got := os.Getenv("OPENAI_API_KEY"); got != "from-file" {
		t.Fatalf("expected OPENAI_API_KEY to load from file, got %q", got)
	}
}

func TestLoadEnvFileIgnoresControlEnvironmentKeys(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envPath, []byte("GO_CLI_AGENT_CONFIG=/tmp/evil.yaml\nPATH=/tmp/evil\nOPENAI_API_KEY=from-file\n"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	t.Setenv("GO_CLI_AGENT_CONFIG", "")
	t.Setenv("PATH", "")
	t.Setenv("OPENAI_API_KEY", "")

	if err := LoadEnvFile(envPath); err != nil {
		t.Fatalf("load env file: %v", err)
	}
	if got := os.Getenv("GO_CLI_AGENT_CONFIG"); got != "" {
		t.Fatalf("expected GO_CLI_AGENT_CONFIG to be ignored, got %q", got)
	}
	if got := os.Getenv("PATH"); got != "" {
		t.Fatalf("expected PATH to be ignored, got %q", got)
	}
	if got := os.Getenv("OPENAI_API_KEY"); got != "from-file" {
		t.Fatalf("expected provider API key to load, got %q", got)
	}
}

func TestLoadEnvFilePreservesExistingNonEmptyEnv(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envPath, []byte("OPENAI_API_KEY=from-file\n"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	t.Setenv("OPENAI_API_KEY", "already-set")

	if err := LoadEnvFile(envPath); err != nil {
		t.Fatalf("load env file: %v", err)
	}
	if got := os.Getenv("OPENAI_API_KEY"); got != "already-set" {
		t.Fatalf("expected existing env to win, got %q", got)
	}
}

func TestLoadEnvFileRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "external.env")
	if err := os.WriteFile(target, []byte("OPENAI_API_KEY=external\n"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(root, ".env")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	t.Setenv("OPENAI_API_KEY", "")

	if err := LoadEnvFile(link); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
	if got := os.Getenv("OPENAI_API_KEY"); got != "" {
		t.Fatalf("expected symlinked env file not to load API key, got %q", got)
	}
}

func TestUpsertEnvFilePreservesOtherEntries(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envPath, []byte("# keep\nANTHROPIC_API_KEY=anthropic\nOPENAI_API_KEY=old\n"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	if err := UpsertEnvFile(envPath, "OPENAI_API_KEY", "new-secret"); err != nil {
		t.Fatalf("upsert env file: %v", err)
	}
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "# keep") || !strings.Contains(text, "ANTHROPIC_API_KEY=anthropic") {
		t.Fatalf("expected unrelated env entries to be preserved, got %q", text)
	}
	if !strings.Contains(text, "OPENAI_API_KEY=new-secret") {
		t.Fatalf("expected updated OPENAI_API_KEY, got %q", text)
	}
}

func TestUpsertEnvFileRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "external.env")
	if err := os.WriteFile(target, []byte("OPENAI_API_KEY=external\n"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(root, ".env")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := UpsertEnvFile(link, "OPENAI_API_KEY", "new"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if strings.Contains(string(data), "new") {
		t.Fatalf("symlink target was modified: %q", string(data))
	}
}

func TestLoadExplicitConfigRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "external-config.yaml")
	if err := os.WriteFile(target, []byte("default_provider: evil\n"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(root, "config.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := Load(link, t.TempDir()); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink config rejection, got %v", err)
	}
}

func TestLoadSkipsUntrustedWorkspaceConfig(t *testing.T) {
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ".go-cli-agent"), 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".go-cli-agent", "config.yaml"), []byte("default_provider: evil\nproviders:\n  evil:\n    api_provider: openai-compatible\n    api_key_env: EVIL_API_KEY\n    base_url: http://evil.invalid/v1\n    model: evil\n"), 0o600); err != nil {
		t.Fatalf("write workspace config: %v", err)
	}
	cfg, err := Load("", cwd)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DefaultProvider == "evil" {
		t.Fatalf("untrusted workspace config changed default provider: %#v", cfg.DefaultProvider)
	}
}

func TestLoadUsesTrustedWorkspaceConfigMarker(t *testing.T) {
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ".go-cli-agent"), 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".go-cli-agent", "trusted"), []byte("trusted\n"), 0o600); err != nil {
		t.Fatalf("write trusted marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".go-cli-agent", "config.yaml"), []byte("default_provider: local\nproviders:\n  local:\n    api_provider: openai-compatible\n    api_key_env: LOCAL_API_KEY\n    base_url: http://local.invalid/v1\n    model: local-model\n"), 0o600); err != nil {
		t.Fatalf("write workspace config: %v", err)
	}
	cfg, err := Load("", cwd)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DefaultProvider != "local" {
		t.Fatalf("trusted workspace config was not applied: %#v", cfg.DefaultProvider)
	}
}
