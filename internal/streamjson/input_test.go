package streamjson

import (
	"strings"
	"testing"
)

func TestReadInitialPrompt(t *testing.T) {
	prompt, err := ReadInitialPrompt(strings.NewReader(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"},{"type":"text","text":"world"}]},"run_role":"worker","metadata":{"mission_id":"mis_1"}}`), 1024)
	if err != nil {
		t.Fatalf("read prompt: %v", err)
	}
	if prompt != "hello\nworld" {
		t.Fatalf("unexpected prompt %q", prompt)
	}
}

func TestReadInitialPromptRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty", input: "", want: "empty"},
		{name: "invalid_json", input: "{", want: "invalid stream-json input"},
		{name: "wrong_type", input: `{"type":"assistant","message":{"role":"user","content":[{"type":"text","text":"x"}]}}`, want: "type must be user"},
		{name: "wrong_role", input: `{"type":"user","message":{"role":"assistant","content":[{"type":"text","text":"x"}]}}`, want: "role must be user"},
		{name: "wrong_block", input: `{"type":"user","message":{"role":"user","content":[{"type":"image","text":"x"}]}}`, want: "only supports text"},
		{name: "empty_text", input: `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"   "} ]}}`, want: "prompt is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadInitialPrompt(strings.NewReader(tc.input), 1024)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
}

func TestReadInitialPromptRejectsOversizedInput(t *testing.T) {
	_, err := ReadInitialPrompt(strings.NewReader(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"toolong"}]}}`), 12)
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum size") {
		t.Fatalf("expected oversized input error, got %v", err)
	}
}
