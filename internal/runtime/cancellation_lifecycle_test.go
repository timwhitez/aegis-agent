package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/hooks"
	"go-cli-agent/internal/session"
	"go-cli-agent/internal/tools"
)

func TestStopAgentCancelsRunningDirectChildProviderAndIsIdempotent(t *testing.T) {
	providerStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_, _ = io.ReadAll(r.Body)
		select {
		case <-providerStarted:
		default:
			close(providerStarted)
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	cfg := cancellationRuntimeConfig(t, server.URL)
	cfg.Runtime.MultiAgent.CancelGraceSec = 2
	runner := NewRunner(cfg)
	workdir := t.TempDir()
	parentID := createParentSession(t, runner.store, workdir)
	otherParentID := createParentSession(t, runner.store, workdir)

	type spawnOutcome struct {
		result tools.AgentSpawnResult
		err    error
	}
	spawnDone := make(chan spawnOutcome, 1)
	go func() {
		result, err := runner.SpawnAgent(context.Background(), tools.AgentSpawnRequest{
			ParentSessionID: parentID,
			Prompt:          "wait for cancellation",
			Workdir:         workdir,
			IsolationMode:   "off",
		})
		spawnDone <- spawnOutcome{result: result, err: err}
	}()

	waitForTestSignal(t, providerStarted, 5*time.Second, "provider call start")
	childID := waitForRunningChild(t, runner.store, parentID, 5*time.Second)
	if _, err := runner.StopAgent(context.Background(), tools.AgentStopRequest{
		ParentSessionID: otherParentID,
		SessionID:       childID,
	}); err == nil || !strings.Contains(err.Error(), "not linked") {
		t.Fatalf("cross-parent stop must be rejected, got %v", err)
	}
	if _, err := runner.store.LoadSessionCancel(childID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cross-parent rejection must not create cancel request, got %v", err)
	}

	stopped, err := runner.StopAgent(context.Background(), tools.AgentStopRequest{
		ParentSessionID: parentID,
		SessionID:       childID,
	})
	if err != nil {
		t.Fatalf("stop running direct child: %v", err)
	}
	if !stopped.Accepted || stopped.Status != session.StatusCancelled {
		t.Fatalf("expected synchronous cancellation outcome, got %#v", stopped)
	}
	outcome := waitForSpawnOutcome(t, spawnDone, 5*time.Second)
	if outcome.err != nil || outcome.result.SessionID != childID || outcome.result.Status != session.StatusCancelled {
		t.Fatalf("unexpected direct child outcome: result=%#v err=%v", outcome.result, outcome.err)
	}

	again, err := runner.StopAgent(context.Background(), tools.AgentStopRequest{
		ParentSessionID: parentID,
		SessionID:       childID,
	})
	if err != nil || !again.Accepted || again.Behavior != "already_cancelled" {
		t.Fatalf("repeated stop must be idempotent: result=%#v err=%v", again, err)
	}
	request, err := runner.store.LoadSessionCancel(childID)
	if err != nil || request.Status != session.CancelRequestStatusApplied || request.AppliedAt == "" {
		t.Fatalf("expected applied durable cancel request, request=%#v err=%v", request, err)
	}
	childEvents, err := runner.store.LoadEvents(childID)
	if err != nil {
		t.Fatalf("load child events: %v", err)
	}
	if countEventType(childEvents, "session.cancel_requested") != 1 || countEventType(childEvents, "session.cancelled") != 1 {
		t.Fatalf("unexpected direct child cancellation event counts: %#v", childEvents)
	}
	parentEvents, err := runner.store.LoadEvents(parentID)
	if err != nil {
		t.Fatalf("load parent events: %v", err)
	}
	if countEventType(parentEvents, "session.child.cancel_requested") != 1 || countEventType(parentEvents, "session.child.cancelled") != 1 {
		t.Fatalf("unexpected parent cancellation event counts: %#v", parentEvents)
	}
	coordination, err := runner.store.LoadParentCoordination(parentID)
	if err != nil {
		t.Fatalf("load parent coordination: %v", err)
	}
	if containsString(coordination.UnresolvedChildSessions, childID) || !containsString(coordination.CancelledChildSessions, childID) || coordination.Parked {
		t.Fatalf("direct child cancellation did not resolve parent gate: %#v", coordination)
	}
}

func TestStopAgentCancelsRunningQueueShellProcessGroup(t *testing.T) {
	workdir := t.TempDir()
	startedPath := filepath.Join(workdir, "shell-started")
	latePath := filepath.Join(workdir, "shell-late")
	command := fmt.Sprintf("printf started > %q; sleep 30; printf late > %q", startedPath, latePath)
	server := responsesToolServer(t, "shell", map[string]any{"command": command})
	defer server.Close()

	cfg := cancellationRuntimeConfig(t, server.URL)
	cfg.Runtime.MultiAgent.CancelGraceSec = 3
	runner := NewRunner(cfg)
	parentID := createParentSession(t, runner.store, workdir)
	job, err := runner.QueueSubmit(context.Background(), QueueSubmitRequest{
		ParentSessionID: parentID,
		Prompt:          "run a cancellable shell",
		Workdir:         workdir,
		IsolationMode:   "off",
	})
	if err != nil {
		t.Fatalf("queue submit: %v", err)
	}

	type processOutcome struct {
		job session.QueueJob
		ok  bool
		err error
	}
	processDone := make(chan processOutcome, 1)
	go func() {
		processed, ok, err := runner.ProcessNextJob(context.Background())
		processDone <- processOutcome{job: processed, ok: ok, err: err}
	}()
	waitForFile(t, startedPath, 5*time.Second)
	loaded, err := runner.store.LoadJob(job.ID)
	if err != nil || loaded.Status != session.QueueStatusRunning || loaded.SessionID == "" {
		t.Fatalf("expected running linked queue child, job=%#v err=%v", loaded, err)
	}
	stopped, err := runner.StopAgent(context.Background(), tools.AgentStopRequest{
		ParentSessionID: parentID,
		QueueJobID:      job.ID,
	})
	if err != nil || !stopped.Accepted || stopped.Status != session.StatusCancelled {
		t.Fatalf("stop running queue child: result=%#v err=%v", stopped, err)
	}
	var outcome processOutcome
	select {
	case outcome = <-processDone:
	case <-time.After(6 * time.Second):
		t.Fatal("queue worker did not settle after cancellation")
	}
	if outcome.err != nil || !outcome.ok || outcome.job.Status != session.QueueStatusCancelled || outcome.job.SessionStatus != session.StatusCancelled {
		t.Fatalf("unexpected queue cancellation outcome: %#v", outcome)
	}
	assertQueueLeaseCleared(t, outcome.job)
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(latePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled shell process group wrote late artifact: %v", err)
	}
	messages, err := runner.store.LoadMessages(loaded.SessionID)
	if err != nil {
		t.Fatalf("load cancelled child messages: %v", err)
	}
	var interruptedResult bool
	for _, message := range messages {
		for _, result := range message.ToolResults {
			if result.Name == "shell" && result.IsError && strings.Contains(strings.ToLower(result.LLMOutput), "interrupt") {
				interruptedResult = true
			}
		}
	}
	if !interruptedResult {
		t.Fatalf("expected replayable interrupted shell result, messages=%#v", messages)
	}
	finalJob, err := runner.store.LoadJob(job.ID)
	if err != nil || finalJob.Status != session.QueueStatusCancelled || finalJob.SessionStatus != session.StatusCancelled {
		t.Fatalf("expected durable cancelled queue job, job=%#v err=%v", finalJob, err)
	}
	assertQueueLeaseCleared(t, finalJob)
	again, err := runner.StopAgent(context.Background(), tools.AgentStopRequest{ParentSessionID: parentID, QueueJobID: job.ID})
	if err != nil || !again.Accepted || again.Behavior != "already_cancelled" {
		t.Fatalf("repeated queue stop must be idempotent: result=%#v err=%v", again, err)
	}
	parentEvents, err := runner.store.LoadEvents(parentID)
	if err != nil {
		t.Fatalf("load queue parent events: %v", err)
	}
	if countEventType(parentEvents, "session.child.cancel_requested") != 1 || countEventType(parentEvents, "queue.job.cancelled") != 1 {
		t.Fatalf("queue cancellation events must be unique: %#v", parentEvents)
	}
	coordination, err := runner.store.LoadParentCoordination(parentID)
	if err != nil {
		t.Fatalf("load parent coordination: %v", err)
	}
	if containsString(coordination.UnresolvedQueueJobs, job.ID) || !containsString(coordination.CancelledQueueJobs, job.ID) || coordination.Parked {
		t.Fatalf("queue cancellation did not resolve parent gate: %#v", coordination)
	}
}

func TestStopAgentRacingQueueResumeSettlesCancelledAndReleasesSlot(t *testing.T) {
	providerStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_, _ = io.ReadAll(r.Body)
		select {
		case <-providerStarted:
		default:
			close(providerStarted)
		}
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer server.Close()

	cfg := cancellationRuntimeConfig(t, server.URL)
	cfg.Runtime.MultiAgent.MaxActiveChildren = 1
	runner := NewRunner(cfg)
	parentID := createParentSession(t, runner.store, t.TempDir())
	now := time.Now().UTC().Format(time.RFC3339Nano)
	child := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_resume_cancel_race",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "openai-compatible",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyAutonomous,
		ParentSessionID:  parentID,
		RootSessionID:    parentID,
		QueueJobID:       "job_resume_cancel_race",
		Depth:            1,
	}
	if err := runner.store.Create(child, session.State{Status: session.StatusPaused, Phase: "interrupt", PauseReason: "manual_stop", UpdatedAt: now}); err != nil {
		t.Fatalf("create paused child: %v", err)
	}
	job := session.QueueJob{
		SchemaVersion:   1,
		ID:              child.QueueJobID,
		CreatedAt:       now,
		UpdatedAt:       now,
		Status:          session.QueueStatusBlocked,
		ParentSessionID: parentID,
		RootSessionID:   parentID,
		Prompt:          "resume then cancel",
		Mode:            session.ModeExec,
		Background:      true,
		SessionID:       child.ID,
		SessionStatus:   session.StatusPaused,
		LastError:       "child session is resumable: paused",
	}
	if err := runner.store.SaveJob(job); err != nil {
		t.Fatalf("save blocked job: %v", err)
	}
	if err := addParentQueueJob(runner.store, parentID, job.ID, parentWaitAll); err != nil {
		t.Fatalf("link blocked job: %v", err)
	}
	type promptOutcome struct {
		result tools.AgentPromptResult
		err    error
	}
	promptDone := make(chan promptOutcome, 1)
	go func() {
		result, err := runner.PromptAgent(context.Background(), tools.AgentPromptRequest{
			ParentSessionID: parentID,
			QueueJobID:      job.ID,
			Message:         "resume and wait",
		})
		promptDone <- promptOutcome{result: result, err: err}
	}()
	waitForTestSignal(t, providerStarted, 5*time.Second, "resumed queue provider call")
	stopped, err := runner.StopAgent(context.Background(), tools.AgentStopRequest{ParentSessionID: parentID, QueueJobID: job.ID})
	if err != nil || !stopped.Accepted || stopped.Status != session.StatusCancelled {
		t.Fatalf("cancel resumed queue child: result=%#v err=%v", stopped, err)
	}
	var prompted promptOutcome
	select {
	case prompted = <-promptDone:
	case <-time.After(6 * time.Second):
		t.Fatal("queue resume did not settle after cancellation")
	}
	if prompted.err != nil || !prompted.result.Accepted {
		t.Fatalf("unexpected queue prompt outcome after cancellation: %#v", prompted)
	}
	finalJob, err := runner.store.LoadJob(job.ID)
	if err != nil || finalJob.Status != session.QueueStatusCancelled || finalJob.SessionStatus != session.StatusCancelled {
		t.Fatalf("resume/cancel race did not settle queue job: job=%#v err=%v", finalJob, err)
	}
	assertQueueLeaseCleared(t, finalJob)
	available, err := runner.store.AcquireDirectChildSlot(parentID, parentID, "child_after_resume_cancel", 1)
	if err != nil || !available {
		t.Fatalf("resume/cancel race leaked active child capacity: acquired=%t err=%v", available, err)
	}
	if err := runner.store.ReleaseDirectChildSlot("child_after_resume_cancel"); err != nil {
		t.Fatalf("release post-race direct slot: %v", err)
	}
}

func TestPreSessionCancelRequestSurvivesWorkerStartAndResolvesOnlyAtTerminalFact(t *testing.T) {
	var providerCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_unexpected","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	cfg := cancellationRuntimeConfig(t, server.URL)
	cfg.Runtime.MultiAgent.CancelGraceSec = 0
	runner := NewRunner(cfg)
	workdir := t.TempDir()
	parentID := createParentSession(t, runner.store, workdir)
	otherParentID := createParentSession(t, runner.store, workdir)
	queued, err := runner.QueueSubmit(context.Background(), QueueSubmitRequest{
		ParentSessionID: parentID,
		Prompt:          "must be cancelled before start",
		Workdir:         workdir,
		IsolationMode:   "off",
	})
	if err != nil {
		t.Fatalf("queue submit: %v", err)
	}
	claimed, ok, err := runner.store.ClaimNextQueuedJobWithLimit(cfg.Runtime.MultiAgent.MaxActiveChildren)
	if err != nil || !ok {
		t.Fatalf("claim queued job: ok=%t err=%v job=%#v", ok, err, claimed)
	}
	childID := session.NewSessionID()
	claimed.SessionID = childID
	claimed.SessionStatus = session.StatusRunning
	if err := runner.store.SaveJob(claimed); err != nil {
		t.Fatalf("persist pre-session linked job: %v", err)
	}
	if _, err := runner.StopAgent(context.Background(), tools.AgentStopRequest{
		ParentSessionID: otherParentID,
		QueueJobID:      queued.ID,
	}); err == nil || !strings.Contains(err.Error(), "not linked") {
		t.Fatalf("cross-parent pre-session stop must be rejected, got %v", err)
	}

	requested, err := runner.StopAgent(context.Background(), tools.AgentStopRequest{
		ParentSessionID: parentID,
		QueueJobID:      queued.ID,
	})
	if err != nil || !requested.Accepted || requested.Behavior != "cancel_requested_before_session_create" {
		t.Fatalf("request pre-session cancellation: result=%#v err=%v", requested, err)
	}
	cancelRequest, err := runner.store.LoadSessionCancel(childID)
	if err != nil || cancelRequest.Status != session.CancelRequestStatusRequested {
		t.Fatalf("expected pending pre-session cancel request, request=%#v err=%v", cancelRequest, err)
	}
	coordination, err := runner.store.LoadParentCoordination(parentID)
	if err != nil {
		t.Fatalf("load parent coordination before terminal fact: %v", err)
	}
	if !containsString(coordination.UnresolvedQueueJobs, queued.ID) || containsString(coordination.CancelledQueueJobs, queued.ID) {
		t.Fatalf("cancel request alone must not release parent gate: %#v", coordination)
	}

	childRunner := NewRunner(cfg)
	result, err := childRunner.Start(context.Background(), StartRequest{
		SessionID:       childID,
		Prompt:          claimed.Prompt,
		Provider:        claimed.Provider,
		Model:           claimed.Model,
		ProviderOptions: claimed.ProviderOptions,
		Workdir:         claimed.RequestedWorkdir,
		Mode:            claimed.Mode,
		ParentSessionID: claimed.ParentSessionID,
		QueueJobID:      claimed.ID,
		IsolationMode:   "off",
		EffectiveBudget: session.CloneEffectiveBudget(claimed.EffectiveBudget),
	})
	if err != nil || result.Status != session.StatusCancelled {
		t.Fatalf("worker start must apply pre-session cancellation: result=%#v err=%v", result, err)
	}
	if providerCalls.Load() != 0 {
		t.Fatalf("pre-session cancellation must win before provider call, calls=%d", providerCalls.Load())
	}
	cancelRequest, err = runner.store.LoadSessionCancel(childID)
	if err != nil || cancelRequest.Status != session.CancelRequestStatusApplied {
		t.Fatalf("expected applied pre-session cancel request, request=%#v err=%v", cancelRequest, err)
	}
	finalJob, err := runner.store.LoadJob(queued.ID)
	if err != nil || finalJob.Status != session.QueueStatusCancelled || finalJob.SessionStatus != session.StatusCancelled {
		t.Fatalf("expected terminal cancelled job after worker start, job=%#v err=%v", finalJob, err)
	}
	coordination, err = runner.store.LoadParentCoordination(parentID)
	if err != nil {
		t.Fatalf("reload parent coordination: %v", err)
	}
	if containsString(coordination.UnresolvedQueueJobs, queued.ID) || !containsString(coordination.CancelledQueueJobs, queued.ID) || coordination.Parked {
		t.Fatalf("terminal cancellation did not release parent gate: %#v", coordination)
	}
	childEvents, err := runner.store.LoadEvents(childID)
	if err != nil {
		t.Fatalf("load pre-session child events: %v", err)
	}
	if countEventType(childEvents, "session.cancel_requested") != 1 || countEventType(childEvents, "session.cancelled") != 1 {
		t.Fatalf("pre-session cancellation events are not idempotent: %#v", childEvents)
	}
}

func TestStopAgentCancelsRunningSessionStartHookBeforeProvider(t *testing.T) {
	workdir := t.TempDir()
	startedPath := filepath.Join(workdir, "hook-started")
	var providerCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_unexpected","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	cfg := cancellationRuntimeConfig(t, server.URL)
	cfg.Runtime.MultiAgent.CancelGraceSec = 3
	cfg.Hooks.SessionStart = []config.HookDefinition{{
		Name:    "slow-session-start",
		Command: []string{"/bin/sh", "-c", fmt.Sprintf("printf started > %q; sleep 30", startedPath)},
	}}
	runner := NewRunner(cfg)
	parentID := createParentSession(t, runner.store, workdir)
	type spawnOutcome struct {
		result tools.AgentSpawnResult
		err    error
	}
	spawnDone := make(chan spawnOutcome, 1)
	go func() {
		result, err := runner.SpawnAgent(context.Background(), tools.AgentSpawnRequest{
			ParentSessionID: parentID,
			Prompt:          "cancel the hook",
			Workdir:         workdir,
			IsolationMode:   "off",
		})
		spawnDone <- spawnOutcome{result: result, err: err}
	}()
	waitForFile(t, startedPath, 5*time.Second)
	childID := waitForRunningChild(t, runner.store, parentID, 5*time.Second)
	started := time.Now()
	stopped, err := runner.StopAgent(context.Background(), tools.AgentStopRequest{ParentSessionID: parentID, SessionID: childID})
	if err != nil || stopped.Status != session.StatusCancelled {
		t.Fatalf("cancel child in session.start hook: result=%#v err=%v", stopped, err)
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("hook cancellation exceeded grace window: %s", elapsed)
	}
	outcome := waitForSpawnOutcome(t, spawnDone, 5*time.Second)
	if outcome.err != nil || outcome.result.Status != session.StatusCancelled {
		t.Fatalf("unexpected hook-cancelled child outcome: result=%#v err=%v", outcome.result, outcome.err)
	}
	if providerCalls.Load() != 0 {
		t.Fatalf("cancelled session.start hook must stop before provider call, calls=%d", providerCalls.Load())
	}
}

func TestBackgroundWaitDoesNotConsumeActiveRuntimeAndRemainsCancellable(t *testing.T) {
	cfg := cancellationRuntimeConfig(t, "http://127.0.0.1:1")
	cfg.Runtime.ChildBudget.MaxActiveRuntimeSec = 1
	cfg.Runtime.MultiAgent.CancelGraceSec = 2
	cfg.Runtime.Queue.PollIntervalMS = 25
	runner := NewRunner(cfg)
	workdir := t.TempDir()
	parentID := createParentSession(t, runner.store, workdir)
	now := time.Now().UTC()
	child := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        now.Format(time.RFC3339Nano),
		Workdir:          workdir,
		RequestedWorkdir: workdir,
		Mode:             session.ModeExec,
		Provider:         "openai-compatible",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyAutonomous,
		ParentSessionID:  parentID,
		RootSessionID:    parentID,
		Depth:            1,
		EffectiveBudget:  session.NewEffectiveBudget(session.BudgetSourceRuntimeChild, 0, 1, 0, 0, now),
	}
	state := session.State{Status: session.StatusRunning, Phase: "tool_execute", Turn: 1, UpdatedAt: now.Format(time.RFC3339Nano)}
	if err := runner.store.Create(child, state); err != nil {
		t.Fatalf("create background-wait child: %v", err)
	}
	if err := addParentChildSession(runner.store, parentID, child.ID, parentWaitAll); err != nil {
		t.Fatalf("add parent child coordination: %v", err)
	}
	runCtx, cancelBudget, budgetRun := runner.engine.beginChildBudgetRun(context.Background(), child, state)
	defer cancelBudget()
	release := registerActiveSessionRunner(runner.store, child.ID, runner)
	defer release()
	type waitOutcome struct {
		result RunResult
		err    error
	}
	waitDone := make(chan waitOutcome, 1)
	go func() {
		result, err := runner.engine.awaitingBackground(runCtx, child, state, hooks.New(cfg.Hooks, workdir), budgetRun)
		waitDone <- waitOutcome{result: result, err: err}
	}()
	waitForSessionStatus(t, runner.store, child.ID, session.StatusAwaitingInput, 3*time.Second)
	time.Sleep(1200 * time.Millisecond)
	stillWaiting, err := runner.store.LoadState(child.ID)
	if err != nil || stillWaiting.Status != session.StatusAwaitingInput {
		t.Fatalf("background wait incorrectly consumed active-runtime budget: state=%#v err=%v", stillWaiting, err)
	}
	meta, err := runner.store.LoadMetadata(child.ID)
	if err != nil {
		t.Fatalf("load background-wait budget: %v", err)
	}
	if meta.EffectiveBudget == nil || meta.EffectiveBudget.UsedActiveRuntimeMS >= 1000 {
		t.Fatalf("background wait must not count toward active runtime: %#v", meta.EffectiveBudget)
	}
	stopped, err := runner.StopAgent(context.Background(), tools.AgentStopRequest{ParentSessionID: parentID, SessionID: child.ID})
	if err != nil || stopped.Status != session.StatusCancelled {
		t.Fatalf("cancel active background wait: result=%#v err=%v", stopped, err)
	}
	select {
	case outcome := <-waitDone:
		if outcome.err != nil || outcome.result.Status != session.StatusCancelled {
			t.Fatalf("unexpected background-wait cancellation outcome: result=%#v err=%v", outcome.result, outcome.err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("background wait did not exit after parent cancellation")
	}
	meta, err = runner.store.LoadMetadata(child.ID)
	if err != nil || meta.EffectiveBudget == nil || meta.EffectiveBudget.Status != session.BudgetStatusCancelled || meta.EffectiveBudget.UsedActiveRuntimeMS >= 1000 {
		t.Fatalf("cancellation must preserve pre-wait active-runtime accounting: budget=%#v err=%v", meta.EffectiveBudget, err)
	}
}

func cancellationRuntimeConfig(t *testing.T, baseURL string) *config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.DefaultProvider = "openai-compatible"
	cfg.Providers["openai-compatible"] = config.Provider{
		APIKeyEnv:  "OPENAI_API_KEY",
		BaseURL:    baseURL,
		Model:      "gpt-5.4",
		TimeoutSec: 60,
		WireAPI:    "responses",
	}
	cfg.Session.Dir = t.TempDir()
	cfg.Runtime.Isolation.RootDir = t.TempDir()
	cfg.Runtime.MaxTurnsHard = -1
	cfg.Runtime.MaxTurnsSoft = 100
	cfg.Runtime.ChildBudget = config.ChildBudgetConfig{}
	return cfg
}

func responsesToolServer(t *testing.T, toolName string, arguments map[string]any) *httptest.Server {
	t.Helper()
	argumentsJSON, err := json.Marshal(arguments)
	if err != nil {
		t.Fatalf("marshal tool arguments: %v", err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     "resp_tool",
			"status": "completed",
			"output": []map[string]any{{
				"type":      "function_call",
				"call_id":   "call_tool",
				"name":      toolName,
				"arguments": string(argumentsJSON),
			}},
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
		})
	}))
}

func waitForRunningChild(t *testing.T, store *session.Store, parentSessionID string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		children, err := store.ListChildren(parentSessionID, -1)
		if err == nil {
			for _, child := range children {
				if child.Status == session.StatusRunning {
					return child.ID
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for running child of %s", parentSessionID)
	return ""
}

func waitForTestSignal(t *testing.T, signal <-chan struct{}, timeout time.Duration, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for file %s", path)
}

func waitForSessionStatus(t *testing.T, store *session.Store, sessionID, status string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		state, err := store.LoadState(sessionID)
		if err == nil && state.Status == status {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for session %s status %s", sessionID, status)
}

func waitForSpawnOutcome[T any](t *testing.T, done <-chan T, timeout time.Duration) T {
	t.Helper()
	select {
	case outcome := <-done:
		return outcome
	case <-time.After(timeout):
		var zero T
		t.Fatal("timed out waiting for direct child to settle")
		return zero
	}
}
