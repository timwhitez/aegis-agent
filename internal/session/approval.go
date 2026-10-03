package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync/atomic"
	"time"
)

// ApprovalTarget identifies the exact content reviewed by an operator. A
// revision is a concurrency condition, not an authorization credential.
type ApprovalTarget struct {
	PlanModeID       string `json:"plan_mode_id"`
	PlanVersion      int    `json:"plan_version"`
	ExpectedRevision string `json:"expected_revision"`
}

type ApprovalSnapshot struct {
	PlanMode PlanModeState        `json:"plan_mode"`
	Goal     *SessionGoal         `json:"goal,omitempty"`
	Revision string               `json:"approval_revision"`
	Coverage *MissionPlanCoverage `json:"coverage,omitempty"`
}

func (snapshot ApprovalSnapshot) Target() ApprovalTarget {
	return ApprovalTarget{PlanModeID: snapshot.PlanMode.PlanModeID, PlanVersion: snapshot.PlanMode.PlanVersion, ExpectedRevision: snapshot.Revision}
}

var ErrApprovalConflict = errors.New("plan approval content changed; review the current plan")
var ErrMissingApprovalTarget = errors.New("approval target is required; reload the plan or upgrade the client")
var ErrApprovalScopeClosed = errors.New("approval store scope is no longer active")

type approvalScope struct {
	sessionID string
	active    atomic.Bool
}

// WithApprovalLock coordinates plan and linked goal facts across Store
// instances and processes. The lock order is approval.lock, Store.mu, then
// existing individual file locks. It never holds Store.mu around the callback.
// The callback must use its supplied Store, synchronously; retaining it or
// calling the original Store from inside the callback is not supported.
// Coordination does not make multi-file writes an atomic transaction.
func (s *Store) WithApprovalLock(sessionID string, fn func(*Store) error) error {
	if err := validateStoreID("session", sessionID); err != nil {
		return err
	}
	if s.approvalScope != nil {
		if !s.approvalScope.active.Load() {
			return ErrApprovalScopeClosed
		}
		if s.approvalScope.sessionID != sessionID {
			return errors.New("approval store scope belongs to another session")
		}
		return fn(s)
	}
	lockPath, err := s.sessionPath(sessionID, "approval.lock")
	if err != nil {
		return err
	}
	return s.withExistingFileLock(lockPath, func() error {
		// A fresh Store keeps mutexes and validation caches private. Copying a
		// live Store or toggling a bypass flag on it would race other callers.
		scoped := NewStoreWithDirMode(s.root, s.dirMode)
		scoped.beforePlanModeMarkdownWrite = s.beforePlanModeMarkdownWrite
		scoped.beforeQueueClaimRename = s.beforeQueueClaimRename
		scoped.beforeQueueClaimLeaseWrite = s.beforeQueueClaimLeaseWrite
		scoped.beforeQueueReapCommit = s.beforeQueueReapCommit
		scoped.approvalScope = &approvalScope{sessionID: sessionID}
		scoped.approvalScope.active.Store(true)
		defer scoped.approvalScope.active.Store(false)
		return fn(scoped)
	})
}

func (s *Store) LoadApprovalSnapshot(sessionID string) (snapshot ApprovalSnapshot, err error) {
	err = s.WithApprovalLock(sessionID, func(scoped *Store) error {
		var loadErr error
		snapshot, loadErr = scoped.loadApprovalSnapshotLocked(sessionID)
		return loadErr
	})
	return snapshot, err
}

func (s *Store) loadApprovalSnapshotLocked(sessionID string) (ApprovalSnapshot, error) {
	var snapshot ApprovalSnapshot
	plan, err := s.LoadPlanMode(sessionID)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return snapshot, fmt.Errorf("load planmode.json: %w", err)
	}
	if err == nil {
		snapshot.PlanMode = plan
	}
	goal, err := s.LoadGoal(sessionID)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return snapshot, fmt.Errorf("load goal.json: %w", err)
	}
	if err == nil {
		snapshot.Goal = &goal
		if plan.LinkedGoalID != "" && plan.LinkedGoalID == goal.GoalID && goal.Mission != nil {
			coverage := CheckMissionPlanCoverage(goal)
			snapshot.Coverage = &coverage
		}
	}
	revision, err := approvalRevision(snapshot)
	if err != nil {
		return ApprovalSnapshot{}, err
	}
	snapshot.Revision = revision
	return snapshot, nil
}

// ValidateApprovalTarget is side-effect-free. State transitions are checked
// separately, so approving or entering executing does not alter a content hash.
func ValidateApprovalTarget(snapshot ApprovalSnapshot, target ApprovalTarget) error {
	if strings.TrimSpace(target.PlanModeID) == "" || target.PlanVersion <= 0 || strings.TrimSpace(target.ExpectedRevision) == "" {
		return ErrMissingApprovalTarget
	}
	plan := snapshot.PlanMode
	if plan.PlanModeID != target.PlanModeID || plan.PlanVersion != target.PlanVersion || snapshot.Revision != target.ExpectedRevision {
		return ErrApprovalConflict
	}
	if !plan.Enabled || (plan.Status != PlanModeStatusAwaitingApproval && plan.Status != PlanModeStatusApproved && plan.Status != PlanModeStatusExecuting) {
		return fmt.Errorf("%w: plan mode is not awaiting approval: %s", ErrApprovalConflict, plan.Status)
	}
	if plan.LinkedGoalID != "" && (snapshot.Goal == nil || snapshot.Goal.GoalID != plan.LinkedGoalID) {
		return fmt.Errorf("%w: linked goal is missing or was replaced", ErrApprovalConflict)
	}
	return nil
}

func (s *Store) ApprovePlanModeTarget(sessionID, source string, target ApprovalTarget, overrideCoverage bool) (state PlanModeState, err error) {
	err = s.WithApprovalLock(sessionID, func(scoped *Store) error {
		snapshot, err := scoped.LoadApprovalSnapshot(sessionID)
		if err != nil {
			return err
		}
		if err := ValidateApprovalTarget(snapshot, target); err != nil {
			return err
		}
		if snapshot.Coverage != nil && snapshot.Coverage.ApprovalBlocked && !overrideCoverage {
			return fmt.Errorf("mission validation coverage blocks approval: %s", snapshot.Coverage.BlockingSummary())
		}
		if snapshot.PlanMode.Status == PlanModeStatusExecuting && snapshot.PlanMode.ApprovedRevision == snapshot.Revision {
			state = snapshot.PlanMode
			return nil
		}
		state, err = scoped.approvePlanModeRevisionLocked(sessionID, source, snapshot.Revision)
		return err
	})
	return state, err
}

// Approval revision v1 has an explicit semantic projection. Runtime statuses,
// accounting, evidence, execution links and approval timestamps are omitted;
// assertion and feature mappings remain included regardless of their writer.
func approvalRevision(snapshot ApprovalSnapshot) (string, error) {
	plan := snapshot.PlanMode
	projection := struct {
		SchemaVersion int           `json:"schema_version"`
		Plan          PlanModeState `json:"plan"`
		GoalExists    bool          `json:"linked_goal_exists"`
		Goal          *SessionGoal  `json:"linked_goal,omitempty"`
	}{SchemaVersion: 1}
	projection.Plan = PlanModeState{
		PlanModeID: plan.PlanModeID, Enabled: plan.Enabled, Objective: plan.Objective,
		LinkedGoalID: plan.LinkedGoalID, PlanID: plan.PlanID, PlanVersion: plan.PlanVersion,
		PlanMarkdown: plan.PlanMarkdown, Summary: plan.Summary, Assumptions: plan.Assumptions,
		Risks: plan.Risks, Verification: plan.Verification,
	}
	if plan.LinkedGoalID != "" && snapshot.Goal != nil {
		projection.GoalExists = true
		goal := snapshot.Goal
		projected := SessionGoal{
			GoalID: goal.GoalID, Mode: goal.Mode, Objective: goal.Objective,
			TokenBudget: goal.TokenBudget, TimeBudgetSeconds: goal.TimeBudgetSeconds,
			Control: goal.Control,
		}
		for _, criterion := range goal.SuccessCriteria {
			projected.SuccessCriteria = append(projected.SuccessCriteria, GoalCriterion{ID: criterion.ID, Text: criterion.Text, Required: criterion.Required})
		}
		projected.ValidationPlan = approvalValidationDefinitions(goal.ValidationPlan)
		if goal.Mission != nil {
			mission := goal.Mission
			projected.Mission = &MissionPlan{
				SharedArtifacts: mission.SharedArtifacts, KnowledgeArtifacts: mission.KnowledgeArtifacts,
				CreateTasksFromPlan: mission.CreateTasksFromPlan,
				ValidationContract:  approvalValidationDefinitions(mission.ValidationContract),
			}
			for _, item := range mission.Requirements {
				projected.Mission.Requirements = append(projected.Mission.Requirements, MissionRequirement{ID: item.ID, Text: item.Text, Source: item.Source})
			}
			for _, item := range mission.Features {
				projected.Mission.Features = append(projected.Mission.Features, MissionFeature{ID: item.ID, Title: item.Title, Description: item.Description, MilestoneID: item.MilestoneID, ClaimedAssertions: item.ClaimedAssertions})
			}
			for _, item := range mission.Milestones {
				projected.Mission.Milestones = append(projected.Mission.Milestones, MissionMilestone{ID: item.ID, Title: item.Title, FeatureIDs: item.FeatureIDs, ValidationIDs: item.ValidationIDs})
			}
			for _, item := range mission.RolePlan {
				projected.Mission.RolePlan = append(projected.Mission.RolePlan, MissionRole{Name: item.Name, Role: item.Role, Scope: item.Scope, Provider: item.Provider, Model: item.Model, Tools: item.Tools})
			}
		}
		projection.Goal = &projected
	}
	data, err := json.Marshal(projection)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return "approval-v1:" + hex.EncodeToString(digest[:]), nil
}

func approvalValidationDefinitions(items []GoalValidation) []GoalValidation {
	var definitions []GoalValidation
	for _, item := range items {
		definitions = append(definitions, GoalValidation{ID: item.ID, Kind: item.Kind, Command: item.Command, Artifact: item.Artifact, Description: item.Description})
	}
	return definitions
}

func (s *Store) approvePlanModeRevisionLocked(sessionID, source, revision string) (PlanModeState, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rollback, err := s.planModeRollbackSnapshot(sessionID)
	if err != nil {
		return PlanModeState{}, err
	}
	state, mutated, err := s.MutatePlanMode(sessionID, func(state *PlanModeState) error {
		if state.PlanModeID == "" {
			return errors.New("session has no current plan mode")
		}
		if state.Status != PlanModeStatusAwaitingApproval && state.Status != PlanModeStatusApproved && state.Status != PlanModeStatusExecuting {
			return fmt.Errorf("plan mode is not awaiting approval: %s", state.Status)
		}
		if state.PlanVersion <= 0 || strings.TrimSpace(state.PlanMarkdown) == "" {
			return errors.New("plan mode has no submitted plan")
		}
		state.Status = PlanModeStatusApproved
		state.ApprovedVersion = state.PlanVersion
		state.ApprovedRevision = revision
		state.Approvals = append(state.Approvals, PlanModeApproval{Version: state.PlanVersion, Source: normalizePlanModeSource(source), ApprovedBy: "operator", ApprovedAt: now, ApprovedRevision: revision})
		return nil
	})
	if err != nil {
		if rollbackErr := s.rollbackPlanModeAfterHistoryError(sessionID, rollback); rollbackErr != nil {
			return PlanModeState{}, fmt.Errorf("restore plan mode snapshot after approval write error %v: %w", err, rollbackErr)
		}
		return PlanModeState{}, err
	}
	if !mutated {
		return PlanModeState{}, errors.New("session has no current plan mode")
	}
	data := map[string]any(nil)
	if revision != "" {
		data = map[string]any{"approved_revision": revision}
	}
	if err := s.AppendPlanModeHistory(sessionID, PlanModeHistoryEntry{PlanModeID: state.PlanModeID, Type: "planmode.plan_approved", Source: normalizePlanModeSource(source), Status: state.Status, PlanVersion: state.PlanVersion, Data: data}); err != nil {
		if rollbackErr := s.rollbackPlanModeAfterHistoryError(sessionID, rollback); rollbackErr != nil {
			return PlanModeState{}, fmt.Errorf("restore plan mode snapshot after %v: %w", err, rollbackErr)
		}
		return PlanModeState{}, err
	}
	return state, nil
}
