package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestToolOutputArtifactStreamWritesCompleteOwnerOnlyFile(t *testing.T) {
	store, sessionID, artifactRoot := newToolOutputArtifactTestStore(t)
	quota := ToolOutputArtifactQuota{FileMaxBytes: 4096, SessionMaxBytes: 8192, MaxFiles: 4}
	stream, initial, err := store.BeginToolOutputArtifactStream(sessionID, artifactRoot, "shell-call_stream", quota)
	if err != nil || stream == nil {
		t.Fatalf("begin stream: stream=%#v initial=%#v err=%v", stream, initial, err)
	}

	chunks := [][]byte{[]byte("stdout-1\n"), []byte("stderr-1\n"), []byte("stdout-2\n")}
	var want []byte
	for _, chunk := range chunks {
		want = append(want, chunk...)
		if n, writeErr := stream.Write(chunk); writeErr != nil || n != len(chunk) {
			t.Fatalf("stream write n=%d err=%v", n, writeErr)
		}
	}
	result, err := stream.Close()
	if err != nil {
		t.Fatalf("close stream: %v", err)
	}
	if result.RawBytes != len(want) || result.PersistedBytes != len(want) || result.OmittedBytes != 0 || !result.Complete || result.Truncated || !result.Recoverable {
		t.Fatalf("unexpected complete stream result: %#v", result)
	}
	data, err := os.ReadFile(result.AbsolutePath)
	if err != nil {
		t.Fatalf("read complete stream artifact: %v", err)
	}
	if !bytes.Equal(data, want) {
		t.Fatalf("stream artifact changed merged bytes: got %q want %q", data, want)
	}
	for path, wantMode := range map[string]os.FileMode{
		artifactRoot:        0o700,
		result.AbsolutePath: 0o600,
	} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("stat %s: %v", path, statErr)
		}
		if got := info.Mode().Perm(); got != wantMode {
			t.Fatalf("mode for %s=%#o, want %#o", path, got, wantMode)
		}
	}
	assertNoToolOutputStreamInternals(t, artifactRoot)
}

func TestToolOutputArtifactStreamEnforcesQuotaAcrossConcurrentStores(t *testing.T) {
	store, sessionID, artifactRoot := newToolOutputArtifactTestStore(t)
	quota := ToolOutputArtifactQuota{FileMaxBytes: 64, SessionMaxBytes: 17, MaxFiles: 4}
	streams := make([]*ToolOutputArtifactStream, 2)
	for index := range streams {
		stream, initial, err := NewStore(store.Root()).BeginToolOutputArtifactStream(sessionID, artifactRoot, "writer-"+string(rune('a'+index)), quota)
		if err != nil || stream == nil {
			t.Fatalf("begin writer %d: initial=%#v err=%v", index, initial, err)
		}
		streams[index] = stream
	}

	payloads := [][]byte{[]byte("0123456789ABCDEF"), []byte("abcdefghijklmnop")}
	var wg sync.WaitGroup
	for index := range streams {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = streams[index].Write(payloads[index])
		}()
	}
	wg.Wait()

	results := make([]ToolOutputArtifactResult, len(streams))
	for index, stream := range streams {
		result, err := stream.Close()
		if err != nil {
			t.Fatalf("close writer %d: %v", index, err)
		}
		results[index] = result
	}
	persisted := results[0].PersistedBytes + results[1].PersistedBytes
	if persisted != quota.SessionMaxBytes {
		t.Fatalf("concurrent streams persisted=%d, want session quota %d: %#v", persisted, quota.SessionMaxBytes, results)
	}
	for index, result := range results {
		if result.RawBytes != len(payloads[index]) || result.OmittedBytes != result.RawBytes-result.PersistedBytes {
			t.Fatalf("writer %d accounting mismatch: %#v", index, result)
		}
		if result.PersistedBytes < result.RawBytes && (result.Reason != ToolOutputArtifactReasonSessionBytes || result.Recoverable || !result.Truncated) {
			t.Fatalf("writer %d quota result mislabeled: %#v", index, result)
		}
	}
	usageBytes, usageFiles, err := scanToolOutputArtifactUsage(artifactRoot)
	if err != nil {
		t.Fatalf("scan usage: %v", err)
	}
	if usageBytes != quota.SessionMaxBytes || usageFiles != len(streams) {
		t.Fatalf("usage bytes/files=%d/%d, want %d/%d", usageBytes, usageFiles, quota.SessionMaxBytes, len(streams))
	}
}

func TestToolOutputArtifactStreamEnforcesFileCountQuota(t *testing.T) {
	store, sessionID, artifactRoot := newToolOutputArtifactTestStore(t)
	quota := ToolOutputArtifactQuota{FileMaxBytes: 64, SessionMaxBytes: 128, MaxFiles: 1}
	first, initial, err := store.BeginToolOutputArtifactStream(sessionID, artifactRoot, "first", quota)
	if err != nil || first == nil {
		t.Fatalf("begin first stream: initial=%#v err=%v", initial, err)
	}
	_, _ = first.Write([]byte("first artifact"))
	firstResult, err := first.Close()
	if err != nil || !firstResult.Complete {
		t.Fatalf("close first stream: result=%#v err=%v", firstResult, err)
	}

	second, secondInitial, err := NewStore(store.Root()).BeginToolOutputArtifactStream(sessionID, artifactRoot, "second", quota)
	if err != nil || second != nil || secondInitial.Reason != ToolOutputArtifactReasonSessionFiles {
		t.Fatalf("file-count quota was not enforced: stream=%#v initial=%#v err=%v", second, secondInitial, err)
	}
	assertNoToolOutputStreamInternals(t, artifactRoot)
}

func TestToolOutputArtifactStreamReclaimsAbandonedReservation(t *testing.T) {
	store, sessionID, artifactRoot := newToolOutputArtifactTestStore(t)
	if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
		t.Fatalf("mkdir artifact root: %v", err)
	}
	tempName := toolOutputArtifactInflightPrefix + "abandoned.tmp"
	reservationName := toolOutputArtifactReservationPrefix + "abandoned.json"
	if err := os.WriteFile(filepath.Join(artifactRoot, tempName), []byte("abandoned"), 0o600); err != nil {
		t.Fatalf("write abandoned temp: %v", err)
	}
	record := toolOutputArtifactReservation{
		SchemaVersion:   1,
		TempName:        tempName,
		ReservedBytes:   1024,
		CreatedAt:       time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano),
		UpdatedAt:       time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano),
		WorkerPID:       999999,
		ProcessIdentity: "linux:dead:owner",
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal abandoned reservation: %v", err)
	}
	if err := os.WriteFile(filepath.Join(artifactRoot, reservationName), encoded, 0o600); err != nil {
		t.Fatalf("write abandoned reservation: %v", err)
	}

	stream, initial, err := NewStore(store.Root()).BeginToolOutputArtifactStream(sessionID, artifactRoot, "replacement", ToolOutputArtifactQuota{
		FileMaxBytes:    32,
		SessionMaxBytes: 32,
		MaxFiles:        1,
	})
	if err != nil || stream == nil {
		t.Fatalf("dead reservation was not reclaimed: initial=%#v err=%v", initial, err)
	}
	_, _ = stream.Write([]byte("replacement"))
	if _, err := stream.Close(); err != nil {
		t.Fatalf("close replacement stream: %v", err)
	}
	for _, stale := range []string{tempName, reservationName} {
		if _, err := os.Lstat(filepath.Join(artifactRoot, stale)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale stream file %s was not removed: %v", stale, err)
		}
	}
}

func TestToolOutputArtifactStreamReportsLifecycleFailures(t *testing.T) {
	operations := []string{"create", "write", "sync", "close", "rename"}
	for _, operation := range operations {
		operation := operation
		t.Run(operation, func(t *testing.T) {
			store, sessionID, artifactRoot := newToolOutputArtifactTestStore(t)
			forcedErr := errors.New("forced stream " + operation + " failure")
			restore := beforeToolOutputArtifactStreamOperation
			writeCalls := 0
			beforeToolOutputArtifactStreamOperation = func(gotOperation, path string) error {
				if gotOperation != operation {
					return nil
				}
				if operation == "write" {
					writeCalls++
					if writeCalls == 1 {
						return nil
					}
				}
				return forcedErr
			}
			t.Cleanup(func() { beforeToolOutputArtifactStreamOperation = restore })

			stream, initial, err := store.BeginToolOutputArtifactStream(sessionID, artifactRoot, "failure-"+operation, ToolOutputArtifactQuota{
				FileMaxBytes:    64,
				SessionMaxBytes: 128,
				MaxFiles:        2,
			})
			if operation == "create" {
				if stream != nil || !errors.Is(err, forcedErr) || initial.Reason != ToolOutputArtifactReasonCreateFailed {
					t.Fatalf("unexpected create failure: stream=%#v initial=%#v err=%v", stream, initial, err)
				}
				return
			}
			if err != nil || stream == nil {
				t.Fatalf("begin %s stream: initial=%#v err=%v", operation, initial, err)
			}
			_, _ = stream.Write([]byte("prefix"))
			if operation == "write" {
				_, _ = stream.Write([]byte("tail"))
			}
			result, closeErr := stream.Close()
			if !errors.Is(closeErr, forcedErr) {
				t.Fatalf("%s failure was not reported: result=%#v err=%v", operation, result, closeErr)
			}
			wantReason := map[string]string{
				"write":  ToolOutputArtifactReasonWriteFailed,
				"sync":   ToolOutputArtifactReasonSyncFailed,
				"close":  ToolOutputArtifactReasonCloseFailed,
				"rename": ToolOutputArtifactReasonRenameFailed,
			}[operation]
			if result.Reason != wantReason || result.Complete || result.Recoverable {
				t.Fatalf("%s failure mislabeled: %#v", operation, result)
			}
			if operation == "write" {
				if result.AbsolutePath == "" || !result.Truncated || result.PersistedBytes != len("prefix") {
					t.Fatalf("write failure should publish the durable prefix: %#v", result)
				}
			} else if result.AbsolutePath != "" || result.PersistedBytes != 0 || result.OmittedBytes != result.RawBytes {
				t.Fatalf("%s failure must not publish an uncertain artifact: %#v", operation, result)
			}
			assertNoToolOutputStreamInternals(t, artifactRoot)
		})
	}
}

func TestToolOutputArtifactStreamIntegrityFailureOverridesWritePrefixPublication(t *testing.T) {
	for _, integrityOperation := range []string{"sync", "close"} {
		integrityOperation := integrityOperation
		t.Run(integrityOperation, func(t *testing.T) {
			store, sessionID, artifactRoot := newToolOutputArtifactTestStore(t)
			writeErr := errors.New("forced stream write failure")
			integrityErr := errors.New("forced stream " + integrityOperation + " failure")
			restore := beforeToolOutputArtifactStreamOperation
			writeCalls := 0
			beforeToolOutputArtifactStreamOperation = func(operation, path string) error {
				switch operation {
				case "write":
					writeCalls++
					if writeCalls == 2 {
						return writeErr
					}
				case integrityOperation:
					return integrityErr
				}
				return nil
			}
			t.Cleanup(func() { beforeToolOutputArtifactStreamOperation = restore })

			stream, initial, err := store.BeginToolOutputArtifactStream(sessionID, artifactRoot, "combined-failure-"+integrityOperation, ToolOutputArtifactQuota{
				FileMaxBytes:    64,
				SessionMaxBytes: 128,
				MaxFiles:        2,
			})
			if err != nil || stream == nil {
				t.Fatalf("begin combined failure stream: initial=%#v err=%v", initial, err)
			}
			_, _ = stream.Write([]byte("durable-prefix"))
			_, _ = stream.Write([]byte("lost-tail"))
			result, closeErr := stream.Close()
			if !errors.Is(closeErr, writeErr) || !errors.Is(closeErr, integrityErr) {
				t.Fatalf("combined failures were not both retained: result=%#v err=%v", result, closeErr)
			}
			wantReason := map[string]string{
				"sync":  ToolOutputArtifactReasonSyncFailed,
				"close": ToolOutputArtifactReasonCloseFailed,
			}[integrityOperation]
			if result.Reason != wantReason || result.AbsolutePath != "" || result.PersistedBytes != 0 || result.OmittedBytes != result.RawBytes || result.Complete || result.Truncated || result.Recoverable {
				t.Fatalf("integrity failure published an uncertain prefix: %#v", result)
			}
			assertNoToolOutputStreamInternals(t, artifactRoot)
		})
	}
}

func TestToolOutputArtifactStreamKeepsPublishedArtifactWhenReservationCleanupFails(t *testing.T) {
	store, sessionID, artifactRoot := newToolOutputArtifactTestStore(t)
	cleanupErr := errors.New("forced reservation cleanup failure")
	restore := beforeToolOutputArtifactStreamOperation
	beforeToolOutputArtifactStreamOperation = func(operation, path string) error {
		if operation == "release" {
			return cleanupErr
		}
		return nil
	}
	t.Cleanup(func() { beforeToolOutputArtifactStreamOperation = restore })

	stream, initial, err := store.BeginToolOutputArtifactStream(sessionID, artifactRoot, "published-cleanup-failure", ToolOutputArtifactQuota{
		FileMaxBytes:    64,
		SessionMaxBytes: 128,
		MaxFiles:        2,
	})
	if err != nil || stream == nil {
		t.Fatalf("begin cleanup failure stream: initial=%#v err=%v", initial, err)
	}
	payload := []byte("complete-and-durable")
	_, _ = stream.Write(payload)
	result, closeErr := stream.Close()
	if !errors.Is(closeErr, cleanupErr) {
		t.Fatalf("reservation cleanup failure was not reported: result=%#v err=%v", result, closeErr)
	}
	if result.AbsolutePath == "" || !result.Complete || result.Truncated || !result.Recoverable || result.PersistedBytes != len(payload) || result.OmittedBytes != 0 || result.Reason != ToolOutputArtifactReasonReservationFail {
		t.Fatalf("published artifact was hidden after cleanup failure: %#v", result)
	}
	data, readErr := os.ReadFile(result.AbsolutePath)
	if readErr != nil || !bytes.Equal(data, payload) {
		t.Fatalf("published artifact was not readable after cleanup failure: data=%q err=%v", data, readErr)
	}
	assertNoToolOutputStreamInternals(t, artifactRoot)
}

func TestToolOutputArtifactStreamReservationUpdateCommitsAfterRecordWrite(t *testing.T) {
	store, sessionID, artifactRoot := newToolOutputArtifactTestStore(t)
	reserveErr := errors.New("forced reservation record update failure")
	restore := beforeToolOutputArtifactStreamOperation
	beforeToolOutputArtifactStreamOperation = func(operation, path string) error {
		if operation == "reserve" {
			return reserveErr
		}
		return nil
	}
	t.Cleanup(func() { beforeToolOutputArtifactStreamOperation = restore })

	stream, initial, err := store.BeginToolOutputArtifactStream(sessionID, artifactRoot, "reservation-update-failure", ToolOutputArtifactQuota{
		FileMaxBytes:    64,
		SessionMaxBytes: 128,
		MaxFiles:        2,
	})
	if err != nil || stream == nil {
		t.Fatalf("begin reservation update failure stream: initial=%#v err=%v", initial, err)
	}
	_, _ = stream.Write([]byte("must-not-be-written"))
	if stream.reservation.ReservedBytes != 0 || stream.persistedBytes != 0 {
		t.Fatalf("failed reservation update changed committed state: reservation=%#v persisted=%d", stream.reservation, stream.persistedBytes)
	}
	result, closeErr := stream.Close()
	if !errors.Is(closeErr, reserveErr) || result.Reason != ToolOutputArtifactReasonReservationFail || result.AbsolutePath != "" || result.PersistedBytes != 0 || result.OmittedBytes != result.RawBytes {
		t.Fatalf("reservation update failure was not fail-closed: result=%#v err=%v", result, closeErr)
	}
	assertNoToolOutputStreamInternals(t, artifactRoot)
}

func TestToolOutputArtifactStreamFinalizeDoesNotReplaceExistingOrSymlinkTarget(t *testing.T) {
	for _, targetKind := range []string{"regular", "symlink"} {
		targetKind := targetKind
		t.Run(targetKind, func(t *testing.T) {
			store, sessionID, artifactRoot := newToolOutputArtifactTestStore(t)
			outside := filepath.Join(t.TempDir(), "outside.txt")
			outsideOriginal := []byte("outside-original")
			if err := os.WriteFile(outside, outsideOriginal, 0o600); err != nil {
				t.Fatalf("write outside target: %v", err)
			}
			restore := beforeToolOutputArtifactStreamOperation
			var finalPath string
			beforeToolOutputArtifactStreamOperation = func(operation, path string) error {
				if operation != "rename" {
					return nil
				}
				finalPath = path
				if targetKind == "symlink" {
					return os.Symlink(outside, path)
				}
				return os.WriteFile(path, []byte("existing-final"), 0o600)
			}
			t.Cleanup(func() { beforeToolOutputArtifactStreamOperation = restore })

			stream, initial, err := store.BeginToolOutputArtifactStream(sessionID, artifactRoot, "no-replace-"+targetKind, ToolOutputArtifactQuota{
				FileMaxBytes:    64,
				SessionMaxBytes: 128,
				MaxFiles:        4,
			})
			if err != nil || stream == nil {
				t.Fatalf("begin no-replace stream: initial=%#v err=%v", initial, err)
			}
			_, _ = stream.Write([]byte("must-not-replace-target"))
			result, closeErr := stream.Close()
			if closeErr == nil || result.Reason != ToolOutputArtifactReasonRenameFailed || result.AbsolutePath != "" || result.PersistedBytes != 0 || result.OmittedBytes != result.RawBytes {
				t.Fatalf("existing finalize target was replaced or mislabeled: result=%#v err=%v", result, closeErr)
			}
			if finalPath == "" {
				t.Fatal("rename hook did not observe final path")
			}
			if targetKind == "regular" {
				data, readErr := os.ReadFile(finalPath)
				if readErr != nil || string(data) != "existing-final" {
					t.Fatalf("existing regular target changed: data=%q err=%v", data, readErr)
				}
			} else {
				data, readErr := os.ReadFile(outside)
				if readErr != nil || !bytes.Equal(data, outsideOriginal) {
					t.Fatalf("symlink target changed: data=%q err=%v", data, readErr)
				}
			}
			assertNoToolOutputStreamInternals(t, artifactRoot)
		})
	}
}

func TestToolOutputArtifactStreamRejectsSymlinkRoot(t *testing.T) {
	store, sessionID, _ := newToolOutputArtifactTestStore(t)
	outside := t.TempDir()
	alias := filepath.Join(t.TempDir(), "tool-outputs")
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatalf("symlink artifact root: %v", err)
	}
	stream, _, err := store.BeginToolOutputArtifactStream(sessionID, alias, "call", ToolOutputArtifactQuota{FileMaxBytes: 64, SessionMaxBytes: 64, MaxFiles: 1})
	if stream != nil || err == nil || !strings.Contains(strings.ToLower(err.Error()), "symlink") {
		t.Fatalf("expected symlink root rejection, stream=%#v err=%v", stream, err)
	}
	entries, readErr := os.ReadDir(outside)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("symlink target changed: entries=%#v err=%v", entries, readErr)
	}
}

func assertNoToolOutputStreamInternals(t *testing.T, artifactRoot string) {
	t.Helper()
	entries, err := os.ReadDir(artifactRoot)
	if err != nil {
		t.Fatalf("read artifact root: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), toolOutputArtifactReservationPrefix) || strings.HasPrefix(entry.Name(), toolOutputArtifactInflightPrefix) {
			t.Fatalf("stream internal leaked after finalization: %s", entry.Name())
		}
	}
}
