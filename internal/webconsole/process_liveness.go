package webconsole

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
)

func hostProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if pid == os.Getpid() {
		return true
	}
	if _, err := os.Stat("/proc"); err != nil {
		return true
	}
	if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid))); err != nil {
		return !errors.Is(err, os.ErrNotExist)
	}
	return true
}
