package webconsole

import "go-cli-agent/internal/session"

const (
	errorCodeUnknownProvider            = "UNKNOWN_PROVIDER"
	errorCodeActiveHandleNotOwned       = "ACTIVE_HANDLE_NOT_OWNED"
	errorCodeSessionNotResumable        = "SESSION_NOT_RESUMABLE"
	errorCodeWebSocketControlDeprecated = "WEBSOCKET_CONTROL_DEPRECATED"
)

type StartSessionRequest struct {
	Prompt         string `json:"prompt"`
	AgentName      string `json:"agent_name"`
	AgentRole      string `json:"agent_role"`
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	Workdir        string `json:"workdir"`
	Mode           string `json:"mode"`
	SystemOverride string `json:"system"`
	IsolationMode  string `json:"isolation_mode"`
	IsolationRoot  string `json:"isolation_root"`
}

type ContinueSessionRequest struct {
	Message        string `json:"message"`
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	SystemOverride string `json:"system"`
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
	Provider             string `json:"provider"`
	APIProvider          string `json:"api_provider"`
	BaseURL              string `json:"base_url"`
	Model                string `json:"model"`
	APIKey               string `json:"api_key"`
	ReasoningMode        string `json:"reasoning_mode"`
	ReasoningSummary     string `json:"reasoning_summary"`
	GuardrailsMode       string `json:"guardrails_mode"`
	MaxTurnsHard         *int   `json:"max_turns_hard"`
	DisableHardTurnLimit bool   `json:"disable_hard_turn_limit"`
}

type TestConfigResponse struct {
	Success                    bool   `json:"success"`
	Provider                   string `json:"provider"`
	APIProvider                string `json:"api_provider,omitempty"`
	EffectiveAPIProvider       string `json:"effective_api_provider,omitempty"`
	Model                      string `json:"model"`
	ReasoningMode              string `json:"reasoning_mode"`
	ReasoningSummary           string `json:"reasoning_summary,omitempty"`
	StopReason                 string `json:"stop_reason,omitempty"`
	FinishMessage              string `json:"finish_message,omitempty"`
	ReasoningEffort            string `json:"reasoning_effort,omitempty"`
	ReasoningSummaryObserved   bool   `json:"reasoning_summary_observed,omitempty"`
	ReasoningEncryptedObserved bool   `json:"reasoning_encrypted_observed,omitempty"`
	ReasoningTokens            int    `json:"reasoning_tokens,omitempty"`
	ThinkingBudget             int    `json:"thinking_budget,omitempty"`
	ThinkingVisibleObserved    bool   `json:"thinking_visible_observed,omitempty"`
	ThinkingReplayObserved     bool   `json:"thinking_replay_observed,omitempty"`
	ThinkingDetail             string `json:"thinking_detail,omitempty"`
	ThinkingStrategy           string `json:"thinking_strategy,omitempty"`
	MaxOutputTokens            int    `json:"max_output_tokens,omitempty"`
	IncludeThoughts            *bool  `json:"include_thoughts,omitempty"`
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
