//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package app

import (
	"errors"
	"io"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

var errTTYReaderStopped = errors.New("TTY reader stopped")

// cancelableTTYReader waits on the caller-owned TTY and an internal wakeup
// pipe. Cancellation only closes the pipe writer; it never closes stdin.
type cancelableTTYReader struct {
	inputFD    int
	wakeRead   *os.File
	wakeWrite  *os.File
	cancelOnce sync.Once
	closeOnce  sync.Once
}

func newCancelableTTYReader(stdin *os.File) (*cancelableTTYReader, error) {
	if stdin == nil {
		return nil, os.ErrInvalid
	}
	wakeRead, wakeWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	return &cancelableTTYReader{
		inputFD:   int(stdin.Fd()),
		wakeRead:  wakeRead,
		wakeWrite: wakeWrite,
	}, nil
}

func (r *cancelableTTYReader) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	for {
		pollFDs := []unix.PollFd{
			{Fd: int32(r.inputFD), Events: unix.POLLIN | unix.POLLHUP | unix.POLLERR},
			{Fd: int32(r.wakeRead.Fd()), Events: unix.POLLIN | unix.POLLHUP | unix.POLLERR},
		}
		if _, err := unix.Poll(pollFDs, -1); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return 0, err
		}
		if pollFDs[1].Revents != 0 {
			return 0, errTTYReaderStopped
		}
		if pollFDs[0].Revents&unix.POLLNVAL != 0 {
			return 0, os.ErrClosed
		}
		if pollFDs[0].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR) == 0 {
			continue
		}
		n, err := unix.Read(r.inputFD, buffer)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if n == 0 && err == nil {
			return 0, io.EOF
		}
		return n, err
	}
}

func (r *cancelableTTYReader) Cancel() {
	r.cancelOnce.Do(func() {
		_ = r.wakeWrite.Close()
	})
}

func (r *cancelableTTYReader) Close() {
	r.closeOnce.Do(func() {
		r.Cancel()
		_ = r.wakeRead.Close()
	})
}
