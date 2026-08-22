package app

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"aegis-agent/internal/streamjson"
)

func modelsCommand(_ context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("models", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		jsonMode   = fs.Bool("json", false, "")
		configPath = fs.String("config", "", "")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("models does not accept positional arguments")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	cfg, err := loadConfig(*configPath, cwd)
	if err != nil {
		return err
	}
	models, err := streamjson.ModelsFromConfig(cfg)
	if err != nil {
		return err
	}
	if *jsonMode {
		return json.NewEncoder(stdout).Encode(models)
	}
	for _, model := range models {
		defaultMarker := ""
		if model.Default {
			defaultMarker = " default"
		}
		if model.Provider != "" {
			_, _ = fmt.Fprintf(stdout, "%s\t%s\t%s%s\n", model.ID, model.Label, model.Provider, defaultMarker)
		} else {
			_, _ = fmt.Fprintf(stdout, "%s\t%s%s\n", model.ID, model.Label, defaultMarker)
		}
	}
	return nil
}
