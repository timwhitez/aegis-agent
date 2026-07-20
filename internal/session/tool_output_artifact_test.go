package session

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newToolOutputArtifactTestStore(t *testing.T) (*Store, string, string) {
	t.Helper()
	store := NewStore(t.TempDir())
	sessionID := NewSessionID()
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               sessionID,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	artifactRoot := filepath.Join(store.SessionDir(sessionID), "artifacts", "tool-outputs")
	return store, sessionID, artifactRoot
}

func TestToolOutputArtifactWritersRejectCrossSessionRoot(t *testing.T) {
	store, ownerSessionID, ownerArtifactRoot := newToolOutputArtifactTestStore(t)
	otherSessionID := NewSessionID()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := store.Create(SessionMetadata{
		SchemaVersion:    1,
		ID:               otherSessionID,
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create other session: %v", err)
	}
	quota := ToolOutputArtifactQuota{FileMaxBytes: 64, SessionMaxBytes: 128, MaxFiles: 4}
	_, writeErr := store.WriteToolOutputArtifact(otherSessionID, ownerArtifactRoot, "cross-session-write", []byte("must-not-cross"), quota)
	stream, _, streamErr := store.BeginToolOutputArtifactStream(otherSessionID, ownerArtifactRoot, "cross-session-stream", quota)
	if stream != nil {
		_, _ = stream.Close()
	}
	if writeErr == nil || streamErr == nil || !strings.Contains(writeErr.Error(), "session") || !strings.Contains(streamErr.Error(), "session") {
		t.Fatalf("cross-session artifact roots were accepted: owner=%s other=%s write_err=%v stream=%#v stream_err=%v", ownerSessionID, otherSessionID, writeErr, stream, streamErr)
	}
	if _, err := os.Lstat(ownerArtifactRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cross-session attempt changed owner artifact root: %v", err)
	}
}

func TestToolOutputArtifactWritesCompleteOwnerOnlyFile(t *testing.T) {
	store, sessionID, artifactRoot := newToolOutputArtifactTestStore(t)
	payload := []byte("complete tool output\nwith tail\n")
	quota := ToolOutputArtifactQuota{FileMaxBytes: 1024, SessionMaxBytes: 4096, MaxFiles: 4}

	result, err := store.WriteToolOutputArtifact(sessionID, artifactRoot, "shell-call_1", payload, quota)
	if err != nil {
		t.Fatalf("write tool output artifact: %v", err)
	}
	if result.RawBytes != len(payload) || result.PersistedBytes != len(payload) || result.OmittedBytes != 0 || !result.Complete || result.Truncated || !result.Recoverable {
		t.Fatalf("unexpected complete artifact result: %#v", result)
	}
	if result.AbsolutePath == "" || result.Filename == "" {
		t.Fatalf("complete artifact did not return paths: %#v", result)
	}
	data, err := os.ReadFile(result.AbsolutePath)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatalf("artifact bytes changed: got %q want %q", data, payload)
	}
	fileInfo, err := os.Stat(result.AbsolutePath)
	if err != nil {
		t.Fatalf("stat artifact: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("artifact mode=%#o, want 0600", got)
	}
	dirInfo, err := os.Stat(artifactRoot)
	if err != nil {
		t.Fatalf("stat artifact root: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("artifact root mode=%#o, want 0700", got)
	}
}

func TestToolOutputArtifactForcesOwnerOnlyModeWithPermissiveSessionConfig(t *testing.T) {
	store := NewStoreWithDirMode(t.TempDir(), 0o755)
	sessionID := NewSessionID()
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               sessionID,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	artifactRoot := filepath.Join(store.SessionDir(sessionID), "artifacts", "tool-outputs")
	result, err := store.WriteToolOutputArtifact(sessionID, artifactRoot, "owner-only", []byte("private output"), ToolOutputArtifactQuota{
		FileMaxBytes:    1024,
		SessionMaxBytes: 4096,
		MaxFiles:        4,
	})
	if err != nil {
		t.Fatalf("write tool output artifact: %v", err)
	}
	for path, want := range map[string]os.FileMode{
		artifactRoot: 0o700,
		filepath.Join(artifactRoot, toolOutputArtifactLockName): 0o600,
		result.AbsolutePath: 0o600,
	} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("stat %s: %v", path, statErr)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("mode for %s=%#o, want %#o", path, got, want)
		}
	}
}

func TestToolOutputArtifactQuotaDimensions(t *testing.T) {
	t.Run("single file bytes", func(t *testing.T) {
		store, sessionID, artifactRoot := newToolOutputArtifactTestStore(t)
		payload := []byte("0123456789")
		result, err := store.WriteToolOutputArtifact(sessionID, artifactRoot, "read-file", payload, ToolOutputArtifactQuota{
			FileMaxBytes:    4,
			SessionMaxBytes: 100,
			MaxFiles:        5,
		})
		if err != nil {
			t.Fatalf("write artifact: %v", err)
		}
		if result.PersistedBytes != 4 || result.OmittedBytes != 6 || result.Complete || !result.Truncated || result.Recoverable || result.Reason != ToolOutputArtifactReasonFileBytes {
			t.Fatalf("unexpected single-file quota result: %#v", result)
		}
		data, err := os.ReadFile(result.AbsolutePath)
		if err != nil || string(data) != "0123" {
			t.Fatalf("single-file artifact mismatch: data=%q err=%v", data, err)
		}
	})

	t.Run("session bytes", func(t *testing.T) {
		store, sessionID, artifactRoot := newToolOutputArtifactTestStore(t)
		quota := ToolOutputArtifactQuota{FileMaxBytes: 100, SessionMaxBytes: 10, MaxFiles: 5}
		first, err := store.WriteToolOutputArtifact(sessionID, artifactRoot, "first", []byte("123456"), quota)
		if err != nil || !first.Complete {
			t.Fatalf("write first artifact: result=%#v err=%v", first, err)
		}
		second, err := store.WriteToolOutputArtifact(sessionID, artifactRoot, "second", []byte("abcdef"), quota)
		if err != nil {
			t.Fatalf("write second artifact: %v", err)
		}
		if second.PersistedBytes != 4 || second.OmittedBytes != 2 || second.Reason != ToolOutputArtifactReasonSessionBytes || second.Recoverable {
			t.Fatalf("unexpected session-byte quota result: %#v", second)
		}
	})

	t.Run("session files", func(t *testing.T) {
		store, sessionID, artifactRoot := newToolOutputArtifactTestStore(t)
		quota := ToolOutputArtifactQuota{FileMaxBytes: 100, SessionMaxBytes: 100, MaxFiles: 1}
		first, err := store.WriteToolOutputArtifact(sessionID, artifactRoot, "first", []byte("first"), quota)
		if err != nil || !first.Complete {
			t.Fatalf("write first artifact: result=%#v err=%v", first, err)
		}
		second, err := store.WriteToolOutputArtifact(sessionID, artifactRoot, "second", []byte("second"), quota)
		if err != nil {
			t.Fatalf("write second artifact: %v", err)
		}
		if second.AbsolutePath != "" || second.PersistedBytes != 0 || second.OmittedBytes != len("second") || second.Reason != ToolOutputArtifactReasonSessionFiles || second.Recoverable {
			t.Fatalf("unexpected session-file quota result: %#v", second)
		}
	})
}

func TestToolOutputArtifactQuotaRebuildsAcrossStoreRestart(t *testing.T) {
	storeA, sessionID, artifactRoot := newToolOutputArtifactTestStore(t)
	quota := ToolOutputArtifactQuota{FileMaxBytes: 100, SessionMaxBytes: 10, MaxFiles: 5}
	if result, err := storeA.WriteToolOutputArtifact(sessionID, artifactRoot, "first", []byte("123456"), quota); err != nil || !result.Complete {
		t.Fatalf("write first artifact: result=%#v err=%v", result, err)
	}

	storeB := NewStore(storeA.Root())
	result, err := storeB.WriteToolOutputArtifact(sessionID, artifactRoot, "second", []byte("abcdef"), quota)
	if err != nil {
		t.Fatalf("write after store restart: %v", err)
	}
	if result.PersistedBytes != 4 || result.OmittedBytes != 2 || result.Reason != ToolOutputArtifactReasonSessionBytes {
		t.Fatalf("restarted store did not rebuild quota from directory facts: %#v", result)
	}
}

func TestToolOutputArtifactQuotaSerializesConcurrentWriters(t *testing.T) {
	store, sessionID, artifactRoot := newToolOutputArtifactTestStore(t)
	quota := ToolOutputArtifactQuota{FileMaxBytes: 100, SessionMaxBytes: 1000, MaxFiles: 3}
	const writers = 12
	results := make(chan ToolOutputArtifactResult, writers)
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			concurrentStore := NewStore(store.Root())
			result, err := concurrentStore.WriteToolOutputArtifact(sessionID, artifactRoot, fmt.Sprintf("writer-%02d", i), []byte(fmt.Sprintf("payload-%02d", i)), quota)
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent artifact write: %v", err)
		}
	}
	written := 0
	denied := 0
	for result := range results {
		if result.AbsolutePath != "" {
			written++
		} else if result.Reason == ToolOutputArtifactReasonSessionFiles {
			denied++
		} else {
			t.Fatalf("unexpected concurrent result: %#v", result)
		}
	}
	if written != quota.MaxFiles || denied != writers-quota.MaxFiles {
		t.Fatalf("concurrent quota outcome written=%d denied=%d", written, denied)
	}
}

func TestToolOutputArtifactRejectsSymlinkAliases(t *testing.T) {
	t.Run("root", func(t *testing.T) {
		store, sessionID, _ := newToolOutputArtifactTestStore(t)
		outside := t.TempDir()
		alias := filepath.Join(t.TempDir(), "tool-outputs")
		if err := os.Symlink(outside, alias); err != nil {
			t.Fatalf("symlink root: %v", err)
		}
		_, err := store.WriteToolOutputArtifact(sessionID, alias, "call", []byte("secret"), ToolOutputArtifactQuota{FileMaxBytes: 100, SessionMaxBytes: 100, MaxFiles: 1})
		if err == nil {
			t.Fatal("expected symlinked artifact root to be rejected")
		}
		entries, readErr := os.ReadDir(outside)
		if readErr != nil || len(entries) != 0 {
			t.Fatalf("symlink target changed: entries=%#v err=%v", entries, readErr)
		}
	})

	t.Run("target", func(t *testing.T) {
		store, sessionID, artifactRoot := newToolOutputArtifactTestStore(t)
		if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
			t.Fatalf("mkdir artifact root: %v", err)
		}
		payload := []byte("do not overwrite")
		filename := toolOutputArtifactFilename("call", payload)
		outside := filepath.Join(t.TempDir(), "outside.txt")
		if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
			t.Fatalf("write outside: %v", err)
		}
		if err := os.Symlink(outside, filepath.Join(artifactRoot, filename)); err != nil {
			t.Fatalf("symlink target: %v", err)
		}
		_, err := store.WriteToolOutputArtifact(sessionID, artifactRoot, "call", payload, ToolOutputArtifactQuota{FileMaxBytes: 100, SessionMaxBytes: 100, MaxFiles: 2})
		if err == nil {
			t.Fatal("expected symlinked artifact target to be rejected")
		}
		data, readErr := os.ReadFile(outside)
		if readErr != nil || string(data) != "keep" {
			t.Fatalf("symlink target changed: data=%q err=%v", data, readErr)
		}
	})
}

func TestToolOutputArtifactReportsWriteFailure(t *testing.T) {
	store, sessionID, artifactRoot := newToolOutputArtifactTestStore(t)
	restore := beforeToolOutputArtifactWrite
	forcedErr := errors.New("forced artifact disk error")
	beforeToolOutputArtifactWrite = func(string, []byte) error { return forcedErr }
	t.Cleanup(func() { beforeToolOutputArtifactWrite = restore })

	_, err := store.WriteToolOutputArtifact(sessionID, artifactRoot, "call", []byte("payload"), ToolOutputArtifactQuota{FileMaxBytes: 100, SessionMaxBytes: 100, MaxFiles: 2})
	if !errors.Is(err, forcedErr) {
		t.Fatalf("expected forced artifact write failure, got %v", err)
	}
}
