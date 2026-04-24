package runtime

import (
	"os"
	"strings"
	"time"

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
	coordination.WaitMode = mergeWaitMode(coordination.WaitMode, waitMode)
	coordination.UnresolvedChildSessions = appendUnique(coordination.UnresolvedChildSessions, childSessionID)
	coordination.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return store.SaveParentCoordination(parentSessionID, coordination)
}

func addParentQueueJob(store *session.Store, parentSessionID, jobID, waitMode string) error {
	if strings.TrimSpace(parentSessionID) == "" || strings.TrimSpace(jobID) == "" {
		return nil
	}
	coordination := loadOrNewParentCoordination(store, parentSessionID)
	coordination.WaitMode = mergeWaitMode(coordination.WaitMode, waitMode)
	coordination.UnresolvedQueueJobs = appendUnique(coordination.UnresolvedQueueJobs, jobID)
	coordination.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return store.SaveParentCoordination(parentSessionID, coordination)
}

func resolveParentChildSession(store *session.Store, parentSessionID, childSessionID, status string) error {
	if strings.TrimSpace(parentSessionID) == "" || strings.TrimSpace(childSessionID) == "" {
		return nil
	}
	coordination := loadOrNewParentCoordination(store, parentSessionID)
	coordination.UnresolvedChildSessions = removeString(coordination.UnresolvedChildSessions, childSessionID)
	if status == session.StatusFailed {
		coordination.FailedChildSessions = appendUnique(coordination.FailedChildSessions, childSessionID)
	} else {
		coordination.CompletedChildSessions = appendUnique(coordination.CompletedChildSessions, childSessionID)
	}
	coordination.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return store.SaveParentCoordination(parentSessionID, coordination)
}

func resolveParentQueueJob(store *session.Store, parentSessionID, jobID, status string) error {
	if strings.TrimSpace(parentSessionID) == "" || strings.TrimSpace(jobID) == "" {
		return nil
	}
	coordination := loadOrNewParentCoordination(store, parentSessionID)
	coordination.UnresolvedQueueJobs = removeString(coordination.UnresolvedQueueJobs, jobID)
	if status == session.QueueStatusFailed {
		coordination.FailedQueueJobs = appendUnique(coordination.FailedQueueJobs, jobID)
	} else {
		coordination.CompletedQueueJobs = appendUnique(coordination.CompletedQueueJobs, jobID)
	}
	coordination.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return store.SaveParentCoordination(parentSessionID, coordination)
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
