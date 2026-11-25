package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"aegis-agent/internal/procutil"
	agenttui "aegis-agent/internal/tui"
)

func tuiCommand(args []string, stdout, stderr io.Writer) error {
	args = normalizeInterspersedFlags(args, []string{"config", "session", "limit", "refresh-ms"}, []string{"once", "help", "h"})
	flagSet := flag.NewFlagSet("tui", flag.ContinueOnError)
	var (
		configPath = flagSet.String("config", "", "Path to config file")
		sessionID  = flagSet.String("session", "", "Focus a specific session ID")
		limit      = flagSet.Int("limit", 20, "Maximum number of sessions to display (must be > 0)")
		once       = flagSet.Bool("once", false, "Render one snapshot and exit")
		refreshMS  = flagSet.Int("refresh-ms", 1000, "Refresh interval in milliseconds (must be > 0)")
	)
	if helped, err := parseFlagSet(flagSet, args, stdout, stderr, "usage: aegis-agent experimental tui [--session id] [--limit N] [--once] [--refresh-ms ms] [--config path]"); err != nil || helped {
		return err
	}
	if *limit <= 0 {
		return fmt.Errorf("--limit must be positive (got %d)", *limit)
	}
	if !*once && *refreshMS <= 0 {
		return fmt.Errorf("--refresh-ms must be positive (got %d)", *refreshMS)
	}
	cwd, err := procutil.Getwd()
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
			if strings.TrimSpace(*sessionID) != "" && errors.Is(err, fs.ErrNotExist) {
				return unknownSessionErr(*sessionID)
			}
			return err
		}
		_, _ = io.WriteString(stdout, agenttui.Render(snapshot))
		return nil
	}
	return agenttui.Run(context.Background(), runner.Store(), *sessionID, *limit, *refreshMS, os.Stdout, os.Stdin)
}
