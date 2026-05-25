package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go-cli-agent/internal/events"
	"go-cli-agent/internal/session"
)

func TestSessionContractTracksRequiredArtifactAndCompletionGate(t *testing.T) {
	store, meta := newRuntimeTestSession(t)
	message := session.NewMessage("user", "Write reports/final.md with the final implementation summary.")
	if err := store.AppendMessage(meta.ID, message); err != nil {
		t.Fatalf("append message: %v", err)
	}
	if err := refreshContractForSession(store, nil, meta); err != nil {
		t.Fatalf("refresh contract: %v", err)
	}

	contract, err := store.LoadContract(meta.ID)
	if err != nil {
		t.Fatalf("load contract: %v", err)
	}
	if contract.SchemaVersion != 1 {
		t.Fatalf("expected schema version 1, got %#v", contract)
	}
	if len(contract.RequiredArtifacts) != 1 {
		t.Fatalf("expected one required artifact, got %#v", contract.RequiredArtifacts)
	}
	wantPath := filepath.Join(meta.Workdir, "reports", "final.md")
	if contract.RequiredArtifacts[0].Path != wantPath {
		t.Fatalf("expected artifact path %q, got %q", wantPath, contract.RequiredArtifacts[0].Path)
	}
	if !containsString(contract.CompletionGates, "required_artifact") {
		t.Fatalf("expected required_artifact gate, got %#v", contract.CompletionGates)
	}

	tracker, err := store.LoadArtifactTracker(meta.ID)
	if err != nil {
		t.Fatalf("load tracker: %v", err)
	}
	if len(tracker) != 1 || tracker[0].Path != wantPath {
		t.Fatalf("expected tracker to mirror contract artifact, got %#v", tracker)
	}

	summary, err := os.ReadFile(filepath.Join(store.SessionDir(meta.ID), "session.md"))
	if err != nil {
		t.Fatalf("read session summary: %v", err)
	}
	if !strings.Contains(string(summary), "reports/final.md") || !strings.Contains(string(summary), "Contract") {
		t.Fatalf("expected session summary to include contract artifact, got:\n%s", string(summary))
	}
}

func TestContractRefreshEmitsArtifactRequiredEvent(t *testing.T) {
	store, meta := newRuntimeTestSession(t)
	if err := store.AppendMessage(meta.ID, session.NewMessage("user", "Write reports/final.md with the final implementation summary.")); err != nil {
		t.Fatalf("append message: %v", err)
	}
	var events []string
	if err := refreshContractForSession(store, func(eventType string, data map[string]any) {
		events = append(events, eventType)
		if eventType == "artifact.required" && data["count"] != 1 {
			t.Fatalf("expected one required artifact event, got %#v", data)
		}
	}, meta); err != nil {
		t.Fatalf("refresh contract: %v", err)
	}
	if !containsString(events, "contract.created") || !containsString(events, "artifact.required") {
		t.Fatalf("expected contract.created and artifact.required events, got %#v", events)
	}
}

func TestContractRefreshReportsHistoryAppendError(t *testing.T) {
	store, meta := newRuntimeTestSession(t)
	if err := store.AppendMessage(meta.ID, session.NewMessage("user", "Write reports/final.md with the final implementation summary.")); err != nil {
		t.Fatalf("append message: %v", err)
	}
	blockRuntimeContractHistoryPath(t, store, meta.ID)

	err := refreshContractForSession(store, nil, meta)
	if err == nil || !strings.Contains(err.Error(), "contract-history.jsonl") {
		t.Fatalf("expected contract history append error, got %v", err)
	}
}

func TestCompletionControllerRequiresSessionTouchedArtifact(t *testing.T) {
	store, meta := newRuntimeTestSession(t)
	artifactPath := filepath.Join(meta.Workdir, "reports", "final.md")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o700); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	if err := os.WriteFile(artifactPath, []byte("old content"), 0o600); err != nil {
		t.Fatalf("write baseline artifact: %v", err)
	}
	if err := store.AppendMessage(meta.ID, session.NewMessage("user", "Write reports/final.md with the final implementation summary.")); err != nil {
		t.Fatalf("append message: %v", err)
	}
	if err := refreshContractForSession(store, nil, meta); err != nil {
		t.Fatalf("refresh contract: %v", err)
	}

	var events []string
	controller := NewCompletionController(store, meta.ID, meta.Workdir, false, func(eventType string, _ map[string]any) {
		events = append(events, eventType)
	})
	kind, text := controller.requiredArtifactGate("finish")
	if kind != "required_artifact" || !strings.Contains(text, "not touched or changed") {
		t.Fatalf("expected stale artifact block, kind=%q text=%q", kind, text)
	}

	if err := os.WriteFile(artifactPath, []byte("new content"), 0o600); err != nil {
		t.Fatalf("update artifact: %v", err)
	}
	controller.TrackToolResult("write_file", session.ToolResult{
		Name:          "write_file",
		LLMOutput:     "wrote reports/final.md",
		DisplayOutput: "wrote reports/final.md",
		Metadata:      map[string]any{"path": artifactPath},
	}, 2)
	kind, text = controller.requiredArtifactGate("finish")
	if kind != "" || text != "" {
		t.Fatalf("expected artifact gate to pass after tracked write, kind=%q text=%q", kind, text)
	}
	if !containsString(events, "artifact.tracked") || !containsString(events, "artifact.gate.passed") {
		t.Fatalf("expected artifact tracking and pass events, got %#v", events)
	}
}

func TestContractRefreshResetsArtifactFreshnessForSamePathNewInstruction(t *testing.T) {
	store, meta := newRuntimeTestSession(t)
	artifactPath := filepath.Join(meta.Workdir, "reports", "final.md")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o700); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	firstInstruction := session.NewMessage("user", "Write reports/final.md with the final implementation summary.")
	if err := store.AppendMessage(meta.ID, firstInstruction); err != nil {
		t.Fatalf("append first instruction: %v", err)
	}
	if err := refreshContractForSession(store, nil, meta); err != nil {
		t.Fatalf("refresh first contract: %v", err)
	}
	if err := os.WriteFile(artifactPath, []byte("initial summary"), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	controller := NewCompletionController(store, meta.ID, meta.Workdir, false, nil)
	controller.TrackToolResult("write_file", session.ToolResult{
		Name:          "write_file",
		LLMOutput:     "wrote reports/final.md",
		DisplayOutput: "wrote reports/final.md",
		Metadata:      map[string]any{"path": artifactPath},
	}, 2)
	if kind, text := controller.requiredArtifactGate("finish"); kind != "" || text != "" {
		t.Fatalf("expected first artifact write to satisfy gate, kind=%q text=%q", kind, text)
	}

	secondInstruction := session.NewMessage("user", "Write reports/final.md again with additional validation notes before finish.")
	if err := store.AppendMessage(meta.ID, secondInstruction); err != nil {
		t.Fatalf("append second instruction: %v", err)
	}
	if err := refreshContractForSession(store, nil, meta); err != nil {
		t.Fatalf("refresh second contract: %v", err)
	}
	contract, err := store.LoadContract(meta.ID)
	if err != nil {
		t.Fatalf("load refreshed contract: %v", err)
	}
	if contract.SourceMessageID != secondInstruction.ID {
		t.Fatalf("expected contract to track latest instruction %q, got %#v", secondInstruction.ID, contract)
	}
	tracker, err := store.LoadArtifactTracker(meta.ID)
	if err != nil {
		t.Fatalf("load tracker: %v", err)
	}
	if len(tracker) != 1 || tracker[0].Status.TouchedBySession {
		t.Fatalf("expected latest same-path instruction to reset artifact freshness, got %#v", tracker)
	}
	if kind, text := controller.requiredArtifactGate("finish"); kind != "required_artifact" || !strings.Contains(text, "not touched or changed") {
		t.Fatalf("expected stale artifact block after latest same-path instruction, kind=%q text=%q", kind, text)
	}
}

func TestRequiredArtifactGateRejectsSymlinkedArtifactAfterContractCreation(t *testing.T) {
	store, meta := newRuntimeTestSession(t)
	if err := store.AppendMessage(meta.ID, session.NewMessage("user", "Write reports/final.md with the final implementation summary.")); err != nil {
		t.Fatalf("append message: %v", err)
	}
	if err := refreshContractForSession(store, nil, meta); err != nil {
		t.Fatalf("refresh contract: %v", err)
	}
	artifactPath := filepath.Join(meta.Workdir, "reports", "final.md")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o700); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside-final.md")
	if err := os.WriteFile(outside, []byte("outside final"), 0o600); err != nil {
		t.Fatalf("write outside artifact: %v", err)
	}
	if err := os.Symlink(outside, artifactPath); err != nil {
		t.Fatalf("symlink artifact: %v", err)
	}

	controller := NewCompletionController(store, meta.ID, meta.Workdir, false, nil)
	kind, text := controller.requiredArtifactGate("finish")
	if kind != "required_artifact" || !strings.Contains(text, "missing reports/final.md") {
		t.Fatalf("expected symlinked artifact to remain missing, kind=%q text=%q", kind, text)
	}
}

func TestProviderAttemptsLedgerAndLongRunCheckpointAreDurable(t *testing.T) {
	store, meta := newRuntimeTestSession(t)
	meta.ParentSessionID = "parent-session"
	if err := store.SaveMetadata(meta.ID, meta); err != nil {
		t.Fatalf("save metadata: %v", err)
	}
	if err := store.SaveState(meta.ID, session.State{
		Status:    session.StatusRunning,
		Phase:     "provider",
		LastError: "temporary timeout",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("save state: %v", err)
	}

	if err := store.AppendProviderAttempt(meta.ID, session.ProviderAttempt{
		Outcome:   "retry",
		Provider:  "openai",
		Model:     "gpt-5.4",
		Attempt:   1,
		Error:     "temporary timeout",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("append provider attempt: %v", err)
	}
	if err := store.AppendProviderAttempt(meta.ID, session.ProviderAttempt{
		Outcome:                  "success",
		Provider:                 "openai",
		Model:                    "gpt-5.4",
		Attempt:                  2,
		ProviderResponseID:       "resp_cache_test",
		CacheCreationInputTokens: 11,
		CacheReadInputTokens:     23,
		CreatedAt:                time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("append provider attempt: %v", err)
	}
	if err := writeSessionSummary(store, meta.ID); err != nil {
		t.Fatalf("write summary: %v", err)
	}
	if err := writeLongRunCheckpoint(store, meta.ID); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}

	attempts, err := store.LoadProviderAttempts(meta.ID)
	if err != nil {
		t.Fatalf("load attempts: %v", err)
	}
	if len(attempts) != 2 || attempts[0].Outcome != "retry" || attempts[0].Error != "temporary timeout" || attempts[1].Outcome != "success" {
		t.Fatalf("unexpected provider attempts: %#v", attempts)
	}
	if attempts[1].CacheCreationInputTokens != 11 || attempts[1].CacheReadInputTokens != 23 {
		t.Fatalf("expected cache counters in provider attempts: %#v", attempts)
	}
	summary, err := os.ReadFile(filepath.Join(store.SessionDir(meta.ID), "session.md"))
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if !strings.Contains(string(summary), "cache usage: read=`23` creation=`11` hit_attempts=`1`") {
		t.Fatalf("expected cache usage summary, got:\n%s", string(summary))
	}
	checkpoint, err := store.LoadLongRunCheckpoint(meta.ID)
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if checkpoint.SessionID != meta.ID || len(checkpoint.ResumeHints) == 0 {
		t.Fatalf("unexpected checkpoint: %#v", checkpoint)
	}
}

func TestSessionSummaryAndCheckpointSeparateCancelledTasks(t *testing.T) {
	store, meta := newRuntimeTestSession(t)
	meta.ParentSessionID = "parent-session"
	if err := store.SaveMetadata(meta.ID, meta); err != nil {
		t.Fatalf("save metadata: %v", err)
	}
	if _, err := session.CreateTask(store, meta.ID, session.TaskCreateInput{Subject: "ship completed task"}); err != nil {
		t.Fatalf("create completed task: %v", err)
	}
	if _, err := session.UpdateTask(store, meta.ID, session.TaskUpdateInput{TaskID: "task_0001", Status: "completed"}); err != nil {
		t.Fatalf("complete task: %v", err)
	}
	if _, err := session.CreateTask(store, meta.ID, session.TaskCreateInput{Subject: "drop cancelled task"}); err != nil {
		t.Fatalf("create cancelled task: %v", err)
	}
	if _, err := session.UpdateTask(store, meta.ID, session.TaskUpdateInput{TaskID: "task_0002", Status: "cancelled"}); err != nil {
		t.Fatalf("cancel task: %v", err)
	}
	if err := writeSessionSummary(store, meta.ID); err != nil {
		t.Fatalf("write summary: %v", err)
	}
	if err := writeLongRunCheckpoint(store, meta.ID); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}

	summary, err := os.ReadFile(filepath.Join(store.SessionDir(meta.ID), "session.md"))
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if !strings.Contains(string(summary), "completed=1 cancelled=1 done=2 total=2") {
		t.Fatalf("expected summary to separate cancelled tasks, got:\n%s", string(summary))
	}
	checkpoint, err := store.LoadLongRunCheckpoint(meta.ID)
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if checkpoint.TaskSummary["completed"] != 1 || checkpoint.TaskSummary["cancelled"] != 1 || checkpoint.TaskSummary["done"] != 2 || checkpoint.TaskSummary["total"] != 2 {
		t.Fatalf("expected checkpoint to separate cancelled tasks, got %#v", checkpoint.TaskSummary)
	}
}

func TestSessionSummaryAndCheckpointRecordRecentOwnerClue(t *testing.T) {
	store, meta := newRuntimeTestSession(t)
	meta.ParentSessionID = "parent-session"
	if err := store.SaveMetadata(meta.ID, meta); err != nil {
		t.Fatalf("save metadata: %v", err)
	}
	if err := store.AppendEvent(meta.ID, events.New(meta.ID, "webconsole.handle.acquired", "webconsole", map[string]any{
		"source":           "webconsole",
		"process_start_id": "123:2026-05-08T00:00:00Z",
		"pid":              123,
		"started_at":       "2026-05-08T00:00:00Z",
	})); err != nil {
		t.Fatalf("append owner event: %v", err)
	}
	if err := writeSessionSummary(store, meta.ID); err != nil {
		t.Fatalf("write summary: %v", err)
	}
	if err := writeLongRunCheckpoint(store, meta.ID); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}

	summary, err := os.ReadFile(filepath.Join(store.SessionDir(meta.ID), "session.md"))
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if !strings.Contains(string(summary), "recent owner") || !strings.Contains(string(summary), "123:2026-05-08T00:00:00Z") {
		t.Fatalf("expected owner clue in summary, got:\n%s", string(summary))
	}
	checkpoint, err := store.LoadLongRunCheckpoint(meta.ID)
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if checkpoint.RecentOwner == nil || checkpoint.RecentOwner.ProcessStartID != "123:2026-05-08T00:00:00Z" || checkpoint.RecentOwner.HandleState != "acquired" {
		t.Fatalf("expected owner clue in checkpoint, got %#v", checkpoint.RecentOwner)
	}
}

func TestCheckpointResumeHintWarnsOnIsolationAndTrustDrift(t *testing.T) {
	store, meta := newRuntimeTestSession(t)
	meta.Isolation = &session.IsolationInfo{
		Mode:          "copy",
		RequestedMode: "copy",
		Workdir:       filepath.Join(meta.Workdir, "isolated-old"),
		RootDir:       filepath.Join(meta.Workdir, "root-old"),
	}
	if err := store.SaveMetadata(meta.ID, meta); err != nil {
		t.Fatalf("save metadata: %v", err)
	}
	if err := store.SaveContract(meta.ID, session.SessionContract{
		SchemaVersion: 1,
		ContractID:    "contract_" + meta.ID,
		Source:        "user_instruction",
		TrustSource:   "explicit_user",
		Profile:       "large_project",
		CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("save contract: %v", err)
	}
	if err := writeLongRunCheckpoint(store, meta.ID); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}
	meta.Isolation = &session.IsolationInfo{
		Mode:          "copy",
		RequestedMode: "copy",
		Workdir:       filepath.Join(meta.Workdir, "isolated-new"),
		RootDir:       filepath.Join(meta.Workdir, "root-old"),
	}
	if err := store.SaveMetadata(meta.ID, meta); err != nil {
		t.Fatalf("save drifted metadata: %v", err)
	}
	contract, err := store.LoadContract(meta.ID)
	if err != nil {
		t.Fatalf("load contract: %v", err)
	}
	contract.TrustSource = "trusted_workspace"
	if err := store.SaveContract(meta.ID, contract); err != nil {
		t.Fatalf("save drifted contract: %v", err)
	}
	injected, warnings, err := appendCheckpointResumeHint(store, meta, meta.Provider, meta.Model)
	if err != nil {
		t.Fatalf("append checkpoint hint: %v", err)
	}
	if !injected {
		t.Fatal("expected checkpoint resume hint")
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "isolation workdir changed") || !strings.Contains(joined, "trust source changed") {
		t.Fatalf("expected isolation and trust drift warnings, got %#v", warnings)
	}
	messages, err := store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	if len(messages) == 0 || !strings.Contains(messages[len(messages)-1].Text, "drift warnings") {
		t.Fatalf("expected drift warning in resume note, got %#v", messages)
	}
}

func TestParentCoordinationGateBlocksWaitAllAndAllowsWaitAnyAfterOneCompletion(t *testing.T) {
	store, meta := newRuntimeTestSession(t)
	controller := NewCompletionController(store, meta.ID, meta.Workdir, false, nil)

	if err := addParentChildSession(store, meta.ID, "child-1", "wait-all"); err != nil {
		t.Fatalf("add child: %v", err)
	}
	decision := controller.EvaluateToolCall(nil, "finish", json.RawMessage(`{}`))
	if decision.Status != GateBlock || decision.GateID != "parent_coordination" {
		t.Fatalf("expected wait-all parent coordination block, got %#v", decision)
	}

	if err := addParentQueueJob(store, meta.ID, "job-1", "wait-any"); err != nil {
		t.Fatalf("add queue job: %v", err)
	}
	if err := resolveParentQueueJob(store, meta.ID, "job-1", session.QueueStatusCompleted); err != nil {
		t.Fatalf("resolve queue job: %v", err)
	}
	decision = controller.EvaluateToolCall(nil, "finish", json.RawMessage(`{}`))
	if decision.Status != GateAllow {
		t.Fatalf("expected wait-any to allow finish after one completion, got %#v", decision)
	}
}

func TestGoalCompletionGateBlocksActiveGoalAndAllowsCompletedGoal(t *testing.T) {
	store, meta := newRuntimeTestSession(t)
	if _, err := store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeGoal,
		Objective: "Finish the durable goal",
		Source:    session.GoalSourceCLI,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	controller := NewCompletionController(store, meta.ID, meta.Workdir, false, nil)

	decision := controller.EvaluateToolCall(nil, "finish", json.RawMessage(`{"message":"done"}`))
	if decision.Status != GateBlock || decision.GateID != "goal_completion_audit" {
		t.Fatalf("expected active goal finish block, got %#v", decision)
	}
	if !strings.Contains(decision.ModelMessage, "update_goal") {
		t.Fatalf("expected update_goal guidance, got %q", decision.ModelMessage)
	}

	goal, err := store.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load goal: %v", err)
	}
	goal.Status = session.GoalStatusComplete
	goal.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := store.SaveGoal(meta.ID, goal); err != nil {
		t.Fatalf("save goal: %v", err)
	}
	decision = controller.EvaluateToolCall(nil, "finish", json.RawMessage(`{"message":"done"}`))
	if decision.Status != GateAllow {
		t.Fatalf("expected completed goal to allow finish, got %#v", decision)
	}
}

func TestGoalCompletionGateRequiresBudgetWrapUpWhenStopOnBudget(t *testing.T) {
	store, meta := newRuntimeTestSession(t)
	tokenBudget := int64(1)
	if _, err := store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:      true,
		Mode:         session.GoalModeGoal,
		Objective:    "Stop cleanly on budget",
		TokenBudget:  &tokenBudget,
		StopOnBudget: true,
		Source:       session.GoalSourceCLI,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	if _, limited, err := store.UpdateGoalAccounting(meta.ID, session.GoalUsageDelta{TokensUsedDelta: 2, SourceTurn: 1}); err != nil || !limited {
		t.Fatalf("expected budget limit, limited=%v err=%v", limited, err)
	}
	controller := NewCompletionController(store, meta.ID, meta.Workdir, false, nil)
	decision := controller.EvaluateToolCall(nil, "finish", json.RawMessage(`{"message":"stopped"}`))
	if decision.Status != GateBlock || decision.GateID != "goal_budget_wrapup" {
		t.Fatalf("expected budget wrap-up gate, got %#v", decision)
	}
	if _, _, err := store.RecordGoalProgress(meta.ID, session.GoalProgressInput{
		Source:   session.GoalSourceTool,
		Kind:     "budget_wrapup",
		Summary:  "Recorded remaining work.",
		Blockers: []string{"needs more budget"},
	}); err != nil {
		t.Fatalf("record budget wrap-up: %v", err)
	}
	decision = controller.EvaluateToolCall(nil, "finish", json.RawMessage(`{"message":"stopped"}`))
	if decision.Status != GateBlock || decision.GateID != "goal_budget_limited" {
		t.Fatalf("expected budget-limited finish to remain blocked after wrap-up record, got %#v", decision)
	}
}

func TestPreCompletionFeatureGateIgnoresSymlinkedFeatureList(t *testing.T) {
	store, meta := newRuntimeTestSession(t)
	outside := filepath.Join(t.TempDir(), "outside-feature-list.json")
	if err := os.WriteFile(outside, []byte(`{"features":[{"id":"feature_0001","status":"pending"}]}`+"\n"), 0o600); err != nil {
		t.Fatalf("write outside feature list: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(store.SessionDir(meta.ID), "feature_list.json")); err != nil {
		t.Fatalf("symlink feature list: %v", err)
	}

	controller := NewCompletionController(store, meta.ID, meta.Workdir, false, nil)
	decision := controller.EvaluatePreCompletionFeatures(true)
	if decision.Status != GateAllow {
		t.Fatalf("expected symlinked feature list to be ignored, got %#v", decision)
	}
}

func TestParentCoordinationGateBlocksPendingBackgroundAcceptanceBeforeFinish(t *testing.T) {
	store, meta := newRuntimeTestSession(t)
	if err := store.AppendBackgroundNotification(meta.ID, session.NewBackgroundNotification(session.QueueJob{
		ID:            "job-accepted-later",
		Status:        session.QueueStatusCompleted,
		SessionID:     "child-accepted-later",
		SessionStatus: session.StatusCompleted,
		FinalText:     "child result",
	})); err != nil {
		t.Fatalf("append background notification: %v", err)
	}
	var events []string
	controller := NewCompletionController(store, meta.ID, meta.Workdir, false, func(eventType string, _ map[string]any) {
		events = append(events, eventType)
	})

	decision := controller.EvaluateToolCall(nil, "finish", json.RawMessage(`{}`))
	if decision.Status != GateBlock || decision.GateID != "parent_background_pending" {
		t.Fatalf("expected pending background acceptance block, got %#v", decision)
	}
	if !containsString(events, "completion.gate.parent_background_pending") {
		t.Fatalf("expected pending-background event, got %#v", events)
	}

	notifications, err := store.LoadBackgroundNotifications(meta.ID)
	if err != nil {
		t.Fatalf("load notifications: %v", err)
	}
	notifications[0].DeliveryStatus = session.BackgroundNotificationAccepted
	if err := store.UpdateBackgroundNotifications(meta.ID, notifications); err != nil {
		t.Fatalf("accept notification: %v", err)
	}
	decision = controller.EvaluateToolCall(nil, "finish", json.RawMessage(`{}`))
	if decision.Status != GateAllow {
		t.Fatalf("expected finish to pass after notification acceptance, got %#v", decision)
	}
}

func TestParentCoordinationWritesParkedAndResumedEvents(t *testing.T) {
	store, meta := newRuntimeTestSession(t)
	if err := addParentChildSession(store, meta.ID, "child-1", "wait-all"); err != nil {
		t.Fatalf("add child: %v", err)
	}
	coordination, err := store.LoadParentCoordination(meta.ID)
	if err != nil {
		t.Fatalf("load coordination: %v", err)
	}
	if !coordination.Parked {
		t.Fatalf("expected parent to be parked, got %#v", coordination)
	}
	if err := resolveParentChildSession(store, meta.ID, "child-1", session.StatusCompleted); err != nil {
		t.Fatalf("resolve child: %v", err)
	}
	coordination, err = store.LoadParentCoordination(meta.ID)
	if err != nil {
		t.Fatalf("reload coordination: %v", err)
	}
	if coordination.Parked {
		t.Fatalf("expected parent to be resumed, got %#v", coordination)
	}
	events, err := store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	var foundParked, foundResumed bool
	for _, event := range events {
		if event.Type == "parent.coordination.parked" {
			foundParked = true
		}
		if event.Type == "parent.coordination.resumed" {
			foundResumed = true
		}
	}
	if !foundParked || !foundResumed {
		t.Fatalf("expected parked/resumed events, got %#v", events)
	}
}

func TestParentCoordinationConcurrentQueueResolutionsPreserveAllResults(t *testing.T) {
	store, meta := newRuntimeTestSession(t)
	jobIDs := []string{
		"job-concurrent-01",
		"job-concurrent-02",
		"job-concurrent-03",
		"job-concurrent-04",
		"job-concurrent-05",
		"job-concurrent-06",
		"job-concurrent-07",
		"job-concurrent-08",
	}
	for _, jobID := range jobIDs {
		if err := addParentQueueJob(store, meta.ID, jobID, "wait-all"); err != nil {
			t.Fatalf("add queue job %s: %v", jobID, err)
		}
	}
	start := make(chan struct{})
	errs := make(chan error, len(jobIDs))
	var wg sync.WaitGroup
	for _, jobID := range jobIDs {
		jobID := jobID
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- resolveParentQueueJob(store, meta.ID, jobID, session.QueueStatusCompleted)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("resolve queue job: %v", err)
		}
	}
	coordination, err := store.LoadParentCoordination(meta.ID)
	if err != nil {
		t.Fatalf("load coordination: %v", err)
	}
	if len(coordination.UnresolvedQueueJobs) != 0 {
		t.Fatalf("expected no unresolved jobs, got %#v", coordination.UnresolvedQueueJobs)
	}
	for _, jobID := range jobIDs {
		if !containsString(coordination.CompletedQueueJobs, jobID) {
			t.Fatalf("missing completed job %s in coordination %#v", jobID, coordination)
		}
	}
}

func newRuntimeTestSession(t *testing.T) (*session.Store, session.SessionMetadata) {
	t.Helper()
	store := session.NewStore(t.TempDir())
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	state := session.State{
		Status:    session.StatusRunning,
		Phase:     "prepare",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return store, meta
}

func blockRuntimeContractHistoryPath(t *testing.T, store *session.Store, sessionID string) {
	t.Helper()
	historyPath := filepath.Join(store.SessionDir(sessionID), "artifacts", "contract-history.jsonl")
	if err := os.Remove(historyPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove contract history: %v", err)
	}
	if err := os.Mkdir(historyPath, 0o700); err != nil {
		t.Fatalf("block contract history path: %v", err)
	}
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
