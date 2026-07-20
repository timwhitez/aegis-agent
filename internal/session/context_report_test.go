package session

import (
	"encoding/json"
	"strings"
	"testing"

	"go-cli-agent/internal/events"
)

const (
	contextReportTestRootID  = "context-report-root"
	contextReportTestChildID = "context-report-child"
	contextReportTestTime    = "2026-07-20T08:00:00Z"
)

func TestStoreContextReportAggregatesLineageAndKeepsUnknownUsageDistinct(t *testing.T) {
	store := NewStore(t.TempDir())
	createContextReportTestSession(t, store, SessionMetadata{
		SchemaVersion:    1,
		ID:               contextReportTestRootID,
		CreatedAt:        contextReportTestTime,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "fake",
		Model:            "fixture",
		CompletionPolicy: CompletionPolicyAutonomous,
		RootSessionID:    contextReportTestRootID,
		ToolProfile:      ToolProfileDefault,
	})
	createContextReportTestSession(t, store, SessionMetadata{
		SchemaVersion:    1,
		ID:               contextReportTestChildID,
		CreatedAt:        "2026-07-20T08:00:01Z",
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "fake",
		Model:            "fixture",
		CompletionPolicy: CompletionPolicyAutonomous,
		ParentSessionID:  contextReportTestRootID,
		RootSessionID:    contextReportTestRootID,
		AgentRole:        "explorer",
		ToolProfile:      ToolProfileExplorerReadOnly,
		QueueJobID:       "job-context-report-child",
		Depth:            1,
	})

	rootSnapshot := RequestBudgetSnapshot{
		SchemaVersion:              1,
		RequestID:                  contextReportTestRootID + ":1:main:0",
		RequestKind:                "main",
		SessionID:                  contextReportTestRootID,
		Turn:                       1,
		Provider:                   "fake",
		Model:                      "fixture",
		WireEstimateSchemaVersion:  1,
		WireBodyBytes:              3600,
		EstimatedInputTokens:       900,
		ReservedOutputTokens:       100,
		SafetyHeadroomTokens:       100,
		EffectiveWindowTokens:      1200,
		RequiredTokens:             1100,
		HeadroomTokens:             100,
		CompactionAction:           "full",
		InlineToolResultCount:      1,
		InlineToolResultBytes:      300,
		CompactedToolResultCount:   1,
		CompactedToolResultBytes:   80,
		PointerizedToolResultCount: 1,
		PointerizedToolResultBytes: 40,
		Fit:                        true,
	}
	childSnapshot := RequestBudgetSnapshot{
		SchemaVersion:             1,
		RequestID:                 contextReportTestChildID + ":1:main:0",
		RequestKind:               "main",
		SessionID:                 contextReportTestChildID,
		Turn:                      1,
		Provider:                  "fake",
		Model:                     "fixture",
		WireEstimateSchemaVersion: 1,
		WireBodyBytes:             1600,
		EstimatedInputTokens:      400,
		ReservedOutputTokens:      100,
		SafetyHeadroomTokens:      100,
		EffectiveWindowTokens:     1000,
		RequiredTokens:            600,
		HeadroomTokens:            400,
		CompactionAction:          "none",
		InlineToolResultCount:     1,
		InlineToolResultBytes:     120,
		Fit:                       true,
	}

	appendContextReportEvents(t, store, contextReportTestRootID, []events.Event{
		contextReportEvent("evt-root-prepared", contextReportTestRootID, "provider.request.prepared", contextReportTestTime, map[string]any{
			"request_id":       rootSnapshot.RequestID,
			"request_kind":     rootSnapshot.RequestKind,
			"turn":             rootSnapshot.Turn,
			"request_sequence": 0,
			"request_budget":   rootSnapshot,
			"metadata_secret":  "sk-PROMPT_SENTINEL",
		}),
		contextReportEvent("evt-root-action", contextReportTestRootID, "provider.request.budget_action", "2026-07-20T08:00:00.100Z", map[string]any{
			"request_id":                    rootSnapshot.RequestID,
			"request_kind":                  "main",
			"pass":                          1,
			"action":                        "pointerize_recoverable_result",
			"before_wire_body_bytes":        4000,
			"after_wire_body_bytes":         3600,
			"before_estimated_input_tokens": 1000,
			"after_estimated_input_tokens":  900,
			"affected_tool_call_ids":        []string{"call-root-1"},
			"affected_count":                1,
		}),
		contextReportEvent("evt-root-compact-started", contextReportTestRootID, "compact.started", "2026-07-20T08:00:00.200Z", map[string]any{
			"request_id":               rootSnapshot.RequestID,
			"request_kind":             "main",
			"turn":                     1,
			"request_sequence":         0,
			"input_chars":              5000,
			"inline_tool_result_count": 1,
			"inline_tool_result_bytes": 900,
			"error":                    "PROMPT_SENTINEL must not enter report",
		}),
		contextReportEvent("evt-root-compact-finished", contextReportTestRootID, "compact.finished", "2026-07-20T08:00:00.300Z", map[string]any{
			"request_id":               rootSnapshot.RequestID,
			"request_kind":             "main",
			"turn":                     1,
			"request_sequence":         0,
			"input_chars":              5000,
			"summary_path":             "artifacts/compactions/summary-fixture.json",
			"semantic_summary_status":  "disabled",
			"recent_message_count":     2,
			"inline_tool_result_count": 1,
			"inline_tool_result_bytes": 300,
		}),
		contextReportEvent("evt-root-tool-after", contextReportTestRootID, "tool.after", "2026-07-20T08:00:00.400Z", map[string]any{
			"call_id":        "call-root-1",
			"display_output": "TOOL_OUTPUT_SENTINEL",
			"metadata":       map[string]any{"persisted_bytes": 4096, "secret": "sk-TOOL_SENTINEL"},
		}),
		contextReportEvent("evt-root-tool-after-duplicate", contextReportTestRootID, "tool.after", "2026-07-20T08:00:00.450Z", map[string]any{
			"call_id":        "call-root-1",
			"display_output": "TOOL_OUTPUT_SENTINEL_DUPLICATE",
			"metadata":       map[string]any{"persisted_bytes": 4096},
		}),
		contextReportEvent("evt-root-completed", contextReportTestRootID, "provider.request.completed", "2026-07-20T08:00:01Z", map[string]any{
			"request_id":           rootSnapshot.RequestID,
			"request_kind":         "main",
			"turn":                 1,
			"request_sequence":     0,
			"stop_reason":          "done_candidate",
			"provider_response_id": "resp-root-1",
			"usage": map[string]any{
				"reported":                    true,
				"source":                      "legacy_inferred",
				"input_tokens":                100,
				"output_tokens":               20,
				"cache_creation_input_tokens": 3,
				"cache_read_input_tokens":     7,
			},
		}),
	})
	appendContextReportEvents(t, store, contextReportTestChildID, []events.Event{
		contextReportEvent("evt-child-prepared", contextReportTestChildID, "provider.request.prepared", "2026-07-20T08:00:01Z", map[string]any{
			"request_id":       childSnapshot.RequestID,
			"request_kind":     childSnapshot.RequestKind,
			"turn":             childSnapshot.Turn,
			"request_sequence": 0,
			"request_budget":   childSnapshot,
		}),
	})
	appendContextReportToolCalls(t, store, contextReportTestRootID, "root", 2)
	appendContextReportToolCalls(t, store, contextReportTestChildID, "child", 1)

	report, err := store.ContextReport(contextReportTestChildID)
	if err != nil {
		t.Fatalf("context report: %v", err)
	}
	if report.SchemaVersion != ContextReportSchemaVersion || report.RequestedSessionID != contextReportTestChildID || report.RootSessionID != contextReportTestRootID {
		t.Fatalf("unexpected report identity: %#v", report)
	}
	if len(report.Sessions) != 2 || report.Sessions[0].SessionID != contextReportTestRootID || report.Sessions[1].SessionID != contextReportTestChildID {
		t.Fatalf("expected root then child details, got %#v", report.Sessions)
	}
	root := report.Sessions[0]
	child := report.Sessions[1]
	if root.Metrics.RequestCount != 1 || root.Metrics.ToolCallCount != 2 || root.Metrics.ArtifactToolOutputBytes != 4096 {
		t.Fatalf("unexpected root metrics: %#v", root.Metrics)
	}
	if root.Metrics.PeakEstimatedInputTokens != 900 || root.Metrics.AggregateEstimatedInputTokens != 900 || root.Metrics.CompactionStartedCount != 1 || root.Metrics.CompactionFinishedCount != 1 {
		t.Fatalf("unexpected root context metrics: %#v", root.Metrics)
	}
	if root.Metrics.FirstEventAt != contextReportTestTime || root.Metrics.LastEventAt != "2026-07-20T08:00:01Z" || root.Metrics.WallTimeMS != 1000 {
		t.Fatalf("sub-second event timestamps produced incorrect session bounds: %#v", root.Metrics)
	}
	if len(root.Requests) != 1 || root.Requests[0].Usage.Reported != true || root.Requests[0].Usage.Source != "legacy_inferred" || root.Requests[0].Usage.InputTokens == nil || *root.Requests[0].Usage.InputTokens != 100 {
		t.Fatalf("expected known root usage: %#v", root.Requests)
	}
	if len(child.Requests) != 1 || child.Requests[0].Status != ContextRequestStatusUnknown || child.Requests[0].Usage.Reported || child.Requests[0].Usage.InputTokens != nil {
		t.Fatalf("prepared-only child request must remain unknown: %#v", child.Requests)
	}
	aggregate := report.Aggregate
	if aggregate.ChildSessionCount != 1 || aggregate.RootPeakEstimatedInputTokens != 900 || aggregate.ChildPeakEstimatedInputTokens != 400 {
		t.Fatalf("unexpected peak lineage metrics: %#v", aggregate)
	}
	if aggregate.RootAggregateEstimatedInputTokens != 900 || aggregate.ChildAggregateEstimatedInputTokens != 400 || aggregate.TotalEstimatedInputTokens != 1300 {
		t.Fatalf("unexpected aggregate input accounting: %#v", aggregate)
	}
	if aggregate.RootProviderViewInlineToolResultBytes != 300 || aggregate.ChildProviderViewInlineToolResultBytes != 120 || aggregate.TotalProviderViewInlineToolResultBytes != 420 ||
		aggregate.RootProviderViewCompactedToolResultBytes != 80 || aggregate.ChildProviderViewCompactedToolResultBytes != 0 || aggregate.TotalProviderViewCompactedToolResultBytes != 80 ||
		aggregate.RootProviderViewPointerizedToolResultBytes != 40 || aggregate.ChildProviderViewPointerizedToolResultBytes != 0 || aggregate.TotalProviderViewPointerizedToolResultBytes != 40 {
		t.Fatalf("unexpected lineage provider-view byte accounting: %#v", aggregate)
	}
	if aggregate.RootProviderUsage.InputTokens != 100 || aggregate.ChildProviderUsage.InputTokens != 0 || aggregate.TotalProviderUsage.InputTokens != 100 || aggregate.UnknownUsageRequestCount != 1 {
		t.Fatalf("unexpected lineage usage accounting: %#v", aggregate)
	}
	if aggregate.FirstEventAt != contextReportTestTime || aggregate.LastEventAt != "2026-07-20T08:00:01Z" || aggregate.WallTimeMS != 1000 {
		t.Fatalf("sub-second event timestamps produced incorrect lineage bounds: %#v", aggregate)
	}

	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	for _, forbidden := range []string{"PROMPT_SENTINEL", "TOOL_OUTPUT_SENTINEL", "sk-TOOL_SENTINEL"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("context report leaked %q: %s", forbidden, encoded)
		}
	}

	reloaded := NewStore(store.root)
	reloadedReport, err := reloaded.ContextReport(contextReportTestRootID)
	if err != nil {
		t.Fatalf("context report after reload: %v", err)
	}
	if reloadedReport.Aggregate != report.Aggregate {
		t.Fatalf("report changed after reload: before=%#v after=%#v", report.Aggregate, reloadedReport.Aggregate)
	}
}

func TestStoreContextReportRejectsForeignRootLineage(t *testing.T) {
	store := NewStore(t.TempDir())
	createContextReportTestSession(t, store, SessionMetadata{
		SchemaVersion: 1, ID: "context-report-foreign-root", CreatedAt: contextReportTestTime,
		Workdir: t.TempDir(), Mode: ModeExec, Provider: "fake", Model: "fixture",
		CompletionPolicy: CompletionPolicyAutonomous, RootSessionID: "context-report-foreign-root",
	})
	createContextReportTestSession(t, store, SessionMetadata{
		SchemaVersion: 1, ID: "context-report-foreign-child", CreatedAt: contextReportTestTime,
		Workdir: t.TempDir(), Mode: ModeExec, Provider: "fake", Model: "fixture",
		CompletionPolicy: CompletionPolicyAutonomous, ParentSessionID: "context-report-foreign-root",
		RootSessionID: "some-other-root", Depth: 1,
	})
	if _, err := store.ContextReport("context-report-foreign-root"); err == nil || !strings.Contains(err.Error(), "root_session_id") {
		t.Fatalf("expected foreign-root lineage rejection, got %v", err)
	}
}

func TestContextProviderUsagePreservesOnlyStableSources(t *testing.T) {
	tests := []struct {
		name       string
		data       map[string]any
		reported   bool
		wantSource string
	}{
		{name: "provider", data: map[string]any{"reported": true, "source": "provider"}, reported: true, wantSource: "provider"},
		{name: "legacy inferred", data: map[string]any{"reported": true, "source": "legacy_inferred", "input_tokens": 7}, reported: true, wantSource: "legacy_inferred"},
		{name: "unknown source defaults to provider", data: map[string]any{"reported": true, "source": "PROMPT_SOURCE_SENTINEL"}, reported: true, wantSource: "provider"},
		{name: "legacy counters infer presence", data: map[string]any{"reported": false, "source": "provider", "output_tokens": 3}, reported: true, wantSource: "legacy_inferred"},
		{name: "absent", data: map[string]any{"reported": false, "source": "provider"}, reported: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage := contextProviderUsage(tt.data)
			if usage.Reported != tt.reported || usage.Source != tt.wantSource {
				t.Fatalf("unexpected usage presence/source: %#v", usage)
			}
			encoded, err := json.Marshal(usage)
			if err != nil {
				t.Fatalf("marshal usage: %v", err)
			}
			if strings.Contains(string(encoded), "PROMPT_SOURCE_SENTINEL") {
				t.Fatalf("unstable source leaked into report: %s", encoded)
			}
		})
	}
}

func createContextReportTestSession(t *testing.T, store *Store, meta SessionMetadata) {
	t.Helper()
	state := State{Status: StatusCompleted, Phase: "complete", Turn: 1, UpdatedAt: meta.CreatedAt}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session %s: %v", meta.ID, err)
	}
}

func appendContextReportEvents(t *testing.T, store *Store, sessionID string, items []events.Event) {
	t.Helper()
	if err := store.AppendEvents(sessionID, items); err != nil {
		t.Fatalf("append events for %s: %v", sessionID, err)
	}
}

func contextReportEvent(id, sessionID, eventType, at string, data map[string]any) events.Event {
	return events.Event{SchemaVersion: events.SchemaVersion, ID: id, SessionID: sessionID, Type: eventType, Time: at, Phase: "fixture", Data: data}
}

func appendContextReportToolCalls(t *testing.T, store *Store, sessionID, prefix string, count int) {
	t.Helper()
	calls := make([]ToolCall, 0, count)
	for i := 0; i < count; i++ {
		calls = append(calls, ToolCall{ID: prefix + "-call-" + string(rune('a'+i)), Name: "read_file", Arguments: json.RawMessage(`{"path":"fixture.txt"}`)})
	}
	message := Message{ID: prefix + "-assistant", Role: "assistant", ToolCalls: calls, CreatedAt: contextReportTestTime}
	if err := store.AppendMessage(sessionID, message); err != nil {
		t.Fatalf("append tool calls for %s: %v", sessionID, err)
	}
}
