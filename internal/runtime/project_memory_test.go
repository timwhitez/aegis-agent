package runtime

import (
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

func containsAll(haystack string, needles []string) bool {
	for _, needle := range needles {
		if !strings.Contains(haystack, needle) {
			return false
		}
	}
	return true
}
