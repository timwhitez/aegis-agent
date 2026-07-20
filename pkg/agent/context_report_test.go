package agent

import (
	"testing"
	"time"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/session"
)

func TestRunnerContextExposesVersionedReadOnlyReport(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = t.TempDir()
	store := session.NewStore(cfg.Session.Dir)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "sdk-context-root",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "fake",
		Model:            "fixture",
		CompletionPolicy: session.CompletionPolicyAutonomous,
		RootSessionID:    "sdk-context-root",
	}
	if err := store.Create(meta, session.State{Status: session.StatusCompleted, Phase: "complete", UpdatedAt: meta.CreatedAt}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	report, err := New(cfg).Context(meta.ID)
	if err != nil {
		t.Fatalf("context: %v", err)
	}
	if report.SchemaVersion != session.ContextReportSchemaVersion || report.RequestedSessionID != meta.ID || report.RootSessionID != meta.ID {
		t.Fatalf("unexpected SDK report: %#v", report)
	}
}
