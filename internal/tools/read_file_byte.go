package tools

import (
	"fmt"
	"math"
	"unicode/utf8"

	"aegis-agent/internal/config"
	"aegis-agent/internal/fileutil"
	"aegis-agent/internal/session"
)

const (
	readFileMaxByteLimit int64 = 24 * 1024
)

func executeReadFileByteMode(execCtx ExecContext, path, displayBase, source, skillName string, requestedOffset, requestedLimit int64) session.ToolResult {
	windowLimit := normalizeReadFileByteLimit(execCtx.Config, requestedLimit)
	llmLimit := toolOutputLLMMaxBytes(execCtx.Config)
	if windowLimit <= 0 {
		return typedToolErrorResult("read_file", FailureClassOutputBudgetTooSmall, FailureClassOutputBudgetTooSmall, "effective byte window is too small to return UTF-8 text")
	}

	readStart := requestedOffset
	if readStart > utf8.UTFMax-1 {
		readStart -= utf8.UTFMax - 1
	} else {
		readStart = 0
	}
	lookbehind := requestedOffset - readStart
	readLimit := windowLimit + lookbehind + utf8.UTFMax
	data, info, err := fileutil.ReadRegularFileRangeNoSymlink(path, readStart, readLimit)
	if err != nil {
		return readFileErrorResult(path, err)
	}
	if source != "session_ephemeral_artifact" && info.Size() > fileutil.MaxRegularFileReadBytes {
		return readFileErrorResult(path, fmt.Errorf("file exceeds maximum readable size: %s (%d > %d bytes)", path, info.Size(), fileutil.MaxRegularFileReadBytes))
	}

	totalBytes := info.Size()
	requestedStart := requestedOffset
	if requestedStart > totalBytes {
		requestedStart = totalBytes
	}
	rawRequestedEnd := saturatingAddInt64(requestedOffset, requestedLimit)
	requestedEnd := saturatingAddInt64(requestedOffset, windowLimit)
	if requestedEnd > totalBytes {
		requestedEnd = totalBytes
	}

	effectiveStart := requestedStart
	effectiveEnd := requestedEnd
	startAdjusted := requestedStart != requestedOffset
	endAdjusted := false
	body := []byte{}
	if requestedOffset < totalBytes {
		effectiveStart, startAdjusted, err = adjustReadFileUTF8Start(data, readStart, requestedStart, totalBytes)
		if err != nil {
			return readFileEncodingError(path, err)
		}
		effectiveEnd, endAdjusted, err = adjustReadFileUTF8End(data, readStart, requestedEnd, totalBytes)
		if err != nil {
			return readFileEncodingError(path, err)
		}
		if effectiveEnd < effectiveStart {
			effectiveEnd = effectiveStart
			endAdjusted = true
		}
		var ok bool
		body, ok = readFileRangeSlice(data, readStart, effectiveStart, effectiveEnd)
		if !ok {
			return typedToolErrorResult("read_file", FailureClassHarnessError, "range_unavailable", "bounded range reader did not return the requested source window")
		}
	}
	if !utf8.Valid(body) {
		return readFileEncodingError(path, fmt.Errorf("requested byte window is not valid UTF-8"))
	}
	if len(body) == 0 && effectiveStart < totalBytes {
		return typedToolErrorResult("read_file", FailureClassOutputBudgetTooSmall, FailureClassOutputBudgetTooSmall, "byte_limit is too small to contain one complete UTF-8 rune at the requested offset")
	}

	displayPath := relativeOrAbsolute(displayBase, path)
	for attempt := 0; attempt < 4; attempt++ {
		header := formatReadFileByteHeader(displayPath, effectiveStart, effectiveEnd, totalBytes, requestedOffset, requestedLimit)
		separatorBytes := 0
		if len(body) > 0 {
			separatorBytes = 1
		}
		availableBody := llmLimit - len(header) - separatorBytes
		if availableBody < 0 {
			return readFileOutputBudgetError(path, llmLimit)
		}
		if len(body) <= availableBody {
			output := header
			if len(body) > 0 {
				output += "\n" + string(body)
			}
			metadata := map[string]any{
				"path":                  path,
				"path_source":           source,
				"mode":                  "byte",
				"encoding":              "utf-8",
				"requested_byte_offset": requestedOffset,
				"requested_byte_limit":  requestedLimit,
				"effective_byte_start":  effectiveStart,
				"effective_byte_end":    effectiveEnd,
				"start_adjusted":        startAdjusted || requestedStart != requestedOffset,
				"end_adjusted":          endAdjusted || effectiveEnd != rawRequestedEnd,
				"returned_bytes":        int64(len(body)),
				"total_bytes":           totalBytes,
				"has_more":              effectiveEnd < totalBytes,
				"next_byte_offset":      effectiveEnd,
			}
			if skillName != "" {
				metadata["skill"] = skillName
			}
			return session.ToolResult{
				Name:          "read_file",
				LLMOutput:     output,
				DisplayOutput: output,
				Metadata:      metadata,
			}
		}

		shortened := []byte(prefixAtRuneBoundary(string(body), availableBody))
		if len(shortened) == 0 && effectiveStart < totalBytes {
			return readFileOutputBudgetError(path, llmLimit)
		}
		body = shortened
		effectiveEnd = effectiveStart + int64(len(body))
		endAdjusted = true
	}
	return readFileOutputBudgetError(path, llmLimit)
}

func adjustReadFileUTF8Start(data []byte, dataStart, requestedStart, totalBytes int64) (int64, bool, error) {
	if requestedStart >= totalBytes {
		return totalBytes, false, nil
	}
	value, ok := readFileRangeByte(data, dataStart, requestedStart)
	if !ok {
		return 0, false, errorsRangeUnavailable(requestedStart)
	}
	if value&0xc0 != 0x80 {
		return requestedStart, false, nil
	}
	lead := requestedStart
	for steps := 0; steps < utf8.UTFMax-1 && lead > 0; steps++ {
		lead--
		candidate, exists := readFileRangeByte(data, dataStart, lead)
		if !exists {
			return 0, false, errorsRangeUnavailable(lead)
		}
		if candidate&0xc0 != 0x80 {
			segment, exists := readFileRangeSlice(data, dataStart, lead, min(totalBytes, lead+utf8.UTFMax))
			if !exists {
				return 0, false, errorsRangeUnavailable(lead)
			}
			runeValue, runeSize := utf8.DecodeRune(segment)
			if (runeValue == utf8.RuneError && runeSize == 1) || lead+int64(runeSize) <= requestedStart {
				return 0, false, fmt.Errorf("requested byte_offset=%d falls inside invalid UTF-8", requestedStart)
			}
			return lead + int64(runeSize), true, nil
		}
	}
	return 0, false, fmt.Errorf("requested byte_offset=%d falls inside invalid UTF-8", requestedStart)
}

func adjustReadFileUTF8End(data []byte, dataStart, requestedEnd, totalBytes int64) (int64, bool, error) {
	if requestedEnd <= 0 || requestedEnd >= totalBytes {
		return requestedEnd, false, nil
	}
	value, ok := readFileRangeByte(data, dataStart, requestedEnd)
	if !ok {
		return 0, false, errorsRangeUnavailable(requestedEnd)
	}
	if value&0xc0 != 0x80 {
		return requestedEnd, false, nil
	}
	lead := requestedEnd
	for steps := 0; steps < utf8.UTFMax-1 && lead > 0; steps++ {
		lead--
		candidate, exists := readFileRangeByte(data, dataStart, lead)
		if !exists {
			return 0, false, errorsRangeUnavailable(lead)
		}
		if candidate&0xc0 != 0x80 {
			segment, exists := readFileRangeSlice(data, dataStart, lead, min(totalBytes, lead+utf8.UTFMax))
			if !exists {
				return 0, false, errorsRangeUnavailable(lead)
			}
			runeValue, runeSize := utf8.DecodeRune(segment)
			if (runeValue == utf8.RuneError && runeSize == 1) || lead+int64(runeSize) <= requestedEnd {
				return 0, false, fmt.Errorf("requested byte window ends inside invalid UTF-8 at offset %d", requestedEnd)
			}
			return lead, true, nil
		}
	}
	return 0, false, fmt.Errorf("requested byte window ends inside invalid UTF-8 at offset %d", requestedEnd)
}

func readFileRangeByte(data []byte, dataStart, position int64) (byte, bool) {
	index := position - dataStart
	if index < 0 || index >= int64(len(data)) {
		return 0, false
	}
	return data[index], true
}

func readFileRangeSlice(data []byte, dataStart, start, end int64) ([]byte, bool) {
	startIndex := start - dataStart
	endIndex := end - dataStart
	if startIndex < 0 || endIndex < startIndex || endIndex > int64(len(data)) {
		return nil, false
	}
	return data[startIndex:endIndex], true
}

func errorsRangeUnavailable(position int64) error {
	return fmt.Errorf("bounded range does not contain source byte %d", position)
}

func formatReadFileByteHeader(path string, start, end, total, requestedOffset, requestedLimit int64) string {
	return fmt.Sprintf("[read_file path=%s bytes=%d-%d of %d; requested=%d+%d; encoding=utf-8]", path, start, end, total, requestedOffset, requestedLimit)
}

func saturatingAddInt64(left, right int64) int64 {
	if right > 0 && left > math.MaxInt64-right {
		return math.MaxInt64
	}
	return left + right
}

func toolOutputLLMMaxBytes(cfg *config.Config) int {
	if cfg != nil && cfg.Runtime.ToolOutput.LLMOutputMaxBytes > 0 {
		return cfg.Runtime.ToolOutput.LLMOutputMaxBytes
	}
	return config.DefaultToolOutputLLMMaxBytes
}

func readFileEncodingError(path string, err error) session.ToolResult {
	result := typedToolErrorResult("read_file", FailureClassUnsupportedEncoding, FailureClassUnsupportedEncoding, fmt.Sprintf("%s is not readable as UTF-8 text: %v", path, err))
	result.Metadata["path"] = path
	result.Metadata["encoding"] = "utf-8"
	return result
}

func readFileOutputBudgetError(path string, byteLimit int) session.ToolResult {
	result := typedToolErrorResult("read_file", FailureClassOutputBudgetTooSmall, FailureClassOutputBudgetTooSmall, fmt.Sprintf("read_file header and one complete UTF-8 rune do not fit model-visible output budget=%d; shorten the path or increase runtime.tool_output.llm_output_max_bytes", byteLimit))
	result.Metadata["path"] = path
	return result
}

func typedToolErrorResult(tool, failureClass, errorCode, message string) session.ToolResult {
	result := errorResult(tool, fmt.Errorf("%s", message))
	setToolResultFailureClass(&result, failureClass)
	if result.Metadata == nil {
		result.Metadata = make(map[string]any)
	}
	result.Metadata["error_code"] = errorCode
	return result
}

func schemaRejectResult(tool string, err error) session.ToolResult {
	result := errorResult(tool, err)
	setToolResultFailureClass(&result, FailureClassSchemaReject)
	return result
}
