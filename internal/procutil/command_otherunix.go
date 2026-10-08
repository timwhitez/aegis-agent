//go:build unix && !linux && !darwin && !dragonfly && !freebsd && !netbsd

package procutil

func waitCommandExit(pid int) error {
	// Outside the Linux/macOS/WSL support set, retain plain Cmd.Wait and the
	// previous unguarded numeric group cancellation when no observer exists.
	return errExitObservationNotImplemented
}
