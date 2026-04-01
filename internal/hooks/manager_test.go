package hooks

import (
	"context"
	"testing"

	"go-cli-agent/internal/config"
)

func TestManagerInjectAndRedact(t *testing.T) {
	manager := New(config.HooksConfig{
		AssistantMessage: []config.HookDefinition{
			{
				Name: "prefix",
				Inject: &config.HookInject{
					Field:  "text",
					Prefix: "[interactive] ",
				},
			},
			{
				Name: "redact",
				Filter: &config.HookFilter{
					Field:  "text",
					Redact: []string{"secret"},
				},
			},
		},
	}, t.TempDir())

	payload, err := manager.Trigger(context.Background(), "assistant.message", map[string]any{
		"text": "secret value",
	})
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}
	if payload["text"] != "[interactive] *** value" {
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
