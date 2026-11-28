package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aegis-agent/internal/config"
	"aegis-agent/internal/runtime"
)

func TestProbeProviderHelpAndThinkingProbeFlag(t *testing.T) {
	var out, errBuf bytes.Buffer
	if err := Run(context.Background(), []string{"probe-provider", "--help"}, &out, &errBuf); err != nil {
		t.Fatalf("probe-provider --help: %v", err)
	}
	help := out.String() + errBuf.String()
	if strings.Contains(help, "chat_completions") {
		t.Fatalf("probe-provider help must not advertise retired chat_completions:\n%s", help)
	}
	if !strings.Contains(help, "-thinking-probe") {
		t.Fatalf("probe-provider help must expose -thinking-probe:\n%s", help)
	}

	out.Reset()
	errBuf.Reset()
	if err := Run(context.Background(), []string{"init", "--help"}, &out, &errBuf); err != nil {
		t.Fatalf("init --help: %v", err)
	}
	initHelp := out.String() + errBuf.String()
	if strings.Contains(initHelp, "chat_completions") {
		t.Fatalf("init help must not advertise retired chat_completions:\n%s", initHelp)
	}
}

func TestProbeProviderWiresThinkingProbeFlagToRunner(t *testing.T) {
	var captured runtime.ProbeRequest
	origLoader := runnerLoader
	t.Cleanup(func() { runnerLoader = origLoader })
	cfg := config.Default()
	cfg.Providers["openai"] = config.Provider{
		APIProvider: "openai-compatible",
		BaseURL:     "https://api.openai.com/v1",
		Model:       "gpt-test",
		APIKeyEnv:   "OPENAI_API_KEY",
	}
	runnerLoader = func(_, _ string) (coreRunner, *config.Config, error) {
		return stubProbeRunner{onProbe: func(req runtime.ProbeRequest) (runtime.ProbeResult, error) {
			captured = req
			return runtime.ProbeResult{Provider: "openai", Model: "gpt-test", StopReason: "tool_use"}, nil
		}}, cfg, nil
	}
	var out, errBuf bytes.Buffer
	if err := Run(context.Background(), []string{"probe-provider", "--allow-builtin-config", "--thinking-probe", "--json"}, &out, &errBuf); err != nil {
		t.Fatalf("probe-provider --thinking-probe: %v (stderr=%s)", err, errBuf.String())
	}
	if !captured.ThinkingProbe {
		t.Fatalf("expected ThinkingProbe=true to reach runner.Probe, got %#v", captured)
	}
}

type stubProbeRunner struct {
	coreRunner
	onProbe func(runtime.ProbeRequest) (runtime.ProbeResult, error)
}

func (s stubProbeRunner) Probe(_ context.Context, req runtime.ProbeRequest) (runtime.ProbeResult, error) {
	return s.onProbe(req)
}

func TestDoctorWarnsOnExcludedSkillCommandTools(t *testing.T) {
	repo := t.TempDir()
	skillsDir := filepath.Join(repo, "skills")
	toolsDir := filepath.Join(skillsDir, "my-skill", "tools")
	if err := os.MkdirAll(toolsDir, 0o700); err != nil {
		t.Fatalf("mkdir tools: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "my-skill", "SKILL.md"), []byte("---\nname: my-skill\ndescription: demo\n---\nbody\n"), 0o600); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(toolsDir, "my_tool.yaml"), []byte("name: my_tool\ncommand: [\"echo\", \"hi\"]\ninput_schema:\n  type: object\n"), 0o600); err != nil {
		t.Fatalf("write tool: %v", err)
	}
	sessDir := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(sessDir, 0o700); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	cfgYAML := "schema_version: 1\ndefault_provider: openai\nproviders:\n  openai:\n    api_provider: openai-compatible\n    base_url: https://api.openai.com/v1\n    model: gpt-5.4\n    api_key_env: OPENAI_API_KEY\nsession:\n  dir: " + sessDir + "\n  dir_mode: \"0700\"\nskills:\n  dirs:\n    - " + skillsDir + "\n"
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Chdir(repo)
	var out, errBuf bytes.Buffer
	if err := Run(context.Background(), []string{"doctor", "--config", cfgPath, "--skip-probe", "--json"}, &out, &errBuf); err != nil {
		t.Fatalf("doctor: %v (stderr=%s)", err, errBuf.String())
	}
	var report doctorReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode doctor json: %v\n%s", err, out.String())
	}
	var found bool
	for _, check := range report.Checks {
		if check.Name != "skills.scan" {
			continue
		}
		found = true
		if check.Status != "warn" {
			t.Fatalf("expected skills.scan warn when command tool excluded, got %#v", check)
		}
		raw, _ := json.Marshal(check.Details["excluded_command_tools"])
		if !strings.Contains(string(raw), "my_tool") {
			t.Fatalf("expected excluded_command_tools to list my_tool, got %s", raw)
		}
	}
	if !found {
		t.Fatalf("skills.scan check missing from doctor report: %#v", report.Checks)
	}
}

func TestStartCLITTYInputRejectsNilStdin(t *testing.T) {
	if _, err := startCLITTYInput(context.Background(), nil, func() {}); err == nil {
		t.Fatal("expected error for nil stdin")
	}
}
