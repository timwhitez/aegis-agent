//go:build darwin

package procutil

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func startDarwinIdentityVictim(t *testing.T) (*exec.Cmd, io.WriteCloser) {
	t.Helper()
	cmd := exec.Command("/bin/cat")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd, stdin
}

func TestDarwinProcessIdentityIsStableAndBootScoped(t *testing.T) {
	boot, err := unix.Sysctl("kern.bootsessionuuid")
	if err != nil || strings.TrimSpace(boot) == "" {
		t.Fatalf("kern.bootsessionuuid unavailable: %q %v", boot, err)
	}
	first, err := ProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, "darwin:"+strings.TrimSpace(boot)+":") {
		t.Fatalf("identity is not boot scoped: %q", first)
	}
	second, err := ProcessIdentity(os.Getpid())
	if err != nil || second != first {
		t.Fatalf("identity is not stable: %q then %q err=%v", first, second, err)
	}
	if ProcessExited(os.Getpid()) {
		t.Fatal("current process reported exited")
	}
}

func TestDarwinProcessIdentityDetectsPIDReuse(t *testing.T) {
	cmd, _ := startDarwinIdentityVictim(t)
	pid := cmd.Process.Pid
	victim, err := ProcessIdentity(pid)
	if err != nil {
		t.Fatal(err)
	}
	// Identity is bound to the PID; a later instance on this PID has a later
	// start second, which SignalProcessInstance tests reject as a mismatch.
	if ProcessExited(pid) {
		t.Fatal("running child reported exited")
	}
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !ProcessExited(pid) {
		if time.Now().After(deadline) {
			t.Fatal("killed child never became a zombie")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The unreaped zombie still pins the PID and its original identity.
	if zombie, err := ProcessIdentity(pid); err != nil || zombie != victim {
		t.Fatalf("zombie identity = %q err=%v, want %q", zombie, err, victim)
	}
	_ = cmd.Wait()
	// After reaping, the PID is either free or held by a new instance whose
	// identity must differ from the reaped one.
	reused, err := ProcessIdentity(pid)
	if err != nil && !errors.Is(err, ErrProcessNotFound) {
		t.Fatalf("reaped pid: %v", err)
	}
	if err == nil && reused == victim {
		t.Fatalf("reaped instance identity %q still reported", victim)
	}
}

func TestDarwinProcessIdentityNonexistentPIDIsProvablyGone(t *testing.T) {
	for _, pid := range []int{999999, 999999999} {
		if _, err := ProcessIdentity(pid); !errors.Is(err, ErrProcessNotFound) {
			t.Fatalf("pid %d: err=%v, want ErrProcessNotFound", pid, err)
		}
		if !ProcessExited(pid) {
			t.Fatalf("pid %d must be provably exited", pid)
		}
	}
	if _, err := ProcessIdentity(0); !errors.Is(err, ErrProcessIdentityUnavailable) {
		t.Fatalf("pid 0: err=%v", err)
	}
	if ProcessExited(0) {
		t.Fatal("invalid pid must not prove exit")
	}
}

func TestDarwinProcessIdentityPermissionErrorsStayConservative(t *testing.T) {
	// launchd is root-owned: kill(1, 0) reports EPERM for ordinary users, and
	// a process that cannot be signalled must never be treated as exited.
	if ProcessExited(1) {
		t.Fatal("launchd reported exited")
	}
	originalRecord, originalProbe := darwinKinfoProcRecord, darwinSignalProbe
	restore := func(record func(int) ([]byte, error), probe func(int) error) {
		darwinKinfoProcRecord, darwinSignalProbe = record, probe
	}
	t.Cleanup(func() { restore(originalRecord, originalProbe) })
	cases := []struct {
		name   string
		record func(int) ([]byte, error)
		probe  func(int) error
		gone   bool
	}{
		{"empty record and EPERM", func(int) ([]byte, error) { return nil, nil }, func(int) error { return unix.EPERM }, false},
		{"empty record and signalable", func(int) ([]byte, error) { return nil, nil }, func(int) error { return nil }, false},
		{"sysctl EPERM", func(int) ([]byte, error) { return nil, unix.EPERM }, func(int) error { return unix.ESRCH }, false},
		{"short record", func(int) ([]byte, error) { return make([]byte, 8), nil }, func(int) error { return unix.ESRCH }, false},
		{"sysctl ESRCH confirmed", func(int) ([]byte, error) { return nil, unix.ESRCH }, func(int) error { return unix.ESRCH }, true},
		{"empty record confirmed", func(int) ([]byte, error) { return nil, nil }, func(int) error { return unix.ESRCH }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restore(tc.record, tc.probe)
			_, err := ProcessIdentity(4242)
			if tc.gone {
				if !errors.Is(err, ErrProcessNotFound) || !ProcessExited(4242) {
					t.Fatalf("err=%v exited=%t, want provably gone", err, ProcessExited(4242))
				}
				return
			}
			if !errors.Is(err, ErrProcessIdentityUnavailable) || errors.Is(err, ErrProcessNotFound) {
				t.Fatalf("err=%v, want identity unavailable", err)
			}
			if ProcessExited(4242) {
				t.Fatal("unverifiable process must stay alive")
			}
		})
	}
}
