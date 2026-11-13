//go:build !darwin

package session

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// platformProcessIdentity reads the Linux boot_id + /proc/<pid>/stat
// starttime identity. Hosts without that procfs layout report ok=false.
func platformProcessIdentity(pid int) (string, bool) {
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", false
	}
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", false
	}
	statText := strings.TrimSpace(string(stat))
	closeParen := strings.LastIndex(statText, ")")
	if closeParen < 0 || closeParen+1 >= len(statText) {
		return "", false
	}
	fields := strings.Fields(statText[closeParen+1:])
	// fields starts at proc stat field 3 (state); starttime is field 22.
	const startTimeIndex = 22 - 3
	if len(fields) <= startTimeIndex {
		return "", false
	}
	boot := strings.TrimSpace(string(bootID))
	startTicks := strings.TrimSpace(fields[startTimeIndex])
	if boot == "" || startTicks == "" {
		return "", false
	}
	return "linux:" + boot + ":" + startTicks, true
}

// platformProcessAlive treats a missing /proc/<pid> as proof of exit. Without
// /proc, or on any other stat error, it stays conservative and reports alive.
func platformProcessAlive(pid int) bool {
	if _, err := os.Stat("/proc"); err != nil {
		return true
	}
	if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid))); err != nil {
		return !errors.Is(err, os.ErrNotExist)
	}
	return true
}
