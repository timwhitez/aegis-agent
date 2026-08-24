package procutil

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"syscall"
)

var (
	ErrProcessNotFound            = errors.New("recorded process no longer exists")
	ErrProcessIdentityMismatch    = errors.New("recorded process identity does not match")
	ErrProcessIdentityUnavailable = errors.New("recorded process identity cannot be verified")
	ErrProcessSignalUnsupported   = errors.New("identity-bound process signalling is unsupported")
)

const (
	SignalProcessExitNotFound         = 3
	SignalProcessExitIdentityMismatch = 4
	SignalProcessExitUnavailable      = 5
)

// RunSignalProcessCommand implements the hidden launcher helper used by run.sh.
// Exit statuses 3/4 mean the durable process instance is gone or stale; 5
// means the host cannot verify and signal it safely.
func RunSignalProcessCommand(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("__signal-process", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	pid := fs.Int("pid", 0, "")
	identity := fs.String("identity", "", "")
	signalName := fs.String("signal", "0", "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *pid <= 0 || strings.TrimSpace(*identity) == "" {
		_, _ = fmt.Fprintln(stderr, "invalid identity-bound signal request")
		return 2
	}
	sig, err := parseProcessSignal(*signalName)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	if err := SignalProcessInstance(*pid, *identity, sig); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		switch {
		case errors.Is(err, ErrProcessNotFound):
			return SignalProcessExitNotFound
		case errors.Is(err, ErrProcessIdentityMismatch):
			return SignalProcessExitIdentityMismatch
		case errors.Is(err, ErrProcessIdentityUnavailable), errors.Is(err, ErrProcessSignalUnsupported):
			return SignalProcessExitUnavailable
		default:
			return 1
		}
	}
	return 0
}

func parseProcessSignal(value string) (syscall.Signal, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "", "0", "CHECK":
		return 0, nil
	case "TERM", "SIGTERM":
		return syscall.SIGTERM, nil
	case "KILL", "SIGKILL":
		return syscall.SIGKILL, nil
	default:
		if numeric, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && numeric == 0 {
			return 0, nil
		}
		return 0, fmt.Errorf("unsupported process signal %q", value)
	}
}
