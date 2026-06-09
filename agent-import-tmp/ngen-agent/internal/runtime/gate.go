package ngenrt

import "ngen/internal/task"

func (s *Service) buildCompletion(spec task.Spec, criteria task.CriteriaSnapshot, review task.ReviewReport, handoffExists bool) task.CompletionReport {
	report := buildCompletion(spec, criteria, review, handoffExists)
	return s.applyMulticaCompletionGate(spec, report)
}

func buildCompletion(spec task.Spec, criteria task.CriteriaSnapshot, review task.ReviewReport, handoffExists bool) task.CompletionReport {
	report := task.CompletionReport{
		SchemaVersion:    task.SchemaVersion,
		TaskID:           spec.TaskID,
		CompletionID:     task.NewID("CMP"),
		CriterionResults: criteria.Criteria,
		HandoffRef:       "",
		EvaluatedAt:      task.Now(),
		Status:           "rejected",
		Summary:          "Done gate rejected.",
	}
	if handoffExists {
		report.HandoffRef = "handoff.md"
	}
	if review.Status == "clear" && handoffExists {
		allMet := true
		for _, item := range criteria.Criteria {
			if item.Status != "met" || len(item.EvidenceRefs) == 0 {
				allMet = false
				break
			}
		}
		if allMet {
			report.Status = "accepted"
			report.Summary = "Done gate passed."
		}
	}
	if review.Status != "clear" {
		report.BlockingRefs = append(report.BlockingRefs, review.BlockingFindingRefs...)
		report.Summary = review.Summary
	}
	return report
}
