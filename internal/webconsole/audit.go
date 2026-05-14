package webconsole

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const webAuditLogName = "webconsole-audit.jsonl"

type webAuditEvent struct {
	SchemaVersion int            `json:"schema_version"`
	ID            string         `json:"id"`
	Type          string         `json:"type"`
	Time          string         `json:"time"`
	Data          map[string]any `json:"data,omitempty"`
}

func (s *Service) appendAuditEvent(eventType string, data map[string]any) error {
	if s == nil || s.store == nil {
		return nil
	}
	path := webAuditLogPath(s.store.Root())
	file, err := openAuditLogNoSymlink(path)
	if err != nil {
		return err
	}
	defer file.Close()
	event := webAuditEvent{
		SchemaVersion: 1,
		ID:            fmt.Sprintf("audit_%d", time.Now().UnixNano()),
		Type:          eventType,
		Time:          time.Now().UTC().Format(time.RFC3339Nano),
		Data:          data,
	}
	return json.NewEncoder(file).Encode(event)
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
	if err := os.MkdirAll(parent, 0o700); err != nil {
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
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_WRONLY|unix.O_APPEND|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func rejectAuditSymlinkAncestors(path string) error {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(abs)
	rest := strings.TrimPrefix(abs, volume)
	separator := string(os.PathSeparator)
	current := volume
	if strings.HasPrefix(rest, separator) {
		current += separator
		rest = strings.TrimPrefix(rest, separator)
	}
	if current == "" {
		current = "."
	}
	for _, part := range strings.Split(rest, separator) {
		if part == "" {
			continue
		}
		if current == separator || strings.HasSuffix(current, separator) {
			current += part
		} else {
			current = filepath.Join(current, part)
		}
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to append through symlinked audit path: %s", current)
		}
	}
	return nil
}

func webAuditLogPath(sessionRoot string) string {
	if sessionRoot == "" {
		return webAuditLogName
	}
	return filepath.Join(filepath.Dir(sessionRoot), webAuditLogName)
}
