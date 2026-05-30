package provider

import (
	"context"
	"encoding/json"
	"errors"
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
			"usage":{"input_tokens":10,"output_tokens":5,"input_tokens_details":{"cached_tokens":7,"cache_write_tokens":3}}
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
	if result.Usage.CacheReadInputTokens != 7 || result.Usage.CacheCreationInputTokens != 3 {
		t.Fatalf("expected openai cache usage telemetry, got %#v", result.Usage)
	}
}

func TestOpenAIResponsesReasoningSummaryEncryptedAndReplay(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_reasoning_1",
			"status":"completed",
			"output":[
				{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"checked constraints"}],"content":[{"type":"reasoning_text","text":"visible reasoning text"}],"encrypted_content":"enc_opaque"},
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}
			],
			"usage":{"input_tokens":10,"output_tokens":5,"output_tokens_details":{"reasoning_tokens":3}}
		}`))
	}))
	defer server.Close()

	adapter := NewOpenAI(server.URL, "key", server.Client())
	result, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:        "s1",
		Model:            "gpt-5.4",
		SystemPrompt:     "system",
		Messages:         []session.Message{session.NewMessage("user", "hello")},
		ReasoningEffort:  "xhigh",
		ReasoningSummary: "auto",
	}, func(string, map[string]any) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	reasoning, _ := body["reasoning"].(map[string]any)
	if reasoning["effort"] != "xhigh" || reasoning["summary"] != "auto" {
		t.Fatalf("expected reasoning effort+summary in request, got %#v", body)
	}
	include, _ := body["include"].([]any)
	if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("expected encrypted reasoning include, got %#v", body["include"])
	}
	if !strings.Contains(result.Thinking, "checked constraints") || !strings.Contains(result.Thinking, "visible reasoning text") {
		t.Fatalf("expected readable reasoning to enter Thinking, got %q", result.Thinking)
	}
	if strings.Contains(result.Thinking, "enc_opaque") {
		t.Fatalf("encrypted content leaked into Thinking: %q", result.Thinking)
	}
	if len(result.ProviderContentBlocks) != 1 {
		t.Fatalf("expected one reasoning provider block, got %#v", result.ProviderContentBlocks)
	}
	block := result.ProviderContentBlocks[0]
	if block.Provider != "openai" || block.Type != "reasoning" || block.ID != "rs_1" || block.Data != "enc_opaque" || block.Model != "gpt-5.4" {
		t.Fatalf("unexpected reasoning provider block: %#v", block)
	}
	if len(block.Summary) != 1 || block.Summary[0] != "checked constraints" || block.Sequence != 1 {
		t.Fatalf("expected summary binding and sequence, got %#v", block)
	}
	if result.RawProvider["reasoning_tokens"] != 3 || result.RawProvider["thinking_visible_observed"] != true || result.RawProvider["thinking_replay_observed"] != true || result.RawProvider["thinking_strategy"] != "responses_reasoning_summary" {
		t.Fatalf("expected non-sensitive reasoning observations, got %#v", result.RawProvider)
	}
}

func TestOpenAIAdapterRejectsInvalidFunctionCallArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_bad_args",
			"status":"completed",
			"output":[
				{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"command\":"}
			]
		}`))
	}))
	defer server.Close()

	adapter := NewOpenAI(server.URL, "key", server.Client())
	_, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "gpt-5.4",
		SystemPrompt: "system",
		Messages:     []session.Message{session.NewMessage("user", "hello")},
		Tools:        []ToolSchema{{Name: "shell", Description: "shell", InputSchema: map[string]any{"type": "object"}}},
	}, func(string, map[string]any) {})
	if err == nil {
		t.Fatal("expected invalid function-call arguments error")
	}
	httpErr, ok := err.(*HTTPError)
	if !ok {
		t.Fatalf("expected HTTPError, got %T", err)
	}
	if httpErr.Provider != "openai" || httpErr.Class != "response_parse_error" {
		t.Fatalf("unexpected provider error: %#v", httpErr)
	}
	if !strings.Contains(httpErr.Message, "tool-call arguments") {
		t.Fatalf("expected tool-call argument parse detail, got %q", httpErr.Message)
	}
}

func TestOpenAIAdapterRejectsNonObjectFunctionCallArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_bad_args",
			"status":"completed",
			"output":[
				{"type":"function_call","call_id":"call_1","name":"shell","arguments":"[]"}
			]
		}`))
	}))
	defer server.Close()

	adapter := NewOpenAI(server.URL, "key", server.Client())
	_, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "gpt-5.4",
		SystemPrompt: "system",
		Messages:     []session.Message{session.NewMessage("user", "hello")},
		Tools:        []ToolSchema{{Name: "shell", Description: "shell", InputSchema: map[string]any{"type": "object"}}},
	}, func(string, map[string]any) {})
	assertProviderParseError(t, err, "openai", "JSON object")
}

func TestOpenAIAdapterRejectsMissingFunctionCallID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_missing_call_id",
			"status":"completed",
			"output":[
				{"type":"function_call","name":"shell","arguments":"{\"command\":\"pwd\"}"}
			]
		}`))
	}))
	defer server.Close()

	adapter := NewOpenAI(server.URL, "key", server.Client())
	_, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "gpt-5.4",
		SystemPrompt: "system",
		Messages:     []session.Message{session.NewMessage("user", "hello")},
		Tools:        []ToolSchema{{Name: "shell", Description: "shell", InputSchema: map[string]any{"type": "object"}}},
	}, func(string, map[string]any) {})
	assertProviderParseError(t, err, "openai", "tool-call id")
}

func TestOpenAIAdapterMapsNonCompletedStatusToErrorStop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_failed",
			"status":"failed",
			"output":[
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial"}]}
			],
			"usage":{"input_tokens":10,"output_tokens":2}
		}`))
	}))
	defer server.Close()

	adapter := NewOpenAI(server.URL, "key", server.Client())
	result, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "gpt-5.4",
		SystemPrompt: "system",
		Messages:     []session.Message{session.NewMessage("user", "hello")},
	}, func(string, map[string]any) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.StopReason != "error" {
		t.Fatalf("expected error stop reason, got %#v", result)
	}
	if result.RawProvider["provider_stop_reason"] != "failed" || result.RawProvider["status"] != "failed" {
		t.Fatalf("expected raw failed status to be preserved, got %#v", result.RawProvider)
	}
}

func TestOpenAIAdapterDoesNotExecuteFunctionCallsFromFailedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_failed_with_call",
			"status":"failed",
			"output":[
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial"}]},
				{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"command\":\"pwd\"}"}
			],
			"usage":{"input_tokens":10,"output_tokens":2}
		}`))
	}))
	defer server.Close()

	adapter := NewOpenAI(server.URL, "key", server.Client())
	result, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "gpt-5.4",
		SystemPrompt: "system",
		Messages:     []session.Message{session.NewMessage("user", "hello")},
		Tools:        []ToolSchema{{Name: "shell", Description: "shell", InputSchema: map[string]any{"type": "object"}}},
	}, func(string, map[string]any) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.StopReason != "error" {
		t.Fatalf("expected failed status to win over function calls, got %#v", result)
	}
	if len(result.ToolCalls) != 0 {
		t.Fatalf("expected failed status to suppress tool calls, got %#v", result.ToolCalls)
	}
	if result.RawProvider["provider_stop_reason"] != "failed" || result.RawProvider["status"] != "failed" {
		t.Fatalf("expected raw failed status to be preserved, got %#v", result.RawProvider)
	}
}

func TestOpenAIAdapterDoesNotExecuteFunctionCallsWithoutCompletedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_missing_status_with_call",
			"output":[
				{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"command\":\"pwd\"}"}
			],
			"usage":{"input_tokens":10,"output_tokens":2}
		}`))
	}))
	defer server.Close()

	adapter := NewOpenAI(server.URL, "key", server.Client())
	result, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "gpt-5.4",
		SystemPrompt: "system",
		Messages:     []session.Message{session.NewMessage("user", "hello")},
		Tools:        []ToolSchema{{Name: "shell", Description: "shell", InputSchema: map[string]any{"type": "object"}}},
	}, func(string, map[string]any) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.StopReason != "error" {
		t.Fatalf("expected missing status to block function calls, got %#v", result)
	}
	if len(result.ToolCalls) != 0 {
		t.Fatalf("expected missing status to suppress tool calls, got %#v", result.ToolCalls)
	}
}

func TestOpenAIAdapterMapsMissingStatusToErrorStop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_missing_status",
			"output":[
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial"}]}
			],
			"usage":{"input_tokens":10,"output_tokens":2}
		}`))
	}))
	defer server.Close()

	adapter := NewOpenAI(server.URL, "key", server.Client())
	result, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "gpt-5.4",
		SystemPrompt: "system",
		Messages:     []session.Message{session.NewMessage("user", "hello")},
	}, func(string, map[string]any) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.StopReason != "error" {
		t.Fatalf("expected missing status to be a provider error stop, got %#v", result)
	}
}

func TestOpenAIInputReplaysEncryptedReasoningBlockSafely(t *testing.T) {
	assistant := session.NewAssistantMessage("", "", []session.ToolCall{{ID: "call_1", Name: "shell", Arguments: json.RawMessage(`{"command":"pwd"}`)}})
	assistant.ProviderContentBlocks = []session.ProviderContentBlock{
		{Provider: "openai", ProviderProfile: "openai", APIProvider: "openai-compatible", Type: "reasoning", ID: "rs_1", Data: "enc_opaque", Summary: []string{"summary"}, Sequence: 1, Model: "gpt-5.4"},
		{Provider: "openai", ProviderProfile: "gateway-b", APIProvider: "openai-compatible", Type: "reasoning", ID: "rs_other_profile", Data: "other", Summary: []string{"other"}, Sequence: 2, Model: "gpt-5.4"},
		{Provider: "openai", ProviderProfile: "openai", APIProvider: "anthropic-compatible", Type: "reasoning", ID: "rs_other_api", Data: "other-api", Summary: []string{"other-api"}, Sequence: 3, Model: "gpt-5.4"},
		{Provider: "openai", Type: "reasoning", ID: "rs_old", Data: "old", Summary: []string{"old"}, Sequence: 4, Model: "other-model"},
	}
	input, err := openAIInput([]session.Message{assistant}, "gpt-5.4", "openai", "openai-compatible")
	if err != nil {
		t.Fatalf("input: %v", err)
	}
	if len(input) != 2 {
		t.Fatalf("expected reasoning item plus function call, got %#v", input)
	}
	reasoning, _ := input[0].(map[string]any)
	if reasoning["type"] != "reasoning" || reasoning["id"] != "rs_1" || reasoning["encrypted_content"] != "enc_opaque" {
		t.Fatalf("unexpected replay reasoning item: %#v", reasoning)
	}
	summary, _ := reasoning["summary"].([]map[string]any)
	if len(summary) != 1 || summary[0]["text"] != "summary" {
		t.Fatalf("expected same-id summary replay, got %#v", reasoning["summary"])
	}

	mixed := assistant
	mixed.Text = "visible"
	input, err = openAIInput([]session.Message{mixed}, "gpt-5.4")
	if err != nil {
		t.Fatalf("input mixed: %v", err)
	}
	for _, item := range input {
		if obj, ok := item.(map[string]any); ok && obj["type"] == "reasoning" {
			t.Fatalf("mixed text+reasoning+tool_call replay should be disabled until order is verified: %#v", input)
		}
	}
}

func TestOpenAIInputReplaysEmptyReasoningSummaryArray(t *testing.T) {
	assistant := session.NewAssistantMessage("", "", []session.ToolCall{{ID: "call_1", Name: "shell", Arguments: json.RawMessage(`{"command":"pwd"}`)}})
	assistant.ProviderContentBlocks = []session.ProviderContentBlock{
		{Provider: "openai", ProviderProfile: "openai", APIProvider: "openai-compatible", Type: "reasoning", ID: "rs_empty", Data: "enc_empty", Sequence: 1, Model: "gpt-5.5"},
	}
	input, err := openAIInput([]session.Message{assistant}, "gpt-5.5", "openai", "openai-compatible")
	if err != nil {
		t.Fatalf("input: %v", err)
	}
	reasoning, _ := input[0].(map[string]any)
	summary, ok := reasoning["summary"].([]map[string]any)
	if !ok || len(summary) != 0 {
		t.Fatalf("expected explicit empty reasoning summary array, got %#v", reasoning["summary"])
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	if !strings.Contains(string(raw), `"summary":[]`) {
		t.Fatalf("expected replay JSON to include empty summary array, got %s", raw)
	}
}

func TestOpenAIInputRejectsMalformedPersistedToolArguments(t *testing.T) {
	for _, tc := range []struct {
		name string
		args json.RawMessage
		want string
	}{
		{name: "invalid json", args: json.RawMessage(`not-json`), want: "valid JSON"},
		{name: "non object", args: json.RawMessage(`[]`), want: "JSON object"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assistant := session.NewAssistantMessage("", "", []session.ToolCall{{ID: "call_bad", Name: "shell", Arguments: tc.args}})
			_, err := openAIInput([]session.Message{assistant}, "gpt-5.5")
			assertProviderParseError(t, err, "openai", tc.want)
		})
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
				{"type":"thinking","thinking":"inspect prior tool result","signature":"sig_thinking_1"},
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
			session.NewAssistantMessage("", "", []session.ToolCall{{ID: "toolu_0", Name: "shell", Arguments: json.RawMessage(`{"command":"ls"}`)}}),
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
	if result.Thinking != "inspect prior tool result" {
		t.Fatalf("expected anthropic thinking text, got %q", result.Thinking)
	}
	if len(result.ProviderContentBlocks) != 3 || result.ProviderContentBlocks[0].Signature != "sig_thinking_1" {
		t.Fatalf("expected anthropic provider content blocks, got %#v", result.ProviderContentBlocks)
	}
	if result.RawProvider["stop_reason"] != "tool_use" {
		t.Fatalf("expected anthropic raw provider stop_reason, got %#v", result.RawProvider)
	}
	if result.RawProvider["provider_stop_reason"] != "tool_use" {
		t.Fatalf("expected normalized provider stop reason, got %#v", result.RawProvider)
	}
}

func TestAnthropicAdapterMapsUnknownStopReasonToErrorStop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_unknown_stop",
			"stop_reason":"refusal",
			"content":[{"type":"text","text":"partial"}],
			"usage":{"input_tokens":8,"output_tokens":2}
		}`))
	}))
	defer server.Close()

	adapter := NewAnthropic(server.URL, "key", "2023-06-01", server.Client())
	result, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "claude-sonnet-4-6",
		SystemPrompt: "system",
		Messages:     []session.Message{session.NewMessage("user", "hello")},
	}, func(string, map[string]any) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.StopReason != "error" {
		t.Fatalf("expected error stop reason, got %#v", result)
	}
	if result.RawProvider["provider_stop_reason"] != "refusal" || result.RawProvider["stop_reason"] != "refusal" {
		t.Fatalf("expected raw stop reason to be preserved, got %#v", result.RawProvider)
	}
}

func TestAnthropicAdapterMapsMissingStopReasonToErrorStop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_missing_stop",
			"content":[{"type":"text","text":"partial"}],
			"usage":{"input_tokens":8,"output_tokens":2}
		}`))
	}))
	defer server.Close()

	adapter := NewAnthropic(server.URL, "key", "2023-06-01", server.Client())
	result, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "claude-sonnet-4-6",
		SystemPrompt: "system",
		Messages:     []session.Message{session.NewMessage("user", "hello")},
	}, func(string, map[string]any) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.StopReason != "error" {
		t.Fatalf("expected missing stop reason to be a provider error stop, got %#v", result)
	}
}

func TestAnthropicAdapterRejectsNonObjectToolUseInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_bad_tool_input",
			"stop_reason":"tool_use",
			"content":[
				{"type":"tool_use","id":"toolu_1","name":"shell","input":[]}
			],
			"usage":{"input_tokens":8,"output_tokens":4}
		}`))
	}))
	defer server.Close()

	adapter := NewAnthropic(server.URL, "key", "2023-06-01", server.Client())
	_, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "claude-sonnet-4-6",
		SystemPrompt: "system",
		Messages:     []session.Message{session.NewMessage("user", "hello")},
		Tools:        []ToolSchema{{Name: "shell", Description: "shell", InputSchema: map[string]any{"type": "object"}}},
	}, func(string, map[string]any) {})
	assertProviderParseError(t, err, "anthropic", "JSON object")
}

func TestAnthropicAdapterRejectsMissingToolUseID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_missing_tool_use_id",
			"stop_reason":"tool_use",
			"content":[
				{"type":"tool_use","name":"shell","input":{"command":"pwd"}}
			],
			"usage":{"input_tokens":8,"output_tokens":4}
		}`))
	}))
	defer server.Close()

	adapter := NewAnthropic(server.URL, "key", "2023-06-01", server.Client())
	_, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "claude-sonnet-4-6",
		SystemPrompt: "system",
		Messages:     []session.Message{session.NewMessage("user", "hello")},
		Tools:        []ToolSchema{{Name: "shell", Description: "shell", InputSchema: map[string]any{"type": "object"}}},
	}, func(string, map[string]any) {})
	assertProviderParseError(t, err, "anthropic", "tool-call id")
}

func TestAnthropicAdapterRejectsToolUseStopWithoutToolUseBlock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_missing_tool_use",
			"stop_reason":"tool_use",
			"content":[{"type":"text","text":"I should call a tool, but no tool_use block is present."}],
			"usage":{"input_tokens":8,"output_tokens":4}
		}`))
	}))
	defer server.Close()

	adapter := NewAnthropic(server.URL, "key", "2023-06-01", server.Client())
	_, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "claude-sonnet-4-6",
		SystemPrompt: "system",
		Messages:     []session.Message{session.NewMessage("user", "hello")},
		Tools:        []ToolSchema{{Name: "shell", Description: "shell", InputSchema: map[string]any{"type": "object"}}},
	}, func(string, map[string]any) {})
	assertProviderParseError(t, err, "anthropic", "tool_use stop reason")
}

func TestAnthropicAdapterRejectsToolUseBlockWithoutToolUseStop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_tool_use_with_end_turn",
			"stop_reason":"end_turn",
			"content":[
				{"type":"tool_use","id":"toolu_1","name":"shell","input":{"command":"pwd"}}
			],
			"usage":{"input_tokens":8,"output_tokens":4}
		}`))
	}))
	defer server.Close()

	adapter := NewAnthropic(server.URL, "key", "2023-06-01", server.Client())
	_, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "claude-sonnet-4-6",
		SystemPrompt: "system",
		Messages:     []session.Message{session.NewMessage("user", "hello")},
		Tools:        []ToolSchema{{Name: "shell", Description: "shell", InputSchema: map[string]any{"type": "object"}}},
	}, func(string, map[string]any) {})
	assertProviderParseError(t, err, "anthropic", "tool_use content block")
}

func TestAnthropicAdapterAppliesPromptCacheMarkersAndTelemetry(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(data, &body); err != nil {
			t.Fatalf("unmarshal request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_cache",
			"stop_reason":"end_turn",
			"content":[{"type":"text","text":"cached"}],
			"usage":{
				"input_tokens":8,
				"cache_creation_input_tokens":21,
				"cache_read_input_tokens":34,
				"output_tokens":4
			}
		}`))
	}))
	defer server.Close()

	cache := true
	adapter := NewAnthropic(server.URL, "key", "2023-06-01", server.Client())
	result, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "claude-sonnet-4-6",
		SystemPrompt: "stable system",
		PromptCache:  &cache,
		Messages: []session.Message{
			session.NewMessage("user", "first"),
			session.NewAssistantMessage("second", "", nil),
			session.NewMessage("user", "third"),
		},
		Tools: []ToolSchema{
			{Name: "read_file", Description: "read", InputSchema: map[string]any{"type": "object"}},
			{Name: "finish", Description: "finish", InputSchema: map[string]any{"type": "object"}},
		},
	}, func(string, map[string]any) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	systemBlocks, ok := body["system"].([]any)
	if !ok || len(systemBlocks) != 1 || !hasCacheControl(systemBlocks[0]) {
		t.Fatalf("expected system cache_control marker, got %#v", body["system"])
	}
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 2 || hasCacheControl(tools[0]) || !hasCacheControl(tools[1]) {
		t.Fatalf("expected only final tool schema to be cache-marked, got %#v", body["tools"])
	}
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 3 {
		t.Fatalf("expected three messages, got %#v", body["messages"])
	}
	if hasMessageCacheControl(messages[0]) || !hasMessageCacheControl(messages[1]) || !hasMessageCacheControl(messages[2]) {
		t.Fatalf("expected last two messages to be cache-marked, got %#v", body["messages"])
	}
	if result.Usage.CacheCreationInputTokens != 21 || result.Usage.CacheReadInputTokens != 34 {
		t.Fatalf("expected cache usage telemetry, got %#v", result.Usage)
	}
	if result.RawProvider["prompt_cache_enabled"] != true || result.RawProvider["cache_read_input_tokens"] != 34 {
		t.Fatalf("expected cache telemetry in raw provider envelope, got %#v", result.RawProvider)
	}
}

func hasMessageCacheControl(message any) bool {
	obj, ok := message.(map[string]any)
	if !ok {
		return false
	}
	content, ok := obj["content"].([]any)
	if !ok {
		return false
	}
	for _, block := range content {
		if hasCacheControl(block) {
			return true
		}
	}
	return false
}

func hasCacheControl(value any) bool {
	obj, ok := value.(map[string]any)
	if !ok {
		return false
	}
	cache, ok := obj["cache_control"].(map[string]any)
	return ok && cache["type"] == "ephemeral"
}

func TestAnthropicReplayDropsReasoningOnlyAssistantBlocks(t *testing.T) {
	var rawBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		rawBody = string(data)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_2",
			"stop_reason":"end_turn",
			"content":[{"type":"text","text":"done"}],
			"usage":{"input_tokens":8,"output_tokens":4}
		}`))
	}))
	defer server.Close()

	assistant := session.NewAssistantMessage("", "", nil)
	assistant.ProviderContentBlocks = []session.ProviderContentBlock{
		{Provider: "anthropic", Type: "thinking", Thinking: "reasoning", Signature: "sig_thinking_1", Model: "claude-sonnet-4-6"},
		{Provider: "anthropic", Type: "redacted_thinking", Data: "opaque_redacted_data", Model: "claude-sonnet-4-6"},
	}
	adapter := NewAnthropic(server.URL, "key", "2023-06-01", server.Client())
	if _, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "claude-sonnet-4-6",
		SystemPrompt: "system",
		Messages:     []session.Message{assistant},
	}, func(string, map[string]any) {}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(rawBody, "redacted_thinking") || strings.Contains(rawBody, "sig_thinking_1") {
		t.Fatalf("reasoning-only anthropic assistant blocks should not be replayed: %s", rawBody)
	}
}

func TestAnthropicAdapterReplaysThinkingBlocks(t *testing.T) {
	var rawBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		rawBody = string(data)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_2",
			"stop_reason":"end_turn",
			"content":[{"type":"text","text":"done"}],
			"usage":{"input_tokens":8,"output_tokens":4}
		}`))
	}))
	defer server.Close()

	assistant := session.NewAssistantMessage("visible", "reasoning", []session.ToolCall{{ID: "toolu_1", Name: "shell", Arguments: json.RawMessage(`{"command":"pwd"}`)}})
	assistant.ProviderContentBlocks = []session.ProviderContentBlock{
		{Provider: "anthropic", Type: "thinking", Thinking: "reasoning", Signature: "sig_thinking_1"},
		{Provider: "anthropic", Type: "redacted_thinking", Data: "opaque_redacted_data"},
		{Provider: "anthropic", Type: "text", Text: "visible"},
		{Provider: "anthropic", Type: "tool_use", ID: "toolu_1", Name: "shell", Input: json.RawMessage(`{"command":"pwd"}`)},
	}

	adapter := NewAnthropic(server.URL, "key", "2023-06-01", server.Client())
	if _, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "claude-sonnet-4-6",
		SystemPrompt: "system",
		Messages:     []session.Message{assistant, session.NewToolMessage([]session.ToolResult{{ToolCallID: "toolu_1", Name: "shell", LLMOutput: "ok"}})},
	}, func(string, map[string]any) {}); err != nil {
		t.Fatalf("run: %v", err)
	}

	for _, want := range []string{
		`"type":"thinking"`,
		`"signature":"sig_thinking_1"`,
		`"type":"redacted_thinking"`,
		`"data":"opaque_redacted_data"`,
		`"type":"tool_use"`,
	} {
		if !strings.Contains(rawBody, want) {
			t.Fatalf("expected %s in anthropic replay body: %s", want, rawBody)
		}
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
					{"text":"think first","thought":true,"thoughtSignature":"sig_thought_1"},
					{"text":"ok"},
					{"functionCall":{"name":"shell","id":"call_1","args":{"command":"pwd"}},"thoughtSignature":"sig_call_1"}
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
	if result.Text != "ok" || result.Thinking != "think first" {
		t.Fatalf("expected google thought summary to stay out of final text, got text=%q thinking=%q", result.Text, result.Thinking)
	}
	if len(result.ProviderContentBlocks) != 3 || result.ProviderContentBlocks[0].Thought == nil || !*result.ProviderContentBlocks[0].Thought || result.ProviderContentBlocks[0].ThoughtSignature != "sig_thought_1" || result.ProviderContentBlocks[2].ThoughtSignature != "sig_call_1" {
		t.Fatalf("expected google provider content blocks with thought signatures, got %#v", result.ProviderContentBlocks)
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

func TestGoogleAdapterGeneratesUniqueFallbackToolCallIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"responseId":"resp_google_no_call_ids",
			"modelVersion":"gemini-2.5-flash",
			"candidates":[{
				"content":{"parts":[
					{"functionCall":{"name":"shell","args":{"command":"pwd"}}},
					{"functionCall":{"name":"shell","args":{"command":"ls"}}}
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
		Messages:     []session.Message{session.NewMessage("user", "hello")},
		Tools:        []ToolSchema{{Name: "shell", Description: "shell", InputSchema: map[string]any{"type": "object"}}},
	}, func(string, map[string]any) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.StopReason != "tool_use" || len(result.ToolCalls) != 2 {
		t.Fatalf("expected two google tool calls, got %#v", result)
	}
	if result.ToolCalls[0].ID == "" || result.ToolCalls[1].ID == "" || result.ToolCalls[0].ID == result.ToolCalls[1].ID {
		t.Fatalf("expected unique fallback tool call ids, got %#v", result.ToolCalls)
	}
	if result.ProviderContentBlocks[0].ID != result.ToolCalls[0].ID || result.ProviderContentBlocks[1].ID != result.ToolCalls[1].ID {
		t.Fatalf("expected provider block ids to match fallback tool call ids, got blocks=%#v calls=%#v", result.ProviderContentBlocks, result.ToolCalls)
	}
}

func TestGoogleAdapterMapsPromptSafetyBlockWithoutCandidates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"responseId":"resp_google_blocked",
			"modelVersion":"gemini-2.5-flash",
			"promptFeedback":{"blockReason":"SAFETY"},
			"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":0}
		}`))
	}))
	defer server.Close()

	adapter := NewGoogle(server.URL, "key", server.Client())
	result, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "gemini-2.5-flash",
		SystemPrompt: "system",
		Messages:     []session.Message{session.NewMessage("user", "hello")},
	}, func(string, map[string]any) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.StopReason != "blocked" {
		t.Fatalf("expected blocked stop reason, got %#v", result)
	}
	if result.ProviderResponseID != "resp_google_blocked" {
		t.Fatalf("expected response id to be preserved, got %#v", result.ProviderResponseID)
	}
	if result.Usage.InputTokens != 7 || result.Usage.OutputTokens != 0 {
		t.Fatalf("expected prompt-block usage telemetry, got %#v", result.Usage)
	}
	if result.RawProvider["provider_stop_reason_source"] != "block_reason" || result.RawProvider["provider_stop_reason"] != "SAFETY" || result.RawProvider["block_reason"] != "SAFETY" {
		t.Fatalf("expected prompt block metadata in raw provider, got %#v", result.RawProvider)
	}
}

func TestGoogleAdapterDoesNotExecuteFunctionCallsFromSafetyFinish(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"responseId":"resp_google_safety_with_call",
			"modelVersion":"gemini-2.5-flash",
			"candidates":[{
				"content":{"parts":[{"functionCall":{"name":"shell","id":"call_1","args":{"command":"pwd"}}}]},
				"finishReason":"SAFETY"
			}],
			"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":1}
		}`))
	}))
	defer server.Close()

	adapter := NewGoogle(server.URL, "key", server.Client())
	result, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "gemini-2.5-flash",
		SystemPrompt: "system",
		Messages:     []session.Message{session.NewMessage("user", "hello")},
		Tools:        []ToolSchema{{Name: "shell", Description: "shell", InputSchema: map[string]any{"type": "object"}}},
	}, func(string, map[string]any) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.StopReason != "blocked" {
		t.Fatalf("expected safety finish to win over function calls, got %#v", result)
	}
	if len(result.ToolCalls) != 0 {
		t.Fatalf("expected safety finish to suppress tool calls, got %#v", result.ToolCalls)
	}
	for _, block := range result.ProviderContentBlocks {
		if block.Provider == "google" && block.Type == "function_call" {
			t.Fatalf("expected safety finish to suppress function-call provider blocks, got %#v", result.ProviderContentBlocks)
		}
	}
	if result.RawProvider["provider_stop_reason"] != "SAFETY" || result.RawProvider["finish_reason"] != "SAFETY" {
		t.Fatalf("expected raw safety finish reason to be preserved, got %#v", result.RawProvider)
	}
}

func TestGoogleAdapterDoesNotExecuteFunctionCallsWithoutStopFinish(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"responseId":"resp_google_missing_finish_with_call",
			"modelVersion":"gemini-2.5-flash",
			"candidates":[{
				"content":{"parts":[{"functionCall":{"name":"shell","id":"call_1","args":{"command":"pwd"}}}]}
			}],
			"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":1}
		}`))
	}))
	defer server.Close()

	adapter := NewGoogle(server.URL, "key", server.Client())
	result, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "gemini-2.5-flash",
		SystemPrompt: "system",
		Messages:     []session.Message{session.NewMessage("user", "hello")},
		Tools:        []ToolSchema{{Name: "shell", Description: "shell", InputSchema: map[string]any{"type": "object"}}},
	}, func(string, map[string]any) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.StopReason != "error" {
		t.Fatalf("expected missing finish reason to block function calls, got %#v", result)
	}
	if len(result.ToolCalls) != 0 {
		t.Fatalf("expected missing finish reason to suppress tool calls, got %#v", result.ToolCalls)
	}
	for _, block := range result.ProviderContentBlocks {
		if block.Provider == "google" && block.Type == "function_call" {
			t.Fatalf("expected missing finish reason to suppress function-call provider blocks, got %#v", result.ProviderContentBlocks)
		}
	}
}

func TestGoogleAdapterMapsMissingFinishReasonToErrorStop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"responseId":"resp_google_missing_finish",
			"modelVersion":"gemini-2.5-flash",
			"candidates":[{
				"content":{"parts":[{"text":"partial"}]}
			}],
			"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":1}
		}`))
	}))
	defer server.Close()

	adapter := NewGoogle(server.URL, "key", server.Client())
	result, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "gemini-2.5-flash",
		SystemPrompt: "system",
		Messages:     []session.Message{session.NewMessage("user", "hello")},
	}, func(string, map[string]any) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.StopReason != "error" {
		t.Fatalf("expected missing finish reason to be a provider error stop, got %#v", result)
	}
}

func TestGoogleAdapterMapsUnknownFinishReasonToErrorStop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"responseId":"resp_google_unknown_finish",
			"modelVersion":"gemini-2.5-flash",
			"candidates":[{
				"content":{"parts":[{"text":"partial"}]},
				"finishReason":"RECITATION"
			}],
			"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":2}
		}`))
	}))
	defer server.Close()

	adapter := NewGoogle(server.URL, "key", server.Client())
	result, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "gemini-2.5-flash",
		SystemPrompt: "system",
		Messages:     []session.Message{session.NewMessage("user", "hello")},
	}, func(string, map[string]any) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.StopReason != "error" {
		t.Fatalf("expected error stop reason, got %#v", result)
	}
	if result.RawProvider["provider_stop_reason"] != "RECITATION" || result.RawProvider["finish_reason"] != "RECITATION" {
		t.Fatalf("expected raw finish reason to be preserved, got %#v", result.RawProvider)
	}
}

func TestGoogleAdapterRejectsNonObjectFunctionCallArgs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"responseId":"resp_google_bad_args",
			"modelVersion":"gemini-2.5-flash",
			"candidates":[{
				"content":{"parts":[{"functionCall":{"name":"shell","id":"call_1","args":[]}}]},
				"finishReason":"STOP"
			}],
			"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3}
		}`))
	}))
	defer server.Close()

	adapter := NewGoogle(server.URL, "key", server.Client())
	_, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "gemini-2.5-flash",
		SystemPrompt: "system",
		Messages:     []session.Message{session.NewMessage("user", "hello")},
		Tools:        []ToolSchema{{Name: "shell", Description: "shell", InputSchema: map[string]any{"type": "object"}}},
	}, func(string, map[string]any) {})
	assertProviderParseError(t, err, "google", "JSON object")
}

func TestGoogleAdapterReplaysThoughtSignatures(t *testing.T) {
	var rawBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		rawBody = string(data)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"responseId":"resp_google_2",
			"candidates":[{"content":{"parts":[{"text":"done"}]},"finishReason":"STOP"}],
			"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3}
		}`))
	}))
	defer server.Close()

	thought := true
	assistant := session.NewAssistantMessage("visible", "reasoning", []session.ToolCall{{ID: "call_1", Name: "shell", Arguments: json.RawMessage(`{"command":"pwd"}`)}})
	assistant.ProviderContentBlocks = []session.ProviderContentBlock{
		{Provider: "google", Type: "part", Text: "reasoning", Thought: &thought, ThoughtSignature: "sig_thought_1"},
		{Provider: "google", Type: "part", Text: "visible"},
		{Provider: "google", Type: "function_call", ID: "call_1", Name: "shell", Args: json.RawMessage(`{"command":"pwd"}`), ThoughtSignature: "sig_call_1"},
	}

	adapter := NewGoogle(server.URL, "key", server.Client())
	if _, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "gemini-2.5-flash",
		SystemPrompt: "system",
		Messages:     []session.Message{assistant},
	}, func(string, map[string]any) {}); err != nil {
		t.Fatalf("run: %v", err)
	}

	for _, want := range []string{
		`"thought":true`,
		`"thoughtSignature":"sig_thought_1"`,
		`"thoughtSignature":"sig_call_1"`,
		`"functionCall"`,
	} {
		if !strings.Contains(rawBody, want) {
			t.Fatalf("expected %s in google replay body: %s", want, rawBody)
		}
	}
}

func TestGoogleReplayDropsThoughtOnlyAssistantBlocks(t *testing.T) {
	var rawBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		rawBody = string(data)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"responseId":"resp_google_2",
			"candidates":[{"content":{"parts":[{"text":"done"}]},"finishReason":"STOP"}],
			"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3}
		}`))
	}))
	defer server.Close()

	thought := true
	assistant := session.NewAssistantMessage("", "", nil)
	assistant.ProviderContentBlocks = []session.ProviderContentBlock{
		{Provider: "google", Type: "part", Text: "reasoning", Thought: &thought, ThoughtSignature: "sig_thought_1", Model: "gemini-2.5-flash"},
	}
	adapter := NewGoogle(server.URL, "key", server.Client())
	if _, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "s1",
		Model:        "gemini-2.5-flash",
		SystemPrompt: "system",
		Messages:     []session.Message{assistant},
	}, func(string, map[string]any) {}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(rawBody, "sig_thought_1") || strings.Contains(rawBody, `"thought":true`) {
		t.Fatalf("thought-only google assistant parts should not be replayed: %s", rawBody)
	}
}

func TestProviderReplaySerializesCompactedProviderBlockToolCalls(t *testing.T) {
	compactedArgs := json.RawMessage(`{"compacted_for_context":true,"original_chars":3600,"head_tail":"TAIL"}`)

	anthropicAssistant := session.NewAssistantMessage("", "", nil)
	anthropicAssistant.ProviderContentBlocks = []session.ProviderContentBlock{
		{Provider: "anthropic", Type: "thinking", Thinking: "reasoning", Signature: "sig_thinking_1"},
		{Provider: "anthropic", Type: "tool_use", ID: "toolu_old", Name: "shell", Input: compactedArgs},
	}
	anthropicReplay, err := anthropicMessages([]session.Message{
		anthropicAssistant,
		session.NewToolMessage([]session.ToolResult{{ToolCallID: "toolu_old", Name: "shell", LLMOutput: "ok"}}),
	}, "claude-sonnet-4-6", "", "", false)
	if err != nil {
		t.Fatalf("anthropic replay: %v", err)
	}
	anthropicData, _ := json.Marshal(anthropicReplay)
	anthropicBody := string(anthropicData)
	for _, want := range []string{
		`"type":"tool_use"`,
		`"tool_use_id":"toolu_old"`,
		`"compacted_for_context":true`,
		`"head_tail":"TAIL"`,
	} {
		if !strings.Contains(anthropicBody, want) {
			t.Fatalf("expected %s in compacted anthropic replay body: %s", want, anthropicBody)
		}
	}

	googleAssistant := session.NewAssistantMessage("", "", nil)
	googleAssistant.ProviderContentBlocks = []session.ProviderContentBlock{
		{Provider: "google", Type: "function_call", ID: "gcall_old", Name: "shell", Args: compactedArgs, ThoughtSignature: "sig_call_1"},
	}
	googleReplay, err := googleContents([]session.Message{
		googleAssistant,
		session.NewToolMessage([]session.ToolResult{{ToolCallID: "gcall_old", Name: "shell", LLMOutput: `{"output":"ok"}`}}),
	}, "gemini-2.5-flash", "", "")
	if err != nil {
		t.Fatalf("google replay: %v", err)
	}
	googleData, _ := json.Marshal(googleReplay)
	googleBody := string(googleData)
	for _, want := range []string{
		`"functionCall"`,
		`"functionResponse"`,
		`"id":"gcall_old"`,
		`"compacted_for_context":true`,
		`"head_tail":"TAIL"`,
	} {
		if !strings.Contains(googleBody, want) {
			t.Fatalf("expected %s in compacted google replay body: %s", want, googleBody)
		}
	}
}

func TestProviderReplayRejectsMalformedPersistedToolArguments(t *testing.T) {
	for _, tc := range []struct {
		name         string
		providerName string
		run          func() error
	}{
		{
			name:         "anthropic provider block",
			providerName: "anthropic",
			run: func() error {
				assistant := session.NewAssistantMessage("", "", nil)
				assistant.ProviderContentBlocks = []session.ProviderContentBlock{
					{Provider: "anthropic", Type: "tool_use", ID: "toolu_bad", Name: "shell", Input: json.RawMessage(`[]`)},
				}
				_, err := anthropicMessages([]session.Message{assistant}, "claude-sonnet-4-6", "", "", false)
				return err
			},
		},
		{
			name:         "anthropic fallback tool call",
			providerName: "anthropic",
			run: func() error {
				assistant := session.NewAssistantMessage("", "", []session.ToolCall{{ID: "toolu_bad", Name: "shell", Arguments: json.RawMessage(`not-json`)}})
				_, err := anthropicMessages([]session.Message{assistant}, "claude-sonnet-4-6", "", "", false)
				return err
			},
		},
		{
			name:         "google provider block",
			providerName: "google",
			run: func() error {
				assistant := session.NewAssistantMessage("", "", nil)
				assistant.ProviderContentBlocks = []session.ProviderContentBlock{
					{Provider: "google", Type: "function_call", ID: "gcall_bad", Name: "shell", Args: json.RawMessage(`[]`)},
				}
				_, err := googleContents([]session.Message{assistant}, "gemini-2.5-flash", "", "")
				return err
			},
		},
		{
			name:         "google fallback tool call",
			providerName: "google",
			run: func() error {
				assistant := session.NewAssistantMessage("", "", []session.ToolCall{{ID: "gcall_bad", Name: "shell", Arguments: json.RawMessage(`not-json`)}})
				_, err := googleContents([]session.Message{assistant}, "gemini-2.5-flash", "", "")
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertProviderParseError(t, tc.run(), tc.providerName, "tool-call arguments")
		})
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

func TestOpenAIAdapterRejectsOversizedProviderResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_too_large","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"`))
		_, _ = io.Copy(w, strings.NewReader(strings.Repeat("x", maxProviderResponseBytes)))
		_, _ = w.Write([]byte(`"}]}]}`))
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
		t.Fatal("expected oversized response error")
	}
	httpErr, ok := err.(*HTTPError)
	if !ok {
		t.Fatalf("expected HTTPError, got %T", err)
	}
	if httpErr.Provider != "openai" || httpErr.Class != "response_parse_error" || !strings.Contains(httpErr.Message, "exceeds maximum size") {
		t.Fatalf("unexpected provider error: %#v", httpErr)
	}
}

func TestAdaptersClassifyNon2xxAndPropagateCancel(t *testing.T) {
	for _, tc := range []struct {
		name    string
		adapter func(baseURL string, client *http.Client) Adapter
		model   string
		class   string
	}{
		{
			name:    "openai",
			adapter: func(baseURL string, client *http.Client) Adapter { return NewOpenAI(baseURL, "key", client) },
			model:   "gpt-5.4",
			class:   "upstream_unavailable",
		},
		{
			name: "anthropic",
			adapter: func(baseURL string, client *http.Client) Adapter {
				return NewAnthropic(baseURL, "key", "2023-06-01", client)
			},
			model: "claude-sonnet-4-6",
			class: "upstream_unavailable",
		},
		{
			name:    "google",
			adapter: func(baseURL string, client *http.Client) Adapter { return NewGoogle(baseURL, "key", client) },
			model:   "gemini-2.5-flash",
			class:   "upstream_unavailable",
		},
	} {
		t.Run(tc.name+"_non_2xx", func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "temporary", http.StatusInternalServerError)
			}))
			defer server.Close()
			_, err := tc.adapter(server.URL, server.Client()).RunTurn(context.Background(), TurnRequest{
				SessionID: "s1",
				Model:     tc.model,
				Messages:  []session.Message{session.NewMessage("user", "hello")},
			}, func(string, map[string]any) {})
			var httpErr *HTTPError
			if !errors.As(err, &httpErr) || httpErr.Class != tc.class {
				t.Fatalf("expected %s HTTPError, got %#v", tc.class, err)
			}
		})
		t.Run(tc.name+"_cancel", func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				<-r.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, err := tc.adapter(server.URL, server.Client()).RunTurn(ctx, TurnRequest{
				SessionID: "s1",
				Model:     tc.model,
				Messages:  []session.Message{session.NewMessage("user", "hello")},
			}, func(string, map[string]any) {})
			if err == nil {
				t.Fatal("expected cancellation error")
			}
		})
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

func TestJSONClientUsesPerAttemptRequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(60 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := JSONClient{
		Client:   server.Client(),
		BaseURL:  server.URL,
		Provider: "test-provider",
		Retry: RetryConfig{
			MaxAttempts:    1,
			RequestTimeout: 20 * time.Millisecond,
		},
	}
	var out map[string]any
	err := client.DoJSON(context.Background(), http.MethodPost, "/", nil, map[string]any{"hello": "world"}, &out, nil)
	if err == nil {
		t.Fatal("expected request timeout")
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected HTTPError, got %T %v", err, err)
	}
	if httpErr.Class != "upstream_timeout" || httpErr.TimeoutKind != "request_timeout" {
		t.Fatalf("unexpected timeout classification: %#v", httpErr)
	}
}

func TestJSONClientClassifiesStreamIdleTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(60 * time.Millisecond)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := JSONClient{
		Client:   server.Client(),
		BaseURL:  server.URL,
		Provider: "test-provider",
		Retry: RetryConfig{
			MaxAttempts:       1,
			StreamIdleTimeout: 20 * time.Millisecond,
		},
	}
	var out map[string]any
	err := client.DoJSON(context.Background(), http.MethodPost, "/", nil, map[string]any{"hello": "world"}, &out, nil)
	if err == nil {
		t.Fatal("expected stream idle timeout")
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected HTTPError, got %T %v", err, err)
	}
	if httpErr.Class != "upstream_timeout" || httpErr.TimeoutKind != "stream_idle_timeout" {
		t.Fatalf("unexpected stream timeout classification: %#v", httpErr)
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

func assertProviderParseError(t *testing.T, err error, providerName, messagePart string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected provider parse error")
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected HTTPError, got %T %v", err, err)
	}
	if httpErr.Provider != providerName || httpErr.Class != "response_parse_error" {
		t.Fatalf("unexpected provider error: %#v", httpErr)
	}
	if !strings.Contains(httpErr.Message, messagePart) {
		t.Fatalf("expected error message to contain %q, got %q", messagePart, httpErr.Message)
	}
}
