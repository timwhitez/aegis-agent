package ngenrt

import (
	"context"
	"fmt"
	"strings"

	"ngen/internal/artifact"
	"ngen/internal/provider"
	"ngen/internal/task"
)

func (s *Service) HarnessEvaluation(ctx context.Context, taskID string) (task.HarnessEvaluation, error) {
	_ = ctx
	if _, err := s.Store.LoadTask(taskID); err != nil {
		return task.HarnessEvaluation{}, err
	}
	return s.Store.LoadHarnessEvaluation(taskID)
}

func (s *Service) captureHarnessEvaluation(ctx context.Context, taskID, runtimeAction string) (task.HarnessEvaluation, error) {
	_ = ctx
	spec, err := s.Store.LoadTask(taskID)
	if err != nil {
		return task.HarnessEvaluation{}, err
	}
	state, err := s.loadStateOrRecover(taskID)
	if err != nil {
		return task.HarnessEvaluation{}, err
	}

	criteriaMet, criteriaOpen := 0, 0
	criteriaRef := ""
	if criteria, loadErr := s.Store.LoadCriteria(taskID); loadErr == nil {
		criteriaMet = criteria.MetCount
		criteriaOpen = criteria.OpenCount
		criteriaRef = "criteria/latest.json"
	}

	verificationStatus := "not_run"
	if verification, loadErr := s.Store.LoadVerification(taskID); loadErr == nil {
		verificationStatus = strings.TrimSpace(verification.Status)
	}
	reviewStatus := "not_run"
	if review, loadErr := s.Store.LoadReview(taskID); loadErr == nil {
		reviewStatus = strings.TrimSpace(review.Status)
	}
	completionStatus := "not_evaluated"
	if completion, loadErr := s.Store.LoadCompletion(taskID); loadErr == nil {
		completionStatus = strings.TrimSpace(completion.Status)
	}

	workspaceEditStatuses := map[string]int{}
	repairAttemptCount := 0
	edits, err := s.Store.ReadWorkspaceEdits(taskID)
	if err != nil {
		return task.HarnessEvaluation{}, err
	}
	for _, edit := range edits {
		status := strings.TrimSpace(edit.Status)
		if status == "" {
			status = "unknown"
		}
		workspaceEditStatuses[status]++
	}

	events, err := s.Store.ReadEvents(taskID)
	if err != nil {
		return task.HarnessEvaluation{}, err
	}
	repairAttemptCount = maxInt(len(edits), countEvents(events, "workspace_edit_started"))
	actions := harnessActions(runtimeAction, events)
	workerActionCount := countWorkerActions(events)
	if workers, loadErr := s.Store.ListWorkerContracts(taskID); loadErr == nil {
		workerActionCount = maxInt(workerActionCount, len(workers))
	}

	memoryPromoteCount := 0
	if entries, loadErr := s.Store.ReadMemoryEntries(); loadErr == nil {
		for _, entry := range entries {
			if strings.TrimSpace(entry.TaskID) == taskID {
				memoryPromoteCount++
			}
		}
	}

	contextPackRef := ""
	if _, loadErr := s.Store.LoadContextSummary(taskID); loadErr == nil {
		contextPackRef = "context/latest-pack.json"
	}

	providerMode := provider.CanonicalMode(s.Config.Provider.Mode)
	if providerMode == "" {
		providerMode = "builtin"
	}
	tokenUsage := "unknown"
	promptCacheUsage := "unknown"
	usageRef := ""
	if usageRecords, loadErr := s.Store.ReadProviderUsage(taskID); loadErr != nil {
		return task.HarnessEvaluation{}, loadErr
	} else if usageRecord, ok := latestProviderUsage(usageRecords); ok {
		tokenUsage = firstNonEmpty(strings.TrimSpace(usageRecord.TokenUsage), "unknown")
		promptCacheUsage = firstNonEmpty(strings.TrimSpace(usageRecord.PromptCacheUsage), "unknown")
		usageRef = providerUsageRef(usageRecord)
	}
	eval := task.HarnessEvaluation{
		ObjectKind:               "harness_evaluation",
		SchemaVersion:            task.SchemaVersion,
		HarnessEvalID:            task.NewID("HEVAL"),
		TaskID:                   spec.TaskID,
		RuntimeAction:            strings.TrimSpace(runtimeAction),
		ProviderMode:             providerMode,
		Model:                    strings.TrimSpace(s.Config.Provider.Model),
		SystemPromptRef:          harnessSystemPromptRef(providerMode, s.Config.Provider.SystemPrompt),
		DecisionSchemaVersion:    "provider_decision.v1",
		ContextPackRef:           contextPackRef,
		ContinuityRef:            harnessContinuityRef(s.Store.ContinuityExists(taskID), "continuity/latest.json"),
		SprintRef:                harnessContinuityRef(s.Store.SprintExists(taskID), "sprint/latest.json"),
		CriteriaRef:              criteriaRef,
		RepairBudget:             s.codingRepairBudget(),
		ObservationCommandBudget: s.codingObservationCommandBudget(),
		ExecutionCommandBudget:   s.codingExecutionCommandBudget(),
		ActionsSelected:          actions,
		RepairAttemptCount:       repairAttemptCount,
		VerificationStatus:       verificationStatus,
		CriteriaMetCount:         criteriaMet,
		CriteriaOpenCount:        criteriaOpen,
		ReviewStatus:             reviewStatus,
		CompletionStatus:         completionStatus,
		BlockedReasonCode:        strings.TrimSpace(state.StatusReasonCode),
		WorkspaceEditStatuses:    nonEmptyStatusMap(workspaceEditStatuses),
		WorkerActionCount:        workerActionCount,
		MemoryPromoteCount:       memoryPromoteCount,
		ProviderUsageRef:         usageRef,
		TokenUsage:               tokenUsage,
		PromptCacheUsage:         promptCacheUsage,
		CreatedAt:                task.Now(),
	}
	eval.Summary = harnessEvaluationSummary(eval, state)
	eval.EvidenceRefs = s.harnessEvidenceRefs(taskID, state, eval)
	if err := s.Store.SaveHarnessEvaluation(eval); err != nil {
		return task.HarnessEvaluation{}, err
	}
	return eval, nil
}

func harnessSystemPromptRef(providerMode, configuredPrompt string) string {
	if strings.TrimSpace(configuredPrompt) != "" {
		return "config:provider.system_prompt"
	}
	if strings.TrimSpace(providerMode) == "" {
		providerMode = "builtin"
	}
	return fmt.Sprintf("provider:%s:default_system_prompt", providerMode)
}

func harnessContinuityRef(exists bool, ref string) string {
	if !exists {
		return ""
	}
	return ref
}

func harnessActions(runtimeAction string, events []task.Event) []string {
	actions := []string{strings.TrimSpace(runtimeAction)}
	for _, event := range takeLastEvents(events, 32) {
		switch strings.TrimSpace(event.Type) {
		case "provider_decided", "provider_responded", "project_task_created", "plan_mutated", "project_mutated", "memory_promoted", "worker_spawned", "worker_continued", "verification_passed", "verification_failed", "workspace_edit_applied", "workspace_edit_failed", "workspace_edit_noop", "review_completed", "done", "completion_rejected", "failed", "auto_turn_limit_reached":
			actions = append(actions, event.Type)
		}
	}
	return uniqueNonEmptyStrings(actions)
}

func takeLastEvents(events []task.Event, limit int) []task.Event {
	if limit <= 0 || len(events) <= limit {
		return events
	}
	return events[len(events)-limit:]
}

func countEvents(events []task.Event, typ string) int {
	count := 0
	for _, event := range events {
		if strings.TrimSpace(event.Type) == typ {
			count++
		}
	}
	return count
}

func countWorkerActions(events []task.Event) int {
	count := 0
	for _, event := range events {
		if strings.HasPrefix(strings.TrimSpace(event.Type), "worker_") {
			count++
		}
	}
	return count
}

func nonEmptyStatusMap(statuses map[string]int) map[string]int {
	if len(statuses) == 0 {
		return nil
	}
	return statuses
}

func (s *Service) harnessEvidenceRefs(taskID string, state task.State, eval task.HarnessEvaluation) []string {
	refs := []string{"task.json", "state.json", "plan.json"}
	if s.Store.HasBaseline(taskID) {
		refs = append(refs, "baseline.json")
	}
	if eval.CriteriaRef != "" {
		refs = append(refs, eval.CriteriaRef)
	}
	if eval.ContextPackRef != "" {
		refs = append(refs, eval.ContextPackRef)
	}
	if eval.ContinuityRef != "" {
		refs = append(refs, eval.ContinuityRef)
	}
	if eval.SprintRef != "" {
		refs = append(refs, eval.SprintRef)
	}
	if state.LastVerificationRef != "" {
		refs = append(refs, state.LastVerificationRef)
	}
	if state.LastReviewRef != "" {
		refs = append(refs, state.LastReviewRef)
	}
	if state.LastCompletionRef != "" {
		refs = append(refs, state.LastCompletionRef)
	}
	if eval.ProviderUsageRef != "" {
		refs = append(refs, eval.ProviderUsageRef)
	}
	if state.LastCheckpointRef != "" {
		refs = append(refs, state.LastCheckpointRef)
	}
	if s.Store.HandoffExists(taskID) {
		refs = append(refs, "handoff.md")
	}
	if edits, err := s.Store.ReadWorkspaceEdits(taskID); err == nil && len(edits) > 0 {
		refs = append(refs, artifact.WorkspaceEditRecordRef(edits[len(edits)-1].EditRecordID))
	}
	if commands, err := s.Store.ReadCommandRuns(taskID); err == nil && len(commands) > 0 {
		refs = append(refs, artifact.CommandRunRecordRef(commands[len(commands)-1].CommandRecordID))
	}
	if state.LastEventRef != "" {
		refs = append(refs, state.LastEventRef)
	}
	return uniqueRefs(refs)
}

func harnessEvaluationSummary(eval task.HarnessEvaluation, state task.State) string {
	parts := []string{
		fmt.Sprintf("action=%s", eval.RuntimeAction),
		fmt.Sprintf("state=%s", state.State),
		fmt.Sprintf("verification=%s", firstNonEmpty(eval.VerificationStatus, "not_run")),
		fmt.Sprintf("review=%s", firstNonEmpty(eval.ReviewStatus, "not_run")),
		fmt.Sprintf("completion=%s", firstNonEmpty(eval.CompletionStatus, "not_evaluated")),
	}
	if eval.BlockedReasonCode != "" {
		parts = append(parts, fmt.Sprintf("blocked_reason=%s", eval.BlockedReasonCode))
	}
	return "Harness evaluation captured " + strings.Join(parts, ", ") + "."
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
