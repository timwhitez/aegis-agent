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
// Darwin has no pidfd, so the identity check and kill(2) are two steps. The
// verified instance cannot release its PID before its parent reaps it, and
// XNU allocates PIDs sequentially, so a different process taking the PID in
// between would require exit, reap and a full PID-space wrap in that window.
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
