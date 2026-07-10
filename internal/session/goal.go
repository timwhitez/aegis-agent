package session

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"go-cli-agent/internal/fileutil"
)

const (
	GoalStatusActive        = "active"
	GoalStatusPaused        = "paused"
	GoalStatusBudgetLimited = "budget_limited"
	GoalStatusComplete      = "complete"
	GoalStatusCleared       = "cleared"

	GoalModeGoal    = "goal"
	GoalModeMission = "mission"

	MissionPlanStatusDraft         = "draft"
	MissionPlanStatusNeedsApproval = "needs_approval"
	MissionPlanStatusApproved      = "approved"

	GoalSourceCLI    = "cli"
	GoalSourceWeb    = "web"
	GoalSourceTool   = "tool"
	GoalSourceSystem = "system"

	MaxGoalObjectiveChars = 4000
)

type SessionGoal struct {
	SchemaVersion             int                  `json:"schema_version"`
	SessionID                 string               `json:"session_id"`
	GoalID                    string               `json:"goal_id"`
	Mode                      string               `json:"mode"`
	Objective                 string               `json:"objective"`
	Status                    string               `json:"status"`
	TokenBudget               *int64               `json:"token_budget,omitempty"`
	TokensUsed                int64                `json:"tokens_used"`
	TimeBudgetSeconds         *int64               `json:"time_budget_seconds,omitempty"`
	TimeUsedSeconds           int64                `json:"time_used_seconds"`
	BudgetLimitedAt           string               `json:"budget_limited_at,omitempty"`
	BudgetWrapUpRequestedAt   string               `json:"budget_wrapup_requested_at,omitempty"`
	BudgetWrapUpTurnStartedAt string               `json:"budget_wrapup_turn_started_at,omitempty"`
	BudgetWrapUpRecordedAt    string               `json:"budget_wrapup_recorded_at,omitempty"`
	SuccessCriteria           []GoalCriterion      `json:"success_criteria,omitempty"`
	ValidationPlan            []GoalValidation     `json:"validation_plan,omitempty"`
	Control                   GoalControl          `json:"control,omitempty"`
	Mission                   *MissionPlan         `json:"mission,omitempty"`
	CompletionAudit           *GoalCompletion      `json:"completion_audit,omitempty"`
	Progress                  []GoalProgressRecord `json:"progress,omitempty"`
	Source                    string               `json:"source"`
	CreatedAt                 string               `json:"created_at"`
	UpdatedAt                 string               `json:"updated_at"`
	CompletedAt               string               `json:"completed_at,omitempty"`
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
	ID                string                  `json:"id"`
	Kind              string                  `json:"kind"`
	Command           string                  `json:"command,omitempty"`
	Artifact          string                  `json:"artifact,omitempty"`
	Description       string                  `json:"description,omitempty"`
	Status            string                  `json:"status"`
	Evidence          []string                `json:"evidence,omitempty"`
	LastRunAt         string                  `json:"last_run_at,omitempty"`
	ChildSessionIDs   []string                `json:"child_session_ids,omitempty"`
	QueueJobIDs       []string                `json:"queue_job_ids,omitempty"`
	EvaluatorEvidence []GoalEvaluatorEvidence `json:"evaluator_evidence,omitempty"`
}

type GoalEvaluatorEvidence struct {
	Role           string `json:"role,omitempty"`
	ChildSessionID string `json:"child_session_id,omitempty"`
	QueueJobID     string `json:"queue_job_id,omitempty"`
	Artifact       string `json:"artifact,omitempty"`
	Summary        string `json:"summary,omitempty"`
	Status         string `json:"status,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
}

type GoalCompletion struct {
	Status      string   `json:"status"`
	Summary     string   `json:"summary,omitempty"`
	Evidence    []string `json:"evidence,omitempty"`
	CompletedBy string   `json:"completed_by,omitempty"`
	CompletedAt string   `json:"completed_at"`
}

type GoalItemStatusUpdate struct {
	ID        string   `json:"id"`
	Status    string   `json:"status,omitempty"`
	Evidence  []string `json:"evidence,omitempty"`
	LastRunAt string   `json:"last_run_at,omitempty"`
}

type GoalProgressCommand struct {
	Command  string `json:"command,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Artifact string `json:"artifact,omitempty"`
	Summary  string `json:"summary,omitempty"`
}

type GoalProgressRecord struct {
	ID              string                `json:"id"`
	Kind            string                `json:"kind"`
	Summary         string                `json:"summary,omitempty"`
	Evidence        []string              `json:"evidence,omitempty"`
	LinkedArtifacts []string              `json:"linked_artifacts,omitempty"`
	Commands        []GoalProgressCommand `json:"commands,omitempty"`
	Blockers        []string              `json:"blockers,omitempty"`
	ChildSessionIDs []string              `json:"child_session_ids,omitempty"`
	QueueJobIDs     []string              `json:"queue_job_ids,omitempty"`
	ValidationIDs   []string              `json:"validation_ids,omitempty"`
	Source          string                `json:"source"`
	CreatedAt       string                `json:"created_at"`
}

type MissionFeatureProgressUpdate struct {
	ID                string   `json:"id"`
	Status            string   `json:"status,omitempty"`
	Evidence          []string `json:"evidence,omitempty"`
	ClaimedAssertions []string `json:"claimed_assertions,omitempty"`
	TaskIDs           []string `json:"task_ids,omitempty"`
	ChildSessionIDs   []string `json:"child_session_ids,omitempty"`
	QueueJobIDs       []string `json:"queue_job_ids,omitempty"`
}

type MissionMilestoneProgressUpdate struct {
	ID              string   `json:"id"`
	Status          string   `json:"status,omitempty"`
	Evidence        []string `json:"evidence,omitempty"`
	FeatureIDs      []string `json:"feature_ids,omitempty"`
	ValidationIDs   []string `json:"validation_ids,omitempty"`
	TaskIDs         []string `json:"task_ids,omitempty"`
	ChildSessionIDs []string `json:"child_session_ids,omitempty"`
	QueueJobIDs     []string `json:"queue_job_ids,omitempty"`
}

type GoalValidationProgressUpdate struct {
	ID                string                  `json:"id"`
	Status            string                  `json:"status,omitempty"`
	Evidence          []string                `json:"evidence,omitempty"`
	LastRunAt         string                  `json:"last_run_at,omitempty"`
	ChildSessionIDs   []string                `json:"child_session_ids,omitempty"`
	QueueJobIDs       []string                `json:"queue_job_ids,omitempty"`
	EvaluatorEvidence []GoalEvaluatorEvidence `json:"evaluator_evidence,omitempty"`
}

type GoalProgressInput struct {
	Source            string
	Kind              string
	Summary           string
	Evidence          []string
	LinkedArtifacts   []string
	Commands          []GoalProgressCommand
	Blockers          []string
	ChildSessionIDs   []string
	QueueJobIDs       []string
	ValidationIDs     []string
	FeatureUpdates    []MissionFeatureProgressUpdate
	MilestoneUpdates  []MissionMilestoneProgressUpdate
	ValidationUpdates []GoalValidationProgressUpdate
}

type GoalCompletionInput struct {
	Source             string
	CompletedBy        string
	Summary            string
	Evidence           []string
	CriteriaStatuses   []GoalItemStatusUpdate
	ValidationStatuses []GoalItemStatusUpdate
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
	StopOnBudget              bool
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

type MissionPlanApprovalInput struct {
	Source           string
	ApprovedSource   string
	ApprovedAt       string
	CoverageOverride bool
	PlanModeID       string
	ApprovedVersion  int
}

type GoalPatchInput struct {
	SuccessCriteria *[]GoalCriterion
	ValidationPlan  *[]GoalValidation
	Control         *GoalControl
	Mission         *MissionPlan
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
			StopOnBudget:              draft.StopOnBudget,
			AskBeforeLargeChanges:     draft.AskBeforeLargeChanges,
			AskBeforeDependencyChange: draft.AskBeforeDependencyChange,
			CheckpointPolicy:          strings.TrimSpace(draft.CheckpointPolicy),
		},
		Source:    source,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if mode == GoalModeMission {
		planStatus := MissionPlanStatusDraft
		if draft.RequirePlanApproval {
			planStatus = MissionPlanStatusNeedsApproval
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
	if err := validateGoalRequiredTimestamp("goal created_at", goal.CreatedAt); err != nil {
		return err
	}
	if err := validateGoalRequiredTimestamp("goal updated_at", goal.UpdatedAt); err != nil {
		return err
	}
	if err := validateGoalOptionalTimestamp("goal completed_at", goal.CompletedAt); err != nil {
		return err
	}
	if err := validateGoalOptionalTimestamp("goal budget_limited_at", goal.BudgetLimitedAt); err != nil {
		return err
	}
	if err := validateGoalOptionalTimestamp("goal budget_wrapup_requested_at", goal.BudgetWrapUpRequestedAt); err != nil {
		return err
	}
	if err := validateGoalOptionalTimestamp("goal budget_wrapup_turn_started_at", goal.BudgetWrapUpTurnStartedAt); err != nil {
		return err
	}
	if err := validateGoalOptionalTimestamp("goal budget_wrapup_recorded_at", goal.BudgetWrapUpRecordedAt); err != nil {
		return err
	}
	if err := validateGoalCriteria(goal.SuccessCriteria); err != nil {
		return err
	}
	if err := validateGoalValidations("validation plan item", goal.ValidationPlan); err != nil {
		return err
	}
	if goal.CompletionAudit != nil {
		if err := validateGoalCompletion(*goal.CompletionAudit); err != nil {
			return err
		}
	}
	if err := validateGoalProgress(goal.Progress); err != nil {
		return err
	}
	if goal.Mission != nil && !IsMissionPlanStatus(goal.Mission.PlanStatus) {
		return fmt.Errorf("invalid mission plan status: %s", goal.Mission.PlanStatus)
	}
	if goal.Mission != nil {
		if err := validateMissionPlan(*goal.Mission); err != nil {
			return err
		}
	}
	return nil
}

func validateGoalRequiredTimestamp(kind, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", kind)
	}
	return validateGoalOptionalTimestamp(kind, value)
}

func validateGoalOptionalTimestamp(kind, value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
		return fmt.Errorf("%s must be RFC3339Nano: %w", kind, err)
	}
	return nil
}

func validateGoalCriteria(items []GoalCriterion) error {
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		id, err := validateGoalItemID("success criteria", item.ID)
		if err != nil {
			return err
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("duplicate success criteria id: %s", id)
		}
		seen[id] = struct{}{}
		if strings.TrimSpace(item.Text) == "" {
			return errors.New("success criteria text is required")
		}
		if err := validateGoalItemStatus("success criteria", item.Status); err != nil {
			return err
		}
		if err := validateGoalStringList("success criteria evidence", item.Evidence); err != nil {
			return err
		}
		if err := validateGoalOptionalTimestamp("success criteria updated_at", item.UpdatedAt); err != nil {
			return err
		}
	}
	return nil
}

func validateGoalValidations(kind string, items []GoalValidation) error {
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		id, err := validateGoalItemID(kind, item.ID)
		if err != nil {
			return err
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("duplicate %s id: %s", kind, id)
		}
		seen[id] = struct{}{}
		if err := validateGoalItemStatus(kind, item.Status); err != nil {
			return err
		}
		if err := validateGoalStringList(kind+" evidence", item.Evidence); err != nil {
			return err
		}
		if err := validateGoalStringList(kind+" child session ids", item.ChildSessionIDs); err != nil {
			return err
		}
		if err := validateGoalStringList(kind+" queue job ids", item.QueueJobIDs); err != nil {
			return err
		}
		if err := validateGoalOptionalTimestamp(kind+" last_run_at", item.LastRunAt); err != nil {
			return err
		}
		for _, evidence := range item.EvaluatorEvidence {
			if err := validateGoalEvaluatorEvidence(kind, evidence); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateMissionPlan(plan MissionPlan) error {
	if err := validateGoalOptionalTimestamp("mission approved_at", plan.ApprovedAt); err != nil {
		return err
	}
	if err := validateMissionRequirements(plan.Requirements); err != nil {
		return err
	}
	if err := validateMissionFeatures(plan.Features); err != nil {
		return err
	}
	if err := validateMissionMilestones(plan.Milestones); err != nil {
		return err
	}
	if err := validateGoalValidations("mission validation contract item", plan.ValidationContract); err != nil {
		return err
	}
	if err := validateMissionRoles(plan.RolePlan); err != nil {
		return err
	}
	if err := validateGoalStringList("mission shared artifacts", plan.SharedArtifacts); err != nil {
		return err
	}
	if err := validateGoalStringList("mission knowledge artifacts", plan.KnowledgeArtifacts); err != nil {
		return err
	}
	return nil
}

func validateMissionRequirements(items []MissionRequirement) error {
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		id, err := validateGoalItemID("mission requirement", item.ID)
		if err != nil {
			return err
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("duplicate mission requirement id: %s", id)
		}
		seen[id] = struct{}{}
		if strings.TrimSpace(item.Text) == "" {
			return errors.New("mission requirement text is required")
		}
		if err := validateGoalStringList("mission requirement evidence", item.Evidence); err != nil {
			return err
		}
	}
	return nil
}

func validateMissionFeatures(items []MissionFeature) error {
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		id, err := validateGoalItemID("mission feature", item.ID)
		if err != nil {
			return err
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("duplicate mission feature id: %s", id)
		}
		seen[id] = struct{}{}
		if strings.TrimSpace(item.Title) == "" {
			return errors.New("mission feature title is required")
		}
		if err := validateGoalItemStatus("mission feature", item.Status); err != nil {
			return err
		}
		if err := validateGoalStringList("mission feature claimed assertions", item.ClaimedAssertions); err != nil {
			return err
		}
		if err := validateGoalStringList("mission feature task ids", item.TaskIDs); err != nil {
			return err
		}
		if err := validateGoalStringList("mission feature child session ids", item.ChildSessionIDs); err != nil {
			return err
		}
		if err := validateGoalStringList("mission feature queue job ids", item.QueueJobIDs); err != nil {
			return err
		}
		if err := validateGoalStringList("mission feature evidence", item.Evidence); err != nil {
			return err
		}
	}
	return nil
}

func validateMissionMilestones(items []MissionMilestone) error {
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		id, err := validateGoalItemID("mission milestone", item.ID)
		if err != nil {
			return err
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("duplicate mission milestone id: %s", id)
		}
		seen[id] = struct{}{}
		if strings.TrimSpace(item.Title) == "" {
			return errors.New("mission milestone title is required")
		}
		if err := validateGoalItemStatus("mission milestone", item.Status); err != nil {
			return err
		}
		if err := validateGoalStringList("mission milestone feature ids", item.FeatureIDs); err != nil {
			return err
		}
		if err := validateGoalStringList("mission milestone validation ids", item.ValidationIDs); err != nil {
			return err
		}
		if err := validateGoalStringList("mission milestone task ids", item.TaskIDs); err != nil {
			return err
		}
		if err := validateGoalStringList("mission milestone child session ids", item.ChildSessionIDs); err != nil {
			return err
		}
		if err := validateGoalStringList("mission milestone queue job ids", item.QueueJobIDs); err != nil {
			return err
		}
		if err := validateGoalStringList("mission milestone evidence", item.Evidence); err != nil {
			return err
		}
	}
	return nil
}

func validateMissionRoles(items []MissionRole) error {
	for _, item := range items {
		if strings.TrimSpace(item.Name) == "" && strings.TrimSpace(item.Role) == "" {
			return errors.New("mission role name or role is required")
		}
		if err := validateGoalStringList("mission role tools", item.Tools); err != nil {
			return err
		}
		if err := validateGoalStringList("mission role session ids", item.SessionIDs); err != nil {
			return err
		}
	}
	return nil
}

func validateGoalEvaluatorEvidence(kind string, item GoalEvaluatorEvidence) error {
	if strings.TrimSpace(item.Status) != "" {
		if err := validateGoalItemStatus(kind+" evaluator evidence", item.Status); err != nil {
			return err
		}
	}
	if err := validateGoalOptionalTimestamp(kind+" evaluator evidence created_at", item.CreatedAt); err != nil {
		return err
	}
	return nil
}

func validateGoalCompletion(completion GoalCompletion) error {
	if err := validateGoalRequiredTimestamp("goal completion completed_at", completion.CompletedAt); err != nil {
		return err
	}
	return nil
}

func validateGoalProgress(items []GoalProgressRecord) error {
	for _, item := range items {
		if err := validateGoalRequiredTimestamp("goal progress created_at", item.CreatedAt); err != nil {
			return err
		}
	}
	return nil
}

func validateGoalItemID(kind, value string) (string, error) {
	id := strings.TrimSpace(value)
	if id == "" {
		return "", fmt.Errorf("%s id is required", kind)
	}
	if id != value {
		return "", fmt.Errorf("%s id has leading or trailing whitespace: %q", kind, value)
	}
	return id, nil
}

func validateGoalItemStatus(kind, value string) error {
	status := strings.TrimSpace(value)
	if status == "" {
		return fmt.Errorf("%s status is required", kind)
	}
	if status != value {
		return fmt.Errorf("%s status has leading or trailing whitespace: %q", kind, value)
	}
	switch strings.ToLower(status) {
	case "pending", "in_progress", "completed", "verified", "failed", "skipped", "blocked":
		return nil
	default:
		return fmt.Errorf("invalid %s status: %s", kind, value)
	}
}

func validateGoalStringList(kind string, values []string) error {
	for i, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s contains blank value at index %d", kind, i)
		}
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
	if err := readJSONFile(path, &goal); err != nil {
		return goal, err
	}
	if err := ValidateGoal(goal); err != nil {
		return SessionGoal{}, fmt.Errorf("validate goal.json: %w", err)
	}
	return goal, nil
}

func (s *Store) SaveGoal(sessionID string, goal SessionGoal) error {
	prepareGoalForSave(sessionID, &goal)
	if err := ValidateGoal(goal); err != nil {
		return err
	}
	if _, _, err := s.MutateGoal(sessionID, func(current *SessionGoal) error {
		*current = goal
		return nil
	}); err != nil {
		return err
	}
	return nil
}

func (s *Store) MutateGoal(sessionID string, mutate func(*SessionGoal) error) (SessionGoal, bool, error) {
	path, err := s.sessionPath(sessionID, "goal.json")
	if err != nil {
		return SessionGoal{}, false, err
	}
	lockPath, err := s.sessionPath(sessionID, "goal.lock")
	if err != nil {
		return SessionGoal{}, false, err
	}
	var goal SessionGoal
	s.mu.Lock()
	defer s.mu.Unlock()
	err = s.withFileLock(lockPath, func() error {
		if err := readJSONFile(path, &goal); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if mutate != nil {
			if err := mutate(&goal); err != nil {
				return err
			}
		}
		if strings.TrimSpace(goal.GoalID) == "" {
			return nil
		}
		prepareGoalForSave(sessionID, &goal)
		if err := ValidateGoal(goal); err != nil {
			return err
		}
		return s.writeJSONFile(path, goal)
	})
	if err != nil {
		return SessionGoal{}, false, err
	}
	if strings.TrimSpace(goal.GoalID) == "" {
		return goal, false, nil
	}
	return goal, true, nil
}

func prepareGoalForSave(sessionID string, goal *SessionGoal) {
	if goal == nil {
		return
	}
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
	if goal.Mission != nil {
		goal.Mission.PlanStatus = NormalizeMissionPlanStatus(goal.Mission.PlanStatus)
	}
	goal.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
}

func (s *Store) CreateGoal(sessionID string, draft GoalDraft) (SessionGoal, error) {
	if existing, err := s.LoadGoal(sessionID); err == nil && existing.GoalID != "" {
		return SessionGoal{}, errors.New("session already has a current goal")
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return SessionGoal{}, err
	}
	rollback, err := s.goalRollbackSnapshot(sessionID)
	if err != nil {
		return SessionGoal{}, err
	}
	goal, err := NewSessionGoalFromDraft(sessionID, draft)
	if err != nil {
		return SessionGoal{}, err
	}
	var tasksRollback []Task
	tasksSnapshotLoaded := false
	if goal.Mission != nil && goal.Mission.CreateTasksFromPlan {
		tasksRollback, err = s.ListTasks(sessionID)
		if err != nil {
			return SessionGoal{}, err
		}
		tasksSnapshotLoaded = true
	}
	if err := s.SaveGoal(sessionID, goal); err != nil {
		return SessionGoal{}, err
	}
	createdTasks, changed, err := syncMissionPlanTasks(s, sessionID, &goal)
	if err != nil {
		if rollbackErr := s.rollbackGoalCreateAfterError(sessionID, rollback, tasksRollback, tasksSnapshotLoaded); rollbackErr != nil {
			return SessionGoal{}, fmt.Errorf("restore goal create after %v: %w", err, rollbackErr)
		}
		return SessionGoal{}, err
	}
	if changed {
		if err := s.SaveGoal(sessionID, goal); err != nil {
			if rollbackErr := s.rollbackGoalCreateAfterError(sessionID, rollback, tasksRollback, tasksSnapshotLoaded); rollbackErr != nil {
				return SessionGoal{}, fmt.Errorf("restore goal create after %v: %w", err, rollbackErr)
			}
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
		if rollbackErr := s.rollbackGoalCreateAfterError(sessionID, rollback, tasksRollback, tasksSnapshotLoaded); rollbackErr != nil {
			return SessionGoal{}, fmt.Errorf("restore goal create after %v: %w", err, rollbackErr)
		}
		return SessionGoal{}, err
	}
	return goal, nil
}

func (s *Store) rollbackGoalCreateAfterError(sessionID string, rollback goalRollback, tasks []Task, hasTasksSnapshot bool) error {
	if hasTasksSnapshot {
		if err := s.SaveTasks(sessionID, tasks); err != nil {
			return err
		}
	}
	return s.rollbackGoalAfterHistoryError(sessionID, rollback)
}

func GoalRequiresPlanApproval(goal SessionGoal) bool {
	if goal.GoalID == "" {
		return false
	}
	if goal.Control.RequirePlanApproval {
		return true
	}
	return goal.Mission != nil && strings.EqualFold(strings.TrimSpace(goal.Mission.PlanStatus), MissionPlanStatusNeedsApproval)
}

func goalNeedsPendingPlanApproval(goal SessionGoal) bool {
	return goal.Mission != nil && strings.EqualFold(strings.TrimSpace(goal.Mission.PlanStatus), MissionPlanStatusNeedsApproval)
}

func (s *Store) EnsurePlanModeForGoal(sessionID string, goal SessionGoal, source string) (PlanModeState, bool, error) {
	if !GoalRequiresPlanApproval(goal) {
		return PlanModeState{}, false, nil
	}
	needsPendingGate := goalNeedsPendingPlanApproval(goal)
	if existing, err := s.LoadPlanMode(sessionID); err == nil && existing.PlanModeID != "" && existing.Enabled {
		linkedToGoal := strings.TrimSpace(existing.LinkedGoalID) == goal.GoalID
		if linkedToGoal && IsPlanModePending(existing.Status) {
			return existing, false, nil
		}
		if linkedToGoal && IsPlanModeExecution(existing.Status) && !needsPendingGate {
			return existing, false, nil
		}
		if strings.TrimSpace(existing.LinkedGoalID) == "" && IsPlanModePending(existing.Status) {
			rollback, err := s.planModeRollbackSnapshot(sessionID)
			if err != nil {
				return PlanModeState{}, false, err
			}
			existing.LinkedGoalID = goal.GoalID
			if err := s.SavePlanMode(sessionID, existing); err != nil {
				return PlanModeState{}, false, err
			}
			if err := s.AppendPlanModeHistory(sessionID, PlanModeHistoryEntry{
				PlanModeID: existing.PlanModeID,
				Type:       "planmode.linked_goal",
				Source:     normalizePlanModeSource(source),
				Status:     existing.Status,
				Data: map[string]any{
					"linked_goal_id": goal.GoalID,
				},
			}); err != nil {
				if rollbackErr := s.rollbackPlanModeAfterHistoryError(sessionID, rollback); rollbackErr != nil {
					return PlanModeState{}, false, fmt.Errorf("restore plan mode snapshot after %v: %w", err, rollbackErr)
				}
				return PlanModeState{}, false, err
			}
			return existing, false, nil
		}
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return PlanModeState{}, false, err
	}
	planMode, err := s.CreatePlanMode(sessionID, PlanModeDraft{
		Enabled:   true,
		Objective: goal.Objective,
		Source:    normalizePlanModeSource(source),
	})
	if err != nil {
		return PlanModeState{}, false, err
	}
	return planMode, true, nil
}

func (s *Store) CompleteGoal(sessionID string, input GoalCompletionInput) (SessionGoal, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rollback, err := s.goalRollbackSnapshot(sessionID)
	if err != nil {
		return SessionGoal{}, err
	}
	goal, mutated, err := s.MutateGoal(sessionID, func(goal *SessionGoal) error {
		if goal.GoalID == "" {
			return errors.New("session has no current goal")
		}
		if err := applyGoalCriterionUpdates(goal.SuccessCriteria, input.CriteriaStatuses, now); err != nil {
			return err
		}
		if err := applyGoalValidationUpdates(goal.ValidationPlan, input.ValidationStatuses, now); err != nil {
			return err
		}
		goal.Status = GoalStatusComplete
		goal.CompletedAt = now
		goal.CompletionAudit = &GoalCompletion{
			Status:      GoalStatusComplete,
			Summary:     strings.TrimSpace(input.Summary),
			Evidence:    compactStringList(input.Evidence),
			CompletedBy: strings.TrimSpace(input.CompletedBy),
			CompletedAt: now,
		}
		if goal.CompletionAudit.CompletedBy == "" {
			goal.CompletionAudit.CompletedBy = normalizeGoalSource(input.Source)
		}
		return nil
	})
	if err != nil {
		return SessionGoal{}, err
	}
	if !mutated {
		return SessionGoal{}, errors.New("session has no current goal")
	}
	source := normalizeGoalSource(input.Source)
	if err := s.AppendGoalHistory(sessionID, GoalHistoryEntry{
		Type:   "goal.completed",
		Source: source,
		Status: goal.Status,
		Data: map[string]any{
			"summary":             goal.CompletionAudit.Summary,
			"evidence":            append([]string(nil), goal.CompletionAudit.Evidence...),
			"criteria_statuses":   append([]GoalItemStatusUpdate(nil), input.CriteriaStatuses...),
			"validation_statuses": append([]GoalItemStatusUpdate(nil), input.ValidationStatuses...),
		},
	}); err != nil {
		if rollbackErr := s.rollbackGoalAfterHistoryError(sessionID, rollback); rollbackErr != nil {
			return SessionGoal{}, fmt.Errorf("restore goal snapshot after %v: %w", err, rollbackErr)
		}
		return SessionGoal{}, err
	}
	return goal, nil
}

func (s *Store) SetGoalStatus(sessionID, status, source string) (SessionGoal, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	goal, mutated, err := s.MutateGoal(sessionID, func(goal *SessionGoal) error {
		if goal.GoalID == "" {
			return errors.New("session has no current goal")
		}
		goal.Status = status
		if status == GoalStatusComplete {
			goal.CompletedAt = now
			goal.CompletionAudit = &GoalCompletion{
				Status:      GoalStatusComplete,
				CompletedBy: normalizeGoalSource(source),
				CompletedAt: now,
			}
		} else {
			goal.CompletedAt = ""
			goal.CompletionAudit = nil
		}
		return nil
	})
	if err != nil {
		return SessionGoal{}, err
	}
	if !mutated {
		return SessionGoal{}, errors.New("session has no current goal")
	}
	return goal, nil
}

func (s *Store) ApproveMissionPlan(sessionID string, input MissionPlanApprovalInput) (SessionGoal, error) {
	approvedAt := strings.TrimSpace(input.ApprovedAt)
	if approvedAt == "" {
		approvedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	rollback, err := s.goalRollbackSnapshot(sessionID)
	if err != nil {
		return SessionGoal{}, err
	}
	goal, mutated, err := s.MutateGoal(sessionID, func(goal *SessionGoal) error {
		if goal.GoalID == "" {
			return errors.New("session has no current goal")
		}
		if goal.Mission == nil {
			return errors.New("mission plan is required before approval")
		}
		goal.Mission.PlanStatus = MissionPlanStatusApproved
		goal.Mission.ApprovedAt = approvedAt
		return nil
	})
	if err != nil {
		return SessionGoal{}, err
	}
	if !mutated {
		return SessionGoal{}, errors.New("session has no current goal")
	}
	source := normalizeGoalSource(input.Source)
	approvedSource := normalizeGoalSource(input.ApprovedSource)
	if strings.TrimSpace(input.ApprovedSource) == "" {
		approvedSource = source
	}
	if err := s.AppendGoalHistory(sessionID, GoalHistoryEntry{
		Type:   "mission.plan.approved",
		Source: source,
		Status: goal.Status,
		Data: map[string]any{
			"approved_at":       approvedAt,
			"approved_source":   approvedSource,
			"plan_mode_id":      input.PlanModeID,
			"approved_version":  input.ApprovedVersion,
			"coverage_override": input.CoverageOverride,
		},
	}); err != nil {
		if rollbackErr := s.rollbackGoalAfterHistoryError(sessionID, rollback); rollbackErr != nil {
			return SessionGoal{}, fmt.Errorf("restore goal snapshot after %v: %w", err, rollbackErr)
		}
		return SessionGoal{}, err
	}
	return goal, nil
}

func (s *Store) PatchGoal(sessionID string, input GoalPatchInput) (SessionGoal, error) {
	goal, mutated, err := s.MutateGoal(sessionID, func(goal *SessionGoal) error {
		if goal.GoalID == "" {
			return errors.New("session has no current goal")
		}
		if input.SuccessCriteria != nil {
			goal.SuccessCriteria = append([]GoalCriterion(nil), (*input.SuccessCriteria)...)
		}
		if input.ValidationPlan != nil {
			goal.ValidationPlan = append([]GoalValidation(nil), (*input.ValidationPlan)...)
		}
		if input.Control != nil {
			goal.Control = *input.Control
		}
		if input.Mission != nil {
			goal.Mode = GoalModeMission
			goal.Mission = input.Mission
		}
		return nil
	})
	if err != nil {
		return SessionGoal{}, err
	}
	if !mutated {
		return SessionGoal{}, errors.New("session has no current goal")
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
		goalWithTaskIDs := goal
		goal, err = s.mergeMissionTaskIDs(sessionID, goalWithTaskIDs)
		if err != nil {
			return SessionGoal{}, nil, false, err
		}
	}
	return goal, createdTasks, changed, nil
}

func (s *Store) mergeMissionTaskIDs(sessionID string, source SessionGoal) (SessionGoal, error) {
	if source.Mission == nil {
		return source, nil
	}
	featureTaskIDs := make(map[string][]string, len(source.Mission.Features))
	for _, feature := range source.Mission.Features {
		if strings.TrimSpace(feature.ID) == "" || len(feature.TaskIDs) == 0 {
			continue
		}
		featureTaskIDs[feature.ID] = append([]string(nil), feature.TaskIDs...)
	}
	goal, mutated, err := s.MutateGoal(sessionID, func(goal *SessionGoal) error {
		if goal.GoalID == "" {
			return errors.New("session has no current goal")
		}
		if goal.Mission == nil {
			return nil
		}
		for i := range goal.Mission.Features {
			feature := &goal.Mission.Features[i]
			if strings.TrimSpace(feature.ID) == "" && i < len(source.Mission.Features) && strings.TrimSpace(source.Mission.Features[i].ID) != "" {
				feature.ID = source.Mission.Features[i].ID
			}
			if ids, ok := featureTaskIDs[feature.ID]; ok {
				feature.TaskIDs = mergeStringLists(feature.TaskIDs, ids)
			}
		}
		return nil
	})
	if err != nil {
		return SessionGoal{}, err
	}
	if !mutated {
		return SessionGoal{}, errors.New("session has no current goal")
	}
	return goal, nil
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
	if err := rejectSymlinkPathAncestors(path); err != nil {
		return false, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("refusing to clear symlinked goal path: %s", path)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("refusing to clear non-regular goal path: %s", path)
	}
	if err := fileutil.RemoveFileNoSymlink(path); err != nil {
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
	if err := validateGoalHistoryEntry(entry); err != nil {
		return fmt.Errorf("validate goal-history.jsonl: %w", err)
	}
	if entry.GoalID == "" {
		if goal, err := s.loadGoalNoLock(sessionID); err == nil {
			entry.GoalID = goal.GoalID
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("load goal.json for goal history: %w", err)
		}
	}
	path, err := s.sessionPath(sessionID, "artifacts", "goal-history.jsonl")
	if err != nil {
		return err
	}
	var current []GoalHistoryEntry
	if err := readJSONL(path, &current); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("load goal history %s: %w", path, err)
	}
	current = append(current, entry)
	if err := validateGoalHistoryEntries(current); err != nil {
		return fmt.Errorf("validate goal-history.jsonl: %w", err)
	}
	if err := s.appendJSONL(path, entry); err != nil {
		return fmt.Errorf("append goal history %s: %w", path, err)
	}
	return nil
}

type goalRollback struct {
	Snapshot    SessionGoal
	HasSnapshot bool
}

func (s *Store) goalRollbackSnapshot(sessionID string) (goalRollback, error) {
	rollback := goalRollback{}
	previous, err := s.LoadGoal(sessionID)
	if errors.Is(err, fs.ErrNotExist) {
		return rollback, nil
	}
	if err != nil {
		return rollback, err
	}
	rollback.Snapshot = previous
	rollback.HasSnapshot = strings.TrimSpace(previous.GoalID) != ""
	return rollback, nil
}

func (s *Store) rollbackGoalAfterHistoryError(sessionID string, rollback goalRollback) error {
	if !rollback.HasSnapshot {
		return s.removeGoalSnapshot(sessionID)
	}
	return s.writeGoalSnapshot(sessionID, rollback.Snapshot)
}

func (s *Store) writeGoalSnapshot(sessionID string, goal SessionGoal) error {
	path, err := s.sessionPath(sessionID, "goal.json")
	if err != nil {
		return err
	}
	prepareGoalForSave(sessionID, &goal)
	if err := ValidateGoal(goal); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeJSONFile(path, goal)
}

func (s *Store) removeGoalSnapshot(sessionID string) error {
	path, err := s.sessionPath(sessionID, "goal.json")
	if err != nil {
		return err
	}
	if err := fileutil.RemoveFileNoSymlink(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
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
	if err != nil {
		return nil, err
	}
	if err := validateGoalHistoryEntries(out); err != nil {
		return nil, fmt.Errorf("validate goal-history.jsonl: %w", err)
	}
	return out, nil
}

func (s *Store) RestoreGoalHistory(sessionID string, entries []GoalHistoryEntry) error {
	path, err := s.sessionPath(sessionID, "artifacts", "goal-history.jsonl")
	if err != nil {
		return err
	}
	if err := validateGoalHistoryEntries(entries); err != nil {
		return fmt.Errorf("validate goal-history.jsonl: %w", err)
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

func validateGoalHistoryEntries(entries []GoalHistoryEntry) error {
	for i, entry := range entries {
		if err := validateGoalHistoryEntry(entry); err != nil {
			return fmt.Errorf("entry %d: %w", i, err)
		}
	}
	return nil
}

func validateGoalHistoryEntry(entry GoalHistoryEntry) error {
	if err := validateGoalRequiredTimestamp("goal history created_at", entry.CreatedAt); err != nil {
		return err
	}
	return nil
}

func (s *Store) UpdateGoalAccounting(sessionID string, delta GoalUsageDelta) (SessionGoal, bool, error) {
	budgetLimited := false
	rollback, err := s.goalRollbackSnapshot(sessionID)
	if err != nil {
		return SessionGoal{}, false, err
	}
	goal, mutated, err := s.MutateGoal(sessionID, func(goal *SessionGoal) error {
		if goal.GoalID == "" {
			return nil
		}
		goal.TokensUsed += maxInt64(0, delta.TokensUsedDelta)
		goal.TimeUsedSeconds += maxInt64(0, delta.TimeUsedSecondsDelta)
		if goal.Status == GoalStatusActive && goalBudgetExceeded(*goal) {
			goal.Status = GoalStatusBudgetLimited
			now := time.Now().UTC().Format(time.RFC3339Nano)
			goal.BudgetLimitedAt = now
			if goal.Control.StopOnBudget && goal.BudgetWrapUpRequestedAt == "" {
				goal.BudgetWrapUpRequestedAt = now
			}
			budgetLimited = true
		}
		return nil
	})
	if err != nil {
		return SessionGoal{}, false, err
	}
	if !mutated {
		return SessionGoal{}, false, nil
	}
	if err := s.AppendGoalHistory(sessionID, GoalHistoryEntry{
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
	}); err != nil {
		if rollbackErr := s.rollbackGoalAfterHistoryError(sessionID, rollback); rollbackErr != nil {
			return SessionGoal{}, false, fmt.Errorf("restore goal snapshot after %v: %w", err, rollbackErr)
		}
		return SessionGoal{}, false, err
	}
	if budgetLimited {
		if err := s.AppendGoalHistory(sessionID, GoalHistoryEntry{
			Type:   "goal.budget_limited",
			Source: GoalSourceSystem,
			Status: goal.Status,
			Data: map[string]any{
				"tokens_used":       goal.TokensUsed,
				"token_budget":      goal.TokenBudget,
				"time_used_seconds": goal.TimeUsedSeconds,
				"time_budget":       goal.TimeBudgetSeconds,
				"stop_on_budget":    goal.Control.StopOnBudget,
			},
		}); err != nil {
			if rollbackErr := s.rollbackGoalAfterHistoryError(sessionID, rollback); rollbackErr != nil {
				return SessionGoal{}, false, fmt.Errorf("restore goal snapshot after %v: %w", err, rollbackErr)
			}
			return SessionGoal{}, false, err
		}
		if goal.Control.StopOnBudget {
			if err := s.AppendGoalHistory(sessionID, GoalHistoryEntry{
				Type:   "goal.budget_wrapup_required",
				Source: GoalSourceSystem,
				Status: goal.Status,
				Data: map[string]any{
					"budget_wrapup_requested_at": goal.BudgetWrapUpRequestedAt,
				},
			}); err != nil {
				if rollbackErr := s.rollbackGoalAfterHistoryError(sessionID, rollback); rollbackErr != nil {
					return SessionGoal{}, false, fmt.Errorf("restore goal snapshot after %v: %w", err, rollbackErr)
				}
				return SessionGoal{}, false, err
			}
		}
	}
	return goal, budgetLimited, nil
}

func (s *Store) loadGoalNoLock(sessionID string) (SessionGoal, error) {
	var goal SessionGoal
	path, err := s.sessionPath(sessionID, "goal.json")
	if err != nil {
		return goal, err
	}
	if err := readJSONFile(path, &goal); err != nil {
		return goal, err
	}
	if err := ValidateGoal(goal); err != nil {
		return SessionGoal{}, fmt.Errorf("validate goal.json: %w", err)
	}
	return goal, nil
}

func NewGoalID() string {
	return "goal_" + randomHex(6)
}

func NewGoalHistoryID() string {
	return "goalhist_" + randomHex(6)
}

func NewGoalProgressID() string {
	return "goalprog_" + randomHex(6)
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

func NormalizeMissionPlanStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return MissionPlanStatusDraft
	case MissionPlanStatusDraft:
		return MissionPlanStatusDraft
	case MissionPlanStatusNeedsApproval:
		return MissionPlanStatusNeedsApproval
	case MissionPlanStatusApproved:
		return MissionPlanStatusApproved
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func IsMissionPlanStatus(value string) bool {
	switch NormalizeMissionPlanStatus(value) {
	case MissionPlanStatusDraft, MissionPlanStatusNeedsApproval, MissionPlanStatusApproved:
		return true
	default:
		return false
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

func unknownGoalItemHint(kind string, ids []string, omitField string) string {
	if len(ids) == 0 {
		return fmt.Sprintf("this goal has no %s items; call get_goal to inspect the current goal, and omit %s (system-assigned ids only, not free-form labels)", kind, omitField)
	}
	return fmt.Sprintf("valid %s ids are [%s]; get exact ids from get_goal (ids are system-assigned, not free-form labels)", kind, strings.Join(ids, ", "))
}

func applyGoalCriterionUpdates(items []GoalCriterion, updates []GoalItemStatusUpdate, now string) error {
	index := make(map[string]int, len(items))
	ids := make([]string, 0, len(items))
	for i, item := range items {
		index[item.ID] = i
		ids = append(ids, item.ID)
	}
	for _, update := range updates {
		id := strings.TrimSpace(update.ID)
		if id == "" {
			return errors.New("criterion status update id is required")
		}
		i, ok := index[id]
		if !ok {
			return fmt.Errorf("unknown criterion id %q: %s", id, unknownGoalItemHint("criterion", ids, "criteria_statuses"))
		}
		status := normalizeGoalEvidenceStatus(update.Status)
		if status != "" {
			items[i].Status = status
		}
		items[i].Evidence = mergeStringLists(items[i].Evidence, update.Evidence)
		items[i].UpdatedAt = now
	}
	return nil
}

func applyGoalValidationUpdates(items []GoalValidation, updates []GoalItemStatusUpdate, now string) error {
	index := make(map[string]int, len(items))
	ids := make([]string, 0, len(items))
	for i, item := range items {
		index[item.ID] = i
		ids = append(ids, item.ID)
	}
	for _, update := range updates {
		id := strings.TrimSpace(update.ID)
		if id == "" {
			return errors.New("validation status update id is required")
		}
		i, ok := index[id]
		if !ok {
			return fmt.Errorf("unknown validation id %q: %s", id, unknownGoalItemHint("validation", ids, "validation_statuses"))
		}
		status := normalizeGoalEvidenceStatus(update.Status)
		if status != "" {
			items[i].Status = status
		}
		items[i].Evidence = mergeStringLists(items[i].Evidence, update.Evidence)
		if strings.TrimSpace(update.LastRunAt) != "" {
			items[i].LastRunAt = strings.TrimSpace(update.LastRunAt)
		} else if status != "" || len(update.Evidence) > 0 {
			items[i].LastRunAt = now
		}
	}
	return nil
}

func normalizeGoalEvidenceStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return ""
	case "pending", "verified", "failed", "skipped", "blocked":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func mergeStringLists(existing []string, additions []string) []string {
	out := compactStringList(existing)
	seen := make(map[string]struct{}, len(out))
	for _, value := range out {
		seen[value] = struct{}{}
	}
	for _, value := range additions {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func compactStringList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
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
