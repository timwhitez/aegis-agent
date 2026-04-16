package session

import "encoding/json"

const (
	StatusRunning       = "running"
	StatusAwaitingInput = "awaiting_input"
	StatusPaused        = "paused"
	StatusCompleted     = "completed"
	StatusFailed        = "failed"

	ModeRun  = "run"
	ModeExec = "exec"

	CompletionPolicyInteractive = "interactive"
	CompletionPolicyAutonomous  = "autonomous"

	SteerStatusPending  = "pending"
	SteerStatusAccepted = "accepted"
	SteerStatusDeferred = "deferred"
	SteerStatusRejected = "rejected"

	QueueStatusQueued    = "queued"
	QueueStatusRunning   = "running"
	QueueStatusCompleted = "completed"
	QueueStatusFailed    = "failed"

	BackgroundNotificationPending  = "pending"
	BackgroundNotificationAccepted = "accepted"
)

type IsolationInfo struct {
	Mode          string `json:"mode"`
	RequestedMode string `json:"requested_mode,omitempty"`
	ParentWorkdir string `json:"parent_workdir,omitempty"`
	Workdir       string `json:"workdir,omitempty"`
	RootDir       string `json:"root_dir,omitempty"`
	GitRepoRoot   string `json:"git_repo_root,omitempty"`
}

type ProviderRetryPolicy struct {
	MaxAttempts    int  `json:"max_attempts,omitempty"`
	BaseDelayMS    int  `json:"base_delay_ms,omitempty"`
	Retry429       bool `json:"retry_429,omitempty"`
	Retry5xx       bool `json:"retry_5xx,omitempty"`
	RetryTransport bool `json:"retry_transport,omitempty"`
}

type ProviderOptions struct {
	Temperature     *float64             `json:"temperature,omitempty"`
	TopP            *float64             `json:"top_p,omitempty"`
	MaxOutputTokens int                  `json:"max_output_tokens,omitempty"`
	ReasoningEffort string               `json:"reasoning_effort,omitempty"`
	TextVerbosity   string               `json:"text_verbosity,omitempty"`
	ThinkingBudget  int                  `json:"thinking_budget,omitempty"`
	IncludeThoughts *bool                `json:"include_thoughts,omitempty"`
	Store           *bool                `json:"store,omitempty"`
	SendMetadata    *bool                `json:"send_metadata,omitempty"`
	RetryPolicy     *ProviderRetryPolicy `json:"retry_policy,omitempty"`
}

type SessionMetadata struct {
	SchemaVersion    int             `json:"schema_version"`
	ID               string          `json:"id"`
	CreatedAt        string          `json:"created_at"`
	Workdir          string          `json:"workdir"`
	RequestedWorkdir string          `json:"requested_workdir,omitempty"`
	Mode             string          `json:"mode"`
	Provider         string          `json:"provider"`
	Model            string          `json:"model"`
	CompletionPolicy string          `json:"completion_policy"`
	ParentSessionID  string          `json:"parent_session_id,omitempty"`
	RootSessionID    string          `json:"root_session_id,omitempty"`
	AgentName        string          `json:"agent_name,omitempty"`
	AgentRole        string          `json:"agent_role,omitempty"`
	QueueJobID       string          `json:"queue_job_id,omitempty"`
	Depth            int             `json:"depth,omitempty"`
	Isolation        *IsolationInfo  `json:"isolation,omitempty"`
	ProviderOptions  ProviderOptions `json:"provider_options,omitempty"`
}

type State struct {
	Status               string   `json:"status"`
	Phase                string   `json:"phase"`
	Turn                 int      `json:"turn"`
	UpdatedAt            string   `json:"updated_at"`
	CurrentTask          string   `json:"current_task,omitempty"`
	LastError            string   `json:"last_error,omitempty"`
	IncompleteReason     string   `json:"incomplete_reason,omitempty"`
	LastAssistantExcerpt string   `json:"last_assistant_excerpt,omitempty"`
	PauseReason          string   `json:"pause_reason,omitempty"`
	PendingSteerCount    int      `json:"pending_steer_count,omitempty"`
	LoadedSkills         []string `json:"loaded_skills,omitempty"`
}

type ToolCall struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Arguments      json.RawMessage `json:"arguments"`
	ProviderCallID string          `json:"provider_call_id,omitempty"`
}

type ToolResult struct {
	ToolCallID    string         `json:"tool_call_id"`
	Name          string         `json:"name"`
	LLMOutput     string         `json:"llm_output"`
	DisplayOutput string         `json:"display_output"`
	IsError       bool           `json:"is_error"`
	Final         bool           `json:"final,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

type Message struct {
	ID          string         `json:"id"`
	Role        string         `json:"role"`
	Text        string         `json:"text,omitempty"`
	ToolCalls   []ToolCall     `json:"tool_calls,omitempty"`
	ToolResults []ToolResult   `json:"tool_results,omitempty"`
	Meta        map[string]any `json:"meta,omitempty"`
	CreatedAt   string         `json:"created_at"`
}

type TodoItem struct {
	Content   string `json:"content" yaml:"content"`
	Status    string `json:"status" yaml:"status"`
	Priority  string `json:"priority,omitempty" yaml:"priority,omitempty"`
	UpdatedAt string `json:"updated_at" yaml:"updated_at"`
}

type Task struct {
	ID          string   `json:"id" yaml:"id"`
	Subject     string   `json:"subject" yaml:"subject"`
	Description string   `json:"description,omitempty" yaml:"description,omitempty"`
	Status      string   `json:"status" yaml:"status"`
	Priority    string   `json:"priority,omitempty" yaml:"priority,omitempty"`
	Owner       string   `json:"owner,omitempty" yaml:"owner,omitempty"`
	BlockedBy   []string `json:"blocked_by,omitempty" yaml:"blocked_by,omitempty"`
	Blocks      []string `json:"blocks,omitempty" yaml:"blocks,omitempty"`
	Labels      []string `json:"labels,omitempty" yaml:"labels,omitempty"`
	Notes       []string `json:"notes,omitempty" yaml:"notes,omitempty"`
	CreatedAt   string   `json:"created_at" yaml:"created_at"`
	UpdatedAt   string   `json:"updated_at" yaml:"updated_at"`
}

type SteerRequest struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at"`
	Source    string `json:"source"`
	Text      string `json:"text"`
	Interrupt bool   `json:"interrupt"`
	Status    string `json:"status"`
}

type BackgroundNotification struct {
	ID               string   `json:"id"`
	CreatedAt        string   `json:"created_at"`
	Source           string   `json:"source"`
	QueueJobID       string   `json:"queue_job_id,omitempty"`
	SessionID        string   `json:"session_id,omitempty"`
	AgentName        string   `json:"agent_name,omitempty"`
	AgentRole        string   `json:"agent_role,omitempty"`
	Status           string   `json:"status"`
	SessionStatus    string   `json:"session_status,omitempty"`
	RequestedWorkdir string   `json:"requested_workdir,omitempty"`
	EffectiveWorkdir string   `json:"effective_workdir,omitempty"`
	VisiblePaths     []string `json:"visible_paths,omitempty"`
	FinalText        string   `json:"final_text,omitempty"`
	LastError        string   `json:"last_error,omitempty"`
	DeliveryStatus   string   `json:"delivery_status"`
}

type SessionSummary struct {
	ID              string `json:"id"`
	Status          string `json:"status"`
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
	Phase           string `json:"phase"`
	LastError       string `json:"last_error,omitempty"`
	Workdir         string `json:"workdir"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
	RootSessionID   string `json:"root_session_id,omitempty"`
	AgentName       string `json:"agent_name,omitempty"`
	AgentRole       string `json:"agent_role,omitempty"`
	Depth           int    `json:"depth,omitempty"`
	QueueJobID      string `json:"queue_job_id,omitempty"`
}

type TaskBoard struct {
	Todo     []TodoItem        `json:"todo"`
	Tasks    []Task            `json:"tasks"`
	Counters map[string]int    `json:"counters"`
	Groups   map[string][]Task `json:"groups,omitempty"`
}

type QueueJob struct {
	SchemaVersion    int      `json:"schema_version"`
	ID               string   `json:"id"`
	CreatedAt        string   `json:"created_at"`
	UpdatedAt        string   `json:"updated_at"`
	Status           string   `json:"status"`
	ParentSessionID  string   `json:"parent_session_id,omitempty"`
	RootSessionID    string   `json:"root_session_id,omitempty"`
	AgentName        string   `json:"agent_name,omitempty"`
	AgentRole        string   `json:"agent_role,omitempty"`
	Prompt           string   `json:"prompt"`
	Mode             string   `json:"mode"`
	Provider         string   `json:"provider,omitempty"`
	Model            string   `json:"model,omitempty"`
	RequestedWorkdir string   `json:"requested_workdir,omitempty"`
	EffectiveWorkdir string   `json:"effective_workdir,omitempty"`
	VisiblePaths     []string `json:"visible_paths,omitempty"`
	SessionID        string   `json:"session_id,omitempty"`
	SessionStatus    string   `json:"session_status,omitempty"`
	SystemOverride   string   `json:"system_override,omitempty"`
	Background       bool     `json:"background"`
	IsolationMode    string   `json:"isolation_mode,omitempty"`
	IsolationRoot    string   `json:"isolation_root,omitempty"`
	LastError        string   `json:"last_error,omitempty"`
	FinalText        string   `json:"final_text,omitempty"`
}
