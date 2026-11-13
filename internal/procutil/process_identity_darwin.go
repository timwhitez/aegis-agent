//go:build darwin

package procutil

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// darwinProcessZombie is XNU's SZOMB p_stat (bsd/sys/proc.h): the process has
// exited and only its exit status remains for the parent to reap.
const darwinProcessZombie = 5

// Test seams for the kernel queries. Production always uses the real sysctl
// and kill(pid, 0) probes.
var (
	darwinKinfoProcRecord = func(pid int) ([]byte, error) { return unix.SysctlRaw("kern.proc.pid", pid) }
	darwinBootSessionUUID = func() (string, error) { return unix.Sysctl("kern.bootsessionuuid") }
	darwinSignalProbe     = func(pid int) error { return unix.Kill(pid, 0) }
)

type darwinProcessRecord struct {
	identity string
	zombie   bool
}

// ProcessIdentity returns the boot-scoped identity of the process instance
// that currently holds pid: "darwin:<kern.bootsessionuuid>:<p_starttime.tv_sec>",
// the same identity run.sh records on macOS (sysctl -n kern.bootsessionuuid
// plus ps -o lstart= as UTC epoch seconds). Like the Linux boot_id +
// /proc/<pid>/stat starttime identity it is meaningful together with the PID:
// XNU records p_starttime once at fork (exec does not change it) and an exited
// process pins its PID until reaped, while new PIDs are allocated
// sequentially, so a reused PID carries a later start time. A reuse within
// the same second would require wrapping the whole PID space and is not
// distinguished.
//
// ErrProcessNotFound means no process holds pid: kern.proc.pid has no record
// and kill(pid, 0) reports ESRCH. Every other failure, including permission
// errors, wraps ErrProcessIdentityUnavailable so callers stay conservative.
func ProcessIdentity(pid int) (string, error) {
	record, err := readDarwinProcess(pid)
	if err != nil {
		return "", err
	}
	return record.identity, nil
}

// ProcessExited reports whether pid provably no longer runs: no process holds
// the PID, or the process is a zombie that already exited and only awaits
// reaping. Lookup failures, including permission errors, never prove exit.
func ProcessExited(pid int) bool {
	record, err := readDarwinProcess(pid)
	if errors.Is(err, ErrProcessNotFound) {
		return true
	}
	return err == nil && record.zombie
}

func readDarwinProcess(pid int) (darwinProcessRecord, error) {
	if pid <= 0 {
		return darwinProcessRecord{}, fmt.Errorf("%w: invalid pid %d", ErrProcessIdentityUnavailable, pid)
	}
	raw, err := darwinKinfoProcRecord(pid)
	if err != nil {
		if errors.Is(err, unix.ESRCH) && darwinPIDUnused(pid) {
			return darwinProcessRecord{}, ErrProcessNotFound
		}
		return darwinProcessRecord{}, fmt.Errorf("%w: sysctl kern.proc.pid.%d: %v", ErrProcessIdentityUnavailable, pid, err)
	}
	if len(raw) == 0 {
		// XNU returns no kinfo_proc for a PID without a process. Require
		// kill(pid, 0) to agree so a filtered or racing sysctl alone never
		// proves an owner dead; EPERM and success both mean "may be alive".
		if darwinPIDUnused(pid) {
			return darwinProcessRecord{}, ErrProcessNotFound
		}
		return darwinProcessRecord{}, fmt.Errorf("%w: kern.proc.pid.%d returned no record for a signalable pid", ErrProcessIdentityUnavailable, pid)
	}
	if len(raw) < unix.SizeofKinfoProc {
		return darwinProcessRecord{}, fmt.Errorf("%w: short kern.proc.pid.%d record (%d bytes)", ErrProcessIdentityUnavailable, pid, len(raw))
	}
	var info unix.KinfoProc
	copy(unsafe.Slice((*byte)(unsafe.Pointer(&info)), unix.SizeofKinfoProc), raw)
	if int(info.Proc.P_pid) != pid {
		return darwinProcessRecord{}, fmt.Errorf("%w: kern.proc.pid.%d returned pid %d", ErrProcessIdentityUnavailable, pid, info.Proc.P_pid)
	}
	start := info.Proc.P_starttime
	if start.Sec <= 0 || start.Usec < 0 || start.Usec >= 1000000 {
		return darwinProcessRecord{}, fmt.Errorf("%w: invalid start time for pid %d", ErrProcessIdentityUnavailable, pid)
	}
	boot, err := darwinBootSessionUUID()
	boot = strings.TrimSpace(boot)
	if err != nil || boot == "" {
		return darwinProcessRecord{}, fmt.Errorf("%w: kern.bootsessionuuid unavailable: %v", ErrProcessIdentityUnavailable, err)
	}
	return darwinProcessRecord{
		identity: fmt.Sprintf("darwin:%s:%d", boot, start.Sec),
		zombie:   info.Proc.P_stat == darwinProcessZombie,
	}, nil
}

// darwinPIDUnused reports whether kill(pid, 0) proves that no process holds
// pid. EPERM means a process exists that this user may not signal.
func darwinPIDUnused(pid int) bool {
	return errors.Is(darwinSignalProbe(pid), unix.ESRCH)
}
