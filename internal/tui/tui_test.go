package tui

import (
	"context"
	"os"
	"testing"
	"time"

	"go-cli-agent/internal/session"
)

func TestRunRejectsNonTTY(t *testing.T) {
	store := session.NewStore(t.TempDir())
	stdout, err := os.CreateTemp(t.TempDir(), "tui-out-*.txt")
	if err != nil {
		t.Fatalf("create stdout file: %v", err)
	}
	defer stdout.Close()
	stdinPath := stdout.Name()
	stdin, err := os.Open(stdinPath)
	if err != nil {
		t.Fatalf("open stdin file: %v", err)
	}
	defer stdin.Close()

	err = Run(context.Background(), store, "", 10, 100, stdout, stdin)
	if err == nil || err.Error() != "tui requires a TTY; use --once for snapshot mode" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHandleKeyRefreshKeepsCurrentSelection(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	snapshot := Snapshot{
		Sessions: []session.SessionSummary{
			{ID: "s1", CreatedAt: now, UpdatedAt: now},
			{ID: "s2", CreatedAt: now, UpdatedAt: now},
		},
		SelectedIndex: 1,
	}

	nextID, quit := handleKey([]byte{'r'}, snapshot)
	if quit {
		t.Fatal("refresh should not quit")
	}
	if nextID != "s2" {
		t.Fatalf("expected refresh to keep current selection, got %q", nextID)
	}
}
