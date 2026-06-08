package task

import "strings"

const SchemaVersion = 1

type Kind string

const (
	KindCoding         Kind = "coding"
	KindGeneral        Kind = "general_execution"
	KindSecurityReview Kind = "security_review"
	KindReviewer       Kind = "reviewer"
)

type PresetID string

const (
	PresetDocsLite PresetID = "docs_lite"
)

type Phase string

const (
	PhaseExplore Phase = "Explore"
	PhasePlan    Phase = "Plan"
	PhaseExecute Phase = "Execute"
	PhaseVerify  Phase = "Verify"
	PhaseReview  Phase = "Review"
)

type StateName string

const (
	StateActive  StateName = "Active"
	StateBlocked StateName = "Blocked"
	StateWaiting StateName = "Waiting"
	StateDone    StateName = "Done"
	StateFailed  StateName = "Failed"
	StateAborted StateName = "Aborted"
)

const (
	PermissionModeStandard = "standard"
	PermissionModeYolo     = "yolo"
)

const (
	MemoryKindTaskCompletion = "task_completion"
	MemoryKindTaskMilestone  = "task_milestone"
	MemoryKindTaskDecision   = "task_decision"
	MemoryKindTaskBlocker    = "task_blocker"
	MemoryKindTaskNote       = "task_note"
)

const (
	MemorySourceRuntime  = "runtime"
	MemorySourceOperator = "operator"
	MemorySourceProvider = "provider"
)

func CanonicalMemoryKind(kind string) string {
	switch normalized := strings.ToLower(strings.TrimSpace(kind)); normalized {
	case "", "note", MemoryKindTaskNote:
		return MemoryKindTaskNote
	case "completion", MemoryKindTaskCompletion:
		return MemoryKindTaskCompletion
	case "milestone", MemoryKindTaskMilestone:
		return MemoryKindTaskMilestone
	case "decision", MemoryKindTaskDecision:
		return MemoryKindTaskDecision
	case "blocker", MemoryKindTaskBlocker:
		return MemoryKindTaskBlocker
	default:
		return normalized
	}
}

func IsSupportedMemoryKind(kind string) bool {
	switch CanonicalMemoryKind(kind) {
	case MemoryKindTaskCompletion, MemoryKindTaskMilestone, MemoryKindTaskDecision, MemoryKindTaskBlocker, MemoryKindTaskNote:
		return true
	default:
		return false
	}
}

func CanonicalMemorySource(source string) string {
	switch normalized := strings.ToLower(strings.TrimSpace(source)); normalized {
	case "", MemorySourceOperator:
		return MemorySourceOperator
	case MemorySourceRuntime:
		return MemorySourceRuntime
	case MemorySourceProvider:
		return MemorySourceProvider
	default:
		return normalized
	}
}

type SuccessCriterion struct {
	ID        string `json:"id"`
	Statement string `json:"statement"`
}

type SubagentPolicy struct {
	PermissionModeID     string   `json:"permission_mode_id,omitempty"`
	WorkspaceIsolation   string   `json:"workspace_isolation,omitempty"`
	ReconcileMode        string   `json:"reconcile_mode,omitempty"`
	AutoReleaseOnSuccess bool     `json:"auto_release_on_success"`
	AllowChildWorkers    bool     `json:"allow_child_workers"`
	AllowedWorkerRoles   []string `json:"allowed_worker_roles,omitempty"`
	MaxWorkersPerTask    int      `json:"max_workers_per_task,omitempty"`
	MaxLineageDepth      int      `json:"max_lineage_depth,omitempty"`
}

type Spec struct {
	SchemaVersion    int                `json:"schema_version"`
	TaskID           string             `json:"task_id"`
	Kind             Kind               `json:"kind"`
	PresetID         PresetID           `json:"preset_id"`
	Title            string             `json:"title"`
	Objective        string             `json:"objective"`
	SuccessCriteria  []SuccessCriterion `json:"success_criteria"`
	Constraints      []string           `json:"constraints,omitempty"`
	WorkspaceRoot    string             `json:"workspace_root"`
	PermissionModeID string             `json:"permission_mode_id,omitempty"`
	ParentTaskID     string             `json:"parent_task_id,omitempty"`
	ParentWorkerID   string             `json:"parent_worker_id,omitempty"`
	RootTaskID       string             `json:"root_task_id,omitempty"`
	LineageDepth     int                `json:"lineage_depth,omitempty"`
	SubagentPolicy   *SubagentPolicy    `json:"subagent_policy,omitempty"`
	CreatedAt        string             `json:"created_at"`
}

type TaskFile struct {
	Kind             Kind               `json:"kind"`
	PresetID         PresetID           `json:"preset_id"`
	Title            string             `json:"title"`
	Objective        string             `json:"objective"`
	SuccessCriteria  []SuccessCriterion `json:"success_criteria"`
	Constraints      []string           `json:"constraints"`
	WorkspaceRoot    string             `json:"workspace_root"`
	PermissionModeID string             `json:"permission_mode_id"`
	ParentTaskID     string             `json:"parent_task_id,omitempty"`
	ParentWorkerID   string             `json:"parent_worker_id,omitempty"`
	RootTaskID       string             `json:"root_task_id,omitempty"`
	LineageDepth     int                `json:"lineage_depth,omitempty"`
	SubagentPolicy   *SubagentPolicy    `json:"subagent_policy,omitempty"`
}

type Step struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind,omitempty"`
	Source       string   `json:"source,omitempty"`
	ParentStepID string   `json:"parent_step_id,omitempty"`
	DependsOn    []string `json:"depends_on,omitempty"`
	Priority     string   `json:"priority,omitempty"`
	Title        string   `json:"title"`
	Status       string   `json:"status"`
	Covers       []string `json:"covers,omitempty"`
	Verifier     []string `json:"verifier,omitempty"`
	Notes        string   `json:"notes,omitempty"`
	EvidenceRefs []string `json:"evidence_refs,omitempty"`
	UpdatedAt    string   `json:"updated_at,omitempty"`
}

type Plan struct {
	SchemaVersion           int      `json:"schema_version"`
	TaskID                  string   `json:"task_id"`
	UpdatedAt               string   `json:"updated_at"`
	Revision                int      `json:"revision,omitempty"`
	Explanation             string   `json:"explanation,omitempty"`
	CurrentSystemStepID     string   `json:"current_system_step_id,omitempty"`
	CurrentExecutionStepID  string   `json:"current_execution_step_id,omitempty"`
	ReadyExecutionStepIDs   []string `json:"ready_execution_step_ids,omitempty"`
	BlockedExecutionStepIDs []string `json:"blocked_execution_step_ids,omitempty"`
	LastMutationRef         string   `json:"last_mutation_ref,omitempty"`
	Steps                   []Step   `json:"steps"`
}

const (
	StepStatusPending    = "pending"
	StepStatusInProgress = "in_progress"
	StepStatusCompleted  = "completed"
	StepStatusCancelled  = "cancelled"
)

const (
	StepPriorityHigh   = "high"
	StepPriorityMedium = "medium"
	StepPriorityLow    = "low"
)

const (
	StepKindBaseline   = "baseline"
	StepKindCriterion  = "criterion"
	StepKindReviewGate = "review_gate"
	StepKindExecution  = "execution"
)

const (
	StepSourceSystem   = "system"
	StepSourceOperator = "operator"
	StepSourceProvider = "provider"
)

type ExecutionPlanStep struct {
	ID           string   `json:"id,omitempty"`
	ParentStepID string   `json:"parent_step_id,omitempty"`
	DependsOn    []string `json:"depends_on,omitempty"`
	Priority     string   `json:"priority,omitempty"`
	Title        string   `json:"title"`
	Status       string   `json:"status,omitempty"`
	Covers       []string `json:"covers,omitempty"`
	Notes        string   `json:"notes,omitempty"`
}

type PlanUpdate struct {
	Explanation string              `json:"explanation,omitempty"`
	Steps       []ExecutionPlanStep `json:"steps"`
}

type PlanPatch struct {
	Operations []PlanPatchOperation `json:"operations"`
}

type PlanPatchOperation struct {
	Op          string             `json:"op"`
	Explanation string             `json:"explanation,omitempty"`
	StepID      string             `json:"step_id,omitempty"`
	AfterStepID string             `json:"after_step_id,omitempty"`
	Step        *ExecutionPlanStep `json:"step,omitempty"`
}

const (
	PlanPatchOpSetExplanation = "set_explanation"
	PlanPatchOpUpsertStep     = "upsert_step"
	PlanPatchOpRemoveStep     = "remove_step"
)

const (
	PlanMutationKindReplace = "replace"
	PlanMutationKindPatch   = "patch"
)

type PlanMutationRecord struct {
	SchemaVersion           int                  `json:"schema_version"`
	MutationID              string               `json:"mutation_id"`
	TaskID                  string               `json:"task_id"`
	Revision                int                  `json:"revision"`
	MutationKind            string               `json:"mutation_kind,omitempty"`
	Source                  string               `json:"source"`
	TS                      string               `json:"ts"`
	Explanation             string               `json:"explanation,omitempty"`
	CurrentExecutionStepID  string               `json:"current_execution_step_id,omitempty"`
	ReadyExecutionStepIDs   []string             `json:"ready_execution_step_ids,omitempty"`
	BlockedExecutionStepIDs []string             `json:"blocked_execution_step_ids,omitempty"`
	Steps                   []Step               `json:"steps"`
	PatchOperations         []PlanPatchOperation `json:"patch_operations,omitempty"`
}

type ProjectStep struct {
	ID           string   `json:"id"`
	ParentStepID string   `json:"parent_step_id,omitempty"`
	DependsOn    []string `json:"depends_on,omitempty"`
	Priority     string   `json:"priority,omitempty"`
	Title        string   `json:"title"`
	Status       string   `json:"status"`
	BranchID     string   `json:"branch_id,omitempty"`
	TaskID       string   `json:"task_id,omitempty"`
	Notes        string   `json:"notes,omitempty"`
	UpdatedAt    string   `json:"updated_at,omitempty"`
}

type ProjectBranch struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Status         string `json:"status"`
	TaskID         string `json:"task_id,omitempty"`
	TaskRef        string `json:"task_ref,omitempty"`
	StatusRef      string `json:"status_ref,omitempty"`
	HandoffRef     string `json:"handoff_ref,omitempty"`
	WorkspaceRoot  string `json:"workspace_root,omitempty"`
	LastReasonCode string `json:"last_reason_code,omitempty"`
	Notes          string `json:"notes,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
}

type ProjectTaskLink struct {
	StepID       string `json:"step_id,omitempty"`
	Title        string `json:"title,omitempty"`
	Status       string `json:"status,omitempty"`
	BranchID     string `json:"branch_id,omitempty"`
	BranchTitle  string `json:"branch_title,omitempty"`
	BranchStatus string `json:"branch_status,omitempty"`
	TaskID       string `json:"task_id,omitempty"`
	TaskRef      string `json:"task_ref,omitempty"`
	StatusRef    string `json:"status_ref,omitempty"`
	HandoffRef   string `json:"handoff_ref,omitempty"`
}

type ProjectTaskContext struct {
	WorkspaceCurrentStepID    string            `json:"workspace_current_step_id,omitempty"`
	WorkspaceCurrentStepTitle string            `json:"workspace_current_step_title,omitempty"`
	PrimaryStepID             string            `json:"primary_step_id,omitempty"`
	PrimaryStepTitle          string            `json:"primary_step_title,omitempty"`
	PrimaryStepStatus         string            `json:"primary_step_status,omitempty"`
	PrimaryBranchID           string            `json:"primary_branch_id,omitempty"`
	PrimaryBranchTitle        string            `json:"primary_branch_title,omitempty"`
	PrimaryBranchStatus       string            `json:"primary_branch_status,omitempty"`
	ParentStepID              string            `json:"parent_step_id,omitempty"`
	Priority                  string            `json:"priority,omitempty"`
	Notes                     string            `json:"notes,omitempty"`
	BoundStepIDs              []string          `json:"bound_step_ids,omitempty"`
	BoundBranchIDs            []string          `json:"bound_branch_ids,omitempty"`
	DependsOnStepIDs          []string          `json:"depends_on_step_ids,omitempty"`
	UnmetDependencyStepIDs    []string          `json:"unmet_dependency_step_ids,omitempty"`
	DependenciesSatisfied     bool              `json:"dependencies_satisfied"`
	DependencySteps           []ProjectTaskLink `json:"dependency_steps,omitempty"`
	DownstreamSteps           []ProjectTaskLink `json:"downstream_steps,omitempty"`
	ReadyProjectStepIDs       []string          `json:"ready_project_step_ids,omitempty"`
	BlockedProjectStepIDs     []string          `json:"blocked_project_step_ids,omitempty"`
	ActiveProjectBranchIDs    []string          `json:"active_project_branch_ids,omitempty"`
	Refs                      []string          `json:"refs,omitempty"`
}

type Project struct {
	SchemaVersion   int             `json:"schema_version"`
	WorkspaceRoot   string          `json:"workspace_root"`
	UpdatedAt       string          `json:"updated_at"`
	Revision        int             `json:"revision,omitempty"`
	Explanation     string          `json:"explanation,omitempty"`
	CurrentStepID   string          `json:"current_step_id,omitempty"`
	ReadyStepIDs    []string        `json:"ready_step_ids,omitempty"`
	BlockedStepIDs  []string        `json:"blocked_step_ids,omitempty"`
	ActiveBranchIDs []string        `json:"active_branch_ids,omitempty"`
	LastMutationRef string          `json:"last_mutation_ref,omitempty"`
	Steps           []ProjectStep   `json:"steps"`
	Branches        []ProjectBranch `json:"branches,omitempty"`
}

const (
	ProjectStepStatusPending    = "pending"
	ProjectStepStatusInProgress = "in_progress"
	ProjectStepStatusBlocked    = "blocked"
	ProjectStepStatusCompleted  = "completed"
	ProjectStepStatusCancelled  = "cancelled"
)

const (
	ProjectBranchStatusPending   = "pending"
	ProjectBranchStatusActive    = "active"
	ProjectBranchStatusBlocked   = "blocked"
	ProjectBranchStatusCompleted = "completed"
	ProjectBranchStatusCancelled = "cancelled"
)

type ProjectExecutionStep struct {
	ID           string   `json:"id,omitempty"`
	ParentStepID string   `json:"parent_step_id,omitempty"`
	DependsOn    []string `json:"depends_on,omitempty"`
	Priority     string   `json:"priority,omitempty"`
	Title        string   `json:"title"`
	Status       string   `json:"status,omitempty"`
	BranchID     string   `json:"branch_id,omitempty"`
	TaskID       string   `json:"task_id,omitempty"`
	Notes        string   `json:"notes,omitempty"`
}

type ProjectBranchSpec struct {
	ID     string `json:"id,omitempty"`
	Title  string `json:"title"`
	Status string `json:"status,omitempty"`
	TaskID string `json:"task_id,omitempty"`
	Notes  string `json:"notes,omitempty"`
}

type ProjectUpdate struct {
	Explanation string                 `json:"explanation,omitempty"`
	Steps       []ProjectExecutionStep `json:"steps,omitempty"`
	Branches    []ProjectBranchSpec    `json:"branches,omitempty"`
}

type ProjectPatch struct {
	Operations []ProjectPatchOperation `json:"operations"`
}

type ProjectPatchOperation struct {
	Op           string                `json:"op"`
	Explanation  string                `json:"explanation,omitempty"`
	StepID       string                `json:"step_id,omitempty"`
	AfterStepID  string                `json:"after_step_id,omitempty"`
	BranchID     string                `json:"branch_id,omitempty"`
	ParentStepID string                `json:"parent_step_id,omitempty"`
	TaskID       string                `json:"task_id,omitempty"`
	DependsOn    []string              `json:"depends_on,omitempty"`
	Status       string                `json:"status,omitempty"`
	Step         *ProjectExecutionStep `json:"step,omitempty"`
	Branch       *ProjectBranchSpec    `json:"branch,omitempty"`
}

const (
	ProjectPatchOpSetExplanation   = "set_explanation"
	ProjectPatchOpUpsertStep       = "upsert_step"
	ProjectPatchOpRemoveStep       = "remove_step"
	ProjectPatchOpSetStepDependsOn = "set_step_dependencies"
	ProjectPatchOpSetStepParent    = "set_step_parent"
	ProjectPatchOpBindStepBranch   = "bind_step_branch"
	ProjectPatchOpBindStepTask     = "bind_step_task"
	ProjectPatchOpUpsertBranch     = "upsert_branch"
	ProjectPatchOpRemoveBranch     = "remove_branch"
	ProjectPatchOpBindBranchTask   = "bind_branch_task"
	ProjectPatchOpSetBranchStatus  = "set_branch_status"
)

type ProjectMutationRecord struct {
	SchemaVersion   int                     `json:"schema_version"`
	MutationID      string                  `json:"mutation_id"`
	Revision        int                     `json:"revision"`
	MutationKind    string                  `json:"mutation_kind,omitempty"`
	Source          string                  `json:"source"`
	TS              string                  `json:"ts"`
	Explanation     string                  `json:"explanation,omitempty"`
	CurrentStepID   string                  `json:"current_step_id,omitempty"`
	ReadyStepIDs    []string                `json:"ready_step_ids,omitempty"`
	BlockedStepIDs  []string                `json:"blocked_step_ids,omitempty"`
	ActiveBranchIDs []string                `json:"active_branch_ids,omitempty"`
	Steps           []ProjectStep           `json:"steps"`
	Branches        []ProjectBranch         `json:"branches,omitempty"`
	PatchOperations []ProjectPatchOperation `json:"patch_operations,omitempty"`
}

const (
	MissionStatusDraft   = "draft"
	MissionStatusActive  = "active"
	MissionStatusBlocked = "blocked"
	MissionStatusPaused  = "paused"
	MissionStatusDone    = "done"
)

const (
	MissionPlanApprovalPending  = "pending"
	MissionPlanApprovalApproved = "approved"
)

const (
	MissionRoleOrchestrator = "orchestrator"
	MissionRoleWorkers      = "workers"
	MissionRoleValidators   = "validators"
)

const (
	MissionRoleModelSourceMission  = "mission.role_models"
	MissionRoleModelSourceProvider = "provider.model"
	MissionRoleModelSourceEmpty    = "empty"
)

type MissionRoleModelResolution struct {
	Role     string `json:"role"`
	Model    string `json:"model,omitempty"`
	Source   string `json:"source"`
	Explicit bool   `json:"explicit"`
}

type MissionRolePlanEntry struct {
	Model    string `json:"model,omitempty"`
	Source   string `json:"source"`
	Explicit bool   `json:"explicit"`
}

type Mission struct {
	ObjectKind              string                          `json:"object_kind"`
	SchemaVersion           int                             `json:"schema_version"`
	MissionID               string                          `json:"mission_id"`
	Title                   string                          `json:"title"`
	Objective               string                          `json:"objective"`
	RootTaskID              string                          `json:"root_task_id"`
	ProjectRef              string                          `json:"project_ref,omitempty"`
	ValidationContractRef   string                          `json:"validation_contract_ref"`
	CurrentMilestoneID      string                          `json:"current_milestone_id,omitempty"`
	Status                  string                          `json:"status"`
	StatusReasonCode        string                          `json:"status_reason_code,omitempty"`
	PlanApprovalStatus      string                          `json:"plan_approval_status,omitempty"`
	PlanApprovedAt          string                          `json:"plan_approved_at,omitempty"`
	PlanApprovedBy          string                          `json:"plan_approved_by,omitempty"`
	PlanApprovedContractRef string                          `json:"plan_approved_contract_ref,omitempty"`
	FeatureRefs             []string                        `json:"feature_refs,omitempty"`
	MilestoneRefs           []string                        `json:"milestone_refs,omitempty"`
	LatestValidationRef     string                          `json:"latest_validation_ref,omitempty"`
	RolePlan                map[string]MissionRolePlanEntry `json:"role_plan,omitempty"`
	CreatedAt               string                          `json:"created_at"`
	UpdatedAt               string                          `json:"updated_at"`
}

type MissionValidationContract struct {
	ObjectKind             string                     `json:"object_kind"`
	SchemaVersion          int                        `json:"schema_version"`
	MissionID              string                     `json:"mission_id"`
	ContractID             string                     `json:"contract_id"`
	BehavioralRequirements []string                   `json:"behavioral_requirements,omitempty"`
	AcceptanceTests        []string                   `json:"acceptance_tests,omitempty"`
	Assertions             []MissionContractAssertion `json:"assertions,omitempty"`
	NegativeCases          []string                   `json:"negative_cases,omitempty"`
	NonGoals               []string                   `json:"non_goals,omitempty"`
	ManualChecks           []string                   `json:"manual_checks,omitempty"`
	AllowedWaivers         []string                   `json:"allowed_waivers,omitempty"`
	EvidenceRequirements   []string                   `json:"evidence_requirements,omitempty"`
	CreatedFromTaskRef     string                     `json:"created_from_task_ref,omitempty"`
	CreatedAt              string                     `json:"created_at"`
	UpdatedAt              string                     `json:"updated_at"`
}

type MissionContractAssertion struct {
	AssertionID      string   `json:"assertion_id"`
	Kind             string   `json:"kind"`
	Statement        string   `json:"statement"`
	EvidenceRequired []string `json:"evidence_required,omitempty"`
	Validator        string   `json:"validator,omitempty"`
	NegativeCase     string   `json:"negative_case,omitempty"`
	ManualCheck      string   `json:"manual_check,omitempty"`
}

type MissionFeatureSet struct {
	ObjectKind    string           `json:"object_kind"`
	SchemaVersion int              `json:"schema_version"`
	MissionID     string           `json:"mission_id"`
	Features      []MissionFeature `json:"features"`
	UpdatedAt     string           `json:"updated_at"`
}

type MissionFeature struct {
	FeatureID            string   `json:"feature_id"`
	Title                string   `json:"title"`
	Description          string   `json:"description,omitempty"`
	DependsOn            []string `json:"depends_on,omitempty"`
	BoundTaskID          string   `json:"bound_task_id,omitempty"`
	BoundWorkerID        string   `json:"bound_worker_id,omitempty"`
	ExpectedFiles        []string `json:"expected_files,omitempty"`
	VerifierCommands     []string `json:"verifier_commands,omitempty"`
	ContractCoverage     []string `json:"contract_coverage,omitempty"`
	Status               string   `json:"status"`
	EvidenceRefs         []string `json:"evidence_refs,omitempty"`
	ValidatorFindingRefs []string `json:"validator_finding_refs,omitempty"`
	FixFeatureRefs       []string `json:"fix_feature_refs,omitempty"`
	UpdatedAt            string   `json:"updated_at"`
}

type MissionMilestoneSet struct {
	ObjectKind        string             `json:"object_kind"`
	SchemaVersion     int                `json:"schema_version"`
	MissionID         string             `json:"mission_id"`
	CurrentFeatureID  string             `json:"current_feature_id,omitempty"`
	ReadyFeatureIDs   []string           `json:"ready_feature_ids,omitempty"`
	BlockedFeatureIDs []string           `json:"blocked_feature_ids,omitempty"`
	Milestones        []MissionMilestone `json:"milestones"`
	UpdatedAt         string             `json:"updated_at"`
}

type MissionMilestone struct {
	MilestoneID       string   `json:"milestone_id"`
	Title             string   `json:"title"`
	FeatureIDs        []string `json:"feature_ids,omitempty"`
	BoundTaskIDs      []string `json:"bound_task_ids,omitempty"`
	BoundWorkerIDs    []string `json:"bound_worker_ids,omitempty"`
	VerifierCommands  []string `json:"verifier_commands,omitempty"`
	ContractCoverage  []string `json:"contract_coverage,omitempty"`
	Status            string   `json:"status"`
	EvidenceRefs      []string `json:"evidence_refs,omitempty"`
	ValidationRunRefs []string `json:"validation_run_refs,omitempty"`
	FixFeatureIDs     []string `json:"fix_feature_ids,omitempty"`
	UpdatedAt         string   `json:"updated_at"`
}

type MissionValidationRun struct {
	ObjectKind             string                     `json:"object_kind"`
	SchemaVersion          int                        `json:"schema_version"`
	ValidationRunID        string                     `json:"validation_run_id"`
	MissionID              string                     `json:"mission_id"`
	MilestoneID            string                     `json:"milestone_id,omitempty"`
	RootTaskID             string                     `json:"root_task_id"`
	ValidatorRole          string                     `json:"validator_role,omitempty"`
	ValidatorKind          string                     `json:"validator_kind,omitempty"`
	ValidatorModel         string                     `json:"validator_model,omitempty"`
	ValidatorModelSource   string                     `json:"validator_model_source,omitempty"`
	ValidatorModelExplicit bool                       `json:"validator_model_explicit,omitempty"`
	ValidatorContextRefs   []string                   `json:"validator_context_refs,omitempty"`
	ProviderUsageRef       string                     `json:"provider_usage_ref,omitempty"`
	Status                 string                     `json:"status"`
	Summary                string                     `json:"summary"`
	ContractCoverageCount  int                        `json:"contract_coverage_count,omitempty"`
	Findings               []MissionValidationFinding `json:"findings,omitempty"`
	EvidenceRefs           []string                   `json:"evidence_refs,omitempty"`
	TokenUsage             string                     `json:"token_usage,omitempty"`
	PromptCacheUsage       string                     `json:"prompt_cache_usage,omitempty"`
	CreatedAt              string                     `json:"created_at"`
}

type ProviderUsageRecord struct {
	ObjectKind       string   `json:"object_kind"`
	SchemaVersion    int      `json:"schema_version"`
	UsageRecordID    string   `json:"usage_record_id"`
	TaskID           string   `json:"task_id"`
	TS               string   `json:"ts"`
	Operation        string   `json:"operation"`
	ProviderMode     string   `json:"provider_mode"`
	Model            string   `json:"model,omitempty"`
	TokenUsage       string   `json:"token_usage,omitempty"`
	PromptCacheUsage string   `json:"prompt_cache_usage,omitempty"`
	Cost             string   `json:"cost,omitempty"`
	Refs             []string `json:"refs,omitempty"`
}

type MulticaRunMetadata struct {
	ObjectKind        string `json:"object_kind"`
	SchemaVersion     int    `json:"schema_version"`
	TaskID            string `json:"task_id"`
	SessionID         string `json:"session_id"`
	RunID             string `json:"run_id"`
	Source            string `json:"source"`
	ModelRoute        string `json:"model_route,omitempty"`
	ProviderMode      string `json:"provider_mode,omitempty"`
	ProviderModel     string `json:"provider_model,omitempty"`
	ConfigSource      string `json:"config_source,omitempty"`
	ConfigFingerprint string `json:"config_fingerprint,omitempty"`
	PermissionModeID  string `json:"permission_mode_id,omitempty"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

type WorkspaceGuidanceArtifact struct {
	ObjectKind    string                      `json:"object_kind"`
	SchemaVersion int                         `json:"schema_version"`
	TaskID        string                      `json:"task_id"`
	GeneratedAt   string                      `json:"generated_at"`
	Documents     []WorkspaceGuidanceDocument `json:"documents,omitempty"`
	Skills        []WorkspaceSkill            `json:"skills,omitempty"`
	Refs          []string                    `json:"refs,omitempty"`
}

type WorkspaceGuidanceDocument struct {
	Ref       string `json:"ref"`
	Path      string `json:"path"`
	Content   string `json:"content,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

type WorkspaceSkill struct {
	Name      string `json:"name"`
	Ref       string `json:"ref"`
	Path      string `json:"path"`
	Summary   string `json:"summary,omitempty"`
	Content   string `json:"content,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

type MissionMetricsRecord struct {
	ObjectKind          string            `json:"object_kind"`
	SchemaVersion       int               `json:"schema_version"`
	MetricID            string            `json:"metric_id"`
	MissionID           string            `json:"mission_id"`
	Trigger             string            `json:"trigger"`
	Status              string            `json:"status"`
	WallTimeMS          int64             `json:"wall_time_ms,omitempty"`
	ProviderCallsByRole map[string]int    `json:"provider_calls_by_role,omitempty"`
	RoleModels          map[string]string `json:"role_models,omitempty"`
	ValidatorTimeMS     *int64            `json:"validator_time_ms,omitempty"`
	TaskCount           int               `json:"task_count,omitempty"`
	WorkerCount         int               `json:"worker_count,omitempty"`
	RepairAttemptCount  int               `json:"repair_attempt_count,omitempty"`
	ValidationRunCount  int               `json:"validation_run_count,omitempty"`
	TokenUsage          string            `json:"token_usage,omitempty"`
	PromptCacheUsage    string            `json:"prompt_cache_usage,omitempty"`
	Cost                string            `json:"cost,omitempty"`
	Refs                []string          `json:"refs,omitempty"`
	CreatedAt           string            `json:"created_at"`
}

type MissionMetricsSnapshot struct {
	ObjectKind           string            `json:"object_kind"`
	SchemaVersion        int               `json:"schema_version"`
	MissionID            string            `json:"mission_id"`
	LatestMetricRef      string            `json:"latest_metric_ref,omitempty"`
	TotalWallTimeMS      int64             `json:"total_wall_time_ms,omitempty"`
	ProviderCallsByRole  map[string]int    `json:"provider_calls_by_role,omitempty"`
	RoleModels           map[string]string `json:"role_models,omitempty"`
	TotalValidatorTimeMS int64             `json:"total_validator_time_ms,omitempty"`
	TaskCount            int               `json:"task_count,omitempty"`
	WorkerCount          int               `json:"worker_count,omitempty"`
	RepairAttemptCount   int               `json:"repair_attempt_count,omitempty"`
	ValidationRunCount   int               `json:"validation_run_count,omitempty"`
	TokenUsage           string            `json:"token_usage,omitempty"`
	PromptCacheUsage     string            `json:"prompt_cache_usage,omitempty"`
	Cost                 string            `json:"cost,omitempty"`
}

type MissionStatusSnapshot struct {
	ObjectKind                 string                  `json:"object_kind"`
	SchemaVersion              int                     `json:"schema_version"`
	MissionID                  string                  `json:"mission_id"`
	Status                     string                  `json:"status"`
	StatusReasonCode           string                  `json:"status_reason_code,omitempty"`
	CurrentMilestoneID         string                  `json:"current_milestone_id,omitempty"`
	CurrentFeatureID           string                  `json:"current_feature_id,omitempty"`
	ReadyFeatureIDs            []string                `json:"ready_feature_ids,omitempty"`
	BlockedFeatureIDs          []string                `json:"blocked_feature_ids,omitempty"`
	RootTaskID                 string                  `json:"root_task_id,omitempty"`
	RootTaskState              string                  `json:"root_task_state,omitempty"`
	ActiveTaskIDs              []string                `json:"active_task_ids,omitempty"`
	ActiveWorkerIDs            []string                `json:"active_worker_ids,omitempty"`
	LatestValidationRef        string                  `json:"latest_validation_ref,omitempty"`
	LatestValidationStatus     string                  `json:"latest_validation_status,omitempty"`
	BlockingFindingCount       int                     `json:"blocking_finding_count,omitempty"`
	UnresolvedFixFeatureIDs    []string                `json:"unresolved_fix_feature_ids,omitempty"`
	RecentMissionEvents        []string                `json:"recent_mission_events,omitempty"`
	Metrics                    *MissionMetricsSnapshot `json:"metrics,omitempty"`
	UserTestingValidatorStatus string                  `json:"user_testing_validator_status,omitempty"`
	UserTestingValidatorReason string                  `json:"user_testing_validator_reason,omitempty"`
}

type MissionValidationFinding struct {
	FindingID         string   `json:"finding_id"`
	Category          string   `json:"category"`
	Severity          string   `json:"severity"`
	Blocking          bool     `json:"blocking"`
	Summary           string   `json:"summary"`
	EvidenceRefs      []string `json:"evidence_refs,omitempty"`
	RecommendedAction string   `json:"recommended_action,omitempty"`
}

type MissionCreateRequest struct {
	Title       string   `json:"title"`
	Objective   string   `json:"objective"`
	RootTaskID  string   `json:"root_task_id,omitempty"`
	Criteria    []string `json:"criteria,omitempty"`
	Constraints []string `json:"constraints,omitempty"`
}

type MissionPlanView struct {
	ObjectKind    string              `json:"object_kind"`
	SchemaVersion int                 `json:"schema_version"`
	MissionID     string              `json:"mission_id"`
	Features      MissionFeatureSet   `json:"features"`
	Milestones    MissionMilestoneSet `json:"milestones"`
}

type MissionView struct {
	ObjectKind            string                    `json:"object_kind"`
	SchemaVersion         int                       `json:"schema_version"`
	Mission               Mission                   `json:"mission"`
	Contract              MissionValidationContract `json:"validation_contract"`
	Features              MissionFeatureSet         `json:"features"`
	Milestones            MissionMilestoneSet       `json:"milestones"`
	LatestValidation      *MissionValidationRun     `json:"latest_validation,omitempty"`
	RootTaskStatus        *StatusSnapshot           `json:"root_task_status,omitempty"`
	MissionStatusSnapshot *MissionStatusSnapshot    `json:"mission_status_snapshot,omitempty"`
	Metrics               *MissionMetricsSnapshot   `json:"metrics,omitempty"`
}

type State struct {
	SchemaVersion       int       `json:"schema_version"`
	TaskID              string    `json:"task_id"`
	Phase               Phase     `json:"phase"`
	State               StateName `json:"state"`
	StatusReasonCode    string    `json:"status_reason_code"`
	StatusDetailRef     string    `json:"status_detail_ref"`
	CurrentStepID       string    `json:"current_step_id"`
	PermissionModeID    string    `json:"permission_mode_id"`
	LastEventRef        string    `json:"last_event_ref"`
	LastVerificationRef string    `json:"last_verification_ref"`
	LastReviewRef       string    `json:"last_review_ref"`
	LastCompletionRef   string    `json:"last_completion_ref"`
	LastCheckpointRef   string    `json:"last_checkpoint_ref"`
	UpdatedAt           string    `json:"updated_at"`
}

type Baseline struct {
	SchemaVersion        int                `json:"schema_version"`
	TaskID               string             `json:"task_id"`
	CapturedAt           string             `json:"captured_at"`
	WorkspaceRoot        string             `json:"workspace_root"`
	RepoTruthRefs        []string           `json:"repo_truth_refs"`
	CommandHints         []CommandHint      `json:"command_hints,omitempty"`
	WorkspaceSnapshot    *WorkspaceSnapshot `json:"workspace_snapshot,omitempty"`
	Environment          EnvInfo            `json:"environment"`
	AvailableVerifiers   []string           `json:"available_verifiers"`
	MissingPrerequisites []string           `json:"missing_prerequisites"`
}

type EnvInfo struct {
	OS        string `json:"os"`
	GoVersion string `json:"go_version"`
}

type CommandHint struct {
	Kind      string   `json:"kind"`
	Command   []string `json:"command,omitempty"`
	Reason    string   `json:"reason"`
	SourceRef string   `json:"source_ref,omitempty"`
}

type WorkspaceSnapshot struct {
	Git *GitSummary `json:"git,omitempty"`
}

type GitSummary struct {
	IsRepository  bool        `json:"is_repository"`
	Branch        string      `json:"branch,omitempty"`
	Head          string      `json:"head,omitempty"`
	Dirty         bool        `json:"dirty,omitempty"`
	StatusSummary string      `json:"status_summary,omitempty"`
	ChangedPaths  []string    `json:"changed_paths,omitempty"`
	RecentCommits []GitCommit `json:"recent_commits,omitempty"`
}

type GitCommit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

type VerificationCheck struct {
	Name         string   `json:"name"`
	Status       string   `json:"status"`
	Summary      string   `json:"summary"`
	Command      []string `json:"command,omitempty"`
	EvidenceRefs []string `json:"evidence_refs"`
}

type VerificationReport struct {
	SchemaVersion  int                 `json:"schema_version"`
	TaskID         string              `json:"task_id"`
	ReportID       string              `json:"report_id"`
	Status         string              `json:"status"`
	Profile        string              `json:"profile"`
	RanAt          string              `json:"ran_at"`
	Checks         []VerificationCheck `json:"checks"`
	FailureSummary string              `json:"failure_summary"`
}

type ReviewReport struct {
	SchemaVersion       int               `json:"schema_version"`
	TaskID              string            `json:"task_id"`
	ReviewID            string            `json:"review_id"`
	Status              string            `json:"status"`
	Summary             string            `json:"summary"`
	ReviewerProfile     string            `json:"reviewer_profile,omitempty"`
	ReviewContextRefs   []string          `json:"review_context_refs,omitempty"`
	ChangedPaths        []string          `json:"changed_paths,omitempty"`
	WorkerResultRefs    []string          `json:"worker_result_refs,omitempty"`
	RiskSummary         ReviewRiskSummary `json:"risk_summary,omitempty"`
	BlockingCategories  []string          `json:"blocking_categories,omitempty"`
	BlockingFindingRefs []string          `json:"blocking_finding_refs"`
	ReviewedAt          string            `json:"reviewed_at"`
}

type ReviewRiskSummary struct {
	BlockingCount        int `json:"blocking_count,omitempty"`
	ConfirmedDefects     int `json:"confirmed_defects,omitempty"`
	MissingEvidence      int `json:"missing_evidence,omitempty"`
	ScopeDriftRisks      int `json:"scope_drift_risks,omitempty"`
	ComplexityRisks      int `json:"complexity_risks,omitempty"`
	SecurityRisks        int `json:"security_risks,omitempty"`
	StaleContextRisks    int `json:"stale_context_risks,omitempty"`
	WorkerTrustGaps      int `json:"worker_trust_gaps,omitempty"`
	InferredRiskFindings int `json:"inferred_risk_findings,omitempty"`
	NotObservedFindings  int `json:"not_observed_findings,omitempty"`
}

type CriterionStatus struct {
	CriterionID      string   `json:"criterion_id"`
	Statement        string   `json:"statement,omitempty"`
	Ordinal          int      `json:"ordinal,omitempty"`
	Status           string   `json:"status"`
	Passes           bool     `json:"passes"`
	Selected         bool     `json:"selected,omitempty"`
	LastSummary      string   `json:"last_summary,omitempty"`
	EvidenceRefs     []string `json:"evidence_refs"`
	LastEvaluatedAt  string   `json:"last_evaluated_at,omitempty"`
	LastTransitionAt string   `json:"last_transition_at,omitempty"`
}

type CriteriaSnapshot struct {
	SchemaVersion             int               `json:"schema_version"`
	SnapshotID                string            `json:"snapshot_id"`
	TaskID                    string            `json:"task_id"`
	UpdatedAt                 string            `json:"updated_at"`
	Summary                   string            `json:"summary,omitempty"`
	CurrentCriterionID        string            `json:"current_criterion_id,omitempty"`
	CurrentCriterionStatement string            `json:"current_criterion_statement,omitempty"`
	MetCount                  int               `json:"met_count,omitempty"`
	OpenCount                 int               `json:"open_count,omitempty"`
	Criteria                  []CriterionStatus `json:"criteria"`
}

type CompletionReport struct {
	SchemaVersion    int               `json:"schema_version"`
	TaskID           string            `json:"task_id"`
	CompletionID     string            `json:"completion_id"`
	Status           string            `json:"status"`
	Summary          string            `json:"summary"`
	CriterionResults []CriterionStatus `json:"criterion_results"`
	BlockingRefs     []string          `json:"blocking_refs"`
	HandoffRef       string            `json:"handoff_ref"`
	EvaluatedAt      string            `json:"evaluated_at"`
}

type HarnessEvaluation struct {
	ObjectKind               string         `json:"object_kind"`
	SchemaVersion            int            `json:"schema_version"`
	HarnessEvalID            string         `json:"harness_eval_id"`
	TaskID                   string         `json:"task_id"`
	RuntimeAction            string         `json:"runtime_action"`
	ProviderMode             string         `json:"provider_mode"`
	Model                    string         `json:"model,omitempty"`
	SystemPromptRef          string         `json:"system_prompt_ref,omitempty"`
	DecisionSchemaVersion    string         `json:"decision_schema_version,omitempty"`
	ContextPackRef           string         `json:"context_pack_ref,omitempty"`
	ContinuityRef            string         `json:"continuity_ref,omitempty"`
	SprintRef                string         `json:"sprint_ref,omitempty"`
	CriteriaRef              string         `json:"criteria_ref,omitempty"`
	RepairBudget             int            `json:"repair_budget"`
	ObservationCommandBudget int            `json:"observation_command_budget"`
	ExecutionCommandBudget   int            `json:"execution_command_budget"`
	ActionsSelected          []string       `json:"actions_selected,omitempty"`
	RepairAttemptCount       int            `json:"repair_attempt_count,omitempty"`
	VerificationStatus       string         `json:"verification_status,omitempty"`
	CriteriaMetCount         int            `json:"criteria_met_count,omitempty"`
	CriteriaOpenCount        int            `json:"criteria_open_count,omitempty"`
	ReviewStatus             string         `json:"review_status,omitempty"`
	CompletionStatus         string         `json:"completion_status,omitempty"`
	BlockedReasonCode        string         `json:"blocked_reason_code,omitempty"`
	WorkspaceEditStatuses    map[string]int `json:"workspace_edit_statuses,omitempty"`
	WorkerActionCount        int            `json:"worker_action_count,omitempty"`
	MemoryPromoteCount       int            `json:"memory_promote_count,omitempty"`
	ProviderUsageRef         string         `json:"provider_usage_ref,omitempty"`
	TokenUsage               string         `json:"token_usage,omitempty"`
	PromptCacheUsage         string         `json:"prompt_cache_usage,omitempty"`
	Summary                  string         `json:"summary"`
	EvidenceRefs             []string       `json:"evidence_refs,omitempty"`
	CreatedAt                string         `json:"created_at"`
}

type Event struct {
	SchemaVersion int       `json:"schema_version"`
	EventID       string    `json:"event_id"`
	TaskID        string    `json:"task_id"`
	TS            string    `json:"ts"`
	Phase         Phase     `json:"phase"`
	State         StateName `json:"state"`
	Type          string    `json:"type"`
	Summary       string    `json:"summary"`
	Refs          []string  `json:"refs,omitempty"`
}

type Finding struct {
	SchemaVersion     int      `json:"schema_version"`
	FindingID         string   `json:"finding_id"`
	TaskID            string   `json:"task_id"`
	TS                string   `json:"ts"`
	Severity          string   `json:"severity"`
	Category          string   `json:"category"`
	Status            string   `json:"status"`
	BlocksCompletion  bool     `json:"blocks_completion"`
	Claim             string   `json:"claim"`
	EvidenceRefs      []string `json:"evidence_refs"`
	AffectedPaths     []string `json:"affected_paths,omitempty"`
	RecommendedAction string   `json:"recommended_action"`
}

type ApprovalRecord struct {
	SchemaVersion    int    `json:"schema_version"`
	ApprovalRecordID string `json:"approval_record_id"`
	ApprovalID       string `json:"approval_id"`
	TaskID           string `json:"task_id"`
	OwnerTaskID      string `json:"owner_task_id,omitempty"`
	OwnerWorkerID    string `json:"owner_worker_id,omitempty"`
	TS               string `json:"ts"`
	Kind             string `json:"kind"`
	Status           string `json:"status"`
	Scope            string `json:"scope"`
	Reason           string `json:"reason"`
}

type OwnedApprovalSummary struct {
	SchemaVersion        int       `json:"schema_version"`
	WorkerID             string    `json:"worker_id"`
	ChildTaskID          string    `json:"child_task_id"`
	ApprovalID           string    `json:"approval_id"`
	ApprovalRef          string    `json:"approval_ref"`
	Status               string    `json:"status"`
	Scope                string    `json:"scope"`
	Reason               string    `json:"reason"`
	ChildState           StateName `json:"child_state"`
	BlockedReasonCode    string    `json:"blocked_reason_code,omitempty"`
	RequiresParentAction bool      `json:"requires_parent_action"`
	ParentActionType     string    `json:"parent_action_type,omitempty"`
	ParentActionOptions  []string  `json:"parent_action_options,omitempty"`
	ParentActionSummary  string    `json:"parent_action_summary,omitempty"`
}

type InputRequestRecord struct {
	SchemaVersion int    `json:"schema_version"`
	InputRecordID string `json:"input_record_id"`
	RequestID     string `json:"request_id"`
	TaskID        string `json:"task_id"`
	TS            string `json:"ts"`
	Kind          string `json:"kind"`
	Status        string `json:"status"`
	Field         string `json:"field"`
	Prompt        string `json:"prompt"`
	Response      string `json:"response,omitempty"`
	Required      bool   `json:"required"`
}

type WorkspaceFileChange struct {
	Path         string `json:"path"`
	Action       string `json:"action"`
	BeforeExists bool   `json:"before_exists"`
	AfterExists  bool   `json:"after_exists"`
	BeforeSHA256 string `json:"before_sha256,omitempty"`
	AfterSHA256  string `json:"after_sha256,omitempty"`
}

type WorkspaceEditRecord struct {
	SchemaVersion    int                   `json:"schema_version"`
	EditRecordID     string                `json:"edit_record_id"`
	EditID           string                `json:"edit_id"`
	TaskID           string                `json:"task_id"`
	TS               string                `json:"ts"`
	Kind             string                `json:"kind"`
	Status           string                `json:"status"`
	ProviderMode     string                `json:"provider_mode"`
	Summary          string                `json:"summary"`
	ProviderUsageRef string                `json:"provider_usage_ref,omitempty"`
	TokenUsage       string                `json:"token_usage,omitempty"`
	PromptCacheUsage string                `json:"prompt_cache_usage,omitempty"`
	FileChanges      []WorkspaceFileChange `json:"file_changes,omitempty"`
	ReplaySafety     *ReplaySafety         `json:"replay_safety,omitempty"`
}

type CommandRunRecord struct {
	SchemaVersion    int           `json:"schema_version"`
	CommandRecordID  string        `json:"command_record_id"`
	CommandID        string        `json:"command_id"`
	TaskID           string        `json:"task_id"`
	TS               string        `json:"ts"`
	Kind             string        `json:"kind"`
	Status           string        `json:"status"`
	Summary          string        `json:"summary"`
	Argv             []string      `json:"argv"`
	PermissionModeID string        `json:"permission_mode_id,omitempty"`
	PolicyDecision   string        `json:"policy_decision,omitempty"`
	ExitCode         int           `json:"exit_code"`
	TimedOut         bool          `json:"timed_out,omitempty"`
	StdoutRef        string        `json:"stdout_ref,omitempty"`
	StderrRef        string        `json:"stderr_ref,omitempty"`
	StdoutExcerpt    string        `json:"stdout_excerpt,omitempty"`
	StderrExcerpt    string        `json:"stderr_excerpt,omitempty"`
	StdoutTruncated  bool          `json:"stdout_truncated,omitempty"`
	StderrTruncated  bool          `json:"stderr_truncated,omitempty"`
	ReplaySafety     *ReplaySafety `json:"replay_safety,omitempty"`
}

type ReplaySafety struct {
	SideEffectClass string `json:"side_effect_class"`
	ReplayPolicy    string `json:"replay_policy"`
	ReadOnly        bool   `json:"read_only,omitempty"`
	WritesWorkspace bool   `json:"writes_workspace,omitempty"`
	Network         bool   `json:"network,omitempty"`
	Destructive     bool   `json:"destructive,omitempty"`
	OpenWorld       bool   `json:"open_world,omitempty"`
	Idempotent      bool   `json:"idempotent,omitempty"`
	Summary         string `json:"summary,omitempty"`
}

type Diagnostic struct {
	SchemaVersion int      `json:"schema_version"`
	DiagnosticID  string   `json:"diagnostic_id"`
	TaskID        string   `json:"task_id"`
	ReasonCode    string   `json:"reason_code"`
	Summary       string   `json:"summary"`
	BrokenRefs    []string `json:"broken_refs"`
	EvidenceRefs  []string `json:"evidence_refs"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
}

type QualityDiagnostic struct {
	ObjectKind               string           `json:"object_kind"`
	SchemaVersion            int              `json:"schema_version"`
	DiagnosticID             string           `json:"diagnostic_id"`
	TaskID                   string           `json:"task_id"`
	Status                   string           `json:"status"`
	ChangedPathCount         int              `json:"changed_path_count,omitempty"`
	ChangedPaths             []string         `json:"changed_paths,omitempty"`
	TestFileChanges          []string         `json:"test_file_changes,omitempty"`
	GeneratedFileChanges     []string         `json:"generated_file_changes,omitempty"`
	WorkspaceEditAttempts    int              `json:"workspace_edit_attempts,omitempty"`
	NoopOrFailedEditCount    int              `json:"noop_or_failed_edit_count,omitempty"`
	SameFailureCount         int              `json:"same_failure_count,omitempty"`
	SameFileRewriteCount     int              `json:"same_file_rewrite_count,omitempty"`
	ScopeDriftPaths          []string         `json:"scope_drift_paths,omitempty"`
	SprintBoundaryViolations []string         `json:"sprint_boundary_violations,omitempty"`
	ProjectFocusViolations   []string         `json:"project_focus_violations,omitempty"`
	LargePatchWarning        bool             `json:"large_patch_warning,omitempty"`
	NewDependencyWarning     bool             `json:"new_dependency_warning,omitempty"`
	NewAbstractionWarning    bool             `json:"new_abstraction_warning,omitempty"`
	ReviewRequired           bool             `json:"review_required,omitempty"`
	BlockCompletion          bool             `json:"block_completion,omitempty"`
	RecommendedAction        string           `json:"recommended_action,omitempty"`
	Findings                 []QualityFinding `json:"findings,omitempty"`
	EvidenceRefs             []string         `json:"evidence_refs,omitempty"`
	CreatedAt                string           `json:"created_at"`
}

type QualityFinding struct {
	Category          string   `json:"category"`
	Severity          string   `json:"severity"`
	Blocking          bool     `json:"blocking"`
	Summary           string   `json:"summary"`
	AffectedPaths     []string `json:"affected_paths,omitempty"`
	EvidenceRefs      []string `json:"evidence_refs,omitempty"`
	RecommendedAction string   `json:"recommended_action,omitempty"`
}

type Checkpoint struct {
	SchemaVersion     int                `json:"schema_version"`
	CheckpointID      string             `json:"checkpoint_id"`
	TaskID            string             `json:"task_id"`
	CapturedAt        string             `json:"captured_at"`
	Phase             Phase              `json:"phase"`
	State             StateName          `json:"state"`
	LastEventRef      string             `json:"last_event_ref"`
	WorkspaceSnapshot *WorkspaceSnapshot `json:"workspace_snapshot,omitempty"`
}

type Watch struct {
	SchemaVersion   int    `json:"schema_version"`
	WatchID         string `json:"watch_id"`
	TaskID          string `json:"task_id"`
	Status          string `json:"status"`
	IntervalSeconds int    `json:"interval_seconds"`
	Reason          string `json:"reason"`
	NextWakeAt      string `json:"next_wake_at"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

type ContextSummary struct {
	SchemaVersion    int                 `json:"schema_version"`
	TaskID           string              `json:"task_id"`
	PackID           string              `json:"pack_id,omitempty"`
	Phase            Phase               `json:"phase,omitempty"`
	State            StateName           `json:"state,omitempty"`
	BuiltAt          string              `json:"built_at,omitempty"`
	UpdatedAt        string              `json:"updated_at,omitempty"`
	Summary          string              `json:"summary,omitempty"`
	NextStep         string              `json:"next_step,omitempty"`
	BasedOnRefs      []string            `json:"based_on_refs,omitempty"`
	IncludedRefs     []string            `json:"included_refs,omitempty"`
	Sections         []ContextSection    `json:"sections,omitempty"`
	Compaction       ContextCompaction   `json:"compaction,omitempty"`
	ProjectFocus     *ProjectTaskContext `json:"project_focus,omitempty"`
	StatusReasonCode string              `json:"status_reason_code,omitempty"`
}

type ContextSection struct {
	Name         string   `json:"name"`
	TokenBudget  int      `json:"token_budget,omitempty"`
	ActualTokens int      `json:"actual_tokens,omitempty"`
	Refs         []string `json:"refs,omitempty"`
}

type ContextCompaction struct {
	Performed  bool   `json:"performed"`
	SummaryRef string `json:"summary_ref,omitempty"`
}

type ContinuityChecklistItem struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind,omitempty"`
	Title   string   `json:"title"`
	Ref     string   `json:"ref,omitempty"`
	Command []string `json:"command,omitempty"`
	Reason  string   `json:"reason,omitempty"`
}

type ContinuityFocus struct {
	CurrentStepID             string              `json:"current_step_id,omitempty"`
	CurrentStepTitle          string              `json:"current_step_title,omitempty"`
	CurrentExecutionStepID    string              `json:"current_execution_step_id,omitempty"`
	CurrentExecutionStepTitle string              `json:"current_execution_step_title,omitempty"`
	CurrentSystemStepID       string              `json:"current_system_step_id,omitempty"`
	CurrentSystemStepTitle    string              `json:"current_system_step_title,omitempty"`
	PrimaryCriterionID        string              `json:"primary_criterion_id,omitempty"`
	PrimaryCriterionStatement string              `json:"primary_criterion_statement,omitempty"`
	CriterionIDs              []string            `json:"criterion_ids,omitempty"`
	Criteria                  []SuccessCriterion  `json:"criteria,omitempty"`
	WorkingSetPaths           []string            `json:"working_set_paths,omitempty"`
	ProjectFocus              *ProjectTaskContext `json:"project_focus,omitempty"`
}

type ContinuitySnapshot struct {
	SchemaVersion       int                       `json:"schema_version"`
	SnapshotID          string                    `json:"snapshot_id"`
	TaskID              string                    `json:"task_id"`
	UpdatedAt           string                    `json:"updated_at"`
	Phase               Phase                     `json:"phase,omitempty"`
	State               StateName                 `json:"state,omitempty"`
	StatusReasonCode    string                    `json:"status_reason_code,omitempty"`
	Summary             string                    `json:"summary,omitempty"`
	NextStep            string                    `json:"next_step,omitempty"`
	CurrentFocus        ContinuityFocus           `json:"current_focus,omitempty"`
	StartupChecklist    []ContinuityChecklistItem `json:"startup_checklist,omitempty"`
	CriteriaMetCount    int                       `json:"criteria_met_count,omitempty"`
	CriteriaTotalCount  int                       `json:"criteria_total_count,omitempty"`
	OpenCriteria        []SuccessCriterion        `json:"open_criteria,omitempty"`
	VerificationStatus  string                    `json:"verification_status,omitempty"`
	VerificationSummary string                    `json:"verification_summary,omitempty"`
	ReviewStatus        string                    `json:"review_status,omitempty"`
	ReviewSummary       string                    `json:"review_summary,omitempty"`
	CompletionStatus    string                    `json:"completion_status,omitempty"`
	CompletionSummary   string                    `json:"completion_summary,omitempty"`
	Refs                []string                  `json:"refs,omitempty"`
}

type SprintSnapshot struct {
	SchemaVersion             int                 `json:"schema_version"`
	SnapshotID                string              `json:"snapshot_id"`
	TaskID                    string              `json:"task_id"`
	UpdatedAt                 string              `json:"updated_at"`
	Summary                   string              `json:"summary,omitempty"`
	Objective                 string              `json:"objective,omitempty"`
	Boundary                  string              `json:"boundary,omitempty"`
	CurrentStepID             string              `json:"current_step_id,omitempty"`
	CurrentStepTitle          string              `json:"current_step_title,omitempty"`
	CurrentExecutionStepID    string              `json:"current_execution_step_id,omitempty"`
	CurrentExecutionStepTitle string              `json:"current_execution_step_title,omitempty"`
	CurrentSystemStepID       string              `json:"current_system_step_id,omitempty"`
	CurrentSystemStepTitle    string              `json:"current_system_step_title,omitempty"`
	PrimaryCriterionID        string              `json:"primary_criterion_id,omitempty"`
	PrimaryCriterionStatement string              `json:"primary_criterion_statement,omitempty"`
	ActiveCriterionIDs        []string            `json:"active_criterion_ids,omitempty"`
	DeferredCriterionIDs      []string            `json:"deferred_criterion_ids,omitempty"`
	CompletionSignals         []string            `json:"completion_signals,omitempty"`
	WorkingSetPaths           []string            `json:"working_set_paths,omitempty"`
	ProjectFocus              *ProjectTaskContext `json:"project_focus,omitempty"`
	Refs                      []string            `json:"refs,omitempty"`
}

type Session struct {
	SchemaVersion int    `json:"schema_version"`
	SessionID     string `json:"session_id"`
	TaskID        string `json:"task_id"`
	Mode          string `json:"mode"`
	Status        string `json:"status"`
	LastPrompt    string `json:"last_prompt"`
	LastAction    string `json:"last_action"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type SessionMessage struct {
	SchemaVersion int    `json:"schema_version"`
	MessageID     string `json:"message_id"`
	SessionID     string `json:"session_id"`
	TaskID        string `json:"task_id"`
	Role          string `json:"role"`
	Content       string `json:"content"`
	TS            string `json:"ts"`
}

type SessionSnapshot struct {
	ObjectKind            string                 `json:"object_kind"`
	SchemaVersion         int                    `json:"schema_version"`
	SessionID             string                 `json:"session_id"`
	TaskID                string                 `json:"task_id"`
	Mode                  string                 `json:"mode"`
	SessionStatus         string                 `json:"session_status"`
	LastPrompt            string                 `json:"last_prompt"`
	LastAction            string                 `json:"last_action"`
	SessionRef            string                 `json:"session_ref"`
	MessagesRef           string                 `json:"messages_ref"`
	MessageCount          int                    `json:"message_count"`
	RecentMessages        []SessionMessage       `json:"recent_messages,omitempty"`
	ManagedWorkers        []WorkerContract       `json:"managed_workers,omitempty"`
	OwnedPendingApprovals []OwnedApprovalSummary `json:"owned_pending_approvals,omitempty"`
	StatusSnapshot        StatusSnapshot         `json:"status_snapshot"`
	UpdatedAt             string                 `json:"updated_at"`
}

type WorkerSnapshot struct {
	ObjectKind    string         `json:"object_kind"`
	SchemaVersion int            `json:"schema_version"`
	Worker        WorkerContract `json:"worker"`
	ParentStatus  StatusSnapshot `json:"parent_status"`
	ChildStatus   StatusSnapshot `json:"child_status"`
	UpdatedAt     string         `json:"updated_at"`
}

type WorkerContract struct {
	SchemaVersion              int             `json:"schema_version"`
	WorkerID                   string          `json:"worker_id"`
	ParentTaskID               string          `json:"parent_task_id"`
	ChildTaskID                string          `json:"child_task_id"`
	RootTaskID                 string          `json:"root_task_id,omitempty"`
	LineageDepth               int             `json:"lineage_depth,omitempty"`
	Role                       string          `json:"role"`
	Objective                  string          `json:"objective"`
	Status                     string          `json:"status"`
	HandoffRef                 string          `json:"handoff_ref"`
	SubagentPolicy             *SubagentPolicy `json:"subagent_policy,omitempty"`
	BlockedReasonCode          string          `json:"blocked_reason_code,omitempty"`
	BlockedDetailRef           string          `json:"blocked_detail_ref,omitempty"`
	ApprovalID                 string          `json:"approval_id,omitempty"`
	ApprovalRef                string          `json:"approval_ref,omitempty"`
	ApprovalStatus             string          `json:"approval_status,omitempty"`
	ApprovalScope              string          `json:"approval_scope,omitempty"`
	ApprovalReason             string          `json:"approval_reason,omitempty"`
	InputRequestID             string          `json:"input_request_id,omitempty"`
	InputRequestRef            string          `json:"input_request_ref,omitempty"`
	InputField                 string          `json:"input_field,omitempty"`
	InputPrompt                string          `json:"input_prompt,omitempty"`
	WorkspaceRoot              string          `json:"workspace_root,omitempty"`
	WorkspaceMode              string          `json:"workspace_mode,omitempty"`
	WorkspaceStatus            string          `json:"workspace_status,omitempty"`
	WorkspaceRef               string          `json:"workspace_ref,omitempty"`
	SettlementStatus           string          `json:"settlement_status,omitempty"`
	SettlementSummary          string          `json:"settlement_summary,omitempty"`
	SettlementRef              string          `json:"settlement_ref,omitempty"`
	ResultSummary              string          `json:"result_summary,omitempty"`
	ResultRef                  string          `json:"result_ref,omitempty"`
	CompletionStatus           string          `json:"completion_status,omitempty"`
	ReviewStatus               string          `json:"review_status,omitempty"`
	VerificationStatus         string          `json:"verification_status,omitempty"`
	ReconcileMode              string          `json:"reconcile_mode,omitempty"`
	ReconcileStatus            string          `json:"reconcile_status,omitempty"`
	ReconcileSummary           string          `json:"reconcile_summary,omitempty"`
	ReconcileRef               string          `json:"reconcile_ref,omitempty"`
	EvidenceScore              int             `json:"evidence_score,omitempty"`
	EvidenceGrade              string          `json:"evidence_grade,omitempty"`
	MissingEvidence            []string        `json:"missing_evidence,omitempty"`
	Verified                   bool            `json:"verified,omitempty"`
	ReviewClear                bool            `json:"review_clear,omitempty"`
	HandoffPresent             bool            `json:"handoff_present,omitempty"`
	CriteriaClosed             bool            `json:"criteria_closed,omitempty"`
	SettlementAccepted         bool            `json:"settlement_accepted,omitempty"`
	ReconcileClean             bool            `json:"reconcile_clean,omitempty"`
	ParentActionUnresolved     bool            `json:"parent_action_unresolved,omitempty"`
	ConflictCount              int             `json:"conflict_count,omitempty"`
	TrustedForParentCompletion bool            `json:"trusted_for_parent_completion,omitempty"`
	RequiresParentAction       bool            `json:"requires_parent_action,omitempty"`
	ParentActionType           string          `json:"parent_action_type,omitempty"`
	ParentActionOptions        []string        `json:"parent_action_options,omitempty"`
	ParentActionSummary        string          `json:"parent_action_summary,omitempty"`
	ContinuationCount          int             `json:"continuation_count,omitempty"`
	LastContinuedAt            string          `json:"last_continued_at,omitempty"`
	LastReconciledAt           string          `json:"last_reconciled_at,omitempty"`
	CreatedAt                  string          `json:"created_at"`
	UpdatedAt                  string          `json:"updated_at"`
}

type WorkerSettlement struct {
	SchemaVersion      int       `json:"schema_version"`
	SettlementID       string    `json:"settlement_id"`
	WorkerID           string    `json:"worker_id"`
	ParentTaskID       string    `json:"parent_task_id"`
	ChildTaskID        string    `json:"child_task_id"`
	Status             string    `json:"status"`
	ChildState         StateName `json:"child_state"`
	CompletionStatus   string    `json:"completion_status,omitempty"`
	ReviewStatus       string    `json:"review_status,omitempty"`
	VerificationStatus string    `json:"verification_status,omitempty"`
	Summary            string    `json:"summary"`
	EvidenceRefs       []string  `json:"evidence_refs,omitempty"`
	CreatedAt          string    `json:"created_at"`
	UpdatedAt          string    `json:"updated_at"`
	SettledAt          string    `json:"settled_at,omitempty"`
}

type WorkerWorkspace struct {
	SchemaVersion  int    `json:"schema_version"`
	WorkerID       string `json:"worker_id"`
	ParentTaskID   string `json:"parent_task_id"`
	ChildTaskID    string `json:"child_task_id,omitempty"`
	RequestedMode  string `json:"requested_mode"`
	EffectiveMode  string `json:"effective_mode"`
	Status         string `json:"status"`
	WorkspaceRoot  string `json:"workspace_root"`
	RepoRoot       string `json:"repo_root,omitempty"`
	BaselineRef    string `json:"baseline_ref,omitempty"`
	Reason         string `json:"reason,omitempty"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
	ReleasedAt     string `json:"released_at,omitempty"`
	ReleaseSummary string `json:"release_summary,omitempty"`
}

type WorkerWorkspaceBaselineEntry struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Kind   string `json:"kind,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

type WorkerWorkspaceBaseline struct {
	SchemaVersion int                            `json:"schema_version"`
	BaselineID    string                         `json:"baseline_id"`
	WorkerID      string                         `json:"worker_id"`
	ParentTaskID  string                         `json:"parent_task_id"`
	ChildTaskID   string                         `json:"child_task_id"`
	FileCount     int                            `json:"file_count"`
	Entries       []WorkerWorkspaceBaselineEntry `json:"entries"`
	CreatedAt     string                         `json:"created_at"`
	UpdatedAt     string                         `json:"updated_at"`
}

type WorkerReconcileFileChange struct {
	Path           string `json:"path"`
	Action         string `json:"action"`
	BaselineExists bool   `json:"baseline_exists"`
	BaselineKind   string `json:"baseline_kind,omitempty"`
	BaselineSHA256 string `json:"baseline_sha256,omitempty"`
	ParentExists   bool   `json:"parent_exists"`
	ParentKind     string `json:"parent_kind,omitempty"`
	ParentSHA256   string `json:"parent_sha256,omitempty"`
	ChildExists    bool   `json:"child_exists"`
	ChildKind      string `json:"child_kind,omitempty"`
	ChildSHA256    string `json:"child_sha256,omitempty"`
	Status         string `json:"status"`
	Summary        string `json:"summary,omitempty"`
}

type WorkerReconcile struct {
	SchemaVersion          int                         `json:"schema_version"`
	ReconcileID            string                      `json:"reconcile_id"`
	WorkerID               string                      `json:"worker_id"`
	ParentTaskID           string                      `json:"parent_task_id"`
	ChildTaskID            string                      `json:"child_task_id"`
	Role                   string                      `json:"role"`
	Mode                   string                      `json:"mode"`
	Status                 string                      `json:"status"`
	Summary                string                      `json:"summary"`
	SettlementStatus       string                      `json:"settlement_status,omitempty"`
	SettlementSettledAt    string                      `json:"settlement_settled_at,omitempty"`
	ChangeCount            int                         `json:"change_count,omitempty"`
	AppliedCount           int                         `json:"applied_count,omitempty"`
	ConflictCount          int                         `json:"conflict_count,omitempty"`
	PartialApply           bool                        `json:"partial_apply,omitempty"`
	ParentTakeoverRequired bool                        `json:"parent_takeover_required,omitempty"`
	ParentTakeoverSummary  string                      `json:"parent_takeover_summary,omitempty"`
	ParentTakeoverRefs     []string                    `json:"parent_takeover_refs,omitempty"`
	WorkspaceEditRef       string                      `json:"workspace_edit_ref,omitempty"`
	EvidenceRefs           []string                    `json:"evidence_refs,omitempty"`
	FileChanges            []WorkerReconcileFileChange `json:"file_changes,omitempty"`
	CreatedAt              string                      `json:"created_at"`
	UpdatedAt              string                      `json:"updated_at"`
	ReconciledAt           string                      `json:"reconciled_at,omitempty"`
	AppliedAt              string                      `json:"applied_at,omitempty"`
}

type WorkerResult struct {
	SchemaVersion              int       `json:"schema_version"`
	ResultID                   string    `json:"result_id"`
	WorkerID                   string    `json:"worker_id"`
	ParentTaskID               string    `json:"parent_task_id"`
	ChildTaskID                string    `json:"child_task_id"`
	Role                       string    `json:"role"`
	Objective                  string    `json:"objective"`
	ChildState                 StateName `json:"child_state"`
	SettlementStatus           string    `json:"settlement_status,omitempty"`
	CompletionStatus           string    `json:"completion_status,omitempty"`
	CompletionSummary          string    `json:"completion_summary,omitempty"`
	ReviewStatus               string    `json:"review_status,omitempty"`
	ReviewSummary              string    `json:"review_summary,omitempty"`
	VerificationStatus         string    `json:"verification_status,omitempty"`
	VerificationSummary        string    `json:"verification_summary,omitempty"`
	HandoffRef                 string    `json:"handoff_ref,omitempty"`
	CompletionRef              string    `json:"completion_ref,omitempty"`
	ReviewRef                  string    `json:"review_ref,omitempty"`
	VerificationRef            string    `json:"verification_ref,omitempty"`
	CriteriaRef                string    `json:"criteria_ref,omitempty"`
	BlockedReasonCode          string    `json:"blocked_reason_code,omitempty"`
	BlockedDetailRef           string    `json:"blocked_detail_ref,omitempty"`
	ApprovalID                 string    `json:"approval_id,omitempty"`
	ApprovalRef                string    `json:"approval_ref,omitempty"`
	ApprovalStatus             string    `json:"approval_status,omitempty"`
	ApprovalScope              string    `json:"approval_scope,omitempty"`
	ApprovalReason             string    `json:"approval_reason,omitempty"`
	InputRequestID             string    `json:"input_request_id,omitempty"`
	InputRequestRef            string    `json:"input_request_ref,omitempty"`
	InputField                 string    `json:"input_field,omitempty"`
	InputPrompt                string    `json:"input_prompt,omitempty"`
	RequiresParentAction       bool      `json:"requires_parent_action,omitempty"`
	ParentActionType           string    `json:"parent_action_type,omitempty"`
	ParentActionOptions        []string  `json:"parent_action_options,omitempty"`
	ParentActionSummary        string    `json:"parent_action_summary,omitempty"`
	EvidenceScore              int       `json:"evidence_score,omitempty"`
	EvidenceGrade              string    `json:"evidence_grade,omitempty"`
	MissingEvidence            []string  `json:"missing_evidence,omitempty"`
	Verified                   bool      `json:"verified,omitempty"`
	ReviewClear                bool      `json:"review_clear,omitempty"`
	HandoffPresent             bool      `json:"handoff_present,omitempty"`
	CriteriaClosed             bool      `json:"criteria_closed,omitempty"`
	SettlementAccepted         bool      `json:"settlement_accepted,omitempty"`
	ReconcileClean             bool      `json:"reconcile_clean,omitempty"`
	ParentActionUnresolved     bool      `json:"parent_action_unresolved,omitempty"`
	ConflictCount              int       `json:"conflict_count,omitempty"`
	TrustedForParentCompletion bool      `json:"trusted_for_parent_completion,omitempty"`
	Summary                    string    `json:"summary"`
	EvidenceRefs               []string  `json:"evidence_refs,omitempty"`
	CreatedAt                  string    `json:"created_at"`
	UpdatedAt                  string    `json:"updated_at"`
}

type MemoryEntry struct {
	SchemaVersion    int      `json:"schema_version"`
	EntryID          string   `json:"entry_id"`
	TaskID           string   `json:"task_id"`
	Kind             string   `json:"kind"`
	Source           string   `json:"source,omitempty"`
	Scope            string   `json:"scope,omitempty"`
	Paths            []string `json:"paths,omitempty"`
	Profiles         []string `json:"profiles,omitempty"`
	ProviderModes    []string `json:"provider_modes,omitempty"`
	StaleAfter       string   `json:"stale_after,omitempty"`
	Supersedes       []string `json:"supersedes,omitempty"`
	SupersededBy     string   `json:"superseded_by,omitempty"`
	Confidence       string   `json:"confidence,omitempty"`
	FreshnessStatus  string   `json:"freshness_status,omitempty"`
	LastValidatedRef string   `json:"last_validated_ref,omitempty"`
	Summary          string   `json:"summary"`
	Refs             []string `json:"refs"`
	CreatedAt        string   `json:"created_at"`
}

type RoleContract struct {
	SchemaVersion            int      `json:"schema_version"`
	RoleID                   string   `json:"role_id"`
	ProfileKind              Kind     `json:"profile_kind"`
	Description              string   `json:"description,omitempty"`
	AllowedProviderActions   []string `json:"allowed_provider_actions"`
	AllowedWorkerRoles       []string `json:"allowed_worker_roles,omitempty"`
	WorkspaceIsolation       string   `json:"workspace_isolation,omitempty"`
	ReconcileMode            string   `json:"reconcile_mode,omitempty"`
	PermissionModeID         string   `json:"permission_mode_id,omitempty"`
	ContextSections          []string `json:"context_sections,omitempty"`
	ReviewRequirements       []string `json:"review_requirements,omitempty"`
	VerificationRequirements []string `json:"verification_requirements,omitempty"`
	MemoryPolicy             string   `json:"memory_policy,omitempty"`
	OutputContract           string   `json:"output_contract,omitempty"`
}

type MemoryPromotion struct {
	Kind    string   `json:"kind,omitempty"`
	Summary string   `json:"summary"`
	Refs    []string `json:"refs,omitempty"`
}

type ACPNotification struct {
	ObjectKind      string              `json:"object_kind"`
	SchemaVersion   int                 `json:"schema_version"`
	NotificationID  string              `json:"notification_id"`
	Kind            string              `json:"kind"`
	TaskID          string              `json:"task_id"`
	SessionID       string              `json:"session_id,omitempty"`
	WorkerID        string              `json:"worker_id,omitempty"`
	Summary         string              `json:"summary"`
	TS              string              `json:"ts"`
	StatusSnapshot  *StatusSnapshot     `json:"status_snapshot,omitempty"`
	SessionSnapshot *SessionSnapshot    `json:"session_snapshot,omitempty"`
	WorkerSnapshot  *WorkerSnapshot     `json:"worker_snapshot,omitempty"`
	InputRequest    *InputRequestRecord `json:"input_request,omitempty"`
	Approval        *ApprovalRecord     `json:"approval,omitempty"`
	Events          []Event             `json:"events,omitempty"`
}

type StatusSnapshot struct {
	ObjectKind                 string        `json:"object_kind"`
	SchemaVersion              int           `json:"schema_version"`
	TaskID                     string        `json:"task_id"`
	Phase                      Phase         `json:"phase"`
	State                      StateName     `json:"state"`
	StatusReasonCode           string        `json:"status_reason_code"`
	StatusDetailRef            string        `json:"status_detail_ref"`
	PlanRef                    string        `json:"plan_ref"`
	ProgressRef                string        `json:"progress_ref"`
	HandoffRef                 string        `json:"handoff_ref"`
	MissionID                  string        `json:"mission_id,omitempty"`
	MissionRef                 string        `json:"mission_ref,omitempty"`
	MissionStatus              string        `json:"mission_status,omitempty"`
	MissionStatusReasonCode    string        `json:"mission_status_reason_code,omitempty"`
	MissionCurrentMilestoneID  string        `json:"mission_current_milestone_id,omitempty"`
	MissionLatestValidationRef string        `json:"mission_latest_validation_ref,omitempty"`
	LastCheckpointRef          string        `json:"last_checkpoint_ref,omitempty"`
	RestoreClues               []RestoreClue `json:"restore_clues,omitempty"`
	PlanRevision               int           `json:"plan_revision,omitempty"`
	CurrentStepID              string        `json:"current_step_id,omitempty"`
	CurrentSystemStepID        string        `json:"current_system_step_id,omitempty"`
	CurrentExecutionStepID     string        `json:"current_execution_step_id,omitempty"`
	LastEventRef               string        `json:"last_event_ref"`
	LastVerificationRef        string        `json:"last_verification_ref"`
	LastReviewRef              string        `json:"last_review_ref"`
	CompletionRef              string        `json:"completion_ref"`
	UpdatedAt                  string        `json:"updated_at"`
}

type RestoreClue struct {
	Ref          string        `json:"ref"`
	Summary      string        `json:"summary"`
	Git          *GitSummary   `json:"git,omitempty"`
	CommandHints []CommandHint `json:"command_hints,omitempty"`
}

type TaskView struct {
	ObjectKind    string         `json:"object_kind"`
	SchemaVersion int            `json:"schema_version"`
	Task          Spec           `json:"task"`
	State         State          `json:"state"`
	Plan          Plan           `json:"plan"`
	Status        StatusSnapshot `json:"status"`
}

type ProjectView struct {
	ObjectKind    string  `json:"object_kind"`
	SchemaVersion int     `json:"schema_version"`
	Project       Project `json:"project"`
}

type TaskListEntry struct {
	ObjectKind             string    `json:"object_kind"`
	SchemaVersion          int       `json:"schema_version"`
	TaskID                 string    `json:"task_id"`
	Title                  string    `json:"title,omitempty"`
	Kind                   Kind      `json:"kind"`
	Phase                  Phase     `json:"phase"`
	State                  StateName `json:"state"`
	StatusReasonCode       string    `json:"status_reason_code,omitempty"`
	PlanRevision           int       `json:"plan_revision,omitempty"`
	CurrentStepID          string    `json:"current_step_id,omitempty"`
	CurrentSystemStepID    string    `json:"current_system_step_id,omitempty"`
	CurrentExecutionStepID string    `json:"current_execution_step_id,omitempty"`
	UpdatedAt              string    `json:"updated_at"`
}

type Config struct {
	StateDir       string             `json:"state_dir"`
	DefaultProfile Kind               `json:"default_profile"`
	Verification   VerificationConfig `json:"verification"`
	Watch          WatchConfig        `json:"watch"`
	Scheduler      SchedulerConfig    `json:"scheduler"`
	Provider       ProviderConfig     `json:"provider"`
	Mission        MissionConfig      `json:"mission"`
	Hooks          HookConfig         `json:"hooks"`
	Visibility     VisibilityConfig   `json:"visibility"`
	Memory         MemoryConfig       `json:"memory"`
	Subagents      SubagentConfig     `json:"subagents"`
	ACP            ACPConfig          `json:"acp"`
	Permission     PermissionConfig   `json:"permission"`
	TUI            TUIConfig          `json:"tui"`
}

type VerificationConfig struct {
	CodingCommands       [][]string `json:"coding_commands,omitempty"`
	CodingGoTestCommand  []string   `json:"coding_go_test_command"`
	CodingTimeoutSeconds int        `json:"coding_timeout_seconds,omitempty"`
}

type WatchConfig struct {
	DefaultIntervalSeconds int `json:"default_interval_seconds"`
}

type SchedulerConfig struct {
	LeaseFile string `json:"lease_file"`
}

type ProviderConfig struct {
	Mode                                   string   `json:"mode"`
	Command                                []string `json:"command"`
	AutoRunMaxTurns                        int      `json:"auto_run_max_turns"`
	BaseURL                                string   `json:"base_url,omitempty"`
	APIKeyEnv                              string   `json:"api_key_env,omitempty"`
	Model                                  string   `json:"model,omitempty"`
	ThinkingLevel                          string   `json:"thinking_level,omitempty"`
	DecisionTimeoutSeconds                 int      `json:"decision_timeout_seconds,omitempty"`
	DecisionMaxOutputTokens                int      `json:"decision_max_output_tokens,omitempty"`
	SystemPrompt                           string   `json:"system_prompt,omitempty"`
	CodingRepairBudget                     int      `json:"coding_repair_budget,omitempty"`
	CodingObservationCommandBudget         int      `json:"coding_observation_command_budget,omitempty"`
	CodingObservationCommandTimeoutSeconds int      `json:"coding_observation_command_timeout_seconds,omitempty"`
	CodingExecutionCommandBudget           int      `json:"coding_execution_command_budget,omitempty"`
	CodingExecutionCommandTimeoutSeconds   int      `json:"coding_execution_command_timeout_seconds,omitempty"`
}

type MissionConfig struct {
	RoleModels       map[string]string `json:"role_models,omitempty"`
	RoleModelPresent map[string]bool   `json:"-"`
}

type HookConfig struct {
	PreRunCommand  []string         `json:"pre_run_command"`
	PostRunCommand []string         `json:"post_run_command"`
	OnDoneCommand  []string         `json:"on_done_command"`
	Registry       []HookDefinition `json:"registry,omitempty"`
}

type HookDefinition struct {
	HookID         string   `json:"hook_id"`
	Stage          string   `json:"stage"`
	Actions        []string `json:"actions,omitempty"`
	Command        []string `json:"command"`
	AllowFailure   bool     `json:"allow_failure,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
}

type VisibilityConfig struct {
	AdditionalRoots []string `json:"additional_roots"`
	DenyPatterns    []string `json:"deny_patterns"`
}

type MemoryConfig struct {
	Enabled    bool   `json:"enabled"`
	File       string `json:"file"`
	MaxEntries int    `json:"max_entries"`
}

type SubagentRolePolicy struct {
	PermissionModeID     string   `json:"permission_mode_id,omitempty"`
	WorkspaceIsolation   string   `json:"workspace_isolation,omitempty"`
	ReconcileMode        string   `json:"reconcile_mode,omitempty"`
	AutoReleaseOnSuccess *bool    `json:"auto_release_on_success,omitempty"`
	AllowChildWorkers    *bool    `json:"allow_child_workers,omitempty"`
	AllowedWorkerRoles   []string `json:"allowed_worker_roles,omitempty"`
	MaxWorkersPerTask    int      `json:"max_workers_per_task,omitempty"`
	MaxLineageDepth      int      `json:"max_lineage_depth,omitempty"`
}

type SubagentConfig struct {
	MaxWorkersPerTask    int                           `json:"max_workers_per_task"`
	WorkspaceIsolation   string                        `json:"workspace_isolation,omitempty"`
	AutoReleaseOnSuccess bool                          `json:"auto_release_on_success,omitempty"`
	MaxLineageDepth      int                           `json:"max_lineage_depth,omitempty"`
	RolePolicies         map[string]SubagentRolePolicy `json:"role_policies,omitempty"`
}

type ACPConfig struct {
	Enabled bool `json:"enabled"`
}

type PermissionConfig struct {
	DefaultMode            string `json:"default_mode"`
	BenchmarkIntegrityMode bool   `json:"benchmark_integrity_mode,omitempty"`
}

type TUIConfig struct {
	AlternateScreen string `json:"alternate_screen,omitempty"`
	PollIntervalMS  int    `json:"poll_interval_ms,omitempty"`
	EventLimit      int    `json:"event_limit,omitempty"`
}
