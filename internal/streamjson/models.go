package streamjson

import (
	"fmt"
	"sort"
	"strings"

	"go-cli-agent/internal/config"
)

type Model struct {
	ID       string         `json:"id"`
	Label    string         `json:"label"`
	Provider string         `json:"provider,omitempty"`
	Default  bool           `json:"default"`
	Thinking *ModelThinking `json:"thinking,omitempty"`
}

type ModelThinking struct {
	SupportedLevels []ThinkingLevel `json:"supported_levels,omitempty"`
	DefaultLevel    string          `json:"default_level,omitempty"`
}

type ThinkingLevel struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

func ModelsFromConfig(cfg *config.Config) ([]Model, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	models := make([]Model, 0, len(names))
	for _, name := range names {
		providerCfg := cfg.Providers[name]
		modelID := strings.TrimSpace(providerCfg.Model)
		if modelID == "" {
			continue
		}
		apiProvider, err := config.EffectiveAPIProvider(name, providerCfg)
		if err != nil {
			return nil, err
		}
		model := Model{
			ID:       name + "/" + modelID,
			Label:    name + ": " + modelID,
			Provider: protocolProviderName(apiProvider),
			Default:  name == cfg.DefaultProvider,
		}
		if levels := supportedThinkingLevels(apiProvider); len(levels) > 0 {
			model.Thinking = &ModelThinking{
				SupportedLevels: levels,
				DefaultLevel:    defaultThinkingLevel(apiProvider, providerCfg),
			}
		}
		models = append(models, model)
	}
	return models, nil
}

func protocolProviderName(apiProvider string) string {
	switch strings.TrimSpace(apiProvider) {
	case "openai-compatible":
		return "openai"
	case "anthropic-compatible":
		return "anthropic"
	case "google":
		return "google"
	default:
		return ""
	}
}

func supportedThinkingLevels(apiProvider string) []ThinkingLevel {
	switch strings.TrimSpace(apiProvider) {
	case "openai-compatible", "anthropic-compatible", "google":
		return []ThinkingLevel{
			{Value: "low", Label: "Low", Description: "Use a small reasoning or thinking budget."},
			{Value: "medium", Label: "Medium", Description: "Use the default reasoning or thinking budget."},
			{Value: "high", Label: "High", Description: "Use a larger reasoning or thinking budget."},
			{Value: "xhigh", Label: "XHigh", Description: "Use the largest gocli reasoning or thinking budget."},
		}
	default:
		return nil
	}
}

func defaultThinkingLevel(apiProvider string, providerCfg config.Provider) string {
	switch strings.TrimSpace(apiProvider) {
	case "openai-compatible":
		if level := normalizeThinkingLevel(providerCfg.ReasoningEffort); level != "" {
			return level
		}
	case "anthropic-compatible", "google":
		switch {
		case providerCfg.ThinkingBudget > 8192:
			return "xhigh"
		case providerCfg.ThinkingBudget > 4096:
			return "high"
		case providerCfg.ThinkingBudget > 0 && providerCfg.ThinkingBudget <= 1024:
			return "low"
		case providerCfg.ThinkingBudget > 0:
			return "medium"
		}
	}
	return "medium"
}

func normalizeThinkingLevel(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "low", "medium", "high", "xhigh":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}
