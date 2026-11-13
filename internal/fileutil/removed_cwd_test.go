package fileutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// snapshotTree records every path, mode and regular-file content below root.
func snapshotTree(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		data := []byte(nil)
		if info.Mode().IsRegular() {
			if data, err = os.ReadFile(path); err != nil {
				return err
			}
		}
		fmt.Fprintf(&b, "%s %v %q\n", path, info.Mode(), data)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return b.String()
}

// Darwin's getcwd(3) keeps returning the path of a removed working directory,
// so resolving a relative path through filepath.Abs would recreate that
// directory or act on whatever now occupies its old path. Every helper must
// instead fail like Linux does and leave the filesystem untouched.
func TestRelativePathsUnderRemovedWorkingDirectoryChangeNothing(t *testing.T) {
	ops := []struct {
		name string
		run  func() error
	}{
		{"MkdirAllNoSymlink", func() error { return MkdirAllNoSymlink("rel/dir", 0o700) }},
		{"OpenFileNoSymlink create", func() error {
			return closeIfOpened(OpenFileNoSymlink("created.txt", unix.O_CREAT|unix.O_WRONLY, 0o600))
		}},
		{"OpenFileNoSymlink nested create", func() error {
			return closeIfOpened(OpenFileNoSymlink("victimdir/created.txt", unix.O_CREAT|unix.O_WRONLY|unix.O_TRUNC, 0o600))
		}},
		{"AtomicWriteFileNoSymlink new parent", func() error { return AtomicWriteFileNoSymlink("rel/file.txt", []byte("new"), 0o600) }},
		{"AtomicWriteFileNoSymlink replace", func() error { return AtomicWriteFileNoSymlink("victim.txt", []byte("new"), 0o600) }},
		{"AtomicCommitFileNoSymlink", func() error {
			_, err := AtomicCommitFileNoSymlink("victim.txt", []byte("new"), 0o600, AtomicCommitOptions{})
			return err
		}},
		{"AtomicCommitFileNoSymlink sync parent chain", func() error {
			_, err := AtomicCommitFileNoSymlink("victim.txt", []byte("new"), 0o600, AtomicCommitOptions{SyncParentChain: true})
			return err
		}},
		{"CreateTempNoSymlink", func() error { return closeIfOpened(CreateTempNoSymlink(".", "tmp-*")) }},
		{"MkdirTempNoSymlink", func() error {
			_, err := MkdirTempNoSymlink("victimdir", "tmp-*")
			return err
		}},
		{"ChmodPathNoSymlink", func() error { return ChmodPathNoSymlink("victim.txt", 0o644) }},
		{"RenamePathNoSymlink", func() error { return RenamePathNoSymlink("victim.txt", "renamed.txt") }},
		{"RenameDirNoSymlink", func() error { return RenameDirNoSymlink("victimdir", "renameddir") }},
		{"RemoveFileNoSymlink", func() error { return RemoveFileNoSymlink("victim.txt") }},
		{"RemoveDirAllNoSymlink", func() error { return RemoveDirAllNoSymlink("victimdir") }},
		{"ReadRegularFileNoSymlink", func() error {
			data, _, err := ReadRegularFileNoSymlink("victim.txt")
			return readResult(data, err)
		}},
		{"ReadRegularFileRangeNoSymlink", func() error {
			data, _, err := ReadRegularFileRangeNoSymlink("victim.txt", 0, 64)
			return readResult(data, err)
		}},
		{"OpenDirNoSymlink", func() error { return closeIfOpened(OpenDirNoSymlink("victimdir")) }},
	}
	for _, replaced := range []bool{false, true} {
		for _, op := range ops {
			name := op.name + "/removed"
			if replaced {
				name = op.name + "/replaced at old path"
			}
			t.Run(name, func(t *testing.T) {
				base := t.TempDir()
				cwd := filepath.Join(base, "cwd")
				if err := os.Mkdir(cwd, 0o700); err != nil {
					t.Fatal(err)
				}
				t.Chdir(cwd)
				if err := os.Remove(cwd); err != nil {
					t.Fatal(err)
				}
				if replaced {
					// An unrelated directory now occupies the stale path.
					if err := os.MkdirAll(filepath.Join(cwd, "victimdir"), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(cwd, "victim.txt"), []byte("victim"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				before := snapshotTree(t, base)
				err := op.run()
				if err == nil {
					t.Fatalf("%s with a relative path under a removed working directory succeeded", op.name)
				}
				if !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("%s: got %v; want a not-exist error for the removed working directory", op.name, err)
				}
				if after := snapshotTree(t, base); after != before {
					t.Fatalf("%s changed the filesystem (%v):\nbefore:\n%safter:\n%s", op.name, err, before, after)
				}
			})
		}
	}
}

func closeIfOpened(file *os.File, err error) error {
	if file != nil {
		_ = file.Close()
	}
	return err
}

func readResult(data []byte, err error) error {
	if err == nil {
		return nil
	}
	if len(data) != 0 {
		return fmt.Errorf("read %q despite error %w", data, err)
	}
	return err
}
