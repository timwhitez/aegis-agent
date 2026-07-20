package tools

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/session"
)

const (
	searchCursorVersion            = 1
	maxSearchCursorEncodedBytes    = 2048
	maxSearchCursorIndex           = 1_000_000_000
	maxSearchCursorDiagnosticBytes = 64

	defaultSearchOutputByteLimit = 24 * 1024
	maxSearchOutputByteLimit     = 32 * 1024
	minSearchOutputByteLimit     = 512
)

type searchCursorPayload struct {
	Version     int    `json:"v"`
	Tool        string `json:"t"`
	Fingerprint string `json:"q"`
	NextIndex   int    `json:"i"`
	LastPath    string `json:"p,omitempty"`
	LastLine    int    `json:"l,omitempty"`
	Checksum    string `json:"c"`
}

type searchCursorQuery struct {
	Tool       string `json:"tool"`
	RootPath   string `json:"root_path"`
	RootSource string `json:"root_source"`
	Pattern    string `json:"pattern"`
	Include    string `json:"include"`
}

type searchCursorError struct {
	Code    string
	Message string
}

func (e *searchCursorError) Error() string {
	if e == nil {
		return "invalid search cursor"
	}
	if strings.TrimSpace(e.Message) == "" {
		return "invalid search cursor: " + e.Code
	}
	return "invalid search cursor: " + e.Message
}

func newSearchCursorQuery(tool string, root resolvedSearchRoot, pattern, include string) searchCursorQuery {
	return searchCursorQuery{
		Tool:       tool,
		RootPath:   root.path,
		RootSource: root.source,
		Pattern:    pattern,
		Include:    include,
	}
}

func (q searchCursorQuery) fingerprint() (string, error) {
	raw, err := json.Marshal(q)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func encodeSearchCursor(query searchCursorQuery, nextIndex int, lastPath string, lastLine int) (string, error) {
	if nextIndex < 0 || nextIndex > maxSearchCursorIndex {
		return "", &searchCursorError{Code: "index_out_of_range", Message: "continuation index is out of range"}
	}
	fingerprint, err := query.fingerprint()
	if err != nil {
		return "", err
	}
	payload := searchCursorPayload{
		Version:     searchCursorVersion,
		Tool:        query.Tool,
		Fingerprint: fingerprint,
		NextIndex:   nextIndex,
		LastPath:    boundedSearchCursorDiagnostic(lastPath),
		LastLine:    lastLine,
	}
	payload.Checksum, err = searchCursorChecksum(payload)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	if len(token) > maxSearchCursorEncodedBytes {
		return "", &searchCursorError{Code: "token_too_large", Message: fmt.Sprintf("encoded cursor exceeds %d bytes", maxSearchCursorEncodedBytes)}
	}
	return token, nil
}

func decodeSearchCursor(token string, query searchCursorQuery) (int, error) {
	if token == "" {
		return 0, nil
	}
	if strings.TrimSpace(token) != token {
		return 0, &searchCursorError{Code: "malformed", Message: "cursor contains surrounding whitespace"}
	}
	if len(token) > maxSearchCursorEncodedBytes {
		return 0, &searchCursorError{Code: "token_too_large", Message: fmt.Sprintf("encoded cursor exceeds %d bytes", maxSearchCursorEncodedBytes)}
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, &searchCursorError{Code: "malformed", Message: "cursor is not canonical base64url"}
	}
	if base64.RawURLEncoding.EncodeToString(raw) != token {
		return 0, &searchCursorError{Code: "malformed", Message: "cursor is not canonical base64url"}
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var payload searchCursorPayload
	if err := decoder.Decode(&payload); err != nil {
		return 0, &searchCursorError{Code: "malformed", Message: "cursor payload is invalid"}
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return 0, &searchCursorError{Code: "malformed", Message: "cursor payload contains trailing data"}
	}
	if payload.Version != searchCursorVersion {
		return 0, &searchCursorError{Code: "unsupported_version", Message: fmt.Sprintf("cursor version %d is unsupported", payload.Version)}
	}
	wantChecksum, err := searchCursorChecksum(payload)
	if err != nil {
		return 0, err
	}
	if subtle.ConstantTimeCompare([]byte(payload.Checksum), []byte(wantChecksum)) != 1 {
		return 0, &searchCursorError{Code: "checksum_mismatch", Message: "cursor checksum does not match its payload"}
	}
	if payload.Tool != query.Tool {
		return 0, &searchCursorError{Code: "tool_mismatch", Message: "cursor belongs to a different search tool"}
	}
	fingerprint, err := query.fingerprint()
	if err != nil {
		return 0, err
	}
	if subtle.ConstantTimeCompare([]byte(payload.Fingerprint), []byte(fingerprint)) != 1 {
		return 0, &searchCursorError{Code: "query_mismatch", Message: "cursor does not match the resolved root, source, pattern, or include filter"}
	}
	if payload.NextIndex < 0 || payload.NextIndex > maxSearchCursorIndex {
		return 0, &searchCursorError{Code: "index_out_of_range", Message: "continuation index is out of range"}
	}
	return payload.NextIndex, nil
}

func searchCursorChecksum(payload searchCursorPayload) (string, error) {
	payload.Checksum = ""
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(sum[:16]), nil
}

func boundedSearchCursorDiagnostic(path string) string {
	if path == "" {
		return ""
	}
	if utf8.ValidString(path) && len(path) <= maxSearchCursorDiagnosticBytes {
		return path
	}
	sum := sha256.Sum256([]byte(path))
	return "h:" + base64.RawURLEncoding.EncodeToString(sum[:16])
}

type searchOutputBudget struct {
	Requested int
	Effective int
	Capped    bool
}

func normalizeSearchOutputByteLimit(cfg *config.Config, requestedValue *int) (searchOutputBudget, error) {
	requested := 0
	selected := defaultSearchOutputByteLimit
	if requestedValue != nil {
		requested = *requestedValue
		selected = requested
		if requested < minSearchOutputByteLimit {
			return searchOutputBudget{}, fmt.Errorf("byte_limit must be at least %d bytes", minSearchOutputByteLimit)
		}
	}
	if selected <= 0 {
		selected = defaultSearchOutputByteLimit
	}
	effective := selected
	if effective > maxSearchOutputByteLimit {
		effective = maxSearchOutputByteLimit
	}
	llmLimit := config.DefaultToolOutputLLMMaxBytes
	if cfg != nil && cfg.Runtime.ToolOutput.LLMOutputMaxBytes > 0 {
		llmLimit = cfg.Runtime.ToolOutput.LLMOutputMaxBytes
	}
	if effective > llmLimit {
		effective = llmLimit
	}
	if effective < minSearchOutputByteLimit {
		return searchOutputBudget{}, fmt.Errorf("effective model-visible output budget %d is below the minimum recoverable search page size %d", effective, minSearchOutputByteLimit)
	}
	return searchOutputBudget{
		Requested: requested,
		Effective: effective,
		Capped:    selected > effective,
	}, nil
}

type searchPageRecord struct {
	Preferred        string
	Prefix           string
	Snippet          string
	SpanSuffix       string
	CanCompact       bool
	SnippetTruncated bool
	Metadata         map[string]any
	Path             string
	Line             int
}

type renderedSearchRecord struct {
	Text             string
	SnippetTruncated bool
	Metadata         map[string]any
}

func plainSearchPageRecord(text, path string, line int) searchPageRecord {
	return searchPageRecord{Preferred: text, Path: path, Line: line}
}

func grepSearchPageRecord(prefix, rawSnippet, preferredSnippet, spanSuffix, path string, line int, snippetTruncated bool, metadata map[string]any) searchPageRecord {
	preferred := prefix + preferredSnippet
	if snippetTruncated {
		preferred += spanSuffix
	}
	return searchPageRecord{
		Preferred:        preferred,
		Prefix:           prefix,
		Snippet:          rawSnippet,
		SpanSuffix:       spanSuffix,
		CanCompact:       true,
		SnippetTruncated: snippetTruncated,
		Metadata:         metadata,
		Path:             path,
		Line:             line,
	}
}

func (record searchPageRecord) minimumBytes() int {
	if !record.CanCompact {
		return len(record.Preferred)
	}
	minimum := len(record.Prefix) + len(record.SpanSuffix)
	if len(record.Preferred) < minimum {
		return len(record.Preferred)
	}
	return minimum
}

func (record searchPageRecord) render(maxBytes int) (renderedSearchRecord, bool) {
	if maxBytes < 0 || !utf8.ValidString(record.Preferred) {
		return renderedSearchRecord{}, false
	}
	if len(record.Preferred) <= maxBytes {
		metadata := cloneSearchRecordMetadata(record.Metadata)
		if metadata != nil {
			metadata["snippet_truncated"] = record.SnippetTruncated
		}
		return renderedSearchRecord{
			Text:             record.Preferred,
			SnippetTruncated: record.SnippetTruncated,
			Metadata:         metadata,
		}, true
	}
	if !record.CanCompact || record.minimumBytes() > maxBytes {
		return renderedSearchRecord{}, false
	}
	snippetBudget := maxBytes - len(record.Prefix) - len(record.SpanSuffix)
	snippet, _ := truncateGrepMatchedText(record.Snippet, snippetBudget)
	metadata := cloneSearchRecordMetadata(record.Metadata)
	if metadata != nil {
		metadata["snippet_truncated"] = true
	}
	return renderedSearchRecord{
		Text:             record.Prefix + snippet + record.SpanSuffix,
		SnippetTruncated: true,
		Metadata:         metadata,
	}, true
}

func cloneSearchRecordMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return nil
	}
	clone := make(map[string]any, len(metadata)+1)
	for key, value := range metadata {
		clone[key] = value
	}
	return clone
}

type searchRecordCollector struct {
	skip     int
	capacity int
	seen     int
	stopped  bool
	records  []searchPageRecord
}

func newSearchRecordCollector(skip, effectiveLimit int) *searchRecordCollector {
	if skip < 0 {
		skip = 0
	}
	return &searchRecordCollector{skip: skip, capacity: effectiveLimit + 1}
}

func (collector *searchRecordCollector) add(record searchPageRecord) bool {
	index := collector.seen
	collector.seen++
	if index < collector.skip {
		return false
	}
	collector.records = append(collector.records, record)
	if len(collector.records) >= collector.capacity {
		collector.stopped = true
		return true
	}
	return false
}

type searchPageOptions struct {
	Tool               string
	Query              searchCursorQuery
	StartIndex         int
	Records            []searchPageRecord
	ScanComplete       bool
	RequestedLimit     int
	EffectiveLimit     int
	RequestedByteLimit int
	EffectiveByteLimit int
	ByteLimitCapped    bool
}

type searchPage struct {
	Output                 string
	Metadata               map[string]any
	RenderedRecordMetadata []map[string]any
	TruncatedSnippetCount  int
}

func buildSearchPage(options searchPageOptions) (searchPage, error) {
	if options.EffectiveLimit <= 0 {
		return searchPage{}, errors.New("effective search limit must be positive")
	}
	if options.EffectiveByteLimit < minSearchOutputByteLimit {
		return searchPage{}, fmt.Errorf("effective search byte limit %d is too small", options.EffectiveByteLimit)
	}
	if len(options.Records) == 0 {
		if !options.ScanComplete {
			return searchPage{}, errors.New("search stopped without collecting a continuation record")
		}
		output := "(no matches)"
		return searchPage{
			Output:   output,
			Metadata: baseSearchPageMetadata(options, 0, false, "complete", false, false, len(output)),
		}, nil
	}

	countCandidate := len(options.Records)
	if countCandidate > options.EffectiveLimit {
		countCandidate = options.EffectiveLimit
	}
	for returnedCount := countCandidate; returnedCount >= 1; returnedCount-- {
		hasMore := !options.ScanComplete || returnedCount < len(options.Records)
		countReached := hasMore && returnedCount == options.EffectiveLimit
		byteReached := returnedCount < countCandidate
		stopReason := "complete"
		if hasMore {
			if byteReached {
				stopReason = "byte_limit"
			} else {
				stopReason = "match_limit"
			}
		}

		output, rendered, ok, err := renderSearchPageCandidate(options, returnedCount, hasMore, stopReason)
		if err != nil {
			return searchPage{}, err
		}
		if !ok {
			continue
		}

		// If the count boundary and output boundary reject the same next
		// record, byte_limit is the primary stop reason while both facts
		// remain observable.
		if countReached && returnedCount < len(options.Records) {
			nextHasMore := !options.ScanComplete || returnedCount+1 < len(options.Records)
			_, _, nextFits, nextErr := renderSearchPageCandidate(options, returnedCount+1, nextHasMore, "byte_limit")
			if nextErr != nil {
				return searchPage{}, nextErr
			}
			if !nextFits {
				byteReached = true
				stopReason = "byte_limit"
				output, rendered, ok, err = renderSearchPageCandidate(options, returnedCount, true, stopReason)
				if err != nil {
					return searchPage{}, err
				}
				if !ok {
					continue
				}
			}
		}

		metadata := baseSearchPageMetadata(options, returnedCount, hasMore, stopReason, countReached, byteReached, len(output))
		recordMetadata := make([]map[string]any, 0, returnedCount)
		truncatedSnippetCount := 0
		for _, record := range rendered {
			if record.SnippetTruncated {
				truncatedSnippetCount++
			}
			if record.Metadata != nil {
				recordMetadata = append(recordMetadata, record.Metadata)
			}
		}
		metadata["truncated_snippet_count"] = truncatedSnippetCount
		return searchPage{
			Output:                 output,
			Metadata:               metadata,
			RenderedRecordMetadata: recordMetadata,
			TruncatedSnippetCount:  truncatedSnippetCount,
		}, nil
	}

	return searchPage{}, fmt.Errorf("the first recoverable search record and continuation cursor exceed byte_limit=%d", options.EffectiveByteLimit)
}

func renderSearchPageCandidate(options searchPageOptions, returnedCount int, hasMore bool, stopReason string) (string, []renderedSearchRecord, bool, error) {
	if returnedCount < 0 || returnedCount > len(options.Records) {
		return "", nil, false, nil
	}
	footer := ""
	if hasMore {
		last := options.Records[returnedCount-1]
		cursor, err := encodeSearchCursor(options.Query, options.StartIndex+returnedCount, last.Path, last.Line)
		if err != nil {
			return "", nil, false, err
		}
		footer = searchContinuationFooter(options.Tool, options.EffectiveLimit, stopReason, cursor)
	}
	separatorBytes := returnedCount - 1
	if hasMore {
		separatorBytes++
	}
	minimum := len(footer) + separatorBytes
	for index := 0; index < returnedCount; index++ {
		minimum += options.Records[index].minimumBytes()
	}
	if minimum > options.EffectiveByteLimit {
		return "", nil, false, nil
	}

	rendered := make([]renderedSearchRecord, 0, returnedCount)
	remaining := options.EffectiveByteLimit - len(footer) - separatorBytes
	for index := 0; index < returnedCount; index++ {
		minimumRest := 0
		for rest := index + 1; rest < returnedCount; rest++ {
			minimumRest += options.Records[rest].minimumBytes()
		}
		maxCurrent := remaining - minimumRest
		record, ok := options.Records[index].render(maxCurrent)
		if !ok {
			return "", nil, false, nil
		}
		rendered = append(rendered, record)
		remaining -= len(record.Text)
	}

	parts := make([]string, 0, returnedCount+1)
	for _, record := range rendered {
		parts = append(parts, record.Text)
	}
	if footer != "" {
		parts = append(parts, footer)
	}
	output := strings.Join(parts, "\n")
	if len(output) > options.EffectiveByteLimit || !utf8.ValidString(output) {
		return "", nil, false, nil
	}
	return output, rendered, true, nil
}

func searchContinuationFooter(tool string, effectiveLimit int, stopReason, cursor string) string {
	if tool == "glob" {
		return fmt.Sprintf("[Truncated at limit=%d matches; stop=%s; next_cursor=%s; narrow pattern.]", effectiveLimit, stopReason, cursor)
	}
	return fmt.Sprintf("[More matches; stop=%s; next_cursor=%s; narrow path, include, or pattern.]", stopReason, cursor)
}

func baseSearchPageMetadata(options searchPageOptions, returnedCount int, hasMore bool, stopReason string, countReached, byteReached bool, outputBytes int) map[string]any {
	metadata := map[string]any{
		"returned_count":          returnedCount,
		"requested_limit":         options.RequestedLimit,
		"effective_limit":         options.EffectiveLimit,
		"has_more":                hasMore,
		"limit_capped":            options.RequestedLimit > options.EffectiveLimit,
		"truncated_snippet_count": 0,
		"requested_byte_limit":    options.RequestedByteLimit,
		"effective_byte_limit":    options.EffectiveByteLimit,
		"byte_limit_capped":       options.ByteLimitCapped,
		"output_bytes":            outputBytes,
		"stop_reason":             stopReason,
		"match_limit_reached":     countReached,
		"byte_limit_reached":      byteReached,
		"cursor_version":          searchCursorVersion,
		"snapshot_semantics":      "current_view",
	}
	if hasMore {
		last := options.Records[returnedCount-1]
		cursor, err := encodeSearchCursor(options.Query, options.StartIndex+returnedCount, last.Path, last.Line)
		if err == nil {
			metadata["next_cursor"] = cursor
		}
	}
	return metadata
}

func searchCursorFailureResult(tool string, err error) session.ToolResult {
	code := "invalid"
	var cursorErr *searchCursorError
	if errors.As(err, &cursorErr) && strings.TrimSpace(cursorErr.Code) != "" {
		code = cursorErr.Code
	}
	result := typedToolErrorResult(tool, FailureClassInvalidCursor, FailureClassInvalidCursor, err.Error())
	result.Metadata["cursor_error"] = code
	result.Metadata["cursor_version"] = searchCursorVersion
	return result
}

func searchPageFailureResult(tool string, budget searchOutputBudget, err error) session.ToolResult {
	result := typedToolErrorResult(tool, FailureClassSearchRecordTooLarge, FailureClassSearchRecordTooLarge, err.Error())
	result.Metadata["requested_byte_limit"] = budget.Requested
	result.Metadata["effective_byte_limit"] = budget.Effective
	result.Metadata["byte_limit_capped"] = budget.Capped
	result.Metadata["cursor_version"] = searchCursorVersion
	result.Metadata["snapshot_semantics"] = "current_view"
	return result
}
