package ngenrt

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"ngen/internal/artifact"
	"ngen/internal/review"
	"ngen/internal/task"
)

var qualityTestPathPattern = regexp.MustCompile(`[A-Za-z0-9_./-]+_test\.go`)

func (s *Service) captureQualityDiagnostic(spec task.Spec, changedPaths []string) (task.QualityDiagnostic, []task.Finding, error) {
	edits, err := s.Store.ReadWorkspaceEdits(spec.TaskID)
	if err != nil {
		return task.QualityDiagnostic{}, nil, err
	}
	now := task.Now()
	diag := task.QualityDiagnostic{
		ObjectKind:    "quality_diagnostic",
		SchemaVersion: task.SchemaVersion,
		DiagnosticID:  task.NewID("QDIAG"),
		TaskID:        spec.TaskID,
		Status:        "clear",
		ChangedPaths:  uniqueStrings(changedPaths),
		CreatedAt:     now,
	}
	pathEditCounts := make(map[string]int)
	failureCounts := make(map[string]int)
	var evidenceRefs []string
	for _, edit := range edits {
		diag.WorkspaceEditAttempts++
		evidenceRefs = append(evidenceRefs, artifact.WorkspaceEditRecordRef(edit.EditRecordID))
		if edit.Status == "failed" || edit.Status == "noop" {
			diag.NoopOrFailedEditCount++
			summary := strings.TrimSpace(edit.Summary)
			if summary != "" {
				failureCounts[summary]++
			}
			for _, path := range qualityTestPathsFromSummary(summary) {
				diag.TestFileChanges = append(diag.TestFileChanges, path)
			}
		}
		for _, change := range edit.FileChanges {
			path := filepath.ToSlash(strings.TrimSpace(change.Path))
			if path == "" {
				continue
			}
			diag.ChangedPaths = append(diag.ChangedPaths, path)
			pathEditCounts[path]++
			if strings.HasSuffix(path, "_test.go") {
				diag.TestFileChanges = append(diag.TestFileChanges, path)
			}
			if qualityLooksGenerated(path) {
				diag.GeneratedFileChanges = append(diag.GeneratedFileChanges, path)
			}
			if qualityLooksDependencyFile(path) {
				diag.NewDependencyWarning = true
			}
			if !change.BeforeExists && qualityLooksAbstractionPath(path) {
				diag.NewAbstractionWarning = true
			}
		}
	}
	diag.ChangedPaths = uniqueStrings(diag.ChangedPaths)
	diag.TestFileChanges = uniqueStrings(diag.TestFileChanges)
	diag.GeneratedFileChanges = uniqueStrings(diag.GeneratedFileChanges)
	diag.ScopeDriftPaths = s.reviewScopeDriftPaths(spec.TaskID, diag.ChangedPaths)
	diag.SprintBoundaryViolations = append([]string(nil), diag.ScopeDriftPaths...)
	diag.ChangedPathCount = len(diag.ChangedPaths)
	diag.SameFailureCount = maxStringCount(failureCounts)
	diag.SameFileRewriteCount = maxStringCount(pathEditCounts)
	diag.LargePatchWarning = diag.ChangedPathCount > 10
	diag.ReviewRequired = diag.LargePatchWarning || diag.NewDependencyWarning || diag.NewAbstractionWarning || diag.SameFileRewriteCount > 2 || len(diag.ScopeDriftPaths) > 0
	diag.EvidenceRefs = uniqueRefs(evidenceRefs)

	var findings []task.QualityFinding
	if len(diag.TestFileChanges) > 0 && qualityConstraintsForbidTestChanges(spec) {
		findings = append(findings, task.QualityFinding{
			Category:          review.CategoryConfirmedDefect,
			Severity:          "high",
			Blocking:          true,
			Summary:           fmt.Sprintf("Task constraints forbid test-file mutation, but test paths were targeted: %s.", strings.Join(diag.TestFileChanges, ", ")),
			AffectedPaths:     diag.TestFileChanges,
			EvidenceRefs:      diag.EvidenceRefs,
			RecommendedAction: "Revert the test-file change attempt or explicitly change the task contract before completion.",
		})
	}
	if len(diag.ScopeDriftPaths) > 0 {
		findings = append(findings, task.QualityFinding{
			Category:          review.CategoryScopeDrift,
			Severity:          "high",
			Blocking:          true,
			Summary:           fmt.Sprintf("Changed paths are outside current sprint working set: %s.", strings.Join(diag.ScopeDriftPaths, ", ")),
			AffectedPaths:     diag.ScopeDriftPaths,
			EvidenceRefs:      append([]string{"sprint/latest.json"}, diag.EvidenceRefs...),
			RecommendedAction: "Update the explicit scope or move these changes into a separate task before claiming Done.",
		})
	}
	if diag.SameFailureCount > 1 && diag.NoopOrFailedEditCount >= s.codingRepairBudget() {
		findings = append(findings, task.QualityFinding{
			Category:          review.CategoryComplexityRisk,
			Severity:          "high",
			Blocking:          true,
			Summary:           "Repair loop repeated the same failed/no-op edit pattern until the repair budget was exhausted.",
			EvidenceRefs:      diag.EvidenceRefs,
			RecommendedAction: "Stop automatic repair and inspect the repeated failure before resuming.",
		})
	}
	diag.Findings = findings
	diag.BlockCompletion = qualityHasBlockingFindings(findings)
	switch {
	case diag.BlockCompletion:
		diag.Status = "blocking"
		diag.RecommendedAction = firstQualityAction(findings, "Resolve blocking quality diagnostics before completion.")
	case diag.ReviewRequired:
		diag.Status = "warning"
		diag.RecommendedAction = "Reviewer should inspect non-empty quality warnings before completion."
	default:
		diag.Status = "clear"
		diag.RecommendedAction = "No quality diagnostics are currently blocking completion."
	}
	if err := s.Store.SaveQualityDiagnostic(diag); err != nil {
		return task.QualityDiagnostic{}, nil, err
	}
	return diag, qualityReviewFindings(spec.TaskID, findings), nil
}

func qualityReviewFindings(taskID string, findings []task.QualityFinding) []task.Finding {
	out := make([]task.Finding, 0, len(findings))
	for _, finding := range findings {
		out = append(out, task.Finding{
			SchemaVersion:     task.SchemaVersion,
			FindingID:         task.NewID("F"),
			TaskID:            taskID,
			TS:                task.Now(),
			Severity:          finding.Severity,
			Category:          finding.Category,
			Status:            "open",
			BlocksCompletion:  finding.Blocking,
			Claim:             finding.Summary,
			EvidenceRefs:      uniqueRefs(append([]string{"diagnostics/quality-latest.json"}, finding.EvidenceRefs...)),
			AffectedPaths:     uniqueStrings(finding.AffectedPaths),
			RecommendedAction: finding.RecommendedAction,
		})
	}
	return out
}

func qualityConstraintsForbidTestChanges(spec task.Spec) bool {
	for _, constraint := range spec.Constraints {
		normalized := strings.ToLower(strings.TrimSpace(constraint))
		if normalized == "" {
			continue
		}
		mentionsTests := strings.Contains(normalized, "_test.go") ||
			strings.Contains(normalized, "test.go") ||
			strings.Contains(normalized, "test file") ||
			strings.Contains(normalized, "tests")
		forbidsMutation := strings.Contains(normalized, "do not modify") ||
			strings.Contains(normalized, "don't modify") ||
			strings.Contains(normalized, "must not modify") ||
			strings.Contains(normalized, "without modifying") ||
			strings.Contains(normalized, "keep") ||
			strings.Contains(normalized, "leave")
		if mentionsTests && forbidsMutation {
			return true
		}
	}
	return false
}

func qualityTestPathsFromSummary(summary string) []string {
	matches := qualityTestPathPattern.FindAllString(summary, -1)
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		out = append(out, filepath.ToSlash(strings.TrimSpace(match)))
	}
	return uniqueStrings(out)
}

func qualityLooksGenerated(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(path))
	return strings.Contains(lower, "generated") ||
		strings.Contains(lower, "/gen/") ||
		strings.HasSuffix(lower, ".pb.go") ||
		strings.HasSuffix(lower, "_generated.go")
}

func qualityLooksDependencyFile(path string) bool {
	switch strings.ToLower(filepath.ToSlash(path)) {
	case "go.mod", "go.sum", "package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "cargo.toml", "cargo.lock":
		return true
	default:
		return false
	}
}

func qualityLooksAbstractionPath(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(path))
	return strings.Contains(lower, "/interface") ||
		strings.Contains(lower, "/adapter") ||
		strings.Contains(lower, "/provider") ||
		strings.Contains(lower, "/factory")
}

func qualityHasBlockingFindings(findings []task.QualityFinding) bool {
	for _, finding := range findings {
		if finding.Blocking {
			return true
		}
	}
	return false
}

func firstQualityAction(findings []task.QualityFinding, fallback string) string {
	for _, finding := range findings {
		if strings.TrimSpace(finding.RecommendedAction) != "" {
			return strings.TrimSpace(finding.RecommendedAction)
		}
	}
	return fallback
}

func maxStringCount(counts map[string]int) int {
	max := 0
	for _, count := range counts {
		if count > max {
			max = count
		}
	}
	return max
}

func (s *Service) renderQualityDiagnosticsMarkdown(b *strings.Builder, taskID string) bool {
	diag, err := s.Store.LoadQualityDiagnostic(taskID)
	if err != nil {
		return false
	}
	if diag.Status == "clear" && len(diag.Findings) == 0 && !diag.ReviewRequired {
		return false
	}
	fmt.Fprintf(b, "\n## Quality Diagnostics\n")
	fmt.Fprintf(b, "- Status: %s\n", diag.Status)
	if diag.RecommendedAction != "" {
		fmt.Fprintf(b, "- Recommended Action: %s\n", diag.RecommendedAction)
	}
	if len(diag.ChangedPaths) > 0 {
		fmt.Fprintf(b, "- Changed Paths: %s\n", strings.Join(diag.ChangedPaths, ", "))
	}
	if len(diag.TestFileChanges) > 0 {
		fmt.Fprintf(b, "- Test File Changes: %s\n", strings.Join(diag.TestFileChanges, ", "))
	}
	if len(diag.ScopeDriftPaths) > 0 {
		fmt.Fprintf(b, "- Scope Drift Paths: %s\n", strings.Join(diag.ScopeDriftPaths, ", "))
	}
	for _, finding := range diag.Findings {
		fmt.Fprintf(b, "- %s: %s\n", finding.Category, finding.Summary)
	}
	return true
}
