package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go-cli-agent/internal/session"
)

func TestOpenAIAdapterSerializesAndParses(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_1",
			"status":"completed",
			"output":[
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]},
				{"type":"function_call","call_id":"call_1","name":"finish","arguments":"{\"message\":\"done\"}"}
			],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()

	adapter := NewOpenAI(server.URL, "key", server.Client())
	result, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "gpt-5.4",
		SystemPrompt: "system",
		Messages:     []session.Message{session.NewMessage("user", "hello")},
		Tools:        []ToolSchema{{Name: "finish", Description: "finish", InputSchema: map[string]any{"type": "object"}}},
	}, func(string, map[string]any) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if body["instructions"] != "system" {
		t.Fatalf("missing instructions in body: %#v", body)
	}
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected one serialized tool, got %#v", body["tools"])
	}
	toolDef, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("expected first tool to be object, got %#v", tools[0])
	}
	if toolDef["description"] != "finish" {
		t.Fatalf("expected serialized tool description, got %#v", toolDef["description"])
	}
	input, ok := body["input"].([]any)
	if !ok || len(input) == 0 {
		t.Fatalf("expected structured input array, got %#v", body["input"])
	}
	first, ok := input[0].(map[string]any)
	if !ok {
		t.Fatalf("expected first input item to be object, got %#v", input[0])
	}
	content, ok := first["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("expected user content blocks, got %#v", first["content"])
	}
	part, ok := content[0].(map[string]any)
	if !ok || part["type"] != "input_text" {
		t.Fatalf("expected input_text block, got %#v", content[0])
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "finish" {
		t.Fatalf("unexpected tool calls: %#v", result.ToolCalls)
	}
	if result.Text != "hello" {
		t.Fatalf("unexpected text: %q", result.Text)
	}
	if result.RawProvider["provider_stop_reason"] != "completed" {
		t.Fatalf("expected normalized openai provider stop reason, got %#v", result.RawProvider)
	}
}

func TestAnthropicAdapterSerializesAndParses(t *testing.T) {
	var rawBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		rawBody = string(data)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_1",
			"stop_reason":"tool_use",
			"content":[
				{"type":"text","text":"check"},
				{"type":"tool_use","id":"toolu_1","name":"shell","input":{"command":"pwd"}}
			],
			"usage":{"input_tokens":8,"output_tokens":4}
		}`))
	}))
	defer server.Close()

	adapter := NewAnthropic(server.URL, "key", "2023-06-01", server.Client())
	result, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "claude-sonnet-4-6",
		SystemPrompt: "system",
		Messages: []session.Message{
			session.NewAssistantMessage("", []session.ToolCall{{ID: "toolu_0", Name: "shell", Arguments: json.RawMessage(`{"command":"ls"}`)}}),
			session.NewToolMessage([]session.ToolResult{{ToolCallID: "toolu_0", Name: "shell", LLMOutput: "ok"}}),
		},
	}, func(string, map[string]any) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(rawBody, `"system":[{"text":"system","type":"text"}]`) {
		t.Fatalf("expected structured system blocks in anthropic body: %s", rawBody)
	}
	if !strings.Contains(rawBody, `"content":[{"id":"toolu_0","input":{"command":"ls"},"name":"shell","type":"tool_use"}]`) &&
		!strings.Contains(rawBody, `"content":[{"type":"tool_use","id":"toolu_0","name":"shell"`) {
		t.Fatalf("expected structured assistant tool_use replay blocks: %s", rawBody)
	}
	if !strings.Contains(rawBody, `"tool_result"`) {
		t.Fatalf("expected tool_result in anthropic replay body: %s", rawBody)
	}
	if result.StopReason != "tool_use" || len(result.ToolCalls) != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.RawProvider["stop_reason"] != "tool_use" {
		t.Fatalf("expected anthropic raw provider stop_reason, got %#v", result.RawProvider)
	}
	if result.RawProvider["provider_stop_reason"] != "tool_use" {
		t.Fatalf("expected normalized provider stop reason, got %#v", result.RawProvider)
	}
}

func TestGoogleAdapterSerializesAndParses(t *testing.T) {
	var rawBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		rawBody = string(data)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"responseId":"resp_google_1",
			"modelVersion":"gemini-2.5-flash",
			"candidates":[{
				"content":{"parts":[
					{"text":"ok"},
					{"functionCall":{"name":"shell","id":"call_1","args":{"command":"pwd"}}}
				]},
				"finishReason":"STOP"
			}],
			"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3}
		}`))
	}))
	defer server.Close()

	adapter := NewGoogle(server.URL, "key", server.Client())
	result, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "gemini-2.5-flash",
		SystemPrompt: "system",
		Messages:     []session.Message{session.NewToolMessage([]session.ToolResult{{ToolCallID: "call_0", Name: "shell", LLMOutput: `{"output":"ok"}`}})},
		Tools:        []ToolSchema{{Name: "shell", Description: "shell", InputSchema: map[string]any{"type": "object"}}},
	}, func(string, map[string]any) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(rawBody, `"functionResponse"`) {
		t.Fatalf("expected functionResponse in google replay body: %s", rawBody)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "shell" {
		t.Fatalf("unexpected tool calls: %#v", result.ToolCalls)
	}
	if result.ProviderResponseID != "resp_google_1" {
		t.Fatalf("expected google provider response id, got %#v", result.ProviderResponseID)
	}
	if result.RawProvider["finish_reason"] != "STOP" {
		t.Fatalf("expected google raw provider finish_reason, got %#v", result.RawProvider)
	}
	if result.RawProvider["provider_stop_reason"] != "STOP" {
		t.Fatalf("expected normalized google provider stop reason, got %#v", result.RawProvider)
	}
}

func TestOpenAIAdapterClassifiesHTTPErrorWithProviderName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad key", http.StatusUnauthorized)
	}))
	defer server.Close()

	adapter := NewOpenAI(server.URL, "key", server.Client())
	_, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "gpt-5.4",
		SystemPrompt: "system",
		Messages:     []session.Message{session.NewMessage("user", "hello")},
	}, func(string, map[string]any) {})
	if err == nil {
		t.Fatal("expected error")
	}
	httpErr, ok := err.(*HTTPError)
	if !ok {
		t.Fatalf("expected HTTPError, got %T", err)
	}
	if httpErr.Provider != "openai" || httpErr.Class != "auth_error" {
		t.Fatalf("unexpected provider error: %#v", httpErr)
	}
}

func TestOpenAIAdapterClassifiesResponseParseError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"not_valid":`))
	}))
	defer server.Close()

	adapter := NewOpenAI(server.URL, "key", server.Client())
	_, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "gpt-5.4",
		SystemPrompt: "system",
		Messages:     []session.Message{session.NewMessage("user", "hello")},
	}, func(string, map[string]any) {})
	if err == nil {
		t.Fatal("expected error")
	}
	httpErr, ok := err.(*HTTPError)
	if !ok {
		t.Fatalf("expected HTTPError, got %T", err)
	}
	if httpErr.Provider != "openai" || httpErr.Class != "response_parse_error" {
		t.Fatalf("unexpected provider error: %#v", httpErr)
	}
}

func TestOpenAIAdapterSendsOptionalGenerationFields(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_1",
			"status":"completed",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()

	temperature := 0.2
	topP := 0.8
	store := false
	metadata := map[string]any{
		"session_id":      "s1",
		"root_session_id": "root-1",
	}
	adapter := NewOpenAI(server.URL, "key", server.Client())
	_, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:       "s1",
		Model:           "gpt-5.4",
		SystemPrompt:    "system",
		Messages:        []session.Message{session.NewMessage("user", "hello")},
		Metadata:        metadata,
		Temperature:     &temperature,
		TopP:            &topP,
		MaxOutputTokens: 512,
		ReasoningEffort: "high",
		TextVerbosity:   "low",
		Store:           &store,
	}, func(string, map[string]any) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := body["temperature"]; got != temperature {
		t.Fatalf("expected temperature %v, got %#v", temperature, got)
	}
	if got := body["top_p"]; got != topP {
		t.Fatalf("expected top_p %v, got %#v", topP, got)
	}
	if got := body["max_output_tokens"]; got != float64(512) {
		t.Fatalf("expected max_output_tokens 512, got %#v", got)
	}
	if got := body["store"]; got != false {
		t.Fatalf("expected store=false, got %#v", got)
	}
	gotMetadata, ok := body["metadata"].(map[string]any)
	if !ok || gotMetadata["session_id"] != "s1" || gotMetadata["root_session_id"] != "root-1" {
		t.Fatalf("expected metadata to be forwarded, got %#v", body["metadata"])
	}
	reasoning, ok := body["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "high" {
		t.Fatalf("expected reasoning.effort=high, got %#v", body["reasoning"])
	}
	text, ok := body["text"].(map[string]any)
	if !ok || text["verbosity"] != "low" {
		t.Fatalf("expected text.verbosity=low, got %#v", body["text"])
	}
}

func TestOpenAIAdapterRetriesRetryable5xx(t *testing.T) {
	var attempts int32
	var retryEvents int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt32(&attempts, 1)
		if attempt == 1 {
			http.Error(w, "try later", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_1",
			"status":"completed",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello after retry"}]}],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()

	adapter := NewOpenAIWithRetry(server.URL, "key", server.Client(), RetryConfig{
		MaxAttempts: 2,
		BaseDelay:   time.Millisecond,
		Retry5xx:    true,
	})
	result, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "gpt-5.4",
		SystemPrompt: "system",
		Messages:     []session.Message{session.NewMessage("user", "hello")},
	}, func(eventType string, data map[string]any) {
		if eventType == "provider.retry" {
			retryEvents++
			if got := data["class"]; got != "upstream_unavailable" {
				t.Fatalf("expected retry class upstream_unavailable, got %#v", got)
			}
		}
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Text != "hello after retry" {
		t.Fatalf("unexpected result text: %q", result.Text)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
	if retryEvents != 1 {
		t.Fatalf("expected 1 retry event, got %d", retryEvents)
	}
}

func TestOpenAIAdapterRetriesTransportTimeout(t *testing.T) {
	var attempts int32
	var retryClass string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt32(&attempts, 1)
		if attempt == 1 {
			time.Sleep(60 * time.Millisecond)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_1",
			"status":"completed",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"transport retry ok"}]}],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()

	client := server.Client()
	client.Timeout = 20 * time.Millisecond
	adapter := NewOpenAIWithRetry(server.URL, "key", client, RetryConfig{
		MaxAttempts:    2,
		BaseDelay:      time.Millisecond,
		RetryTransport: true,
	})
	result, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "gpt-5.4",
		SystemPrompt: "system",
		Messages:     []session.Message{session.NewMessage("user", "hello")},
	}, func(eventType string, data map[string]any) {
		if eventType == "provider.retry" {
			if value, ok := data["class"].(string); ok {
				retryClass = value
			}
		}
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Text != "transport retry ok" {
		t.Fatalf("unexpected result text: %q", result.Text)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
	if retryClass != "upstream_timeout" {
		t.Fatalf("expected upstream_timeout retry class, got %q", retryClass)
	}
}

func TestAnthropicAdapterSendsGenerationFields(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_1",
			"stop_reason":"end_turn",
			"content":[{"type":"text","text":"done"}],
			"usage":{"input_tokens":8,"output_tokens":4}
		}`))
	}))
	defer server.Close()

	temperature := 0.4
	topP := 0.9
	includeThoughts := true
	adapter := NewAnthropic(server.URL, "key", "2023-06-01", server.Client())
	_, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:       "s1",
		Model:           "claude-sonnet-4-6",
		SystemPrompt:    "system",
		Messages:        []session.Message{session.NewMessage("user", "hello")},
		Temperature:     &temperature,
		TopP:            &topP,
		MaxOutputTokens: 2048,
		ThinkingBudget:  1024,
		IncludeThoughts: &includeThoughts,
	}, func(string, map[string]any) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := body["temperature"]; got != temperature {
		t.Fatalf("expected temperature %v, got %#v", temperature, got)
	}
	if got := body["top_p"]; got != topP {
		t.Fatalf("expected top_p %v, got %#v", topP, got)
	}
	if got := body["max_tokens"]; got != float64(2048) {
		t.Fatalf("expected max_tokens 2048, got %#v", got)
	}
	thinking, ok := body["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("expected thinking payload, got %#v", body["thinking"])
	}
	if !reflect.DeepEqual(thinking, map[string]any{
		"type":          "enabled",
		"budget_tokens": float64(1024),
	}) {
		t.Fatalf("unexpected thinking payload: %#v", thinking)
	}
}

func TestGoogleAdapterSendsGenerationFields(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],
			"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3}
		}`))
	}))
	defer server.Close()

	temperature := 0.3
	topP := 0.7
	includeThoughts := true
	adapter := NewGoogle(server.URL, "key", server.Client())
	_, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:       "s1",
		Model:           "gemini-2.5-flash",
		SystemPrompt:    "system",
		Messages:        []session.Message{session.NewMessage("user", "hello")},
		Temperature:     &temperature,
		TopP:            &topP,
		MaxOutputTokens: 1024,
		ThinkingBudget:  2048,
		IncludeThoughts: &includeThoughts,
	}, func(string, map[string]any) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	config, ok := body["generationConfig"].(map[string]any)
	if !ok {
		t.Fatalf("expected generationConfig, got %#v", body["generationConfig"])
	}
	if got := config["temperature"]; got != temperature {
		t.Fatalf("expected generationConfig.temperature %v, got %#v", temperature, got)
	}
	if got := config["topP"]; got != topP {
		t.Fatalf("expected generationConfig.topP %v, got %#v", topP, got)
	}
	if got := config["maxOutputTokens"]; got != float64(1024) {
		t.Fatalf("expected generationConfig.maxOutputTokens 1024, got %#v", got)
	}
	thinking, ok := config["thinkingConfig"].(map[string]any)
	if !ok {
		t.Fatalf("expected thinkingConfig, got %#v", config["thinkingConfig"])
	}
	if !reflect.DeepEqual(thinking, map[string]any{
		"includeThoughts": true,
		"thinkingBudget":  float64(2048),
	}) {
		t.Fatalf("unexpected thinkingConfig: %#v", thinking)
	}
}
