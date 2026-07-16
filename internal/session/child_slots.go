package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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
	if maxActiveChildren <= 0 {
		return true, nil
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
		if activeByRoot[rootSessionID]+directByRoot[rootSessionID] >= maxActiveChildren {
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
		if err := validateChildRunReservation(reservation); err != nil {
			return err
		}
		path := filepath.Join(s.directChildReservationDir(), childSessionID+".json")
		if err := s.writeJSONFile(path, reservation); err != nil {
			return err
		}
		acquired = true
		return nil
	})
	return acquired, err
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
		keep := hostProcessAlive(reservation.WorkerPID)
		if state, stateErr := s.LoadState(reservation.SessionID); stateErr == nil {
			keep = state.Status == StatusRunning
		} else if !errors.Is(stateErr, os.ErrNotExist) {
			return nil, stateErr
		} else if createdAt, parseErr := time.Parse(time.RFC3339Nano, reservation.CreatedAt); parseErr != nil || now.Sub(createdAt) > queueRunningStaleAfter {
			// A live process can survive a panic or failed pre-create path. Without a
			// session fact, a reservation older than the queue stale window must not
			// consume capacity forever.
			keep = false
		}
		if !keep {
			_ = os.Remove(path)
			continue
		}
		counts[reservation.RootSessionID]++
	}
	return counts, nil
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
