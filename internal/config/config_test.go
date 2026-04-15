package config

import (
	"os"
	"path/filepath"
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
	if provider.TimeoutSec != 120 {
		t.Fatalf("expected timeout default 120, got %d", provider.TimeoutSec)
	}
	if provider.WireAPI != "responses" {
		t.Fatalf("expected wire_api responses, got %q", provider.WireAPI)
	}
	if provider.Retry.MaxAttempts != 2 {
		t.Fatalf("expected retry max_attempts 2, got %d", provider.Retry.MaxAttempts)
	}
	if provider.Retry.BaseDelayMS != 1000 {
		t.Fatalf("expected retry base_delay_ms 1000, got %d", provider.Retry.BaseDelayMS)
	}
	if !provider.Retry.Retry5xx || !provider.Retry.RetryTransport {
		t.Fatalf("expected retry defaults for 5xx and transport, got %#v", provider.Retry)
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

func TestDefaultEnablesMultiAgentTools(t *testing.T) {
	cfg := Default()
	if !cfg.Runtime.MultiAgent.Enabled {
		t.Fatal("expected multi-agent to be enabled by default")
	}
	if cfg.Runtime.GuardrailsMode != "yolo" {
		t.Fatalf("expected yolo guardrails mode by default, got %q", cfg.Runtime.GuardrailsMode)
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
