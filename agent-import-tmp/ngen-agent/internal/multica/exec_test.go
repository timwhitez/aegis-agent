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
	}, prompt, ConfigResolution{Workdir: dir, Config: cfg})

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
	if !strings.Contains(tf.SuccessCriteria[1].Statement, `multica-result.md`) ||
		!strings.Contains(tf.SuccessCriteria[1].Statement, `multica issue comment add`) {
		t.Fatalf("expected result artifact to require issue comment command evidence, got %+v", tf.SuccessCriteria[1])
	}
	if !strings.Contains(tf.SuccessCriteria[2].Statement, "ngen-multica-real-e2e-ok") {
		t.Fatalf("expected marker criterion, got %+v", tf.SuccessCriteria[2])
	}
	if len(tf.Constraints) == 0 || !strings.Contains(strings.Join(tf.Constraints, "\n"), "context only") {
		t.Fatalf("expected context-only constraint, got %+v", tf.Constraints)
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
