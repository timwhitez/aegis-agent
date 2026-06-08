package task

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeTaskFileUsesConfiguredPermissionDefault(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Permission.DefaultMode = PermissionModeYolo

	normalized := NormalizeTaskFile(TaskFile{
		Kind:      KindGeneral,
		PresetID:  PresetDocsLite,
		Objective: "review docs",
		SuccessCriteria: []SuccessCriterion{
			{ID: "SC-001", Statement: "docs reviewed"},
		},
	}, "/tmp/workspace", cfg)

	if normalized.PermissionModeID != PermissionModeYolo {
		t.Fatalf("expected permission mode %s, got %s", PermissionModeYolo, normalized.PermissionModeID)
	}
	if normalized.SubagentPolicy == nil || normalized.SubagentPolicy.PermissionModeID != PermissionModeYolo {
		t.Fatalf("expected normalized subagent policy to inherit permission mode %s, got %+v", PermissionModeYolo, normalized.SubagentPolicy)
	}
}

func TestProviderThinkingLevelConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ngen.json"), `{
  "provider": {
    "mode": "openai-response",
    "model": "gpt-5.5",
    "thinking_level": "xhigh"
  }
}`)
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Provider.ThinkingLevel != "xhigh" {
		t.Fatalf("expected thinking level to round trip, got %q", cfg.Provider.ThinkingLevel)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if !strings.Contains(string(data), `"thinking_level":"xhigh"`) {
		t.Fatalf("expected thinking_level in JSON, got %s", string(data))
	}
}

func TestNewInitialStateUsesSpecPermissionMode(t *testing.T) {
	spec := Spec{
		TaskID:           "TASK-001",
		PermissionModeID: PermissionModeYolo,
	}

	state := NewInitialState(spec)

	if state.PermissionModeID != PermissionModeYolo {
		t.Fatalf("expected initial state permission mode %s, got %s", PermissionModeYolo, state.PermissionModeID)
	}
}

func TestNewBootstrapPlanSeedsCriteriaScopedSteps(t *testing.T) {
	spec := Spec{
		TaskID: "TASK-001",
		SuccessCriteria: []SuccessCriterion{
			{ID: "SC-001", Statement: "go test ./... passes"},
			{ID: "SC-002", Statement: "README.md mentions `Add`"},
		},
	}

	plan := NewBootstrapPlan(spec)

	if len(plan.Steps) != 4 {
		t.Fatalf("expected baseline + 2 criteria + final review step, got %+v", plan.Steps)
	}
	if plan.Steps[0].ID != "STEP-001" || plan.Steps[0].Status != "in_progress" {
		t.Fatalf("expected baseline step to start in progress, got %+v", plan.Steps[0])
	}
	if plan.Steps[1].ID != "STEP-002" || !strings.Contains(plan.Steps[1].Title, "SC-001") {
		t.Fatalf("expected first criterion step to mention SC-001, got %+v", plan.Steps[1])
	}
	if plan.Steps[2].ID != "STEP-003" || !strings.Contains(plan.Steps[2].Title, "README.md mentions `Add`") {
		t.Fatalf("expected second criterion step to preserve statement text, got %+v", plan.Steps[2])
	}
	if last := plan.Steps[len(plan.Steps)-1]; last.ID != "STEP-004" || !strings.Contains(last.Title, "Review evidence") {
		t.Fatalf("expected final review step, got %+v", last)
	}
	if plan.Steps[0].Kind != StepKindBaseline || plan.Steps[1].Kind != StepKindCriterion || plan.Steps[3].Kind != StepKindReviewGate {
		t.Fatalf("expected system step kinds to be populated, got %+v", plan.Steps)
	}
}

func TestNewInitialCriteriaSeedsAcceptanceLedgerFields(t *testing.T) {
	spec := Spec{
		TaskID: "TASK-001",
		SuccessCriteria: []SuccessCriterion{
			{ID: "SC-001", Statement: "README.md mentions `alpha`"},
			{ID: "SC-002", Statement: "docs/guide.md mentions `beta`"},
		},
	}

	snapshot := NewInitialCriteria(spec)

	if snapshot.SnapshotID == "" || snapshot.TaskID != spec.TaskID {
		t.Fatalf("expected snapshot identity fields, got %+v", snapshot)
	}
	if snapshot.CurrentCriterionID != "SC-001" || snapshot.CurrentCriterionStatement != "README.md mentions `alpha`" {
		t.Fatalf("expected first criterion to become current focus, got %+v", snapshot)
	}
	if snapshot.MetCount != 0 || snapshot.OpenCount != 2 {
		t.Fatalf("expected initial counts to show all criteria open, got %+v", snapshot)
	}
	if !strings.Contains(snapshot.Summary, "2 criteria are still failing") {
		t.Fatalf("expected initial acceptance-ledger summary, got %q", snapshot.Summary)
	}
	if len(snapshot.Criteria) != 2 {
		t.Fatalf("expected one status per criterion, got %+v", snapshot.Criteria)
	}
	if snapshot.Criteria[0].Ordinal != 1 || snapshot.Criteria[1].Ordinal != 2 {
		t.Fatalf("expected ordinal positions, got %+v", snapshot.Criteria)
	}
	if !snapshot.Criteria[0].Selected || snapshot.Criteria[1].Selected {
		t.Fatalf("expected only the first criterion selected initially, got %+v", snapshot.Criteria)
	}
	if snapshot.Criteria[0].Passes || snapshot.Criteria[1].Passes {
		t.Fatalf("expected initial criteria to be failing, got %+v", snapshot.Criteria)
	}
}

func TestNormalizeExecutionPlanUpdate(t *testing.T) {
	spec := Spec{
		TaskID: "TASK-001",
		SuccessCriteria: []SuccessCriterion{
			{ID: "SC-001", Statement: "docs reviewed"},
			{ID: "SC-002", Statement: "handoff captured"},
		},
	}

	update, err := NormalizeExecutionPlanUpdate(spec, PlanUpdate{
		Explanation: "  keep the checklist tight  ",
		Steps: []ExecutionPlanStep{
			{ID: " epic.repo_truth ", Title: " Inspect repo truth ", Status: "IN_PROGRESS", Priority: "HIGH", Covers: []string{"SC-001", "SC-001"}, Notes: "  start with README  "},
			{ID: "handoff.close", ParentStepID: "epic.repo_truth", DependsOn: []string{"epic.repo_truth", "epic.repo_truth"}, Priority: "", Title: "Refresh handoff", Covers: []string{"SC-002"}},
		},
	})
	if err != nil {
		t.Fatalf("normalize execution plan update: %v", err)
	}
	if update.Explanation != "keep the checklist tight" {
		t.Fatalf("expected trimmed explanation, got %+v", update)
	}
	if update.Steps[0].Status != StepStatusInProgress || update.Steps[1].Status != StepStatusPending {
		t.Fatalf("expected normalized step statuses, got %+v", update.Steps)
	}
	if update.Steps[0].ID != "epic.repo_truth" || update.Steps[1].ParentStepID != "epic.repo_truth" {
		t.Fatalf("expected normalized stable ids and parent step ids, got %+v", update.Steps)
	}
	if strings.Join(update.Steps[1].DependsOn, ",") != "epic.repo_truth" {
		t.Fatalf("expected deduplicated dependency refs, got %+v", update.Steps[1])
	}
	if update.Steps[0].Priority != StepPriorityHigh || update.Steps[1].Priority != StepPriorityMedium {
		t.Fatalf("expected normalized priorities, got %+v", update.Steps)
	}
	if strings.Join(update.Steps[0].Covers, ",") != "SC-001" {
		t.Fatalf("expected deduplicated covers, got %+v", update.Steps[0])
	}
	if update.Steps[0].Notes != "start with README" {
		t.Fatalf("expected trimmed notes, got %+v", update.Steps[0])
	}
}

func TestNormalizeExecutionPlanUpdateRejectsGraphCycles(t *testing.T) {
	spec := Spec{
		TaskID: "TASK-001",
		SuccessCriteria: []SuccessCriterion{
			{ID: "SC-001", Statement: "docs reviewed"},
		},
	}

	if _, err := NormalizeExecutionPlanUpdate(spec, PlanUpdate{
		Steps: []ExecutionPlanStep{
			{ID: "A", ParentStepID: "B", Title: "A"},
			{ID: "B", ParentStepID: "A", Title: "B"},
		},
	}); err == nil || !strings.Contains(err.Error(), "parent graph contains a cycle") {
		t.Fatalf("expected parent cycle to fail normalization, got %v", err)
	}

	if _, err := NormalizeExecutionPlanUpdate(spec, PlanUpdate{
		Steps: []ExecutionPlanStep{
			{ID: "A", DependsOn: []string{"B"}, Title: "A"},
			{ID: "B", DependsOn: []string{"A"}, Title: "B"},
		},
	}); err == nil || !strings.Contains(err.Error(), "depends_on graph contains a cycle") {
		t.Fatalf("expected dependency cycle to fail normalization, got %v", err)
	}
}

func TestNormalizePlanPatchAndApply(t *testing.T) {
	patch, err := NormalizePlanPatch(PlanPatch{
		Operations: []PlanPatchOperation{
			{Op: "set_explanation", Explanation: "  tighten the mutable graph  "},
			{
				Op:          "upsert_step",
				AfterStepID: "epic.repo_truth",
				Step: &ExecutionPlanStep{
					ID:           " handoff.closeout ",
					ParentStepID: " epic.repo_truth ",
					DependsOn:    []string{" epic.repo_truth ", "epic.repo_truth"},
					Priority:     "HIGH",
					Title:        " Refresh handoff ",
					Status:       "PENDING",
					Covers:       []string{"SC-002", "SC-002"},
					Notes:        "  keep the closeout narrow  ",
				},
			},
			{Op: "remove_step", StepID: "legacy.todo"},
		},
	})
	if err != nil {
		t.Fatalf("normalize plan patch: %v", err)
	}
	if patch.Operations[0].Explanation != "tighten the mutable graph" {
		t.Fatalf("expected trimmed patch explanation, got %+v", patch.Operations[0])
	}
	if patch.Operations[1].Step == nil {
		t.Fatalf("expected normalized upsert step, got %+v", patch.Operations[1])
	}
	if patch.Operations[1].Step.ID != "handoff.closeout" || patch.Operations[1].AfterStepID != "epic.repo_truth" {
		t.Fatalf("expected normalized stable ids, got %+v", patch.Operations[1])
	}
	if patch.Operations[1].Step.Priority != StepPriorityHigh || patch.Operations[1].Step.Status != StepStatusPending {
		t.Fatalf("expected normalized step priority/status, got %+v", patch.Operations[1].Step)
	}
	if strings.Join(patch.Operations[1].Step.DependsOn, ",") != "epic.repo_truth" {
		t.Fatalf("expected deduplicated depends_on, got %+v", patch.Operations[1].Step)
	}
	if strings.Join(patch.Operations[1].Step.Covers, ",") != "SC-002" || patch.Operations[1].Step.Notes != "keep the closeout narrow" {
		t.Fatalf("expected normalized covers/notes, got %+v", patch.Operations[1].Step)
	}

	base := PlanUpdate{
		Explanation: "old explanation",
		Steps: []ExecutionPlanStep{
			{
				ID:       "epic.repo_truth",
				Title:    "Inspect repo truth",
				Status:   StepStatusInProgress,
				Priority: StepPriorityHigh,
				Covers:   []string{"SC-001"},
			},
			{
				ID:       "legacy.todo",
				Title:    "Old step",
				Status:   StepStatusPending,
				Priority: StepPriorityLow,
			},
		},
	}
	updated, err := ApplyPlanPatch(base, patch)
	if err != nil {
		t.Fatalf("apply plan patch: %v", err)
	}
	if updated.Explanation != "tighten the mutable graph" {
		t.Fatalf("expected updated explanation, got %+v", updated)
	}
	if len(updated.Steps) != 2 {
		t.Fatalf("expected two execution steps after patch, got %+v", updated.Steps)
	}
	if updated.Steps[0].ID != "epic.repo_truth" || updated.Steps[1].ID != "handoff.closeout" {
		t.Fatalf("expected upserted step order after repo truth, got %+v", updated.Steps)
	}
}

func TestApplyPlanPatchRejectsUnknownReferences(t *testing.T) {
	base := PlanUpdate{
		Steps: []ExecutionPlanStep{
			{ID: "epic.repo_truth", Title: "Inspect repo truth", Status: StepStatusInProgress},
		},
	}
	_, err := ApplyPlanPatch(base, PlanPatch{
		Operations: []PlanPatchOperation{
			{
				Op:          PlanPatchOpUpsertStep,
				AfterStepID: "missing.step",
				Step: &ExecutionPlanStep{
					ID:     "handoff.closeout",
					Title:  "Refresh handoff",
					Status: StepStatusPending,
				},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "after_step_id references unknown step") {
		t.Fatalf("expected missing after_step_id to fail, got %v", err)
	}

	_, err = ApplyPlanPatch(base, PlanPatch{
		Operations: []PlanPatchOperation{
			{Op: PlanPatchOpRemoveStep, StepID: "missing.step"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "step_id references unknown step") {
		t.Fatalf("expected missing remove step to fail, got %v", err)
	}
}

func TestNormalizeProjectUpdate(t *testing.T) {
	update, err := NormalizeProjectUpdate(ProjectUpdate{
		Explanation: "  coordinate the durable tasks  ",
		Steps: []ProjectExecutionStep{
			{ID: " epic.repo_truth ", Title: " Inspect repo truth ", Status: "IN_PROGRESS", Priority: "HIGH", BranchID: " branch.repo ", TaskID: " TASK-001 ", Notes: "  start with README  "},
			{ID: "task.patch", ParentStepID: "epic.repo_truth", DependsOn: []string{" epic.repo_truth ", "epic.repo_truth"}, Priority: "", Title: "Apply patch", Status: "PENDING", BranchID: "branch.patch", TaskID: "TASK-002", Notes: ""},
		},
		Branches: []ProjectBranchSpec{
			{ID: " branch.repo ", Title: " Repo truth ", Status: "ACTIVE", TaskID: " TASK-001 ", Notes: "  first lane  "},
			{ID: "branch.patch", Title: "Patch lane", Status: "", TaskID: "TASK-002", Notes: ""},
		},
	})
	if err != nil {
		t.Fatalf("normalize project update: %v", err)
	}
	if update.Explanation != "coordinate the durable tasks" {
		t.Fatalf("expected trimmed project explanation, got %+v", update)
	}
	if update.Steps[0].ID != "epic.repo_truth" || update.Steps[1].ParentStepID != "epic.repo_truth" {
		t.Fatalf("expected normalized project step ids, got %+v", update.Steps)
	}
	if update.Steps[0].BranchID != "branch.repo" || update.Steps[0].TaskID != "TASK-001" {
		t.Fatalf("expected normalized branch/task bindings, got %+v", update.Steps[0])
	}
	if strings.Join(update.Steps[1].DependsOn, ",") != "epic.repo_truth" {
		t.Fatalf("expected deduplicated project deps, got %+v", update.Steps[1])
	}
	if update.Steps[0].Priority != StepPriorityHigh || update.Steps[1].Priority != StepPriorityMedium {
		t.Fatalf("expected normalized project priorities, got %+v", update.Steps)
	}
	if update.Branches[0].ID != "branch.repo" || update.Branches[0].Status != ProjectBranchStatusActive || update.Branches[0].TaskID != "TASK-001" {
		t.Fatalf("expected normalized branch shape, got %+v", update.Branches[0])
	}
	if update.Branches[1].Status != ProjectBranchStatusPending {
		t.Fatalf("expected default branch status pending, got %+v", update.Branches[1])
	}
}

func TestNormalizeProjectPatchAndApply(t *testing.T) {
	patch, err := NormalizeProjectPatch(ProjectPatch{
		Operations: []ProjectPatchOperation{
			{Op: "set_explanation", Explanation: "  refine workspace orchestration  "},
			{Op: "set_step_dependencies", StepID: " task.patch ", DependsOn: []string{" epic.repo_truth ", "epic.repo_truth"}},
			{Op: "bind_step_branch", StepID: " task.patch ", BranchID: " branch.patch "},
			{Op: "bind_step_task", StepID: " task.patch ", TaskID: " TASK-002 "},
			{Op: "set_branch_status", BranchID: " branch.patch ", Status: "ACTIVE"},
		},
	})
	if err != nil {
		t.Fatalf("normalize project patch: %v", err)
	}
	if patch.Operations[0].Explanation != "refine workspace orchestration" {
		t.Fatalf("expected trimmed project patch explanation, got %+v", patch.Operations[0])
	}
	if patch.Operations[1].StepID != "task.patch" || strings.Join(patch.Operations[1].DependsOn, ",") != "epic.repo_truth" {
		t.Fatalf("expected normalized edge patch, got %+v", patch.Operations[1])
	}
	if patch.Operations[2].BranchID != "branch.patch" || patch.Operations[3].TaskID != "TASK-002" {
		t.Fatalf("expected normalized branch/task binding patch, got %+v %+v", patch.Operations[2], patch.Operations[3])
	}
	if patch.Operations[4].Status != ProjectBranchStatusActive {
		t.Fatalf("expected normalized branch status patch, got %+v", patch.Operations[4])
	}

	base := ProjectUpdate{
		Explanation: "old explanation",
		Steps: []ProjectExecutionStep{
			{ID: "epic.repo_truth", Title: "Inspect repo truth", Status: ProjectStepStatusInProgress, Priority: StepPriorityHigh, BranchID: "branch.repo", TaskID: "TASK-001"},
			{ID: "task.patch", Title: "Apply patch", Status: ProjectStepStatusPending, Priority: StepPriorityMedium},
		},
		Branches: []ProjectBranchSpec{
			{ID: "branch.repo", Title: "Repo truth", Status: ProjectBranchStatusActive, TaskID: "TASK-001"},
			{ID: "branch.patch", Title: "Patch lane", Status: ProjectBranchStatusPending},
		},
	}
	updated, err := ApplyProjectPatch(base, patch)
	if err != nil {
		t.Fatalf("apply project patch: %v", err)
	}
	if updated.Explanation != "refine workspace orchestration" {
		t.Fatalf("expected updated project explanation, got %+v", updated)
	}
	if strings.Join(updated.Steps[1].DependsOn, ",") != "epic.repo_truth" || updated.Steps[1].BranchID != "branch.patch" || updated.Steps[1].TaskID != "TASK-002" {
		t.Fatalf("expected patched step bindings, got %+v", updated.Steps[1])
	}
	if updated.Branches[1].Status != ProjectBranchStatusActive {
		t.Fatalf("expected patched branch status, got %+v", updated.Branches[1])
	}
}

func TestLoadConfigRejectsInvalidPermissionDefaultMode(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ngen.json"), `{
  "permission": {
    "default_mode": "unsafe"
  }
}`)

	_, err := LoadConfig(dir)
	if err == nil {
		t.Fatal("expected invalid permission.default_mode error")
	}
	if !strings.Contains(err.Error(), "unsupported permission.default_mode") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadConfigAcceptsBenchmarkIntegrityMode(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ngen.json"), `{
  "permission": {
    "default_mode": "yolo",
    "benchmark_integrity_mode": true
  }
}`)
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Permission.DefaultMode != PermissionModeYolo {
		t.Fatalf("expected default mode yolo, got %q", cfg.Permission.DefaultMode)
	}
	if !cfg.Permission.BenchmarkIntegrityMode {
		t.Fatal("expected benchmark_integrity_mode to be preserved")
	}
}

func TestLoadConfigRejectsInvalidTUIAltScreenMode(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ngen.json"), `{
  "tui": {
    "alternate_screen": "sometimes"
  }
}`)
	_, err := LoadConfig(dir)
	if err == nil || !strings.Contains(err.Error(), "unsupported tui.alternate_screen") {
		t.Fatalf("expected unsupported tui.alternate_screen error, got %v", err)
	}
}

func TestLoadConfigDefaultsAndClampsTUISettings(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ngen.json"), `{
  "tui": {
    "poll_interval_ms": 10,
    "event_limit": 0
  }
}`)
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.TUI.AlternateScreen != "auto" {
		t.Fatalf("expected default alternate_screen=auto, got %q", cfg.TUI.AlternateScreen)
	}
	if cfg.TUI.PollIntervalMS != 50 {
		t.Fatalf("expected poll_interval_ms to clamp to 50, got %d", cfg.TUI.PollIntervalMS)
	}
	if cfg.TUI.EventLimit != DefaultConfig().TUI.EventLimit {
		t.Fatalf("expected default event_limit, got %d", cfg.TUI.EventLimit)
	}
}

func TestLoadConfigRejectsUnsupportedStateDir(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ngen.json"), `{
  "state_dir": ".custom-ngen"
}`)
	_, err := LoadConfig(dir)
	if err == nil || !strings.Contains(err.Error(), "state_dir is fixed to .ngen") {
		t.Fatalf("expected fixed state_dir error, got %v", err)
	}
}

func TestLoadConfigRejectsUnsafeWorkspaceRelativeConfigPaths(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		message string
	}{
		{
			name: "state traversal",
			config: `{
  "state_dir": "../outside"
}`,
			message: "state_dir must stay inside the workspace",
		},
		{
			name: "scheduler absolute",
			config: `{
  "scheduler": {
    "lease_file": "/tmp/ngen.lock"
  }
}`,
			message: "scheduler.lease_file must be relative to the workspace",
		},
		{
			name: "scheduler windows drive",
			config: `{
  "scheduler": {
    "lease_file": "C:/tmp/ngen.lock"
  }
}`,
			message: "scheduler.lease_file must be a workspace-relative slash path",
		},
		{
			name: "memory backslash",
			config: `{
  "memory": {
    "file": ".ngen\\memory\\MEMORY.md"
  }
}`,
			message: "memory.file must be a workspace-relative slash path",
		},
		{
			name: "memory nul",
			config: `{
  "memory": {
    "file": ".ngen/memory/MEMORY.md\u0000suffix"
  }
}`,
			message: "memory.file must be a workspace-relative slash path",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "ngen.json"), tt.config)
			_, err := LoadConfig(dir)
			if err == nil || !strings.Contains(err.Error(), tt.message) {
				t.Fatalf("expected %q error, got %v", tt.message, err)
			}
		})
	}
}

func TestLoadConfigRejectsInvalidWorkspaceIsolationMode(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ngen.json"), `{
  "subagents": {
    "workspace_isolation": "mystery"
  }
}`)

	_, err := LoadConfig(dir)
	if err == nil {
		t.Fatal("expected invalid subagents.workspace_isolation error")
	}
	if !strings.Contains(err.Error(), "unsupported subagents.workspace_isolation") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadConfigNormalizesMissionRoleModels(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ngen.json"), `{
  "provider": {
    "model": "global-model"
  },
  "mission": {
    "role_models": {
      "orchestrator": " orchestrator-model ",
      "workers": "worker-model",
      "validators": ""
    }
  }
}`)

	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	_, orchestrator, err := ProviderConfigForMissionRole(cfg, MissionRoleOrchestrator)
	if err != nil {
		t.Fatalf("resolve orchestrator: %v", err)
	}
	if orchestrator.Model != "orchestrator-model" || orchestrator.Source != MissionRoleModelSourceMission || !orchestrator.Explicit {
		t.Fatalf("unexpected orchestrator resolution: %+v", orchestrator)
	}
	workerCfg, workers, err := ProviderConfigForMissionRole(cfg, MissionRoleWorkers)
	if err != nil {
		t.Fatalf("resolve workers: %v", err)
	}
	if workerCfg.Model != "worker-model" || workers.Source != MissionRoleModelSourceMission || !workers.Explicit {
		t.Fatalf("unexpected workers resolution: cfg=%+v resolution=%+v", workerCfg, workers)
	}
	_, validators, err := ProviderConfigForMissionRole(cfg, MissionRoleValidators)
	if err != nil {
		t.Fatalf("resolve validators: %v", err)
	}
	if validators.Model != "global-model" || validators.Source != MissionRoleModelSourceProvider || validators.Explicit {
		t.Fatalf("expected validators to inherit provider.model without explicit opt-in, got %+v", validators)
	}
}

func TestLoadConfigRejectsUnsupportedMissionRoleModelKey(t *testing.T) {
	for _, role := range []string{"planner", "worker", "validator"} {
		t.Run(role, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "ngen.json"), fmt.Sprintf(`{
  "mission": {
    "role_models": {
      %q: "gpt-test"
    }
  }
}`, role))

			_, err := LoadConfig(dir)
			if err == nil {
				t.Fatal("expected unsupported mission role key error")
			}
			if !strings.Contains(err.Error(), "unsupported mission.role_models role") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoadConfigNormalizesRolePolicies(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ngen.json"), `{
  "subagents": {
    "role_policies": {
      "general": {
        "workspace_isolation": "snapshot-copy",
        "reconcile_mode": "artifact-only",
        "allowed_worker_roles": ["security-review", "reviewer"],
        "max_workers_per_task": 2,
        "max_lineage_depth": 3
      }
    }
  }
}`)

	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	policy, ok := cfg.Subagents.RolePolicies[string(KindGeneral)]
	if !ok {
		t.Fatalf("expected normalized general_execution policy, got %+v", cfg.Subagents.RolePolicies)
	}
	if policy.WorkspaceIsolation != "snapshot_copy" {
		t.Fatalf("expected normalized workspace isolation, got %+v", policy)
	}
	if policy.ReconcileMode != "artifact_only" {
		t.Fatalf("expected normalized reconcile mode, got %+v", policy)
	}
	if strings.Join(policy.AllowedWorkerRoles, ",") != "security_review,reviewer" {
		t.Fatalf("expected normalized allowed worker roles, got %+v", policy)
	}
}

func TestLoadConfigRejectsInvalidRolePolicyReconcileMode(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ngen.json"), `{
  "subagents": {
    "role_policies": {
      "coding": {
        "reconcile_mode": "mystery"
      }
    }
  }
}`)

	_, err := LoadConfig(dir)
	if err == nil {
		t.Fatal("expected invalid subagents.role_policies reconcile mode error")
	}
	if !strings.Contains(err.Error(), "reconcile_mode") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNormalizeWorkerRole(t *testing.T) {
	role, kind, preset, err := NormalizeWorkerRole("docs_lite")
	if err != nil {
		t.Fatalf("normalize docs_lite worker role: %v", err)
	}
	if role != string(KindGeneral) || kind != KindGeneral || preset != PresetDocsLite {
		t.Fatalf("unexpected docs worker normalization: role=%s kind=%s preset=%s", role, kind, preset)
	}

	role, kind, preset, err = NormalizeWorkerRole("security-review")
	if err != nil {
		t.Fatalf("normalize security worker role: %v", err)
	}
	if role != string(KindSecurityReview) || kind != KindSecurityReview || preset != "" {
		t.Fatalf("unexpected security worker normalization: role=%s kind=%s preset=%s", role, kind, preset)
	}

	if _, _, _, err := NormalizeWorkerRole("unknown"); err == nil {
		t.Fatal("expected unsupported worker role to fail normalization")
	}
}

func TestValidateRoleContractNormalizesAliasesAndActions(t *testing.T) {
	contract, err := ValidateRoleContract(RoleContract{
		SchemaVersion:            SchemaVersion,
		RoleID:                   "security-review",
		AllowedProviderActions:   []string{"run", "run", "review"},
		AllowedWorkerRoles:       []string{"docs-lite", "reviewer", "docs_lite"},
		WorkspaceIsolation:       "snapshot-copy",
		ReconcileMode:            "artifact-only",
		PermissionModeID:         PermissionModeYolo,
		ContextSections:          []string{"task", "task", "criteria"},
		ReviewRequirements:       []string{"evidence refs", "evidence refs"},
		VerificationRequirements: []string{"inventory"},
	})
	if err != nil {
		t.Fatalf("validate role contract: %v", err)
	}
	if contract.RoleID != string(KindSecurityReview) || contract.ProfileKind != KindSecurityReview {
		t.Fatalf("expected normalized security role, got %+v", contract)
	}
	if strings.Join(contract.AllowedProviderActions, ",") != "run,review" {
		t.Fatalf("expected deduped provider actions, got %+v", contract.AllowedProviderActions)
	}
	if strings.Join(contract.AllowedWorkerRoles, ",") != "general_execution,reviewer" {
		t.Fatalf("expected normalized worker roles, got %+v", contract.AllowedWorkerRoles)
	}
	if contract.WorkspaceIsolation != "snapshot_copy" || contract.ReconcileMode != "artifact_only" {
		t.Fatalf("expected normalized policy fields, got %+v", contract)
	}
}

func TestValidateRoleContractRejectsForbiddenAction(t *testing.T) {
	_, err := ValidateRoleContract(RoleContract{
		SchemaVersion:          SchemaVersion,
		RoleID:                 string(KindCoding),
		AllowedProviderActions: []string{"run", "browser_drive"},
	})
	if err == nil {
		t.Fatal("expected unsupported provider action to fail role validation")
	}
	if !strings.Contains(err.Error(), "unsupported action") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNormalizeWorkspaceIsolationMode(t *testing.T) {
	if got := NormalizeWorkspaceIsolationMode("shared"); got != "shared_workspace" {
		t.Fatalf("expected shared alias to normalize, got %q", got)
	}
	if got := NormalizeWorkspaceIsolationMode("snapshot-copy"); got != "snapshot_copy" {
		t.Fatalf("expected snapshot alias to normalize, got %q", got)
	}
	if got := NormalizeWorkspaceIsolationMode("git"); got != "git_worktree" {
		t.Fatalf("expected git alias to normalize, got %q", got)
	}
	if got := NormalizeWorkspaceIsolationMode(""); got != "auto" {
		t.Fatalf("expected empty mode to normalize to auto, got %q", got)
	}
}

func TestHydrateSpecAddsLineageRootAndPolicy(t *testing.T) {
	cfg := DefaultConfig()
	spec := HydrateSpec(Spec{
		TaskID:           "TASK-001",
		Kind:             KindCoding,
		PermissionModeID: PermissionModeYolo,
	}, cfg)

	if spec.RootTaskID != "TASK-001" {
		t.Fatalf("expected root task id to default to task id, got %+v", spec)
	}
	if spec.SubagentPolicy == nil || !spec.SubagentPolicy.AllowChildWorkers {
		t.Fatalf("expected hydrated coding spec to allow child workers, got %+v", spec.SubagentPolicy)
	}
	if spec.SubagentPolicy.PermissionModeID != PermissionModeYolo {
		t.Fatalf("expected hydrated policy to preserve permission mode, got %+v", spec.SubagentPolicy)
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
