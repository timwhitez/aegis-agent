package session

import (
	"os"
	"strconv"
	"strings"
)

// hostProcessIdentity returns a boot-scoped OS process identity when the host
// exposes one, so PID reuse can be distinguished from the process that
// originally created a durable lease. Linux uses boot_id + /proc/<pid>/stat
// starttime (process_liveness_procfs.go); darwin uses kern.bootsessionuuid +
// kern.proc.pid p_starttime (process_liveness_darwin.go). Unknown platforms
// return ok=false and callers retain conservative PID-only behavior.
var hostProcessIdentity = func(pid int) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	return platformProcessIdentity(pid)
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

// ProcessIdentity provides the same boot-scoped identity used by session
// leases. A false result requires conservative PID-only owner checks.
func ProcessIdentity(pid int) (string, bool) {
	return hostProcessIdentity(pid)
}

// ProcessOwnerAlive conservatively checks durable control-operation owners.
// Unavailable host identity information never proves an owner is dead.
func ProcessOwnerAlive(pid int, expectedIdentity string) bool {
	return hostProcessOwnerAlive(pid, expectedIdentity)
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
// stat or sysctl error, permission denied) it returns true so the reaper never
// reclaims a job that might still be owned by a healthy worker.
var hostProcessAlive = func(pid int) bool {
	if pid <= 0 {
		return false
	}
	if pid == os.Getpid() {
		return true
	}
	return platformProcessAlive(pid)
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
