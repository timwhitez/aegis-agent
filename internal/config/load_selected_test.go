package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"aegis-agent/internal/fileutil"
)

func TestMissingExplicitSelectionStopsLayerLoading(t *testing.T) {
	for _, kind := range []string{"cli", "cli_leaf", "env", "env_leaf", "env_after_home", "env_after_trusted_workspace"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			home, cwd := filepath.Join(root, "home"), filepath.Join(root, "workspace")
			for _, dir := range []string{home, cwd} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("HOME", home)
			t.Setenv("AEGIS_AGENT_TRUST_WORKSPACE_CONFIG", "")
			t.Setenv("AEGIS_AGENT_CONFIG", "")
			selected := filepath.Join(root, "missing-parent", "selected.yaml")
			if strings.HasSuffix(kind, "_leaf") {
				selected = filepath.Join(root, "missing-leaf.yaml")
			}
			explicit, wantKind := "", "env"
			if strings.HasPrefix(kind, "cli") {
				explicit, wantKind = selected, "cli"
			} else {
				t.Setenv("AEGIS_AGENT_CONFIG", selected)
			}
			homePath, workspacePath := filepath.Join(home, ".aegis-agent/config.yaml"), filepath.Join(cwd, ".aegis-agent/config.yaml")
			wantSources := []LoadSource{{Kind: "home", Path: homePath, Outcome: "missing"}, {Kind: "workspace", Path: workspacePath, Outcome: "skipped_untrusted"}}
			if wantKind == "cli" {
				wantSources = nil
			}
			if kind == "env_after_home" || kind == "env_after_trusted_workspace" {
				path := filepath.Join(home, ".aegis-agent/config.yaml")
				if kind == "env_after_trusted_workspace" {
					path = filepath.Join(cwd, ".aegis-agent/config.yaml")
					t.Setenv("AEGIS_AGENT_TRUST_WORKSPACE_CONFIG", "true")
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("providers:\n  openai:\n    model: earlier-layer\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				index := 0
				if kind == "env_after_trusted_workspace" {
					index = 1
				}
				wantSources[index].Outcome, wantSources[index].Mode = "loaded", "-rw-------"
			}
			wantSources = append(wantSources, LoadSource{Kind: wantKind, Path: selected, Outcome: "missing"})
			_, _, readerErr := fileutil.ReadRegularFileNoSymlink(selected)
			cfg, report, err := LoadWithReport(explicit, cwd)
			if cfg != nil || !errors.Is(err, os.ErrNotExist) || readerErr == nil || err.Error() != readerErr.Error() {
				t.Fatalf("selected missing file returned fallback/changed safe-reader error: cfg=%v err=%v reader=%v", cfg, err, readerErr)
			}
			if report.Complete || !reflect.DeepEqual(report.Sources, wantSources) {
				t.Fatalf("visited source facts: report=%#v want=%#v", report, wantSources)
			}
			var gotMissing, readerMissing *os.PathError
			if !errors.As(err, &gotMissing) || !errors.As(readerErr, &readerMissing) || gotMissing.Op != readerMissing.Op || gotMissing.Path != readerMissing.Path || !errors.Is(gotMissing.Err, readerMissing.Err) {
				t.Fatalf("lost typed missing error chain: got=%#v reader=%#v", gotMissing, readerMissing)
			}
		})
	}
}

func TestMissingImplicitLayersAndCLIPrecedenceRemainUsable(t *testing.T) {
	root := t.TempDir()
	home, cwd := filepath.Join(root, "home"), filepath.Join(root, "workspace")
	for _, dir := range []string{home, cwd} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("AEGIS_AGENT_TRUST_WORKSPACE_CONFIG", "true")
	t.Setenv("AEGIS_AGENT_CONFIG", "")
	cfg, report, err := LoadWithReport("", cwd)
	if err != nil || cfg == nil || !report.Complete || len(report.Sources) != 2 {
		t.Fatalf("implicit missing default: cfg=%v report=%#v err=%v", cfg, report, err)
	}
	want := []LoadSource{{Kind: "home", Path: filepath.Join(home, ".aegis-agent/config.yaml"), Outcome: "missing"}, {Kind: "workspace", Path: filepath.Join(cwd, ".aegis-agent/config.yaml"), Outcome: "missing"}}
	if !reflect.DeepEqual(report.Sources, want) || cfg.DefaultProvider != Default().DefaultProvider || cfg.Providers["openai"].Model != Default().Providers["openai"].Model {
		t.Fatalf("implicit builtin facts: cfg=%v report=%#v", cfg, report)
	}
	selected := filepath.Join(root, "operator.yaml")
	if err := os.WriteFile(selected, []byte("providers:\n  openai:\n    model: selected-cli\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AEGIS_AGENT_CONFIG", filepath.Join(root, "missing-env.yaml"))
	cfg, report, err = LoadWithReport(selected, cwd)
	if err != nil || cfg == nil || cfg.Providers["openai"].Model != "selected-cli" || !report.Complete || len(report.Sources) != 1 || report.Sources[0].Kind != "cli" {
		t.Fatalf("valid CLI did not supersede missing env: cfg=%v report=%#v err=%v", cfg, report, err)
	}
	if !reflect.DeepEqual(report.Sources, []LoadSource{{Kind: "cli", Path: selected, Outcome: "loaded", Mode: "-rw-------"}}) {
		t.Fatalf("CLI source facts: %#v", report)
	}
}
