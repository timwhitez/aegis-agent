package webconsole

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aegis-agent/internal/config"

	"golang.org/x/sys/unix"
)

func TestPrepareAuditLogRejectsMalformedHistoryBeforeServiceConstruction(t *testing.T) {
	cfg := config.Default()
	cfg.Session.Dir = filepath.Join(t.TempDir(), "sessions")
	path := webAuditLogPath(cfg.Session.Dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AEGIS_AGENT_ENV_FILE", filepath.Join(t.TempDir(), ".env"))
	err := PrepareAuditLog(cfg, filepath.Join(t.TempDir(), "config.yaml"))
	if err == nil || !strings.Contains(err.Error(), "invalid audit log record") {
		t.Fatalf("malformed history was accepted before service construction: %v", err)
	}
}

func TestAuditManagedFilesRejectNonRegularPathsWithoutBlocking(t *testing.T) {
	assertReturns := func(t *testing.T, call func() error) error {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- call() }()
		select {
		case err := <-done:
			return err
		case <-time.After(2 * time.Second):
			t.Fatal("audit operation blocked on a non-regular managed path")
			return nil
		}
	}

	t.Run("log fifo", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), webAuditLogName)
		if err := unix.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		err := assertReturns(t, func() error { return initializeWebAuditLogAtPath(path) })
		if err == nil || !strings.Contains(err.Error(), "non-regular audit log") {
			t.Fatalf("FIFO log was accepted: %v", err)
		}
	})

	t.Run("checkpoint fifo", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), webAuditLogName)
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := unix.Mkfifo(webAuditCheckpointPath(path), 0o600); err != nil {
			t.Fatal(err)
		}
		err := assertReturns(t, func() error { return initializeWebAuditLogAtPath(path) })
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("FIFO checkpoint was accepted: %v", err)
		}
	})

	t.Run("lock fifo", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), webAuditLogName)
		if err := unix.Mkfifo(webAuditLockPath(path), 0o600); err != nil {
			t.Fatal(err)
		}
		err := assertReturns(t, func() error { return initializeWebAuditLogAtPath(path) })
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("FIFO lock was accepted: %v", err)
		}
	})
}

func TestAuditInitializationHardensManagedFilesToOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), webAuditLogName)
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := initializeWebAuditLogAtPath(path); err != nil {
		t.Fatal(err)
	}
	for _, managedPath := range webAuditManagedPaths(path) {
		info, err := os.Lstat(managedPath)
		if err != nil {
			t.Fatalf("stat %s: %v", managedPath, err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode=%s want regular 0600", managedPath, info.Mode())
		}
	}
}

func TestAuditOptionalMetadataMatching(t *testing.T) {
	for _, test := range []struct {
		name     string
		stored   string
		actual   string
		actualOK bool
		want     bool
	}{
		{name: "both unavailable", want: true},
		{name: "capability appeared", actual: "1:2", actualOK: true, want: false},
		{name: "capability disappeared", stored: "1:2", want: false},
		{name: "equal", stored: "1:2", actual: "1:2", actualOK: true, want: true},
		{name: "different", stored: "1:2", actual: "1:3", actualOK: true, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := auditOptionalMetadataMatches(test.stored, test.actual, test.actualOK); got != test.want {
				t.Fatalf("got %v want %v", got, test.want)
			}
		})
	}
}

func TestAuditCheckpointRejectsImpossibleState(t *testing.T) {
	emptyDigest := sha256.Sum256(nil)
	valid := webAuditCheckpoint{
		SchemaVersion: webAuditCheckpointSchemaVersion,
		Epoch:         strings.Repeat("a", 32),
		Size:          0,
		RecordCount:   0,
		ChainSHA256:   strings.Repeat("0", sha256.Size*2),
		HeadSHA256:    hex.EncodeToString(emptyDigest[:]),
		TailOffset:    0,
		TailSHA256:    hex.EncodeToString(emptyDigest[:]),
	}
	if err := validateWebAuditCheckpoint(valid); err != nil {
		t.Fatalf("valid empty checkpoint rejected: %v", err)
	}

	nonEmptyChain := valid
	nonEmptyChain.ChainSHA256 = strings.Repeat("1", sha256.Size*2)
	if err := validateWebAuditCheckpoint(nonEmptyChain); err == nil {
		t.Fatal("empty checkpoint with non-empty chain was accepted")
	}

	tooManyRecords := valid
	tooManyRecords.Size = 1
	tooManyRecords.RecordCount = 2
	tooManyRecords.TailOffset = 0
	if err := validateWebAuditCheckpoint(tooManyRecords); err == nil {
		t.Fatal("checkpoint with more records than bytes was accepted")
	}

	wrongTailOffset := valid
	wrongTailOffset.Size = webAuditProbeBytes + 10
	wrongTailOffset.RecordCount = 1
	wrongTailOffset.TailOffset = 0
	if err := validateWebAuditCheckpoint(wrongTailOffset); err == nil {
		t.Fatal("checkpoint with wrong tail offset was accepted")
	}
}
