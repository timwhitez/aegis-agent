package fileutil

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

func TestAtomicCommitLegalStagesAndOutcomes(t *testing.T) {
	cases := []struct {
		stage     AtomicCommitStage
		outcome   AtomicCommitOutcome
		published bool
	}{
		{AtomicCommitBeforeWrite, AtomicCommitNotPublished, false},
		{AtomicCommitBeforeFileSync, AtomicCommitNotPublished, false},
		{AtomicCommitBeforePublish, AtomicCommitNotPublished, false},
		{AtomicCommitAfterPublish, AtomicCommitPublishedUnconfirmed, true},
		{AtomicCommitBeforeDirectorySync, AtomicCommitPublishedUnconfirmed, true},
		{AtomicCommitAfterCommit, AtomicCommitCommitted, true},
	}
	for _, tc := range cases {
		t.Run(string(tc.stage), func(t *testing.T) {
			parent := t.TempDir()
			path := filepath.Join(parent, "receipt.json")
			if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			cause := errors.New("injected boundary failure")
			outcome, err := AtomicCommitFileNoSymlink(path, []byte("new"), 0600, AtomicCommitOptions{BeforeStage: func(stage AtomicCommitStage) error {
				if stage == tc.stage {
					return cause
				}
				return nil
			}})
			if outcome != tc.outcome || !errors.Is(err, cause) {
				t.Fatalf("unexpected commit result: %s %v", outcome, err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := "old"
			if tc.published {
				want = "new"
			}
			if string(data) != want {
				t.Fatalf("publication mismatch: %s", data)
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 1 {
				t.Fatalf("temp files leaked: %v %v", entries, err)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "receipt.json")
	if outcome, err := AtomicCommitFileNoSymlink(path, []byte("legal"), 0600, AtomicCommitOptions{}); err != nil || outcome != AtomicCommitCommitted {
		t.Fatalf("legal commit failed: %s %v", outcome, err)
	}
}

func parentChainCommitOptions(hook func(AtomicCommitStage) error) AtomicCommitOptions {
	return AtomicCommitOptions{BeforeStage: hook, SyncParentChain: true}
}

func receiptParentChain(path string) []string {
	var paths []string
	for path = filepath.Clean(path); ; path = filepath.Dir(path) {
		paths = append(paths, path)
		if filepath.Dir(path) == path {
			return paths
		}
	}
}

func TestAtomicCommitParentChainSyncBeforePublication(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "new-a", "new-b", "sessions", "session")
	if err := MkdirAllNoSymlink(parent, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "receipt.json")
	want := len(receiptParentChain(parent))
	syncs, syncsBeforeWrite := 0, -1
	outcome, err := AtomicCommitFileNoSymlink(path, []byte("receipt"), 0600, parentChainCommitOptions(func(stage AtomicCommitStage) error {
		if stage == AtomicCommitBeforeParentChainSync {
			syncs++
		}
		if stage == AtomicCommitBeforeWrite {
			syncsBeforeWrite = syncs
			t.Logf("RECEIPT_WRITE_BEGIN parent=%s", parent)
		}
		return nil
	}))
	if err != nil || outcome != AtomicCommitCommitted {
		t.Fatalf("legal receipt commit: %s %v", outcome, err)
	}
	if syncsBeforeWrite != want || syncs != want {
		t.Fatalf("committed before ancestor synchronization: before_write=%d total=%d want=%d parent=%s", syncsBeforeWrite, syncs, want, parent)
	}
}

func TestAtomicCommitParentChainSyncFailureIsNotPublished(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "new-a", "new-b", "sessions", "session")
	if err := MkdirAllNoSymlink(parent, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "receipt.json")
	for failAt := 1; failAt <= len(receiptParentChain(parent)); failAt++ {
		if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
			t.Fatal(err)
		}
		cause := errors.New("injected ancestor sync failure")
		syncs, wrote := 0, false
		outcome, err := AtomicCommitFileNoSymlink(path, []byte("replacement"), 0600, parentChainCommitOptions(func(stage AtomicCommitStage) error {
			if stage == AtomicCommitBeforeParentChainSync {
				syncs++
				if syncs == failAt {
					return cause
				}
			}
			wrote = wrote || stage == AtomicCommitBeforeWrite
			return nil
		}))
		if !errors.Is(err, cause) || outcome != AtomicCommitNotPublished || wrote {
			t.Fatalf("ancestor %d failure reached receipt write: %s %v wrote=%v", failAt, outcome, err, wrote)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "original" {
			t.Fatal("ancestor failure changed authoritative receipt")
		}
		entries, err := os.ReadDir(parent)
		if err != nil || len(entries) != 1 {
			t.Fatalf("ancestor failure leaked a temporary file: %v %v", entries, err)
		}
	}
}

func TestAtomicCommitRejectsMissingParentAndSymlinks(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "missing", "receipt.json")
	if outcome, err := AtomicCommitFileNoSymlink(missing, []byte("receipt"), 0600, AtomicCommitOptions{}); err == nil || outcome != AtomicCommitNotPublished {
		t.Fatalf("missing parent result: %s %v", outcome, err)
	}
	if _, err := os.Stat(filepath.Dir(missing)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("commit created its parent directory")
	}
	outside := t.TempDir()
	target := filepath.Join(outside, "outside.json")
	if err := os.WriteFile(target, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	parentLink := filepath.Join(base, "parent_link")
	if err := os.Symlink(outside, parentLink); err != nil {
		t.Fatal(err)
	}
	leafLink := filepath.Join(base, "leaf_link")
	if err := os.Symlink(target, leafLink); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(parentLink, "outside.json"), leafLink} {
		if outcome, err := AtomicCommitFileNoSymlink(path, []byte("unsafe"), 0600, AtomicCommitOptions{}); err == nil || outcome != AtomicCommitNotPublished {
			t.Fatalf("symlink published: %s %v", outcome, err)
		}
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "outside" {
		t.Fatal("commit changed symlink target")
	}
}

func TestAtomicCommitRejectsReplacedTemporaryFile(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "receipt.json")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	outcome, err := AtomicCommitFileNoSymlink(path, []byte("new"), 0600, AtomicCommitOptions{BeforeStage: func(stage AtomicCommitStage) error {
		if stage != AtomicCommitBeforePublish {
			return nil
		}
		entries, err := os.ReadDir(parent)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".tmp") {
				name := filepath.Join(parent, entry.Name())
				if err := os.Remove(name); err != nil {
					return err
				}
				return os.WriteFile(name, []byte("replacement"), 0600)
			}
		}
		return errors.New("temporary file not found")
	}})
	if err == nil || outcome != AtomicCommitNotPublished {
		t.Fatalf("replaced temporary file committed: %s %v", outcome, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "old" {
		t.Fatal("temporary replacement changed authoritative file")
	}
}

func TestAtomicCommitOptionsArePerCall(t *testing.T) {
	parent := t.TempDir()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, fail := range []bool{false, true} {
		wg.Add(1)
		go func(fail bool) {
			defer wg.Done()
			name := "good"
			if fail {
				name = "bad"
			}
			path := filepath.Join(parent, name)
			outcome, err := AtomicCommitFileNoSymlink(path, []byte("receipt"), 0600, AtomicCommitOptions{BeforeStage: func(stage AtomicCommitStage) error {
				if fail && stage == AtomicCommitBeforeFileSync {
					return errors.New("one call failed")
				}
				return nil
			}})
			if fail {
				if err == nil || outcome != AtomicCommitNotPublished {
					errs <- errors.New("fault injection escaped failed call")
				}
			} else if err != nil || outcome != AtomicCommitCommitted {
				errs <- errors.New("another call inherited fault injection")
			}
		}(fail)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(parent, "good"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("committed file permissions incorrect")
	}
}

func TestAtomicCommitDirectoryReplacementBeforeSyncFailClosed(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "session")
	moved := filepath.Join(base, "old_session")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "receipt.json")
	outcome, err := AtomicCommitFileNoSymlink(path, []byte("receipt"), 0600, AtomicCommitOptions{BeforeStage: func(stage AtomicCommitStage) error {
		if stage != AtomicCommitBeforeDirectorySync {
			return nil
		}
		if err := os.Rename(parent, moved); err != nil {
			return err
		}
		return os.Mkdir(parent, 0700)
	}})
	if err == nil || outcome != AtomicCommitPublishedUnconfirmed {
		t.Fatalf("replaced directory falsely confirmed commit: %s %v", outcome, err)
	}
}

func TestAtomicCommitParentChainReplacementFailsClosed(t *testing.T) {
	for _, stage := range []AtomicCommitStage{AtomicCommitBeforeParentChainSync, AtomicCommitBeforePublish, AtomicCommitAfterPublish, AtomicCommitBeforeDirectorySync} {
		for _, replacement := range []string{"directory", "symlink_to_original"} {
			t.Run(string(stage)+"/"+replacement, func(t *testing.T) {
				base := t.TempDir()
				ancestor := filepath.Join(base, "new-a")
				moved := filepath.Join(base, "moved-a")
				parent := filepath.Join(ancestor, "new-b", "sessions", "session")
				if err := MkdirAllNoSymlink(parent, 0700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(parent, "receipt.json")
				if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
					t.Fatal(err)
				}
				replaced := false
				outcome, err := AtomicCommitFileNoSymlink(path, []byte("replacement"), 0600, parentChainCommitOptions(func(current AtomicCommitStage) error {
					if current != stage || replaced {
						return nil
					}
					replaced = true
					if err := os.Rename(ancestor, moved); err != nil {
						return err
					}
					if replacement == "symlink_to_original" {
						return os.Symlink(moved, ancestor)
					}
					return MkdirAllNoSymlink(parent, 0700)
				}))
				want := AtomicCommitNotPublished
				content := "original"
				if stage == AtomicCommitAfterPublish || stage == AtomicCommitBeforeDirectorySync {
					want, content = AtomicCommitPublishedUnconfirmed, "replacement"
				}
				if err == nil || outcome != want || !replaced {
					t.Fatalf("ancestor replacement falsely confirmed receipt: outcome=%s error=%v", outcome, err)
				}
				data, err := os.ReadFile(filepath.Join(moved, "new-b", "sessions", "session", "receipt.json"))
				if err != nil || string(data) != content {
					t.Fatalf("unexpected pinned directory publication: %q %v", data, err)
				}
			})
		}
	}
}

func TestAtomicCommitParentChainPreservesOutcomes(t *testing.T) {
	for _, test := range []struct {
		stage   AtomicCommitStage
		outcome AtomicCommitOutcome
	}{
		{AtomicCommitBeforeWrite, AtomicCommitNotPublished},
		{AtomicCommitBeforeFileSync, AtomicCommitNotPublished},
		{AtomicCommitBeforePublish, AtomicCommitNotPublished},
		{AtomicCommitAfterPublish, AtomicCommitPublishedUnconfirmed},
		{AtomicCommitBeforeDirectorySync, AtomicCommitPublishedUnconfirmed},
		{AtomicCommitAfterCommit, AtomicCommitCommitted},
	} {
		t.Run(string(test.stage), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "receipt.json")
			cause := errors.New("injected commit boundary error")
			outcome, err := AtomicCommitFileNoSymlink(path, []byte("receipt"), 0600, parentChainCommitOptions(func(stage AtomicCommitStage) error {
				if stage == test.stage {
					return cause
				}
				return nil
			}))
			if outcome != test.outcome || !errors.Is(err, cause) {
				t.Fatalf("lost publication outcome: %s %v", outcome, err)
			}
		})
	}
}

func TestAtomicCommitParentChainKeepsModesAndRejectsMissingPaths(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "ancestor", "session")
	if err := MkdirAllNoSymlink(parent, 0750); err != nil {
		t.Fatal(err)
	}
	paths := receiptParentChain(parent)
	modes := make(map[string]os.FileMode, len(paths))
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		modes[path] = info.Mode()
	}
	if outcome, err := AtomicCommitFileNoSymlink(filepath.Join(parent, "receipt.json"), []byte("receipt"), 0600, parentChainCommitOptions(nil)); err != nil || outcome != AtomicCommitCommitted {
		t.Fatalf("legal parent chain commit: %s %v", outcome, err)
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || info.Mode() != modes[path] {
			t.Fatalf("writer changed ancestor permissions: %s %v", path, err)
		}
	}
	missing := filepath.Join(base, "missing", "session", "receipt.json")
	if outcome, err := AtomicCommitFileNoSymlink(missing, []byte("receipt"), 0600, parentChainCommitOptions(nil)); err == nil || outcome != AtomicCommitNotPublished {
		t.Fatalf("missing directory accepted: %s %v", outcome, err)
	}
	if _, err := os.Stat(filepath.Join(base, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("writer created an ancestor directory")
	}
	link := filepath.Join(base, "ancestor_link")
	if err := os.Symlink(filepath.Join(base, "ancestor"), link); err != nil {
		t.Fatal(err)
	}
	if outcome, err := AtomicCommitFileNoSymlink(filepath.Join(link, "session", "receipt.json"), []byte("unsafe"), 0600, parentChainCommitOptions(nil)); err == nil || outcome != AtomicCommitNotPublished {
		t.Fatalf("symlink ancestor accepted: %s %v", outcome, err)
	}
}

// The separate strace run injects EIO into the first real fsync syscall, with
// no production syscall replacement or global fault hook.
func TestAtomicCommitParentChainSyscallFailure(t *testing.T) {
	if os.Getenv("AEGIS_TEST_PARENT_FSYNC_EIO") != "1" {
		t.Skip("requires targeted strace fsync injection")
	}
	parent := filepath.Join(t.TempDir(), "new-a", "new-b", "session")
	if err := MkdirAllNoSymlink(parent, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "receipt.json")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	wrote := false
	outcome, err := AtomicCommitFileNoSymlink(path, []byte("replacement"), 0600, parentChainCommitOptions(func(stage AtomicCommitStage) error {
		wrote = wrote || stage == AtomicCommitBeforeWrite
		return nil
	}))
	if outcome != AtomicCommitNotPublished || !errors.Is(err, unix.EIO) || wrote {
		t.Fatalf("real directory fsync EIO reached receipt write: %s %v wrote=%v", outcome, err, wrote)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "original" {
		t.Fatal("real directory sync failure changed receipt")
	}
}
