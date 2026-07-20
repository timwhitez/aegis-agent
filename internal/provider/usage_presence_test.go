package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go-cli-agent/internal/session"
)

func TestProviderUsagePresenceDistinguishesMissingFromReportedZero(t *testing.T) {
	tests := []struct {
		name     string
		response func(bool) string
		adapter  func(string, *http.Client) Adapter
	}{
		{
			name: "openai",
			response: func(withUsage bool) string {
				usage := ""
				if withUsage {
					usage = `,"usage":{"input_tokens":0,"output_tokens":0}`
				}
				return `{"id":"resp-usage","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]` + usage + `}`
			},
			adapter: func(baseURL string, client *http.Client) Adapter { return NewOpenAI(baseURL, "key", client) },
		},
		{
			name: "anthropic",
			response: func(withUsage bool) string {
				usage := ""
				if withUsage {
					usage = `,"usage":{"input_tokens":0,"output_tokens":0}`
				}
				return `{"id":"msg-usage","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]` + usage + `}`
			},
			adapter: func(baseURL string, client *http.Client) Adapter {
				return NewAnthropic(baseURL, "key", "2023-06-01", client)
			},
		},
		{
			name: "google",
			response: func(withUsage bool) string {
				usage := ""
				if withUsage {
					usage = `,"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":0}`
				}
				return `{"responseId":"resp-usage","candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]` + usage + `}`
			},
			adapter: func(baseURL string, client *http.Client) Adapter { return NewGoogle(baseURL, "key", client) },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, withUsage := range []bool{false, true} {
				label := "missing"
				if withUsage {
					label = "reported_zero"
				}
				t.Run(label, func(t *testing.T) {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(tc.response(withUsage)))
					}))
					defer server.Close()
					result, err := tc.adapter(server.URL, server.Client()).RunTurn(context.Background(), TurnRequest{
						SessionID: "usage-presence", Model: "fixture", SystemPrompt: "system",
						Messages: []session.Message{session.NewMessage("user", "hello")},
					}, func(string, map[string]any) {})
					if err != nil {
						t.Fatalf("run: %v", err)
					}
					if result.Usage.Reported != withUsage {
						t.Fatalf("reported=%v want %v usage=%#v", result.Usage.Reported, withUsage, result.Usage)
					}
					if result.Usage.InputTokens != 0 || result.Usage.OutputTokens != 0 {
						t.Fatalf("expected zero counters, got %#v", result.Usage)
					}
				})
			}
		})
	}
}
