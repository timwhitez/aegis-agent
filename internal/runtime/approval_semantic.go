package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"aegis-agent/internal/session"
	"aegis-agent/internal/tools"
)

func (e *Engine) newApprovalScopedEngine(scoped *session.Store) *Engine {
	scopedEngine := NewEngine(e.cfg, scoped, e.bus, e.control)
	scopedEngine.runner = e.runner
	scopedEngine.beforeAppendEvent = e.beforeAppendEvent
	return scopedEngine
}

// These tools can update the reviewed goal/plan scope and roll back several
// files after event errors. Keep their complete mutation/rollback chain inside
// the approval boundary. request_user_input waits for an operator and therefore
// coordinates its individual mutation phases in the tool instead.
func (e *Engine) executeApprovalSemanticTool(ctx context.Context, registry *tools.Registry, name string, execCtx tools.ExecContext, args json.RawMessage) (result session.ToolResult, err error) {
	switch name {
	case "create_goal", "update_goal", "record_goal_progress", "submit_plan":
		err = e.store.WithApprovalLock(execCtx.SessionID, func(scoped *session.Store) error {
			execCtx.Store = scoped
			execCtx.Emit, execCtx.EmitRequired, execCtx.EmitBatchRequired = e.approvalScopedEvents(scoped, execCtx.SessionID, "tool_execute")
			result, err = registry.Execute(ctx, name, execCtx, args)
			return err
		})
		return result, err
	default:
		return registry.Execute(ctx, name, execCtx, args)
	}
}

// A budget update is outside the approval projection, but a stale whole-goal
// write or rollback could clobber approved scope. Load its rollback snapshot and
// apply the update under the same boundary as semantic goal mutations.
func (e *Engine) startGoalBudgetWrapUpTurn(ctx context.Context, sessionID string) (goal *session.SessionGoal, started bool, waiting bool, err error) {
	err = e.store.WithApprovalLock(sessionID, func(scoped *session.Store) error {
		// This fresh goal will be used to build the provider prompt. It must
		// still belong to the reviewed scope, even if a later disk comparison
		// could observe that a concurrent semantic edit has been reverted.
		snapshot, loadErr := e.newApprovalScopedEngine(scoped).approvalExecutionSnapshot(ctx, sessionID)
		if loadErr != nil {
			return loadErr
		}
		var current *session.SessionGoal
		if snapshot != nil {
			current = snapshot.Goal
		} else {
			current, loadErr = loadGoalOptional(scoped, sessionID)
			if loadErr != nil {
				return loadErr
			}
		}
		goal = current
		if current == nil || current.Status != session.GoalStatusBudgetLimited || !current.Control.StopOnBudget || current.BudgetWrapUpRequestedAt == "" || session.HasBudgetWrapUpRecord(*current) {
			return nil
		}
		goalCopy := *current
		if !session.MarkBudgetWrapUpTurnStarted(&goalCopy) {
			waiting = true
			return nil
		}
		history, historyErr := scoped.LoadGoalHistory(sessionID)
		if historyErr != nil {
			return historyErr
		}
		if saveErr := scoped.SaveGoal(sessionID, goalCopy); saveErr != nil {
			return saveErr
		}
		rollback := func(cause error) error {
			var restoreErrs []error
			if restoreErr := scoped.SaveGoal(sessionID, *current); restoreErr != nil {
				restoreErrs = append(restoreErrs, fmt.Errorf("restore goal: %w", restoreErr))
			}
			if restoreErr := scoped.RestoreGoalHistory(sessionID, history); restoreErr != nil {
				restoreErrs = append(restoreErrs, fmt.Errorf("restore goal history: %w", restoreErr))
			}
			return errors.Join(append([]error{cause}, restoreErrs...)...)
		}
		if historyErr := scoped.AppendGoalHistory(sessionID, session.GoalHistoryEntry{Type: "goal.budget_wrapup_turn_started", Source: session.GoalSourceSystem, Status: goalCopy.Status, Data: map[string]any{"budget_wrapup_turn_started_at": goalCopy.BudgetWrapUpTurnStartedAt}}); historyErr != nil {
			return rollback(historyErr)
		}
		scopedEngine := e.newApprovalScopedEngine(scoped)
		if eventErr := scopedEngine.appendEvent(sessionID, "goal.budget_wrapup_turn_started", "prepare", goalEventData(goalCopy)); eventErr != nil {
			return rollback(fmt.Errorf("record goal.budget_wrapup_turn_started event: %w", eventErr))
		}
		goal = &goalCopy
		started = true
		return nil
	})
	return goal, started, waiting, err
}

func (e *Engine) approvalScopedEvents(scoped *session.Store, sessionID, phase string) (func(string, map[string]any), func(string, map[string]any) error, func([]tools.ToolEvent) error) {
	scopedEngine := e.newApprovalScopedEngine(scoped)
	return func(eventType string, data map[string]any) { scopedEngine.emit(sessionID, eventType, phase, data) },
		func(eventType string, data map[string]any) error {
			return scopedEngine.appendEvent(sessionID, eventType, phase, data)
		},
		func(items []tools.ToolEvent) error { return scopedEngine.appendToolEvents(sessionID, phase, items) }
}
