package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectMemoryExcerptPrefersHeadingsAndBullets(t *testing.T) {
	text := `
Intro paragraph that should not dominate the excerpt.

# Plan

- Fix provider metadata path
- Tighten review validator

## Validation

1. Run go test ./...
2. Run the real matrix
`

	excerpt := projectMemoryExcerpt(text, 5)
	if excerpt == "" {
		t.Fatal("expected non-empty excerpt")
	}
	if !containsAll(excerpt, []string{"# Plan", "- Fix provider metadata path", "## Validation", "1. Run go test ./..."}) {
		t.Fatalf("expected heading-aware excerpt, got %q", excerpt)
	}
	if containsAll(excerpt, []string{"Intro paragraph"}) {
		t.Fatalf("expected prose intro to be deprioritized, got %q", excerpt)
	}
}

func TestProjectMemorySkipsSymlinkEscape(t *testing.T) {
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, "reports"), 0o700); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	external := filepath.Join(t.TempDir(), "spec.md")
	if err := os.WriteFile(external, []byte("# External secret\n"), 0o600); err != nil {
		t.Fatalf("write external: %v", err)
	}
	if err := os.Symlink(external, filepath.Join(workdir, "reports", "spec.md")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	stack := loadProjectMemoryStack(workdir)
	for _, file := range stack.Files {
		if file.Name == "spec" && file.Present {
			t.Fatalf("symlink-escaped project memory should be skipped: %#v", file)
		}
	}
}

func containsAll(haystack string, needles []string) bool {
	for _, needle := range needles {
		if !strings.Contains(haystack, needle) {
			return false
		}
	}
	return true
}
