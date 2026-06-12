package multica

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ngenrt "ngen/internal/runtime"
	"ngen/internal/task"
)

func TestReadInputEnvelopeAcceptsTopLevelAndNestedUserText(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "top_level_ngen_shape",
			input: `{"protocol":"ngen-stream-json","protocol_version":1,"type":"user","role":"user","content":[{"type":"text","text":"hello"},{"type":"text","text":"world"}],"system_prompt":"ignored","metadata":{"issue_id":"ignored"}}`,
			want:  "hello\nworld",
		},
		{
			name:  "nested_go_cli_shape",
			input: `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"},{"type":"text","text":"world"}]},"system_prompt":"ignored","metadata":{"type":"quick_create"}}`,
			want:  "hello\nworld",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envelope, err := readInputEnvelope(strings.NewReader(tc.input))
			if err != nil {
				t.Fatalf("read input envelope: %v", err)
			}
			if got := envelopeText(envelope); got != tc.want {
				t.Fatalf("unexpected prompt %q", got)
			}
		})
	}
}

func TestTaskFromEnvelopePassesThroughUserTextOnly(t *testing.T) {
	dir := t.TempDir()
	squadID := "3b0ca27f-5db0-42ff-98ea-7750fc40500a"
	projectID := "ae886a17-0ef6-4b02-b154-3ac601df7239"
	issueID := "0496acb9-ff48-4507-bb79-d122a68c3a98"
	writeMulticaTestFile(t, filepath.Join(dir, ".agent_context", "issue_context.md"), strings.Join([]string{
		"# Injected Context",
		"",
		"Existing issue: " + issueID,
		"Required marker: ngen-multica-real-e2e-ok",
	}, "\n"))
	writeMulticaTestFile(t, filepath.Join(dir, "AGENTS.md"), strings.Join([]string{
		"# Runtime",
		"",
		"Multica run role: leader.",
		"Delegation boundary: `quick-create-issue`.",
		"Expected public artifacts: `created_issue`.",
	}, "\n"))
	prompt := strings.Join([]string{
		"You are running as a quick-create assistant for a Multica workspace.",
		"",
		"User input:",
		"> 请分析研究设计并逐步开发一个 Web First 的智能渗透测试系统。",
		"",
		"Field rules:",
		"- pass `--assignee-id \"" + squadID + "\"`.",
		"- pass `--project \"" + projectID + "\"`.",
		"",
		"Output format:",
		"- Run exactly one `multica issue create --output json` invocation.",
	}, "\n")

	tf := taskFromEnvelope(StreamInputMessage{
		Protocol:        ProtocolName,
		ProtocolVersion: ProtocolVersion,
		Type:            "user",
		Role:            "user",
		Content:         []ContentBlock{{Type: "text", Text: prompt}},
	}, prompt, ConfigResolution{Workdir: dir, Config: task.DefaultConfig()}, "leader")

	if tf.Objective != prompt {
		t.Fatalf("expected objective to be user text only, got:\n%s", tf.Objective)
	}
	for _, forbidden := range []string{
		"System prompt:",
		"Multica metadata:",
		"Multica quick-create mode.",
		"Multica issue execution mode",
		"multica issue get " + squadID,
		"multica issue get " + issueID,
		"multica issue comment add " + issueID,
		"Assignee ID:",
		"Project ID:",
		"ngen-multica-real-e2e-ok",
	} {
		if strings.Contains(tf.Objective, forbidden) {
			t.Fatalf("objective should not contain adapter-synthesized %q:\n%s", forbidden, tf.Objective)
		}
	}
	if len(tf.Constraints) != 0 {
		t.Fatalf("expected no adapter-synthesized constraints, got %+v", tf.Constraints)
	}
	if tf.Kind != task.KindGeneral || tf.PresetID != task.PresetDocsLite {
		t.Fatalf("expected explicit command prompt outside code repo to stay generic docs_lite, got kind=%s preset=%s", tf.Kind, tf.PresetID)
	}
	if len(tf.SuccessCriteria) != 1 || !strings.Contains(tf.SuccessCriteria[0].Statement, "completed repair command record") || !strings.Contains(tf.SuccessCriteria[0].Statement, "explicit user-requested command") {
		t.Fatalf("expected generic command-backed criterion from user text, got %+v", tf.SuccessCriteria)
	}
	if got := runModeForObjective(tf.Objective, false); got != "auto" {
		t.Fatalf("expected pass-through objective to use auto mode, got %s", got)
	}
}

func TestTaskFromEnvelopeRecognizesQuickCreateDoNotRetryCommand(t *testing.T) {
	dir := t.TempDir()
	prompt := strings.Join([]string{
		"You are running as a quick-create assistant for a Multica workspace.",
		"",
		"Output format:",
		"- Run exactly one `multica issue create --output json` invocation. Do not retry for any reason — even on non-zero exit.",
	}, "\n")

	tf := taskFromEnvelope(StreamInputMessage{
		Type:    "user",
		Role:    "user",
		Content: []ContentBlock{{Type: "text", Text: prompt}},
	}, prompt, ConfigResolution{Workdir: dir, Config: task.DefaultConfig()}, "")

	if len(tf.SuccessCriteria) != 1 || !strings.Contains(tf.SuccessCriteria[0].Statement, "completed repair command record") {
		t.Fatalf("expected command-backed criterion despite do-not-retry guard, got %+v", tf.SuccessCriteria)
	}
}

func TestTaskFromEnvelopeTreatsChineseActionPromptAsCoding(t *testing.T) {
	dir := t.TempDir()
	writeMulticaTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/action\n\ngo 1.24.0\n")
	tf := taskFromEnvelope(StreamInputMessage{
		Type:    "user",
		Role:    "user",
		Content: []ContentBlock{{Type: "text", Text: "请分析研究设计并逐步开发一个 Web First 的智能渗透测试系统。"}},
	}, "请分析研究设计并逐步开发一个 Web First 的智能渗透测试系统。", ConfigResolution{Workdir: dir, Config: task.DefaultConfig()}, "leader")
	if tf.Kind != task.KindCoding || tf.PresetID != "" {
		t.Fatalf("expected Chinese action prompt to become coding task, got kind=%s preset=%s", tf.Kind, tf.PresetID)
	}
	if len(tf.SuccessCriteria) != 1 || !strings.Contains(tf.SuccessCriteria[0].Statement, "Produce a verifiable handoff/result") {
		t.Fatalf("expected generic handoff criterion, got %+v", tf.SuccessCriteria)
	}
}

func TestTaskFromEnvelopeAssignmentRequiresConcreteProgressEvidence(t *testing.T) {
	dir := t.TempDir()
	issueID := "95ab526f-ba4a-42a6-a644-7bfd96facb70"
	prompt := strings.Join([]string{
		"You are running as a local coding agent for a Multica workspace.",
		"",
		"Your assigned issue ID is: " + issueID,
		"",
		"Start by running `multica issue get " + issueID + " --output json` to understand your task, then complete it.",
		"For comment history, follow the rule in your runtime workflow file (assignment-triggered tasks treat the read as mandatory). `multica issue comment list " + issueID + " --output json` returns all comments for the issue.",
	}, "\n")

	tf := taskFromEnvelope(StreamInputMessage{
		Type:    "user",
		Role:    "user",
		Content: []ContentBlock{{Type: "text", Text: prompt}},
	}, prompt, ConfigResolution{Workdir: dir, Config: task.DefaultConfig()}, "leader")

	if tf.Kind != task.KindGeneral || tf.PresetID != task.PresetDocsLite {
		t.Fatalf("expected assignment outside code repo to stay general docs_lite, got kind=%s preset=%s", tf.Kind, tf.PresetID)
	}
	if len(tf.SuccessCriteria) != 1 || !strings.Contains(tf.SuccessCriteria[0].Statement, "Concrete execution progress is recorded") || !strings.Contains(tf.SuccessCriteria[0].Statement, "result prose alone is not sufficient") {
		t.Fatalf("expected concrete-progress criterion for assignment prompt, got %+v", tf.SuccessCriteria)
	}
}

func TestTaskFromEnvelopePublishArtifactsBecomesPathCriteria(t *testing.T) {
	dir := t.TempDir()
	prompt := strings.Join([]string{
		"You are running as a local coding agent for a Multica workspace.",
		"",
		"[NEW COMMENT] A user just left a new comment. Focus on THIS comment:",
		"> Instructions: Initial worker slice: create the durable mission brief and first implementation-slice plan, not a final completion claim.",
		"Inspect issue/project resources and comments; if no target repo/worktree exists, record that blocker.",
		"Decompose the next SPEC -> failing test -> minimal implementation -> real E2E verification -> regression -> docs -> commit sequence, and publish reports/mission-plan.md plus progress/mission-status.md and handoffs/cybersec-long-horizon.v1.json with evidence and next-owner fields.",
	}, "\n")

	tf := taskFromEnvelope(StreamInputMessage{
		Type:    "user",
		Role:    "user",
		Content: []ContentBlock{{Type: "text", Text: prompt}},
	}, prompt, ConfigResolution{Workdir: dir, Config: task.DefaultConfig()}, "worker")

	if tf.Kind != task.KindGeneral || tf.PresetID != task.PresetDocsLite {
		t.Fatalf("expected artifact worker outside code repo to stay general docs_lite, got kind=%s preset=%s", tf.Kind, tf.PresetID)
	}
	want := []string{
		"Workspace artifact reports/mission-plan.md exists.",
		"Workspace artifact progress/mission-status.md exists.",
		"Workspace artifact handoffs/cybersec-long-horizon.v1.json exists.",
	}
	if len(tf.SuccessCriteria) != len(want) {
		t.Fatalf("expected %d artifact criteria, got %+v", len(want), tf.SuccessCriteria)
	}
	for i := range want {
		if tf.SuccessCriteria[i].ID != fmt.Sprintf("SC-%03d", i+1) || tf.SuccessCriteria[i].Statement != want[i] {
			t.Fatalf("criterion %d mismatch: got %+v want id=%s statement=%q", i, tf.SuccessCriteria[i], fmt.Sprintf("SC-%03d", i+1), want[i])
		}
	}
}

func TestTaskFromEnvelopeKeepsActionPromptGeneralOutsideCodeRepo(t *testing.T) {
	dir := t.TempDir()
	tf := taskFromEnvelope(StreamInputMessage{
		Type:    "user",
		Role:    "user",
		Content: []ContentBlock{{Type: "text", Text: "请分析研究设计并逐步开发一个 Web First 的智能渗透测试系统。"}},
	}, "请分析研究设计并逐步开发一个 Web First 的智能渗透测试系统。", ConfigResolution{Workdir: dir, Config: task.DefaultConfig()}, "leader")
	if tf.Kind != task.KindGeneral || tf.PresetID != task.PresetDocsLite {
		t.Fatalf("expected non-code workspace action prompt to stay generic docs_lite, got kind=%s preset=%s", tf.Kind, tf.PresetID)
	}
	if len(tf.SuccessCriteria) != 1 || !strings.Contains(tf.SuccessCriteria[0].Statement, "Produce a verifiable handoff/result") {
		t.Fatalf("expected generic handoff criterion, got %+v", tf.SuccessCriteria)
	}
}

func TestResultMessageSetsTopLevelResultFromHandoff(t *testing.T) {
	dir := t.TempDir()
	svc := ngenrt.New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "handoff result",
		Objective: "summarize",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "Produce a result."},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := svc.Store.SaveHandoff(spec.TaskID, []byte("# Handoff\n\nFinal visible result text.\n")); err != nil {
		t.Fatalf("save handoff: %v", err)
	}

	msg := resultMessage(task.StatusSnapshot{
		TaskID: spec.TaskID,
		State:  task.StateDone,
		Phase:  task.PhaseReview,
	}, "", task.MulticaRunMetadata{}, ConfigResolution{EffectiveModel: EffectiveModel{Route: "builtin/default"}}, "completed", nil, svc)

	if msg.Type != "result" || msg.Result != "Final visible result text." {
		t.Fatalf("expected top-level result from handoff summary, got %+v", msg)
	}
	if msg.Message != nil {
		t.Fatalf("result should not synthesize assistant message blocks, got %+v", msg.Message)
	}
}

func writeMulticaTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
}
