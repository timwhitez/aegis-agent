package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func newSessionHistoryStore(t *testing.T) (*Store, SessionMetadata) {
	t.Helper()
	store := NewStoreWithDirMode(filepath.Join(t.TempDir(), "sessions"), 0o700)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create history session: %v", err)
	}
	return store, meta
}

func loadAllHistoricalMessageContent(t *testing.T, store *Store, sessionID, messageID string, pageBytes int) string {
	t.Helper()
	var out strings.Builder
	offset := int64(0)
	for page := 0; page < 4096; page++ {
		window, err := store.LoadMessageContentRange(sessionID, messageID, offset, pageBytes)
		if err != nil {
			t.Fatalf("load content page %d: %v", page, err)
		}
		if window.SchemaVersion != HistoricalMessageContentSchemaVersion || !window.HistoricalReference {
			t.Fatalf("unexpected content schema/boundary: %#v", window)
		}
		if window.MessageID != messageID || window.NextByteOffset < offset || !utf8.ValidString(window.Content) {
			t.Fatalf("invalid content page %d: %#v", page, window)
		}
		out.WriteString(window.Content)
		if !window.HasMore {
			if window.NextByteOffset != window.TotalBytes {
				t.Fatalf("EOF cursor=%d total=%d", window.NextByteOffset, window.TotalBytes)
			}
			return out.String()
		}
		if window.NextByteOffset == offset {
			t.Fatalf("content cursor did not advance at page %d: %#v", page, window)
		}
		offset = window.NextByteOffset
	}
	t.Fatal("historical content pagination did not terminate")
	return ""
}

func TestStoreLoadMessageContentRangeSanitizesOpaqueFieldsAndPaginatesUTF8(t *testing.T) {
	store, meta := newSessionHistoryStore(t)
	message := NewAssistantMessage("prefix🙂中suffix", "THINKING_SENTINEL", []ToolCall{{
		ID:             "call_history_1",
		Name:           "read_file",
		Arguments:      []byte(`{"path":"notes.txt","byte_offset":0,"byte_limit":64}`),
		ProviderCallID: "provider_call_history_1",
	}})
	message.ProviderContentBlocks = []ProviderContentBlock{
		{
			Provider:  "openai",
			Type:      "reasoning",
			Data:      "OPENAI_OPAQUE_REPLAY_SENTINEL",
			Thinking:  "OPENAI_OPAQUE_THINKING_SENTINEL",
			Signature: "OPENAI_OPAQUE_SIGNATURE_SENTINEL",
		},
		{
			Provider:  "anthropic",
			Type:      "thinking",
			Thinking:  "ANTHROPIC_OPAQUE_THINKING_SENTINEL",
			Signature: "ANTHROPIC_OPAQUE_SIGNATURE_SENTINEL",
		},
		{
			Provider:         "google",
			Type:             "thought",
			Data:             "GOOGLE_OPAQUE_REPLAY_SENTINEL",
			ThoughtSignature: "GOOGLE_OPAQUE_SIGNATURE_SENTINEL",
		},
	}
	message.Meta = map[string]any{"source": "assistant", "turn": 7}
	if err := store.AppendMessage(meta.ID, message); err != nil {
		t.Fatalf("append assistant: %v", err)
	}

	full := loadAllHistoricalMessageContent(t, store, meta.ID, message.ID, 11)
	for _, want := range []string{"prefix🙂中suffix", "call_history_1", "provider_call_history_1", "read_file", "notes.txt", `"historical_reference":true`} {
		if !strings.Contains(full, want) {
			t.Fatalf("historical content missing %q: %s", want, full)
		}
	}
	for _, forbidden := range []string{
		"THINKING_SENTINEL",
		"OPENAI_OPAQUE_REPLAY_SENTINEL",
		"OPENAI_OPAQUE_THINKING_SENTINEL",
		"OPENAI_OPAQUE_SIGNATURE_SENTINEL",
		"ANTHROPIC_OPAQUE_THINKING_SENTINEL",
		"ANTHROPIC_OPAQUE_SIGNATURE_SENTINEL",
		"GOOGLE_OPAQUE_REPLAY_SENTINEL",
		"GOOGLE_OPAQUE_SIGNATURE_SENTINEL",
	} {
		if strings.Contains(full, forbidden) {
			t.Fatalf("historical content exposed %q: %s", forbidden, full)
		}
	}

	emoji := strings.Index(full, "🙂")
	if emoji < 0 {
		t.Fatal("emoji missing from stable representation")
	}
	mid, err := store.LoadMessageContentRange(meta.ID, message.ID, int64(emoji+1), 8)
	if err != nil {
		t.Fatalf("load mid-rune range: %v", err)
	}
	if !mid.StartAdjusted || mid.EffectiveByteStart <= int64(emoji+1) || strings.HasPrefix(mid.Content, "🙂") || !utf8.ValidString(mid.Content) {
		t.Fatalf("mid-rune range did not advance to a safe boundary: %#v", mid)
	}

	eof, err := store.LoadMessageContentRange(meta.ID, message.ID, int64(len(full)+100), 64)
	if err != nil {
		t.Fatalf("load EOF: %v", err)
	}
	if eof.Content != "" || eof.HasMore || eof.ReturnedBytes != 0 || eof.NextByteOffset != eof.TotalBytes {
		t.Fatalf("unexpected EOF page: %#v", eof)
	}
}

func TestStoreLoadMessageContentRangeKeepsToolLLMOutputAndReferenceMetadataOnly(t *testing.T) {
	store, meta := newSessionHistoryStore(t)
	message := NewToolMessage([]ToolResult{{
		ToolCallID:    "call_tool_history",
		Name:          "shell",
		LLMOutput:     "MODEL_VISIBLE_TOOL_SENTINEL",
		DisplayOutput: "DISPLAY_ONLY_SENTINEL",
		IsError:       true,
		Final:         true,
		Metadata: map[string]any{
			"artifact_path":     "artifacts/tool-outputs/call_tool_history.log",
			"artifact_complete": true,
			"recoverable":       true,
			"failure_class":     "command_nonzero_exit",
			"secret_internal":   "ARBITRARY_METADATA_SENTINEL",
		},
	}})
	if err := store.AppendMessage(meta.ID, message); err != nil {
		t.Fatalf("append tool message: %v", err)
	}

	full := loadAllHistoricalMessageContent(t, store, meta.ID, message.ID, 37)
	for _, want := range []string{"MODEL_VISIBLE_TOOL_SENTINEL", "call_tool_history", "artifact_path", "artifact_complete", "recoverable", "command_nonzero_exit"} {
		if !strings.Contains(full, want) {
			t.Fatalf("tool history missing %q: %s", want, full)
		}
	}
	for _, forbidden := range []string{"DISPLAY_ONLY_SENTINEL", "ARBITRARY_METADATA_SENTINEL", "secret_internal", "display_output"} {
		if strings.Contains(full, forbidden) {
			t.Fatalf("tool history exposed %q: %s", forbidden, full)
		}
	}
}

func TestStoreLoadMessageContentRangeRejectsInvalidArgumentsAndUnknownMessage(t *testing.T) {
	store, meta := newSessionHistoryStore(t)
	message := NewMessage("user", "known message")
	if err := store.AppendMessage(meta.ID, message); err != nil {
		t.Fatalf("append: %v", err)
	}

	for name, args := range map[string]struct {
		offset int64
		limit  int
	}{
		"negative offset": {-1, 32},
		"zero limit":      {0, 0},
		"negative limit":  {0, -1},
		"over max limit":  {0, MaxHistoricalMessageContentPageBytes + 1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.LoadMessageContentRange(meta.ID, message.ID, args.offset, args.limit); err == nil {
				t.Fatal("expected invalid range error")
			}
		})
	}

	_, err := store.LoadMessageContentRange(meta.ID, "msg_missing_history", 0, 32)
	if !errors.Is(err, ErrMessageNotFound) {
		t.Fatalf("expected ErrMessageNotFound, got %v", err)
	}

	multibyte := NewMessage("user", "🙂")
	if err := store.AppendMessage(meta.ID, multibyte); err != nil {
		t.Fatalf("append multibyte message: %v", err)
	}
	full := loadAllHistoricalMessageContent(t, store, meta.ID, multibyte.ID, 64)
	emojiOffset := strings.Index(full, "🙂")
	if emojiOffset < 0 {
		t.Fatal("multibyte message representation lost emoji")
	}
	if _, err := store.LoadMessageContentRange(meta.ID, multibyte.ID, int64(emojiOffset), 1); !errors.Is(err, ErrMessageContentWindowTooSmall) {
		t.Fatalf("expected ErrMessageContentWindowTooSmall, got %v", err)
	}
}

func TestStoreLoadMessageContentRangeFailsClosedForSymlinkAndCorruptJSONL(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		store, meta := newSessionHistoryStore(t)
		message := NewMessage("user", "inside session")
		if err := store.AppendMessage(meta.ID, message); err != nil {
			t.Fatalf("append: %v", err)
		}
		messagePath := filepath.Join(store.SessionDir(meta.ID), "messages.jsonl")
		outside := filepath.Join(t.TempDir(), "outside.jsonl")
		if err := os.WriteFile(outside, []byte("{}\n"), 0o600); err != nil {
			t.Fatalf("write outside: %v", err)
		}
		if err := os.Remove(messagePath); err != nil {
			t.Fatalf("remove messages: %v", err)
		}
		if err := os.Symlink(outside, messagePath); err != nil {
			t.Fatalf("symlink messages: %v", err)
		}
		if _, err := store.LoadMessageContentRange(meta.ID, message.ID, 0, 32); err == nil || !strings.Contains(strings.ToLower(err.Error()), "symlink") {
			t.Fatalf("expected symlink rejection, got %v", err)
		}
	})

	t.Run("corrupt json and utf8", func(t *testing.T) {
		store, meta := newSessionHistoryStore(t)
		message := NewMessage("user", "valid prefix")
		if err := store.AppendMessage(meta.ID, message); err != nil {
			t.Fatalf("append: %v", err)
		}
		messagePath := filepath.Join(store.SessionDir(meta.ID), "messages.jsonl")
		file, err := os.OpenFile(messagePath, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatalf("open messages: %v", err)
		}
		_, writeErr := file.Write([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}', '\n'})
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatalf("corrupt messages: write=%v close=%v", writeErr, closeErr)
		}
		if _, err := store.LoadMessageContentRange(meta.ID, "msg_missing_after_corruption", 0, 32); err == nil || errors.Is(err, ErrMessageNotFound) {
			t.Fatalf("corruption must fail before not-found, got %v", err)
		}
	})
}

func TestStoreLoadMessageContentRangeIsDeterministicDuringConcurrentAppend(t *testing.T) {
	store, meta := newSessionHistoryStore(t)
	target := NewMessage("user", strings.Repeat("stable🙂", 80))
	if err := store.AppendMessage(meta.ID, target); err != nil {
		t.Fatalf("append target: %v", err)
	}
	baseline := loadAllHistoricalMessageContent(t, store, meta.ID, target.ID, 97)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			message := NewMessage("assistant", fmt.Sprintf("concurrent append %03d", i))
			if err := store.AppendMessage(meta.ID, message); err != nil {
				t.Errorf("append %d: %v", i, err)
				return
			}
		}
	}()
	for i := 0; i < 40; i++ {
		if got := loadAllHistoricalMessageContent(t, store, meta.ID, target.ID, 97); got != baseline {
			t.Fatalf("concurrent append changed target representation on iteration %d", i)
		}
	}
	wg.Wait()
}

func TestStoreVisitMessagesUsesFixedSnapshotAndAllowsCallbackAppend(t *testing.T) {
	store, meta := newSessionHistoryStore(t)
	for i := 0; i < 3; i++ {
		if err := store.AppendMessage(meta.ID, NewMessage("user", fmt.Sprintf("snapshot message %d", i))); err != nil {
			t.Fatalf("append baseline %d: %v", i, err)
		}
	}

	type visitResult struct {
		count int
		err   error
	}
	done := make(chan visitResult, 1)
	go func() {
		count := 0
		err := store.VisitMessages(meta.ID, func(Message) error {
			count++
			if count == 1 {
				return store.AppendMessage(meta.ID, NewMessage("assistant", "appended from visitor callback"))
			}
			return nil
		})
		done <- visitResult{count: count, err: err}
	}()

	select {
	case result := <-done:
		if result.err != nil || result.count != 3 {
			t.Fatalf("visitor did not keep its fixed snapshot: %#v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("VisitMessages callback append deadlocked behind its own read lock")
	}

	messages, err := store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("load messages after callback append: %v", err)
	}
	if len(messages) != 4 || messages[3].Text != "appended from visitor callback" {
		t.Fatalf("callback append was not durable: %#v", messages)
	}
}
