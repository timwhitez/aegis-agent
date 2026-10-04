package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRefreshQueueJobLeaseDistinguishesSettlementFromOwnershipLoss(t *testing.T) {
	for _, tc := range []struct {
		name, status, owner, lastError string
		active, lost                   bool
	}{
		{"owned-running", QueueStatusRunning, queueProcessStartID, "", true, false},
		{"legacy-running", QueueStatusRunning, "", "", true, false},
		{"foreign-running", QueueStatusRunning, "foreign-process", "", false, true},
		{"blocked", QueueStatusBlocked, "", "", false, false},
		{"completed", QueueStatusCompleted, "", "", false, false},
		{"cancelled", QueueStatusCancelled, "", "", false, false},
		{"failed", QueueStatusFailed, "", "", false, false},
		{"reaped-blocked", QueueStatusBlocked, "", "queue lease reclaimed: owner process exited", false, true},
		{"requeued", QueueStatusQueued, "", "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := NewStore(t.TempDir())
			job := QueueJob{SchemaVersion: 1, ID: "job_lease", Status: tc.status, Prompt: "offline", Mode: ModeExec, ProcessStartID: tc.owner, LastError: tc.lastError}
			if err := store.SaveJob(job); err != nil {
				t.Fatal(err)
			}
			before, err := store.LoadJobCoordinationSnapshot(job.ID)
			if err != nil {
				t.Fatal(err)
			}
			got, active, err := store.RefreshQueueJobLease(job.ID)
			if active != tc.active || errors.Is(err, ErrQueueJobLeaseLost) != tc.lost || (!tc.lost && err != nil) {
				t.Fatalf("active=%t err=%v job=%#v", active, err, got)
			}
			after, err := store.LoadJobCoordinationSnapshot(job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.active {
				if after.HeartbeatAt == "" || after.ProcessStartID != queueProcessStartID {
					t.Fatalf("owned lease not renewed: %#v", after)
				}
			} else if after.UpdatedAt != before.UpdatedAt || after.Status != before.Status || after.ProcessStartID != before.ProcessStartID {
				t.Fatalf("observer mutated another owner's/settled fact: before=%#v after=%#v", before, after)
			}
			if tc.status != QueueStatusRunning {
				if _, err := store.RefreshQueueJobHeartbeat(job.ID); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("legacy heartbeat contract changed: %v", err)
				}
			}
		})
	}
	for _, malformed := range []bool{false, true} {
		store := NewStore(t.TempDir())
		if malformed {
			if err := store.ensureQueueDirs(); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(store.queueJobPath(QueueStatusBlocked, "job_unreadable"), []byte("{"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if _, active, err := store.RefreshQueueJobLease("job_unreadable"); active || !errors.Is(err, ErrQueueJobLeaseLost) {
			t.Fatalf("missing/unreadable fact must fail closed: active=%t err=%v", active, err)
		}
	}
	store := NewStore(t.TempDir())
	if err := store.SaveJob(QueueJob{SchemaVersion: 1, ID: "job_corrupt_running", Status: QueueStatusRunning, Prompt: "offline", Mode: ModeExec}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.queueJobPath(QueueStatusRunning, "job_corrupt_running"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, active, err := store.RefreshQueueJobLease("job_corrupt_running"); active || !errors.Is(err, ErrQueueJobLeaseLost) {
		t.Fatalf("corrupt running must fail closed: active=%t err=%v", active, err)
	}
}

func TestRefreshQueueJobLeaseUsesCanonicalSettlementOverStaleRunningCopy(t *testing.T) {
	store := NewStore(t.TempDir())
	running := QueueJob{SchemaVersion: 1, ID: "job_duplicate", Status: QueueStatusRunning, Prompt: "offline", Mode: ModeExec, ProcessStartID: queueProcessStartID}
	if err := store.SaveJob(running); err != nil {
		t.Fatal(err)
	}
	runningBytes, err := os.ReadFile(store.queueJobPath(QueueStatusRunning, running.ID))
	if err != nil {
		t.Fatal(err)
	}
	settled := running
	settled.Status, settled.ProcessStartID, settled.FinalText = QueueStatusCompleted, "", "canonical result"
	if err := store.SaveJob(settled); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.queueJobPath(QueueStatusRunning, running.ID), runningBytes, 0600); err != nil {
		t.Fatal(err)
	}
	got, active, err := store.RefreshQueueJobLease(running.ID)
	if err != nil || active || got.Status != QueueStatusCompleted || got.FinalText != "canonical result" {
		t.Fatalf("stale running copy renewed: %#v active=%t err=%v", got, active, err)
	}
	after, err := os.ReadFile(store.queueJobPath(QueueStatusRunning, running.ID))
	if err != nil || string(after) != string(runningBytes) {
		t.Fatalf("stale heartbeat was modified: %v", err)
	}
}

func TestQueueExecutionWriteRejectsUnobservedOwnershipLoss(t *testing.T) {
	for _, status := range []string{QueueStatusRunning, QueueStatusQueued, QueueStatusBlocked} {
		store := NewStore(t.TempDir())
		job := QueueJob{SchemaVersion: 1, ID: "job_foreign", Status: status, Prompt: "offline", Mode: ModeExec, ProcessStartID: "foreign-process", FinalText: "owner result"}
		if err := store.SaveJob(job); err != nil {
			t.Fatal(err)
		}
		called := false
		_, err := store.UpdateQueueJobAfterExecution(job.ID, QueueExecutionSettlement, func(current *QueueJob) { called = true; current.Status = QueueStatusFailed })
		if called || !errors.Is(err, ErrQueueJobLeaseLost) {
			t.Fatalf("foreign callback executed: status=%s called=%t err=%v", status, called, err)
		}
		current, err := store.LoadJobCoordinationSnapshot(job.ID)
		if err != nil || current.Status != status || current.FinalText != "owner result" || current.ProcessStartID != "foreign-process" {
			t.Fatalf("foreign fact overwritten: %#v err=%v", current, err)
		}
	}
	store := NewStore(t.TempDir())
	if err := store.UpdateQueueJobEffectiveBudget("job_missing", nil); !errors.Is(err, ErrQueueJobLeaseLost) {
		t.Fatalf("missing budget fact permits resurrection: %v", err)
	}
	if _, err := store.UpdateQueueJobAfterExecution("job_missing", QueueExecutionRollback, func(*QueueJob) { t.Fatal("missing fact callback executed") }); !errors.Is(err, ErrQueueJobLeaseLost) {
		t.Fatalf("missing settlement not rejected: %v", err)
	}
}

func TestQueueExecutionRollbackCannotReverseSettledResult(t *testing.T) {
	for _, status := range []string{QueueStatusBlocked, QueueStatusCompleted, QueueStatusCancelled, QueueStatusFailed} {
		store := NewStore(t.TempDir())
		job := QueueJob{SchemaVersion: 1, ID: "job_settled", Status: status, Prompt: "offline", Mode: ModeExec, SessionID: "child", SessionStatus: StatusCompleted, FinalText: "durable result"}
		switch status {
		case QueueStatusBlocked:
			job.SessionStatus = StatusPaused
		case QueueStatusCancelled:
			job.SessionStatus = StatusCancelled
		case QueueStatusFailed:
			job.SessionStatus = StatusFailed
		}
		if err := store.SaveJob(job); err != nil {
			t.Fatal(err)
		}
		if _, err := store.UpdateQueueJobAfterExecution(job.ID, QueueExecutionRollback, func(*QueueJob) { t.Fatal("settled rollback callback ran") }); !errors.Is(err, ErrQueueJobLeaseLost) {
			t.Fatalf("settled rollback not rejected: %v", err)
		}
		if status == QueueStatusBlocked {
			continue
		}
		got, err := store.UpdateQueueJobAfterExecution(job.ID, QueueExecutionSettlement, func(current *QueueJob) {
			current.Status, current.SessionStatus, current.FinalText = QueueStatusBlocked, StatusPaused, "stale result"
			current.EffectiveWorkdir = "/output"
		})
		if err != nil || got.Status != status || got.SessionStatus != job.SessionStatus || got.FinalText != "durable result" || got.EffectiveWorkdir != "/output" {
			t.Fatalf("settled outcome reversed or enrichment lost: %#v err=%v", got, err)
		}
	}
}

func TestQueueExecutionFailedHandoffPreservesOwnershipAndChildIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, status, owner, child, failure string
		allowed, lost                       bool
	}{
		{"same-child-output-failure", QueueStatusCompleted, "", "child", "sync outputs failed", true, false},
		{"different-child", QueueStatusCompleted, "", "other-child", "sync outputs failed", false, false},
		{"no-handoff-error", QueueStatusCompleted, "", "child", "", false, false},
		{"cancelled", QueueStatusCancelled, "", "child", "sync outputs failed", false, false},
		{"foreign-owner", QueueStatusCompleted, "foreign-process", "child", "sync outputs failed", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := NewStore(t.TempDir())
			job := QueueJob{SchemaVersion: 1, ID: "job_handoff", Status: tc.status, Prompt: "offline", Mode: ModeExec, SessionID: "child", SessionStatus: StatusCompleted, ProcessStartID: tc.owner, FinalText: "child result"}
			if tc.status == QueueStatusCancelled {
				job.SessionStatus = StatusCancelled
			}
			if err := store.SaveJob(job); err != nil {
				t.Fatal(err)
			}
			got, err := store.UpdateQueueJobAfterExecution(job.ID, QueueExecutionFailedHandoff, func(current *QueueJob) {
				current.Status, current.SessionID, current.LastError = QueueStatusFailed, tc.child, tc.failure
			})
			if errors.Is(err, ErrQueueJobLeaseLost) != tc.lost || (!tc.lost && err != nil) {
				t.Fatalf("handoff guard: %v", err)
			}
			if tc.lost {
				return
			}
			if tc.allowed {
				if got.Status != QueueStatusFailed || got.LastError != tc.failure || got.SessionID != "child" {
					t.Fatalf("real handoff failure suppressed: %#v", got)
				}
			} else if got.Status != job.Status || got.SessionID != "child" || got.LastError != "" {
				t.Fatalf("terminal facts reversed: %#v", got)
			}
		})
	}
}

func TestUpdateQueueJobEffectiveBudgetWaitsForClaimAndPreservesLatestFacts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	claimStore, budgetStore := NewStore(root), NewStore(root)
	job := QueueJob{SchemaVersion: 1, ID: "job_budget", Status: QueueStatusQueued, Prompt: "offline", Mode: ModeExec}
	if err := claimStore.EnqueueJob(job); err != nil {
		t.Fatal(err)
	}
	paused, release := make(chan struct{}), make(chan struct{})
	claimStore.beforeQueueClaimLeaseWrite = func(_, _ string, _ QueueJob) error { close(paused); <-release; return nil }
	claimDone := make(chan error, 1)
	go func() { _, _, err := claimStore.ClaimNextQueuedJob(); claimDone <- err }()
	select {
	case <-paused:
	case <-time.After(2 * time.Second):
		t.Fatal("claim did not reach lease write")
	}
	budget := NewEffectiveBudget(BudgetSourceRuntimeChild, 3, 0, 0, 0, time.Now().UTC())
	budgetDone := make(chan error, 1)
	go func() { budgetDone <- budgetStore.UpdateQueueJobEffectiveBudget(job.ID, budget) }()
	select {
	case err := <-budgetDone:
		close(release)
		t.Fatalf("budget escaped durable claim lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-claimDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("claim did not finish")
	}
	select {
	case err := <-budgetDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("budget did not finish")
	}
	current, err := claimStore.LoadJobCoordinationSnapshot(job.ID)
	if err != nil || current.Status != QueueStatusRunning || current.HeartbeatAt == "" || current.ProcessStartID != queueProcessStartID || current.EffectiveBudget == nil || current.EffectiveBudget.MaxTurnsPerAttempt != 3 {
		t.Fatalf("claim/budget facts lost: %#v err=%v", current, err)
	}
	// A later owner may settle the job before the next mirror. Budget writes
	// must preserve that current status, owner and result rather than resurrect it.
	current.Status, current.ProcessStartID, current.FinalText = QueueStatusCompleted, "later-owner", "durable result"
	if err := claimStore.SaveJob(current); err != nil {
		t.Fatal(err)
	}
	if err := budgetStore.UpdateQueueJobEffectiveBudget(job.ID, nil); err != nil {
		t.Fatal(err)
	}
	after, err := claimStore.LoadJobCoordinationSnapshot(job.ID)
	if err != nil || after.Status != QueueStatusCompleted || after.ProcessStartID != "later-owner" || after.FinalText != "durable result" || after.HeartbeatAt != current.HeartbeatAt || after.EffectiveBudget != nil {
		t.Fatalf("latest facts overwritten: %#v err=%v", after, err)
	}
}

func TestQueueResumeProvisionalClaimRequiresDurableProof(t *testing.T) {
	now := time.Now().UTC()
	job := QueueJob{Status: QueueStatusRunning, SessionStatus: StatusRunning, ParentSessionID: "parent", SessionID: "child", ClaimedBy: "agent_prompt:parent", ClaimedAt: now.Add(-time.Second).Format(time.RFC3339Nano), HeartbeatAt: now.Format(time.RFC3339Nano), ProcessStartID: "another-reader-process", WorkerPID: 1}
	state := State{Status: StatusPaused, UpdatedAt: now.Add(-2 * time.Second).Format(time.RFC3339Nano)}
	if !queueResumeClaimIsPendingChildStart(job, state, now) {
		t.Fatal("valid cross-process provisional claim not recognized")
	}
	for _, change := range []func(*QueueJob, *State){
		func(j *QueueJob, _ *State) { j.ClaimedBy = "worker:ordinary" },
		func(j *QueueJob, _ *State) { j.ClaimedAt = "invalid" },
		func(_ *QueueJob, s *State) { s.UpdatedAt = "" },
		func(_ *QueueJob, s *State) { s.UpdatedAt = now.Format(time.RFC3339Nano) },
		func(j *QueueJob, _ *State) {
			j.HeartbeatAt = now.Add(-queueRunningStaleAfter - time.Second).Format(time.RFC3339Nano)
		},
		func(j *QueueJob, _ *State) {
			j.Status = QueueStatusBlocked
			j.LastError = "queue lease reclaimed: owner process exited"
		},
		func(j *QueueJob, _ *State) { j.Status = QueueStatusBlocked },
	} {
		changedJob, changedState := job, state
		change(&changedJob, &changedState)
		if queueResumeClaimIsPendingChildStart(changedJob, changedState, now) {
			t.Fatalf("invented provisional proof: job=%#v state=%#v", changedJob, changedState)
		}
	}
}
