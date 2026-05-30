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
	contents, err := googleContents(req.Messages, req.Model, req.ProviderProfile, req.APIProvider)
	if err != nil {
		return TurnResult{}, err
	}
	body := map[string]any{
		"systemInstruction": map[string]any{
			"parts": []map[string]any{{"text": req.SystemPrompt}},
		},
		"contents": contents,
		"tools": []map[string]any{
			{"functionDeclarations": googleTools(req.Tools)},
		},
	}
	if generationConfig := googleGenerationConfig(req); len(generationConfig) > 0 {
		body["generationConfig"] = generationConfig
	}
	thinkingStrategy := googleThinkingStrategy(req)
	var resp struct {
		ResponseID   string `json:"responseId"`
		ModelVersion string `json:"modelVersion"`
		Candidates   []struct {
			Content struct {
				Parts []struct {
					Text         string `json:"text"`
					Thought      bool   `json:"thought"`
					ThoughtSig   string `json:"thoughtSignature"`
					FunctionCall *struct {
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
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
	}
	path := fmt.Sprintf("/v1beta/models/%s:generateContent", req.Model)
	err = a.client.DoJSON(ctx, http.MethodPost, path, map[string]string{
		"x-goog-api-key": a.apiKey,
	}, body, &resp, emit)
	if err != nil {
		return TurnResult{}, err
	}
	if len(resp.Candidates) == 0 {
		if blockReason := strings.TrimSpace(resp.PromptFeedback.BlockReason); blockReason != "" {
			return TurnResult{
				StopReason:         "blocked",
				ProviderResponseID: resp.ResponseID,
				Usage: Usage{
					InputTokens:  resp.UsageMetadata.PromptTokenCount,
					OutputTokens: resp.UsageMetadata.CandidatesTokenCount,
				},
				RawProvider: rawProviderEnvelope("block_reason", blockReason, map[string]any{
					"model_version":             resp.ModelVersion,
					"thinking_visible_observed": false,
					"thinking_replay_observed":  false,
					"thinking_strategy":         thinkingStrategy,
				}),
			}, nil
		}
		return TurnResult{}, fmt.Errorf("google: empty candidates")
	}
	candidate := resp.Candidates[0]
	var textParts []string
	var thinkingParts []string
	var providerBlocks []session.ProviderContentBlock
	var calls []ToolCall
	thoughtPartCount := 0
	thoughtSignatureCount := 0
	seenCallIDs := map[string]struct{}{}
	for _, part := range candidate.Content.Parts {
		if part.Thought {
			thoughtPartCount++
			if part.Text != "" {
				thinkingParts = append(thinkingParts, part.Text)
			}
			if part.ThoughtSig != "" {
				thoughtSignatureCount++
			}
			thought := true
			providerBlocks = append(providerBlocks, session.ProviderContentBlock{
				Provider:         "google",
				ProviderProfile:  req.ProviderProfile,
				APIProvider:      req.APIProvider,
				Type:             "part",
				Text:             part.Text,
				Thought:          &thought,
				ThoughtSignature: part.ThoughtSig,
				Model:            req.Model,
			})
			continue
		}
		if part.Text != "" {
			if part.ThoughtSig != "" {
				thoughtSignatureCount++
			}
			textParts = append(textParts, part.Text)
			providerBlocks = append(providerBlocks, session.ProviderContentBlock{
				Provider:         "google",
				ProviderProfile:  req.ProviderProfile,
				APIProvider:      req.APIProvider,
				Type:             "part",
				Text:             part.Text,
				ThoughtSignature: part.ThoughtSig,
				Model:            req.Model,
			})
		}
		if part.FunctionCall != nil {
			if strings.TrimSpace(part.FunctionCall.Name) == "" {
				return TurnResult{}, &HTTPError{
					Provider: "google",
					Class:    "response_parse_error",
					Message:  "tool-call name is required",
				}
			}
			args, err := normalizeToolCallArguments("google", part.FunctionCall.Name, part.FunctionCall.Args)
			if err != nil {
				return TurnResult{}, err
			}
			if part.ThoughtSig != "" {
				thoughtSignatureCount++
			}
			providerCallID := strings.TrimSpace(part.FunctionCall.ID)
			callID := providerCallID
			if callID == "" {
				callID = "call_" + part.FunctionCall.Name
			}
			callID = uniqueGoogleToolCallID(callID, seenCallIDs)
			providerBlocks = append(providerBlocks, session.ProviderContentBlock{
				Provider:         "google",
				ProviderProfile:  req.ProviderProfile,
				APIProvider:      req.APIProvider,
				Type:             "function_call",
				Name:             part.FunctionCall.Name,
				ID:               callID,
				Args:             args,
				ThoughtSignature: part.ThoughtSig,
				Model:            req.Model,
			})
			calls = append(calls, ToolCall{
				ID:             callID,
				Name:           part.FunctionCall.Name,
				Arguments:      args,
				ProviderCallID: providerCallID,
			})
		}
	}
	text := strings.Join(textParts, "\n")
	if text != "" {
		emit("assistant.delta", map[string]any{"text": text})
	}
	stopReason := "done_candidate"
	suppressFunctionCalls := false
	finishReason := strings.TrimSpace(candidate.FinishReason)
	switch {
	case finishReason == "STOP":
		if len(calls) > 0 {
			stopReason = "tool_use"
			break
		}
		stopReason = "done_candidate"
	case finishReason == "MAX_TOKENS":
		stopReason = "max_tokens"
		suppressFunctionCalls = true
	case finishReason == "SAFETY":
		stopReason = "blocked"
		suppressFunctionCalls = true
	case providerStopReasonIsCancelled(finishReason):
		stopReason = "cancelled"
		suppressFunctionCalls = true
	case finishReason != "":
		stopReason = "error"
		suppressFunctionCalls = true
	case len(calls) > 0:
		stopReason = "error"
		suppressFunctionCalls = true
	case finishReason == "":
		stopReason = "error"
	}
	if suppressFunctionCalls {
		calls = nil
		providerBlocks = dropGoogleFunctionCallBlocks(providerBlocks)
	}
	return TurnResult{
		Text:                  text,
		Thinking:              strings.Join(thinkingParts, "\n"),
		ProviderContentBlocks: providerBlocks,
		ToolCalls:             calls,
		StopReason:            stopReason,
		ProviderResponseID:    resp.ResponseID,
		Usage: Usage{
			InputTokens:  resp.UsageMetadata.PromptTokenCount,
			OutputTokens: resp.UsageMetadata.CandidatesTokenCount,
		},
		RawProvider: rawProviderEnvelope("finish_reason", candidate.FinishReason, map[string]any{
			"model_version":             resp.ModelVersion,
			"thought_part_count":        thoughtPartCount,
			"thought_signature_count":   thoughtSignatureCount,
			"thinking_visible_observed": len(thinkingParts) > 0,
			"thinking_replay_observed":  thoughtSignatureCount > 0,
			"thinking_strategy":         thinkingStrategy,
		}),
	}, nil
}

func uniqueGoogleToolCallID(base string, seen map[string]struct{}) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "call_google"
	}
	candidate := base
	for suffix := 2; ; suffix++ {
		if _, exists := seen[candidate]; !exists {
			seen[candidate] = struct{}{}
			return candidate
		}
		candidate = fmt.Sprintf("%s_%d", base, suffix)
	}
}

func dropGoogleFunctionCallBlocks(blocks []session.ProviderContentBlock) []session.ProviderContentBlock {
	if len(blocks) == 0 {
		return blocks
	}
	filtered := blocks[:0]
	for _, block := range blocks {
		if block.Provider == "google" && block.Type == "function_call" {
			continue
		}
		filtered = append(filtered, block)
	}
	return filtered
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

func googleThinkingStrategy(req TurnRequest) string {
	if req.IncludeThoughts != nil && !*req.IncludeThoughts {
		return "off"
	}
	includeThoughts := req.IncludeThoughts != nil && *req.IncludeThoughts
	switch {
	case req.ThinkingBudget > 0 && includeThoughts:
		return "thinking_budget_include_thoughts"
	case req.ThinkingBudget > 0:
		return "thinking_budget"
	case includeThoughts:
		return "include_thoughts"
	default:
		return "provider_default"
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

func googleContents(messages []session.Message, model, providerProfile, apiProvider string) ([]map[string]any, error) {
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
			googleBlocks, err := googleProviderParts(msg.ProviderContentBlocks, model, providerProfile, apiProvider)
			if err != nil {
				return nil, err
			}
			if len(googleBlocks) > 0 {
				parts = googleBlocks
			} else if msg.Text != "" {
				parts = append(parts, map[string]any{"text": msg.Text})
				for _, call := range msg.ToolCalls {
					args, err := toolCallArgumentsObject("google", call.Name, call.Arguments)
					if err != nil {
						return nil, err
					}
					parts = append(parts, map[string]any{
						"functionCall": map[string]any{
							"name": call.Name,
							"id":   call.ID,
							"args": args,
						},
					})
				}
			} else {
				for _, call := range msg.ToolCalls {
					args, err := toolCallArgumentsObject("google", call.Name, call.Arguments)
					if err != nil {
						return nil, err
					}
					parts = append(parts, map[string]any{
						"functionCall": map[string]any{
							"name": call.Name,
							"id":   call.ID,
							"args": args,
						},
					})
				}
			}
			if len(parts) == 0 {
				continue
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
	return out, nil
}

func googleProviderParts(blocks []session.ProviderContentBlock, model, providerProfile, apiProvider string) ([]map[string]any, error) {
	var parts []map[string]any
	hasAnchor := false
	for _, block := range blocks {
		if block.Provider != "google" {
			continue
		}
		scopeOK := providerReplayScopeMatches(block.Model, model) &&
			providerReplayScopeMatches(block.ProviderProfile, providerProfile) &&
			providerReplayScopeMatches(block.APIProvider, apiProvider)
		if block.Thought != nil && *block.Thought && !scopeOK {
			continue
		}
		part := map[string]any{}
		if block.Text != "" {
			part["text"] = block.Text
			if block.Thought == nil || !*block.Thought {
				hasAnchor = true
			}
		}
		if block.Thought != nil {
			part["thought"] = *block.Thought
		}
		if block.ThoughtSignature != "" && scopeOK {
			part["thoughtSignature"] = block.ThoughtSignature
		}
		if block.Type == "function_call" && block.Name != "" {
			args, err := toolCallArgumentsObject("google", block.Name, block.Args)
			if err != nil {
				return nil, err
			}
			part["functionCall"] = map[string]any{
				"name": block.Name,
				"id":   block.ID,
				"args": args,
			}
			hasAnchor = true
		}
		if len(part) > 0 {
			parts = append(parts, part)
		}
	}
	if !hasAnchor {
		return nil, nil
	}
	return parts, nil
}

func isJSON(input string) bool {
	input = strings.TrimSpace(input)
	return strings.HasPrefix(input, "{") || strings.HasPrefix(input, "[")
}
