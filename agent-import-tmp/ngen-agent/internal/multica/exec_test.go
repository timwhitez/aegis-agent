package multica

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ngen/internal/task"
)

func TestTaskFromEnvelopeTurnsMulticaIssueAssignmentIntoCommandBackedCodingTask(t *testing.T) {
	dir := t.TempDir()
	writeMulticaTestFile(t, filepath.Join(dir, ".agent_context", "issue_context.md"), "# Issue\n\nAcceptance: add marker ngen-multica-real-e2e-ok as an issue comment.\n")
	issueID := "0496acb9-ff48-4507-bb79-d122a68c3a98"
	prompt := "You have been assigned issue ID: " + issueID
	cfg := task.DefaultConfig()
	cfg.Permission.DefaultMode = task.PermissionModeYolo

	tf := taskFromEnvelope(StreamInputMessage{
		Protocol:        ProtocolName,
		ProtocolVersion: ProtocolVersion,
		Type:            "user",
		Role:            "user",
		Content:         []ContentBlock{{Type: "text", Text: prompt}},
		SystemPrompt:    "Use injected workspace guidance.",
		Metadata:        map[string]string{"issue_id": issueID},
	}, prompt, ConfigResolution{Workdir: dir, Config: cfg}, "worker")

	if tf.Kind != task.KindCoding || tf.PresetID != "" {
		t.Fatalf("expected Multica issue assignment to become coding task, got kind=%s preset=%s", tf.Kind, tf.PresetID)
	}
	if !strings.Contains(tf.Objective, "Multica issue execution mode") ||
		!strings.Contains(tf.Objective, "multica issue get "+issueID+" --output json") ||
		!strings.Contains(tf.Objective, "ngen-multica-real-e2e-ok") ||
		!strings.Contains(tf.Objective, "Original Multica assignment") {
		t.Fatalf("objective did not preserve Multica issue execution guidance:\n%s", tf.Objective)
	}
	if len(tf.SuccessCriteria) != 3 {
		t.Fatalf("expected read command, comment evidence, and marker criteria, got %+v", tf.SuccessCriteria)
	}
	if !strings.Contains(tf.SuccessCriteria[0].Statement, "`multica issue get "+issueID+" --output json` passes") {
		t.Fatalf("expected command-backed issue read criterion, got %+v", tf.SuccessCriteria[0])
	}
	if !strings.Contains(tf.SuccessCriteria[1].Statement, `repair command record`) ||
		!strings.Contains(tf.SuccessCriteria[1].Statement, `multica issue comment add`) {
		t.Fatalf("expected command-backed issue comment evidence criterion, got %+v", tf.SuccessCriteria[1])
	}
	if !strings.Contains(tf.SuccessCriteria[2].Statement, "ngen-multica-real-e2e-ok") {
		t.Fatalf("expected marker criterion, got %+v", tf.SuccessCriteria[2])
	}
	if len(tf.Constraints) == 0 || !strings.Contains(strings.Join(tf.Constraints, "\n"), "context only") {
		t.Fatalf("expected context-only constraint, got %+v", tf.Constraints)
	}
	if got := runModeForObjective(tf.Objective, false); got != "run" {
		t.Fatalf("expected new Multica issue task to use direct run mode, got %s", got)
	}
	if got := runModeForObjective(tf.Objective, true); got != "resume" {
		t.Fatalf("expected resumed Multica issue task to use direct resume mode, got %s", got)
	}
	if got := runModeForObjective("ordinary task", false); got != "auto" {
		t.Fatalf("expected ordinary task to keep auto mode, got %s", got)
	}
}

func TestTaskFromEnvelopeExtractsSquadMarkersAndLeaderSchedulingCriteria(t *testing.T) {
	dir := t.TempDir()
	writeMulticaTestFile(t, filepath.Join(dir, ".agent_context", "issue_context.md"), strings.Join([]string{
		"# Issue",
		"",
		"Required final marker: ngen-squad-long-e2e-ok-20260609",
		"Worker marker: ngen-squad-worker-e2e-ok-20260609",
		"Validator marker: ngen-squad-validator-e2e-ok-20260609",
	}, "\n"))
	writeMulticaTestFile(t, filepath.Join(dir, "AGENTS.md"), strings.Join([]string{
		"# Runtime",
		"",
		"## Task Coordination Guidance",
		"- Run role: `leader`",
	}, "\n"))
	issueID := "bbda949c-1a3d-4c37-a42d-1367ce702d75"
	prompt := "You have been assigned issue ID: " + issueID

	tf := taskFromEnvelope(StreamInputMessage{
		Protocol:        ProtocolName,
		ProtocolVersion: ProtocolVersion,
		Type:            "user",
		Role:            "user",
		Content:         []ContentBlock{{Type: "text", Text: prompt}},
		Metadata:        map[string]string{"issue_id": issueID},
	}, prompt, ConfigResolution{Workdir: dir, Config: task.DefaultConfig()}, "")

	criteriaText := strings.Join(func() []string {
		var out []string
		for _, criterion := range tf.SuccessCriteria {
			out = append(out, criterion.Statement)
		}
		return out
	}(), "\n")
	if strings.Contains(criteriaText, `exact Multica issue marker "ngen-squad-long-e2e-ok-20260609"`) {
		t.Fatalf("leader dispatch criteria must not require final marker before delegated roles complete:\n%s", criteriaText)
	}
	if !strings.Contains(criteriaText, "issue run/delegation evidence") {
		t.Fatalf("expected leader criteria to require automatic worker/validator run evidence:\n%s", criteriaText)
	}
	if !strings.Contains(tf.Objective, "Multica run role: leader") {
		t.Fatalf("expected AGENTS.md role hint to be preserved in objective:\n%s", tf.Objective)
	}
	if !strings.Contains(strings.Join(tf.Constraints, "\n"), "squad delegation") {
		t.Fatalf("expected leader constraints to allow issue-scoped squad delegation, got %+v", tf.Constraints)
	}
	if got := runModeForObjective(tf.Objective, false); got != "run" {
		t.Fatalf("expected leader issue task file to bypass auto turn limits with direct run mode, got %s", got)
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
