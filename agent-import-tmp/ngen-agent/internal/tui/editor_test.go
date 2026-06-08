package tui

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestResolveEditorCommandPrefersVisual(t *testing.T) {
	t.Setenv("VISUAL", "vis")
	t.Setenv("EDITOR", "ed")

	command, err := resolveEditorCommand()
	if err != nil {
		t.Fatalf("resolve editor: %v", err)
	}
	if want := []string{"vis"}; !reflect.DeepEqual(command, want) {
		t.Fatalf("expected %v, got %v", want, command)
	}
}

func TestBuildEditorProcessAddsWaitFlagForCode(t *testing.T) {
	command := buildEditorProcess([]string{"code"}, "/tmp/draft.md")
	if want := []string{"code", "-w", "/tmp/draft.md"}; !reflect.DeepEqual(command.Args, want) {
		t.Fatalf("expected %v, got %v", want, command.Args)
	}
}

func TestReadEditedContentTrimsSingleTrailingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "draft.md")
	if err := os.WriteFile(path, []byte("edited\n"), 0o644); err != nil {
		t.Fatalf("write content: %v", err)
	}
	got, err := readEditedContent(path)
	if err != nil {
		t.Fatalf("read content: %v", err)
	}
	if got != "edited" {
		t.Fatalf("expected trimmed content, got %q", got)
	}
}

func TestEditorProcessCanRewriteDraftFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell helper uses POSIX sh")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "editor.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'rewritten' > \"$1\"\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	tempPath, err := writeEditorSeed("seed")
	if err != nil {
		t.Fatalf("write seed: %v", err)
	}
	defer os.Remove(tempPath)

	command := buildEditorProcess([]string{script}, tempPath)
	if err := command.Run(); err != nil {
		t.Fatalf("run editor: %v", err)
	}
	got, err := readEditedContent(tempPath)
	if err != nil {
		t.Fatalf("read rewritten content: %v", err)
	}
	if got != "rewritten" {
		t.Fatalf("expected rewritten content, got %q", got)
	}
}
