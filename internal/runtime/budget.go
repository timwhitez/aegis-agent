package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"aegis-agent/internal/config"
	"aegis-agent/internal/session"
)

type childBudgetDeadlineCause struct {
	reason string
}

func (c childBudgetDeadlineCause) Error() string {
	return c.reason
}

const childBudgetCheckpointFailureReason = "child_budget_active_runtime_checkpoint_failed"

type childBudgetCheckpointCause struct {
	err error
}

type childBudgetRunContextKey struct{}

func (c childBudgetCheckpointCause) Error() string {
	if c.err == nil {
		return childBudgetCheckpointFailureReason
	}
	return c.err.Error()
}

func (c childBudgetCheckpointCause) Unwrap() error {
	return c.err
}

type childBudgetRun struct {
	mu                 sync.Mutex
	engine             *Engine
	meta               session.SessionMetadata
	activeStartedAt    time.Time
	activeTimer        *time.Timer
	active             bool
	accounted          bool
	absoluteTurn       int
	checkpointInterval time.Duration
	activeLeaseOwner   string
	checkpointStop     chan struct{}
	checkpointDone     chan struct{}
	checkpointStopOnce sync.Once
	checkpointCancel   context.CancelCauseFunc
	checkpointErr      error
}

func newEffectiveChildBudget(cfg *config.Config, source string, startTurn int, createdAt time.Time) *session.EffectiveBudget {
	if cfg == nil {
		return session.NewEffectiveBudget(source, 0, 0, 0, startTurn, createdAt)
	}
	policy := cfg.Runtime.ChildBudget
	maxTurns := policy.MaxTurnsPerAttempt
	if maxTurns <= 0 && policy.MaxTurns > 0 {
		maxTurns = policy.MaxTurns
	}
	maxActiveRuntimeSec := policy.MaxActiveRuntimeSec
	if maxActiveRuntimeSec <= 0 && policy.MaxWallClockSec > 0 {
		maxActiveRuntimeSec = policy.MaxWallClockSec
	}
	budget := session.NewEffectiveBudget(
		source,
		maxTurns,
		maxActiveRuntimeSec,
		policy.MaxElapsedSec,
		startTurn,
		createdAt,
	)
	budget.ActiveRuntimeCheckpointIntervalMS = int64(childBudgetActiveRuntimeCheckpointMS(cfg))
	return budget
}

func childBudgetActiveRuntimeCheckpointMS(cfg *config.Config) int {
	value := config.DefaultChildBudgetActiveRuntimeCheckpointMS
	if cfg != nil && cfg.Runtime.ChildBudget.ActiveRuntimeCheckpointMS > 0 {
		value = cfg.Runtime.ChildBudget.ActiveRuntimeCheckpointMS
	}
	if value < config.MinChildBudgetActiveRuntimeCheckpointMS {
		return config.MinChildBudgetActiveRuntimeCheckpointMS
	}
	if value > config.MaxChildBudgetActiveRuntimeCheckpointMS {
		return config.MaxChildBudgetActiveRuntimeCheckpointMS
	}
	return value
}

func ensureChildEffectiveBudget(cfg *config.Config, meta *session.SessionMetadata, state session.State, source string) bool {
	if meta == nil || strings.TrimSpace(meta.ParentSessionID) == "" || meta.EffectiveBudget != nil {
		return false
	}
	createdAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(meta.CreatedAt))
	if err != nil {
		createdAt = time.Now().UTC()
	}
	startTurn := state.Turn
	if session.IsChildBudgetPauseReason(state.PauseReason) {
		startTurn = 0
	}
	meta.EffectiveBudget = newEffectiveChildBudget(cfg, source, startTurn, createdAt)
	if session.IsChildBudgetPauseReason(state.PauseReason) {
		switch state.PauseReason {
		case session.ChildBudgetTurnsExceededReason:
			session.RefreshEffectiveBudget(meta.EffectiveBudget, state.Turn)
		case session.ChildBudgetActiveRuntimeExceededReason, session.ChildBudgetLegacyWallClockExceededReason:
			if meta.EffectiveBudget.MaxActiveRuntimeMS > 0 {
				meta.EffectiveBudget.UsedActiveRuntimeMS = meta.EffectiveBudget.MaxActiveRuntimeMS
				meta.EffectiveBudget.TotalActiveRuntimeMS = meta.EffectiveBudget.MaxActiveRuntimeMS
			}
			session.RefreshEffectiveBudget(meta.EffectiveBudget, state.Turn)
		}
		meta.EffectiveBudget.Status = session.BudgetStatusExhausted
		meta.EffectiveBudget.LastReason = state.PauseReason
	}
	return true
}

func (e *Engine) beginChildBudgetRun(ctx context.Context, meta session.SessionMetadata, state session.State) (context.Context, context.CancelFunc, *childBudgetRun, error) {
	budget := session.CloneEffectiveBudget(meta.EffectiveBudget)
	if strings.TrimSpace(meta.ParentSessionID) == "" || !session.EffectiveBudgetEnabled(budget) {
		return ctx, func() {}, nil, nil
	}
	now := timeNow().UTC()
	checkpointMS := budget.ActiveRuntimeCheckpointIntervalMS
	if checkpointMS <= 0 {
		checkpointMS = int64(childBudgetActiveRuntimeCheckpointMS(e.cfg))
	}
	previousBudget := session.CloneEffectiveBudget(budget)
	previousCheckpointAt := budget.ActiveRuntimeCheckpointAt
	previousLeaseOwner := budget.ActiveRuntimeLeaseOwner
	recoveryChargeMS := int64(0)
	if budget.MaxActiveRuntimeMS > 0 && budget.ActiveRuntimeLeaseOpen {
		recoveryChargeMS = checkpointMS
		session.AddEffectiveBudgetRuntime(budget, time.Duration(recoveryChargeMS)*time.Millisecond, state.Turn)
		budget.ActiveRuntimeLastRecoveryMS = recoveryChargeMS
		budget.ActiveRuntimeLastRecoveryAt = now.Format(time.RFC3339Nano)
	}
	budget.ActiveRuntimeLeaseOpen = false
	budget.ActiveRuntimeLeaseOwner = ""
	budget.ActiveRuntimeCheckpointIntervalMS = checkpointMS
	activeLeaseOwner := ""
	checkpointInterval := time.Duration(0)
	if budget.MaxActiveRuntimeMS > 0 {
		activeLeaseOwner = fmt.Sprintf("%s:%d", meta.ID, now.UnixNano())
		checkpointInterval = time.Duration(checkpointMS) * time.Millisecond
		budget.ActiveRuntimeCheckpointAt = now.Format(time.RFC3339Nano)
		budget.ActiveRuntimeLeaseOpen = true
		budget.ActiveRuntimeLeaseOwner = activeLeaseOwner
	}
	session.RefreshEffectiveBudget(budget, state.Turn)
	if err := persistEffectiveBudget(e.store, meta, budget); err != nil {
		return ctx, func() {}, nil, fmt.Errorf("start child active-runtime lease: %w", err)
	}
	if recoveryChargeMS > 0 {
		if err := e.appendEvent(meta.ID, "session.child_budget.active_runtime_recovered", "prepare", map[string]any{
			"recovery_charge_ms":          recoveryChargeMS,
			"checkpoint_interval_ms":      checkpointMS,
			"previous_checkpoint_at":      previousCheckpointAt,
			"previous_active_lease_owner": previousLeaseOwner,
		}); err != nil {
			if rollbackErr := persistEffectiveBudget(e.store, meta, previousBudget); rollbackErr != nil {
				return ctx, func() {}, nil, fmt.Errorf("record active-runtime recovery event failed with %v; restore effective budget: %w", err, rollbackErr)
			}
			return ctx, func() {}, nil, fmt.Errorf("record active-runtime recovery event: %w", err)
		}
	}
	run := &childBudgetRun{
		engine:             e,
		meta:               meta,
		activeStartedAt:    now,
		active:             true,
		absoluteTurn:       state.Turn,
		checkpointInterval: checkpointInterval,
		activeLeaseOwner:   activeLeaseOwner,
	}
	session.RefreshEffectiveBudget(budget, state.Turn)
	runCtx := ctx
	deadlineCancel := func() {}
	if absolute, ok := session.EffectiveBudgetDeadline(budget); ok {
		runCtx, deadlineCancel = context.WithDeadlineCause(runCtx, absolute, childBudgetDeadlineCause{reason: session.ChildBudgetAbsoluteDeadlineExceededReason})
	}
	var activeCancel context.CancelCauseFunc
	if budget.RemainingActiveRuntime != nil {
		runCtx, activeCancel = context.WithCancelCause(runCtx)
		remaining := time.Duration(*budget.RemainingActiveRuntime) * time.Millisecond
		run.activeTimer = time.AfterFunc(remaining, func() {
			activeCancel(childBudgetDeadlineCause{reason: session.ChildBudgetActiveRuntimeExceededReason})
		})
	}
	if checkpointInterval > 0 {
		runCtx, run.checkpointCancel = context.WithCancelCause(runCtx)
	}
	runCtx = context.WithValue(runCtx, childBudgetRunContextKey{}, run)
	stop := context.AfterFunc(runCtx, func() {
		var cause childBudgetDeadlineCause
		if errors.As(context.Cause(runCtx), &cause) {
			e.control.requestPauseWithReason(cause.reason)
		}
	})
	run.startCheckpointLoop(runCtx)
	return runCtx, func() {
		stop()
		run.stopCheckpointLoop()
		run.mu.Lock()
		if run.activeTimer != nil {
			run.activeTimer.Stop()
		}
		run.mu.Unlock()
		if activeCancel != nil {
			activeCancel(context.Canceled)
		}
		if run.checkpointCancel != nil {
			run.checkpointCancel(context.Canceled)
		}
		deadlineCancel()
	}, run, nil
}

func (r *childBudgetRun) startCheckpointLoop(ctx context.Context) {
	if r == nil || r.checkpointInterval <= 0 {
		return
	}
	r.checkpointStop = make(chan struct{})
	r.checkpointDone = make(chan struct{})
	go func() {
		defer close(r.checkpointDone)
		ticker := time.NewTicker(r.checkpointInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-r.checkpointStop:
				return
			case <-ticker.C:
				if err := r.checkpointActiveRuntime(); err != nil {
					return
				}
			}
		}
	}()
}

func (r *childBudgetRun) stopCheckpointLoop() {
	if r == nil || r.checkpointStop == nil {
		return
	}
	r.checkpointStopOnce.Do(func() { close(r.checkpointStop) })
	<-r.checkpointDone
}

func (r *childBudgetRun) setAbsoluteTurn(turn int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if turn > r.absoluteTurn {
		r.absoluteTurn = turn
	}
	r.mu.Unlock()
}

func (r *childBudgetRun) checkpointActiveRuntime() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.accounted || !r.active {
		r.mu.Unlock()
		return nil
	}
	_, err := r.persistActiveRuntimeLocked(r.absoluteTurn, "", false)
	if err != nil {
		err = fmt.Errorf("persist child active-runtime checkpoint: %w", err)
		r.checkpointErr = err
	}
	cancel := r.checkpointCancel
	checkpointAt := r.activeStartedAt.UTC().Format(time.RFC3339Nano)
	r.mu.Unlock()
	if err != nil {
		r.engine.control.requestPauseWithReason(childBudgetCheckpointFailureReason)
		if cancel != nil {
			cancel(childBudgetCheckpointCause{err: err})
		}
		_ = r.engine.appendEvent(r.meta.ID, "session.child_budget.active_runtime_checkpoint_failed", "interrupt", map[string]any{
			"reason":                 childBudgetCheckpointFailureReason,
			"error":                  err.Error(),
			"checkpoint_interval_ms": r.checkpointInterval.Milliseconds(),
			"last_checkpoint_at":     checkpointAt,
		})
	}
	return err
}

func childBudgetReasonFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	var cause childBudgetDeadlineCause
	if errors.As(context.Cause(ctx), &cause) {
		return cause.reason
	}
	return ""
}

func childBudgetCheckpointErrorFromContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	var cause childBudgetCheckpointCause
	if errors.As(context.Cause(ctx), &cause) {
		if cause.err != nil {
			return cause.err
		}
		return errors.New(childBudgetCheckpointFailureReason)
	}
	return nil
}

func (r *childBudgetRun) checkpointFailure() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.checkpointErr
}

func isContextCancellation(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		// Adapter layers may normalize a cancelled request into a provider or
		// operation error. Once the run context has ended, its durable cause
		// takes precedence over that wrapper.
		return true
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	return false
}

func (r *childBudgetRun) finish(absoluteTurn int, exhaustedReason string) (*session.EffectiveBudget, error) {
	return r.settle(absoluteTurn, exhaustedReason, true)
}

func (r *childBudgetRun) settleBeforeTerminal(absoluteTurn int, exhaustedReason string) (*session.EffectiveBudget, error) {
	return r.settle(absoluteTurn, exhaustedReason, false)
}

func (r *childBudgetRun) settle(absoluteTurn int, exhaustedReason string, includeCheckpointFailure bool) (*session.EffectiveBudget, error) {
	if r == nil {
		return nil, nil
	}
	r.stopCheckpointLoop()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.accounted {
		meta, err := r.engine.store.LoadMetadata(r.meta.ID)
		if err != nil {
			return nil, err
		}
		if includeCheckpointFailure {
			return session.CloneEffectiveBudget(meta.EffectiveBudget), r.checkpointErr
		}
		return session.CloneEffectiveBudget(meta.EffectiveBudget), nil
	}
	budget, err := r.persistActiveRuntimeLocked(absoluteTurn, exhaustedReason, true)
	if err != nil {
		if includeCheckpointFailure {
			return nil, errors.Join(r.checkpointErr, err)
		}
		return nil, err
	}
	if r.activeTimer != nil {
		r.activeTimer.Stop()
	}
	r.accounted = true
	if includeCheckpointFailure {
		return session.CloneEffectiveBudget(budget), r.checkpointErr
	}
	return session.CloneEffectiveBudget(budget), nil
}

func childBudgetRunFromContext(ctx context.Context) *childBudgetRun {
	if ctx == nil {
		return nil
	}
	run, _ := ctx.Value(childBudgetRunContextKey{}).(*childBudgetRun)
	return run
}

func settleChildBudgetBeforeTerminal(ctx context.Context, absoluteTurn int) error {
	run := childBudgetRunFromContext(ctx)
	if run == nil {
		return nil
	}
	if _, err := run.settleBeforeTerminal(absoluteTurn, ""); err != nil {
		return err
	}
	return run.checkpointFailure()
}

func settleChildBudgetPersistenceBeforeFailure(ctx context.Context, absoluteTurn int) error {
	run := childBudgetRunFromContext(ctx)
	if run == nil {
		return nil
	}
	_, err := run.settleBeforeTerminal(absoluteTurn, "")
	return err
}

func (r *childBudgetRun) pauseActive(absoluteTurn int) (*session.EffectiveBudget, error) {
	if r == nil {
		return nil, nil
	}
	r.stopCheckpointLoop()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.accounted || !r.active {
		meta, err := r.engine.store.LoadMetadata(r.meta.ID)
		if err != nil {
			return nil, err
		}
		return session.CloneEffectiveBudget(meta.EffectiveBudget), nil
	}
	budget, err := r.persistActiveRuntimeLocked(absoluteTurn, "", true)
	if err != nil {
		return nil, errors.Join(r.checkpointErr, err)
	}
	if r.activeTimer != nil {
		r.activeTimer.Stop()
	}
	r.accounted = true
	return session.CloneEffectiveBudget(budget), r.checkpointErr
}

func (r *childBudgetRun) persistActiveRuntimeLocked(absoluteTurn int, exhaustedReason string, closeLease bool) (*session.EffectiveBudget, error) {
	meta, err := r.engine.store.LoadMetadata(r.meta.ID)
	if err != nil {
		return nil, err
	}
	budget := session.CloneEffectiveBudget(meta.EffectiveBudget)
	if budget == nil {
		return nil, errors.New("child effective budget disappeared during active run")
	}
	now := timeNow().UTC()
	if r.active {
		session.AddEffectiveBudgetRuntime(budget, now.Sub(r.activeStartedAt), absoluteTurn)
	}
	if r.checkpointInterval > 0 {
		budget.ActiveRuntimeCheckpointIntervalMS = r.checkpointInterval.Milliseconds()
		budget.ActiveRuntimeCheckpointAt = now.Format(time.RFC3339Nano)
		budget.ActiveRuntimeLeaseOpen = !closeLease
		if closeLease {
			budget.ActiveRuntimeLeaseOwner = ""
		} else {
			budget.ActiveRuntimeLeaseOwner = r.activeLeaseOwner
		}
	}
	if strings.TrimSpace(exhaustedReason) != "" {
		budget.Status = session.BudgetStatusExhausted
		budget.LastReason = exhaustedReason
	}
	if err := persistEffectiveBudget(r.engine.store, meta, budget); err != nil {
		return nil, err
	}
	r.activeStartedAt = now
	if closeLease {
		r.active = false
	}
	return budget, nil
}

func persistEffectiveBudget(store *session.Store, meta session.SessionMetadata, budget *session.EffectiveBudget) error {
	previous := session.CloneEffectiveBudget(meta.EffectiveBudget)
	meta.EffectiveBudget = session.CloneEffectiveBudget(budget)
	if err := store.SaveMetadata(meta.ID, meta); err != nil {
		return err
	}
	if strings.TrimSpace(meta.QueueJobID) == "" {
		return nil
	}
	job, err := store.LoadJob(meta.QueueJobID)
	if err != nil {
		rollback := meta
		rollback.EffectiveBudget = previous
		_ = store.SaveMetadata(meta.ID, rollback)
		return fmt.Errorf("load linked queue job while persisting effective budget: %w", err)
	}
	previousJobBudget := session.CloneEffectiveBudget(job.EffectiveBudget)
	job.EffectiveBudget = session.CloneEffectiveBudget(budget)
	if err := store.SaveJob(job); err != nil {
		rollback := meta
		rollback.EffectiveBudget = previous
		_ = store.SaveMetadata(meta.ID, rollback)
		job.EffectiveBudget = previousJobBudget
		_ = store.SaveJob(job)
		return fmt.Errorf("persist linked queue job effective budget: %w", err)
	}
	return nil
}

func effectiveBudgetEventData(budget *session.EffectiveBudget) map[string]any {
	if budget == nil {
		return map[string]any{}
	}
	data := map[string]any{
		"policy_version":                        budget.PolicyVersion,
		"policy_source":                         budget.Source,
		"turn_scope":                            budget.TurnScope,
		"time_scope":                            budget.TimeScope,
		"max_turns_per_attempt":                 budget.MaxTurnsPerAttempt,
		"max_active_runtime_ms":                 budget.MaxActiveRuntimeMS,
		"absolute_deadline_at":                  budget.AbsoluteDeadlineAt,
		"attempt":                               budget.Attempt,
		"attempt_start_turn":                    budget.AttemptStartTurn,
		"used_turns":                            budget.UsedTurns,
		"used_active_runtime_ms":                budget.UsedActiveRuntimeMS,
		"total_used_turns":                      budget.TotalUsedTurns,
		"total_active_runtime_ms":               budget.TotalActiveRuntimeMS,
		"overrun_turns":                         budget.OverrunTurns,
		"overrun_active_runtime_ms":             budget.OverrunActiveRuntimeMS,
		"active_runtime_checkpoint_interval_ms": budget.ActiveRuntimeCheckpointIntervalMS,
		"active_runtime_checkpoint_at":          budget.ActiveRuntimeCheckpointAt,
		"active_runtime_lease_open":             budget.ActiveRuntimeLeaseOpen,
		"active_runtime_lease_owner":            budget.ActiveRuntimeLeaseOwner,
		"active_runtime_last_recovery_ms":       budget.ActiveRuntimeLastRecoveryMS,
		"active_runtime_last_recovery_at":       budget.ActiveRuntimeLastRecoveryAt,
		"budget_status":                         budget.Status,
		"last_reason":                           budget.LastReason,
		"updated_at":                            budget.UpdatedAt,
	}
	if budget.RemainingTurns != nil {
		data["remaining_turns"] = *budget.RemainingTurns
	}
	if budget.RemainingActiveRuntime != nil {
		data["remaining_active_runtime_ms"] = *budget.RemainingActiveRuntime
	}
	return data
}
