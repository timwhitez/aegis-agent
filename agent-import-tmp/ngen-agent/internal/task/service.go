package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

func DefaultConfig() Config {
	return Config{
		StateDir:       ".ngen",
		DefaultProfile: KindCoding,
		Verification: VerificationConfig{
			CodingGoTestCommand:  []string{"go", "test", "./..."},
			CodingTimeoutSeconds: 60,
		},
		Watch: WatchConfig{
			DefaultIntervalSeconds: 300,
		},
		Scheduler: SchedulerConfig{
			LeaseFile: filepath.ToSlash(filepath.Join(".ngen", "runtime", "scheduler.lock")),
		},
		Provider: ProviderConfig{
			Mode:                                   "builtin",
			AutoRunMaxTurns:                        3,
			APIKeyEnv:                              "OPENAI_API_KEY",
			DecisionTimeoutSeconds:                 30,
			DecisionMaxOutputTokens:                2048,
			CodingRepairBudget:                     3,
			CodingObservationCommandBudget:         2,
			CodingObservationCommandTimeoutSeconds: 20,
			CodingExecutionCommandBudget:           2,
			CodingExecutionCommandTimeoutSeconds:   60,
		},
		Hooks: HookConfig{},
		Visibility: VisibilityConfig{
			DenyPatterns: []string{".git", ".ngen"},
		},
		Memory: MemoryConfig{
			Enabled:    true,
			File:       filepath.ToSlash(filepath.Join(".ngen", "memory", "MEMORY.md")),
			MaxEntries: 50,
		},
		Subagents: SubagentConfig{
			MaxWorkersPerTask:    4,
			WorkspaceIsolation:   "auto",
			AutoReleaseOnSuccess: true,
			MaxLineageDepth:      2,
		},
		ACP: ACPConfig{
			Enabled: true,
		},
		Permission: PermissionConfig{
			DefaultMode: PermissionModeStandard,
		},
		TUI: TUIConfig{
			AlternateScreen: "auto",
			PollIntervalMS:  200,
			EventLimit:      500,
		},
	}
}

func LoadConfig(workspaceRoot string) (Config, error) {
	path := filepath.Join(workspaceRoot, "ngen.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return NormalizeConfig(DefaultConfig(), nil)
		}
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	return LoadConfigBytes(data)
}

func LoadConfigFile(path string) (Config, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	return LoadConfigBytes(data)
}

func LoadConfigBytes(data []byte) (Config, error) {
	cfg := DefaultConfig()
	if len(data) > 0 {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config: %w", err)
		}
	}
	return NormalizeConfig(cfg, data)
}

func NormalizeConfig(cfg Config, raw []byte) (Config, error) {
	var err error
	missionConfig, err := normalizeMissionConfig(cfg.Mission, raw)
	if err != nil {
		return Config{}, err
	}
	cfg.Mission = missionConfig
	if cfg.StateDir == "" {
		cfg.StateDir = ".ngen"
	}
	if cfg.StateDir, err = normalizeWorkspaceRelativeConfigPath("state_dir", cfg.StateDir); err != nil {
		return Config{}, err
	}
	if cfg.StateDir != ".ngen" {
		return Config{}, fmt.Errorf("state_dir is fixed to .ngen in the current runtime: %q", cfg.StateDir)
	}
	normalizedCodingCommands, err := normalizeCommandMatrix(cfg.Verification.CodingCommands)
	if err != nil {
		return Config{}, err
	}
	cfg.Verification.CodingCommands = normalizedCodingCommands
	if len(cfg.Verification.CodingGoTestCommand) == 0 && len(cfg.Verification.CodingCommands) == 0 {
		cfg.Verification.CodingGoTestCommand = DefaultConfig().Verification.CodingGoTestCommand
	}
	if cfg.Verification.CodingTimeoutSeconds <= 0 {
		cfg.Verification.CodingTimeoutSeconds = DefaultConfig().Verification.CodingTimeoutSeconds
	}
	if cfg.Watch.DefaultIntervalSeconds <= 0 {
		cfg.Watch.DefaultIntervalSeconds = DefaultConfig().Watch.DefaultIntervalSeconds
	}
	if cfg.Scheduler.LeaseFile == "" {
		cfg.Scheduler.LeaseFile = DefaultConfig().Scheduler.LeaseFile
	}
	if cfg.Scheduler.LeaseFile, err = normalizeWorkspaceRelativeConfigPath("scheduler.lease_file", cfg.Scheduler.LeaseFile); err != nil {
		return Config{}, err
	}
	if cfg.Provider.Mode == "" {
		cfg.Provider.Mode = DefaultConfig().Provider.Mode
	}
	if cfg.Provider.AutoRunMaxTurns <= 0 {
		cfg.Provider.AutoRunMaxTurns = DefaultConfig().Provider.AutoRunMaxTurns
	}
	if cfg.Provider.APIKeyEnv == "" {
		cfg.Provider.APIKeyEnv = DefaultConfig().Provider.APIKeyEnv
	}
	if cfg.Provider.DecisionTimeoutSeconds <= 0 {
		cfg.Provider.DecisionTimeoutSeconds = DefaultConfig().Provider.DecisionTimeoutSeconds
	}
	if cfg.Provider.DecisionMaxOutputTokens <= 0 {
		cfg.Provider.DecisionMaxOutputTokens = DefaultConfig().Provider.DecisionMaxOutputTokens
	}
	if cfg.Provider.CodingRepairBudget <= 0 {
		cfg.Provider.CodingRepairBudget = DefaultConfig().Provider.CodingRepairBudget
	}
	if cfg.Provider.CodingObservationCommandBudget < 0 {
		cfg.Provider.CodingObservationCommandBudget = DefaultConfig().Provider.CodingObservationCommandBudget
	}
	if cfg.Provider.CodingObservationCommandTimeoutSeconds <= 0 {
		cfg.Provider.CodingObservationCommandTimeoutSeconds = DefaultConfig().Provider.CodingObservationCommandTimeoutSeconds
	}
	if cfg.Provider.CodingExecutionCommandBudget < 0 {
		cfg.Provider.CodingExecutionCommandBudget = DefaultConfig().Provider.CodingExecutionCommandBudget
	}
	if cfg.Provider.CodingExecutionCommandTimeoutSeconds <= 0 {
		cfg.Provider.CodingExecutionCommandTimeoutSeconds = DefaultConfig().Provider.CodingExecutionCommandTimeoutSeconds
	}
	if len(cfg.Visibility.DenyPatterns) == 0 {
		cfg.Visibility.DenyPatterns = DefaultConfig().Visibility.DenyPatterns
	}
	if cfg.Memory.File == "" {
		cfg.Memory.File = DefaultConfig().Memory.File
	}
	if cfg.Memory.File, err = normalizeWorkspaceRelativeConfigPath("memory.file", cfg.Memory.File); err != nil {
		return Config{}, err
	}
	if cfg.Memory.MaxEntries <= 0 {
		cfg.Memory.MaxEntries = DefaultConfig().Memory.MaxEntries
	}
	if cfg.Subagents.MaxWorkersPerTask <= 0 {
		cfg.Subagents.MaxWorkersPerTask = DefaultConfig().Subagents.MaxWorkersPerTask
	}
	if cfg.Subagents.MaxLineageDepth <= 0 {
		cfg.Subagents.MaxLineageDepth = DefaultConfig().Subagents.MaxLineageDepth
	}
	cfg.Subagents.WorkspaceIsolation = NormalizeWorkspaceIsolationMode(cfg.Subagents.WorkspaceIsolation)
	if cfg.Subagents.WorkspaceIsolation == "" {
		cfg.Subagents.WorkspaceIsolation = DefaultConfig().Subagents.WorkspaceIsolation
	}
	if !IsSupportedWorkspaceIsolationMode(cfg.Subagents.WorkspaceIsolation) {
		return Config{}, fmt.Errorf("unsupported subagents.workspace_isolation: %s", cfg.Subagents.WorkspaceIsolation)
	}
	if len(cfg.Subagents.RolePolicies) > 0 {
		normalized, err := normalizeSubagentRolePolicies(cfg.Subagents.RolePolicies)
		if err != nil {
			return Config{}, err
		}
		cfg.Subagents.RolePolicies = normalized
	}
	if cfg.Permission.DefaultMode == "" {
		cfg.Permission.DefaultMode = DefaultConfig().Permission.DefaultMode
	}
	if !IsSupportedPermissionMode(cfg.Permission.DefaultMode) {
		return Config{}, fmt.Errorf("unsupported permission.default_mode: %s", cfg.Permission.DefaultMode)
	}
	cfg.TUI.AlternateScreen = NormalizeAltScreenMode(cfg.TUI.AlternateScreen)
	if cfg.TUI.AlternateScreen == "" {
		cfg.TUI.AlternateScreen = DefaultConfig().TUI.AlternateScreen
	}
	if !IsSupportedAltScreenMode(cfg.TUI.AlternateScreen) {
		return Config{}, fmt.Errorf("unsupported tui.alternate_screen: %s", cfg.TUI.AlternateScreen)
	}
	if cfg.TUI.PollIntervalMS <= 0 {
		cfg.TUI.PollIntervalMS = DefaultConfig().TUI.PollIntervalMS
	}
	if cfg.TUI.PollIntervalMS < 50 {
		cfg.TUI.PollIntervalMS = 50
	}
	if cfg.TUI.EventLimit <= 0 {
		cfg.TUI.EventLimit = DefaultConfig().TUI.EventLimit
	}
	return cfg, nil
}

func normalizeWorkspaceRelativeConfigPath(field, value string) (string, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return "", fmt.Errorf("%s is required", field)
	}
	if strings.ContainsRune(raw, '\x00') || strings.ContainsAny(raw, `\:`) {
		return "", fmt.Errorf("%s must be a workspace-relative slash path: %q", field, value)
	}
	if filepath.IsAbs(raw) {
		return "", fmt.Errorf("%s must be relative to the workspace: %q", field, value)
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(raw)))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%s must stay inside the workspace: %q", field, value)
	}
	return clean, nil
}

func normalizeCommandMatrix(commands [][]string) ([][]string, error) {
	if len(commands) == 0 {
		return nil, nil
	}
	normalized := make([][]string, 0, len(commands))
	for i, command := range commands {
		var cleaned []string
		for _, token := range command {
			token = strings.TrimSpace(token)
			if token == "" {
				continue
			}
			cleaned = append(cleaned, token)
		}
		if len(cleaned) == 0 {
			return nil, fmt.Errorf("verification.coding_commands[%d] must not be empty", i)
		}
		normalized = append(normalized, cleaned)
	}
	return normalized, nil
}

func normalizeMissionConfig(cfg MissionConfig, raw []byte) (MissionConfig, error) {
	present := missionRoleModelPresence(raw)
	if len(cfg.RoleModelPresent) > 0 {
		if present == nil {
			present = map[string]bool{}
		}
		for role, ok := range cfg.RoleModelPresent {
			if ok {
				present[role] = true
			}
		}
	}
	normalized := MissionConfig{
		RoleModels:       map[string]string{},
		RoleModelPresent: map[string]bool{},
	}
	for role, model := range cfg.RoleModels {
		canonicalRole, err := NormalizeMissionRole(role)
		if err != nil {
			return MissionConfig{}, err
		}
		normalized.RoleModels[canonicalRole] = strings.TrimSpace(model)
	}
	for role, ok := range present {
		if !ok {
			continue
		}
		canonicalRole, err := NormalizeMissionRole(role)
		if err != nil {
			return MissionConfig{}, err
		}
		normalized.RoleModelPresent[canonicalRole] = true
	}
	for role, model := range normalized.RoleModels {
		if strings.TrimSpace(model) != "" && len(present) == 0 {
			normalized.RoleModelPresent[role] = true
		}
	}
	if len(normalized.RoleModels) == 0 {
		normalized.RoleModels = nil
	}
	if len(normalized.RoleModelPresent) == 0 {
		normalized.RoleModelPresent = nil
	}
	return normalized, nil
}

func missionRoleModelPresence(raw []byte) map[string]bool {
	if len(raw) == 0 {
		return nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil
	}
	rawMission, ok := root["mission"]
	if !ok {
		return nil
	}
	var mission map[string]json.RawMessage
	if err := json.Unmarshal(rawMission, &mission); err != nil {
		return nil
	}
	rawRoleModels, ok := mission["role_models"]
	if !ok {
		return nil
	}
	var roleModels map[string]json.RawMessage
	if err := json.Unmarshal(rawRoleModels, &roleModels); err != nil {
		return nil
	}
	present := make(map[string]bool, len(roleModels))
	for role, rawModel := range roleModels {
		var model string
		if err := json.Unmarshal(rawModel, &model); err == nil && strings.TrimSpace(model) != "" {
			present[role] = true
		}
	}
	return present
}

func NormalizeMissionRole(role string) (string, error) {
	switch normalized := strings.ToLower(strings.TrimSpace(role)); normalized {
	case MissionRoleOrchestrator:
		return MissionRoleOrchestrator, nil
	case MissionRoleWorkers:
		return MissionRoleWorkers, nil
	case MissionRoleValidators:
		return MissionRoleValidators, nil
	default:
		return "", fmt.Errorf("unsupported mission.role_models role: %s", strings.TrimSpace(role))
	}
}

func SupportedMissionRoles() []string {
	return []string{MissionRoleOrchestrator, MissionRoleWorkers, MissionRoleValidators}
}

func ProviderConfigForMissionRole(cfg Config, role string) (ProviderConfig, MissionRoleModelResolution, error) {
	canonicalRole, err := NormalizeMissionRole(role)
	if err != nil {
		return ProviderConfig{}, MissionRoleModelResolution{}, err
	}
	providerCfg := cfg.Provider
	model := strings.TrimSpace(cfg.Provider.Model)
	source := MissionRoleModelSourceEmpty
	if model != "" {
		source = MissionRoleModelSourceProvider
	}
	explicit := false
	if cfg.Mission.RoleModels != nil {
		if configured, ok := cfg.Mission.RoleModels[canonicalRole]; ok && strings.TrimSpace(configured) != "" {
			model = strings.TrimSpace(configured)
			source = MissionRoleModelSourceMission
		}
	}
	if cfg.Mission.RoleModelPresent != nil && cfg.Mission.RoleModelPresent[canonicalRole] {
		explicit = strings.TrimSpace(cfg.Mission.RoleModels[canonicalRole]) != ""
	} else if cfg.Mission.RoleModels != nil && strings.TrimSpace(cfg.Mission.RoleModels[canonicalRole]) != "" {
		explicit = true
	}
	providerCfg.Model = model
	return providerCfg, MissionRoleModelResolution{
		Role:     canonicalRole,
		Model:    model,
		Source:   source,
		Explicit: explicit,
	}, nil
}

func ValidateTaskFile(tf TaskFile) error {
	if tf.Objective == "" {
		return errors.New("objective is required")
	}
	if len(tf.SuccessCriteria) == 0 {
		return errors.New("at least one success criterion is required")
	}
	switch tf.Kind {
	case "", KindCoding, KindSecurityReview, KindReviewer:
	case KindGeneral:
		if tf.PresetID != PresetDocsLite {
			return errors.New("general_execution currently requires preset docs_lite")
		}
	default:
		return fmt.Errorf("unsupported kind: %s", tf.Kind)
	}
	if tf.PermissionModeID != "" && !IsSupportedPermissionMode(tf.PermissionModeID) {
		return fmt.Errorf("unsupported permission mode: %s", tf.PermissionModeID)
	}
	if tf.LineageDepth < 0 {
		return fmt.Errorf("lineage_depth must be >= 0")
	}
	if err := validateEffectiveSubagentPolicy(tf.SubagentPolicy); err != nil {
		return err
	}
	return nil
}

func NormalizeTaskFile(tf TaskFile, workspaceRoot string, cfg Config) TaskFile {
	if tf.Kind == "" {
		tf.Kind = cfg.DefaultProfile
	}
	if tf.WorkspaceRoot == "" {
		tf.WorkspaceRoot = workspaceRoot
	}
	if tf.PermissionModeID == "" {
		tf.PermissionModeID = EffectivePermissionModeID(cfg.Permission.DefaultMode)
	} else {
		tf.PermissionModeID = EffectivePermissionModeID(tf.PermissionModeID)
	}
	if tf.LineageDepth < 0 {
		tf.LineageDepth = 0
	}
	if tf.SubagentPolicy == nil {
		policy := DefaultSubagentPolicyForTask(cfg, tf.Kind, tf.PresetID, tf.PermissionModeID)
		tf.SubagentPolicy = &policy
	}
	return tf
}

func NormalizeAltScreenMode(mode string) string {
	switch normalized := strings.ToLower(strings.TrimSpace(mode)); normalized {
	case "", "auto":
		return normalized
	case "always", "never":
		return normalized
	default:
		return normalized
	}
}

func IsSupportedAltScreenMode(mode string) bool {
	switch NormalizeAltScreenMode(mode) {
	case "auto", "always", "never":
		return true
	default:
		return false
	}
}

func NewSpec(tf TaskFile) Spec {
	taskID := NewID("TASK")
	rootTaskID := tf.RootTaskID
	if strings.TrimSpace(rootTaskID) == "" {
		rootTaskID = taskID
	}
	return Spec{
		SchemaVersion:    SchemaVersion,
		TaskID:           taskID,
		Kind:             tf.Kind,
		PresetID:         tf.PresetID,
		Title:            tf.Title,
		Objective:        tf.Objective,
		SuccessCriteria:  tf.SuccessCriteria,
		Constraints:      tf.Constraints,
		WorkspaceRoot:    tf.WorkspaceRoot,
		PermissionModeID: tf.PermissionModeID,
		ParentTaskID:     tf.ParentTaskID,
		ParentWorkerID:   tf.ParentWorkerID,
		RootTaskID:       rootTaskID,
		LineageDepth:     tf.LineageDepth,
		SubagentPolicy:   cloneSubagentPolicy(tf.SubagentPolicy),
		CreatedAt:        Now(),
	}
}

func NewBootstrapPlan(spec Spec) Plan {
	now := Now()
	var covers []string
	for _, criterion := range spec.SuccessCriteria {
		covers = append(covers, criterion.ID)
	}
	steps := []Step{
		{
			ID:        "STEP-001",
			Kind:      StepKindBaseline,
			Source:    StepSourceSystem,
			Title:     "Capture baseline",
			Status:    StepStatusInProgress,
			Covers:    covers,
			Verifier:  []string{"baseline"},
			UpdatedAt: now,
		},
	}
	for i, criterion := range spec.SuccessCriteria {
		stepID := fmt.Sprintf("STEP-%03d", i+2)
		steps = append(steps, Step{
			ID:        stepID,
			Kind:      StepKindCriterion,
			Source:    StepSourceSystem,
			Title:     criterionPlanTitle(criterion),
			Status:    StepStatusPending,
			Covers:    []string{criterion.ID},
			Verifier:  []string{"criteria_evidence"},
			UpdatedAt: now,
		})
	}
	finalStepID := fmt.Sprintf("STEP-%03d", len(spec.SuccessCriteria)+2)
	steps = append(steps, Step{
		ID:        finalStepID,
		Kind:      StepKindReviewGate,
		Source:    StepSourceSystem,
		Title:     "Review evidence, refresh handoff, and close the task",
		Status:    StepStatusPending,
		Covers:    covers,
		Verifier:  []string{"review", "completion_gate", "handoff"},
		UpdatedAt: now,
	})
	return Plan{
		SchemaVersion: SchemaVersion,
		TaskID:        spec.TaskID,
		UpdatedAt:     now,
		Steps:         steps,
	}
}

func NewProject(workspaceRoot string) Project {
	return Project{
		SchemaVersion: SchemaVersion,
		WorkspaceRoot: workspaceRoot,
		UpdatedAt:     Now(),
		Steps:         []ProjectStep{},
		Branches:      []ProjectBranch{},
	}
}

func criterionPlanTitle(criterion SuccessCriterion) string {
	statement := strings.TrimSpace(criterion.Statement)
	if statement == "" {
		return fmt.Sprintf("Satisfy %s", criterion.ID)
	}
	return fmt.Sprintf("Satisfy %s: %s", criterion.ID, statement)
}

func NormalizeExecutionPlanUpdate(spec Spec, update PlanUpdate) (PlanUpdate, error) {
	normalized := PlanUpdate{
		Explanation: strings.TrimSpace(update.Explanation),
	}
	allowedCriteria := make(map[string]struct{}, len(spec.SuccessCriteria))
	for _, criterion := range spec.SuccessCriteria {
		allowedCriteria[strings.TrimSpace(criterion.ID)] = struct{}{}
	}
	seenIDs := make(map[string]int, len(update.Steps))
	inProgressCount := 0
	for idx, step := range update.Steps {
		title := strings.TrimSpace(step.Title)
		if title == "" {
			return PlanUpdate{}, fmt.Errorf("steps[%d].title is required", idx)
		}
		stepID, err := normalizeExecutionStepID(step.ID)
		if err != nil {
			return PlanUpdate{}, fmt.Errorf("steps[%d].id: %w", idx, err)
		}
		if stepID == "" {
			stepID = ExecutionPlanStepID(idx)
		}
		if previous, ok := seenIDs[stepID]; ok {
			return PlanUpdate{}, fmt.Errorf("steps[%d].id duplicates steps[%d].id (%s)", idx, previous, stepID)
		}
		seenIDs[stepID] = idx
		status := strings.TrimSpace(strings.ToLower(step.Status))
		if status == "" {
			status = StepStatusPending
		}
		switch status {
		case StepStatusPending, StepStatusInProgress, StepStatusCompleted, StepStatusCancelled:
		default:
			return PlanUpdate{}, fmt.Errorf("steps[%d].status must be one of pending, in_progress, completed, cancelled", idx)
		}
		if status == StepStatusInProgress {
			inProgressCount++
		}
		covers := uniqueNonEmptyStrings(step.Covers)
		for _, criterionID := range covers {
			if _, ok := allowedCriteria[criterionID]; !ok {
				return PlanUpdate{}, fmt.Errorf("steps[%d].covers references unknown criterion %s", idx, criterionID)
			}
		}
		priority, err := normalizeExecutionStepPriority(step.Priority)
		if err != nil {
			return PlanUpdate{}, fmt.Errorf("steps[%d].priority: %w", idx, err)
		}
		normalized.Steps = append(normalized.Steps, ExecutionPlanStep{
			ID:       stepID,
			Priority: priority,
			Title:    title,
			Status:   status,
			Covers:   covers,
			Notes:    strings.TrimSpace(step.Notes),
		})
	}
	for idx, step := range update.Steps {
		parentStepID, err := normalizeExecutionStepID(step.ParentStepID)
		if err != nil {
			return PlanUpdate{}, fmt.Errorf("steps[%d].parent_step_id: %w", idx, err)
		}
		if parentStepID != "" {
			if parentStepID == normalized.Steps[idx].ID {
				return PlanUpdate{}, fmt.Errorf("steps[%d].parent_step_id may not point to itself", idx)
			}
			if _, ok := seenIDs[parentStepID]; !ok {
				return PlanUpdate{}, fmt.Errorf("steps[%d].parent_step_id references unknown step %s", idx, parentStepID)
			}
		}
		dependsOn, err := normalizeExecutionStepRefs(step.DependsOn)
		if err != nil {
			return PlanUpdate{}, fmt.Errorf("steps[%d].depends_on: %w", idx, err)
		}
		for _, dep := range dependsOn {
			if dep == normalized.Steps[idx].ID {
				return PlanUpdate{}, fmt.Errorf("steps[%d].depends_on may not reference itself", idx)
			}
			if _, ok := seenIDs[dep]; !ok {
				return PlanUpdate{}, fmt.Errorf("steps[%d].depends_on references unknown step %s", idx, dep)
			}
		}
		normalized.Steps[idx].ParentStepID = parentStepID
		normalized.Steps[idx].DependsOn = dependsOn
	}
	if inProgressCount > 1 {
		return PlanUpdate{}, errors.New("execution plan may not have more than one in_progress step")
	}
	if err := validateExecutionParentGraph(normalized.Steps); err != nil {
		return PlanUpdate{}, err
	}
	if err := validateExecutionDependencyGraph(normalized.Steps); err != nil {
		return PlanUpdate{}, err
	}
	return normalized, nil
}

func NormalizePlanPatch(patch PlanPatch) (PlanPatch, error) {
	if len(patch.Operations) == 0 {
		return PlanPatch{}, errors.New("plan patch must include at least one operation")
	}
	normalized := PlanPatch{
		Operations: make([]PlanPatchOperation, 0, len(patch.Operations)),
	}
	for idx, op := range patch.Operations {
		normalizedOp := PlanPatchOperation{
			Op:          strings.TrimSpace(strings.ToLower(op.Op)),
			Explanation: strings.TrimSpace(op.Explanation),
		}
		switch normalizedOp.Op {
		case PlanPatchOpSetExplanation:
			normalized.Operations = append(normalized.Operations, normalizedOp)
		case PlanPatchOpUpsertStep:
			if op.Step == nil {
				return PlanPatch{}, fmt.Errorf("operations[%d].step is required for upsert_step", idx)
			}
			stepID, err := normalizeExecutionStepID(op.Step.ID)
			if err != nil {
				return PlanPatch{}, fmt.Errorf("operations[%d].step.id: %w", idx, err)
			}
			if stepID == "" {
				return PlanPatch{}, fmt.Errorf("operations[%d].step.id is required for upsert_step", idx)
			}
			afterStepID, err := normalizeExecutionStepID(op.AfterStepID)
			if err != nil {
				return PlanPatch{}, fmt.Errorf("operations[%d].after_step_id: %w", idx, err)
			}
			if afterStepID != "" && afterStepID == stepID {
				return PlanPatch{}, fmt.Errorf("operations[%d].after_step_id may not point to the same step", idx)
			}
			parentStepID, err := normalizeExecutionStepID(op.Step.ParentStepID)
			if err != nil {
				return PlanPatch{}, fmt.Errorf("operations[%d].step.parent_step_id: %w", idx, err)
			}
			if parentStepID != "" && parentStepID == stepID {
				return PlanPatch{}, fmt.Errorf("operations[%d].step.parent_step_id may not point to the same step", idx)
			}
			dependsOn, err := normalizeExecutionStepRefs(op.Step.DependsOn)
			if err != nil {
				return PlanPatch{}, fmt.Errorf("operations[%d].step.depends_on: %w", idx, err)
			}
			for _, dep := range dependsOn {
				if dep == stepID {
					return PlanPatch{}, fmt.Errorf("operations[%d].step.depends_on may not reference the same step", idx)
				}
			}
			status := strings.TrimSpace(strings.ToLower(op.Step.Status))
			if status == "" {
				status = StepStatusPending
			}
			switch status {
			case StepStatusPending, StepStatusInProgress, StepStatusCompleted, StepStatusCancelled:
			default:
				return PlanPatch{}, fmt.Errorf("operations[%d].step.status must be one of pending, in_progress, completed, cancelled", idx)
			}
			priority, err := normalizeExecutionStepPriority(op.Step.Priority)
			if err != nil {
				return PlanPatch{}, fmt.Errorf("operations[%d].step.priority: %w", idx, err)
			}
			title := strings.TrimSpace(op.Step.Title)
			if title == "" {
				return PlanPatch{}, fmt.Errorf("operations[%d].step.title is required for upsert_step", idx)
			}
			normalizedOp.AfterStepID = afterStepID
			normalizedOp.Step = &ExecutionPlanStep{
				ID:           stepID,
				ParentStepID: parentStepID,
				DependsOn:    dependsOn,
				Priority:     priority,
				Title:        title,
				Status:       status,
				Covers:       uniqueNonEmptyStrings(op.Step.Covers),
				Notes:        strings.TrimSpace(op.Step.Notes),
			}
			normalized.Operations = append(normalized.Operations, normalizedOp)
		case PlanPatchOpRemoveStep:
			stepID, err := normalizeExecutionStepID(op.StepID)
			if err != nil {
				return PlanPatch{}, fmt.Errorf("operations[%d].step_id: %w", idx, err)
			}
			if stepID == "" {
				return PlanPatch{}, fmt.Errorf("operations[%d].step_id is required for remove_step", idx)
			}
			normalizedOp.StepID = stepID
			normalized.Operations = append(normalized.Operations, normalizedOp)
		default:
			return PlanPatch{}, fmt.Errorf("operations[%d].op must be one of %s, %s, %s", idx, PlanPatchOpSetExplanation, PlanPatchOpUpsertStep, PlanPatchOpRemoveStep)
		}
	}
	return normalized, nil
}

func ApplyPlanPatch(base PlanUpdate, patch PlanPatch) (PlanUpdate, error) {
	current := PlanUpdate{
		Explanation: strings.TrimSpace(base.Explanation),
		Steps:       cloneExecutionPlanSteps(base.Steps),
	}
	for idx, op := range patch.Operations {
		switch op.Op {
		case PlanPatchOpSetExplanation:
			current.Explanation = op.Explanation
		case PlanPatchOpUpsertStep:
			if op.Step == nil {
				return PlanUpdate{}, fmt.Errorf("operations[%d].step is required for upsert_step", idx)
			}
			step := cloneExecutionPlanStep(*op.Step)
			existingIndex := executionPlanStepIndex(current.Steps, step.ID)
			if existingIndex >= 0 {
				current.Steps = append(current.Steps[:existingIndex], current.Steps[existingIndex+1:]...)
			}
			insertIndex := len(current.Steps)
			switch {
			case strings.TrimSpace(op.AfterStepID) != "":
				afterIndex := executionPlanStepIndex(current.Steps, op.AfterStepID)
				if afterIndex < 0 {
					return PlanUpdate{}, fmt.Errorf("operations[%d].after_step_id references unknown step %s", idx, op.AfterStepID)
				}
				insertIndex = afterIndex + 1
			case existingIndex >= 0:
				insertIndex = existingIndex
				if insertIndex > len(current.Steps) {
					insertIndex = len(current.Steps)
				}
			}
			current.Steps = insertExecutionPlanStep(current.Steps, insertIndex, step)
		case PlanPatchOpRemoveStep:
			removeIndex := executionPlanStepIndex(current.Steps, op.StepID)
			if removeIndex < 0 {
				return PlanUpdate{}, fmt.Errorf("operations[%d].step_id references unknown step %s", idx, op.StepID)
			}
			current.Steps = append(current.Steps[:removeIndex], current.Steps[removeIndex+1:]...)
		default:
			return PlanUpdate{}, fmt.Errorf("operations[%d].op must be one of %s, %s, %s", idx, PlanPatchOpSetExplanation, PlanPatchOpUpsertStep, PlanPatchOpRemoveStep)
		}
	}
	return current, nil
}

func NormalizeProjectUpdate(update ProjectUpdate) (ProjectUpdate, error) {
	normalized := ProjectUpdate{
		Explanation: strings.TrimSpace(update.Explanation),
		Steps:       make([]ProjectExecutionStep, 0, len(update.Steps)),
		Branches:    make([]ProjectBranchSpec, 0, len(update.Branches)),
	}
	branchSeen := make(map[string]int, len(update.Branches))
	for idx, branch := range update.Branches {
		title := strings.TrimSpace(branch.Title)
		if title == "" {
			return ProjectUpdate{}, fmt.Errorf("branches[%d].title is required", idx)
		}
		branchID, err := normalizeExecutionStepID(branch.ID)
		if err != nil {
			return ProjectUpdate{}, fmt.Errorf("branches[%d].id: %w", idx, err)
		}
		if branchID == "" {
			branchID = ProjectBranchID(idx)
		}
		if previous, ok := branchSeen[branchID]; ok {
			return ProjectUpdate{}, fmt.Errorf("branches[%d].id duplicates branches[%d].id (%s)", idx, previous, branchID)
		}
		branchSeen[branchID] = idx
		status, err := normalizeProjectBranchStatus(branch.Status)
		if err != nil {
			return ProjectUpdate{}, fmt.Errorf("branches[%d].status: %w", idx, err)
		}
		normalized.Branches = append(normalized.Branches, ProjectBranchSpec{
			ID:     branchID,
			Title:  title,
			Status: status,
			TaskID: strings.TrimSpace(branch.TaskID),
			Notes:  strings.TrimSpace(branch.Notes),
		})
	}
	stepSeen := make(map[string]int, len(update.Steps))
	for idx, step := range update.Steps {
		title := strings.TrimSpace(step.Title)
		if title == "" {
			return ProjectUpdate{}, fmt.Errorf("steps[%d].title is required", idx)
		}
		stepID, err := normalizeExecutionStepID(step.ID)
		if err != nil {
			return ProjectUpdate{}, fmt.Errorf("steps[%d].id: %w", idx, err)
		}
		if stepID == "" {
			stepID = ProjectStepID(idx)
		}
		if previous, ok := stepSeen[stepID]; ok {
			return ProjectUpdate{}, fmt.Errorf("steps[%d].id duplicates steps[%d].id (%s)", idx, previous, stepID)
		}
		stepSeen[stepID] = idx
		status, err := normalizeProjectStepStatus(step.Status)
		if err != nil {
			return ProjectUpdate{}, fmt.Errorf("steps[%d].status: %w", idx, err)
		}
		priority, err := normalizeExecutionStepPriority(step.Priority)
		if err != nil {
			return ProjectUpdate{}, fmt.Errorf("steps[%d].priority: %w", idx, err)
		}
		branchID, err := normalizeExecutionStepID(step.BranchID)
		if err != nil {
			return ProjectUpdate{}, fmt.Errorf("steps[%d].branch_id: %w", idx, err)
		}
		if branchID != "" {
			if _, ok := branchSeen[branchID]; !ok {
				return ProjectUpdate{}, fmt.Errorf("steps[%d].branch_id references unknown branch %s", idx, branchID)
			}
		}
		normalized.Steps = append(normalized.Steps, ProjectExecutionStep{
			ID:       stepID,
			Priority: priority,
			Title:    title,
			Status:   status,
			BranchID: branchID,
			TaskID:   strings.TrimSpace(step.TaskID),
			Notes:    strings.TrimSpace(step.Notes),
		})
	}
	for idx, step := range update.Steps {
		parentStepID, err := normalizeExecutionStepID(step.ParentStepID)
		if err != nil {
			return ProjectUpdate{}, fmt.Errorf("steps[%d].parent_step_id: %w", idx, err)
		}
		if parentStepID != "" {
			if parentStepID == normalized.Steps[idx].ID {
				return ProjectUpdate{}, fmt.Errorf("steps[%d].parent_step_id may not point to itself", idx)
			}
			if _, ok := stepSeen[parentStepID]; !ok {
				return ProjectUpdate{}, fmt.Errorf("steps[%d].parent_step_id references unknown step %s", idx, parentStepID)
			}
		}
		dependsOn, err := normalizeExecutionStepRefs(step.DependsOn)
		if err != nil {
			return ProjectUpdate{}, fmt.Errorf("steps[%d].depends_on: %w", idx, err)
		}
		for _, dep := range dependsOn {
			if dep == normalized.Steps[idx].ID {
				return ProjectUpdate{}, fmt.Errorf("steps[%d].depends_on may not reference itself", idx)
			}
			if _, ok := stepSeen[dep]; !ok {
				return ProjectUpdate{}, fmt.Errorf("steps[%d].depends_on references unknown step %s", idx, dep)
			}
		}
		normalized.Steps[idx].ParentStepID = parentStepID
		normalized.Steps[idx].DependsOn = dependsOn
	}
	if err := validateProjectParentGraph(normalized.Steps); err != nil {
		return ProjectUpdate{}, err
	}
	if err := validateProjectDependencyGraph(normalized.Steps); err != nil {
		return ProjectUpdate{}, err
	}
	return normalized, nil
}

func NormalizeProjectPatch(patch ProjectPatch) (ProjectPatch, error) {
	if len(patch.Operations) == 0 {
		return ProjectPatch{}, errors.New("project patch must include at least one operation")
	}
	normalized := ProjectPatch{
		Operations: make([]ProjectPatchOperation, 0, len(patch.Operations)),
	}
	for idx, op := range patch.Operations {
		normalizedOp := ProjectPatchOperation{
			Op:          strings.TrimSpace(strings.ToLower(op.Op)),
			Explanation: strings.TrimSpace(op.Explanation),
			TaskID:      strings.TrimSpace(op.TaskID),
		}
		switch normalizedOp.Op {
		case ProjectPatchOpSetExplanation:
			normalized.Operations = append(normalized.Operations, normalizedOp)
		case ProjectPatchOpUpsertStep:
			if op.Step == nil {
				return ProjectPatch{}, fmt.Errorf("operations[%d].step is required for upsert_step", idx)
			}
			stepID, err := normalizeExecutionStepID(op.Step.ID)
			if err != nil {
				return ProjectPatch{}, fmt.Errorf("operations[%d].step.id: %w", idx, err)
			}
			if stepID == "" {
				return ProjectPatch{}, fmt.Errorf("operations[%d].step.id is required for upsert_step", idx)
			}
			afterStepID, err := normalizeExecutionStepID(op.AfterStepID)
			if err != nil {
				return ProjectPatch{}, fmt.Errorf("operations[%d].after_step_id: %w", idx, err)
			}
			if afterStepID != "" && afterStepID == stepID {
				return ProjectPatch{}, fmt.Errorf("operations[%d].after_step_id may not point to the same step", idx)
			}
			parentStepID, err := normalizeExecutionStepID(op.Step.ParentStepID)
			if err != nil {
				return ProjectPatch{}, fmt.Errorf("operations[%d].step.parent_step_id: %w", idx, err)
			}
			if parentStepID != "" && parentStepID == stepID {
				return ProjectPatch{}, fmt.Errorf("operations[%d].step.parent_step_id may not point to the same step", idx)
			}
			dependsOn, err := normalizeExecutionStepRefs(op.Step.DependsOn)
			if err != nil {
				return ProjectPatch{}, fmt.Errorf("operations[%d].step.depends_on: %w", idx, err)
			}
			for _, dep := range dependsOn {
				if dep == stepID {
					return ProjectPatch{}, fmt.Errorf("operations[%d].step.depends_on may not reference the same step", idx)
				}
			}
			status, err := normalizeProjectStepStatus(op.Step.Status)
			if err != nil {
				return ProjectPatch{}, fmt.Errorf("operations[%d].step.status: %w", idx, err)
			}
			priority, err := normalizeExecutionStepPriority(op.Step.Priority)
			if err != nil {
				return ProjectPatch{}, fmt.Errorf("operations[%d].step.priority: %w", idx, err)
			}
			branchID, err := normalizeExecutionStepID(op.Step.BranchID)
			if err != nil {
				return ProjectPatch{}, fmt.Errorf("operations[%d].step.branch_id: %w", idx, err)
			}
			title := strings.TrimSpace(op.Step.Title)
			if title == "" {
				return ProjectPatch{}, fmt.Errorf("operations[%d].step.title is required for upsert_step", idx)
			}
			normalizedOp.AfterStepID = afterStepID
			normalizedOp.Step = &ProjectExecutionStep{
				ID:           stepID,
				ParentStepID: parentStepID,
				DependsOn:    dependsOn,
				Priority:     priority,
				Title:        title,
				Status:       status,
				BranchID:     branchID,
				TaskID:       strings.TrimSpace(op.Step.TaskID),
				Notes:        strings.TrimSpace(op.Step.Notes),
			}
			normalized.Operations = append(normalized.Operations, normalizedOp)
		case ProjectPatchOpRemoveStep:
			stepID, err := normalizeExecutionStepID(op.StepID)
			if err != nil {
				return ProjectPatch{}, fmt.Errorf("operations[%d].step_id: %w", idx, err)
			}
			if stepID == "" {
				return ProjectPatch{}, fmt.Errorf("operations[%d].step_id is required for remove_step", idx)
			}
			normalizedOp.StepID = stepID
			normalized.Operations = append(normalized.Operations, normalizedOp)
		case ProjectPatchOpSetStepDependsOn:
			stepID, err := normalizeExecutionStepID(op.StepID)
			if err != nil {
				return ProjectPatch{}, fmt.Errorf("operations[%d].step_id: %w", idx, err)
			}
			if stepID == "" {
				return ProjectPatch{}, fmt.Errorf("operations[%d].step_id is required for set_step_dependencies", idx)
			}
			dependsOn, err := normalizeExecutionStepRefs(op.DependsOn)
			if err != nil {
				return ProjectPatch{}, fmt.Errorf("operations[%d].depends_on: %w", idx, err)
			}
			for _, dep := range dependsOn {
				if dep == stepID {
					return ProjectPatch{}, fmt.Errorf("operations[%d].depends_on may not reference the same step", idx)
				}
			}
			normalizedOp.StepID = stepID
			normalizedOp.DependsOn = dependsOn
			normalized.Operations = append(normalized.Operations, normalizedOp)
		case ProjectPatchOpSetStepParent:
			stepID, err := normalizeExecutionStepID(op.StepID)
			if err != nil {
				return ProjectPatch{}, fmt.Errorf("operations[%d].step_id: %w", idx, err)
			}
			if stepID == "" {
				return ProjectPatch{}, fmt.Errorf("operations[%d].step_id is required for set_step_parent", idx)
			}
			parentStepID, err := normalizeExecutionStepID(op.ParentStepID)
			if err != nil {
				return ProjectPatch{}, fmt.Errorf("operations[%d].parent_step_id: %w", idx, err)
			}
			if parentStepID != "" && parentStepID == stepID {
				return ProjectPatch{}, fmt.Errorf("operations[%d].parent_step_id may not point to the same step", idx)
			}
			normalizedOp.StepID = stepID
			normalizedOp.ParentStepID = parentStepID
			normalized.Operations = append(normalized.Operations, normalizedOp)
		case ProjectPatchOpBindStepBranch:
			stepID, err := normalizeExecutionStepID(op.StepID)
			if err != nil {
				return ProjectPatch{}, fmt.Errorf("operations[%d].step_id: %w", idx, err)
			}
			if stepID == "" {
				return ProjectPatch{}, fmt.Errorf("operations[%d].step_id is required for bind_step_branch", idx)
			}
			branchID, err := normalizeExecutionStepID(op.BranchID)
			if err != nil {
				return ProjectPatch{}, fmt.Errorf("operations[%d].branch_id: %w", idx, err)
			}
			normalizedOp.StepID = stepID
			normalizedOp.BranchID = branchID
			normalized.Operations = append(normalized.Operations, normalizedOp)
		case ProjectPatchOpBindStepTask:
			stepID, err := normalizeExecutionStepID(op.StepID)
			if err != nil {
				return ProjectPatch{}, fmt.Errorf("operations[%d].step_id: %w", idx, err)
			}
			if stepID == "" {
				return ProjectPatch{}, fmt.Errorf("operations[%d].step_id is required for bind_step_task", idx)
			}
			normalizedOp.StepID = stepID
			normalized.Operations = append(normalized.Operations, normalizedOp)
		case ProjectPatchOpUpsertBranch:
			if op.Branch == nil {
				return ProjectPatch{}, fmt.Errorf("operations[%d].branch is required for upsert_branch", idx)
			}
			branchID, err := normalizeExecutionStepID(op.Branch.ID)
			if err != nil {
				return ProjectPatch{}, fmt.Errorf("operations[%d].branch.id: %w", idx, err)
			}
			if branchID == "" {
				return ProjectPatch{}, fmt.Errorf("operations[%d].branch.id is required for upsert_branch", idx)
			}
			status, err := normalizeProjectBranchStatus(op.Branch.Status)
			if err != nil {
				return ProjectPatch{}, fmt.Errorf("operations[%d].branch.status: %w", idx, err)
			}
			title := strings.TrimSpace(op.Branch.Title)
			if title == "" {
				return ProjectPatch{}, fmt.Errorf("operations[%d].branch.title is required for upsert_branch", idx)
			}
			normalizedOp.Branch = &ProjectBranchSpec{
				ID:     branchID,
				Title:  title,
				Status: status,
				TaskID: strings.TrimSpace(op.Branch.TaskID),
				Notes:  strings.TrimSpace(op.Branch.Notes),
			}
			normalized.Operations = append(normalized.Operations, normalizedOp)
		case ProjectPatchOpRemoveBranch:
			branchID, err := normalizeExecutionStepID(op.BranchID)
			if err != nil {
				return ProjectPatch{}, fmt.Errorf("operations[%d].branch_id: %w", idx, err)
			}
			if branchID == "" {
				return ProjectPatch{}, fmt.Errorf("operations[%d].branch_id is required for remove_branch", idx)
			}
			normalizedOp.BranchID = branchID
			normalized.Operations = append(normalized.Operations, normalizedOp)
		case ProjectPatchOpBindBranchTask:
			branchID, err := normalizeExecutionStepID(op.BranchID)
			if err != nil {
				return ProjectPatch{}, fmt.Errorf("operations[%d].branch_id: %w", idx, err)
			}
			if branchID == "" {
				return ProjectPatch{}, fmt.Errorf("operations[%d].branch_id is required for bind_branch_task", idx)
			}
			normalizedOp.BranchID = branchID
			normalized.Operations = append(normalized.Operations, normalizedOp)
		case ProjectPatchOpSetBranchStatus:
			branchID, err := normalizeExecutionStepID(op.BranchID)
			if err != nil {
				return ProjectPatch{}, fmt.Errorf("operations[%d].branch_id: %w", idx, err)
			}
			if branchID == "" {
				return ProjectPatch{}, fmt.Errorf("operations[%d].branch_id is required for set_branch_status", idx)
			}
			status, err := normalizeProjectBranchStatus(op.Status)
			if err != nil {
				return ProjectPatch{}, fmt.Errorf("operations[%d].status: %w", idx, err)
			}
			normalizedOp.BranchID = branchID
			normalizedOp.Status = status
			normalized.Operations = append(normalized.Operations, normalizedOp)
		default:
			return ProjectPatch{}, fmt.Errorf(
				"operations[%d].op must be one of %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s",
				idx,
				ProjectPatchOpSetExplanation,
				ProjectPatchOpUpsertStep,
				ProjectPatchOpRemoveStep,
				ProjectPatchOpSetStepDependsOn,
				ProjectPatchOpSetStepParent,
				ProjectPatchOpBindStepBranch,
				ProjectPatchOpBindStepTask,
				ProjectPatchOpUpsertBranch,
				ProjectPatchOpRemoveBranch,
				ProjectPatchOpBindBranchTask,
				ProjectPatchOpSetBranchStatus,
			)
		}
	}
	return normalized, nil
}

func ApplyProjectPatch(base ProjectUpdate, patch ProjectPatch) (ProjectUpdate, error) {
	current := ProjectUpdate{
		Explanation: strings.TrimSpace(base.Explanation),
		Steps:       cloneProjectExecutionSteps(base.Steps),
		Branches:    cloneProjectBranchSpecs(base.Branches),
	}
	for idx, op := range patch.Operations {
		switch op.Op {
		case ProjectPatchOpSetExplanation:
			current.Explanation = op.Explanation
		case ProjectPatchOpUpsertStep:
			if op.Step == nil {
				return ProjectUpdate{}, fmt.Errorf("operations[%d].step is required for upsert_step", idx)
			}
			step := cloneProjectExecutionStep(*op.Step)
			existingIndex := projectExecutionStepIndex(current.Steps, step.ID)
			if existingIndex >= 0 {
				current.Steps = append(current.Steps[:existingIndex], current.Steps[existingIndex+1:]...)
			}
			insertIndex := len(current.Steps)
			switch {
			case strings.TrimSpace(op.AfterStepID) != "":
				afterIndex := projectExecutionStepIndex(current.Steps, op.AfterStepID)
				if afterIndex < 0 {
					return ProjectUpdate{}, fmt.Errorf("operations[%d].after_step_id references unknown step %s", idx, op.AfterStepID)
				}
				insertIndex = afterIndex + 1
			case existingIndex >= 0:
				insertIndex = existingIndex
				if insertIndex > len(current.Steps) {
					insertIndex = len(current.Steps)
				}
			}
			current.Steps = insertProjectExecutionStep(current.Steps, insertIndex, step)
		case ProjectPatchOpRemoveStep:
			removeIndex := projectExecutionStepIndex(current.Steps, op.StepID)
			if removeIndex < 0 {
				return ProjectUpdate{}, fmt.Errorf("operations[%d].step_id references unknown step %s", idx, op.StepID)
			}
			current.Steps = append(current.Steps[:removeIndex], current.Steps[removeIndex+1:]...)
		case ProjectPatchOpSetStepDependsOn:
			stepIndex := projectExecutionStepIndex(current.Steps, op.StepID)
			if stepIndex < 0 {
				return ProjectUpdate{}, fmt.Errorf("operations[%d].step_id references unknown step %s", idx, op.StepID)
			}
			current.Steps[stepIndex].DependsOn = append([]string(nil), op.DependsOn...)
		case ProjectPatchOpSetStepParent:
			stepIndex := projectExecutionStepIndex(current.Steps, op.StepID)
			if stepIndex < 0 {
				return ProjectUpdate{}, fmt.Errorf("operations[%d].step_id references unknown step %s", idx, op.StepID)
			}
			current.Steps[stepIndex].ParentStepID = op.ParentStepID
		case ProjectPatchOpBindStepBranch:
			stepIndex := projectExecutionStepIndex(current.Steps, op.StepID)
			if stepIndex < 0 {
				return ProjectUpdate{}, fmt.Errorf("operations[%d].step_id references unknown step %s", idx, op.StepID)
			}
			current.Steps[stepIndex].BranchID = op.BranchID
		case ProjectPatchOpBindStepTask:
			stepIndex := projectExecutionStepIndex(current.Steps, op.StepID)
			if stepIndex < 0 {
				return ProjectUpdate{}, fmt.Errorf("operations[%d].step_id references unknown step %s", idx, op.StepID)
			}
			current.Steps[stepIndex].TaskID = op.TaskID
		case ProjectPatchOpUpsertBranch:
			if op.Branch == nil {
				return ProjectUpdate{}, fmt.Errorf("operations[%d].branch is required for upsert_branch", idx)
			}
			branch := cloneProjectBranchSpec(*op.Branch)
			existingIndex := projectBranchSpecIndex(current.Branches, branch.ID)
			if existingIndex >= 0 {
				current.Branches[existingIndex] = branch
			} else {
				current.Branches = append(current.Branches, branch)
			}
		case ProjectPatchOpRemoveBranch:
			removeIndex := projectBranchSpecIndex(current.Branches, op.BranchID)
			if removeIndex < 0 {
				return ProjectUpdate{}, fmt.Errorf("operations[%d].branch_id references unknown branch %s", idx, op.BranchID)
			}
			current.Branches = append(current.Branches[:removeIndex], current.Branches[removeIndex+1:]...)
		case ProjectPatchOpBindBranchTask:
			branchIndex := projectBranchSpecIndex(current.Branches, op.BranchID)
			if branchIndex < 0 {
				return ProjectUpdate{}, fmt.Errorf("operations[%d].branch_id references unknown branch %s", idx, op.BranchID)
			}
			current.Branches[branchIndex].TaskID = op.TaskID
		case ProjectPatchOpSetBranchStatus:
			branchIndex := projectBranchSpecIndex(current.Branches, op.BranchID)
			if branchIndex < 0 {
				return ProjectUpdate{}, fmt.Errorf("operations[%d].branch_id references unknown branch %s", idx, op.BranchID)
			}
			current.Branches[branchIndex].Status = op.Status
		default:
			return ProjectUpdate{}, fmt.Errorf("operations[%d].op is not supported", idx)
		}
	}
	return current, nil
}

func cloneProjectExecutionSteps(steps []ProjectExecutionStep) []ProjectExecutionStep {
	out := make([]ProjectExecutionStep, 0, len(steps))
	for _, step := range steps {
		out = append(out, cloneProjectExecutionStep(step))
	}
	return out
}

func cloneProjectExecutionStep(step ProjectExecutionStep) ProjectExecutionStep {
	return ProjectExecutionStep{
		ID:           step.ID,
		ParentStepID: step.ParentStepID,
		DependsOn:    append([]string(nil), step.DependsOn...),
		Priority:     step.Priority,
		Title:        step.Title,
		Status:       step.Status,
		BranchID:     step.BranchID,
		TaskID:       step.TaskID,
		Notes:        step.Notes,
	}
}

func cloneProjectBranchSpecs(branches []ProjectBranchSpec) []ProjectBranchSpec {
	out := make([]ProjectBranchSpec, 0, len(branches))
	for _, branch := range branches {
		out = append(out, cloneProjectBranchSpec(branch))
	}
	return out
}

func cloneProjectBranchSpec(branch ProjectBranchSpec) ProjectBranchSpec {
	return ProjectBranchSpec{
		ID:     branch.ID,
		Title:  branch.Title,
		Status: branch.Status,
		TaskID: branch.TaskID,
		Notes:  branch.Notes,
	}
}

func projectExecutionStepIndex(steps []ProjectExecutionStep, stepID string) int {
	for idx, step := range steps {
		if strings.TrimSpace(step.ID) == strings.TrimSpace(stepID) {
			return idx
		}
	}
	return -1
}

func projectBranchSpecIndex(branches []ProjectBranchSpec, branchID string) int {
	for idx, branch := range branches {
		if strings.TrimSpace(branch.ID) == strings.TrimSpace(branchID) {
			return idx
		}
	}
	return -1
}

func insertProjectExecutionStep(steps []ProjectExecutionStep, index int, step ProjectExecutionStep) []ProjectExecutionStep {
	if index < 0 {
		index = 0
	}
	if index > len(steps) {
		index = len(steps)
	}
	steps = append(steps, ProjectExecutionStep{})
	copy(steps[index+1:], steps[index:])
	steps[index] = step
	return steps
}

func ProjectStepID(index int) string {
	return fmt.Sprintf("PRJ-STEP-%03d", index+1)
}

func ProjectBranchID(index int) string {
	return fmt.Sprintf("PRJ-BRANCH-%03d", index+1)
}

func normalizeProjectStepStatus(status string) (string, error) {
	switch normalized := strings.TrimSpace(strings.ToLower(status)); normalized {
	case "", ProjectStepStatusPending:
		return ProjectStepStatusPending, nil
	case ProjectStepStatusInProgress, ProjectStepStatusBlocked, ProjectStepStatusCompleted, ProjectStepStatusCancelled:
		return normalized, nil
	default:
		return "", fmt.Errorf("must be one of %s, %s, %s, %s, %s", ProjectStepStatusPending, ProjectStepStatusInProgress, ProjectStepStatusBlocked, ProjectStepStatusCompleted, ProjectStepStatusCancelled)
	}
}

func normalizeProjectBranchStatus(status string) (string, error) {
	switch normalized := strings.TrimSpace(strings.ToLower(status)); normalized {
	case "", ProjectBranchStatusPending:
		return ProjectBranchStatusPending, nil
	case ProjectBranchStatusActive, ProjectBranchStatusBlocked, ProjectBranchStatusCompleted, ProjectBranchStatusCancelled:
		return normalized, nil
	default:
		return "", fmt.Errorf("must be one of %s, %s, %s, %s, %s", ProjectBranchStatusPending, ProjectBranchStatusActive, ProjectBranchStatusBlocked, ProjectBranchStatusCompleted, ProjectBranchStatusCancelled)
	}
}

func validateProjectParentGraph(steps []ProjectExecutionStep) error {
	parentByID := make(map[string]string, len(steps))
	for _, step := range steps {
		parentByID[step.ID] = step.ParentStepID
	}
	for _, step := range steps {
		seen := map[string]struct{}{step.ID: {}}
		parentID := parentByID[step.ID]
		for parentID != "" {
			if _, ok := seen[parentID]; ok {
				return fmt.Errorf("project parent graph contains a cycle at %s", step.ID)
			}
			seen[parentID] = struct{}{}
			parentID = parentByID[parentID]
		}
	}
	return nil
}

func validateProjectDependencyGraph(steps []ProjectExecutionStep) error {
	byID := make(map[string]ProjectExecutionStep, len(steps))
	for _, step := range steps {
		byID[step.ID] = step
	}
	visiting := make(map[string]bool, len(steps))
	visited := make(map[string]bool, len(steps))
	var visit func(string) error
	visit = func(stepID string) error {
		if visited[stepID] {
			return nil
		}
		if visiting[stepID] {
			return fmt.Errorf("project depends_on graph contains a cycle at %s", stepID)
		}
		visiting[stepID] = true
		step := byID[stepID]
		for _, dep := range step.DependsOn {
			if err := visit(dep); err != nil {
				return err
			}
		}
		delete(visiting, stepID)
		visited[stepID] = true
		return nil
	}
	for _, step := range steps {
		if err := visit(step.ID); err != nil {
			return err
		}
	}
	return nil
}

func cloneExecutionPlanSteps(steps []ExecutionPlanStep) []ExecutionPlanStep {
	out := make([]ExecutionPlanStep, 0, len(steps))
	for _, step := range steps {
		out = append(out, cloneExecutionPlanStep(step))
	}
	return out
}

func cloneExecutionPlanStep(step ExecutionPlanStep) ExecutionPlanStep {
	return ExecutionPlanStep{
		ID:           step.ID,
		ParentStepID: step.ParentStepID,
		DependsOn:    append([]string(nil), step.DependsOn...),
		Priority:     step.Priority,
		Title:        step.Title,
		Status:       step.Status,
		Covers:       append([]string(nil), step.Covers...),
		Notes:        step.Notes,
	}
}

func executionPlanStepIndex(steps []ExecutionPlanStep, stepID string) int {
	for idx, step := range steps {
		if strings.TrimSpace(step.ID) == strings.TrimSpace(stepID) {
			return idx
		}
	}
	return -1
}

func insertExecutionPlanStep(steps []ExecutionPlanStep, index int, step ExecutionPlanStep) []ExecutionPlanStep {
	if index < 0 {
		index = 0
	}
	if index > len(steps) {
		index = len(steps)
	}
	steps = append(steps, ExecutionPlanStep{})
	copy(steps[index+1:], steps[index:])
	steps[index] = step
	return steps
}

func ExecutionPlanStepID(index int) string {
	return fmt.Sprintf("STEP-EXEC-%03d", index+1)
}

func IsExecutionStep(step Step) bool {
	return strings.EqualFold(strings.TrimSpace(step.Kind), StepKindExecution) || strings.EqualFold(strings.TrimSpace(step.Source), StepSourceOperator) || strings.EqualFold(strings.TrimSpace(step.Source), StepSourceProvider)
}

func NormalizePlanStep(step Step, fallbackUpdatedAt string) Step {
	if strings.TrimSpace(step.Status) == "" {
		step.Status = StepStatusPending
	}
	step.Status = strings.TrimSpace(strings.ToLower(step.Status))
	switch step.Status {
	case StepStatusPending, StepStatusInProgress, StepStatusCompleted, StepStatusCancelled:
	default:
		step.Status = StepStatusPending
	}
	if IsExecutionStep(step) {
		step.Kind = StepKindExecution
		if strings.TrimSpace(step.Source) == "" {
			step.Source = StepSourceOperator
		}
		if strings.TrimSpace(step.ID) == "" {
			step.ID = ExecutionPlanStepID(0)
		}
		if priority, err := normalizeExecutionStepPriority(step.Priority); err == nil {
			step.Priority = priority
		} else {
			step.Priority = StepPriorityMedium
		}
		step.ParentStepID = strings.TrimSpace(step.ParentStepID)
		step.DependsOn = uniqueNonEmptyStrings(step.DependsOn)
	} else {
		if strings.TrimSpace(step.Source) == "" {
			step.Source = StepSourceSystem
		}
		step.ParentStepID = ""
		step.DependsOn = nil
		step.Priority = ""
	}
	step.Title = strings.TrimSpace(step.Title)
	step.Notes = strings.TrimSpace(step.Notes)
	step.Covers = uniqueNonEmptyStrings(step.Covers)
	step.Verifier = uniqueNonEmptyStrings(step.Verifier)
	step.EvidenceRefs = uniqueNonEmptyStrings(step.EvidenceRefs)
	if strings.TrimSpace(step.UpdatedAt) == "" {
		step.UpdatedAt = fallbackUpdatedAt
	}
	return step
}

func normalizeExecutionStepID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", nil
	}
	for _, r := range id {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
		case strings.ContainsRune("._:-", r):
		default:
			return "", errors.New("must contain only letters, digits, dot, underscore, colon, or dash")
		}
	}
	return id, nil
}

func normalizeExecutionStepRefs(refs []string) ([]string, error) {
	seen := make(map[string]struct{}, len(refs))
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		id, err := normalizeExecutionStepID(ref)
		if err != nil {
			return nil, err
		}
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

func normalizeExecutionStepPriority(priority string) (string, error) {
	switch normalized := strings.TrimSpace(strings.ToLower(priority)); normalized {
	case "", StepPriorityMedium:
		return StepPriorityMedium, nil
	case StepPriorityHigh, StepPriorityLow:
		return normalized, nil
	default:
		return "", fmt.Errorf("must be one of %s, %s, %s", StepPriorityHigh, StepPriorityMedium, StepPriorityLow)
	}
}

func validateExecutionParentGraph(steps []ExecutionPlanStep) error {
	parentByID := make(map[string]string, len(steps))
	for _, step := range steps {
		parentByID[step.ID] = step.ParentStepID
	}
	for _, step := range steps {
		seen := map[string]struct{}{step.ID: struct{}{}}
		parentID := parentByID[step.ID]
		for parentID != "" {
			if _, ok := seen[parentID]; ok {
				return fmt.Errorf("execution plan parent graph contains a cycle at %s", step.ID)
			}
			seen[parentID] = struct{}{}
			parentID = parentByID[parentID]
		}
	}
	return nil
}

func validateExecutionDependencyGraph(steps []ExecutionPlanStep) error {
	byID := make(map[string]ExecutionPlanStep, len(steps))
	for _, step := range steps {
		byID[step.ID] = step
	}
	visiting := make(map[string]bool, len(steps))
	visited := make(map[string]bool, len(steps))
	var visit func(string) error
	visit = func(stepID string) error {
		if visited[stepID] {
			return nil
		}
		if visiting[stepID] {
			return fmt.Errorf("execution plan depends_on graph contains a cycle at %s", stepID)
		}
		visiting[stepID] = true
		step := byID[stepID]
		for _, dep := range step.DependsOn {
			if err := visit(dep); err != nil {
				return err
			}
		}
		delete(visiting, stepID)
		visited[stepID] = true
		return nil
	}
	for _, step := range steps {
		if err := visit(step.ID); err != nil {
			return err
		}
	}
	return nil
}

func uniqueNonEmptyStrings(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func NewInitialState(spec Spec) State {
	return State{
		SchemaVersion:     SchemaVersion,
		TaskID:            spec.TaskID,
		Phase:             PhaseExplore,
		State:             StateActive,
		CurrentStepID:     "STEP-001",
		PermissionModeID:  EffectivePermissionModeID(spec.PermissionModeID),
		LastCheckpointRef: "checkpoints/0001.json",
		UpdatedAt:         Now(),
	}
}

func EffectivePermissionModeID(mode string) string {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return PermissionModeStandard
	}
	return mode
}

func NormalizeWorkerRole(role string) (string, Kind, PresetID, error) {
	switch normalized := strings.ToLower(strings.TrimSpace(role)); normalized {
	case string(KindCoding):
		return string(KindCoding), KindCoding, "", nil
	case string(KindReviewer):
		return string(KindReviewer), KindReviewer, "", nil
	case string(KindSecurityReview), "security-review":
		return string(KindSecurityReview), KindSecurityReview, "", nil
	case string(KindGeneral), "general", string(PresetDocsLite), "docs-lite":
		return string(KindGeneral), KindGeneral, PresetDocsLite, nil
	default:
		return "", "", "", fmt.Errorf("unsupported worker role: %s", strings.TrimSpace(role))
	}
}

func SupportedProviderActions() []string {
	return []string{
		"run",
		"resume",
		"respond",
		"review",
		"task_create",
		"task_update",
		"task_patch",
		"project_update",
		"project_patch",
		"memory_promote",
		"worker_spawn",
		"worker_continue",
		"wait",
		"approval_request",
		"block",
		"noop",
	}
}

func IsSupportedProviderAction(action string) bool {
	action = strings.TrimSpace(action)
	for _, supported := range SupportedProviderActions() {
		if action == supported {
			return true
		}
	}
	return false
}

func DefaultRoleContracts(cfg Config) []RoleContract {
	roles := []struct {
		role        string
		kind        Kind
		description string
		review      []string
		verify      []string
		output      string
	}{
		{
			role:        string(KindCoding),
			kind:        KindCoding,
			description: "Primary coding agent profile; owns implementation, verification, repair, review handoff, and bounded delegation through artifacts.",
			review:      []string{"review gate must consume verification, criteria, changed paths, quality diagnostics, worker evidence, and handoff artifacts"},
			verify:      []string{"coding verifier sequence must pass before Done"},
			output:      "workspace changes plus artifact-backed progress, verification, review, and handoff",
		},
		{
			role:        string(KindGeneral),
			kind:        KindGeneral,
			description: "General execution/docs-lite profile for bounded non-code or docs work that still uses the same task artifacts and review gate.",
			review:      []string{"structural review and handoff are required before Done"},
			verify:      []string{"docs-lite verifier must record artifact truth even when no code test exists"},
			output:      "artifact-backed docs/general execution result",
		},
		{
			role:        string(KindReviewer),
			kind:        KindReviewer,
			description: "Evidence-first reviewer profile; critiques artifacts and changed paths without trusting executor prose alone.",
			review:      []string{"findings must distinguish confirmed defects, missing evidence, inferred risk, and not-observed surfaces"},
			verify:      []string{"reviewer verifier may inspect repo structure and Go tests when available"},
			output:      "typed findings and a compiled review result with evidence refs",
		},
		{
			role:        string(KindSecurityReview),
			kind:        KindSecurityReview,
			description: "Security-review profile; preserves confirmed versus inferred risk boundaries and reports evidence-backed findings.",
			review:      []string{"security claims require source evidence or an explicit inferred-risk/not-observed label"},
			verify:      []string{"security verifier must preserve inventory and evidence refs"},
			output:      "security findings with affected paths, evidence refs, and risk classification",
		},
	}
	contracts := make([]RoleContract, 0, len(roles))
	for _, item := range roles {
		policy, err := ResolveSubagentRolePolicy(cfg, item.role, cfg.Permission.DefaultMode)
		if err != nil {
			policy = DefaultSubagentPolicyForTask(cfg, item.kind, "", cfg.Permission.DefaultMode)
		}
		contract := RoleContract{
			SchemaVersion:            SchemaVersion,
			RoleID:                   item.role,
			ProfileKind:              item.kind,
			Description:              item.description,
			AllowedProviderActions:   SupportedProviderActions(),
			AllowedWorkerRoles:       append([]string(nil), policy.AllowedWorkerRoles...),
			WorkspaceIsolation:       policy.WorkspaceIsolation,
			ReconcileMode:            policy.ReconcileMode,
			PermissionModeID:         policy.PermissionModeID,
			ContextSections:          []string{"task", "plan", "project", "criteria", "continuity", "sprint", "verification", "review", "completion", "worker", "session", "workspace_memory"},
			ReviewRequirements:       item.review,
			VerificationRequirements: item.verify,
			MemoryPolicy:             "workspace memory is a cross-task hint; fresh task-local artifacts win on conflict",
			OutputContract:           item.output,
		}
		contracts = append(contracts, contract)
	}
	return contracts
}

func ValidateRoleContract(contract RoleContract) (RoleContract, error) {
	if contract.SchemaVersion == 0 {
		contract.SchemaVersion = SchemaVersion
	}
	if contract.SchemaVersion != SchemaVersion {
		return RoleContract{}, fmt.Errorf("unsupported role contract schema_version: %d", contract.SchemaVersion)
	}
	role, kind, _, err := NormalizeWorkerRole(contract.RoleID)
	if err != nil {
		return RoleContract{}, fmt.Errorf("role_id: %w", err)
	}
	contract.RoleID = role
	if contract.ProfileKind == "" {
		contract.ProfileKind = kind
	}
	if contract.ProfileKind != kind {
		return RoleContract{}, fmt.Errorf("role %s profile_kind must be %s, got %s", role, kind, contract.ProfileKind)
	}
	if len(contract.AllowedProviderActions) == 0 {
		return RoleContract{}, fmt.Errorf("role %s allowed_provider_actions must not be empty", role)
	}
	actions := make([]string, 0, len(contract.AllowedProviderActions))
	seenActions := make(map[string]struct{}, len(contract.AllowedProviderActions))
	for _, action := range contract.AllowedProviderActions {
		action = strings.TrimSpace(action)
		if !IsSupportedProviderAction(action) {
			return RoleContract{}, fmt.Errorf("role %s allowed_provider_actions contains unsupported action: %s", role, action)
		}
		if _, ok := seenActions[action]; ok {
			continue
		}
		seenActions[action] = struct{}{}
		actions = append(actions, action)
	}
	contract.AllowedProviderActions = actions
	if len(contract.AllowedWorkerRoles) > 0 {
		allowed := make([]string, 0, len(contract.AllowedWorkerRoles))
		seenRoles := make(map[string]struct{}, len(contract.AllowedWorkerRoles))
		for _, allowedRole := range contract.AllowedWorkerRoles {
			canonical, _, _, err := NormalizeWorkerRole(allowedRole)
			if err != nil {
				return RoleContract{}, fmt.Errorf("role %s allowed_worker_roles contains unsupported role %q: %w", role, allowedRole, err)
			}
			if _, ok := seenRoles[canonical]; ok {
				continue
			}
			seenRoles[canonical] = struct{}{}
			allowed = append(allowed, canonical)
		}
		contract.AllowedWorkerRoles = allowed
	}
	contract.WorkspaceIsolation = NormalizeWorkspaceIsolationMode(contract.WorkspaceIsolation)
	if contract.WorkspaceIsolation != "" && !IsSupportedWorkspaceIsolationMode(contract.WorkspaceIsolation) {
		return RoleContract{}, fmt.Errorf("role %s workspace_isolation unsupported: %s", role, contract.WorkspaceIsolation)
	}
	contract.ReconcileMode = NormalizeWorkerReconcileMode(contract.ReconcileMode)
	if contract.ReconcileMode != "" && !IsSupportedWorkerReconcileMode(contract.ReconcileMode) {
		return RoleContract{}, fmt.Errorf("role %s reconcile_mode unsupported: %s", role, contract.ReconcileMode)
	}
	if contract.PermissionModeID != "" {
		contract.PermissionModeID = EffectivePermissionModeID(contract.PermissionModeID)
		if !IsSupportedPermissionMode(contract.PermissionModeID) {
			return RoleContract{}, fmt.Errorf("role %s permission_mode_id unsupported: %s", role, contract.PermissionModeID)
		}
	}
	contract.ContextSections = cleanStringList(contract.ContextSections)
	contract.ReviewRequirements = cleanStringList(contract.ReviewRequirements)
	contract.VerificationRequirements = cleanStringList(contract.VerificationRequirements)
	contract.MemoryPolicy = strings.TrimSpace(contract.MemoryPolicy)
	contract.OutputContract = strings.TrimSpace(contract.OutputContract)
	return contract, nil
}

func RoleContractAllowsProviderAction(contract RoleContract, action string) bool {
	action = strings.TrimSpace(action)
	for _, allowed := range contract.AllowedProviderActions {
		if allowed == action {
			return true
		}
	}
	return false
}

func RoleContractAllowsWorkerRole(contract RoleContract, role string) bool {
	role, _, _, err := NormalizeWorkerRole(role)
	if err != nil {
		return false
	}
	for _, allowed := range contract.AllowedWorkerRoles {
		if allowed == role {
			return true
		}
	}
	return false
}

func cleanStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func NormalizeWorkerReconcileMode(mode string) string {
	switch normalized := strings.ToLower(strings.TrimSpace(mode)); normalized {
	case "", "default":
		return ""
	case "apply", "apply_on_accept", "apply-on-accept":
		return "apply_on_accept"
	case "artifact", "artifact_only", "artifact-only", "record", "record_only", "record-only":
		return "artifact_only"
	default:
		return normalized
	}
}

func NormalizeWorkspaceIsolationMode(mode string) string {
	switch normalized := strings.ToLower(strings.TrimSpace(mode)); normalized {
	case "", "auto":
		return "auto"
	case "shared", "shared_workspace", "shared-workspace":
		return "shared_workspace"
	case "snapshot", "snapshot_copy", "snapshot-copy":
		return "snapshot_copy"
	case "git", "git_worktree", "git-worktree":
		return "git_worktree"
	default:
		return normalized
	}
}

func IsSupportedWorkerReconcileMode(mode string) bool {
	switch mode {
	case "apply_on_accept", "artifact_only":
		return true
	default:
		return false
	}
}

func IsSupportedPermissionMode(mode string) bool {
	switch mode {
	case PermissionModeStandard, PermissionModeYolo:
		return true
	default:
		return false
	}
}

func IsSupportedWorkspaceIsolationMode(mode string) bool {
	switch mode {
	case "auto", "shared_workspace", "snapshot_copy", "git_worktree":
		return true
	default:
		return false
	}
}

func cloneSubagentPolicy(policy *SubagentPolicy) *SubagentPolicy {
	if policy == nil {
		return nil
	}
	cloned := *policy
	cloned.AllowedWorkerRoles = append([]string(nil), policy.AllowedWorkerRoles...)
	return &cloned
}

func taskRoleForKind(kind Kind, preset PresetID) string {
	switch kind {
	case KindCoding:
		return string(KindCoding)
	case KindReviewer:
		return string(KindReviewer)
	case KindSecurityReview:
		return string(KindSecurityReview)
	case KindGeneral:
		if preset == PresetDocsLite {
			return string(KindGeneral)
		}
		return string(KindGeneral)
	default:
		return string(kind)
	}
}

func defaultAllowChildWorkers(role string) bool {
	switch role {
	case string(KindCoding), string(KindGeneral):
		return true
	default:
		return false
	}
}

func defaultAllowedWorkerRoles(role string) []string {
	if !defaultAllowChildWorkers(role) {
		return nil
	}
	return []string{
		string(KindCoding),
		string(KindGeneral),
		string(KindReviewer),
		string(KindSecurityReview),
	}
}

func defaultSubagentReconcileMode(role string) string {
	switch role {
	case string(KindCoding), string(KindGeneral):
		return "apply_on_accept"
	default:
		return "artifact_only"
	}
}

func DefaultSubagentPolicyForTask(cfg Config, kind Kind, preset PresetID, permissionModeID string) SubagentPolicy {
	policy, err := ResolveSubagentRolePolicy(cfg, taskRoleForKind(kind, preset), permissionModeID)
	if err != nil {
		return SubagentPolicy{
			PermissionModeID:     EffectivePermissionModeID(permissionModeID),
			WorkspaceIsolation:   NormalizeWorkspaceIsolationMode(cfg.Subagents.WorkspaceIsolation),
			ReconcileMode:        defaultSubagentReconcileMode(taskRoleForKind(kind, preset)),
			AutoReleaseOnSuccess: cfg.Subagents.AutoReleaseOnSuccess,
			MaxWorkersPerTask:    cfg.Subagents.MaxWorkersPerTask,
			MaxLineageDepth:      cfg.Subagents.MaxLineageDepth,
		}
	}
	return policy
}

func ResolveSubagentRolePolicy(cfg Config, role, inheritedPermissionMode string) (SubagentPolicy, error) {
	role, _, _, err := NormalizeWorkerRole(role)
	if err != nil {
		return SubagentPolicy{}, err
	}
	permissionModeID := EffectivePermissionModeID(inheritedPermissionMode)
	if permissionModeID == "" {
		permissionModeID = EffectivePermissionModeID(cfg.Permission.DefaultMode)
	}
	policy := SubagentPolicy{
		PermissionModeID:     permissionModeID,
		WorkspaceIsolation:   NormalizeWorkspaceIsolationMode(cfg.Subagents.WorkspaceIsolation),
		ReconcileMode:        defaultSubagentReconcileMode(role),
		AutoReleaseOnSuccess: cfg.Subagents.AutoReleaseOnSuccess,
		AllowChildWorkers:    defaultAllowChildWorkers(role),
		AllowedWorkerRoles:   defaultAllowedWorkerRoles(role),
		MaxWorkersPerTask:    cfg.Subagents.MaxWorkersPerTask,
		MaxLineageDepth:      cfg.Subagents.MaxLineageDepth,
	}
	if override, ok := cfg.Subagents.RolePolicies[role]; ok {
		if override.PermissionModeID != "" {
			policy.PermissionModeID = EffectivePermissionModeID(override.PermissionModeID)
		}
		if override.WorkspaceIsolation != "" {
			policy.WorkspaceIsolation = NormalizeWorkspaceIsolationMode(override.WorkspaceIsolation)
		}
		if override.ReconcileMode != "" {
			policy.ReconcileMode = NormalizeWorkerReconcileMode(override.ReconcileMode)
		}
		if override.AutoReleaseOnSuccess != nil {
			policy.AutoReleaseOnSuccess = *override.AutoReleaseOnSuccess
		}
		if override.AllowChildWorkers != nil {
			policy.AllowChildWorkers = *override.AllowChildWorkers
		}
		if len(override.AllowedWorkerRoles) > 0 {
			policy.AllowedWorkerRoles = append([]string(nil), override.AllowedWorkerRoles...)
		}
		if override.MaxWorkersPerTask > 0 {
			policy.MaxWorkersPerTask = override.MaxWorkersPerTask
		}
		if override.MaxLineageDepth > 0 {
			policy.MaxLineageDepth = override.MaxLineageDepth
		}
	}
	if !policy.AllowChildWorkers {
		policy.AllowedWorkerRoles = nil
	}
	if policy.MaxWorkersPerTask <= 0 {
		policy.MaxWorkersPerTask = cfg.Subagents.MaxWorkersPerTask
	}
	if policy.MaxLineageDepth <= 0 {
		policy.MaxLineageDepth = cfg.Subagents.MaxLineageDepth
	}
	return policy, nil
}

func HydrateSpec(spec Spec, cfg Config) Spec {
	if strings.TrimSpace(spec.RootTaskID) == "" {
		spec.RootTaskID = spec.TaskID
	}
	if spec.LineageDepth < 0 {
		spec.LineageDepth = 0
	}
	if spec.SubagentPolicy == nil {
		policy := DefaultSubagentPolicyForTask(cfg, spec.Kind, spec.PresetID, spec.PermissionModeID)
		spec.SubagentPolicy = &policy
	}
	return spec
}

func validateEffectiveSubagentPolicy(policy *SubagentPolicy) error {
	if policy == nil {
		return nil
	}
	if policy.PermissionModeID != "" && !IsSupportedPermissionMode(policy.PermissionModeID) {
		return fmt.Errorf("unsupported subagent policy permission mode: %s", policy.PermissionModeID)
	}
	if policy.WorkspaceIsolation != "" && !IsSupportedWorkspaceIsolationMode(NormalizeWorkspaceIsolationMode(policy.WorkspaceIsolation)) {
		return fmt.Errorf("unsupported subagent policy workspace isolation: %s", policy.WorkspaceIsolation)
	}
	if policy.ReconcileMode != "" && !IsSupportedWorkerReconcileMode(NormalizeWorkerReconcileMode(policy.ReconcileMode)) {
		return fmt.Errorf("unsupported subagent policy reconcile mode: %s", policy.ReconcileMode)
	}
	if policy.MaxWorkersPerTask < 0 {
		return fmt.Errorf("subagent policy max_workers_per_task must be >= 0")
	}
	if policy.MaxLineageDepth < 0 {
		return fmt.Errorf("subagent policy max_lineage_depth must be >= 0")
	}
	for _, role := range policy.AllowedWorkerRoles {
		if _, _, _, err := NormalizeWorkerRole(role); err != nil {
			return fmt.Errorf("unsupported subagent policy allowed worker role: %s", strings.TrimSpace(role))
		}
	}
	return nil
}

func normalizeSubagentRolePolicies(raw map[string]SubagentRolePolicy) (map[string]SubagentRolePolicy, error) {
	normalized := make(map[string]SubagentRolePolicy, len(raw))
	for role, policy := range raw {
		canonicalRole, _, _, err := NormalizeWorkerRole(role)
		if err != nil {
			return nil, fmt.Errorf("unsupported subagents.role_policies key %q: %w", role, err)
		}
		if policy.PermissionModeID != "" && !IsSupportedPermissionMode(policy.PermissionModeID) {
			return nil, fmt.Errorf("unsupported subagents.role_policies.%s.permission_mode_id: %s", canonicalRole, policy.PermissionModeID)
		}
		policy.WorkspaceIsolation = NormalizeWorkspaceIsolationMode(policy.WorkspaceIsolation)
		if policy.WorkspaceIsolation != "" && !IsSupportedWorkspaceIsolationMode(policy.WorkspaceIsolation) {
			return nil, fmt.Errorf("unsupported subagents.role_policies.%s.workspace_isolation: %s", canonicalRole, policy.WorkspaceIsolation)
		}
		policy.ReconcileMode = NormalizeWorkerReconcileMode(policy.ReconcileMode)
		if policy.ReconcileMode != "" && !IsSupportedWorkerReconcileMode(policy.ReconcileMode) {
			return nil, fmt.Errorf("unsupported subagents.role_policies.%s.reconcile_mode: %s", canonicalRole, policy.ReconcileMode)
		}
		if policy.MaxWorkersPerTask < 0 {
			return nil, fmt.Errorf("subagents.role_policies.%s.max_workers_per_task must be >= 0", canonicalRole)
		}
		if policy.MaxLineageDepth < 0 {
			return nil, fmt.Errorf("subagents.role_policies.%s.max_lineage_depth must be >= 0", canonicalRole)
		}
		if len(policy.AllowedWorkerRoles) > 0 {
			seen := make(map[string]struct{}, len(policy.AllowedWorkerRoles))
			allowed := make([]string, 0, len(policy.AllowedWorkerRoles))
			for _, allowedRole := range policy.AllowedWorkerRoles {
				canonicalAllowedRole, _, _, err := NormalizeWorkerRole(allowedRole)
				if err != nil {
					return nil, fmt.Errorf("unsupported subagents.role_policies.%s.allowed_worker_roles entry %q: %w", canonicalRole, allowedRole, err)
				}
				if _, ok := seen[canonicalAllowedRole]; ok {
					continue
				}
				seen[canonicalAllowedRole] = struct{}{}
				allowed = append(allowed, canonicalAllowedRole)
			}
			policy.AllowedWorkerRoles = allowed
		}
		normalized[canonicalRole] = policy
	}
	return normalized, nil
}

func NewInitialCriteria(spec Spec) CriteriaSnapshot {
	now := Now()
	items := make([]CriterionStatus, 0, len(spec.SuccessCriteria))
	currentCriterionID := ""
	currentCriterionStatement := ""
	for i, criterion := range spec.SuccessCriteria {
		selected := false
		if currentCriterionID == "" {
			currentCriterionID = criterion.ID
			currentCriterionStatement = strings.TrimSpace(criterion.Statement)
			selected = true
		}
		items = append(items, CriterionStatus{
			CriterionID:      criterion.ID,
			Statement:        strings.TrimSpace(criterion.Statement),
			Ordinal:          i + 1,
			Status:           "open",
			Passes:           false,
			Selected:         selected,
			LastSummary:      "Criterion remains open until durable verification or workspace evidence closes it.",
			LastEvaluatedAt:  now,
			LastTransitionAt: now,
		})
	}
	summary := "Acceptance ledger initialized."
	switch len(spec.SuccessCriteria) {
	case 0:
		summary = "Acceptance ledger initialized with no explicit success criteria."
	case 1:
		summary = "Acceptance ledger initialized. 1 criterion is still failing."
	default:
		summary = fmt.Sprintf("Acceptance ledger initialized. %d criteria are still failing.", len(spec.SuccessCriteria))
	}
	return CriteriaSnapshot{
		SchemaVersion:             SchemaVersion,
		SnapshotID:                NewID("CRT"),
		TaskID:                    spec.TaskID,
		UpdatedAt:                 now,
		Summary:                   summary,
		CurrentCriterionID:        currentCriterionID,
		CurrentCriterionStatement: currentCriterionStatement,
		MetCount:                  0,
		OpenCount:                 len(spec.SuccessCriteria),
		Criteria:                  items,
	}
}

func NewInitialCheckpoint(spec Spec) Checkpoint {
	return Checkpoint{
		SchemaVersion: SchemaVersion,
		CheckpointID:  "0001",
		TaskID:        spec.TaskID,
		CapturedAt:    Now(),
		Phase:         PhaseExplore,
		State:         StateActive,
	}
}
