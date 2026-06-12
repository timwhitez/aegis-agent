package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"ngen/internal/task"
)

var allowedActions = map[string]struct{}{
	"run":              {},
	"resume":           {},
	"respond":          {},
	"review":           {},
	"task_create":      {},
	"task_update":      {},
	"task_patch":       {},
	"project_update":   {},
	"project_patch":    {},
	"memory_promote":   {},
	"worker_spawn":     {},
	"worker_continue":  {},
	"wait":             {},
	"approval_request": {},
	"block":            {},
	"noop":             {},
}

const (
	modeBuiltin        = "builtin"
	modeCommand        = "command"
	modeOpenAIComp     = "openai-comp"
	modeOpenAIResponse = "openai-response"
	modeAnthropic      = "anthropic"

	decisionToolName                   = "submit_decision"
	workspaceEditToolName              = "submit_workspace_edit"
	workspaceObservationToolName       = "submit_workspace_observation"
	anthropicVersion                   = "2023-06-01"
	anthropicCacheControlTypeEphemeral = "ephemeral"
	defaultAPIKeyEnv                   = "OPENAI_API_KEY"
	defaultDecisionMaxTokens           = 2048
	maxProviderResponseBytes           = 4 * 1024 * 1024
)

var anthropicPromptCacheSplitRules = []anthropicPromptCacheSplitRule{
	{
		ContextMarker:   "Task context JSON:",
		VolatileMarkers: []string{"\n  \"state\":"},
	},
	{
		ContextMarker:   "Workspace edit context JSON:",
		VolatileMarkers: []string{"\n  \"recent_verification\":", "\n  \"previous_failures\":", "\n  \"collection\":", "\n  \"files\":"},
	},
	{
		ContextMarker:   "Workspace observation context JSON:",
		VolatileMarkers: []string{"\n  \"recent_verification\":", "\n  \"previous_failures\":", "\n  \"collection\":", "\n  \"files\":"},
	},
	{
		ContextMarker:   "Mission validation context JSON:",
		VolatileMarkers: []string{"\n  \"root_status\":", "\n  \"harness\":", "\n  \"context_refs\":"},
	},
}

type anthropicPromptCacheSplitRule struct {
	ContextMarker   string
	VolatileMarkers []string
}

func decodeLimitedJSON(source string, body io.Reader, out any) error {
	limited := io.LimitReader(body, maxProviderResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if int64(len(data)) > maxProviderResponseBytes {
		return fmt.Errorf("%s response exceeds max bytes (%d)", source, maxProviderResponseBytes)
	}
	return json.Unmarshal(data, out)
}

type Decision struct {
	Action                 string                       `json:"action"`
	Summary                string                       `json:"summary"`
	TokenUsage             string                       `json:"-"`
	PromptCacheUsage       string                       `json:"-"`
	ResponseText           string                       `json:"response_text,omitempty"`
	TaskKind               string                       `json:"task_kind,omitempty"`
	TaskPresetID           string                       `json:"task_preset_id,omitempty"`
	TaskTitle              string                       `json:"task_title,omitempty"`
	TaskObjective          string                       `json:"task_objective,omitempty"`
	TaskCriteria           []string                     `json:"task_criteria,omitempty"`
	TaskConstraints        []string                     `json:"task_constraints,omitempty"`
	TaskPermissionModeID   string                       `json:"task_permission_mode_id,omitempty"`
	ProjectStepID          string                       `json:"project_step_id,omitempty"`
	ProjectBranchID        string                       `json:"project_branch_id,omitempty"`
	PlanExplanation        string                       `json:"plan_explanation,omitempty"`
	PlanSteps              []task.ExecutionPlanStep     `json:"plan_steps,omitempty"`
	PlanPatchOperations    []task.PlanPatchOperation    `json:"plan_patch_operations,omitempty"`
	ProjectExplanation     string                       `json:"project_explanation,omitempty"`
	ProjectSteps           []task.ProjectExecutionStep  `json:"project_steps,omitempty"`
	ProjectBranches        []task.ProjectBranchSpec     `json:"project_branches,omitempty"`
	ProjectPatchOperations []task.ProjectPatchOperation `json:"project_patch_operations,omitempty"`
	MemoryKind             string                       `json:"memory_kind,omitempty"`
	MemoryRefs             []string                     `json:"memory_refs,omitempty"`
	WorkerID               string                       `json:"worker_id,omitempty"`
	WorkerRole             string                       `json:"worker_role,omitempty"`
	WorkerObjective        string                       `json:"worker_objective,omitempty"`
	WatchInterval          string                       `json:"watch_interval,omitempty"`
	WatchReason            string                       `json:"watch_reason,omitempty"`
	ApprovalScope          string                       `json:"approval_scope,omitempty"`
	ApprovalReason         string                       `json:"approval_reason,omitempty"`
}

type Input struct {
	Task                  task.Spec                       `json:"task"`
	Plan                  *task.Plan                      `json:"plan,omitempty"`
	Project               *task.Project                   `json:"project,omitempty"`
	Mission               *task.MissionView               `json:"mission,omitempty"`
	State                 task.State                      `json:"state"`
	Baseline              *task.Baseline                  `json:"baseline,omitempty"`
	Continuity            *task.ContinuitySnapshot        `json:"continuity,omitempty"`
	Sprint                *task.SprintSnapshot            `json:"sprint,omitempty"`
	Verification          *task.VerificationReport        `json:"verification,omitempty"`
	Review                *task.ReviewReport              `json:"review,omitempty"`
	Completion            *task.CompletionReport          `json:"completion,omitempty"`
	Criteria              *task.CriteriaSnapshot          `json:"criteria,omitempty"`
	ContextPack           *task.ContextSummary            `json:"context_pack,omitempty"`
	RoleContract          *task.RoleContract              `json:"role_contract,omitempty"`
	WorkspaceGuidance     *task.WorkspaceGuidanceArtifact `json:"workspace_guidance,omitempty"`
	WorkspaceMemory       string                          `json:"workspace_memory,omitempty"`
	Session               *task.Session                   `json:"session,omitempty"`
	SessionMessagesRef    string                          `json:"session_messages_ref,omitempty"`
	SessionRecentMessages []task.SessionMessage           `json:"session_recent_messages,omitempty"`
	RecentEvents          []task.Event                    `json:"recent_events,omitempty"`
	ManagedWorkers        []task.WorkerContract           `json:"managed_workers,omitempty"`
	PendingApprovals      []task.ApprovalRecord           `json:"pending_approvals,omitempty"`
	OwnedPendingApprovals []task.OwnedApprovalSummary     `json:"owned_pending_approvals,omitempty"`
	ActiveWatch           *task.Watch                     `json:"active_watch,omitempty"`
}

type WorkspaceFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type WorkspaceEditInput struct {
	Task                  task.Spec                       `json:"task"`
	Baseline              *task.Baseline                  `json:"baseline,omitempty"`
	Continuity            *task.ContinuitySnapshot        `json:"continuity,omitempty"`
	Sprint                *task.SprintSnapshot            `json:"sprint,omitempty"`
	RecentVerification    *task.VerificationReport        `json:"recent_verification,omitempty"`
	Criteria              *task.CriteriaSnapshot          `json:"criteria,omitempty"`
	OpenCriteria          []task.SuccessCriterion         `json:"open_criteria,omitempty"`
	ContextPack           *task.ContextSummary            `json:"context_pack,omitempty"`
	WorkspaceGuidance     *task.WorkspaceGuidanceArtifact `json:"workspace_guidance,omitempty"`
	SessionMessagesRef    string                          `json:"session_messages_ref,omitempty"`
	SessionRecentMessages []task.SessionMessage           `json:"session_recent_messages,omitempty"`
	PreviousFailures      []RepairFailure                 `json:"previous_failures,omitempty"`
	RepairAttempt         int                             `json:"repair_attempt,omitempty"`
	RepairBudget          int                             `json:"repair_budget,omitempty"`
	ExecutionBudget       int                             `json:"execution_budget,omitempty"`
	Collection            WorkspaceCollection             `json:"collection"`
	Observations          []ObservationResult             `json:"observations,omitempty"`
	Files                 []WorkspaceFile                 `json:"files"`
}

type WorkspaceCollection struct {
	IncludedFileCount int      `json:"included_file_count"`
	IncludedByteCount int      `json:"included_byte_count"`
	OmittedFileCount  int      `json:"omitted_file_count"`
	Truncated         bool     `json:"truncated"`
	StopReason        string   `json:"stop_reason,omitempty"`
	OmittedPaths      []string `json:"omitted_paths,omitempty"`
}

type ObservationCommand struct {
	Argv   []string `json:"argv"`
	Reason string   `json:"reason"`
}

type ObservationResult struct {
	CommandID     string   `json:"command_id"`
	Status        string   `json:"status"`
	Summary       string   `json:"summary"`
	Argv          []string `json:"argv"`
	ExitCode      int      `json:"exit_code"`
	TimedOut      bool     `json:"timed_out,omitempty"`
	StdoutRef     string   `json:"stdout_ref,omitempty"`
	StderrRef     string   `json:"stderr_ref,omitempty"`
	StdoutExcerpt string   `json:"stdout_excerpt,omitempty"`
	StderrExcerpt string   `json:"stderr_excerpt,omitempty"`
}

type RepairFailure struct {
	Attempt int    `json:"attempt"`
	Stage   string `json:"stage"`
	Summary string `json:"summary"`
}

type WorkspaceObservationInput struct {
	Task                  task.Spec                       `json:"task"`
	Baseline              *task.Baseline                  `json:"baseline,omitempty"`
	Continuity            *task.ContinuitySnapshot        `json:"continuity,omitempty"`
	Sprint                *task.SprintSnapshot            `json:"sprint,omitempty"`
	RecentVerification    *task.VerificationReport        `json:"recent_verification,omitempty"`
	Criteria              *task.CriteriaSnapshot          `json:"criteria,omitempty"`
	OpenCriteria          []task.SuccessCriterion         `json:"open_criteria,omitempty"`
	ContextPack           *task.ContextSummary            `json:"context_pack,omitempty"`
	WorkspaceGuidance     *task.WorkspaceGuidanceArtifact `json:"workspace_guidance,omitempty"`
	SessionMessagesRef    string                          `json:"session_messages_ref,omitempty"`
	SessionRecentMessages []task.SessionMessage           `json:"session_recent_messages,omitempty"`
	PreviousFailures      []RepairFailure                 `json:"previous_failures,omitempty"`
	RepairAttempt         int                             `json:"repair_attempt,omitempty"`
	RepairBudget          int                             `json:"repair_budget,omitempty"`
	CommandBudget         int                             `json:"command_budget,omitempty"`
	Collection            WorkspaceCollection             `json:"collection"`
	Files                 []WorkspaceFile                 `json:"files"`
}

type WorkspaceObservationPlan struct {
	Summary          string               `json:"summary"`
	TokenUsage       string               `json:"-"`
	PromptCacheUsage string               `json:"-"`
	Commands         []ObservationCommand `json:"commands"`
}

type MissionValidationInput struct {
	Mission     task.Mission                   `json:"mission"`
	Contract    task.MissionValidationContract `json:"validation_contract"`
	Features    task.MissionFeatureSet         `json:"features"`
	Milestones  task.MissionMilestoneSet       `json:"milestones"`
	RootStatus  *task.StatusSnapshot           `json:"root_status,omitempty"`
	Criteria    *task.CriteriaSnapshot         `json:"criteria,omitempty"`
	Completion  *task.CompletionReport         `json:"completion,omitempty"`
	Harness     *task.HarnessEvaluation        `json:"harness,omitempty"`
	ContextRefs []string                       `json:"context_refs,omitempty"`
}

type MissionValidationResult struct {
	Status           string                          `json:"status"`
	Summary          string                          `json:"summary"`
	TokenUsage       string                          `json:"-"`
	PromptCacheUsage string                          `json:"-"`
	Findings         []task.MissionValidationFinding `json:"findings"`
}

type WorkspaceWrite struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type WorkspaceCommand struct {
	Phase                 string   `json:"phase,omitempty"`
	Argv                  []string `json:"argv"`
	Reason                string   `json:"reason"`
	ExplicitUserRequested bool     `json:"-"`
}

type WorkspaceEditPlan struct {
	Summary          string             `json:"summary"`
	TokenUsage       string             `json:"-"`
	PromptCacheUsage string             `json:"-"`
	Patch            string             `json:"patch,omitempty"`
	Writes           []WorkspaceWrite   `json:"writes"`
	Deletes          []string           `json:"deletes"`
	Commands         []WorkspaceCommand `json:"commands"`
}

type Driver interface {
	Decide(ctx context.Context, input Input) (Decision, error)
}

func CanonicalMode(mode string) string {
	switch normalized := strings.ToLower(strings.TrimSpace(mode)); normalized {
	case "", modeBuiltin:
		return modeBuiltin
	case modeCommand, "script":
		return modeCommand
	case modeOpenAIComp, "openai_comp", "openai-compatible", "openai_compatible", "chat-completions", "chat_completions", "openai-chat-completions", "openai_chat_completions":
		return modeOpenAIComp
	case modeOpenAIResponse, "openai_response", "openai-responses", "openai_responses", "responses", "openai-resopnse", "openai_resopnse":
		return modeOpenAIResponse
	case modeAnthropic, "anthropic-messages", "anthropic_messages", "claude":
		return modeAnthropic
	default:
		return normalized
	}
}

func RequiresRemoteConfig(mode string) bool {
	switch CanonicalMode(mode) {
	case modeOpenAIComp, modeOpenAIResponse, modeAnthropic:
		return true
	default:
		return false
	}
}

func SupportsWorkspaceEdit(cfg task.ProviderConfig) bool {
	switch CanonicalMode(cfg.Mode) {
	case modeBuiltin, modeCommand, modeOpenAIComp, modeOpenAIResponse, modeAnthropic:
		return true
	default:
		return false
	}
}

func New(cfg task.ProviderConfig) Driver {
	mode := CanonicalMode(cfg.Mode)
	switch mode {
	case modeBuiltin:
		if len(cfg.Command) > 0 {
			return &CommandDriver{Mode: modeBuiltin, Command: cfg.Command}
		}
		return BuiltinDriver{}
	case modeCommand:
		if len(cfg.Command) == 0 {
			return ErrorDriver{Err: errors.New("provider mode command requires provider.command")}
		}
		return &CommandDriver{Mode: modeCommand, Command: cfg.Command}
	case modeOpenAIComp:
		return NewOpenAIChatCompletionsDriver(cfg)
	case modeOpenAIResponse:
		return NewOpenAIResponsesDriver(cfg)
	case modeAnthropic:
		return NewAnthropicMessagesDriver(cfg)
	default:
		return ErrorDriver{Err: fmt.Errorf("unsupported provider mode: %s", cfg.Mode)}
	}
}

type ErrorDriver struct {
	Err error
}

func (d ErrorDriver) Decide(ctx context.Context, input Input) (Decision, error) {
	_ = ctx
	_ = input
	return Decision{}, d.Err
}

type BuiltinDriver struct{}

func (BuiltinDriver) Decide(ctx context.Context, input Input) (Decision, error) {
	_ = ctx
	if input.Session != nil {
		if decision, ok, err := OperatorPromptDecision(input.Session.LastPrompt); ok {
			return decision, err
		}
	}
	if len(input.OwnedPendingApprovals) > 0 {
		return validateDecision(Decision{
			Action:  "block",
			Summary: summarizeOwnedPendingApprovals(input.OwnedPendingApprovals),
		})
	}
	if workerID, summary, ok := continuableManagedWorker(input.ManagedWorkers); ok {
		return validateDecision(Decision{
			Action:   "worker_continue",
			Summary:  summary,
			WorkerID: workerID,
		})
	}
	if summary, ok := summarizeManagedWorkerAction(input.ManagedWorkers); ok {
		return validateDecision(Decision{
			Action:  "block",
			Summary: summary,
		})
	}
	if explanation, steps, ok := SuggestExecutionPlan(input); ok {
		return validateDecision(Decision{
			Action:          "task_update",
			Summary:         "Seed a mutable execution plan before continuing the task.",
			PlanExplanation: explanation,
			PlanSteps:       steps,
		})
	}
	switch input.State.State {
	case task.StateWaiting:
		return validateDecision(Decision{Action: "wait", Summary: "Task is already waiting on an active watch."})
	case task.StateBlocked:
		switch input.State.StatusReasonCode {
		case "blocked_review":
			return validateDecision(Decision{Action: "review", Summary: "Re-run review to refresh the latest gate verdict."})
		case "blocked_policy":
			return validateDecision(Decision{Action: "block", Summary: "Task is blocked on approval and cannot auto-continue."})
		default:
			return validateDecision(Decision{Action: "block", Summary: "Task is blocked and requires operator intervention."})
		}
	case task.StateDone, task.StateFailed, task.StateAborted:
		return validateDecision(Decision{Action: "noop", Summary: "Task is already terminal."})
	default:
		if input.State.LastVerificationRef == "" {
			return validateDecision(Decision{Action: "run", Summary: "Start a full runtime pass."})
		}
		return validateDecision(Decision{Action: "resume", Summary: "Continue the current runtime pass."})
	}
}

func SuggestExecutionPlan(input Input) (string, []task.ExecutionPlanStep, bool) {
	if input.State.State != task.StateActive {
		return "", nil, false
	}
	if input.Plan != nil && strings.TrimSpace(input.Plan.CurrentExecutionStepID) != "" {
		return "", nil, false
	}
	if input.Plan != nil {
		for _, step := range input.Plan.Steps {
			if task.IsExecutionStep(step) {
				return "", nil, false
			}
		}
	}
	openCriteria := make([]task.SuccessCriterion, 0, len(input.Task.SuccessCriteria))
	for _, criterion := range input.Task.SuccessCriteria {
		status := "open"
		if input.Criteria != nil {
			status = criterionStatusForInput(*input.Criteria, criterion.ID)
		}
		if status != "met" {
			openCriteria = append(openCriteria, criterion)
		}
	}
	if len(openCriteria) < 2 && len(input.ManagedWorkers) == 0 {
		return "", nil, false
	}
	steps := make([]task.ExecutionPlanStep, 0, len(openCriteria))
	for i, criterion := range openCriteria {
		stepStatus := task.StepStatusPending
		priority := task.StepPriorityMedium
		dependsOn := []string(nil)
		stepID := "STEP-EXEC-" + strings.TrimSpace(criterion.ID)
		if i == 0 {
			stepStatus = task.StepStatusInProgress
			priority = task.StepPriorityHigh
		} else if len(steps) > 0 {
			dependsOn = []string{steps[len(steps)-1].ID}
		}
		steps = append(steps, task.ExecutionPlanStep{
			ID:        stepID,
			Priority:  priority,
			Title:     criterion.Statement,
			Status:    stepStatus,
			Covers:    []string{criterion.ID},
			DependsOn: dependsOn,
		})
	}
	if len(steps) == 0 {
		return "", nil, false
	}
	return "Initial mutable execution plan synthesized from the current open criteria as a one-criterion-at-a-time ladder.", steps, true
}

func criterionStatusForInput(snapshot task.CriteriaSnapshot, criterionID string) string {
	for _, criterion := range snapshot.Criteria {
		if strings.TrimSpace(criterion.CriterionID) == strings.TrimSpace(criterionID) {
			return criterion.Status
		}
	}
	return "open"
}

func OperatorPromptDecision(prompt string) (Decision, bool, error) {
	decision, ok := decisionFromPrompt(prompt)
	if !ok {
		return Decision{}, false, nil
	}
	validated, err := validateDecision(decision)
	if err != nil {
		return Decision{}, true, err
	}
	return validated, true, nil
}

func decisionFromPrompt(prompt string) (Decision, bool) {
	text := strings.TrimSpace(strings.ToLower(prompt))
	switch {
	case text == "":
		return Decision{}, false
	case strings.HasPrefix(text, "/run"):
		return Decision{Action: "run", Summary: "Operator requested a full run."}, true
	case strings.HasPrefix(text, "/resume"):
		return Decision{Action: "resume", Summary: "Operator requested resume."}, true
	case strings.HasPrefix(text, "/review"):
		return Decision{Action: "review", Summary: "Operator requested review."}, true
	case strings.HasPrefix(text, "/goal"), strings.HasPrefix(text, "/goals"), strings.HasPrefix(text, "/mission"), strings.HasPrefix(text, "/missions"):
		return Decision{Action: "noop", Summary: "Goal and mission commands are handled on the mission bridge."}, true
	case strings.HasPrefix(text, "/worker_spawn"), strings.HasPrefix(text, "/worker spawn"):
		fields := strings.Fields(prompt)
		roleIndex := 1
		if len(fields) >= 2 && strings.EqualFold(strings.TrimSpace(fields[0]), "/worker") {
			roleIndex = 2
		}
		if len(fields) <= roleIndex {
			return Decision{Action: "noop", Summary: "worker_spawn requires a worker role and objective."}, true
		}
		objectiveIndex := roleIndex + 1
		if len(fields) <= objectiveIndex {
			return Decision{Action: "noop", Summary: "worker_spawn requires a worker objective."}, true
		}
		return Decision{
			Action:          "worker_spawn",
			Summary:         fmt.Sprintf("Operator requested worker spawn for role %s.", strings.TrimSpace(fields[roleIndex])),
			WorkerRole:      strings.TrimSpace(fields[roleIndex]),
			WorkerObjective: strings.TrimSpace(strings.Join(fields[objectiveIndex:], " ")),
		}, true
	case strings.HasPrefix(text, "/worker_continue"), strings.HasPrefix(text, "/worker continue"):
		fields := strings.Fields(prompt)
		if len(fields) < 2 {
			return Decision{Action: "noop", Summary: "worker_continue requires a worker id."}, true
		}
		return Decision{
			Action:   "worker_continue",
			Summary:  fmt.Sprintf("Operator requested worker continuation for %s.", strings.TrimSpace(fields[len(fields)-1])),
			WorkerID: strings.TrimSpace(fields[len(fields)-1]),
		}, true
	case strings.HasPrefix(text, "/memory"):
		fields := strings.Fields(prompt)
		if len(fields) < 2 {
			return Decision{Action: "noop", Summary: "memory requires a summary."}, true
		}
		kind := task.MemoryKindTaskNote
		summaryIndex := 1
		if len(fields) >= 3 && task.IsSupportedMemoryKind(fields[1]) {
			kind = task.CanonicalMemoryKind(fields[1])
			summaryIndex = 2
		}
		summary := strings.TrimSpace(strings.Join(fields[summaryIndex:], " "))
		if summary == "" {
			return Decision{Action: "noop", Summary: "memory requires a summary."}, true
		}
		return Decision{
			Action:     "memory_promote",
			Summary:    summary,
			MemoryKind: kind,
		}, true
	case strings.HasPrefix(text, "/watch"):
		fields := strings.Fields(prompt)
		interval := "5m"
		if len(fields) >= 2 {
			interval = fields[1]
			if _, err := time.ParseDuration(interval); err != nil {
				interval = "5m"
			}
		}
		reason := "provider-directed watch"
		if len(fields) >= 3 {
			reason = strings.Join(fields[2:], " ")
		}
		return Decision{
			Action:        "wait",
			Summary:       "Operator requested a durable watch.",
			WatchInterval: interval,
			WatchReason:   reason,
		}, true
	case strings.HasPrefix(text, "/approve"):
		return Decision{Action: "noop", Summary: "Approval decisions are handled on the permission bridge."}, true
	case strings.HasPrefix(text, "/deny"):
		return Decision{Action: "noop", Summary: "Approval decisions are handled on the permission bridge."}, true
	default:
		if responseText, ok := conversationalPromptResponse(prompt); ok {
			return Decision{
				Action:       "respond",
				Summary:      "Answer the operator directly without starting a task action.",
				ResponseText: responseText,
			}, true
		}
		return Decision{}, false
	}
}

func conversationalPromptResponse(prompt string) (string, bool) {
	normalized := normalizeConversationalPrompt(prompt)
	if normalized == "" {
		return "", false
	}
	switch normalized {
	case "hi", "hello", "hey", "hi ngen", "hello ngen", "hey ngen", "你好", "您好", "嗨":
		return "Hello. Tell me what to inspect, run, review, or change. The TUI stays chat-first; use /run to execute, /review to refresh the gate, or /help for local controls.", true
	case "what can you do", "what do you do", "who are you", "你能做什么", "你是谁":
		return "I can drive the current coding runtime, inspect state, run or resume work, refresh review, and surface approvals or input when human judgment is needed. Give me a concrete task steer or use /help for local controls.", true
	default:
		return "", false
	}
}

func normalizeConversationalPrompt(prompt string) string {
	replacer := strings.NewReplacer(
		"?", " ",
		"!", " ",
		".", " ",
		",", " ",
		"，", " ",
		"。", " ",
		"！", " ",
		"？", " ",
	)
	return strings.Join(strings.Fields(strings.TrimSpace(replacer.Replace(strings.ToLower(prompt)))), " ")
}

func summarizeOwnedPendingApprovals(items []task.OwnedApprovalSummary) string {
	if len(items) == 0 {
		return "Parent task owns pending worker approvals."
	}
	first := items[0]
	summary := fmt.Sprintf(
		"Worker %s is blocked on owned approval %s (%s). Parent can approve, deny, or parent_takeover.",
		first.WorkerID,
		first.ApprovalID,
		first.Scope,
	)
	if len(items) > 1 {
		summary = fmt.Sprintf("%s %d owned approvals are pending across workers.", summary, len(items))
	}
	return summary
}

func summarizeManagedWorkerAction(workers []task.WorkerContract) (string, bool) {
	for _, worker := range workers {
		if !worker.RequiresParentAction {
			continue
		}
		if strings.TrimSpace(worker.ParentActionType) == "continue_child" {
			continue
		}
		if strings.TrimSpace(worker.ParentActionSummary) != "" {
			return worker.ParentActionSummary, true
		}
	}
	return "", false
}

func continuableManagedWorker(workers []task.WorkerContract) (string, string, bool) {
	for _, worker := range workers {
		if !worker.RequiresParentAction {
			continue
		}
		if strings.TrimSpace(worker.ParentActionType) != "continue_child" {
			continue
		}
		summary := strings.TrimSpace(worker.ParentActionSummary)
		if summary == "" {
			summary = fmt.Sprintf("Continue worker %s.", worker.WorkerID)
		}
		return strings.TrimSpace(worker.WorkerID), summary, true
	}
	return "", "", false
}

type CommandDriver struct {
	Mode    string
	Command []string
}

func (d *CommandDriver) Decide(ctx context.Context, input Input) (Decision, error) {
	payload, err := runCommandProviderRaw(ctx, d.Mode, d.Command, "decision", input)
	if err != nil {
		return Decision{}, err
	}
	var decision Decision
	if err := json.Unmarshal(payload, &decision); err != nil {
		return Decision{}, fmt.Errorf("provider command returned invalid decision: %w", err)
	}
	return validateDecision(decision)
}

type OpenAIResponsesDriver struct {
	BaseURL         string
	APIKeyEnv       string
	Model           string
	SystemPrompt    string
	ReasoningEffort string
	MaxTokens       int
	Client          *http.Client
}

func NewOpenAIResponsesDriver(cfg task.ProviderConfig) *OpenAIResponsesDriver {
	timeout := decisionTimeout(cfg)
	keyEnv := normalizedAPIKeyEnv(cfg.APIKeyEnv)
	return &OpenAIResponsesDriver{
		BaseURL:         strings.TrimSpace(cfg.BaseURL),
		APIKeyEnv:       keyEnv,
		Model:           strings.TrimSpace(cfg.Model),
		SystemPrompt:    strings.TrimSpace(cfg.SystemPrompt),
		ReasoningEffort: strings.TrimSpace(cfg.ThinkingLevel),
		MaxTokens:       decisionMaxOutputTokens(cfg),
		Client: &http.Client{
			Timeout: timeout,
		},
	}
}

func (d *OpenAIResponsesDriver) Decide(ctx context.Context, input Input) (Decision, error) {
	if err := validateRemoteConfig(modeOpenAIResponse, d.BaseURL, d.Model); err != nil {
		return Decision{}, err
	}
	apiKey, err := readAPIKey(modeOpenAIResponse, d.APIKeyEnv)
	if err != nil {
		return Decision{}, err
	}
	body, err := d.requestBody(input)
	if err != nil {
		return Decision{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, responsesEndpoint(d.BaseURL), bytes.NewReader(body))
	if err != nil {
		return Decision{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.Client.Do(req)
	if err != nil {
		return Decision{}, fmt.Errorf("responses provider request failed: %w", err)
	}
	defer resp.Body.Close()
	var parsed responsesResponse
	if err := decodeLimitedJSON("responses provider", resp.Body, &parsed); err != nil {
		return Decision{}, fmt.Errorf("responses provider returned invalid JSON: %w", err)
	}
	if resp.StatusCode >= 400 {
		if parsed.Error != nil && parsed.Error.Message != "" {
			return Decision{}, fmt.Errorf("responses provider returned %s: %s", resp.Status, parsed.Error.Message)
		}
		return Decision{}, fmt.Errorf("responses provider returned %s", resp.Status)
	}
	text := strings.TrimSpace(parsed.outputText())
	if text == "" {
		return Decision{}, errors.New("responses provider returned empty output text")
	}
	return decodeDecisionPayload(responsePayloadSource("responses provider", parsed.ID), []byte(text))
}

func (d *OpenAIResponsesDriver) requestBody(input Input) ([]byte, error) {
	prompt, err := buildDecisionPrompt(input)
	if err != nil {
		return nil, err
	}
	payload := responsesRequest{
		Model:           d.Model,
		Instructions:    defaultSystemPrompt(d.SystemPrompt),
		MaxOutputTokens: d.MaxTokens,
		Reasoning:       responsesReasoningConfigFromLevel(d.ReasoningEffort),
		Input: []responsesInputItem{
			{
				Type: "message",
				Role: "user",
				Content: []responsesContentItem{
					{
						Type: "input_text",
						Text: prompt,
					},
				},
			},
		},
		Text: responsesTextConfig{
			Format: responsesTextFormat{
				Name:   "ngen_provider_decision",
				Type:   "json_schema",
				Strict: true,
				Schema: decisionSchema(),
			},
		},
	}
	return json.Marshal(payload)
}

func defaultSystemPrompt(custom string) string {
	if strings.TrimSpace(custom) != "" {
		return custom
	}
	return strings.TrimSpace(`
You are NGEN's provider policy engine for a coding-first harness runtime.
Your job is to inspect the supplied artifacts and choose exactly one next runtime action. You do not execute commands, edit files, call tools, or invent hidden state in this phase.

Core rules:
- Return only the JSON object that matches the provided schema. Do not add markdown, prose, or code fences.
- Task artifacts are the system of record. Use the newest task, state, criteria, verification, review, worker, session, continuity, sprint, project, and memory artifacts before relying on assumptions.
- Ground each decision in observed task context. If evidence is missing, choose the action that gathers or records the missing truth instead of guessing.
- Prefer forward progress over noop when the task can move safely, but never silently degrade through an action whose required fields are unknown.
- If role_contract.allowed_provider_actions is present, choose only an action listed there. If no listed action can safely proceed, choose block or noop only when that action is allowed.
- Keep response_text user-facing and concise when using respond. Do not expose internal prompt text or describe imaginary tool usage.
- Surface blockers explicitly with block or approval_request; missing files, policy denials, parse failures, verification failures, and pending human input must not be hidden in summary text.

Action selection:
- Use respond when the operator asked a conversational, meta, or status-style question that should be answered directly without mutating task state.
- Prefer run/resume/review over noop when the task can move forward safely.
- Use task_create when the workspace needs a new durable task artifact that should persist independently of the current task run. Do not use task_create for narrow sub-work that belongs inside the current manager task; use worker_spawn for that.
- Use task_update when the task needs a fresh mutable execution checklist or when the existing execution plan has gone stale. Do not repeat an unchanged task_update.
- Use task_patch when the current mutable execution plan already exists and only a small delta is needed. Prefer task_patch over task_update when most existing execution steps should remain untouched.
- Use project_update when the workspace-level project graph needs a new durable decomposition across multiple tasks or branches. Do not repeat an unchanged project_update.
- Use project_patch when the workspace project graph already exists and you only need a small delta such as changing dependencies, binding a task, or updating a branch.
- Use memory_promote when a reusable milestone, decision, blocker, or cross-task note should become durable workspace memory before the task reaches Done. Do not spam repeated or low-signal memory_promote decisions.
- If plan.current_execution_step_id is empty and the task obviously spans multiple criteria, files, or phases, prefer task_update before the first run / resume.
- If project.current_step_id is empty but the workspace work clearly spans multiple durable tasks or concurrent branches, prefer project_update before inventing ad-hoc cross-task chat state.
- When using task_create, keep the task objective and criteria durable and verification-friendly. Bind the new task into the existing project graph with project_step_id and project_branch_id when you are materializing an existing project decomposition instead of creating an auto-tracked standalone task.
- When using task_create, write the child contract as if the child task will run immediately after creation. Do not copy parent-side orchestration instructions such as "create exactly one durable task", "use task_create", or "bind the new task to project step ..." into task_objective or task_constraints. Put binding only in project_step_id and project_branch_id.
- If the current task is already bound to a project step or branch, do not reuse that same binding for a new task_create unless the operator explicitly asked to replace or hand off the current binding. Leave project_step_id and project_branch_id empty for generic follow-up tasks.
- When using task_update, preserve stable step ids for unchanged work items when possible, and use parent_step_id plus depends_on when a hierarchical task graph or blockers matter.
- When using task_patch, treat plan_patch_operations as a sequential mutation log over the current mutable execution plan. Use set_explanation to refresh the explanation, upsert_step to add or replace one execution step, and remove_step to drop obsolete execution steps.
- When using project_update, preserve stable project step ids and branch ids when possible. Bind project steps or branches to real task ids only when those durable tasks already exist.
- When using project_patch, prefer the edge-level operations when only dependencies, parent linkage, task binding, branch binding, or branch status need to change.
- When using memory_promote, keep summary concise, durable, and cross-task reusable. Prefer kinds such as task_milestone, task_decision, or task_blocker, and include evidence refs when they are already known.
- Use worker_spawn when the parent should delegate bounded work to a child role and the request is explicit enough to define that child objective.
- Prefer task_create over worker_spawn when the new task should remain a first-class workspace task rather than a managed child contract of the current task.
- When mission context is present, use task_create only to materialize the current pending unbound mission feature as a mission-owned child task; otherwise use task_patch for root-plan follow-up work or worker_spawn for bounded artifact-only/review work.
- Prefer worker_continue when a managed worker explicitly advertises continue_child and no owned approval is still pending.
- Use wait only when a durable watch is the correct next control primitive.
- Use approval_request only when explicit operator approval is required.
- Use block when the task cannot proceed automatically.

Context priority:
- Treat context_pack as the durable task-local continuity summary. Use workspace_memory only as a cross-task hint, and let fresh task artifacts win when they conflict.
- Treat context_pack.project_focus as the task-scoped view over the workspace project graph when it is present. Stay inside its primary step, dependencies, and branch before widening into sibling work.
- Treat criteria as the durable acceptance ledger for the task. Criteria with passes=false remain failing, and criteria.current_criterion_id is the current feature boundary unless the execution plan explicitly moves focus elsewhere.
- Treat continuity as the structured restart ledger for the next fresh context. Use continuity.current_focus as the current sprint contract and continuity.startup_checklist as the shortest safe path to get bearings again. When continuity.current_focus.project_focus is present, treat it as the durable task-scoped project binding and dependency view.
- Treat sprint as the durable current-scope contract. Prefer closing sprint.primary_criterion_id and sprint.completion_signals before inventing a new sprint or expanding into deferred criteria. When sprint.project_focus is present, keep the task inside sprint.project_focus.primary_step_id and its dependency boundary.
- Prefer advancing one open criterion at a time unless the current execution step intentionally covers multiple criteria.
- Treat baseline.command_hints and baseline.workspace_snapshot as durable repo bearings. Prefer repo-owned setup or verifier commands already surfaced there over inventing new harness commands.
- Treat session_recent_messages plus session_messages_ref as the durable session-local steering transcript. Do not rely on session.last_prompt alone when it is ambiguous.
- If session.last_prompt uses a slash command such as /run, /resume, /review, /goal PROMPT, /mission PROMPT, /memory [KIND] SUMMARY, /worker_spawn ROLE OBJECTIVE, or /worker_continue WORKER_ID, honor that command when it is valid and safe.
- /goal, /goals, /mission, and /missions are mission-bridge commands; the runtime handles their artifact mutation before remote decisions, so do not re-create separate mission state from provider output.
`)
}

func buildDecisionPrompt(input Input) (string, error) {
	data, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(`
Choose the next NGEN runtime action from the task context below.
Return exactly one schema-valid JSON object. This is a decision step, not an execution step: do not claim to run commands, edit files, or inspect paths that are not present in the context.

Allowed actions:
- run: start the runtime pass when no current verification/review pass has been attempted or the task needs full execution.
- resume: continue from the current durable state when prior verification, review, or repair truth already exists.
- respond: answer a conversational/status/meta operator prompt without mutating task state.
- review: refresh the review/completion gate when verifier evidence exists or a review blocker needs re-evaluation.
- task_create: materialize a separate durable workspace task.
- task_update: replace the mutable execution checklist.
- task_patch: apply a small sequential mutation to an existing mutable execution checklist.
- project_update: replace the workspace-level project graph when decomposition across tasks or branches is needed.
- project_patch: mutate existing workspace project graph edges, bindings, branches, or statuses.
- memory_promote: persist a reusable milestone, decision, blocker, or cross-task note.
- worker_spawn: delegate bounded child work owned by the current manager task.
- worker_continue: continue a managed child that explicitly advertises continue_child.
- wait: create or maintain a durable watch.
- approval_request: ask for an explicit operator approval before proceeding.
- block: surface why automatic progress is not safe.
- noop: only when the task is terminal or no safe state change exists.

Field requirements:
When selecting respond, put the user-visible assistant-style reply in response_text and keep summary as a short control-plane explanation of why you answered directly. When selecting wait, include a positive watch_interval like "5m" when useful. When selecting approval_request, include approval_scope and approval_reason. When selecting task_create, include task_objective plus task_criteria, and set task_kind / task_preset_id / task_title / task_constraints / task_permission_mode_id when they matter. Write the child contract as a ready-to-run child task, not as another orchestration request. Do not copy parent instructions like "create exactly one durable task" or "bind the new task ..." into task_constraints; keep binding only in project_step_id and project_branch_id. Use project_step_id and project_branch_id when the new durable task should bind into an existing project graph entry; otherwise leave them empty and let the runtime auto-track the new task. When selecting worker_spawn, include worker_role and worker_objective. When selecting worker_continue, include worker_id from managed_workers. When selecting task_update, include plan_steps as the full mutable execution checklist you want NGEN to persist, plus plan_explanation when useful. Include stable plan_steps[].id values for steps you want to preserve across updates, and use parent_step_id / depends_on / priority when the task benefits from hierarchy, blockers, or ordering. When selecting task_patch, include plan_patch_operations as the sequential patch log that should be applied to the current mutable execution plan; preserve existing stable ids unless you are intentionally replacing or removing a step. When selecting project_update, include the full workspace-level project_steps and project_branches plus project_explanation. When selecting project_patch, include project_patch_operations as the sequential mutation log over the workspace project graph; prefer the edge-level operations when only dependencies, parent linkage, task binding, branch binding, or branch status need to change. When selecting memory_promote, use summary as the durable memory text, set memory_kind to the best fitting stable kind, and include memory_refs when there are concrete artifact refs worth preserving.
Return every schema key. Use empty strings or empty arrays for fields that do not apply to the chosen action.
Prefer the smallest action that advances or records real task truth. Do not create new tasks, project graph entries, workers, or memory merely to restate information already present in artifacts.
Use context_pack for continuity before re-deriving older history from recent_events alone.
Use context_pack.project_focus when it is present to keep the current task inside its bound project step, dependencies, and branch.
Use criteria as the durable acceptance ledger. Keep criteria.current_criterion_id as the active feature boundary when it is present, and treat criteria.criteria[].passes=false as still failing.
Use continuity.current_focus and continuity.startup_checklist when they are present to resume the current sprint instead of inventing a new one. If continuity.current_focus.project_focus is present, keep the task within that project binding and dependency boundary.
Use sprint as the durable current-scope contract. Prefer closing sprint.primary_criterion_id and sprint.completion_signals before expanding into sprint.deferred_criterion_ids. If sprint.project_focus is present, avoid widening into sibling project branches or downstream steps without first closing the bound step.
Use baseline.command_hints and baseline.workspace_snapshot as durable repo bearings when they are present.
Use session_recent_messages for short-horizon operator steering before guessing from session.last_prompt alone.
If the task has no mutable execution steps yet and the work is clearly multi-step, prefer task_update first so the runtime can persist a checklist before execution.
Prefer advancing one open criterion at a time unless the current execution step intentionally covers multiple criteria.
Respect role_contract.allowed_provider_actions and role_contract.allowed_worker_roles when present; the runtime will reject a role action outside that contract.
If the workspace project graph is missing or stale and the work spans multiple durable tasks or concurrent branches, prefer project_update or project_patch before relying on implicit memory.
When the work now needs a separate durable task artifact, prefer task_create before describing that task only in chat or memory.
If the current task already owns a bound project step or branch and the operator did not explicitly ask to replace or hand off that binding, do not set task_create.project_step_id or task_create.project_branch_id to the parent task's current binding.
When mission context is present, task_create must correspond to the current pending unbound mission feature; use worker_spawn or task_patch when no such feature is available.

Task context JSON:
` + string(data)), nil
}

func buildMissionValidationPrompt(input MissionValidationInput) (string, error) {
	data, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(`
Validate this NGEN mission using only the supplied immutable artifact snapshots and context refs.
Return exactly one schema-valid JSON object with status, summary, and findings. This is a read-only validator pass:
- Do not request workspace edits, commands, provider decisions, task_create, worker_spawn, worker_continue, or hidden tool calls.
- Deterministic validation has already confirmed the root task reached Done, criteria are closed, and completion is accepted.
- Focus on correctness, completeness, evidence quality, stale context, missing coverage, and contract gaps.
- Findings must cite artifact refs from context_refs or evidence refs visible in the payload.
- Mark blocking=true only when the mission should not close until the finding is addressed.

Mission validation context JSON:
` + string(data)), nil
}

func GenerateWorkspaceEdit(ctx context.Context, cfg task.ProviderConfig, input WorkspaceEditInput) (WorkspaceEditPlan, error) {
	switch CanonicalMode(cfg.Mode) {
	case modeBuiltin:
		if len(cfg.Command) > 0 {
			return generateWorkspaceEditWithCommand(ctx, cfg, input)
		}
		return generateWorkspaceEditBuiltin(input)
	case modeCommand:
		return generateWorkspaceEditWithCommand(ctx, cfg, input)
	case modeOpenAIComp:
		return generateWorkspaceEditWithChatCompletions(ctx, cfg, input)
	case modeOpenAIResponse:
		return generateWorkspaceEditWithResponses(ctx, cfg, input)
	case modeAnthropic:
		return generateWorkspaceEditWithAnthropic(ctx, cfg, input)
	default:
		return WorkspaceEditPlan{}, fmt.Errorf("workspace edit loop is not implemented for provider mode: %s", CanonicalMode(cfg.Mode))
	}
}

func GenerateWorkspaceObservations(ctx context.Context, cfg task.ProviderConfig, input WorkspaceObservationInput) (WorkspaceObservationPlan, error) {
	switch CanonicalMode(cfg.Mode) {
	case modeBuiltin:
		if len(cfg.Command) > 0 {
			return generateWorkspaceObservationsWithCommand(ctx, cfg, input)
		}
		return generateWorkspaceObservationsBuiltin(input)
	case modeCommand:
		return generateWorkspaceObservationsWithCommand(ctx, cfg, input)
	case modeOpenAIComp:
		return generateWorkspaceObservationsWithChatCompletions(ctx, cfg, input)
	case modeOpenAIResponse:
		return generateWorkspaceObservationsWithResponses(ctx, cfg, input)
	case modeAnthropic:
		return generateWorkspaceObservationsWithAnthropic(ctx, cfg, input)
	default:
		return WorkspaceObservationPlan{}, fmt.Errorf("workspace observation loop is not implemented for provider mode: %s", CanonicalMode(cfg.Mode))
	}
}

func GenerateMissionValidation(ctx context.Context, cfg task.ProviderConfig, input MissionValidationInput) (MissionValidationResult, error) {
	switch CanonicalMode(cfg.Mode) {
	case modeBuiltin:
		if len(cfg.Command) > 0 {
			return generateMissionValidationWithCommand(ctx, cfg, input)
		}
		return MissionValidationResult{
			Status:   "passed",
			Summary:  "Builtin mission validator found no additional model-backed findings.",
			Findings: nil,
		}, nil
	case modeCommand:
		return generateMissionValidationWithCommand(ctx, cfg, input)
	case modeOpenAIComp:
		return generateMissionValidationWithChatCompletions(ctx, cfg, input)
	case modeOpenAIResponse:
		return generateMissionValidationWithResponses(ctx, cfg, input)
	case modeAnthropic:
		return generateMissionValidationWithAnthropic(ctx, cfg, input)
	default:
		return MissionValidationResult{}, fmt.Errorf("mission validation loop is not implemented for provider mode: %s", CanonicalMode(cfg.Mode))
	}
}

func generateWorkspaceEditWithChatCompletions(ctx context.Context, cfg task.ProviderConfig, input WorkspaceEditInput) (WorkspaceEditPlan, error) {
	if err := validateRemoteConfig(modeOpenAIComp, cfg.BaseURL, cfg.Model); err != nil {
		return WorkspaceEditPlan{}, err
	}
	apiKey, err := readAPIKey(modeOpenAIComp, cfg.APIKeyEnv)
	if err != nil {
		return WorkspaceEditPlan{}, err
	}
	body, err := workspaceEditChatCompletionsRequestBody(cfg, input)
	if err != nil {
		return WorkspaceEditPlan{}, err
	}
	client := &http.Client{Timeout: decisionTimeout(cfg)}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatCompletionsEndpoint(cfg.BaseURL), bytes.NewReader(body))
	if err != nil {
		return WorkspaceEditPlan{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return WorkspaceEditPlan{}, fmt.Errorf("openai-comp workspace edit request failed: %w", err)
	}
	defer resp.Body.Close()
	var parsed chatCompletionsResponse
	if err := decodeLimitedJSON("openai-comp workspace edit", resp.Body, &parsed); err != nil {
		return WorkspaceEditPlan{}, fmt.Errorf("openai-comp workspace edit returned invalid JSON: %w", err)
	}
	if resp.StatusCode >= 400 {
		if parsed.Error != nil && parsed.Error.Message != "" {
			return WorkspaceEditPlan{}, fmt.Errorf("openai-comp workspace edit returned %s: %s", resp.Status, parsed.Error.Message)
		}
		return WorkspaceEditPlan{}, fmt.Errorf("openai-comp workspace edit returned %s", resp.Status)
	}
	payload, err := parsed.toolPayload(workspaceEditToolName, "openai-comp workspace edit")
	if err != nil {
		return WorkspaceEditPlan{}, err
	}
	return decodeWorkspaceEditPayload("openai-comp workspace edit", payload)
}

func generateWorkspaceObservationsWithChatCompletions(ctx context.Context, cfg task.ProviderConfig, input WorkspaceObservationInput) (WorkspaceObservationPlan, error) {
	if err := validateRemoteConfig(modeOpenAIComp, cfg.BaseURL, cfg.Model); err != nil {
		return WorkspaceObservationPlan{}, err
	}
	apiKey, err := readAPIKey(modeOpenAIComp, cfg.APIKeyEnv)
	if err != nil {
		return WorkspaceObservationPlan{}, err
	}
	body, err := workspaceObservationChatCompletionsRequestBody(cfg, input)
	if err != nil {
		return WorkspaceObservationPlan{}, err
	}
	client := &http.Client{Timeout: decisionTimeout(cfg)}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatCompletionsEndpoint(cfg.BaseURL), bytes.NewReader(body))
	if err != nil {
		return WorkspaceObservationPlan{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return WorkspaceObservationPlan{}, fmt.Errorf("openai-comp workspace observation request failed: %w", err)
	}
	defer resp.Body.Close()
	var parsed chatCompletionsResponse
	if err := decodeLimitedJSON("openai-comp workspace observation", resp.Body, &parsed); err != nil {
		return WorkspaceObservationPlan{}, fmt.Errorf("openai-comp workspace observation returned invalid JSON: %w", err)
	}
	if resp.StatusCode >= 400 {
		if parsed.Error != nil && parsed.Error.Message != "" {
			return WorkspaceObservationPlan{}, fmt.Errorf("openai-comp workspace observation returned %s: %s", resp.Status, parsed.Error.Message)
		}
		return WorkspaceObservationPlan{}, fmt.Errorf("openai-comp workspace observation returned %s", resp.Status)
	}
	payload, err := parsed.toolPayload(workspaceObservationToolName, "openai-comp workspace observation")
	if err != nil {
		return WorkspaceObservationPlan{}, err
	}
	return decodeWorkspaceObservationPayload("openai-comp workspace observation", payload)
}

func generateMissionValidationWithChatCompletions(ctx context.Context, cfg task.ProviderConfig, input MissionValidationInput) (MissionValidationResult, error) {
	if err := validateRemoteConfig(modeOpenAIComp, cfg.BaseURL, cfg.Model); err != nil {
		return MissionValidationResult{}, err
	}
	apiKey, err := readAPIKey(modeOpenAIComp, cfg.APIKeyEnv)
	if err != nil {
		return MissionValidationResult{}, err
	}
	body, err := missionValidationChatCompletionsRequestBody(cfg, input)
	if err != nil {
		return MissionValidationResult{}, err
	}
	client := &http.Client{Timeout: decisionTimeout(cfg)}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatCompletionsEndpoint(cfg.BaseURL), bytes.NewReader(body))
	if err != nil {
		return MissionValidationResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return MissionValidationResult{}, fmt.Errorf("openai-comp mission validation request failed: %w", err)
	}
	defer resp.Body.Close()
	var parsed chatCompletionsResponse
	if err := decodeLimitedJSON("openai-comp mission validation", resp.Body, &parsed); err != nil {
		return MissionValidationResult{}, fmt.Errorf("openai-comp mission validation returned invalid JSON: %w", err)
	}
	if resp.StatusCode >= 400 {
		if parsed.Error != nil && parsed.Error.Message != "" {
			return MissionValidationResult{}, fmt.Errorf("openai-comp mission validation returned %s: %s", resp.Status, parsed.Error.Message)
		}
		return MissionValidationResult{}, fmt.Errorf("openai-comp mission validation returned %s", resp.Status)
	}
	payload, err := parsed.toolPayload("submit_mission_validation", "openai-comp mission validation")
	if err != nil {
		return MissionValidationResult{}, err
	}
	return decodeMissionValidationPayload("openai-comp mission validation", payload)
}

func generateWorkspaceEditWithResponses(ctx context.Context, cfg task.ProviderConfig, input WorkspaceEditInput) (WorkspaceEditPlan, error) {
	if err := validateRemoteConfig(modeOpenAIResponse, cfg.BaseURL, cfg.Model); err != nil {
		return WorkspaceEditPlan{}, err
	}
	apiKey, err := readAPIKey(modeOpenAIResponse, cfg.APIKeyEnv)
	if err != nil {
		return WorkspaceEditPlan{}, err
	}
	body, err := workspaceEditRequestBody(cfg, input)
	if err != nil {
		return WorkspaceEditPlan{}, err
	}
	client := &http.Client{Timeout: decisionTimeout(cfg)}
	parsed, err := doResponsesRequest(ctx, client, cfg, apiKey, "responses workspace edit", body)
	if err != nil {
		return WorkspaceEditPlan{}, err
	}
	text := strings.TrimSpace(parsed.outputText())
	if text == "" {
		retryBody, retryErr := responsesRetryWithoutReasoning(body, 8000)
		if retryErr != nil {
			return WorkspaceEditPlan{}, fmt.Errorf("responses workspace edit could not prepare empty-output retry: %w", retryErr)
		}
		retried, retryErr := doResponsesRequest(ctx, client, cfg, apiKey, "responses workspace edit retry", retryBody)
		if retryErr != nil {
			return WorkspaceEditPlan{}, retryErr
		}
		text = strings.TrimSpace(retried.outputText())
		if text == "" {
			return WorkspaceEditPlan{}, responsesEmptyOutputError("responses workspace edit", parsed, retried)
		}
		parsed = retried
	}
	return decodeWorkspaceEditPayload(responsePayloadSource("responses workspace edit", parsed.ID), []byte(text))
}

func generateWorkspaceObservationsWithResponses(ctx context.Context, cfg task.ProviderConfig, input WorkspaceObservationInput) (WorkspaceObservationPlan, error) {
	if err := validateRemoteConfig(modeOpenAIResponse, cfg.BaseURL, cfg.Model); err != nil {
		return WorkspaceObservationPlan{}, err
	}
	apiKey, err := readAPIKey(modeOpenAIResponse, cfg.APIKeyEnv)
	if err != nil {
		return WorkspaceObservationPlan{}, err
	}
	body, err := workspaceObservationRequestBody(cfg, input)
	if err != nil {
		return WorkspaceObservationPlan{}, err
	}
	client := &http.Client{Timeout: decisionTimeout(cfg)}
	parsed, err := doResponsesRequest(ctx, client, cfg, apiKey, "responses workspace observation", body)
	if err != nil {
		return WorkspaceObservationPlan{}, err
	}
	text := strings.TrimSpace(parsed.outputText())
	if text == "" {
		retryBody, retryErr := responsesRetryWithoutReasoning(body, 4000)
		if retryErr != nil {
			return WorkspaceObservationPlan{}, fmt.Errorf("responses workspace observation could not prepare empty-output retry: %w", retryErr)
		}
		retried, retryErr := doResponsesRequest(ctx, client, cfg, apiKey, "responses workspace observation retry", retryBody)
		if retryErr != nil {
			return WorkspaceObservationPlan{}, retryErr
		}
		text = strings.TrimSpace(retried.outputText())
		if text == "" {
			return WorkspaceObservationPlan{}, responsesEmptyOutputError("responses workspace observation", parsed, retried)
		}
		parsed = retried
	}
	return decodeWorkspaceObservationPayload(responsePayloadSource("responses workspace observation", parsed.ID), []byte(text))
}

func generateMissionValidationWithResponses(ctx context.Context, cfg task.ProviderConfig, input MissionValidationInput) (MissionValidationResult, error) {
	if err := validateRemoteConfig(modeOpenAIResponse, cfg.BaseURL, cfg.Model); err != nil {
		return MissionValidationResult{}, err
	}
	apiKey, err := readAPIKey(modeOpenAIResponse, cfg.APIKeyEnv)
	if err != nil {
		return MissionValidationResult{}, err
	}
	body, err := missionValidationRequestBody(cfg, input)
	if err != nil {
		return MissionValidationResult{}, err
	}
	client := &http.Client{Timeout: decisionTimeout(cfg)}
	parsed, err := doResponsesRequest(ctx, client, cfg, apiKey, "responses mission validation", body)
	if err != nil {
		return MissionValidationResult{}, err
	}
	text := strings.TrimSpace(parsed.outputText())
	if text == "" {
		retryBody, retryErr := responsesRetryWithoutReasoning(body, 4000)
		if retryErr != nil {
			return MissionValidationResult{}, fmt.Errorf("responses mission validation could not prepare empty-output retry: %w", retryErr)
		}
		retried, retryErr := doResponsesRequest(ctx, client, cfg, apiKey, "responses mission validation retry", retryBody)
		if retryErr != nil {
			return MissionValidationResult{}, retryErr
		}
		text = strings.TrimSpace(retried.outputText())
		if text == "" {
			return MissionValidationResult{}, responsesEmptyOutputError("responses mission validation", parsed, retried)
		}
		parsed = retried
	}
	return decodeMissionValidationPayload(responsePayloadSource("responses mission validation", parsed.ID), []byte(text))
}

func doResponsesRequest(ctx context.Context, client *http.Client, cfg task.ProviderConfig, apiKey, operation string, body []byte) (responsesResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, responsesEndpoint(cfg.BaseURL), bytes.NewReader(body))
	if err != nil {
		return responsesResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return responsesResponse{}, fmt.Errorf("%s request failed: %w", operation, err)
	}
	defer resp.Body.Close()
	var parsed responsesResponse
	if err := decodeLimitedJSON(operation, resp.Body, &parsed); err != nil {
		return responsesResponse{}, fmt.Errorf("%s returned invalid JSON: %w", operation, err)
	}
	if resp.StatusCode >= 400 {
		if parsed.Error != nil && parsed.Error.Message != "" {
			return responsesResponse{}, fmt.Errorf("%s returned %s: %s", operation, resp.Status, parsed.Error.Message)
		}
		return responsesResponse{}, fmt.Errorf("%s returned %s", operation, resp.Status)
	}
	return parsed, nil
}

func responsesRetryWithoutReasoning(body []byte, minMaxOutputTokens int) ([]byte, error) {
	var payload responsesRequest
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if payload.MaxOutputTokens < minMaxOutputTokens {
		payload.MaxOutputTokens = minMaxOutputTokens
	}
	payload.Reasoning = nil
	return json.Marshal(payload)
}

func responsesEmptyOutputError(operation string, first, second responsesResponse) error {
	var details []string
	if first.ID != "" {
		details = append(details, "first_response_id="+first.ID)
	}
	if first.Status != "" {
		details = append(details, "first_status="+first.Status)
	}
	if second.ID != "" {
		details = append(details, "retry_response_id="+second.ID)
	}
	if second.Status != "" {
		details = append(details, "retry_status="+second.Status)
	}
	if len(details) == 0 {
		return fmt.Errorf("%s returned empty output text after retry", operation)
	}
	return fmt.Errorf("%s returned empty output text after retry (%s)", operation, strings.Join(details, " "))
}

func generateWorkspaceEditWithAnthropic(ctx context.Context, cfg task.ProviderConfig, input WorkspaceEditInput) (WorkspaceEditPlan, error) {
	if err := validateRemoteConfig(modeAnthropic, cfg.BaseURL, cfg.Model); err != nil {
		return WorkspaceEditPlan{}, err
	}
	apiKey, err := readAPIKey(modeAnthropic, cfg.APIKeyEnv)
	if err != nil {
		return WorkspaceEditPlan{}, err
	}
	body, err := workspaceEditAnthropicRequestBody(cfg, input)
	if err != nil {
		return WorkspaceEditPlan{}, err
	}
	client := &http.Client{Timeout: decisionTimeout(cfg)}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicMessagesEndpoint(cfg.BaseURL), bytes.NewReader(body))
	if err != nil {
		return WorkspaceEditPlan{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	resp, err := client.Do(req)
	if err != nil {
		return WorkspaceEditPlan{}, fmt.Errorf("anthropic workspace edit request failed: %w", err)
	}
	defer resp.Body.Close()
	var parsed anthropicMessagesResponse
	if err := decodeLimitedJSON("anthropic workspace edit", resp.Body, &parsed); err != nil {
		return WorkspaceEditPlan{}, fmt.Errorf("anthropic workspace edit returned invalid JSON: %w", err)
	}
	if resp.StatusCode >= 400 {
		if parsed.Error != nil && parsed.Error.Message != "" {
			return WorkspaceEditPlan{}, fmt.Errorf("anthropic workspace edit returned %s: %s", resp.Status, parsed.Error.Message)
		}
		return WorkspaceEditPlan{}, fmt.Errorf("anthropic workspace edit returned %s", resp.Status)
	}
	payload, err := parsed.toolPayload(workspaceEditToolName, "anthropic workspace edit")
	if err != nil {
		return WorkspaceEditPlan{}, err
	}
	plan, err := decodeWorkspaceEditPayload("anthropic workspace edit", payload)
	if err != nil {
		return WorkspaceEditPlan{}, err
	}
	plan.TokenUsage = parsed.Usage.TokenUsageString()
	plan.PromptCacheUsage = parsed.Usage.PromptCacheUsageString()
	return plan, nil
}

func generateWorkspaceObservationsWithAnthropic(ctx context.Context, cfg task.ProviderConfig, input WorkspaceObservationInput) (WorkspaceObservationPlan, error) {
	if err := validateRemoteConfig(modeAnthropic, cfg.BaseURL, cfg.Model); err != nil {
		return WorkspaceObservationPlan{}, err
	}
	apiKey, err := readAPIKey(modeAnthropic, cfg.APIKeyEnv)
	if err != nil {
		return WorkspaceObservationPlan{}, err
	}
	body, err := workspaceObservationAnthropicRequestBody(cfg, input)
	if err != nil {
		return WorkspaceObservationPlan{}, err
	}
	client := &http.Client{Timeout: decisionTimeout(cfg)}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicMessagesEndpoint(cfg.BaseURL), bytes.NewReader(body))
	if err != nil {
		return WorkspaceObservationPlan{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	resp, err := client.Do(req)
	if err != nil {
		return WorkspaceObservationPlan{}, fmt.Errorf("anthropic workspace observation request failed: %w", err)
	}
	defer resp.Body.Close()
	var parsed anthropicMessagesResponse
	if err := decodeLimitedJSON("anthropic workspace observation", resp.Body, &parsed); err != nil {
		return WorkspaceObservationPlan{}, fmt.Errorf("anthropic workspace observation returned invalid JSON: %w", err)
	}
	if resp.StatusCode >= 400 {
		if parsed.Error != nil && parsed.Error.Message != "" {
			return WorkspaceObservationPlan{}, fmt.Errorf("anthropic workspace observation returned %s: %s", resp.Status, parsed.Error.Message)
		}
		return WorkspaceObservationPlan{}, fmt.Errorf("anthropic workspace observation returned %s", resp.Status)
	}
	payload, err := parsed.toolPayload(workspaceObservationToolName, "anthropic workspace observation")
	if err != nil {
		return WorkspaceObservationPlan{}, err
	}
	plan, err := decodeWorkspaceObservationPayload("anthropic workspace observation", payload)
	if err != nil {
		return WorkspaceObservationPlan{}, err
	}
	plan.TokenUsage = parsed.Usage.TokenUsageString()
	plan.PromptCacheUsage = parsed.Usage.PromptCacheUsageString()
	return plan, nil
}

func generateMissionValidationWithAnthropic(ctx context.Context, cfg task.ProviderConfig, input MissionValidationInput) (MissionValidationResult, error) {
	if err := validateRemoteConfig(modeAnthropic, cfg.BaseURL, cfg.Model); err != nil {
		return MissionValidationResult{}, err
	}
	apiKey, err := readAPIKey(modeAnthropic, cfg.APIKeyEnv)
	if err != nil {
		return MissionValidationResult{}, err
	}
	body, err := missionValidationAnthropicRequestBody(cfg, input)
	if err != nil {
		return MissionValidationResult{}, err
	}
	client := &http.Client{Timeout: decisionTimeout(cfg)}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicMessagesEndpoint(cfg.BaseURL), bytes.NewReader(body))
	if err != nil {
		return MissionValidationResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	resp, err := client.Do(req)
	if err != nil {
		return MissionValidationResult{}, fmt.Errorf("anthropic mission validation request failed: %w", err)
	}
	defer resp.Body.Close()
	var parsed anthropicMessagesResponse
	if err := decodeLimitedJSON("anthropic mission validation", resp.Body, &parsed); err != nil {
		return MissionValidationResult{}, fmt.Errorf("anthropic mission validation returned invalid JSON: %w", err)
	}
	if resp.StatusCode >= 400 {
		if parsed.Error != nil && parsed.Error.Message != "" {
			return MissionValidationResult{}, fmt.Errorf("anthropic mission validation returned %s: %s", resp.Status, parsed.Error.Message)
		}
		return MissionValidationResult{}, fmt.Errorf("anthropic mission validation returned %s", resp.Status)
	}
	payload, err := parsed.toolPayload("submit_mission_validation", "anthropic mission validation")
	if err != nil {
		return MissionValidationResult{}, err
	}
	result, err := decodeMissionValidationPayload("anthropic mission validation", payload)
	if err != nil {
		return MissionValidationResult{}, err
	}
	result.TokenUsage = parsed.Usage.TokenUsageString()
	result.PromptCacheUsage = parsed.Usage.PromptCacheUsageString()
	return result, nil
}

func buildWorkspaceEditPrompt(input WorkspaceEditInput) (string, error) {
	data, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", err
	}
	attemptLine := "This is the current bounded repair attempt."
	if input.RepairAttempt > 0 && input.RepairBudget > 0 {
		attemptLine = fmt.Sprintf("This is bounded repair attempt %d of %d.", input.RepairAttempt, input.RepairBudget)
	}
	return strings.TrimSpace(`
Return the smallest correct workspace edit plan for this coding task.
This is an execution-planning step over the supplied workspace snapshot and observations. Do not invent unseen files, do not claim verification has run, and do not broaden scope beyond the current evidence.

Rules:
- Start from the exact failing verifier, unmet criterion, previous failure, or operator steering visible in this context.
- Work only inside the provided workspace files and the observation excerpts.
- Use context_pack to preserve long-horizon continuity. Prefer its summary, next_step, and based_on_refs over stale guesses.
- Use context_pack.project_focus when it is present to keep the repair inside the bound workspace project step, dependencies, and branch.
- Use criteria as the durable acceptance ledger. Treat criteria.current_criterion_id as the active feature boundary and criteria.criteria[].passes=false as still failing.
- Use continuity.current_focus as the current sprint contract and continuity.startup_checklist as the structured restart checklist when they are present. If continuity.current_focus.project_focus is present, treat it as the task-scoped project binding and dependency truth.
- Use sprint as the durable current-scope contract. Prefer closing sprint.primary_criterion_id and sprint.completion_signals before touching sprint.deferred_criterion_ids. If sprint.project_focus is present, keep edits inside sprint.project_focus.primary_step_id and do not drift into sibling branches or downstream steps.
- Use baseline.command_hints and baseline.workspace_snapshot as durable repo bearings when they are present. Prefer repo-owned setup or verifier commands already surfaced there over inventing new ones.
- Use session_recent_messages plus session_messages_ref to preserve short-horizon operator steering and runtime notes during repair. Do not fall back to session.last_prompt because it is not present here.
- You may include bounded workspace commands when file edits alone are insufficient. Commands run from workspace root.
- Use argv arrays only for commands. Prefer direct safe executables and never return shell strings, pipes, redirects, heredocs, or command chaining.
- Repair commands are policy-gated by task.permission_mode_id: standard mode allows only a small safe command set and rejects shell wrappers or repo scripts as needs_approval; yolo mode is intentionally broader but still records the command.
- Use command phase "pre" only when a command must run before file edits. Otherwise prefer "post" for formatters, generators, dependency sync, builds, installs, or migrations that should happen after edits.
- Keep paths relative to workspace root.
- Never write absolute paths or .ngen paths.
- Honor task constraints exactly as written.
- Treat open_criteria as hard remaining obligations when they are present.
- Treat previous_failures as hard evidence about what already failed. Do not repeat the same bad repair path.
- If a previous failure says a patch could not be applied, prefer a safer write or request more precise observation context before trying another brittle patch.
- When the objective or criteria say rerender, regenerate, refresh, rebuild, or run a script/generator, prefer updating the source inputs and using a bounded workspace command instead of hand-editing generated outputs.
- If the workspace clearly contains a render or generation command path, prefer that durable command-backed repair over directly editing the derived artifact by hand.
- Never modify files that task constraints forbid, even if tests would pass.
- Prefer a patch for existing files. Use writes for full new-file content or when a patch is genuinely impractical.
- Keep patch empty when writes or deletes are sufficient, and keep writes/deletes empty when using patch.
- Do not rename files inside patch; use relative add/delete or write operations instead.
- Prefer editing source files over tests.
- Only return files or commands that need to change.
- Keep summary focused on the intended durable effect, not on process narration.
- If no file change or command is needed, return an empty patch, empty writes, empty deletes, and empty commands with a short explanation.

` + attemptLine + `

Workspace edit context JSON:
` + string(data)), nil
}

func workspaceEditRequestBody(cfg task.ProviderConfig, input WorkspaceEditInput) ([]byte, error) {
	prompt, err := buildWorkspaceEditPrompt(input)
	if err != nil {
		return nil, err
	}
	payload := responsesRequest{
		Model:           strings.TrimSpace(cfg.Model),
		Instructions:    workspaceEditSystemPrompt(cfg.SystemPrompt),
		MaxOutputTokens: 4000,
		Reasoning:       responsesReasoningConfigFromLevel(cfg.ThinkingLevel),
		Input: []responsesInputItem{
			{
				Type: "message",
				Role: "user",
				Content: []responsesContentItem{
					{
						Type: "input_text",
						Text: prompt,
					},
				},
			},
		},
		Text: responsesTextConfig{
			Format: responsesTextFormat{
				Name:   "ngen_workspace_edit",
				Type:   "json_schema",
				Strict: true,
				Schema: workspaceEditSchema(),
			},
		},
	}
	return json.Marshal(payload)
}

func missionValidationRequestBody(cfg task.ProviderConfig, input MissionValidationInput) ([]byte, error) {
	prompt, err := buildMissionValidationPrompt(input)
	if err != nil {
		return nil, err
	}
	payload := responsesRequest{
		Model:           strings.TrimSpace(cfg.Model),
		Instructions:    missionValidationSystemPrompt(cfg.SystemPrompt),
		MaxOutputTokens: decisionMaxOutputTokens(cfg),
		Reasoning:       responsesReasoningConfigFromLevel(cfg.ThinkingLevel),
		Input: []responsesInputItem{
			{
				Type: "message",
				Role: "user",
				Content: []responsesContentItem{
					{
						Type: "input_text",
						Text: prompt,
					},
				},
			},
		},
		Text: responsesTextConfig{
			Format: responsesTextFormat{
				Name:   "ngen_mission_validation",
				Type:   "json_schema",
				Strict: true,
				Schema: missionValidationSchema(),
			},
		},
	}
	return json.Marshal(payload)
}

func missionValidationSystemPrompt(custom string) string {
	if strings.TrimSpace(custom) != "" {
		return custom
	}
	return strings.TrimSpace(`
You are NGEN's independent mission validator.
Return only schema-valid JSON. You have no write tools and must not request execution actions. Use artifact refs as evidence, separate confirmed gaps from inferred risk, and block only when evidence or contract coverage is insufficient for mission closure.
`)
}

func workspaceEditSystemPrompt(custom string) string {
	if strings.TrimSpace(custom) != "" {
		return custom
	}
	return strings.TrimSpace(`
You are NGEN's coding execution engine.
Produce a bounded, schema-valid repair plan that changes only what is necessary to satisfy the coding objective, current acceptance criterion, and verification signal.

Rules:
- Return only JSON matching the provided schema.
- Do not include commentary outside the JSON object.
- Prefer surgical root-cause fixes over broad rewrites.
- Prefer patch hunks for existing files. Use full writes only for new files or when patch context is genuinely unsafe.
- Treat continuity.current_focus as the current sprint contract and continuity.startup_checklist as the structured restart checklist for this repair pass. If continuity.current_focus.project_focus is present, honor that task-scoped project binding and dependency boundary.
- Treat sprint as the durable current-scope contract for this repair pass. Keep edits bounded to sprint.primary_criterion_id and sprint.completion_signals when they are present. If sprint.project_focus is present, keep edits inside sprint.project_focus.primary_step_id and avoid widening into sibling project branches.
- Treat criteria as the durable acceptance ledger. Keep the repair bounded to criteria.current_criterion_id when it is present unless the execution plan explicitly says otherwise.
- Treat session_recent_messages plus session_messages_ref as the durable short-horizon steering transcript for the current repair loop.
- Use workspace_guidance when present as bounded operator/workspace instructions. If those instructions require external CLI actions, represent them as direct argv commands in this plan rather than claiming completion from prose.
- You may request bounded workspace commands when the task needs generators, formatters, dependency sync, package install, migrations, or shell-backed repair steps.
- You may request bounded external CLI commands when the current criterion explicitly requires concrete external progress and the workspace guidance/task text identifies the command workflow. Keep argv direct, avoid shell wrappers, and let runtime permission policy decide whether the command can run.
- When the task explicitly asks to rerender or regenerate an artifact, prefer command-backed repair from canonical source inputs instead of directly rewriting the derived file.
- Commands run from workspace root. Prefer direct argv exec. Standard permission mode rejects shell wrappers and repo scripts as needs_approval; use them only when task permission is yolo or the operator explicitly approved that path.
- Use the latest repair signal as the source of truth. It may be a verifier failure or an unmet criterion summary.
- Respect task constraints as hard requirements.
- Do not rewrite unrelated files.
- Avoid test edits unless the task explicitly requires them.
- If context is insufficient for a safe edit, return no file changes and rely on the observation phase rather than guessing.
`)
}

func buildWorkspaceObservationPrompt(input WorkspaceObservationInput) (string, error) {
	data, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(`
Decide whether any bounded read-only workspace inspection commands are needed before proposing a code edit.
This is a discovery step, not a repair step. Request the fewest commands that can resolve the specific uncertainty blocking a safe edit.

Rules:
- Return at most the configured number of commands.
- Use argv arrays only. Do not return shell strings, pipes, redirects, heredocs, or command chaining.
- Commands must be read-only and workspace-safe.
- Prefer rg for content discovery and narrow file reads or git status/show/log commands for repo bearings when truly needed.
- Do not request build, test, format, install, migration, generator, package-manager, network, or write commands in observation.
- When the task text explicitly asks for a read-only external CLI inspection command, you may request it only if the runtime command validator permits it.
- Use context_pack to preserve continuity and avoid rediscovering already-settled task truth.
- Use context_pack.project_focus when it is present to keep observation inside the bound project step, dependencies, and branch.
- Use criteria as the durable acceptance ledger. Treat criteria.current_criterion_id as the active feature boundary and criteria.criteria[].passes=false as still failing.
- Use continuity.current_focus and continuity.startup_checklist when they are present so the next inspection stays inside the active sprint instead of rediscovering the whole repo. If continuity.current_focus.project_focus is present, use it as the task-scoped project binding and dependency view.
- Use sprint as the durable current-scope contract. Keep inspection centered on sprint.primary_criterion_id and sprint.completion_signals before reading into deferred criteria. If sprint.project_focus is present, do not inspect sibling branches or downstream steps unless the sprint explicitly widened scope.
- Use session_recent_messages plus session_messages_ref to preserve short-horizon operator steering and runtime notes during repair observation.
- Use workspace_guidance when present as bounded operator/workspace instructions, but request only read-only commands in observation. Mutating external actions belong in the workspace edit command plan.
- Use previous_failures to avoid repeating a bad repair path. If a prior patch could not be applied, request the exact code context that would let the next repair succeed.
- If the current verification signal and workspace snapshot are already sufficient, return an empty commands array.

Workspace observation context JSON:
` + string(data)), nil
}

func workspaceObservationRequestBody(cfg task.ProviderConfig, input WorkspaceObservationInput) ([]byte, error) {
	prompt, err := buildWorkspaceObservationPrompt(input)
	if err != nil {
		return nil, err
	}
	payload := responsesRequest{
		Model:           strings.TrimSpace(cfg.Model),
		Instructions:    workspaceObservationSystemPrompt(cfg.SystemPrompt),
		MaxOutputTokens: 1200,
		Reasoning:       responsesReasoningConfigFromLevel(cfg.ThinkingLevel),
		Input: []responsesInputItem{
			{
				Type: "message",
				Role: "user",
				Content: []responsesContentItem{
					{
						Type: "input_text",
						Text: prompt,
					},
				},
			},
		},
		Text: responsesTextConfig{
			Format: responsesTextFormat{
				Name:   "ngen_workspace_observation",
				Type:   "json_schema",
				Strict: true,
				Schema: workspaceObservationSchema(),
			},
		},
	}
	return json.Marshal(payload)
}

func workspaceEditChatCompletionsRequestBody(cfg task.ProviderConfig, input WorkspaceEditInput) ([]byte, error) {
	prompt, err := buildWorkspaceEditPrompt(input)
	if err != nil {
		return nil, err
	}
	payload := chatCompletionsRequest{
		Model: strings.TrimSpace(cfg.Model),
		Messages: []chatCompletionsMessage{
			{Role: "system", Content: workspaceEditSystemPrompt(cfg.SystemPrompt)},
			{Role: "user", Content: prompt},
		},
		MaxTokens: 4000,
		Tools: []chatCompletionsTool{
			{
				Type: "function",
				Function: chatCompletionsToolFunction{
					Name:        workspaceEditToolName,
					Description: "Submit a bounded NGEN coding repair plan: minimal patch/write/delete operations plus optional direct argv commands, grounded only in the provided workspace snapshot, observations, criteria, and constraints.",
					Parameters:  workspaceEditSchema(),
				},
			},
		},
		ToolChoice: chatCompletionsToolChoice{
			Type: "function",
			Function: chatCompletionsToolChoiceFunction{
				Name: workspaceEditToolName,
			},
		},
	}
	return json.Marshal(payload)
}

func workspaceObservationChatCompletionsRequestBody(cfg task.ProviderConfig, input WorkspaceObservationInput) ([]byte, error) {
	prompt, err := buildWorkspaceObservationPrompt(input)
	if err != nil {
		return nil, err
	}
	payload := chatCompletionsRequest{
		Model: strings.TrimSpace(cfg.Model),
		Messages: []chatCompletionsMessage{
			{Role: "system", Content: workspaceObservationSystemPrompt(cfg.SystemPrompt)},
			{Role: "user", Content: prompt},
		},
		MaxTokens: 1200,
		Tools: []chatCompletionsTool{
			{
				Type: "function",
				Function: chatCompletionsToolFunction{
					Name:        workspaceObservationToolName,
					Description: "Submit the fewest read-only argv inspection commands needed to make the next NGEN coding repair safe; return zero commands when the supplied context is sufficient.",
					Parameters:  workspaceObservationSchema(),
				},
			},
		},
		ToolChoice: chatCompletionsToolChoice{
			Type: "function",
			Function: chatCompletionsToolChoiceFunction{
				Name: workspaceObservationToolName,
			},
		},
	}
	return json.Marshal(payload)
}

func missionValidationChatCompletionsRequestBody(cfg task.ProviderConfig, input MissionValidationInput) ([]byte, error) {
	prompt, err := buildMissionValidationPrompt(input)
	if err != nil {
		return nil, err
	}
	payload := chatCompletionsRequest{
		Model: strings.TrimSpace(cfg.Model),
		Messages: []chatCompletionsMessage{
			{Role: "system", Content: missionValidationSystemPrompt(cfg.SystemPrompt)},
			{Role: "user", Content: prompt},
		},
		MaxTokens: decisionMaxOutputTokens(cfg),
		Tools: []chatCompletionsTool{
			{
				Type: "function",
				Function: chatCompletionsToolFunction{
					Name:        "submit_mission_validation",
					Description: "Submit a read-only NGEN mission validation result with evidence-backed findings only; do not request execution, edits, task creation, or worker actions.",
					Parameters:  missionValidationSchema(),
				},
			},
		},
		ToolChoice: chatCompletionsToolChoice{
			Type: "function",
			Function: chatCompletionsToolChoiceFunction{
				Name: "submit_mission_validation",
			},
		},
	}
	return json.Marshal(payload)
}

func workspaceEditAnthropicRequestBody(cfg task.ProviderConfig, input WorkspaceEditInput) ([]byte, error) {
	prompt, err := buildWorkspaceEditPrompt(input)
	if err != nil {
		return nil, err
	}
	payload := anthropicMessagesRequest{
		Model:     strings.TrimSpace(cfg.Model),
		System:    anthropicCacheableSystem(workspaceEditSystemPrompt(cfg.SystemPrompt)),
		MaxTokens: 4000,
		Messages:  []anthropicMessage{anthropicCacheableUserMessage(prompt)},
		Tools: []anthropicTool{
			{
				Name:        workspaceEditToolName,
				Description: "Submit a bounded NGEN coding repair plan: minimal patch/write/delete operations plus optional direct argv commands, grounded only in the provided workspace snapshot, observations, criteria, and constraints.",
				InputSchema: workspaceEditSchema(),
			},
		},
		ToolChoice: anthropicToolChoice{
			Type: "tool",
			Name: workspaceEditToolName,
		},
	}
	return json.Marshal(payload)
}

func workspaceObservationAnthropicRequestBody(cfg task.ProviderConfig, input WorkspaceObservationInput) ([]byte, error) {
	prompt, err := buildWorkspaceObservationPrompt(input)
	if err != nil {
		return nil, err
	}
	payload := anthropicMessagesRequest{
		Model:     strings.TrimSpace(cfg.Model),
		System:    anthropicCacheableSystem(workspaceObservationSystemPrompt(cfg.SystemPrompt)),
		MaxTokens: 1200,
		Messages:  []anthropicMessage{anthropicCacheableUserMessage(prompt)},
		Tools: []anthropicTool{
			{
				Name:        workspaceObservationToolName,
				Description: "Submit the fewest read-only argv inspection commands needed to make the next NGEN coding repair safe; return zero commands when the supplied context is sufficient.",
				InputSchema: workspaceObservationSchema(),
			},
		},
		ToolChoice: anthropicToolChoice{
			Type: "tool",
			Name: workspaceObservationToolName,
		},
	}
	return json.Marshal(payload)
}

func missionValidationAnthropicRequestBody(cfg task.ProviderConfig, input MissionValidationInput) ([]byte, error) {
	prompt, err := buildMissionValidationPrompt(input)
	if err != nil {
		return nil, err
	}
	payload := anthropicMessagesRequest{
		Model:     strings.TrimSpace(cfg.Model),
		System:    anthropicCacheableSystem(missionValidationSystemPrompt(cfg.SystemPrompt)),
		MaxTokens: decisionMaxOutputTokens(cfg),
		Messages:  []anthropicMessage{anthropicCacheableUserMessage(prompt)},
		Tools: []anthropicTool{
			{
				Name:        "submit_mission_validation",
				Description: "Submit a read-only NGEN mission validation result with evidence-backed findings only; do not request execution, edits, task creation, or worker actions.",
				InputSchema: missionValidationSchema(),
			},
		},
		ToolChoice: anthropicToolChoice{
			Type: "tool",
			Name: "submit_mission_validation",
		},
	}
	return json.Marshal(payload)
}

func anthropicCacheableSystem(prompt string) []anthropicTextBlock {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil
	}
	return []anthropicTextBlock{newAnthropicTextBlock(prompt, true)}
}

func anthropicCacheableUserMessage(prompt string) anthropicMessage {
	return anthropicMessage{
		Role:    "user",
		Content: anthropicPromptTextBlocks(prompt),
	}
}

func anthropicPromptTextBlocks(prompt string) []anthropicTextBlock {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return []anthropicTextBlock{newAnthropicTextBlock("", false)}
	}
	for _, rule := range anthropicPromptCacheSplitRules {
		if idx := strings.Index(prompt, rule.ContextMarker); idx >= 0 {
			preludeEnd := idx + len(rule.ContextMarker)
			return anthropicCacheablePromptBlocks(prompt[:preludeEnd], prompt[preludeEnd:], rule.VolatileMarkers)
		}
	}
	return []anthropicTextBlock{newAnthropicTextBlock(prompt, true)}
}

func anthropicCacheablePromptBlocks(prelude, contextTail string, volatileMarkers []string) []anthropicTextBlock {
	blocks := []anthropicTextBlock{newAnthropicTextBlock(prelude, true)}
	if idx := firstAnthropicVolatileMarkerIndex(contextTail, volatileMarkers); idx >= 0 {
		stableTail := contextTail[:idx]
		volatileTail := contextTail[idx:]
		if stableTail != "" {
			blocks = append(blocks, newAnthropicTextBlock(stableTail, true))
		}
		if volatileTail != "" {
			blocks = append(blocks, newAnthropicTextBlock(volatileTail, true))
		}
		return blocks
	}
	if contextTail != "" {
		blocks = append(blocks, newAnthropicTextBlock(contextTail, true))
	}
	return blocks
}

func firstAnthropicVolatileMarkerIndex(text string, markers []string) int {
	first := -1
	for _, marker := range markers {
		if marker == "" {
			continue
		}
		idx := strings.Index(text, marker)
		if idx < 0 {
			continue
		}
		if first < 0 || idx < first {
			first = idx
		}
	}
	return first
}

func newAnthropicTextBlock(text string, cacheable bool) anthropicTextBlock {
	block := anthropicTextBlock{
		Type: "text",
		Text: text,
	}
	if cacheable {
		block.CacheControl = &anthropicCacheControl{Type: anthropicCacheControlTypeEphemeral}
	}
	return block
}

func workspaceObservationSystemPrompt(custom string) string {
	if strings.TrimSpace(custom) != "" {
		return custom
	}
	return strings.TrimSpace(`
You are NGEN's coding observation engine.
Request only the read-only workspace inspection needed to make the next repair safe and grounded.

Rules:
- Return only JSON matching the provided schema.
- Only request read-only commands that help you locate or inspect the code you need to change.
- Do not request commands that write files, change git state, install packages, or mutate the environment.
- Be conservative: zero commands is preferred when current context is enough.
- Prefer exact, narrow discovery over broad repository scans; use rg-style search before reading large files.
- Treat continuity.current_focus as the current sprint contract and continuity.startup_checklist as the structured restart checklist for this observation pass. If continuity.current_focus.project_focus is present, honor that task-scoped project binding and dependency view.
- Treat sprint.project_focus and context_pack.project_focus as the task-scoped workspace project focus when they are present. Keep observation inside the bound project step and branch before widening into sibling work.
- Treat criteria as the durable acceptance ledger. Keep inspection bounded to criteria.current_criterion_id when it is present unless the execution plan explicitly widens scope.
- Treat baseline.command_hints and baseline.workspace_snapshot as durable repo bearings when they are present. Prefer repo-owned setup or verifier commands already surfaced there over guessing.
- Treat open_criteria as the current remaining contract when they are present.
- Treat session_recent_messages plus session_messages_ref as the durable short-horizon steering transcript for the current repair observation loop.
`)
}

func workspaceObservationSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"summary": map[string]any{
				"type": "string",
			},
			"commands": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"argv": map[string]any{
							"type":     "array",
							"minItems": 1,
							"maxItems": 16,
							"items": map[string]any{
								"type": "string",
							},
						},
						"reason": map[string]any{
							"type": "string",
						},
					},
					"required": []string{"argv", "reason"},
				},
			},
		},
		"required": []string{"summary", "commands"},
	}
}

func workspaceEditSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"summary": map[string]any{
				"type": "string",
			},
			"patch": map[string]any{
				"type": "string",
			},
			"writes": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"path":    map[string]any{"type": "string"},
						"content": map[string]any{"type": "string"},
					},
					"required": []string{"path", "content"},
				},
			},
			"deletes": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
				},
			},
			"commands": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"phase": map[string]any{
							"type": "string",
							"enum": []string{"", "pre", "post"},
						},
						"argv": map[string]any{
							"type":     "array",
							"minItems": 1,
							"maxItems": 32,
							"items": map[string]any{
								"type": "string",
							},
						},
						"reason": map[string]any{
							"type": "string",
						},
					},
					"required": []string{"phase", "argv", "reason"},
				},
			},
		},
		"required": []string{"summary", "patch", "writes", "deletes", "commands"},
	}
}

func missionValidationSchema() map[string]any {
	findingSchema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"finding_id": map[string]any{"type": "string"},
			"category": map[string]any{
				"type": "string",
				"enum": []string{"", "missing_evidence", "coverage_gap", "correctness_gap", "stale_context_risk", "inferred_risk", "not_observed", "security_risk", "scope_drift"},
			},
			"severity": map[string]any{
				"type": "string",
				"enum": []string{"", "critical", "high", "medium", "low", "info"},
			},
			"blocking": map[string]any{"type": "boolean"},
			"summary":  map[string]any{"type": "string"},
			"evidence_refs": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
				},
			},
			"recommended_action": map[string]any{"type": "string"},
		},
		"required": []string{"finding_id", "category", "severity", "blocking", "summary", "evidence_refs", "recommended_action"},
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"status": map[string]any{
				"type": "string",
				"enum": []string{"passed", "blocking"},
			},
			"summary": map[string]any{"type": "string"},
			"findings": map[string]any{
				"type":  "array",
				"items": findingSchema,
			},
		},
		"required": []string{"status", "summary", "findings"},
	}
}

func decisionPlanStepSchema(statusEnum []string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"id": map[string]any{
				"type": "string",
			},
			"parent_step_id": map[string]any{
				"type": "string",
			},
			"depends_on": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
				},
			},
			"priority": map[string]any{
				"type": "string",
				"enum": []string{"", task.StepPriorityHigh, task.StepPriorityMedium, task.StepPriorityLow},
			},
			"title": map[string]any{
				"type": "string",
			},
			"status": map[string]any{
				"type": "string",
				"enum": statusEnum,
			},
			"covers": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
				},
			},
			"notes": map[string]any{
				"type": "string",
			},
		},
		"required": []string{"id", "parent_step_id", "depends_on", "priority", "title", "status", "covers", "notes"},
	}
}

func decisionProjectStepSchema(statusEnum []string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"id": map[string]any{
				"type": "string",
			},
			"parent_step_id": map[string]any{
				"type": "string",
			},
			"depends_on": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
				},
			},
			"priority": map[string]any{
				"type": "string",
				"enum": []string{"", task.StepPriorityHigh, task.StepPriorityMedium, task.StepPriorityLow},
			},
			"title": map[string]any{
				"type": "string",
			},
			"status": map[string]any{
				"type": "string",
				"enum": statusEnum,
			},
			"branch_id": map[string]any{
				"type": "string",
			},
			"task_id": map[string]any{
				"type": "string",
			},
			"notes": map[string]any{
				"type": "string",
			},
		},
		"required": []string{"id", "parent_step_id", "depends_on", "priority", "title", "status", "branch_id", "task_id", "notes"},
	}
}

func decisionProjectBranchSchema(statusEnum []string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"id": map[string]any{
				"type": "string",
			},
			"title": map[string]any{
				"type": "string",
			},
			"status": map[string]any{
				"type": "string",
				"enum": statusEnum,
			},
			"task_id": map[string]any{
				"type": "string",
			},
			"notes": map[string]any{
				"type": "string",
			},
		},
		"required": []string{"id", "title", "status", "task_id", "notes"},
	}
}

func planPatchOperationSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"op": map[string]any{
				"type": "string",
				"enum": []string{task.PlanPatchOpSetExplanation, task.PlanPatchOpUpsertStep, task.PlanPatchOpRemoveStep},
			},
			"explanation": map[string]any{
				"type": "string",
			},
			"step_id": map[string]any{
				"type": "string",
			},
			"after_step_id": map[string]any{
				"type": "string",
			},
			"step": decisionPlanStepSchema([]string{"", task.StepStatusPending, task.StepStatusInProgress, task.StepStatusCompleted, task.StepStatusCancelled}),
		},
		"required": []string{"op", "explanation", "step_id", "after_step_id", "step"},
	}
}

func projectPatchOperationSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"op": map[string]any{
				"type": "string",
				"enum": []string{
					task.ProjectPatchOpSetExplanation,
					task.ProjectPatchOpUpsertStep,
					task.ProjectPatchOpRemoveStep,
					task.ProjectPatchOpSetStepDependsOn,
					task.ProjectPatchOpSetStepParent,
					task.ProjectPatchOpBindStepBranch,
					task.ProjectPatchOpBindStepTask,
					task.ProjectPatchOpUpsertBranch,
					task.ProjectPatchOpRemoveBranch,
					task.ProjectPatchOpBindBranchTask,
					task.ProjectPatchOpSetBranchStatus,
				},
			},
			"explanation": map[string]any{
				"type": "string",
			},
			"step_id": map[string]any{
				"type": "string",
			},
			"after_step_id": map[string]any{
				"type": "string",
			},
			"branch_id": map[string]any{
				"type": "string",
			},
			"parent_step_id": map[string]any{
				"type": "string",
			},
			"task_id": map[string]any{
				"type": "string",
			},
			"depends_on": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
				},
			},
			"status": map[string]any{
				"type": "string",
				"enum": []string{"", task.ProjectBranchStatusPending, task.ProjectBranchStatusActive, task.ProjectBranchStatusBlocked, task.ProjectBranchStatusCompleted, task.ProjectBranchStatusCancelled},
			},
			"step":   decisionProjectStepSchema([]string{"", task.ProjectStepStatusPending, task.ProjectStepStatusInProgress, task.ProjectStepStatusBlocked, task.ProjectStepStatusCompleted, task.ProjectStepStatusCancelled}),
			"branch": decisionProjectBranchSchema([]string{"", task.ProjectBranchStatusPending, task.ProjectBranchStatusActive, task.ProjectBranchStatusBlocked, task.ProjectBranchStatusCompleted, task.ProjectBranchStatusCancelled}),
		},
		"required": []string{"op", "explanation", "step_id", "after_step_id", "branch_id", "parent_step_id", "task_id", "depends_on", "status", "step", "branch"},
	}
}

func decisionSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string",
				"enum": []string{"run", "resume", "respond", "review", "task_create", "task_update", "task_patch", "project_update", "project_patch", "memory_promote", "worker_spawn", "worker_continue", "wait", "approval_request", "block", "noop"},
			},
			"summary": map[string]any{
				"type": "string",
			},
			"response_text": map[string]any{
				"type": "string",
			},
			"task_kind": map[string]any{
				"type": "string",
				"enum": []string{"", string(task.KindCoding), string(task.KindGeneral), string(task.KindSecurityReview), string(task.KindReviewer)},
			},
			"task_preset_id": map[string]any{
				"type": "string",
				"enum": []string{"", string(task.PresetDocsLite)},
			},
			"task_title": map[string]any{
				"type": "string",
			},
			"task_objective": map[string]any{
				"type": "string",
			},
			"task_criteria": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
				},
			},
			"task_constraints": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
				},
			},
			"task_permission_mode_id": map[string]any{
				"type": "string",
				"enum": []string{"", task.PermissionModeStandard, task.PermissionModeYolo},
			},
			"project_step_id": map[string]any{
				"type": "string",
			},
			"project_branch_id": map[string]any{
				"type": "string",
			},
			"plan_explanation": map[string]any{
				"type": "string",
			},
			"plan_steps": map[string]any{
				"type":  "array",
				"items": decisionPlanStepSchema([]string{task.StepStatusPending, task.StepStatusInProgress, task.StepStatusCompleted, task.StepStatusCancelled}),
			},
			"plan_patch_operations": map[string]any{
				"type":  "array",
				"items": planPatchOperationSchema(),
			},
			"project_explanation": map[string]any{
				"type": "string",
			},
			"project_steps": map[string]any{
				"type":  "array",
				"items": decisionProjectStepSchema([]string{task.ProjectStepStatusPending, task.ProjectStepStatusInProgress, task.ProjectStepStatusBlocked, task.ProjectStepStatusCompleted, task.ProjectStepStatusCancelled}),
			},
			"project_branches": map[string]any{
				"type":  "array",
				"items": decisionProjectBranchSchema([]string{task.ProjectBranchStatusPending, task.ProjectBranchStatusActive, task.ProjectBranchStatusBlocked, task.ProjectBranchStatusCompleted, task.ProjectBranchStatusCancelled}),
			},
			"project_patch_operations": map[string]any{
				"type":  "array",
				"items": projectPatchOperationSchema(),
			},
			"memory_kind": map[string]any{
				"type": "string",
				"enum": []string{"", task.MemoryKindTaskCompletion, task.MemoryKindTaskMilestone, task.MemoryKindTaskDecision, task.MemoryKindTaskBlocker, task.MemoryKindTaskNote},
			},
			"memory_refs": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
				},
			},
			"worker_id": map[string]any{
				"type": "string",
			},
			"worker_role": map[string]any{
				"type": "string",
			},
			"worker_objective": map[string]any{
				"type": "string",
			},
			"watch_interval": map[string]any{
				"type": "string",
			},
			"watch_reason": map[string]any{
				"type": "string",
			},
			"approval_scope": map[string]any{
				"type": "string",
			},
			"approval_reason": map[string]any{
				"type": "string",
			},
		},
		"required": []string{
			"action",
			"summary",
			"response_text",
			"task_kind",
			"task_preset_id",
			"task_title",
			"task_objective",
			"task_criteria",
			"task_constraints",
			"task_permission_mode_id",
			"project_step_id",
			"project_branch_id",
			"plan_explanation",
			"plan_steps",
			"plan_patch_operations",
			"project_explanation",
			"project_steps",
			"project_branches",
			"project_patch_operations",
			"memory_kind",
			"memory_refs",
			"worker_id",
			"worker_role",
			"worker_objective",
			"watch_interval",
			"watch_reason",
			"approval_scope",
			"approval_reason",
		},
	}
}

func responsesEndpoint(baseURL string) string {
	return appendEndpoint(baseURL, "responses")
}

func chatCompletionsEndpoint(baseURL string) string {
	return appendEndpoint(baseURL, "chat/completions")
}

func anthropicMessagesEndpoint(baseURL string) string {
	return appendEndpoint(baseURL, "messages")
}

func appendEndpoint(baseURL, suffix string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(strings.ToLower(base), "/"+strings.ToLower(suffix)) {
		return base
	}
	return base + "/" + suffix
}

func validateDecision(decision Decision) (Decision, error) {
	decision.Action = strings.TrimSpace(strings.ToLower(decision.Action))
	decision.Summary = strings.TrimSpace(decision.Summary)
	decision.ResponseText = strings.TrimSpace(decision.ResponseText)
	decision.TaskKind = strings.TrimSpace(strings.ToLower(decision.TaskKind))
	decision.TaskPresetID = strings.TrimSpace(decision.TaskPresetID)
	decision.TaskTitle = strings.TrimSpace(decision.TaskTitle)
	decision.TaskObjective = strings.TrimSpace(decision.TaskObjective)
	decision.TaskCriteria = normalizeStringSlice(decision.TaskCriteria)
	decision.TaskConstraints = normalizeStringSlice(decision.TaskConstraints)
	decision.TaskPermissionModeID = strings.TrimSpace(strings.ToLower(decision.TaskPermissionModeID))
	decision.ProjectStepID = strings.TrimSpace(decision.ProjectStepID)
	decision.ProjectBranchID = strings.TrimSpace(decision.ProjectBranchID)
	decision.PlanExplanation = strings.TrimSpace(decision.PlanExplanation)
	decision.ProjectExplanation = strings.TrimSpace(decision.ProjectExplanation)
	for i := range decision.PlanSteps {
		decision.PlanSteps[i].Title = strings.TrimSpace(decision.PlanSteps[i].Title)
		decision.PlanSteps[i].Status = strings.TrimSpace(strings.ToLower(decision.PlanSteps[i].Status))
		decision.PlanSteps[i].Notes = strings.TrimSpace(decision.PlanSteps[i].Notes)
		decision.PlanSteps[i].ID = strings.TrimSpace(decision.PlanSteps[i].ID)
		decision.PlanSteps[i].ParentStepID = strings.TrimSpace(decision.PlanSteps[i].ParentStepID)
		decision.PlanSteps[i].DependsOn = normalizeStringSlice(decision.PlanSteps[i].DependsOn)
		switch strings.TrimSpace(strings.ToLower(decision.PlanSteps[i].Priority)) {
		case "", task.StepPriorityMedium:
			decision.PlanSteps[i].Priority = task.StepPriorityMedium
		case task.StepPriorityHigh, task.StepPriorityLow:
			decision.PlanSteps[i].Priority = strings.TrimSpace(strings.ToLower(decision.PlanSteps[i].Priority))
		default:
			return Decision{}, fmt.Errorf("provider task_update returned invalid step priority: %s", decision.PlanSteps[i].Priority)
		}
		decision.PlanSteps[i].Covers = normalizeStringSlice(decision.PlanSteps[i].Covers)
		switch decision.PlanSteps[i].Status {
		case task.StepStatusPending, task.StepStatusInProgress, task.StepStatusCompleted, task.StepStatusCancelled:
		case "":
			decision.PlanSteps[i].Status = task.StepStatusPending
		default:
			return Decision{}, fmt.Errorf("provider task_update returned invalid step status: %s", decision.PlanSteps[i].Status)
		}
	}
	for i := range decision.ProjectSteps {
		decision.ProjectSteps[i].Title = strings.TrimSpace(decision.ProjectSteps[i].Title)
		decision.ProjectSteps[i].Status = strings.TrimSpace(strings.ToLower(decision.ProjectSteps[i].Status))
		decision.ProjectSteps[i].Notes = strings.TrimSpace(decision.ProjectSteps[i].Notes)
		decision.ProjectSteps[i].ID = strings.TrimSpace(decision.ProjectSteps[i].ID)
		decision.ProjectSteps[i].ParentStepID = strings.TrimSpace(decision.ProjectSteps[i].ParentStepID)
		decision.ProjectSteps[i].DependsOn = normalizeStringSlice(decision.ProjectSteps[i].DependsOn)
		decision.ProjectSteps[i].BranchID = strings.TrimSpace(decision.ProjectSteps[i].BranchID)
		decision.ProjectSteps[i].TaskID = strings.TrimSpace(decision.ProjectSteps[i].TaskID)
		switch strings.TrimSpace(strings.ToLower(decision.ProjectSteps[i].Priority)) {
		case "", task.StepPriorityMedium:
			decision.ProjectSteps[i].Priority = task.StepPriorityMedium
		case task.StepPriorityHigh, task.StepPriorityLow:
			decision.ProjectSteps[i].Priority = strings.TrimSpace(strings.ToLower(decision.ProjectSteps[i].Priority))
		default:
			return Decision{}, fmt.Errorf("provider project_update returned invalid step priority: %s", decision.ProjectSteps[i].Priority)
		}
		switch decision.ProjectSteps[i].Status {
		case task.ProjectStepStatusPending, task.ProjectStepStatusInProgress, task.ProjectStepStatusBlocked, task.ProjectStepStatusCompleted, task.ProjectStepStatusCancelled:
		case "":
			decision.ProjectSteps[i].Status = task.ProjectStepStatusPending
		default:
			return Decision{}, fmt.Errorf("provider project_update returned invalid step status: %s", decision.ProjectSteps[i].Status)
		}
	}
	for i := range decision.ProjectBranches {
		decision.ProjectBranches[i].ID = strings.TrimSpace(decision.ProjectBranches[i].ID)
		decision.ProjectBranches[i].Title = strings.TrimSpace(decision.ProjectBranches[i].Title)
		decision.ProjectBranches[i].Status = strings.TrimSpace(strings.ToLower(decision.ProjectBranches[i].Status))
		decision.ProjectBranches[i].TaskID = strings.TrimSpace(decision.ProjectBranches[i].TaskID)
		decision.ProjectBranches[i].Notes = strings.TrimSpace(decision.ProjectBranches[i].Notes)
		switch decision.ProjectBranches[i].Status {
		case task.ProjectBranchStatusPending, task.ProjectBranchStatusActive, task.ProjectBranchStatusBlocked, task.ProjectBranchStatusCompleted, task.ProjectBranchStatusCancelled:
		case "":
			decision.ProjectBranches[i].Status = task.ProjectBranchStatusPending
		default:
			return Decision{}, fmt.Errorf("provider project_update returned invalid branch status: %s", decision.ProjectBranches[i].Status)
		}
	}
	decision.WorkerID = strings.TrimSpace(decision.WorkerID)
	decision.WorkerRole = strings.TrimSpace(decision.WorkerRole)
	decision.WorkerObjective = strings.TrimSpace(decision.WorkerObjective)
	decision.MemoryKind = task.CanonicalMemoryKind(decision.MemoryKind)
	decision.MemoryRefs = normalizeStringSlice(decision.MemoryRefs)
	decision.WatchInterval = strings.TrimSpace(decision.WatchInterval)
	decision.WatchReason = strings.TrimSpace(decision.WatchReason)
	decision.ApprovalScope = strings.TrimSpace(decision.ApprovalScope)
	decision.ApprovalReason = strings.TrimSpace(decision.ApprovalReason)
	if len(decision.PlanPatchOperations) > 0 {
		normalizedPatch, err := task.NormalizePlanPatch(task.PlanPatch{Operations: decision.PlanPatchOperations})
		if err != nil {
			return Decision{}, err
		}
		decision.PlanPatchOperations = normalizedPatch.Operations
	}
	if len(decision.ProjectPatchOperations) > 0 {
		normalizedPatch, err := task.NormalizeProjectPatch(task.ProjectPatch{Operations: decision.ProjectPatchOperations})
		if err != nil {
			return Decision{}, err
		}
		decision.ProjectPatchOperations = normalizedPatch.Operations
	}
	if decision.Action == "" {
		return Decision{}, errors.New("provider decision missing action")
	}
	if _, ok := allowedActions[decision.Action]; !ok {
		return Decision{}, fmt.Errorf("unsupported provider action: %s", decision.Action)
	}
	if decision.Summary == "" {
		decision.Summary = "Provider decision received."
	}
	if decision.Action == "respond" && decision.ResponseText == "" {
		return Decision{}, errors.New("provider respond missing response_text")
	}
	if decision.Action == "wait" && decision.WatchInterval != "" {
		parsed, err := time.ParseDuration(decision.WatchInterval)
		if err != nil || parsed <= 0 {
			return Decision{}, fmt.Errorf("provider decision returned invalid watch_interval: %s", decision.WatchInterval)
		}
	}
	if decision.Action == "wait" && decision.WatchReason == "" {
		decision.WatchReason = "provider-directed watch"
	}
	if decision.Action == "task_create" {
		file := task.TaskFile{
			Kind:             task.Kind(decision.TaskKind),
			PresetID:         task.PresetID(decision.TaskPresetID),
			Title:            decision.TaskTitle,
			Objective:        decision.TaskObjective,
			SuccessCriteria:  criteriaFromStrings(decision.TaskCriteria),
			Constraints:      append([]string(nil), decision.TaskConstraints...),
			PermissionModeID: decision.TaskPermissionModeID,
		}
		if err := task.ValidateTaskFile(file); err != nil {
			return Decision{}, err
		}
	}
	if decision.Action == "task_update" {
		if len(decision.PlanSteps) == 0 {
			return Decision{}, errors.New("provider task_update missing plan_steps")
		}
	}
	if decision.Action == "task_patch" {
		if len(decision.PlanPatchOperations) == 0 {
			return Decision{}, errors.New("provider task_patch missing plan_patch_operations")
		}
	}
	if decision.Action == "project_update" {
		update, err := task.NormalizeProjectUpdate(task.ProjectUpdate{
			Explanation: decision.ProjectExplanation,
			Steps:       decision.ProjectSteps,
			Branches:    decision.ProjectBranches,
		})
		if err != nil {
			return Decision{}, err
		}
		decision.ProjectExplanation = update.Explanation
		decision.ProjectSteps = update.Steps
		decision.ProjectBranches = update.Branches
		if len(decision.ProjectSteps) == 0 && len(decision.ProjectBranches) == 0 {
			return Decision{}, errors.New("provider project_update missing project_steps or project_branches")
		}
	}
	if decision.Action == "project_patch" {
		if len(decision.ProjectPatchOperations) == 0 {
			return Decision{}, errors.New("provider project_patch missing project_patch_operations")
		}
	}
	if decision.Action == "memory_promote" {
		if !task.IsSupportedMemoryKind(decision.MemoryKind) {
			return Decision{}, fmt.Errorf("provider memory_promote returned unsupported memory_kind: %s", decision.MemoryKind)
		}
	}
	if decision.Action == "worker_spawn" {
		canonicalRole, _, _, err := task.NormalizeWorkerRole(decision.WorkerRole)
		if err != nil {
			return Decision{}, err
		}
		if decision.WorkerObjective == "" {
			return Decision{}, errors.New("provider worker_spawn missing worker_objective")
		}
		decision.WorkerRole = canonicalRole
	}
	if decision.Action == "worker_continue" {
		if decision.WorkerID == "" {
			return Decision{}, errors.New("provider worker_continue missing worker_id")
		}
	}
	if decision.Action == "approval_request" {
		if decision.ApprovalScope == "" {
			return Decision{}, errors.New("provider approval_request missing approval_scope")
		}
		if decision.ApprovalReason == "" {
			decision.ApprovalReason = decision.Summary
		}
	}
	return decision, nil
}

func normalizeStringSlice(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
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

func criteriaFromStrings(values []string) []task.SuccessCriterion {
	out := make([]task.SuccessCriterion, 0, len(values))
	for idx, value := range values {
		statement := strings.TrimSpace(value)
		if statement == "" {
			continue
		}
		out = append(out, task.SuccessCriterion{
			ID:        fmt.Sprintf("SC-%03d", idx+1),
			Statement: statement,
		})
	}
	return out
}

func decisionTimeout(cfg task.ProviderConfig) time.Duration {
	timeout := time.Duration(cfg.DecisionTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return timeout
}

func decisionMaxOutputTokens(cfg task.ProviderConfig) int {
	if cfg.DecisionMaxOutputTokens > 0 {
		return cfg.DecisionMaxOutputTokens
	}
	return defaultDecisionMaxTokens
}

func normalizedAPIKeyEnv(keyEnv string) string {
	keyEnv = strings.TrimSpace(keyEnv)
	if keyEnv == "" {
		return defaultAPIKeyEnv
	}
	return keyEnv
}

func validateRemoteConfig(mode, baseURL, model string) error {
	if strings.TrimSpace(baseURL) == "" {
		return fmt.Errorf("provider mode %s requires provider.base_url", mode)
	}
	if strings.TrimSpace(model) == "" {
		return fmt.Errorf("provider mode %s requires provider.model", mode)
	}
	return nil
}

func readAPIKey(mode, keyEnv string) (string, error) {
	keyEnv = normalizedAPIKeyEnv(keyEnv)
	apiKey := strings.TrimSpace(os.Getenv(keyEnv))
	if apiKey == "" {
		return "", fmt.Errorf("provider mode %s requires env %s", mode, keyEnv)
	}
	return apiKey, nil
}

func responsePayloadSource(source, responseID string) string {
	responseID = strings.TrimSpace(responseID)
	if responseID == "" {
		return source
	}
	return fmt.Sprintf("%s response_id=%s", source, responseID)
}

func decodeDecisionPayload(source string, raw []byte) (Decision, error) {
	candidates := decisionJSONCandidates(raw)
	var lastErr error
	for _, candidate := range candidates {
		var decision Decision
		if err := json.Unmarshal([]byte(candidate), &decision); err != nil {
			lastErr = err
			continue
		}
		return validateDecision(decision)
	}
	if lastErr == nil {
		lastErr = errors.New("empty decision payload")
	}
	return Decision{}, fmt.Errorf("%s returned invalid decision JSON: %w; raw_excerpt=%q", source, lastErr, rawPayloadExcerpt(raw, 500))
}

func decodeWorkspaceEditPayload(source string, raw []byte) (WorkspaceEditPlan, error) {
	candidates := decisionJSONCandidates(raw)
	var lastErr error
	for _, candidate := range candidates {
		var plan WorkspaceEditPlan
		if err := json.Unmarshal([]byte(candidate), &plan); err != nil {
			lastErr = err
			continue
		}
		return validateWorkspaceEditPlan(plan)
	}
	if lastErr == nil {
		lastErr = errors.New("empty workspace edit payload")
	}
	return WorkspaceEditPlan{}, fmt.Errorf("%s returned invalid workspace edit JSON: %w; raw_excerpt=%q", source, lastErr, rawPayloadExcerpt(raw, 500))
}

func decodeWorkspaceObservationPayload(source string, raw []byte) (WorkspaceObservationPlan, error) {
	candidates := decisionJSONCandidates(raw)
	var lastErr error
	for _, candidate := range candidates {
		var plan WorkspaceObservationPlan
		if err := json.Unmarshal([]byte(candidate), &plan); err != nil {
			lastErr = err
			continue
		}
		return validateWorkspaceObservationPlan(plan)
	}
	if lastErr == nil {
		lastErr = errors.New("empty workspace observation payload")
	}
	return WorkspaceObservationPlan{}, fmt.Errorf("%s returned invalid workspace observation JSON: %w; raw_excerpt=%q", source, lastErr, rawPayloadExcerpt(raw, 500))
}

func decodeMissionValidationPayload(source string, raw []byte) (MissionValidationResult, error) {
	candidates := decisionJSONCandidates(raw)
	var lastErr error
	for _, candidate := range candidates {
		var result MissionValidationResult
		decoder := json.NewDecoder(strings.NewReader(candidate))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&result); err != nil {
			lastErr = err
			continue
		}
		return validateMissionValidationResult(result)
	}
	if lastErr == nil {
		lastErr = errors.New("empty mission validation payload")
	}
	return MissionValidationResult{}, fmt.Errorf("%s returned invalid mission validation JSON: %w; raw_excerpt=%q", source, lastErr, rawPayloadExcerpt(raw, 500))
}

func rawPayloadExcerpt(raw []byte, limit int) string {
	text := strings.TrimSpace(string(raw))
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + "...(truncated)"
}

func validateMissionValidationResult(result MissionValidationResult) (MissionValidationResult, error) {
	result.Status = strings.ToLower(strings.TrimSpace(result.Status))
	switch result.Status {
	case "passed", "blocking":
	case "":
		result.Status = "passed"
	default:
		return MissionValidationResult{}, fmt.Errorf("mission validator returned unsupported status: %s", result.Status)
	}
	result.Summary = strings.TrimSpace(result.Summary)
	blocking := false
	for idx := range result.Findings {
		finding := &result.Findings[idx]
		finding.FindingID = strings.TrimSpace(finding.FindingID)
		if finding.FindingID == "" {
			finding.FindingID = task.NewID("MFIND")
		}
		finding.Category = strings.TrimSpace(strings.ToLower(finding.Category))
		if finding.Category == "" {
			finding.Category = "coverage_gap"
		}
		finding.Severity = strings.TrimSpace(strings.ToLower(finding.Severity))
		if finding.Severity == "" {
			finding.Severity = "medium"
		}
		finding.Summary = strings.TrimSpace(finding.Summary)
		if finding.Summary == "" {
			return MissionValidationResult{}, errors.New("mission validator finding missing summary")
		}
		finding.EvidenceRefs = normalizeStringSlice(finding.EvidenceRefs)
		finding.RecommendedAction = strings.TrimSpace(finding.RecommendedAction)
		if finding.Blocking {
			blocking = true
		}
	}
	if blocking {
		result.Status = "blocking"
	}
	if result.Status == "blocking" && !blocking && len(result.Findings) > 0 {
		result.Findings[0].Blocking = true
		blocking = true
	}
	if result.Status == "blocking" && len(result.Findings) == 0 {
		return MissionValidationResult{}, errors.New("mission validator blocking status requires at least one finding")
	}
	if result.Summary == "" {
		if result.Status == "passed" {
			result.Summary = "Mission validator passed."
		} else {
			result.Summary = fmt.Sprintf("Mission validator blocked mission with %d finding(s).", len(result.Findings))
		}
	}
	return result, nil
}

func validateWorkspaceEditPlan(plan WorkspaceEditPlan) (WorkspaceEditPlan, error) {
	plan.Summary = strings.TrimSpace(plan.Summary)
	if plan.Summary == "" {
		plan.Summary = "Workspace edit plan generated."
	}
	plan.Patch = strings.TrimSpace(plan.Patch)
	for i := range plan.Writes {
		plan.Writes[i].Path = strings.TrimSpace(plan.Writes[i].Path)
		if plan.Writes[i].Path == "" {
			return WorkspaceEditPlan{}, errors.New("workspace edit write missing path")
		}
	}
	for i := range plan.Deletes {
		plan.Deletes[i] = strings.TrimSpace(plan.Deletes[i])
		if plan.Deletes[i] == "" {
			return WorkspaceEditPlan{}, errors.New("workspace edit delete missing path")
		}
	}
	for i := range plan.Commands {
		plan.Commands[i].Phase = strings.TrimSpace(strings.ToLower(plan.Commands[i].Phase))
		if plan.Commands[i].Phase == "" {
			plan.Commands[i].Phase = "post"
		}
		if plan.Commands[i].Phase != "pre" && plan.Commands[i].Phase != "post" {
			return WorkspaceEditPlan{}, fmt.Errorf("workspace edit command phase must be pre or post")
		}
		plan.Commands[i].Reason = strings.TrimSpace(plan.Commands[i].Reason)
		for j := range plan.Commands[i].Argv {
			plan.Commands[i].Argv[j] = strings.TrimSpace(plan.Commands[i].Argv[j])
		}
		plan.Commands[i].Argv = compactStrings(plan.Commands[i].Argv)
		if len(plan.Commands[i].Argv) == 0 {
			return WorkspaceEditPlan{}, errors.New("workspace edit command missing argv")
		}
		if plan.Commands[i].Reason == "" {
			return WorkspaceEditPlan{}, errors.New("workspace edit command missing reason")
		}
	}
	if plan.Patch != "" && (len(plan.Writes) > 0 || len(plan.Deletes) > 0) {
		return WorkspaceEditPlan{}, errors.New("workspace edit plan cannot mix patch with writes/deletes")
	}
	return plan, nil
}

func validateWorkspaceObservationPlan(plan WorkspaceObservationPlan) (WorkspaceObservationPlan, error) {
	plan.Summary = strings.TrimSpace(plan.Summary)
	if plan.Summary == "" {
		plan.Summary = "No additional observation commands requested."
	}
	for i := range plan.Commands {
		plan.Commands[i].Reason = strings.TrimSpace(plan.Commands[i].Reason)
		for j := range plan.Commands[i].Argv {
			plan.Commands[i].Argv[j] = strings.TrimSpace(plan.Commands[i].Argv[j])
		}
		plan.Commands[i].Argv = compactStrings(plan.Commands[i].Argv)
		if len(plan.Commands[i].Argv) == 0 {
			return WorkspaceObservationPlan{}, errors.New("workspace observation command missing argv")
		}
		if plan.Commands[i].Reason == "" {
			return WorkspaceObservationPlan{}, errors.New("workspace observation command missing reason")
		}
	}
	return plan, nil
}

func decisionJSONCandidates(raw []byte) []string {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return nil
	}
	var candidates []string
	candidates = append(candidates, text)
	if trimmed, ok := trimCodeFence(text); ok {
		candidates = append(candidates, trimmed)
	}
	if extracted, ok := extractJSONObject(text); ok {
		candidates = append(candidates, extracted)
	}
	if trimmed, ok := trimCodeFence(text); ok {
		if extracted, ok := extractJSONObject(trimmed); ok {
			candidates = append(candidates, extracted)
		}
	}
	return uniqueNonEmpty(candidates)
}

func trimCodeFence(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "```") {
		return "", false
	}
	lines := strings.Split(text, "\n")
	if len(lines) < 2 {
		return "", false
	}
	if !strings.HasPrefix(strings.TrimSpace(lines[0]), "```") || strings.TrimSpace(lines[len(lines)-1]) != "```" {
		return "", false
	}
	return strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n")), true
}

func extractJSONObject(text string) (string, bool) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return "", false
	}
	return strings.TrimSpace(text[start : end+1]), true
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
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

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

type OpenAIChatCompletionsDriver struct {
	BaseURL      string
	APIKeyEnv    string
	Model        string
	SystemPrompt string
	MaxTokens    int
	Client       *http.Client
}

func NewOpenAIChatCompletionsDriver(cfg task.ProviderConfig) *OpenAIChatCompletionsDriver {
	return &OpenAIChatCompletionsDriver{
		BaseURL:      strings.TrimSpace(cfg.BaseURL),
		APIKeyEnv:    normalizedAPIKeyEnv(cfg.APIKeyEnv),
		Model:        strings.TrimSpace(cfg.Model),
		SystemPrompt: strings.TrimSpace(cfg.SystemPrompt),
		MaxTokens:    decisionMaxOutputTokens(cfg),
		Client: &http.Client{
			Timeout: decisionTimeout(cfg),
		},
	}
}

func (d *OpenAIChatCompletionsDriver) Decide(ctx context.Context, input Input) (Decision, error) {
	if err := validateRemoteConfig(modeOpenAIComp, d.BaseURL, d.Model); err != nil {
		return Decision{}, err
	}
	apiKey, err := readAPIKey(modeOpenAIComp, d.APIKeyEnv)
	if err != nil {
		return Decision{}, err
	}
	body, err := d.requestBody(input)
	if err != nil {
		return Decision{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatCompletionsEndpoint(d.BaseURL), bytes.NewReader(body))
	if err != nil {
		return Decision{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.Client.Do(req)
	if err != nil {
		return Decision{}, fmt.Errorf("openai-comp provider request failed: %w", err)
	}
	defer resp.Body.Close()
	var parsed chatCompletionsResponse
	if err := decodeLimitedJSON("openai-comp provider", resp.Body, &parsed); err != nil {
		return Decision{}, fmt.Errorf("openai-comp provider returned invalid JSON: %w", err)
	}
	if resp.StatusCode >= 400 {
		if parsed.Error != nil && parsed.Error.Message != "" {
			return Decision{}, fmt.Errorf("openai-comp provider returned %s: %s", resp.Status, parsed.Error.Message)
		}
		return Decision{}, fmt.Errorf("openai-comp provider returned %s", resp.Status)
	}
	payload, err := parsed.decisionPayload()
	if err != nil {
		return Decision{}, err
	}
	return decodeDecisionPayload("openai-comp provider", payload)
}

func (d *OpenAIChatCompletionsDriver) requestBody(input Input) ([]byte, error) {
	prompt, err := buildDecisionPrompt(input)
	if err != nil {
		return nil, err
	}
	payload := chatCompletionsRequest{
		Model: d.Model,
		Messages: []chatCompletionsMessage{
			{Role: "system", Content: defaultSystemPrompt(d.SystemPrompt)},
			{Role: "user", Content: prompt},
		},
		MaxTokens: d.MaxTokens,
		Tools: []chatCompletionsTool{
			{
				Type: "function",
				Function: chatCompletionsToolFunction{
					Name:        decisionToolName,
					Description: "Submit exactly one schema-valid NGEN runtime decision. Choose the smallest safe action from task artifacts only; do not execute commands, edit files, or invent hidden state.",
					Parameters:  decisionSchema(),
				},
			},
		},
		ToolChoice: chatCompletionsToolChoice{
			Type: "function",
			Function: chatCompletionsToolChoiceFunction{
				Name: decisionToolName,
			},
		},
	}
	return json.Marshal(payload)
}

type AnthropicMessagesDriver struct {
	BaseURL      string
	APIKeyEnv    string
	Model        string
	SystemPrompt string
	MaxTokens    int
	Client       *http.Client
}

func NewAnthropicMessagesDriver(cfg task.ProviderConfig) *AnthropicMessagesDriver {
	return &AnthropicMessagesDriver{
		BaseURL:      strings.TrimSpace(cfg.BaseURL),
		APIKeyEnv:    normalizedAPIKeyEnv(cfg.APIKeyEnv),
		Model:        strings.TrimSpace(cfg.Model),
		SystemPrompt: strings.TrimSpace(cfg.SystemPrompt),
		MaxTokens:    decisionMaxOutputTokens(cfg),
		Client: &http.Client{
			Timeout: decisionTimeout(cfg),
		},
	}
}

func (d *AnthropicMessagesDriver) Decide(ctx context.Context, input Input) (Decision, error) {
	if err := validateRemoteConfig(modeAnthropic, d.BaseURL, d.Model); err != nil {
		return Decision{}, err
	}
	apiKey, err := readAPIKey(modeAnthropic, d.APIKeyEnv)
	if err != nil {
		return Decision{}, err
	}
	body, err := d.requestBody(input)
	if err != nil {
		return Decision{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicMessagesEndpoint(d.BaseURL), bytes.NewReader(body))
	if err != nil {
		return Decision{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	resp, err := d.Client.Do(req)
	if err != nil {
		return Decision{}, fmt.Errorf("anthropic provider request failed: %w", err)
	}
	defer resp.Body.Close()
	var parsed anthropicMessagesResponse
	if err := decodeLimitedJSON("anthropic provider", resp.Body, &parsed); err != nil {
		return Decision{}, fmt.Errorf("anthropic provider returned invalid JSON: %w", err)
	}
	if resp.StatusCode >= 400 {
		if parsed.Error != nil && parsed.Error.Message != "" {
			return Decision{}, fmt.Errorf("anthropic provider returned %s: %s", resp.Status, parsed.Error.Message)
		}
		return Decision{}, fmt.Errorf("anthropic provider returned %s", resp.Status)
	}
	payload, err := parsed.decisionPayload()
	if err != nil {
		return Decision{}, err
	}
	decision, err := decodeDecisionPayload("anthropic provider", payload)
	if err != nil {
		return Decision{}, err
	}
	decision.TokenUsage = parsed.Usage.TokenUsageString()
	decision.PromptCacheUsage = parsed.Usage.PromptCacheUsageString()
	return decision, nil
}

func (d *AnthropicMessagesDriver) requestBody(input Input) ([]byte, error) {
	prompt, err := buildDecisionPrompt(input)
	if err != nil {
		return nil, err
	}
	payload := anthropicMessagesRequest{
		Model:     d.Model,
		System:    anthropicCacheableSystem(defaultSystemPrompt(d.SystemPrompt)),
		MaxTokens: d.MaxTokens,
		Messages:  []anthropicMessage{anthropicCacheableUserMessage(prompt)},
		Tools: []anthropicTool{
			{
				Name:        decisionToolName,
				Description: "Submit exactly one schema-valid NGEN runtime decision. Choose the smallest safe action from task artifacts only; do not execute commands, edit files, or invent hidden state.",
				InputSchema: decisionSchema(),
			},
		},
		ToolChoice: anthropicToolChoice{
			Type: "tool",
			Name: decisionToolName,
		},
	}
	return json.Marshal(payload)
}

type responsesRequest struct {
	Model           string               `json:"model"`
	Instructions    string               `json:"instructions,omitempty"`
	Input           []responsesInputItem `json:"input"`
	MaxOutputTokens int                  `json:"max_output_tokens,omitempty"`
	Reasoning       *responsesReasoning  `json:"reasoning,omitempty"`
	Text            responsesTextConfig  `json:"text"`
}

type responsesReasoning struct {
	Effort string `json:"effort,omitempty"`
}

func responsesReasoningConfigFromLevel(level string) *responsesReasoning {
	level = strings.TrimSpace(level)
	if level == "" {
		return nil
	}
	return &responsesReasoning{Effort: level}
}

type responsesInputItem struct {
	Type    string                 `json:"type"`
	Role    string                 `json:"role"`
	Content []responsesContentItem `json:"content"`
}

type responsesContentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesTextConfig struct {
	Format responsesTextFormat `json:"format"`
}

type responsesTextFormat struct {
	Name   string         `json:"name"`
	Type   string         `json:"type"`
	Strict bool           `json:"strict"`
	Schema map[string]any `json:"schema"`
}

type responsesResponse struct {
	ID         string                `json:"id,omitempty"`
	Status     string                `json:"status,omitempty"`
	Error      *responsesError       `json:"error,omitempty"`
	OutputText responseText          `json:"output_text,omitempty"`
	Output     []responsesOutputItem `json:"output"`
}

type responsesError struct {
	Message string `json:"message"`
}

func (e *responsesError) UnmarshalJSON(data []byte) error {
	var message string
	if err := json.Unmarshal(data, &message); err == nil {
		e.Message = strings.TrimSpace(message)
		return nil
	}
	var obj struct {
		Message string `json:"message"`
		Error   string `json:"error"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	switch {
	case strings.TrimSpace(obj.Message) != "":
		e.Message = strings.TrimSpace(obj.Message)
	case strings.TrimSpace(obj.Error) != "":
		e.Message = strings.TrimSpace(obj.Error)
	case strings.TrimSpace(obj.Type) != "":
		if obj.Code != nil {
			e.Message = fmt.Sprintf("%s code=%v", strings.TrimSpace(obj.Type), obj.Code)
		} else {
			e.Message = strings.TrimSpace(obj.Type)
		}
	case obj.Code != nil:
		e.Message = fmt.Sprintf("code=%v", obj.Code)
	default:
		e.Message = "provider error without message"
	}
	return nil
}

type responsesOutputItem struct {
	Type    string                   `json:"type"`
	Role    string                   `json:"role,omitempty"`
	Text    responseText             `json:"text,omitempty"`
	Content []responsesOutputContent `json:"content,omitempty"`
}

type responsesOutputContent struct {
	Type  string          `json:"type"`
	Text  responseText    `json:"text,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	JSON  json.RawMessage `json:"json,omitempty"`
	Value json.RawMessage `json:"value,omitempty"`
}

type responseText string

func (t *responseText) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*t = ""
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err == nil {
		*t = responseText(strings.TrimSpace(value))
		return nil
	}
	*t = responseText(trimmed)
	return nil
}

func (r responsesResponse) outputText() string {
	var parts []string
	appendPart := func(value string) {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	appendPart(string(r.OutputText))
	for _, item := range r.Output {
		if isResponsesTextContentType(item.Type) || strings.TrimSpace(item.Type) == "message" {
			appendPart(string(item.Text))
		}
		for _, content := range item.Content {
			if isResponsesTextContentType(content.Type) || strings.TrimSpace(content.Type) == "" {
				appendPart(string(content.Text))
				appendPart(responsesRawJSONText(content.Input))
				appendPart(responsesRawJSONText(content.JSON))
				appendPart(responsesRawJSONText(content.Value))
			}
		}
	}
	return strings.Join(parts, "\n")
}

func isResponsesTextContentType(value string) bool {
	switch strings.TrimSpace(value) {
	case "output_text", "text", "json", "output_json", "input_json":
		return true
	default:
		return false
	}
}

func responsesRawJSONText(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	var value string
	if err := json.Unmarshal(trimmed, &value); err == nil {
		return strings.TrimSpace(value)
	}
	return string(trimmed)
}

type chatCompletionsRequest struct {
	Model      string                    `json:"model"`
	Messages   []chatCompletionsMessage  `json:"messages"`
	MaxTokens  int                       `json:"max_tokens,omitempty"`
	Tools      []chatCompletionsTool     `json:"tools,omitempty"`
	ToolChoice chatCompletionsToolChoice `json:"tool_choice,omitempty"`
}

type chatCompletionsMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionsTool struct {
	Type     string                      `json:"type"`
	Function chatCompletionsToolFunction `json:"function"`
}

type chatCompletionsToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type chatCompletionsToolChoice struct {
	Type     string                            `json:"type,omitempty"`
	Function chatCompletionsToolChoiceFunction `json:"function,omitempty"`
}

type chatCompletionsToolChoiceFunction struct {
	Name string `json:"name,omitempty"`
}

type chatCompletionsResponse struct {
	Error   *responsesError        `json:"error,omitempty"`
	Choices []chatCompletionChoice `json:"choices"`
}

type chatCompletionChoice struct {
	Message chatCompletionResponseMessage `json:"message"`
}

type chatCompletionResponseMessage struct {
	Content   json.RawMessage          `json:"content,omitempty"`
	ToolCalls []chatCompletionToolCall `json:"tool_calls,omitempty"`
}

type chatCompletionToolCall struct {
	Function chatCompletionToolCallFunction `json:"function"`
}

type chatCompletionToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func (r chatCompletionsResponse) decisionPayload() ([]byte, error) {
	return r.toolPayload(decisionToolName, "openai-comp provider")
}

func (r chatCompletionsResponse) toolPayload(toolName, source string) ([]byte, error) {
	for _, choice := range r.Choices {
		for _, call := range choice.Message.ToolCalls {
			if call.Function.Name == toolName && strings.TrimSpace(call.Function.Arguments) != "" {
				return []byte(call.Function.Arguments), nil
			}
		}
		if text := extractChatCompletionText(choice.Message.Content); text != "" {
			return []byte(text), nil
		}
	}
	return nil, fmt.Errorf("%s returned no payload", source)
}

func extractChatCompletionText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var direct string
	if err := json.Unmarshal(raw, &direct); err == nil {
		return strings.TrimSpace(direct)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, block := range blocks {
			if strings.TrimSpace(block.Text) != "" {
				parts = append(parts, strings.TrimSpace(block.Text))
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

type anthropicMessagesRequest struct {
	Model      string               `json:"model"`
	System     []anthropicTextBlock `json:"system,omitempty"`
	MaxTokens  int                  `json:"max_tokens"`
	Messages   []anthropicMessage   `json:"messages"`
	Tools      []anthropicTool      `json:"tools,omitempty"`
	ToolChoice anthropicToolChoice  `json:"tool_choice,omitempty"`
}

type anthropicMessage struct {
	Role    string               `json:"role"`
	Content []anthropicTextBlock `json:"content"`
}

type anthropicTextBlock struct {
	Type         string                 `json:"type"`
	Text         string                 `json:"text"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicCacheControl struct {
	Type string `json:"type"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type anthropicMessagesResponse struct {
	Error   *responsesError         `json:"error,omitempty"`
	Content []anthropicContentBlock `json:"content"`
	Usage   anthropicUsage          `json:"usage,omitempty"`
}

type anthropicUsage struct {
	InputTokens              *int `json:"input_tokens,omitempty"`
	OutputTokens             *int `json:"output_tokens,omitempty"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens,omitempty"`
}

type anthropicContentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

func (r anthropicMessagesResponse) decisionPayload() ([]byte, error) {
	return r.toolPayload(decisionToolName, "anthropic provider")
}

func (r anthropicMessagesResponse) toolPayload(toolName, source string) ([]byte, error) {
	for _, block := range r.Content {
		if block.Type == "tool_use" && block.Name == toolName && len(block.Input) > 0 {
			return block.Input, nil
		}
	}
	var parts []string
	for _, block := range r.Content {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, strings.TrimSpace(block.Text))
		}
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("%s returned no payload", source)
	}
	return []byte(strings.Join(parts, "\n")), nil
}

func (u anthropicUsage) TokenUsageString() string {
	var parts []string
	if u.InputTokens != nil {
		parts = append(parts, fmt.Sprintf("input_tokens=%d", *u.InputTokens))
	}
	if u.OutputTokens != nil {
		parts = append(parts, fmt.Sprintf("output_tokens=%d", *u.OutputTokens))
	}
	if len(parts) == 0 {
		return "unknown"
	}
	return strings.Join(parts, " ")
}

func (u anthropicUsage) PromptCacheUsageString() string {
	var parts []string
	if u.CacheCreationInputTokens != nil {
		parts = append(parts, fmt.Sprintf("cache_creation_input_tokens=%d", *u.CacheCreationInputTokens))
	}
	if u.CacheReadInputTokens != nil {
		parts = append(parts, fmt.Sprintf("cache_read_input_tokens=%d", *u.CacheReadInputTokens))
	}
	if len(parts) == 0 {
		return "unknown"
	}
	return strings.Join(parts, " ")
}
