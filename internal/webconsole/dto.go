package webconsole

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
	BaseURL              string `json:"base_url"`
	Model                string `json:"model"`
	APIKey               string `json:"api_key"`
	GuardrailsMode       string `json:"guardrails_mode"`
	MaxTurnsHard         *int   `json:"max_turns_hard"`
	DisableHardTurnLimit bool   `json:"disable_hard_turn_limit"`
}

type ErrorResponse struct {
	Error  string `json:"error"`
	Code   string `json:"code,omitempty"`
	Detail string `json:"detail,omitempty"`
	Action string `json:"action,omitempty"`
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
