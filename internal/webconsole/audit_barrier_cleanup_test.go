package webconsole

import (
	"errors"
	"syscall"
	"testing"
)

func TestAuditRollbackRemainsSafeWhenMarkerUnlinkSyncFails(t *testing.T) {
	path := durabilityFixture(t)
	beforeSyncWebAuditFile = func(_ string, phase string) error {
		if phase == "append" || phase == "marker-remove-directory" {
			return syscall.EIO
		}
		return nil
	}
	if err := appendWebAuditEventsAtPath(path, []webAuditEvent{durabilityEvent()}); !errors.Is(err, syscall.EIO) {
		t.Fatalf("got %v", err)
	}
	if len(durabilityRead(t, path)) != 0 {
		t.Fatal("rollback was not persisted before marker unlink")
	}
	beforeSyncWebAuditFile = nil
	// The unlink is visible now, but might be undone by power loss. With or
	// without the marker, no aborted bytes can be adopted after synced rollback.
	if err := initializeWebAuditLogAtPath(path); err != nil {
		t.Fatal(err)
	}
	cp, _, err := readWebAuditCheckpoint(path)
	if err != nil || cp.RecordCount != 0 {
		t.Fatalf("checkpoint=%+v err=%v", cp, err)
	}
}
