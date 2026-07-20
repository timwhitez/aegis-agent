package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"go-cli-agent/internal/events"
	"go-cli-agent/internal/session"
)

const harnessFixtureSchemaVersion = 1

type harnessFixtureScenario struct {
	Name               string                `json:"name"`
	FactSetID          string                `json:"fact_set_id"`
	ScriptedOperations []string              `json:"scripted_operations"`
	Report             session.ContextReport `json:"report"`
}

type harnessFixtureComparison struct {
	BroadRootPeak                   int  `json:"broad_root_peak"`
	NarrowedRootPeak                int  `json:"narrowed_root_peak"`
	DelegatedRootPeak               int  `json:"delegated_root_peak"`
	DelegatedRootAggregate          int  `json:"delegated_root_aggregate"`
	DelegatedChildAggregate         int  `json:"delegated_child_aggregate"`
	DelegatedTotal                  int  `json:"delegated_total"`
	NarrowedRootPeakLTEBroad        bool `json:"narrowed_root_peak_lte_broad"`
	DelegatedRootPeakLTBroad        bool `json:"delegated_root_peak_lt_broad"`
	DelegatedChildAggregateNonZero  bool `json:"delegated_child_aggregate_non_zero"`
	DelegatedTotalSeparatelyVisible bool `json:"delegated_total_separately_visible"`
}

type harnessFixtureOutput struct {
	SchemaVersion int                      `json:"schema_version"`
	Scenarios     []harnessFixtureScenario `json:"scenarios"`
	Comparison    harnessFixtureComparison `json:"comparison"`
}

type fixtureRequest struct {
	SessionID         string
	Turn              int
	EstimatedTokens   int
	RequiredTokens    int
	WireBytes         int
	InlineBytes       int
	PointerizedBytes  int
	InputTokens       int
	OutputTokens      int
	CompactionStarted bool
}

func main() {
	fixture, err := buildHarnessFixture()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "build context harness fixture: %v\n", err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(fixture); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "encode context harness fixture: %v\n", err)
		os.Exit(1)
	}
}

func buildHarnessFixture() (harnessFixtureOutput, error) {
	tempRoot, err := os.MkdirTemp("", "go-cli-agent-context-harness-")
	if err != nil {
		return harnessFixtureOutput{}, err
	}
	defer os.RemoveAll(tempRoot)

	broad, err := buildFixtureScenario(filepath.Join(tempRoot, "broad"), "single_root_broad", "single-root-broad", []fixtureRequest{
		{SessionID: "single-root-broad", Turn: 1, EstimatedTokens: 1200, RequiredTokens: 1400, WireBytes: 4800, InlineBytes: 2400, InputTokens: 1100, OutputTokens: 80, CompactionStarted: true},
	}, nil, []string{"grep broad patterns", "read broad candidate set"})
	if err != nil {
		return harnessFixtureOutput{}, err
	}
	narrowed, err := buildFixtureScenario(filepath.Join(tempRoot, "narrowed"), "single_root_narrowed", "single-root-narrowed", []fixtureRequest{
		{SessionID: "single-root-narrowed", Turn: 1, EstimatedTokens: 700, RequiredTokens: 900, WireBytes: 2800, InlineBytes: 900, InputTokens: 640, OutputTokens: 70},
	}, nil, []string{"grep_files candidate names", "read_file bounded ranges"})
	if err != nil {
		return harnessFixtureOutput{}, err
	}
	delegated, err := buildFixtureScenario(filepath.Join(tempRoot, "delegated"), "delegated_explorer", "delegated-explorer", []fixtureRequest{
		{SessionID: "delegated-explorer", Turn: 1, EstimatedTokens: 500, RequiredTokens: 700, WireBytes: 2000, InlineBytes: 300, InputTokens: 460, OutputTokens: 40},
		{SessionID: "delegated-explorer", Turn: 2, EstimatedTokens: 450, RequiredTokens: 650, WireBytes: 1800, InlineBytes: 240, InputTokens: 420, OutputTokens: 35},
	}, []fixtureRequest{
		{SessionID: "delegated-explorer-child-a", Turn: 1, EstimatedTokens: 800, RequiredTokens: 1000, WireBytes: 3200, InlineBytes: 1600, InputTokens: 740, OutputTokens: 60},
		{SessionID: "delegated-explorer-child-b", Turn: 1, EstimatedTokens: 600, RequiredTokens: 800, WireBytes: 2400, InlineBytes: 1200, InputTokens: 550, OutputTokens: 50},
	}, []string{"root delegates two read-only explorer slices", "explorers return bounded evidence handoffs", "root synthesizes handoffs"})
	if err != nil {
		return harnessFixtureOutput{}, err
	}

	comparison := harnessFixtureComparison{
		BroadRootPeak:           broad.Report.Aggregate.RootPeakEstimatedInputTokens,
		NarrowedRootPeak:        narrowed.Report.Aggregate.RootPeakEstimatedInputTokens,
		DelegatedRootPeak:       delegated.Report.Aggregate.RootPeakEstimatedInputTokens,
		DelegatedRootAggregate:  delegated.Report.Aggregate.RootAggregateEstimatedInputTokens,
		DelegatedChildAggregate: delegated.Report.Aggregate.ChildAggregateEstimatedInputTokens,
		DelegatedTotal:          delegated.Report.Aggregate.TotalEstimatedInputTokens,
	}
	comparison.NarrowedRootPeakLTEBroad = comparison.NarrowedRootPeak <= comparison.BroadRootPeak
	comparison.DelegatedRootPeakLTBroad = comparison.DelegatedRootPeak < comparison.BroadRootPeak
	comparison.DelegatedChildAggregateNonZero = comparison.DelegatedChildAggregate > 0
	comparison.DelegatedTotalSeparatelyVisible = comparison.DelegatedTotal == comparison.DelegatedRootAggregate+comparison.DelegatedChildAggregate

	return harnessFixtureOutput{
		SchemaVersion: harnessFixtureSchemaVersion,
		Scenarios:     []harnessFixtureScenario{broad, narrowed, delegated},
		Comparison:    comparison,
	}, nil
}

func buildFixtureScenario(root, scenarioName, rootSessionID string, rootRequests, childRequests []fixtureRequest, operations []string) (harnessFixtureScenario, error) {
	store := session.NewStore(filepath.Join(root, "sessions"))
	if err := createFixtureSession(store, rootSessionID, "", rootSessionID, "", session.ToolProfileDefault, 0); err != nil {
		return harnessFixtureScenario{}, err
	}
	for _, request := range rootRequests {
		if err := appendFixtureRequest(store, request); err != nil {
			return harnessFixtureScenario{}, err
		}
	}
	for index, request := range childRequests {
		if err := createFixtureSession(store, request.SessionID, rootSessionID, rootSessionID, "explorer", session.ToolProfileExplorerReadOnly, 1); err != nil {
			return harnessFixtureScenario{}, err
		}
		if err := appendFixtureRequest(store, request); err != nil {
			return harnessFixtureScenario{}, err
		}
		if err := appendFixtureToolCalls(store, request.SessionID, fmt.Sprintf("child-%d", index), 4); err != nil {
			return harnessFixtureScenario{}, err
		}
	}
	if err := appendFixtureToolCalls(store, rootSessionID, "root", len(operations)); err != nil {
		return harnessFixtureScenario{}, err
	}
	report, err := store.ContextReport(rootSessionID)
	if err != nil {
		return harnessFixtureScenario{}, err
	}
	return harnessFixtureScenario{
		Name:               scenarioName,
		FactSetID:          "fixed-small-repo-v1",
		ScriptedOperations: append([]string(nil), operations...),
		Report:             report,
	}, nil
}

func createFixtureSession(store *session.Store, id, parentID, rootID, role, toolProfile string, depth int) error {
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               id,
		CreatedAt:        "2026-07-20T08:00:00Z",
		Workdir:          "/fixture/fixed-small-repo",
		Mode:             session.ModeExec,
		Provider:         "fake",
		Model:            "scripted-context-fixture",
		CompletionPolicy: session.CompletionPolicyAutonomous,
		ParentSessionID:  parentID,
		RootSessionID:    rootID,
		AgentRole:        role,
		ToolProfile:      toolProfile,
		Depth:            depth,
	}
	return store.Create(meta, session.State{Status: session.StatusCompleted, Phase: "complete", Turn: 1, UpdatedAt: meta.CreatedAt})
}

func appendFixtureRequest(store *session.Store, request fixtureRequest) error {
	requestID := fmt.Sprintf("%s:%d:main:0", request.SessionID, request.Turn)
	snapshot := session.RequestBudgetSnapshot{
		SchemaVersion:              session.RequestBudgetSnapshotSchemaVersion,
		RequestID:                  requestID,
		RequestKind:                "main",
		SessionID:                  request.SessionID,
		Turn:                       request.Turn,
		Provider:                   "fake",
		Model:                      "scripted-context-fixture",
		WireEstimateSchemaVersion:  1,
		WireBodyBytes:              request.WireBytes,
		EstimatedInputTokens:       request.EstimatedTokens,
		ReservedOutputTokens:       128,
		SafetyHeadroomTokens:       128,
		EffectiveWindowTokens:      2048,
		RequiredTokens:             request.RequiredTokens,
		HeadroomTokens:             2048 - request.RequiredTokens,
		CompactionAction:           "none",
		InlineToolResultCount:      1,
		InlineToolResultBytes:      request.InlineBytes,
		PointerizedToolResultCount: boolInt(request.PointerizedBytes > 0),
		PointerizedToolResultBytes: request.PointerizedBytes,
		Fit:                        true,
	}
	base := fmt.Sprintf("evt-%s-%d", request.SessionID, request.Turn)
	items := []events.Event{
		fixtureEvent(base+"-prepared", request.SessionID, "provider.request.prepared", "2026-07-20T08:00:00Z", map[string]any{
			"request_id": requestID, "request_kind": "main", "turn": request.Turn, "request_sequence": 0,
			"request_budget": snapshot, "raw_prompt": "FIXTURE_PROMPT_SENTINEL", "metadata_value": "sk-FIXTURE_SECRET",
		}),
	}
	if request.CompactionStarted {
		items = append(items,
			fixtureEvent(base+"-compact-started", request.SessionID, "compact.started", "2026-07-20T08:00:00.100Z", map[string]any{
				"request_id": requestID, "request_kind": "main", "turn": request.Turn, "request_sequence": 0, "input_chars": request.WireBytes,
			}),
			fixtureEvent(base+"-compact-finished", request.SessionID, "compact.finished", "2026-07-20T08:00:00.200Z", map[string]any{
				"request_id": requestID, "request_kind": "main", "turn": request.Turn, "request_sequence": 0, "input_chars": request.WireBytes,
				"summary_path": "artifacts/compactions/summary-fixture.json", "semantic_summary_status": "disabled",
			}),
		)
	}
	items = append(items, fixtureEvent(base+"-completed", request.SessionID, "provider.request.completed", "2026-07-20T08:00:01Z", map[string]any{
		"request_id": requestID, "request_kind": "main", "turn": request.Turn, "request_sequence": 0,
		"status": "completed", "stop_reason": "done_candidate", "provider_response_id": base + "-response",
		"usage": map[string]any{"reported": true, "input_tokens": request.InputTokens, "output_tokens": request.OutputTokens},
	}))
	return store.AppendEvents(request.SessionID, items)
}

func appendFixtureToolCalls(store *session.Store, sessionID, prefix string, count int) error {
	if count <= 0 {
		return nil
	}
	calls := make([]session.ToolCall, 0, count)
	for i := 0; i < count; i++ {
		calls = append(calls, session.ToolCall{
			ID:        fmt.Sprintf("%s-call-%d", prefix, i),
			Name:      "read_file",
			Arguments: json.RawMessage(`{"path":"fixture/fact.txt"}`),
		})
	}
	return store.AppendMessage(sessionID, session.Message{
		ID: prefix + "-assistant", Role: "assistant", ToolCalls: calls, CreatedAt: "2026-07-20T08:00:00Z",
	})
}

func fixtureEvent(id, sessionID, eventType, at string, data map[string]any) events.Event {
	return events.Event{SchemaVersion: events.SchemaVersion, ID: id, SessionID: sessionID, Type: eventType, Time: at, Phase: "fixture", Data: data}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
