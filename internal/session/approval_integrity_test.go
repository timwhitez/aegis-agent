package session

import (
	"encoding/json"
	"reflect"
	"testing"
)

func captureApprovalIntegritySnapshot(t *testing.T, snapshot ApprovalSnapshot) ApprovalSnapshot {
	t.Helper()
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var captured ApprovalSnapshot
	if err := json.Unmarshal(data, &captured); err != nil {
		t.Fatal(err)
	}
	return captured
}

func TestValidateApprovalSnapshotRevisionSemanticIntegrity(t *testing.T) {
	store, id := newLinkedApprovalTestStore(t)
	snapshot, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	before := captureApprovalIntegritySnapshot(t, snapshot)
	if err := ValidateApprovalSnapshotRevision(snapshot); err != nil {
		t.Fatalf("coherent reviewed snapshot rejected: %v", err)
	}
	if !reflect.DeepEqual(snapshot, before) {
		t.Fatal("validation mutated the reviewed snapshot")
	}
	for _, test := range []struct {
		name   string
		mutate func(*ApprovalSnapshot)
	}{
		{"missing_revision", func(s *ApprovalSnapshot) { s.Revision = "" }},
		{"unknown_revision", func(s *ApprovalSnapshot) { s.Revision = "approval-v2:unknown" }},
		{"plan_markdown", func(s *ApprovalSnapshot) { s.PlanMode.PlanMarkdown = "# Unreviewed plan B" }},
		{"plan_summary", func(s *ApprovalSnapshot) { s.PlanMode.Summary = "Unreviewed summary B" }},
		{"goal_objective", func(s *ApprovalSnapshot) { s.Goal.Objective = "Unreviewed goal B" }},
		{"missing_linked_goal", func(s *ApprovalSnapshot) { s.Goal = nil }},
		{"goal_identity", func(s *ApprovalSnapshot) { s.Goal.GoalID = "replacement_goal" }},
		{"claimed_assertions", func(s *ApprovalSnapshot) { s.Goal.Mission.Features[0].ClaimedAssertions = []string{"validation_0001"} }},
		{"validation_ids", func(s *ApprovalSnapshot) { s.Goal.Mission.Milestones[0].ValidationIDs = []string{"validation_0001"} }},
		{"configured_token_budget", func(s *ApprovalSnapshot) { value := int64(2000); s.Goal.TokenBudget = &value }},
		{"configured_time_budget", func(s *ApprovalSnapshot) { value := int64(120); s.Goal.TimeBudgetSeconds = &value }},
	} {
		t.Run(test.name, func(t *testing.T) {
			tampered := captureApprovalIntegritySnapshot(t, snapshot)
			test.mutate(&tampered)
			if err := ValidateApprovalSnapshotRevision(tampered); err == nil {
				t.Fatal("changed semantic content retained the reviewed revision")
			}
		})
	}
	if !reflect.DeepEqual(snapshot, before) {
		t.Fatal("tamper controls changed the original reviewed snapshot")
	}
}

func TestValidateApprovalSnapshotRevisionProgressMappings(t *testing.T) {
	for _, kind := range []string{"claimed_assertions", "validation_ids"} {
		t.Run(kind, func(t *testing.T) {
			store, id := newLinkedApprovalTestStore(t)
			reviewed, err := store.LoadApprovalSnapshot(id)
			if err != nil {
				t.Fatal(err)
			}
			input := GoalProgressInput{Kind: "progress", Summary: "Coverage mapping updated"}
			if kind == "claimed_assertions" {
				input.FeatureUpdates = []MissionFeatureProgressUpdate{{ID: "feature_0001", ClaimedAssertions: []string{"validation_0001"}}}
			} else {
				input.MilestoneUpdates = []MissionMilestoneProgressUpdate{{ID: "milestone_0001", ValidationIDs: []string{"validation_0001"}}}
			}
			if _, _, err := store.RecordGoalProgress(id, input); err != nil {
				t.Fatal(err)
			}
			current, err := store.LoadApprovalSnapshot(id)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateApprovalSnapshotRevision(current); err != nil {
				t.Fatalf("new coherent progress snapshot rejected: %v", err)
			}
			current.Revision = reviewed.Revision
			if err := ValidateApprovalSnapshotRevision(current); err == nil {
				t.Fatal("progress changed approval mappings without matching the declared revision")
			}
			if err := ValidateApprovalSnapshotRevision(reviewed); err != nil {
				t.Fatalf("current goal change invalidated a coherent historical snapshot: %v", err)
			}
		})
	}
}

func TestValidateApprovalSnapshotRevisionObservationsRemainValid(t *testing.T) {
	store, id := newLinkedApprovalTestStore(t)
	reviewed, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	observed := captureApprovalIntegritySnapshot(t, reviewed)
	observed.PlanMode.Status = PlanModeStatusExecuting
	observed.PlanMode.UpdatedAt = "2030-01-01T00:00:00Z"
	observed.PlanMode.ApprovedVersion = observed.PlanMode.PlanVersion
	observed.PlanMode.ApprovedRevision = reviewed.Revision
	observed.Goal.Status = GoalStatusBudgetLimited
	observed.Goal.TokensUsed += 100
	observed.Goal.TimeUsedSeconds += 20
	observed.Goal.UpdatedAt = "2030-01-01T00:00:00Z"
	observed.Goal.BudgetLimitedAt = observed.Goal.UpdatedAt
	observed.Goal.BudgetWrapUpRequestedAt = observed.Goal.UpdatedAt
	observed.Goal.BudgetWrapUpTurnStartedAt = observed.Goal.UpdatedAt
	observed.Goal.BudgetWrapUpRecordedAt = observed.Goal.UpdatedAt
	observed.Goal.Progress = append(observed.Goal.Progress, GoalProgressRecord{ID: "progress_0001", Kind: "progress", Summary: "Observed work", Evidence: []string{"output"}, CreatedAt: observed.Goal.UpdatedAt})
	observed.Goal.Mission.PlanStatus = MissionPlanStatusApproved
	observed.Goal.Mission.ApprovedAt = observed.Goal.UpdatedAt
	observed.Goal.Mission.ApprovedRevision = reviewed.Revision
	observed.Goal.Mission.Features[0].Status = "completed"
	observed.Goal.Mission.Features[0].Evidence = []string{"feature evidence"}
	observed.Goal.Mission.Milestones[0].Status = "completed"
	observed.Goal.Mission.Milestones[0].Evidence = []string{"milestone evidence"}
	observed.Goal.Mission.ValidationContract[0].Status = "verified"
	observed.Goal.Mission.ValidationContract[0].LastRunAt = observed.Goal.UpdatedAt
	observed.Goal.Mission.ValidationContract[0].Evidence = []string{"validation evidence"}
	if err := ValidateApprovalSnapshotRevision(observed); err != nil {
		t.Fatalf("observational updates invalidated reviewed semantic content: %v", err)
	}
	if _, _, err := store.MutateGoal(id, func(goal *SessionGoal) error { goal.Objective = "A later goal objective"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := ValidateApprovalSnapshotRevision(reviewed); err != nil {
		t.Fatalf("historical replay validation read current goal: %v", err)
	}
}
