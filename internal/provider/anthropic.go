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
	systemBlocks := anthropicSystemBlocks(req.SystemPrompt, promptCacheEnabled(req.PromptCache))
	body := map[string]any{
		"model":      req.Model,
		"messages":   anthropicMessages(req.Messages, req.Model, req.ProviderProfile, req.APIProvider, promptCacheEnabled(req.PromptCache)),
		"tools":      anthropicTools(req.Tools, promptCacheEnabled(req.PromptCache)),
		"max_tokens": anthropicMaxTokens(req.MaxOutputTokens),
	}
	if len(systemBlocks) > 0 {
		body["system"] = systemBlocks
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	thinkingStrategy := anthropicThinkingStrategy(req.ThinkingBudget, req.IncludeThoughts)
	if thinking := anthropicThinking(req.ThinkingBudget, req.IncludeThoughts); thinking != nil {
		body["thinking"] = thinking
	}
	var resp struct {
		ID         string `json:"id"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			Thinking  string          `json:"thinking"`
			Signature string          `json:"signature"`
			Data      string          `json:"data"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
		} `json:"content"`
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
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
	var thinkingParts []string
	var providerBlocks []session.ProviderContentBlock
	var calls []ToolCall
	thinkingBlockCount := 0
	redactedThinkingCount := 0
	thinkingReplayObserved := false
	for _, item := range resp.Content {
		switch item.Type {
		case "text":
			textParts = append(textParts, item.Text)
			providerBlocks = append(providerBlocks, session.ProviderContentBlock{
				Provider: "anthropic",
				Type:     "text",
				Text:     item.Text,
				Model:    req.Model,
			})
		case "thinking":
			thinkingBlockCount++
			if item.Thinking != "" {
				thinkingParts = append(thinkingParts, item.Thinking)
			}
			if item.Signature != "" {
				thinkingReplayObserved = true
			}
			providerBlocks = append(providerBlocks, session.ProviderContentBlock{
				Provider:  "anthropic",
				Type:      "thinking",
				Thinking:  item.Thinking,
				Signature: item.Signature,
				Model:     req.Model,
			})
		case "redacted_thinking":
			redactedThinkingCount++
			if item.Data != "" {
				thinkingReplayObserved = true
			}
			providerBlocks = append(providerBlocks, session.ProviderContentBlock{
				Provider: "anthropic",
				Type:     "redacted_thinking",
				Data:     item.Data,
				Model:    req.Model,
			})
		case "tool_use":
			providerBlocks = append(providerBlocks, session.ProviderContentBlock{
				Provider: "anthropic",
				Type:     "tool_use",
				ID:       item.ID,
				Name:     item.Name,
				Input:    item.Input,
				Model:    req.Model,
			})
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
		Text:                  text,
		Thinking:              strings.Join(thinkingParts, "\n"),
		ProviderContentBlocks: providerBlocks,
		ToolCalls:             calls,
		StopReason:            stopReason,
		ProviderResponseID:    resp.ID,
		Usage: Usage{
			InputTokens:              resp.Usage.InputTokens,
			OutputTokens:             resp.Usage.OutputTokens,
			CacheCreationInputTokens: resp.Usage.CacheCreationInputTokens,
			CacheReadInputTokens:     resp.Usage.CacheReadInputTokens,
		},
		RawProvider: rawProviderEnvelope("stop_reason", resp.StopReason, map[string]any{
			"thinking_block_count":        thinkingBlockCount,
			"redacted_thinking_count":     redactedThinkingCount,
			"thinking_visible_observed":   len(thinkingParts) > 0,
			"thinking_replay_observed":    thinkingReplayObserved,
			"thinking_strategy":           thinkingStrategy,
			"prompt_cache_enabled":        promptCacheEnabled(req.PromptCache),
			"cache_creation_input_tokens": resp.Usage.CacheCreationInputTokens,
			"cache_read_input_tokens":     resp.Usage.CacheReadInputTokens,
		}),
	}, nil
}

func promptCacheEnabled(value *bool) bool {
	return value != nil && *value
}

func anthropicSystemBlocks(systemPrompt string, enableCache bool) []map[string]any {
	if strings.TrimSpace(systemPrompt) == "" {
		return nil
	}
	block := map[string]any{"type": "text", "text": systemPrompt}
	if enableCache {
		block["cache_control"] = map[string]any{"type": "ephemeral"}
	}
	return []map[string]any{block}
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

func anthropicThinkingStrategy(budget int, includeThoughts *bool) string {
	if includeThoughts != nil && !*includeThoughts {
		return "off"
	}
	if budget > 0 {
		return "manual_budget"
	}
	return "provider_default"
}

func anthropicTools(tools []ToolSchema, enableCache bool) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for i, tool := range tools {
		item := map[string]any{
			"name":         tool.Name,
			"description":  tool.Description,
			"input_schema": tool.InputSchema,
		}
		if enableCache && i == len(tools)-1 {
			item["cache_control"] = map[string]any{"type": "ephemeral"}
		}
		out = append(out, item)
	}
	return out
}

func anthropicMessages(messages []session.Message, model, providerProfile, apiProvider string, enableCache bool) []map[string]any {
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
			if anthropicBlocks := anthropicProviderContent(msg.ProviderContentBlocks, model, providerProfile, apiProvider); len(anthropicBlocks) > 0 {
				content = anthropicBlocks
			} else {
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
	if enableCache {
		applyAnthropicMessageCache(out)
	}
	return out
}

func applyAnthropicMessageCache(messages []map[string]any) {
	remaining := 2
	for i := len(messages) - 1; i >= 0 && remaining > 0; i-- {
		if anthropicCacheBlockCandidate(messages[i]) {
			remaining--
		}
	}
}

func anthropicCacheBlockCandidate(msg map[string]any) bool {
	content, ok := msg["content"].([]map[string]any)
	if !ok || len(content) == 0 {
		return false
	}
	for i := len(content) - 1; i >= 0; i-- {
		if anthropicBlockCacheable(content[i]) {
			content[i]["cache_control"] = map[string]any{"type": "ephemeral"}
			return true
		}
	}
	return false
}

func anthropicBlockCacheable(block map[string]any) bool {
	switch block["type"] {
	case "text", "tool_use", "tool_result":
		return true
	default:
		return false
	}
}

func anthropicProviderContent(blocks []session.ProviderContentBlock, model, providerProfile, apiProvider string) []map[string]any {
	var content []map[string]any
	hasAnchor := false
	for _, block := range blocks {
		if block.Provider != "anthropic" {
			continue
		}
		if strings.TrimSpace(block.Model) != "" && strings.TrimSpace(model) != "" && block.Model != model {
			continue
		}
		if strings.TrimSpace(block.ProviderProfile) != "" && strings.TrimSpace(providerProfile) != "" && block.ProviderProfile != providerProfile {
			continue
		}
		if strings.TrimSpace(block.APIProvider) != "" && strings.TrimSpace(apiProvider) != "" && block.APIProvider != apiProvider {
			continue
		}
		switch block.Type {
		case "thinking":
			item := map[string]any{
				"type":     "thinking",
				"thinking": block.Thinking,
			}
			if block.Signature != "" {
				item["signature"] = block.Signature
			}
			content = append(content, item)
		case "redacted_thinking":
			item := map[string]any{"type": "redacted_thinking"}
			if block.Data != "" {
				item["data"] = block.Data
			}
			content = append(content, item)
		case "text":
			if block.Text != "" {
				content = append(content, map[string]any{"type": "text", "text": block.Text})
				hasAnchor = true
			}
		case "tool_use":
			var input any
			_ = json.Unmarshal(block.Input, &input)
			content = append(content, map[string]any{
				"type":  "tool_use",
				"id":    block.ID,
				"name":  block.Name,
				"input": input,
			})
			hasAnchor = true
		}
	}
	if !hasAnchor {
		return nil
	}
	return content
}
