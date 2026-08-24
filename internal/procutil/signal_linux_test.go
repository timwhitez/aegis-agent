//go:build linux

package procutil

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func startSignalVictim(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd
}

func processStillAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func TestSignalProcessInstanceRejectsMismatchedIdentity(t *testing.T) {
	cmd := startSignalVictim(t)
	err := SignalProcessInstance(cmd.Process.Pid, "linux:not-the-boot:1", syscall.SIGTERM)
	if !errors.Is(err, ErrProcessIdentityMismatch) {
		t.Fatalf("err=%v", err)
	}
	if !processStillAlive(cmd.Process.Pid) {
		t.Fatal("mismatched process was signalled")
	}
}

func TestSignalProcessInstanceChecksAndSignalsExactInstance(t *testing.T) {
	cmd := startSignalVictim(t)
	identity, err := linuxProcessIdentity(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := SignalProcessInstance(cmd.Process.Pid, identity, 0); err != nil {
		t.Fatal(err)
	}
	if err := SignalProcessInstance(cmd.Process.Pid, identity, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("process did not terminate")
	}
}

func TestSignalProcessInstanceRejectsNonLinuxIdentity(t *testing.T) {
	cmd := startSignalVictim(t)
	err := SignalProcessInstance(cmd.Process.Pid, "ps:some-start-time", 0)
	if !errors.Is(err, ErrProcessIdentityUnavailable) {
		t.Fatalf("err=%v", err)
	}
	if !processStillAlive(cmd.Process.Pid) {
		t.Fatal("process was signalled")
	}
}
