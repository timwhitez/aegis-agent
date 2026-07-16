package session

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestEffectiveBudgetAttemptsAccumulateUsage(t *testing.T) {
	started := time.Date(2026, 7, 15, 1, 2, 3, 0, time.UTC)
	budget := NewEffectiveBudget(BudgetSourceRuntimeChild, 2, 10, 0, 0, started)
	RefreshEffectiveBudget(budget, 2)
	AddEffectiveBudgetRuntime(budget, 3*time.Second, 2)
	budget.Status = BudgetStatusExhausted
	budget.LastReason = ChildBudgetTurnsExceededReason

	if budget.UsedTurns != 2 || budget.TotalUsedTurns != 2 || budget.RemainingTurns == nil || *budget.RemainingTurns != 0 {
		t.Fatalf("unexpected exhausted turn accounting: %#v", budget)
	}
	if budget.UsedActiveRuntimeMS != 3000 || budget.TotalActiveRuntimeMS != 3000 {
		t.Fatalf("unexpected exhausted runtime accounting: %#v", budget)
	}

	next, err := ExtendEffectiveBudget(budget, BudgetExtension{
		AddTurns:            3,
		AddActiveRuntimeSec: 5,
		Reason:              "continue bounded work",
	}, 2, started.Add(time.Hour))
	if err != nil {
		t.Fatalf("extend effective budget: %v", err)
	}
	if next.Attempt != 2 || next.AttemptStartTurn != 2 || next.UsedTurns != 0 || next.UsedActiveRuntimeMS != 0 {
		t.Fatalf("expected a fresh second attempt, got %#v", next)
	}
	if next.TotalUsedTurns != 2 || next.TotalActiveRuntimeMS != 3000 {
		t.Fatalf("expected lifetime usage to survive extension, got %#v", next)
	}
	if next.MaxTurnsPerAttempt != 3 || next.RemainingTurns == nil || *next.RemainingTurns != 3 {
		t.Fatalf("unexpected next-attempt turn limit: %#v", next)
	}
	if next.MaxActiveRuntimeMS != 12_000 || next.RemainingActiveRuntime == nil || *next.RemainingActiveRuntime != 12_000 {
		t.Fatalf("unexpected next-attempt active runtime limit: %#v", next)
	}

	RefreshEffectiveBudget(next, 4)
	AddEffectiveBudgetRuntime(next, 2*time.Second, 4)
	if next.UsedTurns != 2 || next.TotalUsedTurns != 4 || next.RemainingTurns == nil || *next.RemainingTurns != 1 {
		t.Fatalf("unexpected second-attempt turn usage: %#v", next)
	}
	if next.UsedActiveRuntimeMS != 2000 || next.TotalActiveRuntimeMS != 5000 || next.RemainingActiveRuntime == nil || *next.RemainingActiveRuntime != 10_000 {
		t.Fatalf("unexpected second-attempt runtime usage: %#v", next)
	}
}

func TestEffectiveBudgetExtensionMustClearEveryExhaustedDimension(t *testing.T) {
	now := time.Now().UTC()
	budget := NewEffectiveBudget(BudgetSourceRuntimeChild, 1, 10, 1, 0, now.Add(-2*time.Second))
	RefreshEffectiveBudget(budget, 1)
	budget.Status = BudgetStatusExhausted
	budget.LastReason = ChildBudgetTurnsExceededReason

	if _, err := ExtendEffectiveBudget(budget, BudgetExtension{AddActiveRuntimeSec: 1}, 1, now); err == nil {
		t.Fatal("expected extension that leaves exhausted turns and deadline unchanged to fail")
	}
	if _, err := ExtendEffectiveBudget(budget, BudgetExtension{AddTurns: 1, ClearAbsoluteDeadline: true}, 1, now); err != nil {
		t.Fatalf("expected extension that repairs both exhausted dimensions to succeed: %v", err)
	}
	for _, extension := range []BudgetExtension{
		{AddTurns: 1, ClearTurnLimit: true},
		{AddActiveRuntimeSec: 1, ClearActiveRuntime: true},
		{ExtendDeadlineSec: 1, ClearAbsoluteDeadline: true},
	} {
		if _, err := ExtendEffectiveBudget(budget, extension, 1, now); err == nil {
			t.Fatalf("expected conflicting add/clear extension to fail: %#v", extension)
		}
	}
}

func TestDirectChildReservationSharesCapacityWithQueueClaims(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	parentID := "parent_slot_shared"
	rootID := "root_slot_shared"
	childID := "child_slot_shared"
	acquired, err := store.AcquireDirectChildSlot(parentID, rootID, childID, 1)
	if err != nil || !acquired {
		t.Fatalf("reserve direct child slot: acquired=%t err=%v", acquired, err)
	}
	job := QueueJob{
		SchemaVersion:   1,
		ID:              "job_slot_shared",
		Status:          QueueStatusQueued,
		ParentSessionID: parentID,
		RootSessionID:   rootID,
		Prompt:          "queued work",
		Mode:            ModeExec,
		Background:      true,
	}
	if err := store.EnqueueJob(job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if claimed, ok, err := store.ClaimNextQueuedJobWithLimit(1); err != nil || ok {
		t.Fatalf("queue claim must respect direct reservation, ok=%t err=%v job=%#v", ok, err, claimed)
	}
	if err := store.ReleaseDirectChildSlot(childID); err != nil {
		t.Fatalf("release direct child slot: %v", err)
	}
	claimed, ok, err := store.ClaimNextQueuedJobWithLimit(1)
	if err != nil || !ok || claimed.ID != job.ID {
		t.Fatalf("expected queue claim after release, ok=%t err=%v job=%#v", ok, err, claimed)
	}
}

func TestConcurrentQueueClaimsRespectActiveChildCap(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	for i := 0; i < 8; i++ {
		job := QueueJob{
			SchemaVersion:   1,
			ID:              fmt.Sprintf("job_cap_%02d", i),
			Status:          QueueStatusQueued,
			ParentSessionID: "parent_cap",
			RootSessionID:   "root_cap",
			Prompt:          "queued work",
			Mode:            ModeExec,
			Background:      true,
		}
		if err := store.EnqueueJob(job); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	claimedIDs := map[string]struct{}{}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job, ok, err := store.ClaimNextQueuedJobWithLimit(2)
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			if ok {
				mu.Lock()
				claimedIDs[job.ID] = struct{}{}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(claimedIDs) != 2 {
		t.Fatalf("expected exactly two durable claims at cap=2, got %d: %#v", len(claimedIDs), claimedIDs)
	}
}

func TestStalePreCreateDirectReservationDoesNotLeakCapacity(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	childID := "child_stale_precreate"
	acquired, err := store.AcquireDirectChildSlot("parent_stale_precreate", "root_stale_precreate", childID, 1)
	if err != nil || !acquired {
		t.Fatalf("reserve direct child slot: acquired=%t err=%v", acquired, err)
	}
	reservationPath := filepath.Join(store.directChildReservationDir(), childID+".json")
	var reservation ChildRunReservation
	if err := readJSONFile(reservationPath, &reservation); err != nil {
		t.Fatalf("load direct child reservation: %v", err)
	}
	reservation.CreatedAt = time.Now().UTC().Add(-queueRunningStaleAfter - time.Minute).Format(time.RFC3339Nano)
	if err := store.writeJSONFile(reservationPath, reservation); err != nil {
		t.Fatalf("age direct child reservation: %v", err)
	}
	job := QueueJob{
		SchemaVersion:   1,
		ID:              "job_after_stale_precreate",
		Status:          QueueStatusQueued,
		ParentSessionID: "parent_stale_precreate",
		RootSessionID:   "root_stale_precreate",
		Prompt:          "queued work",
		Mode:            ModeExec,
		Background:      true,
	}
	if err := store.EnqueueJob(job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	claimed, ok, err := store.ClaimNextQueuedJobWithLimit(1)
	if err != nil || !ok || claimed.ID != job.ID {
		t.Fatalf("stale reservation must not consume capacity: ok=%t err=%v job=%#v", ok, err, claimed)
	}
}

func TestSessionGoalProviderTimeJSONAliases(t *testing.T) {
	budget := int64(90)
	goal := SessionGoal{TimeBudgetSeconds: &budget, TimeUsedSeconds: 12}
	data, err := json.Marshal(goal)
	if err != nil {
		t.Fatalf("marshal goal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatalf("decode goal wire: %v", err)
	}
	if wire["provider_time_budget_seconds"] != float64(90) || wire["provider_time_used_seconds"] != float64(12) || wire["time_budget_seconds"] != float64(90) || wire["time_used_seconds"] != float64(12) {
		t.Fatalf("expected canonical and compatibility provider-time fields, got %#v", wire)
	}

	var canonical SessionGoal
	if err := json.Unmarshal([]byte(`{"provider_time_budget_seconds":120,"provider_time_used_seconds":34,"time_budget_seconds":1,"time_used_seconds":2}`), &canonical); err != nil {
		t.Fatalf("unmarshal canonical goal: %v", err)
	}
	if canonical.TimeBudgetSeconds == nil || *canonical.TimeBudgetSeconds != 120 || canonical.TimeUsedSeconds != 34 {
		t.Fatalf("canonical fields must win over legacy aliases, got %#v", canonical)
	}

	var legacy SessionGoal
	if err := json.Unmarshal([]byte(`{"time_budget_seconds":45,"time_used_seconds":6}`), &legacy); err != nil {
		t.Fatalf("unmarshal legacy goal: %v", err)
	}
	if legacy.TimeBudgetSeconds == nil || *legacy.TimeBudgetSeconds != 45 || legacy.TimeUsedSeconds != 6 {
		t.Fatalf("legacy fields must remain readable, got %#v", legacy)
	}
}
