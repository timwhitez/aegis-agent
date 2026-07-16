package session

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	ChildBudgetTurnsExceededReason            = "child_budget_turns_exceeded"
	ChildBudgetActiveRuntimeExceededReason    = "child_budget_active_runtime_exceeded"
	ChildBudgetAbsoluteDeadlineExceededReason = "child_budget_absolute_deadline_exceeded"
	ChildBudgetLegacyWallClockExceededReason  = "child_budget_wallclock_exceeded"
)

func NewEffectiveBudget(source string, maxTurns int, maxActiveRuntimeSec int, maxElapsedSec int, startTurn int, createdAt time.Time) *EffectiveBudget {
	if strings.TrimSpace(source) == "" {
		source = BudgetSourceRuntimeChild
	}
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	budget := &EffectiveBudget{
		SchemaVersion:      1,
		PolicyVersion:      BudgetPolicyVersion,
		Source:             source,
		TurnScope:          BudgetTurnScopePerAttempt,
		TimeScope:          BudgetTimeScopeActiveRuntime,
		MaxTurnsPerAttempt: maxNonNegative(maxTurns),
		MaxActiveRuntimeMS: int64(maxNonNegative(maxActiveRuntimeSec)) * int64(time.Second/time.Millisecond),
		Attempt:            1,
		AttemptStartTurn:   maxNonNegative(startTurn),
		AttemptStartedAt:   createdAt.UTC().Format(time.RFC3339Nano),
		Status:             BudgetStatusActive,
		UpdatedAt:          createdAt.UTC().Format(time.RFC3339Nano),
	}
	if maxElapsedSec > 0 {
		budget.AbsoluteDeadlineAt = createdAt.Add(time.Duration(maxElapsedSec) * time.Second).UTC().Format(time.RFC3339Nano)
	}
	RefreshEffectiveBudget(budget, startTurn)
	if !EffectiveBudgetEnabled(budget) {
		budget.Status = BudgetStatusDisabled
	}
	return budget
}

func CloneEffectiveBudget(input *EffectiveBudget) *EffectiveBudget {
	if input == nil {
		return nil
	}
	clone := *input
	if input.RemainingTurns != nil {
		value := *input.RemainingTurns
		clone.RemainingTurns = &value
	}
	if input.RemainingActiveRuntime != nil {
		value := *input.RemainingActiveRuntime
		clone.RemainingActiveRuntime = &value
	}
	return &clone
}

func EffectiveBudgetEnabled(budget *EffectiveBudget) bool {
	return budget != nil && (budget.MaxTurnsPerAttempt > 0 || budget.MaxActiveRuntimeMS > 0 || strings.TrimSpace(budget.AbsoluteDeadlineAt) != "")
}

func RefreshEffectiveBudget(budget *EffectiveBudget, absoluteTurn int) {
	if budget == nil {
		return
	}
	if absoluteTurn < budget.AttemptStartTurn {
		absoluteTurn = budget.AttemptStartTurn
	}
	usedTurns := absoluteTurn - budget.AttemptStartTurn
	if delta := usedTurns - budget.UsedTurns; delta > 0 {
		budget.TotalUsedTurns += delta
	}
	budget.UsedTurns = usedTurns
	budget.TotalUsedTurns = maxNonNegative(budget.TotalUsedTurns)
	budget.RemainingTurns = nil
	budget.OverrunTurns = 0
	if budget.MaxTurnsPerAttempt > 0 {
		remaining := budget.MaxTurnsPerAttempt - budget.UsedTurns
		if remaining < 0 {
			budget.OverrunTurns = -remaining
			remaining = 0
		}
		budget.RemainingTurns = &remaining
	}
	budget.RemainingActiveRuntime = nil
	budget.OverrunActiveRuntimeMS = 0
	if budget.MaxActiveRuntimeMS > 0 {
		remaining := budget.MaxActiveRuntimeMS - budget.UsedActiveRuntimeMS
		if remaining < 0 {
			budget.OverrunActiveRuntimeMS = -remaining
			remaining = 0
		}
		budget.RemainingActiveRuntime = &remaining
	}
	if !EffectiveBudgetEnabled(budget) {
		budget.Status = BudgetStatusDisabled
	} else if budget.Status == "" || budget.Status == BudgetStatusDisabled {
		budget.Status = BudgetStatusActive
	}
	budget.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
}

func AddEffectiveBudgetRuntime(budget *EffectiveBudget, elapsed time.Duration, absoluteTurn int) {
	if budget == nil {
		return
	}
	ms := elapsed.Milliseconds()
	if elapsed > 0 && ms == 0 {
		ms = 1
	}
	if ms > 0 {
		budget.UsedActiveRuntimeMS += ms
		budget.TotalActiveRuntimeMS += ms
	}
	RefreshEffectiveBudget(budget, absoluteTurn)
}

func EffectiveBudgetExceededReason(budget *EffectiveBudget, absoluteTurn int, now time.Time) string {
	if !EffectiveBudgetEnabled(budget) {
		return ""
	}
	RefreshEffectiveBudget(budget, absoluteTurn)
	if budget.MaxTurnsPerAttempt > 0 && budget.UsedTurns >= budget.MaxTurnsPerAttempt {
		return ChildBudgetTurnsExceededReason
	}
	if budget.MaxActiveRuntimeMS > 0 && budget.UsedActiveRuntimeMS >= budget.MaxActiveRuntimeMS {
		return ChildBudgetActiveRuntimeExceededReason
	}
	if deadline, ok := EffectiveBudgetDeadline(budget); ok && !now.Before(deadline) {
		return ChildBudgetAbsoluteDeadlineExceededReason
	}
	return ""
}

func EffectiveBudgetDeadline(budget *EffectiveBudget) (time.Time, bool) {
	if budget == nil || strings.TrimSpace(budget.AbsoluteDeadlineAt) == "" {
		return time.Time{}, false
	}
	deadline, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(budget.AbsoluteDeadlineAt))
	return deadline, err == nil
}

func ExtendEffectiveBudget(current *EffectiveBudget, extension BudgetExtension, absoluteTurn int, now time.Time) (*EffectiveBudget, error) {
	if current == nil {
		return nil, errors.New("effective child budget is missing")
	}
	if extension.AddTurns < 0 || extension.AddActiveRuntimeSec < 0 || extension.ExtendDeadlineSec < 0 {
		return nil, errors.New("budget extension values must be non-negative")
	}
	if extension.AddTurns == 0 && extension.AddActiveRuntimeSec == 0 && extension.ExtendDeadlineSec == 0 &&
		!extension.ClearTurnLimit && !extension.ClearActiveRuntime && !extension.ClearAbsoluteDeadline {
		return nil, errors.New("budget_extension must add or clear at least one budget dimension")
	}
	if extension.ClearTurnLimit && extension.AddTurns > 0 {
		return nil, errors.New("budget_extension cannot add turns and clear the turn limit together")
	}
	if extension.ClearActiveRuntime && extension.AddActiveRuntimeSec > 0 {
		return nil, errors.New("budget_extension cannot add active runtime and clear the active runtime limit together")
	}
	if extension.ClearAbsoluteDeadline && extension.ExtendDeadlineSec > 0 {
		return nil, errors.New("budget_extension cannot extend and clear the absolute deadline together")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	next := CloneEffectiveBudget(current)
	RefreshEffectiveBudget(next, absoluteTurn)
	remainingTurns := 0
	if next.RemainingTurns != nil {
		remainingTurns = *next.RemainingTurns
	}
	remainingRuntimeMS := int64(0)
	if next.RemainingActiveRuntime != nil {
		remainingRuntimeMS = *next.RemainingActiveRuntime
	}
	if extension.ClearTurnLimit {
		next.MaxTurnsPerAttempt = 0
	} else if next.MaxTurnsPerAttempt > 0 || extension.AddTurns > 0 {
		next.MaxTurnsPerAttempt = remainingTurns + extension.AddTurns
	}
	if extension.ClearActiveRuntime {
		next.MaxActiveRuntimeMS = 0
	} else if next.MaxActiveRuntimeMS > 0 || extension.AddActiveRuntimeSec > 0 {
		next.MaxActiveRuntimeMS = remainingRuntimeMS + int64(extension.AddActiveRuntimeSec)*int64(time.Second/time.Millisecond)
	}
	if extension.ClearAbsoluteDeadline {
		next.AbsoluteDeadlineAt = ""
	} else if extension.ExtendDeadlineSec > 0 {
		base := now
		if deadline, ok := EffectiveBudgetDeadline(next); ok && deadline.After(base) {
			base = deadline
		}
		next.AbsoluteDeadlineAt = base.Add(time.Duration(extension.ExtendDeadlineSec) * time.Second).UTC().Format(time.RFC3339Nano)
	}
	next.Attempt++
	if next.Attempt <= 1 {
		next.Attempt = 2
	}
	next.AttemptStartTurn = maxNonNegative(absoluteTurn)
	next.AttemptStartedAt = now.UTC().Format(time.RFC3339Nano)
	next.UsedTurns = 0
	next.UsedActiveRuntimeMS = 0
	next.OverrunTurns = 0
	next.OverrunActiveRuntimeMS = 0
	next.LastReason = ""
	next.Status = BudgetStatusActive
	RefreshEffectiveBudget(next, absoluteTurn)
	if reason := EffectiveBudgetExceededReason(next, absoluteTurn, now); reason != "" {
		return nil, fmt.Errorf("budget_extension does not make the exhausted dimension resumable: %s", reason)
	}
	return next, ValidateEffectiveBudget(next)
}

func IsChildBudgetPauseReason(reason string) bool {
	switch strings.TrimSpace(reason) {
	case ChildBudgetTurnsExceededReason,
		ChildBudgetActiveRuntimeExceededReason,
		ChildBudgetAbsoluteDeadlineExceededReason,
		ChildBudgetLegacyWallClockExceededReason:
		return true
	default:
		return false
	}
}

func ValidateEffectiveBudget(budget *EffectiveBudget) error {
	if budget == nil {
		return nil
	}
	if budget.SchemaVersion <= 0 || budget.PolicyVersion <= 0 {
		return errors.New("effective budget schema_version and policy_version must be positive")
	}
	if strings.TrimSpace(budget.Source) == "" {
		return errors.New("effective budget source is required")
	}
	if budget.TurnScope != BudgetTurnScopePerAttempt {
		return fmt.Errorf("invalid effective budget turn_scope %q", budget.TurnScope)
	}
	if budget.TimeScope != BudgetTimeScopeActiveRuntime {
		return fmt.Errorf("invalid effective budget time_scope %q", budget.TimeScope)
	}
	if budget.MaxTurnsPerAttempt < 0 || budget.MaxActiveRuntimeMS < 0 || budget.Attempt < 1 || budget.AttemptStartTurn < 0 || budget.UsedTurns < 0 || budget.UsedActiveRuntimeMS < 0 || budget.TotalUsedTurns < 0 || budget.TotalActiveRuntimeMS < 0 {
		return errors.New("effective budget counters and limits must be non-negative and attempt must be positive")
	}
	if strings.TrimSpace(budget.AbsoluteDeadlineAt) != "" {
		if _, err := time.Parse(time.RFC3339Nano, budget.AbsoluteDeadlineAt); err != nil {
			return fmt.Errorf("effective budget absolute_deadline_at must be RFC3339Nano: %w", err)
		}
	}
	if strings.TrimSpace(budget.AttemptStartedAt) != "" {
		if _, err := time.Parse(time.RFC3339Nano, budget.AttemptStartedAt); err != nil {
			return fmt.Errorf("effective budget attempt_started_at must be RFC3339Nano: %w", err)
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, budget.UpdatedAt); err != nil {
		return fmt.Errorf("effective budget updated_at must be RFC3339Nano: %w", err)
	}
	switch budget.Status {
	case BudgetStatusDisabled, BudgetStatusActive, BudgetStatusExhausted, BudgetStatusCancelled:
	default:
		return fmt.Errorf("invalid effective budget status %q", budget.Status)
	}
	return nil
}

func maxNonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}
