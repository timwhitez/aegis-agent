package app

import (
	"flag"
	"fmt"

	"aegis-agent/internal/config"
	"aegis-agent/internal/runtime"
)

// Use the actual load report, without checking a skipped workspace candidate.
// Directly assembled configs (SDK/tests) have no report and are explicit inputs.
func admitProviderConfig(cfg *config.Config, allowBuiltin bool) error {
	report := cfg.LoadReport()
	if !report.Complete || allowBuiltin {
		return nil
	}
	for _, source := range report.Sources {
		if source.Outcome == "loaded" {
			return nil
		}
	}
	return runtime.WrapConfigError(fmt.Errorf("no configuration file loaded; provider execution requires explicit selection. Review a complete config and select it with --config <path> or AEGIS_AGENT_CONFIG; generate one with init if needed. A workspace candidate skipped as workspace_config_not_trusted was not inspected: parent-process AEGIS_AGENT_TRUST_WORKSPACE_CONFIG=1 authorizes implicit loading. To deliberately use builtin provider defaults, pass --allow-builtin-config (this does not trust workspace config)"))
}

func explicitlySet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}
