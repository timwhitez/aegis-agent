package provider

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCanonicalTokenUsage(t *testing.T) {
	tests := []struct {
		name, api, usage string
		total            int64
		known            bool
	}{
		{"claude plain", "anthropic", `"input_tokens":1,"output_tokens":1`, 2, true},
		{"claude read", "anthropic", `"input_tokens":1,"output_tokens":1,"cache_read_input_tokens":99`, 101, true},
		{"claude create", "anthropic", `"input_tokens":1,"output_tokens":1,"cache_creation_input_tokens":99`, 101, true},
		{"claude both", "anthropic", `"input_tokens":1,"output_tokens":1,"cache_creation_input_tokens":40,"cache_read_input_tokens":59`, 101, true},
		{"google thoughts", "google", `"promptTokenCount":10,"candidatesTokenCount":5,"thoughtsTokenCount":100,"totalTokenCount":115`, 115, true},
		{"google sum", "google", `"promptTokenCount":10,"candidatesTokenCount":5,"thoughtsTokenCount":100`, 115, true},
		{"google no thoughts", "google", `"promptTokenCount":10,"candidatesTokenCount":5`, 15, true},
		{"google zero thoughts", "google", `"promptTokenCount":10,"candidatesTokenCount":5,"thoughtsTokenCount":0`, 15, true},
		{"google cached included", "google", `"promptTokenCount":10,"cachedContentTokenCount":8,"candidatesTokenCount":5`, 15, true},
		{"google authoritative larger", "google", `"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":120`, 120, true},
		{"google inconsistent total", "google", `"promptTokenCount":10,"candidatesTokenCount":5,"thoughtsTokenCount":100,"totalTokenCount":15`, 0, false},
		{"google negative thoughts", "google", `"promptTokenCount":10,"candidatesTokenCount":5,"thoughtsTokenCount":-1`, 0, false},
		{"openai breakdown included", "openai", `"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":80},"output_tokens_details":{"reasoning_tokens":10}`, 120, true},
		{"negative input", "anthropic", `"input_tokens":-1,"output_tokens":1`, 0, false},
		{"negative cache", "openai", `"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":-1}`, 0, false},
		{"negative total", "google", `"promptTokenCount":1,"totalTokenCount":-1`, 0, false},
		{"overflow", "anthropic", fmt.Sprintf(`"input_tokens":%d,"output_tokens":1`, math.MaxInt64), math.MaxInt64, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var body string
			var adapter func(string, *http.Client) Adapter
			switch tc.api {
			case "anthropic":
				body = `{"stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{` + tc.usage + `}}`
				adapter = func(url string, c *http.Client) Adapter { return NewAnthropic(url, "key", "2023-06-01", c) }
			case "google":
				body = `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{` + tc.usage + `}}`
				adapter = func(url string, c *http.Client) Adapter { return NewGoogle(url, "key", c) }
			default:
				body = `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{` + tc.usage + `}}`
				adapter = func(url string, c *http.Client) Adapter { return NewOpenAI(url, "key", c) }
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, body)
			}))
			defer server.Close()
			result, err := adapter(server.URL, server.Client()).RunTurn(context.Background(), TurnRequest{Model: "fixture"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			total, known := result.Usage.TokenTotal()
			if total != tc.total || known != tc.known {
				t.Fatalf("total=(%d,%v), want=(%d,%v), usage=%#v", total, known, tc.total, tc.known, result.Usage)
			}
			if tc.api == "google" && tc.name == "google zero thoughts" && (result.Usage.ReasoningTokens == nil || *result.Usage.ReasoningTokens != 0) {
				t.Fatal("lost explicit thoughts zero")
			}
			if tc.api == "google" && tc.name == "google no thoughts" && result.Usage.ReasoningTokens != nil {
				t.Fatal("invented thoughts presence")
			}
		})
	}
}
