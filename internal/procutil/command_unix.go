//go:build unix

package procutil

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// PrepareCommandCancellation returns a command whose Run/Wait observe leader
// exit before reaping it where supported. Calling the original cmd.Run/Wait
// bypasses this guard.
func PrepareCommandCancellation(cmd *exec.Cmd) *Command {
	if cmd == nil {
		return nil
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	var mu sync.Mutex
	reaping := false
	cmd.Cancel = func() error {
		mu.Lock()
		defer mu.Unlock()
		if reaping || cmd.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			if killErr := cmd.Process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
				return err
			}
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
	return &Command{Cmd: cmd, wait: func() error {
		if cmd.Process == nil || cmd.ProcessState != nil {
			return cmd.Wait()
		}
		// Exit observation leaves the leader unreaped, pinning its numeric PGID
		// until every in-flight Cancel has finished. Normal exit does not clean
		// up background jobs. Observation failure also closes the signal gate:
		// an unverified numeric PGID must never be used for cancellation.
		return waitAfterExitObservation(waitCommandExit(cmd.Process.Pid), func() {
			mu.Lock()
			reaping = true
			mu.Unlock()
		}, cmd.Wait)
	}}
}
