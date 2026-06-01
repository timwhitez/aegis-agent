package streamjson

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type inputEnvelope struct {
	Type    string              `json:"type"`
	Message inputContentMessage `json:"message"`
}

type inputContentMessage struct {
	Role    string              `json:"role"`
	Content []inputContentBlock `json:"content"`
}

type inputContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func ReadInitialPrompt(r io.Reader, maxBytes int64) (string, error) {
	if maxBytes <= 0 {
		return "", fmt.Errorf("stream-json input max bytes must be positive")
	}
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > maxBytes {
		return "", fmt.Errorf("stream-json input exceeds maximum size: %d bytes", maxBytes)
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", fmt.Errorf("stream-json input is empty")
	}
	var envelope inputEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return "", fmt.Errorf("invalid stream-json input: %w", err)
	}
	if envelope.Type != "user" {
		return "", fmt.Errorf("stream-json input type must be user")
	}
	if envelope.Message.Role != "user" {
		return "", fmt.Errorf("stream-json message role must be user")
	}
	parts := make([]string, 0, len(envelope.Message.Content))
	for _, block := range envelope.Message.Content {
		if block.Type != "text" {
			return "", fmt.Errorf("stream-json input only supports text content blocks")
		}
		parts = append(parts, block.Text)
	}
	prompt := strings.TrimSpace(strings.Join(parts, "\n"))
	if prompt == "" {
		return "", fmt.Errorf("stream-json user prompt is empty")
	}
	return prompt, nil
}
