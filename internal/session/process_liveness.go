package session

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// hostProcessIdentity returns a boot-scoped OS process identity when the host
// exposes one. Linux uses boot_id + /proc/<pid>/stat starttime, so PID reuse can
// be distinguished from the process that originally created a durable lease.
// Unknown platforms return ok=false and callers retain conservative PID-only
// behavior.
var hostProcessIdentity = func(pid int) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", false
	}
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", false
	}
	statText := strings.TrimSpace(string(stat))
	closeParen := strings.LastIndex(statText, ")")
	if closeParen < 0 || closeParen+1 >= len(statText) {
		return "", false
	}
	fields := strings.Fields(statText[closeParen+1:])
	// fields starts at proc stat field 3 (state); starttime is field 22.
	const startTimeIndex = 22 - 3
	if len(fields) <= startTimeIndex {
		return "", false
	}
	boot := strings.TrimSpace(string(bootID))
	startTicks := strings.TrimSpace(fields[startTimeIndex])
	if boot == "" || startTicks == "" {
		return "", false
	}
	return "linux:" + boot + ":" + startTicks, true
}

func hostProcessOwnerAlive(pid int, expectedIdentity string) bool {
	if !hostProcessAlive(pid) {
		return false
	}
	expectedIdentity = strings.TrimSpace(expectedIdentity)
	if expectedIdentity == "" {
		return true
	}
	actual, ok := hostProcessIdentity(pid)
	if !ok {
		// The liveness contract is conservative when the OS cannot provide a
		// stable process identity.
		return true
	}
	return actual == expectedIdentity
}

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
// forward progress on its own. A job is progressable when it is queued or
// actively running with a live owner. Blocked is a stable, intervention-required
// state; legacy lease fields on a blocked snapshot are not authoritative proof
// that a worker is still executing it.
func QueueJobCanProgress(job QueueJob) bool {
	switch job.Status {
	case QueueStatusQueued:
		return true
	case QueueStatusRunning:
		return queueJobOwnerAlive(job)
	default:
		// Terminal statuses are resolved, not pending; treat as non-progressing.
		return false
	}
}
