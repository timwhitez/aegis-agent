package session

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type MissionPlanCoverage struct {
	ValidationTotal               int      `json:"validation_total"`
	CoveredAssertions             int      `json:"covered_assertions"`
	FeatureCoveredAssertions      int      `json:"feature_covered_assertions"`
	MilestoneCoveredAssertions    int      `json:"milestone_covered_assertions"`
	UncoveredAssertions           []string `json:"uncovered_assertions,omitempty"`
	FeaturesWithoutAssertions     []string `json:"features_without_assertions,omitempty"`
	MilestonesWithoutValidation   []string `json:"milestones_without_validation,omitempty"`
	DuplicateValidationIDs        []string `json:"duplicate_validation_ids,omitempty"`
	BlankValidationIndexes        []int    `json:"blank_validation_indexes,omitempty"`
	UnknownClaimedAssertions      []string `json:"unknown_claimed_assertions,omitempty"`
	UnknownMilestoneValidationIDs []string `json:"unknown_milestone_validation_ids,omitempty"`
	ApprovalBlocked               bool     `json:"approval_blocked"`
}

func (r MissionPlanCoverage) Pass() bool {
	return !r.ApprovalBlocked
}

func (r MissionPlanCoverage) BlockingSummary() string {
	if !r.ApprovalBlocked {
		return ""
	}
	parts := []string{}
	if len(r.UncoveredAssertions) > 0 {
		parts = append(parts, "uncovered assertions: "+strings.Join(r.UncoveredAssertions, ", "))
	}
	if len(r.DuplicateValidationIDs) > 0 {
		parts = append(parts, "duplicate assertion ids: "+strings.Join(r.DuplicateValidationIDs, ", "))
	}
	if len(r.BlankValidationIndexes) > 0 {
		parts = append(parts, fmt.Sprintf("blank assertion ids: %v", r.BlankValidationIndexes))
	}
	if len(r.UnknownClaimedAssertions) > 0 {
		parts = append(parts, "unknown feature assertions: "+strings.Join(r.UnknownClaimedAssertions, ", "))
	}
	if len(r.UnknownMilestoneValidationIDs) > 0 {
		parts = append(parts, "unknown milestone validation ids: "+strings.Join(r.UnknownMilestoneValidationIDs, ", "))
	}
	if len(parts) == 0 {
		return "mission validation coverage is incomplete"
	}
	return strings.Join(parts, "; ")
}

func CheckMissionPlanCoverage(goal SessionGoal) MissionPlanCoverage {
	var report MissionPlanCoverage
	if goal.Mission == nil {
		return report
	}
	contractIDs := []string{}
	contractSet := map[string]struct{}{}
	seen := map[string]struct{}{}
	for i, item := range goal.Mission.ValidationContract {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			report.BlankValidationIndexes = append(report.BlankValidationIndexes, i)
			continue
		}
		if _, ok := seen[id]; ok {
			report.DuplicateValidationIDs = appendUnique(report.DuplicateValidationIDs, id)
			continue
		}
		seen[id] = struct{}{}
		contractIDs = append(contractIDs, id)
		contractSet[id] = struct{}{}
	}
	report.ValidationTotal = len(contractIDs)
	featureCovered := map[string]struct{}{}
	milestoneCovered := map[string]struct{}{}
	for _, feature := range goal.Mission.Features {
		claims := compactStringList(feature.ClaimedAssertions)
		if len(claims) == 0 {
			report.FeaturesWithoutAssertions = append(report.FeaturesWithoutAssertions, firstNonEmpty(feature.ID, feature.Title))
			continue
		}
		for _, id := range claims {
			if _, ok := contractSet[id]; !ok {
				report.UnknownClaimedAssertions = appendUnique(report.UnknownClaimedAssertions, id)
				continue
			}
			featureCovered[id] = struct{}{}
		}
	}
	for _, milestone := range goal.Mission.Milestones {
		ids := compactStringList(milestone.ValidationIDs)
		if len(ids) == 0 {
			report.MilestonesWithoutValidation = append(report.MilestonesWithoutValidation, firstNonEmpty(milestone.ID, milestone.Title))
			continue
		}
		for _, id := range ids {
			if _, ok := contractSet[id]; !ok {
				report.UnknownMilestoneValidationIDs = appendUnique(report.UnknownMilestoneValidationIDs, id)
				continue
			}
			milestoneCovered[id] = struct{}{}
		}
	}
	covered := map[string]struct{}{}
	for id := range featureCovered {
		covered[id] = struct{}{}
	}
	for id := range milestoneCovered {
		covered[id] = struct{}{}
	}
	report.FeatureCoveredAssertions = len(featureCovered)
	report.MilestoneCoveredAssertions = len(milestoneCovered)
	report.CoveredAssertions = len(covered)
	for _, id := range contractIDs {
		if _, ok := covered[id]; !ok {
			report.UncoveredAssertions = append(report.UncoveredAssertions, id)
		}
	}
	report.ApprovalBlocked = len(goal.Mission.ValidationContract) > 0 && (len(report.UncoveredAssertions) > 0 ||
		len(report.DuplicateValidationIDs) > 0 ||
		len(report.BlankValidationIndexes) > 0 ||
		len(report.UnknownClaimedAssertions) > 0 ||
		len(report.UnknownMilestoneValidationIDs) > 0)
	return report
}

func (s *Store) RecordGoalProgress(sessionID string, input GoalProgressInput) (SessionGoal, GoalProgressRecord, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	source := normalizeGoalSource(input.Source)
	planChanged := false
	validationChanged := false
	record := GoalProgressRecord{}
	goal, mutated, err := s.MutateGoal(sessionID, func(goal *SessionGoal) error {
		if goal.GoalID == "" {
			return errors.New("session has no current goal")
		}
		if len(input.FeatureUpdates) > 0 || len(input.MilestoneUpdates) > 0 {
			if goal.Mission == nil {
				return errors.New("mission plan is required for feature or milestone updates")
			}
			for _, update := range input.FeatureUpdates {
				if err := applyFeatureProgressUpdate(goal.Mission.Features, update); err != nil {
					return err
				}
				planChanged = true
			}
			for _, update := range input.MilestoneUpdates {
				if err := applyMilestoneProgressUpdate(goal.Mission.Milestones, update); err != nil {
					return err
				}
				planChanged = true
			}
		}
		for _, update := range input.ValidationUpdates {
			changed, err := applyValidationProgressUpdate(goal, update, now)
			if err != nil {
				return err
			}
			validationChanged = validationChanged || changed
		}
		record = buildGoalProgressRecord(input, source, now)
		if record.ID != "" {
			goal.Progress = append(goal.Progress, record)
			if record.Kind == "budget_wrapup" {
				goal.BudgetWrapUpRecordedAt = now
			}
		}
		if !planChanged && !validationChanged && record.ID == "" {
			return errors.New("record_goal_progress requires at least one progress record or item update")
		}
		return nil
	})
	if err != nil {
		return SessionGoal{}, GoalProgressRecord{}, err
	}
	if !mutated {
		return SessionGoal{}, GoalProgressRecord{}, errors.New("session has no current goal")
	}
	if record.ID != "" {
		_ = s.AppendGoalHistory(sessionID, GoalHistoryEntry{
			Type:   "goal.progress.recorded",
			Source: source,
			Status: goal.Status,
			Data: map[string]any{
				"progress": record,
			},
		})
	}
	if planChanged {
		_ = s.AppendGoalHistory(sessionID, GoalHistoryEntry{
			Type:   "mission.plan.updated",
			Source: source,
			Status: goal.Status,
			Data: map[string]any{
				"feature_updates":   input.FeatureUpdates,
				"milestone_updates": input.MilestoneUpdates,
			},
		})
	}
	if validationChanged {
		_ = s.AppendGoalHistory(sessionID, GoalHistoryEntry{
			Type:   "mission.validation.updated",
			Source: source,
			Status: goal.Status,
			Data: map[string]any{
				"validation_updates": input.ValidationUpdates,
			},
		})
	}
	return goal, record, nil
}

func HasBudgetWrapUpRecord(goal SessionGoal) bool {
	if strings.TrimSpace(goal.BudgetWrapUpRecordedAt) != "" {
		return true
	}
	for _, record := range goal.Progress {
		if record.Kind == "budget_wrapup" {
			return true
		}
	}
	return false
}

func MarkBudgetWrapUpTurnStarted(goal *SessionGoal) bool {
	if goal == nil || goal.Status != GoalStatusBudgetLimited || !goal.Control.StopOnBudget || goal.BudgetWrapUpRequestedAt == "" || goal.BudgetWrapUpTurnStartedAt != "" {
		return false
	}
	goal.BudgetWrapUpTurnStartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return true
}

func buildGoalProgressRecord(input GoalProgressInput, source, now string) GoalProgressRecord {
	record := GoalProgressRecord{
		Kind:            normalizeProgressKind(input.Kind),
		Summary:         strings.TrimSpace(input.Summary),
		Evidence:        compactStringList(input.Evidence),
		LinkedArtifacts: compactStringList(input.LinkedArtifacts),
		Commands:        compactProgressCommands(input.Commands),
		Blockers:        compactStringList(input.Blockers),
		ChildSessionIDs: compactStringList(input.ChildSessionIDs),
		QueueJobIDs:     compactStringList(input.QueueJobIDs),
		ValidationIDs:   compactStringList(input.ValidationIDs),
		Source:          source,
		CreatedAt:       now,
	}
	if record.Summary == "" && len(record.Evidence) == 0 && len(record.LinkedArtifacts) == 0 && len(record.Commands) == 0 && len(record.Blockers) == 0 && len(record.ChildSessionIDs) == 0 && len(record.QueueJobIDs) == 0 && len(record.ValidationIDs) == 0 {
		return GoalProgressRecord{}
	}
	record.ID = NewGoalProgressID()
	return record
}

func normalizeProgressKind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "progress":
		return "progress"
	case "handoff", "budget_wrapup", "validation", "blocker":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func compactProgressCommands(commands []GoalProgressCommand) []GoalProgressCommand {
	out := make([]GoalProgressCommand, 0, len(commands))
	for _, command := range commands {
		trimmed := GoalProgressCommand{
			Command:  strings.TrimSpace(command.Command),
			ExitCode: command.ExitCode,
			Artifact: strings.TrimSpace(command.Artifact),
			Summary:  strings.TrimSpace(command.Summary),
		}
		if trimmed.Command == "" && trimmed.Artifact == "" && trimmed.Summary == "" && trimmed.ExitCode == nil {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func applyFeatureProgressUpdate(items []MissionFeature, update MissionFeatureProgressUpdate) error {
	id := strings.TrimSpace(update.ID)
	if id == "" {
		return errors.New("feature update id is required")
	}
	for i := range items {
		if items[i].ID != id {
			continue
		}
		if status := normalizeGoalEvidenceStatus(update.Status); status != "" {
			items[i].Status = status
		}
		items[i].Evidence = mergeStringLists(items[i].Evidence, update.Evidence)
		items[i].ClaimedAssertions = mergeStringLists(items[i].ClaimedAssertions, update.ClaimedAssertions)
		items[i].TaskIDs = mergeStringLists(items[i].TaskIDs, update.TaskIDs)
		items[i].ChildSessionIDs = mergeStringLists(items[i].ChildSessionIDs, update.ChildSessionIDs)
		items[i].QueueJobIDs = mergeStringLists(items[i].QueueJobIDs, update.QueueJobIDs)
		return nil
	}
	return fmt.Errorf("unknown feature id: %s", id)
}

func applyMilestoneProgressUpdate(items []MissionMilestone, update MissionMilestoneProgressUpdate) error {
	id := strings.TrimSpace(update.ID)
	if id == "" {
		return errors.New("milestone update id is required")
	}
	for i := range items {
		if items[i].ID != id {
			continue
		}
		if status := normalizeGoalEvidenceStatus(update.Status); status != "" {
			items[i].Status = status
		}
		items[i].Evidence = mergeStringLists(items[i].Evidence, update.Evidence)
		items[i].FeatureIDs = mergeStringLists(items[i].FeatureIDs, update.FeatureIDs)
		items[i].ValidationIDs = mergeStringLists(items[i].ValidationIDs, update.ValidationIDs)
		items[i].TaskIDs = mergeStringLists(items[i].TaskIDs, update.TaskIDs)
		items[i].ChildSessionIDs = mergeStringLists(items[i].ChildSessionIDs, update.ChildSessionIDs)
		items[i].QueueJobIDs = mergeStringLists(items[i].QueueJobIDs, update.QueueJobIDs)
		return nil
	}
	return fmt.Errorf("unknown milestone id: %s", id)
}

func applyValidationProgressUpdate(goal *SessionGoal, update GoalValidationProgressUpdate, now string) (bool, error) {
	id := strings.TrimSpace(update.ID)
	if id == "" {
		return false, errors.New("validation update id is required")
	}
	changed := false
	for i := range goal.ValidationPlan {
		if goal.ValidationPlan[i].ID == id {
			applyOneValidationProgressUpdate(&goal.ValidationPlan[i], update, now)
			changed = true
		}
	}
	if goal.Mission != nil {
		for i := range goal.Mission.ValidationContract {
			if goal.Mission.ValidationContract[i].ID == id {
				applyOneValidationProgressUpdate(&goal.Mission.ValidationContract[i], update, now)
				changed = true
			}
		}
	}
	if !changed {
		return false, fmt.Errorf("unknown validation id: %s", id)
	}
	return true, nil
}

func applyOneValidationProgressUpdate(item *GoalValidation, update GoalValidationProgressUpdate, now string) {
	if status := normalizeGoalEvidenceStatus(update.Status); status != "" {
		item.Status = status
	}
	item.Evidence = mergeStringLists(item.Evidence, update.Evidence)
	item.ChildSessionIDs = mergeStringLists(item.ChildSessionIDs, update.ChildSessionIDs)
	item.QueueJobIDs = mergeStringLists(item.QueueJobIDs, update.QueueJobIDs)
	item.EvaluatorEvidence = append(item.EvaluatorEvidence, compactEvaluatorEvidence(update.EvaluatorEvidence, now)...)
	if strings.TrimSpace(update.LastRunAt) != "" {
		item.LastRunAt = strings.TrimSpace(update.LastRunAt)
	} else if update.Status != "" || len(update.Evidence) > 0 || len(update.EvaluatorEvidence) > 0 {
		item.LastRunAt = now
	}
}

func compactEvaluatorEvidence(items []GoalEvaluatorEvidence, now string) []GoalEvaluatorEvidence {
	out := make([]GoalEvaluatorEvidence, 0, len(items))
	for _, item := range items {
		evidence := GoalEvaluatorEvidence{
			Role:           strings.TrimSpace(item.Role),
			ChildSessionID: strings.TrimSpace(item.ChildSessionID),
			QueueJobID:     strings.TrimSpace(item.QueueJobID),
			Artifact:       strings.TrimSpace(item.Artifact),
			Summary:        strings.TrimSpace(item.Summary),
			Status:         normalizeGoalEvidenceStatus(item.Status),
			CreatedAt:      strings.TrimSpace(item.CreatedAt),
		}
		if evidence.Role == "" {
			evidence.Role = "evaluator"
		}
		if evidence.CreatedAt == "" {
			evidence.CreatedAt = now
		}
		if evidence.ChildSessionID == "" && evidence.QueueJobID == "" && evidence.Artifact == "" && evidence.Summary == "" {
			continue
		}
		out = append(out, evidence)
	}
	return out
}

func appendUnique(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
