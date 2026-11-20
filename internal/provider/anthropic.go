package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"aegis-agent/internal/config"
	"aegis-agent/internal/session"
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

func buildAnthropicRequestBody(req TurnRequest) (map[string]any, error) {
	messages, err := anthropicMessages(req.Messages, req.Model, req.ProviderProfile, req.APIProvider, promptCacheEnabled(req.PromptCache))
	if err != nil {
		return nil, err
	}
	systemBlocks := anthropicSystemBlocks(req.SystemPrompt, promptCacheEnabled(req.PromptCache))
	body := map[string]any{
		"model":      req.Model,
		"messages":   messages,
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
	thinking, thinkingBudget, _, err := anthropicThinking(req)
	if err != nil {
		return nil, err
	}
	if thinking != nil {
		body["thinking"] = thinking
		// The Messages API rejects max_tokens <= thinking.budget_tokens as
		// invalid_request, and that failure is not retryable. Keep the
		// invariant here so a default max_tokens combined with a large
		// manual thinking budget cannot produce an illegal body.
		if thinkingBudget > 0 {
			body["max_tokens"] = anthropicMaxTokensWithThinking(anthropicMaxTokens(req.MaxOutputTokens), thinkingBudget)
		}
	}
	// output_config.effort is a separate wire field from thinking. Anthropic
	// accepts it on effort-capable Claude models, and MiniMax and compatible
	// gateways use it for the OpenAI effort scale. Older models and gateways
	// may reject it, so an empty value must not add the key at all.
	if effort := strings.TrimSpace(req.OutputConfigEffort); effort != "" {
		body["output_config"] = map[string]any{"effort": effort}
	}
	return body, nil
}

func (a *AnthropicAdapter) EstimateRequest(req TurnRequest) (WireRequestEstimate, error) {
	body, err := buildAnthropicRequestBody(req)
	if err != nil {
		return WireRequestEstimate{}, err
	}
	estimate, err := EstimateWireRequest(body, req)
	if err != nil {
		return WireRequestEstimate{}, err
	}
	estimate.EffectiveMaxOutputTokens = body["max_tokens"].(int)
	switch {
	case estimate.EffectiveMaxOutputTokens != anthropicMaxTokens(req.MaxOutputTokens):
		estimate.MaxOutputTokensSource = "thinking_budget"
	case req.MaxOutputTokens > 0:
		estimate.MaxOutputTokensSource = "configured"
	default:
		estimate.MaxOutputTokensSource = "adapter_default"
	}
	estimate.OutputConfigEffort = diagnosticOutputEffort(req.OutputConfigEffort)
	return estimate, nil
}

// Custom effort labels are valid on compatible gateways. Keep arbitrary
// configuration strings out of diagnostics without changing the wire request.
func diagnosticOutputEffort(effort string) string {
	switch value := strings.TrimSpace(effort); value {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return value
	default:
		return "other"
	}
}

func (a *AnthropicAdapter) RunTurn(ctx context.Context, req TurnRequest, emit EmitFunc) (TurnResult, error) {
	body, err := buildAnthropicRequestBody(req)
	if err != nil {
		return TurnResult{}, err
	}
	thinkingStrategy := anthropicThinkingStrategy(req)
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
		Usage *ReportedTokenCounts `json:"usage"`
	}
	err = a.client.DoJSON(ctx, http.MethodPost, "/v1/messages", map[string]string{
		"x-api-key":         a.apiKey,
		"anthropic-version": a.anthropicVersion,
	}, body, &resp, emit)
	if err != nil {
		return TurnResult{}, err
	}
	usage := Usage{}
	if counts := resp.Usage; counts != nil {
		usage.Reported = true
		usage.ReportedCounts = counts
		if counts.InputTokens != nil {
			usage.InputTokens = *counts.InputTokens
		}
		if counts.OutputTokens != nil {
			usage.OutputTokens = *counts.OutputTokens
		}
		if counts.CacheCreationInputTokens != nil {
			usage.CacheCreationInputTokens = *counts.CacheCreationInputTokens
		}
		if counts.CacheReadInputTokens != nil {
			usage.CacheReadInputTokens = *counts.CacheReadInputTokens
		}
		usage.setTotal("anthropic_sum", int64(usage.InputTokens), int64(usage.OutputTokens), int64(usage.CacheCreationInputTokens), int64(usage.CacheReadInputTokens))
	}
	stopReasonRaw := strings.TrimSpace(resp.StopReason)
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
				Provider:        "anthropic",
				ProviderProfile: req.ProviderProfile,
				APIProvider:     req.APIProvider,
				Type:            "text",
				Text:            item.Text,
				Model:           req.Model,
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
				Provider:        "anthropic",
				ProviderProfile: req.ProviderProfile,
				APIProvider:     req.APIProvider,
				Type:            "thinking",
				Thinking:        item.Thinking,
				Signature:       item.Signature,
				Model:           req.Model,
			})
		case "redacted_thinking":
			redactedThinkingCount++
			if item.Data != "" {
				thinkingReplayObserved = true
			}
			providerBlocks = append(providerBlocks, session.ProviderContentBlock{
				Provider:        "anthropic",
				ProviderProfile: req.ProviderProfile,
				APIProvider:     req.APIProvider,
				Type:            "redacted_thinking",
				Data:            item.Data,
				Model:           req.Model,
			})
		case "tool_use":
			if stopReasonRaw == "" {
				continue
			}
			if err := validateToolCallEnvelope("anthropic", item.Name, item.ID); err != nil {
				return TurnResult{}, err
			}
			input, err := normalizeToolCallArguments("anthropic", item.Name, item.Input)
			if err != nil {
				return TurnResult{}, err
			}
			providerBlocks = append(providerBlocks, session.ProviderContentBlock{
				Provider:        "anthropic",
				ProviderProfile: req.ProviderProfile,
				APIProvider:     req.APIProvider,
				Type:            "tool_use",
				ID:              item.ID,
				Name:            item.Name,
				Input:           input,
				Model:           req.Model,
			})
			calls = append(calls, ToolCall{
				ID:             item.ID,
				Name:           item.Name,
				Arguments:      input,
				ProviderCallID: item.ID,
			})
		}
	}
	text := strings.Join(textParts, "\n")
	if text != "" {
		emitEvent(emit, "assistant.delta", map[string]any{"text": text})
	}
	if stopReasonRaw == "tool_use" && len(calls) == 0 {
		return TurnResult{}, &HTTPError{
			Provider: "anthropic",
			Class:    "response_parse_error",
			Message:  "anthropic tool_use stop reason did not include a tool_use content block",
		}
	}
	if stopReasonRaw != "tool_use" && len(calls) > 0 {
		return TurnResult{}, &HTTPError{
			Provider: "anthropic",
			Class:    "response_parse_error",
			Message:  "anthropic response included a tool_use content block without tool_use stop reason",
		}
	}
	stopReason := "done_candidate"
	switch stopReasonRaw {
	case "tool_use":
		stopReason = "tool_use"
	case "end_turn":
		stopReason = "done_candidate"
	case "max_tokens":
		stopReason = "max_tokens"
	case "cancel", "cancelled":
		stopReason = "cancelled"
	case "pause_turn":
		stopReason = "error"
	default:
		stopReason = "error"
	}
	return TurnResult{
		Text:                  text,
		Thinking:              strings.Join(thinkingParts, "\n"),
		ProviderContentBlocks: providerBlocks,
		ToolCalls:             calls,
		StopReason:            stopReason,
		ProviderResponseID:    resp.ID,
		Usage:                 usage,
		RawProvider: rawProviderEnvelope("stop_reason", resp.StopReason, map[string]any{
			"thinking_block_count":        thinkingBlockCount,
			"redacted_thinking_count":     redactedThinkingCount,
			"thinking_visible_observed":   len(thinkingParts) > 0,
			"thinking_replay_observed":    thinkingReplayObserved,
			"thinking_strategy":           thinkingStrategy,
			"output_config_effort":        diagnosticOutputEffort(req.OutputConfigEffort),
			"prompt_cache_enabled":        promptCacheEnabled(req.PromptCache),
			"cache_creation_input_tokens": usage.CacheCreationInputTokens,
			"cache_read_input_tokens":     usage.CacheReadInputTokens,
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

// anthropicThinkingOutputHeadroom is the visible-output allowance reserved on
// top of thinking.budget_tokens, matching the webconsole settings path
// (internal/webconsole/service.go) which also lifts MaxOutputTokens to
// budget + 1024 when enabling thinking.
const anthropicThinkingOutputHeadroom = 1024

// anthropicMaxTokensWithThinking keeps max_tokens strictly above
// thinking.budget_tokens; the caller-provided limit wins whenever it already
// satisfies the invariant.
func anthropicMaxTokensWithThinking(maxTokens, budget int) int {
	if budget <= 0 || maxTokens > budget {
		return maxTokens
	}
	return budget + anthropicThinkingOutputHeadroom
}

func anthropicThinking(req TurnRequest) (map[string]any, int, string, error) {
	mode := config.NormalizeThinkingMode(req.ThinkingMode)
	switch mode {
	case "", config.ThinkingModeAdaptive, config.ThinkingModeEnabled, config.ThinkingModeDisabled:
	default:
		return nil, 0, "", fmt.Errorf("unsupported anthropic thinking_mode %q", req.ThinkingMode)
	}
	display := config.NormalizeThinkingDisplay(req.ThinkingDisplay)
	switch display {
	case "", config.ThinkingDisplaySummarized, config.ThinkingDisplayOmitted:
	default:
		return nil, 0, "", fmt.Errorf("unsupported anthropic thinking_display %q", req.ThinkingDisplay)
	}
	if req.IncludeThoughts != nil && !*req.IncludeThoughts {
		if mode == config.ThinkingModeEnabled || mode == config.ThinkingModeAdaptive {
			return nil, 0, "", fmt.Errorf("anthropic thinking_mode=%s conflicts with legacy include_thoughts=false; use thinking_display=omitted to hide summaries", mode)
		}
		mode = config.ThinkingModeDisabled
	}
	if req.ThinkingBudget != nil && *req.ThinkingBudget < 0 {
		return nil, 0, "", fmt.Errorf("anthropic thinking_budget must be non-negative, got %d", *req.ThinkingBudget)
	}

	budget := config.ThinkingBudgetValue(req.ThinkingBudget)
	switch mode {
	case config.ThinkingModeDisabled:
		return map[string]any{"type": "disabled"}, 0, "disabled", nil
	case config.ThinkingModeAdaptive:
		if budget > 0 {
			return nil, 0, "", fmt.Errorf("anthropic thinking_mode=adaptive cannot be combined with manual thinking_budget=%d", budget)
		}
		payload := map[string]any{"type": "adaptive"}
		if display != "" {
			payload["display"] = display
		}
		return payload, 0, "adaptive", nil
	case config.ThinkingModeEnabled:
		if config.AnthropicModelRequiresAdaptiveThinking(req.Model) {
			return nil, 0, "", fmt.Errorf("anthropic model %q requires adaptive thinking; manual thinking (type=\"enabled\") is not supported", req.Model)
		}
		if budget <= 0 {
			return nil, 0, "", fmt.Errorf("anthropic thinking_mode=enabled requires a positive thinking_budget")
		}
		payload := map[string]any{
			"type":          "enabled",
			"budget_tokens": budget,
		}
		if display != "" {
			payload["display"] = display
		}
		return payload, budget, "manual_budget", nil
	default:
		if budget <= 0 {
			return nil, 0, "provider_default", nil
		}
		if config.AnthropicModelRequiresAdaptiveThinking(req.Model) {
			return nil, 0, "", fmt.Errorf("anthropic model %q requires adaptive thinking; manual thinking_budget=%d is not supported", req.Model, budget)
		}
		payload := map[string]any{
			"type":          "enabled",
			"budget_tokens": budget,
		}
		if display != "" {
			payload["display"] = display
		}
		return payload, budget, "manual_budget", nil
	}
}

func anthropicThinkingStrategy(req TurnRequest) string {
	_, _, base, err := anthropicThinking(req)
	if err != nil || base == "" {
		base = "provider_default"
	}
	if strings.TrimSpace(req.OutputConfigEffort) == "" {
		return base
	}
	if base == "provider_default" {
		return "output_config_effort"
	}
	return base + "+output_config_effort"
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

func anthropicMessages(messages []session.Message, model, providerProfile, apiProvider string, enableCache bool) ([]map[string]any, error) {
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
			anthropicBlocks, err := anthropicProviderContent(msg.ProviderContentBlocks, model, providerProfile, apiProvider)
			if err != nil {
				return nil, err
			}
			if len(anthropicBlocks) > 0 {
				content = anthropicBlocks
			} else {
				if msg.Text != "" {
					content = append(content, map[string]any{"type": "text", "text": msg.Text})
				}
				for _, call := range msg.ToolCalls {
					input, err := toolCallArgumentsObject("anthropic", call.Name, call.Arguments)
					if err != nil {
						return nil, err
					}
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
	return out, nil
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

func anthropicProviderContent(blocks []session.ProviderContentBlock, model, providerProfile, apiProvider string) ([]map[string]any, error) {
	var content []map[string]any
	hasAnchor := false
	for _, block := range blocks {
		if block.Provider != "anthropic" {
			continue
		}
		scopeOK := providerReplayScopeMatches(block.Model, model) &&
			providerReplayScopeMatches(block.ProviderProfile, providerProfile) &&
			providerReplayScopeMatches(block.APIProvider, apiProvider)
		switch block.Type {
		case "thinking":
			if !scopeOK {
				continue
			}
			item := map[string]any{
				"type":     "thinking",
				"thinking": block.Thinking,
			}
			if block.Signature != "" {
				item["signature"] = block.Signature
			}
			content = append(content, item)
		case "redacted_thinking":
			if !scopeOK {
				continue
			}
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
			input, err := toolCallArgumentsObject("anthropic", block.Name, block.Input)
			if err != nil {
				return nil, err
			}
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
		return nil, nil
	}
	return content, nil
}
