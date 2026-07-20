package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/session"
	"go-cli-agent/internal/tools"
)

func TestResultHashMetadataUsesPreBudgetLLMOutputAndIsIdempotent(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.ToolOutput.LLMOutputMaxBytes = 512
	cfg.Runtime.ToolOutput.DisplayOutputMaxBytes = 512
	cfg.Runtime.ToolOutput.ArtifactFileMaxBytes = 8192
	cfg.Runtime.ToolOutput.ArtifactSessionMaxBytes = 16384
	cfg.Runtime.ToolOutput.ArtifactMaxFiles = 4
	engine, meta, _, _, _, _ := newTestEngineWithConfig(t, cfg, session.ModeRun)
	raw := "hash-start-" + strings.Repeat("payload-", 600) + "-hash-tail"
	sourceMetadata := map[string]any{"path": filepath.Join(meta.Workdir, "source.txt"), "path_source": "workspace"}

	first := engine.finalizeToolResultForContext(meta.ID, session.ToolResult{
		ToolCallID:    "call_hash",
		Name:          "read_file",
		LLMOutput:     raw,
		DisplayOutput: raw,
		Metadata:      sourceMetadata,
	})
	wantHash := sha256.Sum256([]byte(raw))
	if got := first.Metadata["result_content_sha256"]; got != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("result hash=%v, want %s", got, hex.EncodeToString(wantHash[:]))
	}
	if intFromAny(first.Metadata["result_content_hash_version"]) != 1 || intFromAny(first.Metadata["result_content_bytes"]) != len(raw) || intFromAny(first.Metadata["result_inline_bytes"]) != len(first.LLMOutput) {
		t.Fatalf("result hash byte metadata is wrong: %#v", first.Metadata)
	}
	if first.Metadata["result_content_hash_source"] != "pre_budget_llm_output" {
		t.Fatalf("unexpected hash source: %#v", first.Metadata)
	}
	for _, key := range []string{"result_content_sha256", "result_content_bytes", "result_inline_bytes", "tool_output_budget_version"} {
		if _, mutated := sourceMetadata[key]; mutated {
			t.Fatalf("source metadata map was mutated with %s: %#v", key, sourceMetadata)
		}
	}

	second := engine.finalizeToolResultForContext(meta.ID, first)
	if second.Metadata["result_content_sha256"] != first.Metadata["result_content_sha256"] || second.Metadata["result_content_hash_source"] != first.Metadata["result_content_hash_source"] {
		t.Fatalf("repeated finalizer changed result hash facts: first=%#v second=%#v", first.Metadata, second.Metadata)
	}
}

func TestSafeDedupReplacesOnlyOlderIdenticalReadOnlyResult(t *testing.T) {
	cfg := config.Default()
	engine, meta, _, _, _, _ := newTestEngineWithConfig(t, cfg, session.ModeRun)
	oldResult := finalizedDedupResult(t, engine, meta.ID, "call_old", "read_file", "same file bytes")
	newResult := finalizedDedupResult(t, engine, meta.ID, "call_new", "read_file", "same file bytes")
	messages := []session.Message{
		assistantCall("call_old", "", "read_file", `{"path":"src/file.go"}`),
		session.NewToolMessage([]session.ToolResult{oldResult}),
		assistantCall("call_new", "", "read_file", `{"path":"./src/file.go","offset":0,"limit":120}`),
		session.NewToolMessage([]session.ToolResult{newResult}),
	}
	original := cloneMessages(messages)

	view := deduplicateIdenticalReadOnlyToolResults(messages, cfg)
	old := view[1].ToolResults[0]
	newest := view[3].ToolResults[0]
	if old.Metadata["duplicate_tool_result"] != true || old.Metadata["dedup_retained_call_id"] != "call_new" {
		t.Fatalf("old result was not replaced by a precise duplicate marker: %#v", old)
	}
	if old.ToolCallID != "call_old" || old.Name != "read_file" || old.IsError != oldResult.IsError || old.Final != oldResult.Final {
		t.Fatalf("duplicate marker changed result identity/flags: %#v", old)
	}
	if !strings.Contains(old.LLMOutput, "call_new") || !strings.Contains(old.LLMOutput, oldResult.Metadata["result_content_sha256"].(string)) {
		t.Fatalf("duplicate marker lacks retained id/hash: %q", old.LLMOutput)
	}
	if newest.LLMOutput != newResult.LLMOutput || newest.Metadata["duplicate_tool_result"] == true {
		t.Fatalf("newest identical result should remain full: %#v", newest)
	}
	if !reflect.DeepEqual(messages, original) {
		t.Fatalf("dedup mutated its durable-source input:\n got %#v\nwant %#v", messages, original)
	}

	second := deduplicateIdenticalReadOnlyToolResults(view, cfg)
	secondMarker := second[1].ToolResults[0]
	if secondMarker.LLMOutput != old.LLMOutput || strings.Count(secondMarker.LLMOutput, "Duplicate read_file result") != 1 {
		t.Fatalf("second provider-view pass nested or rewrote marker: first=%q second=%q", old.LLMOutput, secondMarker.LLMOutput)
	}
}

func TestSafeDedupFailsClosedWhenResultOrSemanticsDiffer(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(oldResult, newResult *session.ToolResult, oldCall, newCall *session.Message)
	}{
		{name: "different result content", mutate: func(_ *session.ToolResult, newResult *session.ToolResult, _, _ *session.Message) {
			newResult.LLMOutput = "new file bytes"
			sum := sha256.Sum256([]byte(newResult.LLMOutput))
			newResult.Metadata["result_content_sha256"] = hex.EncodeToString(sum[:])
			newResult.Metadata["result_content_bytes"] = len(newResult.LLMOutput)
			newResult.Metadata["raw_bytes"] = len(newResult.LLMOutput)
			newResult.Metadata["inline_bytes"] = len(newResult.LLMOutput)
			newResult.Metadata["result_inline_bytes"] = len(newResult.LLMOutput)
		}},
		{name: "error to success", mutate: func(oldResult, _ *session.ToolResult, _, _ *session.Message) { oldResult.IsError = true }},
		{name: "success to error", mutate: func(_ *session.ToolResult, newResult *session.ToolResult, _, _ *session.Message) {
			newResult.IsError = true
		}},
		{name: "final differs", mutate: func(oldResult, _ *session.ToolResult, _, _ *session.Message) { oldResult.Final = true }},
		{name: "artifact complete to truncated", mutate: func(oldResult, newResult *session.ToolResult, _, _ *session.Message) {
			oldResult.Metadata["artifact_complete"] = true
			oldResult.Metadata["artifact_truncated"] = false
			oldResult.Metadata["recoverable"] = true
			oldResult.Metadata["artifact_path"] = "artifacts/tool-outputs/old.log"
			oldResult.Metadata["persisted_bytes"] = oldResult.Metadata["raw_bytes"]
			oldResult.Metadata["omitted_bytes"] = 0
			oldResult.Metadata["budget_reason"] = "llm_output_max_bytes"
			newResult.Metadata["artifact_complete"] = false
			newResult.Metadata["artifact_truncated"] = true
			newResult.Metadata["recoverable"] = false
			newResult.Metadata["artifact_path"] = "artifacts/tool-outputs/new.log"
			rawBytes := intFromAny(newResult.Metadata["raw_bytes"])
			newResult.Metadata["persisted_bytes"] = rawBytes - 1
			newResult.Metadata["omitted_bytes"] = 1
			newResult.Metadata["budget_reason"] = "artifact_file_quota"
		}},
		{name: "missing reliable hash", mutate: func(oldResult, _ *session.ToolResult, _, _ *session.Message) {
			delete(oldResult.Metadata, "result_content_sha256")
		}},
		{name: "different path", mutate: func(_, _ *session.ToolResult, _, newCall *session.Message) {
			newCall.ToolCalls[0].Arguments = json.RawMessage(`{"path":"src/other.go"}`)
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			engine, meta, _, _, _, _ := newTestEngineWithConfig(t, cfg, session.ModeRun)
			oldResult := finalizedDedupResult(t, engine, meta.ID, "call_old", "read_file", "same file bytes")
			newResult := finalizedDedupResult(t, engine, meta.ID, "call_new", "read_file", "same file bytes")
			oldCall := assistantCall("call_old", "", "read_file", `{"path":"src/file.go"}`)
			newCall := assistantCall("call_new", "", "read_file", `{"path":"src/file.go"}`)
			tc.mutate(&oldResult, &newResult, &oldCall, &newCall)
			view := deduplicateIdenticalReadOnlyToolResults([]session.Message{
				oldCall,
				session.NewToolMessage([]session.ToolResult{oldResult}),
				newCall,
				session.NewToolMessage([]session.ToolResult{newResult}),
			}, cfg)
			if view[1].ToolResults[0].Metadata["duplicate_tool_result"] == true {
				t.Fatalf("unsafe duplicate marker produced for %s: %#v", tc.name, view[1].ToolResults[0])
			}
		})
	}
}

func TestSafeDedupPreservesChangedGrepResults(t *testing.T) {
	cfg := config.Default()
	engine, meta, _, _, _, _ := newTestEngineWithConfig(t, cfg, session.ModeRun)
	oldResult := finalizedDedupResult(t, engine, meta.ID, "grep_old", "grep", "a.go:10:old state")
	newResult := finalizedDedupResult(t, engine, meta.ID, "grep_new", "grep", "a.go:10:new state")
	args := `{"pattern":"state","path":"a.go","include":"*.go","limit":200}`
	view := deduplicateIdenticalReadOnlyToolResults([]session.Message{
		assistantCall("grep_old", "", "grep", args),
		session.NewToolMessage([]session.ToolResult{oldResult}),
		assistantCall("grep_new", "", "grep", args),
		session.NewToolMessage([]session.ToolResult{newResult}),
	}, cfg)
	if view[1].ToolResults[0].Metadata["duplicate_tool_result"] == true || view[1].ToolResults[0].LLMOutput != oldResult.LLMOutput {
		t.Fatalf("changed grep evidence was incorrectly deduplicated: %#v", view[1].ToolResults[0])
	}
}

func TestSafeDedupPreservesRealReadAndGrepExecutionsAcrossFileChanges(t *testing.T) {
	cfg := config.Default()
	engine, meta, _, registry, _, _ := newTestEngineWithConfig(t, cfg, session.ModeRun)
	path := filepath.Join(meta.Workdir, "evidence.txt")
	if err := os.WriteFile(path, []byte("state: old\n"), 0o600); err != nil {
		t.Fatalf("write old evidence: %v", err)
	}
	execCtx := tools.ExecContext{
		SessionID: meta.ID,
		Workdir:   meta.Workdir,
		Store:     engine.store,
		Config:    cfg,
	}
	execute := func(callID, toolName, arguments string) session.ToolResult {
		t.Helper()
		result, err := registry.Execute(context.Background(), toolName, execCtx, json.RawMessage(arguments))
		if err != nil {
			t.Fatalf("execute %s: %v", toolName, err)
		}
		if result.IsError {
			t.Fatalf("execute %s returned tool error: %#v", toolName, result)
		}
		result.ToolCallID = callID
		return engine.finalizeToolResultForContext(meta.ID, result)
	}

	readArgs := `{"path":"evidence.txt"}`
	grepArgs := `{"pattern":"state","path":"evidence.txt","limit":200}`
	oldRead := execute("read_old", "read_file", readArgs)
	oldGrep := execute("grep_old", "grep", grepArgs)
	if err := os.WriteFile(path, []byte("state: new\n"), 0o600); err != nil {
		t.Fatalf("write new evidence: %v", err)
	}
	newRead := execute("read_new", "read_file", readArgs)
	newGrep := execute("grep_new", "grep", grepArgs)

	messages := []session.Message{
		assistantCall("read_old", "", "read_file", readArgs),
		session.NewToolMessage([]session.ToolResult{oldRead}),
		assistantCall("grep_old", "", "grep", grepArgs),
		session.NewToolMessage([]session.ToolResult{oldGrep}),
		assistantCall("read_new", "", "read_file", readArgs),
		session.NewToolMessage([]session.ToolResult{newRead}),
		assistantCall("grep_new", "", "grep", grepArgs),
		session.NewToolMessage([]session.ToolResult{newGrep}),
	}
	view := deduplicateIdenticalReadOnlyToolResults(messages, cfg)
	for _, index := range []int{1, 3} {
		if got := view[index].ToolResults[0]; got.Metadata["duplicate_tool_result"] == true {
			t.Fatalf("changed real %s result was incorrectly deduplicated: %#v", got.Name, got)
		}
	}
	if oldRead.Metadata["result_content_sha256"] == newRead.Metadata["result_content_sha256"] {
		t.Fatal("real read_file executions did not record the content change")
	}
	if oldGrep.Metadata["result_content_sha256"] == newGrep.Metadata["result_content_sha256"] {
		t.Fatal("real grep executions did not record the changed hit")
	}
}

func TestDuplicateToolResultMarkerSurvivesMicroCompactionWithoutNesting(t *testing.T) {
	cfg := config.Default()
	engine, meta, _, _, _, _ := newTestEngineWithConfig(t, cfg, session.ModeRun)
	oldResult := finalizedDedupResult(t, engine, meta.ID, "call_old", "read_file", "same file bytes")
	newResult := finalizedDedupResult(t, engine, meta.ID, "call_new", "read_file", "same file bytes")
	longPath := "src/" + strings.Repeat("nested/", 240) + "file.go"
	messages := []session.Message{
		assistantCall("call_old", "", "read_file", `{"path":"`+longPath+`"}`),
		session.NewToolMessage([]session.ToolResult{oldResult}),
		assistantCall("call_new", "", "read_file", `{"path":"`+longPath+`"}`),
		session.NewToolMessage([]session.ToolResult{newResult}),
	}
	view := deduplicateIdenticalReadOnlyToolResults(messages, cfg)
	marker := view[1].ToolResults[0].LLMOutput
	stats := compactOldToolContext(view, 1, 64*1024)
	got := view[1].ToolResults[0]
	if got.LLMOutput != marker || strings.Count(got.LLMOutput, "Duplicate read_file result") != 1 {
		t.Fatalf("micro-compaction nested or replaced duplicate marker: before=%q after=%q", marker, got.LLMOutput)
	}
	if got.Metadata["compacted_for_context"] != true || stats.CompactedCount != 1 || stats.InlineCount != 1 {
		t.Fatalf("duplicate marker was not classified as one compacted result: result=%#v stats=%#v", got, stats)
	}
	if string(view[0].ToolCalls[0].Arguments) == string(messages[0].ToolCalls[0].Arguments) {
		t.Fatalf("old duplicate call arguments were not compacted after falling outside the recent window: %s", view[0].ToolCalls[0].Arguments)
	}
}

func TestSafeDedupUsesProviderCallIDAndPreservesMixedBatchSiblings(t *testing.T) {
	cfg := config.Default()
	engine, meta, _, _, _, _ := newTestEngineWithConfig(t, cfg, session.ModeRun)
	oldDuplicate := finalizedDedupResult(t, engine, meta.ID, "provider_old", "grep", "same grep result")
	oldSibling := finalizedDedupResult(t, engine, meta.ID, "sibling_old", "grep", "unique sibling result")
	newDuplicate := finalizedDedupResult(t, engine, meta.ID, "provider_new", "grep", "same grep result")
	oldAssistant := session.NewAssistantMessage("", "", []session.ToolCall{
		{ID: "internal_old", ProviderCallID: "provider_old", Name: "grep", Arguments: json.RawMessage(`{"pattern":"needle","path":"internal"}`)},
		{ID: "sibling_old", Name: "grep", Arguments: json.RawMessage(`{"pattern":"unique","path":"internal"}`)},
	})
	oldAssistant.ProviderContentBlocks = []session.ProviderContentBlock{
		{Provider: "anthropic", Type: "tool_use", ID: "provider_old", Name: "grep", Input: json.RawMessage(`{"pattern":"needle","path":"internal"}`)},
		{Provider: "anthropic", Type: "tool_use", ID: "sibling_old", Name: "grep", Input: json.RawMessage(`{"pattern":"unique","path":"internal"}`)},
	}
	newAssistant := assistantCall("internal_new", "provider_new", "grep", `{"pattern":"needle","path":"internal","limit":200}`)
	messages := []session.Message{
		oldAssistant,
		session.NewToolMessage([]session.ToolResult{oldDuplicate, oldSibling}),
		newAssistant,
		session.NewToolMessage([]session.ToolResult{newDuplicate}),
	}
	original := cloneMessages(messages)

	view := deduplicateIdenticalReadOnlyToolResults(messages, cfg)
	if view[1].ToolResults[0].Metadata["duplicate_tool_result"] != true || view[1].ToolResults[0].Metadata["dedup_retained_call_id"] != "provider_new" {
		t.Fatalf("ProviderCallID alias did not locate duplicate pair: %#v", view[1].ToolResults[0])
	}
	if !reflect.DeepEqual(view[1].ToolResults[1], original[1].ToolResults[1]) {
		t.Fatalf("mixed-batch sibling result changed:\n got %#v\nwant %#v", view[1].ToolResults[1], original[1].ToolResults[1])
	}
	if !reflect.DeepEqual(view[0], original[0]) || !reflect.DeepEqual(view[2], original[2]) {
		t.Fatalf("dedup changed assistant call/provider blocks:\n got %#v / %#v\nwant %#v / %#v", view[0], view[2], original[0], original[2])
	}
}

func TestSafeDedupSkipsNonAllowlistedTools(t *testing.T) {
	cfg := config.Default()
	engine, meta, _, _, _, _ := newTestEngineWithConfig(t, cfg, session.ModeRun)
	oldResult := finalizedDedupResult(t, engine, meta.ID, "call_old", "shell", "same output")
	newResult := finalizedDedupResult(t, engine, meta.ID, "call_new", "shell", "same output")
	view := deduplicateIdenticalReadOnlyToolResults([]session.Message{
		assistantCall("call_old", "", "shell", `{"command":"pwd"}`),
		session.NewToolMessage([]session.ToolResult{oldResult}),
		assistantCall("call_new", "", "shell", `{"command":"pwd"}`),
		session.NewToolMessage([]session.ToolResult{newResult}),
	}, cfg)
	if view[1].ToolResults[0].Metadata["duplicate_tool_result"] == true {
		t.Fatalf("shell result must never be deduplicated: %#v", view[1].ToolResults[0])
	}
}

func TestSafeDedupProviderViewBuildLeavesMessagesJSONLByteForByteUnchanged(t *testing.T) {
	cfg := config.Default()
	engine, meta, state, registry, _, _ := newTestEngineWithConfig(t, cfg, session.ModeRun)
	oldResult := finalizedDedupResult(t, engine, meta.ID, "call_old", "read_file", "same file bytes")
	newResult := finalizedDedupResult(t, engine, meta.ID, "call_new", "read_file", "same file bytes")
	messages := []session.Message{
		assistantCall("call_old", "", "read_file", `{"path":"src/file.go"}`),
		session.NewToolMessage([]session.ToolResult{oldResult}),
		assistantCall("call_new", "", "read_file", `{"path":"src/file.go","limit":120}`),
		session.NewToolMessage([]session.ToolResult{newResult}),
	}
	for _, message := range messages {
		if err := engine.store.AppendMessage(meta.ID, message); err != nil {
			t.Fatalf("append message: %v", err)
		}
	}
	logPath := filepath.Join(engine.store.SessionDir(meta.ID), "messages.jsonl")
	before, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read durable messages before view build: %v", err)
	}
	profile := compactionContextProfile{
		InputCharThreshold:        1 << 20,
		KeepRecentMessages:        20,
		KeepRecentToolResults:     20,
		KeepRecentToolResultBytes: 1 << 20,
		HysteresisDeltaChars:      1 << 18,
	}
	view, err := engine.buildProviderView(t.Context(), meta, state, messages, nil, nil, registry, profile, 0, nil)
	if err != nil {
		t.Fatalf("build provider view: %v", err)
	}
	after, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read durable messages after view build: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("provider-view dedup changed messages.jsonl:\nbefore=%s\nafter=%s", before, after)
	}
	if view.Messages[1].ToolResults[0].Metadata["duplicate_tool_result"] != true {
		t.Fatalf("provider view did not contain duplicate marker: %#v", view.Messages)
	}
	second, err := engine.buildProviderView(t.Context(), meta, state, view.Messages, nil, nil, registry, profile, 0, nil)
	if err != nil {
		t.Fatalf("build provider view twice: %v", err)
	}
	if strings.Count(second.Messages[1].ToolResults[0].LLMOutput, "Duplicate read_file result") != 1 {
		t.Fatalf("second view build nested duplicate marker: %q", second.Messages[1].ToolResults[0].LLMOutput)
	}
}

func finalizedDedupResult(t *testing.T, engine *Engine, sessionID, callID, name, output string) session.ToolResult {
	t.Helper()
	return engine.finalizeToolResultForContext(sessionID, session.ToolResult{
		ToolCallID:    callID,
		Name:          name,
		LLMOutput:     output,
		DisplayOutput: output,
		Metadata:      map[string]any{"path_source": "workspace"},
	})
}

func assistantCall(id, providerID, name, args string) session.Message {
	return session.NewAssistantMessage("", "", []session.ToolCall{{
		ID:             id,
		ProviderCallID: providerID,
		Name:           name,
		Arguments:      json.RawMessage(args),
	}})
}
