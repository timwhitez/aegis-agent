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

func TestSubmitPlanModeReturnsHistoryAppendError(t *testing.T) {
	store, sessionID := newPlanModeTestStore(t)
	if _, err := store.CreatePlanMode(sessionID, PlanModeDraft{
		Enabled:   true,
		Objective: "Plan a history failure",
		Source:    PlanModeSourceCLI,
	}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	blockPlanModeHistoryPath(t, store, sessionID)
	_, err := store.SubmitPlanMode(sessionID, PlanModeSubmitInput{
		Title:        "Plan",
		Summary:      "This transition must report history write failures.",
		PlanMarkdown: "# Summary\n\nSubmit with blocked history.\n",
		Verification: []string{"manual"},
		Source:       PlanModeSourceTool,
	})
	if err == nil || !strings.Contains(err.Error(), "planmode-history.jsonl") {
		t.Fatalf("expected plan mode history append error, got %v", err)
	}
	loaded, loadErr := store.LoadPlanMode(sessionID)
	if loadErr != nil {
		t.Fatalf("load plan mode: %v", loadErr)
	}
	if loaded.Status != PlanModeStatusPlanning || loaded.PlanVersion != 0 || loaded.PlanMarkdown != "" {
		t.Fatalf("failed submit should not advance plan mode snapshot, got %#v", loaded)
	}
	planPath := filepath.Join(store.SessionDir(sessionID), "artifacts", "planmode-plan.md")
	if _, statErr := os.Stat(planPath); !os.IsNotExist(statErr) {
		t.Fatalf("failed submit should not leave plan markdown artifact, got stat err=%v", statErr)
	}
}

func TestRestorePlanModeSnapshotRemovesCreatedPlanMode(t *testing.T) {
	store, sessionID := newPlanModeTestStore(t)
	snapshot, err := store.SnapshotPlanMode(sessionID)
	if err != nil {
		t.Fatalf("snapshot plan mode: %v", err)
	}
	if snapshot.HasState {
		t.Fatalf("expected empty snapshot, got %#v", snapshot)
	}
	if _, err := store.CreatePlanMode(sessionID, PlanModeDraft{
		Enabled:   true,
		Objective: "created plan",
		Source:    PlanModeSourceWeb,
	}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	if err := store.RestorePlanModeSnapshot(sessionID, snapshot); err != nil {
		t.Fatalf("restore plan mode: %v", err)
	}
	if _, err := store.LoadPlanMode(sessionID); !os.IsNotExist(err) {
		t.Fatalf("expected restored snapshot to remove plan mode, got %v", err)
	}
}

func TestApprovePlanModeReturnsHistoryAppendError(t *testing.T) {
	store, sessionID := newPlanModeTestStore(t)
	if _, err := store.CreatePlanMode(sessionID, PlanModeDraft{
		Enabled:   true,
		Objective: "Approve with history failure",
		Source:    PlanModeSourceCLI,
	}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	if _, err := store.SubmitPlanMode(sessionID, PlanModeSubmitInput{
		Title:        "Plan",
		Summary:      "Submit before approval.",
		PlanMarkdown: "# Summary\n\nSubmit before approval.\n",
		Verification: []string{"manual"},
		Source:       PlanModeSourceTool,
	}); err != nil {
		t.Fatalf("submit plan: %v", err)
	}
	blockPlanModeHistoryPath(t, store, sessionID)
	_, err := store.ApprovePlanMode(sessionID, PlanModeSourceCLI)
	if err == nil || !strings.Contains(err.Error(), "planmode-history.jsonl") {
		t.Fatalf("expected plan mode history append error, got %v", err)
	}
	loaded, loadErr := store.LoadPlanMode(sessionID)
	if loadErr != nil {
		t.Fatalf("load plan mode: %v", loadErr)
	}
	if loaded.Status != PlanModeStatusAwaitingApproval || loaded.ApprovedVersion != 0 || len(loaded.Approvals) != 0 {
		t.Fatalf("failed approval should not advance plan mode snapshot, got %#v", loaded)
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

func TestPlanModeConcurrentMutationsReadLatestSnapshot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	storeA := NewStore(root)
	storeB := NewStore(root)
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
	if err := storeA.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := storeA.CreatePlanMode(meta.ID, PlanModeDraft{Enabled: true, Objective: "Serialize plan mode"}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	submitDone := make(chan error, 1)
	go func() {
		_, _, err := storeA.MutatePlanMode(meta.ID, func(state *PlanModeState) error {
			if state.PlanID == "" {
				state.PlanID = NewPlanID()
			}
			state.PlanVersion++
			state.Status = PlanModeStatusAwaitingApproval
			state.PlanMarkdown = "# Plan\n\nSerialized."
			state.Summary = "Serialized."
			state.Verification = []string{"go test ./internal/session"}
			close(started)
			<-release
			return nil
		})
		submitDone <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for submit mutation to hold the plan mode lock")
	}

	approveDone := make(chan error, 1)
	go func() {
		_, err := storeB.ApprovePlanMode(meta.ID, PlanModeSourceWeb)
		approveDone <- err
	}()

	released := false
	releasePlanLock := func() {
		if released {
			return
		}
		released = true
		close(release)
	}
	defer releasePlanLock()

	select {
	case err := <-approveDone:
		releasePlanLock()
		if err != nil {
			t.Fatalf("approval returned early with error: %v", err)
		}
		t.Fatal("approval completed before submit released the plan mode lock")
	case <-time.After(100 * time.Millisecond):
	}

	releasePlanLock()
	select {
	case err := <-submitDone:
		if err != nil {
			t.Fatalf("submit mutation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for submit mutation")
	}
	select {
	case err := <-approveDone:
		if err != nil {
			t.Fatalf("approve plan: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for approval mutation")
	}

	loaded, err := storeA.LoadPlanMode(meta.ID)
	if err != nil {
		t.Fatalf("load plan mode: %v", err)
	}
	if loaded.Status != PlanModeStatusApproved || loaded.PlanVersion != 1 || loaded.ApprovedVersion != 1 {
		t.Fatalf("expected approved latest submitted plan, got %#v", loaded)
	}
	if len(loaded.Approvals) != 1 || loaded.Approvals[0].Version != 1 {
		t.Fatalf("expected approval for submitted version, got %#v", loaded.Approvals)
	}
}

func blockPlanModeHistoryPath(t *testing.T, store *Store, sessionID string) {
	t.Helper()
	historyPath := filepath.Join(store.SessionDir(sessionID), "artifacts", "planmode-history.jsonl")
	if err := os.Remove(historyPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove plan mode history: %v", err)
	}
	if err := os.Mkdir(historyPath, 0o700); err != nil {
		t.Fatalf("block plan mode history path: %v", err)
	}
}
