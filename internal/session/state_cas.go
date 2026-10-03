package session

import (
	"fmt"
	"reflect"
	"time"
)

// SaveStateIfCurrent compares the complete durable state inside state.lock.
// It protects run-claim recovery against a later run with the same status and
// phase; UpdatedAt remains part of the expected generation. Loaded skills and
// pending steer counts retain the ordinary SaveState preservation rules.
func (s *Store) SaveStateIfCurrent(sessionID string, expected, next State) (bool, error) {
	_, saved, err := s.SwapStateIfCurrent(sessionID, expected, next)
	return saved, err
}

// SwapStateIfCurrent also returns the exact committed state, including its
// generation, before releasing state.lock. A separate LoadState could instead
// observe a newer claim and must not be used as rollback ownership evidence.
func (s *Store) SwapStateIfCurrent(sessionID string, expected, next State) (committed State, saved bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.sessionPath(sessionID, "state.json")
	if err != nil {
		return State{}, false, err
	}
	lockPath, err := s.sessionPath(sessionID, "state.lock")
	if err != nil {
		return State{}, false, err
	}
	count, hasCount, err := s.pendingSteerCountLocked(sessionID)
	if err != nil {
		return State{}, false, err
	}
	err = s.withFileLock(lockPath, func() error {
		var current State
		if err := readJSONFile(path, &current); err != nil {
			return err
		}
		if err := validateState(current); err != nil {
			return fmt.Errorf("validate current state.json: %w", err)
		}
		if !reflect.DeepEqual(current, expected) {
			return nil
		}
		// Running state updates retain the claimed identity. A CAS restoring a
		// nonrunning preclaim snapshot may explicitly restore its old identity.
		if next.Status == StatusRunning {
			if next.RunGeneration != "" && next.RunGeneration != current.RunGeneration {
				return fmt.Errorf("session run generation changed; refusing replacement running state")
			}
			next.RunGeneration = current.RunGeneration
		}
		next.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		next.LoadedSkills = mergeLoadedSkills(current.LoadedSkills, next.LoadedSkills)
		if hasCount {
			next.PendingSteerCount = count
		}
		if err := validateState(next); err != nil {
			return fmt.Errorf("validate state.json: %w", err)
		}
		if err := s.writeJSONFile(path, next); err != nil {
			return err
		}
		saved = true
		committed = next
		return nil
	})
	return committed, saved, err
}
