package ngenrt

import (
	"strings"

	"ngen/internal/artifact"
	"ngen/internal/provider"
	"ngen/internal/task"
)

const (
	providerUsageOperationDecision             = "decision"
	providerUsageOperationWorkspaceObservation = "workspace_observation"
	providerUsageOperationWorkspaceEdit        = "workspace_edit"
	providerUsageOperationMissionValidation    = "mission_validation"
)

func providerUsageFromDecision(decision provider.Decision) (string, string) {
	return firstNonEmpty(strings.TrimSpace(decision.TokenUsage), "unknown"), firstNonEmpty(strings.TrimSpace(decision.PromptCacheUsage), "unknown")
}

func providerUsageFromWorkspaceObservation(plan provider.WorkspaceObservationPlan) (string, string) {
	return firstNonEmpty(strings.TrimSpace(plan.TokenUsage), "unknown"), firstNonEmpty(strings.TrimSpace(plan.PromptCacheUsage), "unknown")
}

func providerUsageFromWorkspaceEdit(plan provider.WorkspaceEditPlan) (string, string) {
	return firstNonEmpty(strings.TrimSpace(plan.TokenUsage), "unknown"), firstNonEmpty(strings.TrimSpace(plan.PromptCacheUsage), "unknown")
}

func providerUsageFromMissionValidation(result provider.MissionValidationResult) (string, string) {
	return firstNonEmpty(strings.TrimSpace(result.TokenUsage), "unknown"), firstNonEmpty(strings.TrimSpace(result.PromptCacheUsage), "unknown")
}

func (s *Service) appendProviderUsage(taskID, operation string, cfg task.ProviderConfig, tokenUsage, promptCacheUsage string, refs []string) (task.ProviderUsageRecord, error) {
	record := task.ProviderUsageRecord{
		ObjectKind:       "provider_usage",
		SchemaVersion:    task.SchemaVersion,
		UsageRecordID:    task.NewID("PUSE"),
		TaskID:           taskID,
		TS:               task.Now(),
		Operation:        strings.TrimSpace(operation),
		ProviderMode:     provider.CanonicalMode(cfg.Mode),
		Model:            strings.TrimSpace(cfg.Model),
		TokenUsage:       firstNonEmpty(strings.TrimSpace(tokenUsage), "unknown"),
		PromptCacheUsage: firstNonEmpty(strings.TrimSpace(promptCacheUsage), "unknown"),
		Cost:             "unknown",
		Refs:             uniqueRefs(refs),
	}
	if record.ProviderMode == "" {
		record.ProviderMode = "builtin"
	}
	if record.Operation == "" {
		record.Operation = "unknown"
	}
	if err := s.Store.AppendProviderUsage(record); err != nil {
		return task.ProviderUsageRecord{}, err
	}
	return record, nil
}

func latestProviderUsage(records []task.ProviderUsageRecord) (task.ProviderUsageRecord, bool) {
	for idx := len(records) - 1; idx >= 0; idx-- {
		if strings.TrimSpace(records[idx].UsageRecordID) != "" {
			return records[idx], true
		}
	}
	return task.ProviderUsageRecord{}, false
}

func providerUsageRef(record task.ProviderUsageRecord) string {
	if strings.TrimSpace(record.UsageRecordID) == "" {
		return ""
	}
	return artifact.ProviderUsageRecordRef(record.UsageRecordID)
}

func observedUsage(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && value != "unknown"
}
