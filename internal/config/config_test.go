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
	if cfg.Providers["anthropic"].PromptCache == nil || !*cfg.Providers["anthropic"].PromptCache {
		t.Fatalf("expected anthropic example config to enable prompt_cache, got %#v", cfg.Providers["anthropic"].PromptCache)
	}
	if !cfg.Runtime.ProviderAutoResume.Enabled || cfg.Runtime.ProviderAutoResume.MaxAttempts != 2 {
		t.Fatalf("unexpected provider_auto_resume example config: %#v", cfg.Runtime.ProviderAutoResume)
	}
	if cfg.Runtime.MaxTurnsHard != -1 {
		t.Fatalf("expected example config to disable hard turn limit by default, got %d", cfg.Runtime.MaxTurnsHard)
	}
	if cfg.Runtime.CommandTimeoutSec != 300 {
		t.Fatalf("expected command_timeout_sec 300, got %d", cfg.Runtime.CommandTimeoutSec)
	}
	if cfg.Hooks.DefaultTimeoutSec != 300 {
		t.Fatalf("expected hook default_timeout_sec 300, got %d", cfg.Hooks.DefaultTimeoutSec)
	}
}

func TestOptionalWebBasicAuthRoundTripsWithoutClearTextPassword(t *testing.T) {
	cfg := Default()
	cfg.Web.BasicAuth = WebBasicAuthConfig{
		Username:     "operator",
		PasswordHash: "$2a$10$abcdefghijklmnopqrstuuJ7lHjSuP9gGCP3zZ8KXw2mzg9S0FZ7mP9m",
	}

	cloned, err := Clone(cfg)
	if err != nil {
		t.Fatalf("clone config: %v", err)
	}
	if cloned.Web.BasicAuth != cfg.Web.BasicAuth {
		t.Fatalf("web basic auth changed after clone: got %#v want %#v", cloned.Web.BasicAuth, cfg.Web.BasicAuth)
	}

	data, err := MarshalYAML(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if !strings.Contains(string(data), "password_hash:") || strings.Contains(string(data), "password:") {
		t.Fatalf("unexpected web basic auth YAML: %s", data)
	}
}

func TestDefaultDisablesHardTurnLimitAndUsesFiveMinuteTimeouts(t *testing.T) {
	cfg := Default()
	if cfg.Runtime.MaxTurnsHard != -1 {
		t.Fatalf("expected default hard turn limit disabled, got %d", cfg.Runtime.MaxTurnsHard)
	}
	if cfg.Runtime.CommandTimeoutSec != 300 {
		t.Fatalf("expected default command timeout 300, got %d", cfg.Runtime.CommandTimeoutSec)
	}
	if cfg.Hooks.DefaultTimeoutSec != 300 {
		t.Fatalf("expected default hook timeout 300, got %d", cfg.Hooks.DefaultTimeoutSec)
	}
	for name, provider := range cfg.Providers {
		if provider.RequestTimeoutSec != 300 || provider.TimeoutSec != 300 || provider.StreamIdleTimeoutMS != 300000 {
			t.Fatalf("expected provider %s default timeouts to be 300s request/legacy and 300000ms idle, got %#v", name, provider)
		}
	}
}

func TestDefaultDisablesChildBudgetAndConfiguresQueueReaper(t *testing.T) {
	cfg := Default()
	if cfg.Runtime.ChildBudget.MaxActiveRuntimeSec != 0 || cfg.Runtime.ChildBudget.MaxElapsedSec != 0 || cfg.Runtime.ChildBudget.MaxTurnsPerAttempt != 0 {
		t.Fatalf("expected canonical child budgets disabled, got %#v", cfg.Runtime.ChildBudget)
	}
	if cfg.Runtime.ChildBudget.ActiveRuntimeCheckpointMS != DefaultChildBudgetActiveRuntimeCheckpointMS {
		t.Fatalf("unexpected child active-runtime checkpoint default: %#v", cfg.Runtime.ChildBudget)
	}
	if cfg.Runtime.MultiAgent.MaxDepth != 1 || cfg.Runtime.MultiAgent.MaxActiveChildren != 4 || cfg.Runtime.MultiAgent.CancelGraceSec != 5 {
		t.Fatalf("unexpected default multi-agent resource policy: %#v", cfg.Runtime.MultiAgent)
	}
	if cfg.Runtime.Queue.ReaperIntervalMS != 60000 {
		t.Fatalf("expected default reaper interval 60000ms, got %d", cfg.Runtime.Queue.ReaperIntervalMS)
	}
	if cfg.Runtime.Queue.LeaseStaleAfterSec != 900 {
		t.Fatalf("expected default lease stale after 900s, got %d", cfg.Runtime.Queue.LeaseStaleAfterSec)
	}
	if cfg.Runtime.Queue.BackgroundWaitTimeoutSec != 0 {
		t.Fatalf("expected default background wait timeout disabled (0), got %d", cfg.Runtime.Queue.BackgroundWaitTimeoutSec)
	}
}

func TestDefaultToolResultBudgetConfig(t *testing.T) {
	cfg := Default()
	want := ToolOutputConfig{
		LLMOutputMaxBytes:       DefaultToolOutputLLMMaxBytes,
		DisplayOutputMaxBytes:   DefaultToolOutputDisplayMaxBytes,
		ArtifactFileMaxBytes:    DefaultToolOutputArtifactFileMaxBytes,
		ArtifactSessionMaxBytes: DefaultToolOutputArtifactSessionMaxBytes,
		ArtifactMaxFiles:        DefaultToolOutputArtifactMaxFiles,
	}
	if cfg.Runtime.ToolOutput != want {
		t.Fatalf("unexpected default tool output budget: got %#v want %#v", cfg.Runtime.ToolOutput, want)
	}
}

func TestNormalizeToolResultBudgetConfigDefaultsAndClamps(t *testing.T) {
	t.Run("omitted uses defaults", func(t *testing.T) {
		cfg := Default()
		cfg.Runtime.ToolOutput = ToolOutputConfig{}
		normalizeConfig(cfg, "/tmp/work")
		if cfg.Runtime.ToolOutput.LLMOutputMaxBytes != DefaultToolOutputLLMMaxBytes ||
			cfg.Runtime.ToolOutput.DisplayOutputMaxBytes != DefaultToolOutputDisplayMaxBytes ||
			cfg.Runtime.ToolOutput.ArtifactFileMaxBytes != DefaultToolOutputArtifactFileMaxBytes ||
			cfg.Runtime.ToolOutput.ArtifactSessionMaxBytes != DefaultToolOutputArtifactSessionMaxBytes ||
			cfg.Runtime.ToolOutput.ArtifactMaxFiles != DefaultToolOutputArtifactMaxFiles {
			t.Fatalf("omitted tool output budget did not use defaults: %#v", cfg.Runtime.ToolOutput)
		}
	})

	t.Run("positive values clamp independently", func(t *testing.T) {
		cfg := Default()
		cfg.Runtime.ToolOutput = ToolOutputConfig{
			LLMOutputMaxBytes:       1,
			DisplayOutputMaxBytes:   MaxToolOutputDisplayMaxBytes + 1,
			ArtifactFileMaxBytes:    1,
			ArtifactSessionMaxBytes: MaxToolOutputArtifactSessionMaxBytes + 1,
			ArtifactMaxFiles:        MaxToolOutputArtifactMaxFiles + 1,
		}
		normalizeConfig(cfg, "/tmp/work")
		if cfg.Runtime.ToolOutput.LLMOutputMaxBytes != MinToolOutputLLMMaxBytes {
			t.Fatalf("llm output minimum not applied: %#v", cfg.Runtime.ToolOutput)
		}
		if cfg.Runtime.ToolOutput.DisplayOutputMaxBytes != MaxToolOutputDisplayMaxBytes {
			t.Fatalf("display output maximum not applied: %#v", cfg.Runtime.ToolOutput)
		}
		if cfg.Runtime.ToolOutput.ArtifactFileMaxBytes != MinToolOutputArtifactFileMaxBytes {
			t.Fatalf("artifact file minimum not applied: %#v", cfg.Runtime.ToolOutput)
		}
		if cfg.Runtime.ToolOutput.ArtifactSessionMaxBytes != MaxToolOutputArtifactSessionMaxBytes {
			t.Fatalf("artifact session maximum not applied: %#v", cfg.Runtime.ToolOutput)
		}
		if cfg.Runtime.ToolOutput.ArtifactMaxFiles != MaxToolOutputArtifactMaxFiles {
			t.Fatalf("artifact file-count maximum not applied: %#v", cfg.Runtime.ToolOutput)
		}
	})
}

func TestNormalizeClampsNegativeChildBudgetAndQueueReaper(t *testing.T) {
	cfg := Default()
	cfg.Runtime.ChildBudget.MaxActiveRuntimeSec = -5
	cfg.Runtime.ChildBudget.MaxElapsedSec = -5
	cfg.Runtime.ChildBudget.MaxTurnsPerAttempt = -5
	cfg.Runtime.ChildBudget.ActiveRuntimeCheckpointMS = -5
	cfg.Runtime.Queue.ReaperIntervalMS = -5
	cfg.Runtime.Queue.LeaseStaleAfterSec = -5
	cfg.Runtime.Queue.BackgroundWaitTimeoutSec = -5
	normalizeConfig(cfg, "/tmp/work")
	if cfg.Runtime.ChildBudget.MaxActiveRuntimeSec != 0 || cfg.Runtime.ChildBudget.MaxElapsedSec != 0 || cfg.Runtime.ChildBudget.MaxTurnsPerAttempt != 0 {
		t.Fatalf("expected negative child budgets clamped to 0, got %#v", cfg.Runtime.ChildBudget)
	}
	if cfg.Runtime.ChildBudget.ActiveRuntimeCheckpointMS != DefaultChildBudgetActiveRuntimeCheckpointMS {
		t.Fatalf("expected invalid child checkpoint to use default, got %#v", cfg.Runtime.ChildBudget)
	}
	if cfg.Runtime.Queue.ReaperIntervalMS != 0 || cfg.Runtime.Queue.LeaseStaleAfterSec != 0 || cfg.Runtime.Queue.BackgroundWaitTimeoutSec != 0 {
		t.Fatalf("expected negative queue reaper values clamped to 0, got %#v", cfg.Runtime.Queue)
	}
}

func TestNormalizeMigratesLegacyChildBudgetAliasesToCanonicalFields(t *testing.T) {
	budget := ChildBudgetConfig{MaxWallClockSec: 1800, MaxTurns: 40}
	normalizeChildBudget(&budget)
	if budget.MaxActiveRuntimeSec != 1800 || budget.MaxTurnsPerAttempt != 40 || budget.MaxElapsedSec != 0 {
		t.Fatalf("legacy child budget did not migrate: %#v", budget)
	}
	if budget.MaxWallClockSec != 0 || budget.MaxTurns != 0 {
		t.Fatalf("legacy aliases must be cleared after normalization: %#v", budget)
	}
	if budget.ActiveRuntimeCheckpointMS != DefaultChildBudgetActiveRuntimeCheckpointMS {
		t.Fatalf("legacy child budget must gain checkpoint default: %#v", budget)
	}
}

func TestNormalizeClampsChildActiveRuntimeCheckpointBounds(t *testing.T) {
	low := ChildBudgetConfig{ActiveRuntimeCheckpointMS: 1}
	normalizeChildBudget(&low)
	if low.ActiveRuntimeCheckpointMS != MinChildBudgetActiveRuntimeCheckpointMS {
		t.Fatalf("expected minimum checkpoint clamp, got %#v", low)
	}
	high := ChildBudgetConfig{ActiveRuntimeCheckpointMS: MaxChildBudgetActiveRuntimeCheckpointMS + 1}
	normalizeChildBudget(&high)
	if high.ActiveRuntimeCheckpointMS != MaxChildBudgetActiveRuntimeCheckpointMS {
		t.Fatalf("expected maximum checkpoint clamp, got %#v", high)
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

func TestNormalizeConfigDefaultsHardTurnLimitToDisabled(t *testing.T) {
	cfg := &Config{
		Runtime: RuntimeConfig{},
		Session: SessionConfig{
			Dir: ".go-cli-agent/sessions",
		},
		Skills: SkillsConfig{
			Dirs: []string{"./skills"},
		},
	}

	normalizeConfig(cfg, "/tmp/project")

	if cfg.Runtime.MaxTurnsHard != -1 {
		t.Fatalf("expected omitted hard turn limit to normalize to disabled -1, got %d", cfg.Runtime.MaxTurnsHard)
	}
	if cfg.Runtime.CommandTimeoutSec != 300 {
		t.Fatalf("expected omitted command timeout to normalize to 300, got %d", cfg.Runtime.CommandTimeoutSec)
	}
	if cfg.Hooks.DefaultTimeoutSec != 300 {
		t.Fatalf("expected omitted hook timeout to normalize to 300, got %d", cfg.Hooks.DefaultTimeoutSec)
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

func TestUpsertEnvFileRejectsInvalidKey(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), ".env")
	if err := UpsertEnvFile(envPath, "BAD=KEY_API_KEY", "new-secret"); err == nil || !strings.Contains(err.Error(), "invalid env key") {
		t.Fatalf("expected invalid env key rejection, got %v", err)
	}
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Fatalf("invalid env key should not create env file; stat err=%v", err)
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

func TestLoadUsesHomeConfigWhenCwdIsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".go-cli-agent"), 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".go-cli-agent", "config.yaml"), []byte("default_provider: home\nproviders:\n  home:\n    api_provider: openai-compatible\n    api_key_env: HOME_API_KEY\n    base_url: http://home.invalid/v1\n    model: home-model\n"), 0o600); err != nil {
		t.Fatalf("write home config: %v", err)
	}
	cfg, err := Load("", home)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DefaultProvider != "home" {
		t.Fatalf("home config was not applied when cwd is home: %#v", cfg.DefaultProvider)
	}
}

func TestLoadUsesEnvConfigEvenWhenItMatchesWorkspacePath(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	cwd := filepath.Join(root, "work")
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(cwd, ".go-cli-agent"), 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	configPath := filepath.Join(cwd, ".go-cli-agent", "config.yaml")
	if err := os.WriteFile(configPath, []byte("default_provider: env\nproviders:\n  env:\n    api_provider: openai-compatible\n    api_key_env: ENV_API_KEY\n    base_url: http://env.invalid/v1\n    model: env-model\n"), 0o600); err != nil {
		t.Fatalf("write env config: %v", err)
	}
	t.Setenv("GO_CLI_AGENT_CONFIG", configPath)
	cfg, err := Load("", cwd)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DefaultProvider != "env" {
		t.Fatalf("env config was not applied when it matched workspace path: %#v", cfg.DefaultProvider)
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
