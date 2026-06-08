package multica

import (
	"strings"
	"testing"

	"ngen/internal/artifact"
	ngenrt "ngen/internal/runtime"
	"ngen/internal/task"
)

func TestParseUsageSummaryOmitsUnknownAndMapsCacheTokens(t *testing.T) {
	usage, ok := ParseUsageSummary("input_tokens=101 output_tokens=17 unknown=3", "cache_creation_input_tokens=83 cache_read_input_tokens=197")
	if !ok {
		t.Fatal("expected usage to be observed")
	}
	if usage.InputTokens != 101 || usage.OutputTokens != 17 || usage.CacheWriteTokens != 83 || usage.CacheReadTokens != 197 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
	if _, ok := ParseUsageSummary("unknown", ""); ok {
		t.Fatal("expected unknown usage to be omitted")
	}
}

func TestSplitModelRouteRoundTripsProviderAndModel(t *testing.T) {
	mode, model, err := SplitModelRoute("openai-response/gpt-5.5")
	if err != nil {
		t.Fatalf("split model route: %v", err)
	}
	if mode != "openai-response" || model != "gpt-5.5" {
		t.Fatalf("unexpected split: %s %s", mode, model)
	}
}

func TestEventMessagesProjectsWorkspaceEditRecordWithStableCallID(t *testing.T) {
	dir := t.TempDir()
	svc := ngenrt.New(dir, task.DefaultConfig())
	taskID := "TASK-multica-edit"
	if err := svc.Store.EnsureWorkspaceLayout(); err != nil {
		t.Fatalf("ensure workspace layout: %v", err)
	}
	if err := svc.Store.EnsureTaskLayout(taskID); err != nil {
		t.Fatalf("ensure task layout: %v", err)
	}
	record := task.WorkspaceEditRecord{
		SchemaVersion: task.SchemaVersion,
		EditRecordID:  "EDITREC-stable",
		EditID:        "EDIT-stable",
		TaskID:        taskID,
		TS:            task.Now(),
		Kind:          "workspace_edit",
		Status:        "applied",
		ProviderMode:  "openai-response",
		Summary:       "Updated README",
		FileChanges: []task.WorkspaceFileChange{{
			Path:         "README.md",
			Action:       "write",
			BeforeExists: true,
			AfterExists:  true,
		}},
	}
	if err := svc.Store.AppendWorkspaceEdit(record); err != nil {
		t.Fatalf("append workspace edit: %v", err)
	}
	metadata := task.MulticaRunMetadata{
		ModelRoute:    "openai-response/gpt-5.5",
		ProviderMode:  "openai-response",
		ProviderModel: "gpt-5.5",
	}
	event := task.Event{
		SchemaVersion: task.SchemaVersion,
		EventID:       "EVT-workspace-edit",
		TaskID:        taskID,
		TS:            task.Now(),
		Phase:         task.PhaseExecute,
		State:         task.StateActive,
		Type:          "workspace_edit_applied",
		Summary:       record.Summary,
		Refs:          []string{artifact.WorkspaceEditRecordRef(record.EditRecordID)},
	}

	messages := eventMessages(svc, event, "worker", metadata)
	if len(messages) != 2 {
		t.Fatalf("expected tool use/result messages, got %+v", messages)
	}
	use, result := messages[0], messages[1]
	if use.Type != "tool_use" || use.Tool == nil {
		t.Fatalf("expected tool_use projection, got %+v", use)
	}
	if result.Type != "tool_result" || result.Tool == nil {
		t.Fatalf("expected tool_result projection, got %+v", result)
	}
	if use.Tool.Name != "workspace_edit" || result.Tool.Name != "workspace_edit" {
		t.Fatalf("expected workspace_edit tool names, got use=%+v result=%+v", use.Tool, result.Tool)
	}
	if use.Tool.CallID != record.EditRecordID || result.Tool.CallID != record.EditRecordID {
		t.Fatalf("expected stable edit record call id, got use=%q result=%q", use.Tool.CallID, result.Tool.CallID)
	}
	if result.Tool.Status != "applied" || result.IsError {
		t.Fatalf("expected applied non-error result, got %+v", result)
	}
	if use.Tool.Input["provider_mode"] != "openai-response" || use.Tool.Input["summary"] != record.Summary {
		t.Fatalf("unexpected tool input: %+v", use.Tool.Input)
	}
	if !strings.Contains(result.Tool.Output, "Updated README") || !strings.Contains(result.Tool.Output, "write README.md") {
		t.Fatalf("expected summary and file change in output, got %q", result.Tool.Output)
	}
}

func TestResultMessageProjectsWorkerParentActionAndTrustFields(t *testing.T) {
	dir := t.TempDir()
	svc := ngenrt.New(dir, task.DefaultConfig())
	taskID := "TASK-multica-worker"
	if err := svc.Store.EnsureWorkspaceLayout(); err != nil {
		t.Fatalf("ensure workspace layout: %v", err)
	}
	if err := svc.Store.EnsureTaskLayout(taskID); err != nil {
		t.Fatalf("ensure task layout: %v", err)
	}
	contract := task.WorkerContract{
		SchemaVersion:              task.SchemaVersion,
		WorkerID:                   "WKR-001",
		ParentTaskID:               taskID,
		ChildTaskID:                "TASK-child",
		Role:                       string(task.KindReviewer),
		Status:                     "blocked",
		BlockedReasonCode:          "blocked_policy",
		BlockedDetailRef:           "approvals.jsonl#approval_record_id=APRREC-001",
		RequiresParentAction:       true,
		ParentActionType:           "owned_approval_pending",
		ParentActionOptions:        []string{"approve", "deny", "parent_takeover"},
		ParentActionSummary:        "Worker awaits parent approval.",
		ParentActionUnresolved:     true,
		EvidenceScore:              42,
		EvidenceGrade:              "partial",
		MissingEvidence:            []string{"verification/latest.json"},
		TrustedForParentCompletion: false,
		ConflictCount:              1,
		CreatedAt:                  task.Now(),
		UpdatedAt:                  task.Now(),
	}
	if err := svc.Store.SaveWorkerContract(contract); err != nil {
		t.Fatalf("save worker contract: %v", err)
	}
	workerResult := task.WorkerResult{
		SchemaVersion:              task.SchemaVersion,
		ResultID:                   "WKRRES-001",
		WorkerID:                   contract.WorkerID,
		ParentTaskID:               taskID,
		ChildTaskID:                contract.ChildTaskID,
		Role:                       contract.Role,
		ChildState:                 task.StateBlocked,
		CompletionStatus:           "blocked",
		ReviewStatus:               "blocking",
		VerificationStatus:         "failed",
		RequiresParentAction:       true,
		ParentActionType:           contract.ParentActionType,
		ParentActionOptions:        append([]string(nil), contract.ParentActionOptions...),
		ParentActionSummary:        contract.ParentActionSummary,
		ParentActionUnresolved:     true,
		EvidenceScore:              contract.EvidenceScore,
		EvidenceGrade:              contract.EvidenceGrade,
		MissingEvidence:            append([]string(nil), contract.MissingEvidence...),
		TrustedForParentCompletion: false,
		ConflictCount:              contract.ConflictCount,
		Summary:                    "Worker is blocked on parent approval.",
		EvidenceRefs:               []string{"worker_runtime/WKR-001.result.json"},
		CreatedAt:                  task.Now(),
		UpdatedAt:                  task.Now(),
	}
	if err := svc.Store.SaveWorkerResult(workerResult); err != nil {
		t.Fatalf("save worker result: %v", err)
	}
	metadata := task.MulticaRunMetadata{
		ModelRoute:    "openai-response/gpt-5.5",
		ProviderMode:  "openai-response",
		ProviderModel: "gpt-5.5",
	}
	snapshot := task.StatusSnapshot{
		TaskID:           taskID,
		Phase:            task.PhaseReview,
		State:            task.StateBlocked,
		StatusReasonCode: "blocked_review",
	}

	msg := resultMessage(snapshot, "orchestrator", metadata, ConfigResolution{}, "blocked", nil, svc)
	if msg.Type != "result" || msg.Status != "blocked" || msg.SessionID != taskID {
		t.Fatalf("unexpected result message: %+v", msg)
	}
	if msg.Handoff == nil || len(msg.Handoff.WorkerResults) != 1 {
		t.Fatalf("expected one worker digest, got %+v", msg.Handoff)
	}
	digest := msg.Handoff.WorkerResults[0]
	if digest.WorkerID != contract.WorkerID || digest.RequiresParentAction != true || digest.ParentActionType != contract.ParentActionType {
		t.Fatalf("expected parent action fields, got %+v", digest)
	}
	if strings.Join(digest.ParentActionOptions, ",") != "approve,deny,parent_takeover" || digest.ParentActionSummary != contract.ParentActionSummary {
		t.Fatalf("unexpected parent action details: %+v", digest)
	}
	if digest.TrustedForParentCompletion || digest.EvidenceScore != 42 || digest.EvidenceGrade != "partial" || digest.ConflictCount != 1 {
		t.Fatalf("expected worker trust fields, got %+v", digest)
	}
	if digest.Summary != workerResult.Summary || len(digest.EvidenceRefs) != 1 {
		t.Fatalf("expected worker result summary/evidence refs, got %+v", digest)
	}
}
