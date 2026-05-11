package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/events"
	"go-cli-agent/internal/fileutil"
	"go-cli-agent/internal/hooks"
	"go-cli-agent/internal/output"
	"go-cli-agent/internal/provider"
	"go-cli-agent/internal/runtime"
	"go-cli-agent/internal/session"
	"go-cli-agent/internal/skills"
)

type coreRunner interface {
	Start(context.Context, runtime.StartRequest) (runtime.RunResult, error)
	Continue(context.Context, runtime.ContinueRequest) (runtime.RunResult, error)
	Steer(context.Context, runtime.SteerRequest) (runtime.SteerResult, error)
	Probe(context.Context, runtime.ProbeRequest) (runtime.ProbeResult, error)
	Interrupt(string) error
	Tasks(string) (session.TaskBoard, error)
	List(int) ([]session.SessionSummary, error)
	Bus() *events.Bus
}

type storeRunner interface {
	Store() *session.Store
}

type experimentalRunner interface {
	storeRunner
	Delegate(context.Context, runtime.DelegateRequest) (runtime.DelegateResult, error)
	QueueSubmit(context.Context, runtime.QueueSubmitRequest) (session.QueueJob, error)
	QueueShow(string) (session.QueueJob, error)
	QueueList(int) ([]session.QueueJob, error)
	ProcessNextJob(context.Context) (session.QueueJob, bool, error)
}

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return classifyCommandError(usage(stderr))
	}
	var err error
	switch args[0] {
	case "init":
		err = runInit(args[1:], stdout, stderr)
	case "run":
		err = runCommand(ctx, "run", args[1:], stdout, stderr)
	case "exec":
		err = runCommand(ctx, "exec", args[1:], stdout, stderr)
	case "continue":
		err = continueCommand(ctx, args[1:], stdout, stderr)
	case "steer":
		err = steerCommand(ctx, args[1:], stdout, stderr)
	case "sessions":
		err = sessionsCommand(args[1:], stdout)
	case "tasks":
		err = tasksCommand(args[1:], stdout, stderr)
	case "probe-provider":
		err = probeProviderCommand(ctx, args[1:], stdout, stderr)
	case "doctor":
		err = doctorCommand(ctx, args[1:], stdout, stderr)
	case "experimental":
		err = experimentalCommand(ctx, args[1:], stdout, stderr)
	default:
		if isExperimentalSubcommand(args[0]) {
			err = fmt.Errorf("experimental command %q moved under `go-cli-agent experimental %s`", args[0], args[0])
		} else {
			err = usage(stderr)
		}
	}
	return classifyCommandError(err)
}

func usage(w io.Writer) error {
	_, _ = fmt.Fprintln(w, "usage: go-cli-agent <init|run|exec|continue|steer|sessions|tasks|probe-provider|doctor> [...]")
	return flag.ErrHelp
}

func experimentalUsage(w io.Writer) error {
	_, _ = fmt.Fprintln(w, "usage: go-cli-agent experimental <delegate|children|queue|tui|web> [...]")
	return flag.ErrHelp
}

func experimentalCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return experimentalUsage(stderr)
	}
	switch args[0] {
	case "delegate":
		return delegateCommand(ctx, args[1:], stdout, stderr)
	case "children":
		return childrenCommand(args[1:], stdout, stderr)
	case "queue":
		return queueCommand(ctx, args[1:], stdout, stderr)
	case "tui":
		return tuiCommand(args[1:], stdout, stderr)
	case "web":
		return webCommand(ctx, args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown experimental subcommand: %s", args[0])
	}
}

func isExperimentalSubcommand(name string) bool {
	switch name {
	case "delegate", "children", "queue", "tui", "web":
		return true
	default:
		return false
	}
}

type ExitError struct {
	Code int
}

func (e ExitError) Error() string {
	return fmt.Sprintf("exit with status %d", e.Code)
}

type ClassifiedError struct {
	Code int
	Err  error
}

func (e ClassifiedError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("exit with status %d", e.Code)
}

func (e ClassifiedError) Unwrap() error {
	return e.Err
}

func loadConfig(configPath, workdir string) (*config.Config, error) {
	if err := config.LoadEnvFile(config.DefaultEnvFilePath(workdir)); err != nil {
		return nil, runtime.WrapConfigError(err)
	}
	cfg, err := config.Load(configPath, workdir)
	if err != nil {
		return nil, runtime.WrapConfigError(err)
	}
	return cfg, nil
}

func loadRunner(configPath, workdir string) (coreRunner, *config.Config, error) {
	cfg, err := loadConfig(configPath, workdir)
	if err != nil {
		return nil, nil, err
	}
	return runtime.NewCoreRunner(cfg), cfg, nil
}

func loadExperimentalRunner(configPath, workdir string) (experimentalRunner, *config.Config, error) {
	cfg, err := loadConfig(configPath, workdir)
	if err != nil {
		return nil, nil, err
	}
	return runtime.NewExperimentalRunner(cfg), cfg, nil
}

func loadStoreRunner(configPath, workdir string) (storeRunner, *config.Config, error) {
	cfg, err := loadConfig(configPath, workdir)
	if err != nil {
		return nil, nil, err
	}
	return runtime.NewStoreView(cfg), cfg, nil
}

var runnerLoader = loadRunner
var experimentalRunnerLoader = loadExperimentalRunner
var storeRunnerLoader = loadStoreRunner

func runCommand(ctx context.Context, mode string, args []string, stdout, stderr io.Writer) error {
	args = normalizeInterspersedFlags(args, []string{"provider", "model", "config", "workdir", "system", "timeout", "isolation", "isolation-root"}, []string{"json", "init"})
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		providerName  = fs.String("provider", "", "")
		model         = fs.String("model", "", "")
		configPath    = fs.String("config", "", "")
		workdir       = fs.String("workdir", "", "")
		system        = fs.String("system", "", "")
		jsonMode      = fs.Bool("json", false, "")
		initMode      = fs.Bool("init", false, "")
		timeoutSec    = fs.Int("timeout", 0, "")
		isolationMode = fs.String("isolation", "", "")
		isolationRoot = fs.String("isolation-root", "", "")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	invokeCWD, err := os.Getwd()
	if err != nil {
		return err
	}
	runner, _, err := runnerLoader(*configPath, invokeCWD)
	if err != nil {
		return err
	}
	if mode == "run" && !term.IsTerminal(int(os.Stdin.Fd())) && !*jsonMode {
		_, _ = fmt.Fprintln(stderr, "warning: stdin is not a TTY; Esc interrupt is disabled in run mode. Prefer exec for zero-interaction runs.")
	}
	renderer := output.New(*jsonMode, stdout)
	sub := runner.Bus().Subscribe(128)
	var sessionID string
	var sessionMu sync.RWMutex
	done := make(chan struct{})
	renderCtx, cancelRender := context.WithCancel(ctx)
	go func() {
		defer close(done)
		for {
			select {
			case evt := <-sub:
				if evt.Type == "session.started" {
					sessionMu.Lock()
					sessionID = evt.SessionID
					sessionMu.Unlock()
				}
				renderer.Handle(evt)
			case <-renderCtx.Done():
				return
			}
		}
	}()

	prompt, err := resolvePrompt(fs.Args(), os.Stdin)
	if err != nil {
		return err
	}
	runCtx := ctx
	var cancel context.CancelFunc
	if *timeoutSec > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(*timeoutSec)*time.Second)
	} else {
		runCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	if mode == "run" && term.IsTerminal(int(os.Stdin.Fd())) {
		go watchEsc(runCtx, func() {
			sessionMu.RLock()
			id := sessionID
			sessionMu.RUnlock()
			if id != "" {
				_ = runner.Interrupt(id)
			}
		})
	}
	actualMode := mode
	if *initMode {
		actualMode = session.ModeInit
	}
	result, err := runner.Start(runCtx, runtime.StartRequest{
		Prompt:         prompt,
		Provider:       *providerName,
		Model:          *model,
		Workdir:        *workdir,
		Mode:           actualMode,
		SystemOverride: *system,
		IsolationMode:  *isolationMode,
		IsolationRoot:  *isolationRoot,
	})
	cancel()
	cancelRender()
	<-done
	if err != nil {
		return err
	}
	return printResult(stdout, *jsonMode, result, mapStatusToExitCode(result.Status, result.LastError))
}

func continueCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	args = normalizeInterspersedFlags(args, []string{"message", "provider", "model", "config", "system"}, []string{"json"})
	fs := flag.NewFlagSet("continue", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		message    = fs.String("message", "", "")
		provider   = fs.String("provider", "", "")
		model      = fs.String("model", "", "")
		configPath = fs.String("config", "", "")
		jsonMode   = fs.Bool("json", false, "")
		system     = fs.String("system", "", "")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("continue requires <session-id>")
	}
	cwd, _ := os.Getwd()
	runner, _, err := runnerLoader(*configPath, cwd)
	if err != nil {
		return err
	}
	renderer := output.New(*jsonMode, stdout)
	sub := runner.Bus().Subscribe(128)
	done := make(chan struct{})
	renderCtx, cancelRender := context.WithCancel(ctx)
	go func() {
		defer close(done)
		for {
			select {
			case evt := <-sub:
				renderer.Handle(evt)
			case <-renderCtx.Done():
				return
			}
		}
	}()
	if strings.TrimSpace(*message) == "" && !term.IsTerminal(int(os.Stdin.Fd())) {
		data, _ := io.ReadAll(os.Stdin)
		*message = string(data)
	}
	result, err := runner.Continue(ctx, runtime.ContinueRequest{
		SessionID:      fs.Arg(0),
		Message:        strings.TrimSpace(*message),
		Provider:       *provider,
		Model:          *model,
		SystemOverride: *system,
	})
	cancelRender()
	<-done
	if err != nil {
		return err
	}
	return printResult(stdout, *jsonMode, result, mapStatusToExitCode(result.Status, result.LastError))
}

func steerCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	args = normalizeInterspersedFlags(args, []string{"message", "config"}, []string{"interrupt", "json"})
	fs := flag.NewFlagSet("steer", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		message    = fs.String("message", "", "")
		interrupt  = fs.Bool("interrupt", false, "")
		configPath = fs.String("config", "", "")
		jsonMode   = fs.Bool("json", false, "")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("steer requires <session-id>")
	}
	cwd, _ := os.Getwd()
	runner, _, err := runnerLoader(*configPath, cwd)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*message) == "" && !term.IsTerminal(int(os.Stdin.Fd())) {
		data, _ := io.ReadAll(os.Stdin)
		*message = string(data)
	}
	result, err := runner.Steer(ctx, runtime.SteerRequest{
		SessionID: fs.Arg(0),
		Message:   strings.TrimSpace(*message),
		Interrupt: *interrupt,
	})
	if *jsonMode {
		_ = json.NewEncoder(stdout).Encode(steerJSONPayload(fs.Arg(0), result, err))
		if err != nil {
			return ExitError{Code: 1}
		}
		return nil
	}
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "steer queued for %s (%s)\n", result.SessionID, result.Behavior)
	return nil
}

func sessionsCommand(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("sessions", flag.ContinueOnError)
	var (
		configPath = fs.String("config", "", "")
		limit      = fs.Int("limit", 20, "")
		jsonMode   = fs.Bool("json", false, "")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	runner, _, err := runnerLoader(*configPath, cwd)
	if err != nil {
		return err
	}
	sessions, err := runner.List(*limit)
	if err != nil {
		return err
	}
	if sessions == nil {
		sessions = []session.SessionSummary{}
	}
	if *jsonMode {
		return json.NewEncoder(stdout).Encode(sessions)
	}
	for _, item := range sessions {
		_, _ = fmt.Fprintf(stdout, "%s  %s  %s  %s  created=%s  updated=%s  phase=%s\n",
			item.ID, item.Status, item.Provider, item.Model, item.CreatedAt, item.UpdatedAt, item.Phase)
	}
	return nil
}

func tasksCommand(args []string, stdout, stderr io.Writer) error {
	args = normalizeInterspersedFlags(args, []string{"config"}, []string{"json", "all"})
	fs := flag.NewFlagSet("tasks", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		configPath = fs.String("config", "", "")
		jsonMode   = fs.Bool("json", false, "")
		showAll    = fs.Bool("all", false, "")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("tasks requires <session-id>")
	}
	cwd, _ := os.Getwd()
	runner, _, err := runnerLoader(*configPath, cwd)
	if err != nil {
		return err
	}
	board, err := runner.Tasks(fs.Arg(0))
	if err != nil {
		return err
	}
	board = normalizeTaskBoard(board)
	if *jsonMode {
		return json.NewEncoder(stdout).Encode(board)
	}
	fmt.Fprintln(stdout, "== todo ==")
	for _, item := range board.Todo {
		fmt.Fprintf(stdout, "[%s] %s\n", marker(item.Status), item.Content)
	}
	fmt.Fprintln(stdout, "\n== tasks ==")
	for _, group := range []string{"in_progress", "ready", "blocked", "completed"} {
		tasks := board.Groups[group]
		if len(tasks) == 0 {
			continue
		}
		fmt.Fprintln(stdout, strings.ToUpper(group))
		for _, task := range tasks {
			fmt.Fprintf(stdout, "- %s %s\n", task.ID, task.Subject)
		}
	}
	if *showAll {
		fmt.Fprintln(stdout, "\nALL")
		for _, task := range board.Tasks {
			fmt.Fprintf(stdout, "- %s [%s] %s\n", task.ID, task.Status, task.Subject)
		}
	}
	return nil
}

func probeProviderCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("probe-provider", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		providerName = fs.String("provider", "", "")
		model        = fs.String("model", "", "")
		configPath   = fs.String("config", "", "")
		baseURL      = fs.String("base-url", "", "")
		apiKeyEnv    = fs.String("api-key-env", "", "")
		apiProvider  = fs.String("api-provider", "", "")
		wireAPI      = fs.String("wire-api", "", "")
		prompt       = fs.String("prompt", "", "")
		jsonMode     = fs.Bool("json", false, "")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	runner, cfg, err := runnerLoader(*configPath, cwd)
	if err != nil {
		return err
	}
	req := runtime.ProbeRequest{
		Provider:    *providerName,
		Model:       *model,
		BaseURL:     *baseURL,
		APIKeyEnv:   *apiKeyEnv,
		APIProvider: *apiProvider,
		WireAPI:     *wireAPI,
		Prompt:      *prompt,
	}
	result, err := runner.Probe(ctx, req)
	if *jsonMode {
		_ = json.NewEncoder(stdout).Encode(probeProviderJSONPayload(cfg, req, result, err))
		if err != nil {
			return ExitError{Code: 1}
		}
		return nil
	}
	if err != nil {
		fmt.Fprintf(stderr, "probe failed: %s\n", err)
		return err
	}
	fmt.Fprintf(stdout, "provider: %s\nmodel: %s\nbase_url: %s\n", result.Provider, result.Model, result.BaseURL)
	if result.APIProvider != "" {
		fmt.Fprintf(stdout, "api_provider: %s\n", result.APIProvider)
	}
	if result.WireAPI != "" {
		fmt.Fprintf(stdout, "wire_api: %s\n", result.WireAPI)
	}
	fmt.Fprintf(stdout, "stop_reason: %s\n", result.StopReason)
	if result.FinishMessage != "" {
		fmt.Fprintf(stdout, "finish_message: %s\n", result.FinishMessage)
	}
	if len(result.ToolCallNames) > 0 {
		fmt.Fprintf(stdout, "tool_calls: %s\n", strings.Join(result.ToolCallNames, ", "))
	}
	if result.Text != "" {
		fmt.Fprintf(stdout, "text: %s\n", result.Text)
	}
	return nil
}

type probeProviderJSON struct {
	Provider      string         `json:"provider,omitempty"`
	Model         string         `json:"model,omitempty"`
	BaseURL       string         `json:"base_url,omitempty"`
	APIProvider   string         `json:"api_provider,omitempty"`
	WireAPI       string         `json:"wire_api,omitempty"`
	StopReason    string         `json:"stop_reason,omitempty"`
	ToolCallNames []string       `json:"tool_call_names,omitempty"`
	FinishMessage string         `json:"finish_message,omitempty"`
	Text          string         `json:"text,omitempty"`
	Usage         provider.Usage `json:"usage,omitempty"`
	Error         string         `json:"error,omitempty"`
}

type steerJSON struct {
	SessionID   string `json:"session_id,omitempty"`
	Accepted    bool   `json:"accepted"`
	Behavior    string `json:"behavior,omitempty"`
	Error       string `json:"error,omitempty"`
	Code        string `json:"code,omitempty"`
	MaxChars    int    `json:"max_chars,omitempty"`
	ActualChars int    `json:"actual_chars,omitempty"`
}

func steerJSONPayload(sessionID string, result runtime.SteerResult, err error) steerJSON {
	payload := steerJSON{
		SessionID: result.SessionID,
		Accepted:  result.Accepted,
		Behavior:  result.Behavior,
	}
	if payload.SessionID == "" {
		payload.SessionID = strings.TrimSpace(sessionID)
	}
	if err != nil {
		payload.Error = err.Error()
		payload.Accepted = false
		var inputErr runtime.SteerValidationError
		if errors.As(err, &inputErr) {
			payload.Code = inputErr.Code
			payload.MaxChars = inputErr.MaxChars
			payload.ActualChars = inputErr.ActualChars
		}
	}
	return payload
}

func probeProviderJSONPayload(cfg *config.Config, req runtime.ProbeRequest, result runtime.ProbeResult, err error) probeProviderJSON {
	payload := probeProviderJSON{
		Provider:      result.Provider,
		Model:         result.Model,
		BaseURL:       result.BaseURL,
		APIProvider:   result.APIProvider,
		WireAPI:       result.WireAPI,
		StopReason:    result.StopReason,
		ToolCallNames: result.ToolCallNames,
		FinishMessage: result.FinishMessage,
		Text:          result.Text,
		Usage:         result.Usage,
	}
	providerName := strings.TrimSpace(req.Provider)
	if providerName == "" && cfg != nil {
		providerName = strings.TrimSpace(cfg.DefaultProvider)
	}
	if payload.Provider == "" {
		payload.Provider = providerName
	}
	if cfg != nil && providerName != "" {
		if providerCfg, ok := cfg.Providers[providerName]; ok {
			if payload.Model == "" {
				if strings.TrimSpace(req.Model) != "" {
					payload.Model = strings.TrimSpace(req.Model)
				} else {
					payload.Model = strings.TrimSpace(providerCfg.Model)
				}
			}
			if payload.BaseURL == "" {
				if strings.TrimSpace(req.BaseURL) != "" {
					payload.BaseURL = strings.TrimSpace(req.BaseURL)
				} else {
					payload.BaseURL = strings.TrimSpace(providerCfg.BaseURL)
				}
			}
			if payload.WireAPI == "" {
				if strings.TrimSpace(req.WireAPI) != "" {
					payload.WireAPI = strings.TrimSpace(req.WireAPI)
				} else {
					payload.WireAPI = strings.TrimSpace(providerCfg.WireAPI)
				}
			}
			if payload.APIProvider == "" {
				if strings.TrimSpace(req.APIProvider) != "" {
					payload.APIProvider = strings.TrimSpace(req.APIProvider)
				} else if effective, effectiveErr := config.EffectiveAPIProvider(providerName, providerCfg); effectiveErr == nil {
					payload.APIProvider = effective
				}
			}
		}
	}
	if payload.Model == "" {
		payload.Model = strings.TrimSpace(req.Model)
	}
	if payload.BaseURL == "" {
		payload.BaseURL = strings.TrimSpace(req.BaseURL)
	}
	if payload.WireAPI == "" {
		payload.WireAPI = strings.TrimSpace(req.WireAPI)
	}
	if err != nil {
		payload.Error = err.Error()
	}
	return payload
}

type doctorCheck struct {
	Name    string         `json:"name"`
	Status  string         `json:"status"`
	Details map[string]any `json:"details,omitempty"`
}

type doctorReport struct {
	ConfigPath      string        `json:"config_path,omitempty"`
	DefaultProvider string        `json:"default_provider,omitempty"`
	Checks          []doctorCheck `json:"checks"`
}

func doctorCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		providerName = fs.String("provider", "", "")
		model        = fs.String("model", "", "")
		configPath   = fs.String("config", "", "")
		baseURL      = fs.String("base-url", "", "")
		apiKeyEnv    = fs.String("api-key-env", "", "")
		wireAPI      = fs.String("wire-api", "", "")
		prompt       = fs.String("prompt", "", "")
		skipProbe    = fs.Bool("skip-probe", false, "")
		jsonMode     = fs.Bool("json", false, "")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cwd, _ := os.Getwd()
	runner, cfg, err := runnerLoader(*configPath, cwd)
	if err != nil {
		return err
	}

	report := doctorReport{
		ConfigPath:      defaultString(*configPath, filepath.Join(cwd, ".go-cli-agent", "config.yaml")),
		DefaultProvider: cfg.DefaultProvider,
	}

	configStatus := "ok"
	configDetails := map[string]any{"path": report.ConfigPath}
	if info, err := os.Stat(report.ConfigPath); err != nil {
		if os.IsNotExist(err) {
			configStatus = "warn"
			configDetails["present"] = false
		} else {
			configStatus = "fail"
			configDetails["error"] = err.Error()
		}
	} else {
		configDetails["present"] = true
		configDetails["mode"] = info.Mode().Perm().String()
	}
	report.Checks = append(report.Checks, doctorCheck{
		Name:    "config.file",
		Status:  configStatus,
		Details: configDetails,
	})

	selectedProvider := defaultString(*providerName, cfg.DefaultProvider)
	providerCfg, providerErr := cfg.ProviderConfig(selectedProvider)
	if providerErr != nil {
		report.Checks = append(report.Checks, doctorCheck{
			Name:   "provider.config",
			Status: "fail",
			Details: map[string]any{
				"provider": selectedProvider,
				"error":    providerErr.Error(),
			},
		})
		return renderDoctorReport(stdout, report, *jsonMode, true)
	}
	if *baseURL != "" {
		providerCfg.BaseURL = *baseURL
	}
	if *apiKeyEnv != "" {
		providerCfg.APIKeyEnv = *apiKeyEnv
	}
	if *wireAPI != "" {
		providerCfg.WireAPI = *wireAPI
	}
	if *model != "" {
		providerCfg.Model = *model
	}

	report.Checks = append(report.Checks, doctorCheck{
		Name:    "provider.config",
		Status:  "ok",
		Details: doctorProviderConfigDetails(selectedProvider, providerCfg),
	})

	apiKey := cfg.APIKey(selectedProvider)
	apiKeyStatus := "ok"
	if apiKey == "" {
		apiKeyStatus = "warn"
	}
	report.Checks = append(report.Checks, doctorCheck{
		Name:   "provider.api_key_env",
		Status: apiKeyStatus,
		Details: map[string]any{
			"env":     providerCfg.APIKeyEnv,
			"present": apiKey != "",
		},
	})

	sessionStatus := "ok"
	sessionDetails := map[string]any{"dir": cfg.Session.Dir}
	sessionDirMode, dirModeErr := config.ParseFileMode(cfg.Session.DirMode, 0o700)
	if dirModeErr != nil {
		sessionDirMode = 0o700
	}
	if err := os.MkdirAll(cfg.Session.Dir, sessionDirMode); err != nil {
		sessionStatus = "fail"
		sessionDetails["error"] = err.Error()
	}
	report.Checks = append(report.Checks, doctorCheck{
		Name:    "session.dir",
		Status:  sessionStatus,
		Details: sessionDetails,
	})
	report.Checks = append(report.Checks, checkSessionDirMode(cfg.Session.Dir, cfg.Session.DirMode))
	report.Checks = append(report.Checks, checkSessionRootStrategy(cfg))
	report.Checks = append(report.Checks, checkSessionPartialState(cfg.Session.Dir))
	report.Checks = append(report.Checks, checkWorkspaceWrite(cwd))
	report.Checks = append(report.Checks, checkWorkspaceExtensionTrust(cwd))

	skillCatalog, skillErr := skills.Scan(cfg.Skills.Dirs)
	skillStatus := "ok"
	skillDetails := map[string]any{
		"dirs": cfg.Skills.Dirs,
	}
	if skillErr != nil {
		skillStatus = "fail"
		skillDetails["error"] = skillErr.Error()
	} else {
		skillDetails["skills"] = len(skillCatalog.Summaries())
		var names []string
		for _, summary := range skillCatalog.Summaries() {
			names = append(names, summary.Name)
		}
		skillDetails["skill_names"] = names
	}
	report.Checks = append(report.Checks, doctorCheck{
		Name:    "skills.scan",
		Status:  skillStatus,
		Details: skillDetails,
	})
	report.Checks = append(report.Checks, checkHooksConfig(cfg))
	report.Checks = append(report.Checks, checkHookCommands(cfg, cwd))

	if !*skipProbe {
		if apiKey == "" {
			report.Checks = append(report.Checks, doctorCheck{
				Name:   "provider.probe",
				Status: "skip",
				Details: map[string]any{
					"reason": "API key env is not set",
				},
			})
		} else {
			result, probeErr := runner.Probe(ctx, runtime.ProbeRequest{
				Provider:  selectedProvider,
				Model:     providerCfg.Model,
				BaseURL:   providerCfg.BaseURL,
				APIKeyEnv: providerCfg.APIKeyEnv,
				WireAPI:   providerCfg.WireAPI,
				Prompt:    *prompt,
			})
			status := "ok"
			details := map[string]any{
				"provider":        result.Provider,
				"model":           result.Model,
				"base_url":        result.BaseURL,
				"wire_api":        result.WireAPI,
				"stop_reason":     result.StopReason,
				"tool_call_names": result.ToolCallNames,
				"finish_message":  result.FinishMessage,
			}
			if probeErr != nil {
				status = "fail"
				details["error"] = probeErr.Error()
			}
			report.Checks = append(report.Checks, doctorCheck{
				Name:    "provider.probe",
				Status:  status,
				Details: details,
			})
		}
	}

	return renderDoctorReport(stdout, report, *jsonMode, hasDoctorFailure(report.Checks))
}

func renderDoctorReport(stdout io.Writer, report doctorReport, jsonMode, failed bool) error {
	if jsonMode {
		if err := json.NewEncoder(stdout).Encode(report); err != nil {
			return err
		}
		if failed {
			return ExitError{Code: 1}
		}
		return nil
	}

	fmt.Fprintf(stdout, "config_path: %s\n", report.ConfigPath)
	fmt.Fprintf(stdout, "default_provider: %s\n", report.DefaultProvider)
	for _, check := range report.Checks {
		fmt.Fprintf(stdout, "\n[%s] %s\n", strings.ToUpper(check.Status), check.Name)
		for key, value := range check.Details {
			fmt.Fprintf(stdout, "%s: %v\n", key, value)
		}
	}
	if failed {
		return ExitError{Code: 1}
	}
	return nil
}

func doctorProviderConfigDetails(providerName string, providerCfg config.Provider) map[string]any {
	effectiveAPIProvider, _ := config.EffectiveAPIProvider(providerName, providerCfg)
	details := map[string]any{
		"provider":               providerName,
		"api_provider":           providerCfg.APIProvider,
		"effective_api_provider": effectiveAPIProvider,
		"model":                  providerCfg.Model,
		"base_url":               providerCfg.BaseURL,
		"api_key_env":            providerCfg.APIKeyEnv,
		"wire_api":               providerCfg.WireAPI,
		"timeout_sec":            providerCfg.TimeoutSec,
		"request_timeout_sec":    doctorRequestTimeoutSec(providerCfg),
		"stream_idle_timeout_ms": doctorStreamIdleTimeoutMS(providerCfg),
		"retry_policy":           doctorRetryPolicyDetails(providerCfg),
	}
	if effectiveAPIProvider == "openai-compatible" {
		storeValue, storeSource := doctorStoreDetails(providerName, providerCfg.Store)
		sendMetadataValue, sendMetadataSource := doctorSendMetadataDetails(providerCfg.SendMetadata)
		details["store"] = storeValue
		details["store_source"] = storeSource
		details["send_metadata"] = sendMetadataValue
		details["send_metadata_source"] = sendMetadataSource
	}
	return details
}

func doctorRequestTimeoutSec(providerCfg config.Provider) int {
	if providerCfg.RequestTimeoutSec > 0 {
		return providerCfg.RequestTimeoutSec
	}
	if providerCfg.TimeoutSec > 0 {
		return providerCfg.TimeoutSec
	}
	return 300
}

func doctorStreamIdleTimeoutMS(providerCfg config.Provider) int {
	if providerCfg.StreamIdleTimeoutMS > 0 {
		return providerCfg.StreamIdleTimeoutMS
	}
	return 300000
}

func doctorRetryPolicyDetails(providerCfg config.Provider) map[string]any {
	return map[string]any{
		"max_attempts":    providerCfg.Retry.MaxAttempts,
		"base_delay_ms":   providerCfg.Retry.BaseDelayMS,
		"retry_429":       providerCfg.Retry.Retry429,
		"retry_5xx":       providerCfg.Retry.Retry5xx,
		"retry_transport": providerCfg.Retry.RetryTransport,
	}
}

func doctorStoreDetails(providerName string, configured *bool) (bool, string) {
	if configured != nil {
		return *configured, "config"
	}
	if isOpenAIResponsesLikeProvider(providerName) {
		return false, "provider_default"
	}
	return false, "unset"
}

func doctorSendMetadataDetails(configured *bool) (bool, string) {
	if configured != nil {
		return *configured, "config"
	}
	return true, "provider_default"
}

func isOpenAIResponsesLikeProvider(name string) bool {
	switch name {
	case "openai", "openai-compatible":
		return true
	default:
		return false
	}
}

func hasDoctorFailure(checks []doctorCheck) bool {
	for _, check := range checks {
		if check.Status == "fail" {
			return true
		}
	}
	return false
}

func checkWorkspaceWrite(cwd string) doctorCheck {
	check := doctorCheck{
		Name:   "workspace.write",
		Status: "ok",
		Details: map[string]any{
			"dir": cwd,
		},
	}
	file, err := os.CreateTemp(cwd, ".doctor-write-*")
	if err != nil {
		check.Status = "fail"
		check.Details["error"] = err.Error()
		return check
	}
	path := file.Name()
	_ = file.Close()
	_ = os.Remove(path)
	check.Details["temp_file"] = filepath.Base(path)
	return check
}

type sessionDirModeProbeResult struct {
	ProbeDir      string
	ProbeMode     fs.FileMode
	ExpectedMode  fs.FileMode
	ChmodError    string
	SupportsChmod bool
}

var sessionDirModeProbe = probeSessionDirMode

func checkSessionDirMode(dir, configuredMode string) doctorCheck {
	check := doctorCheck{
		Name:   "session.dir.mode",
		Status: "ok",
		Details: map[string]any{
			"dir":        dir,
			"configured": defaultString(configuredMode, "0700"),
		},
	}
	expected, err := config.ParseFileMode(configuredMode, 0o700)
	if err != nil {
		check.Status = "fail"
		check.Details["error"] = err.Error()
		return check
	}
	info, err := os.Stat(dir)
	if err != nil {
		check.Status = "warn"
		check.Details["error"] = err.Error()
		return check
	}
	perm := info.Mode().Perm()
	check.Details["mode"] = perm.String()
	check.Details["expected"] = expected.String()
	if perm != expected {
		check.Status = "warn"
		probe, probeErr := sessionDirModeProbe(dir, expected)
		if probeErr != nil {
			check.Details["reason"] = "mode_probe_failed"
			check.Details["probe_error"] = probeErr.Error()
			return check
		}
		check.Details["probe_mode"] = probe.ProbeMode.String()
		check.Details["posix_owner_only_supported"] = probe.SupportsChmod
		if probe.ProbeDir != "" {
			check.Details["probe_dir"] = probe.ProbeDir
		}
		if probe.ChmodError != "" {
			check.Details["probe_chmod_error"] = probe.ChmodError
		}
		if probe.SupportsChmod {
			check.Details["reason"] = "permission_drift"
			check.Details["advice"] = []string{
				fmt.Sprintf("Recreate or chmod %s so it matches the configured mode %s.", dir, expected.String()),
			}
		} else {
			check.Details["reason"] = "filesystem_does_not_honor_posix_permissions"
			check.Details["advice"] = sessionRootCandidateAdvice(dir, false)
		}
	}
	return check
}

func probeSessionDirMode(dir string, expected fs.FileMode) (sessionDirModeProbeResult, error) {
	result := sessionDirModeProbeResult{ExpectedMode: expected}
	probeDir, err := os.MkdirTemp(dir, ".doctor-mode-*")
	if err != nil {
		return result, err
	}
	result.ProbeDir = probeDir
	defer os.RemoveAll(probeDir)
	if err := os.Chmod(probeDir, expected); err != nil {
		result.ChmodError = err.Error()
	}
	info, err := os.Stat(probeDir)
	if err != nil {
		return result, err
	}
	result.ProbeMode = info.Mode().Perm()
	result.SupportsChmod = result.ChmodError == "" && result.ProbeMode == expected
	return result, nil
}

func checkHooksConfig(cfg *config.Config) doctorCheck {
	check := doctorCheck{
		Name:   "hooks.config",
		Status: "ok",
		Details: map[string]any{
			"default_timeout_sec": cfg.Hooks.DefaultTimeoutSec,
		},
	}
	total := 0
	var warnings []string
	for _, point := range configuredHookPoints(cfg) {
		total += len(point.items)
		for idx, hook := range point.items {
			prefix := fmt.Sprintf("%s[%d]", point.name, idx)
			if strings.TrimSpace(hook.Name) == "" {
				warnings = append(warnings, prefix+": missing name")
			}
			if len(hook.Command) == 0 && hook.Inject == nil && hook.Filter == nil {
				warnings = append(warnings, prefix+": no command/inject/filter action")
			}
			if hook.Inject != nil && strings.TrimSpace(hook.Inject.Field) == "" {
				warnings = append(warnings, prefix+": inject.field is empty")
			}
			if hook.Filter != nil && strings.TrimSpace(hook.Filter.Field) == "" {
				warnings = append(warnings, prefix+": filter.field is empty")
			}
		}
	}
	check.Details["count"] = total
	if len(warnings) > 0 {
		check.Status = "warn"
		check.Details["warnings"] = warnings
	}
	return check
}

func runInit(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		configPath  = fs.String("config", "", "")
		force       = fs.Bool("force", false, "")
		provider    = fs.String("provider", "", "")
		model       = fs.String("model", "", "")
		baseURL     = fs.String("base-url", "", "")
		apiKeyEnv   = fs.String("api-key-env", "", "")
		wireAPI     = fs.String("wire-api", "", "")
		skillDir    = fs.String("skill-dir", "", "")
		sessionDir  = fs.String("session-dir", "", "")
		exampleHook = fs.Bool("example-hook", true, "")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	cfg := config.Default()
	reader := bufio.NewReader(os.Stdin)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		if *provider == "" {
			*provider = prompt(stdout, reader, "Default provider", cfg.DefaultProvider)
		}
		if providerCfg, err := cfg.ProviderConfig(defaultString(*provider, cfg.DefaultProvider)); err == nil {
			if *model == "" {
				*model = prompt(stdout, reader, "Model", providerCfg.Model)
			}
			if *baseURL == "" && defaultString(*provider, cfg.DefaultProvider) == "openai-compatible" {
				*baseURL = prompt(stdout, reader, "Base URL", providerCfg.BaseURL)
			}
			if *apiKeyEnv == "" && defaultString(*provider, cfg.DefaultProvider) == "openai-compatible" {
				*apiKeyEnv = prompt(stdout, reader, "API key env", providerCfg.APIKeyEnv)
			}
			if *wireAPI == "" && defaultString(*provider, cfg.DefaultProvider) == "openai-compatible" {
				*wireAPI = prompt(stdout, reader, "Wire API", defaultString(providerCfg.WireAPI, "responses"))
			}
		}
		*sessionDir = prompt(stdout, reader, "Session dir", defaultString(*sessionDir, cfg.Session.Dir))
		*skillDir = prompt(stdout, reader, "Skills dir", defaultString(*skillDir, "./skills"))
	}
	cfg.DefaultProvider = defaultString(*provider, cfg.DefaultProvider)
	if providerCfg, ok := cfg.Providers[cfg.DefaultProvider]; ok {
		if *model != "" {
			providerCfg.Model = *model
		}
		if *baseURL != "" {
			providerCfg.BaseURL = *baseURL
		}
		if *apiKeyEnv != "" {
			providerCfg.APIKeyEnv = *apiKeyEnv
		}
		if *wireAPI != "" {
			providerCfg.WireAPI = *wireAPI
		}
		cfg.Providers[cfg.DefaultProvider] = providerCfg
	}
	effectiveSessionDir := defaultString(*sessionDir, cfg.Session.Dir)
	effectiveSkillDir := defaultString(*skillDir, "./skills")
	cfg.Session.Dir = effectiveSessionDir
	cfg.Skills.Dirs = []string{effectiveSkillDir}
	if *exampleHook {
		cfg.Hooks.SessionComplete = []config.HookDefinition{
			{
				Name:    "log-session-complete",
				Command: []string{"/bin/sh", ".go-cli-agent/hooks/session-complete.sh"},
			},
		}
	}

	target := *configPath
	if target == "" {
		target = filepath.Join(cwd, ".go-cli-agent", "config.yaml")
	}
	if !*force {
		if _, err := os.Lstat(target); err == nil {
			return fmt.Errorf("config already exists: %s", target)
		}
	}
	data, err := config.MarshalYAML(cfg)
	if err != nil {
		return err
	}
	if err := fileutil.AtomicWriteFileNoSymlink(target, data, 0o600); err != nil {
		return err
	}
	if err := fileutil.AtomicWriteFileNoSymlink(filepath.Join(cwd, ".env.example"), []byte("OPENAI_API_KEY=\nANTHROPIC_API_KEY=\nGEMINI_API_KEY=\n"), 0o600); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(cwd, "workspace"), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(cwd, effectiveSkillDir, "example"), 0o700); err != nil {
		return err
	}
	skillBody := "---\nname: example\ndescription: Example local skill\n---\nWhen asked to inspect the repository, start with rg --files and targeted reads.\n"
	if err := fileutil.AtomicWriteFileNoSymlink(filepath.Join(cwd, effectiveSkillDir, "example", "SKILL.md"), []byte(skillBody), 0o600); err != nil {
		return err
	}
	toolDir := filepath.Join(cwd, effectiveSkillDir, "example", "tools")
	if err := os.MkdirAll(toolDir, 0o700); err != nil {
		return err
	}
	exampleTool := "name: echo_args\ndescription: Echo JSON arguments for debugging\ncommand: [\"/bin/sh\", \"-lc\", \"cat\"]\ntimeout_sec: 15\ninput_schema:\n  type: object\n  properties:\n    message:\n      type: string\n"
	if err := fileutil.AtomicWriteFileNoSymlink(filepath.Join(toolDir, "echo.yaml"), []byte(exampleTool), 0o600); err != nil {
		return err
	}
	if *exampleHook {
		hookDir := filepath.Join(cwd, ".go-cli-agent", "hooks")
		if err := os.MkdirAll(hookDir, 0o700); err != nil {
			return err
		}
		hookScript := "#!/usr/bin/env sh\nset -eu\nmkdir -p .go-cli-agent/hooks/logs\ncat >> .go-cli-agent/hooks/logs/session-complete.jsonl\n"
		if err := fileutil.AtomicWriteFileNoSymlink(filepath.Join(hookDir, "session-complete.sh"), []byte(hookScript), 0o700); err != nil {
			return err
		}
	}
	_, _ = fmt.Fprintf(stdout, "wrote config to %s\n", target)
	_, _ = fmt.Fprintf(stdout, "next: ./bin/go-cli-agent doctor --config %s --skip-probe\n", target)
	_, _ = fmt.Fprintf(stdout, "next: ./bin/go-cli-agent probe-provider --config %s\n", target)
	_, _ = fmt.Fprintln(stdout, "next: ./bin/go-cli-agent run \"Describe the current repository.\"")
	return nil
}

func resolvePrompt(args []string, stdin io.Reader) (string, error) {
	if len(args) > 0 {
		return strings.Join(args, " "), nil
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("prompt is required")
	}
	data, err := io.ReadAll(stdin)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func printResult(w io.Writer, jsonMode bool, result runtime.RunResult, exitCode int) error {
	if jsonMode {
		payload := map[string]any{
			"session_id": result.SessionID,
			"status":     result.Status,
			"final_text": result.FinalText,
			"last_error": result.LastError,
			"exit_code":  exitCode,
		}
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			return err
		}
		if exitCode != 0 {
			return ExitError{Code: exitCode}
		}
		return nil
	}
	if result.FinalText != "" {
		_, _ = fmt.Fprintln(w, result.FinalText)
	}
	if exitCode != 0 {
		return ExitError{Code: exitCode}
	}
	return nil
}

func watchEsc(ctx context.Context, onInterrupt func()) {
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return
	}
	defer term.Restore(fd, oldState)

	input := make(chan byte, 1)
	go func() {
		buffer := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buffer)
			if err != nil || n == 0 {
				close(input)
				return
			}
			input <- buffer[0]
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case value, ok := <-input:
			if !ok {
				continue
			}
			if value == 27 {
				onInterrupt()
				return
			}
		}
	}
}

func normalizeTaskBoard(board session.TaskBoard) session.TaskBoard {
	if board.Todo == nil {
		board.Todo = []session.TodoItem{}
	}
	if board.Tasks == nil {
		board.Tasks = []session.Task{}
	}
	if board.Counters == nil {
		board.Counters = map[string]int{}
	}
	if board.Groups == nil {
		board.Groups = map[string][]session.Task{}
	}
	for _, key := range []string{"ready", "blocked", "completed"} {
		if board.Groups[key] == nil {
			board.Groups[key] = []session.Task{}
		}
	}
	return board
}

func normalizeInterspersedFlags(args []string, valueFlags, boolFlags []string) []string {
	if len(args) == 0 {
		return args
	}
	valueSet := make(map[string]struct{}, len(valueFlags))
	for _, name := range valueFlags {
		valueSet[name] = struct{}{}
	}
	boolSet := make(map[string]struct{}, len(boolFlags))
	for _, name := range boolFlags {
		boolSet[name] = struct{}{}
	}
	flags := make([]string, 0, len(args))
	positionals := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		name, hasInlineValue, isFlag := splitFlagToken(arg)
		if !isFlag {
			positionals = append(positionals, arg)
			continue
		}
		if _, ok := boolSet[name]; ok {
			flags = append(flags, arg)
			continue
		}
		if _, ok := valueSet[name]; ok {
			flags = append(flags, arg)
			if !hasInlineValue && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		flags = append(flags, arg)
	}
	return append(flags, positionals...)
}

func classifyCommandError(err error) error {
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return err
	}
	var exitErr ExitError
	if errors.As(err, &exitErr) {
		return err
	}
	var classified ClassifiedError
	if errors.As(err, &classified) {
		return err
	}
	return ClassifiedError{
		Code: exitCodeForError(err),
		Err:  err,
	}
}

func exitCodeForError(err error) int {
	var cfgErr *runtime.ConfigError
	var providerErr *runtime.ProviderError
	var httpErr *provider.HTTPError
	var hookErr *hooks.FailClosedError
	switch {
	case errors.As(err, &cfgErr):
		return 2
	case errors.As(err, &providerErr), errors.As(err, &httpErr):
		return 3
	case errors.As(err, &hookErr):
		return 5
	default:
		return 1
	}
}

func splitFlagToken(arg string) (name string, hasInlineValue bool, isFlag bool) {
	if !strings.HasPrefix(arg, "-") || arg == "-" {
		return "", false, false
	}
	trimmed := strings.TrimLeft(arg, "-")
	if trimmed == "" {
		return "", false, false
	}
	if index := strings.Index(trimmed, "="); index >= 0 {
		return trimmed[:index], true, true
	}
	return trimmed, false, true
}

func prompt(out io.Writer, reader *bufio.Reader, label, fallback string) string {
	_, _ = fmt.Fprintf(out, "%s [%s]: ", label, fallback)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return fallback
	}
	return line
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func marker(status string) string {
	switch status {
	case "completed":
		return "x"
	case "in_progress":
		return ">"
	default:
		return " "
	}
}

func mapStatusToExitCode(status, lastError string) int {
	switch status {
	case session.StatusCompleted, session.StatusAwaitingInput:
		return 0
	case session.StatusPaused:
		return 130
	case session.StatusFailed:
		if strings.Contains(lastError, "incomplete_no_finish") {
			return 6
		}
		return 1
	default:
		return 1
	}
}
