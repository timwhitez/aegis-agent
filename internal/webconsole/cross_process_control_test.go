package webconsole

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

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
