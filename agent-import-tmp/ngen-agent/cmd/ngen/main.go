package main

import (
	"context"
	"os"

	"ngen/internal/app"
)

func main() {
	os.Exit(app.RunCLI(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
