package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/session"
)

type sessionHistoryTestEnvelope struct {
	SchemaVersion         int              `json:"schema_version"`
	Mode                  string           `json:"mode"`
	HistoricalReference   bool             `json:"historical_reference"`
	InstructionPrecedence string           `json:"instruction_precedence"`
	SourceSessionID       string           `json:"source_session_id"`
	SourceMessageIDs      []string         `json:"source_message_ids"`
	ReturnedCount         int              `json:"returned_count"`
	HasMore               bool             `json:"has_more"`
	NextBeforeMessageID   string           `json:"next_before_message_id"`
	NextByteOffset        int64            `json:"next_byte_offset"`
	ScannedCount          int              `json:"scanned_count"`
	ScanLimitReached      bool             `json:"scan_limit_reached"`
	Messages              []map[string]any `json:"messages"`
	MessageID             string           `json:"message_id"`
	ContentSchemaVersion  int              `json:"content_schema_version"`
	RequestedByteOffset   int64            `json:"requested_byte_offset"`
	RequestedByteLimit    int              `json:"requested_byte_limit"`
	EffectiveByteStart    int64            `json:"effective_byte_start"`
	EffectiveByteEnd      int64            `json:"effective_byte_end"`
	ReturnedBytes         int64            `json:"returned_bytes"`
	TotalBytes            int64            `json:"total_bytes"`
	Content               string           `json:"content"`
}

func newSessionHistoryToolRegistry(t *testing.T, cfg *config.Config) (*Registry, ExecContext, session.SessionMetadata) {
	t.Helper()
	if cfg == nil {
		cfg = config.Default()
	}
	store := session.NewStoreWithDirMode(filepath.Join(t.TempDir(), "sessions"), 0o700)
	workdir := t.TempDir()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	state := session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create history tool session: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("create history tool registry: %v", err)
	}
	return registry, ExecContext{SessionID: meta.ID, Workdir: workdir, Store: store, Config: cfg}, meta
}

func executeSessionHistory(t *testing.T, registry *Registry, execCtx ExecContext, raw string) (session.ToolResult, sessionHistoryTestEnvelope) {
	t.Helper()
	result, err := registry.Execute(context.Background(), "read_session_history", execCtx, json.RawMessage(raw))
	if err != nil {
		t.Fatalf("execute read_session_history: %v", err)
	}
	var envelope sessionHistoryTestEnvelope
	if !result.IsError {
		if err := json.Unmarshal([]byte(result.LLMOutput), &envelope); err != nil {
			t.Fatalf("decode history envelope: %v\n%s", err, result.LLMOutput)
		}
	}
	return result, envelope
}

func appendHistoryToolMessages(t *testing.T, store *session.Store, sessionID string, count int) []session.Message {
	t.Helper()
	messages := make([]session.Message, 0, count)
	for i := 0; i < count; i++ {
		message := session.NewMessage("user", fmt.Sprintf("history message %03d", i))
		if err := store.AppendMessage(sessionID, message); err != nil {
			t.Fatalf("append history message %d: %v", i, err)
		}
		messages = append(messages, message)
	}
	return messages
}

func TestReadSessionHistorySchemaRejectsCrossSessionPathAndConflictingModes(t *testing.T) {
	registry, execCtx, _ := newSessionHistoryToolRegistry(t, nil)
	definition := registry.Get("read_session_history")
	if definition == nil {
		t.Fatal("read_session_history definition missing")
	}
	properties, _ := definition.InputSchema["properties"].(map[string]any)
	for _, field := range []string{"before_message_id", "limit", "query", "message_id", "byte_offset", "byte_limit"} {
		if properties[field] == nil {
			t.Fatalf("history schema missing %s: %#v", field, definition.InputSchema)
		}
	}
	for _, forbidden := range []string{"session_id", "path", "artifact_path", "transcript_path"} {
		if properties[forbidden] != nil {
			t.Fatalf("history schema exposes forbidden field %s: %#v", forbidden, definition.InputSchema)
		}
	}
	if definition.InputSchema["additionalProperties"] != false || definition.InputSchema["oneOf"] == nil {
		t.Fatalf("history schema must be closed and mode-exclusive: %#v", definition.InputSchema)
	}

	longQuery, _ := json.Marshal(map[string]any{"query": strings.Repeat("q", 257)})
	invalid := map[string]string{
		"session id":            `{"session_id":"other"}`,
		"path":                  `{"path":"/tmp/session/messages.jsonl"}`,
		"artifact path":         `{"artifact_path":"artifacts/transcripts/all.jsonl"}`,
		"unknown":               `{"unknown":true}`,
		"mixed modes":           `{"before_message_id":"msg_a","message_id":"msg_b","byte_limit":32}`,
		"message missing size":  `{"message_id":"msg_a"}`,
		"bytes missing message": `{"byte_offset":0,"byte_limit":32}`,
		"negative offset":       `{"message_id":"msg_a","byte_offset":-1,"byte_limit":32}`,
		"zero byte limit":       `{"message_id":"msg_a","byte_limit":0}`,
		"over byte limit":       `{"message_id":"msg_a","byte_limit":16385}`,
		"zero record limit":     `{"limit":0}`,
		"over record limit":     `{"limit":21}`,
		"blank query":           `{"query":"  "}`,
		"overlong query":        string(longQuery),
	}
	for name, raw := range invalid {
		t.Run(name, func(t *testing.T) {
			result, _ := executeSessionHistory(t, registry, execCtx, raw)
			if !result.IsError || result.Metadata[MetadataFailureClass] != FailureClassSchemaReject {
				t.Fatalf("expected schema rejection, got %#v", result)
			}
		})
	}
}

func TestReadSessionHistoryTailBeforeAndUnknownCursor(t *testing.T) {
	registry, execCtx, meta := newSessionHistoryToolRegistry(t, nil)

	result, empty := executeSessionHistory(t, registry, execCtx, `{}`)
	if result.IsError || empty.ReturnedCount != 0 || empty.HasMore || empty.SchemaVersion != SessionHistorySchemaVersion {
		t.Fatalf("unexpected empty tail: result=%#v envelope=%#v", result, empty)
	}

	written := appendHistoryToolMessages(t, execCtx.Store, meta.ID, 7)
	result, exact := executeSessionHistory(t, registry, execCtx, `{"limit":7}`)
	if result.IsError || exact.ReturnedCount != 7 || exact.HasMore || exact.NextBeforeMessageID != "" {
		t.Fatalf("exact-limit tail must be complete: result=%#v envelope=%#v", result, exact)
	}

	result, tail := executeSessionHistory(t, registry, execCtx, `{"limit":3}`)
	if result.IsError || !tail.HistoricalReference || tail.Mode != "tail" || !tail.HasMore || tail.ReturnedCount != 3 {
		t.Fatalf("unexpected tail envelope: result=%#v envelope=%#v", result, tail)
	}
	if tail.SourceSessionID != meta.ID || tail.SourceMessageIDs[0] != written[4].ID || tail.SourceMessageIDs[2] != written[6].ID || tail.NextBeforeMessageID != written[4].ID {
		t.Fatalf("unexpected tail ids/cursor: %#v", tail)
	}
	if !strings.Contains(tail.InstructionPrecedence, "latest external user instruction") || !strings.Contains(tail.InstructionPrecedence, "latest steer") {
		t.Fatalf("missing instruction precedence: %q", tail.InstructionPrecedence)
	}

	_, before := executeSessionHistory(t, registry, execCtx, fmt.Sprintf(`{"before_message_id":%q,"limit":3}`, written[4].ID))
	if before.Mode != "before" || !before.HasMore || before.ReturnedCount != 3 || before.SourceMessageIDs[0] != written[1].ID || before.SourceMessageIDs[2] != written[3].ID || before.NextBeforeMessageID != written[1].ID {
		t.Fatalf("unexpected before page: %#v", before)
	}

	_, first := executeSessionHistory(t, registry, execCtx, fmt.Sprintf(`{"before_message_id":%q,"limit":3}`, written[0].ID))
	if first.ReturnedCount != 0 || first.HasMore || first.NextBeforeMessageID != "" {
		t.Fatalf("unexpected first page: %#v", first)
	}

	missing, _ := executeSessionHistory(t, registry, execCtx, `{"before_message_id":"msg_missing_history","limit":2}`)
	if !missing.IsError || missing.Metadata[MetadataFailureClass] != FailureClassNotFound || missing.Metadata["error_code"] != "before_message_not_found" {
		t.Fatalf("unknown cursor must be typed not-found: %#v", missing)
	}
}

func TestReadSessionHistoryQueryIsBoundedCaseInsensitiveAndContinuable(t *testing.T) {
	registry, execCtx, meta := newSessionHistoryToolRegistry(t, nil)
	written := make([]session.Message, 0, 520)
	for i := 0; i < 520; i++ {
		text := fmt.Sprintf("ordinary history %03d", i)
		if i == 10 {
			text = "older NeEdLe evidence"
		}
		if i == 510 {
			text = "newer NEEDLE evidence"
		}
		message := session.NewMessage("user", text)
		if err := execCtx.Store.AppendMessage(meta.ID, message); err != nil {
			t.Fatalf("append query message %d: %v", i, err)
		}
		written = append(written, message)
	}

	result, newest := executeSessionHistory(t, registry, execCtx, `{"query":"needle","limit":1}`)
	if result.IsError || newest.Mode != "query" || newest.ReturnedCount != 1 || newest.SourceMessageIDs[0] != written[510].ID || newest.ScannedCount > MaxSessionHistoryQueryScanRecords || !newest.ScanLimitReached || !newest.HasMore {
		t.Fatalf("unexpected first query page: result=%#v envelope=%#v", result, newest)
	}
	if newest.NextBeforeMessageID == "" {
		t.Fatal("bounded query page did not return continuation")
	}

	_, older := executeSessionHistory(t, registry, execCtx, fmt.Sprintf(`{"query":"NeEdLe","limit":2,"before_message_id":%q}`, newest.NextBeforeMessageID))
	if older.ReturnedCount != 1 || older.SourceMessageIDs[0] != written[10].ID {
		t.Fatalf("query continuation lost older match: %#v", older)
	}

	result, absent := executeSessionHistory(t, registry, execCtx, `{"query":"absent-query-sentinel","limit":2}`)
	if result.IsError || absent.ReturnedCount != 0 || !absent.HasMore || !absent.ScanLimitReached || absent.NextBeforeMessageID != written[8].ID {
		t.Fatalf("empty bounded query page lost its scan boundary: result=%#v envelope=%#v", result, absent)
	}
	result, absentOlder := executeSessionHistory(t, registry, execCtx, fmt.Sprintf(`{"query":"absent-query-sentinel","limit":2,"before_message_id":%q}`, absent.NextBeforeMessageID))
	if result.IsError || absentOlder.ReturnedCount != 0 || absentOlder.HasMore || absentOlder.ScanLimitReached || absentOlder.NextBeforeMessageID != "" || absentOlder.ScannedCount != 8 {
		t.Fatalf("empty query continuation did not terminate at canonical start: result=%#v envelope=%#v", result, absentOlder)
	}
}

func TestReadSessionHistoryQueryKeepsCurrentViewStableDuringConcurrentAppend(t *testing.T) {
	registry, execCtx, meta := newSessionHistoryToolRegistry(t, nil)
	var match session.Message
	for i := 0; i < 200; i++ {
		text := fmt.Sprintf("stable query history %03d", i)
		if i == 150 {
			text = "CONCURRENT_QUERY_MATCH_SENTINEL"
		}
		message := session.NewMessage("user", text)
		if err := execCtx.Store.AppendMessage(meta.ID, message); err != nil {
			t.Fatalf("append baseline query message %d: %v", i, err)
		}
		if i == 150 {
			match = message
		}
	}

	start := make(chan struct{})
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 200; i++ {
			message := session.NewMessage("assistant", fmt.Sprintf("concurrent non-match append %03d", i))
			if err := execCtx.Store.AppendMessage(meta.ID, message); err != nil {
				errCh <- err
				return
			}
		}
	}()
	close(start)

	failure := ""
	for attempt := 0; attempt < 40; attempt++ {
		result, envelope := executeSessionHistory(t, registry, execCtx, `{"query":"concurrent_query_match_sentinel","limit":2}`)
		if result.IsError || envelope.ReturnedCount != 1 || len(envelope.SourceMessageIDs) != 1 || envelope.SourceMessageIDs[0] != match.ID {
			failure = fmt.Sprintf("concurrent query changed bounded match on attempt %d: result=%#v envelope=%#v", attempt, result, envelope)
			break
		}
		if envelope.ScannedCount < 200 || envelope.ScannedCount > MaxSessionHistoryQueryScanRecords || envelope.ScanLimitReached || envelope.HasMore {
			failure = fmt.Sprintf("concurrent query reported an invalid current view on attempt %d: %#v", attempt, envelope)
			break
		}
	}
	wg.Wait()
	select {
	case err := <-errCh:
		t.Fatalf("concurrent append failed: %v", err)
	default:
	}
	if failure != "" {
		t.Fatal(failure)
	}
}

func TestReadSessionHistoryQueryDistinguishesExactMatchLimitFromOverflow(t *testing.T) {
	registry, execCtx, meta := newSessionHistoryToolRegistry(t, nil)
	written := make([]session.Message, 0, 6)
	for i := 0; i < 6; i++ {
		text := fmt.Sprintf("query limit ordinary %d", i)
		if i == 1 || i == 4 {
			text = fmt.Sprintf("QUERY_LIMIT_SENTINEL %d", i)
		}
		message := session.NewMessage("user", text)
		message.Meta = map[string]any{"turn": i + 1}
		if err := execCtx.Store.AppendMessage(meta.ID, message); err != nil {
			t.Fatalf("append query limit message %d: %v", i, err)
		}
		written = append(written, message)
	}

	result, exact := executeSessionHistory(t, registry, execCtx, `{"query":"query_limit_sentinel","limit":2}`)
	if result.IsError || exact.ReturnedCount != 2 || exact.HasMore || exact.ScanLimitReached || exact.SourceMessageIDs[0] != written[1].ID || exact.SourceMessageIDs[1] != written[4].ID {
		t.Fatalf("exact query match limit was misreported as overflow: result=%#v envelope=%#v", result, exact)
	}

	result, newest := executeSessionHistory(t, registry, execCtx, `{"query":"query_limit_sentinel","limit":1}`)
	if result.IsError || newest.ReturnedCount != 1 || !newest.HasMore || newest.ScanLimitReached || newest.SourceMessageIDs[0] != written[4].ID || newest.NextBeforeMessageID != written[4].ID {
		t.Fatalf("query match overflow lost its newest result/cursor: result=%#v envelope=%#v", result, newest)
	}
	result, older := executeSessionHistory(t, registry, execCtx, fmt.Sprintf(`{"query":"query_limit_sentinel","limit":1,"before_message_id":%q}`, newest.NextBeforeMessageID))
	if result.IsError || older.ReturnedCount != 1 || older.HasMore || older.SourceMessageIDs[0] != written[1].ID {
		t.Fatalf("query match overflow continuation lost older match: result=%#v envelope=%#v", result, older)
	}
}

func TestReadSessionHistorySummaryOmitsThinkingOpaqueAndDisplayButKeepsToolReferences(t *testing.T) {
	registry, execCtx, meta := newSessionHistoryToolRegistry(t, nil)
	assistant := session.NewAssistantMessage("assistant visible", "THINKING_HISTORY_SENTINEL", []session.ToolCall{{
		ID:             "call_summary_history",
		Name:           "shell",
		Arguments:      json.RawMessage(`{"command":"printf hi"}`),
		ProviderCallID: "provider_summary_history",
	}})
	assistant.ProviderContentBlocks = []session.ProviderContentBlock{
		{Provider: "openai", Type: "reasoning", Data: "OPENAI_OPAQUE_HISTORY_SENTINEL", Signature: "OPENAI_SIGNATURE_HISTORY_SENTINEL"},
		{Provider: "anthropic", Type: "thinking", Thinking: "ANTHROPIC_OPAQUE_HISTORY_SENTINEL", Signature: "ANTHROPIC_SIGNATURE_HISTORY_SENTINEL"},
		{Provider: "google", Type: "thought", Data: "GOOGLE_OPAQUE_HISTORY_SENTINEL", ThoughtSignature: "GOOGLE_SIGNATURE_HISTORY_SENTINEL"},
	}
	if err := execCtx.Store.AppendMessage(meta.ID, assistant); err != nil {
		t.Fatalf("append assistant: %v", err)
	}
	tool := session.NewToolMessage([]session.ToolResult{{
		ToolCallID:    "call_summary_history",
		Name:          "shell",
		LLMOutput:     "MODEL_TOOL_PREVIEW_SENTINEL",
		DisplayOutput: "DISPLAY_HISTORY_SENTINEL",
		IsError:       true,
		Final:         true,
		Metadata: map[string]any{
			"artifact_path":     "artifacts/tool-outputs/call_summary_history.log",
			"artifact_complete": true,
			"recoverable":       true,
			"failure_class":     "command_nonzero_exit",
		},
	}})
	if err := execCtx.Store.AppendMessage(meta.ID, tool); err != nil {
		t.Fatalf("append tool: %v", err)
	}

	result, envelope := executeSessionHistory(t, registry, execCtx, `{"limit":2}`)
	if result.IsError || envelope.ReturnedCount != 2 {
		t.Fatalf("unexpected history summary: result=%#v envelope=%#v", result, envelope)
	}
	for _, want := range []string{"call_summary_history", "provider_summary_history", "MODEL_TOOL_PREVIEW_SENTINEL", "artifact_path", "command_nonzero_exit", `"thinking_omitted":true`, `"provider_content_blocks_omitted":3`} {
		if !strings.Contains(result.LLMOutput, want) {
			t.Fatalf("history summary missing %q: %s", want, result.LLMOutput)
		}
	}
	for _, forbidden := range []string{
		"THINKING_HISTORY_SENTINEL",
		"OPENAI_OPAQUE_HISTORY_SENTINEL",
		"OPENAI_SIGNATURE_HISTORY_SENTINEL",
		"ANTHROPIC_OPAQUE_HISTORY_SENTINEL",
		"ANTHROPIC_SIGNATURE_HISTORY_SENTINEL",
		"GOOGLE_OPAQUE_HISTORY_SENTINEL",
		"GOOGLE_SIGNATURE_HISTORY_SENTINEL",
		"DISPLAY_HISTORY_SENTINEL",
		"printf hi",
	} {
		if strings.Contains(result.LLMOutput, forbidden) {
			t.Fatalf("history summary exposed %q: %s", forbidden, result.LLMOutput)
		}
	}
}

func TestReadSessionHistoryMessageContentBytePagesAreStableAndUTF8Safe(t *testing.T) {
	registry, execCtx, meta := newSessionHistoryToolRegistry(t, nil)
	message := session.NewMessage("user", strings.Repeat("old user-shaped instruction 🙂 中 ", 30))
	message.Meta = map[string]any{"source": "steer", "turn": 12}
	if err := execCtx.Store.AppendMessage(meta.ID, message); err != nil {
		t.Fatalf("append message: %v", err)
	}

	var rebuilt strings.Builder
	offset := int64(0)
	for page := 0; page < 2048; page++ {
		result, envelope := executeSessionHistory(t, registry, execCtx, fmt.Sprintf(`{"message_id":%q,"byte_offset":%d,"byte_limit":17}`, message.ID, offset))
		if result.IsError || envelope.Mode != "message_content" || envelope.MessageID != message.ID || envelope.ContentSchemaVersion != session.HistoricalMessageContentSchemaVersion || !utf8.ValidString(envelope.Content) {
			t.Fatalf("invalid content page %d: result=%#v envelope=%#v", page, result, envelope)
		}
		rebuilt.WriteString(envelope.Content)
		if !envelope.HasMore {
			if envelope.NextByteOffset != envelope.TotalBytes {
				t.Fatalf("EOF cursor mismatch: %#v", envelope)
			}
			break
		}
		if envelope.NextByteOffset <= offset {
			t.Fatalf("content cursor did not advance: %#v", envelope)
		}
		offset = envelope.NextByteOffset
	}
	full, err := execCtx.Store.LoadMessageContentRange(meta.ID, message.ID, 0, session.MaxHistoricalMessageContentPageBytes)
	if err != nil {
		t.Fatalf("load baseline content: %v", err)
	}
	if rebuilt.String() != full.Content || !strings.Contains(rebuilt.String(), "old user-shaped instruction") || !strings.Contains(rebuilt.String(), `"historical_reference":true`) {
		t.Fatalf("tool byte pages changed stable representation\nrebuilt=%s\nbaseline=%s", rebuilt.String(), full.Content)
	}
	emojiOffset := strings.Index(full.Content, "🙂")
	if emojiOffset < 0 {
		t.Fatal("stable history representation lost multibyte content")
	}
	tooSmall, _ := executeSessionHistory(t, registry, execCtx, fmt.Sprintf(`{"message_id":%q,"byte_offset":%d,"byte_limit":1}`, message.ID, emojiOffset))
	if !tooSmall.IsError || tooSmall.Metadata[MetadataFailureClass] != FailureClassOutputBudgetTooSmall || tooSmall.Metadata["error_code"] != FailureClassOutputBudgetTooSmall {
		t.Fatalf("sub-rune history page must return typed output-budget error: %#v", tooSmall)
	}
}

func TestReadSessionHistoryOutputEnvelopeHonorsToolByteBudgetWithoutBrokenCursor(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.ToolOutput.LLMOutputMaxBytes = 1200
	registry, execCtx, meta := newSessionHistoryToolRegistry(t, cfg)
	for i := 0; i < 20; i++ {
		message := session.NewMessage("user", fmt.Sprintf("%02d-%s", i, strings.Repeat("large preview 🙂 ", 100)))
		if err := execCtx.Store.AppendMessage(meta.ID, message); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	result, envelope := executeSessionHistory(t, registry, execCtx, `{"limit":20}`)
	if result.IsError || len(result.LLMOutput) > cfg.Runtime.ToolOutput.LLMOutputMaxBytes || !json.Valid([]byte(result.LLMOutput)) || !utf8.ValidString(result.LLMOutput) {
		t.Fatalf("history output exceeded/broke cap: bytes=%d result=%#v", len(result.LLMOutput), result)
	}
	if envelope.ReturnedCount == 0 || envelope.ReturnedCount >= 20 || !envelope.HasMore || envelope.NextBeforeMessageID == "" || len(envelope.SourceMessageIDs) != envelope.ReturnedCount {
		t.Fatalf("byte-limited envelope lost continuation facts: %#v", envelope)
	}
}

func TestReadSessionHistoryReturnsTypedBudgetErrorWhenOneRecordEnvelopeCannotFit(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.ToolOutput.LLMOutputMaxBytes = 512
	registry, execCtx, meta := newSessionHistoryToolRegistry(t, cfg)
	message := session.NewMessage("user", strings.Repeat("single oversized history preview 🙂 ", 80))
	message.ID = "msg_" + strings.Repeat("x", 252)
	if err := execCtx.Store.AppendMessage(meta.ID, message); err != nil {
		t.Fatalf("append oversized summary: %v", err)
	}

	result, _ := executeSessionHistory(t, registry, execCtx, `{"limit":1}`)
	if !result.IsError || result.Metadata[MetadataFailureClass] != FailureClassOutputBudgetTooSmall || result.Metadata["error_code"] != FailureClassOutputBudgetTooSmall {
		t.Fatalf("single-record overflow must be a typed budget error: %#v", result)
	}

	result, envelope := executeSessionHistory(t, registry, execCtx, fmt.Sprintf(`{"message_id":%q,"byte_limit":512}`, message.ID))
	if result.IsError {
		if result.Metadata[MetadataFailureClass] != FailureClassOutputBudgetTooSmall {
			t.Fatalf("long-id content page returned the wrong failure: %#v", result)
		}
		return
	}
	if len(result.LLMOutput) > cfg.Runtime.ToolOutput.LLMOutputMaxBytes || !json.Valid([]byte(result.LLMOutput)) || envelope.MessageID != message.ID || envelope.NextByteOffset <= 0 {
		t.Fatalf("long-id content page broke its bounded envelope: result=%#v envelope=%#v", result, envelope)
	}
}

func TestReadSessionHistoryIsCurrentSessionOnlyAndFailsClosedForSymlinkOrCorruption(t *testing.T) {
	t.Run("sibling message is invisible", func(t *testing.T) {
		registry, execCtx, meta := newSessionHistoryToolRegistry(t, nil)
		sibling := meta
		sibling.ID = session.NewSessionID()
		sibling.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		state := session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		if err := execCtx.Store.Create(sibling, state); err != nil {
			t.Fatalf("create sibling: %v", err)
		}
		message := session.NewMessage("user", "sibling-only sentinel")
		if err := execCtx.Store.AppendMessage(sibling.ID, message); err != nil {
			t.Fatalf("append sibling: %v", err)
		}
		result, _ := executeSessionHistory(t, registry, execCtx, fmt.Sprintf(`{"message_id":%q,"byte_limit":64}`, message.ID))
		if !result.IsError || result.Metadata[MetadataFailureClass] != FailureClassNotFound || strings.Contains(result.LLMOutput, "sibling-only sentinel") {
			t.Fatalf("cross-session message became visible: %#v", result)
		}
	})

	t.Run("parent and child messages are mutually invisible", func(t *testing.T) {
		registry, parentCtx, parent := newSessionHistoryToolRegistry(t, nil)
		parentMessage := session.NewMessage("user", "parent-only history sentinel")
		if err := parentCtx.Store.AppendMessage(parent.ID, parentMessage); err != nil {
			t.Fatalf("append parent: %v", err)
		}

		child := parent
		child.ID = session.NewSessionID()
		child.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		child.ParentSessionID = parent.ID
		child.RootSessionID = parent.ID
		child.AgentRole = "generator"
		child.Depth = 1
		state := session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		if err := parentCtx.Store.Create(child, state); err != nil {
			t.Fatalf("create child: %v", err)
		}
		childMessage := session.NewMessage("assistant", "child-only history sentinel")
		if err := parentCtx.Store.AppendMessage(child.ID, childMessage); err != nil {
			t.Fatalf("append child: %v", err)
		}

		fromParent, _ := executeSessionHistory(t, registry, parentCtx, fmt.Sprintf(`{"message_id":%q,"byte_limit":128}`, childMessage.ID))
		if !fromParent.IsError || fromParent.Metadata[MetadataFailureClass] != FailureClassNotFound || strings.Contains(fromParent.LLMOutput, "child-only history sentinel") {
			t.Fatalf("parent read child history: %#v", fromParent)
		}

		childCtx := parentCtx
		childCtx.SessionID = child.ID
		fromChild, _ := executeSessionHistory(t, registry, childCtx, fmt.Sprintf(`{"message_id":%q,"byte_limit":128}`, parentMessage.ID))
		if !fromChild.IsError || fromChild.Metadata[MetadataFailureClass] != FailureClassNotFound || strings.Contains(fromChild.LLMOutput, "parent-only history sentinel") {
			t.Fatalf("child read parent history: %#v", fromChild)
		}
		ownChild, ownEnvelope := executeSessionHistory(t, registry, childCtx, fmt.Sprintf(`{"message_id":%q,"byte_limit":512}`, childMessage.ID))
		if ownChild.IsError || ownEnvelope.SourceSessionID != child.ID || !strings.Contains(ownEnvelope.Content, "child-only history sentinel") {
			t.Fatalf("child could not read its own history: result=%#v envelope=%#v", ownChild, ownEnvelope)
		}
	})

	t.Run("symlink and corrupt canonical log", func(t *testing.T) {
		registry, execCtx, meta := newSessionHistoryToolRegistry(t, nil)
		message := session.NewMessage("user", "current-only sentinel")
		if err := execCtx.Store.AppendMessage(meta.ID, message); err != nil {
			t.Fatalf("append current: %v", err)
		}
		messagePath := filepath.Join(execCtx.Store.SessionDir(meta.ID), "messages.jsonl")
		outside := filepath.Join(t.TempDir(), "outside.jsonl")
		if err := os.WriteFile(outside, []byte(`{"id":"outside"}`+"\n"), 0o600); err != nil {
			t.Fatalf("write outside: %v", err)
		}
		if err := os.Remove(messagePath); err != nil {
			t.Fatalf("remove messages: %v", err)
		}
		if err := os.Symlink(outside, messagePath); err != nil {
			t.Fatalf("symlink messages: %v", err)
		}
		symlinked, _ := executeSessionHistory(t, registry, execCtx, `{}`)
		if !symlinked.IsError || symlinked.Metadata["error_code"] != "session_history_unavailable" || strings.Contains(symlinked.LLMOutput, "outside") {
			t.Fatalf("symlink did not fail closed: %#v", symlinked)
		}

		if err := os.Remove(messagePath); err != nil {
			t.Fatalf("remove symlink: %v", err)
		}
		if err := os.WriteFile(messagePath, []byte("{broken-json\n"), 0o600); err != nil {
			t.Fatalf("write corrupt messages: %v", err)
		}
		corrupt, _ := executeSessionHistory(t, registry, execCtx, `{}`)
		if !corrupt.IsError || corrupt.Metadata["error_code"] != "session_history_unavailable" {
			t.Fatalf("corrupt history did not fail closed: %#v", corrupt)
		}
	})
}
