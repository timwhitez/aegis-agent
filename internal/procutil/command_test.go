package procutil

import (
	"errors"
	"fmt"
	"syscall"
	"testing"
)

func TestWaitAfterExitObservation(t *testing.T) {
	waitFailure := errors.New("wait failed")
	observeFailure := errors.New("observation failed")
	for _, tc := range []struct {
		name       string
		observeErr error
		waitErr    error
		guard      bool
	}{
		{name: "observed exit", guard: true},
		{name: "unimplemented fallback", observeErr: errExitObservationNotImplemented},
		{name: "wrapped fallback preserves wait error", observeErr: fmt.Errorf("observer: %w", errExitObservationNotImplemented), waitErr: waitFailure},
		{name: "supported syscall unavailable fails closed", observeErr: syscall.ENOSYS, waitErr: waitFailure, guard: true},
		{name: "observation failure fails closed", observeErr: observeFailure, guard: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reaping, waited := false, false
			err := waitAfterExitObservation(tc.observeErr, func() { reaping = true }, func() error {
				if reaping != tc.guard {
					t.Errorf("reaping before Wait = %v, want %v", reaping, tc.guard)
				}
				waited = true
				return tc.waitErr
			})
			if !waited {
				t.Fatal("Wait was not called")
			}
			if reaping != tc.guard {
				t.Fatalf("reaping after Wait = %v, want %v", reaping, tc.guard)
			}
			if tc.waitErr != nil && !errors.Is(err, tc.waitErr) {
				t.Fatalf("error = %v, want wait error %v", err, tc.waitErr)
			}
			if tc.guard && tc.observeErr != nil {
				if !errors.Is(err, tc.observeErr) {
					t.Fatalf("error = %v, want observation error %v", err, tc.observeErr)
				}
			} else if err != tc.waitErr {
				t.Fatalf("error = %v, want only Wait result %v", err, tc.waitErr)
			}
			if errors.Is(err, errExitObservationNotImplemented) {
				t.Fatalf("fallback leaked observation error: %v", err)
			}
		})
	}
}
