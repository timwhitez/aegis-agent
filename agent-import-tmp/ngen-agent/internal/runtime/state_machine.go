package ngenrt

import (
	"os"
	"path/filepath"
)

func ensureDir(path string) error {
	return os.MkdirAll(filepath.Dir(path), 0o755)
}

func createExclusive(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
