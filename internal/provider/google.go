package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"go-cli-agent/internal/session"
)

type GoogleAdapter struct {
	client JSONClient
	apiKey string
}

func NewGoogle(baseURL, apiKey string, httpClient *http.Client) *GoogleAdapter {
	return NewGoogleWithRetry(baseURL, apiKey, httpClient, RetryConfig{})
}

func NewGoogleWithRetry(baseURL, apiKey string, httpClient *http.Client, retry RetryConfig) *GoogleAdapter {
	return &GoogleAdapter{
		client: JSONClient{Client: httpClient, BaseURL: baseURL, Provider: "google", Retry: retry},
		apiKey: apiKey,
	}
}

func (a *GoogleAdapter) Name() string { return "google" }

func (a *GoogleAdapter) RunTurn(ctx context.Context, req TurnRequest, emit EmitFunc) (TurnResult, error) {
	body := map[string]any{
		"systemInstruction": map[string]any{
			"parts": []map[string]any{{"text": req.SystemPrompt}},
		},
		"contents": googleContents(req.Messages),
		"tools": []map[string]any{
			{"functionDeclarations": googleTools(req.Tools)},
		},
	}
	if generationConfig := googleGenerationConfig(req); len(generationConfig) > 0 {
		body["generationConfig"] = generationConfig
	}
	var resp struct {
		ResponseID   string `json:"responseId"`
		ModelVersion string `json:"modelVersion"`
		Candidates   []struct {
			Content struct {
				Parts []struct {
					Text         string `json:"text"`
					FunctionCall struct {
						Name string          `json:"name"`
						ID   string          `json:"id"`
						Args json.RawMessage `json:"args"`
					} `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	path := fmt.Sprintf("/v1beta/models/%s:generateContent", req.Model)
	err := a.client.DoJSON(ctx, http.MethodPost, path, map[string]string{
		"x-goog-api-key": a.apiKey,
	}, body, &resp, emit)
	if err != nil {
		return TurnResult{}, err
	}
	if len(resp.Candidates) == 0 {
		return TurnResult{}, fmt.Errorf("google: empty candidates")
	}
	candidate := resp.Candidates[0]
	var textParts []string
	var calls []ToolCall
	for _, part := range candidate.Content.Parts {
		if part.Text != "" {
			textParts = append(textParts, part.Text)
		}
		if part.FunctionCall.Name != "" {
			callID := part.FunctionCall.ID
			if callID == "" {
				callID = "call_" + part.FunctionCall.Name
			}
			calls = append(calls, ToolCall{
				ID:             callID,
				Name:           part.FunctionCall.Name,
				Arguments:      part.FunctionCall.Args,
				ProviderCallID: part.FunctionCall.ID,
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
	case candidate.FinishReason == "MAX_TOKENS":
		stopReason = "max_tokens"
	case candidate.FinishReason == "SAFETY":
		stopReason = "blocked"
	}
	return TurnResult{
		Text:               text,
		ToolCalls:          calls,
		StopReason:         stopReason,
		ProviderResponseID: resp.ResponseID,
		Usage: Usage{
			InputTokens:  resp.UsageMetadata.PromptTokenCount,
			OutputTokens: resp.UsageMetadata.CandidatesTokenCount,
		},
		RawProvider: rawProviderEnvelope("finish_reason", candidate.FinishReason, map[string]any{
			"model_version": resp.ModelVersion,
		}),
	}, nil
}

func googleGenerationConfig(req TurnRequest) map[string]any {
	config := map[string]any{}
	if req.Temperature != nil {
		config["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		config["topP"] = *req.TopP
	}
	if req.MaxOutputTokens > 0 {
		config["maxOutputTokens"] = req.MaxOutputTokens
	}
	if thinking := googleThinkingConfig(req.ThinkingBudget, req.IncludeThoughts); len(thinking) > 0 {
		config["thinkingConfig"] = thinking
	}
	return config
}

func googleThinkingConfig(budget int, includeThoughts *bool) map[string]any {
	if includeThoughts != nil && !*includeThoughts {
		return nil
	}
	if budget <= 0 {
		return nil
	}
	return map[string]any{
		"includeThoughts": true,
		"thinkingBudget":  budget,
	}
}

func googleTools(tools []ToolSchema) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		out = append(out, map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  tool.InputSchema,
		})
	}
	return out
}

func googleContents(messages []session.Message) []map[string]any {
	var out []map[string]any
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			out = append(out, map[string]any{
				"role":  "user",
				"parts": []map[string]any{{"text": msg.Text}},
			})
		case "assistant":
			parts := make([]map[string]any, 0, len(msg.ToolCalls)+1)
			if msg.Text != "" {
				parts = append(parts, map[string]any{"text": msg.Text})
			}
			for _, call := range msg.ToolCalls {
				var args any
				_ = json.Unmarshal(call.Arguments, &args)
				parts = append(parts, map[string]any{
					"functionCall": map[string]any{
						"name": call.Name,
						"id":   call.ID,
						"args": args,
					},
				})
			}
			out = append(out, map[string]any{
				"role":  "model",
				"parts": parts,
			})
		case "tool":
			parts := make([]map[string]any, 0, len(msg.ToolResults))
			for _, result := range msg.ToolResults {
				var response any
				response = map[string]any{"output": result.LLMOutput}
				if isJSON(result.LLMOutput) {
					var value any
					if err := json.Unmarshal([]byte(result.LLMOutput), &value); err == nil {
						response = value
					}
				}
				parts = append(parts, map[string]any{
					"functionResponse": map[string]any{
						"name":     result.Name,
						"id":       result.ToolCallID,
						"response": response,
					},
				})
			}
			out = append(out, map[string]any{
				"role":  "user",
				"parts": parts,
			})
		}
	}
	return out
}

func isJSON(input string) bool {
	input = strings.TrimSpace(input)
	return strings.HasPrefix(input, "{") || strings.HasPrefix(input, "[")
}
