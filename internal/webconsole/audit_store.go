package webconsole

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"aegis-agent/internal/fileutil"

	"golang.org/x/sys/unix"
)

func ensureWebAuditLogAtPath(path string, forceFull bool) error {
	return withWebAuditFileLock(path, func() error {
		file, err := openAuditLogNoSymlink(path)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = loadWebAuditState(path, file, forceFull)
		return err
	})
}

func withWebAuditFileLock(logPath string, fn func() error) error {
	if fn == nil {
		return errors.New("audit lock callback is required")
	}
	parent := filepath.Dir(logPath)
	if err := rejectAuditSymlinkAncestors(parent); err != nil {
		return err
	}
	if err := fileutil.MkdirAllNoSymlink(parent, 0o700); err != nil {
		return err
	}
	lock, err := fileutil.OpenFileNoSymlink(webAuditLockPath(logPath), unix.O_CREAT|unix.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("lock web audit log: %w", err)
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()
	return fn()
}

func openAuditLogNoSymlink(path string) (*os.File, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" {
		return nil, fmt.Errorf("audit log path is required")
	}
	parent := filepath.Dir(path)
	if err := rejectAuditSymlinkAncestors(parent); err != nil {
		return nil, err
	}
	if err := fileutil.MkdirAllNoSymlink(parent, 0o700); err != nil {
		return nil, err
	}
	if err := rejectAuditSymlinkAncestors(parent); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("refusing to append to symlinked audit log: %s", path)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("refusing to append to audit log directory: %s", path)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if beforeOpenAuditLog != nil {
		if err := beforeOpenAuditLog(path); err != nil {
			return nil, err
		}
	}
	return fileutil.OpenFileNoSymlink(path, unix.O_CREAT|unix.O_RDWR|unix.O_APPEND, 0o600)
}

func loadWebAuditState(path string, file *os.File, forceFull bool) (webAuditCheckpoint, error) {
	checkpoint, exists, err := readWebAuditCheckpoint(path)
	if err != nil {
		return webAuditCheckpoint{}, err
	}
	if exists && !forceFull {
		matches, err := webAuditCheckpointMatches(path, file, checkpoint)
		if err != nil {
			return webAuditCheckpoint{}, err
		}
		if matches {
			return checkpoint, nil
		}
	}
	var expected *webAuditCheckpoint
	if exists {
		expected = &checkpoint
	}
	if err := ensureWebAuditFileStillAtPath(path, file); err != nil {
		return webAuditCheckpoint{}, err
	}
	scan, err := scanWebAuditLog(file, expected, true)
	if err != nil {
		return webAuditCheckpoint{}, err
	}
	if !exists && scan.checkpoint.Epoch != "" {
		return webAuditCheckpoint{}, fmt.Errorf("audit checkpoint is missing for a log that already contains structural event IDs")
	}
	if err := ensureWebAuditFileStillAtPath(path, file); err != nil {
		return webAuditCheckpoint{}, err
	}
	if scan.checkpoint.Epoch == "" {
		scan.checkpoint.Epoch, err = newWebAuditEpoch()
		if err != nil {
			return webAuditCheckpoint{}, err
		}
	}
	scan.checkpoint.SchemaVersion = webAuditCheckpointSchemaVersion
	if err := writeWebAuditCheckpoint(path, scan.checkpoint); err != nil {
		return webAuditCheckpoint{}, err
	}
	return scan.checkpoint, nil
}

func webAuditCheckpointMatches(path string, file *os.File, checkpoint webAuditCheckpoint) (bool, error) {
	if err := validateWebAuditCheckpoint(checkpoint); err != nil {
		return false, err
	}
	if err := ensureWebAuditFileStillAtPath(path, file); err != nil {
		return false, err
	}
	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	if info.Size() != checkpoint.Size || info.ModTime().UnixNano() != checkpoint.ModTimeUnixNano {
		return false, nil
	}
	identity, identityOK := auditFileIdentity(info)
	if checkpoint.FileIdentity == "" || !identityOK || identity != checkpoint.FileIdentity {
		return false, nil
	}
	stamp, stampOK := auditFileChangeStamp(info)
	if checkpoint.ChangeStamp != "" && (!stampOK || stamp != checkpoint.ChangeStamp) {
		return false, nil
	}
	headDigest, tailOffset, tailDigest, err := auditProbeDigests(file, info.Size())
	if err != nil {
		return false, err
	}
	if checkpoint.HeadSHA256 != hex.EncodeToString(headDigest[:]) || checkpoint.TailOffset != tailOffset || checkpoint.TailSHA256 != hex.EncodeToString(tailDigest[:]) {
		return false, nil
	}
	return true, nil
}
