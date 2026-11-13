//go:build darwin

package session

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestDarwinHostProcessIdentityDetectsPIDReuse(t *testing.T) {
	identity, ok := hostProcessIdentity(os.Getpid())
	if !ok || !strings.HasPrefix(identity, "darwin:") {
		t.Fatalf("expected current darwin process identity, got %q ok=%t", identity, ok)
	}
	if !hostProcessOwnerAlive(os.Getpid(), identity) {
		t.Fatalf("current process identity should be live: %q", identity)
	}
	if hostProcessOwnerAlive(os.Getpid(), identity+"-reused") {
		t.Fatalf("mismatched process identity must reject a reused PID: %q", identity)
	}
}

func TestDarwinHostProcessOwnerAliveProvesExitedOwnersDead(t *testing.T) {
	cmd := exec.Command("/bin/cat")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	pid := cmd.Process.Pid
	identity, ok := hostProcessIdentity(pid)
	if !ok {
		t.Fatalf("expected identity for live child %d", pid)
	}
	if !hostProcessOwnerAlive(pid, identity) {
		t.Fatal("live child owner must be alive")
	}
	// A durable owner that started at a different second is a different
	// instance on this PID (PID reuse) and must not count as alive.
	cut := strings.LastIndex(identity, ":")
	start, err := strconv.ParseInt(identity[cut+1:], 10, 64)
	if err != nil {
		t.Fatalf("identity %q: %v", identity, err)
	}
	if hostProcessOwnerAlive(pid, identity[:cut+1]+strconv.FormatInt(start-1, 10)) {
		t.Fatal("a different process instance on the same PID must not count as the owner")
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if hostProcessOwnerAlive(pid, identity) {
		t.Fatal("exited owner must be provably dead")
	}
	for _, dead := range []int{999999, 999999999} {
		if hostProcessAlive(dead) || hostProcessOwnerAlive(dead, "") {
			t.Fatalf("nonexistent pid %d must be provably dead", dead)
		}
	}
	// launchd cannot be signalled by ordinary users (EPERM); unknown stays alive.
	if !hostProcessAlive(1) {
		t.Fatal("permission-denied process must stay alive")
	}
}
