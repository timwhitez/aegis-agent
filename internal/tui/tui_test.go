package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestBuildSnapshotReportsSelectedSessionFactErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
	}{
		{name: "messages", file: "messages.jsonl"},
		{name: "events", file: "events.jsonl"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := session.NewStore(t.TempDir())
			createTUITestSession(t, store, "s1")
			if err := os.WriteFile(filepath.Join(store.SessionDir("s1"), tc.file), []byte("{not-json}\n"), 0o600); err != nil {
				t.Fatalf("corrupt %s: %v", tc.file, err)
			}

			_, err := BuildSnapshot(store, "s1", 10)
			if err == nil || !strings.Contains(err.Error(), tc.file) {
				t.Fatalf("expected %s load error, got %v", tc.file, err)
			}
		})
	}
}

func TestBuildSnapshotReportsSelectedQueueFactErrors(t *testing.T) {
	store := session.NewStore(t.TempDir())
	createTUITestSession(t, store, "s1")
	if err := store.EnqueueJob(session.QueueJob{ID: "job_1", Status: session.QueueStatusQueued, ParentSessionID: "s1", Prompt: "review", Mode: session.ModeExec}); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	jobPath := filepath.Join(store.Root(), "_queue", session.QueueStatusQueued, "job_1.json")
	if err := os.WriteFile(jobPath, []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatalf("corrupt queue job: %v", err)
	}

	_, err := BuildSnapshot(store, "s1", 10)
	if err == nil || !strings.Contains(err.Error(), "job_1.json") {
		t.Fatalf("expected queue job load error, got %v", err)
	}
}

func createTUITestSession(t *testing.T, store *session.Store, id string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	workdir := t.TempDir()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               id,
		CreatedAt:        now,
		Workdir:          workdir,
		RequestedWorkdir: workdir,
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		RootSessionID:    id,
	}
	if err := store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "turn_decide", UpdatedAt: now}); err != nil {
		t.Fatalf("create session: %v", err)
	}
}
