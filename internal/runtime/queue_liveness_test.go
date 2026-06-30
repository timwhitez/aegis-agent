package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/hooks"
	"go-cli-agent/internal/provider"
	"go-cli-agent/internal/session"
	"go-cli-agent/internal/skills"
	"go-cli-agent/internal/tools"
)

func budgetNoopTool() tools.Definition {
	return tools.Definition{
		Name:        "noop",
		Description: "noop",
		InputSchema: map[string]any{"type": "object"},
		Execute: func(context.Context, tools.ExecContext, json.RawMessage) (session.ToolResult, error) {
			return session.ToolResult{LLMOutput: "ok"}, nil
		},
	}
}

// childEngineWithConfig builds an engine and a child-linked session metadata so
// child-budget enforcement (which only applies to sessions with a parent) is
// active. It returns the run dependencies bound to that engine.
func childEngineWithConfig(t *testing.T, cfg *config.Config) (*Engine, session.SessionMetadata, session.State, *tools.Registry, *hooks.Manager, *skills.Catalog) {
	t.Helper()
	engine, parent, _, registry, hookManager, catalog := newTestEngineWithConfig(t, cfg, session.ModeExec)
	childMeta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "fake",
		Model:            "fake",
		ParentSessionID:  parent.ID,
		RootSessionID:    parent.ID,
		Depth:            1,
		CompletionPolicy: session.CompletionPolicyAutonomous,
	}
	state := session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := engine.store.Create(childMeta, state); err != nil {
		t.Fatalf("create child session: %v", err)
	}
	return engine, childMeta, state, registry, hookManager, catalog
}

func TestEngineChildBudgetTurnsExceededPausesChild(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.GuardrailsMode = "standard"
	cfg.Runtime.MaxTurnsHard = -1
	cfg.Runtime.ChildBudget.MaxTurns = 1
	cfg.Runtime.ChildBudget.MaxWallClockSec = 0
	engine, childMeta, state, registry, hookManager, catalog := childEngineWithConfig(t, cfg)

	if err := engine.store.AppendMessage(childMeta.ID, session.NewMessage("user", "go")); err != nil {
		t.Fatalf("append: %v", err)
	}
	// Provider keeps asking for a no-op tool so the loop would never stop on its own.
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{
			ToolCalls:  []provider.ToolCall{{ID: "call_x", Name: "noop", Arguments: json.RawMessage(`{}`)}},
			StopReason: "tool_use",
		}, nil
	})
	registry.Register(budgetNoopTool())

	result, err := engine.Run(context.Background(), childMeta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusPaused {
		t.Fatalf("expected child paused on budget, got %s", result.Status)
	}
	persisted, err := engine.store.LoadState(childMeta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if persisted.PauseReason != "child_budget_turns_exceeded" {
		t.Fatalf("expected child_budget_turns_exceeded, got %q", persisted.PauseReason)
	}
	eventsList, err := loadEvents(engine.store, childMeta.ID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if !hasEventType(eventsList, "session.child_budget.exceeded") {
		t.Fatalf("expected session.child_budget.exceeded event")
	}
}

func TestEngineChildBudgetWallClockExceededPausesChild(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.GuardrailsMode = "standard"
	cfg.Runtime.MaxTurnsHard = -1
	cfg.Runtime.ChildBudget.MaxTurns = 0
	cfg.Runtime.ChildBudget.MaxWallClockSec = 3600
	engine, childMeta, state, registry, hookManager, catalog := childEngineWithConfig(t, cfg)

	// Backdate creation so the wall-clock budget is already exceeded at run start.
	childMeta.CreatedAt = time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano)

	if err := engine.store.AppendMessage(childMeta.ID, session.NewMessage("user", "go")); err != nil {
		t.Fatalf("append: %v", err)
	}
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{Text: "should not get here", StopReason: "done_candidate"}, nil
	})

	result, err := engine.Run(context.Background(), childMeta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusPaused {
		t.Fatalf("expected child paused on wall-clock budget, got %s", result.Status)
	}
	persisted, err := engine.store.LoadState(childMeta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if persisted.PauseReason != "child_budget_wallclock_exceeded" {
		t.Fatalf("expected child_budget_wallclock_exceeded, got %q", persisted.PauseReason)
	}
}

func TestEngineRootSessionIgnoresChildBudget(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.GuardrailsMode = "standard"
	cfg.Runtime.MaxTurnsHard = -1
	cfg.Runtime.ChildBudget.MaxTurns = 1
	engine, rootMeta, state, registry, hookManager, catalog := newTestEngineWithConfig(t, cfg, session.ModeRun)
	// rootMeta has no ParentSessionID, so the child budget must not apply.
	if err := engine.store.AppendMessage(rootMeta.ID, session.NewMessage("user", "go")); err != nil {
		t.Fatalf("append: %v", err)
	}
	turns := 0
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		turns++
		if turns >= 3 {
			return provider.TurnResult{Text: "done", StopReason: "done_candidate"}, nil
		}
		return provider.TurnResult{
			ToolCalls:  []provider.ToolCall{{ID: "call_x", Name: "noop", Arguments: json.RawMessage(`{}`)}},
			StopReason: "tool_use",
		}, nil
	})
	registry.Register(budgetNoopTool())

	result, err := engine.Run(context.Background(), rootMeta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status == session.StatusPaused {
		t.Fatalf("root session should not pause on child budget")
	}
	persisted, err := engine.store.LoadState(rootMeta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if persisted.PauseReason == "child_budget_turns_exceeded" {
		t.Fatalf("root session must not carry child budget pause reason")
	}
}

func TestCoordinationDeadlockReasonDetectsStalledJobs(t *testing.T) {
	engine, parent, _, _, hookManager, _ := newTestEngine(t, session.ModeRun)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// Blocked job whose owner process is dead -> cannot progress.
	job := session.QueueJob{
		SchemaVersion:   1,
		ID:              "job_stalled",
		CreatedAt:       now,
		UpdatedAt:       now,
		Status:          session.QueueStatusBlocked,
		ParentSessionID: parent.ID,
		RootSessionID:   parent.ID,
		AgentRole:       "generator",
		Prompt:          "audit",
		Mode:            session.ModeExec,
		Background:      true,
		SessionID:       "child_stalled",
		SessionStatus:   session.StatusPaused,
		WorkerPID:       999999,
		ProcessStartID:  "999999:" + now,
		LastError:       "child session is resumable: paused",
	}
	if err := engine.store.SaveJob(job); err != nil {
		t.Fatalf("save job: %v", err)
	}
	if err := addParentQueueJob(engine.store, parent.ID, job.ID, parentWaitAll); err != nil {
		t.Fatalf("seed coordination: %v", err)
	}

	reason, deadlocked, err := coordinationDeadlockReason(engine.store, parent.ID)
	if err != nil {
		t.Fatalf("deadlock reason: %v", err)
	}
	if !deadlocked || reason == "" {
		t.Fatalf("expected deadlock detected, got deadlocked=%v reason=%q", deadlocked, reason)
	}

	injected, alreadyDelivered, _, err := engine.injectCoordinationDeadlockWake(parent)
	if err != nil {
		t.Fatalf("inject wake: %v", err)
	}
	if !injected || alreadyDelivered {
		t.Fatalf("expected deadlock wake injected, injected=%v alreadyDelivered=%v", injected, alreadyDelivered)
	}
	notifications, err := engine.store.LoadBackgroundNotifications(parent.ID)
	if err != nil {
		t.Fatalf("load notifications: %v", err)
	}
	pending := false
	for _, n := range notifications {
		if n.Source == session.BackgroundSourceCoordinationDeadlock && n.DeliveryStatus == session.BackgroundNotificationPending {
			pending = true
		}
	}
	if !pending {
		t.Fatalf("expected pending coordination-deadlock notification, got %#v", notifications)
	}
	// Idempotent: a second inject while one is pending must not duplicate.
	injectedAgain, deliveredAgain, _, err := engine.injectCoordinationDeadlockWake(parent)
	if err != nil {
		t.Fatalf("second inject: %v", err)
	}
	if injectedAgain || deliveredAgain {
		t.Fatalf("expected no duplicate wake while one is pending, injected=%v delivered=%v", injectedAgain, deliveredAgain)
	}
	notifications[0].DeliveryStatus = session.BackgroundNotificationAccepted
	if err := engine.store.UpdateBackgroundNotifications(parent.ID, notifications); err != nil {
		t.Fatalf("accept deadlock notification: %v", err)
	}
	injectedAfterAccept, deliveredAfterAccept, _, err := engine.injectCoordinationDeadlockWake(parent)
	if err != nil {
		t.Fatalf("inject after accepted wake: %v", err)
	}
	if injectedAfterAccept || !deliveredAfterAccept {
		t.Fatalf("expected accepted wake to suppress duplicate, injected=%v delivered=%v", injectedAfterAccept, deliveredAfterAccept)
	}
	count, err := engine.waitForBackgroundResult(context.Background(), parent, hookManager)
	if err != nil {
		t.Fatalf("wait after accepted deadlock wake: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected wait to return without draining duplicate notifications, got %d", count)
	}
	notifications, err = engine.store.LoadBackgroundNotifications(parent.ID)
	if err != nil {
		t.Fatalf("reload notifications: %v", err)
	}
	if len(notifications) != 1 || notifications[0].DeliveryStatus != session.BackgroundNotificationAccepted {
		t.Fatalf("expected exactly one accepted deadlock notification, got %#v", notifications)
	}
	eventsList, err := loadEvents(engine.store, parent.ID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if !hasEventType(eventsList, "session.background.deadlock_already_notified") {
		t.Fatalf("expected session.background.deadlock_already_notified event")
	}
}

func TestEngineBeforeAwaitingBackgroundAlreadyDeliveredDeadlockNeedsIntervention(t *testing.T) {
	engine, parent, _, _, _, _ := newTestEngine(t, session.ModeRun)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	child := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_already_delivered_deadlock",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyAutonomous,
		ParentSessionID:  parent.ID,
		RootSessionID:    parent.ID,
		QueueJobID:       "job_already_delivered_deadlock",
		Depth:            1,
	}
	if err := engine.store.Create(child, session.State{Status: session.StatusPaused, Phase: "interrupt", UpdatedAt: now}); err != nil {
		t.Fatalf("create child: %v", err)
	}
	job := session.QueueJob{
		SchemaVersion:   1,
		ID:              child.QueueJobID,
		CreatedAt:       now,
		UpdatedAt:       now,
		Status:          session.QueueStatusBlocked,
		ParentSessionID: parent.ID,
		RootSessionID:   parent.ID,
		AgentRole:       "generator",
		Prompt:          "audit",
		Mode:            session.ModeExec,
		Background:      true,
		SessionID:       child.ID,
		SessionStatus:   session.StatusPaused,
		WorkerPID:       999999,
		ProcessStartID:  "999999:" + now,
		LastError:       "child session is resumable: paused",
	}
	if err := engine.store.SaveJob(job); err != nil {
		t.Fatalf("save job: %v", err)
	}
	if err := addParentQueueJob(engine.store, parent.ID, job.ID, parentWaitAll); err != nil {
		t.Fatalf("seed coordination: %v", err)
	}
	reason, deadlocked, err := coordinationDeadlockReason(engine.store, parent.ID)
	if err != nil {
		t.Fatalf("deadlock reason: %v", err)
	}
	if !deadlocked {
		t.Fatal("expected seeded coordination to be deadlocked")
	}
	accepted := session.NewCoordinationDeadlockNotification(parent.ID, reason)
	accepted.DeliveryStatus = session.BackgroundNotificationAccepted
	if err := engine.store.AppendBackgroundNotification(parent.ID, accepted); err != nil {
		t.Fatalf("append accepted deadlock notification: %v", err)
	}
	notificationsBefore, err := engine.store.LoadBackgroundNotifications(parent.ID)
	if err != nil {
		t.Fatalf("load notifications before decision: %v", err)
	}
	for i := range notificationsBefore {
		notificationsBefore[i].DeliveryStatus = session.BackgroundNotificationAccepted
	}
	if err := engine.store.UpdateBackgroundNotifications(parent.ID, notificationsBefore); err != nil {
		t.Fatalf("accept existing notifications: %v", err)
	}

	decision, err := engine.beforeAwaitingBackground(parent)
	if err != nil {
		t.Fatalf("before background wait: %v", err)
	}
	if !decision.AlreadyDeliveredDeadlock || decision.Reason != reason {
		t.Fatalf("expected already-delivered deadlock intervention decision, got %#v want reason %q", decision, reason)
	}
	if prompt := backgroundWaitNeedsInterventionPrompt(decision.Reason); !strings.Contains(prompt, "agent_wait cannot make progress") || !strings.Contains(prompt, job.ID) {
		t.Fatalf("expected intervention prompt to include wait guidance and job id, got %q", prompt)
	}
	notifications, err := engine.store.LoadBackgroundNotifications(parent.ID)
	if err != nil {
		t.Fatalf("load notifications: %v", err)
	}
	if len(notifications) != len(notificationsBefore) {
		t.Fatalf("expected no duplicate deadlock notification, got %#v", notifications)
	}
	for _, notification := range notifications {
		if notification.DeliveryStatus != session.BackgroundNotificationAccepted {
			t.Fatalf("expected all notifications to remain accepted, got %#v", notifications)
		}
	}
	deadlockCount := 0
	for _, notification := range notifications {
		if notification.Source == session.BackgroundSourceCoordinationDeadlock {
			deadlockCount++
		}
	}
	if deadlockCount != 1 {
		t.Fatalf("expected no duplicate deadlock notification, got %#v", notifications)
	}
}

func TestCoordinationDeadlockReasonTreatsMissingUnresolvedJobAsStalled(t *testing.T) {
	engine, parent, _, _, _, _ := newTestEngine(t, session.ModeRun)
	if err := addParentQueueJob(engine.store, parent.ID, "job_missing_unresolved", parentWaitAll); err != nil {
		t.Fatalf("seed coordination: %v", err)
	}
	reason, deadlocked, err := coordinationDeadlockReason(engine.store, parent.ID)
	if err != nil {
		t.Fatalf("deadlock reason: %v", err)
	}
	if !deadlocked || !strings.Contains(reason, "job_missing_unresolved") {
		t.Fatalf("expected missing unresolved job to be reported as stalled, deadlocked=%v reason=%q", deadlocked, reason)
	}
}

func TestCoordinationDeadlockReasonIgnoresProgressableJob(t *testing.T) {
	engine, parent, _, _, _, _ := newTestEngine(t, session.ModeRun)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// Queued job can still progress -> not a deadlock.
	job := session.QueueJob{
		SchemaVersion:   1,
		ID:              "job_queued",
		CreatedAt:       now,
		UpdatedAt:       now,
		Status:          session.QueueStatusQueued,
		ParentSessionID: parent.ID,
		RootSessionID:   parent.ID,
		AgentRole:       "generator",
		Prompt:          "audit",
		Mode:            session.ModeExec,
		Background:      true,
	}
	if err := engine.store.SaveJob(job); err != nil {
		t.Fatalf("save job: %v", err)
	}
	if err := addParentQueueJob(engine.store, parent.ID, job.ID, parentWaitAll); err != nil {
		t.Fatalf("seed coordination: %v", err)
	}
	_, deadlocked, err := coordinationDeadlockReason(engine.store, parent.ID)
	if err != nil {
		t.Fatalf("deadlock reason: %v", err)
	}
	if deadlocked {
		t.Fatalf("queued job must not be treated as deadlocked")
	}
}

func TestCoordinationDeadlockReasonNoCoordinationFile(t *testing.T) {
	engine, parent, _, _, _, _ := newTestEngine(t, session.ModeRun)
	_, deadlocked, err := coordinationDeadlockReason(engine.store, parent.ID)
	if err != nil {
		t.Fatalf("expected no error when coordination file is absent, got %v", err)
	}
	if deadlocked {
		t.Fatalf("expected no deadlock without coordination")
	}
}

func TestBackgroundWaitTimeoutReturnsAndRecordsEvent(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.GuardrailsMode = "standard"
	cfg.Runtime.Queue.PollIntervalMS = 1
	cfg.Runtime.Queue.BackgroundWaitTimeoutSec = 1
	engine, parent, _, _, hookManager, _ := newTestEngineWithConfig(t, cfg, session.ModeRun)

	// No pending background notifications and no deadlock coordination -> the wait
	// must time out instead of blocking forever, and record a wait_timeout event.
	originalTimeNow := timeNow
	base := time.Now()
	calls := 0
	timeNow = func() time.Time {
		calls++
		if calls == 1 {
			return base
		}
		return base.Add(2 * time.Second)
	}
	defer func() {
		timeNow = originalTimeNow
	}()
	start := time.Now()
	count, err := engine.waitForBackgroundResult(context.Background(), parent, hookManager)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected zero drained notifications on timeout, got %d", count)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("wait took too long: %s", elapsed)
	}
	eventsList, err := loadEvents(engine.store, parent.ID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if !hasEventType(eventsList, "session.background.wait_timeout") {
		t.Fatalf("expected session.background.wait_timeout event")
	}
}
