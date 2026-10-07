//go:build linux

package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

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
