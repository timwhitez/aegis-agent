package fileutil

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type AtomicCommitOutcome string

const (
	AtomicCommitNotPublished         AtomicCommitOutcome = "not_published"
	AtomicCommitPublishedUnconfirmed AtomicCommitOutcome = "published_unconfirmed"
	AtomicCommitCommitted            AtomicCommitOutcome = "committed"
)

type AtomicCommitStage string

const (
	AtomicCommitBeforeWrite         AtomicCommitStage = "before_write"
	AtomicCommitBeforeFileSync      AtomicCommitStage = "before_file_sync"
	AtomicCommitBeforePublish       AtomicCommitStage = "before_publish"
	AtomicCommitAfterPublish        AtomicCommitStage = "after_publish"
	AtomicCommitBeforeDirectorySync AtomicCommitStage = "before_directory_sync"
	AtomicCommitAfterCommit         AtomicCommitStage = "after_commit"
)

// AtomicCommitOptions is per invocation. Its hook is a private fault-injection
// seam for callers; it is not a global hook or a user-facing execution hook.
type AtomicCommitOptions struct {
	BeforeStage func(AtomicCommitStage) error
}

// AtomicCommitFileNoSymlink requires an existing parent directory. The caller
// establishes that directory's durability; this operation creates no directory.
// Committed means both the file and its publication were synced. An error after
// publication must never be interpreted as proof that the previous file remains.
// The existing AtomicWriteFileNoSymlink contract is deliberately unchanged.
func AtomicCommitFileNoSymlink(path string, data []byte, mode os.FileMode, options AtomicCommitOptions) (outcome AtomicCommitOutcome, err error) {
	outcome = AtomicCommitNotPublished
	if strings.TrimSpace(path) == "" {
		return outcome, errors.New("path is required")
	}
	path = filepath.Clean(path)
	parent, base := filepath.Dir(path), filepath.Base(path)
	if base == "." || base == string(filepath.Separator) {
		return outcome, fmt.Errorf("invalid file path: %s", path)
	}
	parentFD, err := openDirNoSymlink(parent)
	if err != nil {
		return outcome, err
	}
	defer unix.Close(parentFD)
	opts := renameAtNoSymlinkOptions{allowRegular: true, replaceExisting: true,
		sourceSymlinkKind: "path", sourceUnsupportedFormat: "refusing to publish non-regular file: %s",
		targetSymlinkKind: "path", targetUnsupportedFormat: "refusing to replace non-regular file: %s"}
	if err := validateRenameTargetAtNoSymlink(parentFD, base, path, opts); err != nil {
		return outcome, err
	}
	tmp, name, err := createTempAtNoSymlink(parentFD, parent, "."+base+".*.tmp")
	if err != nil {
		return outcome, err
	}
	defer unlinkTempAt(parentFD, name, false)
	defer tmp.Close()
	step := func(stage AtomicCommitStage) error {
		if options.BeforeStage != nil {
			return options.BeforeStage(stage)
		}
		return nil
	}
	if err := step(AtomicCommitBeforeWrite); err != nil {
		return outcome, err
	}
	n, err := tmp.Write(data)
	if err != nil {
		return outcome, err
	}
	if n != len(data) {
		return outcome, io.ErrShortWrite
	}
	if err := tmp.Chmod(mode); err != nil {
		return outcome, err
	}
	if err := step(AtomicCommitBeforeFileSync); err != nil {
		return outcome, err
	}
	if err := tmp.Sync(); err != nil {
		return outcome, err
	}
	tmpInfo, err := tmp.Stat()
	if err != nil {
		return outcome, err
	}
	// Keep the descriptor open through publication. Otherwise an unlinked
	// temporary inode can be reused by a replacement before identity checking.
	if err := step(AtomicCommitBeforePublish); err != nil {
		return outcome, err
	}
	if err := ensureDirFDStillAtPath(parentFD, parent); err != nil {
		return outcome, err
	}
	if _, err := validateRenameSourceAtNoSymlink(parentFD, name, tmp.Name(), opts); err != nil {
		return outcome, err
	}
	info, err := os.Lstat(tmp.Name())
	if err != nil || !os.SameFile(tmpInfo, info) {
		return outcome, errors.New("atomic commit temporary file was replaced")
	}
	if err := validateRenameTargetAtNoSymlink(parentFD, base, path, opts); err != nil {
		return outcome, err
	}
	if err := unix.Renameat(parentFD, name, parentFD, base); err != nil {
		return outcome, err
	}
	outcome = AtomicCommitPublishedUnconfirmed
	if err := tmp.Close(); err != nil {
		return outcome, err
	}
	if err := step(AtomicCommitAfterPublish); err != nil {
		return outcome, err
	}
	if err := ensureDirFDStillAtPath(parentFD, parent); err != nil {
		return outcome, err
	}
	if err := step(AtomicCommitBeforeDirectorySync); err != nil {
		return outcome, err
	}
	if err := ensureDirFDStillAtPath(parentFD, parent); err != nil {
		return outcome, err
	}
	if err := unix.Fsync(parentFD); err != nil {
		return outcome, err
	}
	outcome = AtomicCommitCommitted
	if err := step(AtomicCommitAfterCommit); err != nil {
		return outcome, err
	}
	return outcome, nil
}
