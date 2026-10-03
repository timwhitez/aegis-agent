package webconsole

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aegis-agent/internal/runtime"
	"aegis-agent/internal/session"
	sdk "aegis-agent/pkg/agent"
)

func webReceiptFixture(t *testing.T) (*Service, string, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"receipt_mock","status":"completed","output":[{"type":"function_call","call_id":"finish_receipt","name":"finish","arguments":"{\"message\":\"done\"}"}],"usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	svc, id := newApprovalTargetFixtureWithProvider(t, server)
	return svc, id, calls
}

func captureExecutingRepairFacts(t *testing.T, store *session.Store, id string) map[string][]byte {
	t.Helper()
	facts := map[string][]byte{}
	for _, name := range []string{"goal.json", "artifacts/goal-history.jsonl", "planmode.json", "artifacts/planmode-history.jsonl", "messages.jsonl", "state.json", "events.jsonl", "approval-operations.json"} {
		raw, err := os.ReadFile(filepath.Join(store.Root(), id, name))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		facts[name] = raw
	}
	return facts
}

func receiptBody(t *testing.T, target session.ApprovalTarget, requestID string, override bool) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"plan_mode_id": target.PlanModeID, "plan_version": target.PlanVersion, "expected_revision": target.ExpectedRevision, "approval_request_id": requestID, "override_coverage": override})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func receiptGet(svc *Service, id, requestID string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/sessions/"+id+"/approval-receipts/"+requestID, nil)
	r.Host = "127.0.0.1"
	svc.ServeHTTP(w, r)
	return w
}

func TestWebApprovalReceiptReplayBeforeCurrentState(t *testing.T) {
	svc, id, calls := webReceiptFixture(t)
	target := reviewedApprovalPayload(t, svc.store, id, false).ApprovalTarget
	runner := runtime.NewCoreRunner(svc.cfg)
	control, err := runner.Continue(context.Background(), runtime.ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: &target, ApprovalRequestID: "web-original"})
	if err != nil || control.Status != session.StatusCompleted || calls.Load() != 1 {
		t.Fatalf("local runtime admission control: %#v %v calls=%d", control, err, calls.Load())
	}
	before, _ := svc.store.LoadState(id)
	beforeEvents, _ := svc.store.LoadEvents(id)
	beforeMessages, _ := svc.store.LoadMessages(id)
	// The receipt must survive changed content and defaults; these are current facts.
	if _, _, err := svc.store.MutatePlanMode(id, func(plan *session.PlanModeState) error { plan.Objective = "new unreviewed scope"; return nil }); err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	delete(svc.cfg.Providers, svc.cfg.DefaultProvider)
	svc.mu.Unlock()
	for _, requestID := range []string{"web-original", "web-alias"} {
		w := approvalTargetPost(svc, id, receiptBody(t, target, requestID, false))
		t.Logf("request=%s status=%d provider_calls=%d body=%s", requestID, w.Code, calls.Load(), w.Body.String())
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"admitted"`) || !strings.Contains(w.Body.String(), `"replay":true`) {
			t.Errorf("old receipt blocked by current facts: %d %s", w.Code, w.Body)
		}
	}
	after, _ := svc.store.LoadState(id)
	afterEvents, _ := svc.store.LoadEvents(id)
	afterMessages, _ := svc.store.LoadMessages(id)
	if calls.Load() != 1 || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeEvents, afterEvents) || !reflect.DeepEqual(beforeMessages, afterMessages) || svc.hasActiveHandle(id) {
		t.Fatal("receipt replay changed execution facts")
	}
}

func TestWebApprovalReceiptMissingIDRequiresUpgrade(t *testing.T) {
	svc, id, calls := webReceiptFixture(t)
	target := reviewedApprovalPayload(t, svc.store, id, false).ApprovalTarget
	raw, _ := json.Marshal(target)
	w := approvalTargetPost(svc, id, string(raw))
	t.Logf("missing ID status=%d provider_calls=%d body=%s", w.Code, calls.Load(), w.Body.String())
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"APPROVAL_REQUEST_ID_REQUIRED"`) || !strings.Contains(w.Body.String(), "upgrade") || calls.Load() != 0 {
		t.Fatalf("missing operation identity: %d %s calls=%d", w.Code, w.Body, calls.Load())
	}
}

func webReceiptPost(svc *Service, id, entry, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/sessions/"+id+"/"+entry, strings.NewReader(body))
	r.Host = "127.0.0.1"
	r.Header.Set("X-Aegis-Agent-Web", "1")
	r.Header.Set("Content-Type", "application/json")
	svc.ServeHTTP(w, r)
	return w
}

func linkWebReceiptMission(t *testing.T, svc *Service, id string, features ...string) session.ApprovalTarget {
	t.Helper()
	goal, err := svc.store.CreateGoal(id, session.GoalDraft{Enabled: true, Mode: session.GoalModeMission, Objective: "Reviewed linked mission", Features: features})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.store.MutatePlanMode(id, func(plan *session.PlanModeState) error { plan.LinkedGoalID = goal.GoalID; return nil }); err != nil {
		t.Fatal(err)
	}
	return reviewedApprovalPayload(t, svc.store, id, false).ApprovalTarget
}

func TestWebApprovalReceiptTwoServicePreflightRace(t *testing.T) {
	for _, test := range []struct {
		first, peer string
		alias       bool
	}{
		{first: "planmode/approve", peer: "mission/plan/approve"},
		{first: "mission/plan/approve", peer: "planmode/approve"},
		{first: "planmode/approve", peer: "mission/plan/approve", alias: true},
		{first: "mission/plan/approve", peer: "planmode/approve", alias: true},
	} {
		t.Run(test.first+"_"+test.peer+"_alias="+fmt.Sprint(test.alias), func(t *testing.T) {
			var calls atomic.Int32
			release := make(chan struct{})
			entered := make(chan struct{}, 1)
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				entered <- struct{}{}
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"race_mock","status":"completed","output":[{"type":"function_call","call_id":"race_finish","name":"finish","arguments":"{\"message\":\"done\"}"}]}`))
			}))
			svc, id := newApprovalTargetFixtureWithProvider(t, server)
			t.Cleanup(unblock)
			target := linkWebReceiptMission(t, svc, id)
			peer, err := New(svc.cfg, Options{WorkerCount: 0})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(peer.Close)
			lookupMiss, continuePreflight := make(chan struct{}), make(chan struct{})
			svc.beforeApprovalPreflight = func(string) { close(lookupMiss); <-continuePreflight }
			firstID := "shared-operation"
			if test.alias {
				firstID = "alias-operation"
			}
			firstDone := make(chan *httptest.ResponseRecorder, 1)
			go func() { firstDone <- webReceiptPost(svc, id, test.first, receiptBody(t, target, firstID, false)) }()
			select {
			case <-lookupMiss:
			case <-time.After(2 * time.Second):
				t.Fatal("first service did not reach real lookup miss")
			}
			peerResponse := webReceiptPost(peer, id, test.peer, receiptBody(t, target, "shared-operation", false))
			if peerResponse.Code != http.StatusAccepted {
				close(continuePreflight)
				t.Fatalf("peer new admission: %d %s", peerResponse.Code, peerResponse.Body)
			}
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				close(continuePreflight)
				t.Fatal("admitted peer did not reach local provider")
			}
			before, _ := svc.store.LoadState(id)
			beforeEvents, _ := svc.store.LoadEvents(id)
			beforeMessages, _ := svc.store.LoadMessages(id)
			close(continuePreflight)
			var response *httptest.ResponseRecorder
			select {
			case response = <-firstDone:
			case <-time.After(2 * time.Second):
				t.Fatal("receipt replay deadlocked behind current run")
			}
			var resp ApprovalResponse
			if err := json.Unmarshal(response.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			after, _ := svc.store.LoadState(id)
			afterEvents, _ := svc.store.LoadEvents(id)
			afterMessages, _ := svc.store.LoadMessages(id)
			if response.Code != http.StatusOK || resp.Approval == nil || !resp.Approval.Replay || resp.Approval.Lookup.Receipt.Stage != session.ApprovalReceiptAdmitted || resp.Approval.Lookup.Binding.RequestID != firstID {
				t.Fatalf("peer claim blocked receipt replay: %d %s", response.Code, response.Body)
			}
			t.Logf("peer=%d delayed=%d binding=%s canonical=%s replay=%v provider_calls=%d generation=%s", peerResponse.Code, response.Code, resp.Approval.Lookup.Binding.RequestID, resp.Approval.Lookup.Receipt.OperationID, resp.Approval.Replay, calls.Load(), after.RunGeneration)
			if calls.Load() != 1 || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeEvents, afterEvents) || !reflect.DeepEqual(beforeMessages, afterMessages) || svc.hasActiveHandle(id) || !peer.hasActiveHandle(id) {
				t.Fatal("delayed service created another run/handle/replay fact")
			}
			// A new alias may record its own requested override, without changing
			// the canonical parameters that were admitted by the peer.
			alias := webReceiptPost(peer, id, test.first, receiptBody(t, target, "different-params-alias", true))
			var aliasResp ApprovalResponse
			if err := json.Unmarshal(alias.Body.Bytes(), &aliasResp); err != nil {
				t.Fatal(err)
			}
			if alias.Code != http.StatusOK || !aliasResp.Approval.Lookup.Binding.Parameters.OverrideCoverage || aliasResp.Approval.Lookup.Receipt.Parameters.OverrideCoverage || calls.Load() != 1 {
				t.Fatalf("alias rewrote canonical parameters: %d %s", alias.Code, alias.Body)
			}
			sdkReplay, err := sdk.New(svc.cfg).Continue(context.Background(), sdk.ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: &target, ApprovalRequestID: "SDK-alias"})
			if err != nil || sdkReplay.Approval == nil || !sdkReplay.Approval.Replay || sdkReplay.Approval.Lookup.Receipt.OperationID != "shared-operation" || calls.Load() != 1 {
				t.Fatalf("actual SDK alias readmitted Web operation: %#v %v calls=%d", sdkReplay, err, calls.Load())
			}
			unblock()
		})
	}
}

func TestWebApprovalReceiptPreparedRecoveryReturnsOldReceipt(t *testing.T) {
	svc, id, calls := webReceiptFixture(t)
	target := reviewedApprovalPayload(t, svc.store, id, false).ApprovalTarget
	req := runtime.ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: &target, ApprovalRequestID: "prepared-recovery", Provider: "missing-provider"}
	prepared, err := runtime.NewCoreRunner(svc.cfg).PrepareApprovalOperation(context.Background(), req)
	if err == nil || !prepared.Lookup.Found || prepared.Lookup.Receipt.Stage != session.ApprovalReceiptPrepared || calls.Load() != 0 {
		t.Fatalf("real failed preparation control: %#v %v calls=%d", prepared, err, calls.Load())
	}
	if _, _, err := svc.store.MutatePlanMode(id, func(plan *session.PlanModeState) error {
		plan.Objective = "Changed scope after partial preparation"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := captureExecutingRepairFacts(t, svc.store, id)
	svc.mu.Lock()
	delete(svc.cfg.Providers, svc.cfg.DefaultProvider)
	svc.mu.Unlock()
	body, _ := json.Marshal(PlanModeApproveRequest{ApprovalTarget: target, ApprovalRequestID: req.ApprovalRequestID, Provider: req.Provider})
	w := approvalTargetPost(svc, id, string(body))
	var response ApprovalResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || response.Approval == nil || !response.Approval.Replay || !response.Approval.RecoveryRequired || response.Approval.Lookup.Receipt.Stage != session.ApprovalReceiptPrepared || response.Code != "APPROVAL_RECOVERY_REQUIRED" || !strings.Contains(response.Action, "ordinary continue") || calls.Load() != 0 || svc.hasActiveHandle(id) {
		t.Fatalf("prepared recovery became stale rejection or new execution: %d %s", w.Code, w.Body)
	}
	after := captureExecutingRepairFacts(t, svc.store, id)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("prepared receipt recovery restored old scope/control facts")
	}
	t.Logf("known prepared recovery response=%d replay=%v recovery=%v stage=%s provider_calls=%d", w.Code, response.Approval.Replay, response.Approval.RecoveryRequired, response.Approval.Lookup.Receipt.Stage, calls.Load())
}

func TestWebApprovalReceiptHandleFailureCannotReadmit(t *testing.T) {
	svc, id, calls := webReceiptFixture(t)
	target := reviewedApprovalPayload(t, svc.store, id, false).ApprovalTarget
	before, _ := svc.store.LoadState(id)
	preparing, resume := make(chan struct{}), make(chan struct{})
	svc.beforeApprovalPrepare = func(string) { close(preparing); <-resume }
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- approvalTargetPost(svc, id, receiptBody(t, target, "handle-failed", false)) }()
	select {
	case <-preparing:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not reach actual prepare boundary")
	}
	svc.Close()
	close(resume)
	var w *httptest.ResponseRecorder
	select {
	case w = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handle failure did not return")
	}
	var response ApprovalResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code < 400 || response.Approval == nil || response.Approval.Lookup.Receipt.Stage != session.ApprovalReceiptAdmitted || !response.Approval.RecoveryRequired || calls.Load() != 0 {
		t.Fatalf("handle failure lost committed admission: %d %s calls=%d", w.Code, w.Body, calls.Load())
	}
	after, _ := svc.store.LoadState(id)
	if after.Status != before.Status || calls.Load() != 0 || svc.hasActiveHandle(id) {
		t.Fatal("adapter failure did not release only its claim")
	}
	peer, err := New(svc.cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	replay := approvalTargetPost(peer, id, receiptBody(t, target, "handle-failed", false))
	if replay.Code != http.StatusOK || !strings.Contains(replay.Body.String(), `"stage":"admitted"`) || calls.Load() != 0 || peer.hasActiveHandle(id) {
		t.Fatalf("restart readmitted aborted adapter operation: %d %s", replay.Code, replay.Body)
	}
	t.Logf("addHandle failed=%d receipt=%s phase=%s restart replay=%d provider_calls=%d", w.Code, response.Approval.Lookup.Receipt.Stage, response.Approval.Lookup.Receipt.Phase, replay.Code, calls.Load())
}

func TestWebApprovalReceiptExplicitSDKParameters(t *testing.T) {
	svc, id, calls := webReceiptFixture(t)
	target := reviewedApprovalPayload(t, svc.store, id, false).ApprovalTarget
	temperature := 0.2
	params := session.ProviderOptions{Temperature: &temperature}
	req := sdk.ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: &target, ApprovalRequestID: "SDK-explicit", Provider: "openai", Model: "gpt-5.4", ProviderOptions: params, SystemOverride: "Explicit SDK system text"}
	if _, err := sdk.New(svc.cfg).Continue(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(PlanModeApproveRequest{ApprovalTarget: target, ApprovalRequestID: req.ApprovalRequestID, Provider: req.Provider, Model: req.Model, ProviderOptions: params, SystemOverride: req.SystemOverride})
	for _, entry := range []string{"planmode/approve", "mission/plan/approve"} {
		w := webReceiptPost(svc, id, entry, string(body))
		if w.Code != http.StatusOK || calls.Load() != 1 {
			t.Fatalf("Web alias changed SDK explicit params: %d %s calls=%d", w.Code, w.Body, calls.Load())
		}
	}
	changed := PlanModeApproveRequest{ApprovalTarget: target, ApprovalRequestID: req.ApprovalRequestID, Provider: req.Provider, Model: req.Model, ProviderOptions: params, SystemOverride: req.SystemOverride + " changed"}
	body, _ = json.Marshal(changed)
	w := approvalTargetPost(svc, id, string(body))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"APPROVAL_REQUEST_CONFLICT"`) || calls.Load() != 1 {
		t.Fatalf("same SDK ID changed explicit system: %d %s", w.Code, w.Body)
	}
	changed.ApprovalRequestID = "Web-different-parameters-alias"
	body, _ = json.Marshal(changed)
	w = approvalTargetPost(svc, id, string(body))
	var resp ApprovalResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || resp.Approval.Lookup.Binding.Parameters.SystemOverride != changed.SystemOverride || resp.Approval.Lookup.Receipt.Parameters.SystemOverride != req.SystemOverride || calls.Load() != 1 {
		t.Fatalf("new Web alias overwrote canonical explicit parameters: %d %s", w.Code, w.Body)
	}
}

func TestWebApprovalReceiptCoverageRejectedBinding(t *testing.T) {
	svc, id, calls := webReceiptFixture(t)
	target := linkWebReceiptMission(t, svc, id, "Unclaimed requirement")
	if _, _, err := svc.store.MutateGoal(id, func(goal *session.SessionGoal) error {
		goal.Mission.ValidationContract = []session.GoalValidation{{ID: "uncovered", Kind: "command", Command: "true", Status: "pending"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	target = reviewedApprovalPayload(t, svc.store, id, false).ApprovalTarget
	first := webReceiptPost(svc, id, "mission/plan/approve", receiptBody(t, target, "coverage-rejected", false))
	var rejected ApprovalResponse
	if err := json.Unmarshal(first.Body.Bytes(), &rejected); err != nil {
		t.Fatal(err)
	}
	if first.Code != http.StatusConflict || rejected.Approval == nil || rejected.Approval.Lookup.Receipt.Stage != session.ApprovalReceiptRejected || calls.Load() != 0 {
		t.Fatalf("coverage rejection was not bound: %d %s", first.Code, first.Body)
	}
	for _, entry := range []string{"planmode/approve", "mission/plan/approve"} {
		replay := webReceiptPost(svc, id, entry, receiptBody(t, target, "coverage-rejected", false))
		if replay.Code != http.StatusOK || !strings.Contains(replay.Body.String(), `"stage":"rejected"`) || calls.Load() != 0 {
			t.Fatalf("rejected replay: %d %s", replay.Code, replay.Body)
		}
	}
	changed := webReceiptPost(svc, id, "planmode/approve", receiptBody(t, target, "coverage-rejected", true))
	if changed.Code != http.StatusConflict || !strings.Contains(changed.Body.String(), `"APPROVAL_REQUEST_CONFLICT"`) || calls.Load() != 0 {
		t.Fatalf("same ID override mutated binding: %d %s", changed.Code, changed.Body)
	}
	approved := webReceiptPost(svc, id, "mission/plan/approve", receiptBody(t, target, "coverage-confirmed", true))
	if approved.Code != http.StatusAccepted || !strings.Contains(approved.Body.String(), `"stage":"admitted"`) {
		t.Fatalf("explicit new coverage operation: %d %s", approved.Code, approved.Body)
	}
	t.Logf("coverage first=%d replay=200 changed=%d confirmed=%d", first.Code, changed.Code, approved.Code)
}

func TestWebApprovalReceiptWholeTypedLedgerGate(t *testing.T) {
	svc, id, calls := webReceiptFixture(t)
	target := reviewedApprovalPayload(t, svc.store, id, false).ApprovalTarget
	if _, err := runtime.NewCoreRunner(svc.cfg).Continue(context.Background(), runtime.ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: &target, ApprovalRequestID: "corrupt-canonical"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.CreatePlanMode(id, session.PlanModeDraft{Enabled: true, Objective: "next reviewed operation", Source: session.PlanModeSourceWeb}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.SubmitPlanMode(id, session.PlanModeSubmitInput{Title: "Second", Summary: "Second scope", PlanMarkdown: "# Second plan", Verification: []string{"check"}}); err != nil {
		t.Fatal(err)
	}
	target = reviewedApprovalPayload(t, svc.store, id, false).ApprovalTarget
	if _, err := runtime.NewCoreRunner(svc.cfg).Continue(context.Background(), runtime.ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: &target, ApprovalRequestID: "valid-canonical"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(svc.store.Root(), id, "approval-operations.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ledger map[string]any
	if err := json.Unmarshal(raw, &ledger); err != nil {
		t.Fatal(err)
	}
	operation := ledger["operations"].(map[string]any)["corrupt-canonical"].(map[string]any)
	operation["recovery"].(map[string]any)["data"].(map[string]any)["complete"] = false
	raw, _ = json.Marshal(ledger)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.GetApprovalReceipt(id, "corrupt-canonical"); err != nil {
		t.Fatalf("test broke Store envelope rather than typed payload: %v", err)
	}
	for _, response := range []*httptest.ResponseRecorder{receiptGet(svc, id, "valid-canonical"), approvalTargetPost(svc, id, receiptBody(t, target, "valid-canonical", false)), approvalTargetPost(svc, id, receiptBody(t, target, "new-alias", false)), webReceiptPost(svc, id, "mission/plan/approve", receiptBody(t, target, "new-linked-alias", false))} {
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"APPROVAL_RECOVERY_REQUIRED"`) || calls.Load() != 2 {
			t.Fatalf("corrupt typed payload bypassed gate: %d %s calls=%d", response.Code, response.Body, calls.Load())
		}
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(raw, after) {
		t.Fatal("corrupt ledger was changed by query/retry")
	}
}

func TestWebApprovalReceiptQueryReadonlyControl(t *testing.T) {
	svc, id, calls := webReceiptFixture(t)
	target := reviewedApprovalPayload(t, svc.store, id, false).ApprovalTarget
	if _, err := runtime.NewCoreRunner(svc.cfg).Continue(context.Background(), runtime.ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: &target, ApprovalRequestID: "web-query"}); err != nil {
		t.Fatal(err)
	}
	before, _ := svc.store.LoadState(id)
	w := receiptGet(svc, id, "web-query")
	t.Logf("query status=%d provider_calls=%d body=%s", w.Code, calls.Load(), w.Body.String())
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"current_state"`) || !strings.Contains(w.Body.String(), `"web-query"`) {
		t.Errorf("receipt query: %d %s", w.Code, w.Body)
	}
	unknown := receiptGet(svc, id, "not-delivered")
	if unknown.Code != http.StatusNotFound {
		t.Errorf("unknown request query: %d %s", unknown.Code, unknown.Body)
	}
	after, _ := svc.store.LoadState(id)
	if calls.Load() != 1 || !reflect.DeepEqual(before, after) {
		t.Fatal("receipt query was not readonly")
	}
}
