package session

import (
	"reflect"
	"testing"
	"time"
)

func TestRunGenerationPreservesLegacyUnknownAndObservationalUpdates(t *testing.T) {
	store, id := stateCASFixture(t)
	legacy, err := store.LoadState(id)
	if err != nil {
		t.Fatal(err)
	}
	legacy.LoadedSkills = []string{"reviewed-skill"}
	if err := store.SaveState(id, legacy); err != nil {
		t.Fatal(err)
	}
	legacy, err = store.LoadState(id)
	if err != nil || legacy.RunGeneration != "" {
		t.Fatalf("ordinary save forged a legacy generation: %#v %v", legacy, err)
	}
	claimed, err := store.ClaimSessionRun(id, StatusAwaitingInput)
	if err != nil || claimed.RunGeneration == "" {
		t.Fatalf("ordinary claim has no durable identity: %#v %v", claimed, err)
	}
	if err := NewStore(store.Root()).AppendSteerRequest(id, NewSteerRequest("keep the approved scope", false)); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(store.Root()).RefreshPendingSteerCount(id); err != nil {
		t.Fatal(err)
	}
	observed, err := store.LoadState(id)
	if err != nil || observed.RunGeneration != claimed.RunGeneration || observed.PendingSteerCount != 1 || observed.UpdatedAt == claimed.UpdatedAt {
		t.Fatalf("queued steer replaced run identity: %#v %v", observed, err)
	}
	observed.LoadedSkills = append(observed.LoadedSkills, "new-skill")
	if err := store.SaveState(id, observed); err != nil {
		t.Fatal(err)
	}
	updated, err := store.LoadState(id)
	if err != nil || updated.RunGeneration != claimed.RunGeneration || len(updated.LoadedSkills) != 2 {
		t.Fatalf("observational save lost run identity or skills: %#v %v", updated, err)
	}
	next := updated
	next.RunGeneration = ""
	committed, saved, err := store.SwapStateIfCurrent(id, updated, next)
	if err != nil || !saved || committed.RunGeneration != claimed.RunGeneration {
		t.Fatalf("running CAS lost identity: %#v saved=%v err=%v", committed, saved, err)
	}
}

func TestRunGenerationRejectsOldSnapshotAcrossNewOrdinaryClaim(t *testing.T) {
	store, id := stateCASFixture(t)
	first, err := store.ClaimSessionRunWithGeneration(id, time.Now().UTC().Format(time.RFC3339Nano), StatusAwaitingInput)
	if err != nil {
		t.Fatal(err)
	}
	paused := first
	paused.Status = StatusPaused
	if err := store.SaveState(id, paused); err != nil {
		t.Fatal(err)
	}
	second, err := NewStore(store.Root()).ClaimSessionRun(id, StatusPaused)
	if err != nil || second.RunGeneration == "" || second.RunGeneration == first.RunGeneration {
		t.Fatalf("new ordinary claim reused approval identity: %#v %v", second, err)
	}
	if err := store.SaveState(id, first); err == nil {
		t.Fatal("old whole-state update republished its previous run identity")
	}
	// Every other state field, including UpdatedAt, now matches the current
	// state. The old durable identity alone must still reject its restoration.
	stale := second
	stale.RunGeneration = first.RunGeneration
	next := stale
	next.Status = StatusAwaitingInput
	if saved, err := store.SaveStateIfCurrent(id, stale, next); err != nil || saved {
		t.Fatalf("CAS accepted another run identity: saved=%v err=%v", saved, err)
	}
	actual, err := store.LoadState(id)
	if err != nil || !reflect.DeepEqual(actual, second) {
		t.Fatalf("stale writer changed the new run: %#v %v", actual, err)
	}
}

func TestRunGenerationCASCanRestoreOriginalLegacySnapshot(t *testing.T) {
	store, id := stateCASFixture(t)
	original, err := store.LoadState(id)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimSessionRunWithGeneration(id, time.Now().UTC().Format(time.RFC3339Nano), StatusAwaitingInput)
	if err != nil {
		t.Fatal(err)
	}
	restored, saved, err := store.SwapStateIfCurrent(id, claimed, original)
	if err != nil || !saved || restored.Status != original.Status || restored.RunGeneration != "" {
		t.Fatalf("CAS forged original legacy identity: %#v saved=%v err=%v", restored, saved, err)
	}
}

func TestRunGenerationRejectsLegacySnapshotAfterNewClaim(t *testing.T) {
	store, id := stateCASFixture(t)
	legacy, err := store.LoadState(id)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimSessionRun(id, StatusAwaitingInput)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveState(id, legacy); err == nil {
		t.Fatal("outdated unknown-generation snapshot adopted the new run identity")
	}
	actual, err := store.LoadState(id)
	if err != nil || !reflect.DeepEqual(actual, claimed) {
		t.Fatalf("legacy snapshot changed the new run: %#v %v", actual, err)
	}
}
