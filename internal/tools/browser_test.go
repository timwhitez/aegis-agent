package tools

import (
	"aegis-agent/internal/config"
	"aegis-agent/internal/events"
	"aegis-agent/internal/fileutil"
	"aegis-agent/internal/session"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBrowserRegistryAssembly(t *testing.T) {
	cfg := config.Default()
	if err := yaml.Unmarshal([]byte("tools:\n  browser:\n    enabled: true\n"), cfg); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(cfg, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"browser_exec", "browser_screenshot"} {
		if registry.Get(name) == nil {
			t.Fatalf("real registry assembly lacks %s dispatch", name)
		}
	}
}

func browserFixture(t *testing.T) (*Registry, ExecContext) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("Linux browser adapter")
	}
	cfg := config.Default()
	cfg.Tools.Browser.Enabled = true
	cfg.Tools.Browser.InstallRoot = t.TempDir()
	cfg.Runtime.CommandTimeoutSec = 5
	fake, err := os.ReadFile("testdata/browser_fake.py")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(cfg.Tools.Browser.InstallRoot, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(cfg.Tools.Browser.InstallRoot, "bin", "python"), filepath.Join(cfg.Tools.Browser.InstallRoot, "chrome")} {
		if err := os.WriteFile(path, fake, 0700); err != nil {
			t.Fatal(err)
		}
	}
	cfg.Tools.Browser.BrowserExecutable = filepath.Join(cfg.Tools.Browser.InstallRoot, "chrome")
	store := session.NewStore(t.TempDir())
	id := "browser-test"
	meta := session.SessionMetadata{SchemaVersion: 1, CompletionPolicy: session.CompletionPolicyAutonomous, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Provider: "openai", Model: "fake", ID: id, Workdir: t.TempDir(), Mode: session.ModeExec}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	ec := ExecContext{SessionID: id, ToolCallID: "one", Workdir: meta.Workdir, Store: store, Config: cfg, EphemeralArtifactRoot: filepath.Join(store.SessionDir(id), "artifacts", "tool-outputs")}
	reg, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reg.CloseBrowser(); err != nil {
			t.Error(err)
		}
	})
	return reg, ec
}
func browserRun(t *testing.T, r *Registry, ec ExecContext, code string) session.ToolResult {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"code": code})
	result, err := r.Execute(context.Background(), "browser_exec", ec, raw)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestBrowserShortSocketTempWithLongSessionRoot(t *testing.T) {
	r, ec := browserFixture(t)
	meta, err := ec.Store.LoadMetadata(ec.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	ec.Store = session.NewStore(filepath.Join(t.TempDir(), strings.Repeat("workspace-", 15), ".aegis-agent", "sessions"))
	if err := ec.Store.Create(meta, session.State{Status: session.StatusRunning}); err != nil {
		t.Fatal(err)
	}
	ec.EphemeralArtifactRoot = filepath.Join(ec.Store.SessionDir(ec.SessionID), "artifacts", "tool-outputs")
	if result := browserRun(t, r, ec, "print('ready')"); result.IsError {
		t.Fatal(result)
	}
	s := r.browser.sessions[ec.Store.SessionDir(ec.SessionID)]
	for _, process := range []*ownedBrowserProcess{s.browser, s.daemon} {
		found := false
		for _, variable := range process.cmd.Env {
			if value, ok := strings.CutPrefix(variable, "TMPDIR="); ok {
				found = true
				if value != filepath.Join(s.ipc, "t") || len(filepath.Join(value, "com.google.Chrome.XXXXXX", "SingletonSocket")) > 107 {
					t.Fatalf("socket TMPDIR is not short and private: %q", value)
				}
			}
		}
		if !found {
			t.Fatal("socket TMPDIR is missing")
		}
	}
	info, err := os.Stat(filepath.Join(s.ipc, "t"))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("socket temp permissions: %v %v", info, err)
	}
	for _, flag := range []string{"--user-data-dir=" + filepath.Join(s.root, "profile"), "--disk-cache-dir=" + filepath.Join(s.root, "cache")} {
		if !slices.Contains(s.browser.cmd.Args, flag) {
			t.Fatalf("session browser path missing: %s", flag)
		}
	}
	// The fake screenshot helper asserts its PNG path is in session BH_TMP_DIR.
	shot, err := r.Execute(context.Background(), "browser_screenshot", ec, json.RawMessage(`{}`))
	if err != nil || shot.IsError {
		t.Fatal(shot, err)
	}
	ipc := s.ipc
	if err := r.CloseBrowser(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(ipc); !os.IsNotExist(err) {
		t.Fatalf("IPC tree survived cleanup: %v", err)
	}
}

func TestBrowserSocketPathLimitBeforeProcesses(t *testing.T) {
	// Long temp parents must fail before pre-import, daemon or browser startup.
	r, ec := browserFixture(t)
	parent := filepath.Join(t.TempDir(), strings.Repeat("x", 80))
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", parent)
	result := browserRun(t, r, ec, "open('must-not-run','w').close()")
	if !result.IsError || !strings.Contains(result.LLMOutput, "socket path too long") || !strings.Contains(result.LLMOutput, "107") {
		t.Fatal(result)
	}
	s := r.browser.sessions[ec.Store.SessionDir(ec.SessionID)]
	if s.browser != nil || s.daemon != nil {
		t.Fatal("long IPC path started owned processes")
	}
	if _, err := os.Lstat(s.ipc); !os.IsNotExist(err) {
		t.Fatalf("failed initialization retained IPC: %v", err)
	}
}

func TestBrowserSocketPathByteBoundary(t *testing.T) {
	// The Chromium prefix is the longer of the two supported brand prefixes.
	suffix := filepath.Join("t", ".org.chromium.Chromium.XXXXXX", "SingletonSocket")
	base := t.TempDir()
	ipc := filepath.Join(base, strings.Repeat("x", 107-len(base)-len(suffix)-2))
	if err := prepareBrowserIPC(ipc); err != nil {
		t.Fatalf("107-byte path rejected: %v", err)
	}
	for _, extra := range []string{"x", "é"} {
		if err := prepareBrowserIPC(ipc + extra); err == nil {
			t.Fatalf("oversized socket accepted: %q", ipc+extra)
		}
		if _, err := os.Lstat(ipc + extra); !os.IsNotExist(err) {
			t.Fatalf("oversized path created directories: %v", err)
		}
	}
}
func TestBrowserStdinResultMatrix(t *testing.T) {
	r, ec := browserFixture(t)
	for _, tc := range []struct {
		code, class, contains string
		isError               bool
	}{
		{"print(False)", "", "False", false},
		{"if ???", "command_nonzero_exit", "SyntaxError", true},
		{"raise RuntimeError('uncaught')", "command_nonzero_exit", "Traceback", true},
		{"import sys; sys.exit(7)", "command_nonzero_exit", "exit_code=7", true},
		{"import os,signal;os.kill(os.getpid(),signal.SIGTERM)", "command_nonzero_exit", "terminated", true},
	} {
		t.Run(tc.code, func(t *testing.T) {
			got := browserRun(t, r, ec, tc.code)
			if got.IsError != tc.isError || (tc.class != "" && got.Metadata[MetadataFailureClass] != tc.class) || !strings.Contains(got.LLMOutput, tc.contains) {
				t.Fatalf("%#v", got)
			}
			if got.Metadata["business_success"] != "not_evaluated" {
				t.Fatal(got)
			}
		})
	}
	for _, raw := range []string{`{}`, `{"code":""}`, `{"code":"  "}`, `{"code":1}`, `{"code":"print(1)","timeout":10}`, `{"code":null}`} {
		result, err := r.Execute(context.Background(), "browser_exec", ec, json.RawMessage(raw))
		if err != nil || !result.IsError {
			t.Fatalf("input %s: %#v %v", raw, result, err)
		}
	}
}
func TestBrowserOutputArtifacts(t *testing.T) {
	r, ec := browserFixture(t)
	ec.Config.Runtime.ToolOutput.LLMOutputMaxBytes = 512
	ec.Config.Runtime.ToolOutput.DisplayOutputMaxBytes = 700
	result := browserRun(t, r, ec, "import sys;print('HEAD');print('x'*4000);print('MIDDLE');print('y'*4000);print('TAIL');print('STDERR',file=sys.stderr)")
	if result.IsError || result.Metadata["artifact_complete"] != true || len(result.LLMOutput) > 512 {
		t.Fatal(result)
	}
	path := filepath.Join(ec.Store.SessionDir(ec.SessionID), result.Metadata["artifact_path"].(string))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"HEAD", "MIDDLE", "TAIL", "STDERR"} {
		if !bytes.Contains(data, []byte(word)) {
			t.Fatal(word)
		}
	}
	ec.ToolCallID = "partial"
	ec.Config.Runtime.ToolOutput.ArtifactFileMaxBytes = 1024
	partial := browserRun(t, r, ec, "print('z'*10000)")
	if partial.Metadata["artifact_truncated"] != true || partial.Metadata["recoverable"] != false {
		t.Fatal(partial)
	}
	ec.ToolCallID = "unavailable"
	ec.EphemeralArtifactRoot = filepath.Join(t.TempDir(), "cross-session")
	failed := browserRun(t, r, ec, "print('z'*10000)")
	if failed.Metadata["artifact_path"] != "" || failed.Metadata["artifact_error"] == nil || failed.Metadata["recoverable"] != false {
		t.Fatal(failed)
	}
}
func TestBrowserScreenshotPrivateUniqueQuotaAndReadback(t *testing.T) {
	r, ec := browserFixture(t)
	previous := ""
	for i := 0; i < 2; i++ {
		got, err := r.Execute(context.Background(), "browser_screenshot", ec, json.RawMessage(`{}`))
		if err != nil || got.IsError {
			t.Fatalf("%#v %v", got, err)
		}
		ref := got.Metadata["screenshot_artifact_path"].(string)
		if ref == previous || !strings.HasSuffix(ref, ".png") || got.Metadata["image_delivery"] != "ref-only" || got.Metadata["model_image_visible"] != false {
			t.Fatal(got)
		}
		previous = ref
		data, info, err := fileutil.ReadRegularFileNoSymlink(filepath.Join(ec.Store.SessionDir(ec.SessionID), ref))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal(err, info)
		}
		sum := sha256.Sum256(data)
		if got.Metadata["sha256"] != hex.EncodeToString(sum[:]) || got.Metadata["screenshot_complete"] != true {
			t.Fatal(got)
		}
	}
	s := r.browser.sessions[ec.Store.SessionDir(ec.SessionID)]
	os.WriteFile(filepath.Join(s.root, "workspace", "fake-bad-json"), []byte("bad"), 0600)
	badJSON, _ := r.Execute(context.Background(), "browser_screenshot", ec, json.RawMessage(`{}`))
	if !badJSON.IsError || !strings.Contains(badJSON.LLMOutput, "invalid screenshot JSON") {
		t.Fatal(badJSON)
	}
	os.Remove(filepath.Join(s.root, "workspace", "fake-bad-json"))
	os.WriteFile(filepath.Join(s.root, "workspace", "fake-wait-false"), []byte("false"), 0600)
	unmet, _ := r.Execute(context.Background(), "browser_screenshot", ec, json.RawMessage(`{}`))
	if !unmet.IsError || unmet.Metadata[MetadataFailureClass] != "condition_not_met" {
		t.Fatal(unmet)
	}
	rawFalse := browserRun(t, r, ec, "wait_for_load(timeout=0.01)")
	if rawFalse.IsError || rawFalse.Metadata["exit_code"] != 0 || rawFalse.Metadata["business_success"] != "not_evaluated" {
		t.Fatal(rawFalse)
	}
	os.Remove(filepath.Join(s.root, "workspace", "fake-wait-false"))
	if err := os.WriteFile(filepath.Join(s.root, "workspace", "fake-bad-png"), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	bad, _ := r.Execute(context.Background(), "browser_screenshot", ec, json.RawMessage(`{}`))
	if !bad.IsError || !strings.Contains(bad.LLMOutput, "MIME") {
		t.Fatal(bad)
	}
	os.Remove(filepath.Join(s.root, "workspace", "fake-bad-png"))
	ec.Config.Runtime.ToolOutput.ArtifactFileMaxBytes = 20
	partial, _ := r.Execute(context.Background(), "browser_screenshot", ec, json.RawMessage(`{}`))
	if !partial.IsError || partial.Metadata["screenshot_truncated"] != true || partial.Metadata["screenshot_complete"] != false {
		t.Fatal(partial)
	}
	ec.EphemeralArtifactRoot = filepath.Join(t.TempDir(), "wrong")
	failed, _ := r.Execute(context.Background(), "browser_screenshot", ec, json.RawMessage(`{}`))
	if !failed.IsError || failed.Metadata["screenshot_artifact_path"] != nil {
		t.Fatal(failed)
	}
}
func TestBrowserTypedResponseOnly(t *testing.T) {
	r, ec := browserFixture(t)
	browserRun(t, r, ec, "print(False)")
	s := r.browser.sessions[ec.Store.SessionDir(ec.SessionID)]
	for _, tc := range []struct{ raw, class string }{
		{`{"path":"x","condition_met":false}`, "condition_not_met"},
		{`{'path':'x'}`, FailureClassHarnessError},
		{`{"path":"x","condition_met":true}`, FailureClassHarnessError},
		{`{"path":"x","condition_met":true} {}`, FailureClassHarnessError},
	} {
		got := s.screenshot(ec, session.ToolResult{DisplayOutput: "formatted presentation"}, "/owned/expected.png", []byte(tc.raw))
		if !got.IsError || got.Metadata[MetadataFailureClass] != tc.class {
			t.Fatal(got)
		}
	}
}
func TestBrowserEnvironmentAndDisabledRole(t *testing.T) {
	r, ec := browserFixture(t)
	for key, value := range map[string]string{"BH_TELEMETRY": "1", "BU_AUTOSPAWN": "0", "BROWSER_USE_API_KEY": "SYNTHETIC", "OPENAI_API_KEY": "SYNTHETIC", "HTTPS_PROXY": "SYNTHETIC", "PYTHONPATH": "SYNTHETIC", "BH_RUNTIME_DIR": "/unowned"} {
		t.Setenv(key, value)
		ec.Config.Runtime.ShellEnvAllowlist = append(ec.Config.Runtime.ShellEnvAllowlist, key)
	}
	raw := browserRun(t, r, ec, "import os,json;print(json.dumps(dict(os.environ)))")
	for _, value := range []string{"SYNTHETIC", "BU_AUTOSPAWN", "/unowned"} {
		if strings.Contains(raw.LLMOutput, value) {
			t.Fatal(raw)
		}
	}
	if !strings.Contains(raw.LLMOutput, `"BH_TELEMETRY": "0"`) || !strings.Contains(raw.LLMOutput, `"PYTHON_DOTENV_DISABLED": "1"`) {
		t.Fatal(raw)
	}
	if !slices.Contains(filteredEnv(ec.Config.Runtime.ShellEnvAllowlist), "BU_AUTOSPAWN=0") {
		t.Fatal("general filteredEnv changed")
	}
	disabled := config.Default()
	disabled.Tools.Browser.InstallRoot = filepath.Join(t.TempDir(), "untouched")
	reg, err := NewRegistry(disabled, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Get("browser_exec") != nil || reg.browser != nil {
		t.Fatal("disabled registered")
	}
	if _, err := os.Stat(disabled.Tools.Browser.InstallRoot); !os.IsNotExist(err) {
		t.Fatal("disabled touched install")
	}
	explorer, err := NewRegistryForToolProfile(ec.Config, nil, ec.Store, nil, session.ToolProfileExplorerReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"browser_exec", "browser_screenshot"} {
		if explorer.Get(name) != nil {
			t.Fatal(name)
		}
		result, err := explorer.Execute(context.Background(), name, ec, json.RawMessage(`{}`))
		if err != nil || result.Metadata["error_code"] != ErrorCodeToolNotAllowedForRole {
			t.Fatal(result, err)
		}
	}
}
func TestBrowserCancellationAndDeadDaemonNoReplay(t *testing.T) {
	for _, kind := range []string{"timeout", "cancel", "dead"} {
		t.Run(kind, func(t *testing.T) {
			r, ec := browserFixture(t)
			browserRun(t, r, ec, "print('initialized')")
			s := r.browser.sessions[ec.Store.SessionDir(ec.SessionID)]
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			code := "import time;time.sleep(30)"
			want := FailureClassInterrupted
			switch kind {
			case "timeout":
				ec.Config.Tools.Browser.TimeoutSec = 1
				want = FailureClassTimeout
			case "cancel":
				go func() { time.Sleep(100 * time.Millisecond); cancel() }()
			case "dead":
				s.daemon.cmd.Cancel()
				<-s.daemon.done
				os.Remove(filepath.Join(s.ipc, "bu.sock"))
				code = "open('REPLAY','w').write('effect')"
				want = FailureClassHarnessError
			}
			raw, _ := json.Marshal(map[string]string{"code": code})
			got, err := r.Execute(ctx, "browser_exec", ec, raw)
			if !got.IsError || got.Metadata[MetadataFailureClass] != want || (kind == "cancel" && !errors.Is(err, context.Canceled)) {
				t.Fatal(got, err)
			}
			if got.Metadata["cleanup"] != "confirmed" {
				t.Fatal(got)
			}
			if _, err := os.Stat(filepath.Join(s.root, "workspace", "REPLAY")); !os.IsNotExist(err) {
				t.Fatal("replayed")
			}
			previousRoot := s.root
			ec.Config.Tools.Browser.TimeoutSec = 5
			again := browserRun(t, r, ec, "print('new explicit call')")
			if again.IsError || s.root == previousRoot {
				t.Fatal(again)
			}
		})
	}
}
func TestBrowserSessionIsolationAndSerialization(t *testing.T) {
	r, ec := browserFixture(t)
	other := ec
	other.SessionID = "browser-child"
	meta := session.SessionMetadata{SchemaVersion: 1, CompletionPolicy: session.CompletionPolicyAutonomous, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Provider: "openai", Model: "fake", Mode: session.ModeExec, ID: other.SessionID, Workdir: ec.Workdir, ParentSessionID: ec.SessionID, RootSessionID: ec.SessionID, Depth: 1, QueueJobID: "queue-child"}
	if err := ec.Store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	other.EphemeralArtifactRoot = filepath.Join(ec.Store.SessionDir(other.SessionID), "artifacts", "tool-outputs")
	browserRun(t, r, ec, "open('cookie','w').write('parent')")
	got := browserRun(t, r, other, "import os;print(os.path.exists('cookie'))")
	if !strings.Contains(got.LLMOutput, "False") {
		t.Fatal(got)
	}
	a, b := r.browser.sessions[ec.Store.SessionDir(ec.SessionID)], r.browser.sessions[ec.Store.SessionDir(other.SessionID)]
	if a.root == b.root || a.ipc == b.ipc {
		t.Fatal("session paths shared")
	}
	for _, s := range []*browserSession{a, b} {
		info, err := os.Stat(s.root)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatal(info, err)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw := json.RawMessage(`{"code":"import os,time;assert not os.path.exists('busy');open('busy','w').close();time.sleep(0.1);os.remove('busy')"}`)
			result, err := r.Execute(context.Background(), "browser_exec", ec, raw)
			if err != nil {
				errs <- err
			} else if result.IsError {
				errs <- errors.New(result.LLMOutput)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestBrowserDoctorNamedStrictAndDisabled(t *testing.T) {
	r, ec := browserFixture(t)
	ec.EmitRequired = func(kind string, data map[string]any) error {
		return ec.Store.AppendEvent(ec.SessionID, events.New(ec.SessionID, kind, "tool_execute", data))
	}
	got := browserRun(t, r, ec, "print('ready')")
	if got.IsError {
		t.Fatal(got)
	}
	live, err := BrowserDoctor(context.Background(), ec.Config, ec.Store, ec.SessionID)
	if err != nil || live["status"] != "healthy_owned_daemon" {
		t.Fatal(live, err)
	}
	s := r.browser.sessions[ec.Store.SessionDir(ec.SessionID)]
	s.daemon.cmd.Cancel()
	<-s.daemon.done
	dead, err := BrowserDoctor(context.Background(), ec.Config, ec.Store, ec.SessionID)
	if err == nil || dead["status"] != "dead_daemon" {
		t.Fatal(dead, err)
	}
	select {
	case <-s.daemon.done:
	default:
		t.Fatal("doctor repaired daemon")
	}
	ec.Config.Tools.Browser.Enabled = false
	disabled, err := BrowserDoctor(context.Background(), ec.Config, ec.Store, ec.SessionID)
	if err != nil || disabled["status"] != "disabled" {
		t.Fatal(disabled, err)
	}
}

func TestBrowserPNGUsesSessionByteAndFileCaps(t *testing.T) {
	for _, dimension := range []string{"bytes", "files"} {
		t.Run(dimension, func(t *testing.T) {
			r, ec := browserFixture(t)
			if dimension == "bytes" {
				ec.Config.Runtime.ToolOutput.ArtifactSessionMaxBytes = 20
			} else {
				ec.Config.Runtime.ToolOutput.ArtifactMaxFiles = 1
				first, _ := r.Execute(context.Background(), "browser_screenshot", ec, json.RawMessage(`{}`))
				if first.IsError {
					t.Fatal(first)
				}
			}
			got, err := r.Execute(context.Background(), "browser_screenshot", ec, json.RawMessage(`{}`))
			if err != nil || !got.IsError || got.Metadata["screenshot_recoverable"] != false {
				t.Fatal(got, err)
			}
			if dimension == "bytes" && got.Metadata["screenshot_budget_reason"] != session.ToolOutputArtifactReasonSessionBytes {
				t.Fatal(got)
			}
			if dimension == "files" && (got.Metadata["screenshot_budget_reason"] != session.ToolOutputArtifactReasonSessionFiles || got.Metadata["screenshot_artifact_path"] != "") {
				t.Fatal(got)
			}
		})
	}
}
