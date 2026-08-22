package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aegis-agent/internal/events"
)

const queueLeaseReclaimedErrorPrefix = "queue lease reclaimed:"

// ReapResult summarizes a single reaper pass over the queue. It is returned for
// observability/logging and is safe to ignore.
type ReapResult struct {
	Scanned   int
	Requeued  []string
	Blocked   []string
	Completed []string
	Cancelled []string
	Failed    []string
}

// Total reports how many jobs the reaper transitioned in this pass.
func (r ReapResult) Total() int {
	return len(r.Requeued) + len(r.Blocked) + len(r.Completed) + len(r.Cancelled) + len(r.Failed)
}

// ReapStaleQueueJobs reclaims orphaned queue jobs whose owning process has died
// or whose lease heartbeat has gone stale. Without this, a job claimed by a
// process that later exits (e.g. a web service restart) stays in running/blocked
// forever and keeps its parent session parked in wait-all coordination.
//
// Policy (hybrid, liveness-only — the runtime never decides workflow):
//   - terminal child session (completed/cancelled/failed): settle the job to the matching
//     terminal queue status so the parent coordination gate is released.
//   - no child session yet (crashed before Start): requeue so a worker re-runs it.
//   - non-terminal child session (paused/awaiting_input/running-but-orphaned):
//     mark blocked and ensure a parent background notification exists so the
//     master model can decide whether to re-prompt, stop, or continue.
//
// staleAfter bounds how long a heartbeat may lag before a job with a still-live
// (or unknown) owner is considered stale; a non-positive value falls back to
// QueueRunningStaleAfter.
func (s *Store) ReapStaleQueueJobs(staleAfter time.Duration) (ReapResult, error) {
	if staleAfter <= 0 {
		staleAfter = QueueRunningStaleAfter
	}
	now := time.Now().UTC()

	s.mu.Lock()
	if err := s.ensureQueueDirs(); err != nil {
		s.mu.Unlock()
		return ReapResult{}, err
	}
	var candidates []QueueJob
	var result ReapResult
	for _, status := range []string{QueueStatusRunning, QueueStatusBlocked} {
		dir := s.queueStatusDir(status)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			s.mu.Unlock()
			return ReapResult{}, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			jobID, ok := queueJobIDFromFilename(entry.Name())
			if !ok {
				continue
			}
			var job QueueJob
			if err := readJSONFile(s.queueJobPath(status, jobID), &job); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				continue
			}
			if job.ID != jobID {
				continue
			}
			result.Scanned++
			if !queueJobIsOrphaned(job, now, staleAfter) {
				continue
			}
			candidates = append(candidates, job)
		}
	}
	s.mu.Unlock()

	for _, job := range candidates {
		transition, err := s.reapOrphanedQueueJob(job)
		if err != nil {
			return result, fmt.Errorf("reap queue job %s: %w", job.ID, err)
		}
		switch transition {
		case QueueStatusQueued:
			result.Requeued = append(result.Requeued, job.ID)
		case QueueStatusBlocked:
			result.Blocked = append(result.Blocked, job.ID)
		case QueueStatusCompleted:
			result.Completed = append(result.Completed, job.ID)
		case QueueStatusCancelled:
			result.Cancelled = append(result.Cancelled, job.ID)
		case QueueStatusFailed:
			result.Failed = append(result.Failed, job.ID)
		}
	}
	return result, nil
}

// queueJobIsOrphaned reports whether a running/blocked job has lost its owning
// process or exceeded its heartbeat lease. A blocked job whose owner is dead is
// orphaned regardless of heartbeat, because nothing will ever resume it. A
// blocked job whose lease was already cleared has no owner to reclaim and is
// not reported again, so a settled job is not re-reaped every pass.
func queueJobIsOrphaned(job QueueJob, now time.Time, staleAfter time.Duration) bool {
	if job.Status == QueueStatusBlocked && !queueJobHasLease(job) {
		// The lease has already been cleared, so there is no owner left to
		// reclaim and re-settling would rewrite the same durable fact plus
		// re-scan the parent event log on every reaper pass. Parent
		// coordination for such a job is (re)ensured by the path that cleared
		// the lease and by queue job reconciliation on load.
		return false
	}
	ownerAlive := queueJobOwnerAlive(job)
	if !ownerAlive {
		return true
	}
	reference := firstNonEmptyQueueTimestamp(job.HeartbeatAt, job.ClaimedAt, job.UpdatedAt)
	if reference == "" {
		// Owner looks alive but there is no lease timestamp to trust; leave it
		// to avoid reclaiming a healthy job.
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, reference)
	if err != nil {
		return false
	}
	return now.Sub(parsed) > staleAfter
}

// reapOrphanedQueueJob applies the hybrid recovery policy to one orphan and
// returns the queue status it transitioned to. An empty status means the job was
// left untouched because it is no longer the job that was scanned.
func (s *Store) reapOrphanedQueueJob(job QueueJob) (string, error) {
	// Candidates are collected from a snapshot taken before the store lock was
	// released, so the durable fact may have been advanced by its worker (or by
	// another reaper) in the meantime. Re-read the canonical job under the same
	// durable queue lock the writers use and only proceed while the scanned
	// lease is still the current one; otherwise writing the stale snapshot back
	// would roll a settled job (and its FinalText) backwards.
	current, err := s.LoadJobCoordinationSnapshot(job.ID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	if !queueReapCandidateIsCurrent(job, current) {
		return "", nil
	}
	job = current

	childStatus, hasChild := s.orphanChildSessionStatus(job)
	if hasChild {
		cancelled, err := s.applyOrphanedQueueCancelRequest(job)
		if err != nil {
			return "", err
		}
		if cancelled {
			job.StopReason = QueueStopReasonAgentStop
			return s.settleReapedJob(job, QueueStatusCancelled, "cancelled by parent agent after worker owner exited")
		}
	}

	switch {
	case hasChild && childStatus == StatusCompleted:
		return s.settleReapedJob(job, QueueStatusCompleted, "")
	case hasChild && childStatus == StatusCancelled:
		return s.settleReapedJob(job, QueueStatusCancelled, firstNonEmptyQueueValue(job.LastError, "child session cancelled; reclaimed orphaned job"))
	case hasChild && childStatus == StatusFailed:
		return s.settleReapedJob(job, QueueStatusFailed, firstNonEmptyQueueValue(job.LastError, "child session failed; reclaimed orphaned job"))
	case !hasChild && strings.TrimSpace(job.SessionID) == "":
		return s.requeueReapedJob(job)
	default:
		reason := firstNonEmptyQueueValue(job.LastError, "child session is resumable; owner process exited")
		return s.settleReapedJob(job, QueueStatusBlocked, reason)
	}
}

// queueReapCandidateIsCurrent reports whether the canonical queue fact still
// matches the snapshot the reaper scanned. Any advance in status or lease
// identity means another process owns the transition now, so this pass must not
// overwrite it with the older snapshot.
func queueReapCandidateIsCurrent(candidate, current QueueJob) bool {
	if current.Status != candidate.Status {
		return false
	}
	if strings.TrimSpace(current.ProcessStartID) != strings.TrimSpace(candidate.ProcessStartID) {
		return false
	}
	if current.WorkerPID != candidate.WorkerPID {
		return false
	}
	if strings.TrimSpace(current.ClaimedBy) != strings.TrimSpace(candidate.ClaimedBy) {
		return false
	}
	if strings.TrimSpace(current.HeartbeatAt) != strings.TrimSpace(candidate.HeartbeatAt) {
		return false
	}
	if strings.TrimSpace(current.UpdatedAt) != strings.TrimSpace(candidate.UpdatedAt) {
		return false
	}
	if strings.TrimSpace(current.SessionID) != strings.TrimSpace(candidate.SessionID) {
		return false
	}
	return true
}

// QueueJobLeaseWasReclaimed identifies the stable blocked outcome written by
// the liveness reaper. A still-running worker uses this marker to distinguish
// lease loss from its own normal blocked/terminal reconciliation.
func QueueJobLeaseWasReclaimed(job QueueJob) bool {
	return job.Status == QueueStatusBlocked &&
		!queueJobHasLease(job) &&
		queueLeaseReclaimedError(job.LastError)
}

func queueLeaseReclaimedError(lastError string) bool {
	return strings.HasPrefix(strings.TrimSpace(lastError), queueLeaseReclaimedErrorPrefix)
}

// saveReapedQueueJobIfCurrent performs the final reaper transition as a CAS
// under claim.lock. The earlier coordination snapshot is only advisory: a
// worker may advance the job while the reaper inspects its linked child.
func (s *Store) saveReapedQueueJobIfCurrent(candidate, updated QueueJob) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureQueueDirs(); err != nil {
		return false, err
	}
	committed := false
	lockPath := filepath.Join(s.queueRoot(), "claim.lock")
	err := s.withFileLock(lockPath, func() error {
		current, err := s.loadQueueJobForCoordinationLocked(candidate.ID)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !queueReapCandidateIsCurrent(candidate, current) {
			return nil
		}
		if err := s.saveJobLocked(updated); err != nil {
			return err
		}
		committed = true
		return nil
	})
	return committed, err
}

// orphanChildSessionStatus loads the linked child session status if any.
func (s *Store) orphanChildSessionStatus(job QueueJob) (string, bool) {
	sessionID := strings.TrimSpace(job.SessionID)
	if sessionID == "" {
		return "", false
	}
	state, err := s.LoadState(sessionID)
	if err != nil {
		return "", false
	}
	return state.Status, true
}

// settleReapedJob clears the dead lease, sets the new status, persists the job,
// and reconciles parent coordination + background notification via the same
// helpers the normal worker path uses.
func (s *Store) settleReapedJob(job QueueJob, status, lastError string) (string, error) {
	updated := clearReapedQueueLease(job)
	updated.Status = status
	var runningChildState *State
	if strings.TrimSpace(updated.SessionID) != "" {
		if state, err := s.LoadState(updated.SessionID); err == nil {
			updated.SessionStatus = state.Status
			if status == QueueStatusBlocked && state.Status == StatusRunning {
				snapshot := state
				runningChildState = &snapshot
			}
		}
		if meta, err := s.LoadMetadata(updated.SessionID); err == nil {
			updated.EffectiveWorkdir = meta.Workdir
			updated.EffectiveBudget = CloneEffectiveBudget(meta.EffectiveBudget)
		}
	}
	if strings.TrimSpace(lastError) != "" {
		updated.LastError = lastError
	}
	if status == QueueStatusBlocked {
		reason := firstNonEmptyQueueValue(updated.LastError, "child session is resumable; owner process exited")
		updated.LastError = queueLeaseReclaimedErrorPrefix + " " + reason
	}
	if s.beforeQueueReapCommit != nil {
		s.beforeQueueReapCommit(job, updated)
	}
	committed, err := s.saveReapedQueueJobIfCurrent(job, updated)
	if err != nil {
		return "", err
	}
	if !committed {
		return "", nil
	}
	var postCommitErr error
	if runningChildState != nil {
		paused, pauseErr := s.pauseReapedRunningChild(updated.SessionID, *runningChildState, updated.ID)
		postCommitErr = pauseErr
		if paused {
			updated.SessionStatus = StatusPaused
		} else if pauseErr == nil {
			if currentState, err := s.LoadState(updated.SessionID); err == nil {
				updated.SessionStatus = currentState.Status
				switch currentState.Status {
				case StatusCompleted, StatusCancelled, StatusFailed:
					repaired, reconcileErr := s.LoadJob(updated.ID)
					if reconcileErr != nil {
						postCommitErr = errors.Join(postCommitErr, reconcileErr)
					} else {
						updated = repaired
						status = repaired.Status
					}
				}
			}
		}
	}
	if updated.ParentSessionID != "" {
		if isTerminalQueueStatus(updated.Status) {
			if err := s.ensureTerminalQueueJobParentState(updated); err != nil {
				postCommitErr = errors.Join(postCommitErr, err)
			}
		} else if updated.Status == QueueStatusBlocked {
			if err := s.ensureBlockedQueueJobParentState(updated); err != nil {
				postCommitErr = errors.Join(postCommitErr, err)
			}
		}
	}
	return status, postCommitErr
}

// pauseReapedRunningChild advances state.json only when it still matches the
// running snapshot inspected before the queue CAS. This prevents a reaper from
// overwriting a worker that completed or otherwise advanced the session during
// the scan/commit window.
func (s *Store) pauseReapedRunningChild(sessionID string, expected State, jobID string) (bool, error) {
	path, err := s.sessionPath(sessionID, "state.json")
	if err != nil {
		return false, err
	}
	lockPath, err := s.sessionPath(sessionID, "state.lock")
	if err != nil {
		return false, err
	}
	changed := false
	s.mu.Lock()
	err = s.withFileLock(lockPath, func() error {
		var current State
		if err := readJSONFile(path, &current); err != nil {
			return err
		}
		if current.Status != StatusRunning || strings.TrimSpace(current.UpdatedAt) != strings.TrimSpace(expected.UpdatedAt) {
			return nil
		}
		current.Status = StatusPaused
		current.Phase = "interrupt"
		current.PauseReason = "stale_owner_reconciled"
		current.LastError = ""
		current.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := validateState(current); err != nil {
			return fmt.Errorf("validate reaped child state: %w", err)
		}
		if err := s.writeJSONFile(path, current); err != nil {
			return err
		}
		changed = true
		return nil
	})
	s.mu.Unlock()
	if err != nil || !changed {
		return changed, err
	}
	err = s.AppendEvent(sessionID, events.New(sessionID, "session.paused", "interrupt", map[string]any{
		"reason":          "stale_owner_reconciled",
		"source":          "queue_reaper",
		"reconciled":      true,
		"reconciled_from": "orphaned_queue_lease",
		"queue_job_id":    jobID,
	}))
	return true, err
}

func (s *Store) applyOrphanedQueueCancelRequest(job QueueJob) (bool, error) {
	sessionID := strings.TrimSpace(job.SessionID)
	if sessionID == "" {
		return false, nil
	}
	request, err := s.LoadSessionCancel(sessionID)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if request.Status != CancelRequestStatusRequested {
		return request.Status == CancelRequestStatusApplied, nil
	}
	state, err := s.LoadState(sessionID)
	if err != nil {
		return false, err
	}
	state.Status = StatusCancelled
	state.Phase = "cancelled"
	state.PauseReason = ""
	state.LastError = ""
	state.IncompleteReason = ""
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.SaveState(sessionID, state); err != nil {
		return false, err
	}
	if meta, err := s.LoadMetadata(sessionID); err == nil && meta.EffectiveBudget != nil {
		budget := CloneEffectiveBudget(meta.EffectiveBudget)
		budget.Status = BudgetStatusCancelled
		budget.LastReason = request.Reason
		RefreshEffectiveBudget(budget, state.Turn)
		meta.EffectiveBudget = budget
		if err := s.SaveMetadata(sessionID, meta); err != nil {
			return false, err
		}
	} else if err != nil {
		return false, err
	}
	if _, err := s.MarkSessionCancelApplied(sessionID, request.ID); err != nil {
		return false, err
	}
	if err := s.ensureSessionCancelledEvent(sessionID, request); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) ensureSessionCancelledEvent(sessionID string, request CancelRequest) error {
	eventsList, err := s.LoadEvents(sessionID)
	if err != nil {
		return err
	}
	for _, evt := range eventsList {
		if evt.Type == "session.cancelled" {
			return nil
		}
	}
	return s.AppendEvent(sessionID, events.New(sessionID, "session.cancelled", "cancelled", map[string]any{
		"reason":            request.Reason,
		"request_id":        request.ID,
		"parent_session_id": request.ParentSessionID,
		"queue_job_id":      request.QueueJobID,
		"recovered":         true,
	}))
}

// requeueReapedJob returns a pre-Start orphan to the queued pool so a worker can
// claim and run it again.
func (s *Store) requeueReapedJob(job QueueJob) (string, error) {
	updated := clearReapedQueueLease(job)
	updated.Status = QueueStatusQueued
	updated.SessionID = ""
	updated.SessionStatus = ""
	updated.EffectiveWorkdir = ""
	updated.VisiblePaths = nil
	updated.FinalText = ""
	updated.LastError = ""
	updated.StopReason = ""
	if s.beforeQueueReapCommit != nil {
		s.beforeQueueReapCommit(job, updated)
	}
	committed, err := s.saveReapedQueueJobIfCurrent(job, updated)
	if err != nil {
		return "", err
	}
	if !committed {
		return "", nil
	}
	return QueueStatusQueued, nil
}

// clearReapedQueueLease drops the dead owner's lease fields so the job no longer
// appears claimed by a vanished process.
func clearReapedQueueLease(job QueueJob) QueueJob {
	job.ClaimedBy = ""
	job.ClaimedAt = ""
	job.HeartbeatAt = ""
	job.WorkerPID = 0
	job.ProcessStartID = ""
	return job
}

// queueJobHasLease reports whether a job still carries any owner lease fact. A
// job with no lease left was already settled by clearReapedQueueLease (or by a
// worker finishing its write-back), so there is no vanished owner to reclaim.
func queueJobHasLease(job QueueJob) bool {
	return job.WorkerPID != 0 ||
		strings.TrimSpace(job.ProcessStartID) != "" ||
		strings.TrimSpace(job.ClaimedBy) != "" ||
		strings.TrimSpace(job.ClaimedAt) != "" ||
		strings.TrimSpace(job.HeartbeatAt) != ""
}

func firstNonEmptyQueueTimestamp(values ...string) string {
	return firstNonEmptyQueueValue(values...)
}

func firstNonEmptyQueueValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
