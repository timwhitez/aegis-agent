package runtime

import (
	"os"
	"strconv"
	"time"
)

// Durable run-ownership event types. The runtime writes them itself so every
// adapter (CLI, Web, SDK, queue worker, delegated child) leaves the same
// recoverable fact about which OS process is executing a run. Web console
// handle events stay readable next to them.
const (
	RunOwnerAcquiredEventType = "run.owner.acquired"
	RunOwnerReleasedEventType = "run.owner.released"
)

// runOwnerEventSource marks runtime-written owner events.
const runOwnerEventSource = "runtime"

// ProcessOwner identifies the OS process that executes a run. ProcessStartID
// uses the "<pid>:<started_at>" form shared with the Web console handle events
// so a single reader can extract the PID from either event family.
type ProcessOwner struct {
	PID            int
	ProcessStartID string
	StartedAt      string
}

// NewProcessOwner captures the calling process identity.
func NewProcessOwner() ProcessOwner {
	pid := os.Getpid()
	startedAt := time.Now().UTC().Format(time.RFC3339Nano)
	return ProcessOwner{
		PID:            pid,
		ProcessStartID: strconv.Itoa(pid) + ":" + startedAt,
		StartedAt:      startedAt,
	}
}

// processOwner is this process's stable run-owner identity. Every run started
// by this process records the same identity, so an adapter can recognize its
// own runs without comparing per-run handles.
var processOwner = NewProcessOwner()

// CurrentProcessOwner returns this process's run-owner identity.
func CurrentProcessOwner() ProcessOwner {
	return processOwner
}

func (o ProcessOwner) acquiredEventData() map[string]any {
	return map[string]any{
		"source":           runOwnerEventSource,
		"pid":              o.PID,
		"process_start_id": o.ProcessStartID,
		"started_at":       o.StartedAt,
	}
}

func (o ProcessOwner) releasedEventData(releasedAt string) map[string]any {
	data := o.acquiredEventData()
	data["released_at"] = releasedAt
	return data
}

// IsRunOwnerAcquireEventType reports whether eventType records that a process
// took ownership of a run.
func IsRunOwnerAcquireEventType(eventType string) bool {
	return eventType == RunOwnerAcquiredEventType
}

// IsRunOwnerReleaseEventType reports whether eventType records that a process
// released ownership of a run.
func IsRunOwnerReleaseEventType(eventType string) bool {
	return eventType == RunOwnerReleasedEventType
}
