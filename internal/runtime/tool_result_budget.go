package runtime

import (
	"encoding/json"
	"fmt"
	"strings"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/session"
)

const toolOutputBudgetVersion = session.ToolOutputBudgetVersion

func (e *Engine) appendFinalizedToolResults(sessionID string, results []session.ToolResult) error {
	return e.store.AppendMessage(sessionID, e.finalizedToolMessage(sessionID, results))
}

func (e *Engine) finalizedToolMessage(sessionID string, results []session.ToolResult) session.Message {
	finalized := make([]session.ToolResult, len(results))
	for index, result := range results {
		finalized[index] = e.finalizeToolResultForContext(sessionID, result)
	}
	return session.NewToolMessage(finalized)
}

func (e *Engine) finalizeToolResultForContext(sessionID string, input session.ToolResult) session.ToolResult {
	result := cloneToolResultForBudget(input)
	policy := effectiveToolOutputPolicy(e.cfg)
	if toolOutputBudgetAlreadyApplied(result, policy) {
		return result
	}
	rawLLM := result.LLMOutput
	rawDisplay := result.DisplayOutput
	llmOverflow := len(rawLLM) > policy.LLMOutputMaxBytes
	displayOverflow := len(rawDisplay) > policy.DisplayOutputMaxBytes

	metadata := result.Metadata
	if metadata == nil {
		metadata = make(map[string]any)
	}
	artifactPath := ""
	persistedBytes := 0
	omittedBytes := 0
	artifactComplete := false
	artifactTruncated := false
	recoverable := true
	budgetReason := "inline"
	artifactError := ""
	artifactNotice := ""

	if llmOverflow {
		artifactResult, err := e.store.WriteToolOutputArtifact(
			sessionID,
			e.ephemeralArtifactRoot(sessionID),
			result.Name+"-"+result.ToolCallID,
			[]byte(rawLLM),
			session.ToolOutputArtifactQuota{
				FileMaxBytes:    policy.ArtifactFileMaxBytes,
				SessionMaxBytes: policy.ArtifactSessionMaxBytes,
				MaxFiles:        policy.ArtifactMaxFiles,
			},
		)
		persistedBytes = artifactResult.PersistedBytes
		omittedBytes = artifactResult.OmittedBytes
		artifactComplete = artifactResult.Complete
		artifactTruncated = artifactResult.Truncated
		recoverable = artifactResult.Recoverable
		if artifactResult.AbsolutePath != "" {
			artifactPath = e.ephemeralArtifactDisplayPath(sessionID, artifactResult.AbsolutePath)
		}
		if err != nil {
			artifactError = err.Error()
			budgetReason = "artifact_write_failed"
			recoverable = false
			artifactComplete = false
			artifactTruncated = false
			artifactPath = ""
			persistedBytes = 0
			omittedBytes = len(rawLLM)
		} else if artifactResult.Reason != "" {
			budgetReason = artifactResult.Reason
		} else {
			budgetReason = "llm_output_max_bytes"
		}
		artifactNotice = toolOutputArtifactNotice(policy.LLMOutputMaxBytes, artifactPath, len(rawLLM), persistedBytes, omittedBytes, artifactComplete, artifactTruncated, budgetReason)
		result.LLMOutput, _ = boundedToolOutputPreview(rawLLM, artifactNotice, policy.LLMOutputMaxBytes)
	}

	displayRetainedBytes := len(rawDisplay)
	if displayOverflow {
		displayNotice := fmt.Sprintf("[Display output exceeded display_output_max_bytes=%d; only a bounded preview is retained.]", policy.DisplayOutputMaxBytes)
		if llmOverflow && rawDisplay == rawLLM && artifactNotice != "" {
			displayNotice = artifactNotice
		}
		result.DisplayOutput, displayRetainedBytes = boundedToolOutputPreview(rawDisplay, displayNotice, policy.DisplayOutputMaxBytes)
		if !llmOverflow {
			budgetReason = "display_output_max_bytes"
		}
	}

	metadata["tool_output_budget_version"] = toolOutputBudgetVersion
	metadata["raw_bytes"] = len(rawLLM)
	metadata["persisted_bytes"] = persistedBytes
	metadata["inline_bytes"] = len(result.LLMOutput)
	metadata["omitted_bytes"] = omittedBytes
	metadata["artifact_path"] = artifactPath
	metadata["artifact_complete"] = artifactComplete
	metadata["artifact_truncated"] = artifactTruncated
	metadata["budget_reason"] = budgetReason
	metadata["recoverable"] = recoverable
	metadata["display_raw_bytes"] = len(rawDisplay)
	metadata["display_inline_bytes"] = len(result.DisplayOutput)
	metadata["display_omitted_bytes"] = len(rawDisplay) - displayRetainedBytes
	if artifactError != "" {
		metadata["artifact_error"] = artifactError
	}
	result.Metadata = metadata
	return result
}

func effectiveToolOutputPolicy(cfg *config.Config) config.ToolOutputConfig {
	policy := config.ToolOutputConfig{}
	if cfg != nil {
		policy = cfg.Runtime.ToolOutput
	}
	if policy.LLMOutputMaxBytes <= 0 {
		policy.LLMOutputMaxBytes = config.DefaultToolOutputLLMMaxBytes
	}
	if policy.DisplayOutputMaxBytes <= 0 {
		policy.DisplayOutputMaxBytes = config.DefaultToolOutputDisplayMaxBytes
	}
	if policy.ArtifactFileMaxBytes <= 0 {
		policy.ArtifactFileMaxBytes = config.DefaultToolOutputArtifactFileMaxBytes
	}
	if policy.ArtifactSessionMaxBytes <= 0 {
		policy.ArtifactSessionMaxBytes = config.DefaultToolOutputArtifactSessionMaxBytes
	}
	if policy.ArtifactMaxFiles <= 0 {
		policy.ArtifactMaxFiles = config.DefaultToolOutputArtifactMaxFiles
	}
	return policy
}

func cloneToolResultForBudget(input session.ToolResult) session.ToolResult {
	result := input
	if input.Metadata != nil {
		result.Metadata = make(map[string]any, len(input.Metadata)+16)
		for key, value := range input.Metadata {
			result.Metadata[key] = value
		}
	}
	return result
}

func toolOutputBudgetAlreadyApplied(result session.ToolResult, policy config.ToolOutputConfig) bool {
	metadata := result.Metadata
	if !toolOutputBudgetVersionApplied(metadata) || len(result.LLMOutput) > policy.LLMOutputMaxBytes || len(result.DisplayOutput) > policy.DisplayOutputMaxBytes {
		return false
	}
	inlineBytes, inlineOK := toolMetadataInt(metadata, "inline_bytes")
	displayInlineBytes, displayInlineOK := toolMetadataInt(metadata, "display_inline_bytes")
	rawBytes, rawOK := toolMetadataInt(metadata, "raw_bytes")
	displayRawBytes, displayRawOK := toolMetadataInt(metadata, "display_raw_bytes")
	persistedBytes, persistedOK := toolMetadataInt(metadata, "persisted_bytes")
	omittedBytes, omittedOK := toolMetadataInt(metadata, "omitted_bytes")
	displayOmittedBytes, displayOmittedOK := toolMetadataInt(metadata, "display_omitted_bytes")
	if !inlineOK || !displayInlineOK || !rawOK || !displayRawOK || !persistedOK || !omittedOK || !displayOmittedOK {
		return false
	}
	if inlineBytes != len(result.LLMOutput) || displayInlineBytes != len(result.DisplayOutput) || rawBytes < 0 || displayRawBytes < 0 || persistedBytes < 0 || omittedBytes < 0 || displayOmittedBytes < 0 || persistedBytes > rawBytes || omittedBytes > rawBytes {
		return false
	}
	artifactPath, pathOK := metadata["artifact_path"].(string)
	budgetReason, reasonOK := metadata["budget_reason"].(string)
	artifactComplete, completeOK := metadata["artifact_complete"].(bool)
	artifactTruncated, truncatedOK := metadata["artifact_truncated"].(bool)
	recoverable, recoverableOK := metadata["recoverable"].(bool)
	if !pathOK || !reasonOK || !completeOK || !truncatedOK || !recoverableOK || strings.TrimSpace(budgetReason) == "" || artifactComplete && artifactTruncated {
		return false
	}
	if artifactComplete && (strings.TrimSpace(artifactPath) == "" || !recoverable || persistedBytes != rawBytes || omittedBytes != 0) {
		return false
	}
	if artifactTruncated && (strings.TrimSpace(artifactPath) == "" || recoverable || persistedBytes >= rawBytes || omittedBytes != rawBytes-persistedBytes) {
		return false
	}
	if strings.TrimSpace(artifactPath) == "" && (artifactComplete || artifactTruncated) {
		return false
	}
	return true
}

func toolOutputBudgetVersionApplied(metadata map[string]any) bool {
	if metadata == nil {
		return false
	}
	switch value := metadata["tool_output_budget_version"].(type) {
	case int:
		return value == toolOutputBudgetVersion
	case int64:
		return value == toolOutputBudgetVersion
	case float64:
		return value == toolOutputBudgetVersion
	case json.Number:
		parsed, err := value.Int64()
		return err == nil && parsed == toolOutputBudgetVersion
	default:
		return false
	}
}

func toolOutputArtifactNotice(inlineLimit int, artifactPath string, rawBytes, persistedBytes, omittedBytes int, complete, truncated bool, reason string) string {
	switch {
	case complete && artifactPath != "":
		return fmt.Sprintf("[Tool output exceeded llm_output_max_bytes=%d. Complete artifact: %s (%d bytes). Use read_file(path=%q, offset=1, limit=120).]", inlineLimit, artifactPath, rawBytes, artifactPath)
	case truncated && artifactPath != "":
		return fmt.Sprintf("[Tool output exceeded llm_output_max_bytes=%d. Partial artifact: %s (%d/%d bytes; %d omitted; reason=%s). Full output is not recoverable.]", inlineLimit, artifactPath, persistedBytes, rawBytes, omittedBytes, reason)
	default:
		return fmt.Sprintf("[Tool output exceeded llm_output_max_bytes=%d; no artifact was saved (reason=%s). Full output is not recoverable.]", inlineLimit, reason)
	}
}

func boundedToolOutputPreview(raw, notice string, maxBytes int) (string, int) {
	if maxBytes <= 0 {
		return "", 0
	}
	raw = strings.ToValidUTF8(raw, "\uFFFD")
	if notice == "" && len(raw) <= maxBytes {
		return raw, len(raw)
	}
	if len(notice) >= maxBytes {
		return prefixAtRuneBoundary(notice, maxBytes), 0
	}
	separator := "\n[Bounded preview]\n"
	markerTemplate := "\n[... %d source bytes omitted from inline preview ...]\n"
	available := maxBytes - len(notice) - len(separator)
	if available <= 0 {
		return notice, 0
	}
	if len(raw) <= available {
		return notice + separator + raw, len(raw)
	}
	marker := fmt.Sprintf(markerTemplate, len(raw))
	available -= len(marker)
	if available <= 0 {
		return notice, 0
	}
	headLimit := available / 2
	tailLimit := available - headLimit
	head := prefixAtRuneBoundary(raw, headLimit)
	tail := suffixAtRuneBoundary(raw, tailLimit)
	omitted := len(raw) - len(head) - len(tail)
	marker = fmt.Sprintf(markerTemplate, omitted)
	for len(notice)+len(separator)+len(head)+len(marker)+len(tail) > maxBytes && (len(head) > 0 || len(tail) > 0) {
		if len(tail) > len(head) {
			tail = suffixAtRuneBoundary(tail, len(tail)-1)
		} else {
			head = prefixAtRuneBoundary(head, len(head)-1)
		}
		omitted = len(raw) - len(head) - len(tail)
		marker = fmt.Sprintf(markerTemplate, omitted)
	}
	output := notice + separator + head + marker + tail
	if len(output) > maxBytes {
		output = prefixAtRuneBoundary(output, maxBytes)
	}
	return output, len(head) + len(tail)
}
