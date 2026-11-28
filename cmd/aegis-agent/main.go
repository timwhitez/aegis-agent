package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"aegis-agent/internal/app"
	"aegis-agent/internal/procutil"
	"aegis-agent/internal/runtime"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "__signal-process" {
		os.Exit(procutil.RunSignalProcessCommand(os.Args[2:], os.Stderr))
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		select {
		case <-sigCh:
			// Release signal interception after the first graceful signal so a
			// second SIGINT/SIGTERM immediately terminates the process if cleanup blocks.
			signal.Stop(sigCh)
			cancel(runtime.ErrSignalInterrupt)
		case <-ctx.Done():
		}
	}()
	if err := app.Run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil && !errors.Is(err, context.Canceled) {
		var exitErr app.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.Code)
		}
		var classified app.ClassifiedError
		if errors.As(err, &classified) {
			if classified.Err != nil {
				_, _ = os.Stderr.WriteString(classified.Err.Error() + "\n")
			}
			os.Exit(classified.Code)
		}
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}
