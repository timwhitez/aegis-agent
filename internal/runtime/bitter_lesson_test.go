package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"aegis-agent/internal/config"
	"aegis-agent/internal/events"
	"aegis-agent/internal/provider"
	"aegis-agent/internal/session"
)

func TestCompactorSkipsMicroCompactionBelowPressureThreshold(t *testing.T) {
	store := session.NewStore(t.TempDir())
	meta := session.SessionMetadata{ID: session.NewSessionID(), Workdir: t.TempDir()}
	state := session.State{}
	var messages []session.Message
	messages = append(messages, session.NewMessage("user", "Fix bugs across 6 files."))
	for i := 1; i <= 6; i++ {
		body := fmt.Sprintf("package p%d\n// small source file %d\n%s", i, i, strings.Repeat("func f() {}\n", 20))
		messages = append(messages, session.NewToolMessage([]session.ToolResult{{
			ToolCallID:    fmt.Sprintf("call_%d", i),
			Name:          "read_file",
			LLMOutput:     body,
			DisplayOutput: body,
		}}))
	}
	// 1M-class threshold: total conversation is <1% of threshold, so micro-compaction
	// must NOT mutate historical messages (preserving working memory & KV cache).
	profile := compactionContextProfile{
		InputCharThreshold:        3_000_000,
		KeepRecentToolResults:     3,
		KeepRecentToolResultBytes: 64 * 1024,
	}
	view, _, didCompact, err := newCompactor(store).BuildWithProfile(meta.ID, meta.Workdir, state, messages, nil, nil, profile, 0, nil)
	if err != nil {
		t.Fatalf("build view: %v", err)
	}
	if didCompact {
		t.Fatal("unexpected full compaction below pressure threshold")
	}
	states := toolResultCompactionStates(view)
	for i := 1; i <= 6; i++ {
		id := fmt.Sprintf("call_%d", i)
		if states[id] {
			t.Fatalf("expected %s to remain uncompacted below 65%% threshold, states=%#v", id, states)
		}
	}
}

func TestEngineSkipsEphemeralPointerizationBelowPressureThreshold(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.Compact.InputCharThreshold = 3_000_000
	engine, meta, state, registry, hookManager, catalog := newTestEngineWithConfig(t, cfg, session.ModeRun)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "Run several shell checks and finish.")); err != nil {
		t.Fatalf("append user: %v", err)
	}
	var turnIdx int
	fake := provider.NewFake(
		func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
			turnIdx++
			return provider.TurnResult{
				ToolCalls:  []provider.ToolCall{{ID: "sh_1", Name: "shell", Arguments: json.RawMessage(`{"command":"printf 'line1\\nline2\\nline3\\nline4\\nline5\\n'"}`)}},
				StopReason: "tool_use",
			}, nil
		},
		func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
			turnIdx++
			return provider.TurnResult{
				ToolCalls:  []provider.ToolCall{{ID: "sh_2", Name: "shell", Arguments: json.RawMessage(`{"command":"printf 'a1\\na2\\na3\\na4\\na5\\n'"}`)}},
				StopReason: "tool_use",
			}, nil
		},
		func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
			turnIdx++
			return provider.TurnResult{
				ToolCalls:  []provider.ToolCall{{ID: "sh_3", Name: "shell", Arguments: json.RawMessage(`{"command":"printf 'b1\\nb2\\nb3\\nb4\\nb5\\n'"}`)}},
				StopReason: "tool_use",
			}, nil
		},
		func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
			turnIdx++
			// Under a 3M-char budget, sh_1 must still be inline (not pointerized)
			// so the KV prompt prefix stays untouched.
			for _, msg := range req.Messages {
				for _, res := range msg.ToolResults {
					if res.ToolCallID == "sh_1" {
						if !strings.Contains(res.LLMOutput, "line5") || strings.Contains(res.LLMOutput, "moved out of the provider context window") {
							t.Fatalf("sh_1 was prematurely pointerized below pressure threshold: %q", res.LLMOutput)
						}
					}
				}
			}
			return provider.TurnResult{
				ToolCalls:  []provider.ToolCall{{ID: "done", Name: "finish", Arguments: json.RawMessage(`{"message":"ok"}`)}},
				StopReason: "tool_use",
			}, nil
		},
	)
	res, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != session.StatusCompleted {
		t.Fatalf("expected completed, got %s (%s)", res.Status, res.LastError)
	}
}

func TestCompactorPreventsEmptyOscillatingCompactionLoop(t *testing.T) {
	store := session.NewStore(t.TempDir())
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "fake",
		Model:            "test",
		CompletionPolicy: session.CompletionPolicyAutonomous,
	}
	state := session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: meta.CreatedAt}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	// Only 4 messages total (< KeepRecentMessages=6), so nothing can be dropped.
	messages := []session.Message{
		session.NewMessage("user", "Inspect workspace."),
		session.NewAssistantMessage("", "", []session.ToolCall{{ID: "c1", Name: "shell", Arguments: json.RawMessage(`{"command":"ls"}`)}}),
		session.NewToolMessage([]session.ToolResult{{ToolCallID: "c1", Name: "shell", LLMOutput: strings.Repeat("x", 9000)}}),
		session.NewAssistantMessage("working", "", nil),
	}
	profile := compactionContextProfile{
		InputCharThreshold:        16000,
		KeepRecentToolResults:     1,
		KeepRecentToolResultBytes: 64 * 1024,
		KeepRecentMessages:        6,
	}
	// First compaction triggers because lastCompactionInputChars == 0 and systemPromptChars + messages > 16000.
	var firstEvents []string
	_, size1, didCompact1, err := newCompactor(store).build(context.Background(), meta.ID, meta.Workdir, state, messages, nil, nil, profile, 0, 12000, nil, func(evt events.Event) error {
		firstEvents = append(firstEvents, evt.Type)
		return nil
	})
	if err != nil || !didCompact1 {
		t.Fatalf("first compaction expected didCompact=true err=nil, got didCompact=%v err=%v", didCompact1, err)
	}
	// Second call with a huge jump in system/recent size STILL has len(dropped) == 0,
	// so it must reuse instead of running another empty compact.finished loop (#257).
	messages = append(messages, session.NewMessage("user", strings.Repeat("more ", 3000)))
	var secondEvents []string
	_, _, didCompact2, err := newCompactor(store).build(context.Background(), meta.ID, meta.Workdir, state, messages, nil, nil, profile, size1, 12000, nil, func(evt events.Event) error {
		secondEvents = append(secondEvents, evt.Type)
		return nil
	})
	if err != nil {
		t.Fatalf("second build err: %v", err)
	}
	if didCompact2 {
		t.Fatalf("expected second build with zero droppable messages to reuse rather than oscillate, events=%v", secondEvents)
	}
	if len(secondEvents) != 1 || secondEvents[0] != "compact.reused" {
		t.Fatalf("expected compact.reused, got %v", secondEvents)
	}
}

func TestSystemPromptKVPrefixImmutableAcrossGoalAndArtifactTurns(t *testing.T) {
	workdir := t.TempDir()
	tokenBudget := int64(200000)
	timeBudget := int64(600)
	goalTurn1 := session.SessionGoal{
		GoalID:            "goal_1",
		Objective:         "Audit repo and write reports/audit.md",
		Status:            session.GoalStatusActive,
		Mode:              session.GoalModeGoal,
		TokensUsed:        1200,
		TimeUsedSeconds:   3,
		TokenBudget:       &tokenBudget,
		TimeBudgetSeconds: &timeBudget,
	}
	goalTurn9 := goalTurn1
	goalTurn9.TokensUsed = 98765
	goalTurn9.TimeUsedSeconds = 142

	g1 := goalPromptContext(goalTurn1)
	g9 := goalPromptContext(goalTurn9)
	if g1 != g9 {
		t.Fatalf("goalPromptContext mutated between turns (busts KV cache):\nturn1:\n%s\nturn9:\n%s", g1, g9)
	}
	if !strings.Contains(g1, "tokens <= 200000") || !strings.Contains(g1, "get_goal") {
		t.Fatalf("expected static budget limits and get_goal pointer, got:\n%s", g1)
	}

	userMsg := session.NewMessage("user", "Audit db.py and write the report to reports/audit.md.")
	sysTurn1 := buildSystemPrompt(workdir, session.ModeExec, "", nil, nil, session.State{}, []session.Message{userMsg}, "", "")
	// Simulate 10 read_file calls + writing reports/audit.md.
	msgsLater := []session.Message{userMsg}
	for i := 0; i < 10; i++ {
		msgsLater = append(msgsLater, session.NewToolMessage([]session.ToolResult{{
			ToolCallID: fmt.Sprintf("r_%d", i),
			Name:       "read_file",
			Metadata:   map[string]any{"path": fmt.Sprintf("f%d.go", i)},
		}}))
	}
	msgsLater = append(msgsLater, session.NewToolMessage([]session.ToolResult{{
		ToolCallID: "w_1",
		Name:       "write_file",
		Metadata:   map[string]any{"path": "reports/audit.md"},
	}}))
	sysTurn11 := buildSystemPrompt(workdir, session.ModeExec, "", nil, nil, session.State{}, msgsLater, "", "")
	if sysTurn1 != sysTurn11 {
		t.Fatalf("systemPrompt mutated mid-session after reads & artifact write (busts KV cache):\nbefore len=%d after len=%d", len(sysTurn1), len(sysTurn11))
	}
}

func TestToolLoopReminderCatchesCyclicReadOnlyLoops(t *testing.T) {
	var messages []session.Message
	messages = append(messages, session.NewMessage("user", "Fix bugs in a.go and b.go"))
	// Alternating read cycle across 2 files (period-2 cycle) 4 times each = 8 reads, 6 repeats.
	for round := 0; round < 4; round++ {
		for _, p := range []string{"a.go", "b.go"} {
			messages = append(messages, session.NewToolMessage([]session.ToolResult{{
				Name: "read_file",
				Metadata: map[string]any{
					"path":   p,
					"offset": 1,
					"end":    40,
				},
			}}))
		}
	}
	reminder := toolLoopReminderText(messages)
	if !strings.Contains(reminder, "cyclic read-only tool loop") {
		t.Fatalf("expected cyclic read-only loop reminder, got %q", reminder)
	}
	// After an edit_file, the cycle window resets so productive work is not nagged.
	messages = append(messages, session.NewToolMessage([]session.ToolResult{{
		Name:     "edit_file",
		Metadata: map[string]any{"path": "a.go"},
	}}))
	if got := toolLoopReminderText(messages); got != "" {
		t.Fatalf("expected reminder to clear after mutating edit_file, got %q", got)
	}
}
