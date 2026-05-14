package runtime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/events"
	"go-cli-agent/internal/session"
)

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
	}

	var emitted []events.Event
	compactor := newCompactor(store)
	view, err := compactor.Build(meta.ID, meta.Workdir, state, messages, todo, tasks, 32, 1, func(evt events.Event) {
		emitted = append(emitted, evt)
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
		if msg.Role == "user" && msg.Text == "Continue the implementation." {
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
	if summary["current_in_progress_task"] == nil {
		t.Fatalf("expected current_in_progress_task, got %#v", summary)
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
	if budget["reserved_final_targeted_reads"] != float64(2) {
		t.Fatalf("expected reserved proof-read budget, got %#v", summary["proof_read_budget"])
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
	messages := []session.Message{
		session.NewMessage("user", "Continue the large audit."),
		session.NewAssistantMessage(strings.Repeat("A", 512), "", nil),
	}

	var emitted []events.Event
	view, inputChars, didCompact, err := newCompactor(store).BuildWithPolicy(meta.ID, meta.Workdir, state, messages, nil, nil, 32, 1, 520, 1000, func(evt events.Event) {
		emitted = append(emitted, evt)
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
	if len(emitted) != 1 || emitted[0].Type != "compact.reused" {
		t.Fatalf("expected compact.reused event only, got %#v", emitted)
	}
	if emitted[0].Data["last_compaction_input_chars"] != 520 {
		t.Fatalf("expected watermark in compact.reused event, got %#v", emitted[0].Data)
	}
	files, err := os.ReadDir(filepath.Join(store.SessionDir(meta.ID), "artifacts", "compactions"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read compactions dir: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected no new compaction artifacts, got %#v", files)
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
	view, _, didCompact, err := newCompactor(store).BuildWithProfile(meta.ID, meta.Workdir, state, messages, nil, nil, profile, 0, func(events.Event) {})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !didCompact {
		t.Fatal("expected compaction")
	}
	if !strings.Contains(view[0].Text, "not a new user instruction") || !strings.Contains(view[0].Text, "source of truth") {
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
	view, _, didCompact, err := newCompactor(store).BuildWithPolicy(meta.ID, meta.Workdir, state, messages, nil, nil, 32, 1, 0, 0, func(events.Event) {})
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
	view, _, didCompact, err := newCompactor(store).BuildWithPolicy(meta.ID, meta.Workdir, state, messages, nil, nil, 1000000, 1, 0, 0, func(events.Event) {})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if didCompact {
		t.Fatal("expected micro compaction only")
	}
	if !strings.Contains(string(view[1].ToolCalls[0].Arguments), "compacted_for_context") || !strings.Contains(string(view[1].ToolCalls[0].Arguments), "TAIL") {
		t.Fatalf("expected compacted old tool arguments, got %s", string(view[1].ToolCalls[0].Arguments))
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
	if _, _, didCompact, err := newCompactor(store).BuildWithPolicy(meta.ID, meta.Workdir, state, messages, nil, nil, 32, 1, 0, 0, func(events.Event) {}); err != nil {
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

	view, err := newCompactor(store).Build(meta.ID, meta.Workdir, state, messages, nil, nil, 1, 10, func(events.Event) {})
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

			view, err := newCompactor(store).Build(meta.ID, meta.Workdir, state, messages, nil, nil, 1, 10, func(events.Event) {})
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
