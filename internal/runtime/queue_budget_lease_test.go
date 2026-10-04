package runtime

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aegis-agent/internal/session"
	"aegis-agent/internal/tools"
)

func pausedQueueBudgetFixture(t *testing.T) (*Runner, string, session.QueueJob, *atomic.Int64) {
	t.Helper()
	var mode atomic.Int64
	server := newBudgetLifecycleResponsesServer(t, &mode)
	cfg := testRuntimeConfig(t)
	provider := cfg.Providers["openai-compatible"]
	provider.BaseURL, provider.APIKeyEnv = server.URL, "QUEUE_LEASE_TEST_KEY"
	t.Setenv("QUEUE_LEASE_TEST_KEY", "offline-dummy")
	cfg.Providers["openai-compatible"] = provider
	cfg.Runtime.MaxTurnsHard = -1
	cfg.Runtime.ChildBudget.MaxTurnsPerAttempt = 1
	cfg.Runtime.Queue.PollIntervalMS = 50
	runner := NewRunner(cfg)
	parent := createParentSession(t, runner.store, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := runner.QueueSubmit(ctx, QueueSubmitRequest{ParentSessionID: parent, Prompt: "bounded queue child", IsolationMode: "off"}); err != nil {
		t.Fatal(err)
	}
	paused, ok, err := runner.ProcessNextJob(ctx)
	if err != nil || !ok || paused.Status != session.QueueStatusBlocked || paused.SessionStatus != session.StatusPaused {
		t.Fatalf("budget pause: job=%#v ok=%t err=%v", paused, ok, err)
	}
	return runner, parent, paused, &mode
}

func TestPersistEffectiveBudgetPreservesQueueResumeClaim(t *testing.T) {
	runner, parent, job, _ := pausedQueueBudgetFixture(t)
	if _, acquired, err := runner.store.AcquireQueueChildResumeSlot(parent, job.ID, job.SessionID, 4); err != nil || !acquired {
		t.Fatalf("resume claim: acquired=%t err=%v", acquired, err)
	}
	before, err := runner.store.LoadJobCoordinationSnapshot(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := runner.store.LoadMetadata(job.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	next, err := session.ExtendEffectiveBudget(meta.EffectiveBudget, session.BudgetExtension{AddTurns: 1}, 1, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := persistEffectiveBudget(runner.store, meta, next); err != nil {
		t.Fatal(err)
	}
	after, err := runner.store.LoadJobCoordinationSnapshot(job.ID)
	if err != nil || after.Status != session.QueueStatusRunning || after.ProcessStartID != before.ProcessStartID || after.ClaimedBy != before.ClaimedBy || after.HeartbeatAt != before.HeartbeatAt {
		t.Fatalf("budget-only persistence changed current resume claim: before=%#v after=%#v err=%v", before, after, err)
	}
	if after.EffectiveBudget == nil || after.EffectiveBudget.Attempt != 2 {
		t.Fatalf("budget attempt was not mirrored: %#v", after.EffectiveBudget)
	}
	state, err := runner.store.LoadState(job.SessionID)
	if err != nil || state.Status != session.StatusPaused {
		t.Fatalf("preparation must not resume child state: state=%#v err=%v", state, err)
	}
}

func TestPromptAgentPreservesForeignQueueClaimAfterLeaseLoss(t *testing.T) {
	runner, parent, job, mode := pausedQueueBudgetFixture(t)
	// A previous successful extension may be retried without another extension.
	// Prepare it while still blocked so this ownership test does not depend on
	// the separate budget-mirroring bug stopping the heartbeat before Continue.
	state, err := runner.store.LoadState(job.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.extendChildBudget(parent, job.SessionID, job.ID, state, session.BudgetExtension{AddTurns: 1}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runner.SetRunLifecycleHooks(RunLifecycleHooks{OnSessionActive: func(meta session.SessionMetadata, _ *Runner) error {
		if meta.ID != job.SessionID {
			return nil
		}
		current, err := runner.store.LoadJobCoordinationSnapshot(job.ID)
		if err != nil {
			return err
		}
		current.Status, current.SessionStatus = session.QueueStatusRunning, session.StatusRunning
		current.ProcessStartID, current.ClaimedBy = "foreign-process", "foreign-owner"
		current.FinalText = "new owner fact must survive"
		if err := runner.store.SaveJob(current); err != nil {
			return err
		}
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			events, err := runner.store.LoadEvents(parent)
			if err != nil {
				return err
			}
			for _, event := range events {
				if event.Type == "queue.job.lease_lost" {
					return nil
				}
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
		}
	}})
	mode.Store(1)
	_, err = runner.PromptAgent(ctx, tools.AgentPromptRequest{ParentSessionID: parent, QueueJobID: job.ID, Message: "resume"})
	if !errors.Is(err, session.ErrQueueJobLeaseLost) || !strings.Contains(err.Error(), "lost its durable lease") {
		t.Fatalf("must reject the old worker after real ownership loss: %v", err)
	}
	current, loadErr := runner.store.LoadJobCoordinationSnapshot(job.ID)
	if loadErr != nil || current.Status != session.QueueStatusRunning || current.ProcessStartID != "foreign-process" || current.ClaimedBy != "foreign-owner" || current.FinalText != "new owner fact must survive" {
		t.Fatalf("old PromptAgent overwrote another owner: job=%#v err=%v", current, loadErr)
	}
}

func TestQueueResumeHeartbeatContinuesAfterBudgetMirror(t *testing.T) {
	runner, parent, job, _ := pausedQueueBudgetFixture(t)
	if _, acquired, err := runner.store.AcquireQueueChildResumeSlot(parent, job.ID, job.SessionID, 4); err != nil || !acquired {
		t.Fatalf("resume claim: acquired=%t err=%v", acquired, err)
	}
	meta, err := runner.store.LoadMetadata(job.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	next, err := session.ExtendEffectiveBudget(meta.EffectiveBudget, session.BudgetExtension{AddTurns: 1}, 1, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := persistEffectiveBudget(runner.store, meta, next); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runCtx, stop := runner.startQueueJobHeartbeat(ctx, job.ID, parent)
	defer stop()
	first := ""
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := runner.store.LoadJobCoordinationSnapshot(job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if first == "" {
			first = current.HeartbeatAt
		} else if current.HeartbeatAt != first {
			break
		}
		select {
		case <-runCtx.Done():
			t.Fatalf("renewal stopped: %v", runCtx.Err())
		case <-ticker.C:
		}
	}
	if err := stop(); err != nil {
		t.Fatalf("owned resumed lease cancelled: %v", err)
	}
	parentEvents, err := runner.store.LoadEvents(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range parentEvents {
		if event.Type == "queue.job.lease_lost" {
			t.Fatalf("false lease loss: %#v", event)
		}
	}
}

func TestPromptedChildRollbackPreservesLostQueueClaim(t *testing.T) {
	runner, _, job, _ := pausedQueueBudgetFixture(t)
	previous := job
	job.Status, job.ProcessStartID, job.FinalText = session.QueueStatusRunning, "foreign-process", "foreign result"
	if err := runner.store.SaveJob(job); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("budget persistence failed")
	for _, lossFromHeartbeat := range []bool{false, true} {
		reported := cause
		stop := func() error { return session.ErrQueueJobLeaseLost }
		if !lossFromHeartbeat {
			reported = errors.Join(cause, session.ErrQueueJobLeaseLost)
			stop = func() error { return nil }
		}
		err := runner.rollbackPromptedChildRunSlot(reported, stop, job.SessionID, previous, false)
		if !errors.Is(err, cause) || !errors.Is(err, session.ErrQueueJobLeaseLost) {
			t.Fatalf("lost errors: %v", err)
		}
		current, err := runner.store.LoadJobCoordinationSnapshot(job.ID)
		if err != nil || current.Status != session.QueueStatusRunning || current.ProcessStartID != "foreign-process" || current.FinalText != "foreign result" {
			t.Fatalf("failed preparation restored stale claim: %#v err=%v", current, err)
		}
	}
}

func TestPromptedChildFinalWritesRejectUnobservedTakeover(t *testing.T) {
	runner, parent, previous, _ := pausedQueueBudgetFixture(t)
	foreign := previous
	foreign.Status, foreign.ProcessStartID, foreign.FinalText = session.QueueStatusRunning, "foreign-process", "new owner result"
	if err := runner.store.SaveJob(foreign); err != nil {
		t.Fatal(err)
	}
	// The last heartbeat tick preceded takeover; stop reports no observation.
	if err := runner.rollbackPromptedChildRunSlot(errors.New("prepare failed"), func() error { return nil }, previous.SessionID, previous, false); !errors.Is(err, session.ErrQueueJobLeaseLost) {
		t.Fatalf("unobserved takeover rollback: %v", err)
	}
	if err := runner.reconcilePromptedChildJob(parent, previous, RunResult{SessionID: previous.SessionID, Status: session.StatusCompleted, FinalText: "old worker result"}); !errors.Is(err, session.ErrQueueJobLeaseLost) {
		t.Fatalf("unobserved takeover settlement: %v", err)
	}
	current, err := runner.store.LoadJobCoordinationSnapshot(previous.ID)
	if err != nil || current.Status != session.QueueStatusRunning || current.ProcessStartID != "foreign-process" || current.FinalText != "new owner result" {
		t.Fatalf("new owner overwritten: %#v err=%v", current, err)
	}
}
