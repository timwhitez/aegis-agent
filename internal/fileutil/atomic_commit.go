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
	AtomicCommitBeforeWrite           AtomicCommitStage = "before_write"
	AtomicCommitBeforeParentChainSync AtomicCommitStage = "before_parent_chain_sync"
	AtomicCommitBeforeFileSync        AtomicCommitStage = "before_file_sync"
	AtomicCommitBeforePublish         AtomicCommitStage = "before_publish"
	AtomicCommitAfterPublish          AtomicCommitStage = "after_publish"
	AtomicCommitBeforeDirectorySync   AtomicCommitStage = "before_directory_sync"
	AtomicCommitAfterCommit           AtomicCommitStage = "after_commit"
)

// AtomicCommitOptions is per invocation. Its hook is a private fault-injection
// seam for callers; it is not a global hook or a user-facing execution hook.
type AtomicCommitOptions struct {
	BeforeStage func(AtomicCommitStage) error
	// SyncParentChain publishes existing directory entries before any receipt
	// write. Filesystem roots and mount topology must already be durable, and
	// storage must honor directory fsync. No ancestor is created or chmodded.
	SyncParentChain bool
}

// AtomicCommitFileNoSymlink requires an existing parent directory. The caller
// establishes that directory's durability, or opts into SyncParentChain. This
// operation creates no directory and does not change ancestor permissions.
// Committed means both the file and its publication were synced. An error after
// publication must never be interpreted as proof that the previous file remains.
// The existing AtomicWriteFileNoSymlink contract is deliberately unchanged.
func AtomicCommitFileNoSymlink(path string, data []byte, mode os.FileMode, options AtomicCommitOptions) (outcome AtomicCommitOutcome, err error) {
	outcome = AtomicCommitNotPublished
	if strings.TrimSpace(path) == "" {
		return outcome, errors.New("path is required")
	}
	path = filepath.Clean(path)
	if options.SyncParentChain {
		path, err = filepath.Abs(path)
		if err != nil {
			return outcome, err
		}
	}
	parent, base := filepath.Dir(path), filepath.Base(path)
	if base == "." || base == string(filepath.Separator) {
		return outcome, fmt.Errorf("invalid file path: %s", path)
	}
	directories, err := openAtomicCommitDirectories(parent, options.SyncParentChain)
	if err != nil {
		return outcome, err
	}
	defer closeAtomicCommitDirectories(directories)
	parentFD := directories[len(directories)-1].fd
	validateParent := func() error { return validateAtomicCommitDirectories(directories) }
	step := func(stage AtomicCommitStage) error {
		if options.BeforeStage != nil {
			return options.BeforeStage(stage)
		}
		return nil
	}
	opts := renameAtNoSymlinkOptions{allowRegular: true, replaceExisting: true,
		sourceSymlinkKind: "path", sourceUnsupportedFormat: "refusing to publish non-regular file: %s",
		targetSymlinkKind: "path", targetUnsupportedFormat: "refusing to replace non-regular file: %s"}
	if err := validateRenameTargetAtNoSymlink(parentFD, base, path, opts); err != nil {
		return outcome, err
	}
	if options.SyncParentChain {
		// Existence is not durability proof. Sync children and then containing
		// entries up to the pre-existing filesystem root, even on a fresh Store.
		for i := len(directories) - 1; i >= 0; i-- {
			if err := step(AtomicCommitBeforeParentChainSync); err != nil {
				return outcome, err
			}
			if err := validateParent(); err != nil {
				return outcome, err
			}
			if err := unix.Fsync(directories[i].fd); err != nil {
				return outcome, fmt.Errorf("sync receipt directory ancestor %s: %w", directories[i].path, err)
			}
			if err := validateParent(); err != nil {
				return outcome, err
			}
		}
	}
	tmp, name, err := createTempAtNoSymlink(parentFD, parent, "."+base+".*.tmp")
	if err != nil {
		return outcome, err
	}
	defer unlinkTempAt(parentFD, name, false)
	defer tmp.Close()
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
	if err := validateParent(); err != nil {
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
	if err := validateParent(); err != nil {
		return outcome, err
	}
	if err := step(AtomicCommitBeforeDirectorySync); err != nil {
		return outcome, err
	}
	if err := validateParent(); err != nil {
		return outcome, err
	}
	if err := unix.Fsync(parentFD); err != nil {
		return outcome, err
	}
	if err := validateParent(); err != nil {
		return outcome, err
	}
	outcome = AtomicCommitCommitted
	if err := step(AtomicCommitAfterCommit); err != nil {
		return outcome, err
	}
	return outcome, nil
}

type atomicCommitDirectory struct {
	fd   int
	path string
}

func openAtomicCommitDirectories(parent string, wholeChain bool) ([]atomicCommitDirectory, error) {
	if !wholeChain {
		fd, err := openDirNoSymlink(parent)
		if err != nil {
			return nil, err
		}
		return []atomicCommitDirectory{{fd: fd, path: parent}}, nil
	}
	abs, err := filepath.Abs(parent)
	if err != nil {
		return nil, err
	}
	root, parts := splitAbsolutePath(filepath.Clean(abs))
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	directories := []atomicCommitDirectory{{fd: fd, path: root}}
	current := root
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		childFD, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			closeAtomicCommitDirectories(directories)
			return nil, fmt.Errorf("open receipt directory ancestor %s: %w", current, err)
		}
		directories = append(directories, atomicCommitDirectory{fd: childFD, path: current})
		fd = childFD
	}
	if err := validateAtomicCommitDirectories(directories); err != nil {
		closeAtomicCommitDirectories(directories)
		return nil, err
	}
	return directories, nil
}

func validateAtomicCommitDirectories(directories []atomicCommitDirectory) error {
	for i, directory := range directories {
		if err := ensureDirFDStillAtPath(directory.fd, directory.path); err != nil {
			return err
		}
		if i == 0 {
			continue
		}
		var child, entry unix.Stat_t
		if err := unix.Fstat(directory.fd, &child); err != nil {
			return err
		}
		if err := unix.Fstatat(directories[i-1].fd, filepath.Base(directory.path), &entry, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if entry.Mode&unix.S_IFMT != unix.S_IFDIR || child.Dev != entry.Dev || child.Ino != entry.Ino {
			return fmt.Errorf("receipt directory ancestor changed: %s", directory.path)
		}
	}
	return nil
}

func closeAtomicCommitDirectories(directories []atomicCommitDirectory) {
	for i := len(directories) - 1; i >= 0; i-- {
		_ = unix.Close(directories[i].fd)
	}
}
