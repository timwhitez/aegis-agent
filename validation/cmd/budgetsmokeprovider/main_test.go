package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"aegis-agent/internal/config"
	agentruntime "aegis-agent/internal/runtime"
	"aegis-agent/internal/session"
	"aegis-agent/internal/tools"
)

func TestReceiptMissionOldPlanMarkerHitsActualGoalCompletionGate(t *testing.T) {
	store, execCtx, registry, gate := newReceiptMissionControl(t, false)
	facts := requestFacts{SessionID: execCtx.SessionID, InputText: "E2E_UI_PLAN"}
	state := &sessionScriptState{}
	call, err := scriptedCall(facts, state, 1)
	if err != nil || call.Name != "submit_plan" {
		t.Fatalf("initial plan: %#v, %v", call, err)
	}
	executeReceiptControlTool(t, registry, execCtx, call, state)
	approveReceiptControlPlan(t, store, execCtx.SessionID)
	call, err = scriptedCall(facts, state, 2)
	if err != nil || call.Name != "finish" {
		t.Fatalf("old marker finish: %#v, %v", call, err)
	}
	raw, _ := json.Marshal(call.Arguments)
	decision := gate.EvaluateToolCall(nil, call.Name, raw)
	if decision.Status != agentruntime.GateBlock || decision.GateID != "goal_completion_audit" {
		t.Fatalf("old marker should hit actual active-goal gate: %#v", decision)
	}
	t.Log("RED diagnostic: original plan marker calls finish with active Goal; actual completion controller blocks it")
}

func TestReceiptMissionScriptCompletesThroughActualToolsAndGoalGate(t *testing.T) {
	for _, covered := range []bool{false, true} {
		t.Run(map[bool]string{false: "coverage_override", true: "covered_canonical"}[covered], func(t *testing.T) {
			store, execCtx, registry, gate := newReceiptMissionControl(t, covered)
			facts := requestFacts{SessionID: execCtx.SessionID, InputText: "E2E_UI_PLAN_RECEIPT_MISSION"}
			state := &sessionScriptState{}
			sequence := []string{"submit_plan", "get_goal", "shell", "record_goal_progress", "update_goal", "finish"}
			var reviewed session.ApprovalSnapshot
			for index, name := range sequence {
				call, err := scriptedCall(facts, state, index+1)
				if err != nil || call.Name != name {
					t.Fatalf("provider turn %d: %#v, %v; want %s", index+1, call, err, name)
				}
				raw, _ := json.Marshal(call.Arguments)
				if decision := gate.EvaluateToolCall(nil, name, raw); decision.Status != agentruntime.GateAllow {
					t.Fatalf("actual gate for %s: %#v", name, decision)
				}
				executeReceiptControlTool(t, registry, execCtx, call, state)
				if index == 0 {
					approveReceiptControlPlan(t, store, execCtx.SessionID)
					reviewed, err = store.LoadApprovalSnapshot(execCtx.SessionID)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			goal, err := store.LoadGoal(execCtx.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if goal.Status != session.GoalStatusComplete || goal.CompletionAudit == nil || len(goal.CompletionAudit.Evidence) == 0 {
				t.Fatalf("real completion audit missing: %#v", goal)
			}
			if len(goal.Progress) != 1 || len(goal.Progress[0].Commands) != 1 || goal.Progress[0].Commands[0].ExitCode == nil || *goal.Progress[0].Commands[0].ExitCode != 0 {
				t.Fatalf("real command progress missing: %#v", goal.Progress)
			}
			if goal.Mission.Features[0].Status != "completed" || goal.Mission.Milestones[0].Status != "completed" || goal.Mission.ValidationContract[0].Status != "verified" {
				t.Fatalf("actual tool observation updates missing: %#v", goal.Mission)
			}
			if !reflect.DeepEqual(goal.Mission.Features[0].ClaimedAssertions, reviewed.Goal.Mission.Features[0].ClaimedAssertions) || !reflect.DeepEqual(goal.Mission.Milestones[0].ValidationIDs, reviewed.Goal.Mission.Milestones[0].ValidationIDs) {
				t.Fatal("completion must preserve reviewed coverage mappings")
			}
			current, err := store.LoadApprovalSnapshot(execCtx.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if current.Revision != reviewed.Revision {
				t.Fatal("status/evidence observations changed semantic approval scope")
			}
			history, err := store.LoadGoalHistory(execCtx.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if len(history) == 0 || history[len(history)-1].Type != "goal.completed" {
				t.Fatalf("completion history missing: %#v", history)
			}
			t.Logf("actual tool/gate control complete: sequence=%v, admitted execution provider turns=5, coverage mappings preserved", sequence)
		})
	}
}

func TestReceiptMissionScriptRefusesMissingOrFailedToolEvidence(t *testing.T) {
	facts := requestFacts{SessionID: "receipt-session", InputText: "E2E_UI_PLAN_RECEIPT_MISSION"}
	for _, turn := range []int{3, 4, 5, 6} {
		if _, err := scriptedCall(facts, &sessionScriptState{}, turn); err == nil {
			t.Fatalf("turn %d accepted absent actual tool evidence", turn)
		}
	}
	for _, output := range []string{
		"[command_result tool=shell exit_code=1 truncated=false]\napproval-cas",
		"[command_result tool=shell exit_code=0 truncated=false]\nunexpected output",
		"Error: shell command failed",
	} {
		state := &sessionScriptState{}
		captureToolReferences([]any{functionCallOutput(output)}, state)
		if _, err := scriptedCall(facts, state, 4); err == nil {
			t.Fatalf("failed/mismatched command result produced completion progress: %q", output)
		}
	}
}

func newReceiptMissionControl(t *testing.T, covered bool) (*session.Store, tools.ExecContext, *tools.Registry, *agentruntime.CompletionController) {
	t.Helper()
	cfg := config.Default()
	store := session.NewStore(t.TempDir())
	meta := session.SessionMetadata{SchemaVersion: 1, ID: session.NewSessionID(), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Workdir: t.TempDir(), Mode: session.ModeRun, Provider: "fixture", Model: "fixture", CompletionPolicy: session.CompletionPolicyInteractive}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: meta.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	goal, err := store.CreateGoal(meta.ID, session.GoalDraft{Enabled: true, Mode: session.GoalModeMission, Objective: "Exercise receipt approval and execute the local validation command", RequirePlanApproval: true})
	if err != nil {
		t.Fatal(err)
	}
	mission := &session.MissionPlan{PlanStatus: session.MissionPlanStatusNeedsApproval,
		Requirements:       []session.MissionRequirement{{ID: "requirement_scope", Text: "Review the linked scope before execution."}},
		Features:           []session.MissionFeature{{ID: "feature_scope", Title: "Reviewed linked scope", Status: "pending"}},
		Milestones:         []session.MissionMilestone{{ID: "milestone_scope", Title: "Coverage milestone", Status: "pending", FeatureIDs: []string{"feature_scope"}}},
		ValidationContract: []session.GoalValidation{{ID: "validation_scope", Kind: "command", Command: "printf approval-cas", Description: "Validate the reviewed scope.", Status: "pending"}}}
	if covered {
		mission.Features[0].ClaimedAssertions = []string{"validation_scope"}
		mission.Milestones[0].ValidationIDs = []string{"validation_scope"}
	}
	if _, err := store.PatchGoal(meta.ID, session.GoalPatchInput{Mission: mission}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.EnsurePlanModeForGoal(meta.ID, goal, session.PlanModeSourceWeb); err != nil {
		t.Fatal(err)
	}
	registry, err := tools.NewRegistry(cfg, nil, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	execCtx := tools.ExecContext{SessionID: meta.ID, Workdir: meta.Workdir, Store: store, Config: cfg}
	return store, execCtx, registry, agentruntime.NewCompletionController(store, meta.ID, meta.Workdir, false, nil)
}

func approveReceiptControlPlan(t *testing.T, store *session.Store, id string) {
	t.Helper()
	snapshot, err := store.LoadApprovalSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApprovePlanModeTarget(id, session.PlanModeSourceWeb, snapshot.Target(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkPlanModeExecuting(id, session.PlanModeSourceSystem); err != nil {
		t.Fatal(err)
	}
}

func executeReceiptControlTool(t *testing.T, registry *tools.Registry, execCtx tools.ExecContext, call scriptedToolCall, state *sessionScriptState) {
	t.Helper()
	raw, err := json.Marshal(call.Arguments)
	if err != nil {
		t.Fatal(err)
	}
	result, err := registry.Execute(context.Background(), call.Name, execCtx, raw)
	if err != nil || result.IsError {
		t.Fatalf("real %s tool: %#v, %v", call.Name, result, err)
	}
	if call.Name == "shell" && !strings.Contains(result.LLMOutput, "approval-cas") {
		t.Fatalf("actual command output missing: %s", result.LLMOutput)
	}
	captureToolReferences([]any{functionCallOutput(result.LLMOutput)}, state)
}

func TestScriptedCallExercisesBoundedOutputAndHistoryBeforeBudgetLifecycle(t *testing.T) {
	state := &sessionScriptState{}
	facts := requestFacts{SessionID: "root-session"}

	assertScriptedCall(t, facts, state, 1, "todo_write", nil)
	assertScriptedCall(t, facts, state, 2, "shell", map[string]any{
		"command": "head -c 70000 /dev/zero | tr '\\0' p",
	})

	captureToolReferences([]any{functionCallOutput(`[command_result tool=shell exit_code=0]
[Complete artifact: artifacts/tool-outputs/shell-call.txt; raw_bytes=70000. Page with read_file byte_offset/byte_limit (inline_limit=32768).]`)}, state)
	if state.CommandArtifactPath != "artifacts/tool-outputs/shell-call.txt" {
		t.Fatalf("command artifact path=%q", state.CommandArtifactPath)
	}
	assertScriptedCall(t, facts, state, 3, "read_file", map[string]any{
		"path":        state.CommandArtifactPath,
		"byte_offset": 0,
		"byte_limit":  512,
	})
	assertScriptedCall(t, facts, state, 4, "read_session_history", map[string]any{"limit": 4})

	captureToolReferences([]any{functionCallOutput(`{
  "schema_version": 1,
  "mode": "tail",
  "historical_reference": true,
  "has_more": true,
  "next_before_message_id": "msg-before",
  "messages": [
    {"message_id":"msg-shell","tool_results":[{"name":"shell","output_bytes":32768}]},
    {"message_id":"msg-other","tool_results":[{"name":"todo_write","output_bytes":40}]}
  ]
}`)}, state)
	if state.HistoryMessageID != "msg-shell" {
		t.Fatalf("history message id=%q", state.HistoryMessageID)
	}
	assertScriptedCall(t, facts, state, 5, "read_session_history", map[string]any{
		"message_id":  state.HistoryMessageID,
		"byte_offset": 0,
		"byte_limit":  512,
	})

	captureToolReferences([]any{functionCallOutput(`{
  "schema_version": 1,
  "mode": "message_content",
  "message_id": "msg-shell",
  "has_more": true,
  "next_byte_offset": 512,
  "content": "first page"
}`)}, state)
	if state.HistoryNextByteOffset != 512 {
		t.Fatalf("history next byte offset=%d", state.HistoryNextByteOffset)
	}
	assertScriptedCall(t, facts, state, 6, "read_session_history", map[string]any{
		"message_id":  state.HistoryMessageID,
		"byte_offset": int64(512),
		"byte_limit":  512,
	})

	assertScriptedCall(t, facts, state, 7, "agent_spawn", nil)
	state.DirectChildID = "child-direct"
	assertScriptedCall(t, facts, state, 8, "agent_prompt", nil)
	assertScriptedCall(t, facts, state, 9, "agent_status", nil)
	assertScriptedCall(t, facts, state, 10, "agent_spawn", nil)
	state.BackgroundJobID = "job-background"
	assertScriptedCall(t, facts, state, 11, "agent_stop", nil)
	assertScriptedCall(t, facts, state, 12, "agent_list", nil)
	assertScriptedCall(t, facts, state, 13, "todo_write", nil)
	assertScriptedCall(t, facts, state, 14, "finish", nil)
}

func TestCaptureToolReferencesIgnoresPartialArtifactsAndNonShellHistory(t *testing.T) {
	state := &sessionScriptState{}
	captureToolReferences([]any{
		functionCallOutput(`[Partial artifact: artifacts/tool-outputs/partial.txt; saved=10/20 bytes omitted=10 reason=artifact_file_max_bytes; unrecoverable.]`),
		functionCallOutput(`{"mode":"tail","messages":[{"message_id":"msg-other","tool_results":[{"name":"grep","output_bytes":9999}]}]}`),
		functionCallOutput(`{"mode":"message_content","message_id":"msg-other","has_more":false,"content":"done"}`),
	}, state)
	if state.CommandArtifactPath != "" || state.HistoryMessageID != "" || state.HistoryNextByteOffset != 0 {
		t.Fatalf("unrecoverable/non-shell references were accepted: %#v", state)
	}
}

func TestScriptedCallCoversBrowserE2ELifecycleMarkers(t *testing.T) {
	tests := []struct {
		name  string
		input string
		calls []string
	}{
		{name: "main", input: "E2E_UI_MAIN", calls: []string{"todo_write", "task_create", "task_create", "task_create", "task_create", "task_update", "task_create", "task_update", "task_update", "shell", "finish"}},
		{name: "goal", input: "E2E_UI_GOAL", calls: []string{"update_goal", "finish"}},
		{name: "plan", input: "E2E_UI_PLAN", calls: []string{"submit_plan", "finish"}},
		{name: "plan revision", input: "E2E_UI_PLAN_REVISE", calls: []string{"submit_plan", "submit_plan", "finish"}},
		{name: "plan input", input: "E2E_UI_PLAN_INPUT", calls: []string{"request_user_input", "submit_plan", "finish"}},
		{name: "children", input: "E2E_UI_CHILDREN", calls: []string{"agent_spawn", "finish"}},
		{name: "await", input: "E2E_UI_AWAIT", calls: []string{"await_input", "finish"}},
		{name: "slow", input: "E2E_UI_SLOW", calls: []string{"finish", "finish"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			facts := requestFacts{SessionID: "browser-session", InputText: test.input}
			state := &sessionScriptState{}
			for index, want := range test.calls {
				assertScriptedCall(t, facts, state, index+1, want, nil)
			}
		})
	}
}

func assertScriptedCall(t *testing.T, facts requestFacts, state *sessionScriptState, callNumber int, wantName string, wantArguments map[string]any) {
	t.Helper()
	call, err := scriptedCall(facts, state, callNumber)
	if err != nil {
		t.Fatalf("scripted call %d: %v", callNumber, err)
	}
	if call.Name != wantName {
		t.Fatalf("scripted call %d name=%q want=%q", callNumber, call.Name, wantName)
	}
	if wantArguments == nil {
		return
	}
	got, err := json.Marshal(call.Arguments)
	if err != nil {
		t.Fatalf("marshal call %d arguments: %v", callNumber, err)
	}
	want, err := json.Marshal(wantArguments)
	if err != nil {
		t.Fatalf("marshal expected call %d arguments: %v", callNumber, err)
	}
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("scripted call %d arguments=%s want=%s", callNumber, got, want)
	}
}

func functionCallOutput(output string) map[string]any {
	return map[string]any{
		"type":   "function_call_output",
		"output": output,
	}
}
