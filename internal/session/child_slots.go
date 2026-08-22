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

const directChildReservationReclaimPauseReason = "stale_owner_reconciled"

func (s *Store) directChildReservationDir() string {
	return filepath.Join(s.queueRoot(), "active_children")
}

// AcquireDirectChildSlot reserves one active-child slot under the same durable
// claim lock used by background workers. This closes the race where a direct
// child and a queue worker both observe capacity before either has created its
// durable running fact.
func (s *Store) AcquireDirectChildSlot(parentSessionID, rootSessionID, childSessionID string, maxActiveChildren int) (bool, error) {
	if err := validateStoreID("parent session", parentSessionID); err != nil {
		return false, err
	}
	if err := validateStoreID("root session", rootSessionID); err != nil {
		return false, err
	}
	if err := validateStoreID("child session", childSessionID); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureQueueDirs(); err != nil {
		return false, err
	}
	lockPath := filepath.Join(s.queueRoot(), "claim.lock")
	acquired := false
	err := s.withFileLock(lockPath, func() error {
		activeByRoot, err := s.activeRunningQueueJobsByRootLocked()
		if err != nil {
			return err
		}
		directByRoot, err := s.activeDirectChildReservationsByRootLocked()
		if err != nil {
			return err
		}
		path := filepath.Join(s.directChildReservationDir(), childSessionID+".json")
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("child session %s already has an active reservation", childSessionID)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if maxActiveChildren > 0 && activeByRoot[rootSessionID]+directByRoot[rootSessionID] >= maxActiveChildren {
			return nil
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		reservation := ChildRunReservation{
			SchemaVersion:   1,
			SessionID:       childSessionID,
			ParentSessionID: parentSessionID,
			RootSessionID:   rootSessionID,
			WorkerPID:       os.Getpid(),
			ProcessStartID:  queueProcessStartID,
			CreatedAt:       now,
		}
		if identity, ok := hostProcessIdentity(reservation.WorkerPID); ok {
			reservation.ProcessIdentity = identity
		}
		if err := validateChildRunReservation(reservation); err != nil {
			return err
		}
		if err := s.writeJSONFile(path, reservation); err != nil {
			return err
		}
		acquired = true
		return nil
	})
	return acquired, err
}

// AcquireQueueChildResumeSlot atomically moves one blocked queue job back to
// running while reserving capacity under the same durable claim lock used by
// new queue claims and direct-child reservations. The returned job is the
// pre-resume snapshot used to roll back a failed pre-run transition.
func (s *Store) AcquireQueueChildResumeSlot(parentSessionID, queueJobID, childSessionID string, maxActiveChildren int) (QueueJob, bool, error) {
	if err := validateStoreID("parent session", parentSessionID); err != nil {
		return QueueJob{}, false, err
	}
	if err := validateStoreID("queue job", queueJobID); err != nil {
		return QueueJob{}, false, err
	}
	if err := validateStoreID("child session", childSessionID); err != nil {
		return QueueJob{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureQueueDirs(); err != nil {
		return QueueJob{}, false, err
	}
	lockPath := filepath.Join(s.queueRoot(), "claim.lock")
	var previous QueueJob
	acquired := false
	err := s.withFileLock(lockPath, func() error {
		job, err := s.loadQueueJobForCoordinationLocked(queueJobID)
		if err != nil {
			return err
		}
		if strings.TrimSpace(job.ParentSessionID) != parentSessionID || strings.TrimSpace(job.SessionID) != childSessionID {
			return fmt.Errorf("queue job %s is not linked to parent %s and child %s", queueJobID, parentSessionID, childSessionID)
		}
		if job.Status != QueueStatusBlocked {
			return fmt.Errorf("queue job %s is %s; only blocked jobs can acquire a resume slot", queueJobID, job.Status)
		}
		previous = job
		rootSessionID := strings.TrimSpace(job.RootSessionID)
		if rootSessionID == "" {
			rootSessionID = parentSessionID
		}
		if maxActiveChildren > 0 {
			activeByRoot, err := s.activeRunningQueueJobsByRootLocked()
			if err != nil {
				return err
			}
			directByRoot, err := s.activeDirectChildReservationsByRootLocked()
			if err != nil {
				return err
			}
			if activeByRoot[rootSessionID]+directByRoot[rootSessionID] >= maxActiveChildren {
				return nil
			}
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		job.Status = QueueStatusRunning
		job.SessionStatus = StatusRunning
		job.StopReason = ""
		job.LastError = ""
		job.ClaimedBy = "agent_prompt:" + parentSessionID
		job.ClaimedAt = now
		job.HeartbeatAt = now
		job.WorkerPID = os.Getpid()
		job.ProcessStartID = queueProcessStartID
		if err := s.saveJobLocked(job); err != nil {
			return err
		}
		acquired = true
		return nil
	})
	return previous, acquired, err
}

func (s *Store) ReleaseDirectChildSlot(childSessionID string) error {
	if err := validateStoreID("child session", childSessionID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureQueueDirs(); err != nil {
		return err
	}
	lockPath := filepath.Join(s.queueRoot(), "claim.lock")
	return s.withFileLock(lockPath, func() error {
		path := filepath.Join(s.directChildReservationDir(), childSessionID+".json")
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
}

func (s *Store) activeDirectChildReservationsByRootLocked() (map[string]int, error) {
	counts := map[string]int{}
	now := time.Now().UTC()
	dir := s.directChildReservationDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return counts, nil
		}
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		var reservation ChildRunReservation
		if err := readJSONFile(path, &reservation); err != nil || validateChildRunReservation(reservation) != nil {
			// A malformed reservation cannot safely consume capacity forever.
			_ = os.Remove(path)
			continue
		}
		createdAt, _ := time.Parse(time.RFC3339Nano, reservation.CreatedAt)
		ownerAlive := hostProcessOwnerAlive(reservation.WorkerPID, reservation.ProcessIdentity)
		keep := ownerAlive
		reclaimReason := "owner_dead_or_identity_mismatch"
		var childState State
		hasChildState := false
		if state, stateErr := s.LoadState(reservation.SessionID); stateErr == nil {
			childState = state
			hasChildState = true
			if state.Status != StatusRunning {
				// Direct spawn/resume reserves capacity immediately before the
				// session transitions to running. Keep that short provisional
				// claim, but bound it so a failed pre-run path cannot leak forever.
				keep = keep && now.Sub(createdAt) <= queueRunningStaleAfter
				if !keep && ownerAlive {
					reclaimReason = "provisional_reservation_stale"
				}
			}
		} else if !errors.Is(stateErr, os.ErrNotExist) {
			return nil, stateErr
		} else if now.Sub(createdAt) > queueRunningStaleAfter {
			// A live process can survive a panic or failed pre-create path. Without a
			// session fact, a reservation older than the queue stale window must not
			// consume capacity forever.
			keep = false
			if ownerAlive {
				reclaimReason = "precreate_reservation_stale"
			}
		}
		if !keep {
			if hasChildState && childState.Status == StatusRunning {
				if err := s.pauseReclaimedDirectChildLocked(reservation, childState, reclaimReason); err != nil {
					return nil, err
				}
			}
			_ = os.Remove(path)
			continue
		}
		counts[reservation.RootSessionID]++
	}
	return counts, nil
}

func (s *Store) pauseReclaimedDirectChildLocked(reservation ChildRunReservation, state State, reclaimReason string) error {
	previous := state
	state.Status = StatusPaused
	state.Phase = "interrupt"
	state.PauseReason = directChildReservationReclaimPauseReason
	state.LastError = ""
	if err := s.saveStateLocked(reservation.SessionID, state); err != nil {
		return fmt.Errorf("pause child after reclaiming direct reservation: %w", err)
	}
	event := events.New(reservation.SessionID, "session.paused", "interrupt", map[string]any{
		"reason":                 directChildReservationReclaimPauseReason,
		"source":                 "direct_child_slot_reaper",
		"reconciled":             true,
		"reconciled_from":        "direct_child_reservation",
		"reclaim_reason":         reclaimReason,
		"reservation_worker_pid": reservation.WorkerPID,
		"process_start_id":       reservation.ProcessStartID,
		"process_identity":       reservation.ProcessIdentity,
	})
	if err := s.appendEventLocked(reservation.SessionID, event); err != nil {
		if rollbackErr := s.saveStateLocked(reservation.SessionID, previous); rollbackErr != nil {
			return fmt.Errorf("record direct reservation reclaim event failed with %v; restore running child state: %w", err, rollbackErr)
		}
		return fmt.Errorf("record direct reservation reclaim event: %w", err)
	}
	return nil
}

func validateChildRunReservation(reservation ChildRunReservation) error {
	if reservation.SchemaVersion <= 0 {
		return errors.New("child run reservation schema_version must be positive")
	}
	if err := validateStoreID("child run reservation session", reservation.SessionID); err != nil {
		return err
	}
	if err := validateStoreID("child run reservation parent", reservation.ParentSessionID); err != nil {
		return err
	}
	if err := validateStoreID("child run reservation root", reservation.RootSessionID); err != nil {
		return err
	}
	if reservation.WorkerPID <= 0 || strings.TrimSpace(reservation.ProcessStartID) == "" {
		return errors.New("child run reservation process owner is required")
	}
	if _, err := time.Parse(time.RFC3339Nano, reservation.CreatedAt); err != nil {
		return err
	}
	return nil
}
