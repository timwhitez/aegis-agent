package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"aegis-agent/internal/config"
	"aegis-agent/internal/fileutil"
	"aegis-agent/internal/procutil"
	"aegis-agent/internal/session"
)

//go:embed browser_launcher.py
var browserLauncher string

//go:embed browser_guidance.md
var browserGuidance string

type browserManager struct {
	mu       sync.Mutex
	sessions map[string]*browserSession
}
type browserSession struct {
	mu                  sync.Mutex
	execCtx             ExecContext
	root, ipc, endpoint string
	browser, daemon     *ownedBrowserProcess
	closed              bool
	cleanupErr          error
}
type ownedBrowserProcess struct {
	cmd       *exec.Cmd
	done      chan struct{}
	collector *commandOutputCollector
}

func (r *Registry) registerBrowser() {
	if !r.cfg.Tools.Browser.Enabled {
		return
	}
	r.browser = &browserManager{sessions: map[string]*browserSession{}}
	for _, name := range []string{"browser_exec", "browser_screenshot"} {
		def := Definition{Name: name, Ephemeral: true, EphemeralWindow: 2,
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}
		if name == "browser_exec" {
			def.Description = "Execute Python on stdin in the pinned local browser-use CLI. Mutating process/network tool; shell sandbox does not apply.\n\n" + strings.TrimSpace(browserGuidance)
			def.InputSchema["properties"] = map[string]any{"code": map[string]any{"type": "string", "minLength": 1}}
			def.InputSchema["required"] = []string{"code"}
		} else {
			def.Description = "Wait for the current document load, then capture the owned session browser viewport as a unique private PNG session artifact. False load condition is condition_not_met. Returns MIME, dimensions, SHA256 and complete/partial quota facts. Ref-only: image bytes are not visible to the model. Mutating process/network tool; uses the same execution permissions as browser_exec."
		}
		toolName := name
		def.Execute = func(ctx context.Context, ec ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			return r.browser.execute(ctx, ec, toolName, raw)
		}
		r.Register(def)
	}
}

// CloseBrowser releases only handles created by this registry. Never load or
// signal persisted PIDs on recovery: those are diagnostics, not kill authority.
func (r *Registry) CloseBrowser() error {
	if r == nil || r.browser == nil {
		return nil
	}
	r.browser.mu.Lock()
	defer r.browser.mu.Unlock()
	var errs []error
	for _, s := range r.browser.sessions {
		s.mu.Lock()
		errs = append(errs, s.close())
		s.mu.Unlock()
	}
	return errors.Join(errs...)
}

func (m *browserManager) session(ec ExecContext) (*browserSession, error) {
	if ec.Store == nil || ec.SessionID == "" {
		return nil, errors.New("browser requires canonical session context")
	}
	if _, err := ec.Store.LoadMetadata(ec.SessionID); err != nil {
		return nil, err
	}
	key := ec.Store.SessionDir(ec.SessionID)
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.sessions[key]; s != nil {
		return s, nil
	}
	s := &browserSession{execCtx: ec}
	m.sessions[key] = s
	return s, nil
}

func browserEnv(ec ExecContext, root, ipc, endpoint string) []string {
	// Use filteredEnv first, but never pass custom allowlisted credentials, proxy,
	// Python loaders or browser auth into this adapter. The shell policy is intact.
	env := []string{}
	for _, item := range filteredEnv(ec.Config.Runtime.ShellEnvAllowlist) {
		key, _, _ := strings.Cut(item, "=")
		if key == "LANG" || key == "TERM" || key == "LC_ALL" {
			env = append(env, item)
		}
	}
	values := map[string]string{
		"PATH": "/usr/bin:/bin", "HOME": filepath.Join(root, "home"),
		"AEGIS_BROWSER_INSTALL_ROOT": ec.Config.Tools.Browser.InstallRoot,
		"XDG_CONFIG_HOME":            filepath.Join(root, "config"), "XDG_CACHE_HOME": filepath.Join(root, "cache"),
		"XDG_DATA_HOME": filepath.Join(root, "data"), "XDG_RUNTIME_DIR": ipc,
		"TMPDIR": filepath.Join(ipc, "t"), "BH_HOME": filepath.Join(root, "bh-home"),
		"BH_CONFIG_DIR": filepath.Join(root, "config"), "BH_TMP_DIR": filepath.Join(root, "tmp"),
		"BH_RUNTIME_DIR": ipc, "BH_AGENT_WORKSPACE": filepath.Join(root, "workspace"),
		"BU_NAME": "aegis", "BU_CDP_WS": endpoint, "BH_REQUIRE_EXISTING_DAEMON": "1",
		"BH_TELEMETRY": "0", "ANONYMIZED_TELEMETRY": "false", "BH_UPDATE_CHECK": "0",
		"BH_RECORD": "0", "BH_OPEN_LIVE_URL": "0", "PYTHON_DOTENV_DISABLED": "1",
		"BROWSER_USE_SETUP_LOGGING": "false",
	}
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env
}

func prepareBrowserIPC(ipc string) error {
	// Linux sockaddr_un reserves one of its 108 bytes for the NUL terminator.
	// Chrome/Chromium create a six-character random directory below TMPDIR.
	for _, path := range []string{
		filepath.Join(ipc, "bu.sock"),
		filepath.Join(ipc, "t", "com.google.Chrome.XXXXXX", "SingletonSocket"),
		filepath.Join(ipc, "t", ".org.chromium.Chromium.XXXXXX", "SingletonSocket"),
	} {
		if len(path) > 107 {
			return fmt.Errorf("browser IPC socket path too long: %d bytes exceeds Linux limit 107; use a shorter TMPDIR parent (%s)", len(path), path)
		}
	}
	return fileutil.MkdirAllNoSymlink(filepath.Join(ipc, "t"), 0o700)
}

func browserCommand(ctx context.Context, ec ExecContext, root, ipc, endpoint, mode string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, filepath.Join(ec.Config.Tools.Browser.InstallRoot, "bin", "python"), "-I", "-c", browserLauncher, mode)
	procutil.PrepareCommandCancellation(cmd)
	cmd.Dir = filepath.Join(root, "workspace")
	cmd.Env = browserEnv(ec, root, ipc, endpoint)
	return cmd
}

func findBrowser(configured string) (string, error) {
	candidates := []string{configured}
	if configured == "" {
		candidates = []string{"/usr/bin/google-chrome", "/usr/bin/google-chrome-stable", "/opt/google/chrome/chrome", "/usr/bin/chromium", "/usr/bin/chromium-browser"}
	}
	for _, path := range candidates {
		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return path, nil
		}
	}
	return "", errors.New("browser_missing: install a supported Chrome-family browser explicitly; no browser is downloaded")
}

func startOwnedBrowserProcess(cmd *exec.Cmd, ec ExecContext, name string) (*ownedBrowserProcess, error) {
	p := &ownedBrowserProcess{cmd: cmd, done: make(chan struct{}), collector: newCommandOutputCollector(ec, name)}
	cmd.Stdout = p.collector
	cmd.Stderr = p.collector
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() { _ = cmd.Wait(); close(p.done) }()
	return p, nil
}
func (p *ownedBrowserProcess) stop() error {
	if p == nil {
		return nil
	}
	select {
	case <-p.done:
		// The leader already exited; never signal a potentially reused PID. Check
		// the owned group; surviving descendants mean cleanup is unknown.
	default:
		if err := p.cmd.Cancel(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		select {
		case <-p.done:
		case <-time.After(3 * time.Second):
			return errors.New("owned process cleanup unknown: wait did not settle")
		}
	}
	p.collector.finalize(commandOutputResultOptions{Summary: "[Owned browser process stopped]"})
	return browserProcessGroupCleanup(p.cmd.Process.Pid)
}

func (s *browserSession) initialize(ctx context.Context) error {
	ec := s.execCtx
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return errors.New("browser platform unsupported: Linux x86_64 only")
	}
	if err := config.ValidateBrowserConfig(ec.Config.Tools.Browser); err != nil {
		return err
	}
	install, err := fileutil.OpenDirNoSymlink(ec.Config.Tools.Browser.InstallRoot)
	if err != nil {
		return fmt.Errorf("missing_dependencies: private browser installation: %w", err)
	}
	install.Close()
	browser, err := findBrowser(ec.Config.Tools.Browser.BrowserExecutable)
	if err != nil {
		return err
	}
	parent := filepath.Join(ec.Store.SessionDir(ec.SessionID), "browser")
	if err := fileutil.MkdirAllNoSymlink(parent, 0o700); err != nil {
		return err
	}
	s.root, err = fileutil.MkdirTempNoSymlink(parent, "attempt-")
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(ec.Store.SessionDir(ec.SessionID)))
	s.ipc, err = fileutil.MkdirTempNoSymlink(os.TempDir(), "ab-"+hex.EncodeToString(digest[:4])+"-")
	if err != nil {
		return err
	}
	if err := prepareBrowserIPC(s.ipc); err != nil {
		return err
	}
	for _, dir := range []string{"home", "profile", "cache", "downloads", "config", "data", "tmp", "workspace", "bh-home"} {
		if err := fileutil.MkdirAllNoSymlink(filepath.Join(s.root, dir), 0o700); err != nil {
			return err
		}
	}
	// Check before any browser/daemon starts, using only stdlib and metadata.
	check := browserCommand(ctx, ec, s.root, s.ipc, "", "metadata")
	c := newCommandOutputCollector(ec, "browser_install_check")
	check.Stdout = c
	check.Stderr = c
	err = check.Run()
	checked := c.finalize(commandOutputResultOptions{Summary: "[Browser install check]", IsError: err != nil})
	if err != nil {
		return fmt.Errorf("browser install check: %s", checked.LLMOutput)
	}
	cmd := exec.CommandContext(context.Background(), browser, "--headless=new", "--user-data-dir="+filepath.Join(s.root, "profile"),
		"--disk-cache-dir="+filepath.Join(s.root, "cache"), "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0", "--no-first-run", "--no-default-browser-check",
		"--disable-background-networking", "--disable-component-update", "--disable-sync", "--disable-default-apps", "--disable-extensions", "--metrics-recording-only", "about:blank")
	procutil.PrepareCommandCancellation(cmd)
	cmd.Dir = filepath.Join(s.root, "workspace")
	cmd.Env = browserEnv(ec, s.root, s.ipc, "")
	// Chrome owns cookies/cache in the isolated profile. Configure download path
	// in this profile before startup; never write to the host's browser profile.
	prefs := []byte(fmt.Sprintf(`{"download":{"default_directory":%q,"prompt_for_download":false}}`, filepath.Join(s.root, "downloads")))
	if err := fileutil.MkdirAllNoSymlink(filepath.Join(s.root, "profile", "Default"), 0o700); err != nil {
		return err
	}
	if err := fileutil.AtomicWriteFileNoSymlink(filepath.Join(s.root, "profile", "Default", "Preferences"), prefs, 0o600); err != nil {
		return err
	}
	s.browser, err = startOwnedBrowserProcess(cmd, ec, "browser_startup")
	if err != nil {
		return err
	}
	if err := emitToolEvent(ec, "browser.process_started", map[string]any{"kind": "chrome", "pid": s.browser.cmd.Process.Pid, "profile": filepath.Join(s.root, "profile"), "ipc_dir": s.ipc}); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.browser.done:
			failure := s.browser.collector.finalize(commandOutputResultOptions{Summary: "[Owned Chrome startup failed; Chromium sandbox remains enabled]", IsError: true})
			return fmt.Errorf("browser_start_failed (sandbox/permission/dependencies): %s", failure.LLMOutput)
		default:
		}
		data, _, readErr := fileutil.ReadRegularFileNoSymlink(filepath.Join(s.root, "profile", "DevToolsActivePort"))
		if readErr == nil {
			// Chrome can publish the filename before both lines are written.
			// Poll readiness within the same startup deadline; never execute or
			// retry model code, and never attach to an incomplete endpoint.
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) == 2 {
				port, parseErr := strconv.Atoi(lines[0])
				if parseErr == nil && port > 0 && port <= 65535 && strings.HasPrefix(lines[1], "/devtools/browser/") {
					s.endpoint = fmt.Sprintf("ws://127.0.0.1:%d%s", port, lines[1])
					break
				}
			}
		}
		if readErr != nil && !os.IsNotExist(readErr) {
			return readErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	daemonCmd := browserCommand(context.Background(), ec, s.root, s.ipc, s.endpoint, "daemon")
	s.daemon, err = startOwnedBrowserProcess(daemonCmd, ec, "browser_daemon")
	if err != nil {
		return err
	}
	if err := emitToolEvent(ec, "browser.process_started", map[string]any{"kind": "daemon", "pid": s.daemon.cmd.Process.Pid, "profile": filepath.Join(s.root, "profile"), "ipc_dir": s.ipc}); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.daemon.done:
			failure := s.daemon.collector.finalize(commandOutputResultOptions{Summary: "[Owned daemon startup failed]", IsError: true})
			return fmt.Errorf("daemon_start_failed: %s", failure.LLMOutput)
		default:
		}
		if browserDaemonPing(ctx, s.ipc) == nil {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return emitToolEvent(ec, "browser.started", map[string]any{"browser_pid": s.browser.cmd.Process.Pid, "daemon_pid": s.daemon.cmd.Process.Pid, "profile": filepath.Join(s.root, "profile"), "ipc_dir": s.ipc, "mode": "local", "chromium_sandbox": "enabled", "host_sandbox": "off"})
}

func browserDaemonPing(ctx context.Context, ipc string) error {
	conn, err := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(ctx, "unix", filepath.Join(ipc, "bu.sock"))
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(200 * time.Millisecond))
	if _, err = io.WriteString(conn, "{\"meta\":\"ping\"}\n"); err != nil {
		return err
	}
	var reply struct {
		Pong bool `json:"pong"`
	}
	if err = json.NewDecoder(io.LimitReader(conn, 1024)).Decode(&reply); err != nil {
		return err
	}
	if !reply.Pong {
		return errors.New("owned daemon is not ready")
	}
	return nil
}

func (s *browserSession) close() error {
	if s.closed {
		return s.cleanupErr
	}
	s.closed = true
	err := errors.Join(s.daemon.stop(), s.browser.stop())
	status := "confirmed"
	if err != nil {
		status = "unknown"
	}
	if s.ipc != "" && err == nil {
		err = fileutil.RemoveDirAllNoSymlink(s.ipc)
		if err != nil {
			status = "unknown"
		}
	}
	if s.root != "" {
		data := map[string]any{"status": status, "effects": "not_rolled_back"}
		if err != nil {
			data["error"] = err.Error()
		}
		err = errors.Join(err, emitToolEvent(s.execCtx, "browser.cleanup", data))
	}
	s.cleanupErr = err
	return err
}

func (m *browserManager) execute(ctx context.Context, ec ExecContext, name string, raw json.RawMessage) (session.ToolResult, error) {
	var input struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return errorResult(name, err), nil
	}
	if name == "browser_exec" && strings.TrimSpace(input.Code) == "" {
		return errorResult(name, errors.New("code is required")), nil
	}
	timeout := effectiveToolTimeout(ec.Config.Runtime.CommandTimeoutSec, ec.Config.Tools.Browser.TimeoutSec)
	if timeout <= 0 {
		timeout = 300
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	s, err := m.session(ec)
	if err != nil {
		return errorResult(name, err), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	collector := newCommandOutputCollector(ec, name)
	md := map[string]any{"mode": "local", "host_sandbox": "off", "chromium_sandbox": "enabled", "timeout": timeout, "business_success": "not_evaluated", "exit_code": -1, "cleanup_scope": "owned_cli_daemon_browser_groups"}
	fail := func(cause error) (session.ToolResult, error) {
		class := FailureClassHarnessError
		propagate := error(nil)
		if ctx.Err() != nil {
			class = FailureClassInterrupted
			propagate = ctx.Err()
		} else if callCtx.Err() != nil {
			class = FailureClassTimeout
		}
		md[MetadataFailureClass] = class
		cleanupErr := s.close()
		if cleanupErr != nil {
			md["cleanup_error"] = cleanupErr.Error()
			md["cleanup"] = "unknown"
		} else {
			md["cleanup"] = "confirmed"
		}
		return collector.finalize(commandOutputResultOptions{Summary: "[Browser process failed; effects may be partial/unknown]", StatusMessage: cause.Error(), IsError: true, Metadata: md}), propagate
	}
	if err = callCtx.Err(); err != nil {
		return fail(err)
	}
	if s.closed {
		return fail(errors.New("owned browser attempt closed; no retry/replay; continue starts a fresh attempt"))
	}
	if s.root == "" {
		if err = s.initialize(callCtx); err != nil {
			return fail(err)
		}
	}
	if err = browserDaemonPing(callCtx, s.ipc); err != nil {
		return fail(fmt.Errorf("dead_daemon: owned daemon unavailable; no reconnect/replay: %w", err))
	}
	code := input.Code
	shot := ""
	if name == "browser_screenshot" {
		f, createErr := fileutil.CreateTempNoSymlink(filepath.Join(s.root, "tmp"), "screenshot-*.png")
		if createErr != nil {
			return fail(createErr)
		}
		shot = f.Name()
		if err = f.Close(); err != nil {
			return fail(err)
		}
		defer os.Remove(shot)
		quoted, _ := json.Marshal(shot)
		code = fmt.Sprintf("import json\ncondition_met = wait_for_load(timeout=%g)\nprint(json.dumps({'path': capture_screenshot(%s) if condition_met else None, 'condition_met': condition_met}))\n", min(15.0, float64(timeout)/2), string(quoted))
	}
	cmd := browserCommand(callCtx, ec, s.root, s.ipc, s.endpoint, "exec")
	cmd.Stdin = strings.NewReader(code)
	cmd.Stdout = collector
	cmd.Stderr = collector
	err = cmd.Run()
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
		md["signal"] = browserExitSignal(cmd.ProcessState)
	}
	md["exit_code"] = exitCode
	md["output_collection_complete"] = err == nil || (cmd.ProcessState != nil && exitCode != 0 && callCtx.Err() == nil)
	if err != nil {
		if ctx.Err() != nil || callCtx.Err() != nil {
			return fail(err)
		}
		md[MetadataFailureClass] = FailureClassCommandNonzero
		if exitCode == 0 || cmd.ProcessState == nil {
			md[MetadataFailureClass] = FailureClassHarnessError
		}
	}
	summary := fmt.Sprintf("[Browser process completed exit_code=%d signal=%v; business_success=not_evaluated]", exitCode, md["signal"])
	result := collector.finalize(commandOutputResultOptions{Summary: summary, IsError: err != nil, Metadata: md})
	if name == "browser_screenshot" && !result.IsError {
		return s.screenshot(ec, result, shot), nil
	}
	return result, nil
}

func (s *browserSession) screenshot(ec ExecContext, result session.ToolResult, path string) session.ToolResult {
	fail := func(err error) session.ToolResult {
		r := errorResult("browser_screenshot", err)
		r.Metadata = map[string]any{MetadataFailureClass: FailureClassHarnessError, "model_image_visible": false}
		return r
	}
	// Parse only the fixed wrapper response. Oversized/truncated JSON fails closed.
	raw := strings.TrimSpace(result.DisplayOutput)
	var response struct {
		Path         string `json:"path"`
		ConditionMet *bool  `json:"condition_met"`
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&response); err != nil {
		return fail(fmt.Errorf("invalid screenshot JSON: %w", err))
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fail(errors.New("invalid screenshot JSON trailing data"))
	}
	if response.ConditionMet == nil {
		return fail(errors.New("invalid screenshot condition result"))
	}
	if !*response.ConditionMet {
		r := fail(errors.New("condition_not_met: typed screenshot condition was not satisfied; effects may have been sent"))
		r.Metadata[MetadataFailureClass] = "condition_not_met"
		return r
	}
	if response.Path != path {
		return fail(errors.New("screenshot response path mismatch"))
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fail(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return fail(errors.New("screenshot must be a private regular PNG"))
	}
	policy := effectiveCommandToolOutputPolicy(ec.Config)
	// Bound source read/decode independently of compressed artifact quota.
	if info.Size() > int64(fileutil.MaxRegularFileReadBytes) {
		return fail(errors.New("screenshot source exceeds hard byte cap"))
	}
	data, _, err := fileutil.ReadRegularFileNoSymlink(path)
	if err != nil {
		return fail(err)
	}
	if http.DetectContentType(data) != "image/png" {
		return fail(errors.New("invalid screenshot MIME: expected image/png"))
	}
	dims, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return fail(err)
	}
	if dims.Width < 1 || dims.Height < 1 || int64(dims.Width)*int64(dims.Height) > 32*1024*1024 {
		return fail(errors.New("invalid screenshot dimensions"))
	}
	if _, err = png.Decode(bytes.NewReader(data)); err != nil {
		return fail(fmt.Errorf("invalid PNG: %w", err))
	}
	hash := sha256.Sum256(data)
	key := sha256.Sum256([]byte(path))
	artifact, err := ec.Store.WriteToolOutputPNGArtifact(ec.SessionID, ec.EphemeralArtifactRoot, "browser_screenshot-"+hex.EncodeToString(key[:12]), data, session.ToolOutputArtifactQuota{FileMaxBytes: policy.ArtifactFileMaxBytes, SessionMaxBytes: policy.ArtifactSessionMaxBytes, MaxFiles: policy.ArtifactMaxFiles})
	if err != nil {
		return fail(fmt.Errorf("screenshot artifact persist failed: %w", err))
	}
	md := map[string]any{"mime": "image/png", "width": dims.Width, "height": dims.Height, "sha256": hex.EncodeToString(hash[:]), "image_delivery": "ref-only", "model_image_visible": false,
		"screenshot_raw_bytes": len(data), "screenshot_persisted_bytes": artifact.PersistedBytes, "screenshot_omitted_bytes": artifact.OmittedBytes,
		"screenshot_complete": artifact.Complete, "screenshot_truncated": artifact.Truncated, "screenshot_recoverable": artifact.Recoverable, "screenshot_budget_reason": artifact.Reason,
		"screenshot_artifact_path": commandArtifactDisplayPath(ec, artifact.AbsolutePath), "exit_code": 0, "business_success": "not_evaluated"}
	if artifact.PersistedBytes > 0 {
		persistedHash := sha256.Sum256(data[:artifact.PersistedBytes])
		md["screenshot_persisted_sha256"] = hex.EncodeToString(persistedHash[:])
	}
	if !artifact.Complete {
		md[MetadataFailureClass] = FailureClassHarnessError
	}
	output, _ := json.Marshal(md)
	return session.ToolResult{Name: "browser_screenshot", LLMOutput: string(output), DisplayOutput: string(output), Metadata: md, IsError: !artifact.Complete}
}
