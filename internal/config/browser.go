package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	base := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err == nil && filepath.IsAbs(home) {
			base = filepath.Join(home, ".local", "share")
		} else {
			base = os.TempDir()
			if !filepath.IsAbs(base) {
				if abs, absErr := filepath.Abs(base); absErr == nil && filepath.IsAbs(abs) {
					base = abs
				} else {
					base = string(filepath.Separator) + "tmp"
				}
			}
		}
	}
	return filepath.Join(base, "aegis-agent", "browser-use")
}

func ValidateBrowserConfig(b BrowserConfig) error {
	trimmedRoot := strings.TrimSpace(b.InstallRoot)
	if b.Enabled {
		if !filepath.IsAbs(trimmedRoot) {
			return fmt.Errorf("tools.browser.install_root must be absolute (set tools.browser.install_root explicitly)")
		}
	} else if trimmedRoot != "" && !filepath.IsAbs(trimmedRoot) {
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
