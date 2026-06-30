package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go-cli-agent/internal/events"
	"go-cli-agent/internal/fileutil"
	"golang.org/x/sys/unix"
)

func countStoreEventType(items []events.Event, target string) int {
	count := 0
	for _, item := range items {
		if item.Type == target {
			count++
		}
	}
	return count
}

func TestStoreEnsureRootReappliesOwnerOnlyMode(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(root, 0o777); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatalf("chmod drift: %v", err)
	}
	store := NewStoreWithDirMode(root, 0o700)
	if err := store.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("expected root mode 0700, got %s", perm.String())
	}
}

func TestStoreEnsureRootChmodDoesNotFollowReplacedSymlink(t *testing.T) {
	temp := t.TempDir()
	root := filepath.Join(temp, "sessions")
	outside := filepath.Join(temp, "outside")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	if err := os.Mkdir(outside, 0o777); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := os.Chmod(outside, 0o777); err != nil {
		t.Fatalf("chmod outside: %v", err)
	}

	restore := beforeChmodBestEffort
	swapped := false
	beforeChmodBestEffort = func(chmodPath string, mode os.FileMode) error {
		if swapped || filepath.Clean(chmodPath) != filepath.Clean(root) {
			return nil
		}
		swapped = true
		if err := os.Remove(root); err != nil {
			return err
		}
		return os.Symlink(outside, root)
	}
	defer func() {
		beforeChmodBestEffort = restore
	}()

	store := NewStoreWithDirMode(root, 0o700)
	if err := store.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	if !swapped {
		t.Fatal("expected test hook to replace root before chmod")
	}
	info, err := os.Stat(outside)
	if err != nil {
		t.Fatalf("stat outside: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o777 {
		t.Fatalf("expected outside mode to remain 0777, got %s", perm.String())
	}
}

func TestStoreAppendMessageReappliesParentAndFileModes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	sessionDir := store.SessionDir(meta.ID)
	messagePath := filepath.Join(sessionDir, "messages.jsonl")
	if err := os.Chmod(sessionDir, 0o777); err != nil {
		t.Fatalf("chmod session dir: %v", err)
	}
	if err := os.Chmod(messagePath, 0o666); err != nil {
		t.Fatalf("chmod message file: %v", err)
	}

	if err := store.AppendMessage(meta.ID, NewMessage("user", "hello")); err != nil {
		t.Fatalf("append message: %v", err)
	}

	sessionInfo, err := os.Stat(sessionDir)
	if err != nil {
		t.Fatalf("stat session dir: %v", err)
	}
	if perm := sessionInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("expected session dir mode 0700, got %s", perm.String())
	}
	messageInfo, err := os.Stat(messagePath)
	if err != nil {
		t.Fatalf("stat messages: %v", err)
	}
	if perm := messageInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("expected messages mode 0600, got %s", perm.String())
	}
}

func TestStoreAppendEventsAppendsBatchAtomically(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	first := events.New(meta.ID, "first.event", "test", nil)
	if err := store.AppendEvent(meta.ID, first); err != nil {
		t.Fatalf("append first event: %v", err)
	}
	batch := []events.Event{
		events.New(meta.ID, "batch.one", "test", map[string]any{"index": 1}),
		events.New(meta.ID, "batch.two", "test", map[string]any{"index": 2}),
	}
	if err := store.AppendEvents(meta.ID, batch); err != nil {
		t.Fatalf("append event batch: %v", err)
	}
	loaded, err := store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	if len(loaded) != 3 || loaded[0].Type != "first.event" || loaded[1].Type != "batch.one" || loaded[2].Type != "batch.two" {
		t.Fatalf("unexpected event order after batch append: %#v", loaded)
	}
	eventsPath := filepath.Join(store.SessionDir(meta.ID), "events.jsonl")
	if err := os.Remove(eventsPath); err != nil {
		t.Fatalf("remove events: %v", err)
	}
	if err := os.Mkdir(eventsPath, 0o700); err != nil {
		t.Fatalf("replace events with directory: %v", err)
	}
	err = store.AppendEvents(meta.ID, []events.Event{events.New(meta.ID, "blocked.event", "test", nil)})
	if err == nil || !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected blocked event batch error, got %v", err)
	}
}

func TestLoadEventsRejectsMalformedSnapshot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	malformed := events.New(meta.ID, "bad.event", "test", nil)
	malformed.SessionID = NewSessionID()
	path := filepath.Join(store.SessionDir(meta.ID), "events.jsonl")
	if err := store.writeEventsJSONL(path, []events.Event{malformed}); err != nil {
		t.Fatalf("write malformed events: %v", err)
	}
	if _, err := store.LoadEvents(meta.ID); err == nil || !strings.Contains(err.Error(), "validate events.jsonl") || !strings.Contains(err.Error(), "does not match session") {
		t.Fatalf("expected malformed events validation error, got %v", err)
	}

	malformed = events.New(meta.ID, "bad.event", "test", nil)
	malformed.Time = "not-a-time"
	if err := store.writeEventsJSONL(path, []events.Event{malformed}); err != nil {
		t.Fatalf("write invalid-time events: %v", err)
	}
	if _, err := store.LoadEvents(meta.ID); err == nil || !strings.Contains(err.Error(), "validate events.jsonl") || !strings.Contains(err.Error(), "time must be RFC3339Nano") {
		t.Fatalf("expected invalid event time validation error, got %v", err)
	}
}

func TestEventWritesRejectMalformedFacts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	valid := events.New(meta.ID, "valid.event", "test", nil)
	if err := store.AppendEvent(meta.ID, valid); err != nil {
		t.Fatalf("append valid event: %v", err)
	}

	blankType := events.New(meta.ID, "invalid.event", "test", nil)
	blankType.Type = " "
	if err := store.AppendEvent(meta.ID, blankType); err == nil || !strings.Contains(err.Error(), "validate events.jsonl") || !strings.Contains(err.Error(), "type is required") {
		t.Fatalf("expected blank type append rejection, got %v", err)
	}
	invalidTime := events.New(meta.ID, "invalid.event", "test", nil)
	invalidTime.Time = "not-a-time"
	if err := store.AppendEvent(meta.ID, invalidTime); err == nil || !strings.Contains(err.Error(), "validate events.jsonl") || !strings.Contains(err.Error(), "time must be RFC3339Nano") {
		t.Fatalf("expected invalid time append rejection, got %v", err)
	}
	mismatchedSession := events.New(meta.ID, "other.event", "test", nil)
	mismatchedSession.SessionID = NewSessionID()
	if err := store.AppendEvents(meta.ID, []events.Event{mismatchedSession}); err == nil || !strings.Contains(err.Error(), "does not match session") {
		t.Fatalf("expected mismatched session batch rejection, got %v", err)
	}
	loaded, err := store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	if len(loaded) != 1 || loaded[0].ID != valid.ID {
		t.Fatalf("malformed event writes changed durable log: %#v", loaded)
	}
}

func TestAppendEventRejectsMalformedExistingLog(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	malformed := events.New(meta.ID, "bad.event", "test", nil)
	malformed.Time = "not-a-time"
	path := filepath.Join(store.SessionDir(meta.ID), "events.jsonl")
	if err := store.writeEventsJSONL(path, []events.Event{malformed}); err != nil {
		t.Fatalf("write malformed events: %v", err)
	}

	err := store.AppendEvent(meta.ID, events.New(meta.ID, "later.event", "test", nil))
	if err == nil || !strings.Contains(err.Error(), "validate events.jsonl") || !strings.Contains(err.Error(), "time must be RFC3339Nano") {
		t.Fatalf("expected malformed existing event log append error, got %v", err)
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read events: %v", readErr)
	}
	if got := strings.Count(string(raw), "\n"); got != 1 {
		t.Fatalf("malformed existing event log should not be extended, got %d records: %q", got, string(raw))
	}
}

func TestAppendEventRepairsTrailingPartialExistingLog(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	first := events.New(meta.ID, "first.event", "test", nil)
	if err := store.AppendEvent(meta.ID, first); err != nil {
		t.Fatalf("append first event: %v", err)
	}
	path := filepath.Join(store.SessionDir(meta.ID), "events.jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open events: %v", err)
	}
	if _, err := file.Write([]byte(`{"id":"partial-event"`)); err != nil {
		_ = file.Close()
		t.Fatalf("write partial event: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close events: %v", err)
	}

	later := events.New(meta.ID, "later.event", "test", nil)
	if err := store.AppendEvent(meta.ID, later); err != nil {
		t.Fatalf("append after trailing partial repair: %v", err)
	}
	loaded, err := store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load events after repair: %v", err)
	}
	if len(loaded) != 3 {
		t.Fatalf("expected first, repair, and later events, got %#v", loaded)
	}
	if loaded[0].ID != first.ID || loaded[1].Type != "store.jsonl.repaired" || loaded[2].ID != later.ID {
		t.Fatalf("unexpected repaired event order: %#v", loaded)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if strings.Contains(string(raw), "partial-event") {
		t.Fatalf("partial event fragment remained after repair: %q", string(raw))
	}
}

func TestAppendEventRejectsDuplicateFromValidationCache(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	event := events.New(meta.ID, "first.event", "test", nil)
	if err := store.AppendEvent(meta.ID, event); err != nil {
		t.Fatalf("append first event: %v", err)
	}
	if err := store.AppendEvent(meta.ID, event); err == nil || !strings.Contains(err.Error(), "duplicate event id") {
		t.Fatalf("expected duplicate event rejection, got %v", err)
	}
	loaded, err := store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	if len(loaded) != 1 || loaded[0].ID != event.ID {
		t.Fatalf("duplicate append changed durable log: %#v", loaded)
	}
}

func TestAppendEventNormalizesValidTrailingRecordWithoutNewline(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	first := events.New(meta.ID, "first.event", "test", nil)
	raw, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal first event: %v", err)
	}
	path := filepath.Join(store.SessionDir(meta.ID), "events.jsonl")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write event without trailing newline: %v", err)
	}

	later := events.New(meta.ID, "later.event", "test", nil)
	if err := store.AppendEvent(meta.ID, later); err != nil {
		t.Fatalf("append after valid unterminated line: %v", err)
	}
	loaded, err := store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	if len(loaded) != 2 || loaded[0].ID != first.ID || loaded[1].ID != later.ID {
		t.Fatalf("unexpected events after newline normalization: %#v", loaded)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if got := strings.Count(string(raw), "\n"); got != 2 {
		t.Fatalf("expected normalized JSONL to contain two lines, got %d: %q", got, string(raw))
	}
}

func TestRestoreEventsReplacesDurableLogAndRejectsMalformedFacts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	first := events.New(meta.ID, "first.event", "test", nil)
	second := events.New(meta.ID, "second.event", "test", nil)
	if err := store.AppendEvents(meta.ID, []events.Event{first, second}); err != nil {
		t.Fatalf("append events: %v", err)
	}
	if err := store.RestoreEvents(meta.ID, []events.Event{first}); err != nil {
		t.Fatalf("restore events: %v", err)
	}
	loaded, err := store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load restored events: %v", err)
	}
	if len(loaded) != 1 || loaded[0].ID != first.ID {
		t.Fatalf("unexpected restored events: %#v", loaded)
	}
	malformed := events.New(meta.ID, "bad.event", "test", nil)
	malformed.SessionID = NewSessionID()
	if err := store.RestoreEvents(meta.ID, []events.Event{malformed}); err == nil || !strings.Contains(err.Error(), "does not match session") {
		t.Fatalf("expected malformed restore rejection, got %v", err)
	}
	loaded, err = store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load events after rejected restore: %v", err)
	}
	if len(loaded) != 1 || loaded[0].ID != first.ID {
		t.Fatalf("rejected restore changed durable log: %#v", loaded)
	}
}

func TestStoreAppendMessageRejectsSymlinkJSONL(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.WriteFile(outside, []byte("keep\n"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	messagePath := filepath.Join(store.SessionDir(meta.ID), "messages.jsonl")
	if err := os.Remove(messagePath); err != nil {
		t.Fatalf("remove messages: %v", err)
	}
	if err := os.Symlink(outside, messagePath); err != nil {
		t.Fatalf("symlink messages: %v", err)
	}

	if err := store.AppendMessage(meta.ID, NewMessage("user", "hello")); err == nil {
		t.Fatal("expected symlinked messages.jsonl append to fail")
	}
	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("read outside: %v", err)
	}
	if string(data) != "keep\n" {
		t.Fatalf("outside symlink target was modified: %q", string(data))
	}
}

func TestStoreAppendMessageRejectsReplacedParent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	sessionDir := store.SessionDir(meta.ID)
	outside := filepath.Join(t.TempDir(), "outside-session")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	outsideMessages := filepath.Join(outside, "messages.jsonl")
	if err := os.WriteFile(outsideMessages, []byte("keep\n"), 0o600); err != nil {
		t.Fatalf("write outside messages: %v", err)
	}

	restore := beforeOpenNoSymlink
	swapped := false
	beforeOpenNoSymlink = func(openPath string, flags int) error {
		if swapped ||
			flags&unix.O_APPEND == 0 ||
			flags&(unix.O_WRONLY|unix.O_RDWR) == 0 ||
			filepath.Clean(openPath) != filepath.Join(sessionDir, "messages.jsonl") {
			return nil
		}
		swapped = true
		if err := os.Rename(sessionDir, sessionDir+".real"); err != nil {
			return err
		}
		return os.Symlink(outside, sessionDir)
	}
	defer func() {
		beforeOpenNoSymlink = restore
	}()

	err := store.AppendMessage(meta.ID, NewMessage("user", "hello"))
	if err == nil {
		t.Fatal("expected replaced parent append to fail")
	}
	if !strings.Contains(err.Error(), "symlink") && !strings.Contains(err.Error(), "changed") {
		t.Fatalf("expected symlink/path-change error, got %v", err)
	}
	data, readErr := os.ReadFile(outsideMessages)
	if readErr != nil {
		t.Fatalf("read outside messages: %v", readErr)
	}
	if string(data) != "keep\n" {
		t.Fatalf("outside messages should not be modified, got %q", string(data))
	}
}

func TestStoreRemoveLastMessageIfIDOnlyRemovesMatchingTail(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	first := NewMessage("user", "first")
	second := NewMessage("user", "second")
	if err := store.AppendMessage(meta.ID, first); err != nil {
		t.Fatalf("append first: %v", err)
	}
	if err := store.AppendMessage(meta.ID, second); err != nil {
		t.Fatalf("append second: %v", err)
	}
	if err := store.RemoveLastMessageIfID(meta.ID, first.ID); err == nil {
		t.Fatal("expected stale tail rollback to fail")
	}
	messages, err := store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("stale rollback should preserve both messages, got %#v", messages)
	}
	if err := store.RemoveLastMessageIfID(meta.ID, second.ID); err != nil {
		t.Fatalf("remove matching tail: %v", err)
	}
	messages, err = store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("load messages after remove: %v", err)
	}
	if len(messages) != 1 || messages[0].ID != first.ID {
		t.Fatalf("expected only first message to remain, got %#v", messages)
	}
}

func TestStoreLoadMessagesTailAndBeforeKeepBoundedWindows(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	written := make([]Message, 0, 7)
	for i := 0; i < 7; i++ {
		msg := NewMessage("user", fmt.Sprintf("message %d", i))
		if err := store.AppendMessage(meta.ID, msg); err != nil {
			t.Fatalf("append message %d: %v", i, err)
		}
		written = append(written, msg)
	}

	tail, hasMore, err := store.LoadMessagesTail(meta.ID, 3)
	if err != nil {
		t.Fatalf("load tail: %v", err)
	}
	if !hasMore || len(tail) != 3 || tail[0].ID != written[4].ID || tail[2].ID != written[6].ID {
		t.Fatalf("unexpected tail hasMore=%v messages=%#v", hasMore, tail)
	}

	page, hasMore, err := store.LoadMessagesBefore(meta.ID, written[5].ID, 2)
	if err != nil {
		t.Fatalf("load before: %v", err)
	}
	if !hasMore || len(page) != 2 || page[0].ID != written[3].ID || page[1].ID != written[4].ID {
		t.Fatalf("unexpected previous page hasMore=%v messages=%#v", hasMore, page)
	}

	page, hasMore, err = store.LoadMessagesBefore(meta.ID, written[1].ID, 3)
	if err != nil {
		t.Fatalf("load first page: %v", err)
	}
	if hasMore || len(page) != 1 || page[0].ID != written[0].ID {
		t.Fatalf("unexpected first page hasMore=%v messages=%#v", hasMore, page)
	}

	page, hasMore, err = store.LoadMessagesBefore(meta.ID, "missing-message", 2)
	if err != nil {
		t.Fatalf("load missing before id: %v", err)
	}
	if hasMore || len(page) != 0 {
		t.Fatalf("missing before id should return empty page, got hasMore=%v messages=%#v", hasMore, page)
	}
}

func TestStoreLoadMessagesTailValidatesBoundedWindow(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	valid := NewMessage("user", "valid")
	invalid := NewMessage("developer", "bad role")
	validTail := NewMessage("user", "valid tail")
	messagesPath := filepath.Join(store.SessionDir(meta.ID), "messages.jsonl")
	file, err := os.OpenFile(messagesPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("open messages: %v", err)
	}
	encoder := json.NewEncoder(file)
	for _, msg := range []Message{valid, invalid, validTail} {
		if err := encoder.Encode(msg); err != nil {
			t.Fatalf("encode message: %v", err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close messages: %v", err)
	}

	tail, hasMore, err := store.LoadMessagesTail(meta.ID, 1)
	if err != nil {
		t.Fatalf("tail load should only validate the bounded display window: %v", err)
	}
	if !hasMore || len(tail) != 1 || tail[0].ID != validTail.ID {
		t.Fatalf("unexpected tail hasMore=%v messages=%#v", hasMore, tail)
	}
	if _, _, err := store.LoadMessagesBefore(meta.ID, validTail.ID, 1); err == nil || !strings.Contains(err.Error(), "invalid message role") {
		t.Fatalf("expected before load to validate historical malformed record, got %v", err)
	}
	if _, err := store.LoadMessages(meta.ID); err == nil || !strings.Contains(err.Error(), "invalid message role") {
		t.Fatalf("expected full load to validate historical malformed record, got %v", err)
	}
}

func TestStoreLoadMessagesTailHandlesLargeTailRecords(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	first := NewMessage("user", "first")
	large := NewMessage("assistant", strings.Repeat("x", 96*1024))
	last := NewMessage("user", "last")
	for _, msg := range []Message{first, large, last} {
		if err := store.AppendMessage(meta.ID, msg); err != nil {
			t.Fatalf("append message: %v", err)
		}
	}

	tail, hasMore, err := store.LoadMessagesTail(meta.ID, 2)
	if err != nil {
		t.Fatalf("load tail: %v", err)
	}
	if !hasMore || len(tail) != 2 || tail[0].ID != large.ID || tail[1].ID != last.ID {
		t.Fatalf("unexpected large tail hasMore=%v messages=%#v", hasMore, tail)
	}
	if tail[0].Text != large.Text {
		t.Fatalf("large tail record was not preserved")
	}
}

func TestStoreFeatureListRejectsSymlink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	outside := filepath.Join(t.TempDir(), "outside-feature-list.json")
	original := []byte(`{"features":[{"id":"feature_0001","status":"pending"}]}` + "\n")
	if err := os.WriteFile(outside, original, 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	featureListPath := filepath.Join(store.SessionDir(meta.ID), "feature_list.json")
	if err := os.Symlink(outside, featureListPath); err != nil {
		t.Fatalf("symlink feature list: %v", err)
	}

	if _, err := store.LoadFeatureList(meta.ID); err == nil {
		t.Fatal("expected symlinked feature_list.json load to fail")
	}
	err := store.SaveFeatureList(meta.ID, FeatureList{Features: []Feature{{
		ID:          "feature_0001",
		Description: "Valid feature",
		Steps:       []string{"validate"},
		Status:      "completed",
	}}})
	if err == nil {
		t.Fatal("expected symlinked feature_list.json save to fail")
	}
	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("read outside: %v", err)
	}
	if string(data) != string(original) {
		t.Fatalf("outside symlink target was modified: %q", string(data))
	}
}

func TestSaveFeatureListRejectsInvalidItems(t *testing.T) {
	store := NewStore(t.TempDir())
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	cases := []struct {
		name        string
		featureList FeatureList
		want        string
	}{
		{
			name:        "empty list",
			featureList: FeatureList{},
			want:        "at least one feature is required",
		},
		{
			name: "blank id",
			featureList: FeatureList{Features: []Feature{{
				ID:          " ",
				Description: "Valid feature",
				Status:      "pending",
			}}},
			want: "feature 1 id is required",
		},
		{
			name: "blank description",
			featureList: FeatureList{Features: []Feature{{
				ID:          "feature_0001",
				Description: " ",
				Status:      "pending",
			}}},
			want: "feature 1 description is required",
		},
		{
			name: "blank step",
			featureList: FeatureList{Features: []Feature{{
				ID:          "feature_0001",
				Description: "Valid feature",
				Steps:       []string{" "},
				Status:      "pending",
			}}},
			want: "feature 1 step 1 is required",
		},
		{
			name: "invalid status",
			featureList: FeatureList{Features: []Feature{{
				ID:          "feature_0001",
				Description: "Valid feature",
				Status:      "blocked",
			}}},
			want: "invalid feature status",
		},
		{
			name: "negative passes",
			featureList: FeatureList{Features: []Feature{{
				ID:          "feature_0001",
				Description: "Valid feature",
				Status:      "pending",
				Passes:      -1,
			}}},
			want: "passes must be non-negative",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := store.SaveFeatureList(meta.ID, tc.featureList); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
			if _, err := store.LoadFeatureList(meta.ID); err == nil {
				t.Fatal("invalid feature list was persisted")
			}
		})
	}
}

func TestLoadFeatureListRejectsMalformedSnapshot(t *testing.T) {
	store := NewStore(t.TempDir())
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	body := []byte(`{
  "features": [
    {"id":"feature_0001","status":"pending"}
  ]
}`)
	if err := os.WriteFile(filepath.Join(store.SessionDir(meta.ID), "feature_list.json"), body, 0o600); err != nil {
		t.Fatalf("write malformed feature list: %v", err)
	}

	if _, err := store.LoadFeatureList(meta.ID); err == nil || !strings.Contains(err.Error(), "validate feature_list.json") || !strings.Contains(err.Error(), "description is required") {
		t.Fatalf("expected malformed feature_list.json error, got %v", err)
	}
}

func TestLoadContractAndArtifactTrackerRejectMalformedSnapshots(t *testing.T) {
	store := NewStore(t.TempDir())
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	contractPath := filepath.Join(store.SessionDir(meta.ID), "contract.json")
	contract := SessionContract{
		SchemaVersion: 1,
		ContractID:    "contract_" + meta.ID,
		Source:        "user_instruction",
		TrustSource:   "explicit_user",
		Profile:       "default",
		RequiredArtifacts: []RequiredArtifact{{
			Path:     "reports/final.md",
			Required: true,
		}},
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	data, err := json.Marshal(contract)
	if err != nil {
		t.Fatalf("marshal contract: %v", err)
	}
	if err := os.WriteFile(contractPath, data, 0o600); err != nil {
		t.Fatalf("write malformed contract: %v", err)
	}

	if _, err := store.LoadContract(meta.ID); err == nil || !strings.Contains(err.Error(), "validate contract.json") || !strings.Contains(err.Error(), "absolute path") {
		t.Fatalf("expected malformed contract error, got %v", err)
	}

	artifactTrackerPath := filepath.Join(store.SessionDir(meta.ID), "artifact-tracker.json")
	tracker := []RequiredArtifact{{
		Path:     "reports/final.md",
		Required: true,
	}}
	data, err = json.Marshal(tracker)
	if err != nil {
		t.Fatalf("marshal artifact tracker: %v", err)
	}
	if err := os.WriteFile(artifactTrackerPath, data, 0o600); err != nil {
		t.Fatalf("write malformed artifact tracker: %v", err)
	}
	if _, err := store.LoadArtifactTracker(meta.ID); err == nil || !strings.Contains(err.Error(), "validate artifact-tracker.json") || !strings.Contains(err.Error(), "absolute path") {
		t.Fatalf("expected malformed artifact tracker error, got %v", err)
	}
}

func TestSnapshotContractRefreshRejectsMalformedHistory(t *testing.T) {
	store := NewStore(t.TempDir())
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	valid := SessionContract{
		SchemaVersion: 1,
		ContractID:    "contract_" + meta.ID,
		Source:        "user_instruction",
		TrustSource:   "explicit_user",
		Profile:       "default",
		CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.SaveContract(meta.ID, valid); err != nil {
		t.Fatalf("save contract: %v", err)
	}
	if err := store.AppendContractHistory(meta.ID, valid); err != nil {
		t.Fatalf("append contract history: %v", err)
	}
	malformed := valid
	malformed.Profile = ""
	data, err := json.Marshal(malformed)
	if err != nil {
		t.Fatalf("marshal malformed history: %v", err)
	}
	historyPath := filepath.Join(store.SessionDir(meta.ID), "artifacts", "contract-history.jsonl")
	if err := os.WriteFile(historyPath, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write malformed history: %v", err)
	}

	if _, err := store.SnapshotContractRefresh(meta.ID); err == nil || !strings.Contains(err.Error(), "validate contract history snapshot") || !strings.Contains(err.Error(), "contract profile is required") {
		t.Fatalf("expected malformed contract history snapshot error, got %v", err)
	}
}

func TestAppendContractHistoryRejectsMalformedExistingHistory(t *testing.T) {
	store := NewStore(t.TempDir())
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	valid := SessionContract{
		SchemaVersion: 1,
		ContractID:    "contract_" + meta.ID,
		Source:        "user_instruction",
		TrustSource:   "explicit_user",
		Profile:       "default",
		CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.AppendContractHistory(meta.ID, valid); err != nil {
		t.Fatalf("append valid contract history: %v", err)
	}
	malformed := valid
	malformed.Profile = ""
	data, err := json.Marshal(malformed)
	if err != nil {
		t.Fatalf("marshal malformed history: %v", err)
	}
	historyPath := filepath.Join(store.SessionDir(meta.ID), "artifacts", "contract-history.jsonl")
	if err := os.WriteFile(historyPath, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write malformed history: %v", err)
	}

	if err := store.AppendContractHistory(meta.ID, valid); err == nil || !strings.Contains(err.Error(), "validate contract-history.jsonl") || !strings.Contains(err.Error(), "contract profile is required") {
		t.Fatalf("expected malformed existing contract history append error, got %v", err)
	}
	history, err := store.LoadContractHistory(meta.ID)
	if err == nil || !strings.Contains(err.Error(), "contract profile is required") {
		t.Fatalf("expected preserved malformed history load error, got history=%#v err=%v", history, err)
	}
}

func TestContractArtifactsRejectMalformedTimestamps(t *testing.T) {
	store := NewStore(t.TempDir())
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	artifactPath := filepath.Join(meta.Workdir, "reports", "final.md")
	validArtifact := RequiredArtifact{
		Path:     artifactPath,
		Required: true,
		Status: ArtifactStatus{
			UpdatedAt: now,
		},
	}
	validContract := SessionContract{
		SchemaVersion:     1,
		ContractID:        "contract_" + meta.ID,
		Source:            "user_instruction",
		TrustSource:       "explicit_user",
		Profile:           "default",
		RequiredArtifacts: []RequiredArtifact{validArtifact},
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	contractPath := filepath.Join(store.SessionDir(meta.ID), "contract.json")
	artifactTrackerPath := filepath.Join(store.SessionDir(meta.ID), "artifact-tracker.json")
	historyPath := filepath.Join(store.SessionDir(meta.ID), "artifacts", "contract-history.jsonl")

	malformedCreatedAt := validContract
	malformedCreatedAt.CreatedAt = "not-a-time"
	data, err := json.Marshal(malformedCreatedAt)
	if err != nil {
		t.Fatalf("marshal contract: %v", err)
	}
	if err := os.WriteFile(contractPath, data, 0o600); err != nil {
		t.Fatalf("write malformed contract: %v", err)
	}
	if _, err := store.LoadContract(meta.ID); err == nil || !strings.Contains(err.Error(), "validate contract.json") || !strings.Contains(err.Error(), "created_at must be RFC3339Nano") {
		t.Fatalf("expected invalid contract created_at error, got %v", err)
	}
	if err := store.SaveContract(meta.ID, malformedCreatedAt); err == nil || !strings.Contains(err.Error(), "created_at must be RFC3339Nano") {
		t.Fatalf("expected save to reject invalid contract created_at, got %v", err)
	}

	malformedUpdatedAt := validContract
	malformedUpdatedAt.UpdatedAt = "not-a-time"
	data, err = json.Marshal(malformedUpdatedAt)
	if err != nil {
		t.Fatalf("marshal contract: %v", err)
	}
	if err := os.WriteFile(contractPath, data, 0o600); err != nil {
		t.Fatalf("write malformed contract: %v", err)
	}
	if _, err := store.SnapshotContractRefresh(meta.ID); err == nil || !strings.Contains(err.Error(), "validate contract snapshot") || !strings.Contains(err.Error(), "updated_at must be RFC3339Nano") {
		t.Fatalf("expected invalid contract snapshot updated_at error, got %v", err)
	}

	data, err = json.Marshal(malformedUpdatedAt)
	if err != nil {
		t.Fatalf("marshal contract history: %v", err)
	}
	if err := os.WriteFile(historyPath, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write malformed contract history: %v", err)
	}
	if _, err := store.LoadContractHistory(meta.ID); err == nil || !strings.Contains(err.Error(), "updated_at must be RFC3339Nano") {
		t.Fatalf("expected invalid contract history updated_at error, got %v", err)
	}
	if err := store.AppendContractHistory(meta.ID, malformedUpdatedAt); err == nil || !strings.Contains(err.Error(), "updated_at must be RFC3339Nano") {
		t.Fatalf("expected append to reject invalid contract history timestamp, got %v", err)
	}

	malformedArtifact := validArtifact
	malformedArtifact.Status.UpdatedAt = "not-a-time"
	data, err = json.Marshal([]RequiredArtifact{malformedArtifact})
	if err != nil {
		t.Fatalf("marshal artifact tracker: %v", err)
	}
	if err := os.WriteFile(artifactTrackerPath, data, 0o600); err != nil {
		t.Fatalf("write malformed artifact tracker: %v", err)
	}
	if _, err := store.LoadArtifactTracker(meta.ID); err == nil || !strings.Contains(err.Error(), "validate artifact-tracker.json") || !strings.Contains(err.Error(), "status.updated_at must be RFC3339Nano") {
		t.Fatalf("expected invalid artifact status updated_at error, got %v", err)
	}
	if err := store.SaveArtifactTracker(meta.ID, []RequiredArtifact{malformedArtifact}); err == nil || !strings.Contains(err.Error(), "status.updated_at must be RFC3339Nano") {
		t.Fatalf("expected save to reject invalid artifact status updated_at, got %v", err)
	}

	malformedBaseline := validArtifact
	malformedBaseline.Baseline = ArtifactSnapshot{
		Exists: true,
		MTime:  "not-a-time",
	}
	validContract.RequiredArtifacts = []RequiredArtifact{malformedBaseline}
	data, err = json.Marshal(validContract)
	if err != nil {
		t.Fatalf("marshal contract with malformed baseline: %v", err)
	}
	if err := os.WriteFile(contractPath, data, 0o600); err != nil {
		t.Fatalf("write malformed baseline contract: %v", err)
	}
	if _, err := store.LoadContract(meta.ID); err == nil || !strings.Contains(err.Error(), "validate contract.json") || !strings.Contains(err.Error(), "baseline.mtime must be RFC3339Nano") {
		t.Fatalf("expected invalid contract artifact baseline mtime error, got %v", err)
	}
	data, err = json.Marshal([]RequiredArtifact{malformedBaseline})
	if err != nil {
		t.Fatalf("marshal artifact tracker with malformed baseline: %v", err)
	}
	if err := os.WriteFile(artifactTrackerPath, data, 0o600); err != nil {
		t.Fatalf("write malformed baseline artifact tracker: %v", err)
	}
	if _, err := store.LoadArtifactTracker(meta.ID); err == nil || !strings.Contains(err.Error(), "validate artifact-tracker.json") || !strings.Contains(err.Error(), "baseline.mtime must be RFC3339Nano") {
		t.Fatalf("expected invalid artifact tracker baseline mtime error, got %v", err)
	}
	if err := store.SaveArtifactTracker(meta.ID, []RequiredArtifact{malformedBaseline}); err == nil || !strings.Contains(err.Error(), "baseline.mtime must be RFC3339Nano") {
		t.Fatalf("expected save to reject invalid artifact baseline mtime, got %v", err)
	}
}

func TestStoreWriteTranscriptIgnoresPredictableTempSymlink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	transcriptPath := filepath.Join(store.SessionDir(meta.ID), "artifacts", "transcripts", "audit.jsonl")
	if err := os.MkdirAll(filepath.Dir(transcriptPath), 0o700); err != nil {
		t.Fatalf("mkdir transcript dir: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.tmp")
	if err := os.WriteFile(outside, []byte("keep\n"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := os.Symlink(outside, transcriptPath+".tmp"); err != nil {
		t.Fatalf("symlink predictable tmp: %v", err)
	}

	written, err := store.WriteTranscript(meta.ID, "audit.jsonl", []Message{NewMessage("user", "hello")})
	if err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	if written != transcriptPath {
		t.Fatalf("unexpected transcript path: %s", written)
	}
	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("read outside: %v", err)
	}
	if string(data) != "keep\n" {
		t.Fatalf("outside predictable tmp symlink target was modified: %q", string(data))
	}
	if info, err := os.Lstat(transcriptPath); err != nil {
		t.Fatalf("stat transcript: %v", err)
	} else if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("transcript path should not be a symlink")
	}
}

func TestStoreSaveStateIgnoresPredictableTempSymlink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	statePath := filepath.Join(store.SessionDir(meta.ID), "state.json")
	outside := filepath.Join(t.TempDir(), "outside-state.tmp")
	if err := os.WriteFile(outside, []byte("keep\n"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := os.Symlink(outside, statePath+".tmp"); err != nil {
		t.Fatalf("symlink predictable state tmp: %v", err)
	}

	if err := store.SaveState(meta.ID, State{Status: StatusAwaitingInput, Phase: "turn_decide"}); err != nil {
		t.Fatalf("save state: %v", err)
	}
	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("read outside: %v", err)
	}
	if string(data) != "keep\n" {
		t.Fatalf("outside predictable state tmp symlink target was modified: %q", string(data))
	}
	if info, err := os.Lstat(statePath); err != nil {
		t.Fatalf("stat state: %v", err)
	} else if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("state path should not be a symlink")
	}
}

func TestStoreLoadMetadataRejectsMalformedSnapshot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	malformed := meta
	malformed.ID = NewSessionID()
	sessionPath := filepath.Join(store.SessionDir(meta.ID), "session.json")
	if err := store.writeJSONFile(sessionPath, malformed); err != nil {
		t.Fatalf("write malformed metadata: %v", err)
	}
	if _, err := store.LoadMetadata(meta.ID); err == nil || !strings.Contains(err.Error(), "validate session.json") {
		t.Fatalf("expected malformed session.json validation error, got %v", err)
	}

	invalidCreatedAt := meta
	invalidCreatedAt.CreatedAt = "not-a-time"
	if err := store.writeJSONFile(sessionPath, invalidCreatedAt); err != nil {
		t.Fatalf("write invalid created_at metadata: %v", err)
	}
	if _, err := store.LoadMetadata(meta.ID); err == nil || !strings.Contains(err.Error(), "validate session.json") || !strings.Contains(err.Error(), "created_at must be RFC3339Nano") {
		t.Fatalf("expected invalid session created_at validation error, got %v", err)
	}

	invalidMode := meta
	invalidMode.Mode = "debug"
	if err := store.SaveMetadata(meta.ID, invalidMode); err == nil || !strings.Contains(err.Error(), "invalid session mode") {
		t.Fatalf("expected SaveMetadata to reject invalid mode, got %v", err)
	}

	invalidCreatedAt = meta
	invalidCreatedAt.CreatedAt = "not-a-time"
	if err := store.SaveMetadata(meta.ID, invalidCreatedAt); err == nil || !strings.Contains(err.Error(), "created_at must be RFC3339Nano") {
		t.Fatalf("expected SaveMetadata to reject invalid created_at, got %v", err)
	}
}

func TestStoreRejectsMalformedAgentRoleFacts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	sessionPath := filepath.Join(store.SessionDir(meta.ID), "session.json")

	invalidRole := meta
	invalidRole.AgentRole = "assistant"
	if err := store.SaveMetadata(meta.ID, invalidRole); err == nil || !strings.Contains(err.Error(), "invalid session agent_role") {
		t.Fatalf("expected SaveMetadata to reject invalid agent_role, got %v", err)
	}
	if err := store.writeJSONFile(sessionPath, invalidRole); err != nil {
		t.Fatalf("write invalid role metadata: %v", err)
	}
	if _, err := store.LoadMetadata(meta.ID); err == nil || !strings.Contains(err.Error(), "validate session.json") || !strings.Contains(err.Error(), "invalid session agent_role") {
		t.Fatalf("expected LoadMetadata to reject invalid agent_role, got %v", err)
	}

	for _, role := range []string{"", "planner", "generator", "evaluator"} {
		validRole := meta
		validRole.AgentRole = role
		if err := store.SaveMetadata(meta.ID, validRole); err != nil {
			t.Fatalf("save valid role %q: %v", role, err)
		}
		loaded, err := store.LoadMetadata(meta.ID)
		if err != nil {
			t.Fatalf("load valid role %q: %v", role, err)
		}
		if loaded.AgentRole != role {
			t.Fatalf("expected role %q to round trip, got %q", role, loaded.AgentRole)
		}
	}
}

func TestStoreLoadMetadataRejectsMalformedDelegationTopology(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	sessionPath := filepath.Join(store.SessionDir(meta.ID), "session.json")

	rootWithForeignRoot := meta
	rootWithForeignRoot.RootSessionID = NewSessionID()
	if err := store.writeJSONFile(sessionPath, rootWithForeignRoot); err != nil {
		t.Fatalf("write root with foreign root: %v", err)
	}
	if _, err := store.LoadMetadata(meta.ID); err == nil || !strings.Contains(err.Error(), "root_session_id") {
		t.Fatalf("expected LoadMetadata to reject root with foreign root_session_id, got %v", err)
	}
	if err := store.SaveMetadata(meta.ID, rootWithForeignRoot); err == nil || !strings.Contains(err.Error(), "root_session_id") {
		t.Fatalf("expected SaveMetadata to reject root with foreign root_session_id, got %v", err)
	}

	childWithoutRoot := meta
	childWithoutRoot.ParentSessionID = NewSessionID()
	childWithoutRoot.Depth = 1
	if err := store.writeJSONFile(sessionPath, childWithoutRoot); err != nil {
		t.Fatalf("write child without root: %v", err)
	}
	if _, err := store.LoadMetadata(meta.ID); err == nil || !strings.Contains(err.Error(), "root_session_id") {
		t.Fatalf("expected LoadMetadata to reject child without root_session_id, got %v", err)
	}
	if err := store.SaveMetadata(meta.ID, childWithoutRoot); err == nil || !strings.Contains(err.Error(), "root_session_id") {
		t.Fatalf("expected SaveMetadata to reject child without root_session_id, got %v", err)
	}

	childAtRootDepth := meta
	childAtRootDepth.ParentSessionID = NewSessionID()
	childAtRootDepth.RootSessionID = childAtRootDepth.ParentSessionID
	if err := store.writeJSONFile(sessionPath, childAtRootDepth); err != nil {
		t.Fatalf("write child at root depth: %v", err)
	}
	if _, err := store.LoadMetadata(meta.ID); err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("expected LoadMetadata to reject child with zero depth, got %v", err)
	}
	if err := store.SaveMetadata(meta.ID, childAtRootDepth); err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("expected SaveMetadata to reject child with zero depth, got %v", err)
	}

	rootWithSelfRoot := meta
	rootWithSelfRoot.RootSessionID = rootWithSelfRoot.ID
	if err := store.writeJSONFile(sessionPath, rootWithSelfRoot); err != nil {
		t.Fatalf("write root with self root: %v", err)
	}
	if _, err := store.LoadMetadata(meta.ID); err != nil {
		t.Fatalf("expected root with self root_session_id to load: %v", err)
	}

	legacyRoot := meta
	if err := store.writeJSONFile(sessionPath, legacyRoot); err != nil {
		t.Fatalf("write legacy root: %v", err)
	}
	if _, err := store.LoadMetadata(meta.ID); err != nil {
		t.Fatalf("expected legacy root without root_session_id to load: %v", err)
	}
}

func TestStoreLoadStateRejectsMalformedSnapshot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	malformed := state
	malformed.Status = "teleport"
	statePath := filepath.Join(store.SessionDir(meta.ID), "state.json")
	if err := store.writeJSONFile(statePath, malformed); err != nil {
		t.Fatalf("write malformed state: %v", err)
	}
	if _, err := store.LoadState(meta.ID); err == nil || !strings.Contains(err.Error(), "validate state.json") {
		t.Fatalf("expected malformed state.json validation error, got %v", err)
	}

	invalidUpdatedAt := state
	invalidUpdatedAt.UpdatedAt = "not-a-time"
	if err := store.writeJSONFile(statePath, invalidUpdatedAt); err != nil {
		t.Fatalf("write invalid updated_at state: %v", err)
	}
	if _, err := store.LoadState(meta.ID); err == nil || !strings.Contains(err.Error(), "validate state.json") || !strings.Contains(err.Error(), "updated_at must be RFC3339Nano") {
		t.Fatalf("expected invalid state updated_at validation error, got %v", err)
	}

	invalidCounter := state
	invalidCounter.PendingSteerCount = -1
	if err := store.SaveState(meta.ID, invalidCounter); err == nil || !strings.Contains(err.Error(), "pending_steer_count must be non-negative") {
		t.Fatalf("expected SaveState to reject invalid counter, got %v", err)
	}
}

func TestProviderAttemptsRejectMalformedFacts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	malformed := ProviderAttempt{
		Turn:       1,
		Attempt:    1,
		Provider:   "fake",
		Model:      "fake",
		Outcome:    "maybe",
		CreatedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		StatusCode: 200,
	}
	attemptsPath := filepath.Join(store.SessionDir(meta.ID), "provider-attempts.jsonl")
	data, err := json.Marshal(malformed)
	if err != nil {
		t.Fatalf("marshal malformed provider attempt: %v", err)
	}
	if err := os.WriteFile(attemptsPath, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write malformed provider attempt: %v", err)
	}
	if _, err := store.LoadProviderAttempts(meta.ID); err == nil || !strings.Contains(err.Error(), "validate provider-attempts.jsonl") || !strings.Contains(err.Error(), "invalid provider attempt outcome") {
		t.Fatalf("expected malformed provider attempts validation error, got %v", err)
	}

	if err := os.Remove(attemptsPath); err != nil {
		t.Fatalf("remove malformed provider attempts: %v", err)
	}
	invalidCreatedAt := malformed
	invalidCreatedAt.Outcome = "retry"
	invalidCreatedAt.CreatedAt = "not-a-time"
	data, err = json.Marshal(invalidCreatedAt)
	if err != nil {
		t.Fatalf("marshal invalid-time provider attempt: %v", err)
	}
	if err := os.WriteFile(attemptsPath, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write invalid-time provider attempt: %v", err)
	}
	if _, err := store.LoadProviderAttempts(meta.ID); err == nil || !strings.Contains(err.Error(), "validate provider-attempts.jsonl") || !strings.Contains(err.Error(), "created_at must be RFC3339Nano") {
		t.Fatalf("expected invalid provider-attempt timestamp validation error, got %v", err)
	}

	if err := os.Remove(attemptsPath); err != nil {
		t.Fatalf("remove invalid-time provider attempts: %v", err)
	}
	valid := ProviderAttempt{
		Turn:      1,
		Attempt:   1,
		Provider:  "fake",
		Model:     "fake",
		Outcome:   "retry",
		Error:     "temporary",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.AppendProviderAttempt(meta.ID, valid); err != nil {
		t.Fatalf("append valid provider attempt: %v", err)
	}
	invalidCreatedAt = valid
	invalidCreatedAt.Attempt = 2
	invalidCreatedAt.CreatedAt = "not-a-time"
	if err := store.AppendProviderAttempt(meta.ID, invalidCreatedAt); err == nil || !strings.Contains(err.Error(), "created_at must be RFC3339Nano") {
		t.Fatalf("expected append to reject invalid provider-attempt timestamp, got %v", err)
	}
	invalidCounter := valid
	invalidCounter.Outcome = "success"
	invalidCounter.Attempt = 2
	invalidCounter.CacheReadInputTokens = -1
	if err := store.AppendProviderAttempt(meta.ID, invalidCounter); err == nil || !strings.Contains(err.Error(), "cache_read_input_tokens must be non-negative") {
		t.Fatalf("expected append to reject negative cache counter, got %v", err)
	}
	loaded, err := store.LoadProviderAttempts(meta.ID)
	if err != nil {
		t.Fatalf("load provider attempts: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Outcome != "retry" || loaded[0].Attempt != 1 {
		t.Fatalf("malformed provider attempt write changed durable ledger: %#v", loaded)
	}
}

func TestStoreLoadStateRejectsSymlinkJSON(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	outside := filepath.Join(t.TempDir(), "outside-state.json")
	if err := os.WriteFile(outside, []byte(`{"status":"completed","phase":"outside"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	statePath := filepath.Join(store.SessionDir(meta.ID), "state.json")
	if err := os.Remove(statePath); err != nil {
		t.Fatalf("remove state: %v", err)
	}
	if err := os.Symlink(outside, statePath); err != nil {
		t.Fatalf("symlink state: %v", err)
	}

	if _, err := store.LoadState(meta.ID); err == nil {
		t.Fatal("expected symlinked state.json read to fail")
	}
}

func TestStoreLoadStateRejectsSymlinkSessionDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "state.json"), []byte(`{"status":"completed","phase":"outside"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write outside state: %v", err)
	}
	sessionDir := store.SessionDir(meta.ID)
	if err := os.RemoveAll(sessionDir); err != nil {
		t.Fatalf("remove session dir: %v", err)
	}
	if err := os.Symlink(outside, sessionDir); err != nil {
		t.Fatalf("symlink session dir: %v", err)
	}

	if _, err := store.LoadState(meta.ID); err == nil {
		t.Fatal("expected symlinked session dir read to fail")
	}
}

func TestStoreLoadStateRejectsOversizedJSON(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	statePath := filepath.Join(store.SessionDir(meta.ID), "state.json")
	if err := os.Truncate(statePath, fileutil.MaxRegularFileReadBytes+1); err != nil {
		t.Fatalf("truncate state: %v", err)
	}
	if _, err := store.LoadState(meta.ID); err == nil || !strings.Contains(err.Error(), "exceeds maximum readable size") {
		t.Fatalf("expected oversized state.json read to fail, got %v", err)
	}
}

func TestStoreLoadMessagesRejectsSymlinkJSONL(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	outsideMessage, err := json.Marshal(NewMessage("user", "outside"))
	if err != nil {
		t.Fatalf("marshal outside message: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside-messages.jsonl")
	if err := os.WriteFile(outside, append(outsideMessage, '\n'), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	messagePath := filepath.Join(store.SessionDir(meta.ID), "messages.jsonl")
	if err := os.Remove(messagePath); err != nil {
		t.Fatalf("remove messages: %v", err)
	}
	if err := os.Symlink(outside, messagePath); err != nil {
		t.Fatalf("symlink messages: %v", err)
	}

	if _, err := store.LoadMessages(meta.ID); err == nil {
		t.Fatal("expected symlinked messages.jsonl read to fail")
	}
}

func TestStoreLoadMessagesRejectsOversizedJSONLRecord(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	messagePath := filepath.Join(store.SessionDir(meta.ID), "messages.jsonl")
	if err := os.Truncate(messagePath, fileutil.MaxRegularFileReadBytes+1); err != nil {
		t.Fatalf("truncate messages: %v", err)
	}
	if _, err := store.LoadMessages(meta.ID); err == nil || !strings.Contains(err.Error(), "session JSONL record exceeds maximum readable size") {
		t.Fatalf("expected oversized JSONL record read to fail, got %v", err)
	}
}

func TestLoadMessagesRejectsMalformedSnapshot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	malformed := NewMessage("assistant", "I will use a tool.")
	malformed.ToolCalls = []ToolCall{{
		ID:        "call_1",
		Name:      "shell",
		Arguments: json.RawMessage(`[]`),
	}}
	path := filepath.Join(store.SessionDir(meta.ID), "messages.jsonl")
	if err := store.writeJSONL(path, []Message{malformed}); err != nil {
		t.Fatalf("write malformed messages: %v", err)
	}
	if _, err := store.LoadMessages(meta.ID); err == nil || !strings.Contains(err.Error(), "validate messages.jsonl") || !strings.Contains(err.Error(), "arguments must be valid JSON object") {
		t.Fatalf("expected malformed messages validation error, got %v", err)
	}

	malformed = NewMessage("user", "bad time")
	malformed.CreatedAt = "not-a-time"
	if err := store.writeJSONL(path, []Message{malformed}); err != nil {
		t.Fatalf("write invalid-time messages: %v", err)
	}
	if _, err := store.LoadMessages(meta.ID); err == nil || !strings.Contains(err.Error(), "validate messages.jsonl") || !strings.Contains(err.Error(), "created_at must be RFC3339Nano") {
		t.Fatalf("expected invalid message created_at validation error, got %v", err)
	}

	malformed = NewMessage("assistant", "provider replay")
	malformed.ProviderContentBlocks = []ProviderContentBlock{{
		Provider: "anthropic",
		Type:     "tool_use",
		ID:       "toolu_1",
		Name:     "shell",
		Input:    json.RawMessage(`[]`),
	}}
	if err := store.writeJSONL(path, []Message{malformed}); err != nil {
		t.Fatalf("write invalid provider input messages: %v", err)
	}
	if _, err := store.LoadMessages(meta.ID); err == nil || !strings.Contains(err.Error(), "validate messages.jsonl") || !strings.Contains(err.Error(), "input must be valid JSON object") {
		t.Fatalf("expected invalid provider content input validation error, got %v", err)
	}

	malformed = NewMessage("assistant", "provider replay")
	malformed.ProviderContentBlocks = []ProviderContentBlock{{
		Provider: "google",
		Type:     "function_call",
		ID:       "call_shell",
		Name:     "shell",
		Args:     json.RawMessage(`[]`),
	}}
	if err := store.writeJSONL(path, []Message{malformed}); err != nil {
		t.Fatalf("write invalid provider args messages: %v", err)
	}
	if _, err := store.LoadMessages(meta.ID); err == nil || !strings.Contains(err.Error(), "validate messages.jsonl") || !strings.Contains(err.Error(), "args must be valid JSON object") {
		t.Fatalf("expected invalid provider content args validation error, got %v", err)
	}

	malformed = NewMessage("assistant", "")
	malformed.ProviderContentBlocks = []ProviderContentBlock{{
		Type: "thinking",
		Data: "opaque",
	}}
	if err := store.writeJSONL(path, []Message{malformed}); err != nil {
		t.Fatalf("write ownerless provider block messages: %v", err)
	}
	if _, err := store.LoadMessages(meta.ID); err == nil || !strings.Contains(err.Error(), "validate messages.jsonl") || !strings.Contains(err.Error(), "provider is required") {
		t.Fatalf("expected ownerless provider content validation error, got %v", err)
	}

	malformed = NewMessage("assistant", "")
	malformed.ProviderContentBlocks = []ProviderContentBlock{{
		Provider: "unknown-provider",
		Type:     "thinking",
		Data:     "opaque",
	}}
	if err := store.writeJSONL(path, []Message{malformed}); err != nil {
		t.Fatalf("write unknown provider block messages: %v", err)
	}
	if _, err := store.LoadMessages(meta.ID); err == nil || !strings.Contains(err.Error(), "validate messages.jsonl") || !strings.Contains(err.Error(), "invalid provider") {
		t.Fatalf("expected unknown provider content validation error, got %v", err)
	}

	malformed = NewMessage("assistant", "")
	malformed.ProviderContentBlocks = []ProviderContentBlock{{
		Provider: " openai ",
		Type:     "reasoning",
		ID:       "rs_1",
		Data:     "opaque",
	}}
	if err := store.writeJSONL(path, []Message{malformed}); err != nil {
		t.Fatalf("write spaced provider block messages: %v", err)
	}
	if _, err := store.LoadMessages(meta.ID); err == nil || !strings.Contains(err.Error(), "validate messages.jsonl") || !strings.Contains(err.Error(), "invalid provider") {
		t.Fatalf("expected spaced provider content validation error, got %v", err)
	}
}

func TestMessageWritesRejectMalformedFacts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	valid := NewMessage("user", "hello")
	if err := store.AppendMessage(meta.ID, valid); err != nil {
		t.Fatalf("append valid message: %v", err)
	}

	invalidRole := NewMessage("developer", "not a supported role")
	if err := store.AppendMessage(meta.ID, invalidRole); err == nil || !strings.Contains(err.Error(), "validate messages.jsonl") || !strings.Contains(err.Error(), "invalid message role") {
		t.Fatalf("expected invalid role append rejection, got %v", err)
	}
	invalidCreatedAt := NewMessage("user", "bad time")
	invalidCreatedAt.CreatedAt = "not-a-time"
	if err := store.AppendMessage(meta.ID, invalidCreatedAt); err == nil || !strings.Contains(err.Error(), "validate messages.jsonl") || !strings.Contains(err.Error(), "created_at must be RFC3339Nano") {
		t.Fatalf("expected invalid created_at append rejection, got %v", err)
	}
	emptyTool := NewToolMessage(nil)
	if err := store.AppendMessage(meta.ID, emptyTool); err == nil || !strings.Contains(err.Error(), "tool message must contain tool_results") {
		t.Fatalf("expected empty tool message append rejection, got %v", err)
	}
	invalidProviderInput := NewMessage("assistant", "provider replay")
	invalidProviderInput.ProviderContentBlocks = []ProviderContentBlock{{
		Provider: "anthropic",
		Type:     "tool_use",
		ID:       "toolu_1",
		Name:     "shell",
		Input:    json.RawMessage(`[]`),
	}}
	if err := store.AppendMessage(meta.ID, invalidProviderInput); err == nil || !strings.Contains(err.Error(), "validate messages.jsonl") || !strings.Contains(err.Error(), "input must be valid JSON object") {
		t.Fatalf("expected invalid provider input append rejection, got %v", err)
	}
	invalidProviderArgs := NewMessage("assistant", "provider replay")
	invalidProviderArgs.ProviderContentBlocks = []ProviderContentBlock{{
		Provider: "google",
		Type:     "function_call",
		ID:       "call_shell",
		Name:     "shell",
		Args:     json.RawMessage(`[]`),
	}}
	if err := store.AppendMessage(meta.ID, invalidProviderArgs); err == nil || !strings.Contains(err.Error(), "validate messages.jsonl") || !strings.Contains(err.Error(), "args must be valid JSON object") {
		t.Fatalf("expected invalid provider args append rejection, got %v", err)
	}
	ownerlessProviderBlock := NewMessage("assistant", "")
	ownerlessProviderBlock.ProviderContentBlocks = []ProviderContentBlock{{
		Type: "thinking",
		Data: "opaque",
	}}
	if err := store.AppendMessage(meta.ID, ownerlessProviderBlock); err == nil || !strings.Contains(err.Error(), "validate messages.jsonl") || !strings.Contains(err.Error(), "provider is required") {
		t.Fatalf("expected ownerless provider block append rejection, got %v", err)
	}
	unknownProviderBlock := NewMessage("assistant", "")
	unknownProviderBlock.ProviderContentBlocks = []ProviderContentBlock{{
		Provider: "unknown-provider",
		Type:     "thinking",
		Data:     "opaque",
	}}
	if err := store.AppendMessage(meta.ID, unknownProviderBlock); err == nil || !strings.Contains(err.Error(), "validate messages.jsonl") || !strings.Contains(err.Error(), "invalid provider") {
		t.Fatalf("expected unknown provider block append rejection, got %v", err)
	}
	spacedProviderBlock := NewMessage("assistant", "")
	spacedProviderBlock.ProviderContentBlocks = []ProviderContentBlock{{
		Provider: " openai ",
		Type:     "reasoning",
		ID:       "rs_1",
		Data:     "opaque",
	}}
	if err := store.AppendMessage(meta.ID, spacedProviderBlock); err == nil || !strings.Contains(err.Error(), "validate messages.jsonl") || !strings.Contains(err.Error(), "invalid provider") {
		t.Fatalf("expected spaced provider block append rejection, got %v", err)
	}
	if _, err := store.WriteTranscript(meta.ID, "bad.jsonl", []Message{invalidRole}); err == nil || !strings.Contains(err.Error(), "validate transcript messages") {
		t.Fatalf("expected transcript validation rejection, got %v", err)
	}

	loaded, err := store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	if len(loaded) != 1 || loaded[0].ID != valid.ID {
		t.Fatalf("malformed message writes changed durable log: %#v", loaded)
	}
}

func TestAppendMessageRejectsMalformedExistingLog(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	valid := NewMessage("user", "hello")
	if err := store.AppendMessage(meta.ID, valid); err != nil {
		t.Fatalf("append valid message: %v", err)
	}
	malformed := NewMessage("developer", "not a supported role")
	path := filepath.Join(store.SessionDir(meta.ID), "messages.jsonl")
	if err := store.writeJSONL(path, []Message{malformed}); err != nil {
		t.Fatalf("write malformed messages: %v", err)
	}

	if err := store.AppendMessage(meta.ID, NewMessage("user", "later")); err == nil || !strings.Contains(err.Error(), "validate messages.jsonl") || !strings.Contains(err.Error(), "invalid message role") {
		t.Fatalf("expected malformed existing message log append error, got %v", err)
	}
	loaded, err := store.LoadMessages(meta.ID)
	if err == nil || !strings.Contains(err.Error(), "invalid message role") {
		t.Fatalf("expected preserved malformed message load error, got messages=%#v err=%v", loaded, err)
	}
}

func TestStoreProviderRawSidecarRoundTrip(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	sidecar := ProviderRawSidecar{
		Provider:           "openai",
		Model:              "gpt-test",
		Turn:               2,
		ProviderResponseID: "resp_2",
		StopReason:         "done_candidate",
		SelectedRawItems: map[string]any{
			"status": "completed",
		},
	}
	if err := store.SaveProviderRawSidecar(meta.ID, sidecar); err != nil {
		t.Fatalf("save sidecar: %v", err)
	}
	loaded, err := store.LoadProviderRawSidecar(meta.ID, 2)
	if err != nil {
		t.Fatalf("load sidecar: %v", err)
	}
	if loaded.SchemaVersion != 1 || strings.TrimSpace(loaded.Timestamp) == "" {
		t.Fatalf("expected default schema version and timestamp, got %#v", loaded)
	}
	if loaded.Provider != "openai" || loaded.Model != "gpt-test" || loaded.ProviderResponseID != "resp_2" || loaded.StopReason != "done_candidate" {
		t.Fatalf("unexpected sidecar: %#v", loaded)
	}
	if loaded.SelectedRawItems["status"] != "completed" {
		t.Fatalf("unexpected raw items: %#v", loaded.SelectedRawItems)
	}
	if filepath.Base(store.ProviderRawSidecarPath(meta.ID, 2)) != "2.json" {
		t.Fatalf("unexpected sidecar path: %s", store.ProviderRawSidecarPath(meta.ID, 2))
	}
}

func TestProviderRawSidecarRejectsMalformedSnapshot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	malformed := ProviderRawSidecar{
		SchemaVersion: 1,
		Provider:      "openai",
		Model:         "gpt-test",
		Turn:          99,
		Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
		StopReason:    "definitely_not_a_runtime_stop_reason",
	}
	path := store.ProviderRawSidecarPath(meta.ID, 2)
	if err := store.writeJSONFile(path, malformed); err != nil {
		t.Fatalf("write malformed sidecar: %v", err)
	}
	if _, err := store.LoadProviderRawSidecar(meta.ID, 2); err == nil || !strings.Contains(err.Error(), "validate provider-raw/2.json") || !strings.Contains(err.Error(), "does not match requested turn") {
		t.Fatalf("expected malformed provider raw sidecar validation error, got %v", err)
	}
}

func TestProviderRawSidecarWritesRejectMalformedFacts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	valid := ProviderRawSidecar{
		Provider:   "openai",
		Model:      "gpt-test",
		Turn:       2,
		Timestamp:  time.Now().UTC().Format(time.RFC3339Nano),
		StopReason: "done_candidate",
		SelectedRawItems: map[string]any{
			"status": "completed",
		},
	}
	if err := store.SaveProviderRawSidecar(meta.ID, valid); err != nil {
		t.Fatalf("save valid sidecar: %v", err)
	}

	invalid := valid
	invalid.Provider = " "
	invalid.Timestamp = time.Now().UTC().Add(time.Second).Format(time.RFC3339Nano)
	if err := store.SaveProviderRawSidecar(meta.ID, invalid); err == nil || !strings.Contains(err.Error(), "provider raw sidecar provider is required") {
		t.Fatalf("expected provider raw sidecar write validation rejection, got %v", err)
	}

	loaded, err := store.LoadProviderRawSidecar(meta.ID, 2)
	if err != nil {
		t.Fatalf("load preserved sidecar: %v", err)
	}
	if loaded.Provider != "openai" || loaded.Model != "gpt-test" || loaded.StopReason != "done_candidate" {
		t.Fatalf("malformed provider raw sidecar write changed durable fact: %#v", loaded)
	}
}

func TestProviderRawSidecarRejectsInvalidRequestedTurn(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.LoadProviderRawSidecar(meta.ID, 0); err == nil || !strings.Contains(err.Error(), "provider raw sidecar turn must be positive") {
		t.Fatalf("expected requested turn validation error, got %v", err)
	}
}

func TestLongRunCheckpointRejectsMalformedSnapshot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	malformed := LongRunCheckpoint{
		SchemaVersion:           1,
		SessionID:               meta.ID,
		RootSessionID:           meta.ID,
		Provider:                "fake",
		Model:                   "fake",
		Workdir:                 meta.Workdir,
		UnresolvedChildSessions: []string{"../child"},
		CreatedAt:               time.Now().UTC().Format(time.RFC3339Nano),
	}
	path := filepath.Join(store.SessionDir(meta.ID), "checkpoints", "longrun-latest.json")
	if err := store.writeJSONFile(path, malformed); err != nil {
		t.Fatalf("write malformed checkpoint: %v", err)
	}
	if _, err := store.LoadLongRunCheckpoint(meta.ID); err == nil || !strings.Contains(err.Error(), "validate longrun-latest.json") || !strings.Contains(err.Error(), "path separators") {
		t.Fatalf("expected malformed checkpoint validation error, got %v", err)
	}

	malformed.CreatedAt = "not-a-time"
	malformed.UnresolvedChildSessions = nil
	if err := store.writeJSONFile(path, malformed); err != nil {
		t.Fatalf("write malformed checkpoint timestamp: %v", err)
	}
	if _, err := store.LoadLongRunCheckpoint(meta.ID); err == nil || !strings.Contains(err.Error(), "validate longrun-latest.json") || !strings.Contains(err.Error(), "created_at must be RFC3339Nano") {
		t.Fatalf("expected malformed checkpoint created_at validation error, got %v", err)
	}

	malformed.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	malformed.RecentOwner = &ProcessOwnerClue{
		PID:         123,
		StartedAt:   "not-a-time",
		ReleasedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		LastEventAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.writeJSONFile(path, malformed); err != nil {
		t.Fatalf("write malformed checkpoint recent owner: %v", err)
	}
	if _, err := store.LoadLongRunCheckpoint(meta.ID); err == nil || !strings.Contains(err.Error(), "validate longrun-latest.json") || !strings.Contains(err.Error(), "recent_owner") || !strings.Contains(err.Error(), "started_at must be RFC3339Nano") {
		t.Fatalf("expected malformed checkpoint recent_owner started_at validation error, got %v", err)
	}
}

func TestLongRunCheckpointWritesRejectMalformedSnapshots(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	valid := LongRunCheckpoint{
		SchemaVersion:      1,
		SessionID:          meta.ID,
		RootSessionID:      meta.ID,
		Provider:           "fake",
		Model:              "fake",
		Workdir:            meta.Workdir,
		TaskSummary:        map[string]int{"total": 1, "ready": 1},
		SourceEventCount:   1,
		SourceMessageCount: 1,
		CreatedAt:          time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.SaveLongRunCheckpoint(meta.ID, valid); err != nil {
		t.Fatalf("save valid checkpoint: %v", err)
	}

	invalidSession := valid
	invalidSession.SessionID = NewSessionID()
	if err := store.SaveLongRunCheckpoint(meta.ID, invalidSession); err == nil || !strings.Contains(err.Error(), "does not match session") {
		t.Fatalf("expected save to reject mismatched session id, got %v", err)
	}
	invalidCounter := valid
	invalidCounter.TaskSummary = map[string]int{"total": -1}
	if err := store.SaveLongRunCheckpoint(meta.ID, invalidCounter); err == nil || !strings.Contains(err.Error(), "task_summary") {
		t.Fatalf("expected save to reject negative task summary, got %v", err)
	}
	invalidCreatedAt := valid
	invalidCreatedAt.CreatedAt = "not-a-time"
	if err := store.SaveLongRunCheckpoint(meta.ID, invalidCreatedAt); err == nil || !strings.Contains(err.Error(), "created_at must be RFC3339Nano") {
		t.Fatalf("expected save to reject malformed created_at, got %v", err)
	}
	invalidRecentOwner := valid
	invalidRecentOwner.RecentOwner = &ProcessOwnerClue{
		PID:         123,
		StartedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		ReleasedAt:  "not-a-time",
		LastEventAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.SaveLongRunCheckpoint(meta.ID, invalidRecentOwner); err == nil || !strings.Contains(err.Error(), "released_at must be RFC3339Nano") {
		t.Fatalf("expected save to reject malformed recent_owner released_at, got %v", err)
	}
	loaded, err := store.LoadLongRunCheckpoint(meta.ID)
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if loaded.SessionID != meta.ID || loaded.TaskSummary["total"] != 1 {
		t.Fatalf("malformed checkpoint write changed durable snapshot: %#v", loaded)
	}
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func hasPlanModeHistoryType(items []PlanModeHistoryEntry, target string) bool {
	for _, item := range items {
		if item.Type == target {
			return true
		}
	}
	return false
}

func hasGoalHistoryType(items []GoalHistoryEntry, target string) bool {
	for _, item := range items {
		if item.Type == target {
			return true
		}
	}
	return false
}

func blockGoalHistoryPath(t *testing.T, store *Store, sessionID string) {
	t.Helper()
	historyPath := filepath.Join(store.SessionDir(sessionID), "artifacts", "goal-history.jsonl")
	if err := os.Remove(historyPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove goal history: %v", err)
	}
	if err := os.Mkdir(historyPath, 0o700); err != nil {
		t.Fatalf("block goal history path: %v", err)
	}
}

func TestGoalHistoryRejectsMalformedTimestamps(t *testing.T) {
	store := NewStore(t.TempDir())
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
	goal, err := store.CreateGoal(meta.ID, GoalDraft{
		Enabled:   true,
		Objective: "Reject malformed goal history timestamps",
		Source:    GoalSourceCLI,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	valid := GoalHistoryEntry{
		SchemaVersion: 1,
		ID:            NewGoalHistoryID(),
		SessionID:     meta.ID,
		GoalID:        goal.GoalID,
		Type:          "goal.created",
		Source:        GoalSourceCLI,
		Status:        GoalStatusActive,
		CreatedAt:     now,
	}
	malformed := valid
	malformed.ID = NewGoalHistoryID()
	malformed.CreatedAt = "not-a-time"
	historyPath := filepath.Join(store.SessionDir(meta.ID), "artifacts", "goal-history.jsonl")
	writeGoalHistoryEntriesForTest(t, store, historyPath, []GoalHistoryEntry{malformed})
	if _, err := store.LoadGoalHistory(meta.ID); err == nil || !strings.Contains(err.Error(), "validate goal-history.jsonl") || !strings.Contains(err.Error(), "created_at must be RFC3339Nano") {
		t.Fatalf("expected malformed goal history load error, got %v", err)
	}

	writeGoalHistoryEntriesForTest(t, store, historyPath, []GoalHistoryEntry{valid})
	if err := store.AppendGoalHistory(meta.ID, GoalHistoryEntry{
		Type:      "goal.updated",
		Source:    GoalSourceCLI,
		CreatedAt: "not-a-time",
	}); err == nil || !strings.Contains(err.Error(), "created_at must be RFC3339Nano") {
		t.Fatalf("expected malformed goal history append error, got %v", err)
	}
	if err := store.RestoreGoalHistory(meta.ID, []GoalHistoryEntry{valid, malformed}); err == nil || !strings.Contains(err.Error(), "created_at must be RFC3339Nano") {
		t.Fatalf("expected malformed goal history restore error, got %v", err)
	}
	history, err := store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load preserved valid goal history: %v", err)
	}
	if len(history) != 1 || history[0].ID != valid.ID {
		t.Fatalf("expected malformed writes to preserve valid history, got %#v", history)
	}
}

func TestAppendGoalHistoryRejectsMalformedExistingHistory(t *testing.T) {
	store := NewStore(t.TempDir())
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
	goal, err := store.CreateGoal(meta.ID, GoalDraft{
		Enabled:   true,
		Objective: "Reject extending corrupt goal history",
		Source:    GoalSourceCLI,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	valid := GoalHistoryEntry{
		SchemaVersion: 1,
		ID:            NewGoalHistoryID(),
		SessionID:     meta.ID,
		GoalID:        goal.GoalID,
		Type:          "goal.created",
		Source:        GoalSourceCLI,
		Status:        GoalStatusActive,
		CreatedAt:     now,
	}
	malformed := valid
	malformed.ID = NewGoalHistoryID()
	malformed.CreatedAt = "not-a-time"
	historyPath := filepath.Join(store.SessionDir(meta.ID), "artifacts", "goal-history.jsonl")
	writeGoalHistoryEntriesForTest(t, store, historyPath, []GoalHistoryEntry{malformed})

	err = store.AppendGoalHistory(meta.ID, GoalHistoryEntry{
		Type:      "goal.updated",
		Source:    GoalSourceSystem,
		Status:    GoalStatusActive,
		CreatedAt: now,
	})
	if err == nil || !strings.Contains(err.Error(), "validate goal-history.jsonl") || !strings.Contains(err.Error(), "created_at must be RFC3339Nano") {
		t.Fatalf("expected malformed existing goal history error, got %v", err)
	}
	raw, readErr := os.ReadFile(historyPath)
	if readErr != nil {
		t.Fatalf("read goal history: %v", readErr)
	}
	if got := strings.Count(string(raw), "\n"); got != 1 {
		t.Fatalf("malformed existing history should not be extended, got %d records: %q", got, string(raw))
	}
}

func writeGoalHistoryEntriesForTest(t *testing.T, store *Store, path string, entries []GoalHistoryEntry) {
	t.Helper()
	var data strings.Builder
	for _, entry := range entries {
		encoded, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("marshal goal history: %v", err)
		}
		data.Write(encoded)
		data.WriteByte('\n')
	}
	if err := store.writeBytesFile(path, []byte(data.String())); err != nil {
		t.Fatalf("write goal history: %v", err)
	}
}

func TestStoreGoalLifecycleAccountingAndSummary(t *testing.T) {
	store := NewStore(t.TempDir())
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	tokenBudget := int64(5)
	goal, err := store.CreateGoal(meta.ID, GoalDraft{
		Enabled:             true,
		Mode:                GoalModeMission,
		Objective:           "Converge the feature safely",
		TokenBudget:         &tokenBudget,
		SuccessCriteria:     []string{"tests pass"},
		ValidationPlan:      []string{"go test ./internal/session"},
		Features:            []string{"durable goal state"},
		Milestones:          []string{"first checkpoint"},
		CreateTasksFromPlan: true,
		Source:              GoalSourceCLI,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	if goal.Mode != GoalModeMission || goal.Mission == nil || len(goal.Mission.Features) != 1 || len(goal.Mission.Features[0].TaskIDs) != 1 {
		t.Fatalf("expected mission goal, got %#v", goal)
	}
	tasks, err := store.ListTasks(meta.ID)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Subject != "durable goal state" || !containsString(tasks[0].Labels, "mission") {
		t.Fatalf("expected mission feature task, got %#v", tasks)
	}
	loaded, err := store.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load goal: %v", err)
	}
	if loaded.GoalID != goal.GoalID || loaded.SuccessCriteria[0].Text != "tests pass" || loaded.Mission.Features[0].TaskIDs[0] != tasks[0].ID {
		t.Fatalf("unexpected loaded goal: %#v", loaded)
	}
	items, err := store.List(10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 || items[0].GoalStatus != GoalStatusActive || items[0].GoalMode != GoalModeMission {
		t.Fatalf("expected goal summary fields, got %#v", items)
	}
	updated, limited, err := store.UpdateGoalAccounting(meta.ID, GoalUsageDelta{TokensUsedDelta: 6, TimeUsedSecondsDelta: 2, SourceTurn: 1})
	if err != nil {
		t.Fatalf("update accounting: %v", err)
	}
	if !limited || updated.Status != GoalStatusBudgetLimited || updated.TokensUsed != 6 {
		t.Fatalf("expected budget limited accounting, got limited=%v goal=%#v", limited, updated)
	}
	history, err := store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load goal history: %v", err)
	}
	if len(history) < 3 {
		t.Fatalf("expected create/accounting/budget history, got %#v", history)
	}
	if cleared, err := store.ClearGoal(meta.ID); err != nil || !cleared {
		t.Fatalf("clear goal cleared=%v err=%v", cleared, err)
	}
	if _, err := store.LoadGoal(meta.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing goal after clear, got %v", err)
	}
}

func TestClearGoalRejectsSymlinkedSessionDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	sessionID := NewSessionID()
	outsideDir := t.TempDir()
	outsideGoal := filepath.Join(outsideDir, "goal.json")
	outsideData := []byte(`{"outside":true}` + "\n")
	if err := os.WriteFile(outsideGoal, outsideData, 0o600); err != nil {
		t.Fatalf("write outside goal: %v", err)
	}
	sessionLink := filepath.Join(root, sessionID)
	if err := os.Symlink(outsideDir, sessionLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	cleared, err := store.ClearGoal(sessionID)
	if err == nil {
		t.Fatal("expected symlinked session directory rejection")
	}
	if cleared {
		t.Fatal("symlinked session directory must not report goal cleared")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink path error, got %v", err)
	}
	data, readErr := os.ReadFile(outsideGoal)
	if readErr != nil {
		t.Fatalf("outside goal should not be removed: %v", readErr)
	}
	if string(data) != string(outsideData) {
		t.Fatalf("outside goal changed: %q", data)
	}
	if info, statErr := os.Lstat(sessionLink); statErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("session symlink should remain for diagnostics, info=%v err=%v", info, statErr)
	}
}

func TestStoreListReportsCorruptVisibleSummarySnapshots(t *testing.T) {
	cases := []struct {
		name string
		file string
	}{
		{name: "goal", file: "goal.json"},
		{name: "planmode", file: "planmode.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := NewStore(t.TempDir())
			parentID := "summary_snapshot_parent_" + tc.name
			cleanChildMeta := SessionMetadata{
				SchemaVersion:    1,
				ID:               "summary_snapshot_clean_child_" + tc.name,
				CreatedAt:        time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
				Workdir:          t.TempDir(),
				Mode:             ModeRun,
				Provider:         "fake",
				Model:            "fake",
				CompletionPolicy: CompletionPolicyInteractive,
				ParentSessionID:  parentID,
				RootSessionID:    parentID,
				Depth:            1,
			}
			cleanChildState := State{Status: StatusCompleted, Phase: "done", UpdatedAt: time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)}
			if err := store.Create(cleanChildMeta, cleanChildState); err != nil {
				t.Fatalf("create clean child session: %v", err)
			}
			newerMeta := SessionMetadata{
				SchemaVersion:    1,
				ID:               "summary_snapshot_newer_" + tc.name,
				CreatedAt:        time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano),
				Workdir:          t.TempDir(),
				Mode:             ModeRun,
				Provider:         "fake",
				Model:            "fake",
				CompletionPolicy: CompletionPolicyInteractive,
				RootSessionID:    "summary_snapshot_newer_" + tc.name,
			}
			newerState := State{Status: StatusCompleted, Phase: "done", UpdatedAt: time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)}
			if err := store.Create(newerMeta, newerState); err != nil {
				t.Fatalf("create newer session: %v", err)
			}
			meta := SessionMetadata{
				SchemaVersion:    1,
				ID:               NewSessionID(),
				CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
				Workdir:          t.TempDir(),
				Mode:             ModeRun,
				Provider:         "fake",
				Model:            "fake",
				CompletionPolicy: CompletionPolicyInteractive,
				ParentSessionID:  parentID,
				RootSessionID:    parentID,
				Depth:            1,
			}
			state := State{Status: StatusCompleted, Phase: "done", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
			if err := store.Create(meta, state); err != nil {
				t.Fatalf("create session: %v", err)
			}
			if err := os.WriteFile(filepath.Join(store.SessionDir(meta.ID), tc.file), []byte("{not-json}\n"), 0o600); err != nil {
				t.Fatalf("write invalid %s: %v", tc.file, err)
			}

			if _, err := store.List(1); err != nil {
				t.Fatalf("List should not read summary snapshots outside visible limit, got %v", err)
			}
			if _, _, err := store.ListPage(1, 0); err != nil {
				t.Fatalf("ListPage should not read summary snapshots outside visible page, got %v", err)
			}
			if _, err := store.ListChildren(parentID, 1); err != nil {
				t.Fatalf("ListChildren should not read summary snapshots outside visible limit, got %v", err)
			}

			if _, err := store.List(10); err == nil || !strings.Contains(err.Error(), tc.file) {
				t.Fatalf("expected List to report visible %s, got %v", tc.file, err)
			}
			if _, _, err := store.ListPage(10, 0); err == nil || !strings.Contains(err.Error(), tc.file) {
				t.Fatalf("expected ListPage to report visible %s, got %v", tc.file, err)
			}
			if _, err := store.ListChildren(parentID, 10); err == nil || !strings.Contains(err.Error(), tc.file) {
				t.Fatalf("expected ListChildren to report visible %s, got %v", tc.file, err)
			}
		})
	}
}

func TestLoadGoalRejectsMalformedStructuredSnapshot(t *testing.T) {
	store := NewStore(t.TempDir())
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
	goal, err := store.CreateGoal(meta.ID, GoalDraft{
		Enabled:   true,
		Objective: "Reject malformed loaded goal",
		Source:    GoalSourceCLI,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	goal.SuccessCriteria = []GoalCriterion{{
		ID:       "   ",
		Text:     "Loaded criteria must be well-formed.",
		Status:   "pending",
		Required: true,
	}}
	data, err := json.Marshal(goal)
	if err != nil {
		t.Fatalf("marshal malformed goal: %v", err)
	}
	goalPath := filepath.Join(store.SessionDir(meta.ID), "goal.json")
	if err := os.WriteFile(goalPath, data, 0o600); err != nil {
		t.Fatalf("write malformed goal: %v", err)
	}

	loaded, err := store.LoadGoal(meta.ID)
	if err == nil || !strings.Contains(err.Error(), "success criteria id is required") {
		t.Fatalf("expected malformed loaded goal error, got goal=%#v err=%v", loaded, err)
	}
}

func TestGoalSnapshotsRejectMalformedTimestamps(t *testing.T) {
	store := NewStore(t.TempDir())
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
	goal, err := store.CreateGoal(meta.ID, GoalDraft{
		Enabled:   true,
		Objective: "Reject malformed goal timestamps",
		Source:    GoalSourceCLI,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	goalPath := filepath.Join(store.SessionDir(meta.ID), "goal.json")

	invalidCreatedAt := goal
	invalidCreatedAt.CreatedAt = "not-a-time"
	data, err := json.Marshal(invalidCreatedAt)
	if err != nil {
		t.Fatalf("marshal invalid created_at goal: %v", err)
	}
	if err := os.WriteFile(goalPath, data, 0o600); err != nil {
		t.Fatalf("write invalid created_at goal: %v", err)
	}
	if _, err := store.LoadGoal(meta.ID); err == nil || !strings.Contains(err.Error(), "validate goal.json") || !strings.Contains(err.Error(), "created_at must be RFC3339Nano") {
		t.Fatalf("expected malformed goal created_at load error, got %v", err)
	}

	invalidUpdatedAt := goal
	invalidUpdatedAt.UpdatedAt = "not-a-time"
	data, err = json.Marshal(invalidUpdatedAt)
	if err != nil {
		t.Fatalf("marshal invalid updated_at goal: %v", err)
	}
	if err := os.WriteFile(goalPath, data, 0o600); err != nil {
		t.Fatalf("write invalid updated_at goal: %v", err)
	}
	if _, err := store.LoadGoal(meta.ID); err == nil || !strings.Contains(err.Error(), "validate goal.json") || !strings.Contains(err.Error(), "updated_at must be RFC3339Nano") {
		t.Fatalf("expected malformed goal updated_at load error, got %v", err)
	}

	invalidBudgetAt := goal
	invalidBudgetAt.BudgetLimitedAt = "not-a-time"
	data, err = json.Marshal(invalidBudgetAt)
	if err != nil {
		t.Fatalf("marshal invalid budget timestamp goal: %v", err)
	}
	if err := os.WriteFile(goalPath, data, 0o600); err != nil {
		t.Fatalf("write invalid budget timestamp goal: %v", err)
	}
	if _, err := store.LoadGoal(meta.ID); err == nil || !strings.Contains(err.Error(), "validate goal.json") || !strings.Contains(err.Error(), "budget_limited_at must be RFC3339Nano") {
		t.Fatalf("expected malformed goal budget timestamp load error, got %v", err)
	}

	nestedCases := []struct {
		name   string
		want   string
		mutate func(*SessionGoal)
	}{
		{
			name: "success criteria updated_at",
			want: "success criteria updated_at must be RFC3339Nano",
			mutate: func(goal *SessionGoal) {
				goal.SuccessCriteria = []GoalCriterion{{
					ID:        "criterion_1",
					Text:      "prove nested criteria timestamps",
					Status:    "pending",
					Required:  true,
					UpdatedAt: "not-a-time",
				}}
			},
		},
		{
			name: "validation last_run_at",
			want: "validation plan item last_run_at must be RFC3339Nano",
			mutate: func(goal *SessionGoal) {
				goal.ValidationPlan = []GoalValidation{{
					ID:        "validation_1",
					Kind:      "command",
					Status:    "pending",
					LastRunAt: "not-a-time",
				}}
			},
		},
		{
			name: "evaluator evidence created_at",
			want: "validation plan item evaluator evidence created_at must be RFC3339Nano",
			mutate: func(goal *SessionGoal) {
				goal.ValidationPlan = []GoalValidation{{
					ID:     "validation_1",
					Kind:   "command",
					Status: "pending",
					EvaluatorEvidence: []GoalEvaluatorEvidence{{
						Artifact:  "reports/evidence.md",
						Status:    "verified",
						CreatedAt: "not-a-time",
					}},
				}}
			},
		},
		{
			name: "mission approved_at",
			want: "mission approved_at must be RFC3339Nano",
			mutate: func(goal *SessionGoal) {
				goal.Mode = GoalModeMission
				goal.Mission = &MissionPlan{
					PlanStatus: MissionPlanStatusApproved,
					ApprovedAt: "not-a-time",
				}
			},
		},
		{
			name: "completion audit completed_at",
			want: "goal completion completed_at must be RFC3339Nano",
			mutate: func(goal *SessionGoal) {
				goal.Status = GoalStatusComplete
				goal.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
				goal.CompletionAudit = &GoalCompletion{
					Status:      GoalStatusComplete,
					Summary:     "done",
					CompletedAt: "not-a-time",
				}
			},
		},
		{
			name: "progress created_at",
			want: "goal progress created_at must be RFC3339Nano",
			mutate: func(goal *SessionGoal) {
				goal.Progress = []GoalProgressRecord{{
					ID:        "progress_1",
					Kind:      "progress",
					Summary:   "recorded",
					Source:    GoalSourceCLI,
					CreatedAt: "not-a-time",
				}}
			},
		},
	}
	for _, tc := range nestedCases {
		t.Run(tc.name, func(t *testing.T) {
			invalid := goal
			tc.mutate(&invalid)
			data, err := json.Marshal(invalid)
			if err != nil {
				t.Fatalf("marshal invalid nested timestamp goal: %v", err)
			}
			if err := os.WriteFile(goalPath, data, 0o600); err != nil {
				t.Fatalf("write invalid nested timestamp goal: %v", err)
			}
			if _, err := store.LoadGoal(meta.ID); err == nil || !strings.Contains(err.Error(), "validate goal.json") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected malformed nested goal timestamp load error containing %q, got %v", tc.want, err)
			}
		})
	}

	if err := store.SaveGoal(meta.ID, invalidCreatedAt); err == nil || !strings.Contains(err.Error(), "created_at must be RFC3339Nano") {
		t.Fatalf("expected SaveGoal to reject invalid created_at, got %v", err)
	}
	invalidProgress := goal
	invalidProgress.Progress = []GoalProgressRecord{{
		ID:        "progress_bad_save",
		Kind:      "progress",
		Summary:   "recorded",
		Source:    GoalSourceCLI,
		CreatedAt: "not-a-time",
	}}
	if err := store.SaveGoal(meta.ID, invalidProgress); err == nil || !strings.Contains(err.Error(), "goal progress created_at must be RFC3339Nano") {
		t.Fatalf("expected SaveGoal to reject invalid nested progress timestamp, got %v", err)
	}
}

func TestStoreListReportsCorruptStateSnapshot(t *testing.T) {
	store := NewStore(t.TempDir())
	parentID := "state_snapshot_parent"
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
		ParentSessionID:  parentID,
		RootSessionID:    parentID,
		Depth:            1,
	}
	state := State{Status: StatusCompleted, Phase: "done", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.SessionDir(meta.ID), "state.json"), []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatalf("write invalid state.json: %v", err)
	}

	if _, err := store.List(10); err == nil || !strings.Contains(err.Error(), "state.json") {
		t.Fatalf("expected List to report state.json, got %v", err)
	}
	if _, _, err := store.ListPage(10, 0); err == nil || !strings.Contains(err.Error(), "state.json") {
		t.Fatalf("expected ListPage to report state.json, got %v", err)
	}
	if _, err := store.ListChildren(parentID, 10); err == nil || !strings.Contains(err.Error(), "state.json") {
		t.Fatalf("expected ListChildren to report state.json, got %v", err)
	}
}

func TestStoreListReportsCorruptMetadataSnapshot(t *testing.T) {
	store := NewStore(t.TempDir())
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
	meta.RootSessionID = meta.ID
	state := State{Status: StatusCompleted, Phase: "done", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.ensureQueueDirs(); err != nil {
		t.Fatalf("ensure queue dirs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.SessionDir(meta.ID), "session.json"), []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatalf("write invalid session.json: %v", err)
	}

	if _, err := store.List(10); err == nil || !strings.Contains(err.Error(), "session.json") {
		t.Fatalf("expected List to report session.json, got %v", err)
	}
	if _, _, err := store.ListPage(10, 0); err == nil || !strings.Contains(err.Error(), "session.json") {
		t.Fatalf("expected ListPage to report session.json, got %v", err)
	}
	if _, err := store.ListChildren(meta.RootSessionID, 10); err == nil || !strings.Contains(err.Error(), "session.json") {
		t.Fatalf("expected ListChildren to report session.json, got %v", err)
	}
}

func TestCreateGoalReturnsHistoryAppendErrorAndRollsBack(t *testing.T) {
	store := NewStore(t.TempDir())
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
	blockGoalHistoryPath(t, store, meta.ID)
	_, err := store.CreateGoal(meta.ID, GoalDraft{
		Enabled:             true,
		Mode:                GoalModeMission,
		Objective:           "Create with required history",
		Features:            []string{"generated task must roll back"},
		CreateTasksFromPlan: true,
		Source:              GoalSourceCLI,
	})
	if err == nil || !strings.Contains(err.Error(), "goal-history.jsonl") {
		t.Fatalf("expected goal history append error, got %v", err)
	}
	if _, loadErr := store.LoadGoal(meta.ID); !errors.Is(loadErr, os.ErrNotExist) {
		t.Fatalf("failed create should not leave goal snapshot, got %v", loadErr)
	}
	tasks, listErr := store.ListTasks(meta.ID)
	if listErr != nil {
		t.Fatalf("list tasks: %v", listErr)
	}
	if len(tasks) != 0 {
		t.Fatalf("failed create should not leave generated tasks, got %#v", tasks)
	}
}

func TestAppendGoalHistoryReportsCorruptCurrentGoalSnapshot(t *testing.T) {
	store := NewStore(t.TempDir())
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
	if _, err := store.CreateGoal(meta.ID, GoalDraft{
		Enabled:   true,
		Objective: "Track history linkage",
		Source:    GoalSourceCLI,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	goalPath := filepath.Join(store.SessionDir(meta.ID), "goal.json")
	if err := os.WriteFile(goalPath, []byte(`{"goal_id":`), 0o600); err != nil {
		t.Fatalf("write corrupt goal: %v", err)
	}
	err := store.AppendGoalHistory(meta.ID, GoalHistoryEntry{
		Type:   "goal.updated",
		Source: GoalSourceSystem,
		Status: GoalStatusActive,
	})
	if err == nil || !strings.Contains(err.Error(), "load goal.json for goal history") {
		t.Fatalf("expected corrupt goal snapshot error, got %v", err)
	}
	history, historyErr := store.LoadGoalHistory(meta.ID)
	if historyErr != nil {
		t.Fatalf("load goal history: %v", historyErr)
	}
	if len(history) != 1 || history[0].Type != "goal.created" {
		t.Fatalf("corrupt goal snapshot should not append unlinked history, got %#v", history)
	}
}

func TestUpdateGoalAccountingReturnsHistoryAppendError(t *testing.T) {
	store := NewStore(t.TempDir())
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
	if _, err := store.CreateGoal(meta.ID, GoalDraft{
		Enabled:   true,
		Objective: "Track accounting history",
		Source:    GoalSourceCLI,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	blockGoalHistoryPath(t, store, meta.ID)
	_, _, err := store.UpdateGoalAccounting(meta.ID, GoalUsageDelta{TokensUsedDelta: 1, SourceTurn: 1})
	if err == nil || !strings.Contains(err.Error(), "goal-history.jsonl") {
		t.Fatalf("expected goal history append error, got %v", err)
	}
	loaded, loadErr := store.LoadGoal(meta.ID)
	if loadErr != nil {
		t.Fatalf("load goal: %v", loadErr)
	}
	if loaded.TokensUsed != 0 {
		t.Fatalf("failed accounting should not advance goal snapshot, got %#v", loaded)
	}
}

func TestStoreGoalConcurrentAccountingAndProgressMutationsBothPersist(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	storeA := NewStore(root)
	storeB := NewStore(root)
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
	if err := storeA.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create: %v", err)
	}
	tokenBudget := int64(5)
	if _, err := storeA.CreateGoal(meta.ID, GoalDraft{
		Enabled:      true,
		Objective:    "Keep concurrent goal facts",
		TokenBudget:  &tokenBudget,
		StopOnBudget: true,
		Source:       GoalSourceCLI,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	accountingDone := make(chan error, 1)
	go func() {
		_, _, err := storeA.MutateGoal(meta.ID, func(goal *SessionGoal) error {
			goal.TokensUsed += 6
			if goal.Status == GoalStatusActive && goalBudgetExceeded(*goal) {
				now := time.Now().UTC().Format(time.RFC3339Nano)
				goal.Status = GoalStatusBudgetLimited
				goal.BudgetLimitedAt = now
				goal.BudgetWrapUpRequestedAt = now
			}
			close(started)
			<-release
			return nil
		})
		accountingDone <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for accounting mutation to hold the goal lock")
	}

	type progressResult struct {
		record GoalProgressRecord
		err    error
	}
	progressDone := make(chan progressResult, 1)
	go func() {
		_, record, err := storeB.RecordGoalProgress(meta.ID, GoalProgressInput{
			Source:   GoalSourceTool,
			Kind:     "progress",
			Summary:  "progress persisted",
			Evidence: []string{"progress evidence"},
		})
		progressDone <- progressResult{record: record, err: err}
	}()

	released := false
	releaseGoalLock := func() {
		if released {
			return
		}
		released = true
		close(release)
	}
	defer releaseGoalLock()

	select {
	case result := <-progressDone:
		releaseGoalLock()
		if result.err != nil {
			t.Fatalf("progress mutation returned early with error: %v", result.err)
		}
		t.Fatalf("progress mutation completed before accounting released the goal lock: record=%#v", result.record)
	case <-time.After(100 * time.Millisecond):
	}

	releaseGoalLock()
	select {
	case err := <-accountingDone:
		if err != nil {
			t.Fatalf("accounting mutation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for accounting mutation")
	}

	var progress progressResult
	select {
	case progress = <-progressDone:
		if progress.err != nil {
			t.Fatalf("record progress: %v", progress.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for progress mutation")
	}
	if progress.record.ID == "" {
		t.Fatalf("expected progress record id, got %#v", progress.record)
	}

	loaded, err := storeA.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load goal: %v", err)
	}
	if loaded.TokensUsed != 6 || loaded.Status != GoalStatusBudgetLimited || loaded.BudgetWrapUpRequestedAt == "" {
		t.Fatalf("expected accounting and budget fields to persist, got %#v", loaded)
	}
	if len(loaded.Progress) != 1 || loaded.Progress[0].ID != progress.record.ID || !containsString(loaded.Progress[0].Evidence, "progress evidence") {
		t.Fatalf("expected progress record to persist with accounting update, got %#v", loaded.Progress)
	}
}

func TestStoreGoalApprovalCreatesLinkedPlanMode(t *testing.T) {
	store := NewStore(t.TempDir())
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
	goal, err := store.CreateGoal(meta.ID, GoalDraft{
		Enabled:             true,
		Mode:                GoalModeMission,
		Objective:           "Plan before implementing",
		RequirePlanApproval: true,
		Source:              GoalSourceCLI,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	planMode, created, err := store.EnsurePlanModeForGoal(meta.ID, goal, PlanModeSourceCLI)
	if err != nil {
		t.Fatalf("ensure plan mode: %v", err)
	}
	if !created || planMode.LinkedGoalID != goal.GoalID || planMode.Status != PlanModeStatusPlanning {
		t.Fatalf("expected linked planning mode, created=%v plan=%#v goal=%#v", created, planMode, goal)
	}
	again, created, err := store.EnsurePlanModeForGoal(meta.ID, goal, PlanModeSourceCLI)
	if err != nil {
		t.Fatalf("ensure plan mode again: %v", err)
	}
	if created || again.PlanModeID != planMode.PlanModeID {
		t.Fatalf("expected existing linked plan mode, created=%v again=%#v", created, again)
	}
}

func TestApproveMissionPlanRejectsGoalWithoutMissionPlan(t *testing.T) {
	store := NewStore(t.TempDir())
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
	if err := store.Create(meta, State{Status: StatusAwaitingInput, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create: %v", err)
	}
	created, err := store.CreateGoal(meta.ID, GoalDraft{
		Enabled:   true,
		Mode:      GoalModeGoal,
		Objective: "Track a plain goal",
		Source:    GoalSourceCLI,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	if created.Mission != nil {
		t.Fatalf("plain goal unexpectedly has mission plan: %#v", created.Mission)
	}
	_, err = store.ApproveMissionPlan(meta.ID, MissionPlanApprovalInput{Source: GoalSourceCLI})
	if err == nil || !strings.Contains(err.Error(), "mission plan is required") {
		t.Fatalf("expected missing mission plan error, got %v", err)
	}
	loaded, err := store.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load goal: %v", err)
	}
	if loaded.Mode != GoalModeGoal || loaded.Mission != nil {
		t.Fatalf("mission approval mutated plain goal: mode=%s mission=%#v", loaded.Mode, loaded.Mission)
	}
	history, err := store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load goal history: %v", err)
	}
	for _, entry := range history {
		if entry.Type == "mission.plan.approved" {
			t.Fatalf("unexpected mission approval history for plain goal: %#v", history)
		}
	}
}

func TestApproveMissionPlanReturnsHistoryAppendError(t *testing.T) {
	store := NewStore(t.TempDir())
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
	if err := store.Create(meta, State{Status: StatusAwaitingInput, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.CreateGoal(meta.ID, GoalDraft{
		Enabled:             true,
		Mode:                GoalModeMission,
		Objective:           "Approve mission with history",
		RequirePlanApproval: true,
		Source:              GoalSourceCLI,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	blockGoalHistoryPath(t, store, meta.ID)
	_, err := store.ApproveMissionPlan(meta.ID, MissionPlanApprovalInput{Source: GoalSourceCLI})
	if err == nil || !strings.Contains(err.Error(), "goal-history.jsonl") {
		t.Fatalf("expected goal history append error, got %v", err)
	}
	loaded, err := store.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load goal: %v", err)
	}
	if loaded.Mission == nil || loaded.Mission.PlanStatus != MissionPlanStatusNeedsApproval || loaded.Mission.ApprovedAt != "" {
		t.Fatalf("failed mission approval should not advance goal snapshot, got %#v", loaded)
	}
}

func TestStoreGoalApprovalRelinksExistingPendingPlanMode(t *testing.T) {
	store := NewStore(t.TempDir())
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
	initial, err := store.CreatePlanMode(meta.ID, PlanModeDraft{
		Enabled:   true,
		Objective: "Plan before implementing",
		Source:    PlanModeSourceCLI,
	})
	if err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	if initial.LinkedGoalID != "" {
		t.Fatalf("expected initially unlinked plan mode, got %#v", initial)
	}
	goal, err := store.CreateGoal(meta.ID, GoalDraft{
		Enabled:             true,
		Mode:                GoalModeMission,
		Objective:           "Plan before implementing",
		RequirePlanApproval: true,
		Source:              GoalSourceCLI,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	planMode, created, err := store.EnsurePlanModeForGoal(meta.ID, goal, PlanModeSourceCLI)
	if err != nil {
		t.Fatalf("ensure plan mode: %v", err)
	}
	if created || planMode.PlanModeID != initial.PlanModeID || planMode.LinkedGoalID != goal.GoalID || planMode.Status != PlanModeStatusPlanning {
		t.Fatalf("expected existing pending plan mode relinked, created=%v plan=%#v initial=%#v goal=%#v", created, planMode, initial, goal)
	}
	history, err := store.LoadPlanModeHistory(meta.ID)
	if err != nil {
		t.Fatalf("load plan mode history: %v", err)
	}
	if !hasPlanModeHistoryType(history, "planmode.linked_goal") {
		t.Fatalf("expected relink history entry, got %#v", history)
	}
}

func TestStoreGoalApprovalCreatesFreshPendingGateAfterNeedsApprovalReset(t *testing.T) {
	store := NewStore(t.TempDir())
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
	goal, err := store.CreateGoal(meta.ID, GoalDraft{
		Enabled:             true,
		Mode:                GoalModeMission,
		Objective:           "Plan before implementing",
		RequirePlanApproval: true,
		Source:              GoalSourceCLI,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	first, created, err := store.EnsurePlanModeForGoal(meta.ID, goal, PlanModeSourceCLI)
	if err != nil {
		t.Fatalf("ensure plan mode: %v", err)
	}
	if !created || first.LinkedGoalID != goal.GoalID || first.Status != PlanModeStatusPlanning {
		t.Fatalf("expected initial linked planning mode, created=%v plan=%#v goal=%#v", created, first, goal)
	}
	if _, err := store.SubmitPlanMode(meta.ID, PlanModeSubmitInput{
		Title:        "Plan",
		Summary:      "Implement after approval.",
		PlanMarkdown: "# Plan\n\nImplement after approval.\n\n# Verification\n\nRun tests.",
		Verification: []string{"go test ./internal/session"},
		Source:       PlanModeSourceTool,
	}); err != nil {
		t.Fatalf("submit plan mode: %v", err)
	}
	if _, err := store.ApprovePlanMode(meta.ID, PlanModeSourceCLI); err != nil {
		t.Fatalf("approve plan mode: %v", err)
	}
	executing, err := store.MarkPlanModeExecuting(meta.ID, PlanModeSourceCLI)
	if err != nil {
		t.Fatalf("mark plan mode executing: %v", err)
	}
	if executing.PlanModeID != first.PlanModeID || executing.Status != PlanModeStatusExecuting {
		t.Fatalf("expected first plan mode executing, got %#v", executing)
	}
	goal, err = store.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load goal: %v", err)
	}
	goal.Mission.PlanStatus = "needs_approval"
	if err := store.SaveGoal(meta.ID, goal); err != nil {
		t.Fatalf("save reset goal: %v", err)
	}
	second, created, err := store.EnsurePlanModeForGoal(meta.ID, goal, PlanModeSourceCLI)
	if err != nil {
		t.Fatalf("ensure reset plan mode: %v", err)
	}
	if !created || second.PlanModeID == first.PlanModeID || second.LinkedGoalID != goal.GoalID || second.Status != PlanModeStatusPlanning {
		t.Fatalf("expected fresh pending plan mode after needs_approval reset, created=%v first=%#v second=%#v goal=%#v", created, first, second, goal)
	}
}

func TestStoreCompleteGoalPersistsAuditAndItemEvidence(t *testing.T) {
	store := NewStore(t.TempDir())
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
	if _, err := store.CreateGoal(meta.ID, GoalDraft{
		Enabled:         true,
		Objective:       "Finish with evidence",
		SuccessCriteria: []string{"tests pass"},
		ValidationPlan:  []string{"go test ./internal/session"},
		Source:          GoalSourceCLI,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	goal, err := store.CompleteGoal(meta.ID, GoalCompletionInput{
		Source:      GoalSourceTool,
		CompletedBy: "tool",
		Summary:     "All requested checks passed.",
		Evidence:    []string{"go test ./internal/session"},
		CriteriaStatuses: []GoalItemStatusUpdate{{
			ID:       "criterion_0001",
			Status:   "verified",
			Evidence: []string{"criterion evidence"},
		}},
		ValidationStatuses: []GoalItemStatusUpdate{{
			ID:       "validation_0001",
			Status:   "verified",
			Evidence: []string{"validation evidence"},
		}},
	})
	if err != nil {
		t.Fatalf("complete goal: %v", err)
	}
	if goal.Status != GoalStatusComplete || goal.CompletedAt == "" || goal.CompletionAudit == nil {
		t.Fatalf("expected completed goal audit, got %#v", goal)
	}
	if goal.CompletionAudit.Summary == "" || !containsString(goal.CompletionAudit.Evidence, "go test ./internal/session") {
		t.Fatalf("expected completion evidence in snapshot, got %#v", goal.CompletionAudit)
	}
	if goal.SuccessCriteria[0].Status != "verified" || !containsString(goal.SuccessCriteria[0].Evidence, "criterion evidence") {
		t.Fatalf("expected criterion evidence in snapshot, got %#v", goal.SuccessCriteria[0])
	}
	if goal.ValidationPlan[0].Status != "verified" || goal.ValidationPlan[0].LastRunAt == "" || !containsString(goal.ValidationPlan[0].Evidence, "validation evidence") {
		t.Fatalf("expected validation evidence in snapshot, got %#v", goal.ValidationPlan[0])
	}
}

func TestCompleteGoalReturnsHistoryAppendError(t *testing.T) {
	store := NewStore(t.TempDir())
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
	if _, err := store.CreateGoal(meta.ID, GoalDraft{
		Enabled:   true,
		Objective: "Complete with history",
		Source:    GoalSourceCLI,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	blockGoalHistoryPath(t, store, meta.ID)
	_, err := store.CompleteGoal(meta.ID, GoalCompletionInput{
		Source:  GoalSourceTool,
		Summary: "Complete despite blocked history",
	})
	if err == nil || !strings.Contains(err.Error(), "goal-history.jsonl") {
		t.Fatalf("expected goal history append error, got %v", err)
	}
	loaded, loadErr := store.LoadGoal(meta.ID)
	if loadErr != nil {
		t.Fatalf("load goal: %v", loadErr)
	}
	if loaded.Status != GoalStatusActive || loaded.CompletedAt != "" || loaded.CompletionAudit != nil {
		t.Fatalf("failed completion should not advance goal snapshot, got %#v", loaded)
	}
}

func TestMissionPlanCoverageReportsUncoveredAndInvalidAssignments(t *testing.T) {
	goal := SessionGoal{
		SchemaVersion: 1,
		SessionID:     "session_cov",
		GoalID:        "goal_cov",
		Mode:          GoalModeMission,
		Objective:     "Check coverage",
		Status:        GoalStatusActive,
		Mission: &MissionPlan{
			ValidationContract: []GoalValidation{
				{ID: "validation_api", Status: "pending"},
				{ID: "validation_cli", Status: "pending"},
				{ID: "validation_docs", Status: "pending"},
			},
			Features: []MissionFeature{
				{ID: "feature_api", Title: "API", Status: "pending", ClaimedAssertions: []string{"validation_api", "validation_unknown"}},
				{ID: "feature_empty", Title: "Empty", Status: "pending"},
			},
			Milestones: []MissionMilestone{
				{ID: "milestone_cli", Title: "CLI", Status: "pending", ValidationIDs: []string{"validation_cli"}},
				{ID: "milestone_empty", Title: "Empty", Status: "pending"},
			},
		},
	}
	coverage := CheckMissionPlanCoverage(goal)
	if coverage.ValidationTotal != 3 || coverage.CoveredAssertions != 2 || !coverage.ApprovalBlocked {
		t.Fatalf("unexpected coverage summary: %#v", coverage)
	}
	if !containsString(coverage.UncoveredAssertions, "validation_docs") || !containsString(coverage.FeaturesWithoutAssertions, "feature_empty") || !containsString(coverage.MilestonesWithoutValidation, "milestone_empty") {
		t.Fatalf("expected uncovered and unassigned facts, got %#v", coverage)
	}
	if !containsString(coverage.UnknownClaimedAssertions, "validation_unknown") {
		t.Fatalf("expected unknown claimed assertion, got %#v", coverage)
	}
}

func TestPatchGoalRejectsMalformedStructuredItems(t *testing.T) {
	store := NewStore(t.TempDir())
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
	if _, err := store.CreateGoal(meta.ID, GoalDraft{
		Enabled:   true,
		Mode:      GoalModeMission,
		Objective: "Reject malformed structured items",
		Source:    GoalSourceCLI,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}

	criteria := []GoalCriterion{{ID: "criterion_0001", Text: "valid", Status: "pending"}, {ID: "criterion_0001", Text: "duplicate", Status: "pending"}}
	if _, err := store.PatchGoal(meta.ID, GoalPatchInput{SuccessCriteria: &criteria}); err == nil || !strings.Contains(err.Error(), "duplicate success criteria id") {
		t.Fatalf("expected duplicate criteria validation error, got %v", err)
	}
	mission := MissionPlan{
		Features: []MissionFeature{{ID: "feature_0001", Title: "", Status: "pending"}},
	}
	if _, err := store.PatchGoal(meta.ID, GoalPatchInput{Mission: &mission}); err == nil || !strings.Contains(err.Error(), "mission feature title is required") {
		t.Fatalf("expected feature title validation error, got %v", err)
	}
	loaded, err := store.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load goal: %v", err)
	}
	if len(loaded.SuccessCriteria) != 0 || (loaded.Mission != nil && len(loaded.Mission.Features) != 0) {
		t.Fatalf("failed malformed patches should not advance goal snapshot, got %#v", loaded)
	}
}

func TestStoreRecordGoalProgressUpdatesMissionValidationAndBudgetWrapUp(t *testing.T) {
	store := NewStore(t.TempDir())
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
	goal, err := store.CreateGoal(meta.ID, GoalDraft{
		Enabled:        true,
		Mode:           GoalModeMission,
		Objective:      "Record durable progress",
		ValidationPlan: []string{"go test ./internal/session"},
		Features:       []string{"feature work"},
		Milestones:     []string{"milestone work"},
		StopOnBudget:   true,
		Source:         GoalSourceCLI,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	goal.Status = GoalStatusBudgetLimited
	goal.BudgetWrapUpRequestedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := store.SaveGoal(meta.ID, goal); err != nil {
		t.Fatalf("save budget goal: %v", err)
	}
	exitCode := 0
	updated, record, err := store.RecordGoalProgress(meta.ID, GoalProgressInput{
		Source:          GoalSourceTool,
		Kind:            "budget_wrapup",
		Summary:         "Implemented feature work; remaining docs.",
		Evidence:        []string{"go test ./internal/session"},
		LinkedArtifacts: []string{"reports/progress.md"},
		Commands:        []GoalProgressCommand{{Command: "go test ./internal/session", ExitCode: &exitCode, Summary: "passed"}},
		Blockers:        []string{"manual browser validation pending"},
		FeatureUpdates: []MissionFeatureProgressUpdate{{
			ID:                "feature_0001",
			Status:            "completed",
			Evidence:          []string{"feature evidence"},
			ClaimedAssertions: []string{"validation_0001"},
			ChildSessionIDs:   []string{"child_eval"},
		}},
		MilestoneUpdates: []MissionMilestoneProgressUpdate{{
			ID:            "milestone_0001",
			Status:        "completed",
			ValidationIDs: []string{"validation_0001"},
		}},
		ValidationUpdates: []GoalValidationProgressUpdate{{
			ID:              "validation_0001",
			Status:          "verified",
			Evidence:        []string{"validator evidence"},
			ChildSessionIDs: []string{"child_eval"},
			EvaluatorEvidence: []GoalEvaluatorEvidence{{
				ChildSessionID: "child_eval",
				Summary:        "independent evaluator passed",
				Status:         "verified",
			}},
		}},
	})
	if err != nil {
		t.Fatalf("record progress: %v", err)
	}
	if record.Kind != "budget_wrapup" || updated.BudgetWrapUpRecordedAt == "" || !HasBudgetWrapUpRecord(updated) {
		t.Fatalf("expected budget wrap-up record, record=%#v goal=%#v", record, updated)
	}
	if updated.Mission.Features[0].Status != "completed" || !containsString(updated.Mission.Features[0].ClaimedAssertions, "validation_0001") {
		t.Fatalf("expected feature progress update, got %#v", updated.Mission.Features[0])
	}
	if updated.Mission.ValidationContract[0].Status != "verified" || len(updated.Mission.ValidationContract[0].EvaluatorEvidence) != 1 {
		t.Fatalf("expected validation evaluator evidence, got %#v", updated.Mission.ValidationContract[0])
	}
	history, err := store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load goal history: %v", err)
	}
	if !hasGoalHistoryType(history, "goal.progress.recorded") || !hasGoalHistoryType(history, "mission.validation.updated") {
		t.Fatalf("expected progress and validation history, got %#v", history)
	}
}

func TestRecordGoalProgressReturnsHistoryAppendError(t *testing.T) {
	store := NewStore(t.TempDir())
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
	if _, err := store.CreateGoal(meta.ID, GoalDraft{
		Enabled:   true,
		Objective: "Record progress with history",
		Source:    GoalSourceCLI,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	blockGoalHistoryPath(t, store, meta.ID)
	_, _, err := store.RecordGoalProgress(meta.ID, GoalProgressInput{
		Source:  GoalSourceTool,
		Kind:    "handoff",
		Summary: "Progress must report history failures.",
	})
	if err == nil || !strings.Contains(err.Error(), "goal-history.jsonl") {
		t.Fatalf("expected goal history append error, got %v", err)
	}
	loaded, loadErr := store.LoadGoal(meta.ID)
	if loadErr != nil {
		t.Fatalf("load goal: %v", loadErr)
	}
	if len(loaded.Progress) != 0 {
		t.Fatalf("failed progress record should not advance goal snapshot, got %#v", loaded)
	}
}

func TestStoreHonorsConfiguredDirModeForDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o750)
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
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	dirInfo, err := os.Stat(filepath.Join(store.SessionDir(meta.ID), "artifacts"))
	if err != nil {
		t.Fatalf("stat artifacts dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o750 {
		t.Fatalf("expected artifacts dir mode 0750, got %s", perm.String())
	}

	fileInfo, err := os.Stat(filepath.Join(store.SessionDir(meta.ID), "state.json"))
	if err != nil {
		t.Fatalf("stat state file: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o640 {
		t.Fatalf("expected file mode 0640, got %s", perm.String())
	}
}

func TestStoreSaveStateRefreshesUpdatedAt(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{
		Status:    StatusRunning,
		Phase:     "prepare",
		UpdatedAt: "2026-01-01T00:00:00Z",
	}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	time.Sleep(10 * time.Millisecond)
	state.Status = StatusAwaitingInput
	if err := store.SaveState(meta.ID, state); err != nil {
		t.Fatalf("save state: %v", err)
	}

	loaded, err := store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if loaded.UpdatedAt == "2026-01-01T00:00:00Z" || loaded.UpdatedAt == "" {
		t.Fatalf("expected UpdatedAt to refresh, got %q", loaded.UpdatedAt)
	}
}

func TestStoreSaveStatePreservesCurrentLoadedSkills(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{
		Status:       StatusRunning,
		Phase:        "prepare",
		UpdatedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		LoadedSkills: []string{"helpers"},
	}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	stale := State{
		Status:    StatusAwaitingInput,
		Phase:     "turn_decide",
		Turn:      2,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.SaveState(meta.ID, stale); err != nil {
		t.Fatalf("save stale state: %v", err)
	}

	loaded, err := store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if strings.Join(loaded.LoadedSkills, ",") != "helpers" {
		t.Fatalf("expected loaded skill to be preserved, got %#v", loaded.LoadedSkills)
	}
}

func TestStoreRejectsPathLikeRecordIDs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create valid session: %v", err)
	}

	badSession := "../outside"
	if err := store.Create(SessionMetadata{ID: badSession}, State{}); err == nil {
		t.Fatal("expected Create to reject path-like session id")
	}
	if _, err := store.LoadMetadata(badSession); err == nil {
		t.Fatal("expected LoadMetadata to reject path-like session id")
	}
	if err := store.DeleteSessionTree(badSession); err == nil {
		t.Fatal("expected DeleteSessionTree to reject path-like session id")
	}
	if _, err := os.Stat(filepath.Join(root, "..", "outside")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected outside path to remain absent, got %v", err)
	}

	if err := store.SaveTask(meta.ID, Task{ID: "../state"}); err == nil {
		t.Fatal("expected SaveTask to reject path-like task id")
	}
	if _, err := store.GetTask(meta.ID, "../state"); err == nil {
		t.Fatal("expected GetTask to reject path-like task id")
	}
	if err := store.SaveJob(QueueJob{ID: "../job", Status: QueueStatusQueued, Prompt: "x", Mode: ModeExec}); err == nil {
		t.Fatal("expected SaveJob to reject path-like queue job id")
	}
	if _, err := store.LoadJob("../job"); err == nil {
		t.Fatal("expected LoadJob to reject path-like queue job id")
	}
}

func TestStoreSaveTasksRemovesStaleTaskFiles(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "save_tasks_exact_set",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := CreateTask(store, meta.ID, TaskCreateInput{Subject: "first"}); err != nil {
		t.Fatalf("create first task: %v", err)
	}
	if _, err := CreateTask(store, meta.ID, TaskCreateInput{Subject: "second"}); err != nil {
		t.Fatalf("create second task: %v", err)
	}

	if err := store.SaveTasks(meta.ID, []Task{{
		ID:        "task_0001",
		Subject:   "first",
		Status:    "pending",
		Priority:  "medium",
		CreatedAt: now,
		UpdatedAt: now,
	}}); err != nil {
		t.Fatalf("save tasks: %v", err)
	}

	tasks, err := store.ListTasks(meta.ID)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != "task_0001" {
		t.Fatalf("expected stale task file removed, got %#v", tasks)
	}
	if _, err := store.GetTask(meta.ID, "task_0002"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected stale task file missing, got %v", err)
	}
}

func TestDeleteSessionTreeRemovesRootLinkedDescendants(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rootMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "root_linked_delete",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyInteractive,
		RootSessionID:    "root_linked_delete",
	}
	if err := store.Create(rootMeta, State{Status: StatusCompleted, Phase: "turn_decide", UpdatedAt: now}); err != nil {
		t.Fatalf("create root session: %v", err)
	}
	orphanedChildMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "root_linked_orphan_child",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		ParentSessionID:  "missing_root_linked_parent",
		RootSessionID:    rootMeta.ID,
		QueueJobID:       "job_root_linked_delete",
		Depth:            1,
	}
	if err := store.Create(orphanedChildMeta, State{Status: StatusCompleted, Phase: "turn_decide", UpdatedAt: now}); err != nil {
		t.Fatalf("create root-linked child session: %v", err)
	}
	if err := store.SaveJob(QueueJob{
		SchemaVersion:   1,
		ID:              orphanedChildMeta.QueueJobID,
		CreatedAt:       now,
		Status:          QueueStatusCompleted,
		ParentSessionID: orphanedChildMeta.ParentSessionID,
		RootSessionID:   rootMeta.ID,
		SessionID:       orphanedChildMeta.ID,
		Prompt:          "done",
		Mode:            ModeExec,
		Background:      true,
	}); err != nil {
		t.Fatalf("save root-linked job: %v", err)
	}

	if err := store.DeleteSessionTree(rootMeta.ID); err != nil {
		t.Fatalf("delete session tree: %v", err)
	}
	if _, err := store.LoadMetadata(rootMeta.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected root session to be deleted, got %v", err)
	}
	if _, err := store.LoadMetadata(orphanedChildMeta.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected root-linked child session to be deleted, got %v", err)
	}
	if _, err := store.LoadJob(orphanedChildMeta.QueueJobID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected root-linked queue job to be deleted, got %v", err)
	}
}

func TestStoreRejectsPathLikeArtifactPaths(t *testing.T) {
	temp := t.TempDir()
	root := filepath.Join(temp, "sessions")
	store := NewStore(root)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create valid session: %v", err)
	}
	statePath := filepath.Join(store.SessionDir(meta.ID), "state.json")
	stateBefore, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state before artifact write: %v", err)
	}

	if _, err := store.WriteArtifact(meta.ID, "../state.json", map[string]any{"bad": true}); err == nil {
		t.Fatal("expected WriteArtifact to reject parent traversal")
	}
	if _, err := store.WriteArtifact(meta.ID, "../../../escaped.json", map[string]any{"bad": true}); err == nil {
		t.Fatal("expected WriteArtifact to reject root escape")
	}
	if _, err := store.WriteArtifact(meta.ID, "/tmp/escaped.json", map[string]any{"bad": true}); err == nil {
		t.Fatal("expected WriteArtifact to reject absolute path")
	}
	if _, err := store.WriteArtifact(meta.ID, `compactions\..\state.json`, map[string]any{"bad": true}); err == nil {
		t.Fatal("expected WriteArtifact to reject backslash parent traversal")
	}
	if _, err := store.WriteTranscript(meta.ID, "../events.jsonl", []Message{NewMessage("user", "bad")}); err == nil {
		t.Fatal("expected WriteTranscript to reject parent traversal")
	}

	stateAfter, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state after rejected artifact writes: %v", err)
	}
	if string(stateAfter) != string(stateBefore) {
		t.Fatalf("state.json was modified by rejected artifact write:\nbefore: %s\nafter: %s", stateBefore, stateAfter)
	}
	if _, err := os.Stat(filepath.Join(temp, "escaped.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected escaped artifact to remain absent, got %v", err)
	}
}

func TestUpdateSteerRequestsMergesConcurrentAppend(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	storeA := NewStore(root)
	storeB := NewStore(root)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := storeA.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create: %v", err)
	}

	first := NewSteerRequest("first", false)
	if err := storeA.AppendSteerRequest(meta.ID, first); err != nil {
		t.Fatalf("append first: %v", err)
	}
	snapshot, err := storeA.LoadSteerRequests(meta.ID)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	snapshot[0].Status = SteerStatusAccepted

	second := NewSteerRequest("second", false)
	if err := storeB.AppendSteerRequest(meta.ID, second); err != nil {
		t.Fatalf("append second: %v", err)
	}
	if err := storeA.UpdateSteerRequests(meta.ID, snapshot); err != nil {
		t.Fatalf("update snapshot: %v", err)
	}

	loaded, err := storeA.LoadSteerRequests(meta.ID)
	if err != nil {
		t.Fatalf("load merged: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected both steer requests to survive merge, got %#v", loaded)
	}
	statusByText := map[string]string{}
	for _, request := range loaded {
		statusByText[request.Text] = request.Status
	}
	if statusByText["first"] != SteerStatusAccepted || statusByText["second"] != SteerStatusPending {
		t.Fatalf("unexpected merged statuses: %#v", loaded)
	}
}

func TestRefreshPendingSteerCountUsesMergedDurableRequests(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	storeA := NewStore(root)
	storeB := NewStore(root)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := storeA.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create: %v", err)
	}

	first := NewSteerRequest("first", false)
	if err := storeA.AppendSteerRequest(meta.ID, first); err != nil {
		t.Fatalf("append first: %v", err)
	}
	snapshot, err := storeA.LoadSteerRequests(meta.ID)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	snapshot[0].Status = SteerStatusAccepted
	second := NewSteerRequest("second", false)
	if err := storeB.AppendSteerRequest(meta.ID, second); err != nil {
		t.Fatalf("append second: %v", err)
	}
	if err := storeA.UpdateSteerRequests(meta.ID, snapshot); err != nil {
		t.Fatalf("update snapshot: %v", err)
	}

	state, err := storeA.RefreshPendingSteerCount(meta.ID)
	if err != nil {
		t.Fatalf("refresh pending count: %v", err)
	}
	if state.PendingSteerCount != 1 {
		t.Fatalf("expected pending count from merged durable queue, got %#v", state)
	}
}

func TestClaimSessionRunPreservesDurablePendingSteerCount(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusAwaitingInput, Phase: "awaiting_input", UpdatedAt: now}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.AppendSteerRequest(meta.ID, NewSteerRequest("use queued evidence before continuing", false)); err != nil {
		t.Fatalf("append steer request: %v", err)
	}
	if _, err := store.RefreshPendingSteerCount(meta.ID); err != nil {
		t.Fatalf("refresh pending count: %v", err)
	}

	claimed, err := store.ClaimSessionRun(meta.ID, StatusAwaitingInput)
	if err != nil {
		t.Fatalf("claim session run: %v", err)
	}
	if claimed.PendingSteerCount != 1 {
		t.Fatalf("expected claimed state to preserve pending steer count, got %#v", claimed)
	}
	loaded, err := store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load claimed state: %v", err)
	}
	if loaded.PendingSteerCount != 1 {
		t.Fatalf("expected state.json to preserve pending steer count, got %#v", loaded)
	}
}

func TestRestoreOpenSteerRequestsPreservesOtherFacts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	storeA := NewStore(root)
	storeB := NewStore(root)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := storeA.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create: %v", err)
	}

	alreadyAccepted := NewSteerRequest("already accepted", false)
	alreadyAccepted.Status = SteerStatusAccepted
	if err := storeA.AppendSteerRequest(meta.ID, alreadyAccepted); err != nil {
		t.Fatalf("append already accepted: %v", err)
	}
	pending := NewSteerRequest("retry pending", false)
	if err := storeA.AppendSteerRequest(meta.ID, pending); err != nil {
		t.Fatalf("append pending: %v", err)
	}
	deferred := NewSteerRequest("retry deferred", true)
	deferred.Status = SteerStatusDeferred
	if err := storeA.AppendSteerRequest(meta.ID, deferred); err != nil {
		t.Fatalf("append deferred: %v", err)
	}
	acceptedSnapshot, err := storeA.LoadSteerRequests(meta.ID)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	for i := range acceptedSnapshot {
		if acceptedSnapshot[i].ID == pending.ID || acceptedSnapshot[i].ID == deferred.ID {
			acceptedSnapshot[i].Status = SteerStatusAccepted
		}
	}
	if err := storeA.UpdateSteerRequests(meta.ID, acceptedSnapshot); err != nil {
		t.Fatalf("mark accepted: %v", err)
	}

	concurrent := NewSteerRequest("concurrent", false)
	if err := storeB.AppendSteerRequest(meta.ID, concurrent); err != nil {
		t.Fatalf("append concurrent: %v", err)
	}
	if err := storeA.RestoreOpenSteerRequests(meta.ID, []SteerRequest{pending, deferred}); err != nil {
		t.Fatalf("restore open steer: %v", err)
	}

	loaded, err := storeA.LoadSteerRequests(meta.ID)
	if err != nil {
		t.Fatalf("load restored: %v", err)
	}
	statusByText := map[string]string{}
	for _, request := range loaded {
		statusByText[request.Text] = request.Status
	}
	if statusByText["already accepted"] != SteerStatusAccepted {
		t.Fatalf("expected older accepted steer to remain accepted, got %#v", loaded)
	}
	if statusByText["retry pending"] != SteerStatusPending {
		t.Fatalf("expected failed pending steer to return pending, got %#v", loaded)
	}
	if statusByText["retry deferred"] != SteerStatusDeferred {
		t.Fatalf("expected failed deferred steer to return deferred, got %#v", loaded)
	}
	if statusByText["concurrent"] != SteerStatusPending {
		t.Fatalf("expected concurrent steer to survive as pending, got %#v", loaded)
	}
}

func TestLoadSteerRequestsRejectsMalformedSnapshot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create: %v", err)
	}
	malformed := SteerRequest{
		ID:        "../bad",
		CreatedAt: now,
		Source:    "cli",
		Text:      "focus on tests",
		Status:    SteerStatusPending,
	}
	steerPath := filepath.Join(store.SessionDir(meta.ID), "control", "steer.jsonl")
	if err := store.writeJSONL(steerPath, []SteerRequest{malformed}); err != nil {
		t.Fatalf("write malformed steer queue: %v", err)
	}

	if _, err := store.LoadSteerRequests(meta.ID); err == nil || !strings.Contains(err.Error(), "validate steer.jsonl") || !strings.Contains(err.Error(), "path separators") {
		t.Fatalf("expected malformed steer.jsonl validation error, got %v", err)
	}
	if _, err := store.RefreshPendingSteerCount(meta.ID); err == nil || !strings.Contains(err.Error(), "validate steer.jsonl") {
		t.Fatalf("expected pending count refresh to reject malformed steer.jsonl, got %v", err)
	}

	invalidCreatedAt := malformed
	invalidCreatedAt.ID = "steer_bad_time"
	invalidCreatedAt.CreatedAt = "not-a-time"
	if err := store.writeJSONL(steerPath, []SteerRequest{invalidCreatedAt}); err != nil {
		t.Fatalf("write invalid-time steer queue: %v", err)
	}
	if _, err := store.LoadSteerRequests(meta.ID); err == nil || !strings.Contains(err.Error(), "validate steer.jsonl") || !strings.Contains(err.Error(), "created_at must be RFC3339Nano") {
		t.Fatalf("expected invalid steer timestamp validation error, got %v", err)
	}
}

func TestSteerRequestWritesRejectMalformedRequests(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create: %v", err)
	}

	blankText := NewSteerRequest("   ", false)
	if err := store.AppendSteerRequest(meta.ID, blankText); err == nil || !strings.Contains(err.Error(), "steer request text is required") {
		t.Fatalf("expected append to reject blank steer text, got %v", err)
	}
	invalidCreatedAt := NewSteerRequest("bad time", false)
	invalidCreatedAt.CreatedAt = "not-a-time"
	if err := store.AppendSteerRequest(meta.ID, invalidCreatedAt); err == nil || !strings.Contains(err.Error(), "created_at must be RFC3339Nano") {
		t.Fatalf("expected append to reject invalid steer timestamp, got %v", err)
	}

	valid := NewSteerRequest("focus on tests", false)
	if err := store.AppendSteerRequest(meta.ID, valid); err != nil {
		t.Fatalf("append valid steer: %v", err)
	}
	invalidStatus := valid
	invalidStatus.Status = "done"
	if err := store.UpdateSteerRequests(meta.ID, []SteerRequest{invalidStatus}); err == nil || !strings.Contains(err.Error(), "invalid steer request status") {
		t.Fatalf("expected update to reject invalid status, got %v", err)
	}
	duplicate := NewSteerRequest("duplicate", false)
	duplicate.ID = valid.ID
	if err := store.AppendSteerRequest(meta.ID, duplicate); err == nil || !strings.Contains(err.Error(), "duplicate steer request id") {
		t.Fatalf("expected append to reject duplicate id, got %v", err)
	}
	loaded, err := store.LoadSteerRequests(meta.ID)
	if err != nil {
		t.Fatalf("load steer requests: %v", err)
	}
	if len(loaded) != 1 || loaded[0].ID != valid.ID || loaded[0].Status != SteerStatusPending {
		t.Fatalf("malformed steer write changed durable queue: %#v", loaded)
	}
}

func TestUpdateBackgroundNotificationsMergesConcurrentAppend(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	storeA := NewStore(root)
	storeB := NewStore(root)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := storeA.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create: %v", err)
	}

	first := NewBackgroundNotification(QueueJob{
		ID:            "job_background_first",
		Status:        QueueStatusCompleted,
		SessionID:     "child_background_first",
		SessionStatus: StatusCompleted,
	})
	if err := storeA.AppendBackgroundNotification(meta.ID, first); err != nil {
		t.Fatalf("append first: %v", err)
	}
	snapshot, err := storeA.LoadBackgroundNotifications(meta.ID)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	snapshot[0].DeliveryStatus = BackgroundNotificationAccepted

	second := NewBackgroundNotification(QueueJob{
		ID:            "job_background_second",
		Status:        QueueStatusCompleted,
		SessionID:     "child_background_second",
		SessionStatus: StatusCompleted,
	})
	if err := storeB.AppendBackgroundNotification(meta.ID, second); err != nil {
		t.Fatalf("append second: %v", err)
	}
	if err := storeA.UpdateBackgroundNotifications(meta.ID, snapshot); err != nil {
		t.Fatalf("update snapshot: %v", err)
	}

	loaded, err := storeA.LoadBackgroundNotifications(meta.ID)
	if err != nil {
		t.Fatalf("load merged: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected both background notifications to survive merge, got %#v", loaded)
	}
	statusByJob := map[string]string{}
	for _, notification := range loaded {
		statusByJob[notification.QueueJobID] = notification.DeliveryStatus
	}
	if statusByJob["job_background_first"] != BackgroundNotificationAccepted || statusByJob["job_background_second"] != BackgroundNotificationPending {
		t.Fatalf("unexpected merged statuses: %#v", loaded)
	}
}

func TestEnsureBackgroundNotificationRefreshesChangedQueueFacts(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create: %v", err)
	}
	blocked := NewBackgroundNotification(QueueJob{
		ID:            "job_background_refresh",
		Status:        QueueStatusBlocked,
		SessionID:     "child_background_refresh",
		SessionStatus: StatusAwaitingInput,
		LastError:     "child session is resumable: awaiting_input",
	})
	if err := store.EnsureBackgroundNotification(meta.ID, blocked); err != nil {
		t.Fatalf("ensure blocked: %v", err)
	}
	loaded, err := store.LoadBackgroundNotifications(meta.ID)
	if err != nil {
		t.Fatalf("load blocked notification: %v", err)
	}
	loaded[0].DeliveryStatus = BackgroundNotificationAccepted
	if err := store.UpdateBackgroundNotifications(meta.ID, loaded); err != nil {
		t.Fatalf("accept blocked notification: %v", err)
	}
	completed := NewBackgroundNotification(QueueJob{
		ID:            "job_background_refresh",
		Status:        QueueStatusCompleted,
		SessionID:     "child_background_refresh",
		SessionStatus: StatusCompleted,
		FinalText:     "child completed after continue",
	})
	if err := store.EnsureBackgroundNotification(meta.ID, completed); err != nil {
		t.Fatalf("ensure completed: %v", err)
	}

	loaded, err = store.LoadBackgroundNotifications(meta.ID)
	if err != nil {
		t.Fatalf("load refreshed notification: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected one refreshed notification, got %#v", loaded)
	}
	if loaded[0].Status != QueueStatusCompleted || loaded[0].SessionStatus != StatusCompleted || loaded[0].FinalText != "child completed after continue" {
		t.Fatalf("expected completed notification facts, got %#v", loaded[0])
	}
	if loaded[0].LastError != "" {
		t.Fatalf("expected completed notification refresh to clear stale resumable error, got %#v", loaded[0])
	}
	if loaded[0].DeliveryStatus != BackgroundNotificationPending {
		t.Fatalf("expected changed terminal facts to be re-delivered, got %#v", loaded[0])
	}
	if err := store.EnsureBackgroundNotification(meta.ID, completed); err != nil {
		t.Fatalf("ensure completed again: %v", err)
	}
	loaded, err = store.LoadBackgroundNotifications(meta.ID)
	if err != nil {
		t.Fatalf("reload refreshed notification: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected idempotent notification refresh, got %#v", loaded)
	}
}

func TestEnsureBackgroundNotificationDoesNotRedeliverUnchangedFailedFacts(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create: %v", err)
	}
	failed := NewBackgroundNotification(QueueJob{
		ID:            "job_background_failed_same_error",
		Status:        QueueStatusFailed,
		SessionID:     "child_background_failed_same_error",
		SessionStatus: StatusFailed,
		LastError:     "record tool.before event: duplicate event id",
	})
	if err := store.EnsureBackgroundNotification(meta.ID, failed); err != nil {
		t.Fatalf("ensure failed notification: %v", err)
	}
	loaded, err := store.LoadBackgroundNotifications(meta.ID)
	if err != nil {
		t.Fatalf("load failed notification: %v", err)
	}
	loaded[0].DeliveryStatus = BackgroundNotificationAccepted
	if err := store.UpdateBackgroundNotifications(meta.ID, loaded); err != nil {
		t.Fatalf("accept failed notification: %v", err)
	}
	if err := store.EnsureBackgroundNotification(meta.ID, failed); err != nil {
		t.Fatalf("ensure same failed notification: %v", err)
	}

	loaded, err = store.LoadBackgroundNotifications(meta.ID)
	if err != nil {
		t.Fatalf("reload failed notification: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected one failed notification, got %#v", loaded)
	}
	if loaded[0].DeliveryStatus != BackgroundNotificationAccepted {
		t.Fatalf("unchanged failed notification should not be redelivered, got %#v", loaded[0])
	}
}

func TestEnsureBackgroundNotificationRedeliversChangedFailedFacts(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create: %v", err)
	}
	failed := NewBackgroundNotification(QueueJob{
		ID:            "job_background_failed_new_error",
		Status:        QueueStatusFailed,
		SessionID:     "child_background_failed_new_error",
		SessionStatus: StatusFailed,
		LastError:     "first error",
	})
	if err := store.EnsureBackgroundNotification(meta.ID, failed); err != nil {
		t.Fatalf("ensure failed notification: %v", err)
	}
	loaded, err := store.LoadBackgroundNotifications(meta.ID)
	if err != nil {
		t.Fatalf("load failed notification: %v", err)
	}
	loaded[0].DeliveryStatus = BackgroundNotificationAccepted
	if err := store.UpdateBackgroundNotifications(meta.ID, loaded); err != nil {
		t.Fatalf("accept failed notification: %v", err)
	}
	changed := NewBackgroundNotification(QueueJob{
		ID:            "job_background_failed_new_error",
		Status:        QueueStatusFailed,
		SessionID:     "child_background_failed_new_error",
		SessionStatus: StatusFailed,
		LastError:     "second error",
	})
	if err := store.EnsureBackgroundNotification(meta.ID, changed); err != nil {
		t.Fatalf("ensure changed failed notification: %v", err)
	}

	loaded, err = store.LoadBackgroundNotifications(meta.ID)
	if err != nil {
		t.Fatalf("reload failed notification: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected one failed notification, got %#v", loaded)
	}
	if loaded[0].LastError != "second error" || loaded[0].DeliveryStatus != BackgroundNotificationPending {
		t.Fatalf("changed failed notification should be redelivered, got %#v", loaded[0])
	}
}

func TestUpdateBackgroundNotificationsPreservesConcurrentFactRefresh(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	storeA := NewStore(root)
	storeB := NewStore(root)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := storeA.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create: %v", err)
	}

	blocked := NewBackgroundNotification(QueueJob{
		ID:            "job_background_race",
		Status:        QueueStatusBlocked,
		SessionID:     "child_background_race",
		SessionStatus: StatusAwaitingInput,
		LastError:     "child session is resumable: awaiting_input",
	})
	if err := storeA.EnsureBackgroundNotification(meta.ID, blocked); err != nil {
		t.Fatalf("ensure blocked: %v", err)
	}
	staleSnapshot, err := storeA.LoadBackgroundNotifications(meta.ID)
	if err != nil {
		t.Fatalf("load stale snapshot: %v", err)
	}
	staleSnapshot[0].DeliveryStatus = BackgroundNotificationAccepted

	completed := NewBackgroundNotification(QueueJob{
		ID:            "job_background_race",
		Status:        QueueStatusCompleted,
		SessionID:     "child_background_race",
		SessionStatus: StatusCompleted,
		FinalText:     "child completed while parent accepted blocked result",
	})
	if err := storeB.EnsureBackgroundNotification(meta.ID, completed); err != nil {
		t.Fatalf("ensure completed: %v", err)
	}
	if err := storeA.UpdateBackgroundNotifications(meta.ID, staleSnapshot); err != nil {
		t.Fatalf("update stale accepted snapshot: %v", err)
	}

	loaded, err := storeA.LoadBackgroundNotifications(meta.ID)
	if err != nil {
		t.Fatalf("load merged notification: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected one notification, got %#v", loaded)
	}
	if loaded[0].Status != QueueStatusCompleted || loaded[0].SessionStatus != StatusCompleted || loaded[0].FinalText != "child completed while parent accepted blocked result" {
		t.Fatalf("expected concurrent completed facts to survive stale accepted update, got %#v", loaded[0])
	}
	if loaded[0].LastError != "" {
		t.Fatalf("expected concurrent completed refresh to clear stale resumable error, got %#v", loaded[0])
	}
	if loaded[0].DeliveryStatus != BackgroundNotificationPending {
		t.Fatalf("expected completed facts to remain pending for redelivery, got %#v", loaded[0])
	}
}

func TestRestorePendingBackgroundNotificationsPreservesOtherFacts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	storeA := NewStore(root)
	storeB := NewStore(root)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := storeA.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create: %v", err)
	}

	alreadyAccepted := NewBackgroundNotification(QueueJob{
		ID:            "job_background_already_accepted",
		Status:        QueueStatusCompleted,
		SessionID:     "child_background_already_accepted",
		SessionStatus: StatusCompleted,
		FinalText:     "previously accepted",
	})
	alreadyAccepted.DeliveryStatus = BackgroundNotificationAccepted
	if err := storeA.AppendBackgroundNotification(meta.ID, alreadyAccepted); err != nil {
		t.Fatalf("append already accepted: %v", err)
	}
	pending := NewBackgroundNotification(QueueJob{
		ID:            "job_background_retry",
		Status:        QueueStatusCompleted,
		SessionID:     "child_background_retry",
		SessionStatus: StatusCompleted,
		FinalText:     "needs retry",
	})
	if err := storeA.AppendBackgroundNotification(meta.ID, pending); err != nil {
		t.Fatalf("append pending: %v", err)
	}
	rollback, err := storeA.LoadBackgroundNotifications(meta.ID)
	if err != nil {
		t.Fatalf("load rollback snapshot: %v", err)
	}
	for i := range rollback {
		if rollback[i].DeliveryStatus == BackgroundNotificationPending {
			rollback[i].DeliveryStatus = BackgroundNotificationAccepted
		}
	}
	if err := storeA.UpdateBackgroundNotifications(meta.ID, rollback); err != nil {
		t.Fatalf("mark accepted: %v", err)
	}

	concurrent := NewBackgroundNotification(QueueJob{
		ID:            "job_background_concurrent",
		Status:        QueueStatusCompleted,
		SessionID:     "child_background_concurrent",
		SessionStatus: StatusCompleted,
		FinalText:     "arrived while rolling back",
	})
	if err := storeB.AppendBackgroundNotification(meta.ID, concurrent); err != nil {
		t.Fatalf("append concurrent: %v", err)
	}
	if err := storeA.RestorePendingBackgroundNotifications(meta.ID, []BackgroundNotification{pending}); err != nil {
		t.Fatalf("restore pending: %v", err)
	}

	loaded, err := storeA.LoadBackgroundNotifications(meta.ID)
	if err != nil {
		t.Fatalf("load restored: %v", err)
	}
	statusByJob := map[string]string{}
	for _, notification := range loaded {
		statusByJob[notification.QueueJobID] = notification.DeliveryStatus
	}
	if statusByJob["job_background_retry"] != BackgroundNotificationPending {
		t.Fatalf("expected failed acceptance notification to return pending, got %#v", loaded)
	}
	if statusByJob["job_background_already_accepted"] != BackgroundNotificationAccepted {
		t.Fatalf("expected older accepted notification to remain accepted, got %#v", loaded)
	}
	if statusByJob["job_background_concurrent"] != BackgroundNotificationPending {
		t.Fatalf("expected concurrent notification to survive as pending, got %#v", loaded)
	}
}

func TestLoadBackgroundNotificationsRejectsMalformedSnapshot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create: %v", err)
	}
	malformed := NewBackgroundNotification(QueueJob{
		ID:            "../bad",
		Status:        QueueStatusCompleted,
		SessionID:     "child_background_malformed",
		SessionStatus: StatusCompleted,
	})
	backgroundPath := filepath.Join(store.SessionDir(meta.ID), "control", "background.jsonl")
	if err := store.writeJSONL(backgroundPath, []BackgroundNotification{malformed}); err != nil {
		t.Fatalf("write malformed background queue: %v", err)
	}

	if _, err := store.LoadBackgroundNotifications(meta.ID); err == nil || !strings.Contains(err.Error(), "validate background.jsonl") || !strings.Contains(err.Error(), "path separators") {
		t.Fatalf("expected malformed background.jsonl validation error, got %v", err)
	}
	if _, err := store.PendingBackgroundNotifications(meta.ID); err == nil || !strings.Contains(err.Error(), "validate background.jsonl") {
		t.Fatalf("expected pending background load to reject malformed background.jsonl, got %v", err)
	}

	invalidCreatedAt := malformed
	invalidCreatedAt.ID = "background_bad_time"
	invalidCreatedAt.QueueJobID = "job_background_bad_time"
	invalidCreatedAt.CreatedAt = "not-a-time"
	if err := store.writeJSONL(backgroundPath, []BackgroundNotification{invalidCreatedAt}); err != nil {
		t.Fatalf("write invalid-time background queue: %v", err)
	}
	if _, err := store.LoadBackgroundNotifications(meta.ID); err == nil || !strings.Contains(err.Error(), "validate background.jsonl") || !strings.Contains(err.Error(), "created_at must be RFC3339Nano") {
		t.Fatalf("expected invalid background notification timestamp validation error, got %v", err)
	}

	invalidRole := NewBackgroundNotification(QueueJob{
		ID:            "job_background_bad_role",
		Status:        QueueStatusCompleted,
		SessionID:     "child_background_bad_role",
		SessionStatus: StatusCompleted,
		AgentRole:     "assistant",
	})
	if err := store.writeJSONL(backgroundPath, []BackgroundNotification{invalidRole}); err != nil {
		t.Fatalf("write invalid-role background queue: %v", err)
	}
	if _, err := store.LoadBackgroundNotifications(meta.ID); err == nil || !strings.Contains(err.Error(), "validate background.jsonl") || !strings.Contains(err.Error(), "invalid background notification agent_role") {
		t.Fatalf("expected invalid background notification agent_role validation error, got %v", err)
	}

	runningResult := NewBackgroundNotification(QueueJob{
		ID:            "job_background_running_result",
		Status:        QueueStatusRunning,
		SessionID:     "child_background_running_result",
		SessionStatus: StatusRunning,
	})
	if err := store.writeJSONL(backgroundPath, []BackgroundNotification{runningResult}); err != nil {
		t.Fatalf("write running background result: %v", err)
	}
	if _, err := store.LoadBackgroundNotifications(meta.ID); err == nil || !strings.Contains(err.Error(), "validate background.jsonl") || !strings.Contains(err.Error(), "background notification status must be blocked, completed, or failed") {
		t.Fatalf("expected running background result validation error, got %v", err)
	}

	completedWithFailedSession := NewBackgroundNotification(QueueJob{
		ID:            "job_background_completed_failed_session",
		Status:        QueueStatusCompleted,
		SessionID:     "child_background_completed_failed_session",
		SessionStatus: StatusFailed,
	})
	if err := store.writeJSONL(backgroundPath, []BackgroundNotification{completedWithFailedSession}); err != nil {
		t.Fatalf("write completed notification with failed session: %v", err)
	}
	if _, err := store.LoadBackgroundNotifications(meta.ID); err == nil || !strings.Contains(err.Error(), "validate background.jsonl") || !strings.Contains(err.Error(), "completed background notification session_status must be completed") {
		t.Fatalf("expected completed/session_status mismatch validation error, got %v", err)
	}

	failedWithCompletedSession := NewBackgroundNotification(QueueJob{
		ID:            "job_background_failed_completed_session",
		Status:        QueueStatusFailed,
		SessionID:     "child_background_failed_completed_session",
		SessionStatus: StatusCompleted,
	})
	if err := store.writeJSONL(backgroundPath, []BackgroundNotification{failedWithCompletedSession}); err != nil {
		t.Fatalf("write failed notification with completed session and no error: %v", err)
	}
	if _, err := store.LoadBackgroundNotifications(meta.ID); err == nil || !strings.Contains(err.Error(), "validate background.jsonl") || !strings.Contains(err.Error(), "failed background notification session_status must be failed unless last_error is set") {
		t.Fatalf("expected failed/session_status mismatch validation error, got %v", err)
	}

	blockedWithTerminalSession := NewBackgroundNotification(QueueJob{
		ID:            "job_background_blocked_completed_session",
		Status:        QueueStatusBlocked,
		SessionID:     "child_background_blocked_completed_session",
		SessionStatus: StatusCompleted,
	})
	if err := store.writeJSONL(backgroundPath, []BackgroundNotification{blockedWithTerminalSession}); err != nil {
		t.Fatalf("write blocked notification with terminal session: %v", err)
	}
	if _, err := store.LoadBackgroundNotifications(meta.ID); err == nil || !strings.Contains(err.Error(), "validate background.jsonl") || !strings.Contains(err.Error(), "blocked background notification session_status must be awaiting_input or paused") {
		t.Fatalf("expected blocked/session_status mismatch validation error, got %v", err)
	}
}

func TestBackgroundNotificationWritesRejectMalformedFacts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create: %v", err)
	}

	blankQueueJob := NewBackgroundNotification(QueueJob{
		Status:        QueueStatusCompleted,
		SessionID:     "child_background_blank_job",
		SessionStatus: StatusCompleted,
	})
	if err := store.AppendBackgroundNotification(meta.ID, blankQueueJob); err == nil || !strings.Contains(err.Error(), "background notification queue_job_id is required") {
		t.Fatalf("expected append to reject missing queue job id, got %v", err)
	}
	invalidCreatedAt := NewBackgroundNotification(QueueJob{
		ID:            "job_background_bad_time",
		Status:        QueueStatusCompleted,
		SessionID:     "child_background_bad_time",
		SessionStatus: StatusCompleted,
	})
	invalidCreatedAt.CreatedAt = "not-a-time"
	if err := store.AppendBackgroundNotification(meta.ID, invalidCreatedAt); err == nil || !strings.Contains(err.Error(), "created_at must be RFC3339Nano") {
		t.Fatalf("expected append to reject invalid background timestamp, got %v", err)
	}
	invalidRole := NewBackgroundNotification(QueueJob{
		ID:            "job_background_bad_role",
		Status:        QueueStatusCompleted,
		SessionID:     "child_background_bad_role",
		SessionStatus: StatusCompleted,
		AgentRole:     "assistant",
	})
	if err := store.AppendBackgroundNotification(meta.ID, invalidRole); err == nil || !strings.Contains(err.Error(), "invalid background notification agent_role") {
		t.Fatalf("expected append to reject invalid background role, got %v", err)
	}

	valid := NewBackgroundNotification(QueueJob{
		ID:            "job_background_valid",
		Status:        QueueStatusCompleted,
		SessionID:     "child_background_valid",
		SessionStatus: StatusCompleted,
		AgentRole:     "evaluator",
		VisiblePaths:  []string{"reports/result.md"},
	})
	if err := store.AppendBackgroundNotification(meta.ID, valid); err != nil {
		t.Fatalf("append valid background notification: %v", err)
	}
	invalidStatus := valid
	invalidStatus.DeliveryStatus = "done"
	if err := store.UpdateBackgroundNotifications(meta.ID, []BackgroundNotification{invalidStatus}); err == nil || !strings.Contains(err.Error(), "invalid background notification delivery_status") {
		t.Fatalf("expected update to reject invalid delivery status, got %v", err)
	}
	invalidVisiblePath := NewBackgroundNotification(QueueJob{
		ID:            "job_background_bad_visible_path",
		Status:        QueueStatusCompleted,
		SessionID:     "child_background_bad_visible_path",
		SessionStatus: StatusCompleted,
		VisiblePaths:  []string{"../secrets.txt"},
	})
	if err := store.AppendBackgroundNotification(meta.ID, invalidVisiblePath); err == nil || !strings.Contains(err.Error(), "visible_paths") {
		t.Fatalf("expected append to reject invalid visible path, got %v", err)
	}
	runningResult := NewBackgroundNotification(QueueJob{
		ID:            "job_background_running",
		Status:        QueueStatusRunning,
		SessionID:     "child_background_running",
		SessionStatus: StatusRunning,
	})
	if err := store.AppendBackgroundNotification(meta.ID, runningResult); err == nil || !strings.Contains(err.Error(), "background notification status must be blocked, completed, or failed") {
		t.Fatalf("expected append to reject running background result, got %v", err)
	}
	completedWithFailedSession := NewBackgroundNotification(QueueJob{
		ID:            "job_background_completed_failed_session",
		Status:        QueueStatusCompleted,
		SessionID:     "child_background_completed_failed_session",
		SessionStatus: StatusFailed,
	})
	if err := store.AppendBackgroundNotification(meta.ID, completedWithFailedSession); err == nil || !strings.Contains(err.Error(), "completed background notification session_status must be completed") {
		t.Fatalf("expected append to reject completed/session_status mismatch, got %v", err)
	}
	failedWithCompletedSession := NewBackgroundNotification(QueueJob{
		ID:            "job_background_failed_completed_session",
		Status:        QueueStatusFailed,
		SessionID:     "child_background_failed_completed_session",
		SessionStatus: StatusCompleted,
	})
	if err := store.AppendBackgroundNotification(meta.ID, failedWithCompletedSession); err == nil || !strings.Contains(err.Error(), "failed background notification session_status must be failed unless last_error is set") {
		t.Fatalf("expected append to reject failed/session_status mismatch without last_error, got %v", err)
	}
	blockedWithRunningSession := NewBackgroundNotification(QueueJob{
		ID:            "job_background_blocked_running_session",
		Status:        QueueStatusBlocked,
		SessionID:     "child_background_blocked_running_session",
		SessionStatus: StatusRunning,
	})
	if err := store.AppendBackgroundNotification(meta.ID, blockedWithRunningSession); err == nil || !strings.Contains(err.Error(), "blocked background notification session_status must be awaiting_input or paused") {
		t.Fatalf("expected append to reject blocked/running session mismatch, got %v", err)
	}
	failedAfterChildCompleted := NewBackgroundNotification(QueueJob{
		ID:            "job_background_failed_after_child_completed",
		Status:        QueueStatusFailed,
		SessionID:     "child_background_failed_after_child_completed",
		SessionStatus: StatusCompleted,
		LastError:     "queue handoff failed after child completed",
	})
	if err := store.AppendBackgroundNotification(meta.ID, failedAfterChildCompleted); err != nil {
		t.Fatalf("append failed notification with completed child and explicit queue error: %v", err)
	}
	duplicate := NewBackgroundNotification(QueueJob{
		ID:            valid.QueueJobID,
		Status:        QueueStatusCompleted,
		SessionID:     "child_background_duplicate",
		SessionStatus: StatusCompleted,
	})
	if err := store.AppendBackgroundNotification(meta.ID, duplicate); err == nil || !strings.Contains(err.Error(), "duplicate background notification queue_job_id") {
		t.Fatalf("expected append to reject duplicate queue job id, got %v", err)
	}
	loaded, err := store.LoadBackgroundNotifications(meta.ID)
	if err != nil {
		t.Fatalf("load background notifications: %v", err)
	}
	if len(loaded) != 2 ||
		loaded[0].QueueJobID != valid.QueueJobID ||
		loaded[0].DeliveryStatus != BackgroundNotificationPending ||
		loaded[0].AgentRole != "evaluator" ||
		loaded[1].QueueJobID != failedAfterChildCompleted.QueueJobID ||
		loaded[1].DeliveryStatus != BackgroundNotificationPending {
		t.Fatalf("malformed background notification write changed durable queue: %#v", loaded)
	}
}

func TestLoadBackgroundNotificationsTailAllowsJoblessNotifications(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create: %v", err)
	}
	queueResult := NewBackgroundNotification(QueueJob{
		ID:            "job_background_tail",
		Status:        QueueStatusCompleted,
		SessionID:     "child_background_tail",
		SessionStatus: StatusCompleted,
	})
	if err := store.AppendBackgroundNotification(meta.ID, queueResult); err != nil {
		t.Fatalf("append queue notification: %v", err)
	}
	firstDeadlock := NewCoordinationDeadlockNotification(meta.ID, "parent coordination deadlock: first")
	firstDeadlock.DeliveryStatus = BackgroundNotificationAccepted
	if err := store.AppendBackgroundNotification(meta.ID, firstDeadlock); err != nil {
		t.Fatalf("append first jobless notification: %v", err)
	}
	secondDeadlock := NewCoordinationDeadlockNotification(meta.ID, "parent coordination deadlock: second")
	secondDeadlock.DeliveryStatus = BackgroundNotificationAccepted
	if err := store.AppendBackgroundNotification(meta.ID, secondDeadlock); err != nil {
		t.Fatalf("append second jobless notification: %v", err)
	}

	tail, hasMore, err := store.LoadBackgroundNotificationsTail(meta.ID, 10)
	if err != nil {
		t.Fatalf("load background tail: %v", err)
	}
	if hasMore {
		t.Fatalf("did not expect tail truncation")
	}
	if len(tail) != 3 {
		t.Fatalf("expected all notifications in tail, got %#v", tail)
	}
	jobless := 0
	for _, notification := range tail {
		if notification.QueueJobID == "" {
			jobless++
		}
	}
	if jobless != 2 {
		t.Fatalf("expected two jobless notifications, got %d in %#v", jobless, tail)
	}
}

func TestAppendSteerRequestRejectsSymlinkLockFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.lock")
	lockPath := filepath.Join(store.SessionDir(meta.ID), "control", "steer.lock")
	if err := os.Symlink(outside, lockPath); err != nil {
		t.Fatalf("symlink lock: %v", err)
	}

	if err := store.AppendSteerRequest(meta.ID, NewSteerRequest("hello", false)); err == nil {
		t.Fatal("expected symlinked steer lock to fail")
	}
	if _, err := os.Stat(outside); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside lock target should not be created, got %v", err)
	}
}

func TestAppendBackgroundNotificationRejectsSymlinkLockFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.lock")
	lockPath := filepath.Join(store.SessionDir(meta.ID), "control", "background.lock")
	if err := os.Symlink(outside, lockPath); err != nil {
		t.Fatalf("symlink lock: %v", err)
	}

	notification := NewBackgroundNotification(QueueJob{ID: "job_background_lock", Status: QueueStatusCompleted})
	if err := store.AppendBackgroundNotification(meta.ID, notification); err == nil {
		t.Fatal("expected symlinked background lock to fail")
	}
	if _, err := os.Stat(outside); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside lock target should not be created, got %v", err)
	}
}

func TestAppendSteerRequestRejectsSymlinkControlDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create: %v", err)
	}
	controlDir := filepath.Join(store.SessionDir(meta.ID), "control")
	if err := os.RemoveAll(controlDir); err != nil {
		t.Fatalf("remove control dir: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, controlDir); err != nil {
		t.Fatalf("symlink control dir: %v", err)
	}

	if err := store.AppendSteerRequest(meta.ID, NewSteerRequest("hello", false)); err == nil {
		t.Fatal("expected symlinked control dir to fail")
	}
	if _, err := os.Stat(filepath.Join(outside, "steer.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside control target should not receive steer.jsonl, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "steer.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside control target should not receive steer.lock, got %v", err)
	}
}

func TestStoreListIncludesLastErrorInSummaries(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
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
	state := State{
		Status:    StatusFailed,
		Phase:     "provider_call",
		LastError: "auth_unavailable: no auth available",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	items, err := store.List(10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one summary, got %#v", items)
	}
	if items[0].LastError != state.LastError {
		t.Fatalf("expected last_error to be preserved, got %#v", items[0])
	}
}

func TestStoreClaimNextQueuedJobIsAtomicAcrossStores(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	storeA := NewStore(root)
	storeB := NewStore(root)
	job := QueueJob{
		SchemaVersion: 1,
		ID:            "job_atomic",
		Status:        QueueStatusQueued,
		Prompt:        "do work",
		Mode:          ModeExec,
		Background:    true,
	}
	if err := storeA.EnqueueJob(job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	type claimResult struct {
		job QueueJob
		ok  bool
		err error
	}
	start := make(chan struct{})
	results := make(chan claimResult, 2)
	var wg sync.WaitGroup
	for _, store := range []*Store{storeA, storeB} {
		wg.Add(1)
		go func(store *Store) {
			defer wg.Done()
			<-start
			job, ok, err := store.ClaimNextQueuedJob()
			results <- claimResult{job: job, ok: ok, err: err}
		}(store)
	}
	close(start)
	wg.Wait()
	close(results)

	var claimed []claimResult
	for result := range results {
		if result.err != nil {
			t.Fatalf("claim job: %v", result.err)
		}
		if result.ok {
			claimed = append(claimed, result)
		}
	}
	if len(claimed) != 1 {
		t.Fatalf("expected exactly one claimant, got %d", len(claimed))
	}
	if claimed[0].job.ID != job.ID || claimed[0].job.Status != QueueStatusRunning {
		t.Fatalf("unexpected claimed job: %#v", claimed[0].job)
	}
	loaded, err := storeA.LoadJob(job.ID)
	if err != nil {
		t.Fatalf("load claimed job: %v", err)
	}
	if loaded.Status != QueueStatusRunning {
		t.Fatalf("expected running persisted status, got %#v", loaded)
	}
}

func TestClaimNextQueuedJobWritesLease(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	job := QueueJob{
		SchemaVersion: 1,
		ID:            "job_lease",
		Status:        QueueStatusQueued,
		Prompt:        "do work",
		Mode:          ModeExec,
		Background:    true,
	}
	if err := store.EnqueueJob(job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	claimed, ok, err := store.ClaimNextQueuedJob()
	if err != nil {
		t.Fatalf("claim job: %v", err)
	}
	if !ok {
		t.Fatal("expected queued job to be claimed")
	}
	if claimed.Status != QueueStatusRunning || claimed.ClaimedBy == "" || claimed.ClaimedAt == "" || claimed.HeartbeatAt == "" || claimed.WorkerPID == 0 || claimed.ProcessStartID == "" {
		t.Fatalf("expected running lease fields, got %#v", claimed)
	}
	if claimed.HeartbeatAt != claimed.ClaimedAt {
		t.Fatalf("expected initial heartbeat to match claimed_at, got %#v", claimed)
	}
}

func TestStopQueuedJobMovesQueuedJobToFailed(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	job := QueueJob{
		SchemaVersion: 1,
		ID:            "job_stop_queued",
		Status:        QueueStatusQueued,
		Prompt:        "do work",
		Mode:          ModeExec,
		Background:    true,
	}
	if err := store.EnqueueJob(job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	stopped, err := store.StopQueuedJob(job.ID, "", "stopped before claim")
	if err != nil {
		t.Fatalf("stop queued job: %v", err)
	}
	if stopped.Status != QueueStatusFailed || stopped.LastError != "stopped before claim" || stopped.StopReason != QueueStopReasonAgentStop {
		t.Fatalf("expected failed stopped job, got %#v", stopped)
	}
	if _, err := os.Stat(store.queueJobPath(QueueStatusQueued, job.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected queued job to be removed, got %v", err)
	}
	loaded, err := store.LoadJob(job.ID)
	if err != nil {
		t.Fatalf("load stopped job: %v", err)
	}
	if loaded.Status != QueueStatusFailed || loaded.LastError != stopped.LastError || loaded.StopReason != QueueStopReasonAgentStop {
		t.Fatalf("expected stopped failed job to load, got %#v", loaded)
	}
}

func TestBlockQueuedJobForParentStopIsRequeueable(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	job := QueueJob{
		SchemaVersion: 1,
		ID:            "job_parent_stop_queued",
		Status:        QueueStatusQueued,
		Prompt:        "do parent-stopped work",
		Mode:          ModeExec,
		Background:    true,
	}
	if err := store.EnqueueJob(job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	blocked, err := store.BlockQueuedJobForParentStop(job.ID, "")
	if err != nil {
		t.Fatalf("block queued job: %v", err)
	}
	if blocked.Status != QueueStatusBlocked || blocked.StopReason != QueueStopReasonParentStop || blocked.LastError == "" {
		t.Fatalf("expected parent-stopped blocked job, got %#v", blocked)
	}
	if _, err := os.Stat(store.queueJobPath(QueueStatusQueued, job.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected queued job to be removed, got %v", err)
	}
	if _, err := os.Stat(store.queueJobPath(QueueStatusBlocked, job.ID)); err != nil {
		t.Fatalf("expected blocked job copy: %v", err)
	}

	requeued, err := store.RequeueParentStoppedJob(job.ID, "", "finish from current evidence")
	if err != nil {
		t.Fatalf("requeue parent-stopped job: %v", err)
	}
	if requeued.Status != QueueStatusQueued || requeued.StopReason != "" || requeued.LastError != "" {
		t.Fatalf("expected clean requeued job, got %#v", requeued)
	}
	if !strings.Contains(requeued.Prompt, "Parent restart prompt") || !strings.Contains(requeued.Prompt, "finish from current evidence") {
		t.Fatalf("expected requeue prompt to include restart prompt, got %q", requeued.Prompt)
	}
	claimed, ok, err := store.ClaimNextQueuedJob()
	if err != nil || !ok {
		t.Fatalf("claim requeued job: ok=%v err=%v", ok, err)
	}
	if claimed.ID != job.ID || claimed.Status != QueueStatusRunning {
		t.Fatalf("unexpected claimed requeued job: %#v", claimed)
	}
}

func TestStopQueuedJobRejectsAlreadyClaimedJobAndPreservesRunning(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	job := QueueJob{
		SchemaVersion: 1,
		ID:            "job_stop_claimed",
		Status:        QueueStatusQueued,
		Prompt:        "do work",
		Mode:          ModeExec,
		Background:    true,
	}
	if err := store.EnqueueJob(job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	claimed, ok, err := store.ClaimNextQueuedJob()
	if err != nil || !ok {
		t.Fatalf("claim queued job: ok=%v err=%v", ok, err)
	}

	stopped, err := store.StopQueuedJob(job.ID, "", "stopped too late")
	if err == nil {
		t.Fatalf("expected claimed job stop rejection, got %#v", stopped)
	}
	if !strings.Contains(err.Error(), "cannot be safely stopped") {
		t.Fatalf("expected safe stop boundary error, got %v", err)
	}
	if _, err := os.Stat(store.queueJobPath(QueueStatusFailed, job.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("claimed job should not get a failed stop copy, got %v", err)
	}
	loaded, err := store.LoadJob(job.ID)
	if err != nil {
		t.Fatalf("load claimed job: %v", err)
	}
	if loaded.Status != QueueStatusRunning || loaded.ClaimedAt != claimed.ClaimedAt {
		t.Fatalf("expected claimed running job to be preserved, got %#v", loaded)
	}
}

func TestClaimNextQueuedJobRollsBackWhenLeaseWriteFails(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	job := QueueJob{
		SchemaVersion: 1,
		ID:            "job_claim_write_failure",
		Status:        QueueStatusQueued,
		Prompt:        "do work",
		Mode:          ModeExec,
		Background:    true,
	}
	if err := store.EnqueueJob(job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	forced := errors.New("forced lease write failure")
	store.beforeQueueClaimLeaseWrite = func(from, to string, claimed QueueJob) error {
		if _, err := os.Stat(from); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected queued source to be renamed before lease write, got %v", err)
		}
		if _, err := os.Stat(to); err != nil {
			t.Fatalf("expected running target to exist before forced lease write failure: %v", err)
		}
		if claimed.Status != QueueStatusRunning || claimed.ClaimedAt == "" || claimed.HeartbeatAt == "" {
			t.Fatalf("expected claimed lease facts before write, got %#v", claimed)
		}
		return forced
	}

	claimed, ok, err := store.ClaimNextQueuedJob()
	if !errors.Is(err, forced) || ok || claimed.ID != "" {
		t.Fatalf("expected forced claim error without returned job, got job=%#v ok=%v err=%v", claimed, ok, err)
	}
	if _, err := os.Stat(store.queueJobPath(QueueStatusRunning, job.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected failed claim to remove running copy, got %v", err)
	}
	var restored QueueJob
	if err := readJSONFile(store.queueJobPath(QueueStatusQueued, job.ID), &restored); err != nil {
		t.Fatalf("expected failed claim to restore queued job: %v", err)
	}
	if restored.Status != QueueStatusQueued || restored.ClaimedAt != "" || restored.HeartbeatAt != "" || restored.ClaimedBy != "" || restored.WorkerPID != 0 || restored.ProcessStartID != "" {
		t.Fatalf("expected restored queued job without lease facts, got %#v", restored)
	}
}

func TestClaimNextQueuedJobRejectsSymlinkedRunningDirDuringRename(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)
	job := QueueJob{
		SchemaVersion: 1,
		ID:            "job_symlink_running_claim",
		Status:        QueueStatusQueued,
		Prompt:        "do work",
		Mode:          ModeExec,
		Background:    true,
	}
	if err := store.EnqueueJob(job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	outsideDir := t.TempDir()
	store.beforeQueueClaimRename = func(from, to string, queued QueueJob) error {
		if queued.ID != job.ID {
			t.Fatalf("unexpected queued job before rename: %#v", queued)
		}
		if _, err := os.Stat(from); err != nil {
			t.Fatalf("expected queued source before rename: %v", err)
		}
		runningDir := filepath.Dir(to)
		if err := os.RemoveAll(runningDir); err != nil {
			t.Fatalf("remove running dir before rename: %v", err)
		}
		if err := os.Symlink(outsideDir, runningDir); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		return nil
	}

	claimed, ok, err := store.ClaimNextQueuedJob()
	if err == nil || ok || claimed.ID != "" {
		t.Fatalf("expected claim to reject symlinked running dir, got job=%#v ok=%v err=%v", claimed, ok, err)
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink path error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(outsideDir, job.ID+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside running target should not receive claimed job, got %v", err)
	}
	if _, err := os.Stat(store.queueJobPath(QueueStatusQueued, job.ID)); err != nil {
		t.Fatalf("queued job should remain after rejected claim: %v", err)
	}
	if info, statErr := os.Lstat(store.queueStatusDir(QueueStatusRunning)); statErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("running symlink should remain for diagnostics, info=%v err=%v", info, statErr)
	}
}

func TestClaimNextQueuedJobSkipsMismatchedQueueFilename(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	if err := store.ensureQueueDirs(); err != nil {
		t.Fatalf("ensure queue dirs: %v", err)
	}
	mismatched := QueueJob{
		SchemaVersion: 1,
		ID:            "job_actual",
		Status:        QueueStatusQueued,
		Prompt:        "do work",
		Mode:          ModeExec,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.writeJSONFile(filepath.Join(store.queueStatusDir(QueueStatusQueued), "job_other.json"), mismatched); err != nil {
		t.Fatalf("write mismatched job: %v", err)
	}

	claimed, ok, err := store.ClaimNextQueuedJob()
	if err != nil {
		t.Fatalf("claim job: %v", err)
	}
	if ok {
		t.Fatalf("expected mismatched queue file to be skipped, got %#v", claimed)
	}
	if _, err := os.Stat(filepath.Join(store.queueStatusDir(QueueStatusQueued), "job_other.json")); err != nil {
		t.Fatalf("expected mismatched job to remain queued for diagnostics, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.queueStatusDir(QueueStatusRunning), "job_other.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected mismatched job not to move to running, got %v", err)
	}
}

func TestClaimNextQueuedJobReportsCorruptQueuedJob(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	job := QueueJob{
		SchemaVersion: 1,
		ID:            "job_corrupt_claim",
		Status:        QueueStatusQueued,
		Prompt:        "do work",
		Mode:          ModeExec,
		Background:    true,
	}
	if err := store.EnqueueJob(job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	if err := os.WriteFile(store.queueJobPath(QueueStatusQueued, job.ID), []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatalf("corrupt queued job: %v", err)
	}

	claimed, ok, err := store.ClaimNextQueuedJob()
	if err == nil || !strings.Contains(err.Error(), "job_corrupt_claim.json") {
		t.Fatalf("expected corrupt queued job error, got job=%#v ok=%v err=%v", claimed, ok, err)
	}
	if _, statErr := os.Stat(store.queueJobPath(QueueStatusQueued, job.ID)); statErr != nil {
		t.Fatalf("expected corrupt job to remain queued, got %v", statErr)
	}
}

func TestLoadJobsRejectMalformedQueueJobSnapshot(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	valid := QueueJob{
		SchemaVersion: 1,
		ID:            "job_valid_after_bad",
		CreatedAt:     now,
		UpdatedAt:     now,
		Status:        QueueStatusQueued,
		Prompt:        "do valid work",
		Mode:          ModeExec,
		Background:    true,
	}
	if err := store.SaveJob(valid); err != nil {
		t.Fatalf("save valid queue job: %v", err)
	}
	malformed := valid
	malformed.ID = "job_bad_snapshot"
	malformed.Prompt = ""
	if err := store.writeJSONFile(store.queueJobPath(QueueStatusQueued, malformed.ID), malformed); err != nil {
		t.Fatalf("write malformed queue job: %v", err)
	}

	if _, err := store.LoadJob(malformed.ID); err == nil || !strings.Contains(err.Error(), "queue job prompt is required") {
		t.Fatalf("expected malformed queue job load error, got %v", err)
	}
	if _, err := store.ListJobs(10); err == nil || !strings.Contains(err.Error(), "job_bad_snapshot.json") || !strings.Contains(err.Error(), "queue job prompt is required") {
		t.Fatalf("expected malformed queue job list error, got %v", err)
	}

	malformed.Prompt = "do malformed work"
	malformed.CreatedAt = "not-a-time"
	malformed.UpdatedAt = now
	if err := store.writeJSONFile(store.queueJobPath(QueueStatusQueued, malformed.ID), malformed); err != nil {
		t.Fatalf("write invalid-time queue job: %v", err)
	}
	if _, err := store.LoadJob(malformed.ID); err == nil || !strings.Contains(err.Error(), "created_at must be RFC3339Nano") {
		t.Fatalf("expected invalid created_at queue job load error, got %v", err)
	}

	malformed.CreatedAt = now
	malformed.UpdatedAt = "not-a-time"
	if err := store.writeJSONFile(store.queueJobPath(QueueStatusQueued, malformed.ID), malformed); err != nil {
		t.Fatalf("write invalid-updated queue job: %v", err)
	}
	if _, err := store.ListJobs(10); err == nil || !strings.Contains(err.Error(), "job_bad_snapshot.json") || !strings.Contains(err.Error(), "updated_at must be RFC3339Nano") {
		t.Fatalf("expected invalid updated_at queue job list error, got %v", err)
	}
	if err := store.DeleteJob(malformed.ID); err != nil {
		t.Fatalf("delete malformed queue job before semantic checks: %v", err)
	}

	completedWithFailedSession := valid
	completedWithFailedSession.ID = "job_completed_failed_session"
	completedWithFailedSession.Status = QueueStatusCompleted
	completedWithFailedSession.SessionID = "child_completed_failed_session"
	completedWithFailedSession.SessionStatus = StatusFailed
	completedWithFailedSession.LastError = "completed queue job should not carry failed child status"
	completedWithFailedSession.CreatedAt = now
	completedWithFailedSession.UpdatedAt = now
	if err := store.writeJSONFile(store.queueJobPath(QueueStatusCompleted, completedWithFailedSession.ID), completedWithFailedSession); err != nil {
		t.Fatalf("write completed queue job with failed session: %v", err)
	}
	if _, err := store.LoadJob(completedWithFailedSession.ID); err == nil || !strings.Contains(err.Error(), "completed queue job session_status must be completed") {
		t.Fatalf("expected completed/session_status mismatch queue job load error, got %v", err)
	}

	blockedWithTerminalSession := valid
	blockedWithTerminalSession.ID = "job_blocked_completed_session"
	blockedWithTerminalSession.Status = QueueStatusBlocked
	blockedWithTerminalSession.SessionID = "child_blocked_completed_session"
	blockedWithTerminalSession.SessionStatus = StatusCompleted
	blockedWithTerminalSession.CreatedAt = now
	blockedWithTerminalSession.UpdatedAt = now
	if err := store.writeJSONFile(store.queueJobPath(QueueStatusBlocked, blockedWithTerminalSession.ID), blockedWithTerminalSession); err != nil {
		t.Fatalf("write blocked queue job with completed session: %v", err)
	}
	if _, err := store.ListJobs(10); err == nil || !strings.Contains(err.Error(), "job_blocked_completed_session.json") || !strings.Contains(err.Error(), "blocked queue job session_status must be awaiting_input or paused") {
		t.Fatalf("expected blocked/session_status mismatch queue job list error, got %v", err)
	}
	if err := store.DeleteJob(blockedWithTerminalSession.ID); err != nil {
		t.Fatalf("delete blocked/session mismatch queue job before status-dir check: %v", err)
	}

	statusDirMismatch := valid
	statusDirMismatch.ID = "job_status_dir_mismatch"
	statusDirMismatch.Status = QueueStatusQueued
	statusDirMismatch.CreatedAt = now
	statusDirMismatch.UpdatedAt = now
	if err := store.writeJSONFile(store.queueJobPath(QueueStatusRunning, statusDirMismatch.ID), statusDirMismatch); err != nil {
		t.Fatalf("write status-dir mismatch queue job: %v", err)
	}
	if _, err := store.LoadJob(statusDirMismatch.ID); err == nil || !strings.Contains(err.Error(), "status queued does not match queue directory running") {
		t.Fatalf("expected status-dir mismatch queue job load error, got %v", err)
	}
	if _, err := store.ListJobs(10); err == nil || !strings.Contains(err.Error(), "job_status_dir_mismatch.json") || !strings.Contains(err.Error(), "status queued does not match queue directory running") {
		t.Fatalf("expected status-dir mismatch queue job list error, got %v", err)
	}
	if err := store.DeleteJob(statusDirMismatch.ID); err != nil {
		t.Fatalf("delete status-dir mismatch queue job before valid load check: %v", err)
	}

	loaded, err := store.LoadJob(valid.ID)
	if err != nil {
		t.Fatalf("valid queue job should remain loadable: %v", err)
	}
	if loaded.ID != valid.ID || loaded.Prompt != valid.Prompt {
		t.Fatalf("unexpected valid queue job after malformed snapshot: %#v", loaded)
	}
}

func TestDeleteJobRejectsSymlinkedQueueStatusDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)
	queueRoot := filepath.Join(root, "_queue")
	if err := os.MkdirAll(queueRoot, 0o700); err != nil {
		t.Fatalf("mkdir queue root: %v", err)
	}
	jobID := "job_symlink_delete"
	outsideDir := t.TempDir()
	outsideJob := filepath.Join(outsideDir, jobID+".json")
	outsideData := []byte(`{"outside":true}` + "\n")
	if err := os.WriteFile(outsideJob, outsideData, 0o600); err != nil {
		t.Fatalf("write outside queue job: %v", err)
	}
	statusLink := filepath.Join(queueRoot, QueueStatusQueued)
	if err := os.Symlink(outsideDir, statusLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	err := store.DeleteJob(jobID)
	if err == nil {
		t.Fatal("expected symlinked queue status directory rejection")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink path error, got %v", err)
	}
	data, readErr := os.ReadFile(outsideJob)
	if readErr != nil {
		t.Fatalf("outside queue job should not be removed: %v", readErr)
	}
	if string(data) != string(outsideData) {
		t.Fatalf("outside queue job changed: %q", data)
	}
	if info, statErr := os.Lstat(statusLink); statErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("queue status symlink should remain for diagnostics, info=%v err=%v", info, statErr)
	}
}

func TestQueueJobFactsRejectMalformedParentRootTopology(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	validIndependent := QueueJob{
		SchemaVersion: 1,
		ID:            "job_valid_independent_topology",
		CreatedAt:     now,
		UpdatedAt:     now,
		Status:        QueueStatusQueued,
		Prompt:        "do independent work",
		Mode:          ModeExec,
		Background:    true,
	}
	if err := store.SaveJob(validIndependent); err != nil {
		t.Fatalf("save valid independent queue job: %v", err)
	}
	validParentLinked := validIndependent
	validParentLinked.ID = "job_valid_parent_topology"
	validParentLinked.ParentSessionID = "parent_queue_topology"
	validParentLinked.RootSessionID = "root_queue_topology"
	if err := store.SaveJob(validParentLinked); err != nil {
		t.Fatalf("save valid parent-linked queue job: %v", err)
	}

	parentWithoutRoot := validIndependent
	parentWithoutRoot.ID = "job_parent_without_root"
	parentWithoutRoot.ParentSessionID = "parent_queue_topology"
	if err := store.SaveJob(parentWithoutRoot); err == nil || !strings.Contains(err.Error(), "root_session_id is required for parent-linked jobs") {
		t.Fatalf("expected save to reject parent-linked job without root, got %v", err)
	}
	rootWithoutParent := validIndependent
	rootWithoutParent.ID = "job_root_without_parent"
	rootWithoutParent.RootSessionID = "root_queue_topology"
	if err := store.SaveJob(rootWithoutParent); err == nil || !strings.Contains(err.Error(), "root_session_id requires parent_session_id") {
		t.Fatalf("expected save to reject root-only queue job, got %v", err)
	}

	loadStore := NewStore(filepath.Join(t.TempDir(), "sessions"))
	if err := loadStore.ensureQueueDirs(); err != nil {
		t.Fatalf("ensure queue dirs: %v", err)
	}
	if err := loadStore.writeJSONFile(loadStore.queueJobPath(QueueStatusQueued, parentWithoutRoot.ID), parentWithoutRoot); err != nil {
		t.Fatalf("write malformed parent-only queue job: %v", err)
	}
	if _, err := loadStore.LoadJob(parentWithoutRoot.ID); err == nil || !strings.Contains(err.Error(), "root_session_id is required for parent-linked jobs") {
		t.Fatalf("expected load to reject parent-linked job without root, got %v", err)
	}
	if _, _, err := loadStore.ClaimNextQueuedJob(); err == nil || !strings.Contains(err.Error(), "root_session_id is required for parent-linked jobs") {
		t.Fatalf("expected claim to reject parent-linked job without root, got %v", err)
	}

	listStore := NewStore(filepath.Join(t.TempDir(), "sessions"))
	if err := listStore.ensureQueueDirs(); err != nil {
		t.Fatalf("ensure queue dirs: %v", err)
	}
	if err := listStore.writeJSONFile(listStore.queueJobPath(QueueStatusQueued, rootWithoutParent.ID), rootWithoutParent); err != nil {
		t.Fatalf("write malformed root-only queue job: %v", err)
	}
	if _, err := listStore.LoadJob(rootWithoutParent.ID); err == nil || !strings.Contains(err.Error(), "root_session_id requires parent_session_id") {
		t.Fatalf("expected load to reject root-only queue job, got %v", err)
	}
	if _, err := listStore.ListJobs(10); err == nil || !strings.Contains(err.Error(), "root_session_id requires parent_session_id") {
		t.Fatalf("expected list to reject root-only queue job, got %v", err)
	}
}

func TestQueueJobWritesRejectMalformedFacts(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	valid := QueueJob{
		SchemaVersion: 1,
		ID:            "job_valid_write",
		Status:        QueueStatusQueued,
		Prompt:        "do valid work",
		Mode:          ModeExec,
		Background:    true,
	}
	if err := store.SaveJob(valid); err != nil {
		t.Fatalf("save valid queue job: %v", err)
	}

	invalidPrompt := valid
	invalidPrompt.ID = "job_invalid_prompt"
	invalidPrompt.Prompt = " "
	if err := store.SaveJob(invalidPrompt); err == nil || !strings.Contains(err.Error(), "queue job prompt is required") {
		t.Fatalf("expected save to reject blank prompt, got %v", err)
	}
	invalidSessionStatus := valid
	invalidSessionStatus.ID = "job_invalid_session_status"
	invalidSessionStatus.SessionID = "child_invalid_status"
	invalidSessionStatus.SessionStatus = "done"
	if err := store.SaveJob(invalidSessionStatus); err == nil || !strings.Contains(err.Error(), "invalid queue job session_status") {
		t.Fatalf("expected save to reject invalid session status, got %v", err)
	}
	invalidRole := valid
	invalidRole.ID = "job_invalid_role"
	invalidRole.AgentRole = "assistant"
	if err := store.SaveJob(invalidRole); err == nil || !strings.Contains(err.Error(), "invalid queue job agent_role") {
		t.Fatalf("expected save to reject invalid agent role, got %v", err)
	}
	invalidVisiblePath := valid
	invalidVisiblePath.ID = "job_invalid_visible_path"
	invalidVisiblePath.VisiblePaths = []string{"../escape.md"}
	if err := store.SaveJob(invalidVisiblePath); err == nil || !strings.Contains(err.Error(), "queue job visible_paths") {
		t.Fatalf("expected save to reject invalid visible path, got %v", err)
	}
	invalidCreatedAt := valid
	invalidCreatedAt.ID = "job_invalid_created_at"
	invalidCreatedAt.CreatedAt = "not-a-time"
	if err := store.SaveJob(invalidCreatedAt); err == nil || !strings.Contains(err.Error(), "created_at must be RFC3339Nano") {
		t.Fatalf("expected save to reject invalid created_at, got %v", err)
	}
	invalidClaimedAt := valid
	invalidClaimedAt.ID = "job_invalid_claimed_at"
	invalidClaimedAt.ClaimedAt = "not-a-time"
	if err := store.SaveJob(invalidClaimedAt); err == nil || !strings.Contains(err.Error(), "claimed_at must be RFC3339Nano") {
		t.Fatalf("expected save to reject invalid claimed_at, got %v", err)
	}
	invalidHeartbeatAt := valid
	invalidHeartbeatAt.ID = "job_invalid_heartbeat_at"
	invalidHeartbeatAt.HeartbeatAt = "not-a-time"
	if err := store.SaveJob(invalidHeartbeatAt); err == nil || !strings.Contains(err.Error(), "heartbeat_at must be RFC3339Nano") {
		t.Fatalf("expected save to reject invalid heartbeat_at, got %v", err)
	}
	completedWithFailedSession := valid
	completedWithFailedSession.ID = "job_completed_failed_session"
	completedWithFailedSession.Status = QueueStatusCompleted
	completedWithFailedSession.SessionID = "child_completed_failed_session"
	completedWithFailedSession.SessionStatus = StatusFailed
	completedWithFailedSession.LastError = "completed queue job should not carry failed child status"
	if err := store.SaveJob(completedWithFailedSession); err == nil || !strings.Contains(err.Error(), "completed queue job session_status must be completed") {
		t.Fatalf("expected save to reject completed/session_status mismatch, got %v", err)
	}
	blockedWithRunningSession := valid
	blockedWithRunningSession.ID = "job_blocked_running_session"
	blockedWithRunningSession.Status = QueueStatusBlocked
	blockedWithRunningSession.SessionID = "child_blocked_running_session"
	blockedWithRunningSession.SessionStatus = StatusRunning
	if err := store.SaveJob(blockedWithRunningSession); err == nil || !strings.Contains(err.Error(), "blocked queue job session_status must be awaiting_input or paused") {
		t.Fatalf("expected save to reject blocked/running session mismatch, got %v", err)
	}
	failedAfterChildCompleted := valid
	failedAfterChildCompleted.ID = "job_failed_after_child_completed"
	failedAfterChildCompleted.Status = QueueStatusFailed
	failedAfterChildCompleted.SessionID = "child_failed_after_child_completed"
	failedAfterChildCompleted.SessionStatus = StatusCompleted
	failedAfterChildCompleted.LastError = "parent handoff failed after child completed"
	if err := store.SaveJob(failedAfterChildCompleted); err != nil {
		t.Fatalf("save failed queue job with completed child and explicit handoff error: %v", err)
	}

	loaded, err := store.LoadJob(valid.ID)
	if err != nil {
		t.Fatalf("load valid queue job: %v", err)
	}
	if loaded.ID != valid.ID || loaded.Prompt != valid.Prompt {
		t.Fatalf("malformed queue job write changed durable valid job: %#v", loaded)
	}
	for _, id := range []string{invalidPrompt.ID, invalidSessionStatus.ID, invalidRole.ID, invalidVisiblePath.ID, invalidCreatedAt.ID, invalidClaimedAt.ID, invalidHeartbeatAt.ID, completedWithFailedSession.ID, blockedWithRunningSession.ID} {
		if _, err := store.LoadJob(id); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("malformed queue job %s should not be persisted, got %v", id, err)
		}
	}
	if loadedFailed, err := store.LoadJob(failedAfterChildCompleted.ID); err != nil || loadedFailed.Status != QueueStatusFailed || loadedFailed.SessionStatus != StatusCompleted {
		t.Fatalf("valid failed queue job with completed child should remain loadable, got job=%#v err=%v", loadedFailed, err)
	}
}

func TestClaimNextQueuedJobRejectsMalformedQueuedJob(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	job := QueueJob{
		SchemaVersion: 1,
		ID:            "job_bad_claim_shape",
		CreatedAt:     now,
		UpdatedAt:     now,
		Status:        QueueStatusQueued,
		Prompt:        "",
		Mode:          ModeExec,
		Background:    true,
	}
	if err := store.ensureQueueDirs(); err != nil {
		t.Fatalf("ensure queue dirs: %v", err)
	}
	if err := store.writeJSONFile(store.queueJobPath(QueueStatusQueued, job.ID), job); err != nil {
		t.Fatalf("write malformed queued job: %v", err)
	}

	claimed, ok, err := store.ClaimNextQueuedJob()
	if err == nil || !strings.Contains(err.Error(), "job_bad_claim_shape.json") || !strings.Contains(err.Error(), "queue job prompt is required") {
		t.Fatalf("expected malformed queued job claim error, got job=%#v ok=%v err=%v", claimed, ok, err)
	}
	if _, err := os.Stat(store.queueJobPath(QueueStatusQueued, job.ID)); err != nil {
		t.Fatalf("expected malformed queued job to remain queued for diagnostics, got %v", err)
	}
	if _, err := os.Stat(store.queueJobPath(QueueStatusRunning, job.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected malformed queued job not to move to running, got %v", err)
	}
}

func TestClaimNextQueuedJobRejectsMismatchedStatusDirectory(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	job := QueueJob{
		SchemaVersion: 1,
		ID:            "job_claim_status_dir_mismatch",
		CreatedAt:     now,
		UpdatedAt:     now,
		Status:        QueueStatusRunning,
		Prompt:        "do work",
		Mode:          ModeExec,
		Background:    true,
		ClaimedAt:     now,
		HeartbeatAt:   now,
	}
	if err := store.ensureQueueDirs(); err != nil {
		t.Fatalf("ensure queue dirs: %v", err)
	}
	if err := store.writeJSONFile(store.queueJobPath(QueueStatusQueued, job.ID), job); err != nil {
		t.Fatalf("write status-dir mismatch queued job: %v", err)
	}

	claimed, ok, err := store.ClaimNextQueuedJob()
	if err == nil || !strings.Contains(err.Error(), "job_claim_status_dir_mismatch.json") || !strings.Contains(err.Error(), "status running does not match queue directory queued") {
		t.Fatalf("expected status-dir mismatch queued job claim error, got job=%#v ok=%v err=%v", claimed, ok, err)
	}
	if _, err := os.Stat(store.queueJobPath(QueueStatusQueued, job.ID)); err != nil {
		t.Fatalf("expected mismatched queued job to remain queued for diagnostics, got %v", err)
	}
	if _, err := os.Stat(store.queueJobPath(QueueStatusRunning, job.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected mismatched queued job not to move to running, got %v", err)
	}
}

func TestClaimNextQueuedJobRejectsMalformedQueuedJobTimestamps(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	job := QueueJob{
		SchemaVersion: 1,
		ID:            "job_bad_claim_time",
		CreatedAt:     "not-a-time",
		UpdatedAt:     now,
		Status:        QueueStatusQueued,
		Prompt:        "do work",
		Mode:          ModeExec,
		Background:    true,
	}
	if err := store.ensureQueueDirs(); err != nil {
		t.Fatalf("ensure queue dirs: %v", err)
	}
	if err := store.writeJSONFile(store.queueJobPath(QueueStatusQueued, job.ID), job); err != nil {
		t.Fatalf("write malformed queued job: %v", err)
	}

	claimed, ok, err := store.ClaimNextQueuedJob()
	if err == nil || !strings.Contains(err.Error(), "job_bad_claim_time.json") || !strings.Contains(err.Error(), "created_at must be RFC3339Nano") {
		t.Fatalf("expected malformed queued job timestamp error, got job=%#v ok=%v err=%v", claimed, ok, err)
	}
	if _, err := os.Stat(store.queueJobPath(QueueStatusQueued, job.ID)); err != nil {
		t.Fatalf("expected malformed queued job to remain queued for diagnostics, got %v", err)
	}
	if _, err := os.Stat(store.queueJobPath(QueueStatusRunning, job.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected malformed queued job not to move to running, got %v", err)
	}
}

func TestReconcileStaleRunningJobWithoutSessionFailsJob(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	oldHeartbeat := time.Now().UTC().Add(-queueRunningStaleAfter - time.Minute).Format(time.RFC3339Nano)
	job := QueueJob{
		SchemaVersion: 1,
		ID:            "job_stale_orphan",
		Status:        QueueStatusRunning,
		Prompt:        "do work",
		Mode:          ModeExec,
		Background:    true,
		ClaimedBy:     "process:test",
		ClaimedAt:     oldHeartbeat,
		HeartbeatAt:   oldHeartbeat,
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save running job: %v", err)
	}

	reconciled, err := store.LoadJob(job.ID)
	if err != nil {
		t.Fatalf("load reconciled job: %v", err)
	}
	if reconciled.Status != QueueStatusFailed || !strings.Contains(reconciled.LastError, "stale") || !strings.Contains(reconciled.LastError, "no linked session") {
		t.Fatalf("expected stale orphan job to fail, got %#v", reconciled)
	}
}

func TestReconcileKeepsRecentHeartbeatRunningJob(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	job := QueueJob{
		SchemaVersion: 1,
		ID:            "job_recent_orphan",
		Status:        QueueStatusRunning,
		Prompt:        "do work",
		Mode:          ModeExec,
		Background:    true,
		ClaimedBy:     "process:test",
		ClaimedAt:     now,
		HeartbeatAt:   now,
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save running job: %v", err)
	}

	reconciled, err := store.LoadJob(job.ID)
	if err != nil {
		t.Fatalf("load reconciled job: %v", err)
	}
	if reconciled.Status != QueueStatusRunning || reconciled.LastError != "" {
		t.Fatalf("expected recent orphan job to remain running, got %#v", reconciled)
	}
}

func TestListChildrenAndParentJobsUseCreationOrder(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	base := time.Date(2026, 5, 9, 3, 22, 19, 0, time.UTC)
	parentMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "parent_order",
		CreatedAt:        base.Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		RootSessionID:    "parent_order",
	}
	parentState := State{Status: StatusRunning, Phase: "turn_decide", UpdatedAt: base.Format(time.RFC3339Nano)}
	if err := store.Create(parentMeta, parentState); err != nil {
		t.Fatalf("create parent: %v", err)
	}

	children := []struct {
		id        string
		createdAt time.Time
		updatedAt time.Time
	}{
		{"child_first", base.Add(1 * time.Second), base.Add(1 * time.Second)},
		{"child_second", base.Add(2 * time.Second), base.Add(10 * time.Second)},
	}
	for _, child := range children {
		meta := SessionMetadata{
			SchemaVersion:    1,
			ID:               child.id,
			CreatedAt:        child.createdAt.Format(time.RFC3339Nano),
			Workdir:          t.TempDir(),
			Mode:             ModeExec,
			Provider:         "openai",
			Model:            "gpt-5.4",
			CompletionPolicy: CompletionPolicyAutonomous,
			ParentSessionID:  parentMeta.ID,
			RootSessionID:    parentMeta.ID,
			AgentRole:        "evaluator",
			Depth:            1,
		}
		state := State{Status: StatusRunning, Phase: "turn_decide", UpdatedAt: child.updatedAt.Format(time.RFC3339Nano)}
		if err := store.Create(meta, state); err != nil {
			t.Fatalf("create child %s: %v", child.id, err)
		}
	}
	listedChildren, err := store.ListChildren(parentMeta.ID, 10)
	if err != nil {
		t.Fatalf("list children: %v", err)
	}
	if len(listedChildren) != 2 || listedChildren[0].ID != "child_first" || listedChildren[1].ID != "child_second" {
		t.Fatalf("expected child creation order, got %#v", listedChildren)
	}

	firstJob := QueueJob{
		SchemaVersion:   1,
		ID:              "job_first",
		CreatedAt:       base.Add(1 * time.Second).Format(time.RFC3339Nano),
		Status:          QueueStatusQueued,
		ParentSessionID: parentMeta.ID,
		RootSessionID:   parentMeta.ID,
		Prompt:          "first",
		Mode:            ModeExec,
		Background:      true,
	}
	secondJob := QueueJob{
		SchemaVersion:   1,
		ID:              "job_second",
		CreatedAt:       base.Add(2 * time.Second).Format(time.RFC3339Nano),
		Status:          QueueStatusQueued,
		ParentSessionID: parentMeta.ID,
		RootSessionID:   parentMeta.ID,
		Prompt:          "second",
		Mode:            ModeExec,
		Background:      true,
	}
	if err := store.SaveJob(firstJob); err != nil {
		t.Fatalf("save first job: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := store.SaveJob(secondJob); err != nil {
		t.Fatalf("save second job: %v", err)
	}
	listedJobs, err := store.ListJobsByParent(parentMeta.ID, 10)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(listedJobs) != 2 || listedJobs[0].ID != "job_first" || listedJobs[1].ID != "job_second" {
		t.Fatalf("expected job creation order, got %#v", listedJobs)
	}
}

func TestListJobsPageUsesAllJobsBeyondDefaultListLimit(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	base := time.Date(2026, 5, 31, 9, 20, 0, 0, time.UTC)
	for i := 0; i < 105; i++ {
		job := QueueJob{
			SchemaVersion: 1,
			ID:            fmt.Sprintf("job_page_all_%03d", i),
			CreatedAt:     base.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
			Status:        QueueStatusQueued,
			Prompt:        "queued work",
			Mode:          ModeExec,
			Background:    true,
		}
		if err := store.SaveJob(job); err != nil {
			t.Fatalf("save job %d: %v", i, err)
		}
	}

	defaultJobs, err := store.ListJobs(0)
	if err != nil {
		t.Fatalf("list default jobs: %v", err)
	}
	if len(defaultJobs) != 100 {
		t.Fatalf("expected default list limit to remain 100, got %d", len(defaultJobs))
	}
	allJobs, err := store.ListJobs(-1)
	if err != nil {
		t.Fatalf("list all jobs: %v", err)
	}
	if len(allJobs) != 105 {
		t.Fatalf("expected negative limit to return all jobs, got %d", len(allJobs))
	}
	page, total, err := store.ListJobsPage(10, 100)
	if err != nil {
		t.Fatalf("list jobs page: %v", err)
	}
	if total != 105 || len(page) != 5 {
		t.Fatalf("expected total=105 and final page len=5, got total=%d page=%#v", total, page)
	}
}

func TestListJobsReportsCorruptQueueJob(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	job := QueueJob{
		SchemaVersion:   1,
		ID:              "job_corrupt_list",
		Status:          QueueStatusQueued,
		ParentSessionID: "parent_corrupt_list",
		RootSessionID:   "parent_corrupt_list",
		Prompt:          "do work",
		Mode:            ModeExec,
		Background:      true,
	}
	if err := store.EnqueueJob(job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	if err := os.WriteFile(store.queueJobPath(QueueStatusQueued, job.ID), []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatalf("corrupt queued job: %v", err)
	}

	if _, err := store.ListJobs(10); err == nil || !strings.Contains(err.Error(), "job_corrupt_list.json") {
		t.Fatalf("expected ListJobs to report corrupt job, got %v", err)
	}
	if _, _, err := store.ListJobsPage(10, 0); err == nil || !strings.Contains(err.Error(), "job_corrupt_list.json") {
		t.Fatalf("expected ListJobsPage to report corrupt job, got %v", err)
	}
	if _, err := store.ListJobsByParent(job.ParentSessionID, 10); err == nil || !strings.Contains(err.Error(), "job_corrupt_list.json") {
		t.Fatalf("expected ListJobsByParent to report corrupt job, got %v", err)
	}
}

func TestLoadParentCoordinationRejectsMalformedSnapshot(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create: %v", err)
	}
	malformed := ParentCoordination{
		SchemaVersion:           1,
		ParentSessionID:         meta.ID,
		WaitMode:                "wait-all",
		UnresolvedChildSessions: []string{"../child"},
		UpdatedAt:               now,
	}
	if err := store.writeJSONFile(filepath.Join(store.SessionDir(meta.ID), "parent-coordination.json"), malformed); err != nil {
		t.Fatalf("write malformed parent coordination: %v", err)
	}

	if _, err := store.LoadParentCoordination(meta.ID); err == nil || !strings.Contains(err.Error(), "validate parent-coordination.json") || !strings.Contains(err.Error(), "path separators") {
		t.Fatalf("expected malformed parent-coordination.json validation error, got %v", err)
	}
	if _, err := store.SnapshotParentCoordination(meta.ID); err == nil || !strings.Contains(err.Error(), "validate parent-coordination.json") {
		t.Fatalf("expected snapshot to reject malformed parent-coordination.json, got %v", err)
	}

	malformed.UpdatedAt = "not-a-time"
	malformed.UnresolvedChildSessions = []string{"child_parent_time"}
	if err := store.writeJSONFile(filepath.Join(store.SessionDir(meta.ID), "parent-coordination.json"), malformed); err != nil {
		t.Fatalf("write malformed parent coordination timestamp: %v", err)
	}
	if _, err := store.LoadParentCoordination(meta.ID); err == nil || !strings.Contains(err.Error(), "validate parent-coordination.json") || !strings.Contains(err.Error(), "updated_at must be RFC3339Nano") {
		t.Fatalf("expected malformed parent-coordination timestamp validation error, got %v", err)
	}
}

func TestParentCoordinationWritesRejectMalformedFacts(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create: %v", err)
	}

	invalidWaitMode := ParentCoordination{
		SchemaVersion:       1,
		ParentSessionID:     meta.ID,
		WaitMode:            "later",
		UnresolvedQueueJobs: []string{"job_parent_valid"},
		UpdatedAt:           now,
	}
	if err := store.SaveParentCoordination(meta.ID, invalidWaitMode); err == nil || !strings.Contains(err.Error(), "invalid parent coordination wait_mode") {
		t.Fatalf("expected save to reject invalid wait mode, got %v", err)
	}

	invalidUpdatedAt := ParentCoordination{
		SchemaVersion:       1,
		ParentSessionID:     meta.ID,
		WaitMode:            "wait-all",
		UnresolvedQueueJobs: []string{"job_parent_bad_time"},
		UpdatedAt:           "not-a-time",
	}
	if err := store.SaveParentCoordination(meta.ID, invalidUpdatedAt); err == nil || !strings.Contains(err.Error(), "updated_at must be RFC3339Nano") {
		t.Fatalf("expected save to reject invalid updated_at, got %v", err)
	}

	valid := ParentCoordination{
		SchemaVersion:       1,
		ParentSessionID:     meta.ID,
		WaitMode:            "wait-all",
		UnresolvedQueueJobs: []string{"job_parent_valid"},
		Parked:              true,
		UpdatedAt:           now,
	}
	if err := store.SaveParentCoordination(meta.ID, valid); err != nil {
		t.Fatalf("save valid parent coordination: %v", err)
	}
	conflicting := valid
	conflicting.CompletedQueueJobs = []string{"job_parent_valid"}
	if err := store.SaveParentCoordination(meta.ID, conflicting); err == nil || !strings.Contains(err.Error(), "appears in multiple parent coordination queue sets") {
		t.Fatalf("expected save to reject conflicting queue job status, got %v", err)
	}
	if _, _, err := store.MutateParentCoordination(meta.ID, func(coordination *ParentCoordination) error {
		coordination.UnresolvedChildSessions = []string{"child_parent_duplicate", "child_parent_duplicate"}
		return nil
	}); err == nil || !strings.Contains(err.Error(), "duplicate parent coordination child session id") {
		t.Fatalf("expected mutate to reject duplicate child session id, got %v", err)
	}
	loaded, err := store.LoadParentCoordination(meta.ID)
	if err != nil {
		t.Fatalf("load parent coordination: %v", err)
	}
	if len(loaded.UnresolvedQueueJobs) != 1 || loaded.UnresolvedQueueJobs[0] != "job_parent_valid" || len(loaded.CompletedQueueJobs) != 0 {
		t.Fatalf("malformed parent coordination write changed durable fact: %#v", loaded)
	}
}

func TestReconcileFailedJobUpdatesLinkedRunningSession(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	parentMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "parent_failed_job",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		RootSessionID:    "parent_failed_job",
	}
	parentState := State{Status: StatusRunning, Phase: "turn_decide", UpdatedAt: now}
	if err := store.Create(parentMeta, parentState); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	coordination := ParentCoordination{
		SchemaVersion:       1,
		ParentSessionID:     parentMeta.ID,
		WaitMode:            "all",
		UnresolvedQueueJobs: []string{"job_linked_failed"},
		UpdatedAt:           now,
	}
	if err := store.SaveParentCoordination(parentMeta.ID, coordination); err != nil {
		t.Fatalf("save parent coordination: %v", err)
	}
	job := QueueJob{
		SchemaVersion:   1,
		ID:              "job_linked_failed",
		CreatedAt:       now,
		Status:          QueueStatusFailed,
		ParentSessionID: parentMeta.ID,
		RootSessionID:   parentMeta.ID,
		AgentName:       "scan-store-and-data-layer",
		AgentRole:       "evaluator",
		Prompt:          "scan",
		Mode:            ModeExec,
		Background:      true,
		LastError:       "json: error calling MarshalJSON for type json.RawMessage: unexpected end of JSON input",
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save failed job: %v", err)
	}
	childMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_linked_failed",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		ParentSessionID:  parentMeta.ID,
		RootSessionID:    parentMeta.ID,
		AgentName:        "scan-store-and-data-layer",
		AgentRole:        "evaluator",
		QueueJobID:       job.ID,
		Depth:            1,
	}
	childState := State{Status: StatusRunning, Phase: "compact", UpdatedAt: now}
	if err := store.Create(childMeta, childState); err != nil {
		t.Fatalf("create child: %v", err)
	}

	repaired, err := store.LoadJob(job.ID)
	if err != nil {
		t.Fatalf("load repaired job: %v", err)
	}
	if repaired.Status != QueueStatusFailed || repaired.SessionID != childMeta.ID || repaired.SessionStatus != StatusFailed {
		t.Fatalf("expected failed job linked to failed child session, got %#v", repaired)
	}
	if !strings.Contains(repaired.LastError, "MarshalJSON") {
		t.Fatalf("expected job error to be preserved, got %#v", repaired)
	}
	loadedChildState, err := store.LoadState(childMeta.ID)
	if err != nil {
		t.Fatalf("load child state: %v", err)
	}
	if loadedChildState.Status != StatusFailed || !strings.Contains(loadedChildState.LastError, "MarshalJSON") {
		t.Fatalf("expected linked child state to be failed, got %#v", loadedChildState)
	}
	loadedCoordination, err := store.LoadParentCoordination(parentMeta.ID)
	if err != nil {
		t.Fatalf("load parent coordination: %v", err)
	}
	if stringSliceContains(loadedCoordination.UnresolvedQueueJobs, job.ID) || !stringSliceContains(loadedCoordination.FailedQueueJobs, job.ID) {
		t.Fatalf("expected parent coordination to move job to failed, got %#v", loadedCoordination)
	}
}

func TestLoadJobReportsLinkedSessionStateSaveError(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	childMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_state_save_error",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		ParentSessionID:  "parent_state_save_error",
		RootSessionID:    "parent_state_save_error",
		AgentName:        "state-save-child",
		AgentRole:        "evaluator",
		QueueJobID:       "job_state_save_error",
		Depth:            1,
	}
	if err := store.Create(childMeta, State{Status: StatusRunning, Phase: "provider_call", UpdatedAt: now}); err != nil {
		t.Fatalf("create child: %v", err)
	}
	steerPath := filepath.Join(store.SessionDir(childMeta.ID), "control", "steer.jsonl")
	if err := os.Remove(steerPath); err != nil {
		t.Fatalf("remove steer jsonl: %v", err)
	}
	if err := os.Mkdir(steerPath, 0o700); err != nil {
		t.Fatalf("replace steer jsonl with directory: %v", err)
	}
	job := QueueJob{
		SchemaVersion:   1,
		ID:              childMeta.QueueJobID,
		CreatedAt:       now,
		Status:          QueueStatusFailed,
		ParentSessionID: childMeta.ParentSessionID,
		RootSessionID:   childMeta.RootSessionID,
		AgentName:       childMeta.AgentName,
		AgentRole:       childMeta.AgentRole,
		Prompt:          "fail",
		Mode:            ModeExec,
		Background:      true,
		LastError:       "worker failed before state save",
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save failed job: %v", err)
	}

	repaired, err := store.LoadJob(job.ID)
	if err == nil {
		t.Fatalf("expected linked session state save error, got repaired job %#v", repaired)
	}
	loadedState, loadErr := store.LoadState(childMeta.ID)
	if loadErr != nil {
		t.Fatalf("load child state: %v", loadErr)
	}
	if loadedState.Status != StatusRunning {
		t.Fatalf("expected child state to remain running after failed repair, got %#v", loadedState)
	}
}

func TestDeleteSessionTreeDoesNotDeadlockWithReconcilableJob(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	parentMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "parent_delete",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		RootSessionID:    "parent_delete",
	}
	if err := store.Create(parentMeta, State{Status: StatusRunning, Phase: "turn_decide", UpdatedAt: now}); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_delete",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		ParentSessionID:  parentMeta.ID,
		RootSessionID:    parentMeta.ID,
		QueueJobID:       "job_delete",
		Depth:            1,
	}
	if err := store.Create(childMeta, State{Status: StatusRunning, Phase: "provider_call", UpdatedAt: now}); err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := store.SaveJob(QueueJob{
		SchemaVersion:   1,
		ID:              childMeta.QueueJobID,
		CreatedAt:       now,
		Status:          QueueStatusFailed,
		ParentSessionID: parentMeta.ID,
		RootSessionID:   parentMeta.ID,
		Prompt:          "delete",
		Mode:            ModeExec,
		Background:      true,
		LastError:       "failed before delete",
	}); err != nil {
		t.Fatalf("save job: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- store.DeleteSessionTree(parentMeta.ID)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("delete session tree: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("DeleteSessionTree appears deadlocked")
	}
	if _, err := os.Stat(store.SessionDir(parentMeta.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected parent session removed, got %v", err)
	}
	if _, err := os.Stat(store.SessionDir(childMeta.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected child session removed, got %v", err)
	}
	if _, err := store.LoadJob(childMeta.QueueJobID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected linked job removed, got %v", err)
	}
}

func TestClearHistoryRejectsSymlinkedSessionDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)
	if err := store.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "session-link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := store.ClearHistory()
	if err == nil || !strings.Contains(err.Error(), "symlinked") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(root, "session-link")); statErr != nil {
		t.Fatalf("expected symlink to remain, got %v", statErr)
	}
	if _, statErr := os.Stat(outside); statErr != nil {
		t.Fatalf("expected outside dir to remain, got %v", statErr)
	}
}

func TestClearHistoryRemovesRegularRootFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)
	if err := store.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	stale := filepath.Join(root, "stale.json")
	if err := os.WriteFile(stale, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write stale file: %v", err)
	}

	if err := store.ClearHistory(); err != nil {
		t.Fatalf("clear history: %v", err)
	}
	if _, statErr := os.Stat(stale); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected stale file removed, got %v", statErr)
	}
}

func TestLoadJobReconcilesLinkedQueueJobStatusWithoutSessionList(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	childMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_list_reconcile",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		ParentSessionID:  "parent_list_reconcile",
		RootSessionID:    "parent_list_reconcile",
		AgentName:        "child-list",
		AgentRole:        "evaluator",
		QueueJobID:       "job_list_reconcile",
		Depth:            1,
	}
	childState := State{Status: StatusRunning, Phase: "provider_call", UpdatedAt: now}
	if err := store.Create(childMeta, childState); err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := store.SaveJob(QueueJob{
		SchemaVersion:   1,
		ID:              childMeta.QueueJobID,
		CreatedAt:       now,
		Status:          QueueStatusFailed,
		ParentSessionID: childMeta.ParentSessionID,
		RootSessionID:   childMeta.RootSessionID,
		AgentName:       childMeta.AgentName,
		AgentRole:       childMeta.AgentRole,
		Prompt:          "fail",
		Mode:            ModeExec,
		Background:      true,
		LastError:       "worker failed",
	}); err != nil {
		t.Fatalf("save failed job: %v", err)
	}

	items, _, err := store.ListPage(10, 0)
	if err != nil {
		t.Fatalf("list page: %v", err)
	}
	var childSummary *SessionSummary
	for i := range items {
		if items[i].ID == childMeta.ID {
			childSummary = &items[i]
			break
		}
	}
	if childSummary == nil || childSummary.Status != StatusRunning || childSummary.LastError != "" {
		t.Fatalf("expected list page to use session state without queue repair, got %#v", childSummary)
	}
	loadedState, err := store.LoadState(childMeta.ID)
	if err != nil {
		t.Fatalf("load child state before repair: %v", err)
	}
	if loadedState.Status != StatusRunning || loadedState.LastError != "" {
		t.Fatalf("session list should not persist queue repair, got %#v", loadedState)
	}

	loadedJob, err := store.LoadJob(childMeta.QueueJobID)
	if err != nil {
		t.Fatalf("load job: %v", err)
	}
	if loadedJob.Status != QueueStatusFailed || loadedJob.LastError != "worker failed" {
		t.Fatalf("expected direct job load to preserve failed job, got %#v", loadedJob)
	}
	loadedState, err = store.LoadState(childMeta.ID)
	if err != nil {
		t.Fatalf("load child state after repair: %v", err)
	}
	if loadedState.Status != StatusFailed || loadedState.LastError != "worker failed" {
		t.Fatalf("expected direct job load to reconcile child state, got %#v", loadedState)
	}
}

func TestSessionListsAllowMissingMetadataOnlyQueueJob(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	parentMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "parent_missing_metadata_queue",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyInteractive,
		RootSessionID:    "parent_missing_metadata_queue",
	}
	if err := store.Create(parentMeta, State{Status: StatusRunning, Phase: "turn_decide", UpdatedAt: now}); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_missing_metadata_queue",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		ParentSessionID:  parentMeta.ID,
		RootSessionID:    parentMeta.ID,
		QueueJobID:       "job_missing_metadata_only_list",
		Depth:            1,
	}
	if err := store.Create(childMeta, State{Status: StatusAwaitingInput, Phase: "awaiting_input", UpdatedAt: now}); err != nil {
		t.Fatalf("create child: %v", err)
	}

	items, _, err := store.ListPage(10, 0)
	if err != nil {
		t.Fatalf("list page should tolerate missing metadata-only queue job: %v", err)
	}
	var foundChild bool
	for _, item := range items {
		if item.ID == childMeta.ID {
			foundChild = true
			if item.QueueJobID != childMeta.QueueJobID {
				t.Fatalf("expected metadata queue job id in list summary, got %#v", item)
			}
		}
	}
	if !foundChild {
		t.Fatalf("expected child session to remain listed, got %#v", items)
	}
	children, err := store.ListChildren(parentMeta.ID, 10)
	if err != nil {
		t.Fatalf("list children should tolerate missing metadata-only queue job: %v", err)
	}
	if len(children) != 1 || children[0].ID != childMeta.ID || children[0].QueueJobID != childMeta.QueueJobID {
		t.Fatalf("expected child summary with metadata queue job id, got %#v", children)
	}
}

func TestReconcileStaleLinkedRunningJobFailsSession(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	oldHeartbeat := time.Now().UTC().Add(-queueRunningStaleAfter - time.Minute).Format(time.RFC3339Nano)
	childMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_stale_linked",
		CreatedAt:        oldHeartbeat,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		ParentSessionID:  "parent_stale_linked",
		RootSessionID:    "parent_stale_linked",
		AgentName:        "stale-child",
		AgentRole:        "evaluator",
		QueueJobID:       "job_stale_linked",
		Depth:            1,
	}
	if err := store.Create(childMeta, State{Status: StatusRunning, Phase: "provider_call", UpdatedAt: oldHeartbeat}); err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := store.SaveJob(QueueJob{
		SchemaVersion:   1,
		ID:              childMeta.QueueJobID,
		CreatedAt:       oldHeartbeat,
		Status:          QueueStatusRunning,
		ClaimedAt:       oldHeartbeat,
		HeartbeatAt:     oldHeartbeat,
		ParentSessionID: childMeta.ParentSessionID,
		RootSessionID:   childMeta.RootSessionID,
		AgentName:       childMeta.AgentName,
		AgentRole:       childMeta.AgentRole,
		Prompt:          "stale",
		Mode:            ModeExec,
		Background:      true,
	}); err != nil {
		t.Fatalf("save stale running job: %v", err)
	}

	reconciled, err := store.LoadJob(childMeta.QueueJobID)
	if err != nil {
		t.Fatalf("load reconciled job: %v", err)
	}
	if reconciled.Status != QueueStatusFailed || reconciled.SessionStatus != StatusFailed || !strings.Contains(reconciled.LastError, "linked running session heartbeat is stale") {
		t.Fatalf("expected stale linked job to fail, got %#v", reconciled)
	}
	loadedState, err := store.LoadState(childMeta.ID)
	if err != nil {
		t.Fatalf("load child state: %v", err)
	}
	if loadedState.Status != StatusFailed || !strings.Contains(loadedState.LastError, "linked running session heartbeat is stale") {
		t.Fatalf("expected stale linked child session to fail, got %#v", loadedState)
	}
}

func TestLoadJobReportsCorruptLinkedSessionFacts(t *testing.T) {
	cases := []struct {
		name          string
		file          string
		persistJobSID bool
	}{
		{name: "metadata", file: "session.json", persistJobSID: true},
		{name: "state", file: "state.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := NewStore(filepath.Join(t.TempDir(), "sessions"))
			oldHeartbeat := time.Now().UTC().Add(-queueRunningStaleAfter - time.Minute).Format(time.RFC3339Nano)
			childMeta := SessionMetadata{
				SchemaVersion:    1,
				ID:               "child_corrupt_linked_" + tc.name,
				CreatedAt:        oldHeartbeat,
				Workdir:          t.TempDir(),
				Mode:             ModeExec,
				Provider:         "openai",
				Model:            "gpt-5.4",
				CompletionPolicy: CompletionPolicyAutonomous,
				ParentSessionID:  "parent_corrupt_linked_" + tc.name,
				RootSessionID:    "parent_corrupt_linked_" + tc.name,
				AgentName:        "corrupt-child",
				AgentRole:        "evaluator",
				QueueJobID:       "job_corrupt_linked_" + tc.name,
				Depth:            1,
			}
			if err := store.Create(childMeta, State{Status: StatusRunning, Phase: "provider_call", UpdatedAt: oldHeartbeat}); err != nil {
				t.Fatalf("create child: %v", err)
			}
			job := QueueJob{
				SchemaVersion:   1,
				ID:              childMeta.QueueJobID,
				CreatedAt:       oldHeartbeat,
				Status:          QueueStatusRunning,
				ClaimedAt:       oldHeartbeat,
				HeartbeatAt:     oldHeartbeat,
				ParentSessionID: childMeta.ParentSessionID,
				RootSessionID:   childMeta.RootSessionID,
				AgentName:       childMeta.AgentName,
				AgentRole:       childMeta.AgentRole,
				Prompt:          "stale",
				Mode:            ModeExec,
				Background:      true,
			}
			if tc.persistJobSID {
				job.SessionID = childMeta.ID
			}
			if err := store.SaveJob(job); err != nil {
				t.Fatalf("save stale running job: %v", err)
			}
			if err := os.WriteFile(filepath.Join(store.SessionDir(childMeta.ID), tc.file), []byte("{not-json}\n"), 0o600); err != nil {
				t.Fatalf("corrupt linked %s: %v", tc.file, err)
			}

			reconciled, err := store.LoadJob(job.ID)
			if err == nil || !strings.Contains(err.Error(), tc.file) {
				t.Fatalf("expected corrupt linked %s error, got job=%#v err=%v", tc.file, reconciled, err)
			}
			var persisted QueueJob
			if err := readJSONFile(store.queueJobPath(QueueStatusRunning, job.ID), &persisted); err != nil {
				t.Fatalf("read persisted running job: %v", err)
			}
			if persisted.Status != QueueStatusRunning || persisted.LastError != "" {
				t.Fatalf("expected corrupt linked session facts not to mark job orphan, got %#v", persisted)
			}
		})
	}
}

func TestLoadJobDoesNotReadChildMessagesForRunningStatusRepair(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	oldHeartbeat := time.Now().UTC().Add(-queueRunningStaleAfter - time.Minute).Format(time.RFC3339Nano)
	childMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_corrupt_messages_running",
		CreatedAt:        oldHeartbeat,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		ParentSessionID:  "parent_corrupt_messages_running",
		RootSessionID:    "parent_corrupt_messages_running",
		AgentName:        "corrupt-child",
		AgentRole:        "evaluator",
		QueueJobID:       "job_corrupt_messages_running",
		Depth:            1,
	}
	if err := store.Create(childMeta, State{Status: StatusRunning, Phase: "provider_call", UpdatedAt: oldHeartbeat}); err != nil {
		t.Fatalf("create child: %v", err)
	}
	job := QueueJob{
		SchemaVersion:   1,
		ID:              childMeta.QueueJobID,
		CreatedAt:       oldHeartbeat,
		Status:          QueueStatusRunning,
		ClaimedAt:       oldHeartbeat,
		HeartbeatAt:     oldHeartbeat,
		ParentSessionID: childMeta.ParentSessionID,
		RootSessionID:   childMeta.RootSessionID,
		AgentName:       childMeta.AgentName,
		AgentRole:       childMeta.AgentRole,
		Prompt:          "stale",
		Mode:            ModeExec,
		Background:      true,
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save stale running job: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.SessionDir(childMeta.ID), "messages.jsonl"), []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatalf("corrupt linked messages: %v", err)
	}

	reconciled, err := store.LoadJob(job.ID)
	if err != nil {
		t.Fatalf("load reconciled job should not read corrupt running messages: %v", err)
	}
	if reconciled.Status != QueueStatusFailed || reconciled.SessionStatus != StatusFailed || !strings.Contains(reconciled.LastError, "linked running session heartbeat is stale") {
		t.Fatalf("expected stale running job to repair from state without reading messages, got %#v", reconciled)
	}
}

func TestLoadJobReportsCorruptCompletedChildMessagesWhenRepairingVisiblePaths(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	childMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_corrupt_completed_messages",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		ParentSessionID:  "parent_corrupt_completed_messages",
		RootSessionID:    "parent_corrupt_completed_messages",
		AgentName:        "corrupt-child",
		AgentRole:        "evaluator",
		QueueJobID:       "job_corrupt_completed_messages",
		Depth:            1,
	}
	if err := store.Create(childMeta, State{Status: StatusCompleted, Phase: "turn_decide", UpdatedAt: now}); err != nil {
		t.Fatalf("create child: %v", err)
	}
	job := QueueJob{
		SchemaVersion:   1,
		ID:              childMeta.QueueJobID,
		CreatedAt:       now,
		Status:          QueueStatusRunning,
		ParentSessionID: childMeta.ParentSessionID,
		RootSessionID:   childMeta.RootSessionID,
		AgentName:       childMeta.AgentName,
		AgentRole:       childMeta.AgentRole,
		Prompt:          "done",
		Mode:            ModeExec,
		Background:      true,
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save running job: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.SessionDir(childMeta.ID), "messages.jsonl"), []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatalf("corrupt linked messages: %v", err)
	}

	reconciled, err := store.LoadJob(job.ID)
	if err == nil || !strings.Contains(err.Error(), "messages.jsonl") {
		t.Fatalf("expected corrupt completed messages error, got job=%#v err=%v", reconciled, err)
	}
}

func TestListJobsSnapshotDoesNotRepairCompletedChildVisiblePaths(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	childMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_snapshot_no_repair",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		ParentSessionID:  "parent_snapshot_no_repair",
		RootSessionID:    "parent_snapshot_no_repair",
		AgentName:        "snapshot-child",
		AgentRole:        "evaluator",
		QueueJobID:       "job_snapshot_no_repair",
		Depth:            1,
	}
	if err := store.Create(childMeta, State{Status: StatusCompleted, Phase: "turn_decide", UpdatedAt: now}); err != nil {
		t.Fatalf("create child: %v", err)
	}
	job := QueueJob{
		SchemaVersion:   1,
		ID:              childMeta.QueueJobID,
		CreatedAt:       now,
		Status:          QueueStatusRunning,
		ParentSessionID: childMeta.ParentSessionID,
		RootSessionID:   childMeta.RootSessionID,
		AgentName:       childMeta.AgentName,
		AgentRole:       childMeta.AgentRole,
		Prompt:          "done",
		Mode:            ModeExec,
		Background:      true,
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save running job: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.SessionDir(childMeta.ID), "messages.jsonl"), []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatalf("corrupt linked messages: %v", err)
	}

	jobs, err := store.ListJobsSnapshot(10)
	if err != nil {
		t.Fatalf("snapshot list should not read corrupt child messages: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Status != QueueStatusRunning || jobs[0].ID != job.ID {
		t.Fatalf("expected unrepaired running job snapshot, got %#v", jobs)
	}
	parentJobs, err := store.ListJobsByParentSnapshot(childMeta.ParentSessionID, 10)
	if err != nil {
		t.Fatalf("parent snapshot list should not read corrupt child messages: %v", err)
	}
	if len(parentJobs) != 1 || parentJobs[0].Status != QueueStatusRunning || parentJobs[0].ID != job.ID {
		t.Fatalf("expected unrepaired parent running job snapshot, got %#v", parentJobs)
	}
	statusJobs, err := store.ListJobsStatusSnapshot(10)
	if err != nil {
		t.Fatalf("status snapshot list should not read corrupt child messages: %v", err)
	}
	if len(statusJobs) != 1 || statusJobs[0].Status != QueueStatusCompleted || statusJobs[0].SessionID != childMeta.ID {
		t.Fatalf("expected status snapshot to reconcile child state without visible paths, got %#v", statusJobs)
	}
	parentStatusJobs, err := store.ListJobsByParentStatusSnapshot(childMeta.ParentSessionID, 10)
	if err != nil {
		t.Fatalf("parent status snapshot list should not read corrupt child messages: %v", err)
	}
	if len(parentStatusJobs) != 1 || parentStatusJobs[0].Status != QueueStatusCompleted || parentStatusJobs[0].SessionID != childMeta.ID {
		t.Fatalf("expected parent status snapshot to reconcile child state without visible paths, got %#v", parentStatusJobs)
	}
	if _, err := store.ListJobs(10); err == nil || !strings.Contains(err.Error(), "messages.jsonl") {
		t.Fatalf("expected reconciled list to still report corrupt completed child messages, got %v", err)
	}
}

func TestListJobsStatusSnapshotBlocksRunningJobLinkedByChildMetadata(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	parentMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "parent_status_snapshot_paused",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		RootSessionID:    "parent_status_snapshot_paused",
	}
	if err := store.Create(parentMeta, State{Status: StatusPaused, Phase: "interrupt", PauseReason: "manual_stop", UpdatedAt: now}); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	job := QueueJob{
		SchemaVersion:   1,
		ID:              "job_status_snapshot_paused",
		CreatedAt:       now,
		UpdatedAt:       now,
		Status:          QueueStatusRunning,
		ClaimedAt:       now,
		HeartbeatAt:     now,
		ParentSessionID: parentMeta.ID,
		RootSessionID:   parentMeta.ID,
		Prompt:          "paused child",
		Mode:            ModeExec,
		Background:      true,
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save running job: %v", err)
	}
	childMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_status_snapshot_paused",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		ParentSessionID:  parentMeta.ID,
		RootSessionID:    parentMeta.ID,
		QueueJobID:       job.ID,
		Depth:            1,
	}
	if err := store.Create(childMeta, State{Status: StatusPaused, Phase: "interrupt", PauseReason: "manual_stop", UpdatedAt: now}); err != nil {
		t.Fatalf("create child: %v", err)
	}

	jobs, err := store.ListJobsStatusSnapshot(10)
	if err != nil {
		t.Fatalf("status snapshot list: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Status != QueueStatusBlocked || jobs[0].SessionID != childMeta.ID || jobs[0].SessionStatus != StatusPaused || jobs[0].StopReason != QueueStopReasonParentStop {
		t.Fatalf("expected status snapshot to block child-linked running job, got %#v", jobs)
	}
	var persisted QueueJob
	if err := readJSONFile(store.queueJobPath(QueueStatusRunning, job.ID), &persisted); err != nil {
		t.Fatalf("read persisted running job: %v", err)
	}
	if persisted.Status != QueueStatusRunning || persisted.SessionID != "" {
		t.Fatalf("status snapshot should not persist lightweight repair, got %#v", persisted)
	}
}

func TestLoadJobReportsMismatchedExplicitLinkedSession(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	oldHeartbeat := time.Now().UTC().Add(-queueRunningStaleAfter - time.Minute).Format(time.RFC3339Nano)
	childMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_mismatched_linked",
		CreatedAt:        oldHeartbeat,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		ParentSessionID:  "parent_mismatched_linked",
		RootSessionID:    "parent_mismatched_linked",
		AgentName:        "mismatched-child",
		AgentRole:        "evaluator",
		QueueJobID:       "job_other_mismatched_linked",
		Depth:            1,
	}
	if err := store.Create(childMeta, State{Status: StatusRunning, Phase: "provider_call", UpdatedAt: oldHeartbeat}); err != nil {
		t.Fatalf("create child: %v", err)
	}
	job := QueueJob{
		SchemaVersion:   1,
		ID:              "job_mismatched_linked",
		CreatedAt:       oldHeartbeat,
		Status:          QueueStatusRunning,
		ClaimedAt:       oldHeartbeat,
		HeartbeatAt:     oldHeartbeat,
		ParentSessionID: childMeta.ParentSessionID,
		RootSessionID:   childMeta.RootSessionID,
		SessionID:       childMeta.ID,
		AgentName:       childMeta.AgentName,
		AgentRole:       childMeta.AgentRole,
		Prompt:          "stale",
		Mode:            ModeExec,
		Background:      true,
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save stale running job: %v", err)
	}

	reconciled, err := store.LoadJob(job.ID)
	if err == nil || !strings.Contains(err.Error(), "session.json") || !strings.Contains(err.Error(), childMeta.QueueJobID) {
		t.Fatalf("expected mismatched linked session metadata error, got job=%#v err=%v", reconciled, err)
	}
	var persisted QueueJob
	if err := readJSONFile(store.queueJobPath(QueueStatusRunning, job.ID), &persisted); err != nil {
		t.Fatalf("read persisted running job: %v", err)
	}
	if persisted.Status != QueueStatusRunning || persisted.LastError != "" {
		t.Fatalf("expected mismatched linked session facts not to mark job orphan, got %#v", persisted)
	}
}

func TestReconcileCompletedSessionCompletesJob(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)

	parentMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "parent_session",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		RootSessionID:    "parent_session",
	}
	parentState := State{
		Status:    StatusCompleted,
		Phase:     "turn_decide",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.Create(parentMeta, parentState); err != nil {
		t.Fatalf("create parent: %v", err)
	}

	requestedWorkdir := t.TempDir()
	stale := QueueJob{
		SchemaVersion:    1,
		ID:               "job_repair",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		UpdatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Status:           QueueStatusRunning,
		ParentSessionID:  parentMeta.ID,
		RootSessionID:    parentMeta.ID,
		AgentName:        "child-two",
		AgentRole:        "evaluator",
		Prompt:           "Finish child two.",
		Mode:             ModeExec,
		RequestedWorkdir: requestedWorkdir,
		Background:       true,
		IsolationMode:    "auto",
	}
	if err := store.SaveJob(stale); err != nil {
		t.Fatalf("save stale job: %v", err)
	}

	effectiveWorkdir := t.TempDir()
	childMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_session",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          effectiveWorkdir,
		RequestedWorkdir: requestedWorkdir,
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		ParentSessionID:  parentMeta.ID,
		RootSessionID:    parentMeta.ID,
		AgentName:        "child-two",
		AgentRole:        "evaluator",
		QueueJobID:       stale.ID,
		Depth:            1,
	}
	childState := State{
		Status:               StatusCompleted,
		Phase:                "turn_decide",
		UpdatedAt:            time.Now().UTC().Format(time.RFC3339Nano),
		LastAssistantExcerpt: "Done.",
	}
	if err := store.Create(childMeta, childState); err != nil {
		t.Fatalf("create child: %v", err)
	}
	outputPath := filepath.Join(effectiveWorkdir, "reports", "child-two.md")
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		t.Fatalf("mkdir output: %v", err)
	}
	if err := os.WriteFile(outputPath, []byte("CHILD_TWO_OK\n"), 0o644); err != nil {
		t.Fatalf("write output: %v", err)
	}
	if err := store.AppendMessage(childMeta.ID, NewToolMessage([]ToolResult{{
		Name:          "write_file",
		DisplayOutput: "ok",
		Metadata:      map[string]any{"path": outputPath},
	}})); err != nil {
		t.Fatalf("append tool message: %v", err)
	}

	repaired, err := store.LoadJob(stale.ID)
	if err != nil {
		t.Fatalf("load repaired job: %v", err)
	}
	if repaired.Status != QueueStatusCompleted {
		t.Fatalf("expected completed repaired job, got %#v", repaired)
	}
	if repaired.SessionID != childMeta.ID {
		t.Fatalf("expected repaired session id %s, got %#v", childMeta.ID, repaired)
	}
	if repaired.EffectiveWorkdir != effectiveWorkdir {
		t.Fatalf("expected effective workdir %s, got %#v", effectiveWorkdir, repaired)
	}
	if len(repaired.VisiblePaths) != 1 || repaired.VisiblePaths[0] != "reports/child-two.md" {
		t.Fatalf("unexpected visible paths: %#v", repaired.VisiblePaths)
	}
	syncedBytes, err := os.ReadFile(filepath.Join(requestedWorkdir, "reports", "child-two.md"))
	if err != nil {
		t.Fatalf("read synced output: %v", err)
	}
	if string(syncedBytes) != "CHILD_TWO_OK\n" {
		t.Fatalf("unexpected synced output: %q", string(syncedBytes))
	}
	notifications, err := store.LoadBackgroundNotifications(parentMeta.ID)
	if err != nil {
		t.Fatalf("load notifications: %v", err)
	}
	if len(notifications) != 1 || notifications[0].QueueJobID != stale.ID {
		t.Fatalf("unexpected notifications: %#v", notifications)
	}
	eventsList, err := store.LoadEvents(parentMeta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	completedCount := 0
	for _, evt := range eventsList {
		if evt.Type != "queue.job.completed" {
			continue
		}
		jobID, _ := evt.Data["job_id"].(string)
		if jobID == stale.ID {
			completedCount++
		}
	}
	if completedCount != 1 {
		t.Fatalf("expected one repaired completion event, got %d", completedCount)
	}
	reloaded, err := store.LoadJob(stale.ID)
	if err != nil {
		t.Fatalf("reload repaired job: %v", err)
	}
	if reloaded.Status != QueueStatusCompleted {
		t.Fatalf("expected persisted completed job, got %#v", reloaded)
	}
}

func TestLoadJobReconcilesFreshRunningLeaseWhenLinkedSessionSettled(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	parentMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "parent_fresh_lease",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		RootSessionID:    "parent_fresh_lease",
	}
	if err := store.Create(parentMeta, State{Status: StatusRunning, Phase: "turn_decide", UpdatedAt: now}); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	job := QueueJob{
		SchemaVersion:   1,
		ID:              "job_fresh_lease",
		CreatedAt:       now,
		UpdatedAt:       now,
		Status:          QueueStatusRunning,
		ClaimedAt:       now,
		HeartbeatAt:     now,
		ParentSessionID: parentMeta.ID,
		RootSessionID:   parentMeta.ID,
		AgentName:       "child-two",
		AgentRole:       "evaluator",
		Prompt:          "Finish child two.",
		Mode:            ModeExec,
		Background:      true,
		IsolationMode:   "auto",
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save running job: %v", err)
	}
	if err := store.SaveParentCoordination(parentMeta.ID, ParentCoordination{
		SchemaVersion:       1,
		ParentSessionID:     parentMeta.ID,
		WaitMode:            "wait-all",
		UnresolvedQueueJobs: []string{job.ID},
		Parked:              true,
		UpdatedAt:           now,
	}); err != nil {
		t.Fatalf("save parent coordination: %v", err)
	}
	childMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_fresh_lease",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		ParentSessionID:  parentMeta.ID,
		RootSessionID:    parentMeta.ID,
		AgentName:        "child-two",
		AgentRole:        "evaluator",
		QueueJobID:       job.ID,
		Depth:            1,
	}
	if err := store.Create(childMeta, State{
		Status:               StatusCompleted,
		Phase:                "turn_decide",
		UpdatedAt:            now,
		LastAssistantExcerpt: "Done.",
	}); err != nil {
		t.Fatalf("create completed child: %v", err)
	}
	job.SessionID = childMeta.ID
	job.SessionStatus = StatusRunning
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save linked running job: %v", err)
	}

	observed, err := store.LoadJob(job.ID)
	if err != nil {
		t.Fatalf("load fresh running job: %v", err)
	}
	if observed.Status != QueueStatusCompleted || observed.SessionStatus != StatusCompleted || observed.FinalText != "Done." {
		t.Fatalf("fresh lease should reconcile completed linked child, got %#v", observed)
	}
	if _, err := os.Stat(store.queueJobPath(QueueStatusRunning, job.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh lease should remove running queue copy, stat err=%v", err)
	}
	var persisted QueueJob
	if err := readJSONFile(store.queueJobPath(QueueStatusCompleted, job.ID), &persisted); err != nil {
		t.Fatalf("read persisted completed job: %v", err)
	}
	if persisted.Status != QueueStatusCompleted || persisted.SessionID != childMeta.ID || persisted.SessionStatus != StatusCompleted {
		t.Fatalf("fresh lease load should persist completed queue job, got %#v", persisted)
	}
	notifications, err := store.LoadBackgroundNotifications(parentMeta.ID)
	if err != nil {
		t.Fatalf("load notifications: %v", err)
	}
	if len(notifications) != 1 || notifications[0].Status != QueueStatusCompleted || notifications[0].QueueJobID != job.ID {
		t.Fatalf("fresh lease should notify parent after linked child completion, got %#v", notifications)
	}
	coordination, err := store.LoadParentCoordination(parentMeta.ID)
	if err != nil {
		t.Fatalf("load parent coordination: %v", err)
	}
	if len(coordination.UnresolvedQueueJobs) != 0 ||
		!slices.Equal(coordination.CompletedQueueJobs, []string{job.ID}) ||
		len(coordination.FailedQueueJobs) != 0 ||
		coordination.Parked {
		t.Fatalf("fresh lease should resolve parent coordination after linked child completion, got %#v", coordination)
	}
	eventsList, err := store.LoadEvents(parentMeta.ID)
	if err != nil {
		t.Fatalf("load parent events: %v", err)
	}
	if countStoreEventType(eventsList, "queue.job.notified") != 1 || countStoreEventType(eventsList, "queue.job.completed") != 1 {
		t.Fatalf("fresh lease should emit parent completion events, got %#v", eventsList)
	}
}

func TestLoadJobBlocksFreshRunningLeaseWhenLinkedSessionPaused(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	parentMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "parent_fresh_paused",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		RootSessionID:    "parent_fresh_paused",
	}
	if err := store.Create(parentMeta, State{Status: StatusPaused, Phase: "interrupt", PauseReason: "manual_stop", UpdatedAt: now}); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	job := QueueJob{
		SchemaVersion:   1,
		ID:              "job_fresh_paused",
		CreatedAt:       now,
		UpdatedAt:       now,
		Status:          QueueStatusRunning,
		ClaimedAt:       now,
		HeartbeatAt:     now,
		ParentSessionID: parentMeta.ID,
		RootSessionID:   parentMeta.ID,
		Prompt:          "Finish child.",
		Mode:            ModeExec,
		Background:      true,
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save running job: %v", err)
	}
	if err := store.SaveParentCoordination(parentMeta.ID, ParentCoordination{
		SchemaVersion:       1,
		ParentSessionID:     parentMeta.ID,
		WaitMode:            "wait-all",
		UnresolvedQueueJobs: []string{job.ID},
		Parked:              true,
		UpdatedAt:           now,
	}); err != nil {
		t.Fatalf("save parent coordination: %v", err)
	}
	childMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_fresh_paused",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		ParentSessionID:  parentMeta.ID,
		RootSessionID:    parentMeta.ID,
		QueueJobID:       job.ID,
		Depth:            1,
	}
	if err := store.Create(childMeta, State{Status: StatusPaused, Phase: "interrupt", PauseReason: "manual_stop", UpdatedAt: now}); err != nil {
		t.Fatalf("create paused child: %v", err)
	}
	job.SessionID = childMeta.ID
	job.SessionStatus = StatusRunning
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save linked running job: %v", err)
	}

	observed, err := store.LoadJob(job.ID)
	if err != nil {
		t.Fatalf("load fresh paused job: %v", err)
	}
	if observed.Status != QueueStatusBlocked || observed.SessionStatus != StatusPaused || observed.StopReason != QueueStopReasonParentStop {
		t.Fatalf("fresh lease should block parent-stopped paused child, got %#v", observed)
	}
	if _, err := os.Stat(store.queueJobPath(QueueStatusRunning, job.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh lease should remove running queue copy, stat err=%v", err)
	}
	if _, err := os.Stat(store.queueJobPath(QueueStatusBlocked, job.ID)); err != nil {
		t.Fatalf("fresh lease should persist blocked queue copy: %v", err)
	}
	coordination, err := store.LoadParentCoordination(parentMeta.ID)
	if err != nil {
		t.Fatalf("load parent coordination: %v", err)
	}
	if !slices.Equal(coordination.UnresolvedQueueJobs, []string{job.ID}) || !coordination.Parked {
		t.Fatalf("blocked parent-stopped child should remain unresolved, got %#v", coordination)
	}
}

func TestSyncQueueVisiblePathsRejectsDestinationSymlinkAlias(t *testing.T) {
	requestedWorkdir := t.TempDir()
	effectiveWorkdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(effectiveWorkdir, "reports"), 0o755); err != nil {
		t.Fatalf("mkdir effective reports: %v", err)
	}
	if err := os.WriteFile(filepath.Join(effectiveWorkdir, "reports", "child-two.md"), []byte("CHILD_TWO_OK\n"), 0o600); err != nil {
		t.Fatalf("write child output: %v", err)
	}
	envPath := filepath.Join(requestedWorkdir, ".env")
	if err := os.WriteFile(envPath, []byte("KEEP=1\n"), 0o600); err != nil {
		t.Fatalf("write requested env: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(requestedWorkdir, "reports"), 0o755); err != nil {
		t.Fatalf("mkdir requested reports: %v", err)
	}
	if err := os.Symlink(envPath, filepath.Join(requestedWorkdir, "reports", "child-two.md")); err != nil {
		t.Fatalf("symlink visible output alias: %v", err)
	}

	synced := syncQueueVisiblePaths(requestedWorkdir, effectiveWorkdir, []string{"reports/child-two.md"})
	if len(synced) != 0 {
		t.Fatalf("expected symlink destination alias not to sync, got %#v", synced)
	}
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	if string(data) != "KEEP=1\n" {
		t.Fatalf("visible output sync should not overwrite env alias, got %q", data)
	}
}

func TestReconcilePreservesFailedHandoffAfterCompletedChild(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	parentMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "parent_failed_handoff",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		RootSessionID:    "parent_failed_handoff",
	}
	if err := store.Create(parentMeta, State{Status: StatusCompleted, Phase: "turn_decide", UpdatedAt: now}); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	requestedWorkdir := t.TempDir()
	effectiveWorkdir := t.TempDir()
	childMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_failed_handoff",
		CreatedAt:        now,
		Workdir:          effectiveWorkdir,
		RequestedWorkdir: requestedWorkdir,
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		ParentSessionID:  parentMeta.ID,
		RootSessionID:    parentMeta.ID,
		AgentName:        "child-two",
		AgentRole:        "evaluator",
		QueueJobID:       "job_failed_handoff",
		Depth:            1,
	}
	if err := store.Create(childMeta, State{
		Status:               StatusCompleted,
		Phase:                "turn_decide",
		UpdatedAt:            now,
		LastAssistantExcerpt: "Done.",
	}); err != nil {
		t.Fatalf("create child: %v", err)
	}
	job := QueueJob{
		SchemaVersion:    1,
		ID:               childMeta.QueueJobID,
		CreatedAt:        now,
		UpdatedAt:        now,
		Status:           QueueStatusFailed,
		ParentSessionID:  parentMeta.ID,
		RootSessionID:    parentMeta.ID,
		AgentName:        childMeta.AgentName,
		AgentRole:        childMeta.AgentRole,
		Prompt:           "Finish child two.",
		Mode:             ModeExec,
		RequestedWorkdir: requestedWorkdir,
		EffectiveWorkdir: effectiveWorkdir,
		Background:       true,
		IsolationMode:    "auto",
		SessionID:        childMeta.ID,
		SessionStatus:    StatusCompleted,
		LastError:        "queue handoff failed after child completed",
		FinalText:        "Done.",
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save failed handoff job: %v", err)
	}
	if err := store.SaveParentCoordination(parentMeta.ID, ParentCoordination{
		SchemaVersion:       1,
		ParentSessionID:     parentMeta.ID,
		WaitMode:            "wait-all",
		UnresolvedQueueJobs: []string{job.ID},
		Parked:              true,
		UpdatedAt:           now,
	}); err != nil {
		t.Fatalf("save parent coordination: %v", err)
	}

	repaired, err := store.LoadJob(job.ID)
	if err != nil {
		t.Fatalf("load repaired job: %v", err)
	}
	if repaired.Status != QueueStatusFailed || repaired.LastError != job.LastError || repaired.SessionStatus != StatusCompleted {
		t.Fatalf("expected failed handoff job to remain failed, got %#v", repaired)
	}
	notifications, err := store.LoadBackgroundNotifications(parentMeta.ID)
	if err != nil {
		t.Fatalf("load notifications: %v", err)
	}
	if len(notifications) != 1 || notifications[0].Status != QueueStatusFailed || notifications[0].LastError != job.LastError {
		t.Fatalf("expected failed handoff notification, got %#v", notifications)
	}
	coordination, err := store.LoadParentCoordination(parentMeta.ID)
	if err != nil {
		t.Fatalf("load coordination: %v", err)
	}
	if len(coordination.CompletedQueueJobs) != 0 || !slices.Contains(coordination.FailedQueueJobs, job.ID) {
		t.Fatalf("expected failed queue coordination, got %#v", coordination)
	}
}

func TestLoadJobReportsTerminalParentNotificationAppendError(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	parentMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "parent_notification_append_error",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		RootSessionID:    "parent_notification_append_error",
	}
	if err := store.Create(parentMeta, State{Status: StatusRunning, Phase: "turn_decide", UpdatedAt: now}); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	job := QueueJob{
		SchemaVersion:   1,
		ID:              "job_notification_append_error",
		CreatedAt:       now,
		UpdatedAt:       now,
		Status:          QueueStatusCompleted,
		ParentSessionID: parentMeta.ID,
		RootSessionID:   parentMeta.ID,
		Prompt:          "done",
		Mode:            ModeExec,
		Background:      true,
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save completed job: %v", err)
	}
	backgroundPath := filepath.Join(store.SessionDir(parentMeta.ID), "control", "background.jsonl")
	if err := os.Remove(backgroundPath); err != nil {
		t.Fatalf("remove background notifications: %v", err)
	}
	if err := os.Mkdir(backgroundPath, 0o700); err != nil {
		t.Fatalf("replace background notifications with directory: %v", err)
	}

	reconciled, err := store.LoadJob(job.ID)
	if err == nil || !strings.Contains(err.Error(), "background.jsonl") {
		t.Fatalf("expected background notification append error, got job=%#v err=%v", reconciled, err)
	}
}

func TestLoadJobReportsTerminalParentEventAppendError(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	parentMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "parent_event_append_error",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		RootSessionID:    "parent_event_append_error",
	}
	if err := store.Create(parentMeta, State{Status: StatusRunning, Phase: "turn_decide", UpdatedAt: now}); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	job := QueueJob{
		SchemaVersion:   1,
		ID:              "job_event_append_error",
		CreatedAt:       now,
		UpdatedAt:       now,
		Status:          QueueStatusCompleted,
		ParentSessionID: parentMeta.ID,
		RootSessionID:   parentMeta.ID,
		Prompt:          "done",
		Mode:            ModeExec,
		Background:      true,
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save completed job: %v", err)
	}
	if err := store.EnsureBackgroundNotification(parentMeta.ID, NewBackgroundNotification(job)); err != nil {
		t.Fatalf("prewrite background notification: %v", err)
	}
	eventsPath := filepath.Join(store.SessionDir(parentMeta.ID), "events.jsonl")
	if err := os.Remove(eventsPath); err != nil {
		t.Fatalf("remove events: %v", err)
	}
	if err := os.Mkdir(eventsPath, 0o700); err != nil {
		t.Fatalf("replace events with directory: %v", err)
	}

	reconciled, err := store.LoadJob(job.ID)
	if err == nil || !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected queue lifecycle event append error, got job=%#v err=%v", reconciled, err)
	}
}

func TestLoadJobRollsBackBackgroundNotificationWhenNotifiedEventFails(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	parentMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "parent_notified_event_error",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		RootSessionID:    "parent_notified_event_error",
	}
	if err := store.Create(parentMeta, State{Status: StatusRunning, Phase: "turn_decide", UpdatedAt: now}); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	job := QueueJob{
		SchemaVersion:   1,
		ID:              "job_notified_event_error",
		CreatedAt:       now,
		UpdatedAt:       now,
		Status:          QueueStatusCompleted,
		ParentSessionID: parentMeta.ID,
		RootSessionID:   parentMeta.ID,
		SessionID:       "child_notified_event_error",
		SessionStatus:   StatusCompleted,
		Prompt:          "done",
		Mode:            ModeExec,
		Background:      true,
		FinalText:       "done",
	}
	if err := store.SaveParentCoordination(parentMeta.ID, ParentCoordination{
		SchemaVersion:       1,
		ParentSessionID:     parentMeta.ID,
		WaitMode:            "wait-all",
		UnresolvedQueueJobs: []string{job.ID},
		Parked:              true,
		UpdatedAt:           now,
	}); err != nil {
		t.Fatalf("save parent coordination: %v", err)
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save completed job: %v", err)
	}
	eventsPath := filepath.Join(store.SessionDir(parentMeta.ID), "events.jsonl")
	if err := os.Remove(eventsPath); err != nil {
		t.Fatalf("remove events: %v", err)
	}
	if err := os.Mkdir(eventsPath, 0o700); err != nil {
		t.Fatalf("replace events with directory: %v", err)
	}

	reconciled, err := store.LoadJob(job.ID)
	if err == nil || !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected queue notified event append error, got job=%#v err=%v", reconciled, err)
	}
	notifications, loadErr := store.LoadBackgroundNotifications(parentMeta.ID)
	if loadErr != nil {
		t.Fatalf("load background notifications after failed queue notified event: %v", loadErr)
	}
	if len(notifications) != 0 {
		t.Fatalf("failed queue notified event should roll back background notification, got %#v", notifications)
	}
}

func TestLoadJobRollsBackParentCoordinationWhenLifecycleEventFails(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	parentMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "parent_lifecycle_event_error",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		RootSessionID:    "parent_lifecycle_event_error",
	}
	if err := store.Create(parentMeta, State{Status: StatusRunning, Phase: "turn_decide", UpdatedAt: now}); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	job := QueueJob{
		SchemaVersion:   1,
		ID:              "job_lifecycle_event_error",
		CreatedAt:       now,
		UpdatedAt:       now,
		Status:          QueueStatusCompleted,
		ParentSessionID: parentMeta.ID,
		RootSessionID:   parentMeta.ID,
		SessionID:       "child_lifecycle_event_error",
		SessionStatus:   StatusCompleted,
		Prompt:          "done",
		Mode:            ModeExec,
		Background:      true,
		FinalText:       "done",
	}
	previousCoordination := ParentCoordination{
		SchemaVersion:       1,
		ParentSessionID:     parentMeta.ID,
		WaitMode:            "wait-all",
		UnresolvedQueueJobs: []string{job.ID},
		Parked:              true,
		UpdatedAt:           now,
	}
	if err := store.SaveParentCoordination(parentMeta.ID, previousCoordination); err != nil {
		t.Fatalf("save parent coordination: %v", err)
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save completed job: %v", err)
	}
	if err := store.EnsureBackgroundNotification(parentMeta.ID, NewBackgroundNotification(job)); err != nil {
		t.Fatalf("prewrite background notification: %v", err)
	}
	if err := store.ensureQueueLifecycleEvent(job, "queue.job.notified"); err != nil {
		t.Fatalf("prewrite queue notified event: %v", err)
	}
	eventsPath := filepath.Join(store.SessionDir(parentMeta.ID), "events.jsonl")
	if err := os.Remove(eventsPath); err != nil {
		t.Fatalf("remove events: %v", err)
	}
	if err := os.Mkdir(eventsPath, 0o700); err != nil {
		t.Fatalf("replace events with directory: %v", err)
	}

	reconciled, err := store.LoadJob(job.ID)
	if err == nil || !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected queue lifecycle event append error, got job=%#v err=%v", reconciled, err)
	}
	coordination, loadErr := store.LoadParentCoordination(parentMeta.ID)
	if loadErr != nil {
		t.Fatalf("load parent coordination after failed lifecycle event: %v", loadErr)
	}
	if !slices.Equal(coordination.UnresolvedQueueJobs, previousCoordination.UnresolvedQueueJobs) ||
		len(coordination.CompletedQueueJobs) != 0 ||
		len(coordination.FailedQueueJobs) != 0 ||
		!coordination.Parked {
		t.Fatalf("failed queue lifecycle event should roll back parent coordination, got %#v", coordination)
	}
}

func TestLoadJobRollsBackBackgroundNotificationWhenLifecycleEventFails(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	parentMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "parent_lifecycle_notification_error",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		RootSessionID:    "parent_lifecycle_notification_error",
	}
	if err := store.Create(parentMeta, State{Status: StatusRunning, Phase: "turn_decide", UpdatedAt: now}); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	job := QueueJob{
		SchemaVersion:   1,
		ID:              "job_lifecycle_notification_error",
		CreatedAt:       now,
		UpdatedAt:       now,
		Status:          QueueStatusCompleted,
		ParentSessionID: parentMeta.ID,
		RootSessionID:   parentMeta.ID,
		SessionID:       "child_lifecycle_notification_error",
		SessionStatus:   StatusCompleted,
		Prompt:          "done",
		Mode:            ModeExec,
		Background:      true,
		FinalText:       "done",
	}
	if err := store.SaveParentCoordination(parentMeta.ID, ParentCoordination{
		SchemaVersion:       1,
		ParentSessionID:     parentMeta.ID,
		WaitMode:            "wait-all",
		UnresolvedQueueJobs: []string{job.ID},
		Parked:              true,
		UpdatedAt:           now,
	}); err != nil {
		t.Fatalf("save parent coordination: %v", err)
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save completed job: %v", err)
	}
	blockedNotification := NewBackgroundNotification(QueueJob{
		ID:            job.ID,
		Status:        QueueStatusBlocked,
		SessionID:     job.SessionID,
		SessionStatus: StatusAwaitingInput,
		LastError:     "child session is resumable: awaiting_input",
	})
	if err := store.EnsureBackgroundNotification(parentMeta.ID, blockedNotification); err != nil {
		t.Fatalf("prewrite blocked background notification: %v", err)
	}
	if err := store.ensureQueueLifecycleEvent(job, "queue.job.notified"); err != nil {
		t.Fatalf("prewrite queue notified event: %v", err)
	}

	eventsPath := filepath.Join(store.SessionDir(parentMeta.ID), "events.jsonl")
	restore := beforeOpenNoSymlink
	blocked := false
	beforeOpenNoSymlink = func(openPath string, flags int) error {
		if blocked || flags&unix.O_APPEND == 0 || flags&(unix.O_WRONLY|unix.O_RDWR) == 0 || filepath.Clean(openPath) != eventsPath {
			return nil
		}
		blocked = true
		return errors.New("forced lifecycle event append failure")
	}
	defer func() {
		beforeOpenNoSymlink = restore
	}()

	reconciled, err := store.LoadJob(job.ID)
	if err == nil || !strings.Contains(err.Error(), "forced lifecycle event append failure") {
		t.Fatalf("expected queue lifecycle event append error, got job=%#v err=%v", reconciled, err)
	}
	notifications, loadErr := store.LoadBackgroundNotifications(parentMeta.ID)
	if loadErr != nil {
		t.Fatalf("load background notifications after failed lifecycle event: %v", loadErr)
	}
	if len(notifications) != 1 ||
		notifications[0].Status != QueueStatusBlocked ||
		notifications[0].SessionStatus != StatusAwaitingInput ||
		notifications[0].LastError != "child session is resumable: awaiting_input" {
		t.Fatalf("failed queue lifecycle event should roll back background notification, got %#v", notifications)
	}
}

func TestLoadJobRollsBackParentCoordinationWhenNotificationWriteFails(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	parentMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "parent_notification_write_error",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		RootSessionID:    "parent_notification_write_error",
	}
	if err := store.Create(parentMeta, State{Status: StatusRunning, Phase: "turn_decide", UpdatedAt: now}); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	job := QueueJob{
		SchemaVersion:   1,
		ID:              "job_notification_write_error",
		CreatedAt:       now,
		UpdatedAt:       now,
		Status:          QueueStatusCompleted,
		ParentSessionID: parentMeta.ID,
		RootSessionID:   parentMeta.ID,
		SessionID:       "child_notification_write_error",
		SessionStatus:   StatusCompleted,
		Prompt:          "done",
		Mode:            ModeExec,
		Background:      true,
		FinalText:       "done",
	}
	previousCoordination := ParentCoordination{
		SchemaVersion:       1,
		ParentSessionID:     parentMeta.ID,
		WaitMode:            "wait-all",
		UnresolvedQueueJobs: []string{job.ID},
		Parked:              true,
		UpdatedAt:           now,
	}
	if err := store.SaveParentCoordination(parentMeta.ID, previousCoordination); err != nil {
		t.Fatalf("save parent coordination: %v", err)
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save completed job: %v", err)
	}
	backgroundPath := filepath.Join(store.SessionDir(parentMeta.ID), "control", "background.jsonl")
	if err := os.Remove(backgroundPath); err != nil {
		t.Fatalf("remove background notifications: %v", err)
	}
	if err := os.Mkdir(backgroundPath, 0o700); err != nil {
		t.Fatalf("replace background notifications with directory: %v", err)
	}

	reconciled, err := store.LoadJob(job.ID)
	if err == nil || !strings.Contains(err.Error(), "background.jsonl") {
		t.Fatalf("expected background notification write error, got job=%#v err=%v", reconciled, err)
	}
	if err := os.Remove(backgroundPath); err != nil {
		t.Fatalf("unblock background notifications: %v", err)
	}
	coordination, loadErr := store.LoadParentCoordination(parentMeta.ID)
	if loadErr != nil {
		t.Fatalf("load coordination after failed notification write: %v", loadErr)
	}
	if !slices.Equal(coordination.UnresolvedQueueJobs, previousCoordination.UnresolvedQueueJobs) ||
		len(coordination.CompletedQueueJobs) != 0 ||
		len(coordination.FailedQueueJobs) != 0 ||
		!coordination.Parked {
		t.Fatalf("failed notification write should roll back parent coordination, got %#v", coordination)
	}
}

func TestLoadJobRollsBackBlockedParentStateWhenLifecycleEventFails(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	staleLease := time.Now().UTC().Add(-QueueRunningStaleAfter - time.Minute).Format(time.RFC3339Nano)
	parentMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "parent_blocked_lifecycle_error",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		RootSessionID:    "parent_blocked_lifecycle_error",
	}
	if err := store.Create(parentMeta, State{Status: StatusRunning, Phase: "turn_decide", UpdatedAt: now}); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	job := QueueJob{
		SchemaVersion:   1,
		ID:              "job_blocked_lifecycle_error",
		CreatedAt:       staleLease,
		UpdatedAt:       staleLease,
		Status:          QueueStatusRunning,
		ClaimedAt:       staleLease,
		HeartbeatAt:     staleLease,
		ParentSessionID: parentMeta.ID,
		RootSessionID:   parentMeta.ID,
		SessionID:       "child_blocked_lifecycle_error",
		SessionStatus:   StatusRunning,
		Prompt:          "pause for input",
		Mode:            ModeExec,
		Background:      true,
	}
	childMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               job.SessionID,
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		ParentSessionID:  parentMeta.ID,
		RootSessionID:    parentMeta.ID,
		QueueJobID:       job.ID,
		Depth:            1,
	}
	if err := store.Create(childMeta, State{Status: StatusAwaitingInput, Phase: "turn_decide", UpdatedAt: now}); err != nil {
		t.Fatalf("create child: %v", err)
	}
	previousCoordination := ParentCoordination{
		SchemaVersion:       1,
		ParentSessionID:     parentMeta.ID,
		WaitMode:            "wait-all",
		UnresolvedQueueJobs: []string{job.ID},
		Parked:              true,
		UpdatedAt:           now,
	}
	if err := store.SaveParentCoordination(parentMeta.ID, previousCoordination); err != nil {
		t.Fatalf("save parent coordination: %v", err)
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save running job: %v", err)
	}

	eventsPath := filepath.Join(store.SessionDir(parentMeta.ID), "events.jsonl")
	restore := beforeOpenNoSymlink
	blocked := false
	beforeOpenNoSymlink = func(openPath string, flags int) error {
		if blocked || flags&unix.O_APPEND == 0 || flags&(unix.O_WRONLY|unix.O_RDWR) == 0 || filepath.Clean(openPath) != eventsPath {
			return nil
		}
		blocked = true
		return errors.New("forced blocked lifecycle event append failure")
	}
	defer func() {
		beforeOpenNoSymlink = restore
	}()

	reconciled, err := store.LoadJob(job.ID)
	if err == nil || !strings.Contains(err.Error(), "forced blocked lifecycle event append failure") {
		t.Fatalf("expected queue blocked lifecycle event append error, got job=%#v err=%v", reconciled, err)
	}
	notifications, loadErr := store.LoadBackgroundNotifications(parentMeta.ID)
	if loadErr != nil {
		t.Fatalf("load notifications after failed blocked lifecycle event: %v", loadErr)
	}
	if len(notifications) != 0 {
		t.Fatalf("failed blocked lifecycle event should roll back background notification, got %#v", notifications)
	}
	coordination, loadErr := store.LoadParentCoordination(parentMeta.ID)
	if loadErr != nil {
		t.Fatalf("load coordination after failed blocked lifecycle event: %v", loadErr)
	}
	if !slices.Equal(coordination.UnresolvedQueueJobs, previousCoordination.UnresolvedQueueJobs) ||
		len(coordination.CompletedQueueJobs) != 0 ||
		len(coordination.FailedQueueJobs) != 0 ||
		!coordination.Parked {
		t.Fatalf("failed blocked lifecycle event should roll back parent coordination, got %#v", coordination)
	}
}

func TestLoadJobRepairsBlockedParentNotificationAndEvent(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	staleLease := time.Now().UTC().Add(-QueueRunningStaleAfter - time.Minute).Format(time.RFC3339Nano)
	parentMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "parent_blocked_repair",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		RootSessionID:    "parent_blocked_repair",
	}
	if err := store.Create(parentMeta, State{Status: StatusRunning, Phase: "turn_decide", UpdatedAt: now}); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	job := QueueJob{
		SchemaVersion:   1,
		ID:              "job_blocked_repair",
		CreatedAt:       staleLease,
		UpdatedAt:       staleLease,
		Status:          QueueStatusRunning,
		ClaimedAt:       staleLease,
		HeartbeatAt:     staleLease,
		ParentSessionID: parentMeta.ID,
		RootSessionID:   parentMeta.ID,
		SessionID:       "child_blocked_repair",
		SessionStatus:   StatusRunning,
		Prompt:          "pause for input",
		Mode:            ModeExec,
		Background:      true,
	}
	childMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               job.SessionID,
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		ParentSessionID:  parentMeta.ID,
		RootSessionID:    parentMeta.ID,
		QueueJobID:       job.ID,
		Depth:            1,
	}
	if err := store.Create(childMeta, State{Status: StatusAwaitingInput, Phase: "turn_decide", UpdatedAt: now}); err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := store.SaveParentCoordination(parentMeta.ID, ParentCoordination{
		SchemaVersion:       1,
		ParentSessionID:     parentMeta.ID,
		WaitMode:            "wait-all",
		UnresolvedQueueJobs: []string{job.ID},
		Parked:              true,
		UpdatedAt:           now,
	}); err != nil {
		t.Fatalf("save parent coordination: %v", err)
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save running job: %v", err)
	}

	repaired, err := store.LoadJob(job.ID)
	if err != nil {
		t.Fatalf("load repaired job: %v", err)
	}
	if repaired.Status != QueueStatusBlocked || repaired.SessionStatus != StatusAwaitingInput {
		t.Fatalf("expected blocked repaired job, got %#v", repaired)
	}
	coordination, err := store.LoadParentCoordination(parentMeta.ID)
	if err != nil {
		t.Fatalf("load parent coordination: %v", err)
	}
	if !slices.Equal(coordination.UnresolvedQueueJobs, []string{job.ID}) ||
		len(coordination.CompletedQueueJobs) != 0 ||
		len(coordination.FailedQueueJobs) != 0 ||
		!coordination.Parked {
		t.Fatalf("blocked repair should keep parent coordination unresolved, got %#v", coordination)
	}
	notifications, err := store.LoadBackgroundNotifications(parentMeta.ID)
	if err != nil {
		t.Fatalf("load background notifications: %v", err)
	}
	if len(notifications) != 1 ||
		notifications[0].QueueJobID != job.ID ||
		notifications[0].Status != QueueStatusBlocked ||
		notifications[0].SessionStatus != StatusAwaitingInput ||
		notifications[0].DeliveryStatus != BackgroundNotificationPending {
		t.Fatalf("expected pending blocked notification, got %#v", notifications)
	}
	eventsList, err := store.LoadEvents(parentMeta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	var foundNotified, foundBlocked bool
	for _, evt := range eventsList {
		jobID, _ := evt.Data["job_id"].(string)
		if jobID != job.ID {
			continue
		}
		switch evt.Type {
		case "queue.job.notified":
			foundNotified = true
		case "queue.job.blocked":
			foundBlocked = true
		}
	}
	if !foundNotified || !foundBlocked {
		t.Fatalf("expected queue.job.notified and queue.job.blocked repair events, got notified=%v blocked=%v events=%#v", foundNotified, foundBlocked, eventsList)
	}
}

func TestLoadAndListJobsPreferTerminalDuplicateStatusFile(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	if err := store.ensureQueueDirs(); err != nil {
		t.Fatalf("ensure queue dirs: %v", err)
	}
	base := time.Now().UTC()
	running := QueueJob{
		SchemaVersion:  1,
		ID:             "job_duplicate_status",
		CreatedAt:      base.Format(time.RFC3339Nano),
		UpdatedAt:      base.Format(time.RFC3339Nano),
		Status:         QueueStatusRunning,
		ClaimedAt:      base.Format(time.RFC3339Nano),
		HeartbeatAt:    base.Format(time.RFC3339Nano),
		Prompt:         "do work",
		Mode:           ModeExec,
		Background:     true,
		SessionStatus:  StatusRunning,
		ProcessStartID: "stale-process",
	}
	completed := running
	completed.Status = QueueStatusCompleted
	completed.UpdatedAt = base.Add(time.Second).Format(time.RFC3339Nano)
	completed.HeartbeatAt = base.Add(time.Second).Format(time.RFC3339Nano)
	completed.SessionStatus = StatusCompleted
	completed.FinalText = "done"
	if err := store.writeJSONFile(store.queueJobPath(QueueStatusRunning, running.ID), running); err != nil {
		t.Fatalf("write running duplicate: %v", err)
	}
	if err := store.writeJSONFile(store.queueJobPath(QueueStatusCompleted, completed.ID), completed); err != nil {
		t.Fatalf("write completed duplicate: %v", err)
	}

	loaded, err := store.LoadJob(running.ID)
	if err != nil {
		t.Fatalf("load job: %v", err)
	}
	if loaded.Status != QueueStatusCompleted || loaded.FinalText != "done" {
		t.Fatalf("expected completed duplicate to win, got %#v", loaded)
	}
	if _, err := os.Stat(store.queueJobPath(QueueStatusRunning, running.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected stale running duplicate removed, got %v", err)
	}
	if _, err := os.Stat(store.queueJobPath(QueueStatusCompleted, running.ID)); err != nil {
		t.Fatalf("expected completed queue file to remain, got %v", err)
	}

	if err := store.writeJSONFile(store.queueJobPath(QueueStatusRunning, running.ID), running); err != nil {
		t.Fatalf("restore running duplicate: %v", err)
	}
	jobs, err := store.ListJobs(10)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != running.ID || jobs[0].Status != QueueStatusCompleted {
		t.Fatalf("expected one completed canonical job, got %#v", jobs)
	}
	if _, err := os.Stat(store.queueJobPath(QueueStatusRunning, running.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected list to remove stale running duplicate, got %v", err)
	}
}

func TestLoadJobRepairsMissingTerminalBackgroundNotification(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)
	parentMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "parent_missing_notification",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		RootSessionID:    "parent_missing_notification",
	}
	parentState := State{
		Status:    StatusCompleted,
		Phase:     "turn_decide",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.Create(parentMeta, parentState); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	job := QueueJob{
		SchemaVersion:   1,
		ID:              "job_missing_notification",
		CreatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		Status:          QueueStatusCompleted,
		ParentSessionID: parentMeta.ID,
		RootSessionID:   parentMeta.ID,
		SessionID:       "child_missing_notification",
		SessionStatus:   StatusCompleted,
		AgentName:       "child",
		AgentRole:       "generator",
		Prompt:          "already completed",
		Mode:            ModeExec,
		Background:      true,
		FinalText:       "done",
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save terminal job: %v", err)
	}
	before, err := store.LoadBackgroundNotifications(parentMeta.ID)
	if err != nil {
		t.Fatalf("load notifications before repair: %v", err)
	}
	if len(before) != 0 {
		t.Fatalf("expected missing notification before repair, got %#v", before)
	}
	if _, err := store.LoadJob(job.ID); err != nil {
		t.Fatalf("load terminal job: %v", err)
	}
	notifications, err := store.LoadBackgroundNotifications(parentMeta.ID)
	if err != nil {
		t.Fatalf("load notifications after repair: %v", err)
	}
	if len(notifications) != 1 || notifications[0].QueueJobID != job.ID {
		t.Fatalf("expected repaired notification for %s, got %#v", job.ID, notifications)
	}
	eventsList, err := store.LoadEvents(parentMeta.ID)
	if err != nil {
		t.Fatalf("load events after repair: %v", err)
	}
	seen := map[string]bool{}
	for _, evt := range eventsList {
		jobID, _ := evt.Data["job_id"].(string)
		if jobID == job.ID {
			seen[evt.Type] = true
		}
	}
	if !seen["queue.job.notified"] || !seen["queue.job.completed"] {
		t.Fatalf("expected repaired lifecycle events, got %#v", seen)
	}
	if _, err := store.LoadJob(job.ID); err != nil {
		t.Fatalf("reload terminal job: %v", err)
	}
	notifications, err = store.LoadBackgroundNotifications(parentMeta.ID)
	if err != nil {
		t.Fatalf("reload notifications: %v", err)
	}
	if len(notifications) != 1 {
		t.Fatalf("expected repair to stay idempotent, got %#v", notifications)
	}
}

func TestSyncQueueVisiblePathsRejectsRequestedSymlinkEscape(t *testing.T) {
	requestedWorkdir := t.TempDir()
	effectiveWorkdir := t.TempDir()
	outside := t.TempDir()

	outputPath := filepath.Join(effectiveWorkdir, "reports", "child-two.md")
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		t.Fatalf("mkdir effective reports: %v", err)
	}
	if err := os.WriteFile(outputPath, []byte("CHILD_TWO_OK\n"), 0o600); err != nil {
		t.Fatalf("write effective output: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(requestedWorkdir, "reports")); err != nil {
		t.Fatalf("symlink requested reports: %v", err)
	}

	visible := syncQueueVisiblePaths(requestedWorkdir, effectiveWorkdir, []string{"reports/child-two.md"})
	if len(visible) != 0 {
		t.Fatalf("expected symlink escape copy to be skipped, got %#v", visible)
	}
	if _, err := os.Stat(filepath.Join(outside, "child-two.md")); !os.IsNotExist(err) {
		t.Fatalf("outside symlink target should not be written, stat err=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(requestedWorkdir, "reports")); err != nil {
		t.Fatalf("requested symlink should be left untouched: %v", err)
	}
}

func TestCollectQueueVisiblePathsAllowsDotPrefixedDirectory(t *testing.T) {
	workdir := t.TempDir()
	outputPath := filepath.Join(workdir, "..reports", "child-two.md")
	visible := collectQueueVisiblePaths(workdir, []Message{NewToolMessage([]ToolResult{{
		Name:     "write_file",
		Metadata: map[string]any{"path": outputPath},
	}})})

	if len(visible) != 1 || visible[0] != filepath.ToSlash(filepath.Join("..reports", "child-two.md")) {
		t.Fatalf("expected dot-prefixed child path to stay visible, got %#v", visible)
	}
}

func TestLoadJobPreservesResumableChildAsBlocked(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Now().UTC().Format(time.RFC3339Nano)
	job := QueueJob{
		ID:        "job-resumable",
		Status:    QueueStatusBlocked,
		Prompt:    "continue later",
		Mode:      ModeRun,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save blocked job: %v", err)
	}
	childMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "child-resumable",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
		QueueJobID:       job.ID,
	}
	if err := store.Create(childMeta, State{Status: StatusAwaitingInput, Phase: "turn_decide", UpdatedAt: now}); err != nil {
		t.Fatalf("create child: %v", err)
	}

	repaired, err := store.LoadJob(job.ID)
	if err != nil {
		t.Fatalf("load job: %v", err)
	}
	if repaired.Status != QueueStatusBlocked {
		t.Fatalf("expected blocked job for resumable child, got %#v", repaired)
	}
	if repaired.SessionStatus != StatusAwaitingInput {
		t.Fatalf("expected awaiting child status, got %#v", repaired)
	}
	state, err := store.LoadState(childMeta.ID)
	if err != nil {
		t.Fatalf("load child state: %v", err)
	}
	if state.Status != StatusAwaitingInput {
		t.Fatalf("resumable child state should not be forced terminal, got %#v", state)
	}
}

func stringSliceContains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}
