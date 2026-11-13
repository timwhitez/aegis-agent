package session

import (
	"bytes"
	"runtime"
	"testing"
	"time"
)

// waitUnlessLockDeadlocked waits for done and fails only on evidence of a
// lock deadlock: the goroutine running frame stays blocked acquiring a Go
// mutex or an flock(2) lock across consecutive stack samples. A slow but
// progressing operation (open/fsync latency under -race or endpoint-security
// load) is never mistaken for a deadlock, unlike a fixed time window. The
// backstop only bounds hangs of any other kind.
func waitUnlessLockDeadlocked(t *testing.T, done <-chan error, frame string) error {
	t.Helper()
	const (
		sampleEvery     = 100 * time.Millisecond
		blockedSamples  = 10 // >= 1s continuously blocked on a lock
		overallBackstop = 2 * time.Minute
	)
	backstop := time.NewTimer(overallBackstop)
	defer backstop.Stop()
	ticker := time.NewTicker(sampleEvery)
	defer ticker.Stop()
	blocked := 0
	for {
		select {
		case err := <-done:
			return err
		case <-backstop.C:
			t.Fatalf("%s did not finish within %s:\n%s", frame, overallBackstop, goroutineStacks(frame))
		case <-ticker.C:
			stack := goroutineStacks(frame)
			if !stackBlockedOnLock(stack) {
				blocked = 0
				continue
			}
			if blocked++; blocked >= blockedSamples {
				t.Fatalf("%s is deadlocked on a lock:\n%s", frame, stack)
			}
		}
	}
}

// goroutineStacks returns the stacks of goroutines that contain frame.
func goroutineStacks(frame string) []byte {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	var out []byte
	for _, goroutine := range bytes.Split(buf, []byte("\n\n")) {
		if bytes.Contains(goroutine, []byte(frame)) {
			out = append(append(out, goroutine...), '\n', '\n')
		}
	}
	return out
}

func stackBlockedOnLock(stack []byte) bool {
	for _, waiter := range []string{"sync.(*Mutex).Lock", "sync.(*RWMutex).Lock", "sync.(*RWMutex).RLock", "unix.Flock(", "syscall.Flock("} {
		if bytes.Contains(stack, []byte(waiter)) {
			return true
		}
	}
	return false
}
