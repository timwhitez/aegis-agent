package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeStrictConfigFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadRejectsUnknownConfigurationFields(t *testing.T) {
	tests := []struct {
		name    string
		content string
		path    string
	}{
		{
			name:    "top level",
			content: "schema_verzion: 1\n",
			path:    "schema_verzion",
		},
		{
			name:    "nested shell policy",
			content: "runtime:\n  shell:\n    sanbox: bwrap\n",
			path:    "runtime.shell.sanbox",
		},
		{
			name:    "provider entry",
			content: "providers:\n  custom:\n    api_key_env: CUSTOM_API_KEY\n    base_url: http://localhost/v1\n    model: test\n    modle: typo\n",
			path:    "providers.custom.modle",
		},
		{
			name:    "hook entry",
			content: "hooks:\n  session_start:\n    - name: policy\n      command: [\"true\"]\n      fail_close: true\n",
			path:    "hooks.session_start[0].fail_close",
		},
		{
			name:    "retry entry",
			content: "providers:\n  custom:\n    api_key_env: CUSTOM_API_KEY\n    base_url: http://localhost/v1\n    model: test\n    retry:\n      max_atempts: 2\n",
			path:    "providers.custom.retry.max_atempts",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeStrictConfigFixture(t, tt.content), t.TempDir())
			if err == nil {
				t.Fatalf("unknown field %s was silently accepted", tt.path)
			}
			if !strings.Contains(err.Error(), tt.path) {
				t.Fatalf("error does not identify %s: %v", tt.path, err)
			}
		})
	}
}

func TestLoadAcceptsArbitraryProviderNamesAndKnownNestedFields(t *testing.T) {
	path := writeStrictConfigFixture(t, `
schema_version: 1
default_provider: private-gateway
providers:
  private-gateway:
    api_provider: openai-compatible
    api_key_env: PRIVATE_API_KEY
    base_url: http://localhost:8080/v1
    model: private-model
    retry:
      max_attempts: 2
      retry_transport: true
runtime:
  shell:
    sandbox: bwrap
  exec_policy:
    mode: deny
`)
	cfg, err := Load(path, t.TempDir())
	if err != nil {
		t.Fatalf("valid configuration was rejected: %v", err)
	}
	if cfg.DefaultProvider != "private-gateway" {
		t.Fatalf("valid provider layer was not applied: %q", cfg.DefaultProvider)
	}
	if cfg.Providers["private-gateway"].Retry.MaxAttempts != 2 {
		t.Fatalf("known nested provider fields were not decoded: %#v", cfg.Providers["private-gateway"])
	}
}

func TestLoadAcceptsDocumentedDeprecatedChildBudgetAliases(t *testing.T) {
	path := writeStrictConfigFixture(t, `
runtime:
  child_budget:
    max_wall_clock_sec: 19
    max_turns: 7
`)
	cfg, err := Load(path, t.TempDir())
	if err != nil {
		t.Fatalf("documented deprecated aliases were rejected: %v", err)
	}
	if cfg.Runtime.ChildBudget.MaxElapsedSec != 19 || cfg.Runtime.ChildBudget.MaxTurnsPerAttempt != 7 {
		t.Fatalf("deprecated aliases were not migrated: %#v", cfg.Runtime.ChildBudget)
	}
}

func TestStrictConfigValidationPreservesYAMLMergeKeys(t *testing.T) {
	path := writeStrictConfigFixture(t, `
providers:
  defaults: &provider_defaults
    api_provider: openai-compatible
    api_key_env: CUSTOM_API_KEY
    base_url: http://localhost/v1
    model: default-model
  custom:
    <<: *provider_defaults
    model: custom-model
`)
	cfg, err := Load(path, t.TempDir())
	if err != nil {
		t.Fatalf("valid YAML merge was rejected: %v", err)
	}
	if cfg.Providers["custom"].Model != "custom-model" {
		t.Fatalf("merged provider was not decoded: %#v", cfg.Providers["custom"])
	}
}
