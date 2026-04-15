package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/session"
)

func TestRunnerSupportsOpenAICompatibleResponses(t *testing.T) {
	cfg := config.Default()
	cfg.DefaultProvider = "openai-compatible"
	cfg.Providers["openai-compatible"] = config.Provider{
		APIKeyEnv:  "OPENAI_API_KEY",
		BaseURL:    "http://localhost:3000/v1",
		Model:      "gpt-5.4",
		TimeoutSec: 120,
		WireAPI:    "responses",
	}
	runner := NewRunner(cfg)
	adapter, err := runner.adapter("openai-compatible")
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	if adapter.Name() != "openai" {
		t.Fatalf("expected openai adapter, got %s", adapter.Name())
	}
}

func TestProviderOptionsFromConfigIncludesRetryPolicy(t *testing.T) {
	opts := providerOptionsFromConfig("openai-compatible", config.Provider{
		ReasoningEffort: "medium",
		Retry: config.Retry{
			MaxAttempts:    3,
			BaseDelayMS:    250,
			Retry429:       true,
			Retry5xx:       true,
			RetryTransport: true,
		},
	})
	if opts.RetryPolicy == nil {
		t.Fatal("expected retry policy to be recorded in provider options")
	}
	want := &session.ProviderRetryPolicy{
		MaxAttempts:    3,
		BaseDelayMS:    250,
		Retry429:       true,
		Retry5xx:       true,
		RetryTransport: true,
	}
	if !reflect.DeepEqual(opts.RetryPolicy, want) {
		t.Fatalf("unexpected retry policy: %#v", opts.RetryPolicy)
	}
}

func TestRunnerStartPersistsProviderOptionsInSessionMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_1",
			"status":"completed",
			"output":[
				{
					"type":"function_call",
					"call_id":"call_1",
					"name":"finish",
					"arguments":"{\"message\":\"done\"}"
				}
			],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	cfg.DefaultProvider = "openai-compatible"
	store := false
	sendMetadata := false
	cfg.Providers["openai-compatible"] = config.Provider{
		APIKeyEnv:       "OPENAI_API_KEY",
		BaseURL:         server.URL + "/v1",
		Model:           "gpt-5.4",
		TimeoutSec:      30,
		WireAPI:         "responses",
		Store:           &store,
		SendMetadata:    &sendMetadata,
		ReasoningEffort: "medium",
		Retry: config.Retry{
			MaxAttempts:    3,
			BaseDelayMS:    250,
			Retry429:       true,
			Retry5xx:       true,
			RetryTransport: true,
		},
	}
	t.Setenv("OPENAI_API_KEY", "test-key")

	runner := NewRunner(cfg)
	workdir := t.TempDir()
	result, err := runner.Start(context.Background(), StartRequest{
		Prompt:   "Return exactly one finish tool call with message: done",
		Provider: "openai-compatible",
		Workdir:  workdir,
		Mode:     session.ModeExec,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("expected completed result, got %#v", result)
	}

	meta, err := runner.store.LoadMetadata(result.SessionID)
	if err != nil {
		t.Fatalf("load metadata: %v", err)
	}
	if meta.Provider != "openai-compatible" {
		t.Fatalf("expected provider to persist, got %#v", meta.Provider)
	}
	if meta.ProviderOptions.Store == nil || *meta.ProviderOptions.Store {
		t.Fatalf("expected store=false in session metadata, got %#v", meta.ProviderOptions.Store)
	}
	if meta.ProviderOptions.SendMetadata == nil || *meta.ProviderOptions.SendMetadata {
		t.Fatalf("expected send_metadata=false in session metadata, got %#v", meta.ProviderOptions.SendMetadata)
	}
	wantRetry := &session.ProviderRetryPolicy{
		MaxAttempts:    3,
		BaseDelayMS:    250,
		Retry429:       true,
		Retry5xx:       true,
		RetryTransport: true,
	}
	if !reflect.DeepEqual(meta.ProviderOptions.RetryPolicy, wantRetry) {
		t.Fatalf("unexpected retry policy in session metadata: %#v", meta.ProviderOptions.RetryPolicy)
	}
}

func TestRunnerStartPropagatesConfiguredProviderOptionsIntoOpenAIRequest(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		defer r.Body.Close()
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(data, &body); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_1",
			"status":"completed",
			"output":[
				{
					"type":"function_call",
					"call_id":"call_1",
					"name":"finish",
					"arguments":"{\"message\":\"done\"}"
				}
			],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	cfg.DefaultProvider = "openai-compatible"
	store := false
	sendMetadata := true
	temperature := 0.2
	topP := 0.8
	cfg.Providers["openai-compatible"] = config.Provider{
		APIKeyEnv:       "OPENAI_API_KEY",
		BaseURL:         server.URL + "/v1",
		Model:           "gpt-5.4",
		TimeoutSec:      30,
		WireAPI:         "responses",
		Temperature:     &temperature,
		TopP:            &topP,
		MaxOutputTokens: 512,
		ReasoningEffort: "high",
		TextVerbosity:   "low",
		Store:           &store,
		SendMetadata:    &sendMetadata,
		Retry: config.Retry{
			MaxAttempts:    3,
			BaseDelayMS:    250,
			Retry429:       true,
			Retry5xx:       true,
			RetryTransport: true,
		},
	}
	t.Setenv("OPENAI_API_KEY", "test-key")

	runner := NewRunner(cfg)
	result, err := runner.Start(context.Background(), StartRequest{
		Prompt:   "Return exactly one finish tool call with message: done",
		Provider: "openai-compatible",
		Workdir:  t.TempDir(),
		Mode:     session.ModeExec,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("expected completed result, got %#v", result)
	}

	meta, err := runner.store.LoadMetadata(result.SessionID)
	if err != nil {
		t.Fatalf("load metadata: %v", err)
	}
	if meta.ProviderOptions.Store == nil || *meta.ProviderOptions.Store {
		t.Fatalf("expected store=false in session metadata, got %#v", meta.ProviderOptions.Store)
	}
	if meta.ProviderOptions.SendMetadata == nil || !*meta.ProviderOptions.SendMetadata {
		t.Fatalf("expected send_metadata=true in session metadata, got %#v", meta.ProviderOptions.SendMetadata)
	}
	if got := body["store"]; got != false {
		t.Fatalf("expected store=false in outbound body, got %#v", got)
	}
	if got := body["max_output_tokens"]; got != float64(512) {
		t.Fatalf("expected max_output_tokens=512, got %#v", got)
	}
	reasoning, ok := body["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "high" {
		t.Fatalf("expected reasoning.effort=high, got %#v", body["reasoning"])
	}
	text, ok := body["text"].(map[string]any)
	if !ok || text["verbosity"] != "low" {
		t.Fatalf("expected text.verbosity=low, got %#v", body["text"])
	}
	metadata, ok := body["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("expected metadata map in outbound body, got %#v", body["metadata"])
	}
	if metadata["session_id"] != result.SessionID || metadata["mode"] != session.ModeExec {
		t.Fatalf("expected session metadata in outbound body, got %#v", metadata)
	}
}

func TestRunnerRejectsUnsupportedOpenAICompatibleWireAPI(t *testing.T) {
	cfg := config.Default()
	cfg.Providers["openai-compatible"] = config.Provider{
		APIKeyEnv:  "OPENAI_API_KEY",
		BaseURL:    "http://localhost:3000/v1",
		Model:      "gpt-5.4",
		TimeoutSec: 120,
		WireAPI:    "chat",
	}
	runner := NewRunner(cfg)
	if _, err := runner.adapter("openai-compatible"); err == nil {
		t.Fatal("expected unsupported wire_api error")
	}
}

func TestRunnerContinueUsesDurableRetryPolicyFromSessionMetadata(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if attempts.Add(1) == 1 {
			http.Error(w, "try later", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_1",
			"status":"completed",
			"output":[
				{
					"type":"function_call",
					"call_id":"call_1",
					"name":"finish",
					"arguments":"{\"message\":\"done\"}"
				}
			],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	cfg.DefaultProvider = "openai-compatible"
	store := false
	cfg.Providers["openai-compatible"] = config.Provider{
		APIKeyEnv:  "OPENAI_API_KEY",
		BaseURL:    server.URL + "/v1",
		Model:      "gpt-5.4",
		TimeoutSec: 30,
		WireAPI:    "responses",
		Store:      &store,
		Retry: config.Retry{
			MaxAttempts: 2,
			BaseDelayMS: 1,
			Retry5xx:    true,
		},
	}
	t.Setenv("OPENAI_API_KEY", "test-key")

	runner := NewRunner(cfg)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "openai-compatible",
		Model:            "gpt-5.4",
		CompletionPolicy: completionPolicy(session.ModeExec),
		ProviderOptions:  providerOptionsFromConfig("openai-compatible", cfg.Providers["openai-compatible"]),
	}
	state := session.State{
		Status:    session.StatusAwaitingInput,
		Phase:     "turn_decide",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := runner.store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	drifted := cfg.Providers["openai-compatible"]
	drifted.Retry.MaxAttempts = 1
	cfg.Providers["openai-compatible"] = drifted

	result, err := runner.Continue(context.Background(), ContinueRequest{
		SessionID: meta.ID,
		Message:   "Continue and finish.",
	})
	if err != nil {
		t.Fatalf("continue: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("expected completed result, got %#v", result)
	}
	if attempts.Load() != 2 {
		t.Fatalf("expected durable retry policy to allow 2 attempts, got %d", attempts.Load())
	}
	events, err := loadEvents(runner.store, meta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	if !hasEventType(events, "provider.retry") {
		t.Fatalf("expected provider.retry event after durable retry reconstruction, got %#v", events)
	}
}

func TestRunnerAppendUserMessageAppliesUserHook(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	cfg.Hooks.UserMessage = []config.HookDefinition{
		{
			Name:   "prefix",
			Inject: &config.HookInject{Field: "text", Prefix: "[interactive] "},
		},
	}
	runner := NewRunner(cfg)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: completionPolicy(session.ModeRun),
	}
	state := session.State{
		Status:    session.StatusRunning,
		Phase:     "prepare",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := runner.store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := runner.appendUserMessage(context.Background(), meta, "prepare", "hello", nil); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	messages, err := runner.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Text != "[interactive] hello" {
		t.Fatalf("unexpected messages: %#v", messages)
	}
}

func TestRunnerContinueResetsTurnBudgetAfterMaxTurnsFailure(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.GuardrailsMode = "standard"
	cfg.Session.Dir = t.TempDir()
	cfg.DefaultProvider = "openai-compatible"
	cfg.Runtime.MaxTurnsHard = 1

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_1",
			"status":"completed",
			"output":[
				{
					"type":"function_call",
					"call_id":"call_1",
					"name":"finish",
					"arguments":"{\"message\":\"continued\"}"
				}
			],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()

	cfg.Providers["openai-compatible"] = config.Provider{
		APIKeyEnv:  "OPENAI_API_KEY",
		BaseURL:    server.URL + "/v1",
		Model:      "gpt-5.4",
		TimeoutSec: 30,
		WireAPI:    "responses",
	}
	t.Setenv("OPENAI_API_KEY", "test-key")

	runner := NewRunner(cfg)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai-compatible",
		Model:            "gpt-5.4",
		CompletionPolicy: completionPolicy(session.ModeRun),
		RootSessionID:    "root",
		ProviderOptions:  providerOptionsFromConfig("openai-compatible", cfg.Providers["openai-compatible"]),
	}
	state := session.State{
		Status:    session.StatusFailed,
		Phase:     "tool_execute",
		Turn:      41,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		LastError: "max_turns_hard_exceeded",
	}
	if err := runner.store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	result, err := runner.Continue(context.Background(), ContinueRequest{
		SessionID: meta.ID,
		Message:   "Resume and finish.",
	})
	if err != nil {
		t.Fatalf("continue: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("expected completed result, got %#v", result)
	}

	loaded, err := runner.store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if loaded.Status != session.StatusCompleted {
		t.Fatalf("expected completed state after continue, got %#v", loaded)
	}
	if loaded.Turn != 1 {
		t.Fatalf("expected resumed run to restart turn budget, got turn=%d", loaded.Turn)
	}
	if loaded.LastError != "" {
		t.Fatalf("expected continue to clear stale last_error, got %q", loaded.LastError)
	}
}

func TestRunnerSteerRejectsEmptyMessage(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	runner := NewRunner(cfg)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: completionPolicy(session.ModeRun),
	}
	state := session.State{
		Status:    session.StatusRunning,
		Phase:     "prepare",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := runner.store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := runner.Steer(context.Background(), SteerRequest{SessionID: meta.ID, Message: "   "}); err == nil {
		t.Fatal("expected empty steer message to be rejected")
	}
}

func TestRunnerSteerRequiresRunningSession(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	runner := NewRunner(cfg)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: completionPolicy(session.ModeRun),
	}
	state := session.State{
		Status:    session.StatusAwaitingInput,
		Phase:     "turn_decide",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := runner.store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := runner.Steer(context.Background(), SteerRequest{SessionID: meta.ID, Message: "focus on tests"}); err == nil || !strings.Contains(err.Error(), "session is not running; use continue instead") {
		t.Fatalf("expected running-session guard, got %v", err)
	}
}

func TestRunnerSteerRejectsOversizedTextInput(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	runner := NewRunner(cfg)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: completionPolicy(session.ModeRun),
	}
	state := session.State{
		Status:    session.StatusRunning,
		Phase:     "prepare",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := runner.store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err := runner.Steer(context.Background(), SteerRequest{
		SessionID: meta.ID,
		Message:   strings.Repeat("x", defaultSteerMaxMessageChars+1),
	})
	if err == nil {
		t.Fatal("expected oversized steer input to be rejected")
	}
	var inputErr SteerValidationError
	if !errors.As(err, &inputErr) {
		t.Fatalf("expected steer validation error, got %T %v", err, err)
	}
	if inputErr.Code != "steer_input_too_large" {
		t.Fatalf("unexpected validation code: %#v", inputErr)
	}
	if inputErr.MaxChars != defaultSteerMaxMessageChars || inputErr.ActualChars != defaultSteerMaxMessageChars+1 {
		t.Fatalf("unexpected validation limits: %#v", inputErr)
	}
}

func TestRunnerSteerReturnsQueuedBehaviorForRunningSession(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	runner := NewRunner(cfg)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: completionPolicy(session.ModeRun),
	}
	state := session.State{
		Status:    session.StatusRunning,
		Phase:     "prepare",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := runner.store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	result, err := runner.Steer(context.Background(), SteerRequest{
		SessionID: meta.ID,
		Message:   "focus on tests",
		Interrupt: true,
	})
	if err != nil {
		t.Fatalf("steer: %v", err)
	}
	if !result.Accepted || result.Behavior != "queued" || result.SessionID != meta.ID {
		t.Fatalf("unexpected steer result: %#v", result)
	}
	loadedState, err := runner.store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if loadedState.PendingSteerCount != 1 {
		t.Fatalf("expected pending steer count to be tracked, got %#v", loadedState)
	}
	requests, err := runner.store.LoadSteerRequests(meta.ID)
	if err != nil {
		t.Fatalf("load steer requests: %v", err)
	}
	if len(requests) != 1 || requests[0].Status != session.SteerStatusPending || !requests[0].Interrupt {
		t.Fatalf("unexpected stored steer requests: %#v", requests)
	}
}

func TestRunnerWatchSteerHandlesMultipleInterruptRequests(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	cfg.Runtime.Steer.PollIntervalMS = 10
	runner := NewRunner(cfg)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: completionPolicy(session.ModeRun),
	}
	state := session.State{
		Status:    session.StatusRunning,
		Phase:     "prepare",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := runner.store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runner.watchSteer(ctx, meta.ID)

	first := session.NewSteerRequest("first", true)
	if err := runner.store.AppendSteerRequest(meta.ID, first); err != nil {
		t.Fatalf("append first steer: %v", err)
	}
	if !waitForSteerInterrupt(t, runner.control) {
		t.Fatal("expected first interrupt request to be observed")
	}

	requests, err := runner.store.LoadSteerRequests(meta.ID)
	if err != nil {
		t.Fatalf("load steer requests: %v", err)
	}
	requests[0].Status = session.SteerStatusAccepted
	if err := runner.store.UpdateSteerRequests(meta.ID, requests); err != nil {
		t.Fatalf("update steer requests: %v", err)
	}

	second := session.NewSteerRequest("second", true)
	if err := runner.store.AppendSteerRequest(meta.ID, second); err != nil {
		t.Fatalf("append second steer: %v", err)
	}
	if !waitForSteerInterrupt(t, runner.control) {
		t.Fatal("expected second interrupt request to be observed")
	}

	eventsPath := filepath.Join(runner.store.SessionDir(meta.ID), "events.jsonl")
	data, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if !strings.Contains(string(data), `"type":"session.steer.interrupt_requested"`) {
		t.Fatalf("expected interrupt_requested event in %s, got:\n%s", eventsPath, string(data))
	}
}

func waitForSteerInterrupt(t *testing.T, control *runControl) bool {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if control.consumeSteerInterrupt() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
