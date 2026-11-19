package app

import (
	"fmt"
	"strings"

	"aegis-agent/internal/config"
	"aegis-agent/internal/session"
)

func providerOptionsForThinkingLevel(level string, cfg *config.Config, providerName string) (session.ProviderOptions, error) {
	level = strings.ToLower(strings.TrimSpace(level))
	if level == "" {
		return session.ProviderOptions{}, nil
	}
	switch level {
	case "low", "medium", "high", "xhigh", "max":
	default:
		return session.ProviderOptions{}, fmt.Errorf("unsupported --thinking-level %q", level)
	}
	if cfg == nil {
		return session.ProviderOptions{}, fmt.Errorf("config is required for --thinking-level")
	}
	providerName = strings.TrimSpace(providerName)
	if providerName == "" || strings.EqualFold(providerName, "default") {
		providerName = cfg.DefaultProvider
	}
	providerCfg, err := cfg.ProviderConfig(providerName)
	if err != nil {
		return session.ProviderOptions{}, err
	}
	apiProvider, err := config.EffectiveAPIProvider(providerName, providerCfg)
	if err != nil {
		return session.ProviderOptions{}, err
	}
	switch apiProvider {
	case "openai-compatible":
		return session.ProviderOptions{ReasoningEffort: level}, nil
	case "anthropic-compatible":
		includeThoughts := true
		opts := session.ProviderOptions{
			IncludeThoughts: &includeThoughts,
			ThinkingBudget:  thinkingBudgetForLevel(level),
		}
		// Profiles that opted into the Messages output_config.effort wire field
		// (effort-capable Claude models, MiniMax and compatible gateways) also
		// carry the level there. Profiles without the field keep the
		// budget-only mapping, so endpoints or models that do not accept
		// output_config never receive it.
		if strings.TrimSpace(providerCfg.OutputConfigEffort) != "" {
			opts.OutputConfigEffort = level
		}
		return opts, nil
	case "google":
		includeThoughts := true
		return session.ProviderOptions{
			IncludeThoughts: &includeThoughts,
			ThinkingBudget:  thinkingBudgetForLevel(level),
		}, nil
	default:
		return session.ProviderOptions{}, fmt.Errorf("unsupported api_provider for --thinking-level: %s", apiProvider)
	}
}

func thinkingBudgetForLevel(level string) int {
	switch level {
	case "low":
		return 1024
	case "medium":
		return 4096
	case "high":
		return 8192
	case "xhigh":
		return 16384
	case "max":
		return 32000
	default:
		return 0
	}
}
