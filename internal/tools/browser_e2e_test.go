package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aegis-agent/internal/config"
	"aegis-agent/internal/fileutil"
	"aegis-agent/internal/session"
	"golang.org/x/sys/unix"
)

func TestBrowserRealRuntime(t *testing.T) {
	if os.Getenv("AEGIS_BROWSER_E2E") != "1" {
		t.Skip("set AEGIS_BROWSER_E2E=1 for private installed localhost acceptance")
	}
	install := filepath.Clean(os.Getenv("AEGIS_BROWSER_INSTALL_ROOT"))
	if !filepath.IsAbs(install) || !strings.HasPrefix(install, filepath.Clean(os.TempDir())+string(os.PathSeparator)) {
		t.Fatal("use a PRIVATE test install under os.TempDir")
	}
	dir, err := fileutil.OpenDirNoSymlink(install)
	if err != nil {
		t.Fatal("test install must be a private owned directory without symlinks", err)
	}
	var stat unix.Stat_t
	err = unix.Fstat(int(dir.Fd()), &stat)
	dir.Close()
	if err != nil || int(stat.Uid) != os.Geteuid() || stat.Mode&0077 != 0 {
		t.Fatal("test install must be private and owned by the current test uid", err)
	}
	marker, _, err := fileutil.ReadRegularFileNoSymlink(filepath.Join(install, ".aegis-browser-install"))
	if err != nil || string(marker) != "aegis-agent-browser-use-v1\n" {
		t.Fatal("test install is not marked owned", err)
	}
	cfg := config.Default()
	cfg.Tools.Browser = config.BrowserConfig{Enabled: true, InstallRoot: install, BrowserExecutable: "/usr/bin/google-chrome", TimeoutSec: 30}
	temp := t.TempDir()
	cfg.Session.Dir = filepath.Join(temp, "sessions")
	cfg.Runtime.CommandTimeoutSec = 30
	root := filepath.Join(temp, "check")
	for _, d := range []string{"workspace", "tmp", "home", "config", "cache", "data", "bh-home"} {
		if err := fileutil.MkdirAllNoSymlink(filepath.Join(root, d), 0700); err != nil {
			t.Fatal(err)
		}
	}
	ipc, err := fileutil.MkdirTempNoSymlink(os.TempDir(), "ab-check-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fileutil.RemoveDirAllNoSymlink(ipc); err != nil {
			t.Error("check IPC cleanup UNKNOWN:", err)
		}
	})
	if err := prepareBrowserIPC(ipc); err != nil {
		t.Fatal(err)
	}
	ec := ExecContext{Config: cfg}
	run := func(script string, mode string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := browserCommand(ctx, ec, root, ipc, "", mode)
		cmd.Args[3] = script
		collector := newCommandOutputCollector(ec, "browser_e2e_check")
		cmd.Stdout = collector
		cmd.Stderr = collector
		err := cmd.Run()
		r := collector.finalize(commandOutputResultOptions{IsError: err != nil})
		return r.DisplayOutput, err
	}
	metadata, err := run(browserLauncher, "metadata")
	if err != nil {
		t.Fatal(metadata, err)
	}
	var installed map[string]any
	if err := json.Unmarshal([]byte(metadata), &installed); err != nil {
		t.Fatal(err)
	}
	t.Logf("VERIFIED pinned installed metadata: %s", metadata)
	repo := installed["repo_root"].(string)
	for _, base := range []string{repo, filepath.Join(root, "workspace")} {
		for _, name := range []string{".env", "agent_helpers.py"} {
			path := filepath.Join(base, name)
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				t.Fatal("refuse to overwrite any non-test file", err)
			}
			f.WriteString("BROWSER_USE_API_KEY=SYNTHETIC_BROWSER_136\nBU_AUTOSPAWN=0\nOPENAI_API_KEY=SYNTHETIC_PROVIDER_136\n")
			f.Close()
			out, err := run(browserLauncher, "metadata")
			os.Remove(path)
			if err == nil || !strings.Contains(out, "unexpected_install_file") {
				t.Fatal(path, out, err)
			}
		}
	}
	t.Log("VERIFIED pre-import .env/agent_helpers negative controls in installed REPO_ROOT and BH_AGENT_WORKSPACE")
	ancestor := filepath.Join(filepath.Dir(install), ".env")
	f, err := os.OpenFile(ancestor, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal("refuse to overwrite ancestor env", err)
	}
	f.WriteString("BROWSER_USE_API_KEY=SYNTHETIC_BROWSER_136\nBU_AUTOSPAWN=0\nOPENAI_API_KEY=SYNTHETIC_PROVIDER_136\nHTTPS_PROXY=SYNTHETIC_PROXY_136\n")
	f.Close()
	defer os.Remove(ancestor)
	// Test-only outbound mock, before official package import. No production
	// Python interception. Audit only attempted external connections here.
	importCheck := browserLauncher + `
import socket
outbound = []
_original_connect = socket.socket.connect
def checked_connect(self, address):
    if isinstance(address, tuple) and address[0] not in ('127.0.0.1', 'localhost', '::1'):
        outbound.append(str(address))
        raise RuntimeError('external connection blocked by acceptance fixture')
    return _original_connect(self, address)
socket.socket.connect = checked_connect
import browser_use
from browser_use.config import Config
Config()
from browser_harness import telemetry
telemetry.capture_cli_event(action='completed', command='script', task='SYNTHETIC_RAW_PAYLOAD', output='SYNTHETIC_STDOUT')
assert not outbound, outbound
for key in ['BROWSER_USE_API_KEY','BU_AUTOSPAWN','OPENAI_API_KEY','HTTPS_PROXY']:
    assert key not in os.environ, (key, os.environ.get(key))
print('OFFICIAL_IMPORT_CLEAN_NO_OUTBOUND')
`
	out, err := run(importCheck, "metadata")
	if err != nil || !strings.Contains(out, "OFFICIAL_IMPORT_CLEAN_NO_OUTBOUND") {
		t.Fatal(out, err)
	}
	os.Remove(ancestor)
	out, err = run(importCheck, "metadata")
	if err != nil || !strings.Contains(out, "OFFICIAL_IMPORT_CLEAN_NO_OUTBOUND") {
		t.Fatal(out, err)
	}
	t.Log("VERIFIED installed official package import with synthetic ancestor .env, Config clean cwd, telemetry outbound mock and clean positive control")
	// Strict dead doctor is the real pinned harness, not a reimplementation.
	out, err = run(browserLauncher, "doctor")
	var dead map[string]any
	if json.Unmarshal([]byte(out), &dead) != nil || err == nil || dead["healthy"] != false || dead["chrome_running"] != nil || dead["require_existing_daemon"] != true {
		t.Fatal(out, err)
	}
	t.Log("VERIFIED real strict doctor dead named daemon, chrome_running=null, no start")
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<!doctype html><title>Aegis synthetic fixture</title><input id="input"><p id="ready">ready</p>`)
	}))
	defer fixture.Close()
	store := session.NewStore(cfg.Session.Dir)
	id := session.NewSessionID()
	meta := session.SessionMetadata{SchemaVersion: 1, ID: id, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: temp, Mode: session.ModeExec, Provider: "fake", Model: "fake", CompletionPolicy: session.CompletionPolicyAutonomous}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := registry.CloseBrowser(); err != nil {
			t.Log("cleanup UNKNOWN:", err)
		} else {
			t.Log("cleanup VERIFIED: owned handles settled")
		}
	}()
	ec = ExecContext{Config: cfg, Store: store, SessionID: id, ToolCallID: "live", Workdir: temp, EphemeralArtifactRoot: filepath.Join(store.SessionDir(id), "artifacts", "tool-outputs")}
	code := fmt.Sprintf("goto_url(%q)\nassert wait_for_element('#ready', timeout=2)\nprint(page_info())\njs(\"document.getElementById('input').focus()\")\ntype_text('owned input')\nassert js(\"document.getElementById('input').value\") == 'owned input'\nprint(cdp('Browser.getVersion'))\nassert wait_for_element('#missing', timeout=0.1) is False\nprint('FIXTURE_VERIFIED')\n", fixture.URL)
	raw, _ := json.Marshal(map[string]string{"code": code})
	result, err := registry.Execute(context.Background(), "browser_exec", ec, raw)
	if result.IsError {
		if strings.Contains(result.LLMOutput, "sandbox") && os.Geteuid() == 0 {
			t.Logf("VERIFIED fail-closed sandboxed Chrome startup on root host: %s", result.LLMOutput)
			t.Skip("NOT_VERIFIED live navigate/input/JS/CDP/wait/screenshot/strict-live doctor: host runs as root and Chrome refuses sandboxed startup")
		}
		t.Fatal(result, err)
	}
	if err != nil || !strings.Contains(result.LLMOutput, "FIXTURE_VERIFIED") {
		t.Fatal(result, err)
	}
	t.Log("VERIFIED localhost navigate/observe/input/JS/CDP/wait positive and negative controls")
	shot, err := registry.Execute(context.Background(), "browser_screenshot", ec, json.RawMessage(`{}`))
	if err != nil || shot.IsError || shot.Metadata["screenshot_complete"] != true {
		t.Fatal(shot, err)
	}
	s := registry.browser.sessions[store.SessionDir(id)]
	live := browserCommand(context.Background(), ec, s.root, s.ipc, s.endpoint, "doctor")
	var buf strings.Builder
	live.Stdout = &buf
	live.Stderr = &buf
	if err := live.Run(); err != nil {
		t.Fatal(buf.String(), err)
	}
	var liveReport map[string]any
	if json.Unmarshal([]byte(buf.String()), &liveReport) != nil || liveReport["healthy"] != true || liveReport["chrome_running"] != nil {
		t.Fatal(buf.String())
	}
	t.Log("VERIFIED real private PNG screenshot and pinned strict live named daemon doctor")
}
