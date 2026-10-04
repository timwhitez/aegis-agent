package runtime

import (
	"errors"
	"testing"
	"time"

	"aegis-agent/internal/session"
)

func TestQueueJobLeaseIsLostForReaperBlockedOutcome(t *testing.T) {
	store := session.NewStore(t.TempDir())
	now := time.Now().UTC().Format(time.RFC3339Nano)
	job := session.QueueJob{
		SchemaVersion: 1,
		ID:            "job_reaper_blocked",
		CreatedAt:     now,
		UpdatedAt:     now,
		Status:        session.QueueStatusBlocked,
		Prompt:        "bounded task",
		Mode:          session.ModeExec,
		SessionID:     "child_reaper_blocked",
		SessionStatus: session.StatusRunning,
		LastError:     "queue lease reclaimed: owner process exited",
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save reaped job: %v", err)
	}
	_, active, err := store.RefreshQueueJobLease(job.ID)
	if active || !errors.Is(err, session.ErrQueueJobLeaseLost) {
		t.Fatalf("reaper-blocked job must cancel the stale worker: active=%t err=%v", active, err)
	}
}
