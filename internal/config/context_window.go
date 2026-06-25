package config

import (
	"sort"
	"strings"
)

const (
	DefaultContextWindowTokens                 = 200000
	DefaultCompactUtilizationFactor            = 0.85
	DefaultCompactSemanticSummaryMaxInputChars = 200000
	DefaultCompactSemanticSummaryTimeoutSec    = 60

	compactCharsPerToken = 4
)

var knownModelContextWindows = map[string]int{
	"gpt-5.5": 300000,
}

func KnownModelContextWindow(model string) (int, bool) {
	normalized := normalizeModelContextWindowKey(model)
	if normalized == "" {
		return 0, false
	}
	if tokens, ok := knownModelContextWindows[normalized]; ok {
		return tokens, true
	}
	if idx := strings.LastIndex(normalized, "/"); idx >= 0 && idx < len(normalized)-1 {
		if tokens, ok := knownModelContextWindows[normalized[idx+1:]]; ok {
			return tokens, true
		}
	}
	keys := make([]string, 0, len(knownModelContextWindows))
	for key := range knownModelContextWindows {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return len(keys[i]) > len(keys[j])
	})
	for _, key := range keys {
		if strings.Contains(normalized, key) {
			return knownModelContextWindows[key], true
		}
	}
	return 0, false
}

func ResolveContextWindowTokens(model string, configured int) int {
	if configured > 0 {
		return configured
	}
	if tokens, ok := KnownModelContextWindow(model); ok {
		return tokens
	}
	return DefaultContextWindowTokens
}

func DeriveInputCharThreshold(contextWindowTokens int, utilizationFactor float64) int {
	if contextWindowTokens <= 0 {
		contextWindowTokens = DefaultContextWindowTokens
	}
	if utilizationFactor <= 0 || utilizationFactor > 1 {
		utilizationFactor = DefaultCompactUtilizationFactor
	}
	return int(float64(contextWindowTokens) * compactCharsPerToken * utilizationFactor)
}

func normalizeModelContextWindowKey(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}
