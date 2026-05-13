package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newPlanModeTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	store := NewStore(t.TempDir())
	meta := SessionMetadata{
		SchemaVersion: 1,
		ID:            NewSessionID(),
		CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:       t.TempDir(),
		Mode:          ModeExec,
		Provider:      "fake",
		Model:         "fake",
	}
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return store, meta.ID
}

func validPlanModeQuestion(id string) PlanModeInputQuestion {
	return PlanModeInputQuestion{
		ID:       id,
		Header:   "Scope",
		Question: "Which implementation scope should the plan use?",
		Options: []PlanModeInputOption{
			{Label: "Narrow (Recommended)", Description: "Keep the change limited."},
			{Label: "Broad", Description: "Include adjacent cleanup."},
		},
	}
}

func TestPlanModeSubmitApproveAndHistory(t *testing.T) {
	store, sessionID := newPlanModeTestStore(t)
	created, err := store.CreatePlanMode(sessionID, PlanModeDraft{
		Enabled:   true,
		Objective: "Plan a scoped implementation",
		Source:    PlanModeSourceCLI,
	})
	if err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	if created.Status != PlanModeStatusPlanning || created.PlanModeID == "" {
		t.Fatalf("unexpected created plan mode: %#v", created)
	}
	submitted, err := store.SubmitPlanMode(sessionID, PlanModeSubmitInput{
		Title:        "Plan",
		Summary:      "Add Plan Mode facts and gate tools.",
		PlanMarkdown: "# Summary\n\nAdd Plan Mode.\n",
		Verification: []string{"go test ./internal/session"},
		Source:       PlanModeSourceTool,
	})
	if err != nil {
		t.Fatalf("submit plan: %v", err)
	}
	if submitted.Status != PlanModeStatusAwaitingApproval || submitted.PlanVersion != 1 {
		t.Fatalf("unexpected submitted plan: %#v", submitted)
	}
	planPath := filepath.Join(store.SessionDir(sessionID), "artifacts", "planmode-plan.md")
	if got, err := os.ReadFile(planPath); err != nil || !strings.Contains(string(got), "Add Plan Mode") {
		t.Fatalf("expected markdown artifact at %s, got %q err=%v", planPath, string(got), err)
	}
	approved, err := store.ApprovePlanMode(sessionID, PlanModeSourceCLI)
	if err != nil {
		t.Fatalf("approve plan: %v", err)
	}
	if approved.Status != PlanModeStatusApproved || approved.ApprovedVersion != submitted.PlanVersion {
		t.Fatalf("unexpected approved plan: %#v", approved)
	}
	executing, err := store.MarkPlanModeExecuting(sessionID, PlanModeSourceCLI)
	if err != nil {
		t.Fatalf("mark executing: %v", err)
	}
	if executing.Status != PlanModeStatusExecuting {
		t.Fatalf("expected executing, got %#v", executing)
	}
	history, err := store.LoadPlanModeHistory(sessionID)
	if err != nil {
		t.Fatalf("load history: %v", err)
	}
	if len(history) < 4 {
		t.Fatalf("expected created/submitted/approved/executing history, got %#v", history)
	}
}

func TestPlanModeInputValidationAndAnswer(t *testing.T) {
	store, sessionID := newPlanModeTestStore(t)
	if _, err := store.CreatePlanMode(sessionID, PlanModeDraft{Enabled: true, Objective: "Plan decisions"}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	bad := PlanModeInputRequest{
		RequestID:  "pmq_bad",
		ToolCallID: "call_bad",
		Questions:  []PlanModeInputQuestion{validPlanModeQuestion("Bad-ID")},
	}
	if err := ValidatePlanModeInputRequest(bad); err == nil || !strings.Contains(err.Error(), "snake_case") {
		t.Fatalf("expected snake_case validation error, got %v", err)
	}
	request := PlanModeInputRequest{
		RequestID:  "pmq_good",
		ToolCallID: "call_good",
		Questions:  []PlanModeInputQuestion{validPlanModeQuestion("scope_choice")},
	}
	pending, err := store.SetPlanModePendingRequest(sessionID, request, PlanModeSourceTool)
	if err != nil {
		t.Fatalf("set pending request: %v", err)
	}
	if pending.Status != PlanModeStatusAwaitingUserInput || pending.PendingRequest == nil || !pending.PendingRequest.Questions[0].IsOther {
		t.Fatalf("unexpected pending state: %#v", pending)
	}
	answered, answeredRequest, err := store.AnswerPlanModeInput(sessionID, request.RequestID, PlanModeSourceWeb, []PlanModeInputAnswer{{
		QuestionID: "scope_choice",
		Label:      "Narrow (Recommended)",
		Value:      "Narrow (Recommended)",
	}})
	if err != nil {
		t.Fatalf("answer pending request: %v", err)
	}
	if answered.Status != PlanModeStatusPlanning || answered.PendingRequest != nil {
		t.Fatalf("expected planning without pending request, got %#v", answered)
	}
	if answeredRequest.ToolCallID != request.ToolCallID || answeredRequest.AnsweredAt == "" {
		t.Fatalf("expected answered request copy, got %#v", answeredRequest)
	}
}
