package session

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newEnsureDirTestSession(t *testing.T, store *Store) SessionMetadata {
	t.Helper()
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create: %v", err)
	}
	return meta
}

func countChmodBestEffort(t *testing.T) *atomic.Int64 {
	t.Helper()
	var calls atomic.Int64
	restore := beforeChmodBestEffort
	beforeChmodBestEffort = func(string, os.FileMode) error {
		calls.Add(1)
		return nil
	}
	t.Cleanup(func() { beforeChmodBestEffort = restore })
	return &calls
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	if got := info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky); got != want {
		t.Fatalf("%s mode = %v, want %v", path, got, want)
	}
}

// ensureDir must not repeat MkdirAll/chmod work for levels that are already
// real directories with the store mode, but must still repair any level whose
// mode drifted and recreate removed levels.
func TestEnsureDirSkipsPreparedLevelsAndRepairsDrift(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
	meta := newEnsureDirTestSession(t, store)
	sessionDir := store.SessionDir(meta.ID)
	messages := filepath.Join(sessionDir, "messages.jsonl")
	if err := store.AppendMessage(meta.ID, NewMessage("user", "first")); err != nil {
		t.Fatalf("append: %v", err)
	}
	calls := countChmodBestEffort(t)

	if err := store.AppendMessage(meta.ID, NewMessage("user", "steady")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("prepared store levels and file were re-chmodded %d times", got)
	}

	if err := os.Chmod(sessionDir, 0o755); err != nil {
		t.Fatalf("chmod session dir: %v", err)
	}
	if err := os.Chmod(root, 0o700|os.ModeSticky); err != nil {
		t.Fatalf("chmod root: %v", err)
	}
	if err := os.Chmod(messages, 0o644); err != nil {
		t.Fatalf("chmod messages: %v", err)
	}
	if err := store.AppendMessage(meta.ID, NewMessage("user", "drifted")); err != nil {
		t.Fatalf("append after drift: %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("expected root, session dir and messages to be re-chmodded once each, got %d calls", got)
	}
	assertMode(t, root, 0o700)
	assertMode(t, sessionDir, 0o700)
	assertMode(t, messages, 0o600)

	control := filepath.Join(sessionDir, "control")
	if err := os.RemoveAll(control); err != nil {
		t.Fatalf("remove control: %v", err)
	}
	if err := store.AppendSteerRequest(meta.ID, NewSteerRequest("recreate control", false)); err != nil {
		t.Fatalf("append steer after control removal: %v", err)
	}
	assertMode(t, control, 0o700)
}

func TestEnsureDirStillRejectsSymlinkedPreparedLevels(t *testing.T) {
	temp := t.TempDir()
	root := filepath.Join(temp, "sessions")
	store := NewStoreWithDirMode(root, 0o700)
	meta := newEnsureDirTestSession(t, store)
	sessionDir := store.SessionDir(meta.ID)

	// The replacement is a real 0700 directory, so only the symlink check can
	// stop the append from following it.
	outside := filepath.Join(temp, "outside")
	if err := os.Rename(sessionDir, outside); err != nil {
		t.Fatalf("move session dir: %v", err)
	}
	if err := os.Symlink(outside, sessionDir); err != nil {
		t.Fatalf("symlink session dir: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(outside, "messages.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read outside messages: %v", err)
	}
	err = store.AppendMessage(meta.ID, NewMessage("user", "through symlink"))
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("append through a symlinked session dir: got %v, want symlink rejection", err)
	}
	after, readErr := os.ReadFile(filepath.Join(outside, "messages.jsonl"))
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatalf("read outside messages: %v", readErr)
	}
	if string(after) != string(before) {
		t.Fatal("append wrote through a symlinked session dir")
	}

	if err := os.Remove(sessionDir); err != nil {
		t.Fatalf("remove session symlink: %v", err)
	}
	movedRoot := filepath.Join(temp, "moved-root")
	if err := os.Rename(root, movedRoot); err != nil {
		t.Fatalf("move root: %v", err)
	}
	if err := os.Symlink(movedRoot, root); err != nil {
		t.Fatalf("symlink root: %v", err)
	}
	if err := store.EnsureRoot(); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("EnsureRoot through a symlinked root: got %v, want symlink rejection", err)
	}
}
