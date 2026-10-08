//go:build darwin

package procutil

import (
	"syscall"
	"time"
	"unsafe"
)

func waitCommandExit(pid int) error {
	// Darwin's siginfo_t is 104 bytes on both supported architectures. Only
	// si_code is needed; keep the remaining fields opaque and 8-byte aligned.
	var info struct {
		signo, errno, code int32
		pid, uid, status   int32
		padding            [10]uint64
	}
	const pPID = 1
	for {
		_, _, err := syscall.Syscall6(syscall.SYS_WAITID, pPID, uintptr(pid), uintptr(unsafe.Pointer(&info)), syscall.WEXITED|syscall.WNOWAIT, 0, 0)
		if err == syscall.EINTR {
			continue
		}
		if err != 0 {
			return err
		}
		// Some Darwin versions report stopped children despite WEXITED (Go
		// issue 19314). Only CLD_EXITED/KILLED/DUMPED authorize reaping.
		if info.code >= 1 && info.code <= 3 {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
}
