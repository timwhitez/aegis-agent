package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"go-cli-agent/internal/session"
)

type AnthropicAdapter struct {
	client           JSONClient
	apiKey           string
	anthropicVersion string
}

func NewAnthropic(baseURL, apiKey, version string, httpClient *http.Client) *AnthropicAdapter {
	return NewAnthropicWithRetry(baseURL, apiKey, version, httpClient, RetryConfig{})
}

func NewAnthropicWithRetry(baseURL, apiKey, version string, httpClient *http.Client, retry RetryConfig) *AnthropicAdapter {
	return &AnthropicAdapter{
		client:           JSONClient{Client: httpClient, BaseURL: baseURL, Provider: "anthropic", Retry: retry},
		apiKey:           apiKey,
		anthropicVersion: version,
	}
}

func (a *AnthropicAdapter) Name() string { return "anthropic" }

func (a *AnthropicAdapter) RunTurn(ctx context.Context, req TurnRequest, emit EmitFunc) (TurnResult, error) {
	body := map[string]any{
		"model":      req.Model,
		"messages":   anthropicMessages(req.Messages),
		"tools":      anthropicTools(req.Tools),
		"max_tokens": anthropicMaxTokens(req.MaxOutputTokens),
	}
	if strings.TrimSpace(req.SystemPrompt) != "" {
		body["system"] = []map[string]any{
			{"type": "text", "text": req.SystemPrompt},
		}
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if thinking := anthropicThinking(req.ThinkingBudget, req.IncludeThoughts); thinking != nil {
		body["thinking"] = thinking
	}
	var resp struct {
		ID         string `json:"id"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	err := a.client.DoJSON(ctx, http.MethodPost, "/v1/messages", map[string]string{
		"x-api-key":         a.apiKey,
		"anthropic-version": a.anthropicVersion,
	}, body, &resp, emit)
	if err != nil {
		return TurnResult{}, err
	}
	var textParts []string
	var calls []ToolCall
	for _, item := range resp.Content {
		switch item.Type {
		case "text":
			textParts = append(textParts, item.Text)
		case "tool_use":
			calls = append(calls, ToolCall{
				ID:             item.ID,
				Name:           item.Name,
				Arguments:      item.Input,
				ProviderCallID: item.ID,
			})
		}
	}
	text := strings.Join(textParts, "\n")
	if text != "" {
		emit("assistant.delta", map[string]any{"text": text})
	}
	stopReason := "done_candidate"
	switch resp.StopReason {
	case "tool_use":
		stopReason = "tool_use"
	case "max_tokens":
		stopReason = "max_tokens"
	case "pause_turn":
		stopReason = "error"
	}
	return TurnResult{
		Text:               text,
		ToolCalls:          calls,
		StopReason:         stopReason,
		ProviderResponseID: resp.ID,
		Usage: Usage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
		},
		RawProvider: rawProviderEnvelope("stop_reason", resp.StopReason, nil),
	}, nil
}

func anthropicMaxTokens(value int) int {
	if value > 0 {
		return value
	}
	return 4096
}

func anthropicThinking(budget int, includeThoughts *bool) map[string]any {
	if includeThoughts != nil && !*includeThoughts {
		return nil
	}
	if budget <= 0 {
		return nil
	}
	return map[string]any{
		"type":          "enabled",
		"budget_tokens": budget,
	}
}

func anthropicTools(tools []ToolSchema) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		out = append(out, map[string]any{
			"name":         tool.Name,
			"description":  tool.Description,
			"input_schema": tool.InputSchema,
		})
	}
	return out
}

func anthropicMessages(messages []session.Message) []map[string]any {
	var out []map[string]any
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			if msg.Text == "" {
				continue
			}
			out = append(out, map[string]any{
				"role": "user",
				"content": []map[string]any{
					{"type": "text", "text": msg.Text},
				},
			})
		case "assistant":
			content := make([]map[string]any, 0, len(msg.ToolCalls)+1)
			if msg.Text != "" {
				content = append(content, map[string]any{"type": "text", "text": msg.Text})
			}
			for _, call := range msg.ToolCalls {
				var input any
				_ = json.Unmarshal(call.Arguments, &input)
				content = append(content, map[string]any{
					"type":  "tool_use",
					"id":    call.ID,
					"name":  call.Name,
					"input": input,
				})
			}
			if len(content) == 0 {
				continue
			}
			out = append(out, map[string]any{
				"role":    "assistant",
				"content": content,
			})
		case "tool":
			content := make([]map[string]any, 0, len(msg.ToolResults))
			for _, result := range msg.ToolResults {
				content = append(content, map[string]any{
					"type":        "tool_result",
					"tool_use_id": result.ToolCallID,
					"is_error":    result.IsError,
					"content":     result.LLMOutput,
				})
			}
			if len(content) == 0 {
				continue
			}
			out = append(out, map[string]any{
				"role":    "user",
				"content": content,
			})
		}
	}
	return out
}
