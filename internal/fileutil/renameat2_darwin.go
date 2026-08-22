//go:build darwin

package fileutil

import "golang.org/x/sys/unix"

func renameAtNoReplaceSyscall(oldParentFD int, oldBase string, newParentFD int, newBase string) error {
	return unix.RenameatxNp(oldParentFD, oldBase, newParentFD, newBase, unix.RENAME_EXCL)
}
