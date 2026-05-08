package webconsole

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
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
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
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

func webAuditLogPath(sessionRoot string) string {
	if sessionRoot == "" {
		return webAuditLogName
	}
	return filepath.Join(filepath.Dir(sessionRoot), webAuditLogName)
}
