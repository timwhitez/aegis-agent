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
	for _, item := range summary.TopReadPaths {
		if item.Count >= 6 {
			triggers = append(triggers, fmt.Sprintf("read_file(%s) repeated %d times", item.Key, item.Count))
			break
		}
	}
	if cyclic := recentCyclicReadOnlyLoopCount(messages); cyclic >= 6 {
		triggers = append(triggers, fmt.Sprintf("cyclic read-only tool loop detected (%d repeated calls without progress)", cyclic))
	}
	if len(triggers) == 0 {
		return ""
	}
	return "Harness reminder: repeated context-discovery/tool patterns were observed (" + strings.Join(triggers, "; ") + "). Treat this as an observation, not a fixed workflow: reuse already loaded skills and previously read evidence, then either write/update the requested artifact or progress/validation/final notes, or clearly state the blocker before continuing."
}

func recentCyclicReadOnlyLoopCount(messages []session.Message) int {
	// Scan recent tool results since the last mutating action (write_file, edit_file,
	// finish, submit_plan, or a new user instruction). Count how many read-only calls
	// repeated an exact (name, path/pattern/command) signature within that window.
	seen := map[string]int{}
	repeated := 0
	inspectedTurns := 0
	for i := len(messages) - 1; i >= 0 && inspectedTurns < 10; i-- {
		msg := messages[i]
		if msg.Role == "user" {
			source, _ := msg.Meta["source"].(string)
			if source != "harness_reminder" && source != "compaction_summary" {
				break
			}
			continue
		}
		if msg.Role != "tool" {
			continue
		}
		inspectedTurns++
		for _, res := range msg.ToolResults {
			switch res.Name {
			case "write_file", "edit_file", "finish", "submit_plan":
				return repeated
			case "read_file", "grep", "grep_files", "glob", "load_skill":
				sig := readOnlyRepetitionSignature(res)
				if sig == "" {
					continue
				}
				seen[sig]++
				if seen[sig] >= 2 {
					repeated++
				}
			}
		}
	}
	return repeated
}

func readOnlyRepetitionSignature(result session.ToolResult) string {
	switch result.Name {
	case "read_file":
		path := strings.TrimSpace(metadataString(result.Metadata, "path"))
		if path == "" {
			return ""
		}
		if offset, ok := result.Metadata["offset"]; ok {
			return fmt.Sprintf("read_file:%s:%d-%d", path, repetitionMetadataInt(map[string]any{"offset": offset}, "offset"), repetitionMetadataInt(result.Metadata, "end"))
		}
		if byteOffset, ok := result.Metadata["byte_offset"]; ok {
			return fmt.Sprintf("read_file:%s:b%d-%d", path, repetitionMetadataInt(map[string]any{"byte_offset": byteOffset}, "byte_offset"), repetitionMetadataInt(result.Metadata, "byte_end"))
		}
		return "read_file:" + path
	case "grep", "grep_files", "glob":
		return fmt.Sprintf("%s:%s:%s:%s", result.Name, metadataString(result.Metadata, "path"), metadataString(result.Metadata, "pattern"), metadataString(result.Metadata, "include"))
	case "load_skill":
		return "load_skill:" + strings.TrimSpace(metadataString(result.Metadata, "path"))
	default:
		return ""
	}
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
