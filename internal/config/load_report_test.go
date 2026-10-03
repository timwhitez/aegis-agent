package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadReportSelectionAndObservedSources(t *testing.T) {
	for _, scenario := range []string{
		"defaults", "home", "workspace_skipped", "marker_not_authority", "trusted_workspace",
		"env_same_workspace", "env_other", "trusted_layers", "cli_over_env", "missing_cli",
		"missing_env", "home_equals_cwd", "untrusted_symlink", "explicit_symlink", "invalid_home",
	} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			home, cwd := filepath.Join(root, "home"), filepath.Join(root, "workspace")
			for _, dir := range []string{home, cwd} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("HOME", home)
			t.Setenv("AEGIS_AGENT_CONFIG", "")
			t.Setenv("AEGIS_AGENT_TRUST_WORKSPACE_CONFIG", "")
			homePath := filepath.Join(home, ".aegis-agent", "config.yaml")
			workspacePath := filepath.Join(cwd, ".aegis-agent", "config.yaml")
			envPath := filepath.Join(root, "operator.yaml")
			cliPath := filepath.Join(root, "explicit.yaml")
			write := func(path, body string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			modelConfig := func(model string) string {
				return "providers:\n  openai:\n    model: " + model + "\n    api_key_env: offline-secret-sentinel\n"
			}
			wantSources := []LoadSource{{Kind: "home", Path: homePath, Outcome: "missing"}, {Kind: "workspace", Path: workspacePath, Outcome: "skipped_untrusted"}}
			wantModel := Default().Providers["openai"].Model
			explicit, wantError := "", false
			switch scenario {
			case "home", "home_equals_cwd", "trusted_layers":
				write(homePath, modelConfig("home-model"))
				wantSources[0].Outcome, wantSources[0].Mode = "loaded", "-rw-------"
				wantModel = "home-model"
			case "invalid_home":
				write(homePath, "providers: [\n")
				t.Setenv("AEGIS_AGENT_CONFIG", envPath)
				write(envPath, modelConfig("must-not-load"))
				wantSources = []LoadSource{{Kind: "home", Path: homePath, Outcome: "parse_error", Mode: "-rw-------"}}
				wantError = true
			}
			switch scenario {
			case "workspace_skipped", "marker_not_authority", "trusted_workspace", "env_same_workspace", "trusted_layers":
				write(workspacePath, modelConfig("workspace-model"))
			}
			switch scenario {
			case "marker_not_authority":
				write(filepath.Join(cwd, ".aegis-agent", "trusted"), "trusted\n")
			case "trusted_workspace", "trusted_layers":
				t.Setenv("AEGIS_AGENT_TRUST_WORKSPACE_CONFIG", "true")
				wantSources[1].Outcome, wantSources[1].Mode = "loaded", "-rw-------"
				wantModel = "workspace-model"
			case "env_same_workspace":
				t.Setenv("AEGIS_AGENT_CONFIG", workspacePath)
				wantSources = append(wantSources, LoadSource{Kind: "env", Path: workspacePath, Outcome: "loaded", Mode: "-rw-------"})
				wantModel = "workspace-model"
			case "home_equals_cwd":
				cwd = home
				wantSources[1].Path = homePath
			case "untrusted_symlink", "explicit_symlink":
				write(envPath, modelConfig("symlink-model"))
				if err := os.MkdirAll(filepath.Dir(workspacePath), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(envPath, workspacePath); err != nil {
					t.Fatal(err)
				}
				if scenario == "explicit_symlink" {
					explicit, wantError = workspacePath, true
					wantSources = []LoadSource{{Kind: "cli", Path: workspacePath, Outcome: "read_error"}}
				}
			}
			if scenario == "env_other" || scenario == "trusted_layers" || scenario == "cli_over_env" || scenario == "missing_env" {
				t.Setenv("AEGIS_AGENT_CONFIG", envPath)
				if scenario != "missing_env" {
					write(envPath, modelConfig("env-model"))
					wantModel = "env-model"
					wantSources = append(wantSources, LoadSource{Kind: "env", Path: envPath, Outcome: "loaded", Mode: "-rw-------"})
				} else {
					wantSources = append(wantSources, LoadSource{Kind: "env", Path: envPath, Outcome: "missing"})
				}
			}
			if scenario == "cli_over_env" || scenario == "missing_cli" {
				explicit = cliPath
				wantSources = []LoadSource{{Kind: "cli", Path: cliPath, Outcome: "missing"}}
				if scenario == "cli_over_env" {
					write(cliPath, modelConfig("cli-model"))
					wantModel = "cli-model"
					wantSources[0].Outcome, wantSources[0].Mode = "loaded", "-rw-------"
				}
			}
			legacy, legacyErr := Load(explicit, cwd)
			got, report, err := LoadWithReport(explicit, cwd)
			if (err != nil) != wantError || (legacyErr != nil) != wantError {
				t.Fatalf("error mismatch: prototype=%v legacy=%v wantError=%v", err, legacyErr, wantError)
			}
			if err != nil && err.Error() != legacyErr.Error() {
				t.Fatalf("error changed: prototype=%v legacy=%v", err, legacyErr)
			}
			if !reflect.DeepEqual(got, legacy) {
				t.Fatal("prototype changed loaded or normalized configuration")
			}
			if report.Complete == wantError || !reflect.DeepEqual(report.Sources, wantSources) {
				t.Fatalf("report=%#v wantSources=%#v wantError=%v", report, wantSources, wantError)
			}
			if !wantError && got.Providers["openai"].Model != wantModel {
				t.Fatalf("effective model=%q want=%q", got.Providers["openai"].Model, wantModel)
			}
			if !wantError && got.Session.Dir != filepath.Join(cwd, ".aegis-agent", "sessions") {
				t.Fatalf("session directory was not normalized against cwd: %q", got.Session.Dir)
			}
			encoded, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "offline-secret-sentinel") || strings.Contains(string(encoded), wantModel) {
				t.Fatalf("report retained configuration values: %s", encoded)
			}
		})
	}
}

func TestLoadReportSnapshotIsImmutableAndNotSerialized(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  openai:\n    model: snapshot-model\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, returned, err := LoadWithReport(path, root)
	if err != nil {
		t.Fatal(err)
	}
	returned.Sources[0].Outcome = "missing"
	accessed := cfg.LoadReport()
	accessed.Sources[0].Path = "tampered"
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	snapshot := cfg.LoadReport()
	if !snapshot.Complete || len(snapshot.Sources) != 1 || snapshot.Sources[0].Outcome != "loaded" || snapshot.Sources[0].Path != path {
		t.Fatalf("load facts were mutated or reread: %#v", snapshot)
	}
	yamlBytes, err := MarshalYAML(cfg)
	if err != nil {
		t.Fatal(err)
	}
	jsonBytes, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{yamlBytes, jsonBytes} {
		if strings.Contains(string(data), path) || strings.Contains(string(data), "loadReport") || strings.Contains(string(data), "sources") {
			t.Fatalf("transient report leaked into serialized config: %s", data)
		}
	}
	if report := Default().LoadReport(); report.Complete || len(report.Sources) != 0 {
		t.Fatalf("directly constructed config fabricated a source report: %#v", report)
	}
}

func TestLoadReportPreservesLegacyRelativeAndWhitespaceSelectors(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "absent-home"))
	t.Setenv("AEGIS_AGENT_CONFIG", "")
	t.Setenv("AEGIS_AGENT_TRUST_WORKSPACE_CONFIG", "")
	oldCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	for _, selector := range []string{"relative.yaml", " spaced.yaml "} {
		if err := os.WriteFile(strings.TrimSpace(selector), []byte("providers:\n  openai:\n    model: raw-selector\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{"cli", "env"} {
			explicit := selector
			t.Setenv("AEGIS_AGENT_CONFIG", "")
			if kind == "env" {
				explicit = ""
				t.Setenv("AEGIS_AGENT_CONFIG", selector)
			}
			cwd := filepath.Join(root, "normalization-base")
			legacy, legacyErr := Load(explicit, cwd)
			got, report, err := LoadWithReport(explicit, cwd)
			if err != nil || legacyErr != nil || !reflect.DeepEqual(got, legacy) || got.Providers["openai"].Model != "raw-selector" {
				t.Fatalf("selector semantics changed: %q %s prototype=%v legacy=%v", selector, kind, err, legacyErr)
			}
			last := report.Sources[len(report.Sources)-1]
			if last.Kind != kind || last.Path != filepath.Clean(strings.TrimSpace(selector)) || last.Outcome != "loaded" {
				t.Fatalf("selector not preserved: %#v", last)
			}
		}
	}
}
