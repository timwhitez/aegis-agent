package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/events"
	"go-cli-agent/internal/hooks"
	"go-cli-agent/internal/provider"
	"go-cli-agent/internal/runtime"
	"go-cli-agent/internal/session"
)

type fakeRunner struct {
	bus            *events.Bus
	startResult    runtime.RunResult
	continueResult runtime.RunResult
	steerResult    runtime.SteerResult
	steerErr       error
	probeResult    runtime.ProbeResult
	delegateResult runtime.DelegateResult
	probeErr       error
	delegateErr    error
	startErr       error
	listResult     []session.SessionSummary
	taskBoard      session.TaskBoard
	store          *session.Store
	queueJob       session.QueueJob
	queueJobs      []session.QueueJob
	processJob     session.QueueJob
	processOK      bool
	processErr     error
	startCalls     []runtime.StartRequest
	continueCalls  []runtime.ContinueRequest
	steerCalls     []runtime.SteerRequest
	probeCalls     []runtime.ProbeRequest
	delegateCalls  []runtime.DelegateRequest
	interruptIDs   []string
}

func TestWebListenExposesNetwork(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{addr: "127.0.0.1:3940", want: false},
		{addr: "localhost:3940", want: false},
		{addr: "[::1]:3940", want: false},
		{addr: "0.0.0.0:3940", want: true},
		{addr: ":3940", want: true},
		{addr: "192.168.1.5:3940", want: true},
		{addr: "webconsole.local:3940", want: true},
	}
	for _, tc := range cases {
		if got := webListenExposesNetwork(tc.addr); got != tc.want {
			t.Fatalf("webListenExposesNetwork(%q)=%v want %v", tc.addr, got, tc.want)
		}
	}
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{bus: events.NewBus()}
}

func writeDoctorQueueJob(t *testing.T, root, status string, job session.QueueJob) {
	t.Helper()
	dir := filepath.Join(root, "_queue", status)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir queue dir: %v", err)
	}
	data, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("marshal queue job: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, job.ID+".json"), data, 0o600); err != nil {
		t.Fatalf("write queue job: %v", err)
	}
}

func (f *fakeRunner) Start(ctx context.Context, req runtime.StartRequest) (runtime.RunResult, error) {
	f.startCalls = append(f.startCalls, req)
	f.bus.Publish(events.New("s1", "session.started", "prepare", map[string]any{}))
	f.bus.Publish(events.New("s1", "assistant.message", "assistant_output", map[string]any{"text": "hello"}))
	select {
	case <-ctx.Done():
	default:
	}
	return f.startResult, f.startErr
}

func (f *fakeRunner) Continue(_ context.Context, req runtime.ContinueRequest) (runtime.RunResult, error) {
	f.continueCalls = append(f.continueCalls, req)
	return f.continueResult, nil
}

func (f *fakeRunner) Steer(_ context.Context, req runtime.SteerRequest) (runtime.SteerResult, error) {
	f.steerCalls = append(f.steerCalls, req)
	return f.steerResult, f.steerErr
}

func (f *fakeRunner) Probe(_ context.Context, req runtime.ProbeRequest) (runtime.ProbeResult, error) {
	f.probeCalls = append(f.probeCalls, req)
	return f.probeResult, f.probeErr
}

func (f *fakeRunner) Interrupt(sessionID string) error {
	f.interruptIDs = append(f.interruptIDs, sessionID)
	return nil
}

func (f *fakeRunner) Tasks(string) (session.TaskBoard, error) {
	return f.taskBoard, nil
}

func (f *fakeRunner) List(int) ([]session.SessionSummary, error) {
	return f.listResult, nil
}

func (f *fakeRunner) Store() *session.Store {
	return f.store
}

func (f *fakeRunner) Delegate(_ context.Context, req runtime.DelegateRequest) (runtime.DelegateResult, error) {
	f.delegateCalls = append(f.delegateCalls, req)
	return f.delegateResult, f.delegateErr
}

func (f *fakeRunner) QueueSubmit(_ context.Context, _ runtime.QueueSubmitRequest) (session.QueueJob, error) {
	return f.queueJob, nil
}

func (f *fakeRunner) QueueShow(string) (session.QueueJob, error) {
	return f.queueJob, nil
}

func (f *fakeRunner) QueueList(int) ([]session.QueueJob, error) {
	return f.queueJobs, nil
}

func (f *fakeRunner) ProcessNextJob(context.Context) (session.QueueJob, bool, error) {
	return f.processJob, f.processOK, f.processErr
}

func (f *fakeRunner) Bus() *events.Bus {
	return f.bus
}

func TestRunCommandReturnsExitErrorForIncompleteExec(t *testing.T) {
	fake := newFakeRunner()
	fake.startResult = runtime.RunResult{
		SessionID: "s1",
		Status:    session.StatusFailed,
		LastError: "incomplete_no_finish: task ended without explicit finish",
	}
	restore := runnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { runnerLoader = restore }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := Run(context.Background(), []string{"exec", "--json", "do work"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "status 6") {
		t.Fatalf("expected exit status 6, err=%v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"exit_code":6`) {
		t.Fatalf("expected json exit code in output, got %s", stdout.String())
	}
}

func TestRunCommandAcceptsFlagsAfterPrompt(t *testing.T) {
	fake := newFakeRunner()
	fake.startResult = runtime.RunResult{
		SessionID: "s1",
		Status:    session.StatusCompleted,
		FinalText: "done",
	}
	restore := runnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { runnerLoader = restore }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := Run(context.Background(), []string{"exec", "do work", "--json", "--timeout", "30"}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if len(fake.startCalls) != 1 {
		t.Fatalf("expected one start call, got %d", len(fake.startCalls))
	}
	if got := fake.startCalls[0].Prompt; got != "do work" {
		t.Fatalf("expected prompt without trailing flags, got %q", got)
	}
	if !strings.Contains(stdout.String(), `"status":"completed"`) {
		t.Fatalf("expected json output, got %s", stdout.String())
	}
}

func TestRunCommandParsesGoalFlags(t *testing.T) {
	fake := newFakeRunner()
	fake.startResult = runtime.RunResult{
		SessionID: "s1",
		Status:    session.StatusCompleted,
		FinalText: "done",
	}
	restore := runnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { runnerLoader = restore }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := Run(context.Background(), []string{
		"exec",
		"do work",
		"--json",
		"--goal", "Ship the goal",
		"--goal-mode", "mission",
		"--goal-token-budget", "123",
		"--goal-time-budget", "15m",
		"--goal-success", "tests pass",
		"--goal-validate", "go test ./internal/app",
		"--goal-plan-approval",
		"--goal-stop-on-budget",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if len(fake.startCalls) != 1 {
		t.Fatalf("expected one start call, got %d", len(fake.startCalls))
	}
	goal := fake.startCalls[0].Goal
	if goal == nil || !goal.Enabled || goal.Mode != session.GoalModeMission || goal.Objective != "Ship the goal" {
		t.Fatalf("unexpected goal draft: %#v", goal)
	}
	if goal.TokenBudget == nil || *goal.TokenBudget != 123 {
		t.Fatalf("unexpected token budget: %#v", goal.TokenBudget)
	}
	if goal.TimeBudgetSeconds == nil || *goal.TimeBudgetSeconds != 900 {
		t.Fatalf("unexpected time budget: %#v", goal.TimeBudgetSeconds)
	}
	if len(goal.SuccessCriteria) != 1 || goal.SuccessCriteria[0] != "tests pass" || len(goal.ValidationPlan) != 1 {
		t.Fatalf("unexpected criteria/validation: %#v", goal)
	}
	if !goal.RequirePlanApproval || !goal.StopOnBudget || goal.Source != session.GoalSourceCLI {
		t.Fatalf("unexpected goal controls/source: %#v", goal)
	}
}

func TestRunCommandParsesPlanFlags(t *testing.T) {
	fake := newFakeRunner()
	fake.startResult = runtime.RunResult{
		SessionID: "s1",
		Status:    session.StatusAwaitingInput,
		FinalText: "Plan Mode is awaiting approval.",
	}
	restore := runnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { runnerLoader = restore }()
	restoreTTY := stdinIsTerminal
	stdinIsTerminal = func() bool { return false }
	defer func() { stdinIsTerminal = restoreTTY }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := Run(context.Background(), []string{"exec", "--json", "--plan-only", "plan this work"}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if len(fake.startCalls) != 1 {
		t.Fatalf("expected one start call, got %d", len(fake.startCalls))
	}
	planMode := fake.startCalls[0].PlanMode
	if planMode == nil || !planMode.Enabled || planMode.Objective != "plan this work" || planMode.Source != session.PlanModeSourceCLI {
		t.Fatalf("unexpected plan mode draft: %#v", planMode)
	}
	if fake.startCalls[0].PlanInputHandler != nil {
		t.Fatalf("non-TTY CLI must not install an unrecoverable Plan Mode input responder")
	}
	if !strings.Contains(stdout.String(), `"status":"awaiting_input"`) {
		t.Fatalf("expected awaiting_input to be a successful planned state, got %s", stdout.String())
	}
}

func TestReadPromptStdinRejectsOversizedInput(t *testing.T) {
	_, err := readPromptStdin(strings.NewReader(strings.Repeat("x", int(maxPromptStdinBytes)+1)))
	if err == nil || !strings.Contains(err.Error(), "stdin prompt exceeds maximum size") {
		t.Fatalf("expected oversized stdin prompt rejection, got %v", err)
	}
}

func TestRunCommandDoesNotInferPlanModeFromPromptText(t *testing.T) {
	fake := newFakeRunner()
	fake.startResult = runtime.RunResult{
		SessionID: "s1",
		Status:    session.StatusCompleted,
		FinalText: "done",
	}
	restore := runnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { runnerLoader = restore }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := Run(context.Background(), []string{"exec", "--json", "先计划一下再做这个任务"}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if len(fake.startCalls) != 1 {
		t.Fatalf("expected one start call, got %d", len(fake.startCalls))
	}
	if fake.startCalls[0].PlanMode != nil {
		t.Fatalf("natural language prompt must not enable Plan Mode, got %#v", fake.startCalls[0].PlanMode)
	}
}

func TestContinueCommandParsesPlanApproval(t *testing.T) {
	fake := newFakeRunner()
	fake.continueResult = runtime.RunResult{
		SessionID: "s1",
		Status:    session.StatusCompleted,
		FinalText: "done",
	}
	restore := runnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { runnerLoader = restore }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := Run(context.Background(), []string{"continue", "s1", "--json", "--approve-plan"}, &stdout, &stderr); err != nil {
		t.Fatalf("continue: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if len(fake.continueCalls) != 1 {
		t.Fatalf("expected one continue call, got %d", len(fake.continueCalls))
	}
	call := fake.continueCalls[0]
	if !call.ApprovePlan || call.CancelPlan || call.Source != session.PlanModeSourceCLI {
		t.Fatalf("unexpected continue request: %#v", call)
	}
}

func TestGoalCommandAcceptsFlagsAfterSessionID(t *testing.T) {
	store := session.NewStore(t.TempDir())
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "session_goal_cli",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		RootSessionID:    "session_goal_cli",
	}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeGoal,
		Objective: "Show goal through CLI",
		Source:    session.GoalSourceCLI,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	fake := newFakeRunner()
	fake.store = store
	restore := storeRunnerLoader
	storeRunnerLoader = func(string, string) (storeRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { storeRunnerLoader = restore }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := Run(context.Background(), []string{"goal", "show", meta.ID, "--json"}, &stdout, &stderr); err != nil {
		t.Fatalf("goal show: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"objective":"Show goal through CLI"`) {
		t.Fatalf("expected json goal output, got %s", stdout.String())
	}
}

func TestGoalMissionPlanAndValidationCommands(t *testing.T) {
	store := session.NewStore(t.TempDir())
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "session_goal_plan_cli",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		RootSessionID:    "session_goal_plan_cli",
	}
	if err := store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	goal, err := store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:        true,
		Mode:           session.GoalModeMission,
		Objective:      "Plan through CLI",
		ValidationPlan: []string{"go test ./internal/app"},
		Features:       []string{"CLI plan"},
		Milestones:     []string{"CLI validation"},
		Source:         session.GoalSourceCLI,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	goal.Mission.Features[0].ClaimedAssertions = []string{"validation_0001"}
	goal.Mission.Milestones[0].ValidationIDs = []string{"validation_0001"}
	if err := store.SaveGoal(meta.ID, goal); err != nil {
		t.Fatalf("save goal: %v", err)
	}
	fake := newFakeRunner()
	fake.store = store
	restore := storeRunnerLoader
	storeRunnerLoader = func(string, string) (storeRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { storeRunnerLoader = restore }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := Run(context.Background(), []string{"goal", "plan", "check", meta.ID, "--json"}, &stdout, &stderr); err != nil {
		t.Fatalf("goal plan check: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"covered_assertions":1`) || !strings.Contains(stdout.String(), `"approval_blocked":false`) {
		t.Fatalf("expected covered plan check, got %s", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if err := Run(context.Background(), []string{"goal", "validation", "show", meta.ID}, &stdout, &stderr); err != nil {
		t.Fatalf("goal validation show: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "validation_contract: 1") {
		t.Fatalf("expected validation contract output, got %s", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if err := Run(context.Background(), []string{"goal", "plan", "approve", meta.ID, "--json"}, &stdout, &stderr); err != nil {
		t.Fatalf("goal plan approve: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	approved, err := store.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load approved goal: %v", err)
	}
	if approved.Mission.PlanStatus != "approved" {
		t.Fatalf("expected approved mission plan, got %#v", approved.Mission)
	}
}

func TestGoalPlanApproveRejectsGoalWithoutMissionPlan(t *testing.T) {
	store := session.NewStore(t.TempDir())
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "session_goal_plan_plain_cli",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		RootSessionID:    "session_goal_plan_plain_cli",
	}
	if err := store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeGoal,
		Objective: "Plain goal should not synthesize a mission plan",
		Source:    session.GoalSourceCLI,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	fake := newFakeRunner()
	fake.store = store
	restore := storeRunnerLoader
	storeRunnerLoader = func(string, string) (storeRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { storeRunnerLoader = restore }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := Run(context.Background(), []string{"goal", "plan", "approve", meta.ID, "--json"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "mission plan is required") {
		t.Fatalf("expected missing mission plan error, got %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	loaded, err := store.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load goal: %v", err)
	}
	if loaded.Mode != session.GoalModeGoal || loaded.Mission != nil {
		t.Fatalf("approval mutated plain goal: mode=%s mission=%#v", loaded.Mode, loaded.Mission)
	}
}

func TestGoalStatusCommandPreservesAccountingAndProgressFacts(t *testing.T) {
	store := session.NewStore(t.TempDir())
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "session_goal_status_cli_preserves_facts",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		RootSessionID:    "session_goal_status_cli_preserves_facts",
	}
	if err := store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	tokenBudget := int64(5)
	if _, err := store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:      true,
		Objective:    "Pause through CLI",
		TokenBudget:  &tokenBudget,
		StopOnBudget: true,
		Source:       session.GoalSourceCLI,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	if _, limited, err := store.UpdateGoalAccounting(meta.ID, session.GoalUsageDelta{TokensUsedDelta: 6, SourceTurn: 1}); err != nil || !limited {
		t.Fatalf("update accounting limited=%v err=%v", limited, err)
	}
	_, progress, err := store.RecordGoalProgress(meta.ID, session.GoalProgressInput{
		Source:   session.GoalSourceTool,
		Kind:     "budget_wrapup",
		Summary:  "facts recorded",
		Evidence: []string{"progress evidence"},
	})
	if err != nil {
		t.Fatalf("record progress: %v", err)
	}
	fake := newFakeRunner()
	fake.store = store
	restore := storeRunnerLoader
	storeRunnerLoader = func(string, string) (storeRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { storeRunnerLoader = restore }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := Run(context.Background(), []string{"goal", "pause", meta.ID, "--json"}, &stdout, &stderr); err != nil {
		t.Fatalf("goal pause: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	paused, err := store.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load paused goal: %v", err)
	}
	if paused.Status != session.GoalStatusPaused || paused.TokensUsed != 6 || paused.BudgetLimitedAt == "" || paused.BudgetWrapUpRequestedAt == "" {
		t.Fatalf("expected accounting facts to survive CLI status change, got %#v", paused)
	}
	if len(paused.Progress) != 1 || paused.Progress[0].ID != progress.ID {
		t.Fatalf("expected progress facts to survive CLI status change, got %#v", paused.Progress)
	}
}

func TestRunCommandSupportsInitFlag(t *testing.T) {
	fake := newFakeRunner()
	fake.startResult = runtime.RunResult{
		SessionID: "s1",
		Status:    session.StatusCompleted,
		FinalText: "initialized",
	}
	restore := runnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { runnerLoader = restore }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := Run(context.Background(), []string{"exec", "bootstrap repo", "--json", "--init"}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if len(fake.startCalls) != 1 {
		t.Fatalf("expected one start call, got %d", len(fake.startCalls))
	}
	if fake.startCalls[0].Mode != session.ModeInit {
		t.Fatalf("expected init mode, got %#v", fake.startCalls[0].Mode)
	}
	if got := fake.startCalls[0].Prompt; got != "bootstrap repo" {
		t.Fatalf("expected prompt %q, got %q", "bootstrap repo", got)
	}
}

func TestRunCommandLoadsConfigRelativeToInvokeDirectoryNotTaskWorkdir(t *testing.T) {
	fake := newFakeRunner()
	fake.startResult = runtime.RunResult{
		SessionID: "s1",
		Status:    session.StatusCompleted,
		FinalText: "done",
	}

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	invokeDir := t.TempDir()
	if err := os.Chdir(invokeDir); err != nil {
		t.Fatalf("chdir invoke dir: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()

	var loaderCWD string
	restore := runnerLoader
	runnerLoader = func(_ string, cwd string) (coreRunner, *config.Config, error) {
		loaderCWD = cwd
		return fake, config.Default(), nil
	}
	defer func() { runnerLoader = restore }()

	taskWorkdir := filepath.Join(t.TempDir(), "task")
	if err := os.MkdirAll(taskWorkdir, 0o755); err != nil {
		t.Fatalf("mkdir task workdir: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := Run(context.Background(), []string{"exec", "--workdir", taskWorkdir, "--json", "ping"}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if loaderCWD != invokeDir {
		t.Fatalf("expected runnerLoader cwd %q, got %q", invokeDir, loaderCWD)
	}
	if len(fake.startCalls) != 1 || fake.startCalls[0].Workdir != taskWorkdir {
		t.Fatalf("expected task workdir %q, got %#v", taskWorkdir, fake.startCalls)
	}
}

func TestRunCommandLeavesEmptyWorkdirForRuntimeDefaultWorkspace(t *testing.T) {
	fake := newFakeRunner()
	fake.startResult = runtime.RunResult{
		SessionID: "s1",
		Status:    session.StatusCompleted,
		FinalText: "done",
	}
	restore := runnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { runnerLoader = restore }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := Run(context.Background(), []string{"exec", "--json", "ping"}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if len(fake.startCalls) != 1 {
		t.Fatalf("expected one start call, got %d", len(fake.startCalls))
	}
	if fake.startCalls[0].Workdir != "" {
		t.Fatalf("expected empty workdir for runtime defaulting, got %#v", fake.startCalls[0])
	}
}

func TestSessionsCommandRendersSummary(t *testing.T) {
	fake := newFakeRunner()
	fake.listResult = []session.SessionSummary{
		{
			ID:        "s1",
			Status:    session.StatusAwaitingInput,
			Provider:  "openai",
			Model:     "gpt-5.4",
			CreatedAt: "2026-03-18T23:59:00Z",
			UpdatedAt: "2026-03-19T00:00:00Z",
			Phase:     "turn_decide",
		},
	}
	restore := runnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { runnerLoader = restore }()

	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"sessions"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(stdout.String(), "s1") || !strings.Contains(stdout.String(), "awaiting_input") || !strings.Contains(stdout.String(), "phase=turn_decide") {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func TestTasksCommandRendersTaskBoard(t *testing.T) {
	fake := newFakeRunner()
	fake.taskBoard = session.TaskBoard{
		Todo: []session.TodoItem{{Content: "Audit provider", Status: "in_progress"}},
		Groups: map[string][]session.Task{
			"in_progress": {
				{ID: "task_0000", Subject: "Active implementation"},
			},
			"ready": {
				{ID: "task_0001", Subject: "Implement hook tests"},
			},
			"cancelled": {
				{ID: "task_0002", Subject: "Drop stale path", Status: "cancelled"},
			},
		},
	}
	restore := runnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { runnerLoader = restore }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := Run(context.Background(), []string{"tasks", "s1"}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(stdout.String(), "Audit provider") ||
		!strings.Contains(stdout.String(), "Active implementation") ||
		!strings.Contains(stdout.String(), "Implement hook tests") ||
		!strings.Contains(stdout.String(), "CANCELLED") ||
		!strings.Contains(stdout.String(), "Drop stale path") {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func TestTasksCommandAllRendersFlatTaskList(t *testing.T) {
	fake := newFakeRunner()
	fake.taskBoard = session.TaskBoard{
		Tasks: []session.Task{
			{ID: "task_0001", Subject: "Implement hook tests", Status: "ready"},
			{ID: "task_0002", Subject: "Review docs", Status: "completed"},
		},
		Groups: map[string][]session.Task{
			"ready": {
				{ID: "task_0001", Subject: "Implement hook tests", Status: "ready"},
			},
			"completed": {
				{ID: "task_0002", Subject: "Review docs", Status: "completed"},
			},
		},
	}
	restore := runnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { runnerLoader = restore }()

	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"tasks", "--all", "s1"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(stdout.String(), "ALL") || !strings.Contains(stdout.String(), "[completed] Review docs") {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func TestProbeProviderCommandRendersJSONAndExitStatus(t *testing.T) {
	fake := newFakeRunner()
	fake.probeResult = runtime.ProbeResult{
		Provider:      "openai-compatible",
		Model:         "gpt-5.4",
		BaseURL:       "http://example/v1",
		WireAPI:       "responses",
		StopReason:    "tool_use",
		ToolCallNames: []string{"finish"},
		FinishMessage: "provider probe ok",
	}
	restore := runnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { runnerLoader = restore }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := Run(context.Background(), []string{"probe-provider", "--json"}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"provider":"openai-compatible"`) || !strings.Contains(stdout.String(), `"finish_message":"provider probe ok"`) {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func TestProbeProviderCommandRendersJSONErrorAndExitStatus(t *testing.T) {
	fake := newFakeRunner()
	fake.probeErr = errors.New("provider unavailable")
	restore := runnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) {
		cfg := config.Default()
		cfg.DefaultProvider = "openai-compatible"
		cfg.Providers["openai-compatible"] = config.Provider{
			BaseURL: "http://example/v1",
			Model:   "gpt-5.4",
			WireAPI: "responses",
		}
		return fake, cfg, nil
	}
	defer func() { runnerLoader = restore }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := Run(context.Background(), []string{"probe-provider", "--json"}, &stdout, &stderr)
	var exitErr ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 1 {
		t.Fatalf("expected exit code 1, got err=%v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"provider":"openai-compatible"`) ||
		!strings.Contains(stdout.String(), `"model":"gpt-5.4"`) ||
		!strings.Contains(stdout.String(), `"base_url":"http://example/v1"`) ||
		!strings.Contains(stdout.String(), `"wire_api":"responses"`) ||
		!strings.Contains(stdout.String(), `"error":"provider unavailable"`) {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func TestProbeProviderCommandJSONIncludesProviderErrorClassification(t *testing.T) {
	fake := newFakeRunner()
	fake.probeErr = runtime.WrapProviderError(&provider.HTTPError{
		Provider: "openai",
		Class:    "upstream_unavailable",
		Message:  `Post "https://api.openai.com/v1/responses": EOF`,
	})
	restore := runnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) {
		cfg := config.Default()
		cfg.DefaultProvider = "openai"
		return fake, cfg, nil
	}
	defer func() { runnerLoader = restore }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := Run(context.Background(), []string{"probe-provider", "--json"}, &stdout, &stderr)
	var exitErr ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 1 {
		t.Fatalf("expected exit code 1, got err=%v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	var payload probeProviderJSON
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v output=%s", err, stdout.String())
	}
	if payload.ErrorClass != "upstream_unavailable" {
		t.Fatalf("expected upstream_unavailable classification, got %#v", payload)
	}
	if !strings.Contains(payload.Advice, "network connectivity") {
		t.Fatalf("expected network advice, got %#v", payload)
	}
	if payload.Provider != "openai" || payload.Model == "" || payload.BaseURL == "" {
		t.Fatalf("expected provider config fallback fields, got %#v", payload)
	}
}

func TestProbeProviderCommandNonJSONErrorDoesNotPrintEmptySuccessFields(t *testing.T) {
	fake := newFakeRunner()
	fake.probeErr = errors.New("provider unavailable")
	restore := runnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { runnerLoader = restore }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := Run(context.Background(), []string{"probe-provider"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected probe error")
	}
	if strings.Contains(stdout.String(), "provider:") || strings.Contains(stdout.String(), "stop_reason:") {
		t.Fatalf("expected no success-looking stdout fields, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "probe failed: provider unavailable") {
		t.Fatalf("expected stderr probe failure, got %q", stderr.String())
	}
}

func TestProbeProviderCommandNonJSONPrintsProviderErrorAdvice(t *testing.T) {
	fake := newFakeRunner()
	fake.probeErr = runtime.WrapProviderError(&provider.HTTPError{
		Provider:    "openai",
		Class:       "upstream_timeout",
		Message:     "context deadline exceeded",
		TimeoutKind: "request_timeout",
	})
	restore := runnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { runnerLoader = restore }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := Run(context.Background(), []string{"probe-provider"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected probe error")
	}
	if strings.Contains(stdout.String(), "provider:") || strings.Contains(stdout.String(), "stop_reason:") {
		t.Fatalf("expected no success-looking stdout fields, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "advice: Check provider availability") {
		t.Fatalf("expected provider advice, got %q", stderr.String())
	}
}

func TestDoctorCommandProviderProbeIncludesProviderErrorClassification(t *testing.T) {
	fake := newFakeRunner()
	fake.probeErr = runtime.WrapProviderError(&provider.HTTPError{
		Provider:   "openai",
		Class:      "upstream_unavailable",
		Message:    `Post "https://api.openai.com/v1/responses": EOF`,
		StatusCode: 0,
	})
	restore := runnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) {
		cfg := config.Default()
		cfg.DefaultProvider = "openai-compatible"
		cfg.Providers["openai-compatible"] = config.Provider{
			APIKeyEnv: "TEST_PRESENT_KEY",
			BaseURL:   "http://example/v1",
			Model:     "gpt-5.4",
			WireAPI:   "responses",
		}
		return fake, cfg, nil
	}
	defer func() { runnerLoader = restore }()
	t.Setenv("TEST_PRESENT_KEY", "present")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := Run(context.Background(), []string{"doctor", "--provider", "openai-compatible", "--json"}, &stdout, &stderr)
	var exitErr ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 1 {
		t.Fatalf("expected exit code 1, got err=%v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("unmarshal report: %v output=%s", err, stdout.String())
	}
	var probeCheck *doctorCheck
	for i := range report.Checks {
		if report.Checks[i].Name == "provider.probe" {
			probeCheck = &report.Checks[i]
			break
		}
	}
	if probeCheck == nil {
		t.Fatalf("provider.probe check missing: %#v", report.Checks)
	}
	if probeCheck.Status != "fail" || probeCheck.Details["error_class"] != "upstream_unavailable" {
		t.Fatalf("expected classified provider probe failure, got %#v", probeCheck)
	}
	if probeCheck.Details["provider"] != "openai-compatible" || probeCheck.Details["model"] != "gpt-5.4" || probeCheck.Details["base_url"] != "http://example/v1" || probeCheck.Details["wire_api"] != "responses" {
		t.Fatalf("expected provider probe fallback config fields, got %#v", probeCheck.Details)
	}
	advice, _ := probeCheck.Details["advice"].(string)
	if !strings.Contains(advice, "network connectivity") {
		t.Fatalf("expected network advice, got %#v", probeCheck.Details)
	}
}

func TestSessionsCommandJSONUsesEmptyArray(t *testing.T) {
	fake := newFakeRunner()
	restore := runnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { runnerLoader = restore }()

	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"sessions", "--json"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := stdout.String(); got != "[]\n" {
		t.Fatalf("expected empty array json, got %q", got)
	}
}

func TestSteerCommandAcceptsFlagsAfterSessionID(t *testing.T) {
	fake := newFakeRunner()
	fake.steerResult = runtime.SteerResult{
		SessionID: "s1",
		Accepted:  true,
		Behavior:  "queued",
	}
	restore := runnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { runnerLoader = restore }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := Run(context.Background(), []string{"steer", "s1", "--message", "focus tests", "--interrupt", "--json"}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if len(fake.steerCalls) != 1 {
		t.Fatalf("expected one steer call, got %d", len(fake.steerCalls))
	}
	if got := fake.steerCalls[0].Message; got != "focus tests" {
		t.Fatalf("unexpected steer message: %q", got)
	}
	if !fake.steerCalls[0].Interrupt {
		t.Fatal("expected interrupt=true")
	}
	if !strings.Contains(stdout.String(), `"session_id":"s1"`) {
		t.Fatalf("expected snake_case json payload, got %s", stdout.String())
	}
}

func TestSteerCommandRendersJSONErrorAndExitStatus(t *testing.T) {
	fake := newFakeRunner()
	fake.steerErr = runtime.SteerValidationError{
		Code:        "steer_input_too_large",
		MaxChars:    12000,
		ActualChars: 12001,
	}
	restore := runnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { runnerLoader = restore }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := Run(context.Background(), []string{"steer", "s1", "--message", "focus tests", "--json"}, &stdout, &stderr)
	var exitErr ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 1 {
		t.Fatalf("expected exit code 1, got err=%v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"session_id":"s1"`) ||
		!strings.Contains(stdout.String(), `"accepted":false`) ||
		!strings.Contains(stdout.String(), `"code":"steer_input_too_large"`) ||
		!strings.Contains(stdout.String(), `"max_chars":12000`) ||
		!strings.Contains(stdout.String(), `"actual_chars":12001`) {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func TestRunReturnsConfigExitCodeForConfigError(t *testing.T) {
	restore := runnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) {
		return nil, nil, runtime.WrapConfigError(errors.New("bad config"))
	}
	defer func() { runnerLoader = restore }()

	err := Run(context.Background(), []string{"sessions"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected config error")
	}
	var classified ClassifiedError
	if !errors.As(err, &classified) || classified.Code != 2 {
		t.Fatalf("expected exit code 2, got %v", err)
	}
}

func TestRunReturnsProviderExitCodeForProviderError(t *testing.T) {
	fake := newFakeRunner()
	fake.startErr = runtime.WrapProviderError(&provider.HTTPError{
		Provider:   "openai",
		Class:      "auth_error",
		Message:    "bad key",
		StatusCode: 401,
	})
	restore := runnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { runnerLoader = restore }()

	err := Run(context.Background(), []string{"exec", "ping"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected provider error")
	}
	var classified ClassifiedError
	if !errors.As(err, &classified) || classified.Code != 3 {
		t.Fatalf("expected exit code 3, got %v", err)
	}
}

func TestRunReturnsHookExitCodeForFailClosedHook(t *testing.T) {
	fake := newFakeRunner()
	fake.startErr = &hooks.FailClosedError{
		Point: "user.message",
		Name:  "guard",
		Err:   errors.New("blocked"),
	}
	restore := runnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { runnerLoader = restore }()

	err := Run(context.Background(), []string{"exec", "ping"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected hook error")
	}
	var classified ClassifiedError
	if !errors.As(err, &classified) || classified.Code != 5 {
		t.Fatalf("expected exit code 5, got %v", err)
	}
}

func TestDoctorCommandJSONSkipsProbeWhenAPIKeyMissing(t *testing.T) {
	fake := newFakeRunner()
	restore := runnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) {
		cfg := config.Default()
		cfg.DefaultProvider = "openai-compatible"
		cfg.Providers["openai-compatible"] = config.Provider{
			APIKeyEnv: "TEST_MISSING_KEY",
			BaseURL:   "http://example/v1",
			Model:     "gpt-5.4",
			WireAPI:   "responses",
		}
		return fake, cfg, nil
	}
	defer func() { runnerLoader = restore }()
	_ = os.Unsetenv("TEST_MISSING_KEY")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := Run(context.Background(), []string{"doctor", "--provider", "openai-compatible", "--json"}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"name":"provider.probe"`) || !strings.Contains(stdout.String(), `"status":"skip"`) {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"name":"hooks.config"`) || !strings.Contains(stdout.String(), `"name":"hooks.commands"`) || !strings.Contains(stdout.String(), `"name":"session.root.strategy"`) || !strings.Contains(stdout.String(), `"name":"workspace.write"`) {
		t.Fatalf("expected extended doctor checks, got %s", stdout.String())
	}
}

func TestDoctorConfigFileCheckReportsUntrustedWorkspaceConfigSkipped(t *testing.T) {
	cwd := t.TempDir()
	configDir := filepath.Join(cwd, ".go-cli-agent")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	configPath := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("default_provider: local\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	check := doctorConfigFileCheck("", cwd, configPath)
	if check.Status != "warn" {
		t.Fatalf("unexpected status: %#v", check)
	}
	if check.Details["present"] != true || check.Details["loaded"] != false {
		t.Fatalf("expected present but not loaded, got %#v", check.Details)
	}
	if check.Details["reason"] != "workspace_config_not_trusted" {
		t.Fatalf("expected untrusted reason, got %#v", check.Details)
	}
}

func TestDoctorCommandAPIKeyEnvOverrideControlsProbeSkip(t *testing.T) {
	fake := newFakeRunner()
	fake.probeResult = runtime.ProbeResult{
		Provider:      "openai-compatible",
		Model:         "gpt-5.4",
		BaseURL:       "http://example/v1",
		WireAPI:       "responses",
		StopReason:    "tool_use",
		ToolCallNames: []string{"finish"},
		FinishMessage: "provider probe ok",
	}
	restore := runnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) {
		cfg := config.Default()
		cfg.DefaultProvider = "openai-compatible"
		cfg.Providers["openai-compatible"] = config.Provider{
			APIKeyEnv: "TEST_MISSING_KEY",
			BaseURL:   "http://example/v1",
			Model:     "gpt-5.4",
			WireAPI:   "responses",
		}
		return fake, cfg, nil
	}
	defer func() { runnerLoader = restore }()
	_ = os.Unsetenv("TEST_MISSING_KEY")
	t.Setenv("TEST_OVERRIDE_KEY", "present")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := Run(context.Background(), []string{"doctor", "--provider", "openai-compatible", "--api-key-env", "TEST_OVERRIDE_KEY", "--json"}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if len(fake.probeCalls) != 1 {
		t.Fatalf("expected probe to run with override env, got %d calls; output=%s", len(fake.probeCalls), stdout.String())
	}
	if fake.probeCalls[0].APIKeyEnv != "TEST_OVERRIDE_KEY" {
		t.Fatalf("expected probe request to use override env, got %#v", fake.probeCalls[0])
	}
	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("unmarshal report: %v output=%s", err, stdout.String())
	}
	var keyCheck, probeCheck *doctorCheck
	for i := range report.Checks {
		switch report.Checks[i].Name {
		case "provider.api_key_env":
			keyCheck = &report.Checks[i]
		case "provider.probe":
			probeCheck = &report.Checks[i]
		}
	}
	if keyCheck == nil || keyCheck.Status != "ok" || keyCheck.Details["env"] != "TEST_OVERRIDE_KEY" || keyCheck.Details["present"] != true {
		t.Fatalf("expected API key env override to be reported present, got %#v", keyCheck)
	}
	if probeCheck == nil || probeCheck.Status != "ok" {
		t.Fatalf("expected provider probe to run successfully, got %#v", probeCheck)
	}
}

func TestDoctorCommandJSONIncludesEffectiveOpenAICompatibleSettings(t *testing.T) {
	fake := newFakeRunner()
	restore := runnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) {
		cfg := config.Default()
		cfg.DefaultProvider = "openai-compatible"
		store := false
		sendMetadata := false
		cfg.Providers["openai-compatible"] = config.Provider{
			APIKeyEnv:       "TEST_PRESENT_KEY",
			BaseURL:         "http://example/v1",
			Model:           "gpt-5.4",
			TimeoutSec:      240,
			WireAPI:         "responses",
			Store:           &store,
			SendMetadata:    &sendMetadata,
			ReasoningEffort: "medium",
			Retry: config.Retry{
				MaxAttempts:    4,
				BaseDelayMS:    1500,
				Retry429:       true,
				Retry5xx:       true,
				RetryTransport: true,
			},
		}
		return fake, cfg, nil
	}
	defer func() { runnerLoader = restore }()
	t.Setenv("TEST_PRESENT_KEY", "present")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := Run(context.Background(), []string{"doctor", "--provider", "openai-compatible", "--json", "--skip-probe"}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	var providerCheck *doctorCheck
	for i := range report.Checks {
		if report.Checks[i].Name == "provider.config" {
			providerCheck = &report.Checks[i]
			break
		}
	}
	if providerCheck == nil {
		t.Fatalf("provider.config check missing: %#v", report.Checks)
	}
	if got := providerCheck.Details["store"]; got != false {
		t.Fatalf("expected store=false, got %#v", got)
	}
	if got := providerCheck.Details["store_source"]; got != "config" {
		t.Fatalf("expected store_source=config, got %#v", got)
	}
	if got := providerCheck.Details["send_metadata"]; got != false {
		t.Fatalf("expected send_metadata=false, got %#v", got)
	}
	if got := providerCheck.Details["send_metadata_source"]; got != "config" {
		t.Fatalf("expected send_metadata_source=config, got %#v", got)
	}
	if got := providerCheck.Details["request_timeout_sec"]; got != float64(240) {
		t.Fatalf("expected request_timeout_sec=240 from legacy timeout fallback, got %#v", got)
	}
	if got := providerCheck.Details["stream_idle_timeout_ms"]; got != float64(300000) {
		t.Fatalf("expected default stream_idle_timeout_ms=300000, got %#v", got)
	}
	retryPolicy, ok := providerCheck.Details["retry_policy"].(map[string]any)
	if !ok {
		t.Fatalf("expected retry_policy object, got %#v", providerCheck.Details["retry_policy"])
	}
	if got := retryPolicy["max_attempts"]; got != float64(4) {
		t.Fatalf("expected retry max_attempts=4, got %#v", got)
	}
	if got := retryPolicy["base_delay_ms"]; got != float64(1500) {
		t.Fatalf("expected retry base_delay_ms=1500, got %#v", got)
	}
	if got := retryPolicy["retry_transport"]; got != true {
		t.Fatalf("expected retry_transport=true, got %#v", got)
	}
}

func TestTrustedExtensionStatusAppearsInDoctor(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, ".agent", "reviewer"), 0o700); err != nil {
		t.Fatalf("mkdir extension: %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	fake := newFakeRunner()
	restore := runnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) {
		cfg := config.Default()
		cfg.Session.Dir = filepath.Join(workdir, ".go-cli-agent", "sessions")
		cfg.DefaultProvider = "openai-compatible"
		cfg.Providers["openai-compatible"] = config.Provider{
			APIKeyEnv: "TEST_MISSING_KEY",
			BaseURL:   "http://example/v1",
			Model:     "gpt-5.4",
			WireAPI:   "responses",
		}
		return fake, cfg, nil
	}
	defer func() { runnerLoader = restore }()
	_ = os.Unsetenv("TEST_MISSING_KEY")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := Run(context.Background(), []string{"doctor", "--provider", "openai-compatible", "--json", "--skip-probe"}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	var trustCheck *doctorCheck
	for i := range report.Checks {
		if report.Checks[i].Name == "extensions.trust" {
			trustCheck = &report.Checks[i]
			break
		}
	}
	if trustCheck == nil {
		t.Fatalf("extensions.trust check missing: %#v", report.Checks)
	}
	if trustCheck.Status != "ok" || trustCheck.Details["trusted"] != false {
		t.Fatalf("unexpected trust check: %#v", trustCheck)
	}
	if trustCheck.Details["discovery_path"] != filepath.Join(workdir, ".agent") {
		t.Fatalf("unexpected discovery path: %#v", trustCheck.Details)
	}
	candidates, ok := trustCheck.Details["candidates"].([]any)
	if !ok || len(candidates) != 1 {
		t.Fatalf("expected one extension candidate, got %#v", trustCheck.Details["candidates"])
	}
	candidate, ok := candidates[0].(map[string]any)
	if !ok {
		t.Fatalf("expected candidate object, got %#v", candidates[0])
	}
	if candidate["qualified_name"] != "workspace/reviewer" || candidate["disabled"] != true || candidate["disabled_reason"] == "" {
		t.Fatalf("unexpected extension candidate: %#v", candidate)
	}
}

func TestDoctorReportsMissingSessionState(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session_missing_state")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "session.json"), []byte(`{"id":"session_missing_state"}`), 0o600); err != nil {
		t.Fatalf("write session metadata: %v", err)
	}

	check := checkSessionPartialState(root)
	if check.Status != "warn" {
		t.Fatalf("expected warn, got %#v", check)
	}
	missing, ok := check.Details["missing_session_files"].([]map[string]any)
	if !ok || len(missing) != 1 {
		t.Fatalf("expected missing session files, got %#v", check.Details["missing_session_files"])
	}
	if missing[0]["session_id"] != "session_missing_state" {
		t.Fatalf("unexpected missing session detail: %#v", missing[0])
	}
}

func TestDoctorReportsDuplicateQueueJobStatus(t *testing.T) {
	root := t.TempDir()
	job := session.QueueJob{ID: "job_duplicate", Status: session.QueueStatusQueued, Prompt: "hi", Mode: session.ModeExec}
	writeDoctorQueueJob(t, root, session.QueueStatusQueued, job)
	job.Status = session.QueueStatusRunning
	writeDoctorQueueJob(t, root, session.QueueStatusRunning, job)

	check := checkSessionPartialState(root)
	if check.Status != "warn" {
		t.Fatalf("expected warn, got %#v", check)
	}
	duplicates, ok := check.Details["duplicate_queue_jobs"].([]map[string]any)
	if !ok || len(duplicates) != 1 {
		t.Fatalf("expected duplicate queue job, got %#v", check.Details["duplicate_queue_jobs"])
	}
	if duplicates[0]["job_id"] != "job_duplicate" {
		t.Fatalf("unexpected duplicate detail: %#v", duplicates[0])
	}
}

func TestDoctorReportsQueueLeaseAndMissingSessionRef(t *testing.T) {
	root := t.TempDir()
	old := time.Now().UTC().Add(-session.QueueRunningStaleAfter - time.Minute).Format(time.RFC3339Nano)
	writeDoctorQueueJob(t, root, session.QueueStatusRunning, session.QueueJob{
		ID:        "job_running_without_lease",
		Status:    session.QueueStatusRunning,
		Prompt:    "hi",
		Mode:      session.ModeExec,
		UpdatedAt: old,
	})
	writeDoctorQueueJob(t, root, session.QueueStatusCompleted, session.QueueJob{
		ID:        "job_missing_session_ref",
		Status:    session.QueueStatusCompleted,
		Prompt:    "hi",
		Mode:      session.ModeExec,
		SessionID: "session_does_not_exist",
	})

	check := checkSessionPartialState(root)
	if check.Status != "warn" {
		t.Fatalf("expected warn, got %#v", check)
	}
	withoutLease, ok := check.Details["running_jobs_without_lease"].([]map[string]any)
	if !ok || len(withoutLease) != 1 {
		t.Fatalf("expected running job without lease, got %#v", check.Details["running_jobs_without_lease"])
	}
	stale, ok := check.Details["stale_running_jobs"].([]map[string]any)
	if !ok || len(stale) != 1 {
		t.Fatalf("expected stale running job, got %#v", check.Details["stale_running_jobs"])
	}
	missingRefs, ok := check.Details["queue_jobs_missing_session"].([]map[string]any)
	if !ok || len(missingRefs) != 1 {
		t.Fatalf("expected missing session ref, got %#v", check.Details["queue_jobs_missing_session"])
	}
}

func TestDoctorReportsBlockedQueueJobMissingSessionRef(t *testing.T) {
	root := t.TempDir()
	writeDoctorQueueJob(t, root, session.QueueStatusBlocked, session.QueueJob{
		ID:            "job_blocked_missing_session",
		Status:        session.QueueStatusBlocked,
		Prompt:        "continue later",
		Mode:          session.ModeRun,
		SessionID:     "missing_child_session",
		SessionStatus: session.StatusAwaitingInput,
	})

	check := checkSessionPartialState(root)
	if check.Status != "warn" {
		t.Fatalf("expected warn, got %#v", check)
	}
	missingRefs, ok := check.Details["queue_jobs_missing_session"].([]map[string]any)
	if !ok || len(missingRefs) != 1 {
		t.Fatalf("expected blocked missing session ref, got %#v", check.Details["queue_jobs_missing_session"])
	}
	if missingRefs[0]["job_id"] != "job_blocked_missing_session" || missingRefs[0]["status"] != session.QueueStatusBlocked {
		t.Fatalf("unexpected missing session detail: %#v", missingRefs[0])
	}
}

func TestCheckSessionDirModeWarnsOnPermissionDrift(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatalf("chmod drift: %v", err)
	}
	restore := sessionDirModeProbe
	sessionDirModeProbe = func(string, fs.FileMode) (sessionDirModeProbeResult, error) {
		return sessionDirModeProbeResult{
			ProbeMode:     0o700,
			SupportsChmod: true,
		}, nil
	}
	defer func() { sessionDirModeProbe = restore }()

	check := checkSessionDirMode(dir, "0700")
	if check.Status != "warn" {
		t.Fatalf("expected warn, got %#v", check)
	}
	if got := check.Details["reason"]; got != "permission_drift" {
		t.Fatalf("expected permission_drift, got %#v", got)
	}
	if got := check.Details["posix_owner_only_supported"]; got != true {
		t.Fatalf("expected posix_owner_only_supported=true, got %#v", got)
	}
}

func TestCheckSessionDirModeWarnsWhenFilesystemDoesNotHonorPOSIXPermissions(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatalf("chmod drift: %v", err)
	}
	restore := sessionDirModeProbe
	sessionDirModeProbe = func(string, fs.FileMode) (sessionDirModeProbeResult, error) {
		return sessionDirModeProbeResult{
			ProbeMode:     0o777,
			SupportsChmod: false,
			ChmodError:    "",
		}, nil
	}
	defer func() { sessionDirModeProbe = restore }()

	check := checkSessionDirMode(dir, "0700")
	if check.Status != "warn" {
		t.Fatalf("expected warn, got %#v", check)
	}
	if got := check.Details["reason"]; got != "filesystem_does_not_honor_posix_permissions" {
		t.Fatalf("expected filesystem warning, got %#v", got)
	}
	if got := check.Details["posix_owner_only_supported"]; got != false {
		t.Fatalf("expected posix_owner_only_supported=false, got %#v", got)
	}
}

func TestCheckSessionDirModeFailsForInvalidConfiguredMode(t *testing.T) {
	check := checkSessionDirMode(t.TempDir(), "not-octal")
	if check.Status != "fail" {
		t.Fatalf("expected fail, got %#v", check)
	}
	if !strings.Contains(check.Details["error"].(string), "invalid file mode") {
		t.Fatalf("expected invalid mode error, got %#v", check.Details["error"])
	}
}

func TestCheckHookCommandsFailsForMissingFailClosedCommand(t *testing.T) {
	cfg := config.Default()
	cfg.Hooks.SessionComplete = []config.HookDefinition{
		{
			Name:       "notify",
			Command:    []string{"missing-hook-binary"},
			FailClosed: true,
		},
	}
	restore := hookCommandLookPath
	hookCommandLookPath = func(string) (string, error) {
		return "", exec.ErrNotFound
	}
	defer func() { hookCommandLookPath = restore }()

	check := checkHookCommands(cfg, t.TempDir())
	if check.Status != "fail" {
		t.Fatalf("expected fail, got %#v", check)
	}
}

func TestInitGeneratesConfigSkillAndHookAssets(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := Run(context.Background(), []string{"init", "--force", "--provider", "openai-compatible"}, &stdout, &stderr); err != nil {
		t.Fatalf("run init: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "next: ./bin/go-cli-agent doctor") {
		t.Fatalf("expected init guidance in stdout, got %s", stdout.String())
	}
	for _, relative := range []string{
		".go-cli-agent/config.yaml",
		".env.example",
		"workspace",
		"skills/example/SKILL.md",
		"skills/example/tools/echo.yaml",
		".go-cli-agent/hooks/session-complete.sh",
	} {
		if _, err := os.Stat(filepath.Join(tmp, relative)); err != nil {
			t.Fatalf("expected generated file %s: %v", relative, err)
		}
	}
}

func TestDefaultInitSessionDirUsesHomeFallbackForMountedWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := defaultInitSessionDir("/mnt/c/project", ".go-cli-agent/sessions")
	want := filepath.Join(home, ".go-cli-agent", "sessions")
	if got != want {
		t.Fatalf("expected mounted workspace default session dir %q, got %q", want, got)
	}
}

func TestDefaultInitSessionDirKeepsNonMountedWorkspaceDefault(t *testing.T) {
	got := defaultInitSessionDir("/home/user/project", ".go-cli-agent/sessions")
	if got != ".go-cli-agent/sessions" {
		t.Fatalf("expected non-mounted workspace to keep relative default, got %q", got)
	}
}

func TestCheckSessionRootStrategyWarnsAndRecommendsPOSIXFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configured := filepath.Join("/mnt/c/project", ".go-cli-agent", "sessions")
	restore := sessionRootCandidateProbe
	sessionRootCandidateProbe = func(path string, expected fs.FileMode) sessionRootProbeResult {
		switch path {
		case filepath.Clean(configured):
			return sessionRootProbeResult{
				Path:              filepath.Clean(path),
				Writable:          true,
				Mode:              0o777,
				ExpectedMode:      expected,
				SupportsOwnerOnly: false,
				Reason:            "filesystem_does_not_honor_posix_permissions",
			}
		case filepath.Join(home, ".go-cli-agent", "sessions"):
			return sessionRootProbeResult{
				Path:              filepath.Clean(path),
				Writable:          true,
				Mode:              0o700,
				ExpectedMode:      expected,
				SupportsOwnerOnly: true,
				Reason:            "ready",
			}
		default:
			return sessionRootProbeResult{
				Path:              filepath.Clean(path),
				Writable:          true,
				Mode:              0o700,
				ExpectedMode:      expected,
				SupportsOwnerOnly: true,
				Reason:            "ready",
			}
		}
	}
	defer func() { sessionRootCandidateProbe = restore }()

	cfg := config.Default()
	cfg.Session.Dir = configured
	check := checkSessionRootStrategy(cfg)
	if check.Status != "warn" {
		t.Fatalf("expected warn, got %#v", check)
	}
	if got := check.Details["recommended_dir"]; got != filepath.Join(home, ".go-cli-agent", "sessions") {
		t.Fatalf("expected home fallback recommendation, got %#v", got)
	}
}

func TestRunCommandPassesIsolationFlags(t *testing.T) {
	fake := newFakeRunner()
	fake.startResult = runtime.RunResult{SessionID: "s1", Status: session.StatusCompleted}
	restore := runnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { runnerLoader = restore }()

	if err := Run(context.Background(), []string{"exec", "do work", "--isolation", "copy", "--isolation-root", "/tmp/worktrees"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(fake.startCalls) != 1 {
		t.Fatalf("expected one start call, got %d", len(fake.startCalls))
	}
	if fake.startCalls[0].IsolationMode != "copy" || fake.startCalls[0].IsolationRoot != "/tmp/worktrees" {
		t.Fatalf("unexpected isolation request: %#v", fake.startCalls[0])
	}
}

func TestLoadConfigAutoLoadsEnvFile(t *testing.T) {
	cwd := t.TempDir()
	envPath := filepath.Join(cwd, ".env")
	if err := os.WriteFile(envPath, []byte("OPENAI_API_KEY=from-env-file\n"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GO_CLI_AGENT_ENV_FILE", envPath)

	cfg, err := loadConfig("", cwd)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.APIKey("openai"); got != "from-env-file" {
		t.Fatalf("expected openai api key from env file, got %q", got)
	}
}

func TestDelegateCommandDispatchesStructuredRequest(t *testing.T) {
	fake := newFakeRunner()
	fake.delegateResult = runtime.DelegateResult{QueueJobID: "job_1", Status: session.QueueStatusQueued}
	restore := experimentalRunnerLoader
	experimentalRunnerLoader = func(string, string) (experimentalRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { experimentalRunnerLoader = restore }()

	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"experimental", "delegate", "parent-1", "review this", "--agent", "reviewer", "--background", "--isolation", "auto"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("delegate: %v", err)
	}
	if len(fake.delegateCalls) != 1 {
		t.Fatalf("expected delegate call, got %d", len(fake.delegateCalls))
	}
	call := fake.delegateCalls[0]
	if call.ParentSessionID != "parent-1" || call.Prompt != "review this" || call.AgentName != "reviewer" || !call.Background || call.IsolationMode != "auto" {
		t.Fatalf("unexpected delegate request: %#v", call)
	}
	if !strings.Contains(stdout.String(), "queued child job job_1") {
		t.Fatalf("expected queue output, got %s", stdout.String())
	}
}

func TestChildrenCommandReadsChildSessionsAndJobs(t *testing.T) {
	store := session.NewStore(t.TempDir())
	now := time.Now().UTC().Format(time.RFC3339Nano)
	parent := session.SessionMetadata{SchemaVersion: 1, ID: "parent", CreatedAt: now, Workdir: t.TempDir(), RequestedWorkdir: t.TempDir(), Mode: session.ModeRun, Provider: "openai", Model: "gpt-5.4", CompletionPolicy: session.CompletionPolicyInteractive, RootSessionID: "parent"}
	if err := store.Create(parent, session.State{Status: session.StatusCompleted, Phase: "turn_decide", UpdatedAt: now}); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	child := session.SessionMetadata{SchemaVersion: 1, ID: "child", CreatedAt: now, Workdir: t.TempDir(), RequestedWorkdir: t.TempDir(), Mode: session.ModeExec, Provider: "openai", Model: "gpt-5.4", CompletionPolicy: session.CompletionPolicyAutonomous, ParentSessionID: "parent", RootSessionID: "parent", AgentName: "reviewer"}
	if err := store.Create(child, session.State{Status: session.StatusCompleted, Phase: "turn_decide", UpdatedAt: now}); err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := store.EnqueueJob(session.QueueJob{ID: "job_1", Status: session.QueueStatusQueued, ParentSessionID: "parent", Prompt: "hi", Mode: session.ModeExec, AgentName: "batch"}); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	fake := newFakeRunner()
	fake.store = store
	restore := storeRunnerLoader
	storeRunnerLoader = func(string, string) (storeRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { storeRunnerLoader = restore }()

	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"experimental", "children", "parent"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("children: %v", err)
	}
	if !strings.Contains(stdout.String(), "child") || !strings.Contains(stdout.String(), "job_1") {
		t.Fatalf("unexpected children output: %s", stdout.String())
	}
}

func TestTUISnapshotRendersPanels(t *testing.T) {
	store := session.NewStore(t.TempDir())
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := session.SessionMetadata{SchemaVersion: 1, ID: "s1", CreatedAt: now, Workdir: t.TempDir(), RequestedWorkdir: t.TempDir(), Mode: session.ModeRun, Provider: "openai", Model: "gpt-5.4", CompletionPolicy: session.CompletionPolicyInteractive, RootSessionID: "s1"}
	if err := store.Create(meta, session.State{Status: session.StatusCompleted, Phase: "turn_decide", UpdatedAt: now}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.AppendMessage("s1", session.NewMessage("user", "hello world")); err != nil {
		t.Fatalf("append message: %v", err)
	}
	fake := newFakeRunner()
	fake.store = store
	restore := storeRunnerLoader
	storeRunnerLoader = func(string, string) (storeRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { storeRunnerLoader = restore }()

	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"experimental", "tui", "--once"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("tui once: %v", err)
	}
	if !strings.Contains(stdout.String(), "Sessions") || !strings.Contains(stdout.String(), "Recent Events") {
		t.Fatalf("unexpected tui snapshot: %s", stdout.String())
	}
}

func TestQueueWorkerCommandOnceJSONIdle(t *testing.T) {
	fake := newFakeRunner()
	restore := experimentalRunnerLoader
	experimentalRunnerLoader = func(string, string) (experimentalRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { experimentalRunnerLoader = restore }()

	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"experimental", "queue", "worker", "--once", "--json"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("queue worker: %v", err)
	}
	if !strings.Contains(stdout.String(), `"idle":true`) {
		t.Fatalf("expected idle json, got %s", stdout.String())
	}
}

func TestQueueWorkerCommandRejectsNonPositivePollMS(t *testing.T) {
	err := Run(context.Background(), []string{"experimental", "queue", "worker", "--poll-ms", "0"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "poll-ms must be > 0") {
		t.Fatalf("expected poll-ms validation error, got %v", err)
	}
}

func TestQueueWorkerCommandOncePrintsFailedJobWithoutError(t *testing.T) {
	fake := newFakeRunner()
	fake.processJob = session.QueueJob{ID: "job_1", Status: session.QueueStatusFailed, LastError: "boom"}
	fake.processOK = true
	restore := experimentalRunnerLoader
	experimentalRunnerLoader = func(string, string) (experimentalRunner, *config.Config, error) {
		return fake, config.Default(), nil
	}
	defer func() { experimentalRunnerLoader = restore }()

	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"experimental", "queue", "worker", "--once", "--json"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("queue worker: %v", err)
	}
	if !strings.Contains(stdout.String(), `"status":"failed"`) || !strings.Contains(stdout.String(), `"last_error":"boom"`) {
		t.Fatalf("unexpected failed job output: %s", stdout.String())
	}
}

func TestUsageShowsWebFirstSurfaceByDefault(t *testing.T) {
	var stderr bytes.Buffer
	err := Run(context.Background(), []string{"unknown"}, &bytes.Buffer{}, &stderr)
	if err == nil {
		t.Fatal("expected usage error")
	}
	if !strings.Contains(stderr.String(), "usage: go-cli-agent <web|init|run|exec|continue|steer|sessions|goal|tasks|probe-provider|doctor> [...]") {
		t.Fatalf("expected default usage to show web-first surface, got %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "experimental") {
		t.Fatalf("expected default usage to keep experimental commands out, got %q", stderr.String())
	}
}

func TestExperimentalCommandShowsUsageWhenExplicitlyInvoked(t *testing.T) {
	var stderr bytes.Buffer
	err := Run(context.Background(), []string{"experimental"}, &bytes.Buffer{}, &stderr)
	if err == nil {
		t.Fatal("expected experimental usage error")
	}
	if !strings.Contains(stderr.String(), "usage: go-cli-agent experimental <delegate|children|queue|tui|web> [...]") {
		t.Fatalf("expected explicit experimental usage, got %q", stderr.String())
	}
}

func TestLegacyExperimentalAliasReturnsMigrationError(t *testing.T) {
	err := Run(context.Background(), []string{"delegate"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "go-cli-agent experimental delegate") {
		t.Fatalf("expected migration error, got %v", err)
	}
}

func TestTopLevelWebCommandDispatches(t *testing.T) {
	var stderr bytes.Buffer
	err := Run(context.Background(), []string{"web", "-bad-flag"}, &bytes.Buffer{}, &stderr)
	if err == nil {
		t.Fatal("expected web flag parse error")
	}
	if strings.Contains(err.Error(), "go-cli-agent experimental web") {
		t.Fatalf("top-level web should not be treated as experimental migration, got %v", err)
	}
}
