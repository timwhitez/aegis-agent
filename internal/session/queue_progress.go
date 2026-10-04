package session

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// UpdateQueueJobEffectiveBudget mirrors one child budget without reconciling
// its state or restoring a stale queue claim. Claim/settlement/heartbeat facts
// are read and preserved under the same durable lock as other queue writers.
func (s *Store) UpdateQueueJobEffectiveBudget(jobID string, budget *EffectiveBudget) error {
	if err := validateStoreID("queue job", jobID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureQueueDirs(); err != nil {
		return err
	}
	return s.withFileLock(filepath.Join(s.queueRoot(), "claim.lock"), func() error {
		job, err := s.loadQueueJobForCoordinationLocked(jobID)
		if err != nil {
			return fmt.Errorf("load linked queue job while persisting effective budget: %w: %w", ErrQueueJobLeaseLost, err)
		}
		previous := job
		job.EffectiveBudget = CloneEffectiveBudget(budget)
		if err := s.saveJobLocked(job); err != nil {
			// A publication error can follow rename. Roll back while still
			// holding the claim lock, before another owner can advance the job.
			return errors.Join(fmt.Errorf("persist linked queue job effective budget: %w", err), s.saveJobLocked(previous))
		}
		return nil
	})
}

// RefreshQueueJobLease atomically renews a current owned running claim or
// observes its durable outcome. active=false,nil means normal settlement;
// reclaimed/requeued/missing facts are ownership loss, never settlement.
func (s *Store) RefreshQueueJobLease(jobID string) (job QueueJob, active bool, err error) {
	if err := validateStoreID("queue job", jobID); err != nil {
		return QueueJob{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureQueueDirs(); err != nil {
		return QueueJob{}, false, err
	}
	err = s.withFileLock(filepath.Join(s.queueRoot(), "claim.lock"), func() error {
		job, err = s.loadQueueJobForCoordinationLocked(jobID)
		if err != nil {
			return fmt.Errorf("queue job %s has no readable lease outcome: %w: %w", jobID, ErrQueueJobLeaseLost, err)
		}
		if err := validateQueueExecutionOwnership(job); err != nil {
			return err
		}
		if job.Status != QueueStatusRunning {
			return nil
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		job.UpdatedAt = now
		applyQueueLease(&job, now)
		if err := s.writeJSONFile(s.queueJobPath(QueueStatusRunning, jobID), job); err != nil {
			return err
		}
		active = true
		return nil
	})
	return job, active, err
}

type QueueExecutionWriteMode int

const (
	QueueExecutionSettlement QueueExecutionWriteMode = iota
	QueueExecutionRollback
	QueueExecutionFailedHandoff
)

// UpdateQueueJobAfterExecution validates the latest durable lease outcome and
// publishes the caller's settlement/rollback in the same claim-lock critical
// section. The mutation callback must not call Store methods.
func (s *Store) UpdateQueueJobAfterExecution(jobID string, mode QueueExecutionWriteMode, update func(*QueueJob)) (job QueueJob, err error) {
	if err := validateStoreID("queue job", jobID); err != nil {
		return QueueJob{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureQueueDirs(); err != nil {
		return QueueJob{}, err
	}
	err = s.withFileLock(filepath.Join(s.queueRoot(), "claim.lock"), func() error {
		job, err = s.loadQueueJobForCoordinationLocked(jobID)
		if err != nil {
			return fmt.Errorf("load queue job %s before execution settlement: %w: %w", jobID, ErrQueueJobLeaseLost, err)
		}
		if err := validateQueueExecutionOwnership(job); err != nil {
			return err
		}
		if mode == QueueExecutionRollback && job.Status != QueueStatusRunning {
			return fmt.Errorf("queue job %s is no longer an active rollback claim: %w", jobID, ErrQueueJobLeaseLost)
		}
		if mode != QueueExecutionSettlement && mode != QueueExecutionRollback && mode != QueueExecutionFailedHandoff {
			return errors.New("invalid queue execution write mode")
		}
		previous := job
		update(&job)
		failedHandoff := mode == QueueExecutionFailedHandoff && previous.Status == QueueStatusCompleted && job.Status == QueueStatusFailed && previous.SessionID != "" && previous.SessionID == job.SessionID && strings.TrimSpace(job.LastError) != ""
		if !failedHandoff && (previous.Status == QueueStatusCompleted || previous.Status == QueueStatusCancelled || previous.Status == QueueStatusFailed) {
			// The child may have settled before handoff. Enrich output metadata,
			// but never reverse an authoritative terminal outcome/result.
			job.Status, job.SessionID, job.SessionStatus = previous.Status, previous.SessionID, previous.SessionStatus
			job.FinalText, job.LastError, job.StopReason = previous.FinalText, previous.LastError, previous.StopReason
		}
		if err := s.saveJobLocked(job); err != nil {
			return err
		}
		job, err = s.loadQueueJobForCoordinationLocked(jobID)
		return err
	})
	return job, err
}

func validateQueueExecutionOwnership(job QueueJob) error {
	if QueueJobLeaseWasReclaimed(job) || job.Status == QueueStatusQueued {
		return fmt.Errorf("queue job %s no longer has the worker's running claim (status %s): %w", job.ID, job.Status, ErrQueueJobLeaseLost)
	}
	if owner := strings.TrimSpace(job.ProcessStartID); owner != "" && owner != queueProcessStartID {
		return fmt.Errorf("queue job %s is claimed by process %s: %w", job.ID, owner, ErrQueueJobLeaseLost)
	}
	return nil
}

// Caller holds Store.mu and the durable queue claim lock.
func (s *Store) refreshQueueJobHeartbeatLocked(jobID string) (QueueJob, error) {
	path := s.queueJobPath(QueueStatusRunning, jobID)
	var job QueueJob
	if err := readJSONFile(path, &job); err != nil {
		return QueueJob{}, err
	}
	if err := validateQueueJob(job); err != nil {
		return QueueJob{}, fmt.Errorf("queue job %s: %w", jobID, err)
	}
	if err := validateQueueJobStatusDirectory(job, QueueStatusRunning); err != nil {
		return QueueJob{}, fmt.Errorf("queue job %s: %w", jobID, err)
	}
	if owner := strings.TrimSpace(job.ProcessStartID); owner != "" && owner != queueProcessStartID {
		return QueueJob{}, fmt.Errorf("queue job %s is claimed by process %s: %w", jobID, owner, ErrQueueJobLeaseLost)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	job.UpdatedAt = now
	applyQueueLease(&job, now)
	if err := s.writeJSONFile(path, job); err != nil {
		return QueueJob{}, err
	}
	return job, nil
}

// A resume reserves capacity and installs its lease before Continue updates the
// previously paused child. Passive readers must not settle that reservation.
// A new pause after the claim is a genuine outcome and reconciles normally.
func queueResumeClaimIsPendingChildStart(job QueueJob, state State, now time.Time) bool {
	if job.Status != QueueStatusRunning || job.SessionStatus != StatusRunning || job.ParentSessionID == "" || job.SessionID == "" || job.ClaimedBy != "agent_prompt:"+job.ParentSessionID || job.ProcessStartID == "" || job.WorkerPID <= 0 || QueueJobLeaseWasReclaimed(job) || !queueJobHasRecentLease(job, now) {
		return false
	}
	if state.Status != StatusPaused && state.Status != StatusAwaitingInput {
		return false
	}
	claimedAt, claimErr := time.Parse(time.RFC3339Nano, job.ClaimedAt)
	stateAt, stateErr := time.Parse(time.RFC3339Nano, state.UpdatedAt)
	return claimErr == nil && stateErr == nil && !claimedAt.After(now) && !stateAt.After(claimedAt)
}

// A reader may have captured its snapshot before a resume/settlement writer.
// Recheck the complete canonical fact under claim.lock before any repair write.
// repaired=nil only verifies that reconciliation still starts from current facts.
func (s *Store) updateQueueJobRepairIfCurrent(expected QueueJob, repaired *QueueJob) (current QueueJob, matches bool, err error) {
	if err := validateStoreID("queue job", expected.ID); err != nil {
		return QueueJob{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err = s.withFileLock(filepath.Join(s.queueRoot(), "claim.lock"), func() error {
		current, err = s.loadQueueJobForCoordinationLocked(expected.ID)
		if err != nil {
			return err
		}
		matches = reflect.DeepEqual(current, expected)
		if !matches || repaired == nil {
			return nil
		}
		if repaired.ID != expected.ID {
			return errors.New("queue job repair changed identity")
		}
		if err := s.saveJobLocked(*repaired); err != nil {
			return err
		}
		current, err = s.loadQueueJobForCoordinationLocked(expected.ID)
		return err
	})
	return current, matches, err
}
