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
	"strings"
	"text/template"
	"time"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"

	"go-cli-agent/internal/config"
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
	"finish": {}, "load_skill": {}, "todo_write": {}, "todo_read": {}, "task_create": {},
	"task_update": {}, "task_list": {}, "task_get": {}, "agent_spawn": {}, "agent_status": {},
	"agent_list": {}, "feature_list_create": {}, "feature_list_update": {}, "feature_list_read": {},
}

func NewRegistry(cfg *config.Config, catalog *skills.Catalog, store *session.Store, control ControlPlane) (*Registry, error) {
	registry := &Registry{defs: map[string]Definition{}, control: control}
	for _, def := range builtinDefinitions(cfg, catalog, control) {
		registry.Register(def)
	}
	if catalog != nil {
		for _, tool := range catalog.CommandTools() {
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
			"content": map[string]any{"type": "string"},
			"status": map[string]any{
				"type": "string",
				"enum": []string{"pending", "in_progress", "completed", "cancelled"},
			},
			"priority": map[string]any{
				"type": "string",
				"enum": []string{"high", "medium", "low"},
			},
			"updated_at": map[string]any{"type": "string"},
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

func defShell() Definition {
	return Definition{
		Name:            "shell",
		Description:     "Run shell command in workspace.",
		Ephemeral:       true,
		EphemeralWindow: 2,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string"},
				"timeout": map[string]any{"type": "integer"},
				"workdir": map[string]any{"type": "string"},
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
			if strings.TrimSpace(input.Workdir) != "" {
				resolvedWorkdir, err := ResolveWorkspacePath(execCtx.Workdir, input.Workdir)
				if err != nil {
					return errorResult("shell", err), nil
				}
				info, err := os.Stat(resolvedWorkdir)
				if err != nil {
					return errorResult("shell", err), nil
				}
				if !info.IsDir() {
					return errorResult("shell", fmt.Errorf("workdir is not a directory: %s", relativeOrAbsolute(execCtx.Workdir, resolvedWorkdir))), nil
				}
				workdir = resolvedWorkdir
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
			commandPath, commandArgs, sandboxStatus := shellSandboxCommand(shellSandbox, workdir, command, shellArg, input.Command)
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
						Metadata: map[string]any{
							"command":    input.Command,
							"exit_code":  exitCode,
							"timeout":    timeout,
							"workdir":    workdir,
							"sandbox":    sandboxStatus,
							"raw_length": rawLength,
							"truncated":  truncated,
						},
					}, interruptErr
				}
				return session.ToolResult{
					Name:          "shell",
					LLMOutput:     text,
					DisplayOutput: text,
					IsError:       true,
					Metadata: map[string]any{
						"command":    input.Command,
						"exit_code":  exitCode,
						"timeout":    timeout,
						"workdir":    workdir,
						"sandbox":    sandboxStatus,
						"raw_length": rawLength,
						"truncated":  truncated,
					},
				}, nil
			}
			return session.ToolResult{
				Name:          "shell",
				LLMOutput:     text,
				DisplayOutput: text,
				Metadata: map[string]any{
					"command":    input.Command,
					"exit_code":  exitCode,
					"timeout":    timeout,
					"workdir":    workdir,
					"sandbox":    sandboxStatus,
					"raw_length": rawLength,
					"truncated":  truncated,
				},
			}, nil
		},
	}
}

func defReadFile() Definition {
	return Definition{
		Name:        "read_file",
		Description: "Read file lines with offset/limit (max 120 lines per call).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string"},
				"offset": map[string]any{"type": "integer"},
				"limit":  map[string]any{"type": "integer"},
			},
			"required": []string{"path"},
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
			path, err := ResolveWorkspacePath(execCtx.Workdir, input.Path)
			if err != nil {
				return errorResult("read_file", err), nil
			}
			if isInternalGeneratedArtifactPath(execCtx.Workdir, path) {
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
			selected = annotateReadWindow(execCtx.Workdir, path, offset, end, len(lines), input.Limit, capped, selected)
			return session.ToolResult{
				Name:          "read_file",
				LLMOutput:     selected,
				DisplayOutput: selected,
				Metadata: map[string]any{
					"path":   path,
					"offset": offset,
					"end":    end,
				},
			}, nil
		},
	}
}

func defWriteFile() Definition {
	return Definition{
		Name:        "write_file",
		Description: "Write file to workspace (creates parent dirs).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
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
		Description: "Replace exact text in file.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":     map[string]any{"type": "string"},
				"old_text": map[string]any{"type": "string"},
				"new_text": map[string]any{"type": "string"},
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
		Description:     "Match files by glob pattern.",
		Ephemeral:       true,
		EphemeralWindow: 3,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string"},
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
		Description: "Search file contents recursively (skips build artifacts).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string"},
				"path":    map[string]any{"type": "string"},
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
		Description:     "Find files matching pattern (returns paths only).",
		Ephemeral:       true,
		EphemeralWindow: 3,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string"},
				"path":    map[string]any{"type": "string"},
				"include": map[string]any{"type": "string"},
				"limit":   map[string]any{"type": "integer"},
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
		if part == ".artifacts" {
			return true
		}
	}
	return false
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
		Description: "Mark task complete.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{"type": "string"},
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
	description := "Load a registered skill definition by exact name."
	if len(availableSkills) > 0 {
		nameSchema["enum"] = availableSkills
		description = fmt.Sprintf("Load a registered skill definition by exact name. Available skills: %s.", strings.Join(availableSkills, ", "))
	}
	return Definition{
		Name:        "load_skill",
		Description: description,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": nameSchema,
			},
			"required":             []string{"name"},
			"additionalProperties": false,
		},
		Execute: func(_ context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			var input struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return errorResult("load_skill", err), nil
			}
			if execCtx.Catalog == nil {
				return errorResult("load_skill", errors.New("skill catalog not available")), nil
			}
			body, err := execCtx.Catalog.LoadBody(input.Name)
			if err != nil {
				if available := execCtx.Catalog.Names(); len(available) > 0 {
					err = fmt.Errorf("%w; available skills: %s", err, strings.Join(available, ", "))
				}
				return errorResult("load_skill", err), nil
			}
			skill, _ := execCtx.Catalog.Load(input.Name)
			skillDir := filepath.Dir(skill.Path)
			shellWorkdir := relativeOrAbsolute(execCtx.Workdir, skillDir)
			output := fmt.Sprintf("<skill path=%q shell_workdir=%q>\nWhen this skill uses relative shell paths, call the shell tool with `workdir=%q` so commands run from the skill bundle root.\n\n%s\n</skill>", skill.Path, shellWorkdir, shellWorkdir, body)
			return session.ToolResult{
				Name:          "load_skill",
				LLMOutput:     output,
				DisplayOutput: fmt.Sprintf("Loaded skill: %s", input.Name),
				Metadata: map[string]any{
					"path":          skill.Path,
					"shell_workdir": shellWorkdir,
				},
			}, nil
		},
	}
}

func defTodoWrite() Definition {
	return Definition{
		Name:        "todo_write",
		Description: "Write session todo list.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"todos": map[string]any{
					"type":  "array",
					"items": todoItemSchema(),
				},
			},
			"required": []string{"todos"},
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
			if err := execCtx.Store.SaveTodo(execCtx.SessionID, input.Todos); err != nil {
				return errorResult("todo_write", err), nil
			}
			if execCtx.Emit != nil {
				execCtx.Emit("todo.updated", map[string]any{
					"count": len(input.Todos),
				})
			}
			data, _ := json.MarshalIndent(input.Todos, "", "  ")
			return session.ToolResult{
				Name:          "todo_write",
				LLMOutput:     string(data),
				DisplayOutput: string(data),
				Metadata: map[string]any{
					"path":  todoFilePath(execCtx),
					"count": len(input.Todos),
				},
			}, nil
		},
	}
}

func defTodoRead() Definition {
	return Definition{
		Name:        "todo_read",
		Description: "Read session todo list.",
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

func defTaskCreate() Definition {
	return Definition{
		Name:        "task_create",
		Description: "Create task node.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"subject":     map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
				"priority":    map[string]any{"type": "string"},
				"blocked_by":  stringArraySchema(),
				"labels":      stringArraySchema(),
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
		Description: "Update task node.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id":           map[string]any{"type": "string"},
				"status":            map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed", "cancelled"}},
				"subject":           map[string]any{"type": "string"},
				"description":       map[string]any{"type": "string"},
				"priority":          map[string]any{"type": "string", "enum": []string{"high", "medium", "low"}},
				"owner":             map[string]any{"type": "string"},
				"add_blocked_by":    stringArraySchema(),
				"remove_blocked_by": stringArraySchema(),
				"add_blocks":        stringArraySchema(),
				"remove_blocks":     stringArraySchema(),
				"append_note":       map[string]any{"type": "string"},
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
		Description: "List task graph with ready/blocked views.",
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
		Description: "Read task node by ID.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string"},
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
		Description: "Spawn a child agent when the model decides delegation would improve coverage, independence, or context control. Consider this for broad investigations, separable long-running slices, code audits, module scans, independent validation, or reviewer/evaluator passes; keep tiny single-file checks in the parent. Child sessions and background jobs are durable facts, and their results should be reconciled before final parent conclusions. Delegation is optional and model-led. Use isolation_mode=auto when the child must write artifacts.",
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
					"description": "Child role hint. evaluator fits review, audit, validation, and reviewer work; planner fits decomposition; generator fits bounded drafting or implementation.",
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
	return Definition{
		Name:        tool.Name,
		Description: fmt.Sprintf("Direct-call skill command tool from skill %s. Call this tool directly by name; do not search the workspace, skill files, or shell PATH for it. This tool executes from the skill directory. %s", tool.SkillName, description),
		InputSchema: tool.InputSchema,
		Execute: func(ctx context.Context, execCtx ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			args, err := decodeCommandToolArgs(raw)
			if err != nil {
				return errorResult(tool.Name, err), nil
			}
			if err := validateCommandToolInput(tool.InputSchema, args); err != nil {
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
			cmd := exec.CommandContext(callCtx, argv[0], argv[1:]...)
			cmd.Dir = skillDir
			cmd.Env = append(
				filteredEnv(execCtx.Config.Runtime.ShellEnvAllowlist),
				"GO_CLI_AGENT_ARGS_JSON="+string(raw),
				"GO_CLI_AGENT_SKILL_DIR="+skillDir,
				"GO_CLI_AGENT_SKILL_NAME="+tool.SkillName,
			)
			cmd.Stdin = bytes.NewReader(raw)
			output, err := cmd.CombinedOutput()
			text, rawLength, truncated := truncateOutput(string(output), 12000)
			if text == "" {
				text = "(no output)"
			}
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return session.ToolResult{
						Name:          tool.Name,
						LLMOutput:     "[Tool execution was interrupted]",
						DisplayOutput: "[Tool execution was interrupted]",
						IsError:       true,
						Metadata: map[string]any{
							"skill_name": tool.SkillName,
							"skill_path": tool.SkillPath,
							"workdir":    skillDir,
							"raw_length": rawLength,
							"truncated":  truncated,
						},
					}, err
				}
				return session.ToolResult{
					Name:          tool.Name,
					LLMOutput:     text,
					DisplayOutput: text,
					IsError:       true,
					Metadata: map[string]any{
						"skill_name": tool.SkillName,
						"skill_path": tool.SkillPath,
						"workdir":    skillDir,
						"raw_length": rawLength,
						"truncated":  truncated,
					},
				}, nil
			}
			return session.ToolResult{
				Name:          tool.Name,
				LLMOutput:     text,
				DisplayOutput: text,
				Metadata: map[string]any{
					"skill_name": tool.SkillName,
					"skill_path": tool.SkillPath,
					"workdir":    skillDir,
					"raw_length": rawLength,
					"truncated":  truncated,
				},
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

func writeAtomically(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
