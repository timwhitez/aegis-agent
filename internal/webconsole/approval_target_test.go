package webconsole

import (
	"aegis-agent/internal/events"
	"aegis-agent/internal/runtime"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aegis-agent/internal/session"
)

func newApprovalTargetFixture(t *testing.T) (*Service, string) {
	t.Helper()
	return newApprovalTargetFixtureWithProvider(t, newFinishServer())
}

func newApprovalTargetFixtureWithProvider(t *testing.T, provider *httptest.Server) (*Service, string) {
	t.Helper()
	t.Cleanup(provider.Close)
	svc, err := New(testConfig(t, provider.URL), Options{WorkerCount: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	meta := testSessionMetadata(t, "approval_target")
	meta.Mode = session.ModeExec
	meta.RootSessionID = meta.ID
	meta.CompletionPolicy = session.CompletionPolicyAutonomous
	if err := svc.store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_approval"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.CreatePlanMode(meta.ID, session.PlanModeDraft{Enabled: true, Objective: "Review this plan"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.SubmitPlanMode(meta.ID, session.PlanModeSubmitInput{Title: "Plan", Summary: "V1", Verification: []string{"check"}, PlanMarkdown: "# Plan\nV1", Source: session.PlanModeSourceTool}); err != nil {
		t.Fatal(err)
	}
	return svc, meta.ID
}

func approvalTargetPost(svc *Service, id, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/sessions/"+id+"/planmode/approve", strings.NewReader(body))
	r.Host = "127.0.0.1"
	r.Header.Set("X-Aegis-Agent-Web", "1")
	r.Header.Set("Content-Type", "application/json")
	svc.ServeHTTP(w, r)
	return w
}

func TestApprovalTargetMissingDoesNotAdmitLatest(t *testing.T) {
	svc, id := newApprovalTargetFixture(t)
	w := approvalTargetPost(svc, id, `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing review target must fail before admission; got %d %s", w.Code, w.Body)
	}
	state, err := svc.store.LoadState(id)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != session.StatusAwaitingInput {
		t.Fatalf("rejected approval changed recoverable state: %#v", state)
	}
	plan, err := svc.store.LoadPlanMode(id)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ApprovedVersion != 0 {
		t.Fatalf("rejected approval approved latest: %#v", plan)
	}
	events, err := svc.store.LoadEvents(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == "webconsole.handle.acquired" || event.Type == "session.resumed" {
			t.Fatalf("rejected approval published acceptance: %#v", event)
		}
	}
}

func reviewedApprovalPayload(t *testing.T, store *session.Store, id string, override bool) PlanModeApproveRequest {
	t.Helper()
	snapshot, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	return PlanModeApproveRequest{ApprovalTarget: snapshot.Target(), ApprovalRequestID: "reviewed_" + snapshot.PlanMode.PlanModeID, OverrideCoverage: override}
}

func assertApprovalUnaccepted(t *testing.T, svc *Service, id string) {
	t.Helper()
	state, err := svc.store.LoadState(id)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != session.StatusAwaitingInput {
		t.Fatalf("rejected target changed recoverable state: %#v", state)
	}
	plan, err := svc.store.LoadPlanMode(id)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ApprovedVersion != 0 {
		t.Fatalf("stale approval approved newer scope: %#v", plan)
	}
	events, err := svc.store.LoadEvents(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Type == "webconsole.handle.acquired" || e.Type == "session.resumed" || strings.HasPrefix(e.Type, "provider.") {
			t.Fatalf("stale request produced acceptance/provider fact: %#v", e)
		}
	}
	messages, err := svc.store.LoadMessages(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range messages {
		if m.Meta["source"] == "planmode_approval" {
			t.Fatalf("stale request produced replay fact: %#v", m)
		}
	}
}

func TestApprovalTargetStaleScopeRejectedBeforeAcceptance(t *testing.T) {
	for _, change := range []string{"version", "replacement", "linked_contents", "progress_assertions", "progress_validations", "goal_deleted"} {
		t.Run(change, func(t *testing.T) {
			svc, id := newApprovalTargetFixture(t)
			if strings.HasPrefix(change, "linked") || strings.HasPrefix(change, "progress") || change == "goal_deleted" {
				goal, err := svc.store.CreateGoal(id, session.GoalDraft{Enabled: true, Mode: session.GoalModeMission, Objective: "linked"})
				if err != nil {
					t.Fatal(err)
				}
				goal.Mission.ValidationContract = []session.GoalValidation{{ID: "validation", Kind: "command", Command: "go test", Status: "pending"}}
				goal.Mission.Features = []session.MissionFeature{{ID: "feature", Title: "Feature", Status: "pending"}}
				goal.Mission.Milestones = []session.MissionMilestone{{ID: "milestone", Title: "Milestone", Status: "pending"}}
				if err := svc.store.SaveGoal(id, goal); err != nil {
					t.Fatal(err)
				}
				plan, err := svc.store.LoadPlanMode(id)
				if err != nil {
					t.Fatal(err)
				}
				plan.LinkedGoalID = goal.GoalID
				if err := svc.store.SavePlanMode(id, plan); err != nil {
					t.Fatal(err)
				}
			}
			payload := reviewedApprovalPayload(t, svc.store, id, true)
			other := session.NewStore(svc.store.Root())
			switch change {
			case "version":
				if _, err := other.RevisePlanMode(id, session.PlanModeSourceWeb, "revise"); err != nil {
					t.Fatal(err)
				}
				if _, err := other.SubmitPlanMode(id, session.PlanModeSubmitInput{Title: "Plan", Summary: "V2", Verification: []string{"check"}, PlanMarkdown: "# Plan\nV2", Source: session.PlanModeSourceTool}); err != nil {
					t.Fatal(err)
				}
			case "replacement":
				plan, err := other.LoadPlanMode(id)
				if err != nil {
					t.Fatal(err)
				}
				plan.PlanModeID = session.NewPlanModeID()
				if err := other.SavePlanMode(id, plan); err != nil {
					t.Fatal(err)
				}
			case "linked_contents":
				if _, _, err := other.MutateGoal(id, func(goal *session.SessionGoal) error {
					goal.Mission.Requirements = []session.MissionRequirement{{ID: "req", Text: "new scope"}}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			case "progress_assertions":
				if _, _, err := other.RecordGoalProgress(id, session.GoalProgressInput{Summary: "Mapping", FeatureUpdates: []session.MissionFeatureProgressUpdate{{ID: "feature", ClaimedAssertions: []string{"validation"}}}}); err != nil {
					t.Fatal(err)
				}
			case "progress_validations":
				if _, _, err := other.RecordGoalProgress(id, session.GoalProgressInput{Summary: "Mapping", MilestoneUpdates: []session.MissionMilestoneProgressUpdate{{ID: "milestone", ValidationIDs: []string{"validation"}}}}); err != nil {
					t.Fatal(err)
				}
			case "goal_deleted":
				if _, err := other.ClearGoal(id); err != nil {
					t.Fatal(err)
				}
			}
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			w := approvalTargetPost(svc, id, string(body))
			if w.Code != http.StatusConflict {
				t.Fatalf("stale %s got %d %s", change, w.Code, w.Body)
			}
			assertApprovalUnaccepted(t, svc, id)
		})
	}
}

func TestApprovalTargetCoherentDisplayAndAdmissionControl(t *testing.T) {
	svc, id := newApprovalTargetFixture(t)
	snapshot, err := svc.store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	svc.handlePlanModeGet(w, id)
	var display struct {
		session.PlanModeState
		LinkedGoal *session.SessionGoal `json:"linked_goal"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &display); err != nil {
		t.Fatal(err)
	}
	if display.ApprovalRevision != snapshot.Revision || display.PlanMarkdown != snapshot.PlanMode.PlanMarkdown {
		t.Fatalf("display mismatched snapshot: %#v", display)
	}
	body, err := json.Marshal(PlanModeApproveRequest{ApprovalTarget: snapshot.Target(), ApprovalRequestID: "coherent-display"})
	if err != nil {
		t.Fatal(err)
	}
	w = approvalTargetPost(svc, id, string(body))
	if w.Code != http.StatusAccepted {
		t.Fatalf("reviewed target rejected: %d %s", w.Code, w.Body)
	}
	// All durable preparation is visible at response time, before async completion.
	plan, err := svc.store.LoadPlanMode(id)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ApprovedRevision != snapshot.Revision {
		t.Fatalf("approved scope=%s want=%s", plan.ApprovedRevision, snapshot.Revision)
	}
	messages, err := svc.store.LoadMessages(id)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range messages {
		if m.Meta["source"] == "planmode_approval" && m.Meta["approved_revision"] == snapshot.Revision {
			found = true
		}
	}
	if !found {
		t.Fatal("202 returned before replay preparation")
	}
	waitFor(t, 4*time.Second, func() bool { state, _ := svc.store.LoadState(id); return state.Status == session.StatusCompleted }, func() string { state, _ := svc.store.LoadState(id); return fmt.Sprint(state) })
}

func TestApprovalTargetInterleavedUpdateAtSynchronousBoundary(t *testing.T) {
	svc, id := newApprovalTargetFixture(t)
	payload := reviewedApprovalPayload(t, svc.store, id, false)
	svc.beforeApprovalPrepare = func(id string) {
		other := session.NewStore(svc.store.Root())
		if _, err := other.RevisePlanMode(id, session.PlanModeSourceWeb, "V2"); err != nil {
			t.Fatal(err)
		}
		if _, err := other.SubmitPlanMode(id, session.PlanModeSubmitInput{Title: "Plan", Summary: "V2", Verification: []string{"check"}, PlanMarkdown: "# Plan\nV2", Source: session.PlanModeSourceTool}); err != nil {
			t.Fatal(err)
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	w := approvalTargetPost(svc, id, string(body))
	if w.Code != http.StatusConflict {
		t.Fatalf("interleaved stale must return409 before admission: %d %s", w.Code, w.Body)
	}
	assertApprovalUnaccepted(t, svc, id)
}

func TestLinkedApprovalCannotDropTargetToFactsAfterUnlink(t *testing.T) {
	svc, id := newApprovalTargetFixture(t)
	goal, err := svc.store.CreateGoal(id, session.GoalDraft{Enabled: true, Mode: session.GoalModeMission, Objective: "Linked scope"})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := svc.store.LoadPlanMode(id)
	if err != nil {
		t.Fatal(err)
	}
	plan.LinkedGoalID = goal.GoalID
	if err := svc.store.SavePlanMode(id, plan); err != nil {
		t.Fatal(err)
	}
	payload := reviewedApprovalPayload(t, svc.store, id, false)
	plan.LinkedGoalID = ""
	if err := svc.store.SavePlanMode(id, plan); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(svc)
	defer ts.Close()
	response := postJSONError(t, ts.URL+"/api/sessions/"+id+"/mission/plan/approve", payload, http.StatusConflict)
	if response.Code != "APPROVAL_TARGET_CONFLICT" {
		t.Fatalf("missing explicit conflict: %#v", response)
	}
	loaded, err := svc.store.LoadGoal(id)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Mission.PlanStatus == session.MissionPlanStatusApproved {
		t.Fatal("unlinked target silently narrowed to purefacts approval")
	}
	assertApprovalUnaccepted(t, svc, id)
}

func TestApprovalPreparationClaimSurvivesPeerReaperBeforeHandle(t *testing.T) {
	svc, id := newApprovalTargetFixture(t)
	if err := svc.store.AppendEvent(id, events.New(id, "webconsole.handle.released", "webconsole", map[string]any{"process_start_id": webconsoleProcessOwner.processStartID, "pid": webconsoleProcessOwner.pid, "started_at": webconsoleProcessOwner.startedAt})); err != nil {
		t.Fatal(err)
	}
	peer, err := New(svc.cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	runner := runtime.NewRunner(svc.cfg)
	snapshot, err := runner.Store().LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	target := snapshot.Target()
	prepared, err := runner.PrepareApprovalContinue(context.Background(), runtime.ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: &target, ApprovalRequestID: "claim_owner"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runner.AbortPreparedApproval(prepared, nil); err != nil {
			t.Error(err)
		}
	}()
	if changed, err := peer.reconcileStaleRunningSession(id, "peer_reaper"); err != nil || changed {
		t.Fatalf("peer reclaimed live preparation gap: changed=%v err=%v", changed, err)
	}
	state, err := svc.store.LoadState(id)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != session.StatusRunning {
		t.Fatalf("live claim was overwritten: %#v", state)
	}
	second := runtime.NewRunner(svc.cfg)
	if _, err := second.PrepareApprovalContinue(context.Background(), runtime.ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: &target, ApprovalRequestID: "claim_peer"}); err == nil {
		t.Fatal("peer reaper permitted duplicate claim")
	}
}

func TestExecutingMissionFactRepairCannotApproveDifferentScope(t *testing.T) {
	for _, testCase := range []struct {
		name            string
		changed, legacy bool
	}{{name: "known_same"}, {name: "known_changed", changed: true}, {name: "legacy_unknown", legacy: true}} {
		t.Run(testCase.name, func(t *testing.T) {
			svc, id := newApprovalTargetFixture(t)
			goal, err := svc.store.CreateGoal(id, session.GoalDraft{Enabled: true, Mode: session.GoalModeMission, Objective: "Linked"})
			if err != nil {
				t.Fatal(err)
			}
			plan, err := svc.store.LoadPlanMode(id)
			if err != nil {
				t.Fatal(err)
			}
			plan.LinkedGoalID = goal.GoalID
			if err := svc.store.SavePlanMode(id, plan); err != nil {
				t.Fatal(err)
			}
			target := reviewedApprovalPayload(t, svc.store, id, false)
			if testCase.legacy {
				_, err = svc.store.ApprovePlanMode(id, session.PlanModeSourceWeb)
			} else {
				_, err = svc.store.ApprovePlanModeTarget(id, session.PlanModeSourceWeb, target.ApprovalTarget, false)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.store.MarkPlanModeExecuting(id, session.PlanModeSourceWeb); err != nil {
				t.Fatal(err)
			}
			if testCase.changed {
				goal.Mission.Requirements = []session.MissionRequirement{{ID: "new", Text: "Different scope"}}
				if err := svc.store.SaveGoal(id, goal); err != nil {
					t.Fatal(err)
				}
			}
			current := reviewedApprovalPayload(t, svc.store, id, false)
			ts := httptest.NewServer(svc)
			defer ts.Close()
			if testCase.changed {
				postJSONError(t, ts.URL+"/api/sessions/"+id+"/mission/plan/approve", current, http.StatusConflict)
				loaded, err := svc.store.LoadGoal(id)
				if err != nil {
					t.Fatal(err)
				}
				if loaded.Mission.PlanStatus == session.MissionPlanStatusApproved {
					t.Fatal("fact repair approved a different scope")
				}
			} else {
				var loaded session.SessionGoal
				postJSON(t, ts.URL+"/api/sessions/"+id+"/mission/plan/approve", current, http.StatusOK, &loaded)
				wantRevision := target.ExpectedRevision
				if testCase.legacy {
					wantRevision = ""
				}
				if loaded.Mission.ApprovedRevision != wantRevision {
					t.Fatal("matched fact repair lost original revision")
				}
			}
			events, err := svc.store.LoadEvents(id)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if event.Type == "webconsole.handle.acquired" || strings.HasPrefix(event.Type, "provider.") {
					t.Fatal("fact repair launched execution")
				}
			}
		})
	}
}

func TestExecutingMissionFactRepairRequiresReviewedTarget(t *testing.T) {
	for _, testCase := range []struct {
		name                             string
		legacy, missing, coverageBlocked bool
	}{
		{name: "known_same_missing_target", missing: true},
		{name: "legacy_unknown_missing_target", legacy: true, missing: true},
		{name: "uncovered_missing_target", missing: true, coverageBlocked: true},
		{name: "known_same_complete_target"},
		{name: "legacy_unknown_complete_target", legacy: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var providerCalls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				providerCalls.Add(1)
				http.Error(w, "fact repair must not call the provider", http.StatusInternalServerError)
			}))
			svc, id := newApprovalTargetFixtureWithProvider(t, provider)
			draft := session.GoalDraft{Enabled: true, Mode: session.GoalModeMission, Objective: "Linked executing mission"}
			if testCase.coverageBlocked {
				draft.Features = []string{"Uncovered feature"}
				draft.ValidationPlan = []string{"go test"}
			}
			goal, err := svc.store.CreateGoal(id, draft)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := svc.store.LoadPlanMode(id)
			if err != nil {
				t.Fatal(err)
			}
			plan.LinkedGoalID = goal.GoalID
			if err := svc.store.SavePlanMode(id, plan); err != nil {
				t.Fatal(err)
			}
			target := reviewedApprovalPayload(t, svc.store, id, false)
			wantRevision := target.ExpectedRevision
			if testCase.legacy {
				_, err = svc.store.ApprovePlanMode(id, session.PlanModeSourceWeb)
				wantRevision = ""
			} else {
				_, err = svc.store.ApprovePlanModeTarget(id, session.PlanModeSourceWeb, target.ApprovalTarget, testCase.coverageBlocked)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.store.MarkPlanModeExecuting(id, session.PlanModeSourceWeb); err != nil {
				t.Fatal(err)
			}
			capture := func() map[string][]byte {
				t.Helper()
				facts := make(map[string][]byte)
				for _, name := range []string{"goal.json", "artifacts/goal-history.jsonl", "planmode.json", "artifacts/planmode-history.jsonl", "messages.jsonl", "state.json", "events.jsonl"} {
					data, err := os.ReadFile(filepath.Join(svc.store.SessionDir(id), name))
					if err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
					facts[name] = data
				}
				return facts
			}
			before := capture()
			body := `{}`
			if !testCase.missing {
				payload, err := json.Marshal(reviewedApprovalPayload(t, svc.store, id, false))
				if err != nil {
					t.Fatal(err)
				}
				body = string(payload)
			}
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/api/sessions/"+id+"/mission/plan/approve", strings.NewReader(body))
			r.Host = "127.0.0.1"
			r.Header.Set("X-Aegis-Agent-Web", "1")
			r.Header.Set("Content-Type", "application/json")
			svc.ServeHTTP(w, r)
			svc.Close()
			after := capture()
			if providerCalls.Load() != 0 {
				t.Errorf("executing mission fact repair called provider %d times", providerCalls.Load())
			}
			if testCase.missing {
				if w.Code != http.StatusBadRequest {
					t.Errorf("missing executing review target must return 400: got %d %s", w.Code, w.Body)
				}
				if !strings.Contains(w.Body.String(), session.ErrMissingApprovalTarget.Error()) {
					t.Error("missing target response did not request reload/upgrade")
				}
				for name, data := range before {
					if !bytes.Equal(data, after[name]) {
						t.Errorf("missing target changed durable %s", name)
					}
				}
				return
			}
			if w.Code != http.StatusOK {
				t.Fatalf("complete executing review target must retain 200 fact repair: got %d %s", w.Code, w.Body)
			}
			var approved session.SessionGoal
			if err := json.Unmarshal(w.Body.Bytes(), &approved); err != nil {
				t.Fatal(err)
			}
			if approved.Mission == nil || approved.Mission.PlanStatus != session.MissionPlanStatusApproved || approved.Mission.ApprovedRevision != wantRevision {
				t.Fatalf("complete target lost original historical revision: goal=%#v want=%s", approved, wantRevision)
			}
			for _, name := range []string{"planmode.json", "artifacts/planmode-history.jsonl", "messages.jsonl", "state.json"} {
				if !bytes.Equal(before[name], after[name]) {
					t.Errorf("fact repair changed prior approval/execution durable %s", name)
				}
			}
		})
	}
}

func TestLegacyApprovalReaperAmbiguityRequiresExplicitStop(t *testing.T) {
	for _, reason := range []string{queueReaperPauseReason, "manual_stop"} {
		t.Run(reason, func(t *testing.T) {
			svc, id := newApprovalTargetFixture(t)
			runner := runtime.NewRunner(svc.cfg)
			target := reviewedApprovalPayload(t, svc.store, id, false).ApprovalTarget
			prepared, err := runner.PrepareApprovalContinue(context.Background(), runtime.ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: &target, ApprovalRequestID: "reaper_owner"})
			if err != nil {
				t.Fatal(err)
			}
			defer runner.AbortPreparedApproval(prepared, nil)
			lookup, err := runner.ApprovalReceipt(id, "reaper_owner")
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal(lookup.Receipt.Recovery.Data, &payload); err != nil {
				t.Fatal(err)
			}
			// This temporary fixture represents an old #125 host: only its
			// schema-v1 journal exists, so generation lookup cannot select a
			// modern operation. New production operations never write both.
			journal := payload["preparation"].(map[string]any)
			journal["owner_pid"] = 999999999
			if _, err := svc.store.WriteArtifact(id, "approval-preparation.json", journal); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(svc.store.Root(), id, "approval-operations.json")); err != nil {
				t.Fatal(err)
			}
			state, err := svc.store.LoadState(id)
			if err != nil {
				t.Fatal(err)
			}
			state.Status = session.StatusPaused
			if err := svc.store.SaveState(id, state); err != nil {
				t.Fatal(err)
			}
			current, err := session.NewStore(svc.store.Root()).ClaimSessionRun(id, session.StatusPaused)
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.store.AppendEvent(id, events.New(id, "webconsole.handle.released", "webconsole", map[string]any{"pid": 999999999})); err != nil {
				t.Fatal(err)
			}
			changed, err := svc.reconcileStaleRunningSession(id, reason)
			loaded, loadErr := svc.store.LoadState(id)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if reason == queueReaperPauseReason {
				if changed || err == nil || loaded.Status != session.StatusRunning || loaded.UpdatedAt != current.UpdatedAt {
					t.Fatalf("automatic reaper reclaimed another run: changed=%v err=%v state=%#v", changed, err, loaded)
				}
			} else if err != nil || !changed || loaded.Status != session.StatusPaused {
				t.Fatalf("explicit stop recovery unavailable: changed=%v err=%v state=%#v", changed, err, loaded)
			}
		})
	}
}

func TestApprovalReaperRollbackCannotOverwriteNewClaim(t *testing.T) {
	svc, id := newApprovalTargetFixture(t)
	previous, err := svc.store.ClaimSessionRun(id, session.StatusAwaitingInput)
	if err != nil {
		t.Fatal(err)
	}
	paused := previous
	paused.Status = session.StatusPaused
	committed, saved, err := svc.store.SwapStateIfCurrent(id, previous, paused)
	if err != nil || !saved {
		t.Fatalf("pause: saved=%v err=%v", saved, err)
	}
	current, err := session.NewStore(svc.store.Root()).ClaimSessionRun(id, session.StatusPaused)
	if err != nil {
		t.Fatal(err)
	}
	newEvent := events.New(id, "session.resumed", "prepare", map[string]any{"generation": current.UpdatedAt})
	if err := svc.store.AppendEvent(id, newEvent); err != nil {
		t.Fatal(err)
	}
	if err := svc.restoreStaleRunningSessionReconcile(id, previous, nil, nil, fmt.Errorf("injected event error"), committed); err == nil {
		t.Fatal("stale rollback succeeded")
	}
	loaded, err := svc.store.LoadState(id)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.UpdatedAt != current.UpdatedAt || loaded.Status != session.StatusRunning {
		t.Fatal("rollback changed a later claim")
	}
	list, err := svc.store.LoadEvents(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != newEvent.ID {
		t.Fatal("rollback removed newer run facts")
	}
}
