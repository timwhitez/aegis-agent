//go:build !unix

package procutil

import (
	"os/exec"
	"time"
)

// PrepareCommandCancellation degrades to the portable subset on platforms
// without POSIX process groups (windows, plan9, js, ...): the process group
// SIGKILL and Setpgid used by the unix builds are unavailable, so cancellation
// only kills the direct child. WaitDelay is still set so a surviving
// grandchild holding the stdout/stderr pipe cannot block cmd.Wait forever and
// defeat the shell tool timeout.
func PrepareCommandCancellation(cmd *exec.Cmd) *Command {
	if cmd == nil {
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
	return &Command{Cmd: cmd}
}
