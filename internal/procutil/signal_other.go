//go:build !linux

package procutil

import (
	"fmt"
	"syscall"
)

func SignalProcessInstance(pid int, expectedIdentity string, sig syscall.Signal) error {
	return fmt.Errorf("%w on this platform", ErrProcessSignalUnsupported)
}
