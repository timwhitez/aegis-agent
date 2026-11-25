package config

import (
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"testing"
)

func TestBrowserConfigCanonicalRoundTripAndValidation(t *testing.T) {
	cfg := Default()
	cfg.Tools.Browser = BrowserConfig{Enabled: true, InstallRoot: filepath.Join(t.TempDir(), "private"), BrowserExecutable: "/usr/bin/google-chrome", TimeoutSec: 12}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, raw, 0600)
	loaded, err := Load(path, t.TempDir())
	if err != nil || loaded.Tools.Browser != cfg.Tools.Browser {
		t.Fatal(loaded, err)
	}
	for _, body := range []string{"tools:\n browser:\n  install_root: relative\n", "tools:\n browser:\n  timeout_sec: -1\n", "tools:\n browser:\n  browser_executable: chrome\n", "tools:\n browser:\n  enable: true\n"} {
		os.WriteFile(path, []byte(body), 0600)
		if _, err := Load(path, t.TempDir()); err == nil {
			t.Fatal(body)
		}
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if !filepath.IsAbs(DefaultBrowserInstallRoot()) {
		t.Fatal("non-absolute default")
	}
}

func TestBrowserConfigAllowsDisabledWithoutHomeOrInstallRoot(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_DATA_HOME", "")

	if root := DefaultBrowserInstallRoot(); !filepath.IsAbs(root) {
		t.Fatalf("expected absolute default browser install root without HOME, got %q", root)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("schema_version: 1\ntools:\n  browser:\n    enabled: false\n    install_root: \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path, dir)
	if err != nil {
		t.Fatalf("disabled browser without HOME/install_root should load cleanly: %v", err)
	}
	if loaded.Tools.Browser.Enabled {
		t.Fatal("expected browser to remain disabled")
	}

	if err := ValidateBrowserConfig(BrowserConfig{Enabled: true, InstallRoot: ""}); err == nil {
		t.Fatal("enabled browser with empty install_root must fail validation")
	}
}
