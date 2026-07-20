package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"go-cli-agent/internal/config"
)

const canonicalReadOnlyToolArgumentsVersion = 1

const (
	readFileModeLine = "line"
	readFileModeByte = "byte"
)

type readFileToolArguments struct {
	Path string `json:"path"`

	Offset     *int   `json:"offset"`
	Limit      *int   `json:"limit"`
	ByteOffset *int64 `json:"byte_offset"`
	ByteLimit  *int64 `json:"byte_limit"`

	hasOffset     bool
	hasLimit      bool
	hasByteOffset bool
	hasByteLimit  bool
}

type normalizedReadFileToolArguments struct {
	Path string
	Mode string

	RequestedLineOffset int
	EffectiveLineOffset int
	RequestedLineLimit  int
	EffectiveLineLimit  int
	LineLimitCapped     bool

	RequestedByteOffset int64
	RequestedByteLimit  int64
	EffectiveByteLimit  int64
}

type searchToolArguments struct {
	Pattern   string `json:"pattern"`
	Path      string `json:"path"`
	Include   string `json:"include"`
	Limit     *int   `json:"limit"`
	ByteLimit *int   `json:"byte_limit"`
	Cursor    string `json:"cursor"`

	hasLimit     bool
	hasByteLimit bool
}

type normalizedSearchToolArguments struct {
	Pattern string
	Path    string
	Include string
	Cursor  string

	RequestedLimit int
	EffectiveLimit int
	OutputBudget   searchOutputBudget
}

// CanonicalReadOnlyToolArguments returns a stable JSON representation of the
// effective arguments used by the allowlisted read-only tools. The same typed
// decoders and normalizers are used by the tool execution handlers below. A
// non-allowlisted tool returns eligible=false without interpreting its input.
func CanonicalReadOnlyToolArguments(toolName string, raw json.RawMessage, cfg *config.Config) ([]byte, bool, error) {
	switch toolName {
	case "read_file":
		input, err := decodeReadFileToolArguments(raw)
		if err != nil {
			return nil, true, err
		}
		normalized, err := normalizeReadFileToolArguments(input, cfg)
		if err != nil {
			return nil, true, err
		}
		if normalized.Mode == readFileModeByte {
			canonical := struct {
				Version    int    `json:"version"`
				Tool       string `json:"tool"`
				Path       string `json:"path"`
				Mode       string `json:"mode"`
				ByteOffset int64  `json:"byte_offset"`
				ByteLimit  int64  `json:"byte_limit"`
			}{
				Version:    canonicalReadOnlyToolArgumentsVersion,
				Tool:       toolName,
				Path:       canonicalToolPath(normalized.Path, false),
				Mode:       normalized.Mode,
				ByteOffset: normalized.RequestedByteOffset,
				ByteLimit:  normalized.EffectiveByteLimit,
			}
			data, err := json.Marshal(canonical)
			return data, true, err
		}
		canonical := struct {
			Version int    `json:"version"`
			Tool    string `json:"tool"`
			Path    string `json:"path"`
			Mode    string `json:"mode"`
			Offset  int    `json:"offset"`
			Limit   int    `json:"limit"`
		}{
			Version: canonicalReadOnlyToolArgumentsVersion,
			Tool:    toolName,
			Path:    canonicalToolPath(normalized.Path, false),
			Mode:    normalized.Mode,
			Offset:  normalized.EffectiveLineOffset,
			Limit:   normalized.EffectiveLineLimit,
		}
		data, err := json.Marshal(canonical)
		return data, true, err

	case "grep", "grep_files", "glob":
		input, err := decodeSearchToolArguments(raw)
		if err != nil {
			return nil, true, err
		}
		if err := validateGrepPattern(input.Pattern); err != nil {
			return nil, true, err
		}
		normalized, err := normalizeSearchToolArguments(toolName, input, cfg)
		if err != nil {
			return nil, true, err
		}
		canonical := struct {
			Version   int    `json:"version"`
			Tool      string `json:"tool"`
			Pattern   string `json:"pattern"`
			Path      string `json:"path"`
			Include   string `json:"include"`
			Limit     int    `json:"limit"`
			ByteLimit int    `json:"byte_limit"`
			Cursor    string `json:"cursor"`
		}{
			Version:   canonicalReadOnlyToolArgumentsVersion,
			Tool:      toolName,
			Pattern:   normalized.Pattern,
			Path:      canonicalToolPath(normalized.Path, true),
			Include:   normalized.Include,
			Limit:     normalized.EffectiveLimit,
			ByteLimit: normalized.OutputBudget.Effective,
			Cursor:    normalized.Cursor,
		}
		data, err := json.Marshal(canonical)
		return data, true, err
	default:
		return nil, false, nil
	}
}

func decodeReadFileToolArguments(raw json.RawMessage) (readFileToolArguments, error) {
	var input readFileToolArguments
	fields, err := decodeStrictToolArgumentObject(raw, &input)
	if err != nil {
		return readFileToolArguments{}, err
	}
	input.hasOffset = fields["offset"] != nil
	input.hasLimit = fields["limit"] != nil
	input.hasByteOffset = fields["byte_offset"] != nil
	input.hasByteLimit = fields["byte_limit"] != nil
	for field, present := range map[string]bool{
		"offset":      input.hasOffset && input.Offset == nil,
		"limit":       input.hasLimit && input.Limit == nil,
		"byte_offset": input.hasByteOffset && input.ByteOffset == nil,
		"byte_limit":  input.hasByteLimit && input.ByteLimit == nil,
	} {
		if present {
			return readFileToolArguments{}, fmt.Errorf("%s must be an integer", field)
		}
	}
	return input, nil
}

func normalizeReadFileToolArguments(input readFileToolArguments, cfg *config.Config) (normalizedReadFileToolArguments, error) {
	if err := validateToolPath(input.Path); err != nil {
		return normalizedReadFileToolArguments{}, err
	}
	hasLineFields := input.hasOffset || input.hasLimit
	hasByteFields := input.hasByteOffset || input.hasByteLimit
	if hasLineFields && hasByteFields {
		return normalizedReadFileToolArguments{}, errors.New("line offset/limit and byte_offset/byte_limit are mutually exclusive")
	}
	if hasByteFields {
		if input.ByteLimit == nil {
			return normalizedReadFileToolArguments{}, errors.New("byte mode requires integer byte_limit")
		}
		if *input.ByteLimit <= 0 {
			return normalizedReadFileToolArguments{}, errors.New("byte_limit must be positive")
		}
		requestedOffset := int64(0)
		if input.ByteOffset != nil {
			requestedOffset = *input.ByteOffset
		}
		if requestedOffset < 0 {
			return normalizedReadFileToolArguments{}, errors.New("byte_offset must be non-negative")
		}
		effectiveLimit := normalizeReadFileByteLimit(cfg, *input.ByteLimit)
		if effectiveLimit <= 0 {
			return normalizedReadFileToolArguments{}, errors.New("effective byte window is too small to return UTF-8 text")
		}
		return normalizedReadFileToolArguments{
			Path:                input.Path,
			Mode:                readFileModeByte,
			RequestedByteOffset: requestedOffset,
			RequestedByteLimit:  *input.ByteLimit,
			EffectiveByteLimit:  effectiveLimit,
		}, nil
	}

	requestedOffset := 0
	if input.Offset != nil {
		requestedOffset = *input.Offset
	}
	effectiveOffset := requestedOffset
	if effectiveOffset < 1 {
		effectiveOffset = 1
	}
	requestedLimit := 0
	if input.Limit != nil {
		requestedLimit = *input.Limit
	}
	effectiveLimit := requestedLimit
	if effectiveLimit <= 0 {
		effectiveLimit = readFileDefaultLimit
	}
	capped := effectiveLimit > readFileMaxLimit
	if capped {
		effectiveLimit = readFileMaxLimit
	}
	return normalizedReadFileToolArguments{
		Path:                input.Path,
		Mode:                readFileModeLine,
		RequestedLineOffset: requestedOffset,
		EffectiveLineOffset: effectiveOffset,
		RequestedLineLimit:  requestedLimit,
		EffectiveLineLimit:  effectiveLimit,
		LineLimitCapped:     capped,
	}, nil
}

func normalizeReadFileByteLimit(cfg *config.Config, requested int64) int64 {
	effective := requested
	if effective > readFileMaxByteLimit {
		effective = readFileMaxByteLimit
	}
	llmLimit := int64(toolOutputLLMMaxBytes(cfg))
	if effective > llmLimit {
		effective = llmLimit
	}
	return effective
}

func decodeSearchToolArguments(raw json.RawMessage) (searchToolArguments, error) {
	var input searchToolArguments
	fields, err := decodeStrictToolArgumentObject(raw, &input)
	if err != nil {
		return searchToolArguments{}, err
	}
	input.hasLimit = fields["limit"] != nil
	input.hasByteLimit = fields["byte_limit"] != nil
	if input.hasLimit && input.Limit == nil {
		return searchToolArguments{}, errors.New("limit must be an integer")
	}
	if input.hasByteLimit && input.ByteLimit == nil {
		return searchToolArguments{}, errors.New("byte_limit must be an integer")
	}
	return input, nil
}

func normalizeSearchToolArguments(toolName string, input searchToolArguments, cfg *config.Config) (normalizedSearchToolArguments, error) {
	requestedLimit := 0
	if input.Limit != nil {
		requestedLimit = *input.Limit
	}
	effectiveLimit := 0
	switch toolName {
	case "grep":
		effectiveLimit = normalizeGrepMatchesLimit(requestedLimit)
	case "grep_files", "glob":
		effectiveLimit = normalizeGrepFilesLimit(requestedLimit)
	default:
		return normalizedSearchToolArguments{}, fmt.Errorf("tool %q does not have read-only search arguments", toolName)
	}
	outputBudget, err := normalizeSearchOutputByteLimit(cfg, input.ByteLimit)
	if err != nil {
		return normalizedSearchToolArguments{}, err
	}
	return normalizedSearchToolArguments{
		Pattern:        input.Pattern,
		Path:           input.Path,
		Include:        input.Include,
		Cursor:         input.Cursor,
		RequestedLimit: requestedLimit,
		EffectiveLimit: effectiveLimit,
		OutputBudget:   outputBudget,
	}, nil
}

func canonicalToolPath(path string, preserveEmpty bool) string {
	if preserveEmpty && path == "" {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(path))
}

func decodeStrictToolArgumentObject(raw json.RawMessage, target any) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		trimmed = []byte("{}")
	}
	if trimmed[0] != '{' {
		return nil, errors.New("tool input must be a JSON object")
	}
	var fields map[string]json.RawMessage
	if err := decodeSingleJSONValue(trimmed, &fields, false); err != nil {
		return nil, err
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	if err := decodeSingleJSONValue(trimmed, target, true); err != nil {
		return nil, err
	}
	return fields, nil
}

func decodeSingleJSONValue(raw []byte, target any, disallowUnknown bool) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if disallowUnknown {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("tool input must contain a single JSON value")
		}
		return err
	}
	return nil
}
