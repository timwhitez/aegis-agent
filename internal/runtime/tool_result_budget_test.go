package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/provider"
	"go-cli-agent/internal/session"
)

func toolBudgetMetadataInt(t *testing.T, metadata map[string]any, key string) int {
	t.Helper()
	switch value := metadata[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			t.Fatalf("metadata[%q]=%#v is not an integer: %v", key, value, err)
		}
		return int(parsed)
	default:
		t.Fatalf("metadata[%q]=%#v is not numeric", key, metadata[key])
		return 0
	}
}

func toolBudgetMetadataString(t *testing.T, metadata map[string]any, key string) string {
	t.Helper()
	value, ok := metadata[key].(string)
	if !ok {
		t.Fatalf("metadata[%q]=%#v is not a string", key, metadata[key])
	}
	return value
}

func TestToolResultBudgetKeepsInlineResultAndIdentity(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.ToolOutput.LLMOutputMaxBytes = 512
	cfg.Runtime.ToolOutput.DisplayOutputMaxBytes = 512
	engine, meta, _, _, _, _ := newTestEngineWithConfig(t, cfg, session.ModeRun)
	original := session.ToolResult{
		ToolCallID:    "call_inline",
		Name:          "custom_tool",
		LLMOutput:     "small model output",
		DisplayOutput: "small display output",
		IsError:       true,
		Final:         true,
		Metadata:      map[string]any{"business_key": "keep"},
	}

	got := engine.finalizeToolResultForContext(meta.ID, original)
	if got.ToolCallID != original.ToolCallID || got.Name != original.Name || got.LLMOutput != original.LLMOutput || got.DisplayOutput != original.DisplayOutput || got.IsError != original.IsError || got.Final != original.Final {
		t.Fatalf("inline finalizer changed ToolResult identity or content: got %#v want %#v", got, original)
	}
	if got.Metadata["business_key"] != "keep" {
		t.Fatalf("inline finalizer dropped business metadata: %#v", got.Metadata)
	}
	if toolBudgetMetadataInt(t, got.Metadata, "tool_output_budget_version") != 1 ||
		toolBudgetMetadataInt(t, got.Metadata, "raw_bytes") != len(original.LLMOutput) ||
		toolBudgetMetadataInt(t, got.Metadata, "persisted_bytes") != 0 ||
		toolBudgetMetadataInt(t, got.Metadata, "inline_bytes") != len(original.LLMOutput) ||
		toolBudgetMetadataInt(t, got.Metadata, "omitted_bytes") != 0 {
		t.Fatalf("unexpected inline budget metadata: %#v", got.Metadata)
	}
	if got.Metadata["recoverable"] != true || got.Metadata["artifact_complete"] != false || got.Metadata["artifact_truncated"] != false {
		t.Fatalf("unexpected inline recoverability metadata: %#v", got.Metadata)
	}
}

func TestToolResultBudgetPersistsCompleteArtifactAndIsIdempotent(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.ToolOutput.LLMOutputMaxBytes = 512
	cfg.Runtime.ToolOutput.DisplayOutputMaxBytes = 700
	cfg.Runtime.ToolOutput.ArtifactFileMaxBytes = 8192
	cfg.Runtime.ToolOutput.ArtifactSessionMaxBytes = 16384
	cfg.Runtime.ToolOutput.ArtifactMaxFiles = 4
	engine, meta, _, _, _, _ := newTestEngineWithConfig(t, cfg, session.ModeRun)
	rawLLM := "START-" + strings.Repeat("\u4e2d\U0001f642x", 700) + "-TAIL"
	rawDisplay := "DISPLAY-" + strings.Repeat("y", 2400) + "-TAIL"
	original := session.ToolResult{
		ToolCallID:    "call_large",
		Name:          "custom_tool",
		LLMOutput:     rawLLM,
		DisplayOutput: rawDisplay,
		Metadata:      map[string]any{"business_key": "keep"},
	}

	first := engine.finalizeToolResultForContext(meta.ID, original)
	if len(first.LLMOutput) > cfg.Runtime.ToolOutput.LLMOutputMaxBytes || len(first.DisplayOutput) > cfg.Runtime.ToolOutput.DisplayOutputMaxBytes {
		t.Fatalf("bounded outputs exceed policy: llm=%d display=%d result=%#v", len(first.LLMOutput), len(first.DisplayOutput), first)
	}
	if !strings.Contains(first.LLMOutput, "Complete artifact:") || strings.Contains(first.LLMOutput, "Full output:") {
		t.Fatalf("complete artifact notice is missing or uses legacy Full output label: %q", first.LLMOutput)
	}
	artifactPath := toolBudgetMetadataString(t, first.Metadata, "artifact_path")
	if artifactPath == "" {
		t.Fatalf("complete artifact path missing: %#v", first.Metadata)
	}
	if first.Metadata["artifact_complete"] != true || first.Metadata["artifact_truncated"] != false || first.Metadata["recoverable"] != true {
		t.Fatalf("unexpected complete artifact metadata: %#v", first.Metadata)
	}
	if toolBudgetMetadataInt(t, first.Metadata, "raw_bytes") != len(rawLLM) ||
		toolBudgetMetadataInt(t, first.Metadata, "persisted_bytes") != len(rawLLM) ||
		toolBudgetMetadataInt(t, first.Metadata, "omitted_bytes") != 0 ||
		toolBudgetMetadataInt(t, first.Metadata, "inline_bytes") != len(first.LLMOutput) ||
		toolBudgetMetadataInt(t, first.Metadata, "display_raw_bytes") != len(rawDisplay) ||
		toolBudgetMetadataInt(t, first.Metadata, "display_inline_bytes") != len(first.DisplayOutput) {
		t.Fatalf("unexpected complete artifact byte metadata: %#v", first.Metadata)
	}
	absolutePath := filepath.Join(engine.store.SessionDir(meta.ID), filepath.FromSlash(artifactPath))
	data, err := os.ReadFile(absolutePath)
	if err != nil {
		t.Fatalf("read complete artifact: %v", err)
	}
	if string(data) != rawLLM {
		t.Fatalf("complete artifact changed raw LLM output: got %d bytes want %d", len(data), len(rawLLM))
	}

	second := engine.finalizeToolResultForContext(meta.ID, first)
	if second.LLMOutput != first.LLMOutput || second.DisplayOutput != first.DisplayOutput || toolBudgetMetadataString(t, second.Metadata, "artifact_path") != artifactPath {
		t.Fatalf("finalizer was not idempotent: first=%#v second=%#v", first, second)
	}
	entries, err := os.ReadDir(filepath.Dir(absolutePath))
	if err != nil {
		t.Fatalf("read artifact directory: %v", err)
	}
	artifactFiles := 0
	for _, entry := range entries {
		if !entry.IsDir() && entry.Name() != ".quota.lock" {
			artifactFiles++
		}
	}
	if artifactFiles != 1 {
		t.Fatalf("idempotent finalizer created %d artifacts, want 1", artifactFiles)
	}
}

func TestToolResultBudgetDoesNotTrustSpoofedVersionOnOversizedResult(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.ToolOutput.LLMOutputMaxBytes = 512
	cfg.Runtime.ToolOutput.DisplayOutputMaxBytes = 512
	cfg.Runtime.ToolOutput.ArtifactFileMaxBytes = 8192
	cfg.Runtime.ToolOutput.ArtifactSessionMaxBytes = 16384
	cfg.Runtime.ToolOutput.ArtifactMaxFiles = 4
	engine, meta, _, _, _, _ := newTestEngineWithConfig(t, cfg, session.ModeRun)
	raw := "spoof-start-" + strings.Repeat("x", 3000) + "-spoof-tail"

	got := engine.finalizeToolResultForContext(meta.ID, session.ToolResult{
		ToolCallID:    "call_spoofed",
		Name:          "custom_tool",
		LLMOutput:     raw,
		DisplayOutput: raw,
		Metadata: map[string]any{
			"tool_output_budget_version": 1,
			"business_key":               "keep",
		},
	})
	if len(got.LLMOutput) > 512 || len(got.DisplayOutput) > 512 {
		t.Fatalf("spoofed metadata bypassed the byte budget: llm=%d display=%d", len(got.LLMOutput), len(got.DisplayOutput))
	}
	if toolBudgetMetadataInt(t, got.Metadata, "raw_bytes") != len(raw) || got.Metadata["artifact_complete"] != true || got.Metadata["business_key"] != "keep" {
		t.Fatalf("spoofed metadata was not replaced with finalizer facts: %#v", got.Metadata)
	}
}

func TestToolResultBudgetMarksPartialAndFailedArtifactsUnrecoverable(t *testing.T) {
	t.Run("file quota", func(t *testing.T) {
		cfg := config.Default()
		cfg.Runtime.ToolOutput.LLMOutputMaxBytes = 512
		cfg.Runtime.ToolOutput.DisplayOutputMaxBytes = 512
		cfg.Runtime.ToolOutput.ArtifactFileMaxBytes = 100
		cfg.Runtime.ToolOutput.ArtifactSessionMaxBytes = 1000
		cfg.Runtime.ToolOutput.ArtifactMaxFiles = 2
		engine, meta, _, _, _, _ := newTestEngineWithConfig(t, cfg, session.ModeRun)
		raw := "prefix-" + strings.Repeat("z", 2000) + "-tail"

		got := engine.finalizeToolResultForContext(meta.ID, session.ToolResult{ToolCallID: "call_partial", Name: "custom", LLMOutput: raw, DisplayOutput: raw})
		if len(got.LLMOutput) > 512 || !strings.Contains(got.LLMOutput, "Partial artifact:") || strings.Contains(got.LLMOutput, "Full output:") {
			t.Fatalf("unexpected partial artifact notice: %q", got.LLMOutput)
		}
		if got.Metadata["artifact_complete"] != false || got.Metadata["artifact_truncated"] != true || got.Metadata["recoverable"] != false {
			t.Fatalf("partial artifact was mislabeled recoverable: %#v", got.Metadata)
		}
		if toolBudgetMetadataInt(t, got.Metadata, "persisted_bytes") != 100 || toolBudgetMetadataInt(t, got.Metadata, "omitted_bytes") != len(raw)-100 {
			t.Fatalf("partial artifact byte counts are wrong: %#v", got.Metadata)
		}
	})

	t.Run("symlinked root", func(t *testing.T) {
		cfg := config.Default()
		cfg.Runtime.ToolOutput.LLMOutputMaxBytes = 512
		cfg.Runtime.ToolOutput.DisplayOutputMaxBytes = 512
		outside := t.TempDir()
		alias := filepath.Join(t.TempDir(), "artifact-root")
		if err := os.Symlink(outside, alias); err != nil {
			t.Fatalf("symlink artifact root: %v", err)
		}
		cfg.Runtime.Ephemeral.ArtifactDir = alias
		engine, meta, _, _, _, _ := newTestEngineWithConfig(t, cfg, session.ModeRun)
		raw := strings.Repeat("x", 2000)

		got := engine.finalizeToolResultForContext(meta.ID, session.ToolResult{ToolCallID: "call_failed", Name: "custom", LLMOutput: raw, DisplayOutput: raw})
		if len(got.LLMOutput) > 512 || !strings.Contains(got.LLMOutput, "no artifact was saved") || strings.Contains(got.LLMOutput, "Full output:") {
			t.Fatalf("unexpected failed artifact notice: %q", got.LLMOutput)
		}
		if got.Metadata["recoverable"] != false || got.Metadata["artifact_complete"] != false || toolBudgetMetadataString(t, got.Metadata, "artifact_path") != "" {
			t.Fatalf("failed artifact was mislabeled: %#v", got.Metadata)
		}
		if !strings.Contains(toolBudgetMetadataString(t, got.Metadata, "artifact_error"), "symlink") {
			t.Fatalf("failed artifact error is not observable: %#v", got.Metadata)
		}
	})
}

func TestEngineToolResultBudgetRunsAfterHookAndBudgetsEachBatchResult(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.ToolOutput.LLMOutputMaxBytes = 512
	cfg.Runtime.ToolOutput.DisplayOutputMaxBytes = 768
	cfg.Runtime.ToolOutput.ArtifactFileMaxBytes = 3 * 1024 * 1024
	cfg.Runtime.ToolOutput.ArtifactSessionMaxBytes = 8 * 1024 * 1024
	cfg.Runtime.ToolOutput.ArtifactMaxFiles = 8
	hookOutput := "HOOK-START-" + strings.Repeat("q", 2*1024*1024) + "-HOOK-TAIL"
	cfg.Hooks.ToolAfter = []config.HookDefinition{{
		Name:   "amplify-read-output",
		Match:  config.HookMatch{Tool: "read_file"},
		Inject: &config.HookInject{Field: "llm_output", Set: hookOutput},
	}}
	engine, meta, state, registry, hookManager, catalog := newTestEngineWithConfig(t, cfg, session.ModeRun)
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(meta.Workdir, name), []byte("small\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "read two files and finish")); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	fake := provider.NewFake(
		func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
			return provider.TurnResult{ToolCalls: []provider.ToolCall{
				{ID: "call_a", Name: "read_file", Arguments: json.RawMessage(`{"path":"a.txt"}`)},
				{ID: "call_b", Name: "read_file", Arguments: json.RawMessage(`{"path":"b.txt"}`)},
			}, StopReason: "tool_use"}, nil
		},
		func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
			return provider.TurnResult{ToolCalls: []provider.ToolCall{{ID: "call_finish", Name: "finish", Arguments: json.RawMessage(`{"message":"done"}`)}}, StopReason: "tool_use"}, nil
		},
	)
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("expected completed run, got %#v", result)
	}
	messages, err := engine.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	var batch []session.ToolResult
	for _, message := range messages {
		if message.Role == "tool" && len(message.ToolResults) == 2 && message.ToolResults[0].Name == "read_file" {
			batch = message.ToolResults
			break
		}
	}
	if len(batch) != 2 {
		t.Fatalf("expected two finalized read_file results, got %#v", messages)
	}
	seenPaths := map[string]bool{}
	for index, toolResult := range batch {
		wantCallID := []string{"call_a", "call_b"}[index]
		wantSourcePath := []string{"a.txt", "b.txt"}[index]
		if toolResult.ToolCallID != wantCallID || toolResult.Name != "read_file" || toolResult.IsError || toolResult.Final {
			t.Fatalf("batch result identity changed at %d: %#v", index, toolResult)
		}
		if len(toolResult.LLMOutput) > 512 || len(toolResult.DisplayOutput) > 768 {
			t.Fatalf("batch result exceeded policy at %d: llm=%d display=%d", index, len(toolResult.LLMOutput), len(toolResult.DisplayOutput))
		}
		if toolBudgetMetadataString(t, toolResult.Metadata, "path") != filepath.Join(meta.Workdir, wantSourcePath) {
			t.Fatalf("batch result business metadata crossed at %d: %#v", index, toolResult.Metadata)
		}
		artifactPath := toolBudgetMetadataString(t, toolResult.Metadata, "artifact_path")
		if artifactPath == "" || seenPaths[artifactPath] {
			t.Fatalf("batch result artifact path missing or reused at %d: %#v", index, toolResult.Metadata)
		}
		seenPaths[artifactPath] = true
		data, readErr := os.ReadFile(filepath.Join(engine.store.SessionDir(meta.ID), filepath.FromSlash(artifactPath)))
		if readErr != nil || string(data) != hookOutput {
			t.Fatalf("batch artifact mismatch at %d: bytes=%d err=%v", index, len(data), readErr)
		}
	}
	rawMessages, err := os.ReadFile(filepath.Join(engine.store.SessionDir(meta.ID), "messages.jsonl"))
	if err != nil {
		t.Fatalf("read messages.jsonl: %v", err)
	}
	if len(rawMessages) >= len(hookOutput) || strings.Contains(string(rawMessages), strings.Repeat("q", 1024)) {
		t.Fatalf("hook-amplified raw output leaked into durable messages.jsonl: %d bytes", len(rawMessages))
	}
	eventsList, err := engine.store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	for _, callID := range []string{"call_a", "call_b"} {
		found := false
		for _, event := range eventsList {
			if event.Type == "tool.after" && event.Data["call_id"] == callID {
				found = true
				display, _ := event.Data["display_output"].(string)
				if len(display) > 768 {
					t.Fatalf("tool.after event bypassed display budget for %s: %d bytes", callID, len(display))
				}
			}
		}
		if !found {
			t.Fatalf("missing tool.after event for %s", callID)
		}
	}
}

func TestSynchronousAgentHandoffBudgetPreservesChildReferences(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.ToolOutput.LLMOutputMaxBytes = 512
	cfg.Runtime.ToolOutput.DisplayOutputMaxBytes = 512
	cfg.Runtime.ToolOutput.ArtifactFileMaxBytes = 8192
	cfg.Runtime.ToolOutput.ArtifactSessionMaxBytes = 16384
	cfg.Runtime.ToolOutput.ArtifactMaxFiles = 4
	engine, meta, _, _, _, _ := newTestEngineWithConfig(t, cfg, session.ModeRun)
	raw := "{\n  \"session_id\": \"child_sync_1\",\n  \"status\": \"completed\",\n  \"final_text\": \"" + strings.Repeat("detail-", 900) + "\"\n}"

	got := engine.finalizeToolResultForContext(meta.ID, session.ToolResult{
		ToolCallID:    "call_agent_status",
		Name:          "agent_status",
		LLMOutput:     raw,
		DisplayOutput: raw,
	})
	if len(got.LLMOutput) > 512 || !strings.Contains(got.LLMOutput, "child_sync_1") || !strings.Contains(got.LLMOutput, "completed") || !strings.Contains(got.LLMOutput, "Complete artifact:") {
		t.Fatalf("synchronous child handoff lost bounded references: %q", got.LLMOutput)
	}
	artifactPath := toolBudgetMetadataString(t, got.Metadata, "artifact_path")
	data, err := os.ReadFile(filepath.Join(engine.store.SessionDir(meta.ID), filepath.FromSlash(artifactPath)))
	if err != nil || string(data) != raw {
		t.Fatalf("synchronous child handoff artifact mismatch: bytes=%d err=%v", len(data), err)
	}
}

func TestBackgroundHandoffBudgetBoundsParentMessageAndKeepsReferences(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.ToolOutput.LLMOutputMaxBytes = 512
	cfg.Runtime.ToolOutput.DisplayOutputMaxBytes = 512
	cfg.Runtime.ToolOutput.ArtifactFileMaxBytes = 8192
	cfg.Runtime.ToolOutput.ArtifactSessionMaxBytes = 16384
	cfg.Runtime.ToolOutput.ArtifactMaxFiles = 4
	engine, meta, _, _, hookManager, _ := newTestEngineWithConfig(t, cfg, session.ModeRun)
	finalText := "CHILD-START-" + strings.Repeat("child-detail-", 500) + "-CHILD-TAIL"
	notification := session.NewBackgroundNotification(session.QueueJob{
		SchemaVersion:   1,
		ID:              "job_budget_1",
		Status:          session.QueueStatusCompleted,
		ParentSessionID: meta.ID,
		SessionID:       "child_budget_1",
		SessionStatus:   session.StatusCompleted,
		FinalText:       finalText,
		Background:      true,
	})
	if err := engine.store.AppendBackgroundNotification(meta.ID, notification); err != nil {
		t.Fatalf("append background notification: %v", err)
	}
	accepted, err := engine.drainBackground(context.Background(), meta, hookManager)
	if err != nil || accepted != 1 {
		t.Fatalf("drain background: accepted=%d err=%v", accepted, err)
	}
	messages, err := engine.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	last := messages[len(messages)-1]
	if last.Role != "user" || last.Meta["source"] != "background_results" {
		t.Fatalf("unexpected background handoff message: %#v", last)
	}
	if len(last.Text) > 512 || !strings.Contains(last.Text, "Complete artifact:") {
		t.Fatalf("background handoff bypassed budget: %d bytes %q", len(last.Text), last.Text)
	}
	if !strings.Contains(last.Text, "job_budget_1") || !strings.Contains(last.Text, "child_budget_1") {
		t.Fatalf("background handoff lost job/session references: %q", last.Text)
	}
	handoffMetadata, ok := last.Meta["handoff_budget"].(map[string]any)
	if !ok {
		t.Fatalf("background handoff budget metadata missing: %#v", last.Meta)
	}
	artifactPath := toolBudgetMetadataString(t, handoffMetadata, "artifact_path")
	data, err := os.ReadFile(filepath.Join(engine.store.SessionDir(meta.ID), filepath.FromSlash(artifactPath)))
	if err != nil {
		t.Fatalf("read background handoff artifact: %v", err)
	}
	if !strings.Contains(string(data), finalText) || !strings.Contains(string(data), "job_budget_1") || !strings.Contains(string(data), "child_budget_1") {
		t.Fatalf("background handoff artifact is incomplete: %d bytes", len(data))
	}
}

func TestRunnerPlanInputRecoveryUsesToolResultBudget(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	cfg.Runtime.ToolOutput.LLMOutputMaxBytes = 512
	cfg.Runtime.ToolOutput.DisplayOutputMaxBytes = 512
	cfg.Runtime.ToolOutput.ArtifactFileMaxBytes = 16 * 1024
	cfg.Runtime.ToolOutput.ArtifactSessionMaxBytes = 32 * 1024
	cfg.Runtime.ToolOutput.ArtifactMaxFiles = 4
	runner := NewRunner(cfg)
	sessionID := session.NewSessionID()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               sessionID,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyAutonomous,
	}
	if err := runner.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_input", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := runner.store.CreatePlanMode(sessionID, session.PlanModeDraft{Enabled: true, Objective: "Answer a large free-form question"}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	request := session.PlanModeInputRequest{
		RequestID:  "pmq_budget",
		ToolCallID: "call_plan_input_budget",
		Questions: []session.PlanModeInputQuestion{{
			ID:       "details",
			Header:   "Details",
			Question: "What details should be retained?",
			IsOther:  true,
			Options: []session.PlanModeInputOption{
				{Label: "Short (Recommended)", Description: "Use the short answer."},
				{Label: "Long", Description: "Use a long answer."},
			},
		}},
		Status:    "pending",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if _, err := runner.store.SetPlanModePendingRequest(sessionID, request, session.PlanModeSourceTool); err != nil {
		t.Fatalf("set pending request: %v", err)
	}
	largeAnswer := "answer-start-" + strings.Repeat("detail-", 1200) + "-answer-tail"
	if err := runner.appendPlanInputToolResult(sessionID, request.RequestID, session.PlanModeSourceWeb, []session.PlanModeInputAnswer{{
		QuestionID: "details",
		Label:      "Other",
		Value:      largeAnswer,
		IsOther:    true,
	}}); err != nil {
		t.Fatalf("append plan input tool result: %v", err)
	}
	messages, err := runner.store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	var got *session.ToolResult
	for messageIndex := range messages {
		for resultIndex := range messages[messageIndex].ToolResults {
			result := &messages[messageIndex].ToolResults[resultIndex]
			if result.ToolCallID == request.ToolCallID && result.Name == "request_user_input" {
				got = result
			}
		}
	}
	if got == nil {
		t.Fatalf("plan input tool result missing: %#v", messages)
	}
	if len(got.LLMOutput) > 512 || len(got.DisplayOutput) > 512 || strings.Contains(got.LLMOutput, strings.Repeat("detail-", 100)) {
		t.Fatalf("runner recovery bypassed tool-result budget: llm=%d display=%d", len(got.LLMOutput), len(got.DisplayOutput))
	}
	if toolBudgetMetadataInt(t, got.Metadata, "tool_output_budget_version") != 1 || got.Metadata["artifact_complete"] != true {
		t.Fatalf("runner recovery result lacks complete budget metadata: %#v", got.Metadata)
	}
}

func TestToolResultBudgetEphemeralProviderViewReusesCompleteArtifact(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.ToolOutput.LLMOutputMaxBytes = 512
	cfg.Runtime.ToolOutput.DisplayOutputMaxBytes = 512
	cfg.Runtime.ToolOutput.ArtifactFileMaxBytes = 16 * 1024
	cfg.Runtime.ToolOutput.ArtifactSessionMaxBytes = 32 * 1024
	cfg.Runtime.ToolOutput.ArtifactMaxFiles = 8
	engine, meta, _, registry, _, _ := newTestEngineWithConfig(t, cfg, session.ModeRun)
	raw := "complete-start-" + strings.Repeat("payload-", 900) + "-complete-tail"
	oldest := engine.finalizeToolResultForContext(meta.ID, session.ToolResult{ToolCallID: "call_1", Name: "glob", LLMOutput: raw, DisplayOutput: raw})
	artifactPath := toolBudgetMetadataString(t, oldest.Metadata, "artifact_path")
	messages := []session.Message{session.NewToolMessage([]session.ToolResult{oldest})}
	for index := 2; index <= 4; index++ {
		messages = append(messages, session.NewToolMessage([]session.ToolResult{{ToolCallID: "call_" + string(rune('0'+index)), Name: "glob", LLMOutput: "short", DisplayOutput: "short"}}))
	}

	view := engine.applyEphemeralProviderView(meta.ID, messages, messages, registry)
	got := view[0].ToolResults[0]
	if !strings.Contains(got.LLMOutput, "Complete artifact: "+artifactPath) {
		t.Fatalf("provider view did not reuse the finalized complete artifact: %q", got.LLMOutput)
	}
	entries, err := os.ReadDir(engine.ephemeralArtifactRoot(meta.ID))
	if err != nil {
		t.Fatalf("read artifact root: %v", err)
	}
	artifactFiles := 0
	for _, entry := range entries {
		if entry.Name() != ".quota.lock" {
			artifactFiles++
		}
	}
	if artifactFiles != 1 {
		t.Fatalf("provider view duplicated a first-layer artifact: files=%d entries=%v", artifactFiles, entries)
	}
}

func TestToolResultBudgetEphemeralProviderViewDoesNotRelabelPartialArtifactComplete(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.ToolOutput.LLMOutputMaxBytes = 512
	cfg.Runtime.ToolOutput.DisplayOutputMaxBytes = 512
	cfg.Runtime.ToolOutput.ArtifactFileMaxBytes = 128
	cfg.Runtime.ToolOutput.ArtifactSessionMaxBytes = 16 * 1024
	cfg.Runtime.ToolOutput.ArtifactMaxFiles = 8
	engine, meta, _, registry, _, _ := newTestEngineWithConfig(t, cfg, session.ModeRun)
	raw := "partial-start-" + strings.Repeat("payload-", 900) + "-partial-tail"
	oldest := engine.finalizeToolResultForContext(meta.ID, session.ToolResult{ToolCallID: "call_1", Name: "glob", LLMOutput: raw, DisplayOutput: raw})
	if oldest.Metadata["artifact_truncated"] != true || oldest.Metadata["recoverable"] != false {
		t.Fatalf("test setup did not create a partial artifact: %#v", oldest.Metadata)
	}
	messages := []session.Message{session.NewToolMessage([]session.ToolResult{oldest})}
	for index := 2; index <= 4; index++ {
		messages = append(messages, session.NewToolMessage([]session.ToolResult{{ToolCallID: "call_" + string(rune('0'+index)), Name: "glob", LLMOutput: "short", DisplayOutput: "short"}}))
	}

	view := engine.applyEphemeralProviderView(meta.ID, messages, messages, registry)
	got := view[0].ToolResults[0]
	if strings.Contains(got.LLMOutput, "Complete artifact:") || !strings.Contains(got.LLMOutput, "not recoverable") {
		t.Fatalf("provider view relabeled an incomplete first-layer artifact: %q", got.LLMOutput)
	}
	entries, err := os.ReadDir(engine.ephemeralArtifactRoot(meta.ID))
	if err != nil {
		t.Fatalf("read artifact root: %v", err)
	}
	artifactFiles := 0
	for _, entry := range entries {
		if entry.Name() != ".quota.lock" {
			artifactFiles++
		}
	}
	if artifactFiles != 1 {
		t.Fatalf("provider view wrote a misleading second artifact: files=%d entries=%v", artifactFiles, entries)
	}
}
