package app

import (
	"context"
	"flag"
	"io"
	"os"

	agenttui "go-cli-agent/internal/tui"
)

func tuiCommand(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		configPath = fs.String("config", "", "")
		sessionID  = fs.String("session", "", "")
		limit      = fs.Int("limit", 20, "")
		once       = fs.Bool("once", false, "")
		refreshMS  = fs.Int("refresh-ms", 1000, "")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	runner, _, err := storeRunnerLoader(*configPath, cwd)
	if err != nil {
		return err
	}
	if *once {
		snapshot, err := agenttui.BuildSnapshot(runner.Store(), *sessionID, *limit)
		if err != nil {
			return err
		}
		_, _ = io.WriteString(stdout, agenttui.Render(snapshot))
		return nil
	}
	return agenttui.Run(context.Background(), runner.Store(), *sessionID, *limit, *refreshMS, os.Stdout, os.Stdin)
}
