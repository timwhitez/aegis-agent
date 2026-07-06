package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go-cli-agent/internal/events"
	"go-cli-agent/internal/session"
	"go-cli-agent/internal/skills"
	"go-cli-agent/internal/tools"
)

func readFileNotFoundResultForPromptTest(path string) session.ToolResult {
	return session.ToolResult{
		Name:          "read_file",
		DisplayOutput: "Error: open " + path + ": no such file or directory",
		IsError:       true,
		Metadata: map[string]any{
			"path":                     path,
			tools.MetadataFailureClass: tools.FailureClassNotFound,
		},
	}
}

func TestBuildSystemPromptIncludesDirectToolGuidance(t *testing.T) {
	prompt := buildSystemPrompt(
		"/tmp/work",
		session.ModeExec,
		"",
		[]skills.Summary{{Name: "repo_audit", Description: "Repository audit workflow"}},
		[]skills.CommandTool{{Name: "markdown_inventory", Description: "List Markdown files"}},
		session.State{},
		nil,
	)

	checks := []string{
		"The harness provides tools, skills, session state, and safety boundaries",
		"Ground claims in files, session facts, and command results",
		"## Tool Use",
		"Tool names are capabilities, not workspace files or shell binaries.",
		"Workspace boundary is the current workdir.",
		"Prefer dedicated tools for their purpose",
		"Do not chain shell commands with separators",
		"reports/_*.txt",
		"Avoid `cat`, `grep`, `sed`, and `echo` inside `shell`",
		"Do not read a source path from memory",
		"issue them together; keep dependent operations sequential",
		"Do not guess required tool arguments, paths, or skill names",
		"`context/...` and `.context/...` are different paths",
		"Use `load_skill` only with exact names listed under Available skills",
		"When the user asks for a review, lead with findings ordered by severity",
		"## Skills",
		"### Available skills",
		"- repo_audit: Repository audit workflow",
		"check whether a listed skill clearly matches the user request",
		"After loading a skill, follow its instructions within the current project, user, and system constraints",
		"## Response Style",
		"Keep final answers concise and high-signal",
		"Reference local files with `path:line`",
		"Do not open with filler acknowledgements",
		"## Skill Command Tools",
		"- markdown_inventory: List Markdown files",
	}
	for _, needle := range checks {
		if !strings.Contains(prompt, needle) {
			t.Fatalf("expected prompt to contain %q, got:\n%s", needle, prompt)
		}
	}
}

func TestBuildSystemPromptUsesInitializerMode(t *testing.T) {
	prompt := buildSystemPrompt(
		"/tmp/work",
		session.ModeInit,
		"",
		nil,
		nil,
		session.State{},
		nil,
	)
	for _, needle := range []string{
		"You are a project initializer agent",
		"Use `feature_list_create` early",
		"Do not implement product features yet",
	} {
		if !strings.Contains(prompt, needle) {
			t.Fatalf("expected init prompt to contain %q, got:\n%s", needle, prompt)
		}
	}
}

func TestBuildSystemPromptSkipsSymlinkEscapedAgentsDoc(t *testing.T) {
	workdir := t.TempDir()
	external := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(external, []byte("external instruction"), 0o600); err != nil {
		t.Fatalf("write external: %v", err)
	}
	if err := os.Symlink(external, filepath.Join(workdir, "AGENTS.md")); err != nil {
		t.Fatalf("symlink agents: %v", err)
	}
	prompt := buildSystemPrompt(workdir, session.ModeExec, "", nil, nil, session.State{}, nil)
	if strings.Contains(prompt, "external instruction") || strings.Contains(prompt, "## Project Instructions") {
		t.Fatalf("symlink-escaped AGENTS.md should not be loaded, got:\n%s", prompt)
	}
}

func TestBuildSystemPromptAddsAuditEvidenceNoteForReviewTasks(t *testing.T) {
	prompt := buildSystemPrompt(
		"/tmp/work",
		session.ModeExec,
		"",
		nil,
		nil,
		session.State{},
		[]session.Message{
			session.NewMessage("user", "Audit whether the default runtime surface stays aligned with the docs."),
		},
	)
	// After prompt simplification, audit tasks without explicit artifact path get a simpler note
	if !strings.Contains(prompt, "For audit or review tasks, write a durable Markdown artifact before finishing") {
		t.Fatalf("expected audit task note, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Keep findings first, and separate unresolved questions or inference-limited points from validated findings") {
		t.Fatalf("expected findings structure guidance, got:\n%s", prompt)
	}
}

func TestBuildSystemPromptAddsReviewArtifactStructureNote(t *testing.T) {
	prompt := buildSystemPrompt(
		"/tmp/work",
		session.ModeExec,
		"",
		nil,
		nil,
		session.State{},
		[]session.Message{
			session.NewMessage("user", "Audit the repo and write reports/final-audit.md."),
		},
	)
	if !strings.Contains(prompt, "For audit or review deliverables, write findings first") {
		t.Fatalf("expected review artifact structure note, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "severity, confidence, exact evidence path/line") || !strings.Contains(prompt, "quoted snippet or identifier") || !strings.Contains(prompt, "why it matters") {
		t.Fatalf("expected evidence/confidence guidance, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "make sure the cited line range literally contains that text") {
		t.Fatalf("expected line-range correction guidance, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "inline at least one decisive assertion, event, or snippet from the child/subrun") {
		t.Fatalf("expected parent-artifact inline proof guidance, got:\n%s", prompt)
	}
}

func TestBuildSystemPromptDoesNotTreatGenericReviewHarnessSynthesisAsAuditTask(t *testing.T) {
	prompt := buildSystemPrompt(
		"/tmp/work",
		session.ModeExec,
		"",
		nil,
		nil,
		session.State{},
		[]session.Message{
			session.NewMessage("user", "Synthesize the recurring principles that a serious CLI coding/review harness should import."),
		},
	)
	if strings.Contains(prompt, "For audit or review tasks") || strings.Contains(prompt, "For audit or review deliverables") {
		t.Fatalf("expected generic coding/review synthesis prompt not to trigger audit guard notes, got:\n%s", prompt)
	}
}

func TestBuildSystemPromptOnlyInjectsCurrentWorkdirAgents(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("parent-only instruction: read spec/00-product.md"), 0o600); err != nil {
		t.Fatalf("write parent agents: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "AGENTS.md"), []byte("workspace-only instruction"), 0o600); err != nil {
		t.Fatalf("write workspace agents: %v", err)
	}

	prompt := buildSystemPrompt(
		workdir,
		session.ModeExec,
		"",
		nil,
		nil,
		session.State{},
		nil,
	)
	if !strings.Contains(prompt, "workspace-only instruction") {
		t.Fatalf("expected current workdir AGENTS.md to be injected, got:\n%s", prompt)
	}
	if strings.Contains(prompt, "parent-only instruction") || strings.Contains(prompt, "spec/00-product.md") {
		t.Fatalf("expected parent AGENTS.md to be excluded, got:\n%s", prompt)
	}
}

func TestBuildSystemPromptDoesNotClimbToParentAgentsWhenWorkdirHasNone(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("parent instruction must not leak"), 0o600); err != nil {
		t.Fatalf("write parent agents: %v", err)
	}

	prompt := buildSystemPrompt(
		workdir,
		session.ModeExec,
		"",
		nil,
		nil,
		session.State{},
		nil,
	)
	if strings.Contains(prompt, "parent instruction must not leak") {
		t.Fatalf("expected parent AGENTS.md to be excluded, got:\n%s", prompt)
	}
}

func TestBuildSystemPromptStillTreatsExplicitReviewOnlyTaskAsAuditTask(t *testing.T) {
	prompt := buildSystemPrompt(
		"/tmp/work",
		session.ModeExec,
		"",
		nil,
		nil,
		session.State{},
		[]session.Message{
			session.NewMessage("user", "Review only README.md and handler.go, then write reports/api-review.md."),
		},
	)
	// After prompt simplification, explicit review tasks with artifact path get detailed guidance
	if !strings.Contains(prompt, "For audit or review deliverables, write findings first") {
		t.Fatalf("expected explicit review task to keep audit notes, got:\n%s", prompt)
	}
}

func TestBuildSystemPromptDoesNotTreatDelegatedAuditScaffoldingAsCurrentReviewTask(t *testing.T) {
	prompt := buildSystemPrompt(
		"/tmp/work",
		session.ModeExec,
		"",
		nil,
		nil,
		session.State{},
		[]session.Message{
			session.NewMessage("user", "Write reports/spec.md with a short note that an evaluator child must audit config and quota behavior.\nWrite reports/plan.md with a three-step delegated evaluator checklist.\nWrite reports/progress.md with one line noting the parent prepared the role-aware handoff.\nWrite reports/validation.md with one line reserving space for evaluator findings.\nThen call finish."),
		},
	)
	if strings.Contains(prompt, "For audit or review tasks") || strings.Contains(prompt, "For audit or review deliverables") {
		t.Fatalf("expected delegated audit scaffolding prompt not to trigger current-session audit notes, got:\n%s", prompt)
	}
}

func TestBuildSystemPromptKeepsHarnessPromptWhenSystemOverrideIsSet(t *testing.T) {
	prompt := buildSystemPrompt(
		"/tmp/work",
		session.ModeExec,
		"Always answer in terse bullets.",
		nil,
		nil,
		session.State{},
		[]session.Message{
			session.NewMessage("user", "Audit the repo and write reports/final-audit.md."),
		},
	)
	if !strings.Contains(prompt, "## User System Instructions") || !strings.Contains(prompt, "Always answer in terse bullets.") {
		t.Fatalf("expected user system instructions section, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "In exec mode, you must use the finish tool") {
		t.Fatalf("expected mandatory harness guidance to survive system override, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Project instructions (outer to inner scope):") && !strings.Contains(prompt, "## Runtime Notes") {
		t.Fatalf("expected normal harness prompt body, got:\n%s", prompt)
	}
}

func TestBuildSystemPromptHighlightsRecentInterruptSteer(t *testing.T) {
	msg := session.NewMessage("user", "Stop exploring and write the report now.")
	msg.Meta = map[string]any{
		"source":    "steer",
		"interrupt": true,
	}
	prompt := buildSystemPrompt(
		"/tmp/work",
		session.ModeExec,
		"",
		nil,
		nil,
		session.State{},
		[]session.Message{
			session.NewMessage("user", "Initial task."),
			msg,
		},
	)
	if !strings.Contains(prompt, "A recent interrupt steer is now the active priority") {
		t.Fatalf("expected recent interrupt steer note, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Stop exploring and write the report now.") {
		t.Fatalf("expected steer text in prompt note, got:\n%s", prompt)
	}
}

func TestBuildSystemPromptAddsPlannerRoleGuidance(t *testing.T) {
	prompt := buildSystemPrompt(
		"/tmp/work",
		session.ModeExec,
		"",
		nil,
		nil,
		session.State{},
		[]session.Message{
			session.NewMessage("user", "Turn this brief prompt into a durable implementation plan."),
		},
		"planning-agent",
		"planner",
	)
	if !strings.Contains(prompt, "## Session Role") || !strings.Contains(prompt, "acting as the planner role") {
		t.Fatalf("expected planner role section, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "reports/spec.md plus reports/plan.md") || !strings.Contains(prompt, "durable task board aligned with the written plan") {
		t.Fatalf("expected planner handoff guidance, got:\n%s", prompt)
	}
}

func TestBuildSystemPromptAddsEvaluatorRoleGuidance(t *testing.T) {
	prompt := buildSystemPrompt(
		"/tmp/work",
		session.ModeExec,
		"",
		nil,
		nil,
		session.State{},
		[]session.Message{
			session.NewMessage("user", "Review the latest implementation and decide whether it is actually done."),
		},
		"reviewer",
		"evaluator",
	)
	if !strings.Contains(prompt, "acting as the evaluator role") {
		t.Fatalf("expected evaluator role section, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Be skeptical of claimed success") || !strings.Contains(prompt, "Refresh reports/validation.md") {
		t.Fatalf("expected evaluator skepticism and validation handoff guidance, got:\n%s", prompt)
	}
}

func TestBuildSystemPromptDoesNotInferRoleFromAgentName(t *testing.T) {
	prompt := buildSystemPrompt(
		"/tmp/work",
		session.ModeExec,
		"",
		nil,
		nil,
		session.State{},
		[]session.Message{
			session.NewMessage("user", "Review the latest implementation and decide whether it is actually done."),
		},
		"reviewer",
	)
	if strings.Contains(prompt, "## Session Role") || strings.Contains(prompt, "acting as the evaluator role") {
		t.Fatalf("expected agent_name alone not to infer role guidance, got:\n%s", prompt)
	}
}

func TestBuildSystemPromptUsesExplicitRoleWithAgentName(t *testing.T) {
	prompt := buildSystemPrompt(
		"/tmp/work",
		session.ModeExec,
		"",
		nil,
		nil,
		session.State{},
		[]session.Message{
			session.NewMessage("user", "Expand this into a durable implementation plan."),
		},
		"reviewer",
		"planner",
	)
	if !strings.Contains(prompt, "acting as the planner role") {
		t.Fatalf("expected explicit planner role guidance, got:\n%s", prompt)
	}
	if strings.Contains(prompt, "acting as the evaluator role") {
		t.Fatalf("expected explicit role to be the only role guidance, got:\n%s", prompt)
	}
}

func TestBuildSystemPromptKeepsLatestSteerPriorityPastOldWindow(t *testing.T) {
	steer := session.NewMessage("user", "Stop reading and finish from current evidence.")
	steer.Meta = map[string]any{
		"source":    "steer",
		"interrupt": true,
	}
	messages := []session.Message{
		session.NewMessage("user", "Initial task."),
		steer,
	}
	for i := 0; i < 12; i++ {
		messages = append(messages, session.NewToolMessage([]session.ToolResult{
			{Name: "read_file", Metadata: map[string]any{"path": "/tmp/work/file.txt"}},
		}))
	}
	prompt := buildSystemPrompt(
		"/tmp/work",
		session.ModeExec,
		"",
		nil,
		nil,
		session.State{},
		messages,
	)
	if !strings.Contains(prompt, "A recent interrupt steer is now the active priority") {
		t.Fatalf("expected steer note to persist, got:\n%s", prompt)
	}
}

func TestBuildSystemPromptDoesNotWarnOnRetrievalHeavyTail(t *testing.T) {
	prompt := buildSystemPrompt(
		"/tmp/work",
		session.ModeExec,
		"",
		nil,
		nil,
		session.State{},
		[]session.Message{
			session.NewMessage("user", "Audit the repo and write a report."),
			session.NewToolMessage([]session.ToolResult{
				{Name: "read_file", Metadata: map[string]any{"path": "/tmp/work/README.md"}},
				{Name: "read_file", Metadata: map[string]any{"path": "/tmp/work/README.md"}},
				{Name: "read_file", Metadata: map[string]any{"path": "/tmp/work/AGENTS.md"}},
				{Name: "grep"},
				{Name: "grep_files"},
				{Name: "glob"},
			}),
		},
	)
	for _, forbidden := range []string{
		"Recent work already used 6 read-only tool calls",
		"Do not reread files just to reconfirm",
		"runtime-reserved final proof rereads",
	} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("expected retrieval-heavy prompt not to include %q, got:\n%s", forbidden, prompt)
		}
	}
}

func TestToolGuardBlocksStaleTargetReportEvenInYolo(t *testing.T) {
	workdir := t.TempDir()
	steer := session.NewMessage("user", "你已经发散了，原始目标是https://it-infra-dev.bytedance.net/sys/ikvm-net?ikvm-net=%2Fikvm-net%2Fikvm%2Fsim；进行中文报告产出")
	steer.Meta = map[string]any{"source": "steer", "interrupt": true}
	messages := []session.Message{
		session.NewMessage("user", "Audit the target and write reports/assessment-report.md."),
		steer,
	}

	kind, text := toolGuard(workdir, messages, "write_file", json.RawMessage(`{
		"path":"reports/assessment-report.md",
		"content":"# Assessment\n\n目标: /ikvm-net/ikvm/list\n\n结论: 高风险。"
	}`), true)
	if kind != "target_consistency" {
		t.Fatalf("expected target_consistency guard, got kind=%q text=%q", kind, text)
	}
	if !strings.Contains(text, "/ikvm-net/ikvm/sim") {
		t.Fatalf("expected latest target in guard text, got %q", text)
	}

	kind, text = toolGuard(workdir, messages, "write_file", json.RawMessage(`{
		"path":"reports/assessment-report.md",
		"content":"# Assessment\n\n目标: /ikvm-net/ikvm/sim\n\n结论: validated."
	}`), true)
	if kind != "" || text != "" {
		t.Fatalf("expected corrected target report to pass, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardBlocksFinishWhenLatestTargetNotInFinalArtifact(t *testing.T) {
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, "reports"), 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	reportPath := filepath.Join(workdir, "reports", "assessment-report.md")
	if err := os.WriteFile(reportPath, []byte("# Assessment\n\n目标: /ikvm-net/ikvm/list\n"), 0o600); err != nil {
		t.Fatalf("write report: %v", err)
	}
	steer := session.NewMessage("user", "原始目标是https://it-infra-dev.bytedance.net/sys/ikvm-net?ikvm-net=%2Fikvm-net%2Fikvm%2Fsim")
	steer.Meta = map[string]any{"source": "steer", "interrupt": true}
	messages := []session.Message{
		session.NewMessage("user", "Write reports/assessment-report.md and finish."),
		steer,
		session.NewToolMessage([]session.ToolResult{{
			Name:     "write_file",
			Metadata: map[string]any{"path": reportPath},
		}}),
	}

	kind, text := toolGuard(workdir, messages, "finish", json.RawMessage(`{"message":"done"}`), true)
	if kind != "target_consistency" {
		t.Fatalf("expected target_consistency finish guard, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardBlocksReviewArtifactValidationSuccessContradictedByShellFailure(t *testing.T) {
	workdir := t.TempDir()
	messages := []session.Message{
		session.NewMessage("user", "Audit the repo and write reports/final-audit.md."),
		session.NewToolMessage([]session.ToolResult{{
			Name:          "shell",
			DisplayOutput: "validation failed: 2 checks failed",
			IsError:       true,
			Metadata: map[string]any{
				"command":   "make test",
				"exit_code": 2,
			},
		}}),
	}

	kind, text := toolGuard(workdir, messages, "write_file", json.RawMessage(`{
		"path":"reports/final-audit.md",
		"content":"# Final Audit\n\n## Validation\nAll tests passed.\n\n## Findings\nNo validated findings.\n\n## Remaining Risks\n- None.\n"
	}`))
	if kind != "validation_fact_consistency" {
		t.Fatalf("expected validation_fact_consistency guard, got kind=%q text=%q", kind, text)
	}
	if !strings.Contains(text, "make test") || !strings.Contains(text, "durable session evidence") {
		t.Fatalf("expected contradictory validation evidence in guard text, got %q", text)
	}
}

func TestToolGuardAllowsReviewArtifactThatReportsValidationFailure(t *testing.T) {
	workdir := t.TempDir()
	messages := []session.Message{
		session.NewMessage("user", "Audit the repo and write reports/final-audit.md."),
		session.NewToolMessage([]session.ToolResult{{
			Name:          "shell",
			DisplayOutput: "validation failed: 2 checks failed",
			IsError:       true,
			Metadata: map[string]any{
				"command":   "make test",
				"exit_code": 2,
			},
		}}),
	}

	kind, text := toolGuard(workdir, messages, "write_file", json.RawMessage(`{
		"path":"reports/final-audit.md",
		"content":"# Final Audit\n\n## Validation\nValidation failed: make test exited with code 2.\n\n## Findings\nNo validated findings.\n\n## Remaining Risks\n- Fix the failing checks before release.\n"
	}`), true)
	if kind != "" || text != "" {
		t.Fatalf("expected accurate validation failure report to pass fact guard, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardAllowsReviewArtifactWithoutValidationSuccessClaim(t *testing.T) {
	workdir := t.TempDir()
	messages := []session.Message{
		session.NewMessage("user", "Audit the repo and write reports/final-audit.md."),
		session.NewToolMessage([]session.ToolResult{{
			Name:          "shell",
			DisplayOutput: "validation failed: 2 checks failed",
			IsError:       true,
			Metadata: map[string]any{
				"command":   "make test",
				"exit_code": 2,
			},
		}}),
	}

	kind, text := toolGuard(workdir, messages, "write_file", json.RawMessage(`{
		"path":"reports/final-audit.md",
		"content":"# Final Audit\n\n## Findings\nNo validated findings.\n\n## Remaining Risks\n- Validation status is listed separately.\n"
	}`), true)
	if kind != "" || text != "" {
		t.Fatalf("expected artifact without validation success claim to pass fact guard, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardSkipsValidationFactGuardForExplicitNonReviewTask(t *testing.T) {
	workdir := t.TempDir()
	messages := []session.Message{
		session.NewMessage("user", "This is not a review or audit task. Write reports/final-audit.md as a migration note."),
		session.NewToolMessage([]session.ToolResult{{
			Name:          "shell",
			DisplayOutput: "validation failed: 2 checks failed",
			IsError:       true,
			Metadata: map[string]any{
				"command":   "make test",
				"exit_code": 2,
			},
		}}),
	}

	kind, text := toolGuard(workdir, messages, "write_file", json.RawMessage(`{
		"path":"reports/final-audit.md",
		"content":"# Migration Note\n\nValidation passed.\n"
	}`), true)
	if kind != "" || text != "" {
		t.Fatalf("expected explicit non-review task to bypass review fact guard, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardBlocksValidationSuccessContradictedBySupportingDoc(t *testing.T) {
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, "reports"), 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "reports", "validation.md"), []byte("Validation failed: command exited with code 1.\n"), 0o600); err != nil {
		t.Fatalf("write validation: %v", err)
	}
	messages := []session.Message{
		session.NewMessage("user", "Audit the repo and write reports/final-audit.md."),
	}

	kind, text := toolGuard(workdir, messages, "write_file", json.RawMessage(`{
		"path":"reports/final-audit.md",
		"content":"# Final Audit\n\n## Validation\nValidation passed.\n\n## Findings\nNo validated findings.\n\n## Remaining Risks\n- None.\n"
	}`), true)
	if kind != "validation_fact_consistency" {
		t.Fatalf("expected validation_fact_consistency guard, got kind=%q text=%q", kind, text)
	}
	if !strings.Contains(text, "reports/validation.md") {
		t.Fatalf("expected supporting validation doc evidence, got %q", text)
	}
}

func TestValidationFailureEvidenceRejectsSymlinkEscapedSupportingDoc(t *testing.T) {
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, "reports"), 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "validation.md")
	if err := os.WriteFile(outside, []byte("Validation failed: command exited with code 1.\n"), 0o600); err != nil {
		t.Fatalf("write outside validation: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(workdir, "reports", "validation.md")); err != nil {
		t.Fatalf("symlink validation: %v", err)
	}

	if evidence, ok := validationFailureEvidenceFromSupportingDocs(workdir); ok {
		t.Fatalf("expected symlink-escaped supporting doc to be ignored, got %q", evidence)
	}
}

func TestToolGuardBlocksFinishWhenSupportingDocsChangedAfterFinalReport(t *testing.T) {
	workdir := t.TempDir()
	finalPath := filepath.Join(workdir, "reports", "assessment-report.md")
	validationPath := filepath.Join(workdir, "reports", "validation.md")
	messages := []session.Message{
		session.NewMessage("user", "Write reports/assessment-report.md and finish."),
		session.NewToolMessage([]session.ToolResult{{
			Name:     "write_file",
			Metadata: map[string]any{"path": finalPath},
		}}),
		session.NewToolMessage([]session.ToolResult{{
			Name:     "write_file",
			Metadata: map[string]any{"path": validationPath},
		}}),
	}

	kind, text := toolGuard(workdir, messages, "finish", json.RawMessage(`{"message":"done"}`), true)
	if kind != "report_consistency" {
		t.Fatalf("expected report_consistency finish guard, got kind=%q text=%q", kind, text)
	}
	for _, want := range []string{
		"reports/assessment-report.md",
		"reports/validation.md",
		"Reading files again will not clear this guard",
		"Edit or rewrite reports/assessment-report.md",
		"Do not restart broad exploration",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected report_consistency guard to contain %q, got %q", want, text)
		}
	}
}

func TestNextHarnessReminderWarnsWhenSupportingDocsChangedAfterFinalReport(t *testing.T) {
	workdir := t.TempDir()
	finalPath := filepath.Join(workdir, "reports", "assessment-report.md")
	progressPath := filepath.Join(workdir, "reports", "progress.md")
	reminder := nextHarnessReminder(workdir, session.ModeExec, []session.Message{
		session.NewMessage("user", "Write reports/assessment-report.md and finish."),
		session.NewToolMessage([]session.ToolResult{{
			Name:     "write_file",
			Metadata: map[string]any{"path": finalPath},
		}}),
		session.NewToolMessage([]session.ToolResult{{
			Name:     "write_file",
			Metadata: map[string]any{"path": progressPath},
		}}),
	})
	if reminder.Kind != "report_consistency" {
		t.Fatalf("expected report_consistency reminder, got %#v", reminder)
	}
	for _, want := range []string{
		"supporting docs changed after the final deliverable reports/assessment-report.md",
		"reports/progress.md",
		"Edit or rewrite reports/assessment-report.md",
		"Do not restart broad exploration",
	} {
		if !strings.Contains(reminder.Text, want) {
			t.Fatalf("expected report consistency reminder to contain %q, got %q", want, reminder.Text)
		}
	}
}

func TestBuildSystemPromptDoesNotAddRetrievalPressureForReadOnlyShellInspection(t *testing.T) {
	prompt := buildSystemPrompt(
		"/tmp/work",
		session.ModeExec,
		"",
		nil,
		nil,
		session.State{},
		[]session.Message{
			session.NewMessage("user", "Audit the repo and write a report."),
			session.NewToolMessage([]session.ToolResult{
				{Name: "shell", Metadata: map[string]any{"command": "grep -n TODO README.md"}},
				{Name: "shell", Metadata: map[string]any{"command": "sed -n 1,40p README.md"}},
				{Name: "read_file", Metadata: map[string]any{"path": "/tmp/work/AGENTS.md"}},
				{Name: "glob"},
				{Name: "grep_files"},
				{Name: "shell", Metadata: map[string]any{"command": "cat README.md"}},
			}),
		},
	)
	if strings.Contains(prompt, "Recent work already used") {
		t.Fatalf("expected shell inspection not to produce retrieval pressure, got:\n%s", prompt)
	}
}

func TestBuildSystemPromptAddsGenericValidationAndChildReconciliationGuidance(t *testing.T) {
	prompt := buildSystemPrompt(
		"/tmp/work",
		session.ModeExec,
		"",
		nil,
		nil,
		session.State{},
		nil,
	)
	for _, want := range []string{
		"Before reporting validation success",
		"identify the relevant project or build root",
		"reconcile their durable results before final parent conclusions",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt guidance %q, got:\n%s", want, prompt)
		}
	}
	for _, forbidden := range []string{
		"must spawn",
		"must use child",
	} {
		if strings.Contains(strings.ToLower(prompt), forbidden) {
			t.Fatalf("prompt should keep delegation model-led, found %q in:\n%s", forbidden, prompt)
		}
	}
}

func TestBuildSystemPromptHighlightsCompletedArtifactWrite(t *testing.T) {
	prompt := buildSystemPrompt(
		"/tmp/work",
		session.ModeExec,
		"",
		nil,
		nil,
		session.State{},
		[]session.Message{
			session.NewMessage("user", "Write reports/final-audit.md and finish."),
			session.NewToolMessage([]session.ToolResult{
				{
					Name:     "write_file",
					Metadata: map[string]any{"path": "/tmp/work/reports/final-audit.md"},
				},
			}),
			session.NewToolMessage([]session.ToolResult{
				{
					Name:          "todo_write",
					DisplayOutput: "[{\"content\":\"Write report\",\"status\":\"completed\",\"priority\":\"high\",\"updated_at\":\"2026-03-20T00:00:00Z\"}]",
				},
			}),
		},
	)
	if !strings.Contains(prompt, "A requested artifact was already written to reports/final-audit.md") {
		t.Fatalf("expected artifact write note, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "If required side effects are done, call finish") {
		t.Fatalf("expected soft finish note, got:\n%s", prompt)
	}
	if strings.Contains(prompt, "todo state is fully completed") {
		t.Fatalf("todo completion must not be treated as delivery evidence, got:\n%s", prompt)
	}
}

func TestBuildSystemPromptAddsExactRequestedArtifactPathNote(t *testing.T) {
	workdir := t.TempDir()
	prompt := buildSystemPrompt(
		workdir,
		session.ModeExec,
		"",
		nil,
		nil,
		session.State{},
		[]session.Message{
			session.NewMessage("user", "Write reports/final-audit.md and finish."),
		},
	)
	if !strings.Contains(prompt, "The latest external instruction requested the deliverable at reports/final-audit.md") {
		t.Fatalf("expected exact requested artifact path note, got:\n%s", prompt)
	}
}

func TestNextHarnessReminderAddsSteerCompletionReminder(t *testing.T) {
	steer := session.NewMessage("user", "Stop reading now. Use current evidence, write reports/final-audit.md immediately, and finish.")
	steer.Meta = map[string]any{
		"source":    "steer",
		"interrupt": true,
	}
	reminder := nextHarnessReminder("/tmp/work", session.ModeExec, []session.Message{
		session.NewMessage("user", "Initial task."),
		steer,
	})
	if reminder.Kind != "steer_completion" {
		t.Fatalf("expected steer_completion reminder, got %#v", reminder)
	}
	if !strings.Contains(reminder.Text, "write the requested artifact") || !strings.Contains(reminder.Text, "finish immediately") {
		t.Fatalf("expected completion reminder text, got %q", reminder.Text)
	}
}

func TestNextHarnessReminderSkipsConditionalSteerCompletionReminder(t *testing.T) {
	steer := session.NewMessage("user", "Keep the same repair focused, and once go test ./... is green write reports/rt07-proof.md and call finish in the same turn.")
	steer.Meta = map[string]any{
		"source":    "steer",
		"interrupt": true,
	}
	reminder := nextHarnessReminder("/tmp/work", session.ModeExec, []session.Message{
		session.NewMessage("user", "Initial repair task."),
		steer,
	})
	if reminder.Kind == "steer_completion" || reminder.Kind == "steer_completion_escalated" {
		t.Fatalf("expected conditional repair steer not to trigger immediate completion reminder, got %#v", reminder)
	}
}

func TestNextHarnessReminderNudgesAfterRepeatedReadFileNotFound(t *testing.T) {
	reminder := nextHarnessReminder("/tmp/work", session.ModeExec, []session.Message{
		session.NewMessage("user", "Audit the Python service."),
		session.NewToolMessage([]session.ToolResult{
			readFileNotFoundResultForPromptTest("/tmp/work/vllm/missing_one.py"),
			readFileNotFoundResultForPromptTest("/tmp/work/vllm/missing_two.py"),
			readFileNotFoundResultForPromptTest("/tmp/work/vllm/missing_three.py"),
		}),
	})
	if reminder.Kind != "path_discovery_needed" {
		t.Fatalf("expected path_discovery_needed reminder, got %#v", reminder)
	}
	for _, want := range []string{"3 consecutive read_file not-found", "vllm", "grep_files or glob", "do not read source paths from memory"} {
		if !strings.Contains(reminder.Text, want) {
			t.Fatalf("expected path discovery reminder to contain %q, got %q", want, reminder.Text)
		}
	}
}

func TestNextHarnessReminderKeepsDeferredFinishInterruptSteerResumable(t *testing.T) {
	steer := session.NewMessage("user", "Actually change direction for this large documentation task: prioritize safer rollback and migration guidance. Refresh reports/spec.md and reports/plan.md before more drafting, then stop without finishing so a later continue can close the task.")
	steer.Meta = map[string]any{
		"source":    "steer",
		"interrupt": true,
	}
	reminder := nextHarnessReminder("/tmp/work", session.ModeRun, []session.Message{
		session.NewMessage("user", "Initial docset task."),
		steer,
	})
	if reminder.Kind != "steer_completion" {
		t.Fatalf("expected steer_completion reminder for deferred-finish steer, got %#v", reminder)
	}
	if !strings.Contains(reminder.Text, "write the requested artifact") {
		t.Fatalf("expected artifact-write reminder text, got %q", reminder.Text)
	}
	if strings.Contains(reminder.Text, "finish immediately") {
		t.Fatalf("expected deferred-finish steer not to demand immediate finish, got %q", reminder.Text)
	}
	if !strings.Contains(reminder.Text, "stop without finishing so a later continue can close the task") {
		t.Fatalf("expected deferred-finish reminder text, got %q", reminder.Text)
	}
}

func TestNextHarnessReminderEscalatesAfterBlockedSteerDetour(t *testing.T) {
	steer := session.NewMessage("user", "Stop reading now. Use current evidence, write reports/final-audit.md immediately, and finish.")
	steer.Meta = map[string]any{
		"source":    "steer",
		"interrupt": true,
	}
	initialReminder := session.NewMessage("user", "Harness reminder: deliver now.")
	initialReminder.Meta = map[string]any{
		"source": "harness_reminder",
		"kind":   "steer_completion",
	}
	blocked := session.NewToolMessage([]session.ToolResult{
		{
			Name:          "todo_write",
			IsError:       true,
			DisplayOutput: "Error: blocked",
			Metadata:      map[string]any{"guard": "steer_completion"},
		},
	})
	reminder := nextHarnessReminder("/tmp/work", session.ModeExec, []session.Message{
		session.NewMessage("user", "Initial task."),
		steer,
		initialReminder,
		blocked,
	})
	if reminder.Kind != "steer_completion_escalated" {
		t.Fatalf("expected escalated steer completion reminder, got %#v", reminder)
	}
	if !strings.Contains(reminder.Text, "blocked detour") {
		t.Fatalf("expected escalated reminder text, got %q", reminder.Text)
	}
}

func TestNextHarnessReminderEscalatesAfterAssistantReplyWithoutDelivery(t *testing.T) {
	steer := session.NewMessage("user", "Stop reading now. Use current evidence, write reports/final-audit.md immediately, and finish.")
	steer.Meta = map[string]any{
		"source":    "steer",
		"interrupt": true,
	}
	initialReminder := session.NewMessage("user", "Harness reminder: deliver now.")
	initialReminder.Meta = map[string]any{
		"source": "harness_reminder",
		"kind":   "steer_completion",
	}
	assistant := session.NewAssistantMessage("I have enough evidence and will summarize next.", "", nil)

	reminder := nextHarnessReminder("/tmp/work", session.ModeExec, []session.Message{
		session.NewMessage("user", "Initial task."),
		steer,
		initialReminder,
		assistant,
	})
	if reminder.Kind != "steer_completion_escalated" {
		t.Fatalf("expected escalated reminder after assistant-only reply, got %#v", reminder)
	}
	if !strings.Contains(reminder.Text, "non-delivery reply") {
		t.Fatalf("expected non-delivery escalation text, got %q", reminder.Text)
	}
}

func TestNextHarnessReminderRepeatsEscalatedSteerReminderUntilDelivery(t *testing.T) {
	steer := session.NewMessage("user", "Stop reading now. Use current evidence, write reports/final-audit.md immediately, and finish.")
	steer.Meta = map[string]any{
		"source":    "steer",
		"interrupt": true,
	}
	initialReminder := session.NewMessage("user", "Harness reminder: deliver now.")
	initialReminder.Meta = map[string]any{
		"source": "harness_reminder",
		"kind":   "steer_completion",
	}
	escalatedReminder := session.NewMessage("user", "Harness reminder: deliver now.")
	escalatedReminder.Meta = map[string]any{
		"source": "harness_reminder",
		"kind":   "steer_completion_escalated",
	}
	assistant := session.NewAssistantMessage("Still narrating.", "", nil)
	reminder := nextHarnessReminder("/tmp/work", session.ModeExec, []session.Message{
		session.NewMessage("user", "Initial task."),
		steer,
		initialReminder,
		assistant,
		escalatedReminder,
		assistant,
	})
	if reminder.Kind != "steer_completion_escalated" {
		t.Fatalf("expected escalated reminder to repeat until delivery, got %#v", reminder)
	}
}

func TestToolGuardDoesNotBlockReadsAfterRetrievalTailReminder(t *testing.T) {
	messages := []session.Message{
		session.NewMessage("user", "Audit the repo and write a report."),
	}
	reminder := session.NewMessage("user", "Harness reminder: stop exploring.")
	reminder.Meta = map[string]any{
		"source": "harness_reminder",
		"kind":   "retrieval_tail",
	}
	messages = append(messages, reminder)
	messages = append(messages, session.NewToolMessage([]session.ToolResult{
		{Name: "read_file", Metadata: map[string]any{"path": filepath.Join("/tmp/work", "internal", "app", "app.go"), "offset": 0, "end": 120}},
		{Name: "read_file", Metadata: map[string]any{"path": filepath.Join("/tmp/work", "internal", "runtime", "prompt.go"), "offset": 240, "end": 320}},
		{Name: "read_file", Metadata: map[string]any{"path": filepath.Join("/tmp/work", "spec", "09-phase-plan.md"), "offset": 0, "end": 80}},
	}))
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"path":"spec/11-spec-audit-and-traceability.md","offset":0,"limit":80}`),
		json.RawMessage(`{"path":"internal/runtime/prompt.go","offset":240,"limit":80}`),
		json.RawMessage(`{"path":"internal/app/app.go","offset":40,"limit":40}`),
	} {
		kind, text := toolGuard("/tmp/work", messages, "read_file", raw)
		if kind != "" || text != "" {
			t.Fatalf("expected retrieval_tail reminder not to block read_file %s, got kind=%q text=%q", string(raw), kind, text)
		}
	}
}

func TestToolGuardDoesNotBlockReadOnlyDiscoveryAfterRetrievalReminder(t *testing.T) {
	reminder := session.NewMessage("user", "Harness reminder: stop exploring.")
	reminder.Meta = map[string]any{
		"source": "harness_reminder",
		"kind":   "retrieval_tail",
	}
	messages := []session.Message{
		session.NewMessage("user", "Write reports/final-audit.md and finish."),
		reminder,
	}
	for _, tc := range []struct {
		name string
		args json.RawMessage
	}{
		{name: "grep_files", args: nil},
		{name: "grep", args: json.RawMessage(`{"path":"README.md","pattern":"TODO"}`)},
		{name: "glob", args: json.RawMessage(`{"pattern":"**/*.go"}`)},
		{name: "shell", args: json.RawMessage(`{"command":"grep -n TODO README.md"}`)},
	} {
		kind, text := toolGuard("/tmp/work", messages, tc.name, tc.args)
		if kind != "" || text != "" {
			t.Fatalf("expected retrieval_tail reminder not to block %s, got kind=%q text=%q", tc.name, kind, text)
		}
	}
}

func TestToolGuardAllowsActionShellAfterRetrievalReminder(t *testing.T) {
	reminder := session.NewMessage("user", "Harness reminder: stop exploring.")
	reminder.Meta = map[string]any{
		"source": "harness_reminder",
		"kind":   "retrieval_tail",
	}
	kind, text := toolGuard("/tmp/work", []session.Message{
		session.NewMessage("user", "Fix the bug and verify it."),
		reminder,
	}, "shell", json.RawMessage(`{"command":"go test ./..."}`))
	if kind != "" || text != "" {
		t.Fatalf("expected action shell to remain allowed, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardAllowsReadsAfterArtifactReminder(t *testing.T) {
	reminder := session.NewMessage("user", "Harness reminder: artifact already written.")
	reminder.Meta = map[string]any{
		"source": "harness_reminder",
		"kind":   "artifact_written",
	}
	kind, text := toolGuard("/tmp/work", []session.Message{
		session.NewMessage("user", "Write reports/final-audit.md and finish."),
		reminder,
	}, "grep", nil)
	if kind != "" || text != "" {
		t.Fatalf("expected artifact_written reminder not to block retrieval, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardBlocksBookkeepingAfterSteerCompletionReminder(t *testing.T) {
	steer := session.NewMessage("user", "Stop reading now. Use current evidence, write reports/final-audit.md immediately, and finish.")
	steer.Meta = map[string]any{
		"source":    "steer",
		"interrupt": true,
	}
	reminder := session.NewMessage("user", "Harness reminder: deliver now.")
	reminder.Meta = map[string]any{
		"source": "harness_reminder",
		"kind":   "steer_completion",
	}
	kind, text := toolGuard("/tmp/work", []session.Message{
		session.NewMessage("user", "Initial task."),
		steer,
		reminder,
	}, "todo_write", json.RawMessage(`{"todos":[]}`))
	if kind != "steer_completion" {
		t.Fatalf("expected steer_completion guard, got %q", kind)
	}
	if !strings.Contains(text, "todo/task bookkeeping") {
		t.Fatalf("expected completion detour guard text, got %q", text)
	}
}

func TestToolGuardBlocksFinishForDeferredInterruptSteer(t *testing.T) {
	workdir := t.TempDir()
	steer := session.NewMessage("user", "Refresh reports/spec.md and reports/plan.md before more drafting, then stop without finishing so a later continue can close the task.")
	steer.Meta = map[string]any{
		"source":    "steer",
		"interrupt": true,
	}
	messages := []session.Message{
		session.NewMessage("user", "Initial docset task."),
		session.NewToolMessage([]session.ToolResult{
			{
				Name: "write_file",
				Metadata: map[string]any{
					"path": filepath.Join(workdir, "reports", "spec.md"),
				},
			},
			{
				Name: "write_file",
				Metadata: map[string]any{
					"path": filepath.Join(workdir, "reports", "plan.md"),
				},
			},
		}),
		steer,
	}
	kind, text := toolGuard(workdir, messages, "finish", json.RawMessage(`{"message":"done"}`))
	if kind != "steer_deferred_finish" {
		t.Fatalf("expected steer_deferred_finish guard, got %q", kind)
	}
	if !strings.Contains(text, "stop without finishing so a later continue can close the task") {
		t.Fatalf("expected deferred finish guard text, got %q", text)
	}
}

func TestToolGuardBlocksInvalidReviewArtifactWrite(t *testing.T) {
	workdir := t.TempDir()
	kind, text := toolGuard(workdir, []session.Message{
		session.NewMessage("user", "Audit the repo and write reports/final-audit.md."),
	}, "write_file", json.RawMessage(`{"path":"reports/final-audit.md","content":"# audit\n\n## findings\n- evidence only\n"}`))
	if kind != "review_artifact" {
		t.Fatalf("expected review_artifact guard, got %q", kind)
	}
	if !strings.Contains(text, "Severity/Confidence/Evidence/Why it matters") {
		t.Fatalf("expected structured review guard text, got %q", text)
	}
	if !strings.Contains(text, "missing unresolved or remaining-risks section") {
		t.Fatalf("expected specific validator issue in guard text, got %q", text)
	}
}

func TestToolGuardBlocksFinishUntilReviewArtifactIsWritten(t *testing.T) {
	kind, text := toolGuard("/tmp/work", []session.Message{
		session.NewMessage("user", "Audit the repo and write reports/final-audit.md."),
	}, "finish", json.RawMessage(`{"message":"done"}`))
	if kind != "review_artifact" {
		t.Fatalf("expected review_artifact finish guard, got %q", kind)
	}
	if !strings.Contains(text, "before finishing this audit/review task") {
		t.Fatalf("expected finish guard text, got %q", text)
	}
}

func TestToolGuardBlocksFinishForReviewTaskWithoutExplicitArtifactPath(t *testing.T) {
	kind, text := toolGuard("/tmp/work", []session.Message{
		session.NewMessage("user", "Review the default runtime surface and validate the risks."),
	}, "finish", json.RawMessage(`{"message":"done"}`))
	if kind != "review_artifact" {
		t.Fatalf("expected review_artifact guard, got %q", kind)
	}
	if !strings.Contains(text, "reports/final-audit.md") {
		t.Fatalf("expected fallback artifact path guidance, got %q", text)
	}
}

func TestToolGuardAllowsFinishForExplicitNonReviewRetryProof(t *testing.T) {
	kind, text := toolGuard("/tmp/work", []session.Message{
		session.NewMessage("user", "This is a retry-resume transport proof, not a review or audit task. Do not read or write any files. Immediately call finish with exact message: RETRY_DRIFT_PROOF continue ok"),
	}, "finish", json.RawMessage(`{"message":"RETRY_DRIFT_PROOF continue ok"}`))
	if kind != "" || text != "" {
		t.Fatalf("expected explicit non-review proof task to bypass review_artifact guard, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardAllowsFinishAfterValidatedReviewArtifactWrite(t *testing.T) {
	messages := []session.Message{
		session.NewMessage("user", "Audit the repo and write reports/final-audit.md."),
		session.NewToolMessage([]session.ToolResult{
			{
				Name: "write_file",
				Metadata: map[string]any{
					"path":                  "/tmp/work/reports/final-audit.md",
					"review_artifact_valid": true,
				},
			},
		}),
	}
	kind, text := toolGuard("/tmp/work", messages, "finish", json.RawMessage(`{"message":"done"}`))
	if kind != "" || text != "" {
		t.Fatalf("expected finish to be allowed after validated artifact, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardBlocksWrongFinalArtifactPathWhenExactArtifactRequested(t *testing.T) {
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, "reports"), 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	kind, text := toolGuard(workdir, []session.Message{
		session.NewMessage("user", "Write reports/final-audit.md and finish."),
	}, "write_file", json.RawMessage(`{"path":"final-audit.md","content":"# audit"}`))
	if kind != "artifact_path" {
		t.Fatalf("expected artifact_path guard, got %q", kind)
	}
	if !strings.Contains(text, "reports/final-audit.md") {
		t.Fatalf("expected exact requested artifact path in guard text, got %q", text)
	}
}

func TestToolGuardBlocksEscapingFinalArtifactPathBeforeExecution(t *testing.T) {
	workdir := t.TempDir()
	kind, text := toolGuard(workdir, []session.Message{
		session.NewMessage("user", "Write reports/final-audit.md and finish."),
	}, "write_file", json.RawMessage(`{"path":"../escape.md","content":"# audit"}`))
	if kind != "artifact_path" {
		t.Fatalf("expected artifact_path guard, got %q", kind)
	}
	if !strings.Contains(text, "stay within the workspace") {
		t.Fatalf("expected workspace path guidance, got %q", text)
	}
}

func TestToolGuardAllowsRequestedFinalArtifactPathBeforeReviewValidation(t *testing.T) {
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, "reports"), 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	kind, _ := toolGuard(workdir, []session.Message{
		session.NewMessage("user", "Audit the repo and write reports/final-audit.md."),
	}, "write_file", json.RawMessage(`{"path":"reports/final-audit.md","content":"# audit\n\n## findings\n- evidence only\n"}`))
	if kind == "artifact_path" {
		t.Fatal("expected exact requested artifact path to bypass artifact_path guard")
	}
}

func TestRequestedArtifactPathsFromTextIgnoresNegatedArtifactPath(t *testing.T) {
	workdir := t.TempDir()
	allowed := filepath.Join(workdir, "reports", "final-audit.md")
	text := fmt.Sprintf("Write %s with sections.\nDo not write to a relative file named scratch-copy.md in the workspace root.", allowed)
	paths := requestedArtifactPathsFromText(workdir, text)
	if len(paths) != 1 {
		t.Fatalf("expected exactly one requested artifact path, got %#v", paths)
	}
	if paths[0] != filepath.Clean(allowed) {
		t.Fatalf("expected allowed artifact path %q, got %#v", filepath.Clean(allowed), paths)
	}
}

func TestRequestedArtifactPathsFromTextIgnoresReviewInputFiles(t *testing.T) {
	workdir := t.TempDir()
	text := "Use the review_pipeline skill for this task. Read reports/spec.md and reports/plan.md first as the delegated reviewer handoff. Review README.md, docs/contracts.md, internal/api/handler.go, internal/config/config.go, internal/quota/policy.go, internal/api/handler_test.go, internal/config/config_test.go, and internal/quota/policy_test.go. Write reports/delegate-review.md with sections: findings, unresolved questions, next fixes. Refresh reports/validation.md with sections: delegated reviewer contract, confirmed findings, remaining risks. Then call finish."
	paths := requestedArtifactPathsFromText(workdir, text)
	want := filepath.Join(workdir, "reports", "delegate-review.md")
	if len(paths) != 1 || paths[0] != want {
		t.Fatalf("expected only delegated review artifact %q, got %#v", want, paths)
	}
}

func TestRequestedArtifactPathsFromTextKeepsInlineCreatedArtifact(t *testing.T) {
	workdir := t.TempDir()
	text := "Review README.md, then write reports/final-review.md with findings and finish."
	paths := requestedArtifactPathsFromText(workdir, text)
	want := filepath.Join(workdir, "reports", "final-review.md")
	if len(paths) != 1 || paths[0] != want {
		t.Fatalf("expected only final review artifact %q, got %#v", want, paths)
	}
}

func TestToolGuardBlocksWriteToNegatedArtifactPath(t *testing.T) {
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, "reports"), 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	allowed := filepath.Join(workdir, "reports", "final-audit.md")
	message := fmt.Sprintf("Write %s with sections.\nDo not write to a relative file named scratch-copy.md in the workspace root.", allowed)
	kind, text := toolGuard(workdir, []session.Message{
		session.NewMessage("user", message),
	}, "write_file", json.RawMessage(`{"path":"scratch-copy.md","content":"# audit"}`))
	if kind != "artifact_path" {
		t.Fatalf("expected artifact_path guard, got %q", kind)
	}
	if !strings.Contains(text, "reports/final-audit.md") {
		t.Fatalf("expected guard to keep the allowed artifact path, got %q", text)
	}
}

func TestToolGuardBlocksFinishUntilExactRequestedArtifactIsWritten(t *testing.T) {
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, "reports"), 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	kind, text := toolGuard(workdir, []session.Message{
		session.NewMessage("user", "Write reports/final-audit.md and finish."),
	}, "finish", json.RawMessage(`{"message":"done"}`))
	if kind != "artifact_path" {
		t.Fatalf("expected artifact_path finish guard, got %q", kind)
	}
	if !strings.Contains(text, "reports/final-audit.md") {
		t.Fatalf("expected exact requested artifact path in finish guard text, got %q", text)
	}
}

func TestToolGuardKeepsExactArtifactFinishGuardAfterCompactionSummary(t *testing.T) {
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, "reports"), 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	compacted := session.NewMessage("user", "[Conversation compacted]\n{\"key_paths\":[\"README.md\"]}")
	compacted.Meta = map[string]any{
		"source": "compaction_summary",
	}
	kind, text := toolGuard(workdir, []session.Message{
		session.NewMessage("user", "Write reports/rt04-forced-compaction-proof.md and finish."),
		compacted,
	}, "finish", json.RawMessage(`{"message":"done"}`))
	if kind != "artifact_path" {
		t.Fatalf("expected artifact_path finish guard after compaction summary, got %q", kind)
	}
	if !strings.Contains(text, "reports/rt04-forced-compaction-proof.md") {
		t.Fatalf("expected exact requested artifact path in finish guard text, got %q", text)
	}
}

func TestToolGuardKeepsExactArtifactWriteGuardAfterCompactionSummary(t *testing.T) {
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, "reports"), 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	compacted := session.NewMessage("user", "[Conversation compacted]\n{\"key_paths\":[\"README.md\"]}")
	compacted.Meta = map[string]any{
		"source": "compaction_summary",
	}
	kind, text := toolGuard(workdir, []session.Message{
		session.NewMessage("user", "Write reports/rt04-forced-compaction-proof.md and finish."),
		compacted,
	}, "write_file", json.RawMessage(`{"path":"reports/final-audit.md","content":"# audit"}`))
	if kind != "artifact_path" {
		t.Fatalf("expected artifact_path write guard after compaction summary, got %q", kind)
	}
	if !strings.Contains(text, "reports/rt04-forced-compaction-proof.md") {
		t.Fatalf("expected exact requested artifact path in write guard text, got %q", text)
	}
}

func TestToolGuardBlocksFinishAfterCompactionWhenPromptFallsOutOfRecentTail(t *testing.T) {
	workdir := t.TempDir()
	store := session.NewStore(filepath.Join(t.TempDir(), "sessions"))
	state := session.State{}
	messages := []session.Message{
		session.NewMessage("user", "Write reports/rt04-forced-compaction-proof.md and finish."),
		session.NewAssistantMessage(strings.Repeat("analysis ", 200), "", nil),
		session.NewToolMessage([]session.ToolResult{{Name: "read_file", Metadata: map[string]any{"path": filepath.Join(workdir, "README.md")}, DisplayOutput: strings.Repeat("evidence ", 200)}}),
		session.NewAssistantMessage(strings.Repeat("more analysis ", 200), "", nil),
		session.NewToolMessage([]session.ToolResult{{Name: "grep", Metadata: map[string]any{"path": filepath.Join(workdir, "internal", "runtime", "prompt.go")}, DisplayOutput: strings.Repeat("match ", 200)}}),
		session.NewAssistantMessage(strings.Repeat("proof ", 200), "", nil),
		session.NewToolMessage([]session.ToolResult{{Name: "read_file", Metadata: map[string]any{"path": filepath.Join(workdir, "internal", "runtime", "compaction.go")}, DisplayOutput: strings.Repeat("proof ", 200)}}),
		session.NewAssistantMessage(strings.Repeat("wrap up ", 200), "", nil),
	}
	compactor := newCompactor(store)
	view, err := compactor.Build("sess-1", workdir, state, messages, nil, nil, 1, 1, func(events.Event) error { return nil })
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	kind, text := toolGuard(workdir, view, "finish", json.RawMessage(`{"message":"done"}`))
	if kind != "artifact_path" {
		t.Fatalf("expected artifact_path finish guard after compaction view, got %q", kind)
	}
	if !strings.Contains(text, "reports/rt04-forced-compaction-proof.md") {
		t.Fatalf("expected exact requested artifact path in finish guard text, got %q", text)
	}
}

func TestToolGuardBlocksFinishWhenValidatedScratchReviewArtifactMissesExactRequestedPath(t *testing.T) {
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, "reports"), 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
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
	kind, text := toolGuard(workdir, messages, "finish", json.RawMessage(`{"message":"done"}`))
	if kind != "artifact_path" {
		t.Fatalf("expected artifact_path finish guard, got %q", kind)
	}
	if !strings.Contains(text, "reports/final-audit.md") {
		t.Fatalf("expected exact requested artifact path in finish guard text, got %q", text)
	}
}

func TestToolGuardAllowsProjectMemoryWritesDuringReviewTask(t *testing.T) {
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, "reports"), 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	kind, text := toolGuard(workdir, []session.Message{
		session.NewMessage("user", "Audit the repo and write reports/final-audit.md."),
	}, "write_file", json.RawMessage(`{"path":"reports/spec.md","content":"# Spec snapshot"}`))
	if kind != "" || text != "" {
		t.Fatalf("expected project memory write to be allowed, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardBlocksEscapingReviewArtifactPathBeforeExecution(t *testing.T) {
	workdir := t.TempDir()
	kind, text := toolGuard(workdir, []session.Message{
		session.NewMessage("user", "Audit the repo and write findings with remaining risks."),
	}, "write_file", json.RawMessage(`{"path":"../review.md","content":"# findings\n\n## remaining risks\n- none\n"}`))
	if kind != "review_artifact" {
		t.Fatalf("expected review_artifact guard, got %q", kind)
	}
	if !strings.Contains(text, "must stay within the workspace") {
		t.Fatalf("expected workspace path guidance, got %q", text)
	}
}

func TestLooksFinalArtifactPathExcludesProjectMemoryStack(t *testing.T) {
	if looksFinalArtifactPath("/tmp/work/reports/spec.md") {
		t.Fatal("expected reports/spec.md not to count as final artifact path")
	}
	if looksFinalArtifactPath("/tmp/work/app/report.py") {
		t.Fatal("expected app/report.py not to count as final artifact path")
	}
	if !looksFinalArtifactPath("/tmp/work/reports/final-audit.md") {
		t.Fatal("expected reports/final-audit.md to count as final artifact path")
	}
}

func TestToolGuardAllowsSourceFileWritesEvenWhenArtifactIsRequested(t *testing.T) {
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, "app"), 0o755); err != nil {
		t.Fatalf("mkdir app: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workdir, "reports"), 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	kind, text := toolGuard(workdir, []session.Message{
		session.NewMessage("user", "Implement the fix, then write reports/change-summary.md and finish."),
	}, "write_file", json.RawMessage(`{"path":"app/report.py","content":"print('ok')\n"}`))
	if kind != "" || text != "" {
		t.Fatalf("expected source write to bypass artifact_path guard, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardBlocksFinishUntilChangeSummaryArtifactIsWritten(t *testing.T) {
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, "reports"), 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	kind, text := toolGuard(workdir, []session.Message{
		session.NewMessage("user", "Implement the fix, then write reports/change-summary.md and finish."),
	}, "finish", json.RawMessage(`{"message":"done"}`))
	if kind != "artifact_path" {
		t.Fatalf("expected artifact_path finish guard, got %q", kind)
	}
	if !strings.Contains(text, "reports/change-summary.md") {
		t.Fatalf("expected change-summary artifact path in finish guard text, got %q", text)
	}
}

func TestBuildSystemPromptAddsDurableProjectMemoryNote(t *testing.T) {
	workdir := t.TempDir()
	reportsDir := filepath.Join(workdir, "reports")
	if err := os.MkdirAll(reportsDir, 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	if err := os.WriteFile(filepath.Join(reportsDir, "plan.md"), []byte("# plan\n\n- Step 1"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	prompt := buildSystemPrompt(
		workdir,
		session.ModeExec,
		"",
		nil,
		nil,
		session.State{},
		[]session.Message{
			session.NewMessage("user", "Do a full large-project architecture review and implementation plan."),
		},
	)
	if !strings.Contains(prompt, "A durable project-memory stack is available: reports/plan.md") {
		t.Fatalf("expected durable project memory note, got:\n%s", prompt)
	}
}

func TestNextHarnessReminderSkipsLargeProjectCoordinationForNormalMultiFileRead(t *testing.T) {
	workdir := t.TempDir()
	reminder := nextHarnessReminder(workdir, session.ModeExec, []session.Message{
		session.NewMessage("user", "Handle this large complex repository refactor and keep the work traceable."),
		session.NewToolMessage([]session.ToolResult{
			{Name: "read_file", Metadata: map[string]any{"path": filepath.Join(workdir, "README.md")}},
			{Name: "read_file", Metadata: map[string]any{"path": filepath.Join(workdir, "AGENTS.md")}},
			{Name: "grep_files"},
			{Name: "glob"},
		}),
	})
	if reminder.Kind == "large_project_coordination" {
		t.Fatalf("expected normal multi-file read not to trigger coordination reminder, got %#v", reminder)
	}
}

func TestNextHarnessReminderSkipsLargeProjectCoordinationAfterBootstrap(t *testing.T) {
	workdir := t.TempDir()
	reportsDir := filepath.Join(workdir, "reports")
	if err := os.MkdirAll(reportsDir, 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	if err := os.WriteFile(filepath.Join(reportsDir, "spec.md"), []byte("# spec"), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	reminder := nextHarnessReminder(workdir, session.ModeExec, []session.Message{
		session.NewMessage("user", "Handle this large complex repository refactor and keep the work traceable."),
		session.NewToolMessage([]session.ToolResult{
			{Name: "read_file", Metadata: map[string]any{"path": filepath.Join(workdir, "README.md")}},
			{Name: "read_file", Metadata: map[string]any{"path": filepath.Join(workdir, "AGENTS.md")}},
			{Name: "grep_files"},
			{Name: "glob"},
		}),
		session.NewToolMessage([]session.ToolResult{
			{Name: "todo_write"},
			{Name: "task_create"},
		}),
	})
	if reminder.Kind == "large_project_coordination" {
		t.Fatalf("expected bootstrap activity to suppress coordination reminder, got %#v", reminder)
	}
}

func TestNextHarnessReminderSkipsLargeProjectCoordinationForSimpleValidationFlow(t *testing.T) {
	workdir := t.TempDir()
	reminder := nextHarnessReminder(workdir, session.ModeExec, []session.Message{
		session.NewMessage("user", "Validate skill execution and write reports/skill-proof.md with the observed tool outputs."),
		session.NewToolMessage([]session.ToolResult{
			{Name: "load_skill"},
			{Name: "todo_read"},
			{Name: "task_list"},
			{Name: "read_file", Metadata: map[string]any{"path": filepath.Join(workdir, "README.md")}},
		}),
	})
	if reminder.Kind == "large_project_coordination" {
		t.Fatalf("expected simple validation flow to skip coordination reminder, got %#v", reminder)
	}
}

func TestToolGuardBlocksBroadDiscoveryWhenInstructionEnumeratesExactFiles(t *testing.T) {
	workdir := t.TempDir()
	messages := []session.Message{
		session.NewMessage("user", "Inspect only README.md, AGENTS.md, and spec/00-product.md. Use targeted retrieval only."),
	}
	kind, text := toolGuard(workdir, messages, "glob", json.RawMessage(`{"pattern":"**/AGENTS.md"}`), false)
	if kind != "explicit_scope" {
		t.Fatalf("expected explicit_scope guard, got kind=%q text=%q", kind, text)
	}
	if !strings.Contains(text, "README.md") || !strings.Contains(text, "spec/00-product.md") {
		t.Fatalf("expected explicit paths in guard text, got %q", text)
	}
}

func TestToolGuardAllowsDirectReadWithinExplicitInspectionScope(t *testing.T) {
	workdir := t.TempDir()
	messages := []session.Message{
		session.NewMessage("user", "Inspect only README.md, AGENTS.md, and spec/00-product.md. Use targeted retrieval only."),
	}
	kind, text := toolGuard(workdir, messages, "read_file", json.RawMessage(`{"path":"README.md","offset":0,"limit":20}`), false)
	if kind != "" || text != "" {
		t.Fatalf("expected direct read within explicit scope to remain allowed, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardKeepsExplicitInspectionScopeInYolo(t *testing.T) {
	workdir := t.TempDir()
	messages := []session.Message{
		session.NewMessage("user", "Inspect only README.md and AGENTS.md in the current go-cli-agent repository. Use targeted retrieval only."),
	}
	kind, text := toolGuard(workdir, messages, "glob", json.RawMessage(`{"pattern":"**/README.md"}`), true)
	if kind != "explicit_scope" {
		t.Fatalf("expected explicit_scope guard in yolo, got kind=%q text=%q", kind, text)
	}
	if !strings.Contains(text, "README.md") || !strings.Contains(text, "AGENTS.md") {
		t.Fatalf("expected explicit paths in guard text, got %q", text)
	}
}

func TestToolGuardDoesNotTreatReadFirstInstructionsAsExactInspectionScope(t *testing.T) {
	workdir := t.TempDir()
	messages := []session.Message{
		session.NewMessage("user", "Read the local AGENTS.md and README.md first. Run the narrowest failing tests first, then diagnose all failing pytest cases across app/config.py, app/rules.py, and app/report.py."),
	}
	kind, text := toolGuard(workdir, messages, "glob", json.RawMessage(`{"pattern":"tests/test*.py"}`), false)
	if kind != "" || text != "" {
		t.Fatalf("expected read-first instructions to leave test discovery available, got kind=%q text=%q", kind, text)
	}
}

func TestNextHarnessReminderSkipsLargeProjectCoordinationForExplicitSmallestFixOptOut(t *testing.T) {
	workdir := t.TempDir()
	reminder := nextHarnessReminder(workdir, session.ModeExec, []session.Message{
		session.NewMessage("user", "This is a real smallest-correct-fix task. This is intentionally not a large-project task. Do not create a todo list, task board, or durable reports stack under reports/spec.md, reports/plan.md, reports/progress.md, or reports/validation.md. Start with python3 -m unittest -q test_inventory.py, inspect only the implicated files, write reports/change-summary.md, and then call finish."),
		session.NewToolMessage([]session.ToolResult{
			{Name: "read_file", Metadata: map[string]any{"path": filepath.Join(workdir, "AGENTS.md")}},
			{Name: "glob"},
			{Name: "read_file", Metadata: map[string]any{"path": filepath.Join(workdir, "reports", "change-summary.md")}},
			{Name: "read_file", Metadata: map[string]any{"path": filepath.Join(workdir, "inventory.py")}},
			{Name: "read_file", Metadata: map[string]any{"path": filepath.Join(workdir, "test_inventory.py")}},
		}),
	})
	if reminder.Kind == "large_project_coordination" {
		t.Fatalf("expected explicit smallest-fix opt-out to suppress coordination reminder, got %#v", reminder)
	}
}

func TestNextHarnessReminderAddsProjectMemoryRefreshReminder(t *testing.T) {
	workdir := t.TempDir()
	reportsDir := filepath.Join(workdir, "reports")
	if err := os.MkdirAll(reportsDir, 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	for _, name := range []string{"spec.md", "plan.md", "progress.md", "validation.md"} {
		if err := os.WriteFile(filepath.Join(reportsDir, name), []byte("# "+name), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	reminder := nextHarnessReminder(workdir, session.ModeExec, []session.Message{
		session.NewMessage("user", "Handle this large complex repository implementation and keep the handoff traceable."),
		session.NewToolMessage([]session.ToolResult{
			{Name: "write_file", Metadata: map[string]any{"path": filepath.Join(workdir, "reports", "progress.md")}},
		}),
		session.NewToolMessage([]session.ToolResult{
			{Name: "write_file", Metadata: map[string]any{"path": filepath.Join(workdir, "app", "main.go")}},
		}),
	})
	if reminder.Kind != "project_memory_refresh" {
		t.Fatalf("expected project_memory_refresh reminder, got %#v", reminder)
	}
	if !strings.Contains(reminder.Text, "refresh reports/progress.md") || !strings.Contains(reminder.Text, "refresh reports/validation.md") {
		t.Fatalf("expected refresh guidance, got %q", reminder.Text)
	}
}

func TestNextHarnessReminderRefreshesSpecAndPlanAfterSteerScopeChange(t *testing.T) {
	workdir := t.TempDir()
	reportsDir := filepath.Join(workdir, "reports")
	if err := os.MkdirAll(reportsDir, 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	for _, name := range []string{"spec.md", "plan.md", "progress.md", "validation.md"} {
		if err := os.WriteFile(filepath.Join(reportsDir, name), []byte("# "+name), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	steer := session.NewMessage("user", "Actually switch scope for this large repository task: prioritize API privacy and update the durable plan.")
	steer.Meta = map[string]any{
		"source":    "steer",
		"interrupt": true,
	}
	messages := []session.Message{
		session.NewMessage("user", "Handle this large complex repository implementation and keep the handoff traceable."),
		session.NewToolMessage([]session.ToolResult{
			{Name: "write_file", Metadata: map[string]any{"path": filepath.Join(workdir, "reports", "spec.md")}},
			{Name: "write_file", Metadata: map[string]any{"path": filepath.Join(workdir, "reports", "plan.md")}},
			{Name: "write_file", Metadata: map[string]any{"path": filepath.Join(workdir, "reports", "progress.md")}},
			{Name: "write_file", Metadata: map[string]any{"path": filepath.Join(workdir, "reports", "validation.md")}},
		}),
		steer,
	}
	reminder := nextHarnessReminder(workdir, session.ModeExec, messages)
	if reminder.Kind != "project_memory_refresh" {
		t.Fatalf("expected project_memory_refresh reminder after steer scope change, got %#v", reminder)
	}
	if !strings.Contains(reminder.Text, "refresh reports/spec.md") || !strings.Contains(reminder.Text, "refresh reports/plan.md") {
		t.Fatalf("expected spec/plan refresh guidance after steer scope change, got %q", reminder.Text)
	}
	if !strings.Contains(reminder.Text, "API privacy") {
		t.Fatalf("expected reminder to carry latest steer priority, got %q", reminder.Text)
	}
}

func TestToolGuardBlocksFinalArtifactWriteThatViolatesExactTemplate(t *testing.T) {
	workdir := t.TempDir()
	messages := []session.Message{
		session.NewMessage("user", "Write reports/rt20.md with sections: comparator setup and findings.\nThe file must begin exactly with these lines before any findings section:\n\n# rt20 same-task comparator\n\n## comparator setup\nThis is not a live competitor benchmark.\nThis is a same-task comparator built from go-cli-agent live run evidence plus local codex/opencode reference implementations.\n\nDo not paraphrase either sentence."),
	}
	kind, text := toolGuard(workdir, messages, "write_file", json.RawMessage(`{"path":"reports/rt20.md","content":"# rt20 same-task comparator\n\n## findings\nNo validated findings.\n"}`))
	if kind != "artifact_template" {
		t.Fatalf("expected artifact_template guard, got %q", kind)
	}
	if !strings.Contains(text, "exact required opening block") {
		t.Fatalf("expected exact-template guidance, got %q", text)
	}
}

func TestToolGuardAllowsFinishAfterExactTemplateArtifactWasWritten(t *testing.T) {
	workdir := t.TempDir()
	targetPath := filepath.Join(workdir, "reports", "rt20.md")
	messages := []session.Message{
		session.NewMessage("user", "Write reports/rt20.md with sections: comparator setup and findings.\nThe file must begin exactly with these lines before any findings section:\n\n# rt20 same-task comparator\n\n## comparator setup\nThis is not a live competitor benchmark.\nThis is a same-task comparator built from go-cli-agent live run evidence plus local codex/opencode reference implementations.\n\nDo not paraphrase either sentence."),
		session.NewToolMessage([]session.ToolResult{
			{
				Name: "write_file",
				Metadata: map[string]any{
					"path":                      targetPath,
					"exact_template_valid":      true,
					"review_artifact_valid":     true,
					"review_artifact_candidate": true,
				},
			},
		}),
	}
	kind, text := toolGuard(workdir, messages, "finish", json.RawMessage(`{"message":"done"}`))
	if kind != "" || text != "" {
		t.Fatalf("expected finish to be allowed after exact-template artifact write, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardBlocksFinalArtifactWriteThatViolatesExactLiteralAnchors(t *testing.T) {
	workdir := t.TempDir()
	messages := []session.Message{
		session.NewMessage("user", "Write reports/rt15.md with sections: confirmed runtime evidence, required proof anchors, findings, remaining gaps, next validation moves.\nThe final artifact must literally include the strings compact.started, compact.finished, rt05-incident-taskboard-before.json, and rt05-incident-taskboard-after.json in its evidence text.\nWrite a dedicated section named required proof anchors immediately after confirmed runtime evidence. In that section, include these exact standalone bullet prefixes, verbatim, before your explanation text:\n- compact.started:\n- compact.finished:\n- rt05-incident-taskboard-before.json:\n- rt05-incident-taskboard-after.json:\nThen call finish with a one-line summary."),
	}
	kind, text := toolGuard(workdir, messages, "write_file", json.RawMessage(`{"path":"reports/rt15.md","content":"# rt15\n\n## required proof anchors\n- compact.started:\n- compact.finished:\n"}`))
	if kind != "artifact_literal" {
		t.Fatalf("expected artifact_literal guard, got %q", kind)
	}
	if !strings.Contains(text, "rt05-incident-taskboard-before.json") || !strings.Contains(text, "rt05-incident-taskboard-after.json") {
		t.Fatalf("expected missing literal anchors in guard text, got %q", text)
	}
}

func TestToolGuardAllowsFinishAfterExactLiteralArtifactWasWritten(t *testing.T) {
	workdir := t.TempDir()
	targetPath := filepath.Join(workdir, "reports", "rt15.md")
	messages := []session.Message{
		session.NewMessage("user", "Write reports/rt15.md with sections: confirmed runtime evidence, required proof anchors, findings, remaining gaps, next validation moves.\nThe final artifact must literally include the strings compact.started, compact.finished, rt05-incident-taskboard-before.json, and rt05-incident-taskboard-after.json in its evidence text.\nWrite a dedicated section named required proof anchors immediately after confirmed runtime evidence. In that section, include these exact standalone bullet prefixes, verbatim, before your explanation text:\n- compact.started:\n- compact.finished:\n- rt05-incident-taskboard-before.json:\n- rt05-incident-taskboard-after.json:\nThen call finish with a one-line summary."),
		session.NewToolMessage([]session.ToolResult{
			{
				Name: "write_file",
				Metadata: map[string]any{
					"path":                      targetPath,
					"exact_literals_valid":      true,
					"review_artifact_valid":     true,
					"review_artifact_candidate": true,
				},
			},
		}),
	}
	kind, text := toolGuard(workdir, messages, "finish", json.RawMessage(`{"message":"done"}`))
	if kind != "" || text != "" {
		t.Fatalf("expected finish to be allowed after exact-literal artifact write, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardAllowsTargetedReadForReviewArtifactRepair(t *testing.T) {
	workdir := t.TempDir()
	inspectedPath := filepath.Join(workdir, "docs", "audit.md")
	reminder := session.NewMessage("user", "Harness reminder: use current evidence.")
	reminder.Meta = map[string]any{
		"source": "harness_reminder",
		"kind":   "retrieval_tail",
	}
	reviewBlocked := session.NewToolMessage([]session.ToolResult{
		{
			Name:          "write_file",
			IsError:       true,
			DisplayOutput: "Error: review artifact mismatch",
			Metadata:      map[string]any{"guard": "review_artifact"},
		},
	})
	messages := []session.Message{
		session.NewMessage("user", "Audit this doc and write reports/audit.md."),
		session.NewToolMessage([]session.ToolResult{
			{
				Name: "read_file",
				Metadata: map[string]any{
					"path":   inspectedPath,
					"offset": 40,
					"end":    66,
				},
				DisplayOutput: "inspected lines",
			},
		}),
		reminder,
		reviewBlocked,
	}
	kind, text := toolGuard(workdir, messages, "read_file", json.RawMessage(`{"path":"docs/audit.md","offset":58,"limit":6}`))
	if kind != "" || text != "" {
		t.Fatalf("expected targeted read repair to be allowed, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardAllowsTargetedGrepForReviewArtifactRepair(t *testing.T) {
	workdir := t.TempDir()
	inspectedPath := filepath.Join(workdir, "docs", "audit.md")
	reminder := session.NewMessage("user", "Harness reminder: use current evidence.")
	reminder.Meta = map[string]any{
		"source": "harness_reminder",
		"kind":   "retrieval_tail",
	}
	reviewBlocked := session.NewToolMessage([]session.ToolResult{
		{
			Name:          "write_file",
			IsError:       true,
			DisplayOutput: "Error: review artifact mismatch",
			Metadata:      map[string]any{"guard": "review_artifact"},
		},
	})
	messages := []session.Message{
		session.NewMessage("user", "Audit this doc and write reports/audit.md."),
		session.NewToolMessage([]session.ToolResult{
			{
				Name: "read_file",
				Metadata: map[string]any{
					"path":   inspectedPath,
					"offset": 40,
					"end":    66,
				},
				DisplayOutput: "inspected lines",
			},
		}),
		reminder,
		reviewBlocked,
	}
	kind, text := toolGuard(workdir, messages, "grep", json.RawMessage(`{"path":"docs/audit.md","pattern":"Authorization"}`))
	if kind != "" || text != "" {
		t.Fatalf("expected targeted grep repair to be allowed, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardAllowsProjectMemoryWriteDuringRefreshReminder(t *testing.T) {
	workdir := t.TempDir()
	reminder := session.NewMessage("user", "Harness reminder: refresh the durable project-memory stack.")
	reminder.Meta = map[string]any{
		"source": "harness_reminder",
		"kind":   "project_memory_refresh",
	}
	messages := []session.Message{
		session.NewMessage("user", "Handle this large complex repository implementation and keep the handoff traceable."),
		session.NewToolMessage([]session.ToolResult{
			{Name: "write_file", Metadata: map[string]any{"path": filepath.Join(workdir, "reports", "progress.md")}},
		}),
		session.NewToolMessage([]session.ToolResult{
			{Name: "write_file", Metadata: map[string]any{"path": filepath.Join(workdir, "app", "main.go")}},
		}),
		reminder,
	}
	kind, text := toolGuard(workdir, messages, "write_file", json.RawMessage(`{"path":"reports/validation.md","content":"# Validation\n\n- refreshed"}`))
	if kind != "" || text != "" {
		t.Fatalf("expected project-memory refresh write to be allowed, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardAllowsFinishDuringProjectMemoryRefreshReminder(t *testing.T) {
	workdir := t.TempDir()
	reminder := session.NewMessage("user", "Harness reminder: refresh the durable project-memory stack.")
	reminder.Meta = map[string]any{
		"source": "harness_reminder",
		"kind":   "project_memory_refresh",
	}
	messages := []session.Message{
		session.NewMessage("user", "Handle this large complex repository implementation and keep the handoff traceable."),
		session.NewToolMessage([]session.ToolResult{
			{Name: "write_file", Metadata: map[string]any{"path": filepath.Join(workdir, "reports", "progress.md")}},
		}),
		session.NewToolMessage([]session.ToolResult{
			{Name: "shell", Metadata: map[string]any{"command": "go test ./..."}},
		}),
		reminder,
	}
	kind, text := toolGuard(workdir, messages, "finish", json.RawMessage(`{"message":"done"}`))
	if kind != "" || text != "" {
		t.Fatalf("expected project-memory reminder not to block finish, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardAllowsAgentSpawnDuringProjectMemoryRefresh(t *testing.T) {
	workdir := t.TempDir()
	reminder := session.NewMessage("user", "Harness reminder: refresh the durable project-memory stack.")
	reminder.Meta = map[string]any{
		"source": "harness_reminder",
		"kind":   "project_memory_refresh",
	}
	messages := []session.Message{
		session.NewMessage("user", "Handle this large complex repository implementation and keep the handoff traceable."),
		session.NewToolMessage([]session.ToolResult{
			{Name: "write_file", Metadata: map[string]any{"path": filepath.Join(workdir, "reports", "progress.md")}},
		}),
		session.NewToolMessage([]session.ToolResult{
			{Name: "write_file", Metadata: map[string]any{"path": filepath.Join(workdir, "app", "main.go")}},
		}),
		reminder,
	}
	kind, text := toolGuard(workdir, messages, "agent_spawn", json.RawMessage(`{"prompt":"Review the latest work.","agent_name":"reviewer"}`))
	if kind != "" || text != "" {
		t.Fatalf("expected agent_spawn to remain model-led during project-memory refresh, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardAllowsWriteFileWhenInterruptSteerDemandsCurrentEvidenceDelivery(t *testing.T) {
	workdir := t.TempDir()
	evidenceDir := filepath.Join(workdir, "internal", "tools")
	if err := os.MkdirAll(evidenceDir, 0o755); err != nil {
		t.Fatalf("mkdir evidence dir: %v", err)
	}
	evidencePath := filepath.Join(evidenceDir, "registry.go")
	evidenceBody := "package tools\n\nvar reservedNames = map[string]struct{}{ \"agent_spawn\": {} }\n"
	if err := os.WriteFile(evidencePath, []byte(evidenceBody), 0o600); err != nil {
		t.Fatalf("write evidence file: %v", err)
	}
	steer := session.NewMessage("user", "Stop reading now. Use current evidence, write reports/audit.md immediately, and finish.")
	steer.Meta = map[string]any{
		"source":    "steer",
		"interrupt": true,
	}
	reminder := session.NewMessage("user", "Harness reminder: deliver now with current evidence.")
	reminder.Meta = map[string]any{
		"source": "harness_reminder",
		"kind":   "steer_completion",
	}
	messages := []session.Message{
		session.NewMessage("user", "Audit whether the default Web-first surface, CLI fallback, and docs stay aligned with Web-first v1 boundaries."),
		session.NewToolMessage([]session.ToolResult{
			{
				Name: "read_file",
				Metadata: map[string]any{
					"path": evidencePath,
				},
				DisplayOutput: "var reservedNames = map[string]struct{}{ \"agent_spawn\": {} }",
			},
		}),
		steer,
		reminder,
	}
	kind, text := toolGuard(workdir, messages, "write_file", json.RawMessage(`{
		"path":"reports/audit.md",
		"content":"# audit\n\n## findings\n### Finding 1\nSeverity: low\nConfidence: low\nEvidence: internal/tools/registry.go:3 (\"reservedNames\")\nSnippet: reservedNames\nWhy it matters: current-evidence delivery should still write a structured artifact.\n\n## unresolved questions\n- The owning gate read was intentionally deferred after the interrupt steer."
	}`))
	if kind != "" || text != "" {
		t.Fatalf("expected current-evidence interrupt steer to allow artifact delivery, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardAllowsFinishWhenInterruptSteerDemandsCurrentEvidenceDelivery(t *testing.T) {
	steer := session.NewMessage("user", "Stop reading now. Use current evidence and finish after writing the report.")
	steer.Meta = map[string]any{
		"source":    "steer",
		"interrupt": true,
	}
	messages := []session.Message{
		session.NewMessage("user", "Audit whether the default Web-first surface, CLI fallback, and docs stay aligned with Web-first v1 boundaries."),
		session.NewToolMessage([]session.ToolResult{
			{
				Name: "read_file",
				Metadata: map[string]any{
					"path": filepath.Join("/tmp/work", "internal", "tools", "registry.go"),
				},
				DisplayOutput: "var reservedNames = map[string]struct{}{ \"agent_spawn\": {} }",
			},
			{
				Name: "write_file",
				Metadata: map[string]any{
					"path":                  filepath.Join("/tmp/work", "reports", "audit.md"),
					"review_artifact_valid": true,
				},
			},
		}),
		steer,
	}
	kind, text := toolGuard("/tmp/work", messages, "finish", json.RawMessage(`{"message":"done"}`))
	if kind != "" || text != "" {
		t.Fatalf("expected current-evidence interrupt steer to allow finish after artifact delivery, got kind=%q text=%q", kind, text)
	}
}
