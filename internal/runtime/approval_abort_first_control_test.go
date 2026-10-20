package runtime

import (
	"reflect"
	"strings"
	"testing"

	"aegis-agent/internal/session"
)

// The old window fixture incorrectly required peer SaveState to succeed after
// jointly releasing two flock waiters. This lawful ordering explains why that
// expectation is invalid; it does not assert a production CAS defect.
func TestPreparedApprovalAbortFirstRejectsStalePeerControl(t *testing.T) {
	r, p, _ := newPreparedApprovalWindowFixture(t)
	if err := r.AbortPreparedApproval(p, nil); err != nil {
		t.Fatal(err)
	}
	restored, err := r.store.LoadState(p.meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Status != p.originalState.Status || restored.RunGeneration != p.originalState.RunGeneration {
		t.Fatalf("abort did not restore original identity: %+v", restored)
	}
	peer := session.NewStore(r.store.Root())
	stale := p.state
	stale.Turn++
	if err := peer.SaveState(p.meta.ID, stale); err == nil || !strings.Contains(err.Error(), "run generation changed") {
		t.Fatalf("stale peer unexpectedly committed after abort: %v", err)
	}
	after, err := peer.LoadState(p.meta.ID)
	if err != nil || !reflect.DeepEqual(restored, after) {
		t.Fatalf("stale peer overwrote abort: %+v %v", after, err)
	}
	var record approvalPreparationRecord
	if err := readApprovalPreparationForTest(r, p.meta.ID, &record); err != nil || record.Phase != "aborted" {
		t.Fatalf("abort preparation fact: %+v %v", record, err)
	}
}
