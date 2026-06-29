package runtime

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"go-cli-agent/internal/events"
	"go-cli-agent/internal/session"
)

const (
	parentWaitAll = "wait-all"
	parentWaitAny = "wait-any"
)

func normalizeParentWaitMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "any", "wait-any", "wait_any":
		return parentWaitAny
	default:
		return parentWaitAll
	}
}

func normalizeAndValidateParentWaitMode(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "default", "all", "wait-all", "wait_all":
		return parentWaitAll, nil
	case "any", "wait-any", "wait_any":
		return parentWaitAny, nil
	default:
		return "", fmt.Errorf("unsupported wait mode: %s", strings.TrimSpace(value))
	}
}

func addParentChildSession(store *session.Store, parentSessionID, childSessionID, waitMode string) error {
	if strings.TrimSpace(parentSessionID) == "" || strings.TrimSpace(childSessionID) == "" {
		return nil
	}
	previousCoordination, err := store.SnapshotParentCoordination(parentSessionID)
	if err != nil {
		return err
	}
	var wasParked bool
	coordination, _, err := store.MutateParentCoordination(parentSessionID, func(coordination *session.ParentCoordination) error {
		if coordination.ParentSessionID == "" {
			*coordination = newParentCoordination(parentSessionID)
		}
		wasParked = coordination.Parked
		coordination.WaitMode = mergeWaitMode(coordination.WaitMode, waitMode)
		coordination.UnresolvedChildSessions = appendUnique(coordination.UnresolvedChildSessions, childSessionID)
		coordination.Parked = shouldParkParent(*coordination)
		return nil
	})
	if err != nil {
		return err
	}
	if err := emitParentCoordinationTransition(store, coordination, wasParked, "child_session", childSessionID); err != nil {
		return restoreParentCoordinationAfterTransitionError(store, parentSessionID, previousCoordination, err)
	}
	return nil
}

func addParentQueueJob(store *session.Store, parentSessionID, jobID, waitMode string) error {
	if strings.TrimSpace(parentSessionID) == "" || strings.TrimSpace(jobID) == "" {
		return nil
	}
	previousCoordination, err := store.SnapshotParentCoordination(parentSessionID)
	if err != nil {
		return err
	}
	var wasParked bool
	coordination, _, err := store.MutateParentCoordination(parentSessionID, func(coordination *session.ParentCoordination) error {
		if coordination.ParentSessionID == "" {
			*coordination = newParentCoordination(parentSessionID)
		}
		wasParked = coordination.Parked
		coordination.WaitMode = mergeWaitMode(coordination.WaitMode, waitMode)
		coordination.UnresolvedQueueJobs = appendUnique(coordination.UnresolvedQueueJobs, jobID)
		coordination.Parked = shouldParkParent(*coordination)
		return nil
	})
	if err != nil {
		return err
	}
	if err := emitParentCoordinationTransition(store, coordination, wasParked, "queue_job", jobID); err != nil {
		return restoreParentCoordinationAfterTransitionError(store, parentSessionID, previousCoordination, err)
	}
	return nil
}

func resolveParentChildSession(store *session.Store, parentSessionID, childSessionID, status string) error {
	if strings.TrimSpace(parentSessionID) == "" || strings.TrimSpace(childSessionID) == "" {
		return nil
	}
	if !isTerminalSessionStatus(status) {
		return nil
	}
	previousCoordination, err := store.SnapshotParentCoordination(parentSessionID)
	if err != nil {
		return err
	}
	var wasParked bool
	coordination, _, err := store.MutateParentCoordination(parentSessionID, func(coordination *session.ParentCoordination) error {
		if coordination.ParentSessionID == "" {
			*coordination = newParentCoordination(parentSessionID)
		}
		wasParked = coordination.Parked
		coordination.UnresolvedChildSessions = removeString(coordination.UnresolvedChildSessions, childSessionID)
		coordination.CompletedChildSessions = removeString(coordination.CompletedChildSessions, childSessionID)
		coordination.FailedChildSessions = removeString(coordination.FailedChildSessions, childSessionID)
		if status == session.StatusFailed {
			coordination.FailedChildSessions = appendUnique(coordination.FailedChildSessions, childSessionID)
		} else {
			coordination.CompletedChildSessions = appendUnique(coordination.CompletedChildSessions, childSessionID)
		}
		coordination.Parked = shouldParkParent(*coordination)
		return nil
	})
	if err != nil {
		return err
	}
	if err := emitParentCoordinationTransition(store, coordination, wasParked, "child_session", childSessionID); err != nil {
		return restoreParentCoordinationAfterTransitionError(store, parentSessionID, previousCoordination, err)
	}
	return nil
}

func isTerminalSessionStatus(status string) bool {
	return status == session.StatusCompleted || status == session.StatusFailed
}

func resolveParentQueueJob(store *session.Store, parentSessionID, jobID, status string) error {
	if strings.TrimSpace(parentSessionID) == "" || strings.TrimSpace(jobID) == "" {
		return nil
	}
	previousCoordination, err := store.SnapshotParentCoordination(parentSessionID)
	if err != nil {
		return err
	}
	var wasParked bool
	coordination, _, err := store.MutateParentCoordination(parentSessionID, func(coordination *session.ParentCoordination) error {
		if coordination.ParentSessionID == "" {
			*coordination = newParentCoordination(parentSessionID)
		}
		wasParked = coordination.Parked
		coordination.UnresolvedQueueJobs = removeString(coordination.UnresolvedQueueJobs, jobID)
		coordination.CompletedQueueJobs = removeString(coordination.CompletedQueueJobs, jobID)
		coordination.FailedQueueJobs = removeString(coordination.FailedQueueJobs, jobID)
		if status == session.QueueStatusFailed {
			coordination.FailedQueueJobs = appendUnique(coordination.FailedQueueJobs, jobID)
		} else {
			coordination.CompletedQueueJobs = appendUnique(coordination.CompletedQueueJobs, jobID)
		}
		coordination.Parked = shouldParkParent(*coordination)
		return nil
	})
	if err != nil {
		return err
	}
	if err := emitParentCoordinationTransition(store, coordination, wasParked, "queue_job", jobID); err != nil {
		return restoreParentCoordinationAfterTransitionError(store, parentSessionID, previousCoordination, err)
	}
	return nil
}

func newParentCoordination(parentSessionID string) session.ParentCoordination {
	return session.ParentCoordination{
		SchemaVersion:   1,
		ParentSessionID: parentSessionID,
		WaitMode:        parentWaitAll,
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func mergeWaitMode(existing, next string) string {
	existing = normalizeParentWaitMode(existing)
	next = normalizeParentWaitMode(next)
	if existing == parentWaitAny || next == parentWaitAny {
		return parentWaitAny
	}
	return parentWaitAll
}

func shouldParkParent(coordination session.ParentCoordination) bool {
	if len(coordination.UnresolvedChildSessions) == 0 && len(coordination.UnresolvedQueueJobs) == 0 {
		return false
	}
	if normalizeParentWaitMode(coordination.WaitMode) == parentWaitAny &&
		(len(coordination.CompletedChildSessions) > 0 || len(coordination.CompletedQueueJobs) > 0) {
		return false
	}
	return true
}

func emitParentCoordinationTransition(store *session.Store, coordination session.ParentCoordination, wasParked bool, itemKind, itemID string) error {
	eventType := ""
	switch {
	case !wasParked && coordination.Parked:
		eventType = "parent.coordination.parked"
	case wasParked && !coordination.Parked:
		eventType = "parent.coordination.resumed"
	default:
		return nil
	}
	if err := store.AppendEvent(coordination.ParentSessionID, events.New(coordination.ParentSessionID, eventType, "parent_coordination", map[string]any{
		"wait_mode":                 coordination.WaitMode,
		"item_kind":                 itemKind,
		"item_id":                   itemID,
		"unresolved_child_sessions": append([]string(nil), coordination.UnresolvedChildSessions...),
		"unresolved_queue_jobs":     append([]string(nil), coordination.UnresolvedQueueJobs...),
	})); err != nil {
		return fmt.Errorf("append %s event: %w", eventType, err)
	}
	return nil
}

func restoreParentCoordinationAfterTransitionError(store *session.Store, parentSessionID string, snapshot session.ParentCoordinationSnapshot, err error) error {
	if restoreErr := store.RestoreParentCoordination(parentSessionID, snapshot); restoreErr != nil {
		return fmt.Errorf("%v; restore parent coordination after transition event failure: %w", err, restoreErr)
	}
	return err
}

func appendUnique(items []string, item string) []string {
	item = strings.TrimSpace(item)
	if item == "" {
		return items
	}
	for _, existing := range items {
		if existing == item {
			return items
		}
	}
	return append(items, item)
}

func removeString(items []string, item string) []string {
	var out []string
	for _, existing := range items {
		if existing != item {
			out = append(out, existing)
		}
	}
	return out
}

// coordinationDeadlockReason inspects a parent's coordination record and reports
// a non-empty reason when the parent is parked on unresolved work that can no
// longer make progress on its own. This happens when every unresolved queue job
// is blocked with a dead/absent owner and every unresolved child session is in a
// non-terminal state with no live owner — i.e. nothing will ever wake the
// parent. It is the cross-restart safety net behind the queue reaper: detection
// here only wakes the model with facts; it never resolves the work itself.
func coordinationDeadlockReason(store *session.Store, parentSessionID string) (string, bool, error) {
	coordination, err := store.LoadParentCoordination(parentSessionID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	if coordination.ParentSessionID == "" || !coordination.Parked {
		return "", false, nil
	}
	if len(coordination.UnresolvedChildSessions) == 0 && len(coordination.UnresolvedQueueJobs) == 0 {
		return "", false, nil
	}
	stalledJobs := make([]string, 0, len(coordination.UnresolvedQueueJobs))
	for _, jobID := range coordination.UnresolvedQueueJobs {
		job, err := store.LoadJob(jobID)
		if err != nil {
			// A job that cannot be loaded is treated as still potentially
			// progressing so we never wake on a transient read error.
			return "", false, nil
		}
		if session.QueueJobCanProgress(job) {
			return "", false, nil
		}
		stalledJobs = append(stalledJobs, jobID)
	}
	stalledChildren := make([]string, 0, len(coordination.UnresolvedChildSessions))
	for _, childID := range coordination.UnresolvedChildSessions {
		state, err := store.LoadState(childID)
		if err != nil {
			return "", false, nil
		}
		// A child still running may be progressing in another process; only a
		// non-terminal, non-running child counts as stalled here.
		if state.Status == session.StatusRunning {
			return "", false, nil
		}
		stalledChildren = append(stalledChildren, childID)
	}
	reason := fmt.Sprintf("parent coordination deadlock: no unresolved child work can progress (stalled jobs: %s; stalled child sessions: %s)",
		joinCoordinationItems(stalledJobs), joinCoordinationItems(stalledChildren))
	return reason, true, nil
}

func joinCoordinationItems(items []string) string {
	if len(items) == 0 {
		return "none"
	}
	return strings.Join(items, ", ")
}
