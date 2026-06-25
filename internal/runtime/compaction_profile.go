package runtime

import (
	"strings"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/session"
)

type compactionContextProfile struct {
	Provider              string  `json:"provider,omitempty"`
	Model                 string  `json:"model,omitempty"`
	Source                string  `json:"source,omitempty"`
	ThresholdSource       string  `json:"threshold_source,omitempty"`
	ContextWindowTokens   int     `json:"context_window_tokens,omitempty"`
	UtilizationFactor     float64 `json:"utilization_factor,omitempty"`
	InputCharThreshold    int     `json:"input_char_threshold,omitempty"`
	KeepRecentToolResults int     `json:"keep_recent_tool_results,omitempty"`
	HysteresisDeltaChars  int     `json:"hysteresis_delta_chars,omitempty"`
	KeepRecentMessages    int     `json:"keep_recent_messages,omitempty"`
}

func compactionProfileFromConfig(meta session.SessionMetadata, cfg config.CompactConfig) compactionContextProfile {
	contextWindowTokens := config.ResolveContextWindowTokens(meta.Model, meta.ProviderOptions.ContextWindowTokens)
	utilizationFactor := cfg.UtilizationFactor
	if utilizationFactor <= 0 || utilizationFactor > 1 {
		utilizationFactor = config.DefaultCompactUtilizationFactor
	}
	inputThreshold := cfg.InputCharThreshold
	thresholdSource := "explicit"
	if inputThreshold <= 0 {
		inputThreshold = config.DeriveInputCharThreshold(contextWindowTokens, utilizationFactor)
		thresholdSource = "context_window"
	}
	profile := compactionContextProfile{
		Provider:              strings.TrimSpace(meta.Provider),
		Model:                 strings.TrimSpace(meta.Model),
		Source:                "runtime.compact",
		ThresholdSource:       thresholdSource,
		ContextWindowTokens:   contextWindowTokens,
		UtilizationFactor:     utilizationFactor,
		InputCharThreshold:    inputThreshold,
		KeepRecentToolResults: cfg.KeepRecentToolResults,
		HysteresisDeltaChars:  cfg.HysteresisDeltaChars,
		KeepRecentMessages:    cfg.KeepRecentMessages,
	}
	if len(cfg.ContextProfiles) == 0 {
		return normalizeCompactionProfile(profile)
	}
	keys := []string{
		strings.ToLower(strings.TrimSpace(meta.Provider + "/" + meta.Model)),
		strings.ToLower(strings.TrimSpace(meta.Model)),
		strings.ToLower(strings.TrimSpace(meta.Provider)),
	}
	for _, key := range keys {
		if key == "" || key == "/" {
			continue
		}
		override, ok := lookupCompactionProfile(cfg.ContextProfiles, key)
		if !ok {
			continue
		}
		profile.Source = "runtime.compact.context_profiles." + key
		profile.ThresholdSource = "context_profiles." + key
		applyCompactionProfileOverride(&profile, override)
		return normalizeCompactionProfile(profile)
	}
	return normalizeCompactionProfile(profile)
}

func lookupCompactionProfile(profiles map[string]config.CompactContextProfile, key string) (config.CompactContextProfile, bool) {
	if profile, ok := profiles[key]; ok {
		return profile, true
	}
	for candidate, profile := range profiles {
		if strings.EqualFold(strings.TrimSpace(candidate), key) {
			return profile, true
		}
	}
	return config.CompactContextProfile{}, false
}

func applyCompactionProfileOverride(profile *compactionContextProfile, override config.CompactContextProfile) {
	if override.InputCharThreshold > 0 {
		profile.InputCharThreshold = override.InputCharThreshold
	}
	if override.KeepRecentToolResults > 0 {
		profile.KeepRecentToolResults = override.KeepRecentToolResults
	}
	if override.HysteresisDeltaChars > 0 {
		profile.HysteresisDeltaChars = override.HysteresisDeltaChars
	}
	if override.KeepRecentMessages > 0 {
		profile.KeepRecentMessages = override.KeepRecentMessages
	}
}

func normalizeCompactionProfile(profile compactionContextProfile) compactionContextProfile {
	if profile.InputCharThreshold <= 0 {
		profile.InputCharThreshold = 160000
	}
	if profile.KeepRecentToolResults <= 0 {
		profile.KeepRecentToolResults = 3
	}
	if profile.HysteresisDeltaChars <= 0 {
		profile.HysteresisDeltaChars = profile.InputCharThreshold / 4
		if profile.HysteresisDeltaChars <= 0 {
			profile.HysteresisDeltaChars = 40000
		}
	}
	if profile.KeepRecentMessages <= 0 {
		profile.KeepRecentMessages = deriveKeepRecentMessages(profile.InputCharThreshold)
	}
	if strings.TrimSpace(profile.Source) == "" {
		profile.Source = "runtime.compact"
	}
	if strings.TrimSpace(profile.ThresholdSource) == "" {
		profile.ThresholdSource = "explicit"
	}
	if profile.ContextWindowTokens <= 0 {
		profile.ContextWindowTokens = config.DefaultContextWindowTokens
	}
	if profile.UtilizationFactor <= 0 || profile.UtilizationFactor > 1 {
		profile.UtilizationFactor = config.DefaultCompactUtilizationFactor
	}
	return profile
}

func compactionProfileForPolicy(threshold, keepRecent, hysteresisDelta int) compactionContextProfile {
	return normalizeCompactionProfile(compactionContextProfile{
		Source:                "legacy_policy",
		InputCharThreshold:    threshold,
		KeepRecentToolResults: keepRecent,
		HysteresisDeltaChars:  hysteresisDelta,
		KeepRecentMessages:    deriveKeepRecentMessages(threshold),
	})
}

func deriveKeepRecentMessages(threshold int) int {
	const (
		minRecentMessages = 6
		maxRecentMessages = 60
		charsPerMessage   = 27000
	)
	if threshold <= 0 {
		return minRecentMessages
	}
	count := threshold / charsPerMessage
	if count < minRecentMessages {
		return minRecentMessages
	}
	if count > maxRecentMessages {
		return maxRecentMessages
	}
	return count
}
