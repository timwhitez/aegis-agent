package runtime

import (
	"fmt"
	"sort"
	"strings"

	"aegis-agent/internal/session"
)

type toolRepetitionSummary struct {
	TopTools      []toolCount `json:"top_tools,omitempty"`
	TopReadPaths  []toolCount `json:"top_read_paths,omitempty"`
	TodoNoopCount int         `json:"todo_noop_count,omitempty"`
}

type toolCount struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

func summarizeToolRepetition(messages []session.Message) toolRepetitionSummary {
	toolCounts := map[string]int{}
	readPathCounts := map[string]int{}
	todoNoops := 0
	for _, msg := range messages {
		for _, result := range msg.ToolResults {
			toolCounts[result.Name]++
			if result.Name == "read_file" {
				key := strings.TrimSpace(metadataString(result.Metadata, "path"))
				if offset, ok := result.Metadata["offset"]; ok {
					key = fmt.Sprintf("%s:%d-%d", key, repetitionMetadataInt(map[string]any{"offset": offset}, "offset"), repetitionMetadataInt(result.Metadata, "end"))
				}
				if key != "" {
					readPathCounts[key]++
				}
			}
			if result.Name == "todo_write" && metadataBool(result.Metadata, "noop") {
				todoNoops++
			}
		}
	}
	return toolRepetitionSummary{
		TopTools:      topCounts(toolCounts, 5),
		TopReadPaths:  topCounts(readPathCounts, 5),
		TodoNoopCount: todoNoops,
	}
}

func toolLoopReminderText(messages []session.Message) string {
	if recentToolLoopReminder(messages) {
		return ""
	}
	summary := summarizeToolRepetition(messages)
	var triggers []string
	for _, item := range summary.TopTools {
		if item.Count >= 4 && (item.Key == "load_skill" || item.Key == "todo_write") {
			triggers = append(triggers, fmt.Sprintf("%s repeated %d times", item.Key, item.Count))
		}
	}
	if summary.TodoNoopCount >= 4 {
		triggers = append(triggers, fmt.Sprintf("todo_write no-op repeated %d times", summary.TodoNoopCount))
	}
	if len(triggers) == 0 {
		return ""
	}
	return "Harness reminder: repeated context-discovery/tool patterns were observed (" + strings.Join(triggers, "; ") + "). Treat this as an observation, not a fixed workflow: reuse already loaded skills and previously read evidence, then either write/update the requested artifact or progress/validation/final notes, or clearly state the blocker before continuing."
}

func recentToolLoopReminder(messages []session.Message) bool {
	start := len(messages) - 8
	if start < 0 {
		start = 0
	}
	for _, msg := range messages[start:] {
		if msg.Meta == nil {
			continue
		}
		if source, _ := msg.Meta["source"].(string); source != "harness_reminder" {
			continue
		}
		if kind, _ := msg.Meta["kind"].(string); kind == "tool_loop_observation" {
			return true
		}
	}
	return false
}

func topCounts(counts map[string]int, limit int) []toolCount {
	items := make([]toolCount, 0, len(counts))
	for key, count := range counts {
		if count <= 1 || strings.TrimSpace(key) == "" {
			continue
		}
		items = append(items, toolCount{Key: key, Count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count == items[j].Count {
			return items[i].Key < items[j].Key
		}
		return items[i].Count > items[j].Count
	})
	if limit > 0 && len(items) > limit {
		return items[:limit]
	}
	return items
}

func metadataString(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return value
}

func metadataBool(metadata map[string]any, key string) bool {
	value, _ := metadata[key].(bool)
	return value
}

func repetitionMetadataInt(metadata map[string]any, key string) int {
	switch value := metadata[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}
