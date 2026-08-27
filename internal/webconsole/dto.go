package webconsole

import "aegis-agent/internal/session"

const (
	errorCodeUnknownProvider            = "UNKNOWN_PROVIDER"
	errorCodeProviderProbeFailed        = "PROVIDER_PROBE_FAILED"
	errorCodeActiveHandleNotOwned       = "ACTIVE_HANDLE_NOT_OWNED"
	errorCodeSessionNotResumable        = "SESSION_NOT_RESUMABLE"
	errorCodeWebSocketControlDeprecated = "WEBSOCKET_CONTROL_DEPRECATED"
)

type StartSessionRequest struct {
	Prompt         string                `json:"prompt"`
	AgentName      string                `json:"agent_name"`
	AgentRole      string                `json:"agent_role"`
	Provider       string                `json:"provider"`
	Model          string                `json:"model"`
	Workdir        string                `json:"workdir"`
	Mode           string                `json:"mode"`
	SystemOverride string                `json:"system"`
	IsolationMode  string                `json:"isolation_mode"`
	IsolationRoot  string                `json:"isolation_root"`
	Goal           *GoalDraftRequest     `json:"goal,omitempty"`
	PlanMode       *PlanModeDraftRequest `json:"plan_mode,omitempty"`
}

type GoalDraftRequest struct {
	Enabled                   bool     `json:"enabled"`
	Mode                      string   `json:"mode"`
	Objective                 string   `json:"objective"`
	SuccessCriteria           []string `json:"success_criteria,omitempty"`
	ValidationPlan            []string `json:"validation_plan,omitempty"`
	TokenBudget               *int64   `json:"token_budget,omitempty"`
	ProviderTimeBudgetMinutes *int64   `json:"provider_time_budget_minutes,omitempty"`
	TimeBudgetMinutes         *int64   `json:"time_budget_minutes,omitempty"`
	Autonomy                  string   `json:"autonomy,omitempty"`
	RequirePlanApproval       bool     `json:"require_plan_approval,omitempty"`
	StopOnBudget              bool     `json:"stop_on_budget,omitempty"`
	CreateTasksFromPlan       bool     `json:"create_tasks_from_plan,omitempty"`
	Features                  []string `json:"features,omitempty"`
	Milestones                []string `json:"milestones,omitempty"`
	AskBeforeLargeChanges     bool     `json:"ask_before_large_changes,omitempty"`
	AskBeforeDependencyChange bool     `json:"ask_before_dependency_change,omitempty"`
}

type PlanModeDraftRequest struct {
	Enabled   bool   `json:"enabled"`
	Objective string `json:"objective,omitempty"`
}

type PlanModeReviseRequest struct {
	Message string `json:"message"`
}

type PlanModeInputRequest struct {
	RequestID string                        `json:"request_id,omitempty"`
	Answers   []session.PlanModeInputAnswer `json:"answers"`
}

type GoalPatchRequest struct {
	SuccessCriteria []session.GoalCriterion  `json:"success_criteria,omitempty"`
	ValidationPlan  []session.GoalValidation `json:"validation_plan,omitempty"`
	Control         *session.GoalControl     `json:"control,omitempty"`
	Mission         *session.MissionPlan     `json:"mission,omitempty"`
}

type MissionPlanPatchRequest struct {
	Requirements        []session.MissionRequirement `json:"requirements,omitempty"`
	Features            []session.MissionFeature     `json:"features,omitempty"`
	Milestones          []session.MissionMilestone   `json:"milestones,omitempty"`
	ValidationContract  []session.GoalValidation     `json:"validation_contract,omitempty"`
	RolePlan            []session.MissionRole        `json:"role_plan,omitempty"`
	SharedArtifacts     []string                     `json:"shared_artifacts,omitempty"`
	KnowledgeArtifacts  []string                     `json:"knowledge_artifacts,omitempty"`
	PlanStatus          string                       `json:"plan_status,omitempty"`
	CreateTasksFromPlan *bool                        `json:"create_tasks_from_plan,omitempty"`
}

type MissionPlanApproveRequest struct {
	OverrideCoverage bool `json:"override_coverage,omitempty"`
}

type ContinueSessionRequest struct {
	Message        string                `json:"message"`
	Provider       string                `json:"provider"`
	Model          string                `json:"model"`
	SystemOverride string                `json:"system"`
	PlanMode       *PlanModeDraftRequest `json:"plan_mode,omitempty"`
}

type SteerSessionRequest struct {
	Message   string `json:"message"`
	Interrupt bool   `json:"interrupt"`
}

type QueueJobRequest struct {
	Prompt          string `json:"prompt"`
	ParentSessionID string `json:"parent_session_id"`
	AgentName       string `json:"agent_name"`
	AgentRole       string `json:"agent_role"`
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	Workdir         string `json:"workdir"`
	SystemOverride  string `json:"system"`
	Mode            string `json:"mode"`
	WaitMode        string `json:"wait_mode"`
	IsolationMode   string `json:"isolation_mode"`
	IsolationRoot   string `json:"isolation_root"`
}

type UpdateConfigRequest struct {
	Provider             string                                 `json:"provider"`
	APIProvider          *string                                `json:"api_provider"`
	BaseURL              *string                                `json:"base_url"`
	Model                *string                                `json:"model"`
	ContextWindowTokens  *int                                   `json:"context_window_tokens"`
	APIKey               *string                                `json:"api_key"`
	ReasoningMode        *string                                `json:"reasoning_mode"`
	ReasoningSummary     *string                                `json:"reasoning_summary"`
	GuardrailsMode       string                                 `json:"guardrails_mode"`
	MaxTurnsSoft         *int                                   `json:"max_turns_soft"`
	MaxTurnsHard         *int                                   `json:"max_turns_hard"`
	DisableHardTurnLimit bool                                   `json:"disable_hard_turn_limit"`
	LegacyUIEnabled      *bool                                  `json:"legacy_ui_enabled"`
	ChildBudget          *ChildBudgetSettingsRequest            `json:"child_budget"`
	RoleProviders        map[string]RoleProviderOverrideRequest `json:"role_providers"`
}

type ChildBudgetSettingsRequest struct {
	Disabled                  bool `json:"disabled"`
	MaxActiveRuntimeSec       int  `json:"max_active_runtime_sec"`
	MaxElapsedSec             int  `json:"max_elapsed_sec"`
	MaxTurnsPerAttempt        int  `json:"max_turns_per_attempt"`
	ActiveRuntimeCheckpointMS *int `json:"active_runtime_checkpoint_ms"`
	MaxWallClockSec           int  `json:"max_wall_clock_sec"`
	MaxTurns                  int  `json:"max_turns"`
}

type TestConfigRequest struct {
	Provider            string  `json:"provider"`
	APIProvider         *string `json:"api_provider"`
	BaseURL             *string `json:"base_url"`
	Model               *string `json:"model"`
	ContextWindowTokens *int    `json:"context_window_tokens"`
	APIKey              *string `json:"api_key"`
	ReasoningMode       *string `json:"reasoning_mode"`
	ReasoningSummary    *string `json:"reasoning_summary"`
}

type RoleProviderOverrideRequest struct {
	Provider        string `json:"provider"`
	APIProvider     string `json:"api_provider"`
	BaseURL         string `json:"base_url"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
	MaxOutputTokens int    `json:"max_output_tokens"`
}

type TestConfigResponse struct {
	Success                      bool   `json:"success"`
	Provider                     string `json:"provider"`
	APIProvider                  string `json:"api_provider,omitempty"`
	EffectiveAPIProvider         string `json:"effective_api_provider,omitempty"`
	Model                        string `json:"model"`
	ContextWindowTokens          int    `json:"context_window_tokens,omitempty"`
	EffectiveContextWindowTokens int    `json:"effective_context_window_tokens,omitempty"`
	ReasoningMode                string `json:"reasoning_mode"`
	ReasoningSummary             string `json:"reasoning_summary,omitempty"`
	StopReason                   string `json:"stop_reason,omitempty"`
	FinishMessage                string `json:"finish_message,omitempty"`
	ReasoningEffort              string `json:"reasoning_effort,omitempty"`
	ReasoningSummaryObserved     bool   `json:"reasoning_summary_observed,omitempty"`
	ReasoningEncryptedObserved   bool   `json:"reasoning_encrypted_observed,omitempty"`
	ReasoningTokens              int    `json:"reasoning_tokens,omitempty"`
	ThinkingBudget               int    `json:"thinking_budget,omitempty"`
	ThinkingVisibleObserved      bool   `json:"thinking_visible_observed,omitempty"`
	ThinkingReplayObserved       bool   `json:"thinking_replay_observed,omitempty"`
	ThinkingDetail               string `json:"thinking_detail,omitempty"`
	ThinkingStrategy             string `json:"thinking_strategy,omitempty"`
	MaxOutputTokens              int    `json:"max_output_tokens,omitempty"`
	IncludeThoughts              *bool  `json:"include_thoughts,omitempty"`
}

type ErrorResponse struct {
	Error  string `json:"error"`
	Code   string `json:"code,omitempty"`
	Detail string `json:"detail,omitempty"`
	Action string `json:"action,omitempty"`
}

type MessagesResponse struct {
	Messages []session.Message `json:"messages"`
	HasMore  bool              `json:"has_more"`
}

type webError struct {
	code    string
	message string
	detail  string
	action  string
}

func (e webError) Error() string {
	return e.message
}

func newWebError(code, message, detail, action string) error {
	return webError{
		code:    code,
		message: message,
		detail:  detail,
		action:  action,
	}
}
