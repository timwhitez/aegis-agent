package config

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	ThinkingModeAdaptive = "adaptive"
	ThinkingModeEnabled  = "enabled"
	ThinkingModeDisabled = "disabled"

	ThinkingDisplaySummarized = "summarized"
	ThinkingDisplayOmitted    = "omitted"
)

// IntPtr returns a pointer to value.
func IntPtr(value int) *int {
	v := value
	return &v
}

// CloneIntPtr returns a shallow copy of an optional int pointer.
func CloneIntPtr(value *int) *int {
	if value == nil {
		return nil
	}
	v := *value
	return &v
}

// ThinkingBudgetValue dereferences an optional thinking budget, returning 0
// when unset. Callers that need to distinguish unset from explicit 0 or -1
// must inspect the pointer directly.
func ThinkingBudgetValue(budget *int) int {
	if budget == nil {
		return 0
	}
	return *budget
}

// NormalizeThinkingMode normalizes canonical and alias thinking mode values.
// Unknown non-empty values are returned lower-cased so validation can reject
// them explicitly.
func NormalizeThinkingMode(value string) string {
	switch normalized := strings.ToLower(strings.TrimSpace(value)); normalized {
	case "":
		return ""
	case ThinkingModeAdaptive:
		return ThinkingModeAdaptive
	case ThinkingModeEnabled, "manual":
		return ThinkingModeEnabled
	case ThinkingModeDisabled, "off":
		return ThinkingModeDisabled
	default:
		return normalized
	}
}

// NormalizeThinkingDisplay normalizes thinking summary display settings.
// Unknown non-empty values are returned lower-cased so validation can reject
// them explicitly.
func NormalizeThinkingDisplay(value string) string {
	switch normalized := strings.ToLower(strings.TrimSpace(value)); normalized {
	case "":
		return ""
	case ThinkingDisplaySummarized:
		return ThinkingDisplaySummarized
	case ThinkingDisplayOmitted:
		return ThinkingDisplayOmitted
	default:
		return normalized
	}
}

func normalizeProviderThinking(provider *Provider) {
	if provider == nil {
		return
	}
	provider.ThinkingMode = NormalizeThinkingMode(provider.ThinkingMode)
	provider.ThinkingDisplay = NormalizeThinkingDisplay(provider.ThinkingDisplay)
	// Preserve the repo's legacy close semantics for include_thoughts=false:
	// when thinking_mode is unset (or already disabled), migrate to explicit
	// thinking_mode=disabled with close-priority over any stale thinking_budget
	// so the config never silently turns into active thinking.
	// If thinking_mode explicitly enables thinking (enabled/adaptive) while
	// include_thoughts=false is also set, keep both so ValidateProviderThinking
	// rejects the conflict and points operators to thinking_display=omitted.
	if provider.IncludeThoughts != nil && !*provider.IncludeThoughts {
		if provider.ThinkingMode == "" || provider.ThinkingMode == ThinkingModeDisabled {
			provider.ThinkingMode = ThinkingModeDisabled
			provider.ThinkingBudget = nil
			provider.IncludeThoughts = nil
		}
	}
}

// ValidateProviderThinking validates provider-level thinking settings after
// normalization.
func ValidateProviderThinking(name string, provider Provider) error {
	mode := NormalizeThinkingMode(provider.ThinkingMode)
	switch mode {
	case "", ThinkingModeAdaptive, ThinkingModeEnabled, ThinkingModeDisabled:
	default:
		return fmt.Errorf("provider %q has unsupported thinking_mode %q (expected adaptive, enabled, or disabled)", name, provider.ThinkingMode)
	}
	display := NormalizeThinkingDisplay(provider.ThinkingDisplay)
	switch display {
	case "", ThinkingDisplaySummarized, ThinkingDisplayOmitted:
	default:
		return fmt.Errorf("provider %q has unsupported thinking_display %q (expected summarized or omitted)", name, provider.ThinkingDisplay)
	}
	if provider.IncludeThoughts != nil && !*provider.IncludeThoughts && (mode == ThinkingModeEnabled || mode == ThinkingModeAdaptive) {
		return fmt.Errorf("provider %q combines thinking_mode=%s with legacy include_thoughts=false; use thinking_display=omitted to hide thought summaries while keeping thinking active", name, mode)
	}
	apiProvider, err := EffectiveAPIProvider(name, provider)
	if err != nil {
		return nil
	}
	switch apiProvider {
	case "anthropic-compatible":
		if provider.ThinkingBudget != nil && *provider.ThinkingBudget < 0 {
			return fmt.Errorf("provider %q anthropic thinking_budget must be non-negative, got %d", name, *provider.ThinkingBudget)
		}
		if mode == ThinkingModeAdaptive && provider.ThinkingBudget != nil && *provider.ThinkingBudget > 0 {
			return fmt.Errorf("provider %q cannot combine thinking_mode=adaptive with manual thinking_budget=%d", name, *provider.ThinkingBudget)
		}
		if AnthropicModelRequiresAdaptiveThinking(provider.Model) {
			if mode == ThinkingModeEnabled {
				return fmt.Errorf("provider %q model %q requires adaptive thinking; thinking_mode=enabled is not supported", name, provider.Model)
			}
			if mode == "" && provider.ThinkingBudget != nil && *provider.ThinkingBudget > 0 {
				return fmt.Errorf("provider %q model %q requires adaptive thinking; manual thinking_budget=%d is not supported", name, provider.Model, *provider.ThinkingBudget)
			}
		}
	case "google":
		if mode == ThinkingModeAdaptive {
			return fmt.Errorf("provider %q google adapter does not support thinking_mode=adaptive", name)
		}
		if mode == ThinkingModeDisabled {
			if err := ValidateGoogleThinkingBudget(provider.Model, 0); err != nil {
				return fmt.Errorf("provider %q: %w", name, err)
			}
		}
		if provider.ThinkingBudget != nil {
			if err := ValidateGoogleThinkingBudget(provider.Model, *provider.ThinkingBudget); err != nil {
				return fmt.Errorf("provider %q: %w", name, err)
			}
		}
	}
	return nil
}

// ValidateProvidersThinking validates thinking settings across all configured
// providers.
func ValidateProvidersThinking(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	for name, provider := range cfg.Providers {
		if err := ValidateProviderThinking(name, provider); err != nil {
			return err
		}
	}
	return nil
}

// claudeModelVersion parses a native Claude model identifier such as
// "claude-opus-4-7", "claude-opus-4-7-20260601", "claude-sonnet-4-6",
// "claude-opus-5-5", or "claude-opus-4.7" into (family, major, minor, ok).
// Legacy 3.x naming ("claude-3-7-sonnet") returns ok=true with major=3, minor=7.
func claudeModelVersion(model string) (family string, major int, minor int, ok bool) {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if !strings.HasPrefix(normalized, "claude-") {
		return "", 0, 0, false
	}
	rest := strings.TrimPrefix(normalized, "claude-")
	tokens := strings.Split(rest, "-")
	if len(tokens) == 0 {
		return "", 0, 0, false
	}
	// Pattern A: claude-<family>-<major>-<minor>[-...] or claude-<family>-<major>.<minor>[-...]
	switch tokens[0] {
	case "opus", "sonnet", "haiku":
		family = tokens[0]
		if len(tokens) < 2 {
			return "", 0, 0, false
		}
		if maj, min, parsed := parseDotVersion(tokens[1]); parsed {
			return family, maj, min, true
		}
		maj, err := strconv.Atoi(tokens[1])
		if err != nil {
			return "", 0, 0, false
		}
		if len(tokens) >= 3 {
			if min, err := strconv.Atoi(tokens[2]); err == nil && min < 1000 {
				return family, maj, min, true
			}
		}
		return family, maj, 0, true
	default:
		// Pattern B: claude-<major>-<minor>-<family>[-...] or claude-<major>.<minor>[-...]
		if maj, min, parsed := parseDotVersion(tokens[0]); parsed {
			if len(tokens) >= 2 {
				family = tokens[1]
			}
			return family, maj, min, true
		}
		maj, err := strconv.Atoi(tokens[0])
		if err != nil {
			return "", 0, 0, false
		}
		if len(tokens) >= 2 {
			if min, err := strconv.Atoi(tokens[1]); err == nil && min < 1000 {
				if len(tokens) >= 3 {
					family = tokens[2]
				}
				return family, maj, min, true
			}
		}
		return "", maj, 0, true
	}
}

func parseDotVersion(token string) (int, int, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return 0, 0, false
	}
	maj, err1 := strconv.Atoi(parts[0])
	min, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return maj, min, true
}

// AnthropicModelRequiresAdaptiveThinking reports whether model is a native
// Claude model that rejects manual thinking (type="enabled" + budget_tokens)
// with HTTP 400 and requires type="adaptive": Claude Opus 4.7/4.8 and Claude 5.x+.
func AnthropicModelRequiresAdaptiveThinking(model string) bool {
	family, major, minor, ok := claudeModelVersion(model)
	if !ok {
		return false
	}
	if major >= 5 {
		return true
	}
	if major == 4 && family == "opus" && minor >= 7 {
		return true
	}
	return false
}

// AnthropicModelSupportsAdaptiveThinking reports whether model is a native
// Claude model that supports adaptive thinking (Claude 4.6+, Opus 4.7/4.8, 5.x+).
func AnthropicModelSupportsAdaptiveThinking(model string) bool {
	if AnthropicModelRequiresAdaptiveThinking(model) {
		return true
	}
	_, major, minor, ok := claudeModelVersion(model)
	if !ok {
		return false
	}
	return major == 4 && minor >= 6
}

// AnthropicThinkingModeForLevel decides whether --thinking-level should build
// adaptive thinking + output_config.effort or manual thinking budget for a
// provider profile and effective model.
//
// Policy:
//   - Explicit thinking_mode ("adaptive" or "enabled") on the profile wins.
//   - Native Claude models that require adaptive (Opus 4.7/4.8, 5.x+) always
//     use "adaptive".
//   - Claude 4.6 (still accepts deprecated manual thinking), older Claude
//     models (4.5, 3.7), and non-Claude compatible gateways default to
//     "enabled" unless they explicitly opt into thinking_mode="adaptive".
func AnthropicThinkingModeForLevel(provider Provider, modelOverride string) string {
	mode := NormalizeThinkingMode(provider.ThinkingMode)
	if mode == ThinkingModeAdaptive || mode == ThinkingModeEnabled {
		return mode
	}
	model := strings.TrimSpace(modelOverride)
	if model == "" {
		model = strings.TrimSpace(provider.Model)
	}
	if AnthropicModelRequiresAdaptiveThinking(model) {
		return ThinkingModeAdaptive
	}
	return ThinkingModeEnabled
}

// GoogleThinkingSpec describes the documented thinkingBudget constraints for a
// Gemini model family on the native generateContent API.
type GoogleThinkingSpec struct {
	Known           bool
	MinPositive     int
	MaxBudget       int
	SupportsDisable bool
	DefaultDisabled bool
}

// GoogleThinkingBudgetSpec returns the thinking budget bounds for a Gemini
// model. Unknown models return Known=false with conservative defaults.
func GoogleThinkingBudgetSpec(model string) GoogleThinkingSpec {
	normalized := strings.ToLower(strings.TrimSpace(model))
	normalized = strings.TrimPrefix(normalized, "models/")
	switch {
	case strings.HasPrefix(normalized, "gemini-2.5-flash-lite"):
		return GoogleThinkingSpec{
			Known:           true,
			MinPositive:     512,
			MaxBudget:       24576,
			SupportsDisable: true,
			DefaultDisabled: true,
		}
	case strings.HasPrefix(normalized, "gemini-2.5-flash"):
		return GoogleThinkingSpec{
			Known:           true,
			MinPositive:     1,
			MaxBudget:       24576,
			SupportsDisable: true,
			DefaultDisabled: false,
		}
	case strings.HasPrefix(normalized, "gemini-2.5-pro"):
		return GoogleThinkingSpec{
			Known:           true,
			MinPositive:     128,
			MaxBudget:       32768,
			SupportsDisable: false,
			DefaultDisabled: false,
		}
	default:
		return GoogleThinkingSpec{
			Known:           false,
			MinPositive:     1,
			MaxBudget:       32768,
			SupportsDisable: true,
			DefaultDisabled: false,
		}
	}
}

// GoogleThinkingBudgetForLevel maps a CLI --thinking-level value to a legal
// Gemini thinkingBudget for the target model.
func GoogleThinkingBudgetForLevel(model, level string) int {
	spec := GoogleThinkingBudgetSpec(model)
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "low":
		return 1024
	case "medium":
		return 4096
	case "high":
		return 8192
	case "xhigh":
		return 16384
	case "max":
		return spec.MaxBudget
	default:
		return 0
	}
}

// ValidateGoogleThinkingBudget validates a Google thinkingBudget value against
// the target model's documented bounds.
func ValidateGoogleThinkingBudget(model string, budget int) error {
	if budget < -1 {
		return fmt.Errorf("google thinking_budget for model %q must be -1 (dynamic), 0 (off), or positive, got %d", model, budget)
	}
	spec := GoogleThinkingBudgetSpec(model)
	if !spec.Known {
		return nil
	}
	if budget == 0 && !spec.SupportsDisable {
		return fmt.Errorf("google model %q does not support disabling thinking (thinking_budget=0)", model)
	}
	if budget > 0 {
		if budget < spec.MinPositive {
			return fmt.Errorf("google thinking_budget %d is below minimum %d for model %q", budget, spec.MinPositive, model)
		}
		if budget > spec.MaxBudget {
			return fmt.Errorf("google thinking_budget %d exceeds maximum %d for model %q", budget, spec.MaxBudget, model)
		}
	}
	return nil
}
