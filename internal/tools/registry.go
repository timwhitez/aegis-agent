package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"text/template"
	"text/template/parse"
	"time"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/fileutil"
	"go-cli-agent/internal/procutil"
	"go-cli-agent/internal/session"
	"go-cli-agent/internal/skills"
)

type Definition struct {
	Name            string
	Description     string
	InputSchema     map[string]any
	Execute         func(context.Context, ExecContext, json.RawMessage) (session.ToolResult, error)
	SkipInputCheck  bool
	Ephemeral       bool
	EphemeralWindow int
}

const (
	MetadataFailureClass             = "failure_class"
	MetadataAwaitInput               = "await_input"
	MetadataAwaitInputKind           = "await_input_kind"
	MetadataAwaitInputReason         = "await_input_reason"
	MetadataAwaitInputBlockers       = "await_input_blockers"
	MetadataAwaitInputResume         = "await_input_resume_condition"
	FailureClassHarnessError         = "harness_error"
	FailureClassCommandNonzero       = "command_nonzero_exit"
	FailureClassTimeout              = "command_timeout"
	FailureClassInterrupted          = "interrupted"
	FailureClassSchemaReject         = "schema_reject"
	FailureClassNotFound             = "not_found"
	FailureClassUnsupportedPath      = "unsupported_path_source"
	FailureClassUnsupportedEncoding  = "unsupported_encoding"
	FailureClassInvalidCursor        = "invalid_cursor"
	FailureClassOutputBudgetTooSmall = "output_budget_too_small"
	FailureClassSearchRecordTooLarge = "search_record_exceeds_byte_limit"
	ErrorCodeToolNotAllowedForRole   = "tool_not_allowed_for_role"
	InterruptedToolExecutionMessage  = "[Tool execution was interrupted. This tool call may have partially executed, and any spawned process may still be running. Verify state before re-running side-effecting commands.]"
	TimedOutToolExecutionMessage     = "[Command timed out and was terminated after the timeout window. This is a command/network timeout, not a bug in the command syntax; any spawned process may still be running. Consider an offline approach, a narrower command, or a larger timeout; verify state before re-running side-effecting commands.]"
)

type ExecContext struct {
	SessionID             string
	ToolCallID            string
	Workdir               string
	EphemeralArtifactRoot string
	Store                 *session.Store
	Config                *config.Config
	Catalog               *skills.Catalog
	ToolProfile           string
	Emit                  func(string, map[string]any)
	EmitRequired          func(string, map[string]any) error
	EmitBatchRequired     func([]ToolEvent) error
	PlanInputResponder    PlanInputResponder
}

type ToolEvent struct {
	Type string
	Data map[string]any
}

type PlanInputResponder interface {
	RequestPlanInput(context.Context, string, session.PlanModeInputRequest) ([]session.PlanModeInputAnswer, error)
}

var ErrPlanInputCancelled = errors.New("plan mode input cancelled")

type AgentSpawnRequest struct {
	ParentSessionID string
	Prompt          string `json:"prompt"`
	AgentName       string `json:"agent_name,omitempty"`
	AgentRole       string `json:"agent_role,omitempty"`
	Provider        string `json:"provider,omitempty"`
	Model           string `json:"model,omitempty"`
	Workdir         string `json:"workdir,omitempty"`
	SystemOverride  string `json:"system,omitempty"`
	Mode            string `json:"mode,omitempty"`
	Background      bool   `json:"background,omitempty"`
	WaitMode        string `json:"wait_mode,omitempty"`
	ResumeParent    bool   `json:"resume_parent,omitempty"`
	IsolationMode   string `json:"isolation_mode,omitempty"`
	IsolationRoot   string `json:"isolation_root,omitempty"`
}

type AgentSpawnResult struct {
	SessionID    string   `json:"session_id,omitempty"`
	QueueJobID   string   `json:"queue_job_id,omitempty"`
	Status       string   `json:"status,omitempty"`
	FinalText    string   `json:"final_text,omitempty"`
	LastError    string   `json:"last_error,omitempty"`
	Workdir      string   `json:"workdir,omitempty"`
	VisiblePaths []string `json:"visible_paths,omitempty"`
	AgentRole    string   `json:"agent_role,omitempty"`
	ToolProfile  string   `json:"tool_profile,omitempty"`
}

type AgentStatusRequest struct {
	ParentSessionID string `json:"-"`
	SessionID       string `json:"session_id,omitempty"`
	QueueJobID      string `json:"queue_job_id,omitempty"`
}

type AgentWaitRequest struct {
	ParentSessionID string `json:"-"`
	QueueJobID      string `json:"queue_job_id,omitempty"`
}

type AgentStopRequest struct {
	ParentSessionID string `json:"-"`
	SessionID       string `json:"session_id,omitempty"`
	QueueJobID      string `json:"queue_job_id,omitempty"`
}

type AgentPromptRequest struct {
	ParentSessionID string                   `json:"-"`
	SessionID       string                   `json:"session_id,omitempty"`
	QueueJobID      string                   `json:"queue_job_id,omitempty"`
	Message         string                   `json:"message"`
	Interrupt       *bool                    `json:"interrupt,omitempty"`
	BudgetExtension *session.BudgetExtension `json:"budget_extension,omitempty"`
}

type AgentStopResult struct {
	SessionID  string `json:"session_id,omitempty"`
	QueueJobID string `json:"queue_job_id,omitempty"`
	Status     string `json:"status"`
	Accepted   bool   `json:"accepted"`
	Behavior   string `json:"behavior,omitempty"`
	LastError  string `json:"last_error,omitempty"`
}

type AgentPromptResult struct {
	SessionID       string                   `json:"session_id"`
	QueueJobID      string                   `json:"queue_job_id,omitempty"`
	Accepted        bool                     `json:"accepted"`
	Behavior        string                   `json:"behavior"`
	EffectiveBudget *session.EffectiveBudget `json:"effective_budget,omitempty"`
}

type AgentStatusResult struct {
	SessionID       string                   `json:"session_id,omitempty"`
	QueueJobID      string                   `json:"queue_job_id,omitempty"`
	Status          string                   `json:"status,omitempty"`
	SessionStatus   string                   `json:"session_status,omitempty"`
	FinalText       string                   `json:"final_text,omitempty"`
	StopReason      string                   `json:"stop_reason,omitempty"`
	LastError       string                   `json:"last_error,omitempty"`
	Workdir         string                   `json:"workdir,omitempty"`
	AgentName       string                   `json:"agent_name,omitempty"`
	AgentRole       string                   `json:"agent_role,omitempty"`
	ToolProfile     string                   `json:"tool_profile,omitempty"`
	EffectiveBudget *session.EffectiveBudget `json:"effective_budget,omitempty"`
}

type AgentListResult struct {
	Sessions []session.SessionSummary `json:"sessions"`
	Jobs     []session.QueueJob       `json:"jobs"`
}

const (
	readFileDefaultLimit = 120
	readFileMaxLimit     = 120
)

type ControlPlane interface {
	SpawnAgent(context.Context, AgentSpawnRequest) (AgentSpawnResult, error)
	StopAgent(context.Context, AgentStopRequest) (AgentStopResult, error)
	PromptAgent(context.Context, AgentPromptRequest) (AgentPromptResult, error)
	AgentStatus(context.Context, AgentStatusRequest) (AgentStatusResult, error)
	AgentList(context.Context, string) (AgentListResult, error)
}

type Registry struct {
	defs        map[string]Definition
	order       []string
	control     ControlPlane
	cfg         *config.Config
	toolProfile toolCapabilityProfile
}

type toolCapabilityProfile struct {
	name    string
	allowed map[string]struct{}
}

var explorerReadOnlyToolAllowlist = map[string]struct{}{
	"read_file":  {},
	"grep_files": {},
	"grep":       {},
	"glob":       {},
	"load_skill": {},
	"finish":     {},
}

func resolveToolCapabilityProfile(name string) (toolCapabilityProfile, error) {
	name = strings.TrimSpace(name)
	switch name {
	case "", session.ToolProfileDefault:
		return toolCapabilityProfile{name: session.ToolProfileDefault}, nil
	case session.ToolProfileExplorerReadOnly:
		return toolCapabilityProfile{name: session.ToolProfileExplorerReadOnly, allowed: explorerReadOnlyToolAllowlist}, nil
	default:
		return toolCapabilityProfile{}, fmt.Errorf("unsupported tool profile: %s", name)
	}
}

func (p toolCapabilityProfile) allows(name string) bool {
	if p.allowed == nil {
		return true
	}
	_, ok := p.allowed[name]
	return ok
}

var beforeShellCommandStart func(workdir string) error

var reservedNames = map[string]struct{}{
	"shell": {}, "read_file": {}, "read_session_history": {}, "write_file": {}, "edit_file": {}, "glob": {}, "grep": {}, "grep_files": {},
	"finish": {}, "await_input": {}, "load_skill": {}, "get_goal": {}, "create_goal": {}, "record_goal_progress": {}, "update_goal": {}, "todo_write": {}, "todo_read": {}, "task_create": {},
	"task_update": {}, "task_list": {}, "task_get": {}, "agent_spawn": {}, "agent_wait": {}, "agent_stop": {}, "agent_status": {},
	"agent_prompt": {}, "agent_list": {}, "feature_list_create": {}, "feature_list_update": {}, "feature_list_read": {},
	"get_plan_mode": {}, "submit_plan": {}, "request_user_input": {},
}

const providerToolNamePatternText = `^[A-Za-z_][A-Za-z0-9_-]{0,63}$`

var providerToolNamePattern = regexp.MustCompile(providerToolNamePatternText)

func NewRegistry(cfg *config.Config, catalog *skills.Catalog, store *session.Store, control ControlPlane, trustedCommandWorkdir ...string) (*Registry, error) {
	return NewRegistryForToolProfile(cfg, catalog, store, control, session.ToolProfileDefault, trustedCommandWorkdir...)
}

func NewRegistryForToolProfile(cfg *config.Config, catalog *skills.Catalog, store *session.Store, control ControlPlane, toolProfile string, trustedCommandWorkdir ...string) (*Registry, error) {
	if cfg == nil {
		cfg = config.Default()
	}
	profile, err := resolveToolCapabilityProfile(toolProfile)
	if err != nil {
		return nil, err
	}
	registry := &Registry{defs: map[string]Definition{}, control: control, cfg: cfg, toolProfile: profile}
	for _, def := range builtinDefinitions(cfg, catalog, control) {
		registry.Register(def)
	}
	if catalog != nil {
		workdir := ""
		if len(trustedCommandWorkdir) > 0 {
			workdir = trustedCommandWorkdir[0]
		}
		for _, tool := range catalog.TrustedCommandTools(workdir) {
			toolName := strings.TrimSpace(tool.Name)
			if toolName == "" {
				return nil, fmt.Errorf("skill tool name must not be empty: %s", tool.SkillPath)
			}
			if toolName != tool.Name {
				return nil, fmt.Errorf("skill tool name must not contain surrounding whitespace: %q", tool.Name)
			}
			if !providerToolNamePattern.MatchString(toolName) {
				return nil, fmt.Errorf("skill tool name must match provider-compatible pattern %s: %q", providerToolNamePatternText, toolName)
			}
			if _, ok := reservedNames[toolName]; ok {
				return nil, fmt.Errorf("skill tool name is reserved: %s", toolName)
			}
			if _, exists := registry.defs[toolName]; exists {
				return nil, fmt.Errorf("duplicate skill tool name: %s", toolName)
			}
			def := commandToolDefinition(cfg, tool)
			registry.Register(def)
		}
	}
	return registry, nil
}

func (r *Registry) Register(def Definition) {
	if _, exists := r.defs[def.Name]; !exists {
		r.order = append(r.order, def.Name)
	}
	r.defs[def.Name] = def
}

func (r *Registry) Get(name string) *Definition {
	if !r.toolProfile.allows(name) {
		return nil
	}
	if def, ok := r.defs[name]; ok {
		return &def
	}
	return nil
}

func (r *Registry) Definitions() []Definition {
	var out []Definition
	for _, name := range r.order {
		if !r.toolProfile.allows(name) {
			continue
		}
		out = append(out, r.defs[name])
	}
	return out
}

// CapabilityDenial returns the stable, side-effect-free denial for a tool that
// is outside the registry's effective capability profile. Dispatchers call it
// before tool lifecycle hooks; Execute calls it again as the final authority
// for direct, resumed, or forged invocations.
func (r *Registry) CapabilityDenial(name string) (session.ToolResult, bool) {
	if r.toolProfile.allows(name) {
		return session.ToolResult{}, false
	}
	result := errorResult(name, fmt.Errorf("tool %s is not allowed by role capability profile %s", name, r.toolProfile.name))
	setToolResultFailureClass(&result, FailureClassSchemaReject)
	if result.Metadata == nil {
		result.Metadata = make(map[string]any)
	}
	result.Metadata["error_code"] = ErrorCodeToolNotAllowedForRole
	result.Metadata["tool_profile"] = r.toolProfile.name
	return result, true
}

func (r *Registry) Execute(ctx context.Context, name string, execCtx ExecContext, args json.RawMessage) (session.ToolResult, error) {
	if result, denied := r.CapabilityDenial(name); denied {
		return result, nil
	}
	def, ok := r.defs[name]
	if !ok {
		return session.ToolResult{}, fmt.Errorf("unknown tool: %s", name)
	}
	if err := validateToolArgs(def, args); err != nil {
		result := errorResult(name, err)
		setToolResultFailureClass(&result, FailureClassSchemaReject)
		return result, nil
	}
	if execCtx.Config == nil {
		execCtx.Config = r.cfg
	}
	execCtx.ToolProfile = r.toolProfile.name
	return def.Execute(ctx, execCtx, args)
}

func validateToolArgs(def Definition, raw json.RawMessage) error {
	if def.SkipInputCheck || strings.TrimSpace(def.Name) == "" || len(def.InputSchema) == 0 {
		return nil
	}
	if schemaType, _ := def.InputSchema["type"].(string); schemaType != "" && schemaType != "object" {
		return nil
	}
	_, err := decodeClosedToolArgs(raw, def.InputSchema)
	return err
}

func decodeClosedToolArgs(raw json.RawMessage, schema map[string]any) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		trimmed = []byte("{}")
	}
	if trimmed[0] != '{' {
		return nil, errors.New("tool input must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	var args map[string]json.RawMessage
	if err := decoder.Decode(&args); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("tool input must contain a single JSON value")
		}
		return nil, err
	}
	if args == nil {
		args = map[string]json.RawMessage{}
	}
	if err := validateClosedToolObject(schema, args, ""); err != nil {
		return nil, err
	}
	return args, nil
}

func validateClosedToolObject(schema map[string]any, object map[string]json.RawMessage, path string) error {
	properties, _ := schema["properties"].(map[string]any)
	if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
		for key := range object {
			if _, known := properties[key]; !known {
				if isHarnessReplayMarkerField(key) {
					return fmt.Errorf("unexpected context replay marker field %q; compacted historical tool arguments are read-only context, not valid new arguments. Resend the call with real fields from this tool's schema", toolFieldPath(path, key))
				}
				return fmt.Errorf("unexpected field %q", toolFieldPath(path, key))
			}
		}
	}
	for _, key := range schemaRequiredFields(schema) {
		if _, exists := object[key]; !exists {
			return fmt.Errorf("%s is required", toolFieldPath(path, key))
		}
	}
	for key, rawPropertySchema := range properties {
		propertySchema, ok := rawPropertySchema.(map[string]any)
		if !ok {
			continue
		}
		value, exists := object[key]
		if !exists {
			continue
		}
		if err := validateClosedToolValue(propertySchema, value, toolFieldPath(path, key)); err != nil {
			return err
		}
	}
	return nil
}

func validateClosedToolValue(schema map[string]any, raw json.RawMessage, path string) error {
	if len(schema) == 0 {
		return nil
	}
	expectedType, _ := schema["type"].(string)
	switch expectedType {
	case "object":
		object, ok, err := decodeToolObjectValue(raw)
		if err != nil || !ok {
			return err
		}
		return validateClosedToolObject(schema, object, path)
	case "array":
		itemSchema, _ := schema["items"].(map[string]any)
		if len(itemSchema) == 0 {
			return nil
		}
		items, ok, err := decodeToolArrayValue(raw)
		if err != nil || !ok {
			return err
		}
		for i, item := range items {
			if err := validateClosedToolValue(itemSchema, item, toolIndexPath(path, i)); err != nil {
				return err
			}
		}
		return nil
	default:
		return nil
	}
}

func isHarnessReplayMarkerField(field string) bool {
	switch field {
	case "compacted_for_context", "head_tail", "original_chars":
		return true
	default:
		return false
	}
}

func decodeToolObjectValue(raw json.RawMessage) (map[string]json.RawMessage, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, false, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return nil, true, err
	}
	if object == nil {
		object = map[string]json.RawMessage{}
	}
	return object, true, nil
}

func decodeToolArrayValue(raw json.RawMessage) ([]json.RawMessage, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, false, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(trimmed, &items); err != nil {
		return nil, true, err
	}
	return items, true, nil
}

func toolFieldPath(path, field string) string {
	if strings.TrimSpace(path) == "" {
		return field
	}
	return path + "." + field
}

func toolIndexPath(path string, index int) string {
	if strings.TrimSpace(path) == "" {
		return fmt.Sprintf("[%d]", index)
	}
	return fmt.Sprintf("%s[%d]", path, index)
}

func builtinDefinitions(cfg *config.Config, catalog *skills.Catalog, control ControlPlane) []Definition {
	defs := []Definition{
		defShell(),
		defReadFile(),
		defReadSessionHistory(),
		defWriteFile(),
		defEditFile(),
		defGlob(),
		defGrepFiles(),
		defGrep(),
		defFinish(),
		defAwaitInput(),
		defLoadSkill(catalog),
		defGetGoal(),
		defCreateGoal(),
		defRecordGoalProgress(),
		defUpdateGoal(),
		defGetPlanMode(),
		defSubmitPlan(),
		defRequestUserInput(),
		defTodoWrite(),
		defTodoRead(),
		defTaskCreate(),
		defTaskUpdate(),
		defTaskList(),
		defTaskGet(),
		defFeatureListCreate(),
		defFeatureListUpdate(),
		defFeatureListRead(),
	}
	if cfg != nil && cfg.Runtime.MultiAgent.Enabled {
		defs = append(defs,
			defAgentSpawn(control),
			defAgentWait(control),
			defAgentStop(control),
			defAgentPrompt(control),
			defAgentStatus(control),
			defAgentList(control),
		)
	}
	for i := range defs {
		defs[i].InputSchema = closeObjectSchemas(defs[i].InputSchema)
	}
	return defs
}

func closeObjectSchemas(schema map[string]any) map[string]any {
	closed, _ := closeSchemaValue(schema).(map[string]any)
	return closed
}

func closeSchemaValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed)+1)
		for key, child := range typed {
			out[key] = closeSchemaValue(child)
		}
		if schemaType, _ := out["type"].(string); schemaType == "object" {
			if _, exists := out["additionalProperties"]; !exists {
				out["additionalProperties"] = false
			}
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = closeSchemaValue(child)
		}
		return out
	default:
		return value
	}
}

func todoItemSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"content": map[string]any{"type": "string", "description": "Concise task text. Keep it specific enough to track progress without duplicating nearby todos."},
			"status": map[string]any{
				"type":        "string",
				"enum":        []string{"pending", "in_progress", "completed", "cancelled"},
				"description": "Current task state. Multiple independent or parallel items may be in_progress; use completed only after the work is actually done.",
			},
			"priority": map[string]any{
				"type":        "string",
				"enum":        []string{"high", "medium", "low"},
				"description": "Relative importance for this session.",
			},
			"updated_at": map[string]any{"type": "string", "description": "Optional RFC3339 timestamp; omitted values are filled by the runtime."},
		},
		"required":             []string{"content", "status"},
		"additionalProperties": false,
	}
}

func stringArraySchema() map[string]any {
	return map[string]any{
		"type":  "array",
		"items": map[string]any{"type": "string"},
	}
}

func budgetExtensionSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"add_turns": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"description": "Additional provider turns made available to the next child budget attempt.",
			},
			"add_active_runtime_sec": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"description": "Additional active runtime seconds made available to the next attempt; paused/offline time is excluded.",
			},
			"extend_deadline_sec": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"description": "Seconds to add to the durable absolute deadline.",
			},
			"clear_turn_limit":           map[string]any{"type": "boolean"},
			"clear_active_runtime_limit": map[string]any{"type": "boolean"},
			"clear_absolute_deadline":    map[string]any{"type": "boolean"},
			"reason": map[string]any{
				"type":        "string",
				"description": "Short parent rationale recorded in the durable extension event.",
			},
		},
	}
}

func goalItemStatusUpdateArraySchema() map[string]any {
	return map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":          map[string]any{"type": "string", "description": "Existing criterion or validation id from get_goal; ids are system-assigned, not free-form labels."},
				"status":      map[string]any{"type": "string", "description": "Item status such as verified, failed, skipped, blocked, or pending."},
				"evidence":    withDescription(stringArraySchema(), "Concrete evidence refs for this item."),
				"last_run_at": map[string]any{"type": "string", "description": "Optional RFC3339 validation run timestamp."},
			},
			"required":             []string{"id"},
			"additionalProperties": false,
		},
	}
}

func goalProgressCommandArraySchema() map[string]any {
	return withDescription(map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command":   map[string]any{"type": "string", "description": "Command that was run or should be handed off."},
				"exit_code": map[string]any{"type": "integer", "description": "Observed exit code when known."},
				"artifact":  map[string]any{"type": "string", "description": "Output artifact or evidence path for the command."},
				"summary":   map[string]any{"type": "string", "description": "Short command result summary."},
			},
			"additionalProperties": false,
		},
	}, "Command results or handoff commands linked to this progress update.")
}

func missionFeatureUpdateArraySchema() map[string]any {
	return withDescription(map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":                 map[string]any{"type": "string", "description": "Existing mission feature id from get_goal.mission.features; ids are system-assigned, not free-form labels."},
				"status":             map[string]any{"type": "string", "description": "Feature status such as pending, in_progress, completed, blocked, or skipped."},
				"evidence":           withDescription(stringArraySchema(), "Evidence refs for this feature."),
				"claimed_assertions": withDescription(stringArraySchema(), "Existing mission validation contract ids from get_goal.mission.validation_contract. Omit when the mission has no validation contract items."),
				"task_ids":           withDescription(stringArraySchema(), "Durable task ids linked to this feature."),
				"child_session_ids":  withDescription(stringArraySchema(), "Child session ids linked to this feature."),
				"queue_job_ids":      withDescription(stringArraySchema(), "Queue job ids linked to this feature."),
			},
			"required":             []string{"id"},
			"additionalProperties": false,
		},
	}, "Append-friendly updates for mission features by exact ids from get_goal. Omit feature_updates when the current goal has no mission features.")
}

func missionMilestoneUpdateArraySchema() map[string]any {
	return withDescription(map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":                map[string]any{"type": "string", "description": "Existing mission milestone id from get_goal.mission.milestones; ids are system-assigned, not free-form labels."},
				"status":            map[string]any{"type": "string", "description": "Milestone status such as pending, in_progress, completed, blocked, or skipped."},
				"evidence":          withDescription(stringArraySchema(), "Evidence refs for this milestone."),
				"feature_ids":       withDescription(stringArraySchema(), "Existing mission feature ids from get_goal.mission.features."),
				"validation_ids":    withDescription(stringArraySchema(), "Existing mission validation contract ids from get_goal.mission.validation_contract. Omit when the mission has no validation contract items."),
				"task_ids":          withDescription(stringArraySchema(), "Durable task ids linked to this milestone."),
				"child_session_ids": withDescription(stringArraySchema(), "Child session ids linked to this milestone."),
				"queue_job_ids":     withDescription(stringArraySchema(), "Queue job ids linked to this milestone."),
			},
			"required":             []string{"id"},
			"additionalProperties": false,
		},
	}, "Append-friendly updates for mission milestones by exact ids from get_goal. Omit milestone_updates when the current goal has no mission milestones.")
}

func missionValidationUpdateArraySchema() map[string]any {
	return withDescription(map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":                 map[string]any{"type": "string", "description": "Existing validation id from get_goal.validation_plan or get_goal.mission.validation_contract; ids are system-assigned, not free-form labels."},
				"status":             map[string]any{"type": "string", "description": "Validation status such as verified, failed, skipped, blocked, or pending."},
				"evidence":           withDescription(stringArraySchema(), "Evidence refs for this validation item."),
				"last_run_at":        map[string]any{"type": "string", "description": "Optional RFC3339 validation run timestamp."},
				"child_session_ids":  withDescription(stringArraySchema(), "Child session ids that produced validation evidence."),
				"queue_job_ids":      withDescription(stringArraySchema(), "Queue job ids that produced validation evidence."),
				"evaluator_evidence": evaluatorEvidenceArraySchema(),
			},
			"required":             []string{"id"},
			"additionalProperties": false,
		},
	}, "Append-friendly updates for validation plan or mission validation contract items by exact ids from get_goal. Omit validation_updates when the current goal has no validation items.")
}

func evaluatorEvidenceArraySchema() map[string]any {
	return withDescription(map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"role":             map[string]any{"type": "string", "description": "Usually evaluator when evidence came from independent validation."},
				"child_session_id": map[string]any{"type": "string", "description": "Evaluator child session id."},
				"queue_job_id":     map[string]any{"type": "string", "description": "Evaluator queue job id."},
				"artifact":         map[string]any{"type": "string", "description": "Evidence artifact path or id."},
				"summary":          map[string]any{"type": "string", "description": "Short evaluator result summary."},
				"status":           map[string]any{"type": "string", "description": "Evaluator result status."},
				"created_at":       map[string]any{"type": "string", "description": "Optional RFC3339 timestamp."},
			},
			"additionalProperties": false,
		},
	}, "Independent evaluator evidence links for this validation item.")
}

func withDescription(schema map[string]any, description string) map[string]any {
	out := make(map[string]any, len(schema)+1)
	for key, value := range schema {
		out[key] = value
	}
	out["description"] = description
	return out
}

func defShell() Definition {
	return Definition{
		Name:            "shell",
		Description:     "Run non-interactive terminal commands in the workspace for build, test, package, git, and runtime operations. Prefer dedicated tools for file search, reading, writing, and editing instead of shell cat/grep/sed/echo. Use the workdir parameter instead of embedding cd when changing directories; workdir may be workspace-relative or a registered skill directory returned by load_skill. Quote paths with spaces, and inspect exit_code/output before claiming success. When remote data needs repeated analysis, fetch it once into a temporary snapshot and parse that snapshot; refresh only after external state changes or freshness matters. Never combine piped data with a heredoc that also supplies the same interpreter stdin (for example, curl ... | python3 - <<'PY'); use a temporary file, python3 -c, or let the script perform the request.",
		Ephemeral:       true,
		EphemeralWindow: 2,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The non-interactive shell command to execute. Avoid cd for directory changes; use workdir instead.",
				},
				"timeout": map[string]any{
					"type":        "integer",
					"description": "Optional per-command timeout in seconds. Omit to use the configured runtime default.",
				},
				"workdir": map[string]any{
					"type":        "string",
					"description": "Optional directory for command execution. Must resolve inside the workspace or under a registered skill root returned by load_skill.",
				},
			},
			"required": []string{"command"},
		},
		Execute: func(ctx context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			var input struct {
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
				Workdir string `json:"workdir"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("shell", err), nil
			}
			if strings.TrimSpace(input.Command) == "" {
				return errorResult("shell", errors.New("command is required")), nil
			}
			workdir := execCtx.Workdir
			workdirSource := "workspace"
			workdirSkill := ""
			if strings.TrimSpace(input.Workdir) != "" {
				resolvedWorkdir, source, skillName, err := resolveShellWorkdir(execCtx, input.Workdir)
				if err != nil {
					return errorResult("shell", err), nil
				}
				workdir = resolvedWorkdir
				workdirSource = source
				workdirSkill = skillName
			}
			timeout := effectiveToolTimeout(execCtx.Config.Runtime.CommandTimeoutSec, input.Timeout)
			callCtx := ctx
			var cancel context.CancelFunc
			if timeout > 0 {
				callCtx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
				defer cancel()
			}
			command, shellArg := shellCommand()
			shellSandbox := ""
			if execCtx.Config != nil {
				shellSandbox = execCtx.Config.Runtime.Shell.Sandbox
			}
			stableDir, commandDir, err := openStableCommandWorkdir(workdir)
			if err != nil {
				return errorResult("shell", err), nil
			}
			defer func() {
				_ = stableDir.Close()
			}()
			sandboxSource, sandboxExtraFiles := commandWorkdirSandboxSource(stableDir, workdir)
			commandPath, commandArgs, sandboxStatus, sandboxErr := shellSandboxCommand(shellSandbox, workdir, sandboxSource, command, shellArg, input.Command)
			policyMode := effectiveExecPolicyMode(execCtx.Config)
			policyViolations := DetectExecPolicyViolations(input.Command)
			policyMetadata := execPolicyMetadata(policyMode, policyViolations)
			metadata := func(exitCode, rawLength int, truncated bool) map[string]any {
				data := map[string]any{
					"command":        input.Command,
					"exit_code":      exitCode,
					"timeout":        timeout,
					"workdir":        workdir,
					"workdir_source": workdirSource,
					"sandbox":        sandboxStatus,
					"raw_length":     rawLength,
					"truncated":      truncated,
				}
				if workdirSkill != "" {
					data["skill"] = workdirSkill
				}
				return attachExecPolicyMetadata(data, policyMetadata)
			}
			if policyMode == "deny" && len(policyViolations) > 0 {
				text := "Error: shell command denied by exec policy"
				return session.ToolResult{
					Name:          "shell",
					LLMOutput:     text,
					DisplayOutput: text,
					IsError:       true,
					Metadata:      metadata(0, 0, false),
				}, nil
			}
			if sandboxErr != nil {
				text := "Error: " + sandboxErr.Error()
				return session.ToolResult{
					Name:          "shell",
					LLMOutput:     text,
					DisplayOutput: text,
					IsError:       true,
					Metadata:      metadata(0, 0, false),
				}, nil
			}
			cmd := exec.CommandContext(callCtx, commandPath, commandArgs...)
			procutil.PrepareCommandCancellation(cmd)
			cmd.Dir = commandDir
			if sandboxStatus == "bwrap" {
				cmd.ExtraFiles = append(cmd.ExtraFiles, sandboxExtraFiles...)
			}
			cmd.Env = filteredEnv(execCtx.Config.Runtime.ShellEnvAllowlist)
			if beforeShellCommandStart != nil {
				if err := beforeShellCommandStart(workdir); err != nil {
					return errorResult("shell", err), nil
				}
			}
			collector := newCommandOutputCollector(execCtx, "shell")
			cmd.Stdout = collector
			cmd.Stderr = collector
			err = cmd.Run()
			exitCode := 0
			if cmd.ProcessState != nil {
				exitCode = cmd.ProcessState.ExitCode()
			}
			rawLength := collector.rawByteCount()
			truncated := rawLength > effectiveCommandToolOutputPolicy(execCtx.Config).LLMOutputMaxBytes
			summary := commandResultSummary("shell", exitCode, timeout, workdirSource, workdir, sandboxStatus, rawLength, truncated)
			if err != nil {
				// A real interrupt cancels the parent ctx (user steer, pause,
				// session shutdown) and must propagate. A per-command timeout
				// only expires callCtx: it is a recoverable tool error the model
				// can react to, so surface a timeout-specific message + class and
				// return nil error instead of a bare "interrupted".
				if ctx.Err() != nil {
					md := metadata(exitCode, rawLength, truncated)
					md[MetadataFailureClass] = FailureClassInterrupted
					return collector.finalize(commandOutputResultOptions{
						Summary:       summary,
						StatusMessage: InterruptedToolExecutionMessage,
						IsError:       true,
						Metadata:      md,
					}), ctx.Err()
				}
				if callCtx.Err() != nil {
					md := metadata(exitCode, rawLength, truncated)
					md[MetadataFailureClass] = FailureClassTimeout
					return collector.finalize(commandOutputResultOptions{
						Summary:       summary,
						StatusMessage: TimedOutToolExecutionMessage,
						IsError:       true,
						Metadata:      md,
					}), nil
				}
				return collector.finalize(commandOutputResultOptions{
					Summary:  summary,
					IsError:  true,
					Metadata: metadata(exitCode, rawLength, truncated),
				}), nil
			}
			return collector.finalize(commandOutputResultOptions{
				Summary:  summary,
				Metadata: metadata(exitCode, rawLength, truncated),
			}), nil
		},
	}
}

func openStableCommandWorkdir(path string) (*os.File, string, error) {
	dir, err := fileutil.OpenDirNoSymlink(path)
	if err != nil {
		return nil, "", err
	}
	if runtime.GOOS == "linux" {
		return dir, fmt.Sprintf("/proc/self/fd/%d", dir.Fd()), nil
	}
	return dir, path, nil
}

func commandWorkdirSandboxSource(dir *os.File, fallback string) (string, []*os.File) {
	if runtime.GOOS != "linux" || dir == nil {
		return fallback, nil
	}
	return "/proc/self/fd/3", []*os.File{dir}
}

func defReadFile() Definition {
	return Definition{
		Name:        "read_file",
		Description: "Read a known text file as UTF-8; use grep_files or grep first for workspace discovery, then read the owning file slices. The default line mode uses 1-based offset/limit and is capped at 120 lines. For minified files, long records, or exact tool-output artifacts, use the mutually exclusive 0-based byte_offset/byte_limit mode and continue with next_byte_offset; byte_limit is capped at 24 KiB and every page contains complete UTF-8 runes. Paths normally resolve inside the workspace. Registered skill bundle files are also readable by exact skill path such as skills/<skill-name>/references/file.md, by the absolute path returned from load_skill, or by an unambiguous skill-relative link such as references/file.md. A current-session ephemeral tool-output artifact path shown by a prior tool result is readable explicitly, but discovery tools skip those artifacts. This reads files only, not directories, and rejects internal generated artifacts.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Workspace-relative file path to read, a registered skill bundle file path such as skills/<skill-name>/references/file.md, or an explicit session ephemeral artifact path returned by a prior tool result.",
				},
				"offset": map[string]any{
					"type":        "integer",
					"description": "1-based starting line. Omit or use 1 to start at the beginning.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum lines to return. Values above 120 are capped to 120.",
				},
				"byte_offset": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"description": "0-based source byte offset for byte mode. Omit to start at byte 0. Do not combine with offset or limit.",
				},
				"byte_limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": fmt.Sprintf("Positive source-byte window for byte mode. Capped at %d bytes and adjusted to complete UTF-8 runes. Required whenever byte_offset is present; do not combine with line offset or limit.", readFileMaxByteLimit),
				},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
			"oneOf": []any{
				map[string]any{
					"not": map[string]any{"anyOf": []any{
						map[string]any{"required": []string{"byte_offset"}},
						map[string]any{"required": []string{"byte_limit"}},
					}},
				},
				map[string]any{
					"required": []string{"byte_limit"},
					"not": map[string]any{"anyOf": []any{
						map[string]any{"required": []string{"offset"}},
						map[string]any{"required": []string{"limit"}},
					}},
				},
			},
		},
		Execute: func(_ context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			input, err := decodeReadFileToolArguments(raw)
			if err != nil {
				return errorResult("read_file", err), nil
			}
			if err := validateToolPath(input.Path); err != nil {
				return errorResult("read_file", err), nil
			}
			normalized, err := normalizeReadFileToolArguments(input, execCtx.Config)
			if err != nil {
				return schemaRejectResult("read_file", err), nil
			}
			path, displayBase, source, skillName, err := resolveReadFilePath(execCtx, normalized.Path)
			if err != nil {
				return readFileErrorResult(normalized.Path, err), nil
			}
			if source == "workspace" && (isInternalGeneratedArtifactInput(normalized.Path) || isInternalGeneratedArtifactPath(execCtx.Workdir, path)) {
				return errorResult("read_file", errors.New("path is an internal generated artifact; use source files, copied validation evidence, or rerun the command and redirect output to a normal workspace file (for example under reports/)")), nil
			}
			if normalized.Mode == readFileModeByte {
				return executeReadFileByteMode(execCtx, path, displayBase, source, skillName, normalized.RequestedByteOffset, normalized.RequestedByteLimit), nil
			}
			data, _, err := fileutil.ReadRegularFileNoSymlink(path)
			if err != nil {
				return readFileErrorResult(normalized.Path, err), nil
			}
			lines := strings.Split(string(data), "\n")
			offset := normalized.EffectiveLineOffset - 1
			if offset > len(lines) {
				offset = len(lines)
			}
			limit := normalized.EffectiveLineLimit
			end := offset + limit
			if end > len(lines) {
				end = len(lines)
			}
			selected := strings.Join(lines[offset:end], "\n")
			selected = annotateReadWindow(displayBase, path, offset, end, len(lines), normalized.RequestedLineLimit, normalized.LineLimitCapped, selected)
			if len(selected) > toolOutputLLMMaxBytes(execCtx.Config) {
				result := typedToolErrorResult("read_file", FailureClassOutputBudgetTooSmall, FailureClassOutputBudgetTooSmall, fmt.Sprintf("line window is %d bytes and exceeds the model-visible output budget; retry this exact path with byte_offset=0 and a positive byte_limit", len(selected)))
				result.Metadata["path"] = path
				result.Metadata["path_source"] = source
				result.Metadata["total_bytes"] = len(data)
				return result, nil
			}
			metadata := map[string]any{
				"path":        path,
				"offset":      offset,
				"end":         end,
				"path_source": source,
			}
			if skillName != "" {
				metadata["skill"] = skillName
			}
			return session.ToolResult{
				Name:          "read_file",
				LLMOutput:     selected,
				DisplayOutput: selected,
				Metadata:      metadata,
			}, nil
		},
	}
}

func defWriteFile() Definition {
	return Definition{
		Name:        "write_file",
		Description: "Create or overwrite a workspace file with exact content, creating parent directories when needed. Use this for requested artifacts, new tests, configs, or full-file rewrites; prefer edit_file for targeted changes to existing files. The target must stay inside the workspace and pass write-policy checks.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Workspace-relative destination path.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "Complete file content to write.",
				},
			},
			"required": []string{"path", "content"},
		},
		Execute: func(_ context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			var input struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("write_file", err), nil
			}
			if err := validateToolPath(input.Path); err != nil {
				return errorResult("write_file", err), nil
			}
			if err := CheckWorkspaceWriteInputAllowed(execCtx.Workdir, input.Path); err != nil {
				return errorResult("write_file", err), nil
			}
			path, err := ResolveWorkspacePath(execCtx.Workdir, input.Path)
			if err != nil {
				return errorResult("write_file", err), nil
			}
			if err := CheckWorkspaceWriteAllowed(execCtx.Workdir, path); err != nil {
				return errorResult("write_file", err), nil
			}
			if err := writeAtomically(path, []byte(input.Content), 0o600); err != nil {
				return errorResult("write_file", err), nil
			}
			message := fmt.Sprintf("Wrote %d bytes to %s", len(input.Content), relativeOrAbsolute(execCtx.Workdir, path))
			return session.ToolResult{
				Name:          "write_file",
				LLMOutput:     message,
				DisplayOutput: message,
				Metadata: map[string]any{
					"path": path,
				},
			}, nil
		},
	}
}

func defEditFile() Definition {
	return Definition{
		Name:        "edit_file",
		Description: "Replace exact text in an existing workspace file. Use this for surgical edits after reading the relevant file slice; old_text must match the file exactly and should include enough surrounding context to identify the intended occurrence. Prefer this over write_file when only part of an existing file changes.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Workspace-relative path of the existing file to edit.",
				},
				"old_text": map[string]any{
					"type":        "string",
					"description": "Exact text currently present in the file. Preserve indentation and surrounding context.",
				},
				"new_text": map[string]any{
					"type":        "string",
					"description": "Replacement text to write in place of old_text.",
				},
			},
			"required": []string{"path", "old_text", "new_text"},
		},
		Execute: func(_ context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			var input struct {
				Path    string `json:"path"`
				OldText string `json:"old_text"`
				NewText string `json:"new_text"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("edit_file", err), nil
			}
			if err := validateToolPath(input.Path); err != nil {
				return errorResult("edit_file", err), nil
			}
			if input.OldText == "" {
				return errorResult("edit_file", errors.New("old_text is required")), nil
			}
			if err := CheckWorkspaceWriteInputAllowed(execCtx.Workdir, input.Path); err != nil {
				return errorResult("edit_file", err), nil
			}
			path, err := ResolveWorkspacePath(execCtx.Workdir, input.Path)
			if err != nil {
				return errorResult("edit_file", err), nil
			}
			if err := CheckWorkspaceWriteAllowed(execCtx.Workdir, path); err != nil {
				return errorResult("edit_file", err), nil
			}
			content, _, err := fileutil.ReadRegularFileNoSymlink(path)
			if err != nil {
				return errorResult("edit_file", err), nil
			}
			current := string(content)
			if !strings.Contains(current, input.OldText) {
				return errorResult("edit_file", errors.New(editOldTextNotFoundHint(current, input.OldText, relativeOrAbsolute(execCtx.Workdir, path)))), nil
			}
			updated := strings.Replace(current, input.OldText, input.NewText, 1)
			if err := writeAtomically(path, []byte(updated), 0o600); err != nil {
				return errorResult("edit_file", err), nil
			}
			message := fmt.Sprintf("Edited %s", relativeOrAbsolute(execCtx.Workdir, path))
			return session.ToolResult{
				Name:          "edit_file",
				LLMOutput:     message,
				DisplayOutput: message,
				Metadata: map[string]any{
					"path": path,
				},
			}, nil
		},
	}
}

// editOldTextNotFoundHint builds an actionable error for edit_file when the
// exact old_text is absent. It diagnoses the most common causes (whitespace
// drift, text that already moved/changed) and points the model at a concrete
// recovery step instead of a bare "old_text not found".
func editOldTextNotFoundHint(current, oldText, displayPath string) string {
	msg := fmt.Sprintf("old_text not found in %s", displayPath)

	// Whitespace-insensitive match: strong signal that only spacing/indentation
	// differs, so the model should re-read the region and copy exact whitespace.
	if collapseWhitespace(oldText) != "" && strings.Contains(collapseWhitespace(current), collapseWhitespace(oldText)) {
		return msg + ": a whitespace-insensitive match exists, so indentation, tabs-vs-spaces, or trailing whitespace differ. Re-read the region with read_file and copy the exact whitespace, or use a shorter unique anchor."
	}

	// Anchor on the first non-empty line of old_text. If that line exists
	// verbatim, the rest drifted; report where so the model can re-read.
	if anchor := firstNonEmptyLine(oldText); anchor != "" {
		if lineNo := lineNumberOfExact(current, anchor); lineNo > 0 {
			return fmt.Sprintf("%s: its first line matches at line %d but the full block does not, so the surrounding text changed since you last read it. Re-read around line %d with read_file, then retry with the current text or a shorter unique anchor.", msg, lineNo, lineNo)
		}
	}

	return msg + ": no close match found, so the text may already be edited or in a different file. Re-read the file with read_file to get the current content, then retry with a shorter unique anchor."
}

func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// lineNumberOfExact returns the 1-based line number whose trimmed content equals
// anchor, or 0 if none. Trimming keeps the anchor robust to leading indentation.
func lineNumberOfExact(content, anchor string) int {
	for i, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == anchor {
			return i + 1
		}
	}
	return 0
}

func defGlob() Definition {
	return Definition{
		Name:            "glob",
		Description:     "Find workspace paths by glob pattern and return file paths only. Use this when you know the filename shape or extension; use grep_files or grep when you need content-based discovery. Optional path scopes the search to a workspace or registered skill directory, and optional include applies an additional file filter. Session artifacts/tool-outputs paths are not searchable by discovery tools; use read_file with the exact artifact path returned by the producing tool. Generated, cache, and internal artifact directories are skipped. Results use both a count limit and a model-visible byte_limit. When has_more is true, pass the opaque next_cursor back with the same pattern/path/include; limit and byte_limit may change between pages. Cursor pages are current-view best effort, not a transactional snapshot.",
		Ephemeral:       true,
		EphemeralWindow: 3,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Glob pattern such as **/*.go or spec/*.md, evaluated inside the selected path.",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Optional workspace-relative or registered skill directory to search. Session artifacts/tool-outputs paths are unsupported here and must be read with read_file using the exact path. Omit to search the workspace.",
				},
				"include": map[string]any{
					"type":        "string",
					"description": "Optional additional glob filter for returned files, for example **/*.go or spec/*.md.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": fmt.Sprintf("Optional maximum number of matching paths to return. Defaults to %d and is capped at %d.", defaultGrepFilesLimit, maxGrepFilesLimit),
				},
				"byte_limit": map[string]any{
					"type":        "integer",
					"minimum":     minSearchOutputByteLimit,
					"description": fmt.Sprintf("Optional maximum model-visible page bytes, including the continuation footer. Defaults to %d and is capped at %d.", defaultSearchOutputByteLimit, maxSearchOutputByteLimit),
				},
				"cursor": map[string]any{
					"type":        "string",
					"description": "Opaque next_cursor from a prior glob page with the same resolved path, pattern, and include filter.",
				},
			},
			"required": []string{"pattern"},
		},
		Execute: func(_ context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			input, err := decodeSearchToolArguments(raw)
			if err != nil {
				return errorResult("glob", err), nil
			}
			if err := validateGrepPattern(input.Pattern); err != nil {
				return errorResult("glob", err), nil
			}
			if isSessionEphemeralArtifactSearchInput(execCtx, input.Path) {
				return sessionArtifactDiscoveryErrorResult("glob", input.Path), nil
			}
			root, err := resolveGrepRoot(execCtx, input.Path)
			if err != nil {
				return errorResult("glob", err), nil
			}
			if input.Path != "" && root.source == "workspace" && isInternalGeneratedArtifactPath(execCtx.Workdir, root.path) {
				return errorResult("glob", errors.New("path is an internal generated artifact; use source files, copied validation evidence, or rerun the command and redirect output to a normal workspace file (for example under reports/)")), nil
			}
			info, err := os.Lstat(root.path)
			if err != nil {
				return errorResult("glob", fmt.Errorf("path %q does not exist or is not accessible: %w", input.Path, err)), nil
			}
			if !info.IsDir() {
				return errorResult("glob", fmt.Errorf("path %q is not a directory", input.Path)), nil
			}
			normalized, err := normalizeSearchToolArguments("glob", input, execCtx.Config)
			if err != nil {
				return typedToolErrorResult("glob", FailureClassOutputBudgetTooSmall, FailureClassOutputBudgetTooSmall, err.Error()), nil
			}
			limit := normalized.EffectiveLimit
			outputBudget := normalized.OutputBudget
			query := newSearchCursorQuery("glob", root, input.Pattern, input.Include)
			startIndex, err := decodeSearchCursor(input.Cursor, query)
			if err != nil {
				return searchCursorFailureResult("glob", err), nil
			}
			collector := newSearchRecordCollector(startIndex, limit)
			if err := doublestar.GlobWalk(os.DirFS(root.path), input.Pattern, func(path string, d os.DirEntry) error {
				fullPath := filepath.Join(root.path, path)
				displayPath := relativeOrAbsolute(root.displayBase, fullPath)
				if path != "." {
					if err := validateGlobMatchedPath(execCtx, root, path, fullPath); err != nil {
						if d.IsDir() {
							return fs.SkipDir
						}
						return nil
					}
				}
				if d.IsDir() && path != "." && shouldSkipGrepDir(displayPath) {
					return fs.SkipDir
				}
				if !d.IsDir() {
					if root.source == "workspace" && isInternalGeneratedArtifactPath(execCtx.Workdir, fullPath) {
						return nil
					}
					if input.Include != "" && !pathMatchesInclude(root.displayBase, fullPath, input.Include) {
						return nil
					}
					if collector.add(plainSearchPageRecord(displayPath, displayPath, 0)) {
						return errGrepLimitReached
					}
				}
				return nil
			}); err != nil && !errors.Is(err, errGrepLimitReached) {
				return errorResult("glob", err), nil
			}
			page, err := buildSearchPage(searchPageOptions{
				Tool:               "glob",
				Query:              query,
				StartIndex:         startIndex,
				Records:            collector.records,
				ScanComplete:       !collector.stopped,
				RequestedLimit:     normalized.RequestedLimit,
				EffectiveLimit:     limit,
				RequestedByteLimit: outputBudget.Requested,
				EffectiveByteLimit: outputBudget.Effective,
				ByteLimitCapped:    outputBudget.Capped,
			})
			if err != nil {
				return searchPageFailureResult("glob", outputBudget, err), nil
			}
			page.Metadata["path_source"] = root.source
			if root.skillName != "" {
				page.Metadata["skill"] = root.skillName
			}
			return session.ToolResult{Name: "glob", LLMOutput: page.Output, DisplayOutput: page.Output, Metadata: page.Metadata}, nil
		},
	}
}

func validateGlobMatchedPath(execCtx ExecContext, root resolvedSearchRoot, path, fullPath string) error {
	switch root.source {
	case "workspace":
		_, err := ResolveWorkspacePath(execCtx.Workdir, relativeOrAbsolute(execCtx.Workdir, fullPath))
		return err
	case "skill":
		_, err := resolvePathUnderRoot(root.path, path)
		return err
	default:
		return nil
	}
}

func defGrep() Definition {
	return Definition{
		Name:        "grep",
		Description: "Search workspace UTF-8 text recursively and return matching lines as path:line:text. Registered skill bundle files are also searchable by exact skill path such as skills/<skill-name>/references/file.md, by the absolute path returned from load_skill, or by an unambiguous skill-relative link such as references/file.md. Use this when exact snippets or line numbers matter; use grep_files first when you only need candidate file paths. Session artifacts/tool-outputs paths are not searchable by discovery tools; use read_file with the exact artifact path returned by the producing tool. The path parameter is a single file or directory; use include for a file filter. Results stop on either match limit or total byte_limit. When has_more is true, continue with the opaque next_cursor and the same pattern/path/include; page sizes may change. Long snippets expose source and match byte spans for read_file byte mode. Cursor pages are current-view best effort.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Regex pattern if it compiles, otherwise literal substring to search for.",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Optional single workspace-relative file or directory to search. Registered skill bundle paths such as skills/<skill-name>/references/file.md are also accepted. Session artifacts/tool-outputs paths are unsupported here and must be read with read_file using the exact path. Does not accept |, multiple paths, or glob syntax; call repeatedly for multiple exact paths or use include for a file filter. Omit to search the workspace.",
				},
				"include": map[string]any{
					"type":        "string",
					"description": "Optional glob filter for searched files, for example **/*.go or spec/*.md.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": fmt.Sprintf("Optional maximum matching lines to return. Defaults to %d and is capped at %d.", defaultGrepMatchesLimit, maxGrepMatches),
				},
				"byte_limit": map[string]any{
					"type":        "integer",
					"minimum":     minSearchOutputByteLimit,
					"description": fmt.Sprintf("Optional maximum model-visible page bytes, including complete records and continuation footer. Defaults to %d and is capped at %d.", defaultSearchOutputByteLimit, maxSearchOutputByteLimit),
				},
				"cursor": map[string]any{
					"type":        "string",
					"description": "Opaque next_cursor from a prior grep page with the same resolved path, pattern, and include filter.",
				},
			},
			"required": []string{"pattern"},
		},
		Execute: func(_ context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			input, err := decodeSearchToolArguments(raw)
			if err != nil {
				return errorResult("grep", err), nil
			}
			if err := validateGrepPattern(input.Pattern); err != nil {
				return errorResult("grep", err), nil
			}
			if isSessionEphemeralArtifactSearchInput(execCtx, input.Path) {
				return sessionArtifactDiscoveryErrorResult("grep", input.Path), nil
			}
			root, err := resolveGrepRoot(execCtx, input.Path)
			if err != nil {
				return errorResult("grep", err), nil
			}
			if input.Path != "" && root.source == "workspace" {
				if isInternalGeneratedArtifactInput(input.Path) {
					return errorResult("grep", errors.New("path is an internal generated artifact; use source files, copied validation evidence, or rerun the command and redirect output to a normal workspace file (for example under reports/)")), nil
				}
			}
			if input.Path != "" && root.source == "workspace" && isInternalGeneratedArtifactPath(execCtx.Workdir, root.path) {
				return errorResult("grep", errors.New("path is an internal generated artifact; use source files, copied validation evidence, or rerun the command and redirect output to a normal workspace file (for example under reports/)")), nil
			}
			normalized, err := normalizeSearchToolArguments("grep", input, execCtx.Config)
			if err != nil {
				return typedToolErrorResult("grep", FailureClassOutputBudgetTooSmall, FailureClassOutputBudgetTooSmall, err.Error()), nil
			}
			limit := normalized.EffectiveLimit
			outputBudget := normalized.OutputBudget
			query := newSearchCursorQuery("grep", root, input.Pattern, input.Include)
			startIndex, err := decodeSearchCursor(input.Cursor, query)
			if err != nil {
				return searchCursorFailureResult("grep", err), nil
			}
			matcher, useRegex := compileGrepMatcher(input.Pattern)
			collector := newSearchRecordCollector(startIndex, limit)
			walkErr := filepath.Walk(root.path, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					if sameCleanPath(path, root.path) {
						return err
					}
					return nil
				}
				if info.IsDir() {
					if path != root.path && shouldSkipGrepDir(path) {
						return filepath.SkipDir
					}
					return nil
				}
				if !info.Mode().IsRegular() {
					return nil
				}
				if input.Include != "" && !pathMatchesInclude(root.displayBase, path, input.Include) {
					return nil
				}
				data, _, readErr := fileutil.ReadRegularFileNoSymlink(path)
				if readErr != nil {
					if sameCleanPath(path, root.path) {
						return readErr
					}
					return nil
				}
				if shouldSkipGrepBinary(data) {
					return nil
				}
				lineStart := 0
				lineNo := 1
				for {
					relativeEnd := bytes.IndexByte(data[lineStart:], '\n')
					lineEnd := len(data)
					if relativeEnd >= 0 {
						lineEnd = lineStart + relativeEnd
					}
					lineBytes := data[lineStart:lineEnd]
					line := string(lineBytes)
					matchStart, matchEnd, matched := firstGrepMatchSpan(line, matcher, useRegex, input.Pattern)
					if matched {
						displayPath := relativeOrAbsolute(root.displayBase, path)
						rawSnippet := strings.TrimSpace(line)
						preferredSnippet, snippetTruncated := truncateGrepMatchedText(rawSnippet, maxGrepLineOutputBytes)
						globalLineStart := int64(lineStart)
						globalLineEnd := int64(lineEnd)
						globalMatchStart := globalLineStart + int64(matchStart)
						globalMatchEnd := globalLineStart + int64(matchEnd)
						spanSuffix := fmt.Sprintf(" [source_bytes=%d-%d match_bytes=%d-%d]", globalLineStart, globalLineEnd, globalMatchStart, globalMatchEnd)
						recordMetadata := map[string]any{
							"path":              displayPath,
							"line":              lineNo,
							"line_start_byte":   globalLineStart,
							"line_end_byte":     globalLineEnd,
							"match_start_byte":  globalMatchStart,
							"match_end_byte":    globalMatchEnd,
							"snippet_truncated": snippetTruncated,
						}
						record := grepSearchPageRecord(
							fmt.Sprintf("%s:%d:", displayPath, lineNo),
							rawSnippet,
							preferredSnippet,
							spanSuffix,
							displayPath,
							lineNo,
							snippetTruncated,
							recordMetadata,
						)
						if collector.add(record) {
							return errGrepLimitReached
						}
					}
					if relativeEnd < 0 {
						break
					}
					lineStart = lineEnd + 1
					lineNo++
				}
				return nil
			})
			if walkErr != nil && !errors.Is(walkErr, errGrepLimitReached) {
				return errorResult("grep", walkErr), nil
			}
			page, err := buildSearchPage(searchPageOptions{
				Tool:               "grep",
				Query:              query,
				StartIndex:         startIndex,
				Records:            collector.records,
				ScanComplete:       !collector.stopped,
				RequestedLimit:     normalized.RequestedLimit,
				EffectiveLimit:     limit,
				RequestedByteLimit: outputBudget.Requested,
				EffectiveByteLimit: outputBudget.Effective,
				ByteLimitCapped:    outputBudget.Capped,
			})
			if err != nil {
				return searchPageFailureResult("grep", outputBudget, err), nil
			}
			page.Metadata["truncated"] = page.TruncatedSnippetCount > 0
			page.Metadata["truncated_matching_lines"] = page.TruncatedSnippetCount
			page.Metadata["match_records"] = page.RenderedRecordMetadata
			page.Metadata["path_source"] = root.source
			if root.skillName != "" {
				page.Metadata["skill"] = root.skillName
			}
			return session.ToolResult{Name: "grep", LLMOutput: page.Output, DisplayOutput: page.Output, Metadata: page.Metadata}, nil
		},
	}
}

func defGrepFiles() Definition {
	return Definition{
		Name:            "grep_files",
		Description:     "Search workspace UTF-8 text recursively and return only files that contain the pattern. Registered skill bundle files are also searchable by exact skill path such as skills/<skill-name>/references/file.md, by the absolute path returned from load_skill, or by an unambiguous skill-relative link such as references/file.md. Use this as the default discovery step before read_file. Session artifacts/tool-outputs paths are not searchable by discovery tools; use read_file with the exact artifact path returned by the producing tool. The path parameter is a single file or directory; use include for a file filter. Results use both a path count limit and a model-visible byte_limit. When has_more is true, continue with the opaque next_cursor and the same pattern/path/include; page sizes may change. Cursor pages are current-view best effort.",
		Ephemeral:       true,
		EphemeralWindow: 3,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Regex pattern if it compiles, otherwise literal substring to search for.",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Optional single workspace-relative file or directory to search. Registered skill bundle paths such as skills/<skill-name>/references/file.md are also accepted. Session artifacts/tool-outputs paths are unsupported here and must be read with read_file using the exact path. Does not accept |, multiple paths, or glob syntax; call repeatedly for multiple exact paths or use include for a file filter. Omit to search the workspace.",
				},
				"include": map[string]any{
					"type":        "string",
					"description": "Optional glob filter for candidate files, for example **/*.go or spec/*.md.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of matching file paths to return. Defaults to 100 and is capped at 200.",
				},
				"byte_limit": map[string]any{
					"type":        "integer",
					"minimum":     minSearchOutputByteLimit,
					"description": fmt.Sprintf("Optional maximum model-visible page bytes, including the continuation footer. Defaults to %d and is capped at %d.", defaultSearchOutputByteLimit, maxSearchOutputByteLimit),
				},
				"cursor": map[string]any{
					"type":        "string",
					"description": "Opaque next_cursor from a prior grep_files page with the same resolved path, pattern, and include filter.",
				},
			},
			"required": []string{"pattern"},
		},
		Execute: func(_ context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			input, err := decodeSearchToolArguments(raw)
			if err != nil {
				return errorResult("grep_files", err), nil
			}
			if err := validateGrepPattern(input.Pattern); err != nil {
				return errorResult("grep_files", err), nil
			}
			if isSessionEphemeralArtifactSearchInput(execCtx, input.Path) {
				return sessionArtifactDiscoveryErrorResult("grep_files", input.Path), nil
			}
			root, err := resolveGrepRoot(execCtx, input.Path)
			if err != nil {
				return errorResult("grep_files", err), nil
			}
			if input.Path != "" && root.source == "workspace" && isInternalGeneratedArtifactInput(input.Path) {
				return errorResult("grep_files", errors.New("path is an internal generated artifact; use source files, copied validation evidence, or rerun the command and redirect output to a normal workspace file (for example under reports/)")), nil
			}
			if input.Path != "" && root.source == "workspace" && isInternalGeneratedArtifactPath(execCtx.Workdir, root.path) {
				return errorResult("grep_files", errors.New("path is an internal generated artifact; use source files, copied validation evidence, or rerun the command and redirect output to a normal workspace file (for example under reports/)")), nil
			}
			matcher, useRegex := compileGrepMatcher(input.Pattern)
			normalized, err := normalizeSearchToolArguments("grep_files", input, execCtx.Config)
			if err != nil {
				return typedToolErrorResult("grep_files", FailureClassOutputBudgetTooSmall, FailureClassOutputBudgetTooSmall, err.Error()), nil
			}
			limit := normalized.EffectiveLimit
			outputBudget := normalized.OutputBudget
			query := newSearchCursorQuery("grep_files", root, input.Pattern, input.Include)
			startIndex, err := decodeSearchCursor(input.Cursor, query)
			if err != nil {
				return searchCursorFailureResult("grep_files", err), nil
			}
			collector := newSearchRecordCollector(startIndex, limit)
			err = walkTextSearchFiles(root.displayBase, root.path, input.Include, func(path string, data string) error {
				if !textMatchesPattern(data, matcher, useRegex, input.Pattern) {
					return nil
				}
				displayPath := relativeOrAbsolute(root.displayBase, path)
				if collector.add(plainSearchPageRecord(displayPath, displayPath, 0)) {
					return errGrepLimitReached
				}
				return nil
			})
			if err != nil && !errors.Is(err, errGrepLimitReached) {
				return errorResult("grep_files", err), nil
			}
			page, err := buildSearchPage(searchPageOptions{
				Tool:               "grep_files",
				Query:              query,
				StartIndex:         startIndex,
				Records:            collector.records,
				ScanComplete:       !collector.stopped,
				RequestedLimit:     normalized.RequestedLimit,
				EffectiveLimit:     limit,
				RequestedByteLimit: outputBudget.Requested,
				EffectiveByteLimit: outputBudget.Effective,
				ByteLimitCapped:    outputBudget.Capped,
			})
			if err != nil {
				return searchPageFailureResult("grep_files", outputBudget, err), nil
			}
			page.Metadata["path_source"] = root.source
			if root.skillName != "" {
				page.Metadata["skill"] = root.skillName
			}
			return session.ToolResult{Name: "grep_files", LLMOutput: page.Output, DisplayOutput: page.Output, Metadata: page.Metadata}, nil
		},
	}
}

var grepSkippedDirNames = map[string]struct{}{
	".artifacts":    {},
	".git":          {},
	".go-cli-agent": {},
	".next":         {},
	".turbo":        {},
	"bin":           {},
	"build":         {},
	"coverage":      {},
	"dist":          {},
	"node_modules":  {},
	"out":           {},
}

var grepSkippedPathFragments = []string{
	"/validation/runs/",
	"/validation/sessions/",
}

var errGrepLimitReached = errors.New("grep limit reached")

const (
	defaultGrepMatchesLimit = 200
	defaultGrepFilesLimit   = 100
	maxGrepFilesLimit       = 200
	maxGrepMatches          = 200
	maxGrepLineOutputBytes  = 4096
)

func truncateGrepMatchedText(text string, limit int) (string, bool) {
	rawLength := len(text)
	if rawLength <= limit {
		return text, false
	}
	if limit <= 0 {
		return "", true
	}
	marker := " ...[truncated]... "
	headBytes := limit / 2
	tailBytes := limit - headBytes
	head := prefixAtRuneBoundary(text, headBytes)
	tail := suffixAtRuneBoundary(text, tailBytes)
	for i := 0; i < 3; i++ {
		omitted := rawLength - len(head) - len(tail)
		if omitted < 0 {
			omitted = 0
		}
		marker = fmt.Sprintf(" ...[truncated: %d bytes omitted]... ", omitted)
		remaining := limit - len(marker)
		if remaining < 2 {
			return prefixAtRuneBoundary(text, limit), true
		}
		headBytes = remaining / 2
		tailBytes = remaining - headBytes
		head = prefixAtRuneBoundary(text, headBytes)
		tail = suffixAtRuneBoundary(text, tailBytes)
	}
	return head + marker + tail, true
}

func shouldSkipGrepDir(path string) bool {
	if _, ok := grepSkippedDirNames[filepath.Base(path)]; ok {
		return true
	}
	normalized := "/" + filepath.ToSlash(path) + "/"
	for _, fragment := range grepSkippedPathFragments {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}

func shouldSkipGrepBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return true
	}
	return !utf8.Valid(data)
}

func isInternalGeneratedArtifactPath(workdir, path string) bool {
	rel, err := filepath.Rel(workdir, path)
	if err != nil {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if strings.EqualFold(part, ".artifacts") {
			return true
		}
	}
	if artifactRoot, err := ResolveWorkspacePath(workdir, ".artifacts"); err == nil && isWithin(artifactRoot, path) {
		return true
	}
	return false
}

func isInternalGeneratedArtifactInput(path string) bool {
	clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	for _, part := range strings.Split(clean, "/") {
		if strings.EqualFold(part, ".artifacts") {
			return true
		}
	}
	return false
}

func isSessionEphemeralArtifactSearchInput(execCtx ExecContext, input string) bool {
	clean := filepath.Clean(filepath.FromSlash(strings.ReplaceAll(strings.TrimSpace(input), `\`, "/")))
	if clean == "." || clean == "" {
		return false
	}
	if isSessionEphemeralArtifactRelativePath(clean) {
		return true
	}
	if !filepath.IsAbs(clean) || execCtx.Store == nil || strings.TrimSpace(execCtx.SessionID) == "" {
		return false
	}
	rel, err := filepath.Rel(execCtx.Store.SessionDir(execCtx.SessionID), clean)
	return err == nil && isSessionEphemeralArtifactRelativePath(rel)
}

func isSessionEphemeralArtifactRelativePath(input string) bool {
	slash := filepath.ToSlash(filepath.Clean(input))
	return slash == "artifacts/tool-outputs" || strings.HasPrefix(slash, "artifacts/tool-outputs/")
}

func sessionArtifactDiscoveryErrorResult(tool, inputPath string) session.ToolResult {
	path := strings.TrimSpace(inputPath)
	result := errorResult(tool, fmt.Errorf("session artifact path %q is not searchable by discovery tools; use read_file with the exact path %q (and offset/limit when paging). Do not guess another path or rerun the producing command", path, path))
	setToolResultFailureClass(&result, FailureClassUnsupportedPath)
	if result.Metadata == nil {
		result.Metadata = make(map[string]any)
	}
	result.Metadata["path"] = path
	result.Metadata["path_source"] = "session_ephemeral_artifact"
	return result
}

var errNoRegisteredSkillPath = errors.New("path is not under a registered skill")

type resolvedSkillPath struct {
	path        string
	displayBase string
	skillName   string
	explicit    bool
}

type resolvedSearchRoot struct {
	path        string
	displayBase string
	source      string
	skillName   string
}

func resolveShellWorkdir(execCtx ExecContext, input string) (string, string, string, error) {
	workspacePath, workspaceErr := ResolveWorkspacePath(execCtx.Workdir, input)
	if workspaceErr == nil {
		info, err := os.Stat(workspacePath)
		if err == nil {
			if info.IsDir() {
				return workspacePath, "workspace", "", nil
			}
			workspaceErr = fmt.Errorf("workdir is not a directory: %s", relativeOrAbsolute(execCtx.Workdir, workspacePath))
		} else {
			workspaceErr = err
		}
	}

	skillPath, _, skillName, skillErr := resolveRegisteredSkillDir(execCtx.Catalog, input)
	if skillErr == nil {
		return skillPath, "skill", skillName, nil
	}
	if !errors.Is(skillErr, errNoRegisteredSkillPath) {
		return "", "", "", skillErr
	}
	return "", "", "", workspaceErr
}

func resolveReadFilePath(execCtx ExecContext, input string) (string, string, string, string, error) {
	if artifactPath, artifactBase, ok, err := resolveSessionEphemeralArtifactFile(execCtx, input); ok || err != nil {
		if err != nil {
			return "", "", "", "", err
		}
		return artifactPath, artifactBase, "session_ephemeral_artifact", "", nil
	}

	workspacePath, workspaceErr := ResolveWorkspacePath(execCtx.Workdir, input)
	if workspaceErr == nil {
		if _, err := os.Stat(workspacePath); err == nil {
			return workspacePath, execCtx.Workdir, "workspace", "", nil
		} else if !os.IsNotExist(err) {
			return "", "", "", "", err
		}
	}

	skillPath, displayBase, skillName, skillErr := resolveRegisteredSkillFile(execCtx.Catalog, input)
	if skillErr == nil {
		return skillPath, displayBase, "skill", skillName, nil
	}
	if !errors.Is(skillErr, errNoRegisteredSkillPath) {
		return "", "", "", "", skillErr
	}
	if workspaceErr != nil {
		return "", "", "", "", workspaceErr
	}
	return workspacePath, execCtx.Workdir, "workspace", "", nil
}

func resolveSessionEphemeralArtifactFile(execCtx ExecContext, input string) (string, string, bool, error) {
	root := strings.TrimSpace(execCtx.EphemeralArtifactRoot)
	if root == "" {
		return "", "", false, nil
	}
	targetInput := strings.TrimSpace(input)
	if targetInput == "" {
		return "", "", false, nil
	}
	if !filepath.IsAbs(targetInput) {
		relativeTarget, ok := relativeSessionEphemeralArtifactTarget(root, targetInput)
		if !ok {
			return "", "", false, nil
		}
		targetInput = relativeTarget
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", false, err
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", false, nil
		}
		return "", "", false, err
	}
	targetResolved, err := resolveWithExistingParent(filepath.Clean(targetInput))
	if err != nil {
		return "", "", false, err
	}
	if !isWithin(rootResolved, targetResolved) {
		return "", "", false, nil
	}
	return targetResolved, rootResolved, true, nil
}

func relativeSessionEphemeralArtifactTarget(root, input string) (string, bool) {
	clean := filepath.Clean(filepath.FromSlash(strings.TrimSpace(input)))
	if filepath.IsAbs(clean) {
		return clean, true
	}
	slash := filepath.ToSlash(clean)
	if slash == "artifacts/tool-outputs" || strings.HasPrefix(slash, "artifacts/tool-outputs/") {
		sessionDir := filepath.Dir(filepath.Dir(root))
		return filepath.Join(sessionDir, filepath.FromSlash(slash)), true
	}
	return "", false
}

func resolveRegisteredSkillFile(catalog *skills.Catalog, input string) (string, string, string, error) {
	match, err := resolveRegisteredSkillPath(catalog, input, false)
	if err != nil {
		return "", "", "", err
	}
	return match.path, match.displayBase, match.skillName, nil
}

func resolveRegisteredSkillDir(catalog *skills.Catalog, input string) (string, string, string, error) {
	match, err := resolveRegisteredSkillPath(catalog, input, true)
	if err != nil {
		return "", "", "", err
	}
	return match.path, match.displayBase, match.skillName, nil
}

func resolveRegisteredSkillPath(catalog *skills.Catalog, input string, requireDir bool) (resolvedSkillPath, error) {
	if catalog == nil || strings.TrimSpace(input) == "" {
		return resolvedSkillPath{}, errNoRegisteredSkillPath
	}

	var matches []resolvedSkillPath
	for _, summary := range catalog.Summaries() {
		skillDir := filepath.Dir(summary.Path)
		if filepath.IsAbs(input) {
			if !pathLexicallyUnderRoot(skillDir, input) {
				continue
			}
			match, ok, err := resolveSkillCandidate(skillDir, summary.Name, input, true, requireDir)
			if err != nil {
				return resolvedSkillPath{}, err
			}
			if ok {
				matches = append(matches, match)
			}
			continue
		}

		rel, explicit, ok := skillRelativeInput(input, summary.Name)
		if !ok {
			continue
		}
		match, candidateOK, err := resolveSkillCandidate(skillDir, summary.Name, rel, explicit, requireDir)
		if err != nil {
			if explicit {
				return resolvedSkillPath{}, err
			}
			continue
		}
		if candidateOK {
			matches = append(matches, match)
		}
	}

	if len(matches) == 0 {
		return resolvedSkillPath{}, errNoRegisteredSkillPath
	}
	if len(matches) > 1 {
		names := make([]string, 0, len(matches))
		for _, match := range matches {
			names = append(names, match.skillName)
		}
		sort.Strings(names)
		return resolvedSkillPath{}, fmt.Errorf("ambiguous skill-relative path %q matches multiple registered skills: %s; use skills/<skill-name>/...", input, strings.Join(names, ", "))
	}
	return matches[0], nil
}

func pathLexicallyUnderRoot(root, input string) bool {
	base, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	target, err := filepath.Abs(input)
	if err != nil {
		return false
	}
	return isWithin(filepath.Clean(base), filepath.Clean(target))
}

func resolveSkillCandidate(skillDir, skillName, input string, explicit, requireDir bool) (resolvedSkillPath, bool, error) {
	path, err := resolvePathUnderRoot(skillDir, input)
	if err != nil {
		return resolvedSkillPath{}, false, err
	}
	info, statErr := os.Stat(path)
	if requireDir {
		if statErr != nil {
			if explicit {
				return resolvedSkillPath{}, false, statErr
			}
			return resolvedSkillPath{}, false, nil
		}
		if !info.IsDir() {
			if explicit {
				return resolvedSkillPath{}, false, fmt.Errorf("skill workdir is not a directory: %s", relativeOrAbsolute(skillDisplayBase(skillDir), path))
			}
			return resolvedSkillPath{}, false, nil
		}
	} else if !explicit {
		if statErr != nil || info.IsDir() {
			return resolvedSkillPath{}, false, nil
		}
	}

	return resolvedSkillPath{
		path:        path,
		displayBase: skillDisplayBase(skillDir),
		skillName:   skillName,
		explicit:    explicit,
	}, true, nil
}

func skillRelativeInput(input, skillName string) (string, bool, bool) {
	cleaned := filepath.ToSlash(filepath.Clean(input))
	if cleaned == "." || cleaned == "" {
		return "", false, false
	}
	for _, prefix := range []string{
		"skills/" + skillName,
		"../skills/" + skillName,
	} {
		if cleaned == prefix {
			return ".", true, true
		}
		if strings.HasPrefix(cleaned, prefix+"/") {
			return strings.TrimPrefix(cleaned, prefix+"/"), true, true
		}
	}
	if strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "skills/") {
		return "", false, false
	}
	return cleaned, false, true
}

func resolvePathUnderRoot(root, input string) (string, error) {
	base, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}
	target := input
	if !filepath.IsAbs(target) {
		target = filepath.Join(base, target)
	}
	target = filepath.Clean(target)
	resolved, err := resolveWithExistingParent(target)
	if err != nil {
		return "", err
	}
	if !isWithin(base, resolved) {
		return "", errors.New("path escapes registered skill root")
	}
	return resolved, nil
}

func skillDisplayBase(skillDir string) string {
	parent := filepath.Dir(skillDir)
	if filepath.Base(parent) == "skills" {
		return filepath.Dir(parent)
	}
	return parent
}

func resolveGrepRoot(execCtx ExecContext, inputPath string) (resolvedSearchRoot, error) {
	if inputPath == "" {
		return resolvedSearchRoot{path: execCtx.Workdir, displayBase: execCtx.Workdir, source: "workspace"}, nil
	}
	path, workspaceErr := ResolveWorkspacePath(execCtx.Workdir, inputPath)
	if workspaceErr == nil {
		if _, err := os.Lstat(path); err == nil {
			return resolvedSearchRoot{path: path, displayBase: execCtx.Workdir, source: "workspace"}, nil
		} else {
			workspaceErr = fmt.Errorf("path %q does not exist or is not accessible: %w", inputPath, err)
		}
	}

	skillPath, skillErr := resolveRegisteredSearchPath(execCtx.Catalog, inputPath)
	if skillErr == nil {
		if _, err := os.Lstat(skillPath.path); err != nil {
			return resolvedSearchRoot{}, fmt.Errorf("path %q does not exist or is not accessible: %w", inputPath, err)
		}
		return resolvedSearchRoot{
			path:        skillPath.path,
			displayBase: skillPath.displayBase,
			source:      "skill",
			skillName:   skillPath.skillName,
		}, nil
	}
	if !errors.Is(skillErr, errNoRegisteredSkillPath) {
		return resolvedSearchRoot{}, skillErr
	}
	if workspaceErr != nil {
		return resolvedSearchRoot{}, workspaceErr
	}
	return resolvedSearchRoot{}, fmt.Errorf("path %q does not exist or is not accessible", inputPath)
}

func resolveRegisteredSearchPath(catalog *skills.Catalog, input string) (resolvedSkillPath, error) {
	match, err := resolveRegisteredSkillPath(catalog, input, false)
	if err == nil || !errors.Is(err, errNoRegisteredSkillPath) {
		return match, err
	}
	return resolveRegisteredSkillPath(catalog, input, true)
}

func compileGrepMatcher(pattern string) (*regexp.Regexp, bool) {
	matcher, err := regexp.Compile(pattern)
	return matcher, err == nil
}

func firstGrepMatchSpan(line string, matcher *regexp.Regexp, useRegex bool, pattern string) (int, int, bool) {
	if useRegex {
		span := matcher.FindStringIndex(line)
		if span == nil {
			return 0, 0, false
		}
		return span[0], span[1], true
	}
	start := strings.Index(line, pattern)
	if start < 0 {
		return 0, 0, false
	}
	return start, start + len(pattern), true
}

func validateGrepPattern(pattern string) error {
	if pattern == "" {
		return errors.New("pattern is required")
	}
	return nil
}

func validateToolPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("path is required")
	}
	return nil
}

func textMatchesPattern(text string, matcher *regexp.Regexp, useRegex bool, pattern string) bool {
	if useRegex {
		return matcher.MatchString(text)
	}
	return strings.Contains(text, pattern)
}

func normalizeGrepFilesLimit(limit int) int {
	if limit <= 0 {
		return defaultGrepFilesLimit
	}
	if limit > maxGrepFilesLimit {
		return maxGrepFilesLimit
	}
	return limit
}

func normalizeGrepMatchesLimit(limit int) int {
	if limit <= 0 {
		return defaultGrepMatchesLimit
	}
	if limit > maxGrepMatches {
		return maxGrepMatches
	}
	return limit
}

func walkTextSearchFiles(workdir, root, include string, fn func(path string, data string) error) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if sameCleanPath(path, root) {
				return err
			}
			return nil
		}
		if info.IsDir() {
			if path != root && shouldSkipGrepDir(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if include != "" && !pathMatchesInclude(workdir, path, include) {
			return nil
		}
		data, _, readErr := fileutil.ReadRegularFileNoSymlink(path)
		if readErr != nil {
			if sameCleanPath(path, root) {
				return readErr
			}
			return nil
		}
		if shouldSkipGrepBinary(data) {
			return nil
		}
		return fn(path, string(data))
	})
}

func pathMatchesInclude(workdir, path, include string) bool {
	relative := filepath.ToSlash(relativeOrAbsolute(workdir, path))
	base := filepath.Base(relative)
	matched, err := doublestar.Match(include, relative)
	if err == nil && matched {
		return true
	}
	matched, err = doublestar.Match(include, base)
	return err == nil && matched
}

func annotateReadWindow(workdir, path string, offset, end, totalLines, requestedLimit int, capped bool, body string) string {
	startLine := offset + 1
	endLine := end
	if endLine < startLine {
		startLine = endLine
	}
	if endLine < 1 {
		startLine = 0
	}
	header := fmt.Sprintf("[read_file path=%s lines=%d-%d of %d", relativeOrAbsolute(workdir, path), startLine, endLine, totalLines)
	if capped {
		header += fmt.Sprintf("; requested_limit=%d capped_to=%d", requestedLimit, readFileMaxLimit)
	}
	header += "]"
	if body == "" {
		return header
	}
	return header + "\n" + body
}

func defFinish() Definition {
	return Definition{
		Name:        "finish",
		Description: "Signal that the current task is complete and provide the final concise result for the user. Use only after requested work, required artifacts, and necessary validation are complete. If work is unfinished because an external prerequisite, missing user decision, or external wait prevents progress, use await_input instead and state the blocker/resume condition there. Report any unrun/failed validation honestly, but do not call finish merely because the model has no more ideas.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{
					"type":        "string",
					"description": "Concise final user-facing summary, including validation status or blockers when relevant.",
				},
			},
			"required": []string{"message"},
		},
		Execute: func(_ context.Context, _ ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			var input struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("finish", err), nil
			}
			if strings.TrimSpace(input.Message) == "" {
				return errorResult("finish", errors.New("message is required")), nil
			}
			return session.ToolResult{
				Name:          "finish",
				LLMOutput:     input.Message,
				DisplayOutput: input.Message,
				Final:         true,
			}, nil
		},
	}
}

func defAwaitInput() Definition {
	const (
		maxReasonChars    = 2000
		maxResumeChars    = 2000
		maxBlockerChars   = 1000
		maxBlockerEntries = 20
	)
	return Definition{
		Name:        "await_input",
		Description: "Park the current run in awaiting_input without marking the task or active Goal complete. Use only when work is unfinished but cannot safely continue until an external prerequisite changes, the user provides a decision, or an external wait completes. Use finish for completed work, request_user_input for Plan Mode questions, and agent_wait for background child results. If an active Goal has a durable blocker, record it with record_goal_progress before parking when useful.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind": map[string]any{
					"type":        "string",
					"enum":        []string{"blocked", "needs_input", "external_wait"},
					"description": "Why execution must park. Defaults to blocked when omitted.",
				},
				"reason": map[string]any{
					"type":        "string",
					"maxLength":   maxReasonChars,
					"description": "Concise user-facing explanation of why the unfinished task cannot continue now.",
				},
				"blockers": map[string]any{
					"type":        "array",
					"maxItems":    maxBlockerEntries,
					"items":       map[string]any{"type": "string", "maxLength": maxBlockerChars},
					"description": "Optional concrete blockers that must be resolved.",
				},
				"resume_condition": map[string]any{
					"type":        "string",
					"maxLength":   maxResumeChars,
					"description": "Optional condition or information needed before continue can make progress.",
				},
			},
			"required": []string{"reason"},
		},
		Execute: func(_ context.Context, _ ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			var input struct {
				Kind            string   `json:"kind"`
				Reason          string   `json:"reason"`
				Blockers        []string `json:"blockers"`
				ResumeCondition string   `json:"resume_condition"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("await_input", err), nil
			}
			kind := strings.ToLower(strings.TrimSpace(input.Kind))
			if kind == "" {
				kind = "blocked"
			}
			switch kind {
			case "blocked", "needs_input", "external_wait":
			default:
				return errorResult("await_input", fmt.Errorf("kind must be blocked, needs_input, or external_wait")), nil
			}
			reason := strings.TrimSpace(input.Reason)
			if reason == "" {
				return errorResult("await_input", errors.New("reason is required")), nil
			}
			if utf8.RuneCountInString(reason) > maxReasonChars {
				return errorResult("await_input", fmt.Errorf("reason exceeds %d characters", maxReasonChars)), nil
			}
			resumeCondition := strings.TrimSpace(input.ResumeCondition)
			if utf8.RuneCountInString(resumeCondition) > maxResumeChars {
				return errorResult("await_input", fmt.Errorf("resume_condition exceeds %d characters", maxResumeChars)), nil
			}
			if len(input.Blockers) > maxBlockerEntries {
				return errorResult("await_input", fmt.Errorf("blockers exceeds %d items", maxBlockerEntries)), nil
			}
			blockers := make([]string, 0, len(input.Blockers))
			for _, blocker := range input.Blockers {
				blocker = strings.TrimSpace(blocker)
				if blocker == "" {
					continue
				}
				if utf8.RuneCountInString(blocker) > maxBlockerChars {
					return errorResult("await_input", fmt.Errorf("blocker exceeds %d characters", maxBlockerChars)), nil
				}
				blockers = append(blockers, blocker)
			}

			var display strings.Builder
			display.WriteString("Execution parked (" + kind + "): " + reason)
			if len(blockers) > 0 {
				display.WriteString("\nBlockers:")
				for _, blocker := range blockers {
					display.WriteString("\n- " + blocker)
				}
			}
			if resumeCondition != "" {
				display.WriteString("\nResume when: " + resumeCondition)
			}
			metadata := map[string]any{
				MetadataAwaitInput:       true,
				MetadataAwaitInputKind:   kind,
				MetadataAwaitInputReason: reason,
			}
			if len(blockers) > 0 {
				metadata[MetadataAwaitInputBlockers] = blockers
			}
			if resumeCondition != "" {
				metadata[MetadataAwaitInputResume] = resumeCondition
			}
			return session.ToolResult{
				Name:          "await_input",
				LLMOutput:     display.String(),
				DisplayOutput: display.String(),
				Metadata:      metadata,
			}, nil
		},
	}
}

func defLoadSkill(catalog *skills.Catalog) Definition {
	availableSkills := catalog.Names()
	nameSchema := map[string]any{"type": "string"}
	description := "Load the full instructions for one registered skill by exact name. Use this when the user names a skill or an available skill clearly matches the requested task; do not invent aliases or load skills that are not listed."
	if len(availableSkills) > 0 {
		nameSchema["enum"] = availableSkills
		description = fmt.Sprintf("Load the full instructions for one registered skill by exact name. Use this when the user names a skill or an available skill clearly matches the requested task; do not invent aliases or load skills that are not listed. Available skills: %s.", strings.Join(availableSkills, ", "))
	}
	return Definition{
		Name:        "load_skill",
		Description: description,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": withDescription(nameSchema, "Exact registered skill name from the available-skills list."),
				"force_reload": map[string]any{
					"type":        "boolean",
					"description": "When true, reload the full skill body even if this session already loaded it. Use only when the skill file may have changed or the user explicitly asks to reload it.",
				},
			},
			"required":             []string{"name"},
			"additionalProperties": false,
		},
		Execute: func(_ context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			var input struct {
				Name        string `json:"name"`
				ForceReload bool   `json:"force_reload"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("load_skill", err), nil
			}
			if execCtx.Catalog == nil {
				return errorResult("load_skill", errors.New("skill catalog not available")), nil
			}
			skill, skillErr := execCtx.Catalog.Load(input.Name)
			if skillErr != nil {
				if available := execCtx.Catalog.Names(); len(available) > 0 {
					skillErr = fmt.Errorf("%w; available skills: %s", skillErr, strings.Join(available, ", "))
				}
				return errorResult("load_skill", skillErr), nil
			}
			skillDir := filepath.Dir(skill.Path)
			shellWorkdir := relativeOrAbsolute(execCtx.Workdir, skillDir)
			explorerReadOnly := execCtx.ToolProfile == session.ToolProfileExplorerReadOnly
			if !input.ForceReload && skillLoaded(execCtx, input.Name) {
				output := fmt.Sprintf("<skill name=%q path=%q already_loaded=true shell_workdir=%q>\nThis skill has already been loaded in this session. Reuse the prior instructions; call load_skill again with force_reload=true only if the skill file changed or the user explicitly asks to reload it.\nAvailable bundle files can still be inspected or searched with read_file, grep, or grep_files using paths like `skills/%s/references/...` or skill-relative links.\n</skill>", skill.Name, skill.Path, shellWorkdir, skill.Name)
				metadata := map[string]any{
					"path":           skill.Path,
					"shell_workdir":  shellWorkdir,
					"already_loaded": true,
					"force_reload":   false,
				}
				if explorerReadOnly {
					output = fmt.Sprintf("<skill name=%q path=%q already_loaded=true tool_profile=%q>\nThis skill has already been loaded. Reuse its read-only guidance; modification and command instructions remain unavailable in this capability profile. Inspect bundle references only with read_file, grep, or grep_files.\n</skill>", skill.Name, skill.Path, execCtx.ToolProfile)
					delete(metadata, "shell_workdir")
					metadata["tool_profile"] = execCtx.ToolProfile
				}
				return session.ToolResult{
					Name:          "load_skill",
					LLMOutput:     output,
					DisplayOutput: fmt.Sprintf("Skill already loaded: %s", input.Name),
					Metadata:      metadata,
				}, nil
			}
			body, err := execCtx.Catalog.LoadBody(input.Name)
			if err != nil {
				if available := execCtx.Catalog.Names(); len(available) > 0 {
					err = fmt.Errorf("%w; available skills: %s", err, strings.Join(available, ", "))
				}
				return errorResult("load_skill", err), nil
			}
			if err := markSkillLoaded(execCtx, input.Name); err != nil {
				return errorResult("load_skill", err), nil
			}
			output := fmt.Sprintf("<skill path=%q shell_workdir=%q>\nWhen this skill uses relative shell paths, call the shell tool with `workdir=%q` so commands run from the skill bundle root.\nSkill bundle files are registered read-only resources, not workspace files. To inspect or search referenced skill files, call read_file, grep, or grep_files with paths like `skills/%s/references/...` or an unambiguous skill-relative link such as `references/...`; do not resolve those links under the workspace directory.\n\n%s\n</skill>", skill.Path, shellWorkdir, shellWorkdir, skill.Name, body)
			metadata := map[string]any{
				"path":          skill.Path,
				"shell_workdir": shellWorkdir,
				"force_reload":  input.ForceReload,
			}
			if explorerReadOnly {
				output = fmt.Sprintf("<skill path=%q tool_profile=%q>\nThis capability profile remains read-only. Treat any modification or command instruction in the skill as unavailable. Inspect referenced bundle files only with read_file, grep, or grep_files.\n\n%s\n</skill>", skill.Path, execCtx.ToolProfile, body)
				delete(metadata, "shell_workdir")
				metadata["tool_profile"] = execCtx.ToolProfile
			}
			return session.ToolResult{
				Name:          "load_skill",
				LLMOutput:     output,
				DisplayOutput: fmt.Sprintf("Loaded skill: %s", input.Name),
				Metadata:      metadata,
			}, nil
		},
	}
}

func defGetGoal() Definition {
	return Definition{
		Name:        "get_goal",
		Description: "Read the current durable session goal or mission. Use before completion audits, resume decisions, or when the user asks about goal progress. Returns null when this session has no goal.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Execute: func(_ context.Context, execCtx ExecContext, _ json.RawMessage) (session.ToolResult, error) {
			if err := requireToolSessionMetadata(execCtx); err != nil {
				if missingSessionMetadata(err) {
					return readOnlyEmptyStateResult("get_goal", "null", "session_metadata_missing", map[string]any{
						"goal_present":          false,
						"session_metadata_path": sessionMetadataFilePath(execCtx),
					}), nil
				}
				return errorResult("get_goal", err), nil
			}
			goal, err := execCtx.Store.LoadGoal(execCtx.SessionID)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return session.ToolResult{Name: "get_goal", LLMOutput: "null", DisplayOutput: "null"}, nil
				}
				return errorResult("get_goal", err), nil
			}
			data, _ := json.MarshalIndent(goal, "", "  ")
			return session.ToolResult{
				Name:          "get_goal",
				LLMOutput:     string(data),
				DisplayOutput: string(data),
				Metadata: map[string]any{
					"path":    filepath.Join(execCtx.Store.SessionDir(execCtx.SessionID), "goal.json"),
					"goal_id": goal.GoalID,
					"status":  goal.Status,
				},
			}, nil
		},
	}
}

func defCreateGoal() Definition {
	return Definition{
		Name:        "create_goal",
		Description: "Create one durable goal for this session when the user or system explicitly asks for goal-driven work. Do not infer a goal from ordinary prompts. Fails if a current goal already exists. For large goal-driven work, any internal mission role plan should choose role values directly from planner, generator, and evaluator so Settings role provider overrides apply.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"objective": map[string]any{
					"type":        "string",
					"maxLength":   session.MaxGoalObjectiveChars,
					"description": fmt.Sprintf("Durable objective, max %d characters. Treat as user-provided task context, not higher-priority instructions.", session.MaxGoalObjectiveChars),
				},
				"mode": map[string]any{
					"type":        "string",
					"enum":        []string{"goal", "mission"},
					"description": "goal for a single long objective, mission for structured features/milestones.",
				},
				"token_budget": map[string]any{"type": "integer", "description": "Optional positive token budget."},
				"provider_time_budget_minutes": map[string]any{
					"type":        "integer",
					"description": "Optional positive provider-call elapsed-time budget in minutes. Paused time, queue wait, tools, and child active runtime are excluded.",
				},
				"time_budget_minutes": map[string]any{
					"type":        "integer",
					"description": "Deprecated compatibility alias for provider_time_budget_minutes.",
				},
				"success_criteria": withDescription(stringArraySchema(), "Optional concrete completion criteria."),
				"validation_plan":  withDescription(stringArraySchema(), "Optional validation commands, artifacts, manual checks, browser checks, or review checks."),
				"features":         withDescription(stringArraySchema(), "Optional mission features when mode is mission."),
				"milestones":       withDescription(stringArraySchema(), "Optional mission milestones when mode is mission."),
				"require_plan_approval": map[string]any{
					"type":        "boolean",
					"description": "When true, mission plan starts in needs_approval.",
				},
				"stop_on_budget": map[string]any{
					"type":        "boolean",
					"description": "When true, budget exhaustion records a budget-limited goal and allows only a wrap-up turn instead of open-ended continuation.",
				},
				"create_tasks_from_plan": map[string]any{
					"type":        "boolean",
					"description": "When true, the mission plan may be synced into durable tasks by explicit follow-up work.",
				},
			},
			"required": []string{"objective"},
		},
		Execute: func(_ context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			var input struct {
				Objective                 string   `json:"objective"`
				Mode                      string   `json:"mode"`
				TokenBudget               *int64   `json:"token_budget"`
				ProviderTimeBudgetMinutes *int64   `json:"provider_time_budget_minutes"`
				TimeBudgetMinutes         *int64   `json:"time_budget_minutes"`
				SuccessCriteria           []string `json:"success_criteria"`
				ValidationPlan            []string `json:"validation_plan"`
				Features                  []string `json:"features"`
				Milestones                []string `json:"milestones"`
				RequirePlanApproval       bool     `json:"require_plan_approval"`
				StopOnBudget              bool     `json:"stop_on_budget"`
				CreateTasksFromPlan       bool     `json:"create_tasks_from_plan"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("create_goal", err), nil
			}
			var seconds *int64
			providerTimeBudgetMinutes := input.ProviderTimeBudgetMinutes
			if providerTimeBudgetMinutes == nil {
				providerTimeBudgetMinutes = input.TimeBudgetMinutes
			}
			if providerTimeBudgetMinutes != nil {
				value := *providerTimeBudgetMinutes * 60
				seconds = &value
			}
			if err := requireToolSessionMetadata(execCtx); err != nil {
				return errorResult("create_goal", err), nil
			}
			previousHistory, err := execCtx.Store.LoadGoalHistory(execCtx.SessionID)
			if err != nil {
				return errorResult("create_goal", err), nil
			}
			previousTasks, err := execCtx.Store.ListTasks(execCtx.SessionID)
			if err != nil {
				return errorResult("create_goal", err), nil
			}
			goal, err := execCtx.Store.CreateGoal(execCtx.SessionID, session.GoalDraft{
				Enabled:             true,
				Mode:                input.Mode,
				Objective:           input.Objective,
				SuccessCriteria:     input.SuccessCriteria,
				ValidationPlan:      input.ValidationPlan,
				TokenBudget:         input.TokenBudget,
				TimeBudgetSeconds:   seconds,
				RequirePlanApproval: input.RequirePlanApproval,
				StopOnBudget:        input.StopOnBudget,
				CreateTasksFromPlan: input.CreateTasksFromPlan,
				Features:            input.Features,
				Milestones:          input.Milestones,
				Source:              session.GoalSourceTool,
			})
			if err != nil {
				return errorResult("create_goal", err), nil
			}
			if err := emitToolEvent(execCtx, "goal.created", map[string]any{
				"goal_id":   goal.GoalID,
				"mode":      goal.Mode,
				"status":    goal.Status,
				"objective": goal.Objective,
			}); err != nil {
				if _, rollbackErr := execCtx.Store.ClearGoal(execCtx.SessionID); rollbackErr != nil {
					return errorResult("create_goal", fmt.Errorf("restore goal after goal.created event failure %v: %w", err, rollbackErr)), nil
				}
				if rollbackErr := execCtx.Store.RestoreGoalHistory(execCtx.SessionID, previousHistory); rollbackErr != nil {
					return errorResult("create_goal", fmt.Errorf("restore goal history after goal.created event failure %v: %w", err, rollbackErr)), nil
				}
				if rollbackErr := execCtx.Store.SaveTasks(execCtx.SessionID, previousTasks); rollbackErr != nil {
					return errorResult("create_goal", fmt.Errorf("restore tasks after goal.created event failure %v: %w", err, rollbackErr)), nil
				}
				return errorResult("create_goal", fmt.Errorf("record goal.created event: %w", err)), nil
			}
			previousPlanMode, err := execCtx.Store.SnapshotPlanMode(execCtx.SessionID)
			if err != nil {
				return errorResult("create_goal", err), nil
			}
			previousPlanModeHistory, err := execCtx.Store.LoadPlanModeHistory(execCtx.SessionID)
			if err != nil {
				return errorResult("create_goal", err), nil
			}
			if planMode, created, err := execCtx.Store.EnsurePlanModeForGoal(execCtx.SessionID, goal, session.PlanModeSourceTool); err != nil {
				return errorResult("create_goal", err), nil
			} else if created {
				if err := emitToolEvent(execCtx, "planmode.created", map[string]any{
					"plan_mode_id":   planMode.PlanModeID,
					"status":         planMode.Status,
					"linked_goal_id": planMode.LinkedGoalID,
				}); err != nil {
					if result, ok := restoreCreateGoalWithPlanMode(execCtx, previousPlanMode, previousPlanModeHistory, previousHistory, previousTasks, "planmode.created", err); ok {
						return result, nil
					}
					return errorResult("create_goal", fmt.Errorf("record planmode.created event: %w", err)), nil
				}
			} else if planMode.PlanModeID != "" && planMode.LinkedGoalID == goal.GoalID && previousPlanMode.State.LinkedGoalID != goal.GoalID {
				if err := emitToolEvent(execCtx, "planmode.linked_goal", map[string]any{
					"plan_mode_id":   planMode.PlanModeID,
					"status":         planMode.Status,
					"linked_goal_id": planMode.LinkedGoalID,
				}); err != nil {
					if result, ok := restoreCreateGoalWithPlanMode(execCtx, previousPlanMode, previousPlanModeHistory, previousHistory, previousTasks, "planmode.linked_goal", err); ok {
						return result, nil
					}
					return errorResult("create_goal", fmt.Errorf("record planmode.linked_goal event: %w", err)), nil
				}
			}
			data, _ := json.MarshalIndent(goal, "", "  ")
			return session.ToolResult{
				Name:          "create_goal",
				LLMOutput:     string(data),
				DisplayOutput: string(data),
				Metadata: map[string]any{
					"path":    filepath.Join(execCtx.Store.SessionDir(execCtx.SessionID), "goal.json"),
					"goal_id": goal.GoalID,
					"status":  goal.Status,
				},
			}, nil
		},
	}
}

func restoreCreateGoalWithPlanMode(execCtx ExecContext, previousPlanMode session.PlanModeSnapshot, previousPlanModeHistory []session.PlanModeHistoryEntry, previousGoalHistory []session.GoalHistoryEntry, previousTasks []session.Task, eventType string, cause error) (session.ToolResult, bool) {
	if rollbackErr := execCtx.Store.RestorePlanModeSnapshot(execCtx.SessionID, previousPlanMode); rollbackErr != nil {
		return errorResult("create_goal", fmt.Errorf("restore plan mode after %s event failure %v: %w", eventType, cause, rollbackErr)), true
	}
	if rollbackErr := execCtx.Store.RestorePlanModeHistory(execCtx.SessionID, previousPlanModeHistory); rollbackErr != nil {
		return errorResult("create_goal", fmt.Errorf("restore plan mode history after %s event failure %v: %w", eventType, cause, rollbackErr)), true
	}
	if _, rollbackErr := execCtx.Store.ClearGoal(execCtx.SessionID); rollbackErr != nil {
		return errorResult("create_goal", fmt.Errorf("restore goal after %s event failure %v: %w", eventType, cause, rollbackErr)), true
	}
	if rollbackErr := execCtx.Store.RestoreGoalHistory(execCtx.SessionID, previousGoalHistory); rollbackErr != nil {
		return errorResult("create_goal", fmt.Errorf("restore goal history after %s event failure %v: %w", eventType, cause, rollbackErr)), true
	}
	if rollbackErr := execCtx.Store.SaveTasks(execCtx.SessionID, previousTasks); rollbackErr != nil {
		return errorResult("create_goal", fmt.Errorf("restore tasks after %s event failure %v: %w", eventType, cause, rollbackErr)), true
	}
	return session.ToolResult{}, false
}

func defRecordGoalProgress() Definition {
	return Definition{
		Name:        "record_goal_progress",
		Description: "Append structured progress, handoff, validation evidence, evaluator child/queue attribution, commands, artifacts, blockers, or budget wrap-up facts to the current durable goal. Read get_goal before sending any feature, milestone, or validation id: these ids are system-assigned and are not free-form labels. This does not change the objective, pause/resume/clear the goal, approve plans, or mark completion; use update_goal only after a real completion audit.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind": map[string]any{
					"type":        "string",
					"description": "Optional record kind such as progress, handoff, validation, blocker, or budget_wrapup.",
				},
				"summary":            withDescription(map[string]any{"type": "string"}, "Short progress or handoff summary."),
				"evidence":           withDescription(stringArraySchema(), "Concrete evidence references."),
				"linked_artifacts":   withDescription(stringArraySchema(), "Files or artifacts that support this progress record."),
				"commands":           goalProgressCommandArraySchema(),
				"blockers":           withDescription(stringArraySchema(), "Current blockers or remaining risks."),
				"child_session_ids":  withDescription(stringArraySchema(), "Child session ids related to this progress or handoff."),
				"queue_job_ids":      withDescription(stringArraySchema(), "Queue job ids related to this progress or handoff."),
				"validation_ids":     withDescription(stringArraySchema(), "Existing ids from get_goal.validation_plan or get_goal.mission.validation_contract related to this progress. Omit when the current goal has no validation items."),
				"feature_updates":    missionFeatureUpdateArraySchema(),
				"milestone_updates":  missionMilestoneUpdateArraySchema(),
				"validation_updates": missionValidationUpdateArraySchema(),
			},
		},
		Execute: func(_ context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			var input struct {
				Kind              string                                   `json:"kind"`
				Summary           string                                   `json:"summary"`
				Evidence          []string                                 `json:"evidence"`
				LinkedArtifacts   []string                                 `json:"linked_artifacts"`
				Commands          []session.GoalProgressCommand            `json:"commands"`
				Blockers          []string                                 `json:"blockers"`
				ChildSessionIDs   []string                                 `json:"child_session_ids"`
				QueueJobIDs       []string                                 `json:"queue_job_ids"`
				ValidationIDs     []string                                 `json:"validation_ids"`
				FeatureUpdates    []session.MissionFeatureProgressUpdate   `json:"feature_updates"`
				MilestoneUpdates  []session.MissionMilestoneProgressUpdate `json:"milestone_updates"`
				ValidationUpdates []session.GoalValidationProgressUpdate   `json:"validation_updates"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("record_goal_progress", err), nil
			}
			if err := requireToolSessionMetadata(execCtx); err != nil {
				return errorResult("record_goal_progress", err), nil
			}
			goal, record, err := execCtx.Store.RecordGoalProgress(execCtx.SessionID, session.GoalProgressInput{
				Source:            session.GoalSourceTool,
				Kind:              input.Kind,
				Summary:           input.Summary,
				Evidence:          input.Evidence,
				LinkedArtifacts:   input.LinkedArtifacts,
				Commands:          input.Commands,
				Blockers:          input.Blockers,
				ChildSessionIDs:   input.ChildSessionIDs,
				QueueJobIDs:       input.QueueJobIDs,
				ValidationIDs:     input.ValidationIDs,
				FeatureUpdates:    input.FeatureUpdates,
				MilestoneUpdates:  input.MilestoneUpdates,
				ValidationUpdates: input.ValidationUpdates,
			})
			if err != nil {
				return errorResult("record_goal_progress", err), nil
			}
			if execCtx.Emit != nil {
				execCtx.Emit("goal.progress.recorded", map[string]any{
					"goal_id":     goal.GoalID,
					"status":      goal.Status,
					"kind":        record.Kind,
					"progress_id": record.ID,
				})
			}
			data, _ := json.MarshalIndent(goal, "", "  ")
			return session.ToolResult{
				Name:          "record_goal_progress",
				LLMOutput:     string(data),
				DisplayOutput: string(data),
				Metadata: map[string]any{
					"path":        filepath.Join(execCtx.Store.SessionDir(execCtx.SessionID), "goal.json"),
					"goal_id":     goal.GoalID,
					"status":      goal.Status,
					"progress_id": record.ID,
				},
			}, nil
		},
	}
}

func defUpdateGoal() Definition {
	return Definition{
		Name:        "update_goal",
		Description: "Mark the existing session goal complete after a concrete completion audit. The model may only set status=complete; pause, resume, clear, objective changes, and budget-limited status are user/system controlled. Evidence is persisted into goal.json, not only history.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status": map[string]any{
					"type":        "string",
					"enum":        []string{"complete"},
					"description": "Only complete is allowed from the model tool.",
				},
				"evidence":            withDescription(stringArraySchema(), "Optional concrete evidence refs for the completion audit."),
				"completion_summary":  withDescription(map[string]any{"type": "string"}, "Optional short completion audit summary."),
				"criteria_statuses":   withDescription(goalItemStatusUpdateArraySchema(), "Optional status/evidence updates for success criteria by their existing id. Get exact ids from get_goal first; ids are system-assigned, not free-form labels. Omit this if the goal has no criteria."),
				"validation_statuses": withDescription(goalItemStatusUpdateArraySchema(), "Optional status/evidence updates for validation plan items by their existing id. Get exact ids from get_goal first; ids are system-assigned, not free-form labels. Omit this if the goal has no validation items."),
			},
			"required": []string{"status"},
		},
		Execute: func(_ context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			var input struct {
				Status             string                         `json:"status"`
				Evidence           []string                       `json:"evidence"`
				CompletionSummary  string                         `json:"completion_summary"`
				CriteriaStatuses   []session.GoalItemStatusUpdate `json:"criteria_statuses"`
				ValidationStatuses []session.GoalItemStatusUpdate `json:"validation_statuses"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("update_goal", err), nil
			}
			if strings.TrimSpace(input.Status) != session.GoalStatusComplete {
				return errorResult("update_goal", errors.New("update_goal can only mark the existing goal complete; pause, resume, and budget-limited status changes are controlled by the user or system")), nil
			}
			if err := requireToolSessionMetadata(execCtx); err != nil {
				return errorResult("update_goal", err), nil
			}
			previousGoal, err := execCtx.Store.LoadGoal(execCtx.SessionID)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return errorResult("update_goal", errors.New("session has no current goal")), nil
				}
				return errorResult("update_goal", err), nil
			}
			previousHistory, err := execCtx.Store.LoadGoalHistory(execCtx.SessionID)
			if err != nil {
				return errorResult("update_goal", err), nil
			}
			goal, err := execCtx.Store.CompleteGoal(execCtx.SessionID, session.GoalCompletionInput{
				Source:             session.GoalSourceTool,
				CompletedBy:        session.GoalSourceTool,
				Summary:            input.CompletionSummary,
				Evidence:           input.Evidence,
				CriteriaStatuses:   input.CriteriaStatuses,
				ValidationStatuses: input.ValidationStatuses,
			})
			if err != nil {
				return errorResult("update_goal", err), nil
			}
			if err := emitToolEvent(execCtx, "goal.completed", map[string]any{
				"goal_id":   goal.GoalID,
				"mode":      goal.Mode,
				"status":    goal.Status,
				"objective": goal.Objective,
				"evidence":  append([]string(nil), goal.CompletionAudit.Evidence...),
			}); err != nil {
				if rollbackErr := execCtx.Store.SaveGoal(execCtx.SessionID, previousGoal); rollbackErr != nil {
					return errorResult("update_goal", fmt.Errorf("restore goal after goal.completed event failure %v: %w", err, rollbackErr)), nil
				}
				if rollbackErr := execCtx.Store.RestoreGoalHistory(execCtx.SessionID, previousHistory); rollbackErr != nil {
					return errorResult("update_goal", fmt.Errorf("restore goal history after goal.completed event failure %v: %w", err, rollbackErr)), nil
				}
				return errorResult("update_goal", fmt.Errorf("record goal.completed event: %w", err)), nil
			}
			data, _ := json.MarshalIndent(goal, "", "  ")
			return session.ToolResult{
				Name:          "update_goal",
				LLMOutput:     string(data),
				DisplayOutput: string(data),
				Metadata: map[string]any{
					"path":    filepath.Join(execCtx.Store.SessionDir(execCtx.SessionID), "goal.json"),
					"goal_id": goal.GoalID,
					"status":  goal.Status,
				},
			}, nil
		},
	}
}

func requireToolSessionMetadata(execCtx ExecContext) error {
	_, err := loadToolSessionMetadata(execCtx)
	return err
}

func missingSessionMetadata(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}

func readOnlyEmptyStateResult(toolName, output, reason string, metadata map[string]any) session.ToolResult {
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["empty_state"] = true
	metadata["reason"] = reason
	return session.ToolResult{
		Name:          toolName,
		LLMOutput:     output,
		DisplayOutput: output,
		Metadata:      metadata,
	}
}

func sessionMetadataFilePath(execCtx ExecContext) string {
	if execCtx.Store == nil {
		return ""
	}
	return filepath.Join(execCtx.Store.SessionDir(execCtx.SessionID), "session.json")
}

func loadToolSessionMetadata(execCtx ExecContext) (session.SessionMetadata, error) {
	if execCtx.Store == nil {
		return session.SessionMetadata{}, errors.New("session store is required")
	}
	meta, err := execCtx.Store.LoadMetadata(execCtx.SessionID)
	if err != nil {
		return session.SessionMetadata{}, err
	}
	return meta, nil
}

func isSubAgentSession(meta session.SessionMetadata) bool {
	return meta.Depth > 0 || strings.TrimSpace(meta.ParentSessionID) != ""
}

func defGetPlanMode() Definition {
	return Definition{
		Name:        "get_plan_mode",
		Description: "Read the current session Plan Mode state. Use this in Plan Mode to inspect objective, pending questions, submitted plan version, approval status, and approved plan context. Returns null when Plan Mode is not enabled.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Execute: func(_ context.Context, execCtx ExecContext, _ json.RawMessage) (session.ToolResult, error) {
			if err := requireToolSessionMetadata(execCtx); err != nil {
				if missingSessionMetadata(err) {
					return readOnlyEmptyStateResult("get_plan_mode", "null", "session_metadata_missing", map[string]any{
						"enabled":               false,
						"session_metadata_path": sessionMetadataFilePath(execCtx),
					}), nil
				}
				return errorResult("get_plan_mode", err), nil
			}
			planMode, err := execCtx.Store.LoadPlanMode(execCtx.SessionID)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return session.ToolResult{Name: "get_plan_mode", LLMOutput: "null", DisplayOutput: "null"}, nil
				}
				return errorResult("get_plan_mode", err), nil
			}
			data, _ := json.MarshalIndent(planMode, "", "  ")
			return session.ToolResult{
				Name:          "get_plan_mode",
				LLMOutput:     string(data),
				DisplayOutput: string(data),
				Metadata: map[string]any{
					"path":         filepath.Join(execCtx.Store.SessionDir(execCtx.SessionID), "planmode.json"),
					"plan_mode_id": planMode.PlanModeID,
					"status":       planMode.Status,
				},
			}, nil
		},
	}
}

func defSubmitPlan() Definition {
	return Definition{
		Name:        "submit_plan",
		Description: "Submit the complete Plan Mode plan for user approval. This records the plan and pauses execution; it does not implement the plan.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title":         map[string]any{"type": "string", "description": "Short plan title."},
				"summary":       map[string]any{"type": "string", "description": "Concise summary of the recommended implementation path."},
				"plan_markdown": map[string]any{"type": "string", "description": "Complete Markdown plan with summary, implementation steps, interfaces/data model, verification, risks, and assumptions."},
				"assumptions":   withDescription(stringArraySchema(), "Important assumptions the plan depends on."),
				"risks":         withDescription(stringArraySchema(), "Risks or tradeoffs and expected mitigations."),
				"verification":  withDescription(stringArraySchema(), "Tests, commands, manual checks, or evidence required before completion."),
			},
			"required": []string{"title", "summary", "plan_markdown", "verification"},
		},
		Execute: func(_ context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			var input session.PlanModeSubmitInput
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("submit_plan", err), nil
			}
			input.Source = session.PlanModeSourceTool
			if err := requireToolSessionMetadata(execCtx); err != nil {
				return errorResult("submit_plan", err), nil
			}
			previousPlanMode, err := execCtx.Store.SnapshotPlanMode(execCtx.SessionID)
			if err != nil {
				return errorResult("submit_plan", err), nil
			}
			previousHistory, err := execCtx.Store.LoadPlanModeHistory(execCtx.SessionID)
			if err != nil {
				return errorResult("submit_plan", err), nil
			}
			planMode, err := execCtx.Store.SubmitPlanMode(execCtx.SessionID, input)
			if err != nil {
				return errorResult("submit_plan", err), nil
			}
			if err := emitToolEvent(execCtx, "planmode.plan_submitted", map[string]any{
				"plan_mode_id": planMode.PlanModeID,
				"plan_id":      planMode.PlanID,
				"version":      planMode.PlanVersion,
				"summary":      planMode.Summary,
			}); err != nil {
				if rollbackErr := execCtx.Store.RestorePlanModeSnapshot(execCtx.SessionID, previousPlanMode); rollbackErr != nil {
					return errorResult("submit_plan", fmt.Errorf("restore plan mode after planmode.plan_submitted event failure %v: %w", err, rollbackErr)), nil
				}
				if rollbackErr := execCtx.Store.RestorePlanModeHistory(execCtx.SessionID, previousHistory); rollbackErr != nil {
					return errorResult("submit_plan", fmt.Errorf("restore plan mode history after planmode.plan_submitted event failure %v: %w", err, rollbackErr)), nil
				}
				return errorResult("submit_plan", fmt.Errorf("record planmode.plan_submitted event: %w", err)), nil
			}
			data, _ := json.MarshalIndent(planMode, "", "  ")
			return session.ToolResult{
				Name:          "submit_plan",
				LLMOutput:     string(data),
				DisplayOutput: fmt.Sprintf("Plan submitted for approval (version %d).", planMode.PlanVersion),
				Metadata: map[string]any{
					"planmode":          true,
					"planmode_terminal": "plan_submitted",
					"path":              filepath.Join(execCtx.Store.SessionDir(execCtx.SessionID), "planmode.json"),
					"plan_path":         filepath.Join(execCtx.Store.SessionDir(execCtx.SessionID), "artifacts", "planmode-plan.md"),
					"plan_mode_id":      planMode.PlanModeID,
					"plan_version":      planMode.PlanVersion,
				},
			}, nil
		},
	}
}

func defRequestUserInput() Definition {
	return Definition{
		Name:        "request_user_input",
		Description: "Request user input for one to three short Plan Mode questions and wait for the response. This tool is only available in Plan Mode.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"questions": map[string]any{
					"type":        "array",
					"description": "Questions to show the user. Prefer 1 and do not exceed 3.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":       map[string]any{"type": "string", "description": "Stable identifier for mapping answers (snake_case)."},
							"header":   map[string]any{"type": "string", "description": "Short header label shown in the UI (12 or fewer chars)."},
							"question": map[string]any{"type": "string", "description": "Single-sentence prompt shown to the user."},
							"options": map[string]any{
								"type":        "array",
								"description": "Provide 2-3 mutually exclusive choices. Put the recommended option first and suffix its label with \"(Recommended)\". Do not include an Other option; the client adds free-form Other automatically.",
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"label":       map[string]any{"type": "string", "description": "User-facing label (1-5 words)."},
										"description": map[string]any{"type": "string", "description": "One short sentence explaining impact/tradeoff if selected."},
									},
									"required": []string{"label", "description"},
								},
							},
						},
						"required": []string{"id", "header", "question", "options"},
					},
				},
			},
			"required": []string{"questions"},
		},
		Execute: func(ctx context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			var input struct {
				Questions []session.PlanModeInputQuestion `json:"questions"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("request_user_input", err), nil
			}
			meta, metaErr := execCtx.Store.LoadMetadata(execCtx.SessionID)
			if metaErr != nil {
				return errorResult("request_user_input", metaErr), nil
			}
			if strings.TrimSpace(meta.ParentSessionID) != "" {
				return errorResult("request_user_input", errors.New("request_user_input is only available in the root session")), nil
			}
			if execCtx.PlanInputResponder == nil {
				return errorResult("request_user_input", errors.New("request_user_input requires an interactive responder (TTY or Web API)")), nil
			}
			request := session.PlanModeInputRequest{
				RequestID:  session.NewPlanModeQuestionID(),
				ToolCallID: strings.TrimSpace(execCtx.ToolCallID),
				Questions:  input.Questions,
				Status:     "pending",
				CreatedAt:  time.Now().UTC().Format(time.RFC3339Nano),
			}
			if request.ToolCallID == "" {
				request.ToolCallID = request.RequestID
			}
			previousPendingPlanMode, err := execCtx.Store.SnapshotPlanMode(execCtx.SessionID)
			if err != nil {
				return errorResult("request_user_input", err), nil
			}
			previousPendingHistory, err := execCtx.Store.LoadPlanModeHistory(execCtx.SessionID)
			if err != nil {
				return errorResult("request_user_input", err), nil
			}
			planMode, err := execCtx.Store.SetPlanModePendingRequest(execCtx.SessionID, request, session.PlanModeSourceTool)
			if err != nil {
				return errorResult("request_user_input", err), nil
			}
			state, stateErr := execCtx.Store.LoadState(execCtx.SessionID)
			if stateErr != nil {
				if rollbackErr := execCtx.Store.RestorePlanModeSnapshot(execCtx.SessionID, previousPendingPlanMode); rollbackErr != nil {
					return errorResult("request_user_input", fmt.Errorf("restore plan mode after state load error %v: %w", stateErr, rollbackErr)), nil
				}
				if rollbackErr := execCtx.Store.RestorePlanModeHistory(execCtx.SessionID, previousPendingHistory); rollbackErr != nil {
					return errorResult("request_user_input", fmt.Errorf("restore plan mode history after state load error %v: %w", stateErr, rollbackErr)), nil
				}
				return errorResult("request_user_input", stateErr), nil
			}
			state.Status = session.StatusAwaitingInput
			state.Phase = "plan_input"
			if err := execCtx.Store.SaveState(execCtx.SessionID, state); err != nil {
				if rollbackErr := execCtx.Store.RestorePlanModeSnapshot(execCtx.SessionID, previousPendingPlanMode); rollbackErr != nil {
					return errorResult("request_user_input", fmt.Errorf("restore plan mode after state save error %v: %w", err, rollbackErr)), nil
				}
				if rollbackErr := execCtx.Store.RestorePlanModeHistory(execCtx.SessionID, previousPendingHistory); rollbackErr != nil {
					return errorResult("request_user_input", fmt.Errorf("restore plan mode history after state save error %v: %w", err, rollbackErr)), nil
				}
				return errorResult("request_user_input", err), nil
			}
			if err := emitToolEvent(execCtx, "planmode.input_requested", map[string]any{
				"plan_mode_id": planMode.PlanModeID,
				"request_id":   request.RequestID,
				"questions":    len(request.Questions),
			}); err != nil {
				return pendingPlanInputErrorResult(fmt.Errorf("record planmode.input_requested event: %w", err), request, planMode), nil
			}
			answers, err := execCtx.PlanInputResponder.RequestPlanInput(ctx, execCtx.SessionID, request)
			if err != nil {
				if errors.Is(err, ErrPlanInputCancelled) {
					previousPlanMode, snapshotErr := execCtx.Store.SnapshotPlanMode(execCtx.SessionID)
					if snapshotErr != nil {
						return pendingPlanInputErrorResult(snapshotErr, request, planMode), nil
					}
					previousHistory, historyErr := execCtx.Store.LoadPlanModeHistory(execCtx.SessionID)
					if historyErr != nil {
						return pendingPlanInputErrorResult(historyErr, request, planMode), nil
					}
					planMode, cancelErr := execCtx.Store.CancelPlanMode(execCtx.SessionID, session.PlanModeSourceTool)
					if cancelErr != nil {
						return pendingPlanInputErrorResult(cancelErr, request, planMode), nil
					}
					if eventErr := emitToolEvents(execCtx, []ToolEvent{
						{
							Type: "planmode.input_cancelled",
							Data: map[string]any{
								"plan_mode_id": planMode.PlanModeID,
								"request_id":   request.RequestID,
							},
						},
						{
							Type: "planmode.cancelled",
							Data: map[string]any{
								"plan_mode_id": planMode.PlanModeID,
								"request_id":   request.RequestID,
							},
						},
					}); eventErr != nil {
						if rollbackErr := execCtx.Store.RestorePlanModeSnapshot(execCtx.SessionID, previousPlanMode); rollbackErr != nil {
							return pendingPlanInputErrorResult(fmt.Errorf("restore plan mode after planmode cancellation event failure %v: %w", eventErr, rollbackErr), request, planMode), nil
						}
						if rollbackErr := execCtx.Store.RestorePlanModeHistory(execCtx.SessionID, previousHistory); rollbackErr != nil {
							return pendingPlanInputErrorResult(fmt.Errorf("restore plan mode history after planmode cancellation event failure %v: %w", eventErr, rollbackErr), request, planMode), nil
						}
						return pendingPlanInputErrorResult(fmt.Errorf("record planmode.input_cancelled and planmode.cancelled events: %w", eventErr), request, planMode), nil
					}
					return session.ToolResult{
						Name:          "request_user_input",
						LLMOutput:     "Error: Plan Mode input was cancelled by the user.",
						DisplayOutput: "Error: Plan Mode input was cancelled by the user.",
						IsError:       true,
						Metadata: map[string]any{
							"planmode":          true,
							"planmode_terminal": "plan_cancelled",
							"request_id":        request.RequestID,
							"cancelled":         true,
							"plan_mode_id":      planMode.PlanModeID,
						},
					}, nil
				}
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return session.ToolResult{}, err
				}
				return session.ToolResult{
					Name:          "request_user_input",
					LLMOutput:     "Error: " + err.Error(),
					DisplayOutput: "Error: " + err.Error(),
					IsError:       true,
					Metadata: map[string]any{
						"planmode":           true,
						"plan_input_pending": true,
						"request_id":         request.RequestID,
						"plan_mode_id":       planMode.PlanModeID,
					},
				}, nil
			}
			previousPlanMode, err := execCtx.Store.SnapshotPlanMode(execCtx.SessionID)
			if err != nil {
				return pendingPlanInputErrorResult(err, request, planMode), nil
			}
			previousHistory, err := execCtx.Store.LoadPlanModeHistory(execCtx.SessionID)
			if err != nil {
				return pendingPlanInputErrorResult(err, request, planMode), nil
			}
			planMode, answered, err := execCtx.Store.AnswerPlanModeInput(execCtx.SessionID, request.RequestID, session.PlanModeSourceTool, answers)
			if err != nil {
				return pendingPlanInputErrorResult(err, request, planMode), nil
			}
			if err := emitToolEvent(execCtx, "planmode.input_answered", map[string]any{
				"plan_mode_id": planMode.PlanModeID,
				"request_id":   answered.RequestID,
				"answers":      answers,
			}); err != nil {
				if rollbackErr := execCtx.Store.RestorePlanModeSnapshot(execCtx.SessionID, previousPlanMode); rollbackErr != nil {
					return pendingPlanInputErrorResult(fmt.Errorf("restore plan mode after planmode.input_answered event failure %v: %w", err, rollbackErr), request, planMode), nil
				}
				if rollbackErr := execCtx.Store.RestorePlanModeHistory(execCtx.SessionID, previousHistory); rollbackErr != nil {
					return pendingPlanInputErrorResult(fmt.Errorf("restore plan mode history after planmode.input_answered event failure %v: %w", err, rollbackErr), request, planMode), nil
				}
				return pendingPlanInputErrorResult(fmt.Errorf("record planmode.input_answered event: %w", err), request, planMode), nil
			}
			data, _ := json.Marshal(map[string]any{"answers": answers})
			return session.ToolResult{
				Name:          "request_user_input",
				LLMOutput:     string(data),
				DisplayOutput: string(data),
				Metadata: map[string]any{
					"planmode":     true,
					"request_id":   answered.RequestID,
					"plan_mode_id": planMode.PlanModeID,
				},
			}, nil
		},
	}
}

func defTodoWrite() Definition {
	return Definition{
		Name:        "todo_write",
		Description: "Update the session todo progress ledger for non-trivial multi-step work; skip trivial one-step or purely conversational tasks. This is an append/advance-only ledger, not a plan you rewrite: always resubmit the FULL current list (call todo_read first if unsure of the exact text/order), keeping every existing item in its original position. The ONLY edit allowed to an existing item is advancing its status (pending->in_progress->completed/cancelled). Multiple independent or parallel items may be in_progress at the same time. Existing content text, priority, and order must be preserved exactly: do not reword, reorder, insert, or delete existing items. To reword, re-scope, reprioritize, or split an item, append a NEW item to the end instead of editing the existing one. New items must start as pending or in_progress, and completed/cancelled items stay in the list unchanged. This tool only records progress; it does not perform or verify the work.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"todos": map[string]any{
					"type":        "array",
					"description": "Full current snapshot of the session todo list, resubmitted every call. Repeat all existing items verbatim in their original order (same content text and priority); only advance their status or append new items at the end. Never reword, reorder, or drop an existing item — append a new item instead.",
					"items":       todoItemSchema(),
				},
			},
			"required":             []string{"todos"},
			"additionalProperties": false,
		},
		Execute: func(_ context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			var input struct {
				Todos []session.TodoItem `json:"todos"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("todo_write", err), nil
			}
			now := time.Now().UTC().Format(time.RFC3339Nano)
			for i := range input.Todos {
				if input.Todos[i].UpdatedAt == "" {
					input.Todos[i].UpdatedAt = now
				}
			}
			if err := validateTodoSnapshot(input.Todos); err != nil {
				return errorResult("todo_write", err), nil
			}
			if err := requireToolSessionMetadata(execCtx); err != nil {
				return errorResult("todo_write", err), nil
			}
			existing, err := execCtx.Store.LoadTodo(execCtx.SessionID)
			if err != nil {
				return errorResult("todo_write", err), nil
			}
			if err := validateTodoProgressUpdate(existing, input.Todos); err != nil {
				return errorResult("todo_write", err), nil
			}
			changed := !normalizedTodosEqual(existing, input.Todos)
			if !changed {
				if err := emitToolEvent(execCtx, "todo.updated", map[string]any{
					"count":   len(existing),
					"changed": false,
					"noop":    true,
				}); err != nil {
					return errorResult("todo_write", fmt.Errorf("record todo.updated event: %w", err)), nil
				}
				data, _ := json.MarshalIndent(existing, "", "  ")
				return session.ToolResult{
					Name:          "todo_write",
					LLMOutput:     string(data),
					DisplayOutput: string(data),
					Metadata: map[string]any{
						"path":    todoFilePath(execCtx),
						"count":   len(existing),
						"changed": false,
						"noop":    true,
					},
				}, nil
			}
			if err := execCtx.Store.SaveTodo(execCtx.SessionID, input.Todos); err != nil {
				return errorResult("todo_write", err), nil
			}
			if err := emitToolEvent(execCtx, "todo.updated", map[string]any{
				"count":   len(input.Todos),
				"changed": true,
				"noop":    false,
			}); err != nil {
				if rollbackErr := execCtx.Store.SaveTodo(execCtx.SessionID, existing); rollbackErr != nil {
					return errorResult("todo_write", fmt.Errorf("restore todo after todo.updated event failure %v: %w", err, rollbackErr)), nil
				}
				return errorResult("todo_write", fmt.Errorf("record todo.updated event: %w", err)), nil
			}
			data, _ := json.MarshalIndent(input.Todos, "", "  ")
			return session.ToolResult{
				Name:          "todo_write",
				LLMOutput:     string(data),
				DisplayOutput: string(data),
				Metadata: map[string]any{
					"path":    todoFilePath(execCtx),
					"count":   len(input.Todos),
					"changed": true,
					"noop":    false,
				},
			}, nil
		},
	}
}

func defTodoRead() Definition {
	return Definition{
		Name:        "todo_read",
		Description: "Read the current session todo list. Use before updating todos when resuming, avoiding duplicates, checking pending work, or answering progress questions. Returns an empty list if no todos exist.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		Execute: func(_ context.Context, execCtx ExecContext, _ json.RawMessage) (session.ToolResult, error) {
			if err := requireToolSessionMetadata(execCtx); err != nil {
				if missingSessionMetadata(err) {
					return readOnlyEmptyStateResult("todo_read", "[]", "session_metadata_missing", map[string]any{
						"count":                 0,
						"session_metadata_path": sessionMetadataFilePath(execCtx),
					}), nil
				}
				return errorResult("todo_read", err), nil
			}
			todo, err := execCtx.Store.LoadTodo(execCtx.SessionID)
			if err != nil {
				return errorResult("todo_read", err), nil
			}
			data, _ := json.MarshalIndent(todo, "", "  ")
			return session.ToolResult{
				Name:          "todo_read",
				LLMOutput:     string(data),
				DisplayOutput: string(data),
				Metadata: map[string]any{
					"path":  todoFilePath(execCtx),
					"count": len(todo),
				},
			}, nil
		},
	}
}

type readRepeatObservation struct {
	Count       int
	FirstSeenAt string
	LastSeenAt  string
}

func readFileRepeatObservation(execCtx ExecContext, path string, offset, end int) readRepeatObservation {
	if execCtx.Store == nil || strings.TrimSpace(execCtx.SessionID) == "" {
		return readRepeatObservation{Count: 1}
	}
	messages, err := execCtx.Store.LoadMessages(execCtx.SessionID)
	if err != nil {
		return readRepeatObservation{Count: 1}
	}
	obs := readRepeatObservation{Count: 1}
	for _, msg := range messages {
		for _, result := range msg.ToolResults {
			if result.Name != "read_file" {
				continue
			}
			if strings.TrimSpace(metadataString(result.Metadata, "path")) != strings.TrimSpace(path) {
				continue
			}
			if metadataInt(result.Metadata, "offset") != offset || metadataInt(result.Metadata, "end") != end {
				continue
			}
			obs.Count++
			if obs.FirstSeenAt == "" {
				obs.FirstSeenAt = msg.CreatedAt
			}
			obs.LastSeenAt = msg.CreatedAt
		}
	}
	return obs
}

func skillLoaded(execCtx ExecContext, name string) bool {
	if execCtx.Store == nil || strings.TrimSpace(execCtx.SessionID) == "" {
		return false
	}
	state, err := execCtx.Store.LoadState(execCtx.SessionID)
	if err != nil {
		return false
	}
	for _, loaded := range state.LoadedSkills {
		if loaded == name {
			return true
		}
	}
	return false
}

func markSkillLoaded(execCtx ExecContext, name string) error {
	if execCtx.Store == nil || strings.TrimSpace(execCtx.SessionID) == "" {
		return nil
	}
	state, err := execCtx.Store.LoadState(execCtx.SessionID)
	if err != nil {
		return err
	}
	for _, loaded := range state.LoadedSkills {
		if loaded == name {
			return nil
		}
	}
	state.LoadedSkills = append(state.LoadedSkills, name)
	return execCtx.Store.SaveState(execCtx.SessionID, state)
}

func validateTodoSnapshot(todos []session.TodoItem) error {
	for i, item := range todos {
		if strings.TrimSpace(item.Content) == "" {
			return fmt.Errorf("todo item %d content is required", i+1)
		}
		switch item.Status {
		case "pending", "in_progress", "completed", "cancelled":
		default:
			if strings.TrimSpace(item.Status) == "" {
				return fmt.Errorf("todo item %d status is required", i+1)
			}
			return fmt.Errorf("invalid todo status: %s", item.Status)
		}
		switch item.Priority {
		case "", "high", "medium", "low":
		default:
			return fmt.Errorf("invalid todo priority: %s", item.Priority)
		}
		if strings.TrimSpace(item.UpdatedAt) == "" {
			return fmt.Errorf("todo item %d updated_at is required", i+1)
		}
		if _, err := time.Parse(time.RFC3339Nano, item.UpdatedAt); err != nil {
			return fmt.Errorf("todo item %d updated_at must be RFC3339Nano: %w", i+1, err)
		}
	}
	return nil
}

func validateTodoProgressUpdate(existing, next []session.TodoItem) error {
	if len(next) < len(existing) {
		return fmt.Errorf("todo_write must preserve existing todo items: got %d items, existing list has %d. Call todo_read, resubmit every existing item unchanged in its original order, then append or advance from there", len(next), len(existing))
	}
	for i, old := range existing {
		updated := next[i]
		if old.Content != updated.Content {
			return fmt.Errorf("todo_write cannot reword existing todo %d (%q); keep its content text unchanged and, if it needs new wording or scope, append a new todo at the end instead", i+1, old.Content)
		}
		if old.Priority != updated.Priority {
			return fmt.Errorf("todo_write cannot change existing todo %d priority (%q->%q); keep the original priority and append a new todo if the plan changed", i+1, old.Priority, updated.Priority)
		}
		if !validTodoStatusTransition(old.Status, updated.Status) {
			return fmt.Errorf("todo_write cannot change existing todo %d status from %s to %s; allowed transitions are pending->in_progress->completed/cancelled and terminal states cannot be reopened", i+1, old.Status, updated.Status)
		}
	}
	for i := len(existing); i < len(next); i++ {
		if next[i].Status == "completed" || next[i].Status == "cancelled" {
			return fmt.Errorf("todo_write cannot add new todo %d directly as %s; add it as pending or in_progress, then mark it done after the work", i+1, next[i].Status)
		}
	}
	return nil
}

func validTodoStatusTransition(from, to string) bool {
	if from == to {
		return true
	}
	switch from {
	case "pending":
		return to == "in_progress" || to == "completed" || to == "cancelled"
	case "in_progress":
		return to == "completed" || to == "cancelled"
	case "completed", "cancelled":
		return false
	default:
		return false
	}
}

func normalizedTodosEqual(a, b []session.TodoItem) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Content != b[i].Content || a[i].Status != b[i].Status || a[i].Priority != b[i].Priority {
			return false
		}
	}
	return true
}

func metadataString(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return value
}

func metadataInt(metadata map[string]any, key string) int {
	switch value := metadata[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func defTaskCreate() Definition {
	return Definition{
		Name:        "task_create",
		Description: "Create a durable task-graph node for long-running, dependent, or resumable work. Use this when a task needs dependency tracking or handoff beyond the short session todo list; do not use it for trivial single-step work. The runtime maintains IDs, dependency edges, and cycle checks.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"subject":     map[string]any{"type": "string", "description": "Short task title."},
				"description": map[string]any{"type": "string", "description": "Optional task detail, expected output, or acceptance notes."},
				"priority":    map[string]any{"type": "string", "enum": []string{"high", "medium", "low"}, "description": "Optional priority: high, medium, or low."},
				"blocked_by":  withDescription(stringArraySchema(), "Optional task IDs that must complete before this task is ready."),
				"labels":      withDescription(stringArraySchema(), "Optional grouping labels such as provider, docs, or validation."),
			},
			"required": []string{"subject"},
		},
		Execute: func(_ context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			var input session.TaskCreateInput
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("task_create", err), nil
			}
			if err := requireToolSessionMetadata(execCtx); err != nil {
				return errorResult("task_create", err), nil
			}
			existing, err := execCtx.Store.ListTasks(execCtx.SessionID)
			if err != nil {
				return errorResult("task_create", err), nil
			}
			task, err := session.CreateTask(execCtx.Store, execCtx.SessionID, input)
			if err != nil {
				return errorResult("task_create", err), nil
			}
			if err := emitToolEvent(execCtx, "task.created", map[string]any{
				"task_id": task.ID,
				"status":  task.Status,
			}); err != nil {
				if rollbackErr := execCtx.Store.SaveTasks(execCtx.SessionID, existing); rollbackErr != nil {
					return errorResult("task_create", fmt.Errorf("restore tasks after task.created event failure %v: %w", err, rollbackErr)), nil
				}
				return errorResult("task_create", fmt.Errorf("record task.created event: %w", err)), nil
			}
			data, _ := json.MarshalIndent(task, "", "  ")
			return session.ToolResult{
				Name:          "task_create",
				LLMOutput:     string(data),
				DisplayOutput: string(data),
				Metadata: map[string]any{
					"path":        taskFilePath(execCtx, task.ID),
					"task_id":     task.ID,
					"session_dir": execCtx.Store.SessionDir(execCtx.SessionID),
				},
			}, nil
		},
	}
}

func defTaskUpdate() Definition {
	return Definition{
		Name:        "task_update",
		Description: "Update a durable task-graph node, including status, dependency edges, owner, or notes. Use this as long work progresses so resume and handoff state stays fresh. Mark completed only after the task is actually done; dependency edges are kept consistent by the runtime.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id":           map[string]any{"type": "string", "description": "Task ID to update, for example task_0001."},
				"status":            map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed", "cancelled"}, "description": "Optional new task status."},
				"subject":           map[string]any{"type": "string", "description": "Optional replacement task title."},
				"description":       map[string]any{"type": "string", "description": "Optional replacement task detail."},
				"priority":          map[string]any{"type": "string", "enum": []string{"high", "medium", "low"}, "description": "Optional priority."},
				"owner":             map[string]any{"type": "string", "description": "Optional owner or role hint for handoff."},
				"add_blocked_by":    withDescription(stringArraySchema(), "Task IDs to add as blockers for this task."),
				"remove_blocked_by": withDescription(stringArraySchema(), "Task IDs to remove from this task's blockers."),
				"add_blocks":        withDescription(stringArraySchema(), "Task IDs that this task should block."),
				"remove_blocks":     withDescription(stringArraySchema(), "Task IDs that this task should stop blocking."),
				"append_note":       map[string]any{"type": "string", "description": "Optional note to append without replacing existing notes."},
			},
			"required": []string{"task_id"},
		},
		Execute: func(_ context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			var input session.TaskUpdateInput
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("task_update", err), nil
			}
			if err := requireToolSessionMetadata(execCtx); err != nil {
				return errorResult("task_update", err), nil
			}
			existing, err := execCtx.Store.ListTasks(execCtx.SessionID)
			if err != nil {
				return errorResult("task_update", err), nil
			}
			task, err := session.UpdateTask(execCtx.Store, execCtx.SessionID, input)
			if err != nil {
				return errorResult("task_update", err), nil
			}
			if err := emitToolEvent(execCtx, "task.updated", map[string]any{
				"task_id": task.ID,
				"status":  task.Status,
			}); err != nil {
				if rollbackErr := execCtx.Store.SaveTasks(execCtx.SessionID, existing); rollbackErr != nil {
					return errorResult("task_update", fmt.Errorf("restore tasks after task.updated event failure %v: %w", err, rollbackErr)), nil
				}
				return errorResult("task_update", fmt.Errorf("record task.updated event: %w", err)), nil
			}
			data, _ := json.MarshalIndent(task, "", "  ")
			return session.ToolResult{
				Name:          "task_update",
				LLMOutput:     string(data),
				DisplayOutput: string(data),
				Metadata: map[string]any{
					"path":        taskFilePath(execCtx, task.ID),
					"task_id":     task.ID,
					"session_dir": execCtx.Store.SessionDir(execCtx.SessionID),
				},
			}, nil
		},
	}
}

func defTaskList() Definition {
	return Definition{
		Name:        "task_list",
		Description: "List the durable task graph and derived ready, blocked, completed, cancelled, and done views. Use this when resuming long work, choosing the next ready task, or reconciling handoff state. This is not a substitute for checking current files or validation results.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"include_completed": map[string]any{
					"type":        "boolean",
					"description": "Whether to include completed and cancelled tasks in the returned task array when status is omitted. Defaults to true so recovery sees the full durable graph.",
				},
				"status": map[string]any{
					"type":        "string",
					"enum":        []any{"pending", "in_progress", "completed", "cancelled", "ready", "blocked", "done"},
					"description": "Optional task status or derived group to return. Explicit status filters override include_completed.",
				},
			},
			"additionalProperties": false,
		},
		Execute: func(_ context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			input, err := decodeTaskListInput(raw)
			if err != nil {
				return errorResult("task_list", err), nil
			}
			if err := requireToolSessionMetadata(execCtx); err != nil {
				return errorResult("task_list", err), nil
			}
			todo, err := execCtx.Store.LoadTodo(execCtx.SessionID)
			if err != nil {
				return errorResult("task_list", err), nil
			}
			tasks, err := execCtx.Store.ListTasks(execCtx.SessionID)
			if err != nil {
				return errorResult("task_list", err), nil
			}
			board := session.BuildTaskBoard(todo, tasks)
			visibleTasks, err := filterTaskListTasks(tasks, board, input)
			if err != nil {
				return errorResult("task_list", err), nil
			}
			responseBoard := session.BuildTaskBoard(todo, visibleTasks)
			data, _ := json.MarshalIndent(responseBoard, "", "  ")
			return session.ToolResult{
				Name:          "task_list",
				LLMOutput:     string(data),
				DisplayOutput: string(data),
				Metadata: map[string]any{
					"tasks_dir":           taskDirPath(execCtx),
					"session_dir":         execCtx.Store.SessionDir(execCtx.SessionID),
					"todo_count":          len(todo),
					"task_count":          len(tasks),
					"filtered_task_count": len(visibleTasks),
					"include_completed":   input.includeCompleted(),
					"filter_status":       input.Status,
					"ready_count":         board.Counters["ready"],
					"blocked_count":       board.Counters["blocked"],
					"completed_count":     board.Counters["completed"],
					"cancelled_count":     board.Counters["cancelled"],
					"done_count":          board.Counters["done"],
				},
			}, nil
		},
	}
}

type taskListInput struct {
	IncludeCompleted *bool  `json:"include_completed,omitempty"`
	Status           string `json:"status,omitempty"`
}

func (input taskListInput) includeCompleted() bool {
	if input.IncludeCompleted == nil {
		return true
	}
	return *input.IncludeCompleted
}

func decodeTaskListInput(raw json.RawMessage) (taskListInput, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var input taskListInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return taskListInput{}, err
	}
	status := strings.TrimSpace(input.Status)
	if status == "" {
		input.Status = ""
		return input, nil
	}
	switch status {
	case "pending", "in_progress", "completed", "cancelled", "ready", "blocked", "done":
		input.Status = status
		return input, nil
	default:
		return taskListInput{}, fmt.Errorf("invalid status: %s", input.Status)
	}
}

func filterTaskListTasks(tasks []session.Task, board session.TaskBoard, input taskListInput) ([]session.Task, error) {
	if input.Status != "" {
		switch input.Status {
		case "ready", "blocked", "done":
			return append([]session.Task{}, board.Groups[input.Status]...), nil
		case "pending", "in_progress", "completed", "cancelled":
			return filterTasksByStatus(tasks, input.Status), nil
		default:
			return nil, fmt.Errorf("invalid status: %s", input.Status)
		}
	}
	if input.includeCompleted() {
		return append([]session.Task{}, tasks...), nil
	}
	visible := make([]session.Task, 0, len(tasks))
	for _, task := range tasks {
		if task.Status == "completed" || task.Status == "cancelled" {
			continue
		}
		visible = append(visible, task)
	}
	return visible, nil
}

func filterTasksByStatus(tasks []session.Task, status string) []session.Task {
	filtered := make([]session.Task, 0, len(tasks))
	for _, task := range tasks {
		if task.Status == status {
			filtered = append(filtered, task)
		}
	}
	return filtered
}

func defTaskGet() Definition {
	return Definition{
		Name:        "task_get",
		Description: "Read one durable task node by ID. Use this when task_list shows a task that needs detail before updating, implementing, or summarizing it.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string", "description": "Task ID to read, for example task_0001."},
			},
			"required": []string{"task_id"},
		},
		Execute: func(_ context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			var input struct {
				TaskID string `json:"task_id"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("task_get", err), nil
			}
			if err := requireToolSessionMetadata(execCtx); err != nil {
				return errorResult("task_get", err), nil
			}
			task, err := execCtx.Store.GetTask(execCtx.SessionID, input.TaskID)
			if err != nil {
				return errorResult("task_get", err), nil
			}
			data, _ := json.MarshalIndent(task, "", "  ")
			return session.ToolResult{
				Name:          "task_get",
				LLMOutput:     string(data),
				DisplayOutput: string(data),
				Metadata: map[string]any{
					"path":        taskFilePath(execCtx, task.ID),
					"task_id":     task.ID,
					"session_dir": execCtx.Store.SessionDir(execCtx.SessionID),
				},
			}, nil
		},
	}
}

func todoFilePath(execCtx ExecContext) string {
	return filepath.Join(execCtx.Store.SessionDir(execCtx.SessionID), "todo.json")
}

func taskDirPath(execCtx ExecContext) string {
	return filepath.Join(execCtx.Store.SessionDir(execCtx.SessionID), "tasks")
}

func taskFilePath(execCtx ExecContext, taskID string) string {
	return filepath.Join(taskDirPath(execCtx), taskID+".json")
}

func emitToolEvent(execCtx ExecContext, eventType string, data map[string]any) error {
	if execCtx.EmitRequired != nil {
		return execCtx.EmitRequired(eventType, data)
	}
	if execCtx.Emit != nil {
		execCtx.Emit(eventType, data)
	}
	return nil
}

func emitToolEvents(execCtx ExecContext, items []ToolEvent) error {
	if len(items) == 0 {
		return nil
	}
	if execCtx.EmitBatchRequired != nil {
		return execCtx.EmitBatchRequired(items)
	}
	for _, item := range items {
		if err := emitToolEvent(execCtx, item.Type, item.Data); err != nil {
			return err
		}
	}
	return nil
}

func defAgentSpawn(control ControlPlane) Definition {
	return Definition{
		Name:        "agent_spawn",
		Description: "Spawn a child agent when the model decides delegation would improve coverage, independence, or context control. Consider this for broad investigations, separable long-running slices, code audits, module scans, independent validation, or reviewer/evaluator passes; keep tiny single-file checks in the parent. Delegation is optional and model-led. For open-ended, cross-module exploration where raw search output is much larger than the final evidence, an explorer can provide context isolation; use synchronous spawn or background plus agent_wait when preserving the parent context matters, and avoid repeating exploration already covered by the child. Child sessions and background jobs are durable facts, and their results should be reconciled before final parent conclusions. Choose agent_role directly when role-specific Settings provider overrides should apply. Use isolation_mode=auto when a non-read-only child must write artifacts.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt": map[string]any{
					"type":        "string",
					"description": "Self-contained child task prompt. Include objective, scope, boundaries, inputs, expected output, completion criteria, and any inherited rubric. The parent should preserve synthesis and final decisions.",
				},
				"agent_name": map[string]any{
					"type":        "string",
					"description": "Short human-readable child label such as audit-auth-slice, scan-api-module, or reviewer-routing.",
				},
				"agent_role": map[string]any{
					"type":        "string",
					"enum":        []string{"planner", "generator", "evaluator", "explorer"},
					"description": "Optional child role hint. Choose exactly one of planner, generator, or evaluator for planning, implementation, or review; choose explorer for a read-only evidence handoff when raw search volume warrants context isolation. Omit provider/model to use the configured default for the chosen role.",
				},
				"provider": map[string]any{
					"type":        "string",
					"description": "Optional provider override. Omit or use default to inherit the current session provider.",
				},
				"model": map[string]any{
					"type":        "string",
					"description": "Optional model override. Omit or use default to inherit the current session model.",
				},
				"workdir": map[string]any{
					"type":        "string",
					"description": "Optional child working directory. Omit to inherit the parent workspace.",
				},
				"system": map[string]any{
					"type":        "string",
					"description": "Optional child system instruction override. Usually omit so the child inherits the runtime contract.",
				},
				"mode": map[string]any{
					"type":        "string",
					"enum":        []string{"run", "exec", "full-auto", "default"},
					"description": "Optional run mode. full-auto is accepted as an alias for exec.",
				},
				"background": map[string]any{
					"type":        "boolean",
					"description": "Set true for independent or long-running delegated slices when the parent can continue non-overlapping work, then collect results later with agent_status or agent_list.",
				},
				"wait_mode": map[string]any{
					"type":        "string",
					"enum":        []string{"wait-all", "wait-any", "all", "any", "default"},
					"description": "Optional parent coordination mode for background or child work. default/all means parent finish waits for all unresolved work; any allows finish after one completed result while keeping other work visible.",
				},
				"resume_parent": map[string]any{
					"type":        "boolean",
					"description": "Only meaningful with background=true. Set true when the parent should park after this tool result and automatically resume when this background child reports completed, failed, or blocked results.",
				},
				"isolation_mode": map[string]any{
					"type":        "string",
					"enum":        []string{"auto", "copy", "git", "off", "none", "workspace-write", "default"},
					"description": "Optional isolation mode. workspace-write is accepted as an alias for off.",
				},
				"isolation_root": map[string]any{
					"type":        "string",
					"description": "Optional base directory for copy/git isolation workspaces.",
				},
			},
			"required": []string{"prompt"},
		},
		Execute: func(ctx context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			if control == nil {
				return errorResult("agent_spawn", errors.New("agent control plane is not available")), nil
			}
			var input AgentSpawnRequest
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("agent_spawn", err), nil
			}
			if strings.TrimSpace(input.Prompt) == "" {
				return errorResult("agent_spawn", errors.New("prompt is required")), nil
			}
			meta, err := loadToolSessionMetadata(execCtx)
			if err != nil {
				return errorResult("agent_spawn", err), nil
			}
			if isSubAgentSession(meta) {
				return errorResult("agent_spawn", errors.New("nested sub-agents are not allowed; only the root master session can create sub-agents")), nil
			}
			input.ParentSessionID = execCtx.SessionID
			result, err := control.SpawnAgent(ctx, input)
			if err != nil {
				return errorResult("agent_spawn", err), nil
			}
			data, _ := json.MarshalIndent(result, "", "  ")
			metadata := map[string]any{}
			if input.Background && input.ResumeParent {
				metadata["background_wait"] = true
				metadata["queue_job_id"] = result.QueueJobID
			}
			return session.ToolResult{Name: "agent_spawn", LLMOutput: string(data), DisplayOutput: string(data), Metadata: metadata}, nil
		},
	}
}

func defAgentStatus(control ControlPlane) Definition {
	return Definition{
		Name:        "agent_status",
		Description: "Check current or final child-agent/background-job status after agent_spawn. Use this to collect final_text, last_error, session_status, and effective workdir before parent synthesis or recovery.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id": map[string]any{
					"type":        "string",
					"description": "Child session id returned by agent_spawn.",
				},
				"queue_job_id": map[string]any{
					"type":        "string",
					"description": "Background queue job id returned by agent_spawn(background=true).",
				},
			},
		},
		Execute: func(ctx context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			if control == nil {
				return errorResult("agent_status", errors.New("agent control plane is not available")), nil
			}
			var input AgentStatusRequest
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("agent_status", err), nil
			}
			if strings.TrimSpace(input.SessionID) == "" && strings.TrimSpace(input.QueueJobID) == "" {
				return errorResult("agent_status", errors.New("session_id or queue_job_id is required")), nil
			}
			if err := requireToolSessionMetadata(execCtx); err != nil {
				return errorResult("agent_status", err), nil
			}
			input.ParentSessionID = execCtx.SessionID
			result, err := control.AgentStatus(ctx, input)
			if err != nil {
				return errorResult("agent_status", err), nil
			}
			data, _ := json.MarshalIndent(result, "", "  ")
			return session.ToolResult{Name: "agent_status", LLMOutput: string(data), DisplayOutput: string(data)}, nil
		},
	}
}

func defAgentWait(control ControlPlane) Definition {
	return Definition{
		Name:        "agent_wait",
		Description: "Park the parent agent until any background child job reports a durable result, then automatically resume the parent with that result in context. Use this when unresolved background work is required before the parent can proceed or exit; after resuming, the model decides whether to continue waiting. This does not cancel or stop child work. queue_job_id is optional and kept only as a compatibility hint; it does not restrict which background result wakes the parent.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"queue_job_id": map[string]any{
					"type":        "string",
					"description": "Optional compatibility hint for a background queue job id returned by agent_spawn(background=true) or agent_list. The parent still wakes on any pending background result.",
				},
			},
		},
		Execute: func(ctx context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			var input AgentWaitRequest
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("agent_wait", err), nil
			}
			if err := requireToolSessionMetadata(execCtx); err != nil {
				return errorResult("agent_wait", err), nil
			}
			queueJobID := strings.TrimSpace(input.QueueJobID)
			if control != nil && queueJobID != "" {
				input.ParentSessionID = execCtx.SessionID
				if _, err := control.AgentStatus(ctx, AgentStatusRequest{ParentSessionID: input.ParentSessionID, QueueJobID: queueJobID}); err != nil {
					return errorResult("agent_wait", err), nil
				}
			}
			output := map[string]any{
				"background_wait": true,
				"wake_on":         "any_background_result",
			}
			metadata := map[string]any{
				"background_wait": true,
			}
			if queueJobID != "" {
				output["queue_job_id"] = queueJobID
				metadata["queue_job_id"] = queueJobID
			}
			data, _ := json.MarshalIndent(output, "", "  ")
			return session.ToolResult{
				Name:          "agent_wait",
				LLMOutput:     string(data),
				DisplayOutput: string(data),
				Metadata:      metadata,
			}, nil
		},
	}
}

func defAgentStop(control ControlPlane) Definition {
	return Definition{
		Name:        "agent_stop",
		Description: "Cancel child work owned by the current parent. Target by session_id or queue_job_id. Queued jobs become cancelled immediately; running children first receive a durable cancel request and cooperative provider/tool/shell cancellation; budget-paused jobs can be settled as cancelled while preserving the child's paused budget evidence. Cancellation is distinct from execution failure.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id": map[string]any{
					"type":        "string",
					"description": "Child session id returned by agent_spawn, agent_status, or agent_list. Provide session_id or queue_job_id.",
				},
				"queue_job_id": map[string]any{
					"type":        "string",
					"description": "Background job id returned by agent_spawn(background=true), agent_status, or agent_list. Provide queue_job_id or session_id.",
				},
			},
		},
		Execute: func(ctx context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			if control == nil {
				return errorResult("agent_stop", errors.New("agent control plane is not available")), nil
			}
			var input AgentStopRequest
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("agent_stop", err), nil
			}
			if strings.TrimSpace(input.SessionID) == "" && strings.TrimSpace(input.QueueJobID) == "" {
				return errorResult("agent_stop", errors.New("session_id or queue_job_id is required")), nil
			}
			if err := requireToolSessionMetadata(execCtx); err != nil {
				return errorResult("agent_stop", err), nil
			}
			input.ParentSessionID = execCtx.SessionID
			result, err := control.StopAgent(ctx, input)
			if err != nil {
				return errorResult("agent_stop", err), nil
			}
			data, _ := json.MarshalIndent(result, "", "  ")
			return session.ToolResult{Name: "agent_stop", LLMOutput: string(data), DisplayOutput: string(data)}, nil
		},
	}
}

func defAgentPrompt(control ControlPlane) Definition {
	return Definition{
		Name:        "agent_prompt",
		Description: "Send a prompt/steer to a child agent or background child job owned by the current parent session. Use this to refine scope, add evidence requirements, request a progress update, ask for a handoff, or redirect a sub-agent before waiting or synthesizing. For a child paused because the parent was stopped, this restarts the child with the prompt; for a pre-claim job blocked by parent stop, this requeues it. A budget-paused child requires budget_extension that adds or clears the exhausted dimension; that explicit extension starts the next budget attempt. This does not mark child work complete or require it to stop. interrupt defaults to false; set interrupt=true only when the child should be preempted at the next best-effort boundary.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id": map[string]any{
					"type":        "string",
					"description": "Child session id returned by agent_spawn, agent_status, or agent_list. Provide either session_id or queue_job_id.",
				},
				"queue_job_id": map[string]any{
					"type":        "string",
					"description": "Background queue job id returned by agent_spawn(background=true), agent_status, or agent_list. Provide either session_id or queue_job_id.",
				},
				"message": map[string]any{
					"type":        "string",
					"description": "Prompt to send to the child. Be specific about the desired scope, artifact, stop condition, or evidence to deliver.",
				},
				"interrupt": map[string]any{
					"type":        "boolean",
					"description": "Whether to request best-effort interruption of the child run. Defaults to false.",
				},
				"budget_extension": budgetExtensionSchema(),
			},
			"required": []string{"message"},
		},
		Execute: func(ctx context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			if control == nil {
				return errorResult("agent_prompt", errors.New("agent control plane is not available")), nil
			}
			var input AgentPromptRequest
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("agent_prompt", err), nil
			}
			if strings.TrimSpace(input.SessionID) == "" && strings.TrimSpace(input.QueueJobID) == "" {
				return errorResult("agent_prompt", errors.New("session_id or queue_job_id is required")), nil
			}
			if strings.TrimSpace(input.Message) == "" {
				return errorResult("agent_prompt", errors.New("message is required")), nil
			}
			if err := requireToolSessionMetadata(execCtx); err != nil {
				return errorResult("agent_prompt", err), nil
			}
			input.ParentSessionID = execCtx.SessionID
			result, err := control.PromptAgent(ctx, input)
			if err != nil {
				return errorResult("agent_prompt", err), nil
			}
			data, _ := json.MarshalIndent(result, "", "  ")
			return session.ToolResult{Name: "agent_prompt", LLMOutput: string(data), DisplayOutput: string(data)}, nil
		},
	}
}

func defAgentList(control ControlPlane) Definition {
	return Definition{
		Name:        "agent_list",
		Description: "List child agents and associated background jobs for the current parent session. Use this to recover delegated work, find unresolved outputs, and decide what still needs reconciliation before summarizing.",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
		Execute: func(ctx context.Context, execCtx ExecContext, _ json.RawMessage) (session.ToolResult, error) {
			if control == nil {
				return errorResult("agent_list", errors.New("agent control plane is not available")), nil
			}
			if err := requireToolSessionMetadata(execCtx); err != nil {
				return errorResult("agent_list", err), nil
			}
			result, err := control.AgentList(ctx, execCtx.SessionID)
			if err != nil {
				return errorResult("agent_list", err), nil
			}
			data, _ := json.MarshalIndent(result, "", "  ")
			return session.ToolResult{Name: "agent_list", LLMOutput: string(data), DisplayOutput: string(data)}, nil
		},
	}
}

func commandToolDefinition(cfg *config.Config, tool skills.CommandTool) Definition {
	description := strings.TrimSpace(tool.Description)
	if description == "" {
		description = fmt.Sprintf("Skill command tool from skill %s.", tool.SkillName)
	}
	skillDir := filepath.Dir(tool.SkillPath)
	inputSchema := closeObjectSchemas(tool.InputSchema)
	commandMetadata := func(timeout int, sandboxStatus string, exitCode, rawLength int, truncated bool) map[string]any {
		if strings.TrimSpace(sandboxStatus) == "" {
			sandboxStatus = effectiveSandboxStatus(cfg)
		}
		return map[string]any{
			"skill_name": tool.SkillName,
			"skill_path": tool.SkillPath,
			"workdir":    skillDir,
			"sandbox":    sandboxStatus,
			"timeout":    timeout,
			"exit_code":  exitCode,
			"raw_length": rawLength,
			"truncated":  truncated,
		}
	}
	return Definition{
		Name:            tool.Name,
		Description:     fmt.Sprintf("Direct-call skill command tool from skill %s. Call this tool directly by name; do not search the workspace, skill files, or shell PATH for it. This tool executes from the skill directory. %s", tool.SkillName, description),
		InputSchema:     inputSchema,
		SkipInputCheck:  true,
		Ephemeral:       true,
		EphemeralWindow: 2,
		Execute: func(ctx context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			args, err := decodeCommandToolArgs(raw)
			if err != nil {
				return errorResult(tool.Name, err), nil
			}
			if err := validateCommandToolInput(inputSchema, args); err != nil {
				return errorResult(tool.Name, err), nil
			}
			argv, err := renderCommand(tool.Command, args)
			if err != nil {
				return errorResult(tool.Name, err), nil
			}
			effectiveConfig := execCtx.Config
			if effectiveConfig == nil {
				effectiveConfig = cfg
			}
			if effectiveConfig == nil {
				effectiveConfig = config.Default()
			}
			callCtx := ctx
			var cancel context.CancelFunc
			timeout := effectiveToolTimeout(effectiveConfig.Runtime.CommandTimeoutSec, tool.TimeoutSec)
			if timeout > 0 {
				callCtx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
				defer cancel()
			}
			commandText := strings.Join(argv, " ")
			policyMode := effectiveExecPolicyMode(effectiveConfig)
			policyViolations := DetectExecPolicyViolations(commandText)
			policyMetadata := execPolicyMetadata(policyMode, policyViolations)
			shellSandbox := effectiveConfig.Runtime.Shell.Sandbox
			stableDir, commandDir, err := openStableCommandWorkdir(skillDir)
			if err != nil {
				return errorResult(tool.Name, err), nil
			}
			defer func() {
				_ = stableDir.Close()
			}()
			sandboxSource, sandboxExtraFiles := commandWorkdirSandboxSource(stableDir, skillDir)
			commandPath, commandArgs, sandboxStatus, sandboxErr := sandboxCommand(shellSandbox, skillDir, sandboxSource, argv)
			if policyMode == "deny" && len(policyViolations) > 0 {
				text := "Error: skill command denied by exec policy"
				return session.ToolResult{
					Name:          tool.Name,
					LLMOutput:     text,
					DisplayOutput: text,
					IsError:       true,
					Metadata:      attachExecPolicyMetadata(commandMetadata(timeout, sandboxStatus, 0, 0, false), policyMetadata),
				}, nil
			}
			if sandboxErr != nil {
				text := "Error: " + sandboxErr.Error()
				metadata := commandMetadata(timeout, sandboxStatus, 0, 0, false)
				return session.ToolResult{
					Name:          tool.Name,
					LLMOutput:     text,
					DisplayOutput: text,
					IsError:       true,
					Metadata:      attachExecPolicyMetadata(metadata, policyMetadata),
				}, nil
			}
			cmd := exec.CommandContext(callCtx, commandPath, commandArgs...)
			procutil.PrepareCommandCancellation(cmd)
			cmd.Dir = commandDir
			if sandboxStatus == "bwrap" {
				cmd.ExtraFiles = append(cmd.ExtraFiles, sandboxExtraFiles...)
			}
			cmd.Env = append(
				filteredEnv(effectiveConfig.Runtime.ShellEnvAllowlist),
				"GO_CLI_AGENT_ARGS_JSON="+string(raw),
				"GO_CLI_AGENT_SKILL_DIR="+skillDir,
				"GO_CLI_AGENT_SKILL_NAME="+tool.SkillName,
			)
			cmd.Stdin = bytes.NewReader(raw)
			if beforeShellCommandStart != nil {
				if err := beforeShellCommandStart(skillDir); err != nil {
					return errorResult(tool.Name, err), nil
				}
			}
			collector := newCommandOutputCollector(execCtx, tool.Name)
			cmd.Stdout = collector
			cmd.Stderr = collector
			err = cmd.Run()
			exitCode := 0
			if cmd.ProcessState != nil {
				exitCode = cmd.ProcessState.ExitCode()
			}
			rawLength := collector.rawByteCount()
			truncated := rawLength > effectiveCommandToolOutputPolicy(effectiveConfig).LLMOutputMaxBytes
			summary := commandResultSummary(tool.Name, exitCode, timeout, "skill", skillDir, sandboxStatus, rawLength, truncated)
			if err != nil {
				if ctx.Err() != nil {
					md := attachExecPolicyMetadata(commandMetadata(timeout, sandboxStatus, exitCode, rawLength, truncated), policyMetadata)
					md[MetadataFailureClass] = FailureClassInterrupted
					return collector.finalize(commandOutputResultOptions{
						Summary:       summary,
						StatusMessage: InterruptedToolExecutionMessage,
						IsError:       true,
						Metadata:      md,
					}), ctx.Err()
				}
				if callCtx.Err() != nil {
					md := attachExecPolicyMetadata(commandMetadata(timeout, sandboxStatus, exitCode, rawLength, truncated), policyMetadata)
					md[MetadataFailureClass] = FailureClassTimeout
					return collector.finalize(commandOutputResultOptions{
						Summary:       summary,
						StatusMessage: TimedOutToolExecutionMessage,
						IsError:       true,
						Metadata:      md,
					}), nil
				}
				return collector.finalize(commandOutputResultOptions{
					Summary:  summary,
					IsError:  true,
					Metadata: attachExecPolicyMetadata(commandMetadata(timeout, sandboxStatus, exitCode, rawLength, truncated), policyMetadata),
				}), nil
			}
			return collector.finalize(commandOutputResultOptions{
				Summary:  summary,
				Metadata: attachExecPolicyMetadata(commandMetadata(timeout, sandboxStatus, exitCode, rawLength, truncated), policyMetadata),
			}), nil
		},
	}
}

func decodeCommandToolArgs(raw json.RawMessage) (map[string]any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return map[string]any{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("tool input must contain a single JSON value")
		}
		return nil, err
	}
	if value == nil {
		return map[string]any{}, nil
	}
	args, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("tool input must be a JSON object")
	}
	return args, nil
}

func validateCommandToolInput(schema map[string]any, args map[string]any) error {
	if len(schema) == 0 {
		return nil
	}
	return validateCommandToolValue(schema, args, "")
}

func validateCommandToolValue(schema map[string]any, value any, field string) error {
	if len(schema) == 0 {
		return nil
	}
	expectedType, _ := schema["type"].(string)
	switch expectedType {
	case "", "object":
		object, ok := value.(map[string]any)
		if !ok {
			return commandToolTypeError(field, "object")
		}
		if properties, ok := schema["properties"].(map[string]any); ok {
			if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
				for key := range object {
					if _, known := properties[key]; !known {
						return fmt.Errorf("unexpected field %q", key)
					}
				}
			}
			for key, rawProperty := range properties {
				propertySchema, ok := rawProperty.(map[string]any)
				if !ok {
					continue
				}
				current, exists := object[key]
				if !exists {
					continue
				}
				if err := validateCommandToolValue(propertySchema, current, key); err != nil {
					return err
				}
			}
		}
		for _, key := range schemaRequiredFields(schema) {
			current, exists := object[key]
			if !exists || current == nil {
				return fmt.Errorf("missing required field %q", key)
			}
		}
		return nil
	case "string":
		if _, ok := value.(string); !ok {
			return commandToolTypeError(field, "string")
		}
		return nil
	case "integer":
		if !isCommandToolInteger(value) {
			return commandToolTypeError(field, "integer")
		}
		return nil
	case "number":
		if !isCommandToolNumber(value) {
			return commandToolTypeError(field, "number")
		}
		return nil
	case "boolean":
		if _, ok := value.(bool); !ok {
			return commandToolTypeError(field, "boolean")
		}
		return nil
	case "array":
		items, ok := value.([]any)
		if !ok {
			return commandToolTypeError(field, "array")
		}
		itemSchema, _ := schema["items"].(map[string]any)
		for _, item := range items {
			if err := validateCommandToolValue(itemSchema, item, field); err != nil {
				return err
			}
		}
		return nil
	default:
		return nil
	}
}

func schemaRequiredFields(schema map[string]any) []string {
	switch required := schema["required"].(type) {
	case []string:
		out := make([]string, 0, len(required))
		for _, item := range required {
			if strings.TrimSpace(item) != "" {
				out = append(out, item)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(required))
		for _, item := range required {
			name, ok := item.(string)
			if ok && strings.TrimSpace(name) != "" {
				out = append(out, name)
			}
		}
		return out
	default:
		return nil
	}
}

func commandToolTypeError(field, expected string) error {
	if strings.TrimSpace(field) == "" {
		return fmt.Errorf("tool input must be a JSON %s", expected)
	}
	return fmt.Errorf("field %q must be a JSON %s", field, expected)
}

func isCommandToolInteger(value any) bool {
	switch current := value.(type) {
	case json.Number:
		_, err := current.Int64()
		return err == nil
	case float64:
		return current == float64(int64(current))
	default:
		return false
	}
}

func isCommandToolNumber(value any) bool {
	switch value.(type) {
	case json.Number, float64:
		return true
	default:
		return false
	}
}

// commandTemplateFields reports the top-level `.field` names referenced by an
// argv template. Names consumed by range/with are reported separately because
// substituting an empty string for them would turn a no-op into an error. The
// third result reports whether the template contains a conditional node
// (if/with/range), whose branches are expected to render to nothing when the
// condition does not hold. The last result holds the names whose value always
// contributes to the rendered argument, i.e. plain `{{.field}}` slots (or
// variables carrying such a field) used outside every conditional node.
// Assignment actions like `{{$v := .field}}` or `{{$v = .field}}` render to
// nothing, so the fields they read only count when the declared variable is
// later emitted outside every conditional node.
func commandTemplateFields(tmpl *template.Template) ([]string, map[string]struct{}, bool, map[string]struct{}) {
	iterated := map[string]struct{}{}
	plain := map[string]struct{}{}
	if tmpl == nil || tmpl.Tree == nil {
		return nil, iterated, false, plain
	}
	var referenced []string
	conditional := false
	condDepth := 0
	seen := map[string]struct{}{}
	// declared maps a template variable to the field names feeding it, and
	// declSink is non-nil while walking the pipeline of an assignment action.
	declared := map[string]map[string]struct{}{}
	var declSink map[string]struct{}
	collect := func(field *parse.FieldNode, iterating bool) {
		if len(field.Ident) == 0 {
			return
		}
		name := field.Ident[0]
		if _, ok := seen[name]; !ok {
			seen[name] = struct{}{}
			referenced = append(referenced, name)
		}
		if iterating {
			iterated[name] = struct{}{}
		}
		if declSink != nil {
			declSink[name] = struct{}{}
			return
		}
		if condDepth == 0 {
			plain[name] = struct{}{}
		}
	}
	// useVariable records the fields behind a variable that is read outside
	// every conditional node, because emitting the variable there contributes
	// the field value to the argument just like a plain `{{.field}}` slot.
	useVariable := func(node *parse.VariableNode) {
		if len(node.Ident) == 0 {
			return
		}
		sources := declared[node.Ident[0]]
		if len(sources) == 0 {
			return
		}
		if declSink != nil {
			for name := range sources {
				declSink[name] = struct{}{}
			}
			return
		}
		if condDepth == 0 {
			for name := range sources {
				plain[name] = struct{}{}
			}
		}
	}
	var walkNode func(parse.Node)
	var walkPipe func(*parse.PipeNode, bool)
	walkPipe = func(pipe *parse.PipeNode, iterating bool) {
		if pipe == nil {
			return
		}
		for _, command := range pipe.Cmds {
			for _, arg := range command.Args {
				switch current := arg.(type) {
				case *parse.FieldNode:
					collect(current, iterating)
				case *parse.VariableNode:
					useVariable(current)
				case *parse.ChainNode:
					walkNode(current.Node)
				case *parse.PipeNode:
					walkPipe(current, iterating)
				}
			}
		}
	}
	walkNode = func(node parse.Node) {
		switch current := node.(type) {
		case *parse.ListNode:
			if current == nil {
				return
			}
			for _, item := range current.Nodes {
				walkNode(item)
			}
		case *parse.ActionNode:
			if current.Pipe != nil && len(current.Pipe.Decl) > 0 {
				// `{{$v := ...}}` / `{{$v = ...}}` renders to nothing, so the
				// fields it reads are attributed to the declared variables
				// instead of being treated as plain output slots.
				outer := declSink
				sink := map[string]struct{}{}
				declSink = sink
				walkPipe(current.Pipe, false)
				declSink = outer
				for _, decl := range current.Pipe.Decl {
					if len(decl.Ident) == 0 {
						continue
					}
					existing := declared[decl.Ident[0]]
					if existing == nil {
						existing = map[string]struct{}{}
						declared[decl.Ident[0]] = existing
					}
					for name := range sink {
						existing[name] = struct{}{}
					}
				}
				return
			}
			walkPipe(current.Pipe, false)
		case *parse.IfNode:
			conditional = true
			condDepth++
			walkPipe(current.Pipe, false)
			walkNode(current.List)
			walkNode(current.ElseList)
			condDepth--
		case *parse.RangeNode:
			conditional = true
			condDepth++
			walkPipe(current.Pipe, true)
			walkNode(current.List)
			walkNode(current.ElseList)
			condDepth--
		case *parse.WithNode:
			conditional = true
			condDepth++
			walkPipe(current.Pipe, true)
			walkNode(current.List)
			walkNode(current.ElseList)
			condDepth--
		case *parse.PipeNode:
			walkPipe(current, false)
		case *parse.FieldNode:
			collect(current, false)
		}
	}
	walkNode(tmpl.Tree.Root)
	return referenced, iterated, conditional, plain
}

func renderCommand(command []string, args map[string]any) ([]string, error) {
	if len(command) == 0 {
		return nil, errors.New("command must not be empty")
	}
	out := make([]string, 0, len(command))
	for _, part := range command {
		tmpl, err := template.New("arg").Option("missingkey=zero").Parse(part)
		if err != nil {
			return nil, err
		}
		// missingkey=zero does not apply to map[string]any inputs: absent keys
		// render as the literal "<no value>". Substitute an empty string for
		// absent keys so the placeholder never becomes an argument value, and
		// remember whether every referenced field was absent.
		referenced, iterated, conditional, plain := commandTemplateFields(tmpl)
		data := args
		allMissing := len(referenced) > 0
		explicitPlain := false
		for _, name := range referenced {
			if _, provided := args[name]; provided {
				allMissing = false
				if _, outside := plain[name]; outside {
					explicitPlain = true
				}
				continue
			}
			if _, skip := iterated[name]; skip {
				continue
			}
			if data == nil || len(data) == len(args) {
				copied := make(map[string]any, len(args)+len(referenced))
				for key, value := range args {
					copied[key] = value
				}
				data = copied
			}
			data[name] = ""
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			return nil, err
		}
		value := strings.TrimSpace(buf.String())
		// Drop the argv element only when it is a placeholder for omitted
		// optional arguments, or when a conditional block (if/with/range)
		// legitimately rendered to nothing. An explicitly provided empty value
		// in a plain `{{.field}}` slot keeps its slot so later positional
		// arguments do not shift left, so the conditional exemption only applies
		// while no such slot exists outside the conditional blocks.
		if value == "" && !explicitPlain && (allMissing || conditional) {
			continue
		}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil, errors.New("command rendered to empty argv")
	}
	return out, nil
}

func commandResultSummary(toolName string, exitCode, timeout int, workdirSource, workdir, sandbox string, rawLength int, truncated bool) string {
	parts := []string{
		fmt.Sprintf("tool=%s", toolName),
		fmt.Sprintf("exit_code=%d", exitCode),
		fmt.Sprintf("timeout_sec=%d", timeout),
		fmt.Sprintf("workdir_source=%s", workdirSource),
		fmt.Sprintf("workdir=%s", workdir),
		fmt.Sprintf("sandbox=%s", sandbox),
		fmt.Sprintf("raw_output_bytes=%d", rawLength),
		fmt.Sprintf("truncated=%t", truncated),
	}
	return "[command_result " + strings.Join(parts, " ") + "]"
}

func commandLLMOutput(output, summary string) string {
	if strings.TrimSpace(summary) == "" {
		return output
	}
	if output == "" {
		return summary
	}
	return summary + "\n" + output
}

func filteredEnv(allowlist []string) []string {
	allowed := map[string]struct{}{}
	for _, item := range allowlist {
		allowed[item] = struct{}{}
	}
	// Always non-nil: os/exec treats a nil Env as "inherit the full parent
	// environment", which would turn an allowlist that matches nothing into a
	// fail-open leak of every parent variable.
	out := make([]string, 0, len(allowlist))
	for _, entry := range os.Environ() {
		key := strings.SplitN(entry, "=", 2)[0]
		if _, ok := allowed[key]; ok {
			out = append(out, entry)
		}
	}
	return out
}

func shellCommand() (string, string) {
	if strings.Contains(strings.ToLower(os.Getenv("COMSPEC")), "cmd.exe") {
		return "cmd", "/C"
	}
	return "/bin/bash", "-lc"
}

func effectiveToolTimeout(defaultTimeout, requestedTimeout int) int {
	if defaultTimeout > 0 {
		if requestedTimeout <= 0 {
			return defaultTimeout
		}
		if requestedTimeout > defaultTimeout {
			return defaultTimeout
		}
		return requestedTimeout
	}
	if requestedTimeout > 0 {
		return requestedTimeout
	}
	return 0
}

func effectiveSandboxStatus(cfg *config.Config) string {
	if cfg == nil || strings.TrimSpace(cfg.Runtime.Shell.Sandbox) == "" {
		return "off"
	}
	return strings.ToLower(strings.TrimSpace(cfg.Runtime.Shell.Sandbox))
}

func writeAtomically(path string, data []byte, mode os.FileMode) error {
	return fileutil.AtomicWriteFileNoSymlink(path, data, mode)
}

func truncateOutput(text string, limit int) (string, int, bool) {
	rawLength := len(text)
	if len(text) <= limit {
		return text, rawLength, false
	}
	if limit <= 0 {
		return "", rawLength, true
	}
	marker := "\n...[truncated]...\n"
	headBytes := limit / 2
	tailBytes := limit - headBytes
	head := prefixAtRuneBoundary(text, headBytes)
	tail := suffixAtRuneBoundary(text, tailBytes)
	for i := 0; i < 3; i++ {
		omitted := rawLength - len(head) - len(tail)
		if omitted < 0 {
			omitted = 0
		}
		marker = fmt.Sprintf("\n...[truncated: %d bytes omitted]...\n", omitted)
		remaining := limit - len(marker)
		if remaining < 2 {
			return prefixAtRuneBoundary(text, limit), rawLength, true
		}
		headBytes = remaining / 2
		tailBytes = remaining - headBytes
		head = prefixAtRuneBoundary(text, headBytes)
		tail = suffixAtRuneBoundary(text, tailBytes)
	}
	return head + marker + tail, rawLength, true
}

func prefixAtRuneBoundary(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if limit >= len(text) {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit]
}

func suffixAtRuneBoundary(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if limit >= len(text) {
		return text
	}
	start := len(text) - limit
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[start:]
}

func relativeOrAbsolute(base, path string) string {
	if rel, err := filepath.Rel(base, path); err == nil && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return rel
	}
	return path
}

func errorResult(tool string, err error) session.ToolResult {
	result := session.ToolResult{
		Name:          tool,
		LLMOutput:     "Error: " + err.Error(),
		DisplayOutput: "Error: " + err.Error(),
		IsError:       true,
	}
	if class := classifyToolError(err); class != "" {
		setToolResultFailureClass(&result, class)
	}
	return result
}

func readFileErrorResult(inputPath string, err error) session.ToolResult {
	result := errorResult("read_file", withReadFileDiscoveryHint(err))
	if strings.TrimSpace(inputPath) != "" {
		if result.Metadata == nil {
			result.Metadata = make(map[string]any)
		}
		result.Metadata["path"] = inputPath
	}
	return result
}

func withReadFileDiscoveryHint(err error) error {
	if classifyToolError(err) != FailureClassNotFound {
		return err
	}
	return fmt.Errorf("%w. Locate the path with grep_files or glob before reading; do not read source paths from memory", err)
}

func classifyToolError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, fs.ErrNotExist) {
		return FailureClassNotFound
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "does not exist or is not accessible"):
		return FailureClassNotFound
	case strings.Contains(message, "no such file or directory"):
		return FailureClassNotFound
	default:
		return ""
	}
}

func setToolResultFailureClass(result *session.ToolResult, class string) {
	if result == nil || strings.TrimSpace(class) == "" {
		return
	}
	if result.Metadata == nil {
		result.Metadata = make(map[string]any)
	}
	result.Metadata[MetadataFailureClass] = class
}

func pendingPlanInputErrorResult(err error, request session.PlanModeInputRequest, planMode session.PlanModeState) session.ToolResult {
	message := err.Error()
	return session.ToolResult{
		Name:          "request_user_input",
		LLMOutput:     "Error: " + message,
		DisplayOutput: "Error: " + message,
		IsError:       true,
		Metadata: map[string]any{
			"planmode":                 true,
			"plan_input_pending":       true,
			"plan_input_pending_error": message,
			"request_id":               request.RequestID,
			"plan_mode_id":             planMode.PlanModeID,
		},
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
