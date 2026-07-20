package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

const (
	HistoricalMessageContentSchemaVersion = 1
	MaxHistoricalMessageContentPageBytes  = 16 * 1024
)

var (
	ErrMessageNotFound              = errors.New("session message not found")
	ErrMessageContentWindowTooSmall = errors.New("message content byte window is too small")
)

type MessageContentRange struct {
	SchemaVersion       int    `json:"schema_version"`
	HistoricalReference bool   `json:"historical_reference"`
	MessageID           string `json:"message_id"`
	Role                string `json:"role"`
	CreatedAt           string `json:"created_at"`
	Source              string `json:"source,omitempty"`
	RequestedByteOffset int64  `json:"requested_byte_offset"`
	RequestedByteLimit  int    `json:"requested_byte_limit"`
	EffectiveByteStart  int64  `json:"effective_byte_start"`
	EffectiveByteEnd    int64  `json:"effective_byte_end"`
	StartAdjusted       bool   `json:"start_adjusted"`
	EndAdjusted         bool   `json:"end_adjusted"`
	ReturnedBytes       int64  `json:"returned_bytes"`
	TotalBytes          int64  `json:"total_bytes"`
	HasMore             bool   `json:"has_more"`
	NextByteOffset      int64  `json:"next_byte_offset"`
	Content             string `json:"content"`
}

type historicalMessageContent struct {
	SchemaVersion                int                           `json:"schema_version"`
	HistoricalReference          bool                          `json:"historical_reference"`
	MessageID                    string                        `json:"message_id"`
	Role                         string                        `json:"role"`
	CreatedAt                    string                        `json:"created_at"`
	Source                       string                        `json:"source,omitempty"`
	Kind                         string                        `json:"kind,omitempty"`
	Turn                         any                           `json:"turn,omitempty"`
	Interrupt                    *bool                         `json:"interrupt,omitempty"`
	Text                         string                        `json:"text,omitempty"`
	ToolCalls                    []historicalToolCallContent   `json:"tool_calls,omitempty"`
	ToolResults                  []historicalToolResultContent `json:"tool_results,omitempty"`
	ThinkingOmitted              bool                          `json:"thinking_omitted,omitempty"`
	ProviderContentBlocksOmitted int                           `json:"provider_content_blocks_omitted,omitempty"`
}

type historicalToolCallContent struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Arguments      json.RawMessage `json:"arguments"`
	ProviderCallID string          `json:"provider_call_id,omitempty"`
}

type historicalToolResultContent struct {
	ToolCallID        string         `json:"tool_call_id,omitempty"`
	Name              string         `json:"name"`
	LLMOutput         string         `json:"llm_output"`
	IsError           bool           `json:"is_error"`
	Final             bool           `json:"final"`
	ReferenceMetadata map[string]any `json:"reference_metadata,omitempty"`
}

var historicalToolReferenceMetadataKeys = []string{
	"artifact_path",
	"artifact_complete",
	"artifact_truncated",
	"recoverable",
	"path",
	"path_source",
	"source",
	"source_session_id",
	"session_id",
	"child_session_id",
	"queue_job_id",
	"failure_class",
	"error_code",
	"mode",
	"has_more",
	"next_byte_offset",
	"next_cursor",
	"cursor",
	"stop_reason",
	"returned_count",
	"requested_byte_offset",
	"requested_byte_limit",
	"effective_byte_start",
	"effective_byte_end",
}

// LoadMessageContentRange locates and validates one canonical messages.jsonl
// record, converts it to the stable model-visible history representation, and
// returns one UTF-8-safe byte page. The canonical record stays authoritative;
// this method never reads a transcript or provider sidecar.
func (s *Store) LoadMessageContentRange(sessionID, messageID string, byteOffset int64, byteLimit int) (MessageContentRange, error) {
	var result MessageContentRange
	if _, err := s.sessionPath(sessionID, "messages.jsonl"); err != nil {
		return result, err
	}
	if err := validateStoreID("message", messageID); err != nil {
		return result, err
	}
	if byteOffset < 0 {
		return result, errors.New("message content byte_offset must be non-negative")
	}
	if byteLimit <= 0 {
		return result, errors.New("message content byte_limit must be positive")
	}
	if byteLimit > MaxHistoricalMessageContentPageBytes {
		return result, fmt.Errorf("message content byte_limit exceeds maximum %d", MaxHistoricalMessageContentPageBytes)
	}

	var message Message
	found := false
	err := s.VisitMessages(sessionID, func(candidate Message) error {
		if candidate.ID != messageID {
			return nil
		}
		message = candidate
		found = true
		return errHistoricalMessageLocated
	})
	if err != nil && !errors.Is(err, errHistoricalMessageLocated) {
		return result, err
	}
	if !found {
		return result, fmt.Errorf("%w: %s", ErrMessageNotFound, messageID)
	}

	content, err := marshalHistoricalMessageContent(message)
	if err != nil {
		return result, fmt.Errorf("encode historical message %s: %w", messageID, err)
	}
	if !utf8.Valid(content) {
		return result, fmt.Errorf("historical message %s representation is not valid UTF-8", messageID)
	}

	total := int64(len(content))
	requestedStart := byteOffset
	if requestedStart > total {
		requestedStart = total
	}
	effectiveStart := requestedStart
	for effectiveStart < total && effectiveStart > 0 && !utf8.RuneStart(content[effectiveStart]) {
		effectiveStart++
	}

	rawEnd := saturatingHistoryAdd(byteOffset, int64(byteLimit))
	requestedEnd := rawEnd
	if requestedEnd > total {
		requestedEnd = total
	}
	effectiveEnd := requestedEnd
	for effectiveEnd > effectiveStart && effectiveEnd < total && !utf8.RuneStart(content[effectiveEnd]) {
		effectiveEnd--
	}
	if effectiveEnd < effectiveStart {
		effectiveEnd = effectiveStart
	}
	if effectiveEnd == effectiveStart && effectiveStart < total {
		return result, fmt.Errorf("%w to contain one complete UTF-8 rune at the requested offset", ErrMessageContentWindowTooSmall)
	}

	page := content[effectiveStart:effectiveEnd]
	source, _ := message.Meta["source"].(string)
	return MessageContentRange{
		SchemaVersion:       HistoricalMessageContentSchemaVersion,
		HistoricalReference: true,
		MessageID:           message.ID,
		Role:                message.Role,
		CreatedAt:           message.CreatedAt,
		Source:              source,
		RequestedByteOffset: byteOffset,
		RequestedByteLimit:  byteLimit,
		EffectiveByteStart:  effectiveStart,
		EffectiveByteEnd:    effectiveEnd,
		StartAdjusted:       effectiveStart != byteOffset,
		EndAdjusted:         effectiveEnd != rawEnd,
		ReturnedBytes:       int64(len(page)),
		TotalBytes:          total,
		HasMore:             effectiveEnd < total,
		NextByteOffset:      effectiveEnd,
		Content:             string(page),
	}, nil
}

var errHistoricalMessageLocated = errors.New("historical message located")

func marshalHistoricalMessageContent(message Message) ([]byte, error) {
	content := historicalMessageContent{
		SchemaVersion:                HistoricalMessageContentSchemaVersion,
		HistoricalReference:          true,
		MessageID:                    message.ID,
		Role:                         message.Role,
		CreatedAt:                    message.CreatedAt,
		Text:                         message.Text,
		ThinkingOmitted:              strings.TrimSpace(message.Thinking) != "",
		ProviderContentBlocksOmitted: len(message.ProviderContentBlocks),
	}
	if source, ok := historicalScalarMetadata(message.Meta["source"]); ok {
		content.Source, _ = source.(string)
	}
	if kind, ok := historicalScalarMetadata(message.Meta["kind"]); ok {
		content.Kind, _ = kind.(string)
	}
	if turn, ok := historicalScalarMetadata(message.Meta["turn"]); ok {
		content.Turn = turn
	}
	if interrupt, ok := message.Meta["interrupt"].(bool); ok {
		content.Interrupt = &interrupt
	}
	if len(message.ToolCalls) > 0 {
		content.ToolCalls = make([]historicalToolCallContent, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			content.ToolCalls = append(content.ToolCalls, historicalToolCallContent{
				ID:             call.ID,
				Name:           call.Name,
				Arguments:      append(json.RawMessage(nil), call.Arguments...),
				ProviderCallID: call.ProviderCallID,
			})
		}
	}
	if len(message.ToolResults) > 0 {
		content.ToolResults = make([]historicalToolResultContent, 0, len(message.ToolResults))
		for _, toolResult := range message.ToolResults {
			content.ToolResults = append(content.ToolResults, historicalToolResultContent{
				ToolCallID:        toolResult.ToolCallID,
				Name:              toolResult.Name,
				LLMOutput:         toolResult.LLMOutput,
				IsError:           toolResult.IsError,
				Final:             toolResult.Final,
				ReferenceMetadata: selectHistoricalMetadata(toolResult.Metadata, historicalToolReferenceMetadataKeys),
			})
		}
	}
	return json.Marshal(content)
}

func selectHistoricalMetadata(metadata map[string]any, allowlist []string) map[string]any {
	if len(metadata) == 0 || len(allowlist) == 0 {
		return nil
	}
	out := make(map[string]any)
	for _, key := range allowlist {
		if value, ok := historicalScalarMetadata(metadata[key]); ok {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func historicalScalarMetadata(value any) (any, bool) {
	switch typed := value.(type) {
	case string, bool,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64, json.Number:
		return typed, true
	default:
		return nil, false
	}
}

func saturatingHistoryAdd(left, right int64) int64 {
	if right > 0 && left > math.MaxInt64-right {
		return math.MaxInt64
	}
	return left + right
}
