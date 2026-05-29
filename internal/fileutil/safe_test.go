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

func TestChmodAfterAtomicRenameRejectsSymlinkParentBeforeOpen(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "sessions")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	path := filepath.Join(parent, "state.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write local file: %v", err)
	}
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "state.json")
	if err := os.WriteFile(outsideFile, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	swapped := false
	restore := beforeChmodAfterAtomicRenameOpen
	beforeChmodAfterAtomicRenameOpen = func(chmodPath string) error {
		if swapped || chmodPath != path {
			return nil
		}
		swapped = true
		if err := os.RemoveAll(parent); err != nil {
			return err
		}
		return os.Symlink(outside, parent)
	}
	defer func() {
		beforeChmodAfterAtomicRenameOpen = restore
	}()

	err := chmodAfterAtomicRename(path, 0o600)
	if err == nil {
		t.Fatal("expected symlinked parent during chmod to be rejected")
	}
	info, statErr := os.Stat(outsideFile)
	if statErr != nil {
		t.Fatalf("stat outside file: %v", statErr)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("outside target mode changed: got %v", info.Mode().Perm())
	}
}

func TestChmodPathNoSymlinkRejectsSymlinkDirectory(t *testing.T) {
	temp := t.TempDir()
	outside := filepath.Join(temp, "outside")
	if err := os.Mkdir(outside, 0o777); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := os.Chmod(outside, 0o777); err != nil {
		t.Fatalf("chmod outside: %v", err)
	}
	link := filepath.Join(temp, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := ChmodPathNoSymlink(link, 0o700)
	if err == nil || !strings.Contains(err.Error(), "symlinked path") {
		t.Fatalf("expected symlink chmod rejection, got %v", err)
	}
	info, statErr := os.Stat(outside)
	if statErr != nil {
		t.Fatalf("stat outside: %v", statErr)
	}
	if mode := info.Mode().Perm(); mode != 0o777 {
		t.Fatalf("outside directory mode changed through symlink: %s", mode.String())
	}
}

func TestChmodPathNoSymlinkAppliesToRegularFileAndDirectory(t *testing.T) {
	temp := t.TempDir()
	dir := filepath.Join(temp, "dir")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatalf("mkdir dir: %v", err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatalf("chmod dir setup: %v", err)
	}
	file := filepath.Join(temp, "file.txt")
	if err := os.WriteFile(file, []byte("data"), 0o666); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := os.Chmod(file, 0o666); err != nil {
		t.Fatalf("chmod file setup: %v", err)
	}

	if err := ChmodPathNoSymlink(dir, 0o700); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	if err := ChmodPathNoSymlink(file, 0o600); err != nil {
		t.Fatalf("chmod file: %v", err)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if mode := dirInfo.Mode().Perm(); mode != 0o700 {
		t.Fatalf("expected dir mode 0700, got %s", mode.String())
	}
	fileInfo, err := os.Stat(file)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if mode := fileInfo.Mode().Perm(); mode != 0o600 {
		t.Fatalf("expected file mode 0600, got %s", mode.String())
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

func TestReadRegularFileNoSymlinkRejectsReplacedParent(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "facts")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	path := filepath.Join(parent, "session.json")
	if err := os.WriteFile(path, []byte("inside"), 0o600); err != nil {
		t.Fatalf("write inside file: %v", err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "session.json"), []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	beforeReadRegularFileOpen = func(readPath string) error {
		if readPath != path {
			return nil
		}
		if err := os.Rename(parent, parent+".real"); err != nil {
			return err
		}
		return os.Symlink(outside, parent)
	}
	defer func() {
		beforeReadRegularFileOpen = nil
	}()

	data, info, err := ReadRegularFileNoSymlink(path)
	if err == nil {
		t.Fatalf("expected replaced parent read rejection, got data=%q info=%#v", string(data), info)
	}
	if !strings.Contains(err.Error(), "symlink") && !strings.Contains(err.Error(), "changed") {
		t.Fatalf("expected symlink/path-change error, got %v", err)
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

func TestRemoveDirAllNoSymlinkRejectsSymlinkParentBeforeRemove(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "sessions")
	target := filepath.Join(parent, "session-1")
	if err := os.MkdirAll(filepath.Join(target, "artifacts"), 0o700); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "artifacts", "local.txt"), []byte("local"), 0o600); err != nil {
		t.Fatalf("write local file: %v", err)
	}
	outside := t.TempDir()
	outsideTarget := filepath.Join(outside, "session-1")
	if err := os.MkdirAll(filepath.Join(outsideTarget, "artifacts"), 0o700); err != nil {
		t.Fatalf("mkdir outside target: %v", err)
	}
	outsideFile := filepath.Join(outsideTarget, "artifacts", "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	restore := beforeRemoveDirAllNoSymlinkRemove
	beforeRemoveDirAllNoSymlinkRemove = func(path string) error {
		if path != target {
			return nil
		}
		if err := os.RemoveAll(parent); err != nil {
			return err
		}
		return os.Symlink(outside, parent)
	}
	defer func() {
		beforeRemoveDirAllNoSymlinkRemove = restore
	}()

	err := RemoveDirAllNoSymlink(target)
	if err == nil {
		t.Fatal("expected symlinked parent during recursive remove to be rejected")
	}
	if _, statErr := os.Stat(outsideFile); statErr != nil {
		t.Fatalf("outside target should not be removed, got %v", statErr)
	}
}

func TestRemoveDirAllNoSymlinkRemovesNestedDirectoryWithoutFollowingChildSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "session-1")
	if err := os.MkdirAll(filepath.Join(target, "artifacts", "nested"), 0o700); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "artifacts", "nested", "local.txt"), []byte("local"), 0o600); err != nil {
		t.Fatalf("write local file: %v", err)
	}
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(target, "artifacts", "outside-link")); err != nil {
		t.Fatalf("symlink child: %v", err)
	}

	if err := RemoveDirAllNoSymlink(target); err != nil {
		t.Fatalf("remove nested directory: %v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("target should be removed, stat err=%v", statErr)
	}
	if _, statErr := os.Stat(outsideFile); statErr != nil {
		t.Fatalf("outside file should remain, got %v", statErr)
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

func TestMkdirTempNoSymlinkRejectsSymlinkParentBeforeCreate(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "sessions")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	outside := t.TempDir()

	restore := beforeMkdirTempNoSymlinkCreate
	beforeMkdirTempNoSymlinkCreate = func(path string) error {
		if path != parent {
			return nil
		}
		if err := os.RemoveAll(parent); err != nil {
			return err
		}
		return os.Symlink(outside, parent)
	}
	defer func() {
		beforeMkdirTempNoSymlinkCreate = restore
	}()

	path, err := MkdirTempNoSymlink(parent, ".upload-*")
	if err == nil {
		_ = RemoveDirAllNoSymlink(path)
		t.Fatalf("expected symlinked parent during temp dir create to be rejected, got %s", path)
	}
	entries, readErr := os.ReadDir(outside)
	if readErr != nil {
		t.Fatalf("read outside dir: %v", readErr)
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

func TestCreateTempNoSymlinkRejectsSymlinkParentBeforeCreate(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "sessions")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	outside := t.TempDir()

	restore := beforeCreateTempNoSymlinkCreate
	beforeCreateTempNoSymlinkCreate = func(path string) error {
		if path != parent {
			return nil
		}
		if err := os.RemoveAll(parent); err != nil {
			return err
		}
		return os.Symlink(outside, parent)
	}
	defer func() {
		beforeCreateTempNoSymlinkCreate = restore
	}()

	file, err := CreateTempNoSymlink(parent, ".upload-*")
	if err == nil {
		_ = file.Close()
		_ = RemoveFileNoSymlink(file.Name())
		t.Fatalf("expected symlinked parent during temp file create to be rejected, got %s", file.Name())
	}
	entries, readErr := os.ReadDir(outside)
	if readErr != nil {
		t.Fatalf("read outside dir: %v", readErr)
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

func TestRenamePathNoSymlinkRejectsSymlinkDestinationParentBeforeRename(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "job.json")
	if err := os.WriteFile(source, []byte(`{"id":"job"}`), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	targetParent := filepath.Join(root, "backup")
	if err := os.MkdirAll(targetParent, 0o700); err != nil {
		t.Fatalf("mkdir target parent: %v", err)
	}
	target := filepath.Join(targetParent, "job.json")
	outside := t.TempDir()
	outsideTarget := filepath.Join(outside, "job.json")

	restore := beforeRenamePathNoSymlinkRename
	beforeRenamePathNoSymlinkRename = func(oldPath, newPath string) error {
		if oldPath != source || newPath != target {
			return nil
		}
		if err := os.RemoveAll(targetParent); err != nil {
			return err
		}
		return os.Symlink(outside, targetParent)
	}
	defer func() {
		beforeRenamePathNoSymlinkRename = restore
	}()

	err := RenamePathNoSymlink(source, target)
	if err == nil {
		t.Fatal("expected symlinked destination parent during rename to be rejected")
	}
	if _, statErr := os.Stat(outsideTarget); !os.IsNotExist(statErr) {
		t.Fatalf("outside target should not be created, stat err=%v", statErr)
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

func TestRemoveFileNoSymlinkRejectsSymlinkParentBeforeRemove(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "sessions")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	path := filepath.Join(parent, "stale.json")
	if err := os.WriteFile(path, []byte("local"), 0o600); err != nil {
		t.Fatalf("write local file: %v", err)
	}
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "stale.json")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	restore := beforeRemoveFileNoSymlinkRemove
	beforeRemoveFileNoSymlinkRemove = func(removePath string) error {
		if removePath != path {
			return nil
		}
		if err := os.RemoveAll(parent); err != nil {
			return err
		}
		return os.Symlink(outside, parent)
	}
	defer func() {
		beforeRemoveFileNoSymlinkRemove = restore
	}()

	err := RemoveFileNoSymlink(path)
	if err == nil {
		t.Fatal("expected symlinked parent during file remove to be rejected")
	}
	if _, statErr := os.Stat(outsideFile); statErr != nil {
		t.Fatalf("outside file should not be removed, got %v", statErr)
	}
}
