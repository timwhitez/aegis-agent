package config

import (
	"fmt"
	"os"
	"path/filepath"
)

type ToolsConfig struct {
	Browser BrowserConfig `yaml:"browser,omitempty"`
}
type BrowserConfig struct {
	Enabled           bool   `yaml:"enabled" json:"enabled"`
	InstallRoot       string `yaml:"install_root" json:"install_root"`
	BrowserExecutable string `yaml:"browser_executable,omitempty" json:"browser_executable,omitempty"`
	TimeoutSec        int    `yaml:"timeout_sec,omitempty" json:"timeout_sec,omitempty"`
}

func DefaultBrowserInstallRoot() string {
	base := os.Getenv("XDG_DATA_HOME")
	if !filepath.IsAbs(base) {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "aegis-agent", "browser-use")
}
func ValidateBrowserConfig(b BrowserConfig) error {
	if !filepath.IsAbs(b.InstallRoot) {
		return fmt.Errorf("tools.browser.install_root must be absolute")
	}
	if b.BrowserExecutable != "" && !filepath.IsAbs(b.BrowserExecutable) {
		return fmt.Errorf("tools.browser.browser_executable must be absolute")
	}
	if b.TimeoutSec < 0 {
		return fmt.Errorf("tools.browser.timeout_sec must be nonnegative")
	}
	return nil
}
