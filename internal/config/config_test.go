package config

import "testing"

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
}
