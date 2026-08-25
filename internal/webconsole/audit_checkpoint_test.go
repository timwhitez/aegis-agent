package webconsole

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newAuditTestPath(t testing.TB) string {
	t.Helper()
	return filepath.Join(t.TempDir(), webAuditLogName)
}

func auditCheckpointTestEvent(eventType string, data map[string]any) webAuditEvent {
	return webAuditEvent{
		SchemaVersion: 1,
		Type:          eventType,
		Time:          time.Unix(1, 0).UTC().Format(time.RFC3339Nano),
		Data:          data,
	}
}

func readCheckpointForTest(t testing.TB, path string) webAuditCheckpoint {
	t.Helper()
	checkpoint, ok, err := readWebAuditCheckpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("checkpoint missing")
	}
	return checkpoint
}

func readEventsForTest(t testing.TB, path string) []webAuditEvent {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var events []webAuditEvent
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var event webAuditEvent
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

func resetAuditTestHooks() {
	beforeWriteAuditCheckpoint = nil
	auditRecordDecodeObserver = nil
}

func TestAuditCheckpointFastPathAvoidsHistoricalDecode(t *testing.T) {
	resetAuditTestHooks()
	t.Cleanup(resetAuditTestHooks)
	path := newAuditTestPath(t)
	if err := initializeWebAuditLogAtPath(path); err != nil {
		t.Fatal(err)
	}
	if err := appendWebAuditEventsAtPath(path, []webAuditEvent{auditCheckpointTestEvent("web.first", map[string]any{"n": 1})}); err != nil {
		t.Fatal(err)
	}
	decoded := 0
	auditRecordDecodeObserver = func() { decoded++ }
	if err := appendWebAuditEventsAtPath(path, []webAuditEvent{auditCheckpointTestEvent("web.second", map[string]any{"n": 2})}); err != nil {
		t.Fatal(err)
	}
	if decoded != 0 {
		t.Fatalf("fast append decoded %d historical records", decoded)
	}
	events := readEventsForTest(t, path)
	if len(events) != 2 {
		t.Fatalf("events=%d", len(events))
	}
	for index, event := range events {
		epoch, offset, structured, err := parseWebAuditStructuredID(event.ID)
		if err != nil || !structured || !validWebAuditEpoch(epoch) || offset < 0 {
			t.Fatalf("event %d id=%q epoch=%q offset=%d structured=%v err=%v", index, event.ID, epoch, offset, structured, err)
		}
	}
	checkpoint := readCheckpointForTest(t, path)
	if checkpoint.RecordCount != 2 {
		t.Fatalf("checkpoint=%+v", checkpoint)
	}
}

func TestAuditCheckpointRecoversDurableUncheckpointedTail(t *testing.T) {
	resetAuditTestHooks()
	t.Cleanup(resetAuditTestHooks)
	path := newAuditTestPath(t)
	if err := initializeWebAuditLogAtPath(path); err != nil {
		t.Fatal(err)
	}
	fail := true
	beforeWriteAuditCheckpoint = func(string, []byte) error {
		if fail {
			fail = false
			return errors.New("checkpoint unavailable")
		}
		return nil
	}
	if err := appendWebAuditEventsAtPath(path, []webAuditEvent{auditCheckpointTestEvent("web.durable", nil)}); err != nil {
		t.Fatalf("durable append must not fail solely because checkpoint publication failed: %v", err)
	}
	beforeWriteAuditCheckpoint = nil
	decoded := 0
	auditRecordDecodeObserver = func() { decoded++ }
	if err := appendWebAuditEventsAtPath(path, []webAuditEvent{auditCheckpointTestEvent("web.after-recovery", nil)}); err != nil {
		t.Fatal(err)
	}
	if decoded != 1 {
		t.Fatalf("recovery decoded=%d want 1 durable tail record", decoded)
	}
	checkpoint := readCheckpointForTest(t, path)
	if checkpoint.RecordCount != 2 {
		t.Fatalf("record_count=%d", checkpoint.RecordCount)
	}
}

func TestAuditCheckpointRejectsHistoricalRewriteOnStartup(t *testing.T) {
	resetAuditTestHooks()
	t.Cleanup(resetAuditTestHooks)
	path := newAuditTestPath(t)
	if err := initializeWebAuditLogAtPath(path); err != nil {
		t.Fatal(err)
	}
	if err := appendWebAuditEventsAtPath(path, []webAuditEvent{auditCheckpointTestEvent("web.original", map[string]any{"value": "AAAA"})}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rewritten := bytes.Replace(data, []byte("AAAA"), []byte("BBBB"), 1)
	if bytes.Equal(data, rewritten) || len(data) != len(rewritten) {
		t.Fatal("rewrite fixture did not preserve size")
	}
	if err := os.WriteFile(path, rewritten, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := initializeWebAuditLogAtPath(path); err == nil || !strings.Contains(err.Error(), "historical prefix changed") {
		t.Fatalf("rewrite accepted: %v", err)
	}
}

func TestAuditCheckpointRejectsTruncationAndReplacement(t *testing.T) {
	resetAuditTestHooks()
	t.Cleanup(resetAuditTestHooks)
	t.Run("truncation", func(t *testing.T) {
		path := newAuditTestPath(t)
		if err := initializeWebAuditLogAtPath(path); err != nil {
			t.Fatal(err)
		}
		if err := appendWebAuditEventsAtPath(path, []webAuditEvent{auditCheckpointTestEvent("web.original", nil)}); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(path, 0); err != nil {
			t.Fatal(err)
		}
		if err := appendWebAuditEventsAtPath(path, []webAuditEvent{auditCheckpointTestEvent("web.next", nil)}); err == nil || !strings.Contains(err.Error(), "truncated") {
			t.Fatalf("truncation accepted: %v", err)
		}
	})
	t.Run("replacement", func(t *testing.T) {
		path := newAuditTestPath(t)
		if err := initializeWebAuditLogAtPath(path); err != nil {
			t.Fatal(err)
		}
		if err := appendWebAuditEventsAtPath(path, []webAuditEvent{auditCheckpointTestEvent("web.original", nil)}); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		replacement := path + ".replacement"
		if err := os.WriteFile(replacement, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, path); err != nil {
			t.Fatal(err)
		}
		if err := appendWebAuditEventsAtPath(path, []webAuditEvent{auditCheckpointTestEvent("web.next", nil)}); err == nil || !strings.Contains(err.Error(), "identity changed") {
			t.Fatalf("replacement accepted: %v", err)
		}
	})
}

func TestAuditCheckpointRejectsEpochMismatchOnStartup(t *testing.T) {
	resetAuditTestHooks()
	t.Cleanup(resetAuditTestHooks)
	path := newAuditTestPath(t)
	if err := initializeWebAuditLogAtPath(path); err != nil {
		t.Fatal(err)
	}
	if err := appendWebAuditEventsAtPath(path, []webAuditEvent{auditCheckpointTestEvent("web.original", nil)}); err != nil {
		t.Fatal(err)
	}
	checkpoint := readCheckpointForTest(t, path)
	originalEpoch := checkpoint.Epoch
	checkpoint.Epoch = strings.Repeat("f", 32)
	if checkpoint.Epoch == originalEpoch {
		checkpoint.Epoch = strings.Repeat("e", 32)
	}
	if err := writeWebAuditCheckpoint(path, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := initializeWebAuditLogAtPath(path); err == nil || !strings.Contains(err.Error(), "does not match structural history epoch") {
		t.Fatalf("checkpoint epoch mismatch accepted: %v", err)
	}
}

func TestAuditManagedPathsRejectConfigAndEnvSidecarAliases(t *testing.T) {
	resetAuditTestHooks()
	t.Cleanup(resetAuditTestHooks)
	cwd := t.TempDir()
	previousCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousCWD) })
	logPath := filepath.Join(cwd, webAuditLogName)
	svc := &Service{configPath: filepath.Join(cwd, "config.yaml")}

	for _, managedPath := range []string{webAuditCheckpointPath(logPath), webAuditLockPath(logPath)} {
		t.Run("config_"+filepath.Base(managedPath), func(t *testing.T) {
			svc.configPath = managedPath
			t.Setenv("AEGIS_AGENT_ENV_FILE", filepath.Join(cwd, ".env"))
			if err := svc.ensureWebAuditManagedPathsDistinct(logPath); err == nil || !strings.Contains(err.Error(), "config file and audit log must be separate") {
				t.Fatalf("config sidecar alias accepted: %v", err)
			}
		})
		t.Run("env_"+filepath.Base(managedPath), func(t *testing.T) {
			svc.configPath = filepath.Join(cwd, "config.yaml")
			t.Setenv("AEGIS_AGENT_ENV_FILE", managedPath)
			if err := svc.ensureWebAuditManagedPathsDistinct(logPath); err == nil || !strings.Contains(err.Error(), "env file and audit log must be separate") {
				t.Fatalf("env sidecar alias accepted: %v", err)
			}
		})
	}
}

func TestAuditCheckpointSerializesCooperatingWriters(t *testing.T) {
	resetAuditTestHooks()
	t.Cleanup(resetAuditTestHooks)
	path := newAuditTestPath(t)
	if err := initializeWebAuditLogAtPath(path); err != nil {
		t.Fatal(err)
	}
	const appends = 40
	var wg sync.WaitGroup
	errs := make(chan error, appends)
	for i := 0; i < appends; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- appendWebAuditEventsAtPath(path, []webAuditEvent{auditCheckpointTestEvent("web.concurrent", map[string]any{"n": i})})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := validateExistingAuditLog(file); err != nil {
		t.Fatal(err)
	}
	checkpoint := readCheckpointForTest(t, path)
	if checkpoint.RecordCount != appends {
		t.Fatalf("record_count=%d", checkpoint.RecordCount)
	}
}

func TestAuditCheckpointMigratesLegacyHistory(t *testing.T) {
	resetAuditTestHooks()
	t.Cleanup(resetAuditTestHooks)
	path := newAuditTestPath(t)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for i := 0; i < 2; i++ {
		event := auditCheckpointTestEvent("web.legacy", nil)
		event.ID = fmt.Sprintf("legacy-%d", i)
		if err := encoder.Encode(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := initializeWebAuditLogAtPath(path); err != nil {
		t.Fatal(err)
	}
	if err := appendWebAuditEventsAtPath(path, []webAuditEvent{auditCheckpointTestEvent("web.new", nil)}); err != nil {
		t.Fatal(err)
	}
	events := readEventsForTest(t, path)
	if len(events) != 3 {
		t.Fatalf("events=%d", len(events))
	}
	if _, _, structured, err := parseWebAuditStructuredID(events[2].ID); err != nil || !structured {
		t.Fatalf("new event id=%q structured=%v err=%v", events[2].ID, structured, err)
	}
}

func TestAuditCheckpointRejectsMissingCheckpointForStructuredHistory(t *testing.T) {
	resetAuditTestHooks()
	t.Cleanup(resetAuditTestHooks)
	path := newAuditTestPath(t)
	if err := initializeWebAuditLogAtPath(path); err != nil {
		t.Fatal(err)
	}
	if err := appendWebAuditEventsAtPath(path, []webAuditEvent{auditCheckpointTestEvent("web.original", nil)}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(webAuditCheckpointPath(path)); err != nil {
		t.Fatal(err)
	}
	if err := initializeWebAuditLogAtPath(path); err == nil || !strings.Contains(err.Error(), "checkpoint is missing") {
		t.Fatalf("structured history was re-anchored without its checkpoint: %v", err)
	}
}

func TestAuditCheckpointRejectsMalformedCheckpoint(t *testing.T) {
	resetAuditTestHooks()
	t.Cleanup(resetAuditTestHooks)
	path := newAuditTestPath(t)
	if err := initializeWebAuditLogAtPath(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(webAuditCheckpointPath(path), []byte(`{"schema_version":1,"unexpected":true}\n`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := appendWebAuditEventsAtPath(path, []webAuditEvent{auditCheckpointTestEvent("web.next", nil)}); err == nil || !strings.Contains(err.Error(), "invalid audit checkpoint") {
		t.Fatalf("malformed checkpoint accepted: %v", err)
	}
}

func TestAuditCheckpointRejectsOversizedActiveLog(t *testing.T) {
	resetAuditTestHooks()
	t.Cleanup(resetAuditTestHooks)
	path := newAuditTestPath(t)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxWebAuditLogBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := initializeWebAuditLogAtPath(path); err == nil || !strings.Contains(err.Error(), "retention limit") {
		t.Fatalf("oversized active log accepted: %v", err)
	}
}

func TestAuditStructuredIDRejectsWrongOffsetAndMixedEpochs(t *testing.T) {
	resetAuditTestHooks()
	t.Cleanup(resetAuditTestHooks)
	t.Run("wrong offset", func(t *testing.T) {
		path := newAuditTestPath(t)
		epoch := strings.Repeat("a", 32)
		event := auditCheckpointTestEvent("web.test", nil)
		event.ID = formatWebAuditStructuredID(epoch, 99)
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(file).Encode(event); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		file, err = os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if err := validateExistingAuditLog(file); err == nil || !strings.Contains(err.Error(), "does not match record offset") {
			t.Fatalf("wrong offset accepted: %v", err)
		}
	})

	t.Run("mixed epochs", func(t *testing.T) {
		path := newAuditTestPath(t)
		first := auditCheckpointTestEvent("web.first", nil)
		first.ID = formatWebAuditStructuredID(strings.Repeat("a", 32), 0)
		var firstLine bytes.Buffer
		if err := json.NewEncoder(&firstLine).Encode(first); err != nil {
			t.Fatal(err)
		}
		second := auditCheckpointTestEvent("web.second", nil)
		second.ID = formatWebAuditStructuredID(strings.Repeat("b", 32), int64(firstLine.Len()))
		var content bytes.Buffer
		content.Write(firstLine.Bytes())
		if err := json.NewEncoder(&content).Encode(second); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if err := validateExistingAuditLog(file); err == nil || !strings.Contains(err.Error(), "differs from earlier epoch") {
			t.Fatalf("mixed epochs accepted: %v", err)
		}
	})
}

func BenchmarkWebAuditAppendFastPathByHistorySize(b *testing.B) {
	for _, records := range []int{1_000, 10_000, 100_000} {
		b.Run(fmt.Sprintf("records_%d", records), func(b *testing.B) {
			resetAuditTestHooks()
			path := newAuditTestPath(b)
			file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				b.Fatal(err)
			}
			writer := bufio.NewWriterSize(file, 256<<10)
			encoder := json.NewEncoder(writer)
			for i := 0; i < records; i++ {
				event := auditCheckpointTestEvent("web.fixture", nil)
				event.ID = fmt.Sprintf("legacy-%d", i)
				if err := encoder.Encode(event); err != nil {
					b.Fatal(err)
				}
			}
			if err := writer.Flush(); err != nil {
				b.Fatal(err)
			}
			if err := file.Close(); err != nil {
				b.Fatal(err)
			}
			if err := initializeWebAuditLogAtPath(path); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := appendWebAuditEventsAtPath(path, []webAuditEvent{auditCheckpointTestEvent("web.benchmark", map[string]any{"iteration": i})}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
