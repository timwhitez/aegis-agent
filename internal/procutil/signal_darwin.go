//go:build darwin

package procutil

import (
	"errors"
	"fmt"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// SignalProcessInstance signals pid only when it still is the process instance
// recorded as expectedIdentity ("darwin:<kern.bootsessionuuid>:<start s>", see
// ProcessIdentity). It fails closed: a non-darwin or unverifiable identity
// returns ErrProcessIdentityUnavailable, a different instance returns
// ErrProcessIdentityMismatch, and an unused PID or an already-exited zombie
// returns ErrProcessNotFound; none of them sends a signal.
//
// Darwin has no pidfd, so the identity check and kill(2) are two separate
// system calls. Signalling a different process through that gap relies on one
// kernel assumption: XNU never hands a freed PID straight back out. forkproc
// (bsd/kern/kern_fork.c) issues PIDs in increasing order from the last PID it
// issued, wraps from PID_MAX (99999) to 100, skips every PID still naming a
// process (including an unreaped zombie), process group or session, and does
// not randomize. A PID therefore returns only when that counter next reaches
// it. For kill(2) to hit a new owner, the verified instance would have to
// exit, be reaped by its parent, and the very next allocation would have to
// land exactly on pid, all between two consecutive system calls of this
// short-lived helper. That residual race is accepted; were XNU to randomize or
// recycle PIDs eagerly, this check-then-kill would no longer be sound.
// Everything else stays fail-closed: no signal is sent on any lookup doubt,
// and ESRCH from kill(2) reports ErrProcessNotFound.
func SignalProcessInstance(pid int, expectedIdentity string, sig syscall.Signal) error {
	if pid <= 0 {
		return fmt.Errorf("%w: invalid pid %d", ErrProcessIdentityUnavailable, pid)
	}
	expectedIdentity = strings.TrimSpace(expectedIdentity)
	if !strings.HasPrefix(expectedIdentity, "darwin:") {
		return fmt.Errorf("%w: unsupported identity %q", ErrProcessIdentityUnavailable, expectedIdentity)
	}
	record, err := readDarwinProcess(pid)
	if err != nil {
		return err
	}
	if record.identity != expectedIdentity {
		return fmt.Errorf("%w: expected %q, got %q", ErrProcessIdentityMismatch, expectedIdentity, record.identity)
	}
	if record.zombie {
		// The recorded instance already exited; only its exit status remains.
		return ErrProcessNotFound
	}
	if err := unix.Kill(pid, sig); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return ErrProcessNotFound
		}
		return fmt.Errorf("signal pid %d: %w", pid, err)
	}
	return nil
}
