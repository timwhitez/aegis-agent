//go:build e2e_config_guidance

package app

// These opt-in regressions for #127/#128 intentionally fail on the audited
// baseline. Remove the build tag when the coordinated production fixes land.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aegis-agent/internal/config"
	"aegis-agent/internal/hooks"
)

func guidanceIsolatedEnvironment(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("AEGIS_AGENT_CONFIG", "")
	t.Setenv("AEGIS_AGENT_TRUST_WORKSPACE_CONFIG", "")
	t.Setenv("AEGIS_AGENT_ENV_FILE", filepath.Join(t.TempDir(), "absent-fixture.env"))
	t.Setenv("OPENAI_API_KEY", "offline-guidance-fixture-only")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
}

func guidanceWorkingDirectory(t *testing.T, path string) {
	t.Helper()
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(before) })
}

func guidanceWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestE2EConfigDoctorReportsActualSelection(t *testing.T) {
	for _, scenario := range []string{"env_workspace", "env_other", "home", "home_equals_cwd", "missing_cli", "cli_over_env", "untrusted_workspace"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			home, cwd := filepath.Join(root, "home"), filepath.Join(root, "workspace")
			guidanceIsolatedEnvironment(t, home)
			if scenario == "home_equals_cwd" {
				cwd = home
			}
			guidanceWorkingDirectory(t, cwd)
			const body = "providers:\n  openai:\n    model: audit-config-model\n"
			workspacePath := filepath.Join(cwd, ".aegis-agent", "config.yaml")
			homePath := filepath.Join(home, ".aegis-agent", "config.yaml")
			wantPath := workspacePath
			wantLoaded := true
			wantModel := "audit-config-model"
			args := []string{"doctor", "--skip-probe", "--json"}
			switch scenario {
			case "env_workspace", "untrusted_workspace":
				guidanceWrite(t, workspacePath, body, 0o600)
				if scenario == "env_workspace" {
					t.Setenv("AEGIS_AGENT_CONFIG", workspacePath)
				} else {
					// A marker written by the workspace must not grant authority.
					guidanceWrite(t, filepath.Join(cwd, ".aegis-agent", "trusted"), "trusted\n", 0o600)
					wantLoaded, wantModel = false, config.Default().Providers["openai"].Model
				}
			case "env_other":
				wantPath = filepath.Join(root, "operator.yaml")
				guidanceWrite(t, wantPath, body, 0o600)
				t.Setenv("AEGIS_AGENT_CONFIG", wantPath)
			case "home", "home_equals_cwd":
				wantPath = homePath
				guidanceWrite(t, homePath, body, 0o600)
			case "missing_cli":
				wantPath = filepath.Join(root, "absent.yaml")
				wantLoaded, wantModel = false, config.Default().Providers["openai"].Model
				args = append(args, "--config", wantPath)
			case "cli_over_env":
				envPath := filepath.Join(root, "env.yaml")
				guidanceWrite(t, envPath, "providers:\n  openai:\n    model: ignored-env-model\n", 0o600)
				t.Setenv("AEGIS_AGENT_CONFIG", envPath)
				wantPath = filepath.Join(root, "explicit.yaml")
				guidanceWrite(t, wantPath, body, 0o600)
				args = append(args, "--config", wantPath)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var stdout, stderr bytes.Buffer
			err := Run(ctx, args, &stdout, &stderr)
			var report doctorReport
			if decodeErr := json.Unmarshal(stdout.Bytes(), &report); decodeErr != nil {
				t.Fatalf("doctor JSON: %v run=%v stderr=%s stdout=%s", decodeErr, err, stderr.String(), stdout.String())
			}
			// doctor can separately fail an unrelated host dependency check; the
			// selection and source facts still need to be internally consistent.
			if scenario != "untrusted_workspace" && report.ConfigPath != wantPath {
				t.Errorf("config_path=%q want actual selected path %q", report.ConfigPath, wantPath)
			}
			foundFile, foundProvider := false, false
			for _, check := range report.Checks {
				switch check.Name {
				case "config.file":
					foundFile = true
					if check.Details["loaded"] != wantLoaded {
						t.Errorf("config.file loaded=%v want=%v; details=%#v", check.Details["loaded"], wantLoaded, check.Details)
					}
					advice, _ := check.Details["advice"].(string)
					if strings.Contains(advice, "create .aegis-agent/trusted") {
						t.Error("doctor suggests a marker that cannot authorize workspace config")
					}
				case "provider.config":
					foundProvider = true
					if check.Details["model"] != wantModel {
						t.Errorf("effective model=%v want=%q", check.Details["model"], wantModel)
					}
				}
			}
			if !foundFile || !foundProvider {
				t.Fatalf("missing config/provider checks: %#v", report.Checks)
			}
		})
	}
}

func TestE2EConfigInitGuidanceSelectsConfigWithShellSafePath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "operator's workspace")
	guidanceWorkingDirectory(t, root)
	guidanceIsolatedEnvironment(t, t.TempDir())
	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), []string{"init", "--provider", "openai-compatible", "--example-hook=false"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, ".aegis-agent", "trusted")); !os.IsNotExist(err) {
		t.Fatalf("init must not write a trust marker: %v", err)
	}
	// Capture the shell's argument parsing of each printed next command. The
	// executable does no provider or runtime work and only writes fixture argv.
	argvPath := filepath.Join(root, "argv.txt")
	t.Setenv("AEGIS_GUIDANCE_ARGV_FIXTURE", argvPath)
	guidanceWrite(t, filepath.Join(root, "bin", "aegis-agent"), "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$AEGIS_GUIDANCE_ARGV_FIXTURE\"\n", 0o700)
	count := 0
	for _, line := range strings.Split(stdout.String(), "\n") {
		if !strings.HasPrefix(line, "next: ") {
			continue
		}
		count++
		command := strings.TrimPrefix(line, "next: ")
		output, err := exec.Command("/bin/sh", "-c", command).CombinedOutput()
		if err != nil {
			t.Errorf("printed command cannot execute: %q error=%v output=%s", command, err, output)
			continue
		}
		data, err := os.ReadFile(argvPath)
		if err != nil {
			t.Fatal(err)
		}
		argv := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		found := false
		for i := 0; i+1 < len(argv); i++ {
			if argv[i] == "--config" && argv[i+1] == filepath.Join(root, ".aegis-agent", "config.yaml") {
				found = true
			}
		}
		if !found {
			t.Errorf("printed next command omitted effective config: %q argv=%q", command, argv)
		}
	}
	if count != 3 {
		t.Fatalf("expected doctor, probe, run guidance; got %d commands", count)
	}
}

func TestE2EConfigGeneratedHookExecutesAndRecordsPayload(t *testing.T) {
	for _, scenario := range []string{"init_directory_control", "default_workspace", "custom_workspace", "moved_config"} {
		t.Run(scenario, func(t *testing.T) {
			invokeDir := filepath.Join(t.TempDir(), "operator's installation")
			guidanceWorkingDirectory(t, invokeDir)
			guidanceIsolatedEnvironment(t, t.TempDir())
			var stdout, stderr bytes.Buffer
			if err := Run(context.Background(), []string{"init", "--provider", "openai-compatible"}, &stdout, &stderr); err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(invokeDir, ".aegis-agent", "config.yaml")
			if scenario == "moved_config" {
				data, err := os.ReadFile(configPath)
				if err != nil {
					t.Fatal(err)
				}
				configPath = filepath.Join(t.TempDir(), "exported.yaml")
				guidanceWrite(t, configPath, string(data), 0o600)
			}
			cfg, err := config.Load(configPath, invokeDir)
			if err != nil {
				t.Fatal(err)
			}
			workdir := filepath.Join(invokeDir, "workspace")
			if scenario == "init_directory_control" {
				workdir = invokeDir
			} else if scenario == "custom_workspace" {
				workdir = t.TempDir()
			}
			manager := hooks.New(cfg.Hooks, workdir)
			commandSucceeded := false
			manager.SetEmitter(func(event string, data map[string]any) error {
				if event == "hook.command" && data["exit_code"] == 0 {
					commandSucceeded = true
				}
				return nil
			})
			payload := map[string]any{"session_id": "offline-hook-fixture", "status": "completed", "workdir": workdir}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			// A missing noncritical hook remains fail-open. Successful execution
			// must additionally have a real command exit and actual logged payload.
			if _, err := manager.Trigger(ctx, "session.complete", payload); err != nil {
				t.Fatalf("completion hook unexpectedly blocked: %v", err)
			}
			if !commandSucceeded {
				t.Error("generated hook did not produce hook.command exit_code=0")
			}
			data, err := os.ReadFile(filepath.Join(workdir, ".aegis-agent", "hooks", "logs", "session-complete.jsonl"))
			if err != nil {
				t.Fatalf("generated hook did not record actual completion payload: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(data), &got); err != nil || got["session_id"] != payload["session_id"] || got["status"] != "completed" || got["workdir"] != workdir {
				t.Fatalf("invalid hook payload: %s error=%v", data, err)
			}
		})
	}
}

func TestE2EConfigInitDisabledSampleAndCustomRelativeHook(t *testing.T) {
	invokeDir := t.TempDir()
	guidanceWorkingDirectory(t, invokeDir)
	guidanceIsolatedEnvironment(t, t.TempDir())
	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), []string{"init", "--example-hook=false"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(invokeDir, ".aegis-agent", "config.yaml"), invokeDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Hooks.SessionComplete) != 0 {
		t.Fatal("disabled sample installed a completion hook")
	}
	if _, err := os.Lstat(filepath.Join(invokeDir, ".aegis-agent", "hooks", "session-complete.sh")); !os.IsNotExist(err) {
		t.Fatalf("disabled sample generated a script: %v", err)
	}
	workdir := t.TempDir()
	guidanceWrite(t, filepath.Join(workdir, "custom.sh"), "#!/bin/sh\ncat > custom-payload.json\n", 0o700)
	cfg.Hooks.SessionComplete = []config.HookDefinition{{Name: "custom-relative-control", Command: []string{"/bin/sh", "custom.sh"}}}
	manager := hooks.New(cfg.Hooks, workdir)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := manager.Trigger(ctx, "session.complete", map[string]any{"status": "completed"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(workdir, "custom-payload.json"))
	if err != nil {
		t.Fatalf("custom relative hook did not execute in session workdir: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(data), &payload); err != nil || payload["status"] != "completed" {
		t.Fatalf("custom relative hook payload=%s error=%v", data, err)
	}
}
