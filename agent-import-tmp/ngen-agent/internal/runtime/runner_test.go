package ngenrt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ngen/internal/provider"
	"ngen/internal/task"
)

func TestStatusRecoveryPreservesTaskPermissionMode(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")

	svc := New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:             task.KindGeneral,
		PresetID:         task.PresetDocsLite,
		Objective:        "review docs",
		PermissionModeID: task.PermissionModeYolo,
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "docs reviewed"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	statePath := filepath.Join(dir, ".ngen", "tasks", spec.TaskID, "state.json")
	if err := os.Remove(statePath); err != nil {
		t.Fatalf("remove state: %v", err)
	}

	snapshot, err := svc.Status(context.Background(), spec.TaskID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if snapshot.State != task.StateFailed || snapshot.StatusReasonCode != "failed_state" {
		t.Fatalf("expected failed_state snapshot, got state=%s reason=%s", snapshot.State, snapshot.StatusReasonCode)
	}

	state, err := svc.Store.LoadState(spec.TaskID)
	if err != nil {
		t.Fatalf("load recovered state: %v", err)
	}
	if state.PermissionModeID != task.PermissionModeYolo {
		t.Fatalf("expected recovered permission mode %s, got %s", task.PermissionModeYolo, state.PermissionModeID)
	}
}

func TestCreateHydratesDefaultRoleContracts(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")

	svc := New(dir, task.DefaultConfig())
	if _, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     "role contracts",
		Objective: "hydrate default role contracts",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "go test passes"},
		},
		WorkspaceRoot: dir,
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}

	contracts, err := svc.Store.ReadRoleContracts()
	if err != nil {
		t.Fatalf("read role contracts: %v", err)
	}
	for _, role := range []string{string(task.KindCoding), string(task.KindGeneral), string(task.KindReviewer), string(task.KindSecurityReview)} {
		contract, ok := contracts[role]
		if !ok {
			t.Fatalf("expected hydrated role contract %s, got %+v", role, contracts)
		}
		if !task.RoleContractAllowsProviderAction(contract, "run") {
			t.Fatalf("expected role %s to allow run by default, got %+v", role, contract)
		}
	}
	if !task.RoleContractAllowsWorkerRole(contracts[string(task.KindCoding)], string(task.KindReviewer)) {
		t.Fatalf("expected coding role contract to preserve default reviewer delegation")
	}
	if task.RoleContractAllowsWorkerRole(contracts[string(task.KindReviewer)], string(task.KindCoding)) {
		t.Fatalf("expected reviewer role contract to preserve no-child-worker default")
	}
}

func TestAutoRejectsProviderActionForbiddenByRoleContract(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")

	svc := New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     "forbidden role action",
		Objective: "prove role contract gates provider actions",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "go test passes"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := svc.Store.SaveRoleContract(task.RoleContract{
		SchemaVersion:          task.SchemaVersion,
		RoleID:                 string(task.KindCoding),
		ProfileKind:            task.KindCoding,
		AllowedProviderActions: []string{"noop"},
	}); err != nil {
		t.Fatalf("save restrictive role contract: %v", err)
	}

	_, _, err = svc.Auto(context.Background(), spec.TaskID)
	if err == nil {
		t.Fatal("expected forbidden provider action to fail")
	}
	if !strings.Contains(err.Error(), "cannot select provider action") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSpawnWorkerRejectsRoleContractWorkerRole(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")

	svc := New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     "forbidden child role",
		Objective: "prove role contract gates child roles",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "go test passes"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := svc.Store.SaveRoleContract(task.RoleContract{
		SchemaVersion:          task.SchemaVersion,
		RoleID:                 string(task.KindCoding),
		ProfileKind:            task.KindCoding,
		AllowedProviderActions: task.SupportedProviderActions(),
		AllowedWorkerRoles:     []string{string(task.KindReviewer)},
	}); err != nil {
		t.Fatalf("save restrictive role contract: %v", err)
	}

	_, err = svc.SpawnWorker(context.Background(), spec.TaskID, string(task.KindCoding), "implement a child change")
	if err == nil {
		t.Fatal("expected forbidden worker role to fail")
	}
	if !strings.Contains(err.Error(), "cannot spawn worker role coding") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestListTasksSkipsTaskWithoutStateInsteadOfRecoveringFailedState(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")

	svc := New(dir, task.DefaultConfig())
	ready, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "ready task",
		Objective: "prove ready tasks still list",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "docs reviewed"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create ready task: %v", err)
	}

	if err := svc.Store.EnsureWorkspaceLayout(); err != nil {
		t.Fatalf("ensure workspace layout: %v", err)
	}
	partial := task.Spec{
		SchemaVersion: task.SchemaVersion,
		TaskID:        "TASK-partial",
		Kind:          task.KindCoding,
		Title:         "partial task",
		Objective:     "still materializing",
		WorkspaceRoot: dir,
	}
	if err := svc.Store.EnsureTaskLayout(partial.TaskID); err != nil {
		t.Fatalf("ensure partial task layout: %v", err)
	}
	if err := svc.Store.SaveTask(partial); err != nil {
		t.Fatalf("save partial task: %v", err)
	}

	listed, err := svc.ListTasks(context.Background())
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(listed) != 1 || listed[0].TaskID != ready.TaskID {
		t.Fatalf("expected only ready task to be listed, got %+v", listed)
	}
	if _, err := os.Stat(filepath.Join(dir, ".ngen", "tasks", partial.TaskID, "state.json")); !os.IsNotExist(err) {
		t.Fatalf("expected partial task to remain without recovered state, got err=%v", err)
	}
	diagnostics, err := filepath.Glob(filepath.Join(dir, ".ngen", "tasks", partial.TaskID, "diagnostics", "*.json"))
	if err != nil {
		t.Fatalf("glob diagnostics: %v", err)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("expected no diagnostics for skipped partial task, got %+v", diagnostics)
	}
}

func TestGetTaskDoesNotRewritePlanArtifact(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")

	svc := New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "read only view",
		Objective: "ensure GetTask is read only",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "docs reviewed"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	planPath := filepath.Join(dir, ".ngen", "tasks", spec.TaskID, "plan.json")
	before, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read plan before GetTask: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	view, err := svc.GetTask(context.Background(), spec.TaskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if view.Task.TaskID != spec.TaskID {
		t.Fatalf("expected task view for %s, got %+v", spec.TaskID, view)
	}

	after, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read plan after GetTask: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("expected GetTask to leave plan artifact untouched\nbefore:\n%s\nafter:\n%s", string(before), string(after))
	}
}

func TestPromptSessionRunSurvivesConcurrentGetTaskPolling(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/poll\n\ngo 1.24.2\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package poll\n\nfunc Add(a, b int) int { return a + b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package poll\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")

	cfg := task.DefaultConfig()
	cfg.Verification.CodingCommands = [][]string{{"bash", "-lc", "sleep 0.2 && go test ./..."}}
	svc := New(dir, cfg)
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     "poll during run",
		Objective: "exercise concurrent GetTask polling while /run is active",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "go test passes"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	session, err := svc.StartSession(context.Background(), spec.TaskID, "terminal")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		_, _, _, runErr := svc.PromptSession(context.Background(), session.SessionID, "/run")
		errCh <- runErr
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case runErr := <-errCh:
			if runErr != nil {
				t.Fatalf("prompt session /run: %v", runErr)
			}
			snapshot, err := svc.Status(context.Background(), spec.TaskID)
			if err != nil {
				t.Fatalf("status after concurrent polling: %v", err)
			}
			if snapshot.State != task.StateDone {
				t.Fatalf("expected task to finish done, got %+v", snapshot)
			}
			messages, err := svc.Store.ReadSessionMessages(session.SessionID)
			if err != nil {
				t.Fatalf("read session messages: %v", err)
			}
			for _, msg := range messages {
				if strings.Contains(msg.Content, "plan.json.tmp") || strings.Contains(msg.Content, "no such file or directory") {
					t.Fatalf("expected no plan temp-file collision in session transcript, got message %+v", msg)
				}
			}
			return
		default:
			if time.Now().After(deadline) {
				t.Fatal("timed out waiting for /run to finish under concurrent GetTask polling")
			}
			if _, err := svc.GetTask(context.Background(), spec.TaskID); err != nil {
				t.Fatalf("concurrent GetTask: %v", err)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func TestBuildProviderInputIncludesOwnedPendingApprovals(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# worker approvals\n")

	svc := New(dir, task.DefaultConfig())
	parent, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "parent",
		Objective: "manage workers",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "worker approvals are visible"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	worker, err := svc.SpawnWorker(context.Background(), parent.TaskID, "reviewer", "review parent output")
	if err != nil {
		t.Fatalf("spawn worker: %v", err)
	}
	record, err := svc.RequestApproval(context.Background(), worker.ChildTaskID, "manual step", "worker asks parent")
	if err != nil {
		t.Fatalf("request approval: %v", err)
	}
	updated, err := svc.SyncWorker(context.Background(), parent.TaskID, worker.WorkerID)
	if err != nil {
		t.Fatalf("sync worker: %v", err)
	}

	_, _, input, err := svc.buildProviderInput(context.Background(), parent.TaskID, nil)
	if err != nil {
		t.Fatalf("build provider input: %v", err)
	}
	if len(input.ManagedWorkers) != 1 {
		t.Fatalf("expected one managed worker, got %+v", input.ManagedWorkers)
	}
	managed := input.ManagedWorkers[0]
	if managed.WorkerID != worker.WorkerID || managed.RequiresParentAction != true || managed.ParentActionType != "owned_approval_pending" {
		t.Fatalf("expected actionable managed worker, got %+v", managed)
	}
	if managed.ApprovalID != record.ApprovalID || managed.ApprovalRef == "" {
		t.Fatalf("expected approval metadata on managed worker, got %+v", managed)
	}
	if managed.ApprovalStatus != "pending" || managed.ApprovalScope != "manual step" || managed.ApprovalReason != "worker asks parent" {
		t.Fatalf("expected approval detail on managed worker, got %+v", managed)
	}
	if managed.ResultRef == "" || !strings.Contains(strings.ToLower(managed.ResultSummary), "blocked") {
		t.Fatalf("expected managed worker result summary/ref, got %+v", managed)
	}
	if updated.ResultRef == "" || updated.ResultSummary == "" {
		t.Fatalf("expected synced worker to persist compiled result, got %+v", updated)
	}
	result, err := svc.Store.LoadWorkerResult(parent.TaskID, worker.WorkerID)
	if err != nil {
		t.Fatalf("load worker result: %v", err)
	}
	if result.ChildState != task.StateBlocked || result.SettlementStatus != "blocked" {
		t.Fatalf("expected blocked worker result, got %+v", result)
	}
	if result.Summary == "" || result.CompletionRef != "" {
		t.Fatalf("expected blocker-oriented worker result summary without completion ref, got %+v", result)
	}
	if result.BlockedReasonCode != "blocked_policy" || result.BlockedDetailRef == "" {
		t.Fatalf("expected blocked worker result detail refs, got %+v", result)
	}
	if result.ApprovalID != record.ApprovalID || result.ApprovalRef == "" || result.ApprovalStatus != "pending" {
		t.Fatalf("expected blocked worker approval detail in result, got %+v", result)
	}
	if result.ApprovalScope != "manual step" || result.ApprovalReason != "worker asks parent" {
		t.Fatalf("expected approval scope/reason in blocked worker result, got %+v", result)
	}
	if !result.RequiresParentAction || result.ParentActionType != "owned_approval_pending" || len(result.ParentActionOptions) != 3 {
		t.Fatalf("expected blocked worker result to preserve parent action metadata, got %+v", result)
	}
	if len(input.OwnedPendingApprovals) != 1 {
		t.Fatalf("expected one owned pending approval, got %+v", input.OwnedPendingApprovals)
	}
	owned := input.OwnedPendingApprovals[0]
	if owned.WorkerID != worker.WorkerID || owned.ChildTaskID != worker.ChildTaskID || owned.ApprovalID != record.ApprovalID {
		t.Fatalf("expected owned approval summary for worker, got %+v", owned)
	}
	if owned.ParentActionType != "owned_approval_pending" || owned.RequiresParentAction != true {
		t.Fatalf("expected owned approval to require parent action, got %+v", owned)
	}
	if input.ContextPack == nil || strings.TrimSpace(input.ContextPack.PackID) == "" {
		t.Fatalf("expected provider input to include context pack, got %+v", input.ContextPack)
	}
	if len(input.ContextPack.Sections) == 0 {
		t.Fatalf("expected provider input context pack sections, got %+v", input.ContextPack)
	}
	if !strings.Contains(input.WorkspaceMemory, "## Recent Memory Entries") {
		t.Fatalf("expected provider input workspace memory summary, got %q", input.WorkspaceMemory)
	}
}

func TestBuildProviderInputIncludesContinuitySnapshot(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# continuity\n")

	svc := New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "continuity",
		Objective: "preserve a durable restart ledger",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "docs reviewed"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	_, _, input, err := svc.buildProviderInput(context.Background(), spec.TaskID, nil)
	if err != nil {
		t.Fatalf("build provider input: %v", err)
	}
	if input.Continuity == nil || input.Continuity.SnapshotID == "" {
		t.Fatalf("expected continuity snapshot in provider input, got %+v", input.Continuity)
	}
	if len(input.Continuity.StartupChecklist) < 2 {
		t.Fatalf("expected continuity startup checklist, got %+v", input.Continuity)
	}
	if input.Continuity.CurrentFocus.CurrentSystemStepID == "" {
		t.Fatalf("expected continuity focus to include current system step, got %+v", input.Continuity.CurrentFocus)
	}
	if input.Continuity.CurrentFocus.PrimaryCriterionID != "SC-001" {
		t.Fatalf("expected continuity focus to expose a primary criterion, got %+v", input.Continuity.CurrentFocus)
	}
}

func TestCreateTracksTaskInProjectGraph(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# project graph\n")

	svc := New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "repo truth",
		Objective: "inspect the repository",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "repo truth captured"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	project, err := svc.Store.LoadProject()
	if err != nil {
		t.Fatalf("load project: %v", err)
	}
	if project.Revision == 0 || len(project.Steps) != 1 || len(project.Branches) != 1 {
		t.Fatalf("expected tracked task in project graph, got %+v", project)
	}
	if project.Steps[0].TaskID != spec.TaskID || project.Branches[0].TaskID != spec.TaskID {
		t.Fatalf("expected step/branch bindings for created task, got %+v %+v", project.Steps[0], project.Branches[0])
	}
	if project.Steps[0].BranchID != project.Branches[0].ID {
		t.Fatalf("expected tracked step to point at tracked branch, got %+v %+v", project.Steps[0], project.Branches[0])
	}
}

func TestCreateTaskBindsIntoExistingProjectGraph(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# project graph\n")

	svc := New(dir, task.DefaultConfig())
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

	view, err := svc.CreateTask(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "docs task",
		Objective: "update the docs checklist",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "docs reviewed"},
		},
		WorkspaceRoot: dir,
	}, task.StepSourceOperator, "phase.docs", "branch.docs")
	if err != nil {
		t.Fatalf("create bound task: %v", err)
	}

	project, err := svc.GetProject(context.Background())
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if len(project.Project.Steps) != 1 || len(project.Project.Branches) != 1 {
		t.Fatalf("expected explicit project graph to stay singular after binding, got %+v", project.Project)
	}
	if project.Project.Steps[0].TaskID != view.Task.TaskID || project.Project.Branches[0].TaskID != view.Task.TaskID {
		t.Fatalf("expected explicit step/branch bindings to point at created task, got %+v %+v", project.Project.Steps[0], project.Project.Branches[0])
	}
	if project.Project.Steps[0].ID != "phase.docs" || project.Project.Branches[0].ID != "branch.docs" {
		t.Fatalf("expected explicit graph ids to be preserved, got %+v", project.Project)
	}
}

func TestBuildProviderInputIncludesProjectGraph(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# project graph\n")

	svc := New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "repo truth",
		Objective: "inspect the repository",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "repo truth captured"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := svc.RequestInput(context.Background(), spec.TaskID, "target_path", "Provide target path", true); err != nil {
		t.Fatalf("request input: %v", err)
	}

	_, _, input, err := svc.buildProviderInput(context.Background(), spec.TaskID, nil)
	if err != nil {
		t.Fatalf("build provider input: %v", err)
	}
	if input.Project == nil {
		t.Fatal("expected provider input to include workspace project graph")
	}
	if len(input.Project.Steps) != 1 || len(input.Project.Branches) != 1 {
		t.Fatalf("expected one tracked project step/branch, got %+v", input.Project)
	}
	if input.Project.Steps[0].Status != task.ProjectStepStatusBlocked || input.Project.BlockedStepIDs[0] != input.Project.Steps[0].ID {
		t.Fatalf("expected blocked project step after input blocker, got %+v", input.Project)
	}
	if input.Project.Branches[0].Status != task.ProjectBranchStatusBlocked || input.Project.Branches[0].LastReasonCode != "blocked_missing_input" {
		t.Fatalf("expected blocked branch state to refresh from task blocker, got %+v", input.Project.Branches[0])
	}
}

func TestProjectFocusFlowsIntoNarrativeArtifactsAndProviderInput(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# project focus\n")

	svc := New(dir, task.DefaultConfig())
	if _, err := svc.UpdateProject(context.Background(), task.ProjectUpdate{
		Explanation: "Track a repo-truth step before the coding implementation step.",
		Steps: []task.ProjectExecutionStep{
			{
				ID:       "phase.repo_truth",
				Title:    "Capture repo truth",
				Status:   task.ProjectStepStatusPending,
				BranchID: "branch.repo_truth",
			},
			{
				ID:        "phase.impl",
				Title:     "Implement the bounded coding fix",
				Status:    task.ProjectStepStatusPending,
				BranchID:  "branch.impl",
				DependsOn: []string{"phase.repo_truth"},
			},
		},
		Branches: []task.ProjectBranchSpec{
			{ID: "branch.repo_truth", Title: "Repo truth branch", Status: task.ProjectBranchStatusPending},
			{ID: "branch.impl", Title: "Impl branch", Status: task.ProjectBranchStatusPending},
		},
	}, task.StepSourceOperator); err != nil {
		t.Fatalf("seed project graph: %v", err)
	}

	view, err := svc.CreateTask(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "impl",
		Objective: "ship the bounded implementation step",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "implementation step is ready"},
		},
		WorkspaceRoot: dir,
	}, task.StepSourceOperator, "phase.impl", "branch.impl")
	if err != nil {
		t.Fatalf("create bound task: %v", err)
	}

	_, _, input, err := svc.buildProviderInput(context.Background(), view.Task.TaskID, nil)
	if err != nil {
		t.Fatalf("build provider input: %v", err)
	}
	if input.ContextPack == nil || input.ContextPack.ProjectFocus == nil {
		t.Fatalf("expected context pack project focus, got %+v", input.ContextPack)
	}
	if input.ContextPack.ProjectFocus.PrimaryStepID != "phase.impl" || input.ContextPack.ProjectFocus.PrimaryBranchID != "branch.impl" {
		t.Fatalf("expected bound primary project focus, got %+v", input.ContextPack.ProjectFocus)
	}
	if !containsString(input.ContextPack.ProjectFocus.UnmetDependencyStepIDs, "phase.repo_truth") {
		t.Fatalf("expected unmet repo-truth dependency in context pack, got %+v", input.ContextPack.ProjectFocus)
	}
	if !containsString(input.ContextPack.IncludedRefs, "workspace:.ngen/project/project.json") {
		t.Fatalf("expected project artifact ref in context pack, got %+v", input.ContextPack.IncludedRefs)
	}
	if input.Continuity == nil || input.Continuity.CurrentFocus.ProjectFocus == nil {
		t.Fatalf("expected continuity project focus, got %+v", input.Continuity)
	}
	if input.Continuity.CurrentFocus.ProjectFocus.PrimaryStepID != "phase.impl" {
		t.Fatalf("expected continuity to retain primary project step, got %+v", input.Continuity.CurrentFocus.ProjectFocus)
	}
	if input.Sprint == nil || input.Sprint.ProjectFocus == nil {
		t.Fatalf("expected sprint project focus, got %+v", input.Sprint)
	}
	if !containsString(input.Sprint.ProjectFocus.Refs, "workspace:.ngen/project/project.json") {
		t.Fatalf("expected sprint project refs, got %+v", input.Sprint.ProjectFocus)
	}

	progress, err := svc.Store.LoadProgress(view.Task.TaskID)
	if err != nil {
		t.Fatalf("load progress: %v", err)
	}
	if !strings.Contains(string(progress), "## Project Focus") || !strings.Contains(string(progress), "Primary Step: phase.impl") || !strings.Contains(string(progress), "Unmet Dependencies: phase.repo_truth") {
		t.Fatalf("expected project focus section in progress, got %s", string(progress))
	}

	compacted, err := svc.Store.LoadContextCompactionSummary(view.Task.TaskID)
	if err != nil {
		t.Fatalf("load compacted context summary: %v", err)
	}
	if !strings.Contains(string(compacted), "## Project Focus") || !strings.Contains(string(compacted), "Primary Branch: branch.impl") {
		t.Fatalf("expected project focus section in context summary, got %s", string(compacted))
	}
}

func TestSyncWorkerPersistsBlockedMissingInputWorkerResult(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# worker input\n")

	svc := New(dir, task.DefaultConfig())
	parent, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "parent",
		Objective: "manage worker input",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "worker input blockers are visible"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	worker, err := svc.SpawnWorker(context.Background(), parent.TaskID, "general_execution", "inspect docs")
	if err != nil {
		t.Fatalf("spawn worker: %v", err)
	}
	requested, err := svc.RequestInput(context.Background(), worker.ChildTaskID, "target_path", "Provide target path", true)
	if err != nil {
		t.Fatalf("request input: %v", err)
	}

	updated, err := svc.SyncWorker(context.Background(), parent.TaskID, worker.WorkerID)
	if err != nil {
		t.Fatalf("sync worker: %v", err)
	}
	if updated.BlockedReasonCode != "blocked_missing_input" || updated.InputRequestID != requested.RequestID || updated.InputRequestRef == "" {
		t.Fatalf("expected input blocker on synced worker, got %+v", updated)
	}
	if updated.ParentActionType != "inspect_child" || !updated.RequiresParentAction {
		t.Fatalf("expected inspect_child parent action for input blocker, got %+v", updated)
	}
	if !strings.Contains(updated.ResultSummary, "Provide target path") {
		t.Fatalf("expected result summary to mention pending input prompt, got %+v", updated)
	}

	_, _, input, err := svc.buildProviderInput(context.Background(), parent.TaskID, nil)
	if err != nil {
		t.Fatalf("build provider input: %v", err)
	}
	if len(input.ManagedWorkers) != 1 {
		t.Fatalf("expected one managed worker, got %+v", input.ManagedWorkers)
	}
	managed := input.ManagedWorkers[0]
	if managed.InputRequestID != requested.RequestID || managed.InputRequestRef == "" || managed.InputField != "target_path" || managed.InputPrompt != "Provide target path" {
		t.Fatalf("expected input detail on managed worker, got %+v", managed)
	}

	result, err := svc.Store.LoadWorkerResult(parent.TaskID, worker.WorkerID)
	if err != nil {
		t.Fatalf("load worker result: %v", err)
	}
	if result.ChildState != task.StateBlocked || result.SettlementStatus != "blocked" {
		t.Fatalf("expected blocked worker result, got %+v", result)
	}
	if result.BlockedReasonCode != "blocked_missing_input" || result.BlockedDetailRef == "" {
		t.Fatalf("expected blocked input detail refs, got %+v", result)
	}
	if result.InputRequestID != requested.RequestID || result.InputRequestRef == "" || result.InputField != "target_path" || result.InputPrompt != "Provide target path" {
		t.Fatalf("expected pending input detail in worker result, got %+v", result)
	}
	if result.ApprovalRef != "" || result.ApprovalID != "" {
		t.Fatalf("expected blocked input result to avoid approval metadata, got %+v", result)
	}
	if !result.RequiresParentAction || result.ParentActionType != "inspect_child" || !strings.Contains(result.ParentActionSummary, "answer the input request directly") {
		t.Fatalf("expected blocked input result to preserve parent action metadata, got %+v", result)
	}
	if !strings.Contains(result.Summary, "Provide target path") {
		t.Fatalf("expected blocked input result summary to mention prompt, got %+v", result)
	}
}

func TestSyncWorkerPersistsApprovedContinuationWorkerResult(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# worker approvals\n")

	svc := New(dir, task.DefaultConfig())
	parent, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "parent",
		Objective: "manage workers",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "worker continuation surface is visible"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	worker, err := svc.SpawnWorker(context.Background(), parent.TaskID, "reviewer", "review parent output")
	if err != nil {
		t.Fatalf("spawn worker: %v", err)
	}
	record, err := svc.RequestApproval(context.Background(), worker.ChildTaskID, "manual step", "worker asks parent")
	if err != nil {
		t.Fatalf("request approval: %v", err)
	}
	if _, err := svc.DecideApproval(context.Background(), parent.TaskID, record.ApprovalID, "approved"); err != nil {
		t.Fatalf("approve child approval: %v", err)
	}

	updated, err := svc.SyncWorker(context.Background(), parent.TaskID, worker.WorkerID)
	if err != nil {
		t.Fatalf("sync worker: %v", err)
	}
	if updated.Status != "active" || updated.RequiresParentAction != true || updated.ParentActionType != "continue_child" {
		t.Fatalf("expected worker to wait on parent continuation, got %+v", updated)
	}
	if updated.ApprovalStatus != "approved" || updated.ApprovalScope != "manual step" || updated.ApprovalReason != "worker asks parent" {
		t.Fatalf("expected approved approval detail on worker, got %+v", updated)
	}
	if !strings.Contains(updated.ResultSummary, "worker continue") {
		t.Fatalf("expected continuation summary on worker result, got %+v", updated)
	}

	result, err := svc.Store.LoadWorkerResult(parent.TaskID, worker.WorkerID)
	if err != nil {
		t.Fatalf("load worker result: %v", err)
	}
	if result.ChildState != task.StateActive || result.SettlementStatus != "pending" {
		t.Fatalf("expected active worker result awaiting continuation, got %+v", result)
	}
	if result.ApprovalID != record.ApprovalID || result.ApprovalRef == "" || result.ApprovalStatus != "approved" {
		t.Fatalf("expected approved approval detail in worker result, got %+v", result)
	}
	if !result.RequiresParentAction || result.ParentActionType != "continue_child" || !strings.Contains(result.ParentActionSummary, "worker continue") {
		t.Fatalf("expected worker result to preserve continuation action metadata, got %+v", result)
	}
	if !strings.Contains(result.Summary, "ready for parent continuation") {
		t.Fatalf("expected continuation-oriented worker result summary, got %+v", result)
	}
}

func TestContinueWorkerPersistsAcceptedWorkerResult(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# parent docs\n")

	cfg := task.DefaultConfig()
	cfg.Subagents.WorkspaceIsolation = "snapshot_copy"
	svc := New(dir, cfg)
	parent, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "parent",
		Objective: "manage reviewer child",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "reviewer child produces a compiled result"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	worker, err := svc.SpawnWorker(context.Background(), parent.TaskID, "reviewer", "review parent docs")
	if err != nil {
		t.Fatalf("spawn worker: %v", err)
	}

	writeFile(t, filepath.Join(worker.WorkspaceRoot, "README.md"), "# reviewer draft\n")
	updated, err := svc.ContinueWorker(context.Background(), parent.TaskID, worker.WorkerID)
	if err != nil {
		t.Fatalf("continue worker: %v", err)
	}
	if updated.ResultRef == "" || updated.ResultSummary == "" {
		t.Fatalf("expected accepted worker to expose compiled result fields, got %+v", updated)
	}
	if updated.CompletionStatus != "accepted" || updated.ReviewStatus != "clear" || updated.VerificationStatus != "passed" {
		t.Fatalf("expected accepted worker result statuses, got %+v", updated)
	}
	if !updated.TrustedForParentCompletion || updated.EvidenceGrade != "complete" || updated.EvidenceScore < 90 {
		t.Fatalf("expected complete trusted worker evidence score, got %+v", updated)
	}

	result, err := svc.Store.LoadWorkerResult(parent.TaskID, worker.WorkerID)
	if err != nil {
		t.Fatalf("load worker result: %v", err)
	}
	if result.ChildState != task.StateDone || result.SettlementStatus != "accepted" {
		t.Fatalf("expected accepted worker result, got %+v", result)
	}
	if !result.TrustedForParentCompletion || result.EvidenceGrade != "complete" || len(result.MissingEvidence) != 0 {
		t.Fatalf("expected trusted worker result evidence, got %+v", result)
	}
	if result.HandoffRef == "" || result.CompletionRef == "" || result.ReviewRef == "" || result.VerificationRef == "" {
		t.Fatalf("expected compiled worker result refs, got %+v", result)
	}
	if len(result.EvidenceRefs) == 0 || !containsString(result.EvidenceRefs, "worker_runtime/"+worker.WorkerID+".settlement.json") {
		t.Fatalf("expected worker result evidence refs to include settlement, got %+v", result.EvidenceRefs)
	}
}

func TestBuildProviderInputIncludesSessionRecentMessages(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")

	svc := New(dir, task.DefaultConfig())
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
	session, err := svc.StartSession(context.Background(), spec.TaskID, "terminal")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	session, _, _, err = svc.PromptSession(context.Background(), session.SessionID, "/run")
	if err != nil {
		t.Fatalf("prompt session: %v", err)
	}

	_, _, input, err := svc.buildProviderInput(context.Background(), spec.TaskID, &session)
	if err != nil {
		t.Fatalf("build provider input: %v", err)
	}
	if input.SessionMessagesRef == "" || !strings.HasSuffix(input.SessionMessagesRef, ".messages.jsonl") {
		t.Fatalf("expected session_messages_ref in provider input, got %+v", input)
	}
	if len(input.SessionRecentMessages) != 2 {
		t.Fatalf("expected operator and runtime session messages, got %+v", input.SessionRecentMessages)
	}
	if input.SessionRecentMessages[0].Role != "operator" || input.SessionRecentMessages[1].Role != "runtime" {
		t.Fatalf("unexpected session message roles: %+v", input.SessionRecentMessages)
	}
	if !strings.Contains(input.SessionRecentMessages[1].Content, "Done") {
		t.Fatalf("expected runtime summary in session transcript, got %+v", input.SessionRecentMessages[1])
	}
}

func TestPromptSessionPassesSessionTranscriptToRepairPrompts(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package demo\n\nfunc Add(a, b int) int { return a - b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package demo\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")
	writeFile(t, filepath.Join(dir, "README.md"), "# keep untouched\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	var (
		mu     sync.Mutex
		bodies = map[string][][]byte{}
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		body, err := json.Marshal(raw)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		text, ok := raw["text"].(map[string]any)
		if !ok {
			t.Fatalf("expected text config in request, got %#v", raw["text"])
		}
		format, ok := text["format"].(map[string]any)
		if !ok {
			t.Fatalf("expected text.format in request, got %#v", text["format"])
		}
		name, _ := format["name"].(string)
		mu.Lock()
		bodies[name] = append(bodies[name], body)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch name {
		case "ngen_provider_decision":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"action\":\"run\",\"summary\":\"Run the coding loop now\",\"worker_id\":\"\",\"worker_role\":\"\",\"worker_objective\":\"\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
					}]
				}]
			}`))
		case "ngen_workspace_observation":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"summary\":\"Read calc.go before editing\",\"commands\":[{\"argv\":[\"sed\",\"-n\",\"1,40p\",\"calc.go\"],\"reason\":\"Inspect calc.go before patching\"}]}"
					}]
				}]
			}`))
		case "ngen_workspace_edit":
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"summary\":\"Fix Add implementation without touching README.md\",\"patch\":\"\",\"writes\":[{\"path\":\"calc.go\",\"content\":\"package demo\\n\\nfunc Add(a, b int) int { return a + b }\\n\"}],\"deletes\":[],\"commands\":[]}"
					}]
				}]
			}`))
		default:
			t.Fatalf("unexpected request schema name: %q", name)
		}
	}))
	defer server.Close()

	cfg := task.DefaultConfig()
	cfg.Provider.Mode = "openai-response"
	cfg.Provider.BaseURL = server.URL + "/v1"
	cfg.Provider.APIKeyEnv = "OPENAI_API_KEY"
	cfg.Provider.Model = "gpt-5.4"
	cfg.Provider.AutoRunMaxTurns = 1

	svc := New(dir, cfg)
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     "session-aware repair",
		Objective: "fix Add without touching README.md",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "go test passes"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	session, err := svc.StartSession(context.Background(), spec.TaskID, "terminal")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	_, snapshot, _, err := svc.PromptSession(context.Background(), session.SessionID, "/run and keep README.md untouched while fixing calc.go")
	if err != nil {
		t.Fatalf("prompt session: %v", err)
	}
	if snapshot.State != task.StateDone {
		t.Fatalf("expected task to finish after repair, got %+v", snapshot)
	}

	mu.Lock()
	observationBodies := append([][]byte(nil), bodies["ngen_workspace_observation"]...)
	editBodies := append([][]byte(nil), bodies["ngen_workspace_edit"]...)
	mu.Unlock()
	if len(observationBodies) != 1 {
		t.Fatalf("expected one workspace observation request, got %d", len(observationBodies))
	}
	if len(editBodies) != 1 {
		t.Fatalf("expected one workspace edit request, got %d", len(editBodies))
	}

	observationPrompt := string(observationBodies[0])
	if !strings.Contains(observationPrompt, "session_messages_ref") || !strings.Contains(observationPrompt, "session_recent_messages") {
		t.Fatalf("expected session transcript fields in observation request, got %s", observationPrompt)
	}
	if !strings.Contains(observationPrompt, "keep README.md untouched while fixing calc.go") {
		t.Fatalf("expected operator steering in observation request, got %s", observationPrompt)
	}

	editPrompt := string(editBodies[0])
	if !strings.Contains(editPrompt, "session_messages_ref") || !strings.Contains(editPrompt, "session_recent_messages") {
		t.Fatalf("expected session transcript fields in workspace edit request, got %s", editPrompt)
	}
	if !strings.Contains(editPrompt, "keep README.md untouched while fixing calc.go") {
		t.Fatalf("expected operator steering in workspace edit request, got %s", editPrompt)
	}
}

func TestTUISessionFirstPromptRefinesPlaceholderTask(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "demo.go"), "package demo\n\nfunc Add(a, b int) int { return a + b }\n")

	svc := New(dir, task.DefaultConfig())
	view, err := svc.CreateTask(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     "TUI Session",
		Objective: "Capture the first TUI prompt and turn it into durable task context.",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "First operator prompt is captured and used to drive the session"},
		},
		WorkspaceRoot: dir,
	}, "tui", "", "")
	if err != nil {
		t.Fatalf("create tui placeholder task: %v", err)
	}
	session, err := svc.StartSession(context.Background(), view.Task.TaskID, "tui")
	if err != nil {
		t.Fatalf("start tui session: %v", err)
	}

	if _, _, _, err := svc.PromptSession(context.Background(), session.SessionID, "Implement Add and keep go test passing"); err != nil {
		t.Fatalf("prompt session: %v", err)
	}

	refined, err := svc.Store.LoadTask(view.Task.TaskID)
	if err != nil {
		t.Fatalf("load refined task: %v", err)
	}
	if refined.Title != "Implement Add and keep go test passing" {
		t.Fatalf("expected refined title from first prompt, got %+v", refined)
	}
	if refined.Objective != "Implement Add and keep go test passing" {
		t.Fatalf("expected refined objective from first prompt, got %+v", refined)
	}
	if len(refined.SuccessCriteria) != 1 || !strings.Contains(refined.SuccessCriteria[0].Statement, "Implement Add") {
		t.Fatalf("expected refined criterion from first prompt, got %+v", refined.SuccessCriteria)
	}
	events, err := svc.Store.ReadEvents(view.Task.TaskID)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	var sawRefined bool
	for _, event := range events {
		if event.Type == "task_refined" {
			sawRefined = true
			break
		}
	}
	if !sawRefined {
		t.Fatalf("expected task_refined event, got %+v", events)
	}
}

func TestPromptSessionGreetingAppendsAssistantReplyWithoutRunningTask(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")

	svc := New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "chat-like greet",
		Objective: "keep task idle while answering a greeting",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "docs reviewed"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	session, err := svc.StartSession(context.Background(), spec.TaskID, "terminal")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	session, snapshot, events, err := svc.PromptSession(context.Background(), session.SessionID, "hello")
	if err != nil {
		t.Fatalf("prompt session: %v", err)
	}
	if snapshot.State != task.StateActive || snapshot.Phase != task.PhaseExplore {
		t.Fatalf("expected greeting to leave task active/explore, got %+v", snapshot)
	}
	if svc.Store.HasBaseline(spec.TaskID) {
		t.Fatal("expected greeting prompt to avoid starting a baseline/runtime pass")
	}
	var sawDecision, sawReply bool
	for _, event := range events {
		switch event.Type {
		case "provider_decided":
			sawDecision = true
		case "provider_responded":
			sawReply = true
		case "baseline_captured", "plan_updated", "state_changed":
			t.Fatalf("did not expect runtime execution events for greeting, got %+v", events)
		}
	}
	if !sawDecision || !sawReply {
		t.Fatalf("expected provider_decided and provider_responded events, got %+v", events)
	}

	_, messages, err := svc.ReadSession(context.Background(), session.SessionID)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("expected operator, assistant, and runtime messages, got %+v", messages)
	}
	if messages[0].Role != "operator" || messages[0].Content != "hello" {
		t.Fatalf("unexpected operator message: %+v", messages[0])
	}
	if messages[1].Role != "assistant" || !strings.Contains(strings.ToLower(messages[1].Content), "chat-first") {
		t.Fatalf("unexpected assistant reply: %+v", messages[1])
	}
	if messages[2].Role != "runtime" || !strings.Contains(strings.ToLower(messages[2].Content), "responded to operator prompt") {
		t.Fatalf("unexpected runtime summary: %+v", messages[2])
	}
}

func TestPromptSessionSpawnsAndRunsWorkerFromSlashPrompt(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "demo.go"), "package demo\n\nfunc Add(a, b int) int { return a + b }\n")
	writeFile(t, filepath.Join(dir, "demo_test.go"), "package demo\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")
	writeFile(t, filepath.Join(dir, "README.md"), "# review target\n")

	svc := New(dir, task.DefaultConfig())
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

	session, err := svc.StartSession(context.Background(), parent.TaskID, "terminal")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	_, snapshot, events, err := svc.PromptSession(context.Background(), session.SessionID, "/worker_spawn reviewer review the parent output")
	if err != nil {
		t.Fatalf("prompt session worker spawn: %v", err)
	}
	if snapshot.State != task.StateDone {
		t.Fatalf("expected parent to remain done after worker spawn, got %+v", snapshot)
	}

	var sawSpawn, sawContinue bool
	for _, event := range events {
		switch event.Type {
		case "worker_spawned":
			sawSpawn = true
		case "worker_continued":
			sawContinue = true
		}
	}
	if !sawSpawn || !sawContinue {
		t.Fatalf("expected worker_spawned and worker_continued events, got %+v", events)
	}

	workers, err := svc.ListWorkers(context.Background(), parent.TaskID)
	if err != nil {
		t.Fatalf("list workers: %v", err)
	}
	if len(workers) != 1 {
		t.Fatalf("expected one worker, got %+v", workers)
	}
	if workers[0].Role != string(task.KindReviewer) || workers[0].Status != "done" || workers[0].RequiresParentAction {
		t.Fatalf("expected reviewer worker to finish cleanly, got %+v", workers[0])
	}

	childStatus, err := svc.Status(context.Background(), workers[0].ChildTaskID)
	if err != nil {
		t.Fatalf("child status: %v", err)
	}
	if childStatus.State != task.StateDone {
		t.Fatalf("expected child to be done after worker spawn prompt, got %+v", childStatus)
	}
}

func TestPromptSessionMemoryPromptStopsAfterSinglePromotion(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")

	svc := New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "memory prompt",
		Objective: "capture a durable milestone",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "docs reviewed"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	session, err := svc.StartSession(context.Background(), spec.TaskID, "terminal")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	_, snapshot, events, err := svc.PromptSession(context.Background(), session.SessionID, "/memory milestone repo truth confirmed")
	if err != nil {
		t.Fatalf("prompt session memory promote: %v", err)
	}
	if snapshot.State != task.StateActive {
		t.Fatalf("expected task to remain active after standalone memory prompt, got %+v", snapshot)
	}
	for _, event := range events {
		if event.Type == "auto_turn_limit_reached" {
			t.Fatalf("expected standalone memory prompt to stop cleanly, got %+v", events)
		}
	}

	entries, err := svc.Store.ReadMemoryEntries()
	if err != nil {
		t.Fatalf("read memory entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one memory entry, got %+v", entries)
	}
	if entries[0].Kind != task.MemoryKindTaskMilestone || entries[0].Source != task.MemorySourceProvider {
		t.Fatalf("expected provider milestone entry from slash prompt, got %+v", entries[0])
	}
}

func TestPromptSessionMemoryPromptBypassesRemoteDecisionProvider(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")

	cfg := task.DefaultConfig()
	cfg.Provider.Mode = "openai-response"
	cfg.Provider.BaseURL = "http://127.0.0.1:1/v1"
	svc := New(dir, cfg)
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "memory prompt remote bypass",
		Objective: "capture a durable milestone without contacting the remote decision provider",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "docs reviewed"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	session, err := svc.StartSession(context.Background(), spec.TaskID, "terminal")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	if _, _, _, err := svc.PromptSession(context.Background(), session.SessionID, "/memory milestone deterministic slash command"); err != nil {
		t.Fatalf("prompt session memory promote with remote mode: %v", err)
	}

	entries, err := svc.Store.ReadMemoryEntries()
	if err != nil {
		t.Fatalf("read memory entries: %v", err)
	}
	if len(entries) != 1 || !strings.Contains(entries[0].Summary, "deterministic slash command") {
		t.Fatalf("expected deterministic memory entry without remote decision call, got %+v", entries)
	}
}

func TestPromptSessionMissionsSlashOpensMissionWithoutRemoteProvider(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")

	cfg := task.DefaultConfig()
	cfg.Provider.Mode = "openai-response"
	cfg.Provider.BaseURL = "http://127.0.0.1:1/v1"
	svc := New(dir, cfg)
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "mission slash",
		Objective: "open mission mode without contacting the remote decision provider",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "docs reviewed"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	session, err := svc.StartSession(context.Background(), spec.TaskID, "terminal")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	_, snapshot, events, err := svc.PromptSession(context.Background(), session.SessionID, "/missions")
	if err != nil {
		t.Fatalf("prompt session /missions: %v", err)
	}
	if snapshot.TaskID != spec.TaskID {
		t.Fatalf("expected root task snapshot, got %+v", snapshot)
	}
	var sawOpened bool
	for _, event := range events {
		if event.Type == "mission_opened" {
			sawOpened = true
		}
		if event.Type == "provider_decided" {
			t.Fatalf("expected /missions to bypass remote decision provider, got %+v", events)
		}
	}
	if !sawOpened {
		t.Fatalf("expected mission_opened event, got %+v", events)
	}
	ids, err := svc.Store.ListMissionIDs()
	if err != nil {
		t.Fatalf("list missions: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected one mission, got %+v", ids)
	}
	mission, err := svc.Store.LoadMission(ids[0])
	if err != nil {
		t.Fatalf("load mission: %v", err)
	}
	if mission.RootTaskID != spec.TaskID {
		t.Fatalf("expected mission bound to root task %s, got %+v", spec.TaskID, mission)
	}
}

func TestPromptSessionGoalSlashSetsMissionObjectiveWithoutRemoteProvider(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")

	cfg := task.DefaultConfig()
	cfg.Provider.Mode = "openai-response"
	cfg.Provider.BaseURL = "http://127.0.0.1:1/v1"
	svc := New(dir, cfg)
	view, err := svc.CreateTask(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     "TUI Session",
		Objective: "Capture the first TUI prompt and turn it into durable task context.",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "First operator prompt is captured and used to drive the session"},
		},
		WorkspaceRoot: dir,
	}, "tui", "", "")
	if err != nil {
		t.Fatalf("create tui placeholder task: %v", err)
	}
	session, err := svc.StartSession(context.Background(), view.Task.TaskID, "tui")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	prompt := "/goal ship the docs refresh with tests passing"
	_, snapshot, events, err := svc.PromptSession(context.Background(), session.SessionID, prompt)
	if err != nil {
		t.Fatalf("prompt session goal slash: %v", err)
	}
	if snapshot.TaskID != view.Task.TaskID || snapshot.MissionID == "" {
		t.Fatalf("expected mission-bound snapshot, got %+v", snapshot)
	}
	for _, event := range events {
		if event.Type == "provider_decided" {
			t.Fatalf("expected /goal to bypass remote decision provider, got %+v", events)
		}
	}
	refined, err := svc.Store.LoadTask(view.Task.TaskID)
	if err != nil {
		t.Fatalf("load refined task: %v", err)
	}
	if refined.Objective != "ship the docs refresh with tests passing" {
		t.Fatalf("expected slash prompt objective without command prefix, got %+v", refined)
	}
	mission, err := svc.Store.LoadMission(snapshot.MissionID)
	if err != nil {
		t.Fatalf("load mission: %v", err)
	}
	if mission.RootTaskID != view.Task.TaskID || mission.Objective != refined.Objective {
		t.Fatalf("expected mission objective bound to refined task, mission=%+v task=%+v", mission, refined)
	}
	contract, err := svc.Store.LoadMissionValidationContract(snapshot.MissionID)
	if err != nil {
		t.Fatalf("load mission contract: %v", err)
	}
	if len(contract.AcceptanceTests) != 1 || !strings.Contains(contract.AcceptanceTests[0], "goal objective") {
		t.Fatalf("expected goal-flavored default criterion, got %+v", contract.AcceptanceTests)
	}
}

func TestPromptSessionMissionSlashUpdatesExistingMissionObjective(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")

	svc := New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "mission update",
		Objective: "open mission mode",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "docs reviewed"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	created, err := svc.CreateMission(context.Background(), task.MissionCreateRequest{
		Title:      "old mission",
		Objective:  "old mission objective",
		RootTaskID: spec.TaskID,
		Criteria:   []string{"old criterion"},
	})
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}
	session, err := svc.StartSession(context.Background(), spec.TaskID, "terminal")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	_, snapshot, events, err := svc.PromptSession(context.Background(), session.SessionID, "/mission update the existing mission objective")
	if err != nil {
		t.Fatalf("prompt session mission update: %v", err)
	}
	if snapshot.MissionID != created.Mission.MissionID {
		t.Fatalf("expected existing mission id %s, got %+v", created.Mission.MissionID, snapshot)
	}
	var sawUpdated bool
	for _, event := range events {
		if event.Type == "mission_updated" {
			sawUpdated = true
		}
		if event.Type == "provider_decided" {
			t.Fatalf("expected mission update to bypass remote decision provider, got %+v", events)
		}
	}
	if !sawUpdated {
		t.Fatalf("expected mission_updated event, got %+v", events)
	}
	mission, err := svc.Store.LoadMission(created.Mission.MissionID)
	if err != nil {
		t.Fatalf("load mission: %v", err)
	}
	if mission.Objective != "update the existing mission objective" || mission.Status != task.MissionStatusDraft {
		t.Fatalf("expected updated draft mission, got %+v", mission)
	}
}

func TestProviderInputStatusAndNarrativeExposeMissionContext(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# mission docs\n")

	svc := New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "mission context",
		Objective: "surface mission context to provider and operator artifacts",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "mission docs are reviewed"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	created, err := svc.CreateMission(context.Background(), task.MissionCreateRequest{
		Title:      "mission context",
		Objective:  "surface mission context to provider and operator artifacts",
		RootTaskID: spec.TaskID,
		Criteria:   []string{"mission docs are reviewed"},
	})
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}
	validated, err := svc.ValidateMission(context.Background(), created.Mission.MissionID, "")
	if err != nil {
		t.Fatalf("validate mission: %v", err)
	}
	if validated.LatestValidation == nil || validated.LatestValidation.Status != "blocking" {
		t.Fatalf("expected blocking validation in fixture, got %+v", validated.LatestValidation)
	}

	_, _, input, err := svc.buildProviderInput(context.Background(), spec.TaskID, nil)
	if err != nil {
		t.Fatalf("build provider input: %v", err)
	}
	if input.Mission == nil || input.Mission.Mission.MissionID != created.Mission.MissionID {
		t.Fatalf("expected provider mission context, got %+v", input.Mission)
	}
	if input.Mission.Contract.ContractID == "" || len(input.Mission.Features.Features) == 0 || len(input.Mission.Milestones.Milestones) == 0 {
		t.Fatalf("expected mission contract, features, and milestones in provider context, got %+v", input.Mission)
	}
	if input.Mission.LatestValidation == nil || len(input.Mission.LatestValidation.Findings) == 0 {
		t.Fatalf("expected latest validation findings in provider context, got %+v", input.Mission.LatestValidation)
	}

	status, err := svc.Status(context.Background(), spec.TaskID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.MissionID != created.Mission.MissionID || status.MissionStatus != task.MissionStatusBlocked || status.MissionLatestValidationRef == "" {
		t.Fatalf("expected mission refs in status snapshot, got %+v", status)
	}
	progress := readFile(t, filepath.Join(dir, ".ngen", "tasks", spec.TaskID, "progress.md"))
	if !strings.Contains(progress, "## Mission") || !strings.Contains(progress, "Latest Validation Status: blocking") {
		t.Fatalf("expected mission section in progress, got:\n%s", progress)
	}
	contextSummary := readFile(t, filepath.Join(dir, ".ngen", "tasks", spec.TaskID, "context", "summary.md"))
	if !strings.Contains(contextSummary, "## Mission") || !strings.Contains(contextSummary, created.Mission.MissionID) {
		t.Fatalf("expected mission section in context summary, got:\n%s", contextSummary)
	}
}

func TestRunMissionRequiresApprovedPlan(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/missionapproval\n\ngo 1.24.0\n")

	svc := New(dir, task.DefaultConfig())
	created, err := svc.CreateMission(context.Background(), task.MissionCreateRequest{
		Title:     "approval gate",
		Objective: "prove mission run requires approval",
		Criteria:  []string{"go test passes"},
	})
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}
	view, err := svc.RunMission(context.Background(), created.Mission.MissionID)
	if err != nil {
		t.Fatalf("run mission should return blocking view, not error: %v", err)
	}
	if view.Mission.Status != task.MissionStatusBlocked || view.Mission.StatusReasonCode != "blocked_plan_gate" {
		t.Fatalf("expected blocked plan gate, got %+v", view.Mission)
	}
	if view.LatestValidation == nil || view.LatestValidation.ValidatorKind != "deterministic_plan_gate" || len(view.LatestValidation.Findings) == 0 {
		t.Fatalf("expected deterministic plan gate finding, got %+v", view.LatestValidation)
	}
	if view.LatestValidation.Findings[0].Category != "plan_unapproved" {
		t.Fatalf("expected plan_unapproved finding, got %+v", view.LatestValidation.Findings)
	}
}

func TestApproveMissionPlanBlocksUncoveredAssertions(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/missioncoverage\n\ngo 1.24.0\n")

	svc := New(dir, task.DefaultConfig())
	created, err := svc.CreateMission(context.Background(), task.MissionCreateRequest{
		Title:     "coverage gate",
		Objective: "prove mission approval requires assertion coverage",
		Criteria:  []string{"first assertion passes", "second assertion passes"},
	})
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}
	features, err := svc.Store.LoadMissionFeatures(created.Mission.MissionID)
	if err != nil {
		t.Fatalf("load features: %v", err)
	}
	features.Features[0].ContractCoverage = []string{"ASSERT-001"}
	if err := svc.Store.SaveMissionFeatures(features); err != nil {
		t.Fatalf("save features: %v", err)
	}
	view, err := svc.ApproveMissionPlan(context.Background(), created.Mission.MissionID)
	if err != nil {
		t.Fatalf("approve mission plan should return blocking view, not error: %v", err)
	}
	if view.Mission.PlanApprovalStatus == task.MissionPlanApprovalApproved || view.Mission.Status != task.MissionStatusBlocked {
		t.Fatalf("expected approval to remain blocked, got %+v", view.Mission)
	}
	if view.LatestValidation == nil || view.LatestValidation.ValidatorKind != "deterministic_plan_approval" {
		t.Fatalf("expected plan approval validation run, got %+v", view.LatestValidation)
	}
	var sawUncovered bool
	for _, finding := range view.LatestValidation.Findings {
		if finding.Category == "uncovered_assertion" && strings.Contains(finding.Summary, "ASSERT-002") {
			sawUncovered = true
		}
	}
	if !sawUncovered {
		t.Fatalf("expected uncovered ASSERT-002 finding, got %+v", view.LatestValidation.Findings)
	}
}

func TestRunMissionRequiresApprovalForCurrentContractRef(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/missioncontractref\n\ngo 1.24.0\n")

	svc := New(dir, task.DefaultConfig())
	created, err := svc.CreateMission(context.Background(), task.MissionCreateRequest{
		Title:     "contract ref gate",
		Objective: "prove mission run requires approval for the current contract",
		Criteria:  []string{"contract approval ref matches"},
	})
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}
	approved, err := svc.ApproveMissionPlan(context.Background(), created.Mission.MissionID)
	if err != nil {
		t.Fatalf("approve mission plan: %v", err)
	}
	mission := approved.Mission
	mission.PlanApprovedContractRef = "validation_contract.json#contract_id=MCON-stale"
	if err := svc.Store.SaveMission(mission); err != nil {
		t.Fatalf("save stale mission approval: %v", err)
	}
	view, err := svc.RunMission(context.Background(), created.Mission.MissionID)
	if err != nil {
		t.Fatalf("run mission should return blocking view, not error: %v", err)
	}
	if view.Mission.Status != task.MissionStatusBlocked || view.Mission.StatusReasonCode != "blocked_plan_gate" {
		t.Fatalf("expected stale approval to block plan gate, got %+v", view.Mission)
	}
	if view.LatestValidation == nil {
		t.Fatal("expected validation run for stale approval")
	}
	var sawStaleApproval bool
	for _, finding := range view.LatestValidation.Findings {
		if finding.Category == "plan_unapproved" && strings.Contains(finding.Summary, "does not match") {
			sawStaleApproval = true
		}
	}
	if !sawStaleApproval {
		t.Fatalf("expected stale approval finding, got %+v", view.LatestValidation.Findings)
	}
}

func TestValidateMissionRequiresApprovalForCurrentContractRef(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/missionvalidateref\n\ngo 1.24.0\n")

	svc := New(dir, task.DefaultConfig())
	created, err := svc.CreateMission(context.Background(), task.MissionCreateRequest{
		Title:     "validate contract ref gate",
		Objective: "prove mission validate requires approval for the current contract",
		Criteria:  []string{"contract approval ref matches during validation"},
	})
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}
	approved, err := svc.ApproveMissionPlan(context.Background(), created.Mission.MissionID)
	if err != nil {
		t.Fatalf("approve mission plan: %v", err)
	}
	mission := approved.Mission
	mission.PlanApprovedContractRef = "validation_contract.json#contract_id=MCON-stale"
	if err := svc.Store.SaveMission(mission); err != nil {
		t.Fatalf("save stale mission approval: %v", err)
	}
	view, err := svc.ValidateMission(context.Background(), created.Mission.MissionID, "")
	if err != nil {
		t.Fatalf("validate mission should return blocking view, not error: %v", err)
	}
	if view.Mission.Status != task.MissionStatusBlocked || view.Mission.StatusReasonCode != "blocked_validation" {
		t.Fatalf("expected stale approval to block validation, got %+v", view.Mission)
	}
	if view.LatestValidation == nil {
		t.Fatal("expected validation run for stale approval")
	}
	var sawStaleApproval bool
	for _, finding := range view.LatestValidation.Findings {
		if finding.Category == "plan_unapproved" && strings.Contains(finding.Summary, "does not match") {
			sawStaleApproval = true
		}
	}
	if !sawStaleApproval {
		t.Fatalf("expected stale approval finding, got %+v", view.LatestValidation.Findings)
	}
}

func TestValidateMissionBlocksUncoveredAssertions(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/missionvalidatecoverage\n\ngo 1.24.0\n")

	svc := New(dir, task.DefaultConfig())
	created, err := svc.CreateMission(context.Background(), task.MissionCreateRequest{
		Title:     "validate coverage gate",
		Objective: "prove mission validation requires assertion coverage",
		Criteria:  []string{"first assertion passes", "second assertion passes"},
	})
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}
	if _, err := svc.ApproveMissionPlan(context.Background(), created.Mission.MissionID); err != nil {
		t.Fatalf("approve mission plan: %v", err)
	}
	features, err := svc.Store.LoadMissionFeatures(created.Mission.MissionID)
	if err != nil {
		t.Fatalf("load features: %v", err)
	}
	features.Features[0].ContractCoverage = []string{"ASSERT-001"}
	if err := svc.Store.SaveMissionFeatures(features); err != nil {
		t.Fatalf("save features: %v", err)
	}
	view, err := svc.ValidateMission(context.Background(), created.Mission.MissionID, "")
	if err != nil {
		t.Fatalf("validate mission should return blocking view, not error: %v", err)
	}
	if view.LatestValidation == nil || view.LatestValidation.ValidatorKind != "deterministic_artifact" {
		t.Fatalf("expected deterministic validation run, got %+v", view.LatestValidation)
	}
	var sawUncovered bool
	for _, finding := range view.LatestValidation.Findings {
		if finding.Category == "uncovered_assertion" && strings.Contains(finding.Summary, "ASSERT-002") {
			sawUncovered = true
		}
	}
	if !sawUncovered {
		t.Fatalf("expected uncovered ASSERT-002 finding, got %+v", view.LatestValidation.Findings)
	}
}

func TestValidateMissionBlocksAssertionWithoutClosingEvidence(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/missionevidence\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package missionevidence\n\nfunc Add(a, b int) int { return a + b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package missionevidence\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) { if Add(2, 3) != 5 { t.Fatal(\"bad add\") } }\n")

	svc := New(dir, task.DefaultConfig())
	created, err := svc.CreateMission(context.Background(), task.MissionCreateRequest{
		Title:     "assertion evidence gate",
		Objective: "prove every assertion needs closing evidence",
		Criteria:  []string{"root assertion has evidence", "orphan assertion has evidence"},
	})
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}
	features, err := svc.Store.LoadMissionFeatures(created.Mission.MissionID)
	if err != nil {
		t.Fatalf("load features: %v", err)
	}
	features.Features[0].ContractCoverage = []string{"ASSERT-001"}
	features.Features = append(features.Features, task.MissionFeature{
		FeatureID:        "FEAT-002",
		Title:            "Orphan assertion evidence",
		Description:      "Covered by the plan but missing durable execution evidence.",
		ContractCoverage: []string{"ASSERT-002"},
		Status:           task.ProjectStepStatusPending,
		UpdatedAt:        task.Now(),
	})
	if err := svc.Store.SaveMissionFeatures(features); err != nil {
		t.Fatalf("save features: %v", err)
	}
	milestones, err := svc.Store.LoadMissionMilestones(created.Mission.MissionID)
	if err != nil {
		t.Fatalf("load milestones: %v", err)
	}
	milestones.Milestones[0].FeatureIDs = []string{"FEAT-001", "FEAT-002"}
	milestones.Milestones[0].ContractCoverage = []string{"ASSERT-001", "ASSERT-002"}
	if err := svc.Store.SaveMissionMilestones(milestones); err != nil {
		t.Fatalf("save milestones: %v", err)
	}
	if _, err := svc.ApproveMissionPlan(context.Background(), created.Mission.MissionID); err != nil {
		t.Fatalf("approve mission plan: %v", err)
	}
	if _, _, err := svc.Run(context.Background(), created.Mission.RootTaskID); err != nil {
		t.Fatalf("run root task: %v", err)
	}

	view, err := svc.ValidateMission(context.Background(), created.Mission.MissionID, "")
	if err != nil {
		t.Fatalf("validate mission: %v", err)
	}
	if view.Mission.Status != task.MissionStatusBlocked || view.LatestValidation == nil {
		t.Fatalf("expected assertion evidence blocker, got mission=%+v validation=%+v", view.Mission, view.LatestValidation)
	}
	var sawMissingEvidence bool
	for _, finding := range view.LatestValidation.Findings {
		if finding.Category == "missing_assertion_evidence" && strings.Contains(finding.Summary, "ASSERT-002") {
			sawMissingEvidence = true
		}
	}
	if !sawMissingEvidence {
		t.Fatalf("expected ASSERT-002 missing assertion evidence finding, got %+v", view.LatestValidation.Findings)
	}
	if len(view.Milestones.Milestones[0].FixFeatureIDs) != 0 {
		t.Fatalf("deterministic assertion evidence blockers should not create fix features, got %+v", view.Milestones.Milestones[0].FixFeatureIDs)
	}
}

func TestApproveMissionPlanAcceptsLegacyStatementCoverage(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/missionlegacycoverage\n\ngo 1.24.0\n")

	svc := New(dir, task.DefaultConfig())
	created, err := svc.CreateMission(context.Background(), task.MissionCreateRequest{
		Title:     "legacy coverage",
		Objective: "prove old natural-language mission coverage remains readable",
		Criteria:  []string{"legacy coverage statement passes"},
	})
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}
	contract, err := svc.Store.LoadMissionValidationContract(created.Mission.MissionID)
	if err != nil {
		t.Fatalf("load contract: %v", err)
	}
	contract.Assertions = nil
	if err := svc.Store.SaveMissionValidationContract(contract); err != nil {
		t.Fatalf("save legacy contract: %v", err)
	}
	features, err := svc.Store.LoadMissionFeatures(created.Mission.MissionID)
	if err != nil {
		t.Fatalf("load features: %v", err)
	}
	features.Features[0].ContractCoverage = []string{"legacy coverage statement passes"}
	if err := svc.Store.SaveMissionFeatures(features); err != nil {
		t.Fatalf("save legacy features: %v", err)
	}
	milestones, err := svc.Store.LoadMissionMilestones(created.Mission.MissionID)
	if err != nil {
		t.Fatalf("load milestones: %v", err)
	}
	milestones.Milestones[0].ContractCoverage = []string{"legacy coverage statement passes"}
	if err := svc.Store.SaveMissionMilestones(milestones); err != nil {
		t.Fatalf("save legacy milestones: %v", err)
	}
	view, err := svc.ApproveMissionPlan(context.Background(), created.Mission.MissionID)
	if err != nil {
		t.Fatalf("approve mission plan: %v", err)
	}
	if view.Mission.PlanApprovalStatus != task.MissionPlanApprovalApproved || view.Mission.Status == task.MissionStatusBlocked {
		t.Fatalf("expected legacy statement coverage to approve, got %+v", view.Mission)
	}
}

func TestHarnessEvaluationRecordsProviderUsage(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# provider usage\n")

	svc := New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     "provider usage",
		Objective: "record provider usage in harness snapshot",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "provider usage recorded"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := svc.Store.AppendProviderUsage(task.ProviderUsageRecord{
		ObjectKind:       "provider_usage",
		SchemaVersion:    task.SchemaVersion,
		UsageRecordID:    "PUSE-test-harness",
		TaskID:           spec.TaskID,
		TS:               task.Now(),
		Operation:        "decision",
		ProviderMode:     "anthropic",
		Model:            "claude-test",
		TokenUsage:       "input_tokens=31 output_tokens=7",
		PromptCacheUsage: "cache_creation_input_tokens=11 cache_read_input_tokens=19",
		Cost:             "unknown",
	}); err != nil {
		t.Fatalf("append provider usage: %v", err)
	}

	eval, err := svc.captureHarnessEvaluation(context.Background(), spec.TaskID, "auto")
	if err != nil {
		t.Fatalf("capture harness evaluation: %v", err)
	}
	if eval.TokenUsage != "input_tokens=31 output_tokens=7" || eval.PromptCacheUsage != "cache_creation_input_tokens=11 cache_read_input_tokens=19" {
		t.Fatalf("expected harness provider usage, got token=%q cache=%q", eval.TokenUsage, eval.PromptCacheUsage)
	}
	if eval.ProviderUsageRef != "provider_usage.jsonl#usage_record_id=PUSE-test-harness" || !containsString(eval.EvidenceRefs, eval.ProviderUsageRef) {
		t.Fatalf("expected provider usage evidence ref, got ref=%q evidence=%+v", eval.ProviderUsageRef, eval.EvidenceRefs)
	}
}

func TestMissionMetricsRecordsProviderUsage(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/missionproviderusage\n\ngo 1.24.0\n")

	svc := New(dir, task.DefaultConfig())
	created, err := svc.CreateMission(context.Background(), task.MissionCreateRequest{
		Title:     "mission provider usage",
		Objective: "record provider cache usage in mission metrics",
		Criteria:  []string{"provider usage is durable"},
	})
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}
	if err := svc.Store.AppendProviderUsage(task.ProviderUsageRecord{
		ObjectKind:       "provider_usage",
		SchemaVersion:    task.SchemaVersion,
		UsageRecordID:    "PUSE-test-mission",
		TaskID:           created.Mission.RootTaskID,
		TS:               task.Now(),
		Operation:        "mission_validation",
		ProviderMode:     "anthropic",
		Model:            "claude-test",
		TokenUsage:       "input_tokens=41 output_tokens=9",
		PromptCacheUsage: "cache_creation_input_tokens=13 cache_read_input_tokens=23",
		Cost:             "unknown",
	}); err != nil {
		t.Fatalf("append provider usage: %v", err)
	}

	if err := svc.appendMissionMetricsRecord(context.Background(), created.Mission.MissionID, "test_metric", "active", 10*time.Millisecond, []string{"mission.json"}, nil); err != nil {
		t.Fatalf("append mission metrics: %v", err)
	}
	records, err := svc.Store.ReadMissionMetrics(created.Mission.MissionID)
	if err != nil {
		t.Fatalf("read mission metrics: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("expected mission metric record")
	}
	got := records[len(records)-1]
	if got.TokenUsage != "input_tokens=41 output_tokens=9" || got.PromptCacheUsage != "cache_creation_input_tokens=13 cache_read_input_tokens=23" {
		t.Fatalf("expected mission metric provider usage, got token=%q cache=%q", got.TokenUsage, got.PromptCacheUsage)
	}
	snapshot := missionMetricsSnapshot(created.Mission.MissionID, records)
	if snapshot == nil || snapshot.TokenUsage != got.TokenUsage || snapshot.PromptCacheUsage != got.PromptCacheUsage {
		t.Fatalf("expected mission metrics snapshot provider usage, got %+v", snapshot)
	}
}

func TestValidateMissionRecordsMetricsAndProgressSummary(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/missionmetrics\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package missionmetrics\n\nfunc Add(a, b int) int { return a + b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package missionmetrics\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) { if Add(2, 3) != 5 { t.Fatal(\"bad add\") } }\n")

	svc := New(dir, task.DefaultConfig())
	created, err := svc.CreateMission(context.Background(), task.MissionCreateRequest{
		Title:     "mission metrics",
		Objective: "prove mission validation metrics are durable",
		Criteria:  []string{"go test passes"},
	})
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}
	if _, err := svc.ApproveMissionPlan(context.Background(), created.Mission.MissionID); err != nil {
		t.Fatalf("approve mission plan: %v", err)
	}
	if _, _, err := svc.Run(context.Background(), created.Mission.RootTaskID); err != nil {
		t.Fatalf("run root task: %v", err)
	}
	view, err := svc.ValidateMission(context.Background(), created.Mission.MissionID, "")
	if err != nil {
		t.Fatalf("validate mission: %v", err)
	}
	if view.Mission.Status != task.MissionStatusDone {
		t.Fatalf("expected mission done after validation, got %+v", view.Mission)
	}
	records, err := svc.Store.ReadMissionMetrics(created.Mission.MissionID)
	if err != nil {
		t.Fatalf("read mission metrics: %v", err)
	}
	var sawValidateMetric bool
	for _, record := range records {
		if record.Trigger == "mission_validate" && record.ValidatorTimeMS != nil && record.TokenUsage == "unknown" && record.PromptCacheUsage == "unknown" && record.Cost == "unknown" {
			sawValidateMetric = true
		}
	}
	if !sawValidateMetric {
		t.Fatalf("expected validation metrics record with explicit unknown token metadata, got %+v", records)
	}
	refreshed, err := svc.GetMission(context.Background(), created.Mission.MissionID)
	if err != nil {
		t.Fatalf("get mission: %v", err)
	}
	if refreshed.Metrics == nil || refreshed.Metrics.TotalValidatorTimeMS < 0 || refreshed.Metrics.TokenUsage != "unknown" {
		t.Fatalf("expected mission metrics snapshot, got %+v", refreshed.Metrics)
	}
	progress := readFile(t, filepath.Join(dir, ".ngen", "tasks", created.Mission.RootTaskID, "progress.md"))
	if !strings.Contains(progress, "Metrics: wall_ms=") || !strings.Contains(progress, "tokens=unknown") {
		t.Fatalf("expected progress mission metrics summary, got:\n%s", progress)
	}
}

func TestRunMissionUsesOrchestratorModelForProviderDecision(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/missionmodel\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package missionmodel\n\nfunc Add(a, b int) int { return a + b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package missionmodel\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) { if Add(2, 3) != 5 { t.Fatal(\"bad add\") } }\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	var decisionCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if got, _ := raw["model"].(string); got != "orchestrator-model" {
			t.Fatalf("expected orchestrator model, got request model %q body=%#v", got, raw)
		}
		decisionCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"output": [{
				"type": "message",
				"role": "assistant",
				"content": [{
					"type": "output_text",
					"text": "{\"action\":\"run\",\"summary\":\"Run the mission root.\",\"response_text\":\"\",\"task_kind\":\"\",\"task_preset_id\":\"\",\"task_title\":\"\",\"task_objective\":\"\",\"task_criteria\":[],\"task_constraints\":[],\"task_permission_mode_id\":\"\",\"project_step_id\":\"\",\"project_branch_id\":\"\",\"plan_explanation\":\"\",\"plan_steps\":[],\"plan_patch_operations\":[],\"project_explanation\":\"\",\"project_steps\":[],\"project_branches\":[],\"project_patch_operations\":[],\"memory_kind\":\"\",\"memory_refs\":[],\"worker_id\":\"\",\"worker_role\":\"\",\"worker_objective\":\"\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
				}]
			}]
		}`))
	}))
	defer server.Close()

	cfg := task.DefaultConfig()
	cfg.Provider.Mode = "openai-response"
	cfg.Provider.BaseURL = server.URL + "/v1"
	cfg.Provider.APIKeyEnv = "OPENAI_API_KEY"
	cfg.Provider.Model = "global-model"
	cfg.Provider.AutoRunMaxTurns = 1
	cfg.Mission.RoleModels = map[string]string{task.MissionRoleOrchestrator: "orchestrator-model"}

	svc := New(dir, cfg)
	created, err := svc.CreateMission(context.Background(), task.MissionCreateRequest{
		Title:     "mission model",
		Objective: "prove orchestrator model routing",
		Criteria:  []string{"go test passes"},
	})
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}
	if _, err := svc.ApproveMissionPlan(context.Background(), created.Mission.MissionID); err != nil {
		t.Fatalf("approve mission plan: %v", err)
	}
	view, err := svc.RunMission(context.Background(), created.Mission.MissionID)
	if err != nil {
		t.Fatalf("run mission: %v", err)
	}
	if view.Mission.Status != task.MissionStatusDone {
		t.Fatalf("expected done mission, got %+v", view.Mission)
	}
	if decisionCalls != 1 {
		t.Fatalf("expected one provider decision, got %d", decisionCalls)
	}
	eval, err := svc.Store.LoadHarnessEvaluation(created.Mission.RootTaskID)
	if err != nil {
		t.Fatalf("load harness eval: %v", err)
	}
	if eval.RuntimeAction != "mission_orchestrator" || eval.Model != "orchestrator-model" {
		t.Fatalf("expected mission harness to record orchestrator model, got %+v", eval)
	}
}

func TestMissionOwnedWorkerContinuationUsesWorkersModelAndLineage(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/missionworker\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "worker.go"), "package missionworker\n\nfunc OK() bool { return true }\n")
	writeFile(t, filepath.Join(dir, "worker_test.go"), "package missionworker\n\nimport \"testing\"\n\nfunc TestOK(t *testing.T) { if !OK() { t.Fatal(\"not ok\") } }\n")

	cfg := task.DefaultConfig()
	cfg.Provider.Model = "global-model"
	cfg.Mission.RoleModels = map[string]string{task.MissionRoleWorkers: "worker-model"}
	svc := New(dir, cfg)
	created, err := svc.CreateMission(context.Background(), task.MissionCreateRequest{
		Title:     "worker mission",
		Objective: "prove worker model routing",
		Criteria:  []string{"worker evidence exists"},
	})
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}
	worker, err := svc.SpawnWorker(context.Background(), created.Mission.RootTaskID, string(task.KindReviewer), "review the mission root evidence")
	if err != nil {
		t.Fatalf("spawn worker: %v", err)
	}
	missionID, err := svc.missionIDForTask(worker.ChildTaskID)
	if err != nil {
		t.Fatalf("mission lookup for child: %v", err)
	}
	if missionID != created.Mission.MissionID {
		t.Fatalf("expected child task to resolve mission %s, got %s", created.Mission.MissionID, missionID)
	}
	if _, err := svc.ContinueWorker(context.Background(), created.Mission.RootTaskID, worker.WorkerID); err != nil {
		t.Fatalf("continue worker: %v", err)
	}
	eval, err := svc.Store.LoadHarnessEvaluation(worker.ChildTaskID)
	if err != nil {
		t.Fatalf("load child harness eval: %v", err)
	}
	if eval.Model != "worker-model" {
		t.Fatalf("expected worker model in child harness eval, got %+v", eval)
	}
}

func TestMissionValidationModelPassRequiresExplicitValidatorsModel(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/missionvalidator\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package missionvalidator\n\nfunc Add(a, b int) int { return a + b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package missionvalidator\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) { if Add(2, 3) != 5 { t.Fatal(\"bad add\") } }\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	var validatorCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if got, _ := raw["model"].(string); got != "validator-model" {
			t.Fatalf("expected validator model, got %q", got)
		}
		validatorCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"output": [{
				"type": "message",
				"role": "assistant",
				"content": [{
					"type": "output_text",
					"text": "{\"status\":\"blocking\",\"summary\":\"Coverage gap remains.\",\"findings\":[{\"finding_id\":\"\",\"category\":\"coverage_gap\",\"severity\":\"high\",\"blocking\":true,\"summary\":\"Mission evidence lacks release-note coverage.\",\"evidence_refs\":[\"mission.json\"],\"recommended_action\":\"Add release-note coverage evidence.\"}]}"
				}]
			}]
		}`))
	}))
	defer server.Close()

	cfg := task.DefaultConfig()
	cfg.Provider.Mode = "openai-response"
	cfg.Provider.BaseURL = server.URL + "/v1"
	cfg.Provider.APIKeyEnv = "OPENAI_API_KEY"
	cfg.Provider.Model = "global-model"
	cfg.Mission.RoleModels = map[string]string{task.MissionRoleValidators: "validator-model"}

	svc := New(dir, cfg)
	created, err := svc.CreateMission(context.Background(), task.MissionCreateRequest{
		Title:     "validator mission",
		Objective: "prove model validator routing",
		Criteria:  []string{"go test passes"},
	})
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}
	if _, err := svc.ApproveMissionPlan(context.Background(), created.Mission.MissionID); err != nil {
		t.Fatalf("approve mission plan: %v", err)
	}
	if _, _, err := svc.Run(context.Background(), created.Mission.RootTaskID); err != nil {
		t.Fatalf("run root task: %v", err)
	}
	view, err := svc.ValidateMission(context.Background(), created.Mission.MissionID, "")
	if err != nil {
		t.Fatalf("validate mission: %v", err)
	}
	if validatorCalls != 1 {
		t.Fatalf("expected one model validator call, got %d", validatorCalls)
	}
	if view.LatestValidation == nil || view.LatestValidation.ValidatorKind != "model_validator" || view.LatestValidation.ValidatorModel != "validator-model" || view.LatestValidation.Status != "blocking" {
		t.Fatalf("expected blocking model validator provenance, got %+v", view.LatestValidation)
	}
	if len(view.Features.Features) < 2 || len(view.Milestones.Milestones[0].FixFeatureIDs) == 0 {
		t.Fatalf("expected validator finding to become fix feature candidate, got features=%+v milestones=%+v", view.Features, view.Milestones)
	}
	fixFeature := view.Features.Features[len(view.Features.Features)-1]
	if len(fixFeature.ContractCoverage) != 0 {
		t.Fatalf("expected fix feature to keep finding evidence out of contract coverage, got %+v", fixFeature.ContractCoverage)
	}
	if !slices.Contains(fixFeature.EvidenceRefs, "mission.json") {
		t.Fatalf("expected fix feature evidence refs to keep validator evidence, got %+v", fixFeature.EvidenceRefs)
	}
}

func TestMissionValidationSkipsModelPassWhenDeterministicBlocksOrValidatorIsInherited(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/missiondeterministic\n\ngo 1.24.0\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("model validator should not be called when deterministic validation blocks or validator model is inherited")
	}))
	defer server.Close()

	cfg := task.DefaultConfig()
	cfg.Provider.Mode = "openai-response"
	cfg.Provider.BaseURL = server.URL + "/v1"
	cfg.Provider.APIKeyEnv = "OPENAI_API_KEY"
	cfg.Provider.Model = "global-model"
	cfg.Mission.RoleModels = map[string]string{task.MissionRoleValidators: ""}

	svc := New(dir, cfg)
	created, err := svc.CreateMission(context.Background(), task.MissionCreateRequest{
		Title:     "deterministic first",
		Objective: "prove deterministic blocker short-circuits",
		Criteria:  []string{"go test passes"},
	})
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}
	view, err := svc.ValidateMission(context.Background(), created.Mission.MissionID, "")
	if err != nil {
		t.Fatalf("validate mission: %v", err)
	}
	if view.LatestValidation == nil || view.LatestValidation.ValidatorKind != "deterministic_artifact" || view.LatestValidation.Status != "blocking" {
		t.Fatalf("expected deterministic blocking validation, got %+v", view.LatestValidation)
	}
}

func TestRunMissionTaskCreateMaterializesMissionChildTask(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/missiontaskcreate\n\ngo 1.24.0\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"output": [{
				"type": "message",
				"role": "assistant",
				"content": [{
					"type": "output_text",
					"text": "{\"action\":\"task_create\",\"summary\":\"Create a mission child task.\",\"response_text\":\"\",\"task_kind\":\"coding\",\"task_preset_id\":\"\",\"task_title\":\"child\",\"task_objective\":\"do child work\",\"task_criteria\":[\"child done\"],\"task_constraints\":[],\"task_permission_mode_id\":\"\",\"project_step_id\":\"\",\"project_branch_id\":\"\",\"plan_explanation\":\"\",\"plan_steps\":[],\"plan_patch_operations\":[],\"project_explanation\":\"\",\"project_steps\":[],\"project_branches\":[],\"project_patch_operations\":[],\"memory_kind\":\"\",\"memory_refs\":[],\"worker_id\":\"\",\"worker_role\":\"\",\"worker_objective\":\"\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
				}]
			}]
		}`))
	}))
	defer server.Close()

	cfg := task.DefaultConfig()
	cfg.Provider.Mode = "openai-response"
	cfg.Provider.BaseURL = server.URL + "/v1"
	cfg.Provider.APIKeyEnv = "OPENAI_API_KEY"
	cfg.Provider.Model = "global-model"
	cfg.Provider.AutoRunMaxTurns = 1
	cfg.Mission.RoleModels = map[string]string{task.MissionRoleOrchestrator: "orchestrator-model"}

	svc := New(dir, cfg)
	created, err := svc.CreateMission(context.Background(), task.MissionCreateRequest{
		Title:     "task create enabled",
		Objective: "prove task_create can materialize a mission child task",
		Criteria:  []string{"mission child task exists"},
	})
	if err != nil {
		t.Fatalf("create mission: %v", err)
	}
	features, err := svc.Store.LoadMissionFeatures(created.Mission.MissionID)
	if err != nil {
		t.Fatalf("load features: %v", err)
	}
	features.Features[0].BoundTaskID = ""
	if err := svc.Store.SaveMissionFeatures(features); err != nil {
		t.Fatalf("save unbound mission feature: %v", err)
	}
	if _, err := svc.ApproveMissionPlan(context.Background(), created.Mission.MissionID); err != nil {
		t.Fatalf("approve mission plan: %v", err)
	}
	view, err := svc.RunMission(context.Background(), created.Mission.MissionID)
	if err != nil {
		t.Fatalf("run mission: %v", err)
	}
	if len(view.Features.Features) == 0 || view.Features.Features[0].BoundTaskID == "" || view.Features.Features[0].BoundTaskID == created.Mission.RootTaskID {
		t.Fatalf("expected mission feature to bind a new child task, got %+v", view.Features.Features)
	}
	child, err := svc.Store.LoadTask(view.Features.Features[0].BoundTaskID)
	if err != nil {
		t.Fatalf("load child task: %v", err)
	}
	if child.ParentTaskID != created.Mission.RootTaskID || child.RootTaskID != created.Mission.RootTaskID || child.LineageDepth != 1 {
		t.Fatalf("expected mission child lineage to point at root task, got %+v", child)
	}
	if view.MissionStatusSnapshot == nil || !slices.ContainsFunc(view.MissionStatusSnapshot.RecentMissionEvents, func(event string) bool {
		return strings.HasPrefix(event, "mission_child_task_created:")
	}) {
		t.Fatalf("expected mission status snapshot to include child creation event, got %+v", view.MissionStatusSnapshot)
	}
	refreshed, err := svc.GetMission(context.Background(), created.Mission.MissionID)
	if err != nil {
		t.Fatalf("refresh mission: %v", err)
	}
	if refreshed.Metrics == nil || refreshed.Metrics.TaskCount < 2 || refreshed.Metrics.TokenUsage != "unknown" {
		t.Fatalf("expected mission metrics snapshot with child task count and unknown token metadata, got %+v", refreshed.Metrics)
	}
}

func TestPromptSessionTaskCreateStopsAfterSingleDurableTask(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# parent docs\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	decisionCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		text, ok := raw["text"].(map[string]any)
		if !ok {
			t.Fatalf("expected text body, got %#v", raw["text"])
		}
		format, ok := text["format"].(map[string]any)
		if !ok {
			t.Fatalf("expected text.format body, got %#v", text["format"])
		}
		if got, _ := format["name"].(string); got != "ngen_provider_decision" {
			t.Fatalf("unexpected schema name: %q", got)
		}

		w.Header().Set("Content-Type", "application/json")
		decisionCalls++
		switch decisionCalls {
		case 1:
			_, _ = w.Write([]byte(`{
				"output": [{
					"type": "message",
					"role": "assistant",
					"content": [{
						"type": "output_text",
						"text": "{\"action\":\"task_create\",\"summary\":\"Create a durable follow-up docs task first.\",\"task_kind\":\"general_execution\",\"task_preset_id\":\"docs_lite\",\"task_title\":\"follow-up docs\",\"task_objective\":\"capture a durable docs follow-up\",\"task_criteria\":[\"docs reviewed\"],\"task_constraints\":[],\"task_permission_mode_id\":\"\",\"project_step_id\":\"\",\"project_branch_id\":\"\",\"plan_explanation\":\"\",\"plan_steps\":[],\"plan_patch_operations\":[],\"project_explanation\":\"\",\"project_steps\":[],\"project_branches\":[],\"project_patch_operations\":[],\"memory_kind\":\"\",\"memory_refs\":[],\"worker_id\":\"\",\"worker_role\":\"\",\"worker_objective\":\"\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
					}]
				}]
			}`))
		default:
			t.Fatalf("unexpected decision call %d", decisionCalls)
		}
	}))
	defer server.Close()

	cfg := task.DefaultConfig()
	cfg.Provider.Mode = "openai-response"
	cfg.Provider.BaseURL = server.URL + "/v1"
	cfg.Provider.APIKeyEnv = "OPENAI_API_KEY"
	cfg.Provider.Model = "gpt-5.4"
	cfg.Provider.AutoRunMaxTurns = 1

	svc := New(dir, cfg)
	parent, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "parent orchestration",
		Objective: "coordinate follow-up docs work",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "docs reviewed"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	session, err := svc.StartSession(context.Background(), parent.TaskID, "terminal")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	_, snapshot, events, err := svc.PromptSession(context.Background(), session.SessionID, "/create the next durable docs task and stop cleanly")
	if err != nil {
		t.Fatalf("prompt session: %v", err)
	}
	if decisionCalls != 1 {
		t.Fatalf("expected session prompt to stop after one task_create, got %d decisions", decisionCalls)
	}
	if snapshot.State != task.StateActive || snapshot.StatusReasonCode != "" {
		t.Fatalf("expected active parent snapshot after bounded task_create prompt, got %+v", snapshot)
	}

	var sawTaskCreate bool
	for _, event := range events {
		switch event.Type {
		case "project_task_created":
			sawTaskCreate = true
		case "review_completed", "completion_rejected":
			t.Fatalf("did not expect review events after bounded task_create prompt, got %+v", events)
		}
	}
	if !sawTaskCreate {
		t.Fatalf("expected project_task_created in prompt response, got %+v", events)
	}

	listed, err := svc.ListTasks(context.Background())
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("expected parent plus created child, got %+v", listed)
	}

	if _, err := svc.Store.LoadReview(parent.TaskID); !os.IsNotExist(err) {
		t.Fatalf("expected no review artifact after bounded task_create prompt, got err=%v", err)
	}
}

func TestPromptSessionTaskCreateClearsParentBindingReuse(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# parent docs\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	parentTaskID := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{
			"output": [{
				"type": "message",
				"role": "assistant",
				"content": [{
					"type": "output_text",
					"text": "{\"action\":\"task_create\",\"summary\":\"Create a durable follow-up docs task.\",\"task_kind\":\"general_execution\",\"task_preset_id\":\"docs_lite\",\"task_title\":\"follow-up docs\",\"task_objective\":\"update README.md so it mentions child task execution\",\"task_criteria\":[\"README.md mentions child task execution\"],\"task_constraints\":[\"Do not use worker_spawn.\"],\"task_permission_mode_id\":\"\",\"project_step_id\":\"task:%s\",\"project_branch_id\":\"branch:%s\",\"plan_explanation\":\"\",\"plan_steps\":[],\"plan_patch_operations\":[],\"project_explanation\":\"\",\"project_steps\":[],\"project_branches\":[],\"project_patch_operations\":[],\"memory_kind\":\"\",\"memory_refs\":[],\"worker_id\":\"\",\"worker_role\":\"\",\"worker_objective\":\"\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
				}]
			}]
		}`, parentTaskID, parentTaskID)))
	}))
	defer server.Close()

	cfg := task.DefaultConfig()
	cfg.Provider.Mode = "openai-response"
	cfg.Provider.BaseURL = server.URL + "/v1"
	cfg.Provider.APIKeyEnv = "OPENAI_API_KEY"
	cfg.Provider.Model = "gpt-5.4"
	cfg.Provider.AutoRunMaxTurns = 1

	svc := New(dir, cfg)
	parent, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "parent orchestration",
		Objective: "coordinate follow-up docs work",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "docs reviewed"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	parentTaskID = parent.TaskID

	session, err := svc.StartSession(context.Background(), parent.TaskID, "terminal")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	if _, _, _, err := svc.PromptSession(context.Background(), session.SessionID, "/create a durable docs child and stop"); err != nil {
		t.Fatalf("prompt session: %v", err)
	}

	listed, err := svc.ListTasks(context.Background())
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("expected parent plus created child, got %+v", listed)
	}

	childTaskID := ""
	for _, entry := range listed {
		if entry.TaskID != parent.TaskID {
			childTaskID = entry.TaskID
			break
		}
	}
	if childTaskID == "" {
		t.Fatalf("expected created child task, got %+v", listed)
	}

	project, err := svc.GetProject(context.Background())
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if !projectHasBoundTask(project.Project.Steps, projectTaskStepID(parent.TaskID), parent.TaskID) {
		t.Fatalf("expected parent auto-tracked project binding to remain intact, got %+v", project.Project.Steps)
	}
	if !projectHasBoundTask(project.Project.Steps, projectTaskStepID(childTaskID), childTaskID) {
		t.Fatalf("expected child to auto-track onto its own project step after parent binding reuse was stripped, got %+v", project.Project.Steps)
	}
}

func TestPromptSessionTaskCreateStripsOrchestrationConstraintsFromChild(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs branch\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"output": [{
				"type": "message",
				"role": "assistant",
				"content": [{
					"type": "output_text",
					"text": "{\"action\":\"task_create\",\"summary\":\"Create the durable docs child.\",\"task_kind\":\"general_execution\",\"task_preset_id\":\"docs_lite\",\"task_title\":\"docs child\",\"task_objective\":\"perform the bound docs review task\",\"task_criteria\":[\"docs reviewed\"],\"task_constraints\":[\"Create exactly one durable general_execution/docs_lite task.\",\"Bind the new task to project step phase.docs and branch branch.docs.\",\"Do not use worker_spawn.\",\"Do not edit files in the parent task.\"],\"task_permission_mode_id\":\"\",\"project_step_id\":\"phase.docs\",\"project_branch_id\":\"branch.docs\",\"plan_explanation\":\"\",\"plan_steps\":[],\"plan_patch_operations\":[],\"project_explanation\":\"\",\"project_steps\":[],\"project_branches\":[],\"project_patch_operations\":[],\"memory_kind\":\"\",\"memory_refs\":[],\"worker_id\":\"\",\"worker_role\":\"\",\"worker_objective\":\"\",\"watch_interval\":\"\",\"watch_reason\":\"\",\"approval_scope\":\"\",\"approval_reason\":\"\"}"
				}]
			}]
		}`))
	}))
	defer server.Close()

	cfg := task.DefaultConfig()
	cfg.Provider.Mode = "openai-response"
	cfg.Provider.BaseURL = server.URL + "/v1"
	cfg.Provider.APIKeyEnv = "OPENAI_API_KEY"
	cfg.Provider.Model = "gpt-5.4"
	cfg.Provider.AutoRunMaxTurns = 1

	svc := New(dir, cfg)
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

	parent, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "parent orchestration",
		Objective: "coordinate follow-up docs work",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "docs reviewed"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	session, err := svc.StartSession(context.Background(), parent.TaskID, "terminal")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	if _, _, _, err := svc.PromptSession(context.Background(), session.SessionID, "/create the bound docs child and stop"); err != nil {
		t.Fatalf("prompt session: %v", err)
	}

	listed, err := svc.ListTasks(context.Background())
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("expected parent plus created child, got %+v", listed)
	}

	childTaskID := ""
	for _, entry := range listed {
		if entry.TaskID != parent.TaskID {
			childTaskID = entry.TaskID
			break
		}
	}
	if childTaskID == "" {
		t.Fatalf("expected created child task, got %+v", listed)
	}

	childSpec, err := svc.Store.LoadTask(childTaskID)
	if err != nil {
		t.Fatalf("load child task: %v", err)
	}
	if containsString(childSpec.Constraints, "Create exactly one durable general_execution/docs_lite task.") {
		t.Fatalf("expected child task to drop create-task orchestration constraint, got %+v", childSpec.Constraints)
	}
	if containsString(childSpec.Constraints, "Bind the new task to project step phase.docs and branch branch.docs.") {
		t.Fatalf("expected child task to drop project-binding orchestration constraint, got %+v", childSpec.Constraints)
	}
	if !containsString(childSpec.Constraints, "Do not use worker_spawn.") || !containsString(childSpec.Constraints, "Do not edit files in the parent task.") {
		t.Fatalf("expected child task to preserve child-safe constraints, got %+v", childSpec.Constraints)
	}

	project, err := svc.GetProject(context.Background())
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if !projectHasBoundTask(project.Project.Steps, "phase.docs", childTaskID) {
		t.Fatalf("expected explicit docs step to bind to created child, got %+v", project.Project.Steps)
	}
}

func TestPromptSessionPersistsRuntimeMessageOnFailure(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")
	t.Setenv("OPENAI_API_KEY", "test-key")

	cfg := task.DefaultConfig()
	cfg.Provider.Mode = "openai-response"
	cfg.Provider.BaseURL = "http://127.0.0.1:1/v1"
	cfg.Provider.APIKeyEnv = "OPENAI_API_KEY"
	cfg.Provider.Model = "gpt-5.4"

	svc := New(dir, cfg)
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Objective: "exercise provider failure",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "provider failure is surfaced"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	session, err := svc.StartSession(context.Background(), spec.TaskID, "terminal")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	updated, snapshot, events, err := svc.PromptSession(context.Background(), session.SessionID, "continue with the provider failure scenario")
	if err == nil {
		t.Fatal("expected prompt session to fail when provider request cannot reach the configured base_url")
	}
	if updated.SessionID != session.SessionID {
		t.Fatalf("expected updated session on failure, got %+v", updated)
	}
	if snapshot.TaskID != spec.TaskID {
		t.Fatalf("expected fallback snapshot for failed prompt session, got %+v", snapshot)
	}
	if len(events) != 0 {
		t.Fatalf("expected provider failure before task events are appended, got %+v", events)
	}

	_, messages, err := svc.ReadSession(context.Background(), session.SessionID)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected operator and runtime messages after failed prompt, got %+v", messages)
	}
	if messages[1].Role != "runtime" || !strings.Contains(strings.ToLower(messages[1].Content), "provider") {
		t.Fatalf("expected runtime failure message in session transcript, got %+v", messages[1])
	}
}

func TestCancelSessionAppendsRuntimeMessage(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs only\n")

	svc := New(dir, task.DefaultConfig())
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
	session, err := svc.StartSession(context.Background(), spec.TaskID, "terminal")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	cancelled, err := svc.CancelSession(context.Background(), session.SessionID)
	if err != nil {
		t.Fatalf("cancel session: %v", err)
	}
	if cancelled.Status != "cancelled" || cancelled.LastAction != "aborted" {
		t.Fatalf("expected cancelled session with aborted last action, got %+v", cancelled)
	}

	_, messages, err := svc.ReadSession(context.Background(), session.SessionID)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected one runtime cancellation message, got %+v", messages)
	}
	if messages[0].Role != "runtime" || !strings.Contains(strings.ToLower(messages[0].Content), "cancelled") {
		t.Fatalf("expected runtime cancellation message, got %+v", messages[0])
	}

	status, err := svc.Status(context.Background(), spec.TaskID)
	if err != nil {
		t.Fatalf("status after cancel: %v", err)
	}
	if status.State != task.StateAborted || status.StatusReasonCode != "aborted_user" {
		t.Fatalf("expected aborted_user status after session cancel, got %+v", status)
	}
}

func TestSpawnWorkerPreparesSnapshotWorkspace(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# parent docs\n")
	writeFile(t, filepath.Join(dir, "notes.md"), "parent note\n")
	writeFile(t, filepath.Join(dir, ".ngen", "hidden.txt"), "secret\n")

	cfg := task.DefaultConfig()
	cfg.Subagents.WorkspaceIsolation = "snapshot_copy"
	svc := New(dir, cfg)
	parent, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "parent",
		Objective: "manage a reviewer child",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "child workspace is isolated"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}

	worker, err := svc.SpawnWorker(context.Background(), parent.TaskID, "reviewer", "review parent docs")
	if err != nil {
		t.Fatalf("spawn worker: %v", err)
	}
	if worker.WorkspaceMode != "snapshot_copy" || worker.WorkspaceStatus != "prepared" {
		t.Fatalf("expected snapshot workspace, got %+v", worker)
	}
	if worker.WorkspaceRoot == dir {
		t.Fatalf("expected isolated workspace root, got parent root %s", worker.WorkspaceRoot)
	}
	if _, err := os.Stat(filepath.Join(worker.WorkspaceRoot, "notes.md")); err != nil {
		t.Fatalf("expected mirrored file in child workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(worker.WorkspaceRoot, ".ngen")); !os.IsNotExist(err) {
		t.Fatalf("expected .ngen to stay out of child snapshot, got err=%v", err)
	}
}

func TestSpawnWorkerPreparesGitWorktreeAndMirrorsDirtyState(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# initial\n")
	writeFile(t, filepath.Join(dir, "docs", "guide.md"), "guide\n")
	initGitRepo(t, dir)
	writeFile(t, filepath.Join(dir, "README.md"), "# dirty parent\n")
	writeFile(t, filepath.Join(dir, "scratch.txt"), "untracked scratch\n")

	cfg := task.DefaultConfig()
	cfg.Subagents.WorkspaceIsolation = "git_worktree"
	svc := New(dir, cfg)
	parent, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "parent",
		Objective: "manage a reviewer child",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "git child workspace exists"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}

	worker, err := svc.SpawnWorker(context.Background(), parent.TaskID, "reviewer", "review parent docs")
	if err != nil {
		t.Fatalf("spawn worker: %v", err)
	}
	if worker.WorkspaceMode != "git_worktree" || worker.WorkspaceStatus != "prepared" {
		t.Fatalf("expected git worktree workspace, got %+v", worker)
	}
	gotReadme, err := os.ReadFile(filepath.Join(worker.WorkspaceRoot, "README.md"))
	if err != nil {
		t.Fatalf("read child README: %v", err)
	}
	if string(gotReadme) != "# dirty parent\n" {
		t.Fatalf("expected dirty parent state mirrored into child worktree, got %q", string(gotReadme))
	}
	gotScratch, err := os.ReadFile(filepath.Join(worker.WorkspaceRoot, "scratch.txt"))
	if err != nil {
		t.Fatalf("read child scratch file: %v", err)
	}
	if string(gotScratch) != "untracked scratch\n" {
		t.Fatalf("expected untracked file mirrored into child worktree, got %q", string(gotScratch))
	}
	cmd := exec.Command("git", "-C", worker.WorkspaceRoot, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("verify child git worktree: %v", err)
	}
	if strings.TrimSpace(string(out)) == "" {
		t.Fatalf("expected child workspace to be a git worktree, got empty toplevel")
	}
}

func TestContinueWorkerSettlesAcceptedChildAndReleasesWorkspace(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# parent docs\n")

	cfg := task.DefaultConfig()
	cfg.Subagents.WorkspaceIsolation = "snapshot_copy"
	svc := New(dir, cfg)
	parent, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "parent",
		Objective: "manage a reviewer child",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "child settles cleanly"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	worker, err := svc.SpawnWorker(context.Background(), parent.TaskID, "reviewer", "review parent docs")
	if err != nil {
		t.Fatalf("spawn worker: %v", err)
	}

	updated, err := svc.ContinueWorker(context.Background(), parent.TaskID, worker.WorkerID)
	if err != nil {
		t.Fatalf("continue worker: %v", err)
	}
	if updated.SettlementStatus != "accepted" {
		t.Fatalf("expected accepted settlement, got %+v", updated)
	}
	if updated.WorkspaceStatus != "released" {
		t.Fatalf("expected released workspace after accepted child completion, got %+v", updated)
	}
	if updated.ContinuationCount != 1 || updated.LastContinuedAt == "" || updated.LastReconciledAt == "" {
		t.Fatalf("expected continuation and reconcile timestamps, got %+v", updated)
	}
	if _, err := os.Stat(updated.WorkspaceRoot); !os.IsNotExist(err) {
		t.Fatalf("expected released child workspace root to be removed, got err=%v", err)
	}

	settlement, err := svc.Store.LoadWorkerSettlement(parent.TaskID, worker.WorkerID)
	if err != nil {
		t.Fatalf("load worker settlement: %v", err)
	}
	if settlement.Status != "accepted" || settlement.SettledAt == "" {
		t.Fatalf("expected persisted accepted settlement, got %+v", settlement)
	}
	workspace, err := svc.Store.LoadWorkerWorkspace(parent.TaskID, worker.WorkerID)
	if err != nil {
		t.Fatalf("load worker workspace: %v", err)
	}
	if workspace.Status != "released" || workspace.ReleasedAt == "" {
		t.Fatalf("expected persisted released workspace record, got %+v", workspace)
	}

	events, err := svc.Store.ReadEvents(parent.TaskID)
	if err != nil {
		t.Fatalf("read parent events: %v", err)
	}
	var sawSettled, sawReleased bool
	for _, event := range events {
		switch event.Type {
		case "worker_settled":
			sawSettled = true
		case "worker_workspace_released":
			sawReleased = true
		}
	}
	if !sawSettled || !sawReleased {
		t.Fatalf("expected settle/release events, got %+v", events)
	}
}

func TestContinueWorkerAppliesGeneralChildReconcileToParentWorkspace(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# parent docs\n")

	cfg := task.DefaultConfig()
	cfg.Subagents.WorkspaceIsolation = "snapshot_copy"
	svc := New(dir, cfg)
	parent, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "parent",
		Objective: "manage a docs child",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "child reconcile applies"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	worker, err := svc.SpawnWorker(context.Background(), parent.TaskID, "general_execution", "update parent docs")
	if err != nil {
		t.Fatalf("spawn worker: %v", err)
	}
	baseline, err := svc.Store.LoadWorkerBaseline(parent.TaskID, worker.WorkerID)
	if err != nil {
		t.Fatalf("load worker baseline: %v", err)
	}
	if baseline.FileCount == 0 || worker.WorkspaceRef == "" {
		t.Fatalf("expected persisted worker baseline and workspace refs, got baseline=%+v worker=%+v", baseline, worker)
	}

	writeFile(t, filepath.Join(worker.WorkspaceRoot, "README.md"), "# child docs update\n")
	updated, err := svc.ContinueWorker(context.Background(), parent.TaskID, worker.WorkerID)
	if err != nil {
		t.Fatalf("continue worker: %v", err)
	}
	if updated.ReconcileMode != "apply_on_accept" || updated.ReconcileStatus != "applied" {
		t.Fatalf("expected applied reconcile for general child, got %+v", updated)
	}
	got, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatalf("read parent README: %v", err)
	}
	if string(got) != "# child docs update\n" {
		t.Fatalf("expected parent README to receive child reconcile, got %q", string(got))
	}

	reconcile, err := svc.Store.LoadWorkerReconcile(parent.TaskID, worker.WorkerID)
	if err != nil {
		t.Fatalf("load worker reconcile: %v", err)
	}
	if reconcile.Status != "applied" || reconcile.AppliedCount != 1 || reconcile.WorkspaceEditRef == "" {
		t.Fatalf("expected persisted applied reconcile, got %+v", reconcile)
	}
	edits, err := svc.Store.ReadWorkspaceEdits(parent.TaskID)
	if err != nil {
		t.Fatalf("read workspace edits: %v", err)
	}
	if len(edits) == 0 {
		t.Fatal("expected worker reconcile workspace edit record")
	}
	last := edits[len(edits)-1]
	if last.Kind != "worker_reconcile" || last.Status != "applied" || len(last.FileChanges) != 1 {
		t.Fatalf("expected worker_reconcile edit record, got %+v", last)
	}
}

func TestContinueWorkerDetectsReconcileConflictAndKeepsWorkspace(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# parent docs\n")

	cfg := task.DefaultConfig()
	cfg.Subagents.WorkspaceIsolation = "snapshot_copy"
	svc := New(dir, cfg)
	parent, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "parent",
		Objective: "manage a docs child",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "conflicts stay explicit"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	worker, err := svc.SpawnWorker(context.Background(), parent.TaskID, "general_execution", "update parent docs")
	if err != nil {
		t.Fatalf("spawn worker: %v", err)
	}

	writeFile(t, filepath.Join(worker.WorkspaceRoot, "README.md"), "# child update\n")
	writeFile(t, filepath.Join(dir, "README.md"), "# parent drift\n")
	updated, err := svc.ContinueWorker(context.Background(), parent.TaskID, worker.WorkerID)
	if err != nil {
		t.Fatalf("continue worker: %v", err)
	}
	if updated.ReconcileStatus != "conflict" {
		t.Fatalf("expected conflict reconcile status, got %+v", updated)
	}
	if updated.WorkspaceStatus != "prepared" {
		t.Fatalf("expected conflicted child workspace to stay prepared, got %+v", updated)
	}
	got, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatalf("read parent README: %v", err)
	}
	if string(got) != "# parent drift\n" {
		t.Fatalf("expected parent drift to remain unchanged, got %q", string(got))
	}
	reconcile, err := svc.Store.LoadWorkerReconcile(parent.TaskID, worker.WorkerID)
	if err != nil {
		t.Fatalf("load worker reconcile: %v", err)
	}
	if reconcile.Status != "conflict" || reconcile.ConflictCount == 0 {
		t.Fatalf("expected persisted conflict reconcile, got %+v", reconcile)
	}
	if !reconcile.ParentTakeoverRequired || reconcile.ParentTakeoverSummary == "" || len(reconcile.ParentTakeoverRefs) == 0 {
		t.Fatalf("expected reconcile to preserve parent takeover guidance, got %+v", reconcile)
	}
	if _, err := os.Stat(updated.WorkspaceRoot); err != nil {
		t.Fatalf("expected conflicted child workspace to remain for inspection: %v", err)
	}
}

func TestContinueWorkerRecordsReviewerChildChangesWithoutApplyingThem(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# parent docs\n")

	cfg := task.DefaultConfig()
	cfg.Subagents.WorkspaceIsolation = "snapshot_copy"
	svc := New(dir, cfg)
	parent, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "parent",
		Objective: "manage a reviewer child",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "reviewer changes stay artifact-only"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	worker, err := svc.SpawnWorker(context.Background(), parent.TaskID, "reviewer", "review parent docs")
	if err != nil {
		t.Fatalf("spawn worker: %v", err)
	}

	writeFile(t, filepath.Join(worker.WorkspaceRoot, "README.md"), "# reviewer draft\n")
	updated, err := svc.ContinueWorker(context.Background(), parent.TaskID, worker.WorkerID)
	if err != nil {
		t.Fatalf("continue worker: %v", err)
	}
	if updated.ReconcileMode != "artifact_only" || updated.ReconcileStatus != "recorded" {
		t.Fatalf("expected artifact-only reviewer reconcile, got %+v", updated)
	}
	got, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatalf("read parent README: %v", err)
	}
	if string(got) != "# parent docs\n" {
		t.Fatalf("expected parent README to remain unchanged, got %q", string(got))
	}
	reconcile, err := svc.Store.LoadWorkerReconcile(parent.TaskID, worker.WorkerID)
	if err != nil {
		t.Fatalf("load worker reconcile: %v", err)
	}
	if reconcile.Status != "recorded" || reconcile.ChangeCount != 1 || reconcile.AppliedCount != 0 {
		t.Fatalf("expected recorded reconcile artifact, got %+v", reconcile)
	}
	if updated.WorkspaceStatus != "released" {
		t.Fatalf("expected artifact-only reviewer workspace to release cleanly, got %+v", updated)
	}
}

func TestCriteriaFromEvidenceUsesReviewerWorkerResultRefs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# parent docs\n")

	svc := New(dir, task.DefaultConfig())
	parent, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "parent",
		Objective: "manage reviewer child",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "reviewer child review is clear"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	worker, err := svc.SpawnWorker(context.Background(), parent.TaskID, "reviewer", "review parent docs")
	if err != nil {
		t.Fatalf("spawn reviewer worker: %v", err)
	}

	writeFile(t, filepath.Join(worker.WorkspaceRoot, "README.md"), "# reviewer draft\n")
	updated, err := svc.ContinueWorker(context.Background(), parent.TaskID, worker.WorkerID)
	if err != nil {
		t.Fatalf("continue worker: %v", err)
	}
	if updated.Status != "done" || updated.ResultRef == "" {
		t.Fatalf("expected completed reviewer worker result, got %+v", updated)
	}

	report := task.VerificationReport{
		SchemaVersion: task.SchemaVersion,
		TaskID:        parent.TaskID,
		Status:        "passed",
	}
	criteria := svc.criteriaFromEvidence(parent, report)
	status := criterionStatusForID(criteria, "SC-001")
	if status.Status != "met" {
		t.Fatalf("expected reviewer result criterion to be met, got %+v", status)
	}
	if !containsString(status.EvidenceRefs, filepath.ToSlash(filepath.Join("workers", worker.WorkerID+".json"))) {
		t.Fatalf("expected worker contract evidence ref, got %+v", status.EvidenceRefs)
	}
	if !containsString(status.EvidenceRefs, "worker_runtime/"+worker.WorkerID+".result.json") {
		t.Fatalf("expected worker result evidence ref, got %+v", status.EvidenceRefs)
	}
	if !containsString(status.EvidenceRefs, filepath.ToSlash(filepath.Join("..", worker.ChildTaskID, "reviews", "latest.json"))) {
		t.Fatalf("expected child review evidence ref, got %+v", status.EvidenceRefs)
	}
	if containsString(status.EvidenceRefs, "verification/latest.json") {
		t.Fatalf("did not expect generic verification evidence for worker criterion, got %+v", status.EvidenceRefs)
	}
}

func TestCriteriaFromEvidenceUsesWorkerReconcileRefs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# parent docs\n")

	cfg := task.DefaultConfig()
	cfg.Subagents.WorkspaceIsolation = "snapshot_copy"
	svc := New(dir, cfg)
	parent, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "parent",
		Objective: "manage docs child",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "child reconcile applies"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	worker, err := svc.SpawnWorker(context.Background(), parent.TaskID, "general_execution", "update parent docs")
	if err != nil {
		t.Fatalf("spawn general worker: %v", err)
	}

	writeFile(t, filepath.Join(worker.WorkspaceRoot, "README.md"), "# child docs update\n")
	updated, err := svc.ContinueWorker(context.Background(), parent.TaskID, worker.WorkerID)
	if err != nil {
		t.Fatalf("continue worker: %v", err)
	}
	if updated.ReconcileStatus != "applied" || updated.ReconcileRef == "" {
		t.Fatalf("expected applied reconcile, got %+v", updated)
	}

	report := task.VerificationReport{
		SchemaVersion: task.SchemaVersion,
		TaskID:        parent.TaskID,
		Status:        "passed",
	}
	criteria := svc.criteriaFromEvidence(parent, report)
	status := criterionStatusForID(criteria, "SC-001")
	if status.Status != "met" {
		t.Fatalf("expected reconcile criterion to be met, got %+v", status)
	}
	if !containsString(status.EvidenceRefs, filepath.ToSlash(filepath.Join("workers", worker.WorkerID+".json"))) {
		t.Fatalf("expected worker contract evidence ref, got %+v", status.EvidenceRefs)
	}
	if !containsString(status.EvidenceRefs, "worker_runtime/"+worker.WorkerID+".reconcile.json") {
		t.Fatalf("expected worker reconcile evidence ref, got %+v", status.EvidenceRefs)
	}
	if !strings.Contains(strings.Join(status.EvidenceRefs, "\n"), "workspace_edits.jsonl#edit_record_id=") {
		t.Fatalf("expected reconcile workspace edit evidence ref, got %+v", status.EvidenceRefs)
	}
}

func TestCriteriaFromEvidenceUsesWorkerContinuationRefs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# worker continuation\n")

	svc := New(dir, task.DefaultConfig())
	parent, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "parent",
		Objective: "manage reviewer child",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "reviewer child worker continue is exposed"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	worker, err := svc.SpawnWorker(context.Background(), parent.TaskID, "reviewer", "review parent docs")
	if err != nil {
		t.Fatalf("spawn reviewer worker: %v", err)
	}
	record, err := svc.RequestApproval(context.Background(), worker.ChildTaskID, "manual step", "worker asks parent")
	if err != nil {
		t.Fatalf("request approval: %v", err)
	}
	if _, err := svc.DecideApproval(context.Background(), parent.TaskID, record.ApprovalID, "approved"); err != nil {
		t.Fatalf("approve owned worker approval: %v", err)
	}
	updated, err := svc.SyncWorker(context.Background(), parent.TaskID, worker.WorkerID)
	if err != nil {
		t.Fatalf("sync worker: %v", err)
	}
	if !updated.RequiresParentAction || updated.ParentActionType != "continue_child" {
		t.Fatalf("expected continuation-ready worker, got %+v", updated)
	}

	report := task.VerificationReport{
		SchemaVersion: task.SchemaVersion,
		TaskID:        parent.TaskID,
		Status:        "passed",
	}
	criteria := svc.criteriaFromEvidence(parent, report)
	status := criterionStatusForID(criteria, "SC-001")
	if status.Status != "met" {
		t.Fatalf("expected continuation criterion to be met, got %+v", status)
	}
	if !containsString(status.EvidenceRefs, "worker_runtime/"+worker.WorkerID+".result.json") {
		t.Fatalf("expected worker result evidence ref, got %+v", status.EvidenceRefs)
	}
	if !strings.Contains(strings.Join(status.EvidenceRefs, "\n"), "approvals.jsonl#approval_record_id=") {
		t.Fatalf("expected approval evidence ref, got %+v", status.EvidenceRefs)
	}
}

func TestCriteriaFromEvidenceKeepsWorkerCriterionOpenForRoleMismatch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# parent docs\n")

	svc := New(dir, task.DefaultConfig())
	parent, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "parent",
		Objective: "manage reviewer child",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "security child produces a compiled result"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	worker, err := svc.SpawnWorker(context.Background(), parent.TaskID, "reviewer", "review parent docs")
	if err != nil {
		t.Fatalf("spawn reviewer worker: %v", err)
	}

	writeFile(t, filepath.Join(worker.WorkspaceRoot, "README.md"), "# reviewer draft\n")
	if _, err := svc.ContinueWorker(context.Background(), parent.TaskID, worker.WorkerID); err != nil {
		t.Fatalf("continue worker: %v", err)
	}

	report := task.VerificationReport{
		SchemaVersion: task.SchemaVersion,
		TaskID:        parent.TaskID,
		Status:        "passed",
	}
	criteria := svc.criteriaFromEvidence(parent, report)
	status := criterionStatusForID(criteria, "SC-001")
	if status.Status != "open" || len(status.EvidenceRefs) != 0 {
		t.Fatalf("expected mismatched worker-role criterion to stay open, got %+v", status)
	}
}

func TestSpawnWorkerRejectsNestedWorkersForReviewerChild(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# parent docs\n")

	cfg := task.DefaultConfig()
	cfg.Subagents.WorkspaceIsolation = "snapshot_copy"
	svc := New(dir, cfg)
	parent, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "parent",
		Objective: "manage reviewer child",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "reviewer child exists"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	worker, err := svc.SpawnWorker(context.Background(), parent.TaskID, "reviewer", "review parent docs")
	if err != nil {
		t.Fatalf("spawn reviewer worker: %v", err)
	}

	_, err = svc.SpawnWorker(context.Background(), worker.ChildTaskID, "reviewer", "review nested docs")
	if err == nil {
		t.Fatal("expected reviewer child nested worker spawn to fail")
	}
	if !strings.Contains(err.Error(), "not allowed to spawn child workers") {
		t.Fatalf("unexpected nested reviewer error: %v", err)
	}
}

func TestSpawnWorkerAllowsGrandchildWithinLineageBudget(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# parent docs\n")

	cfg := task.DefaultConfig()
	cfg.Subagents.WorkspaceIsolation = "snapshot_copy"
	cfg.Subagents.MaxLineageDepth = 2
	svc := New(dir, cfg)
	parent, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     "parent",
		Objective: "manage coding child",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "child lineage is tracked"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	child, err := svc.SpawnWorker(context.Background(), parent.TaskID, "coding", "update child workspace")
	if err != nil {
		t.Fatalf("spawn coding child: %v", err)
	}
	grandchild, err := svc.SpawnWorker(context.Background(), child.ChildTaskID, "reviewer", "review the child output")
	if err != nil {
		t.Fatalf("spawn reviewer grandchild: %v", err)
	}
	if grandchild.RootTaskID != parent.TaskID || grandchild.LineageDepth != 2 {
		t.Fatalf("expected reviewer grandchild to inherit root/depth, got %+v", grandchild)
	}
	if grandchild.SubagentPolicy == nil || grandchild.SubagentPolicy.MaxLineageDepth != 2 {
		t.Fatalf("expected reviewer grandchild to inherit bounded lineage policy, got %+v", grandchild.SubagentPolicy)
	}
}

func TestSpawnWorkerRebindsReleasedChildWorkspaceBeforeGrandchildSpawn(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/policy\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package policy\n\nfunc Add(a, b int) int { return a + b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package policy\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")

	cfg := task.DefaultConfig()
	cfg.Subagents.WorkspaceIsolation = "snapshot_copy"
	cfg.Subagents.MaxLineageDepth = 2
	svc := New(dir, cfg)
	parent, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     "parent",
		Objective: "manage coding child",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "child workspace rebinding works"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	child, err := svc.SpawnWorker(context.Background(), parent.TaskID, "coding", "refine the coding workspace")
	if err != nil {
		t.Fatalf("spawn coding child: %v", err)
	}
	updated, err := svc.ContinueWorker(context.Background(), parent.TaskID, child.WorkerID)
	if err != nil {
		t.Fatalf("continue coding child: %v", err)
	}
	if updated.WorkspaceStatus != "released" {
		t.Fatalf("expected coding child workspace to release cleanly, got %+v", updated)
	}

	childSpec, err := svc.Store.LoadTask(child.ChildTaskID)
	if err != nil {
		t.Fatalf("load child spec: %v", err)
	}
	if childSpec.WorkspaceRoot != dir {
		t.Fatalf("expected child task workspace root to rebind to parent workspace, got %+v", childSpec)
	}

	grandchild, err := svc.SpawnWorker(context.Background(), child.ChildTaskID, "reviewer", "review the coding child output")
	if err != nil {
		t.Fatalf("spawn reviewer grandchild after child release: %v", err)
	}
	if grandchild.RootTaskID != parent.TaskID || grandchild.LineageDepth != 2 {
		t.Fatalf("expected reviewer grandchild to preserve lineage after workspace rebinding, got %+v", grandchild)
	}
}

func TestSpawnWorkerRejectsWhenLineageBudgetIsExhausted(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# parent docs\n")

	cfg := task.DefaultConfig()
	cfg.Subagents.WorkspaceIsolation = "snapshot_copy"
	cfg.Subagents.MaxLineageDepth = 1
	svc := New(dir, cfg)
	parent, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     "parent",
		Objective: "manage coding child",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "lineage stays bounded"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	child, err := svc.SpawnWorker(context.Background(), parent.TaskID, "coding", "update child workspace")
	if err != nil {
		t.Fatalf("spawn coding child: %v", err)
	}

	_, err = svc.SpawnWorker(context.Background(), child.ChildTaskID, "reviewer", "review nested docs")
	if err == nil {
		t.Fatal("expected nested worker spawn to fail at lineage depth limit")
	}
	if !strings.Contains(err.Error(), "max child lineage depth") {
		t.Fatalf("unexpected lineage limit error: %v", err)
	}
}

func TestSpawnWorkerRespectsAllowedWorkerRoles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# parent docs\n")

	cfg := task.DefaultConfig()
	cfg.Subagents.WorkspaceIsolation = "snapshot_copy"
	cfg.Subagents.RolePolicies = map[string]task.SubagentRolePolicy{
		string(task.KindGeneral): {
			AllowedWorkerRoles: []string{string(task.KindReviewer)},
		},
	}
	svc := New(dir, cfg)
	parent, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "parent",
		Objective: "manage reviewer child only",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "allowed roles are enforced"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	if _, err := svc.SpawnWorker(context.Background(), parent.TaskID, "reviewer", "review the parent docs"); err != nil {
		t.Fatalf("expected reviewer child to be allowed, got %v", err)
	}
	if _, err := svc.SpawnWorker(context.Background(), parent.TaskID, "coding", "modify the parent docs"); err == nil {
		t.Fatal("expected coding child to be rejected by allowed worker roles")
	}
}

func TestContinueWorkerUsesRoleAutoReleaseOverride(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# parent docs\n")

	cfg := task.DefaultConfig()
	cfg.Subagents.WorkspaceIsolation = "snapshot_copy"
	cfg.Subagents.RolePolicies = map[string]task.SubagentRolePolicy{
		string(task.KindReviewer): {
			AutoReleaseOnSuccess: boolPtr(false),
		},
	}
	svc := New(dir, cfg)
	parent, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "parent",
		Objective: "manage reviewer child",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "workspace release is configurable"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	worker, err := svc.SpawnWorker(context.Background(), parent.TaskID, "reviewer", "review parent docs")
	if err != nil {
		t.Fatalf("spawn reviewer worker: %v", err)
	}

	writeFile(t, filepath.Join(worker.WorkspaceRoot, "README.md"), "# reviewer draft\n")
	updated, err := svc.ContinueWorker(context.Background(), parent.TaskID, worker.WorkerID)
	if err != nil {
		t.Fatalf("continue worker: %v", err)
	}
	if updated.WorkspaceStatus != "prepared" {
		t.Fatalf("expected reviewer workspace to stay prepared when auto release is disabled, got %+v", updated)
	}
	if updated.SubagentPolicy == nil || updated.SubagentPolicy.AutoReleaseOnSuccess {
		t.Fatalf("expected reviewer policy to carry auto release override, got %+v", updated.SubagentPolicy)
	}
	if _, err := os.Stat(updated.WorkspaceRoot); err != nil {
		t.Fatalf("expected reviewer workspace to remain on disk: %v", err)
	}
}

func TestContinueWorkerUsesRoleReconcileOverride(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# parent docs\n")

	cfg := task.DefaultConfig()
	cfg.Subagents.WorkspaceIsolation = "snapshot_copy"
	cfg.Subagents.RolePolicies = map[string]task.SubagentRolePolicy{
		string(task.KindGeneral): {
			ReconcileMode: "artifact_only",
		},
	}
	svc := New(dir, cfg)
	parent, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "parent",
		Objective: "manage docs child",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "reconcile mode is configurable"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	worker, err := svc.SpawnWorker(context.Background(), parent.TaskID, "general_execution", "update parent docs")
	if err != nil {
		t.Fatalf("spawn general worker: %v", err)
	}

	writeFile(t, filepath.Join(worker.WorkspaceRoot, "README.md"), "# child docs update\n")
	updated, err := svc.ContinueWorker(context.Background(), parent.TaskID, worker.WorkerID)
	if err != nil {
		t.Fatalf("continue worker: %v", err)
	}
	if updated.ReconcileMode != "artifact_only" || updated.ReconcileStatus != "recorded" {
		t.Fatalf("expected general child reconcile override to record changes only, got %+v", updated)
	}
	got, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatalf("read parent README: %v", err)
	}
	if string(got) != "# parent docs\n" {
		t.Fatalf("expected parent README to remain unchanged under artifact_only override, got %q", string(got))
	}
}

func TestValidateObservationCommandRejectsShellWrapper(t *testing.T) {
	if err := validateObservationCommand([]string{"rg", "-n", "Add", "."}); err != nil {
		t.Fatalf("expected rg to be allowed, got %v", err)
	}
	issueID := "0496acb9-ff48-4507-bb79-d122a68c3a98"
	if err := validateObservationCommand([]string{"multica", "issue", "get", issueID, "--output", "json"}); err != nil {
		t.Fatalf("expected read-only multica issue get to be allowed, got %v", err)
	}
	if err := validateObservationCommandWorkspacePathsWithDeny(t.TempDir(), []string{"multica", "issue", "get", issueID, "--output", "json"}, []string{".ngen"}); err != nil {
		t.Fatalf("expected multica issue get not to be interpreted as workspace paths, got %v", err)
	}
	if err := validateObservationCommand([]string{"multica", "issue", "comment", "add", issueID, "--content", "done"}); err == nil {
		t.Fatal("expected mutating multica issue comment add to be rejected as observation")
	}
	if err := validateObservationCommand([]string{"bash", "-lc", "touch hacked"}); err == nil {
		t.Fatal("expected bash shell wrapper to be rejected")
	}
	if err := validateObservationCommand([]string{"find", ".", "-exec", "cat", "{}", ";"}); err == nil {
		t.Fatal("expected find -exec to be rejected")
	}
	if err := validateObservationCommand([]string{"git", "-C", "/tmp", "status"}); err == nil {
		t.Fatal("expected git -C to be rejected")
	}
}

func TestHeuristicObservationCommandsIncludeMulticaIssueContext(t *testing.T) {
	issueID := "0496acb9-ff48-4507-bb79-d122a68c3a98"
	commands := heuristicObservationCommands(task.Spec{
		Objective: "Multica issue execution mode for issue " + issueID + ".",
	}, task.VerificationReport{}, provider.WorkspaceCollection{}, 2)
	if len(commands) != 2 {
		t.Fatalf("expected two Multica issue observation commands, got %+v", commands)
	}
	if !slices.Equal(commands[0].Argv, []string{"multica", "issue", "get", issueID, "--output", "json"}) {
		t.Fatalf("unexpected issue get command: %+v", commands[0])
	}
	if !slices.Equal(commands[1].Argv, []string{"multica", "issue", "comment", "list", issueID, "--output", "json"}) {
		t.Fatalf("unexpected issue comment list command: %+v", commands[1])
	}
	if got := heuristicObservationCommands(task.Spec{Objective: "ordinary task"}, task.VerificationReport{}, provider.WorkspaceCollection{}, 2); len(got) != 0 {
		t.Fatalf("expected ordinary task not to get Multica observation commands, got %+v", got)
	}
}

func TestMulticaIssueFallbackWorkspaceEditPlanPostsMarkerComment(t *testing.T) {
	issueID := "0496acb9-ff48-4507-bb79-d122a68c3a98"
	marker := "ngen-multica-real-e2e-ok"
	cfg := task.DefaultConfig()
	cfg.Provider.Mode = "openai-response"
	cfg.Provider.Model = "gpt-5.5"
	cfg.Provider.ThinkingLevel = "xhigh"
	svc := New(t.TempDir(), cfg)
	spec := task.Spec{
		TaskID:    "TASK-001",
		Objective: "Multica issue execution mode for issue " + issueID + ".",
		SuccessCriteria: []task.SuccessCriterion{
			{Statement: `multica-result.md contains the phrase "multica issue comment add".`},
		},
	}
	observations := []provider.ObservationResult{
		{
			Argv:          []string{"multica", "issue", "get", issueID, "--output", "json"},
			StdoutExcerpt: `{"description":"Produce marker ` + marker + ` as an issue comment."}`,
		},
	}
	plan, ok := svc.multicaIssueFallbackWorkspaceEditPlan(spec, observations, fmt.Errorf("responses workspace edit returned empty output text"))
	if !ok {
		t.Fatal("expected Multica fallback plan")
	}
	if len(plan.Writes) != 1 || plan.Writes[0].Path != "multica-result.md" {
		t.Fatalf("unexpected fallback writes: %+v", plan.Writes)
	}
	if !strings.Contains(plan.Writes[0].Content, marker) ||
		!strings.Contains(plan.Writes[0].Content, "Provider route: openai-response/gpt-5.5") ||
		!strings.Contains(plan.Writes[0].Content, "Reasoning effort: xhigh") {
		t.Fatalf("fallback content missing marker/provider evidence:\n%s", plan.Writes[0].Content)
	}
	if len(plan.Commands) != 1 {
		t.Fatalf("expected one marker comment command, got %+v", plan.Commands)
	}
	want := []string{"multica", "issue", "comment", "add", issueID, "--content-file", "multica-result.md", "--output", "json"}
	if !slices.Equal(plan.Commands[0].Argv, want) {
		t.Fatalf("unexpected fallback command: %+v", plan.Commands[0].Argv)
	}
	if _, ok := svc.multicaIssueFallbackWorkspaceEditPlan(task.Spec{Objective: "ordinary task"}, observations, fmt.Errorf("responses workspace edit returned empty output text")); ok {
		t.Fatal("expected fallback to stay disabled for ordinary tasks")
	}
}

func TestValidateExecutionCommandPolicyByPermissionMode(t *testing.T) {
	svc := New(t.TempDir(), task.DefaultConfig())
	if err := svc.validateExecutionCommand([]string{"gofmt", "-w", "calc.go"}, task.PermissionModeStandard); err != nil {
		t.Fatalf("expected gofmt to be allowed in standard mode, got %v", err)
	}
	if err := svc.validateExecutionCommand([]string{"bash", "-lc", "printf ok"}, task.PermissionModeStandard); err == nil {
		t.Fatal("expected bash shell wrapper to require approval in standard mode")
	}
	if err := svc.validateExecutionCommand([]string{"bash", "-lc", "printf ok"}, task.PermissionModeYolo); err != nil {
		t.Fatalf("expected bash shell wrapper to be allowed in yolo mode, got %v", err)
	}
	if got := svc.executionCommandPolicyDecision([]string{"bash", "-lc", "printf ok"}, task.PermissionModeStandard); got != "needs_approval" {
		t.Fatalf("expected bash to be classified needs_approval, got %s", got)
	}
	issueID := "0496acb9-ff48-4507-bb79-d122a68c3a98"
	multicaComment := []string{"multica", "issue", "comment", "add", issueID, "--content", "done", "--output", "json"}
	if got := svc.executionCommandPolicyDecision(multicaComment, task.PermissionModeStandard); got != "needs_approval" {
		t.Fatalf("expected multica issue mutation to be classified needs_approval, got %s", got)
	}
	if err := svc.validateExecutionCommand(multicaComment, task.PermissionModeYolo); err != nil {
		t.Fatalf("expected multica issue mutation to be allowed in yolo mode, got %v", err)
	}
	if err := svc.validateExecutionCommand([]string{}, task.PermissionModeStandard); err == nil {
		t.Fatal("expected empty repair command argv to be rejected")
	}
}

func TestValidateExecutionCommandBenchmarkIntegrityMode(t *testing.T) {
	cfg := task.DefaultConfig()
	cfg.Permission.DefaultMode = task.PermissionModeYolo
	cfg.Permission.BenchmarkIntegrityMode = true
	svc := New(t.TempDir(), cfg)
	if err := svc.validateExecutionCommand([]string{"gofmt", "-w", "calc.go"}, task.PermissionModeYolo); err != nil {
		t.Fatalf("expected local formatter to be allowed in benchmark integrity mode, got %v", err)
	}
	for _, argv := range [][]string{
		{"curl", "https://example.com/solution"},
		{"bash", "-lc", "curl https://example.com/solution"},
		{"go", "mod", "download"},
		{"git", "clone", "https://example.com/repo.git"},
		{"./build.sh", "test"},
		{"multica", "issue", "comment", "add", "0496acb9-ff48-4507-bb79-d122a68c3a98", "--content", "done"},
	} {
		if err := svc.validateExecutionCommand(argv, task.PermissionModeYolo); err == nil {
			t.Fatalf("expected benchmark integrity mode to reject %v", argv)
		}
		if got := svc.executionCommandPolicyDecision(argv, task.PermissionModeYolo); got != "denied_benchmark_integrity" {
			t.Fatalf("expected denied_benchmark_integrity for %v, got %s", argv, got)
		}
		safety := commandReplaySafety(argv, task.PermissionModeYolo, svc.executionCommandPolicyDecision(argv, task.PermissionModeYolo))
		if safety == nil || safety.ReplayPolicy != "do_not_auto_replay" || !safety.Network || !safety.OpenWorld {
			t.Fatalf("expected benchmark integrity replay safety to block %v, got %+v", argv, safety)
		}
	}
}

func TestRecentRepairFailuresIncludesRepairCommandFailures(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# failures\n")

	svc := New(dir, task.DefaultConfig())
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
	if err := svc.Store.AppendCommandRun(task.CommandRunRecord{
		SchemaVersion:   task.SchemaVersion,
		CommandRecordID: task.NewID("CMDREC"),
		CommandID:       task.NewID("CMD"),
		TaskID:          spec.TaskID,
		TS:              task.Now(),
		Kind:            "repair_command",
		Status:          "failed",
		Summary:         "go generate ./... failed: exit status 1",
		Argv:            []string{"go", "generate", "./..."},
		ExitCode:        1,
	}); err != nil {
		t.Fatalf("append command run: %v", err)
	}

	failures := svc.recentRepairFailures(spec.TaskID, 3)
	if len(failures) != 1 {
		t.Fatalf("expected one repair failure, got %+v", failures)
	}
	if failures[0].Stage != "workspace_command_failed" {
		t.Fatalf("expected workspace_command_failed stage, got %+v", failures[0])
	}
	if !strings.Contains(failures[0].Summary, "go generate ./... failed") {
		t.Fatalf("expected repair failure summary from command run, got %+v", failures[0])
	}
}

func TestValidateObservationCommandWorkspacePathsRejectsPathOutsideWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := validateObservationCommandWorkspacePaths(dir, []string{"cat", "/etc/passwd"}); err == nil {
		t.Fatal("expected absolute path outside workspace to be rejected")
	}
	if err := validateObservationCommandWorkspacePaths(dir, []string{"cat", "../../etc/passwd"}); err == nil {
		t.Fatal("expected relative escape path to be rejected")
	}
}

func TestValidateObservationCommandWorkspacePathsAllowsWorkspaceAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "calc.go")
	writeFile(t, target, "package main\n")

	if err := validateObservationCommandWorkspacePaths(dir, []string{"sed", "-n", "1,40p", target}); err != nil {
		t.Fatalf("expected workspace absolute path to be allowed, got %v", err)
	}
}

func TestValidateObservationCommandWorkspacePathsRejectsDeniedVisibilityPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".ngen", "private.txt"), "secret\n")

	err := validateObservationCommandWorkspacePathsWithDeny(dir, []string{"cat", ".ngen/private.txt"}, []string{".git", ".ngen"})
	if err == nil || !strings.Contains(err.Error(), "denied by visibility rules") {
		t.Fatalf("expected hidden path to be rejected by visibility rules, got %v", err)
	}

	abs := filepath.Join(dir, ".ngen", "private.txt")
	err = validateObservationCommandWorkspacePathsWithDeny(dir, []string{"cat", abs}, []string{".git", ".ngen"})
	if err == nil || !strings.Contains(err.Error(), "denied by visibility rules") {
		t.Fatalf("expected hidden absolute path to be rejected by visibility rules, got %v", err)
	}
}

func TestObservationCommandsRejectVisibilityBypassFlags(t *testing.T) {
	for _, argv := range [][]string{
		{"rg", "--hidden", "secret", "."},
		{"rg", "-uuu", "secret", "."},
		{"rg", "--follow", "secret", "."},
		{"ls", "-la", "."},
		{"ls", "--all", "."},
	} {
		if err := validateObservationCommand(argv); err == nil {
			t.Fatalf("expected visibility bypass argv to be rejected: %+v", argv)
		}
	}
}

func TestGoObservationRejectsMutatingAndVerifierCommands(t *testing.T) {
	for _, argv := range [][]string{
		{"go", "test", "./..."},
		{"go", "build", "./..."},
		{"go", "env", "-w", "GOPROXY=off"},
		{"go", "env", "-u", "GOPROXY"},
		{"go", "list", "-mod=mod", "./..."},
		{"go", "list", "-modfile=../go.mod", "./..."},
		{"go", "list", "-overlay=../overlay.json", "./..."},
	} {
		if err := validateObservationCommand(argv); err == nil {
			t.Fatalf("expected unsafe go observation argv to be rejected: %+v", argv)
		}
	}
	for _, argv := range [][]string{
		{"go", "version"},
		{"go", "env", "GOVERSION"},
		{"go", "list", "./..."},
		{"go", "doc", "fmt.Println"},
	} {
		if err := validateObservationCommand(argv); err != nil {
			t.Fatalf("expected read-only go observation argv to be allowed: %+v: %v", argv, err)
		}
	}
}

func TestRGFilesObservationValidatesPathOperands(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".ngen", "private.txt"), "secret\n")

	for _, argv := range [][]string{
		{"rg", "--files", "/etc"},
		{"rg", "--files", ".ngen"},
	} {
		if err := validateObservationCommand(argv); err != nil {
			t.Fatalf("basic rg validation should leave path parser to reject %+v: %v", argv, err)
		}
		if err := validateObservationCommandWorkspacePathsWithDeny(dir, argv, []string{".ngen"}); err == nil {
			t.Fatalf("expected rg --files path to be rejected: %+v", argv)
		}
	}
}

func TestObservationCommandsRejectBroadNonHiddenDeniedRoots(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "private", "note.txt"), "secret\n")

	for _, argv := range [][]string{
		{"rg", "secret", "."},
		{"rg", "--files"},
		{"ls"},
		{"ls", "."},
	} {
		if err := validateObservationCommand(argv); err != nil {
			t.Fatalf("basic validation should leave path parser to reject %+v: %v", argv, err)
		}
		if err := validateObservationCommandWorkspacePathsWithDeny(dir, argv, []string{"private"}); err == nil {
			t.Fatalf("expected broad observation path to reject non-hidden denied root: %+v", argv)
		}
	}

	for _, argv := range [][]string{
		{"rg", "secret", "."},
		{"rg", "--files"},
		{"ls"},
		{"ls", "."},
	} {
		if err := validateObservationCommandWorkspacePathsWithDeny(dir, argv, []string{".ngen"}); err != nil {
			t.Fatalf("hidden deny roots should rely on command hidden-default guards for %+v, got %v", argv, err)
		}
	}
}

func TestFindObservationRejectsOptionBeforeExternalPath(t *testing.T) {
	dir := t.TempDir()
	for _, argv := range [][]string{
		{"find", "-H", "/etc", "-maxdepth", "1"},
		{"find", "-L", "."},
		{"find", "-maxdepth", "1", "/etc"},
	} {
		if err := validateObservationCommand(argv); err != nil {
			continue
		}
		if err := validateObservationCommandWorkspacePaths(dir, argv); err == nil {
			t.Fatalf("expected find argv to be rejected: %+v", argv)
		}
	}
}

func TestFindObservationRejectsRootsCoveringDeniedVisibilityPaths(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".ngen", "private.txt"), "secret\n")

	for _, argv := range [][]string{
		{"find", ".", "-maxdepth", "1", "-type", "f"},
		{"find", "-maxdepth", "1", "-type", "f"},
	} {
		if err := validateObservationCommand(argv); err != nil {
			t.Fatalf("basic validation should leave path parser to reject %+v: %v", argv, err)
		}
		if err := validateObservationCommandWorkspacePathsWithDeny(dir, argv, []string{".ngen"}); err == nil {
			t.Fatalf("expected broad find root to be rejected: %+v", argv)
		}
	}

	if err := validateObservationCommandWorkspacePathsWithDeny(dir, []string{"find", "docs", "-maxdepth", "1", "-type", "f"}, []string{".ngen"}); err != nil {
		t.Fatalf("expected narrow find root outside denied paths to be allowed, got %v", err)
	}
}

func TestGitObservationRejectsWorkspaceAndDenyBypasses(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".ngen", "private.txt"), "secret\n")

	for _, argv := range [][]string{
		{"git", "diff", "--no-index", "/etc/passwd", "README.md"},
		{"git", "diff", "--output=/tmp/diff.txt"},
		{"git", "show", "HEAD:.ngen/private.txt"},
		{"git", "status", "--ignored"},
		{"git", "grep", "-f", "/etc/patterns"},
	} {
		if err := validateObservationCommand(argv); err == nil {
			t.Fatalf("expected unsafe git observation argv to be rejected: %+v", argv)
		}
	}

	for _, argv := range [][]string{
		{"git", "grep", "secret"},
		{"git", "grep", "secret", ".ngen"},
		{"git", "grep", "secret", "--", ".ngen"},
		{"git", "status", ".ngen"},
		{"git", "status", "--", ".ngen"},
		{"git", "diff"},
		{"git", "diff", ".ngen"},
		{"git", "diff", "--", ".ngen"},
		{"git", "show", "--", ".ngen/private.txt"},
		{"git", "ls-files"},
		{"git", "ls-files", "--", ".ngen"},
	} {
		if err := validateObservationCommand(argv); err != nil {
			t.Fatalf("basic validation should leave path parser to reject %+v: %v", argv, err)
		}
		if err := validateObservationCommandWorkspacePathsWithDeny(dir, argv, []string{".ngen"}); err == nil {
			t.Fatalf("expected git observation path to be rejected: %+v", argv)
		}
	}

	for _, argv := range [][]string{
		{"git", "status", "--short"},
		{"git", "status", "--short", "--", "docs"},
		{"git", "grep", "secret", "--", "docs"},
		{"git", "diff", "--", "docs"},
		{"git", "show", "--", "docs/README.md"},
		{"git", "ls-files", "--", "docs"},
		{"git", "rev-parse", "--show-toplevel"},
	} {
		if err := validateObservationCommand(argv); err != nil {
			t.Fatalf("expected basic git observation argv to be allowed: %+v: %v", argv, err)
		}
		if err := validateObservationCommandWorkspacePathsWithDeny(dir, argv, []string{".ngen"}); err != nil {
			t.Fatalf("expected bounded git observation path to be allowed: %+v: %v", argv, err)
		}
	}
}

func TestFindObservationRejectsPredicateExternalOperand(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# docs\n")

	for _, argv := range [][]string{
		{"find", ".", "-newer", "/etc/passwd"},
		{"find", ".", "-samefile", "/etc/passwd"},
		{"find", ".", "-name", "*.md", ".ngen/private.txt"},
	} {
		if err := validateObservationCommand(argv); err != nil {
			t.Fatalf("basic validation should leave path parser to reject %+v: %v", argv, err)
		}
		if err := validateObservationCommandWorkspacePathsWithDeny(dir, argv, []string{".ngen"}); err == nil {
			t.Fatalf("expected find argv to be rejected: %+v", argv)
		}
	}
}

func TestWorkspaceEditRejectsIntermediateSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	svc := New(dir, task.DefaultConfig())
	_, err := svc.applyWorkspaceEditPlan(task.Spec{
		TaskID:        "TASK-001",
		Kind:          task.KindCoding,
		WorkspaceRoot: dir,
	}, "EDIT-001", provider.WorkspaceEditPlan{
		Summary: "write through link",
		Writes:  []provider.WorkspaceWrite{{Path: "link/outside.txt", Content: "escape\n"}},
	})
	if err == nil {
		t.Fatal("expected workspace edit through intermediate symlink to be rejected")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "outside.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("workspace edit should not create outside file, stat err=%v", statErr)
	}
}

func TestWorkspaceEditRejectsFinalSymlinkWrite(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "target.txt")
	writeFile(t, outside, "secret\n")
	if err := os.Symlink(outside, filepath.Join(dir, "alias.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	svc := New(dir, task.DefaultConfig())
	_, err := svc.applyWorkspaceEditPlan(task.Spec{
		TaskID:        "TASK-001",
		Kind:          task.KindCoding,
		WorkspaceRoot: dir,
	}, "EDIT-001", provider.WorkspaceEditPlan{
		Summary: "write final symlink",
		Writes:  []provider.WorkspaceWrite{{Path: "alias.txt", Content: "changed\n"}},
	})
	if err == nil {
		t.Fatal("expected workspace edit final symlink write to be rejected")
	}
	if got := readFile(t, outside); got != "secret\n" {
		t.Fatalf("workspace edit followed final symlink, target now %q", got)
	}
}

func TestWorkspaceSnapshotSkipsSymlinkTargets(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	writeFile(t, outside, "secret\n")
	writeFile(t, filepath.Join(dir, ".ngen", "private.txt"), "state secret\n")
	if err := os.Symlink(outside, filepath.Join(dir, "outside.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, ".ngen", "private.txt"), filepath.Join(dir, "state-alias.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	svc := New(dir, task.DefaultConfig())
	files, collection, err := svc.collectWorkspaceEditFiles(task.Spec{
		TaskID:        "TASK-001",
		Kind:          task.KindCoding,
		Objective:     "inspect docs",
		WorkspaceRoot: dir,
	}, task.VerificationReport{})
	if err != nil {
		t.Fatalf("collect workspace files: %v", err)
	}
	if containsWorkspaceFilePath(files, "outside.txt") || containsWorkspaceFilePath(files, "state-alias.txt") {
		t.Fatalf("snapshot should omit symlinks, got %+v", files)
	}
	if collection.OmittedFileCount < 2 || !collection.Truncated || collection.StopReason != "skipped symlink paths" {
		t.Fatalf("expected symlink omissions to be reported, got %+v", collection)
	}
}

func TestWorkerReconcileRejectsParentIntermediateSymlinkTarget(t *testing.T) {
	parent := t.TempDir()
	child := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(parent, "out")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	writeFile(t, filepath.Join(child, "out", "result.txt"), "child result\n")

	parentManifest, err := collectWorkerManifest(parent)
	if err != nil {
		t.Fatalf("collect parent manifest: %v", err)
	}
	childManifest, err := collectWorkerManifest(child)
	if err != nil {
		t.Fatalf("collect child manifest: %v", err)
	}
	decisions := buildWorkerReconcileDecisions("apply_on_accept", map[string]workerManifestEntry{}, parentManifest, childManifest)
	markWorkerReconcileUnsafeParentPaths(parent, decisions)
	for _, decision := range decisions {
		if decision.change.Path != "out/result.txt" {
			continue
		}
		if decision.apply || decision.change.Status != "conflict" {
			t.Fatalf("expected descendant under parent symlink to be conflict, got %+v", decision)
		}
		if _, err := os.Stat(filepath.Join(outside, "result.txt")); !os.IsNotExist(err) {
			t.Fatalf("worker reconcile should not write outside parent, stat err=%v", err)
		}
		return
	}
	t.Fatalf("expected child-only descendant reconcile decision, got %+v", decisions)
}

func TestUnsafeRepairCommandReplayIsRejected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# replay\n")

	svc := New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:             task.KindCoding,
		Title:            "replay guard",
		Objective:        "guard unsafe command replay",
		PermissionModeID: task.PermissionModeYolo,
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "docs reviewed"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	state, err := svc.Store.LoadState(spec.TaskID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	argv := []string{"bash", "-lc", "echo side-effect"}
	if err := svc.Store.AppendCommandRun(task.CommandRunRecord{
		SchemaVersion:    task.SchemaVersion,
		CommandRecordID:  "CMDREC-unsafe",
		CommandID:        "CMD-unsafe",
		TaskID:           spec.TaskID,
		TS:               task.Now(),
		Kind:             "repair_command",
		Status:           "completed",
		Summary:          "previous shell side effect",
		Argv:             argv,
		PermissionModeID: task.PermissionModeYolo,
		PolicyDecision:   "allow_yolo",
		ReplaySafety: &task.ReplaySafety{
			SideEffectClass: "workspace_repair_command",
			ReplayPolicy:    "manual_review_required",
			OpenWorld:       true,
		},
	}); err != nil {
		t.Fatalf("append command run: %v", err)
	}

	events, failure, err := svc.executeWorkspaceExecutionCommand(context.Background(), spec, state, provider.WorkspaceCommand{
		Phase:  "post",
		Argv:   argv,
		Reason: "repeat shell side effect",
	}, "post")
	if err != nil {
		t.Fatalf("execute command: %v", err)
	}
	if failure == nil || !strings.Contains(failure.Summary, "Rejected repair command replay") {
		t.Fatalf("expected unsafe replay failure, got failure=%+v events=%+v", failure, events)
	}
	records, err := svc.Store.ReadCommandRuns(spec.TaskID)
	if err != nil {
		t.Fatalf("read command runs: %v", err)
	}
	last := records[len(records)-1]
	if last.Status != "failed" || last.ReplaySafety == nil || last.ReplaySafety.ReplayPolicy != "manual_review_required" {
		t.Fatalf("expected failed replay record with safety metadata, got %+v", last)
	}
}

func TestWorkspaceExecutionCommandCapsOversizedOutput(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# output cap\n")

	svc := New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:             task.KindCoding,
		Title:            "output cap",
		Objective:        "cap command output",
		PermissionModeID: task.PermissionModeYolo,
		SuccessCriteria:  []task.SuccessCriterion{{ID: "SC-001", Statement: "docs reviewed"}},
		WorkspaceRoot:    dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	state, err := svc.Store.LoadState(spec.TaskID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	_, failure, err := svc.executeWorkspaceExecutionCommand(context.Background(), spec, state, provider.WorkspaceCommand{
		Phase:  "post",
		Argv:   []string{"sh", "-c", "yes x | head -c 1200000"},
		Reason: "emit too much output",
	}, "post")
	if err != nil {
		t.Fatalf("execute command: %v", err)
	}
	if failure == nil || !strings.Contains(failure.Summary, "stdout exceeded max bytes") {
		t.Fatalf("expected output cap failure, got %+v", failure)
	}
	records, err := svc.Store.ReadCommandRuns(spec.TaskID)
	if err != nil {
		t.Fatalf("read command runs: %v", err)
	}
	last := records[len(records)-1]
	if !last.StdoutTruncated || last.Status != "failed" {
		t.Fatalf("expected failed truncated record, got %+v", last)
	}
	if len(last.StdoutExcerpt) == 0 || len(last.StdoutExcerpt) > 3000 {
		t.Fatalf("expected bounded stdout excerpt, got len=%d", len(last.StdoutExcerpt))
	}
}

func TestCollectWorkspaceEditFilesCountsOmittedFilesAfterBudget(t *testing.T) {
	dir := t.TempDir()
	svc := New(dir, task.DefaultConfig())
	for i := 0; i < 130; i++ {
		writeFile(t, filepath.Join(dir, "pkg", fmt.Sprintf("file%03d.go", i)), "package pkg\n")
	}

	files, collection, err := svc.collectWorkspaceEditFiles(task.Spec{
		TaskID:        "TASK-001",
		Kind:          task.KindCoding,
		Objective:     "fix pkg behavior",
		WorkspaceRoot: dir,
	}, task.VerificationReport{})
	if err != nil {
		t.Fatalf("collect workspace files: %v", err)
	}
	if len(files) != 128 {
		t.Fatalf("expected file budget to cap included files at 128, got %d", len(files))
	}
	if collection.OmittedFileCount != 2 {
		t.Fatalf("expected 2 omitted files after hitting budget, got %+v", collection)
	}
	if collection.StopReason != "workspace snapshot file budget reached" {
		t.Fatalf("expected file budget stop reason, got %+v", collection)
	}
}

func TestCollectWorkspaceEditFilesPrioritizesFailureHintPaths(t *testing.T) {
	dir := t.TempDir()
	svc := New(dir, task.DefaultConfig())
	for i := 0; i < 140; i++ {
		writeFile(t, filepath.Join(dir, "aaa", fmt.Sprintf("file%03d.go", i)), "package aaa\n")
	}
	writeFile(t, filepath.Join(dir, "zzz", "target.go"), "package zzz\n\nfunc Broken() int { return 0 }\n")

	files, collection, err := svc.collectWorkspaceEditFiles(task.Spec{
		TaskID:        "TASK-001",
		Kind:          task.KindCoding,
		Objective:     "fix zzz/target.go",
		WorkspaceRoot: dir,
	}, task.VerificationReport{
		FailureSummary: "zzz/target.go:3: undefined: ReadyState",
	})
	if err != nil {
		t.Fatalf("collect workspace files: %v", err)
	}
	if !containsWorkspaceFilePath(files, "zzz/target.go") {
		t.Fatalf("expected hinted target file to be prioritized into the snapshot, got %+v", files)
	}
	if len(files) == 0 || files[0].Path != "zzz/target.go" {
		t.Fatalf("expected hinted target file to be surfaced first in the returned snapshot, got first=%+v", files)
	}
	if got := countWorkspaceFilesWithPrefix(files, "aaa/"); got > 24 {
		t.Fatalf("expected focused snapshot to cap unrelated noise files, got %d files: %+v", got, files)
	}
	if collection.OmittedFileCount == 0 {
		t.Fatalf("expected omitted files once the budget is exceeded, got %+v", collection)
	}
}

func TestHeuristicObservationCommandsForOmittedLargeGoFile(t *testing.T) {
	commands := heuristicObservationCommands(
		task.Spec{
			TaskID:        "TASK-001",
			Kind:          task.KindCoding,
			Objective:     "Fix Add inside a large source file without touching tests.",
			WorkspaceRoot: t.TempDir(),
		},
		task.VerificationReport{
			FailureSummary: "--- FAIL: TestAdd\nexpected 5, got -1",
		},
		provider.WorkspaceCollection{
			Truncated:    true,
			StopReason:   "skipped large or non-text files",
			OmittedPaths: []string{"engine.go"},
		},
		2,
	)
	if len(commands) != 2 {
		t.Fatalf("expected two heuristic observation commands, got %+v", commands)
	}
	if got := strings.Join(commands[0].Argv, " "); got != "rg -n Add engine.go" {
		t.Fatalf("unexpected first heuristic command: %s", got)
	}
	if got := strings.Join(commands[1].Argv, " "); got != "tail -n 120 engine.go" {
		t.Fatalf("unexpected second heuristic command: %s", got)
	}
}

func TestObservationSearchTermsSkipsGenericUpdateVerb(t *testing.T) {
	terms := observationSearchTerms("Update generated_large.go so TestLargeToken returns READY")
	for _, term := range terms {
		if term == "Update" {
			t.Fatalf("expected generic Update verb to be ignored, got %+v", terms)
		}
		if term == "READY" {
			t.Fatalf("expected all-caps goal token to be ignored, got %+v", terms)
		}
		if term == "TestLargeToken" {
			t.Fatalf("expected test name to be ignored, got %+v", terms)
		}
	}
}

func TestCriteriaFromEvidenceRequiresExplicitPathEvidence(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# demo\n")

	svc := New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     "criteria evidence",
		Objective: "implement Add and update README.md",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "go test ./... passes"},
			{ID: "SC-002", Statement: "README.md mentions `Add`"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	report := task.VerificationReport{
		SchemaVersion: task.SchemaVersion,
		TaskID:        spec.TaskID,
		Status:        "passed",
	}
	criteria := svc.criteriaFromEvidence(spec, report)
	if got := criterionStatusForID(criteria, "SC-001"); got.Status != "met" {
		t.Fatalf("expected verifier-backed criterion to be met, got %+v", got)
	}
	if got := criterionStatusForID(criteria, "SC-002"); got.Status != "open" {
		t.Fatalf("expected README criterion to stay open without evidence, got %+v", got)
	}

	writeFile(t, filepath.Join(dir, "README.md"), "# demo\n\nUse Add to sum two values.\n")
	criteria = svc.criteriaFromEvidence(spec, report)
	got := criterionStatusForID(criteria, "SC-002")
	if got.Status != "met" {
		t.Fatalf("expected README criterion to become met from workspace evidence, got %+v", got)
	}
	if !containsString(got.EvidenceRefs, "workspace:README.md") {
		t.Fatalf("expected README workspace ref in evidence, got %+v", got.EvidenceRefs)
	}
}

func TestCriteriaFromEvidenceRequiresMatchingVerifierCommand(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "build.sh"), "#!/usr/bin/env bash\nset -euo pipefail\n")
	if err := os.Chmod(filepath.Join(dir, "build.sh"), 0o755); err != nil {
		t.Fatalf("chmod build.sh: %v", err)
	}

	svc := New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     "verifier command evidence",
		Objective: "match explicit verifier command",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "`./build.sh build` passes"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	report := task.VerificationReport{
		SchemaVersion: task.SchemaVersion,
		TaskID:        spec.TaskID,
		Status:        "passed",
		Checks: []task.VerificationCheck{
			{
				Name:    "go_test",
				Status:  "passed",
				Summary: "go test ./... passed",
				Command: []string{"go", "test", "./..."},
			},
		},
	}
	criteria := svc.criteriaFromEvidence(spec, report)
	if got := criterionStatusForID(criteria, "SC-001"); got.Status != "open" {
		t.Fatalf("expected explicit build verifier criterion to stay open on mismatched verification command, got %+v", got)
	}

	report.Checks = append(report.Checks, task.VerificationCheck{
		Name:    "verifier_command_02",
		Status:  "passed",
		Summary: "./build.sh build passed",
		Command: []string{"./build.sh", "build"},
	})
	criteria = svc.criteriaFromEvidence(spec, report)
	got := criterionStatusForID(criteria, "SC-001")
	if got.Status != "met" {
		t.Fatalf("expected explicit build verifier criterion to become met, got %+v", got)
	}
	if !containsString(got.EvidenceRefs, "verification/latest.json") {
		t.Fatalf("expected verification ref in criterion evidence, got %+v", got.EvidenceRefs)
	}
}

func TestCriteriaFromEvidenceRequiresSemanticConfigEvidenceWithoutPathHint(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "config.sample.json"), "{\n  \"port\": 8080\n}\n")

	svc := New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     "semantic config evidence",
		Objective: "fix timeout defaults",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "sample config mentions `timeout_seconds`"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	report := task.VerificationReport{
		SchemaVersion: task.SchemaVersion,
		TaskID:        spec.TaskID,
		Status:        "passed",
	}
	criteria := svc.criteriaFromEvidence(spec, report)
	if got := criterionStatusForID(criteria, "SC-001"); got.Status != "open" {
		t.Fatalf("expected semantic config criterion to stay open without workspace evidence, got %+v", got)
	}

	writeFile(t, filepath.Join(dir, "config.sample.json"), "{\n  \"timeout_seconds\": 15\n}\n")
	criteria = svc.criteriaFromEvidence(spec, report)
	got := criterionStatusForID(criteria, "SC-001")
	if got.Status != "met" {
		t.Fatalf("expected semantic config criterion to become met, got %+v", got)
	}
	if !containsString(got.EvidenceRefs, "workspace:config.sample.json") {
		t.Fatalf("expected config workspace ref in evidence, got %+v", got.EvidenceRefs)
	}
}

func TestCriteriaFromEvidenceSupportsGlobPathCriteria(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "docs", "guide.md"), "# Guide\n\nPending.\n")

	svc := New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     "glob criterion",
		Objective: "update docs guide",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "docs/*.md mentions `READY`"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	report := task.VerificationReport{
		SchemaVersion: task.SchemaVersion,
		TaskID:        spec.TaskID,
		Status:        "passed",
	}
	criteria := svc.criteriaFromEvidence(spec, report)
	if got := criterionStatusForID(criteria, "SC-001"); got.Status != "open" {
		t.Fatalf("expected glob criterion to stay open without matching content, got %+v", got)
	}

	writeFile(t, filepath.Join(dir, "docs", "guide.md"), "# Guide\n\nSystem is READY.\n")
	criteria = svc.criteriaFromEvidence(spec, report)
	got := criterionStatusForID(criteria, "SC-001")
	if got.Status != "met" {
		t.Fatalf("expected glob criterion to become met, got %+v", got)
	}
	if !containsString(got.EvidenceRefs, "workspace:docs/guide.md") {
		t.Fatalf("expected guide workspace ref in evidence, got %+v", got.EvidenceRefs)
	}
}

func TestSyncTaskNarrativeRefreshesCriteriaScopedPlan(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# demo\n")

	svc := New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     "plan refresh",
		Objective: "track multi-step progress",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "`go test ./...` passes"},
			{ID: "SC-002", Statement: "README.md mentions `Add`"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	plan, err := svc.Store.LoadPlan(spec.TaskID)
	if err != nil {
		t.Fatalf("load initial plan: %v", err)
	}
	if len(plan.Steps) != 4 || plan.Steps[0].Status != "in_progress" {
		t.Fatalf("expected criteria-scoped initial plan, got %+v", plan.Steps)
	}

	baseline := task.Baseline{
		SchemaVersion:      task.SchemaVersion,
		TaskID:             spec.TaskID,
		CapturedAt:         task.Now(),
		WorkspaceRoot:      dir,
		RepoTruthRefs:      []string{"workspace:README.md"},
		AvailableVerifiers: []string{"baseline", "go_test"},
	}
	if err := svc.Store.SaveBaseline(baseline); err != nil {
		t.Fatalf("save baseline: %v", err)
	}
	if err := svc.Store.SaveCriteria(task.CriteriaSnapshot{
		SchemaVersion: task.SchemaVersion,
		TaskID:        spec.TaskID,
		UpdatedAt:     task.Now(),
		Criteria: []task.CriterionStatus{
			{CriterionID: "SC-001", Status: "met", EvidenceRefs: []string{"verification/latest.json"}},
			{CriterionID: "SC-002", Status: "open"},
		},
	}); err != nil {
		t.Fatalf("save criteria: %v", err)
	}
	state, err := svc.Store.LoadState(spec.TaskID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	state.Phase = task.PhaseExecute
	state.State = task.StateBlocked
	state.StatusReasonCode = "blocked_review"
	state.UpdatedAt = task.Now()
	if err := svc.syncTaskNarrative(spec, state, "README criterion still open."); err != nil {
		t.Fatalf("sync narrative: %v", err)
	}

	plan, err = svc.Store.LoadPlan(spec.TaskID)
	if err != nil {
		t.Fatalf("reload plan: %v", err)
	}
	if got := plan.Steps[0].Status; got != "completed" {
		t.Fatalf("expected baseline step completed, got %+v", plan.Steps[0])
	}
	if got := plan.Steps[1].Status; got != "completed" || plan.Steps[1].Verifier[0] != "go test ./..." {
		t.Fatalf("expected verifier criterion step completed with command hint, got %+v", plan.Steps[1])
	}
	if got := plan.Steps[2].Status; got != "in_progress" {
		t.Fatalf("expected open README criterion to become current step, got %+v", plan.Steps[2])
	}
	if got := plan.Steps[3].Status; got != "pending" {
		t.Fatalf("expected final gate step pending, got %+v", plan.Steps[3])
	}

	state, err = svc.Store.LoadState(spec.TaskID)
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}
	if state.CurrentStepID != "STEP-003" {
		t.Fatalf("expected current step to move to open criterion, got %+v", state)
	}

	if err := svc.Store.SaveCriteria(task.CriteriaSnapshot{
		SchemaVersion: task.SchemaVersion,
		TaskID:        spec.TaskID,
		UpdatedAt:     task.Now(),
		Criteria: []task.CriterionStatus{
			{CriterionID: "SC-001", Status: "met", EvidenceRefs: []string{"verification/latest.json"}},
			{CriterionID: "SC-002", Status: "met", EvidenceRefs: []string{"workspace:README.md", "verification/latest.json"}},
		},
	}); err != nil {
		t.Fatalf("save completed criteria: %v", err)
	}
	if err := svc.Store.SaveCompletion(task.CompletionReport{
		SchemaVersion: task.SchemaVersion,
		TaskID:        spec.TaskID,
		CompletionID:  "CMP-001",
		Status:        "accepted",
		Summary:       "Done gate passed.",
		EvaluatedAt:   task.Now(),
	}); err != nil {
		t.Fatalf("save completion: %v", err)
	}
	state.State = task.StateDone
	state.StatusReasonCode = ""
	state.StatusDetailRef = ""
	state.UpdatedAt = task.Now()
	if err := svc.syncTaskNarrative(spec, state, "Task is done."); err != nil {
		t.Fatalf("sync completed narrative: %v", err)
	}

	plan, err = svc.Store.LoadPlan(spec.TaskID)
	if err != nil {
		t.Fatalf("reload completed plan: %v", err)
	}
	if got := plan.Steps[len(plan.Steps)-1].Status; got != "completed" {
		t.Fatalf("expected final gate step completed, got %+v", plan.Steps[len(plan.Steps)-1])
	}
	state, err = svc.Store.LoadState(spec.TaskID)
	if err != nil {
		t.Fatalf("reload completed state: %v", err)
	}
	if state.CurrentStepID != "STEP-004" {
		t.Fatalf("expected current step to move to final gate on completion, got %+v", state)
	}
}

func TestNarrativeAndCheckpointExposeRepoBearings(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# bearings\n")
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/repo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package repo\n\nfunc Add(a, b int) int { return a + b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package repo\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")
	writeFile(t, filepath.Join(dir, "build.sh"), "#!/usr/bin/env bash\nset -euo pipefail\ngo test ./...\n")
	writeFile(t, filepath.Join(dir, "init.sh"), "#!/usr/bin/env bash\nset -euo pipefail\necho bootstrap ready\n")
	if err := os.Chmod(filepath.Join(dir, "build.sh"), 0o755); err != nil {
		t.Fatalf("chmod build.sh: %v", err)
	}
	if err := os.Chmod(filepath.Join(dir, "init.sh"), 0o755); err != nil {
		t.Fatalf("chmod init.sh: %v", err)
	}
	initGitRepo(t, dir)
	writeFile(t, filepath.Join(dir, "README.md"), "# bearings\n\ndirty\n")

	svc := New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     "repo bearings",
		Objective: "capture durable repo bearings",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "`./build.sh test` passes"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, _, err := svc.Run(context.Background(), spec.TaskID); err != nil {
		t.Fatalf("run task: %v", err)
	}

	progressBytes, err := os.ReadFile(filepath.Join(svc.Store.TaskRoot(spec.TaskID), "progress.md"))
	if err != nil {
		t.Fatalf("read progress: %v", err)
	}
	progress := string(progressBytes)
	if !strings.Contains(progress, "## Repo Bearings") || !strings.Contains(progress, "./init.sh") || !strings.Contains(progress, "dirty working tree") {
		t.Fatalf("expected repo bearings in progress, got %s", progress)
	}
	compactionBytes, err := os.ReadFile(filepath.Join(svc.Store.TaskRoot(spec.TaskID), "context", "summary.md"))
	if err != nil {
		t.Fatalf("read context summary: %v", err)
	}
	compaction := string(compactionBytes)
	if !strings.Contains(compaction, "## Repo Bearings") || !strings.Contains(compaction, "Checkpoint Git") {
		t.Fatalf("expected repo bearings in context summary, got %s", compaction)
	}
	checkpoint, err := svc.Store.LoadLatestCheckpoint(spec.TaskID)
	if err != nil {
		t.Fatalf("load latest checkpoint: %v", err)
	}
	if checkpoint.WorkspaceSnapshot == nil || checkpoint.WorkspaceSnapshot.Git == nil || !checkpoint.WorkspaceSnapshot.Git.IsRepository {
		t.Fatalf("expected checkpoint workspace snapshot git summary, got %+v", checkpoint)
	}
	status, err := svc.Status(context.Background(), spec.TaskID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.LastCheckpointRef == "" || len(status.RestoreClues) == 0 || status.RestoreClues[0].Git == nil {
		t.Fatalf("expected status restore clues from checkpoint, got %+v", status)
	}
	handoff, err := svc.Store.LoadHandoff(spec.TaskID)
	if err != nil {
		t.Fatalf("load handoff: %v", err)
	}
	if !strings.Contains(string(handoff), "Restore Clues") || !strings.Contains(string(handoff), status.LastCheckpointRef) {
		t.Fatalf("expected handoff restore clues, got %s", handoff)
	}
}

func TestContinuitySnapshotCapturesChecklistAndHistory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# continuity bearings\n")
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/repo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package repo\n\nfunc Add(a, b int) int { return a + b }\n")
	writeFile(t, filepath.Join(dir, "calc_test.go"), "package repo\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"expected 5, got %d\", got)\n\t}\n}\n")
	writeFile(t, filepath.Join(dir, "build.sh"), "#!/usr/bin/env bash\nset -euo pipefail\ngo test ./...\n")
	writeFile(t, filepath.Join(dir, "init.sh"), "#!/usr/bin/env bash\nset -euo pipefail\necho bootstrap ready\n")
	if err := os.Chmod(filepath.Join(dir, "build.sh"), 0o755); err != nil {
		t.Fatalf("chmod build.sh: %v", err)
	}
	if err := os.Chmod(filepath.Join(dir, "init.sh"), 0o755); err != nil {
		t.Fatalf("chmod init.sh: %v", err)
	}
	initGitRepo(t, dir)
	writeFile(t, filepath.Join(dir, "README.md"), "# continuity bearings\n\ndirty\n")

	svc := New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     "continuity",
		Objective: "preserve a durable restart ledger with repo bearings",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "`./build.sh test` passes"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, _, err := svc.Run(context.Background(), spec.TaskID); err != nil {
		t.Fatalf("run task: %v", err)
	}

	continuity, err := svc.Store.LoadContinuity(spec.TaskID)
	if err != nil {
		t.Fatalf("load continuity: %v", err)
	}
	if continuity.SnapshotID == "" || continuity.CurrentFocus.CurrentStepID == "" {
		t.Fatalf("expected continuity snapshot ids and focus, got %+v", continuity)
	}
	if len(continuity.StartupChecklist) < 4 {
		t.Fatalf("expected startup checklist entries for refs and repo commands, got %+v", continuity.StartupChecklist)
	}
	if !containsContinuityRef(continuity.StartupChecklist, "sprint/latest.json") {
		t.Fatalf("expected sprint contract checklist ref, got %+v", continuity.StartupChecklist)
	}
	if !containsContinuityRef(continuity.StartupChecklist, "criteria/latest.json") {
		t.Fatalf("expected acceptance-ledger checklist ref, got %+v", continuity.StartupChecklist)
	}
	if !containsContinuityCommand(continuity.StartupChecklist, []string{"git", "status", "--short"}) {
		t.Fatalf("expected git status checklist command, got %+v", continuity.StartupChecklist)
	}
	if !containsContinuityCommand(continuity.StartupChecklist, []string{"bash", "./init.sh"}) {
		t.Fatalf("expected repo setup checklist command, got %+v", continuity.StartupChecklist)
	}
	if !containsContinuityCommand(continuity.StartupChecklist, []string{"./build.sh", "test"}) {
		t.Fatalf("expected repo verifier checklist command, got %+v", continuity.StartupChecklist)
	}
	if !containsContinuityPath(continuity.CurrentFocus.WorkingSetPaths, "README.md") {
		t.Fatalf("expected dirty README.md in continuity working set, got %+v", continuity.CurrentFocus)
	}
	if containsContinuityPath(continuity.CurrentFocus.WorkingSetPaths, ".ngen/") || containsContinuityPath(continuity.CurrentFocus.WorkingSetPaths, "./") {
		t.Fatalf("expected continuity working set to omit runtime/noise paths, got %+v", continuity.CurrentFocus)
	}
	history, err := svc.Store.ReadContinuity(spec.TaskID)
	if err != nil {
		t.Fatalf("read continuity history: %v", err)
	}
	if len(history) < 2 {
		t.Fatalf("expected append-only continuity history across create/run, got %+v", history)
	}
}

func TestSprintSnapshotCapturesCurrentScopeAndHistory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# alpha\n")

	svc := New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "sprint contract",
		Objective: "track the current sprint boundary",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "README.md mentions `alpha`"},
			{ID: "SC-002", Statement: "docs/guide.md mentions `beta`"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	initial, err := svc.Store.LoadSprint(spec.TaskID)
	if err != nil {
		t.Fatalf("load initial sprint: %v", err)
	}
	if initial.PrimaryCriterionID != "SC-001" || len(initial.CompletionSignals) == 0 {
		t.Fatalf("expected initial sprint to focus SC-001 with completion signals, got %+v", initial)
	}
	if len(initial.DeferredCriterionIDs) != 1 || initial.DeferredCriterionIDs[0] != "SC-002" {
		t.Fatalf("expected deferred criterion list in sprint snapshot, got %+v", initial)
	}

	report := task.VerificationReport{
		SchemaVersion: task.SchemaVersion,
		TaskID:        spec.TaskID,
		ReportID:      task.NewID("VER"),
		Status:        "passed",
		Profile:       string(spec.Kind),
		RanAt:         task.Now(),
		Checks: []task.VerificationCheck{
			{Name: "docs", Status: "passed", Summary: "README is present."},
		},
	}
	if err := svc.Store.SaveVerification(report); err != nil {
		t.Fatalf("save verification: %v", err)
	}
	updated := svc.criteriaFromEvidence(spec, report)
	if err := svc.Store.SaveCriteria(updated); err != nil {
		t.Fatalf("save updated criteria: %v", err)
	}
	if err := svc.Store.SaveSprint(svc.buildSprintSnapshot(spec, task.State{
		SchemaVersion: task.SchemaVersion,
		TaskID:        spec.TaskID,
		Phase:         task.PhaseReview,
		State:         task.StateBlocked,
		CurrentStepID: "STEP-002",
	})); err != nil {
		t.Fatalf("save updated sprint: %v", err)
	}

	latest, err := svc.Store.LoadSprint(spec.TaskID)
	if err != nil {
		t.Fatalf("load updated sprint: %v", err)
	}
	if latest.PrimaryCriterionID != "SC-002" {
		t.Fatalf("expected sprint focus to advance to SC-002, got %+v", latest)
	}
	if len(latest.DeferredCriterionIDs) != 0 {
		t.Fatalf("expected no deferred criteria after focus advance, got %+v", latest)
	}

	history, err := svc.Store.ReadSprint(spec.TaskID)
	if err != nil {
		t.Fatalf("read sprint history: %v", err)
	}
	if len(history) < 2 {
		t.Fatalf("expected append-only sprint history across create/update, got %+v", history)
	}
}

func TestCriteriaSnapshotCapturesAcceptanceLedgerAndHistory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# alpha\n")

	svc := New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "criteria ledger",
		Objective: "track one acceptance item at a time",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "README.md mentions `alpha`"},
			{ID: "SC-002", Statement: "docs/guide.md mentions `beta`"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	initial, err := svc.Store.LoadCriteria(spec.TaskID)
	if err != nil {
		t.Fatalf("load initial criteria: %v", err)
	}
	if initial.CurrentCriterionID != "SC-001" || !initial.Criteria[0].Selected || initial.Criteria[0].Passes {
		t.Fatalf("expected initial acceptance ledger to focus SC-001, got %+v", initial)
	}

	report := task.VerificationReport{
		SchemaVersion: task.SchemaVersion,
		TaskID:        spec.TaskID,
		ReportID:      task.NewID("VER"),
		Status:        "passed",
		Profile:       string(spec.Kind),
		RanAt:         task.Now(),
		Checks: []task.VerificationCheck{
			{Name: "docs", Status: "passed", Summary: "README is present."},
		},
	}
	updated := svc.criteriaFromEvidence(spec, report)
	if err := svc.Store.SaveCriteria(updated); err != nil {
		t.Fatalf("save updated criteria: %v", err)
	}

	latest, err := svc.Store.LoadCriteria(spec.TaskID)
	if err != nil {
		t.Fatalf("load updated criteria: %v", err)
	}
	if latest.CurrentCriterionID != "SC-002" || latest.CurrentCriterionStatement != "docs/guide.md mentions `beta`" {
		t.Fatalf("expected focus to advance to SC-002 after SC-001 passed, got %+v", latest)
	}
	if latest.MetCount != 1 || latest.OpenCount != 1 {
		t.Fatalf("expected acceptance counts to update, got %+v", latest)
	}
	first := criterionStatusForID(latest, "SC-001")
	second := criterionStatusForID(latest, "SC-002")
	if !first.Passes || first.Statement != "README.md mentions `alpha`" || first.Ordinal != 1 {
		t.Fatalf("expected first criterion to be marked passing with statement metadata, got %+v", first)
	}
	if second.Passes || !second.Selected || second.Ordinal != 2 {
		t.Fatalf("expected second criterion to remain selected and open, got %+v", second)
	}

	history, err := svc.Store.ReadCriteria(spec.TaskID)
	if err != nil {
		t.Fatalf("read criteria history: %v", err)
	}
	if len(history) < 2 {
		t.Fatalf("expected append-only criteria history across create/update, got %+v", history)
	}
}

func TestProgressMarkdownSurfacesAcceptanceLedgerFocus(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "# alpha\n\nalpha\n")

	svc := New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindGeneral,
		PresetID:  task.PresetDocsLite,
		Title:     "progress acceptance",
		Objective: "render acceptance focus in progress",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "README.md mentions `alpha`"},
			{ID: "SC-002", Statement: "reviewer child exists"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	snapshot, _, err := svc.Run(context.Background(), spec.TaskID)
	if err != nil {
		t.Fatalf("run task: %v", err)
	}
	if snapshot.State != task.StateBlocked || snapshot.StatusReasonCode != "blocked_review" {
		t.Fatalf("expected blocked_review snapshot, got %+v", snapshot)
	}

	progressBytes, err := svc.Store.LoadProgress(spec.TaskID)
	if err != nil {
		t.Fatalf("load progress: %v", err)
	}
	progress := string(progressBytes)
	if !strings.Contains(progress, "## Current Sprint") {
		t.Fatalf("expected current sprint section in progress, got %s", progress)
	}
	if !strings.Contains(progress, "Primary Criterion: SC-002 reviewer child exists") {
		t.Fatalf("expected sprint primary criterion in progress, got %s", progress)
	}
	if !strings.Contains(progress, "## Criteria Snapshot") {
		t.Fatalf("expected criteria snapshot section in progress, got %s", progress)
	}
	if !strings.Contains(progress, "Current Focus: SC-002 reviewer child exists") {
		t.Fatalf("expected current acceptance focus in progress, got %s", progress)
	}
	if !strings.Contains(progress, "Passing: 1/2") {
		t.Fatalf("expected passing count in progress, got %s", progress)
	}

	handoffBytes, err := svc.Store.LoadHandoff(spec.TaskID)
	if err != nil {
		t.Fatalf("load handoff: %v", err)
	}
	handoff := string(handoffBytes)
	if !strings.Contains(handoff, "## Current Sprint") || !strings.Contains(handoff, "primary criterion: SC-002 reviewer child exists") {
		t.Fatalf("expected handoff to surface sprint contract, got %s", handoff)
	}
}

func containsContinuityCommand(items []task.ContinuityChecklistItem, want []string) bool {
	for _, item := range items {
		if strings.Join(item.Command, "\x00") == strings.Join(want, "\x00") {
			return true
		}
	}
	return false
}

func containsContinuityRef(items []task.ContinuityChecklistItem, want string) bool {
	for _, item := range items {
		if item.Ref == want {
			return true
		}
	}
	return false
}

func containsContinuityPath(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestCollectWorkspaceEditFilesPrioritizesCriterionHintPaths(t *testing.T) {
	dir := t.TempDir()
	svc := New(dir, task.DefaultConfig())
	for i := 0; i < 140; i++ {
		writeFile(t, filepath.Join(dir, "aaa", fmt.Sprintf("file%03d.go", i)), "package aaa\n")
	}
	writeFile(t, filepath.Join(dir, "README.md"), "# demo\nUse Add here.\n")

	files, _, err := svc.collectWorkspaceEditFiles(task.Spec{
		TaskID:    "TASK-001",
		Kind:      task.KindCoding,
		Objective: "ship Add",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "README.md mentions `Add`"},
		},
		WorkspaceRoot: dir,
	}, task.VerificationReport{})
	if err != nil {
		t.Fatalf("collect workspace files: %v", err)
	}
	if !containsWorkspaceFilePath(files, "README.md") {
		t.Fatalf("expected README.md to be prioritized into the snapshot, got %+v", files)
	}
}

func TestCollectWorkspaceEditFilesPrioritizesSemanticConfigCriteria(t *testing.T) {
	dir := t.TempDir()
	svc := New(dir, task.DefaultConfig())
	for i := 0; i < 140; i++ {
		writeFile(t, filepath.Join(dir, "aaa", fmt.Sprintf("file%03d.go", i)), "package aaa\n")
	}
	writeFile(t, filepath.Join(dir, "config.sample.json"), "{\n  \"timeout_seconds\": 15\n}\n")

	files, _, err := svc.collectWorkspaceEditFiles(task.Spec{
		TaskID:    "TASK-001",
		Kind:      task.KindCoding,
		Objective: "ship timeout defaults",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "sample config mentions `timeout_seconds`"},
		},
		WorkspaceRoot: dir,
	}, task.VerificationReport{})
	if err != nil {
		t.Fatalf("collect workspace files: %v", err)
	}
	if !containsWorkspaceFilePath(files, "config.sample.json") {
		t.Fatalf("expected semantic config criterion to prioritize config.sample.json, got %+v", files)
	}
}

func TestApplyWorkspacePatchUpdatesExistingFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.24.0\n")
	writeFile(t, filepath.Join(dir, "calc.go"), "package main\n\nfunc Add(a, b int) int { return a - b }\n")

	svc := New(dir, task.DefaultConfig())
	spec, err := svc.Create(context.Background(), task.TaskFile{
		Kind:      task.KindCoding,
		Title:     "patch",
		Objective: "fix Add",
		SuccessCriteria: []task.SuccessCriterion{
			{ID: "SC-001", Statement: "Add sums values"},
		},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	record, err := svc.applyWorkspaceEditPlan(spec, "EDIT-001", provider.WorkspaceEditPlan{
		Summary: "Patch Add",
		Patch: strings.Join([]string{
			"*** Begin Patch",
			"*** Update File: calc.go",
			"@@",
			"-func Add(a, b int) int { return a - b }",
			"+func Add(a, b int) int { return a + b }",
			"*** End Patch",
		}, "\n"),
	})
	if err != nil {
		t.Fatalf("apply patch plan: %v", err)
	}
	if len(record.FileChanges) != 1 || record.FileChanges[0].Path != "calc.go" {
		t.Fatalf("expected one calc.go change, got %+v", record.FileChanges)
	}
	got, err := os.ReadFile(filepath.Join(dir, "calc.go"))
	if err != nil {
		t.Fatalf("read calc.go: %v", err)
	}
	if !strings.Contains(string(got), "return a + b") {
		t.Fatalf("expected patched source, got %q", string(got))
	}
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

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file %s: %v", path, err)
	}
	return string(data)
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "ngen@example.com")
	runGit(t, dir, "config", "user.name", "NGEN Test")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(output))
	}
}

func containsWorkspaceFilePath(files []provider.WorkspaceFile, want string) bool {
	for _, file := range files {
		if file.Path == want {
			return true
		}
	}
	return false
}

func countWorkspaceFilesWithPrefix(files []provider.WorkspaceFile, prefix string) int {
	count := 0
	for _, file := range files {
		if strings.HasPrefix(file.Path, prefix) {
			count++
		}
	}
	return count
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func projectHasBoundTask(steps []task.ProjectStep, wantStepID, wantTaskID string) bool {
	for _, step := range steps {
		if step.ID == wantStepID && step.TaskID == wantTaskID {
			return true
		}
	}
	return false
}

func boolPtr(v bool) *bool {
	return &v
}
