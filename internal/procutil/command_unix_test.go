//go:build linux || darwin || dragonfly || freebsd || netbsd

package procutil

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestCommandCancelAfterWaitDoesNotSignalGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := PrepareCommandCancellation(exec.CommandContext(context.Background(), "sh", "-c", `sleep 60 </dev/null >/dev/null 2>&1 & echo $! > "$1"`, "sh", pidFile))
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
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
	if err := child.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("normal shell exit killed background child: %v", err)
	}
	// A surviving owned child makes the old numeric group signal succeed.
	// No PID reuse or signal to an unrelated process is needed to expose it.
	if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("Cancel after leader reaped = %v, want os.ErrProcessDone", err)
	}
}

func TestCommandCancelKillsGroupBeforeWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := PrepareCommandCancellation(exec.CommandContext(ctx, "sh", "-c", "sleep 60 & wait"))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatal("cancelled command succeeded")
	}
	if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("Cancel after cancelled Wait = %v", err)
	}
}
