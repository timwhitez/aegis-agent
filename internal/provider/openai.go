package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"aegis-agent/internal/session"
)

type OpenAIAdapter struct {
	client              JSONClient
	apiKey              string
	metadataUnsupported atomic.Bool
}

const maxOpenAIRefusalTextBytes = 8 << 10

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

func buildOpenAIRequestBody(req TurnRequest, includeMetadata bool) (map[string]any, error) {
	input, err := openAIInput(req.Messages, req.Model, req.ProviderProfile, req.APIProvider)
	if err != nil {
		return nil, err
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
	reasoning := map[string]any{}
	if strings.TrimSpace(req.ReasoningEffort) != "" {
		reasoning["effort"] = req.ReasoningEffort
	}
	if summary := normalizeOpenAIReasoningSummary(req.ReasoningSummary); summary != "" && summary != "none" {
		reasoning["summary"] = summary
	}
	if len(reasoning) > 0 {
		body["reasoning"] = reasoning
		body["include"] = []string{"reasoning.encrypted_content"}
	}
	if strings.TrimSpace(req.TextVerbosity) != "" {
		body["text"] = map[string]any{
			"verbosity": req.TextVerbosity,
		}
	}
	if req.Store != nil {
		body["store"] = *req.Store
	}
	if includeMetadata && len(req.Metadata) > 0 {
		body["metadata"] = req.Metadata
	}
	return body, nil
}

func (a *OpenAIAdapter) EstimateRequest(req TurnRequest) (WireRequestEstimate, error) {
	body, err := buildOpenAIRequestBody(req, !a.metadataUnsupported.Load())
	if err != nil {
		return WireRequestEstimate{}, err
	}
	return EstimateWireRequest(body, req)
}

func (a *OpenAIAdapter) RunTurn(ctx context.Context, req TurnRequest, emit EmitFunc) (TurnResult, error) {
	metadataRequested := len(req.Metadata) > 0
	metadataSent := metadataRequested && !a.metadataUnsupported.Load()
	metadataFallback := false
	body, err := buildOpenAIRequestBody(req, metadataSent)
	if err != nil {
		return TurnResult{}, err
	}
	thinkingStrategy := openAIThinkingStrategy(req)
	var resp struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Output []struct {
			ID               string                `json:"id"`
			Type             string                `json:"type"`
			CallID           string                `json:"call_id"`
			Name             string                `json:"name"`
			Arguments        string                `json:"arguments"`
			Role             string                `json:"role"`
			EncryptedContent string                `json:"encrypted_content"`
			Content          []openAIReasoningText `json:"content"`
			Summary          []openAIReasoningText `json:"summary"`
		} `json:"output"`
		Usage *struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			InputTokensDetails struct {
				CachedTokens     int `json:"cached_tokens"`
				CacheWriteTokens int `json:"cache_write_tokens"`
			} `json:"input_tokens_details"`
			OutputTokensDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
		IncompleteDetails struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	}
	err = a.client.DoJSON(ctx, http.MethodPost, "/responses", map[string]string{
		"Authorization": "Bearer " + a.apiKey,
	}, body, &resp, emit)
	if err != nil && metadataSent && isUnsupportedOpenAIMetadataError(err) {
		var httpErr *HTTPError
		_ = errors.As(err, &httpErr)
		a.metadataUnsupported.Store(true)
		delete(body, "metadata")
		metadataSent = false
		metadataFallback = true
		data := map[string]any{
			"provider":         "openai",
			"provider_profile": req.ProviderProfile,
			"api_provider":     req.APIProvider,
			"feature":          "metadata",
			"reason":           "unsupported_argument",
		}
		if httpErr != nil && httpErr.StatusCode > 0 {
			data["status_code"] = httpErr.StatusCode
		}
		emitEvent(emit, "provider.capability_fallback", data)
		err = a.client.DoJSON(ctx, http.MethodPost, "/responses", map[string]string{
			"Authorization": "Bearer " + a.apiKey,
		}, body, &resp, emit)
	}
	if err != nil {
		return TurnResult{}, err
	}
	status := strings.TrimSpace(resp.Status)
	incompleteReason := strings.TrimSpace(resp.IncompleteDetails.Reason)
	var textParts []string
	var refusalParts []string
	var thinkingParts []string
	var providerBlocks []session.ProviderContentBlock
	var calls []ToolCall
	reasoningSummaryCount := 0
	reasoningTextCount := 0
	reasoningEncryptedCount := 0
	refusalBlockCount := 0
	// Refusal is a response-level safety boundary. Detect it before touching any
	// co-returned function call so even a malformed call cannot turn a refusal
	// into an adapter parse error or expose an executable action.
	for _, item := range resp.Output {
		if item.Type != "message" {
			continue
		}
		for _, content := range item.Content {
			if content.Type != "refusal" {
				continue
			}
			refusalBlockCount++
			refusal := strings.TrimSpace(content.Refusal)
			if refusal == "" {
				// Some Responses-compatible gateways use the generic text member
				// while retaining the refusal content type.
				refusal = strings.TrimSpace(content.Text)
			}
			if refusal != "" {
				refusalParts = append(refusalParts, refusal)
			}
		}
	}
	allowFunctionCalls := status == "completed" && incompleteReason != "max_output_tokens" && refusalBlockCount == 0
	for index, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, content := range item.Content {
				switch content.Type {
				case "output_text":
					textParts = append(textParts, content.Text)
				}
			}
		case "function_call":
			if !allowFunctionCalls {
				continue
			}
			if err := validateToolCallEnvelope("openai", item.Name, item.CallID); err != nil {
				return TurnResult{}, err
			}
			arguments, err := normalizeToolCallArguments("openai", item.Name, json.RawMessage(item.Arguments))
			if err != nil {
				return TurnResult{}, err
			}
			calls = append(calls, ToolCall{
				ID:             item.CallID,
				Name:           item.Name,
				Arguments:      arguments,
				ProviderCallID: item.CallID,
			})
		case "reasoning":
			summaryParts := reasoningSummaryTexts(item.Summary)
			if len(summaryParts) > 0 {
				thinkingParts = append(thinkingParts, summaryParts...)
				reasoningSummaryCount += len(summaryParts)
			}
			contentParts := reasoningContentTexts(item.Content)
			if len(contentParts) > 0 {
				thinkingParts = append(thinkingParts, contentParts...)
				reasoningTextCount += len(contentParts)
			}
			if strings.TrimSpace(item.EncryptedContent) != "" {
				reasoningEncryptedCount++
				providerBlocks = append(providerBlocks, session.ProviderContentBlock{
					Provider:        "openai",
					ProviderProfile: req.ProviderProfile,
					APIProvider:     req.APIProvider,
					Type:            "reasoning",
					ID:              item.ID,
					Data:            item.EncryptedContent,
					Summary:         summaryParts,
					Sequence:        index + 1,
					Model:           req.Model,
				})
			}
		}
	}
	refusalText, refusalOriginalBytes, refusalTruncated := boundedOpenAIRefusalText(refusalParts)
	if refusalText != "" {
		textParts = append(textParts, refusalText)
	}
	text := strings.Join(textParts, "\n")
	thinking := strings.Join(thinkingParts, "\n")
	if text != "" {
		emitEvent(emit, "assistant.delta", map[string]any{"text": text})
	}
	stopReason := "done_candidate"
	switch {
	case incompleteReason == "max_output_tokens":
		stopReason = "max_tokens"
		calls = nil
	case providerStopReasonIsCancelled(status):
		stopReason = "cancelled"
		calls = nil
	case status == "completed" && refusalBlockCount > 0:
		stopReason = "blocked"
		calls = nil
	case status == "completed" && len(calls) > 0:
		stopReason = "tool_use"
	case status == "completed":
		stopReason = "done_candidate"
	case status != "":
		stopReason = "error"
		calls = nil
	case len(calls) > 0:
		stopReason = "error"
		calls = nil
	case status == "":
		stopReason = "error"
	}
	usage := Usage{}
	if resp.Usage != nil {
		usage = Usage{
			Reported:                 true,
			InputTokens:              resp.Usage.InputTokens,
			OutputTokens:             resp.Usage.OutputTokens,
			CacheCreationInputTokens: resp.Usage.InputTokensDetails.CacheWriteTokens,
			CacheReadInputTokens:     resp.Usage.InputTokensDetails.CachedTokens,
		}
	}
	rawStopSource := "status"
	rawStopReason := resp.Status
	rawExtras := map[string]any{
		"reasoning_summary_count":      reasoningSummaryCount,
		"reasoning_text_count":         reasoningTextCount,
		"reasoning_encrypted_count":    reasoningEncryptedCount,
		"refusal_block_count":          refusalBlockCount,
		"refusal_text_original_bytes":  refusalOriginalBytes,
		"refusal_text_retained_bytes":  len(refusalText),
		"refusal_text_truncated":       refusalTruncated,
		"reasoning_tokens":             0,
		"cache_creation_input_tokens":  usage.CacheCreationInputTokens,
		"cache_read_input_tokens":      usage.CacheReadInputTokens,
		"thinking_visible_observed":    len(thinkingParts) > 0,
		"thinking_replay_observed":     reasoningEncryptedCount > 0,
		"thinking_strategy":            thinkingStrategy,
		"metadata_requested":           metadataRequested,
		"metadata_sent":                metadataSent,
		"metadata_capability_fallback": metadataFallback,
	}
	if resp.Usage != nil {
		rawExtras["reasoning_tokens"] = resp.Usage.OutputTokensDetails.ReasoningTokens
	}
	if incompleteReason != "" {
		rawStopSource = "incomplete_details.reason"
		rawStopReason = resp.IncompleteDetails.Reason
		rawExtras["status"] = resp.Status
	}
	return TurnResult{
		Text:                  text,
		Thinking:              thinking,
		ProviderContentBlocks: providerBlocks,
		ToolCalls:             calls,
		StopReason:            stopReason,
		ProviderResponseID:    resp.ID,
		Usage:                 usage,
		RawProvider:           rawProviderEnvelope(rawStopSource, rawStopReason, rawExtras),
	}, nil
}

func isUnsupportedOpenAIMetadataError(err error) bool {
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Class != "invalid_request" {
		return false
	}
	if httpErr.StatusCode != http.StatusBadRequest && httpErr.StatusCode != http.StatusUnprocessableEntity {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(httpErr.Message))
	if !strings.Contains(message, "metadata") {
		return false
	}
	for _, marker := range []string{
		"argument not supported",
		"unsupported argument",
		"unsupported parameter",
		"parameter not supported",
		"unknown argument",
		"unknown parameter",
		"unrecognized argument",
		"unrecognized parameter",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func openAIThinkingStrategy(req TurnRequest) string {
	summary := normalizeOpenAIReasoningSummary(req.ReasoningSummary)
	effort := strings.TrimSpace(req.ReasoningEffort)
	switch {
	case effort != "" && summary != "" && summary != "none":
		return "responses_reasoning_summary"
	case effort != "":
		return "responses_reasoning"
	case summary != "" && summary != "none":
		return "responses_summary"
	case summary == "none":
		return "responses_summary_off"
	default:
		return "provider_default"
	}
}

func normalizeOpenAIReasoningSummary(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "auto", "concise", "detailed", "none":
		return strings.ToLower(strings.TrimSpace(value))
	case "off":
		return "none"
	default:
		return ""
	}
}

type openAIReasoningText struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

func boundedOpenAIRefusalText(parts []string) (string, int, bool) {
	joined := strings.Join(parts, "\n")
	originalBytes := len(joined)
	if originalBytes <= maxOpenAIRefusalTextBytes {
		return joined, originalBytes, false
	}
	const notice = "\n...[refusal truncated]"
	prefixBytes := maxOpenAIRefusalTextBytes - len(notice)
	for prefixBytes > 0 && !utf8.RuneStart(joined[prefixBytes]) {
		prefixBytes--
	}
	return joined[:prefixBytes] + notice, originalBytes, true
}

func reasoningSummaryTexts(parts []openAIReasoningText) []string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		text := strings.TrimSpace(part.Text)
		if text == "" {
			continue
		}
		switch part.Type {
		case "", "summary_text", "text":
			out = append(out, text)
		}
	}
	return out
}

func reasoningContentTexts(parts []openAIReasoningText) []string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		text := strings.TrimSpace(part.Text)
		if text == "" {
			continue
		}
		switch part.Type {
		case "", "reasoning_text", "text":
			out = append(out, text)
		}
	}
	return out
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

func openAIInput(messages []session.Message, model string, scope ...string) ([]any, error) {
	providerProfile := ""
	apiProvider := ""
	if len(scope) > 0 {
		providerProfile = scope[0]
	}
	if len(scope) > 1 {
		apiProvider = scope[1]
	}
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
			for _, item := range openAIReasoningReplayItems(msg, model, providerProfile, apiProvider) {
				input = append(input, item)
			}
			if msg.Text != "" {
				input = append(input, map[string]any{
					"role": "assistant",
					"content": []map[string]any{
						{"type": "output_text", "text": msg.Text},
					},
				})
			}
			for _, call := range msg.ToolCalls {
				arguments, err := normalizeToolCallArguments("openai", call.Name, call.Arguments)
				if err != nil {
					return nil, err
				}
				input = append(input, map[string]any{
					"type":      "function_call",
					"call_id":   call.ID,
					"name":      call.Name,
					"arguments": string(arguments),
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

func openAIReasoningReplayItems(msg session.Message, model, providerProfile, apiProvider string) []map[string]any {
	if msg.Text != "" && len(msg.ToolCalls) > 0 {
		return nil
	}
	type replayBlock struct {
		sequence int
		item     map[string]any
	}
	var blocks []replayBlock
	for _, block := range msg.ProviderContentBlocks {
		if block.Provider != "openai" || block.Type != "reasoning" {
			continue
		}
		if strings.TrimSpace(block.ID) == "" || strings.TrimSpace(block.Data) == "" {
			continue
		}
		if !providerReplayScopeMatches(block.Model, model) {
			continue
		}
		if !providerReplayScopeMatches(block.ProviderProfile, providerProfile) {
			continue
		}
		if !providerReplayScopeMatches(block.APIProvider, apiProvider) {
			continue
		}
		item := map[string]any{
			"type":              "reasoning",
			"id":                block.ID,
			"encrypted_content": block.Data,
			"summary":           []map[string]any{},
		}
		if len(block.Summary) > 0 {
			summary := make([]map[string]any, 0, len(block.Summary))
			for _, text := range block.Summary {
				if strings.TrimSpace(text) == "" {
					continue
				}
				summary = append(summary, map[string]any{"type": "summary_text", "text": text})
			}
			if len(summary) > 0 {
				item["summary"] = summary
			}
		}
		blocks = append(blocks, replayBlock{sequence: block.Sequence, item: item})
	}
	sort.SliceStable(blocks, func(i, j int) bool {
		return blocks[i].sequence < blocks[j].sequence
	})
	out := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		out = append(out, block.item)
	}
	return out
}
