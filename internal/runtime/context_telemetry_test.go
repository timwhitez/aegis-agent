package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/events"
	"go-cli-agent/internal/provider"
	"go-cli-agent/internal/session"
)

type contextTelemetryAdapter struct {
	estimator     *provider.FakeAdapter
	mainErr       error
	semanticErr   error
	emitMainRetry bool
}

type contextTelemetryControlAdapter struct {
	estimator      *provider.FakeAdapter
	beforeEstimate func()
	run            func(context.Context, provider.TurnRequest, provider.EmitFunc) (provider.TurnResult, error)
}

func (a *contextTelemetryControlAdapter) Name() string { return "context-telemetry-control-fixture" }

func (a *contextTelemetryControlAdapter) EstimateRequest(req provider.TurnRequest) (provider.WireRequestEstimate, error) {
	if a.beforeEstimate != nil {
		before := a.beforeEstimate
		a.beforeEstimate = nil
		before()
	}
	return a.estimator.EstimateRequest(req)
}

func (a *contextTelemetryControlAdapter) RunTurn(ctx context.Context, req provider.TurnRequest, emit provider.EmitFunc) (provider.TurnResult, error) {
	if a.run == nil {
		return provider.TurnResult{}, errors.New("unexpected provider call")
	}
	return a.run(ctx, req, emit)
}

func newContextTelemetryAdapter(mainErr error) *contextTelemetryAdapter {
	return &contextTelemetryAdapter{estimator: provider.NewFake(), mainErr: mainErr}
}

func (a *contextTelemetryAdapter) Name() string { return "context-telemetry-fixture" }

func (a *contextTelemetryAdapter) EstimateRequest(req provider.TurnRequest) (provider.WireRequestEstimate, error) {
	return a.estimator.EstimateRequest(req)
}

func (a *contextTelemetryAdapter) RunTurn(_ context.Context, req provider.TurnRequest, emit provider.EmitFunc) (provider.TurnResult, error) {
	if req.SystemPrompt == semanticSummarySystemPrompt {
		emit("provider.error", map[string]any{"class": "transient_fixture", "message": "SEMANTIC_CALLBACK_SENTINEL"})
		if a.semanticErr != nil {
			return provider.TurnResult{}, a.semanticErr
		}
		return provider.TurnResult{
			Text:       "bounded semantic summary",
			StopReason: "done_candidate",
			Usage:      provider.Usage{Reported: false},
		}, nil
	}
	if a.emitMainRetry {
		emit("provider.retry", map[string]any{"attempt": 1, "delay_ms": 0, "class": "fixture_retry", "error": "RETRY_SENTINEL"})
	}
	if a.mainErr != nil {
		return provider.TurnResult{}, a.mainErr
	}
	return provider.TurnResult{
		ToolCalls: []provider.ToolCall{{
			ID:        "context-telemetry-finish",
			Name:      "finish",
			Arguments: json.RawMessage(`{"message":"done"}`),
		}},
		StopReason:         "tool_use",
		ProviderResponseID: "resp-context-telemetry",
		Usage:              provider.Usage{Reported: true},
	}, nil
}

func TestEngineContextTelemetryCorrelatesCompactionSemanticAndMainRequests(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.GuardrailsMode = "standard"
	cfg.Runtime.Compact.InputCharThreshold = 64
	cfg.Runtime.Compact.KeepRecentMessages = 1
	cfg.Runtime.Compact.HysteresisDeltaChars = 32
	cfg.Runtime.Compact.SemanticSummary.Enabled = true
	engine, meta, state, registry, hookManager, catalog := newTestEngineWithConfig(t, cfg, session.ModeRun)
	for i := 0; i < 5; i++ {
		text := strings.Repeat(string(rune('a'+i)), 256) + " PROMPT_CONTEXT_SENTINEL"
		if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", text)); err != nil {
			t.Fatalf("append message: %v", err)
		}
	}

	adapter := newContextTelemetryAdapter(nil)
	adapter.emitMainRetry = true
	result, err := engine.Run(context.Background(), meta, state, "", adapter, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("expected completed session, got %#v", result)
	}

	recorded, err := loadEvents(engine.store, meta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	prepared := map[string]map[string]any{}
	completed := map[string]map[string]any{}
	retries := map[string][]map[string]any{}
	for _, evt := range recorded {
		kind := strings.TrimSpace(stringFromAny(evt.Data["request_kind"]))
		switch evt.Type {
		case "provider.request.prepared":
			prepared[kind] = evt.Data
		case "provider.request.completed":
			completed[kind] = evt.Data
		case "provider.retry":
			retries[kind] = append(retries[kind], evt.Data)
		case "compact.started", "compact.finished", "compact.reused", "compact.deferred":
			if evt.Data["request_kind"] != requestKindMain || strings.TrimSpace(stringFromAny(evt.Data["request_id"])) == "" {
				t.Fatalf("compaction event missing main correlation: %#v", evt)
			}
		case "provider.error":
			if kind != requestKindSemanticSummary || strings.TrimSpace(stringFromAny(evt.Data["request_id"])) == "" || evt.Data["turn"] == nil || evt.Data["request_sequence"] == nil {
				t.Fatalf("semantic callback missing correlation: %#v", evt)
			}
		}
	}
	for _, kind := range []string{requestKindSemanticSummary, requestKindMain} {
		if prepared[kind] == nil || completed[kind] == nil {
			t.Fatalf("missing %s lifecycle: prepared=%#v completed=%#v", kind, prepared, completed)
		}
		if prepared[kind]["request_id"] != completed[kind]["request_id"] || prepared[kind]["turn"] != completed[kind]["turn"] || prepared[kind]["request_sequence"] != completed[kind]["request_sequence"] {
			t.Fatalf("%s lifecycle correlation drift: prepared=%#v completed=%#v", kind, prepared[kind], completed[kind])
		}
	}
	if len(retries[requestKindMain]) != 1 || retries[requestKindMain][0]["request_id"] != prepared[requestKindMain]["request_id"] || retries[requestKindMain][0]["request_sequence"] != prepared[requestKindMain]["request_sequence"] {
		t.Fatalf("transport retry must stay inside the prepared main request: retries=%#v prepared=%#v", retries, prepared)
	}
	semanticUsage, ok := completed[requestKindSemanticSummary]["usage"].(map[string]any)
	if !ok || semanticUsage["reported"] != false || semanticUsage["input_tokens"] != nil {
		t.Fatalf("semantic usage absence must remain unknown: %#v", completed[requestKindSemanticSummary])
	}
	mainUsage, ok := completed[requestKindMain]["usage"].(map[string]any)
	if !ok || mainUsage["reported"] != true || mainUsage["input_tokens"] != float64(0) || mainUsage["output_tokens"] != float64(0) {
		t.Fatalf("reported zero usage must remain known: %#v", completed[requestKindMain])
	}

	report, err := engine.store.ContextReport(meta.ID)
	if err != nil {
		t.Fatalf("context report: %v", err)
	}
	if len(report.Sessions) != 1 || report.Sessions[0].Metrics.SemanticSummaryRequestCount != 1 || report.Sessions[0].Metrics.MainRequestCount != 1 || report.Aggregate.UnknownUsageRequestCount != 1 {
		t.Fatalf("unexpected report request accounting: %#v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	for _, forbidden := range []string{"PROMPT_CONTEXT_SENTINEL", "SEMANTIC_CALLBACK_SENTINEL"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("context report leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestEngineContextTelemetrySemanticTimeoutHasOneTerminalLifecycle(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.GuardrailsMode = "standard"
	cfg.Runtime.Compact.InputCharThreshold = 64
	cfg.Runtime.Compact.KeepRecentMessages = 1
	cfg.Runtime.Compact.SemanticSummary.Enabled = true
	engine, meta, state, registry, hookManager, catalog := newTestEngineWithConfig(t, cfg, session.ModeRun)
	for i := 0; i < 4; i++ {
		if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", strings.Repeat("semantic timeout context ", 20))); err != nil {
			t.Fatalf("append message: %v", err)
		}
	}
	adapter := newContextTelemetryAdapter(nil)
	adapter.semanticErr = context.DeadlineExceeded
	result, err := engine.Run(context.Background(), meta, state, "", adapter, catalog, registry, hookManager)
	if err != nil || result.Status != session.StatusCompleted {
		t.Fatalf("semantic timeout must fall back while main completes: result=%#v err=%v", result, err)
	}
	recorded, err := loadEvents(engine.store, meta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	semanticPrepared, ok := findRequestEvent(recorded, "provider.request.prepared", requestKindSemanticSummary)
	if !ok {
		t.Fatalf("missing semantic-summary prepared event: %#v", recorded)
	}
	terminal := terminalRequestEvents(recorded, stringFromAny(semanticPrepared.Data["request_id"]))
	if len(terminal) != 1 || terminal[0].Type != "provider.request.failed" || terminal[0].Data["status"] != "cancelled" || terminal[0].Data["error_class"] != "context_deadline_exceeded" {
		t.Fatalf("semantic timeout must have one typed terminal event: %#v", terminal)
	}
	finished, ok := findEventByType(recorded, "compact.finished")
	if !ok || finished.Data["semantic_summary_status"] != "failed" {
		t.Fatalf("semantic timeout must record deterministic fallback: %#v", finished)
	}
}

func TestEngineContextTelemetryPauseBeforeProviderCallHasOneTypedTerminalLifecycle(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	engine.cfg.Runtime.Compact.SemanticSummary.Enabled = false
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "pause after request preparation")); err != nil {
		t.Fatalf("append message: %v", err)
	}
	providerCalls := 0
	adapter := &contextTelemetryControlAdapter{
		estimator:      provider.NewFake(),
		beforeEstimate: func() { engine.control.requestPauseWithReason("manual_stop") },
		run: func(context.Context, provider.TurnRequest, provider.EmitFunc) (provider.TurnResult, error) {
			providerCalls++
			return provider.TurnResult{}, nil
		},
	}
	result, err := engine.Run(context.Background(), meta, state, "", adapter, catalog, registry, hookManager)
	if err != nil || result.Status != session.StatusPaused {
		t.Fatalf("expected prepared request to pause before provider call: result=%#v err=%v", result, err)
	}
	if providerCalls != 0 {
		t.Fatalf("pause before provider call reached adapter %d times", providerCalls)
	}
	recorded, err := loadEvents(engine.store, meta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	prepared, ok := findRequestEvent(recorded, "provider.request.prepared", requestKindMain)
	if !ok {
		t.Fatalf("missing main prepared event: %#v", recorded)
	}
	terminal := terminalRequestEvents(recorded, stringFromAny(prepared.Data["request_id"]))
	if len(terminal) != 1 || terminal[0].Type != "provider.request.failed" || terminal[0].Data["status"] != "cancelled" || terminal[0].Data["error_class"] != "paused_before_provider_call" {
		t.Fatalf("pause before provider call must have one typed terminal event: %#v", terminal)
	}
}

func TestEngineContextTelemetryProviderCancellationHasOneTerminalLifecycle(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeRun)
	engine.cfg.Runtime.Compact.SemanticSummary.Enabled = false
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "cancel provider request")); err != nil {
		t.Fatalf("append message: %v", err)
	}
	adapter := &contextTelemetryControlAdapter{
		estimator: provider.NewFake(),
		run: func(ctx context.Context, _ provider.TurnRequest, _ provider.EmitFunc) (provider.TurnResult, error) {
			engine.control.requestPauseWithReason("manual_stop")
			<-ctx.Done()
			return provider.TurnResult{}, ctx.Err()
		},
	}
	result, err := engine.Run(context.Background(), meta, state, "", adapter, catalog, registry, hookManager)
	if err != nil || result.Status != session.StatusPaused {
		t.Fatalf("expected cancelled provider request to pause: result=%#v err=%v", result, err)
	}
	recorded, err := loadEvents(engine.store, meta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	prepared, ok := findRequestEvent(recorded, "provider.request.prepared", requestKindMain)
	if !ok {
		t.Fatalf("missing main prepared event: %#v", recorded)
	}
	terminal := terminalRequestEvents(recorded, stringFromAny(prepared.Data["request_id"]))
	if len(terminal) != 1 || terminal[0].Type != "provider.request.failed" || terminal[0].Data["status"] != "cancelled" || terminal[0].Data["error_class"] != "context_cancelled" {
		t.Fatalf("provider cancellation must have one terminal event: %#v", terminal)
	}
}

func TestEngineContextTelemetryRetryAttemptPersistenceFailureHasOneTerminalLifecycle(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	engine.cfg.Runtime.Compact.SemanticSummary.Enabled = false
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "emit a transport retry")); err != nil {
		t.Fatalf("append message: %v", err)
	}
	blockRuntimeProviderAttemptsPath(t, engine.store, meta.ID)
	adapter := newContextTelemetryAdapter(nil)
	adapter.emitMainRetry = true
	result, err := engine.Run(context.Background(), meta, state, "", adapter, catalog, registry, hookManager)
	if err == nil || result.Status != session.StatusFailed || !strings.Contains(err.Error(), "provider-attempts.jsonl") {
		t.Fatalf("expected retry-attempt persistence failure: result=%#v err=%v", result, err)
	}
	recorded, err := loadEvents(engine.store, meta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	prepared, ok := findRequestEvent(recorded, "provider.request.prepared", requestKindMain)
	if !ok {
		t.Fatalf("missing main prepared event: %#v", recorded)
	}
	terminal := terminalRequestEvents(recorded, stringFromAny(prepared.Data["request_id"]))
	if len(terminal) != 1 || terminal[0].Type != "provider.request.failed" || terminal[0].Data["status"] != "failed" || terminal[0].Data["error_class"] != "provider_attempt_record_failed" {
		t.Fatalf("retry-attempt persistence failure must have one typed terminal event: %#v", terminal)
	}
}

func TestEngineContextTelemetryPersistsTypedProviderFailure(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	engine.cfg.Runtime.ProviderAutoResume.Enabled = false
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "fail once")); err != nil {
		t.Fatalf("append: %v", err)
	}
	wantErr := &provider.HTTPError{Provider: "fake", Class: "upstream_timeout", Message: "PROVIDER_ERROR_SENTINEL"}
	_, err := engine.Run(context.Background(), meta, state, "", newContextTelemetryAdapter(wantErr), catalog, registry, hookManager)
	if err == nil {
		t.Fatal("expected provider failure")
	}
	var wrapped *ProviderError
	if !errors.As(err, &wrapped) {
		t.Fatalf("expected wrapped provider error, got %T: %v", err, err)
	}
	recorded, loadErr := loadEvents(engine.store, meta.ID)
	if loadErr != nil {
		t.Fatalf("load events: %v", loadErr)
	}
	failed, ok := findEventByType(recorded, "provider.request.failed")
	if !ok {
		t.Fatalf("missing provider.request.failed: %#v", recorded)
	}
	if failed.Data["status"] != "failed" || failed.Data["error_class"] != "upstream_timeout" || failed.Data["request_kind"] != requestKindMain || strings.TrimSpace(stringFromAny(failed.Data["request_id"])) == "" {
		t.Fatalf("unexpected provider failure correlation: %#v", failed.Data)
	}
	encoded, _ := json.Marshal(failed.Data)
	if strings.Contains(string(encoded), "PROVIDER_ERROR_SENTINEL") {
		t.Fatalf("provider lifecycle event leaked error text: %s", encoded)
	}
}

func findRequestEvent(items []events.Event, eventType, requestKind string) (events.Event, bool) {
	for _, item := range items {
		if item.Type == eventType && stringFromAny(item.Data["request_kind"]) == requestKind {
			return item, true
		}
	}
	return events.Event{}, false
}

func terminalRequestEvents(items []events.Event, requestID string) []events.Event {
	result := make([]events.Event, 0, 1)
	for _, item := range items {
		if item.Type != "provider.request.completed" && item.Type != "provider.request.failed" {
			continue
		}
		if stringFromAny(item.Data["request_id"]) == requestID {
			result = append(result, item)
		}
	}
	return result
}
