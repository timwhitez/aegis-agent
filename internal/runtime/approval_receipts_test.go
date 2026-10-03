package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"aegis-agent/internal/config"
	"aegis-agent/internal/fileutil"
	"aegis-agent/internal/session"
)

// Actual Continue controls on main a659, before receipt admission is implemented.
func receiptBaselineRunFacts(t *testing.T, r *Runner, id string) (session.State, int, int) {
	t.Helper()
	state, err := r.store.LoadState(id)
	if err != nil {
		t.Fatal(err)
	}
	events, err := r.store.LoadEvents(id)
	if err != nil {
		t.Fatal(err)
	}
	resumed := 0
	for _, event := range events {
		if event.Type == "session.resumed" {
			resumed++
		}
	}
	messages, err := r.store.LoadMessages(id)
	if err != nil {
		t.Fatal(err)
	}
	return state, resumed, countPlanModeApprovalMessages(messages)
}

func TestApprovalReceiptAncestorSyncFailureNeverStartsProvider(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	request := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "ancestor-sync-failure"}
	cause := errors.New("injected ancestor directory sync failure")
	r.approvalAdmit = func(store *session.Store, id, operationID, generation string) (session.ApprovalReceipt, error) {
		path := filepath.Join(store.SessionDir(id), "approval-operations.json")
		data, err := os.ReadFile(path)
		if err != nil {
			return session.ApprovalReceipt{}, err
		}
		// Inject the real opt-in writer before the actual Store admission. Store
		// package tests independently verify that its production writer opts in.
		options := fileutil.AtomicCommitOptions{SyncParentChain: true, BeforeStage: func(stage fileutil.AtomicCommitStage) error {
			if stage == fileutil.AtomicCommitBeforeParentChainSync {
				return cause
			}
			return nil
		}}
		outcome, err := fileutil.AtomicCommitFileNoSymlink(path, data, 0600, options)
		if err != nil {
			return session.ApprovalReceipt{}, &session.ApprovalReceiptCommitError{Outcome: outcome, Err: err}
		}
		return store.AdmitApprovalOperation(id, operationID, generation)
	}
	_, err := r.Continue(context.Background(), request)
	var commitErr *session.ApprovalReceiptCommitError
	if !errors.As(err, &commitErr) || commitErr.Outcome != fileutil.AtomicCommitNotPublished || !errors.Is(err, cause) || calls.Load() != 0 {
		t.Fatalf("ancestor sync failure admitted execution: error=%v provider=%d", err, calls.Load())
	}
	lookup, err := r.store.GetApprovalReceipt(id, request.ApprovalRequestID)
	if err != nil || lookup.Receipt.Stage != session.ApprovalReceiptPrepared {
		t.Fatalf("ancestor failure advanced canonical operation: %#v %v", lookup, err)
	}
}

func TestApprovalReceiptSameRequestDoesNotReadmit(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	target := approvalTargetForTest(t, r, id)
	req := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: target, ApprovalRequestID: "operation-one", Source: "cli"}
	first, err := r.Continue(context.Background(), req)
	if err != nil || first.Status != session.StatusCompleted || calls.Load() != 1 {
		t.Fatalf("first exact-target approval did not complete against the fixed local mock: %#v %v provider_calls=%d", first, err, calls.Load())
	}
	firstState, firstResumed, firstReplay := receiptBaselineRunFacts(t, r, id)
	second, secondErr := r.Continue(context.Background(), req)
	secondState, secondResumed, secondReplay := receiptBaselineRunFacts(t, r, id)
	t.Logf("same_target=%#v first=%s second=%s second_error=%v provider_calls=%d->%d run_generation=%s->%s resumed_events=%d->%d approval_replay_messages=%d->%d", *target, first.Status, second.Status, secondErr, 1, calls.Load(), firstState.RunGeneration, secondState.RunGeneration, firstResumed, secondResumed, firstReplay, secondReplay)
	if calls.Load() != 1 || secondState.RunGeneration != firstState.RunGeneration || secondResumed != firstResumed {
		t.Fatalf("completed approval target was admitted again: provider_calls=%d resumed_events=%d->%d generation_changed=%v", calls.Load(), firstResumed, secondResumed, secondState.RunGeneration != firstState.RunGeneration)
	}
}

func TestApprovalReceiptOrdinaryFollowupControl(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	first, err := r.Continue(context.Background(), ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "ordinary-control-approval"})
	if err != nil || first.Status != session.StatusCompleted || calls.Load() != 1 {
		t.Fatalf("initial approval did not complete: %#v %v provider_calls=%d", first, err, calls.Load())
	}
	firstState, firstResumed, firstReplay := receiptBaselineRunFacts(t, r, id)
	followup, err := r.Continue(context.Background(), ContinueRequest{SessionID: id, Message: "Please perform this explicitly requested follow-up", Source: "cli"})
	finalState, finalResumed, finalReplay := receiptBaselineRunFacts(t, r, id)
	t.Logf("ordinary_followup=%s error=%v provider_calls=%d run_generation=%s->%s resumed_events=%d->%d approval_replay_messages=%d->%d", followup.Status, err, calls.Load(), firstState.RunGeneration, finalState.RunGeneration, firstResumed, finalResumed, firstReplay, finalReplay)
	if err != nil || followup.Status != session.StatusCompleted || calls.Load() != 2 || finalState.RunGeneration == firstState.RunGeneration || finalResumed != firstResumed+1 || finalReplay != firstReplay {
		t.Fatalf("explicit ordinary follow-up did not retain its execution semantics: %#v %v state=%#v", followup, err, finalState)
	}
	messages, err := r.store.LoadMessages(id)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, msg := range messages {
		if msg.Role == "user" && strings.Contains(msg.Text, "explicitly requested follow-up") {
			found = true
		}
	}
	if !found {
		t.Fatal("ordinary follow-up message was not retained")
	}
}

func TestApprovalReceiptMissingIDRequiresUpgrade(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	before, _, _ := receiptBaselineRunFacts(t, r, id)
	result, err := r.Continue(context.Background(), ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id)})
	after, resumed, replay := receiptBaselineRunFacts(t, r, id)
	t.Logf("missing_request_id result=%s error=%v provider_calls=%d resumed_events=%d approval_replay_messages=%d before=%s after=%s", result.Status, err, calls.Load(), resumed, replay, before.Status, after.Status)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "approval") || !strings.Contains(strings.ToLower(err.Error()), "request") || calls.Load() != 0 || resumed != 0 || replay != 0 || !reflect.DeepEqual(before, after) {
		t.Fatalf("identity-free approval was accepted instead of an upgrade error: result=%#v error=%v provider_calls=%d", result, err, calls.Load())
	}
}

func TestApprovalReceiptConflictingControlsPrecedeIdentityAndReplay(t *testing.T) {
	for _, identity := range []string{"missing_identity", "missing_target", "known_request", "new_alias"} {
		for _, control := range []string{"start", "cancel", "answer_input"} {
			t.Run(identity+"/"+control, func(t *testing.T) {
				r, id, calls := newApprovalTargetFixture(t)
				req := ContinueRequest{SessionID: id, ApprovePlan: true}
				if identity == "missing_target" {
					req.ApprovalRequestID = "missing-target"
				}
				if identity == "known_request" || identity == "new_alias" {
					req.ApprovalRequestID = "admitted-control"
					req.ApprovalTarget = approvalTargetForTest(t, r, id)
					if _, err := r.Continue(context.Background(), req); err != nil {
						t.Fatal(err)
					}
					if identity == "new_alias" {
						req.ApprovalRequestID = "must-not-bind-alias"
					}
				}
				switch control {
				case "start":
					req.PlanMode = &session.PlanModeDraft{Enabled: true, Objective: "Conflicting start"}
				case "cancel":
					req.CancelPlan = true
				case "answer_input":
					req.PlanInputRequestID = "conflicting-input"
					req.PlanInputAnswers = []session.PlanModeInputAnswer{{QuestionID: "choice", Value: "answer"}}
				}
				before, resumed, replay := receiptBaselineRunFacts(t, r, id)
				plan, err := r.store.LoadPlanMode(id)
				if err != nil {
					t.Fatal(err)
				}
				ledgerPath := filepath.Join(r.cfg.Session.Dir, id, "approval-operations.json")
				ledger, readErr := os.ReadFile(ledgerPath)
				if readErr != nil && !os.IsNotExist(readErr) {
					t.Fatal(readErr)
				}
				providerCalls := calls.Load()
				entries := []struct {
					name string
					call func() error
				}{
					{"continue", func() error { _, err := r.Continue(context.Background(), req); return err }},
					{"prepare", func() error { _, err := r.PrepareApprovalOperation(context.Background(), req); return err }},
					{"lookup", func() error { _, err := r.LookupApprovalContinue(req); return err }},
				}
				for _, entry := range entries {
					if err := entry.call(); err == nil || !strings.Contains(err.Error(), "conflicting plan mode controls") {
						t.Errorf("%s checked identity or replay before incompatible controls: %v", entry.name, err)
					}
				}
				after, afterResumed, afterReplay := receiptBaselineRunFacts(t, r, id)
				afterPlan, err := r.store.LoadPlanMode(id)
				if err != nil {
					t.Fatal(err)
				}
				afterLedger, afterReadErr := os.ReadFile(ledgerPath)
				if calls.Load() != providerCalls || resumed != afterResumed || replay != afterReplay || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(plan, afterPlan) || !bytes.Equal(ledger, afterLedger) || os.IsNotExist(readErr) != os.IsNotExist(afterReadErr) {
					t.Fatal("incompatible controls changed approval, alias, run, or replay facts")
				}
			})
		}
	}
}

func TestApprovalReceiptNewRequestSameTargetDoesNotReadmit(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	target := approvalTargetForTest(t, r, id)
	req := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: target, ApprovalRequestID: "first-request"}
	if _, err := r.Continue(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	before, resumed, _ := receiptBaselineRunFacts(t, r, id)
	req.ApprovalRequestID = "second-request"
	result, err := r.Continue(context.Background(), req)
	after, afterResumed, _ := receiptBaselineRunFacts(t, r, id)
	t.Logf("new_request_same_target result=%#v error=%v provider_calls=%d generation=%s->%s", result, err, calls.Load(), before.RunGeneration, after.RunGeneration)
	if err != nil || calls.Load() != 1 || after.RunGeneration != before.RunGeneration || afterResumed != resumed {
		t.Fatalf("new request ID readmitted the same target: result=%#v error=%v provider_calls=%d", result, err, calls.Load())
	}
}

func TestApprovalReceiptCorruptUnrelatedRuntimePayloadClosesNewID(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	req := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "operation-a"}
	if _, err := r.Continue(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.cfg.Session.Dir, id, "approval-operations.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ledger map[string]any
	if err := json.Unmarshal(raw, &ledger); err != nil {
		t.Fatal(err)
	}
	operation := ledger["operations"].(map[string]any)["operation-a"].(map[string]any)
	operation["recovery"].(map[string]any)["data"].(map[string]any)["complete"] = false
	raw, err = json.Marshal(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	// The Store envelope is still valid. Only the runtime's typed admission facts
	// are incomplete, and a new request/target must not bypass that corruption.
	if _, err := r.store.GetApprovalReceipt(id, "operation-a"); err != nil {
		t.Fatalf("fixture broke Store envelope: %v", err)
	}
	if _, _, err := r.store.MutatePlanMode(id, func(plan *session.PlanModeState) error {
		plan.Status = session.PlanModeStatusAwaitingApproval
		plan.PlanVersion++
		plan.PlanMarkdown = "New reviewed plan"
		plan.Summary = "New scope"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	req.ApprovalTarget = approvalTargetForTest(t, r, id)
	req.ApprovalRequestID = "operation-b"
	before, _, _ := receiptBaselineRunFacts(t, r, id)
	result, err := r.PrepareApprovalOperation(context.Background(), req)
	after, _, _ := receiptBaselineRunFacts(t, r, id)
	if result.Prepared != nil {
		defer r.AbortPreparedApproval(result.Prepared, nil)
	}
	t.Logf("new target/id after unrelated corrupt recovery: prepared=%v error=%v provider_calls=%d", result.Prepared != nil, err, calls.Load())
	if !errors.Is(err, session.ErrApprovalReceiptUnverifiable) || result.Prepared != nil || !reflect.DeepEqual(before, after) || calls.Load() != 1 {
		t.Fatalf("unrelated corrupt recovery bypassed: prepared=%v error=%v statechanged=%v", result.Prepared != nil, err, !reflect.DeepEqual(before, after))
	}
}

func TestApprovalReceiptCompletedPreparedPayloadCannotBecomePartial(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	req := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "completed-preparation"}
	r.approvalAdmit = func(*session.Store, string, string, string) (session.ApprovalReceipt, error) {
		return session.ApprovalReceipt{}, &session.ApprovalReceiptCommitError{Outcome: fileutil.AtomicCommitNotPublished, Err: errors.New("admission not published")}
	}
	partial, err := r.PrepareApprovalOperation(context.Background(), req)
	if err == nil || partial.Lookup.Receipt.Stage != session.ApprovalReceiptPrepared {
		t.Fatalf("fixture did not leave an unadmitted completed preparation: %v", err)
	}
	payload, err := decodeApprovalRecovery(partial.Lookup.Receipt)
	if err != nil || !payload.Complete || payload.Preparation.CompletedPhase != "prepared" {
		t.Fatalf("legitimate completed preparation was invalid: %v", err)
	}
	path := filepath.Join(r.cfg.Session.Dir, id, "approval-operations.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ledger map[string]any
	if err := json.Unmarshal(raw, &ledger); err != nil {
		t.Fatal(err)
	}
	data := ledger["operations"].(map[string]any)[req.ApprovalRequestID].(map[string]any)["recovery"].(map[string]any)["data"].(map[string]any)
	data["complete"] = false
	data["plan_history"] = []any{}
	raw, err = json.Marshal(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.GetApprovalReceipt(id, req.ApprovalRequestID); err != nil {
		t.Fatalf("fixture damaged Store envelope: %v", err)
	}
	_, queryErr := r.ApprovalReceipt(id, req.ApprovalRequestID)
	if _, _, err := r.store.MutatePlanMode(id, func(plan *session.PlanModeState) error {
		plan.PlanMarkdown = "A different target cannot bypass a damaged prepared receipt"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	newReq := req
	newReq.ApprovalRequestID = "bypass-damaged-preparation"
	newReq.ApprovalTarget = approvalTargetForTest(t, r, id)
	r.approvalAdmit = nil
	newResult, newErr := r.PrepareApprovalOperation(context.Background(), newReq)
	if newResult.Prepared != nil {
		defer newResult.Prepared.releaseRunSlot()
	}
	if !errors.Is(queryErr, session.ErrApprovalReceiptUnverifiable) || !errors.Is(newErr, session.ErrApprovalReceiptUnverifiable) || calls.Load() != 0 {
		t.Fatalf("completed prepared payload was accepted as early partial: query=%v new_target=%v provider=%d", queryErr, newErr, calls.Load())
	}
}

func TestApprovalReceiptReplayPrecedesCurrentScopeAndDefaults(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	req := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "old-scope"}
	first, err := r.Continue(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.store.MutatePlanMode(id, func(plan *session.PlanModeState) error { plan.Summary = "Changed current scope"; return nil }); err != nil {
		t.Fatal(err)
	}
	delete(r.cfg.Providers, r.cfg.DefaultProvider)
	before, _, _ := receiptBaselineRunFacts(t, r, id)
	replay, err := r.PrepareApprovalOperation(context.Background(), req)
	after, _, _ := receiptBaselineRunFacts(t, r, id)
	if err != nil || !replay.Replay || replay.Prepared != nil || replay.RecoveryRequired || replay.Lookup.Receipt.OperationID != first.Approval.Lookup.Receipt.OperationID || !reflect.DeepEqual(before, after) || calls.Load() != 1 {
		t.Fatalf("receipt-first replay failed: %#v error=%v", replay, err)
	}
	req.Message = "Changed explicit parameter"
	if _, err := r.PrepareApprovalOperation(context.Background(), req); !errors.Is(err, session.ErrApprovalRequestConflict) {
		t.Fatalf("same ID accepted changed parameters: %v", err)
	}
}

func TestApprovalReceiptAliasRetainsCanonicalParameters(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	req := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "canonical"}
	first, err := r.Continue(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.ApprovalRequestID = "alias"
	req.Message = "Different explicit message"
	req.Provider = "unknown provider"
	replay, err := r.PrepareApprovalOperation(context.Background(), req)
	if err != nil || !replay.Replay || replay.Prepared != nil || calls.Load() != 1 || !reflect.DeepEqual(replay.Lookup.Receipt.Parameters, first.Approval.Lookup.Receipt.Parameters) || replay.Lookup.Binding.Parameters.Message != req.Message {
		t.Fatalf("alias changed canonical admission: %#v %v", replay, err)
	}
	var outcome *ApprovalPreparationOutcomeError
	if _, err := r.PrepareApprovalContinue(context.Background(), req); !errors.As(err, &outcome) || outcome.Result.Lookup.Binding.RequestID != "alias" {
		t.Fatalf("strict wrapper lost explicit replay: %v", err)
	}
}

func TestApprovalReceiptNewTargetNewIDControl(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	req := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "target-one"}
	if _, err := r.Continue(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.store.MutatePlanMode(id, func(plan *session.PlanModeState) error {
		plan.PlanVersion++
		plan.Status = session.PlanModeStatusAwaitingApproval
		plan.PlanMarkdown = "New approved work"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	req.ApprovalRequestID = "target-two"
	req.ApprovalTarget = approvalTargetForTest(t, r, id)
	second, err := r.Continue(context.Background(), req)
	if err != nil || second.Status != session.StatusCompleted || calls.Load() != 2 || second.Approval.Lookup.Receipt.OperationID != "target-two" {
		t.Fatalf("fresh target failed legitimate admission: %#v %v", second, err)
	}
}

func TestApprovalReceiptNewTargetAfterUnadmittedPreparation(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	oldReq := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "prepared-target-a", SystemOverride: "First reviewed instructions"}
	r.approvalAdmit = func(*session.Store, string, string, string) (session.ApprovalReceipt, error) {
		return session.ApprovalReceipt{}, &session.ApprovalReceiptCommitError{Outcome: fileutil.AtomicCommitNotPublished, Err: errors.New("admission not published")}
	}
	partial, err := r.PrepareApprovalOperation(context.Background(), oldReq)
	if err == nil || partial.Lookup.Receipt.Stage != session.ApprovalReceiptPrepared || calls.Load() != 0 {
		t.Fatalf("control did not leave a provably unadmitted preparation: error=%v stage=%s provider=%d", err, partial.Lookup.Receipt.Stage, calls.Load())
	}
	r.approvalAdmit = nil
	if _, _, err := r.store.MutatePlanMode(id, func(plan *session.PlanModeState) error {
		plan.PlanMarkdown = "Separately reviewed scope B"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	newReq := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "new-target-b", SystemOverride: "Different reviewed instructions"}
	if newReq.ApprovalTarget.ExpectedRevision == oldReq.ApprovalTarget.ExpectedRevision {
		t.Fatal("fixture did not change the reviewed target")
	}
	second, err := r.Continue(context.Background(), newReq)
	if err != nil || second.Status != session.StatusCompleted || calls.Load() != 1 {
		t.Fatalf("old prepared target blocked a legitimate new target: status=%s error=%v provider=%d", second.Status, err, calls.Load())
	}
	before, _, _ := receiptBaselineRunFacts(t, r, id)
	old, err := r.Continue(context.Background(), oldReq)
	after, _, _ := receiptBaselineRunFacts(t, r, id)
	if err != nil || old.Approval == nil || !old.Approval.RecoveryRequired || old.Approval.Lookup.Receipt.Stage != session.ApprovalReceiptPrepared || calls.Load() != 1 || !reflect.DeepEqual(before, after) {
		t.Fatalf("old prepared operation executed after another target: result=%#v error=%v provider=%d", old.Approval, err, calls.Load())
	}
}

func TestApprovalReceiptFailedReplayRecoversSameGenerationAndHookDecision(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	r.cfg.Hooks.UserMessage = []config.HookDefinition{{Name: "original-prefix", Command: []string{"/bin/sh", "-c", `rm -f "$1" && mkdir "$1"`, "block-message", filepath.Join(r.cfg.Session.Dir, id, "messages.jsonl")}, Inject: &config.HookInject{Field: "text", Prefix: "[captured once] "}}}
	req := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "recover-message"}
	path := filepath.Join(r.cfg.Session.Dir, id, "messages.jsonl")
	failed, err := r.PrepareApprovalOperation(context.Background(), req)
	if err == nil || failed.Prepared != nil || calls.Load() != 0 || failed.Lookup.Receipt.Stage != session.ApprovalReceiptPrepared {
		t.Fatalf("fixture did not interrupt unadmitted preparation: %#v %v", failed, err)
	}
	generation := failed.Lookup.Receipt.Recovery.RunGeneration
	payload, err := decodeApprovalRecovery(failed.Lookup.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	if payload.ReplayDecision == nil || payload.ReplayDecision.Suppressed || !strings.HasPrefix(payload.ReplayDecision.Message.Text, "[captured once]") {
		t.Fatalf("hook decision not captured before message write: %#v", payload.ReplayDecision)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	r.cfg.Hooks.UserMessage = []config.HookDefinition{{Name: "replacement-prefix", Inject: &config.HookInject{Field: "text", Prefix: "[must not run] "}}}
	recovered, err := r.PrepareApprovalOperation(context.Background(), req)
	if err != nil || recovered.Prepared == nil || recovered.Replay || recovered.RecoveryRequired || recovered.Lookup.Receipt.Stage != session.ApprovalReceiptAdmitted || recovered.Lookup.Receipt.Recovery.RunGeneration != generation {
		t.Fatalf("failed preparation did not recover own generation: prepared=%v replay=%v stage=%s recovery=%v reason=%s error=%v", recovered.Prepared != nil, recovered.Replay, recovered.Lookup.Receipt.Stage, recovered.RecoveryRequired, recovered.RecoveryReason, err)
	}
	if _, err := r.RunPreparedApproval(context.Background(), recovered.Prepared); err != nil {
		t.Fatal(err)
	}
	messages, err := r.store.LoadMessages(id)
	if err != nil {
		t.Fatal(err)
	}
	replayCount := 0
	for _, msg := range messages {
		if msg.Meta["source"] == "planmode_approval" {
			replayCount++
			if msg.ID != payload.ReplayDecision.Message.ID || msg.Text != payload.ReplayDecision.Message.Text {
				t.Fatal("retry rebuilt hook decision or replay identity")
			}
		}
	}
	if replayCount != 1 || calls.Load() != 1 {
		t.Fatalf("retry effects not unique: replay=%d provider=%d", replayCount, calls.Load())
	}
}

func TestApprovalReceiptAbortRemainsAdmittedAndOldApprovalNeverRuns(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	req := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "handle-failure"}
	admitted, err := r.PrepareApprovalOperation(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.AbortPreparedApproval(admitted.Prepared, errors.New("adapter could not add handle")); err != nil {
		t.Fatal(err)
	}
	replay, err := r.Continue(context.Background(), req)
	if err != nil || replay.Approval == nil || !replay.Approval.Replay || replay.Approval.Lookup.Receipt.Stage != session.ApprovalReceiptAdmitted || replay.Approval.Lookup.Receipt.Phase != "aborted" || calls.Load() != 0 {
		t.Fatalf("aborted admission ran again: %#v %v", replay, err)
	}
	ordinary, err := r.Continue(context.Background(), ContinueRequest{SessionID: id, Message: "Explicit ordinary recovery"})
	if err != nil || ordinary.Status != session.StatusCompleted || calls.Load() != 1 {
		t.Fatalf("ordinary explicit recovery unavailable: %#v %v", ordinary, err)
	}
}

func TestApprovalReceiptDeepCapturesOptionsAndTarget(t *testing.T) {
	r, id, _ := newApprovalTargetFixture(t)
	temperature := 0.2
	options := session.ProviderOptions{Temperature: &temperature}
	target := approvalTargetForTest(t, r, id)
	req := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: target, ProviderOptions: options, ApprovalRequestID: "capture-options"}
	prepared, err := r.PrepareApprovalOperation(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	temperature = 0.9
	target.ExpectedRevision = "changed by caller"
	receipt, err := r.ApprovalReceipt(id, "capture-options")
	if err != nil {
		t.Fatal(err)
	}
	if *receipt.Receipt.Parameters.ProviderOptions.Temperature != 0.2 || receipt.Receipt.Target.ExpectedRevision == target.ExpectedRevision || *prepared.Prepared.req.ProviderOptions.Temperature != 0.2 {
		t.Fatal("caller mutation changed captured preparation")
	}
	if err := r.AbortPreparedApproval(prepared.Prepared, nil); err != nil {
		t.Fatal(err)
	}
}

func TestApprovalReceiptLegacyExecutingNeedsOrdinaryRecovery(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	target := approvalTargetForTest(t, r, id)
	if _, err := r.store.ApprovePlanModeTarget(id, session.PlanModeSourceCLI, *target, false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.MarkPlanModeExecuting(id, session.PlanModeSourceCLI); err != nil {
		t.Fatal(err)
	}
	before, _, _ := receiptBaselineRunFacts(t, r, id)
	result, err := r.PrepareApprovalOperation(context.Background(), ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: target, ApprovalRequestID: "unknown-legacy"})
	after, _, _ := receiptBaselineRunFacts(t, r, id)
	if !errors.Is(err, session.ErrApprovalReceiptUnverifiable) || result.Prepared != nil || calls.Load() != 0 || !reflect.DeepEqual(before, after) {
		t.Fatalf("legacy approval manufactured admission: %#v %v", result, err)
	}
	if _, err := r.Continue(context.Background(), ContinueRequest{SessionID: id, Message: "Explicit legacy continuation"}); err != nil || calls.Load() != 1 {
		t.Fatalf("legacy ordinary recovery failed: %v", err)
	}
}

func TestApprovalReceiptUnrelatedOriginsDoNotUnlockLegacyExecution(t *testing.T) {
	for _, origin := range []string{"other_mode", "other_version", "other_revision", "unknown_revision", "rejected"} {
		t.Run(origin, func(t *testing.T) {
			r, id, calls := newApprovalTargetFixture(t)
			old := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "prior-origin"}
			prepared, err := r.PrepareApprovalOperation(context.Background(), old)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.AbortPreparedApproval(prepared.Prepared, nil); err != nil {
				t.Fatal(err)
			}
			if _, _, err := r.store.MutatePlanMode(id, func(plan *session.PlanModeState) error {
				plan.PlanMarkdown = "Unreceipted executing scope"
				if origin == "other_mode" {
					plan.PlanModeID = session.NewPlanModeID()
				}
				if origin == "other_version" {
					plan.PlanVersion++
					plan.ApprovedVersion = plan.PlanVersion
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			target := approvalTargetForTest(t, r, id)
			if _, _, err := r.store.MutatePlanMode(id, func(plan *session.PlanModeState) error {
				plan.ApprovedRevision = target.ExpectedRevision
				if origin == "unknown_revision" {
					plan.ApprovedRevision = ""
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if origin == "rejected" {
				if _, err := r.store.RejectApprovalOperation(id, session.ApprovalOperationRequest{RequestID: "rejected-origin", Parameters: session.ApprovalParameters{Target: *target}}, "coverage rejected"); err != nil {
					t.Fatal(err)
				}
			}
			before, _, _ := receiptBaselineRunFacts(t, r, id)
			_, err = r.PrepareApprovalOperation(context.Background(), ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: target, ApprovalRequestID: "unknown-execution"})
			after, _, _ := receiptBaselineRunFacts(t, r, id)
			if !errors.Is(err, session.ErrApprovalReceiptUnverifiable) || calls.Load() != 0 || !reflect.DeepEqual(before, after) {
				t.Fatalf("%s receipt unlocked unknown execution: error=%v provider=%d", origin, err, calls.Load())
			}
		})
	}
}

func TestApprovalReceiptCommitErrorsNeverStartProvider(t *testing.T) {
	for _, outcome := range []fileutil.AtomicCommitOutcome{fileutil.AtomicCommitNotPublished, fileutil.AtomicCommitPublishedUnconfirmed, fileutil.AtomicCommitCommitted} {
		t.Run(string(outcome), func(t *testing.T) {
			r, id, calls := newApprovalTargetFixture(t)
			req := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "commit-error"}
			r.approvalAdmit = func(store *session.Store, id, operationID, generation string) (session.ApprovalReceipt, error) {
				var receipt session.ApprovalReceipt
				if outcome != fileutil.AtomicCommitNotPublished {
					var err error
					receipt, err = store.AdmitApprovalOperation(id, operationID, generation)
					if err != nil {
						return receipt, err
					}
				}
				return receipt, &session.ApprovalReceiptCommitError{Outcome: outcome, Err: errors.New("reported admission commit failure")}
			}
			result, err := r.Continue(context.Background(), req)
			var commitErr *session.ApprovalReceiptCommitError
			if !errors.As(err, &commitErr) || commitErr.Outcome != outcome || calls.Load() != 0 || result.Approval == nil {
				t.Fatalf("commit error authorized effects: error=%v provider=%d", err, calls.Load())
			}
			r.approvalAdmit = nil
			replay, err := r.Continue(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if outcome == fileutil.AtomicCommitNotPublished {
				if calls.Load() != 1 || replay.Status != session.StatusCompleted {
					t.Fatalf("provably unadmitted operation did not recover: %s provider=%d", replay.Status, calls.Load())
				}
			} else if calls.Load() != 0 || !replay.Approval.Replay || replay.Approval.Lookup.Receipt.Stage != session.ApprovalReceiptAdmitted {
				t.Fatal("published admission was executed after error")
			}
			if outcome != fileutil.AtomicCommitNotPublished {
				ordinary, err := r.Continue(context.Background(), ContinueRequest{SessionID: id, Message: "Explicit recovery after failed admission response"})
				if err != nil || ordinary.Status != session.StatusCompleted || calls.Load() != 1 {
					t.Fatalf("admission error stranded ordinary recovery: status=%s error=%v provider=%d", ordinary.Status, err, calls.Load())
				}
				if _, err := r.Continue(context.Background(), req); err != nil || calls.Load() != 1 {
					t.Fatal("old approval executed after ordinary recovery")
				}
			}
		})
	}
}

func TestApprovalReceiptCommitErrorDoesNotReleasePeerGeneration(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	req := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "peer-claim-control"}
	var peer session.State
	r.approvalAdmit = func(store *session.Store, id, operationID, generation string) (session.ApprovalReceipt, error) {
		receipt, err := store.AdmitApprovalOperation(id, operationID, generation)
		if err != nil {
			return receipt, err
		}
		current, err := store.LoadState(id)
		if err != nil {
			return receipt, err
		}
		paused := current
		paused.Status = session.StatusAwaitingInput
		paused.Phase = "manual_stop"
		if _, saved, err := store.SwapStateIfCurrent(id, current, paused); err != nil || !saved {
			return receipt, errors.New("fixture could not release original claim")
		}
		peer, err = store.ClaimSessionRun(id, session.StatusAwaitingInput)
		if err != nil {
			return receipt, err
		}
		return receipt, &session.ApprovalReceiptCommitError{Outcome: fileutil.AtomicCommitCommitted, Err: errors.New("reported failure after newer ordinary claim")}
	}
	_, err := r.Continue(context.Background(), req)
	var commitErr *session.ApprovalReceiptCommitError
	if !errors.As(err, &commitErr) || !errors.Is(err, session.ErrApprovalConflict) || calls.Load() != 0 || peer.RunGeneration == "" {
		t.Fatalf("fixture did not return committed error across peer claim: %v provider=%d", err, calls.Load())
	}
	after, _, _ := receiptBaselineRunFacts(t, r, id)
	if !reflect.DeepEqual(peer, after) {
		t.Fatal("old admission error overwrote peer generation")
	}
	r.approvalAdmit = nil
	replay, err := r.Continue(context.Background(), req)
	if err != nil || !replay.Approval.Replay || replay.Approval.Lookup.Receipt.Stage != session.ApprovalReceiptAdmitted || calls.Load() != 0 {
		t.Fatalf("committed operation executed after peer claim: %v provider=%d", err, calls.Load())
	}
}

func TestApprovalReceiptPreparedAliasesKeepOneGeneration(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	req := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "prepared-canonical"}
	r.approvalAdmit = func(*session.Store, string, string, string) (session.ApprovalReceipt, error) {
		return session.ApprovalReceipt{}, &session.ApprovalReceiptCommitError{Outcome: fileutil.AtomicCommitNotPublished, Err: errors.New("admission not published")}
	}
	partial, err := r.PrepareApprovalOperation(context.Background(), req)
	if err == nil || partial.Lookup.Receipt.Stage != session.ApprovalReceiptPrepared {
		t.Fatalf("fixture did not leave a prepared operation: %v", err)
	}
	before, _, _ := receiptBaselineRunFacts(t, r, id)
	different := req
	different.ApprovalRequestID = "conflicting-pending"
	different.SystemOverride = "Different explicit system input"
	if _, err := r.PrepareApprovalOperation(context.Background(), different); !errors.Is(err, session.ErrApprovalRequestConflict) {
		t.Fatalf("different pending parameters did not conflict: %v", err)
	}
	after, _, _ := receiptBaselineRunFacts(t, r, id)
	if calls.Load() != 0 || !reflect.DeepEqual(before, after) {
		t.Fatal("pending conflict changed execution facts")
	}
	r.approvalAdmit = nil
	req.ApprovalRequestID = "prepared-alias"
	result, err := r.Continue(context.Background(), req)
	if err != nil || result.Status != session.StatusCompleted || calls.Load() != 1 || result.Approval.Lookup.Receipt.OperationID != "prepared-canonical" || result.Approval.Lookup.Binding.RequestID != "prepared-alias" || result.Approval.Lookup.Receipt.Recovery.RunGeneration != partial.Lookup.Receipt.Recovery.RunGeneration {
		t.Fatalf("prepared alias did not resume its canonical generation: error=%v provider=%d", err, calls.Load())
	}
	req.ApprovalRequestID = "prepared-canonical"
	if result, err := r.Continue(context.Background(), req); err != nil || !result.Approval.Replay || calls.Load() != 1 {
		t.Fatalf("canonical retry executed after alias admission: error=%v provider=%d", err, calls.Load())
	}
}

func TestApprovalReceiptSuppressedHookDecisionIsDurable(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	r.cfg.Hooks.UserMessage = []config.HookDefinition{{Name: "suppress", Inject: &config.HookInject{Field: "text", Set: " "}}}
	req := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "suppressed"}
	prepared, err := r.PrepareApprovalOperation(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := decodeApprovalRecovery(prepared.Lookup.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	if payload.ReplayDecision == nil || !payload.ReplayDecision.Suppressed || len(payload.Messages) != 0 {
		t.Fatal("suppressed replay was inferred from absence instead of captured")
	}
	if _, err := r.RunPreparedApproval(context.Background(), prepared.Prepared); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Continue(context.Background(), req); err != nil || calls.Load() != 1 {
		t.Fatalf("suppressed message caused readmission: %v provider=%d", err, calls.Load())
	}
}

func TestApprovalReceiptCapturesNewCompensationWithoutOldToolHistory(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	oldAssistant := session.NewMessage("assistant", "")
	oldAssistant.ToolCalls = []session.ToolCall{{ID: "old-call", Name: "read_file", Arguments: json.RawMessage(`{}`)}}
	oldTool := session.NewMessage("tool", "")
	oldTool.ToolResults = []session.ToolResult{{ToolCallID: "old-call", Name: "read_file", LLMOutput: strings.Repeat("old output", 300)}}
	pending := session.NewMessage("assistant", "")
	pending.ToolCalls = []session.ToolCall{{ID: "pending-call", Name: "read_file", Arguments: json.RawMessage(`{}`)}}
	for _, msg := range []session.Message{oldAssistant, oldTool, pending} {
		if err := r.store.AppendMessage(id, msg); err != nil {
			t.Fatal(err)
		}
	}
	req := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "new-compensation"}
	r.approvalAdmit = func(*session.Store, string, string, string) (session.ApprovalReceipt, error) {
		return session.ApprovalReceipt{}, &session.ApprovalReceiptCommitError{Outcome: fileutil.AtomicCommitNotPublished, Err: errors.New("admit not published")}
	}
	partial, err := r.PrepareApprovalOperation(context.Background(), req)
	if err == nil {
		t.Fatal("fixture did not stop before admit")
	}
	payload, err := decodeApprovalRecovery(partial.Lookup.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	compensationID := ""
	for _, msg := range payload.Messages {
		if msg.ID == oldTool.ID {
			t.Fatal("receipt copied unrelated old tool history")
		}
		for _, result := range msg.ToolResults {
			if result.ToolCallID == "pending-call" {
				compensationID = msg.ID
			}
		}
	}
	if compensationID == "" {
		t.Fatal("receipt omitted this preparation's dangling-tool compensation")
	}
	// Lose only this operation's non-fsynced JSONL tail. Its durable prepared
	// payload must restore the captured compensation and replay with original IDs.
	var original bytes.Buffer
	for _, msg := range []session.Message{oldAssistant, oldTool, pending} {
		data, err := json.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		original.Write(data)
		original.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(r.cfg.Session.Dir, id, "messages.jsonl"), original.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	r.approvalAdmit = nil
	recovered, err := r.PrepareApprovalOperation(context.Background(), req)
	if err != nil || recovered.Prepared == nil {
		t.Fatalf("complete prepared payload failed recovery: prepared=%v reason=%s error=%v", recovered.Prepared != nil, recovered.RecoveryReason, err)
	}
	msgs, err := r.store.LoadMessages(id)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, msg := range msgs {
		found = found || msg.ID == compensationID
	}
	if !found || calls.Load() != 0 {
		t.Fatal("recovery did not restore captured compensation before provider")
	}
	if err := r.AbortPreparedApproval(recovered.Prepared, nil); err != nil {
		t.Fatal(err)
	}
}

func TestApprovalReceiptDeadPreparedOwnerIsReboundBeforeAdmission(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	req := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "dead-prepared-owner"}
	r.approvalAdmit = func(*session.Store, string, string, string) (session.ApprovalReceipt, error) {
		return session.ApprovalReceipt{}, errors.New("stopped before admit")
	}
	partial, err := r.PrepareApprovalOperation(context.Background(), req)
	if err == nil {
		t.Fatal("fixture did not stop")
	}
	payload, err := decodeApprovalRecovery(partial.Lookup.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	payload.Preparation.OwnerPID = 999999999
	payload.Preparation.OwnerIdentity = ""
	recovery := partial.Lookup.Receipt.Recovery
	recovery.OwnerPID = payload.Preparation.OwnerPID
	recovery.OwnerIdentity = ""
	recovery.Data, err = json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.CheckpointApprovalOperation(id, req.ApprovalRequestID, recovery.RunGeneration, partial.Lookup.Receipt.Phase, recovery); err != nil {
		t.Fatal(err)
	}
	r.approvalAdmit = nil
	admitted, err := r.PrepareApprovalOperation(context.Background(), req)
	if err != nil || admitted.Prepared == nil {
		t.Fatalf("dead-owner prepared recovery: reason=%s error=%v", admitted.RecoveryReason, err)
	}
	if alive, err := ApprovalPreparationOwnerAlive(r.store, id); err != nil || !alive {
		t.Fatalf("new preparing owner unprotected before handle: alive=%v error=%v", alive, err)
	}
	if allowed, err := CanReconcileApprovalPreparation(r.store, id); err != nil || allowed || calls.Load() != 0 {
		t.Fatalf("reaper reclaimed new admission: allowed=%v error=%v", allowed, err)
	}
	if err := r.AbortPreparedApproval(admitted.Prepared, nil); err != nil {
		t.Fatal(err)
	}
}

func TestApprovalReceiptRecoverySnapshotsMustMatchRevision(t *testing.T) {
	r, id, calls := newApprovalTargetFixture(t)
	req := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "snapshot-integrity"}
	if _, err := r.Continue(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.cfg.Session.Dir, id, "approval-operations.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ledger map[string]any
	if err := json.Unmarshal(raw, &ledger); err != nil {
		t.Fatal(err)
	}
	data := ledger["operations"].(map[string]any)["snapshot-integrity"].(map[string]any)["recovery"].(map[string]any)["data"].(map[string]any)
	data["preparation"].(map[string]any)["snapshot"].(map[string]any)["plan_mode"].(map[string]any)["plan_markdown"] = "Scope B never reviewed under this revision"
	data["approved_plan"].(map[string]any)["plan_markdown"] = "Scope B never reviewed under this revision"
	raw, err = json.Marshal(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.GetApprovalReceipt(id, req.ApprovalRequestID); err != nil {
		t.Fatalf("fixture broke Store envelope: %v", err)
	}
	_, err = r.LookupApprovalContinue(req)
	if !errors.Is(err, session.ErrApprovalReceiptUnverifiable) || calls.Load() != 1 {
		t.Fatalf("recovery snapshots contradicted approved semantic revision: error=%v provider=%d", err, calls.Load())
	}
}

func TestApprovalReceiptRecoveryStateCorruptionBlocksUnrelatedNewAdmission(t *testing.T) {
	for _, damage := range []string{"different_prepared_generation", "empty_prepared_generation", "invalid_original_status", "running_original_status", "cancelled_original_status", "invalid_prepared_status", "negative_original_turn", "negative_prepared_turn", "null_hook_pending", "null_prepared_state"} {
		t.Run(damage, func(t *testing.T) {
			r, id, calls := newApprovalTargetFixture(t)
			old := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "old-state-receipt"}
			if _, err := r.Continue(context.Background(), old); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(r.cfg.Session.Dir, id, "approval-operations.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var ledger map[string]any
			if err := json.Unmarshal(raw, &ledger); err != nil {
				t.Fatal(err)
			}
			payload := ledger["operations"].(map[string]any)[old.ApprovalRequestID].(map[string]any)["recovery"].(map[string]any)["data"].(map[string]any)
			preparation := payload["preparation"].(map[string]any)
			original, prepared := preparation["original_state"].(map[string]any), preparation["prepared_state"].(map[string]any)
			switch damage {
			case "different_prepared_generation":
				prepared["run_generation"] = "run_other"
			case "empty_prepared_generation":
				delete(prepared, "run_generation")
			case "invalid_original_status":
				original["status"] = "INVALID"
			case "running_original_status":
				original["status"] = session.StatusRunning
			case "cancelled_original_status":
				original["status"] = session.StatusCancelled
			case "invalid_prepared_status":
				prepared["status"] = "INVALID"
			case "negative_original_turn":
				original["turn"] = -1
			case "negative_prepared_turn":
				prepared["turn"] = -1
			case "null_hook_pending":
				payload["hook_pending"] = nil
			case "null_prepared_state":
				preparation["prepared_state"] = nil
			}
			raw, err = json.Marshal(ledger)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := r.store.MutatePlanMode(id, func(plan *session.PlanModeState) error {
				plan.PlanVersion++
				plan.Status = session.PlanModeStatusAwaitingApproval
				plan.PlanMarkdown = "New reviewed target"
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			before, beforeResumed, beforeReplay := receiptBaselineRunFacts(t, r, id)
			next := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "new-state-receipt"}
			_, err = r.Continue(context.Background(), next)
			after, afterResumed, afterReplay := receiptBaselineRunFacts(t, r, id)
			t.Logf("damage=%s error=%v provider_calls=%d", damage, err, calls.Load())
			if !errors.Is(err, session.ErrApprovalReceiptUnverifiable) || calls.Load() != 1 || !reflect.DeepEqual(before, after) || beforeResumed != afterResumed || beforeReplay != afterReplay {
				t.Fatalf("invalid unrelated recovery did not fail before admission: error=%v provider_calls=%d state_changed=%v", err, calls.Load(), !reflect.DeepEqual(before, after))
			}
		})
	}
}

func TestApprovalReceiptRecoveryGenerationPhaseControls(t *testing.T) {
	r, id, _ := newApprovalTargetFixture(t)
	admitted, err := r.PrepareApprovalOperation(context.Background(), ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "phase-controls"})
	if err != nil {
		t.Fatal(err)
	}
	defer r.AbortPreparedApproval(admitted.Prepared, nil)
	for _, tc := range []struct {
		name, phase, completed, state string
		complete, claimed             bool
	}{
		{"validated original", "validated", "validated", "original", false, false},
		{"claim pending original", "claim_pending", "claim_pending", "original", false, true},
		{"claim pending resumed own claim", "claim_pending", "claim_pending", "claim", false, true},
		{"failed validated original", "prepare_failed", "validated", "original", false, false},
		{"failed claim restored original", "prepare_failed", "claim_pending", "original", false, true},
		{"failed claim pending own claim", "prepare_failed", "claim_pending", "failed", false, true},
		{"failed own claim", "prepare_failed", "run_claimed", "failed", false, true},
		{"prepared own claim", "prepared", "prepared", "claim", true, true},
		{"executing own claim", "executing", "executing", "claim", true, true},
		{"settled own generation", "settled", "settled", "completed", true, true},
		{"abort restored original", "aborted", "aborted", "original", true, true},
		{"abort restored original refreshed observations", "aborted", "aborted", "original_observed", true, true},
		{"uncertain admission aborted own failed claim", "aborted", "aborted", "failed", true, true},
		{"review required own claim", "review_required", "review_required", "awaiting", true, true},
		{"recovered original", "recovered", "recovered", "original", true, true},
		{"recovered own generation", "recovered", "recovered", "awaiting", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receipt := admitted.Lookup.Receipt
			payload, err := decodeApprovalRecovery(receipt)
			if err != nil {
				t.Fatal(err)
			}
			receipt.Phase = tc.phase
			if !tc.complete {
				receipt.Stage = session.ApprovalReceiptPrepared
			}
			if !tc.claimed {
				receipt.Recovery.RunGeneration = ""
			}
			payload.Complete = tc.complete
			payload.Preparation.Phase, payload.Preparation.CompletedPhase = tc.phase, tc.completed
			switch tc.state {
			case "original":
				payload.Preparation.PreparedState = payload.Preparation.OriginalState
			case "original_observed":
				payload.Preparation.PreparedState = payload.Preparation.OriginalState
				payload.Preparation.PreparedState.UpdatedAt = payload.Preparation.ClaimUpdatedAt
				payload.Preparation.PreparedState.PendingSteerCount = 2
				payload.Preparation.PreparedState.LoadedSkills = []string{"loaded-during-preparation"}
			case "failed":
				payload.Preparation.PreparedState.Status = session.StatusFailed
				payload.Preparation.PreparedState.LastError = "captured preparation failure"
				payload.Preparation.LastError = "captured preparation failure"
			case "completed":
				payload.Preparation.PreparedState.Status = session.StatusCompleted
			case "awaiting":
				payload.Preparation.PreparedState.Status = session.StatusAwaitingInput
				if tc.phase == "review_required" {
					payload.Preparation.PreparedState.Phase = "plan_approval"
					payload.Preparation.PreparedState.IdleReason = "approval_content_changed"
					payload.Preparation.PreparedState.LastError = "captured scope change"
					payload.Preparation.LastError = "captured scope change"
				}
			}
			receipt.Recovery.Data, err = json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeApprovalRecovery(receipt); err != nil {
				t.Fatalf("legitimate captured phase rejected: %v", err)
			}
		})
	}
}

func TestApprovalReceiptLinkedAndReplayFactsAreSelfContained(t *testing.T) {
	for _, damage := range []string{"mission_history", "mission_event", "replay_identity", "required_field", "unknown_field", "contradictory_phase"} {
		t.Run(damage, func(t *testing.T) {
			r, id, calls := newApprovalTargetFixture(t)
			linkApprovalMissionForTest(t, r, id)
			req := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "linked-facts"}
			admitted, err := r.PrepareApprovalOperation(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			defer admitted.Prepared.releaseRunSlot()
			payload, err := decodeApprovalRecovery(admitted.Lookup.Receipt)
			if err != nil {
				t.Fatal(err)
			}
			if payload.ApprovedGoal == nil || len(payload.GoalHistory) == 0 || payload.ReplayDecision == nil {
				t.Fatal("legitimate linked control facts missing")
			}
			path := filepath.Join(r.cfg.Session.Dir, id, "approval-operations.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var ledger map[string]any
			if err := json.Unmarshal(raw, &ledger); err != nil {
				t.Fatal(err)
			}
			data := ledger["operations"].(map[string]any)["linked-facts"].(map[string]any)["recovery"].(map[string]any)["data"].(map[string]any)
			switch damage {
			case "mission_history":
				data["goal_history"] = []any{}
			case "mission_event":
				var kept []any
				for _, evt := range data["events"].([]any) {
					if evt.(map[string]any)["type"] != "mission.plan.approved" {
						kept = append(kept, evt)
					}
				}
				data["events"] = kept
			case "replay_identity":
				data["replay_decision"].(map[string]any)["message"].(map[string]any)["id"] = "replacement-message"
			case "required_field":
				delete(data, "replay_decision")
			case "unknown_field":
				data["invented_recovery_authority"] = true
			case "contradictory_phase":
				data["preparation"].(map[string]any)["phase"] = "settled"
			}
			raw, err = json.Marshal(ledger)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := r.store.GetApprovalReceipt(id, req.ApprovalRequestID); err != nil {
				t.Fatalf("damage invalidated Store envelope instead of runtime facts: %v", err)
			}
			if _, err := r.ApprovalReceipt(id, req.ApprovalRequestID); !errors.Is(err, session.ErrApprovalReceiptUnverifiable) || calls.Load() != 0 {
				t.Fatalf("typed recovery accepted %s: %v provider=%d", damage, err, calls.Load())
			}
		})
	}
}

func TestApprovalReceiptPreparedPhaseStateConsistency(t *testing.T) {
	for _, variation := range []string{"control", "completed_status", "advanced_phase", "advanced_turn"} {
		t.Run(variation, func(t *testing.T) {
			r, id, calls := newApprovalTargetFixture(t)
			original := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "old-local-operation"}
			boundaryError := errors.New("fixed fixture admission before publication")
			r.approvalAdmit = func(_ *session.Store, _, _, _ string) (session.ApprovalReceipt, error) {
				return session.ApprovalReceipt{}, boundaryError
			}
			prior, err := r.PrepareApprovalOperation(context.Background(), original)
			if !errors.Is(err, boundaryError) || prior.Prepared != nil || calls.Load() != 0 {
				t.Fatalf("fixture boundary: error=%v prepared=%v calls=%d", err, prior.Prepared != nil, calls.Load())
			}
			payload, err := decodeApprovalRecovery(prior.Lookup.Receipt)
			if err != nil {
				t.Fatal(err)
			}
			if prior.Lookup.Receipt.Stage != session.ApprovalReceiptPrepared || prior.Lookup.Receipt.Phase != "prepare_failed" || payload.Preparation.CompletedPhase != "prepared" {
				t.Fatalf("unexpected source phase: stage=%s phase=%s completed=%s", prior.Lookup.Receipt.Stage, prior.Lookup.Receipt.Phase, payload.Preparation.CompletedPhase)
			}
			t.Logf("source stage=%s phase=%s completed=%s prepared_status=%s prepared_phase=%s prepared_turn=%d", prior.Lookup.Receipt.Stage, prior.Lookup.Receipt.Phase, payload.Preparation.CompletedPhase, payload.Preparation.PreparedState.Status, payload.Preparation.PreparedState.Phase, payload.Preparation.PreparedState.Turn)
			path := filepath.Join(r.cfg.Session.Dir, id, "approval-operations.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var ledger map[string]any
			if err := json.Unmarshal(raw, &ledger); err != nil {
				t.Fatal(err)
			}
			state := ledger["operations"].(map[string]any)[original.ApprovalRequestID].(map[string]any)["recovery"].(map[string]any)["data"].(map[string]any)["preparation"].(map[string]any)["prepared_state"].(map[string]any)
			switch variation {
			case "completed_status":
				state["status"] = session.StatusCompleted
			case "advanced_phase":
				state["phase"] = "provider_execution"
			case "advanced_turn":
				state["turn"] = state["turn"].(float64) + 1
			}
			raw, err = json.Marshal(ledger)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := r.store.MutatePlanMode(id, func(plan *session.PlanModeState) error {
				plan.PlanVersion++
				plan.Status = session.PlanModeStatusAwaitingApproval
				plan.PlanMarkdown = "Another reviewed target"
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			r.approvalAdmit = nil
			next := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "new-local-operation"}
			_, err = r.Continue(context.Background(), next)
			t.Logf("variation=%s error=%v provider_calls=%d", variation, err, calls.Load())
			if variation == "control" {
				if err != nil || calls.Load() != 1 {
					t.Fatalf("lawful control: error=%v calls=%d", err, calls.Load())
				}
			} else if !errors.Is(err, session.ErrApprovalReceiptUnverifiable) || calls.Load() != 0 {
				t.Fatalf("phase-inconsistent historical state accepted: error=%v calls=%d", err, calls.Load())
			}
		})
	}
}

func TestApprovalReceiptReviewRequiredStateConsistency(t *testing.T) {
	for _, damage := range []string{"control", "provider_resume_count", "max_tokens_resume_count", "ralph_loop_count", "compaction_chars", "current_task", "assistant_excerpt", "pause_reason", "incomplete_reason"} {
		t.Run(damage, func(t *testing.T) {
			r, id, calls := newApprovalTargetFixture(t)
			old := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "old-review-required"}
			prepared, err := r.PrepareApprovalOperation(context.Background(), old)
			if err != nil || prepared.Prepared == nil {
				t.Fatalf("prepare real operation: %v", err)
			}
			if _, err := r.Steer(context.Background(), SteerRequest{SessionID: id, Message: "Preserve this legal queued observation"}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := r.store.MutatePlanMode(id, func(plan *session.PlanModeState) error {
				plan.PlanVersion++
				plan.Status = session.PlanModeStatusAwaitingApproval
				plan.PlanMarkdown = "New reviewed target after actual scope change"
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := r.RunPreparedApproval(context.Background(), prepared.Prepared); !errors.Is(err, session.ErrApprovalConflict) || calls.Load() != 0 {
				t.Fatalf("actual scope-change boundary: %v calls=%d", err, calls.Load())
			}
			prior, err := r.ApprovalReceipt(id, old.ApprovalRequestID)
			if err != nil || prior.Receipt.Phase != "review_required" {
				t.Fatalf("actual source receipt: %v %#v", err, prior.Receipt)
			}
			payload, err := decodeApprovalRecovery(prior.Receipt)
			if err != nil || payload.Preparation.PreparedState.PendingSteerCount != 1 {
				t.Fatalf("legal queued observation was not captured: %v %#v", err, payload.Preparation.PreparedState)
			}
			path := filepath.Join(r.cfg.Session.Dir, id, "approval-operations.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var ledger map[string]any
			if err := json.Unmarshal(raw, &ledger); err != nil {
				t.Fatal(err)
			}
			captured := ledger["operations"].(map[string]any)[old.ApprovalRequestID].(map[string]any)["recovery"].(map[string]any)["data"].(map[string]any)["preparation"].(map[string]any)["prepared_state"].(map[string]any)
			switch damage {
			case "provider_resume_count":
				captured["provider_auto_resume_count"] = 1
			case "max_tokens_resume_count":
				captured["provider_max_tokens_resume_count"] = 1
			case "ralph_loop_count":
				captured["ralph_loop_count"] = 1
			case "compaction_chars":
				captured["last_compaction_input_chars"] = 1
			case "current_task":
				captured["current_task"] = "invented task"
			case "assistant_excerpt":
				captured["last_assistant_excerpt"] = "invented excerpt"
			case "pause_reason":
				captured["pause_reason"] = "invented pause"
			case "incomplete_reason":
				captured["incomplete_reason"] = "invented incompleteness"
			}
			raw, err = json.Marshal(ledger)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			next := ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "new-review-required"}
			before, beforeResumed, beforeReplay := receiptBaselineRunFacts(t, r, id)
			_, queryErr := r.ApprovalReceipt(id, old.ApprovalRequestID)
			_, lookupErr := r.LookupApprovalContinue(next)
			_, err = r.Continue(context.Background(), next)
			after, afterResumed, afterReplay := receiptBaselineRunFacts(t, r, id)
			t.Logf("damage=%s source=actual_review_required queued=1 query=%v lookup=%v execution=%v provider_calls=%d", damage, queryErr, lookupErr, err, calls.Load())
			if damage == "control" {
				if queryErr != nil || lookupErr != nil || err != nil || calls.Load() != 1 {
					t.Fatalf("lawful control: query=%v lookup=%v execute=%v calls=%d", queryErr, lookupErr, err, calls.Load())
				}
				return
			}
			if !errors.Is(queryErr, session.ErrApprovalReceiptUnverifiable) || !errors.Is(lookupErr, session.ErrApprovalReceiptUnverifiable) || !errors.Is(err, session.ErrApprovalReceiptUnverifiable) || calls.Load() != 0 || !reflect.DeepEqual(before, after) || beforeResumed != afterResumed || beforeReplay != afterReplay {
				t.Fatalf("corrupt review-required state admitted: query=%v lookup=%v execute=%v calls=%d", queryErr, lookupErr, err, calls.Load())
			}
			if afterRaw, err := os.ReadFile(path); err != nil || !bytes.Equal(raw, afterRaw) {
				t.Fatalf("failed validation changed ledger: %v", err)
			}
		})
	}
}

func TestApprovalReceiptResumableOriginalStatusControls(t *testing.T) {
	for _, status := range []string{session.StatusPaused, session.StatusAwaitingInput, session.StatusFailed, session.StatusCompleted} {
		t.Run(status, func(t *testing.T) {
			r, id, calls := newApprovalTargetFixture(t)
			state, err := r.store.LoadState(id)
			if err != nil {
				t.Fatal(err)
			}
			state.Status = status
			if err := r.store.SaveState(id, state); err != nil {
				t.Fatal(err)
			}
			result, err := r.Continue(context.Background(), ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: approvalTargetForTest(t, r, id), ApprovalRequestID: "resumable-original"})
			if err != nil || result.Approval == nil || calls.Load() != 1 {
				t.Fatalf("legitimate original status blocked: status=%s error=%v calls=%d", status, err, calls.Load())
			}
			payload, err := decodeApprovalRecovery(result.Approval.Lookup.Receipt)
			if err != nil || payload.Preparation.OriginalState.Status != status {
				t.Fatalf("captured original status changed: status=%s error=%v", status, err)
			}
		})
	}
}
