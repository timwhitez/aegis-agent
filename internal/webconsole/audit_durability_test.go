package webconsole

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func durabilityEvent() webAuditEvent {
	return webAuditEvent{SchemaVersion: 1, Type: "web.config.write", Time: time.Unix(1, 0).UTC().Format(time.RFC3339Nano)}
}

func durabilityFixture(t *testing.T) string {
	t.Helper()
	t.Cleanup(func() {
		beforeSyncWebAuditFile = nil
		beforeTruncateWebAuditFile = nil
		beforeRemoveWebAuditBarrier = nil
		beforeWriteAuditCheckpoint = nil
		auditRecordDecodeObserver = nil
	})
	path := filepath.Join(t.TempDir(), webAuditLogName)
	if err := initializeWebAuditLogAtPath(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func durabilityRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAuditSyncFailureRollsBackBeforeReturningError(t *testing.T) {
	for _, count := range []int{1, 3} {
		t.Run(string(rune('0'+count)), func(t *testing.T) {
			path := durabilityFixture(t)
			checkpointBefore := durabilityRead(t, webAuditCheckpointPath(path))
			var phases []string
			beforeSyncWebAuditFile = func(_ string, phase string) error {
				phases = append(phases, phase)
				if phase == "append" {
					if len(durabilityRead(t, path)) == 0 {
						t.Fatal("failure injection must follow the actual batch write")
					}
					return syscall.EIO
				}
				return nil
			}
			batch := make([]webAuditEvent, count)
			for i := range batch {
				batch[i] = durabilityEvent()
			}
			if err := appendWebAuditEventsAtPath(path, batch); !errors.Is(err, syscall.EIO) {
				t.Fatalf("expected append sync failure, got %v", err)
			}
			if len(durabilityRead(t, path)) != 0 || !bytes.Equal(checkpointBefore, durabilityRead(t, webAuditCheckpointPath(path))) {
				t.Fatal("failed operation survived in the log or checkpoint advanced")
			}
			want := []string{"marker", "marker-directory", "append", "rollback", "marker-remove-directory"}
			if !reflect.DeepEqual(phases, want) {
				t.Fatalf("sync ordering=%v want %v", phases, want)
			}
			beforeSyncWebAuditFile = nil
			if _, err := os.Lstat(webAuditBarrierPath(path)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("successful rollback left marker: %v", err)
			}
			if err := initializeWebAuditLogAtPath(path); err != nil {
				t.Fatal(err)
			}
			if err := appendWebAuditEventsAtPath(path, []webAuditEvent{durabilityEvent()}); err != nil {
				t.Fatal(err)
			}
			cp, _, err := readWebAuditCheckpoint(path)
			if err != nil || cp.RecordCount != 1 {
				t.Fatalf("failed batch was adopted during recovery: %+v %v", cp, err)
			}
		})
	}
}

func TestAuditFailedRollbackRetainsPersistentBarrier(t *testing.T) {
	for _, failure := range []string{"truncate", "rollback-sync"} {
		t.Run(failure, func(t *testing.T) {
			path := durabilityFixture(t)
			checkpointBefore := durabilityRead(t, webAuditCheckpointPath(path))
			beforeSyncWebAuditFile = func(_ string, phase string) error {
				if phase == "append" || (phase == "rollback" && failure == "rollback-sync") {
					return syscall.EIO
				}
				return nil
			}
			if failure == "truncate" {
				beforeTruncateWebAuditFile = func(string, int64) error { return syscall.EIO }
			}
			if err := appendWebAuditEventsAtPath(path, []webAuditEvent{durabilityEvent()}); err == nil || !strings.Contains(err.Error(), "recovery marker retained") {
				t.Fatalf("uncertain rollback not reported: %v", err)
			}
			marker, err := os.Lstat(webAuditBarrierPath(path))
			if err != nil || marker.Mode().Perm() != 0o600 || !marker.Mode().IsRegular() {
				t.Fatalf("persistent barrier missing/unsafe: %v %v", marker, err)
			}
			beforeSyncWebAuditFile, beforeTruncateWebAuditFile = nil, nil
			for _, op := range []func() error{
				func() error { return initializeWebAuditLogAtPath(path) },
				func() error { return appendWebAuditEventsAtPath(path, []webAuditEvent{durabilityEvent()}) },
			} {
				if err := op(); err == nil || !strings.Contains(err.Error(), "audit recovery required") {
					t.Fatalf("uncertain tail automatically adopted: %v", err)
				}
			}
			if !bytes.Equal(checkpointBefore, durabilityRead(t, webAuditCheckpointPath(path))) {
				t.Fatal("checkpoint advanced over an uncertain batch")
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestAuditPendingRestartProbe$")
			cmd.Env = append(os.Environ(), "AEGIS_TEST_AUDIT_PENDING="+path)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("fresh process did not retain fail-closed state: %v\n%s", err, out)
			}
		})
	}
}

func TestAuditPendingRestartProbe(t *testing.T) {
	path := os.Getenv("AEGIS_TEST_AUDIT_PENDING")
	if path == "" {
		return
	}
	if err := initializeWebAuditLogAtPath(path); err == nil || !strings.Contains(err.Error(), "audit recovery required") {
		t.Fatalf("fresh process accepted unresolved append: %v", err)
	}
}

func TestAuditMarkerFailurePreventsBatchWrite(t *testing.T) {
	for _, phase := range []string{"marker", "marker-directory"} {
		t.Run(phase, func(t *testing.T) {
			path := durabilityFixture(t)
			beforeSyncWebAuditFile = func(_ string, current string) error {
				if current == phase {
					return syscall.EIO
				}
				return nil
			}
			if err := appendWebAuditEventsAtPath(path, []webAuditEvent{durabilityEvent()}); err == nil {
				t.Fatal("unsynced barrier was accepted")
			}
			if len(durabilityRead(t, path)) != 0 {
				t.Fatal("log changed before marker was durable")
			}
		})
	}
}

func TestAuditPostCommitCleanupFailureDoesNotReturnPreCommitError(t *testing.T) {
	path := durabilityFixture(t)
	beforeRemoveWebAuditBarrier = func(string) error { return syscall.EIO }
	if err := appendWebAuditEventsAtPath(path, []webAuditEvent{durabilityEvent()}); err != nil {
		t.Fatalf("durable audit commit must not ask caller to roll back: %v", err)
	}
	beforeRemoveWebAuditBarrier = nil
	if len(durabilityRead(t, path)) == 0 {
		t.Fatal("committed record absent")
	}
	if err := initializeWebAuditLogAtPath(path); err == nil {
		t.Fatal("cleanup failure must require explicit reconciliation")
	}
}

func TestAuditRecoverySyncPrecedesCheckpoint(t *testing.T) {
	for _, fixture := range []string{"empty", "legacy", "tail"} {
		t.Run(fixture, func(t *testing.T) {
			path := durabilityFixture(t)
			if fixture == "legacy" {
				if err := os.Remove(webAuditCheckpointPath(path)); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("{\"schema_version\":1,\"id\":\"legacy\",\"type\":\"web.test\",\"time\":\"1970-01-01T00:00:01Z\"}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if fixture == "tail" {
				cp, _, err := readWebAuditCheckpoint(path)
				if err != nil {
					t.Fatal(err)
				}
				encoded, _, err := encodeWebAuditBatch([]webAuditEvent{durabilityEvent()}, cp)
				if err != nil {
					t.Fatal(err)
				}
				// Simulate a complete tail visible in page cache after an older
				// writer exits before fsync. No marker exists for this legacy case.
				f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.WriteString(encoded); err != nil {
					t.Fatal(err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			var order []string
			beforeSyncWebAuditFile = func(_ string, phase string) error { order = append(order, phase); return nil }
			beforeWriteAuditCheckpoint = func(_ string, _ []byte) error { order = append(order, "checkpoint"); return nil }
			if err := initializeWebAuditLogAtPath(path); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(order, []string{"recovery", "checkpoint"}) {
				t.Fatalf("checkpoint can lead JSONL durability: %v", order)
			}
		})
	}
}

func TestAuditRecoverySyncFailureNeverAdvancesCheckpoint(t *testing.T) {
	for _, fixture := range []string{"new", "tail"} {
		t.Run(fixture, func(t *testing.T) {
			path := durabilityFixture(t)
			cpBefore := durabilityRead(t, webAuditCheckpointPath(path))
			if fixture == "new" {
				if err := os.Remove(webAuditCheckpointPath(path)); err != nil {
					t.Fatal(err)
				}
			} else {
				cp, _, err := readWebAuditCheckpoint(path)
				if err != nil {
					t.Fatal(err)
				}
				encoded, _, err := encodeWebAuditBatch([]webAuditEvent{durabilityEvent()}, cp)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			beforeSyncWebAuditFile = func(_ string, phase string) error {
				if phase == "recovery" {
					return syscall.EIO
				}
				return nil
			}
			published := false
			beforeWriteAuditCheckpoint = func(string, []byte) error { published = true; return nil }
			if err := initializeWebAuditLogAtPath(path); !errors.Is(err, syscall.EIO) {
				t.Fatalf("got %v", err)
			}
			if published {
				t.Fatal("checkpoint published despite failed JSONL fsync")
			}
			if fixture == "new" {
				if _, err := os.Stat(webAuditCheckpointPath(path)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("new checkpoint survived failed recovery: %v", err)
				}
			} else if !bytes.Equal(cpBefore, durabilityRead(t, webAuditCheckpointPath(path))) {
				t.Fatal("old checkpoint advanced")
			}
		})
	}
}

func TestAuditConfirmedTailRecoversAndFastPathStillSkipsHistory(t *testing.T) {
	path := durabilityFixture(t)
	beforeWriteAuditCheckpoint = func(string, []byte) error { return syscall.EIO }
	if err := appendWebAuditEventsAtPath(path, []webAuditEvent{durabilityEvent()}); err != nil {
		t.Fatal(err)
	}
	beforeWriteAuditCheckpoint = nil
	if err := initializeWebAuditLogAtPath(path); err != nil {
		t.Fatal(err)
	}
	decoded := 0
	auditRecordDecodeObserver = func() { decoded++ }
	if err := appendWebAuditEventsAtPath(path, []webAuditEvent{durabilityEvent()}); err != nil {
		t.Fatal(err)
	}
	if decoded != 0 {
		t.Fatalf("fast append decoded %d historical records", decoded)
	}
	cp, _, err := readWebAuditCheckpoint(path)
	if err != nil || cp.RecordCount != 2 {
		t.Fatalf("checkpoint=%+v err=%v", cp, err)
	}
}

func TestAuditUnexpectedBarrierCannotBypassRecovery(t *testing.T) {
	for _, kind := range []string{"empty", "directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			path := durabilityFixture(t)
			marker := webAuditBarrierPath(path)
			var err error
			switch kind {
			case "empty":
				err = os.WriteFile(marker, nil, 0o600)
			case "directory":
				err = os.Mkdir(marker, 0o700)
			case "symlink":
				err = os.Symlink(path, marker)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := initializeWebAuditLogAtPath(path); err == nil || !strings.Contains(err.Error(), "audit recovery required") {
				t.Fatalf("%s barrier accepted: %v", kind, err)
			}
			if len(durabilityRead(t, path)) != 0 {
				t.Fatal("uncertain input was modified")
			}
		})
	}
}

func TestAuditConcurrentProcessWriter(t *testing.T) {
	path := os.Getenv("AEGIS_TEST_AUDIT_WRITER")
	if path == "" {
		return
	}
	for i := 0; i < 4; i++ {
		if err := appendWebAuditEventsAtPath(path, []webAuditEvent{durabilityEvent()}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAuditBarrierSerializesAcrossProcesses(t *testing.T) {
	path := durabilityFixture(t)
	var commands []*exec.Cmd
	for i := 0; i < 3; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestAuditConcurrentProcessWriter$")
		cmd.Env = append(os.Environ(), "AEGIS_TEST_AUDIT_WRITER="+path)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, cmd)
	}
	for i := 0; i < 4; i++ {
		if err := appendWebAuditEventsAtPath(path, []webAuditEvent{durabilityEvent()}); err != nil {
			t.Error(err)
		}
	}
	for _, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Error(err)
		}
	}
	if err := initializeWebAuditLogAtPath(path); err != nil {
		t.Fatal(err)
	}
	cp, _, err := readWebAuditCheckpoint(path)
	if err != nil || cp.RecordCount != 16 {
		t.Fatalf("checkpoint=%+v err=%v", cp, err)
	}
}
