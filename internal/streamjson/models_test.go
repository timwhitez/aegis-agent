package streamjson

import (
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
			if model.Thinking == nil || len(model.Thinking.SupportedLevels) != 4 || model.Thinking.DefaultLevel != "medium" {
				t.Fatalf("expected thinking catalog, got %#v", model.Thinking)
			}
			if model.ContextWindow != config.DefaultContextWindowTokens {
				t.Fatalf("expected default context window, got %#v", model)
			}
			var hasXHigh bool
			for _, level := range model.Thinking.SupportedLevels {
				if level.Value == "xhigh" {
					hasXHigh = true
				}
			}
			if !hasXHigh {
				t.Fatalf("expected xhigh thinking level, got %#v", model.Thinking.SupportedLevels)
			}
		}
	}
	if !foundDefault {
		t.Fatalf("expected default anthropic route in %#v", models)
	}
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
