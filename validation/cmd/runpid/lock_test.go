package runpid

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// backgroundLauncher is a run.sh process whose exit can be observed without
// blocking; its stdout and stderr go to a file so no copy goroutine races.
type backgroundLauncher struct {
	cmd    *exec.Cmd
	output string
	done   chan struct{}
	err    error
}

func startBackgroundLauncher(t *testing.T, root, command string, env ...string) *backgroundLauncher {
	t.Helper()
	output := filepath.Join(root, command+"-"+strconv.Itoa(len(env))+"-"+strconv.FormatInt(time.Now().UnixNano(), 10)+".out")
	file, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	cmd := exec.Command("bash", filepath.Join(root, "run.sh"), command)
	cmd.Env = append(cleanAgentEnv(), env...)
	cmd.Stdout = file
	cmd.Stderr = file
	// A process group lets cleanup also stop the launcher's helper and lock
	// children if an assertion fails while they are blocked.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	l := &backgroundLauncher{cmd: cmd, output: output, done: make(chan struct{})}
	go func() {
		l.err = cmd.Wait()
		close(l.done)
	}()
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		select {
		case <-l.done:
		case <-time.After(10 * time.Second):
			t.Errorf("launcher %d did not exit after SIGKILL", cmd.Process.Pid)
		}
	})
	return l
}

func (l *backgroundLauncher) exited() bool {
	select {
	case <-l.done:
		return true
	default:
		return false
	}
}

func (l *backgroundLauncher) wait(t *testing.T, timeout time.Duration) (string, error) {
	t.Helper()
	select {
	case <-l.done:
	case <-time.After(timeout):
		t.Fatalf("launcher %d did not finish within %v", l.cmd.Process.Pid, timeout)
	}
	data, err := os.ReadFile(l.output)
	if err != nil {
		t.Fatal(err)
	}
	return string(data), l.err
}

// waitUntil polls cond until it reports done, returns an error, or timeout.
func waitUntil(t *testing.T, what string, timeout time.Duration, cond func() (bool, error)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		done, err := cond()
		if err != nil {
			t.Fatalf("waiting for %s: %v", what, err)
		}
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", timeout, what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// lockChildPID returns the PID of a live child of parent whose command is the
// run.sh lock primitive (flock(1), or perl taking flock(2)), or 0.
func lockChildPID(t *testing.T, parent int, tool string) int {
	t.Helper()
	out, err := exec.Command("ps", "-A", "-o", "pid=", "-o", "ppid=", "-o", "comm=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		ppid, ppidErr := strconv.Atoi(fields[1])
		if pidErr != nil || ppidErr != nil || ppid != parent {
			continue
		}
		if strings.HasPrefix(filepath.Base(strings.Join(fields[2:], " ")), tool) {
			return pid
		}
	}
	return 0
}

// runtimeLockHeld probes run.sh's lock file with a non-blocking flock(2) from
// a separate open file description.
func runtimeLockHeld(t *testing.T, path string) bool {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open runtime lock: %v", err)
	}
	defer file.Close()
	err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		return false
	}
	if !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatalf("probe runtime lock: %v", err)
	}
	return true
}

// writeGatedSignalHelper records each call as gate/call.<n> and then blocks
// until the test writes a line to the gate/release FIFO. It reports the
// recorded process as gone (exit 3) and never signals anything.
func writeGatedSignalHelper(t *testing.T, root, gate string) string {
	t.Helper()
	path := filepath.Join(root, "gated-signal-helper")
	script := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
gate=%q
n=1
while ! mkdir "$gate/call.$n" 2>/dev/null; do n=$((n + 1)); done
read -r _ <"$gate/release"
exit 3
`, gate)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func releaseGate(t *testing.T, fifo string) {
	t.Helper()
	var fd int
	waitUntil(t, "the gated helper to open its release FIFO", 30*time.Second, func() (bool, error) {
		var err error
		fd, err = syscall.Open(fifo, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
		if errors.Is(err, syscall.ENXIO) {
			return false, nil // no reader yet
		}
		return err == nil, err
	})
	defer syscall.Close(fd)
	if _, err := syscall.Write(fd, []byte("go\n")); err != nil {
		t.Fatalf("release gated helper: %v", err)
	}
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// TestRuntimeLockExcludesSecondLauncherUntilRelease proves that the lock
// run.sh takes on fd 9 (flock(1) where installed, else perl's native flock(2),
// as on macOS) outlives the short-lived lock primitive, keeps a second
// launcher blocked inside its own lock primitive, and lets it proceed only
// after the first launcher has finished its critical section.
func TestRuntimeLockExcludesSecondLauncherUntilRelease(t *testing.T) {
	tool := "flock"
	if _, err := exec.LookPath("flock"); err != nil {
		if _, err := exec.LookPath("perl"); err != nil {
			t.Skip("neither flock(1) nor perl is installed; run.sh fails closed without both")
		}
		tool = "perl"
	}
	t.Logf("run.sh runtime lock on %s uses %s", runtime.GOOS, tool)

	root := copyLauncher(t)
	gate := filepath.Join(root, "gate")
	if err := os.Mkdir(gate, 0o700); err != nil {
		t.Fatal(err)
	}
	release := filepath.Join(gate, "release")
	if err := syscall.Mkfifo(release, 0o600); err != nil {
		t.Fatal(err)
	}
	// A record makes the first launcher consult the helper while it holds the
	// lock; the helper's exit 3 then makes it delete the record before exiting.
	if err := os.MkdirAll(filepath.Dir(pidFile(root)), 0o700); err != nil {
		t.Fatal(err)
	}
	record := fmt.Sprintf("pid=%d\nidentity=%s:runtime-lock-test:1\n", os.Getpid(), runtime.GOOS)
	if err := os.WriteFile(pidFile(root), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	lockFile := filepath.Join(root, ".aegis-agent", "runtime", "webconsole.lock")
	env := []string{"AEGIS_AGENT_SIGNAL_HELPER=" + writeGatedSignalHelper(t, root, gate)}

	first := startBackgroundLauncher(t, root, "status", env...)
	waitUntil(t, "the first launcher to reach its signal helper", 30*time.Second, func() (bool, error) {
		if pathExists(filepath.Join(gate, "call.1")) {
			return true, nil
		}
		if first.exited() {
			return false, errors.New("first launcher exited before reaching the helper")
		}
		return false, nil
	})
	if child := lockChildPID(t, first.cmd.Process.Pid, tool); child != 0 {
		t.Fatalf("first launcher still runs its %s lock primitive (pid %d) inside the critical section", tool, child)
	}
	if !runtimeLockHeld(t, lockFile) {
		t.Fatalf("runtime lock is free while the first launcher is in its critical section; the %s lock did not outlive the lock primitive", tool)
	}

	second := startBackgroundLauncher(t, root, "status", env...)
	secondBlocked := func() (bool, error) {
		if second.exited() {
			out, _ := os.ReadFile(second.output)
			return false, fmt.Errorf("second launcher finished while the first held the lock: %s", out)
		}
		if pathExists(filepath.Join(gate, "call.2")) {
			return false, errors.New("second launcher entered the critical section while the first held the lock")
		}
		return lockChildPID(t, second.cmd.Process.Pid, tool) != 0, nil
	}
	waitUntil(t, "the second launcher to block in its "+tool+" lock primitive", 30*time.Second, secondBlocked)
	// The kernel keeps it there; re-check a few times so a primitive that only
	// passed through (a non-blocking or wrong-file lock) cannot satisfy this.
	for i := 0; i < 10; i++ {
		time.Sleep(20 * time.Millisecond)
		blocked, err := secondBlocked()
		if err != nil || !blocked {
			t.Fatalf("second launcher did not stay blocked on the runtime lock: blocked=%v err=%v", blocked, err)
		}
	}
	if data, err := os.ReadFile(second.output); err != nil || len(data) != 0 {
		t.Fatalf("second launcher produced output while blocked: %q %v", data, err)
	}

	releaseGate(t, release)
	output, err := first.wait(t, 30*time.Second)
	if err != nil || !strings.Contains(output, "webconsole not running") {
		t.Fatalf("first launcher: %v\n%s", err, output)
	}
	output, err = second.wait(t, 30*time.Second)
	if err != nil || !strings.Contains(output, "webconsole not running") {
		t.Fatalf("second launcher did not proceed after release: %v\n%s", err, output)
	}
	// The second launcher found no record to verify, so it ran only after the
	// first launcher's helper returned and the record was removed under lock.
	if pathExists(filepath.Join(gate, "call.2")) {
		t.Fatal("second launcher consulted the helper; it overlapped the first launcher's critical section")
	}
	if pathExists(pidFile(root)) {
		t.Fatal("first launcher did not remove the stale record")
	}
	if runtimeLockHeld(t, lockFile) {
		t.Fatal("runtime lock is still held after both launchers exited")
	}
}
