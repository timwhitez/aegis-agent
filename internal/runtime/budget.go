package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/session"
)

type childBudgetDeadlineCause struct {
	reason string
}

func (c childBudgetDeadlineCause) Error() string {
	return c.reason
}

type childBudgetRun struct {
	mu              sync.Mutex
	engine          *Engine
	meta            session.SessionMetadata
	activeStartedAt time.Time
	activeTimer     *time.Timer
	active          bool
	accounted       bool
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
	return session.NewEffectiveBudget(
		source,
		maxTurns,
		maxActiveRuntimeSec,
		policy.MaxElapsedSec,
		startTurn,
		createdAt,
	)
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

func (e *Engine) beginChildBudgetRun(ctx context.Context, meta session.SessionMetadata, state session.State) (context.Context, context.CancelFunc, *childBudgetRun) {
	budget := session.CloneEffectiveBudget(meta.EffectiveBudget)
	if strings.TrimSpace(meta.ParentSessionID) == "" || !session.EffectiveBudgetEnabled(budget) {
		return ctx, func() {}, nil
	}
	now := timeNow().UTC()
	run := &childBudgetRun{engine: e, meta: meta, activeStartedAt: now, active: true}
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
	stop := context.AfterFunc(runCtx, func() {
		var cause childBudgetDeadlineCause
		if errors.As(context.Cause(runCtx), &cause) {
			e.control.requestPauseWithReason(cause.reason)
		}
	})
	return runCtx, func() {
		stop()
		run.mu.Lock()
		if run.activeTimer != nil {
			run.activeTimer.Stop()
		}
		run.mu.Unlock()
		if activeCancel != nil {
			activeCancel(context.Canceled)
		}
		deadlineCancel()
	}, run
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
	if r == nil {
		return nil, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.accounted {
		meta, err := r.engine.store.LoadMetadata(r.meta.ID)
		if err != nil {
			return nil, err
		}
		return session.CloneEffectiveBudget(meta.EffectiveBudget), nil
	}
	meta, err := r.engine.store.LoadMetadata(r.meta.ID)
	if err != nil {
		return nil, err
	}
	budget := session.CloneEffectiveBudget(meta.EffectiveBudget)
	if budget == nil {
		return nil, errors.New("child effective budget disappeared during active run")
	}
	if r.active {
		session.AddEffectiveBudgetRuntime(budget, timeNow().Sub(r.activeStartedAt), absoluteTurn)
		r.active = false
	}
	if r.activeTimer != nil {
		r.activeTimer.Stop()
	}
	if strings.TrimSpace(exhaustedReason) != "" {
		budget.Status = session.BudgetStatusExhausted
		budget.LastReason = exhaustedReason
	}
	if err := persistEffectiveBudget(r.engine.store, meta, budget); err != nil {
		return nil, err
	}
	r.accounted = true
	return session.CloneEffectiveBudget(budget), nil
}

func (r *childBudgetRun) pauseActive(absoluteTurn int) (*session.EffectiveBudget, error) {
	if r == nil {
		return nil, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.accounted || !r.active {
		meta, err := r.engine.store.LoadMetadata(r.meta.ID)
		if err != nil {
			return nil, err
		}
		return session.CloneEffectiveBudget(meta.EffectiveBudget), nil
	}
	if r.activeTimer != nil {
		r.activeTimer.Stop()
	}
	meta, err := r.engine.store.LoadMetadata(r.meta.ID)
	if err != nil {
		return nil, err
	}
	budget := session.CloneEffectiveBudget(meta.EffectiveBudget)
	if budget == nil {
		return nil, errors.New("child effective budget disappeared while entering background wait")
	}
	session.AddEffectiveBudgetRuntime(budget, timeNow().Sub(r.activeStartedAt), absoluteTurn)
	if err := persistEffectiveBudget(r.engine.store, meta, budget); err != nil {
		return nil, err
	}
	r.active = false
	return session.CloneEffectiveBudget(budget), nil
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
		"policy_version":            budget.PolicyVersion,
		"policy_source":             budget.Source,
		"turn_scope":                budget.TurnScope,
		"time_scope":                budget.TimeScope,
		"max_turns_per_attempt":     budget.MaxTurnsPerAttempt,
		"max_active_runtime_ms":     budget.MaxActiveRuntimeMS,
		"absolute_deadline_at":      budget.AbsoluteDeadlineAt,
		"attempt":                   budget.Attempt,
		"attempt_start_turn":        budget.AttemptStartTurn,
		"used_turns":                budget.UsedTurns,
		"used_active_runtime_ms":    budget.UsedActiveRuntimeMS,
		"total_used_turns":          budget.TotalUsedTurns,
		"total_active_runtime_ms":   budget.TotalActiveRuntimeMS,
		"overrun_turns":             budget.OverrunTurns,
		"overrun_active_runtime_ms": budget.OverrunActiveRuntimeMS,
		"budget_status":             budget.Status,
	}
	if budget.RemainingTurns != nil {
		data["remaining_turns"] = *budget.RemainingTurns
	}
	if budget.RemainingActiveRuntime != nil {
		data["remaining_active_runtime_ms"] = *budget.RemainingActiveRuntime
	}
	return data
}
