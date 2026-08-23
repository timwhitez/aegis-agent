//go:build linux

package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func installFakeBwrap(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "bwrap")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestBwrapRejectsLinkedWorktreeGitPointer(t *testing.T) {
	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, ".git"), []byte("gitdir: /outside/repo/.git/worktrees/child\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, status, err := sandboxCommand("bwrap", workdir, workdir, []string{"git", "status"})
	if err == nil {
		t.Fatal("linked worktree unexpectedly accepted")
	}
	if status != "bwrap_external_git_metadata" {
		t.Fatalf("status=%q err=%v", status, err)
	}
	if !strings.Contains(err.Error(), "use copy isolation or disable bwrap") {
		t.Fatalf("missing actionable error: %v", err)
	}
}

func TestBwrapProductionFDMappingInspectsStableWorkdir(t *testing.T) {
	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, ".git"), []byte("gitdir: /outside/repo/.git/worktrees/child\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stableDir, err := os.Open(workdir)
	if err != nil {
		t.Fatal(err)
	}
	defer stableDir.Close()

	_, _, status, err := sandboxCommand("bwrap", workdir, childBwrapWorkdirFD, []string{"git", "status"})
	if err == nil || status != "bwrap_external_git_metadata" {
		t.Fatalf("production fd mapping failed to reject linked worktree: status=%q err=%v", status, err)
	}
	if !strings.Contains(err.Error(), "use copy isolation or disable bwrap") {
		t.Fatalf("missing actionable error: %v", err)
	}
}

func TestBwrapProductionFDMappingFailsClosedAfterPathReplacement(t *testing.T) {
	parent := t.TempDir()
	workdir := filepath.Join(parent, "workspace")
	if err := os.Mkdir(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	stableDir, err := os.Open(workdir)
	if err != nil {
		t.Fatal(err)
	}
	defer stableDir.Close()
	if err := os.Rename(workdir, filepath.Join(parent, "old-workspace")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(workdir, 0o700); err != nil {
		t.Fatal(err)
	}

	_, _, status, err := sandboxCommand("bwrap", workdir, childBwrapWorkdirFD, []string{"true"})
	if err == nil || status != "bwrap_external_git_metadata" {
		t.Fatalf("replaced path did not fail closed: status=%q err=%v", status, err)
	}
	if !strings.Contains(err.Error(), "cannot locate the stable descriptor") {
		t.Fatalf("unexpected replacement diagnostic: %v", err)
	}
}

func TestBwrapAllowsSelfContainedGitDirectory(t *testing.T) {
	installFakeBwrap(t)
	workdir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workdir, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	path, args, status, err := sandboxCommand("bwrap", workdir, workdir, []string{"git", "status"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "bwrap" || status != "bwrap" {
		t.Fatalf("path=%q status=%q", path, status)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--bind "+workdir+" "+workdir) {
		t.Fatalf("workdir bind missing: %q", joined)
	}
}

func TestBwrapAllowsSelfContainedGitDirectoryThroughProductionFDMapping(t *testing.T) {
	installFakeBwrap(t)
	workdir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workdir, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	stableDir, err := os.Open(workdir)
	if err != nil {
		t.Fatal(err)
	}
	defer stableDir.Close()

	_, _, status, err := sandboxCommand("bwrap", workdir, childBwrapWorkdirFD, []string{"git", "status"})
	if err != nil || status != "bwrap" {
		t.Fatalf("self-contained repository rejected through production mapping: status=%q err=%v", status, err)
	}
}

func TestBwrapRejectsSymlinkedGitMetadata(t *testing.T) {
	workdir := t.TempDir()
	target := filepath.Join(t.TempDir(), "git")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(workdir, ".git")); err != nil {
		t.Fatal(err)
	}
	_, _, status, err := sandboxCommand("bwrap", workdir, workdir, []string{"true"})
	if err == nil || status != "bwrap_external_git_metadata" {
		t.Fatalf("symlinked metadata accepted: status=%q err=%v", status, err)
	}
}

func TestBwrapRejectsOversizedGitPointer(t *testing.T) {
	workdir := t.TempDir()
	data := strings.Repeat("x", maxBwrapGitPointerBytes+1)
	if err := os.WriteFile(filepath.Join(workdir, ".git"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, status, err := sandboxCommand("bwrap", workdir, workdir, []string{"true"})
	if err == nil || status != "bwrap_external_git_metadata" {
		t.Fatalf("oversized metadata accepted: status=%q err=%v", status, err)
	}
}

func TestBwrapRejectsUnsupportedRegularGitMetadata(t *testing.T) {
	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, ".git"), []byte("not-a-gitdir-pointer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, status, err := sandboxCommand("bwrap", workdir, workdir, []string{"true"})
	if err == nil || status != "bwrap_external_git_metadata" {
		t.Fatalf("unsupported regular metadata accepted: status=%q err=%v", status, err)
	}
	if !strings.Contains(err.Error(), "unsupported regular Git metadata") {
		t.Fatalf("missing unsupported-representation diagnostic: %v", err)
	}
}
