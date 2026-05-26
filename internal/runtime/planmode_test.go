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

func TestParentLinkedQueueBlockedDuringPendingPlanMode(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	runner := NewRunner(cfg)
	parentID := session.NewSessionID()
	meta := session.SessionMetadata{
		SchemaVersion: 1,
		ID:            parentID,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:       t.TempDir(),
		Mode:          session.ModeExec,
		Provider:      "fake",
		Model:         "fake",
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
