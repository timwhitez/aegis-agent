//go:build !linux && !darwin

package fileutil

import "golang.org/x/sys/unix"

// renameAtNoReplaceSyscall reports "no-replace rename unsupported" on platforms
// without renameat2(RENAME_NOREPLACE); renameNoReplaceUnsupported then routes
// the caller to renameAtNoSymlinkNoReplaceFallback.
func renameAtNoReplaceSyscall(oldParentFD int, oldBase string, newParentFD int, newBase string) error {
	return unix.ENOSYS
}
