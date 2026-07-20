package runtime

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/provider"
)

const (
	requestBudgetSnapshotSchemaVersion = 1
	defaultRequestOutputReserveTokens  = 8192

	requestKindMain            = "main"
	requestKindSemanticSummary = "semantic_summary"
	requestKindProbe           = "probe"

	requestBudgetExceededCode       = "request_budget_exceeded"
	requestEstimatorUnavailableCode = "request_estimator_unavailable"
	requestEstimateFailedCode       = "request_estimate_failed"
)

type requestBudgetPolicy struct {
	EffectiveWindowTokens int
	UtilizationFactor     float64
}

type requestBudgetContext struct {
	RequestKind       string
	SessionID         string
	Turn              int
	RequestSequence   int
	CompactionAction  string
	CompactionSummary string
}

type RequestBudgetSnapshot struct {
	SchemaVersion             int     `json:"schema_version"`
	RequestID                 string  `json:"request_id"`
	RequestKind               string  `json:"request_kind"`
	SessionID                 string  `json:"session_id"`
	Turn                      int     `json:"turn"`
	RequestSequence           int     `json:"request_sequence,omitempty"`
	Provider                  string  `json:"provider"`
	APIProvider               string  `json:"api_provider,omitempty"`
	Model                     string  `json:"model"`
	WireEstimateSchemaVersion int     `json:"wire_estimate_schema_version"`
	SystemChars               int     `json:"system_chars"`
	MessageCount              int     `json:"message_count"`
	MessagesBytes             int     `json:"messages_bytes"`
	ToolCount                 int     `json:"tool_count"`
	ToolSchemaBytes           int     `json:"tool_schema_bytes"`
	MetadataKeyCount          int     `json:"metadata_key_count"`
	MetadataBytes             int     `json:"metadata_bytes"`
	WireBodyBytes             int     `json:"wire_body_bytes"`
	EstimatedInputTokens      int     `json:"estimated_input_tokens"`
	ReservedOutputTokens      int     `json:"reserved_output_tokens"`
	OutputReserveSource       string  `json:"output_reserve_source"`
	SafetyHeadroomTokens      int     `json:"safety_headroom_tokens"`
	UtilizationFactor         float64 `json:"utilization_factor"`
	EffectiveWindowTokens     int     `json:"effective_window_tokens"`
	RequiredTokens            int     `json:"required_tokens"`
	HeadroomTokens            int     `json:"headroom_tokens"`
	CompactionAction          string  `json:"compaction_action"`
	CompactionSummaryID       string  `json:"compaction_summary_id,omitempty"`
	Fit                       bool    `json:"fit"`
	RejectionCode             string  `json:"rejection_code,omitempty"`
}

type RequestBudgetExceededError struct {
	Code     string
	Snapshot RequestBudgetSnapshot
}

func (e *RequestBudgetExceededError) Error() string {
	return fmt.Sprintf("%s: request %s requires %d tokens with reserves, effective window is %d (headroom %d)", e.Code, e.Snapshot.RequestID, e.Snapshot.RequiredTokens, e.Snapshot.EffectiveWindowTokens, e.Snapshot.HeadroomTokens)
}

type RequestBudgetPreflightError struct {
	Code     string
	Snapshot RequestBudgetSnapshot
	Err      error
}

func (e *RequestBudgetPreflightError) Error() string {
	if e.Err == nil {
		return e.Code
	}
	return fmt.Sprintf("%s: %v", e.Code, e.Err)
}

func (e *RequestBudgetPreflightError) Unwrap() error { return e.Err }

func newRequestBudgetPolicy(model string, configuredWindow int, utilizationFactor float64) requestBudgetPolicy {
	window := config.ResolveContextWindowTokens(model, configuredWindow)
	if window <= 0 {
		window = config.DefaultContextWindowTokens
	}
	if utilizationFactor <= 0 || utilizationFactor > 1 {
		utilizationFactor = config.DefaultCompactUtilizationFactor
	}
	return requestBudgetPolicy{EffectiveWindowTokens: window, UtilizationFactor: utilizationFactor}
}

func preflightProviderRequest(adapter provider.Adapter, req provider.TurnRequest, policy requestBudgetPolicy, requestContext requestBudgetContext) (RequestBudgetSnapshot, error) {
	policy = newRequestBudgetPolicy(req.Model, policy.EffectiveWindowTokens, policy.UtilizationFactor)
	requestKind := strings.TrimSpace(requestContext.RequestKind)
	if requestKind == "" {
		requestKind = requestKindMain
	}
	sessionID := strings.TrimSpace(requestContext.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(req.SessionID)
	}
	providerName := strings.TrimSpace(req.ProviderProfile)
	if providerName == "" && adapter != nil {
		providerName = strings.TrimSpace(adapter.Name())
	}
	compactionAction := strings.TrimSpace(requestContext.CompactionAction)
	if compactionAction == "" {
		compactionAction = "none"
	}
	requestID := fmt.Sprintf("%s:%d:%s:%d", sessionID, requestContext.Turn, requestKind, requestContext.RequestSequence)
	reserve := req.MaxOutputTokens
	reserveSource := "max_output_tokens"
	if reserve <= 0 {
		reserve = defaultRequestOutputReserveTokens
		reserveSource = "default"
	}
	usableWindow := int(math.Floor(float64(policy.EffectiveWindowTokens) * policy.UtilizationFactor))
	if usableWindow < 0 {
		usableWindow = 0
	}
	if usableWindow > policy.EffectiveWindowTokens {
		usableWindow = policy.EffectiveWindowTokens
	}
	safetyHeadroom := policy.EffectiveWindowTokens - usableWindow
	snapshot := RequestBudgetSnapshot{
		SchemaVersion:         requestBudgetSnapshotSchemaVersion,
		RequestID:             requestID,
		RequestKind:           requestKind,
		SessionID:             sessionID,
		Turn:                  requestContext.Turn,
		RequestSequence:       requestContext.RequestSequence,
		Provider:              providerName,
		APIProvider:           strings.TrimSpace(req.APIProvider),
		Model:                 strings.TrimSpace(req.Model),
		ReservedOutputTokens:  reserve,
		OutputReserveSource:   reserveSource,
		SafetyHeadroomTokens:  safetyHeadroom,
		UtilizationFactor:     policy.UtilizationFactor,
		EffectiveWindowTokens: policy.EffectiveWindowTokens,
		CompactionAction:      compactionAction,
		CompactionSummaryID:   strings.TrimSpace(requestContext.CompactionSummary),
	}

	estimate, err := provider.EstimateAdapterRequest(adapter, req)
	if err != nil {
		code := requestEstimateFailedCode
		if errors.Is(err, provider.ErrRequestEstimatorUnavailable) {
			code = requestEstimatorUnavailableCode
		}
		snapshot.RejectionCode = code
		snapshot.HeadroomTokens = saturatingSubtract(policy.EffectiveWindowTokens, saturatingAdd(reserve, safetyHeadroom))
		return snapshot, &RequestBudgetPreflightError{Code: code, Snapshot: snapshot, Err: err}
	}
	expectedInputTokens := estimate.WireBodyBytes / 4
	if estimate.WireBodyBytes%4 != 0 {
		expectedInputTokens++
	}
	if estimate.SchemaVersion <= 0 || estimate.WireBodyBytes < 0 || estimate.EstimatedInputTokens != expectedInputTokens || estimate.SystemChars < 0 || estimate.MessageCount < 0 || estimate.MessagesBytes < 0 || estimate.ToolCount < 0 || estimate.ToolSchemaBytes < 0 || estimate.MetadataKeyCount < 0 || estimate.MetadataBytes < 0 {
		err := fmt.Errorf("invalid wire request estimate: schema=%d body_bytes=%d input_tokens=%d expected_input_tokens=%d", estimate.SchemaVersion, estimate.WireBodyBytes, estimate.EstimatedInputTokens, expectedInputTokens)
		snapshot.RejectionCode = requestEstimateFailedCode
		return snapshot, &RequestBudgetPreflightError{Code: requestEstimateFailedCode, Snapshot: snapshot, Err: err}
	}
	snapshot.WireEstimateSchemaVersion = estimate.SchemaVersion
	snapshot.SystemChars = estimate.SystemChars
	snapshot.MessageCount = estimate.MessageCount
	snapshot.MessagesBytes = estimate.MessagesBytes
	snapshot.ToolCount = estimate.ToolCount
	snapshot.ToolSchemaBytes = estimate.ToolSchemaBytes
	snapshot.MetadataKeyCount = estimate.MetadataKeyCount
	snapshot.MetadataBytes = estimate.MetadataBytes
	snapshot.WireBodyBytes = estimate.WireBodyBytes
	snapshot.EstimatedInputTokens = estimate.EstimatedInputTokens
	snapshot.RequiredTokens = saturatingAdd(estimate.EstimatedInputTokens, reserve, safetyHeadroom)
	snapshot.HeadroomTokens = saturatingSubtract(policy.EffectiveWindowTokens, snapshot.RequiredTokens)
	snapshot.Fit = snapshot.RequiredTokens <= policy.EffectiveWindowTokens
	if !snapshot.Fit {
		snapshot.RejectionCode = requestBudgetExceededCode
		return snapshot, &RequestBudgetExceededError{Code: requestBudgetExceededCode, Snapshot: snapshot}
	}
	return snapshot, nil
}

func saturatingAdd(values ...int) int {
	maxInt := int(^uint(0) >> 1)
	total := 0
	for _, value := range values {
		if value > 0 && total > maxInt-value {
			return maxInt
		}
		if value < 0 && total < -maxInt-value {
			return -maxInt
		}
		total += value
	}
	return total
}

func saturatingSubtract(left, right int) int {
	maxInt := int(^uint(0) >> 1)
	if right > 0 && left < -maxInt+right {
		return -maxInt
	}
	if right < 0 && left > maxInt+right {
		return maxInt
	}
	return left - right
}

func (e *Engine) appendProviderRequestRejection(sessionID, phase string, snapshot RequestBudgetSnapshot) error {
	data := map[string]any{
		"request_id":     snapshot.RequestID,
		"request_kind":   snapshot.RequestKind,
		"provider":       snapshot.Provider,
		"model":          snapshot.Model,
		"fit":            false,
		"rejection_code": snapshot.RejectionCode,
		"request_budget": snapshot,
	}
	if err := e.appendEvent(sessionID, "provider.request.rejected", phase, data); err != nil {
		return err
	}
	if snapshot.RejectionCode == requestBudgetExceededCode {
		if err := e.appendEvent(sessionID, "provider.request.budget_exceeded", phase, data); err != nil {
			return err
		}
	}
	return nil
}
