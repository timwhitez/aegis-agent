package runtime

import (
	"context"
	"encoding/json"
	"strings"
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
