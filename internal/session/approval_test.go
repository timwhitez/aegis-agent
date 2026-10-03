package session

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newApprovalTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	store, id := newPlanModeTestStore(t)
	if _, err := store.CreatePlanMode(id, PlanModeDraft{Enabled: true, Objective: "Review the implementation"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SubmitPlanMode(id, PlanModeSubmitInput{Title: "Plan", Summary: "Reviewed scope", PlanMarkdown: "# Plan\nImplement V1", Verification: []string{"go test"}}); err != nil {
		t.Fatal(err)
	}
	return store, id
}

func newLinkedApprovalTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	store, id := newPlanModeTestStore(t)
	goal, err := store.CreateGoal(id, GoalDraft{
		Enabled: true, Mode: GoalModeMission, Objective: "Reviewed mission",
		ValidationPlan: []string{"go test"}, Features: []string{"Feature"}, Milestones: []string{"Milestone"},
		RequirePlanApproval: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.EnsurePlanModeForGoal(id, goal, PlanModeSourceWeb); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SubmitPlanMode(id, PlanModeSubmitInput{Title: "Plan", Summary: "Reviewed scope", PlanMarkdown: "# Plan\nImplement mission", Verification: []string{"go test"}}); err != nil {
		t.Fatal(err)
	}
	return store, id
}

func TestApprovalRevisionIncludesProgressCoverageMappings(t *testing.T) {
	for _, kind := range []string{"claimed_assertions", "validation_ids", "feature_ids"} {
		t.Run(kind, func(t *testing.T) {
			store, id := newLinkedApprovalTestStore(t)
			seen, err := store.LoadApprovalSnapshot(id)
			if err != nil {
				t.Fatal(err)
			}
			input := GoalProgressInput{Kind: "progress", Summary: "Mapping update"}
			switch kind {
			case "claimed_assertions":
				input.FeatureUpdates = []MissionFeatureProgressUpdate{{ID: "feature_0001", ClaimedAssertions: []string{"validation_0001"}}}
			case "validation_ids":
				input.MilestoneUpdates = []MissionMilestoneProgressUpdate{{ID: "milestone_0001", ValidationIDs: []string{"validation_0001"}}}
			case "feature_ids":
				input.MilestoneUpdates = []MissionMilestoneProgressUpdate{{ID: "milestone_0001", FeatureIDs: []string{"feature_0001"}}}
			}
			if _, _, err := store.RecordGoalProgress(id, input); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ApprovePlanModeTarget(id, PlanModeSourceWeb, seen.Target(), true); !errors.Is(err, ErrApprovalConflict) {
				t.Fatalf("coverage override cannot authorize a changed mapping: %v", err)
			}
			current, err := store.LoadApprovalSnapshot(id)
			if err != nil {
				t.Fatal(err)
			}
			if current.PlanMode.PlanVersion != seen.PlanMode.PlanVersion || current.Revision == seen.Revision {
				t.Fatalf("mapping must change only semantic revision: %#v", current)
			}
		})
	}
}

func TestApprovalRevisionExcludesAccountingStatusAndEvidence(t *testing.T) {
	store, id := newLinkedApprovalTestStore(t)
	seen, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.UpdateGoalAccounting(id, GoalUsageDelta{TokensUsedDelta: 11, TimeUsedSecondsDelta: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetGoalStatus(id, GoalStatusPaused, GoalSourceWeb); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RecordGoalProgress(id, GoalProgressInput{
		Kind: "progress", Summary: "Work recorded", Evidence: []string{"new evidence"},
		FeatureUpdates:    []MissionFeatureProgressUpdate{{ID: "feature_0001", Status: "completed", Evidence: []string{"feature evidence"}, TaskIDs: []string{"task_0001"}, ChildSessionIDs: []string{"child_1"}}},
		MilestoneUpdates:  []MissionMilestoneProgressUpdate{{ID: "milestone_0001", Status: "completed", Evidence: []string{"milestone evidence"}}},
		ValidationUpdates: []GoalValidationProgressUpdate{{ID: "validation_0001", Status: "verified", Evidence: []string{"validation evidence"}}},
	}); err != nil {
		t.Fatal(err)
	}
	current, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision != seen.Revision {
		t.Fatalf("unrelated progress produced false conflict: %s -> %s", seen.Revision, current.Revision)
	}
	approved, err := store.ApprovePlanModeTarget(id, PlanModeSourceWeb, seen.Target(), true)
	if err != nil {
		t.Fatal(err)
	}
	if approved.ApprovedRevision != seen.Revision || approved.Approvals[len(approved.Approvals)-1].ApprovedRevision != seen.Revision {
		t.Fatalf("missing matched revision: %#v", approved)
	}
	history, err := store.LoadPlanModeHistory(id)
	if err != nil {
		t.Fatal(err)
	}
	if history[len(history)-1].Data["approved_revision"] != seen.Revision {
		t.Fatalf("approval history omitted revision: %#v", history)
	}
	if _, err := store.MarkPlanModeExecuting(id, PlanModeSourceWeb); err != nil {
		t.Fatal(err)
	}
	executing, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	if executing.Revision != seen.Revision {
		t.Fatal("approval/executing changed content revision")
	}
}

func TestApprovalRejectsReplacedPlanAndMissingLinkedScope(t *testing.T) {
	for _, change := range []string{"replace_plan", "clear_goal", "replace_goal", "remove_mission"} {
		t.Run(change, func(t *testing.T) {
			store, id := newLinkedApprovalTestStore(t)
			seen, err := store.LoadApprovalSnapshot(id)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "replace_plan":
				if _, err := store.CreatePlanMode(id, PlanModeDraft{Enabled: true, Objective: "Replacement"}); err != nil {
					t.Fatal(err)
				}
				if _, err := store.SubmitPlanMode(id, PlanModeSubmitInput{Title: "Plan", Summary: "Replacement", PlanMarkdown: "# Replacement", Verification: []string{"go test"}}); err != nil {
					t.Fatal(err)
				}
			case "clear_goal":
				if _, err := store.ClearGoal(id); err != nil {
					t.Fatal(err)
				}
			case "replace_goal":
				if _, err := store.ClearGoal(id); err != nil {
					t.Fatal(err)
				}
				if _, err := store.CreateGoal(id, GoalDraft{Enabled: true, Objective: "New goal"}); err != nil {
					t.Fatal(err)
				}
			case "remove_mission":
				if _, _, err := store.MutateGoal(id, func(goal *SessionGoal) error { goal.Mode = GoalModeGoal; goal.Mission = nil; return nil }); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.ApprovePlanModeTarget(id, PlanModeSourceWeb, seen.Target(), true); !errors.Is(err, ErrApprovalConflict) {
				t.Fatalf("changed approval scope accepted: %v", err)
			}
		})
	}
}

func TestApprovalSnapshotCoordinatesGoalAndPlanAcrossStores(t *testing.T) {
	store, id := newLinkedApprovalTestStore(t)
	other := NewStore(store.Root())
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- store.WithApprovalLock(id, func(scoped *Store) error {
			if _, _, err := scoped.MutateGoal(id, func(goal *SessionGoal) error { goal.Objective = "Joint replacement"; return nil }); err != nil {
				return err
			}
			close(started)
			<-release
			_, _, err := scoped.MutatePlanMode(id, func(plan *PlanModeState) error { plan.Objective = "Joint replacement"; return nil })
			return err
		})
	}()
	<-started
	result := make(chan ApprovalSnapshot, 1)
	readErr := make(chan error, 1)
	go func() { snapshot, err := other.LoadApprovalSnapshot(id); result <- snapshot; readErr <- err }()
	select {
	case snapshot := <-result:
		close(release)
		t.Fatalf("snapshot escaped joint lock: %#v", snapshot)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	snapshot := <-result
	if err := <-readErr; err != nil {
		t.Fatal(err)
	}
	if snapshot.Goal.Objective != snapshot.PlanMode.Objective {
		t.Fatalf("inconsistent snapshot: %#v", snapshot)
	}
}

func TestApprovalScopeSupportsSameStoreCallsAndExpires(t *testing.T) {
	store, id := newApprovalTestStore(t)
	var retained *Store
	if err := store.WithApprovalLock(id, func(scoped *Store) error {
		retained = scoped
		snapshot, err := scoped.LoadApprovalSnapshot(id)
		if err != nil {
			return err
		}
		_, err = scoped.ApprovePlanModeTarget(id, PlanModeSourceWeb, snapshot.Target(), false)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := retained.MutatePlanMode(id, func(plan *PlanModeState) error { plan.Objective = "Bypass"; return nil }); !errors.Is(err, ErrApprovalScopeClosed) {
		t.Fatalf("expired scope bypassed lock: %v", err)
	}
}

func TestApprovalHistoryFailureRestoresUnapprovedSnapshot(t *testing.T) {
	store, id := newApprovalTestStore(t)
	seen, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	blockPlanModeHistoryPath(t, store, id)
	if _, err := store.ApprovePlanModeTarget(id, PlanModeSourceWeb, seen.Target(), false); err == nil {
		t.Fatal("expected history write failure")
	}
	current, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	if current.PlanMode.ApprovedVersion != 0 || current.PlanMode.ApprovedRevision != "" || len(current.PlanMode.Approvals) != 0 || current.PlanMode.Status != PlanModeStatusAwaitingApproval || current.Revision != seen.Revision {
		t.Fatalf("partial approval reported in snapshot: %#v", current)
	}
}

func TestApprovalExplicitLegacyExecutingRecoveryCreatesNewRevisionFact(t *testing.T) {
	store, id := newApprovalTestStore(t)
	legacy, err := store.ApprovePlanMode(id, PlanModeSourceCLI)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.ApprovedRevision != "" || legacy.Approvals[0].ApprovedRevision != "" {
		t.Fatal("legacy approval fabricated a reviewed revision")
	}
	if _, err := store.MarkPlanModeExecuting(id, PlanModeSourceCLI); err != nil {
		t.Fatal(err)
	}
	seen, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := store.ApprovePlanModeTarget(id, PlanModeSourceWeb, seen.Target(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(approved.Approvals) != 2 || approved.Approvals[0].ApprovedRevision != "" || approved.Approvals[1].ApprovedRevision != seen.Revision {
		t.Fatalf("legacy history was rewritten instead of a new explicit review: %#v", approved)
	}
	if _, err := store.MarkPlanModeExecuting(id, PlanModeSourceWeb); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.MutatePlanMode(id, func(plan *PlanModeState) error { plan.Summary = "New review content at same version"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApprovePlanModeTarget(id, PlanModeSourceWeb, seen.Target(), false); !errors.Is(err, ErrApprovalConflict) {
		t.Fatalf("old executing target authorized new content: %v", err)
	}
	fresh, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	newApproval, err := store.ApprovePlanModeTarget(id, PlanModeSourceWeb, fresh.Target(), false)
	if err != nil {
		t.Fatal(err)
	}
	if newApproval.ApprovedRevision != fresh.Revision || newApproval.ApprovedRevision == seen.Revision {
		t.Fatalf("new scope did not retain its own revision: %#v", newApproval)
	}
}

func TestApprovalDerivedRevisionIsNotPersisted(t *testing.T) {
	store, id := newApprovalTestStore(t)
	seen, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	plan := seen.PlanMode
	plan.ApprovalRevision = seen.Revision
	if err := store.SavePlanMode(id, plan); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadPlanMode(id)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ApprovalRevision != "" {
		t.Fatalf("derived review token persisted as state: %#v", loaded)
	}
}

func TestApprovalMissingTargetAndCoverageDoNotWrite(t *testing.T) {
	store, id := newLinkedApprovalTestStore(t)
	seen, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApprovePlanModeTarget(id, PlanModeSourceWeb, ApprovalTarget{}, false); !errors.Is(err, ErrMissingApprovalTarget) {
		t.Fatalf("missing target: %v", err)
	}
	if _, err := store.ApprovePlanModeTarget(id, PlanModeSourceWeb, seen.Target(), false); err == nil || !strings.Contains(err.Error(), "coverage blocks approval") {
		t.Fatalf("coverage gate: %v", err)
	}
	current, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	if current.PlanMode.ApprovedVersion != 0 || len(current.PlanMode.Approvals) != 0 {
		t.Fatalf("rejected approval changed facts: %#v", current)
	}
}

func TestApprovalSnapshotWithoutPlanAndCorruptGoal(t *testing.T) {
	store, id := newPlanModeTestStore(t)
	if _, err := store.CreateGoal(id, GoalDraft{Enabled: true, Objective: "Goal without plan"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PlanMode.PlanModeID != "" || snapshot.Goal == nil {
		t.Fatalf("goal unavailable without plan: %#v", snapshot)
	}
	if err := os.WriteFile(filepath.Join(store.SessionDir(id), "goal.json"), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadApprovalSnapshot(id); err == nil {
		t.Fatal("corrupt goal ignored")
	}
}

func TestApprovalLockAcrossProcesses(t *testing.T) {
	if root := os.Getenv("AEGIS_APPROVAL_LOCK_TEST_ROOT"); root != "" {
		id := os.Getenv("AEGIS_APPROVAL_LOCK_TEST_SESSION")
		marker := os.Getenv("AEGIS_APPROVAL_LOCK_TEST_MARKER")
		if err := os.WriteFile(marker+".started", nil, 0600); err != nil {
			t.Fatal(err)
		}
		store := NewStore(root)
		if _, _, err := store.MutatePlanMode(id, func(plan *PlanModeState) error { plan.Summary = "Other process"; return nil }); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker+".done", nil, 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	store, id := newApprovalTestStore(t)
	marker := filepath.Join(t.TempDir(), "child")
	child := exec.Command(os.Args[0], "-test.run=^TestApprovalLockAcrossProcesses$", "-test.timeout=10s")
	child.Env = append(os.Environ(), "AEGIS_APPROVAL_LOCK_TEST_ROOT="+store.Root(), "AEGIS_APPROVAL_LOCK_TEST_SESSION="+id, "AEGIS_APPROVAL_LOCK_TEST_MARKER="+marker)
	if err := store.WithApprovalLock(id, func(scoped *Store) error {
		if err := child.Start(); err != nil {
			return err
		}
		deadline := time.Now().Add(2 * time.Second)
		for {
			if _, err := os.Stat(marker + ".started"); err == nil {
				break
			}
			if time.Now().After(deadline) {
				return errors.New("child did not start")
			}
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(30 * time.Millisecond)
		if _, err := os.Stat(marker + ".done"); !errors.Is(err, os.ErrNotExist) {
			return fmtApprovalTestError("other process escaped approval lock", err)
		}
		return nil
	}); err != nil {
		_ = child.Process.Kill()
		_ = child.Wait()
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker + ".done"); err != nil {
		t.Fatal(err)
	}
	plan, err := store.LoadPlanMode(id)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Summary != "Other process" {
		t.Fatalf("missing cross-process mutation: %#v", plan)
	}
}

func fmtApprovalTestError(message string, err error) error {
	if err != nil {
		return errors.New(message + ": " + err.Error())
	}
	return errors.New(message)
}

func TestApprovalRejectsPreviouslyReviewedVersion(t *testing.T) {
	store, id := newApprovalTestStore(t)
	seen, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RevisePlanMode(id, PlanModeSourceCLI, "Revise scope"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SubmitPlanMode(id, PlanModeSubmitInput{Title: "Plan", Summary: "Changed scope", PlanMarkdown: "# Plan\nImplement V2", Verification: []string{"go test"}}); err != nil {
		t.Fatal(err)
	}
	before, err := store.LoadPlanModeHistory(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApprovePlanModeTarget(id, PlanModeSourceWeb, seen.Target(), false); !errors.Is(err, ErrApprovalConflict) {
		t.Fatalf("old review must conflict, got %v", err)
	}
	current, err := store.LoadPlanMode(id)
	if err != nil {
		t.Fatal(err)
	}
	after, err := store.LoadPlanModeHistory(id)
	if err != nil {
		t.Fatal(err)
	}
	if current.ApprovedVersion != 0 || current.Status != PlanModeStatusAwaitingApproval || len(after) != len(before) {
		t.Fatalf("stale approval modified facts: %#v, history %d -> %d", current, len(before), len(after))
	}
}
