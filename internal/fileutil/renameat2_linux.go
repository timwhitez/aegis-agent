//go:build linux

package fileutil

import "golang.org/x/sys/unix"

func renameAtNoReplaceSyscall(oldParentFD int, oldBase string, newParentFD int, newBase string) error {
	return unix.Renameat2(oldParentFD, oldBase, newParentFD, newBase, unix.RENAME_NOREPLACE)
}
