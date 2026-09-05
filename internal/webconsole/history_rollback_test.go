package webconsole

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aegis-agent/internal/session"
)

// historyRollbackFixture creates a session root with two durable sessions and
// moves both into a history transaction backup.
func historyRollbackFixture(t *testing.T) (root string, tx *webHistoryMutationTransaction) {
	t.Helper()
	parent := t.TempDir()
	root = filepath.Join(parent, "sessions")
	for _, sessionID := range []string{"session_alpha", "session_beta"} {
		dir := filepath.Join(root, sessionID)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "messages.jsonl"), []byte(sessionID+"\n"), 0o600); err != nil {
			t.Fatalf("write messages: %v", err)
		}
	}
	tx, err := newWebHistoryMutationTransaction(root, "clear")
	if err != nil {
		t.Fatalf("new history transaction: %v", err)
	}
	for _, sessionID := range []string{"session_alpha", "session_beta"} {
		if err := tx.MovePath(filepath.Join(root, sessionID)); err != nil {
			t.Fatalf("move %s: %v", sessionID, err)
		}
	}
	return root, tx
}

func historyRollbackPayload(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(path, "messages.jsonl"))
	if err != nil {
		t.Fatalf("read messages under %s: %v", path, err)
	}
	return string(data)
}

// retainedHistoryBackupRoot finds the single retained `.sessions.clear-*`
// backup directory next to a session root.
func retainedHistoryBackupRoot(t *testing.T, sessionRoot string) string {
	t.Helper()
	parent := filepath.Dir(sessionRoot)
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("read backup parent %s: %v", parent, err)
	}
	var backups []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".sessions.clear-") && entry.IsDir() {
			backups = append(backups, filepath.Join(parent, entry.Name()))
		}
	}
	if len(backups) != 1 {
		t.Fatalf("expected exactly one retained clear backup next to %s, got %v", sessionRoot, backups)
	}
	return backups[0]
}

func TestHistoryRollbackRemovesBackupAfterFullRestore(t *testing.T) {
	root, tx := historyRollbackFixture(t)
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if payload := historyRollbackPayload(t, filepath.Join(root, "session_alpha")); payload != "session_alpha\n" {
		t.Fatalf("session_alpha payload=%q", payload)
	}
	if payload := historyRollbackPayload(t, filepath.Join(root, "session_beta")); payload != "session_beta\n" {
		t.Fatalf("session_beta payload=%q", payload)
	}
	if _, err := os.Stat(tx.backupRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup root must be removed after a confirmed full restore, got %v", err)
	}
}

func TestHistoryRollbackRetainsBackupWhenDestinationConflicts(t *testing.T) {
	root, tx := historyRollbackFixture(t)
	// A concurrent CLI or second web service can reoccupy the original name;
	// historyMu cannot protect cross-process writers.
	conflict := filepath.Join(root, "session_beta")
	if err := os.WriteFile(conflict, []byte("intruder\n"), 0o600); err != nil {
		t.Fatalf("create conflict: %v", err)
	}

	err := tx.Rollback()
	if err == nil {
		t.Fatal("expected rollback error on destination conflict")
	}
	if !strings.Contains(err.Error(), "backup retained at") || !strings.Contains(err.Error(), tx.backupRoot) {
		t.Fatalf("rollback error must point at the retained backup location, got %v", err)
	}
	if payload := historyRollbackPayload(t, filepath.Join(tx.backupRoot, "session_beta")); payload != "session_beta\n" {
		t.Fatalf("unrestored session_beta must survive in the backup, got %q", payload)
	}
	if payload := historyRollbackPayload(t, filepath.Join(root, "session_alpha")); payload != "session_alpha\n" {
		t.Fatalf("session_alpha must be restored despite the sibling conflict, got %q", payload)
	}

	// Retrying after the operator removes the conflicting occupant must
	// complete the restore idempotently instead of failing on the entries that
	// the first attempt already restored.
	if err := os.Remove(conflict); err != nil {
		t.Fatalf("remove conflict: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback retry: %v", err)
	}
	if payload := historyRollbackPayload(t, filepath.Join(root, "session_beta")); payload != "session_beta\n" {
		t.Fatalf("session_beta must be restored by the retry, got %q", payload)
	}
	if _, err := os.Stat(tx.backupRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup root must be removed once every entry is restored, got %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback after completion must stay idempotent, got %v", err)
	}
}

func TestHistoryRollbackRetainsBackupWhenParentRecreationFails(t *testing.T) {
	root, tx := historyRollbackFixture(t)
	// Replacing the emptied session root with a regular file makes parent
	// recreation fail for every entry; no entry may be lost.
	if err := os.Remove(root); err != nil {
		t.Fatalf("remove session root: %v", err)
	}
	if err := os.WriteFile(root, []byte("occupied\n"), 0o600); err != nil {
		t.Fatalf("reoccupy session root: %v", err)
	}

	err := tx.Rollback()
	if err == nil {
		t.Fatal("expected rollback error when the session root cannot be recreated")
	}
	if !strings.Contains(err.Error(), "backup retained at") {
		t.Fatalf("rollback error must retain the backup pointer, got %v", err)
	}
	if payload := historyRollbackPayload(t, filepath.Join(tx.backupRoot, "session_alpha")); payload != "session_alpha\n" {
		t.Fatalf("session_alpha must survive in the backup, got %q", payload)
	}
	if payload := historyRollbackPayload(t, filepath.Join(tx.backupRoot, "session_beta")); payload != "session_beta\n" {
		t.Fatalf("session_beta must survive in the backup, got %q", payload)
	}
}

func TestClearSessionsPrepareFailureSurfacesDeferredRollbackFailure(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	root := svc.store.Root()
	if err := svc.store.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "session_alpha"), 0o700); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "session_alpha", "messages.jsonl"), []byte("session_alpha\n"), 0o600); err != nil {
		t.Fatalf("write messages: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	// "zlink" sorts after "session_alpha": the session moves first, then the
	// symlink entry aborts the prepare loop and the deferred rollback runs.
	if err := os.Symlink(outside, filepath.Join(root, "zlink")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	afterWebHistoryPrepareMoveFailure = func() {
		// Simulate a concurrent process reoccupying the moved session's
		// original name before the deferred rollback restores it.
		if err := os.WriteFile(filepath.Join(root, "session_alpha"), []byte("intruder\n"), 0o600); err != nil {
			t.Errorf("reoccupy session_alpha: %v", err)
		}
	}
	defer func() { afterWebHistoryPrepareMoveFailure = nil }()

	_, err = svc.prepareClearHistoryTransaction()
	if err == nil {
		t.Fatal("expected prepare failure")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("prepare error must keep the original move failure, got %v", err)
	}
	if !strings.Contains(err.Error(), "backup retained at") {
		t.Fatalf("prepare error must surface the deferred rollback failure and backup location, got %v", err)
	}
	backupRoot := retainedHistoryBackupRoot(t, root)
	if payload := historyRollbackPayload(t, filepath.Join(backupRoot, "session_alpha")); payload != "session_alpha\n" {
		t.Fatalf("unrestored session must survive in the retained backup, got %q", payload)
	}
}

func TestClearSessionsPrepareFailureRestoresMovesWhenRollbackSucceeds(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	root := svc.store.Root()
	if err := svc.store.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "session_alpha"), 0o700); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "session_alpha", "messages.jsonl"), []byte("session_alpha\n"), 0o600); err != nil {
		t.Fatalf("write messages: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "zlink")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	_, err = svc.prepareClearHistoryTransaction()
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink prepare failure, got %v", err)
	}
	if strings.Contains(err.Error(), "backup retained") {
		t.Fatalf("clean deferred rollback must not report unrestored entries, got %v", err)
	}
	if payload := historyRollbackPayload(t, filepath.Join(root, "session_alpha")); payload != "session_alpha\n" {
		t.Fatalf("session must be restored by the deferred rollback, got %q", payload)
	}
}

func TestClearSessionsAuditFailureWithRestoreConflictRetainsBackup(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "clear_conflict_session")
	meta.RootSessionID = meta.ID
	if err := svc.store.Create(meta, testSessionState(session.StatusCompleted)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sessionPath := filepath.Join(svc.store.Root(), meta.ID)
	if err := os.WriteFile(filepath.Join(sessionPath, "messages.jsonl"), []byte("clear_conflict_session\n"), 0o600); err != nil {
		t.Fatalf("write messages: %v", err)
	}
	svc.beforeAppendAuditEvent = func(eventType string, _ map[string]any) error {
		if eventType != "web.sessions.clear" {
			return nil
		}
		// Simulate a concurrent process reoccupying the session name after the
		// clear transaction moved it into the backup but before the restore.
		if err := os.WriteFile(sessionPath, []byte("intruder\n"), 0o600); err != nil {
			t.Errorf("reoccupy session path: %v", err)
		}
		return errors.New("blocked sessions clear audit append")
	}
	defer func() { svc.beforeAppendAuditEvent = nil }()

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/clear", nil)
	req.Header.Set(webMutationHeader, "1")
	svc.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected blocked clear response, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "blocked sessions clear audit append") || !strings.Contains(body, "backup retained at") {
		t.Fatalf("response must report the audit failure and the retained backup location, got %s", body)
	}
	backupRoot := retainedHistoryBackupRoot(t, svc.store.Root())
	if payload := historyRollbackPayload(t, filepath.Join(backupRoot, meta.ID)); payload != "clear_conflict_session\n" {
		t.Fatalf("unrestored session must remain readable in the retained backup, got %q", payload)
	}
}
