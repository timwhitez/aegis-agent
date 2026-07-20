package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestScriptedCallExercisesBoundedOutputAndHistoryBeforeBudgetLifecycle(t *testing.T) {
	state := &sessionScriptState{}
	facts := requestFacts{SessionID: "root-session"}

	assertScriptedCall(t, facts, state, 1, "todo_write", nil)
	assertScriptedCall(t, facts, state, 2, "shell", map[string]any{
		"command": "head -c 70000 /dev/zero | tr '\\0' p",
	})

	captureToolReferences([]any{functionCallOutput(`[command_result tool=shell exit_code=0]
[Complete artifact: artifacts/tool-outputs/shell-call.txt; raw_bytes=70000. Page with read_file byte_offset/byte_limit (inline_limit=32768).]`)}, state)
	if state.CommandArtifactPath != "artifacts/tool-outputs/shell-call.txt" {
		t.Fatalf("command artifact path=%q", state.CommandArtifactPath)
	}
	assertScriptedCall(t, facts, state, 3, "read_file", map[string]any{
		"path":        state.CommandArtifactPath,
		"byte_offset": 0,
		"byte_limit":  512,
	})
	assertScriptedCall(t, facts, state, 4, "read_session_history", map[string]any{"limit": 4})

	captureToolReferences([]any{functionCallOutput(`{
  "schema_version": 1,
  "mode": "tail",
  "historical_reference": true,
  "has_more": true,
  "next_before_message_id": "msg-before",
  "messages": [
    {"message_id":"msg-shell","tool_results":[{"name":"shell","output_bytes":32768}]},
    {"message_id":"msg-other","tool_results":[{"name":"todo_write","output_bytes":40}]}
  ]
}`)}, state)
	if state.HistoryMessageID != "msg-shell" {
		t.Fatalf("history message id=%q", state.HistoryMessageID)
	}
	assertScriptedCall(t, facts, state, 5, "read_session_history", map[string]any{
		"message_id":  state.HistoryMessageID,
		"byte_offset": 0,
		"byte_limit":  512,
	})

	captureToolReferences([]any{functionCallOutput(`{
  "schema_version": 1,
  "mode": "message_content",
  "message_id": "msg-shell",
  "has_more": true,
  "next_byte_offset": 512,
  "content": "first page"
}`)}, state)
	if state.HistoryNextByteOffset != 512 {
		t.Fatalf("history next byte offset=%d", state.HistoryNextByteOffset)
	}
	assertScriptedCall(t, facts, state, 6, "read_session_history", map[string]any{
		"message_id":  state.HistoryMessageID,
		"byte_offset": int64(512),
		"byte_limit":  512,
	})

	assertScriptedCall(t, facts, state, 7, "agent_spawn", nil)
	state.DirectChildID = "child-direct"
	assertScriptedCall(t, facts, state, 8, "agent_prompt", nil)
	assertScriptedCall(t, facts, state, 9, "agent_status", nil)
	assertScriptedCall(t, facts, state, 10, "agent_spawn", nil)
	state.BackgroundJobID = "job-background"
	assertScriptedCall(t, facts, state, 11, "agent_stop", nil)
	assertScriptedCall(t, facts, state, 12, "agent_list", nil)
	assertScriptedCall(t, facts, state, 13, "todo_write", nil)
	assertScriptedCall(t, facts, state, 14, "finish", nil)
}

func TestCaptureToolReferencesIgnoresPartialArtifactsAndNonShellHistory(t *testing.T) {
	state := &sessionScriptState{}
	captureToolReferences([]any{
		functionCallOutput(`[Partial artifact: artifacts/tool-outputs/partial.txt; saved=10/20 bytes omitted=10 reason=artifact_file_max_bytes; unrecoverable.]`),
		functionCallOutput(`{"mode":"tail","messages":[{"message_id":"msg-other","tool_results":[{"name":"grep","output_bytes":9999}]}]}`),
		functionCallOutput(`{"mode":"message_content","message_id":"msg-other","has_more":false,"content":"done"}`),
	}, state)
	if state.CommandArtifactPath != "" || state.HistoryMessageID != "" || state.HistoryNextByteOffset != 0 {
		t.Fatalf("unrecoverable/non-shell references were accepted: %#v", state)
	}
}

func assertScriptedCall(t *testing.T, facts requestFacts, state *sessionScriptState, callNumber int, wantName string, wantArguments map[string]any) {
	t.Helper()
	call, err := scriptedCall(facts, state, callNumber)
	if err != nil {
		t.Fatalf("scripted call %d: %v", callNumber, err)
	}
	if call.Name != wantName {
		t.Fatalf("scripted call %d name=%q want=%q", callNumber, call.Name, wantName)
	}
	if wantArguments == nil {
		return
	}
	got, err := json.Marshal(call.Arguments)
	if err != nil {
		t.Fatalf("marshal call %d arguments: %v", callNumber, err)
	}
	want, err := json.Marshal(wantArguments)
	if err != nil {
		t.Fatalf("marshal expected call %d arguments: %v", callNumber, err)
	}
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("scripted call %d arguments=%s want=%s", callNumber, got, want)
	}
}

func functionCallOutput(output string) map[string]any {
	return map[string]any{
		"type":   "function_call_output",
		"output": output,
	}
}
