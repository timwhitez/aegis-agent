//go:build dragonfly || freebsd || netbsd

package procutil

import (
	"errors"
	"time"

	"golang.org/x/sys/unix"
)

func waitCommandExit(pid int) error {
	var status unix.WaitStatus
	for {
		_, err := unix.Wait4(pid, &status, unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if status.Exited() || status.Signaled() {
			return nil
		}
		// Wait4 may also report ptrace stops; they do not authorize reaping.
		time.Sleep(10 * time.Millisecond)
	}
}
