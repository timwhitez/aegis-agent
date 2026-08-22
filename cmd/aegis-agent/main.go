package main

import (
	"context"
	"errors"
	"os"

	"aegis-agent/internal/app"
)

func main() {
	if err := app.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil && !errors.Is(err, context.Canceled) {
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
