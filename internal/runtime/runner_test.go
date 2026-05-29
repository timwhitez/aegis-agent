package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
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
	"go-cli-agent/internal/events"
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

func TestRunnerTasksRejectsUnknownSession(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = filepath.Join(t.TempDir(), "sessions")
	runner := NewRunner(cfg)

	if _, err := runner.Tasks("missing_session_tasks"); err == nil || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected missing session metadata error, got %v", err)
	}
}

func TestRunnerStateRejectsOrphanStateWithoutSessionMetadata(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = filepath.Join(t.TempDir(), "sessions")
	runner := NewRunner(cfg)
	sessionID := "session_orphan_state_runtime"
	sessionDir := runner.store.SessionDir(sessionID)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	state := session.State{
		Status:    session.StatusRunning,
		Phase:     "prepare",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "state.json"), data, 0o600); err != nil {
		t.Fatalf("write orphan state: %v", err)
	}

	loaded, err := runner.State(sessionID)
	if err == nil {
		t.Fatalf("expected missing session metadata error, loaded %#v", loaded)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected missing session metadata error, got %v", err)
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

func TestResolvedProviderOptionsRejectsUnsupportedAPIProviderOverride(t *testing.T) {
	cfg := config.Provider{
		APIProvider: "openai-compatible",
		BaseURL:     "http://provider.invalid/v1",
		Model:       "gpt-5.4",
	}
	_, err := resolvedProviderOptions("openai-compatible", cfg, session.ProviderOptions{
		APIProvider: "not-real",
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported api_provider") {
		t.Fatalf("expected unsupported api_provider error, got %v", err)
	}
}

func TestResolvedProviderOptionsRejectsUnsupportedConfigAPIProvider(t *testing.T) {
	cfg := config.Provider{
		APIProvider: "not-real",
		BaseURL:     "http://provider.invalid/v1",
		Model:       "gpt-5.4",
	}
	_, err := resolvedProviderOptions("bad-provider", cfg, session.ProviderOptions{})
	if err == nil || !strings.Contains(err.Error(), "unsupported api_provider") {
		t.Fatalf("expected unsupported api_provider error, got %v", err)
	}
}

func TestRunnerStartRejectsUnsupportedProviderOptionsAPIProviderBeforeCreate(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	cfg.DefaultProvider = "openai-compatible"
	cfg.Providers["openai-compatible"] = config.Provider{
		APIProvider: "openai-compatible",
		APIKeyEnv:   "OPENAI_API_KEY",
		BaseURL:     "http://provider.invalid/v1",
		Model:       "gpt-5.4",
		WireAPI:     "responses",
	}
	runner := NewRunner(cfg)

	_, err := runner.Start(context.Background(), StartRequest{
		Prompt: "should not create a session",
		Mode:   session.ModeExec,
		ProviderOptions: session.ProviderOptions{
			APIProvider: "not-real",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported api_provider") {
		t.Fatalf("expected unsupported api_provider error, got %v", err)
	}
	sessions, listErr := runner.store.List(10)
	if listErr != nil {
		t.Fatalf("list sessions: %v", listErr)
	}
	if len(sessions) != 0 {
		t.Fatalf("unsupported provider options should not create sessions, got %#v", sessions)
	}
}

func TestRunnerStartRejectsUnsupportedProviderConfigAPIProviderBeforeCreate(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	cfg.DefaultProvider = "bad-provider"
	cfg.Providers["bad-provider"] = config.Provider{
		APIProvider: "not-real",
		APIKeyEnv:   "OPENAI_API_KEY",
		BaseURL:     "http://provider.invalid/v1",
		Model:       "gpt-5.4",
	}
	runner := NewRunner(cfg)

	_, err := runner.Start(context.Background(), StartRequest{
		Prompt: "should not create a session",
		Mode:   session.ModeExec,
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported api_provider") {
		t.Fatalf("expected unsupported api_provider error, got %v", err)
	}
	sessions, listErr := runner.store.List(10)
	if listErr != nil {
		t.Fatalf("list sessions: %v", listErr)
	}
	if len(sessions) != 0 {
		t.Fatalf("unsupported provider config should not create sessions, got %#v", sessions)
	}
}

func TestRunnerContinueRejectsUnsupportedProviderConfigBeforeMetadataMutation(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	cfg.DefaultProvider = "openai-compatible"
	cfg.Providers["openai-compatible"] = config.Provider{
		APIProvider: "openai-compatible",
		APIKeyEnv:   "OPENAI_API_KEY",
		BaseURL:     "http://provider.invalid/v1",
		Model:       "gpt-5.4",
		WireAPI:     "responses",
	}
	cfg.Providers["bad-provider"] = config.Provider{
		APIProvider: "not-real",
		APIKeyEnv:   "OPENAI_API_KEY",
		BaseURL:     "http://bad-provider.invalid/v1",
		Model:       "bad-model",
	}
	runner := NewRunner(cfg)
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
		ProviderOptions:  providerOptionsFromConfig("openai-compatible", cfg.Providers["openai-compatible"]),
	}
	if err := runner.store.Create(meta, session.State{
		Status:    session.StatusAwaitingInput,
		Phase:     "awaiting_input",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err := runner.Continue(context.Background(), ContinueRequest{
		SessionID: meta.ID,
		Provider:  "bad-provider",
		Message:   "resume",
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported api_provider") {
		t.Fatalf("expected unsupported api_provider error, got %v", err)
	}
	loadedMeta, loadErr := runner.store.LoadMetadata(meta.ID)
	if loadErr != nil {
		t.Fatalf("load metadata: %v", loadErr)
	}
	if loadedMeta.Provider != "openai-compatible" || loadedMeta.ProviderOptions.APIProvider != "openai-compatible" {
		t.Fatalf("unsupported continue provider should not mutate metadata, got %#v", loadedMeta)
	}
	loadedState, stateErr := runner.store.LoadState(meta.ID)
	if stateErr != nil {
		t.Fatalf("load state: %v", stateErr)
	}
	if loadedState.Status != session.StatusAwaitingInput {
		t.Fatalf("unsupported continue provider should leave state awaiting_input, got %#v", loadedState)
	}
}

func TestRunnerContinueRejectsStoredUnsupportedProviderOptionsBeforeClaim(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	cfg.DefaultProvider = "openai-compatible"
	cfg.Providers["openai-compatible"] = config.Provider{
		APIProvider: "openai-compatible",
		APIKeyEnv:   "OPENAI_API_KEY",
		BaseURL:     "http://provider.invalid/v1",
		Model:       "gpt-5.4",
		WireAPI:     "responses",
	}
	runner := NewRunner(cfg)
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
		ProviderOptions: session.ProviderOptions{
			APIProvider: "not-real",
		},
	}
	if err := runner.store.Create(meta, session.State{
		Status:    session.StatusAwaitingInput,
		Phase:     "awaiting_input",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err := runner.Continue(context.Background(), ContinueRequest{
		SessionID: meta.ID,
		Message:   "resume",
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported api_provider") {
		t.Fatalf("expected unsupported api_provider error, got %v", err)
	}
	loadedState, stateErr := runner.store.LoadState(meta.ID)
	if stateErr != nil {
		t.Fatalf("load state: %v", stateErr)
	}
	if loadedState.Status != session.StatusAwaitingInput {
		t.Fatalf("stored bad provider options should not claim or fail the session, got %#v", loadedState)
	}
	messages, msgErr := runner.store.LoadMessages(meta.ID)
	if msgErr != nil {
		t.Fatalf("load messages: %v", msgErr)
	}
	if len(messages) != 0 {
		t.Fatalf("stored bad provider options should reject before appending resume message, got %#v", messages)
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

func TestRunnerStartRejectsUnsupportedRunModeBeforeCreatingSession(t *testing.T) {
	cfg := testRuntimeConfig(t)
	runner := NewRunner(cfg)

	result, err := runner.Start(context.Background(), StartRequest{
		Prompt:        "finish this",
		Workdir:       t.TempDir(),
		Mode:          "sideways",
		IsolationMode: "off",
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported run mode") {
		t.Fatalf("expected unsupported run mode error, got result=%#v err=%v", result, err)
	}
	if result.SessionID != "" {
		t.Fatalf("invalid mode should not create a session, got %#v", result)
	}
	sessions, listErr := runner.store.List(10)
	if listErr != nil {
		t.Fatalf("list sessions: %v", listErr)
	}
	if len(sessions) != 0 {
		t.Fatalf("invalid mode should not persist sessions, got %#v", sessions)
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

func TestRunnerStartReportsCreatedEventAppendError(t *testing.T) {
	var providerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_created_event",
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

	req := StartRequest{
		Prompt:   "start should stop when session.created cannot be recorded",
		Mode:     session.ModeRun,
		Provider: "openai-compatible",
		Model:    "gpt-5.4",
		Workdir:  t.TempDir(),
	}
	runner.beforeStartSessionCreatedEvent = func(sessionID string) {
		blockRunnerEventsPath(t, runner.store, sessionID)
	}
	result, err := runner.Start(context.Background(), req)
	if err == nil {
		t.Fatalf("expected session.created event append error, got result=%#v", result)
	}
	if !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected events append error with path context, got %v", err)
	}
	if !strings.Contains(err.Error(), "session.created") {
		t.Fatalf("expected created event context, got %v", err)
	}
	if calls := providerCalls.Load(); calls != 0 {
		t.Fatalf("provider should not run after missing created event, got %d calls", calls)
	}
}

func TestRunnerStartGoalCreatedEventAppendErrorRestoresGoal(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	runner := NewRunner(cfg)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: completionPolicy(session.ModeRun),
	}
	state := session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := runner.store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	previousHistory, err := runner.store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load previous goal history: %v", err)
	}
	previousTasks, err := runner.store.ListTasks(meta.ID)
	if err != nil {
		t.Fatalf("load previous tasks: %v", err)
	}
	blockRunnerEventsPath(t, runner.store, meta.ID)

	err = runner.initializeStartGoalAndPlanMode(meta.ID, StartRequest{
		Goal: &session.GoalDraft{
			Enabled:   true,
			Objective: "Start with a durable goal",
			Source:    session.GoalSourceCLI,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "goal.created") || !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected goal.created events.jsonl error, got %v", err)
	}
	if _, err := runner.store.LoadGoal(meta.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected failed start goal event to remove goal snapshot, got %v", err)
	}
	history, err := runner.store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load restored goal history: %v", err)
	}
	if len(history) != len(previousHistory) {
		t.Fatalf("expected goal history restored to %d entries, got %d: %#v", len(previousHistory), len(history), history)
	}
	tasks, err := runner.store.ListTasks(meta.ID)
	if err != nil {
		t.Fatalf("load restored tasks: %v", err)
	}
	if len(tasks) != len(previousTasks) {
		t.Fatalf("expected tasks restored to %d entries, got %d: %#v", len(previousTasks), len(tasks), tasks)
	}
}

func TestRunnerStartLinkedPlanModeCreatedEventAppendErrorRestoresGoalAndPlanMode(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	runner := NewRunner(cfg)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: completionPolicy(session.ModeRun),
	}
	state := session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := runner.store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	previousGoalHistory, err := runner.store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load previous goal history: %v", err)
	}
	previousTasks, err := runner.store.ListTasks(meta.ID)
	if err != nil {
		t.Fatalf("load previous tasks: %v", err)
	}
	previousPlanMode, err := runner.store.SnapshotPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("snapshot previous plan mode: %v", err)
	}
	previousPlanHistory, err := runner.store.LoadPlanModeHistory(meta.ID)
	if err != nil {
		t.Fatalf("load previous plan mode history: %v", err)
	}
	blockRunnerEventsPath(t, runner.store, meta.ID)

	err = runner.initializeStartGoalAndPlanMode(meta.ID, StartRequest{
		Goal: &session.GoalDraft{
			Enabled:             true,
			Mode:                session.GoalModeMission,
			Objective:           "Plan-gated start goal",
			RequirePlanApproval: true,
			Source:              session.GoalSourceCLI,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "goal.created") || !strings.Contains(err.Error(), "planmode.created") || !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected goal/planmode created events.jsonl error, got %v", err)
	}
	if _, err := runner.store.LoadGoal(meta.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected failed linked Plan Mode event to remove goal snapshot, got %v", err)
	}
	restoredPlanMode, err := runner.store.SnapshotPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("snapshot restored plan mode: %v", err)
	}
	if restoredPlanMode.HasState != previousPlanMode.HasState || restoredPlanMode.State.PlanModeID != previousPlanMode.State.PlanModeID {
		t.Fatalf("expected previous Plan Mode snapshot restored, before=%#v after=%#v", previousPlanMode, restoredPlanMode)
	}
	goalHistory, err := runner.store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load restored goal history: %v", err)
	}
	if len(goalHistory) != len(previousGoalHistory) {
		t.Fatalf("expected goal history restored to %d entries, got %d: %#v", len(previousGoalHistory), len(goalHistory), goalHistory)
	}
	tasks, err := runner.store.ListTasks(meta.ID)
	if err != nil {
		t.Fatalf("load restored tasks: %v", err)
	}
	if len(tasks) != len(previousTasks) {
		t.Fatalf("expected tasks restored to %d entries, got %d: %#v", len(previousTasks), len(tasks), tasks)
	}
	planHistory, err := runner.store.LoadPlanModeHistory(meta.ID)
	if err != nil {
		t.Fatalf("load restored plan mode history: %v", err)
	}
	if len(planHistory) != len(previousPlanHistory) {
		t.Fatalf("expected plan mode history restored to %d entries, got %d: %#v", len(previousPlanHistory), len(planHistory), planHistory)
	}
}

func TestRunnerStartExplicitPlanModeCreatedEventAppendErrorRestoresPlanMode(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	runner := NewRunner(cfg)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: completionPolicy(session.ModeRun),
	}
	state := session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := runner.store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	previousPlanMode, err := runner.store.SnapshotPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("snapshot previous plan mode: %v", err)
	}
	previousPlanHistory, err := runner.store.LoadPlanModeHistory(meta.ID)
	if err != nil {
		t.Fatalf("load previous plan mode history: %v", err)
	}
	blockRunnerEventsPath(t, runner.store, meta.ID)

	err = runner.initializeStartGoalAndPlanMode(meta.ID, StartRequest{
		Prompt: "Plan this session before changes.",
		PlanMode: &session.PlanModeDraft{
			Enabled: true,
			Source:  session.PlanModeSourceCLI,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "planmode.created") || !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected planmode.created events.jsonl error, got %v", err)
	}
	restoredPlanMode, err := runner.store.SnapshotPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("snapshot restored plan mode: %v", err)
	}
	if restoredPlanMode.HasState != previousPlanMode.HasState || restoredPlanMode.State.PlanModeID != previousPlanMode.State.PlanModeID {
		t.Fatalf("expected previous Plan Mode snapshot restored, before=%#v after=%#v", previousPlanMode, restoredPlanMode)
	}
	planHistory, err := runner.store.LoadPlanModeHistory(meta.ID)
	if err != nil {
		t.Fatalf("load restored plan mode history: %v", err)
	}
	if len(planHistory) != len(previousPlanHistory) {
		t.Fatalf("expected plan mode history restored to %d entries, got %d: %#v", len(previousPlanHistory), len(planHistory), planHistory)
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
	events, err := runner.store.LoadEvents(result.SessionID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	if countRuntimeEventType(events, "goal.created") != 1 || countRuntimeEventType(events, "planmode.created") != 1 {
		t.Fatalf("expected one goal.created and one planmode.created event, got %#v", events)
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

func TestRunnerContinueBackfillsMissingProviderOptions(t *testing.T) {
	var seenBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(data, &seenBody); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_legacy_continue",
			"status":"completed",
			"output":[{"type":"function_call","call_id":"call_finish","name":"finish","arguments":"{\"message\":\"continued\"}"}],
			"usage":{"input_tokens":1,"output_tokens":1}
		}`))
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	cfg.DefaultProvider = "openai-compatible"
	cfg.Providers["openai-compatible"] = config.Provider{
		APIProvider:       "openai-compatible",
		APIKeyEnv:         "OPENAI_API_KEY",
		BaseURL:           server.URL + "/v1",
		Model:             "gpt-5.4",
		RequestTimeoutSec: 3,
		WireAPI:           "responses",
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
	}
	state := session.State{Status: session.StatusAwaitingInput, Phase: "turn_decide", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := runner.store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	result, err := runner.Continue(context.Background(), ContinueRequest{SessionID: meta.ID, Message: "continue from a legacy session"})
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
	if loadedMeta.ProviderOptions.Store == nil || *loadedMeta.ProviderOptions.Store {
		t.Fatalf("expected continue to backfill durable store=false, got %#v", loadedMeta.ProviderOptions)
	}
	if got := seenBody["store"]; got != false {
		t.Fatalf("expected provider request to include backfilled store=false, got %#v", seenBody)
	}
}

func TestRunnerContinueBackfillsPartialProviderOptions(t *testing.T) {
	var seenBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(data, &seenBody); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_partial_continue",
			"status":"completed",
			"output":[{"type":"function_call","call_id":"call_finish","name":"finish","arguments":"{\"message\":\"continued\"}"}],
			"usage":{"input_tokens":1,"output_tokens":1}
		}`))
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	cfg.DefaultProvider = "openai-compatible"
	cfg.Providers["openai-compatible"] = config.Provider{
		APIProvider:       "openai-compatible",
		APIKeyEnv:         "OPENAI_API_KEY",
		BaseURL:           server.URL + "/v1",
		Model:             "gpt-5.4",
		RequestTimeoutSec: 3,
		WireAPI:           "responses",
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
		ProviderOptions: session.ProviderOptions{
			APIProvider: "openai-compatible",
		},
	}
	state := session.State{Status: session.StatusAwaitingInput, Phase: "turn_decide", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := runner.store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	result, err := runner.Continue(context.Background(), ContinueRequest{SessionID: meta.ID, Message: "continue from a partial legacy session"})
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
	if loadedMeta.ProviderOptions.Store == nil || *loadedMeta.ProviderOptions.Store {
		t.Fatalf("expected continue to fill missing store=false while preserving partial options, got %#v", loadedMeta.ProviderOptions)
	}
	if got := seenBody["store"]; got != false {
		t.Fatalf("expected provider request to include merged store=false, got %#v", seenBody)
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

func TestRunnerUserMessageHookCommandEventFailureBlocksContinue(t *testing.T) {
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
		Command:    []string{"/bin/sh", "-c", "rm -f \"$1\" && mkdir \"$1\"", "block-events-then-fail", eventsPath},
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
		t.Fatalf("expected hook event append error, got result=%#v", result)
	}
	if !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected events append error with path context, got %v", err)
	}
	if !strings.Contains(err.Error(), "hook.command") {
		t.Fatalf("expected hook.command event context, got %v", err)
	}
	messages, loadErr := runner.store.LoadMessages(meta.ID)
	if loadErr != nil {
		t.Fatalf("load messages: %v", loadErr)
	}
	if len(messages) != 0 {
		t.Fatalf("expected failed hook trace to leave no user message, got %#v", messages)
	}
}

func TestRunnerUserMessageHookRequiresTriggeredEventBeforeCommand(t *testing.T) {
	root := t.TempDir()
	sessionID := session.NewSessionID()
	hookRan := filepath.Join(root, "hook-ran")
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
		Name:    "touch-before-message",
		Command: []string{"/bin/sh", "-c", "touch \"$1\"", "touch-before-message", hookRan},
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
	blockRunnerEventsPath(t, runner.store, meta.ID)

	result, err := runner.Continue(context.Background(), ContinueRequest{SessionID: meta.ID, Message: "continue through hook"})
	if err == nil {
		t.Fatalf("expected hook.triggered event append error, got result=%#v", result)
	}
	if !strings.Contains(err.Error(), "hook.triggered") || !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected hook.triggered events error, got %v", err)
	}
	if _, statErr := os.Stat(hookRan); !os.IsNotExist(statErr) {
		t.Fatalf("expected hook command not to run without durable triggered event, stat err=%v", statErr)
	}
	messages, loadErr := runner.store.LoadMessages(meta.ID)
	if loadErr != nil {
		t.Fatalf("load messages: %v", loadErr)
	}
	if len(messages) != 0 {
		t.Fatalf("expected failed hook trace to leave no user message, got %#v", messages)
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
		ProviderOptions:  providerOptionsFromConfig("openai-compatible", cfg.Providers["openai-compatible"]),
	}
	meta.RootSessionID = meta.ID
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

func TestRunnerContinueModelDefaultUsesProviderDefaultModel(t *testing.T) {
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
			"id":"resp_1",
			"status":"completed",
			"output":[{"type":"function_call","call_id":"call_finish","name":"finish","arguments":"{\"message\":\"done\"}"}],
			"usage":{"input_tokens":1,"output_tokens":1}
		}`))
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	openaiCompatible := cfg.Providers["openai-compatible"]
	openaiCompatible.BaseURL = server.URL
	openaiCompatible.Model = "gpt-provider-default"
	cfg.Providers["openai-compatible"] = openaiCompatible
	t.Setenv("OPENAI_API_KEY", "test-key")

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

	result, err := runner.Continue(context.Background(), ContinueRequest{SessionID: meta.ID, Provider: "openai-compatible", Model: "default"})
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
	if loadedMeta.Model != "gpt-provider-default" || seenModel != "gpt-provider-default" {
		t.Fatalf("expected provider default model, meta=%#v seen=%q", loadedMeta, seenModel)
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

func TestAutoContinueRollsBackRalphLoopCountWhenTriggeredEventFails(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	cfg.Runtime.RalphLoop.MaxIterations = 2
	runner := NewRunner(cfg)
	meta := session.SessionMetadata{SchemaVersion: 1, ID: session.NewSessionID(), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: t.TempDir(), Mode: session.ModeExec, Provider: "fake", Model: "fake", CompletionPolicy: completionPolicy(session.ModeExec)}
	state := session.State{Status: session.StatusFailed, Phase: "turn_decide", IncompleteReason: "incomplete_no_finish", LastError: "incomplete_no_finish", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := runner.store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := runner.store.AppendMessage(meta.ID, session.NewMessage("user", "finish")); err != nil {
		t.Fatalf("append: %v", err)
	}
	eventsPath := filepath.Join(runner.store.SessionDir(meta.ID), "events.jsonl")
	if err := os.RemoveAll(eventsPath); err != nil {
		t.Fatalf("remove events: %v", err)
	}
	if err := os.Mkdir(eventsPath, 0o700); err != nil {
		t.Fatalf("block events: %v", err)
	}

	_, err := runner.AutoContinue(context.Background(), meta.ID)
	if err == nil || !strings.Contains(err.Error(), events.EventRalphLoopTriggered) || !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected ralph_loop.triggered event error, got %v", err)
	}
	loaded, err := runner.store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if loaded.RalphLoopCount != 0 || loaded.Status != session.StatusFailed || loaded.IncompleteReason != "incomplete_no_finish" {
		t.Fatalf("expected Ralph loop count rollback to original failed state, got %#v", loaded)
	}
	messages, err := runner.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected no auto-continue user message after event failure, got %d messages", len(messages))
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
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		runner.watchSteer(ctx, meta.ID)
	}()
	time.Sleep(50 * time.Millisecond)
	if runner.control.consumeSteerInterrupt() {
		t.Fatal("watcher should not signal interrupt when interrupt_requested event cannot be persisted")
	}
	cancel()
	select {
	case <-watcherDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for blocked steer watcher to stop")
	}

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

func blockRunnerEventsPath(t *testing.T, store *session.Store, sessionID string) {
	t.Helper()
	eventsPath := filepath.Join(store.SessionDir(sessionID), "events.jsonl")
	if err := os.Remove(eventsPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove events: %v", err)
	}
	if err := os.Mkdir(eventsPath, 0o700); err != nil {
		t.Fatalf("block events path: %v", err)
	}
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
