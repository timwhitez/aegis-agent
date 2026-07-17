package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func TestValidateEffectiveBudgetEnforcesActiveRuntimeLeaseConsistency(t *testing.T) {
	now := time.Now().UTC()
	valid := NewEffectiveBudget(BudgetSourceRuntimeChild, 0, 5, 0, 0, now)
	valid.ActiveRuntimeCheckpointIntervalMS = 1000
	valid.ActiveRuntimeCheckpointAt = now.Format(time.RFC3339Nano)
	valid.ActiveRuntimeLeaseOpen = true
	valid.ActiveRuntimeLeaseOwner = "owner"
	valid.ActiveRuntimeLastRecoveryMS = 1000
	valid.ActiveRuntimeLastRecoveryAt = now.Format(time.RFC3339Nano)
	if err := ValidateEffectiveBudget(valid); err != nil {
		t.Fatalf("validate consistent active-runtime lease: %v", err)
	}

	closedWithOwner := CloneEffectiveBudget(valid)
	closedWithOwner.ActiveRuntimeLeaseOpen = false
	if err := ValidateEffectiveBudget(closedWithOwner); err == nil || !strings.Contains(err.Error(), "cannot retain an owner") {
		t.Fatalf("expected closed lease owner rejection, got %v", err)
	}
	missingRecoveryTime := CloneEffectiveBudget(valid)
	missingRecoveryTime.ActiveRuntimeLastRecoveryAt = ""
	if err := ValidateEffectiveBudget(missingRecoveryTime); err == nil || !strings.Contains(err.Error(), "recorded together") {
		t.Fatalf("expected recovery telemetry consistency rejection, got %v", err)
	}
	openWithoutLimit := CloneEffectiveBudget(valid)
	openWithoutLimit.MaxActiveRuntimeMS = 0
	if err := ValidateEffectiveBudget(openWithoutLimit); err == nil || !strings.Contains(err.Error(), "active-runtime limit") {
		t.Fatalf("expected open lease without active-runtime dimension rejection, got %v", err)
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

func TestConcurrentQueueResumeSlotsRespectActiveChildCap(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)
	stores := []*Store{store, NewStore(root)}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for i := 0; i < 2; i++ {
		childID := fmt.Sprintf("child_resume_cap_%d", i)
		job := QueueJob{
			SchemaVersion:   1,
			ID:              fmt.Sprintf("job_resume_cap_%d", i),
			CreatedAt:       now,
			UpdatedAt:       now,
			Status:          QueueStatusBlocked,
			ParentSessionID: "parent_resume_cap",
			RootSessionID:   "root_resume_cap",
			Prompt:          "resume queued work",
			Mode:            ModeExec,
			Background:      true,
			SessionID:       childID,
			SessionStatus:   StatusPaused,
			LastError:       "child session is resumable: paused",
		}
		if err := store.SaveJob(job); err != nil {
			t.Fatalf("save blocked job %d: %v", i, err)
		}
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	acquiredIDs := map[string]struct{}{}
	for i := 0; i < 2; i++ {
		i := i
		wg.Add(1)
		go func(store *Store) {
			defer wg.Done()
			jobID := fmt.Sprintf("job_resume_cap_%d", i)
			childID := fmt.Sprintf("child_resume_cap_%d", i)
			_, acquired, err := store.AcquireQueueChildResumeSlot("parent_resume_cap", jobID, childID, 1)
			if err != nil {
				t.Errorf("acquire resume slot for %s: %v", jobID, err)
				return
			}
			if acquired {
				mu.Lock()
				acquiredIDs[jobID] = struct{}{}
				mu.Unlock()
			}
		}(stores[i])
	}
	wg.Wait()
	if len(acquiredIDs) != 1 {
		t.Fatalf("expected exactly one queue resume slot at cap=1, got %#v", acquiredIDs)
	}
	for i := 0; i < 2; i++ {
		jobID := fmt.Sprintf("job_resume_cap_%d", i)
		job, err := store.LoadJob(jobID)
		if err != nil {
			t.Fatalf("load queue resume candidate %s: %v", jobID, err)
		}
		_, acquired := acquiredIDs[jobID]
		if acquired && job.Status != QueueStatusRunning {
			t.Fatalf("acquired resume job must be running: %#v", job)
		}
		if !acquired && job.Status != QueueStatusBlocked {
			t.Fatalf("capacity-rejected resume job must remain blocked: %#v", job)
		}
	}
}

func TestConcurrentDirectResumeReservationRejectsDuplicateSession(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	stores := []*Store{NewStore(root), NewStore(root)}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	child := SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_direct_resume",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyAutonomous,
		ParentSessionID:  "parent_direct_resume",
		RootSessionID:    "root_direct_resume",
		Depth:            1,
	}
	if err := stores[0].Create(child, State{Status: StatusPaused, Phase: "interrupt", PauseReason: "manual_stop", UpdatedAt: now}); err != nil {
		t.Fatalf("create paused direct child: %v", err)
	}
	type outcome struct {
		acquired bool
		err      error
	}
	results := make(chan outcome, len(stores))
	var wg sync.WaitGroup
	for _, store := range stores {
		store := store
		wg.Add(1)
		go func() {
			defer wg.Done()
			acquired, err := store.AcquireDirectChildSlot("parent_direct_resume", "root_direct_resume", "child_direct_resume", 4)
			results <- outcome{acquired: acquired, err: err}
		}()
	}
	wg.Wait()
	close(results)
	acquiredCount := 0
	duplicateRejected := false
	for result := range results {
		if result.acquired {
			acquiredCount++
		}
		if result.err != nil && strings.Contains(result.err.Error(), "already has an active reservation") {
			duplicateRejected = true
		}
	}
	if acquiredCount != 1 || !duplicateRejected {
		t.Fatalf("expected one direct resume owner and one duplicate rejection: acquired=%d duplicate_rejected=%t", acquiredCount, duplicateRejected)
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

func TestDeadDirectReservationReclaimsCapacityAndPausesZombieSession(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	child := SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_dead_direct_owner",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyAutonomous,
		ParentSessionID:  "parent_dead_direct_owner",
		RootSessionID:    "root_dead_direct_owner",
		Depth:            1,
	}
	if err := store.Create(child, State{Status: StatusRunning, Phase: "provider_call", UpdatedAt: now}); err != nil {
		t.Fatalf("create running child: %v", err)
	}
	acquired, err := store.AcquireDirectChildSlot(child.ParentSessionID, child.RootSessionID, child.ID, 1)
	if err != nil || !acquired {
		t.Fatalf("acquire direct reservation: acquired=%t err=%v", acquired, err)
	}
	reservationPath := filepath.Join(store.directChildReservationDir(), child.ID+".json")
	var reservation ChildRunReservation
	if err := readJSONFile(reservationPath, &reservation); err != nil {
		t.Fatalf("load direct reservation: %v", err)
	}
	reservation.WorkerPID = 999999
	reservation.ProcessStartID = "999999:dead-owner"
	reservation.ProcessIdentity = ""
	if err := store.writeJSONFile(reservationPath, reservation); err != nil {
		t.Fatalf("write dead reservation owner: %v", err)
	}
	job := QueueJob{
		SchemaVersion:   1,
		ID:              "job_after_dead_direct_owner",
		Status:          QueueStatusQueued,
		ParentSessionID: child.ParentSessionID,
		RootSessionID:   child.RootSessionID,
		Prompt:          "claim after dead direct owner",
		Mode:            ModeExec,
		Background:      true,
	}
	if err := store.EnqueueJob(job); err != nil {
		t.Fatalf("enqueue replacement work: %v", err)
	}
	claimed, ok, err := store.ClaimNextQueuedJobWithLimit(1)
	if err != nil || !ok || claimed.ID != job.ID {
		t.Fatalf("dead direct owner must release capacity: ok=%t err=%v job=%#v", ok, err, claimed)
	}
	state, err := store.LoadState(child.ID)
	if err != nil || state.Status != StatusPaused || state.PauseReason != "stale_owner_reconciled" {
		t.Fatalf("dead direct owner must pause zombie running child: state=%#v err=%v", state, err)
	}
	eventsList, err := store.LoadEvents(child.ID)
	if err != nil {
		t.Fatalf("load reclaimed child events: %v", err)
	}
	found := false
	for _, event := range eventsList {
		if event.Type == "session.paused" && event.Data["reconciled_from"] == "direct_child_reservation" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing durable direct-reservation reclaim event: %#v", eventsList)
	}
	if _, err := os.Stat(reservationPath); !os.IsNotExist(err) {
		t.Fatalf("dead direct reservation was not removed: %v", err)
	}
}

func TestDirectReservationRejectsPIDReuseByProcessIdentity(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	child := SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_pid_reuse_direct_owner",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyAutonomous,
		ParentSessionID:  "parent_pid_reuse_direct_owner",
		RootSessionID:    "root_pid_reuse_direct_owner",
		Depth:            1,
	}
	if err := store.Create(child, State{Status: StatusRunning, Phase: "provider_call", UpdatedAt: now}); err != nil {
		t.Fatalf("create running child: %v", err)
	}
	acquired, err := store.AcquireDirectChildSlot(child.ParentSessionID, child.RootSessionID, child.ID, 1)
	if err != nil || !acquired {
		t.Fatalf("acquire direct reservation: acquired=%t err=%v", acquired, err)
	}
	reservationPath := filepath.Join(store.directChildReservationDir(), child.ID+".json")
	var reservation ChildRunReservation
	if err := readJSONFile(reservationPath, &reservation); err != nil {
		t.Fatalf("load direct reservation: %v", err)
	}
	reservation.ProcessIdentity = "linux:test-boot:old-start-ticks"
	if err := store.writeJSONFile(reservationPath, reservation); err != nil {
		t.Fatalf("write stale process identity: %v", err)
	}
	originalIdentity := hostProcessIdentity
	hostProcessIdentity = func(pid int) (string, bool) {
		if pid == os.Getpid() {
			return "linux:test-boot:new-start-ticks", true
		}
		return "", false
	}
	defer func() { hostProcessIdentity = originalIdentity }()
	job := QueueJob{
		SchemaVersion:   1,
		ID:              "job_after_pid_reuse_direct_owner",
		Status:          QueueStatusQueued,
		ParentSessionID: child.ParentSessionID,
		RootSessionID:   child.RootSessionID,
		Prompt:          "claim after pid reuse",
		Mode:            ModeExec,
		Background:      true,
	}
	if err := store.EnqueueJob(job); err != nil {
		t.Fatalf("enqueue replacement work: %v", err)
	}
	claimed, ok, err := store.ClaimNextQueuedJobWithLimit(1)
	if err != nil || !ok || claimed.ID != job.ID {
		t.Fatalf("process identity mismatch must release capacity: ok=%t err=%v job=%#v", ok, err, claimed)
	}
	state, err := store.LoadState(child.ID)
	if err != nil || state.Status != StatusPaused || state.PauseReason != "stale_owner_reconciled" {
		t.Fatalf("pid reuse must pause zombie running child: state=%#v err=%v", state, err)
	}
}

func TestDirectReservationReclaimRollsBackStateWhenDiagnosticEventFails(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	child := SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_reclaim_event_failure",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyAutonomous,
		ParentSessionID:  "parent_reclaim_event_failure",
		RootSessionID:    "root_reclaim_event_failure",
		Depth:            1,
	}
	if err := store.Create(child, State{Status: StatusRunning, Phase: "provider_call", UpdatedAt: now}); err != nil {
		t.Fatalf("create running child: %v", err)
	}
	acquired, err := store.AcquireDirectChildSlot(child.ParentSessionID, child.RootSessionID, child.ID, 1)
	if err != nil || !acquired {
		t.Fatalf("acquire direct reservation: acquired=%t err=%v", acquired, err)
	}
	reservationPath := filepath.Join(store.directChildReservationDir(), child.ID+".json")
	var reservation ChildRunReservation
	if err := readJSONFile(reservationPath, &reservation); err != nil {
		t.Fatalf("load direct reservation: %v", err)
	}
	reservation.WorkerPID = 999999
	reservation.ProcessStartID = "999999:dead-owner"
	reservation.ProcessIdentity = ""
	if err := store.writeJSONFile(reservationPath, reservation); err != nil {
		t.Fatalf("write dead reservation owner: %v", err)
	}
	eventsPath := filepath.Join(store.SessionDir(child.ID), "events.jsonl")
	if err := os.Remove(eventsPath); err != nil {
		t.Fatalf("remove events file: %v", err)
	}
	if err := os.Mkdir(eventsPath, 0o700); err != nil {
		t.Fatalf("block events path: %v", err)
	}
	job := QueueJob{
		SchemaVersion:   1,
		ID:              "job_reclaim_event_failure",
		Status:          QueueStatusQueued,
		ParentSessionID: child.ParentSessionID,
		RootSessionID:   child.RootSessionID,
		Prompt:          "must remain queued",
		Mode:            ModeExec,
		Background:      true,
	}
	if err := store.EnqueueJob(job); err != nil {
		t.Fatalf("enqueue replacement work: %v", err)
	}
	if claimed, ok, err := store.ClaimNextQueuedJobWithLimit(1); err == nil || ok {
		t.Fatalf("expected reclaim diagnostic failure, ok=%t err=%v job=%#v", ok, err, claimed)
	}
	state, err := store.LoadState(child.ID)
	if err != nil || state.Status != StatusRunning || state.PauseReason != "" {
		t.Fatalf("failed reclaim diagnostic must restore running state: state=%#v err=%v", state, err)
	}
	if _, err := os.Stat(reservationPath); err != nil {
		t.Fatalf("failed reclaim diagnostic must retain reservation: %v", err)
	}
	queued, err := store.LoadJob(job.ID)
	if err != nil || queued.Status != QueueStatusQueued {
		t.Fatalf("failed reclaim diagnostic must leave replacement queued: job=%#v err=%v", queued, err)
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
