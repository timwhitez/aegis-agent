package webconsole

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

func validateAuditLogAndBatch(file *os.File, events []webAuditEvent) error {
	scan, err := scanWebAuditLog(file, nil, true)
	if err != nil {
		return err
	}
	if err := validateAuditBatchIDs(scan.seenIDs, events); err != nil {
		return err
	}
	_, err = file.Seek(0, io.SeekEnd)
	return err
}

func scanExistingAuditLog(file *os.File) (map[string]struct{}, error) {
	scan, err := scanWebAuditLog(file, nil, true)
	if err != nil {
		return nil, err
	}
	return scan.seenIDs, nil
}

func validateAuditBatchIDs(seenIDs map[string]struct{}, events []webAuditEvent) error {
	if seenIDs == nil {
		seenIDs = map[string]struct{}{}
	}
	for _, event := range events {
		id := strings.TrimSpace(event.ID)
		if _, ok := seenIDs[id]; ok {
			return fmt.Errorf("duplicate audit event id %q", id)
		}
		seenIDs[id] = struct{}{}
	}
	return nil
}

func validateExistingAuditLog(file *os.File) error {
	_, err := scanWebAuditLog(file, nil, false)
	return err
}

func validateAuditBatchUnique(file *os.File, events []webAuditEvent) error {
	if len(events) == 0 {
		return nil
	}
	return validateAuditLogAndBatch(file, events)
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
