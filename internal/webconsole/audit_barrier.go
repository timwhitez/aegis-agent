package webconsole

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"aegis-agent/internal/fileutil"

	"golang.org/x/sys/unix"
)

const webAuditBarrierSuffix = ".pending.json"

// Hooks inject failures before the real operation; nil always uses the actual
// filesystem. Tests must restore these hooks and must not install them in
// parallel with other audit operations.
var beforeSyncWebAuditFile func(path, phase string) error
var beforeTruncateWebAuditFile func(path string, offset int64) error
var beforeRemoveWebAuditBarrier func(path string) error

func webAuditBarrierPath(logPath string) string { return logPath + webAuditBarrierSuffix }

func ensureNoWebAuditBarrier(logPath string) error {
	path := webAuditBarrierPath(logPath)
	if _, err := os.Lstat(path); err == nil {
		// Do not parse or repair a marker here. Even a malformed, empty or
		// non-regular marker means the previous outcome cannot be inferred.
		return fmt.Errorf("audit recovery required: unresolved append marker %s; stop all writers and reconcile the operation before removing the marker", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// createWebAuditBarrier is called under the stable audit lock. The exclusive,
// durable marker precedes every byte of a new batch. A crash or failed rollback
// cannot subsequently turn an uncertain batch into an automatically accepted
// structural tail, even after process restart.
func createWebAuditBarrier(logPath string, state webAuditCheckpoint, batchBytes int) error {
	path := webAuditBarrierPath(logPath)
	file, err := fileutil.OpenFileNoSymlink(path, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return fmt.Errorf("create audit append marker %s: %w", path, err)
	}
	defer file.Close()
	if _, err := hardenWebAuditRegularFile(path, file); err != nil {
		return err
	}
	data, err := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		Epoch         string `json:"epoch"`
		Offset        int64  `json:"offset"`
		BatchBytes    int    `json:"batch_bytes"`
	}{1, state.Epoch, state.Size, batchBytes})
	if err != nil {
		return err
	}
	data = append(data, '\n')
	written, err := file.Write(data)
	if err == nil && written != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return fmt.Errorf("write audit append marker %s: %w", path, err)
	}
	if err := syncWebAuditFile(file, "marker"); err != nil {
		return fmt.Errorf("sync audit append marker %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return err
	}
	return syncWebAuditDirectory(logPath, "marker-directory")
}

func syncWebAuditFile(file *os.File, phase string) error {
	if beforeSyncWebAuditFile != nil {
		if err := beforeSyncWebAuditFile(file.Name(), phase); err != nil {
			return err
		}
	}
	return file.Sync()
}

func syncWebAuditDirectory(logPath, phase string) error {
	dir, err := fileutil.OpenDirNoSymlink(filepath.Dir(logPath))
	if err != nil {
		return err
	}
	defer dir.Close()
	return syncWebAuditFile(dir, phase)
}

func removeWebAuditBarrier(logPath string) error {
	path := webAuditBarrierPath(logPath)
	if beforeRemoveWebAuditBarrier != nil {
		if err := beforeRemoveWebAuditBarrier(path); err != nil {
			return err
		}
	}
	if err := fileutil.RemoveFileNoSymlink(path); err != nil {
		return err
	}
	return syncWebAuditDirectory(logPath, "marker-remove-directory")
}

// abortWebAuditAppend returns an error to the business caller only after trying
// to restore and persist its prior log boundary. If that outcome is uncertain,
// the marker already on disk blocks every later append and recovery attempt.
func abortWebAuditAppend(logPath string, file *os.File, offset int64, cause error) error {
	rollbackErr := ensureWebAuditFileStillAtPath(logPath, file)
	if rollbackErr == nil && beforeTruncateWebAuditFile != nil {
		rollbackErr = beforeTruncateWebAuditFile(logPath, offset)
	}
	if rollbackErr == nil {
		rollbackErr = file.Truncate(offset)
	}
	if rollbackErr == nil {
		rollbackErr = syncWebAuditFile(file, "rollback")
	}
	if rollbackErr != nil {
		return errors.Join(cause, fmt.Errorf("audit rollback is uncertain; recovery marker retained at %s: %w", webAuditBarrierPath(logPath), rollbackErr))
	}
	if err := removeWebAuditBarrier(logPath); err != nil {
		return errors.Join(cause, fmt.Errorf("audit rollback persisted but marker cleanup requires recovery at %s: %w", webAuditBarrierPath(logPath), err))
	}
	return cause
}
