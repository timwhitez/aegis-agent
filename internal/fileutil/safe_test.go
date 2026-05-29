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

func TestAtomicWriteFileNoSymlinkRejectsSymlinkParentDuringRename(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "skills")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	outside := t.TempDir()

	restore := beforeAtomicWriteRename
	beforeAtomicWriteRename = func(tmpPath, path string) error {
		if err := os.RemoveAll(parent); err != nil {
			return err
		}
		if err := os.Symlink(outside, parent); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(outside, filepath.Base(tmpPath)), []byte("outside"), 0o600)
	}
	defer func() {
		beforeAtomicWriteRename = restore
	}()

	err := AtomicWriteFileNoSymlink(filepath.Join(parent, "SKILL.md"), []byte("body"), 0o600)
	if err == nil {
		t.Fatal("expected symlinked parent during rename to be rejected")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "SKILL.md")); !os.IsNotExist(statErr) {
		t.Fatalf("outside target should not be created, stat err=%v", statErr)
	}
}

func TestAtomicWriteFileNoSymlinkReplacesRegularFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.json")
	if err := os.WriteFile(path, []byte(`{"old":true}`), 0o600); err != nil {
		t.Fatalf("write original file: %v", err)
	}

	if err := AtomicWriteFileNoSymlink(path, []byte(`{"new":true}`), 0o600); err != nil {
		t.Fatalf("replace regular file: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read replaced file: %v", err)
	}
	if string(data) != `{"new":true}` {
		t.Fatalf("unexpected replaced content: %q", data)
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

func TestReadRegularFileRangeNoSymlinkReadsOversizedFileSlice(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "large.log")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	if _, err := file.WriteString("0123456789"); err != nil {
		_ = file.Close()
		t.Fatalf("write prefix: %v", err)
	}
	if err := file.Truncate(MaxRegularFileReadBytes + 1); err != nil {
		_ = file.Close()
		t.Fatalf("truncate file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}

	data, info, err := ReadRegularFileRangeNoSymlink(path, 2, 4)
	if err != nil {
		t.Fatalf("range read oversized file slice: %v", err)
	}
	if string(data) != "2345" {
		t.Fatalf("unexpected range content: %q", string(data))
	}
	if info == nil || info.Size() != MaxRegularFileReadBytes+1 {
		t.Fatalf("expected full file info for oversized range read, got %#v", info)
	}
}

func TestReadRegularFileRangeNoSymlinkRejectsSymlinkFile(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	link := filepath.Join(root, "artifact.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	data, info, err := ReadRegularFileRangeNoSymlink(link, 0, 4)
	if err == nil {
		t.Fatalf("expected symlink range read rejection, got data=%q info=%#v", string(data), info)
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

func TestMkdirAllNoSymlinkRejectsSymlinkParentBeforeCreate(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "skills")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	outside := t.TempDir()
	target := filepath.Join(parent, "demo", "references")
	firstMissing := filepath.Join(parent, "demo")

	restore := beforeMkdirAllNoSymlinkMkdir
	beforeMkdirAllNoSymlinkMkdir = func(path string) error {
		if path != firstMissing && path != target {
			return nil
		}
		if path != firstMissing {
			return nil
		}
		if err := os.RemoveAll(parent); err != nil {
			return err
		}
		return os.Symlink(outside, parent)
	}
	defer func() {
		beforeMkdirAllNoSymlinkMkdir = restore
	}()

	err := MkdirAllNoSymlink(target, 0o755)
	if err == nil {
		t.Fatal("expected symlinked parent during mkdir to be rejected")
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

func TestMkdirTempNoSymlinkRejectsSymlinkParent(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	path, err := MkdirTempNoSymlink(link, ".upload-*")
	if err == nil {
		t.Fatalf("expected symlink parent to be rejected, got %s", path)
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatalf("read outside dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("temp dir should not be created under symlink target, got %d entries", len(entries))
	}
}

func TestMkdirTempNoSymlinkCreatesDirectory(t *testing.T) {
	parent := t.TempDir()
	path, err := MkdirTempNoSymlink(parent, ".upload-*")
	if err != nil {
		t.Fatalf("mkdir temp no symlink: %v", err)
	}
	defer RemoveDirAllNoSymlink(path)
	if filepath.Dir(path) != parent {
		t.Fatalf("temp dir should be direct child of parent, got %s under %s", path, parent)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat temp dir: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		t.Fatalf("expected regular directory, got mode %v", info.Mode())
	}
}

func TestCreateTempNoSymlinkRejectsSymlinkParent(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	file, err := CreateTempNoSymlink(link, ".upload-*")
	if err == nil {
		_ = file.Close()
		t.Fatalf("expected symlink parent to be rejected, got %s", file.Name())
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatalf("read outside dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("temp file should not be created under symlink target, got %d entries", len(entries))
	}
}

func TestCreateTempNoSymlinkCreatesFile(t *testing.T) {
	parent := t.TempDir()
	file, err := CreateTempNoSymlink(parent, ".upload-*")
	if err != nil {
		t.Fatalf("create temp no symlink: %v", err)
	}
	path := file.Name()
	defer RemoveFileNoSymlink(path)
	if filepath.Dir(path) != parent {
		t.Fatalf("temp file should be direct child of parent, got %s under %s", path, parent)
	}
	if _, err := file.WriteString("ok"); err != nil {
		_ = file.Close()
		t.Fatalf("write temp file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close temp file: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat temp file: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		t.Fatalf("expected regular file, got mode %v", info.Mode())
	}
}

func TestRenameDirNoSymlinkRejectsSymlinkDestinationParent(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("body"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	outside := t.TempDir()
	link := filepath.Join(root, "skills")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := RenameDirNoSymlink(source, filepath.Join(link, "demo-skill"))
	if err == nil || !strings.Contains(err.Error(), "symlinked") {
		t.Fatalf("expected symlink ancestor rejection, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "demo-skill")); !os.IsNotExist(statErr) {
		t.Fatalf("outside target should not be created, stat err=%v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(source, "SKILL.md")); statErr != nil {
		t.Fatalf("source directory should remain after rejected rename: %v", statErr)
	}
}

func TestRenameDirNoSymlinkRejectsSymlinkSource(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "source-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := RenameDirNoSymlink(link, filepath.Join(root, "target"))
	if err == nil || !strings.Contains(err.Error(), "symlinked") {
		t.Fatalf("expected symlink source rejection, got %v", err)
	}
	if _, statErr := os.Lstat(link); statErr != nil {
		t.Fatalf("source symlink should remain after rejected rename: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, "target")); !os.IsNotExist(statErr) {
		t.Fatalf("target should not be created, stat err=%v", statErr)
	}
}

func TestRenameDirNoSymlinkRejectsSamePathSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "source-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := RenameDirNoSymlink(link, link)
	if err == nil || !strings.Contains(err.Error(), "symlinked") {
		t.Fatalf("expected same-path symlink rejection, got %v", err)
	}
	if _, statErr := os.Lstat(link); statErr != nil {
		t.Fatalf("source symlink should remain after rejected same-path rename: %v", statErr)
	}
}

func TestRenameDirNoSymlinkRenamesDirectory(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("body"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	if err := RenameDirNoSymlink(source, target); err != nil {
		t.Fatalf("rename directory: %v", err)
	}
	if _, statErr := os.Stat(source); !os.IsNotExist(statErr) {
		t.Fatalf("source should be moved, stat err=%v", statErr)
	}
	data, err := os.ReadFile(filepath.Join(target, "SKILL.md"))
	if err != nil {
		t.Fatalf("read renamed file: %v", err)
	}
	if string(data) != "body" {
		t.Fatalf("unexpected renamed file content: %q", data)
	}
}

func TestRenamePathNoSymlinkRejectsSymlinkSourceAncestor(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "session"), 0o700); err != nil {
		t.Fatalf("mkdir outside session: %v", err)
	}
	outsideFile := filepath.Join(outside, "session", "job.json")
	if err := os.WriteFile(outsideFile, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	link := filepath.Join(root, "sessions")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := RenamePathNoSymlink(filepath.Join(link, "session", "job.json"), filepath.Join(root, "backup.json"))
	if err == nil || !strings.Contains(err.Error(), "symlinked") {
		t.Fatalf("expected symlink ancestor rejection, got %v", err)
	}
	if _, statErr := os.Stat(outsideFile); statErr != nil {
		t.Fatalf("outside file should remain after rejected rename: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, "backup.json")); !os.IsNotExist(statErr) {
		t.Fatalf("backup target should not be created, stat err=%v", statErr)
	}
}

func TestRenamePathNoSymlinkRenamesRegularFile(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "job.json")
	target := filepath.Join(root, "backup", "job.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatalf("mkdir backup: %v", err)
	}
	if err := os.WriteFile(source, []byte(`{"id":"job"}`), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	if err := RenamePathNoSymlink(source, target); err != nil {
		t.Fatalf("rename regular file: %v", err)
	}
	if _, statErr := os.Stat(source); !os.IsNotExist(statErr) {
		t.Fatalf("source should be moved, stat err=%v", statErr)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(data) != `{"id":"job"}` {
		t.Fatalf("unexpected target content: %q", data)
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
