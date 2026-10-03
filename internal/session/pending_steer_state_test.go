package session

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRefreshPendingSteerCountPreservesCurrentStateAfterLockWait(t *testing.T) {
	for _, transition := range []string{"review_required", "failed", "advanced_phase", "replacement_generation", "no_transition"} {
		t.Run(transition, func(t *testing.T) {
			store, id := stateCASFixture(t)
			claimed, err := store.ClaimSessionRun(id, StatusAwaitingInput)
			if err != nil {
				t.Fatal(err)
			}
			peer := NewStore(store.Root())
			if err := peer.AppendSteerRequest(id, NewSteerRequest("accepted queued instruction", false)); err != nil {
				t.Fatal(err)
			}
			blocked, release := make(chan struct{}), make(chan struct{})
			var intercepted atomic.Bool
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			beforeOpenNoSymlink = func(path string, flags int) error {
				if path == filepath.Join(store.SessionDir(id), "state.lock") && intercepted.CompareAndSwap(false, true) {
					close(blocked)
					<-release
				}
				return nil
			}
			finished := make(chan struct{})
			var refreshed State
			var refreshErr error
			go func() {
				defer close(finished)
				refreshed, refreshErr = store.RefreshPendingSteerCount(id)
			}()
			t.Cleanup(func() {
				unblock()
				<-finished
				beforeOpenNoSymlink = nil
			})
			select {
			case <-blocked:
			case <-time.After(3 * time.Second):
				t.Fatal("refresh did not reach the state.lock barrier")
			}
			expected := claimed
			if transition == "replacement_generation" {
				expected, err = peer.ClaimSessionRun(id, StatusRunning)
				if err != nil {
					t.Fatal(err)
				}
			} else if transition != "no_transition" {
				next := claimed
				switch transition {
				case "review_required":
					next.Status = StatusAwaitingInput
					next.Phase = "plan_approval"
					next.IdleReason = "approval_content_changed"
					next.LastError = "review the changed plan"
				case "failed":
					next.Status = StatusFailed
					next.Phase = "settled"
					next.LastError = "provider deliberately stopped"
				case "advanced_phase":
					next.Phase = "provider"
					next.Turn = 1
				}
				var saved bool
				expected, saved, err = peer.SwapStateIfCurrent(id, claimed, next)
				if err != nil || !saved {
					t.Fatalf("legal semantic CAS failed: saved=%v err=%v", saved, err)
				}
			}
			// Another append while refresh waits must also be counted from the
			// durable queue, rather than the earlier count-read snapshot.
			if err := peer.AppendSteerRequest(id, NewSteerRequest("second accepted instruction", false)); err != nil {
				t.Fatal(err)
			}
			unblock()
			<-finished
			if refreshErr != nil {
				t.Fatalf("refresh current count: %v", refreshErr)
			}
			actual, err := peer.LoadState(id)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(refreshed, actual) || actual.PendingSteerCount != 2 {
				t.Fatalf("refresh did not return its committed current count: returned=%#v actual=%#v", refreshed, actual)
			}
			observed := actual
			observed.UpdatedAt = expected.UpdatedAt
			observed.PendingSteerCount = expected.PendingSteerCount
			if !reflect.DeepEqual(observed, expected) {
				t.Fatalf("count refresh overwrote a durable semantic transition: expected=%#v actual=%#v", expected, actual)
			}
			if transition == "replacement_generation" {
				if err := peer.SaveState(id, claimed); err == nil {
					t.Fatal("generic SaveState accepted a stale run generation")
				}
			}
		})
	}
}

func TestRefreshPendingSteerCountEmptyAndInvalidFacts(t *testing.T) {
	store, id := stateCASFixture(t)
	state, err := store.RefreshPendingSteerCount(id)
	if err != nil || state.PendingSteerCount != 0 || state.Status != StatusAwaitingInput {
		t.Fatalf("empty queue refresh: %#v %v", state, err)
	}
	if _, err := store.RefreshPendingSteerCount("unknown_session"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unknown session count refresh: %v", err)
	}
	if _, err := os.Stat(store.SessionDir("unknown_session")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("count refresh created an unknown session: %v", err)
	}
	statePath := filepath.Join(store.SessionDir(id), "state.json")
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.SessionDir(id), "control", "steer.jsonl"), []byte("{broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RefreshPendingSteerCount(id); err == nil {
		t.Fatal("corrupt queue was accepted")
	}
	after, err := os.ReadFile(statePath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("corrupt queue count refresh changed state: %v", err)
	}
	if err := os.WriteFile(statePath, []byte("{broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RefreshPendingSteerCount(id); err == nil {
		t.Fatal("corrupt current state was replaced by a count refresh")
	}
	after, err = os.ReadFile(statePath)
	if err != nil || string(after) != "{broken\n" {
		t.Fatalf("corrupt state was overwritten: %v", err)
	}
}
