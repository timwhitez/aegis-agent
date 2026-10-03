package fileutil

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
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
