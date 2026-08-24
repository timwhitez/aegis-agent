package webconsole

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func auditScanFixtureEvent(id string) webAuditEvent {
	return webAuditEvent{
		SchemaVersion: 1,
		ID:            id,
		Type:          "web.test",
		Time:          time.Unix(1, 0).UTC().Format(time.RFC3339Nano),
		Data:          map[string]any{"id": id},
	}
}

func writeAuditScanFixture(t *testing.T, path string, events ...webAuditEvent) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("open audit fixture: %v", err)
	}
	encoder := json.NewEncoder(file)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			_ = file.Close()
			t.Fatalf("encode audit fixture: %v", err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close audit fixture: %v", err)
	}
}

func openAuditScanFixture(t *testing.T, path string) *os.File {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open audit fixture for validation: %v", err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func TestValidateAuditLogAndBatchAcceptsUniqueIDsAndSeeksToEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	writeAuditScanFixture(t, path,
		auditScanFixtureEvent("history-1"),
		auditScanFixtureEvent("history-2"),
	)
	file := openAuditScanFixture(t, path)
	batch := []webAuditEvent{
		auditScanFixtureEvent("candidate-1"),
		auditScanFixtureEvent("candidate-2"),
	}
	if err := validateAuditLogAndBatch(file, batch); err != nil {
		t.Fatalf("validate audit log and batch: %v", err)
	}
	position, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatalf("read audit file position: %v", err)
	}
	info, err := file.Stat()
	if err != nil {
		t.Fatalf("stat audit fixture: %v", err)
	}
	if position != info.Size() {
		t.Fatalf("audit file position=%d want end=%d", position, info.Size())
	}
}

func TestValidateAuditLogAndBatchRejectsDuplicateHistoricalID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	writeAuditScanFixture(t, path,
		auditScanFixtureEvent("duplicate"),
		auditScanFixtureEvent("duplicate"),
	)
	file := openAuditScanFixture(t, path)
	if err := validateAuditLogAndBatch(file, nil); err == nil || !strings.Contains(err.Error(), "duplicate audit event id") {
		t.Fatalf("duplicate historical ID was accepted: %v", err)
	}
}

func TestValidateAuditLogAndBatchRejectsCandidateCollisions(t *testing.T) {
	t.Run("with history", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "audit.jsonl")
		writeAuditScanFixture(t, path, auditScanFixtureEvent("duplicate"))
		file := openAuditScanFixture(t, path)
		if err := validateAuditLogAndBatch(file, []webAuditEvent{auditScanFixtureEvent("duplicate")}); err == nil || !strings.Contains(err.Error(), "duplicate audit event id") {
			t.Fatalf("historical collision was accepted: %v", err)
		}
	})

	t.Run("within batch", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "audit.jsonl")
		writeAuditScanFixture(t, path, auditScanFixtureEvent("history"))
		file := openAuditScanFixture(t, path)
		batch := []webAuditEvent{
			auditScanFixtureEvent("duplicate"),
			auditScanFixtureEvent("duplicate"),
		}
		if err := validateAuditLogAndBatch(file, batch); err == nil || !strings.Contains(err.Error(), "duplicate audit event id") {
			t.Fatalf("within-batch collision was accepted: %v", err)
		}
	})
}

func TestValidateAuditLogAndBatchStillFailsClosedOnInvalidHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	invalid := auditScanFixtureEvent("invalid-schema")
	invalid.SchemaVersion = 2
	writeAuditScanFixture(t, path, invalid)
	file := openAuditScanFixture(t, path)
	if err := validateAuditLogAndBatch(file, []webAuditEvent{auditScanFixtureEvent("candidate")}); err == nil || !strings.Contains(err.Error(), "unsupported audit event schema_version") {
		t.Fatalf("invalid historical event was accepted: %v", err)
	}
}
