package fileutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestChmodAfterAtomicRenameRetriesTransientMissingPath(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.json")
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(15 * time.Millisecond)
		_ = os.WriteFile(path, []byte("{}"), 0o600)
	}()

	if err := chmodAfterAtomicRename(path, 0o600); err != nil {
		t.Fatalf("expected retry to observe file creation, got %v", err)
	}
	<-done
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat path: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected mode 0600, got %v", info.Mode().Perm())
	}
}

func TestChmodAfterAtomicRenameRejectsSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	link := filepath.Join(root, "session.json")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := chmodAfterAtomicRename(link, 0o600)
	if err == nil {
		t.Fatal("expected symlink chmod rejection")
	}
	if !strings.Contains(err.Error(), "symlinked") && !strings.Contains(err.Error(), "too many levels") {
		t.Fatalf("expected symlink error, got %v", err)
	}
	info, statErr := os.Stat(outside)
	if statErr != nil {
		t.Fatalf("stat outside: %v", statErr)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("outside target mode changed: got %v", info.Mode().Perm())
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

func TestReadRegularFileNoSymlinkRejectsOversizedFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "large.log")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	if err := file.Truncate(MaxRegularFileReadBytes + 1); err != nil {
		_ = file.Close()
		t.Fatalf("truncate file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}

	data, info, err := ReadRegularFileNoSymlink(path)
	if err == nil {
		t.Fatalf("expected oversized file rejection, got data len=%d info=%#v", len(data), info)
	}
	if info == nil || info.Size() != MaxRegularFileReadBytes+1 {
		t.Fatalf("expected returned file info with oversized size, got %#v", info)
	}
	if !strings.Contains(err.Error(), "exceeds maximum readable size") {
		t.Fatalf("expected size cap error, got %v", err)
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

func TestRemoveFileNoSymlinkRemovesRegularFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "stale.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if err := RemoveFileNoSymlink(path); err != nil {
		t.Fatalf("remove file: %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("expected file removed, got %v", statErr)
	}
}

func TestRemoveFileNoSymlinkRejectsSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	link := filepath.Join(root, "stale.json")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := RemoveFileNoSymlink(link)
	if err == nil || !strings.Contains(err.Error(), "symlinked") {
		t.Fatalf("expected symlink target rejection, got %v", err)
	}
	if _, statErr := os.Lstat(link); statErr != nil {
		t.Fatalf("expected symlink to remain, got %v", statErr)
	}
	if _, statErr := os.Stat(outside); statErr != nil {
		t.Fatalf("expected outside file to remain, got %v", statErr)
	}
}
