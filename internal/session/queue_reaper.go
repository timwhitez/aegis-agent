package session

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"go-cli-agent/internal/events"
)

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
// orphaned regardless of heartbeat, because nothing will ever resume it.
func queueJobIsOrphaned(job QueueJob, now time.Time, staleAfter time.Duration) bool {
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
// returns the queue status it transitioned to.
func (s *Store) reapOrphanedQueueJob(job QueueJob) (string, error) {
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
	if strings.TrimSpace(updated.SessionID) != "" {
		if state, err := s.LoadState(updated.SessionID); err == nil {
			updated.SessionStatus = state.Status
		}
		if meta, err := s.LoadMetadata(updated.SessionID); err == nil {
			updated.EffectiveWorkdir = meta.Workdir
			updated.EffectiveBudget = CloneEffectiveBudget(meta.EffectiveBudget)
		}
	}
	if strings.TrimSpace(lastError) != "" {
		updated.LastError = lastError
	}
	if status == QueueStatusBlocked && strings.TrimSpace(updated.LastError) == "" {
		updated.LastError = "child session is resumable; owner process exited"
	}
	if err := s.SaveJob(updated); err != nil {
		return "", err
	}
	if updated.ParentSessionID != "" {
		if isTerminalQueueStatus(updated.Status) {
			if err := s.ensureTerminalQueueJobParentState(updated); err != nil {
				return "", err
			}
		} else if updated.Status == QueueStatusBlocked {
			if err := s.ensureBlockedQueueJobParentState(updated); err != nil {
				return "", err
			}
		}
	}
	return status, nil
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
	if err := s.SaveJob(updated); err != nil {
		return "", err
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
