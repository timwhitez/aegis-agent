package webconsole

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"aegis-agent/internal/events"
	"aegis-agent/internal/runtime"
	"aegis-agent/internal/session"
)

// appendRunOwnerAcquire writes the durable owner fact a CLI, SDK or queue
// worker run leaves behind. The web console must treat it exactly like its own
// handle events.
func appendRunOwnerAcquire(t *testing.T, svc *Service, sessionID string, pid int, startedAt string) {
	t.Helper()
	if err := svc.store.AppendEvent(sessionID, events.New(sessionID, runtime.RunOwnerAcquiredEventType, "prepare", map[string]any{
		"source":           "runtime",
		"process_start_id": strconv.Itoa(pid) + ":" + startedAt,
		"pid":              pid,
		"started_at":       startedAt,
	})); err != nil {
		t.Fatalf("append run owner event: %v", err)
	}
}

func newCrossProcessControlService(t *testing.T, alive func(pid int) bool) *Service {
	t.Helper()
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(func() { svc.Close() })
	previousAlive := webconsoleProcessAlive
	webconsoleProcessAlive = alive
	t.Cleanup(func() { webconsoleProcessAlive = previousAlive })
	return svc
}

func postSessionControl(t *testing.T, svc *Service, sessionID, action string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	req := newLocalWebRequest(http.MethodPost, "/api/sessions/"+sessionID+"/"+action, nil)
	req.Header.Set(webMutationHeader, "1")
	svc.ServeHTTP(recorder, req)
	return recorder
}

func decodeControlResponse(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode control response: %v body=%s", err, recorder.Body.String())
	}
	return response
}

func TestStopSessionReconcilesDeadRuntimeRunOwner(t *testing.T) {
	svc := newCrossProcessControlService(t, func(pid int) bool { return pid != 818181 })

	meta := testSessionMetadata(t, "runtime_owner_stop_dead")
	state := testSessionState(session.StatusRunning)
	state.Turn = 7
	if err := svc.store.Create(meta, state); err != nil {
		t.Fatalf("create running session: %v", err)
	}
	appendRunOwnerAcquire(t, svc, meta.ID, 818181, "2026-06-12T06:09:05Z")

	recorder := postSessionControl(t, svc, meta.ID, "stop")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected dead runtime owner stop to be accepted, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	response := decodeControlResponse(t, recorder)
	if response["reconciled"] != true {
		t.Fatalf("expected reconciled stop response, got %#v", response)
	}
	if response["cross_process"] == true {
		t.Fatalf("dead owner must reconcile instead of steering, got %#v", response)
	}
	after, err := svc.store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load reconciled state: %v", err)
	}
	if after.Status != session.StatusPaused || after.Phase != "interrupt" || after.PauseReason != "manual_stop" || after.Turn != 7 {
		t.Fatalf("expected paused manual_stop state, got %#v", after)
	}
	requests, err := svc.store.LoadSteerRequests(meta.ID)
	if err != nil {
		t.Fatalf("load steer requests: %v", err)
	}
	if len(requests) != 0 {
		t.Fatalf("dead owner stop must not queue a cross-process steer, got %#v", requests)
	}
	eventsList, err := svc.store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	if countWebEventType(eventsList, "session.paused") != 1 {
		t.Fatalf("expected one reconciled pause event, got %#v", eventsList)
	}
}

func TestStopSessionQueuesInterruptSteerForLiveExternalRunOwner(t *testing.T) {
	svc := newCrossProcessControlService(t, func(int) bool { return true })

	meta := testSessionMetadata(t, "runtime_owner_stop_live")
	if err := svc.store.Create(meta, testSessionState(session.StatusRunning)); err != nil {
		t.Fatalf("create running session: %v", err)
	}
	appendRunOwnerAcquire(t, svc, meta.ID, 717171, "2026-06-12T06:09:05Z")

	recorder := postSessionControl(t, svc, meta.ID, "stop")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected live external owner stop to be accepted, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	response := decodeControlResponse(t, recorder)
	if response["cross_process"] != true || response["steer"] != "queued" {
		t.Fatalf("expected queued cross-process stop steer, got %#v", response)
	}
	if response["reconciled"] == true {
		t.Fatalf("live external owner must not be reconciled, got %#v", response)
	}

	after, err := svc.store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if after.Status != session.StatusRunning {
		t.Fatalf("live external owner must stay running, got %#v", after)
	}
	requests, err := svc.store.LoadSteerRequests(meta.ID)
	if err != nil {
		t.Fatalf("load steer requests: %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("expected exactly one queued stop steer, got %#v", requests)
	}
	if !requests[0].Interrupt || requests[0].Text != stopFallbackSteerMessage || requests[0].Status != session.SteerStatusPending {
		t.Fatalf("expected pending interrupt stop steer, got %#v", requests[0])
	}
	eventsList, err := svc.store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	if countWebEventType(eventsList, "session.paused") != 0 {
		t.Fatalf("live external owner must not be paused, got %#v", eventsList)
	}

	recorder = postSessionControl(t, svc, meta.ID, "stop")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected repeated stop to stay accepted, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	response = decodeControlResponse(t, recorder)
	if response["steer"] != "already_queued" {
		t.Fatalf("expected duplicate stop steer to be suppressed, got %#v", response)
	}
	requests, err = svc.store.LoadSteerRequests(meta.ID)
	if err != nil {
		t.Fatalf("reload steer requests: %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("expected no duplicate stop steer, got %#v", requests)
	}
}

func TestInterruptSessionQueuesInterruptSteerForLiveExternalRunOwner(t *testing.T) {
	svc := newCrossProcessControlService(t, func(int) bool { return true })

	meta := testSessionMetadata(t, "runtime_owner_interrupt_live")
	if err := svc.store.Create(meta, testSessionState(session.StatusRunning)); err != nil {
		t.Fatalf("create running session: %v", err)
	}
	appendRunOwnerAcquire(t, svc, meta.ID, 616161, "2026-06-12T06:09:05Z")

	recorder := postSessionControl(t, svc, meta.ID, "interrupt")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected live external owner interrupt to be accepted, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	response := decodeControlResponse(t, recorder)
	if response["cross_process"] != true || response["status"] != "interrupt_requested" {
		t.Fatalf("expected cross-process interrupt response, got %#v", response)
	}
	requests, err := svc.store.LoadSteerRequests(meta.ID)
	if err != nil {
		t.Fatalf("load steer requests: %v", err)
	}
	if len(requests) != 1 || !requests[0].Interrupt || requests[0].Text != runtime.InterruptWithoutFinishSteerMessage {
		t.Fatalf("expected one pending interrupt steer, got %#v", requests)
	}
	if requests[0].Text == stopFallbackSteerMessage {
		t.Fatalf("interrupt steer must stay distinct from the stop fallback steer, got %#v", requests[0])
	}
	after, err := svc.store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if after.Status != session.StatusRunning {
		t.Fatalf("live external owner must stay running, got %#v", after)
	}
}

func TestReconcileStaleRunningSessionKeepsLiveExternalRunOwner(t *testing.T) {
	svc := newCrossProcessControlService(t, func(int) bool { return true })

	meta := testSessionMetadata(t, "runtime_owner_reconcile_live")
	if err := svc.store.Create(meta, testSessionState(session.StatusRunning)); err != nil {
		t.Fatalf("create running session: %v", err)
	}
	appendRunOwnerAcquire(t, svc, meta.ID, 515151, "2026-06-12T06:09:05Z")

	changed, err := svc.reconcileStaleRunningSession(meta.ID, "manual_stop")
	if err != nil {
		t.Fatalf("reconcile stale running session: %v", err)
	}
	if changed {
		t.Fatal("expected a live external owner to block reconciliation")
	}
	after, err := svc.store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if after.Status != session.StatusRunning {
		t.Fatalf("live external owner must stay running, got %#v", after)
	}
	eventsList, err := svc.store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	if countWebEventType(eventsList, "session.paused") != 0 {
		t.Fatalf("live external owner must not be paused, got %#v", eventsList)
	}
}

func TestCrossProcessControlKeepsNotOwnedResponseWithoutOwnerEvents(t *testing.T) {
	svc := newCrossProcessControlService(t, func(int) bool { return true })
	ts := httptest.NewServer(svc)
	defer ts.Close()

	running := testSessionMetadata(t, "no_owner_events_running")
	if err := svc.store.Create(running, testSessionState(session.StatusRunning)); err != nil {
		t.Fatalf("create running session: %v", err)
	}
	settled := testSessionMetadata(t, "no_owner_events_completed")
	if err := svc.store.Create(settled, testSessionState(session.StatusCompleted)); err != nil {
		t.Fatalf("create completed session: %v", err)
	}

	for _, sessionID := range []string{running.ID, settled.ID} {
		for _, action := range []string{"stop", "interrupt"} {
			errResp := postJSONError(t, ts.URL+"/api/sessions/"+sessionID+"/"+action, map[string]any{}, http.StatusConflict)
			if errResp.Code != errorCodeActiveHandleNotOwned || errResp.Action == "" {
				t.Fatalf("expected structured active-handle error for %s/%s, got %#v", sessionID, action, errResp)
			}
		}
		requests, err := svc.store.LoadSteerRequests(sessionID)
		if err != nil {
			t.Fatalf("load steer requests: %v", err)
		}
		if len(requests) != 0 {
			t.Fatalf("sessions without an owner clue must not queue steers, got %#v", requests)
		}
	}
}

func TestReconcileMissingOwnerRunningSessionAfterGraceAndForceControl(t *testing.T) {
	svc := newCrossProcessControlService(t, func(int) bool { return false })
	ts := httptest.NewServer(svc)
	defer ts.Close()

	// Stale running session with NO owner events (UpdatedAt 30s ago).
	stale := testSessionMetadata(t, "missing_owner_stale")
	staleState := session.State{
		Status:    session.StatusRunning,
		Phase:     "provider_call",
		UpdatedAt: time.Now().UTC().Add(-30 * time.Second).Format(time.RFC3339Nano),
	}
	if err := svc.store.Create(stale, staleState); err != nil {
		t.Fatalf("create stale session: %v", err)
	}
	if err := svc.reconcileAllStaleRunningSessions(); err != nil {
		t.Fatalf("reconcileAllStaleRunningSessions: %v", err)
	}
	after, err := svc.store.LoadState(stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != session.StatusPaused || after.PauseReason != "stale_owner_missing" {
		t.Fatalf("expected stale missing-owner session paused with stale_owner_missing, got %#v", after)
	}

	// Fresh (<5s) running session with NO owner events: ?force=true reconciles immediately on interrupt, stop, and delete.
	for _, action := range []string{"stop", "interrupt"} {
		fresh := testSessionMetadata(t, "missing_owner_force_"+action)
		if err := svc.store.Create(fresh, testSessionState(session.StatusRunning)); err != nil {
			t.Fatal(err)
		}
		var resp map[string]any
		postJSON(t, ts.URL+"/api/sessions/"+fresh.ID+"/"+action+"?force=true", map[string]any{}, http.StatusAccepted, &resp)
		if resp["reconciled"] != true {
			t.Fatalf("expected ?force=true %s to reconcile missing-owner session, got %#v", action, resp)
		}
		loaded, err := svc.store.LoadState(fresh.ID)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Status != session.StatusPaused {
			t.Fatalf("expected paused after force %s, got %#v", action, loaded)
		}
	}

	// Corrupt session must not abort reconciling subsequent valid stale sessions, and runReaperPass logs audit errors instead of silently stopping.
	corrupt := testSessionMetadata(t, "corrupt_running")
	if err := svc.store.Create(corrupt, staleState); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svc.store.SessionDir(corrupt.ID), "events.jsonl"), []byte("{not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	validAfterCorrupt := testSessionMetadata(t, "valid_after_corrupt")
	if err := svc.store.Create(validAfterCorrupt, staleState); err != nil {
		t.Fatal(err)
	}
	svc.runReaperPass()
	reconciledAfter, err := svc.store.LoadState(validAfterCorrupt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reconciledAfter.Status != session.StatusPaused {
		t.Fatalf("expected valid stale session after corrupt session to be reconciled, got %#v", reconciledAfter)
	}
	auditBytes, err := os.ReadFile(webAuditLogPath(svc.cfg.Session.Dir))
	if err != nil || !strings.Contains(string(auditBytes), "web.reaper.error") {
		t.Fatalf("expected web.reaper.error audit event when reaper hits corrupt session, err=%v audit=%s", err, string(auditBytes))
	}
}

func TestReconcileDeadExecutingApprovalOperationUsesApprovalRecoveryReason(t *testing.T) {
	svc := newCrossProcessControlService(t, func(int) bool { return false })

	meta := testSessionMetadata(t, "dead_approval_executing")
	nowStr := time.Now().UTC().Add(-20 * time.Second).Format(time.RFC3339Nano)
	if err := svc.store.Create(meta, session.State{
		Status:    session.StatusAwaitingInput,
		Phase:     "plan_approval",
		UpdatedAt: nowStr,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.CreatePlanMode(meta.ID, session.PlanModeDraft{
		Enabled:   true,
		Objective: "Approve plan",
		Source:    session.PlanModeSourceWeb,
	}); err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	submitted, err := svc.store.SubmitPlanMode(meta.ID, session.PlanModeSubmitInput{
		Title:        "Plan",
		Summary:      "Summary",
		PlanMarkdown: "# Plan",
		Verification: []string{"go test ./..."},
		Source:       session.PlanModeSourceTool,
	})
	if err != nil {
		t.Fatalf("submit plan mode: %v", err)
	}
	snap, err := svc.store.LoadApprovalSnapshot(meta.ID)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	target := session.ApprovalTarget{
		PlanModeID:       submitted.PlanModeID,
		PlanVersion:      submitted.PlanVersion,
		ExpectedRevision: snap.Revision,
	}
	prepResult, err := runtime.NewCoreRunner(svc.cfg).PrepareApprovalOperation(context.Background(), runtime.ContinueRequest{
		SessionID:         meta.ID,
		ApprovePlan:       true,
		ApprovalTarget:    &target,
		ApprovalRequestID: "req_dead_exec",
		Source:            session.PlanModeSourceWeb,
	})
	if err != nil {
		t.Fatalf("prepare approval operation: %v", err)
	}
	receipt := prepResult.Lookup.Receipt
	var dataMap map[string]any
	if err := json.Unmarshal(receipt.Recovery.Data, &dataMap); err != nil {
		t.Fatal(err)
	}
	prepMap := dataMap["preparation"].(map[string]any)
	prepMap["phase"] = "executing"
	prepMap["completed_phase"] = "executing"
	prepMap["owner_pid"] = 999998
	recoveryPayload := receipt.Recovery
	recoveryPayload.OwnerPID = 999998
	recoveryPayload.Data = mustMarshalTestJSON(t, dataMap)
	if _, err := svc.store.CheckpointApprovalOperation(meta.ID, receipt.OperationID, receipt.Recovery.RunGeneration, "executing", recoveryPayload); err != nil {
		t.Fatalf("checkpoint executing: %v", err)
	}
	appendRunOwnerAcquire(t, svc, meta.ID, 999998, "2026-06-12T06:09:05Z")

	if err := svc.reconcileAllStaleRunningSessions(); err != nil {
		t.Fatalf("reconcileAllStaleRunningSessions: %v", err)
	}
	after, err := svc.store.LoadState(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != session.StatusPaused || after.PauseReason != "stale_owner_reconciled_approval_recovery" {
		t.Fatalf("expected approval recovery pause reason, got %#v", after)
	}
}

func TestReaperReconcilesDeadExecutingApprovalAfterPhaseAdvance(t *testing.T) {
	svc := newCrossProcessControlService(t, func(pid int) bool { return pid == os.Getpid() })

	createExecutingApproval := func(id, requestID string, ownerPID int, advanceState, withOwnerEvent bool) (session.SessionMetadata, session.ApprovalTarget) {
		t.Helper()
		meta := testSessionMetadata(t, id)
		nowStr := time.Now().UTC().Format(time.RFC3339Nano)
		if err := svc.store.Create(meta, session.State{
			Status:    session.StatusAwaitingInput,
			Phase:     "plan_approval",
			UpdatedAt: nowStr,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.store.CreatePlanMode(meta.ID, session.PlanModeDraft{
			Enabled:   true,
			Objective: "Approve and execute plan",
			Source:    session.PlanModeSourceCLI,
		}); err != nil {
			t.Fatalf("create plan mode: %v", err)
		}
		submitted, err := svc.store.SubmitPlanMode(meta.ID, session.PlanModeSubmitInput{
			Title:        "Plan",
			Summary:      "Summary",
			PlanMarkdown: "# Plan",
			Verification: []string{"go test ./..."},
			Source:       session.PlanModeSourceTool,
		})
		if err != nil {
			t.Fatalf("submit plan mode: %v", err)
		}
		snap, err := svc.store.LoadApprovalSnapshot(meta.ID)
		if err != nil {
			t.Fatalf("load snapshot: %v", err)
		}
		target := session.ApprovalTarget{
			PlanModeID:       submitted.PlanModeID,
			PlanVersion:      submitted.PlanVersion,
			ExpectedRevision: snap.Revision,
		}
		prepResult, err := runtime.NewCoreRunner(svc.cfg).PrepareApprovalOperation(context.Background(), runtime.ContinueRequest{
			SessionID:         meta.ID,
			ApprovePlan:       true,
			ApprovalTarget:    &target,
			ApprovalRequestID: requestID,
			Source:            session.PlanModeSourceCLI,
		})
		if err != nil {
			t.Fatalf("prepare approval operation: %v", err)
		}
		receipt := prepResult.Lookup.Receipt
		var dataMap map[string]any
		if err := json.Unmarshal(receipt.Recovery.Data, &dataMap); err != nil {
			t.Fatal(err)
		}
		prepMap := dataMap["preparation"].(map[string]any)
		prepMap["phase"] = "executing"
		prepMap["completed_phase"] = "executing"
		prepMap["owner_pid"] = ownerPID
		prepMap["owner_identity"] = ""
		recoveryPayload := receipt.Recovery
		recoveryPayload.OwnerPID = ownerPID
		recoveryPayload.OwnerIdentity = ""
		recoveryPayload.Data = mustMarshalTestJSON(t, dataMap)
		if _, err := svc.store.CheckpointApprovalOperation(meta.ID, receipt.OperationID, receipt.Recovery.RunGeneration, "executing", recoveryPayload); err != nil {
			t.Fatalf("checkpoint executing: %v", err)
		}
		if advanceState {
			current, err := svc.store.LoadState(meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			current.Phase = "provider_call"
			current.Turn = 1
			if err := svc.store.SaveState(meta.ID, current); err != nil {
				t.Fatalf("advance state to provider_call: %v", err)
			}
		}
		if withOwnerEvent {
			appendRunOwnerAcquire(t, svc, meta.ID, ownerPID, "2026-11-25T07:09:45Z")
		}
		return meta, target
	}

	// Case 1 (#217 exact repro): state advanced to running/provider_call Turn=1, dead owner PID.
	deadMeta, deadTarget := createExecutingApproval("dead_approval_provider_call", "req_dead_exec_advanced", 999998, true, true)
	deadState, err := svc.store.LoadState(deadMeta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if canReconcile, err := runtime.CanReconcileApprovalPreparation(svc.store, deadMeta.ID); canReconcile || !errors.Is(err, session.ErrApprovalConflict) {
		t.Fatalf("expected CanReconcileApprovalPreparation to return ErrApprovalConflict after phase advance, got canReconcile=%v err=%v", canReconcile, err)
	}
	if !runtime.HasDeadExecutingApprovalOperation(svc.store, deadMeta.ID, deadState.RunGeneration) {
		t.Fatalf("expected HasDeadExecutingApprovalOperation=true for dead executing approval")
	}

	// Case 2: dead executing approval killed before run.owner.acquired was appended (fresh UpdatedAt < 5s).
	noOwnerMeta, _ := createExecutingApproval("dead_approval_missing_owner_evt", "req_dead_exec_no_owner", 999997, true, false)

	// Case 3: live executing approval (current process PID) must NOT be reconciled.
	liveMeta, _ := createExecutingApproval("live_approval_provider_call", "req_live_exec_advanced", os.Getpid(), true, true)

	// Run the actual background reaper pass.
	svc.runReaperPass()

	// Verify Case 1 reconciled cleanly without reaper error audit entries.
	afterDead, err := svc.store.LoadState(deadMeta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterDead.Status != session.StatusPaused || afterDead.Phase != "interrupt" || afterDead.PauseReason != "stale_owner_reconciled_approval_recovery" || afterDead.Turn != 1 {
		t.Fatalf("expected dead executing approval to reconcile to paused/stale_owner_reconciled_approval_recovery, got %#v", afterDead)
	}
	deadEvents, err := svc.store.LoadEvents(deadMeta.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundApprovalRecoveryEvt := false
	for _, evt := range deadEvents {
		if evt.Type == "session.paused" && evt.Data["reason"] == "stale_owner_reconciled_approval_recovery" && evt.Data["approval_recovery"] == true {
			foundApprovalRecoveryEvt = true
		}
	}
	if !foundApprovalRecoveryEvt {
		t.Fatalf("expected session.paused event with approval_recovery=true, got %#v", deadEvents)
	}
	if err := runtime.ValidateContinueTargetWithStore(svc.store, deadMeta, afterDead); err != nil {
		t.Fatalf("expected reconciled session to accept ordinary continue, got %v", err)
	}
	replayRes, err := runtime.NewCoreRunner(svc.cfg).PrepareApprovalOperation(context.Background(), runtime.ContinueRequest{
		SessionID:         deadMeta.ID,
		ApprovePlan:       true,
		ApprovalTarget:    &deadTarget,
		ApprovalRequestID: "req_dead_exec_advanced",
		Source:            session.PlanModeSourceCLI,
	})
	if err != nil || !replayRes.Replay || replayRes.RecoveryRequired || replayRes.Prepared != nil {
		t.Fatalf("expected admitted approval replay without duplicate execution, got res=%#v err=%v", replayRes, err)
	}

	// Verify Case 2 reconciled immediately even without run.owner.acquired event.
	afterNoOwner, err := svc.store.LoadState(noOwnerMeta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterNoOwner.Status != session.StatusPaused || afterNoOwner.PauseReason != "stale_owner_reconciled_approval_recovery" {
		t.Fatalf("expected dead executing approval without owner event to reconcile with approval recovery reason, got %#v", afterNoOwner)
	}

	// Verify Case 3 (live owner) stayed running.
	afterLive, err := svc.store.LoadState(liveMeta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterLive.Status != session.StatusRunning {
		t.Fatalf("live executing approval must stay running, got %#v", afterLive)
	}

	// Verify zero web.reaper.error audit entries were produced.
	if auditBytes, err := os.ReadFile(webAuditLogPath(svc.cfg.Session.Dir)); err == nil && strings.Contains(string(auditBytes), "web.reaper.error") {
		t.Fatalf("unexpected web.reaper.error audit event during healthy approval recovery:\n%s", string(auditBytes))
	}

	// Case 4: corrupt approval-operations.json must still fail and must NOT reconcile.
	corruptMeta, _ := createExecutingApproval("corrupt_approval_ledger", "req_corrupt_ledger", 999996, true, true)
	if err := os.WriteFile(filepath.Join(svc.store.SessionDir(corruptMeta.ID), "approval-operations.json"), []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := svc.reconcileAllStaleRunningSessions(); err == nil {
		t.Fatal("expected corrupt approval ledger to return error during reconciliation")
	}
	afterCorrupt, err := svc.store.LoadState(corruptMeta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterCorrupt.Status != session.StatusRunning {
		t.Fatalf("corrupt approval ledger session must not be reconciled, got %#v", afterCorrupt)
	}
}

func mustMarshalTestJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
