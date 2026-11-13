package procutil

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetwdReportsCurrentDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	got, err := Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(got)
	if err != nil || !os.SameFile(want, info) {
		t.Fatalf("Getwd() = %q (%v), want directory %q", got, err, dir)
	}
}

func TestGetwdReportsRemovedCurrentDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cwd")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	got, err := Getwd()
	if err == nil || !strings.Contains(err.Error(), "getwd") || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Getwd() = %q, %v; want getwd not-exist error", got, err)
	}
}
