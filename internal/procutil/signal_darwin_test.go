//go:build darwin

package procutil

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// launcherDarwinIdentity re-derives the identity exactly as run.sh records it
// on macOS: boot session UUID plus BSD ps(1) lstart in C locale/UTC.
func launcherDarwinIdentity(t *testing.T, pid int) string {
	t.Helper()
	script := `set -euo pipefail
boot="$(/usr/sbin/sysctl -n kern.bootsessionuuid)"
started="$(LC_ALL=C TZ=UTC0 /bin/ps -o lstart= -p "$1")"
read -r wday month mday clock year extra <<<"$started"
[[ -n "$year" && -z "$extra" ]]
start_epoch="$(LC_ALL=C TZ=UTC0 /bin/date -j -f "%a %b %d %T %Y" "$wday $month $mday $clock $year" +%s)"
printf 'darwin:%s:%s' "$boot" "$start_epoch"`
	out, err := exec.Command("/bin/bash", "-c", script, "bash", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("launcher identity for pid %d: %v", pid, err)
	}
	return string(out)
}

func TestDarwinProcessIdentityMatchesLauncherFormat(t *testing.T) {
	cmd, _ := startDarwinIdentityVictim(t)
	for _, pid := range []int{os.Getpid(), cmd.Process.Pid} {
		got, err := ProcessIdentity(pid)
		if err != nil {
			t.Fatal(err)
		}
		if want := launcherDarwinIdentity(t, pid); got != want {
			t.Fatalf("pid %d: Go identity %q, run.sh identity %q", pid, got, want)
		}
	}
}

func TestDarwinSignalProcessInstanceRejectsMismatchedIdentity(t *testing.T) {
	cmd, _ := startDarwinIdentityVictim(t)
	pid := cmd.Process.Pid
	identity, err := ProcessIdentity(pid)
	if err != nil {
		t.Fatal(err)
	}
	cut := strings.LastIndex(identity, ":")
	start, err := strconv.ParseInt(identity[cut+1:], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	for _, stale := range []string{"darwin:not-the-boot:1", identity[:cut+1] + strconv.FormatInt(start-1, 10)} {
		if err := SignalProcessInstance(pid, stale, syscall.SIGTERM); !errors.Is(err, ErrProcessIdentityMismatch) {
			t.Fatalf("identity %q: err=%v", stale, err)
		}
	}
	if ProcessExited(pid) {
		t.Fatal("mismatched process was signalled")
	}
}

func TestDarwinSignalProcessInstanceRejectsNonDarwinIdentity(t *testing.T) {
	cmd, _ := startDarwinIdentityVictim(t)
	for _, identity := range []string{"", "ps:some-start-time", "linux:boot:123"} {
		err := SignalProcessInstance(cmd.Process.Pid, identity, syscall.SIGTERM)
		if !errors.Is(err, ErrProcessIdentityUnavailable) {
			t.Fatalf("identity %q: err=%v", identity, err)
		}
	}
	if err := SignalProcessInstance(0, "darwin:x:1", 0); !errors.Is(err, ErrProcessIdentityUnavailable) {
		t.Fatalf("pid 0: err=%v", err)
	}
	if ProcessExited(cmd.Process.Pid) {
		t.Fatal("process was signalled")
	}
}

func TestDarwinSignalProcessInstanceChecksAndSignalsExactInstance(t *testing.T) {
	cmd, _ := startDarwinIdentityVictim(t)
	pid := cmd.Process.Pid
	identity := launcherDarwinIdentity(t, pid)
	if err := SignalProcessInstance(pid, identity, 0); err != nil {
		t.Fatal(err)
	}
	if err := SignalProcessInstance(pid, identity, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !ProcessExited(pid) {
		if time.Now().After(deadline) {
			t.Fatal("process did not terminate")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// An exited (zombie) instance is gone and is never signalled again.
	if err := SignalProcessInstance(pid, identity, syscall.SIGKILL); !errors.Is(err, ErrProcessNotFound) {
		t.Fatalf("zombie: err=%v", err)
	}
	_ = cmd.Wait()
	if err := SignalProcessInstance(pid, identity, 0); err == nil || (!errors.Is(err, ErrProcessNotFound) && !errors.Is(err, ErrProcessIdentityMismatch)) {
		t.Fatalf("reaped instance: err=%v", err)
	}
	if err := SignalProcessInstance(999999, identity, 0); !errors.Is(err, ErrProcessNotFound) {
		t.Fatalf("unused pid: err=%v", err)
	}
}

func TestDarwinSignalProcessCommandExitCodes(t *testing.T) {
	cmd, _ := startDarwinIdentityVictim(t)
	pid := strconv.Itoa(cmd.Process.Pid)
	identity := launcherDarwinIdentity(t, cmd.Process.Pid)
	var stderr strings.Builder
	cases := []struct {
		args []string
		want int
	}{
		{[]string{"--pid", pid, "--identity", identity, "--signal", "0"}, 0},
		{[]string{"--pid", pid, "--identity", "darwin:not-the-boot:1", "--signal", "TERM"}, SignalProcessExitIdentityMismatch},
		{[]string{"--pid", pid, "--identity", "linux:boot:1", "--signal", "TERM"}, SignalProcessExitUnavailable},
		{[]string{"--pid", "999999", "--identity", identity, "--signal", "0"}, SignalProcessExitNotFound},
	}
	for _, tc := range cases {
		if got := RunSignalProcessCommand(tc.args, &stderr); got != tc.want {
			t.Fatalf("%v: exit=%d want %d stderr=%s", tc.args, got, tc.want, stderr.String())
		}
	}
	if ProcessExited(cmd.Process.Pid) {
		t.Fatal("verification-only and rejected requests must not stop the process")
	}
}
