package streamjson

import (
	"testing"

	"go-cli-agent/internal/config"
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
			if model.Thinking == nil || len(model.Thinking.SupportedLevels) != 3 || model.Thinking.DefaultLevel != "medium" {
				t.Fatalf("expected thinking catalog, got %#v", model.Thinking)
			}
		}
	}
	if !foundDefault {
		t.Fatalf("expected default anthropic route in %#v", models)
	}
}
