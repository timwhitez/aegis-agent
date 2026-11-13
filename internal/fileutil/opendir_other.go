//go:build !darwin

package fileutil

// openNoSymlinkSingleOpen has no single-syscall equivalent here; callers use
// the per-component O_NOFOLLOW walk, which is cheap on these platforms.
func openNoSymlinkSingleOpen(abs string, flags int) (int, bool) {
	return -1, false
}
