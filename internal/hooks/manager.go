package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/procutil"
)

type EmitFunc func(eventType string, data map[string]any) error

type FailClosedError struct {
	Point string
	Name  string
	Err   error
}

func (e *FailClosedError) Error() string {
	return e.Err.Error()
}

func (e *FailClosedError) Unwrap() error {
	return e.Err
}

type emitError struct {
	Event   string
	Context string
	Err     error
}

func (e *emitError) Error() string {
	if e.Context != "" {
		return fmt.Sprintf("record %s event for %s: %v", e.Event, e.Context, e.Err)
	}
	return fmt.Sprintf("record %s event: %v", e.Event, e.Err)
}

func (e *emitError) Unwrap() error {
	return e.Err
}

type Manager struct {
	workdir        string
	defaultTimeout int
	points         map[string][]config.HookDefinition
	emit           EmitFunc
}

type hookExecution struct {
	payload         map[string]any
	modifiedFields  []string
	skipped         []string
	commandExitCode *int
}

const hookCommandOutputLimit = 12000

func New(cfg config.HooksConfig, workdir string) *Manager {
	return &Manager{
		workdir:        workdir,
		defaultTimeout: cfg.DefaultTimeoutSec,
		points: map[string][]config.HookDefinition{
			"session.start":          cfg.SessionStart,
			"session.awaiting_input": cfg.SessionAwaiting,
			"session.pause":          cfg.SessionPause,
			"session.complete":       cfg.SessionComplete,
			"session.fail":           cfg.SessionFail,
			"user.message":           cfg.UserMessage,
			"assistant.message":      cfg.AssistantMessage,
			"tool.before":            cfg.ToolBefore,
			"tool.after":             cfg.ToolAfter,
		},
		emit: func(string, map[string]any) error { return nil },
	}
}

func (m *Manager) SetEmitter(fn EmitFunc) {
	if fn != nil {
		m.emit = fn
	}
}

func (m *Manager) Trigger(ctx context.Context, point string, payload map[string]any) (map[string]any, error) {
	next := cloneMap(payload)
	for _, hook := range m.points[point] {
		if !matches(hook.Match, next) {
			continue
		}
		if err := m.emit("hook.triggered", map[string]any{
			"point": point,
			"name":  hook.Name,
		}); err != nil {
			return next, fmt.Errorf("record hook.triggered event for %s/%s: %w", point, hook.Name, err)
		}
		execution, err := m.runHook(ctx, hook, next)
		if err != nil {
			var eventErr *emitError
			if errors.As(err, &eventErr) {
				return next, err
			}
			data := map[string]any{
				"point":       point,
				"name":        hook.Name,
				"fail_closed": hook.FailClosed,
				"error":       err.Error(),
			}
			if execution.commandExitCode != nil {
				data["command_exit_code"] = *execution.commandExitCode
			}
			if len(execution.skipped) > 0 {
				data["skipped"] = execution.skipped
			}
			if emitErr := m.emit("hook.failed", data); emitErr != nil {
				return next, fmt.Errorf("record hook.failed event for %s/%s after %v: %w", point, hook.Name, err, emitErr)
			}
			if hook.FailClosed {
				return next, &FailClosedError{
					Point: point,
					Name:  hook.Name,
					Err:   err,
				}
			}
			continue
		}
		next = execution.payload
		data := map[string]any{
			"point": point,
			"name":  hook.Name,
		}
		if execution.commandExitCode != nil {
			data["command_exit_code"] = *execution.commandExitCode
		}
		if len(execution.modifiedFields) > 0 {
			data["modified_fields"] = execution.modifiedFields
		}
		if len(execution.skipped) > 0 {
			data["skipped"] = execution.skipped
		}
		if err := m.emit("hook.finished", data); err != nil {
			return next, fmt.Errorf("record hook.finished event for %s/%s: %w", point, hook.Name, err)
		}
	}
	return next, nil
}

func (m *Manager) runHook(ctx context.Context, hook config.HookDefinition, payload map[string]any) (hookExecution, error) {
	next := cloneMap(payload)
	execution := hookExecution{payload: next}
	if len(hook.Command) > 0 {
		timeout := hook.TimeoutSec
		if timeout <= 0 {
			timeout = m.defaultTimeout
		}
		callCtx := ctx
		var cancel context.CancelFunc
		if timeout > 0 {
			callCtx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
			defer cancel()
		}
		stdin, err := json.Marshal(next)
		if err != nil {
			return execution, err
		}
		argv := substituteVars(hook.Command, next)
		if preflight, ok := m.missingCommandPreflight(argv); ok {
			execution.skipped = append(execution.skipped, preflight.Message)
			data := map[string]any{
				"name":         hook.Name,
				"command":      argv,
				"missing_path": preflight.Path,
				"reason":       preflight.Reason,
			}
			if hook.FailClosed {
				return execution, fmt.Errorf("%s", preflight.Message)
			}
			if err := m.emit("hook.warning", data); err != nil {
				return execution, &emitError{Event: "hook.warning", Context: hook.Name, Err: err}
			}
			goto afterCommand
		}
		cmd := exec.CommandContext(callCtx, argv[0], argv[1:]...)
		procutil.PrepareCommandCancellation(cmd)
		cmd.Dir = m.workdir
		cmd.Env = minimalEnv(next)
		cmd.Stdin = bytes.NewReader(stdin)
		output, err := cmd.CombinedOutput()
		exitCode := 0
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
		text, rawLength, truncated := truncateHookOutput(string(output), hookCommandOutputLimit)
		timedOut := callCtx.Err() == context.DeadlineExceeded
		if emitErr := m.emit("hook.command", map[string]any{
			"name":       hook.Name,
			"output":     text,
			"raw_length": rawLength,
			"truncated":  truncated,
			"timeout":    timedOut,
			"exit_code":  exitCode,
		}); emitErr != nil {
			return execution, &emitError{Event: "hook.command", Context: hook.Name, Err: emitErr}
		}
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
				execution.commandExitCode = &exitCode
			}
			return execution, err
		}
		execution.commandExitCode = &exitCode
	}
afterCommand:
	if hook.Inject != nil {
		field := hook.Inject.Field
		rawValue, exists := next[field]
		if !exists {
			execution.skipped = append(execution.skipped, fmt.Sprintf("inject skipped: field %q missing", field))
		} else if value, ok := rawValue.(string); !ok {
			execution.skipped = append(execution.skipped, fmt.Sprintf("inject skipped: field %q is not a string", field))
		} else {
			updated := value
			if hook.Inject.Set != "" {
				updated = hook.Inject.Set
			} else {
				updated = hook.Inject.Prefix + value + hook.Inject.Suffix
			}
			if updated != value {
				next[field] = updated
				execution.modifiedFields = appendUniqueString(execution.modifiedFields, field)
			}
		}
	}
	if hook.Filter != nil {
		field := hook.Filter.Field
		rawValue, exists := next[field]
		if !exists {
			execution.skipped = append(execution.skipped, fmt.Sprintf("filter skipped: field %q missing", field))
		} else if value, ok := rawValue.(string); !ok {
			execution.skipped = append(execution.skipped, fmt.Sprintf("filter skipped: field %q is not a string", field))
		} else {
			if hook.Filter.RejectIfContains != "" && strings.Contains(value, hook.Filter.RejectIfContains) {
				return execution, fmt.Errorf("hook rejected payload by field %s", field)
			}
		}
	}
	return execution, nil
}

func truncateHookOutput(output string, limit int) (string, int, bool) {
	rawLength := len(output)
	if limit <= 0 || rawLength <= limit {
		return output, rawLength, false
	}
	if limit < len("\n[... truncated ...]") {
		return prefixAtRuneBoundary(output, limit), rawLength, true
	}
	suffix := "\n[... truncated ...]"
	return prefixAtRuneBoundary(output, limit-len(suffix)) + suffix, rawLength, true
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

type hookCommandPreflight struct {
	Path    string
	Reason  string
	Message string
}

func (m *Manager) missingCommandPreflight(argv []string) (hookCommandPreflight, bool) {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return hookCommandPreflight{
			Reason:  "empty_command",
			Message: "hook command is empty",
		}, true
	}
	if path, ok := missingExecutablePath(m.workdir, argv[0]); ok {
		return hookCommandPreflight{
			Path:    path,
			Reason:  "missing_executable",
			Message: fmt.Sprintf("hook command executable is missing: %s", path),
		}, true
	}
	if path, ok := missingShellScriptOperand(m.workdir, argv); ok {
		return hookCommandPreflight{
			Path:    path,
			Reason:  "missing_shell_script",
			Message: fmt.Sprintf("hook shell script is missing: %s", path),
		}, true
	}
	return hookCommandPreflight{}, false
}

func missingExecutablePath(workdir, command string) (string, bool) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", false
	}
	if strings.ContainsAny(command, `/\`) {
		path := resolveHookPath(workdir, command)
		if _, err := os.Stat(path); err != nil && os.IsNotExist(err) {
			return path, true
		}
		return "", false
	}
	if _, err := exec.LookPath(command); err != nil {
		return command, true
	}
	return "", false
}

func missingShellScriptOperand(workdir string, argv []string) (string, bool) {
	if len(argv) < 2 || !isShellExecutable(argv[0]) {
		return "", false
	}
	for i := 1; i < len(argv); i++ {
		arg := strings.TrimSpace(argv[i])
		if arg == "" {
			continue
		}
		if arg == "-c" {
			return "", false
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		path := resolveHookPath(workdir, arg)
		if _, err := os.Stat(path); err != nil && os.IsNotExist(err) {
			return path, true
		}
		return "", false
	}
	return "", false
}

func isShellExecutable(command string) bool {
	base := filepath.Base(strings.TrimSpace(command))
	switch base {
	case "sh", "bash", "dash", "zsh", "ksh":
		return true
	default:
		return false
	}
}

func resolveHookPath(workdir, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(workdir, path))
}

func matches(match config.HookMatch, payload map[string]any) bool {
	if match.Tool != "" {
		if payload["tool_name"] != match.Tool {
			return false
		}
	}
	if match.Mode != "" {
		if payload["mode"] != match.Mode {
			return false
		}
	}
	if match.Status != "" {
		if payload["status"] != match.Status {
			return false
		}
	}
	return true
}

func cloneMap(input map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range input {
		out[key] = value
	}
	return out
}

func substituteVars(command []string, payload map[string]any) []string {
	replacer := strings.NewReplacer(
		"$SESSION_ID", fmt.Sprint(payload["session_id"]),
		"$WORKDIR", fmt.Sprint(payload["workdir"]),
		"$TOOL_NAME", fmt.Sprint(payload["tool_name"]),
		"$STATUS", fmt.Sprint(payload["status"]),
		"$FILE", fmt.Sprint(payload["file"]),
	)
	out := make([]string, 0, len(command))
	for _, part := range command {
		out = append(out, replacer.Replace(part))
	}
	return out
}

func minimalEnv(payload map[string]any) []string {
	var out []string
	for _, key := range []string{"PATH", "HOME", "LANG", "TERM"} {
		if value, ok := os.LookupEnv(key); ok {
			out = append(out, key+"="+value)
		}
	}
	if sessionID, ok := payload["session_id"].(string); ok {
		out = append(out, "SESSION_ID="+sessionID)
	}
	return out
}

func appendUniqueString(items []string, value string) []string {
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}
