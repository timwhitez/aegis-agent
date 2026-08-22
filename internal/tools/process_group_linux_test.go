//go:build linux

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"aegis-agent/internal/config"
	"aegis-agent/internal/session"
)

func TestShellTimeoutKillsChildProcessGroup(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.CommandTimeoutSec = 1
	store := session.NewStore(t.TempDir())
	workdir := t.TempDir()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		Mode:             session.ModeExec,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyAutonomous,
	}
	state := session.State{
		Status:    session.StatusRunning,
		Phase:     "prepare",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}

	result, err := registry.Execute(context.Background(), "shell", ExecContext{
		SessionID: meta.ID,
		Workdir:   workdir,
		Store:     store,
		Config:    cfg,
	}, json.RawMessage(`{
		"command":"sleep 30 & echo $! > child.pid; wait"
	}`))
	if err != nil {
		t.Fatalf("expected nil error for recoverable command timeout, got result=%#v err=%v", result, err)
	}
	if !result.IsError || result.Metadata[MetadataFailureClass] != FailureClassTimeout {
		t.Fatalf("expected command_timeout failure class, got result=%#v", result)
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
		if linuxProcessExists(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !linuxProcessExists(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed-out shell child process %d is still alive", pid)
}

func linuxProcessExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
