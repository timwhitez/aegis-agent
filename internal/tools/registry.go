package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/fileutil"
	"go-cli-agent/internal/session"
	"go-cli-agent/internal/skills"
)

type Definition struct {
	Name            string
	Description     string
	InputSchema     map[string]any
	Execute         func(context.Context, ExecContext, json.RawMessage) (session.ToolResult, error)
	Ephemeral       bool
	EphemeralWindow int
}

type ExecContext struct {
	SessionID string
	Workdir   string
	Store     *session.Store
	Config    *config.Config
	Catalog   *skills.Catalog
	Emit      func(string, map[string]any)
}

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
}

type AgentStatusRequest struct {
	SessionID  string `json:"session_id,omitempty"`
	QueueJobID string `json:"queue_job_id,omitempty"`
}

type AgentStatusResult struct {
	SessionID     string `json:"session_id,omitempty"`
	QueueJobID    string `json:"queue_job_id,omitempty"`
	Status        string `json:"status,omitempty"`
	SessionStatus string `json:"session_status,omitempty"`
	FinalText     string `json:"final_text,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	Workdir       string `json:"workdir,omitempty"`
	AgentName     string `json:"agent_name,omitempty"`
	AgentRole     string `json:"agent_role,omitempty"`
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
	AgentStatus(context.Context, AgentStatusRequest) (AgentStatusResult, error)
	AgentList(context.Context, string) (AgentListResult, error)
}

type Registry struct {
	defs    map[string]Definition
	order   []string
	control ControlPlane
}

var reservedNames = map[string]struct{}{
	"shell": {}, "read_file": {}, "write_file": {}, "edit_file": {}, "glob": {}, "grep": {}, "grep_files": {},
	"finish": {}, "load_skill": {}, "get_goal": {}, "create_goal": {}, "update_goal": {}, "todo_write": {}, "todo_read": {}, "task_create": {},
	"task_update": {}, "task_list": {}, "task_get": {}, "agent_spawn": {}, "agent_status": {},
	"agent_list": {}, "feature_list_create": {}, "feature_list_update": {}, "feature_list_read": {},
}

func NewRegistry(cfg *config.Config, catalog *skills.Catalog, store *session.Store, control ControlPlane, trustedCommandWorkdir ...string) (*Registry, error) {
	registry := &Registry{defs: map[string]Definition{}, control: control}
	for _, def := range builtinDefinitions(cfg, catalog, control) {
		registry.Register(def)
	}
	if catalog != nil {
		workdir := ""
		if len(trustedCommandWorkdir) > 0 {
			workdir = trustedCommandWorkdir[0]
		}
		for _, tool := range catalog.TrustedCommandTools(workdir) {
			if _, ok := reservedNames[tool.Name]; ok {
				return nil, fmt.Errorf("skill tool name is reserved: %s", tool.Name)
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
	if def, ok := r.defs[name]; ok {
		return &def
	}
	return nil
}

func (r *Registry) Definitions() []Definition {
	var out []Definition
	for _, name := range r.order {
		out = append(out, r.defs[name])
	}
	return out
}

func (r *Registry) Execute(ctx context.Context, name string, execCtx ExecContext, args json.RawMessage) (session.ToolResult, error) {
	def, ok := r.defs[name]
	if !ok {
		return session.ToolResult{}, fmt.Errorf("unknown tool: %s", name)
	}
	return def.Execute(ctx, execCtx, args)
}

func builtinDefinitions(cfg *config.Config, catalog *skills.Catalog, control ControlPlane) []Definition {
	defs := []Definition{
		defShell(),
		defReadFile(),
		defWriteFile(),
		defEditFile(),
		defGlob(),
		defGrepFiles(),
		defGrep(),
		defFinish(),
		defLoadSkill(catalog),
		defGetGoal(),
		defCreateGoal(),
		defUpdateGoal(),
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
				"description": "Current task state. Use in_progress for at most one active item and completed only after the work is actually done.",
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
		Description:     "Run non-interactive terminal commands in the workspace for build, test, package, git, and runtime operations. Prefer dedicated tools for file search, reading, writing, and editing instead of shell cat/grep/sed/echo. Use the workdir parameter instead of embedding cd when changing directories; workdir may be workspace-relative or a registered skill directory returned by load_skill. Quote paths with spaces, and inspect exit_code/output before claiming success.",
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
			if input.Command == "" {
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
			commandPath, commandArgs, sandboxStatus, sandboxErr := shellSandboxCommand(shellSandbox, workdir, command, shellArg, input.Command)
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
			cmd.Dir = workdir
			cmd.Env = filteredEnv(execCtx.Config.Runtime.ShellEnvAllowlist)
			output, err := cmd.CombinedOutput()
			exitCode := 0
			if cmd.ProcessState != nil {
				exitCode = cmd.ProcessState.ExitCode()
			}
			text, rawLength, truncated := truncateOutput(string(output), 12000)
			if text == "" {
				text = "(no output)"
			}
			if err != nil {
				interruptErr := ctx.Err()
				if interruptErr == nil {
					interruptErr = callCtx.Err()
				}
				if interruptErr != nil {
					return session.ToolResult{
						ToolCallID:    "",
						Name:          "shell",
						LLMOutput:     "[Tool execution was interrupted]",
						DisplayOutput: "[Tool execution was interrupted]",
						IsError:       true,
						Metadata:      metadata(exitCode, rawLength, truncated),
					}, interruptErr
				}
				return session.ToolResult{
					Name:          "shell",
					LLMOutput:     text,
					DisplayOutput: text,
					IsError:       true,
					Metadata:      metadata(exitCode, rawLength, truncated),
				}, nil
			}
			return session.ToolResult{
				Name:          "shell",
				LLMOutput:     text,
				DisplayOutput: text,
				Metadata:      metadata(exitCode, rawLength, truncated),
			}, nil
		},
	}
}

func defReadFile() Definition {
	return Definition{
		Name:        "read_file",
		Description: "Read a known text file with 1-based offset and limit. Paths normally resolve inside the workspace. Registered skill bundle files are also readable by exact skill path such as skills/<skill-name>/references/file.md, by the absolute path returned from load_skill, or by an unambiguous skill-relative link such as references/file.md. Each call returns an annotated line window and is capped at 120 lines, so use grep_files or grep first for workspace discovery and then read the owning file slices you need. This reads files only, not directories, and rejects internal generated artifacts.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Workspace-relative file path to read, or a registered skill bundle file path such as skills/<skill-name>/references/file.md.",
				},
				"offset": map[string]any{
					"type":        "integer",
					"description": "1-based starting line. Omit or use 1 to start at the beginning.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum lines to return. Values above 120 are capped to 120.",
				},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		},
		Execute: func(_ context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			var input struct {
				Path   string `json:"path"`
				Offset int    `json:"offset"`
				Limit  int    `json:"limit"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("read_file", err), nil
			}
			path, displayBase, source, skillName, err := resolveReadFilePath(execCtx, input.Path)
			if err != nil {
				return errorResult("read_file", err), nil
			}
			if source == "workspace" && (isInternalGeneratedArtifactInput(input.Path) || isInternalGeneratedArtifactPath(execCtx.Workdir, path)) {
				return errorResult("read_file", errors.New("path is an internal generated artifact; use source files, copied validation evidence, or rerun the command and redirect output to a normal workspace file (for example under reports/)")), nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return errorResult("read_file", err), nil
			}
			lines := strings.Split(string(data), "\n")
			offset := max(input.Offset, 1) - 1
			if offset > len(lines) {
				offset = len(lines)
			}
			limit := input.Limit
			if limit <= 0 {
				limit = readFileDefaultLimit
			}
			capped := false
			if limit > readFileMaxLimit {
				limit = readFileMaxLimit
				capped = true
			}
			end := offset + limit
			if end > len(lines) {
				end = len(lines)
			}
			selected := strings.Join(lines[offset:end], "\n")
			selected = annotateReadWindow(displayBase, path, offset, end, len(lines), input.Limit, capped, selected)
			metadata := map[string]any{
				"path":        path,
				"offset":      offset,
				"end":         end,
				"path_source": source,
			}
			if repeat := readFileRepeatObservation(execCtx, path, offset, end); repeat.Count > 1 {
				metadata["repeat_count"] = repeat.Count
				metadata["first_seen_at"] = repeat.FirstSeenAt
				metadata["last_seen_at"] = repeat.LastSeenAt
				metadata["warning"] = "repeated read_file call for the same path and line window; use existing evidence unless you need a precise recheck"
				selected += fmt.Sprintf("\n[read_file warning repeat_count=%d same path/range was already read in this session]", repeat.Count)
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
			content, err := os.ReadFile(path)
			if err != nil {
				return errorResult("edit_file", err), nil
			}
			current := string(content)
			if !strings.Contains(current, input.OldText) {
				return errorResult("edit_file", errors.New("old_text not found")), nil
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

func defGlob() Definition {
	return Definition{
		Name:            "glob",
		Description:     "Find workspace paths by glob pattern and return file paths only. Use this when you know the filename shape or extension; use grep_files or grep when you need content-based discovery. Generated, cache, and internal artifact directories are skipped.",
		Ephemeral:       true,
		EphemeralWindow: 3,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Glob pattern such as **/*.go or spec/*.md, evaluated inside the workspace.",
				},
			},
			"required": []string{"pattern"},
		},
		Execute: func(_ context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			var input struct {
				Pattern string `json:"pattern"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("glob", err), nil
			}
			var matches []string
			if err := doublestar.GlobWalk(os.DirFS(execCtx.Workdir), input.Pattern, func(path string, d os.DirEntry) error {
				if path != "." {
					if _, err := ResolveWorkspacePath(execCtx.Workdir, path); err != nil {
						if d.IsDir() {
							return fs.SkipDir
						}
						return nil
					}
				}
				if d.IsDir() && path != "." && shouldSkipGrepDir(path) {
					return fs.SkipDir
				}
				if !d.IsDir() {
					if isInternalGeneratedArtifactPath(execCtx.Workdir, filepath.Join(execCtx.Workdir, path)) {
						return nil
					}
					matches = append(matches, path)
				}
				return nil
			}); err != nil {
				return errorResult("glob", err), nil
			}
			output := strings.Join(matches, "\n")
			if output == "" {
				output = "(no matches)"
			}
			return session.ToolResult{Name: "glob", LLMOutput: output, DisplayOutput: output}, nil
		},
	}
}

func defGrep() Definition {
	return Definition{
		Name:        "grep",
		Description: "Search workspace text recursively and return matching lines as path:line:text. Use this when exact snippets or line numbers matter; use grep_files first when you only need candidate file paths. Patterns are treated as regex when valid and literal substring otherwise; build/cache/internal artifacts and binary files are skipped.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Regex pattern if it compiles, otherwise literal substring to search for.",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Optional workspace-relative file or directory to search. Omit to search the workspace.",
				},
			},
			"required": []string{"pattern"},
		},
		Execute: func(_ context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			var input struct {
				Pattern string `json:"pattern"`
				Path    string `json:"path"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("grep", err), nil
			}
			root := execCtx.Workdir
			if input.Path != "" {
				path, err := ResolveWorkspacePath(execCtx.Workdir, input.Path)
				if err != nil {
					return errorResult("grep", err), nil
				}
				root = path
			}
			matcher, regexErr := regexp.Compile(input.Pattern)
			useRegex := regexErr == nil
			var lines []string
			_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
				if err != nil {
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
				data, readErr := os.ReadFile(path)
				if readErr != nil {
					return nil
				}
				if shouldSkipGrepBinary(data) {
					return nil
				}
				for lineNo, line := range strings.Split(string(data), "\n") {
					matched := false
					if useRegex {
						matched = matcher.MatchString(line)
					} else {
						matched = strings.Contains(line, input.Pattern)
					}
					if matched {
						lines = append(lines, fmt.Sprintf("%s:%d:%s", relativeOrAbsolute(execCtx.Workdir, path), lineNo+1, strings.TrimSpace(line)))
						if len(lines) >= 200 {
							return errors.New("limit reached")
						}
					}
				}
				return nil
			})
			output := strings.Join(lines, "\n")
			if output == "" {
				output = "(no matches)"
			}
			return session.ToolResult{Name: "grep", LLMOutput: output, DisplayOutput: output}, nil
		},
	}
}

func defGrepFiles() Definition {
	return Definition{
		Name:            "grep_files",
		Description:     "Search workspace text recursively and return only files that contain the pattern. Use this as the default discovery step before read_file when you need to locate owning files without flooding the context. Supports regex-or-literal matching, optional path/include filters, and skips build/cache/internal artifacts and binary files.",
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
					"description": "Optional workspace-relative file or directory to search. Omit to search the workspace.",
				},
				"include": map[string]any{
					"type":        "string",
					"description": "Optional glob filter for candidate files, for example **/*.go or spec/*.md.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of matching file paths to return. Defaults to 100.",
				},
			},
			"required": []string{"pattern"},
		},
		Execute: func(_ context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			var input struct {
				Pattern string `json:"pattern"`
				Path    string `json:"path"`
				Include string `json:"include"`
				Limit   int    `json:"limit"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("grep_files", err), nil
			}
			root, err := resolveGrepRoot(execCtx.Workdir, input.Path)
			if err != nil {
				return errorResult("grep_files", err), nil
			}
			matcher, useRegex := compileGrepMatcher(input.Pattern)
			limit := input.Limit
			if limit <= 0 {
				limit = 100
			}
			var matches []string
			err = walkTextSearchFiles(execCtx.Workdir, root, input.Include, func(path string, data string) error {
				if !textMatchesPattern(data, matcher, useRegex, input.Pattern) {
					return nil
				}
				matches = append(matches, relativeOrAbsolute(execCtx.Workdir, path))
				if len(matches) >= limit {
					return errGrepLimitReached
				}
				return nil
			})
			if err != nil && !errors.Is(err, errGrepLimitReached) {
				return errorResult("grep_files", err), nil
			}
			output := strings.Join(matches, "\n")
			if output == "" {
				output = "(no matches)"
			}
			return session.ToolResult{Name: "grep_files", LLMOutput: output, DisplayOutput: output}, nil
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

var errNoRegisteredSkillPath = errors.New("path is not under a registered skill")

type resolvedSkillPath struct {
	path        string
	displayBase string
	skillName   string
	explicit    bool
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

func resolveGrepRoot(workdir, inputPath string) (string, error) {
	root := workdir
	if inputPath == "" {
		return root, nil
	}
	path, err := ResolveWorkspacePath(workdir, inputPath)
	if err != nil {
		return "", err
	}
	return path, nil
}

func compileGrepMatcher(pattern string) (*regexp.Regexp, bool) {
	matcher, err := regexp.Compile(pattern)
	return matcher, err == nil
}

func textMatchesPattern(text string, matcher *regexp.Regexp, useRegex bool, pattern string) bool {
	if useRegex {
		return matcher.MatchString(text)
	}
	return strings.Contains(text, pattern)
}

func walkTextSearchFiles(workdir, root, include string, fn func(path string, data string) error) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
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
		data, readErr := os.ReadFile(path)
		if readErr != nil {
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
		Description: "Signal that the current task is complete and provide the final concise result for the user. Use only after requested work, required artifacts, and necessary validation are complete, or after clearly stating any blocker or unrun/failed validation. Do not call finish merely because the model has no more ideas.",
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
			return session.ToolResult{
				Name:          "finish",
				LLMOutput:     input.Message,
				DisplayOutput: input.Message,
				Final:         true,
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
			if !input.ForceReload && skillLoaded(execCtx, input.Name) {
				output := fmt.Sprintf("<skill name=%q path=%q already_loaded=true shell_workdir=%q>\nThis skill has already been loaded in this session. Reuse the prior instructions; call load_skill again with force_reload=true only if the skill file changed or the user explicitly asks to reload it.\nAvailable bundle files can still be inspected with read_file using paths like `skills/%s/references/...` or skill-relative links.\n</skill>", skill.Name, skill.Path, shellWorkdir, skill.Name)
				return session.ToolResult{
					Name:          "load_skill",
					LLMOutput:     output,
					DisplayOutput: fmt.Sprintf("Skill already loaded: %s", input.Name),
					Metadata: map[string]any{
						"path":           skill.Path,
						"shell_workdir":  shellWorkdir,
						"already_loaded": true,
						"force_reload":   false,
					},
				}, nil
			}
			body, err := execCtx.Catalog.LoadBody(input.Name)
			if err != nil {
				if available := execCtx.Catalog.Names(); len(available) > 0 {
					err = fmt.Errorf("%w; available skills: %s", err, strings.Join(available, ", "))
				}
				return errorResult("load_skill", err), nil
			}
			markSkillLoaded(execCtx, input.Name)
			output := fmt.Sprintf("<skill path=%q shell_workdir=%q>\nWhen this skill uses relative shell paths, call the shell tool with `workdir=%q` so commands run from the skill bundle root.\nSkill bundle files are registered read-only resources, not workspace files. To inspect referenced skill files, call read_file with paths like `skills/%s/references/...` or an unambiguous skill-relative link such as `references/...`; do not resolve those links under the workspace directory.\n\n%s\n</skill>", skill.Path, shellWorkdir, shellWorkdir, skill.Name, body)
			return session.ToolResult{
				Name:          "load_skill",
				LLMOutput:     output,
				DisplayOutput: fmt.Sprintf("Loaded skill: %s", input.Name),
				Metadata: map[string]any{
					"path":          skill.Path,
					"shell_workdir": shellWorkdir,
					"force_reload":  input.ForceReload,
				},
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
					"description": "Durable objective, max 4000 characters. Treat as user-provided task context, not higher-priority instructions.",
				},
				"mode": map[string]any{
					"type":        "string",
					"enum":        []string{"goal", "mission"},
					"description": "goal for a single long objective, mission for structured features/milestones.",
				},
				"token_budget":        map[string]any{"type": "integer", "description": "Optional positive token budget."},
				"time_budget_minutes": map[string]any{"type": "integer", "description": "Optional positive time budget in minutes."},
				"success_criteria":    withDescription(stringArraySchema(), "Optional concrete completion criteria."),
				"validation_plan":     withDescription(stringArraySchema(), "Optional validation commands, artifacts, manual checks, browser checks, or review checks."),
				"features":            withDescription(stringArraySchema(), "Optional mission features when mode is mission."),
				"milestones":          withDescription(stringArraySchema(), "Optional mission milestones when mode is mission."),
				"require_plan_approval": map[string]any{
					"type":        "boolean",
					"description": "When true, mission plan starts in needs_approval.",
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
				Objective           string   `json:"objective"`
				Mode                string   `json:"mode"`
				TokenBudget         *int64   `json:"token_budget"`
				TimeBudgetMinutes   *int64   `json:"time_budget_minutes"`
				SuccessCriteria     []string `json:"success_criteria"`
				ValidationPlan      []string `json:"validation_plan"`
				Features            []string `json:"features"`
				Milestones          []string `json:"milestones"`
				RequirePlanApproval bool     `json:"require_plan_approval"`
				CreateTasksFromPlan bool     `json:"create_tasks_from_plan"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("create_goal", err), nil
			}
			var seconds *int64
			if input.TimeBudgetMinutes != nil {
				value := *input.TimeBudgetMinutes * 60
				seconds = &value
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
				CreateTasksFromPlan: input.CreateTasksFromPlan,
				Features:            input.Features,
				Milestones:          input.Milestones,
				Source:              session.GoalSourceTool,
			})
			if err != nil {
				return errorResult("create_goal", err), nil
			}
			if execCtx.Emit != nil {
				execCtx.Emit("goal.created", map[string]any{
					"goal_id":   goal.GoalID,
					"mode":      goal.Mode,
					"status":    goal.Status,
					"objective": goal.Objective,
				})
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

func defUpdateGoal() Definition {
	return Definition{
		Name:        "update_goal",
		Description: "Mark the existing session goal complete after a concrete completion audit. The model may only set status=complete; pause, resume, clear, objective changes, and budget-limited status are user/system controlled.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status": map[string]any{
					"type":        "string",
					"enum":        []string{"complete"},
					"description": "Only complete is allowed from the model tool.",
				},
				"evidence": withDescription(stringArraySchema(), "Optional concrete evidence refs for the completion audit."),
			},
			"required": []string{"status"},
		},
		Execute: func(_ context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			var input struct {
				Status   string   `json:"status"`
				Evidence []string `json:"evidence"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("update_goal", err), nil
			}
			if strings.TrimSpace(input.Status) != session.GoalStatusComplete {
				return errorResult("update_goal", errors.New("update_goal can only mark the existing goal complete; pause, resume, and budget-limited status changes are controlled by the user or system")), nil
			}
			goal, err := execCtx.Store.LoadGoal(execCtx.SessionID)
			if err != nil {
				return errorResult("update_goal", err), nil
			}
			now := time.Now().UTC().Format(time.RFC3339Nano)
			goal.Status = session.GoalStatusComplete
			goal.CompletedAt = now
			goal.UpdatedAt = now
			if err := execCtx.Store.SaveGoal(execCtx.SessionID, goal); err != nil {
				return errorResult("update_goal", err), nil
			}
			_ = execCtx.Store.AppendGoalHistory(execCtx.SessionID, session.GoalHistoryEntry{
				Type:   "goal.completed",
				Source: session.GoalSourceTool,
				Status: goal.Status,
				Data: map[string]any{
					"evidence": append([]string(nil), input.Evidence...),
				},
			})
			if execCtx.Emit != nil {
				execCtx.Emit("goal.completed", map[string]any{
					"goal_id":   goal.GoalID,
					"mode":      goal.Mode,
					"status":    goal.Status,
					"objective": goal.Objective,
					"evidence":  append([]string(nil), input.Evidence...),
				})
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

func defTodoWrite() Definition {
	return Definition{
		Name:        "todo_write",
		Description: "Replace the session todo list with the current execution plan. Use for non-trivial multi-step work, after new user instructions, or when progress needs durable visibility; skip trivial one-step or purely conversational tasks. Keep at most one item in_progress and mark items completed immediately after the work and relevant verification are done.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"todos": map[string]any{
					"type":        "array",
					"description": "Full replacement snapshot of the session todo list.",
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
			existing, _ := execCtx.Store.LoadTodo(execCtx.SessionID)
			changed := !normalizedTodosEqual(existing, input.Todos)
			if !changed {
				if execCtx.Emit != nil {
					execCtx.Emit("todo.updated", map[string]any{
						"count":   len(existing),
						"changed": false,
						"noop":    true,
					})
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
			if execCtx.Emit != nil {
				execCtx.Emit("todo.updated", map[string]any{
					"count":   len(input.Todos),
					"changed": true,
					"noop":    false,
				})
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

func markSkillLoaded(execCtx ExecContext, name string) {
	if execCtx.Store == nil || strings.TrimSpace(execCtx.SessionID) == "" {
		return
	}
	state, err := execCtx.Store.LoadState(execCtx.SessionID)
	if err != nil {
		return
	}
	for _, loaded := range state.LoadedSkills {
		if loaded == name {
			return
		}
	}
	state.LoadedSkills = append(state.LoadedSkills, name)
	_ = execCtx.Store.SaveState(execCtx.SessionID, state)
}

func validateTodoSnapshot(todos []session.TodoItem) error {
	inProgress := 0
	for _, item := range todos {
		switch item.Status {
		case "", "pending", "in_progress", "completed", "cancelled":
		default:
			return fmt.Errorf("invalid todo status: %s", item.Status)
		}
		if item.Status == "in_progress" {
			inProgress++
		}
	}
	if inProgress > 1 {
		return errors.New("todo_write allows at most one in_progress item")
	}
	return nil
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
				"priority":    map[string]any{"type": "string", "description": "Optional priority: high, medium, or low."},
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
			task, err := session.CreateTask(execCtx.Store, execCtx.SessionID, input)
			if err != nil {
				return errorResult("task_create", err), nil
			}
			if execCtx.Emit != nil {
				execCtx.Emit("task.created", map[string]any{
					"task_id": task.ID,
					"status":  task.Status,
				})
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
			task, err := session.UpdateTask(execCtx.Store, execCtx.SessionID, input)
			if err != nil {
				return errorResult("task_update", err), nil
			}
			if execCtx.Emit != nil {
				execCtx.Emit("task.updated", map[string]any{
					"task_id": task.ID,
					"status":  task.Status,
				})
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
		Description: "List the durable task graph and derived ready, blocked, and completed views. Use this when resuming long work, choosing the next ready task, or reconciling handoff state. This is not a substitute for checking current files or validation results.",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
		Execute: func(_ context.Context, execCtx ExecContext, _ json.RawMessage) (session.ToolResult, error) {
			todo, err := execCtx.Store.LoadTodo(execCtx.SessionID)
			if err != nil {
				return errorResult("task_list", err), nil
			}
			tasks, err := execCtx.Store.ListTasks(execCtx.SessionID)
			if err != nil {
				return errorResult("task_list", err), nil
			}
			board := session.BuildTaskBoard(todo, tasks)
			data, _ := json.MarshalIndent(board, "", "  ")
			return session.ToolResult{
				Name:          "task_list",
				LLMOutput:     string(data),
				DisplayOutput: string(data),
				Metadata: map[string]any{
					"tasks_dir":       taskDirPath(execCtx),
					"session_dir":     execCtx.Store.SessionDir(execCtx.SessionID),
					"todo_count":      len(todo),
					"task_count":      len(tasks),
					"ready_count":     board.Counters["ready"],
					"blocked_count":   board.Counters["blocked"],
					"completed_count": board.Counters["completed"],
				},
			}, nil
		},
	}
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

func defAgentSpawn(control ControlPlane) Definition {
	return Definition{
		Name:        "agent_spawn",
		Description: "Spawn a child agent when the model decides delegation would improve coverage, independence, or context control. Consider this for broad investigations, separable long-running slices, code audits, module scans, independent validation, or reviewer/evaluator passes; keep tiny single-file checks in the parent. Child sessions and background jobs are durable facts, and their results should be reconciled before final parent conclusions. Delegation is optional and model-led. Choose agent_role directly from planner, generator, or evaluator when role-specific Settings provider overrides should apply. Use isolation_mode=auto when the child must write artifacts.",
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
					"enum":        []string{"planner", "generator", "evaluator"},
					"description": "Optional child role hint. Choose exactly one of planner, generator, or evaluator when that role's Settings provider override should apply. Omit provider/model to use the configured default for the chosen role.",
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
			input.ParentSessionID = execCtx.SessionID
			result, err := control.SpawnAgent(ctx, input)
			if err != nil {
				return errorResult("agent_spawn", err), nil
			}
			data, _ := json.MarshalIndent(result, "", "  ")
			return session.ToolResult{Name: "agent_spawn", LLMOutput: string(data), DisplayOutput: string(data)}, nil
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
		Execute: func(ctx context.Context, _ ExecContext, raw json.RawMessage) (session.ToolResult, error) {
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
			result, err := control.AgentStatus(ctx, input)
			if err != nil {
				return errorResult("agent_status", err), nil
			}
			data, _ := json.MarshalIndent(result, "", "  ")
			return session.ToolResult{Name: "agent_status", LLMOutput: string(data), DisplayOutput: string(data)}, nil
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
	commandMetadata := func(timeout, exitCode, rawLength int, truncated bool) map[string]any {
		return map[string]any{
			"skill_name": tool.SkillName,
			"skill_path": tool.SkillPath,
			"workdir":    skillDir,
			"sandbox":    effectiveSandboxStatus(cfg),
			"timeout":    timeout,
			"exit_code":  exitCode,
			"raw_length": rawLength,
			"truncated":  truncated,
		}
	}
	return Definition{
		Name:        tool.Name,
		Description: fmt.Sprintf("Direct-call skill command tool from skill %s. Call this tool directly by name; do not search the workspace, skill files, or shell PATH for it. This tool executes from the skill directory. %s", tool.SkillName, description),
		InputSchema: inputSchema,
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
			callCtx := ctx
			var cancel context.CancelFunc
			timeout := tool.TimeoutSec
			if timeout <= 0 {
				timeout = cfg.Runtime.CommandTimeoutSec
			}
			if timeout > 0 {
				callCtx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
				defer cancel()
			}
			commandText := strings.Join(argv, " ")
			policyMode := effectiveExecPolicyMode(execCtx.Config)
			policyViolations := DetectExecPolicyViolations(commandText)
			policyMetadata := execPolicyMetadata(policyMode, policyViolations)
			shellSandbox := ""
			if execCtx.Config != nil {
				shellSandbox = execCtx.Config.Runtime.Shell.Sandbox
			}
			commandPath, commandArgs, sandboxStatus, sandboxErr := sandboxCommand(shellSandbox, skillDir, argv)
			if policyMode == "deny" && len(policyViolations) > 0 {
				text := "Error: skill command denied by exec policy"
				return session.ToolResult{
					Name:          tool.Name,
					LLMOutput:     text,
					DisplayOutput: text,
					IsError:       true,
					Metadata:      attachExecPolicyMetadata(commandMetadata(timeout, 0, 0, false), policyMetadata),
				}, nil
			}
			if sandboxErr != nil {
				text := "Error: " + sandboxErr.Error()
				metadata := commandMetadata(timeout, 0, 0, false)
				metadata["sandbox"] = sandboxStatus
				return session.ToolResult{
					Name:          tool.Name,
					LLMOutput:     text,
					DisplayOutput: text,
					IsError:       true,
					Metadata:      attachExecPolicyMetadata(metadata, policyMetadata),
				}, nil
			}
			cmd := exec.CommandContext(callCtx, commandPath, commandArgs...)
			cmd.Dir = skillDir
			cmd.Env = append(
				filteredEnv(execCtx.Config.Runtime.ShellEnvAllowlist),
				"GO_CLI_AGENT_ARGS_JSON="+string(raw),
				"GO_CLI_AGENT_SKILL_DIR="+skillDir,
				"GO_CLI_AGENT_SKILL_NAME="+tool.SkillName,
			)
			cmd.Stdin = bytes.NewReader(raw)
			output, err := cmd.CombinedOutput()
			exitCode := 0
			if cmd.ProcessState != nil {
				exitCode = cmd.ProcessState.ExitCode()
			}
			text, rawLength, truncated := truncateOutput(string(output), 12000)
			if text == "" {
				text = "(no output)"
			}
			if err != nil {
				interruptErr := ctx.Err()
				if interruptErr == nil {
					interruptErr = callCtx.Err()
				}
				if interruptErr != nil {
					return session.ToolResult{
						Name:          tool.Name,
						LLMOutput:     "[Tool execution was interrupted]",
						DisplayOutput: "[Tool execution was interrupted]",
						IsError:       true,
						Metadata:      attachExecPolicyMetadata(commandMetadata(timeout, exitCode, rawLength, truncated), policyMetadata),
					}, interruptErr
				}
				return session.ToolResult{
					Name:          tool.Name,
					LLMOutput:     text,
					DisplayOutput: text,
					IsError:       true,
					Metadata:      attachExecPolicyMetadata(commandMetadata(timeout, exitCode, rawLength, truncated), policyMetadata),
				}, nil
			}
			return session.ToolResult{
				Name:          tool.Name,
				LLMOutput:     text,
				DisplayOutput: text,
				Metadata:      attachExecPolicyMetadata(commandMetadata(timeout, exitCode, rawLength, truncated), policyMetadata),
			}, nil
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
	required, _ := schema["required"].([]any)
	out := make([]string, 0, len(required))
	for _, item := range required {
		name, ok := item.(string)
		if ok && strings.TrimSpace(name) != "" {
			out = append(out, name)
		}
	}
	return out
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

func renderCommand(command []string, args map[string]any) ([]string, error) {
	if len(command) == 0 {
		return nil, errors.New("command must not be empty")
	}
	var out []string
	for _, part := range command {
		tmpl, err := template.New("arg").Option("missingkey=zero").Parse(part)
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, args); err != nil {
			return nil, err
		}
		value := strings.TrimSpace(buf.String())
		if value != "" {
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("command rendered to empty argv")
	}
	return out, nil
}

func filteredEnv(allowlist []string) []string {
	allowed := map[string]struct{}{}
	for _, item := range allowlist {
		allowed[item] = struct{}{}
	}
	var out []string
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
	return text[:limit] + "\n...[truncated]", rawLength, true
}

func relativeOrAbsolute(base, path string) string {
	if rel, err := filepath.Rel(base, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}

func errorResult(tool string, err error) session.ToolResult {
	return session.ToolResult{
		Name:          tool,
		LLMOutput:     "Error: " + err.Error(),
		DisplayOutput: "Error: " + err.Error(),
		IsError:       true,
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
