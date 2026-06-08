package review

import (
	"fmt"
	"strings"

	"ngen/internal/task"
)

const (
	CategoryConfirmedDefect = "confirmed_defect"
	CategoryMissingEvidence = "missing_evidence"
	CategoryScopeDrift      = "scope_drift"
	CategoryComplexityRisk  = "complexity_risk"
	CategorySecurityRisk    = "security_risk"
	CategoryStaleContext    = "stale_context_risk"
	CategoryWorkerTrustGap  = "worker_trust_gap"
	CategoryInferredRisk    = "inferred_risk"
	CategoryNotObserved     = "not_observed"
)

type Input struct {
	Spec            task.Spec
	Verification    task.VerificationReport
	HandoffExists   bool
	HandoffStale    bool
	Criteria        task.CriteriaSnapshot
	ContextRefs     []string
	ChangedPaths    []string
	ScopeDriftPaths []string
	WorkerEvidence  []WorkerEvidence
	QualityFindings []task.Finding
}

type WorkerEvidence struct {
	WorkerID                   string
	Role                       string
	ContractRef                string
	ResultRef                  string
	SettlementRef              string
	ReconcileRef               string
	ChildState                 task.StateName
	SettlementStatus           string
	CompletionStatus           string
	ReviewStatus               string
	VerificationStatus         string
	ReconcileStatus            string
	RequiresParentAction       bool
	ParentActionType           string
	EvidenceScore              int
	EvidenceGrade              string
	TrustedForParentCompletion bool
	UsedByCriteria             bool
}

func Evaluate(spec task.Spec, verification task.VerificationReport, handoffExists bool, criteria task.CriteriaSnapshot) (task.ReviewReport, *task.Finding) {
	report, findings := EvaluateWithContext(Input{
		Spec:          spec,
		Verification:  verification,
		HandoffExists: handoffExists,
		Criteria:      criteria,
	})
	if len(findings) == 0 {
		return report, nil
	}
	return report, &findings[0]
}

func EvaluateWithContext(input Input) (task.ReviewReport, []task.Finding) {
	report := task.ReviewReport{
		SchemaVersion:     task.SchemaVersion,
		TaskID:            input.Spec.TaskID,
		ReviewID:          task.NewID("REV"),
		Status:            "clear",
		Summary:           "review cleared from artifact-backed verification, criteria, handoff, and worker evidence.",
		ReviewerProfile:   reviewerProfile(input.Spec),
		ReviewContextRefs: uniqueStrings(input.ContextRefs),
		ChangedPaths:      uniqueStrings(input.ChangedPaths),
		WorkerResultRefs:  workerResultRefs(input.WorkerEvidence),
		ReviewedAt:        task.Now(),
	}

	var findings []task.Finding
	if input.Verification.Status != "passed" {
		findings = append(findings, newFinding(input.Spec.TaskID, CategoryMissingEvidence, "high", true, "Completion was evaluated before verification passed.", []string{"verification/latest.json"}, nil, "Run the verifier until it passes."))
	}
	if !input.HandoffExists {
		findings = append(findings, newFinding(input.Spec.TaskID, CategoryMissingEvidence, "high", true, "Completion was evaluated without a handoff artifact.", nil, nil, "Write handoff.md before claiming done."))
	}
	if input.HandoffStale {
		findings = append(findings, newFinding(input.Spec.TaskID, CategoryStaleContext, "high", true, "Handoff artifact is older than the review context it claims to summarize.", []string{"handoff.md"}, nil, "Regenerate handoff.md from current verification, criteria, review, and completion artifacts."))
	}
	for _, item := range input.Criteria.Criteria {
		if item.Status != "met" || len(item.EvidenceRefs) == 0 {
			findings = append(findings, newFinding(input.Spec.TaskID, CategoryMissingEvidence, "high", true, "At least one criterion is still open or lacks evidence refs.", []string{"criteria/latest.json"}, nil, "Update criteria evidence before claiming done."))
			break
		}
	}
	if len(input.ScopeDriftPaths) > 0 {
		findings = append(findings, newFinding(input.Spec.TaskID, CategoryScopeDrift, "high", true, fmt.Sprintf("Changed paths are outside the active sprint or project focus: %s.", strings.Join(uniqueStrings(input.ScopeDriftPaths), ", ")), reviewScopeRefs(input.ContextRefs), uniqueStrings(input.ScopeDriftPaths), "Move the change into the active scope, update the explicit plan/project boundary, or split it into a follow-up task."))
	}
	findings = append(findings, input.QualityFindings...)
	for _, worker := range input.WorkerEvidence {
		findings = append(findings, workerFindings(input.Spec.TaskID, worker)...)
	}

	report.RiskSummary = riskSummary(findings)
	report.BlockingCategories = blockingCategories(findings)
	report.BlockingFindingRefs = findingRefs(findings)
	if len(findings) > 0 {
		report.Status = "blocking"
		report.Summary = fmt.Sprintf("review blocked by %d artifact-backed finding(s).", len(findings))
	}
	return report, findings
}

func workerFindings(taskID string, worker WorkerEvidence) []task.Finding {
	if !worker.UsedByCriteria {
		return nil
	}
	refs := uniqueStrings([]string{worker.ContractRef, worker.ResultRef, worker.SettlementRef, worker.ReconcileRef})
	missing := missingWorkerEvidence(worker)
	if len(missing) > 0 {
		return []task.Finding{newFinding(taskID, CategoryWorkerTrustGap, "high", true, fmt.Sprintf("Worker %s is cited by criteria but lacks required runtime evidence: %s.", worker.WorkerID, strings.Join(missing, ", ")), refs, nil, "Sync or continue the worker until result, settlement, and reconcile artifacts exist before trusting it for parent completion.")}
	}
	var findings []task.Finding
	if worker.ChildState != "" && worker.ChildState != task.StateDone {
		findings = append(findings, newFinding(taskID, CategoryWorkerTrustGap, "high", true, fmt.Sprintf("Worker %s child state is %s, not Done.", worker.WorkerID, worker.ChildState), refs, nil, "Continue, inspect, or replace the worker before using its result for parent completion."))
	}
	if worker.SettlementStatus != "" && worker.SettlementStatus != "accepted" {
		findings = append(findings, newFinding(taskID, CategoryWorkerTrustGap, "high", true, fmt.Sprintf("Worker %s settlement status is %s.", worker.WorkerID, worker.SettlementStatus), refs, nil, "Resolve worker settlement before closing parent criteria."))
	}
	if worker.ReconcileStatus == "conflict" || worker.ReconcileStatus == "failed" {
		findings = append(findings, newFinding(taskID, CategoryWorkerTrustGap, "high", true, fmt.Sprintf("Worker %s reconcile status is %s.", worker.WorkerID, worker.ReconcileStatus), refs, nil, "Resolve reconcile conflict or failure before trusting child output."))
	}
	if worker.RequiresParentAction {
		findings = append(findings, newFinding(taskID, CategoryWorkerTrustGap, "high", true, fmt.Sprintf("Worker %s still requires parent action: %s.", worker.WorkerID, worker.ParentActionType), refs, nil, "Complete the parent-owned worker action before closing the parent task."))
	}
	if worker.VerificationStatus != "" && worker.VerificationStatus != "passed" {
		findings = append(findings, newFinding(taskID, CategoryWorkerTrustGap, "high", true, fmt.Sprintf("Worker %s verification status is %s.", worker.WorkerID, worker.VerificationStatus), refs, nil, "Require passing child verification before trusting worker output."))
	}
	if worker.ReviewStatus != "" && worker.ReviewStatus != "clear" {
		findings = append(findings, newFinding(taskID, CategoryWorkerTrustGap, "high", true, fmt.Sprintf("Worker %s review status is %s.", worker.WorkerID, worker.ReviewStatus), refs, nil, "Require clear child review before trusting worker output."))
	}
	if worker.CompletionStatus != "" && worker.CompletionStatus != "accepted" {
		findings = append(findings, newFinding(taskID, CategoryWorkerTrustGap, "high", true, fmt.Sprintf("Worker %s completion status is %s.", worker.WorkerID, worker.CompletionStatus), refs, nil, "Require accepted child completion before trusting worker output."))
	}
	if len(findings) == 0 && worker.EvidenceGrade != "" && !worker.TrustedForParentCompletion {
		findings = append(findings, newFinding(taskID, CategoryWorkerTrustGap, "high", true, fmt.Sprintf("Worker %s evidence grade is %s with score %d; it is not trusted for parent completion.", worker.WorkerID, worker.EvidenceGrade, worker.EvidenceScore), refs, nil, "Require complete worker evidence before closing worker-backed parent criteria."))
	}
	return findings
}

func missingWorkerEvidence(worker WorkerEvidence) []string {
	var missing []string
	if strings.TrimSpace(worker.ResultRef) == "" {
		missing = append(missing, "result")
	}
	if strings.TrimSpace(worker.SettlementRef) == "" {
		missing = append(missing, "settlement")
	}
	if strings.TrimSpace(worker.ReconcileRef) == "" {
		missing = append(missing, "reconcile")
	}
	return missing
}

func newFinding(taskID, category, severity string, blocking bool, claim string, refs []string, paths []string, action string) task.Finding {
	return task.Finding{
		SchemaVersion:     task.SchemaVersion,
		FindingID:         task.NewID("F"),
		TaskID:            taskID,
		TS:                task.Now(),
		Severity:          severity,
		Category:          category,
		Status:            "open",
		BlocksCompletion:  blocking,
		Claim:             claim,
		EvidenceRefs:      uniqueStrings(refs),
		AffectedPaths:     uniqueStrings(paths),
		RecommendedAction: action,
	}
}

func reviewerProfile(spec task.Spec) string {
	switch spec.Kind {
	case task.KindReviewer:
		return "reviewer"
	case task.KindSecurityReview:
		return "security_review"
	case task.KindCoding:
		return "coding_reviewer"
	case task.KindGeneral:
		return "general_execution_reviewer"
	default:
		if strings.TrimSpace(string(spec.Kind)) == "" {
			return "reviewer"
		}
		return string(spec.Kind) + "_reviewer"
	}
}

func riskSummary(findings []task.Finding) task.ReviewRiskSummary {
	var summary task.ReviewRiskSummary
	for _, finding := range findings {
		if finding.BlocksCompletion {
			summary.BlockingCount++
		}
		switch finding.Category {
		case CategoryConfirmedDefect:
			summary.ConfirmedDefects++
		case CategoryMissingEvidence:
			summary.MissingEvidence++
		case CategoryScopeDrift:
			summary.ScopeDriftRisks++
		case CategoryComplexityRisk:
			summary.ComplexityRisks++
		case CategorySecurityRisk:
			summary.SecurityRisks++
		case CategoryStaleContext:
			summary.StaleContextRisks++
		case CategoryWorkerTrustGap:
			summary.WorkerTrustGaps++
		case CategoryInferredRisk:
			summary.InferredRiskFindings++
		case CategoryNotObserved:
			summary.NotObservedFindings++
		}
	}
	return summary
}

func blockingCategories(findings []task.Finding) []string {
	var categories []string
	for _, finding := range findings {
		if finding.BlocksCompletion {
			categories = append(categories, finding.Category)
		}
	}
	return uniqueStrings(categories)
}

func findingRefs(findings []task.Finding) []string {
	refs := make([]string, 0, len(findings))
	for _, finding := range findings {
		if !finding.BlocksCompletion {
			continue
		}
		refs = append(refs, "findings.jsonl#finding_id="+finding.FindingID)
	}
	return refs
}

func workerResultRefs(workers []WorkerEvidence) []string {
	var refs []string
	for _, worker := range workers {
		refs = append(refs, worker.ResultRef)
	}
	return uniqueStrings(refs)
}

func reviewScopeRefs(refs []string) []string {
	var out []string
	for _, ref := range refs {
		switch {
		case strings.Contains(ref, "sprint/latest.json"):
			out = append(out, ref)
		case strings.Contains(ref, "project/project.json"):
			out = append(out, ref)
		}
	}
	if len(out) == 0 {
		out = []string{"sprint/latest.json", "project/project.json"}
	}
	return uniqueStrings(out)
}

func uniqueStrings(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}
