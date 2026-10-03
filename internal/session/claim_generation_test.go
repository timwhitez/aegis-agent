package session

import (
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestClaimSessionRunWithGenerationKeepsPreparedStamp(t *testing.T) {
	store, id := stateCASFixture(t)
	if err := store.AppendSteerRequest(id, NewSteerRequest("retain this queued request", false)); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	claimed, err := store.ClaimSessionRunWithGeneration(id, stamp, StatusAwaitingInput)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.UpdatedAt != stamp || claimed.Status != StatusRunning || claimed.Phase != "prepare" || claimed.PendingSteerCount != 1 {
		t.Fatalf("prepared generation was not retained: %#v", claimed)
	}
	if _, err := NewStore(store.Root()).ClaimSessionRunWithGeneration(id, time.Now().UTC().Format(time.RFC3339Nano), StatusAwaitingInput); err == nil {
		t.Fatal("another store claimed the running generation")
	}
}

func TestClaimSessionRunWithGenerationRejectsInvalidOrReusedStamp(t *testing.T) {
	for _, invalid := range []string{"", "invalid", "2026-10-03T11:00:00+01:00", "current"} {
		t.Run(invalid, func(t *testing.T) {
			store, id := stateCASFixture(t)
			original, err := store.LoadState(id)
			if err != nil {
				t.Fatal(err)
			}
			stamp := invalid
			if invalid == "current" {
				stamp = original.UpdatedAt
			}
			if _, err := store.ClaimSessionRunWithGeneration(id, stamp, StatusAwaitingInput); err == nil {
				t.Fatal("invalid or reused generation accepted")
			}
			actual, err := store.LoadState(id)
			if err != nil || !reflect.DeepEqual(actual, original) {
				t.Fatalf("invalid claim changed state: %#v %v", actual, err)
			}
		})
	}
}

func TestClaimSessionRunWithGenerationConcurrentStoresPermitOneClaim(t *testing.T) {
	store, id := stateCASFixture(t)
	var winners atomic.Int32
	var group sync.WaitGroup
	for range 2 {
		group.Go(func() {
			if _, err := NewStore(store.Root()).ClaimSessionRunWithGeneration(id, time.Now().UTC().Format(time.RFC3339Nano), StatusAwaitingInput); err == nil {
				winners.Add(1)
			}
		})
	}
	group.Wait()
	if winners.Load() != 1 {
		t.Fatalf("expected one prepared claim, got %d", winners.Load())
	}
}
