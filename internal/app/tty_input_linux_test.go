//go:build linux

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"aegis-agent/internal/session"
)

type synchronizedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *synchronizedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(data)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

type planInputPTYFixture struct {
	mode         string
	server       *httptest.Server
	sessionRoot  string
	calls        atomic.Int32
	sessionIDs   chan string
	continuation chan []byte
	blockedCall  chan struct{}
	errors       chan error
}

func newPlanInputPTYFixture(t *testing.T, mode string) *planInputPTYFixture {
	t.Helper()
	fixture := &planInputPTYFixture{
		mode:         mode,
		sessionIDs:   make(chan string, 2),
		continuation: make(chan []byte, 1),
		blockedCall:  make(chan struct{}, 1),
		errors:       make(chan error, 4),
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (f *planInputPTYFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.errors <- fmt.Errorf("decode provider request: %w", err)
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	metadata, _ := body["metadata"].(map[string]any)
	sessionID, _ := metadata["session_id"].(string)
	if sessionID != "" {
		select {
		case f.sessionIDs <- sessionID:
		default:
		}
	}

	call := int(f.calls.Add(1))
	switch call {
	case 1:
		if f.mode == "interrupt-before" {
			f.blockedCall <- struct{}{}
			<-r.Context().Done()
			return
		}
		writePTYToolCall(w, "response-input", "call-plan-input", "request_user_input", map[string]any{
			"questions": []map[string]any{{
				"id":       "scope_choice",
				"header":   "Scope",
				"question": "Which scope should the CLI use?",
				"options": []map[string]any{
					{"label": "Narrow (Recommended)", "description": "Keep the plan focused."},
					{"label": "Broad", "description": "Include adjacent cleanup."},
				},
			}},
		})
	case 2:
		raw, err := json.Marshal(body)
		if err != nil {
			f.errors <- fmt.Errorf("marshal continuation request: %w", err)
			http.Error(w, "marshal failure", http.StatusInternalServerError)
			return
		}
		f.continuation <- raw
		if f.mode == "interrupt-after" {
			f.blockedCall <- struct{}{}
			<-r.Context().Done()
			return
		}
		writePTYToolCall(w, "response-plan", "call-submit-plan", "submit_plan", map[string]any{
			"title":         "PTY answered plan",
			"summary":       "The CLI answer reached the provider continuation.",
			"plan_markdown": "# PTY answered plan\n\n1. Use the selected scope.\n2. Verify the durable answer.",
			"verification":  []string{"The real PTY answer is replayed"},
		})
	default:
		f.errors <- fmt.Errorf("unexpected provider call %d", call)
		http.Error(w, "unexpected call", http.StatusInternalServerError)
	}
}

func writePTYToolCall(w http.ResponseWriter, responseID, callID, name string, arguments map[string]any) {
	rawArguments, _ := json.Marshal(arguments)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":     responseID,
		"status": "completed",
		"output": []map[string]any{{
			"type":      "function_call",
			"call_id":   callID,
			"name":      name,
			"arguments": string(rawArguments),
		}},
		"usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
	})
}

func TestRunPlanInputUsesSinglePTYReaderAndSettles(t *testing.T) {
	fixture := newPlanInputPTYFixture(t, "settle")
	sessionID, stdout, stderr, result := runPlanInputPTY(t, fixture, func(master *os.File, stderr *synchronizedBuffer) {
		waitForPTYText(t, stderr, "Select [1]: ")
		if _, err := master.Write([]byte("2\r")); err != nil {
			t.Fatalf("write PTY answer: %v", err)
		}
	})
	if result != nil {
		t.Fatalf("run plan input: %v\nstdout=%s\nstderr=%s", result, stdout, stderr)
	}
	continuation := waitPTYValue(t, fixture.continuation, "provider continuation")
	if !bytes.Contains(continuation, []byte("Broad")) {
		t.Fatalf("provider continuation did not receive the selected option: %s", continuation)
	}
	assertPTYSessionState(t, fixture.sessionRoot, sessionID, session.StatusAwaitingInput, "plan_approval")
	assertPTYPlanModeStatus(t, fixture.sessionRoot, sessionID, session.PlanModeStatusAwaitingApproval)
	if got := fixture.calls.Load(); got != 2 {
		t.Fatalf("provider calls=%d want 2", got)
	}
	assertNoPTYFixtureErrors(t, fixture)
}

func TestRunEscInterruptWorksBeforePlanPromptLease(t *testing.T) {
	fixture := newPlanInputPTYFixture(t, "interrupt-before")
	sessionID, _, _, result := runPlanInputPTY(t, fixture, func(master *os.File, _ *synchronizedBuffer) {
		waitPTYValue(t, fixture.blockedCall, "blocked initial provider call")
		if _, err := master.Write([]byte{27}); err != nil {
			t.Fatalf("write Esc before prompt: %v", err)
		}
	})
	if result == nil {
		t.Fatal("interrupted run unexpectedly returned success")
	}
	assertPTYSessionState(t, fixture.sessionRoot, sessionID, session.StatusPaused, "interrupt")
	assertNoPTYFixtureErrors(t, fixture)
}

func TestRunEscInterruptWorksAfterPlanPromptLease(t *testing.T) {
	fixture := newPlanInputPTYFixture(t, "interrupt-after")
	sessionID, _, _, result := runPlanInputPTY(t, fixture, func(master *os.File, stderr *synchronizedBuffer) {
		waitForPTYText(t, stderr, "Select [1]: ")
		if _, err := master.Write([]byte("2\r")); err != nil {
			t.Fatalf("write PTY answer: %v", err)
		}
		continuation := waitPTYValue(t, fixture.continuation, "provider continuation after prompt")
		if !bytes.Contains(continuation, []byte("Broad")) {
			t.Fatalf("provider continuation did not receive the selected option: %s", continuation)
		}
		waitPTYValue(t, fixture.blockedCall, "blocked continuation provider call")
		if _, err := master.Write([]byte{27}); err != nil {
			t.Fatalf("write Esc after prompt: %v", err)
		}
	})
	if result == nil {
		t.Fatal("interrupted run unexpectedly returned success")
	}
	assertPTYSessionState(t, fixture.sessionRoot, sessionID, session.StatusPaused, "interrupt")
	assertNoPTYFixtureErrors(t, fixture)
}

func runPlanInputPTY(t *testing.T, fixture *planInputPTYFixture, interact func(*os.File, *synchronizedBuffer)) (string, string, string, error) {
	t.Helper()
	master, slave := openLinuxPTY(t)
	defer master.Close()
	defer slave.Close()

	oldStdin := os.Stdin
	os.Stdin = slave
	defer func() { os.Stdin = oldStdin }()
	t.Setenv("E2E_API_KEY", "local-pty-fixture-key")

	runtimeRoot, err := os.MkdirTemp("", "aegis-plan-pty-test-")
	if err != nil {
		t.Fatalf("create PTY runtime root: %v", err)
	}
	t.Cleanup(func() {
		for attempt := 0; attempt < 20; attempt++ {
			if err := os.RemoveAll(runtimeRoot); err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := os.RemoveAll(runtimeRoot); err != nil {
			t.Errorf("remove PTY runtime root: %v", err)
		}
	})
	sessionRoot := filepath.Join(runtimeRoot, "sessions")
	fixture.sessionRoot = sessionRoot
	workspaceRoot := filepath.Join(runtimeRoot, "workspace")
	if err := os.MkdirAll(workspaceRoot, 0o700); err != nil {
		t.Fatalf("create PTY workspace: %v", err)
	}
	configPath := filepath.Join(runtimeRoot, "config.yaml")
	configBody := fmt.Sprintf(`schema_version: 1
default_provider: e2e
providers:
  e2e:
    api_provider: openai-compatible
    api_key_env: E2E_API_KEY
    base_url: %q
    model: e2e-model
    request_timeout_sec: 10
    stream_idle_timeout_ms: 10000
    retry:
      max_attempts: 1
    max_output_tokens: 2048
    context_window_tokens: 200000
    store: false
session:
  dir: %q
  dir_mode: "0700"
runtime:
  guardrails_mode: yolo
  max_turns_hard: -1
  compact:
    input_char_threshold: 0
    semantic_summary:
      enabled: false
  queue:
    auto_worker: false
    reaper_interval_ms: -1
`, fixture.server.URL, sessionRoot)
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatalf("write PTY config: %v", err)
	}

	var stdout synchronizedBuffer
	var stderr synchronizedBuffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resultCh := make(chan error, 1)
	go func() {
		resultCh <- Run(ctx, []string{"run", "--config", configPath, "--workdir", workspaceRoot, "--plan", "exercise real PTY plan input"}, &stdout, &stderr)
	}()

	sessionID := waitPTYValue(t, fixture.sessionIDs, "provider session id")
	interact(master, &stderr)
	var result error
	select {
	case result = <-resultCh:
	case <-ctx.Done():
		t.Fatalf("PTY run did not settle: %v\nstdout=%s\nstderr=%s", ctx.Err(), stdout.String(), stderr.String())
	}
	return sessionID, stdout.String(), stderr.String(), result
}

func openLinuxPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	masterFD, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("open PTY master: %v", err)
	}
	master := os.NewFile(uintptr(masterFD), "/dev/ptmx")
	if err := unix.IoctlSetPointerInt(masterFD, unix.TIOCSPTLCK, 0); err != nil {
		master.Close()
		t.Fatalf("unlock PTY: %v", err)
	}
	number, err := unix.IoctlGetInt(masterFD, unix.TIOCGPTN)
	if err != nil {
		master.Close()
		t.Fatalf("resolve PTY slave: %v", err)
	}
	slavePath := fmt.Sprintf("/dev/pts/%d", number)
	slaveFD, err := unix.Open(slavePath, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		master.Close()
		t.Fatalf("open PTY slave: %v", err)
	}
	return master, os.NewFile(uintptr(slaveFD), slavePath)
}

func waitForPTYText(t *testing.T, output *synchronizedBuffer, text string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(output.String(), text) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("PTY output never contained %q: %s", text, output.String())
}

func waitPTYValue[T any](t *testing.T, values <-chan T, label string) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
		var zero T
		return zero
	}
}

func assertPTYSessionState(t *testing.T, sessionRoot, sessionID, wantStatus, wantPhase string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(sessionRoot, sessionID, "state.json"))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var state session.State
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if state.Status != wantStatus || state.Phase != wantPhase {
		t.Fatalf("state=%#v want status=%s phase=%s", state, wantStatus, wantPhase)
	}
}

func assertPTYPlanModeStatus(t *testing.T, sessionRoot, sessionID, wantStatus string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(sessionRoot, sessionID, "planmode.json"))
	if err != nil {
		t.Fatalf("read plan mode: %v", err)
	}
	var planMode session.PlanModeState
	if err := json.Unmarshal(data, &planMode); err != nil {
		t.Fatalf("decode plan mode: %v", err)
	}
	if planMode.Status != wantStatus {
		t.Fatalf("plan mode status=%s want %s", planMode.Status, wantStatus)
	}
}

func assertNoPTYFixtureErrors(t *testing.T, fixture *planInputPTYFixture) {
	t.Helper()
	select {
	case err := <-fixture.errors:
		t.Fatal(err)
	default:
	}
}
