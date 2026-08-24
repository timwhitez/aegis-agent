package runpid

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func copyLauncher(t *testing.T) string {
	t.Helper()
	repoRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	data, err := os.ReadFile(filepath.Join(repoRoot, "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "run.sh")
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func cleanAgentEnv() []string {
	var out []string
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "AEGIS_AGENT_") {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func launcher(t *testing.T, root, command string, env ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", filepath.Join(root, "run.sh"), command)
	cmd.Env = append(cleanAgentEnv(), env...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func startVictim(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "600")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd
}

func processAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func pidFile(root string) string {
	return filepath.Join(root, ".aegis-agent", "runtime", "webconsole.pid")
}

func writeSignalHelper(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "signal-helper")
	script := `#!/usr/bin/env bash
set -euo pipefail
[[ "${1:-}" == "__signal-process" ]] || exit 2
shift
pid=""; identity=""; signal=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --pid) pid="$2"; shift 2 ;;
    --identity) identity="$2"; shift 2 ;;
    --signal) signal="$2"; shift 2 ;;
    *) exit 2 ;;
  esac
done
[[ -n "$pid" && -n "$identity" ]] || exit 2
if [[ ! -r "/proc/$pid/stat" ]]; then exit 3; fi
if ! boot="$(cat /proc/sys/kernel/random/boot_id 2>/dev/null)"; then exit 5; fi
if ! stat_text="$(cat "/proc/$pid/stat" 2>/dev/null)"; then exit 3; fi
tail="${stat_text##*) }"
read -r -a fields <<<"$tail"
(( ${#fields[@]} > 19 )) || exit 3
actual="linux:${boot}:${fields[19]}"
[[ "$actual" == "$identity" ]] || exit 4
case "$signal" in
  0) kill -0 "$pid" || exit 3 ;;
  TERM) kill -TERM "$pid" || exit 3 ;;
  KILL) kill -KILL "$pid" || exit 3 ;;
  *) exit 2 ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeNoopSignalHelper(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "noop-signal-helper")
	script := `#!/usr/bin/env bash
set -euo pipefail
[[ "${1:-}" == "__signal-process" ]] || exit 2
shift
pid=""; identity=""; signal=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --pid) pid="$2"; shift 2 ;;
    --identity) identity="$2"; shift 2 ;;
    --signal) signal="$2"; shift 2 ;;
    *) exit 2 ;;
  esac
done
[[ -n "$pid" && -n "$identity" && -n "$signal" ]] || exit 2
[[ -r "/proc/$pid/stat" ]] || exit 3
boot="$(cat /proc/sys/kernel/random/boot_id 2>/dev/null)" || exit 5
stat_text="$(cat "/proc/$pid/stat" 2>/dev/null)" || exit 3
tail="${stat_text##*) }"
read -r -a fields <<<"$tail"
(( ${#fields[@]} > 19 )) || exit 3
[[ "linux:${boot}:${fields[19]}" == "$identity" ]] || exit 4
# Deliberately report successful TERM/KILL delivery without changing the
# process. The launcher must not delete its recovery record until a later
# identity-bound check proves that the exact process instance is gone.
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func linuxIdentity(t *testing.T, pid int) string {
	t.Helper()
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatal(err)
	}
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.TrimSpace(string(stat))
	at := strings.LastIndex(text, ")")
	if at < 0 {
		t.Fatalf("malformed stat: %q", text)
	}
	fields := strings.Fields(text[at+1:])
	return "linux:" + strings.TrimSpace(string(boot)) + ":" + fields[19]
}

func TestLegacyBarePIDNeverSignalsUnrelatedLiveProcess(t *testing.T) {
	root := copyLauncher(t)
	victim := startVictim(t)
	if err := os.MkdirAll(filepath.Dir(pidFile(root)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pidFile(root), []byte(strconv.Itoa(victim.Process.Pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := launcher(t, root, "stop", "AEGIS_AGENT_SIGNAL_HELPER="+writeSignalHelper(t, root))
	if err == nil {
		t.Fatalf("expected legacy record to fail closed: %s", output)
	}
	if !strings.Contains(output, "unverifiable legacy pid record") {
		t.Fatalf("diagnostic: %s", output)
	}
	if !processAlive(victim.Process.Pid) {
		t.Fatal("launcher signalled unrelated process")
	}
}

func TestMismatchedIdentityIsDiscardedWithoutSignal(t *testing.T) {
	root := copyLauncher(t)
	victim := startVictim(t)
	if err := os.MkdirAll(filepath.Dir(pidFile(root)), 0o700); err != nil {
		t.Fatal(err)
	}
	record := fmt.Sprintf("pid=%d\nidentity=linux:not-the-current-process:1\n", victim.Process.Pid)
	if err := os.WriteFile(pidFile(root), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := launcher(t, root, "stop", "AEGIS_AGENT_SIGNAL_HELPER="+writeSignalHelper(t, root))
	if err != nil {
		t.Fatalf("stale identity should be safely discarded: %v\n%s", err, output)
	}
	if !strings.Contains(output, "not running") {
		t.Fatalf("unexpected output: %s", output)
	}
	if !processAlive(victim.Process.Pid) {
		t.Fatal("launcher signalled mismatched process")
	}
	if _, err := os.Stat(pidFile(root)); !os.IsNotExist(err) {
		t.Fatalf("stale record retained: %v", err)
	}
}

func TestRecordedWebConsoleInstanceCanBeStopped(t *testing.T) {
	root := copyLauncher(t)
	fake := filepath.Join(root, "fake-aegis-agent")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'web console listening on test'\nexec sleep 600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	helper := writeSignalHelper(t, root)
	env := []string{
		"AEGIS_AGENT_SKIP_BUILD=1",
		"AEGIS_AGENT_BIN=" + fake,
		"AEGIS_AGENT_SIGNAL_HELPER=" + helper,
		"AEGIS_AGENT_WEB_LOG=" + filepath.Join(root, "web.log"),
	}
	output, err := launcher(t, root, "start", env...)
	if err != nil {
		t.Fatalf("start failed: %v\n%s", err, output)
	}
	data, err := os.ReadFile(pidFile(root))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "pid=") || !strings.Contains(string(data), "identity=linux:") {
		t.Fatalf("record=%q", data)
	}
	output, err = launcher(t, root, "stop", env...)
	if err != nil {
		t.Fatalf("stop failed: %v\n%s", err, output)
	}
	if !strings.Contains(output, "webconsole stopped") && !strings.Contains(output, "webconsole force-stopped") {
		t.Fatalf("output=%s", output)
	}
	if _, err := os.Stat(pidFile(root)); !os.IsNotExist(err) {
		t.Fatalf("record retained: %v", err)
	}
}

func TestDotEnvCannotReplaceSignalHelperOrLauncherPolicy(t *testing.T) {
	root := copyLauncher(t)
	marker := filepath.Join(root, "malicious-used")
	malicious := filepath.Join(root, "malicious-helper")
	if err := os.WriteFile(malicious, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(root, ".env")
	if err := os.WriteFile(envFile, []byte(
		"AEGIS_AGENT_SIGNAL_HELPER="+malicious+"\n"+
			"AEGIS_AGENT_LISTEN=0.0.0.0:3940\n"+
			"AEGIS_AGENT_ALLOW_NETWORK=1\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(root, "fake-aegis-agent")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'web console listening on test'\nexec sleep 600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	trusted := writeSignalHelper(t, root)
	env := []string{
		"AEGIS_AGENT_SKIP_BUILD=1",
		"AEGIS_AGENT_BIN=" + fake,
		"AEGIS_AGENT_SIGNAL_HELPER=" + trusted,
		"AEGIS_AGENT_ENV_FILE=" + envFile,
		"AEGIS_AGENT_WEB_LOG=" + filepath.Join(root, "web.log"),
	}
	output, err := launcher(t, root, "start", env...)
	if err != nil {
		t.Fatalf("start failed: %v\n%s", err, output)
	}
	if !strings.Contains(output, "URL: http://127.0.0.1:3940") {
		t.Fatalf("dotenv changed loopback policy: %s", output)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("dotenv replaced trusted helper: %v", err)
	}
	if output, err := launcher(t, root, "stop", env...); err != nil {
		t.Fatalf("stop failed: %v\n%s", err, output)
	}
}

func TestMalformedPIDRecordBlocksRestart(t *testing.T) {
	root := copyLauncher(t)
	if err := os.MkdirAll(filepath.Dir(pidFile(root)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pidFile(root), []byte("pid=not-a-number\nidentity=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := launcher(t, root, "restart",
		"AEGIS_AGENT_SKIP_BUILD=1",
		"AEGIS_AGENT_BIN=/bin/true",
		"AEGIS_AGENT_SIGNAL_HELPER="+writeSignalHelper(t, root),
	)
	if err == nil {
		t.Fatalf("restart accepted malformed record: %s", output)
	}
	if !strings.Contains(output, "malformed or symlinked pid record") {
		t.Fatalf("diagnostic: %s", output)
	}
}

func waitForCapturedPID(t *testing.T, path string) int {
	t.Helper()
	for i := 0; i < 100; i++ {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process pid was not captured at %s", path)
	return 0
}

func waitForProcessExit(pid int) bool {
	for i := 0; i < 100; i++ {
		if !processAlive(pid) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return !processAlive(pid)
}

func writeCapturingWebConsole(t *testing.T, root, capture string) string {
	t.Helper()
	path := filepath.Join(root, "capturing-aegis-agent")
	script := "#!/bin/sh\nprintf '%s\\n' \"$$\" > \"$CAPTURE_PID\"\necho 'web console listening on test'\nexec sleep 600\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDurableRecordPublishFailureUsesIdentityBoundCleanup(t *testing.T) {
	root := copyLauncher(t)
	capture := filepath.Join(root, "child.pid")
	fake := writeCapturingWebConsole(t, root, capture)
	helper := writeSignalHelper(t, root)
	if err := os.MkdirAll(pidFile(root), 0o700); err != nil {
		t.Fatal(err)
	}
	output, err := launcher(t, root, "start",
		"AEGIS_AGENT_SKIP_BUILD=1",
		"AEGIS_AGENT_BIN="+fake,
		"AEGIS_AGENT_SIGNAL_HELPER="+helper,
		"AEGIS_AGENT_WEB_LOG="+filepath.Join(root, "web.log"),
		"CAPTURE_PID="+capture,
	)
	if err == nil {
		t.Fatalf("start unexpectedly succeeded despite blocked pid record: %s", output)
	}
	if !strings.Contains(output, "durable pid record could not be published") {
		t.Fatalf("missing publication error: %s", output)
	}
	pid := waitForCapturedPID(t, capture)
	if !waitForProcessExit(pid) {
		t.Fatalf("identity-bound cleanup left child %d alive", pid)
	}
}

func TestPendingIdentityMismatchNeverFallsBackToNumericKill(t *testing.T) {
	root := copyLauncher(t)
	capture := filepath.Join(root, "child.pid")
	fake := writeCapturingWebConsole(t, root, capture)
	helper := filepath.Join(root, "always-mismatch-helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 4\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	output, err := launcher(t, root, "start",
		"AEGIS_AGENT_SKIP_BUILD=1",
		"AEGIS_AGENT_BIN="+fake,
		"AEGIS_AGENT_SIGNAL_HELPER="+helper,
		"AEGIS_AGENT_WEB_LOG="+filepath.Join(root, "web.log"),
		"CAPTURE_PID="+capture,
	)
	if err == nil {
		t.Fatalf("mismatched pending identity unexpectedly succeeded: %s", output)
	}
	if !strings.Contains(output, "pending process identity could not be verified") {
		t.Fatalf("missing mismatch error: %s", output)
	}
	pid := waitForCapturedPID(t, capture)
	if !processAlive(pid) {
		t.Fatalf("launcher used a numeric-PID cleanup fallback for %d", pid)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

func writeStatefulHelper(t *testing.T, root string, succeedCalls int) string {
	t.Helper()
	path := filepath.Join(root, "stateful-helper")
	counter := filepath.Join(root, "helper-count")
	script := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
count=0
if [[ -f %q ]]; then count="$(<%q)"; fi
count=$((count + 1))
printf '%%s\n' "$count" > %q
if (( count <= %d )); then exit 0; fi
exit 5
`, counter, counter, counter, succeedCalls)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadinessVerificationFailureRetainsRecoverableRecord(t *testing.T) {
	root := copyLauncher(t)
	capture := filepath.Join(root, "child.pid")
	fake := writeCapturingWebConsole(t, root, capture)
	helper := writeStatefulHelper(t, root, 1) // pending check succeeds; readiness check fails closed.
	output, err := launcher(t, root, "start",
		"AEGIS_AGENT_SKIP_BUILD=1",
		"AEGIS_AGENT_BIN="+fake,
		"AEGIS_AGENT_SIGNAL_HELPER="+helper,
		"AEGIS_AGENT_WEB_LOG="+filepath.Join(root, "web.log"),
		"CAPTURE_PID="+capture,
	)
	if err == nil {
		t.Fatalf("unverifiable readiness unexpectedly succeeded: %s", output)
	}
	if !strings.Contains(output, "durable record retained") {
		t.Fatalf("missing fail-closed recovery diagnostic: %s", output)
	}
	pid := waitForCapturedPID(t, capture)
	if !processAlive(pid) {
		t.Fatalf("unverifiable process %d was signalled", pid)
	}
	if _, err := os.Stat(pidFile(root)); err != nil {
		t.Fatalf("durable recovery record was removed: %v", err)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

func TestStopVerificationFailureRetainsRecordAndProcess(t *testing.T) {
	root := copyLauncher(t)
	victim := startVictim(t)
	if err := os.MkdirAll(filepath.Dir(pidFile(root)), 0o700); err != nil {
		t.Fatal(err)
	}
	identity := linuxIdentity(t, victim.Process.Pid)
	if err := os.WriteFile(pidFile(root), []byte(fmt.Sprintf("pid=%d\nidentity=%s\n", victim.Process.Pid, identity)), 0o600); err != nil {
		t.Fatal(err)
	}
	helper := writeStatefulHelper(t, root, 2) // current check + TERM succeed; follow-up check is unavailable.
	output, err := launcher(t, root, "stop", "AEGIS_AGENT_SIGNAL_HELPER="+helper)
	if err == nil {
		t.Fatalf("unverifiable stop unexpectedly succeeded: %s", output)
	}
	if !strings.Contains(output, "durable record retained") {
		t.Fatalf("missing fail-closed stop diagnostic: %s", output)
	}
	if !processAlive(victim.Process.Pid) {
		t.Fatal("stateful helper test unexpectedly signalled victim")
	}
	if _, err := os.Stat(pidFile(root)); err != nil {
		t.Fatalf("durable record was removed: %v", err)
	}
}

func TestSuccessfulSignalsWithoutConfirmedExitRetainDurableRecord(t *testing.T) {
	root := copyLauncher(t)
	victim := startVictim(t)
	if err := os.MkdirAll(filepath.Dir(pidFile(root)), 0o700); err != nil {
		t.Fatal(err)
	}
	identity := linuxIdentity(t, victim.Process.Pid)
	if err := os.WriteFile(pidFile(root), []byte(fmt.Sprintf("pid=%d\nidentity=%s\n", victim.Process.Pid, identity)), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := launcher(t, root, "stop", "AEGIS_AGENT_SIGNAL_HELPER="+writeNoopSignalHelper(t, root))
	if err == nil {
		t.Fatalf("stop accepted unconfirmed TERM/KILL success: %s", output)
	}
	if !strings.Contains(output, "durable record retained") {
		t.Fatalf("missing recovery diagnostic: %s", output)
	}
	if !processAlive(victim.Process.Pid) {
		t.Fatal("no-op helper test process unexpectedly exited")
	}
	if _, err := os.Stat(pidFile(root)); err != nil {
		t.Fatalf("unconfirmed stop removed durable record: %v", err)
	}
}

func TestPendingRecoveryRecordBlocksAnotherStart(t *testing.T) {
	root := copyLauncher(t)
	victim := startVictim(t)
	pending := pidFile(root) + ".pending.recovery"
	if err := os.MkdirAll(filepath.Dir(pending), 0o700); err != nil {
		t.Fatal(err)
	}
	identity := linuxIdentity(t, victim.Process.Pid)
	if err := os.WriteFile(pending, []byte(fmt.Sprintf("pid=%d\nidentity=%s\n", victim.Process.Pid, identity)), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := launcher(t, root, "start",
		"AEGIS_AGENT_SKIP_BUILD=1",
		"AEGIS_AGENT_BIN=/bin/true",
		"AEGIS_AGENT_SIGNAL_HELPER="+writeSignalHelper(t, root),
	)
	if err == nil {
		t.Fatalf("start ignored a pending recovery process: %s", output)
	}
	if !strings.Contains(output, "recovery records exist") {
		t.Fatalf("missing recovery collision diagnostic: %s", output)
	}
	if !processAlive(victim.Process.Pid) {
		t.Fatal("start signalled the pending recovery process")
	}
	if _, err := os.Stat(pending); err != nil {
		t.Fatalf("pending recovery record was removed: %v", err)
	}
}
