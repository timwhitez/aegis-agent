package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"aegis-agent/internal/config"
	"aegis-agent/internal/runtime"
	"aegis-agent/internal/session"
)

func cliApprovalFixture(t *testing.T) (*session.Store, string) {
	t.Helper()
	store := session.NewStore(t.TempDir())
	id := "session_cli_review"
	if err := store.Create(session.SessionMetadata{SchemaVersion: 1, ID: id, Mode: session.ModeRun, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: t.TempDir(), RequestedWorkdir: t.TempDir(), Provider: "openai", Model: "gpt-5.4", CompletionPolicy: session.CompletionPolicyInteractive, RootSessionID: id}, session.State{Status: session.StatusAwaitingInput}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePlanMode(id, session.PlanModeDraft{Enabled: true, Objective: "Reviewed objective", Source: session.PlanModeSourceCLI}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SubmitPlanMode(id, session.PlanModeSubmitInput{Title: "Reviewed title", Summary: "Reviewed summary", PlanMarkdown: "# Reviewed plan\n\nDo reviewed work.", Verification: []string{"verify reviewed work"}, Source: session.PlanModeSourceTool}); err != nil {
		t.Fatal(err)
	}
	return store, id
}

type approvalReadFunc func([]byte) (int, error)

func (read approvalReadFunc) Read(data []byte) (int, error) { return read(data) }

func TestInteractiveApprovalCapturesDisplayedTargetDuringConfirmation(t *testing.T) {
	store, id := cliApprovalFixture(t)
	seen, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	restoreTTY := stdinIsTerminal
	stdinIsTerminal = func() bool { return true }
	defer func() { stdinIsTerminal = restoreTTY }()
	var displayed bytes.Buffer
	reader := approvalReadFunc(func(data []byte) (int, error) {
		if _, err := store.RevisePlanMode(id, session.PlanModeSourceCLI, "Review a changed plan"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.SubmitPlanMode(id, session.PlanModeSubmitInput{Title: "Changed title", Summary: "Changed summary", PlanMarkdown: "# Changed plan\n\nDo other work.", Verification: []string{"verify changed work"}, Source: session.PlanModeSourceTool}); err != nil {
			t.Fatal(err)
		}
		return copy(data, "yes\n"), io.EOF
	})
	target, err := resolveCLIApprovalTarget(context.Background(), store, id, &cliApprovalFlags{}, reader, &displayed)
	if err != nil {
		t.Fatal(err)
	}
	if *target != seen.Target() {
		t.Fatalf("target drifted from displayed content: got %#v want %#v", target, seen.Target())
	}
	if !strings.Contains(displayed.String(), seen.Revision) || !strings.Contains(displayed.String(), "Reviewed plan") {
		t.Fatalf("missing reviewed content/target: %s", displayed.String())
	}
	current, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.ValidateApprovalTarget(current, *target); err == nil {
		t.Fatal("changed content accepted displayed target")
	}
}

func TestContinueApproveLatestExplicitlyCapturesCurrentTarget(t *testing.T) {
	store, id := cliApprovalFixture(t)
	seen, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	fake := newFakeRunner()
	fake.store = store
	fake.continueResult = runtime.RunResult{SessionID: id, Status: session.StatusCompleted}
	restoreRunner, restoreStore := runnerLoader, storeRunnerLoader
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) { return fake, config.Default(), nil }
	storeRunnerLoader = func(string, string) (storeRunner, *config.Config, error) { return fake, config.Default(), nil }
	defer func() { runnerLoader, storeRunnerLoader = restoreRunner, restoreStore }()
	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), []string{"continue", id, "--approve-latest", "--json"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if len(fake.continueCalls) != 1 || !fake.continueCalls[0].ApprovePlan || fake.continueCalls[0].ApprovalTarget == nil || *fake.continueCalls[0].ApprovalTarget != seen.Target() {
		t.Fatalf("latest shortcut did not capture target: %#v", fake.continueCalls)
	}
	if strings.Contains(stderr.String(), "[y/N]") {
		t.Fatalf("explicit latest shortcut asked interactive confirmation: %s", stderr.String())
	}
}

func TestInteractiveApprovalCancellationDoesNotReturnTarget(t *testing.T) {
	store, id := cliApprovalFixture(t)
	restoreTTY := stdinIsTerminal
	stdinIsTerminal = func() bool { return true }
	defer func() { stdinIsTerminal = restoreTTY }()
	target, err := resolveCLIApprovalTarget(context.Background(), store, id, &cliApprovalFlags{}, strings.NewReader("no\n"), io.Discard)
	if err == nil || target != nil {
		t.Fatalf("declined review returned approval target: %#v %v", target, err)
	}
}

func TestLinkedMissionCLIShowsAndForwardsReviewedTarget(t *testing.T) {
	store, id := cliApprovalFixture(t)
	goal, err := store.CreateGoal(id, session.GoalDraft{Enabled: true, Mode: session.GoalModeMission, Objective: "Reviewed mission", Features: []string{"Reviewed feature"}, Source: session.GoalSourceCLI})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := store.LoadPlanMode(id)
	if err != nil {
		t.Fatal(err)
	}
	plan.LinkedGoalID = goal.GoalID
	if err := store.SavePlanMode(id, plan); err != nil {
		t.Fatal(err)
	}
	seen, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	fake := newFakeRunner()
	fake.store = store
	fake.continueResult = runtime.RunResult{SessionID: id, Status: session.StatusCompleted}
	restoreRunner, restoreStore, restoreTTY := runnerLoader, storeRunnerLoader, stdinIsTerminal
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) { return fake, config.Default(), nil }
	storeRunnerLoader = func(string, string) (storeRunner, *config.Config, error) { return fake, config.Default(), nil }
	stdinIsTerminal = func() bool { return false }
	defer func() { runnerLoader, storeRunnerLoader, stdinIsTerminal = restoreRunner, restoreStore, restoreTTY }()
	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), []string{"goal", "plan", "show", id, "--json"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var shown struct {
		Target   session.ApprovalTarget   `json:"approval_target"`
		Features []session.MissionFeature `json:"features"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &shown); err != nil {
		t.Fatal(err)
	}
	if shown.Target != seen.Target() || len(shown.Features) != 1 || shown.Features[0].Title != "Reviewed feature" {
		t.Fatalf("show lost reviewed mission/target: %s", stdout.String())
	}
	if err := Run(context.Background(), []string{"goal", "plan", "approve", id, "--json"}, io.Discard, io.Discard); err == nil || len(fake.continueCalls) != 0 {
		t.Fatalf("linked mission missing target entered runtime: %v %#v", err, fake.continueCalls)
	}
	if err := Run(context.Background(), []string{"goal", "plan", "approve", id, "--json", "--plan-mode-id", shown.Target.PlanModeID, "--plan-version", "1", "--expected-revision", shown.Target.ExpectedRevision}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(fake.continueCalls) != 1 || fake.continueCalls[0].ApprovalTarget == nil || *fake.continueCalls[0].ApprovalTarget != shown.Target {
		t.Fatalf("linked alias lost reviewed target: %#v", fake.continueCalls)
	}
	if err := store.RestorePlanModeSnapshot(id, session.PlanModeSnapshot{}); err != nil {
		t.Fatal(err)
	}
	err = Run(context.Background(), []string{"goal", "plan", "approve", id, "--plan-mode-id", shown.Target.PlanModeID, "--plan-version", "1", "--expected-revision", shown.Target.ExpectedRevision}, io.Discard, io.Discard)
	if !errors.Is(err, session.ErrApprovalConflict) {
		t.Fatalf("removed gate silently narrowed target to pure facts approval: %v", err)
	}
}

func TestCLIExecutingMissionRepairPreservesHistoricalApprovalScope(t *testing.T) {
	for _, test := range []struct {
		name            string
		legacy, changed bool
	}{
		{name: "known unchanged control"},
		{name: "legacy remains unknown", legacy: true},
		{name: "known changed fresh target rejected", changed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, id := cliApprovalFixture(t)
			goal, err := store.CreateGoal(id, session.GoalDraft{Enabled: true, Mode: session.GoalModeMission, Objective: "Repair reviewed mission facts", Features: []string{"Original feature scope"}, Source: session.GoalSourceCLI})
			if err != nil {
				t.Fatal(err)
			}
			plan, err := store.LoadPlanMode(id)
			if err != nil {
				t.Fatal(err)
			}
			plan.LinkedGoalID = goal.GoalID
			if err := store.SavePlanMode(id, plan); err != nil {
				t.Fatal(err)
			}
			approved, err := store.LoadApprovalSnapshot(id)
			if err != nil {
				t.Fatal(err)
			}
			if test.legacy {
				_, err = store.ApprovePlanMode(id, session.PlanModeSourceCLI)
			} else {
				_, err = store.ApprovePlanModeTarget(id, session.PlanModeSourceCLI, approved.Target(), false)
			}
			if err != nil {
				t.Fatal(err)
			}
			plan, err = store.MarkPlanModeExecuting(id, session.PlanModeSourceCLI)
			if err != nil {
				t.Fatal(err)
			}
			if test.changed {
				goal.Mission.Features[0].Title = "Changed unapproved feature scope"
				if err := store.SaveGoal(id, goal); err != nil {
					t.Fatal(err)
				}
			}
			beforeGoal, err := store.LoadGoal(id)
			if err != nil {
				t.Fatal(err)
			}
			beforeHistory, err := store.LoadGoalHistory(id)
			if err != nil {
				t.Fatal(err)
			}
			beforePlanHistory, err := store.LoadPlanModeHistory(id)
			if err != nil {
				t.Fatal(err)
			}
			beforeEvents, err := store.LoadEvents(id)
			if err != nil {
				t.Fatal(err)
			}
			fake := newFakeRunner()
			fake.store = store
			restoreRunner, restoreStore := runnerLoader, storeRunnerLoader
			runnerLoader = func(string, string) (coreRunner, *config.Config, error) { return fake, config.Default(), nil }
			storeRunnerLoader = func(string, string) (storeRunner, *config.Config, error) { return fake, config.Default(), nil }
			defer func() { runnerLoader, storeRunnerLoader = restoreRunner, restoreStore }()
			err = Run(context.Background(), []string{"goal", "plan", "approve", id, "--approve-latest", "--json"}, io.Discard, io.Discard)
			afterGoal, loadErr := store.LoadGoal(id)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			afterHistory, loadErr := store.LoadGoalHistory(id)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			afterEvents, loadErr := store.LoadEvents(id)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if test.changed {
				if !errors.Is(err, session.ErrApprovalConflict) {
					t.Fatalf("fresh current target repaired changed historical scope: %v", err)
				}
				if !reflect.DeepEqual(beforeGoal, afterGoal) || !reflect.DeepEqual(beforeHistory, afterHistory) || !reflect.DeepEqual(beforeEvents, afterEvents) {
					t.Fatal("conflict changed mission approval facts")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if afterGoal.Mission.ApprovedRevision != plan.ApprovedRevision {
					t.Fatalf("repair backfilled historical revision: got %q want %q", afterGoal.Mission.ApprovedRevision, plan.ApprovedRevision)
				}
				if afterGoal.Mission.PlanStatus != session.MissionPlanStatusApproved {
					t.Fatalf("repair did not approve existing scope facts: %#v", afterGoal.Mission)
				}
				for _, entry := range afterHistory[len(beforeHistory):] {
					if entry.Type == "mission.plan.approved" && entry.Data["approved_revision"] != plan.ApprovedRevision {
						t.Fatalf("history invented revision: %#v", entry)
					}
				}
				for _, event := range afterEvents[len(beforeEvents):] {
					if event.Type == "mission.plan.approved" && event.Data["approved_revision"] != plan.ApprovedRevision {
						t.Fatalf("event invented revision: %#v", event)
					}
				}
			}
			afterPlan, loadErr := store.LoadPlanMode(id)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			afterPlanHistory, loadErr := store.LoadPlanModeHistory(id)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if !reflect.DeepEqual(plan, afterPlan) || !reflect.DeepEqual(beforePlanHistory, afterPlanHistory) {
				t.Fatal("fact repair rewrote prior Plan Mode approval/history")
			}
			if len(fake.continueCalls) != 0 {
				t.Fatalf("fact repair launched provider execution: %#v", fake.continueCalls)
			}
		})
	}
}
