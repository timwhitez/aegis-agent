//go:build unix

package procutil

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAbsCleansAbsolutePathWithoutWorkingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cwd")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	for input, want := range map[string]string{
		"/":                     "/",
		"/a/b/../c/":            "/a/c",
		"//a//./b":              "/a/b",
		"/a/../../b":            "/b",
		filepath.Join(dir, "x"): filepath.Join(dir, "x"),
	} {
		got, err := Abs(input)
		if err != nil || got != want {
			t.Errorf("Abs(%q) = %q, %v; want %q (an absolute path needs no working directory)", input, got, err, want)
		}
	}
}

func TestAbsMatchesFilepathAbsForLiveWorkingDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	wd, err := Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"", ".", "./", "rel", "rel/dir/", "a/../b", "../up", "./x/./y", "..."} {
		got, err := Abs(input)
		if err != nil {
			t.Fatalf("Abs(%q): %v", input, err)
		}
		want, err := filepath.Abs(input)
		if err != nil {
			t.Fatalf("filepath.Abs(%q): %v", input, err)
		}
		if got != want || got != filepath.Join(wd, input) || !filepath.IsAbs(got) {
			t.Errorf("Abs(%q) = %q; want %q (filepath.Abs) joined to %q", input, got, want, wd)
		}
	}
}

func TestAbsFollowsMovedWorkingDirectory(t *testing.T) {
	base := t.TempDir()
	oldPath, newPath := filepath.Join(base, "old"), filepath.Join(base, "new")
	if err := os.Mkdir(oldPath, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(oldPath)
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatal(err)
	}
	// A different directory now occupies the old path; the process is still in
	// the moved one, which getcwd reports on every Unix host.
	if err := os.Mkdir(oldPath, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := Abs("rel")
	if err != nil {
		t.Fatalf("Abs under a moved working directory: %v", err)
	}
	info, err := os.Stat(filepath.Dir(got))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.Stat(newPath)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "rel" || !os.SameFile(info, want) {
		t.Fatalf("Abs(%q) = %q; want a path inside the moved directory %q", "rel", got, newPath)
	}
}

func TestAbsRejectsRelativePathUnderRemovedWorkingDirectory(t *testing.T) {
	for name, recreate := range map[string]bool{"removed": false, "replaced at old path": true} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "cwd")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Chdir(dir)
			if err := os.Remove(dir); err != nil {
				t.Fatal(err)
			}
			if recreate {
				// A new directory at the stale path is not the working directory.
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			for _, input := range []string{"", ".", "rel", "rel/dir", "../sibling"} {
				got, err := Abs(input)
				if err == nil || got != "" || !strings.Contains(err.Error(), "getwd") || !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("Abs(%q) = %q, %v; want the getwd not-exist error filepath.Abs reports on Linux", input, got, err)
				}
			}
		})
	}
}
