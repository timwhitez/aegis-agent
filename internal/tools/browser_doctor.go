package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"aegis-agent/internal/config"
	"aegis-agent/internal/fileutil"
	"aegis-agent/internal/procutil"
	"aegis-agent/internal/session"
)

// BrowserSettings is read-only operator information, with no credentials/CDP.
func BrowserSettings(cfg *config.Config) map[string]any {
	return map[string]any{"enabled": cfg.Tools.Browser.Enabled, "mode": "local", "install_root": cfg.Tools.Browser.InstallRoot,
		"browser_executable": cfg.Tools.Browser.BrowserExecutable, "timeout_sec": cfg.Tools.Browser.TimeoutSec,
		"host_sandbox": "off: Python has host filesystem/process/network permissions", "chromium_sandbox": "enabled", "image_delivery": "ref-only"}
}

// BrowserDoctor uses only the pinned strict machine doctor for a selected
// session. An omitted session never starts/discovers a daemon or a browser.
func BrowserDoctor(ctx context.Context, cfg *config.Config, store *session.Store, id string) (map[string]any, error) {
	report := BrowserSettings(cfg)
	if !cfg.Tools.Browser.Enabled {
		report["status"] = "disabled"
		return report, nil
	}
	fail := func(status string, err error) (map[string]any, error) { report["status"] = status; return report, err }
	if err := config.ValidateBrowserConfig(cfg.Tools.Browser); err != nil {
		return fail("invalid_config", err)
	}
	install, err := fileutil.OpenDirNoSymlink(cfg.Tools.Browser.InstallRoot)
	if err != nil {
		if os.IsPermission(err) {
			return fail("permission_pending", err)
		}
		return fail("missing_dependencies", err)
	}
	install.Close()
	temp, err := fileutil.MkdirTempNoSymlink(os.TempDir(), "ab-doctor-")
	if err != nil {
		return fail("permission_pending", err)
	}
	defer fileutil.RemoveDirAllNoSymlink(temp)
	for _, dir := range []string{"workspace", "home", "tmp", "config", "cache", "data", "bh-home", "ipc"} {
		if err := fileutil.MkdirAllNoSymlink(filepath.Join(temp, dir), 0o700); err != nil {
			return fail("permission_pending", err)
		}
	}
	if err := prepareBrowserIPC(filepath.Join(temp, "ipc")); err != nil {
		return fail("invalid_ipc_path", err)
	}
	ec := ExecContext{Config: cfg}
	runJSON := func(root, ipc, mode string) (map[string]any, error) {
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		cmd := browserCommand(callCtx, ec, root, ipc, "", mode)
		collector := newCommandOutputCollector(ec, "browser_doctor")
		cmd.Stdout = collector
		cmd.Stderr = collector
		runErr := cmd.Run()
		result := collector.finalize(commandOutputResultOptions{Summary: "[Browser doctor]", IsError: runErr != nil})
		var data map[string]any
		if err := json.Unmarshal([]byte(result.DisplayOutput), &data); err != nil {
			return nil, errors.Join(runErr, fmt.Errorf("browser check failed: %s", result.LLMOutput))
		}
		return data, runErr
	}
	metadata, err := runJSON(temp, filepath.Join(temp, "ipc"), "metadata")
	if err != nil {
		status := "missing_dependencies"
		if os.IsPermission(err) {
			status = "permission_pending"
		}
		if strings.Contains(err.Error(), "wrong_version") || strings.Contains(err.Error(), "wrong_python") {
			status = "wrong_version"
		}
		if strings.Contains(err.Error(), "unexpected_install_file") {
			status = "unexpected_install_file"
		}
		return fail(status, err)
	}
	report["installed"] = metadata
	if inventory, _, readErr := fileutil.ReadRegularFileNoSymlink(filepath.Join(cfg.Tools.Browser.InstallRoot, "install-metadata.json")); readErr == nil {
		var data map[string]any
		if json.Unmarshal(inventory, &data) == nil {
			report["install_metadata"] = data
		}
	}
	browser, err := findBrowser(cfg.Tools.Browser.BrowserExecutable)
	if err != nil {
		return fail("browser_missing", err)
	}
	report["browser_executable"] = browser
	report["browser_found"] = true
	versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	versionCmd := exec.CommandContext(versionCtx, browser, "--version")
	procutil.PrepareCommandCancellation(versionCmd)
	versionCmd.Env = browserEnv(ec, temp, filepath.Join(temp, "ipc"), "")
	versionCmd.Dir = filepath.Join(temp, "workspace")
	collector := newCommandOutputCollector(ec, "browser_version")
	versionCmd.Stdout = collector
	versionCmd.Stderr = collector
	err = versionCmd.Run()
	version := collector.finalize(commandOutputResultOptions{IsError: err != nil})
	report["browser_build"] = version.DisplayOutput
	if err != nil {
		return fail("browser_missing_dependencies_or_permission", err)
	}
	if id == "" {
		report["status"] = "installed_no_session_selected"
		return report, nil
	}
	if store == nil {
		return fail("missing_session", errors.New("session store required"))
	}
	events, err := store.LoadEvents(id)
	if err != nil {
		return fail("missing_session", err)
	}
	var root, ipc string
	for _, event := range events {
		if event.Type == "browser.started" {
			profile, _ := event.Data["profile"].(string)
			root = filepath.Dir(profile)
			ipc, _ = event.Data["ipc_dir"].(string)
		}
	}
	if root == "" || ipc == "" {
		return fail("dead_daemon", errors.New("no owned named daemon recorded for selected session"))
	}
	// Facts from events can never redirect doctor into user/browser roots.
	canonical := filepath.Join(store.SessionDir(id), "browser")
	if !isWithin(canonical, root) || filepath.Dir(ipc) != filepath.Clean(os.TempDir()) || len(filepath.Base(ipc)) < 3 || filepath.Base(ipc)[:3] != "ab-" {
		return fail("unsafe_session_paths", errors.New("invalid owned browser paths"))
	}
	for _, dir := range []string{root, ipc, filepath.Join(root, "workspace")} {
		f, err := fileutil.OpenDirNoSymlink(dir)
		if err != nil {
			return fail("dead_daemon", err)
		}
		f.Close()
	}
	data, err := runJSON(root, ipc, "doctor")
	report["strict_doctor"] = data
	if data != nil {
		if data["chrome_running"] != nil || data["require_existing_daemon"] != true || data["version"] != "0.1.13" {
			return fail("wrong_doctor_contract", errors.New("invalid pinned strict doctor contract"))
		}
		if data["healthy"] != true {
			return fail("dead_daemon", errors.New("selected named daemon or live CDP is unavailable"))
		}
	}
	if err != nil {
		return fail("dead_daemon_or_permission_pending", err)
	}
	report["status"] = "healthy_owned_daemon"
	return report, nil
}
