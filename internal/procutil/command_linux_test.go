//go:build linux

package procutil

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCommandCancelAfterReapWhileDrainingPipe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := PrepareCommandCancellation(exec.CommandContext(ctx, "sh", "-c", `printf ready; sleep 60 & echo $! > "$1"`, "sh", pidFile))
	release := make(chan struct{})
	defer func() {
		if release != nil {
			close(release)
		}
	}()
	w := &blockedCommandOutput{started: make(chan struct{}), release: release}
	cmd.Stdout = w
	cmd.WaitDelay = 200 * time.Millisecond
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Waitid observes only this child and does not reap it. ECHILD proves the
	// wrapper has reaped the leader while its grandchild still holds stdout.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-w.started:
	case <-time.After(3 * time.Second):
		t.Fatal("output copy did not start")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_PID, cmd.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT|unix.WNOHANG, nil)
		if errors.Is(err, unix.ECHILD) {
			break
		}
		if err != nil && !errors.Is(err, unix.EINTR) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("leader was not reaped")
		}
		time.Sleep(time.Millisecond)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	child, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Kill(); _ = child.Release() })
	if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("Cancel during pipe drain after reap = %v", err)
	}
	cancel()
	close(release)
	release = nil
	select {
	case err := <-done:
		if !errors.Is(err, exec.ErrWaitDelay) {
			t.Fatalf("Wait = %v, want exec.ErrWaitDelay", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WaitDelay did not bound pipe drain")
	}
}

type blockedCommandOutput struct {
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (w *blockedCommandOutput) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.release
	return len(p), nil
}
