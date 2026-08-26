package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

type FakeAdapter struct {
	mu       sync.Mutex
	name     string
	sequence []func(context.Context, TurnRequest) (TurnResult, error)
	index    int
}

func NewFake(sequence ...func(context.Context, TurnRequest) (TurnResult, error)) *FakeAdapter {
	return &FakeAdapter{name: "fake", sequence: sequence}
}

func (f *FakeAdapter) Name() string { return f.name }

func buildFakeRequestBody(req TurnRequest) map[string]any {
	body := map[string]any{
		"model":         req.Model,
		"system_prompt": req.SystemPrompt,
		"messages":      req.Messages,
		"tools":         req.Tools,
	}
	if len(req.Metadata) > 0 {
		body["metadata"] = req.Metadata
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
	if strings.TrimSpace(req.ProviderProfile) != "" {
		body["provider_profile"] = req.ProviderProfile
	}
	if strings.TrimSpace(req.APIProvider) != "" {
		body["api_provider"] = req.APIProvider
	}
	if strings.TrimSpace(req.ReasoningEffort) != "" {
		body["reasoning_effort"] = req.ReasoningEffort
	}
	if strings.TrimSpace(req.ReasoningSummary) != "" {
		body["reasoning_summary"] = req.ReasoningSummary
	}
	if strings.TrimSpace(req.TextVerbosity) != "" {
		body["text_verbosity"] = req.TextVerbosity
	}
	if req.ThinkingBudget > 0 {
		body["thinking_budget"] = req.ThinkingBudget
	}
	if req.IncludeThoughts != nil {
		body["include_thoughts"] = *req.IncludeThoughts
	}
	if req.PromptCache != nil {
		body["prompt_cache"] = *req.PromptCache
	}
	if req.Store != nil {
		body["store"] = *req.Store
	}
	return body
}

func (f *FakeAdapter) EstimateRequest(req TurnRequest) (WireRequestEstimate, error) {
	return EstimateWireRequest(buildFakeRequestBody(req), req)
}

func (f *FakeAdapter) RunTurn(ctx context.Context, req TurnRequest, _ EmitFunc) (TurnResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.index >= len(f.sequence) {
		if len(f.sequence) == 0 {
			return fakeDefaultTurn(ctx, req)
		}
		return TurnResult{}, nil
	}
	fn := f.sequence[f.index]
	f.index++
	return fn(ctx, req)
}

func fakeDefaultTurn(ctx context.Context, req TurnRequest) (TurnResult, error) {
	select {
	case <-ctx.Done():
		return TurnResult{}, ctx.Err()
	default:
	}

	if len(req.Messages) == 0 {
		return TurnResult{Text: "Fake provider ready.", StopReason: "done_candidate"}, nil
	}

	last := req.Messages[len(req.Messages)-1]
	switch last.Role {
	case "tool":
		if len(last.ToolResults) == 0 {
			return TurnResult{Text: "Fake provider observed an empty tool result.", StopReason: "done_candidate"}, nil
		}
		result := last.ToolResults[0]
		return TurnResult{
			Text:       fmt.Sprintf("Fake provider observed tool %s: %s", result.Name, result.DisplayOutput),
			StopReason: "done_candidate",
		}, nil
	case "user":
		text := strings.TrimSpace(last.Text)
		switch {
		case strings.Contains(strings.ToLower(text), "call finish"),
			strings.Contains(strings.ToLower(text), "finish tool"),
			strings.Contains(strings.ToLower(text), "explicitly finish"):
			return TurnResult{
				ToolCalls: []ToolCall{{
					ID:        "fake_finish_1",
					Name:      "finish",
					Arguments: json.RawMessage(`{"message":"Fake provider completed the task."}`),
				}},
				StopReason: "tool_use",
			}, nil
		case strings.Contains(strings.ToLower(text), "use shell"),
			strings.Contains(strings.ToLower(text), "run shell"),
			strings.Contains(strings.ToLower(text), "pwd"):
			return TurnResult{
				ToolCalls: []ToolCall{{
					ID:        "fake_shell_1",
					Name:      "shell",
					Arguments: json.RawMessage(`{"command":"pwd"}`),
				}},
				StopReason: "tool_use",
			}, nil
		case strings.Contains(strings.ToLower(text), "load skill"):
			return TurnResult{
				ToolCalls: []ToolCall{{
					ID:        "fake_skill_1",
					Name:      "load_skill",
					Arguments: json.RawMessage(`{"name":"example"}`),
				}},
				StopReason: "tool_use",
			}, nil
		default:
			return TurnResult{
				Text:       "Fake provider reply: " + text,
				StopReason: "done_candidate",
			}, nil
		}
	case "assistant":
		if len(last.ToolCalls) > 0 {
			return TurnResult{
				Text:       "Fake provider is waiting for tool results.",
				StopReason: "done_candidate",
			}, nil
		}
	case "system":
		return TurnResult{Text: "Fake provider received a system note.", StopReason: "done_candidate"}, nil
	}

	return TurnResult{
		Text:       fmt.Sprintf("Fake provider saw role %s.", last.Role),
		StopReason: "done_candidate",
	}, nil
}
