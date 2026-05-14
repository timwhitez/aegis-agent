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
	return os.Chmod(path, mode)
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
	data, err := io.ReadAll(file)
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
