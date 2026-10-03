package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"aegis-agent/internal/runtime"
	"aegis-agent/internal/session"
	"aegis-agent/internal/webconsole"
	sdk "aegis-agent/pkg/agent"
)

// Read every durable JSON/JSONL fact, including bindings, history and handle
// events. Coordination lock files are observations, not execution facts.
func approvalEntrypointFacts(t *testing.T, root string) map[string]string {
	t.Helper()
	facts := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || (!strings.HasSuffix(path, ".json") && !strings.HasSuffix(path, ".jsonl")) {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		facts[rel] = string(raw)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return facts
}

func TestApprovalRecoveryFreshTargetAcrossExecutionEntrypoints(t *testing.T) {
	for _, entry := range []string{"core_continue", "core_prepare", "sdk_continue", "sdk_prepare", "sdk_prepare_compat", "cli_continue", "cli_latest", "cli_mission", "cli_mission_latest", "web_plan", "web_mission"} {
		for _, damage := range []string{"control", "provider_resume_count", "pause_reason", "prepared_generation"} {
			t.Run(entry+"/"+damage, func(t *testing.T) {
				store, id, cfg, calls := cliReceiptFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				runner := sdk.New(cfg)
				first, err := store.LoadApprovalSnapshot(id)
				if err != nil {
					t.Fatal(err)
				}
				firstTarget := first.Target()
				old := sdk.ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: &firstTarget, ApprovalRequestID: "old-operation"}
				prepared, err := runner.PrepareApprovalOperation(ctx, old)
				if err != nil || prepared.Prepared == nil {
					t.Fatalf("real preparation: %v", err)
				}
				if _, err := runner.Steer(ctx, sdk.SteerRequest{SessionID: id, Message: "Legal queued steer before scope-change CAS"}); err != nil {
					t.Fatal(err)
				}
				if _, _, err := store.MutatePlanMode(id, func(plan *session.PlanModeState) error {
					plan.PlanVersion++
					plan.Status = session.PlanModeStatusAwaitingApproval
					plan.PlanMarkdown = "Fresh reviewed target after scope change"
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if _, err := runner.RunPreparedApproval(ctx, prepared.Prepared); !errors.Is(err, session.ErrApprovalConflict) || calls.Load() != 0 {
					t.Fatalf("actual review-required producer: %v calls=%d", err, calls.Load())
				}
				prior, err := runner.ApprovalReceipt(id, old.ApprovalRequestID)
				if err != nil || prior.Receipt.Phase != "review_required" {
					t.Fatalf("actual old operation: %v", err)
				}
				// The linked controls use the real awaiting-approval execution
				// branch. Complete its empty goal through the public Store API so
				// the fixed finish provider does not hit the separate goal gate.
				if strings.Contains(entry, "mission") {
					goal, err := store.CreateGoal(id, session.GoalDraft{Enabled: true, Mode: session.GoalModeMission, Objective: "Empty completed goal for linked execution control", RequirePlanApproval: true})
					if err != nil {
						t.Fatal(err)
					}
					if _, err := store.SetGoalStatus(id, session.GoalStatusComplete, session.PlanModeSourceCLI); err != nil {
						t.Fatal(err)
					}
					if _, _, err := store.MutatePlanMode(id, func(plan *session.PlanModeState) error { plan.LinkedGoalID = goal.GoalID; return nil }); err != nil {
						t.Fatal(err)
					}
				}
				snapshot, err := store.LoadApprovalSnapshot(id)
				if err != nil {
					t.Fatal(err)
				}
				target := snapshot.Target()
				if target == firstTarget || target.PlanVersion <= firstTarget.PlanVersion || snapshot.PlanMode.Status != session.PlanModeStatusAwaitingApproval {
					t.Fatal("fixture did not create a fresh approvable target")
				}
				path := filepath.Join(store.Root(), id, "approval-operations.json")
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
				case "pause_reason":
					captured["pause_reason"] = "invented pause"
				case "prepared_generation":
					captured["run_generation"] = "run_other"
				}
				raw, err = json.Marshal(ledger)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				request := sdk.ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: &target, ApprovalRequestID: "new-operation"}
				var svc *webconsole.Service
				if strings.HasPrefix(entry, "web_") {
					svc, err = webconsole.New(cfg, webconsole.Options{WorkerCount: 0})
					if err != nil {
						t.Fatal(err)
					}
					defer svc.Close()
				}
				before := approvalEntrypointFacts(t, filepath.Join(store.Root(), id))
				switch entry {
				case "core_continue":
					_, err = runtime.NewCoreRunner(cfg).Continue(ctx, request)
				case "core_prepare":
					core := runtime.NewCoreRunner(cfg)
					p, prepareErr := core.PrepareApprovalOperation(ctx, request)
					err = prepareErr
					if p.Prepared != nil {
						if damage != "control" {
							t.Error("corrupt receipt returned an executable prepared runner")
						}
						_, err = core.RunPreparedApproval(ctx, p.Prepared)
					}
				case "sdk_continue":
					_, err = runner.Continue(ctx, request)
				case "sdk_prepare":
					p, prepareErr := runner.PrepareApprovalOperation(ctx, request)
					err = prepareErr
					if p.Prepared != nil {
						if damage != "control" {
							t.Error("corrupt receipt returned an executable prepared runner")
						}
						_, err = runner.RunPreparedApproval(ctx, p.Prepared)
					}
				case "sdk_prepare_compat":
					p, prepareErr := runner.PrepareApprovalContinue(ctx, request)
					err = prepareErr
					if p != nil {
						if damage != "control" {
							t.Error("corrupt receipt returned an executable prepared runner")
						}
						_, err = runner.RunPreparedApproval(ctx, p)
					}
				case "cli_continue", "cli_latest", "cli_mission", "cli_mission_latest":
					args := cliReceiptArgs(id, request.ApprovalRequestID, target)
					if strings.HasSuffix(entry, "latest") {
						args = []string{"continue", id, "--approve-latest", "--approval-request-id", request.ApprovalRequestID, "--json"}
					}
					if strings.Contains(entry, "mission") {
						if entry == "cli_mission_latest" {
							args = append([]string{"goal", "plan", "approve", id}, args[2:]...)
						} else {
							args = append([]string{"goal", "plan", "approve", id}, args[3:]...)
						}
					}
					err = Run(ctx, args, io.Discard, io.Discard)
				case "web_plan", "web_mission":
					body, marshalErr := json.Marshal(map[string]any{"approval_request_id": request.ApprovalRequestID, "plan_mode_id": target.PlanModeID, "plan_version": target.PlanVersion, "expected_revision": target.ExpectedRevision})
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					suffix := "planmode/approve"
					if entry == "web_mission" {
						suffix = "mission/plan/approve"
					}
					r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/sessions/"+id+"/"+suffix, bytes.NewReader(body))
					r.Host = "127.0.0.1"
					r.Header.Set("X-Aegis-Agent-Web", "1")
					r.Header.Set("Content-Type", "application/json")
					w := httptest.NewRecorder()
					svc.ServeHTTP(w, r)
					if damage != "control" {
						if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "APPROVAL_RECOVERY_REQUIRED") {
							t.Fatalf("corrupt real Web entry: status=%d body=%s calls=%d", w.Code, w.Body, calls.Load())
						}
						err = session.ErrApprovalReceiptUnverifiable
					} else {
						if w.Code != http.StatusAccepted {
							t.Fatalf("lawful Web admission: status=%d body=%s", w.Code, w.Body)
						}
						for {
							state, loadErr := store.LoadState(id)
							if loadErr != nil {
								t.Fatal(loadErr)
							}
							if state.Status == session.StatusCompleted {
								break
							}
							select {
							case <-ctx.Done():
								t.Fatalf("control run did not complete: %#v", state)
							default:
								time.Sleep(10 * time.Millisecond)
							}
						}
					}
				}
				after := approvalEntrypointFacts(t, filepath.Join(store.Root(), id))
				t.Logf("entry=%s damage=%s old_phase=review_required fresh_version=%d new_id=%s error=%v provider_calls=%d facts_unchanged=%v", entry, damage, target.PlanVersion, request.ApprovalRequestID, err, calls.Load(), reflect.DeepEqual(before, after))
				if damage == "control" {
					if err != nil || calls.Load() != 1 {
						t.Fatalf("lawful actual execution: error=%v calls=%d", err, calls.Load())
					}
					lookup, err := runner.ApprovalReceipt(id, request.ApprovalRequestID)
					if err != nil || !lookup.Found || lookup.Receipt.Stage != session.ApprovalReceiptAdmitted {
						t.Fatalf("lawful actual admission receipt: %v %#v", err, lookup)
					}
				} else if !errors.Is(err, session.ErrApprovalReceiptUnverifiable) || calls.Load() != 0 || !reflect.DeepEqual(before, after) {
					t.Fatalf("corrupt old receipt bypassed actual entry: error=%v calls=%d facts_changed=%v", err, calls.Load(), !reflect.DeepEqual(before, after))
				}
			})
		}
	}
}
