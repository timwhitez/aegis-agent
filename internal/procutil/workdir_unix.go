//go:build unix && !linux

package procutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// Getwd returns the process working directory and fails with the same
// "getwd: no such file or directory" error that Linux reports once that
// directory has been removed. Darwin and BSD getcwd(3) can keep returning the
// stale path of a deleted directory, so the returned path must still resolve
// to the directory "." refers to.
func Getwd() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dot, err := os.Stat(".")
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	info, err := os.Stat(dir)
	switch {
	case err == nil:
		if os.SameFile(dot, info) {
			return dir, nil
		}
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
	default:
		// The path cannot be inspected (for example, an ancestor lost search
		// permission), but getcwd still answered, as it would on Linux.
		return dir, nil
	}
	return "", os.NewSyscallError("getwd", syscall.ENOENT)
}
