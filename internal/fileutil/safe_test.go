package fileutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAtomicWriteFileNoSymlinkRejectsSymlinkAncestor(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "skills")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := AtomicWriteFileNoSymlink(filepath.Join(root, "skills", "example", "SKILL.md"), []byte("body"), 0o600)
	if err == nil || !strings.Contains(err.Error(), "symlinked parent") {
		t.Fatalf("expected symlink ancestor rejection, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "example", "SKILL.md")); !os.IsNotExist(statErr) {
		t.Fatalf("outside target should not be created, stat err=%v", statErr)
	}
}
