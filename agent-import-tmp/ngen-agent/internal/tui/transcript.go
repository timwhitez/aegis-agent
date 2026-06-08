package tui

import (
	"fmt"
	"sort"
	"strings"

	"ngen/internal/task"
)

type transcriptEntry struct {
	Key     string
	TS      string
	Kind    string
	Title   string
	Summary string
	Refs    []string
}

func buildTranscript(messages []task.SessionMessage, events []task.Event) []transcriptEntry {
	entries := make([]transcriptEntry, 0, len(messages)+len(events))
	seenMessages := make(map[string]struct{}, len(messages))
	for _, message := range messages {
		if key, ok := transcriptDedupeKey("msg", message.TS, message.MessageID); ok {
			if _, exists := seenMessages[key]; exists {
				continue
			}
			seenMessages[key] = struct{}{}
		}
		title := "runtime"
		if strings.TrimSpace(message.Role) != "" {
			title = message.Role
		}
		entries = append(entries, transcriptEntry{
			Key:     "msg:" + message.MessageID,
			TS:      message.TS,
			Kind:    "message",
			Title:   title,
			Summary: strings.TrimSpace(message.Content),
		})
	}
	seenEvents := make(map[string]struct{}, len(events))
	for _, event := range events {
		if key, ok := transcriptDedupeKey("evt", event.TS, event.EventID); ok {
			if _, exists := seenEvents[key]; exists {
				continue
			}
			seenEvents[key] = struct{}{}
		}
		entries = append(entries, transcriptEntry{
			Key:     "evt:" + event.EventID,
			TS:      event.TS,
			Kind:    "event",
			Title:   event.Type,
			Summary: strings.TrimSpace(event.Summary),
			Refs:    append([]string(nil), event.Refs...),
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].TS == entries[j].TS {
			return entries[i].Key < entries[j].Key
		}
		return entries[i].TS < entries[j].TS
	})
	return entries
}

func transcriptDedupeKey(kind, ts, id string) (string, bool) {
	trimmedTS := strings.TrimSpace(ts)
	trimmedID := strings.TrimSpace(id)
	if trimmedTS == "" || trimmedID == "" {
		return "", false
	}
	return kind + ":" + trimmedTS + ":" + trimmedID, true
}

func renderTranscript(entries []transcriptEntry, width int, running bool) string {
	if width <= 0 {
		width = 1
	}
	lines := make([]string, 0, len(entries)*3+2)
	for _, entry := range entries {
		label := shortTimestamp(entry.TS) + " " + strings.ToUpper(entry.Title)
		lines = append(lines, wrapPrefixedLine(width, "", label)...)
		if strings.TrimSpace(entry.Summary) != "" {
			lines = append(lines, wrapPrefixedLine(width, "  ", entry.Summary)...)
		}
		for _, ref := range entry.Refs {
			lines = append(lines, wrapPrefixedLine(width, "  ref: ", ref)...)
		}
		lines = append(lines, "")
	}
	if running {
		lines = append(lines, "Working...")
	}
	if len(lines) == 0 {
		return "No transcript yet."
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func shortTimestamp(ts string) string {
	if len(ts) >= 19 {
		return ts[11:19]
	}
	return ts
}

func wrapPrefixedLine(width int, prefix, text string) []string {
	available := width - runeLen(prefix)
	if available < 1 {
		available = 1
	}
	words := strings.Fields(strings.TrimSpace(text))
	if len(words) == 0 {
		return []string{strings.TrimRight(prefix, " ")}
	}
	lines := make([]string, 0, 4)
	indent := strings.Repeat(" ", runeLen(prefix))
	current := ""
	flush := func() {
		if current == "" {
			return
		}
		linePrefix := prefix
		if len(lines) > 0 {
			linePrefix = indent
		}
		lines = append(lines, linePrefix+current)
		current = ""
	}
	for _, word := range words {
		wordLen := runeLen(word)
		if wordLen > available {
			flush()
			for _, chunk := range splitRuneChunks(word, available) {
				linePrefix := prefix
				if len(lines) > 0 {
					linePrefix = indent
				}
				lines = append(lines, linePrefix+chunk)
			}
			continue
		}
		if current == "" {
			current = word
			continue
		}
		if runeLen(current)+1+wordLen > available {
			flush()
			current = word
			continue
		}
		current += " " + word
	}
	flush()
	return lines
}

func renderMemoryPreview(memory string, width int) string {
	memory = strings.TrimSpace(memory)
	if memory == "" {
		return "No workspace memory entries yet."
	}
	if width <= 0 {
		width = 1
	}
	var lines []string
	for _, raw := range strings.Split(memory, "\n") {
		line := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(line) == "" {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, wrapPrefixedLine(width, "", line)...)
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func renderCriteriaSummary(criteria task.CriteriaSnapshot, width int) string {
	if len(criteria.Criteria) == 0 {
		return "No criteria snapshot yet."
	}
	metadata := []string{
		fmt.Sprintf("Summary: %s", blankDash(criteria.Summary)),
		fmt.Sprintf("Met/Open: %d/%d", criteria.MetCount, criteria.OpenCount),
	}
	if criteria.CurrentCriterionID != "" || criteria.CurrentCriterionStatement != "" {
		metadata = append(metadata, fmt.Sprintf("Current: %s %s", blankDash(criteria.CurrentCriterionID), blankDash(criteria.CurrentCriterionStatement)))
	}
	var lines []string
	for _, line := range metadata {
		lines = append(lines, wrapPrefixedLine(width, "", line)...)
	}
	lines = append(lines, "")
	for _, item := range criteria.Criteria {
		status := "open"
		if item.Passes {
			status = "met"
		}
		lines = append(lines, wrapPrefixedLine(width, "- ", fmt.Sprintf("[%s] %s", status, blankDash(item.Statement)))...)
		if item.LastSummary != "" {
			lines = append(lines, wrapPrefixedLine(width, "  ", item.LastSummary)...)
		}
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func blankDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return strings.TrimSpace(value)
}

func splitRuneChunks(text string, width int) []string {
	if width <= 0 {
		width = 1
	}
	runes := []rune(text)
	if len(runes) == 0 {
		return []string{""}
	}
	chunks := make([]string, 0, (len(runes)+width-1)/width)
	for len(runes) > 0 {
		n := width
		if n > len(runes) {
			n = len(runes)
		}
		chunks = append(chunks, string(runes[:n]))
		runes = runes[n:]
	}
	return chunks
}

func runeLen(text string) int {
	return len([]rune(text))
}
