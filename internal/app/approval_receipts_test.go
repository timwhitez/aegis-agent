package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"aegis-agent/internal/config"
	"aegis-agent/internal/runtime"
	"aegis-agent/internal/session"
	sdk "aegis-agent/pkg/agent"
)

func cliReceiptFixture(t *testing.T) (*session.Store, string, *config.Config, *atomic.Int32) {
	t.Helper()
	store, id := cliApprovalFixture(t)
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cli_receipt_mock","status":"completed","output":[{"type":"function_call","call_id":"cli_finish_receipt","name":"finish","arguments":"{\"message\":\"done\"}"}],"usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	t.Cleanup(server.Close)
	cfg := config.Default()
	cfg.Session.Dir = store.Root()
	cfg.Skills.Dirs = nil
	provider := cfg.Providers["openai"]
	provider.BaseURL, provider.APIKeyEnv, provider.Model = server.URL, "", "gpt-5.4"
	cfg.Providers["openai"] = provider
	restoreRunner, restoreStore, restoreTTY := runnerLoader, storeRunnerLoader, stdinIsTerminal
	runnerLoader = func(string, string) (coreRunner, *config.Config, error) { return runtime.NewCoreRunner(cfg), cfg, nil }
	storeRunnerLoader = func(string, string) (storeRunner, *config.Config, error) { return runtime.NewStoreView(cfg), cfg, nil }
	stdinIsTerminal = func() bool { return false }
	t.Cleanup(func() { runnerLoader, storeRunnerLoader, stdinIsTerminal = restoreRunner, restoreStore, restoreTTY })
	return store, id, cfg, calls
}

func cliReceiptArgs(id, requestID string, target session.ApprovalTarget) []string {
	return []string{"continue", id, "--approve-plan", "--json", "--approval-request-id", requestID, "--plan-mode-id", target.PlanModeID, "--plan-version", strconv.Itoa(target.PlanVersion), "--expected-revision", target.ExpectedRevision}
}

func TestCLIApprovalReceiptReplayAndQuery(t *testing.T) {
	store, id, cfg, calls := cliReceiptFixture(t)
	snapshot, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	target := snapshot.Target()
	control, err := runtime.NewCoreRunner(cfg).Continue(context.Background(), runtime.ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: &target, ApprovalRequestID: "cli-original"})
	if err != nil || control.Status != session.StatusCompleted || calls.Load() != 1 {
		t.Fatalf("actual local admission control: %#v %v calls=%d", control, err, calls.Load())
	}
	delete(cfg.Providers, cfg.DefaultProvider)
	for _, args := range [][]string{cliReceiptArgs(id, "cli-original", target), {"continue", id, "--approval-receipt", "--approval-request-id", "cli-original", "--json"}} {
		var stdout, stderr bytes.Buffer
		err := Run(context.Background(), args, &stdout, &stderr)
		t.Logf("args=%v error=%v provider_calls=%d stdout=%s stderr=%s", args, err, calls.Load(), stdout.String(), stderr.String())
		if err != nil || !strings.Contains(stdout.String(), `"approval"`) || !strings.Contains(stdout.String(), `"current_state"`) || !strings.Contains(stdout.String(), `"admitted"`) {
			t.Errorf("CLI lost receipt outcome: error=%v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
	}
	if calls.Load() != 1 {
		t.Fatal("CLI receipt replay/query started another provider")
	}
}

func TestCLIApprovalReceiptLatestCapturesIdentity(t *testing.T) {
	store, id, _, calls := cliReceiptFixture(t)
	snapshot, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err = Run(context.Background(), []string{"continue", id, "--approve-latest", "--approval-request-id", "cli-latest", "--json"}, &stdout, &stderr)
	t.Logf("latest error=%v calls=%d stdout=%s stderr=%s", err, calls.Load(), stdout.String(), stderr.String())
	if err != nil || calls.Load() != 1 || !strings.Contains(stderr.String(), "cli-latest") || !strings.Contains(stderr.String(), snapshot.Revision) {
		t.Fatalf("latest identity capture: %v calls=%d stderr=%s", err, calls.Load(), stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	var result map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &result); err != nil || result["approval"] == nil {
		t.Fatalf("latest receipt result: %v %s", err, stdout.String())
	}
}

func TestCLIApprovalReceiptLatestRetryAndLinkedAliasUseOriginalBinding(t *testing.T) {
	store, id, cfg, calls := cliReceiptFixture(t)
	snapshot, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	target := snapshot.Target()
	if _, err := runtime.NewCoreRunner(cfg).Continue(context.Background(), runtime.ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: &target, ApprovalRequestID: "original-latest"}); err != nil {
		t.Fatal(err)
	}
	before, _ := store.LoadState(id)
	beforeEvents, _ := store.LoadEvents(id)
	beforeMessages, _ := store.LoadMessages(id)
	if _, _, err := store.MutatePlanMode(id, func(plan *session.PlanModeState) error { plan.Objective = "A later unapproved objective"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Root(), id, "goal.json"), []byte("broken current goal"), 0600); err != nil {
		t.Fatal(err)
	}
	delete(cfg.Providers, cfg.DefaultProvider)
	for _, args := range [][]string{
		{"continue", id, "--approve-latest", "--approval-request-id", "original-latest", "--json"},
		{"goal", "plan", "approve", id, "--approval-request-id", "original-latest", "--approve-latest", "--json"},
		{"goal", "plan", "approve", id, "--approval-request-id", "linked-alias", "--plan-mode-id", target.PlanModeID, "--plan-version", "1", "--expected-revision", target.ExpectedRevision, "--json"},
		{"goal", "plan", "approve", id, "--approval-receipt", "--approval-request-id", "linked-alias", "--json"},
	} {
		var stdout, stderr bytes.Buffer
		err := Run(context.Background(), args, &stdout, &stderr)
		if err != nil || !strings.Contains(stdout.String(), `"stage":"admitted"`) || !strings.Contains(stdout.String(), target.ExpectedRevision) || strings.Contains(stderr.String(), "Captured approval operation") {
			t.Fatalf("retry recaptured latest/current goal: args=%v err=%v stdout=%s stderr=%s", args, err, stdout.String(), stderr.String())
		}
	}
	changedArgs := append(cliReceiptArgs(id, "original-latest", target), "--message", "changed explicit parameters")
	if err := Run(context.Background(), changedArgs, io.Discard, io.Discard); !errors.Is(err, session.ErrApprovalRequestConflict) {
		t.Fatalf("same identity changed parameters: %v", err)
	}
	after, _ := store.LoadState(id)
	afterEvents, _ := store.LoadEvents(id)
	afterMessages, _ := store.LoadMessages(id)
	if calls.Load() != 1 || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeEvents, afterEvents) || !reflect.DeepEqual(beforeMessages, afterMessages) {
		t.Fatal("CLI alias/retry changed execution facts")
	}
	t.Logf("latest retry, linked aliases, receipt query: provider_calls=%d generation=%s unchanged; changed params conflict", calls.Load(), after.RunGeneration)
}

func TestCLIApprovalReceiptMissingIDAndOrdinaryContinueControl(t *testing.T) {
	store, id, _, calls := cliReceiptFixture(t)
	snapshot, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	target := snapshot.Target()
	missing := cliReceiptArgs(id, "", target)
	if err := Run(context.Background(), missing, io.Discard, io.Discard); !errors.Is(err, session.ErrMissingApprovalRequestID) || calls.Load() != 0 {
		t.Fatalf("missing ID did not require upgrade before run: %v calls=%d", err, calls.Load())
	}
	if err := Run(context.Background(), cliReceiptArgs(id, "ordinary-approved", target), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"continue", id, "--message", "Explicit ordinary follow-up", "--json"}, &stdout, io.Discard); err != nil || calls.Load() != 2 {
		t.Fatalf("ordinary continue changed: %v calls=%d output=%s", err, calls.Load(), stdout.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if strings.Contains(lines[len(lines)-1], `"approval"`) {
		t.Fatal("ordinary continue acquired an approval receipt")
	}
	t.Logf("missing ID calls=0; explicit approval then ordinary follow-up calls=%d", calls.Load())
}

func TestCLIApprovalReceiptCoverageRejectionAndRecoveryExit(t *testing.T) {
	store, id, _, calls := cliReceiptFixture(t)
	goal, err := store.CreateGoal(id, session.GoalDraft{Enabled: true, Mode: session.GoalModeMission, Objective: "Covered mission", ValidationPlan: []string{"true"}, Features: []string{"required feature"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.MutatePlanMode(id, func(plan *session.PlanModeState) error { plan.LinkedGoalID = goal.GoalID; return nil }); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	target := snapshot.Target()
	args := []string{"goal", "plan", "approve", id, "--approval-request-id", "coverage-rejected", "--plan-mode-id", target.PlanModeID, "--plan-version", "1", "--expected-revision", target.ExpectedRevision, "--json"}
	for _, input := range [][]string{args, cliReceiptArgs(id, "coverage-rejected", target)} {
		var stdout bytes.Buffer
		err := Run(context.Background(), input, &stdout, io.Discard)
		if err == nil || !strings.Contains(stdout.String(), `"stage":"rejected"`) || !strings.Contains(stdout.String(), `"exit_code":1`) || calls.Load() != 0 {
			t.Fatalf("rejected operation looked successful or was not bound: %v output=%s calls=%d", err, stdout.String(), calls.Load())
		}
	}
	changed := append(cliReceiptArgs(id, "coverage-rejected", target), "--override-goal-coverage")
	if err := Run(context.Background(), changed, io.Discard, io.Discard); !errors.Is(err, session.ErrApprovalRequestConflict) {
		t.Fatalf("override reused rejected ID: %v", err)
	}
	t.Logf("linked CLI rejection bound, replay rejected exit1, same ID override conflict; provider_calls=%d", calls.Load())
}

func TestCLIApprovalReceiptWholeTypedLedgerGate(t *testing.T) {
	store, id, cfg, calls := cliReceiptFixture(t)
	snapshot, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	target := snapshot.Target()
	if _, err := runtime.NewCoreRunner(cfg).Continue(context.Background(), runtime.ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: &target, ApprovalRequestID: "corrupt-canonical"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePlanMode(id, session.PlanModeDraft{Enabled: true, Objective: "next reviewed operation", Source: session.PlanModeSourceCLI}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SubmitPlanMode(id, session.PlanModeSubmitInput{Title: "Second", Summary: "Second scope", PlanMarkdown: "# Second plan", Verification: []string{"check"}}); err != nil {
		t.Fatal(err)
	}
	snapshot, err = store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	target = snapshot.Target()
	if _, err := runtime.NewCoreRunner(cfg).Continue(context.Background(), runtime.ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: &target, ApprovalRequestID: "valid-canonical"}); err != nil {
		t.Fatal(err)
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
	ledger["operations"].(map[string]any)["corrupt-canonical"].(map[string]any)["recovery"].(map[string]any)["data"].(map[string]any)["complete"] = false
	raw, _ = json.Marshal(ledger)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetApprovalReceipt(id, "corrupt-canonical"); err != nil {
		t.Fatalf("fixture invalidated Store envelope: %v", err)
	}
	for _, args := range [][]string{cliReceiptArgs(id, "valid-canonical", target), cliReceiptArgs(id, "new-alias", target), {"continue", id, "--approval-receipt", "--approval-request-id", "valid-canonical", "--json"}, {"continue", id, "--approve-latest", "--approval-request-id", "new-latest", "--json"}} {
		if err := Run(context.Background(), args, io.Discard, io.Discard); !errors.Is(err, session.ErrApprovalReceiptUnverifiable) || calls.Load() != 2 {
			t.Fatalf("CLI bypassed unrelated typed payload: %v calls=%d args=%v", err, calls.Load(), args)
		}
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(raw, after) {
		t.Fatal("failed CLI typed validation changed ledger")
	}
}

func TestCLIApprovalReceiptPreparedRecoveryIsNotSuccessfulExecution(t *testing.T) {
	store, id, cfg, calls := cliReceiptFixture(t)
	snapshot, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	target := snapshot.Target()
	prepared, err := runtime.NewCoreRunner(cfg).PrepareApprovalOperation(context.Background(), runtime.ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: &target, ApprovalRequestID: "prepared-recovery", Provider: "missing-provider"})
	if err == nil || !prepared.Lookup.Found || prepared.Lookup.Receipt.Stage != session.ApprovalReceiptPrepared {
		t.Fatalf("actual partial prepare fixture: %#v %v", prepared, err)
	}
	if _, _, err := store.MutatePlanMode(id, func(plan *session.PlanModeState) error { plan.Objective = "Changed scope"; return nil }); err != nil {
		t.Fatal(err)
	}
	args := append(cliReceiptArgs(id, "prepared-recovery", target), "--provider", "missing-provider")
	var stdout bytes.Buffer
	err = Run(context.Background(), args, &stdout, io.Discard)
	var exit ExitError
	if !errors.As(err, &exit) || exit.Code != 1 || !strings.Contains(stdout.String(), `"recovery_required":true`) || !strings.Contains(stdout.String(), `"stage":"prepared"`) || calls.Load() != 0 {
		t.Fatalf("prepared recovery looked like an admitted run: %v %s calls=%d", err, stdout.String(), calls.Load())
	}
}

func TestCLIApprovalReceiptSDKCanonicalAlias(t *testing.T) {
	store, id, cfg, calls := cliReceiptFixture(t)
	snapshot, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	target := snapshot.Target()
	temperature := 0.2
	if _, err := sdk.New(cfg).Continue(context.Background(), sdk.ContinueRequest{SessionID: id, ApprovePlan: true, ApprovalTarget: &target, ApprovalRequestID: "SDK-canonical", ProviderOptions: session.ProviderOptions{Temperature: &temperature}}); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := Run(context.Background(), cliReceiptArgs(id, "CLI-alias", target), &stdout, io.Discard); err != nil || calls.Load() != 1 {
		t.Fatalf("CLI readmitted SDK canonical: %v calls=%d output=%s", err, calls.Load(), stdout.String())
	}
	lookup, err := sdk.New(cfg).ApprovalReceipt(id, "CLI-alias")
	if err != nil {
		t.Fatal(err)
	}
	if lookup.Receipt.OperationID != "SDK-canonical" || lookup.Binding.Parameters.ProviderOptions.Temperature != nil || lookup.Receipt.Parameters.ProviderOptions.Temperature == nil || *lookup.Receipt.Parameters.ProviderOptions.Temperature != temperature {
		t.Fatalf("SDK/CLI alias collapsed requested/canonical parameters: %#v", lookup)
	}
	t.Logf("SDK actual admission + CLI new alias: canonical=%s provider_calls=%d", lookup.Receipt.OperationID, calls.Load())
}

func TestCLIApprovalReceiptStaleTargetKeepsCurrentSessionIdentity(t *testing.T) {
	store, id, _, calls := cliReceiptFixture(t)
	snapshot, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.MutatePlanMode(id, func(plan *session.PlanModeState) error { plan.Objective = "Changed current scope"; return nil }); err != nil {
		t.Fatal(err)
	}
	before, _ := store.LoadState(id)
	var stdout bytes.Buffer
	err = Run(context.Background(), cliReceiptArgs(id, "stale-request", snapshot.Target()), &stdout, io.Discard)
	var result struct {
		SessionID string         `json:"session_id"`
		Current   *session.State `json:"current_state"`
		ExitCode  int            `json:"exit_code"`
	}
	if decode := json.Unmarshal(stdout.Bytes(), &result); decode != nil {
		t.Fatal(decode)
	}
	t.Logf("stale target error=%v provider_calls=%d session=%q current=%#v", err, calls.Load(), result.SessionID, result.Current)
	if !errors.Is(err, session.ErrApprovalConflict) || result.SessionID != id || result.Current == nil || !reflect.DeepEqual(*result.Current, before) || result.ExitCode != 1 || calls.Load() != 0 {
		t.Fatalf("stale CLI result lost requested session/current facts: %v %s", err, stdout.String())
	}
}
