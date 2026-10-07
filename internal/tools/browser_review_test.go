//go:build linux

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aegis-agent/internal/config"
	"aegis-agent/internal/procutil"
	"aegis-agent/internal/session"
)

func TestBrowserCLIUnknownCleanupPreservesErrorResult(t *testing.T) {
	collector := newCommandOutputCollector(ExecContext{Config: config.Default()}, "browser_exec")
	done := make(chan struct{})
	close(done)
	p := &ownedBrowserProcess{done: done, collector: collector, cleanupErr: errors.New("owned CLI survivor unknown")}
	if err := p.stop(); err == nil {
		t.Fatal("cleanup error missing")
	}
	result := collector.finalize(commandOutputResultOptions{IsError: true, Metadata: map[string]any{"cleanup": "unknown"}})
	if !result.IsError || result.Metadata["cleanup"] != "unknown" {
		t.Fatal("process stop finalized the collector before cleanup error metadata", result)
	}
}

func TestBrowserNoGroupSignalAfterReapDuringOutputDrain(t *testing.T) {
	barrier := filepath.Join(t.TempDir(), "exit")
	code := "import os,time;\nwhile not os.path.exists(" + strconv.Quote(barrier) + "): time.sleep(.01)\nprint('drain')"
	cmd := exec.CommandContext(context.Background(), "/usr/bin/python3", "-c", code)
	procutil.PrepareCommandCancellation(cmd)
	var signals atomic.Int32
	// Observe the dangerous numeric-group path without ever signaling a reused PID.
	cmd.Cancel = func() error { signals.Add(1); return os.ErrProcessDone }
	p, err := startOwnedBrowserProcess(cmd, newCommandOutputCollector(ExecContext{Config: config.Default()}, "browser_test"))
	if err != nil {
		t.Fatal(err)
	}
	p.collector.mu.Lock()
	if err := os.WriteFile(barrier, nil, 0600); err != nil {
		p.collector.mu.Unlock()
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid))); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			p.collector.mu.Unlock()
			t.Fatal("leader was not reaped during blocked output drain")
		}
		time.Sleep(5 * time.Millisecond)
	}
	before := signals.Load()
	stopped := make(chan error, 1)
	go func() { stopped <- p.stop() }()
	time.Sleep(50 * time.Millisecond)
	after := signals.Load()
	p.collector.mu.Unlock()
	<-stopped
	if after != before {
		t.Fatal("cleanup invoked numeric group cancellation after the leader was reaped")
	}
}

func TestBrowserCLIDescendantsSettledOnNormalExit(t *testing.T) {
	r, ec := browserFixture(t)
	pidFile := filepath.Join(t.TempDir(), "descendant")
	child := "import os,time;open(" + strconv.Quote(pidFile) + ",'w').write(str(os.getpid()));time.sleep(30)"
	code := "import subprocess,os,time\np=subprocess.Popen(['/usr/bin/python3','-c'," + strconv.Quote(child) + "],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)\nwhile not os.path.exists(" + strconv.Quote(pidFile) + "): time.sleep(.01)\nprint(os.getpgrp())"
	result := browserRun(t, r, ec, code)
	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(pidBytes))
	if err != nil {
		t.Fatal(err)
	}
	// Bind the handle while this test-owned child exists; never recover a kill PID.
	if child, err := os.FindProcess(pid); err == nil {
		t.Cleanup(func() { _ = child.Kill(); _ = child.Release() })
	}
	pgid, err := strconv.Atoi(strings.TrimSpace(result.DisplayOutput))
	if err != nil {
		t.Fatal(result, err)
	}
	if err := browserProcessGroupCleanup(pgid); err != nil {
		t.Fatalf("CLI descendant survived normal exit: %v", err)
	}
	if result.IsError || result.Metadata["cleanup"] != "confirmed" {
		t.Fatal("CLI group settlement was not reported", result)
	}
}

func TestBrowserScreenshotRawJSONWithTinyBudgetLongRoot(t *testing.T) {
	r, ec := browserFixture(t)
	meta, err := ec.Store.LoadMetadata(ec.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	padding := strings.Repeat("w", 180)
	ec.Store = session.NewStore(filepath.Join(t.TempDir(), padding, padding, padding, "sessions"))
	if err := ec.Store.Create(meta, session.State{Status: session.StatusRunning}); err != nil {
		t.Fatal(err)
	}
	ec.EphemeralArtifactRoot = filepath.Join(ec.Store.SessionDir(ec.SessionID), "artifacts", "tool-outputs")
	ec.Config.Runtime.ToolOutput.LLMOutputMaxBytes = 512
	ec.Config.Runtime.ToolOutput.DisplayOutputMaxBytes = 512
	result, err := r.Execute(context.Background(), "browser_screenshot", ec, json.RawMessage(`{}`))
	if err != nil || result.IsError || result.Metadata["screenshot_complete"] != true {
		t.Fatal("valid long-path screenshot JSON was lost to presentation formatting", result, err)
	}
}
