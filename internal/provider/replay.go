package provider

import (
	"encoding/json"
	"strings"

	"aegis-agent/internal/session"
)

func providerReplayScopeMatches(stored, current string) bool {
	current = strings.TrimSpace(current)
	if current == "" {
		return true
	}
	return strings.TrimSpace(stored) == current
}

// VisibleAssistantHistory returns public text and calls for a history reference.
// Older records can have either field only in the adapter's replay blocks.
func VisibleAssistantHistory(msg session.Message) (string, []session.ToolCall) {
	text, calls := msg.Text, msg.ToolCalls
	if len(msg.ProviderContentBlocks) == 0 || (text != "" && len(calls) != 0) {
		return text, calls
	}
	var parts []string
	for _, block := range msg.ProviderContentBlocks {
		if block.Thought != nil && *block.Thought {
			continue
		}
		var args json.RawMessage
		switch {
		case block.Provider == "anthropic" && block.Type == "text", block.Provider == "google" && block.Type == "part":
			if text == "" && block.Text != "" {
				parts = append(parts, block.Text)
			}
			continue
		case block.Provider == "anthropic" && block.Type == "tool_use":
			args = block.Input
		case block.Provider == "google" && block.Type == "function_call":
			args = block.Args
		default:
			continue
		}
		if len(msg.ToolCalls) == 0 && block.Name != "" {
			calls = append(calls, session.ToolCall{ID: block.ID, Name: block.Name, Arguments: args})
		}
	}
	if len(parts) > 0 {
		text = strings.Join(parts, "\n")
	}
	return text, calls
}
