package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/events"
	"go-cli-agent/internal/hooks"
	"go-cli-agent/internal/provider"
	"go-cli-agent/internal/session"
	"go-cli-agent/internal/skills"
	"go-cli-agent/internal/tools"
)

func TestEngineRunModeStopsAtAwaitingInput(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "hello")); err != nil {
		t.Fatalf("append: %v", err)
	}
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{Text: "done_candidate", StopReason: "done_candidate"}, nil
	})
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusAwaitingInput {
		t.Fatalf("expected awaiting_input, got %s", result.Status)
	}
}

func TestEngineAwaitingInputReportsEventAppendError(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "hello")); err != nil {
		t.Fatalf("append: %v", err)
	}
	eventsPath := filepath.Join(engine.store.SessionDir(meta.ID), "events.jsonl")
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		if err := os.Remove(eventsPath); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove events: %v", err)
		}
		if err := os.Mkdir(eventsPath, 0o700); err != nil {
			t.Fatalf("block events path: %v", err)
		}
		return provider.TurnResult{Text: "done_candidate", StopReason: "done_candidate"}, nil
	})

	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err == nil {
		t.Fatalf("expected session.awaiting_input event append error, got result=%#v", result)
	}
	if !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected events append error with path context, got %v", err)
	}
	if !strings.Contains(err.Error(), "session.awaiting_input") {
		t.Fatalf("expected awaiting_input event context, got %v", err)
	}
}

func TestEnginePreservesLoadedSkillStateAcrossNextTurn(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngineWithSkill(t, session.ModeRun, "helpers", "helper skill", "FULL SKILL BODY")
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "load helper")); err != nil {
		t.Fatalf("append: %v", err)
	}
	turns := 0
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		turns++
		if turns == 1 {
			return provider.TurnResult{
				ToolCalls: []provider.ToolCall{{
					ID:        "call_load_skill",
					Name:      "load_skill",
					Arguments: json.RawMessage(`{"name":"helpers"}`),
				}},
				StopReason: "tool_use",
			}, nil
		}
		return provider.TurnResult{Text: "ready", StopReason: "done_candidate"}, nil
	})
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusAwaitingInput {
		t.Fatalf("expected awaiting_input, got %#v", result)
	}
	loaded, err := engine.store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if fmt.Sprint(loaded.LoadedSkills) != "[helpers]" {
		t.Fatalf("expected loaded skill state to survive next turn, got %#v", loaded.LoadedSkills)
	}
}

func TestEnginePersistsProviderTurnMetadata(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "hello")); err != nil {
		t.Fatalf("append: %v", err)
	}
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{
			Text:               "done_candidate",
			StopReason:         "done_candidate",
			ProviderResponseID: "resp_test_1",
			RawProvider: map[string]any{
				"provider_stop_reason": "completed",
				"status":               "completed",
			},
			Usage: provider.Usage{
				InputTokens:              12,
				OutputTokens:             4,
				CacheCreationInputTokens: 5,
				CacheReadInputTokens:     9,
			},
		}, nil
	})
	if _, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager); err != nil {
		t.Fatalf("run: %v", err)
	}
	messages, err := engine.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(messages) == 0 || messages[len(messages)-1].Role != "assistant" {
		t.Fatalf("expected assistant message, got %#v", messages)
	}
	if messages[len(messages)-1].Meta["provider_response_id"] != "resp_test_1" {
		t.Fatalf("expected provider response id in assistant metadata, got %#v", messages[len(messages)-1].Meta)
	}
	events, err := loadEvents(engine.store, meta.ID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if !hasEventType(events, "turn.stopped") {
		t.Fatalf("expected turn.stopped event, got %#v", events)
	}
	stopped, ok := findEventByType(events, "turn.stopped")
	if !ok {
		t.Fatalf("expected turn.stopped event, got %#v", events)
	}
	usage, ok := stopped.Data["usage"].(map[string]any)
	if !ok {
		t.Fatalf("expected usage data on turn.stopped, got %#v", stopped.Data)
	}
	if usage["cache_creation_input_tokens"] != float64(5) || usage["cache_read_input_tokens"] != float64(9) {
		t.Fatalf("expected cache usage counters in turn.stopped, got %#v", usage)
	}
	attempts, err := engine.store.LoadProviderAttempts(meta.ID)
	if err != nil {
		t.Fatalf("provider attempts: %v", err)
	}
	if len(attempts) != 1 || attempts[0].CacheCreationInputTokens != 5 || attempts[0].CacheReadInputTokens != 9 {
		t.Fatalf("expected cache counters in provider attempts, got %#v", attempts)
	}
	summary, err := os.ReadFile(filepath.Join(engine.store.SessionDir(meta.ID), "session.md"))
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if !strings.Contains(string(summary), "cache usage: read=`9` creation=`5` hit_attempts=`1`") {
		t.Fatalf("expected cache usage summary, got:\n%s", string(summary))
	}
}

func TestEngineProviderParseErrorFailsBeforeAssistantPersist(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "hello")); err != nil {
		t.Fatalf("append: %v", err)
	}
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{}, &provider.HTTPError{
			Provider: "openai",
			Class:    "response_parse_error",
			Message:  "function_call arguments for \"shell\" are not valid JSON",
		}
	})
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err == nil {
		t.Fatal("expected provider parse failure")
	}
	if result.Status != session.StatusFailed {
		t.Fatalf("expected failed session, got %#v", result)
	}
	messages, err := engine.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	for _, msg := range messages {
		if msg.Role == "assistant" {
			t.Fatalf("provider parse failure should not persist assistant message: %#v", messages)
		}
	}
	attempts, err := engine.store.LoadProviderAttempts(meta.ID)
	if err != nil {
		t.Fatalf("provider attempts: %v", err)
	}
	if len(attempts) != 1 || attempts[0].Outcome != "failure" || attempts[0].ErrorClass != "response_parse_error" {
		t.Fatalf("expected parse error provider attempt, got %#v", attempts)
	}
}

func TestEngineProviderRetryReportsProviderAttemptAppendError(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "hello")); err != nil {
		t.Fatalf("append: %v", err)
	}
	blockRuntimeProviderAttemptsPath(t, engine.store, meta.ID)
	adapter := emittingAdapter{
		run: func(ctx context.Context, _ provider.TurnRequest, emit provider.EmitFunc) (provider.TurnResult, error) {
			emit("provider.retry", map[string]any{
				"attempt":      1,
				"delay_ms":     1,
				"error":        "temporary timeout",
				"class":        "upstream_timeout",
				"timeout_kind": "request_timeout",
			})
			select {
			case <-ctx.Done():
				return provider.TurnResult{}, ctx.Err()
			default:
			}
			return provider.TurnResult{Text: "should not persist", StopReason: "done_candidate"}, nil
		},
	}

	result, err := engine.Run(context.Background(), meta, state, "", adapter, catalog, registry, hookManager)
	if err == nil || !strings.Contains(err.Error(), "provider-attempts.jsonl") {
		t.Fatalf("expected provider-attempt append error, result=%#v err=%v", result, err)
	}
	if result.Status != session.StatusFailed {
		t.Fatalf("expected failed result after provider-attempt append error, got %#v", result)
	}
	messages, err := engine.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	for _, msg := range messages {
		if msg.Role == "assistant" {
			t.Fatalf("provider retry ledger failure should stop before assistant persistence: %#v", messages)
		}
	}
}

func TestEngineProviderRetryReportsEventAppendError(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "hello")); err != nil {
		t.Fatalf("append: %v", err)
	}
	eventsPath := filepath.Join(engine.store.SessionDir(meta.ID), "events.jsonl")
	adapter := emittingAdapter{
		run: func(_ context.Context, _ provider.TurnRequest, emit provider.EmitFunc) (provider.TurnResult, error) {
			if err := os.Remove(eventsPath); err != nil && !os.IsNotExist(err) {
				t.Fatalf("remove events: %v", err)
			}
			if err := os.Mkdir(eventsPath, 0o700); err != nil {
				t.Fatalf("block events path: %v", err)
			}
			emit("provider.retry", map[string]any{
				"attempt":      1,
				"delay_ms":     1,
				"error":        "temporary timeout",
				"class":        "upstream_timeout",
				"timeout_kind": "request_timeout",
			})
			return provider.TurnResult{Text: "should not persist", StopReason: "done_candidate"}, nil
		},
	}

	result, err := engine.Run(context.Background(), meta, state, "", adapter, catalog, registry, hookManager)
	if err == nil {
		t.Fatalf("expected provider.retry event append error, got result=%#v", result)
	}
	if !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected events append error with path context, got %v", err)
	}
	messages, err := engine.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	for _, msg := range messages {
		if msg.Role == "assistant" {
			t.Fatalf("provider retry event failure should stop before assistant persistence: %#v", messages)
		}
	}
}

func TestEngineProviderFailureReportsProviderAttemptAppendError(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "hello")); err != nil {
		t.Fatalf("append: %v", err)
	}
	blockRuntimeProviderAttemptsPath(t, engine.store, meta.ID)
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{}, &provider.HTTPError{
			Provider: "openai",
			Class:    "response_parse_error",
			Message:  "function_call arguments for \"shell\" are not valid JSON",
		}
	})

	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err == nil || !strings.Contains(err.Error(), "provider-attempts.jsonl") {
		t.Fatalf("expected provider-attempt append error, result=%#v err=%v", result, err)
	}
	if result.Status != session.StatusFailed {
		t.Fatalf("expected failed result after provider-attempt append error, got %#v", result)
	}
}

func TestEngineProviderFailureReportsStateSaveError(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "hello")); err != nil {
		t.Fatalf("append: %v", err)
	}
	statePath := filepath.Join(engine.store.SessionDir(meta.ID), "state.json")
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		if err := os.Remove(statePath); err != nil {
			t.Fatalf("remove state: %v", err)
		}
		if err := os.Mkdir(statePath, 0o700); err != nil {
			t.Fatalf("block state path: %v", err)
		}
		return provider.TurnResult{}, errors.New("upstream failed")
	})

	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err == nil {
		t.Fatalf("expected state save error, got result=%#v", result)
	}
	if !strings.Contains(err.Error(), "state.json") {
		t.Fatalf("expected state write error with path context, got %v", err)
	}
}

func TestEngineProviderFailureReportsFailedEventAppendError(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "hello")); err != nil {
		t.Fatalf("append: %v", err)
	}
	eventsPath := filepath.Join(engine.store.SessionDir(meta.ID), "events.jsonl")
	if err := os.Remove(eventsPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove events: %v", err)
	}
	if err := os.Mkdir(eventsPath, 0o700); err != nil {
		t.Fatalf("block events path: %v", err)
	}
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{}, errors.New("upstream failed")
	})

	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err == nil {
		t.Fatalf("expected session.failed event append error, got result=%#v", result)
	}
	if !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected events append error with path context, got %v", err)
	}
	if !strings.Contains(err.Error(), "upstream failed") {
		t.Fatalf("expected original provider failure context, got %v", err)
	}
}

func TestEngineProviderCancellationReportsCancelledEventAppendError(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "initial")); err != nil {
		t.Fatalf("append: %v", err)
	}
	eventsPath := filepath.Join(engine.store.SessionDir(meta.ID), "events.jsonl")
	firstTurnStarted := make(chan struct{})
	fake := provider.NewFake(func(ctx context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
		if got := req.Messages[len(req.Messages)-1].Text; got != "initial" {
			t.Fatalf("expected initial prompt on first turn, got %q", got)
		}
		close(firstTurnStarted)
		<-ctx.Done()
		return provider.TurnResult{}, ctx.Err()
	})

	go func() {
		<-firstTurnStarted
		if err := engine.store.AppendSteerRequest(meta.ID, session.NewSteerRequest("switch direction", true)); err != nil {
			t.Errorf("append steer: %v", err)
			return
		}
		if err := os.Remove(eventsPath); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove events: %v", err)
			return
		}
		if err := os.Mkdir(eventsPath, 0o700); err != nil {
			t.Errorf("block events path: %v", err)
			return
		}
		engine.control.requestSteerInterrupt()
	}()

	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err == nil {
		t.Fatalf("expected provider.cancelled event append error, got result=%#v", result)
	}
	if !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected events append error with path context, got %v", err)
	}
}

func TestEngineFailReportsFailedEventAppendError(t *testing.T) {
	cfg := config.Default()
	cfg.Hooks.SessionStart = []config.HookDefinition{{
		Name:       "fail-start",
		FailClosed: true,
		Command:    []string{"/bin/sh", "-c", "exit 1"},
	}}
	engine, meta, state, registry, hookManager, catalog := newTestEngineWithConfig(t, cfg, session.ModeRun)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "hello")); err != nil {
		t.Fatalf("append: %v", err)
	}
	eventsPath := filepath.Join(engine.store.SessionDir(meta.ID), "events.jsonl")
	if err := os.Remove(eventsPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove events: %v", err)
	}
	if err := os.Mkdir(eventsPath, 0o700); err != nil {
		t.Fatalf("block events path: %v", err)
	}
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		t.Fatal("provider should not be called after fail-closed session start hook")
		return provider.TurnResult{}, nil
	})

	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err == nil {
		t.Fatalf("expected session.failed event append error, got result=%#v", result)
	}
	if !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected events append error with path context, got %v", err)
	}
	if !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("expected original failure context, got %v", err)
	}
}

func TestEngineProviderAutoResumeReportsProviderAttemptAppendError(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	engine.cfg.Runtime.ProviderAutoResume.Enabled = true
	engine.cfg.Runtime.ProviderAutoResume.MaxAttempts = 2
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "Return a finish tool call.")); err != nil {
		t.Fatalf("append: %v", err)
	}
	blockRuntimeProviderAttemptsPath(t, engine.store, meta.ID)
	callCount := 0
	fake := provider.NewFake(
		func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
			callCount++
			return provider.TurnResult{}, &provider.HTTPError{
				Provider:    "fake",
				Class:       "upstream_timeout",
				Message:     "context deadline exceeded",
				TimeoutKind: "request_timeout",
			}
		},
		func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
			callCount++
			t.Fatalf("provider should not be recalled after provider-attempt append failure")
			return provider.TurnResult{}, nil
		},
	)

	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err == nil || !strings.Contains(err.Error(), "provider-attempts.jsonl") {
		t.Fatalf("expected provider-attempt append error, result=%#v err=%v", result, err)
	}
	if result.Status != session.StatusFailed {
		t.Fatalf("expected failed result after provider-attempt append error, got %#v", result)
	}
	if callCount != 1 {
		t.Fatalf("expected provider to stop after first failed call, got %d calls", callCount)
	}
}

func TestEngineProviderAutoResumeReportsEventAppendError(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	engine.cfg.Runtime.ProviderAutoResume.Enabled = true
	engine.cfg.Runtime.ProviderAutoResume.MaxAttempts = 2
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "Return a finish tool call.")); err != nil {
		t.Fatalf("append: %v", err)
	}
	eventsPath := filepath.Join(engine.store.SessionDir(meta.ID), "events.jsonl")
	callCount := 0
	fake := provider.NewFake(
		func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
			callCount++
			if err := os.Remove(eventsPath); err != nil && !os.IsNotExist(err) {
				t.Fatalf("remove events: %v", err)
			}
			if err := os.Mkdir(eventsPath, 0o700); err != nil {
				t.Fatalf("block events path: %v", err)
			}
			return provider.TurnResult{}, &provider.HTTPError{
				Provider:    "fake",
				Class:       "upstream_timeout",
				Message:     "context deadline exceeded",
				TimeoutKind: "request_timeout",
			}
		},
		func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
			callCount++
			return provider.TurnResult{
				ToolCalls:  []provider.ToolCall{{ID: "call_1", Name: "finish", Arguments: json.RawMessage(`{"message":"done"}`)}},
				StopReason: "tool_use",
			}, nil
		},
	)

	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err == nil {
		t.Fatalf("expected provider.auto_resume event append error, got result=%#v", result)
	}
	if !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected events append error with path context, got %v", err)
	}
	if callCount != 1 {
		t.Fatalf("provider should not be recalled after provider.auto_resume append failure, got %d calls", callCount)
	}
}

func TestEngineProviderSuccessReportsProviderAttemptAppendError(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "hello")); err != nil {
		t.Fatalf("append: %v", err)
	}
	blockRuntimeProviderAttemptsPath(t, engine.store, meta.ID)
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{Text: "should not persist", StopReason: "done_candidate"}, nil
	})

	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err == nil || !strings.Contains(err.Error(), "provider-attempts.jsonl") {
		t.Fatalf("expected provider-attempt append error, result=%#v err=%v", result, err)
	}
	if result.Status != session.StatusFailed {
		t.Fatalf("expected failed result after provider-attempt append error, got %#v", result)
	}
	messages, err := engine.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	for _, msg := range messages {
		if msg.Role == "assistant" {
			t.Fatalf("provider success ledger failure should stop before assistant persistence: %#v", messages)
		}
	}
}

func TestEngineGoalAccountingReportsEventAppendError(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	if _, err := engine.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Mode:      session.GoalModeGoal,
		Objective: "Track accounting events.",
		Source:    session.GoalSourceCLI,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "hello")); err != nil {
		t.Fatalf("append: %v", err)
	}
	eventsPath := filepath.Join(engine.store.SessionDir(meta.ID), "events.jsonl")
	adapter := emittingAdapter{
		run: func(_ context.Context, _ provider.TurnRequest, _ provider.EmitFunc) (provider.TurnResult, error) {
			if err := os.Remove(eventsPath); err != nil && !os.IsNotExist(err) {
				t.Fatalf("remove events: %v", err)
			}
			if err := os.Mkdir(eventsPath, 0o700); err != nil {
				t.Fatalf("block events path: %v", err)
			}
			return provider.TurnResult{
				Text:       "accounting should fail before this persists",
				StopReason: "done_candidate",
				Usage:      provider.Usage{InputTokens: 3, OutputTokens: 2},
			}, nil
		},
	}

	result, err := engine.Run(context.Background(), meta, state, "", adapter, catalog, registry, hookManager)
	if err == nil {
		t.Fatalf("expected goal.accounting.updated event append error, got result=%#v", result)
	}
	if !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected events append error with path context, got %v", err)
	}
	if !strings.Contains(err.Error(), "goal.accounting.updated") {
		t.Fatalf("expected accounting event context, got %v", err)
	}
	messages, err := engine.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	for _, msg := range messages {
		if msg.Role == "assistant" {
			t.Fatalf("goal accounting event failure should stop before assistant persistence: %#v", messages)
		}
	}
}

func TestEngineWritesReplayCompleteToolResultsWhenBeforeHookFails(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.GuardrailsMode = "standard"
	cfg.Hooks.ToolBefore = []config.HookDefinition{{
		Name:       "fail-before",
		Command:    []string{"/bin/sh", "-c", "exit 7"},
		FailClosed: true,
	}}
	engine, meta, state, registry, hookManager, catalog := newTestEngineWithConfig(t, cfg, session.ModeExec)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "hello")); err != nil {
		t.Fatalf("append: %v", err)
	}
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{
			ToolCalls: []provider.ToolCall{
				{ID: "call_1", Name: "shell", Arguments: json.RawMessage(`{"command":"true"}`)},
				{ID: "call_2", Name: "finish", Arguments: json.RawMessage(`{"message":"done"}`)},
			},
			StopReason: "tool_use",
		}, nil
	})
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err == nil {
		t.Fatal("expected hook failure")
	}
	if result.Status != session.StatusFailed {
		t.Fatalf("expected failed result, got %#v", result)
	}
	messages, err := engine.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	last := messages[len(messages)-1]
	if last.Role != "tool" || len(last.ToolResults) != 2 {
		t.Fatalf("expected replay-complete synthetic tool results, got %#v", last)
	}
	for _, toolResult := range last.ToolResults {
		if !toolResult.IsError || !strings.Contains(toolResult.LLMOutput, "tool.before hook failed") {
			t.Fatalf("expected hook failure tool result, got %#v", toolResult)
		}
	}
}

func TestEngineWritesSyntheticToolResultsAfterFinishInSameTurn(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "finish then ignore later tools")); err != nil {
		t.Fatalf("append: %v", err)
	}
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{
			ToolCalls: []provider.ToolCall{
				{ID: "call_finish", Name: "finish", Arguments: json.RawMessage(`{"message":"done"}`)},
				{ID: "call_later", Name: "shell", Arguments: json.RawMessage(`{"command":"touch should-not-run"}`)},
			},
			StopReason: "tool_use",
		}, nil
	})
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("expected completed result, got %#v", result)
	}
	if _, err := os.Stat(filepath.Join(meta.Workdir, "should-not-run")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("later shell tool should not execute after finish, stat err=%v", err)
	}
	messages, err := engine.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	var toolMessages []session.Message
	for _, msg := range messages {
		if msg.Role == "tool" {
			toolMessages = append(toolMessages, msg)
		}
	}
	if len(toolMessages) != 1 {
		t.Fatalf("expected one replay-complete tool message, got %#v", toolMessages)
	}
	results := toolMessages[0].ToolResults
	if len(results) != 2 {
		t.Fatalf("expected finish result plus synthetic later result, got %#v", results)
	}
	if results[0].ToolCallID != "call_finish" || !results[0].Final || results[0].DisplayOutput != "done" {
		t.Fatalf("unexpected finish result: %#v", results[0])
	}
	if results[1].ToolCallID != "call_later" || !results[1].IsError || !strings.Contains(results[1].LLMOutput, "finish completed the session") {
		t.Fatalf("unexpected synthetic later result: %#v", results[1])
	}
}

func TestEngineProviderStopReasonFailuresAreResumable(t *testing.T) {
	for _, stopReason := range []string{"max_tokens", "blocked", "error"} {
		t.Run(stopReason, func(t *testing.T) {
			engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
			if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "hello")); err != nil {
				t.Fatalf("append: %v", err)
			}
			fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
				return provider.TurnResult{Text: "partial", StopReason: stopReason}, nil
			})
			result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if result.Status != session.StatusFailed {
				t.Fatalf("expected failed status for %s, got %#v", stopReason, result)
			}
			loaded, err := engine.store.LoadState(meta.ID)
			if err != nil {
				t.Fatalf("load state: %v", err)
			}
			if loaded.IncompleteReason == "" || !strings.Contains(loaded.IncompleteReason, "provider") {
				t.Fatalf("expected provider incomplete reason, got %#v", loaded)
			}
		})
	}
}

func TestEngineProviderStopReasonReportsFailedEventAppendError(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "hello")); err != nil {
		t.Fatalf("append: %v", err)
	}
	eventsPath := filepath.Join(engine.store.SessionDir(meta.ID), "events.jsonl")
	if err := os.Remove(eventsPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove events: %v", err)
	}
	if err := os.Mkdir(eventsPath, 0o700); err != nil {
		t.Fatalf("block events path: %v", err)
	}
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{Text: "partial", StopReason: "max_tokens"}, nil
	})

	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err == nil {
		t.Fatalf("expected session.failed event append error, got result=%#v", result)
	}
	if !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected events append error with path context, got %v", err)
	}
	if !strings.Contains(err.Error(), "provider_max_tokens") {
		t.Fatalf("expected provider stop failure context, got %v", err)
	}
}

func TestEnginePersistsOpenAIReasoningOnlyProviderBlockWhenReplayValid(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "hello")); err != nil {
		t.Fatalf("append: %v", err)
	}
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{
			ProviderContentBlocks: []session.ProviderContentBlock{
				{Provider: "openai", Type: "reasoning", ID: "rs_1", Data: "enc_opaque", Summary: []string{"summary"}, Sequence: 1, Model: "gpt-5.4"},
			},
			StopReason: "done_candidate",
			RawProvider: map[string]any{
				"reasoning_encrypted_count": 1,
				"thinking_replay_observed":  true,
			},
		}, nil
	})
	if _, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager); err != nil {
		t.Fatalf("run: %v", err)
	}
	messages, err := engine.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	var assistant *session.Message
	for i := range messages {
		if messages[i].Role == "assistant" {
			assistant = &messages[i]
		}
	}
	if assistant == nil || len(assistant.ProviderContentBlocks) != 1 {
		t.Fatalf("expected assistant reasoning block to persist, got %#v", messages)
	}
	if assistant.Thinking != "" || assistant.ProviderContentBlocks[0].Data != "enc_opaque" {
		t.Fatalf("expected encrypted-only block without visible thinking, got %#v", assistant)
	}
}

func TestProviderRawSidecarDisabledByDefault(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "hello")); err != nil {
		t.Fatalf("append: %v", err)
	}
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{
			Text:               "done_candidate",
			StopReason:         "done_candidate",
			ProviderResponseID: "resp_test_1",
			RawProvider: map[string]any{
				"provider_stop_reason": "completed",
				"status":               "completed",
			},
		}, nil
	})
	if _, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := os.Stat(engine.store.ProviderRawSidecarPath(meta.ID, 1)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected no provider raw sidecar by default, stat err=%v", err)
	}
}

func TestProviderRawSidecarWritesEnvelopeWhenEnabled(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	enabled := true
	meta.ProviderOptions.RawSidecar = &enabled
	if err := engine.store.SaveMetadata(meta.ID, meta); err != nil {
		t.Fatalf("save metadata: %v", err)
	}
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "hello")); err != nil {
		t.Fatalf("append: %v", err)
	}
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{
			Text:               "done_candidate",
			StopReason:         "done_candidate",
			ProviderResponseID: "resp_test_1",
			RawProvider: map[string]any{
				"provider_stop_reason": "completed",
				"status":               "completed",
			},
		}, nil
	})
	if _, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager); err != nil {
		t.Fatalf("run: %v", err)
	}
	sidecar, err := engine.store.LoadProviderRawSidecar(meta.ID, 1)
	if err != nil {
		t.Fatalf("load sidecar: %v", err)
	}
	if sidecar.SchemaVersion != 1 || sidecar.Provider != meta.Provider || sidecar.Model != meta.Model || sidecar.Turn != 1 {
		t.Fatalf("unexpected sidecar envelope: %#v", sidecar)
	}
	if sidecar.ProviderResponseID != "resp_test_1" || sidecar.StopReason != "done_candidate" {
		t.Fatalf("unexpected sidecar provider metadata: %#v", sidecar)
	}
	if sidecar.SelectedRawItems["status"] != "completed" || sidecar.SelectedRawItems["provider_stop_reason"] != "completed" {
		t.Fatalf("unexpected selected raw items: %#v", sidecar.SelectedRawItems)
	}
	if strings.TrimSpace(sidecar.Timestamp) == "" {
		t.Fatalf("expected timestamp in sidecar: %#v", sidecar)
	}
}

func writeEvidenceFile(t *testing.T, workdir, rel, content string) {
	t.Helper()
	path := filepath.Join(workdir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir evidence dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write evidence file: %v", err)
	}
}

func TestEnginePassesSessionMetadataIntoProviderRequest(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	meta.RootSessionID = "root-1"
	meta.ParentSessionID = "parent-1"
	meta.AgentName = "reviewer"
	meta.AgentRole = "evaluator"
	meta.QueueJobID = "job-1"
	meta.Depth = 2
	meta.Isolation = &session.IsolationInfo{Mode: "copy"}
	if err := engine.store.SaveMetadata(meta.ID, meta); err != nil {
		t.Fatalf("save metadata: %v", err)
	}
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "hello")); err != nil {
		t.Fatalf("append: %v", err)
	}
	fake := provider.NewFake(func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
		if req.Metadata["session_id"] != meta.ID {
			t.Fatalf("expected session metadata in provider request, got %#v", req.Metadata)
		}
		if req.Metadata["root_session_id"] != "root-1" || req.Metadata["parent_session_id"] != "parent-1" {
			t.Fatalf("expected root/parent metadata, got %#v", req.Metadata)
		}
		if req.Metadata["agent_name"] != "reviewer" || req.Metadata["agent_role"] != "evaluator" || req.Metadata["queue_job_id"] != "job-1" {
			t.Fatalf("expected agent/job metadata, got %#v", req.Metadata)
		}
		if req.Metadata["depth"] != 2 || req.Metadata["isolation_mode"] != "copy" {
			t.Fatalf("expected depth/isolation metadata, got %#v", req.Metadata)
		}
		return provider.TurnResult{
			ToolCalls:  []provider.ToolCall{{ID: "call_1", Name: "finish", Arguments: json.RawMessage(`{"message":"done"}`)}},
			StopReason: "tool_use",
		}, nil
	})
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("expected completed, got %s", result.Status)
	}
}

func TestEngineBudgetWrapUpThenFinishAwaitsInput(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	tokenBudget := int64(1)
	if _, err := engine.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:      true,
		Mode:         session.GoalModeGoal,
		Objective:    "Stop when budget is exhausted.",
		TokenBudget:  &tokenBudget,
		StopOnBudget: true,
		Source:       session.GoalSourceCLI,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	if _, limited, err := engine.store.UpdateGoalAccounting(meta.ID, session.GoalUsageDelta{TokensUsedDelta: 2, SourceTurn: 1}); err != nil || !limited {
		t.Fatalf("expected budget limit, limited=%v err=%v", limited, err)
	}
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "Record budget wrap-up, then stop.")); err != nil {
		t.Fatalf("append user: %v", err)
	}
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{
			ToolCalls: []provider.ToolCall{
				{
					ID:   "call_budget_wrapup",
					Name: "record_goal_progress",
					Arguments: json.RawMessage(`{
						"kind":"budget_wrapup",
						"summary":"Budget exhausted before completion.",
						"evidence":["runtime test"],
						"blockers":["needs more budget"]
					}`),
				},
				{
					ID:        "call_finish_after_wrapup",
					Name:      "finish",
					Arguments: json.RawMessage(`{"message":"budget wrap-up recorded"}`),
				},
			},
			StopReason: "tool_use",
		}, nil
	})
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusAwaitingInput {
		t.Fatalf("expected awaiting_input after budget wrap-up, got %#v", result)
	}
	loaded, err := engine.store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if loaded.Status != session.StatusAwaitingInput || loaded.Phase != "goal_budget_limited" {
		t.Fatalf("expected goal_budget_limited awaiting input, got %#v", loaded)
	}
	messages, err := engine.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	var blockedFinish bool
	for _, msg := range messages {
		for _, toolResult := range msg.ToolResults {
			if toolResult.Name == "finish" && toolResult.IsError && toolResult.Metadata["guard"] == "goal_budget_limited" {
				blockedFinish = true
			}
		}
	}
	if !blockedFinish {
		t.Fatalf("expected finish to be blocked by goal_budget_limited guard, got %#v", messages)
	}
}

func TestEngineBudgetWrapUpAwaitsReportsCorruptGoalSnapshot(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.GuardrailsMode = "standard"
	engine, meta, state, registry, hookManager, catalog := newTestEngineWithConfig(t, cfg, session.ModeExec)
	tokenBudget := int64(1)
	if _, err := engine.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:      true,
		Mode:         session.GoalModeGoal,
		Objective:    "Stop when budget is exhausted.",
		TokenBudget:  &tokenBudget,
		StopOnBudget: true,
		Source:       session.GoalSourceCLI,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	if _, limited, err := engine.store.UpdateGoalAccounting(meta.ID, session.GoalUsageDelta{TokensUsedDelta: 2, SourceTurn: 1}); err != nil || !limited {
		t.Fatalf("expected budget limit, limited=%v err=%v", limited, err)
	}
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "Record budget wrap-up, then stop.")); err != nil {
		t.Fatalf("append user: %v", err)
	}
	escapedSessionRoot := strings.ReplaceAll(cfg.Session.Dir, "'", "'\"'\"'")
	cfg.Hooks.ToolAfter = []config.HookDefinition{
		{
			Name:    "corrupt-goal-after-wrapup",
			Match:   config.HookMatch{Tool: "record_goal_progress"},
			Command: []string{"/bin/sh", "-c", "printf '{' > '" + escapedSessionRoot + "'/\"$SESSION_ID\"/goal.json"},
		},
	}
	hookManager = hooks.New(cfg.Hooks, meta.Workdir)
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{
			ToolCalls: []provider.ToolCall{
				{
					ID:   "call_budget_wrapup",
					Name: "record_goal_progress",
					Arguments: json.RawMessage(`{
						"kind":"budget_wrapup",
						"summary":"Budget exhausted before completion.",
						"evidence":["runtime test"],
						"blockers":["needs more budget"]
					}`),
				},
			},
			StopReason: "tool_use",
		}, nil
	})

	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err == nil || !strings.Contains(err.Error(), "goal.json") {
		t.Fatalf("expected corrupt goal snapshot error, result=%#v err=%v", result, err)
	}
	loaded, loadErr := engine.store.LoadState(meta.ID)
	if loadErr != nil {
		t.Fatalf("load state: %v", loadErr)
	}
	if loaded.Status == session.StatusAwaitingInput && loaded.Phase == "goal_budget_limited" {
		t.Fatalf("corrupt goal snapshot should not transition to budget awaiting input, got %#v", loaded)
	}
}

func TestEngineBudgetWrapUpAwaitingReportsEventAppendError(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.GuardrailsMode = "standard"
	engine, meta, state, registry, hookManager, catalog := newTestEngineWithConfig(t, cfg, session.ModeExec)
	tokenBudget := int64(1)
	if _, err := engine.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:      true,
		Mode:         session.GoalModeGoal,
		Objective:    "Stop when budget is exhausted.",
		TokenBudget:  &tokenBudget,
		StopOnBudget: true,
		Source:       session.GoalSourceCLI,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	if _, limited, err := engine.store.UpdateGoalAccounting(meta.ID, session.GoalUsageDelta{TokensUsedDelta: 2, SourceTurn: 1}); err != nil || !limited {
		t.Fatalf("expected budget limit, limited=%v err=%v", limited, err)
	}
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "Record budget wrap-up, then stop.")); err != nil {
		t.Fatalf("append user: %v", err)
	}
	eventsPath := filepath.Join(engine.store.SessionDir(meta.ID), "events.jsonl")
	cfg.Hooks.ToolAfter = []config.HookDefinition{
		{
			Name:    "block-events-after-wrapup",
			Match:   config.HookMatch{Tool: "record_goal_progress"},
			Command: []string{"/bin/sh", "-c", "rm -f \"$1\" && mkdir \"$1\"", "block-events-after-wrapup", eventsPath},
		},
	}
	hookManager = hooks.New(cfg.Hooks, meta.Workdir)
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{
			ToolCalls: []provider.ToolCall{
				{
					ID:   "call_budget_wrapup",
					Name: "record_goal_progress",
					Arguments: json.RawMessage(`{
						"kind":"budget_wrapup",
						"summary":"Budget exhausted before completion.",
						"evidence":["runtime test"],
						"blockers":["needs more budget"]
					}`),
				},
			},
			StopReason: "tool_use",
		}, nil
	})

	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err == nil {
		t.Fatalf("expected session.awaiting_input event append error, got result=%#v", result)
	}
	if !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected events append error with path context, got %v", err)
	}
	if !strings.Contains(err.Error(), "goal_budget_limited") || !strings.Contains(err.Error(), "session.awaiting_input") {
		t.Fatalf("expected budget awaiting-input event context, got %v", err)
	}
}

func TestEngineBudgetWrapUpTurnStartReportsGoalHistoryError(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	tokenBudget := int64(1)
	if _, err := engine.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:      true,
		Mode:         session.GoalModeGoal,
		Objective:    "Stop when budget is exhausted.",
		TokenBudget:  &tokenBudget,
		StopOnBudget: true,
		Source:       session.GoalSourceCLI,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	if _, limited, err := engine.store.UpdateGoalAccounting(meta.ID, session.GoalUsageDelta{TokensUsedDelta: 2, SourceTurn: 1}); err != nil || !limited {
		t.Fatalf("expected budget limit, limited=%v err=%v", limited, err)
	}
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "Record budget wrap-up, then stop.")); err != nil {
		t.Fatalf("append user: %v", err)
	}
	blockRuntimeGoalHistoryPath(t, engine.store, meta.ID)
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		t.Fatalf("provider should not be called after budget wrap-up history append failure")
		return provider.TurnResult{}, nil
	})

	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err == nil || !strings.Contains(err.Error(), "goal-history.jsonl") {
		t.Fatalf("expected goal-history append error, result=%#v err=%v", result, err)
	}
	if result.Status != session.StatusFailed {
		t.Fatalf("expected failed result after goal-history append error, got %#v", result)
	}
	goal, loadErr := engine.store.LoadGoal(meta.ID)
	if loadErr != nil {
		t.Fatalf("load goal: %v", loadErr)
	}
	if goal.BudgetWrapUpTurnStartedAt != "" {
		t.Fatalf("failed budget wrap-up turn start should not advance goal snapshot, got %#v", goal)
	}
}

func TestEngineYoloBypassesRetrievalGuards(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.GuardrailsMode = "yolo"
	engine, meta, state, registry, hookManager, catalog := newTestEngineWithConfig(t, cfg, session.ModeRun)
	writeEvidenceFile(t, meta.Workdir, "docs/guide.md", "hello from yolo\n")
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "Audit the repo and keep going.")); err != nil {
		t.Fatalf("append user: %v", err)
	}
	reminder := session.NewMessage("user", "Harness reminder: stop exploring.")
	reminder.Meta = map[string]any{
		"source": "harness_reminder",
		"kind":   "retrieval_tail",
	}
	if err := engine.store.AppendMessage(meta.ID, reminder); err != nil {
		t.Fatalf("append reminder: %v", err)
	}

	fake := provider.NewFake(
		func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
			return provider.TurnResult{
				ToolCalls: []provider.ToolCall{{
					ID:   "call_shell_yolo",
					Name: "shell",
					Arguments: json.RawMessage(`{
						"command":"python - <<'PY'\nfrom pathlib import Path\nprint(Path('docs/guide.md').read_text())\nPY"
					}`),
				}},
				StopReason: "tool_use",
			}, nil
		},
		func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
			last := req.Messages[len(req.Messages)-1]
			if last.Role != "tool" || len(last.ToolResults) == 0 {
				t.Fatalf("expected tool result before finish, got %#v", last)
			}
			if last.ToolResults[0].IsError {
				t.Fatalf("expected shell result to pass in yolo mode, got %#v", last.ToolResults[0])
			}
			if !strings.Contains(last.ToolResults[0].DisplayOutput, "hello from yolo") {
				t.Fatalf("expected shell output to contain file text, got %#v", last.ToolResults[0].DisplayOutput)
			}
			return provider.TurnResult{
				ToolCalls: []provider.ToolCall{{
					ID:        "call_finish_yolo",
					Name:      "finish",
					Arguments: json.RawMessage(`{"message":"done"}`),
				}},
				StopReason: "tool_use",
			}, nil
		},
	)

	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("expected completed status, got %s (%s)", result.Status, result.LastError)
	}
}

func TestEngineEmitsProviderRequestPreparedEvent(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	meta.Provider = "openai-compatible"
	meta.RootSessionID = "root-1"
	meta.ParentSessionID = "parent-1"
	meta.AgentName = "reviewer"
	meta.AgentRole = "evaluator"
	store := false
	includeThoughts := true
	meta.ProviderOptions = session.ProviderOptions{
		MaxOutputTokens: 256,
		ReasoningEffort: "high",
		TextVerbosity:   "low",
		ThinkingBudget:  1024,
		IncludeThoughts: &includeThoughts,
		Store:           &store,
		RetryPolicy: &session.ProviderRetryPolicy{
			MaxAttempts:    3,
			BaseDelayMS:    250,
			Retry429:       true,
			Retry5xx:       true,
			RetryTransport: true,
		},
		TimeoutPolicy: &session.ProviderTimeoutPolicy{
			TimeoutSec:          30,
			RequestTimeoutSec:   240,
			StreamIdleTimeoutMS: 300000,
		},
	}
	if err := engine.store.SaveMetadata(meta.ID, meta); err != nil {
		t.Fatalf("save metadata: %v", err)
	}
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "hello")); err != nil {
		t.Fatalf("append: %v", err)
	}
	fake := provider.NewFake(func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{
			ToolCalls:  []provider.ToolCall{{ID: "call_1", Name: "finish", Arguments: json.RawMessage(`{"message":"done"}`)}},
			StopReason: "tool_use",
		}, nil
	})
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("expected completed, got %s", result.Status)
	}
	events, err := loadEvents(engine.store, meta.ID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	evt, ok := findEventByType(events, "provider.request.prepared")
	if !ok {
		t.Fatalf("expected provider.request.prepared event, got %#v", events)
	}
	if evt.Data["provider"] != "openai-compatible" || evt.Data["model"] != meta.Model {
		t.Fatalf("expected provider/model in prepared event, got %#v", evt.Data)
	}
	if evt.Data["metadata_enabled"] != true {
		t.Fatalf("expected metadata_enabled=true, got %#v", evt.Data["metadata_enabled"])
	}
	keys, _ := evt.Data["metadata_keys"].([]any)
	wantKeys := []string{"agent_name", "agent_role", "mode", "parent_session_id", "root_session_id", "session_id"}
	if len(keys) != len(wantKeys) {
		t.Fatalf("unexpected metadata keys: %#v", evt.Data["metadata_keys"])
	}
	for i, key := range wantKeys {
		if keys[i] != key {
			t.Fatalf("unexpected metadata key order: %#v", evt.Data["metadata_keys"])
		}
	}
	if evt.Data["reasoning_effort"] != "high" || evt.Data["text_verbosity"] != "low" {
		t.Fatalf("expected reasoning/text verbosity in prepared event, got %#v", evt.Data)
	}
	if evt.Data["max_output_tokens"] != float64(256) || evt.Data["thinking_budget"] != float64(1024) {
		t.Fatalf("expected max_output_tokens/thinking_budget in prepared event, got %#v", evt.Data)
	}
	if evt.Data["include_thoughts"] != true || evt.Data["store"] != false {
		t.Fatalf("expected include_thoughts/store in prepared event, got %#v", evt.Data)
	}
	retryPolicy, ok := evt.Data["retry_policy"].(map[string]any)
	if !ok {
		t.Fatalf("expected retry_policy object, got %#v", evt.Data["retry_policy"])
	}
	if retryPolicy["max_attempts"] != float64(3) || retryPolicy["base_delay_ms"] != float64(250) {
		t.Fatalf("unexpected retry policy event payload: %#v", retryPolicy)
	}
	timeoutPolicy, ok := evt.Data["timeout_policy"].(map[string]any)
	if !ok {
		t.Fatalf("expected timeout_policy object, got %#v", evt.Data["timeout_policy"])
	}
	if timeoutPolicy["request_timeout_sec"] != float64(240) || timeoutPolicy["stream_idle_timeout_ms"] != float64(300000) {
		t.Fatalf("unexpected timeout policy event payload: %#v", timeoutPolicy)
	}
}

func TestEngineAutoResumesProviderTimeoutBeforeFailing(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	engine.cfg.Runtime.ProviderAutoResume.Enabled = true
	engine.cfg.Runtime.ProviderAutoResume.MaxAttempts = 2
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "Return a finish tool call.")); err != nil {
		t.Fatalf("append: %v", err)
	}
	callCount := 0
	fake := provider.NewFake(
		func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
			callCount++
			return provider.TurnResult{}, &provider.HTTPError{
				Provider:    "fake",
				Class:       "upstream_timeout",
				Message:     "context deadline exceeded",
				TimeoutKind: "request_timeout",
			}
		},
		func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
			callCount++
			last := req.Messages[len(req.Messages)-1]
			source, _ := last.Meta["source"].(string)
			kind, _ := last.Meta["kind"].(string)
			if last.Role != "user" || source != "harness_reminder" || kind != "provider_auto_resume" {
				t.Fatalf("expected provider auto-resume reminder, got %#v", last)
			}
			for _, want := range []string{
				"provider/gateway timeout, not a shell or tool hang",
				"session is still running",
				"auto-resuming (1/2)",
				"fix the specific finish guard",
			} {
				if !strings.Contains(last.Text, want) {
					t.Fatalf("expected auto-resume reminder to contain %q, got %q", want, last.Text)
				}
			}
			return provider.TurnResult{
				ToolCalls:  []provider.ToolCall{{ID: "call_1", Name: "finish", Arguments: json.RawMessage(`{"message":"done"}`)}},
				StopReason: "tool_use",
			}, nil
		},
	)
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("expected completed after auto-resume, got %#v", result)
	}
	if callCount != 2 {
		t.Fatalf("expected 2 provider calls, got %d", callCount)
	}
	loaded, err := engine.store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if loaded.ProviderAutoResumeCount != 0 {
		t.Fatalf("expected auto-resume counter reset after success, got %#v", loaded)
	}
	events, err := loadEvents(engine.store, meta.ID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	evt, ok := findEventByType(events, "provider.auto_resume")
	if !ok {
		t.Fatalf("expected provider.auto_resume event, got %#v", events)
	}
	if evt.Data["class"] != "upstream_timeout" || evt.Data["timeout_kind"] != "request_timeout" {
		t.Fatalf("unexpected auto-resume event data: %#v", evt.Data)
	}
}

func TestEngineCanSuppressProviderMetadataFromConfig(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	sendMetadata := false
	meta.ProviderOptions.SendMetadata = &sendMetadata
	if err := engine.store.SaveMetadata(meta.ID, meta); err != nil {
		t.Fatalf("save metadata: %v", err)
	}
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "hello")); err != nil {
		t.Fatalf("append: %v", err)
	}
	fake := provider.NewFake(func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
		if len(req.Metadata) != 0 {
			t.Fatalf("expected metadata to be suppressed, got %#v", req.Metadata)
		}
		return provider.TurnResult{
			ToolCalls:  []provider.ToolCall{{ID: "call_1", Name: "finish", Arguments: json.RawMessage(`{"message":"done"}`)}},
			StopReason: "tool_use",
		}, nil
	})
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("expected completed, got %s", result.Status)
	}
}

func TestEngineBlocksEscapingFinalArtifactPathBeforeToolExecution(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	rootDir := t.TempDir()
	workdir := filepath.Join(rootDir, "workspace")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	meta.Workdir = workdir
	if err := engine.store.SaveMetadata(meta.ID, meta); err != nil {
		t.Fatalf("save metadata: %v", err)
	}
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "Audit the repo and write reports/final-audit.md with findings and finish.")); err != nil {
		t.Fatalf("append: %v", err)
	}
	outsidePath := filepath.Join(rootDir, "escape.md")
	fake := provider.NewFake(
		func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
			return provider.TurnResult{
				ToolCalls: []provider.ToolCall{{
					ID:   "call_1",
					Name: "write_file",
					Arguments: json.RawMessage(`{
						"path":"../escape.md",
						"content":"# Findings\n\n## Finding 1\nSeverity: low\nConfidence: medium\nEvidence: internal/runtime/engine.go:1 (\"package runtime\")\nSnippet: package runtime\nWhy it matters: prove the guard blocks before execution.\n\n## Remaining Risks\n- none\n"
					}`),
				}},
				StopReason: "tool_use",
			}, nil
		},
		func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
			return provider.TurnResult{Text: "done_candidate", StopReason: "done_candidate"}, nil
		},
	)
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusFailed {
		t.Fatalf("expected failed status after blocked write without finish, got %s", result.Status)
	}
	if _, err := os.Stat(outsidePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected escaping file not to be written, got err=%v", err)
	}
	events, err := loadEvents(engine.store, meta.ID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	evt, ok := findEventByType(events, "tool.blocked")
	if !ok {
		t.Fatalf("expected tool.blocked event, got %#v", events)
	}
	if evt.Data["reason"] != "artifact_path" {
		t.Fatalf("expected artifact_path block reason, got %#v", evt.Data)
	}
}

func TestEngineArtifactTrackingFailureWritesReplayCompleteToolResult(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "Write reports/final.md with the final implementation summary.")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := refreshContractForSession(engine.store, nil, meta); err != nil {
		t.Fatalf("refresh contract: %v", err)
	}
	blockRuntimeArtifactTrackerPath(t, engine.store, meta.ID)
	artifactPath := filepath.Join(meta.Workdir, "reports", "final.md")
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{
			ToolCalls: []provider.ToolCall{
				{
					ID:        "call_write",
					Name:      "write_file",
					Arguments: json.RawMessage(`{"path":"reports/final.md","content":"final summary"}`),
				},
				{
					ID:        "call_finish",
					Name:      "finish",
					Arguments: json.RawMessage(`{"message":"done"}`),
				},
			},
			StopReason: "tool_use",
		}, nil
	})

	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err == nil || !strings.Contains(err.Error(), "artifact-tracker.json") {
		t.Fatalf("expected artifact tracker error, result=%#v err=%v", result, err)
	}
	if result.Status != session.StatusFailed {
		t.Fatalf("expected failed result after artifact tracking error, got %#v", result)
	}
	if data, readErr := os.ReadFile(artifactPath); readErr != nil || string(data) != "final summary" {
		t.Fatalf("expected write_file side effect to remain recorded on disk, data=%q err=%v", data, readErr)
	}
	messages, err := engine.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	var toolMessages []session.Message
	for _, msg := range messages {
		if msg.Role == "tool" {
			toolMessages = append(toolMessages, msg)
		}
	}
	if len(toolMessages) != 1 {
		t.Fatalf("expected one replay-complete tool message, got %#v", toolMessages)
	}
	results := toolMessages[0].ToolResults
	if len(results) != 2 {
		t.Fatalf("expected failed write result plus synthetic finish result, got %#v", results)
	}
	if results[0].ToolCallID != "call_write" || !results[0].IsError || !strings.Contains(results[0].LLMOutput, "artifact-tracker.json") {
		t.Fatalf("expected artifact tracker failure on write result, got %#v", results[0])
	}
	if results[1].ToolCallID != "call_finish" || !results[1].IsError || !strings.Contains(results[1].LLMOutput, "artifact tracker update failed before this call ran") {
		t.Fatalf("expected synthetic later result for finish, got %#v", results[1])
	}
}

func TestEngineAllowsSingleResolutionTurnAfterHardLimitToolResult(t *testing.T) {
	cfg := config.Default()
	engine, meta, state, registry, hookManager, catalog := newTestEngineWithConfig(t, cfg, session.ModeExec)
	engine.cfg.Runtime.MaxTurnsHard = 1
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "Write a proof file and finish.")); err != nil {
		t.Fatalf("append: %v", err)
	}
	callCount := 0
	fake := provider.NewFake(
		func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
			callCount++
			return provider.TurnResult{
				ToolCalls: []provider.ToolCall{{
					ID:        "call_1",
					Name:      "write_file",
					Arguments: json.RawMessage(`{"path":"reports/proof.md","content":"intermediate"}`),
				}},
				StopReason: "tool_use",
			}, nil
		},
		func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
			callCount++
			return provider.TurnResult{
				ToolCalls: []provider.ToolCall{
					{
						ID:        "call_2",
						Name:      "write_file",
						Arguments: json.RawMessage(`{"path":"reports/proof.md","content":"final proof"}`),
					},
					{
						ID:        "call_3",
						Name:      "finish",
						Arguments: json.RawMessage(`{"message":"done"}`),
					},
				},
				StopReason: "tool_use",
			}, nil
		},
	)
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("expected completed, got %s (%s)", result.Status, result.LastError)
	}
	if callCount != 2 {
		t.Fatalf("expected 2 provider calls, got %d", callCount)
	}
	data, err := os.ReadFile(filepath.Join(meta.Workdir, "reports", "proof.md"))
	if err != nil {
		t.Fatalf("read proof: %v", err)
	}
	if string(data) != "final proof" {
		t.Fatalf("expected final proof content, got %q", string(data))
	}
}

func TestEngineFailsAfterResolutionTurnNeedsAnotherProviderPass(t *testing.T) {
	cfg := config.Default()
	engine, meta, state, registry, hookManager, catalog := newTestEngineWithConfig(t, cfg, session.ModeExec)
	engine.cfg.Runtime.MaxTurnsHard = 1
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "Keep working until done.")); err != nil {
		t.Fatalf("append: %v", err)
	}
	callCount := 0
	fake := provider.NewFake(
		func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
			callCount++
			return provider.TurnResult{
				ToolCalls: []provider.ToolCall{{
					ID:        "call_1",
					Name:      "write_file",
					Arguments: json.RawMessage(`{"path":"reports/proof.md","content":"step one"}`),
				}},
				StopReason: "tool_use",
			}, nil
		},
		func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
			callCount++
			return provider.TurnResult{
				ToolCalls: []provider.ToolCall{{
					ID:        "call_2",
					Name:      "write_file",
					Arguments: json.RawMessage(`{"path":"reports/proof.md","content":"step two"}`),
				}},
				StopReason: "tool_use",
			}, nil
		},
	)
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusFailed {
		t.Fatalf("expected failed, got %s", result.Status)
	}
	if result.LastError != "max_turns_hard_exceeded" {
		t.Fatalf("expected max_turns_hard_exceeded, got %q", result.LastError)
	}
	if callCount != 2 {
		t.Fatalf("expected 2 provider calls, got %d", callCount)
	}
}

func TestEngineExecModeRequiresFinish(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "hello")); err != nil {
		t.Fatalf("append: %v", err)
	}
	fake := provider.NewFake(
		func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
			return provider.TurnResult{Text: "first", StopReason: "done_candidate"}, nil
		},
		func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
			return provider.TurnResult{Text: "second", StopReason: "done_candidate"}, nil
		},
	)
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusFailed {
		t.Fatalf("expected failed, got %s", result.Status)
	}
	loaded, err := engine.store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if loaded.IncompleteReason != "incomplete_no_finish" {
		t.Fatalf("expected incomplete_no_finish, got %q", loaded.IncompleteReason)
	}
	messages, err := engine.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	found := false
	for _, msg := range messages {
		if msg.Role != "user" || !strings.Contains(msg.Text, "Harness reminder: if the task is complete") {
			continue
		}
		source, _ := msg.Meta["source"].(string)
		if source != "harness_reminder" {
			t.Fatalf("expected harness reminder metadata, got %#v", msg.Meta)
		}
		found = true
	}
	if !found {
		t.Fatalf("expected stored harness reminder message, got %#v", messages)
	}
}

func TestEngineIncompleteNoFinishReportsFailedEventAppendError(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "hello")); err != nil {
		t.Fatalf("append: %v", err)
	}
	eventsPath := filepath.Join(engine.store.SessionDir(meta.ID), "events.jsonl")
	fake := provider.NewFake(
		func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
			return provider.TurnResult{Text: "first", StopReason: "done_candidate"}, nil
		},
		func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
			if err := os.Remove(eventsPath); err != nil && !os.IsNotExist(err) {
				t.Fatalf("remove events: %v", err)
			}
			if err := os.Mkdir(eventsPath, 0o700); err != nil {
				t.Fatalf("block events path: %v", err)
			}
			return provider.TurnResult{Text: "second", StopReason: "done_candidate"}, nil
		},
	)

	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err == nil {
		t.Fatalf("expected session.failed event append error, got result=%#v", result)
	}
	if !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected events append error with path context, got %v", err)
	}
	if !strings.Contains(err.Error(), "incomplete_no_finish") || !strings.Contains(err.Error(), "session.failed") {
		t.Fatalf("expected incomplete_no_finish failed event context, got %v", err)
	}
}

func TestEngineAllowsDisablingHardTurnLimit(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	engine.cfg.Runtime.MaxTurnsHard = -1
	state.Turn = 50
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "Continue working without a hard turn cap.")); err != nil {
		t.Fatalf("append: %v", err)
	}
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{Text: "still running", StopReason: "done_candidate"}, nil
	})

	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusAwaitingInput {
		t.Fatalf("expected awaiting_input with disabled hard limit, got %#v", result)
	}
}

func TestEngineAppendsRetrievalTailHarnessReminderBeforeProviderCall(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "Inspect README.md and AGENTS.md and summarize the runtime surface.")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := engine.store.AppendMessage(meta.ID, session.NewToolMessage([]session.ToolResult{
		{Name: "glob"},
		{Name: "grep_files"},
		{Name: "grep"},
		{Name: "read_file", Metadata: map[string]any{"path": filepath.Join(meta.Workdir, "README.md")}},
		{Name: "read_file", Metadata: map[string]any{"path": filepath.Join(meta.Workdir, "README.md")}},
		{Name: "read_file", Metadata: map[string]any{"path": filepath.Join(meta.Workdir, "AGENTS.md")}},
	})); err != nil {
		t.Fatalf("append tool message: %v", err)
	}
	fake := provider.NewFake(func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
		last := req.Messages[len(req.Messages)-1]
		source, _ := last.Meta["source"].(string)
		kind, _ := last.Meta["kind"].(string)
		if last.Role != "user" || source != "harness_reminder" || kind != "retrieval_tail" {
			t.Fatalf("expected retrieval-tail harness reminder, got %#v", last)
		}
		if !strings.Contains(last.Text, "Recent work already used 6 read-only tool calls") {
			t.Fatalf("expected retrieval-tail text, got %q", last.Text)
		}
		return provider.TurnResult{
			ToolCalls:  []provider.ToolCall{{ID: "call_1", Name: "finish", Arguments: json.RawMessage(`{"message":"done"}`)}},
			StopReason: "tool_use",
		}, nil
	})
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("expected completed, got %s", result.Status)
	}
}

func TestEngineAppendsArtifactCompletionHarnessReminderBeforeProviderCall(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "Write reports/final-audit.md with findings and finish.")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := engine.store.AppendMessage(meta.ID, session.NewToolMessage([]session.ToolResult{
		{
			Name:          "write_file",
			DisplayOutput: "Wrote 128 bytes to reports/final-audit.md",
			Metadata: map[string]any{
				"path":                  filepath.Join(meta.Workdir, "reports", "final-audit.md"),
				"review_artifact_valid": true,
			},
		},
	})); err != nil {
		t.Fatalf("append write_file tool result: %v", err)
	}
	if err := engine.store.AppendMessage(meta.ID, session.NewToolMessage([]session.ToolResult{
		{
			Name:          "todo_write",
			DisplayOutput: "[{\"content\":\"Write final audit\",\"status\":\"completed\",\"priority\":\"high\",\"updated_at\":\"2026-03-20T00:00:00Z\"}]",
		},
	})); err != nil {
		t.Fatalf("append todo_write tool result: %v", err)
	}
	fake := provider.NewFake(func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
		last := req.Messages[len(req.Messages)-1]
		source, _ := last.Meta["source"].(string)
		kind, _ := last.Meta["kind"].(string)
		if last.Role != "user" || source != "harness_reminder" || kind != "artifact_written" {
			t.Fatalf("expected artifact-written harness reminder, got %#v", last)
		}
		if !strings.Contains(last.Text, "reports/final-audit.md") || !strings.Contains(last.Text, "Call finish now") {
			t.Fatalf("expected artifact completion text, got %q", last.Text)
		}
		return provider.TurnResult{
			ToolCalls:  []provider.ToolCall{{ID: "call_1", Name: "finish", Arguments: json.RawMessage(`{"message":"done"}`)}},
			StopReason: "tool_use",
		}, nil
	})
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("expected completed, got %s", result.Status)
	}
}

func TestEngineEphemeralArtifactGuidanceAvoidsReadFileLoop(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	writeEvidenceFile(t, meta.Workdir, "visible.txt", "visible\n")
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "Keep checking the same glob output until you finish.")); err != nil {
		t.Fatalf("append: %v", err)
	}
	fake := provider.NewFake(
		func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
			return provider.TurnResult{
				ToolCalls:  []provider.ToolCall{{ID: "call_1", Name: "glob", Arguments: json.RawMessage(`{"pattern":"**/*.txt"}`)}},
				StopReason: "tool_use",
			}, nil
		},
		func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
			return provider.TurnResult{
				ToolCalls:  []provider.ToolCall{{ID: "call_2", Name: "glob", Arguments: json.RawMessage(`{"pattern":"**/*.txt"}`)}},
				StopReason: "tool_use",
			}, nil
		},
		func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
			return provider.TurnResult{
				ToolCalls:  []provider.ToolCall{{ID: "call_3", Name: "glob", Arguments: json.RawMessage(`{"pattern":"**/*.txt"}`)}},
				StopReason: "tool_use",
			}, nil
		},
		func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
			return provider.TurnResult{
				ToolCalls:  []provider.ToolCall{{ID: "call_4", Name: "glob", Arguments: json.RawMessage(`{"pattern":"**/*.txt"}`)}},
				StopReason: "tool_use",
			}, nil
		},
	)
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusAwaitingInput {
		t.Fatalf("expected awaiting_input, got status=%s last_error=%q", result.Status, result.LastError)
	}

	messages, err := engine.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	found := false
	for _, msg := range messages {
		if msg.Role != "tool" {
			continue
		}
		for _, toolResult := range msg.ToolResults {
			if toolResult.Name != "glob" {
				continue
			}
			artifactPath, _ := toolResult.Metadata["ephemeral_artifact"].(string)
			if artifactPath == "" {
				continue
			}
			found = true
			if !strings.Contains(toolResult.LLMOutput, "not readable via read_file") {
				t.Fatalf("expected explicit read_file warning, got %q", toolResult.LLMOutput)
			}
			if !strings.Contains(toolResult.LLMOutput, "reports/validation.txt") {
				t.Fatalf("expected workspace redirect guidance, got %q", toolResult.LLMOutput)
			}
			if strings.Contains(toolResult.LLMOutput, "use read_file to review if needed") {
				t.Fatalf("expected old misleading guidance to be removed, got %q", toolResult.LLMOutput)
			}
			wantPrefix := filepath.Join(engine.store.SessionDir(meta.ID), "artifacts", "tool-outputs")
			if !strings.HasPrefix(artifactPath, wantPrefix+string(os.PathSeparator)) {
				t.Fatalf("expected default ephemeral artifact under session root %s, got %s", wantPrefix, artifactPath)
			}
			info, err := os.Stat(artifactPath)
			if err != nil {
				t.Fatalf("stat ephemeral artifact: %v", err)
			}
			if perm := info.Mode().Perm(); perm != 0o600 {
				t.Fatalf("expected ephemeral artifact mode 0600, got %s", perm.String())
			}
		}
	}
	if !found {
		t.Fatal("expected ephemeral artifact guidance to be recorded")
	}
}

func TestEngineEphemeralArtifactRejectsSymlinkTarget(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	writeEvidenceFile(t, meta.Workdir, "visible.txt", "visible\n")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("keep\n"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	for turn := 0; turn <= 5; turn++ {
		artifactPath := engine.ephemeralArtifactPath(meta.ID, "glob", turn)
		if err := os.MkdirAll(filepath.Dir(artifactPath), 0o700); err != nil {
			t.Fatalf("mkdir artifact dir: %v", err)
		}
		if err := os.Symlink(outside, artifactPath); err != nil {
			t.Fatalf("symlink artifact path: %v", err)
		}
	}
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "Repeat glob output.")); err != nil {
		t.Fatalf("append: %v", err)
	}
	fake := provider.NewFake(
		func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
			return provider.TurnResult{ToolCalls: []provider.ToolCall{{ID: "call_1", Name: "glob", Arguments: json.RawMessage(`{"pattern":"**/*.txt"}`)}}, StopReason: "tool_use"}, nil
		},
		func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
			return provider.TurnResult{ToolCalls: []provider.ToolCall{{ID: "call_2", Name: "glob", Arguments: json.RawMessage(`{"pattern":"**/*.txt"}`)}}, StopReason: "tool_use"}, nil
		},
		func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
			return provider.TurnResult{ToolCalls: []provider.ToolCall{{ID: "call_3", Name: "glob", Arguments: json.RawMessage(`{"pattern":"**/*.txt"}`)}}, StopReason: "tool_use"}, nil
		},
		func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
			return provider.TurnResult{ToolCalls: []provider.ToolCall{{ID: "call_4", Name: "glob", Arguments: json.RawMessage(`{"pattern":"**/*.txt"}`)}}, StopReason: "tool_use"}, nil
		},
	)
	if _, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager); err != nil {
		t.Fatalf("run: %v", err)
	}
	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("read outside file: %v", err)
	}
	if string(data) != "keep\n" {
		t.Fatalf("outside file was overwritten through ephemeral artifact symlink: %q", data)
	}
}

func TestEngineAppendsSteerCompletionReminderAndEscalatesAfterBlockedDetour(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	writeEvidenceFile(t, meta.Workdir, "internal/runtime/prompt.go", "package runtime\n\nfunc deliveryNote() string { return \"delivery did not happen immediately after the interrupt steer\" }\n")
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "Inspect the repo and report findings.")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := engine.store.AppendSteerRequest(meta.ID, session.NewSteerRequest("Stop reading now. Use current evidence, write reports/steer-audit.md immediately, and finish.", true)); err != nil {
		t.Fatalf("append steer: %v", err)
	}
	fake := provider.NewFake(
		func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
			last := req.Messages[len(req.Messages)-1]
			source, _ := last.Meta["source"].(string)
			kind, _ := last.Meta["kind"].(string)
			if last.Role != "user" || source != "harness_reminder" || kind != "steer_completion" {
				t.Fatalf("expected steer completion reminder, got %#v", last)
			}
			return provider.TurnResult{
				ToolCalls:  []provider.ToolCall{{ID: "call_1", Name: "todo_write", Arguments: json.RawMessage(`{"todos":[]}`)}},
				StopReason: "tool_use",
			}, nil
		},
		func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
			last := req.Messages[len(req.Messages)-1]
			source, _ := last.Meta["source"].(string)
			kind, _ := last.Meta["kind"].(string)
			if last.Role != "user" || source != "harness_reminder" || kind != "steer_completion_escalated" {
				t.Fatalf("expected escalated steer completion reminder, got %#v", last)
			}
			return provider.TurnResult{
				ToolCalls: []provider.ToolCall{
					{
						ID:        "call_2",
						Name:      "write_file",
						Arguments: json.RawMessage(`{"path":"reports/steer-audit.md","content":"# steer audit\n\n## findings\n1. Severity: medium\n   Confidence: high\n   Evidence: internal/runtime/prompt.go:3 (\"delivery did not happen immediately after the interrupt steer\")\n   Why it matters: delivery did not happen immediately after the interrupt steer.\n\n## unresolved questions\n- Should the runtime block narration-only turns after interrupt steer?"}`),
					},
					{
						ID:        "call_3",
						Name:      "finish",
						Arguments: json.RawMessage(`{"message":"done"}`),
					},
				},
				StopReason: "tool_use",
			}, nil
		},
	)
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("expected completed, got %s", result.Status)
	}
	events, err := loadEvents(engine.store, meta.ID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if !hasEventType(events, "tool.blocked") {
		t.Fatalf("expected blocked tool event, got %#v", events)
	}
	messages, err := engine.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	foundEscalated := false
	for _, msg := range messages {
		if msg.Role != "user" {
			continue
		}
		source, _ := msg.Meta["source"].(string)
		kind, _ := msg.Meta["kind"].(string)
		if source == "harness_reminder" && kind == "steer_completion_escalated" {
			foundEscalated = true
			break
		}
	}
	if !foundEscalated {
		t.Fatalf("expected escalated reminder in stored messages, got %#v", messages)
	}
}

func TestEngineInterruptSteerAllowsCurrentEvidenceArtifactDelivery(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	writeEvidenceFile(t, meta.Workdir, "internal/tools/registry.go", "package tools\n\nvar reservedNames = map[string]struct{}{ \"agent_spawn\": {} }\n\nfunc builtinDefinitions(cfg any) []string {\n\tif cfg != nil {\n\t\treturn []string{\"cfg.Runtime.MultiAgent.Enabled\"}\n\t}\n\treturn nil\n}\n")
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "Audit whether the default Web-first surface, CLI fallback, and docs stay aligned with Web-first v1 boundaries and write reports/steer-audit.md.")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := engine.store.AppendMessage(meta.ID, session.NewToolMessage([]session.ToolResult{
		{
			Name: "read_file",
			Metadata: map[string]any{
				"path": filepath.Join(meta.Workdir, "internal", "tools", "registry.go"),
			},
			DisplayOutput: "var reservedNames = map[string]struct{}{ \"agent_spawn\": {} }",
		},
	})); err != nil {
		t.Fatalf("append read_file tool result: %v", err)
	}
	firstTurnStarted := make(chan struct{})
	go func() {
		<-firstTurnStarted
		time.Sleep(20 * time.Millisecond)
		if err := engine.store.AppendSteerRequest(meta.ID, session.NewSteerRequest("Stop reading now. Use current evidence, write reports/steer-audit.md immediately, and finish.", true)); err != nil {
			t.Errorf("append steer: %v", err)
			return
		}
		engine.control.requestSteerInterrupt()
	}()
	fake := provider.NewFake(
		func(ctx context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
			hasReadEvidence := false
			for _, msg := range req.Messages {
				if msg.Role != "tool" {
					continue
				}
				for _, result := range msg.ToolResults {
					if result.Name == "read_file" {
						hasReadEvidence = true
						break
					}
				}
				if hasReadEvidence {
					break
				}
			}
			if !hasReadEvidence {
				t.Fatalf("expected existing read_file evidence in provider request, got %#v", req.Messages)
			}
			close(firstTurnStarted)
			<-ctx.Done()
			return provider.TurnResult{}, ctx.Err()
		},
		func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
			last := req.Messages[len(req.Messages)-1]
			source, _ := last.Meta["source"].(string)
			kind, _ := last.Meta["kind"].(string)
			if last.Role != "user" || source != "harness_reminder" || kind != "steer_completion" {
				t.Fatalf("expected steer completion reminder, got %#v", last)
			}
			return provider.TurnResult{
				ToolCalls: []provider.ToolCall{
					{
						ID:   "call_2",
						Name: "write_file",
						Arguments: json.RawMessage(`{
								"path":"reports/steer-audit.md",
								"content":"# steer audit\n\n## findings\n### Finding 1\nSeverity: medium\nConfidence: low\nEvidence: internal/tools/registry.go:3 (\"reservedNames\")\nSnippet: reservedNames\nWhy it matters: declaration-level hints alone do not prove default exposure.\n\n## unresolved questions\n- The owning gate read was intentionally deferred after the interrupt steer and remains a risk."
							}`),
					},
					{
						ID:        "call_3",
						Name:      "finish",
						Arguments: json.RawMessage(`{"message":"done"}`),
					},
				},
				StopReason: "tool_use",
			}, nil
		},
	)
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("expected completed, got %s", result.Status)
	}
	reportPath := filepath.Join(meta.Workdir, "reports", "steer-audit.md")
	if _, err := os.Stat(reportPath); err != nil {
		t.Fatalf("expected steer audit artifact to be written, got stat error: %v", err)
	}
}

func TestEngineAcceptsPendingSteerBeforeProviderCall(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	if err := engine.store.AppendSteerRequest(meta.ID, session.NewSteerRequest("focus on tests", false)); err != nil {
		t.Fatalf("steer: %v", err)
	}
	fake := provider.NewFake(func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
		last := req.Messages[len(req.Messages)-1]
		if last.Text != "focus on tests" {
			t.Fatalf("expected steer message to be appended before provider call, got %#v", last)
		}
		return provider.TurnResult{Text: "ok", StopReason: "done_candidate"}, nil
	})
	if _, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager); err != nil {
		t.Fatalf("run: %v", err)
	}
	loadedState, err := engine.store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if loadedState.PendingSteerCount != 0 {
		t.Fatalf("expected pending steer count to drain to zero, got %d", loadedState.PendingSteerCount)
	}
}

func TestEngineSteerAcceptanceReportsGoalHistoryError(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	if _, err := engine.store.CreateGoal(meta.ID, session.GoalDraft{
		Enabled:   true,
		Objective: "Track steer in goal history",
		Source:    session.GoalSourceCLI,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	if err := engine.store.AppendSteerRequest(meta.ID, session.NewSteerRequest("focus on tests", false)); err != nil {
		t.Fatalf("steer: %v", err)
	}
	blockRuntimeGoalHistoryPath(t, engine.store, meta.ID)
	fake := provider.NewFake(func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
		t.Fatalf("provider should not be called after goal history append failure")
		return provider.TurnResult{}, nil
	})
	if _, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager); err == nil || !strings.Contains(err.Error(), "goal-history.jsonl") {
		t.Fatalf("expected goal history append error, got %v", err)
	}
}

func TestEngineSteerAcceptanceReportsCorruptGoalSnapshot(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	goalPath := filepath.Join(engine.store.SessionDir(meta.ID), "goal.json")
	if err := os.WriteFile(goalPath, []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatalf("write corrupt goal: %v", err)
	}
	if err := engine.store.AppendSteerRequest(meta.ID, session.NewSteerRequest("focus on tests", false)); err != nil {
		t.Fatalf("steer: %v", err)
	}
	fake := provider.NewFake(func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
		t.Fatalf("provider should not be called after corrupt goal snapshot")
		return provider.TurnResult{}, nil
	})
	if _, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager); err == nil || !strings.Contains(err.Error(), "goal.json") {
		t.Fatalf("expected corrupt goal snapshot error, got %v", err)
	}
}

func TestEngineSteerAcceptanceReportsAcceptedEventAppendError(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	if err := engine.store.AppendSteerRequest(meta.ID, session.NewSteerRequest("focus on tests", false)); err != nil {
		t.Fatalf("steer: %v", err)
	}
	eventsPath := filepath.Join(engine.store.SessionDir(meta.ID), "events.jsonl")
	if err := os.Remove(eventsPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove events: %v", err)
	}
	if err := os.Mkdir(eventsPath, 0o700); err != nil {
		t.Fatalf("block events path: %v", err)
	}
	fake := provider.NewFake(func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
		t.Fatalf("provider should not be called after steer accepted event append failure")
		return provider.TurnResult{}, nil
	})

	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err == nil || !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected steer accepted event append error, result=%#v err=%v", result, err)
	}
}

func TestEngineRefreshesPendingSteerCountAfterConcurrentAppend(t *testing.T) {
	engine, meta, state, registry, _, catalog := newTestEngine(t, session.ModeRun)
	_ = state
	_ = registry
	_ = catalog
	first := session.NewSteerRequest("first", false)
	if err := engine.store.AppendSteerRequest(meta.ID, first); err != nil {
		t.Fatalf("append first steer: %v", err)
	}
	hookManager := hooks.New(config.HooksConfig{
		UserMessage: []config.HookDefinition{{
			Name: "append-concurrent-steer",
			Inject: &config.HookInject{
				Field: "text",
				Set:   "first",
			},
		}},
	}, meta.Workdir)
	hookManager.SetEmitter(func(string, map[string]any) {
		requests, err := engine.store.LoadSteerRequests(meta.ID)
		if err != nil || len(requests) != 1 {
			return
		}
		if err := engine.store.AppendSteerRequest(meta.ID, session.NewSteerRequest("second", false)); err != nil {
			t.Errorf("append concurrent steer: %v", err)
		}
	})
	accepted, err := engine.drainSteer(context.Background(), meta, hookManager)
	if err != nil {
		t.Fatalf("drain steer: %v", err)
	}
	if accepted != 1 {
		t.Fatalf("expected one accepted steer from initial snapshot, got %d", accepted)
	}
	loadedState, err := engine.store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if loadedState.PendingSteerCount != 1 {
		t.Fatalf("expected pending steer count to reflect concurrent append, got %#v", loadedState)
	}
	requests, err := engine.store.LoadSteerRequests(meta.ID)
	if err != nil {
		t.Fatalf("load steer requests: %v", err)
	}
	statusByText := map[string]string{}
	for _, request := range requests {
		statusByText[request.Text] = request.Status
	}
	if statusByText["first"] != session.SteerStatusAccepted || statusByText["second"] != session.SteerStatusPending {
		t.Fatalf("unexpected steer statuses: %#v", requests)
	}
}

func TestEngineInterruptSteerCancelsProviderAndContinuesWithAcceptedMessage(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "initial")); err != nil {
		t.Fatalf("append: %v", err)
	}
	firstTurnStarted := make(chan struct{})
	fake := provider.NewFake(
		func(ctx context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
			if got := req.Messages[len(req.Messages)-1].Text; got != "initial" {
				t.Fatalf("expected initial prompt on first turn, got %q", got)
			}
			close(firstTurnStarted)
			<-ctx.Done()
			return provider.TurnResult{}, ctx.Err()
		},
		func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
			last := req.Messages[len(req.Messages)-1]
			if last.Role != "user" || last.Text != "switch direction" {
				t.Fatalf("expected accepted steer message after provider cancellation, got %#v", last)
			}
			return provider.TurnResult{Text: "done", StopReason: "done_candidate"}, nil
		},
	)
	go func() {
		<-firstTurnStarted
		time.Sleep(20 * time.Millisecond)
		if err := engine.store.AppendSteerRequest(meta.ID, session.NewSteerRequest("switch direction", true)); err != nil {
			t.Errorf("append steer: %v", err)
			return
		}
		engine.control.requestSteerInterrupt()
	}()
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusAwaitingInput {
		t.Fatalf("expected awaiting_input, got %s", result.Status)
	}
	events, err := loadEvents(engine.store, meta.ID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if !hasEventType(events, "provider.cancelled") {
		t.Fatalf("expected provider.cancelled event after provider cancellation, got %#v", events)
	}
	if !hasEventType(events, "session.steer.accepted") {
		t.Fatalf("expected accepted steer event after provider cancellation, got %#v", events)
	}
}

func TestEngineInterruptSteerDeferredFinishLeavesSessionAwaitingInput(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "Initial docset task.")); err != nil {
		t.Fatalf("append: %v", err)
	}
	firstTurnStarted := make(chan struct{})
	fake := provider.NewFake(
		func(ctx context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
			if got := req.Messages[len(req.Messages)-1].Text; got != "Initial docset task." {
				t.Fatalf("expected initial prompt on first turn, got %q", got)
			}
			close(firstTurnStarted)
			<-ctx.Done()
			return provider.TurnResult{}, ctx.Err()
		},
		func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
			last := req.Messages[len(req.Messages)-1]
			source, _ := last.Meta["source"].(string)
			kind, _ := last.Meta["kind"].(string)
			if last.Role != "user" || source != "harness_reminder" || kind != "steer_completion" {
				t.Fatalf("expected steer completion reminder, got %#v", last)
			}
			if !strings.Contains(last.Text, "stop without finishing so a later continue can close the task") {
				t.Fatalf("expected deferred-finish reminder text, got %q", last.Text)
			}
			return provider.TurnResult{
				ToolCalls: []provider.ToolCall{
					{
						ID:        "call_2",
						Name:      "write_file",
						Arguments: json.RawMessage(`{"path":"reports/spec.md","content":"# Spec\n\nRollback scope refreshed.\n"}`),
					},
					{
						ID:        "call_3",
						Name:      "write_file",
						Arguments: json.RawMessage(`{"path":"reports/plan.md","content":"# Plan\n\nPause for later continue.\n"}`),
					},
					{
						ID:        "call_4",
						Name:      "finish",
						Arguments: json.RawMessage(`{"message":"done"}`),
					},
				},
				StopReason: "tool_use",
			}, nil
		},
		func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
			blockedFinish := false
			for _, msg := range req.Messages {
				if msg.Role != "tool" {
					continue
				}
				for _, result := range msg.ToolResults {
					if result.Name != "finish" {
						continue
					}
					if !result.IsError {
						t.Fatalf("expected finish to be blocked, got %#v", result)
					}
					if guard, _ := result.Metadata["guard"].(string); guard != "steer_deferred_finish" {
						t.Fatalf("expected steer_deferred_finish guard, got %#v", result.Metadata)
					}
					blockedFinish = true
				}
			}
			if !blockedFinish {
				t.Fatalf("expected blocked finish tool result in provider request, got %#v", req.Messages)
			}
			return provider.TurnResult{Text: "Artifacts refreshed. Waiting for continue.", StopReason: "done_candidate"}, nil
		},
	)
	go func() {
		<-firstTurnStarted
		time.Sleep(20 * time.Millisecond)
		if err := engine.store.AppendSteerRequest(meta.ID, session.NewSteerRequest("Actually change direction for this large documentation task: prioritize safer rollback and migration guidance. Refresh reports/spec.md and reports/plan.md before more drafting, then stop without finishing so a later continue can close the task.", true)); err != nil {
			t.Errorf("append steer: %v", err)
			return
		}
		engine.control.requestSteerInterrupt()
	}()
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusAwaitingInput {
		t.Fatalf("expected awaiting_input, got %s", result.Status)
	}
	if _, err := os.Stat(filepath.Join(meta.Workdir, "reports", "spec.md")); err != nil {
		t.Fatalf("expected refreshed spec artifact, got stat error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(meta.Workdir, "reports", "plan.md")); err != nil {
		t.Fatalf("expected refreshed plan artifact, got stat error: %v", err)
	}
	events, err := loadEvents(engine.store, meta.ID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if !hasEventType(events, "provider.cancelled") {
		t.Fatalf("expected provider.cancelled event after interrupt steer, got %#v", events)
	}
	foundBlockedFinish := false
	for _, evt := range events {
		if evt.Type != "tool.blocked" {
			continue
		}
		if reason, _ := evt.Data["reason"].(string); reason == "steer_deferred_finish" {
			foundBlockedFinish = true
			break
		}
	}
	if !foundBlockedFinish {
		t.Fatalf("expected blocked finish event, got %#v", events)
	}
}

func TestEngineAcceptsBackgroundResultsBeforeProviderCall(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "master task")); err != nil {
		t.Fatalf("append: %v", err)
	}
	notification := session.NewBackgroundNotification(session.QueueJob{
		ID:               "job_1",
		Status:           session.QueueStatusCompleted,
		SessionID:        "child_1",
		SessionStatus:    session.StatusCompleted,
		AgentName:        "reviewer",
		AgentRole:        "evaluator",
		RequestedWorkdir: meta.Workdir,
		EffectiveWorkdir: meta.Workdir,
		VisiblePaths:     []string{"reports/queue-output.md"},
		FinalText:        "child done",
	})
	if err := engine.store.AppendBackgroundNotification(meta.ID, notification); err != nil {
		t.Fatalf("append background notification: %v", err)
	}
	fake := provider.NewFake(func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
		last := req.Messages[len(req.Messages)-1]
		if last.Role != "user" || !strings.Contains(last.Text, "<background-agent-results>") || !strings.Contains(last.Text, "child done") || !strings.Contains(last.Text, "visible_paths") || !strings.Contains(last.Text, "reports/queue-output.md") || !strings.Contains(last.Text, `"agent_role": "evaluator"`) {
			t.Fatalf("expected background results message before provider call, got %#v", last)
		}
		return provider.TurnResult{Text: "ok", StopReason: "done_candidate"}, nil
	})
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusAwaitingInput {
		t.Fatalf("expected awaiting_input, got %s", result.Status)
	}
	notifications, err := engine.store.LoadBackgroundNotifications(meta.ID)
	if err != nil {
		t.Fatalf("load background notifications: %v", err)
	}
	if len(notifications) != 1 || notifications[0].DeliveryStatus != session.BackgroundNotificationAccepted {
		t.Fatalf("expected accepted background notification, got %#v", notifications)
	}
}

func TestEngineBackgroundAcceptanceReportsAcceptedEventAppendError(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "master task")); err != nil {
		t.Fatalf("append: %v", err)
	}
	notification := session.NewBackgroundNotification(session.QueueJob{
		ID:            "job_1",
		Status:        session.QueueStatusCompleted,
		SessionID:     "child_1",
		SessionStatus: session.StatusCompleted,
		FinalText:     "child done",
	})
	if err := engine.store.AppendBackgroundNotification(meta.ID, notification); err != nil {
		t.Fatalf("append background notification: %v", err)
	}
	eventsPath := filepath.Join(engine.store.SessionDir(meta.ID), "events.jsonl")
	if err := os.Remove(eventsPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove events: %v", err)
	}
	if err := os.Mkdir(eventsPath, 0o700); err != nil {
		t.Fatalf("block events path: %v", err)
	}
	fake := provider.NewFake(func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
		t.Fatalf("provider should not be called after background accepted event append failure")
		return provider.TurnResult{}, nil
	})

	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err == nil || !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected background accepted event append error, result=%#v err=%v", result, err)
	}
}

func TestEngineCompletingQueuedChildReconcilesParentQueueFacts(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	parentMeta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "parent_queue_reconcile",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
		RootSessionID:    "parent_queue_reconcile",
	}
	if err := engine.store.Create(parentMeta, session.State{Status: session.StatusRunning, Phase: "turn_decide", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	meta.ParentSessionID = parentMeta.ID
	meta.RootSessionID = parentMeta.ID
	meta.QueueJobID = "job_child_reconcile"
	if err := engine.store.SaveMetadata(meta.ID, meta); err != nil {
		t.Fatalf("save child metadata: %v", err)
	}
	if err := engine.store.SaveJob(session.QueueJob{
		SchemaVersion:   1,
		ID:              meta.QueueJobID,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		Status:          session.QueueStatusBlocked,
		ParentSessionID: parentMeta.ID,
		RootSessionID:   parentMeta.ID,
		SessionID:       meta.ID,
		SessionStatus:   session.StatusAwaitingInput,
		Prompt:          "continue child",
		Mode:            session.ModeExec,
		Background:      true,
		LastError:       "child session is resumable: awaiting_input",
	}); err != nil {
		t.Fatalf("save blocked job: %v", err)
	}
	if err := addParentQueueJob(engine.store, parentMeta.ID, meta.QueueJobID, "wait-all"); err != nil {
		t.Fatalf("add parent queue job: %v", err)
	}
	blockedNotification := session.NewBackgroundNotification(session.QueueJob{
		ID:            meta.QueueJobID,
		Status:        session.QueueStatusBlocked,
		SessionID:     meta.ID,
		SessionStatus: session.StatusAwaitingInput,
		LastError:     "child session is resumable: awaiting_input",
	})
	if err := engine.store.EnsureBackgroundNotification(parentMeta.ID, blockedNotification); err != nil {
		t.Fatalf("ensure blocked notification: %v", err)
	}
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "finish child")); err != nil {
		t.Fatalf("append child prompt: %v", err)
	}
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{
			ToolCalls:  []provider.ToolCall{{ID: "call_finish", Name: "finish", Arguments: json.RawMessage(`{"message":"child done"}`)}},
			StopReason: "tool_use",
		}, nil
	})

	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run child: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("expected completed child, got %#v", result)
	}
	job, err := engine.store.LoadJob(meta.QueueJobID)
	if err != nil {
		t.Fatalf("load reconciled job: %v", err)
	}
	if job.Status != session.QueueStatusCompleted || job.SessionStatus != session.StatusCompleted || job.FinalText != "child done" {
		t.Fatalf("expected completed queue job facts, got %#v", job)
	}
	coordination, err := engine.store.LoadParentCoordination(parentMeta.ID)
	if err != nil {
		t.Fatalf("load parent coordination: %v", err)
	}
	if containsString(coordination.UnresolvedQueueJobs, meta.QueueJobID) || !containsString(coordination.CompletedQueueJobs, meta.QueueJobID) || coordination.Parked {
		t.Fatalf("expected parent coordination resolved, got %#v", coordination)
	}
	notifications, err := engine.store.LoadBackgroundNotifications(parentMeta.ID)
	if err != nil {
		t.Fatalf("load parent notifications: %v", err)
	}
	if len(notifications) != 1 || notifications[0].Status != session.QueueStatusCompleted || notifications[0].FinalText != "child done" || notifications[0].DeliveryStatus != session.BackgroundNotificationPending {
		t.Fatalf("expected refreshed completed notification, got %#v", notifications)
	}
}

func TestEngineReportsLinkedQueueJobReconcileSaveError(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	parentMeta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "parent_queue_save_error",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
		RootSessionID:    "parent_queue_save_error",
	}
	if err := engine.store.Create(parentMeta, session.State{Status: session.StatusRunning, Phase: "turn_decide", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	meta.ParentSessionID = parentMeta.ID
	meta.RootSessionID = parentMeta.ID
	meta.QueueJobID = "job_queue_save_error"
	if err := engine.store.SaveMetadata(meta.ID, meta); err != nil {
		t.Fatalf("save child metadata: %v", err)
	}
	if err := engine.store.SaveJob(session.QueueJob{
		SchemaVersion:   1,
		ID:              meta.QueueJobID,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		Status:          session.QueueStatusBlocked,
		ParentSessionID: parentMeta.ID,
		RootSessionID:   parentMeta.ID,
		SessionID:       meta.ID,
		SessionStatus:   session.StatusAwaitingInput,
		Prompt:          "continue child",
		Mode:            session.ModeExec,
		Background:      true,
		LastError:       "child session is resumable: awaiting_input",
	}); err != nil {
		t.Fatalf("save blocked job: %v", err)
	}
	queuePath := filepath.Join(engine.store.Root(), "_queue", session.QueueStatusCompleted, meta.QueueJobID+".json")
	if err := os.MkdirAll(queuePath, 0o700); err != nil {
		t.Fatalf("block completed queue job path: %v", err)
	}
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "finish child")); err != nil {
		t.Fatalf("append child prompt: %v", err)
	}
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{
			ToolCalls:  []provider.ToolCall{{ID: "call_finish", Name: "finish", Arguments: json.RawMessage(`{"message":"child done"}`)}},
			StopReason: "tool_use",
		}, nil
	})

	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err == nil {
		t.Fatalf("expected queue job reconcile save error, got result %#v", result)
	}
	if !strings.Contains(err.Error(), meta.QueueJobID) && !strings.Contains(err.Error(), "_queue") {
		t.Fatalf("expected queue job reconciliation path error, got %v", err)
	}
}

func TestEngineAcceptsSteerAfterProviderDoneCandidateBoundary(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "initial")); err != nil {
		t.Fatalf("append: %v", err)
	}
	fake := provider.NewFake(
		func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
			if got := req.Messages[len(req.Messages)-1].Text; got != "initial" {
				t.Fatalf("expected initial prompt on first turn, got %q", got)
			}
			if err := engine.store.AppendSteerRequest(meta.ID, session.NewSteerRequest("focus on tests", false)); err != nil {
				t.Fatalf("append steer: %v", err)
			}
			return provider.TurnResult{Text: "first turn", StopReason: "done_candidate"}, nil
		},
		func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
			last := req.Messages[len(req.Messages)-1]
			if last.Role != "user" || last.Text != "focus on tests" {
				t.Fatalf("expected accepted steer message on second turn, got %#v", last)
			}
			return provider.TurnResult{Text: "second turn", StopReason: "done_candidate"}, nil
		},
	)
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusAwaitingInput {
		t.Fatalf("expected awaiting_input, got %s", result.Status)
	}
	requests, err := engine.store.LoadSteerRequests(meta.ID)
	if err != nil {
		t.Fatalf("load steer requests: %v", err)
	}
	if len(requests) != 1 || requests[0].Status != session.SteerStatusAccepted {
		t.Fatalf("expected accepted steer request, got %#v", requests)
	}
}

func TestEngineWritesInterruptedToolResultOnPause(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	registry.Register(tools.Definition{
		Name:        "slow",
		Description: "slow",
		InputSchema: map[string]any{"type": "object"},
		Execute: func(ctx context.Context, execCtx tools.ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			<-ctx.Done()
			return session.ToolResult{}, ctx.Err()
		},
	})
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "slow")); err != nil {
		t.Fatalf("append: %v", err)
	}
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{
			ToolCalls:  []provider.ToolCall{{ID: "call_1", Name: "slow", Arguments: json.RawMessage(`{}`)}},
			StopReason: "tool_use",
		}, nil
	})
	go func() {
		time.Sleep(50 * time.Millisecond)
		engine.control.requestPause()
	}()
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusPaused {
		t.Fatalf("expected paused, got %s", result.Status)
	}
	messages, err := engine.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	last := messages[len(messages)-1]
	if last.Role != "tool" || len(last.ToolResults) == 0 || !last.ToolResults[0].IsError {
		t.Fatalf("expected interrupted tool result, got %#v", last)
	}
	events, err := loadEvents(engine.store, meta.ID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if !hasEventType(events, "tool.before") || !hasEventType(events, "tool.interrupted") {
		t.Fatalf("expected tool lifecycle events, got %#v", events)
	}
}

func TestEnginePauseReportsPausedEventAppendError(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	registry.Register(tools.Definition{
		Name:        "slow",
		Description: "slow",
		InputSchema: map[string]any{"type": "object"},
		Execute: func(ctx context.Context, execCtx tools.ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			<-ctx.Done()
			return session.ToolResult{}, ctx.Err()
		},
	})
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "slow")); err != nil {
		t.Fatalf("append: %v", err)
	}
	eventsPath := filepath.Join(engine.store.SessionDir(meta.ID), "events.jsonl")
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{
			ToolCalls:  []provider.ToolCall{{ID: "call_1", Name: "slow", Arguments: json.RawMessage(`{}`)}},
			StopReason: "tool_use",
		}, nil
	})
	go func() {
		time.Sleep(50 * time.Millisecond)
		if err := os.Remove(eventsPath); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove events: %v", err)
			return
		}
		if err := os.Mkdir(eventsPath, 0o700); err != nil {
			t.Errorf("block events path: %v", err)
			return
		}
		engine.control.requestPause()
	}()

	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err == nil {
		t.Fatalf("expected session.paused event append error, got result=%#v", result)
	}
	if !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected events append error with path context, got %v", err)
	}
	if !strings.Contains(err.Error(), "session.paused") {
		t.Fatalf("expected paused event context, got %v", err)
	}
}

func TestEngineStopsAfterReplayCompleteToolResultsWhenRunContextCancelsTool(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	started := make(chan struct{})
	registry.Register(tools.Definition{
		Name:        "slow_cancel",
		Description: "slow cancel",
		InputSchema: map[string]any{"type": "object"},
		Execute: func(ctx context.Context, execCtx tools.ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			close(started)
			<-ctx.Done()
			return session.ToolResult{}, ctx.Err()
		},
	})
	laterExecuted := false
	registry.Register(tools.Definition{
		Name:        "later_tool",
		Description: "must not run after cancellation",
		InputSchema: map[string]any{"type": "object"},
		Execute: func(ctx context.Context, execCtx tools.ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			laterExecuted = true
			return session.ToolResult{Name: "later_tool", LLMOutput: "later", DisplayOutput: "later"}, nil
		},
	})
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "run slow then later")); err != nil {
		t.Fatalf("append: %v", err)
	}
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{
			ToolCalls: []provider.ToolCall{
				{ID: "call_slow", Name: "slow_cancel", Arguments: json.RawMessage(`{}`)},
				{ID: "call_later", Name: "later_tool", Arguments: json.RawMessage(`{}`)},
			},
			StopReason: "tool_use",
		}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-started
		cancel()
	}()
	result, err := engine.Run(ctx, meta, state, "", fake, catalog, registry, hookManager)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation error, got result=%#v err=%v", result, err)
	}
	if result.Status != session.StatusFailed {
		t.Fatalf("expected failed result after run context cancellation, got %#v", result)
	}
	if laterExecuted {
		t.Fatal("later tool executed after run context cancellation")
	}
	messages, err := engine.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	var toolMessages []session.Message
	for _, msg := range messages {
		if msg.Role == "tool" {
			toolMessages = append(toolMessages, msg)
		}
	}
	if len(toolMessages) != 1 {
		t.Fatalf("expected one replay-complete tool message, got %#v", toolMessages)
	}
	results := toolMessages[0].ToolResults
	if len(results) != 2 {
		t.Fatalf("expected interrupted current result plus synthetic later result, got %#v", results)
	}
	if results[0].ToolCallID != "call_slow" || !results[0].IsError || results[0].LLMOutput != "[Tool execution was interrupted]" {
		t.Fatalf("unexpected interrupted current result: %#v", results[0])
	}
	if results[1].ToolCallID != "call_later" || !results[1].IsError || !strings.Contains(results[1].LLMOutput, "before this call ran") {
		t.Fatalf("unexpected synthetic later result: %#v", results[1])
	}
}

func TestEnginePreservesDeadlineToolResultMetadata(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	registry.Register(tools.Definition{
		Name:        "deadline_tool",
		Description: "returns structured timeout metadata",
		InputSchema: map[string]any{"type": "object"},
		Execute: func(ctx context.Context, execCtx tools.ExecContext, raw json.RawMessage) (session.ToolResult, error) {
			return session.ToolResult{
				Name:          "deadline_tool",
				LLMOutput:     "[Tool execution timed out]",
				DisplayOutput: "[Tool execution timed out]",
				IsError:       true,
				Metadata: map[string]any{
					"timeout":    1,
					"exit_code":  -1,
					"raw_length": 0,
					"truncated":  false,
				},
			}, context.DeadlineExceeded
		},
	})
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "run timeout then finish")); err != nil {
		t.Fatalf("append: %v", err)
	}
	var calls int
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		calls++
		if calls == 1 {
			return provider.TurnResult{
				ToolCalls:  []provider.ToolCall{{ID: "call_timeout", Name: "deadline_tool", Arguments: json.RawMessage(`{}`)}},
				StopReason: "tool_use",
			}, nil
		}
		return provider.TurnResult{
			ToolCalls:  []provider.ToolCall{{ID: "call_finish", Name: "finish", Arguments: json.RawMessage(`{"message":"done"}`)}},
			StopReason: "tool_use",
		}, nil
	})
	if _, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager); err != nil {
		t.Fatalf("run: %v", err)
	}
	messages, err := engine.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	var timeoutResult *session.ToolResult
	for _, msg := range messages {
		for i := range msg.ToolResults {
			if msg.ToolResults[i].Name == "deadline_tool" {
				timeoutResult = &msg.ToolResults[i]
			}
		}
	}
	if timeoutResult == nil {
		t.Fatalf("expected deadline tool result in messages: %#v", messages)
	}
	if !timeoutResult.IsError || timeoutResult.DisplayOutput != "[Tool execution timed out]" {
		t.Fatalf("expected structured timeout result to be preserved, got %#v", timeoutResult)
	}
	if timeoutResult.Metadata["timeout"] != float64(1) && timeoutResult.Metadata["timeout"] != 1 {
		t.Fatalf("expected timeout metadata to survive engine dispatch, got %#v", timeoutResult.Metadata)
	}
}

func TestEngineDoesNotHardBlockNormalFinishOnStaleFeatureList(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	if err := os.WriteFile(filepath.Join(engine.store.SessionDir(meta.ID), "feature_list.json"), []byte(`{"features":[{"id":"feature_0001","status":"pending"}]}`), 0o600); err != nil {
		t.Fatalf("write feature list: %v", err)
	}
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "finish current scope")); err != nil {
		t.Fatalf("append: %v", err)
	}
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{
			ToolCalls:  []provider.ToolCall{{ID: "call_finish", Name: "finish", Arguments: json.RawMessage(`{"message":"done"}`)}},
			StopReason: "tool_use",
		}, nil
	})
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("expected normal exec finish not to be blocked by stale roadmap, got %#v", result)
	}
}

func TestEngineStillBlocksInitFinishOnIncompleteFeatureList(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeInit)
	if err := os.WriteFile(filepath.Join(engine.store.SessionDir(meta.ID), "feature_list.json"), []byte(`{"features":[{"id":"feature_0001","status":"pending"}]}`), 0o600); err != nil {
		t.Fatalf("write feature list: %v", err)
	}
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "finish init")); err != nil {
		t.Fatalf("append: %v", err)
	}
	var calls int
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		calls++
		if calls > 1 {
			return provider.TurnResult{Text: "blocked", StopReason: "done_candidate"}, nil
		}
		return provider.TurnResult{
			ToolCalls:  []provider.ToolCall{{ID: "call_finish", Name: "finish", Arguments: json.RawMessage(`{"message":"done"}`)}},
			StopReason: "tool_use",
		}, nil
	})
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status == session.StatusCompleted {
		t.Fatalf("expected init finish to be blocked by incomplete feature list")
	}
	messages, err := engine.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	var foundBlock bool
	for _, msg := range messages {
		for _, toolResult := range msg.ToolResults {
			if toolResult.Name == "finish" && strings.Contains(toolResult.DisplayOutput, "Pre-completion check failed") {
				foundBlock = true
			}
		}
	}
	if !foundBlock {
		t.Fatalf("expected pre-completion block result, got %#v", messages)
	}
}

func TestEngineTriggersSessionAndAssistantHooks(t *testing.T) {
	cfg := config.Default()
	cfg.Hooks.SessionStart = []config.HookDefinition{
		{
			Name:    "log-start",
			Command: []string{"/bin/sh", "-c", "cat >> .hook-session-start.json"},
		},
	}
	cfg.Hooks.SessionAwaiting = []config.HookDefinition{
		{
			Name:    "log-awaiting",
			Command: []string{"/bin/sh", "-c", "cat >> .hook-session-awaiting.json"},
		},
	}
	cfg.Hooks.AssistantMessage = []config.HookDefinition{
		{
			Name:   "assistant-prefix",
			Inject: &config.HookInject{Field: "text", Prefix: "[hook] "},
		},
	}
	engine, meta, state, registry, hookManager, catalog := newTestEngineWithConfig(t, cfg, session.ModeRun)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "hello")); err != nil {
		t.Fatalf("append: %v", err)
	}
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{Text: "done_candidate", StopReason: "done_candidate"}, nil
	})
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.FinalText != "[hook] done_candidate" {
		t.Fatalf("expected hooked final text, got %q", result.FinalText)
	}
	messages, err := engine.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if got := messages[len(messages)-1].Text; got != "[hook] done_candidate" {
		t.Fatalf("expected assistant hook to rewrite stored text, got %q", got)
	}
	for _, name := range []string{".hook-session-start.json", ".hook-session-awaiting.json"} {
		path := filepath.Join(meta.Workdir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if len(data) == 0 {
			t.Fatalf("expected hook payload in %s", path)
		}
	}
}

func TestEngineToolBeforeHookPayloadIncludesArguments(t *testing.T) {
	cfg := config.Default()
	cfg.Hooks.ToolBefore = []config.HookDefinition{
		{
			Name:    "log-tool-before",
			Command: []string{"/bin/sh", "-c", "cat >> .hook-tool-before.json"},
		},
	}
	engine, meta, state, registry, hookManager, catalog := newTestEngineWithConfig(t, cfg, session.ModeRun)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "finish")); err != nil {
		t.Fatalf("append: %v", err)
	}
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{
			ToolCalls:  []provider.ToolCall{{ID: "call_1", Name: "finish", Arguments: json.RawMessage(`{"message":"done"}`)}},
			StopReason: "tool_use",
		}, nil
	})
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("expected completed, got %s", result.Status)
	}
	data, err := os.ReadFile(filepath.Join(meta.Workdir, ".hook-tool-before.json"))
	if err != nil {
		t.Fatalf("read hook payload: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal hook payload: %v", err)
	}
	arguments, _ := payload["arguments"].(string)
	if !strings.Contains(arguments, `"message": "done"`) {
		t.Fatalf("expected hook payload to include tool arguments, got %q", arguments)
	}
}

func TestEngineToolBeforeHookCanRewriteArguments(t *testing.T) {
	cfg := config.Default()
	cfg.Hooks.ToolBefore = []config.HookDefinition{
		{
			Name:   "rewrite-finish-message",
			Match:  config.HookMatch{Tool: "finish"},
			Inject: &config.HookInject{Field: "arguments", Set: "{\"message\":\"hooked\"}"},
		},
	}
	engine, meta, state, registry, hookManager, catalog := newTestEngineWithConfig(t, cfg, session.ModeRun)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "finish")); err != nil {
		t.Fatalf("append: %v", err)
	}
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{
			ToolCalls:  []provider.ToolCall{{ID: "call_1", Name: "finish", Arguments: json.RawMessage(`{"message":"original"}`)}},
			StopReason: "tool_use",
		}, nil
	})
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("expected completed, got %s", result.Status)
	}
	if result.FinalText != "hooked" {
		t.Fatalf("expected rewritten finish message, got %q", result.FinalText)
	}
}

func TestEngineCompleteReportsCompletedEventAppendError(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "finish")); err != nil {
		t.Fatalf("append: %v", err)
	}
	eventsPath := filepath.Join(engine.store.SessionDir(meta.ID), "events.jsonl")
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		if err := os.Remove(eventsPath); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove events: %v", err)
		}
		if err := os.Mkdir(eventsPath, 0o700); err != nil {
			t.Fatalf("block events path: %v", err)
		}
		return provider.TurnResult{
			ToolCalls:  []provider.ToolCall{{ID: "call_1", Name: "finish", Arguments: json.RawMessage(`{"message":"done"}`)}},
			StopReason: "tool_use",
		}, nil
	})

	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err == nil {
		t.Fatalf("expected session.completed event append error, got result=%#v", result)
	}
	if !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected events append error with path context, got %v", err)
	}
	if !strings.Contains(err.Error(), "session.completed") {
		t.Fatalf("expected completed event context, got %v", err)
	}
}

func TestEngineMarksInterruptSteerDeferredWhenToolIgnoresCancel(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	release := make(chan struct{})
	registry.Register(tools.Definition{
		Name:        "slow",
		Description: "slow",
		InputSchema: map[string]any{"type": "object"},
		Execute: func(context.Context, tools.ExecContext, json.RawMessage) (session.ToolResult, error) {
			<-release
			return session.ToolResult{
				Name:          "slow",
				LLMOutput:     "slow done",
				DisplayOutput: "slow done",
			}, nil
		},
	})
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "slow")); err != nil {
		t.Fatalf("append: %v", err)
	}
	fake := provider.NewFake(
		func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
			return provider.TurnResult{
				ToolCalls:  []provider.ToolCall{{ID: "call_1", Name: "slow", Arguments: json.RawMessage(`{}`)}},
				StopReason: "tool_use",
			}, nil
		},
		func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
			last := req.Messages[len(req.Messages)-1]
			if last.Role != "user" || last.Text != "switch direction" {
				t.Fatalf("expected accepted steer message on second turn, got %#v", last)
			}
			return provider.TurnResult{Text: "done_candidate", StopReason: "done_candidate"}, nil
		},
	)
	go func() {
		time.Sleep(50 * time.Millisecond)
		if err := engine.store.AppendSteerRequest(meta.ID, session.NewSteerRequest("switch direction", true)); err != nil {
			t.Errorf("append steer: %v", err)
			return
		}
		engine.control.requestSteerInterrupt()
		time.Sleep(20 * time.Millisecond)
		close(release)
	}()
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusAwaitingInput {
		t.Fatalf("expected awaiting_input, got %s", result.Status)
	}
	events, err := loadEvents(engine.store, meta.ID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if !hasEventType(events, "session.steer.deferred") || !hasEventType(events, "session.steer.accepted") {
		t.Fatalf("expected deferred and accepted steer events, got %#v", events)
	}
	requests, err := engine.store.LoadSteerRequests(meta.ID)
	if err != nil {
		t.Fatalf("load steer requests: %v", err)
	}
	if len(requests) != 1 || requests[0].Status != session.SteerStatusAccepted {
		t.Fatalf("expected accepted steer request, got %#v", requests)
	}
}

func TestEngineEmitsContextLoadedEventWithDurableState(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	meta.RootSessionID = "root-role"
	meta.ParentSessionID = "parent-role"
	meta.AgentName = "reviewer"
	meta.AgentRole = "evaluator"
	if err := os.MkdirAll(filepath.Join(meta.Workdir, "reports"), 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	if err := os.WriteFile(filepath.Join(meta.Workdir, "reports", "spec.md"), []byte("# spec\n"), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	if err := engine.store.SaveTodo(meta.ID, []session.TodoItem{{
		Content:   "Refresh rollout plan",
		Status:    "in_progress",
		Priority:  "high",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}}); err != nil {
		t.Fatalf("save todo: %v", err)
	}
	if _, err := session.CreateTask(engine.store, meta.ID, session.TaskCreateInput{
		Subject:  "Verify durable task graph",
		Priority: "high",
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "finish after loading context")); err != nil {
		t.Fatalf("append: %v", err)
	}
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{
			ToolCalls:  []provider.ToolCall{{ID: "call_1", Name: "finish", Arguments: json.RawMessage(`{"message":"done"}`)}},
			StopReason: "tool_use",
		}, nil
	})
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("expected completed, got %s", result.Status)
	}
	events, err := loadEvents(engine.store, meta.ID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	evt, ok := findEventByType(events, "session.context.loaded")
	if !ok {
		t.Fatalf("expected session.context.loaded event, got %#v", events)
	}
	if evt.Data["todo_count"] != float64(1) || evt.Data["task_count"] != float64(1) || evt.Data["ready_task_count"] != float64(1) {
		t.Fatalf("expected durable counts in context event, got %#v", evt.Data)
	}
	present, _ := evt.Data["project_memory_present"].([]any)
	if len(present) != 1 || present[0] != filepath.Join("reports", "spec.md") {
		t.Fatalf("expected project-memory present path, got %#v", evt.Data["project_memory_present"])
	}
	if evt.Data["agent_role"] != "evaluator" || evt.Data["agent_name"] != "reviewer" {
		t.Fatalf("expected agent identity in context event, got %#v", evt.Data)
	}
	if evt.Data["root_session_id"] != "root-role" || evt.Data["parent_session_id"] != "parent-role" {
		t.Fatalf("expected parent/root identity in context event, got %#v", evt.Data)
	}
}

func newTestEngine(t *testing.T, mode string) (*Engine, session.SessionMetadata, session.State, *tools.Registry, *hooks.Manager, *skills.Catalog) {
	cfg := config.Default()
	cfg.Runtime.GuardrailsMode = "standard"
	return newTestEngineWithConfig(t, cfg, mode)
}

func newTestEngineWithSkill(t *testing.T, mode, skillName, skillDescription, skillBody string) (*Engine, session.SessionMetadata, session.State, *tools.Registry, *hooks.Manager, *skills.Catalog) {
	t.Helper()
	cfg := config.Default()
	cfg.Runtime.GuardrailsMode = "standard"
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", skillName)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	content := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n%s\n", skillName, skillDescription, skillBody)
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	cfg.Skills.Dirs = []string{filepath.Join(root, "skills")}
	return newTestEngineWithConfig(t, cfg, mode)
}

func newTestEngineWithConfig(t *testing.T, cfg *config.Config, mode string) (*Engine, session.SessionMetadata, session.State, *tools.Registry, *hooks.Manager, *skills.Catalog) {
	t.Helper()
	switch strings.ToLower(strings.TrimSpace(cfg.Runtime.GuardrailsMode)) {
	case "", "standard":
		cfg.Runtime.GuardrailsMode = "standard"
	case "yolo":
		cfg.Runtime.GuardrailsMode = "yolo"
	default:
		cfg.Runtime.GuardrailsMode = "standard"
	}
	cfg.Session.Dir = t.TempDir()
	cfg.Runtime.MaxTurnsHard = 4
	store := session.NewStore(cfg.Session.Dir)
	bus := events.NewBus()
	control := &runControl{}
	engine := NewEngine(cfg, store, bus, control)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             mode,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: completionPolicy(mode),
	}
	state := session.State{
		Status:    session.StatusRunning,
		Phase:     "prepare",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	catalog, err := skills.Scan(cfg.Skills.Dirs)
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("catalog: %v", err)
	}
	registry, err := tools.NewRegistry(cfg, catalog, store, nil)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	hookManager := hooks.New(cfg.Hooks, meta.Workdir)
	return engine, meta, state, registry, hookManager, catalog
}

func loadEvents(store *session.Store, sessionID string) ([]events.Event, error) {
	var out []events.Event
	path := filepath.Join(store.SessionDir(sessionID), "events.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	for _, line := range bytesSplitNonEmpty(data, '\n') {
		var evt events.Event
		if err := json.Unmarshal(line, &evt); err != nil {
			return nil, err
		}
		out = append(out, evt)
	}
	return out, nil
}

func hasEventType(events []events.Event, eventType string) bool {
	for _, evt := range events {
		if evt.Type == eventType {
			return true
		}
	}
	return false
}

func findEventByType(evts []events.Event, eventType string) (events.Event, bool) {
	for _, evt := range evts {
		if evt.Type == eventType {
			return evt, true
		}
	}
	return events.Event{}, false
}

func blockRuntimeGoalHistoryPath(t *testing.T, store *session.Store, sessionID string) {
	t.Helper()
	historyPath := filepath.Join(store.SessionDir(sessionID), "artifacts", "goal-history.jsonl")
	if err := os.Remove(historyPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove goal history: %v", err)
	}
	if err := os.Mkdir(historyPath, 0o700); err != nil {
		t.Fatalf("block goal history path: %v", err)
	}
}

func blockRuntimeProviderAttemptsPath(t *testing.T, store *session.Store, sessionID string) {
	t.Helper()
	attemptsPath := filepath.Join(store.SessionDir(sessionID), "provider-attempts.jsonl")
	if err := os.Remove(attemptsPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove provider attempts: %v", err)
	}
	if err := os.Mkdir(attemptsPath, 0o700); err != nil {
		t.Fatalf("block provider attempts path: %v", err)
	}
}

type emittingAdapter struct {
	run func(context.Context, provider.TurnRequest, provider.EmitFunc) (provider.TurnResult, error)
}

func (a emittingAdapter) Name() string {
	return "emitting"
}

func (a emittingAdapter) RunTurn(ctx context.Context, req provider.TurnRequest, emit provider.EmitFunc) (provider.TurnResult, error) {
	return a.run(ctx, req, emit)
}

func bytesSplitNonEmpty(data []byte, sep byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b != sep {
			continue
		}
		if i > start {
			out = append(out, append([]byte(nil), data[start:i]...))
		}
		start = i + 1
	}
	if start < len(data) {
		out = append(out, append([]byte(nil), data[start:]...))
	}
	return out
}
