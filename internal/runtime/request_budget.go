package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"aegis-agent/internal/config"
	"aegis-agent/internal/provider"
	"aegis-agent/internal/session"
	"aegis-agent/internal/tools"
)

const (
	requestBudgetSnapshotSchemaVersion = session.RequestBudgetSnapshotSchemaVersion
	defaultRequestOutputReserveTokens  = 8192

	requestKindMain            = "main"
	requestKindSemanticSummary = "semantic_summary"
	requestKindProbe           = "probe"

	requestBudgetExceededCode       = "request_budget_exceeded"
	requestBudgetUnfitCode          = "request_budget_unfit"
	requestEstimatorUnavailableCode = "request_estimator_unavailable"
	requestEstimateFailedCode       = "request_estimate_failed"

	requestBudgetActionPointerizeRecoverableResult = "pointerize_recoverable_result"
	requestBudgetActionDropOldestReplayClosure     = "drop_oldest_replay_closure"
	requestBudgetActionDropSemanticSummary         = "drop_semantic_summary"
	requestBudgetActionReduceDeterministicSummary  = "reduce_deterministic_summary"

	requestBudgetComponentSystemPrompt               = "system_prompt"
	requestBudgetComponentToolSchemas                = "tool_schemas"
	requestBudgetComponentMetadataOrProviderEnvelope = "metadata_or_provider_envelope"
	requestBudgetComponentLatestExternalInstruction  = "latest_external_instruction"
	requestBudgetComponentLatestToolResult           = "latest_tool_result"
	requestBudgetComponentCompactionSummary          = "compaction_summary"
	requestBudgetComponentProviderRequest            = "provider_request"

	// A compacted provider view normally contains at most sixty recent
	// messages. Keep a considerably larger, fixed ceiling so malformed or old
	// sessions still terminate deterministically without turning hard-fit into
	// an unbounded retry loop.
	maxRequestBudgetShrinkPasses = 256
)

var deterministicSummaryCoreFields = []string{
	"current_goal",
	"open_items",
	"key_paths",
	"latest_external_instruction",
	"latest_steer_constraints",
	"transcript",
	"history_reference",
}

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

type RequestBudgetSnapshot = session.RequestBudgetSnapshot

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

type RequestBudgetAction = session.RequestBudgetAction

type providerRequestFit struct {
	Request         provider.TurnRequest
	InitialSnapshot RequestBudgetSnapshot
	Snapshot        RequestBudgetSnapshot
	Actions         []RequestBudgetAction
}

type RequestBudgetUnfitError struct {
	Code                  string                `json:"code"`
	RequestKind           string                `json:"request_kind"`
	BlockingComponent     string                `json:"blocking_component"`
	EstimatedInputTokens  int                   `json:"estimated_input_tokens"`
	AvailableInputTokens  int                   `json:"available_input_tokens"`
	ReservedOutputTokens  int                   `json:"reserved_output_tokens"`
	SafetyHeadroomTokens  int                   `json:"safety_headroom_tokens"`
	EffectiveWindowTokens int                   `json:"effective_window_tokens"`
	RequiredTokens        int                   `json:"required_tokens"`
	Snapshot              RequestBudgetSnapshot `json:"snapshot"`
	Actions               []RequestBudgetAction `json:"actions,omitempty"`
}

func (e *RequestBudgetUnfitError) Error() string {
	if e == nil {
		return requestBudgetUnfitCode
	}
	return fmt.Sprintf(
		"%s: %s request cannot fit because %s requires an estimated %d input tokens with %d available (%d output reserved, %d safety headroom, %d effective window)",
		e.Code,
		e.RequestKind,
		e.BlockingComponent,
		e.EstimatedInputTokens,
		e.AvailableInputTokens,
		e.ReservedOutputTokens,
		e.SafetyHeadroomTokens,
		e.EffectiveWindowTokens,
	)
}

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

func normalizedRequestKind(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return requestKindMain
	}
	return value
}

func requestBudgetID(requestContext requestBudgetContext) string {
	return fmt.Sprintf(
		"%s:%d:%s:%d",
		strings.TrimSpace(requestContext.SessionID),
		requestContext.Turn,
		normalizedRequestKind(requestContext.RequestKind),
		requestContext.RequestSequence,
	)
}

func preflightProviderRequest(adapter provider.Adapter, req provider.TurnRequest, policy requestBudgetPolicy, requestContext requestBudgetContext) (RequestBudgetSnapshot, error) {
	policy = newRequestBudgetPolicy(req.Model, policy.EffectiveWindowTokens, policy.UtilizationFactor)
	requestKind := normalizedRequestKind(requestContext.RequestKind)
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
	requestContext.SessionID = sessionID
	requestContext.RequestKind = requestKind
	requestID := requestBudgetID(requestContext)
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
	toolResultStats := measureToolResultContext(req.Messages)
	snapshot := RequestBudgetSnapshot{
		SchemaVersion:              requestBudgetSnapshotSchemaVersion,
		RequestID:                  requestID,
		RequestKind:                requestKind,
		SessionID:                  sessionID,
		Turn:                       requestContext.Turn,
		RequestSequence:            requestContext.RequestSequence,
		Provider:                   providerName,
		APIProvider:                strings.TrimSpace(req.APIProvider),
		Model:                      strings.TrimSpace(req.Model),
		ReservedOutputTokens:       reserve,
		OutputReserveSource:        reserveSource,
		SafetyHeadroomTokens:       safetyHeadroom,
		UtilizationFactor:          policy.UtilizationFactor,
		EffectiveWindowTokens:      policy.EffectiveWindowTokens,
		CompactionAction:           compactionAction,
		CompactionSummaryID:        strings.TrimSpace(requestContext.CompactionSummary),
		InlineToolResultCount:      toolResultStats.InlineCount,
		InlineToolResultBytes:      toolResultStats.InlineBytes,
		CompactedToolResultCount:   toolResultStats.CompactedCount,
		CompactedToolResultBytes:   toolResultStats.CompactedBytes,
		PointerizedToolResultCount: toolResultStats.PointerizedCount,
		PointerizedToolResultBytes: toolResultStats.PointerizedBytes,
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

// fitProviderRequestToBudget performs the final, provider-specific hard-fit
// pass. It only changes a cloned provider view; durable session messages and
// artifacts remain untouched. Every accepted action is re-estimated by the
// adapter and must strictly reduce the serialized wire body.
func fitProviderRequestToBudget(adapter provider.Adapter, req provider.TurnRequest, policy requestBudgetPolicy, requestContext requestBudgetContext, cfg *config.Config) (providerRequestFit, error) {
	if cfg == nil {
		cfg = config.Default()
	}
	requestContext.RequestKind = normalizedRequestKind(requestContext.RequestKind)
	if strings.TrimSpace(requestContext.SessionID) == "" {
		requestContext.SessionID = strings.TrimSpace(req.SessionID)
	}
	req.RequestID = requestBudgetID(requestContext)
	fit := providerRequestFit{Request: cloneProviderRequestForBudget(req)}
	snapshot, exceeded, err := requestBudgetSnapshotForFit(adapter, fit.Request, policy, requestContext)
	fit.InitialSnapshot = snapshot
	fit.Snapshot = snapshot
	if err != nil {
		return fit, err
	}
	if !exceeded {
		return fit, nil
	}

	phases := []func(provider.Adapter, provider.TurnRequest, RequestBudgetSnapshot, requestBudgetPolicy, requestBudgetContext, *config.Config, int) (provider.TurnRequest, RequestBudgetSnapshot, RequestBudgetAction, bool, error){
		tryPointerizeRecoverableResult,
		tryDropOldestReplayClosure,
		tryDropSemanticSummary,
		tryReduceDeterministicSummary,
	}
	for _, phase := range phases {
		for len(fit.Actions) < maxRequestBudgetShrinkPasses && !fit.Snapshot.Fit {
			candidate, after, action, accepted, actionErr := phase(adapter, fit.Request, fit.Snapshot, policy, requestContext, cfg, len(fit.Actions)+1)
			if actionErr != nil {
				fit.Snapshot = after
				return fit, actionErr
			}
			if !accepted {
				break
			}
			fit.Request = candidate
			fit.Snapshot = after
			fit.Actions = append(fit.Actions, action)
		}
		if fit.Snapshot.Fit || len(fit.Actions) >= maxRequestBudgetShrinkPasses {
			break
		}
	}
	if fit.Snapshot.Fit {
		return fit, nil
	}

	fit.Snapshot.Fit = false
	fit.Snapshot.RejectionCode = requestBudgetUnfitCode
	available := saturatingSubtract(
		fit.Snapshot.EffectiveWindowTokens,
		saturatingAdd(fit.Snapshot.ReservedOutputTokens, fit.Snapshot.SafetyHeadroomTokens),
	)
	actions := append([]RequestBudgetAction(nil), fit.Actions...)
	unfit := &RequestBudgetUnfitError{
		Code:                  requestBudgetUnfitCode,
		RequestKind:           fit.Snapshot.RequestKind,
		BlockingComponent:     classifyRequestBudgetBlockingComponent(fit.Request, fit.Snapshot, available),
		EstimatedInputTokens:  fit.Snapshot.EstimatedInputTokens,
		AvailableInputTokens:  available,
		ReservedOutputTokens:  fit.Snapshot.ReservedOutputTokens,
		SafetyHeadroomTokens:  fit.Snapshot.SafetyHeadroomTokens,
		EffectiveWindowTokens: fit.Snapshot.EffectiveWindowTokens,
		RequiredTokens:        fit.Snapshot.RequiredTokens,
		Snapshot:              fit.Snapshot,
		Actions:               actions,
	}
	return fit, unfit
}

func cloneProviderRequestForBudget(req provider.TurnRequest) provider.TurnRequest {
	out := req
	out.Messages = cloneMessages(req.Messages)
	return out
}

func requestBudgetSnapshotForFit(adapter provider.Adapter, req provider.TurnRequest, policy requestBudgetPolicy, requestContext requestBudgetContext) (RequestBudgetSnapshot, bool, error) {
	snapshot, err := preflightProviderRequest(adapter, req, policy, requestContext)
	if err == nil {
		return snapshot, false, nil
	}
	var exceeded *RequestBudgetExceededError
	if errors.As(err, &exceeded) {
		return snapshot, true, nil
	}
	return snapshot, false, err
}

type requestBudgetCandidate struct {
	Request             provider.TurnRequest
	AffectedMessageIDs  []string
	AffectedToolCallIDs []string
	AffectedCount       int
}

func evaluateRequestBudgetCandidate(adapter provider.Adapter, beforeRequest provider.TurnRequest, before RequestBudgetSnapshot, candidate requestBudgetCandidate, actionName string, policy requestBudgetPolicy, requestContext requestBudgetContext, pass int) (provider.TurnRequest, RequestBudgetSnapshot, RequestBudgetAction, bool, error) {
	after, _, err := requestBudgetSnapshotForFit(adapter, candidate.Request, policy, requestContext)
	if err != nil {
		return beforeRequest, after, RequestBudgetAction{}, false, err
	}
	if after.WireBodyBytes >= before.WireBodyBytes {
		return beforeRequest, before, RequestBudgetAction{}, false, nil
	}
	action := RequestBudgetAction{
		SchemaVersion:              requestBudgetSnapshotSchemaVersion,
		Pass:                       pass,
		Action:                     actionName,
		BeforeWireBodyBytes:        before.WireBodyBytes,
		AfterWireBodyBytes:         after.WireBodyBytes,
		BeforeEstimatedInputTokens: before.EstimatedInputTokens,
		AfterEstimatedInputTokens:  after.EstimatedInputTokens,
		AffectedMessageIDs:         sortedNonEmptyStrings(candidate.AffectedMessageIDs),
		AffectedToolCallIDs:        sortedNonEmptyStrings(candidate.AffectedToolCallIDs),
		AffectedCount:              candidate.AffectedCount,
	}
	return candidate.Request, after, action, true, nil
}

func tryPointerizeRecoverableResult(adapter provider.Adapter, req provider.TurnRequest, before RequestBudgetSnapshot, policy requestBudgetPolicy, requestContext requestBudgetContext, cfg *config.Config, pass int) (provider.TurnRequest, RequestBudgetSnapshot, RequestBudgetAction, bool, error) {
	for messageIndex, message := range req.Messages {
		if message.Role != "tool" {
			continue
		}
		for resultIndex := range message.ToolResults {
			candidateRequest := cloneProviderRequestForBudget(req)
			messageIDs, callIDs, ok := pointerizeRecoverableResultForHardFit(candidateRequest.Messages, messageIndex, resultIndex, cfg)
			if !ok {
				continue
			}
			candidate := requestBudgetCandidate{
				Request:             candidateRequest,
				AffectedMessageIDs:  messageIDs,
				AffectedToolCallIDs: callIDs,
				AffectedCount:       1,
			}
			fitted, after, action, accepted, err := evaluateRequestBudgetCandidate(adapter, req, before, candidate, requestBudgetActionPointerizeRecoverableResult, policy, requestContext, pass)
			if err != nil || accepted {
				return fitted, after, action, accepted, err
			}
		}
	}
	return req, before, RequestBudgetAction{}, false, nil
}

func pointerizeRecoverableResultForHardFit(messages []session.Message, messageIndex, resultIndex int, cfg *config.Config) ([]string, []string, bool) {
	if messageIndex < 0 || messageIndex >= len(messages) || resultIndex < 0 || resultIndex >= len(messages[messageIndex].ToolResults) {
		return nil, nil, false
	}
	result := &messages[messageIndex].ToolResults[resultIndex]
	if toolResultIsPointerized(*result) || toolResultIsDuplicateMarker(*result) {
		return nil, nil, false
	}
	if compacted, _ := result.Metadata["compacted_for_context"].(bool); compacted {
		return nil, nil, false
	}
	originalBytes := len(result.LLMOutput)
	if originalBytes == 0 {
		return nil, nil, false
	}

	callIDs := []string{result.ToolCallID}
	artifactPath, _, completeArtifact := completeFinalizedToolOutputArtifact(*result)
	reason := "hard_fit_complete_artifact"
	if completeArtifact {
		if !pointerizeFinalizedToolResultForContext(result) {
			return nil, nil, false
		}
		if !strings.Contains(result.LLMOutput, artifactPath) {
			return nil, nil, false
		}
	} else {
		calls, ambiguous := indexDedupToolCalls(messages)
		callID := strings.TrimSpace(result.ToolCallID)
		if callID == "" || ambiguous[callID] || result.IsError {
			return nil, nil, false
		}
		callRef, ok := calls[callID]
		if !ok || callRef.Call.Name != result.Name {
			return nil, nil, false
		}
		canonical, eligible, err := tools.CanonicalReadOnlyToolArguments(callRef.Call.Name, callRef.Call.Arguments, cfg)
		if err != nil || !eligible || len(canonical) == 0 {
			return nil, nil, false
		}
		callIDs = append(callIDs, callRef.Call.ID, callRef.Call.ProviderCallID)
		replayID := strings.TrimSpace(callRef.Call.ID)
		if replayID == "" {
			replayID = strings.TrimSpace(callRef.Call.ProviderCallID)
		}
		if replayID == "" {
			return nil, nil, false
		}
		pointer := fmt.Sprintf(
			"[Previous %s tool result removed from the inline context by the hard request budget. Recoverable current-view source: replay retained read-only tool call %s with its original bounded arguments.]",
			strings.TrimSpace(result.Name),
			replayID,
		)
		result.LLMOutput = pointer
		result.DisplayOutput = pointer
		reason = "hard_fit_recoverable_source"
	}
	if len(result.LLMOutput) >= originalBytes {
		return nil, nil, false
	}
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	result.Metadata["pointerized_for_context"] = true
	result.Metadata["hard_fit_pointerized"] = true
	result.Metadata["compaction_reason"] = reason
	result.Metadata["context_original_llm_bytes"] = originalBytes
	if !completeArtifact {
		result.Metadata["hard_fit_recovery_source"] = "retained_read_only_call"
	}
	return []string{messages[messageIndex].ID}, callIDs, true
}

type requestReplayClosure struct {
	RemoveMessages      map[int]struct{}
	RemoveToolResultIDs map[int]map[string]struct{}
	TouchedMessages     map[int]struct{}
	ToolCallIDs         map[string]struct{}
}

func newRequestReplayClosure() requestReplayClosure {
	return requestReplayClosure{
		RemoveMessages:      map[int]struct{}{},
		RemoveToolResultIDs: map[int]map[string]struct{}{},
		TouchedMessages:     map[int]struct{}{},
		ToolCallIDs:         map[string]struct{}{},
	}
}

func tryDropOldestReplayClosure(adapter provider.Adapter, req provider.TurnRequest, before RequestBudgetSnapshot, policy requestBudgetPolicy, requestContext requestBudgetContext, _ *config.Config, pass int) (provider.TurnRequest, RequestBudgetSnapshot, RequestBudgetAction, bool, error) {
	protected := protectedRequestBudgetMessageIndexes(req.Messages)
	seen := map[string]struct{}{}
	for messageIndex := range req.Messages {
		if _, keep := protected[messageIndex]; keep {
			continue
		}
		closure := replayClosureForMessage(req.Messages, messageIndex)
		if len(closure.TouchedMessages) == 0 || replayClosureIntersectsProtected(closure, protected) {
			continue
		}
		signature := replayClosureSignature(closure)
		if _, duplicate := seen[signature]; duplicate {
			continue
		}
		seen[signature] = struct{}{}
		candidateRequest := cloneProviderRequestForBudget(req)
		candidateRequest.Messages = applyReplayClosureRemoval(candidateRequest.Messages, closure)
		if len(candidateRequest.Messages) >= len(req.Messages) && len(closure.RemoveToolResultIDs) == 0 {
			continue
		}
		messageIDs := requestMessageIDsForIndexes(req.Messages, closure.TouchedMessages)
		callIDs := stringSetValues(closure.ToolCallIDs)
		candidate := requestBudgetCandidate{
			Request:             candidateRequest,
			AffectedMessageIDs:  messageIDs,
			AffectedToolCallIDs: callIDs,
			AffectedCount:       len(closure.TouchedMessages),
		}
		fitted, after, action, accepted, err := evaluateRequestBudgetCandidate(adapter, req, before, candidate, requestBudgetActionDropOldestReplayClosure, policy, requestContext, pass)
		if err != nil || accepted {
			return fitted, after, action, accepted, err
		}
	}
	return req, before, RequestBudgetAction{}, false, nil
}

func protectedRequestBudgetMessageIndexes(messages []session.Message) map[int]struct{} {
	protected := map[int]struct{}{}
	protect := func(index int) {
		if index >= 0 && index < len(messages) {
			protected[index] = struct{}{}
		}
	}
	protect(latestExternalInstructionIndex(messages))
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "user" {
			continue
		}
		source, _ := messages[i].Meta["source"].(string)
		if source == "steer" {
			protect(i)
			break
		}
	}
	for i, message := range messages {
		if message.Role == "system" || isCompactionSummaryMessage(message) {
			protect(i)
		}
	}

	// Protect the newest result and every assistant/result message touched by
	// its replay batch. This is deliberately conservative for mixed batches.
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "tool" || len(messages[i].ToolResults) == 0 {
			continue
		}
		protect(i)
		resultID := strings.TrimSpace(messages[i].ToolResults[len(messages[i].ToolResults)-1].ToolCallID)
		if resultID != "" {
			for assistantIndex := i - 1; assistantIndex >= 0; assistantIndex-- {
				if messages[assistantIndex].Role != "assistant" || !toolCallIDInSet(stringSliceSet(assistantToolCallIDs(messages[assistantIndex])), resultID) {
					continue
				}
				closure := replayClosureForMessage(messages, assistantIndex)
				for touched := range closure.TouchedMessages {
					protect(touched)
				}
				break
			}
		}
		break
	}
	return protected
}

func replayClosureForMessage(messages []session.Message, index int) requestReplayClosure {
	closure := newRequestReplayClosure()
	if index < 0 || index >= len(messages) {
		return closure
	}
	message := messages[index]
	if message.Role == "assistant" {
		return replayClosureForAssistant(messages, index)
	}
	if message.Role != "tool" || len(message.ToolResults) == 0 {
		closure.RemoveMessages[index] = struct{}{}
		closure.TouchedMessages[index] = struct{}{}
		return closure
	}

	for _, result := range message.ToolResults {
		resultID := strings.TrimSpace(result.ToolCallID)
		if resultID == "" {
			continue
		}
		for assistantIndex := index - 1; assistantIndex >= 0; assistantIndex-- {
			if messages[assistantIndex].Role != "assistant" {
				continue
			}
			ids := stringSliceSet(assistantToolCallIDs(messages[assistantIndex]))
			if toolCallIDInSet(ids, resultID) {
				return replayClosureForAssistant(messages, assistantIndex)
			}
		}
	}
	closure.RemoveMessages[index] = struct{}{}
	closure.TouchedMessages[index] = struct{}{}
	for _, result := range message.ToolResults {
		if id := strings.TrimSpace(result.ToolCallID); id != "" {
			closure.ToolCallIDs[id] = struct{}{}
		}
	}
	return closure
}

func replayClosureForAssistant(messages []session.Message, assistantIndex int) requestReplayClosure {
	closure := newRequestReplayClosure()
	if assistantIndex < 0 || assistantIndex >= len(messages) || messages[assistantIndex].Role != "assistant" {
		return closure
	}
	closure.RemoveMessages[assistantIndex] = struct{}{}
	closure.TouchedMessages[assistantIndex] = struct{}{}
	for _, id := range assistantToolCallIDs(messages[assistantIndex]) {
		if id = strings.TrimSpace(id); id != "" {
			closure.ToolCallIDs[id] = struct{}{}
		}
	}
	if len(closure.ToolCallIDs) == 0 {
		return closure
	}
	for messageIndex, message := range messages {
		if message.Role != "tool" || len(message.ToolResults) == 0 {
			continue
		}
		matches := map[string]struct{}{}
		for _, result := range message.ToolResults {
			resultID := strings.TrimSpace(result.ToolCallID)
			if _, ok := closure.ToolCallIDs[resultID]; ok {
				matches[resultID] = struct{}{}
			}
		}
		if len(matches) == 0 {
			continue
		}
		closure.TouchedMessages[messageIndex] = struct{}{}
		if len(matches) == len(message.ToolResults) {
			closure.RemoveMessages[messageIndex] = struct{}{}
			continue
		}
		closure.RemoveToolResultIDs[messageIndex] = matches
	}
	return closure
}

func replayClosureIntersectsProtected(closure requestReplayClosure, protected map[int]struct{}) bool {
	for index := range closure.TouchedMessages {
		if _, ok := protected[index]; ok {
			return true
		}
	}
	return false
}

func replayClosureSignature(closure requestReplayClosure) string {
	indexes := make([]int, 0, len(closure.TouchedMessages))
	for index := range closure.TouchedMessages {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	parts := make([]string, 0, len(indexes)+len(closure.ToolCallIDs))
	for _, index := range indexes {
		parts = append(parts, fmt.Sprintf("m:%d", index))
	}
	for _, id := range sortedNonEmptyStrings(stringSetValues(closure.ToolCallIDs)) {
		parts = append(parts, "c:"+id)
	}
	return strings.Join(parts, "|")
}

func applyReplayClosureRemoval(messages []session.Message, closure requestReplayClosure) []session.Message {
	out := make([]session.Message, 0, len(messages))
	for index, message := range messages {
		if _, remove := closure.RemoveMessages[index]; remove {
			continue
		}
		if ids := closure.RemoveToolResultIDs[index]; len(ids) > 0 && message.Role == "tool" {
			results := make([]session.ToolResult, 0, len(message.ToolResults))
			for _, result := range message.ToolResults {
				if _, remove := ids[strings.TrimSpace(result.ToolCallID)]; !remove {
					results = append(results, result)
				}
			}
			message.ToolResults = results
			if len(message.ToolResults) == 0 && strings.TrimSpace(message.Text) == "" {
				continue
			}
		}
		out = append(out, message)
	}
	return out
}

func tryDropSemanticSummary(adapter provider.Adapter, req provider.TurnRequest, before RequestBudgetSnapshot, policy requestBudgetPolicy, requestContext requestBudgetContext, _ *config.Config, pass int) (provider.TurnRequest, RequestBudgetSnapshot, RequestBudgetAction, bool, error) {
	for messageIndex, message := range req.Messages {
		summary, prefix, ok := parseCompactionSummaryMessage(message)
		if !ok {
			continue
		}
		if _, exists := summary["semantic_summary"]; !exists {
			continue
		}
		delete(summary, "semantic_summary")
		data, err := json.Marshal(summary)
		if err != nil {
			continue
		}
		candidateRequest := cloneProviderRequestForBudget(req)
		candidateRequest.Messages[messageIndex].Text = prefix + string(data)
		candidate := requestBudgetCandidate{
			Request:            candidateRequest,
			AffectedMessageIDs: []string{message.ID},
			AffectedCount:      1,
		}
		fitted, after, action, accepted, evalErr := evaluateRequestBudgetCandidate(adapter, req, before, candidate, requestBudgetActionDropSemanticSummary, policy, requestContext, pass)
		if evalErr != nil || accepted {
			return fitted, after, action, accepted, evalErr
		}
	}
	return req, before, RequestBudgetAction{}, false, nil
}

func tryReduceDeterministicSummary(adapter provider.Adapter, req provider.TurnRequest, before RequestBudgetSnapshot, policy requestBudgetPolicy, requestContext requestBudgetContext, _ *config.Config, pass int) (provider.TurnRequest, RequestBudgetSnapshot, RequestBudgetAction, bool, error) {
	for messageIndex, message := range req.Messages {
		summary, prefix, ok := parseCompactionSummaryMessage(message)
		if !ok {
			continue
		}
		core := deterministicRequestBudgetSummaryCore(summary)
		data, err := json.Marshal(core)
		if err != nil {
			continue
		}
		text := prefix + string(data)
		if text == message.Text || len(text) >= len(message.Text) {
			continue
		}
		candidateRequest := cloneProviderRequestForBudget(req)
		candidateRequest.Messages[messageIndex].Text = text
		candidate := requestBudgetCandidate{
			Request:            candidateRequest,
			AffectedMessageIDs: []string{message.ID},
			AffectedCount:      1,
		}
		fitted, after, action, accepted, evalErr := evaluateRequestBudgetCandidate(adapter, req, before, candidate, requestBudgetActionReduceDeterministicSummary, policy, requestContext, pass)
		if evalErr != nil || accepted {
			return fitted, after, action, accepted, evalErr
		}
	}
	return req, before, RequestBudgetAction{}, false, nil
}

func isCompactionSummaryMessage(message session.Message) bool {
	source, _ := message.Meta["source"].(string)
	return source == "compaction_summary" || strings.HasPrefix(message.Text, compactionReferencePrefix)
}

func parseCompactionSummaryMessage(message session.Message) (map[string]any, string, bool) {
	if !isCompactionSummaryMessage(message) {
		return nil, "", false
	}
	prefix := ""
	body := message.Text
	if strings.HasPrefix(body, compactionReferencePrefix) {
		prefix = compactionReferencePrefix
		body = strings.TrimPrefix(body, compactionReferencePrefix)
	}
	var summary map[string]any
	if err := json.Unmarshal([]byte(body), &summary); err != nil || summary == nil {
		return nil, "", false
	}
	return summary, prefix, true
}

func deterministicRequestBudgetSummaryCore(summary map[string]any) map[string]any {
	limits := map[string]int{
		"current_goal":                2048,
		"open_items":                  3072,
		"key_paths":                   3072,
		"latest_external_instruction": 2048,
		"latest_steer_constraints":    2048,
		"transcript":                  1024,
		"history_reference":           2048,
	}
	core := make(map[string]any, len(deterministicSummaryCoreFields))
	for _, field := range deterministicSummaryCoreFields {
		value := summary[field]
		if field == "history_reference" && value == nil {
			value = canonicalSessionHistoryReference("current_session")
		}
		core[field] = boundedRequestBudgetSummaryValue(value, limits[field])
	}
	return core
}

func boundedRequestBudgetSummaryValue(value any, limit int) any {
	if value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return map[string]any{"unavailable": true}
	}
	if limit <= 0 || len(data) <= limit {
		return value
	}
	previewLimit := limit - 96
	if previewLimit < 64 {
		previewLimit = 64
	}
	return map[string]any{
		"truncated":      true,
		"original_bytes": len(data),
		"preview":        prefixAtRuneBoundary(string(data), previewLimit),
	}
}

func classifyRequestBudgetBlockingComponent(req provider.TurnRequest, snapshot RequestBudgetSnapshot, availableInputTokens int) string {
	availableBytes := availableInputTokens * 4
	if availableInputTokens < 0 {
		availableBytes = availableInputTokens
	}
	if strings.TrimSpace(req.SystemPrompt) != "" && snapshot.SystemChars > availableBytes {
		return requestBudgetComponentSystemPrompt
	}
	if len(req.Tools) > 0 && snapshot.ToolSchemaBytes > availableBytes {
		return requestBudgetComponentToolSchemas
	}
	if len(req.Metadata) > 0 && snapshot.MetadataBytes > availableBytes {
		return requestBudgetComponentMetadataOrProviderEnvelope
	}
	if index := latestExternalInstructionIndex(req.Messages); index >= 0 && estimateChars([]session.Message{req.Messages[index]}) > availableBytes {
		return requestBudgetComponentLatestExternalInstruction
	}
	if index := latestToolResultMessageIndex(req.Messages); index >= 0 {
		closure := replayClosureForMessage(req.Messages, index)
		if estimateRequestReplayClosureBytes(req.Messages, closure) > availableBytes {
			return requestBudgetComponentLatestToolResult
		}
	}
	for _, message := range req.Messages {
		if isCompactionSummaryMessage(message) && estimateChars([]session.Message{message}) > availableBytes {
			return requestBudgetComponentCompactionSummary
		}
	}
	knownBytes := snapshot.SystemChars + snapshot.MessagesBytes + snapshot.ToolSchemaBytes + snapshot.MetadataBytes
	if snapshot.WireBodyBytes-knownBytes > availableBytes || len(req.Metadata) > 0 {
		return requestBudgetComponentMetadataOrProviderEnvelope
	}
	return requestBudgetComponentProviderRequest
}

func latestToolResultMessageIndex(messages []session.Message) int {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == "tool" && len(messages[index].ToolResults) > 0 {
			return index
		}
	}
	return -1
}

func estimateRequestReplayClosureBytes(messages []session.Message, closure requestReplayClosure) int {
	selected := make([]session.Message, 0, len(closure.TouchedMessages))
	for index := range closure.TouchedMessages {
		if index >= 0 && index < len(messages) {
			selected = append(selected, messages[index])
		}
	}
	return estimateChars(selected)
}

func requestMessageIDsForIndexes(messages []session.Message, indexes map[int]struct{}) []string {
	ids := make([]string, 0, len(indexes))
	for index := range indexes {
		if index >= 0 && index < len(messages) {
			ids = append(ids, messages[index].ID)
		}
	}
	return ids
}

func stringSliceSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out[value] = struct{}{}
		}
	}
	return out
}

func stringSetValues(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	return out
}

func sortedNonEmptyStrings(values []string) []string {
	set := stringSliceSet(values)
	out := stringSetValues(set)
	sort.Strings(out)
	return out
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
		"provider":       snapshot.Provider,
		"model":          snapshot.Model,
		"fit":            false,
		"rejection_code": snapshot.RejectionCode,
		"request_budget": snapshot,
	}
	addRequestSnapshotCorrelation(data, snapshot)
	if err := e.appendEvent(sessionID, "provider.request.rejected", phase, data); err != nil {
		return err
	}
	if snapshot.RejectionCode == requestBudgetExceededCode {
		if err := e.appendEvent(sessionID, "provider.request.budget_exceeded", phase, data); err != nil {
			return err
		}
	}
	if snapshot.RejectionCode == requestBudgetUnfitCode {
		if err := e.appendEvent(sessionID, "provider.request.budget_unfit", phase, data); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) appendProviderRequestBudgetActions(sessionID, phase string, snapshot RequestBudgetSnapshot, actions []RequestBudgetAction) error {
	for _, action := range actions {
		data := map[string]any{
			"provider":      snapshot.Provider,
			"model":         snapshot.Model,
			"pass":          action.Pass,
			"action":        action.Action,
			"budget_action": action,
		}
		addRequestSnapshotCorrelation(data, snapshot)
		if err := e.appendEvent(sessionID, "provider.request.budget_action", phase, data); err != nil {
			return err
		}
	}
	return nil
}
