package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"aegis-agent/internal/events"
)

const (
	RequestBudgetSnapshotSchemaVersion = 1
	ContextReportSchemaVersion         = 1

	ContextRequestStatusCompleted = "completed"
	ContextRequestStatusFailed    = "failed"
	ContextRequestStatusCancelled = "cancelled"
	ContextRequestStatusRejected  = "rejected"
	ContextRequestStatusUnknown   = "unknown"
)

// RequestBudgetSnapshot is the canonical, content-free provider request
// budget record. Runtime hard-fit and read-only telemetry use this exact type
// and JSON representation.
type RequestBudgetSnapshot struct {
	SchemaVersion              int     `json:"schema_version"`
	RequestID                  string  `json:"request_id"`
	RequestKind                string  `json:"request_kind"`
	SessionID                  string  `json:"session_id"`
	Turn                       int     `json:"turn"`
	RequestSequence            int     `json:"request_sequence,omitempty"`
	Provider                   string  `json:"provider"`
	APIProvider                string  `json:"api_provider,omitempty"`
	Model                      string  `json:"model"`
	WireEstimateSchemaVersion  int     `json:"wire_estimate_schema_version"`
	SystemChars                int     `json:"system_chars"`
	MessageCount               int     `json:"message_count"`
	MessagesBytes              int     `json:"messages_bytes"`
	ToolCount                  int     `json:"tool_count"`
	ToolSchemaBytes            int     `json:"tool_schema_bytes"`
	MetadataKeyCount           int     `json:"metadata_key_count"`
	MetadataBytes              int     `json:"metadata_bytes"`
	WireBodyBytes              int     `json:"wire_body_bytes"`
	EstimatedInputTokens       int     `json:"estimated_input_tokens"`
	ReservedOutputTokens       int     `json:"reserved_output_tokens"`
	OutputReserveSource        string  `json:"output_reserve_source"`
	SafetyHeadroomTokens       int     `json:"safety_headroom_tokens"`
	UtilizationFactor          float64 `json:"utilization_factor"`
	EffectiveWindowTokens      int     `json:"effective_window_tokens"`
	RequiredTokens             int     `json:"required_tokens"`
	HeadroomTokens             int     `json:"headroom_tokens"`
	CompactionAction           string  `json:"compaction_action"`
	CompactionSummaryID        string  `json:"compaction_summary_id,omitempty"`
	InlineToolResultCount      int     `json:"inline_tool_result_count"`
	InlineToolResultBytes      int     `json:"inline_tool_result_bytes"`
	CompactedToolResultCount   int     `json:"compacted_tool_result_count"`
	CompactedToolResultBytes   int     `json:"compacted_tool_result_bytes"`
	PointerizedToolResultCount int     `json:"pointerized_tool_result_count"`
	PointerizedToolResultBytes int     `json:"pointerized_tool_result_bytes"`
	Fit                        bool    `json:"fit"`
	RejectionCode              string  `json:"rejection_code,omitempty"`
}

type RequestBudgetAction struct {
	SchemaVersion              int      `json:"schema_version"`
	Pass                       int      `json:"pass"`
	Action                     string   `json:"action"`
	BeforeWireBodyBytes        int      `json:"before_wire_body_bytes"`
	AfterWireBodyBytes         int      `json:"after_wire_body_bytes"`
	BeforeEstimatedInputTokens int      `json:"before_estimated_input_tokens"`
	AfterEstimatedInputTokens  int      `json:"after_estimated_input_tokens"`
	AffectedMessageIDs         []string `json:"affected_message_ids,omitempty"`
	AffectedToolCallIDs        []string `json:"affected_tool_call_ids,omitempty"`
	AffectedCount              int      `json:"affected_count"`
}

type ContextProviderUsage struct {
	Reported                 bool   `json:"reported"`
	Source                   string `json:"source,omitempty"`
	InputTokens              *int   `json:"input_tokens,omitempty"`
	OutputTokens             *int   `json:"output_tokens,omitempty"`
	CacheCreationInputTokens *int   `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     *int   `json:"cache_read_input_tokens,omitempty"`
}

type ContextUsageTotals struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

type ContextCompactionEvent struct {
	Type                       string `json:"type"`
	Time                       string `json:"time"`
	InputChars                 int    `json:"input_chars,omitempty"`
	ThresholdSource            string `json:"threshold_source,omitempty"`
	ContextWindowTokens        int    `json:"context_window_tokens,omitempty"`
	RecentMessageCount         int    `json:"recent_message_count,omitempty"`
	KeepRecentMessages         int    `json:"keep_recent_messages,omitempty"`
	SemanticSummaryStatus      string `json:"semantic_summary_status,omitempty"`
	SummaryID                  string `json:"summary_id,omitempty"`
	InlineToolResultCount      int    `json:"inline_tool_result_count,omitempty"`
	InlineToolResultBytes      int    `json:"inline_tool_result_bytes,omitempty"`
	CompactedToolResultCount   int    `json:"compacted_tool_result_count,omitempty"`
	CompactedToolResultBytes   int    `json:"compacted_tool_result_bytes,omitempty"`
	PointerizedToolResultCount int    `json:"pointerized_tool_result_count,omitempty"`
	PointerizedToolResultBytes int    `json:"pointerized_tool_result_bytes,omitempty"`
}

type ContextRequestReport struct {
	RequestID           string                   `json:"request_id"`
	RequestKind         string                   `json:"request_kind"`
	SessionID           string                   `json:"session_id"`
	Turn                int                      `json:"turn"`
	RequestSequence     int                      `json:"request_sequence"`
	PreparedAt          string                   `json:"prepared_at,omitempty"`
	FinishedAt          string                   `json:"finished_at,omitempty"`
	DurationMS          int64                    `json:"duration_ms,omitempty"`
	Status              string                   `json:"status"`
	Budget              *RequestBudgetSnapshot   `json:"budget,omitempty"`
	BudgetActions       []RequestBudgetAction    `json:"budget_actions,omitempty"`
	CompactionEvents    []ContextCompactionEvent `json:"compaction_events,omitempty"`
	TransportRetryCount int                      `json:"transport_retry_count"`
	Usage               ContextProviderUsage     `json:"usage"`
	StopReason          string                   `json:"stop_reason,omitempty"`
	ProviderResponseID  string                   `json:"provider_response_id,omitempty"`
	ErrorClass          string                   `json:"error_class,omitempty"`
	RejectionCode       string                   `json:"rejection_code,omitempty"`
}

type ContextSessionMetrics struct {
	RequestCount                           int                `json:"request_count"`
	MainRequestCount                       int                `json:"main_request_count"`
	SemanticSummaryRequestCount            int                `json:"semantic_summary_request_count"`
	CompletedRequestCount                  int                `json:"completed_request_count"`
	FailedRequestCount                     int                `json:"failed_request_count"`
	CancelledRequestCount                  int                `json:"cancelled_request_count"`
	RejectedRequestCount                   int                `json:"rejected_request_count"`
	UnknownRequestCount                    int                `json:"unknown_request_count"`
	UnknownUsageRequestCount               int                `json:"unknown_usage_request_count"`
	TurnCount                              int                `json:"turn_count"`
	ToolCallCount                          int                `json:"tool_call_count"`
	CompactionStartedCount                 int                `json:"compaction_started_count"`
	CompactionFinishedCount                int                `json:"compaction_finished_count"`
	CompactionReusedCount                  int                `json:"compaction_reused_count"`
	CompactionDeferredCount                int                `json:"compaction_deferred_count"`
	PeakEstimatedInputTokens               int                `json:"peak_estimated_input_tokens"`
	PeakRequiredTokens                     int                `json:"peak_required_tokens"`
	PeakWireBodyBytes                      int                `json:"peak_wire_body_bytes"`
	AggregateEstimatedInputTokens          int                `json:"aggregate_estimated_input_tokens"`
	ProviderViewInlineToolResultBytes      int                `json:"provider_view_inline_tool_result_bytes"`
	ProviderViewCompactedToolResultBytes   int                `json:"provider_view_compacted_tool_result_bytes"`
	ProviderViewPointerizedToolResultBytes int                `json:"provider_view_pointerized_tool_result_bytes"`
	ArtifactToolOutputBytes                int                `json:"artifact_tool_output_bytes"`
	ProviderUsage                          ContextUsageTotals `json:"provider_usage"`
	FirstEventAt                           string             `json:"first_event_at,omitempty"`
	LastEventAt                            string             `json:"last_event_at,omitempty"`
	WallTimeMS                             int64              `json:"wall_time_ms"`
}

type ContextSessionReport struct {
	SessionID       string                 `json:"session_id"`
	ParentSessionID string                 `json:"parent_session_id,omitempty"`
	RootSessionID   string                 `json:"root_session_id"`
	QueueJobID      string                 `json:"queue_job_id,omitempty"`
	AgentRole       string                 `json:"agent_role,omitempty"`
	ToolProfile     string                 `json:"tool_profile,omitempty"`
	Metrics         ContextSessionMetrics  `json:"metrics"`
	Requests        []ContextRequestReport `json:"requests"`
}

type ContextLineageAggregate struct {
	SessionCount                                int                `json:"session_count"`
	ChildSessionCount                           int                `json:"child_session_count"`
	RootPeakEstimatedInputTokens                int                `json:"root_peak_estimated_input_tokens"`
	ChildPeakEstimatedInputTokens               int                `json:"child_peak_estimated_input_tokens"`
	RootPeakRequiredTokens                      int                `json:"root_peak_required_tokens"`
	ChildPeakRequiredTokens                     int                `json:"child_peak_required_tokens"`
	RootPeakWireBodyBytes                       int                `json:"root_peak_wire_body_bytes"`
	ChildPeakWireBodyBytes                      int                `json:"child_peak_wire_body_bytes"`
	RootAggregateEstimatedInputTokens           int                `json:"root_aggregate_estimated_input_tokens"`
	ChildAggregateEstimatedInputTokens          int                `json:"child_aggregate_estimated_input_tokens"`
	TotalEstimatedInputTokens                   int                `json:"total_estimated_input_tokens"`
	RootRequestCount                            int                `json:"root_request_count"`
	ChildRequestCount                           int                `json:"child_request_count"`
	TotalRequestCount                           int                `json:"total_request_count"`
	RootTurnCount                               int                `json:"root_turn_count"`
	ChildTurnCount                              int                `json:"child_turn_count"`
	TotalTurnCount                              int                `json:"total_turn_count"`
	RootToolCallCount                           int                `json:"root_tool_call_count"`
	ChildToolCallCount                          int                `json:"child_tool_call_count"`
	TotalToolCallCount                          int                `json:"total_tool_call_count"`
	RootCompactionCount                         int                `json:"root_compaction_count"`
	ChildCompactionCount                        int                `json:"child_compaction_count"`
	TotalCompactionCount                        int                `json:"total_compaction_count"`
	RootProviderViewInlineToolResultBytes       int                `json:"root_provider_view_inline_tool_result_bytes"`
	ChildProviderViewInlineToolResultBytes      int                `json:"child_provider_view_inline_tool_result_bytes"`
	TotalProviderViewInlineToolResultBytes      int                `json:"total_provider_view_inline_tool_result_bytes"`
	RootProviderViewCompactedToolResultBytes    int                `json:"root_provider_view_compacted_tool_result_bytes"`
	ChildProviderViewCompactedToolResultBytes   int                `json:"child_provider_view_compacted_tool_result_bytes"`
	TotalProviderViewCompactedToolResultBytes   int                `json:"total_provider_view_compacted_tool_result_bytes"`
	RootProviderViewPointerizedToolResultBytes  int                `json:"root_provider_view_pointerized_tool_result_bytes"`
	ChildProviderViewPointerizedToolResultBytes int                `json:"child_provider_view_pointerized_tool_result_bytes"`
	TotalProviderViewPointerizedToolResultBytes int                `json:"total_provider_view_pointerized_tool_result_bytes"`
	RootArtifactToolOutputBytes                 int                `json:"root_artifact_tool_output_bytes"`
	ChildArtifactToolOutputBytes                int                `json:"child_artifact_tool_output_bytes"`
	TotalArtifactToolOutputBytes                int                `json:"total_artifact_tool_output_bytes"`
	RootProviderUsage                           ContextUsageTotals `json:"root_provider_usage"`
	ChildProviderUsage                          ContextUsageTotals `json:"child_provider_usage"`
	TotalProviderUsage                          ContextUsageTotals `json:"total_provider_usage"`
	UnknownUsageRequestCount                    int                `json:"unknown_usage_request_count"`
	FirstEventAt                                string             `json:"first_event_at,omitempty"`
	LastEventAt                                 string             `json:"last_event_at,omitempty"`
	WallTimeMS                                  int64              `json:"wall_time_ms"`
}

type ContextReportTruncation struct {
	Truncated           bool `json:"truncated"`
	OmittedSessionCount int  `json:"omitted_session_count"`
	OmittedRequestCount int  `json:"omitted_request_count"`
}

type ContextReport struct {
	SchemaVersion      int                      `json:"schema_version"`
	RequestedSessionID string                   `json:"requested_session_id"`
	RootSessionID      string                   `json:"root_session_id"`
	Sessions           []ContextSessionReport   `json:"sessions"`
	Aggregate          ContextLineageAggregate  `json:"aggregate"`
	Truncation         *ContextReportTruncation `json:"truncation,omitempty"`
}

type contextRequestBuilder struct {
	report        ContextRequestReport
	lifecycleSeen bool
	legacyStopped bool
}

// ContextReport derives a read-only report from the canonical session files.
// It never writes a report artifact or changes runtime state.
func (s *Store) ContextReport(requestedSessionID string) (ContextReport, error) {
	requestedMeta, err := s.LoadMetadata(requestedSessionID)
	if err != nil {
		return ContextReport{}, err
	}
	rootSessionID, err := s.resolveContextReportRoot(requestedMeta)
	if err != nil {
		return ContextReport{}, err
	}
	lineage, err := s.contextReportLineage(rootSessionID, requestedSessionID)
	if err != nil {
		return ContextReport{}, err
	}
	report := ContextReport{
		SchemaVersion:      ContextReportSchemaVersion,
		RequestedSessionID: requestedSessionID,
		RootSessionID:      rootSessionID,
		Sessions:           make([]ContextSessionReport, 0, len(lineage)),
	}
	for _, meta := range lineage {
		sessionReport, err := s.contextSessionReport(meta, rootSessionID)
		if err != nil {
			return ContextReport{}, fmt.Errorf("build context report for session %s: %w", meta.ID, err)
		}
		report.Sessions = append(report.Sessions, sessionReport)
	}
	report.Aggregate = aggregateContextLineage(report.Sessions, rootSessionID)
	return report, nil
}

func (s *Store) resolveContextReportRoot(meta SessionMetadata) (string, error) {
	rootID := strings.TrimSpace(meta.RootSessionID)
	if rootID == "" && strings.TrimSpace(meta.ParentSessionID) == "" {
		rootID = meta.ID
	}
	if rootID == "" {
		seen := map[string]struct{}{meta.ID: {}}
		current := meta
		for strings.TrimSpace(current.ParentSessionID) != "" {
			parentID := strings.TrimSpace(current.ParentSessionID)
			if _, exists := seen[parentID]; exists {
				return "", fmt.Errorf("context lineage cycle while resolving root_session_id at %s", parentID)
			}
			seen[parentID] = struct{}{}
			parent, err := s.LoadMetadata(parentID)
			if err != nil {
				return "", fmt.Errorf("load parent session %s while resolving root_session_id: %w", parentID, err)
			}
			current = parent
		}
		rootID = current.ID
	}
	root, err := s.LoadMetadata(rootID)
	if err != nil {
		return "", fmt.Errorf("load root_session_id %s: %w", rootID, err)
	}
	if strings.TrimSpace(root.ParentSessionID) != "" {
		return "", fmt.Errorf("root_session_id %s has parent_session_id %s", rootID, root.ParentSessionID)
	}
	if value := strings.TrimSpace(root.RootSessionID); value != "" && value != rootID {
		return "", fmt.Errorf("root session %s has foreign root_session_id %s", rootID, value)
	}
	return rootID, nil
}

func (s *Store) contextReportLineage(rootSessionID, requestedSessionID string) ([]SessionMetadata, error) {
	visited := map[string]struct{}{}
	lineage := make([]SessionMetadata, 0, 4)
	requestedFound := false
	var walk func(string, string) error
	walk = func(sessionID, expectedParentID string) error {
		if _, exists := visited[sessionID]; exists {
			return fmt.Errorf("context lineage cycle or duplicate session id %s", sessionID)
		}
		visited[sessionID] = struct{}{}
		meta, err := s.LoadMetadata(sessionID)
		if err != nil {
			return err
		}
		if sessionID == rootSessionID {
			if strings.TrimSpace(meta.ParentSessionID) != "" {
				return fmt.Errorf("root session %s has parent_session_id %s", sessionID, meta.ParentSessionID)
			}
		} else {
			if strings.TrimSpace(meta.ParentSessionID) != expectedParentID {
				return fmt.Errorf("session %s parent_session_id %s does not match %s", sessionID, meta.ParentSessionID, expectedParentID)
			}
			if strings.TrimSpace(meta.RootSessionID) != rootSessionID {
				return fmt.Errorf("session %s root_session_id %s does not match %s", sessionID, meta.RootSessionID, rootSessionID)
			}
		}
		if sessionID == requestedSessionID {
			requestedFound = true
		}
		lineage = append(lineage, meta)
		children, err := s.ListChildren(sessionID, -1)
		if err != nil {
			return err
		}
		for _, child := range children {
			if err := walk(child.ID, sessionID); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(rootSessionID, ""); err != nil {
		return nil, err
	}
	if !requestedFound {
		return nil, fmt.Errorf("requested session %s is not reachable from root_session_id %s", requestedSessionID, rootSessionID)
	}
	return lineage, nil
}

func (s *Store) contextSessionReport(meta SessionMetadata, rootSessionID string) (ContextSessionReport, error) {
	report := ContextSessionReport{
		SessionID:       meta.ID,
		ParentSessionID: meta.ParentSessionID,
		RootSessionID:   rootSessionID,
		QueueJobID:      meta.QueueJobID,
		AgentRole:       meta.AgentRole,
		ToolProfile:     meta.ToolProfile,
		Requests:        []ContextRequestReport{},
	}
	if err := s.VisitMessages(meta.ID, func(message Message) error {
		report.Metrics.ToolCallCount += len(message.ToolCalls)
		return nil
	}); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return ContextSessionReport{}, fmt.Errorf("visit messages: %w", err)
	}

	requests := map[string]*contextRequestBuilder{}
	artifactBytes := map[string]int{}
	getRequest := func(requestID string, data map[string]any) *contextRequestBuilder {
		requestID = strings.TrimSpace(requestID)
		if requestID == "" {
			return nil
		}
		if current := requests[requestID]; current != nil {
			return current
		}
		builder := &contextRequestBuilder{report: ContextRequestReport{
			RequestID:       requestID,
			RequestKind:     strings.TrimSpace(contextString(data, "request_kind")),
			SessionID:       meta.ID,
			Turn:            contextInt(data, "turn"),
			RequestSequence: contextInt(data, "request_sequence"),
			Status:          ContextRequestStatusUnknown,
			Usage:           ContextProviderUsage{Reported: false},
		}}
		requests[requestID] = builder
		return builder
	}

	err := s.VisitEvents(meta.ID, func(evt events.Event) error {
		updateContextEventBounds(&report.Metrics, evt.Time)
		data := evt.Data
		requestID := strings.TrimSpace(contextString(data, "request_id"))
		switch evt.Type {
		case "provider.request.prepared":
			builder := getRequest(requestID, data)
			if builder == nil {
				return nil
			}
			budget, ok, err := decodeRequestBudgetSnapshot(data["request_budget"])
			if err != nil {
				return err
			}
			if ok {
				if budget.SchemaVersion != RequestBudgetSnapshotSchemaVersion {
					return fmt.Errorf("request %s has unsupported budget schema_version %d", requestID, budget.SchemaVersion)
				}
				if strings.TrimSpace(budget.RequestID) != requestID || strings.TrimSpace(budget.SessionID) != meta.ID {
					return fmt.Errorf("request budget correlation mismatch for %s", requestID)
				}
				builder.report.Budget = &budget
				builder.report.RequestKind = budget.RequestKind
				builder.report.Turn = budget.Turn
				builder.report.RequestSequence = budget.RequestSequence
				builder.report.RejectionCode = budget.RejectionCode
			}
			if builder.report.PreparedAt == "" {
				builder.report.PreparedAt = evt.Time
			}
		case "provider.request.budget_action":
			builder := getRequest(requestID, data)
			if builder == nil {
				return nil
			}
			action, err := decodeRequestBudgetAction(data)
			if err != nil {
				return err
			}
			builder.report.BudgetActions = append(builder.report.BudgetActions, action)
		case "compact.started", "compact.finished", "compact.reused", "compact.deferred":
			incrementCompactionMetric(&report.Metrics, evt.Type)
			builder := getRequest(requestID, data)
			if builder != nil {
				builder.report.CompactionEvents = append(builder.report.CompactionEvents, contextCompactionEvent(evt))
			}
		case "provider.retry":
			builder := getRequest(requestID, data)
			if builder != nil {
				builder.report.TransportRetryCount++
			}
		case "provider.request.completed":
			builder := getRequest(requestID, data)
			if builder == nil {
				return nil
			}
			builder.lifecycleSeen = true
			builder.report.Status = ContextRequestStatusCompleted
			builder.report.FinishedAt = evt.Time
			builder.report.StopReason = strings.TrimSpace(contextString(data, "stop_reason"))
			builder.report.ProviderResponseID = strings.TrimSpace(contextString(data, "provider_response_id"))
			builder.report.Usage = contextProviderUsage(data["usage"])
		case "provider.request.failed":
			builder := getRequest(requestID, data)
			if builder == nil {
				return nil
			}
			builder.lifecycleSeen = true
			builder.report.Status = normalizeContextRequestStatus(contextString(data, "status"))
			builder.report.FinishedAt = evt.Time
			builder.report.ErrorClass = strings.TrimSpace(contextString(data, "error_class"))
			builder.report.RejectionCode = strings.TrimSpace(contextString(data, "rejection_code"))
			builder.report.Usage = contextProviderUsage(data["usage"])
		case "provider.request.rejected":
			builder := getRequest(requestID, data)
			if builder != nil && !builder.lifecycleSeen {
				builder.report.Status = ContextRequestStatusRejected
				builder.report.FinishedAt = evt.Time
				builder.report.RejectionCode = strings.TrimSpace(contextString(data, "rejection_code"))
			}
		case "turn.stopped":
			builder := getRequest(requestID, data)
			if builder != nil && !builder.lifecycleSeen {
				builder.legacyStopped = true
				builder.report.Status = ContextRequestStatusCompleted
				builder.report.FinishedAt = evt.Time
				builder.report.StopReason = strings.TrimSpace(contextString(data, "stop_reason"))
				builder.report.ProviderResponseID = strings.TrimSpace(contextString(data, "provider_response_id"))
				builder.report.Usage = contextProviderUsage(data["usage"])
			}
		case "tool.after":
			callID := strings.TrimSpace(contextString(data, "call_id"))
			if callID == "" {
				return nil
			}
			metadata, _ := data["metadata"].(map[string]any)
			persisted := contextInt(metadata, "persisted_bytes")
			if persisted > artifactBytes[callID] {
				artifactBytes[callID] = persisted
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return ContextSessionReport{}, fmt.Errorf("visit events: %w", err)
	}
	for _, value := range artifactBytes {
		report.Metrics.ArtifactToolOutputBytes += value
	}

	requestIDs := make([]string, 0, len(requests))
	for requestID := range requests {
		requestIDs = append(requestIDs, requestID)
	}
	sort.Slice(requestIDs, func(i, j int) bool {
		left := requests[requestIDs[i]].report
		right := requests[requestIDs[j]].report
		if left.PreparedAt == right.PreparedAt {
			return left.RequestID < right.RequestID
		}
		if left.PreparedAt == "" {
			return false
		}
		if right.PreparedAt == "" {
			return true
		}
		return contextTimestampBefore(left.PreparedAt, right.PreparedAt)
	})
	mainTurns := map[int]struct{}{}
	for _, requestID := range requestIDs {
		request := requests[requestID].report
		request.DurationMS = contextDurationMS(request.PreparedAt, request.FinishedAt)
		report.Requests = append(report.Requests, request)
		accumulateContextRequestMetrics(&report.Metrics, request, mainTurns)
	}
	report.Metrics.TurnCount = len(mainTurns)
	report.Metrics.WallTimeMS = contextDurationMS(report.Metrics.FirstEventAt, report.Metrics.LastEventAt)
	return report, nil
}

func decodeRequestBudgetSnapshot(value any) (RequestBudgetSnapshot, bool, error) {
	if value == nil {
		return RequestBudgetSnapshot{}, false, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return RequestBudgetSnapshot{}, false, fmt.Errorf("marshal request budget snapshot: %w", err)
	}
	var snapshot RequestBudgetSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return RequestBudgetSnapshot{}, false, fmt.Errorf("decode request budget snapshot: %w", err)
	}
	return snapshot, true, nil
}

func decodeRequestBudgetAction(data map[string]any) (RequestBudgetAction, error) {
	value := any(data)
	if nested := data["budget_action"]; nested != nil {
		value = nested
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return RequestBudgetAction{}, fmt.Errorf("marshal request budget action: %w", err)
	}
	var action RequestBudgetAction
	if err := json.Unmarshal(encoded, &action); err != nil {
		return RequestBudgetAction{}, fmt.Errorf("decode request budget action: %w", err)
	}
	if action.SchemaVersion == 0 {
		action.SchemaVersion = RequestBudgetSnapshotSchemaVersion
	}
	return action, nil
}

func contextCompactionEvent(evt events.Event) ContextCompactionEvent {
	data := evt.Data
	summaryID := strings.TrimSpace(contextString(data, "summary_path"))
	if summaryID == "" {
		summaryID = strings.TrimSpace(contextString(data, "summary_source"))
	}
	return ContextCompactionEvent{
		Type:                       evt.Type,
		Time:                       evt.Time,
		InputChars:                 contextInt(data, "input_chars"),
		ThresholdSource:            strings.TrimSpace(contextString(data, "threshold_source")),
		ContextWindowTokens:        contextInt(data, "context_window_tokens"),
		RecentMessageCount:         contextInt(data, "recent_message_count"),
		KeepRecentMessages:         contextInt(data, "keep_recent_messages"),
		SemanticSummaryStatus:      strings.TrimSpace(contextString(data, "semantic_summary_status")),
		SummaryID:                  summaryID,
		InlineToolResultCount:      contextInt(data, "inline_tool_result_count"),
		InlineToolResultBytes:      contextInt(data, "inline_tool_result_bytes"),
		CompactedToolResultCount:   contextInt(data, "compacted_tool_result_count"),
		CompactedToolResultBytes:   contextInt(data, "compacted_tool_result_bytes"),
		PointerizedToolResultCount: contextInt(data, "pointerized_tool_result_count"),
		PointerizedToolResultBytes: contextInt(data, "pointerized_tool_result_bytes"),
	}
}

func contextProviderUsage(value any) ContextProviderUsage {
	usage := ContextProviderUsage{Reported: false}
	data, ok := value.(map[string]any)
	if !ok {
		return usage
	}
	reported, hasReported := contextBool(data, "reported")
	input := contextInt(data, "input_tokens")
	output := contextInt(data, "output_tokens")
	creation := contextInt(data, "cache_creation_input_tokens")
	read := contextInt(data, "cache_read_input_tokens")
	if hasReported && reported {
		usage.Reported = true
		switch strings.TrimSpace(contextString(data, "source")) {
		case "legacy_inferred":
			usage.Source = "legacy_inferred"
		default:
			usage.Source = "provider"
		}
	} else if input != 0 || output != 0 || creation != 0 || read != 0 {
		usage.Reported = true
		usage.Source = "legacy_inferred"
	}
	if !usage.Reported {
		return usage
	}
	usage.InputTokens = intPointer(input)
	usage.OutputTokens = intPointer(output)
	usage.CacheCreationInputTokens = intPointer(creation)
	usage.CacheReadInputTokens = intPointer(read)
	return usage
}

func normalizeContextRequestStatus(value string) string {
	switch strings.TrimSpace(value) {
	case ContextRequestStatusCompleted:
		return ContextRequestStatusCompleted
	case ContextRequestStatusCancelled:
		return ContextRequestStatusCancelled
	case ContextRequestStatusRejected:
		return ContextRequestStatusRejected
	case ContextRequestStatusFailed:
		return ContextRequestStatusFailed
	default:
		return ContextRequestStatusFailed
	}
}

func accumulateContextRequestMetrics(metrics *ContextSessionMetrics, request ContextRequestReport, mainTurns map[int]struct{}) {
	metrics.RequestCount++
	switch request.RequestKind {
	case "main":
		metrics.MainRequestCount++
		mainTurns[request.Turn] = struct{}{}
	case "semantic_summary":
		metrics.SemanticSummaryRequestCount++
	}
	switch request.Status {
	case ContextRequestStatusCompleted:
		metrics.CompletedRequestCount++
	case ContextRequestStatusFailed:
		metrics.FailedRequestCount++
	case ContextRequestStatusCancelled:
		metrics.CancelledRequestCount++
	case ContextRequestStatusRejected:
		metrics.RejectedRequestCount++
	default:
		metrics.UnknownRequestCount++
	}
	if !request.Usage.Reported {
		metrics.UnknownUsageRequestCount++
	} else {
		addContextUsage(&metrics.ProviderUsage, request.Usage)
	}
	if request.Budget == nil {
		return
	}
	budget := request.Budget
	metrics.PeakEstimatedInputTokens = maxContextInt(metrics.PeakEstimatedInputTokens, budget.EstimatedInputTokens)
	metrics.PeakRequiredTokens = maxContextInt(metrics.PeakRequiredTokens, budget.RequiredTokens)
	metrics.PeakWireBodyBytes = maxContextInt(metrics.PeakWireBodyBytes, budget.WireBodyBytes)
	metrics.AggregateEstimatedInputTokens += budget.EstimatedInputTokens
	metrics.ProviderViewInlineToolResultBytes += budget.InlineToolResultBytes
	metrics.ProviderViewCompactedToolResultBytes += budget.CompactedToolResultBytes
	metrics.ProviderViewPointerizedToolResultBytes += budget.PointerizedToolResultBytes
}

func aggregateContextLineage(sessions []ContextSessionReport, rootSessionID string) ContextLineageAggregate {
	aggregate := ContextLineageAggregate{SessionCount: len(sessions)}
	for _, item := range sessions {
		metrics := item.Metrics
		isRoot := item.SessionID == rootSessionID
		if !isRoot {
			aggregate.ChildSessionCount++
		}
		updateAggregateEventBounds(&aggregate, metrics.FirstEventAt, metrics.LastEventAt)
		compactions := metrics.CompactionStartedCount + metrics.CompactionReusedCount + metrics.CompactionDeferredCount
		if isRoot {
			aggregate.RootPeakEstimatedInputTokens = maxContextInt(aggregate.RootPeakEstimatedInputTokens, metrics.PeakEstimatedInputTokens)
			aggregate.RootPeakRequiredTokens = maxContextInt(aggregate.RootPeakRequiredTokens, metrics.PeakRequiredTokens)
			aggregate.RootPeakWireBodyBytes = maxContextInt(aggregate.RootPeakWireBodyBytes, metrics.PeakWireBodyBytes)
			aggregate.RootAggregateEstimatedInputTokens += metrics.AggregateEstimatedInputTokens
			aggregate.RootRequestCount += metrics.RequestCount
			aggregate.RootTurnCount += metrics.TurnCount
			aggregate.RootToolCallCount += metrics.ToolCallCount
			aggregate.RootCompactionCount += compactions
			aggregate.RootProviderViewInlineToolResultBytes += metrics.ProviderViewInlineToolResultBytes
			aggregate.RootProviderViewCompactedToolResultBytes += metrics.ProviderViewCompactedToolResultBytes
			aggregate.RootProviderViewPointerizedToolResultBytes += metrics.ProviderViewPointerizedToolResultBytes
			aggregate.RootArtifactToolOutputBytes += metrics.ArtifactToolOutputBytes
			addContextUsageTotals(&aggregate.RootProviderUsage, metrics.ProviderUsage)
		} else {
			aggregate.ChildPeakEstimatedInputTokens = maxContextInt(aggregate.ChildPeakEstimatedInputTokens, metrics.PeakEstimatedInputTokens)
			aggregate.ChildPeakRequiredTokens = maxContextInt(aggregate.ChildPeakRequiredTokens, metrics.PeakRequiredTokens)
			aggregate.ChildPeakWireBodyBytes = maxContextInt(aggregate.ChildPeakWireBodyBytes, metrics.PeakWireBodyBytes)
			aggregate.ChildAggregateEstimatedInputTokens += metrics.AggregateEstimatedInputTokens
			aggregate.ChildRequestCount += metrics.RequestCount
			aggregate.ChildTurnCount += metrics.TurnCount
			aggregate.ChildToolCallCount += metrics.ToolCallCount
			aggregate.ChildCompactionCount += compactions
			aggregate.ChildProviderViewInlineToolResultBytes += metrics.ProviderViewInlineToolResultBytes
			aggregate.ChildProviderViewCompactedToolResultBytes += metrics.ProviderViewCompactedToolResultBytes
			aggregate.ChildProviderViewPointerizedToolResultBytes += metrics.ProviderViewPointerizedToolResultBytes
			aggregate.ChildArtifactToolOutputBytes += metrics.ArtifactToolOutputBytes
			addContextUsageTotals(&aggregate.ChildProviderUsage, metrics.ProviderUsage)
		}
		aggregate.UnknownUsageRequestCount += metrics.UnknownUsageRequestCount
	}
	aggregate.TotalEstimatedInputTokens = aggregate.RootAggregateEstimatedInputTokens + aggregate.ChildAggregateEstimatedInputTokens
	aggregate.TotalRequestCount = aggregate.RootRequestCount + aggregate.ChildRequestCount
	aggregate.TotalTurnCount = aggregate.RootTurnCount + aggregate.ChildTurnCount
	aggregate.TotalToolCallCount = aggregate.RootToolCallCount + aggregate.ChildToolCallCount
	aggregate.TotalCompactionCount = aggregate.RootCompactionCount + aggregate.ChildCompactionCount
	aggregate.TotalProviderViewInlineToolResultBytes = aggregate.RootProviderViewInlineToolResultBytes + aggregate.ChildProviderViewInlineToolResultBytes
	aggregate.TotalProviderViewCompactedToolResultBytes = aggregate.RootProviderViewCompactedToolResultBytes + aggregate.ChildProviderViewCompactedToolResultBytes
	aggregate.TotalProviderViewPointerizedToolResultBytes = aggregate.RootProviderViewPointerizedToolResultBytes + aggregate.ChildProviderViewPointerizedToolResultBytes
	aggregate.TotalArtifactToolOutputBytes = aggregate.RootArtifactToolOutputBytes + aggregate.ChildArtifactToolOutputBytes
	aggregate.TotalProviderUsage = aggregate.RootProviderUsage
	addContextUsageTotals(&aggregate.TotalProviderUsage, aggregate.ChildProviderUsage)
	aggregate.WallTimeMS = contextDurationMS(aggregate.FirstEventAt, aggregate.LastEventAt)
	return aggregate
}

func updateContextEventBounds(metrics *ContextSessionMetrics, value string) {
	if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
		return
	}
	if metrics.FirstEventAt == "" || contextTimestampBefore(value, metrics.FirstEventAt) {
		metrics.FirstEventAt = value
	}
	if metrics.LastEventAt == "" || contextTimestampBefore(metrics.LastEventAt, value) {
		metrics.LastEventAt = value
	}
}

func updateAggregateEventBounds(aggregate *ContextLineageAggregate, first, last string) {
	if first != "" && (aggregate.FirstEventAt == "" || contextTimestampBefore(first, aggregate.FirstEventAt)) {
		aggregate.FirstEventAt = first
	}
	if last != "" && (aggregate.LastEventAt == "" || contextTimestampBefore(aggregate.LastEventAt, last)) {
		aggregate.LastEventAt = last
	}
}

func contextTimestampBefore(left, right string) bool {
	leftTime, leftErr := time.Parse(time.RFC3339Nano, left)
	rightTime, rightErr := time.Parse(time.RFC3339Nano, right)
	if leftErr == nil && rightErr == nil {
		return leftTime.Before(rightTime)
	}
	return left < right
}

func incrementCompactionMetric(metrics *ContextSessionMetrics, eventType string) {
	switch eventType {
	case "compact.started":
		metrics.CompactionStartedCount++
	case "compact.finished":
		metrics.CompactionFinishedCount++
	case "compact.reused":
		metrics.CompactionReusedCount++
	case "compact.deferred":
		metrics.CompactionDeferredCount++
	}
}

func addContextUsage(total *ContextUsageTotals, usage ContextProviderUsage) {
	if usage.InputTokens != nil {
		total.InputTokens += *usage.InputTokens
	}
	if usage.OutputTokens != nil {
		total.OutputTokens += *usage.OutputTokens
	}
	if usage.CacheCreationInputTokens != nil {
		total.CacheCreationInputTokens += *usage.CacheCreationInputTokens
	}
	if usage.CacheReadInputTokens != nil {
		total.CacheReadInputTokens += *usage.CacheReadInputTokens
	}
}

func addContextUsageTotals(total *ContextUsageTotals, value ContextUsageTotals) {
	total.InputTokens += value.InputTokens
	total.OutputTokens += value.OutputTokens
	total.CacheCreationInputTokens += value.CacheCreationInputTokens
	total.CacheReadInputTokens += value.CacheReadInputTokens
}

func contextDurationMS(start, end string) int64 {
	if strings.TrimSpace(start) == "" || strings.TrimSpace(end) == "" {
		return 0
	}
	startTime, err := time.Parse(time.RFC3339Nano, start)
	if err != nil {
		return 0
	}
	endTime, err := time.Parse(time.RFC3339Nano, end)
	if err != nil || endTime.Before(startTime) {
		return 0
	}
	return endTime.Sub(startTime).Milliseconds()
}

func contextString(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	value, _ := data[key].(string)
	return value
}

func contextInt(data map[string]any, key string) int {
	if data == nil {
		return 0
	}
	switch value := data[key].(type) {
	case int:
		return value
	case int8:
		return int(value)
	case int16:
		return int(value)
	case int32:
		return int(value)
	case int64:
		return int(value)
	case uint:
		return int(value)
	case uint32:
		return int(value)
	case uint64:
		return int(value)
	case float32:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		parsed, _ := value.Int64()
		return int(parsed)
	default:
		return 0
	}
}

func contextBool(data map[string]any, key string) (bool, bool) {
	if data == nil {
		return false, false
	}
	value, ok := data[key].(bool)
	return value, ok
}

func intPointer(value int) *int {
	copy := value
	return &copy
}

func maxContextInt(left, right int) int {
	if right > left {
		return right
	}
	return left
}
