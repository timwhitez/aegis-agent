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

func containsToolName(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func hasAnthropicCacheControl(value any) bool {
	obj, ok := value.(map[string]any)
	if !ok {
		return false
	}
	cache, ok := obj["cache_control"].(map[string]any)
	return ok && cache["type"] == "ephemeral"
}

func anthropicProbeSystemHasCacheControl(body map[string]any) bool {
	system, ok := body["system"].([]any)
	if !ok || len(system) == 0 {
		return false
	}
	return hasAnthropicCacheControl(system[0])
}

func anthropicProbeFinalToolHasCacheControl(body map[string]any) bool {
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) == 0 {
		return false
	}
	return hasAnthropicCacheControl(tools[len(tools)-1])
}

func anthropicProbeMessageHasCacheControl(body map[string]any) bool {
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) == 0 {
		return false
	}
	msg, ok := messages[len(messages)-1].(map[string]any)
	if !ok {
		return false
	}
	content, ok := msg["content"].([]any)
	if !ok || len(content) == 0 {
		return false
	}
	return hasAnthropicCacheControl(content[len(content)-1])
}

func openAIRequestToolNames(value any) []string {
	rawTools, ok := value.([]any)
	if !ok {
		return nil
	}
	var names []string
	for _, rawTool := range rawTools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		if name, ok := tool["name"].(string); ok && name != "" {
			names = append(names, name)
			continue
		}
		if fn, ok := tool["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok && name != "" {
				names = append(names, name)
			}
		}
	}
	return names
}

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
	if !anthropicProbeSystemHasCacheControl(seenBody) || !anthropicProbeFinalToolHasCacheControl(seenBody) || !anthropicProbeMessageHasCacheControl(seenBody) {
		t.Fatalf("expected anthropic-compatible probe to send default prompt cache markers, got %#v", seenBody)
	}
}

func TestProbeHonorsPromptCacheFalseForAnthropicCompatible(t *testing.T) {
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
			"id":"msg_1",
			"stop_reason":"tool_use",
			"content":[
				{"type":"tool_use","id":"toolu_1","name":"finish","input":{"message":"provider probe ok"}}
			],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()

	disabled := false
	cfg := config.Default()
	cfg.DefaultProvider = "anthropic-gateway"
	cfg.Providers["anthropic-gateway"] = config.Provider{
		APIProvider:       "anthropic-compatible",
		APIKeyEnv:         "ANTHROPIC_GATEWAY_API_KEY",
		BaseURL:           server.URL,
		Model:             "claude-sonnet-4-6",
		TimeoutSec:        3,
		PromptCache:       &disabled,
		AnthropicVersion:  "2023-06-01",
		RequestTimeoutSec: 3,
	}
	t.Setenv("ANTHROPIC_GATEWAY_API_KEY", "test-key")

	if _, err := NewRunner(cfg).Probe(context.Background(), ProbeRequest{Provider: "anthropic-gateway"}); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if anthropicProbeSystemHasCacheControl(seenBody) || anthropicProbeFinalToolHasCacheControl(seenBody) || anthropicProbeMessageHasCacheControl(seenBody) {
		t.Fatalf("expected prompt_cache=false probe to omit cache markers, got %#v", seenBody)
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

func TestProviderOptionsFromConfigDefaultsPromptCacheForAnthropicCompatible(t *testing.T) {
	opts := providerOptionsFromConfig("anthropic", config.Provider{
		APIProvider: "anthropic-compatible",
		Model:       "claude-sonnet-4-6",
	})
	if opts.PromptCache == nil || !*opts.PromptCache {
		t.Fatalf("expected anthropic-compatible prompt_cache default true, got %#v", opts.PromptCache)
	}

	disabled := false
	opts = providerOptionsFromConfig("anthropic", config.Provider{
		APIProvider: "anthropic-compatible",
		Model:       "claude-sonnet-4-6",
		PromptCache: &disabled,
	})
	if opts.PromptCache == nil || *opts.PromptCache {
		t.Fatalf("expected explicit prompt_cache=false to be preserved, got %#v", opts.PromptCache)
	}

	openai := providerOptionsFromConfig("openai-compatible", config.Provider{
		APIProvider: "openai-compatible",
		Model:       "gpt-5.4",
	})
	if openai.PromptCache != nil {
		t.Fatalf("expected openai-compatible prompt_cache to rely on provider default, got %#v", openai.PromptCache)
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

func TestRunnerStartReportsStartedEventAppendError(t *testing.T) {
	var providerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_started_event",
			"status":"completed",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"should not run"}]}],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	cfg.DefaultProvider = "openai-compatible"
	cfg.Providers["openai-compatible"] = config.Provider{
		APIProvider: "openai-compatible",
		APIKeyEnv:   "OPENAI_API_KEY",
		BaseURL:     server.URL + "/v1",
		Model:       "gpt-5.4",
		TimeoutSec:  30,
		WireAPI:     "responses",
	}
	t.Setenv("OPENAI_API_KEY", "test-key")
	runner := NewRunner(cfg)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai-compatible",
		Model:            "gpt-5.4",
		CompletionPolicy: completionPolicy(session.ModeRun),
		ProviderOptions:  providerOptionsFromConfig("openai-compatible", cfg.Providers["openai-compatible"]),
	}
	state := session.State{
		Status:    session.StatusRunning,
		Phase:     "prepare",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := runner.store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	eventsPath := filepath.Join(runner.store.SessionDir(meta.ID), "events.jsonl")
	if err := os.Remove(eventsPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove events: %v", err)
	}
	if err := os.Mkdir(eventsPath, 0o700); err != nil {
		t.Fatalf("block events path: %v", err)
	}

	result, err := runner.runExisting(context.Background(), meta, state, "", nil)
	if err == nil {
		t.Fatalf("expected session.started event append error, got result=%#v", result)
	}
	if !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected events append error with path context, got %v", err)
	}
	if !strings.Contains(err.Error(), "session.started") {
		t.Fatalf("expected started event context, got %v", err)
	}
	if calls := providerCalls.Load(); calls != 0 {
		t.Fatalf("provider should not run after missing started event, got %d calls", calls)
	}
}

func TestRunnerStartGoalPlanApprovalCreatesLinkedPlanModeGate(t *testing.T) {
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
		toolNames := openAIRequestToolNames(body["tools"])
		if !containsToolName(toolNames, "submit_plan") || !containsToolName(toolNames, "get_plan_mode") {
			t.Fatalf("expected Plan Mode tools in provider request, got %#v", toolNames)
		}
		if containsToolName(toolNames, "shell") || containsToolName(toolNames, "write_file") {
			t.Fatalf("mutating tools leaked into mission planning request: %#v", toolNames)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_1",
			"status":"completed",
			"output":[
				{
					"type":"function_call",
					"call_id":"call_submit",
					"name":"submit_plan",
					"arguments":"{\"title\":\"Mission plan\",\"summary\":\"Plan before changes.\",\"plan_markdown\":\"# Plan\\n\\nPlan before changes.\\n\\n# Verification\\n\\nRun tests.\",\"verification\":[\"go test ./internal/runtime\"]}"
				}
			],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	cfg.DefaultProvider = "openai-compatible"
	cfg.Providers["openai-compatible"] = config.Provider{
		APIKeyEnv:         "OPENAI_API_KEY",
		BaseURL:           server.URL + "/v1",
		Model:             "gpt-5.4",
		TimeoutSec:        30,
		RequestTimeoutSec: 45,
		WireAPI:           "responses",
	}
	t.Setenv("OPENAI_API_KEY", "test-key")

	runner := NewRunner(cfg)
	result, err := runner.Start(context.Background(), StartRequest{
		Prompt:   "Plan this mission before editing.",
		Provider: "openai-compatible",
		Workdir:  t.TempDir(),
		Mode:     session.ModeExec,
		Goal: &session.GoalDraft{
			Enabled:             true,
			Mode:                session.GoalModeMission,
			Objective:           "Plan-gated mission",
			RequirePlanApproval: true,
			Source:              session.GoalSourceCLI,
		},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if result.Status != session.StatusAwaitingInput {
		t.Fatalf("expected awaiting input after submitted plan, got %#v", result)
	}
	goal, err := runner.store.LoadGoal(result.SessionID)
	if err != nil {
		t.Fatalf("load goal: %v", err)
	}
	planMode, err := runner.store.LoadPlanMode(result.SessionID)
	if err != nil {
		t.Fatalf("load plan mode: %v", err)
	}
	if planMode.LinkedGoalID != goal.GoalID || planMode.Status != session.PlanModeStatusAwaitingApproval {
		t.Fatalf("expected linked awaiting approval plan mode, goal=%#v plan=%#v", goal, planMode)
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

func TestRunnerContinueClaimsSessionBeforeUserMessageHook(t *testing.T) {
	root := t.TempDir()
	hookStarted := filepath.Join(root, "hook-started")
	hookRelease := filepath.Join(root, "hook-release")
	defer func() { _ = os.WriteFile(hookRelease, []byte("1"), 0o600) }()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_1",
			"status":"completed",
			"output":[
				{"type":"function_call","call_id":"call_finish_1","name":"finish","arguments":"{\"message\":\"continued\"}"}
			],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()

	newConfig := func() *config.Config {
		cfg := config.Default()
		cfg.Session.Dir = root
		cfg.DefaultProvider = "openai-compatible"
		cfg.Providers["openai-compatible"] = config.Provider{
			APIKeyEnv:  "OPENAI_API_KEY",
			BaseURL:    server.URL + "/v1",
			Model:      "gpt-5.4",
			TimeoutSec: 30,
			WireAPI:    "responses",
		}
		return cfg
	}
	cfg1 := newConfig()
	cfg1.Hooks.UserMessage = []config.HookDefinition{{
		Name:    "block-user-message",
		Command: []string{"/bin/sh", "-c", "touch \"$1\"; while [ ! -f \"$2\" ]; do sleep 0.01; done", "block-user-message", hookStarted, hookRelease},
	}}
	cfg2 := newConfig()
	t.Setenv("OPENAI_API_KEY", "test-key")

	runner1 := NewRunner(cfg1)
	runner2 := NewRunner(cfg2)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "openai-compatible",
		Model:            "gpt-5.4",
		CompletionPolicy: completionPolicy(session.ModeExec),
		ProviderOptions:  providerOptionsFromConfig("openai-compatible", cfg1.Providers["openai-compatible"]),
	}
	state := session.State{
		Status:    session.StatusAwaitingInput,
		Phase:     "turn_decide",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := runner1.store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	type continueOutcome struct {
		result RunResult
		err    error
	}
	firstDone := make(chan continueOutcome, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		result, err := runner1.Continue(ctx, ContinueRequest{SessionID: meta.ID, Message: "first continuation"})
		firstDone <- continueOutcome{result: result, err: err}
	}()

	waitForPath(t, hookStarted, 2*time.Second)
	secondResult, secondErr := runner2.Continue(context.Background(), ContinueRequest{SessionID: meta.ID, Message: "second continuation"})
	if secondErr == nil || !strings.Contains(secondErr.Error(), "session is not resumable") {
		t.Fatalf("expected concurrent same-session continue rejection, result=%#v err=%v", secondResult, secondErr)
	}
	if err := os.WriteFile(hookRelease, []byte("1"), 0o600); err != nil {
		t.Fatalf("release hook: %v", err)
	}
	select {
	case outcome := <-firstDone:
		if outcome.err != nil {
			t.Fatalf("first continue: %v", outcome.err)
		}
		if outcome.result.Status != session.StatusCompleted {
			t.Fatalf("expected first continue to complete, got %#v", outcome.result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first continue")
	}
	if requests.Load() != 1 {
		t.Fatalf("expected one provider request after rejecting concurrent continue, got %d", requests.Load())
	}
	messages, err := runner1.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	var firstMessages, secondMessages int
	for _, msg := range messages {
		if msg.Role != "user" {
			continue
		}
		if msg.Text == "first continuation" {
			firstMessages++
		}
		if msg.Text == "second continuation" {
			secondMessages++
		}
	}
	if firstMessages != 1 || secondMessages != 0 {
		t.Fatalf("expected only the claimed continuation message, first=%d second=%d messages=%#v", firstMessages, secondMessages, messages)
	}
}

func TestRunnerFailBeforeRunReportsStateSaveError(t *testing.T) {
	root := t.TempDir()
	sessionID := session.NewSessionID()
	statePath := filepath.Join(root, sessionID, "state.json")
	cfg := config.Default()
	cfg.Session.Dir = root
	cfg.DefaultProvider = "openai-compatible"
	cfg.Providers["openai-compatible"] = config.Provider{
		APIProvider: "openai-compatible",
		APIKeyEnv:   "OPENAI_API_KEY",
		BaseURL:     "http://127.0.0.1:1/v1",
		Model:       "gpt-5.4",
		TimeoutSec:  30,
		WireAPI:     "responses",
	}
	cfg.Hooks.UserMessage = []config.HookDefinition{{
		Name:       "block-state-then-fail",
		FailClosed: true,
		Command:    []string{"/bin/sh", "-c", "rm -f \"$1\" && mkdir \"$1\" && exit 1", "block-state-then-fail", statePath},
	}}
	runner := NewRunner(cfg)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               sessionID,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
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

	result, err := runner.Continue(context.Background(), ContinueRequest{SessionID: meta.ID, Message: "reject me"})
	if err == nil {
		t.Fatalf("expected failure state write error, got result=%#v", result)
	}
	if !strings.Contains(err.Error(), "state.json") {
		t.Fatalf("expected state write error with path context, got %v", err)
	}
}

func TestRunnerFailBeforeRunReportsFailedEventAppendError(t *testing.T) {
	root := t.TempDir()
	sessionID := session.NewSessionID()
	eventsPath := filepath.Join(root, sessionID, "events.jsonl")
	cfg := config.Default()
	cfg.Session.Dir = root
	cfg.DefaultProvider = "openai-compatible"
	cfg.Providers["openai-compatible"] = config.Provider{
		APIProvider: "openai-compatible",
		APIKeyEnv:   "OPENAI_API_KEY",
		BaseURL:     "http://127.0.0.1:1/v1",
		Model:       "gpt-5.4",
		TimeoutSec:  30,
		WireAPI:     "responses",
	}
	cfg.Hooks.UserMessage = []config.HookDefinition{{
		Name:       "block-events-then-fail",
		FailClosed: true,
		Command:    []string{"/bin/sh", "-c", "rm -f \"$1\" && mkdir \"$1\" && exit 1", "block-events-then-fail", eventsPath},
	}}
	runner := NewRunner(cfg)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               sessionID,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
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

	result, err := runner.Continue(context.Background(), ContinueRequest{SessionID: meta.ID, Message: "reject me"})
	if err == nil {
		t.Fatalf("expected failed event append error, got result=%#v", result)
	}
	if !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected events append error with path context, got %v", err)
	}
	if !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("expected original hook failure context, got %v", err)
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

func TestRunnerAppendUserMessageReportsEventAppendErrorAndRollsBackMessage(t *testing.T) {
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
	eventsPath := filepath.Join(runner.store.SessionDir(meta.ID), "events.jsonl")
	if err := os.Remove(eventsPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove events: %v", err)
	}
	if err := os.Mkdir(eventsPath, 0o700); err != nil {
		t.Fatalf("block events path: %v", err)
	}

	err := runner.appendUserMessage(context.Background(), meta, "prepare", "hello", nil)
	if err == nil {
		t.Fatal("expected user.message event append error")
	}
	if !strings.Contains(err.Error(), "user.message") {
		t.Fatalf("expected user.message event context, got %v", err)
	}
	if !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected events append error with path context, got %v", err)
	}
	messages, loadErr := runner.store.LoadMessages(meta.ID)
	if loadErr != nil {
		t.Fatalf("load messages: %v", loadErr)
	}
	if len(messages) != 0 {
		t.Fatalf("event append failure should roll back user message, got %#v", messages)
	}
}

func TestRunnerContinueReportsCheckpointResumeHintEventAppendError(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	runner := NewRunner(cfg)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: completionPolicy(session.ModeExec),
		ProviderOptions:  providerOptionsFromConfig("openai", cfg.Providers["openai"]),
	}
	state := session.State{
		Status:    session.StatusAwaitingInput,
		Phase:     "turn_decide",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := runner.store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := runner.store.SaveLongRunCheckpoint(meta.ID, session.LongRunCheckpoint{
		SchemaVersion:    1,
		SessionID:        meta.ID,
		RootSessionID:    meta.ID,
		Provider:         meta.Provider,
		Model:            meta.Model,
		Workdir:          meta.Workdir,
		RequestedWorkdir: meta.RequestedWorkdir,
		ResumeHints:      []string{"resume from checkpoint"},
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("save checkpoint: %v", err)
	}
	eventsPath := filepath.Join(runner.store.SessionDir(meta.ID), "events.jsonl")
	if err := os.Remove(eventsPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove events: %v", err)
	}
	if err := os.Mkdir(eventsPath, 0o700); err != nil {
		t.Fatalf("block events path: %v", err)
	}

	result, err := runner.Continue(context.Background(), ContinueRequest{SessionID: meta.ID})
	if err == nil {
		t.Fatalf("expected checkpoint resume hint event append error, got result=%#v", result)
	}
	if !strings.Contains(err.Error(), "checkpoint.resume_hint.injected") {
		t.Fatalf("expected checkpoint resume hint event context, got %v", err)
	}
	if !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected events append error with path context, got %v", err)
	}
	messages, loadErr := runner.store.LoadMessages(meta.ID)
	if loadErr != nil {
		t.Fatalf("load messages: %v", loadErr)
	}
	for _, msg := range messages {
		if msg.Meta != nil && msg.Meta["kind"] == "longrun_checkpoint" {
			t.Fatalf("event append failure should roll back checkpoint resume hint, got %#v", msg)
		}
	}
}

func TestRunnerContinueKeepsDurableTurnAndResetsRunBudgetAfterMaxTurnsFailure(t *testing.T) {
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

	rawSidecar := true
	cfg.Providers["openai-compatible"] = config.Provider{
		APIKeyEnv:  "OPENAI_API_KEY",
		BaseURL:    server.URL + "/v1",
		Model:      "gpt-5.4",
		TimeoutSec: 30,
		WireAPI:    "responses",
		RawSidecar: &rawSidecar,
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
	if loaded.Turn != 42 {
		t.Fatalf("expected resumed run to keep monotonic durable turn while restarting run budget, got turn=%d", loaded.Turn)
	}
	if loaded.LastError != "" {
		t.Fatalf("expected continue to clear stale last_error, got %q", loaded.LastError)
	}
	sidecar, err := runner.store.LoadProviderRawSidecar(meta.ID, 42)
	if err != nil {
		t.Fatalf("expected resumed run to write monotonic raw sidecar: %v", err)
	}
	if sidecar.Turn != 42 {
		t.Fatalf("expected raw sidecar turn 42, got %#v", sidecar)
	}
}

func TestRunnerContinueProviderOverrideUsesNewProviderDefaultModel(t *testing.T) {
	var seenModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
		if model, _ := body["model"].(string); model != "" {
			seenModel = model
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_1",
			"stop_reason":"tool_use",
			"content":[{"type":"tool_use","id":"toolu_1","name":"finish","input":{"message":"done"}}],
			"usage":{"input_tokens":1,"output_tokens":1}
		}`))
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	cfg.Providers["anthropic"] = config.Provider{
		APIProvider:      "anthropic-compatible",
		APIKeyEnv:        "ANTHROPIC_API_KEY",
		BaseURL:          server.URL,
		Model:            "claude-new-default",
		TimeoutSec:       30,
		AnthropicVersion: "2023-06-01",
	}
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	runner := NewRunner(cfg)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "openai-compatible",
		Model:            "gpt-old",
		CompletionPolicy: completionPolicy(session.ModeExec),
	}
	state := session.State{Status: session.StatusAwaitingInput, Phase: "turn_decide", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := runner.store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := runner.store.AppendMessage(meta.ID, session.NewMessage("user", "continue")); err != nil {
		t.Fatalf("append: %v", err)
	}
	result, err := runner.Continue(context.Background(), ContinueRequest{SessionID: meta.ID, Provider: "anthropic"})
	if err != nil {
		t.Fatalf("continue: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("expected completed result, got %#v", result)
	}
	loadedMeta, err := runner.store.LoadMetadata(meta.ID)
	if err != nil {
		t.Fatalf("load metadata: %v", err)
	}
	if loadedMeta.Provider != "anthropic" || loadedMeta.Model != "claude-new-default" || seenModel != "claude-new-default" {
		t.Fatalf("expected new provider default model, meta=%#v seen=%q", loadedMeta, seenModel)
	}
}

func TestAutoContinuePersistsRalphLoopCountBeforeResume(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_1",
			"status":"completed",
			"output":[{"type":"function_call","call_id":"call_1","name":"finish","arguments":"{\"message\":\"done\"}"}],
			"usage":{"input_tokens":1,"output_tokens":1}
		}`))
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	cfg.DefaultProvider = "openai-compatible"
	cfg.Runtime.RalphLoop.MaxIterations = 2
	cfg.Providers["openai-compatible"] = config.Provider{APIProvider: "openai-compatible", APIKeyEnv: "OPENAI_API_KEY", BaseURL: server.URL + "/v1", Model: "gpt-5.4", TimeoutSec: 30, WireAPI: "responses"}
	t.Setenv("OPENAI_API_KEY", "test-key")
	runner := NewRunner(cfg)
	meta := session.SessionMetadata{SchemaVersion: 1, ID: session.NewSessionID(), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: t.TempDir(), Mode: session.ModeExec, Provider: "openai-compatible", Model: "gpt-5.4", CompletionPolicy: completionPolicy(session.ModeExec), ProviderOptions: providerOptionsFromConfig("openai-compatible", cfg.Providers["openai-compatible"])}
	state := session.State{Status: session.StatusFailed, Phase: "turn_decide", IncompleteReason: "incomplete_no_finish", LastError: "incomplete_no_finish", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := runner.store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := runner.store.AppendMessage(meta.ID, session.NewMessage("user", "finish")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := runner.AutoContinue(context.Background(), meta.ID); err != nil {
		t.Fatalf("auto continue: %v", err)
	}
	loaded, err := runner.store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if loaded.RalphLoopCount != 1 {
		t.Fatalf("expected persisted Ralph loop count, got %#v", loaded)
	}
}

func TestResolveRequestedWorkdirRejectsDefaultWorkspaceSymlink(t *testing.T) {
	cwd := t.TempDir()
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldCwd)
	if err := os.Symlink(t.TempDir(), filepath.Join(cwd, defaultWorkspaceDirName)); err != nil {
		t.Fatalf("symlink workspace: %v", err)
	}
	if _, err := resolveRequestedWorkdir("", nil); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected default workspace symlink rejection, got %v", err)
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

func TestRunnerSteerReportsCorruptMetadataBeforeQueueing(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(runner.store.SessionDir(meta.ID), "session.json"), []byte("{"), 0o600); err != nil {
		t.Fatalf("corrupt session metadata: %v", err)
	}

	result, err := runner.Steer(context.Background(), SteerRequest{
		SessionID: meta.ID,
		Message:   "focus on tests",
		Interrupt: true,
	})
	if err == nil || !strings.Contains(err.Error(), "session.json") {
		t.Fatalf("expected corrupt metadata error, result=%#v err=%v", result, err)
	}
	requests, err := runner.store.LoadSteerRequests(meta.ID)
	if err != nil {
		t.Fatalf("load steer requests: %v", err)
	}
	if len(requests) != 0 {
		t.Fatalf("corrupt metadata should not queue steer request, got %#v", requests)
	}
	events, err := runner.store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("corrupt metadata should not emit steer events, got %#v", events)
	}
}

func TestRunnerSteerReportsQueuedEventAppendError(t *testing.T) {
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
	eventsPath := filepath.Join(runner.store.SessionDir(meta.ID), "events.jsonl")
	if err := os.Remove(eventsPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove events: %v", err)
	}
	if err := os.Mkdir(eventsPath, 0o700); err != nil {
		t.Fatalf("block events path: %v", err)
	}
	result, err := runner.Steer(context.Background(), SteerRequest{
		SessionID: meta.ID,
		Message:   "focus on tests",
		Interrupt: true,
	})
	if err == nil {
		t.Fatalf("expected steer queued event append error, got result=%#v", result)
	}
	if !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected events append error with path context, got %v", err)
	}
	requests, err := runner.store.LoadSteerRequests(meta.ID)
	if err != nil {
		t.Fatalf("load steer requests: %v", err)
	}
	if len(requests) != 1 || requests[0].Status != session.SteerStatusRejected {
		t.Fatalf("event append failure should reject durable steer request, got %#v", requests)
	}
	loadedState, err := runner.store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if loadedState.PendingSteerCount != 0 {
		t.Fatalf("event append failure should refresh pending steer count, got %#v", loadedState)
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

func TestRunnerWatchSteerRequiresInterruptRequestedEventBeforeSignal(t *testing.T) {
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
	request := session.NewSteerRequest("interrupt", true)
	if err := runner.store.AppendSteerRequest(meta.ID, request); err != nil {
		t.Fatalf("append steer: %v", err)
	}
	eventsPath := filepath.Join(runner.store.SessionDir(meta.ID), "events.jsonl")
	if err := os.Remove(eventsPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove events: %v", err)
	}
	if err := os.Mkdir(eventsPath, 0o700); err != nil {
		t.Fatalf("block events path: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go runner.watchSteer(ctx, meta.ID)
	time.Sleep(50 * time.Millisecond)
	if runner.control.consumeSteerInterrupt() {
		t.Fatal("watcher should not signal interrupt when interrupt_requested event cannot be persisted")
	}
	cancel()

	if err := os.Remove(eventsPath); err != nil {
		t.Fatalf("remove blocked events path: %v", err)
	}
	if err := os.WriteFile(eventsPath, nil, 0o600); err != nil {
		t.Fatalf("restore events path: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	go runner.watchSteer(ctx, meta.ID)
	if !waitForSteerInterrupt(t, runner.control) {
		t.Fatal("expected interrupt after interrupt_requested event can be persisted")
	}
	eventsData, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if !strings.Contains(string(eventsData), `"type":"session.steer.interrupt_requested"`) {
		t.Fatalf("expected interrupt_requested event after restore, got:\n%s", string(eventsData))
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

func waitForPath(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
