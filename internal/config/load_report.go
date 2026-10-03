package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"aegis-agent/internal/fileutil"

	"gopkg.in/yaml.v3"
)

// LoadSource describes an attempted layer without retaining configuration values
// or credentials. Path uses the safe reader's trimmed/cleaned path semantics.
// Outcome is loaded, missing, skipped_untrusted, read_error, or parse_error.
// Mode is available only after a successful safe file read.
type LoadSource struct {
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Outcome string `json:"outcome"`
	Mode    string `json:"mode,omitempty"`
}

// LoadReport records visited layers in precedence order. Complete is false when
// loading stopped at an error; later layers are not represented as attempted.
type LoadReport struct {
	Sources  []LoadSource `json:"sources"`
	Complete bool         `json:"complete"`
}

// LoadWithReport loads the same layers as Load and records their actual outcomes.
// The report is also retained on the returned configuration for diagnostics.
func LoadWithReport(explicitPath, cwd string) (*Config, LoadReport, error) {
	cfg := Default()
	report := LoadReport{Sources: []LoadSource{}}
	type candidate struct {
		kind, path string
		workspace  bool
	}
	var candidates []candidate
	if explicitPath == "" {
		home, _ := os.UserHomeDir()
		if home != "" {
			candidates = append(candidates, candidate{kind: "home", path: filepath.Join(home, ".aegis-agent", "config.yaml")})
		}
		candidates = append(candidates, candidate{kind: "workspace", path: filepath.Join(cwd, ".aegis-agent", "config.yaml"), workspace: true})
		if envPath := os.Getenv("AEGIS_AGENT_CONFIG"); envPath != "" {
			candidates = append(candidates, candidate{kind: "env", path: envPath})
		}
	} else {
		candidates = append(candidates, candidate{kind: "cli", path: explicitPath})
	}
	for _, c := range candidates {
		if c.path == "" {
			continue
		}
		path := strings.TrimSpace(c.path)
		if path != "" {
			path = filepath.Clean(path)
		}
		source := LoadSource{Kind: c.kind, Path: path}
		if c.workspace && !workspaceConfigTrusted(cwd) {
			source.Outcome = "skipped_untrusted"
			report.Sources = append(report.Sources, source)
			continue
		}
		data, info, err := fileutil.ReadRegularFileNoSymlink(c.path)
		if err != nil {
			source.Outcome = "read_error"
			if errors.Is(err, os.ErrNotExist) {
				source.Outcome = "missing"
			}
			report.Sources = append(report.Sources, source)
			if source.Outcome == "missing" {
				continue
			}
			return nil, report, err
		}
		source.Mode = info.Mode().Perm().String()
		if err := yaml.Unmarshal(data, cfg); err != nil {
			source.Outcome = "parse_error"
			report.Sources = append(report.Sources, source)
			return nil, report, err
		}
		source.Outcome = "loaded"
		report.Sources = append(report.Sources, source)
	}
	normalizeConfig(cfg, cwd)
	report.Complete = true
	cfg.loadReport = cloneLoadReport(report)
	return cfg, report, nil
}

// LoadReport returns a copy of the source facts from this configuration's load.
// Configurations constructed directly, without a load, have an incomplete empty
// report. This method never reads files or infers source facts from their paths.
func (cfg *Config) LoadReport() LoadReport {
	if cfg == nil {
		return LoadReport{}
	}
	return cloneLoadReport(cfg.loadReport)
}

func cloneLoadReport(report LoadReport) LoadReport {
	report.Sources = append([]LoadSource(nil), report.Sources...)
	return report
}
