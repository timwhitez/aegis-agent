package session

import (
	"errors"
	"fmt"
	"time"
)

// ClaimSessionRunWithGeneration uses a generation persisted by the caller before
// the run claim. A preparation journal can therefore identify even a claim whose
// following journal write was interrupted. The run and state locks still decide
// which caller may claim the session.
func (s *Store) ClaimSessionRunWithGeneration(sessionID, generation string, allowedStatuses ...string) (State, error) {
	stamp, err := time.Parse(time.RFC3339Nano, generation)
	if err != nil || stamp.IsZero() || stamp.UTC().Format(time.RFC3339Nano) != generation {
		return State{}, errors.New("run claim generation must be a canonical UTC timestamp")
	}
	allowed := make(map[string]struct{}, len(allowedStatuses))
	for _, status := range allowedStatuses {
		allowed[status] = struct{}{}
	}
	path, err := s.sessionPath(sessionID, "state.json")
	if err != nil {
		return State{}, err
	}
	lockPath, err := s.sessionPath(sessionID, "control", "run.lock")
	if err != nil {
		return State{}, err
	}
	stateLockPath, err := s.sessionPath(sessionID, "state.lock")
	if err != nil {
		return State{}, err
	}
	var claimed State
	s.mu.Lock()
	defer s.mu.Unlock()
	err = s.withFileLock(lockPath, func() error {
		pendingSteerCount := 0
		if count, ok, err := s.pendingSteerCountLocked(sessionID); err != nil {
			return err
		} else if ok {
			pendingSteerCount = count
		}
		return s.withFileLock(stateLockPath, func() error {
			if err := readJSONFile(path, &claimed); err != nil {
				return err
			}
			if err := validateState(claimed); err != nil {
				return fmt.Errorf("validate state.json: %w", err)
			}
			if _, ok := allowed[claimed.Status]; !ok {
				return errors.New("session is not resumable")
			}
			previous, err := time.Parse(time.RFC3339Nano, claimed.UpdatedAt)
			if err != nil || !stamp.After(previous) {
				return errors.New("run claim generation must advance the current state")
			}
			claimed.Status = StatusRunning
			claimed.Phase = "prepare"
			claimed.PendingSteerCount = pendingSteerCount
			claimed.PauseReason = ""
			claimed.ProviderAutoResumeCount = 0
			claimed.UpdatedAt = generation
			if err := validateState(claimed); err != nil {
				return fmt.Errorf("validate state.json: %w", err)
			}
			return s.writeJSONFile(path, claimed)
		})
	})
	if err != nil {
		return State{}, err
	}
	return claimed, nil
}
