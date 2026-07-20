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

type fixedEstimateAdapter struct {
	estimate provider.WireRequestEstimate
	calls    int
}

func (a *fixedEstimateAdapter) Name() string { return "fixed-estimate" }

func (a *fixedEstimateAdapter) EstimateRequest(provider.TurnRequest) (provider.WireRequestEstimate, error) {
	return a.estimate, nil
}

func (a *fixedEstimateAdapter) RunTurn(context.Context, provider.TurnRequest, provider.EmitFunc) (provider.TurnResult, error) {
	a.calls++
	return provider.TurnResult{Text: "ok", StopReason: "done_candidate"}, nil
}

type missingEstimateAdapter struct {
	calls int
}

func (a *missingEstimateAdapter) Name() string { return "missing-estimate" }

func (a *missingEstimateAdapter) RunTurn(context.Context, provider.TurnRequest, provider.EmitFunc) (provider.TurnResult, error) {
	a.calls++
	return provider.TurnResult{Text: "unexpected", StopReason: "done_candidate"}, nil
}

func TestRequestBudgetBoundaryAndTypedError(t *testing.T) {
	adapter := &fixedEstimateAdapter{estimate: provider.WireRequestEstimate{
		SchemaVersion:        1,
		WireBodyBytes:        400,
		EstimatedInputTokens: 100,
	}}
	req := provider.TurnRequest{SessionID: "session-1", Model: "model-1", MaxOutputTokens: 10}
	context := requestBudgetContext{RequestKind: requestKindMain, SessionID: req.SessionID, Turn: 7}

	fit, err := preflightProviderRequest(adapter, req, requestBudgetPolicy{EffectiveWindowTokens: 110, UtilizationFactor: 1}, context)
	if err != nil {
		t.Fatalf("expected exact boundary to fit: %v", err)
	}
	if !fit.Fit || fit.HeadroomTokens != 0 || fit.RequiredTokens != 110 {
		t.Fatalf("unexpected exact-boundary snapshot: %#v", fit)
	}

	rejected, err := preflightProviderRequest(adapter, req, requestBudgetPolicy{EffectiveWindowTokens: 109, UtilizationFactor: 1}, context)
	var budgetErr *RequestBudgetExceededError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("expected typed request budget error, snapshot=%#v err=%v", rejected, err)
	}
	if budgetErr.Code != requestBudgetExceededCode || rejected.Fit || rejected.HeadroomTokens != -1 {
		t.Fatalf("unexpected rejected snapshot/error: snapshot=%#v error=%#v", rejected, budgetErr)
	}
	if adapter.calls != 0 {
		t.Fatalf("preflight must not call provider, got %d calls", adapter.calls)
	}
}

func TestRequestBudgetSnapshotReportsToolResultContextStats(t *testing.T) {
	adapter := &fixedEstimateAdapter{estimate: provider.WireRequestEstimate{
		SchemaVersion:        1,
		WireBodyBytes:        400,
		EstimatedInputTokens: 100,
	}}
	req := provider.TurnRequest{
		SessionID: "session-stats",
		Model:     "model-stats",
		Messages: []session.Message{session.NewToolMessage([]session.ToolResult{
			{ToolCallID: "inline", Name: "shell", LLMOutput: "inline"},
			{ToolCallID: "compacted", Name: "shell", LLMOutput: "short", Metadata: map[string]any{"compacted_for_context": true}},
			{ToolCallID: "pointer", Name: "shell", LLMOutput: "ptr", Metadata: map[string]any{"compacted_for_context": true, "ephemeral_provider_view": true, "ephemeral_artifact": "artifact.log"}},
		})},
	}
	snapshot, err := preflightProviderRequest(adapter, req, requestBudgetPolicy{EffectiveWindowTokens: 10000, UtilizationFactor: 1}, requestBudgetContext{RequestKind: requestKindMain, SessionID: req.SessionID})
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if snapshot.InlineToolResultCount != 1 || snapshot.InlineToolResultBytes != len("inline") ||
		snapshot.CompactedToolResultCount != 1 || snapshot.CompactedToolResultBytes != len("short") ||
		snapshot.PointerizedToolResultCount != 1 || snapshot.PointerizedToolResultBytes != len("ptr") {
		t.Fatalf("unexpected result-level snapshot stats: %#v", snapshot)
	}
}

func TestRequestBudgetPolicyCompatibilityDefaults(t *testing.T) {
	for _, configured := range []int{0, -1} {
		policy := newRequestBudgetPolicy("unknown-model", configured, 0)
		if policy.EffectiveWindowTokens != config.DefaultContextWindowTokens {
			t.Fatalf("configured=%d: expected default context window, got %#v", configured, policy)
		}
		if policy.UtilizationFactor != config.DefaultCompactUtilizationFactor {
			t.Fatalf("configured=%d: expected default utilization, got %#v", configured, policy)
		}
	}
	if policy := newRequestBudgetPolicy("unknown-model", 12345, 1); policy.EffectiveWindowTokens != 12345 || policy.UtilizationFactor != 1 {
		t.Fatalf("expected explicit override to win, got %#v", policy)
	}

	adapter := &fixedEstimateAdapter{estimate: provider.WireRequestEstimate{SchemaVersion: 1, WireBodyBytes: 4, EstimatedInputTokens: 1}}
	for _, maxOutput := range []int{0, -5} {
		snapshot, err := preflightProviderRequest(adapter, provider.TurnRequest{SessionID: "s", Model: "m", MaxOutputTokens: maxOutput}, requestBudgetPolicy{EffectiveWindowTokens: 10000, UtilizationFactor: 1}, requestBudgetContext{RequestKind: requestKindMain, SessionID: "s"})
		if err != nil {
			t.Fatalf("max_output_tokens=%d: preflight: %v", maxOutput, err)
		}
		if snapshot.ReservedOutputTokens != defaultRequestOutputReserveTokens || snapshot.OutputReserveSource != "default" {
			t.Fatalf("max_output_tokens=%d: unexpected reserve: %#v", maxOutput, snapshot)
		}
	}
}

func TestRequestBudgetSnapshotOmitsRequestContentAndReadsOlderShape(t *testing.T) {
	adapter := provider.NewFake()
	req := provider.TurnRequest{
		SessionID:    "session-redaction",
		Model:        "fake",
		SystemPrompt: "SYSTEM-SENTINEL-DO-NOT-PERSIST",
		Messages:     []session.Message{session.NewMessage("user", "MESSAGE-SENTINEL-DO-NOT-PERSIST")},
		Tools: []provider.ToolSchema{{
			Name:        "sentinel_tool",
			Description: "TOOL-SENTINEL-DO-NOT-PERSIST",
			InputSchema: map[string]any{"type": "object"},
		}},
		Metadata:        map[string]any{"safe_key": "METADATA-SENTINEL-DO-NOT-PERSIST"},
		MaxOutputTokens: 1,
	}
	snapshot, err := preflightProviderRequest(adapter, req, requestBudgetPolicy{EffectiveWindowTokens: 10000, UtilizationFactor: 1}, requestBudgetContext{RequestKind: requestKindMain, SessionID: req.SessionID, Turn: 2})
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	for _, forbidden := range []string{"SYSTEM-SENTINEL", "MESSAGE-SENTINEL", "TOOL-SENTINEL", "METADATA-SENTINEL"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("snapshot leaked %q: %s", forbidden, payload)
		}
	}
	if snapshot.SchemaVersion != requestBudgetSnapshotSchemaVersion || snapshot.RequestID == "" || !snapshot.Fit {
		t.Fatalf("unexpected snapshot identity: %#v", snapshot)
	}

	var historical RequestBudgetSnapshot
	if err := json.Unmarshal([]byte(`{"schema_version":1,"request_kind":"main","session_id":"old-session","fit":true,"future_field":"ignored"}`), &historical); err != nil {
		t.Fatalf("read historical snapshot: %v", err)
	}
	if historical.SchemaVersion != 1 || historical.SessionID != "old-session" || !historical.Fit {
		t.Fatalf("unexpected historical snapshot: %#v", historical)
	}
}

func TestRequestBudgetMissingEstimatorFailsClosed(t *testing.T) {
	adapter := &missingEstimateAdapter{}
	snapshot, err := preflightProviderRequest(adapter, provider.TurnRequest{SessionID: "s", Model: "m", MaxOutputTokens: 1}, requestBudgetPolicy{EffectiveWindowTokens: 1000, UtilizationFactor: 1}, requestBudgetContext{RequestKind: requestKindMain, SessionID: "s"})
	var preflightErr *RequestBudgetPreflightError
	if !errors.As(err, &preflightErr) || preflightErr.Code != requestEstimatorUnavailableCode {
		t.Fatalf("expected estimator-unavailable error, snapshot=%#v err=%v", snapshot, err)
	}
	if snapshot.Fit || snapshot.RejectionCode != requestEstimatorUnavailableCode || adapter.calls != 0 {
		t.Fatalf("missing estimator did not fail closed: snapshot=%#v calls=%d", snapshot, adapter.calls)
	}
}

func TestEngineMissingEstimatorFailsClosedBeforeRunTurn(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	engine.cfg.Runtime.Compact.SemanticSummary.Enabled = false
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "hello")); err != nil {
		t.Fatalf("append message: %v", err)
	}
	adapter := &missingEstimateAdapter{}
	_, err := engine.Run(context.Background(), meta, state, "", adapter, catalog, registry, hookManager)
	var preflightErr *RequestBudgetPreflightError
	if !errors.As(err, &preflightErr) || preflightErr.Code != requestEstimatorUnavailableCode {
		t.Fatalf("expected engine estimator-unavailable failure, got %v", err)
	}
	if adapter.calls != 0 {
		t.Fatalf("engine called adapter without estimator %d times", adapter.calls)
	}
}

func TestRequestBudgetRequestComponentsCanCrossBoundary(t *testing.T) {
	base := provider.TurnRequest{
		SessionID:        "component-session",
		Model:            "m",
		MaxOutputTokens:  1,
		ProviderProfile:  "fake",
		APIProvider:      "fake",
		ReasoningSummary: "none",
	}
	mutations := []struct {
		name   string
		mutate func(*provider.TurnRequest)
	}{
		{name: "system", mutate: func(req *provider.TurnRequest) { req.SystemPrompt = strings.Repeat("S", 80) }},
		{name: "messages", mutate: func(req *provider.TurnRequest) {
			req.Messages = []session.Message{session.NewMessage("user", strings.Repeat("M", 80))}
		}},
		{name: "tools", mutate: func(req *provider.TurnRequest) {
			req.Tools = []provider.ToolSchema{{Name: "tool", Description: strings.Repeat("T", 80), InputSchema: map[string]any{"type": "object"}}}
		}},
		{name: "metadata", mutate: func(req *provider.TurnRequest) { req.Metadata = map[string]any{"key": strings.Repeat("D", 80)} }},
		{name: "provider envelope", mutate: func(req *provider.TurnRequest) { req.ReasoningEffort = strings.Repeat("E", 80) }},
		{name: "output reserve", mutate: func(req *provider.TurnRequest) { req.MaxOutputTokens = 80 }},
	}

	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			adapter := provider.NewFake()
			baseline, err := preflightProviderRequest(adapter, base, requestBudgetPolicy{EffectiveWindowTokens: 1 << 20, UtilizationFactor: 1}, requestBudgetContext{RequestKind: requestKindMain, SessionID: base.SessionID})
			if err != nil {
				t.Fatalf("baseline preflight: %v", err)
			}
			mutated := base
			tc.mutate(&mutated)
			rejected, err := preflightProviderRequest(adapter, mutated, requestBudgetPolicy{EffectiveWindowTokens: baseline.RequiredTokens, UtilizationFactor: 1}, requestBudgetContext{RequestKind: requestKindMain, SessionID: base.SessionID})
			var budgetErr *RequestBudgetExceededError
			if !errors.As(err, &budgetErr) || rejected.Fit {
				t.Fatalf("expected component to cross baseline boundary: baseline=%#v mutated=%#v err=%v", baseline, rejected, err)
			}
		})
	}
}

func TestRequestBudgetSafetyHeadroomCanCrossBoundary(t *testing.T) {
	adapter := &fixedEstimateAdapter{estimate: provider.WireRequestEstimate{SchemaVersion: 1, WireBodyBytes: 400, EstimatedInputTokens: 100}}
	req := provider.TurnRequest{SessionID: "safety", Model: "m", MaxOutputTokens: 10}
	withoutSafety, err := preflightProviderRequest(adapter, req, requestBudgetPolicy{EffectiveWindowTokens: 110, UtilizationFactor: 1}, requestBudgetContext{RequestKind: requestKindMain, SessionID: req.SessionID})
	if err != nil || !withoutSafety.Fit {
		t.Fatalf("expected no-safety exact boundary to fit: snapshot=%#v err=%v", withoutSafety, err)
	}
	withSafety, err := preflightProviderRequest(adapter, req, requestBudgetPolicy{EffectiveWindowTokens: 110, UtilizationFactor: 0.99}, requestBudgetContext{RequestKind: requestKindMain, SessionID: req.SessionID})
	var budgetErr *RequestBudgetExceededError
	if !errors.As(err, &budgetErr) || withSafety.Fit || withSafety.SafetyHeadroomTokens != 2 {
		t.Fatalf("expected safety headroom to cross boundary: snapshot=%#v err=%v", withSafety, err)
	}
}

func TestEngineRejectsOversizedMainRequestBeforeProviderCall(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	engine.cfg.Runtime.Compact.SemanticSummary.Enabled = false
	engine.cfg.Runtime.Compact.UtilizationFactor = 1
	meta.ProviderOptions.ContextWindowTokens = 16
	meta.ProviderOptions.MaxOutputTokens = 1
	if err := engine.store.SaveMetadata(meta.ID, meta); err != nil {
		t.Fatalf("save metadata: %v", err)
	}
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", strings.Repeat("oversized-main-request ", 40))); err != nil {
		t.Fatalf("append message: %v", err)
	}
	calls := 0
	adapter := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		calls++
		return provider.TurnResult{}, nil
	})
	_, err := engine.Run(context.Background(), meta, state, "", adapter, catalog, registry, hookManager)
	var budgetErr *RequestBudgetExceededError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("expected typed oversized request error, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("oversized main request reached provider %d times", calls)
	}
	events, loadErr := loadEvents(engine.store, meta.ID)
	if loadErr != nil {
		t.Fatalf("load events: %v", loadErr)
	}
	prepared, ok := findEventByType(events, "provider.request.prepared")
	if !ok || prepared.Data["fit"] != false || prepared.Data["request_kind"] != requestKindMain {
		t.Fatalf("expected rejected prepared event, got %#v", prepared)
	}
	if _, ok := findEventByType(events, "provider.request.budget_exceeded"); !ok {
		t.Fatalf("expected budget-exceeded event, got %#v", events)
	}
	if _, ok := findEventByType(events, "provider.call"); ok {
		t.Fatalf("locally rejected request must not emit provider.call: %#v", events)
	}
	attempts, loadErr := engine.store.LoadProviderAttempts(meta.ID)
	if loadErr != nil {
		t.Fatalf("load provider attempts: %v", loadErr)
	}
	if len(attempts) != 1 || attempts[0].Outcome != "failure" || attempts[0].ErrorClass != requestBudgetExceededCode {
		t.Fatalf("expected one local budget failure without retry, got %#v", attempts)
	}
}

func TestSemanticSummaryBudgetRejectionFallsBackToDeterministicCompaction(t *testing.T) {
	engine, meta, state, _, _, _ := newTestEngine(t, session.ModeExec)
	engine.cfg.Runtime.Compact.SemanticSummary.Enabled = true
	engine.cfg.Runtime.Compact.SemanticSummary.MaxInputChars = 4096
	meta.ProviderOptions.ContextWindowTokens = 8
	meta.ProviderOptions.MaxOutputTokens = 1
	if err := engine.store.SaveMetadata(meta.ID, meta); err != nil {
		t.Fatalf("save metadata: %v", err)
	}
	calls := 0
	adapter := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
		calls++
		return provider.TurnResult{Text: "semantic text", StopReason: "done_candidate"}, nil
	})
	profile := compactionContextProfile{
		Provider:              meta.Provider,
		Model:                 meta.Model,
		InputCharThreshold:    1,
		KeepRecentToolResults: 1,
		HysteresisDeltaChars:  4096,
		KeepRecentMessages:    2,
		ContextWindowTokens:   8,
		UtilizationFactor:     1,
	}
	messages := make([]session.Message, 0, 8)
	for i := 0; i < 8; i++ {
		messages = append(messages, session.NewMessage("user", strings.Repeat("earlier context ", 12)))
	}
	view, _, didCompact, err := engine.compactor.build(context.Background(), meta.ID, meta.Workdir, state, messages, nil, nil, profile, 0, 0, engine.semanticSummaryFunc(adapter, meta, state.Turn, profile), func(evt events.Event) error {
		return engine.store.AppendEvent(meta.ID, evt)
	})
	if err != nil {
		t.Fatalf("deterministic compaction must survive semantic rejection: %v", err)
	}
	if !didCompact || len(view) == 0 || !strings.Contains(view[0].Text, compactionReferencePrefix) {
		t.Fatalf("expected deterministic compacted view, got compact=%v view=%#v", didCompact, view)
	}
	if strings.Contains(view[0].Text, `"semantic_summary"`) {
		t.Fatalf("rejected semantic summary must not be included: %s", view[0].Text)
	}
	if calls != 0 {
		t.Fatalf("oversized semantic summary reached provider %d times", calls)
	}
	events, loadErr := loadEvents(engine.store, meta.ID)
	if loadErr != nil {
		t.Fatalf("load events: %v", loadErr)
	}
	var found bool
	for _, evt := range events {
		if evt.Type == "provider.request.prepared" && evt.Data["request_kind"] == requestKindSemanticSummary && evt.Data["fit"] == false {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected semantic-summary rejected snapshot, got %#v", events)
	}
	finished, ok := findEventByType(events, "compact.finished")
	if !ok || finished.Data["semantic_summary_status"] != "failed" {
		t.Fatalf("expected deterministic compaction to record semantic fallback, got %#v", finished)
	}
}
