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
