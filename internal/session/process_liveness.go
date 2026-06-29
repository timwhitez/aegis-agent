package session

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// pidFromProcessStartID extracts the PID prefix from a "<pid>:<started_at>"
// process start identity token. It returns 0 when the token is empty or
// malformed.
func pidFromProcessStartID(processStartID string) int {
	pidText, _, ok := strings.Cut(strings.TrimSpace(processStartID), ":")
	if !ok {
		pidText = strings.TrimSpace(processStartID)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(pidText))
	if err != nil {
		return 0
	}
	return pid
}

// hostProcessAlive reports whether a PID belongs to a live process on this host.
// It is conservative: when liveness cannot be determined (no /proc, unexpected
// stat error) it returns true so the reaper never reclaims a job that might
// still be owned by a healthy worker.
var hostProcessAlive = func(pid int) bool {
	if pid <= 0 {
		return false
	}
	if pid == os.Getpid() {
		return true
	}
	if _, err := os.Stat("/proc"); err != nil {
		return true
	}
	if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid))); err != nil {
		return !errors.Is(err, os.ErrNotExist)
	}
	return true
}

// queueJobOwnerAlive reports whether the process that currently holds a job's
// lease is the live current process or another live host process. A job whose
// owner is dead is an orphan eligible for reaping.
func queueJobOwnerAlive(job QueueJob) bool {
	startID := strings.TrimSpace(job.ProcessStartID)
	if startID != "" && startID == queueProcessStartID {
		return true
	}
	pid := job.WorkerPID
	if pid == 0 {
		pid = pidFromProcessStartID(startID)
	}
	if pid == 0 {
		// No identifiable owner; treat as not-alive so heartbeat staleness
		// alone decides whether the job is reaped.
		return false
	}
	return hostProcessAlive(pid)
}

// QueueJobCanProgress reports whether an unresolved queue job could still make
// forward progress on its own. A job is progressable when it is queued, actively
// running with a live owner, or blocked but still owned by a live process that
// can resume it. A blocked job whose owner has exited cannot progress without
// outside intervention and therefore indicates a coordination deadlock.
func QueueJobCanProgress(job QueueJob) bool {
	switch job.Status {
	case QueueStatusQueued:
		return true
	case QueueStatusRunning, QueueStatusBlocked:
		return queueJobOwnerAlive(job)
	default:
		// Terminal statuses are resolved, not pending; treat as non-progressing.
		return false
	}
}
