package streamjson

import (
	"os"
	"path/filepath"
	"testing"

	"aegis-agent/internal/config"
)

func TestModelsFromConfigUsesProviderRouteIDs(t *testing.T) {
	cfg := config.Default()
	cfg.DefaultProvider = "anthropic"
	models, err := ModelsFromConfig(cfg)
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	if len(models) == 0 {
		t.Fatal("expected models")
	}
	var foundDefault bool
	for _, model := range models {
		if model.ID == "anthropic/claude-sonnet-4-6" {
			foundDefault = true
			if !model.Default || model.Provider != "anthropic" {
				t.Fatalf("unexpected anthropic model: %#v", model)
			}
			if model.Thinking == nil || len(model.Thinking.SupportedLevels) != 5 || model.Thinking.DefaultLevel != "medium" {
				t.Fatalf("expected thinking catalog, got %#v", model.Thinking)
			}
			if model.ContextWindow != config.DefaultContextWindowTokens {
				t.Fatalf("expected default context window, got %#v", model)
			}
			var hasXHigh, hasMax bool
			for _, level := range model.Thinking.SupportedLevels {
				if level.Value == "xhigh" {
					hasXHigh = true
				}
				if level.Value == "max" {
					hasMax = true
				}
			}
			if !hasXHigh || !hasMax {
				t.Fatalf("expected xhigh and max thinking levels, got %#v", model.Thinking.SupportedLevels)
			}
		}
	}
	if !foundDefault {
		t.Fatalf("expected default anthropic route in %#v", models)
	}
}

func TestModelsFromConfigPreservesConfiguredMaxThinkingDefault(t *testing.T) {
	cfg := config.Default()
	openai := cfg.Providers["openai"]
	openai.ReasoningEffort = "max"
	cfg.Providers["openai"] = openai

	models, err := ModelsFromConfig(cfg)
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	for _, model := range models {
		if model.ID == "openai/gpt-5.4" {
			if model.Thinking == nil || model.Thinking.DefaultLevel != "max" {
				t.Fatalf("expected configured max default, got %#v", model.Thinking)
			}
			return
		}
	}
	t.Fatalf("openai model not found in %#v", models)
}

func TestModelsFromConfigUsesOutputConfigEffortDefault(t *testing.T) {
	cfg := config.Default()
	anthropic := cfg.Providers["anthropic"]
	anthropic.OutputConfigEffort = "xhigh"
	cfg.Providers["anthropic"] = anthropic

	models, err := ModelsFromConfig(cfg)
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	for _, model := range models {
		if model.ID == "anthropic/claude-sonnet-4-6" {
			if model.Thinking == nil || model.Thinking.DefaultLevel != "xhigh" {
				t.Fatalf("expected configured xhigh default, got %#v", model.Thinking)
			}
			return
		}
	}
	t.Fatalf("anthropic model not found in %#v", models)
}

func TestModelsFromConfigUsesConfiguredThinkingDefault(t *testing.T) {
	cfg := config.Default()
	openai := cfg.Providers["openai"]
	openai.ReasoningEffort = "xhigh"
	cfg.Providers["openai"] = openai

	models, err := ModelsFromConfig(cfg)
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	for _, model := range models {
		if model.ID == "openai/gpt-5.4" {
			if model.Thinking == nil || model.Thinking.DefaultLevel != "xhigh" {
				t.Fatalf("expected configured xhigh default, got %#v", model.Thinking)
			}
			return
		}
	}
	t.Fatalf("openai model not found in %#v", models)
}

func TestModelsFromConfigUsesConfiguredContextWindow(t *testing.T) {
	cfg := config.Default()
	openai := cfg.Providers["openai"]
	openai.Model = "gpt-5.5"
	openai.ContextWindowTokens = 272000
	cfg.Providers["openai"] = openai

	models, err := ModelsFromConfig(cfg)
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	for _, model := range models {
		if model.ID == "openai/gpt-5.5" {
			if model.ContextWindow != 272000 {
				t.Fatalf("expected configured context window, got %#v", model)
			}
			return
		}
	}
	t.Fatalf("openai gpt-5.5 model not found in %#v", models)
}

func TestModelsFromConfigDistinguishesFileAndBuiltinSources(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`schema_version: 1
default_provider: mine
providers:
  mine:
    api_provider: anthropic-compatible
    api_key_env: ANTHROPIC_API_KEY
    base_url: https://api.appintheloop.com
    model: MiniMax-M3.1-Flash
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	models, err := ModelsFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Model{}
	for _, m := range models {
		byID[m.ID] = m
	}
	if byID["mine/MiniMax-M3.1-Flash"].Source != config.ProviderSourceFile || !byID["mine/MiniMax-M3.1-Flash"].Default {
		t.Fatalf("expected mine to have source=file and default=true, got %#v", byID["mine/MiniMax-M3.1-Flash"])
	}
	if byID["openai/gpt-5.4"].Source != config.ProviderSourceBuiltin {
		t.Fatalf("expected openai to have source=builtin, got %#v", byID["openai/gpt-5.4"])
	}
}
