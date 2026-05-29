package fileutil

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const MaxRegularFileReadBytes int64 = 16 << 20

var beforeAtomicWriteRename func(tmpPath, path string) error
var beforeMkdirAllNoSymlinkMkdir func(path string) error
var beforeMkdirTempNoSymlinkCreate func(parent string) error
var beforeCreateTempNoSymlinkCreate func(parent string) error
var beforeRemoveDirAllNoSymlinkRemove func(path string) error
var beforeRemoveFileNoSymlinkRemove func(path string) error
var beforeRenamePathNoSymlinkRename func(oldPath, newPath string) error
var beforeChmodAfterAtomicRenameOpen func(path string) error

func AtomicWriteFileNoSymlink(path string, data []byte, mode os.FileMode) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("path is required")
	}
	path = filepath.Clean(path)
	base := filepath.Base(path)
	if base == "." || base == string(filepath.Separator) {
		return fmt.Errorf("invalid file path: %s", path)
	}
	parent := filepath.Dir(path)
	if err := rejectExistingSymlinkAncestors(parent); err != nil {
		return err
	}
	if err := MkdirAllNoSymlink(parent, 0o700); err != nil {
		return err
	}
	if err := rejectExistingSymlinkAncestors(parent); err != nil {
		return err
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	if parentInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to write through symlinked parent: %s", parent)
	}
	if !parentInfo.IsDir() {
		return fmt.Errorf("parent is not a directory: %s", parent)
	}
	if err := rejectSymlinkOrDirectory(path); err != nil {
		return err
	}

	tmp, err := CreateTempNoSymlink(parent, "."+base+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	closed := false
	defer RemoveFileNoSymlink(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	closed = true
	if err := rejectSymlinkOrDirectory(path); err != nil {
		return err
	}
	if !closed {
		_ = tmp.Close()
	}
	if beforeAtomicWriteRename != nil {
		if err := beforeAtomicWriteRename(tmpPath, path); err != nil {
			return err
		}
	}
	if err := renameRegularFileReplacingNoSymlink(tmpPath, path); err != nil {
		return err
	}
	return chmodAfterAtomicRename(path, mode)
}

func renameRegularFileReplacingNoSymlink(oldPath, newPath string) error {
	oldPath = strings.TrimSpace(oldPath)
	newPath = strings.TrimSpace(newPath)
	if oldPath == "" || newPath == "" {
		return errors.New("source and destination paths are required")
	}
	oldPath = filepath.Clean(oldPath)
	newPath = filepath.Clean(newPath)
	if filepath.Dir(oldPath) == oldPath || filepath.Dir(newPath) == newPath {
		return fmt.Errorf("refusing to rename filesystem root: %s -> %s", oldPath, newPath)
	}
	if err := rejectExistingSymlinkAncestors(oldPath); err != nil {
		return err
	}
	if err := rejectExistingSymlinkAncestors(filepath.Dir(newPath)); err != nil {
		return err
	}
	info, err := os.Lstat(oldPath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to rename symlinked path: %s", oldPath)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to rename non-regular file path: %s", oldPath)
	}
	if targetInfo, err := os.Lstat(newPath); err == nil {
		if targetInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to replace symlinked path: %s", newPath)
		}
		if !targetInfo.Mode().IsRegular() {
			return fmt.Errorf("refusing to replace non-regular file path: %s", newPath)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err := renameAtNoSymlink(oldPath, newPath, renameAtNoSymlinkOptions{
		allowRegular:            true,
		sourceSymlinkKind:       "path",
		sourceUnsupportedFormat: "refusing to rename non-regular file path: %s",
		replaceExisting:         true,
		targetSymlinkKind:       "path",
		targetExistingFormat:    "refusing to replace existing path: %s",
		targetUnsupportedFormat: "refusing to replace non-regular file path: %s",
	}); err != nil {
		return err
	}
	if err := rejectExistingSymlinkAncestors(newPath); err != nil {
		return err
	}
	newInfo, err := os.Lstat(newPath)
	if err != nil {
		return err
	}
	if newInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("renamed file became symlinked: %s", newPath)
	}
	if !newInfo.Mode().IsRegular() {
		return fmt.Errorf("renamed path is not a regular file: %s", newPath)
	}
	return nil
}

type renameAtNoSymlinkOptions struct {
	allowRegular            bool
	allowDir                bool
	sourceSymlinkKind       string
	sourceUnsupportedFormat string
	replaceExisting         bool
	targetSymlinkKind       string
	targetExistingFormat    string
	targetUnsupportedFormat string
	beforeRename            func(oldPath, newPath string) error
}

func renameAtNoSymlink(oldPath, newPath string, opts renameAtNoSymlinkOptions) (unix.Stat_t, error) {
	var zero unix.Stat_t
	oldParent := filepath.Dir(oldPath)
	newParent := filepath.Dir(newPath)
	oldBase := filepath.Base(oldPath)
	newBase := filepath.Base(newPath)
	if oldBase == "." || oldBase == string(filepath.Separator) || newBase == "." || newBase == string(filepath.Separator) {
		return zero, fmt.Errorf("invalid rename path: %s -> %s", oldPath, newPath)
	}

	oldParentFD, err := openDirNoSymlink(oldParent)
	if err != nil {
		return zero, err
	}
	defer func() {
		_ = unix.Close(oldParentFD)
	}()
	newParentFD, err := openDirNoSymlink(newParent)
	if err != nil {
		return zero, err
	}
	defer func() {
		_ = unix.Close(newParentFD)
	}()

	sourceStat, err := validateRenameSourceAtNoSymlink(oldParentFD, oldBase, oldPath, opts)
	if err != nil {
		return zero, err
	}
	if err := validateRenameTargetAtNoSymlink(newParentFD, newBase, newPath, opts); err != nil {
		return zero, err
	}
	if opts.beforeRename != nil {
		if err := opts.beforeRename(oldPath, newPath); err != nil {
			return zero, err
		}
	}
	if err := ensureDirFDStillAtPath(oldParentFD, oldParent); err != nil {
		return zero, err
	}
	if err := ensureDirFDStillAtPath(newParentFD, newParent); err != nil {
		return zero, err
	}
	sourceStat, err = validateRenameSourceAtNoSymlink(oldParentFD, oldBase, oldPath, opts)
	if err != nil {
		return zero, err
	}
	if err := validateRenameTargetAtNoSymlink(newParentFD, newBase, newPath, opts); err != nil {
		return zero, err
	}

	if opts.replaceExisting {
		if err := unix.Renameat(oldParentFD, oldBase, newParentFD, newBase); err != nil {
			return zero, err
		}
	} else {
		if err := unix.Renameat2(oldParentFD, oldBase, newParentFD, newBase, unix.RENAME_NOREPLACE); err != nil {
			if errors.Is(err, unix.EEXIST) {
				return zero, fmt.Errorf(opts.targetExistingFormat, newPath)
			}
			return zero, err
		}
	}
	return sourceStat, nil
}

func validateRenameSourceAtNoSymlink(parentFD int, name, path string, opts renameAtNoSymlinkOptions) (unix.Stat_t, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return stat, err
	}
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		return stat, fmt.Errorf("refusing to rename symlinked %s: %s", opts.sourceSymlinkKind, path)
	case unix.S_IFREG:
		if opts.allowRegular {
			return stat, nil
		}
	case unix.S_IFDIR:
		if opts.allowDir {
			return stat, nil
		}
	}
	return stat, fmt.Errorf(opts.sourceUnsupportedFormat, path)
}

func validateRenameTargetAtNoSymlink(parentFD int, name, path string, opts renameAtNoSymlinkOptions) error {
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	if stat.Mode&unix.S_IFMT == unix.S_IFLNK {
		return fmt.Errorf("refusing to replace symlinked %s: %s", opts.targetSymlinkKind, path)
	}
	if !opts.replaceExisting {
		return fmt.Errorf(opts.targetExistingFormat, path)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf(opts.targetUnsupportedFormat, path)
	}
	return nil
}

func chmodAfterAtomicRename(path string, mode os.FileMode) error {
	var err error
	for attempt := 0; attempt < 6; attempt++ {
		if beforeChmodAfterAtomicRenameOpen != nil {
			if hookErr := beforeChmodAfterAtomicRenameOpen(path); hookErr != nil {
				return hookErr
			}
		}
		err = chmodRegularFileAtNoSymlink(path, mode)
		if err == nil {
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
	}
	return err
}

// ChmodPathNoSymlink applies mode to an existing regular file or directory
// without following symlinks in the path.
func ChmodPathNoSymlink(path string, mode os.FileMode) error {
	return chmodPathAtNoSymlink(path, mode, true, true)
}

func chmodRegularFileAtNoSymlink(path string, mode os.FileMode) error {
	return chmodPathAtNoSymlink(path, mode, true, false)
}

func chmodPathAtNoSymlink(path string, mode os.FileMode, allowRegular, allowDir bool) error {
	path = filepath.Clean(path)
	parent := filepath.Dir(path)
	base := filepath.Base(path)
	if base == "." || base == string(filepath.Separator) {
		return fmt.Errorf("invalid chmod path: %s", path)
	}
	parentFD, err := openDirNoSymlink(parent)
	if err != nil {
		return err
	}
	defer func() {
		_ = unix.Close(parentFD)
	}()
	if err := ensureDirFDStillAtPath(parentFD, parent); err != nil {
		return err
	}
	fd, err := unix.Openat(parentFD, base, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return fmt.Errorf("refusing to chmod symlinked path: %s", path)
		}
		if errors.Is(err, unix.ENOENT) {
			return os.ErrNotExist
		}
		return err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return err
	}
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		if !allowRegular {
			_ = unix.Close(fd)
			return fmt.Errorf("refusing to chmod non-directory path: %s", path)
		}
	case unix.S_IFDIR:
		if !allowDir {
			_ = unix.Close(fd)
			return fmt.Errorf("refusing to chmod non-regular file path: %s", path)
		}
	default:
		_ = unix.Close(fd)
		return fmt.Errorf("refusing to chmod non-regular file path: %s", path)
	}
	err = unix.Fchmod(fd, uint32(mode.Perm()))
	closeErr := unix.Close(fd)
	if err == nil && closeErr != nil {
		err = closeErr
	}
	return err
}

func ReadRegularFileNoSymlink(path string) ([]byte, os.FileInfo, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil, errors.New("path is required")
	}
	path = filepath.Clean(path)
	if err := rejectExistingSymlinkAncestors(path); err != nil {
		return nil, nil, err
	}
	file, err := os.OpenFile(path, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, info, fmt.Errorf("not a regular file: %s", path)
	}
	if info.Size() > MaxRegularFileReadBytes {
		return nil, info, fmt.Errorf("file exceeds maximum readable size: %s (%d > %d bytes)", path, info.Size(), MaxRegularFileReadBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxRegularFileReadBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(data)) > MaxRegularFileReadBytes {
		return nil, info, fmt.Errorf("file exceeds maximum readable size while reading: %s (> %d bytes)", path, MaxRegularFileReadBytes)
	}
	return data, info, nil
}

func ReadRegularFileRangeNoSymlink(path string, offset, limit int64) ([]byte, os.FileInfo, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil, errors.New("path is required")
	}
	if offset < 0 {
		return nil, nil, errors.New("offset must be non-negative")
	}
	if limit < 0 {
		return nil, nil, errors.New("limit must be non-negative")
	}
	if limit > MaxRegularFileReadBytes {
		return nil, nil, fmt.Errorf("range read limit exceeds maximum readable size: %d > %d bytes", limit, MaxRegularFileReadBytes)
	}
	path = filepath.Clean(path)
	if err := rejectExistingSymlinkAncestors(path); err != nil {
		return nil, nil, err
	}
	file, err := os.OpenFile(path, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, info, fmt.Errorf("not a regular file: %s", path)
	}
	if offset >= info.Size() || limit == 0 {
		return []byte{}, info, nil
	}
	if remaining := info.Size() - offset; limit > remaining {
		limit = remaining
	}
	data, err := io.ReadAll(io.NewSectionReader(file, offset, limit))
	if err != nil {
		return nil, nil, err
	}
	return data, info, nil
}

func MkdirAllNoSymlink(path string, mode os.FileMode) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("path is required")
	}
	path = filepath.Clean(path)
	if err := mkdirAllNoSymlinkAt(path, mode); err != nil {
		return err
	}
	if err := rejectExistingSymlinkAncestors(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to use symlinked directory: %s", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("path is not a directory: %s", path)
	}
	return nil
}

func mkdirAllNoSymlinkAt(path string, mode os.FileMode) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	abs = filepath.Clean(abs)
	root, parts := splitAbsolutePath(abs)
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() {
		_ = unix.Close(fd)
	}()

	current := root
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		next := filepath.Join(current, part)
		childFD, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			if errors.Is(err, unix.ENOENT) {
				if beforeMkdirAllNoSymlinkMkdir != nil {
					if hookErr := beforeMkdirAllNoSymlinkMkdir(next); hookErr != nil {
						return hookErr
					}
				}
				if mkdirErr := unix.Mkdirat(fd, part, uint32(mode.Perm())); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
					if ancestorErr := rejectExistingSymlinkAncestors(path); ancestorErr != nil {
						return ancestorErr
					}
					return mkdirErr
				}
				childFD, err = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			}
			if err != nil {
				if ancestorErr := rejectExistingSymlinkAncestors(path); ancestorErr != nil {
					return ancestorErr
				}
				return mkdirAllNoSymlinkOpenError(next, err)
			}
		}
		if closeErr := unix.Close(fd); closeErr != nil {
			_ = unix.Close(childFD)
			return closeErr
		}
		fd = childFD
		current = next
	}
	return nil
}

func splitAbsolutePath(abs string) (string, []string) {
	volume := filepath.VolumeName(abs)
	rest := strings.TrimPrefix(abs, volume)
	separator := string(os.PathSeparator)
	root := volume
	if strings.HasPrefix(rest, separator) {
		root += separator
		rest = strings.TrimPrefix(rest, separator)
	}
	if root == "" {
		root = "."
	}
	if rest == "" {
		return root, nil
	}
	return root, strings.Split(rest, separator)
}

func mkdirAllNoSymlinkOpenError(path string, err error) error {
	info, lstatErr := os.Lstat(path)
	if lstatErr != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to use symlinked directory: %s", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("path is not a directory: %s", path)
	}
	return err
}

func MkdirTempNoSymlink(parent, pattern string) (string, error) {
	parent = strings.TrimSpace(parent)
	if parent == "" {
		return "", errors.New("parent path is required")
	}
	parent = filepath.Clean(parent)
	if err := rejectExistingSymlinkAncestors(parent); err != nil {
		return "", err
	}
	parentFD, err := openDirNoSymlink(parent)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = unix.Close(parentFD)
	}()
	if beforeMkdirTempNoSymlinkCreate != nil {
		if err := beforeMkdirTempNoSymlinkCreate(parent); err != nil {
			return "", err
		}
	}
	name, err := mkdirTempAtNoSymlink(parentFD, parent, pattern)
	if err != nil {
		return "", err
	}
	path := filepath.Join(parent, name)
	path = filepath.Clean(path)
	if filepath.Dir(path) != parent {
		_ = unlinkTempAt(parentFD, name, true)
		return "", fmt.Errorf("invalid temp directory path: %s", path)
	}
	if err := ensureDirFDStillAtPath(parentFD, parent); err != nil {
		_ = unlinkTempAt(parentFD, name, true)
		return "", err
	}
	if err := rejectExistingSymlinkAncestors(path); err != nil {
		_ = unlinkTempAt(parentFD, name, true)
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		_ = unlinkTempAt(parentFD, name, true)
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		_ = unlinkTempAt(parentFD, name, true)
		return "", fmt.Errorf("created temp directory became symlinked: %s", path)
	}
	if !info.IsDir() {
		_ = unlinkTempAt(parentFD, name, true)
		return "", fmt.Errorf("created temp path is not a directory: %s", path)
	}
	return path, nil
}

func CreateTempNoSymlink(parent, pattern string) (*os.File, error) {
	parent = strings.TrimSpace(parent)
	if parent == "" {
		return nil, errors.New("parent path is required")
	}
	parent = filepath.Clean(parent)
	if err := rejectExistingSymlinkAncestors(parent); err != nil {
		return nil, err
	}
	parentFD, err := openDirNoSymlink(parent)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = unix.Close(parentFD)
	}()
	if beforeCreateTempNoSymlinkCreate != nil {
		if err := beforeCreateTempNoSymlinkCreate(parent); err != nil {
			return nil, err
		}
	}
	file, name, err := createTempAtNoSymlink(parentFD, parent, pattern)
	if err != nil {
		return nil, err
	}
	path := filepath.Clean(file.Name())
	if filepath.Dir(path) != parent {
		_ = file.Close()
		_ = unlinkTempAt(parentFD, name, false)
		return nil, fmt.Errorf("invalid temp file path: %s", path)
	}
	if err := ensureDirFDStillAtPath(parentFD, parent); err != nil {
		_ = file.Close()
		_ = unlinkTempAt(parentFD, name, false)
		return nil, err
	}
	if err := rejectExistingSymlinkAncestors(path); err != nil {
		_ = file.Close()
		_ = unlinkTempAt(parentFD, name, false)
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		_ = file.Close()
		_ = unlinkTempAt(parentFD, name, false)
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		_ = file.Close()
		_ = unlinkTempAt(parentFD, name, false)
		return nil, fmt.Errorf("created temp file became symlinked: %s", path)
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		_ = unlinkTempAt(parentFD, name, false)
		return nil, fmt.Errorf("created temp path is not a regular file: %s", path)
	}
	return file, nil
}

func openDirNoSymlink(path string) (int, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return -1, err
	}
	root, parts := splitAbsolutePath(abs)
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	current := root
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		next := filepath.Join(current, part)
		childFD, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			_ = unix.Close(fd)
			if ancestorErr := rejectExistingSymlinkAncestors(path); ancestorErr != nil {
				return -1, ancestorErr
			}
			return -1, mkdirAllNoSymlinkOpenError(next, err)
		}
		if closeErr := unix.Close(fd); closeErr != nil {
			_ = unix.Close(childFD)
			return -1, closeErr
		}
		fd = childFD
		current = next
	}
	if err := ensureDirFDStillAtPath(fd, path); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func mkdirTempAtNoSymlink(parentFD int, parent, pattern string) (string, error) {
	for attempt := 0; attempt < 10000; attempt++ {
		name, err := tempChildName(pattern)
		if err != nil {
			return "", err
		}
		if err := unix.Mkdirat(parentFD, name, 0o700); err != nil {
			if errors.Is(err, unix.EEXIST) {
				continue
			}
			if parentErr := ensureDirFDStillAtPath(parentFD, parent); parentErr != nil {
				return "", parentErr
			}
			return "", err
		}
		return name, nil
	}
	return "", fmt.Errorf("unable to create unique temp directory for pattern %q", pattern)
}

func createTempAtNoSymlink(parentFD int, parent, pattern string) (*os.File, string, error) {
	for attempt := 0; attempt < 10000; attempt++ {
		name, err := tempChildName(pattern)
		if err != nil {
			return nil, "", err
		}
		fd, err := unix.Openat(parentFD, name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if err != nil {
			if errors.Is(err, unix.EEXIST) {
				continue
			}
			if parentErr := ensureDirFDStillAtPath(parentFD, parent); parentErr != nil {
				return nil, "", parentErr
			}
			return nil, "", err
		}
		path := filepath.Join(parent, name)
		return os.NewFile(uintptr(fd), path), name, nil
	}
	return nil, "", fmt.Errorf("unable to create unique temp file for pattern %q", pattern)
}

func tempChildName(pattern string) (string, error) {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	suffix := hex.EncodeToString(random[:])
	name := pattern + suffix
	if index := strings.LastIndex(pattern, "*"); index >= 0 {
		name = pattern[:index] + suffix + pattern[index+1:]
	}
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("invalid temp pattern: %q", pattern)
	}
	return name, nil
}

func ensureDirFDStillAtPath(fd int, path string) error {
	var fdStat unix.Stat_t
	if err := unix.Fstat(fd, &fdStat); err != nil {
		return err
	}
	var pathStat unix.Stat_t
	if err := unix.Lstat(path, &pathStat); err != nil {
		return err
	}
	switch pathStat.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		return fmt.Errorf("refusing to create temp item under symlinked parent: %s", path)
	case unix.S_IFDIR:
	default:
		return fmt.Errorf("temp parent is not a directory: %s", path)
	}
	if fdStat.Dev != pathStat.Dev || fdStat.Ino != pathStat.Ino {
		return fmt.Errorf("temp parent changed while creating: %s", path)
	}
	return nil
}

func unlinkTempAt(parentFD int, name string, directory bool) error {
	flags := 0
	if directory {
		flags = unix.AT_REMOVEDIR
	}
	if err := unix.Unlinkat(parentFD, name, flags); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return nil
}

func RemoveDirAllNoSymlink(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("path is required")
	}
	path = filepath.Clean(path)
	if filepath.Dir(path) == path {
		return fmt.Errorf("refusing to remove filesystem root: %s", path)
	}
	parent := filepath.Dir(path)
	base := filepath.Base(path)
	if base == "." || base == string(filepath.Separator) {
		return fmt.Errorf("invalid directory path: %s", path)
	}
	if err := rejectExistingSymlinkAncestors(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to remove symlinked path: %s", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("refusing to remove non-directory path: %s", path)
	}
	parentFD, err := openDirNoSymlink(parent)
	if err != nil {
		return err
	}
	defer func() {
		_ = unix.Close(parentFD)
	}()
	if beforeRemoveDirAllNoSymlinkRemove != nil {
		if err := beforeRemoveDirAllNoSymlinkRemove(path); err != nil {
			return err
		}
	}
	if err := ensureDirFDStillAtPath(parentFD, parent); err != nil {
		return err
	}
	return removeDirAllAtNoSymlink(parentFD, base, path)
}

func removeDirAllAtNoSymlink(parentFD int, name, path string) error {
	dirFD, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		if errors.Is(err, unix.ELOOP) {
			return fmt.Errorf("refusing to remove symlinked path: %s", path)
		}
		if ancestorErr := rejectExistingSymlinkAncestors(path); ancestorErr != nil {
			return ancestorErr
		}
		return mkdirAllNoSymlinkOpenError(path, err)
	}
	closed := false
	closeDir := func() error {
		if closed {
			return nil
		}
		closed = true
		return unix.Close(dirFD)
	}
	defer func() {
		_ = closeDir()
	}()

	if err := ensureDirFDStillAtPath(dirFD, path); err != nil {
		return err
	}
	if err := removeDirContentsAtNoSymlink(dirFD, path); err != nil {
		return err
	}
	if err := ensureDirFDStillAtPath(dirFD, path); err != nil {
		return err
	}
	if err := closeDir(); err != nil {
		return err
	}
	if err := unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return nil
}

func removeDirContentsAtNoSymlink(dirFD int, dirPath string) error {
	names, err := readDirNamesFromFD(dirFD)
	if err != nil {
		return err
	}
	for _, name := range names {
		if name == "" || name == "." || name == ".." {
			continue
		}
		if err := removeChildAtNoSymlink(dirFD, dirPath, name); err != nil {
			return err
		}
	}
	return nil
}

func readDirNamesFromFD(fd int) ([]string, error) {
	dupFD, err := unix.Dup(fd)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(dupFD), "go-cli-agent-remove-dir")
	if file == nil {
		_ = unix.Close(dupFD)
		return nil, errors.New("duplicate directory fd is invalid")
	}
	names, readErr := file.Readdirnames(-1)
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return names, nil
}

func removeChildAtNoSymlink(parentFD int, parentPath, name string) error {
	childPath := filepath.Join(parentPath, name)
	childFD, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == nil {
		closed := false
		closeChild := func() error {
			if closed {
				return nil
			}
			closed = true
			return unix.Close(childFD)
		}
		defer func() {
			_ = closeChild()
		}()
		if err := ensureDirFDStillAtPath(childFD, childPath); err != nil {
			return err
		}
		if err := removeDirContentsAtNoSymlink(childFD, childPath); err != nil {
			return err
		}
		if err := ensureDirFDStillAtPath(childFD, childPath); err != nil {
			return err
		}
		if err := closeChild(); err != nil {
			return err
		}
		if err := unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) {
			return err
		}
		return nil
	}
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	var stat unix.Stat_t
	if statErr := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); statErr != nil {
		if errors.Is(statErr, unix.ENOENT) {
			return nil
		}
		return err
	}
	if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
		return err
	}
	if unlinkErr := unix.Unlinkat(parentFD, name, 0); unlinkErr != nil && !errors.Is(unlinkErr, unix.ENOENT) {
		return unlinkErr
	}
	return nil
}

func RenameDirNoSymlink(oldPath, newPath string) error {
	oldPath = strings.TrimSpace(oldPath)
	newPath = strings.TrimSpace(newPath)
	if oldPath == "" || newPath == "" {
		return errors.New("source and destination paths are required")
	}
	oldPath = filepath.Clean(oldPath)
	newPath = filepath.Clean(newPath)
	if filepath.Dir(oldPath) == oldPath || filepath.Dir(newPath) == newPath {
		return fmt.Errorf("refusing to rename filesystem root: %s -> %s", oldPath, newPath)
	}
	if err := rejectExistingSymlinkAncestors(oldPath); err != nil {
		return err
	}
	if err := rejectExistingSymlinkAncestors(filepath.Dir(newPath)); err != nil {
		return err
	}
	info, err := os.Lstat(oldPath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to rename symlinked directory: %s", oldPath)
	}
	if !info.IsDir() {
		return fmt.Errorf("refusing to rename non-directory path: %s", oldPath)
	}
	if oldPath == newPath {
		return nil
	}
	if targetInfo, err := os.Lstat(newPath); err == nil {
		if targetInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to replace symlinked directory: %s", newPath)
		}
		return fmt.Errorf("refusing to replace existing directory path: %s", newPath)
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err := renameAtNoSymlink(oldPath, newPath, renameAtNoSymlinkOptions{
		allowDir:                true,
		sourceSymlinkKind:       "directory",
		sourceUnsupportedFormat: "refusing to rename non-directory path: %s",
		replaceExisting:         false,
		targetSymlinkKind:       "directory",
		targetExistingFormat:    "refusing to replace existing directory path: %s",
	}); err != nil {
		return err
	}
	if err := rejectExistingSymlinkAncestors(newPath); err != nil {
		return err
	}
	info, err = os.Lstat(newPath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("renamed directory became symlinked: %s", newPath)
	}
	if !info.IsDir() {
		return fmt.Errorf("renamed path is not a directory: %s", newPath)
	}
	return nil
}

func RenamePathNoSymlink(oldPath, newPath string) error {
	oldPath = strings.TrimSpace(oldPath)
	newPath = strings.TrimSpace(newPath)
	if oldPath == "" || newPath == "" {
		return errors.New("source and destination paths are required")
	}
	oldPath = filepath.Clean(oldPath)
	newPath = filepath.Clean(newPath)
	if filepath.Dir(oldPath) == oldPath || filepath.Dir(newPath) == newPath {
		return fmt.Errorf("refusing to rename filesystem root: %s -> %s", oldPath, newPath)
	}
	if err := rejectExistingSymlinkAncestors(oldPath); err != nil {
		return err
	}
	if err := rejectExistingSymlinkAncestors(filepath.Dir(newPath)); err != nil {
		return err
	}
	info, err := os.Lstat(oldPath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to rename symlinked path: %s", oldPath)
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to rename unsupported path: %s", oldPath)
	}
	if oldPath == newPath {
		return nil
	}
	if targetInfo, err := os.Lstat(newPath); err == nil {
		if targetInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to replace symlinked path: %s", newPath)
		}
		return fmt.Errorf("refusing to replace existing path: %s", newPath)
	} else if !os.IsNotExist(err) {
		return err
	}
	sourceStat, err := renameAtNoSymlink(oldPath, newPath, renameAtNoSymlinkOptions{
		allowRegular:            true,
		allowDir:                true,
		sourceSymlinkKind:       "path",
		sourceUnsupportedFormat: "refusing to rename unsupported path: %s",
		replaceExisting:         false,
		targetSymlinkKind:       "path",
		targetExistingFormat:    "refusing to replace existing path: %s",
		beforeRename:            beforeRenamePathNoSymlinkRename,
	})
	if err != nil {
		return err
	}
	if err := rejectExistingSymlinkAncestors(newPath); err != nil {
		return err
	}
	newInfo, err := os.Lstat(newPath)
	if err != nil {
		return err
	}
	if newInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("renamed path became symlinked: %s", newPath)
	}
	if sourceStat.Mode&unix.S_IFMT == unix.S_IFDIR && !newInfo.IsDir() {
		return fmt.Errorf("renamed path is not a directory: %s", newPath)
	}
	if sourceStat.Mode&unix.S_IFMT == unix.S_IFREG && !newInfo.Mode().IsRegular() {
		return fmt.Errorf("renamed path is not a regular file: %s", newPath)
	}
	return nil
}

func RemoveFileNoSymlink(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("path is required")
	}
	path = filepath.Clean(path)
	if filepath.Dir(path) == path {
		return fmt.Errorf("refusing to remove filesystem root: %s", path)
	}
	parent := filepath.Dir(path)
	base := filepath.Base(path)
	if base == "." || base == string(filepath.Separator) {
		return fmt.Errorf("invalid file path: %s", path)
	}
	if err := rejectExistingSymlinkAncestors(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to remove symlinked path: %s", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to remove non-regular file path: %s", path)
	}
	if err := rejectExistingSymlinkAncestors(path); err != nil {
		return err
	}
	parentFD, err := openDirNoSymlink(parent)
	if err != nil {
		return err
	}
	defer func() {
		_ = unix.Close(parentFD)
	}()
	if err := rejectRegularFileChildAtNoSymlink(parentFD, base, path); err != nil {
		return err
	}
	if beforeRemoveFileNoSymlinkRemove != nil {
		if err := beforeRemoveFileNoSymlinkRemove(path); err != nil {
			return err
		}
	}
	if err := ensureDirFDStillAtPath(parentFD, parent); err != nil {
		return err
	}
	if err := rejectRegularFileChildAtNoSymlink(parentFD, base, path); err != nil {
		return err
	}
	if err := unix.Unlinkat(parentFD, base, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return nil
}

func rejectRegularFileChildAtNoSymlink(parentFD int, name, path string) error {
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		return fmt.Errorf("refusing to remove symlinked path: %s", path)
	case unix.S_IFREG:
		return nil
	default:
		return fmt.Errorf("refusing to remove non-regular file path: %s", path)
	}
}

func rejectExistingSymlinkAncestors(path string) error {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(abs)
	rest := strings.TrimPrefix(abs, volume)
	separator := string(os.PathSeparator)
	current := volume
	if strings.HasPrefix(rest, separator) {
		current += separator
		rest = strings.TrimPrefix(rest, separator)
	}
	if current == "" {
		current = "."
	}
	for _, part := range strings.Split(rest, separator) {
		if part == "" {
			continue
		}
		if current == separator || strings.HasSuffix(current, separator) {
			current += part
		} else {
			current = filepath.Join(current, part)
		}
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to write through symlinked parent: %s", current)
		}
	}
	return nil
}

func rejectSymlinkOrDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to write symlink target: %s", path)
	}
	if info.IsDir() {
		return fmt.Errorf("refusing to overwrite directory: %s", path)
	}
	return nil
}
