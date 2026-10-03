package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"aegis-agent/internal/config"
	"aegis-agent/internal/session"
)

type approvalBoundaryResponderFunc func(context.Context, string, session.PlanModeInputRequest) ([]session.PlanModeInputAnswer, error)

func (fn approvalBoundaryResponderFunc) RequestPlanInput(ctx context.Context, id string, request session.PlanModeInputRequest) ([]session.PlanModeInputAnswer, error) {
	return fn(ctx, id, request)
}

func newPlanInputApprovalBoundaryTest(t *testing.T) (*Registry, *session.Store, ExecContext, json.RawMessage) {
	t.Helper()
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	meta := session.SessionMetadata{SchemaVersion: 1, ID: session.NewSessionID(), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: t.TempDir(), Mode: session.ModeRun, Provider: "fake", Model: "fake", CompletionPolicy: session.CompletionPolicyInteractive}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePlanMode(meta.ID, session.PlanModeDraft{Enabled: true, Objective: "Original planning objective"}); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	execCtx := ExecContext{SessionID: meta.ID, ToolCallID: "call_plan_input", Workdir: meta.Workdir, Store: store, Config: cfg, PlanInputResponder: &recordingPlanInputResponder{}}
	raw := json.RawMessage(`{"questions":[{"id":"scope_choice","header":"Scope","question":"Which scope?","options":[{"label":"Narrow (Recommended)","description":"Keep the implementation focused."},{"label":"Broad","description":"More work."}]}]}`)
	return registry, store, execCtx, raw
}

func TestPlanInputApprovalBoundaryRollbackDoesNotOverwriteConcurrentPlan(t *testing.T) {
	for _, phase := range []string{"answer", "cancel"} {
		t.Run(phase, func(t *testing.T) {
			registry, store, execCtx, raw := newPlanInputApprovalBoundaryTest(t)
			other := session.NewStore(store.Root())
			if phase == "cancel" {
				execCtx.PlanInputResponder = failingPlanInputResponder{err: ErrPlanInputCancelled}
			}
			mutationDone := make(chan error, 1)
			started := false
			crossed := false
			failEvent := func() error {
				started = true
				go func() {
					_, _, err := other.MutatePlanMode(execCtx.SessionID, func(plan *session.PlanModeState) error { plan.Objective = "Concurrent planning objective"; return nil })
					mutationDone <- err
				}()
				select {
				case err := <-mutationDone:
					mutationDone <- err
					crossed = true
				case <-time.After(30 * time.Millisecond):
				}
				return errors.New("injected required event failure")
			}
			execCtx.EmitRequired = func(kind string, _ map[string]any) error {
				if kind == "planmode.input_answered" {
					return failEvent()
				}
				return nil
			}
			execCtx.EmitBatchRequired = func(_ []ToolEvent) error { return failEvent() }
			result, err := registry.Execute(context.Background(), "request_user_input", execCtx, raw)
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || !started {
				t.Fatalf("missing injected rollback: %#v", result)
			}
			select {
			case err := <-mutationDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("concurrent writer never released")
			}
			plan, err := store.LoadPlanMode(execCtx.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if crossed || plan.Objective != "Concurrent planning objective" {
				t.Fatalf("event/rollback escaped approval coordination: crossed=%t plan=%#v", crossed, plan)
			}
			if plan.PendingRequest == nil || plan.PendingRequest.Status != "pending" {
				t.Fatalf("failed control lost recoverable pending input: %#v", plan)
			}
		})
	}
}

func TestPlanInputApprovalBoundaryRejectsCancellationOfReplacedRequest(t *testing.T) {
	registry, store, execCtx, raw := newPlanInputApprovalBoundaryTest(t)
	var replacement session.PlanModeState
	execCtx.PlanInputResponder = approvalBoundaryResponderFunc(func(ctx context.Context, id string, _ session.PlanModeInputRequest) ([]session.PlanModeInputAnswer, error) {
		var err error
		replacement, err = store.CreatePlanMode(id, session.PlanModeDraft{Enabled: true, Objective: "Replacement plan"})
		return nil, errors.Join(err, ErrPlanInputCancelled)
	})
	result, err := registry.Execute(context.Background(), "request_user_input", execCtx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("stale cancellation unexpectedly succeeded: %#v", result)
	}
	current, err := store.LoadPlanMode(execCtx.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if current.PlanModeID != replacement.PlanModeID || current.Status != session.PlanModeStatusPlanning {
		t.Fatalf("old responder cancelled replacement plan: %#v", current)
	}
}

func TestPlanInputApprovalBoundaryWaitAllowsConcurrentMutation(t *testing.T) {
	registry, store, execCtx, raw := newPlanInputApprovalBoundaryTest(t)
	execCtx.PlanInputResponder = approvalBoundaryResponderFunc(func(ctx context.Context, id string, request session.PlanModeInputRequest) ([]session.PlanModeInputAnswer, error) {
		done := make(chan error, 1)
		go func() {
			_, _, err := store.MutatePlanMode(id, func(plan *session.PlanModeState) error { plan.Objective = "Updated during human wait"; return nil })
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				return nil, err
			}
		case <-time.After(2 * time.Second):
			return nil, errors.New("human responder is holding approval coordination lock")
		}
		return []session.PlanModeInputAnswer{{QuestionID: "scope_choice", Label: request.Questions[0].Options[0].Label, Value: request.Questions[0].Options[0].Description}}, nil
	})
	result, err := registry.Execute(context.Background(), "request_user_input", execCtx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("legal live answer failed: %#v", result)
	}
	current, err := store.LoadPlanMode(execCtx.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Objective != "Updated during human wait" || current.PendingRequest != nil || current.Status != session.PlanModeStatusPlanning {
		t.Fatalf("wait or completion lost current facts: %#v", current)
	}
}

func TestPlanInputApprovalBoundaryRebindsEventStore(t *testing.T) {
	registry, store, execCtx, raw := newPlanInputApprovalBoundaryTest(t)
	eventCount := 0
	execCtx.EmitRequired = func(string, map[string]any) error {
		t.Error("unscoped emitter used inside approval boundary")
		return errors.New("unscoped emitter")
	}
	execCtx.ScopedEvents = func(scoped *session.Store) (func(string, map[string]any), func(string, map[string]any) error, func([]ToolEvent) error) {
		return nil, func(kind string, _ map[string]any) error {
			if scoped == store {
				return errors.New("emitter retained original Store")
			}
			// A nested snapshot must reuse the callback scope instead of opening
			// an independent file descriptor for the same approval lock.
			if _, err := scoped.LoadApprovalSnapshot(execCtx.SessionID); err != nil {
				return err
			}
			eventCount++
			return nil
		}, nil
	}
	result, err := registry.Execute(context.Background(), "request_user_input", execCtx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || eventCount != 2 {
		t.Fatalf("scoped event callbacks failed: count=%d result=%#v", eventCount, result)
	}
}
