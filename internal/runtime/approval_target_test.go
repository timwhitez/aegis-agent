package runtime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aegis-agent/internal/config"
	"aegis-agent/internal/events"
	"aegis-agent/internal/session"
)

func newApprovalTargetFixture(t *testing.T) (*Runner, string, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"approved-target","status":"completed","output":[{"type":"function_call","call_id":"call_finish","name":"finish","arguments":"{\"message\":\"reviewed plan completed\"}"}],"usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	t.Cleanup(server.Close)
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	cfg.DefaultProvider = "openai-compatible"
	cfg.Providers["openai-compatible"] = config.Provider{APIProvider: "openai-compatible", APIKeyEnv: "AEGIS_APPROVAL_TEST_KEY", BaseURL: server.URL + "/v1", Model: "test", TimeoutSec: 3, RequestTimeoutSec: 3, WireAPI: "responses", Retry: config.Retry{MaxAttempts: 1}}
	t.Setenv("AEGIS_APPROVAL_TEST_KEY", "test-key")
	r := NewRunner(cfg)
	id := session.NewSessionID()
	meta := session.SessionMetadata{SchemaVersion: 1, ID: id, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: t.TempDir(), Mode: session.ModeRun, Provider: cfg.DefaultProvider, Model: "test", CompletionPolicy: session.CompletionPolicyInteractive}
	if err := r.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_approval"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.CreatePlanMode(id, session.PlanModeDraft{Enabled: true, Objective: "Review this exact plan"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.SubmitPlanMode(id, session.PlanModeSubmitInput{Title: "Plan", Summary: "Reviewed summary", PlanMarkdown: "# Plan\n\nDo the reviewed work.", Verification: []string{"unit tests"}, Source: session.PlanModeSourceTool}); err != nil {
		t.Fatal(err)
	}
	return r, id, &calls
}

func TestContinueApprovalRequiresReviewedTargetBeforeClaim(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	_, err := r.Continue(context.Background(), ContinueRequest{SessionID: id, ApprovePlan: true})
	if err == nil || !strings.Contains(err.Error(), "approval target") {
		t.Fatalf("missing target must request reload/upgrade: %v", err)
	}
	state, err := r.store.LoadState(id)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != session.StatusAwaitingInput || state.Phase != "plan_approval" {
		t.Fatalf("missing target claimed session: %#v", state)
	}
	plan, err := r.store.LoadPlanMode(id)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != session.PlanModeStatusAwaitingApproval || len(plan.Approvals) != 0 {
		t.Fatalf("missing target approved latest: %#v", plan)
	}
	messages, err := r.store.LoadMessages(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("missing target wrote replay messages: %#v", messages)
	}
	if calls.Load() != 0 {
		t.Fatal("missing target invoked provider")
	}
}

func approvalTargetForTest(t *testing.T, r *Runner, id string) *session.ApprovalTarget {
	t.Helper()
	snapshot, err := r.store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	target := snapshot.Target()
	return &target
}

func assertApprovalRejectedWithoutFacts(t *testing.T, r *Runner, id string, calls *atomic.Int32, target *session.ApprovalTarget) {
	t.Helper()
	beforeEvents, err := r.store.LoadEvents(id)
	if err != nil {
		t.Fatal(err)
	}
	beforePlan, err := r.store.LoadPlanMode(id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.PrepareApprovalContinue(context.Background(), ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: target, OverrideGoalCoverage: true})
	if !errors.Is(err, session.ErrApprovalConflict) {
		t.Fatalf("expected synchronous stale conflict: %v", err)
	}
	state, err := r.store.LoadState(id)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != session.StatusAwaitingInput {
		t.Fatalf("stale approval changed recoverable state: %#v", state)
	}
	afterPlan, err := r.store.LoadPlanMode(id)
	if err != nil {
		t.Fatal(err)
	}
	if afterPlan.Status != beforePlan.Status || len(afterPlan.Approvals) != len(beforePlan.Approvals) {
		t.Fatal("stale approval wrote approval state")
	}
	events, err := r.store.LoadEvents(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != len(beforeEvents) {
		t.Fatalf("stale approval appended execution facts: %#v", events)
	}
	messages, err := r.store.LoadMessages(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("stale approval appended replay: %#v", messages)
	}
	if calls.Load() != 0 {
		t.Fatal("stale approval invoked provider")
	}
}

func TestPrepareApprovalRejectsChangedVersionAndModeBeforeClaim(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "version", true: "mode"}[replacement], func(t *testing.T) {
			r, id, calls := newApprovalTargetFixture(t)
			target := approvalTargetForTest(t, r, id)
			if replacement {
				if _, err := r.store.CreatePlanMode(id, session.PlanModeDraft{Enabled: true, Objective: "Replacement"}); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := r.store.RevisePlanMode(id, session.PlanModeSourceWeb, "new scope"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := r.store.SubmitPlanMode(id, session.PlanModeSubmitInput{Title: "Updated", Summary: "changed", PlanMarkdown: "# Updated plan", Verification: []string{"tests"}, Source: session.PlanModeSourceTool}); err != nil {
				t.Fatal(err)
			}
			assertApprovalRejectedWithoutFacts(t, r, id, calls, target)
		})
	}
}

func linkApprovalMissionForTest(t *testing.T, r *Runner, id string) session.SessionGoal {
	t.Helper()
	goal, err := r.store.CreateGoal(id, session.GoalDraft{Enabled: true, Mode: session.GoalModeMission, Objective: "Reviewed mission", RequirePlanApproval: true, Source: session.GoalSourceCLI})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.store.MutatePlanMode(id, func(plan *session.PlanModeState) error { plan.LinkedGoalID = goal.GoalID; return nil }); err != nil {
		t.Fatal(err)
	}
	return goal
}

func TestPrepareApprovalRejectsChangedLinkedScopeAndKeepsOverrideTarget(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	goal := linkApprovalMissionForTest(t, r, id)
	target := approvalTargetForTest(t, r, id)
	goal.Mission.Requirements = append(goal.Mission.Requirements, session.MissionRequirement{ID: "new", Text: "Additional reviewed requirement"})
	if err := r.store.SaveGoal(id, goal); err != nil {
		t.Fatal(err)
	}
	assertApprovalRejectedWithoutFacts(t, r, id, calls, target)
}

func TestPrepareApprovalPersistsRevisionAndReplayBeforeProvider(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	target := approvalTargetForTest(t, r, id)
	prepared, err := r.PrepareApprovalContinue(context.Background(), ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: target})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.SessionID() != id || calls.Load() != 0 {
		t.Fatal("preparation should claim without calling provider")
	}
	plan, err := r.store.LoadPlanMode(id)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != session.PlanModeStatusExecuting || plan.ApprovedRevision != target.ExpectedRevision {
		t.Fatalf("wrong approved scope: %#v", plan)
	}
	history, err := r.store.LoadPlanModeHistory(id)
	if err != nil {
		t.Fatal(err)
	}
	var approved bool
	for _, entry := range history {
		if entry.Type == "planmode.plan_approved" {
			approved = true
			if entry.Data["approved_revision"] != target.ExpectedRevision {
				t.Fatal("approval history lost revision")
			}
		}
	}
	if !approved {
		t.Fatal("missing approval history")
	}
	messages, err := r.store.LoadMessages(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Meta["approved_revision"] != target.ExpectedRevision {
		t.Fatalf("wrong replay: %#v", messages)
	}
	var journal approvalPreparationRecord
	if err := r.store.ReadArtifact(id, approvalPreparationArtifact, &journal); err != nil {
		t.Fatal(err)
	}
	if journal.Phase != "prepared" || journal.Target != *target || journal.Snapshot.Revision != target.ExpectedRevision || journal.OriginalState.Status != session.StatusAwaitingInput {
		t.Fatalf("wrong prepare journal: %#v", journal)
	}
	result, err := r.RunPreparedApproval(context.Background(), prepared)
	if err != nil || result.Status != session.StatusCompleted || calls.Load() != 1 {
		t.Fatalf("matching approval did not execute: %#v %v calls=%d", result, err, calls.Load())
	}
	if _, err := r.RunPreparedApproval(context.Background(), prepared); err == nil {
		t.Fatal("prepared approval ran twice")
	}
}

func TestPreparedApprovalAbortRestoresClaimAndRetryDeduplicatesRevision(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	target := approvalTargetForTest(t, r, id)
	req := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: target}
	prepared, err := r.PrepareApprovalContinue(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.AbortPreparedApproval(prepared, errors.New("handle unavailable")); err != nil {
		t.Fatal(err)
	}
	state, err := r.store.LoadState(id)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != session.StatusAwaitingInput || state.Phase != "plan_approval" {
		t.Fatalf("abort failed to restore original state: %#v", state)
	}
	prepared, err = r.PrepareApprovalContinue(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.AbortPreparedApproval(prepared, nil)
	messages, err := r.store.LoadMessages(id)
	if err != nil {
		t.Fatal(err)
	}
	if countPlanModeApprovalMessages(messages) != 1 {
		t.Fatalf("retry duplicated revision replay: %#v", messages)
	}
	if calls.Load() != 0 {
		t.Fatal("abort/prepare invoked provider")
	}
}

func TestPreparedApprovalRefusesChangedSnapshotBeforeProvider(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	goal := linkApprovalMissionForTest(t, r, id)
	target := approvalTargetForTest(t, r, id)
	prepared, err := r.PrepareApprovalContinue(context.Background(), ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: target})
	if err != nil {
		t.Fatal(err)
	}
	goal.Mission.Requirements = append(goal.Mission.Requirements, session.MissionRequirement{ID: "changed", Text: "Changed after synchronous admission"})
	if err := r.store.SaveGoal(id, goal); err != nil {
		t.Fatal(err)
	}
	result, err := r.RunPreparedApproval(context.Background(), prepared)
	if !errors.Is(err, session.ErrApprovalConflict) || result.Status != session.StatusAwaitingInput || calls.Load() != 0 {
		t.Fatalf("changed scope must require review without provider: %#v %v calls=%d", result, err, calls.Load())
	}
}

func TestPreparedApprovalGuardsScopeAfterContextAssembly(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	goal := linkApprovalMissionForTest(t, r, id)
	target := approvalTargetForTest(t, r, id)
	prepared, err := r.PrepareApprovalContinue(context.Background(), ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: target})
	if err != nil {
		t.Fatal(err)
	}
	changed := false
	r.engine.beforeAppendEvent = func(evt events.Event) {
		if evt.Type == "session.context.loaded" && !changed {
			changed = true
			goal.Mission.Requirements = append(goal.Mission.Requirements, session.MissionRequirement{ID: "late", Text: "Changed during async context assembly"})
			if err := r.store.SaveGoal(id, goal); err != nil {
				t.Fatal(err)
			}
		}
	}
	result, err := r.RunPreparedApproval(context.Background(), prepared)
	if !changed || !errors.Is(err, session.ErrApprovalConflict) || result.Status != session.StatusAwaitingInput || calls.Load() != 0 {
		t.Fatalf("late changed scope must stop before provider: %#v %v calls=%d", result, err, calls.Load())
	}
}

func TestApprovalPreparationJournalFailureDoesNotClaim(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	target := approvalTargetForTest(t, r, id)
	path := filepath.Join(r.store.SessionDir(id), "artifacts", approvalPreparationArtifact)
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	_, err := r.PrepareApprovalContinue(context.Background(), ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: target})
	if err == nil || !strings.Contains(err.Error(), "approval preparation") {
		t.Fatalf("expected synchronous journal failure: %v", err)
	}
	state, err := r.store.LoadState(id)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != session.StatusAwaitingInput || calls.Load() != 0 {
		t.Fatalf("journal failure claimed/invoked run: %#v calls=%d", state, calls.Load())
	}
}

func TestApprovalPreparationRecoversDeadOwnerFromDurableSnapshot(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	target := approvalTargetForTest(t, r, id)
	original, err := r.store.LoadState(id)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := r.store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	generation := time.Now().UTC().Format(time.RFC3339Nano)
	record := approvalPreparationRecord{SchemaVersion: 1, SessionID: id, Target: *target, Snapshot: snapshot, OriginalState: original, ClaimUpdatedAt: generation, Phase: "claim_pending", OwnerPID: 999999999}
	if _, err := r.store.WriteArtifact(id, approvalPreparationArtifact, record); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.ClaimSessionRunWithGeneration(id, generation, session.StatusAwaitingInput); err != nil {
		t.Fatal(err)
	}
	prepared, err := r.PrepareApprovalContinue(context.Background(), ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: target})
	if err != nil {
		t.Fatal(err)
	}
	defer r.AbortPreparedApproval(prepared, nil)
	events, err := r.store.LoadEvents(id)
	if err != nil {
		t.Fatal(err)
	}
	if countRuntimeEventType(events, "planmode.approval_prepare_recovered") != 1 || calls.Load() != 0 {
		t.Fatal("orphan preparation not recovered from journal")
	}
}

func TestApprovalFactsDifferentRevisionSameVersionRemainDistinct(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	linkApprovalMissionForTest(t, r, id)
	original := approvalTargetForTest(t, r, id)
	p, err := r.PrepareApprovalContinue(context.Background(), ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: original})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.AbortPreparedApproval(p, nil); err != nil {
		t.Fatal(err)
	}
	goal, err := r.store.LoadGoal(id)
	if err != nil {
		t.Fatal(err)
	}
	goal.Mission.Requirements = append(goal.Mission.Requirements, session.MissionRequirement{ID: "next-scope", Text: "Newly reviewed requirement"})
	if err := r.store.SaveGoal(id, goal); err != nil {
		t.Fatal(err)
	}
	updated := approvalTargetForTest(t, r, id)
	if updated.PlanVersion != original.PlanVersion || updated.ExpectedRevision == original.ExpectedRevision {
		t.Fatal("fixture must change scope at same plan version")
	}
	p, err = r.PrepareApprovalContinue(context.Background(), ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: updated})
	if err != nil {
		t.Fatal(err)
	}
	defer r.AbortPreparedApproval(p, nil)
	messages, err := r.store.LoadMessages(id)
	if err != nil {
		t.Fatal(err)
	}
	if countPlanModeApprovalMessages(messages) != 2 {
		t.Fatalf("same version collapsed separate scope replay: %#v", messages)
	}
	history, err := r.store.LoadGoalHistory(id)
	if err != nil {
		t.Fatal(err)
	}
	if countRuntimeGoalHistoryType(history, "mission.plan.approved") != 2 {
		t.Fatalf("same version collapsed separate mission scope history: %#v", history)
	}
	events, err := r.store.LoadEvents(id)
	if err != nil {
		t.Fatal(err)
	}
	if countRuntimeEventType(events, "planmode.plan_approved") != 2 || countRuntimeEventType(events, "mission.plan.approved") != 2 || calls.Load() != 0 {
		t.Fatal("different approval revisions merged into one event or invoked provider")
	}
}

func TestApprovalPreparationOwnerGuardRequiresValidDurableJournal(t *testing.T) {
	r, id, _ := newApprovalTargetFixture(t)
	alive, err := ApprovalPreparationOwnerAlive(r.store, id)
	if err != nil || alive {
		t.Fatalf("absent journal should have no owner: %v %v", alive, err)
	}
	if allowed, err := CanReconcileApprovalPreparation(r.store, id); err != nil || !allowed {
		t.Fatalf("absent journal should allow ordinary reconciliation: %v %v", allowed, err)
	}
	p, err := r.PrepareApprovalContinue(context.Background(), ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id)})
	if err != nil {
		t.Fatal(err)
	}
	alive, err = ApprovalPreparationOwnerAlive(r.store, id)
	if err != nil || !alive {
		t.Fatalf("prepared live owner must protect claim: %v %v", alive, err)
	}
	if allowed, err := CanReconcileApprovalPreparation(r.store, id); err != nil || allowed {
		t.Fatalf("live preparation must prevent reconciliation: %v %v", allowed, err)
	}
	if err := r.AbortPreparedApproval(p, nil); err != nil {
		t.Fatal(err)
	}
	alive, err = ApprovalPreparationOwnerAlive(r.store, id)
	if err != nil || alive {
		t.Fatalf("aborted prepare must release owner: %v %v", alive, err)
	}
	if _, err := r.store.WriteArtifact(id, approvalPreparationArtifact, map[string]any{"phase": "prepared", "owner_pid": os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	alive, err = ApprovalPreparationOwnerAlive(r.store, id)
	if err == nil || alive {
		t.Fatal("corrupt journal cannot authorize owner/reclaim")
	}
	if allowed, err := CanReconcileApprovalPreparation(r.store, id); err == nil || allowed || errors.Is(err, session.ErrApprovalConflict) {
		t.Fatalf("corruption cannot be treated as an explicit-stop ownership conflict: %v %v", allowed, err)
	}
}

func TestPreparedApprovalAbortDoesNotRestoreAnotherRunGeneration(t *testing.T) {
	r, id, _ := newApprovalTargetFixture(t)
	p, err := r.PrepareApprovalContinue(context.Background(), ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id)})
	if err != nil {
		t.Fatal(err)
	}
	advanced := p.state
	advanced.Status = session.StatusPaused
	if err := r.store.SaveState(id, advanced); err != nil {
		t.Fatal(err)
	}
	second, err := session.NewStore(r.store.Root()).ClaimSessionRun(id, session.StatusPaused)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.AbortPreparedApproval(p, nil); !errors.Is(err, session.ErrApprovalConflict) {
		t.Fatalf("expected generation conflict: %v", err)
	}
	actual, err := r.store.LoadState(id)
	if err != nil {
		t.Fatal(err)
	}
	if actual.UpdatedAt != second.UpdatedAt || actual.Status != session.StatusRunning {
		t.Fatalf("abort overwrote another generation: %#v", actual)
	}
}

func TestPreparedApprovalRunDoesNotStartAnotherRunGeneration(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	p, err := r.PrepareApprovalContinue(context.Background(), ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id)})
	if err != nil {
		t.Fatal(err)
	}
	advanced := p.state
	advanced.Status = session.StatusPaused
	if err := r.store.SaveState(id, advanced); err != nil {
		t.Fatal(err)
	}
	second, err := session.NewStore(r.store.Root()).ClaimSessionRun(id, session.StatusPaused)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.RunPreparedApproval(context.Background(), p)
	if !errors.Is(err, session.ErrApprovalConflict) || calls.Load() != 0 {
		t.Fatalf("expected generation conflict before provider: %v calls=%d", err, calls.Load())
	}
	actual, err := r.store.LoadState(id)
	if err != nil {
		t.Fatal(err)
	}
	if actual.UpdatedAt != second.UpdatedAt || actual.Status != session.StatusRunning {
		t.Fatalf("old prepared run overwrote another generation: %#v", actual)
	}
}

func TestApprovalRecoveryRejectsOldJournalOverNewRunGeneration(t *testing.T) {
	for _, phase := range []string{"claim_pending", "prepared", "executing", "missing_generation"} {
		t.Run(phase, func(t *testing.T) {
			r, id, calls := newApprovalTargetFixture(t)
			target := approvalTargetForTest(t, r, id)
			old, err := r.PrepareApprovalContinue(context.Background(), ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: target})
			if err != nil {
				t.Fatal(err)
			}
			old.releaseRunSlot() // simulate the process owner being lost without Abort
			record := *old.preparation
			record.OwnerPID = 999999999
			record.OwnerIdentity = ""
			record.Phase = phase
			if phase == "missing_generation" {
				record.Phase = "claim_pending"
				record.ClaimUpdatedAt = ""
			}
			if _, err := r.store.WriteArtifact(id, approvalPreparationArtifact, record); err != nil {
				t.Fatal(err)
			}
			paused := old.state
			paused.Status = session.StatusPaused
			if err := r.store.SaveState(id, paused); err != nil {
				t.Fatal(err)
			}
			newer, err := session.NewStore(r.store.Root()).ClaimSessionRun(id, session.StatusPaused)
			if err != nil {
				t.Fatal(err)
			}
			if newer.UpdatedAt == old.state.UpdatedAt {
				t.Fatal("fixture requires a different run generation")
			}
			if allowed, err := CanReconcileApprovalPreparation(r.store, id); allowed || !errors.Is(err, session.ErrApprovalConflict) {
				t.Fatalf("automatic reconciliation accepted stale generation: %v %v", allowed, err)
			}
			retried, err := r.PrepareApprovalContinue(context.Background(), ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: target})
			if retried != nil {
				defer r.AbortPreparedApproval(retried, nil)
			}
			if !errors.Is(err, session.ErrApprovalConflict) {
				t.Fatalf("old %s journal reclaimed a different live run generation: %v", phase, err)
			}
			actual, err := r.store.LoadState(id)
			if err != nil {
				t.Fatal(err)
			}
			if actual.UpdatedAt != newer.UpdatedAt || actual.Status != session.StatusRunning || calls.Load() != 0 {
				t.Fatalf("old recovery overwrote new run: %#v calls=%d", actual, calls.Load())
			}
		})
	}
}

func TestApprovalRecoveryAcceptsProvenUnadvancedDeadOwnerGeneration(t *testing.T) {
	for _, phase := range []string{"claim_pending", "prepared", "executing"} {
		t.Run(phase, func(t *testing.T) {
			r, id, calls := newApprovalTargetFixture(t)
			target := approvalTargetForTest(t, r, id)
			old, err := r.PrepareApprovalContinue(context.Background(), ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: target})
			if err != nil {
				t.Fatal(err)
			}
			old.releaseRunSlot()
			record := *old.preparation
			record.OwnerPID = 999999999
			record.OwnerIdentity = ""
			record.Phase = phase
			if _, err := r.store.WriteArtifact(id, approvalPreparationArtifact, record); err != nil {
				t.Fatal(err)
			}
			if allowed, err := CanReconcileApprovalPreparation(r.store, id); err != nil || !allowed {
				t.Fatalf("proven dead-owner preparation should allow reconciliation: %v %v", allowed, err)
			}
			recovered, err := r.PrepareApprovalContinue(context.Background(), ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: target})
			if err != nil {
				t.Fatalf("matching generation should be recoverable: %v", err)
			}
			defer r.AbortPreparedApproval(recovered, nil)
			if calls.Load() != 0 {
				t.Fatal("recovery started provider")
			}
		})
	}
}

func TestApprovalRecoveryRejectsAmbiguousOrAdvancedDeadOwnerState(t *testing.T) {
	for _, ambiguous := range []string{"missing_generation", "advanced_execution"} {
		t.Run(ambiguous, func(t *testing.T) {
			r, id, calls := newApprovalTargetFixture(t)
			target := approvalTargetForTest(t, r, id)
			old, err := r.PrepareApprovalContinue(context.Background(), ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: target})
			if err != nil {
				t.Fatal(err)
			}
			old.releaseRunSlot()
			record := *old.preparation
			record.OwnerPID = 999999999
			record.OwnerIdentity = ""
			record.Phase = "executing"
			if ambiguous == "missing_generation" {
				record.ClaimUpdatedAt = ""
			} else {
				advanced := old.state
				advanced.Turn++
				advanced.Phase = "provider"
				if err := r.store.SaveState(id, advanced); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := r.store.WriteArtifact(id, approvalPreparationArtifact, record); err != nil {
				t.Fatal(err)
			}
			before, err := r.store.LoadState(id)
			if err != nil {
				t.Fatal(err)
			}
			_, err = r.PrepareApprovalContinue(context.Background(), ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: target})
			if !errors.Is(err, session.ErrApprovalConflict) {
				t.Fatalf("ambiguous recovery was admitted: %v", err)
			}
			after, err := r.store.LoadState(id)
			if err != nil || !reflect.DeepEqual(after, before) || calls.Load() != 0 {
				t.Fatalf("recovery changed execution state: %#v %v calls=%d", after, err, calls.Load())
			}
		})
	}
}

func TestApprovalClaimPublicationErrorRecoversOnlyItsGeneration(t *testing.T) {
	for _, peerClaim := range []bool{false, true} {
		t.Run(fmt.Sprint(peerClaim), func(t *testing.T) {
			r, id, calls := newApprovalTargetFixture(t)
			target := approvalTargetForTest(t, r, id)
			original, err := r.store.LoadState(id)
			if err != nil {
				t.Fatal(err)
			}
			publicationError := errors.New("injected error after claim publication")
			var newer session.State
			r.approvalRunClaim = func(store *session.Store, sessionID, generation string, allowed ...string) (session.State, error) {
				claimed, err := store.ClaimSessionRunWithGeneration(sessionID, generation, allowed...)
				if err != nil {
					t.Fatal(err)
				}
				if peerClaim {
					paused := claimed
					paused.Status = session.StatusPaused
					peer := session.NewStore(store.Root())
					if err := peer.SaveState(sessionID, paused); err != nil {
						t.Fatal(err)
					}
					newer, err = peer.ClaimSessionRun(sessionID, session.StatusPaused)
					if err != nil {
						t.Fatal(err)
					}
				}
				return session.State{}, publicationError
			}
			prepared, err := r.PrepareApprovalContinue(context.Background(), ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: target})
			if prepared != nil || !errors.Is(err, publicationError) || calls.Load() != 0 {
				t.Fatalf("publication failure was not synchronous: %#v %v calls=%d", prepared, err, calls.Load())
			}
			state, err := r.store.LoadState(id)
			if err != nil {
				t.Fatal(err)
			}
			if peerClaim {
				if !reflect.DeepEqual(state, newer) {
					t.Fatalf("failed claim overwrote a later run: %#v", state)
				}
			} else {
				if state.Status != original.Status || state.Phase != original.Phase {
					t.Fatalf("published claim was stranded: %#v", state)
				}
				var record approvalPreparationRecord
				if err := r.store.ReadArtifact(id, approvalPreparationArtifact, &record); err != nil {
					t.Fatal(err)
				}
				if record.Phase != "prepare_failed" || record.CompletedPhase != "claim_pending" || !strings.Contains(record.LastError, publicationError.Error()) {
					t.Fatalf("claim failure did not retain its durable recovery phase: %#v", record)
				}
				r.approvalRunClaim = nil
				retried, err := r.PrepareApprovalContinue(context.Background(), ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: target})
				if err != nil {
					t.Fatalf("recovered claim cannot be retried: %v", err)
				}
				defer r.AbortPreparedApproval(retried, nil)
			}
		})
	}
}
