package provider

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"go-cli-agent/internal/session"
)

func TestLiveOpenAICompatibleResponses(t *testing.T) {
	baseURL := os.Getenv("GO_CLI_AGENT_LIVE_RESPONSES_URL")
	apiKey := os.Getenv("OPENAI_API_KEY")
	model := os.Getenv("GO_CLI_AGENT_LIVE_MODEL")
	if baseURL == "" || apiKey == "" {
		t.Skip("set GO_CLI_AGENT_LIVE_RESPONSES_URL and OPENAI_API_KEY to run live responses test")
	}
	if model == "" {
		model = "gpt-5.4"
	}

	adapter := NewOpenAI(baseURL, apiKey, nil)
	result, err := adapter.RunTurn(context.Background(), TurnRequest{
		SessionID:    "live-smoke",
		Model:        model,
		SystemPrompt: "You are a test harness.",
		Messages: []session.Message{
			session.NewMessage("user", "Return exactly one finish tool call with message: live responses test ok"),
		},
		Tools: []ToolSchema{
			{
				Name:        "finish",
				Description: "finish",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"message": map[string]any{"type": "string"},
					},
					"required": []string{"message"},
				},
			},
		},
	}, func(string, map[string]any) {})
	if err != nil {
		t.Fatalf("live run: %v", err)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "finish" {
		t.Fatalf("expected finish tool call, got %#v text=%q", result.ToolCalls, result.Text)
	}
	var args struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(result.ToolCalls[0].Arguments, &args); err != nil {
		t.Fatalf("parse tool args: %v", err)
	}
	if args.Message == "" {
		t.Fatalf("expected non-empty finish message")
	}
}
