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

func TestCustomAnthropicAPIProviderUsesAnthropicAdapter(t *testing.T) {
	var seenPath string
	var seenBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		defer r.Body.Close()
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(data, &seenBody); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_1",
			"stop_reason":"tool_use",
			"content":[
				{"type":"tool_use","id":"toolu_1","name":"finish","input":{"message":"provider probe ok"}}
			],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.DefaultProvider = "deepseek"
	includeThoughts := true
	cfg.Providers["deepseek"] = config.Provider{
		APIProvider:       "anthropic-compatible",
		APIKeyEnv:         "DEEPSEEK_API_KEY",
		BaseURL:           server.URL,
		Model:             "deepseek-chat",
		TimeoutSec:        3,
		ThinkingBudget:    1024,
		IncludeThoughts:   &includeThoughts,
		MaxOutputTokens:   2048,
		AnthropicVersion:  "2023-06-01",
		RequestTimeoutSec: 3,
	}
	t.Setenv("DEEPSEEK_API_KEY", "test-key")

	result, err := NewRunner(cfg).Probe(context.Background(), ProbeRequest{Provider: "deepseek"})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if result.APIProvider != "anthropic-compatible" || result.FinishMessage != "provider probe ok" {
		t.Fatalf("unexpected probe result: %#v", result)
	}
	if seenPath != "/v1/messages" {
		t.Fatalf("expected Anthropic Messages path, got %q", seenPath)
	}
	if seenBody["model"] != "deepseek-chat" || seenBody["input"] != nil {
		t.Fatalf("expected Anthropic request body, got %#v", seenBody)
	}
	thinking, _ := seenBody["thinking"].(map[string]any)
	if thinking["type"] != "enabled" || int(thinking["budget_tokens"].(float64)) != 1024 {
		t.Fatalf("expected configured thinking budget, got %#v", seenBody)
	}
}

func TestProviderOptionsFromConfigIncludesRetryPolicy(t *testing.T) {
	rawSidecar := true
	opts := providerOptionsFromConfig("openai-compatible", config.Provider{
		ReasoningEffort:     "medium",
		TimeoutSec:          90,
		RequestTimeoutSec:   180,
		StreamIdleTimeoutMS: 45000,
		RawSidecar:          &rawSidecar,
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
	wantTimeout := &session.ProviderTimeoutPolicy{
		TimeoutSec:          90,
		RequestTimeoutSec:   180,
		StreamIdleTimeoutMS: 45000,
	}
	if !reflect.DeepEqual(opts.TimeoutPolicy, wantTimeout) {
		t.Fatalf("unexpected timeout policy: %#v", opts.TimeoutPolicy)
	}
	if opts.RawSidecar == nil || !*opts.RawSidecar {
		t.Fatalf("expected raw_sidecar to be recorded in provider options, got %#v", opts.RawSidecar)
	}
}

func TestProviderOptionsFromConfigDefaultsStoreFalseForCustomOpenAICompatible(t *testing.T) {
	opts := providerOptionsFromConfig("gateway", config.Provider{
		APIProvider: "openai-compatible",
		Model:       "gpt-5.4",
	})
	if opts.Store == nil || *opts.Store {
		t.Fatalf("expected custom openai-compatible provider to default store=false, got %#v", opts.Store)
	}

	store := true
	opts = providerOptionsFromConfig("gateway", config.Provider{
		APIProvider: "openai-compatible",
		Model:       "gpt-5.4",
		Store:       &store,
	})
	if opts.Store == nil || !*opts.Store {
		t.Fatalf("expected explicit store=true to be preserved, got %#v", opts.Store)
	}
}

func TestProbeDefaultsStoreFalseForCustomOpenAICompatible(t *testing.T) {
	var seenBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(data, &seenBody); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_probe",
			"status":"completed",
			"output":[{"type":"function_call","call_id":"call_finish","name":"finish","arguments":"{\"message\":\"provider probe ok\"}"}],
			"usage":{"input_tokens":1,"output_tokens":1}
		}`))
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.DefaultProvider = "gateway"
	cfg.Providers["gateway"] = config.Provider{
		APIProvider:       "openai-compatible",
		APIKeyEnv:         "GATEWAY_API_KEY",
		BaseURL:           server.URL + "/v1",
		Model:             "gpt-5.4",
		RequestTimeoutSec: 3,
	}
	t.Setenv("GATEWAY_API_KEY", "test-key")

	result, err := NewRunner(cfg).Probe(context.Background(), ProbeRequest{Provider: "gateway"})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if result.APIProvider != "openai-compatible" {
		t.Fatalf("unexpected probe api provider: %#v", result)
	}
	if seenBody["store"] != false {
		t.Fatalf("expected probe request to include store=false, got %#v", seenBody)
	}
}

func TestApplySessionProviderOptionsRestoresTimeoutPolicy(t *testing.T) {
	cfg := config.Provider{
		TimeoutSec:          10,
		RequestTimeoutSec:   10,
		StreamIdleTimeoutMS: 10000,
	}
	restored := applySessionProviderOptions(cfg, session.ProviderOptions{
		TimeoutPolicy: &session.ProviderTimeoutPolicy{
			TimeoutSec:          30,
			RequestTimeoutSec:   240,
			StreamIdleTimeoutMS: 300000,
		},
	})
	if restored.TimeoutSec != 30 || restored.RequestTimeoutSec != 240 || restored.StreamIdleTimeoutMS != 300000 {
		t.Fatalf("expected durable timeout policy to be restored, got %#v", restored)
	}
}

func TestRunnerRejectsDifferentConcurrentActiveSessionSlot(t *testing.T) {
	runner := NewRunner(config.Default())
	release, err := runner.acquireRunSlot("session_a")
	if err != nil {
		t.Fatalf("acquire first slot: %v", err)
	}
	defer release()

	if _, err := runner.acquireRunSlot("session_b"); err == nil || !strings.Contains(err.Error(), "already has active session session_a") {
		t.Fatalf("expected different active session to be rejected, got %v", err)
	}
	releaseSame, err := runner.acquireRunSlot("session_a")
	if err != nil {
		t.Fatalf("expected same-session nested acquire for auto-continue, got %v", err)
	}
	releaseSame()
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
	rawSidecar := true
	cfg.Providers["openai-compatible"] = config.Provider{
		APIKeyEnv:           "OPENAI_API_KEY",
		BaseURL:             server.URL + "/v1",
		Model:               "gpt-5.4",
		TimeoutSec:          30,
		RequestTimeoutSec:   45,
		StreamIdleTimeoutMS: 60000,
		WireAPI:             "responses",
		Store:               &store,
		SendMetadata:        &sendMetadata,
		RawSidecar:          &rawSidecar,
		ReasoningEffort:     "medium",
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
	if meta.ProviderOptions.RawSidecar == nil || !*meta.ProviderOptions.RawSidecar {
		t.Fatalf("expected raw_sidecar=true in session metadata, got %#v", meta.ProviderOptions.RawSidecar)
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
	wantTimeout := &session.ProviderTimeoutPolicy{
		TimeoutSec:          30,
		RequestTimeoutSec:   45,
		StreamIdleTimeoutMS: 60000,
	}
	if !reflect.DeepEqual(meta.ProviderOptions.TimeoutPolicy, wantTimeout) {
		t.Fatalf("unexpected timeout policy in session metadata: %#v", meta.ProviderOptions.TimeoutPolicy)
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

func TestResolveRequestedWorkdirDefaultsToWorkspaceSubdirAndCreatesIt(t *testing.T) {
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := t.TempDir()
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(originalWD) }()

	got, err := resolveRequestedWorkdir("", nil)
	if err != nil {
		t.Fatalf("resolveRequestedWorkdir: %v", err)
	}
	want := filepath.Join(root, "workspace")
	if got != want {
		t.Fatalf("expected default workspace %q, got %q", want, got)
	}
	info, err := os.Stat(want)
	if err != nil {
		t.Fatalf("expected workspace dir to be created: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected %q to be a directory", want)
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
