package review

import (
	"testing"

	"ngen/internal/task"
)

func TestEvaluateWithContextClassifiesArtifactBackedRisks(t *testing.T) {
	spec := task.Spec{
		TaskID: "TASK-REVIEW",
		Kind:   task.KindCoding,
	}
	verification := task.VerificationReport{
		TaskID: spec.TaskID,
		Status: "passed",
	}
	criteria := task.CriteriaSnapshot{
		TaskID: spec.TaskID,
		Criteria: []task.CriterionStatus{
			{
				CriterionID:  "SC-001",
				Status:       "met",
				Passes:       true,
				EvidenceRefs: []string{"workers/WRK-001.json"},
			},
		},
	}

	report, findings := EvaluateWithContext(Input{
		Spec:            spec,
		Verification:    verification,
		HandoffExists:   true,
		HandoffStale:    true,
		Criteria:        criteria,
		ContextRefs:     []string{"sprint/latest.json", "workspace:.ngen/project/project.json"},
		ChangedPaths:    []string{"README.md", "cmd/demo/main.go"},
		ScopeDriftPaths: []string{"cmd/demo/main.go"},
		WorkerEvidence: []WorkerEvidence{
			{
				WorkerID:       "WRK-001",
				Role:           string(task.KindReviewer),
				ContractRef:    "workers/WRK-001.json",
				UsedByCriteria: true,
			},
		},
	})

	if report.Status != "blocking" {
		t.Fatalf("expected blocking review, got %+v", report)
	}
	if len(findings) != 3 {
		t.Fatalf("expected stale-context, scope-drift, and worker-trust findings, got %+v", findings)
	}
	for _, category := range []string{CategoryStaleContext, CategoryScopeDrift, CategoryWorkerTrustGap} {
		if !contains(report.BlockingCategories, category) {
			t.Fatalf("expected blocking category %s in %+v", category, report.BlockingCategories)
		}
	}
	if report.RiskSummary.BlockingCount != 3 || report.RiskSummary.StaleContextRisks != 1 || report.RiskSummary.ScopeDriftRisks != 1 || report.RiskSummary.WorkerTrustGaps != 1 {
		t.Fatalf("unexpected risk summary: %+v", report.RiskSummary)
	}
	if !contains(findings[1].AffectedPaths, "cmd/demo/main.go") {
		t.Fatalf("expected scope drift affected path, got %+v", findings[1])
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
