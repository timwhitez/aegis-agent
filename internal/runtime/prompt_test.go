package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go-cli-agent/internal/session"
	"go-cli-agent/internal/skills"
)

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
		"## Tool Use",
		"Every tool in the tool list is already callable by name through the tool interface.",
		"Workspace boundary is the current workdir.",
		"Parent-scope `AGENTS.md` instructions still apply",
		"Prefer targeted retrieval",
		"Do not use read-only shell commands like `cat`, `sed`, `grep`, or `rg` to bypass retrieval limits",
		"Before any extra retrieval, ask whether it will materially change the answer",
		"`write_file` directly once you have enough evidence",
		"`glob` or `grep_files`",
		"Requests above 120 lines will be capped.",
		"After a compaction summary, rely on its `key_paths`",
		"`high_value_proofs`",
		"Use `load_skill` only for skill names",
		"## Skills",
		"### Available skills",
		"## Skill Command Tools",
		"- markdown_inventory: List Markdown files",
	}
	for _, needle := range checks {
		if !strings.Contains(prompt, needle) {
			t.Fatalf("expected prompt to contain %q, got:\n%s", needle, prompt)
		}
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
	if !strings.Contains(prompt, "For audit or review tasks, keep validated findings evidence-scoped") {
		t.Fatalf("expected audit evidence note, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "verify the owning code path or downgrade the point to risk or inference") {
		t.Fatalf("expected audit downgrade guidance, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Prefer the owning registration or gate in the same file before widening search") {
		t.Fatalf("expected same-file proof guidance, got:\n%s", prompt)
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
	if !strings.Contains(prompt, "For audit or review tasks") || !strings.Contains(prompt, "For audit or review deliverables") {
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
	)
	if !strings.Contains(prompt, "acting as the evaluator role") {
		t.Fatalf("expected evaluator role section, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Be skeptical of claimed success") || !strings.Contains(prompt, "Refresh reports/validation.md") {
		t.Fatalf("expected evaluator skepticism and validation handoff guidance, got:\n%s", prompt)
	}
}

func TestBuildSystemPromptPrefersExplicitRoleOverAgentName(t *testing.T) {
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
		t.Fatalf("expected explicit role to override inferred agent-name role, got:\n%s", prompt)
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

func TestBuildSystemPromptWarnsOnRetrievalHeavyTail(t *testing.T) {
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
	if !strings.Contains(prompt, "Recent work already used 6 read-only tool calls") {
		t.Fatalf("expected retrieval pressure note, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Do not reread files just to reconfirm") {
		t.Fatalf("expected reread warning, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "runtime-reserved final proof rereads") {
		t.Fatalf("expected reserved proof-read guidance, got:\n%s", prompt)
	}
}

func TestBuildSystemPromptCountsReadOnlyShellInspectionAsRetrieval(t *testing.T) {
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
	if !strings.Contains(prompt, "Recent work already used 6 read-only tool calls") {
		t.Fatalf("expected shell inspection to count toward retrieval pressure, got:\n%s", prompt)
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
	if !strings.Contains(prompt, "Call finish now") {
		t.Fatalf("expected finish pressure note, got:\n%s", prompt)
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
	assistant := session.NewAssistantMessage("I have enough evidence and will summarize next.", nil)

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
	assistant := session.NewAssistantMessage("Still narrating.", nil)
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

func TestNextHarnessReminderAddsAuditEvidenceReminderWhenProofIsMissing(t *testing.T) {
	reminder := nextHarnessReminder("/tmp/work", session.ModeExec, []session.Message{
		session.NewMessage("user", "Audit whether the default core tool surface stays aligned with core v1 boundaries."),
		session.NewToolMessage([]session.ToolResult{
			{
				Name: "read_file",
				Metadata: map[string]any{
					"path": filepath.Join("/tmp/work", "internal", "tools", "registry.go"),
				},
				DisplayOutput: "var reservedNames = map[string]struct{}{ \"agent_spawn\": {} }",
			},
		}),
	})
	if reminder.Kind != "audit_evidence" {
		t.Fatalf("expected audit_evidence reminder, got %#v", reminder)
	}
	if !strings.Contains(reminder.Text, "Reserved names") || !strings.Contains(reminder.Text, "registration or config gate") {
		t.Fatalf("expected audit reminder text, got %q", reminder.Text)
	}
	if !strings.Contains(reminder.Text, "builtinDefinitions(...)") || !strings.Contains(reminder.Text, "cfg.Runtime.MultiAgent.Enabled") {
		t.Fatalf("expected focused audit follow-up hint, got %q", reminder.Text)
	}
}

func TestNextHarnessReminderSkipsAuditEvidenceForGenericArchitectureAudit(t *testing.T) {
	reminder := nextHarnessReminder("/tmp/work", session.ModeExec, []session.Message{
		session.NewMessage("user", "Audit the current repository for core v1 readiness. Inspect README.md, AGENTS.md, internal/runtime, and internal/app."),
		session.NewToolMessage([]session.ToolResult{
			{
				Name: "read_file",
				Metadata: map[string]any{
					"path": filepath.Join("/tmp/work", "internal", "app", "app.go"),
				},
				DisplayOutput: "package app",
			},
		}),
	})
	if reminder.Kind == "audit_evidence" {
		t.Fatalf("expected generic architecture audit to avoid audit_evidence reminder, got %#v", reminder)
	}
}

func TestNextHarnessReminderSkipsAuditEvidenceReminderWhenGateProofExists(t *testing.T) {
	reminder := nextHarnessReminder("/tmp/work", session.ModeExec, []session.Message{
		session.NewMessage("user", "Audit whether the default core tool surface stays aligned with core v1 boundaries."),
		session.NewToolMessage([]session.ToolResult{
			{
				Name: "read_file",
				Metadata: map[string]any{
					"path": filepath.Join("/tmp/work", "internal", "tools", "registry.go"),
				},
				DisplayOutput: "if cfg != nil && cfg.Runtime.MultiAgent.Enabled {\n\tdefs = append(defs, defAgentSpawn(control))\n}",
			},
		}),
	})
	if reminder.Kind == "audit_evidence" {
		t.Fatalf("expected no audit_evidence reminder once gate proof exists, got %#v", reminder)
	}
}

func TestNextHarnessReminderDoesNotTreatBuiltinDefinitionsCallsiteAsProof(t *testing.T) {
	reminder := nextHarnessReminder("/tmp/work", session.ModeExec, []session.Message{
		session.NewMessage("user", "Audit whether the default core tool surface stays aligned with core v1 boundaries."),
		session.NewToolMessage([]session.ToolResult{
			{
				Name: "read_file",
				Metadata: map[string]any{
					"path": filepath.Join("/tmp/work", "internal", "tools", "registry.go"),
				},
				DisplayOutput: "func NewRegistry(...) {\n\tfor _, def := range builtinDefinitions(cfg, control) {\n\t\tregistry.Register(def)\n\t}\n}",
			},
		}),
	})
	if reminder.Kind != "audit_evidence" {
		t.Fatalf("expected audit_evidence reminder when only callsite is seen, got %#v", reminder)
	}
}

func TestToolGuardAllowsOneNewReadAfterRetrievalTailReminder(t *testing.T) {
	reminder := session.NewMessage("user", "Harness reminder: stop exploring.")
	reminder.Meta = map[string]any{
		"source": "harness_reminder",
		"kind":   "retrieval_tail",
	}
	messages := []session.Message{
		session.NewMessage("user", "Audit the repo and write a report."),
		session.NewToolMessage([]session.ToolResult{
			{Name: "read_file", Metadata: map[string]any{"path": filepath.Join("/tmp/work", "README.md")}},
		}),
		reminder,
	}
	kind, text := toolGuard("/tmp/work", messages, "read_file", json.RawMessage(`{"path":"docs/guide.md"}`))
	if kind != "" || text != "" {
		t.Fatalf("expected one new post-reminder read to be allowed, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardAllowsSecondNonOverlappingSlicePastReminder(t *testing.T) {
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
		{Name: "read_file", Metadata: map[string]any{"path": filepath.Join("/tmp/work", "docs", "guide.md"), "offset": 0, "end": 120}},
	}))
	kind, text := toolGuard("/tmp/work", messages, "read_file", json.RawMessage(`{"path":"docs/guide.md","offset":240,"limit":80}`))
	if kind != "" || text != "" {
		t.Fatalf("expected non-overlapping second slice to be allowed, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardAllowsSecondNonOverlappingSlicePastReminderWithJSONMetadataNumbers(t *testing.T) {
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
		{Name: "read_file", Metadata: map[string]any{"path": filepath.Join("/tmp/work", "docs", "guide.md"), "offset": float64(0), "end": float64(120)}},
	}))
	kind, text := toolGuard("/tmp/work", messages, "read_file", json.RawMessage(`{"path":"docs/guide.md","offset":240,"limit":80}`))
	if kind != "" || text != "" {
		t.Fatalf("expected non-overlapping second slice to be allowed after JSON roundtrip, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardBlocksRepeatedReadsPastReminder(t *testing.T) {
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
		{Name: "read_file", Metadata: map[string]any{"path": filepath.Join("/tmp/work", "docs", "guide.md"), "offset": 0, "end": 120}},
	}))
	kind, text := toolGuard("/tmp/work", messages, "read_file", json.RawMessage(`{"path":"docs/guide.md","offset":40,"limit":80}`))
	if kind != "retrieval_tail" {
		t.Fatalf("expected retrieval_tail guard, got %q", kind)
	}
	if !strings.Contains(text, "rereading the same file slice is blocked") {
		t.Fatalf("expected retrieval guard text, got %q", text)
	}
}

func TestToolGuardAllowsNarrowRereadAfterReviewArtifactGuard(t *testing.T) {
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
		{Name: "read_file", Metadata: map[string]any{"path": filepath.Join("/tmp/work", "internal", "provider", "openai.go"), "offset": 0, "end": 120}},
	}))
	messages = append(messages, session.NewToolMessage([]session.ToolResult{
		{
			Name:    "write_file",
			IsError: true,
			Metadata: map[string]any{
				"guard": "review_artifact",
			},
		},
	}))
	kind, text := toolGuard("/tmp/work", messages, "read_file", json.RawMessage(`{"path":"internal/provider/openai.go","offset":80,"limit":20}`))
	if kind != "" || text != "" {
		t.Fatalf("expected narrow review-artifact repair reread to be allowed, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardAllowsReservedFinalProofReadAfterTwoExplorationReads(t *testing.T) {
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
		{Name: "read_file", Metadata: map[string]any{"path": filepath.Join("/tmp/work", "internal", "runtime", "compaction.go"), "offset": 0, "end": 120}},
		{Name: "read_file", Metadata: map[string]any{"path": filepath.Join("/tmp/work", "internal", "runtime", "prompt.go"), "offset": 240, "end": 320}},
	}))
	kind, text := toolGuard("/tmp/work", messages, "read_file", json.RawMessage(`{"path":"internal/runtime/compaction.go","offset":20,"limit":20}`))
	if kind != "" || text != "" {
		t.Fatalf("expected reserved final proof read to be allowed, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardBlocksThirdReservedFinalProofRead(t *testing.T) {
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
		{Name: "read_file", Metadata: map[string]any{"path": filepath.Join("/tmp/work", "internal", "runtime", "compaction.go"), "offset": 0, "end": 120}},
		{Name: "read_file", Metadata: map[string]any{"path": filepath.Join("/tmp/work", "internal", "runtime", "compaction.go"), "offset": 10, "end": 30}},
		{Name: "read_file", Metadata: map[string]any{"path": filepath.Join("/tmp/work", "internal", "runtime", "prompt.go"), "offset": 240, "end": 320}},
		{Name: "read_file", Metadata: map[string]any{"path": filepath.Join("/tmp/work", "internal", "runtime", "prompt.go"), "offset": 260, "end": 280}},
	}))
	kind, text := toolGuard("/tmp/work", messages, "read_file", json.RawMessage(`{"path":"internal/runtime/compaction.go","offset":40,"limit":20}`))
	if kind != "retrieval_tail" {
		t.Fatalf("expected retrieval_tail guard after reserved proof budget is spent, got %q", kind)
	}
	if !strings.Contains(text, "rereading an already inspected file slice is blocked") && !strings.Contains(text, "rereading the same file slice is blocked") {
		t.Fatalf("expected reserved proof reread guard text, got %q", text)
	}
}

func TestToolGuardBlocksWideRereadAfterReviewArtifactGuard(t *testing.T) {
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
		{Name: "read_file", Metadata: map[string]any{"path": filepath.Join("/tmp/work", "internal", "provider", "openai.go"), "offset": 0, "end": 120}},
	}))
	messages = append(messages, session.NewToolMessage([]session.ToolResult{
		{
			Name:    "write_file",
			IsError: true,
			Metadata: map[string]any{
				"guard": "review_artifact",
			},
		},
	}))
	kind, text := toolGuard("/tmp/work", messages, "read_file", json.RawMessage(`{"path":"internal/provider/openai.go","offset":40,"limit":80}`))
	if kind != "retrieval_tail" {
		t.Fatalf("expected retrieval_tail guard for wide reread, got %q", kind)
	}
	if !strings.Contains(text, "rereading an already inspected file slice is blocked") && !strings.Contains(text, "rereading the same file slice is blocked") {
		t.Fatalf("expected reread guard text, got %q", text)
	}
}

func TestToolGuardBlocksThirdNarrowRereadAfterReviewArtifactGuard(t *testing.T) {
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
		{Name: "read_file", Metadata: map[string]any{"path": filepath.Join("/tmp/work", "internal", "provider", "openai.go"), "offset": 0, "end": 120}},
	}))
	messages = append(messages, session.NewToolMessage([]session.ToolResult{
		{
			Name:    "write_file",
			IsError: true,
			Metadata: map[string]any{
				"guard": "review_artifact",
			},
		},
	}))
	messages = append(messages, session.NewToolMessage([]session.ToolResult{
		{Name: "read_file", Metadata: map[string]any{"path": filepath.Join("/tmp/work", "internal", "provider", "openai.go"), "offset": 80, "end": 100}},
		{Name: "read_file", Metadata: map[string]any{"path": filepath.Join("/tmp/work", "internal", "provider", "openai.go"), "offset": 100, "end": 120}},
	}))
	kind, text := toolGuard("/tmp/work", messages, "read_file", json.RawMessage(`{"path":"internal/provider/openai.go","offset":60,"limit":20}`))
	if kind != "retrieval_tail" {
		t.Fatalf("expected retrieval_tail guard after two repair rereads, got %q", kind)
	}
	if !strings.Contains(text, "rereading the same file slice is blocked") && !strings.Contains(text, "rereading an already inspected file slice is blocked") && !strings.Contains(text, "maximum targeted read_file budget") && !strings.Contains(text, "two allowed targeted read_file calls") {
		t.Fatalf("expected repair reread budget guard text, got %q", text)
	}
}

func TestToolGuardBlocksThirdTargetedReadPastReminder(t *testing.T) {
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
		{Name: "read_file", Metadata: map[string]any{"path": filepath.Join("/tmp/work", "docs", "guide.md"), "offset": 0, "end": 120}},
		{Name: "read_file", Metadata: map[string]any{"path": filepath.Join("/tmp/work", "docs", "guide.md"), "offset": 240, "end": 320}},
	}))
	kind, text := toolGuard("/tmp/work", messages, "read_file", json.RawMessage(`{"path":"docs/guide.md","offset":360,"limit":40}`))
	if kind != "retrieval_tail" {
		t.Fatalf("expected retrieval_tail guard, got %q", kind)
	}
	if !strings.Contains(text, "two allowed targeted read_file calls") {
		t.Fatalf("expected targeted read budget text, got %q", text)
	}
}

func TestToolGuardAllowsOneDurableGuidanceReadPastReminderBudget(t *testing.T) {
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
	}))
	kind, text := toolGuard("/tmp/work", messages, "read_file", json.RawMessage(`{"path":"spec/09-phase-plan.md","offset":0,"limit":80}`))
	if kind == "" && text == "" {
		return
	}
	t.Fatalf("expected one durable guidance read past the normal budget to be allowed, got kind=%q text=%q", kind, text)
}

func TestToolGuardBlocksSecondDurableGuidanceReadPastReminderBudget(t *testing.T) {
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
	kind, text := toolGuard("/tmp/work", messages, "read_file", json.RawMessage(`{"path":"spec/11-spec-audit-and-traceability.md","offset":0,"limit":80}`))
	if kind != "retrieval_tail" {
		t.Fatalf("expected retrieval_tail guard, got %q", kind)
	}
	if !strings.Contains(text, "maximum targeted read_file budget") {
		t.Fatalf("expected expanded-budget guard text, got %q", text)
	}
}

func TestToolGuardBlocksBroadDiscoveryAfterRetrievalReminder(t *testing.T) {
	reminder := session.NewMessage("user", "Harness reminder: stop exploring.")
	reminder.Meta = map[string]any{
		"source": "harness_reminder",
		"kind":   "retrieval_tail",
	}
	kind, text := toolGuard("/tmp/work", []session.Message{
		session.NewMessage("user", "Write reports/final-audit.md and finish."),
		reminder,
	}, "grep_files", nil)
	if kind != "retrieval_tail" {
		t.Fatalf("expected retrieval_tail guard, got %q", kind)
	}
	if !strings.Contains(text, "Broad discovery is blocked") && !strings.Contains(text, "broad discovery is blocked") {
		t.Fatalf("expected discovery guard text, got %q", text)
	}
}

func TestToolGuardBlocksReadOnlyShellAfterRetrievalReminder(t *testing.T) {
	reminder := session.NewMessage("user", "Harness reminder: stop exploring.")
	reminder.Meta = map[string]any{
		"source": "harness_reminder",
		"kind":   "retrieval_tail",
	}
	kind, text := toolGuard("/tmp/work", []session.Message{
		session.NewMessage("user", "Write reports/final-audit.md and finish."),
		reminder,
	}, "shell", json.RawMessage(`{"command":"grep -n TODO README.md"}`))
	if kind != "retrieval_tail" {
		t.Fatalf("expected retrieval_tail guard, got %q", kind)
	}
	if !strings.Contains(text, "read-only shell inspection is blocked") {
		t.Fatalf("expected shell retrieval guard text, got %q", text)
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

func TestToolGuardBlocksReadsAfterArtifactReminder(t *testing.T) {
	reminder := session.NewMessage("user", "Harness reminder: artifact already written.")
	reminder.Meta = map[string]any{
		"source": "harness_reminder",
		"kind":   "artifact_written",
	}
	kind, text := toolGuard("/tmp/work", []session.Message{
		session.NewMessage("user", "Write reports/final-audit.md and finish."),
		reminder,
	}, "grep", nil)
	if kind != "artifact_written" {
		t.Fatalf("expected artifact_written guard, got %q", kind)
	}
	if !strings.Contains(text, "artifact was already written") {
		t.Fatalf("expected artifact guard text, got %q", text)
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

func TestNextHarnessReminderAddsLargeProjectCoordinationReminder(t *testing.T) {
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
	if reminder.Kind != "large_project_coordination" {
		t.Fatalf("expected large_project_coordination reminder, got %#v", reminder)
	}
	if !strings.Contains(reminder.Text, "reports/spec.md") || !strings.Contains(reminder.Text, "todo_write plus task_create/task_update") {
		t.Fatalf("expected project-memory and task-board guidance, got %q", reminder.Text)
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

func TestToolGuardAllowsTargetedGrepForAuditProofFollowup(t *testing.T) {
	reminder := session.NewMessage("user", "Harness reminder: inspect the owning gate.")
	reminder.Meta = map[string]any{
		"source": "harness_reminder",
		"kind":   "audit_evidence",
	}
	messages := []session.Message{
		session.NewMessage("user", "Audit whether the default core tool surface stays aligned with core v1 boundaries."),
		session.NewToolMessage([]session.ToolResult{
			{
				Name: "read_file",
				Metadata: map[string]any{
					"path": filepath.Join("/tmp/work", "internal", "tools", "registry.go"),
				},
				DisplayOutput: "var reservedNames = map[string]struct{}{ \"agent_spawn\": {} }",
			},
		}),
		reminder,
	}
	kind, text := toolGuard("/tmp/work", messages, "grep", json.RawMessage(`{"path":"internal/tools/registry.go","pattern":"builtinDefinitions"}`))
	if kind != "" || text != "" {
		t.Fatalf("expected targeted grep follow-up to be allowed, got kind=%q text=%q", kind, text)
	}
}

func TestToolGuardAllowsTargetedReadForAuditProofFollowup(t *testing.T) {
	reminder := session.NewMessage("user", "Harness reminder: inspect the owning gate.")
	reminder.Meta = map[string]any{
		"source": "harness_reminder",
		"kind":   "audit_evidence",
	}
	messages := []session.Message{
		session.NewMessage("user", "Audit whether the default core tool surface stays aligned with core v1 boundaries."),
		session.NewToolMessage([]session.ToolResult{
			{
				Name: "read_file",
				Metadata: map[string]any{
					"path": filepath.Join("/tmp/work", "internal", "tools", "registry.go"),
				},
				DisplayOutput: "var reservedNames = map[string]struct{}{ \"agent_spawn\": {} }",
			},
		}),
		reminder,
	}
	kind, text := toolGuard("/tmp/work", messages, "read_file", json.RawMessage(`{"path":"internal/tools/registry.go","offset":120,"limit":80}`))
	if kind != "" || text != "" {
		t.Fatalf("expected targeted read follow-up to be allowed, got kind=%q text=%q", kind, text)
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

func TestToolGuardBlocksFinishUntilProjectMemoryRefresh(t *testing.T) {
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
	if kind != "project_memory_refresh" {
		t.Fatalf("expected project_memory_refresh guard, got %q", kind)
	}
	if !strings.Contains(text, "before finishing this large task") || !strings.Contains(text, "reports/validation.md") {
		t.Fatalf("expected finish guard text, got %q", text)
	}
}

func TestToolGuardBlocksAgentSpawnUntilProjectMemoryRefresh(t *testing.T) {
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
	if kind != "project_memory_refresh" {
		t.Fatalf("expected project_memory_refresh guard, got %q", kind)
	}
	if !strings.Contains(text, "before handing work to another agent") {
		t.Fatalf("expected agent handoff guard text, got %q", text)
	}
}

func TestToolGuardBlocksWriteFileUntilAuditProofFollowup(t *testing.T) {
	reminder := session.NewMessage("user", "Harness reminder: inspect the owning gate.")
	reminder.Meta = map[string]any{
		"source": "harness_reminder",
		"kind":   "audit_evidence",
	}
	messages := []session.Message{
		session.NewMessage("user", "Audit whether the default core tool surface stays aligned with core v1 boundaries."),
		session.NewToolMessage([]session.ToolResult{
			{
				Name: "read_file",
				Metadata: map[string]any{
					"path": filepath.Join("/tmp/work", "internal", "tools", "registry.go"),
				},
				DisplayOutput: "var reservedNames = map[string]struct{}{ \"agent_spawn\": {} }",
			},
		}),
		reminder,
	}
	kind, text := toolGuard("/tmp/work", messages, "write_file", json.RawMessage(`{"path":"reports/audit.md","content":"report"}`))
	if kind != "audit_proof" {
		t.Fatalf("expected audit_proof guard, got %q", kind)
	}
	if !strings.Contains(text, "inspect /tmp/work/internal/tools/registry.go") && !strings.Contains(text, "inspect internal/tools/registry.go") {
		t.Fatalf("expected audit proof guard text, got %q", text)
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
		session.NewMessage("user", "Audit whether the default core tool surface stays aligned with core v1 boundaries."),
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
		session.NewMessage("user", "Audit whether the default core tool surface stays aligned with core v1 boundaries."),
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

func TestToolGuardBlocksReadOnlyShellUntilAuditProofFollowup(t *testing.T) {
	reminder := session.NewMessage("user", "Harness reminder: inspect the owning gate.")
	reminder.Meta = map[string]any{
		"source": "harness_reminder",
		"kind":   "audit_evidence",
	}
	messages := []session.Message{
		session.NewMessage("user", "Audit whether the default core tool surface stays aligned with core v1 boundaries."),
		session.NewToolMessage([]session.ToolResult{
			{
				Name: "read_file",
				Metadata: map[string]any{
					"path": filepath.Join("/tmp/work", "internal", "tools", "registry.go"),
				},
				DisplayOutput: "var reservedNames = map[string]struct{}{ \"agent_spawn\": {} }",
			},
		}),
		reminder,
	}
	kind, text := toolGuard("/tmp/work", messages, "shell", json.RawMessage(`{"command":"grep -n builtinDefinitions internal/tools/registry.go"}`))
	if kind != "audit_proof" {
		t.Fatalf("expected audit_proof guard, got %q", kind)
	}
	if !strings.Contains(text, "Do not use shell grep/cat to bypass that check") {
		t.Fatalf("expected audit proof shell guard text, got %q", text)
	}
}
