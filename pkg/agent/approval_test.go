package agent_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"aegis-agent/internal/config"
	"aegis-agent/internal/session"
	sdk "aegis-agent/pkg/agent"
)

func TestSDKApprovalSnapshotProvidesReviewedContinuationTarget(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	store := session.NewStore(cfg.Session.Dir)
	id := session.NewSessionID()
	meta := session.SessionMetadata{SchemaVersion: 1, ID: id, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: t.TempDir(), Mode: session.ModeRun, Provider: cfg.DefaultProvider, Model: cfg.Providers[cfg.DefaultProvider].Model, CompletionPolicy: session.CompletionPolicyInteractive}
	if err := store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_approval"}); err != nil {
		t.Fatal(err)
	}
	goal, err := store.CreateGoal(id, session.GoalDraft{Enabled: true, Mode: session.GoalModeMission, Objective: "SDK reviewed mission", RequirePlanApproval: true, Source: session.GoalSourceCLI})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.EnsurePlanModeForGoal(id, goal, session.PlanModeSourceCLI); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SubmitPlanMode(id, session.PlanModeSubmitInput{Title: "Reviewed plan", Summary: "SDK snapshot", PlanMarkdown: "# Plan\n\nImplement reviewed scope", Verification: []string{"unit checks"}, Source: session.PlanModeSourceTool}); err != nil {
		t.Fatal(err)
	}
	runner := sdk.New(cfg)
	snapshot, err := runner.Approval(id)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision == "" || snapshot.Goal == nil || snapshot.Goal.GoalID != goal.GoalID || snapshot.PlanMode.LinkedGoalID != goal.GoalID || snapshot.Coverage == nil {
		t.Fatalf("facade lost reviewed scope: %#v", snapshot)
	}
	var reviewed sdk.ApprovalTarget = snapshot.Target()
	authoritative, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	if authoritative.Target() != reviewed || authoritative.PlanMode.PlanMarkdown != snapshot.PlanMode.PlanMarkdown || authoritative.Goal.Objective != snapshot.Goal.Objective {
		t.Fatal("facade did not return the coherent authoritative snapshot")
	}
	if _, _, err := store.MutateGoal(id, func(current *session.SessionGoal) error {
		current.Objective = "Independent mission scope change"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.PrepareApprovalContinue(context.Background(), sdk.ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: &reviewed}); !errors.Is(err, session.ErrApprovalConflict) {
		t.Fatalf("SDK accepted stale same-version scope: %v", err)
	}
	fresh, err := runner.Approval(id)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Revision == reviewed.ExpectedRevision || fresh.PlanMode.PlanVersion != reviewed.PlanVersion {
		t.Fatal("linked scope should change revision without plan version")
	}
	if _, _, err := store.MutateGoal(id, func(current *session.SessionGoal) error { current.TokensUsed += 10; return nil }); err != nil {
		t.Fatal(err)
	}
	usageOnly, err := runner.Approval(id)
	if err != nil {
		t.Fatal(err)
	}
	if usageOnly.Revision != fresh.Revision || usageOnly.Goal.TokensUsed != fresh.Goal.TokensUsed+10 {
		t.Fatal("SDK snapshot should include current usage without invalidating reviewed scope")
	}
	target := fresh.Target()
	prepared, err := runner.PrepareApprovalContinue(context.Background(), sdk.ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: &target})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.SessionID() != id {
		t.Fatal("SDK prepared another session")
	}
	if err := runner.AbortPreparedApproval(prepared, nil); err != nil {
		t.Fatal(err)
	}
	state, err := runner.State(id)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != session.StatusAwaitingInput {
		t.Fatalf("SDK abort did not restore claim: %#v", state)
	}
}
