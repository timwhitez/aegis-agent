package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/events"
	"go-cli-agent/internal/session"
)

func TestTruncateTextKeepsUTF8Boundaries(t *testing.T) {
	input := strings.Repeat("已完成全部测试并生成最终报告。", 20)
	output := truncateText(input, 500)
	if !utf8.ValidString(output) {
		t.Fatalf("expected valid UTF-8 output, got %q", output)
	}
	if strings.ContainsRune(output, utf8.RuneError) {
		t.Fatalf("expected no replacement rune from mid-rune truncation, got %q", output)
	}
	if !strings.HasSuffix(output, "...") {
		t.Fatalf("expected truncation marker, got %q", output)
	}
}

func TestCompactTextForContextKeepsUTF8Boundaries(t *testing.T) {
	input := strings.Repeat("前缀", 500) + "middle" + strings.Repeat("后缀", 500)
	output := compactTextForContext(input, "utf8_test")
	if !utf8.ValidString(output) {
		t.Fatalf("expected valid UTF-8 compacted output, got %q", output)
	}
	if strings.ContainsRune(output, utf8.RuneError) {
		t.Fatalf("expected no replacement rune from mid-rune compaction, got %q", output)
	}
	if !strings.Contains(output, "HEAD:") || !strings.Contains(output, "TAIL:") {
		t.Fatalf("expected head/tail compaction marker, got %q", output)
	}
}

func TestCompactRawJSONForContextPreservesClosedToolArgumentShape(t *testing.T) {
	raw := json.RawMessage(`{"path":"reports/final.md","content":"` + strings.Repeat("A", 1800) + `MIDDLE` + strings.Repeat("Z", 1800) + `","append":false}`)
	compacted := compactRawJSONForContext(raw)
	if string(compacted) == string(raw) {
		t.Fatal("expected long string argument to be compacted")
	}
	for _, reserved := range []string{"compacted_for_context", "original_chars", "head_tail"} {
		if strings.Contains(string(compacted), reserved) {
			t.Fatalf("compacted tool arguments must not expose reserved replay marker %q: %s", reserved, string(compacted))
		}
	}
	var decoded struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Append  bool   `json:"append"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(compacted)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatalf("expected compacted arguments to keep the closed tool schema shape: %v\n%s", err, string(compacted))
	}
	if decoded.Path != "reports/final.md" || decoded.Append {
		t.Fatalf("expected non-compacted fields to survive, got %#v", decoded)
	}
	if !strings.Contains(decoded.Content, "[Compacted previous_tool_arguments") || !strings.Contains(decoded.Content, "suffix:") {
		t.Fatalf("expected string value to carry compaction marker, got %q", decoded.Content)
	}
	if strings.Contains(decoded.Content, "MIDDLE") {
		t.Fatalf("expected middle of compacted argument string to be omitted, got %q", decoded.Content)
	}
}

func TestCompactorWritesDurableSummaryArtifact(t *testing.T) {
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	state := session.State{
		Status:           session.StatusRunning,
		Phase:            "prepare",
		UpdatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		LastError:        "build failed",
		PauseReason:      "keyboard_interrupt",
		IncompleteReason: "incomplete_no_finish",
	}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}

	keyPath := filepath.Join(workdir, "docs", "plan.md")
	messages := []session.Message{
		session.NewMessage("user", "Continue the implementation."),
		session.NewToolMessage([]session.ToolResult{{
			Name:          "read_file",
			LLMOutput:     strings.Repeat("A", 64),
			DisplayOutput: strings.Repeat("A", 64),
			Metadata: map[string]any{
				"path": keyPath,
			},
		}}),
		func() session.Message {
			msg := session.NewMessage("user", "Newest steer: finish the compaction handoff without restarting.")
			msg.Meta = map[string]any{
				"source":    "steer",
				"interrupt": true,
			}
			return msg
		}(),
		session.NewToolMessage([]session.ToolResult{{
			Name:          "shell",
			LLMOutput:     "Error: npm test failed",
			DisplayOutput: "Error: npm test failed",
			IsError:       true,
		}}),
		session.NewAssistantMessage(strings.Repeat("B", 128), "", nil),
	}
	todo := []session.TodoItem{
		{Content: "Audit provider contracts", Status: "completed", Priority: "high", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)},
		{Content: "Finish compaction artifact", Status: "in_progress", Priority: "high", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)},
		{Content: "Refresh validation evidence", Status: "in_progress", Priority: "medium", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)},
	}
	tasks := []session.Task{
		{
			ID:        "task_0001",
			Subject:   "Build runtime",
			Status:    "completed",
			Priority:  "high",
			CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
			UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		},
		{
			ID:          "task_0002",
			Subject:     "Improve compaction",
			Description: "Write richer summary artifacts",
			Status:      "in_progress",
			Priority:    "high",
			CreatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
			UpdatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		},
		{
			ID:        "task_0003",
			Subject:   "Review docs",
			Status:    "pending",
			BlockedBy: []string{"task_0002"},
			Priority:  "medium",
			CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
			UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		},
		{
			ID:        "task_0004",
			Subject:   "Drop stale approach",
			Status:    "cancelled",
			Priority:  "low",
			CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
			UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		},
	}

	var emitted []events.Event
	compactor := newCompactor(store)
	view, err := compactor.Build(meta.ID, meta.Workdir, state, messages, todo, tasks, 32, 1, func(evt events.Event) error {
		emitted = append(emitted, evt)
		return nil
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(view) == 0 || !strings.Contains(view[0].Text, "[Conversation compacted]") {
		t.Fatalf("expected compacted lead message, got %#v", view)
	}
	if got, _ := view[0].Meta["source"].(string); got != "compaction_summary" {
		t.Fatalf("expected compaction summary source metadata, got %#v", view[0].Meta)
	}
	foundLatestExternal := false
	for _, msg := range view[1:] {
		if msg.Role == "user" && msg.Text == "Newest steer: finish the compaction handoff without restarting." {
			foundLatestExternal = true
			break
		}
	}
	if !foundLatestExternal {
		t.Fatalf("expected compaction view to preserve the latest external user instruction, got %#v", view)
	}

	summaryFiles, err := os.ReadDir(filepath.Join(store.SessionDir(meta.ID), "artifacts", "compactions"))
	if err != nil {
		t.Fatalf("read compactions dir: %v", err)
	}
	if len(summaryFiles) != 1 {
		t.Fatalf("expected 1 compaction summary, got %d", len(summaryFiles))
	}

	data, err := os.ReadFile(filepath.Join(store.SessionDir(meta.ID), "artifacts", "compactions", summaryFiles[0].Name()))
	if err != nil {
		t.Fatalf("read summary artifact: %v", err)
	}
	var summary map[string]any
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}

	if summary["current_in_progress_todo"] == nil {
		t.Fatalf("expected current_in_progress_todo, got %#v", summary)
	}
	inProgressTodos, _ := summary["in_progress_todos"].([]any)
	if len(inProgressTodos) != 2 {
		t.Fatalf("expected all in-progress todos in compaction summary, got %#v", summary["in_progress_todos"])
	}
	if summary["current_in_progress_task"] == nil {
		t.Fatalf("expected current_in_progress_task, got %#v", summary)
	}
	if summary["current_goal"] == nil {
		t.Fatalf("expected current_goal handoff field, got %#v", summary)
	}
	latestExternal, _ := summary["latest_external_instruction"].(map[string]any)
	if latestExternal["text"] != "Newest steer: finish the compaction handoff without restarting." || latestExternal["source"] != "steer" {
		t.Fatalf("expected newest steer as latest external instruction, got %#v", summary["latest_external_instruction"])
	}
	latestSteer, _ := summary["latest_steer_constraints"].(map[string]any)
	if latestSteer["text"] != "Newest steer: finish the compaction handoff without restarting." || latestSteer["interrupt"] != true {
		t.Fatalf("expected latest steer constraints, got %#v", summary["latest_steer_constraints"])
	}
	openItems, _ := summary["open_items"].([]any)
	if len(openItems) == 0 {
		t.Fatalf("expected open_items, got %#v", summary["open_items"])
	}
	validated, _ := summary["validated_conclusions"].([]any)
	if len(validated) == 0 {
		t.Fatalf("expected validated_conclusions, got %#v", summary["validated_conclusions"])
	}
	handoff, _ := summary["handoff_summary"].(map[string]any)
	if handoff["goal"] == nil || handoff["todo"] == nil || handoff["key_paths"] == nil || handoff["latest_external_instruction"] == nil {
		t.Fatalf("expected structured handoff summary, got %#v", summary["handoff_summary"])
	}
	if summary["artifact_memory"] == nil {
		t.Fatalf("expected artifact_memory, got %#v", summary)
	}
	if summary["high_value_proofs"] == nil {
		t.Fatalf("expected high_value_proofs, got %#v", summary)
	}
	if summary["proof_read_budget"] == nil {
		t.Fatalf("expected proof_read_budget, got %#v", summary)
	}
	if summary["project_memory_stack"] == nil {
		t.Fatalf("expected project_memory_stack, got %#v", summary)
	}
	if summary["completed_task_count"] != float64(1) || summary["cancelled_task_count"] != float64(1) || summary["done_task_count"] != float64(2) {
		t.Fatalf("expected compaction summary to separate completed/cancelled tasks, got %#v", summary)
	}
	keyPaths, _ := summary["key_paths"].([]any)
	if len(keyPaths) == 0 || keyPaths[0] != filepath.Join("docs", "plan.md") {
		t.Fatalf("expected relative key path, got %#v", summary["key_paths"])
	}
	guidance, _ := summary["next_step_guidance"].([]any)
	if len(guidance) < 4 {
		t.Fatalf("expected compaction next_step_guidance, got %#v", summary["next_step_guidance"])
	}
	if !strings.Contains(guidance[0].(string), "artifact_memory") || !strings.Contains(guidance[0].(string), "project_memory_stack") || !strings.Contains(guidance[0].(string), "high_value_proofs") {
		t.Fatalf("expected updated guidance, got %#v", summary["next_step_guidance"])
	}
	proofs, _ := summary["high_value_proofs"].([]any)
	if len(proofs) == 0 {
		t.Fatalf("expected at least one pinned proof, got %#v", summary["high_value_proofs"])
	}
	budget, _ := summary["proof_read_budget"].(map[string]any)
	if budget["reserved_final_targeted_reads"] != float64(0) {
		t.Fatalf("expected no reserved proof-read budget, got %#v", summary["proof_read_budget"])
	}
	unresolved, _ := summary["unresolved_issues"].([]any)
	if len(unresolved) == 0 {
		t.Fatalf("expected unresolved issues, got %#v", summary["unresolved_issues"])
	}
	recentFailure, _ := summary["recent_failure_or_pause"].(map[string]any)
	if recentFailure["last_error"] != "build failed" || recentFailure["pause_reason"] != "keyboard_interrupt" {
		t.Fatalf("expected recent failure details, got %#v", summary["recent_failure_or_pause"])
	}
	completed, _ := summary["completed_items"].([]any)
	if len(completed) == 0 {
		t.Fatalf("expected completed items, got %#v", summary["completed_items"])
	}

	if messages[0].Text != "Continue the implementation." {
		t.Fatalf("expected original messages to remain unchanged, got %#v", messages[0])
	}
	if messages[1].ToolResults[0].LLMOutput != strings.Repeat("A", 64) {
		t.Fatalf("expected original tool result to remain unchanged, got %#v", messages[1].ToolResults[0])
	}

	if len(emitted) != 2 || emitted[0].Type != "compact.started" || emitted[1].Type != "compact.finished" {
		t.Fatalf("expected compact lifecycle events, got %#v", emitted)
	}
	for _, evt := range emitted {
		if evt.Data["completed_task_count"] != 1 || evt.Data["cancelled_task_count"] != 1 || evt.Data["done_task_count"] != 2 {
			t.Fatalf("expected compaction event to separate completed/cancelled tasks, got %#v", evt.Data)
		}
	}
	if emitted[1].Data["reason"] != "input_char_threshold_exceeded" {
		t.Fatalf("expected compact reason metadata, got %#v", emitted[1].Data)
	}
	if emitted[0].Data["proof_read_budget"] == nil || emitted[1].Data["proof_read_budget"] == nil {
		t.Fatalf("expected proof_read_budget in compact events, got %#v / %#v", emitted[0].Data, emitted[1].Data)
	}
	if emitted[1].Data["artifact_memory_count"] != len(artifactMemory(summary)) {
		t.Fatalf("expected artifact memory count metadata, got %#v", emitted[1].Data)
	}
	if emitted[1].Data["high_value_proof_count"] != len(highValueProofs(summary)) {
		t.Fatalf("expected high-value proof count metadata, got %#v", emitted[1].Data)
	}
	present, _ := emitted[1].Data["project_memory_present"].([]string)
	if len(present) != 0 {
		t.Fatalf("expected no project-memory files in compact event, got %#v", emitted[1].Data)
	}
}

func TestCompactorIncludesSemanticSummaryWhenProviderSummarySucceeds(t *testing.T) {
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-test",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	state := session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	messages := []session.Message{
		session.NewMessage("user", "middle decision: use context windows"),
		session.NewAssistantMessage(strings.Repeat("A", 512), "", nil),
		session.NewMessage("user", "latest instruction"),
	}
	var sawDropped bool
	var emitted []events.Event
	profile := compactionContextProfile{
		Source:                "test",
		InputCharThreshold:    32,
		KeepRecentToolResults: 1,
		HysteresisDeltaChars:  8,
		KeepRecentMessages:    1,
	}
	view, _, didCompact, err := newCompactor(store).build(context.Background(), meta.ID, meta.Workdir, state, messages, nil, nil, profile, 0, 0, func(_ context.Context, dropped []session.Message, budget int) (string, error) {
		sawDropped = len(dropped) > 0 && budget > 0
		return "semantic middle summary", nil
	}, func(evt events.Event) error {
		emitted = append(emitted, evt)
		return nil
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !didCompact || len(view) == 0 {
		t.Fatalf("expected compaction, didCompact=%v view=%#v", didCompact, view)
	}
	if !sawDropped {
		t.Fatal("expected semantic summarizer to receive dropped messages and budget")
	}
	if !strings.Contains(view[0].Text, `"semantic_summary": "semantic middle summary"`) {
		t.Fatalf("expected semantic summary in compacted view, got %q", view[0].Text)
	}
	if len(emitted) != 2 || emitted[1].Data["semantic_summary_status"] != "ok" {
		t.Fatalf("expected ok semantic summary event, got %#v", emitted)
	}
}

func TestCompactorFallsBackWhenSemanticSummaryFails(t *testing.T) {
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-test",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	state := session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	messages := []session.Message{
		session.NewMessage("user", "middle decision"),
		session.NewAssistantMessage(strings.Repeat("A", 512), "", nil),
		session.NewMessage("user", "latest instruction"),
	}
	var emitted []events.Event
	profile := compactionContextProfile{
		Source:                "test",
		InputCharThreshold:    32,
		KeepRecentToolResults: 1,
		HysteresisDeltaChars:  8,
		KeepRecentMessages:    1,
	}
	view, _, didCompact, err := newCompactor(store).build(context.Background(), meta.ID, meta.Workdir, state, messages, nil, nil, profile, 0, 0, func(context.Context, []session.Message, int) (string, error) {
		return "", errors.New("summary unavailable")
	}, func(evt events.Event) error {
		emitted = append(emitted, evt)
		return nil
	})
	if err != nil {
		t.Fatalf("build should not fail on semantic summary error: %v", err)
	}
	if !didCompact || len(view) == 0 {
		t.Fatalf("expected compaction, didCompact=%v view=%#v", didCompact, view)
	}
	if strings.Contains(view[0].Text, "semantic_summary") {
		t.Fatalf("semantic summary should be omitted on failure, got %q", view[0].Text)
	}
	if len(emitted) != 2 || emitted[1].Data["semantic_summary_status"] != "failed" {
		t.Fatalf("expected failed semantic summary event, got %#v", emitted)
	}
}

func TestCompactorCountsSystemPromptCharsForThreshold(t *testing.T) {
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-test",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	state := session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	messages := []session.Message{session.NewMessage("user", "small request")}
	profile := compactionProfileForPolicy(1024, 1, 0)
	view, inputChars, didCompact, err := newCompactor(store).build(context.Background(), meta.ID, meta.Workdir, state, messages, nil, nil, profile, 0, 2048, nil, func(events.Event) error { return nil })
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !didCompact {
		t.Fatalf("expected system prompt chars to trigger compaction, input=%d view=%#v", inputChars, view)
	}
}

func TestCompactorReportsEventEmitErrors(t *testing.T) {
	newSession := func(t *testing.T) (*session.Store, session.SessionMetadata, session.State, []session.Message) {
		t.Helper()
		store := session.NewStore(t.TempDir())
		meta := session.SessionMetadata{
			SchemaVersion:    1,
			ID:               session.NewSessionID(),
			CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
			Workdir:          t.TempDir(),
			Mode:             session.ModeRun,
			Provider:         "openai",
			Model:            "gpt-5.4",
			CompletionPolicy: session.CompletionPolicyInteractive,
		}
		state := session.State{
			Status:    session.StatusRunning,
			Phase:     "prepare",
			UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}
		if err := store.Create(meta, state); err != nil {
			t.Fatalf("create session: %v", err)
		}
		messages := []session.Message{
			session.NewMessage("user", strings.Repeat("context ", 80)),
			session.NewToolMessage([]session.ToolResult{{
				ToolCallID:    "call_1",
				Name:          "shell",
				LLMOutput:     strings.Repeat("output ", 80),
				DisplayOutput: strings.Repeat("output ", 80),
			}}),
		}
		return store, meta, state, messages
	}
	assertEventError := func(t *testing.T, err error, eventType string, sentinel error) {
		t.Helper()
		if err == nil {
			t.Fatalf("expected %s emit error", eventType)
		}
		if !strings.Contains(err.Error(), eventType) {
			t.Fatalf("expected %s context, got %v", eventType, err)
		}
		if !errors.Is(err, sentinel) {
			t.Fatalf("expected sentinel error, got %v", err)
		}
	}

	t.Run("started", func(t *testing.T) {
		store, meta, state, messages := newSession(t)
		sentinel := errors.New("append compact.started")
		view, _, didCompact, err := newCompactor(store).BuildWithPolicy(meta.ID, meta.Workdir, state, messages, nil, nil, 32, 1, 0, 0, func(events.Event) error {
			return sentinel
		})
		assertEventError(t, err, "compact.started", sentinel)
		if view != nil || didCompact {
			t.Fatalf("expected no compacted view after started event failure, got didCompact=%v view=%#v", didCompact, view)
		}
		summaryDir := filepath.Join(store.SessionDir(meta.ID), "artifacts", "compactions")
		if entries, readErr := os.ReadDir(summaryDir); readErr == nil && len(entries) > 0 {
			t.Fatalf("expected no summary artifact after started event failure, got %#v", entries)
		} else if readErr != nil && !os.IsNotExist(readErr) {
			t.Fatalf("read summary dir: %v", readErr)
		}
	})

	t.Run("finished", func(t *testing.T) {
		store, meta, state, messages := newSession(t)
		sentinel := errors.New("append compact.finished")
		emitCount := 0
		view, _, didCompact, err := newCompactor(store).BuildWithPolicy(meta.ID, meta.Workdir, state, messages, nil, nil, 32, 1, 0, 0, func(events.Event) error {
			emitCount++
			if emitCount == 2 {
				return sentinel
			}
			return nil
		})
		assertEventError(t, err, "compact.finished", sentinel)
		if view != nil || didCompact {
			t.Fatalf("expected no compacted view after finished event failure, got didCompact=%v view=%#v", didCompact, view)
		}
	})

	t.Run("reused", func(t *testing.T) {
		store, meta, state, messages := newSession(t)
		if _, err := store.WriteArtifact(meta.ID, filepath.Join("compactions", "summary-20260527-010000.json"), map[string]any{
			"current_status": "stable",
		}); err != nil {
			t.Fatalf("write reusable summary: %v", err)
		}
		sentinel := errors.New("append compact.reused")
		view, _, didCompact, err := newCompactor(store).BuildWithPolicy(meta.ID, meta.Workdir, state, messages, nil, nil, 32, 1, 100000, 100000, func(events.Event) error {
			return sentinel
		})
		assertEventError(t, err, "compact.reused", sentinel)
		if view != nil || didCompact {
			t.Fatalf("expected no compacted view after reused event failure, got didCompact=%v view=%#v", didCompact, view)
		}
	})
}

func TestFallbackCompactionDeferredViewDoesNotRetainFullHistory(t *testing.T) {
	messages := []session.Message{
		session.NewMessage("user", "old external instruction "+strings.Repeat("A", 1200)),
		session.NewAssistantMessage("old assistant narration "+strings.Repeat("B", 1200), "", nil),
		session.NewToolMessage([]session.ToolResult{{
			ToolCallID:    "call_old",
			Name:          "shell",
			LLMOutput:     "old massive output " + strings.Repeat("C", 3000),
			DisplayOutput: "old massive output " + strings.Repeat("C", 3000),
		}}),
		session.NewAssistantMessage("middle assistant", "", nil),
		session.NewMessage("user", "middle user"),
		session.NewAssistantMessage("recent assistant one", "", nil),
		session.NewMessage("user", "recent user two"),
		session.NewAssistantMessage("recent assistant three", "", nil),
		session.NewMessage("user", "recent user four"),
		session.NewMessage("user", "latest user instruction"),
	}
	view, _ := fallbackCompactionDeferredView(messages, compactionProfileForPolicy(32, 1, 0), errors.New("write summary failed"), 0)
	if len(view) == 0 || view[0].Meta["source"] != "compaction_deferred" {
		t.Fatalf("expected leading deferred message, got %#v", view)
	}
	serialized, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal view: %v", err)
	}
	text := string(serialized)
	if strings.Contains(text, strings.Repeat("A", 1000)) || strings.Contains(text, strings.Repeat("B", 1000)) || strings.Contains(text, strings.Repeat("C", 1000)) {
		t.Fatalf("fallback view should not retain full old history: %s", text)
	}
	if !strings.Contains(text, "latest user instruction") {
		t.Fatalf("fallback view should retain latest user instruction: %s", text)
	}
}

func TestCompactorReportsCorruptFeatureList(t *testing.T) {
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	state := session.State{
		Status:    session.StatusRunning,
		Phase:     "prepare",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.SessionDir(meta.ID), "feature_list.json"), []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatalf("write corrupt feature list: %v", err)
	}
	messages := []session.Message{
		session.NewMessage("user", "Continue the large feature convergence task."),
		session.NewAssistantMessage(strings.Repeat("A", 512), "", nil),
	}

	view, _, didCompact, err := newCompactor(store).BuildWithProfile(meta.ID, meta.Workdir, state, messages, nil, nil, compactionProfileForPolicy(32, 1, 0), 0, func(events.Event) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "feature_list.json") {
		t.Fatalf("expected corrupt feature_list.json error, got view=%#v didCompact=%t err=%v", view, didCompact, err)
	}
	if didCompact {
		t.Fatal("expected corrupt feature list to stop compaction")
	}
	summaryDir := filepath.Join(store.SessionDir(meta.ID), "artifacts", "compactions")
	if entries, readErr := os.ReadDir(summaryDir); readErr == nil && len(entries) != 0 {
		t.Fatalf("expected no compaction summary after corrupt feature list, got %#v", entries)
	} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		t.Fatalf("read compaction summary dir: %v", readErr)
	}
}

func TestCompactorReportsCorruptGoalSnapshot(t *testing.T) {
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	state := session.State{
		Status:    session.StatusRunning,
		Phase:     "prepare",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.SessionDir(meta.ID), "goal.json"), []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatalf("write corrupt goal: %v", err)
	}
	messages := []session.Message{
		session.NewMessage("user", "Continue the large goal-backed task."),
		session.NewAssistantMessage(strings.Repeat("A", 512), "", nil),
	}

	view, _, didCompact, err := newCompactor(store).BuildWithProfile(meta.ID, meta.Workdir, state, messages, nil, nil, compactionProfileForPolicy(32, 1, 0), 0, func(events.Event) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "goal.json") {
		t.Fatalf("expected corrupt goal.json error, got view=%#v didCompact=%t err=%v", view, didCompact, err)
	}
	if didCompact {
		t.Fatal("expected corrupt goal to stop compaction")
	}
	summaryDir := filepath.Join(store.SessionDir(meta.ID), "artifacts", "compactions")
	if entries, readErr := os.ReadDir(summaryDir); readErr == nil && len(entries) != 0 {
		t.Fatalf("expected no compaction summary after corrupt goal, got %#v", entries)
	} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		t.Fatalf("read compaction summary dir: %v", readErr)
	}
}

func TestCompactorReuseReportsCorruptGoalSnapshot(t *testing.T) {
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	state := session.State{
		Status:    session.StatusRunning,
		Phase:     "prepare",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.SessionDir(meta.ID), "goal.json"), []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatalf("write corrupt goal: %v", err)
	}
	messages := []session.Message{
		session.NewMessage("user", "Continue the large goal-backed task."),
		session.NewAssistantMessage(strings.Repeat("A", 512), "", nil),
	}

	view, _, didCompact, err := newCompactor(store).BuildWithPolicy(meta.ID, meta.Workdir, state, messages, nil, nil, 32, 1, 520, 1000, func(events.Event) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "goal.json") {
		t.Fatalf("expected corrupt goal.json reuse error, got view=%#v didCompact=%t err=%v", view, didCompact, err)
	}
	if didCompact {
		t.Fatal("expected corrupt goal to stop compaction reuse")
	}
}

func TestCompactorReusesSummaryWithinHysteresisWindow(t *testing.T) {
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	state := session.State{
		Status:    session.StatusRunning,
		Phase:     "prepare",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	stableSummary := map[string]any{
		"current_status": []string{"stable cached summary"},
		"transcript":     "stable transcript",
	}
	if _, err := store.WriteArtifact(meta.ID, filepath.Join("compactions", "summary-20260521-010000.json"), stableSummary); err != nil {
		t.Fatalf("write prior summary: %v", err)
	}
	messages := []session.Message{
		session.NewMessage("user", "Continue the large audit."),
		session.NewAssistantMessage(strings.Repeat("A", 512), "", nil),
	}

	var emitted []events.Event
	view, inputChars, didCompact, err := newCompactor(store).BuildWithPolicy(meta.ID, meta.Workdir, state, messages, nil, nil, 32, 1, 520, 1000, func(evt events.Event) error {
		emitted = append(emitted, evt)
		return nil
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if didCompact {
		t.Fatalf("expected hysteresis reuse, got didCompact=true input=%d", inputChars)
	}
	if len(view) == 0 || !strings.Contains(view[0].Text, "[Conversation compacted]") {
		t.Fatalf("expected compacted provider view, got %#v", view)
	}
	if !strings.Contains(view[0].Text, "stable cached summary") {
		t.Fatalf("expected hysteresis reuse to load previous summary artifact, got %q", view[0].Text)
	}
	if strings.Contains(view[0].Text, "Continue the large audit") {
		t.Fatalf("expected reused summary prefix to stay stable, got %q", view[0].Text)
	}
	if len(view) < 2 || !strings.Contains(view[len(view)-1].Text, "A") {
		t.Fatalf("expected hysteresis reuse to carry recent tail messages, got %#v", view)
	}
	if len(emitted) != 1 || emitted[0].Type != "compact.reused" {
		t.Fatalf("expected compact.reused event only, got %#v", emitted)
	}
	if emitted[0].Data["last_compaction_input_chars"] != 520 {
		t.Fatalf("expected watermark in compact.reused event, got %#v", emitted[0].Data)
	}
	if emitted[0].Data["summary_source"] != filepath.Join("compactions", "summary-20260521-010000.json") {
		t.Fatalf("expected summary source in compact.reused event, got %#v", emitted[0].Data)
	}
	files, err := os.ReadDir(filepath.Join(store.SessionDir(meta.ID), "artifacts", "compactions"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read compactions dir: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected no new compaction artifacts, got %#v", files)
	}
}

func TestCompactorReportsCorruptReusableSummaryArtifact(t *testing.T) {
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	state := session.State{
		Status:    session.StatusRunning,
		Phase:     "prepare",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	relativePath := filepath.Join("compactions", "summary-20260521-010000.json")
	artifactPath := filepath.Join(store.SessionDir(meta.ID), "artifacts", relativePath)
	if err := os.WriteFile(artifactPath, []byte(`{"current_status":`), 0o600); err != nil {
		t.Fatalf("write corrupt reusable summary: %v", err)
	}
	messages := []session.Message{
		session.NewMessage("user", "Continue the large audit."),
		session.NewAssistantMessage(strings.Repeat("A", 512), "", nil),
	}

	var emitted []events.Event
	view, _, didCompact, err := newCompactor(store).BuildWithPolicy(meta.ID, meta.Workdir, state, messages, nil, nil, 32, 1, 520, 1000, func(evt events.Event) error {
		emitted = append(emitted, evt)
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "read compaction summary artifact") || !strings.Contains(err.Error(), relativePath) {
		t.Fatalf("expected corrupt reusable summary error for %s, got view=%#v didCompact=%t err=%v", relativePath, view, didCompact, err)
	}
	if view != nil || didCompact {
		t.Fatalf("expected corrupt reusable summary to stop reuse, got didCompact=%v view=%#v", didCompact, view)
	}
	if len(emitted) != 0 {
		t.Fatalf("expected no compact.reused event after corrupt reusable summary, got %#v", emitted)
	}
}

func TestCompactorReportsUnreadableCompactionArtifactDirectory(t *testing.T) {
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	state := session.State{
		Status:    session.StatusRunning,
		Phase:     "prepare",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	dir := filepath.Join(store.SessionDir(meta.ID), "artifacts", "compactions")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove compactions dir: %v", err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing-compactions"), dir); err != nil {
		t.Fatalf("create broken compactions symlink: %v", err)
	}
	messages := []session.Message{
		session.NewMessage("user", "Continue the large audit."),
		session.NewAssistantMessage(strings.Repeat("A", 512), "", nil),
	}

	var emitted []events.Event
	view, _, didCompact, err := newCompactor(store).BuildWithPolicy(meta.ID, meta.Workdir, state, messages, nil, nil, 32, 1, 520, 1000, func(evt events.Event) error {
		emitted = append(emitted, evt)
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "list compaction summary artifacts") || !strings.Contains(err.Error(), "artifacts") {
		t.Fatalf("expected unreadable compaction directory error, got view=%#v didCompact=%t err=%v", view, didCompact, err)
	}
	if view != nil || didCompact {
		t.Fatalf("expected unreadable compaction directory to stop reuse, got didCompact=%v view=%#v", didCompact, view)
	}
	if len(emitted) != 0 {
		t.Fatalf("expected no compact.reused event after unreadable compaction directory, got %#v", emitted)
	}
}

func TestCompactionAddsReferencePrefix(t *testing.T) {
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-test",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	state := session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	messages := []session.Message{
		session.NewMessage("user", "Keep going."),
		session.NewAssistantMessage(strings.Repeat("A", 512), "", nil),
	}
	profile := compactionContextProfile{
		Provider:              "openai",
		Model:                 "gpt-test",
		Source:                "test-profile",
		InputCharThreshold:    32,
		KeepRecentToolResults: 1,
		HysteresisDeltaChars:  100,
	}
	view, _, didCompact, err := newCompactor(store).BuildWithProfile(meta.ID, meta.Workdir, state, messages, nil, nil, profile, 0, func(events.Event) error { return nil })
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !didCompact {
		t.Fatal("expected compaction")
	}
	if !strings.Contains(view[0].Text, "Another model produced this compacted summary") ||
		!strings.Contains(view[0].Text, "Do not restart from scratch") ||
		!strings.Contains(view[0].Text, "newest external instruction") ||
		!strings.Contains(view[0].Text, "not a new user instruction") ||
		!strings.Contains(view[0].Text, "source of truth") {
		t.Fatalf("expected reference prefix, got %q", view[0].Text)
	}
	if !strings.Contains(view[0].Text, `"provider": "openai"`) || !strings.Contains(view[0].Text, `"model": "gpt-test"`) {
		t.Fatalf("expected provider/model context profile in compacted summary, got %q", view[0].Text)
	}
}

func TestCompactionProfileFromConfigUsesProviderModelOverride(t *testing.T) {
	meta := session.SessionMetadata{Provider: "OpenAI", Model: "GPT-Test"}
	profile := compactionProfileFromConfig(meta, config.CompactConfig{
		InputCharThreshold:    160000,
		KeepRecentToolResults: 3,
		HysteresisDeltaChars:  40000,
		ContextProfiles: map[string]config.CompactContextProfile{
			"openai/gpt-test": {
				InputCharThreshold:    2048,
				KeepRecentToolResults: 5,
				HysteresisDeltaChars:  256,
			},
		},
	})
	if profile.InputCharThreshold != 2048 || profile.KeepRecentToolResults != 5 || profile.HysteresisDeltaChars != 256 {
		t.Fatalf("expected provider/model override, got %#v", profile)
	}
	if profile.Source != "runtime.compact.context_profiles.openai/gpt-test" {
		t.Fatalf("unexpected profile source: %#v", profile)
	}
	if profile.ThresholdSource != "context_profiles.openai/gpt-test" {
		t.Fatalf("unexpected threshold source: %#v", profile)
	}
}

func TestCompactionProfileFromConfigDerivesFromContextWindow(t *testing.T) {
	meta := session.SessionMetadata{
		Provider: "openai",
		Model:    "gpt-5.5",
	}
	profile := compactionProfileFromConfig(meta, config.CompactConfig{
		KeepRecentToolResults: 3,
		UtilizationFactor:     0.85,
	})
	if profile.ContextWindowTokens != 300000 {
		t.Fatalf("expected gpt-5.5 known context window, got %#v", profile)
	}
	if profile.InputCharThreshold != 1020000 {
		t.Fatalf("expected derived threshold 1020000, got %#v", profile)
	}
	if profile.HysteresisDeltaChars != 255000 {
		t.Fatalf("expected derived hysteresis, got %#v", profile)
	}
	if profile.KeepRecentMessages <= 6 {
		t.Fatalf("expected proportional recent message window, got %#v", profile)
	}
	if profile.ThresholdSource != "context_window" {
		t.Fatalf("expected context_window threshold source, got %#v", profile)
	}
}

func TestCompactionProfileFromConfigExplicitThresholdWins(t *testing.T) {
	meta := session.SessionMetadata{
		Provider: "openai",
		Model:    "gpt-5.5",
		ProviderOptions: session.ProviderOptions{
			ContextWindowTokens: 272000,
		},
	}
	profile := compactionProfileFromConfig(meta, config.CompactConfig{
		InputCharThreshold:    12345,
		KeepRecentToolResults: 3,
		UtilizationFactor:     0.85,
	})
	if profile.InputCharThreshold != 12345 {
		t.Fatalf("expected explicit threshold to win, got %#v", profile)
	}
	if profile.ContextWindowTokens != 272000 {
		t.Fatalf("expected provider context window to be recorded, got %#v", profile)
	}
	if profile.ThresholdSource != "explicit" {
		t.Fatalf("expected explicit threshold source, got %#v", profile)
	}
}

func TestCompactionDoesNotRedactSecretLikeText(t *testing.T) {
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-test",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	state := session.State{
		Status:    session.StatusRunning,
		Phase:     "prepare",
		LastError: "TOKEN=tok_secret_123456789",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	apiKey := "sk-testsecret123456789"
	bearer := "abcdefghijklmnopqrstuvwxyz123456"
	privateKey := "-----BEGIN PRIVATE KEY-----\nabcdef1234567890\n-----END PRIVATE KEY-----"
	messages := []session.Message{
		session.NewMessage("user", "OPENAI_API_KEY="+apiKey),
		session.NewToolMessage([]session.ToolResult{{
			Name:          "shell",
			LLMOutput:     "Authorization: Bearer " + bearer + "\n" + privateKey,
			DisplayOutput: "Authorization: Bearer " + bearer + "\n" + privateKey,
			IsError:       true,
			Metadata: map[string]any{
				"token": "tok_metadata_123456789",
			},
		}}),
		session.NewAssistantMessage(strings.Repeat("A", 512), "", nil),
	}
	view, _, didCompact, err := newCompactor(store).BuildWithPolicy(meta.ID, meta.Workdir, state, messages, nil, nil, 32, 1, 0, 0, func(events.Event) error { return nil })
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !didCompact {
		t.Fatal("expected compaction")
	}
	for _, expected := range []string{apiKey, "tok_secret_123456789"} {
		if !strings.Contains(view[0].Text, expected) {
			t.Fatalf("expected secret-like text to remain in compacted provider view: %q", expected)
		}
	}
	if strings.Contains(view[0].Text, "[REDACTED]") {
		t.Fatalf("did not expect redaction markers in compacted view, got %q", view[0].Text)
	}
	summaryFiles, err := os.ReadDir(filepath.Join(store.SessionDir(meta.ID), "artifacts", "compactions"))
	if err != nil {
		t.Fatalf("read compactions dir: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(store.SessionDir(meta.ID), "artifacts", "compactions", summaryFiles[0].Name()))
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	for _, expected := range []string{apiKey, "tok_secret_123456789"} {
		if !strings.Contains(string(data), expected) {
			t.Fatalf("expected secret-like text to remain in summary artifact: %q", expected)
		}
	}
	if strings.Contains(string(data), "[REDACTED]") {
		t.Fatalf("did not expect redaction markers in summary artifact: %q", string(data))
	}

	transcriptFiles, err := os.ReadDir(filepath.Join(store.SessionDir(meta.ID), "artifacts", "transcripts"))
	if err != nil {
		t.Fatalf("read transcripts dir: %v", err)
	}
	transcriptData, err := os.ReadFile(filepath.Join(store.SessionDir(meta.ID), "artifacts", "transcripts", transcriptFiles[0].Name()))
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	for _, expected := range []string{apiKey, bearer, "BEGIN PRIVATE KEY", "abcdef1234567890", "END PRIVATE KEY", "tok_metadata_123456789"} {
		if !strings.Contains(string(transcriptData), expected) {
			t.Fatalf("expected secret-like text to remain in transcript artifact: %q", expected)
		}
	}
	if strings.Contains(string(transcriptData), "[REDACTED]") {
		t.Fatalf("did not expect redaction markers in transcript artifact: %q", string(transcriptData))
	}
}

func TestCompactionTruncatesOldToolOutput(t *testing.T) {
	store := session.NewStore(t.TempDir())
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-test",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	state := session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	oldArgs := `{"command":"` + strings.Repeat("A", 1800) + `MIDDLE` + strings.Repeat("Z", 1800) + `"}`
	oldOutput := "HEAD-" + strings.Repeat("B", 1800) + "MIDDLE" + strings.Repeat("Y", 1800) + "-TAIL"
	messages := []session.Message{
		session.NewMessage("user", "Run tools."),
		session.NewAssistantMessage("", "", []session.ToolCall{{
			ID:        "old_call",
			Name:      "shell",
			Arguments: json.RawMessage(oldArgs),
		}}),
		session.NewToolMessage([]session.ToolResult{{
			ToolCallID:    "old_call",
			Name:          "shell",
			LLMOutput:     oldOutput,
			DisplayOutput: oldOutput,
		}}),
		session.NewAssistantMessage("", "", []session.ToolCall{{
			ID:        "new_call",
			Name:      "shell",
			Arguments: json.RawMessage(`{"command":"pwd"}`),
		}}),
		session.NewToolMessage([]session.ToolResult{{
			ToolCallID:    "new_call",
			Name:          "shell",
			LLMOutput:     "new output",
			DisplayOutput: "new output",
		}}),
	}
	view, _, didCompact, err := newCompactor(store).BuildWithPolicy(meta.ID, meta.Workdir, state, messages, nil, nil, 1000000, 1, 0, 0, func(events.Event) error { return nil })
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if didCompact {
		t.Fatal("expected micro compaction only")
	}
	gotArgs := string(view[1].ToolCalls[0].Arguments)
	if strings.Contains(gotArgs, "compacted_for_context") || strings.Contains(gotArgs, "original_chars") || strings.Contains(gotArgs, "head_tail") {
		t.Fatalf("compacted old tool arguments should keep schema shape without reserved marker fields, got %s", gotArgs)
	}
	if !strings.Contains(gotArgs, `"command"`) || !strings.Contains(gotArgs, "suffix:") {
		t.Fatalf("expected compacted old tool arguments, got %s", string(view[1].ToolCalls[0].Arguments))
	}
	if strings.Contains(gotArgs, "MIDDLE") {
		t.Fatalf("expected middle of old tool arguments to be omitted, got %s", gotArgs)
	}
	got := view[2].ToolResults[0].LLMOutput
	if !strings.Contains(got, "[Compacted previous_tool_result") || !strings.Contains(got, "HEAD:") || !strings.Contains(got, "TAIL:") {
		t.Fatalf("expected head/tail compacted old tool output, got %q", got)
	}
	if strings.Contains(got, "MIDDLE") {
		t.Fatalf("expected middle of old output to be omitted, got %q", got)
	}
	if view[4].ToolResults[0].LLMOutput != "new output" {
		t.Fatalf("expected recent tool output preserved, got %q", view[4].ToolResults[0].LLMOutput)
	}
}

func TestCompactionTruncatesProviderBlockToolArguments(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		callID     string
		makeBlock  func(callID string, raw json.RawMessage) session.ProviderContentBlock
		blockValue func(block session.ProviderContentBlock) json.RawMessage
	}{
		{
			name:     "anthropic tool_use input",
			provider: "anthropic",
			callID:   "toolu_old",
			makeBlock: func(callID string, raw json.RawMessage) session.ProviderContentBlock {
				return session.ProviderContentBlock{
					Provider: "anthropic",
					Type:     "tool_use",
					ID:       callID,
					Name:     "shell",
					Input:    raw,
				}
			},
			blockValue: func(block session.ProviderContentBlock) json.RawMessage { return block.Input },
		},
		{
			name:     "google function_call args",
			provider: "google",
			callID:   "gcall_old",
			makeBlock: func(callID string, raw json.RawMessage) session.ProviderContentBlock {
				return session.ProviderContentBlock{
					Provider: "google",
					Type:     "function_call",
					ID:       callID,
					Name:     "shell",
					Args:     raw,
				}
			},
			blockValue: func(block session.ProviderContentBlock) json.RawMessage { return block.Args },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := session.NewStore(t.TempDir())
			meta := session.SessionMetadata{
				SchemaVersion:    1,
				ID:               session.NewSessionID(),
				CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
				Workdir:          t.TempDir(),
				Mode:             session.ModeRun,
				Provider:         tc.provider,
				Model:            "test-model",
				CompletionPolicy: session.CompletionPolicyInteractive,
			}
			state := session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
			if err := store.Create(meta, state); err != nil {
				t.Fatalf("create session: %v", err)
			}
			oldArgs := json.RawMessage(`{"command":"` + strings.Repeat("A", 1800) + `MIDDLE` + strings.Repeat("Z", 1800) + `"}`)
			assistant := session.NewAssistantMessage("", "provider thinking", nil)
			assistant.ProviderContentBlocks = []session.ProviderContentBlock{tc.makeBlock(tc.callID, oldArgs)}
			messages := []session.Message{
				session.NewMessage("user", "Run tools."),
				assistant,
				session.NewToolMessage([]session.ToolResult{{
					ToolCallID:    tc.callID,
					Name:          "shell",
					LLMOutput:     "old output",
					DisplayOutput: "old output",
				}}),
				session.NewAssistantMessage("", "", []session.ToolCall{{
					ID:        "new_call",
					Name:      "shell",
					Arguments: json.RawMessage(`{"command":"pwd"}`),
				}}),
				session.NewToolMessage([]session.ToolResult{{
					ToolCallID:    "new_call",
					Name:          "shell",
					LLMOutput:     "new output",
					DisplayOutput: "new output",
				}}),
			}

			view, _, didCompact, err := newCompactor(store).BuildWithPolicy(meta.ID, meta.Workdir, state, messages, nil, nil, 1000000, 1, 0, 0, func(events.Event) error { return nil })
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if didCompact {
				t.Fatal("expected micro compaction only")
			}
			if len(view) < 2 || len(view[1].ProviderContentBlocks) != 1 {
				t.Fatalf("expected retained provider block assistant, got %#v", view)
			}
			got := string(tc.blockValue(view[1].ProviderContentBlocks[0]))
			if strings.Contains(got, "compacted_for_context") || strings.Contains(got, "original_chars") || strings.Contains(got, "head_tail") {
				t.Fatalf("compacted old provider block arguments should keep schema shape without reserved marker fields, got %s", got)
			}
			if !strings.Contains(got, `"command"`) || !strings.Contains(got, "suffix:") {
				t.Fatalf("expected compacted old provider block arguments, got %s", got)
			}
			if strings.Contains(got, "MIDDLE") {
				t.Fatalf("expected middle of old provider block arguments to be omitted, got %s", got)
			}
			if string(tc.blockValue(messages[1].ProviderContentBlocks[0])) != string(oldArgs) {
				t.Fatalf("expected source provider block arguments to remain unchanged, got %s", string(tc.blockValue(messages[1].ProviderContentBlocks[0])))
			}
		})
	}
}

func TestCompactorDoesNotMutateSourceToolResultMetadata(t *testing.T) {
	store := session.NewStore(t.TempDir())
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-test",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	state := session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	metadata := map[string]any{"path": "reports/large.log"}
	messages := []session.Message{
		session.NewMessage("user", "Run tools."),
		session.NewAssistantMessage("", "", []session.ToolCall{{
			ID:        "old_call",
			Name:      "shell",
			Arguments: json.RawMessage(`{"command":"pwd"}`),
		}}),
		session.NewToolMessage([]session.ToolResult{{
			ToolCallID:    "old_call",
			Name:          "shell",
			LLMOutput:     strings.Repeat("O", 2000),
			DisplayOutput: strings.Repeat("O", 2000),
			Metadata:      metadata,
		}}),
		session.NewAssistantMessage("", "", []session.ToolCall{{
			ID:        "new_call",
			Name:      "shell",
			Arguments: json.RawMessage(`{"command":"pwd"}`),
		}}),
		session.NewToolMessage([]session.ToolResult{{
			ToolCallID:    "new_call",
			Name:          "shell",
			LLMOutput:     "new output",
			DisplayOutput: "new output",
		}}),
	}

	view, _, _, err := newCompactor(store).BuildWithPolicy(meta.ID, meta.Workdir, state, messages, nil, nil, 1000000, 1, 0, 0, func(events.Event) error { return nil })
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := view[2].ToolResults[0].Metadata["compacted_for_context"]; got != true {
		t.Fatalf("expected compacted view metadata marker, got %#v", view[2].ToolResults[0].Metadata)
	}
	if _, ok := metadata["compacted_for_context"]; ok {
		t.Fatalf("source metadata map was mutated: %#v", metadata)
	}
	if _, ok := messages[2].ToolResults[0].Metadata["compaction_reason"]; ok {
		t.Fatalf("source message metadata was mutated: %#v", messages[2].ToolResults[0].Metadata)
	}
}

func TestCompactionKeepsArtifactProofMemory(t *testing.T) {
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-test",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	state := session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	reportPath := filepath.Join(workdir, "reports", "validation.md")
	codePath := filepath.Join(workdir, "internal", "runtime", "engine.go")
	messages := []session.Message{
		session.NewMessage("user", "Continue."),
		session.NewToolMessage([]session.ToolResult{{
			Name:          "read_file",
			LLMOutput:     "## Validation\nall checks passed",
			DisplayOutput: "## Validation\nall checks passed",
			Metadata:      map[string]any{"path": reportPath},
		}}),
		session.NewToolMessage([]session.ToolResult{{
			Name:          "read_file",
			LLMOutput:     "func run() { return nil }",
			DisplayOutput: "func run() { return nil }",
			Metadata:      map[string]any{"path": codePath, "offset": 12, "end": 20},
		}}),
		session.NewAssistantMessage(strings.Repeat("A", 512), "", nil),
	}
	if _, _, didCompact, err := newCompactor(store).BuildWithPolicy(meta.ID, meta.Workdir, state, messages, nil, nil, 32, 1, 0, 0, func(events.Event) error { return nil }); err != nil {
		t.Fatalf("build: %v", err)
	} else if !didCompact {
		t.Fatal("expected compaction")
	}
	summaryFiles, err := os.ReadDir(filepath.Join(store.SessionDir(meta.ID), "artifacts", "compactions"))
	if err != nil {
		t.Fatalf("read compactions dir: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(store.SessionDir(meta.ID), "artifacts", "compactions", summaryFiles[0].Name()))
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	var summary map[string]any
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}
	if len(artifactMemory(summary)) == 0 {
		t.Fatalf("expected artifact memory, got %#v", summary["artifact_memory"])
	}
	if len(highValueProofs(summary)) == 0 {
		t.Fatalf("expected high-value proofs, got %#v", summary["high_value_proofs"])
	}
}

func artifactMemory(summary map[string]any) []any {
	items, _ := summary["artifact_memory"].([]any)
	return items
}

func highValueProofs(summary map[string]any) []any {
	items, _ := summary["high_value_proofs"].([]any)
	return items
}

func TestCompactorPreservesAssistantToolCallsForRetainedToolResults(t *testing.T) {
	store := session.NewStore(t.TempDir())
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	state := session.State{
		Status:    session.StatusRunning,
		Phase:     "prepare",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}

	messages := []session.Message{
		session.NewMessage("user", "Initial request"),
		session.NewAssistantMessage("", "", []session.ToolCall{{
			ID:             "call_old",
			Name:           "read_file",
			Arguments:      json.RawMessage(`{"path":"old.md"}`),
			ProviderCallID: "call_old",
		}}),
		session.NewToolMessage([]session.ToolResult{{
			ToolCallID:    "call_old",
			Name:          "read_file",
			LLMOutput:     "old file content",
			DisplayOutput: "old file content",
		}}),
		session.NewAssistantMessage("", "", []session.ToolCall{{
			ID:             "call_new",
			Name:           "read_file",
			Arguments:      json.RawMessage(`{"path":"new.md"}`),
			ProviderCallID: "call_new",
		}}),
		session.NewToolMessage([]session.ToolResult{{
			ToolCallID:    "call_new",
			Name:          "read_file",
			LLMOutput:     "new file content",
			DisplayOutput: "new file content",
		}}),
		session.NewAssistantMessage("Observed enough context.", "", nil),
		session.NewMessage("user", "Interrupt and keep the answer short."),
		session.NewAssistantMessage("Will do.", "", nil),
	}

	view, err := newCompactor(store).Build(meta.ID, meta.Workdir, state, messages, nil, nil, 1, 10, func(events.Event) error { return nil })
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(view) < 8 {
		t.Fatalf("expected compacted summary plus preserved dependency context, got %d messages", len(view))
	}

	var assistantIndex, toolIndex = -1, -1
	for i, msg := range view {
		if len(msg.ToolCalls) > 0 && msg.ToolCalls[0].ID == "call_old" {
			assistantIndex = i
		}
		if len(msg.ToolResults) > 0 && msg.ToolResults[0].ToolCallID == "call_old" {
			toolIndex = i
		}
	}
	if assistantIndex == -1 {
		t.Fatalf("expected retained assistant tool_call for call_old, got %#v", view)
	}
	if toolIndex == -1 {
		t.Fatalf("expected retained tool result for call_old, got %#v", view)
	}
	if assistantIndex >= toolIndex {
		t.Fatalf("expected assistant tool_call before tool result, got assistant=%d tool=%d", assistantIndex, toolIndex)
	}
}

func TestCompactorPreservesProviderBlockToolCallsForRetainedToolResults(t *testing.T) {
	tests := []struct {
		name       string
		callID     string
		provider   string
		blockType  string
		makeBlocks func(callID string) []session.ProviderContentBlock
	}{
		{
			name:      "anthropic tool_use block",
			callID:    "toolu_old",
			provider:  "anthropic",
			blockType: "tool_use",
			makeBlocks: func(callID string) []session.ProviderContentBlock {
				return []session.ProviderContentBlock{{
					Provider: "anthropic",
					Type:     "tool_use",
					ID:       callID,
					Name:     "shell",
					Input:    json.RawMessage(`{"command":"pwd"}`),
				}}
			},
		},
		{
			name:      "google function_call block",
			callID:    "gcall_old",
			provider:  "google",
			blockType: "function_call",
			makeBlocks: func(callID string) []session.ProviderContentBlock {
				return []session.ProviderContentBlock{{
					Provider: "google",
					Type:     "function_call",
					ID:       callID,
					Name:     "shell",
					Args:     json.RawMessage(`{"command":"pwd"}`),
				}}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := session.NewStore(t.TempDir())
			meta := session.SessionMetadata{
				SchemaVersion:    1,
				ID:               session.NewSessionID(),
				CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
				Workdir:          t.TempDir(),
				Mode:             session.ModeRun,
				Provider:         tc.provider,
				Model:            "test-model",
				CompletionPolicy: session.CompletionPolicyInteractive,
			}
			state := session.State{
				Status:    session.StatusRunning,
				Phase:     "prepare",
				UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
			}
			if err := store.Create(meta, state); err != nil {
				t.Fatalf("create session: %v", err)
			}

			assistant := session.NewAssistantMessage("", "provider thinking", nil)
			assistant.ProviderContentBlocks = tc.makeBlocks(tc.callID)
			messages := []session.Message{
				session.NewMessage("user", "Initial request"),
				assistant,
				session.NewToolMessage([]session.ToolResult{{
					ToolCallID:    tc.callID,
					Name:          "shell",
					LLMOutput:     "old output",
					DisplayOutput: "old output",
				}}),
				session.NewAssistantMessage("filler one", "", nil),
				session.NewAssistantMessage("filler two", "", nil),
				session.NewAssistantMessage("filler three", "", nil),
				session.NewMessage("user", "Latest external instruction."),
				session.NewAssistantMessage("Recent answer.", "", nil),
			}

			view, err := newCompactor(store).Build(meta.ID, meta.Workdir, state, messages, nil, nil, 1, 10, func(events.Event) error { return nil })
			if err != nil {
				t.Fatalf("build: %v", err)
			}

			var assistantIndex, toolIndex = -1, -1
			for i, msg := range view {
				for _, block := range msg.ProviderContentBlocks {
					if block.Provider == tc.provider && block.Type == tc.blockType && block.ID == tc.callID {
						assistantIndex = i
					}
				}
				for _, result := range msg.ToolResults {
					if result.ToolCallID == tc.callID {
						toolIndex = i
					}
				}
			}
			if assistantIndex == -1 {
				t.Fatalf("expected retained provider block assistant for %s, got %#v", tc.callID, view)
			}
			if toolIndex == -1 {
				t.Fatalf("expected retained tool result for %s, got %#v", tc.callID, view)
			}
			if assistantIndex >= toolIndex {
				t.Fatalf("expected provider block assistant before tool result, got assistant=%d tool=%d", assistantIndex, toolIndex)
			}
		})
	}
}

func TestDeduplicateToolResults(t *testing.T) {
	messages := []session.Message{
		session.NewMessage("user", "Read the file"),
		session.NewAssistantMessage("", "", []session.ToolCall{{
			ID:        "call_1",
			Name:      "read_file",
			Arguments: json.RawMessage(`{"file_path":"/test/file.go"}`),
		}}),
		session.NewToolMessage([]session.ToolResult{{
			ToolCallID:    "call_1",
			Name:          "read_file",
			LLMOutput:     "first read content",
			DisplayOutput: "first read content",
		}}),
		session.NewAssistantMessage("", "", []session.ToolCall{{
			ID:        "call_2",
			Name:      "read_file",
			Arguments: json.RawMessage(`{"file_path":"/test/file.go"}`),
		}}),
		session.NewToolMessage([]session.ToolResult{{
			ToolCallID:    "call_2",
			Name:          "read_file",
			LLMOutput:     "second read content",
			DisplayOutput: "second read content",
		}}),
	}

	result := deduplicateToolResults(messages)
	if len(result) != len(messages) {
		t.Fatalf("expected same message count, got %d", len(result))
	}

	if !strings.Contains(result[2].ToolResults[0].LLMOutput, "Duplicate") {
		t.Fatalf("expected first duplicate to be marked, got %s", result[2].ToolResults[0].LLMOutput)
	}
	if result[4].ToolResults[0].LLMOutput != "second read content" {
		t.Fatalf("expected latest result to be preserved, got %s", result[4].ToolResults[0].LLMOutput)
	}
}

func TestProofPriority(t *testing.T) {
	tests := []struct {
		excerpt  string
		priority int
	}{
		{"error: file not found", 3},
		{"exception occurred", 3},
		{"warning: deprecated function", 2},
		{"test failed", 2},
		{"normal code content", 1},
	}

	for _, tt := range tests {
		got := proofPriority(tt.excerpt)
		if got != tt.priority {
			t.Errorf("proofPriority(%q) = %d, want %d", tt.excerpt, got, tt.priority)
		}
	}
}

func TestShouldCompressToolResult(t *testing.T) {
	tests := []struct {
		name     string
		result   session.ToolResult
		expected bool
	}{
		{
			name: "ephemeral artifact present",
			result: session.ToolResult{
				Metadata: map[string]any{
					"ephemeral_artifact": "/path/to/artifact",
				},
			},
			expected: true,
		},
		{
			name: "no ephemeral artifact",
			result: session.ToolResult{
				Metadata: map[string]any{},
			},
			expected: false,
		},
		{
			name: "empty ephemeral artifact",
			result: session.ToolResult{
				Metadata: map[string]any{
					"ephemeral_artifact": "",
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldCompressToolResult(tt.result)
			if got != tt.expected {
				t.Errorf("shouldCompressToolResult() = %v, want %v", got, tt.expected)
			}
		})
	}
}
