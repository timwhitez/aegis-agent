//go:build linux

package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func (p *ownedBrowserProcess) wait(cancel func() error) {
	// WNOWAIT observes exit without reaping. The unreaped leader pins the group
	// identity while we cancel inherited descendants, including redirected ones.
	var info unix.Siginfo
	var observed error
	for {
		observed = unix.Waitid(unix.P_PID, p.cmd.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if !errors.Is(observed, unix.EINTR) {
			break
		}
	}
	p.mu.Lock()
	if observed == nil {
		if err := cancel(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			p.cleanupErr = fmt.Errorf("owned group cancellation: %w", err)
		}
	} else {
		p.cleanupErr = fmt.Errorf("owned exit observation unknown: %w", observed)
	}
	// All Cancel callers use this mutex. From here on, even while Cmd.Wait
	// drains output, no caller can signal a possibly reused numeric PGID.
	p.reaping = true
	p.mu.Unlock()
	p.waitErr = p.cmd.Wait()
	deadline := time.Now().Add(time.Second)
	for {
		err := browserProcessGroupCleanup(p.cmd.Process.Pid)
		if err == nil || time.Now().After(deadline) {
			p.cleanupErr = errors.Join(p.cleanupErr, err)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(p.done)
}

func browserExitSignal(state *os.ProcessState) string {
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return status.Signal().String()
	}
	return ""
}
func browserProcessGroupCleanup(pgid int) error {
	// Confirm owned group descendants, excluding dead zombies. Do not signal
	// after Wait; a reused leader PID is never cleanup authority.
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return fmt.Errorf("process group cleanup unknown: %w", err)
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("process group cleanup unknown: %w", err)
		}
		end := strings.LastIndexByte(string(data), ')')
		if end < 0 {
			continue
		}
		fields := strings.Fields(string(data[end+1:]))
		if len(fields) < 3 {
			continue
		}
		group, _ := strconv.Atoi(fields[2])
		if group == pgid && fields[0] != "Z" && fields[0] != "X" {
			return fmt.Errorf("owned process group %d cleanup unknown: live descendant %s", pgid, entry.Name())
		}
	}
	return nil
}
