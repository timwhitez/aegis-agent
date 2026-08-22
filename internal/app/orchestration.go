package app

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"aegis-agent/internal/runtime"
	"aegis-agent/internal/session"
)

func delegateCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	args = normalizeInterspersedFlags(args, []string{"agent", "role", "provider", "model", "config", "workdir", "system", "timeout", "isolation", "isolation-root"}, []string{"background", "json"})
	fs := flag.NewFlagSet("delegate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		agentName     = fs.String("agent", "", "")
		agentRole     = fs.String("role", "", "")
		providerName  = fs.String("provider", "", "")
		model         = fs.String("model", "", "")
		configPath    = fs.String("config", "", "")
		workdir       = fs.String("workdir", "", "")
		system        = fs.String("system", "", "")
		timeoutSec    = fs.Int("timeout", 0, "")
		background    = fs.Bool("background", false, "")
		jsonMode      = fs.Bool("json", false, "")
		isolationMode = fs.String("isolation", "", "")
		isolationRoot = fs.String("isolation-root", "", "")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("delegate requires <parent-session-id>")
	}
	parentSessionID := fs.Arg(0)
	prompt, err := resolvePrompt(fs.Args()[1:], os.Stdin)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	runner, _, err := experimentalRunnerLoader(*configPath, cwd)
	if err != nil {
		return err
	}
	callCtx := ctx
	if *timeoutSec > 0 && !*background {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, time.Duration(*timeoutSec)*time.Second)
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
	args = normalizeInterspersedFlags(args, []string{"config", "limit"}, []string{"json"})
	fs := flag.NewFlagSet("children", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		configPath = fs.String("config", "", "")
		limit      = fs.Int("limit", 50, "")
		jsonMode   = fs.Bool("json", false, "")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("children requires <session-id>")
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
	if _, err := store.LoadMetadata(fs.Arg(0)); err != nil {
		return err
	}
	result := runtime.ChildrenResult{}
	result.Sessions, err = store.ListChildren(fs.Arg(0), *limit)
	if err != nil {
		return err
	}
	result.Jobs, err = store.ListJobsByParent(fs.Arg(0), *limit)
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
		return fmt.Errorf("unknown queue subcommand: %s", args[0])
	}
}

func queueSubmitCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	args = normalizeInterspersedFlags(args, []string{"parent", "agent", "role", "provider", "model", "config", "workdir", "system", "mode", "wait-mode", "isolation", "isolation-root"}, []string{"json"})
	fs := flag.NewFlagSet("queue submit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		parentSessionID = fs.String("parent", "", "")
		agentName       = fs.String("agent", "", "")
		agentRole       = fs.String("role", "", "")
		providerName    = fs.String("provider", "", "")
		model           = fs.String("model", "", "")
		configPath      = fs.String("config", "", "")
		workdir         = fs.String("workdir", "", "")
		system          = fs.String("system", "", "")
		mode            = fs.String("mode", session.ModeExec, "")
		waitMode        = fs.String("wait-mode", "", "")
		jsonMode        = fs.Bool("json", false, "")
		isolationMode   = fs.String("isolation", "", "")
		isolationRoot   = fs.String("isolation-root", "", "")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	prompt, err := resolvePrompt(fs.Args(), os.Stdin)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
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
		return err
	}
	if *jsonMode {
		return json.NewEncoder(stdout).Encode(job)
	}
	_, _ = fmt.Fprintf(stdout, "queued job: %s\n", job.ID)
	return nil
}

func queueListCommand(args []string, stdout, stderr io.Writer) error {
	args = normalizeInterspersedFlags(args, []string{"config", "limit"}, []string{"json"})
	fs := flag.NewFlagSet("queue list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		configPath = fs.String("config", "", "")
		limit      = fs.Int("limit", 50, "")
		jsonMode   = fs.Bool("json", false, "")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cwd, err := os.Getwd()
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
	args = normalizeInterspersedFlags(args, []string{"config"}, []string{"json"})
	fs := flag.NewFlagSet("queue show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		configPath = fs.String("config", "", "")
		jsonMode   = fs.Bool("json", false, "")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("queue show requires <job-id>")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	runner, _, err := experimentalRunnerLoader(*configPath, cwd)
	if err != nil {
		return err
	}
	job, err := runner.QueueShow(fs.Arg(0))
	if err != nil {
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
	fs := flag.NewFlagSet("queue worker", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		configPath = fs.String("config", "", "")
		once       = fs.Bool("once", false, "")
		pollMS     = fs.Int("poll-ms", 1000, "")
		jsonMode   = fs.Bool("json", false, "")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*once && *pollMS <= 0 {
		return fmt.Errorf("queue worker poll-ms must be > 0")
	}
	cwd, err := os.Getwd()
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
