package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"go-cli-agent/internal/session"
)

type OpenAIAdapter struct {
	client JSONClient
	apiKey string
}

func NewOpenAI(baseURL, apiKey string, httpClient *http.Client) *OpenAIAdapter {
	return NewOpenAIWithRetry(baseURL, apiKey, httpClient, RetryConfig{})
}

func NewOpenAIWithRetry(baseURL, apiKey string, httpClient *http.Client, retry RetryConfig) *OpenAIAdapter {
	return &OpenAIAdapter{
		client: JSONClient{Client: httpClient, BaseURL: baseURL, Provider: "openai", Retry: retry},
		apiKey: apiKey,
	}
}

func (a *OpenAIAdapter) Name() string { return "openai" }

func (a *OpenAIAdapter) RunTurn(ctx context.Context, req TurnRequest, emit EmitFunc) (TurnResult, error) {
	input, err := openAIInput(req.Messages)
	if err != nil {
		return TurnResult{}, err
	}
	body := map[string]any{
		"model":        req.Model,
		"instructions": req.SystemPrompt,
		"input":        input,
		"tools":        openAITools(req.Tools),
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if req.MaxOutputTokens > 0 {
		body["max_output_tokens"] = req.MaxOutputTokens
	}
	if strings.TrimSpace(req.ReasoningEffort) != "" {
		body["reasoning"] = map[string]any{
			"effort": req.ReasoningEffort,
		}
	}
	if strings.TrimSpace(req.TextVerbosity) != "" {
		body["text"] = map[string]any{
			"verbosity": req.TextVerbosity,
		}
	}
	if req.Store != nil {
		body["store"] = *req.Store
	}
	if len(req.Metadata) > 0 {
		body["metadata"] = req.Metadata
	}
	var resp struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Output []struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Role      string `json:"role"`
			Content   []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
		IncompleteDetails struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	}
	err = a.client.DoJSON(ctx, http.MethodPost, "/responses", map[string]string{
		"Authorization": "Bearer " + a.apiKey,
	}, body, &resp, emit)
	if err != nil {
		return TurnResult{}, err
	}
	var textParts []string
	var calls []ToolCall
	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, content := range item.Content {
				if content.Type == "output_text" {
					textParts = append(textParts, content.Text)
				}
			}
		case "function_call":
			calls = append(calls, ToolCall{
				ID:             item.CallID,
				Name:           item.Name,
				Arguments:      json.RawMessage(item.Arguments),
				ProviderCallID: item.CallID,
			})
		}
	}
	text := strings.Join(textParts, "\n")
	if text != "" {
		emit("assistant.delta", map[string]any{"text": text})
	}
	stopReason := "done_candidate"
	switch {
	case len(calls) > 0:
		stopReason = "tool_use"
	case resp.IncompleteDetails.Reason == "max_output_tokens":
		stopReason = "max_tokens"
	case resp.Status == "completed":
		stopReason = "done_candidate"
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
		RawProvider: rawProviderEnvelope("status", resp.Status, nil),
	}, nil
}

func openAITools(tools []ToolSchema) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		out = append(out, map[string]any{
			"type":        "function",
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  tool.InputSchema,
		})
	}
	return out
}

func openAIInput(messages []session.Message) ([]any, error) {
	var input []any
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			input = append(input, map[string]any{
				"role": "user",
				"content": []map[string]any{
					{"type": "input_text", "text": msg.Text},
				},
			})
		case "assistant":
			if msg.Text != "" {
				input = append(input, map[string]any{
					"role": "assistant",
					"content": []map[string]any{
						{"type": "output_text", "text": msg.Text},
					},
				})
			}
			for _, call := range msg.ToolCalls {
				input = append(input, map[string]any{
					"type":      "function_call",
					"call_id":   call.ID,
					"name":      call.Name,
					"arguments": string(call.Arguments),
				})
			}
		case "tool":
			for _, result := range msg.ToolResults {
				input = append(input, map[string]any{
					"type":    "function_call_output",
					"call_id": result.ToolCallID,
					"output":  result.LLMOutput,
				})
			}
		case "system":
			input = append(input, map[string]any{
				"role":    "system",
				"content": msg.Text,
			})
		default:
			return nil, fmt.Errorf("unsupported message role for OpenAI replay: %s", msg.Role)
		}
	}
	return input, nil
}
