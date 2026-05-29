package fileutil

import (
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
	if err := os.MkdirAll(parent, 0o700); err != nil {
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

	tmp, err := os.CreateTemp(parent, "."+base+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	closed := false
	defer os.Remove(tmpPath)

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
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return chmodAfterAtomicRename(path, mode)
}

func chmodAfterAtomicRename(path string, mode os.FileMode) error {
	var err error
	for attempt := 0; attempt < 6; attempt++ {
		var file *os.File
		file, err = os.OpenFile(path, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
		if err != nil {
			if !os.IsNotExist(err) {
				return err
			}
			time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
			continue
		}
		info, statErr := file.Stat()
		if statErr != nil {
			_ = file.Close()
			return statErr
		}
		if !info.Mode().IsRegular() {
			_ = file.Close()
			return fmt.Errorf("refusing to chmod non-regular file path: %s", path)
		}
		err = file.Chmod(mode)
		closeErr := file.Close()
		if err == nil && closeErr != nil {
			err = closeErr
		}
		if err == nil {
			return nil
		}
		if !os.IsNotExist(err) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
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
	if err := rejectExistingSymlinkAncestors(path); err != nil {
		return err
	}
	if err := os.MkdirAll(path, mode); err != nil {
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

func RemoveDirAllNoSymlink(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("path is required")
	}
	path = filepath.Clean(path)
	if filepath.Dir(path) == path {
		return fmt.Errorf("refusing to remove filesystem root: %s", path)
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
	if err := rejectExistingSymlinkAncestors(path); err != nil {
		return err
	}
	return os.RemoveAll(path)
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
	if err := os.Rename(oldPath, newPath); err != nil {
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
	if err := os.Rename(oldPath, newPath); err != nil {
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
	if info.IsDir() && !newInfo.IsDir() {
		return fmt.Errorf("renamed path is not a directory: %s", newPath)
	}
	if info.Mode().IsRegular() && !newInfo.Mode().IsRegular() {
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
	return os.Remove(path)
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
