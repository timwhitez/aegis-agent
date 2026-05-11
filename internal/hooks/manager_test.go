package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go-cli-agent/internal/config"
)

func TestManagerInject(t *testing.T) {
	manager := New(config.HooksConfig{
		AssistantMessage: []config.HookDefinition{
			{
				Name: "prefix",
				Inject: &config.HookInject{
					Field:  "text",
					Prefix: "[interactive] ",
				},
			},
		},
	}, t.TempDir())

	payload, err := manager.Trigger(context.Background(), "assistant.message", map[string]any{
		"text": "raw value",
	})
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}
	if payload["text"] != "[interactive] raw value" {
		t.Fatalf("unexpected payload: %#v", payload["text"])
	}
}

func TestManagerRejectsFailClosed(t *testing.T) {
	manager := New(config.HooksConfig{
		UserMessage: []config.HookDefinition{
			{
				Name:       "block",
				FailClosed: true,
				Filter: &config.HookFilter{
					Field:            "text",
					RejectIfContains: "forbidden",
				},
			},
		},
	}, t.TempDir())

	if _, err := manager.Trigger(context.Background(), "user.message", map[string]any{"text": "forbidden"}); err == nil {
		t.Fatal("expected fail-closed rejection")
	}
}

func TestManagerSkipsMissingFieldInsteadOfCreatingIt(t *testing.T) {
	manager := New(config.HooksConfig{
		AssistantMessage: []config.HookDefinition{
			{
				Name:   "missing-field",
				Inject: &config.HookInject{Field: "missing", Prefix: "x"},
			},
		},
	}, t.TempDir())

	payload, err := manager.Trigger(context.Background(), "assistant.message", map[string]any{
		"text": "hello",
	})
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}
	if _, ok := payload["missing"]; ok {
		t.Fatalf("expected missing field to stay absent, got %#v", payload)
	}
}

func TestManagerEmitsCommandExitCodeOnFailure(t *testing.T) {
	manager := New(config.HooksConfig{
		UserMessage: []config.HookDefinition{
			{
				Name:    "failing-command",
				Command: []string{"/bin/sh", "-c", "exit 3"},
			},
		},
	}, t.TempDir())

	var failed map[string]any
	manager.SetEmitter(func(eventType string, data map[string]any) {
		if eventType == "hook.failed" {
			failed = data
		}
	})

	payload, err := manager.Trigger(context.Background(), "user.message", map[string]any{"text": "hello"})
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}
	if payload["text"] != "hello" {
		t.Fatalf("expected fail-open hook to preserve payload, got %#v", payload)
	}
	if failed == nil {
		t.Fatal("expected hook.failed event")
	}
	if failed["command_exit_code"] != 3 {
		t.Fatalf("expected exit code 3, got %#v", failed["command_exit_code"])
	}
}

func TestManagerTruncatesLargeHookCommandOutput(t *testing.T) {
	manager := New(config.HooksConfig{
		UserMessage: []config.HookDefinition{
			{
				Name:    "large-output",
				Command: []string{"/bin/sh", "-c", "yes A | head -n 20000"},
			},
		},
	}, t.TempDir())

	var command map[string]any
	manager.SetEmitter(func(eventType string, data map[string]any) {
		if eventType == "hook.command" {
			command = data
		}
	})

	if _, err := manager.Trigger(context.Background(), "user.message", map[string]any{"text": "hello"}); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	if command == nil {
		t.Fatal("expected hook.command event")
	}
	output, _ := command["output"].(string)
	if len(output) > hookCommandOutputLimit || !strings.Contains(output, "truncated") {
		t.Fatalf("expected truncated output, got len=%d output suffix=%q", len(output), output[max(0, len(output)-32):])
	}
	if command["truncated"] != true || command["raw_length"] == nil || command["exit_code"] != 0 || command["timeout"] != false {
		t.Fatalf("expected structured truncation metadata, got %#v", command)
	}
}

func TestManagerEmitsHookCommandTimeoutMetadata(t *testing.T) {
	manager := New(config.HooksConfig{
		UserMessage: []config.HookDefinition{
			{
				Name:       "timeout",
				Command:    []string{"/bin/sh", "-c", "sleep 2"},
				TimeoutSec: 1,
			},
		},
	}, t.TempDir())

	var command map[string]any
	manager.SetEmitter(func(eventType string, data map[string]any) {
		if eventType == "hook.command" {
			command = data
		}
	})

	if _, err := manager.Trigger(context.Background(), "user.message", map[string]any{"text": "hello"}); err != nil {
		t.Fatalf("fail-open timeout should preserve payload, got %v", err)
	}
	if command == nil || command["timeout"] != true || command["exit_code"] == nil || command["raw_length"] == nil || command["truncated"] == nil {
		t.Fatalf("expected timeout command metadata, got %#v", command)
	}
}

func TestManagerSkipsMissingFailOpenCommandWithWarning(t *testing.T) {
	manager := New(config.HooksConfig{
		UserMessage: []config.HookDefinition{
			{
				Name:    "missing-command",
				Command: []string{"definitely-missing-hook-command-for-preflight"},
			},
		},
	}, t.TempDir())

	var warning map[string]any
	var commandRan bool
	manager.SetEmitter(func(eventType string, data map[string]any) {
		if eventType == "hook.warning" {
			warning = data
		}
		if eventType == "hook.command" {
			commandRan = true
		}
	})

	payload, err := manager.Trigger(context.Background(), "user.message", map[string]any{"text": "hello"})
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}
	if payload["text"] != "hello" {
		t.Fatalf("expected fail-open hook to preserve payload, got %#v", payload)
	}
	if warning == nil || warning["reason"] != "missing_executable" {
		t.Fatalf("expected missing executable warning, got %#v", warning)
	}
	if commandRan {
		t.Fatal("expected missing fail-open hook to skip command execution")
	}
}

func TestManagerMissingFailClosedCommandBlocks(t *testing.T) {
	manager := New(config.HooksConfig{
		UserMessage: []config.HookDefinition{
			{
				Name:       "missing-command",
				Command:    []string{"definitely-missing-hook-command-for-preflight"},
				FailClosed: true,
			},
		},
	}, t.TempDir())

	var failed map[string]any
	manager.SetEmitter(func(eventType string, data map[string]any) {
		if eventType == "hook.failed" {
			failed = data
		}
	})

	if _, err := manager.Trigger(context.Background(), "user.message", map[string]any{"text": "hello"}); err == nil {
		t.Fatal("expected missing fail-closed hook to block")
	}
	if failed == nil || failed["error"] == "" {
		t.Fatalf("expected hook.failed event, got %#v", failed)
	}
}

func TestManagerPreflightsMissingRelativeShellScript(t *testing.T) {
	workdir := t.TempDir()
	manager := New(config.HooksConfig{
		SessionComplete: []config.HookDefinition{
			{
				Name:    "missing-script",
				Command: []string{"/bin/sh", ".go-cli-agent/hooks/session-complete.sh"},
			},
		},
	}, workdir)

	var warning map[string]any
	manager.SetEmitter(func(eventType string, data map[string]any) {
		if eventType == "hook.warning" {
			warning = data
		}
	})

	if _, err := manager.Trigger(context.Background(), "session.complete", map[string]any{"status": "completed"}); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	want := filepath.Join(workdir, ".go-cli-agent", "hooks", "session-complete.sh")
	if warning == nil || warning["reason"] != "missing_shell_script" || warning["missing_path"] != want {
		t.Fatalf("expected relative shell script warning for %s, got %#v", want, warning)
	}
}

func TestManagerRunsExistingRelativeShellScript(t *testing.T) {
	workdir := t.TempDir()
	script := filepath.Join(workdir, ".go-cli-agent", "hooks", "session-complete.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0o700); err != nil {
		t.Fatalf("mkdir hook dir: %v", err)
	}
	if err := os.WriteFile(script, []byte("#!/usr/bin/env sh\ncat > hook-payload.json\n"), 0o700); err != nil {
		t.Fatalf("write hook script: %v", err)
	}
	manager := New(config.HooksConfig{
		SessionComplete: []config.HookDefinition{
			{
				Name:    "existing-script",
				Command: []string{"/bin/sh", ".go-cli-agent/hooks/session-complete.sh"},
			},
		},
	}, workdir)

	if _, err := manager.Trigger(context.Background(), "session.complete", map[string]any{"status": "completed"}); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workdir, "hook-payload.json")); err != nil {
		t.Fatalf("expected hook script to run: %v", err)
	}
}
