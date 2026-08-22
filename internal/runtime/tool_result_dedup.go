package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"aegis-agent/internal/config"
	"aegis-agent/internal/session"
	"aegis-agent/internal/tools"
)

const duplicateToolResultVersion = 1

type dedupToolCallRef struct {
	MessageIndex int
	CallIndex    int
	Call         session.ToolCall
}

type dedupResultSignature struct {
	ToolName                 string `json:"tool_name"`
	CanonicalArgumentsSHA256 string `json:"canonical_arguments_sha256"`
	ResultContentSHA256      string `json:"result_content_sha256"`
	ResultContentBytes       int    `json:"result_content_bytes"`
	ResultInlineBytes        int    `json:"result_inline_bytes"`
	IsError                  bool   `json:"is_error"`
	Final                    bool   `json:"final"`
	ArtifactComplete         bool   `json:"artifact_complete"`
	ArtifactTruncated        bool   `json:"artifact_truncated"`
	Recoverable              bool   `json:"recoverable"`
	BudgetReason             string `json:"budget_reason"`
	RawBytes                 int    `json:"raw_bytes"`
	PersistedBytes           int    `json:"persisted_bytes"`
	OmittedBytes             int    `json:"omitted_bytes"`
	PathSource               string `json:"path_source"`
	Skill                    string `json:"skill"`
}

type retainedDedupResult struct {
	CallID string
}

// deduplicateIdenticalReadOnlyToolResults clones the provider view and only
// replaces an older individual ToolResult after both its effective request and
// original result content have been proven identical to a newer result. It
// never edits the durable input messages.
func deduplicateIdenticalReadOnlyToolResults(messages []session.Message, cfg *config.Config) []session.Message {
	view := cloneMessages(messages)
	calls, ambiguous := indexDedupToolCalls(view)
	retainedBySignature := make(map[string]retainedDedupResult)

	for messageIndex := len(view) - 1; messageIndex >= 0; messageIndex-- {
		if view[messageIndex].Role != "tool" {
			continue
		}
		for resultIndex := len(view[messageIndex].ToolResults) - 1; resultIndex >= 0; resultIndex-- {
			result := &view[messageIndex].ToolResults[resultIndex]
			if toolResultIsDuplicateMarker(*result) {
				continue
			}
			callID := strings.TrimSpace(result.ToolCallID)
			if callID == "" || ambiguous[callID] {
				continue
			}
			callRef, ok := calls[callID]
			if !ok || callRef.Call.Name != result.Name {
				continue
			}
			signature, resultHash, originalBytes, ok := safeReadOnlyDedupSignature(callRef.Call, *result, cfg)
			if !ok {
				continue
			}
			if retained, exists := retainedBySignature[signature]; exists {
				replaceWithDuplicateToolResultMarker(result, retained.CallID, resultHash, originalBytes)
				continue
			}
			retainedBySignature[signature] = retainedDedupResult{CallID: result.ToolCallID}
		}
	}
	return view
}

func indexDedupToolCalls(messages []session.Message) (map[string]dedupToolCallRef, map[string]bool) {
	byID := make(map[string]dedupToolCallRef)
	ambiguous := make(map[string]bool)
	for messageIndex, message := range messages {
		if message.Role != "assistant" {
			continue
		}
		for callIndex, call := range message.ToolCalls {
			ref := dedupToolCallRef{MessageIndex: messageIndex, CallIndex: callIndex, Call: call}
			for _, alias := range []string{call.ID, call.ProviderCallID} {
				alias = strings.TrimSpace(alias)
				if alias == "" || ambiguous[alias] {
					continue
				}
				if previous, exists := byID[alias]; exists && (previous.MessageIndex != messageIndex || previous.CallIndex != callIndex) {
					delete(byID, alias)
					ambiguous[alias] = true
					continue
				}
				byID[alias] = ref
			}
		}
	}
	return byID, ambiguous
}

func safeReadOnlyDedupSignature(call session.ToolCall, result session.ToolResult, cfg *config.Config) (signature, resultHash string, originalBytes int, ok bool) {
	canonical, eligible, err := tools.CanonicalReadOnlyToolArguments(call.Name, call.Arguments, cfg)
	if err != nil || !eligible || len(canonical) == 0 {
		return "", "", 0, false
	}
	resultHash, contentBytes, resultInlineBytes, source, ok := resultContentHashMetadata(result.Metadata)
	if !ok || source != resultContentHashSourcePreBudget {
		return "", "", 0, false
	}
	if !toolOutputBudgetVersionApplied(result.Metadata) {
		return "", "", 0, false
	}
	rawBytes, rawOK := toolMetadataInt(result.Metadata, "raw_bytes")
	inlineBytes, inlineOK := toolMetadataInt(result.Metadata, "inline_bytes")
	persistedBytes, persistedOK := toolMetadataInt(result.Metadata, "persisted_bytes")
	omittedBytes, omittedOK := toolMetadataInt(result.Metadata, "omitted_bytes")
	artifactComplete, completeOK := result.Metadata["artifact_complete"].(bool)
	artifactTruncated, truncatedOK := result.Metadata["artifact_truncated"].(bool)
	recoverable, recoverableOK := result.Metadata["recoverable"].(bool)
	budgetReason, reasonOK := result.Metadata["budget_reason"].(string)
	artifactPath, pathOK := result.Metadata["artifact_path"].(string)
	if !rawOK || !inlineOK || !persistedOK || !omittedOK || !completeOK || !truncatedOK || !recoverableOK || !reasonOK || !pathOK {
		return "", "", 0, false
	}
	if rawBytes != contentBytes || inlineBytes != resultInlineBytes || rawBytes < 0 || inlineBytes < 0 || persistedBytes < 0 || omittedBytes < 0 || persistedBytes > rawBytes || omittedBytes > rawBytes || strings.TrimSpace(budgetReason) == "" {
		return "", "", 0, false
	}
	artifactPath = strings.TrimSpace(artifactPath)
	switch {
	case artifactComplete:
		if artifactTruncated || !recoverable || artifactPath == "" || persistedBytes != rawBytes || omittedBytes != 0 {
			return "", "", 0, false
		}
	case artifactTruncated:
		if recoverable || artifactPath == "" || persistedBytes >= rawBytes || omittedBytes != rawBytes-persistedBytes {
			return "", "", 0, false
		}
	default:
		if artifactPath != "" {
			return "", "", 0, false
		}
		if recoverable && (persistedBytes != 0 || omittedBytes != 0) {
			return "", "", 0, false
		}
		if !recoverable && omittedBytes != rawBytes-persistedBytes {
			return "", "", 0, false
		}
	}
	argumentsHash := sha256.Sum256(canonical)
	pathSource, _ := result.Metadata["path_source"].(string)
	skill, _ := result.Metadata["skill"].(string)
	proof := dedupResultSignature{
		ToolName:                 result.Name,
		CanonicalArgumentsSHA256: hex.EncodeToString(argumentsHash[:]),
		ResultContentSHA256:      resultHash,
		ResultContentBytes:       contentBytes,
		ResultInlineBytes:        resultInlineBytes,
		IsError:                  result.IsError,
		Final:                    result.Final,
		ArtifactComplete:         artifactComplete,
		ArtifactTruncated:        artifactTruncated,
		Recoverable:              recoverable,
		BudgetReason:             budgetReason,
		RawBytes:                 rawBytes,
		PersistedBytes:           persistedBytes,
		OmittedBytes:             omittedBytes,
		PathSource:               pathSource,
		Skill:                    skill,
	}
	data, err := json.Marshal(proof)
	if err != nil {
		return "", "", 0, false
	}
	return string(data), resultHash, contentBytes, true
}

func replaceWithDuplicateToolResultMarker(result *session.ToolResult, retainedCallID, resultHash string, originalBytes int) {
	if result == nil || toolResultIsDuplicateMarker(*result) {
		return
	}
	source := duplicateToolResultSourceReference(result.Metadata)
	sourceText := ""
	if source != "" {
		sourceText = fmt.Sprintf("; source=%q", source)
	}
	marker := fmt.Sprintf(
		"[Duplicate %s result omitted from provider view; identical result retained at call %q; result_sha256=%s; original_bytes=%d%s.]",
		strings.TrimSpace(result.Name), strings.TrimSpace(retainedCallID), resultHash, originalBytes, sourceText,
	)
	originalVisibleBytes := len(result.LLMOutput)
	result.LLMOutput = marker
	result.DisplayOutput = marker
	if result.Metadata == nil {
		result.Metadata = make(map[string]any)
	}
	result.Metadata["duplicate_tool_result"] = true
	result.Metadata["duplicate_tool_result_version"] = duplicateToolResultVersion
	result.Metadata["dedup_retained_call_id"] = retainedCallID
	result.Metadata["dedup_result_content_sha256"] = resultHash
	result.Metadata["dedup_original_bytes"] = originalBytes
	result.Metadata["dedup_provider_view_bytes"] = len(marker)
	result.Metadata["compacted_for_context"] = true
	result.Metadata["compaction_reason"] = "duplicate_tool_result"
	result.Metadata["context_original_llm_bytes"] = originalVisibleBytes
}

func duplicateToolResultSourceReference(metadata map[string]any) string {
	if metadata == nil {
		return ""
	}
	for _, key := range []string{"artifact_path", "ephemeral_artifact", "path"} {
		if value, _ := metadata[key].(string); strings.TrimSpace(value) != "" {
			return prefixAtRuneBoundary(strings.TrimSpace(value), 256)
		}
	}
	return ""
}

func toolResultIsDuplicateMarker(result session.ToolResult) bool {
	if value, _ := result.Metadata["duplicate_tool_result"].(bool); !value {
		return false
	}
	return metadataVersionEquals(result.Metadata["duplicate_tool_result_version"], duplicateToolResultVersion)
}
