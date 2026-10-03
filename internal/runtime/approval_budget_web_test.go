package runtime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aegis-agent/internal/config"
	agentruntime "aegis-agent/internal/runtime"
	"aegis-agent/internal/session"
	"aegis-agent/internal/webconsole"
)

const approvedBudgetCriterion = "APPROVED_BUDGET_CRITERION_A"
const unreviewedBudgetCriterion = "UNREVIEWED_BUDGET_CRITERION_B"

func patchBudgetCriterionThroughWeb(t *testing.T, svc *webconsole.Service, id string, criterion session.GoalCriterion, text string) {
	t.Helper()
	criterion.Text = text
	body, err := json.Marshal(map[string]any{"success_criteria": []session.GoalCriterion{criterion}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPatch, "http://localhost/api/sessions/"+id+"/goal", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://localhost")
	response := httptest.NewRecorder()
	svc.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("real Web goal PATCH failed: %d %s", response.Code, response.Body.String())
	}
}

func TestApprovalBudgetWrapUpUsesMatchedWebGoalSnapshot(t *testing.T) {
	for _, change := range []string{"semantic_aba", "budget_accounting"} {
		t.Run(change, func(t *testing.T) {
			requests := make(chan string, 4)
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var payload struct {
					Instructions string `json:"instructions"`
				}
				if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				requests <- payload.Instructions
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"approval-budget-spy","status":"completed","output":[{"type":"function_call","call_id":"budget_wrapup","name":"record_goal_progress","arguments":"{\"kind\":\"budget_wrapup\",\"summary\":\"Budget exhausted; work remains.\"}"}],"usage":{"input_tokens":0,"output_tokens":0}}`))
			}))
			defer provider.Close()
			cfg := config.Default()
			cfg.Session.Dir = t.TempDir()
			cfg.Runtime.Queue.ReaperIntervalMS = 0
			cfg.DefaultProvider = "openai-compatible"
			cfg.Providers["openai-compatible"] = config.Provider{APIProvider: "openai-compatible", APIKeyEnv: "AEGIS_APPROVAL_BUDGET_TEST_KEY", BaseURL: provider.URL + "/v1", Model: "test", TimeoutSec: 3, RequestTimeoutSec: 3, WireAPI: "responses", Retry: config.Retry{MaxAttempts: 1}}
			t.Setenv("AEGIS_APPROVAL_BUDGET_TEST_KEY", "local-test-key")
			runner := agentruntime.NewRunner(cfg)
			store := runner.Store()
			id := session.NewSessionID()
			meta := session.SessionMetadata{SchemaVersion: 1, ID: id, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: t.TempDir(), Mode: session.ModeRun, Provider: cfg.DefaultProvider, Model: "test", CompletionPolicy: session.CompletionPolicyInteractive}
			if err := store.Create(meta, session.State{Status: session.StatusAwaitingInput, Phase: "plan_approval"}); err != nil {
				t.Fatal(err)
			}
			budget := int64(1)
			goal, err := store.CreateGoal(id, session.GoalDraft{Enabled: true, Mode: session.GoalModeGoal, Objective: "Wrap up the reviewed goal", SuccessCriteria: []string{approvedBudgetCriterion}, TokenBudget: &budget, StopOnBudget: true, Source: session.GoalSourceCLI})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.CreatePlanMode(id, session.PlanModeDraft{Enabled: true, Objective: "Reviewed budget wrap-up"}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.MutatePlanMode(id, func(plan *session.PlanModeState) error { plan.LinkedGoalID = goal.GoalID; return nil }); err != nil {
				t.Fatal(err)
			}
			if _, err := store.SubmitPlanMode(id, session.PlanModeSubmitInput{Title: "Reviewed", Summary: "Reviewed wrap-up", PlanMarkdown: "# Reviewed wrap-up", Verification: []string{"tests"}}); err != nil {
				t.Fatal(err)
			}
			if _, limited, err := store.UpdateGoalAccounting(id, session.GoalUsageDelta{TokensUsedDelta: 2, SourceTurn: 1}); err != nil || !limited {
				t.Fatalf("budget-limited fixture failed: limited=%v %v", limited, err)
			}
			snapshot, err := store.LoadApprovalSnapshot(id)
			if err != nil {
				t.Fatal(err)
			}
			target := snapshot.Target()
			prepared, err := runner.PrepareApprovalContinue(context.Background(), agentruntime.ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalRequestID: strings.ReplaceAll(t.Name(), "/", "_"), ApprovalTarget: &target})
			if err != nil {
				t.Fatal(err)
			}
			svc, err := webconsole.New(cfg, webconsole.Options{WorkerCount: 0})
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			beforeCalled, afterCalled := false, false
			agentruntime.SetApprovalBudgetBarriersForTest(runner, func() {
				beforeCalled = true
				if change == "semantic_aba" {
					patchBudgetCriterionThroughWeb(t, svc, id, goal.SuccessCriteria[0], unreviewedBudgetCriterion)
				} else if _, _, err := store.UpdateGoalAccounting(id, session.GoalUsageDelta{TokensUsedDelta: 1, SourceTurn: 1}); err != nil {
					t.Fatal(err)
				}
				current, err := store.LoadApprovalSnapshot(id)
				if err != nil || current.PlanMode.PlanModeID != target.PlanModeID || current.PlanMode.PlanVersion != target.PlanVersion || current.PlanMode.Status != session.PlanModeStatusExecuting {
					t.Fatalf("interleaving replaced the executing plan: %#v %v", current, err)
				}
				if (current.Revision == target.ExpectedRevision) != (change == "budget_accounting") {
					t.Fatalf("interleaving must alter only its intended projection: revision=%s target=%s", current.Revision, target.ExpectedRevision)
				}
			}, func() {
				afterCalled = true
				if change == "semantic_aba" {
					patchBudgetCriterionThroughWeb(t, svc, id, goal.SuccessCriteria[0], approvedBudgetCriterion)
				}
			})
			result, runErr := runner.RunPreparedApproval(context.Background(), prepared)
			// A fixed implementation can reject B before context.loaded. Restore A
			// afterwards through the same real Web API, without another admission.
			if change == "semantic_aba" && !afterCalled {
				patchBudgetCriterionThroughWeb(t, svc, id, goal.SuccessCriteria[0], approvedBudgetCriterion)
			}
			final, err := store.LoadApprovalSnapshot(id)
			if err != nil || final.Revision != target.ExpectedRevision || final.PlanMode.Status != session.PlanModeStatusExecuting {
				t.Fatalf("final durable target is not the original A: %#v %v", final, err)
			}
			calls := len(requests)
			promptContainsA, promptContainsB := false, false
			for len(requests) > 0 {
				prompt := <-requests
				promptContainsA = promptContainsA || strings.Contains(prompt, approvedBudgetCriterion)
				promptContainsB = promptContainsB || strings.Contains(prompt, unreviewedBudgetCriterion)
			}
			t.Logf("actual_transport_calls=%d approved_A=%v unreviewed_B=%v before_budget=%v context_loaded=%v final_revision_matches_A=%v result=%s error=%v", calls, promptContainsA, promptContainsB, beforeCalled, afterCalled, final.Revision == target.ExpectedRevision, result.Status, runErr)
			if !beforeCalled || promptContainsB {
				t.Fatalf("provider received a goal that was not in the matched approval snapshot: B=%v calls=%d", promptContainsB, calls)
			}
			if change == "semantic_aba" && calls == 0 {
				state, err := store.LoadState(id)
				if !errors.Is(runErr, session.ErrApprovalConflict) || result.Status != session.StatusAwaitingInput || err != nil || state.Phase != "plan_approval" || state.Status != session.StatusAwaitingInput {
					t.Fatalf("changed goal must request approval review without failing session: %#v %v state=%#v load=%v", result, runErr, state, err)
				}
			} else if runErr != nil || result.Status != session.StatusAwaitingInput || calls != 1 || !promptContainsA || !afterCalled || final.Goal.BudgetWrapUpTurnStartedAt == "" {
				t.Fatalf("matched budget wrap-up was not admitted using A: %#v %v calls=%d", result, runErr, calls)
			}
		})
	}
}
