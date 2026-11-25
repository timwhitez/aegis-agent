package app

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"aegis-agent/internal/config"
	"aegis-agent/internal/procutil"
	"aegis-agent/internal/streamjson"
)

func modelsCommand(_ context.Context, args []string, stdout, stderr io.Writer) error {
	args = normalizeInterspersedFlags(args, []string{"config"}, []string{"json", "declared", "help", "h"})
	fs := flag.NewFlagSet("models", flag.ContinueOnError)
	var (
		jsonMode     = fs.Bool("json", false, "Output models as JSON array")
		configPath   = fs.String("config", "", "Path to config YAML file")
		declaredOnly = fs.Bool("declared", false, "List only providers explicitly declared in loaded config files")
	)
	if shown, err := parseFlagSet(fs, args, stdout, stderr, "usage: aegis-agent models [--config path] [--declared] [--json]"); err != nil {
		return err
	} else if shown {
		return nil
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("models does not accept positional arguments")
	}
	cwd, err := procutil.Getwd()
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
	if *declaredOnly {
		filtered := make([]streamjson.Model, 0, len(models))
		for _, model := range models {
			if model.Source == config.ProviderSourceFile {
				filtered = append(filtered, model)
			}
		}
		models = filtered
	}
	if *jsonMode {
		return json.NewEncoder(stdout).Encode(models)
	}
	for _, model := range models {
		defaultMarker := ""
		if model.Default {
			defaultMarker = " default"
		}
		source := model.Source
		if source == "" {
			source = config.ProviderSourceBuiltin
		}
		if model.Provider != "" {
			_, _ = fmt.Fprintf(stdout, "%s\t%s\t%s\t%s%s\n", model.ID, model.Label, model.Provider, source, defaultMarker)
		} else {
			_, _ = fmt.Fprintf(stdout, "%s\t%s\t%s%s\n", model.ID, model.Label, source, defaultMarker)
		}
	}
	return nil
}
