package webconsole

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go-cli-agent/internal/fileutil"

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

type pendingWebAuditEvent struct {
	eventType string
	data      map[string]any
}

func (s *Service) appendAuditEvent(eventType string, data map[string]any) error {
	return s.appendAuditEvents(pendingWebAuditEvent{eventType: eventType, data: data})
}

func (s *Service) appendAuditEvents(pending ...pendingWebAuditEvent) error {
	if s == nil || s.store == nil {
		return nil
	}
	if len(pending) == 0 {
		return nil
	}
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	now := time.Now().UTC()
	events := make([]webAuditEvent, 0, len(pending))
	for i, item := range pending {
		if s.beforeAppendAuditEvent != nil {
			if err := s.beforeAppendAuditEvent(item.eventType, item.data); err != nil {
				return err
			}
		}
		event := webAuditEvent{
			SchemaVersion: 1,
			ID:            fmt.Sprintf("audit_%d_%d", now.UnixNano(), i),
			Type:          item.eventType,
			Time:          now.Format(time.RFC3339Nano),
			Data:          item.data,
		}
		if err := validateAuditEvent(event); err != nil {
			return err
		}
		events = append(events, event)
	}
	path := webAuditLogPath(s.store.Root())
	file, err := openAuditLogNoSymlink(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := validateExistingAuditLog(file); err != nil {
		return err
	}
	if err := validateAuditBatchUnique(file, events); err != nil {
		return err
	}
	offset, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	for _, event := range events {
		if err := enc.Encode(event); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(file, buf.String()); err != nil {
		if truncateErr := file.Truncate(offset); truncateErr != nil {
			return errors.Join(err, fmt.Errorf("restore audit log after failed batch append: %w", truncateErr))
		}
		return err
	}
	return nil
}

func (s *Service) ensureAuditLogWritable() error {
	if s == nil || s.store == nil {
		return nil
	}
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	file, err := openAuditLogNoSymlink(webAuditLogPath(s.store.Root()))
	if err != nil {
		return err
	}
	defer file.Close()
	return validateExistingAuditLog(file)
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
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_APPEND|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func validateExistingAuditLog(file *os.File) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	seenIDs := map[string]struct{}{}
	scanner := bufio.NewScanner(file)
	line := 0
	for scanner.Scan() {
		line++
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		var event webAuditEvent
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return fmt.Errorf("invalid audit log record %d: %w", line, err)
		}
		if err := validateAuditEvent(event); err != nil {
			return fmt.Errorf("invalid audit log record %d: %w", line, err)
		}
		id := strings.TrimSpace(event.ID)
		if _, ok := seenIDs[id]; ok {
			return fmt.Errorf("invalid audit log record %d: duplicate audit event id %q", line, id)
		}
		seenIDs[id] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	_, err := file.Seek(0, io.SeekEnd)
	return err
}

func validateAuditBatchUnique(file *os.File, events []webAuditEvent) error {
	if len(events) == 0 {
		return nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	seenIDs := map[string]struct{}{}
	scanner := bufio.NewScanner(file)
	line := 0
	for scanner.Scan() {
		line++
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		var event webAuditEvent
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return fmt.Errorf("invalid audit log record %d: %w", line, err)
		}
		id := strings.TrimSpace(event.ID)
		if id != "" {
			seenIDs[id] = struct{}{}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	for _, event := range events {
		id := strings.TrimSpace(event.ID)
		if _, ok := seenIDs[id]; ok {
			return fmt.Errorf("duplicate audit event id %q", id)
		}
		seenIDs[id] = struct{}{}
	}
	_, err := file.Seek(0, io.SeekEnd)
	return err
}

func validateAuditEvent(event webAuditEvent) error {
	if event.SchemaVersion != 1 {
		return fmt.Errorf("unsupported audit event schema_version %d", event.SchemaVersion)
	}
	if strings.TrimSpace(event.ID) == "" {
		return fmt.Errorf("audit event id is required")
	}
	if strings.TrimSpace(event.Type) == "" {
		return fmt.Errorf("audit event type is required")
	}
	if strings.TrimSpace(event.Time) == "" {
		return fmt.Errorf("audit event time is required")
	}
	if _, err := time.Parse(time.RFC3339Nano, event.Time); err != nil {
		return fmt.Errorf("invalid audit event time %q: %w", event.Time, err)
	}
	return nil
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
