package session

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func stateCASFixture(t *testing.T) (*Store, string) {
	t.Helper()
	store := NewStore(t.TempDir())
	id := NewSessionID()
	if err := store.Create(SessionMetadata{SchemaVersion: 1, ID: id, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: t.TempDir(), Mode: ModeRun, Provider: "fake", Model: "fake", CompletionPolicy: CompletionPolicyInteractive}, State{Status: StatusAwaitingInput, Phase: "plan_approval"}); err != nil {
		t.Fatal(err)
	}
	return store, id
}

func TestSaveStateIfCurrentRejectsLaterClaimWithSameStatusAndPhase(t *testing.T) {
	store, id := stateCASFixture(t)
	first, err := store.ClaimSessionRun(id, StatusAwaitingInput)
	if err != nil {
		t.Fatal(err)
	}
	paused := first
	paused.Status = StatusPaused
	if err := store.SaveState(id, paused); err != nil {
		t.Fatal(err)
	}
	second, err := NewStore(store.Root()).ClaimSessionRun(id, StatusPaused)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != second.Status || first.Phase != second.Phase || first.UpdatedAt == second.UpdatedAt {
		t.Fatal("fixture requires distinct generations with same status and phase")
	}
	saved, err := store.SaveStateIfCurrent(id, first, paused)
	if err != nil || saved {
		t.Fatalf("stale claim restored later generation: saved=%v err=%v", saved, err)
	}
	current, err := store.LoadState(id)
	if err != nil {
		t.Fatal(err)
	}
	if current.UpdatedAt != second.UpdatedAt || current.Status != StatusRunning {
		t.Fatalf("CAS changed second run: %#v", current)
	}
}

func TestSaveStateIfCurrentConcurrentStoresPermitOneRestorer(t *testing.T) {
	store, id := stateCASFixture(t)
	expected, err := store.ClaimSessionRun(id, StatusAwaitingInput)
	if err != nil {
		t.Fatal(err)
	}
	next := expected
	next.Status = StatusAwaitingInput
	var winners atomic.Int32
	var group sync.WaitGroup
	for range 2 {
		group.Go(func() {
			saved, err := NewStore(store.Root()).SaveStateIfCurrent(id, expected, next)
			if err != nil {
				t.Errorf("state CAS: %v", err)
			}
			if saved {
				winners.Add(1)
			}
		})
	}
	group.Wait()
	if winners.Load() != 1 {
		t.Fatalf("expected one successful restore, got %d", winners.Load())
	}
}
