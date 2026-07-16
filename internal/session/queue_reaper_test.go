package session

import (
	"os"
	"testing"
	"time"
)

func reaperTestStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	return NewStore(root)
}

func reaperParentMeta(t *testing.T, store *Store) SessionMetadata {
	t.Helper()
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	return meta
}

func reaperChildSession(t *testing.T, store *Store, parentID, status string) SessionMetadata {
	t.Helper()
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "fake",
		Model:            "fake",
		ParentSessionID:  parentID,
		RootSessionID:    parentID,
		Depth:            1,
		CompletionPolicy: CompletionPolicyAutonomous,
	}
	state := State{Status: status, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create child: %v", err)
	}
	return meta
}

// deadOwnerJob builds a running/blocked job whose owning process is a PID that is
// definitely not alive, so the reaper treats it as an orphan.
func deadOwnerJob(id, parentID, sessionID, queueStatus, sessionStatus string) QueueJob {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	job := QueueJob{
		SchemaVersion:   1,
		ID:              id,
		CreatedAt:       now,
		UpdatedAt:       now,
		Status:          queueStatus,
		ClaimedBy:       "process:999999:" + now,
		ClaimedAt:       now,
		HeartbeatAt:     now,
		WorkerPID:       999999,
		ProcessStartID:  "999999:" + now,
		ParentSessionID: parentID,
		RootSessionID:   parentID,
		AgentName:       "audit",
		AgentRole:       "generator",
		Prompt:          "audit something",
		Mode:            ModeExec,
		Background:      true,
		SessionID:       sessionID,
		SessionStatus:   sessionStatus,
	}
	return job
}

func seedParentJob(t *testing.T, store *Store, parentID string, job QueueJob) {
	t.Helper()
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save job %s: %v", job.ID, err)
	}
	if _, _, err := store.MutateParentCoordination(parentID, func(c *ParentCoordination) error {
		if c.ParentSessionID == "" {
			c.SchemaVersion = 1
			c.ParentSessionID = parentID
			c.WaitMode = "wait-all"
			c.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		}
		c.UnresolvedQueueJobs = appendUniqueString(c.UnresolvedQueueJobs, job.ID)
		c.Parked = true
		return nil
	}); err != nil {
		t.Fatalf("seed coordination for %s: %v", job.ID, err)
	}
}

func TestReapStaleQueueJobsRequeuesPreStartOrphan(t *testing.T) {
	store := reaperTestStore(t)
	parent := reaperParentMeta(t, store)
	// Orphan with no child session yet (crashed before Start) -> requeue.
	job := deadOwnerJob("job_prestart", parent.ID, "", QueueStatusRunning, "")
	seedParentJob(t, store, parent.ID, job)

	result, err := store.ReapStaleQueueJobs(time.Minute)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(result.Requeued) != 1 || result.Requeued[0] != "job_prestart" {
		t.Fatalf("expected job_prestart requeued, got %#v", result)
	}
	reloaded, err := store.LoadJob("job_prestart")
	if err != nil {
		t.Fatalf("load job: %v", err)
	}
	if reloaded.Status != QueueStatusQueued {
		t.Fatalf("expected queued, got %s", reloaded.Status)
	}
	if reloaded.ProcessStartID != "" || reloaded.WorkerPID != 0 {
		t.Fatalf("expected cleared lease, got pid=%d start=%q", reloaded.WorkerPID, reloaded.ProcessStartID)
	}
}

func TestReapStaleQueueJobsSettlesTerminalChild(t *testing.T) {
	store := reaperTestStore(t)
	parent := reaperParentMeta(t, store)
	child := reaperChildSession(t, store, parent.ID, StatusCompleted)
	job := deadOwnerJob("job_done", parent.ID, child.ID, QueueStatusRunning, "")
	seedParentJob(t, store, parent.ID, job)

	result, err := store.ReapStaleQueueJobs(time.Minute)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(result.Completed) != 1 {
		t.Fatalf("expected one completed, got %#v", result)
	}
	reloaded, err := store.LoadJob("job_done")
	if err != nil {
		t.Fatalf("load job: %v", err)
	}
	if reloaded.Status != QueueStatusCompleted {
		t.Fatalf("expected completed, got %s", reloaded.Status)
	}
	coordination, err := store.LoadParentCoordination(parent.ID)
	if err != nil {
		t.Fatalf("load coordination: %v", err)
	}
	if sliceHasString(coordination.UnresolvedQueueJobs, "job_done") {
		t.Fatalf("expected job removed from unresolved, got %#v", coordination.UnresolvedQueueJobs)
	}
	if coordination.Parked {
		t.Fatalf("expected parent unparked after settling its only job")
	}
}

func TestReapStaleQueueJobsSettlesCancelledChild(t *testing.T) {
	store := reaperTestStore(t)
	parent := reaperParentMeta(t, store)
	child := reaperChildSession(t, store, parent.ID, StatusCancelled)
	job := deadOwnerJob("job_cancelled", parent.ID, child.ID, QueueStatusRunning, StatusRunning)
	seedParentJob(t, store, parent.ID, job)

	result, err := store.ReapStaleQueueJobs(time.Minute)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(result.Cancelled) != 1 || result.Cancelled[0] != job.ID {
		t.Fatalf("expected cancelled settlement, got %#v", result)
	}
	reloaded, err := store.LoadJob(job.ID)
	if err != nil || reloaded.Status != QueueStatusCancelled || reloaded.SessionStatus != StatusCancelled {
		t.Fatalf("unexpected cancelled reaped job: job=%#v err=%v", reloaded, err)
	}
	coordination, err := store.LoadParentCoordination(parent.ID)
	if err != nil || sliceHasString(coordination.UnresolvedQueueJobs, job.ID) || !sliceHasString(coordination.CancelledQueueJobs, job.ID) || coordination.Parked {
		t.Fatalf("cancelled reaped job did not resolve parent: coordination=%#v err=%v", coordination, err)
	}
}

func TestReapStaleQueueJobsAppliesPendingCancelRequestAfterOwnerExit(t *testing.T) {
	store := reaperTestStore(t)
	parent := reaperParentMeta(t, store)
	child := reaperChildSession(t, store, parent.ID, StatusRunning)
	job := deadOwnerJob("job_pending_cancel", parent.ID, child.ID, QueueStatusRunning, StatusRunning)
	child.QueueJobID = job.ID
	if err := store.SaveMetadata(child.ID, child); err != nil {
		t.Fatalf("link child metadata to job: %v", err)
	}
	seedParentJob(t, store, parent.ID, job)
	request, created, err := store.RequestSessionCancel(child.ID, parent.ID, job.ID, "agent_cancel_requested")
	if err != nil || !created {
		t.Fatalf("request cancellation: request=%#v created=%t err=%v", request, created, err)
	}

	result, err := store.ReapStaleQueueJobs(time.Minute)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(result.Cancelled) != 1 || result.Cancelled[0] != job.ID {
		t.Fatalf("pending cancel request did not converge: %#v", result)
	}
	state, err := store.LoadState(child.ID)
	if err != nil || state.Status != StatusCancelled {
		t.Fatalf("child session was not cancelled: state=%#v err=%v", state, err)
	}
	request, err = store.LoadSessionCancel(child.ID)
	if err != nil || request.Status != CancelRequestStatusApplied || request.AppliedAt == "" {
		t.Fatalf("cancel request was not marked applied: request=%#v err=%v", request, err)
	}
	eventsList, err := store.LoadEvents(child.ID)
	if err != nil {
		t.Fatalf("load child events: %v", err)
	}
	cancelledEvents := 0
	for _, event := range eventsList {
		if event.Type == "session.cancelled" {
			cancelledEvents++
		}
	}
	if cancelledEvents != 1 {
		t.Fatalf("expected one recovered session.cancelled event, got %d events=%#v", cancelledEvents, eventsList)
	}
}

func TestReapStaleQueueJobsBlocksNonTerminalOrphanAndNotifiesParent(t *testing.T) {
	store := reaperTestStore(t)
	parent := reaperParentMeta(t, store)
	child := reaperChildSession(t, store, parent.ID, StatusPaused)
	// Owner dead, child still resumable -> blocked + parent notification.
	job := deadOwnerJob("job_paused", parent.ID, child.ID, QueueStatusRunning, StatusPaused)
	seedParentJob(t, store, parent.ID, job)

	result, err := store.ReapStaleQueueJobs(time.Minute)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(result.Blocked) != 1 {
		t.Fatalf("expected one blocked, got %#v", result)
	}
	reloaded, err := store.LoadJob("job_paused")
	if err != nil {
		t.Fatalf("load job: %v", err)
	}
	if reloaded.Status != QueueStatusBlocked {
		t.Fatalf("expected blocked, got %s", reloaded.Status)
	}
	notifications, err := store.LoadBackgroundNotifications(parent.ID)
	if err != nil {
		t.Fatalf("load notifications: %v", err)
	}
	found := false
	for _, n := range notifications {
		if n.QueueJobID == "job_paused" && n.DeliveryStatus == BackgroundNotificationPending {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected pending parent notification for blocked orphan, got %#v", notifications)
	}
}

func TestReapStaleQueueJobsLeavesLiveOwnerAlone(t *testing.T) {
	store := reaperTestStore(t)
	parent := reaperParentMeta(t, store)
	child := reaperChildSession(t, store, parent.ID, StatusRunning)
	// Owner is the current live process; fresh heartbeat -> not orphaned.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	job := deadOwnerJob("job_live", parent.ID, child.ID, QueueStatusRunning, "")
	job.WorkerPID = os.Getpid()
	job.ProcessStartID = queueProcessStartID
	job.ClaimedBy = "process:" + queueProcessStartID
	job.HeartbeatAt = now
	seedParentJob(t, store, parent.ID, job)

	result, err := store.ReapStaleQueueJobs(time.Minute)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if result.Total() != 0 {
		t.Fatalf("expected no transitions for live owner, got %#v", result)
	}
	reloaded, err := store.LoadJob("job_live")
	if err != nil {
		t.Fatalf("load job: %v", err)
	}
	if reloaded.Status != QueueStatusRunning {
		t.Fatalf("expected job left running, got %s", reloaded.Status)
	}
}

func TestReapStaleQueueJobsReclaimsBlockedOrphanWithDeadOwner(t *testing.T) {
	store := reaperTestStore(t)
	parent := reaperParentMeta(t, store)
	child := reaperChildSession(t, store, parent.ID, StatusPaused)
	// Already blocked but the owner that recorded it has since died: the reaper
	// must still ensure the parent has a pending notification so it is woken.
	job := deadOwnerJob("job_blocked_orphan", parent.ID, child.ID, QueueStatusBlocked, StatusPaused)
	job.LastError = "child session is resumable: paused"
	seedParentJob(t, store, parent.ID, job)

	result, err := store.ReapStaleQueueJobs(time.Minute)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(result.Blocked) != 1 {
		t.Fatalf("expected blocked reclamation, got %#v", result)
	}
	notifications, err := store.LoadBackgroundNotifications(parent.ID)
	if err != nil {
		t.Fatalf("load notifications: %v", err)
	}
	pending := false
	for _, n := range notifications {
		if n.QueueJobID == "job_blocked_orphan" && n.DeliveryStatus == BackgroundNotificationPending {
			pending = true
		}
	}
	if !pending {
		t.Fatalf("expected pending notification for reclaimed blocked orphan, got %#v", notifications)
	}
}

func TestQueueJobCanProgress(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	live := QueueJob{Status: QueueStatusRunning, WorkerPID: os.Getpid(), ProcessStartID: queueProcessStartID, HeartbeatAt: now}
	if !QueueJobCanProgress(live) {
		t.Fatal("expected live running job to be progressable")
	}
	queued := QueueJob{Status: QueueStatusQueued}
	if !QueueJobCanProgress(queued) {
		t.Fatal("expected queued job to be progressable")
	}
	deadBlocked := QueueJob{Status: QueueStatusBlocked, WorkerPID: 999999, ProcessStartID: "999999:" + now}
	if QueueJobCanProgress(deadBlocked) {
		t.Fatal("expected blocked job with dead owner to be non-progressable")
	}
	completed := QueueJob{Status: QueueStatusCompleted}
	if QueueJobCanProgress(completed) {
		t.Fatal("expected terminal job to be non-progressable")
	}
}

func sliceHasString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
