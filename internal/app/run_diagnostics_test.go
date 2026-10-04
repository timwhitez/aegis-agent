package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	"aegis-agent/internal/config"
	"aegis-agent/internal/events"
	"aegis-agent/internal/runtime"
	"aegis-agent/internal/session"
)

// HTTP handlers observe stderr concurrently, so the evidence buffer must be
// synchronized independently of the CLI's own diagnostic serialization.
type runDiagnosticTestBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *runDiagnosticTestBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *runDiagnosticTestBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func (b *runDiagnosticTestBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.b.Reset()
}

type runDiagnosticWire struct {
	Model, Before string
	Metadata      bool
}

type runDiagnosticFixture struct {
	cfg    *config.Config
	path   string
	home   string
	origin string
	errOut *runDiagnosticTestBuffer
	mu     sync.Mutex
	wire   []runDiagnosticWire
}

func newRunDiagnosticFixture(t *testing.T) *runDiagnosticFixture {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	home := filepath.Join(dir, "private-home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("AEGIS_AGENT_CONFIG", "")
	t.Setenv("AEGIS_AGENT_TRUST_WORKSPACE_CONFIG", "")
	t.Setenv("AEGIS_AGENT_ENV_FILE", filepath.Join(dir, "absent.env"))
	t.Setenv("RUN_DIAGNOSTIC_TEST_KEY", "offline-dummy-key")
	f := &runDiagnosticFixture{path: filepath.Join(dir, "operator's $literal.yaml"), home: home, errOut: &runDiagnosticTestBuffer{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, metadata := body["metadata"]
		f.mu.Lock()
		f.wire = append(f.wire, runDiagnosticWire{Model: fmt.Sprint(body["model"]), Metadata: metadata, Before: f.errOut.String()})
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if metadata {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"code":"unsupported_argument","param":"metadata","message":"Argument not supported: metadata"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"diagnostic_mock","status":"completed","output":[{"type":"function_call","call_id":"diagnostic_finish","name":"finish","arguments":"{\"message\":\"offline complete\"}"}],"usage":{"input_tokens":5,"output_tokens":2}}`)
	}))
	t.Cleanup(server.Close)
	f.origin = server.URL
	f.cfg = config.Default()
	f.cfg.DefaultProvider = "gateway"
	f.cfg.Providers["gateway"] = config.Provider{APIProvider: "openai-compatible", WireAPI: "responses", BaseURL: server.URL + "/private-path?secret=query#PRIVATE_FRAGMENT_TOKEN", Model: "original-model", APIKeyEnv: "RUN_DIAGNOSTIC_TEST_KEY"}
	f.cfg.Session.Dir = filepath.Join(dir, "sessions")
	return f
}

func (f *runDiagnosticFixture) save(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := yaml.Marshal(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *runDiagnosticFixture) observations() []runDiagnosticWire {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]runDiagnosticWire(nil), f.wire...)
}

func (f *runDiagnosticFixture) run(t *testing.T, args ...string) []map[string]any {
	t.Helper()
	f.errOut.Reset()
	var out bytes.Buffer
	if err := Run(context.Background(), args, &out, f.errOut); err != nil {
		t.Fatalf("run %v: %v\nstdout=%s\nstderr=%s", args, err, out.String(), f.errOut.String())
	}
	var values []map[string]any
	decoder := json.NewDecoder(&out)
	for {
		var value map[string]any
		if err := decoder.Decode(&value); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("machine stdout polluted: %v\n%s", err, out.String())
		}
		values = append(values, value)
	}
	return values
}

func TestRunDiagnosticsBeforeRequestAndUnreadWorkspace(t *testing.T) {
	for _, mode := range []string{"run", "exec"} {
		t.Run(mode, func(t *testing.T) {
			f := newRunDiagnosticFixture(t)
			off := false
			p := f.cfg.Providers["gateway"]
			p.SendMetadata = &off
			f.cfg.Providers["gateway"] = p
			f.save(t, filepath.Join(f.home, ".aegis-agent", "config.yaml"))
			workspace := filepath.Join(".aegis-agent", "config.yaml")
			if err := os.MkdirAll(filepath.Dir(workspace), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(workspace, []byte("unparseable: [\nUNTRUSTED_PRIVATE_VALUE"), 0o600); err != nil {
				t.Fatal(err)
			}
			f.run(t, mode, "--json", "PRIVATE_PROMPT_MUST_NOT_BE_DIAGNOSTIC")
			wire := f.observations()
			if len(wire) != 1 || wire[0].Metadata {
				t.Fatalf("wire: %#v", wire)
			}
			before := wire[0].Before
			for _, want := range []string{"config layer", "home", "loaded", "workspace", "skipped_untrusted", "not inspected", "--config", "execution target", `profile="gateway"`, `model="original-model"`, f.origin, "send_metadata=false"} {
				if !strings.Contains(before, want) {
					t.Errorf("before first request missing %q: %s", want, before)
				}
			}
			for _, secret := range []string{"private-path", "secret=query", "PRIVATE_FRAGMENT_TOKEN", "UNTRUSTED_PRIVATE_VALUE", "PRIVATE_PROMPT_MUST_NOT_BE_DIAGNOSTIC", "offline-dummy-key"} {
				if strings.Contains(before, secret) {
					t.Errorf("diagnostic leaked %q: %s", secret, before)
				}
			}
			if strings.Contains(before, "metadata compatibility") {
				t.Error("false option must not show enabled-metadata advice")
			}
		})
	}
}

func TestRunDiagnosticsMetadataScopeAndDurableResume(t *testing.T) {
	f := newRunDiagnosticFixture(t)
	on, off := true, false
	p := f.cfg.Providers["gateway"]
	p.SendMetadata = &on
	f.cfg.Providers["gateway"] = p
	f.save(t, f.path)
	values := f.run(t, "exec", "--config", f.path, "--json", "offline")
	id := fmt.Sprint(values[len(values)-1]["session_id"])
	// A second Start in this very process constructs a fresh adapter: there is
	// no process-wide unsupported metadata cache.
	f.run(t, "exec", "--config", f.path, "--json", "offline again")
	wire := f.observations()
	if len(wire) != 4 || !wire[0].Metadata || wire[1].Metadata || !wire[2].Metadata || wire[3].Metadata {
		t.Fatalf("instance scope changed: %#v", wire)
	}
	for _, want := range []string{"metadata compatibility", "adapter instance", "send_metadata: false", "existing complete", "new session", "snapshot", `"gateway"`} {
		if !strings.Contains(wire[0].Before, want) {
			t.Errorf("pre-request compatibility guidance missing %q: %s", want, wire[0].Before)
		}
	}
	p.SendMetadata = &off
	f.cfg.Providers["gateway"] = p
	f.save(t, f.path)
	f.run(t, "exec", "--config", f.path, "--output-format", "stream-json", "new false session")
	if got := f.observations(); len(got) != 5 || got[4].Metadata {
		t.Fatalf("configured false must omit metadata on FIRST new request: %#v", got)
	}
	p.Model, p.BaseURL = "changed-model", "http://127.0.0.1:1/never-contact"
	f.cfg.Providers["gateway"] = p
	f.save(t, f.path)
	f.run(t, "exec", "--resume", id, "--config", f.path, "--json", "resume old true snapshot")
	wire = f.observations()
	if len(wire) != 7 || !wire[5].Metadata || wire[6].Metadata || wire[5].Model != "original-model" {
		t.Fatalf("durable resume changed: %#v", wire)
	}
	if !strings.Contains(wire[5].Before, `model="original-model"`) || !strings.Contains(wire[5].Before, f.origin) || strings.Contains(wire[5].Before, "never-contact") {
		t.Fatalf("resume target is not durable: %s", wire[5].Before)
	}
	store := session.NewStore(f.cfg.Session.Dir)
	meta, err := store.LoadMetadata(id)
	if err != nil || meta.ProviderOptions.SendMetadata == nil || !*meta.ProviderOptions.SendMetadata {
		t.Fatalf("persisted true snapshot lost: %#v %v", meta, err)
	}
}

func TestRunDiagnosticsLayeredSelectionAndCLIOverrides(t *testing.T) {
	f := newRunDiagnosticFixture(t)
	f.save(t, filepath.Join(f.home, ".aegis-agent", "config.yaml"))
	// This layer intentionally does not define the provider or session path.
	if err := os.WriteFile(f.path, []byte("runtime:\n  max_turns_soft: 5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AEGIS_AGENT_CONFIG", f.path)
	f.run(t, "exec", "--provider", "gateway", "--model", "override-model", "--workdir", "chosen-work", "--json", "offline")
	wire := f.observations()
	if len(wire) != 2 || wire[0].Model != "override-model" {
		t.Fatalf("overrides lost: %#v", wire)
	}
	before := wire[0].Before
	for _, want := range []string{"home", "env", "current configuration", "--provider", "override-model", "chosen-work", "Later layers", "existing complete"} {
		if !strings.Contains(before, want) {
			t.Errorf("missing layered guidance %q: %s", want, before)
		}
	}
	// Only the skipped-workspace recipe may introduce --config. The new-session
	// metadata recipe must preserve the layered selector and its environment.
	for _, line := range strings.Split(before, "\n") {
		if strings.HasPrefix(line, "new session:") && strings.Contains(line, "--config") {
			t.Fatalf("metadata advice promotes an incomplete layer into --config: %s", line)
		}
	}
}

type runDiagnosticFailingWriter struct{}

func (runDiagnosticFailingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestRunDiagnosticsStderrFailureDoesNotChangeSession(t *testing.T) {
	f := newRunDiagnosticFixture(t)
	f.save(t, f.path)
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"exec", "--config", f.path, "--json", "offline"}, &out, runDiagnosticFailingWriter{}); err != nil {
		t.Fatalf("stderr failure altered execution: %v %s", err, out.String())
	}
	if wire := f.observations(); len(wire) != 2 {
		t.Fatalf("stderr failure blocked provider: %#v", wire)
	}
}

func TestRunDiagnosticsEndpointRedaction(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"https://user:SECRET_PASSWORD@gateway.example:8443/SECRET_PATH?SECRET_QUERY=value#SECRET_FRAGMENT", "https://gateway.example:8443"},
		{"http://[::1]:1234/path?key=x", "http://[::1]:1234"},
		{"https://gateway.example/a%2Fb", "https://gateway.example"},
		{"https://gateway.example/%zzSECRET", "[endpoint unavailable]"},
		{"https:SECRET_OPAQUE", "[endpoint unavailable]"},
		{"file:///SECRET_PATH", "[endpoint unavailable]"},
		{"https://SECRET\n.invalid", "[endpoint unavailable]"},
		{"", "[endpoint unavailable]"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			if got := safeDiagnosticEndpoint(tc.raw); got != tc.want {
				t.Errorf("redaction = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRunDiagnosticsShellTemplatesAndControls(t *testing.T) {
	// Execute only a fixed printf built-in to prove literal argument quoting.
	// No diagnostic command is evaluated as shell code elsewhere in the CLI.
	for _, value := range []string{"a'b", "$(exit 19)", "`exit 19`", "literal $HOME", "space name", "普通文字"} {
		var out bytes.Buffer
		d := newRunDiagnostics(config.Default(), runDiagnosticSelection{}, &out)
		d.command("recipe", "printf", "%s", value)
		command := strings.TrimPrefix(strings.TrimSpace(out.String()), "recipe: ")
		got, err := exec.Command("/bin/sh", "-c", command).Output()
		if err != nil || string(got) != value {
			t.Errorf("quote literal %q: got=%q err=%v", value, got, err)
		}
	}
	for _, value := range []string{"line\nbreak", "escape\x1b[31m", "bidi\u202e", "join\u200d", "tab\t", "invalid\x9b"} {
		var out bytes.Buffer
		d := newRunDiagnostics(config.Default(), runDiagnosticSelection{mode: "exec", provider: value}, &out)
		d.sessionActive(session.SessionMetadata{ID: "id", Provider: value, Model: value, ProviderOptions: session.ProviderOptions{APIProvider: "openai-compatible", BaseURL: "https://gateway.example"}})
		if strings.Contains(out.String(), value) || !strings.Contains(out.String(), "template omitted") {
			t.Errorf("unsafe executable/display value %q: %s", value, out.String())
		}
	}
	var out bytes.Buffer
	d := newRunDiagnostics(config.Default(), runDiagnosticSelection{mode: "exec", resume: true}, &out)
	d.sessionActive(session.SessionMetadata{Provider: "MixedCase", Model: "model", ProviderOptions: session.ProviderOptions{APIProvider: "openai-compatible"}})
	if !strings.Contains(out.String(), "cannot be reproduced") || strings.Contains(out.String(), "'--provider' 'MixedCase'") {
		t.Fatalf("unrepresentable stored profile copied into CLI: %s", out.String())
	}
}

func TestRunDiagnosticsFallbackUsesTypedEscapedProfile(t *testing.T) {
	var out bytes.Buffer
	d := newRunDiagnostics(config.Default(), runDiagnosticSelection{}, &out)
	d.handle(events.New("id", "provider.capability_fallback", "provider", map[string]any{"feature": "other", "reason": "unsupported_argument", "provider_profile": "gateway"}))
	d.handle(events.New("id", "provider.capability_fallback", "provider", map[string]any{"feature": "metadata", "reason": "validation", "provider_profile": "gateway"}))
	d.handle(events.New("id", "provider.capability_fallback", "provider", map[string]any{"feature": "metadata", "reason": "unsupported_argument", "provider_profile": 1}))
	if out.Len() != 0 {
		t.Fatal("misleading capability notice")
	}
	d.handle(events.New("id", "provider.capability_fallback", "provider", map[string]any{"feature": "metadata", "reason": "unsupported_argument", "provider_profile": "gateway\x1b[31m"}))
	if strings.Contains(out.String(), "\x1b") || !strings.Contains(out.String(), `gateway\x1b[31m`) {
		t.Fatalf("fallback terminal escape leaked: %s", out.String())
	}
}

func TestRunDiagnosticsDefaultNilAndLegacyResume(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint(legacy), func(t *testing.T) {
			f := newRunDiagnosticFixture(t)
			f.save(t, f.path)
			values := f.run(t, "exec", "--config", f.path, "--json", "offline")
			id := fmt.Sprint(values[len(values)-1]["session_id"])
			store := session.NewStore(f.cfg.Session.Dir)
			meta, err := store.LoadMetadata(id)
			if err != nil {
				t.Fatal(err)
			}
			if meta.ProviderOptions.SendMetadata != nil {
				t.Fatal("expected recorded default nil option")
			}
			if legacy {
				meta.ProviderOptions = session.ProviderOptions{}
				if err := store.SaveMetadata(id, meta); err != nil {
					t.Fatal(err)
				}
			}
			off := false
			p := f.cfg.Providers["gateway"]
			p.SendMetadata = &off
			f.cfg.Providers["gateway"] = p
			f.save(t, f.path)
			f.run(t, "exec", "--resume", id, "--config", f.path, "--json", "offline resume")
			wire := f.observations()[2:]
			if legacy {
				if len(wire) != 1 || wire[0].Metadata || strings.Contains(wire[0].Before, "metadata compatibility") {
					t.Fatalf("legacy missing options did not resolve current false: %#v", wire)
				}
			} else if len(wire) != 2 || !wire[0].Metadata || wire[1].Metadata || !strings.Contains(wire[0].Before, "send_metadata=enabled_default") {
				t.Fatalf("recorded nil default reinterpreted: %#v", wire)
			}
		})
	}
}

func TestRunDiagnosticsResolvedRoleTargetThroughCoreFacade(t *testing.T) {
	f := newRunDiagnosticFixture(t)
	f.cfg.RoleProviders.Explorer = config.RoleProviderOverride{Provider: "gateway", Model: "role-model", BaseURL: f.origin + "/role-path"}
	d := newRunDiagnostics(f.cfg, runDiagnosticSelection{mode: "exec"}, f.errOut)
	runner := runtime.NewCoreRunner(f.cfg)
	runner.SetRunLifecycleHooks(runtime.RunLifecycleHooks{OnSessionActive: func(meta session.SessionMetadata, _ *runtime.Runner) error { d.sessionActive(meta); return nil }})
	result, err := runner.Start(context.Background(), runtime.StartRequest{Prompt: "offline role", AgentRole: "explorer", Workdir: ".", Mode: "exec"})
	wire := f.observations()
	if err != nil || result.Status != session.StatusCompleted || len(wire) != 2 || wire[0].Model != "role-model" || !strings.Contains(wire[0].Before, `model="role-model"`) || !strings.Contains(wire[0].Before, f.origin) {
		t.Fatalf("role target: %v %#v %#v", err, result, wire)
	}
}

func TestRunDiagnosticsMissingConfigReportWithoutReinspection(t *testing.T) {
	f := newRunDiagnosticFixture(t)
	cfg, _, err := config.LoadWithReport(f.path, ".")
	if err != nil {
		t.Fatal(err)
	}
	// A changed, invalid file after the authoritative load must not alter the
	// report or cause a second read in the diagnostic path.
	if err := os.WriteFile(f.path, []byte("invalid: [\nPRIVATE_AFTER_LOAD"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := newRunDiagnostics(cfg, runDiagnosticSelection{mode: "exec"}, f.errOut)
	d.sessionActive(session.SessionMetadata{Provider: "openai", Model: "gpt-5.4", ProviderOptions: session.ProviderOptions{APIProvider: "openai-compatible", BaseURL: "https://api.openai.com/v1"}})
	text := f.errOut.String()
	for _, want := range []string{`outcome="missing"`, "No config file loaded", "init --config", `profile="openai"`, `model="gpt-5.4"`} {
		if !strings.Contains(text, want) {
			t.Errorf("missing source fact %q: %s", want, text)
		}
	}
	if strings.Contains(text, "PRIVATE_AFTER_LOAD") || strings.Contains(text, `outcome="loaded"`) {
		t.Fatalf("reinspection changed authoritative report: %s", text)
	}
}
