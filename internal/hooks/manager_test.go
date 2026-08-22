package hooks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"go-cli-agent/internal/config"
)

func TestBoundedHookOutputKeepsUTF8Boundaries(t *testing.T) {
	input := strings.Repeat("钩子输出", 200)
	collector := newBoundedHookOutput(257)
	for offset := 0; offset < len(input); offset += 64 {
		end := offset + 64
		if end > len(input) {
			end = len(input)
		}
		if _, err := collector.Write([]byte(input[offset:end])); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	output, rawLength, truncated := collector.result()
	if !truncated {
		t.Fatal("expected output to be truncated")
	}
	if rawLength != len(input) {
		t.Fatalf("expected raw length %d, got %d", len(input), rawLength)
	}
	if !utf8.ValidString(output) {
		t.Fatalf("expected valid UTF-8 output, got %q", output)
	}
	if strings.ContainsRune(output, utf8.RuneError) {
		t.Fatalf("expected no replacement rune from mid-rune truncation, got %q", output)
	}
	if !strings.HasSuffix(output, "\n[... truncated ...]") {
		t.Fatalf("expected truncation marker suffix, got %q", output)
	}
	if !strings.HasPrefix(output, "钩子输出") {
		t.Fatalf("expected retained prefix of the hook output, got %q", output)
	}
}

func TestBoundedHookOutputTrimsPartialRuneBelowSuffixLength(t *testing.T) {
	// A limit smaller than the truncation marker leaves the byte cap itself as
	// the only boundary, so a partial trailing rune must still be dropped.
	collector := newBoundedHookOutput(5)
	if _, err := collector.Write([]byte(strings.Repeat("钩子输出", 4))); err != nil {
		t.Fatalf("write: %v", err)
	}
	output, rawLength, truncated := collector.result()
	if !truncated {
		t.Fatal("expected output to be truncated")
	}
	if rawLength != 48 {
		t.Fatalf("expected raw length 48, got %d", rawLength)
	}
	if !utf8.ValidString(output) {
		t.Fatalf("expected valid UTF-8 output, got %q", output)
	}
	if strings.ContainsRune(output, utf8.RuneError) {
		t.Fatalf("expected no replacement rune from mid-rune truncation, got %q", output)
	}
	if output != "钩" {
		t.Fatalf("expected only the first whole rune, got %q", output)
	}
}

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
	manager.SetEmitter(func(eventType string, data map[string]any) error {
		if eventType == "hook.failed" {
			failed = data
		}
		return nil
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

func TestManagerPropagatesCallerCancellationForFailOpenHook(t *testing.T) {
	manager := New(config.HooksConfig{
		UserMessage: []config.HookDefinition{
			{
				Name:    "cancelled-command",
				Command: []string{"/bin/sh", "-c", "sleep 30"},
			},
		},
	}, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	started := time.Now()
	_, err := manager.Trigger(ctx, "user.message", map[string]any{"text": "hello"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected caller cancellation, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("cancelled fail-open hook returned too slowly: %s", elapsed)
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
	manager.SetEmitter(func(eventType string, data map[string]any) error {
		if eventType == "hook.command" {
			command = data
		}
		return nil
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
	manager.SetEmitter(func(eventType string, data map[string]any) error {
		if eventType == "hook.command" {
			command = data
		}
		return nil
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
	manager.SetEmitter(func(eventType string, data map[string]any) error {
		if eventType == "hook.warning" {
			warning = data
		}
		if eventType == "hook.command" {
			commandRan = true
		}
		return nil
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
	manager.SetEmitter(func(eventType string, data map[string]any) error {
		if eventType == "hook.failed" {
			failed = data
		}
		return nil
	})

	if _, err := manager.Trigger(context.Background(), "user.message", map[string]any{"text": "hello"}); err == nil {
		t.Fatal("expected missing fail-closed hook to block")
	}
	if failed == nil || failed["error"] == "" {
		t.Fatalf("expected hook.failed event, got %#v", failed)
	}
}

func TestManagerReturnsHookCommandEmitterError(t *testing.T) {
	manager := New(config.HooksConfig{
		UserMessage: []config.HookDefinition{
			{
				Name:    "command-event-fails",
				Command: []string{"/bin/sh", "-c", "exit 0"},
			},
		},
	}, t.TempDir())

	manager.SetEmitter(func(eventType string, data map[string]any) error {
		if eventType == "hook.command" {
			return errors.New("events closed")
		}
		return nil
	})

	if _, err := manager.Trigger(context.Background(), "user.message", map[string]any{"text": "hello"}); err == nil || !strings.Contains(err.Error(), "hook.command") || !strings.Contains(err.Error(), "events closed") {
		t.Fatalf("expected hook.command emitter error, got %v", err)
	}
}

func TestManagerReturnsHookFinishedEmitterError(t *testing.T) {
	manager := New(config.HooksConfig{
		UserMessage: []config.HookDefinition{
			{
				Name: "finished-event-fails",
				Inject: &config.HookInject{
					Field:  "text",
					Prefix: "updated ",
				},
			},
		},
	}, t.TempDir())

	manager.SetEmitter(func(eventType string, data map[string]any) error {
		if eventType == "hook.finished" {
			return errors.New("events closed")
		}
		return nil
	})

	if _, err := manager.Trigger(context.Background(), "user.message", map[string]any{"text": "hello"}); err == nil || !strings.Contains(err.Error(), "hook.finished") || !strings.Contains(err.Error(), "events closed") {
		t.Fatalf("expected hook.finished emitter error, got %v", err)
	}
}

func TestManagerReturnsHookFailedEmitterError(t *testing.T) {
	manager := New(config.HooksConfig{
		UserMessage: []config.HookDefinition{
			{
				Name: "failed-event-fails",
				Filter: &config.HookFilter{
					Field:            "text",
					RejectIfContains: "reject",
				},
			},
		},
	}, t.TempDir())

	manager.SetEmitter(func(eventType string, data map[string]any) error {
		if eventType == "hook.failed" {
			return errors.New("events closed")
		}
		return nil
	})

	if _, err := manager.Trigger(context.Background(), "user.message", map[string]any{"text": "reject"}); err == nil || !strings.Contains(err.Error(), "hook.failed") || !strings.Contains(err.Error(), "hook rejected payload") || !strings.Contains(err.Error(), "events closed") {
		t.Fatalf("expected hook.failed emitter error with original context, got %v", err)
	}
}

func TestManagerReturnsHookWarningEmitterError(t *testing.T) {
	manager := New(config.HooksConfig{
		UserMessage: []config.HookDefinition{
			{
				Name:    "warning-event-fails",
				Command: []string{"definitely-missing-hook-command-for-preflight"},
			},
		},
	}, t.TempDir())

	manager.SetEmitter(func(eventType string, data map[string]any) error {
		if eventType == "hook.warning" {
			return errors.New("events closed")
		}
		return nil
	})

	if _, err := manager.Trigger(context.Background(), "user.message", map[string]any{"text": "hello"}); err == nil || !strings.Contains(err.Error(), "hook.warning") || !strings.Contains(err.Error(), "events closed") {
		t.Fatalf("expected hook.warning emitter error, got %v", err)
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
	manager.SetEmitter(func(eventType string, data map[string]any) error {
		if eventType == "hook.warning" {
			warning = data
		}
		return nil
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
