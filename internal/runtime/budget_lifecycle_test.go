package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/events"
	"go-cli-agent/internal/provider"
	"go-cli-agent/internal/session"
	"go-cli-agent/internal/tools"
)

func TestGlobalHardTurnLimitAppliesToRootAndDirectChild(t *testing.T) {
	for _, tc := range []struct {
		name  string
		child bool
	}{
		{name: "root"},
		{name: "direct_child", child: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Runtime.GuardrailsMode = "standard"
			cfg.Runtime.ChildBudget = config.ChildBudgetConfig{}
			if tc.child {
				eng, childMeta, childState, reg, hooks, catalog := childEngineWithConfig(t, cfg)
				engine, meta, state := eng, childMeta, childState
				engine.cfg.Runtime.MaxTurnsHard = 1
				engine.cfg.Runtime.MaxTurnsSoft = 100
				reg.Register(budgetNoopTool())
				if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "keep working")); err != nil {
					t.Fatalf("append user: %v", err)
				}
				fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
					return provider.TurnResult{ToolCalls: []provider.ToolCall{{ID: session.NewQueueJobID(), Name: "noop", Arguments: json.RawMessage(`{}`)}}, StopReason: "tool_use"}, nil
				})
				result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, reg, hooks)
				assertHardTurnFailure(t, engine, result, err)
				return
			}

			eng, rootMeta, rootState, reg, hooks, catalog := newTestEngineWithConfig(t, cfg, session.ModeExec)
			engine, meta, state := eng, rootMeta, rootState
			engine.cfg.Runtime.MaxTurnsHard = 1
			engine.cfg.Runtime.MaxTurnsSoft = 100
			reg.Register(budgetNoopTool())
			if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "keep working")); err != nil {
				t.Fatalf("append user: %v", err)
			}
			fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
				return provider.TurnResult{ToolCalls: []provider.ToolCall{{ID: session.NewQueueJobID(), Name: "noop", Arguments: json.RawMessage(`{}`)}}, StopReason: "tool_use"}, nil
			})
			result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, reg, hooks)
			assertHardTurnFailure(t, engine, result, err)
		})
	}
}

func assertHardTurnFailure(t *testing.T, engine *Engine, result RunResult, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusFailed || result.LastError != "max_turns_hard_exceeded" {
		t.Fatalf("expected global hard turn failure, got %#v", result)
	}
	state, loadErr := engine.store.LoadState(result.SessionID)
	if loadErr != nil {
		t.Fatalf("load state: %v", loadErr)
	}
	if state.Status != session.StatusFailed || state.LastError != "max_turns_hard_exceeded" {
		t.Fatalf("unexpected durable hard turn state: %#v", state)
	}
}

func TestGlobalHardTurnLimitAppliesToQueueChild(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_, _ = io.ReadAll(r.Body)
		call := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"resp_%d","status":"completed","output":[{"type":"function_call","call_id":"call_%d","name":"read_file","arguments":"{\"path\":\"missing-%d.txt\"}"}],"usage":{"input_tokens":1,"output_tokens":1}}`, call, call, call)
	}))
	defer server.Close()

	cfg := testRuntimeConfig(t)
	providerCfg := cfg.Providers["openai-compatible"]
	providerCfg.BaseURL = server.URL
	cfg.Providers["openai-compatible"] = providerCfg
	cfg.Runtime.MaxTurnsHard = 1
	cfg.Runtime.MaxTurnsSoft = 100
	cfg.Runtime.ChildBudget = config.ChildBudgetConfig{}
	runner := NewRunner(cfg)
	parentID := createParentSession(t, runner.store, t.TempDir())
	job, err := runner.QueueSubmit(context.Background(), QueueSubmitRequest{ParentSessionID: parentID, Prompt: "keep working", IsolationMode: "off"})
	if err != nil {
		t.Fatalf("queue submit: %v", err)
	}
	processed, ok, err := runner.ProcessNextJob(context.Background())
	if err != nil || !ok {
		t.Fatalf("process queue job: ok=%t err=%v job=%#v", ok, err, processed)
	}
	if processed.ID != job.ID || processed.Status != session.QueueStatusFailed || processed.SessionStatus != session.StatusFailed || processed.LastError != "max_turns_hard_exceeded" {
		t.Fatalf("expected queue child global hard turn failure, got %#v", processed)
	}
}

func TestGlobalHardTurnLimitResetsForEachEngineRun(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.GuardrailsMode = "standard"
	cfg.Runtime.ChildBudget = config.ChildBudgetConfig{}
	engine, meta, state, registry, hookManager, catalog := newTestEngineWithConfig(t, cfg, session.ModeExec)
	engine.cfg.Runtime.MaxTurnsHard = 1
	engine.cfg.Runtime.MaxTurnsSoft = 100
	registry.Register(budgetNoopTool())
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "run twice")); err != nil {
		t.Fatalf("append user: %v", err)
	}
	turn := func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{
			ToolCalls:  []provider.ToolCall{{ID: session.NewQueueJobID(), Name: "noop", Arguments: json.RawMessage(`{}`)}},
			StopReason: "tool_use",
		}, nil
	}
	first, err := engine.Run(context.Background(), meta, state, "", provider.NewFake(turn), catalog, registry, hookManager)
	assertHardTurnFailure(t, engine, first, err)
	firstState, err := engine.store.LoadState(meta.ID)
	// A tool result at the configured boundary retains the existing single
	// resolution-turn allowance; the accounting reset being tested here is the
	// start of a new Engine.Run, not removal of that compatibility behavior.
	if err != nil || firstState.Turn != 2 {
		t.Fatalf("unexpected first-run turn state: %#v err=%v", firstState, err)
	}
	second, err := engine.Run(context.Background(), meta, firstState, "", provider.NewFake(turn), catalog, registry, hookManager)
	assertHardTurnFailure(t, engine, second, err)
	secondState, err := engine.store.LoadState(meta.ID)
	if err != nil || secondState.Turn != 4 {
		t.Fatalf("hard limit must reset and allow a fresh bounded run: %#v err=%v", secondState, err)
	}
}

func TestUnlimitedGlobalTurnLimitDoesNotStopRootOrChild(t *testing.T) {
	for _, tc := range []struct {
		name  string
		child bool
	}{
		{name: "root"},
		{name: "child", child: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Runtime.GuardrailsMode = "standard"
			cfg.Runtime.ChildBudget = config.ChildBudgetConfig{}
			cfg.Runtime.Degeneration.ReminderAfter = 2
			cfg.Runtime.Degeneration.GiveUpAfter = 3
			if tc.child {
				eng, childMeta, childState, reg, hooks, catalog := childEngineWithConfig(t, cfg)
				engine, meta, state := eng, childMeta, childState
				engine.cfg.Runtime.MaxTurnsHard = -1
				engine.cfg.Runtime.MaxTurnsSoft = 100
				if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "finish after several turns")); err != nil {
					t.Fatalf("append user: %v", err)
				}
				calls := 0
				turn := func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
					calls++
					return provider.TurnResult{Text: "still working", StopReason: "done_candidate"}, nil
				}
				fake := provider.NewFake(repeatFakeTurns(3, turn)...)
				result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, reg, hooks)
				if err != nil || result.Status != session.StatusFailed || result.LastError == "max_turns_hard_exceeded" || calls < 3 {
					t.Fatalf("unlimited child run: calls=%d result=%#v err=%v", calls, result, err)
				}
				return
			}

			eng, rootMeta, rootState, reg, hooks, catalog := newTestEngineWithConfig(t, cfg, session.ModeExec)
			engine, meta, state := eng, rootMeta, rootState
			engine.cfg.Runtime.MaxTurnsHard = -1
			engine.cfg.Runtime.MaxTurnsSoft = 100
			if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "finish after several turns")); err != nil {
				t.Fatalf("append user: %v", err)
			}
			calls := 0
			turn := func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
				calls++
				return provider.TurnResult{Text: "still working", StopReason: "done_candidate"}, nil
			}
			fake := provider.NewFake(repeatFakeTurns(3, turn)...)
			result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, reg, hooks)
			if err != nil || result.Status != session.StatusFailed || result.LastError == "max_turns_hard_exceeded" || calls < 3 {
				t.Fatalf("unlimited root run: calls=%d result=%#v err=%v", calls, result, err)
			}
		})
	}
}

func TestChildBudgetAndGlobalHardLimitKeepWinningReason(t *testing.T) {
	for _, tc := range []struct {
		name       string
		hardTurns  int
		childTurns int
		wantStatus string
		wantReason string
	}{
		{name: "child_budget_first", hardTurns: 5, childTurns: 1, wantStatus: session.StatusPaused, wantReason: session.ChildBudgetTurnsExceededReason},
		{name: "global_hard_first", hardTurns: 1, childTurns: 5, wantStatus: session.StatusFailed, wantReason: "max_turns_hard_exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Runtime.GuardrailsMode = "standard"
			cfg.Runtime.ChildBudget.MaxTurnsPerAttempt = tc.childTurns
			engine, meta, state, reg, hooks, catalog := childEngineWithConfig(t, cfg)
			engine.cfg.Runtime.MaxTurnsHard = tc.hardTurns
			engine.cfg.Runtime.MaxTurnsSoft = 100
			reg.Register(budgetNoopTool())
			if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "keep working")); err != nil {
				t.Fatalf("append user: %v", err)
			}
			fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
				return provider.TurnResult{ToolCalls: []provider.ToolCall{{ID: session.NewQueueJobID(), Name: "noop", Arguments: json.RawMessage(`{}`)}}, StopReason: "tool_use"}, nil
			})
			result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, reg, hooks)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if result.Status != tc.wantStatus {
				t.Fatalf("expected status %s, got %#v", tc.wantStatus, result)
			}
			persisted, err := engine.store.LoadState(meta.ID)
			if err != nil {
				t.Fatalf("load state: %v", err)
			}
			gotReason := persisted.PauseReason
			if tc.wantStatus == session.StatusFailed {
				gotReason = persisted.LastError
			}
			if gotReason != tc.wantReason {
				t.Fatalf("expected winning reason %q, got state %#v", tc.wantReason, persisted)
			}
		})
	}
}

func TestSoftTurnCheckpointWritesOnceAndDoesNotStop(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.GuardrailsMode = "standard"
	engine, meta, state, reg, hooks, catalog := newTestEngineWithConfig(t, cfg, session.ModeExec)
	engine.cfg.Runtime.MaxTurnsSoft = 1
	engine.cfg.Runtime.MaxTurnsHard = -1
	engine.cfg.Runtime.Degeneration.ReminderAfter = 2
	engine.cfg.Runtime.Degeneration.GiveUpAfter = 3
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "finish after checkpoint")); err != nil {
		t.Fatalf("append user: %v", err)
	}
	calls := 0
	turn := func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		calls++
		return provider.TurnResult{Text: "still working", StopReason: "done_candidate"}, nil
	}
	fake := provider.NewFake(repeatFakeTurns(3, turn)...)
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, reg, hooks)
	if err != nil || result.Status != session.StatusFailed || result.LastError == "max_turns_hard_exceeded" {
		t.Fatalf("soft checkpoint run: result=%#v err=%v", result, err)
	}
	eventsList, err := engine.store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	if countEventType(eventsList, "session.turn_limit.soft_reached") != 1 {
		t.Fatalf("soft checkpoint must be emitted once per run, events=%#v", eventsList)
	}
	if countHarnessReminderSource(t, engine.store, meta.ID, "max_turns_soft_reached") != 1 {
		t.Fatal("soft checkpoint reminder must be written exactly once")
	}
}

func TestChildActiveRuntimeDeadlineCancelsProviderCall(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.GuardrailsMode = "standard"
	cfg.Runtime.ChildBudget.MaxActiveRuntimeSec = 1
	cfg.Runtime.ChildBudget.MaxElapsedSec = 10
	engine, meta, state, reg, hooks, catalog := childEngineWithConfig(t, cfg)
	engine.cfg.Runtime.MaxTurnsHard = -1
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "wait")); err != nil {
		t.Fatalf("append user: %v", err)
	}
	fake := provider.NewFake(func(ctx context.Context, _ provider.TurnRequest) (provider.TurnResult, error) {
		<-ctx.Done()
		return provider.TurnResult{}, ctx.Err()
	})
	started := time.Now()
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, reg, hooks)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("active runtime deadline did not preempt provider promptly: %v", elapsed)
	}
	assertBudgetPauseReason(t, engine.store, result, session.ChildBudgetActiveRuntimeExceededReason)
}

func TestChildAbsoluteDeadlineCancelsProviderCall(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.GuardrailsMode = "standard"
	cfg.Runtime.ChildBudget.MaxActiveRuntimeSec = 10
	cfg.Runtime.ChildBudget.MaxElapsedSec = 1
	engine, meta, state, reg, hooks, catalog := childEngineWithConfig(t, cfg)
	engine.cfg.Runtime.MaxTurnsHard = -1
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "wait")); err != nil {
		t.Fatalf("append user: %v", err)
	}
	fake := provider.NewFake(func(ctx context.Context, _ provider.TurnRequest) (provider.TurnResult, error) {
		<-ctx.Done()
		return provider.TurnResult{}, ctx.Err()
	})
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, reg, hooks)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	assertBudgetPauseReason(t, engine.store, result, session.ChildBudgetAbsoluteDeadlineExceededReason)
}

func TestChildBudgetReasonWinsWrappedProviderTimeout(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.GuardrailsMode = "standard"
	cfg.Runtime.ProviderAutoResume.Enabled = false
	cfg.Runtime.ChildBudget.MaxElapsedSec = 1
	engine, meta, state, reg, hooks, catalog := childEngineWithConfig(t, cfg)
	engine.cfg.Runtime.MaxTurnsHard = -1
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "wait")); err != nil {
		t.Fatalf("append user: %v", err)
	}
	fake := provider.NewFake(func(ctx context.Context, _ provider.TurnRequest) (provider.TurnResult, error) {
		<-ctx.Done()
		return provider.TurnResult{}, &provider.HTTPError{
			Provider:    "openai",
			Class:       "upstream_timeout",
			Message:     context.DeadlineExceeded.Error(),
			TimeoutKind: "request_timeout",
		}
	})
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, reg, hooks)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	assertBudgetPauseReason(t, engine.store, result, session.ChildBudgetAbsoluteDeadlineExceededReason)
	if countHarnessReminderSource(t, engine.store, meta.ID, "provider_auto_resume") != 0 {
		t.Fatal("child budget cancellation must not add a provider auto-resume reminder")
	}
}

func TestQueueChildAbsoluteDeadlineWinsRealHTTPProviderTimeoutMatrix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_, _ = io.ReadAll(r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer server.Close()

	for _, tc := range []struct {
		name           string
		retryTransport bool
		autoResume     bool
	}{
		{name: "retry_off_auto_resume_off"},
		{name: "retry_on_auto_resume_off", retryTransport: true},
		{name: "retry_off_auto_resume_on", autoResume: true},
		{name: "retry_on_auto_resume_on", retryTransport: true, autoResume: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testRuntimeConfig(t)
			providerCfg := cfg.Providers["openai-compatible"]
			providerCfg.BaseURL = server.URL
			providerCfg.RequestTimeoutSec = 30
			providerCfg.Retry = config.Retry{
				MaxAttempts:    1,
				BaseDelayMS:    1,
				RetryTransport: tc.retryTransport,
			}
			if tc.retryTransport {
				providerCfg.Retry.MaxAttempts = 2
			}
			cfg.Providers["openai-compatible"] = providerCfg
			cfg.Runtime.MaxTurnsHard = -1
			cfg.Runtime.ProviderAutoResume.Enabled = tc.autoResume
			cfg.Runtime.ProviderAutoResume.MaxAttempts = 2
			cfg.Runtime.ChildBudget = config.ChildBudgetConfig{MaxElapsedSec: 1}
			runner := NewRunner(cfg)
			parentID := createParentSession(t, runner.store, t.TempDir())
			job, err := runner.QueueSubmit(context.Background(), QueueSubmitRequest{
				ParentSessionID: parentID,
				Prompt:          "wait until the absolute budget expires",
				IsolationMode:   "off",
			})
			if err != nil {
				t.Fatalf("queue submit: %v", err)
			}
			processed, ok, err := runner.ProcessNextJob(context.Background())
			if err != nil || !ok {
				t.Fatalf("process queue child: ok=%t err=%v job=%#v", ok, err, processed)
			}
			if processed.ID != job.ID || processed.Status != session.QueueStatusBlocked || processed.SessionStatus != session.StatusPaused {
				t.Fatalf("absolute deadline must block a paused child: %#v", processed)
			}
			state, err := runner.store.LoadState(processed.SessionID)
			if err != nil {
				t.Fatalf("load child state: %v", err)
			}
			if state.PauseReason != session.ChildBudgetAbsoluteDeadlineExceededReason {
				t.Fatalf("unexpected child deadline result: %#v", state)
			}
			eventsList, err := runner.store.LoadEvents(processed.SessionID)
			if err != nil {
				t.Fatalf("load child events: %v", err)
			}
			if countEventType(eventsList, "provider.auto_resume") != 0 || countHarnessReminderSource(t, runner.store, processed.SessionID, "provider_auto_resume") != 0 {
				t.Fatalf("child budget cancellation must bypass provider auto-resume: %#v", eventsList)
			}
		})
	}
}

func TestChildActiveRuntimeDeadlineCancelsShellProcessGroup(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.GuardrailsMode = "standard"
	cfg.Runtime.ChildBudget.MaxActiveRuntimeSec = 1
	engine, meta, state, reg, hooks, catalog := childEngineWithConfig(t, cfg)
	engine.cfg.Runtime.MaxTurnsHard = -1
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "run slow shell")); err != nil {
		t.Fatalf("append user: %v", err)
	}
	latePath := filepath.Join(meta.Workdir, "budget-shell-late.txt")
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		return provider.TurnResult{ToolCalls: []provider.ToolCall{{
			ID:        "shell_budget",
			Name:      "shell",
			Arguments: json.RawMessage(`{"command":"sleep 3; printf late > budget-shell-late.txt"}`),
		}}, StopReason: "tool_use"}, nil
	})
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, reg, hooks)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	assertBudgetPauseReason(t, engine.store, result, session.ChildBudgetActiveRuntimeExceededReason)
	time.Sleep(350 * time.Millisecond)
	if _, statErr := os.Stat(latePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("cancelled shell process group wrote late artifact: %v", statErr)
	}
}

func TestOperationDeadlineEarlierThanChildBudgetRemainsProviderFailure(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.GuardrailsMode = "standard"
	cfg.Runtime.ChildBudget.MaxActiveRuntimeSec = 5
	engine, meta, state, reg, hooks, catalog := childEngineWithConfig(t, cfg)
	engine.cfg.Runtime.MaxTurnsHard = -1
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "provider timeout")); err != nil {
		t.Fatalf("append user: %v", err)
	}
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		time.Sleep(20 * time.Millisecond)
		return provider.TurnResult{}, context.DeadlineExceeded
	})
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, reg, hooks)
	if err == nil {
		t.Fatalf("expected provider deadline error, got %#v", result)
	}
	persisted, loadErr := engine.store.LoadState(meta.ID)
	if loadErr != nil {
		t.Fatalf("load state: %v", loadErr)
	}
	if persisted.Status != session.StatusFailed || session.IsChildBudgetPauseReason(persisted.PauseReason) {
		t.Fatalf("operation timeout must not be rewritten as child budget pause: %#v", persisted)
	}
}

func TestRealHTTPProviderTimeoutBeforeChildDeadlineRemainsFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_, _ = io.ReadAll(r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Runtime.GuardrailsMode = "standard"
	cfg.Runtime.ProviderAutoResume.Enabled = false
	cfg.Runtime.ChildBudget.MaxElapsedSec = 1
	engine, meta, state, reg, hooks, catalog := childEngineWithConfig(t, cfg)
	engine.cfg.Runtime.MaxTurnsHard = -1
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "provider timeout first")); err != nil {
		t.Fatalf("append user: %v", err)
	}
	adapter := provider.NewOpenAIWithRetry(server.URL, "key", server.Client(), provider.RetryConfig{
		MaxAttempts:    1,
		RequestTimeout: 20 * time.Millisecond,
	})
	result, err := engine.Run(context.Background(), meta, state, "", adapter, catalog, reg, hooks)
	if err == nil {
		t.Fatalf("expected provider timeout failure, got %#v", result)
	}
	persisted, loadErr := engine.store.LoadState(meta.ID)
	if loadErr != nil {
		t.Fatalf("load state: %v", loadErr)
	}
	if persisted.Status != session.StatusFailed || session.IsChildBudgetPauseReason(persisted.PauseReason) {
		t.Fatalf("provider-owned timeout must win before child deadline: %#v", persisted)
	}
}

func TestPausedOfflineTimeDoesNotConsumeChildActiveRuntime(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.GuardrailsMode = "standard"
	cfg.Runtime.ChildBudget.MaxActiveRuntimeSec = 5
	engine, meta, state, reg, hooks, catalog := childEngineWithConfig(t, cfg)
	engine.cfg.Runtime.MaxTurnsHard = -1
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "park once")); err != nil {
		t.Fatalf("append user: %v", err)
	}
	fake := provider.NewFake(
		func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
			return provider.TurnResult{ToolCalls: []provider.ToolCall{{ID: "await_once", Name: "await_input", Arguments: json.RawMessage(`{"kind":"external_wait","reason":"wait","resume_condition":"continue"}`)}}, StopReason: "tool_use"}, nil
		},
		func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
			return provider.TurnResult{ToolCalls: []provider.ToolCall{{ID: "await_after_wait", Name: "await_input", Arguments: json.RawMessage(`{"kind":"external_wait","reason":"wait again","resume_condition":"continue"}`)}}, StopReason: "tool_use"}, nil
		},
	)
	first, err := engine.Run(context.Background(), meta, state, "", fake, catalog, reg, hooks)
	if err != nil || first.Status != session.StatusAwaitingInput {
		t.Fatalf("first run: result=%#v err=%v", first, err)
	}
	meta, err = engine.store.LoadMetadata(meta.ID)
	if err != nil {
		t.Fatalf("load metadata: %v", err)
	}
	meta.EffectiveBudget.AttemptStartedAt = time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339Nano)
	if err := engine.store.SaveMetadata(meta.ID, meta); err != nil {
		t.Fatalf("backdate paused attempt marker: %v", err)
	}
	state, err = engine.store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	second, err := engine.Run(context.Background(), meta, state, "", fake, catalog, reg, hooks)
	if err != nil || second.Status != session.StatusAwaitingInput {
		t.Fatalf("second run: result=%#v err=%v", second, err)
	}
	meta, err = engine.store.LoadMetadata(meta.ID)
	if err != nil {
		t.Fatalf("reload metadata: %v", err)
	}
	if meta.EffectiveBudget.TotalActiveRuntimeMS >= int64((10*time.Minute)/time.Millisecond) {
		t.Fatalf("paused/offline interval leaked into active runtime: %#v", meta.EffectiveBudget)
	}
}

func TestChildActiveRuntimeCheckpointUpdatesRunningSessionAndLinkedJob(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.GuardrailsMode = "standard"
	cfg.Runtime.MaxTurnsHard = -1
	cfg.Runtime.ChildBudget = config.ChildBudgetConfig{
		MaxActiveRuntimeSec:       5,
		ActiveRuntimeCheckpointMS: 100,
	}
	engine, meta, state, registry, hookManager, catalog := childEngineWithConfig(t, cfg)
	job := attachRunningBudgetJob(t, engine.store, &meta)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "wait for checkpoint")); err != nil {
		t.Fatalf("append user: %v", err)
	}

	started := make(chan struct{}, 1)
	fake := provider.NewFake(func(ctx context.Context, _ provider.TurnRequest) (provider.TurnResult, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return provider.TurnResult{}, ctx.Err()
	})
	type runOutcome struct {
		result RunResult
		err    error
	}
	done := make(chan runOutcome, 1)
	go func() {
		result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
		done <- runOutcome{result: result, err: err}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}

	var liveBudget *session.EffectiveBudget
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		loadedMeta, err := engine.store.LoadMetadata(meta.ID)
		if err != nil {
			t.Fatalf("load running child metadata: %v", err)
		}
		loadedJob, err := engine.store.LoadJob(job.ID)
		if err != nil {
			t.Fatalf("load running queue job: %v", err)
		}
		if loadedMeta.EffectiveBudget != nil && loadedMeta.EffectiveBudget.UsedActiveRuntimeMS >= 100 &&
			loadedMeta.EffectiveBudget.ActiveRuntimeLeaseOpen && loadedMeta.EffectiveBudget.ActiveRuntimeCheckpointAt != "" &&
			loadedJob.EffectiveBudget != nil && loadedJob.EffectiveBudget.UsedActiveRuntimeMS == loadedMeta.EffectiveBudget.UsedActiveRuntimeMS &&
			loadedJob.EffectiveBudget.ActiveRuntimeCheckpointAt == loadedMeta.EffectiveBudget.ActiveRuntimeCheckpointAt {
			liveBudget = session.CloneEffectiveBudget(loadedMeta.EffectiveBudget)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if liveBudget == nil {
		t.Fatal("running active-runtime checkpoint did not reach the session and linked job")
	}
	if liveBudget.ActiveRuntimeCheckpointIntervalMS != 100 || liveBudget.ActiveRuntimeLeaseOwner == "" {
		t.Fatalf("unexpected running checkpoint telemetry: %#v", liveBudget)
	}

	engine.control.requestPauseWithReason("manual_stop")
	select {
	case outcome := <-done:
		if outcome.err != nil || outcome.result.Status != session.StatusPaused {
			t.Fatalf("stop checkpointed child: result=%#v err=%v", outcome.result, outcome.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("checkpointed child did not stop")
	}
	finalMeta, err := engine.store.LoadMetadata(meta.ID)
	if err != nil {
		t.Fatalf("load settled child metadata: %v", err)
	}
	finalJob, err := engine.store.LoadJob(job.ID)
	if err != nil {
		t.Fatalf("load settled queue job: %v", err)
	}
	if finalMeta.EffectiveBudget == nil || finalMeta.EffectiveBudget.ActiveRuntimeLeaseOpen || finalMeta.EffectiveBudget.ActiveRuntimeLeaseOwner != "" || finalMeta.EffectiveBudget.UsedActiveRuntimeMS < liveBudget.UsedActiveRuntimeMS {
		t.Fatalf("graceful settlement did not close and settle active-runtime lease: before=%#v after=%#v", liveBudget, finalMeta.EffectiveBudget)
	}
	if finalJob.EffectiveBudget == nil || finalJob.EffectiveBudget.ActiveRuntimeLeaseOpen || finalJob.EffectiveBudget.UsedActiveRuntimeMS != finalMeta.EffectiveBudget.UsedActiveRuntimeMS {
		t.Fatalf("linked job did not receive final active-runtime settlement: session=%#v job=%#v", finalMeta.EffectiveBudget, finalJob.EffectiveBudget)
	}
}

func TestChildActiveRuntimeCrashRecoveryChargesOneBoundedIntervalPerOpenLease(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.ChildBudget = config.ChildBudgetConfig{
		MaxActiveRuntimeSec:       5,
		ActiveRuntimeCheckpointMS: 100,
	}
	engine, meta, state, _, _, _ := childEngineWithConfig(t, cfg)
	now := time.Now().UTC()
	budget := newEffectiveChildBudget(cfg, session.BudgetSourceRuntimeChild, state.Turn, now.Add(-time.Hour))
	budget.UsedActiveRuntimeMS = 250
	budget.TotalActiveRuntimeMS = 250
	budget.ActiveRuntimeCheckpointAt = now.Add(-time.Hour).Format(time.RFC3339Nano)
	budget.ActiveRuntimeLeaseOpen = true
	budget.ActiveRuntimeLeaseOwner = "dead-owner-1"
	session.RefreshEffectiveBudget(budget, state.Turn)
	meta.EffectiveBudget = budget
	if err := engine.store.SaveMetadata(meta.ID, meta); err != nil {
		t.Fatalf("seed crashed active lease: %v", err)
	}

	_, cancelFirst, _, err := engine.beginChildBudgetRun(context.Background(), meta, state)
	if err != nil {
		t.Fatalf("recover first crashed lease: %v", err)
	}
	cancelFirst()
	first, err := engine.store.LoadMetadata(meta.ID)
	if err != nil {
		t.Fatalf("load first recovery: %v", err)
	}
	if first.EffectiveBudget == nil || first.EffectiveBudget.UsedActiveRuntimeMS != 350 || first.EffectiveBudget.ActiveRuntimeLastRecoveryMS != 100 || !first.EffectiveBudget.ActiveRuntimeLeaseOpen || first.EffectiveBudget.ActiveRuntimeLeaseOwner == "dead-owner-1" {
		t.Fatalf("first recovery must charge exactly one interval and open a new lease: %#v", first.EffectiveBudget)
	}

	_, cancelSecond, secondRun, err := engine.beginChildBudgetRun(context.Background(), first, state)
	if err != nil {
		t.Fatalf("recover second crashed lease: %v", err)
	}
	cancelSecond()
	second, err := engine.store.LoadMetadata(meta.ID)
	if err != nil {
		t.Fatalf("load second recovery: %v", err)
	}
	if second.EffectiveBudget == nil || second.EffectiveBudget.UsedActiveRuntimeMS != 450 || second.EffectiveBudget.TotalActiveRuntimeMS != 450 || second.EffectiveBudget.ActiveRuntimeLastRecoveryMS != 100 || !second.EffectiveBudget.ActiveRuntimeLeaseOpen {
		t.Fatalf("repeated crash must pay another bounded interval without counting offline wall time: %#v", second.EffectiveBudget)
	}
	if _, err := secondRun.finish(state.Turn, ""); err != nil {
		t.Fatalf("close recovered test lease: %v", err)
	}
	eventsList, err := engine.store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load recovery events: %v", err)
	}
	if countEventType(eventsList, "session.child_budget.active_runtime_recovered") != 2 {
		t.Fatalf("expected one durable event per recovered open lease, events=%#v", eventsList)
	}
}

func TestRecoveredActiveRuntimeExhaustionPausesBeforeProviderCall(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.GuardrailsMode = "standard"
	cfg.Runtime.MaxTurnsHard = -1
	cfg.Runtime.ChildBudget = config.ChildBudgetConfig{
		MaxActiveRuntimeSec:       1,
		ActiveRuntimeCheckpointMS: 100,
	}
	engine, meta, state, registry, hookManager, catalog := childEngineWithConfig(t, cfg)
	now := time.Now().UTC()
	budget := newEffectiveChildBudget(cfg, session.BudgetSourceRuntimeChild, state.Turn, now)
	budget.MaxActiveRuntimeMS = 150
	budget.UsedActiveRuntimeMS = 100
	budget.TotalActiveRuntimeMS = 100
	budget.ActiveRuntimeCheckpointAt = now.Add(-time.Hour).Format(time.RFC3339Nano)
	budget.ActiveRuntimeLeaseOpen = true
	budget.ActiveRuntimeLeaseOwner = "dead-owner"
	session.RefreshEffectiveBudget(budget, state.Turn)
	meta.EffectiveBudget = budget
	if err := engine.store.SaveMetadata(meta.ID, meta); err != nil {
		t.Fatalf("seed nearly exhausted crashed lease: %v", err)
	}
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "must not call provider")); err != nil {
		t.Fatalf("append user: %v", err)
	}
	var providerCalls atomic.Int64
	fake := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		providerCalls.Add(1)
		return provider.TurnResult{}, nil
	})
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run recovered exhausted child: %v", err)
	}
	assertBudgetPauseReason(t, engine.store, result, session.ChildBudgetActiveRuntimeExceededReason)
	if providerCalls.Load() != 0 {
		t.Fatalf("recovery-exhausted budget must pause before provider call, calls=%d", providerCalls.Load())
	}
}

func TestActiveRuntimeCheckpointPersistenceFailureCancelsProviderAndFailsClosed(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.GuardrailsMode = "standard"
	cfg.Runtime.MaxTurnsHard = -1
	cfg.Runtime.ChildBudget = config.ChildBudgetConfig{
		MaxActiveRuntimeSec:       5,
		ActiveRuntimeCheckpointMS: 100,
	}
	engine, meta, state, registry, hookManager, catalog := childEngineWithConfig(t, cfg)
	job := attachRunningBudgetJob(t, engine.store, &meta)
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "checkpoint failure")); err != nil {
		t.Fatalf("append user: %v", err)
	}
	started := make(chan struct{}, 1)
	fake := provider.NewFake(func(ctx context.Context, _ provider.TurnRequest) (provider.TurnResult, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return provider.TurnResult{}, ctx.Err()
	})
	type runOutcome struct {
		result RunResult
		err    error
	}
	done := make(chan runOutcome, 1)
	go func() {
		result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
		done <- runOutcome{result: result, err: err}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}
	if err := engine.store.DeleteJob(job.ID); err != nil {
		t.Fatalf("delete linked job to inject checkpoint persistence failure: %v", err)
	}
	select {
	case outcome := <-done:
		if outcome.result.Status != session.StatusFailed || outcome.err == nil || !strings.Contains(outcome.err.Error(), "persist child active-runtime checkpoint") {
			t.Fatalf("checkpoint persistence failure must fail closed: result=%#v err=%v", outcome.result, outcome.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("checkpoint persistence failure did not cancel provider")
	}
	persistedState, err := engine.store.LoadState(meta.ID)
	if err != nil || persistedState.Status != session.StatusFailed || !strings.Contains(persistedState.LastError, "persist child active-runtime checkpoint") {
		t.Fatalf("checkpoint failure was not persisted as session failure: state=%#v err=%v", persistedState, err)
	}
	if engine.control.consumePause() {
		t.Fatal("handled checkpoint failure left a stale pause request for the next run")
	}
	eventsList, err := engine.store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load checkpoint failure events: %v", err)
	}
	if countEventType(eventsList, "session.child_budget.active_runtime_checkpoint_failed") != 1 {
		t.Fatalf("missing checkpoint failure event: %#v", eventsList)
	}
}

func TestActiveRuntimeSettlementFailurePreventsCompletedTerminalFact(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.GuardrailsMode = "standard"
	cfg.Runtime.ChildBudget = config.ChildBudgetConfig{
		MaxActiveRuntimeSec:       5,
		ActiveRuntimeCheckpointMS: config.MaxChildBudgetActiveRuntimeCheckpointMS,
	}
	engine, meta, state, _, hookManager, _ := childEngineWithConfig(t, cfg)
	job := attachRunningBudgetJob(t, engine.store, &meta)
	meta.EffectiveBudget = newEffectiveChildBudget(cfg, session.BudgetSourceRuntimeChild, state.Turn, time.Now().UTC())
	if err := engine.store.SaveMetadata(meta.ID, meta); err != nil {
		t.Fatalf("save terminal settlement budget: %v", err)
	}
	runCtx, cancel, _, err := engine.beginChildBudgetRun(context.Background(), meta, state)
	if err != nil {
		t.Fatalf("start terminal settlement lease: %v", err)
	}
	defer cancel()
	if err := engine.store.DeleteJob(job.ID); err != nil {
		t.Fatalf("delete linked job before terminal settlement: %v", err)
	}
	result, err := engine.complete(runCtx, meta, state, "must not complete", hookManager)
	if err == nil || result.Status != session.StatusFailed || !strings.Contains(err.Error(), "settle child active runtime before completion") {
		t.Fatalf("terminal budget settlement failure must prevent completion: result=%#v err=%v", result, err)
	}
	persisted, loadErr := engine.store.LoadState(meta.ID)
	if loadErr != nil || persisted.Status != session.StatusFailed {
		t.Fatalf("terminal settlement failure did not persist failed state: state=%#v err=%v", persisted, loadErr)
	}
	eventsList, loadErr := engine.store.LoadEvents(meta.ID)
	if loadErr != nil {
		t.Fatalf("load terminal settlement events: %v", loadErr)
	}
	if countEventType(eventsList, "session.completed") != 0 || countEventType(eventsList, "session.failed") != 1 {
		t.Fatalf("completion fact escaped before budget settlement: %#v", eventsList)
	}
}

func TestEffectiveBudgetEventDataIncludesActiveRuntimeCheckpointTelemetry(t *testing.T) {
	now := time.Now().UTC()
	budget := session.NewEffectiveBudget(session.BudgetSourceRuntimeChild, 2, 5, 10, 0, now)
	budget.ActiveRuntimeCheckpointIntervalMS = 1000
	budget.ActiveRuntimeCheckpointAt = now.Format(time.RFC3339Nano)
	budget.ActiveRuntimeLeaseOpen = true
	budget.ActiveRuntimeLeaseOwner = "lease-owner"
	budget.ActiveRuntimeLastRecoveryMS = 1000
	budget.ActiveRuntimeLastRecoveryAt = now.Add(-time.Minute).Format(time.RFC3339Nano)
	budget.LastReason = session.ChildBudgetActiveRuntimeExceededReason
	data := effectiveBudgetEventData(budget)
	for key, want := range map[string]any{
		"active_runtime_checkpoint_interval_ms": int64(1000),
		"active_runtime_checkpoint_at":          budget.ActiveRuntimeCheckpointAt,
		"active_runtime_lease_open":             true,
		"active_runtime_lease_owner":            "lease-owner",
		"active_runtime_last_recovery_ms":       int64(1000),
		"active_runtime_last_recovery_at":       budget.ActiveRuntimeLastRecoveryAt,
		"last_reason":                           session.ChildBudgetActiveRuntimeExceededReason,
	} {
		if got := data[key]; got != want {
			t.Fatalf("event telemetry %s mismatch: got=%#v want=%#v data=%#v", key, got, want, data)
		}
	}
}

func TestDirectAndQueueChildrenSnapshotBudgetAtCreation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		background bool
	}{
		{name: "direct"},
		{name: "queue", background: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testRuntimeConfig(t)
			cfg.Runtime.MaxTurnsHard = -1
			cfg.Runtime.ChildBudget = config.ChildBudgetConfig{
				MaxActiveRuntimeSec: 7,
				MaxElapsedSec:       120,
				MaxTurnsPerAttempt:  3,
			}
			runner := NewRunner(cfg)
			parentID := createParentSession(t, runner.store, t.TempDir())
			if tc.background {
				job, err := runner.QueueSubmit(context.Background(), QueueSubmitRequest{ParentSessionID: parentID, Prompt: "finish", IsolationMode: "off"})
				if err != nil {
					t.Fatalf("queue submit: %v", err)
				}
				snapshot := session.CloneEffectiveBudget(job.EffectiveBudget)
				cfg.Runtime.ChildBudget = config.ChildBudgetConfig{MaxActiveRuntimeSec: 99, MaxElapsedSec: 999, MaxTurnsPerAttempt: 99}
				processed, ok, err := runner.ProcessNextJob(context.Background())
				if err != nil || !ok || processed.Status != session.QueueStatusCompleted {
					t.Fatalf("process queue child: ok=%t err=%v job=%#v", ok, err, processed)
				}
				childMeta, err := runner.store.LoadMetadata(processed.SessionID)
				if err != nil {
					t.Fatalf("load child metadata: %v", err)
				}
				assertBudgetPolicySnapshotEqual(t, snapshot, childMeta.EffectiveBudget)
				loadedJob, err := runner.store.LoadJob(job.ID)
				if err != nil {
					t.Fatalf("load job: %v", err)
				}
				assertBudgetPolicySnapshotEqual(t, snapshot, loadedJob.EffectiveBudget)
				return
			}

			result, err := runner.SpawnAgent(context.Background(), tools.AgentSpawnRequest{
				ParentSessionID: parentID,
				Prompt:          "finish",
				IsolationMode:   "off",
			})
			if err != nil || result.Status != session.StatusCompleted {
				t.Fatalf("spawn direct child: result=%#v err=%v", result, err)
			}
			childMeta, err := runner.store.LoadMetadata(result.SessionID)
			if err != nil {
				t.Fatalf("load child metadata: %v", err)
			}
			if childMeta.EffectiveBudget == nil || childMeta.EffectiveBudget.MaxActiveRuntimeMS != 7000 || childMeta.EffectiveBudget.MaxTurnsPerAttempt != 3 || childMeta.EffectiveBudget.AbsoluteDeadlineAt == "" {
				t.Fatalf("unexpected direct child budget snapshot: %#v", childMeta.EffectiveBudget)
			}
		})
	}
}

func TestForegroundBudgetPauseExtendResume(t *testing.T) {
	var mode atomic.Int64
	server := newBudgetLifecycleResponsesServer(t, &mode)
	cfg := testRuntimeConfig(t)
	providerCfg := cfg.Providers["openai-compatible"]
	providerCfg.BaseURL = server.URL
	cfg.Providers["openai-compatible"] = providerCfg
	cfg.Runtime.MaxTurnsHard = -1
	cfg.Runtime.MultiAgent.MaxActiveChildren = 1
	cfg.Runtime.ChildBudget.MaxTurnsPerAttempt = 1
	runner := NewRunner(cfg)
	parentID := createParentSession(t, runner.store, t.TempDir())

	spawned, err := runner.SpawnAgent(context.Background(), tools.AgentSpawnRequest{ParentSessionID: parentID, Prompt: "bounded direct child", IsolationMode: "off"})
	if err != nil || spawned.Status != session.StatusPaused {
		t.Fatalf("spawn budget-bounded direct child: result=%#v err=%v", spawned, err)
	}
	state, err := runner.store.LoadState(spawned.SessionID)
	if err != nil || state.PauseReason != session.ChildBudgetTurnsExceededReason {
		t.Fatalf("expected turns budget pause, state=%#v err=%v", state, err)
	}
	if _, err := runner.Continue(context.Background(), ContinueRequest{SessionID: spawned.SessionID, Message: "unsafe generic resume"}); err == nil || !strings.Contains(err.Error(), "only its parent") {
		t.Fatalf("generic continue must reject budget-paused child, got %v", err)
	}
	eventsBefore, err := runner.store.LoadEvents(spawned.SessionID)
	if err != nil {
		t.Fatalf("load child events: %v", err)
	}

	mode.Store(1)
	resumed, err := runner.PromptAgent(context.Background(), tools.AgentPromptRequest{
		ParentSessionID: parentID,
		SessionID:       spawned.SessionID,
		Message:         "finish within the extended attempt",
		BudgetExtension: &session.BudgetExtension{AddTurns: 1, Reason: "finish direct child"},
	})
	if err != nil || !resumed.Accepted || resumed.Behavior != "continued_budget_extended_child" {
		t.Fatalf("extend/resume direct child: result=%#v err=%v", resumed, err)
	}
	state, err = runner.store.LoadState(spawned.SessionID)
	if err != nil || state.Status != session.StatusCompleted {
		t.Fatalf("expected resumed direct child to complete, state=%#v err=%v", state, err)
	}
	meta, err := runner.store.LoadMetadata(spawned.SessionID)
	if err != nil {
		t.Fatalf("load direct child metadata: %v", err)
	}
	if meta.EffectiveBudget == nil || meta.EffectiveBudget.Attempt != 2 || meta.EffectiveBudget.TotalUsedTurns < 1 {
		t.Fatalf("expected durable second budget attempt, got %#v", meta.EffectiveBudget)
	}
	eventsAfter, err := runner.store.LoadEvents(spawned.SessionID)
	if err != nil {
		t.Fatalf("reload child events: %v", err)
	}
	if countEventType(eventsBefore, "session.child_budget.exceeded") != 1 || countEventType(eventsAfter, "session.child_budget.exceeded") != 1 || countEventType(eventsAfter, "session.child_budget.extended") != 1 {
		t.Fatalf("unexpected budget event idempotency: before=%#v after=%#v", eventsBefore, eventsAfter)
	}
	coordination, err := runner.store.LoadParentCoordination(parentID)
	if err != nil {
		t.Fatalf("load parent coordination: %v", err)
	}
	if containsString(coordination.UnresolvedChildSessions, spawned.SessionID) || !containsString(coordination.CompletedChildSessions, spawned.SessionID) {
		t.Fatalf("direct child completion did not release parent gate: %#v", coordination)
	}
	queued, err := runner.QueueSubmit(context.Background(), QueueSubmitRequest{ParentSessionID: parentID, Prompt: "verify direct resume slot released", IsolationMode: "off"})
	if err != nil {
		t.Fatalf("queue submit after direct resume: %v", err)
	}
	claimed, ok, err := runner.store.ClaimNextQueuedJobWithLimit(1)
	if err != nil || !ok || claimed.ID != queued.ID {
		t.Fatalf("direct resume slot was not released: ok=%t err=%v job=%#v", ok, err, claimed)
	}
}

func TestPromptAgentResumeRespectsActiveChildCapWithoutMutatingBudget(t *testing.T) {
	for _, tc := range []struct {
		name        string
		queueTarget bool
	}{
		{name: "running_queue_blocks_direct_resume"},
		{name: "direct_reservation_blocks_queue_resume", queueTarget: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testRuntimeConfig(t)
			cfg.Runtime.MultiAgent.MaxActiveChildren = 1
			cfg.Runtime.MaxTurnsHard = -1
			runner := NewRunner(cfg)
			parentID := createParentSession(t, runner.store, t.TempDir())
			now := time.Now().UTC()

			if tc.queueTarget {
				acquired, err := runner.store.AcquireDirectChildSlot(parentID, parentID, "occupying_direct_child", 1)
				if err != nil || !acquired {
					t.Fatalf("occupy direct child slot: acquired=%t err=%v", acquired, err)
				}
				t.Cleanup(func() { _ = runner.store.ReleaseDirectChildSlot("occupying_direct_child") })
			} else {
				occupier, err := runner.QueueSubmit(context.Background(), QueueSubmitRequest{
					ParentSessionID: parentID,
					Prompt:          "occupy queue child slot",
					IsolationMode:   "off",
				})
				if err != nil {
					t.Fatalf("submit occupying queue job: %v", err)
				}
				claimed, ok, err := runner.store.ClaimNextQueuedJobWithLimit(1)
				if err != nil || !ok || claimed.ID != occupier.ID {
					t.Fatalf("claim occupying queue job: ok=%t err=%v job=%#v", ok, err, claimed)
				}
			}

			budget := session.NewEffectiveBudget(session.BudgetSourceRuntimeChild, 1, 0, 0, 0, now)
			session.RefreshEffectiveBudget(budget, 1)
			budget.Status = session.BudgetStatusExhausted
			budget.LastReason = session.ChildBudgetTurnsExceededReason
			childID := "capacity_paused_direct_child"
			queueJobID := ""
			if tc.queueTarget {
				childID = "capacity_paused_queue_child"
				queueJobID = "job_capacity_paused_queue_child"
			}
			child := session.SessionMetadata{
				SchemaVersion:    1,
				ID:               childID,
				CreatedAt:        now.Format(time.RFC3339Nano),
				Workdir:          t.TempDir(),
				RequestedWorkdir: t.TempDir(),
				Mode:             session.ModeExec,
				Provider:         "openai-compatible",
				Model:            "gpt-5.4",
				CompletionPolicy: session.CompletionPolicyAutonomous,
				ParentSessionID:  parentID,
				RootSessionID:    parentID,
				QueueJobID:       queueJobID,
				Depth:            1,
				EffectiveBudget:  session.CloneEffectiveBudget(budget),
			}
			state := session.State{Status: session.StatusPaused, Phase: "interrupt", PauseReason: session.ChildBudgetTurnsExceededReason, Turn: 1, UpdatedAt: now.Format(time.RFC3339Nano)}
			if err := runner.store.Create(child, state); err != nil {
				t.Fatalf("create paused child: %v", err)
			}
			if tc.queueTarget {
				job := session.QueueJob{
					SchemaVersion:   1,
					ID:              queueJobID,
					CreatedAt:       now.Format(time.RFC3339Nano),
					UpdatedAt:       now.Format(time.RFC3339Nano),
					Status:          session.QueueStatusBlocked,
					ParentSessionID: parentID,
					RootSessionID:   parentID,
					Prompt:          "resume queue target",
					Mode:            session.ModeExec,
					Background:      true,
					SessionID:       childID,
					SessionStatus:   session.StatusPaused,
					LastError:       "child session is resumable: paused",
					EffectiveBudget: session.CloneEffectiveBudget(budget),
				}
				if err := runner.store.SaveJob(job); err != nil {
					t.Fatalf("save blocked queue target: %v", err)
				}
				if err := addParentQueueJob(runner.store, parentID, queueJobID, parentWaitAll); err != nil {
					t.Fatalf("link queue target: %v", err)
				}
			} else if err := addParentChildSession(runner.store, parentID, childID, parentWaitAll); err != nil {
				t.Fatalf("link direct target: %v", err)
			}

			_, err := runner.PromptAgent(context.Background(), tools.AgentPromptRequest{
				ParentSessionID: parentID,
				SessionID:       childID,
				QueueJobID:      queueJobID,
				Message:         "resume only when capacity is available",
				BudgetExtension: &session.BudgetExtension{AddTurns: 1, Reason: "capacity test"},
			})
			if err == nil || !strings.Contains(err.Error(), "max active children reached") {
				t.Fatalf("expected resume capacity rejection, got %v", err)
			}
			after, err := runner.store.LoadMetadata(childID)
			if err != nil {
				t.Fatalf("reload paused child metadata: %v", err)
			}
			if after.EffectiveBudget == nil || after.EffectiveBudget.Attempt != 1 || after.EffectiveBudget.MaxTurnsPerAttempt != 1 || after.EffectiveBudget.Status != session.BudgetStatusExhausted {
				t.Fatalf("capacity rejection mutated effective budget: %#v", after.EffectiveBudget)
			}
			state, err = runner.store.LoadState(childID)
			if err != nil || state.Status != session.StatusPaused || state.PauseReason != session.ChildBudgetTurnsExceededReason {
				t.Fatalf("capacity rejection changed child state: state=%#v err=%v", state, err)
			}
			if tc.queueTarget {
				job, err := runner.store.LoadJob(queueJobID)
				if err != nil || job.Status != session.QueueStatusBlocked || job.SessionStatus != session.StatusPaused {
					t.Fatalf("capacity rejection changed queue target: job=%#v err=%v", job, err)
				}
			}
			eventsList, err := runner.store.LoadEvents(childID)
			if err != nil {
				t.Fatalf("load child events: %v", err)
			}
			if countEventType(eventsList, "session.child_budget.extended") != 0 {
				t.Fatalf("capacity rejection must not record budget extension: %#v", eventsList)
			}
		})
	}
}

func TestBackgroundBudgetPauseExtendResumeAndCrossParentRejection(t *testing.T) {
	var mode atomic.Int64
	server := newBudgetLifecycleResponsesServer(t, &mode)
	cfg := testRuntimeConfig(t)
	providerCfg := cfg.Providers["openai-compatible"]
	providerCfg.BaseURL = server.URL
	cfg.Providers["openai-compatible"] = providerCfg
	cfg.Runtime.MaxTurnsHard = -1
	cfg.Runtime.ChildBudget.MaxTurnsPerAttempt = 1
	runner := NewRunner(cfg)
	parentID := createParentSession(t, runner.store, t.TempDir())
	otherParentID := createParentSession(t, runner.store, t.TempDir())
	job, err := runner.QueueSubmit(context.Background(), QueueSubmitRequest{ParentSessionID: parentID, Prompt: "bounded queue child", IsolationMode: "off"})
	if err != nil {
		t.Fatalf("queue submit: %v", err)
	}
	processed, ok, err := runner.ProcessNextJob(context.Background())
	if err != nil || !ok || processed.Status != session.QueueStatusBlocked || processed.SessionStatus != session.StatusPaused {
		t.Fatalf("expected budget-paused queue child, ok=%t err=%v job=%#v", ok, err, processed)
	}
	if processed.EffectiveBudget == nil || processed.EffectiveBudget.Status != session.BudgetStatusExhausted {
		t.Fatalf("expected exhausted queue budget snapshot, got %#v", processed.EffectiveBudget)
	}
	assertQueueLeaseCleared(t, processed)
	if session.QueueJobCanProgress(processed) {
		t.Fatalf("budget-paused blocked job must require parent intervention: %#v", processed)
	}
	notifications, err := runner.store.LoadBackgroundNotifications(parentID)
	if err != nil {
		t.Fatalf("load budget-pause notifications: %v", err)
	}
	if len(notifications) != 1 || notifications[0].Status != session.QueueStatusBlocked || len(notifications[0].AvailableActions) != 3 || notifications[0].EffectiveBudget == nil {
		t.Fatalf("expected actionable budget-pause notification, got %#v", notifications)
	}
	if _, err := runner.PromptAgent(context.Background(), tools.AgentPromptRequest{ParentSessionID: parentID, QueueJobID: job.ID, Message: "resume without extension"}); err == nil || !strings.Contains(err.Error(), "provide budget_extension") {
		t.Fatalf("missing extension must reject budget resume, got %v", err)
	}
	if _, err := runner.PromptAgent(context.Background(), tools.AgentPromptRequest{
		ParentSessionID: otherParentID,
		QueueJobID:      job.ID,
		Message:         "steal child",
		BudgetExtension: &session.BudgetExtension{AddTurns: 1},
	}); err == nil || !strings.Contains(err.Error(), "not linked") {
		t.Fatalf("cross-parent extension must be rejected, got %v", err)
	}
	if _, err := runner.PromptAgent(context.Background(), tools.AgentPromptRequest{
		ParentSessionID: parentID,
		QueueJobID:      job.ID,
		Message:         "invalid extension must roll back the resume slot",
		BudgetExtension: &session.BudgetExtension{AddTurns: 1, ClearTurnLimit: true},
	}); err == nil || !strings.Contains(err.Error(), "cannot add turns and clear") {
		t.Fatalf("invalid extension must be rejected after safe slot rollback, got %v", err)
	}
	rolledBackJob, err := runner.store.LoadJob(job.ID)
	if err != nil || rolledBackJob.Status != session.QueueStatusBlocked || rolledBackJob.SessionStatus != session.StatusPaused {
		t.Fatalf("invalid extension did not restore blocked queue job: job=%#v err=%v", rolledBackJob, err)
	}
	assertQueueLeaseCleared(t, rolledBackJob)
	rolledBackMeta, err := runner.store.LoadMetadata(processed.SessionID)
	if err != nil || rolledBackMeta.EffectiveBudget == nil || rolledBackMeta.EffectiveBudget.Attempt != 1 || rolledBackMeta.EffectiveBudget.Status != session.BudgetStatusExhausted {
		t.Fatalf("invalid extension mutated durable budget: budget=%#v err=%v", rolledBackMeta.EffectiveBudget, err)
	}

	mode.Store(1)
	resumed, err := runner.PromptAgent(context.Background(), tools.AgentPromptRequest{
		ParentSessionID: parentID,
		QueueJobID:      job.ID,
		Message:         "finish queue child",
		BudgetExtension: &session.BudgetExtension{AddTurns: 1, Reason: "finish queue child"},
	})
	if err != nil || !resumed.Accepted || resumed.Behavior != "continued_budget_extended_child" {
		t.Fatalf("extend/resume queue child: result=%#v err=%v", resumed, err)
	}
	loaded, err := runner.store.LoadJob(job.ID)
	if err != nil || loaded.Status != session.QueueStatusCompleted || loaded.SessionStatus != session.StatusCompleted {
		t.Fatalf("expected queue child completion after extension, job=%#v err=%v", loaded, err)
	}
	if loaded.EffectiveBudget == nil || loaded.EffectiveBudget.Attempt != 2 {
		t.Fatalf("expected queue budget attempt 2, got %#v", loaded.EffectiveBudget)
	}
}

func TestLegacyBudgetPausedChildCanMigrateAndExtend(t *testing.T) {
	var mode atomic.Int64
	mode.Store(1)
	server := newBudgetLifecycleResponsesServer(t, &mode)
	cfg := testRuntimeConfig(t)
	providerCfg := cfg.Providers["openai-compatible"]
	providerCfg.BaseURL = server.URL
	cfg.Providers["openai-compatible"] = providerCfg
	cfg.Runtime.ChildBudget.MaxTurns = 1
	cfg.Runtime.ChildBudget.MaxTurnsPerAttempt = 0
	runner := NewRunner(cfg)
	parentID := createParentSession(t, runner.store, t.TempDir())
	now := time.Now().UTC().Format(time.RFC3339Nano)
	child := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "legacy_budget_child",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "openai-compatible",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyAutonomous,
		ParentSessionID:  parentID,
		RootSessionID:    parentID,
		Depth:            1,
	}
	if err := runner.store.Create(child, session.State{Status: session.StatusPaused, Phase: "interrupt", PauseReason: session.ChildBudgetTurnsExceededReason, Turn: 1, UpdatedAt: now}); err != nil {
		t.Fatalf("create legacy child: %v", err)
	}
	if err := addParentChildSession(runner.store, parentID, child.ID, parentWaitAll); err != nil {
		t.Fatalf("add parent child: %v", err)
	}
	result, err := runner.PromptAgent(context.Background(), tools.AgentPromptRequest{
		ParentSessionID: parentID,
		SessionID:       child.ID,
		Message:         "finish migrated child",
		BudgetExtension: &session.BudgetExtension{AddTurns: 1, Reason: "migrate legacy budget"},
	})
	if err != nil || !result.Accepted {
		t.Fatalf("migrate/extend legacy child: result=%#v err=%v", result, err)
	}
	meta, err := runner.store.LoadMetadata(child.ID)
	if err != nil {
		t.Fatalf("load migrated metadata: %v", err)
	}
	if meta.EffectiveBudget == nil || meta.EffectiveBudget.Source != session.BudgetSourceLegacyResume || meta.EffectiveBudget.Attempt != 2 {
		t.Fatalf("unexpected migrated budget snapshot: %#v", meta.EffectiveBudget)
	}
}

func TestActiveRuntimeBudgetPauseExtendResumeForDirectAndQueueChild(t *testing.T) {
	for _, background := range []bool{false, true} {
		name := "direct"
		if background {
			name = "queue"
		}
		t.Run(name, func(t *testing.T) {
			var mode atomic.Int64
			server := newBlockingThenFinishResponsesServer(t, &mode)
			cfg := testRuntimeConfig(t)
			providerCfg := cfg.Providers["openai-compatible"]
			providerCfg.BaseURL = server.URL
			cfg.Providers["openai-compatible"] = providerCfg
			cfg.Runtime.MaxTurnsHard = -1
			cfg.Runtime.ChildBudget = config.ChildBudgetConfig{MaxActiveRuntimeSec: 1}
			runner := NewRunner(cfg)
			parentID := createParentSession(t, runner.store, t.TempDir())

			childID := ""
			jobID := ""
			if background {
				job, err := runner.QueueSubmit(context.Background(), QueueSubmitRequest{ParentSessionID: parentID, Prompt: "active runtime queue child", IsolationMode: "off"})
				if err != nil {
					t.Fatalf("queue submit: %v", err)
				}
				processed, ok, err := runner.ProcessNextJob(context.Background())
				if err != nil || !ok || processed.Status != session.QueueStatusBlocked || processed.SessionStatus != session.StatusPaused {
					t.Fatalf("expected active-runtime paused queue child, ok=%t job=%#v err=%v", ok, processed, err)
				}
				childID = processed.SessionID
				jobID = job.ID
			} else {
				spawned, err := runner.SpawnAgent(context.Background(), tools.AgentSpawnRequest{ParentSessionID: parentID, Prompt: "active runtime direct child", IsolationMode: "off"})
				if err != nil || spawned.Status != session.StatusPaused {
					t.Fatalf("expected active-runtime paused direct child: result=%#v err=%v", spawned, err)
				}
				childID = spawned.SessionID
			}
			state, err := runner.store.LoadState(childID)
			if err != nil || state.PauseReason != session.ChildBudgetActiveRuntimeExceededReason {
				t.Fatalf("unexpected active-runtime pause state: %#v err=%v", state, err)
			}
			mode.Store(1)
			request := tools.AgentPromptRequest{
				ParentSessionID: parentID,
				SessionID:       childID,
				QueueJobID:      jobID,
				Message:         "finish after active runtime extension",
				BudgetExtension: &session.BudgetExtension{AddActiveRuntimeSec: 1, Reason: "finish active runtime child"},
			}
			resumed, err := runner.PromptAgent(context.Background(), request)
			if err != nil || !resumed.Accepted || resumed.Behavior != "continued_budget_extended_child" {
				t.Fatalf("extend/resume active-runtime child: result=%#v err=%v", resumed, err)
			}
			state, err = runner.store.LoadState(childID)
			if err != nil || state.Status != session.StatusCompleted {
				t.Fatalf("expected active-runtime child completion: state=%#v err=%v", state, err)
			}
			meta, err := runner.store.LoadMetadata(childID)
			if err != nil || meta.EffectiveBudget == nil || meta.EffectiveBudget.Attempt != 2 || meta.EffectiveBudget.TotalActiveRuntimeMS < 900 {
				t.Fatalf("expected durable active-runtime attempt accounting: budget=%#v err=%v", meta.EffectiveBudget, err)
			}
			if background {
				job, err := runner.store.LoadJob(jobID)
				if err != nil || job.Status != session.QueueStatusCompleted || job.EffectiveBudget == nil || job.EffectiveBudget.Attempt != 2 {
					t.Fatalf("expected completed queue budget attempt 2: job=%#v err=%v", job, err)
				}
			}
		})
	}
}

func TestAbsoluteDeadlineBudgetPersistsAcrossRunnerRestartAndCanBeExtended(t *testing.T) {
	var mode atomic.Int64
	server := newBlockingThenFinishResponsesServer(t, &mode)
	cfg := testRuntimeConfig(t)
	providerCfg := cfg.Providers["openai-compatible"]
	providerCfg.BaseURL = server.URL
	cfg.Providers["openai-compatible"] = providerCfg
	cfg.Runtime.MaxTurnsHard = -1
	cfg.Runtime.ChildBudget = config.ChildBudgetConfig{MaxElapsedSec: 1}
	runner := NewRunner(cfg)
	parentID := createParentSession(t, runner.store, t.TempDir())
	spawned, err := runner.SpawnAgent(context.Background(), tools.AgentSpawnRequest{ParentSessionID: parentID, Prompt: "absolute deadline child", IsolationMode: "off"})
	if err != nil || spawned.Status != session.StatusPaused {
		t.Fatalf("expected absolute-deadline pause: result=%#v err=%v", spawned, err)
	}
	state, err := runner.store.LoadState(spawned.SessionID)
	if err != nil || state.PauseReason != session.ChildBudgetAbsoluteDeadlineExceededReason {
		t.Fatalf("unexpected absolute deadline state: %#v err=%v", state, err)
	}
	before, err := runner.store.LoadMetadata(spawned.SessionID)
	if err != nil || before.EffectiveBudget == nil || before.EffectiveBudget.AbsoluteDeadlineAt == "" {
		t.Fatalf("missing durable absolute deadline: %#v err=%v", before.EffectiveBudget, err)
	}
	mode.Store(1)
	restarted := NewRunner(cfg)
	resumed, err := restarted.PromptAgent(context.Background(), tools.AgentPromptRequest{
		ParentSessionID: parentID,
		SessionID:       spawned.SessionID,
		Message:         "finish after deadline extension",
		BudgetExtension: &session.BudgetExtension{ExtendDeadlineSec: 2, Reason: "resume after restart"},
	})
	if err != nil || !resumed.Accepted || resumed.Behavior != "continued_budget_extended_child" {
		t.Fatalf("restart deadline extension/resume: result=%#v err=%v", resumed, err)
	}
	after, err := restarted.store.LoadMetadata(spawned.SessionID)
	if err != nil || after.EffectiveBudget == nil || after.EffectiveBudget.Attempt != 2 {
		t.Fatalf("missing restarted deadline attempt: %#v err=%v", after.EffectiveBudget, err)
	}
	deadline, ok := session.EffectiveBudgetDeadline(after.EffectiveBudget)
	if !ok || !deadline.After(time.Now().UTC()) {
		t.Fatalf("deadline extension was not persisted into the future: %#v", after.EffectiveBudget)
	}
	state, err = restarted.store.LoadState(spawned.SessionID)
	if err != nil || state.Status != session.StatusCompleted {
		t.Fatalf("expected restarted child completion: state=%#v err=%v", state, err)
	}
}

func TestPersistEffectiveBudgetRollsBackSessionSnapshotWhenLinkedJobLoadFails(t *testing.T) {
	cfg := testRuntimeConfig(t)
	runner := NewRunner(cfg)
	parentID := createParentSession(t, runner.store, t.TempDir())
	now := time.Now().UTC()
	oldBudget := session.NewEffectiveBudget(session.BudgetSourceRuntimeChild, 1, 0, 0, 0, now)
	child := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        now.Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             session.ModeExec,
		Provider:         "openai-compatible",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyAutonomous,
		ParentSessionID:  parentID,
		RootSessionID:    parentID,
		QueueJobID:       session.NewQueueJobID(),
		Depth:            1,
		EffectiveBudget:  session.CloneEffectiveBudget(oldBudget),
	}
	if err := runner.store.Create(child, session.State{Status: session.StatusPaused, Phase: "interrupt", PauseReason: session.ChildBudgetTurnsExceededReason, Turn: 1, UpdatedAt: now.Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create linked child: %v", err)
	}
	job := session.QueueJob{
		SchemaVersion:   1,
		ID:              child.QueueJobID,
		CreatedAt:       now.Format(time.RFC3339Nano),
		UpdatedAt:       now.Format(time.RFC3339Nano),
		Status:          session.QueueStatusBlocked,
		ParentSessionID: parentID,
		RootSessionID:   parentID,
		Prompt:          "linked child",
		Mode:            session.ModeExec,
		Background:      true,
		SessionID:       child.ID,
		SessionStatus:   session.StatusPaused,
		EffectiveBudget: session.CloneEffectiveBudget(oldBudget),
	}
	if err := runner.store.SaveJob(job); err != nil {
		t.Fatalf("save linked job: %v", err)
	}
	jobPath := filepath.Join(cfg.Session.Dir, "_queue", session.QueueStatusBlocked, job.ID+".json")
	if err := os.WriteFile(jobPath, []byte("{invalid"), 0o600); err != nil {
		t.Fatalf("corrupt linked job: %v", err)
	}
	newBudget := session.CloneEffectiveBudget(oldBudget)
	newBudget.MaxTurnsPerAttempt = 5
	session.RefreshEffectiveBudget(newBudget, 0)
	if err := persistEffectiveBudget(runner.store, child, newBudget); err == nil || !strings.Contains(err.Error(), "load linked queue job") {
		t.Fatalf("expected linked job load failure, got %v", err)
	}
	reloaded, err := runner.store.LoadMetadata(child.ID)
	if err != nil {
		t.Fatalf("reload child metadata: %v", err)
	}
	if reloaded.EffectiveBudget == nil || reloaded.EffectiveBudget.MaxTurnsPerAttempt != oldBudget.MaxTurnsPerAttempt {
		t.Fatalf("session budget was not rolled back after linked job failure: %#v", reloaded.EffectiveBudget)
	}
}

func assertBudgetPolicySnapshotEqual(t *testing.T, want, got *session.EffectiveBudget) {
	t.Helper()
	if want == nil || got == nil {
		t.Fatalf("budget snapshot missing: want=%#v got=%#v", want, got)
	}
	if got.PolicyVersion != want.PolicyVersion || got.Source != want.Source || got.TurnScope != want.TurnScope || got.TimeScope != want.TimeScope || got.MaxTurnsPerAttempt != want.MaxTurnsPerAttempt || got.MaxActiveRuntimeMS != want.MaxActiveRuntimeMS || got.AbsoluteDeadlineAt != want.AbsoluteDeadlineAt || got.ActiveRuntimeCheckpointIntervalMS != want.ActiveRuntimeCheckpointIntervalMS {
		t.Fatalf("budget policy snapshot drifted: want=%#v got=%#v", want, got)
	}
}

func attachRunningBudgetJob(t *testing.T, store *session.Store, meta *session.SessionMetadata) session.QueueJob {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	job := session.QueueJob{
		SchemaVersion:   1,
		ID:              session.NewQueueJobID(),
		CreatedAt:       now,
		UpdatedAt:       now,
		Status:          session.QueueStatusRunning,
		ClaimedBy:       "test-worker",
		ClaimedAt:       now,
		HeartbeatAt:     now,
		WorkerPID:       os.Getpid(),
		ProcessStartID:  fmt.Sprintf("%d:%s", os.Getpid(), now),
		ParentSessionID: meta.ParentSessionID,
		RootSessionID:   meta.RootSessionID,
		Prompt:          "budget checkpoint test",
		Mode:            session.ModeExec,
		SessionID:       meta.ID,
		SessionStatus:   session.StatusRunning,
		Background:      true,
		WaitMode:        parentWaitAll,
	}
	meta.QueueJobID = job.ID
	if err := store.SaveMetadata(meta.ID, *meta); err != nil {
		t.Fatalf("link child metadata to queue job: %v", err)
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save running queue job: %v", err)
	}
	if err := addParentQueueJob(store, meta.ParentSessionID, job.ID, parentWaitAll); err != nil {
		t.Fatalf("add parent queue coordination: %v", err)
	}
	return job
}

func newBudgetLifecycleResponsesServer(t *testing.T, mode *atomic.Int64) *httptest.Server {
	t.Helper()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_, _ = io.ReadAll(r.Body)
		call := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if mode.Load() == 0 {
			_, _ = fmt.Fprintf(w, `{"id":"resp_budget_%d","status":"completed","output":[{"type":"function_call","call_id":"call_budget_%d","name":"read_file","arguments":"{\"path\":\"missing-budget-%d.txt\"}"}],"usage":{"input_tokens":1,"output_tokens":1}}`, call, call, call)
			return
		}
		_, _ = fmt.Fprintf(w, `{"id":"resp_finish_%d","status":"completed","output":[{"type":"function_call","call_id":"call_finish_%d","name":"finish","arguments":"{\"message\":\"budget lifecycle done\"}"}],"usage":{"input_tokens":1,"output_tokens":1}}`, call, call)
	}))
	t.Cleanup(server.Close)
	return server
}

func newBlockingThenFinishResponsesServer(t *testing.T, mode *atomic.Int64) *httptest.Server {
	t.Helper()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_, _ = io.ReadAll(r.Body)
		call := calls.Add(1)
		if mode.Load() == 0 {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"resp_finish_%d","status":"completed","output":[{"type":"function_call","call_id":"call_finish_%d","name":"finish","arguments":"{\"message\":\"budget lifecycle done\"}"}],"usage":{"input_tokens":1,"output_tokens":1}}`, call, call)
	}))
	t.Cleanup(server.Close)
	return server
}

func assertBudgetPauseReason(t *testing.T, store *session.Store, result RunResult, want string) {
	t.Helper()
	if result.Status != session.StatusPaused {
		t.Fatalf("expected paused result, got %#v", result)
	}
	state, err := store.LoadState(result.SessionID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.PauseReason != want {
		t.Fatalf("expected pause reason %q, got %#v", want, state)
	}
	meta, err := store.LoadMetadata(result.SessionID)
	if err != nil {
		t.Fatalf("load metadata: %v", err)
	}
	if meta.EffectiveBudget == nil || meta.EffectiveBudget.Status != session.BudgetStatusExhausted || meta.EffectiveBudget.LastReason != want {
		t.Fatalf("expected exhausted durable effective budget, got %#v", meta.EffectiveBudget)
	}
}

func countEventType(items []events.Event, eventType string) int {
	count := 0
	for _, item := range items {
		if item.Type == eventType {
			count++
		}
	}
	return count
}

func countHarnessReminderSource(t *testing.T, store *session.Store, sessionID, source string) int {
	t.Helper()
	messages, err := store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	count := 0
	for _, message := range messages {
		if message.Role != "user" || message.Meta == nil {
			continue
		}
		if strings.TrimSpace(fmt.Sprint(message.Meta["source"])) == "harness_reminder" && strings.TrimSpace(fmt.Sprint(message.Meta["kind"])) == source {
			count++
		}
	}
	return count
}

func repeatFakeTurns(count int, turn func(context.Context, provider.TurnRequest) (provider.TurnResult, error)) []func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
	out := make([]func(context.Context, provider.TurnRequest) (provider.TurnResult, error), count)
	for i := range out {
		out[i] = turn
	}
	return out
}
