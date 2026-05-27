package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"go-cli-agent/internal/fileutil"
)

const (
	PlanModeStatusOff               = "off"
	PlanModeStatusPlanning          = "planning"
	PlanModeStatusAwaitingUserInput = "awaiting_user_input"
	PlanModeStatusAwaitingApproval  = "awaiting_approval"
	PlanModeStatusApproved          = "approved"
	PlanModeStatusRejected          = "rejected"
	PlanModeStatusCancelled         = "cancelled"
	PlanModeStatusExecuting         = "executing"

	PlanModeSourceCLI    = "cli"
	PlanModeSourceWeb    = "web"
	PlanModeSourceTool   = "tool"
	PlanModeSourceSystem = "system"

	MaxPlanModeObjectiveChars = 4000
	MaxPlanModeMarkdownChars  = 200000
	MaxPlanModeSummaryChars   = 8000
)

var planModeQuestionIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type PlanModeDraft struct {
	Enabled   bool
	Objective string
	Source    string
}

type PlanModeState struct {
	SchemaVersion   int                   `json:"schema_version"`
	SessionID       string                `json:"session_id"`
	PlanModeID      string                `json:"plan_mode_id"`
	Enabled         bool                  `json:"enabled"`
	Status          string                `json:"status"`
	Objective       string                `json:"objective"`
	Source          string                `json:"source"`
	LinkedGoalID    string                `json:"linked_goal_id,omitempty"`
	PlanID          string                `json:"plan_id,omitempty"`
	PlanVersion     int                   `json:"plan_version,omitempty"`
	ApprovedVersion int                   `json:"approved_version,omitempty"`
	PlanMarkdown    string                `json:"plan_markdown,omitempty"`
	Summary         string                `json:"summary,omitempty"`
	Assumptions     []string              `json:"assumptions,omitempty"`
	Risks           []string              `json:"risks,omitempty"`
	Verification    []string              `json:"verification,omitempty"`
	PendingRequest  *PlanModeInputRequest `json:"pending_request,omitempty"`
	Approvals       []PlanModeApproval    `json:"approvals,omitempty"`
	CreatedAt       string                `json:"created_at"`
	UpdatedAt       string                `json:"updated_at"`
}

type PlanModeInputOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type PlanModeInputQuestion struct {
	ID       string                `json:"id"`
	Header   string                `json:"header"`
	Question string                `json:"question"`
	Options  []PlanModeInputOption `json:"options"`
	IsOther  bool                  `json:"is_other,omitempty"`
}

type PlanModeInputRequest struct {
	RequestID   string                  `json:"request_id"`
	ToolCallID  string                  `json:"tool_call_id"`
	Questions   []PlanModeInputQuestion `json:"questions"`
	Status      string                  `json:"status"`
	Answers     []PlanModeInputAnswer   `json:"answers,omitempty"`
	CreatedAt   string                  `json:"created_at"`
	AnsweredAt  string                  `json:"answered_at,omitempty"`
	CancelledAt string                  `json:"cancelled_at,omitempty"`
}

type PlanModeInputAnswer struct {
	QuestionID string `json:"question_id"`
	Label      string `json:"label,omitempty"`
	Value      string `json:"value"`
	IsOther    bool   `json:"is_other,omitempty"`
}

type PlanModeApproval struct {
	Version    int    `json:"version"`
	Source     string `json:"source"`
	ApprovedBy string `json:"approved_by,omitempty"`
	ApprovedAt string `json:"approved_at"`
}

type PlanModeHistoryEntry struct {
	SchemaVersion int            `json:"schema_version"`
	ID            string         `json:"id"`
	SessionID     string         `json:"session_id"`
	PlanModeID    string         `json:"plan_mode_id,omitempty"`
	Type          string         `json:"type"`
	Source        string         `json:"source"`
	Status        string         `json:"status,omitempty"`
	PlanVersion   int            `json:"plan_version,omitempty"`
	Data          map[string]any `json:"data,omitempty"`
	CreatedAt     string         `json:"created_at"`
}

type PlanModeSubmitInput struct {
	Title        string   `json:"title"`
	Summary      string   `json:"summary"`
	PlanMarkdown string   `json:"plan_markdown"`
	Assumptions  []string `json:"assumptions,omitempty"`
	Risks        []string `json:"risks,omitempty"`
	Verification []string `json:"verification"`
	Source       string   `json:"source,omitempty"`
}

func NewPlanModeFromDraft(sessionID string, draft PlanModeDraft, linkedGoalID string) (PlanModeState, error) {
	if err := validateStoreID("session", sessionID); err != nil {
		return PlanModeState{}, err
	}
	if !draft.Enabled {
		return PlanModeState{}, errors.New("plan mode draft is disabled")
	}
	objective := strings.TrimSpace(draft.Objective)
	if objective == "" {
		return PlanModeState{}, errors.New("plan mode objective is required")
	}
	if utf8.RuneCountInString(objective) > MaxPlanModeObjectiveChars {
		return PlanModeState{}, fmt.Errorf("plan mode objective exceeds %d characters", MaxPlanModeObjectiveChars)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	state := PlanModeState{
		SchemaVersion: 1,
		SessionID:     sessionID,
		PlanModeID:    NewPlanModeID(),
		Enabled:       true,
		Status:        PlanModeStatusPlanning,
		Objective:     objective,
		Source:        normalizePlanModeSource(draft.Source),
		LinkedGoalID:  strings.TrimSpace(linkedGoalID),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	return state, ValidatePlanMode(state)
}

func ValidatePlanMode(state PlanModeState) error {
	if state.Status == "" || state.Status == PlanModeStatusOff {
		return nil
	}
	if err := validateStoreID("session", state.SessionID); err != nil {
		return err
	}
	if strings.TrimSpace(state.PlanModeID) == "" {
		return errors.New("plan mode id is required")
	}
	if !state.Enabled {
		return errors.New("plan mode state is disabled")
	}
	if strings.TrimSpace(state.Objective) == "" {
		return errors.New("plan mode objective is required")
	}
	if utf8.RuneCountInString(state.Objective) > MaxPlanModeObjectiveChars {
		return fmt.Errorf("plan mode objective exceeds %d characters", MaxPlanModeObjectiveChars)
	}
	switch state.Status {
	case PlanModeStatusPlanning, PlanModeStatusAwaitingUserInput, PlanModeStatusAwaitingApproval, PlanModeStatusApproved, PlanModeStatusRejected, PlanModeStatusCancelled, PlanModeStatusExecuting:
	default:
		return fmt.Errorf("invalid plan mode status: %s", state.Status)
	}
	if utf8.RuneCountInString(state.PlanMarkdown) > MaxPlanModeMarkdownChars {
		return fmt.Errorf("plan markdown exceeds %d characters", MaxPlanModeMarkdownChars)
	}
	if utf8.RuneCountInString(state.Summary) > MaxPlanModeSummaryChars {
		return fmt.Errorf("plan summary exceeds %d characters", MaxPlanModeSummaryChars)
	}
	if state.PendingRequest != nil {
		if err := ValidatePlanModeInputRequest(*state.PendingRequest); err != nil {
			return err
		}
	}
	return nil
}

func ValidatePlanModeInputRequest(request PlanModeInputRequest) error {
	if strings.TrimSpace(request.RequestID) == "" {
		return errors.New("plan input request id is required")
	}
	if strings.TrimSpace(request.ToolCallID) == "" {
		return errors.New("plan input tool call id is required")
	}
	if len(request.Questions) < 1 || len(request.Questions) > 3 {
		return errors.New("request_user_input requires one to three questions")
	}
	seen := map[string]struct{}{}
	for _, question := range request.Questions {
		id := strings.TrimSpace(question.ID)
		if id == "" {
			return errors.New("request_user_input question id is required")
		}
		if !planModeQuestionIDPattern.MatchString(id) {
			return fmt.Errorf("request_user_input question id must be snake_case: %s", id)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("duplicate request_user_input question id: %s", id)
		}
		seen[id] = struct{}{}
		if strings.TrimSpace(question.Header) == "" {
			return fmt.Errorf("request_user_input question %s header is required", id)
		}
		if utf8.RuneCountInString(question.Header) > 12 {
			return fmt.Errorf("request_user_input question %s header exceeds 12 characters", id)
		}
		if strings.TrimSpace(question.Question) == "" {
			return fmt.Errorf("request_user_input question %s prompt is required", id)
		}
		if len(question.Options) < 2 || len(question.Options) > 3 {
			return fmt.Errorf("request_user_input question %s requires two to three options", id)
		}
		for _, option := range question.Options {
			if strings.TrimSpace(option.Label) == "" || strings.TrimSpace(option.Description) == "" {
				return fmt.Errorf("request_user_input question %s option label and description are required", id)
			}
		}
	}
	return nil
}

func ValidatePlanModeAnswers(request PlanModeInputRequest, answers []PlanModeInputAnswer) error {
	if len(answers) != len(request.Questions) {
		return fmt.Errorf("plan input answer count mismatch: got %d want %d", len(answers), len(request.Questions))
	}
	questionsByID := map[string]PlanModeInputQuestion{}
	for _, question := range request.Questions {
		questionsByID[strings.TrimSpace(question.ID)] = question
	}
	seen := map[string]struct{}{}
	for _, answer := range answers {
		id := strings.TrimSpace(answer.QuestionID)
		question, ok := questionsByID[id]
		if !ok {
			return fmt.Errorf("unknown plan input question id: %s", id)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("duplicate plan input answer for question id: %s", id)
		}
		seen[id] = struct{}{}
		label := strings.TrimSpace(answer.Label)
		value := strings.TrimSpace(answer.Value)
		if value == "" && label == "" {
			return fmt.Errorf("plan input answer for %s is empty", id)
		}
		if answer.IsOther {
			if !question.IsOther {
				return fmt.Errorf("plan input answer for %s cannot use other", id)
			}
			if value == "" {
				return fmt.Errorf("plan input other answer for %s is empty", id)
			}
			continue
		}
		option, ok := findPlanModeQuestionOption(question, label, value)
		if !ok {
			return fmt.Errorf("plan input answer for %s must match an offered option", id)
		}
		if label != "" && strings.TrimSpace(option.Label) != label {
			return fmt.Errorf("plan input answer label for %s must match selected option label", id)
		}
		if value != "" && value != strings.TrimSpace(option.Label) && value != strings.TrimSpace(option.Description) {
			return fmt.Errorf("plan input answer value for %s must match selected option", id)
		}
	}
	return nil
}

func findPlanModeQuestionOption(question PlanModeInputQuestion, label, value string) (PlanModeInputOption, bool) {
	label = strings.TrimSpace(label)
	value = strings.TrimSpace(value)
	for _, option := range question.Options {
		optionLabel := strings.TrimSpace(option.Label)
		optionDescription := strings.TrimSpace(option.Description)
		if label != "" {
			if optionLabel == label {
				return option, true
			}
			continue
		}
		if value != "" && (optionLabel == value || optionDescription == value) {
			return option, true
		}
	}
	return PlanModeInputOption{}, false
}

func (s *Store) CreatePlanMode(sessionID string, draft PlanModeDraft) (PlanModeState, error) {
	linkedGoalID := ""
	if goal, err := s.LoadGoal(sessionID); err == nil && goal.GoalID != "" {
		linkedGoalID = goal.GoalID
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return PlanModeState{}, fmt.Errorf("load goal.json: %w", err)
	}
	rollback, err := s.planModeRollbackSnapshot(sessionID)
	if err != nil {
		return PlanModeState{}, err
	}
	state, err := NewPlanModeFromDraft(sessionID, draft, linkedGoalID)
	if err != nil {
		return PlanModeState{}, err
	}
	if err := s.SavePlanMode(sessionID, state); err != nil {
		return PlanModeState{}, err
	}
	if err := s.AppendPlanModeHistory(sessionID, PlanModeHistoryEntry{
		PlanModeID: state.PlanModeID,
		Type:       "planmode.created",
		Source:     state.Source,
		Status:     state.Status,
		Data: map[string]any{
			"objective": state.Objective,
		},
	}); err != nil {
		if rollbackErr := s.rollbackPlanModeAfterHistoryError(sessionID, rollback); rollbackErr != nil {
			return PlanModeState{}, fmt.Errorf("restore plan mode snapshot after %v: %w", err, rollbackErr)
		}
		return PlanModeState{}, err
	}
	return state, nil
}

func (s *Store) LoadPlanMode(sessionID string) (PlanModeState, error) {
	var state PlanModeState
	path, err := s.sessionPath(sessionID, "planmode.json")
	if err != nil {
		return state, err
	}
	err = readJSONFile(path, &state)
	return state, err
}

func (s *Store) SavePlanMode(sessionID string, state PlanModeState) error {
	preparePlanModeForSave(sessionID, &state)
	if err := ValidatePlanMode(state); err != nil {
		return err
	}
	if _, _, err := s.MutatePlanMode(sessionID, func(current *PlanModeState) error {
		*current = state
		return nil
	}); err != nil {
		return err
	}
	return nil
}

func (s *Store) MutatePlanMode(sessionID string, mutate func(*PlanModeState) error) (PlanModeState, bool, error) {
	path, err := s.sessionPath(sessionID, "planmode.json")
	if err != nil {
		return PlanModeState{}, false, err
	}
	lockPath, err := s.sessionPath(sessionID, "planmode.lock")
	if err != nil {
		return PlanModeState{}, false, err
	}
	var state PlanModeState
	s.mu.Lock()
	defer s.mu.Unlock()
	err = s.withFileLock(lockPath, func() error {
		if err := readJSONFile(path, &state); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if mutate != nil {
			if err := mutate(&state); err != nil {
				return err
			}
		}
		if strings.TrimSpace(state.PlanModeID) == "" {
			return nil
		}
		preparePlanModeForSave(sessionID, &state)
		if err := ValidatePlanMode(state); err != nil {
			return err
		}
		return s.writeJSONFile(path, state)
	})
	if err != nil {
		return PlanModeState{}, false, err
	}
	if strings.TrimSpace(state.PlanModeID) == "" {
		return state, false, nil
	}
	return state, true, nil
}

func preparePlanModeForSave(sessionID string, state *PlanModeState) {
	if state == nil {
		return
	}
	if strings.TrimSpace(state.SessionID) == "" {
		state.SessionID = sessionID
	}
	if state.SchemaVersion == 0 {
		state.SchemaVersion = 1
	}
	if state.PlanModeID == "" {
		state.PlanModeID = NewPlanModeID()
	}
	if state.CreatedAt == "" {
		state.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
}

func (s *Store) AppendPlanModeHistory(sessionID string, entry PlanModeHistoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry.SchemaVersion == 0 {
		entry.SchemaVersion = 1
	}
	if entry.ID == "" {
		entry.ID = NewPlanModeHistoryID()
	}
	if entry.SessionID == "" {
		entry.SessionID = sessionID
	}
	if entry.Source == "" {
		entry.Source = PlanModeSourceSystem
	}
	if entry.CreatedAt == "" {
		entry.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if entry.PlanModeID == "" {
		if state, err := s.loadPlanModeNoLock(sessionID); err == nil {
			entry.PlanModeID = state.PlanModeID
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("load planmode.json for plan mode history: %w", err)
		}
	}
	path, err := s.sessionPath(sessionID, "artifacts", "planmode-history.jsonl")
	if err != nil {
		return err
	}
	if err := s.appendJSONL(path, entry); err != nil {
		return fmt.Errorf("append plan mode history %s: %w", path, err)
	}
	return nil
}

func (s *Store) LoadPlanModeHistory(sessionID string) ([]PlanModeHistoryEntry, error) {
	path, err := s.sessionPath(sessionID, "artifacts", "planmode-history.jsonl")
	if err != nil {
		return nil, err
	}
	var out []PlanModeHistoryEntry
	err = readJSONL(path, &out)
	if errors.Is(err, os.ErrNotExist) {
		return []PlanModeHistoryEntry{}, nil
	}
	return out, err
}

func (s *Store) RestorePlanModeHistory(sessionID string, entries []PlanModeHistoryEntry) error {
	path, err := s.sessionPath(sessionID, "artifacts", "planmode-history.jsonl")
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var data bytes.Buffer
	enc := json.NewEncoder(&data)
	for _, entry := range entries {
		if err := enc.Encode(entry); err != nil {
			return err
		}
	}
	if err := s.ensureDir(filepath.Dir(path)); err != nil {
		return err
	}
	return fileutil.AtomicWriteFileNoSymlink(path, data.Bytes(), s.fileMode)
}

func (s *Store) SubmitPlanMode(sessionID string, input PlanModeSubmitInput) (PlanModeState, error) {
	planMarkdown := strings.TrimSpace(input.PlanMarkdown)
	if planMarkdown == "" {
		return PlanModeState{}, errors.New("plan_markdown is required")
	}
	if utf8.RuneCountInString(planMarkdown) > MaxPlanModeMarkdownChars {
		return PlanModeState{}, fmt.Errorf("plan_markdown exceeds %d characters", MaxPlanModeMarkdownChars)
	}
	summary := strings.TrimSpace(input.Summary)
	if summary == "" {
		return PlanModeState{}, errors.New("summary is required")
	}
	if len(input.Verification) == 0 {
		return PlanModeState{}, errors.New("verification is required")
	}
	rollback, err := s.planModeRollbackSnapshot(sessionID)
	if err != nil {
		return PlanModeState{}, err
	}
	state, mutated, err := s.MutatePlanMode(sessionID, func(state *PlanModeState) error {
		if state.PlanModeID == "" {
			return errors.New("session has no current plan mode")
		}
		if state.Status != PlanModeStatusPlanning {
			return fmt.Errorf("plan mode is not planning: %s", state.Status)
		}
		if state.PlanID == "" {
			state.PlanID = NewPlanID()
		}
		state.PlanVersion++
		state.ApprovedVersion = 0
		state.Status = PlanModeStatusAwaitingApproval
		state.PendingRequest = nil
		state.PlanMarkdown = planMarkdown
		state.Summary = summary
		state.Assumptions = cleanStringSlice(input.Assumptions)
		state.Risks = cleanStringSlice(input.Risks)
		state.Verification = cleanStringSlice(input.Verification)
		return nil
	})
	if err != nil {
		return PlanModeState{}, err
	}
	if !mutated {
		return PlanModeState{}, errors.New("session has no current plan mode")
	}
	if s.beforePlanModeMarkdownWrite != nil {
		if err := s.beforePlanModeMarkdownWrite(sessionID, state); err != nil {
			if rollbackErr := s.rollbackPlanModeAfterHistoryError(sessionID, rollback); rollbackErr != nil {
				return PlanModeState{}, fmt.Errorf("restore plan mode snapshot after %v: %w", err, rollbackErr)
			}
			return PlanModeState{}, err
		}
	}
	if err := s.WritePlanModeMarkdown(sessionID, state); err != nil {
		if rollbackErr := s.rollbackPlanModeAfterHistoryError(sessionID, rollback); rollbackErr != nil {
			return PlanModeState{}, fmt.Errorf("restore plan mode snapshot after %v: %w", err, rollbackErr)
		}
		return PlanModeState{}, err
	}
	if err := s.AppendPlanModeHistory(sessionID, PlanModeHistoryEntry{
		PlanModeID:  state.PlanModeID,
		Type:        "planmode.plan_submitted",
		Source:      normalizePlanModeSource(input.Source),
		Status:      state.Status,
		PlanVersion: state.PlanVersion,
		Data: map[string]any{
			"title":   strings.TrimSpace(input.Title),
			"summary": state.Summary,
		},
	}); err != nil {
		if rollbackErr := s.rollbackPlanModeAfterHistoryError(sessionID, rollback); rollbackErr != nil {
			return PlanModeState{}, fmt.Errorf("restore plan mode snapshot after %v: %w", err, rollbackErr)
		}
		return PlanModeState{}, err
	}
	return state, nil
}

func (s *Store) WritePlanModeMarkdown(sessionID string, state PlanModeState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.sessionPath(sessionID, "artifacts", "planmode-plan.md")
	if err != nil {
		return err
	}
	return s.writeBytesFile(path, []byte(strings.TrimSpace(state.PlanMarkdown)+"\n"))
}

func (s *Store) SetPlanModePendingRequest(sessionID string, request PlanModeInputRequest, source string) (PlanModeState, error) {
	if request.RequestID == "" {
		request.RequestID = NewPlanModeQuestionID()
	}
	if request.Status == "" {
		request.Status = "pending"
	}
	if request.CreatedAt == "" {
		request.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	for i := range request.Questions {
		request.Questions[i].IsOther = true
	}
	if err := ValidatePlanModeInputRequest(request); err != nil {
		return PlanModeState{}, err
	}
	rollback, err := s.planModeRollbackSnapshot(sessionID)
	if err != nil {
		return PlanModeState{}, err
	}
	state, mutated, err := s.MutatePlanMode(sessionID, func(state *PlanModeState) error {
		if state.PlanModeID == "" {
			return errors.New("session has no current plan mode")
		}
		if state.Status != PlanModeStatusPlanning {
			return fmt.Errorf("plan mode is not planning: %s", state.Status)
		}
		state.Status = PlanModeStatusAwaitingUserInput
		state.PendingRequest = &request
		return nil
	})
	if err != nil {
		return PlanModeState{}, err
	}
	if !mutated {
		return PlanModeState{}, errors.New("session has no current plan mode")
	}
	if err := s.AppendPlanModeHistory(sessionID, PlanModeHistoryEntry{
		PlanModeID: state.PlanModeID,
		Type:       "planmode.input_requested",
		Source:     normalizePlanModeSource(source),
		Status:     state.Status,
		Data: map[string]any{
			"request_id":   request.RequestID,
			"tool_call_id": request.ToolCallID,
			"questions":    len(request.Questions),
		},
	}); err != nil {
		if rollbackErr := s.rollbackPlanModeAfterHistoryError(sessionID, rollback); rollbackErr != nil {
			return PlanModeState{}, fmt.Errorf("restore plan mode snapshot after %v: %w", err, rollbackErr)
		}
		return PlanModeState{}, err
	}
	return state, nil
}

func (s *Store) AnswerPlanModeInput(sessionID, requestID, source string, answers []PlanModeInputAnswer) (PlanModeState, PlanModeInputRequest, error) {
	var request PlanModeInputRequest
	rollback, err := s.planModeRollbackSnapshot(sessionID)
	if err != nil {
		return PlanModeState{}, PlanModeInputRequest{}, err
	}
	state, mutated, err := s.MutatePlanMode(sessionID, func(state *PlanModeState) error {
		if state.PlanModeID == "" {
			return errors.New("session has no current plan mode")
		}
		if state.PendingRequest == nil {
			return errors.New("plan mode has no pending input request")
		}
		request = *state.PendingRequest
		if strings.TrimSpace(requestID) != "" && request.RequestID != requestID {
			return fmt.Errorf("plan input request mismatch: %s", requestID)
		}
		if err := ValidatePlanModeAnswers(request, answers); err != nil {
			return err
		}
		request.Status = "answered"
		request.Answers = append([]PlanModeInputAnswer(nil), answers...)
		request.AnsweredAt = time.Now().UTC().Format(time.RFC3339Nano)
		state.Status = PlanModeStatusPlanning
		state.PendingRequest = nil
		return nil
	})
	if err != nil {
		return PlanModeState{}, PlanModeInputRequest{}, err
	}
	if !mutated {
		return PlanModeState{}, PlanModeInputRequest{}, errors.New("session has no current plan mode")
	}
	if err := s.AppendPlanModeHistory(sessionID, PlanModeHistoryEntry{
		PlanModeID: state.PlanModeID,
		Type:       "planmode.input_answered",
		Source:     normalizePlanModeSource(source),
		Status:     state.Status,
		Data: map[string]any{
			"request_id": request.RequestID,
			"answers":    answers,
		},
	}); err != nil {
		if rollbackErr := s.rollbackPlanModeAfterHistoryError(sessionID, rollback); rollbackErr != nil {
			return PlanModeState{}, PlanModeInputRequest{}, fmt.Errorf("restore plan mode snapshot after %v: %w", err, rollbackErr)
		}
		return PlanModeState{}, PlanModeInputRequest{}, err
	}
	return state, request, nil
}

func (s *Store) ApprovePlanMode(sessionID string, source string) (PlanModeState, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rollback, err := s.planModeRollbackSnapshot(sessionID)
	if err != nil {
		return PlanModeState{}, err
	}
	state, mutated, err := s.MutatePlanMode(sessionID, func(state *PlanModeState) error {
		if state.PlanModeID == "" {
			return errors.New("session has no current plan mode")
		}
		if state.Status != PlanModeStatusAwaitingApproval && state.Status != PlanModeStatusApproved {
			return fmt.Errorf("plan mode is not awaiting approval: %s", state.Status)
		}
		if state.PlanVersion <= 0 || strings.TrimSpace(state.PlanMarkdown) == "" {
			return errors.New("plan mode has no submitted plan")
		}
		state.Status = PlanModeStatusApproved
		state.ApprovedVersion = state.PlanVersion
		state.Approvals = append(state.Approvals, PlanModeApproval{
			Version:    state.PlanVersion,
			Source:     normalizePlanModeSource(source),
			ApprovedBy: "operator",
			ApprovedAt: now,
		})
		return nil
	})
	if err != nil {
		return PlanModeState{}, err
	}
	if !mutated {
		return PlanModeState{}, errors.New("session has no current plan mode")
	}
	if err := s.AppendPlanModeHistory(sessionID, PlanModeHistoryEntry{
		PlanModeID:  state.PlanModeID,
		Type:        "planmode.plan_approved",
		Source:      normalizePlanModeSource(source),
		Status:      state.Status,
		PlanVersion: state.PlanVersion,
	}); err != nil {
		if rollbackErr := s.rollbackPlanModeAfterHistoryError(sessionID, rollback); rollbackErr != nil {
			return PlanModeState{}, fmt.Errorf("restore plan mode snapshot after %v: %w", err, rollbackErr)
		}
		return PlanModeState{}, err
	}
	return state, nil
}

func (s *Store) MarkPlanModeExecuting(sessionID string, source string) (PlanModeState, error) {
	rollback, err := s.planModeRollbackSnapshot(sessionID)
	if err != nil {
		return PlanModeState{}, err
	}
	state, mutated, err := s.MutatePlanMode(sessionID, func(state *PlanModeState) error {
		if state.PlanModeID == "" {
			return errors.New("session has no current plan mode")
		}
		if state.Status != PlanModeStatusApproved && state.Status != PlanModeStatusExecuting {
			return fmt.Errorf("plan mode is not approved: %s", state.Status)
		}
		state.Status = PlanModeStatusExecuting
		if state.ApprovedVersion == 0 {
			state.ApprovedVersion = state.PlanVersion
		}
		return nil
	})
	if err != nil {
		return PlanModeState{}, err
	}
	if !mutated {
		return PlanModeState{}, errors.New("session has no current plan mode")
	}
	if err := s.AppendPlanModeHistory(sessionID, PlanModeHistoryEntry{
		PlanModeID:  state.PlanModeID,
		Type:        "planmode.execution_started",
		Source:      normalizePlanModeSource(source),
		Status:      state.Status,
		PlanVersion: state.ApprovedVersion,
	}); err != nil {
		if rollbackErr := s.rollbackPlanModeAfterHistoryError(sessionID, rollback); rollbackErr != nil {
			return PlanModeState{}, fmt.Errorf("restore plan mode snapshot after %v: %w", err, rollbackErr)
		}
		return PlanModeState{}, err
	}
	return state, nil
}

func (s *Store) RevisePlanMode(sessionID, source, message string) (PlanModeState, error) {
	rollback, err := s.planModeRollbackSnapshot(sessionID)
	if err != nil {
		return PlanModeState{}, err
	}
	state, mutated, err := s.MutatePlanMode(sessionID, func(state *PlanModeState) error {
		if state.PlanModeID == "" {
			return errors.New("session has no current plan mode")
		}
		if state.Status != PlanModeStatusAwaitingApproval && state.Status != PlanModeStatusRejected && state.Status != PlanModeStatusApproved {
			return fmt.Errorf("plan mode cannot be revised from status: %s", state.Status)
		}
		state.Status = PlanModeStatusPlanning
		state.PendingRequest = nil
		return nil
	})
	if err != nil {
		return PlanModeState{}, err
	}
	if !mutated {
		return PlanModeState{}, errors.New("session has no current plan mode")
	}
	if err := s.AppendPlanModeHistory(sessionID, PlanModeHistoryEntry{
		PlanModeID:  state.PlanModeID,
		Type:        "planmode.plan_revised",
		Source:      normalizePlanModeSource(source),
		Status:      state.Status,
		PlanVersion: state.PlanVersion,
		Data: map[string]any{
			"message": strings.TrimSpace(message),
		},
	}); err != nil {
		if rollbackErr := s.rollbackPlanModeAfterHistoryError(sessionID, rollback); rollbackErr != nil {
			return PlanModeState{}, fmt.Errorf("restore plan mode snapshot after %v: %w", err, rollbackErr)
		}
		return PlanModeState{}, err
	}
	return state, nil
}

func (s *Store) CancelPlanMode(sessionID string, source string) (PlanModeState, error) {
	rollback, err := s.planModeRollbackSnapshot(sessionID)
	if err != nil {
		return PlanModeState{}, err
	}
	state, mutated, err := s.MutatePlanMode(sessionID, func(state *PlanModeState) error {
		if state.PlanModeID == "" {
			return errors.New("session has no current plan mode")
		}
		state.Status = PlanModeStatusCancelled
		if state.PendingRequest != nil {
			now := time.Now().UTC().Format(time.RFC3339Nano)
			state.PendingRequest.Status = "cancelled"
			state.PendingRequest.CancelledAt = now
		}
		return nil
	})
	if err != nil {
		return PlanModeState{}, err
	}
	if !mutated {
		return PlanModeState{}, errors.New("session has no current plan mode")
	}
	if err := s.AppendPlanModeHistory(sessionID, PlanModeHistoryEntry{
		PlanModeID:  state.PlanModeID,
		Type:        "planmode.cancelled",
		Source:      normalizePlanModeSource(source),
		Status:      state.Status,
		PlanVersion: state.PlanVersion,
	}); err != nil {
		if rollbackErr := s.rollbackPlanModeAfterHistoryError(sessionID, rollback); rollbackErr != nil {
			return PlanModeState{}, fmt.Errorf("restore plan mode snapshot after %v: %w", err, rollbackErr)
		}
		return PlanModeState{}, err
	}
	return state, nil
}

type planModeRollback struct {
	Snapshot        PlanModeState
	HasSnapshot     bool
	PlanMarkdown    []byte
	HasPlanMarkdown bool
}

type PlanModeSnapshot struct {
	State           PlanModeState
	HasState        bool
	PlanMarkdown    []byte
	HasPlanMarkdown bool
}

func (s *Store) SnapshotPlanMode(sessionID string) (PlanModeSnapshot, error) {
	rollback, err := s.planModeRollbackSnapshot(sessionID)
	if err != nil {
		return PlanModeSnapshot{}, err
	}
	return PlanModeSnapshot{
		State:           rollback.Snapshot,
		HasState:        rollback.HasSnapshot,
		PlanMarkdown:    append([]byte(nil), rollback.PlanMarkdown...),
		HasPlanMarkdown: rollback.HasPlanMarkdown,
	}, nil
}

func (s *Store) RestorePlanModeSnapshot(sessionID string, snapshot PlanModeSnapshot) error {
	return s.rollbackPlanModeAfterHistoryError(sessionID, planModeRollback{
		Snapshot:        snapshot.State,
		HasSnapshot:     snapshot.HasState,
		PlanMarkdown:    append([]byte(nil), snapshot.PlanMarkdown...),
		HasPlanMarkdown: snapshot.HasPlanMarkdown,
	})
}

func (s *Store) planModeRollbackSnapshot(sessionID string) (planModeRollback, error) {
	rollback := planModeRollback{}
	previous, err := s.LoadPlanMode(sessionID)
	if errors.Is(err, fs.ErrNotExist) {
		return rollback, nil
	}
	if err != nil {
		return rollback, err
	}
	rollback.Snapshot = previous
	rollback.HasSnapshot = strings.TrimSpace(previous.PlanModeID) != ""
	planPath, err := s.sessionPath(sessionID, "artifacts", "planmode-plan.md")
	if err != nil {
		return rollback, err
	}
	data, _, err := fileutil.ReadRegularFileNoSymlink(planPath)
	if err == nil {
		rollback.PlanMarkdown = append([]byte(nil), data...)
		rollback.HasPlanMarkdown = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return rollback, err
	}
	return rollback, nil
}

func (s *Store) rollbackPlanModeAfterHistoryError(sessionID string, rollback planModeRollback) error {
	if !rollback.HasSnapshot {
		return s.removePlanModeSnapshot(sessionID)
	}
	if err := s.writePlanModeSnapshot(sessionID, rollback.Snapshot); err != nil {
		return err
	}
	return s.restorePlanModeMarkdown(sessionID, rollback)
}

func (s *Store) writePlanModeSnapshot(sessionID string, state PlanModeState) error {
	path, err := s.sessionPath(sessionID, "planmode.json")
	if err != nil {
		return err
	}
	preparePlanModeForSave(sessionID, &state)
	if err := ValidatePlanMode(state); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeJSONFile(path, state)
}

func (s *Store) removePlanModeSnapshot(sessionID string) error {
	path, err := s.sessionPath(sessionID, "planmode.json")
	if err != nil {
		return err
	}
	if err := fileutil.RemoveFileNoSymlink(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (s *Store) restorePlanModeMarkdown(sessionID string, rollback planModeRollback) error {
	path, err := s.sessionPath(sessionID, "artifacts", "planmode-plan.md")
	if err != nil {
		return err
	}
	if !rollback.HasPlanMarkdown {
		return fileutil.RemoveFileNoSymlink(path)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDir(filepath.Dir(path)); err != nil {
		return err
	}
	return fileutil.AtomicWriteFileNoSymlink(path, rollback.PlanMarkdown, s.fileMode)
}

func (s *Store) loadPlanModeNoLock(sessionID string) (PlanModeState, error) {
	var state PlanModeState
	path, err := s.sessionPath(sessionID, "planmode.json")
	if err != nil {
		return state, err
	}
	err = readJSONFile(path, &state)
	return state, err
}

func LoadPlanModeOptional(store *Store, sessionID string) (*PlanModeState, error) {
	state, err := store.LoadPlanMode(sessionID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if state.PlanModeID == "" || !state.Enabled || state.Status == "" || state.Status == PlanModeStatusOff {
		return nil, nil
	}
	return &state, nil
}

func IsPlanModePending(status string) bool {
	switch status {
	case PlanModeStatusPlanning, PlanModeStatusAwaitingUserInput, PlanModeStatusAwaitingApproval:
		return true
	default:
		return false
	}
}

func IsPlanModeExecution(status string) bool {
	return status == PlanModeStatusApproved || status == PlanModeStatusExecuting
}

func NewPlanModeID() string {
	return "pm_" + randomHex(6)
}

func NewPlanID() string {
	return "plan_" + randomHex(6)
}

func NewPlanModeHistoryID() string {
	return "pmhist_" + randomHex(6)
}

func NewPlanModeQuestionID() string {
	return "pmq_" + randomHex(6)
}

func normalizePlanModeSource(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case PlanModeSourceCLI, PlanModeSourceWeb, PlanModeSourceTool, PlanModeSourceSystem:
		return strings.ToLower(strings.TrimSpace(value))
	case "":
		return PlanModeSourceCLI
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func cleanStringSlice(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func PlanModeAnswersJSON(answers []PlanModeInputAnswer) string {
	data, err := json.Marshal(map[string]any{"answers": answers})
	if err != nil {
		return `{"answers":[]}`
	}
	return string(data)
}
