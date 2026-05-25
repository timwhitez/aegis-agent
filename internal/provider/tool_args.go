package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
)

func normalizeToolCallArguments(providerName, toolName string, raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, toolCallArgumentsError(providerName, toolName, "are empty")
	}
	if !json.Valid(trimmed) {
		return nil, toolCallArgumentsError(providerName, toolName, "are not valid JSON")
	}
	var value any
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return nil, toolCallArgumentsError(providerName, toolName, "are not valid JSON")
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, toolCallArgumentsError(providerName, toolName, "must be a JSON object")
	}
	return json.RawMessage(trimmed), nil
}

func toolCallArgumentsError(providerName, toolName, detail string) error {
	return &HTTPError{
		Provider: providerName,
		Class:    "response_parse_error",
		Message:  fmt.Sprintf("tool-call arguments for %q %s", toolName, detail),
	}
}
