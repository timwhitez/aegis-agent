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
	ModeInit = "init"

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

type ProviderTimeoutPolicy struct {
	TimeoutSec          int `json:"timeout_sec,omitempty"`
	RequestTimeoutSec   int `json:"request_timeout_sec,omitempty"`
	StreamIdleTimeoutMS int `json:"stream_idle_timeout_ms,omitempty"`
}

type ProviderOptions struct {
	Temperature     *float64               `json:"temperature,omitempty"`
	TopP            *float64               `json:"top_p,omitempty"`
	MaxOutputTokens int                    `json:"max_output_tokens,omitempty"`
	ReasoningEffort string                 `json:"reasoning_effort,omitempty"`
	TextVerbosity   string                 `json:"text_verbosity,omitempty"`
	ThinkingBudget  int                    `json:"thinking_budget,omitempty"`
	IncludeThoughts *bool                  `json:"include_thoughts,omitempty"`
	Store           *bool                  `json:"store,omitempty"`
	SendMetadata    *bool                  `json:"send_metadata,omitempty"`
	RawSidecar      *bool                  `json:"raw_sidecar,omitempty"`
	RetryPolicy     *ProviderRetryPolicy   `json:"retry_policy,omitempty"`
	TimeoutPolicy   *ProviderTimeoutPolicy `json:"timeout_policy,omitempty"`
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

type ArtifactSnapshot struct {
	Exists bool   `json:"exists"`
	Size   int64  `json:"size,omitempty"`
	MTime  string `json:"mtime,omitempty"`
	Hash   string `json:"hash,omitempty"`
}

type ArtifactStatus struct {
	Present             bool     `json:"present"`
	TouchedBySession    bool     `json:"touched_by_session"`
	ChangedFromBaseline bool     `json:"changed_from_baseline"`
	LastWriteTurn       int      `json:"last_write_turn,omitempty"`
	LastWriterTool      string   `json:"last_writer_tool,omitempty"`
	ValidationStatus    string   `json:"validation_status,omitempty"`
	ValidationIssues    []string `json:"validation_issues,omitempty"`
	UpdatedAt           string   `json:"updated_at,omitempty"`
}

type RequiredArtifact struct {
	Path             string           `json:"path"`
	DisplayPath      string           `json:"display_path,omitempty"`
	Required         bool             `json:"required"`
	Baseline         ArtifactSnapshot `json:"baseline"`
	Status           ArtifactStatus   `json:"status"`
	ContentValidator string           `json:"content_validator,omitempty"`
}

type SessionContract struct {
	SchemaVersion             int                `json:"schema_version"`
	ContractID                string             `json:"contract_id"`
	Source                    string             `json:"source"`
	TrustSource               string             `json:"trust_source"`
	Profile                   string             `json:"profile"`
	AgentRole                 string             `json:"agent_role,omitempty"`
	RequiredArtifacts         []RequiredArtifact `json:"required_artifacts,omitempty"`
	AllowedTools              []string           `json:"allowed_tools,omitempty"`
	RequiredSkills            []string           `json:"required_skills,omitempty"`
	MaxTurns                  int                `json:"max_turns,omitempty"`
	CompletionGates           []string           `json:"completion_gates,omitempty"`
	ExactTargetAnchors        []string           `json:"exact_target_anchors,omitempty"`
	ExactTemplateRequirements []string           `json:"exact_template_requirements,omitempty"`
	LiteralAnchors            []string           `json:"literal_anchors,omitempty"`
	SupportingDocsFreshness   string             `json:"supporting_docs_freshness,omitempty"`
	TaskboardRequirement      string             `json:"taskboard_requirement,omitempty"`
	ChildQueueRequirement     string             `json:"child_queue_requirement,omitempty"`
	CreatedAt                 string             `json:"created_at"`
	UpdatedAt                 string             `json:"updated_at"`
}

type ProviderAttempt struct {
	Turn                int    `json:"turn,omitempty"`
	Attempt             int    `json:"attempt,omitempty"`
	Provider            string `json:"provider"`
	Model               string `json:"model"`
	RequestTimeoutSec   int    `json:"request_timeout_sec,omitempty"`
	StreamIdleTimeoutMS int    `json:"stream_idle_timeout_ms,omitempty"`
	Outcome             string `json:"outcome"`
	Retryable           bool   `json:"retryable,omitempty"`
	StatusCode          int    `json:"status_code,omitempty"`
	ErrorClass          string `json:"error_class,omitempty"`
	TimeoutKind         string `json:"timeout_kind,omitempty"`
	ResponseCommitted   bool   `json:"response_committed,omitempty"`
	BackoffMS           int64  `json:"backoff_ms,omitempty"`
	ProviderResponseID  string `json:"provider_response_id,omitempty"`
	Error               string `json:"error,omitempty"`
	CreatedAt           string `json:"created_at"`
}

type ProviderRawSidecar struct {
	SchemaVersion      int            `json:"schema_version"`
	Provider           string         `json:"provider"`
	Model              string         `json:"model"`
	Turn               int            `json:"turn"`
	Timestamp          string         `json:"timestamp"`
	ProviderResponseID string         `json:"provider_response_id,omitempty"`
	StopReason         string         `json:"stop_reason,omitempty"`
	SelectedRawItems   map[string]any `json:"selected_raw_items,omitempty"`
}

type LongRunCheckpoint struct {
	SchemaVersion            int                `json:"schema_version"`
	SessionID                string             `json:"session_id"`
	RootSessionID            string             `json:"root_session_id,omitempty"`
	ContractSnapshot         *SessionContract   `json:"contract_snapshot,omitempty"`
	TodoSummary              []TodoItem         `json:"todo_summary,omitempty"`
	TaskSummary              map[string]int     `json:"task_summary,omitempty"`
	RequiredArtifactStatus   []RequiredArtifact `json:"required_artifact_status,omitempty"`
	LatestCompactionArtifact string             `json:"latest_compaction_artifact,omitempty"`
	Provider                 string             `json:"provider"`
	Model                    string             `json:"model"`
	EffectiveProviderOptions ProviderOptions    `json:"effective_provider_options,omitempty"`
	Workdir                  string             `json:"workdir"`
	RequestedWorkdir         string             `json:"requested_workdir,omitempty"`
	Isolation                *IsolationInfo     `json:"isolation,omitempty"`
	ParentWaitState          string             `json:"parent_wait_state,omitempty"`
	UnresolvedChildSessions  []string           `json:"unresolved_child_sessions,omitempty"`
	UnresolvedQueueJobs      []string           `json:"unresolved_queue_jobs,omitempty"`
	BackgroundNotifications  int                `json:"background_notifications,omitempty"`
	RecentOwner              *ProcessOwnerClue  `json:"recent_owner,omitempty"`
	ResumeHints              []string           `json:"resume_hints,omitempty"`
	SourceEventCount         int                `json:"source_event_count,omitempty"`
	SourceMessageCount       int                `json:"source_message_count,omitempty"`
	CreatedAt                string             `json:"created_at"`
}

type ProcessOwnerClue struct {
	Source         string `json:"source,omitempty"`
	HandleState    string `json:"handle_state,omitempty"`
	EventType      string `json:"event_type,omitempty"`
	ProcessStartID string `json:"process_start_id,omitempty"`
	PID            int    `json:"pid,omitempty"`
	StartedAt      string `json:"started_at,omitempty"`
	ReleasedAt     string `json:"released_at,omitempty"`
	LastEventAt    string `json:"last_event_at,omitempty"`
}

type ParentCoordination struct {
	SchemaVersion           int      `json:"schema_version"`
	ParentSessionID         string   `json:"parent_session_id"`
	WaitMode                string   `json:"wait_mode"`
	UnresolvedChildSessions []string `json:"unresolved_child_sessions,omitempty"`
	UnresolvedQueueJobs     []string `json:"unresolved_queue_jobs,omitempty"`
	CompletedChildSessions  []string `json:"completed_child_sessions,omitempty"`
	CompletedQueueJobs      []string `json:"completed_queue_jobs,omitempty"`
	FailedChildSessions     []string `json:"failed_child_sessions,omitempty"`
	FailedQueueJobs         []string `json:"failed_queue_jobs,omitempty"`
	Parked                  bool     `json:"parked,omitempty"`
	UpdatedAt               string   `json:"updated_at"`
}

type State struct {
	Status                   string   `json:"status"`
	Phase                    string   `json:"phase"`
	Turn                     int      `json:"turn"`
	UpdatedAt                string   `json:"updated_at"`
	CurrentTask              string   `json:"current_task,omitempty"`
	LastError                string   `json:"last_error,omitempty"`
	IncompleteReason         string   `json:"incomplete_reason,omitempty"`
	LastAssistantExcerpt     string   `json:"last_assistant_excerpt,omitempty"`
	PauseReason              string   `json:"pause_reason,omitempty"`
	PendingSteerCount        int      `json:"pending_steer_count,omitempty"`
	LoadedSkills             []string `json:"loaded_skills,omitempty"`
	RalphLoopCount           int      `json:"ralph_loop_count,omitempty"`
	ProviderAutoResumeCount  int      `json:"provider_auto_resume_count,omitempty"`
	LastCompactionInputChars int      `json:"last_compaction_input_chars,omitempty"`
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

type Feature struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Steps       []string `json:"steps"`
	Status      string   `json:"status"`
	Passes      int      `json:"passes"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

type FeatureList struct {
	Features []Feature `json:"features"`
}

type QueueJob struct {
	SchemaVersion    int      `json:"schema_version"`
	ID               string   `json:"id"`
	CreatedAt        string   `json:"created_at"`
	UpdatedAt        string   `json:"updated_at"`
	Status           string   `json:"status"`
	ClaimedBy        string   `json:"claimed_by,omitempty"`
	ClaimedAt        string   `json:"claimed_at,omitempty"`
	HeartbeatAt      string   `json:"heartbeat_at,omitempty"`
	WorkerPID        int      `json:"worker_pid,omitempty"`
	ProcessStartID   string   `json:"process_start_id,omitempty"`
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
	WaitMode         string   `json:"wait_mode,omitempty"`
	IsolationMode    string   `json:"isolation_mode,omitempty"`
	IsolationRoot    string   `json:"isolation_root,omitempty"`
	LastError        string   `json:"last_error,omitempty"`
	FinalText        string   `json:"final_text,omitempty"`
}
