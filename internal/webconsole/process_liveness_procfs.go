//go:build !darwin

package webconsole

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
)

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
