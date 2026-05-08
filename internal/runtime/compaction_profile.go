package runtime

import (
	"strings"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/session"
)

type compactionContextProfile struct {
	Provider              string `json:"provider,omitempty"`
	Model                 string `json:"model,omitempty"`
	Source                string `json:"source,omitempty"`
	InputCharThreshold    int    `json:"input_char_threshold,omitempty"`
	KeepRecentToolResults int    `json:"keep_recent_tool_results,omitempty"`
	HysteresisDeltaChars  int    `json:"hysteresis_delta_chars,omitempty"`
}

func compactionProfileFromConfig(meta session.SessionMetadata, cfg config.CompactConfig) compactionContextProfile {
	profile := compactionContextProfile{
		Provider:              strings.TrimSpace(meta.Provider),
		Model:                 strings.TrimSpace(meta.Model),
		Source:                "runtime.compact",
		InputCharThreshold:    cfg.InputCharThreshold,
		KeepRecentToolResults: cfg.KeepRecentToolResults,
		HysteresisDeltaChars:  cfg.HysteresisDeltaChars,
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
}

func normalizeCompactionProfile(profile compactionContextProfile) compactionContextProfile {
	if profile.InputCharThreshold <= 0 {
		profile.InputCharThreshold = 160000
	}
	if profile.KeepRecentToolResults <= 0 {
		profile.KeepRecentToolResults = 3
	}
	if profile.HysteresisDeltaChars <= 0 {
		profile.HysteresisDeltaChars = 40000
	}
	if strings.TrimSpace(profile.Source) == "" {
		profile.Source = "runtime.compact"
	}
	return profile
}

func compactionProfileForPolicy(threshold, keepRecent, hysteresisDelta int) compactionContextProfile {
	return normalizeCompactionProfile(compactionContextProfile{
		Source:                "legacy_policy",
		InputCharThreshold:    threshold,
		KeepRecentToolResults: keepRecent,
		HysteresisDeltaChars:  hysteresisDelta,
	})
}
