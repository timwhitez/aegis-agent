package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestThinkingConfigYAMLRoundTripDistinguishesUnsetZeroDynamicPositive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := `schema_version: 1
default_provider: google_zero
providers:
  google_unset:
    api_provider: google
    api_key_env: GEMINI_API_KEY
    base_url: https://generativelanguage.googleapis.com
    model: gemini-2.5-flash
  google_zero:
    api_provider: google
    api_key_env: GEMINI_API_KEY
    base_url: https://generativelanguage.googleapis.com
    model: gemini-2.5-flash
    thinking_budget: 0
  google_dynamic:
    api_provider: google
    api_key_env: GEMINI_API_KEY
    base_url: https://generativelanguage.googleapis.com
    model: gemini-2.5-flash-lite
    thinking_budget: -1
    thinking_display: omitted
  google_positive:
    api_provider: google
    api_key_env: GEMINI_API_KEY
    base_url: https://generativelanguage.googleapis.com
    model: gemini-2.5-flash
    thinking_budget: 2048
    thinking_display: summarized
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, _, err := LoadWithReport(path, dir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Providers["google_unset"].ThinkingBudget != nil {
		t.Fatalf("expected unset thinking_budget to stay nil, got %v", *cfg.Providers["google_unset"].ThinkingBudget)
	}
	if got := cfg.Providers["google_zero"].ThinkingBudget; got == nil || *got != 0 {
		t.Fatalf("expected explicit 0 thinking_budget, got %#v", got)
	}
	if got := cfg.Providers["google_dynamic"].ThinkingBudget; got == nil || *got != -1 {
		t.Fatalf("expected explicit -1 thinking_budget, got %#v", got)
	}
	if got := cfg.Providers["google_dynamic"].ThinkingDisplay; got != ThinkingDisplayOmitted {
		t.Fatalf("expected thinking_display=omitted, got %q", got)
	}
	if got := cfg.Providers["google_positive"].ThinkingBudget; got == nil || *got != 2048 {
		t.Fatalf("expected explicit 2048 thinking_budget, got %#v", got)
	}
	if got := cfg.Providers["google_positive"].ThinkingDisplay; got != ThinkingDisplaySummarized {
		t.Fatalf("expected thinking_display=summarized, got %q", got)
	}

	marshaled, err := MarshalYAML(cfg)
	if err != nil {
		t.Fatalf("marshal yaml: %v", err)
	}
	text := string(marshaled)
	if !strings.Contains(text, "thinking_budget: 0") || !strings.Contains(text, "thinking_budget: -1") {
		t.Fatalf("expected explicit 0 and -1 budgets to survive YAML marshal:\n%s", text)
	}
}

func TestLegacyIncludeThoughtsFalseMigratesWithClosePriority(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := `schema_version: 1
default_provider: google
providers:
  google:
    api_provider: google
    api_key_env: GEMINI_API_KEY
    base_url: https://generativelanguage.googleapis.com
    model: gemini-2.5-flash
    thinking_budget: 1024
    include_thoughts: false
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, _, err := LoadWithReport(path, dir)
	if err != nil {
		t.Fatalf("load legacy off config: %v", err)
	}
	p := cfg.Providers["google"]
	if p.ThinkingMode != ThinkingModeDisabled {
		t.Fatalf("expected legacy include_thoughts=false to migrate to thinking_mode=disabled, got %#v", p)
	}
	if p.ThinkingBudget != nil {
		t.Fatalf("expected conflicting thinking_budget to be cleared under close-priority migration, got %v", *p.ThinkingBudget)
	}
	if p.IncludeThoughts != nil {
		t.Fatalf("expected legacy include_thoughts=false to clear after migration, got %v", *p.IncludeThoughts)
	}
}

func TestThinkingConfigValidationRejectsIllegalCombinations(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "google 2.5 pro rejects legacy include_thoughts false",
			yaml: `schema_version: 1
providers:
  google:
    api_provider: google
    api_key_env: GEMINI_API_KEY
    base_url: https://generativelanguage.googleapis.com
    model: gemini-2.5-pro
    include_thoughts: false
`,
			wantErr: "does not support disabling thinking",
		},
		{
			name: "google 2.5 pro rejects budget 0",
			yaml: `schema_version: 1
providers:
  google:
    api_provider: google
    api_key_env: GEMINI_API_KEY
    base_url: https://generativelanguage.googleapis.com
    model: gemini-2.5-pro
    thinking_budget: 0
`,
			wantErr: "does not support disabling thinking",
		},
		{
			name: "google 2.5 flash rejects budget above 24576",
			yaml: `schema_version: 1
providers:
  google:
    api_provider: google
    api_key_env: GEMINI_API_KEY
    base_url: https://generativelanguage.googleapis.com
    model: gemini-2.5-flash
    thinking_budget: 32000
`,
			wantErr: "exceeds maximum 24576",
		},
		{
			name: "anthropic opus 4.7 rejects manual thinking_budget",
			yaml: `schema_version: 1
providers:
  anthropic:
    api_provider: anthropic-compatible
    api_key_env: ANTHROPIC_API_KEY
    base_url: https://api.anthropic.com
    model: claude-opus-4-7
    thinking_budget: 8192
`,
			wantErr: "requires adaptive thinking",
		},
		{
			name: "anthropic adaptive rejects manual budget",
			yaml: `schema_version: 1
providers:
  anthropic:
    api_provider: anthropic-compatible
    api_key_env: ANTHROPIC_API_KEY
    base_url: https://api.anthropic.com
    model: claude-sonnet-4-6
    thinking_mode: adaptive
    thinking_budget: 4096
`,
			wantErr: "cannot combine thinking_mode=adaptive",
		},
		{
			name: "explicit enabled mode conflicts with legacy include_thoughts false",
			yaml: `schema_version: 1
providers:
  google:
    api_provider: google
    api_key_env: GEMINI_API_KEY
    base_url: https://generativelanguage.googleapis.com
    model: gemini-2.5-flash
    thinking_mode: enabled
    thinking_budget: 1024
    include_thoughts: false
`,
			wantErr: "thinking_display=omitted",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			_, _, err := LoadWithReport(path, dir)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestAnthropicModelAdaptiveClassification(t *testing.T) {
	requiresAdaptive := []string{
		"claude-opus-4-7",
		"claude-opus-4-7-20260601",
		"claude-opus-4.7",
		"claude-opus-4-8",
		"claude-opus-5-5",
		"claude-sonnet-5-0",
		"claude-haiku-5-1",
	}
	for _, model := range requiresAdaptive {
		if !AnthropicModelRequiresAdaptiveThinking(model) {
			t.Fatalf("expected %q to require adaptive thinking", model)
		}
	}
	allowsManual := []string{
		"claude-sonnet-4-6",
		"claude-opus-4-6",
		"claude-sonnet-4-5",
		"claude-opus-4-5",
		"claude-3-7-sonnet-20250219",
		"MiniMax-M3.1-Flash",
		"deepseek-chat",
	}
	for _, model := range allowsManual {
		if AnthropicModelRequiresAdaptiveThinking(model) {
			t.Fatalf("expected %q to allow manual thinking", model)
		}
	}
}
