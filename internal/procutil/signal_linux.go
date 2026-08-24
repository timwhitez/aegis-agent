//go:build linux

package procutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func SignalProcessInstance(pid int, expectedIdentity string, sig syscall.Signal) error {
	if pid <= 0 {
		return fmt.Errorf("%w: invalid pid %d", ErrProcessIdentityUnavailable, pid)
	}
	expectedIdentity = strings.TrimSpace(expectedIdentity)
	if !strings.HasPrefix(expectedIdentity, "linux:") {
		return fmt.Errorf("%w: unsupported identity %q", ErrProcessIdentityUnavailable, expectedIdentity)
	}
	pidfd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		if errors.Is(err, unix.ESRCH) {
			return ErrProcessNotFound
		}
		if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) {
			return fmt.Errorf("%w: pidfd_open: %v", ErrProcessSignalUnsupported, err)
		}
		return fmt.Errorf("open pidfd for %d: %w", pid, err)
	}
	defer unix.Close(pidfd)
	if err := unix.PidfdSendSignal(pidfd, 0, nil, 0); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return ErrProcessNotFound
		}
		if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) {
			return fmt.Errorf("%w: pidfd_send_signal: %v", ErrProcessSignalUnsupported, err)
		}
		return fmt.Errorf("verify pidfd for %d: %w", pid, err)
	}
	actualIdentity, err := linuxProcessIdentity(pid)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrProcessNotFound
		}
		return fmt.Errorf("%w: %v", ErrProcessIdentityUnavailable, err)
	}
	if actualIdentity != expectedIdentity {
		return fmt.Errorf("%w: expected %q, got %q", ErrProcessIdentityMismatch, expectedIdentity, actualIdentity)
	}
	if err := unix.PidfdSendSignal(pidfd, unix.Signal(sig), nil, 0); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return ErrProcessNotFound
		}
		if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) {
			return fmt.Errorf("%w: pidfd_send_signal: %v", ErrProcessSignalUnsupported, err)
		}
		return fmt.Errorf("signal pidfd for %d: %w", pid, err)
	}
	return nil
}

func linuxProcessIdentity(pid int) (string, error) {
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	statData, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", err
	}
	statText := strings.TrimSpace(string(statData))
	closeParen := strings.LastIndex(statText, ")")
	if closeParen < 0 || closeParen+1 >= len(statText) {
		return "", fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	fields := strings.Fields(statText[closeParen+1:])
	const startTimeIndex = 22 - 3
	if len(fields) <= startTimeIndex {
		return "", fmt.Errorf("short /proc/%d/stat", pid)
	}
	boot := strings.TrimSpace(string(bootID))
	startTicks := strings.TrimSpace(fields[startTimeIndex])
	if boot == "" || startTicks == "" {
		return "", fmt.Errorf("empty process identity for pid %d", pid)
	}
	return "linux:" + boot + ":" + startTicks, nil
}
