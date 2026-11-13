//go:build darwin

package fileutil

import "golang.org/x/sys/unix"

// openNoSymlinkSingleOpen opens an absolute, cleaned path with one open(2)
// carrying O_NOFOLLOW_ANY, so the kernel rejects a symlink in any path
// component, including the last one, atomically. That is the same guarantee as
// the per-component O_NOFOLLOW walk, but costs one open instead of one per
// component; on macOS hosts with an Endpoint Security client every open is an
// authorization round trip of several milliseconds. O_NOFOLLOW_ANY exists since
// macOS 11, below Go's minimum supported macOS. Any error reports ok=false and
// the caller repeats the walk, which keeps error classification and test hooks
// unchanged.
func openNoSymlinkSingleOpen(abs string, flags int) (int, bool) {
	fd, err := unix.Open(abs, flags|unix.O_NOFOLLOW_ANY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, false
	}
	return fd, true
}
