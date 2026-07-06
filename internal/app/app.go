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
	"go-cli-agent/internal/streamjson"
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
	if args[0] == "--version" || args[0] == "version" {
		return classifyCommandError(printVersion(stdout))
	}
	var err error
	switch args[0] {
	case "init":
		err = runInit(args[1:], stdout, stderr)
	case "models":
		err = modelsCommand(ctx, args[1:], stdout, stderr)
	case "web":
		err = webCommand(ctx, args[1:], stdout, stderr)
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
	case "goal":
		err = goalCommand(ctx, args[1:], stdout, stderr)
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
	_, _ = fmt.Fprintln(w, "usage: go-cli-agent <web|init|run|exec|continue|steer|sessions|goal|tasks|models|probe-provider|doctor> [...]")
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
	case "delegate", "children", "queue", "tui":
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
var stdinIsTerminal = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

const maxPromptStdinBytes int64 = 4 << 20

func runCommand(ctx context.Context, mode string, args []string, stdout, stderr io.Writer) error {
	args = normalizeInterspersedFlags(args, []string{"provider", "model", "config", "workdir", "system", "timeout", "isolation", "isolation-root", "goal", "goal-mode", "goal-token-budget", "goal-time-budget", "goal-success", "goal-validate", "output-format", "input-format", "resume", "thinking-level"}, []string{"json", "init", "goal-plan-approval", "goal-stop-on-budget", "plan", "plan-only"})
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		providerName     = fs.String("provider", "", "")
		model            = fs.String("model", "", "")
		configPath       = fs.String("config", "", "")
		workdir          = fs.String("workdir", "", "")
		system           = fs.String("system", "", "")
		jsonMode         = fs.Bool("json", false, "")
		initMode         = fs.Bool("init", false, "")
		timeoutSec       = fs.Int("timeout", 0, "")
		isolationMode    = fs.String("isolation", "", "")
		isolationRoot    = fs.String("isolation-root", "", "")
		goalObjective    = fs.String("goal", "", "")
		goalMode         = fs.String("goal-mode", "goal", "")
		goalTokenBudget  = fs.Int64("goal-token-budget", 0, "")
		goalTimeBudget   = fs.String("goal-time-budget", "", "")
		goalPlanApproval = fs.Bool("goal-plan-approval", false, "")
		goalStopOnBudget = fs.Bool("goal-stop-on-budget", false, "")
		planModeEnabled  = fs.Bool("plan", false, "")
		planOnly         = fs.Bool("plan-only", false, "")
		outputFormat     = fs.String("output-format", "text", "")
		inputFormat      = fs.String("input-format", "text", "")
		resumeSession    = fs.String("resume", "", "")
		thinkingLevel    = fs.String("thinking-level", "", "")
		goalCriteria     stringSliceFlag
		goalValidation   stringSliceFlag
	)
	fs.Var(&goalCriteria, "goal-success", "")
	fs.Var(&goalValidation, "goal-validate", "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *outputFormat != "text" && *outputFormat != "stream-json" {
		return fmt.Errorf("unsupported --output-format %q", *outputFormat)
	}
	if *inputFormat != "text" && *inputFormat != "stream-json" {
		return fmt.Errorf("unsupported --input-format %q", *inputFormat)
	}
	if *outputFormat == "stream-json" && *jsonMode {
		return fmt.Errorf("--output-format stream-json and --json are mutually exclusive")
	}
	if *resumeSession != "" && mode != session.ModeExec {
		return fmt.Errorf("--resume is only supported on exec")
	}
	if *inputFormat == "stream-json" && len(fs.Args()) > 0 {
		return fmt.Errorf("positional prompt arguments are not allowed with --input-format stream-json")
	}
	invokeCWD, err := os.Getwd()
	if err != nil {
		return err
	}
	runner, cfg, err := runnerLoader(*configPath, invokeCWD)
	if err != nil {
		return err
	}
	if mode == "run" && !term.IsTerminal(int(os.Stdin.Fd())) && !*jsonMode {
		_, _ = fmt.Fprintln(stderr, "warning: stdin is not a TTY; Esc interrupt is disabled in run mode. Prefer exec for zero-interaction runs.")
	}
	var prompt string
	if *inputFormat == "stream-json" {
		prompt, err = streamjson.ReadInitialPrompt(os.Stdin, maxPromptStdinBytes)
	} else {
		prompt, err = resolvePrompt(fs.Args(), os.Stdin)
	}
	if err != nil {
		return err
	}
	providerOptions, err := providerOptionsForThinkingLevel(*thinkingLevel, cfg, *providerName)
	if err != nil {
		return err
	}
	goalDraft, err := goalDraftFromCLI(*goalObjective, *goalMode, *goalTokenBudget, *goalTimeBudget, *goalPlanApproval, *goalStopOnBudget, goalCriteria, goalValidation)
	if err != nil {
		return err
	}
	planDraft := planModeDraftFromCLI(*planModeEnabled || *planOnly, prompt)

	streamMode := *outputFormat == "stream-json"
	var sjAdapter *streamjson.Adapter
	renderer := output.New(*jsonMode, stdout)
	if streamMode {
		sjAdapter = streamjson.NewAdapter(stdout)
	}
	sub := runner.Bus().Subscribe(128)
	var sessionID string
	var sessionMu sync.RWMutex
	renderCtx, cancelRender := context.WithCancel(ctx)
	done := renderEventsUntilDone(renderCtx, sub, func(evt events.Event) {
		if evt.Type == "session.started" {
			sessionMu.Lock()
			sessionID = evt.SessionID
			sessionMu.Unlock()
		}
		if streamMode {
			sjAdapter.Handle(evt)
			return
		}
		renderer.Handle(evt)
	}, isTerminalSessionEvent)

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
	var result runtime.RunResult
	if *resumeSession != "" {
		result, err = runner.Continue(runCtx, runtime.ContinueRequest{
			SessionID:        *resumeSession,
			Message:          prompt,
			Provider:         *providerName,
			Model:            *model,
			SystemOverride:   *system,
			PlanInputHandler: cliPlanInputHandler(os.Stdin, stderr),
			ProviderOptions:  providerOptions,
		})
	} else {
		result, err = runner.Start(runCtx, runtime.StartRequest{
			Prompt:           prompt,
			Provider:         *providerName,
			Model:            *model,
			ProviderOptions:  providerOptions,
			Workdir:          *workdir,
			Mode:             actualMode,
			SystemOverride:   *system,
			Goal:             goalDraft,
			PlanMode:         planDraft,
			PlanInputHandler: cliPlanInputHandler(os.Stdin, stderr),
			IsolationMode:    *isolationMode,
			IsolationRoot:    *isolationRoot,
		})
	}
	cancel()
	waitForRenderer(done, cancelRender, streamMode)
	if err != nil {
		if streamMode && result.SessionID != "" {
			exitCode := mapStatusToExitCode(result.Status, result.LastError)
			if writeErr := sjAdapter.WriteResult(result.SessionID, result.FinalText, result.Status, result.LastError, exitCode); writeErr != nil {
				return writeErr
			}
			return ExitError{Code: exitCode}
		}
		return err
	}
	if streamMode {
		exitCode := mapStatusToExitCode(result.Status, result.LastError)
		if err := sjAdapter.WriteResult(result.SessionID, result.FinalText, result.Status, result.LastError, exitCode); err != nil {
			return err
		}
		if exitCode != 0 {
			return ExitError{Code: exitCode}
		}
		return nil
	}
	return printResult(stdout, *jsonMode, result, mapStatusToExitCode(result.Status, result.LastError))
}

type stringSliceFlag []string

func (f *stringSliceFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *stringSliceFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func goalDraftFromCLI(objective, mode string, tokenBudget int64, timeBudget string, requirePlanApproval bool, stopOnBudget bool, criteria, validation []string) (*session.GoalDraft, error) {
	objective = strings.TrimSpace(objective)
	hasGoalFields := objective != "" || tokenBudget > 0 || strings.TrimSpace(timeBudget) != "" || requirePlanApproval || stopOnBudget || len(criteria) > 0 || len(validation) > 0 || strings.TrimSpace(mode) == session.GoalModeMission
	if !hasGoalFields {
		return nil, nil
	}
	if objective == "" {
		return nil, errors.New("--goal is required when goal options are provided")
	}
	var tokenPtr *int64
	if tokenBudget > 0 {
		tokenPtr = &tokenBudget
	}
	var secondsPtr *int64
	if strings.TrimSpace(timeBudget) != "" {
		seconds, err := parseGoalDurationSeconds(timeBudget)
		if err != nil {
			return nil, err
		}
		secondsPtr = &seconds
	}
	return &session.GoalDraft{
		Enabled:             true,
		Mode:                mode,
		Objective:           objective,
		TokenBudget:         tokenPtr,
		TimeBudgetSeconds:   secondsPtr,
		SuccessCriteria:     append([]string(nil), criteria...),
		ValidationPlan:      append([]string(nil), validation...),
		RequirePlanApproval: requirePlanApproval,
		StopOnBudget:        stopOnBudget,
		Source:              session.GoalSourceCLI,
	}, nil
}

func planModeDraftFromCLI(enabled bool, prompt string) *session.PlanModeDraft {
	if !enabled {
		return nil
	}
	return &session.PlanModeDraft{
		Enabled:   true,
		Objective: strings.TrimSpace(prompt),
		Source:    session.PlanModeSourceCLI,
	}
}

func cliPlanInputHandler(stdin io.Reader, stderr io.Writer) runtime.PlanInputHandler {
	if !stdinIsTerminal() {
		return nil
	}
	return func(ctx context.Context, request session.PlanModeInputRequest) ([]session.PlanModeInputAnswer, error) {
		reader := bufio.NewReader(stdin)
		answers := make([]session.PlanModeInputAnswer, 0, len(request.Questions))
		for _, question := range request.Questions {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
			_, _ = fmt.Fprintf(stderr, "\n%s\n%s\n", question.Header, question.Question)
			for i, option := range question.Options {
				_, _ = fmt.Fprintf(stderr, "  %d. %s - %s\n", i+1, option.Label, option.Description)
			}
			_, _ = fmt.Fprintf(stderr, "  other. Enter custom answer\n")
			_, _ = fmt.Fprint(stderr, "Select [1]: ")
			line, err := reader.ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return nil, err
			}
			value := strings.TrimSpace(line)
			if value == "" {
				value = "1"
			}
			answer := session.PlanModeInputAnswer{QuestionID: question.ID}
			if len(value) == 1 && value[0] >= '1' && int(value[0]-'1') < len(question.Options) {
				option := question.Options[int(value[0]-'1')]
				answer.Label = option.Label
				answer.Value = option.Label
			} else {
				answer.Value = value
				answer.IsOther = true
			}
			answers = append(answers, answer)
		}
		return answers, nil
	}
}

func parseGoalDurationSeconds(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	if allDigits(value) {
		value += "m"
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid --goal-time-budget: %w", err)
	}
	if duration <= 0 {
		return 0, errors.New("--goal-time-budget must be positive")
	}
	return int64(duration / time.Second), nil
}

func allDigits(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return value != ""
}

func continueCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	args = normalizeInterspersedFlags(args, []string{"message", "provider", "model", "config", "system"}, []string{"json", "plan", "approve-plan", "cancel-plan", "override-goal-coverage"})
	fs := flag.NewFlagSet("continue", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		message              = fs.String("message", "", "")
		provider             = fs.String("provider", "", "")
		model                = fs.String("model", "", "")
		configPath           = fs.String("config", "", "")
		jsonMode             = fs.Bool("json", false, "")
		system               = fs.String("system", "", "")
		planMode             = fs.Bool("plan", false, "")
		approvePlan          = fs.Bool("approve-plan", false, "")
		overrideGoalCoverage = fs.Bool("override-goal-coverage", false, "")
		cancelPlan           = fs.Bool("cancel-plan", false, "")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("continue requires <session-id>")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
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
		data, err := readPromptStdin(os.Stdin)
		if err != nil {
			cancelRender()
			<-done
			return err
		}
		*message = data
	}
	result, err := runner.Continue(ctx, runtime.ContinueRequest{
		SessionID:            fs.Arg(0),
		Message:              strings.TrimSpace(*message),
		Provider:             *provider,
		Model:                *model,
		SystemOverride:       *system,
		PlanMode:             planModeDraftFromCLI(*planMode, strings.TrimSpace(*message)),
		PlanInputHandler:     cliPlanInputHandler(os.Stdin, stderr),
		ApprovePlan:          *approvePlan,
		OverrideGoalCoverage: *overrideGoalCoverage,
		CancelPlan:           *cancelPlan,
		Source:               session.PlanModeSourceCLI,
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
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	runner, _, err := runnerLoader(*configPath, cwd)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*message) == "" && !term.IsTerminal(int(os.Stdin.Fd())) {
		data, err := readPromptStdin(os.Stdin)
		if err != nil {
			return err
		}
		*message = data
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
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
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

func goalCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: go-cli-agent goal <show|pause|resume|clear|complete|plan|validation> <session-id> [--json] [--config path]")
		return flag.ErrHelp
	}
	subcommand := args[0]
	switch subcommand {
	case "plan":
		return goalPlanCommand(ctx, args[1:], stdout, stderr)
	case "validation":
		return goalValidationCommand(args[1:], stdout, stderr)
	}
	subArgs := normalizeInterspersedFlags(args[1:], []string{"config"}, []string{"json"})
	fs := flag.NewFlagSet("goal "+subcommand, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		configPath = fs.String("config", "", "")
		jsonMode   = fs.Bool("json", false, "")
	)
	if err := fs.Parse(subArgs); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("goal %s requires <session-id>", subcommand)
	}
	sessionID := fs.Arg(0)
	switch subcommand {
	case "show", "pause", "resume", "complete", "clear":
	default:
		return fmt.Errorf("unknown goal subcommand: %s", subcommand)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	runner, _, err := storeRunnerLoader(*configPath, cwd)
	if err != nil {
		return err
	}
	store := runner.Store()
	switch subcommand {
	case "show":
		goal, err := loadCLIGoal(store, sessionID)
		if err != nil {
			return err
		}
		if *jsonMode {
			return json.NewEncoder(stdout).Encode(goal)
		}
		printGoal(stdout, goal)
		return nil
	case "pause":
		return mutateGoalStatus(stdout, store, sessionID, session.GoalStatusPaused, "goal.paused", "paused", *jsonMode)
	case "resume":
		return mutateGoalStatus(stdout, store, sessionID, session.GoalStatusActive, "goal.resumed", "active", *jsonMode)
	case "complete":
		return mutateGoalStatus(stdout, store, sessionID, session.GoalStatusComplete, "goal.completed", "complete", *jsonMode)
	case "clear":
		goal, err := loadCLIGoal(store, sessionID)
		if err != nil {
			return err
		}
		previousHistory, err := store.LoadGoalHistory(sessionID)
		if err != nil {
			return err
		}
		cleared, err := store.ClearGoal(sessionID)
		if err != nil {
			return err
		}
		if cleared {
			if err := store.AppendGoalHistory(sessionID, session.GoalHistoryEntry{
				GoalID: goal.GoalID,
				Type:   "goal.cleared",
				Source: session.GoalSourceCLI,
				Status: session.GoalStatusCleared,
				Data: map[string]any{
					"previous_status": goal.Status,
				},
			}); err != nil {
				if restoreErr := store.SaveGoal(sessionID, goal); restoreErr != nil {
					return fmt.Errorf("restore goal after clear history error %v: %w", err, restoreErr)
				}
				return err
			}
			if err := store.AppendEvent(sessionID, events.New(sessionID, "goal.cleared", "goal", map[string]any{
				"goal_id":         goal.GoalID,
				"previous_status": goal.Status,
			})); err != nil {
				if restoreErr := store.SaveGoal(sessionID, goal); restoreErr != nil {
					return fmt.Errorf("restore goal after clear event error %v: %w", err, restoreErr)
				}
				if restoreErr := store.RestoreGoalHistory(sessionID, previousHistory); restoreErr != nil {
					return fmt.Errorf("restore goal history after clear event error %v: %w", err, restoreErr)
				}
				return err
			}
		}
		if *jsonMode {
			return json.NewEncoder(stdout).Encode(map[string]any{"session_id": sessionID, "cleared": cleared})
		}
		_, _ = fmt.Fprintf(stdout, "goal cleared: %t\n", cleared)
		return nil
	}
	return nil
}

func goalPlanCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: go-cli-agent goal plan <show|check|approve> <session-id> [--json] [--config path]")
		return flag.ErrHelp
	}
	subcommand := args[0]
	subArgs := normalizeInterspersedFlags(args[1:], []string{"config"}, []string{"json", "override-coverage"})
	fs := flag.NewFlagSet("goal plan "+subcommand, flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "")
	jsonMode := fs.Bool("json", false, "")
	overrideCoverage := fs.Bool("override-coverage", false, "")
	if err := fs.Parse(subArgs); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("goal plan %s requires <session-id>", subcommand)
	}
	sessionID := fs.Arg(0)
	switch subcommand {
	case "show":
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		runner, _, err := storeRunnerLoader(*configPath, cwd)
		if err != nil {
			return err
		}
		goal, err := loadCLIGoal(runner.Store(), sessionID)
		if err != nil {
			return err
		}
		if *jsonMode {
			return json.NewEncoder(stdout).Encode(goal.Mission)
		}
		printMissionPlan(stdout, goal)
		return nil
	case "check":
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		runner, _, err := storeRunnerLoader(*configPath, cwd)
		if err != nil {
			return err
		}
		goal, err := loadCLIGoal(runner.Store(), sessionID)
		if err != nil {
			return err
		}
		coverage := session.CheckMissionPlanCoverage(goal)
		if *jsonMode {
			return json.NewEncoder(stdout).Encode(coverage)
		}
		printMissionCoverage(stdout, coverage)
		return nil
	case "approve":
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		return goalPlanApproveCommand(ctx, sessionID, *configPath, cwd, *jsonMode, *overrideCoverage, stdout)
	default:
		return fmt.Errorf("unknown goal plan subcommand: %s", subcommand)
	}
}

func goalPlanApproveCommand(ctx context.Context, sessionID, configPath, cwd string, jsonMode bool, overrideCoverage bool, stdout io.Writer) error {
	storeRunner, _, err := storeRunnerLoader(configPath, cwd)
	if err != nil {
		return err
	}
	store := storeRunner.Store()
	goal, err := loadCLIGoal(store, sessionID)
	if err != nil {
		return err
	}
	if err := approveMissionCoverage(goal, overrideCoverage); err != nil {
		return err
	}
	planMode, planModeErr := store.LoadPlanMode(sessionID)
	if planModeErr == nil && planMode.Enabled && planMode.LinkedGoalID == goal.GoalID {
		switch planMode.Status {
		case session.PlanModeStatusAwaitingApproval, session.PlanModeStatusApproved:
			runner, _, err := runnerLoader(configPath, cwd)
			if err != nil {
				return err
			}
			result, err := runner.Continue(ctx, runtime.ContinueRequest{
				SessionID:            sessionID,
				ApprovePlan:          true,
				OverrideGoalCoverage: overrideCoverage,
				Source:               session.PlanModeSourceCLI,
			})
			if err != nil {
				return err
			}
			if jsonMode {
				return json.NewEncoder(stdout).Encode(result)
			}
			_, _ = fmt.Fprintf(stdout, "goal plan approval accepted: %s (%s)\n", result.SessionID, result.Status)
			return nil
		case session.PlanModeStatusPlanning, session.PlanModeStatusAwaitingUserInput:
			return errors.New("linked Plan Mode is not awaiting approval; submit the plan before approving the mission plan")
		case session.PlanModeStatusExecuting:
			if planMode.ApprovedVersion <= 0 || strings.TrimSpace(planMode.PlanMarkdown) == "" {
				return errors.New("plan mode has no approved plan")
			}
			approvedAt := ""
			if goal.Mission != nil {
				approvedAt = goal.Mission.ApprovedAt
			}
			if approvedAt == "" {
				approvedAt = time.Now().UTC().Format(time.RFC3339Nano)
			}
			approved, err := approveCLIMissionPlanWithEvent(store, sessionID, goal, session.MissionPlanApprovalInput{
				Source:           session.GoalSourceCLI,
				ApprovedAt:       approvedAt,
				CoverageOverride: overrideCoverage,
				PlanModeID:       planMode.PlanModeID,
				ApprovedVersion:  planMode.ApprovedVersion,
			}, map[string]any{
				"plan_mode_id":     planMode.PlanModeID,
				"approved_version": planMode.ApprovedVersion,
			})
			if err != nil {
				return err
			}
			if jsonMode {
				return json.NewEncoder(stdout).Encode(approved)
			}
			_, _ = fmt.Fprintf(stdout, "goal plan approved: %s\n", approved.GoalID)
			return nil
		}
	} else if planModeErr != nil && !errors.Is(planModeErr, fs.ErrNotExist) {
		return planModeErr
	}
	if session.GoalRequiresPlanApproval(goal) {
		previousPlanMode, err := store.SnapshotPlanMode(sessionID)
		if err != nil {
			return err
		}
		previousPlanModeHistory, err := store.LoadPlanModeHistory(sessionID)
		if err != nil {
			return err
		}
		planMode, created, err := store.EnsurePlanModeForGoal(sessionID, goal, session.PlanModeSourceCLI)
		if err != nil {
			return err
		}
		if err := appendCLIPlanModeLinkEvent(store, sessionID, previousPlanMode, planMode, created); err != nil {
			if restoreErr := store.RestorePlanModeSnapshot(sessionID, previousPlanMode); restoreErr != nil {
				return fmt.Errorf("restore plan mode after linked plan mode event error %v: %w", err, restoreErr)
			}
			if restoreErr := store.RestorePlanModeHistory(sessionID, previousPlanModeHistory); restoreErr != nil {
				return fmt.Errorf("restore plan mode history after linked plan mode event error %v: %w", err, restoreErr)
			}
			return err
		}
		return errors.New("linked Plan Mode is not awaiting approval; submit the plan before approving the mission plan")
	}
	approvedAt := time.Now().UTC().Format(time.RFC3339Nano)
	goal, err = approveCLIMissionPlanWithEvent(store, sessionID, goal, session.MissionPlanApprovalInput{
		Source:           session.GoalSourceCLI,
		ApprovedAt:       approvedAt,
		CoverageOverride: overrideCoverage,
	}, nil)
	if err != nil {
		return err
	}
	if jsonMode {
		return json.NewEncoder(stdout).Encode(goal)
	}
	_, _ = fmt.Fprintf(stdout, "goal plan approved: %s\n", goal.GoalID)
	return nil
}

func approveCLIMissionPlanWithEvent(store *session.Store, sessionID string, goal session.SessionGoal, input session.MissionPlanApprovalInput, extra map[string]any) (session.SessionGoal, error) {
	previousHistory, err := store.LoadGoalHistory(sessionID)
	if err != nil {
		return session.SessionGoal{}, err
	}
	previousGoal := goal
	approved, err := store.ApproveMissionPlan(sessionID, input)
	if err != nil {
		return session.SessionGoal{}, err
	}
	eventData := map[string]any{
		"goal_id":           approved.GoalID,
		"plan_status":       approved.Mission.PlanStatus,
		"approved_at":       input.ApprovedAt,
		"coverage_override": input.CoverageOverride,
	}
	for key, value := range extra {
		eventData[key] = value
	}
	if err := store.AppendEvent(sessionID, events.New(sessionID, "mission.plan.approved", "goal", eventData)); err != nil {
		if restoreErr := store.SaveGoal(sessionID, previousGoal); restoreErr != nil {
			return session.SessionGoal{}, fmt.Errorf("restore goal after mission approval event error %v: %w", err, restoreErr)
		}
		if restoreErr := store.RestoreGoalHistory(sessionID, previousHistory); restoreErr != nil {
			return session.SessionGoal{}, fmt.Errorf("restore goal history after mission approval event error %v: %w", err, restoreErr)
		}
		return session.SessionGoal{}, err
	}
	return approved, nil
}

func appendCLIPlanModeLinkEvent(store *session.Store, sessionID string, previous session.PlanModeSnapshot, planMode session.PlanModeState, created bool) error {
	eventType := ""
	switch {
	case created:
		eventType = "planmode.created"
	case planMode.PlanModeID != "" && planMode.LinkedGoalID != "" && previous.State.LinkedGoalID != planMode.LinkedGoalID:
		eventType = "planmode.linked_goal"
	default:
		return nil
	}
	if err := store.AppendEvent(sessionID, events.New(sessionID, eventType, "goal", map[string]any{
		"plan_mode_id":   planMode.PlanModeID,
		"status":         planMode.Status,
		"linked_goal_id": planMode.LinkedGoalID,
	})); err != nil {
		return fmt.Errorf("record %s event: %w", eventType, err)
	}
	return nil
}

func goalValidationCommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: go-cli-agent goal validation show <session-id> [--json] [--config path]")
		return flag.ErrHelp
	}
	subcommand := args[0]
	subArgs := normalizeInterspersedFlags(args[1:], []string{"config"}, []string{"json"})
	fs := flag.NewFlagSet("goal validation "+subcommand, flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "")
	jsonMode := fs.Bool("json", false, "")
	if err := fs.Parse(subArgs); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("goal validation %s requires <session-id>", subcommand)
	}
	if subcommand != "show" {
		return fmt.Errorf("unknown goal validation subcommand: %s", subcommand)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	runner, _, err := storeRunnerLoader(*configPath, cwd)
	if err != nil {
		return err
	}
	goal, err := loadCLIGoal(runner.Store(), fs.Arg(0))
	if err != nil {
		return err
	}
	if *jsonMode {
		return json.NewEncoder(stdout).Encode(map[string]any{
			"validation_plan":     goal.ValidationPlan,
			"validation_contract": missionValidationContract(goal),
			"coverage":            session.CheckMissionPlanCoverage(goal),
		})
	}
	printGoalValidation(stdout, goal)
	return nil
}

func approveMissionCoverage(goal session.SessionGoal, override bool) error {
	coverage := session.CheckMissionPlanCoverage(goal)
	if !coverage.ApprovalBlocked || override {
		return nil
	}
	return fmt.Errorf("mission validation coverage blocks approval: %s; use --override-coverage to approve anyway", coverage.BlockingSummary())
}

func loadCLIGoal(store *session.Store, sessionID string) (session.SessionGoal, error) {
	if _, err := store.LoadMetadata(sessionID); err != nil {
		return session.SessionGoal{}, err
	}
	return store.LoadGoal(sessionID)
}

func mutateGoalStatus(stdout io.Writer, store *session.Store, sessionID, status, eventType, label string, jsonMode bool) error {
	previous, err := loadCLIGoal(store, sessionID)
	if err != nil {
		return err
	}
	previousHistory, err := store.LoadGoalHistory(sessionID)
	if err != nil {
		return err
	}
	goal, err := store.SetGoalStatus(sessionID, status, session.GoalSourceCLI)
	if err != nil {
		return err
	}
	if err := store.AppendGoalHistory(sessionID, session.GoalHistoryEntry{
		GoalID: goal.GoalID,
		Type:   eventType,
		Source: session.GoalSourceCLI,
		Status: goal.Status,
	}); err != nil {
		if restoreErr := store.SaveGoal(sessionID, previous); restoreErr != nil {
			return fmt.Errorf("restore goal after status history error %v: %w", err, restoreErr)
		}
		return err
	}
	if err := store.AppendEvent(sessionID, events.New(sessionID, eventType, "goal", map[string]any{
		"goal_id":   goal.GoalID,
		"status":    goal.Status,
		"mode":      goal.Mode,
		"objective": goal.Objective,
	})); err != nil {
		if restoreErr := store.SaveGoal(sessionID, previous); restoreErr != nil {
			return fmt.Errorf("restore goal after status event error %v: %w", err, restoreErr)
		}
		if restoreErr := store.RestoreGoalHistory(sessionID, previousHistory); restoreErr != nil {
			return fmt.Errorf("restore goal history after status event error %v: %w", err, restoreErr)
		}
		return err
	}
	if jsonMode {
		return json.NewEncoder(stdout).Encode(goal)
	}
	_, _ = fmt.Fprintf(stdout, "goal %s: %s (%s)\n", label, goal.GoalID, goal.Status)
	return nil
}

func printGoal(stdout io.Writer, goal session.SessionGoal) {
	fmt.Fprintf(stdout, "goal: %s\n", goal.GoalID)
	fmt.Fprintf(stdout, "mode: %s\n", goal.Mode)
	fmt.Fprintf(stdout, "status: %s\n", goal.Status)
	fmt.Fprintf(stdout, "objective: %s\n", goal.Objective)
	fmt.Fprintf(stdout, "tokens: %d", goal.TokensUsed)
	if goal.TokenBudget != nil {
		fmt.Fprintf(stdout, " / %d", *goal.TokenBudget)
	}
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "time_seconds: %d", goal.TimeUsedSeconds)
	if goal.TimeBudgetSeconds != nil {
		fmt.Fprintf(stdout, " / %d", *goal.TimeBudgetSeconds)
	}
	fmt.Fprintln(stdout)
	if len(goal.SuccessCriteria) > 0 {
		fmt.Fprintln(stdout, "success_criteria:")
		for _, criterion := range goal.SuccessCriteria {
			fmt.Fprintf(stdout, "- [%s] %s\n", criterion.Status, criterion.Text)
		}
	}
	if goal.Mission != nil {
		fmt.Fprintf(stdout, "mission_plan_status: %s\n", goal.Mission.PlanStatus)
	}
}

func printMissionPlan(stdout io.Writer, goal session.SessionGoal) {
	if goal.Mission == nil {
		fmt.Fprintln(stdout, "mission_plan: not recorded")
		return
	}
	mission := goal.Mission
	fmt.Fprintf(stdout, "mission_plan_status: %s\n", firstNonEmpty(mission.PlanStatus, "draft"))
	if mission.ApprovedAt != "" {
		fmt.Fprintf(stdout, "approved_at: %s\n", mission.ApprovedAt)
	}
	fmt.Fprintf(stdout, "features: %d\n", len(mission.Features))
	for _, feature := range mission.Features {
		fmt.Fprintf(stdout, "- %s [%s] %s", feature.ID, firstNonEmpty(feature.Status, "pending"), feature.Title)
		if len(feature.ClaimedAssertions) > 0 {
			fmt.Fprintf(stdout, " assertions=%s", strings.Join(feature.ClaimedAssertions, ","))
		}
		fmt.Fprintln(stdout)
	}
	fmt.Fprintf(stdout, "milestones: %d\n", len(mission.Milestones))
	for _, milestone := range mission.Milestones {
		fmt.Fprintf(stdout, "- %s [%s] %s", milestone.ID, firstNonEmpty(milestone.Status, "pending"), milestone.Title)
		if len(milestone.ValidationIDs) > 0 {
			fmt.Fprintf(stdout, " validation=%s", strings.Join(milestone.ValidationIDs, ","))
		}
		fmt.Fprintln(stdout)
	}
}

func printMissionCoverage(stdout io.Writer, coverage session.MissionPlanCoverage) {
	fmt.Fprintf(stdout, "validation_assertions: %d\n", coverage.ValidationTotal)
	fmt.Fprintf(stdout, "covered_assertions: %d\n", coverage.CoveredAssertions)
	fmt.Fprintf(stdout, "feature_covered_assertions: %d\n", coverage.FeatureCoveredAssertions)
	fmt.Fprintf(stdout, "milestone_covered_assertions: %d\n", coverage.MilestoneCoveredAssertions)
	fmt.Fprintf(stdout, "approval_blocked: %t\n", coverage.ApprovalBlocked)
	printStringList(stdout, "uncovered_assertions", coverage.UncoveredAssertions)
	printStringList(stdout, "features_without_assertions", coverage.FeaturesWithoutAssertions)
	printStringList(stdout, "milestones_without_validation", coverage.MilestonesWithoutValidation)
	printStringList(stdout, "duplicate_validation_ids", coverage.DuplicateValidationIDs)
	printStringList(stdout, "unknown_claimed_assertions", coverage.UnknownClaimedAssertions)
	printStringList(stdout, "unknown_milestone_validation_ids", coverage.UnknownMilestoneValidationIDs)
	if len(coverage.BlankValidationIndexes) > 0 {
		fmt.Fprintf(stdout, "blank_validation_indexes: %v\n", coverage.BlankValidationIndexes)
	}
}

func printGoalValidation(stdout io.Writer, goal session.SessionGoal) {
	fmt.Fprintf(stdout, "validation_plan: %d\n", len(goal.ValidationPlan))
	for _, validation := range goal.ValidationPlan {
		fmt.Fprintf(stdout, "- %s [%s] %s\n", validation.ID, firstNonEmpty(validation.Status, "pending"), validationDisplayText(validation))
	}
	contract := missionValidationContract(goal)
	fmt.Fprintf(stdout, "validation_contract: %d\n", len(contract))
	for _, validation := range contract {
		fmt.Fprintf(stdout, "- %s [%s] %s\n", validation.ID, firstNonEmpty(validation.Status, "pending"), validationDisplayText(validation))
	}
	printMissionCoverage(stdout, session.CheckMissionPlanCoverage(goal))
}

func missionValidationContract(goal session.SessionGoal) []session.GoalValidation {
	if goal.Mission == nil {
		return nil
	}
	return append([]session.GoalValidation(nil), goal.Mission.ValidationContract...)
}

func validationDisplayText(validation session.GoalValidation) string {
	if strings.TrimSpace(validation.Command) != "" {
		return validation.Command
	}
	if strings.TrimSpace(validation.Artifact) != "" {
		return validation.Artifact
	}
	if strings.TrimSpace(validation.Description) != "" {
		return validation.Description
	}
	return validation.Kind
}

func printStringList(stdout io.Writer, label string, values []string) {
	if len(values) == 0 {
		return
	}
	fmt.Fprintf(stdout, "%s:\n", label)
	for _, value := range values {
		fmt.Fprintf(stdout, "- %s\n", value)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
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
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
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
	for _, group := range []string{"in_progress", "ready", "blocked", "completed", "cancelled"} {
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
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
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
		if advice := providerProbeFailureAdvice(err); advice != "" {
			fmt.Fprintf(stderr, "advice: %s\n", advice)
		}
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
	ErrorClass    string         `json:"error_class,omitempty"`
	StatusCode    int            `json:"status_code,omitempty"`
	TimeoutKind   string         `json:"timeout_kind,omitempty"`
	Advice        string         `json:"advice,omitempty"`
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
		details := probeProviderErrorDetails(err)
		payload.ErrorClass, _ = details["error_class"].(string)
		payload.StatusCode, _ = details["status_code"].(int)
		payload.TimeoutKind, _ = details["timeout_kind"].(string)
		payload.Advice, _ = details["advice"].(string)
	}
	return payload
}

func probeProviderErrorDetails(err error) map[string]any {
	details := map[string]any{}
	var httpErr *provider.HTTPError
	if errors.As(err, &httpErr) {
		if strings.TrimSpace(httpErr.Class) != "" {
			details["error_class"] = httpErr.Class
		}
		if httpErr.StatusCode != 0 {
			details["status_code"] = httpErr.StatusCode
		}
		if strings.TrimSpace(httpErr.TimeoutKind) != "" {
			details["timeout_kind"] = httpErr.TimeoutKind
		}
	}
	if advice := providerProbeFailureAdvice(err); advice != "" {
		details["advice"] = advice
	}
	return details
}

func providerProbeFailureAdvice(err error) string {
	var httpErr *provider.HTTPError
	if !errors.As(err, &httpErr) {
		return ""
	}
	switch httpErr.Class {
	case "auth_error":
		return "Check the API key environment variable and provider account access."
	case "invalid_request":
		return "Check the provider profile, base URL, wire API, model, and request options."
	case "rate_limit":
		return "The provider rate limited the probe; retry later or adjust provider quota."
	case "upstream_timeout":
		return "Check provider availability, request timeout settings, and network or proxy stability."
	case "upstream_unavailable":
		return "Check network connectivity, TLS/proxy settings, and provider endpoint availability before changing model options."
	default:
		return ""
	}
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

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	runner, cfg, err := runnerLoader(*configPath, cwd)
	if err != nil {
		return err
	}

	report := doctorReport{
		ConfigPath:      defaultString(*configPath, filepath.Join(cwd, ".go-cli-agent", "config.yaml")),
		DefaultProvider: cfg.DefaultProvider,
	}

	report.Checks = append(report.Checks, doctorConfigFileCheck(*configPath, cwd, report.ConfigPath))

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

	if err := doctorValidateProviderConfig(selectedProvider, providerCfg); err != nil {
		details := doctorProviderConfigDetails(selectedProvider, providerCfg)
		details["error"] = err.Error()
		report.Checks = append(report.Checks, doctorCheck{
			Name:    "provider.config",
			Status:  "fail",
			Details: details,
		})
		return renderDoctorReport(stdout, report, *jsonMode, true)
	}
	report.Checks = append(report.Checks, doctorCheck{
		Name:    "provider.config",
		Status:  "ok",
		Details: doctorProviderConfigDetails(selectedProvider, providerCfg),
	})

	apiKey := providerCfg.ResolvedAPIKey()
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
	if err := fileutil.MkdirAllNoSymlink(cfg.Session.Dir, sessionDirMode); err != nil {
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
	report.Checks = append(report.Checks, checkRuntimeEnvironment(ctx, cwd))

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
			effectiveAPIProvider, _ := config.EffectiveAPIProvider(selectedProvider, providerCfg)
			status := "ok"
			details := map[string]any{
				"provider":        defaultString(result.Provider, selectedProvider),
				"model":           defaultString(result.Model, providerCfg.Model),
				"base_url":        defaultString(result.BaseURL, providerCfg.BaseURL),
				"api_provider":    defaultString(result.APIProvider, effectiveAPIProvider),
				"wire_api":        defaultString(result.WireAPI, providerCfg.WireAPI),
				"stop_reason":     result.StopReason,
				"tool_call_names": result.ToolCallNames,
				"finish_message":  result.FinishMessage,
			}
			if probeErr != nil {
				status = "fail"
				details["error"] = probeErr.Error()
				for key, value := range probeProviderErrorDetails(probeErr) {
					details[key] = value
				}
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

func doctorConfigFileCheck(explicitPath, cwd, reportPath string) doctorCheck {
	configStatus := "ok"
	configDetails := map[string]any{
		"path":   reportPath,
		"loaded": true,
	}
	if strings.TrimSpace(explicitPath) == "" {
		workspacePath := filepath.Join(cwd, ".go-cli-agent", "config.yaml")
		if filepath.Clean(reportPath) == filepath.Clean(workspacePath) && !config.WorkspaceConfigTrusted(cwd) {
			configStatus = "warn"
			configDetails["loaded"] = false
			configDetails["reason"] = "workspace_config_not_trusted"
			configDetails["advice"] = "Pass --config explicitly or create .go-cli-agent/trusted if this workspace config should be used."
		}
	}
	if info, err := os.Stat(reportPath); err != nil {
		if os.IsNotExist(err) {
			configStatus = "warn"
			configDetails["present"] = false
		} else {
			configStatus = "fail"
			configDetails["loaded"] = false
			configDetails["error"] = err.Error()
		}
	} else {
		configDetails["present"] = true
		configDetails["mode"] = info.Mode().Perm().String()
	}
	return doctorCheck{
		Name:    "config.file",
		Status:  configStatus,
		Details: configDetails,
	}
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
		storeValue, storeSource := doctorStoreDetails(effectiveAPIProvider, providerCfg.Store)
		sendMetadataValue, sendMetadataSource := doctorSendMetadataDetails(providerCfg.SendMetadata)
		details["store"] = storeValue
		details["store_source"] = storeSource
		details["send_metadata"] = sendMetadataValue
		details["send_metadata_source"] = sendMetadataSource
	}
	return details
}

func doctorValidateProviderConfig(providerName string, providerCfg config.Provider) error {
	apiProvider, err := config.EffectiveAPIProvider(providerName, providerCfg)
	if err != nil {
		return err
	}
	switch apiProvider {
	case "openai-compatible", "anthropic-compatible", "google":
		return nil
	default:
		return fmt.Errorf("unsupported api_provider for %s: %s", providerName, apiProvider)
	}
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

func doctorStoreDetails(effectiveAPIProvider string, configured *bool) (bool, string) {
	if configured != nil {
		return *configured, "config"
	}
	if strings.TrimSpace(effectiveAPIProvider) == "openai-compatible" {
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
	info, err := os.Lstat(cwd)
	if err != nil {
		check.Status = "fail"
		check.Details["error"] = err.Error()
		return check
	}
	if info.Mode()&os.ModeSymlink != 0 {
		check.Status = "fail"
		check.Details["error"] = fmt.Sprintf("workspace must not be a symlink: %s", cwd)
		return check
	}
	if !info.IsDir() {
		check.Status = "fail"
		check.Details["error"] = fmt.Sprintf("workspace is not a directory: %s", cwd)
		return check
	}
	if beforeWorkspaceWriteTempCreate != nil {
		if err := beforeWorkspaceWriteTempCreate(cwd); err != nil {
			check.Status = "fail"
			check.Details["error"] = err.Error()
			return check
		}
	}
	file, err := fileutil.CreateTempNoSymlink(cwd, ".doctor-write-*")
	if err != nil {
		check.Status = "fail"
		check.Details["error"] = err.Error()
		return check
	}
	path := file.Name()
	_ = file.Close()
	if err := fileutil.RemoveFileNoSymlink(path); err != nil {
		check.Status = "fail"
		check.Details["error"] = err.Error()
		return check
	}
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
var beforeWorkspaceWriteTempCreate func(cwd string) error
var beforeSessionDirModeProbeCreate func(dir string) error
var beforeSessionDirModeProbeChmod func(probeDir string) error
var beforeSessionDirModeProbeCleanup func(probeDir string) error

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
	if beforeSessionDirModeProbeCreate != nil {
		if err := beforeSessionDirModeProbeCreate(dir); err != nil {
			return result, err
		}
	}
	probeDir, err := fileutil.MkdirTempNoSymlink(dir, ".doctor-mode-*")
	if err != nil {
		return result, err
	}
	result.ProbeDir = probeDir
	defer func() {
		_ = fileutil.RemoveDirAllNoSymlink(probeDir)
	}()
	if beforeSessionDirModeProbeChmod != nil {
		if err := beforeSessionDirModeProbeChmod(probeDir); err != nil {
			return result, err
		}
	}
	if err := fileutil.ChmodPathNoSymlink(probeDir, expected); err != nil {
		result.ChmodError = err.Error()
		return result, err
	}
	info, err := os.Stat(probeDir)
	if err != nil {
		return result, err
	}
	result.ProbeMode = info.Mode().Perm()
	result.SupportsChmod = result.ChmodError == "" && result.ProbeMode == expected
	if beforeSessionDirModeProbeCleanup != nil {
		if err := beforeSessionDirModeProbeCleanup(probeDir); err != nil {
			return result, err
		}
	}
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
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
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
	effectiveSessionDir := strings.TrimSpace(*sessionDir)
	if effectiveSessionDir == "" {
		effectiveSessionDir = defaultInitSessionDir(cwd, cfg.Session.Dir)
	}
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
	if err := fileutil.MkdirAllNoSymlink(filepath.Join(cwd, "workspace"), 0o700); err != nil {
		return err
	}
	if err := fileutil.MkdirAllNoSymlink(filepath.Join(cwd, effectiveSkillDir, "example"), 0o700); err != nil {
		return err
	}
	skillBody := "---\nname: example\ndescription: Example local skill\n---\nWhen asked to inspect the repository, start with rg --files and targeted reads.\n"
	if err := fileutil.AtomicWriteFileNoSymlink(filepath.Join(cwd, effectiveSkillDir, "example", "SKILL.md"), []byte(skillBody), 0o600); err != nil {
		return err
	}
	toolDir := filepath.Join(cwd, effectiveSkillDir, "example", "tools")
	if err := fileutil.MkdirAllNoSymlink(toolDir, 0o700); err != nil {
		return err
	}
	exampleTool := "name: echo_args\ndescription: Echo JSON arguments for debugging\ncommand: [\"/bin/sh\", \"-lc\", \"cat\"]\ntimeout_sec: 15\ninput_schema:\n  type: object\n  properties:\n    message:\n      type: string\n"
	if err := fileutil.AtomicWriteFileNoSymlink(filepath.Join(toolDir, "echo.yaml"), []byte(exampleTool), 0o600); err != nil {
		return err
	}
	if *exampleHook {
		hookDir := filepath.Join(cwd, ".go-cli-agent", "hooks")
		if err := fileutil.MkdirAllNoSymlink(hookDir, 0o700); err != nil {
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

func defaultInitSessionDir(cwd, configured string) string {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return configured
	}
	resolved := configured
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(cwd, resolved)
	}
	if !strings.HasPrefix(filepath.Clean(resolved), "/mnt/") {
		return configured
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return configured
	}
	return filepath.Join(home, ".go-cli-agent", "sessions")
}

func resolvePrompt(args []string, stdin io.Reader) (string, error) {
	if len(args) > 0 {
		return strings.Join(args, " "), nil
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("prompt is required")
	}
	data, err := readPromptStdin(stdin)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(data), nil
}

func readPromptStdin(stdin io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(stdin, maxPromptStdinBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > maxPromptStdinBytes {
		return "", fmt.Errorf("stdin prompt exceeds maximum size: %d bytes", maxPromptStdinBytes)
	}
	return string(data), nil
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

func renderEventsUntilDone(ctx context.Context, sub <-chan events.Event, render func(events.Event), terminal func(events.Event) bool) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case evt := <-sub:
				render(evt)
				if terminal != nil && terminal(evt) {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return done
}

func waitForRenderer(done <-chan struct{}, cancel context.CancelFunc, drain bool) {
	if drain {
		select {
		case <-done:
			cancel()
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

func isTerminalSessionEvent(evt events.Event) bool {
	switch evt.Type {
	case "session.completed", "session.failed", "session.paused", "session.awaiting_input":
		return true
	default:
		return false
	}
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
	for _, key := range []string{"in_progress", "ready", "blocked", "completed", "cancelled", "done"} {
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
