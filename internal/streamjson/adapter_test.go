package streamjson

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"aegis-agent/internal/events"
)

func TestAdapterConvertsEventsToStreamJSON(t *testing.T) {
	var out bytes.Buffer
	adapter := NewAdapter(&out)
	adapter.Handle(events.New("s1", "session.created", "prepare", map[string]any{}))
	adapter.Handle(events.New("s1", "session.started", "prepare", map[string]any{}))
	adapter.Handle(events.New("s1", "assistant.message", "assistant_output", map[string]any{
		"thinking": "Need inspect first.",
		"text":     "I will inspect the file.",
	}))
	adapter.Handle(events.New("s1", "assistant.message", "assistant_output", map[string]any{}))
	adapter.Handle(events.New("s1", "tool.before", "tool_execute", map[string]any{
		"call_id":   "call_01",
		"tool_name": "read_file",
		"arguments": `{"path":"main.go"}`,
	}))
	adapter.Handle(events.New("s1", "tool.after", "tool_execute", map[string]any{
		"call_id":        "call_01",
		"display_output": "package main",
		"is_error":       false,
	}))
	adapter.Handle(events.New("s1", "turn.stopped", "provider_call", map[string]any{
		"usage": map[string]any{
			"input_tokens":                10,
			"output_tokens":               int64(5),
			"cache_creation_input_tokens": float64(3),
			"cache_read_input_tokens":     json.Number("7"),
		},
	}))
	if err := adapter.WriteResult("s1", "done", "completed", "", 0); err != nil {
		t.Fatalf("write result: %v", err)
	}
	lines := decodeOutputLines(t, out.String())
	if len(lines) != 5 {
		t.Fatalf("expected 5 output lines, got %d: %s", len(lines), out.String())
	}
	if lines[0].Type != "system" || lines[0].Protocol != ProtocolName || lines[0].ProtocolVersion != ProtocolVersion || lines[0].SessionID != "s1" {
		t.Fatalf("unexpected system line: %#v", lines[0])
	}
	if lines[1].Type != "assistant" || len(lines[1].Message.Content) != 2 {
		t.Fatalf("expected assistant thinking and text blocks, got %#v", lines[1])
	}
	if lines[1].Message.Content[0].Type != "thinking" || lines[1].Message.Content[1].Type != "text" {
		t.Fatalf("unexpected assistant blocks: %#v", lines[1].Message.Content)
	}
	toolUse := lines[2].Message.Content[0]
	if toolUse.Type != "tool_use" || toolUse.ID != "call_01" || toolUse.Name != "read_file" || toolUse.Input["path"] != "main.go" {
		t.Fatalf("unexpected tool_use block: %#v", toolUse)
	}
	toolResult := lines[3].Message.Content[0]
	if toolResult.Type != "tool_result" || toolResult.ToolUseID != "call_01" || toolResult.Content != "package main" || toolResult.IsError {
		t.Fatalf("unexpected tool_result block: %#v", toolResult)
	}
	if lines[4].Type != "result" || lines[4].Usage.InputTokens != 10 || lines[4].Usage.OutputTokens != 5 || lines[4].Usage.CacheCreationInputTokens != 3 || lines[4].Usage.CacheReadInputTokens != 7 {
		t.Fatalf("unexpected result usage: %#v", lines[4])
	}
	if lines[4].IsError == nil || *lines[4].IsError {
		t.Fatalf("expected explicit is_error=false on result, got %#v", lines[4].IsError)
	}
}

func TestAdapterHandlesInvalidToolArgumentsAndFailureResult(t *testing.T) {
	var out bytes.Buffer
	adapter := NewAdapter(&out)
	adapter.Handle(events.New("s1", "tool.before", "tool_execute", map[string]any{
		"call_id":   "call_bad",
		"tool_name": "bad_tool",
		"arguments": `{bad json`,
	}))
	if err := adapter.WriteResult("s1", "", "failed", "boom", 6); err != nil {
		t.Fatalf("write result: %v", err)
	}
	lines := decodeOutputLines(t, out.String())
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %s", len(lines), out.String())
	}
	if lines[0].Message.Content[0].Input != nil {
		t.Fatalf("invalid arguments should decode to nil input, got %#v", lines[0].Message.Content[0].Input)
	}
	if lines[1].IsError == nil || !*lines[1].IsError || lines[1].Result != "boom" || lines[1].Status != "failed" {
		t.Fatalf("unexpected failure result: %#v", lines[1])
	}
}

func TestAdapterRejectsSuccessfulResultWithDanglingToolUse(t *testing.T) {
	var out bytes.Buffer
	adapter := NewAdapter(&out)
	adapter.Handle(events.New("s1", "tool.before", "tool_execute", map[string]any{
		"call_id":   "call_dangling",
		"tool_name": "read_file",
		"arguments": `{"path":"main.go"}`,
	}))
	if err := adapter.WriteResult("s1", "done", "completed", "", 0); err == nil || !strings.Contains(err.Error(), "dangling") {
		t.Fatalf("expected dangling tool_use to block successful result, got %v", err)
	}
	if strings.Contains(out.String(), `"type":"result"`) {
		t.Fatalf("successful result was written after dangling tool_use: %s", out.String())
	}
}

func TestAdapterFailsClosedOnTranscriptIntegrityErrors(t *testing.T) {
	cases := []struct {
		name   string
		events []events.Event
		want   string
	}{
		{
			name: "empty tool use id",
			events: []events.Event{events.New("s1", "tool.before", "tool_execute", map[string]any{
				"tool_name": "read_file",
			})},
			want: "empty tool_use id",
		},
		{
			name: "result before use",
			events: []events.Event{events.New("s1", "tool.after", "tool_execute", map[string]any{
				"call_id": "call_missing",
			})},
			want: "without a pending tool_use",
		},
		{
			name: "duplicate use",
			events: []events.Event{
				events.New("s1", "tool.before", "tool_execute", map[string]any{"call_id": "call_dup", "tool_name": "read_file"}),
				events.New("s1", "tool.before", "tool_execute", map[string]any{"call_id": "call_dup", "tool_name": "read_file"}),
			},
			want: "duplicate tool_use id",
		},
		{
			name: "duplicate result",
			events: []events.Event{
				events.New("s1", "tool.before", "tool_execute", map[string]any{"call_id": "call_dup_result", "tool_name": "read_file"}),
				events.New("s1", "tool.after", "tool_execute", map[string]any{"call_id": "call_dup_result"}),
				events.New("s1", "tool.after", "tool_execute", map[string]any{"call_id": "call_dup_result"}),
			},
			want: "without a pending tool_use",
		},
		{
			name: "known event loss",
			events: []events.Event{events.New("s1", events.EventEventsDropped, "tool_execute", map[string]any{
				"dropped": int64(2),
			})},
			want: "events dropped",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			adapter := NewAdapter(&out)
			for _, evt := range tc.events {
				adapter.Handle(evt)
			}
			if err := adapter.Err(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("adapter error=%v, want substring %q", err, tc.want)
			}
			if err := adapter.WriteResult("s1", "done", "completed", "", 0); err == nil {
				t.Fatal("transcript integrity error allowed a successful result")
			}
			if strings.Contains(out.String(), `"type":"result"`) {
				t.Fatalf("successful result was written after integrity error: %s", out.String())
			}
		})
	}
}

func TestMarshalLineRoundTripsMissionCompatibleFields(t *testing.T) {
	line, err := MarshalLine(&StreamOutputMessage{
		Type:    "result",
		RunRole: "generator",
		Metadata: map[string]any{
			"mission_id": "mis_1",
		},
		Handoff: &StreamHandoff{
			Summary:   "done",
			Completed: []string{"VC-1"},
			Commands: []StreamHandoffCommand{{
				Command:  "go test ./...",
				ExitCode: 0,
				Status:   "passed",
			}},
			Artifacts: []StreamArtifactRef{{Kind: "report", Path: "reports/handoff.json"}},
			Validation: []StreamHandoffValidation{{
				AssertionID: "VC-1",
				Status:      "passed",
				Evidence:    "go test ./...",
			}},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(line, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["run_role"] != "generator" || decoded["handoff"] == nil || decoded["metadata"] == nil {
		t.Fatalf("expected optional mission fields, got %s", string(line))
	}
	if strings.Contains(string(line), `"message":{"role":"generator"`) {
		t.Fatalf("run_role must not become transcript role: %s", string(line))
	}
}

func decodeOutputLines(t *testing.T, raw string) []StreamOutputMessage {
	t.Helper()
	parts := strings.Split(strings.TrimSpace(raw), "\n")
	out := make([]StreamOutputMessage, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		var msg StreamOutputMessage
		if err := json.Unmarshal([]byte(part), &msg); err != nil {
			t.Fatalf("decode %q: %v", part, err)
		}
		out = append(out, msg)
	}
	return out
}
