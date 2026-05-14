package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateMarkdownArtifactAcceptsCanonicalFindingRecords(t *testing.T) {
	content := `# audit

## findings
1. Severity: medium
   Confidence: high
   Evidence: internal/app/app.go:10-20
   Why it matters: default surface is too wide

2. Severity: low
   Confidence: medium
   Evidence: internal/runtime/prompt.go:30-40
   Why it matters: prompt wording drift

## unresolved questions
- Should experimental aliases remain?`

	result := ValidateMarkdownArtifact(content)
	if !result.Valid {
		t.Fatalf("expected valid artifact, got %#v", result)
	}
	if result.FindingCount != 2 {
		t.Fatalf("expected two finding records, got %#v", result)
	}
}

func TestValidateMarkdownArtifactAllowsExplicitNoFindings(t *testing.T) {
	content := `# audit

## findings
No validated findings.

## unresolved questions
None.`

	result := ValidateMarkdownArtifact(content)
	if !result.Valid {
		t.Fatalf("expected no-findings artifact to be valid, got %#v", result)
	}
	if !result.NoFindings {
		t.Fatalf("expected no-findings marker to be recognized, got %#v", result)
	}
}

func TestValidateMarkdownArtifactAllowsConfirmedNoDriftSummary(t *testing.T) {
	content := `# audit

## findings
- No confirmed drift was proven from the reviewed evidence slices.

## unresolved questions
- None`

	result := ValidateMarkdownArtifact(content)
	if !result.Valid {
		t.Fatalf("expected confirmed-no-drift artifact to be valid, got %#v", result)
	}
	if !result.NoFindings {
		t.Fatalf("expected confirmed-no-drift marker to be recognized, got %#v", result)
	}
}

func TestValidateMarkdownArtifactAllowsNoValidatedBreaksSummary(t *testing.T) {
	content := `# audit

## findings
No validated core-v1 surface-discipline breaks found in the inspected owning code.

## remaining risks
- None`

	result := ValidateMarkdownArtifact(content)
	if !result.Valid {
		t.Fatalf("expected no-breaks artifact to be valid, got %#v", result)
	}
	if !result.NoFindings {
		t.Fatalf("expected no-breaks marker to be recognized, got %#v", result)
	}
}

func TestValidateMarkdownArtifactAllowsNoLiveValidatedBlockerSummary(t *testing.T) {
	content := `# audit

## findings
No new live-validated go-cli-agent product blocker was proven in the allowlisted evidence.

## unresolved questions
- None`

	result := ValidateMarkdownArtifact(content)
	if !result.Valid {
		t.Fatalf("expected no-blocker artifact to be valid, got %#v", result)
	}
	if !result.NoFindings {
		t.Fatalf("expected no-blocker marker to be recognized, got %#v", result)
	}
}

func TestValidateMarkdownArtifactAcceptsSingleLevelSections(t *testing.T) {
	content := `# audit

# findings
### Finding 1
Severity: medium
Confidence: high
Evidence: internal/app/app.go:10-20
Why it matters: default surface is too wide

# unresolved questions
- None`

	result := ValidateMarkdownArtifact(content)
	if !result.Valid {
		t.Fatalf("expected valid artifact with single-level sections, got %#v", result)
	}
	if result.FindingCount != 1 {
		t.Fatalf("expected one finding record, got %#v", result)
	}
}

func TestValidateMarkdownArtifactAcceptsRemainingRisksSection(t *testing.T) {
	content := `# audit

## findings
### Finding 1
Severity: medium
Confidence: high
Evidence: internal/app/app.go:10-20
Why it matters: default surface is too wide

## remaining risks
- A deeper owning-gate read is still pending.`

	result := ValidateMarkdownArtifact(content)
	if !result.Valid {
		t.Fatalf("expected remaining-risks artifact to be valid, got %#v", result)
	}
	if result.FindingCount != 1 {
		t.Fatalf("expected one finding record, got %#v", result)
	}
}

func TestValidateMarkdownArtifactAcceptsLevelTwoFindingHeadings(t *testing.T) {
	content := `# audit

# findings
## Finding 1
- Severity: medium
- Confidence: high
- Evidence: internal/app/app.go:10-20
- Why it matters: default surface is too wide

# unresolved questions
- None`

	result := ValidateMarkdownArtifact(content)
	if !result.Valid {
		t.Fatalf("expected valid artifact with level-two finding headings, got %#v", result)
	}
	if result.FindingCount != 1 {
		t.Fatalf("expected one finding record, got %#v", result)
	}
}

func TestValidateMarkdownArtifactIgnoresNonFindingFieldLabelsOutsideFindingsSection(t *testing.T) {
	content := `# audit

## findings
### Finding 1
Severity: medium
Confidence: high
Evidence: internal/app/app.go:10-20
Why it matters: default surface is too wide

## confirmed alignments
Confidence: high
Evidence: internal/runtime/engine.go:1-10
Why it matters: adapter boundary is thin

## unresolved questions
- Should the interface split remain?`

	result := ValidateMarkdownArtifact(content)
	if !result.Valid {
		t.Fatalf("expected valid artifact with extra non-finding sections, got %#v", result)
	}
	if result.FindingCount != 1 {
		t.Fatalf("expected one finding record, got %#v", result)
	}
}

func TestValidateMarkdownArtifactRejectsMissingUnresolvedSection(t *testing.T) {
	content := `# audit

## findings
1. Severity: medium
   Confidence: high
   Evidence: internal/app/app.go:10-20
   Why it matters: default surface is too wide`

	result := ValidateMarkdownArtifact(content)
	if result.Valid {
		t.Fatalf("expected invalid artifact, got %#v", result)
	}
	if !strings.Contains(strings.Join(result.Issues, "\n"), "missing unresolved or remaining-risks section") {
		t.Fatalf("expected unresolved-section error, got %#v", result.Issues)
	}
}

func TestValidateMarkdownArtifactRejectsMismatchedFindingFields(t *testing.T) {
	content := `# audit

## findings
1. Severity: medium
   Confidence: high
   Evidence: internal/app/app.go:10-20

## unresolved questions
- none`

	result := ValidateMarkdownArtifact(content)
	if result.Valid {
		t.Fatalf("expected invalid artifact, got %#v", result)
	}
	if !strings.Contains(strings.Join(result.Issues, "\n"), "why it matters") {
		t.Fatalf("expected why-it-matters error, got %#v", result.Issues)
	}
}

func TestValidateMarkdownArtifactRequiresConcreteEvidencePathLine(t *testing.T) {
	content := `# audit

## findings
### Finding 1
Severity: medium
Confidence: high
Evidence: runtime behavior looked wrong in testing
Why it matters: this is too vague to validate

## unresolved questions
- none`

	result := ValidateMarkdownArtifact(content)
	if result.Valid {
		t.Fatalf("expected invalid artifact, got %#v", result)
	}
	if !strings.Contains(strings.Join(result.Issues, "\n"), "path:line") {
		t.Fatalf("expected evidence path:line error, got %#v", result.Issues)
	}
}

func TestValidateMarkdownArtifactRequiresMeaningfulUnresolvedContent(t *testing.T) {
	content := `# audit

## findings
### Finding 1
Severity: medium
Confidence: high
Evidence: internal/app/app.go:10-20
Impact: default surface is too wide

## unresolved questions`

	result := ValidateMarkdownArtifact(content)
	if result.Valid {
		t.Fatalf("expected invalid artifact, got %#v", result)
	}
	if !strings.Contains(strings.Join(result.Issues, "\n"), "unresolved or remaining-risks section must contain") {
		t.Fatalf("expected unresolved-content error, got %#v", result.Issues)
	}
}

func TestValidateMarkdownArtifactAcceptsImpactAliasAndDashSeparator(t *testing.T) {
	content := `# audit

## findings
### Finding 1
Severity - medium
Confidence - high
Evidence - internal/app/app.go:10-20
Impact - default surface is too wide

## unresolved questions
- None`

	result := ValidateMarkdownArtifact(content)
	if !result.Valid {
		t.Fatalf("expected valid artifact, got %#v", result)
	}
	if result.FindingCount != 1 {
		t.Fatalf("expected one finding record, got %#v", result)
	}
}

func TestValidateMarkdownArtifactAcceptsMultilineEvidenceAndWhySections(t *testing.T) {
	content := `# audit

## findings
### Finding 1
Severity:
- medium
Confidence:
- high
Evidence:
- internal/app/app.go:10-20
- internal/runtime/prompt.go:30-40
Why it matters:
- default surface is too wide

## unresolved questions
- None`

	result := ValidateMarkdownArtifact(content)
	if !result.Valid {
		t.Fatalf("expected valid multiline-field artifact, got %#v", result)
	}
	if result.FindingCount != 1 {
		t.Fatalf("expected one finding record, got %#v", result)
	}
}

func TestValidateMarkdownArtifactAcceptsRepeatedEvidenceLinesWithinFinding(t *testing.T) {
	content := `# audit

## findings
### Finding 1: Explicit routing still exists
Severity: medium
Confidence: high
Evidence: internal/app/app.go:120-123 ('case "delegate", "children", "queue", "tui": return true')
Evidence: internal/app/app.go:153-177 ('experimentalRunnerLoader = loadExperimentalRunner')
Why it matters: explicit routing and separate loader seams are still operator-visible design choices.

## unresolved questions
- None`

	result := ValidateMarkdownArtifact(content)
	if !result.Valid {
		t.Fatalf("expected repeated-evidence artifact to be valid, got %#v", result)
	}
	if result.FindingCount != 1 {
		t.Fatalf("expected one finding record, got %#v", result)
	}
	if result.EvidenceCount != 1 {
		t.Fatalf("expected one merged evidence field, got %#v", result)
	}
}

func TestValidateMarkdownArtifactWithWorkspaceAcceptsMatchingEvidenceSnippet(t *testing.T) {
	workdir := t.TempDir()
	path := filepath.Join(workdir, "internal", "app", "app.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "package app\n\nfunc exposeDefaultSurface() {}\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	report := `# audit

## findings
### Finding 1
Severity: medium
Confidence: high
Evidence: internal/app/app.go:3
Snippet: "exposeDefaultSurface"
Why it matters: default surface is too wide

## unresolved questions
- None`

	result := ValidateMarkdownArtifactWithWorkspace(workdir, report)
	if !result.Valid {
		t.Fatalf("expected valid workspace-backed artifact, got %#v", result)
	}
	if result.VerifiedEvidenceCount != 1 {
		t.Fatalf("expected one verified evidence record, got %#v", result)
	}
}

func TestValidateMarkdownArtifactWithWorkspaceRejectsSymlinkEscapedEvidence(t *testing.T) {
	workdir := t.TempDir()
	linkPath := filepath.Join(workdir, "internal", "app", "app.go")
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("package outside\n\nfunc externalProof() {}\n"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Fatalf("symlink evidence path: %v", err)
	}
	report := `# audit

## findings
### Finding 1
Severity: medium
Confidence: high
Evidence: internal/app/app.go:3
Snippet: "externalProof"
Why it matters: symlink-escaped evidence should not validate a workspace finding

## unresolved questions
- None`

	result := ValidateMarkdownArtifactWithWorkspace(workdir, report)
	if result.Valid {
		t.Fatalf("expected invalid artifact for symlink-escaped evidence, got %#v", result)
	}
	if !strings.Contains(strings.Join(result.Issues, "\n"), "readable in-workspace files") {
		t.Fatalf("expected readable-path error, got %#v", result.Issues)
	}
}

func TestValidateMarkdownArtifactWithWorkspaceAcceptsSamePathMultiWindowShorthand(t *testing.T) {
	workdir := t.TempDir()
	path := filepath.Join(workdir, "internal", "app", "app.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := strings.Join([]string{
		"package app",
		"",
		"func coreOnlySurface() {}",
		"",
		"func runtimeNote() {}",
		"",
		"func usageSurface() {}",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	report := `# audit

## findings
### Finding 1
Severity: medium
Confidence: high
Evidence: internal/app/app.go:3,7
Snippet: "usageSurface"
Why it matters: shorthand path reuse should still verify later cited windows

## unresolved questions
- None`

	result := ValidateMarkdownArtifactWithWorkspace(workdir, report)
	if !result.Valid {
		t.Fatalf("expected valid shorthand multi-window artifact, got %#v", result)
	}
	if result.VerifiedEvidenceCount != 1 {
		t.Fatalf("expected one verified evidence record, got %#v", result)
	}
}

func TestValidateMarkdownArtifactWithWorkspaceRejectsMissingEvidenceSnippet(t *testing.T) {
	workdir := t.TempDir()
	path := filepath.Join(workdir, "internal", "app", "app.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("package app\n\nfunc exposeDefaultSurface() {}\n"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	report := `# audit

## findings
### Finding 1
Severity: medium
Confidence: high
Evidence: internal/app/app.go:3
Why it matters: default surface is too wide

## unresolved questions
- None`

	result := ValidateMarkdownArtifactWithWorkspace(workdir, report)
	if result.Valid {
		t.Fatalf("expected invalid artifact without snippet-backed evidence, got %#v", result)
	}
	if !strings.Contains(strings.Join(result.Issues, "\n"), "quoted snippet or identifier") {
		t.Fatalf("expected snippet guidance error, got %#v", result.Issues)
	}
}

func TestValidateMarkdownArtifactWithWorkspaceMismatchIssueSuggestsCorrectingLineRange(t *testing.T) {
	workdir := t.TempDir()
	path := filepath.Join(workdir, "docs", "sandbox.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := strings.Join([]string{
		"## Sandbox & approvals",
		"",
		"For information about Codex sandboxing and approvals, see the security guide.",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	report := `# audit

## findings
### Finding 1
Severity: medium
Confidence: high
Evidence: docs/sandbox.md:1
Snippet: "For information about Codex sandboxing and approvals"
Why it matters: line ranges should point at the quoted text itself

## unresolved questions
- None`

	result := ValidateMarkdownArtifactWithWorkspace(workdir, report)
	if result.Valid {
		t.Fatalf("expected invalid artifact for mismatched evidence range, got %#v", result)
	}
	if !strings.Contains(strings.Join(result.Issues, "\n"), "correct the cited line numbers or widen the cited line range") {
		t.Fatalf("expected line-range correction guidance, got %#v", result.Issues)
	}
}

func TestValidateMarkdownArtifactWithWorkspaceRejectsUnreadableEvidencePath(t *testing.T) {
	workdir := t.TempDir()
	report := `# audit

## findings
### Finding 1
Severity: medium
Confidence: high
Evidence: internal/app/app.go:3 ("exposeDefaultSurface")
Why it matters: default surface is too wide

## unresolved questions
- None`

	result := ValidateMarkdownArtifactWithWorkspace(workdir, report)
	if result.Valid {
		t.Fatalf("expected invalid artifact for unreadable evidence path, got %#v", result)
	}
	if !strings.Contains(strings.Join(result.Issues, "\n"), "readable in-workspace files") {
		t.Fatalf("expected readable-path error, got %#v", result.Issues)
	}
	if !strings.Contains(strings.Join(result.Issues, "\n"), "omitted-path or ellipsis shorthand") {
		t.Fatalf("expected explicit-path guidance, got %#v", result.Issues)
	}
}
