//go:build darwin

package runpid

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"aegis-agent/internal/procutil"
)

// TestMain lets this test binary act as the real identity-bound signal
// helper: run.sh runs "$AEGIS_AGENT_SIGNAL_HELPER" __signal-process ..., which
// cmd/aegis-agent dispatches to procutil.RunSignalProcessCommand. The fake
// helpers in run_pid_test.go re-derive identities with run.sh's own shell
// algorithm, so only these tests prove that run.sh and procutil agree.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "__signal-process" {
		os.Exit(procutil.RunSignalProcessCommand(os.Args[2:], os.Stderr))
	}
	os.Exit(m.Run())
}

// launcherChildScript extracts run.sh's real child_script (the code that
// records pid/identity and then execs the web console).
func launcherChildScript(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	const open, closing = "child_script='", "exec \"$@\"'"
	start := strings.Index(text, open)
	if start < 0 {
		t.Fatal("run.sh no longer defines child_script; update this test with the launcher")
	}
	end := strings.Index(text[start:], closing)
	if end < 0 {
		t.Fatal("run.sh child_script no longer ends with exec \"$@\"; update this test with the launcher")
	}
	body := text[start+len(open) : start+end+len(closing)-1]
	return strings.ReplaceAll(body, `'\''`, "'")
}

func readPIDRecord(t *testing.T, path string) (int, string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil && bytes.HasSuffix(data, []byte("\n")) && bytes.Contains(data, []byte("identity=")) {
			var pid int
			var identity string
			for _, line := range strings.Split(string(data), "\n") {
				if value, ok := strings.CutPrefix(line, "pid="); ok {
					pid, _ = strconv.Atoi(value)
				}
				if value, ok := strings.CutPrefix(line, "identity="); ok {
					identity = value
				}
			}
			return pid, identity
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read pid record: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("no pid record at %s (last error %v, data %q)", path, err, data)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLauncherChildIdentityMatchesProcessIdentity(t *testing.T) {
	script := launcherChildScript(t)
	dir := t.TempDir()
	// The launcher pins C locale and UTC for ps/date; caller locale and time
	// zone must not change the recorded identity.
	for i, env := range [][]string{
		nil,
		{"LC_ALL=zh_CN.UTF-8", "TZ=Asia/Shanghai"},
		{"LANG=ja_JP.UTF-8", "LC_TIME=ja_JP.UTF-8", "TZ=America/New_York"},
		{"LC_ALL=de_DE.UTF-8", "TZ=Pacific/Chatham"},
	} {
		pending := filepath.Join(dir, "pending."+strconv.Itoa(i))
		cmd := exec.Command("/bin/bash", "-c", script, "bash", pending, "/bin/sleep", "600")
		cmd.Env = append(os.Environ(), env...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		pid, recorded := readPIDRecord(t, pending)
		live, err := procutil.ProcessIdentity(cmd.Process.Pid)
		if err != nil {
			t.Fatalf("env=%v: ProcessIdentity(%d): %v", env, cmd.Process.Pid, err)
		}
		if pid != cmd.Process.Pid || recorded != live || !strings.HasPrefix(recorded, "darwin:") {
			t.Fatalf("env=%v: run.sh recorded pid=%d identity=%q, procutil has pid=%d identity=%q (stderr=%q)", env, pid, recorded, cmd.Process.Pid, live, stderr.String())
		}
		var helperErr bytes.Buffer
		if code := procutil.RunSignalProcessCommand([]string{"--pid", strconv.Itoa(pid), "--identity", recorded, "--signal", "0"}, &helperErr); code != 0 {
			t.Fatalf("env=%v: real signal helper rejected run.sh identity: exit %d %s", env, code, helperErr.String())
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
}

func TestRecordedWebConsoleInstanceWithRealSignalHelper(t *testing.T) {
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := copyLauncher(t)
	fake := filepath.Join(root, "fake-aegis-agent")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'web console listening on test'\nexec sleep 600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"AEGIS_AGENT_SKIP_BUILD=1",
		"AEGIS_AGENT_BIN=" + fake,
		"AEGIS_AGENT_SIGNAL_HELPER=" + helper,
		"AEGIS_AGENT_WEB_LOG=" + filepath.Join(root, "web.log"),
	}
	// start verifies the pending record with the helper before publishing it.
	output, err := launcher(t, root, "start", env...)
	if err != nil {
		t.Fatalf("start with the real signal helper failed: %v\n%s", err, output)
	}
	pid, recorded := readPIDRecord(t, pidFile(root))
	// Identity-bound, so a reused PID is never signalled.
	t.Cleanup(func() { _ = procutil.SignalProcessInstance(pid, recorded, syscall.SIGKILL) })
	live, err := procutil.ProcessIdentity(pid)
	if err != nil || live != recorded {
		t.Fatalf("recorded identity %q, procutil.ProcessIdentity(%d) = %q, %v", recorded, pid, live, err)
	}
	output, err = launcher(t, root, "stop", env...)
	if err != nil {
		t.Fatalf("stop with the real signal helper failed: %v\n%s", err, output)
	}
	if !strings.Contains(output, "webconsole stopped") && !strings.Contains(output, "webconsole force-stopped") {
		t.Fatalf("output=%s", output)
	}
	if _, err := os.Stat(pidFile(root)); !os.IsNotExist(err) {
		t.Fatalf("record retained: %v", err)
	}
	if !procutil.ProcessExited(pid) {
		if current, err := procutil.ProcessIdentity(pid); err == nil && current == recorded {
			t.Fatalf("stopped web console %d is still running", pid)
		}
	}
}
