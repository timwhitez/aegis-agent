package procutil

import (
	"errors"
	"fmt"
	"os/exec"
)

var errExitObservationNotImplemented = errors.New("non-reaping exit observation not implemented")

// ErrCommandExitObservation identifies failures to observe exit before reaping.
var ErrCommandExitObservation = errors.New("observe command exit")

// Command keeps group cancellation synchronized with reaping where a non-reaping
// exit observer is available. Use its Run or Wait methods, rather than the
// embedded exec.Cmd methods, after preparation.
// Like exec.Cmd, Start and Wait must each be called at most once.
type Command struct {
	*exec.Cmd
	wait func() error
}

func (c *Command) Run() error {
	if err := c.Start(); err != nil {
		return err
	}
	return c.Wait()
}

func (c *Command) Wait() error {
	if c.wait != nil {
		return c.wait()
	}
	return c.Cmd.Wait()
}

func waitAfterExitObservation(observeErr error, startReaping func(), wait func() error) error {
	if errors.Is(observeErr, errExitObservationNotImplemented) {
		return wait()
	}
	startReaping()
	waitErr := wait()
	if observeErr != nil {
		return errors.Join(fmt.Errorf("%w: %w", ErrCommandExitObservation, observeErr), waitErr)
	}
	return waitErr
}
