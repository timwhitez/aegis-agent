package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aegis-agent/internal/review"
	"aegis-agent/internal/session"
	"aegis-agent/internal/tools"
)

func TestReviewIssueDetailAddsSnippetMismatchCorrectionHint(t *testing.T) {
	detail := reviewIssueDetail(review.ValidationResult{
		Issues: []string{
			"finding 1 evidence snippets must match the cited lines; correct the cited line numbers or widen the cited line range so the quoted text or identifier appears within those exact lines",
		},
	})
	if !strings.Contains(detail, "Fix snippet-backed evidence by citing the line(s) that literally contain the quoted text or identifier") {
		t.Fatalf("expected snippet mismatch correction hint, got %q", detail)
	}
}

func TestReviewIssueDetailAddsReadablePathHint(t *testing.T) {
	detail := reviewIssueDetail(review.ValidationResult{
		Issues: []string{
			"finding 1 evidence must resolve to readable in-workspace files and line ranges; use explicit workspace-relative repo paths like internal/app/app.go:42-44 instead of omitted-path or ellipsis shorthand",
		},
	})
	if !strings.Contains(detail, "Use explicit workspace-relative repo paths like `internal/app/app.go:42-44`") {
		t.Fatalf("expected readable-path correction hint, got %q", detail)
	}
}

func TestToolGuardBlocksInvalidReviewArtifactWriteOnReviewLikeScratchPath(t *testing.T) {
	workdir := t.TempDir()
	kind, text := toolGuard(workdir, []session.Message{
		session.NewMessage("user", "Audit the repo and write a review artifact with findings and remaining risks."),
	}, "write_file", json.RawMessage(`{"path":"scratch/review-notes.txt","content":"# Findings\n\n## Finding 1\nEvidence only\n\n## Remaining risks\n- still open\n"}`))
	if kind != "review_artifact" {
		t.Fatalf("expected review_artifact guard, got %q", kind)
	}
	if !strings.Contains(text, "Severity/Confidence/Evidence/Why it matters") {
		t.Fatalf("expected structured review guidance, got %q", text)
	}
}

func TestToolGuardDoesNotTreatGenericValidationReportAsReviewArtifact(t *testing.T) {
	workdir := t.TempDir()
	kind, text := toolGuard(workdir, []session.Message{
		session.NewMessage("user", "Validate skill execution and write reports/skill-proof.md with the observed tool outputs."),
	}, "write_file", json.RawMessage(`{"path":"reports/skill-proof.md","content":"# Skill proof\n\n- load_skill ran\n- markdown_inventory ran\n- pretty_json_args ran\n"}`))
	if kind != "" || text != "" {
		t.Fatalf("expected generic validation report to bypass review_artifact guard, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardDoesNotTreatChangeSummaryAsReviewArtifact(t *testing.T) {
	workdir := t.TempDir()
	kind, text := toolGuard(workdir, []session.Message{
		session.NewMessage("user", "Implement the fix, rerun the narrowest tests, write reports/change-summary.md with sections: findings, files changed, verification, remaining risks, and then finish."),
	}, "write_file", json.RawMessage(`{"path":"reports/change-summary.md","content":"# Change Summary\n\n## Findings\n- Fixed the default quota handling mismatch.\n\n## Files Changed\n- internal/config/config.go\n\n## Verification\n- go test ./internal/config\n\n## Remaining Risks\n- Wider integration coverage still depends on full matrix reruns.\n"}`))
	if kind != "" || text != "" {
		t.Fatalf("expected change summary to bypass review_artifact guard, got kind=%q text=%q", kind, text)
	}
}

func TestRequiresReviewArtifactIgnoresChangeSummaryOnlyPrompt(t *testing.T) {
	text := "Implement the fix, rerun the narrowest tests, write reports/change-summary.md with sections: findings, files changed, verification, remaining risks, and then finish."
	if requiresReviewArtifact(text) {
		t.Fatalf("expected change-summary-only repair prompt not to require review artifact")
	}
}

func TestRequiresReviewArtifactIgnoresChangeSummaryPlusProjectMemoryPrompt(t *testing.T) {
	text := "Implement the planned multi-package fixes, refresh reports/progress.md and reports/validation.md, write reports/change-summary.md with sections: findings, files changed, verification, remaining risks, and only call finish after go test ./... passes."
	if requiresReviewArtifact(text) {
		t.Fatalf("expected change-summary plus project-memory repair prompt not to require review artifact")
	}
}

func TestRequiresReviewArtifactNeedsExplicitArtifactRequest(t *testing.T) {
	if requiresReviewArtifact("Review the runtime and return findings with severity and evidence.") {
		t.Fatal("ordinary inline review must not infer an artifact")
	}
	if !requiresReviewArtifact("Review the runtime and write a report with severity and evidence.") {
		t.Fatal("explicit report request must activate the review artifact contract")
	}
}

func TestToolGuardAllowsFinishAfterChangeSummaryWrite(t *testing.T) {
	workdir := t.TempDir()
	messages := []session.Message{
		session.NewMessage("user", "Implement the fix, rerun the narrowest tests, write reports/change-summary.md with sections: findings, files changed, verification, remaining risks, and then finish."),
		session.NewToolMessage([]session.ToolResult{
			{Name: "write_file", Metadata: map[string]any{"path": filepath.Join(workdir, "reports", "change-summary.md")}},
		}),
	}
	kind, text := toolGuard(workdir, messages, "finish", json.RawMessage(`{"message":"done"}`))
	if kind != "" || text != "" {
		t.Fatalf("expected finish after change summary to bypass review_artifact guard, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardDoesNotTreatDelegatedAuditSetupNoteAsReviewArtifact(t *testing.T) {
	workdir := t.TempDir()
	kind, text := toolGuard(workdir, []session.Message{
		session.NewMessage("user", "Write reports/parent-note.md with one sentence noting that a delegated audit of this workspace is about to run.\nWrite reports/spec.md with a short delegated-review scope summary for this workspace.\nWrite reports/plan.md with a three-step reviewer checklist for the delegated audit.\nThen call finish."),
	}, "write_file", json.RawMessage(`{"path":"reports/parent-note.md","content":"A delegated audit of this workspace is about to run.\n"}`))
	if kind != "" || text != "" {
		t.Fatalf("expected delegated audit setup note to bypass review_artifact guard, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardAllowsFinishAfterDelegatedAuditSetupWrites(t *testing.T) {
	workdir := t.TempDir()
	messages := []session.Message{
		session.NewMessage("user", "Write reports/parent-note.md with one sentence noting that a delegated audit of this workspace is about to run.\nWrite reports/spec.md with a short delegated-review scope summary for this workspace.\nWrite reports/plan.md with a three-step reviewer checklist for the delegated audit.\nThen call finish."),
		session.NewToolMessage([]session.ToolResult{
			{Name: "write_file", Metadata: map[string]any{"path": filepath.Join(workdir, "reports", "parent-note.md")}},
			{Name: "write_file", Metadata: map[string]any{"path": filepath.Join(workdir, "reports", "spec.md")}},
			{Name: "write_file", Metadata: map[string]any{"path": filepath.Join(workdir, "reports", "plan.md")}},
		}),
	}
	kind, text := toolGuard(workdir, messages, "finish", json.RawMessage(`{"message":"done"}`))
	if kind != "" || text != "" {
		t.Fatalf("expected finish to bypass review_artifact guard for delegated audit setup note flow, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardAllowsFinishAfterRoleAwareDelegatedAuditScaffoldingWrites(t *testing.T) {
	workdir := t.TempDir()
	messages := []session.Message{
		session.NewMessage("user", "Write reports/spec.md with a short note that an evaluator child must audit config and quota behavior.\nWrite reports/plan.md with a three-step delegated evaluator checklist.\nWrite reports/progress.md with one line noting the parent prepared the role-aware handoff.\nWrite reports/validation.md with one line reserving space for evaluator findings.\nThen call finish."),
		session.NewToolMessage([]session.ToolResult{
			{Name: "write_file", Metadata: map[string]any{"path": filepath.Join(workdir, "reports", "spec.md")}},
			{Name: "write_file", Metadata: map[string]any{"path": filepath.Join(workdir, "reports", "plan.md")}},
			{Name: "write_file", Metadata: map[string]any{"path": filepath.Join(workdir, "reports", "progress.md")}},
			{Name: "write_file", Metadata: map[string]any{"path": filepath.Join(workdir, "reports", "validation.md")}},
		}),
	}
	kind, text := toolGuard(workdir, messages, "finish", json.RawMessage(`{"message":"done"}`))
	if kind != "" || text != "" {
		t.Fatalf("expected role-aware delegated audit scaffolding flow to bypass review_artifact guard, got kind=%q text=%q", kind, text)
	}
}

func TestReviewArtifactSatisfiedCountsValidatedReviewLikeScratchPath(t *testing.T) {
	workdir := t.TempDir()
	messages := []session.Message{
		session.NewMessage("user", "Audit the repo and write findings with remaining risks."),
		session.NewToolMessage([]session.ToolResult{
			{
				Name: "write_file",
				Metadata: map[string]any{
					"path":                      "/tmp/work/scratch/review-notes.txt",
					"review_artifact_valid":     true,
					"review_artifact_candidate": true,
				},
			},
		}),
	}
	if !reviewArtifactSatisfied(workdir, messages) {
		t.Fatal("expected validated review-like scratch artifact to satisfy finish guard")
	}
}

func TestReviewArtifactSatisfiedRequiresExactRequestedPathWhenPresent(t *testing.T) {
	workdir := t.TempDir()
	messages := []session.Message{
		session.NewMessage("user", "Audit the repo and write reports/final-audit.md."),
		session.NewToolMessage([]session.ToolResult{
			{
				Name: "write_file",
				Metadata: map[string]any{
					"path":                      filepath.Join(workdir, "scratch", "review-notes.txt"),
					"review_artifact_valid":     true,
					"review_artifact_candidate": true,
				},
			},
		}),
	}
	if reviewArtifactSatisfied(workdir, messages) {
		t.Fatal("expected exact requested artifact path to be required when one was specified")
	}
}

func TestReviewArtifactSatisfiedCountsValidatedRequestedPathWhenPresent(t *testing.T) {
	workdir := t.TempDir()
	messages := []session.Message{
		session.NewMessage("user", "Audit the repo and write reports/final-audit.md."),
		session.NewToolMessage([]session.ToolResult{
			{
				Name: "write_file",
				Metadata: map[string]any{
					"path":                  filepath.Join(workdir, "reports", "final-audit.md"),
					"review_artifact_valid": true,
				},
			},
		}),
	}
	if !reviewArtifactSatisfied(workdir, messages) {
		t.Fatal("expected validated exact requested artifact path to satisfy finish guard")
	}
}

func TestRequestedArtifactWriteRejectsWorkspaceEscapeForEditFile(t *testing.T) {
	workdir := t.TempDir()
	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "outside.md")
	if err := os.WriteFile(outsidePath, []byte("outside"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	target, ok := requestedArtifactWrite(workdir, "edit_file", json.RawMessage(`{"path":"`+outsidePath+`","old_text":"outside","new_text":"changed"}`))
	if ok {
		t.Fatalf("expected outside edit path to be rejected, got %#v", target)
	}
}

func TestRequestedArtifactWriteRejectsSymlinkEscapeForWriteFile(t *testing.T) {
	workdir := t.TempDir()
	outsideDir := t.TempDir()
	linkPath := filepath.Join(workdir, "escape-link")
	if err := os.Symlink(outsideDir, linkPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	target, ok := requestedArtifactWrite(workdir, "write_file", json.RawMessage(`{"path":"escape-link/review.md","content":"# findings"}`))
	if ok {
		t.Fatalf("expected symlink escape to be rejected, got %#v", target)
	}
}

func TestRequestedArtifactWriteUsesSameWorkspaceResolverAsFileTools(t *testing.T) {
	workdir := t.TempDir()
	reportsDir := filepath.Join(workdir, "reports")
	if err := os.MkdirAll(reportsDir, 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	linkPath := filepath.Join(workdir, "reports-link")
	if err := os.Symlink(reportsDir, linkPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	target, ok := requestedArtifactWrite(workdir, "write_file", json.RawMessage(`{"path":"reports-link/final-audit.md","content":"# findings\n\n## unresolved questions\n- None"}`))
	if !ok {
		t.Fatal("expected review artifact path to be accepted")
	}
	want, err := tools.ResolveWorkspacePath(workdir, "reports-link/final-audit.md")
	if err != nil {
		t.Fatalf("resolve workspace path: %v", err)
	}
	if target.Path != want {
		t.Fatalf("expected requestedArtifactWrite to reuse workspace resolver, got %q want %q", target.Path, want)
	}
}
