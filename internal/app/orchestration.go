package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	"aegis-agent/internal/procutil"
	"aegis-agent/internal/runtime"
	"aegis-agent/internal/session"
)

func delegateCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	args = normalizeInterspersedFlags(args, []string{"agent", "role", "provider", "model", "config", "workdir", "system", "timeout", "isolation", "isolation-root"}, []string{"background", "json", "help", "h"})
	flagSet := flag.NewFlagSet("delegate", flag.ContinueOnError)
	var (
		agentName     = flagSet.String("agent", "", "Child agent name")
		agentRole     = flagSet.String("role", "", "Child agent role (default, explorer, generator, evaluator, planner)")
		providerName  = flagSet.String("provider", "", "Provider profile override")
		model         = flagSet.String("model", "", "Model override")
		configPath    = flagSet.String("config", "", "Path to config file")
		workdir       = flagSet.String("workdir", "", "Working directory override")
		system        = flagSet.String("system", "", "System prompt override")
		timeoutRaw    = flagSet.String("timeout", "", "Timeout as positive seconds or Go duration (e.g. 30 or 2m)")
		background    = flagSet.Bool("background", false, "Submit delegated child to the background queue")
		jsonMode      = flagSet.Bool("json", false, "Output JSON")
		isolationMode = flagSet.String("isolation", "", "Workspace isolation mode (off, auto, worktree)")
		isolationRoot = flagSet.String("isolation-root", "", "Isolation root directory")
	)
	if helped, err := parseFlagSet(flagSet, args, stdout, stderr, "usage: aegis-agent experimental delegate <parent-session-id> <prompt> [flags]"); err != nil || helped {
		return err
	}
	timeoutDuration, err := parseTimeoutDuration(*timeoutRaw)
	if err != nil {
		return err
	}
	if flagSet.NArg() < 1 {
		return fmt.Errorf("delegate requires <parent-session-id>")
	}
	parentSessionID := flagSet.Arg(0)
	prompt, err := resolvePrompt(flagSet.Args()[1:], os.Stdin)
	if err != nil {
		return err
	}
	cwd, err := procutil.Getwd()
	if err != nil {
		return err
	}
	runner, _, err := experimentalRunnerLoader(*configPath, cwd)
	if err != nil {
		return err
	}
	callCtx := ctx
	if timeoutDuration > 0 && !*background {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, timeoutDuration)
		defer cancel()
	}
	result, err := runner.Delegate(callCtx, runtime.DelegateRequest{
		ParentSessionID: parentSessionID,
		Prompt:          prompt,
		AgentName:       *agentName,
		AgentRole:       *agentRole,
		Provider:        *providerName,
		Model:           *model,
		Workdir:         *workdir,
		SystemOverride:  *system,
		Background:      *background,
		Mode:            session.ModeExec,
		IsolationMode:   *isolationMode,
		IsolationRoot:   *isolationRoot,
	})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("unknown session %s: %w", parentSessionID, fs.ErrNotExist)
		}
		return err
	}
	if *jsonMode {
		return json.NewEncoder(stdout).Encode(result)
	}
	if result.QueueJobID != "" {
		_, _ = fmt.Fprintf(stdout, "queued child job %s\n", result.QueueJobID)
		return nil
	}
	_, _ = fmt.Fprintf(stdout, "child session: %s (%s)\n", result.SessionID, result.Status)
	if strings.TrimSpace(result.Workdir) != "" {
		_, _ = fmt.Fprintf(stdout, "workdir: %s\n", result.Workdir)
	}
	if strings.TrimSpace(result.FinalText) != "" {
		_, _ = fmt.Fprintln(stdout, result.FinalText)
	}
	return nil
}

func childrenCommand(args []string, stdout, stderr io.Writer) error {
	args = normalizeInterspersedFlags(args, []string{"config", "limit"}, []string{"json", "help", "h"})
	flagSet := flag.NewFlagSet("children", flag.ContinueOnError)
	var (
		configPath = flagSet.String("config", "", "Path to config file")
		limit      = flagSet.Int("limit", 50, "Maximum number of child sessions and jobs to return (must be > 0)")
		jsonMode   = flagSet.Bool("json", false, "Output JSON")
	)
	if helped, err := parseFlagSet(flagSet, args, stdout, stderr, "usage: aegis-agent experimental children <session-id> [--limit N] [--config path] [--json]"); err != nil || helped {
		return err
	}
	if *limit <= 0 {
		return fmt.Errorf("--limit must be positive (got %d)", *limit)
	}
	if flagSet.NArg() < 1 {
		return fmt.Errorf("children requires <session-id>")
	}
	sessionID := flagSet.Arg(0)
	cwd, err := procutil.Getwd()
	if err != nil {
		return err
	}
	runner, _, err := storeRunnerLoader(*configPath, cwd)
	if err != nil {
		return err
	}
	store := runner.Store()
	if _, err := store.LoadMetadata(sessionID); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("unknown session %s: %w", sessionID, fs.ErrNotExist)
		}
		return err
	}
	result := runtime.ChildrenResult{}
	result.Sessions, err = store.ListChildren(sessionID, *limit)
	if err != nil {
		return err
	}
	result.Jobs, err = store.ListJobsByParent(sessionID, *limit)
	if err != nil {
		return err
	}
	if *jsonMode {
		return json.NewEncoder(stdout).Encode(result)
	}
	fmt.Fprintln(stdout, "== child sessions ==")
	for _, item := range result.Sessions {
		fmt.Fprintf(stdout, "%s  %s  %s  %s  workdir=%s\n", item.ID, item.Status, item.AgentName, item.Model, item.Workdir)
	}
	fmt.Fprintln(stdout, "\n== child jobs ==")
	for _, job := range result.Jobs {
		fmt.Fprintf(stdout, "%s  %s  %s  session=%s\n", job.ID, job.Status, job.AgentName, job.SessionID)
	}
	return nil
}

func queueCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("queue requires a subcommand: submit|list|show|worker")
	}
	if args[0] == "--help" || args[0] == "-h" || args[0] == "-help" {
		_, _ = fmt.Fprintln(stdout, "usage: aegis-agent experimental queue <submit|list|show|worker> [...]")
		return nil
	}
	switch args[0] {
	case "submit":
		return queueSubmitCommand(ctx, args[1:], stdout, stderr)
	case "list":
		return queueListCommand(args[1:], stdout, stderr)
	case "show":
		return queueShowCommand(args[1:], stdout, stderr)
	case "worker":
		return queueWorkerCommand(ctx, args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown queue subcommand %q (expected one of: submit, list, show, worker)", args[0])
	}
}

func queueSubmitCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	args = normalizeInterspersedFlags(args, []string{"parent", "agent", "role", "provider", "model", "config", "workdir", "system", "mode", "wait-mode", "isolation", "isolation-root"}, []string{"json", "help", "h"})
	flagSet := flag.NewFlagSet("queue submit", flag.ContinueOnError)
	var (
		parentSessionID = flagSet.String("parent", "", "Parent session ID")
		agentName       = flagSet.String("agent", "", "Child agent name")
		agentRole       = flagSet.String("role", "", "Child agent role (default, explorer, generator, evaluator, planner)")
		providerName    = flagSet.String("provider", "", "Provider profile override")
		model           = flagSet.String("model", "", "Model override")
		configPath      = flagSet.String("config", "", "Path to config file")
		workdir         = flagSet.String("workdir", "", "Working directory override")
		system          = flagSet.String("system", "", "System prompt override")
		mode            = flagSet.String("mode", session.ModeExec, "Execution mode (exec, run)")
		waitMode        = flagSet.String("wait-mode", "", "Parent wait mode (wait-all, wait-any, all, any, default)")
		jsonMode        = flagSet.Bool("json", false, "Output JSON")
		isolationMode   = flagSet.String("isolation", "", "Workspace isolation mode (off, auto, worktree)")
		isolationRoot   = flagSet.String("isolation-root", "", "Isolation root directory")
	)
	if helped, err := parseFlagSet(flagSet, args, stdout, stderr, "usage: aegis-agent experimental queue submit [flags] <prompt>"); err != nil || helped {
		return err
	}
	prompt, err := resolvePrompt(flagSet.Args(), os.Stdin)
	if err != nil {
		return err
	}
	cwd, err := procutil.Getwd()
	if err != nil {
		return err
	}
	runner, _, err := experimentalRunnerLoader(*configPath, cwd)
	if err != nil {
		return err
	}
	job, err := runner.QueueSubmit(ctx, runtime.QueueSubmitRequest{
		ParentSessionID: *parentSessionID,
		Prompt:          prompt,
		AgentName:       *agentName,
		AgentRole:       *agentRole,
		Provider:        *providerName,
		Model:           *model,
		Workdir:         *workdir,
		SystemOverride:  *system,
		Mode:            *mode,
		WaitMode:        *waitMode,
		IsolationMode:   *isolationMode,
		IsolationRoot:   *isolationRoot,
	})
	if err != nil {
		if strings.TrimSpace(*parentSessionID) != "" && errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("unknown session %s: %w", strings.TrimSpace(*parentSessionID), fs.ErrNotExist)
		}
		return err
	}
	if *jsonMode {
		return json.NewEncoder(stdout).Encode(job)
	}
	_, _ = fmt.Fprintf(stdout, "queued job: %s\n", job.ID)
	return nil
}

func queueListCommand(args []string, stdout, stderr io.Writer) error {
	args = normalizeInterspersedFlags(args, []string{"config", "limit"}, []string{"json", "help", "h"})
	flagSet := flag.NewFlagSet("queue list", flag.ContinueOnError)
	var (
		configPath = flagSet.String("config", "", "Path to config file")
		limit      = flagSet.Int("limit", 50, "Maximum number of queue jobs to return (must be > 0)")
		jsonMode   = flagSet.Bool("json", false, "Output JSON")
	)
	if helped, err := parseFlagSet(flagSet, args, stdout, stderr, "usage: aegis-agent experimental queue list [--limit N] [--config path] [--json]"); err != nil || helped {
		return err
	}
	if *limit <= 0 {
		return fmt.Errorf("--limit must be positive (got %d)", *limit)
	}
	cwd, err := procutil.Getwd()
	if err != nil {
		return err
	}
	runner, _, err := experimentalRunnerLoader(*configPath, cwd)
	if err != nil {
		return err
	}
	jobs, err := runner.QueueList(*limit)
	if err != nil {
		return err
	}
	if *jsonMode {
		return json.NewEncoder(stdout).Encode(jobs)
	}
	for _, job := range jobs {
		fmt.Fprintf(stdout, "%s  %s  agent=%s  session=%s  updated=%s\n", job.ID, job.Status, job.AgentName, job.SessionID, job.UpdatedAt)
	}
	return nil
}

func queueShowCommand(args []string, stdout, stderr io.Writer) error {
	args = normalizeInterspersedFlags(args, []string{"config"}, []string{"json", "help", "h"})
	flagSet := flag.NewFlagSet("queue show", flag.ContinueOnError)
	var (
		configPath = flagSet.String("config", "", "Path to config file")
		jsonMode   = flagSet.Bool("json", false, "Output JSON")
	)
	if helped, err := parseFlagSet(flagSet, args, stdout, stderr, "usage: aegis-agent experimental queue show <job-id> [--config path] [--json]"); err != nil || helped {
		return err
	}
	if flagSet.NArg() < 1 {
		return fmt.Errorf("queue show requires <job-id>")
	}
	jobID := flagSet.Arg(0)
	cwd, err := procutil.Getwd()
	if err != nil {
		return err
	}
	runner, _, err := experimentalRunnerLoader(*configPath, cwd)
	if err != nil {
		return err
	}
	job, err := runner.QueueShow(jobID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("unknown queue job %s: %w", jobID, fs.ErrNotExist)
		}
		return err
	}
	if *jsonMode {
		return json.NewEncoder(stdout).Encode(job)
	}
	data, _ := json.MarshalIndent(job, "", "  ")
	_, _ = fmt.Fprintln(stdout, string(data))
	return nil
}

func queueWorkerCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	args = normalizeInterspersedFlags(args, []string{"config", "poll-ms"}, []string{"once", "json", "help", "h"})
	flagSet := flag.NewFlagSet("queue worker", flag.ContinueOnError)
	var (
		configPath = flagSet.String("config", "", "Path to config file")
		once       = flagSet.Bool("once", false, "Process at most one queued job and exit")
		pollMS     = flagSet.Int("poll-ms", 1000, "Worker poll interval in milliseconds (must be > 0 unless --once)")
		jsonMode   = flagSet.Bool("json", false, "Output JSON")
	)
	if helped, err := parseFlagSet(flagSet, args, stdout, stderr, "usage: aegis-agent experimental queue worker [--once] [--poll-ms ms] [--config path] [--json]"); err != nil || helped {
		return err
	}
	if !*once && *pollMS <= 0 {
		return fmt.Errorf("queue worker poll-ms must be > 0")
	}
	cwd, err := procutil.Getwd()
	if err != nil {
		return err
	}
	runner, _, err := experimentalRunnerLoader(*configPath, cwd)
	if err != nil {
		return err
	}
	process := func() (bool, error) {
		job, ok, err := runner.ProcessNextJob(ctx)
		if err != nil {
			return ok, err
		}
		if !ok {
			if *jsonMode && *once {
				return false, json.NewEncoder(stdout).Encode(map[string]any{"idle": true})
			}
			if *once {
				_, _ = fmt.Fprintln(stdout, "queue idle")
			}
			return false, nil
		}
		if *jsonMode {
			if err := json.NewEncoder(stdout).Encode(job); err != nil {
				return true, err
			}
		} else {
			_, _ = fmt.Fprintf(stdout, "processed %s -> %s session=%s\n", job.ID, job.Status, job.SessionID)
		}
		return true, nil
	}
	if *once {
		_, err := process()
		return err
	}
	ticker := time.NewTicker(time.Duration(*pollMS) * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := process(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
