package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/events"
	"go-cli-agent/internal/provider"
	"go-cli-agent/internal/session"
	"go-cli-agent/internal/tools"
)

func TestProviderToolsExposePlanModeToolsOnlyWhilePending(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	store := session.NewStore(cfg.Session.Dir)
	registry, err := tools.NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	defaultTools := providerToolsForPlanMode(registry, nil)
	if hasProviderTool(defaultTools, "submit_plan") || hasProviderTool(defaultTools, "request_user_input") {
		t.Fatalf("plan-only tools must not be exposed in default mode: %#v", defaultTools)
	}
	pending := &session.PlanModeState{Status: session.PlanModeStatusPlanning}
	planTools := providerToolsForPlanMode(registry, pending)
	for _, name := range []string{"read_file", "grep", "get_plan_mode", "request_user_input", "submit_plan"} {
		if !hasProviderTool(planTools, name) {
			t.Fatalf("expected %s in Plan Mode tools: %#v", name, planTools)
		}
	}
	for _, name := range []string{"shell", "write_file", "todo_write", "agent_spawn", "finish"} {
		if hasProviderTool(planTools, name) {
			t.Fatalf("did not expect %s in Plan Mode tools: %#v", name, planTools)
		}
	}
}

func TestContinuePlanModeRetryAfterCreatedEventFailureDoesNotReplacePlanMode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_planmode_create_retry",
			"status":"completed",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"planning continued"}]}],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	cfg.DefaultProvider = "openai-compatible"
	cfg.Providers["openai-compatible"] = config.Provider{
		APIProvider:       "openai-compatible",
		APIKeyEnv:         "OPENAI_API_KEY",
		BaseURL:           server.URL + "/v1",
		Model:             "gpt-5.4",
		TimeoutSec:        3,
		RequestTimeoutSec: 3,
		WireAPI:           "responses",
		Retry:             config.Retry{MaxAttempts: 1},
	}
	t.Setenv("OPENAI_API_KEY", "test-key")
	runner := NewRunner(cfg)
	sessionID := session.NewSessionID()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               sessionID,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai-compatible",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		ProviderOptions:  providerOptionsFromConfig("openai-compatible", cfg.Providers["openai-compatible"]),
	}
	if err := runner.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	draft := session.PlanModeDraft{Enabled: true, Objective: "Create plan mode on continue", Source: session.PlanModeSourceCLI}
	blockRuntimeEventsPath(t, runner.store, sessionID)

	result, err := runner.Continue(context.Background(), ContinueRequest{SessionID: sessionID, PlanMode: &draft, Source: session.PlanModeSourceCLI})
	if err == nil || !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected plan mode created event append error, got result=%#v err=%v", result, err)
	}
	created, err := runner.store.LoadPlanMode(sessionID)
	if err != nil {
		t.Fatalf("load plan mode after failed event append: %v", err)
	}
	if created.Status != session.PlanModeStatusPlanning || created.Objective != draft.Objective {
		t.Fatalf("expected durable created plan mode after event failure, got %#v", created)
	}
	history, err := runner.store.LoadPlanModeHistory(sessionID)
	if err != nil {
		t.Fatalf("load plan mode history after failed event append: %v", err)
	}
	if count := countRuntimePlanModeHistoryType(history, "planmode.created"); count != 1 {
		t.Fatalf("expected one created history row after failed event append, got %d history=%#v", count, history)
	}

	eventsPath := filepath.Join(runner.store.SessionDir(sessionID), "events.jsonl")
	if err := os.RemoveAll(eventsPath); err != nil {
		t.Fatalf("remove blocked events path: %v", err)
	}
	retried, err := runner.Continue(context.Background(), ContinueRequest{SessionID: sessionID, PlanMode: &draft, Source: session.PlanModeSourceCLI})
	if err != nil {
		t.Fatalf("retry plan mode creation event: result=%#v err=%v", retried, err)
	}
	reloaded, err := runner.store.LoadPlanMode(sessionID)
	if err != nil {
		t.Fatalf("load plan mode after retry: %v", err)
	}
	if reloaded.PlanModeID != created.PlanModeID {
		t.Fatalf("retry should not replace current plan mode, before=%#v after=%#v", created, reloaded)
	}
	history, err = runner.store.LoadPlanModeHistory(sessionID)
	if err != nil {
		t.Fatalf("load plan mode history after retry: %v", err)
	}
	if count := countRuntimePlanModeHistoryType(history, "planmode.created"); count != 1 {
		t.Fatalf("retry should not duplicate created history, got %d history=%#v", count, history)
	}
	events, err := runner.store.LoadEvents(sessionID)
	if err != nil {
		t.Fatalf("load events after retry: %v", err)
	}
	if count := countRuntimeEventType(events, "planmode.created"); count != 1 {
		t.Fatalf("retry should append one created event, got %d events=%#v", count, events)
	}
}

func TestPlanModeGateBlocksToolsAfterCreateGoalRequiresApproval(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	store := session.NewStore(cfg.Session.Dir)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyAutonomous,
	}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "tool_execute", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	registry, err := tools.NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	execCtx := tools.ExecContext{
		SessionID: meta.ID,
		Workdir:   meta.Workdir,
		Store:     store,
		Config:    cfg,
	}
	result, err := registry.Execute(context.Background(), "create_goal", execCtx, json.RawMessage(`{
		"objective":"Plan-gated mission",
		"mode":"mission",
		"require_plan_approval":true
	}`))
	if err != nil {
		t.Fatalf("create_goal execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("create_goal returned error: %s", result.DisplayOutput)
	}
	planMode, err := store.LoadPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("load plan mode: %v", err)
	}
	if !session.IsPlanModePending(planMode.Status) {
		t.Fatalf("expected pending plan mode after create_goal, got %#v", planMode)
	}
	controller := NewCompletionController(store, meta.ID, meta.Workdir, false, nil)
	decision := controller.EvaluateToolCall(nil, "write_file", json.RawMessage(`{"path":"x.txt","content":"blocked"}`))
	if decision.Status != GateBlock || decision.GateID != "plan_mode_pending" {
		t.Fatalf("expected write_file blocked by pending plan mode, got %#v", decision)
	}
}

func TestCompletionControllerBlocksWhenPlanModeSnapshotCorrupt(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	store := session.NewStore(cfg.Session.Dir)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyAutonomous,
	}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "tool_execute", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	planModePath := filepath.Join(store.SessionDir(meta.ID), "planmode.json")
	if err := os.WriteFile(planModePath, []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatalf("write corrupt plan mode: %v", err)
	}

	controller := NewCompletionController(store, meta.ID, meta.Workdir, false, nil)
	decision := controller.EvaluateToolCall(nil, "write_file", json.RawMessage(`{"path":"x.txt","content":"blocked"}`))
	if decision.Status != GateBlock || decision.GateID != "plan_mode_state" || !strings.Contains(decision.ModelMessage, "planmode.json") {
		t.Fatalf("expected corrupt planmode gate block with filename, got %#v", decision)
	}
}

func TestEngineSubmitPlanStopsTurnAndCompletesLaterToolResults(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "Plan this change before editing.")); err != nil {
		t.Fatalf("append user: %v", err)
	}
	if _, err := engine.store.CreatePlanMode(meta.ID, session.PlanModeDraft{Enabled: true, Objective: "Plan this change"}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	fake := provider.NewFake(func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
		if hasProviderTool(req.Tools, "write_file") {
			t.Fatalf("write_file leaked into planning tools: %#v", req.Tools)
		}
		if !hasProviderTool(req.Tools, "submit_plan") {
			t.Fatalf("submit_plan missing from planning tools: %#v", req.Tools)
		}
		return provider.TurnResult{
			ToolCalls: []provider.ToolCall{
				{
					ID:   "call_submit",
					Name: "submit_plan",
					Arguments: json.RawMessage(`{
						"title":"Plan",
						"summary":"Add Plan Mode safely.",
						"plan_markdown":"# Summary\n\nAdd Plan Mode safely.\n\n# Verification\n\nRun tests.",
						"verification":["go test ./internal/runtime"]
					}`),
				},
				{ID: "call_write", Name: "write_file", Arguments: json.RawMessage(`{"path":"x.txt","content":"no"}`)},
			},
			StopReason: "tool_use",
		}, nil
	})
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusAwaitingInput {
		t.Fatalf("expected awaiting input after submit_plan, got %#v", result)
	}
	loadedState, err := engine.store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if loadedState.Phase != "plan_approval" {
		t.Fatalf("expected plan_approval phase, got %#v", loadedState)
	}
	messages, err := engine.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	last := messages[len(messages)-1]
	if last.Role != "tool" || len(last.ToolResults) != 2 {
		t.Fatalf("expected submit_plan plus synthetic later result, got %#v", last)
	}
	if !strings.Contains(last.ToolResults[1].LLMOutput, "submit_plan ended") {
		t.Fatalf("expected synthetic result for later tool call, got %#v", last.ToolResults[1])
	}
	planMode, err := engine.store.LoadPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("load plan mode: %v", err)
	}
	if planMode.Status != session.PlanModeStatusAwaitingApproval || planMode.PlanVersion != 1 {
		t.Fatalf("unexpected submitted plan mode: %#v", planMode)
	}
}

func TestEnginePlanInputCancelStopsTurnAndCompletesLaterToolResults(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	engine.SetRunner(cancellingPlanInputRunner{})
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "Ask before planning.")); err != nil {
		t.Fatalf("append user: %v", err)
	}
	if _, err := engine.store.CreatePlanMode(meta.ID, session.PlanModeDraft{Enabled: true, Objective: "Ask before planning"}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	fake := provider.NewFake(func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
		if !hasProviderTool(req.Tools, "request_user_input") {
			t.Fatalf("request_user_input missing from planning tools: %#v", req.Tools)
		}
		return provider.TurnResult{
			ToolCalls: []provider.ToolCall{
				{
					ID:   "call_plan_input",
					Name: "request_user_input",
					Arguments: json.RawMessage(`{
						"questions":[{
							"id":"scope_choice",
							"header":"Scope",
							"question":"Which scope should the plan use?",
							"options":[
								{"label":"Narrow (Recommended)","description":"Keep the plan focused."},
								{"label":"Broad","description":"Include adjacent cleanup."}
							]
						}]
					}`),
				},
				{ID: "call_read_after_cancel", Name: "read_file", Arguments: json.RawMessage(`{"path":"README.md"}`)},
			},
			StopReason: "tool_use",
		}, nil
	})

	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusAwaitingInput {
		t.Fatalf("expected awaiting input after input cancellation, got %#v", result)
	}
	loadedState, err := engine.store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if loadedState.Phase != "plan_cancelled" {
		t.Fatalf("expected plan_cancelled phase, got %#v", loadedState)
	}
	messages, err := engine.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	last := messages[len(messages)-1]
	if last.Role != "tool" || len(last.ToolResults) != 2 {
		t.Fatalf("expected cancelled request_user_input plus synthetic later result, got %#v", last)
	}
	if !last.ToolResults[0].IsError || !strings.Contains(last.ToolResults[0].LLMOutput, "cancelled") {
		t.Fatalf("expected first tool result to record input cancellation, got %#v", last.ToolResults[0])
	}
	if strings.Contains(last.ToolResults[1].LLMOutput, "submit_plan ended") || !strings.Contains(last.ToolResults[1].LLMOutput, "Plan Mode was cancelled") {
		t.Fatalf("expected cancellation-specific synthetic result for later tool call, got %#v", last.ToolResults[1])
	}
}

type cancellingPlanInputRunner struct{}

func (cancellingPlanInputRunner) AutoContinue(context.Context, string) (RunResult, error) {
	return RunResult{}, errors.New("unexpected AutoContinue")
}

func (cancellingPlanInputRunner) RequestPlanInput(context.Context, string, session.PlanModeInputRequest) ([]session.PlanModeInputAnswer, error) {
	return nil, tools.ErrPlanInputCancelled
}

func TestEngineSubmitPlanReportsPlanSubmittedEventAppendError(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "Plan this change before editing.")); err != nil {
		t.Fatalf("append user: %v", err)
	}
	initialPlanMode, err := engine.store.CreatePlanMode(meta.ID, session.PlanModeDraft{Enabled: true, Objective: "Plan this change"})
	if err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	initialHistory, err := engine.store.LoadPlanModeHistory(meta.ID)
	if err != nil {
		t.Fatalf("load initial plan mode history: %v", err)
	}
	eventsPath := filepath.Join(engine.store.SessionDir(meta.ID), "events.jsonl")
	blockRuntimeEventsOnRequiredToolEvent(t, registry, "submit_plan", "planmode.plan_submitted", eventsPath)
	fake := provider.NewFake(func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
		if !hasProviderTool(req.Tools, "submit_plan") {
			t.Fatalf("submit_plan missing from planning tools: %#v", req.Tools)
		}
		return provider.TurnResult{
			ToolCalls: []provider.ToolCall{{
				ID:   "call_submit",
				Name: "submit_plan",
				Arguments: json.RawMessage(`{
					"title":"Plan",
					"summary":"Add Plan Mode safely.",
					"plan_markdown":"# Summary\n\nAdd Plan Mode safely.\n\n# Verification\n\nRun tests.",
					"verification":["go test ./internal/runtime"]
				}`),
			}},
			StopReason: "tool_use",
		}, nil
	})

	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err == nil {
		t.Fatalf("expected tool.after append error after failed submit_plan event, got result=%#v", result)
	}
	if !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected events append error with path context, got %v", err)
	}
	if !strings.Contains(err.Error(), "tool.after") {
		t.Fatalf("expected tool.after event context, got %v", err)
	}
	planMode, err := engine.store.LoadPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("load plan mode: %v", err)
	}
	if planMode.Status != initialPlanMode.Status || planMode.PlanVersion != initialPlanMode.PlanVersion || planMode.PlanMarkdown != "" {
		t.Fatalf("expected submit_plan event failure to restore initial plan mode, got %#v", planMode)
	}
	history, err := engine.store.LoadPlanModeHistory(meta.ID)
	if err != nil {
		t.Fatalf("load plan mode history: %v", err)
	}
	if len(history) != len(initialHistory) {
		t.Fatalf("expected plan mode history restored to %d entries, got %d: %#v", len(initialHistory), len(history), history)
	}
	messages, err := engine.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	var toolResult *session.ToolResult
	for _, msg := range messages {
		for i := range msg.ToolResults {
			if msg.ToolResults[i].Name == "submit_plan" {
				toolResult = &msg.ToolResults[i]
			}
		}
	}
	if toolResult == nil || !toolResult.IsError || !strings.Contains(toolResult.DisplayOutput, "planmode.plan_submitted") || !strings.Contains(toolResult.DisplayOutput, "events.jsonl") {
		t.Fatalf("expected persisted submit_plan error result with event context, got %#v in messages %#v", toolResult, messages)
	}
}

func TestEnginePlanCancelledReportsAwaitingInputEventAppendError(t *testing.T) {
	engine, meta, state, _, hookManager, _ := newTestEngine(t, session.ModeExec)
	eventsPath := filepath.Join(engine.store.SessionDir(meta.ID), "events.jsonl")
	if err := os.Remove(eventsPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove events: %v", err)
	}
	if err := os.Mkdir(eventsPath, 0o700); err != nil {
		t.Fatalf("block events path: %v", err)
	}

	result, err := engine.awaitingPlanCancelled(context.Background(), meta, state, hookManager)
	if err == nil {
		t.Fatalf("expected session.awaiting_input event append error, got result=%#v", result)
	}
	if !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected events append error with path context, got %v", err)
	}
	if !strings.Contains(err.Error(), "plan_cancelled") || !strings.Contains(err.Error(), "session.awaiting_input") {
		t.Fatalf("expected plan cancelled awaiting-input event context, got %v", err)
	}
}

func TestParentLinkedQueueBlockedDuringPendingPlanMode(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	runner := NewRunner(cfg)
	parentID := session.NewSessionID()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               parentID,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyAutonomous,
	}
	if err := runner.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_approval"}); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if _, err := runner.store.CreatePlanMode(parentID, session.PlanModeDraft{Enabled: true, Objective: "Plan before child work"}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	if _, err := runner.QueueSubmit(context.Background(), QueueSubmitRequest{ParentSessionID: parentID, Prompt: "child task"}); err == nil || !strings.Contains(err.Error(), "plan mode is pending") {
		t.Fatalf("expected parent-linked queue rejection, got %v", err)
	}
	if _, err := runner.QueueSubmit(context.Background(), QueueSubmitRequest{Prompt: "independent task"}); err != nil {
		t.Fatalf("independent queue job should not be blocked: %v", err)
	}
}

func TestCancelPlanModeDoesNotDuplicateRecoveredInputToolResult(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
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
	if _, err := runner.store.CreatePlanMode(sessionID, session.PlanModeDraft{Enabled: true, Objective: "Cancel plan input"}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	request := session.PlanModeInputRequest{
		RequestID:  "pmq_cancel",
		ToolCallID: "call_cancel",
		Questions: []session.PlanModeInputQuestion{{
			ID:       "scope_choice",
			Header:   "Scope",
			Question: "Which scope?",
			Options: []session.PlanModeInputOption{
				{Label: "Narrow (Recommended)", Description: "Keep it focused."},
				{Label: "Broad", Description: "Include cleanup."},
			},
		}},
		Status:    "pending",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if _, err := runner.store.SetPlanModePendingRequest(sessionID, request, session.PlanModeSourceTool); err != nil {
		t.Fatalf("set pending request: %v", err)
	}

	for i := 0; i < 2; i++ {
		result, err := runner.Continue(context.Background(), ContinueRequest{SessionID: sessionID, CancelPlan: true, Source: session.PlanModeSourceCLI})
		if err != nil {
			t.Fatalf("cancel plan %d: %v", i+1, err)
		}
		if result.Status != session.StatusAwaitingInput {
			t.Fatalf("expected awaiting input after cancel %d, got %#v", i+1, result)
		}
	}

	messages, err := runner.store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	var count int
	for _, msg := range messages {
		for _, result := range msg.ToolResults {
			if result.ToolCallID == request.ToolCallID && result.Name == "request_user_input" {
				count++
			}
		}
	}
	if count != 1 {
		t.Fatalf("expected one recovered cancellation result for %s, got %d messages=%#v", request.ToolCallID, count, messages)
	}
}

func TestCancelPlanModeRecordsAwaitingInputLifecycleEvent(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
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
	if err := runner.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_approval", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := runner.store.CreatePlanMode(sessionID, session.PlanModeDraft{Enabled: true, Objective: "Cancel plan mode", Source: session.PlanModeSourceCLI}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}

	result, err := runner.Continue(context.Background(), ContinueRequest{SessionID: sessionID, CancelPlan: true, Source: session.PlanModeSourceCLI})
	if err != nil {
		t.Fatalf("cancel plan mode: result=%#v err=%v", result, err)
	}
	if result.Status != session.StatusAwaitingInput {
		t.Fatalf("expected awaiting input after plan cancellation, got %#v", result)
	}
	events, err := runner.store.LoadEvents(sessionID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	if count := countRuntimeEventType(events, "planmode.cancelled"); count != 1 {
		t.Fatalf("expected one planmode.cancelled event, got %d events=%#v", count, events)
	}
	if count := countRuntimeEventWithReason(events, "session.awaiting_input", "plan_cancelled"); count != 1 {
		t.Fatalf("expected one plan-cancelled awaiting-input lifecycle event, got %d events=%#v", count, events)
	}
}

func TestPlanInputCancelReturnsHistoryAppendError(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
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
	if _, err := runner.store.CreatePlanMode(sessionID, session.PlanModeDraft{Enabled: true, Objective: "Cancel plan input"}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	request := session.PlanModeInputRequest{
		RequestID:  "pmq_cancel_history",
		ToolCallID: "call_cancel_history",
		Questions: []session.PlanModeInputQuestion{{
			ID:       "scope_choice",
			Header:   "Scope",
			Question: "Which scope?",
			Options: []session.PlanModeInputOption{
				{Label: "Narrow (Recommended)", Description: "Keep it focused."},
				{Label: "Broad", Description: "Include cleanup."},
			},
		}},
		Status:    "pending",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if _, err := runner.store.SetPlanModePendingRequest(sessionID, request, session.PlanModeSourceTool); err != nil {
		t.Fatalf("set pending request: %v", err)
	}
	blockRuntimePlanModeHistoryPath(t, runner.store, sessionID)
	err := runner.appendPlanInputCancelToolResult(sessionID, session.PlanModeSourceCLI)
	if err == nil || !strings.Contains(err.Error(), "planmode-history.jsonl") {
		t.Fatalf("expected plan mode history append error, got %v", err)
	}
}

func TestPlanInputCancelRetryAfterHistoryFailureRestoresFacts(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
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
	if _, err := runner.store.CreatePlanMode(sessionID, session.PlanModeDraft{Enabled: true, Objective: "Cancel plan input"}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	request := session.PlanModeInputRequest{
		RequestID:  "pmq_cancel_retry_history",
		ToolCallID: "call_cancel_retry_history",
		Questions: []session.PlanModeInputQuestion{{
			ID:       "scope_choice",
			Header:   "Scope",
			Question: "Which scope?",
			Options: []session.PlanModeInputOption{
				{Label: "Narrow (Recommended)", Description: "Keep it focused."},
				{Label: "Broad", Description: "Include cleanup."},
			},
		}},
		Status:    "pending",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if _, err := runner.store.SetPlanModePendingRequest(sessionID, request, session.PlanModeSourceTool); err != nil {
		t.Fatalf("set pending request: %v", err)
	}
	blockRuntimePlanModeHistoryPath(t, runner.store, sessionID)

	err := runner.appendPlanInputCancelToolResult(sessionID, session.PlanModeSourceCLI)
	if err == nil || !strings.Contains(err.Error(), "planmode-history.jsonl") {
		t.Fatalf("expected plan mode history append error, got %v", err)
	}
	messages, err := runner.store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("load messages after failed history append: %v", err)
	}
	if count := countRuntimeToolResults(messages, request.ToolCallID, "request_user_input"); count != 1 {
		t.Fatalf("expected one replay tool result after failed history append, got %d messages=%#v", count, messages)
	}

	historyPath := filepath.Join(runner.store.SessionDir(sessionID), "artifacts", "planmode-history.jsonl")
	if err := os.RemoveAll(historyPath); err != nil {
		t.Fatalf("remove blocked history path: %v", err)
	}
	if err := runner.appendPlanInputCancelToolResult(sessionID, session.PlanModeSourceCLI); err != nil {
		t.Fatalf("retry plan input cancellation facts: %v", err)
	}
	messages, err = runner.store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("load messages after retry: %v", err)
	}
	if count := countRuntimeToolResults(messages, request.ToolCallID, "request_user_input"); count != 1 {
		t.Fatalf("retry should not duplicate replay tool result, got %d messages=%#v", count, messages)
	}
	history, err := runner.store.LoadPlanModeHistory(sessionID)
	if err != nil {
		t.Fatalf("load history after retry: %v", err)
	}
	if count := countRuntimePlanModeHistoryType(history, "planmode.input_cancelled"); count != 1 {
		t.Fatalf("retry should restore one input-cancel history row, got %d history=%#v", count, history)
	}
	events, err := runner.store.LoadEvents(sessionID)
	if err != nil {
		t.Fatalf("load events after retry: %v", err)
	}
	if count := countRuntimeEventType(events, "planmode.input_cancelled"); count != 1 {
		t.Fatalf("retry should append one input-cancel event, got %d events=%#v", count, events)
	}
}

func TestPlanInputAnswerRollsBackWhenToolResultAppendFails(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
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
	if _, err := runner.store.CreatePlanMode(sessionID, session.PlanModeDraft{Enabled: true, Objective: "Answer plan input"}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	request := session.PlanModeInputRequest{
		RequestID:  "pmq_answer_messages",
		ToolCallID: "call_answer_messages",
		Questions: []session.PlanModeInputQuestion{{
			ID:       "scope_choice",
			Header:   "Scope",
			Question: "Which scope?",
			Options: []session.PlanModeInputOption{
				{Label: "Narrow (Recommended)", Description: "Keep it focused."},
				{Label: "Broad", Description: "Include cleanup."},
			},
		}},
		Status:    "pending",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if _, err := runner.store.SetPlanModePendingRequest(sessionID, request, session.PlanModeSourceTool); err != nil {
		t.Fatalf("set pending request: %v", err)
	}
	beforeHistory, err := runner.store.LoadPlanModeHistory(sessionID)
	if err != nil {
		t.Fatalf("load plan mode history: %v", err)
	}
	blockRuntimeMessagesPath(t, runner.store, sessionID)

	err = runner.appendPlanInputToolResult(sessionID, request.RequestID, session.PlanModeSourceWeb, []session.PlanModeInputAnswer{{
		QuestionID: "scope_choice",
		Label:      "Narrow (Recommended)",
		Value:      "Narrow (Recommended)",
	}})
	if err == nil {
		t.Fatal("expected messages append error")
	}
	planMode, err := runner.store.LoadPlanMode(sessionID)
	if err != nil {
		t.Fatalf("load plan mode: %v", err)
	}
	if planMode.Status != session.PlanModeStatusAwaitingUserInput || planMode.PendingRequest == nil || planMode.PendingRequest.RequestID != request.RequestID {
		t.Fatalf("failed tool result append should restore pending input, got %#v", planMode)
	}
	history, err := runner.store.LoadPlanModeHistory(sessionID)
	if err != nil {
		t.Fatalf("load plan mode history after failed answer: %v", err)
	}
	if len(history) != len(beforeHistory) || hasRuntimePlanModeHistoryType(history, "planmode.input_answered") {
		t.Fatalf("failed tool result append should restore plan mode history, before=%#v after=%#v", beforeHistory, history)
	}
}

func TestPlanInputAnswerRetryAfterEventFailureRestoresEvent(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
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
	if _, err := runner.store.CreatePlanMode(sessionID, session.PlanModeDraft{Enabled: true, Objective: "Answer plan input"}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	request := session.PlanModeInputRequest{
		RequestID:  "pmq_answer_event",
		ToolCallID: "call_answer_event",
		Questions: []session.PlanModeInputQuestion{{
			ID:       "scope_choice",
			Header:   "Scope",
			Question: "Which scope?",
			Options: []session.PlanModeInputOption{
				{Label: "Narrow (Recommended)", Description: "Keep it focused."},
				{Label: "Broad", Description: "Include cleanup."},
			},
		}},
		Status:    "pending",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if _, err := runner.store.SetPlanModePendingRequest(sessionID, request, session.PlanModeSourceTool); err != nil {
		t.Fatalf("set pending request: %v", err)
	}
	answers := []session.PlanModeInputAnswer{{
		QuestionID: "scope_choice",
		Label:      "Narrow (Recommended)",
		Value:      "Narrow (Recommended)",
	}}
	blockRuntimeEventsPath(t, runner.store, sessionID)

	err := runner.appendPlanInputToolResult(sessionID, request.RequestID, session.PlanModeSourceWeb, answers)
	if err == nil || !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected event append error, got %v", err)
	}
	planMode, err := runner.store.LoadPlanMode(sessionID)
	if err != nil {
		t.Fatalf("load plan mode after failed event append: %v", err)
	}
	if planMode.Status != session.PlanModeStatusPlanning || planMode.PendingRequest != nil {
		t.Fatalf("failed event append should keep answered plan mode facts, got %#v", planMode)
	}
	messages, err := runner.store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("load messages after failed event append: %v", err)
	}
	if count := countRuntimeToolResults(messages, request.ToolCallID, "request_user_input"); count != 1 {
		t.Fatalf("expected one replay tool result after failed event append, got %d messages=%#v", count, messages)
	}
	history, err := runner.store.LoadPlanModeHistory(sessionID)
	if err != nil {
		t.Fatalf("load history after failed event append: %v", err)
	}
	if count := countRuntimePlanModeHistoryType(history, "planmode.input_answered"); count != 1 {
		t.Fatalf("expected one answered history row after failed event append, got %d history=%#v", count, history)
	}

	eventsPath := filepath.Join(runner.store.SessionDir(sessionID), "events.jsonl")
	if err := os.RemoveAll(eventsPath); err != nil {
		t.Fatalf("remove blocked events path: %v", err)
	}
	if err := runner.appendPlanInputToolResult(sessionID, request.RequestID, session.PlanModeSourceWeb, answers); err != nil {
		t.Fatalf("retry plan input answer event: %v", err)
	}
	messages, err = runner.store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("load messages after retry: %v", err)
	}
	if count := countRuntimeToolResults(messages, request.ToolCallID, "request_user_input"); count != 1 {
		t.Fatalf("retry should not duplicate replay tool result, got %d messages=%#v", count, messages)
	}
	history, err = runner.store.LoadPlanModeHistory(sessionID)
	if err != nil {
		t.Fatalf("load history after retry: %v", err)
	}
	if count := countRuntimePlanModeHistoryType(history, "planmode.input_answered"); count != 1 {
		t.Fatalf("retry should not duplicate answered history, got %d history=%#v", count, history)
	}
	events, err := runner.store.LoadEvents(sessionID)
	if err != nil {
		t.Fatalf("load events after retry: %v", err)
	}
	if count := countRuntimeEventType(events, "planmode.input_answered"); count != 1 {
		t.Fatalf("retry should append one answered event, got %d events=%#v", count, events)
	}
}

func TestCancelPlanModeReportsCancelledEventAppendError(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	runner := NewRunner(cfg)
	sessionID := session.NewSessionID()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               sessionID,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyAutonomous,
	}
	if err := runner.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_approval", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := runner.store.CreatePlanMode(sessionID, session.PlanModeDraft{Enabled: true, Objective: "Cancel plan mode"}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	blockRuntimeEventsPath(t, runner.store, sessionID)

	result, err := runner.Continue(context.Background(), ContinueRequest{SessionID: sessionID, CancelPlan: true, Source: session.PlanModeSourceCLI})
	if err == nil || !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected plan mode cancellation event append error, got result=%#v err=%v", result, err)
	}
}

func TestCancelPlanModeRetryAfterCancelledEventFailureDoesNotDuplicateHistory(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
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
	if err := runner.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_approval", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	planMode, err := runner.store.CreatePlanMode(sessionID, session.PlanModeDraft{Enabled: true, Objective: "Cancel plan mode", Source: session.PlanModeSourceCLI})
	if err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	blockRuntimeEventsPath(t, runner.store, sessionID)

	result, err := runner.Continue(context.Background(), ContinueRequest{SessionID: sessionID, CancelPlan: true, Source: session.PlanModeSourceCLI})
	if err == nil || !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected cancellation event append error, got result=%#v err=%v", result, err)
	}
	cancelled, err := runner.store.LoadPlanMode(sessionID)
	if err != nil {
		t.Fatalf("load plan mode after failed event append: %v", err)
	}
	if cancelled.PlanModeID != planMode.PlanModeID || cancelled.Status != session.PlanModeStatusCancelled {
		t.Fatalf("expected cancellation state to be durable, got %#v", cancelled)
	}
	history, err := runner.store.LoadPlanModeHistory(sessionID)
	if err != nil {
		t.Fatalf("load plan mode history after failed event append: %v", err)
	}
	if count := countRuntimePlanModeHistoryType(history, "planmode.cancelled"); count != 1 {
		t.Fatalf("expected one durable cancellation history row after failed event append, got %d history=%#v", count, history)
	}

	eventsPath := filepath.Join(runner.store.SessionDir(sessionID), "events.jsonl")
	if err := os.RemoveAll(eventsPath); err != nil {
		t.Fatalf("remove blocked events path: %v", err)
	}
	retried, err := runner.Continue(context.Background(), ContinueRequest{SessionID: sessionID, CancelPlan: true, Source: session.PlanModeSourceCLI})
	if err != nil {
		t.Fatalf("retry cancel plan mode: result=%#v err=%v", retried, err)
	}
	if retried.Status != session.StatusAwaitingInput {
		t.Fatalf("expected awaiting input after retry, got %#v", retried)
	}
	history, err = runner.store.LoadPlanModeHistory(sessionID)
	if err != nil {
		t.Fatalf("load plan mode history after retry: %v", err)
	}
	if count := countRuntimePlanModeHistoryType(history, "planmode.cancelled"); count != 1 {
		t.Fatalf("retry should not duplicate cancellation history, got %d history=%#v", count, history)
	}
	events, err := runner.store.LoadEvents(sessionID)
	if err != nil {
		t.Fatalf("load events after retry: %v", err)
	}
	if count := countRuntimeEventType(events, "planmode.cancelled"); count != 1 {
		t.Fatalf("retry should append one cancellation event, got %d events=%#v", count, events)
	}
}

func TestApprovePlanModeReportsPlanApprovedEventAppendError(t *testing.T) {
	var providerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		if r.URL.Path != "/v1/responses" {
			t.Errorf("expected /v1/responses, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_planmode_event",
			"status":"completed",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ready after approval"}]}],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	cfg.DefaultProvider = "openai-compatible"
	cfg.Providers["openai-compatible"] = config.Provider{
		APIProvider:       "openai-compatible",
		APIKeyEnv:         "OPENAI_API_KEY",
		BaseURL:           server.URL + "/v1",
		Model:             "gpt-5.4",
		TimeoutSec:        3,
		RequestTimeoutSec: 3,
		WireAPI:           "responses",
		Retry:             config.Retry{MaxAttempts: 1},
	}
	t.Setenv("OPENAI_API_KEY", "test-key")
	runner := NewRunner(cfg)
	sessionID := session.NewSessionID()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               sessionID,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai-compatible",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		ProviderOptions:  providerOptionsFromConfig("openai-compatible", cfg.Providers["openai-compatible"]),
	}
	if err := runner.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_approval", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := runner.store.CreatePlanMode(sessionID, session.PlanModeDraft{Enabled: true, Objective: "Approve plan mode"}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	if _, err := runner.store.SubmitPlanMode(sessionID, session.PlanModeSubmitInput{
		Title:        "Plan",
		Summary:      "Plan summary",
		PlanMarkdown: "# Plan\n\nDo it.\n\n# Verification\n\nRun tests.",
		Verification: []string{"go test ./internal/runtime"},
		Source:       session.PlanModeSourceTool,
	}); err != nil {
		t.Fatalf("submit plan mode: %v", err)
	}
	blockRuntimeEventsPath(t, runner.store, sessionID)

	result, err := runner.Continue(context.Background(), ContinueRequest{SessionID: sessionID, ApprovePlan: true, Source: session.PlanModeSourceCLI})
	if err == nil || !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected plan mode approval event append error, got result=%#v err=%v", result, err)
	}
	if calls := providerCalls.Load(); calls != 0 {
		t.Fatalf("provider should not run after missing plan approval event, got %d calls", calls)
	}
}

func TestApprovePlanModeRetryAfterApprovalMessageFailureAppendsApprovalMessage(t *testing.T) {
	var providerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_planmode_retry",
			"status":"completed",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"approval retry continued"}]}],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	cfg.DefaultProvider = "openai-compatible"
	cfg.Providers["openai-compatible"] = config.Provider{
		APIProvider:       "openai-compatible",
		APIKeyEnv:         "OPENAI_API_KEY",
		BaseURL:           server.URL + "/v1",
		Model:             "gpt-5.4",
		TimeoutSec:        3,
		RequestTimeoutSec: 3,
		WireAPI:           "responses",
		Retry:             config.Retry{MaxAttempts: 1},
	}
	t.Setenv("OPENAI_API_KEY", "test-key")
	runner := NewRunner(cfg)
	sessionID := session.NewSessionID()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               sessionID,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai-compatible",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		ProviderOptions:  providerOptionsFromConfig("openai-compatible", cfg.Providers["openai-compatible"]),
	}
	if err := runner.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_approval", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	goal, err := runner.store.CreateGoal(sessionID, session.GoalDraft{
		Enabled:             true,
		Mode:                session.GoalModeMission,
		Objective:           "Retry plan approval after event failure",
		RequirePlanApproval: true,
		Source:              session.GoalSourceCLI,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	planMode, created, err := runner.store.EnsurePlanModeForGoal(sessionID, goal, session.PlanModeSourceCLI)
	if err != nil {
		t.Fatalf("ensure plan mode: %v", err)
	}
	if !created {
		t.Fatalf("expected linked plan mode creation")
	}
	if _, err := runner.store.SubmitPlanMode(sessionID, session.PlanModeSubmitInput{
		Title:        "Plan",
		Summary:      "Plan summary",
		PlanMarkdown: "# Plan\n\nDo it.",
		Verification: []string{"manual"},
		Source:       session.PlanModeSourceTool,
	}); err != nil {
		t.Fatalf("submit plan mode: %v", err)
	}
	blockRuntimeMessagesPath(t, runner.store, sessionID)
	result, err := runner.Continue(context.Background(), ContinueRequest{SessionID: sessionID, ApprovePlan: true, Source: session.PlanModeSourceCLI})
	if err == nil {
		t.Fatalf("expected approval replay message append error, got result=%#v err=%v", result, err)
	}
	if calls := providerCalls.Load(); calls != 0 {
		t.Fatalf("provider should not run while approval replay message is missing, got %d calls", calls)
	}
	advanced, loadErr := runner.store.LoadPlanMode(sessionID)
	if loadErr != nil {
		t.Fatalf("load plan mode after failed approval: %v", loadErr)
	}
	if advanced.PlanModeID != planMode.PlanModeID {
		t.Fatalf("failed approval should keep the same plan mode, got %#v", advanced)
	}
	messages, err := runner.store.LoadMessages(sessionID)
	if err == nil && countPlanModeApprovalMessages(messages) != 0 {
		t.Fatalf("failed approval should not have appended replay message yet, got %#v", messages)
	}

	messagesPath := filepath.Join(runner.store.SessionDir(sessionID), "messages.jsonl")
	if err := os.RemoveAll(messagesPath); err != nil {
		t.Fatalf("remove blocked messages path: %v", err)
	}
	retried, err := runner.Continue(context.Background(), ContinueRequest{SessionID: sessionID, ApprovePlan: true, Source: session.PlanModeSourceCLI})
	if err != nil {
		t.Fatalf("retry plan approval: result=%#v err=%v", retried, err)
	}
	if retried.Status != session.StatusAwaitingInput {
		t.Fatalf("expected retry to continue into awaiting input, got %#v", retried)
	}
	if calls := providerCalls.Load(); calls != 1 {
		t.Fatalf("expected provider to run exactly once after retry, got %d", calls)
	}
	messages, err = runner.store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("load messages after retry: %v", err)
	}
	if count := countPlanModeApprovalMessages(messages); count != 1 {
		t.Fatalf("expected one replayable planmode approval user message after retry, got %d messages=%#v", count, messages)
	}
	loadedGoal, err := runner.store.LoadGoal(sessionID)
	if err != nil {
		t.Fatalf("load goal after retry: %v", err)
	}
	if loadedGoal.Mission == nil || loadedGoal.Mission.PlanStatus != session.MissionPlanStatusApproved {
		t.Fatalf("retry should approve linked mission plan, got %#v", loadedGoal.Mission)
	}
}

func TestRevisePlanModeRetryAfterRevisionMessageFailureAppendsRevisionMessage(t *testing.T) {
	var providerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_planmode_revision_retry",
			"status":"completed",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"revision retry continued"}]}],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	cfg.DefaultProvider = "openai-compatible"
	cfg.Providers["openai-compatible"] = config.Provider{
		APIProvider:       "openai-compatible",
		APIKeyEnv:         "OPENAI_API_KEY",
		BaseURL:           server.URL + "/v1",
		Model:             "gpt-5.4",
		TimeoutSec:        3,
		RequestTimeoutSec: 3,
		WireAPI:           "responses",
		Retry:             config.Retry{MaxAttempts: 1},
	}
	t.Setenv("OPENAI_API_KEY", "test-key")
	runner := NewRunner(cfg)
	sessionID := session.NewSessionID()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               sessionID,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai-compatible",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		ProviderOptions:  providerOptionsFromConfig("openai-compatible", cfg.Providers["openai-compatible"]),
	}
	if err := runner.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_approval", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	planMode, err := runner.store.CreatePlanMode(sessionID, session.PlanModeDraft{Enabled: true, Objective: "Retry plan revision", Source: session.PlanModeSourceCLI})
	if err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	if _, err := runner.store.SubmitPlanMode(sessionID, session.PlanModeSubmitInput{
		Title:        "Plan",
		Summary:      "Plan summary",
		PlanMarkdown: "# Plan\n\nDo it.",
		Verification: []string{"manual"},
		Source:       session.PlanModeSourceTool,
	}); err != nil {
		t.Fatalf("submit plan mode: %v", err)
	}

	revisionMessage := "Use a narrower implementation."
	blockRuntimeMessagesPath(t, runner.store, sessionID)
	result, err := runner.Continue(context.Background(), ContinueRequest{SessionID: sessionID, Message: revisionMessage, Source: session.PlanModeSourceCLI})
	if err == nil {
		t.Fatalf("expected revision replay message append error, got result=%#v err=%v", result, err)
	}
	if calls := providerCalls.Load(); calls != 0 {
		t.Fatalf("provider should not run while revision replay message is missing, got %d calls", calls)
	}
	revised, err := runner.store.LoadPlanMode(sessionID)
	if err != nil {
		t.Fatalf("load plan mode after failed revision: %v", err)
	}
	if revised.PlanModeID != planMode.PlanModeID || revised.Status != session.PlanModeStatusPlanning {
		t.Fatalf("expected partially revised planning plan mode, got %#v", revised)
	}
	messages, err := runner.store.LoadMessages(sessionID)
	if err == nil && countPlanModeRevisionMessages(messages) != 0 {
		t.Fatalf("failed revision should not have appended replay message yet, got %#v", messages)
	}

	messagesPath := filepath.Join(runner.store.SessionDir(sessionID), "messages.jsonl")
	if err := os.RemoveAll(messagesPath); err != nil {
		t.Fatalf("remove blocked messages path: %v", err)
	}
	retried, err := runner.Continue(context.Background(), ContinueRequest{SessionID: sessionID, Message: revisionMessage, Source: session.PlanModeSourceCLI})
	if err != nil {
		t.Fatalf("retry plan revision: result=%#v err=%v", retried, err)
	}
	if retried.Status != session.StatusAwaitingInput {
		t.Fatalf("expected retry to continue into awaiting input, got %#v", retried)
	}
	if calls := providerCalls.Load(); calls != 1 {
		t.Fatalf("expected provider to run exactly once after retry, got %d", calls)
	}
	messages, err = runner.store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("load messages after retry: %v", err)
	}
	if count := countPlanModeRevisionMessages(messages); count != 1 {
		t.Fatalf("expected one replayable planmode revision user message after retry, got %d messages=%#v", count, messages)
	}

	duplicate, err := runner.Continue(context.Background(), ContinueRequest{SessionID: sessionID, Message: revisionMessage, Source: session.PlanModeSourceCLI})
	if err != nil {
		t.Fatalf("continue after recorded revision: result=%#v err=%v", duplicate, err)
	}
	if calls := providerCalls.Load(); calls != 2 {
		t.Fatalf("expected provider to run again for ordinary planning continuation, got %d", calls)
	}
	messages, err = runner.store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("load messages after duplicate continuation: %v", err)
	}
	if count := countPlanModeRevisionMessages(messages); count != 1 {
		t.Fatalf("recorded revision should not be reclassified on repeated planning continuation, got %d messages=%#v", count, messages)
	}
}

func TestRevisePlanModeRetryReportsCorruptHistory(t *testing.T) {
	var providerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_planmode_revision_corrupt_history",
			"status":"completed",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"should not run"}]}],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	cfg.DefaultProvider = "openai-compatible"
	cfg.Providers["openai-compatible"] = config.Provider{
		APIProvider:       "openai-compatible",
		APIKeyEnv:         "OPENAI_API_KEY",
		BaseURL:           server.URL + "/v1",
		Model:             "gpt-5.4",
		TimeoutSec:        3,
		RequestTimeoutSec: 3,
		WireAPI:           "responses",
		Retry:             config.Retry{MaxAttempts: 1},
	}
	t.Setenv("OPENAI_API_KEY", "test-key")
	runner := NewRunner(cfg)
	sessionID := session.NewSessionID()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               sessionID,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai-compatible",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		ProviderOptions:  providerOptionsFromConfig("openai-compatible", cfg.Providers["openai-compatible"]),
	}
	if err := runner.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_approval", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := runner.store.CreatePlanMode(sessionID, session.PlanModeDraft{Enabled: true, Objective: "Retry corrupt history", Source: session.PlanModeSourceCLI}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	if _, err := runner.store.SubmitPlanMode(sessionID, session.PlanModeSubmitInput{
		Title:        "Plan",
		Summary:      "Plan summary",
		PlanMarkdown: "# Plan\n\nDo it.",
		Verification: []string{"manual"},
		Source:       session.PlanModeSourceTool,
	}); err != nil {
		t.Fatalf("submit plan mode: %v", err)
	}

	revisionMessage := "Use the recovery plan."
	blockRuntimeMessagesPath(t, runner.store, sessionID)
	if result, err := runner.Continue(context.Background(), ContinueRequest{SessionID: sessionID, Message: revisionMessage, Source: session.PlanModeSourceCLI}); err == nil {
		t.Fatalf("expected revision replay message append error, got result=%#v", result)
	}
	if calls := providerCalls.Load(); calls != 0 {
		t.Fatalf("provider should not run while revision replay message is missing, got %d calls", calls)
	}

	messagesPath := filepath.Join(runner.store.SessionDir(sessionID), "messages.jsonl")
	if err := os.RemoveAll(messagesPath); err != nil {
		t.Fatalf("remove blocked messages path: %v", err)
	}
	historyPath := filepath.Join(runner.store.SessionDir(sessionID), "artifacts", "planmode-history.jsonl")
	if err := os.WriteFile(historyPath, []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatalf("corrupt plan mode history: %v", err)
	}

	result, err := runner.Continue(context.Background(), ContinueRequest{SessionID: sessionID, Message: revisionMessage, Source: session.PlanModeSourceCLI})
	if err == nil || !strings.Contains(err.Error(), "planmode-history.jsonl") {
		t.Fatalf("expected corrupt plan mode history error, got result=%#v err=%v", result, err)
	}
	if calls := providerCalls.Load(); calls != 0 {
		t.Fatalf("provider must not run while planmode-history.jsonl is corrupt, got %d calls", calls)
	}
	messages, loadErr := runner.store.LoadMessages(sessionID)
	if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
		t.Fatalf("load messages after corrupt history retry: %v", loadErr)
	}
	for _, msg := range messages {
		if msg.Role == "user" && msg.Text == revisionMessage {
			t.Fatalf("corrupt plan mode history should fail before appending retry message, got %#v", messages)
		}
	}
}

func TestContinueMessageReportsCorruptPlanModeSnapshot(t *testing.T) {
	var providerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_corrupt_planmode",
			"status":"completed",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"should not run"}]}],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	cfg.DefaultProvider = "openai-compatible"
	cfg.Providers["openai-compatible"] = config.Provider{
		APIProvider:       "openai-compatible",
		APIKeyEnv:         "OPENAI_API_KEY",
		BaseURL:           server.URL + "/v1",
		Model:             "gpt-5.4",
		TimeoutSec:        3,
		RequestTimeoutSec: 3,
		WireAPI:           "responses",
		Retry:             config.Retry{MaxAttempts: 1},
	}
	t.Setenv("OPENAI_API_KEY", "test-key")
	runner := NewRunner(cfg)
	sessionID := session.NewSessionID()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               sessionID,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai-compatible",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		ProviderOptions:  providerOptionsFromConfig("openai-compatible", cfg.Providers["openai-compatible"]),
	}
	if err := runner.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_approval", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	planModePath := filepath.Join(runner.store.SessionDir(sessionID), "planmode.json")
	if err := os.WriteFile(planModePath, []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatalf("write corrupt planmode: %v", err)
	}

	result, err := runner.Continue(context.Background(), ContinueRequest{SessionID: sessionID, Message: "revise the plan", Source: session.PlanModeSourceCLI})
	if err == nil || !strings.Contains(err.Error(), "planmode.json") {
		t.Fatalf("expected corrupt planmode error, got result=%#v err=%v", result, err)
	}
	if calls := providerCalls.Load(); calls != 0 {
		t.Fatalf("provider must not run while planmode.json is corrupt, got %d calls", calls)
	}
	messages, loadErr := runner.store.LoadMessages(sessionID)
	if loadErr != nil {
		t.Fatalf("load messages: %v", loadErr)
	}
	for _, msg := range messages {
		if msg.Role == "user" && msg.Text == "revise the plan" {
			t.Fatalf("corrupt planmode should fail before appending continuation message, got %#v", messages)
		}
	}
}

func TestActivePlanInputDeliveryClaimsWaiterBeforeSend(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	runner := NewRunner(cfg)
	sessionID := session.NewSessionID()
	requestID := "pmq_duplicate"
	key := planInputWaiterKey(sessionID, requestID)

	t.Run("answer", func(t *testing.T) {
		ch := make(chan planInputResponse, 1)
		runner.planInputMu.Lock()
		runner.planInputWaiters[key] = ch
		runner.planInputMu.Unlock()
		answers := []session.PlanModeInputAnswer{{
			QuestionID: "scope_choice",
			Label:      "Narrow (Recommended)",
			Value:      "Narrow (Recommended)",
		}}

		if !runner.AnswerActivePlanInput(sessionID, requestID, answers) {
			t.Fatal("expected first active plan input answer to be delivered")
		}
		assertPlanInputDuplicateReturnsFalse(t, func() bool {
			return runner.AnswerActivePlanInput(sessionID, requestID, answers)
		})

		select {
		case response := <-ch:
			if response.err != nil {
				t.Fatalf("unexpected response error: %v", response.err)
			}
			if len(response.answers) != 1 || response.answers[0].QuestionID != "scope_choice" {
				t.Fatalf("unexpected delivered answers: %#v", response.answers)
			}
		default:
			t.Fatal("expected first answer response to remain available")
		}
	})

	t.Run("cancel", func(t *testing.T) {
		ch := make(chan planInputResponse, 1)
		runner.planInputMu.Lock()
		runner.planInputWaiters[key] = ch
		runner.planInputMu.Unlock()

		if !runner.CancelActivePlanInput(sessionID, requestID) {
			t.Fatal("expected first active plan input cancel to be delivered")
		}
		assertPlanInputDuplicateReturnsFalse(t, func() bool {
			return runner.CancelActivePlanInput(sessionID, requestID)
		})

		select {
		case response := <-ch:
			if !errors.Is(response.err, tools.ErrPlanInputCancelled) {
				t.Fatalf("expected cancel error, got %#v", response)
			}
		default:
			t.Fatal("expected first cancel response to remain available")
		}
	})
}

func assertPlanInputDuplicateReturnsFalse(t *testing.T, call func() bool) {
	t.Helper()
	done := make(chan bool, 1)
	go func() {
		done <- call()
	}()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("duplicate active plan input delivery should return false")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("duplicate active plan input delivery blocked on a full waiter channel")
	}
}

func blockRuntimePlanModeHistoryPath(t *testing.T, store *session.Store, sessionID string) {
	t.Helper()
	historyPath := filepath.Join(store.SessionDir(sessionID), "artifacts", "planmode-history.jsonl")
	if err := os.Remove(historyPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove plan mode history: %v", err)
	}
	if err := os.Mkdir(historyPath, 0o700); err != nil {
		t.Fatalf("block plan mode history path: %v", err)
	}
}

func blockRuntimeEventsPath(t *testing.T, store *session.Store, sessionID string) {
	t.Helper()
	eventsPath := filepath.Join(store.SessionDir(sessionID), "events.jsonl")
	if err := os.Remove(eventsPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove events: %v", err)
	}
	if err := os.Mkdir(eventsPath, 0o700); err != nil {
		t.Fatalf("block events path: %v", err)
	}
}

func blockRuntimeMessagesPath(t *testing.T, store *session.Store, sessionID string) {
	t.Helper()
	messagesPath := filepath.Join(store.SessionDir(sessionID), "messages.jsonl")
	if err := os.Remove(messagesPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove messages: %v", err)
	}
	if err := os.Mkdir(messagesPath, 0o700); err != nil {
		t.Fatalf("block messages path: %v", err)
	}
}

func hasRuntimePlanModeHistoryType(items []session.PlanModeHistoryEntry, target string) bool {
	for _, item := range items {
		if item.Type == target {
			return true
		}
	}
	return false
}

func countRuntimePlanModeHistoryType(items []session.PlanModeHistoryEntry, target string) int {
	var count int
	for _, item := range items {
		if item.Type == target {
			count++
		}
	}
	return count
}

func countRuntimeEventType(items []events.Event, target string) int {
	var count int
	for _, item := range items {
		if item.Type == target {
			count++
		}
	}
	return count
}

func countRuntimeEventWithReason(items []events.Event, target, reason string) int {
	var count int
	for _, item := range items {
		if item.Type == target && item.Data["reason"] == reason {
			count++
		}
	}
	return count
}

func countRuntimeToolResults(messages []session.Message, toolCallID, name string) int {
	var count int
	for _, msg := range messages {
		for _, result := range msg.ToolResults {
			if result.ToolCallID == toolCallID && result.Name == name {
				count++
			}
		}
	}
	return count
}

func countRuntimeGoalHistoryType(items []session.GoalHistoryEntry, target string) int {
	var count int
	for _, item := range items {
		if item.Type == target {
			count++
		}
	}
	return count
}

func countPlanModeApprovalMessages(messages []session.Message) int {
	var count int
	for _, msg := range messages {
		if msg.Role == "user" && msg.Meta["source"] == "planmode_approval" {
			count++
		}
	}
	return count
}

func countPlanModeRevisionMessages(messages []session.Message) int {
	var count int
	for _, msg := range messages {
		if msg.Role == "user" && msg.Meta["source"] == "planmode_revision" {
			count++
		}
	}
	return count
}

func TestApproveLinkedPlanModeMarksMissionPlanApproved(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	runner := NewRunner(cfg)
	sessionID := session.NewSessionID()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               sessionID,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	if err := runner.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_approval"}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	goal, err := runner.store.CreateGoal(sessionID, session.GoalDraft{
		Enabled:             true,
		Mode:                session.GoalModeMission,
		Objective:           "Approve linked mission plan",
		RequirePlanApproval: true,
		Source:              session.GoalSourceCLI,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	planMode, created, err := runner.store.EnsurePlanModeForGoal(sessionID, goal, session.PlanModeSourceCLI)
	if err != nil {
		t.Fatalf("ensure plan mode: %v", err)
	}
	if !created {
		t.Fatalf("expected linked plan mode creation")
	}
	if _, err := runner.store.SubmitPlanMode(sessionID, session.PlanModeSubmitInput{
		Title:        "Plan",
		Summary:      "Plan summary",
		PlanMarkdown: "# Plan\n\nDo it.\n\n# Verification\n\nRun tests.",
		Verification: []string{"go test ./internal/runtime"},
		Source:       session.PlanModeSourceTool,
	}); err != nil {
		t.Fatalf("submit plan mode: %v", err)
	}
	approved, err := runner.store.ApprovePlanMode(sessionID, session.PlanModeSourceCLI)
	if err != nil {
		t.Fatalf("approve plan mode: %v", err)
	}
	executing, err := runner.store.MarkPlanModeExecuting(sessionID, session.PlanModeSourceCLI)
	if err != nil {
		t.Fatalf("mark executing: %v", err)
	}
	if executing.PlanModeID != planMode.PlanModeID || executing.ApprovedVersion != approved.ApprovedVersion {
		t.Fatalf("unexpected executing plan mode: %#v approved=%#v", executing, approved)
	}
	if err := runner.approveLinkedMissionPlan(sessionID, executing, session.PlanModeSourceCLI, false); err != nil {
		t.Fatalf("approve linked mission plan: %v", err)
	}
	loaded, err := runner.store.LoadGoal(sessionID)
	if err != nil {
		t.Fatalf("load goal: %v", err)
	}
	if loaded.Mission == nil || loaded.Mission.PlanStatus != "approved" || loaded.Mission.ApprovedAt == "" {
		t.Fatalf("expected mission plan approval synced from plan mode, got %#v", loaded.Mission)
	}
}

func TestApproveLinkedMissionPlanReportsEventAppendError(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	runner := NewRunner(cfg)
	sessionID := session.NewSessionID()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               sessionID,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	if err := runner.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_approval"}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	goal, err := runner.store.CreateGoal(sessionID, session.GoalDraft{
		Enabled:             true,
		Mode:                session.GoalModeMission,
		Objective:           "Approve linked mission plan with required event",
		RequirePlanApproval: true,
		Source:              session.GoalSourceCLI,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	planMode, _, err := runner.store.EnsurePlanModeForGoal(sessionID, goal, session.PlanModeSourceCLI)
	if err != nil {
		t.Fatalf("ensure plan mode: %v", err)
	}
	if _, err := runner.store.SubmitPlanMode(sessionID, session.PlanModeSubmitInput{
		Title:        "Plan",
		Summary:      "Plan summary",
		PlanMarkdown: "# Plan\n\nDo it.",
		Verification: []string{"manual"},
		Source:       session.PlanModeSourceTool,
	}); err != nil {
		t.Fatalf("submit plan mode: %v", err)
	}
	if _, err := runner.store.ApprovePlanMode(sessionID, session.PlanModeSourceCLI); err != nil {
		t.Fatalf("approve plan mode: %v", err)
	}
	executing, err := runner.store.MarkPlanModeExecuting(sessionID, session.PlanModeSourceCLI)
	if err != nil {
		t.Fatalf("mark executing: %v", err)
	}
	if executing.PlanModeID != planMode.PlanModeID {
		t.Fatalf("unexpected executing plan mode: %#v", executing)
	}
	eventsPath := filepath.Join(runner.store.SessionDir(sessionID), "events.jsonl")
	if err := os.Remove(eventsPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove events: %v", err)
	}
	if err := os.Mkdir(eventsPath, 0o700); err != nil {
		t.Fatalf("block events: %v", err)
	}

	err = runner.approveLinkedMissionPlan(sessionID, executing, session.PlanModeSourceCLI, false)
	if err == nil || !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected mission approval event append error, got %v", err)
	}
	loaded, loadErr := runner.store.LoadGoal(sessionID)
	if loadErr != nil {
		t.Fatalf("load goal: %v", loadErr)
	}
	if loaded.Mission == nil || loaded.Mission.PlanStatus != session.MissionPlanStatusApproved || loaded.Mission.ApprovedAt == "" {
		t.Fatalf("mission approval history-backed snapshot should remain approved, got %#v", loaded.Mission)
	}
}

func TestApproveLinkedMissionPlanRetryAfterEventFailureDoesNotDuplicateHistory(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	runner := NewRunner(cfg)
	sessionID := session.NewSessionID()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               sessionID,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	if err := runner.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_approval"}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	goal, err := runner.store.CreateGoal(sessionID, session.GoalDraft{
		Enabled:             true,
		Mode:                session.GoalModeMission,
		Objective:           "Retry linked mission approval event",
		RequirePlanApproval: true,
		Source:              session.GoalSourceCLI,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	planMode, _, err := runner.store.EnsurePlanModeForGoal(sessionID, goal, session.PlanModeSourceCLI)
	if err != nil {
		t.Fatalf("ensure plan mode: %v", err)
	}
	if _, err := runner.store.SubmitPlanMode(sessionID, session.PlanModeSubmitInput{
		Title:        "Plan",
		Summary:      "Plan summary",
		PlanMarkdown: "# Plan\n\nDo it.",
		Verification: []string{"manual"},
		Source:       session.PlanModeSourceTool,
	}); err != nil {
		t.Fatalf("submit plan mode: %v", err)
	}
	if _, err := runner.store.ApprovePlanMode(sessionID, session.PlanModeSourceCLI); err != nil {
		t.Fatalf("approve plan mode: %v", err)
	}
	executing, err := runner.store.MarkPlanModeExecuting(sessionID, session.PlanModeSourceCLI)
	if err != nil {
		t.Fatalf("mark executing: %v", err)
	}
	if executing.PlanModeID != planMode.PlanModeID {
		t.Fatalf("unexpected executing plan mode: %#v", executing)
	}
	blockRuntimeEventsPath(t, runner.store, sessionID)

	err = runner.approveLinkedMissionPlan(sessionID, executing, session.PlanModeSourceCLI, false)
	if err == nil || !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected mission approval event append error, got %v", err)
	}
	history, err := runner.store.LoadGoalHistory(sessionID)
	if err != nil {
		t.Fatalf("load goal history after failed event append: %v", err)
	}
	if count := countRuntimeGoalHistoryType(history, "mission.plan.approved"); count != 1 {
		t.Fatalf("expected one mission approval history row after failed event append, got %d history=%#v", count, history)
	}

	eventsPath := filepath.Join(runner.store.SessionDir(sessionID), "events.jsonl")
	if err := os.RemoveAll(eventsPath); err != nil {
		t.Fatalf("remove blocked events path: %v", err)
	}
	if err := runner.approveLinkedMissionPlan(sessionID, executing, session.PlanModeSourceCLI, false); err != nil {
		t.Fatalf("retry linked mission approval event: %v", err)
	}
	history, err = runner.store.LoadGoalHistory(sessionID)
	if err != nil {
		t.Fatalf("load goal history after retry: %v", err)
	}
	if count := countRuntimeGoalHistoryType(history, "mission.plan.approved"); count != 1 {
		t.Fatalf("retry should not duplicate mission approval history, got %d history=%#v", count, history)
	}
	events, err := runner.store.LoadEvents(sessionID)
	if err != nil {
		t.Fatalf("load events after retry: %v", err)
	}
	if count := countRuntimeEventType(events, "mission.plan.approved"); count != 1 {
		t.Fatalf("retry should append one mission approval event, got %d events=%#v", count, events)
	}
}

func TestApproveLinkedMissionPlanRetryReportsCorruptGoalHistory(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	runner := NewRunner(cfg)
	sessionID := session.NewSessionID()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               sessionID,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	if err := runner.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_approval"}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	goal, err := runner.store.CreateGoal(sessionID, session.GoalDraft{
		Enabled:             true,
		Mode:                session.GoalModeMission,
		Objective:           "Retry linked mission approval corrupt history",
		RequirePlanApproval: true,
		Source:              session.GoalSourceCLI,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	planMode, _, err := runner.store.EnsurePlanModeForGoal(sessionID, goal, session.PlanModeSourceCLI)
	if err != nil {
		t.Fatalf("ensure plan mode: %v", err)
	}
	if _, err := runner.store.SubmitPlanMode(sessionID, session.PlanModeSubmitInput{
		Title:        "Plan",
		Summary:      "Plan summary",
		PlanMarkdown: "# Plan\n\nDo it.",
		Verification: []string{"manual"},
		Source:       session.PlanModeSourceTool,
	}); err != nil {
		t.Fatalf("submit plan mode: %v", err)
	}
	if _, err := runner.store.ApprovePlanMode(sessionID, session.PlanModeSourceCLI); err != nil {
		t.Fatalf("approve plan mode: %v", err)
	}
	executing, err := runner.store.MarkPlanModeExecuting(sessionID, session.PlanModeSourceCLI)
	if err != nil {
		t.Fatalf("mark executing: %v", err)
	}
	if executing.PlanModeID != planMode.PlanModeID {
		t.Fatalf("unexpected executing plan mode: %#v", executing)
	}
	blockRuntimeEventsPath(t, runner.store, sessionID)

	err = runner.approveLinkedMissionPlan(sessionID, executing, session.PlanModeSourceCLI, false)
	if err == nil || !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected mission approval event append error, got %v", err)
	}

	eventsPath := filepath.Join(runner.store.SessionDir(sessionID), "events.jsonl")
	if err := os.RemoveAll(eventsPath); err != nil {
		t.Fatalf("remove blocked events path: %v", err)
	}
	historyPath := filepath.Join(runner.store.SessionDir(sessionID), "artifacts", "goal-history.jsonl")
	if err := os.WriteFile(historyPath, []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatalf("corrupt goal history: %v", err)
	}

	err = runner.approveLinkedMissionPlan(sessionID, executing, session.PlanModeSourceCLI, false)
	if err == nil || !strings.Contains(err.Error(), "goal-history.jsonl") {
		t.Fatalf("expected corrupt goal history error, got %v", err)
	}
	events, loadErr := runner.store.LoadEvents(sessionID)
	if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
		t.Fatalf("load events after corrupt history retry: %v", loadErr)
	}
	if count := countRuntimeEventType(events, "mission.plan.approved"); count != 0 {
		t.Fatalf("corrupt goal history should fail before appending mission approval event, got %d events=%#v", count, events)
	}
}

func TestApproveLinkedPlanModeBlocksUncoveredMissionValidation(t *testing.T) {
	cfg := config.Default()
	runner := NewRunner(cfg)
	sessionID := session.NewSessionID()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               sessionID,
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	if err := runner.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_approval", UpdatedAt: now}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	goal, err := runner.store.CreateGoal(sessionID, session.GoalDraft{
		Enabled:             true,
		Mode:                session.GoalModeMission,
		Objective:           "Approve only covered plans",
		ValidationPlan:      []string{"go test ./internal/runtime"},
		Features:            []string{"runtime change"},
		RequirePlanApproval: true,
		Source:              session.GoalSourceCLI,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	planMode, _, err := runner.store.EnsurePlanModeForGoal(sessionID, goal, session.PlanModeSourceCLI)
	if err != nil {
		t.Fatalf("ensure plan mode: %v", err)
	}
	if _, err := runner.store.SubmitPlanMode(sessionID, session.PlanModeSubmitInput{
		Title:        "Plan",
		Summary:      "Do the work.",
		PlanMarkdown: "# Plan\n\nDo the work.",
		Verification: []string{"go test ./internal/runtime"},
		Source:       session.PlanModeSourceTool,
	}); err != nil {
		t.Fatalf("submit plan: %v", err)
	}
	if err := runner.checkPlanModeGoalCoverage(sessionID, false); err == nil || !strings.Contains(err.Error(), "coverage blocks approval") {
		t.Fatalf("expected coverage approval block, got %v", err)
	}
	loaded, err := runner.store.LoadGoal(sessionID)
	if err != nil {
		t.Fatalf("load goal: %v", err)
	}
	loaded.Mission.Features[0].ClaimedAssertions = []string{"validation_0001"}
	loaded.Mission.Milestones = []session.MissionMilestone{{ID: "milestone_0001", Title: "validation", Status: "pending", ValidationIDs: []string{"validation_0001"}}}
	if err := runner.store.SaveGoal(sessionID, loaded); err != nil {
		t.Fatalf("save goal: %v", err)
	}
	approved, err := runner.store.ApprovePlanMode(sessionID, session.PlanModeSourceCLI)
	if err != nil {
		t.Fatalf("approve plan mode: %v", err)
	}
	executing, err := runner.store.MarkPlanModeExecuting(sessionID, session.PlanModeSourceCLI)
	if err != nil {
		t.Fatalf("mark executing: %v", err)
	}
	if executing.PlanModeID != planMode.PlanModeID || approved.ApprovedVersion == 0 {
		t.Fatalf("unexpected plan mode state: approved=%#v executing=%#v", approved, executing)
	}
	if err := runner.approveLinkedMissionPlan(sessionID, executing, session.PlanModeSourceCLI, false); err != nil {
		t.Fatalf("approve linked mission plan after coverage fix: %v", err)
	}
}

func hasProviderTool(tools []provider.ToolSchema, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}
