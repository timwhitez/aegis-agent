package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// messageOnlyEstimateAdapter models a provider whose wire envelope contains
// only the replay messages. It is useful for end-to-end engine tests because
// the request boundary can be selected independently from the large default
// system prompt and tool catalog.
type messageOnlyEstimateAdapter struct {
	calls    int
	requests []provider.TurnRequest
}

func (a *messageOnlyEstimateAdapter) Name() string { return "message-only-estimate" }

func (a *messageOnlyEstimateAdapter) EstimateRequest(req provider.TurnRequest) (provider.WireRequestEstimate, error) {
	return provider.EstimateWireRequest(map[string]any{"messages": req.Messages}, req)
}

func (a *messageOnlyEstimateAdapter) RunTurn(_ context.Context, req provider.TurnRequest, _ provider.EmitFunc) (provider.TurnResult, error) {
	a.calls++
	a.requests = append(a.requests, cloneProviderRequestForBudget(req))
	return provider.TurnResult{
		ToolCalls:  []provider.ToolCall{{ID: "call_finish", Name: "finish", Arguments: json.RawMessage(`{"message":"done"}`)}},
		StopReason: "tool_use",
	}, nil
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

func TestHardFitPointerizesRecoverableToolResultAndStrictlyDecreasesWireEstimate(t *testing.T) {
	adapter := provider.NewFake()
	payload := strings.Repeat("recoverable-payload-", 1600)
	request := provider.TurnRequest{
		SessionID:       "hard-fit-pointer",
		Model:           "fake",
		MaxOutputTokens: 1,
		Messages: []session.Message{
			session.NewAssistantMessage("", "", []session.ToolCall{{ID: "call_artifact", Name: "shell", Arguments: json.RawMessage(`{"command":"generate report"}`)}}),
			session.NewToolMessage([]session.ToolResult{{
				ToolCallID:    "call_artifact",
				Name:          "shell",
				LLMOutput:     payload,
				DisplayOutput: payload,
				Metadata: map[string]any{
					"tool_output_budget_version": session.ToolOutputBudgetVersion,
					"artifact_complete":          true,
					"recoverable":                true,
					"artifact_path":              "artifacts/tool-outputs/call_artifact.log",
					"raw_bytes":                  len(payload),
				},
			}}),
		},
	}
	baseline, err := preflightProviderRequest(adapter, request, requestBudgetPolicy{EffectiveWindowTokens: 1 << 20, UtilizationFactor: 1}, requestBudgetContext{RequestKind: requestKindMain, SessionID: request.SessionID})
	if err != nil {
		t.Fatalf("baseline preflight: %v", err)
	}
	window := baseline.RequiredTokens - len(payload)/16
	fit, err := fitProviderRequestToBudget(adapter, request, requestBudgetPolicy{EffectiveWindowTokens: window, UtilizationFactor: 1}, requestBudgetContext{RequestKind: requestKindMain, SessionID: request.SessionID}, config.Default())
	if err != nil {
		t.Fatalf("hard fit recoverable result: %v", err)
	}
	if !fit.Snapshot.Fit || len(fit.Actions) == 0 || fit.Actions[0].Action != requestBudgetActionPointerizeRecoverableResult {
		t.Fatalf("expected recoverable pointer action and fitting snapshot: %#v", fit)
	}
	for _, action := range fit.Actions {
		if action.AfterWireBodyBytes >= action.BeforeWireBodyBytes {
			t.Fatalf("accepted hard-fit action did not strictly decrease wire bytes: %#v", action)
		}
	}
	got := fit.Request.Messages[1].ToolResults[0]
	if !toolResultIsPointerized(got) || !strings.Contains(got.LLMOutput, "artifacts/tool-outputs/call_artifact.log") {
		t.Fatalf("recoverable result was not replaced with its exact artifact pointer: %#v", got)
	}
	if request.Messages[1].ToolResults[0].LLMOutput != payload {
		t.Fatal("hard fit mutated the caller's provider request")
	}
}

func TestHardFitPointerizesRecoverableReadOnlySourceWithoutClaimingArtifactCompleteness(t *testing.T) {
	adapter := provider.NewFake()
	payload := strings.Repeat("current-source-payload-", 1400)
	request := provider.TurnRequest{
		SessionID:       "hard-fit-source-pointer",
		Model:           "fake",
		MaxOutputTokens: 1,
		Messages: []session.Message{
			session.NewAssistantMessage("", "", []session.ToolCall{{
				ID:        "call_read_source",
				Name:      "read_file",
				Arguments: json.RawMessage(`{"path":"src/current.go","offset":1,"limit":200}`),
			}}),
			session.NewToolMessage([]session.ToolResult{{
				ToolCallID: "call_read_source",
				Name:       "read_file",
				LLMOutput:  payload,
			}}),
			session.NewMessage("user", "keep this latest instruction"),
		},
	}
	original, _ := json.Marshal(request.Messages)
	baseline, err := preflightProviderRequest(adapter, request, requestBudgetPolicy{EffectiveWindowTokens: 1 << 20, UtilizationFactor: 1}, requestBudgetContext{RequestKind: requestKindMain, SessionID: request.SessionID})
	if err != nil {
		t.Fatalf("baseline preflight: %v", err)
	}
	fit, err := fitProviderRequestToBudget(adapter, request, requestBudgetPolicy{EffectiveWindowTokens: baseline.RequiredTokens - len(payload)/16, UtilizationFactor: 1}, requestBudgetContext{RequestKind: requestKindMain, SessionID: request.SessionID}, config.Default())
	if err != nil {
		t.Fatalf("hard fit current source: %v", err)
	}
	if len(fit.Actions) == 0 || fit.Actions[0].Action != requestBudgetActionPointerizeRecoverableResult {
		t.Fatalf("expected source pointer action: %#v", fit.Actions)
	}
	result := fit.Request.Messages[1].ToolResults[0]
	if result.Metadata["compaction_reason"] != "hard_fit_recoverable_source" || result.Metadata["hard_fit_recovery_source"] != "retained_read_only_call" {
		t.Fatalf("source pointer metadata made the wrong recovery claim: %#v", result.Metadata)
	}
	if complete, _ := result.Metadata["artifact_complete"].(bool); complete {
		t.Fatalf("source pointer must not claim a complete artifact: %#v", result.Metadata)
	}
	if !strings.Contains(result.LLMOutput, "call_read_source") || !strings.Contains(string(fit.Request.Messages[0].ToolCalls[0].Arguments), "src/current.go") {
		t.Fatalf("source pointer lost its retained replay query: %#v", fit.Request.Messages)
	}
	afterOriginal, _ := json.Marshal(request.Messages)
	if string(afterOriginal) != string(original) {
		t.Fatal("source pointer hard fit mutated durable/caller messages")
	}
}

func TestHardFitNeverPromotesPartialArtifactToRecoverablePointer(t *testing.T) {
	payload := strings.Repeat("partial-artifact-payload-", 700)
	request := provider.TurnRequest{
		SessionID:       "hard-fit-partial-artifact",
		Model:           "fake",
		MaxOutputTokens: 1,
		Messages: []session.Message{
			session.NewAssistantMessage("", "", []session.ToolCall{{ID: "partial_call", Name: "shell", Arguments: json.RawMessage(`{"command":"report"}`)}}),
			session.NewToolMessage([]session.ToolResult{{
				ToolCallID: "partial_call",
				Name:       "shell",
				LLMOutput:  payload,
				Metadata: map[string]any{
					"tool_output_budget_version": session.ToolOutputBudgetVersion,
					"artifact_complete":          false,
					"artifact_truncated":         true,
					"recoverable":                false,
					"artifact_path":              "artifacts/tool-outputs/partial.log",
					"raw_bytes":                  len(payload) * 2,
				},
			}}),
		},
	}
	fit, err := fitProviderRequestToBudget(provider.NewFake(), request, requestBudgetPolicy{EffectiveWindowTokens: 64, UtilizationFactor: 1}, requestBudgetContext{RequestKind: requestKindMain, SessionID: request.SessionID}, config.Default())
	var unfit *RequestBudgetUnfitError
	if !errors.As(err, &unfit) {
		t.Fatalf("expected unrecoverable latest result to remain unfit, fit=%#v err=%v", fit, err)
	}
	result := fit.Request.Messages[1].ToolResults[0]
	if toolResultIsPointerized(result) || result.Metadata["artifact_complete"] != false || result.Metadata["recoverable"] != false || result.LLMOutput != payload {
		t.Fatalf("partial artifact was promoted or rewritten: %#v", result)
	}
	if containsRequestBudgetAction(fit.Actions, requestBudgetActionPointerizeRecoverableResult) {
		t.Fatalf("partial artifact produced a pointer action: %#v", fit.Actions)
	}
}

func TestHardFitDropsOldestReplayClosureAndPreservesExternalSteerAndLatestToolResult(t *testing.T) {
	adapter := provider.NewFake()
	oldPayload := strings.Repeat("old-tool-evidence-", 1400)
	steer := session.NewMessage("user", "latest steer must survive")
	steer.Meta = map[string]any{"source": "steer", "interrupt": true}
	latestExternal := session.NewMessage("user", "latest external instruction must survive")
	request := provider.TurnRequest{
		SessionID:       "hard-fit-tail",
		Model:           "fake",
		MaxOutputTokens: 1,
		Messages: []session.Message{
			session.NewAssistantMessage("", "", []session.ToolCall{
				{ID: "old_a", ProviderCallID: "provider_old_a", Name: "shell", Arguments: json.RawMessage(`{"command":"old command a"}`)},
				{ID: "old_b", Name: "shell", Arguments: json.RawMessage(`{"command":"old command b"}`)},
			}),
			session.NewToolMessage([]session.ToolResult{
				{ToolCallID: "provider_old_a", Name: "shell", LLMOutput: oldPayload},
				{ToolCallID: "old_b", Name: "shell", LLMOutput: "old sibling"},
			}),
			steer,
			session.NewAssistantMessage("", "", []session.ToolCall{{ID: "latest_call", Name: "read_file", Arguments: json.RawMessage(`{"path":"latest.txt"}`)}}),
			session.NewToolMessage([]session.ToolResult{{ToolCallID: "latest_call", Name: "read_file", LLMOutput: "latest tool evidence"}}),
			latestExternal,
		},
	}
	baseline, err := preflightProviderRequest(adapter, request, requestBudgetPolicy{EffectiveWindowTokens: 1 << 20, UtilizationFactor: 1}, requestBudgetContext{RequestKind: requestKindMain, SessionID: request.SessionID})
	if err != nil {
		t.Fatalf("baseline preflight: %v", err)
	}
	window := baseline.RequiredTokens - len(oldPayload)/16
	fit, err := fitProviderRequestToBudget(adapter, request, requestBudgetPolicy{EffectiveWindowTokens: window, UtilizationFactor: 1}, requestBudgetContext{RequestKind: requestKindMain, SessionID: request.SessionID}, config.Default())
	if err != nil {
		t.Fatalf("hard fit replay-safe tail: %v", err)
	}
	if !containsRequestBudgetAction(fit.Actions, requestBudgetActionDropOldestReplayClosure) {
		t.Fatalf("expected replay-safe tail action: %#v", fit.Actions)
	}
	serialized, _ := json.Marshal(fit.Request.Messages)
	for _, want := range []string{"latest steer must survive", "latest external instruction must survive", "latest_call", "latest tool evidence"} {
		if !strings.Contains(string(serialized), want) {
			t.Fatalf("hard fit dropped protected %q: %s", want, serialized)
		}
	}
	for _, removed := range []string{"provider_old_a", "old_b", "old-tool-evidence"} {
		if strings.Contains(string(serialized), removed) {
			t.Fatalf("hard fit left part of old replay closure %q: %s", removed, serialized)
		}
	}
}

func TestHardFitRemovesSemanticSummaryBeforeReducingDeterministicCore(t *testing.T) {
	adapter := provider.NewFake()
	summary := map[string]any{
		"semantic_summary":            strings.Repeat("optional-semantic-", 1400),
		"current_goal":                map[string]any{"status": "active", "objective": "finish hard fit"},
		"open_items":                  []string{"implement", "validate"},
		"key_paths":                   []string{"internal/runtime/request_budget.go"},
		"latest_external_instruction": map[string]any{"source": "user", "text": "finish hard fit"},
		"latest_steer_constraints":    map[string]any{"source": "steer", "text": "do not drop constraints"},
		"transcript":                  "artifacts/transcript.jsonl",
		"completed_items":             []string{"phase a"},
	}
	summaryJSON, _ := json.Marshal(summary)
	summaryMessage := session.NewMessage("user", compactionReferencePrefix+string(summaryJSON))
	summaryMessage.Meta = map[string]any{"source": "compaction_summary"}
	latest := session.NewMessage("user", "latest user instruction")
	request := provider.TurnRequest{SessionID: "hard-fit-summary", Model: "fake", MaxOutputTokens: 1, Messages: []session.Message{summaryMessage, latest}}
	baseline, err := preflightProviderRequest(adapter, request, requestBudgetPolicy{EffectiveWindowTokens: 1 << 20, UtilizationFactor: 1}, requestBudgetContext{RequestKind: requestKindMain, SessionID: request.SessionID})
	if err != nil {
		t.Fatalf("baseline preflight: %v", err)
	}
	window := baseline.RequiredTokens - len(summary["semantic_summary"].(string))/16
	fit, err := fitProviderRequestToBudget(adapter, request, requestBudgetPolicy{EffectiveWindowTokens: window, UtilizationFactor: 1}, requestBudgetContext{RequestKind: requestKindMain, SessionID: request.SessionID}, config.Default())
	if err != nil {
		t.Fatalf("hard fit summary: %v", err)
	}
	if len(fit.Actions) == 0 || fit.Actions[0].Action != requestBudgetActionDropSemanticSummary {
		t.Fatalf("semantic summary was not the first summary action: %#v", fit.Actions)
	}
	text := fit.Request.Messages[0].Text
	if strings.Contains(text, "optional-semantic") {
		t.Fatalf("semantic summary remained in fitted request: %s", text)
	}
	for _, want := range []string{"current_goal", "open_items", "key_paths", "latest_external_instruction", "latest_steer_constraints", "transcript"} {
		if !strings.Contains(text, want) {
			t.Fatalf("deterministic summary lost required field %q: %s", want, text)
		}
	}
}

func TestHardFitReducesDeterministicSummaryAfterDroppingOptionalSemanticLayer(t *testing.T) {
	adapter := provider.NewFake()
	summary := map[string]any{
		"semantic_summary":            strings.Repeat("optional-semantic-", 500),
		"current_goal":                map[string]any{"status": "active", "objective": "finish hard fit"},
		"open_items":                  []string{"implement", "validate"},
		"key_paths":                   []string{"internal/runtime/request_budget.go"},
		"latest_external_instruction": map[string]any{"source": "user", "text": "finish hard fit"},
		"latest_steer_constraints":    map[string]any{"source": "steer", "text": "preserve constraints"},
		"transcript":                  "artifacts/transcript.jsonl",
		"completed_items":             []string{strings.Repeat("LOW-PRIORITY-COMPLETED-", 1200)},
		"artifact_memory":             []string{strings.Repeat("LOW-PRIORITY-ARTIFACT-", 1200)},
	}
	summaryJSON, _ := json.Marshal(summary)
	summaryMessage := session.NewMessage("user", compactionReferencePrefix+string(summaryJSON))
	summaryMessage.Meta = map[string]any{"source": "compaction_summary"}
	latest := session.NewMessage("user", "latest user instruction")
	request := provider.TurnRequest{SessionID: "hard-fit-summary-core", Model: "fake", MaxOutputTokens: 1, Messages: []session.Message{summaryMessage, latest}}

	coreJSON, _ := json.Marshal(deterministicRequestBudgetSummaryCore(summary))
	coreRequest := cloneProviderRequestForBudget(request)
	coreRequest.Messages[0].Text = compactionReferencePrefix + string(coreJSON)
	coreBudget, err := preflightProviderRequest(adapter, coreRequest, requestBudgetPolicy{EffectiveWindowTokens: 1 << 20, UtilizationFactor: 1}, requestBudgetContext{RequestKind: requestKindMain, SessionID: request.SessionID})
	if err != nil {
		t.Fatalf("core baseline preflight: %v", err)
	}

	fit, err := fitProviderRequestToBudget(adapter, request, requestBudgetPolicy{EffectiveWindowTokens: coreBudget.RequiredTokens, UtilizationFactor: 1}, requestBudgetContext{RequestKind: requestKindMain, SessionID: request.SessionID}, config.Default())
	if err != nil {
		t.Fatalf("hard fit summary core: %v", err)
	}
	if len(fit.Actions) != 2 || fit.Actions[0].Action != requestBudgetActionDropSemanticSummary || fit.Actions[1].Action != requestBudgetActionReduceDeterministicSummary {
		t.Fatalf("unexpected summary reduction order: %#v", fit.Actions)
	}
	text := fit.Request.Messages[0].Text
	for _, dropped := range []string{"optional-semantic", "LOW-PRIORITY-COMPLETED", "LOW-PRIORITY-ARTIFACT"} {
		if strings.Contains(text, dropped) {
			t.Fatalf("deterministic core retained low-priority field %q: %s", dropped, text)
		}
	}
	for _, required := range deterministicSummaryCoreFields {
		if !strings.Contains(text, `"`+required+`"`) {
			t.Fatalf("deterministic core lost required field %q: %s", required, text)
		}
	}
}

func TestHardFitHasFixedPassLimitStrictProgressAndStableSecondExecution(t *testing.T) {
	adapter := provider.NewFake()
	messages := make([]session.Message, 0, maxRequestBudgetShrinkPasses+45)
	for i := 0; i < maxRequestBudgetShrinkPasses+44; i++ {
		messages = append(messages, session.NewMessage("user", strings.Repeat("old removable message ", 12)))
	}
	messages = append(messages, session.NewMessage("user", "latest instruction remains"))
	request := provider.TurnRequest{SessionID: "hard-fit-pass-limit", Model: "fake", MaxOutputTokens: 1, Messages: messages}
	fit, err := fitProviderRequestToBudget(adapter, request, requestBudgetPolicy{EffectiveWindowTokens: 64, UtilizationFactor: 1}, requestBudgetContext{RequestKind: requestKindMain, SessionID: request.SessionID}, config.Default())
	var unfit *RequestBudgetUnfitError
	if !errors.As(err, &unfit) {
		t.Fatalf("expected bounded unfit result, fit=%#v err=%v", fit, err)
	}
	if len(fit.Actions) != maxRequestBudgetShrinkPasses {
		t.Fatalf("hard fit did not stop at fixed pass limit %d: %d", maxRequestBudgetShrinkPasses, len(fit.Actions))
	}
	seenMessageIDs := map[string]struct{}{}
	for index, action := range fit.Actions {
		if action.Pass != index+1 || action.AfterWireBodyBytes >= action.BeforeWireBodyBytes {
			t.Fatalf("action %d lacks strict deterministic progress: %#v", index, action)
		}
		for _, id := range action.AffectedMessageIDs {
			if _, duplicate := seenMessageIDs[id]; duplicate {
				t.Fatalf("message %s was removed by more than one action", id)
			}
			seenMessageIDs[id] = struct{}{}
		}
	}

	// A fitted view is a fixed point: running the gate again neither restores
	// payload nor adds nested markers/actions.
	payload := strings.Repeat("stable-artifact-", 1200)
	stableRequest := provider.TurnRequest{
		SessionID:       "hard-fit-stable",
		Model:           "fake",
		MaxOutputTokens: 1,
		Messages: []session.Message{
			session.NewAssistantMessage("", "", []session.ToolCall{{ID: "stable_call", Name: "shell", Arguments: json.RawMessage(`{"command":"report"}`)}}),
			session.NewToolMessage([]session.ToolResult{{ToolCallID: "stable_call", Name: "shell", LLMOutput: payload, Metadata: map[string]any{
				"tool_output_budget_version": session.ToolOutputBudgetVersion,
				"artifact_complete":          true,
				"recoverable":                true,
				"artifact_path":              "artifacts/tool-outputs/stable.log",
				"raw_bytes":                  len(payload),
			}}}),
		},
	}
	baseline, _ := preflightProviderRequest(adapter, stableRequest, requestBudgetPolicy{EffectiveWindowTokens: 1 << 20, UtilizationFactor: 1}, requestBudgetContext{RequestKind: requestKindMain, SessionID: stableRequest.SessionID})
	first, err := fitProviderRequestToBudget(adapter, stableRequest, requestBudgetPolicy{EffectiveWindowTokens: baseline.RequiredTokens - len(payload)/16, UtilizationFactor: 1}, requestBudgetContext{RequestKind: requestKindMain, SessionID: stableRequest.SessionID}, config.Default())
	if err != nil {
		t.Fatalf("first stable fit: %v", err)
	}
	second, err := fitProviderRequestToBudget(adapter, first.Request, requestBudgetPolicy{EffectiveWindowTokens: baseline.RequiredTokens - len(payload)/16, UtilizationFactor: 1}, requestBudgetContext{RequestKind: requestKindMain, SessionID: stableRequest.SessionID}, config.Default())
	if err != nil {
		t.Fatalf("second stable fit: %v", err)
	}
	firstJSON, _ := json.Marshal(first.Request.Messages)
	secondJSON, _ := json.Marshal(second.Request.Messages)
	if len(second.Actions) != 0 || string(firstJSON) != string(secondJSON) {
		t.Fatalf("second hard fit was not stable: actions=%#v\nfirst=%s\nsecond=%s", second.Actions, firstJSON, secondJSON)
	}
}

func TestHardFitReturnsTypedUnfitForUnshrinkableComponents(t *testing.T) {
	tests := []struct {
		name      string
		request   provider.TurnRequest
		component string
		sentinel  string
	}{
		{
			name: "system prompt",
			request: provider.TurnRequest{
				SessionID: "unfit-system", Model: "fake", MaxOutputTokens: 1,
				SystemPrompt: strings.Repeat("SYSTEM-PROMPT-SENTINEL-", 500),
				Messages:     []session.Message{session.NewMessage("user", "keep")},
			},
			component: requestBudgetComponentSystemPrompt,
			sentinel:  "SYSTEM-PROMPT-SENTINEL",
		},
		{
			name:      "latest external instruction",
			request:   provider.TurnRequest{SessionID: "unfit-user", Model: "fake", MaxOutputTokens: 1, Messages: []session.Message{session.NewMessage("user", strings.Repeat("LATEST-USER-SENTINEL-", 500))}},
			component: requestBudgetComponentLatestExternalInstruction,
			sentinel:  "LATEST-USER-SENTINEL",
		},
		{
			name: "tool schemas",
			request: provider.TurnRequest{SessionID: "unfit-tools", Model: "fake", MaxOutputTokens: 1, Messages: []session.Message{session.NewMessage("user", "keep")}, Tools: []provider.ToolSchema{{
				Name: "oversized", Description: strings.Repeat("TOOL-SCHEMA-SENTINEL-", 500), InputSchema: map[string]any{"type": "object"},
			}}},
			component: requestBudgetComponentToolSchemas,
			sentinel:  "TOOL-SCHEMA-SENTINEL",
		},
		{
			name: "metadata envelope",
			request: provider.TurnRequest{
				SessionID: "unfit-metadata", Model: "fake", MaxOutputTokens: 1,
				Messages: []session.Message{session.NewMessage("user", "keep")},
				Metadata: map[string]any{"opaque": strings.Repeat("METADATA-SENTINEL-", 500)},
			},
			component: requestBudgetComponentMetadataOrProviderEnvelope,
			sentinel:  "METADATA-SENTINEL",
		},
		{
			name: "latest unrecoverable tool result",
			request: provider.TurnRequest{
				SessionID: "unfit-latest-tool", Model: "fake", MaxOutputTokens: 1,
				Messages: []session.Message{
					session.NewAssistantMessage("", "", []session.ToolCall{{ID: "latest_shell", Name: "shell", Arguments: json.RawMessage(`{"command":"run"}`)}}),
					session.NewToolMessage([]session.ToolResult{{ToolCallID: "latest_shell", Name: "shell", LLMOutput: strings.Repeat("LATEST-TOOL-SENTINEL-", 500)}}),
					session.NewMessage("user", "keep latest instruction"),
				},
			},
			component: requestBudgetComponentLatestToolResult,
			sentinel:  "LATEST-TOOL-SENTINEL",
		},
		{
			name: "minimal deterministic compaction summary",
			request: func() provider.TurnRequest {
				summary := map[string]any{
					"current_goal":                strings.Repeat("SUMMARY-SENTINEL-", 500),
					"open_items":                  []string{"open"},
					"key_paths":                   []string{"internal/runtime/request_budget.go"},
					"latest_external_instruction": map[string]any{"text": "continue"},
					"latest_steer_constraints":    map[string]any{"text": "preserve"},
					"transcript":                  "artifacts/transcript.jsonl",
				}
				data, _ := json.Marshal(summary)
				message := session.NewMessage("user", compactionReferencePrefix+string(data))
				message.Meta = map[string]any{"source": "compaction_summary"}
				return provider.TurnRequest{SessionID: "unfit-summary", Model: "fake", MaxOutputTokens: 1, Messages: []session.Message{message}}
			}(),
			component: requestBudgetComponentCompactionSummary,
			sentinel:  "SUMMARY-SENTINEL",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fit, err := fitProviderRequestToBudget(provider.NewFake(), tc.request, requestBudgetPolicy{EffectiveWindowTokens: 64, UtilizationFactor: 1}, requestBudgetContext{RequestKind: requestKindMain, SessionID: tc.request.SessionID}, config.Default())
			var unfit *RequestBudgetUnfitError
			if !errors.As(err, &unfit) {
				t.Fatalf("expected typed request_budget_unfit, fit=%#v err=%v", fit, err)
			}
			if unfit.Code != requestBudgetUnfitCode || unfit.RequestKind != requestKindMain || unfit.BlockingComponent != tc.component || unfit.EstimatedInputTokens <= unfit.AvailableInputTokens || unfit.ReservedOutputTokens != 1 {
				t.Fatalf("unexpected typed unfit facts: %#v", unfit)
			}
			if fit.Snapshot.Fit || fit.Snapshot.RejectionCode != requestBudgetUnfitCode || len(fit.Actions) > maxRequestBudgetShrinkPasses {
				t.Fatalf("unexpected final hard-fit result: %#v", fit)
			}
			payload, _ := json.Marshal(unfit)
			if strings.Contains(unfit.Error(), tc.sentinel) || strings.Contains(string(payload), tc.sentinel) {
				t.Fatalf("typed unfit leaked request content: error=%q payload=%s", unfit.Error(), payload)
			}
		})
	}
}

func containsRequestBudgetAction(actions []RequestBudgetAction, action string) bool {
	for _, item := range actions {
		if item.Action == action {
			return true
		}
	}
	return false
}

func TestHardFitRechecksFullReusedAndDeferredCompactionViews(t *testing.T) {
	for _, compactionAction := range []string{"full", "reused", "deferred"} {
		t.Run(compactionAction, func(t *testing.T) {
			adapter := provider.NewFake()
			request := provider.TurnRequest{
				SessionID:       "hard-fit-" + compactionAction,
				Model:           "fake",
				MaxOutputTokens: 1,
				Messages: []session.Message{
					session.NewMessage("assistant", strings.Repeat("old removable provider view ", 1200)),
					session.NewMessage("user", "latest external instruction"),
				},
			}
			baseline, err := preflightProviderRequest(adapter, request, requestBudgetPolicy{EffectiveWindowTokens: 1 << 20, UtilizationFactor: 1}, requestBudgetContext{RequestKind: requestKindMain, SessionID: request.SessionID, CompactionAction: compactionAction})
			if err != nil {
				t.Fatalf("baseline: %v", err)
			}
			fit, err := fitProviderRequestToBudget(adapter, request, requestBudgetPolicy{EffectiveWindowTokens: baseline.RequiredTokens - 1000, UtilizationFactor: 1}, requestBudgetContext{RequestKind: requestKindMain, SessionID: request.SessionID, CompactionAction: compactionAction}, config.Default())
			if err != nil {
				t.Fatalf("hard fit %s view: %v", compactionAction, err)
			}
			if !fit.Snapshot.Fit || fit.Snapshot.CompactionAction != compactionAction || !containsRequestBudgetAction(fit.Actions, requestBudgetActionDropOldestReplayClosure) {
				t.Fatalf("%s view bypassed final hard fit: %#v", compactionAction, fit)
			}
		})
	}
}

func TestHardFitPreservesOpenAIAnthropicGoogleMultiCallReplay(t *testing.T) {
	providers := []struct {
		name          string
		adapter       provider.Adapter
		makeAssistant func(prefix string, callCount int) session.Message
	}{
		{
			name:    "openai",
			adapter: provider.NewOpenAI("http://127.0.0.1", "test", nil),
			makeAssistant: func(prefix string, callCount int) session.Message {
				calls := make([]session.ToolCall, 0, callCount)
				for index := 0; index < callCount; index++ {
					id := fmt.Sprintf("%s_%d", prefix, index)
					calls = append(calls, session.ToolCall{ID: id, ProviderCallID: id, Name: "shell", Arguments: json.RawMessage(`{"command":"pwd"}`)})
				}
				return session.NewAssistantMessage("", "", calls)
			},
		},
		{
			name:    "anthropic",
			adapter: provider.NewAnthropic("http://127.0.0.1", "test", "2023-06-01", nil),
			makeAssistant: func(prefix string, callCount int) session.Message {
				calls := make([]session.ToolCall, 0, callCount)
				blocks := make([]session.ProviderContentBlock, 0, callCount)
				for index := 0; index < callCount; index++ {
					id := fmt.Sprintf("%s_%d", prefix, index)
					args := json.RawMessage(`{"command":"pwd"}`)
					calls = append(calls, session.ToolCall{ID: id, ProviderCallID: id, Name: "shell", Arguments: args})
					blocks = append(blocks, session.ProviderContentBlock{Provider: "anthropic", Type: "tool_use", ID: id, Name: "shell", Input: args})
				}
				message := session.NewAssistantMessage("", "", calls)
				message.ProviderContentBlocks = blocks
				return message
			},
		},
		{
			name:    "google",
			adapter: provider.NewGoogle("http://127.0.0.1", "test", nil),
			makeAssistant: func(prefix string, callCount int) session.Message {
				calls := make([]session.ToolCall, 0, callCount)
				blocks := make([]session.ProviderContentBlock, 0, callCount)
				for index := 0; index < callCount; index++ {
					id := fmt.Sprintf("%s_%d", prefix, index)
					args := json.RawMessage(`{"command":"pwd"}`)
					calls = append(calls, session.ToolCall{ID: id, ProviderCallID: id, Name: "shell", Arguments: args})
					blocks = append(blocks, session.ProviderContentBlock{Provider: "google", Type: "function_call", ID: id, Name: "shell", Args: args})
				}
				message := session.NewAssistantMessage("", "", calls)
				message.ProviderContentBlocks = blocks
				return message
			},
		},
	}
	for _, tc := range providers {
		for _, strategy := range []string{"pointer", "tail"} {
			t.Run(tc.name+"_"+strategy, func(t *testing.T) {
				oldPayload := strings.Repeat("old-provider-payload-", 1200)
				oldAssistant := tc.makeAssistant("old_call", 2)
				oldResults := session.NewToolMessage([]session.ToolResult{
					{ToolCallID: "old_call_0", Name: "shell", LLMOutput: oldPayload},
					{ToolCallID: "old_call_1", Name: "shell", LLMOutput: "old sibling"},
				})
				if strategy == "pointer" {
					oldResults.ToolResults[0].Metadata = map[string]any{
						"tool_output_budget_version": session.ToolOutputBudgetVersion,
						"artifact_complete":          true,
						"recoverable":                true,
						"artifact_path":              "artifacts/tool-outputs/old-provider.log",
						"raw_bytes":                  len(oldPayload),
					}
				}
				latestAssistant := tc.makeAssistant("latest_call", 1)
				request := provider.TurnRequest{
					SessionID:       "hard-fit-provider-" + tc.name + "-" + strategy,
					Model:           "model",
					MaxOutputTokens: 1,
					Messages: []session.Message{
						oldAssistant,
						oldResults,
						latestAssistant,
						session.NewToolMessage([]session.ToolResult{{ToolCallID: "latest_call_0", Name: "shell", LLMOutput: "latest result"}}),
						session.NewMessage("user", "latest external instruction"),
					},
				}
				baseline, err := preflightProviderRequest(tc.adapter, request, requestBudgetPolicy{EffectiveWindowTokens: 1 << 20, UtilizationFactor: 1}, requestBudgetContext{RequestKind: requestKindMain, SessionID: request.SessionID})
				if err != nil {
					t.Fatalf("baseline provider estimate: %v", err)
				}
				fit, err := fitProviderRequestToBudget(tc.adapter, request, requestBudgetPolicy{EffectiveWindowTokens: baseline.RequiredTokens - len(oldPayload)/16, UtilizationFactor: 1}, requestBudgetContext{RequestKind: requestKindMain, SessionID: request.SessionID}, config.Default())
				if err != nil {
					t.Fatalf("provider hard fit: %v", err)
				}
				if _, err := provider.EstimateAdapterRequest(tc.adapter, fit.Request); err != nil {
					t.Fatalf("fitted provider replay no longer encodes: %v", err)
				}
				calls := map[string]struct{}{}
				results := map[string]struct{}{}
				for _, message := range fit.Request.Messages {
					if message.Role == "assistant" {
						for _, id := range assistantToolCallIDs(message) {
							calls[id] = struct{}{}
						}
					}
					if message.Role == "tool" {
						for _, result := range message.ToolResults {
							results[result.ToolCallID] = struct{}{}
						}
					}
				}
				for resultID := range results {
					if _, ok := calls[resultID]; !ok {
						t.Fatalf("dangling provider result %s after %s: %#v", resultID, strategy, fit.Request.Messages)
					}
				}
				for callID := range calls {
					if _, ok := results[callID]; !ok {
						t.Fatalf("dangling provider call %s after %s: %#v", callID, strategy, fit.Request.Messages)
					}
				}
				if _, ok := calls["latest_call_0"]; !ok {
					t.Fatalf("latest replay batch was removed: %#v", fit.Request.Messages)
				}
				if strategy == "tail" {
					for _, oldID := range []string{"old_call_0", "old_call_1"} {
						if _, ok := calls[oldID]; ok {
							t.Fatalf("old replay closure was only partially removed: %#v", fit.Request.Messages)
						}
					}
				} else if _, ok := calls["old_call_0"]; !ok {
					t.Fatalf("pointer action removed its replay call: %#v", fit.Request.Messages)
				}
			})
		}
	}
}

func TestEngineUsesFittedRequestRecordsActionsAndLeavesDurableMessagesUntouched(t *testing.T) {
	engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
	engine.cfg.Runtime.Compact.SemanticSummary.Enabled = false
	engine.cfg.Runtime.Compact.InputCharThreshold = 1 << 30
	engine.cfg.Runtime.Compact.UtilizationFactor = 1
	meta.ProviderOptions.ContextWindowTokens = 500
	meta.ProviderOptions.MaxOutputTokens = 1
	if err := engine.store.SaveMetadata(meta.ID, meta); err != nil {
		t.Fatalf("save metadata: %v", err)
	}
	old := session.NewMessage("user", strings.Repeat("ENGINE-HARD-FIT-SENTINEL ", 400))
	latest := session.NewMessage("user", "latest instruction survives")
	if err := engine.store.AppendMessage(meta.ID, old); err != nil {
		t.Fatalf("append old message: %v", err)
	}
	if err := engine.store.AppendMessage(meta.ID, latest); err != nil {
		t.Fatalf("append latest message: %v", err)
	}
	before, err := engine.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("load messages before run: %v", err)
	}
	beforeJSON, _ := json.Marshal(before)

	adapter := &messageOnlyEstimateAdapter{}
	result, err := engine.Run(context.Background(), meta, state, "", adapter, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("engine hard fit run: %v", err)
	}
	if result.Status != session.StatusCompleted || adapter.calls != 1 || len(adapter.requests) != 1 {
		t.Fatalf("unexpected fitted provider result=%#v calls=%d requests=%d", result, adapter.calls, len(adapter.requests))
	}
	providerJSON, _ := json.Marshal(adapter.requests[0].Messages)
	if strings.Contains(string(providerJSON), "ENGINE-HARD-FIT-SENTINEL") || !strings.Contains(string(providerJSON), "latest instruction survives") {
		t.Fatalf("provider did not receive the fitted view: %s", providerJSON)
	}
	after, err := engine.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("load messages after run: %v", err)
	}
	if len(after) < len(before) {
		t.Fatalf("engine rewrote durable message history: before=%d after=%d", len(before), len(after))
	}
	afterPrefixJSON, _ := json.Marshal(after[:len(before)])
	if string(afterPrefixJSON) != string(beforeJSON) {
		t.Fatalf("hard fit changed durable message prefix:\nbefore=%s\nafter=%s", beforeJSON, afterPrefixJSON)
	}

	events, err := loadEvents(engine.store, meta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	actionIndex, preparedIndex, callIndex := -1, -1, -1
	for index, event := range events {
		switch event.Type {
		case "provider.request.budget_action":
			if actionIndex < 0 {
				actionIndex = index
			}
		case "provider.request.prepared":
			if preparedIndex < 0 {
				preparedIndex = index
			}
		case "provider.call":
			if callIndex < 0 {
				callIndex = index
			}
		}
	}
	if actionIndex < 0 || preparedIndex <= actionIndex || callIndex <= preparedIndex {
		t.Fatalf("expected budget action -> prepared -> provider.call ordering, got %#v", events)
	}
	prepared := events[preparedIndex]
	if prepared.Data["fit"] != true {
		t.Fatalf("provider.call followed a non-fitting snapshot: %#v", prepared)
	}
	eventJSON, _ := json.Marshal(events)
	if strings.Contains(string(eventJSON), "ENGINE-HARD-FIT-SENTINEL") {
		t.Fatalf("budget telemetry leaked removed request content: %s", eventJSON)
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
	var budgetErr *RequestBudgetUnfitError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("expected typed unfit request error, got %v", err)
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
	if _, ok := findEventByType(events, "provider.request.budget_unfit"); !ok {
		t.Fatalf("expected budget-unfit event, got %#v", events)
	}
	if _, ok := findEventByType(events, "provider.call"); ok {
		t.Fatalf("locally rejected request must not emit provider.call: %#v", events)
	}
	for _, forbidden := range []string{"provider.retry", "provider.auto_resume", "provider.max_tokens_resume"} {
		if _, ok := findEventByType(events, forbidden); ok {
			t.Fatalf("local unfit must not enter %s: %#v", forbidden, events)
		}
	}
	attempts, loadErr := engine.store.LoadProviderAttempts(meta.ID)
	if loadErr != nil {
		t.Fatalf("load provider attempts: %v", loadErr)
	}
	if len(attempts) != 1 || attempts[0].Outcome != "failure" || attempts[0].ErrorClass != requestBudgetUnfitCode {
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
