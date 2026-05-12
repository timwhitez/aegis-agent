package session

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	GoalStatusActive        = "active"
	GoalStatusPaused        = "paused"
	GoalStatusBudgetLimited = "budget_limited"
	GoalStatusComplete      = "complete"
	GoalStatusCleared       = "cleared"

	GoalModeGoal    = "goal"
	GoalModeMission = "mission"

	GoalSourceCLI    = "cli"
	GoalSourceWeb    = "web"
	GoalSourceTool   = "tool"
	GoalSourceSystem = "system"

	MaxGoalObjectiveChars = 4000
)

type SessionGoal struct {
	SchemaVersion     int              `json:"schema_version"`
	SessionID         string           `json:"session_id"`
	GoalID            string           `json:"goal_id"`
	Mode              string           `json:"mode"`
	Objective         string           `json:"objective"`
	Status            string           `json:"status"`
	TokenBudget       *int64           `json:"token_budget,omitempty"`
	TokensUsed        int64            `json:"tokens_used"`
	TimeBudgetSeconds *int64           `json:"time_budget_seconds,omitempty"`
	TimeUsedSeconds   int64            `json:"time_used_seconds"`
	SuccessCriteria   []GoalCriterion  `json:"success_criteria,omitempty"`
	ValidationPlan    []GoalValidation `json:"validation_plan,omitempty"`
	Control           GoalControl      `json:"control,omitempty"`
	Mission           *MissionPlan     `json:"mission,omitempty"`
	Source            string           `json:"source"`
	CreatedAt         string           `json:"created_at"`
	UpdatedAt         string           `json:"updated_at"`
	CompletedAt       string           `json:"completed_at,omitempty"`
}

type GoalCriterion struct {
	ID        string   `json:"id"`
	Text      string   `json:"text"`
	Status    string   `json:"status"`
	Evidence  []string `json:"evidence,omitempty"`
	Required  bool     `json:"required"`
	UpdatedAt string   `json:"updated_at,omitempty"`
}

type GoalValidation struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Command     string   `json:"command,omitempty"`
	Artifact    string   `json:"artifact,omitempty"`
	Description string   `json:"description,omitempty"`
	Status      string   `json:"status"`
	Evidence    []string `json:"evidence,omitempty"`
	LastRunAt   string   `json:"last_run_at,omitempty"`
}

type GoalControl struct {
	Autonomy                  string   `json:"autonomy,omitempty"`
	RequirePlanApproval       bool     `json:"require_plan_approval,omitempty"`
	StopOnBudget              bool     `json:"stop_on_budget,omitempty"`
	AskBeforeLargeChanges     bool     `json:"ask_before_large_changes,omitempty"`
	AskBeforeDependencyChange bool     `json:"ask_before_dependency_change,omitempty"`
	CheckpointPolicy          string   `json:"checkpoint_policy,omitempty"`
	AllowedRoles              []string `json:"allowed_roles,omitempty"`
}

type MissionPlan struct {
	Requirements        []MissionRequirement `json:"requirements,omitempty"`
	Features            []MissionFeature     `json:"features,omitempty"`
	Milestones          []MissionMilestone   `json:"milestones,omitempty"`
	ValidationContract  []GoalValidation     `json:"validation_contract,omitempty"`
	RolePlan            []MissionRole        `json:"role_plan,omitempty"`
	SharedArtifacts     []string             `json:"shared_artifacts,omitempty"`
	KnowledgeArtifacts  []string             `json:"knowledge_artifacts,omitempty"`
	PlanStatus          string               `json:"plan_status,omitempty"`
	ApprovedAt          string               `json:"approved_at,omitempty"`
	CreateTasksFromPlan bool                 `json:"create_tasks_from_plan,omitempty"`
}

type MissionRequirement struct {
	ID       string   `json:"id"`
	Text     string   `json:"text"`
	Source   string   `json:"source,omitempty"`
	Evidence []string `json:"evidence,omitempty"`
}

type MissionFeature struct {
	ID                string   `json:"id"`
	Title             string   `json:"title"`
	Description       string   `json:"description,omitempty"`
	Status            string   `json:"status"`
	MilestoneID       string   `json:"milestone_id,omitempty"`
	ClaimedAssertions []string `json:"claimed_assertions,omitempty"`
	TaskIDs           []string `json:"task_ids,omitempty"`
	ChildSessionIDs   []string `json:"child_session_ids,omitempty"`
	QueueJobIDs       []string `json:"queue_job_ids,omitempty"`
	Evidence          []string `json:"evidence,omitempty"`
}

type MissionMilestone struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	Status          string   `json:"status"`
	FeatureIDs      []string `json:"feature_ids,omitempty"`
	ValidationIDs   []string `json:"validation_ids,omitempty"`
	TaskIDs         []string `json:"task_ids,omitempty"`
	ChildSessionIDs []string `json:"child_session_ids,omitempty"`
	QueueJobIDs     []string `json:"queue_job_ids,omitempty"`
	Evidence        []string `json:"evidence,omitempty"`
}

type MissionRole struct {
	Name       string   `json:"name"`
	Role       string   `json:"role"`
	Scope      string   `json:"scope,omitempty"`
	Provider   string   `json:"provider,omitempty"`
	Model      string   `json:"model,omitempty"`
	Tools      []string `json:"tools,omitempty"`
	SessionIDs []string `json:"session_ids,omitempty"`
}

type GoalDraft struct {
	Enabled                   bool
	Mode                      string
	Objective                 string
	SuccessCriteria           []string
	ValidationPlan            []string
	TokenBudget               *int64
	TimeBudgetSeconds         *int64
	Autonomy                  string
	RequirePlanApproval       bool
	CreateTasksFromPlan       bool
	Features                  []string
	Milestones                []string
	Source                    string
	AskBeforeLargeChanges     bool
	AskBeforeDependencyChange bool
	CheckpointPolicy          string
}

type GoalUsageDelta struct {
	TokensUsedDelta      int64
	TimeUsedSecondsDelta int64
	SourceTurn           int
}

type GoalHistoryEntry struct {
	SchemaVersion int            `json:"schema_version"`
	ID            string         `json:"id"`
	SessionID     string         `json:"session_id"`
	GoalID        string         `json:"goal_id,omitempty"`
	Type          string         `json:"type"`
	Source        string         `json:"source"`
	Status        string         `json:"status,omitempty"`
	Data          map[string]any `json:"data,omitempty"`
	CreatedAt     string         `json:"created_at"`
}

func NewSessionGoalFromDraft(sessionID string, draft GoalDraft) (SessionGoal, error) {
	if err := validateStoreID("session", sessionID); err != nil {
		return SessionGoal{}, err
	}
	if !draft.Enabled {
		return SessionGoal{}, errors.New("goal draft is disabled")
	}
	objective := strings.TrimSpace(draft.Objective)
	if objective == "" {
		return SessionGoal{}, errors.New("goal objective is required")
	}
	if utf8.RuneCountInString(objective) > MaxGoalObjectiveChars {
		return SessionGoal{}, fmt.Errorf("goal objective exceeds %d characters", MaxGoalObjectiveChars)
	}
	if draft.TokenBudget != nil && *draft.TokenBudget <= 0 {
		return SessionGoal{}, errors.New("goal token budget must be positive")
	}
	if draft.TimeBudgetSeconds != nil && *draft.TimeBudgetSeconds <= 0 {
		return SessionGoal{}, errors.New("goal time budget must be positive")
	}
	mode := normalizeGoalMode(draft.Mode)
	source := normalizeGoalSource(draft.Source)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	goal := SessionGoal{
		SchemaVersion:     1,
		SessionID:         sessionID,
		GoalID:            NewGoalID(),
		Mode:              mode,
		Objective:         objective,
		Status:            GoalStatusActive,
		TokenBudget:       cloneInt64Ptr(draft.TokenBudget),
		TimeBudgetSeconds: cloneInt64Ptr(draft.TimeBudgetSeconds),
		SuccessCriteria:   criteriaFromStrings(draft.SuccessCriteria, now),
		ValidationPlan:    validationsFromStrings(draft.ValidationPlan),
		Control: GoalControl{
			Autonomy:                  normalizeGoalAutonomy(draft.Autonomy),
			RequirePlanApproval:       draft.RequirePlanApproval,
			AskBeforeLargeChanges:     draft.AskBeforeLargeChanges,
			AskBeforeDependencyChange: draft.AskBeforeDependencyChange,
			CheckpointPolicy:          strings.TrimSpace(draft.CheckpointPolicy),
		},
		Source:    source,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if mode == GoalModeMission {
		planStatus := "draft"
		if draft.RequirePlanApproval {
			planStatus = "needs_approval"
		}
		validationContract := append([]GoalValidation(nil), goal.ValidationPlan...)
		goal.Mission = &MissionPlan{
			Features:            featuresFromStrings(draft.Features),
			Milestones:          milestonesFromStrings(draft.Milestones),
			ValidationContract:  validationContract,
			PlanStatus:          planStatus,
			CreateTasksFromPlan: draft.CreateTasksFromPlan,
		}
	}
	return goal, ValidateGoal(goal)
}

func ValidateGoal(goal SessionGoal) error {
	if err := validateStoreID("session", goal.SessionID); err != nil {
		return err
	}
	if strings.TrimSpace(goal.GoalID) == "" {
		return errors.New("goal id is required")
	}
	if strings.TrimSpace(goal.Objective) == "" {
		return errors.New("goal objective is required")
	}
	if utf8.RuneCountInString(goal.Objective) > MaxGoalObjectiveChars {
		return fmt.Errorf("goal objective exceeds %d characters", MaxGoalObjectiveChars)
	}
	switch goal.Mode {
	case GoalModeGoal, GoalModeMission:
	default:
		return fmt.Errorf("invalid goal mode: %s", goal.Mode)
	}
	switch goal.Status {
	case GoalStatusActive, GoalStatusPaused, GoalStatusBudgetLimited, GoalStatusComplete:
	default:
		return fmt.Errorf("invalid goal status: %s", goal.Status)
	}
	if goal.TokenBudget != nil && *goal.TokenBudget <= 0 {
		return errors.New("goal token budget must be positive")
	}
	if goal.TimeBudgetSeconds != nil && *goal.TimeBudgetSeconds <= 0 {
		return errors.New("goal time budget must be positive")
	}
	return nil
}

func (g SessionGoal) IsActiveCompletionRequired() bool {
	return g.GoalID != "" && g.Status == GoalStatusActive
}

func (s *Store) LoadGoal(sessionID string) (SessionGoal, error) {
	var goal SessionGoal
	path, err := s.sessionPath(sessionID, "goal.json")
	if err != nil {
		return goal, err
	}
	err = readJSONFile(path, &goal)
	return goal, err
}

func (s *Store) SaveGoal(sessionID string, goal SessionGoal) error {
	if strings.TrimSpace(goal.SessionID) == "" {
		goal.SessionID = sessionID
	}
	if goal.SchemaVersion == 0 {
		goal.SchemaVersion = 1
	}
	if goal.GoalID == "" {
		goal.GoalID = NewGoalID()
	}
	if goal.CreatedAt == "" {
		goal.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	goal.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := ValidateGoal(goal); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.sessionPath(sessionID, "goal.json")
	if err != nil {
		return err
	}
	return s.writeJSONFile(path, goal)
}

func (s *Store) CreateGoal(sessionID string, draft GoalDraft) (SessionGoal, error) {
	if existing, err := s.LoadGoal(sessionID); err == nil && existing.GoalID != "" {
		return SessionGoal{}, errors.New("session already has a current goal")
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return SessionGoal{}, err
	}
	goal, err := NewSessionGoalFromDraft(sessionID, draft)
	if err != nil {
		return SessionGoal{}, err
	}
	if err := s.SaveGoal(sessionID, goal); err != nil {
		return SessionGoal{}, err
	}
	createdTasks, changed, err := syncMissionPlanTasks(s, sessionID, &goal)
	if err != nil {
		return SessionGoal{}, err
	}
	if changed {
		if err := s.SaveGoal(sessionID, goal); err != nil {
			return SessionGoal{}, err
		}
	}
	if err := s.AppendGoalHistory(sessionID, GoalHistoryEntry{
		Type:   "goal.created",
		Source: goal.Source,
		Status: goal.Status,
		Data: map[string]any{
			"mode":             goal.Mode,
			"objective":        goal.Objective,
			"goal_id":          goal.GoalID,
			"created_at":       goal.CreatedAt,
			"created_task_ids": taskIDs(createdTasks),
		},
	}); err != nil {
		return SessionGoal{}, err
	}
	return goal, nil
}

func (s *Store) SyncMissionPlanTasks(sessionID string) (SessionGoal, []Task, bool, error) {
	goal, err := s.LoadGoal(sessionID)
	if err != nil {
		return SessionGoal{}, nil, false, err
	}
	createdTasks, changed, err := syncMissionPlanTasks(s, sessionID, &goal)
	if err != nil {
		return SessionGoal{}, nil, false, err
	}
	if changed {
		if err := s.SaveGoal(sessionID, goal); err != nil {
			return SessionGoal{}, nil, false, err
		}
	}
	return goal, createdTasks, changed, nil
}

func syncMissionPlanTasks(store *Store, sessionID string, goal *SessionGoal) ([]Task, bool, error) {
	if goal == nil || goal.Mission == nil || !goal.Mission.CreateTasksFromPlan {
		return nil, false, nil
	}
	var created []Task
	changed := false
	for i := range goal.Mission.Features {
		feature := &goal.Mission.Features[i]
		if strings.TrimSpace(feature.ID) == "" {
			feature.ID = fmt.Sprintf("feature_%04d", i+1)
			changed = true
		}
		if len(feature.TaskIDs) > 0 {
			continue
		}
		title := strings.TrimSpace(feature.Title)
		if title == "" {
			continue
		}
		description := strings.TrimSpace(feature.Description)
		if description != "" {
			description += "\n\n"
		}
		description += fmt.Sprintf("Mission feature `%s` for goal `%s`.", feature.ID, goal.GoalID)
		task, err := CreateTask(store, sessionID, TaskCreateInput{
			Subject:     title,
			Description: description,
			Labels:      []string{"mission", "goal:" + goal.GoalID, "feature:" + feature.ID},
		})
		if err != nil {
			return nil, false, err
		}
		feature.TaskIDs = append(feature.TaskIDs, task.ID)
		created = append(created, task)
		changed = true
	}
	return created, changed, nil
}

func taskIDs(tasks []Task) []string {
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		if strings.TrimSpace(task.ID) != "" {
			ids = append(ids, task.ID)
		}
	}
	return ids
}

func (s *Store) ClearGoal(sessionID string) (bool, error) {
	path, err := s.sessionPath(sessionID, "goal.json")
	if err != nil {
		return false, err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *Store) AppendGoalHistory(sessionID string, entry GoalHistoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry.SchemaVersion == 0 {
		entry.SchemaVersion = 1
	}
	if entry.ID == "" {
		entry.ID = NewGoalHistoryID()
	}
	if entry.SessionID == "" {
		entry.SessionID = sessionID
	}
	if entry.CreatedAt == "" {
		entry.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if entry.GoalID == "" {
		if goal, err := s.loadGoalNoLock(sessionID); err == nil {
			entry.GoalID = goal.GoalID
		}
	}
	path, err := s.sessionPath(sessionID, "artifacts", "goal-history.jsonl")
	if err != nil {
		return err
	}
	return s.appendJSONL(path, entry)
}

func (s *Store) LoadGoalHistory(sessionID string) ([]GoalHistoryEntry, error) {
	path, err := s.sessionPath(sessionID, "artifacts", "goal-history.jsonl")
	if err != nil {
		return nil, err
	}
	var out []GoalHistoryEntry
	err = readJSONL(path, &out)
	if errors.Is(err, os.ErrNotExist) {
		return []GoalHistoryEntry{}, nil
	}
	return out, err
}

func (s *Store) UpdateGoalAccounting(sessionID string, delta GoalUsageDelta) (SessionGoal, bool, error) {
	goal, err := s.LoadGoal(sessionID)
	if err != nil {
		return SessionGoal{}, false, err
	}
	goal.TokensUsed += maxInt64(0, delta.TokensUsedDelta)
	goal.TimeUsedSeconds += maxInt64(0, delta.TimeUsedSecondsDelta)
	budgetLimited := false
	if goal.Status == GoalStatusActive && goalBudgetExceeded(goal) {
		goal.Status = GoalStatusBudgetLimited
		budgetLimited = true
	}
	if err := s.SaveGoal(sessionID, goal); err != nil {
		return SessionGoal{}, false, err
	}
	_ = s.AppendGoalHistory(sessionID, GoalHistoryEntry{
		Type:   "goal.accounting.updated",
		Source: GoalSourceSystem,
		Status: goal.Status,
		Data: map[string]any{
			"source_turn":             delta.SourceTurn,
			"tokens_used_delta":       maxInt64(0, delta.TokensUsedDelta),
			"time_used_seconds_delta": maxInt64(0, delta.TimeUsedSecondsDelta),
			"tokens_used":             goal.TokensUsed,
			"time_used_seconds":       goal.TimeUsedSeconds,
		},
	})
	if budgetLimited {
		_ = s.AppendGoalHistory(sessionID, GoalHistoryEntry{
			Type:   "goal.budget_limited",
			Source: GoalSourceSystem,
			Status: goal.Status,
			Data: map[string]any{
				"tokens_used":       goal.TokensUsed,
				"token_budget":      goal.TokenBudget,
				"time_used_seconds": goal.TimeUsedSeconds,
				"time_budget":       goal.TimeBudgetSeconds,
			},
		})
	}
	return goal, budgetLimited, nil
}

func (s *Store) loadGoalNoLock(sessionID string) (SessionGoal, error) {
	var goal SessionGoal
	path, err := s.sessionPath(sessionID, "goal.json")
	if err != nil {
		return goal, err
	}
	err = readJSONFile(path, &goal)
	return goal, err
}

func NewGoalID() string {
	return "goal_" + randomHex(6)
}

func NewGoalHistoryID() string {
	return "goalhist_" + randomHex(6)
}

func normalizeGoalMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", GoalModeGoal:
		return GoalModeGoal
	case GoalModeMission:
		return GoalModeMission
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func normalizeGoalSource(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case GoalSourceCLI, GoalSourceWeb, GoalSourceTool, GoalSourceSystem:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return GoalSourceSystem
	}
}

func normalizeGoalAutonomy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "assistive":
		return "assistive"
	case "supervised", "autonomous":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "assistive"
	}
}

func criteriaFromStrings(values []string, now string) []GoalCriterion {
	var out []GoalCriterion
	for _, value := range values {
		text := strings.TrimSpace(value)
		if text == "" {
			continue
		}
		out = append(out, GoalCriterion{
			ID:        fmt.Sprintf("criterion_%04d", len(out)+1),
			Text:      text,
			Status:    "pending",
			Required:  true,
			UpdatedAt: now,
		})
	}
	return out
}

func validationsFromStrings(values []string) []GoalValidation {
	var out []GoalValidation
	for _, value := range values {
		text := strings.TrimSpace(value)
		if text == "" {
			continue
		}
		validation := GoalValidation{
			ID:     fmt.Sprintf("validation_%04d", len(out)+1),
			Kind:   "command",
			Status: "pending",
		}
		switch {
		case strings.HasPrefix(strings.ToLower(text), "artifact:"):
			validation.Kind = "artifact"
			validation.Artifact = strings.TrimSpace(text[len("artifact:"):])
		case strings.HasPrefix(strings.ToLower(text), "manual:"):
			validation.Kind = "manual"
			validation.Description = strings.TrimSpace(text[len("manual:"):])
		case strings.HasPrefix(strings.ToLower(text), "browser:"):
			validation.Kind = "browser"
			validation.Description = strings.TrimSpace(text[len("browser:"):])
		case strings.HasPrefix(strings.ToLower(text), "review:"):
			validation.Kind = "review"
			validation.Description = strings.TrimSpace(text[len("review:"):])
		default:
			validation.Command = text
		}
		out = append(out, validation)
	}
	return out
}

func featuresFromStrings(values []string) []MissionFeature {
	var out []MissionFeature
	for _, value := range values {
		title := strings.TrimSpace(value)
		if title == "" {
			continue
		}
		out = append(out, MissionFeature{
			ID:     fmt.Sprintf("feature_%04d", len(out)+1),
			Title:  title,
			Status: "pending",
		})
	}
	return out
}

func milestonesFromStrings(values []string) []MissionMilestone {
	var out []MissionMilestone
	for _, value := range values {
		title := strings.TrimSpace(value)
		if title == "" {
			continue
		}
		out = append(out, MissionMilestone{
			ID:     fmt.Sprintf("milestone_%04d", len(out)+1),
			Title:  title,
			Status: "pending",
		})
	}
	return out
}

func goalBudgetExceeded(goal SessionGoal) bool {
	if goal.TokenBudget != nil && goal.TokensUsed >= *goal.TokenBudget {
		return true
	}
	if goal.TimeBudgetSeconds != nil && goal.TimeUsedSeconds >= *goal.TimeBudgetSeconds {
		return true
	}
	return false
}

func cloneInt64Ptr(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
