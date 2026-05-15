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

func TestReadRegularFileNoSymlinkRejectsSymlinkFile(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	link := filepath.Join(root, "artifact.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	data, info, err := ReadRegularFileNoSymlink(link)
	if err == nil {
		t.Fatalf("expected symlink read rejection, got data=%q info=%#v", string(data), info)
	}
	if !strings.Contains(err.Error(), "symlinked") && !strings.Contains(err.Error(), "too many levels") {
		t.Fatalf("expected symlink error, got %v", err)
	}
}

func TestMkdirAllNoSymlinkRejectsSymlinkAncestor(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "skills")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := MkdirAllNoSymlink(filepath.Join(root, "skills", "demo", "references"), 0o755)
	if err == nil || !strings.Contains(err.Error(), "symlinked") {
		t.Fatalf("expected symlink ancestor rejection, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "demo", "references")); !os.IsNotExist(statErr) {
		t.Fatalf("outside target should not be created, stat err=%v", statErr)
	}
}

func TestRemoveDirAllNoSymlinkRejectsSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "session-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := RemoveDirAllNoSymlink(link)
	if err == nil || !strings.Contains(err.Error(), "symlinked") {
		t.Fatalf("expected symlink target rejection, got %v", err)
	}
	if _, statErr := os.Lstat(link); statErr != nil {
		t.Fatalf("expected symlink to remain, got %v", statErr)
	}
	if _, statErr := os.Stat(outside); statErr != nil {
		t.Fatalf("expected outside dir to remain, got %v", statErr)
	}
}

func TestRemoveDirAllNoSymlinkRejectsSymlinkAncestor(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "sessions")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := RemoveDirAllNoSymlink(filepath.Join(link, "child"))
	if err == nil || !strings.Contains(err.Error(), "symlinked") {
		t.Fatalf("expected symlink ancestor rejection, got %v", err)
	}
	if _, statErr := os.Stat(outside); statErr != nil {
		t.Fatalf("expected outside dir to remain, got %v", statErr)
	}
}
