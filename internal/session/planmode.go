package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
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
	questionIDs := map[string]struct{}{}
	for _, question := range request.Questions {
		questionIDs[question.ID] = struct{}{}
	}
	seen := map[string]struct{}{}
	for _, answer := range answers {
		id := strings.TrimSpace(answer.QuestionID)
		if _, ok := questionIDs[id]; !ok {
			return fmt.Errorf("unknown plan input question id: %s", id)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("duplicate plan input answer for question id: %s", id)
		}
		seen[id] = struct{}{}
		if strings.TrimSpace(answer.Value) == "" && strings.TrimSpace(answer.Label) == "" {
			return fmt.Errorf("plan input answer for %s is empty", id)
		}
	}
	return nil
}

func (s *Store) CreatePlanMode(sessionID string, draft PlanModeDraft) (PlanModeState, error) {
	linkedGoalID := ""
	if goal, err := s.LoadGoal(sessionID); err == nil && goal.GoalID != "" {
		linkedGoalID = goal.GoalID
	}
	state, err := NewPlanModeFromDraft(sessionID, draft, linkedGoalID)
	if err != nil {
		return PlanModeState{}, err
	}
	if err := s.SavePlanMode(sessionID, state); err != nil {
		return PlanModeState{}, err
	}
	_ = s.AppendPlanModeHistory(sessionID, PlanModeHistoryEntry{
		PlanModeID: state.PlanModeID,
		Type:       "planmode.created",
		Source:     state.Source,
		Status:     state.Status,
		Data: map[string]any{
			"objective": state.Objective,
		},
	})
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
	if err := ValidatePlanMode(state); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.sessionPath(sessionID, "planmode.json")
	if err != nil {
		return err
	}
	return s.writeJSONFile(path, state)
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
		}
	}
	path, err := s.sessionPath(sessionID, "artifacts", "planmode-history.jsonl")
	if err != nil {
		return err
	}
	return s.appendJSONL(path, entry)
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

func (s *Store) SubmitPlanMode(sessionID string, input PlanModeSubmitInput) (PlanModeState, error) {
	state, err := s.LoadPlanMode(sessionID)
	if err != nil {
		return PlanModeState{}, err
	}
	if state.Status != PlanModeStatusPlanning {
		return PlanModeState{}, fmt.Errorf("plan mode is not planning: %s", state.Status)
	}
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
	if err := s.SavePlanMode(sessionID, state); err != nil {
		return PlanModeState{}, err
	}
	if err := s.WritePlanModeMarkdown(sessionID, state); err != nil {
		return PlanModeState{}, err
	}
	_ = s.AppendPlanModeHistory(sessionID, PlanModeHistoryEntry{
		PlanModeID:  state.PlanModeID,
		Type:        "planmode.plan_submitted",
		Source:      normalizePlanModeSource(input.Source),
		Status:      state.Status,
		PlanVersion: state.PlanVersion,
		Data: map[string]any{
			"title":   strings.TrimSpace(input.Title),
			"summary": state.Summary,
		},
	})
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
	state, err := s.LoadPlanMode(sessionID)
	if err != nil {
		return PlanModeState{}, err
	}
	if state.Status != PlanModeStatusPlanning {
		return PlanModeState{}, fmt.Errorf("plan mode is not planning: %s", state.Status)
	}
	state.Status = PlanModeStatusAwaitingUserInput
	state.PendingRequest = &request
	if err := s.SavePlanMode(sessionID, state); err != nil {
		return PlanModeState{}, err
	}
	_ = s.AppendPlanModeHistory(sessionID, PlanModeHistoryEntry{
		PlanModeID: state.PlanModeID,
		Type:       "planmode.input_requested",
		Source:     normalizePlanModeSource(source),
		Status:     state.Status,
		Data: map[string]any{
			"request_id":   request.RequestID,
			"tool_call_id": request.ToolCallID,
			"questions":    len(request.Questions),
		},
	})
	return state, nil
}

func (s *Store) AnswerPlanModeInput(sessionID, requestID, source string, answers []PlanModeInputAnswer) (PlanModeState, PlanModeInputRequest, error) {
	state, err := s.LoadPlanMode(sessionID)
	if err != nil {
		return PlanModeState{}, PlanModeInputRequest{}, err
	}
	if state.PendingRequest == nil {
		return PlanModeState{}, PlanModeInputRequest{}, errors.New("plan mode has no pending input request")
	}
	request := *state.PendingRequest
	if strings.TrimSpace(requestID) != "" && request.RequestID != requestID {
		return PlanModeState{}, PlanModeInputRequest{}, fmt.Errorf("plan input request mismatch: %s", requestID)
	}
	if err := ValidatePlanModeAnswers(request, answers); err != nil {
		return PlanModeState{}, PlanModeInputRequest{}, err
	}
	request.Status = "answered"
	request.Answers = append([]PlanModeInputAnswer(nil), answers...)
	request.AnsweredAt = time.Now().UTC().Format(time.RFC3339Nano)
	state.Status = PlanModeStatusPlanning
	state.PendingRequest = nil
	if err := s.SavePlanMode(sessionID, state); err != nil {
		return PlanModeState{}, PlanModeInputRequest{}, err
	}
	_ = s.AppendPlanModeHistory(sessionID, PlanModeHistoryEntry{
		PlanModeID: state.PlanModeID,
		Type:       "planmode.input_answered",
		Source:     normalizePlanModeSource(source),
		Status:     state.Status,
		Data: map[string]any{
			"request_id": request.RequestID,
			"answers":    answers,
		},
	})
	return state, request, nil
}

func (s *Store) ApprovePlanMode(sessionID string, source string) (PlanModeState, error) {
	state, err := s.LoadPlanMode(sessionID)
	if err != nil {
		return PlanModeState{}, err
	}
	if state.Status != PlanModeStatusAwaitingApproval && state.Status != PlanModeStatusApproved {
		return PlanModeState{}, fmt.Errorf("plan mode is not awaiting approval: %s", state.Status)
	}
	if state.PlanVersion <= 0 || strings.TrimSpace(state.PlanMarkdown) == "" {
		return PlanModeState{}, errors.New("plan mode has no submitted plan")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	state.Status = PlanModeStatusApproved
	state.ApprovedVersion = state.PlanVersion
	state.Approvals = append(state.Approvals, PlanModeApproval{
		Version:    state.PlanVersion,
		Source:     normalizePlanModeSource(source),
		ApprovedBy: "operator",
		ApprovedAt: now,
	})
	if err := s.SavePlanMode(sessionID, state); err != nil {
		return PlanModeState{}, err
	}
	_ = s.AppendPlanModeHistory(sessionID, PlanModeHistoryEntry{
		PlanModeID:  state.PlanModeID,
		Type:        "planmode.plan_approved",
		Source:      normalizePlanModeSource(source),
		Status:      state.Status,
		PlanVersion: state.PlanVersion,
	})
	return state, nil
}

func (s *Store) MarkPlanModeExecuting(sessionID string, source string) (PlanModeState, error) {
	state, err := s.LoadPlanMode(sessionID)
	if err != nil {
		return PlanModeState{}, err
	}
	if state.Status != PlanModeStatusApproved && state.Status != PlanModeStatusExecuting {
		return PlanModeState{}, fmt.Errorf("plan mode is not approved: %s", state.Status)
	}
	state.Status = PlanModeStatusExecuting
	if state.ApprovedVersion == 0 {
		state.ApprovedVersion = state.PlanVersion
	}
	if err := s.SavePlanMode(sessionID, state); err != nil {
		return PlanModeState{}, err
	}
	_ = s.AppendPlanModeHistory(sessionID, PlanModeHistoryEntry{
		PlanModeID:  state.PlanModeID,
		Type:        "planmode.execution_started",
		Source:      normalizePlanModeSource(source),
		Status:      state.Status,
		PlanVersion: state.ApprovedVersion,
	})
	return state, nil
}

func (s *Store) RevisePlanMode(sessionID, source, message string) (PlanModeState, error) {
	state, err := s.LoadPlanMode(sessionID)
	if err != nil {
		return PlanModeState{}, err
	}
	if state.Status != PlanModeStatusAwaitingApproval && state.Status != PlanModeStatusRejected && state.Status != PlanModeStatusApproved {
		return PlanModeState{}, fmt.Errorf("plan mode cannot be revised from status: %s", state.Status)
	}
	state.Status = PlanModeStatusPlanning
	state.PendingRequest = nil
	if err := s.SavePlanMode(sessionID, state); err != nil {
		return PlanModeState{}, err
	}
	_ = s.AppendPlanModeHistory(sessionID, PlanModeHistoryEntry{
		PlanModeID:  state.PlanModeID,
		Type:        "planmode.plan_revised",
		Source:      normalizePlanModeSource(source),
		Status:      state.Status,
		PlanVersion: state.PlanVersion,
		Data: map[string]any{
			"message": strings.TrimSpace(message),
		},
	})
	return state, nil
}

func (s *Store) CancelPlanMode(sessionID string, source string) (PlanModeState, error) {
	state, err := s.LoadPlanMode(sessionID)
	if err != nil {
		return PlanModeState{}, err
	}
	state.Status = PlanModeStatusCancelled
	if state.PendingRequest != nil {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		state.PendingRequest.Status = "cancelled"
		state.PendingRequest.CancelledAt = now
	}
	if err := s.SavePlanMode(sessionID, state); err != nil {
		return PlanModeState{}, err
	}
	_ = s.AppendPlanModeHistory(sessionID, PlanModeHistoryEntry{
		PlanModeID:  state.PlanModeID,
		Type:        "planmode.cancelled",
		Source:      normalizePlanModeSource(source),
		Status:      state.Status,
		PlanVersion: state.PlanVersion,
	})
	return state, nil
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
