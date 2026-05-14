//go:build linux

package hooks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"go-cli-agent/internal/config"
)

func TestManagerTimeoutKillsChildProcessGroup(t *testing.T) {
	workdir := t.TempDir()
	manager := New(config.HooksConfig{
		UserMessage: []config.HookDefinition{
			{
				Name:       "timeout-child",
				Command:    []string{"/bin/sh", "-c", "sleep 30 & echo $! > child.pid; wait"},
				TimeoutSec: 1,
			},
		},
	}, workdir)
	var command map[string]any
	manager.SetEmitter(func(eventType string, data map[string]any) {
		if eventType == "hook.command" {
			command = data
		}
	})

	if _, err := manager.Trigger(context.Background(), "user.message", map[string]any{"text": "hello"}); err != nil {
		t.Fatalf("fail-open timeout should preserve payload, got %v", err)
	}
	if command == nil || command["timeout"] != true {
		t.Fatalf("expected timeout command metadata, got %#v", command)
	}
	pidBytes, err := os.ReadFile(filepath.Join(workdir, "child.pid"))
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatalf("parse child pid: %v", err)
	}
	t.Cleanup(func() {
		if hookLinuxProcessExists(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !hookLinuxProcessExists(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed-out hook child process %d is still alive", pid)
}

func hookLinuxProcessExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
