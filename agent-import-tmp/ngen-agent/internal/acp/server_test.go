package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ngenrt "ngen/internal/runtime"
	"ngen/internal/task"
)

func TestServerSessionPromptFlow(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")
	cfg := task.DefaultConfig()
	svc := ngenrt.New(dir, cfg)
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Objective: "review docs",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "docs reviewed"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	var output bytes.Buffer
	server := Server{Service: svc}
	firstOnly := &bytes.Buffer{}
	mustWriteJSONLine(t, firstOnly, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "session.start",
		"params":  map[string]any{"task_id": spec.TaskID, "mode": "acp"},
	})
	if err := server.Serve(context.Background(), firstOnly, &output); err != nil {
		t.Fatalf("serve start session: %v", err)
	}
	responses, notifications := parseRPCOutput(t, output.String())
	if len(responses) != 1 || len(notifications) != 1 {
		t.Fatalf("expected one response and one notification, got responses=%d notifications=%d body=%q", len(responses), len(notifications), output.String())
	}
	startResp := responses[0]
	if startResp.Error != nil {
		t.Fatalf("unexpected start error: %+v", startResp.Error)
	}
	startResult, ok := startResp.Result.(map[string]any)
	if !ok {
		raw, err := json.Marshal(startResp.Result)
		if err != nil {
			t.Fatalf("marshal start result: %v", err)
		}
		if err := json.Unmarshal(raw, &startResult); err != nil {
			t.Fatalf("decode start result: %v", err)
		}
	}
	sessionID, _ := startResult["session_id"].(string)
	if sessionID == "" {
		t.Fatal("expected session id")
	}

	output.Reset()
	var promptInput bytes.Buffer
	mustWriteJSONLine(t, &promptInput, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "session.prompt",
		"params":  map[string]any{"session_id": sessionID, "message": "/run"},
	})
	if err := server.Serve(context.Background(), &promptInput, &output); err != nil {
		t.Fatalf("serve prompt session: %v", err)
	}
	responses, notifications = parseRPCOutput(t, output.String())
	if len(responses) != 1 || len(notifications) != 1 {
		t.Fatalf("expected one prompt response and one notification, got responses=%d notifications=%d body=%q", len(responses), len(notifications), output.String())
	}
	promptResp := responses[0]
	if promptResp.Error != nil {
		t.Fatalf("unexpected prompt error: %+v", promptResp.Error)
	}
	raw, err := json.Marshal(promptResp.Result)
	if err != nil {
		t.Fatalf("marshal prompt result: %v", err)
	}
	var promptResult struct {
		Status task.StatusSnapshot `json:"status"`
	}
	if err := json.Unmarshal(raw, &promptResult); err != nil {
		t.Fatalf("unmarshal prompt result: %v", err)
	}
	if promptResult.Status.State != task.StateDone {
		t.Fatalf("expected done status from session.prompt /run, got state=%s reason=%s", promptResult.Status.State, promptResult.Status.StatusReasonCode)
	}
	notification := decodeNotification(t, notifications[0])
	if notification.Kind != "session.updated" {
		t.Fatalf("expected session.updated notification, got %+v", notification)
	}
	if notification.SessionSnapshot == nil || notification.SessionSnapshot.StatusSnapshot.State != task.StateDone {
		t.Fatalf("expected done session snapshot in notification, got %+v", notification.SessionSnapshot)
	}
	if len(notification.Events) == 0 {
		t.Fatalf("expected prompt notification to include events, got %+v", notification)
	}
}

func TestServerInitializeAndPing(t *testing.T) {
	dir := t.TempDir()
	cfg := task.DefaultConfig()
	svc := ngenrt.New(dir, cfg)
	server := Server{Service: svc}

	var input bytes.Buffer
	mustWriteJSONLine(t, &input, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
	})
	mustWriteJSONLine(t, &input, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "rpc.ping",
	})

	var output bytes.Buffer
	if err := server.Serve(context.Background(), &input, &output); err != nil {
		t.Fatalf("serve initialize/ping: %v", err)
	}
	responses, notifications := parseRPCOutput(t, output.String())
	if len(responses) != 2 || len(notifications) != 0 {
		t.Fatalf("expected two responses and no notifications, got responses=%d notifications=%d body=%q", len(responses), len(notifications), output.String())
	}
	data, err := json.Marshal(responses[0].Result)
	if err != nil {
		t.Fatalf("marshal initialize result: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, `"capabilities"`) {
		t.Fatalf("expected capabilities in initialize response, got %s", body)
	}
	if !strings.Contains(body, `"session_snapshot"`) || !strings.Contains(body, `"mission_status_snapshot"`) || !strings.Contains(body, `"worker_snapshot"`) || !strings.Contains(body, `"input"`) || !strings.Contains(body, `"mission"`) || !strings.Contains(body, `"notifications"`) || !strings.Contains(body, `"permission"`) || !strings.Contains(body, `"events"`) || !strings.Contains(body, `"decide"`) || !strings.Contains(body, `"list"`) || !strings.Contains(body, `"continue"`) {
		t.Fatalf("expected snapshot, mission, permission, worker, input, events, and notifications capability in initialize response, got %s", body)
	}
	pingBody, err := json.Marshal(responses[1].Result)
	if err != nil {
		t.Fatalf("marshal ping result: %v", err)
	}
	if !strings.Contains(string(pingBody), `"ok":true`) {
		t.Fatalf("expected ping ok response, got %s", string(pingBody))
	}
}

func TestServerMissionStatus(t *testing.T) {
	dir := t.TempDir()
	svc := ngenrt.New(dir, task.DefaultConfig())
	created, err := svc.CreateMission(context.Background(), task.MissionCreateRequest{
		Title:     "acp mission",
		Objective: "expose mission status over acp",
		Criteria:  []string{"mission.status returns mission view"},
	})
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}

	var input bytes.Buffer
	mustWriteJSONLine(t, &input, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "mission.status",
		"params": map[string]any{
			"mission_id": created.Mission.MissionID,
		},
	})
	var output bytes.Buffer
	if err := (&Server{Service: svc}).Serve(context.Background(), &input, &output); err != nil {
		t.Fatalf("serve mission.status: %v", err)
	}
	responses, notifications := parseRPCOutput(t, output.String())
	if len(responses) != 1 || len(notifications) != 0 {
		t.Fatalf("expected one response and no notifications, got responses=%d notifications=%d body=%q", len(responses), len(notifications), output.String())
	}
	body, err := json.Marshal(responses[0].Result)
	if err != nil {
		t.Fatalf("marshal mission.status result: %v", err)
	}
	if !strings.Contains(string(body), created.Mission.MissionID) || !strings.Contains(string(body), `"mission_status_snapshot"`) {
		t.Fatalf("expected mission view status snapshot, got %s", string(body))
	}
}

func TestServerTaskEventsReplaysAfterCursor(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# acp events\n")
	svc := ngenrt.New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "acp events",
		Objective: "replay events by cursor",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "docs reviewed"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := svc.PromoteMemory(context.Background(), spec.TaskID, task.MemoryPromotion{
		Kind:    "note",
		Summary: "ACP event replay marker.",
	}, task.MemorySourceOperator); err != nil {
		t.Fatalf("promote memory: %v", err)
	}
	events, err := svc.TailEvents(spec.TaskID, 10)
	if err != nil {
		t.Fatalf("tail events: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("expected at least two events, got %+v", events)
	}

	server := Server{Service: svc}
	var input bytes.Buffer
	mustWriteJSONLine(t, &input, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "task.events",
		"params":  map[string]any{"task_id": spec.TaskID, "after": events[0].EventID, "limit": 10},
	})
	var output bytes.Buffer
	if err := server.Serve(context.Background(), &input, &output); err != nil {
		t.Fatalf("serve task.events: %v", err)
	}
	responses, notifications := parseRPCOutput(t, output.String())
	if len(responses) != 1 || len(notifications) != 0 {
		t.Fatalf("expected one response and no notifications, got responses=%d notifications=%d body=%q", len(responses), len(notifications), output.String())
	}
	if responses[0].Error != nil {
		t.Fatalf("unexpected task.events error: %+v", responses[0].Error)
	}
	raw, err := json.Marshal(responses[0].Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var replayed []task.Event
	if err := json.Unmarshal(raw, &replayed); err != nil {
		t.Fatalf("unmarshal replayed events: %v", err)
	}
	if len(replayed) == 0 || replayed[0].EventID == events[0].EventID {
		t.Fatalf("expected replay after cursor to omit first event, got %+v", replayed)
	}
	if !strings.Contains(string(raw), "memory_promoted") {
		t.Fatalf("expected replay to include later memory event, got %s", raw)
	}

	input.Reset()
	output.Reset()
	mustWriteJSONLine(t, &input, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "task.events",
		"params":  map[string]any{"task_id": spec.TaskID, "after": "EVT-missing"},
	})
	if err := server.Serve(context.Background(), &input, &output); err != nil {
		t.Fatalf("serve stale task.events: %v", err)
	}
	responses, _ = parseRPCOutput(t, output.String())
	if len(responses) != 1 || responses[0].Error == nil || !strings.Contains(responses[0].Error.Message, "event cursor not found") {
		t.Fatalf("expected stale cursor error, got responses=%+v body=%q", responses, output.String())
	}
}

func TestServerMemoryPromoteAndShow(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")
	cfg := task.DefaultConfig()
	svc := ngenrt.New(dir, cfg)
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "memory",
		Objective: "capture durable milestone",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "docs reviewed"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	server := Server{Service: svc}
	var input bytes.Buffer
	mustWriteJSONLine(t, &input, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "memory.promote",
		"params": map[string]any{
			"task_id": spec.TaskID,
			"summary": "Milestone api_key=supersecret repo truth captured.",
			"kind":    "milestone",
			"refs":    []string{"progress.md", "context/summary.md"},
		},
	})
	mustWriteJSONLine(t, &input, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "memory.show",
	})

	var output bytes.Buffer
	if err := server.Serve(context.Background(), &input, &output); err != nil {
		t.Fatalf("serve memory RPCs: %v", err)
	}
	responses, notifications := parseRPCOutput(t, output.String())
	if len(responses) != 2 || len(notifications) != 1 {
		t.Fatalf("expected two responses and one notification, got responses=%d notifications=%d body=%q", len(responses), len(notifications), output.String())
	}

	raw, err := json.Marshal(responses[0].Result)
	if err != nil {
		t.Fatalf("marshal memory promote result: %v", err)
	}
	var entry task.MemoryEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("unmarshal memory entry: %v", err)
	}
	if entry.Kind != task.MemoryKindTaskMilestone || entry.Source != task.MemorySourceOperator {
		t.Fatalf("expected operator milestone memory entry, got %+v", entry)
	}
	if strings.Contains(entry.Summary, "supersecret") {
		t.Fatalf("expected redacted summary, got %+v", entry)
	}
	if entry.Scope != "task" || entry.FreshnessStatus != "fresh" || entry.Confidence != "observed" || entry.LastValidatedRef == "" {
		t.Fatalf("expected memory governance metadata, got %+v", entry)
	}

	raw, err = json.Marshal(responses[1].Result)
	if err != nil {
		t.Fatalf("marshal memory show result: %v", err)
	}
	var memoryShow map[string]any
	if err := json.Unmarshal(raw, &memoryShow); err != nil {
		t.Fatalf("unmarshal memory show result: %v", err)
	}
	markdown, _ := memoryShow["markdown"].(string)
	if !strings.Contains(markdown, "[task_milestone/operator/fresh]") || strings.Contains(markdown, "supersecret") {
		t.Fatalf("expected redacted milestone in memory markdown, got %q", markdown)
	}

	notification := decodeNotification(t, notifications[0])
	if notification.Kind != "task.updated" {
		t.Fatalf("expected task.updated notification, got %+v", notification)
	}
	if notification.StatusSnapshot == nil || notification.StatusSnapshot.TaskID != spec.TaskID {
		t.Fatalf("expected task status snapshot in notification, got %+v", notification)
	}
}

func TestServerTaskCreate(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")
	cfg := task.DefaultConfig()
	svc := ngenrt.New(dir, cfg)
	if _, err := svc.UpdateProject(context.Background(), task.ProjectUpdate{
		Explanation: "Track the durable docs rollout.",
		Steps: []task.ProjectExecutionStep{
			{
				ID:       "phase.docs",
				Title:    "Author docs task",
				Status:   task.ProjectStepStatusPending,
				BranchID: "branch.docs",
			},
		},
		Branches: []task.ProjectBranchSpec{
			{
				ID:     "branch.docs",
				Title:  "Docs branch",
				Status: task.ProjectBranchStatusPending,
			},
		},
	}, task.StepSourceOperator); err != nil {
		t.Fatalf("seed project graph: %v", err)
	}

	server := Server{Service: svc}
	var input bytes.Buffer
	mustWriteJSONLine(t, &input, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "task.create",
		"params": map[string]any{
			"kind":              string(task.KindGeneral),
			"preset_id":         string(task.PresetDocsLite),
			"title":             "docs task",
			"objective":         "sync the operator guide",
			"criteria":          []string{"docs reviewed"},
			"constraints":       []string{"do not edit generated files"},
			"project_step_id":   "phase.docs",
			"project_branch_id": "branch.docs",
		},
	})
	mustWriteJSONLine(t, &input, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "task.list",
	})
	mustWriteJSONLine(t, &input, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "project.get",
	})

	var output bytes.Buffer
	if err := server.Serve(context.Background(), &input, &output); err != nil {
		t.Fatalf("serve task.create: %v", err)
	}
	responses, notifications := parseRPCOutput(t, output.String())
	if len(responses) != 3 || len(notifications) != 1 {
		t.Fatalf("expected three responses and one notification, got responses=%d notifications=%d body=%q", len(responses), len(notifications), output.String())
	}

	var created task.TaskView
	raw, err := json.Marshal(responses[0].Result)
	if err != nil {
		t.Fatalf("marshal task.create result: %v", err)
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("unmarshal task.create result: %v", err)
	}
	if created.Task.Kind != task.KindGeneral || created.Task.PresetID != task.PresetDocsLite {
		t.Fatalf("expected created docs task, got %+v", created.Task)
	}
	if created.Status.State != task.StateActive {
		t.Fatalf("expected newly created task to remain active, got %+v", created.Status)
	}

	notification := decodeNotification(t, notifications[0])
	if notification.Kind != "task.created" || notification.StatusSnapshot == nil || notification.StatusSnapshot.TaskID != created.Task.TaskID {
		t.Fatalf("expected task.created notification for the new task, got %+v", notification)
	}

	var listed []task.TaskListEntry
	raw, err = json.Marshal(responses[1].Result)
	if err != nil {
		t.Fatalf("marshal task.list result: %v", err)
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatalf("unmarshal task.list result: %v", err)
	}
	if len(listed) != 1 || listed[0].TaskID != created.Task.TaskID {
		t.Fatalf("expected task.list to include the created task, got %+v", listed)
	}

	var project task.ProjectView
	raw, err = json.Marshal(responses[2].Result)
	if err != nil {
		t.Fatalf("marshal project.get result: %v", err)
	}
	if err := json.Unmarshal(raw, &project); err != nil {
		t.Fatalf("unmarshal project.get result: %v", err)
	}
	if len(project.Project.Steps) != 1 || len(project.Project.Branches) != 1 {
		t.Fatalf("expected explicit project graph to stay singular, got %+v", project.Project)
	}
	if project.Project.Steps[0].TaskID != created.Task.TaskID || project.Project.Branches[0].TaskID != created.Task.TaskID {
		t.Fatalf("expected project bindings to point at created task, got %+v %+v", project.Project.Steps[0], project.Project.Branches[0])
	}
}

func TestServerSessionSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")
	cfg := task.DefaultConfig()
	svc := ngenrt.New(dir, cfg)
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Objective: "review docs",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "docs reviewed"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	session, err := svc.StartSession(context.Background(), spec.TaskID, "acp")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	if _, _, _, err := svc.PromptSession(context.Background(), session.SessionID, "/run"); err != nil {
		t.Fatalf("prompt session: %v", err)
	}

	var input bytes.Buffer
	mustWriteJSONLine(t, &input, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "session.snapshot",
		"params":  map[string]any{"session_id": session.SessionID},
	})

	var output bytes.Buffer
	server := Server{Service: svc}
	if err := server.Serve(context.Background(), &input, &output); err != nil {
		t.Fatalf("serve session snapshot: %v", err)
	}
	responses, notifications := parseRPCOutput(t, output.String())
	if len(responses) != 1 || len(notifications) != 0 {
		t.Fatalf("expected one response and no notifications, got responses=%d notifications=%d body=%q", len(responses), len(notifications), output.String())
	}
	resp := responses[0]
	if resp.Error != nil {
		t.Fatalf("unexpected snapshot error: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal snapshot result: %v", err)
	}
	var snapshot task.SessionSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatalf("unmarshal session snapshot: %v", err)
	}
	if snapshot.ObjectKind != "session_snapshot" {
		t.Fatalf("expected session_snapshot object, got %s", snapshot.ObjectKind)
	}
	if snapshot.SessionID != session.SessionID {
		t.Fatalf("expected session id %s, got %s", session.SessionID, snapshot.SessionID)
	}
	if snapshot.StatusSnapshot.State != task.StateDone {
		t.Fatalf("expected done task state, got %s", snapshot.StatusSnapshot.State)
	}
	if snapshot.MessageCount != 2 {
		t.Fatalf("expected two session messages, got %d", snapshot.MessageCount)
	}
	if len(snapshot.RecentMessages) != 2 {
		t.Fatalf("expected two recent messages, got %d", len(snapshot.RecentMessages))
	}
	if snapshot.RecentMessages[0].Role != "operator" || snapshot.RecentMessages[1].Role != "runtime" {
		t.Fatalf("unexpected recent message roles: %+v", snapshot.RecentMessages)
	}
	if !strings.HasPrefix(snapshot.SessionRef, "workspace:.ngen/sessions/") {
		t.Fatalf("expected workspace session ref, got %s", snapshot.SessionRef)
	}
	if !strings.HasSuffix(snapshot.MessagesRef, ".messages.jsonl") {
		t.Fatalf("expected messages ref, got %s", snapshot.MessagesRef)
	}
}

func TestServerTaskListGetAndUpdate(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")
	cfg := task.DefaultConfig()
	svc := ngenrt.New(dir, cfg)
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "mutable plan",
		Objective: "review docs",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "docs reviewed"},
			{ID: "SC-002", Statement: "handoff captured"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	var input bytes.Buffer
	mustWriteJSONLine(t, &input, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "task.update",
		"params": map[string]any{
			"task_id":     spec.TaskID,
			"explanation": "Track long-running work with a mutable plan.",
			"steps": []map[string]any{
				{
					"id":       "epic.repo_truth",
					"priority": "high",
					"title":    "Inspect repo truth",
					"status":   "in_progress",
					"covers":   []string{"SC-001"},
					"notes":    "Start with README.",
				},
				{
					"id":             "closeout",
					"parent_step_id": "epic.repo_truth",
					"depends_on":     []string{"epic.repo_truth"},
					"title":          "Refresh handoff",
					"status":         "pending",
					"covers":         []string{"SC-002"},
					"notes":          "",
				},
			},
		},
	})
	mustWriteJSONLine(t, &input, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "task.get",
		"params":  map[string]any{"task_id": spec.TaskID},
	})
	mustWriteJSONLine(t, &input, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "task.list",
	})

	var output bytes.Buffer
	server := Server{Service: svc}
	if err := server.Serve(context.Background(), &input, &output); err != nil {
		t.Fatalf("serve task list/get/update: %v", err)
	}
	responses, notifications := parseRPCOutput(t, output.String())
	if len(responses) != 3 || len(notifications) != 1 {
		t.Fatalf("expected three responses and one notification, got responses=%d notifications=%d body=%q", len(responses), len(notifications), output.String())
	}
	updateNote := decodeNotification(t, notifications[0])
	if updateNote.Kind != "task.updated" || updateNote.StatusSnapshot == nil || updateNote.StatusSnapshot.CurrentExecutionStepID != "epic.repo_truth" {
		t.Fatalf("expected task.updated notification with current execution step, got %+v", updateNote)
	}

	var view task.TaskView
	raw, err := json.Marshal(responses[1].Result)
	if err != nil {
		t.Fatalf("marshal task.get result: %v", err)
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("unmarshal task.get result: %v", err)
	}
	if view.Plan.Explanation != "Track long-running work with a mutable plan." {
		t.Fatalf("expected persisted mutable-plan explanation, got %+v", view.Plan)
	}
	if view.Plan.Revision != 1 || !strings.Contains(view.Plan.LastMutationRef, "plan_updates.jsonl#mutation_id=") {
		t.Fatalf("expected revisioned mutable-plan history, got %+v", view.Plan)
	}
	if view.Plan.CurrentExecutionStepID != "epic.repo_truth" || view.Status.CurrentSystemStepID != "STEP-001" {
		t.Fatalf("expected task.get to expose mutable/system step ids, got %+v", view)
	}
	if strings.Join(view.Plan.BlockedExecutionStepIDs, ",") != "closeout" {
		t.Fatalf("expected dependency-blocked child step in task.get, got %+v", view.Plan)
	}

	var listed []task.TaskListEntry
	raw, err = json.Marshal(responses[2].Result)
	if err != nil {
		t.Fatalf("marshal task.list result: %v", err)
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatalf("unmarshal task.list result: %v", err)
	}
	if len(listed) != 1 || listed[0].TaskID != spec.TaskID {
		t.Fatalf("expected one task list entry, got %+v", listed)
	}
	if listed[0].PlanRevision != 1 || listed[0].CurrentExecutionStepID != "epic.repo_truth" || listed[0].CurrentSystemStepID != "STEP-001" {
		t.Fatalf("expected task.list to expose mutable/system step ids, got %+v", listed[0])
	}
}

func TestServerTaskPatch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")
	cfg := task.DefaultConfig()
	svc := ngenrt.New(dir, cfg)
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "mutable plan patch",
		Objective: "review docs",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "docs reviewed"},
			{ID: "SC-002", Statement: "handoff captured"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := svc.UpdateTaskPlan(context.Background(), spec.TaskID, task.PlanUpdate{
		Explanation: "Track long-running work with a mutable plan.",
		Steps: []task.ExecutionPlanStep{
			{
				ID:       "epic.repo_truth",
				Priority: task.StepPriorityHigh,
				Title:    "Inspect repo truth",
				Status:   task.StepStatusInProgress,
				Covers:   []string{"SC-001"},
				Notes:    "Start with README.",
			},
			{
				ID:     "legacy.closeout",
				Title:  "Legacy closeout",
				Status: task.StepStatusPending,
				Covers: []string{"SC-002"},
				Notes:  "",
			},
		},
	}, task.StepSourceOperator); err != nil {
		t.Fatalf("seed mutable plan: %v", err)
	}

	var input bytes.Buffer
	mustWriteJSONLine(t, &input, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "task.patch",
		"params": map[string]any{
			"task_id": spec.TaskID,
			"operations": []map[string]any{
				{
					"op":          "set_explanation",
					"explanation": "Refine the mutable plan through incremental graph mutations.",
				},
				{
					"op":            "upsert_step",
					"after_step_id": "epic.repo_truth",
					"step": map[string]any{
						"id":             "handoff.closeout",
						"parent_step_id": "epic.repo_truth",
						"depends_on":     []string{"epic.repo_truth"},
						"priority":       "high",
						"title":          "Refresh handoff",
						"status":         "pending",
						"covers":         []string{"SC-002"},
						"notes":          "Prefer patch over full rewrite.",
					},
				},
				{
					"op":      "remove_step",
					"step_id": "legacy.closeout",
				},
			},
		},
	})

	var output bytes.Buffer
	server := Server{Service: svc}
	if err := server.Serve(context.Background(), &input, &output); err != nil {
		t.Fatalf("serve task patch: %v", err)
	}
	responses, notifications := parseRPCOutput(t, output.String())
	if len(responses) != 1 || len(notifications) != 1 {
		t.Fatalf("expected one response and one notification, got responses=%d notifications=%d body=%q", len(responses), len(notifications), output.String())
	}
	updateNote := decodeNotification(t, notifications[0])
	if updateNote.Kind != "task.updated" || updateNote.StatusSnapshot == nil || updateNote.StatusSnapshot.PlanRevision != 2 {
		t.Fatalf("expected task.updated notification with revision 2, got %+v", updateNote)
	}

	var view task.TaskView
	raw, err := json.Marshal(responses[0].Result)
	if err != nil {
		t.Fatalf("marshal task.patch result: %v", err)
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("unmarshal task.patch result: %v", err)
	}
	if view.Plan.Revision != 2 || view.Plan.Explanation != "Refine the mutable plan through incremental graph mutations." {
		t.Fatalf("expected patched plan revision/explanation, got %+v", view.Plan)
	}
	if view.Plan.Steps[2].ID != "handoff.closeout" {
		t.Fatalf("expected patched execution step in merged plan, got %+v", view.Plan.Steps)
	}
	for _, step := range view.Plan.Steps {
		if step.ID == "legacy.closeout" {
			t.Fatalf("expected legacy step to be removed, got %+v", view.Plan.Steps)
		}
	}
}

func TestServerProjectGetUpdateAndPatch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# project\n")
	cfg := task.DefaultConfig()
	svc := ngenrt.New(dir, cfg)
	first, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "repo truth",
		Objective: "inspect repo",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "repo truth captured"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create first task: %v", err)
	}
	second, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "patch lane",
		Objective: "apply patch",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "patch applied"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create second task: %v", err)
	}

	var input bytes.Buffer
	mustWriteJSONLine(t, &input, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "project.update",
		"params": map[string]any{
			"explanation": "Coordinate the workspace through a durable project graph.",
			"steps": []map[string]any{
				{
					"id":        "epic.repo_truth",
					"priority":  "high",
					"title":     "Inspect repo truth",
					"status":    "in_progress",
					"branch_id": "branch.repo",
					"task_id":   first.TaskID,
					"notes":     "Primary lane.",
				},
				{
					"id":             "epic.patch",
					"parent_step_id": "epic.repo_truth",
					"depends_on":     []string{"epic.repo_truth"},
					"title":          "Apply patch",
					"status":         "pending",
					"branch_id":      "branch.patch",
					"task_id":        second.TaskID,
					"notes":          "",
				},
			},
			"branches": []map[string]any{
				{
					"id":      "branch.repo",
					"title":   "Repo lane",
					"status":  "active",
					"task_id": first.TaskID,
					"notes":   "Primary lane.",
				},
				{
					"id":      "branch.patch",
					"title":   "Patch lane",
					"status":  "pending",
					"task_id": second.TaskID,
					"notes":   "",
				},
			},
		},
	})
	mustWriteJSONLine(t, &input, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "project.patch",
		"params": map[string]any{
			"operations": []map[string]any{
				{
					"op":        "set_branch_status",
					"branch_id": "branch.patch",
					"status":    "active",
				},
			},
		},
	})
	mustWriteJSONLine(t, &input, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "project.get",
	})

	var output bytes.Buffer
	server := Server{Service: svc}
	if err := server.Serve(context.Background(), &input, &output); err != nil {
		t.Fatalf("serve project graph methods: %v", err)
	}
	responses, notifications := parseRPCOutput(t, output.String())
	if len(responses) != 3 || len(notifications) != 2 {
		t.Fatalf("expected three responses and two notifications, got responses=%d notifications=%d body=%q", len(responses), len(notifications), output.String())
	}
	updateNote := decodeNotification(t, notifications[0])
	if updateNote.Kind != "project.updated" {
		t.Fatalf("expected project.updated notification, got %+v", updateNote)
	}
	patchNote := decodeNotification(t, notifications[1])
	if patchNote.Kind != "project.updated" {
		t.Fatalf("expected project.updated notification for patch, got %+v", patchNote)
	}

	var view task.ProjectView
	raw, err := json.Marshal(responses[2].Result)
	if err != nil {
		t.Fatalf("marshal project.get result: %v", err)
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("unmarshal project.get result: %v", err)
	}
	if len(view.Project.Steps) != 2 || len(view.Project.Branches) != 2 {
		t.Fatalf("expected explicit project graph, got %+v", view.Project)
	}
	if !containsString(view.Project.ActiveBranchIDs, "branch.patch") {
		t.Fatalf("expected patch branch to be active after project.patch, got %+v", view.Project)
	}
}

func TestServerInputRequestLifecycle(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")
	cfg := task.DefaultConfig()
	svc := ngenrt.New(dir, cfg)
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Objective: "collect input",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "input durable"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	var input bytes.Buffer
	mustWriteJSONLine(t, &input, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "input.request",
		"params":  map[string]any{"task_id": spec.TaskID, "field": "target", "prompt": "Provide target"},
	})

	var output bytes.Buffer
	server := Server{Service: svc}
	if err := server.Serve(context.Background(), &input, &output); err != nil {
		t.Fatalf("serve input request: %v", err)
	}
	responses, notifications := parseRPCOutput(t, output.String())
	if len(responses) != 1 || len(notifications) != 1 {
		t.Fatalf("expected one request response and one notification, got responses=%d notifications=%d body=%q", len(responses), len(notifications), output.String())
	}
	requestResp := responses[0]
	if requestResp.Error != nil {
		t.Fatalf("unexpected input request error: %+v", requestResp.Error)
	}
	raw, err := json.Marshal(requestResp.Result)
	if err != nil {
		t.Fatalf("marshal input request result: %v", err)
	}
	var requested task.InputRequestRecord
	if err := json.Unmarshal(raw, &requested); err != nil {
		t.Fatalf("unmarshal input request record: %v", err)
	}
	if requested.Status != "pending" || requested.RequestID == "" {
		t.Fatalf("expected pending input request, got %+v", requested)
	}
	requestNote := decodeNotification(t, notifications[0])
	if requestNote.Kind != "input.updated" {
		t.Fatalf("expected input.updated notification, got %+v", requestNote)
	}
	if requestNote.StatusSnapshot == nil || requestNote.StatusSnapshot.State != task.StateBlocked || requestNote.StatusSnapshot.StatusReasonCode != "blocked_missing_input" {
		t.Fatalf("expected blocked_missing_input notification snapshot, got %+v", requestNote.StatusSnapshot)
	}

	output.Reset()
	var listInput bytes.Buffer
	mustWriteJSONLine(t, &listInput, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "input.list",
		"params":  map[string]any{"task_id": spec.TaskID},
	})
	if err := server.Serve(context.Background(), &listInput, &output); err != nil {
		t.Fatalf("serve input list: %v", err)
	}
	responses, notifications = parseRPCOutput(t, output.String())
	if len(responses) != 1 || len(notifications) != 0 {
		t.Fatalf("expected one list response and no notifications, got responses=%d notifications=%d body=%q", len(responses), len(notifications), output.String())
	}
	listResp := responses[0]
	raw, err = json.Marshal(listResp.Result)
	if err != nil {
		t.Fatalf("marshal input list result: %v", err)
	}
	var records []task.InputRequestRecord
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatalf("unmarshal input records: %v", err)
	}
	if len(records) != 1 || records[0].RequestID != requested.RequestID {
		t.Fatalf("expected listed pending request, got %+v", records)
	}

	output.Reset()
	var respondInput bytes.Buffer
	mustWriteJSONLine(t, &respondInput, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "input.respond",
		"params":  map[string]any{"task_id": spec.TaskID, "request_id": requested.RequestID, "response": "/tmp/target"},
	})
	if err := server.Serve(context.Background(), &respondInput, &output); err != nil {
		t.Fatalf("serve input respond: %v", err)
	}
	responses, notifications = parseRPCOutput(t, output.String())
	if len(responses) != 1 || len(notifications) != 1 {
		t.Fatalf("expected one respond response and one notification, got responses=%d notifications=%d body=%q", len(responses), len(notifications), output.String())
	}
	respondResp := responses[0]
	raw, err = json.Marshal(respondResp.Result)
	if err != nil {
		t.Fatalf("marshal input respond result: %v", err)
	}
	var answered task.InputRequestRecord
	if err := json.Unmarshal(raw, &answered); err != nil {
		t.Fatalf("unmarshal answered input record: %v", err)
	}
	if answered.Status != "answered" {
		t.Fatalf("expected answered input record, got %+v", answered)
	}
	respondNote := decodeNotification(t, notifications[0])
	if respondNote.StatusSnapshot == nil || respondNote.StatusSnapshot.State != task.StateActive || respondNote.StatusSnapshot.StatusReasonCode != "" {
		t.Fatalf("expected active notification snapshot after respond, got %+v", respondNote.StatusSnapshot)
	}

	status, err := svc.Status(context.Background(), spec.TaskID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.State != task.StateActive || status.StatusReasonCode != "" {
		t.Fatalf("expected active task after input response, got state=%s reason=%s", status.State, status.StatusReasonCode)
	}
}

func TestServerApprovalLifecycle(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# approval flow\n")
	cfg := task.DefaultConfig()
	svc := ngenrt.New(dir, cfg)
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Objective: "collect approval",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "approval durable"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	server := Server{Service: svc}
	var input bytes.Buffer
	mustWriteJSONLine(t, &input, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "permission.request",
		"params":  map[string]any{"task_id": spec.TaskID, "scope": "manual step", "reason": "test"},
	})

	var output bytes.Buffer
	if err := server.Serve(context.Background(), &input, &output); err != nil {
		t.Fatalf("serve permission request: %v", err)
	}
	responses, notifications := parseRPCOutput(t, output.String())
	if len(responses) != 1 || len(notifications) != 1 {
		t.Fatalf("expected one request response and one notification, got responses=%d notifications=%d body=%q", len(responses), len(notifications), output.String())
	}
	requestResp := responses[0]
	if requestResp.Error != nil {
		t.Fatalf("unexpected permission request error: %+v", requestResp.Error)
	}
	raw, err := json.Marshal(requestResp.Result)
	if err != nil {
		t.Fatalf("marshal permission request result: %v", err)
	}
	var requested task.ApprovalRecord
	if err := json.Unmarshal(raw, &requested); err != nil {
		t.Fatalf("unmarshal approval request: %v", err)
	}
	if requested.Status != "pending" || requested.ApprovalID == "" {
		t.Fatalf("expected pending approval request, got %+v", requested)
	}
	requestNote := decodeNotification(t, notifications[0])
	if requestNote.Kind != "approval.updated" {
		t.Fatalf("expected approval.updated notification, got %+v", requestNote)
	}
	if requestNote.StatusSnapshot == nil || requestNote.StatusSnapshot.State != task.StateBlocked || requestNote.StatusSnapshot.StatusReasonCode != "blocked_policy" {
		t.Fatalf("expected blocked_policy notification snapshot, got %+v", requestNote.StatusSnapshot)
	}
	if requestNote.Approval == nil || requestNote.Approval.ApprovalID != requested.ApprovalID {
		t.Fatalf("expected approval payload in notification, got %+v", requestNote)
	}

	output.Reset()
	var listInput bytes.Buffer
	mustWriteJSONLine(t, &listInput, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "permission.list",
		"params":  map[string]any{"task_id": spec.TaskID},
	})
	if err := server.Serve(context.Background(), &listInput, &output); err != nil {
		t.Fatalf("serve permission list: %v", err)
	}
	responses, notifications = parseRPCOutput(t, output.String())
	if len(responses) != 1 || len(notifications) != 0 {
		t.Fatalf("expected one list response and no notifications, got responses=%d notifications=%d body=%q", len(responses), len(notifications), output.String())
	}
	raw, err = json.Marshal(responses[0].Result)
	if err != nil {
		t.Fatalf("marshal permission list result: %v", err)
	}
	var records []task.ApprovalRecord
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatalf("unmarshal approval records: %v", err)
	}
	if len(records) != 1 || records[0].ApprovalID != requested.ApprovalID || records[0].Status != "pending" {
		t.Fatalf("expected pending approval history, got %+v", records)
	}

	output.Reset()
	var decideInput bytes.Buffer
	mustWriteJSONLine(t, &decideInput, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "permission.decide",
		"params":  map[string]any{"task_id": spec.TaskID, "approval_id": requested.ApprovalID, "decision": "approved"},
	})
	if err := server.Serve(context.Background(), &decideInput, &output); err != nil {
		t.Fatalf("serve permission decide: %v", err)
	}
	responses, notifications = parseRPCOutput(t, output.String())
	if len(responses) != 1 || len(notifications) != 1 {
		t.Fatalf("expected one decide response and one notification, got responses=%d notifications=%d body=%q", len(responses), len(notifications), output.String())
	}
	raw, err = json.Marshal(responses[0].Result)
	if err != nil {
		t.Fatalf("marshal permission decide result: %v", err)
	}
	var decided task.ApprovalRecord
	if err := json.Unmarshal(raw, &decided); err != nil {
		t.Fatalf("unmarshal approval decision: %v", err)
	}
	if decided.Status != "approved" {
		t.Fatalf("expected approved decision record, got %+v", decided)
	}
	decideNote := decodeNotification(t, notifications[0])
	if decideNote.StatusSnapshot == nil || decideNote.StatusSnapshot.State != task.StateActive || decideNote.StatusSnapshot.StatusReasonCode != "" {
		t.Fatalf("expected active notification snapshot after approval, got %+v", decideNote.StatusSnapshot)
	}
	if decideNote.Approval == nil || decideNote.Approval.Status != "approved" {
		t.Fatalf("expected approved approval payload in notification, got %+v", decideNote)
	}

	status, err := svc.Status(context.Background(), spec.TaskID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.State != task.StateActive || status.StatusReasonCode != "" {
		t.Fatalf("expected active task after approval decision, got state=%s reason=%s", status.State, status.StatusReasonCode)
	}
}

func TestServerParentOwnedWorkerApprovalLifecycle(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "demo.go"), "package demo\n\nfunc Add(a, b int) int { return a + b }\n")
	cfg := task.DefaultConfig()
	svc := ngenrt.New(dir, cfg)
	parent, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     "parent",
		Objective: "verify coding",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "go test passes"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	if _, _, err := svc.Run(context.Background(), parent.TaskID); err != nil {
		t.Fatalf("run parent: %v", err)
	}
	worker, err := svc.SpawnWorker(context.Background(), parent.TaskID, "reviewer", "review parent changes")
	if err != nil {
		t.Fatalf("spawn worker: %v", err)
	}

	server := Server{Service: svc}
	var output bytes.Buffer

	var requestInput bytes.Buffer
	mustWriteJSONLine(t, &requestInput, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "permission.request",
		"params":  map[string]any{"task_id": worker.ChildTaskID, "scope": "manual step", "reason": "worker asks parent"},
	})
	if err := server.Serve(context.Background(), &requestInput, &output); err != nil {
		t.Fatalf("serve child permission request: %v", err)
	}
	responses, notifications := parseRPCOutput(t, output.String())
	if len(responses) != 1 || len(notifications) != 1 {
		t.Fatalf("expected one request response and one notification, got responses=%d notifications=%d body=%q", len(responses), len(notifications), output.String())
	}
	requested := decodeApprovalRecordResult(t, responses[0].Result)
	if requested.Status != "pending" || requested.OwnerTaskID != parent.TaskID || requested.OwnerWorkerID != worker.WorkerID {
		t.Fatalf("expected parent-owned pending approval, got %+v", requested)
	}
	requestNote := decodeNotification(t, notifications[0])
	if requestNote.TaskID != worker.ChildTaskID || requestNote.WorkerSnapshot != nil {
		t.Fatalf("expected child-scoped request notification without worker snapshot, got %+v", requestNote)
	}
	if requestNote.StatusSnapshot == nil || requestNote.StatusSnapshot.State != task.StateBlocked || requestNote.StatusSnapshot.StatusReasonCode != "blocked_policy" {
		t.Fatalf("expected blocked child status in request notification, got %+v", requestNote.StatusSnapshot)
	}

	output.Reset()
	var syncBlockedInput bytes.Buffer
	mustWriteJSONLine(t, &syncBlockedInput, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "worker.sync",
		"params":  map[string]any{"parent_task_id": parent.TaskID, "worker_id": worker.WorkerID},
	})
	if err := server.Serve(context.Background(), &syncBlockedInput, &output); err != nil {
		t.Fatalf("serve blocked worker sync: %v", err)
	}
	responses, notifications = parseRPCOutput(t, output.String())
	if len(responses) != 1 || len(notifications) != 1 {
		t.Fatalf("expected one blocked worker sync response and one notification, got responses=%d notifications=%d body=%q", len(responses), len(notifications), output.String())
	}
	blockedSnapshot := decodeWorkerSnapshotResult(t, responses[0].Result)
	if blockedSnapshot.Worker.BlockedReasonCode != "blocked_policy" || blockedSnapshot.Worker.ApprovalID != requested.ApprovalID || blockedSnapshot.Worker.ApprovalRef == "" {
		t.Fatalf("expected worker control metadata for blocked approval, got %+v", blockedSnapshot.Worker)
	}
	if blockedSnapshot.Worker.RequiresParentAction != true || blockedSnapshot.Worker.ParentActionType != "owned_approval_pending" {
		t.Fatalf("expected blocked worker to require parent action, got %+v", blockedSnapshot.Worker)
	}
	if !strings.Contains(blockedSnapshot.Worker.ParentActionSummary, "parent_takeover") {
		t.Fatalf("expected parent action summary on blocked worker, got %+v", blockedSnapshot.Worker)
	}

	output.Reset()
	var directListInput bytes.Buffer
	mustWriteJSONLine(t, &directListInput, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "permission.list",
		"params":  map[string]any{"task_id": parent.TaskID},
	})
	if err := server.Serve(context.Background(), &directListInput, &output); err != nil {
		t.Fatalf("serve parent direct permission list: %v", err)
	}
	responses, notifications = parseRPCOutput(t, output.String())
	if len(responses) != 1 || len(notifications) != 0 {
		t.Fatalf("expected one direct list response and no notifications, got responses=%d notifications=%d body=%q", len(responses), len(notifications), output.String())
	}
	records := decodeApprovalRecordListResult(t, responses[0].Result)
	if len(records) != 0 {
		t.Fatalf("expected no direct parent approvals, got %+v", records)
	}

	output.Reset()
	var ownedListInput bytes.Buffer
	mustWriteJSONLine(t, &ownedListInput, map[string]any{
		"jsonrpc": "2.0",
		"id":      4,
		"method":  "permission.list",
		"params":  map[string]any{"task_id": parent.TaskID, "include_owned": true},
	})
	if err := server.Serve(context.Background(), &ownedListInput, &output); err != nil {
		t.Fatalf("serve parent owned permission list: %v", err)
	}
	responses, notifications = parseRPCOutput(t, output.String())
	if len(responses) != 1 || len(notifications) != 0 {
		t.Fatalf("expected one owned list response and no notifications, got responses=%d notifications=%d body=%q", len(responses), len(notifications), output.String())
	}
	records = decodeApprovalRecordListResult(t, responses[0].Result)
	if len(records) != 1 || records[0].ApprovalID != requested.ApprovalID || records[0].TaskID != worker.ChildTaskID || records[0].OwnerTaskID != parent.TaskID || records[0].OwnerWorkerID != worker.WorkerID {
		t.Fatalf("expected parent-owned approval history, got %+v", records)
	}

	output.Reset()
	var decideInput bytes.Buffer
	mustWriteJSONLine(t, &decideInput, map[string]any{
		"jsonrpc": "2.0",
		"id":      5,
		"method":  "permission.decide",
		"params":  map[string]any{"task_id": parent.TaskID, "approval_id": requested.ApprovalID, "decision": "approved"},
	})
	if err := server.Serve(context.Background(), &decideInput, &output); err != nil {
		t.Fatalf("serve parent permission decide: %v", err)
	}
	responses, notifications = parseRPCOutput(t, output.String())
	if len(responses) != 1 || len(notifications) != 1 {
		t.Fatalf("expected one decide response and one notification, got responses=%d notifications=%d body=%q", len(responses), len(notifications), output.String())
	}
	decided := decodeApprovalRecordResult(t, responses[0].Result)
	if decided.Status != "approved" || decided.TaskID != worker.ChildTaskID || decided.OwnerTaskID != parent.TaskID || decided.OwnerWorkerID != worker.WorkerID {
		t.Fatalf("expected approved parent-owned child approval, got %+v", decided)
	}
	decideNote := decodeNotification(t, notifications[0])
	if decideNote.TaskID != parent.TaskID || decideNote.WorkerID != worker.WorkerID {
		t.Fatalf("expected parent-scoped approval notification, got %+v", decideNote)
	}
	if decideNote.StatusSnapshot == nil || decideNote.StatusSnapshot.TaskID != parent.TaskID || decideNote.StatusSnapshot.State != task.StateDone {
		t.Fatalf("expected parent status snapshot in approval notification, got %+v", decideNote.StatusSnapshot)
	}
	if decideNote.WorkerSnapshot == nil || decideNote.WorkerSnapshot.Worker.ChildTaskID != worker.ChildTaskID || decideNote.WorkerSnapshot.ChildStatus.State != task.StateActive {
		t.Fatalf("expected worker snapshot with active child after approval, got %+v", decideNote.WorkerSnapshot)
	}
	if decideNote.WorkerSnapshot.Worker.ParentActionType != "continue_child" || decideNote.WorkerSnapshot.Worker.RequiresParentAction != true {
		t.Fatalf("expected worker snapshot to advertise continuation after approval, got %+v", decideNote.WorkerSnapshot.Worker)
	}
	if decideNote.Approval == nil || decideNote.Approval.TaskID != worker.ChildTaskID || decideNote.Approval.OwnerTaskID != parent.TaskID {
		t.Fatalf("expected approval payload for owned child approval, got %+v", decideNote)
	}

	status, err := svc.Status(context.Background(), worker.ChildTaskID)
	if err != nil {
		t.Fatalf("child status: %v", err)
	}
	if status.State != task.StateActive || status.StatusReasonCode != "" {
		t.Fatalf("expected active child after parent-owned approval decision, got state=%s reason=%s", status.State, status.StatusReasonCode)
	}

	output.Reset()
	var continueInput bytes.Buffer
	mustWriteJSONLine(t, &continueInput, map[string]any{
		"jsonrpc": "2.0",
		"id":      6,
		"method":  "worker.continue",
		"params":  map[string]any{"parent_task_id": parent.TaskID, "worker_id": worker.WorkerID},
	})
	if err := server.Serve(context.Background(), &continueInput, &output); err != nil {
		t.Fatalf("serve worker continue: %v", err)
	}
	responses, notifications = parseRPCOutput(t, output.String())
	if len(responses) != 1 || len(notifications) != 1 {
		t.Fatalf("expected one continue response and one notification, got responses=%d notifications=%d body=%q", len(responses), len(notifications), output.String())
	}
	continued := decodeWorkerSnapshotResult(t, responses[0].Result)
	if continued.Worker.Status != "done" || continued.Worker.RequiresParentAction != false || continued.ChildStatus.State != task.StateDone {
		t.Fatalf("expected continued worker to finish cleanly, got %+v", continued)
	}
	continueNote := decodeNotification(t, notifications[0])
	if continueNote.WorkerSnapshot == nil || continueNote.WorkerSnapshot.ChildStatus.State != task.StateDone {
		t.Fatalf("expected worker continue notification with done child, got %+v", continueNote)
	}
}

func TestServerWorkerLifecycle(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "demo.go"), "package demo\n\nfunc Add(a, b int) int { return a + b }\n")
	cfg := task.DefaultConfig()
	svc := ngenrt.New(dir, cfg)
	parent, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     "parent",
		Objective: "verify coding",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "go test passes"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	if _, _, err := svc.Run(context.Background(), parent.TaskID); err != nil {
		t.Fatalf("run parent: %v", err)
	}

	server := Server{Service: svc}
	var input bytes.Buffer
	mustWriteJSONLine(t, &input, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "worker.spawn",
		"params":  map[string]any{"parent_task_id": parent.TaskID, "role": "reviewer", "objective": "review parent changes"},
	})

	var output bytes.Buffer
	if err := server.Serve(context.Background(), &input, &output); err != nil {
		t.Fatalf("serve worker spawn: %v", err)
	}
	responses, notifications := parseRPCOutput(t, output.String())
	if len(responses) != 1 || len(notifications) != 1 {
		t.Fatalf("expected one spawn response and one notification, got responses=%d notifications=%d body=%q", len(responses), len(notifications), output.String())
	}
	spawned := decodeWorkerSnapshotResult(t, responses[0].Result)
	if spawned.Worker.WorkerID == "" || spawned.Worker.ChildTaskID == "" {
		t.Fatalf("expected worker and child ids, got %+v", spawned)
	}
	if spawned.ChildStatus.State != task.StateActive {
		t.Fatalf("expected active child status after spawn, got %+v", spawned.ChildStatus)
	}
	spawnNote := decodeNotification(t, notifications[0])
	if spawnNote.Kind != "worker.updated" || spawnNote.WorkerSnapshot == nil {
		t.Fatalf("expected worker.updated notification, got %+v", spawnNote)
	}

	output.Reset()
	var listInput bytes.Buffer
	mustWriteJSONLine(t, &listInput, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "worker.list",
		"params":  map[string]any{"parent_task_id": parent.TaskID},
	})
	if err := server.Serve(context.Background(), &listInput, &output); err != nil {
		t.Fatalf("serve worker list: %v", err)
	}
	responses, notifications = parseRPCOutput(t, output.String())
	if len(responses) != 1 || len(notifications) != 0 {
		t.Fatalf("expected one worker.list response and no notifications, got responses=%d notifications=%d body=%q", len(responses), len(notifications), output.String())
	}
	listed := decodeWorkerSnapshotsResult(t, responses[0].Result)
	if len(listed) != 1 || listed[0].Worker.WorkerID != spawned.Worker.WorkerID {
		t.Fatalf("expected one worker snapshot, got %+v", listed)
	}

	if _, _, err := svc.Run(context.Background(), spawned.Worker.ChildTaskID); err != nil {
		t.Fatalf("run child: %v", err)
	}

	output.Reset()
	var syncInput bytes.Buffer
	mustWriteJSONLine(t, &syncInput, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "worker.sync",
		"params":  map[string]any{"parent_task_id": parent.TaskID, "worker_id": spawned.Worker.WorkerID},
	})
	if err := server.Serve(context.Background(), &syncInput, &output); err != nil {
		t.Fatalf("serve worker sync: %v", err)
	}
	responses, notifications = parseRPCOutput(t, output.String())
	if len(responses) != 1 || len(notifications) != 1 {
		t.Fatalf("expected one sync response and one notification, got responses=%d notifications=%d body=%q", len(responses), len(notifications), output.String())
	}
	synced := decodeWorkerSnapshotResult(t, responses[0].Result)
	if synced.Worker.Status != "done" || synced.ChildStatus.State != task.StateDone {
		t.Fatalf("expected synced done worker snapshot, got %+v", synced)
	}
	syncNote := decodeNotification(t, notifications[0])
	if syncNote.WorkerSnapshot == nil || syncNote.WorkerSnapshot.Worker.Status != "done" {
		t.Fatalf("expected done worker notification, got %+v", syncNote)
	}
}

func TestServerReturnsJSONRPCErrors(t *testing.T) {
	dir := t.TempDir()
	cfg := task.DefaultConfig()
	svc := ngenrt.New(dir, cfg)
	server := Server{Service: svc}

	var input bytes.Buffer
	mustWriteJSONLine(t, &input, map[string]any{
		"jsonrpc": "1.0",
		"id":      1,
		"method":  "task.status",
	})
	mustWriteJSONLine(t, &input, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "session.start",
		"params":  map[string]any{},
	})
	mustWriteJSONLine(t, &input, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "unknown.method",
	})

	var output bytes.Buffer
	if err := server.Serve(context.Background(), &input, &output); err != nil {
		t.Fatalf("serve invalid requests: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected three responses, got %d: %q", len(lines), output.String())
	}
	assertErrorCode(t, lines[0], -32600)
	assertErrorCode(t, lines[1], -32602)
	assertErrorCode(t, lines[2], -32601)
}

func assertErrorCode(t *testing.T, body string, want int) {
	t.Helper()
	var resp Response
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != want {
		t.Fatalf("expected error code %d, got %+v", want, resp.Error)
	}
}

func mustWriteJSONLine(t *testing.T, buf *bytes.Buffer, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	buf.Write(data)
	buf.WriteByte('\n')
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func parseRPCOutput(t *testing.T, body string) ([]Response, []Notification) {
	t.Helper()
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return nil, nil
	}
	lines := strings.Split(trimmed, "\n")
	responses := make([]Response, 0, len(lines))
	notifications := make([]Notification, 0, len(lines))
	for _, line := range lines {
		var envelope struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			t.Fatalf("unmarshal rpc envelope: %v", err)
		}
		if envelope.Method == "ngen.notification" {
			var notification Notification
			if err := json.Unmarshal([]byte(line), &notification); err != nil {
				t.Fatalf("unmarshal notification: %v", err)
			}
			notifications = append(notifications, notification)
			continue
		}
		var response Response
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		responses = append(responses, response)
	}
	return responses, notifications
}

func decodeNotification(t *testing.T, notification Notification) task.ACPNotification {
	t.Helper()
	data, err := json.Marshal(notification.Params)
	if err != nil {
		t.Fatalf("marshal notification params: %v", err)
	}
	var note task.ACPNotification
	if err := json.Unmarshal(data, &note); err != nil {
		t.Fatalf("unmarshal notification params: %v", err)
	}
	return note
}

func decodeWorkerSnapshotResult(t *testing.T, result any) task.WorkerSnapshot {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal worker snapshot result: %v", err)
	}
	var snapshot task.WorkerSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatalf("unmarshal worker snapshot result: %v", err)
	}
	return snapshot
}

func decodeApprovalRecordResult(t *testing.T, result any) task.ApprovalRecord {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal approval record result: %v", err)
	}
	var record task.ApprovalRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("unmarshal approval record result: %v", err)
	}
	return record
}

func decodeApprovalRecordListResult(t *testing.T, result any) []task.ApprovalRecord {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal approval record list: %v", err)
	}
	var records []task.ApprovalRecord
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("unmarshal approval record list: %v", err)
	}
	return records
}

func decodeWorkerSnapshotsResult(t *testing.T, result any) []task.WorkerSnapshot {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal worker snapshot list: %v", err)
	}
	var snapshots []task.WorkerSnapshot
	if err := json.Unmarshal(data, &snapshots); err != nil {
		t.Fatalf("unmarshal worker snapshot list: %v", err)
	}
	return snapshots
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
