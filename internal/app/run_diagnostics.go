package app

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"aegis-agent/internal/config"
	"aegis-agent/internal/events"
	"aegis-agent/internal/session"
)

type runDiagnosticSelection struct {
	mode, provider, model, workdir string
	resume                         bool
}

// The renderer and interactive input can write stderr concurrently. Share this
// wrapper across both, even when the supplied writer is a plain bytes.Buffer.
type runStderrWriter struct {
	mu  sync.Mutex
	out io.Writer
}

func (w *runStderrWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.out.Write(p)
}

// Diagnostics observe the loader's facts and the runtime's resolved metadata.
// They neither inspect skipped paths nor recompute provider selection.
type runDiagnostics struct {
	mu        sync.Mutex
	reported  bool
	primaryID string
	report    config.LoadReport
	selection runDiagnosticSelection
	exe       string
	out       io.Writer
}

func newRunDiagnostics(cfg *config.Config, selection runDiagnosticSelection, out io.Writer) *runDiagnostics {
	exe, err := os.Executable()
	if err != nil {
		exe = "aegis-agent"
	}
	return &runDiagnostics{report: cfg.LoadReport(), selection: selection, exe: exe, out: out}
}

func (d *runDiagnostics) sessionActive(meta session.SessionMetadata) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.reported {
		d.reported = true
		d.primaryID = meta.ID
		if !d.report.Complete {
			_, _ = fmt.Fprintln(d.out, "config layer: source report unavailable")
		}
		for _, source := range d.report.Sources {
			_, _ = fmt.Fprintf(d.out, "config layer: kind=%q path=%q outcome=%q\n", source.Kind, source.Path, source.Outcome)
			if source.Kind == "workspace" && source.Outcome == "skipped_untrusted" {
				_, _ = fmt.Fprintln(d.out, "workspace candidate not inspected; if you review and choose this complete config, select it explicitly:")
				d.command("config selection template", d.exe, d.selection.mode, "--config", source.Path, "Your prompt here.")
			}
		}
	}
	metadata := "unset"
	if meta.ProviderOptions.APIProvider == "openai-compatible" {
		metadata = "enabled_default"
	}
	if meta.ProviderOptions.SendMetadata != nil {
		metadata = fmt.Sprint(*meta.ProviderOptions.SendMetadata)
	}
	_, _ = fmt.Fprintf(d.out, "execution target: session=%q profile=%q model=%q api_provider=%q endpoint=%q (origin only; path/query/fragment/userinfo hidden) send_metadata=%s\n", meta.ID, meta.Provider, meta.Model, meta.ProviderOptions.APIProvider, safeDiagnosticEndpoint(meta.ProviderOptions.BaseURL), metadata)
	// Delegated runners inherit this observer, but their role selections cannot
	// be reproduced by the parent invocation's CLI flags. A directly selected
	// child session is still primary because it supplies the first callback.
	primary := meta.ID == d.primaryID
	if primary && d.selection.resume {
		_, _ = fmt.Fprintln(d.out, "resume target uses the session's effective provider snapshot; editing a profile does not replace already recorded options.")
	}
	if meta.ProviderOptions.APIProvider != "openai-compatible" || (meta.ProviderOptions.SendMetadata != nil && !*meta.ProviderOptions.SendMetadata) {
		return
	}
	_, _ = fmt.Fprintln(d.out, "metadata compatibility: enabled; automatic unsupported-metadata fallback is scoped to this adapter instance. A new Start/Continue can discover it again within the same process.")
	_, _ = fmt.Fprintf(d.out, "For a known rejecting gateway, add send_metadata: false inside the existing complete providers[%q] block; preserve its other fields. Later layers can replace the entire profile.\n", meta.Provider)
	if !primary {
		_, _ = fmt.Fprintln(d.out, "Delegated session: the parent CLI selection does not provide a standalone fresh-session recipe for this child role. Verify its actual target after editing the profile.")
		return
	}
	_, _ = fmt.Fprintln(d.out, "Start a new session with the same selection/environment after editing; recorded session options (including default nil) remain a snapshot. The new session resolves current configuration endpoint/options; verify its execution target.")
	loaded := false
	var explicit string
	for _, source := range d.report.Sources {
		loaded = loaded || source.Outcome == "loaded"
		if source.Kind == "cli" {
			explicit = source.Path
		}
	}
	if d.report.Complete && !loaded {
		_, _ = fmt.Fprintln(d.out, "No config file loaded. Create and review a complete operator config (init --config <chosen-path>), add the option to its profile, then explicitly select that file.")
		return
	}
	args := []string{d.exe, d.selection.mode}
	if explicit != "" {
		args = append(args, "--config", explicit)
	}
	provider, model := d.selection.provider, d.selection.model
	if d.selection.resume {
		// A resumed profile/model can differ from today's config defaults. This
		// is a fresh-session template, not restoration of its durable options.
		provider, model = meta.Provider, meta.Model
		if provider != strings.ToLower(strings.TrimSpace(provider)) || strings.EqualFold(provider, "default") || model != strings.TrimSpace(model) || strings.EqualFold(model, "default") {
			_, _ = fmt.Fprintln(d.out, "new session: review the current config and choose provider/model explicitly; these stored selectors cannot be reproduced by normalized CLI flags")
			return
		}
	}
	for _, option := range []struct{ name, value string }{{"--provider", provider}, {"--model", model}, {"--workdir", d.selection.workdir}} {
		if option.value != "" {
			args = append(args, option.name, option.value)
		}
	}
	args = append(args, "Your prompt here.")
	d.command("new session", args...)
}

// Actual fallback notices retain the existing best-effort root-bus observation.
// Actionable compatibility guidance above does not depend on event delivery.
func (d *runDiagnostics) handle(evt events.Event) {
	if evt.Type != "provider.capability_fallback" || evt.Data["feature"] != "metadata" || evt.Data["reason"] != "unsupported_argument" {
		return
	}
	profile, ok := evt.Data["provider_profile"].(string)
	if !ok {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, _ = fmt.Fprintf(d.out, "metadata fallback: session=%q profile=%q rejected metadata; adapter retries without metadata.\n", evt.SessionID, profile)
}

func (d *runDiagnostics) command(label string, args ...string) {
	quoted := make([]string, len(args))
	for i, arg := range args {
		if !utf8.ValidString(arg) || strings.IndexFunc(arg, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
			_, _ = fmt.Fprintf(d.out, "%s: executable template omitted because a value contains non-printable characters\n", label)
			return
		}
		quoted[i] = quoteShellArgument(arg)
	}
	_, _ = fmt.Fprintf(d.out, "%s: %s\n", label, strings.Join(quoted, " "))
}

func safeDiagnosticEndpoint(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.Opaque != "" {
		return "[endpoint unavailable]"
	}
	return u.Scheme + "://" + u.Host
}
