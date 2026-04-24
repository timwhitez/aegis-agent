package runtime

import (
	"os"
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

func addParentChildSession(store *session.Store, parentSessionID, childSessionID, waitMode string) error {
	if strings.TrimSpace(parentSessionID) == "" || strings.TrimSpace(childSessionID) == "" {
		return nil
	}
	coordination := loadOrNewParentCoordination(store, parentSessionID)
	wasParked := coordination.Parked
	coordination.WaitMode = mergeWaitMode(coordination.WaitMode, waitMode)
	coordination.UnresolvedChildSessions = appendUnique(coordination.UnresolvedChildSessions, childSessionID)
	coordination.Parked = shouldParkParent(coordination)
	coordination.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := store.SaveParentCoordination(parentSessionID, coordination); err != nil {
		return err
	}
	emitParentCoordinationTransition(store, coordination, wasParked, "child_session", childSessionID)
	return nil
}

func addParentQueueJob(store *session.Store, parentSessionID, jobID, waitMode string) error {
	if strings.TrimSpace(parentSessionID) == "" || strings.TrimSpace(jobID) == "" {
		return nil
	}
	coordination := loadOrNewParentCoordination(store, parentSessionID)
	wasParked := coordination.Parked
	coordination.WaitMode = mergeWaitMode(coordination.WaitMode, waitMode)
	coordination.UnresolvedQueueJobs = appendUnique(coordination.UnresolvedQueueJobs, jobID)
	coordination.Parked = shouldParkParent(coordination)
	coordination.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := store.SaveParentCoordination(parentSessionID, coordination); err != nil {
		return err
	}
	emitParentCoordinationTransition(store, coordination, wasParked, "queue_job", jobID)
	return nil
}

func resolveParentChildSession(store *session.Store, parentSessionID, childSessionID, status string) error {
	if strings.TrimSpace(parentSessionID) == "" || strings.TrimSpace(childSessionID) == "" {
		return nil
	}
	coordination := loadOrNewParentCoordination(store, parentSessionID)
	wasParked := coordination.Parked
	coordination.UnresolvedChildSessions = removeString(coordination.UnresolvedChildSessions, childSessionID)
	if status == session.StatusFailed {
		coordination.FailedChildSessions = appendUnique(coordination.FailedChildSessions, childSessionID)
	} else {
		coordination.CompletedChildSessions = appendUnique(coordination.CompletedChildSessions, childSessionID)
	}
	coordination.Parked = shouldParkParent(coordination)
	coordination.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := store.SaveParentCoordination(parentSessionID, coordination); err != nil {
		return err
	}
	emitParentCoordinationTransition(store, coordination, wasParked, "child_session", childSessionID)
	return nil
}

func resolveParentQueueJob(store *session.Store, parentSessionID, jobID, status string) error {
	if strings.TrimSpace(parentSessionID) == "" || strings.TrimSpace(jobID) == "" {
		return nil
	}
	coordination := loadOrNewParentCoordination(store, parentSessionID)
	wasParked := coordination.Parked
	coordination.UnresolvedQueueJobs = removeString(coordination.UnresolvedQueueJobs, jobID)
	if status == session.QueueStatusFailed {
		coordination.FailedQueueJobs = appendUnique(coordination.FailedQueueJobs, jobID)
	} else {
		coordination.CompletedQueueJobs = appendUnique(coordination.CompletedQueueJobs, jobID)
	}
	coordination.Parked = shouldParkParent(coordination)
	coordination.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := store.SaveParentCoordination(parentSessionID, coordination); err != nil {
		return err
	}
	emitParentCoordinationTransition(store, coordination, wasParked, "queue_job", jobID)
	return nil
}

func loadOrNewParentCoordination(store *session.Store, parentSessionID string) session.ParentCoordination {
	coordination, err := store.LoadParentCoordination(parentSessionID)
	if err == nil && coordination.ParentSessionID != "" {
		return coordination
	}
	if err != nil && !os.IsNotExist(err) {
		return session.ParentCoordination{}
	}
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

func emitParentCoordinationTransition(store *session.Store, coordination session.ParentCoordination, wasParked bool, itemKind, itemID string) {
	eventType := ""
	switch {
	case !wasParked && coordination.Parked:
		eventType = "parent.coordination.parked"
	case wasParked && !coordination.Parked:
		eventType = "parent.coordination.resumed"
	default:
		return
	}
	_ = store.AppendEvent(coordination.ParentSessionID, events.New(coordination.ParentSessionID, eventType, "parent_coordination", map[string]any{
		"wait_mode":                 coordination.WaitMode,
		"item_kind":                 itemKind,
		"item_id":                   itemID,
		"unresolved_child_sessions": append([]string(nil), coordination.UnresolvedChildSessions...),
		"unresolved_queue_jobs":     append([]string(nil), coordination.UnresolvedQueueJobs...),
	}))
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
