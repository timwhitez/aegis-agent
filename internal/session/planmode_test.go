package session

import (
	"encoding/json"
	"errors"
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
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyAutonomous,
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

func TestCreatePlanModeReportsCorruptLinkedGoalSnapshot(t *testing.T) {
	store, sessionID := newPlanModeTestStore(t)
	goalPath := filepath.Join(store.SessionDir(sessionID), "goal.json")
	if err := os.WriteFile(goalPath, []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatalf("write corrupt goal: %v", err)
	}

	_, err := store.CreatePlanMode(sessionID, PlanModeDraft{
		Enabled:   true,
		Objective: "Plan with corrupt goal",
		Source:    PlanModeSourceCLI,
	})
	if err == nil || !strings.Contains(err.Error(), "goal.json") {
		t.Fatalf("expected corrupt goal snapshot error, got %v", err)
	}
	if _, loadErr := store.LoadPlanMode(sessionID); !os.IsNotExist(loadErr) {
		t.Fatalf("failed create should not leave plan mode, got %v", loadErr)
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

func TestSubmitPlanModeRejectsBlankRequiredFields(t *testing.T) {
	tests := []struct {
		name       string
		input      PlanModeSubmitInput
		wantErr    string
		wantVerify []string
	}{
		{
			name: "blank title",
			input: PlanModeSubmitInput{
				Title:        "   ",
				Summary:      "Submit a plan with a usable title.",
				PlanMarkdown: "# Summary\n\nSubmit a plan with a usable title.\n",
				Verification: []string{"go test ./internal/session"},
				Source:       PlanModeSourceTool,
			},
			wantErr: "title is required",
		},
		{
			name: "blank verification item",
			input: PlanModeSubmitInput{
				Title:        "Plan",
				Summary:      "Submit a plan with usable verification.",
				PlanMarkdown: "# Summary\n\nSubmit a plan with usable verification.\n",
				Verification: []string{"   "},
				Source:       PlanModeSourceTool,
			},
			wantErr: "verification is required",
		},
		{
			name: "trimmed verification item",
			input: PlanModeSubmitInput{
				Title:        "  Plan  ",
				Summary:      "Submit a plan with normalized verification.",
				PlanMarkdown: "# Summary\n\nSubmit a plan with normalized verification.\n",
				Verification: []string{"  go test ./internal/session  "},
				Source:       PlanModeSourceTool,
			},
			wantVerify: []string{"go test ./internal/session"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, sessionID := newPlanModeTestStore(t)
			if _, err := store.CreatePlanMode(sessionID, PlanModeDraft{
				Enabled:   true,
				Objective: "Plan required fields",
				Source:    PlanModeSourceCLI,
			}); err != nil {
				t.Fatalf("create plan mode: %v", err)
			}
			submitted, err := store.SubmitPlanMode(sessionID, tt.input)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected %q error, got state=%#v err=%v", tt.wantErr, submitted, err)
				}
				loaded, loadErr := store.LoadPlanMode(sessionID)
				if loadErr != nil {
					t.Fatalf("load plan mode: %v", loadErr)
				}
				if loaded.Status != PlanModeStatusPlanning || loaded.PlanVersion != 0 || loaded.PlanMarkdown != "" || len(loaded.Verification) != 0 {
					t.Fatalf("failed submit should not advance plan mode snapshot, got %#v", loaded)
				}
				planPath := filepath.Join(store.SessionDir(sessionID), "artifacts", "planmode-plan.md")
				if _, statErr := os.Stat(planPath); !os.IsNotExist(statErr) {
					t.Fatalf("failed submit should not leave plan markdown artifact, got stat err=%v", statErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("submit plan: %v", err)
			}
			if submitted.Status != PlanModeStatusAwaitingApproval || submitted.PlanVersion != 1 {
				t.Fatalf("unexpected submitted plan: %#v", submitted)
			}
			if strings.TrimSpace(submitted.Summary) == "" || strings.TrimSpace(submitted.PlanMarkdown) == "" {
				t.Fatalf("expected submitted summary and markdown, got %#v", submitted)
			}
			if len(submitted.Verification) != len(tt.wantVerify) {
				t.Fatalf("expected verification %#v, got %#v", tt.wantVerify, submitted.Verification)
			}
			for i, want := range tt.wantVerify {
				if submitted.Verification[i] != want {
					t.Fatalf("expected verification %#v, got %#v", tt.wantVerify, submitted.Verification)
				}
			}
		})
	}
}

func TestSavePlanModeRejectsSubmittedStatesWithoutPlanFacts(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*PlanModeState)
		wantErr string
	}{
		{
			name: "awaiting approval missing plan version",
			mutate: func(state *PlanModeState) {
				state.Status = PlanModeStatusAwaitingApproval
				state.PlanMarkdown = "# Plan\n\nDo it."
				state.Summary = "Do it."
				state.Verification = []string{"manual"}
			},
			wantErr: "plan mode submitted plan version is required",
		},
		{
			name: "awaiting approval missing markdown",
			mutate: func(state *PlanModeState) {
				state.Status = PlanModeStatusAwaitingApproval
				state.PlanVersion = 1
				state.Summary = "Do it."
				state.Verification = []string{"manual"}
			},
			wantErr: "plan mode submitted plan markdown is required",
		},
		{
			name: "approved missing summary",
			mutate: func(state *PlanModeState) {
				state.Status = PlanModeStatusApproved
				state.PlanVersion = 1
				state.ApprovedVersion = 1
				state.PlanMarkdown = "# Plan\n\nDo it."
				state.Verification = []string{"manual"}
			},
			wantErr: "plan mode submitted plan summary is required",
		},
		{
			name: "executing empty verification",
			mutate: func(state *PlanModeState) {
				state.Status = PlanModeStatusExecuting
				state.PlanVersion = 1
				state.ApprovedVersion = 1
				state.PlanMarkdown = "# Plan\n\nDo it."
				state.Summary = "Do it."
				state.Verification = []string{"   "}
			},
			wantErr: "plan mode submitted plan verification is required",
		},
		{
			name: "approved version without approved plan",
			mutate: func(state *PlanModeState) {
				state.Status = PlanModeStatusApproved
				state.PlanVersion = 2
				state.ApprovedVersion = 3
				state.PlanMarkdown = "# Plan\n\nDo it."
				state.Summary = "Do it."
				state.Verification = []string{"manual"}
			},
			wantErr: "plan mode approved version must reference submitted plan version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, sessionID := newPlanModeTestStore(t)
			state, err := store.CreatePlanMode(sessionID, PlanModeDraft{
				Enabled:   true,
				Objective: "Validate submitted states",
				Source:    PlanModeSourceCLI,
			})
			if err != nil {
				t.Fatalf("create plan mode: %v", err)
			}
			tt.mutate(&state)

			if err := store.SavePlanMode(sessionID, state); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected %q error, got %v", tt.wantErr, err)
			}
			loaded, loadErr := store.LoadPlanMode(sessionID)
			if loadErr != nil {
				t.Fatalf("load plan mode: %v", loadErr)
			}
			if loaded.Status != PlanModeStatusPlanning || loaded.PlanVersion != 0 || loaded.PlanMarkdown != "" || len(loaded.Verification) != 0 {
				t.Fatalf("invalid full snapshot should not be persisted, got %#v", loaded)
			}
		})
	}
}

func TestLoadPlanModeRejectsInvalidSubmittedSnapshot(t *testing.T) {
	store, sessionID := newPlanModeTestStore(t)
	state, err := store.CreatePlanMode(sessionID, PlanModeDraft{
		Enabled:   true,
		Objective: "Validate loaded plan mode",
		Source:    PlanModeSourceCLI,
	})
	if err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	state.Status = PlanModeStatusAwaitingApproval
	state.PlanVersion = 1
	state.PlanMarkdown = ""
	state.Summary = "Missing markdown should be invalid."
	state.Verification = []string{"manual"}
	planModePath := filepath.Join(store.SessionDir(sessionID), "planmode.json")
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal invalid plan mode: %v", err)
	}
	if err := os.WriteFile(planModePath, data, 0o600); err != nil {
		t.Fatalf("write invalid plan mode: %v", err)
	}

	loaded, err := store.LoadPlanMode(sessionID)
	if err == nil || !strings.Contains(err.Error(), "plan mode submitted plan markdown is required") {
		t.Fatalf("expected invalid loaded plan mode error, got state=%#v err=%v", loaded, err)
	}
}

func TestSubmitPlanModeRollsBackWhenMarkdownWriteFails(t *testing.T) {
	store, sessionID := newPlanModeTestStore(t)
	if _, err := store.CreatePlanMode(sessionID, PlanModeDraft{
		Enabled:   true,
		Objective: "Plan a markdown write failure",
		Source:    PlanModeSourceCLI,
	}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	store.beforePlanModeMarkdownWrite = func(gotSessionID string, state PlanModeState) error {
		if gotSessionID != sessionID {
			t.Fatalf("unexpected session id: %s", gotSessionID)
		}
		if state.Status != PlanModeStatusAwaitingApproval || state.PlanVersion != 1 {
			t.Fatalf("expected submitted plan before markdown write, got %#v", state)
		}
		return errors.New("blocked plan markdown write")
	}
	_, err := store.SubmitPlanMode(sessionID, PlanModeSubmitInput{
		Title:        "Plan",
		Summary:      "This transition must roll back if markdown cannot be written.",
		PlanMarkdown: "# Summary\n\nSubmit with blocked markdown.\n",
		Verification: []string{"manual"},
		Source:       PlanModeSourceTool,
	})
	if err == nil || !strings.Contains(err.Error(), "blocked plan markdown write") {
		t.Fatalf("expected plan markdown write error, got %v", err)
	}
	loaded, loadErr := store.LoadPlanMode(sessionID)
	if loadErr != nil {
		t.Fatalf("load plan mode: %v", loadErr)
	}
	if loaded.Status != PlanModeStatusPlanning || loaded.PlanVersion != 0 || loaded.PlanMarkdown != "" {
		t.Fatalf("failed markdown write should not advance plan mode snapshot, got %#v", loaded)
	}
	history, historyErr := store.LoadPlanModeHistory(sessionID)
	if historyErr != nil {
		t.Fatalf("load plan mode history: %v", historyErr)
	}
	if len(history) != 1 || history[0].Type != "planmode.created" {
		t.Fatalf("failed markdown write should not append submit history, got %#v", history)
	}
}

func TestAppendPlanModeHistoryReportsCorruptCurrentPlanModeSnapshot(t *testing.T) {
	store, sessionID := newPlanModeTestStore(t)
	if _, err := store.CreatePlanMode(sessionID, PlanModeDraft{
		Enabled:   true,
		Objective: "Track plan history linkage",
		Source:    PlanModeSourceCLI,
	}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	planModePath := filepath.Join(store.SessionDir(sessionID), "planmode.json")
	if err := os.WriteFile(planModePath, []byte(`{"plan_mode_id":`), 0o600); err != nil {
		t.Fatalf("write corrupt plan mode: %v", err)
	}
	err := store.AppendPlanModeHistory(sessionID, PlanModeHistoryEntry{
		Type:   "planmode.plan_revised",
		Source: PlanModeSourceSystem,
		Status: PlanModeStatusPlanning,
	})
	if err == nil || !strings.Contains(err.Error(), "load planmode.json for plan mode history") {
		t.Fatalf("expected corrupt plan mode snapshot error, got %v", err)
	}
	history, historyErr := store.LoadPlanModeHistory(sessionID)
	if historyErr != nil {
		t.Fatalf("load plan mode history: %v", historyErr)
	}
	if len(history) != 1 || history[0].Type != "planmode.created" {
		t.Fatalf("corrupt plan mode snapshot should not append unlinked history, got %#v", history)
	}
}

func TestPlanModeHistoryRejectsMalformedTimestamps(t *testing.T) {
	store, sessionID := newPlanModeTestStore(t)
	state, err := store.CreatePlanMode(sessionID, PlanModeDraft{
		Enabled:   true,
		Objective: "Reject malformed plan mode history timestamps",
		Source:    PlanModeSourceCLI,
	})
	if err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	valid := PlanModeHistoryEntry{
		SchemaVersion: 1,
		ID:            NewPlanModeHistoryID(),
		SessionID:     sessionID,
		PlanModeID:    state.PlanModeID,
		Type:          "planmode.created",
		Source:        PlanModeSourceCLI,
		Status:        PlanModeStatusPlanning,
		CreatedAt:     now,
	}
	malformed := valid
	malformed.ID = NewPlanModeHistoryID()
	malformed.CreatedAt = "not-a-time"
	historyPath := filepath.Join(store.SessionDir(sessionID), "artifacts", "planmode-history.jsonl")
	writePlanModeHistoryEntriesForTest(t, store, historyPath, []PlanModeHistoryEntry{malformed})
	if _, err := store.LoadPlanModeHistory(sessionID); err == nil || !strings.Contains(err.Error(), "validate planmode-history.jsonl") || !strings.Contains(err.Error(), "created_at must be RFC3339Nano") {
		t.Fatalf("expected malformed plan mode history load error, got %v", err)
	}

	writePlanModeHistoryEntriesForTest(t, store, historyPath, []PlanModeHistoryEntry{valid})
	if err := store.AppendPlanModeHistory(sessionID, PlanModeHistoryEntry{
		Type:      "planmode.plan_revised",
		Source:    PlanModeSourceSystem,
		CreatedAt: "not-a-time",
	}); err == nil || !strings.Contains(err.Error(), "created_at must be RFC3339Nano") {
		t.Fatalf("expected malformed plan mode history append error, got %v", err)
	}
	if err := store.RestorePlanModeHistory(sessionID, []PlanModeHistoryEntry{valid, malformed}); err == nil || !strings.Contains(err.Error(), "created_at must be RFC3339Nano") {
		t.Fatalf("expected malformed plan mode history restore error, got %v", err)
	}
	history, err := store.LoadPlanModeHistory(sessionID)
	if err != nil {
		t.Fatalf("load preserved valid plan mode history: %v", err)
	}
	if len(history) != 1 || history[0].ID != valid.ID {
		t.Fatalf("expected malformed writes to preserve valid history, got %#v", history)
	}
}

func writePlanModeHistoryEntriesForTest(t *testing.T, store *Store, path string, entries []PlanModeHistoryEntry) {
	t.Helper()
	var data strings.Builder
	for _, entry := range entries {
		encoded, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("marshal plan mode history: %v", err)
		}
		data.Write(encoded)
		data.WriteByte('\n')
	}
	if err := store.writeBytesFile(path, []byte(data.String())); err != nil {
		t.Fatalf("write plan mode history: %v", err)
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

func TestValidatePlanModeAnswersRejectsUnknownOption(t *testing.T) {
	request := PlanModeInputRequest{
		RequestID:  "pmq_options",
		ToolCallID: "call_options",
		Questions:  []PlanModeInputQuestion{validPlanModeQuestion("scope_choice")},
	}

	err := ValidatePlanModeAnswers(request, []PlanModeInputAnswer{{
		QuestionID: "scope_choice",
		Label:      "Surprise",
		Value:      "Surprise",
	}})
	if err == nil || !strings.Contains(err.Error(), "must match an offered option") {
		t.Fatalf("expected unknown option rejection, got %v", err)
	}
}

func TestValidatePlanModeAnswersAllowsOfferedOptionDescription(t *testing.T) {
	request := PlanModeInputRequest{
		RequestID:  "pmq_options",
		ToolCallID: "call_options",
		Questions:  []PlanModeInputQuestion{validPlanModeQuestion("scope_choice")},
	}

	err := ValidatePlanModeAnswers(request, []PlanModeInputAnswer{{
		QuestionID: "scope_choice",
		Label:      "Narrow (Recommended)",
		Value:      "Keep the change limited.",
	}})
	if err != nil {
		t.Fatalf("expected offered option description to be accepted, got %v", err)
	}
}

func TestPlanModeConcurrentMutationsReadLatestSnapshot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	storeA := NewStore(root)
	storeB := NewStore(root)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyAutonomous,
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
