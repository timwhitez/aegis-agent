package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func parseYAMLForTest(t *testing.T, raw string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("parse yaml: %v", err)
	}
	return &doc
}

// patchLayoutFixture is a config with the block separators an operator would
// write by hand: a blank line between top-level sections and between groups
// inside providers.
const patchLayoutFixture = `# Operator notes for this deployment.
schema_version: 1
default_provider: appintheloop

providers:
  appintheloop:
    # Anthropic Messages appends /v1/messages itself.
    api_provider: anthropic-compatible
    api_key_env: ANTHROPIC_API_KEY
    base_url: https://api.appintheloop.com
    model: MiniMax-M3.1-Flash
    context_window_tokens: 1000000
    output_config_effort: max

runtime:
  guardrails_mode: yolo
  max_turns_soft: 24

  compact:
    input_char_threshold: 0

hooks:
  default_timeout_sec: 300

# Browser tools stay off in this deployment.
tools:
  browser:
    enabled: false
    timeout_sec: 0
`

func writePatchLayoutFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(patchLayoutFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

func readPatchLayout(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// A single-field save must leave every other line — including blank separators —
// byte-identical.
func TestPatchFilePreservesBlankLineSeparators(t *testing.T) {
	dir, path := writePatchLayoutFixture(t)
	cfg, _, err := LoadWithReport(path, dir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	before := cfg.Runtime.MaxTurnsSoft
	after := before + 6
	if err := PatchFile(path, cfg, FilePatch{
		Runtime: RuntimePatch{MaxTurnsSoftBefore: before, MaxTurnsSoft: &after},
	}); err != nil {
		t.Fatalf("patch max_turns_soft: %v", err)
	}

	got := readPatchLayout(t, path)
	want := strings.Replace(patchLayoutFixture, "  max_turns_soft: 24\n", "  max_turns_soft: 30\n", 1)
	if got != want {
		t.Fatalf("blank-line separators or unrelated lines changed:\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

// Deleting a block must not leave the blank line that separated it hanging on an
// unrelated neighbour, and must keep the remaining separators.
func TestPatchFileKeepsBlankLinesAfterKeyDeletion(t *testing.T) {
	dir, path := writePatchLayoutFixture(t)
	cfg, _, err := LoadWithReport(path, dir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	// Clearing output_config_effort deletes that provider key.
	afterProvider := cfg.Providers["appintheloop"]
	afterProvider.OutputConfigEffort = ""
	if err := PatchFile(path, cfg, FilePatch{
		ProviderName:   "appintheloop",
		ProviderBefore: cfg.Providers["appintheloop"],
		ProviderAfter:  afterProvider,
	}); err != nil {
		t.Fatalf("patch output_config_effort: %v", err)
	}

	got := readPatchLayout(t, path)
	if strings.Contains(got, "output_config_effort") {
		t.Fatalf("deleted key still present:\n%s", got)
	}
	for _, want := range []string{
		"# Operator notes for this deployment.\n",
		"\nproviders:\n",
		"\n    # Anthropic Messages appends /v1/messages itself.\n",
		"\nruntime:\n",
		"\n  compact:\n",
		"\nhooks:\n",
		"\n# Browser tools stay off in this deployment.\ntools:\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q to survive the patch:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\n\n\n") {
		t.Fatalf("patch introduced a doubled blank separator:\n%s", got)
	}
	if _, _, err := LoadWithReport(path, dir); err != nil {
		t.Fatalf("patched config no longer loads: %v", err)
	}
}

// A nested block added by the patch must not swallow neighbouring separators nor
// duplicate them.
func TestPatchFileKeepsBlankLinesWhenAddingNestedBlock(t *testing.T) {
	dir, path := writePatchLayoutFixture(t)
	cfg, _, err := LoadWithReport(path, dir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	afterGuardrails := "deny"
	if err := PatchFile(path, cfg, FilePatch{
		Runtime: RuntimePatch{
			GuardrailsModeBefore: cfg.Runtime.GuardrailsMode,
			GuardrailsMode:       &afterGuardrails,
		},
	}); err != nil {
		t.Fatalf("patch guardrails_mode: %v", err)
	}

	got := readPatchLayout(t, path)
	if !strings.Contains(got, "  guardrails_mode: deny\n") {
		t.Fatalf("guardrails_mode not updated:\n%s", got)
	}
	for _, want := range []string{
		"\nproviders:\n",
		"\n  compact:\n",
		"\nhooks:\n",
		"\ntools:\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected separator %q to survive:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\n\n\n") {
		t.Fatalf("patch introduced a doubled blank separator:\n%s", got)
	}
}

// The layout helpers must work on the parsed tree without depending on node
// identity, so paths are matched structurally.
func TestBlankLineLayoutMatchesStructurally(t *testing.T) {
	doc := parseYAMLForTest(t, patchLayoutFixture)
	layout := captureBlankLineLayout(doc, []byte(patchLayoutFixture))

	for _, want := range []struct {
		path  []string
		count int
	}{
		{[]string{"providers"}, 1},
		{[]string{"runtime"}, 1},
		{[]string{"runtime", "compact"}, 1},
		{[]string{"hooks"}, 1},
		{[]string{"tools"}, 1},
	} {
		key := joinYAMLPath(want.path)
		if got := layout.blanks[key]; got != want.count {
			t.Fatalf("blankLines[%v]=%d, want %d (all=%#v)", want.path, got, want.count, layout.blanks)
		}
	}
	// Keys that were not separated by a blank line stay absent.
	if _, ok := layout.blanks[joinYAMLPath([]string{"schema_version"})]; ok {
		t.Fatalf("schema_version must not record a separator: %#v", layout.blanks)
	}
	if _, ok := layout.blanks[joinYAMLPath([]string{"runtime", "max_turns_soft"})]; ok {
		t.Fatalf("max_turns_soft must not record a separator: %#v", layout.blanks)
	}
}

func TestBlankLineLayoutSkipsNodeComments(t *testing.T) {
	// A blank line above a comment belongs to the comment's block.
	doc := parseYAMLForTest(t, "a: 1\n\n# note for b\nb: 2\n")
	layout := captureBlankLineLayout(doc, []byte("a: 1\n\n# note for b\nb: 2\n"))
	if got := layout.blanks[joinYAMLPath([]string{"b"})]; got != 1 {
		t.Fatalf("blankLines[b]=%d, want 1 (all=%#v)", got, layout.blanks)
	}
}

// Leading blank lines are not a separator between two blocks. Recording them
// would make restore() prepend a blank line to a document that never had one.
func TestBlankLineLayoutIgnoresLeadingBlankLines(t *testing.T) {
	const raw = "\n\na: 1\n\nb: 2\n"
	doc := parseYAMLForTest(t, raw)
	layout := captureBlankLineLayout(doc, []byte(raw))
	if _, ok := layout.blanks[joinYAMLPath([]string{"a"})]; ok {
		t.Fatalf("leading blanks recorded as a separator: %#v", layout.blanks)
	}
	if got := layout.blanks[joinYAMLPath([]string{"b"})]; got != 1 {
		t.Fatalf("blankLines[b]=%d, want 1 (all=%#v)", got, layout.blanks)
	}
	if restored := string(layout.restore([]byte("a: 1\n\nb: 2\n"))); strings.HasPrefix(restored, "\n") {
		t.Fatalf("restore introduced a leading blank line:\n%q", restored)
	}
}

// CRLF input must count separators the same way.
func TestBlankLineLayoutHandlesCRLF(t *testing.T) {
	const raw = "a: 1\r\n\r\n# note\r\nb: 2\r\n"
	doc := parseYAMLForTest(t, raw)
	layout := captureBlankLineLayout(doc, []byte(raw))
	if got := layout.blanks[joinYAMLPath([]string{"b"})]; got != 1 {
		t.Fatalf("blankLines[b]=%d, want 1 (all=%#v)", got, layout.blanks)
	}
}

// Multiple consecutive blank lines are preserved as authored.
func TestBlankLineLayoutKeepsMultipleBlanks(t *testing.T) {
	const raw = "a: 1\n\n\n\nb: 2\n"
	doc := parseYAMLForTest(t, raw)
	layout := captureBlankLineLayout(doc, []byte(raw))
	if got := layout.blanks[joinYAMLPath([]string{"b"})]; got != 3 {
		t.Fatalf("blankLines[b]=%d, want 3 (all=%#v)", got, layout.blanks)
	}
	if restored := string(layout.restore([]byte(raw))); restored != raw {
		t.Fatalf("restore changed an already-matching document:\nwant %q\ngot  %q", raw, restored)
	}
}
