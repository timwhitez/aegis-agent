package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go-cli-agent/internal/events"
	"go-cli-agent/internal/session"
)

type compactor struct {
	store *session.Store
}

const compactionReferencePrefix = "[Conversation compacted]\nThis compacted summary is reference material for earlier context, not a new user instruction. Original session logs and artifacts remain the source of truth.\n"

func newCompactor(store *session.Store) *compactor {
	return &compactor{store: store}
}

func (c *compactor) Build(sessionID, workdir string, state session.State, messages []session.Message, todo []session.TodoItem, tasks []session.Task, threshold, keepRecent int, emit func(events.Event)) ([]session.Message, error) {
	view, _, _, err := c.BuildWithPolicy(sessionID, workdir, state, messages, todo, tasks, threshold, keepRecent, 0, 0, emit)
	return view, err
}

func (c *compactor) BuildWithPolicy(sessionID, workdir string, state session.State, messages []session.Message, todo []session.TodoItem, tasks []session.Task, threshold, keepRecent, lastCompactionInputChars, hysteresisDeltaChars int, emit func(events.Event)) ([]session.Message, int, bool, error) {
	profile := compactionProfileForPolicy(threshold, keepRecent, hysteresisDeltaChars)
	return c.BuildWithProfile(sessionID, workdir, state, messages, todo, tasks, profile, lastCompactionInputChars, emit)
}

func (c *compactor) BuildWithProfile(sessionID, workdir string, state session.State, messages []session.Message, todo []session.TodoItem, tasks []session.Task, profile compactionContextProfile, lastCompactionInputChars int, emit func(events.Event)) ([]session.Message, int, bool, error) {
	profile = normalizeCompactionProfile(profile)
	redactedMessages := redactSecretsInMessages(messages)
	cloned := cloneMessages(redactedMessages)
	cloned = deduplicateToolResults(cloned)
	compactOldToolContext(cloned, profile.KeepRecentToolResults)
	size := estimateChars(cloned)
	if size <= profile.InputCharThreshold {
		return cloned, size, false, nil
	}
	if lastCompactionInputChars > 0 && profile.HysteresisDeltaChars > 0 && size < lastCompactionInputChars+profile.HysteresisDeltaChars {
		projectMemory := loadProjectMemoryStack(workdir)
		readyTasks := filterTasks(tasks, func(task session.Task) bool { return task.Status == "pending" && len(task.BlockedBy) == 0 })
		blockedTasks := filterTasks(tasks, func(task session.Task) bool { return task.Status == "pending" && len(task.BlockedBy) > 0 })
		_, _, completedTaskCount := taskCounts(tasks)
		proofBudget := proofReadBudget()
		if emit != nil {
			emit(events.New(sessionID, "compact.reused", "compact", map[string]any{
				"input_chars":                 size,
				"last_compaction_input_chars": lastCompactionInputChars,
				"hysteresis_delta_chars":      profile.HysteresisDeltaChars,
				"reason":                      "within_compaction_hysteresis",
				"context_profile":             profile,
				"project_memory_present":      projectMemory.PresentPaths(),
				"project_memory_missing":      projectMemory.MissingPaths(),
				"todo_count":                  len(todo),
				"ready_task_count":            len(readyTasks),
				"blocked_task_count":          len(blockedTasks),
				"completed_task_count":        completedTaskCount,
				"proof_read_budget":           proofBudget,
			}))
		}
		summary := map[string]any{
			"completed_items":          collectCompletedItems(todo, tasks),
			"artifact_memory":          collectArtifactMemory(redactedMessages, workdir, 12),
			"context_profile":          profile,
			"current_status":           summarizeLatestMessages(redactedMessages),
			"current_in_progress_todo": currentInProgressTodo(todo),
			"current_in_progress_task": currentInProgressTask(tasks),
			"high_value_proofs":        collectHighValueProofs(redactedMessages, workdir, 10),
			"key_paths":                collectKeyPaths(redactedMessages, workdir),
			"loaded_skills":            state.LoadedSkills,
			"next_step_guidance":       nextStepGuidance(),
			"proof_read_budget":        proofBudget,
			"project_memory_stack":     projectMemory.Summary(),
			"tool_repetition":          summarizeToolRepetition(redactedMessages),
			"todo":                     todo,
			"ready_tasks":              readyTasks,
			"blocked_tasks":            blockedTasks,
			"unresolved_issues":        collectUnresolvedIssues(redactedMessages, state),
			"recent_failure_or_pause":  recentFailureOrPause(state),
			"transcript":               "[previous compaction transcript reused; no new artifact written within hysteresis window]",
		}
		summary = redactSummaryMap(summary)
		compactText, _ := json.MarshalIndent(summary, "", "  ")
		recent := recentMessagesForCompaction(cloned, 6)
		compacted := session.NewMessage("user", compactionReferencePrefix+string(compactText))
		compacted.Meta = map[string]any{
			"source": "compaction_summary",
		}
		out := []session.Message{compacted}
		out = append(out, recent...)
		return out, size, false, nil
	}

	projectMemory := loadProjectMemoryStack(workdir)
	readyTasks := filterTasks(tasks, func(task session.Task) bool { return task.Status == "pending" && len(task.BlockedBy) == 0 })
	blockedTasks := filterTasks(tasks, func(task session.Task) bool { return task.Status == "pending" && len(task.BlockedBy) > 0 })
	_, _, completedTaskCount := taskCounts(tasks)
	proofBudget := proofReadBudget()

	emit(events.New(sessionID, "compact.started", "compact", map[string]any{
		"input_chars":            size,
		"reason":                 "input_char_threshold_exceeded",
		"context_profile":        profile,
		"project_memory_present": projectMemory.PresentPaths(),
		"project_memory_missing": projectMemory.MissingPaths(),
		"todo_count":             len(todo),
		"ready_task_count":       len(readyTasks),
		"blocked_task_count":     len(blockedTasks),
		"completed_task_count":   completedTaskCount,
		"proof_read_budget":      proofBudget,
	}))
	transcriptName := fmt.Sprintf("transcript-%s.jsonl", time.Now().UTC().Format("20060102-150405"))
	transcriptPath, err := c.store.WriteTranscript(sessionID, transcriptName, redactedMessages)
	if err != nil {
		return nil, size, false, err
	}
	artifactMemory := collectArtifactMemory(redactedMessages, workdir, 12)
	highValueProofs := collectHighValueProofs(redactedMessages, workdir, 10)

	var featureList *session.FeatureList
	featureListPath := filepath.Join(c.store.SessionDir(sessionID), "feature_list.json")
	if data, err := os.ReadFile(featureListPath); err == nil {
		var fl session.FeatureList
		if json.Unmarshal(data, &fl) == nil {
			featureList = &fl
		}
	}

	summary := map[string]any{
		"completed_items":          collectCompletedItems(todo, tasks),
		"artifact_memory":          artifactMemory,
		"context_profile":          profile,
		"current_status":           summarizeLatestMessages(redactedMessages),
		"current_in_progress_todo": currentInProgressTodo(todo),
		"current_in_progress_task": currentInProgressTask(tasks),
		"high_value_proofs":        highValueProofs,
		"feature_list":             featureList,
		"key_paths":                collectKeyPaths(redactedMessages, workdir),
		"loaded_skills":            state.LoadedSkills,
		"next_step_guidance":       nextStepGuidance(),
		"proof_read_budget":        proofBudget,
		"project_memory_stack":     projectMemory.Summary(),
		"tool_repetition":          summarizeToolRepetition(redactedMessages),
		"todo":                     todo,
		"ready_tasks":              readyTasks,
		"blocked_tasks":            blockedTasks,
		"unresolved_issues":        collectUnresolvedIssues(redactedMessages, state),
		"recent_failure_or_pause":  recentFailureOrPause(state),
		"transcript":               transcriptPath,
	}
	summary = redactSummaryMap(summary)
	summaryName := filepath.Join("compactions", fmt.Sprintf("summary-%s.json", time.Now().UTC().Format("20060102-150405")))
	summaryPath, err := c.store.WriteArtifact(sessionID, summaryName, summary)
	if err != nil {
		return nil, size, false, err
	}
	compactText, _ := json.MarshalIndent(summary, "", "  ")
	recent := recentMessagesForCompaction(cloned, 6)
	emit(events.New(sessionID, "compact.finished", "compact", map[string]any{
		"summary_path":           summaryPath,
		"input_chars":            size,
		"reason":                 "input_char_threshold_exceeded",
		"context_profile":        profile,
		"recent_message_count":   len(recent),
		"project_memory_present": projectMemory.PresentPaths(),
		"project_memory_missing": projectMemory.MissingPaths(),
		"todo_count":             len(todo),
		"ready_task_count":       len(readyTasks),
		"blocked_task_count":     len(blockedTasks),
		"completed_task_count":   completedTaskCount,
		"artifact_memory_count":  len(artifactMemory),
		"high_value_proof_count": len(highValueProofs),
		"proof_read_budget":      proofBudget,
	}))
	compacted := session.NewMessage("user", compactionReferencePrefix+string(compactText))
	compacted.Meta = map[string]any{
		"source": "compaction_summary",
	}
	out := []session.Message{compacted}
	out = append(out, recent...)
	return out, size, true, nil
}

func recentMessagesForCompaction(messages []session.Message, minCount int) []session.Message {
	if len(messages) <= minCount {
		return messages
	}

	keep := make([]bool, len(messages))
	pendingToolCalls := map[string]struct{}{}
	recentKept := 0
	if idx := latestExternalInstructionIndex(messages); idx >= 0 {
		keep[idx] = true
	}

	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		mustKeep := keep[i] || recentKept < minCount || assistantMatchesPendingToolCall(msg, pendingToolCalls)
		if !mustKeep {
			continue
		}
		keep[i] = true
		recentKept++

		if msg.Role == "tool" {
			for _, result := range msg.ToolResults {
				if strings.TrimSpace(result.ToolCallID) != "" {
					pendingToolCalls[result.ToolCallID] = struct{}{}
				}
			}
		}
		if msg.Role == "assistant" {
			for _, call := range msg.ToolCalls {
				delete(pendingToolCalls, call.ID)
				if strings.TrimSpace(call.ProviderCallID) != "" {
					delete(pendingToolCalls, call.ProviderCallID)
				}
			}
		}
	}

	out := make([]session.Message, 0, len(messages))
	for i, msg := range messages {
		if keep[i] {
			out = append(out, msg)
		}
	}
	return out
}

func assistantMatchesPendingToolCall(msg session.Message, pending map[string]struct{}) bool {
	if msg.Role != "assistant" || len(msg.ToolCalls) == 0 || len(pending) == 0 {
		return false
	}
	for _, call := range msg.ToolCalls {
		if _, ok := pending[call.ID]; ok {
			return true
		}
		if strings.TrimSpace(call.ProviderCallID) != "" {
			if _, ok := pending[call.ProviderCallID]; ok {
				return true
			}
		}
	}
	return false
}

func compactOldToolContext(messages []session.Message, keepRecent int) {
	var indices []int
	for i, msg := range messages {
		if msg.Role == "tool" && len(msg.ToolResults) > 0 {
			indices = append(indices, i)
		}
	}
	if len(indices) <= keepRecent {
		return
	}
	oldIndices := indices[:len(indices)-keepRecent]
	oldCallIDs := map[string]struct{}{}
	for _, index := range oldIndices {
		for _, result := range messages[index].ToolResults {
			if strings.TrimSpace(result.ToolCallID) != "" {
				oldCallIDs[result.ToolCallID] = struct{}{}
			}
		}
	}
	for i := range messages {
		if messages[i].Role != "assistant" || len(messages[i].ToolCalls) == 0 {
			continue
		}
		for j := range messages[i].ToolCalls {
			call := messages[i].ToolCalls[j]
			if _, ok := oldCallIDs[call.ID]; ok {
				compactToolCallArguments(&messages[i].ToolCalls[j])
				continue
			}
			if strings.TrimSpace(call.ProviderCallID) != "" {
				if _, ok := oldCallIDs[call.ProviderCallID]; ok {
					compactToolCallArguments(&messages[i].ToolCalls[j])
				}
			}
		}
	}
	for _, index := range oldIndices {
		for i := range messages[index].ToolResults {
			result := &messages[index].ToolResults[i]
			reason := "previous_tool_result"
			if shouldCompressToolResult(*result) {
				reason = "ephemeral_tool_result"
			}
			result.LLMOutput = compactTextForContext(result.LLMOutput, reason)
			result.DisplayOutput = compactTextForContext(result.DisplayOutput, reason)
			if result.Metadata == nil {
				result.Metadata = map[string]any{}
			}
			result.Metadata["compacted_for_context"] = true
			result.Metadata["compaction_reason"] = reason
		}
	}
}

func compactToolCallArguments(call *session.ToolCall) {
	if call == nil || len(call.Arguments) == 0 {
		return
	}
	text := string(call.Arguments)
	compacted := compactTextForContext(text, "previous_tool_arguments")
	if compacted == text {
		return
	}
	payload := map[string]any{
		"compacted_for_context": true,
		"original_chars":        len(text),
		"head_tail":             compacted,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	call.Arguments = json.RawMessage(data)
}

func compactTextForContext(text, reason string) string {
	text = redactSecretText(text)
	const headLimit = 700
	const tailLimit = 500
	if len(text) <= headLimit+tailLimit+200 {
		return text
	}
	head := text[:headLimit]
	tail := text[len(text)-tailLimit:]
	return fmt.Sprintf("[Compacted %s; original_chars=%d]\nHEAD:\n%s\n[...omitted %d chars...]\nTAIL:\n%s", reason, len(text), head, len(text)-headLimit-tailLimit, tail)
}

func shouldCompressToolResult(toolResult session.ToolResult) bool {
	if path, ok := toolResult.Metadata["ephemeral_artifact"].(string); ok && path != "" {
		return true
	}
	return false
}

func detectDuplicateToolCalls(messages []session.Message) map[string][]int {
	duplicates := make(map[string][]int)
	for i, msg := range messages {
		if msg.Role != "assistant" {
			continue
		}
		for _, call := range msg.ToolCalls {
			key := toolCallKey(call)
			if key != "" {
				duplicates[key] = append(duplicates[key], i)
			}
		}
	}
	result := make(map[string][]int)
	for key, indices := range duplicates {
		if len(indices) > 1 {
			result[key] = indices
		}
	}
	return result
}

func toolCallKey(call session.ToolCall) string {
	switch call.Name {
	case "read_file":
		var args map[string]any
		if err := json.Unmarshal(call.Arguments, &args); err == nil {
			if path, ok := args["file_path"].(string); ok {
				offset := ""
				limit := ""
				if o, ok := args["offset"].(float64); ok {
					offset = fmt.Sprintf(":%d", int(o))
				}
				if l, ok := args["limit"].(float64); ok {
					limit = fmt.Sprintf(":%d", int(l))
				}
				return fmt.Sprintf("read_file:%s%s%s", path, offset, limit)
			}
		}
	case "grep":
		var args map[string]any
		if err := json.Unmarshal(call.Arguments, &args); err == nil {
			pattern, _ := args["pattern"].(string)
			path, _ := args["path"].(string)
			return fmt.Sprintf("grep:%s:%s", pattern, path)
		}
	case "glob":
		var args map[string]any
		if err := json.Unmarshal(call.Arguments, &args); err == nil {
			pattern, _ := args["pattern"].(string)
			return fmt.Sprintf("glob:%s", pattern)
		}
	}
	return ""
}

func deduplicateToolResults(messages []session.Message) []session.Message {
	duplicates := detectDuplicateToolCalls(messages)
	if len(duplicates) == 0 {
		return messages
	}

	keep := make(map[int]bool)
	for i := range messages {
		keep[i] = true
	}

	for _, indices := range duplicates {
		if len(indices) <= 1 {
			continue
		}
		latestIndex := indices[len(indices)-1]
		for _, idx := range indices[:len(indices)-1] {
			if idx < len(messages) && messages[idx].Role == "assistant" {
				for _, call := range messages[idx].ToolCalls {
					toolResultIdx := findToolResultIndex(messages, call.ID, idx+1)
					if toolResultIdx > 0 && toolResultIdx < latestIndex {
						if messages[toolResultIdx].Role == "tool" {
							for i := range messages[toolResultIdx].ToolResults {
								messages[toolResultIdx].ToolResults[i].LLMOutput = "[Duplicate tool result removed; latest result retained]"
								messages[toolResultIdx].ToolResults[i].DisplayOutput = "[Duplicate tool result removed; latest result retained]"
							}
						}
					}
				}
			}
		}
	}

	out := make([]session.Message, 0, len(messages))
	for i, msg := range messages {
		if keep[i] {
			out = append(out, msg)
		}
	}
	return out
}

func findToolResultIndex(messages []session.Message, toolCallID string, startFrom int) int {
	for i := startFrom; i < len(messages); i++ {
		if messages[i].Role == "tool" {
			for _, result := range messages[i].ToolResults {
				if result.ToolCallID == toolCallID {
					return i
				}
			}
		}
	}
	return -1
}

func estimateChars(messages []session.Message) int {
	data, _ := json.Marshal(messages)
	return len(data)
}

func cloneMessages(messages []session.Message) []session.Message {
	out := make([]session.Message, len(messages))
	for i, msg := range messages {
		out[i] = msg
		if msg.ToolCalls != nil {
			out[i].ToolCalls = append([]session.ToolCall{}, msg.ToolCalls...)
		}
		if msg.ToolResults != nil {
			out[i].ToolResults = append([]session.ToolResult{}, msg.ToolResults...)
		}
		if msg.Meta != nil {
			meta := map[string]any{}
			for key, value := range msg.Meta {
				meta[key] = value
			}
			out[i].Meta = meta
		}
	}
	return out
}

func redactSecretsInMessages(messages []session.Message) []session.Message {
	out := cloneMessages(messages)
	for i := range out {
		out[i].Text = redactSecretText(out[i].Text)
		for j := range out[i].ToolCalls {
			if len(out[i].ToolCalls[j].Arguments) > 0 {
				out[i].ToolCalls[j].Arguments = redactToolCallArguments(out[i].ToolCalls[j].Arguments)
			}
		}
		for j := range out[i].ToolResults {
			out[i].ToolResults[j].LLMOutput = redactSecretText(out[i].ToolResults[j].LLMOutput)
			out[i].ToolResults[j].DisplayOutput = redactSecretText(out[i].ToolResults[j].DisplayOutput)
			out[i].ToolResults[j].Metadata = redactMetadata(out[i].ToolResults[j].Metadata)
		}
		out[i].Meta = redactMetadata(out[i].Meta)
	}
	return out
}

func redactToolCallArguments(raw json.RawMessage) json.RawMessage {
	if strings.TrimSpace(string(raw)) == "" {
		return json.RawMessage(`{}`)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err == nil {
		redacted := redactAny(decoded)
		if data, err := json.Marshal(redacted); err == nil {
			return json.RawMessage(data)
		}
	}
	redactedText := redactSecretText(string(raw))
	if json.Valid([]byte(redactedText)) {
		return json.RawMessage(redactedText)
	}
	data, err := json.Marshal(map[string]any{
		"redacted_invalid_json": true,
		"text":                  redactedText,
	})
	if err != nil {
		return json.RawMessage(`{"redacted_invalid_json":true}`)
	}
	return json.RawMessage(data)
}

func redactMetadata(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	value, ok := redactAny(input).(map[string]any)
	if !ok {
		return nil
	}
	return value
}

func redactSummaryMap(input map[string]any) map[string]any {
	value, ok := redactAny(input).(map[string]any)
	if !ok {
		return input
	}
	return value
}

func redactAny(value any) any {
	switch typed := value.(type) {
	case string:
		return redactSecretText(typed)
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if isSecretLikeKey(key) {
				out[key] = redactSecretValue(item)
				continue
			}
			out[key] = redactAny(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = redactAny(item)
		}
		return out
	case []string:
		out := make([]string, len(typed))
		for i, item := range typed {
			out[i] = redactSecretText(item)
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, len(typed))
		for i, item := range typed {
			out[i] = redactSummaryMap(item)
		}
		return out
	case []session.TodoItem, []session.Task:
		data, err := json.Marshal(typed)
		if err != nil {
			return value
		}
		var out any
		if json.Unmarshal([]byte(redactSecretText(string(data))), &out) != nil {
			return value
		}
		return out
	default:
		return value
	}
}

func isSecretLikeKey(key string) bool {
	compact := strings.Map(func(r rune) rune {
		switch r {
		case '_', '-', '.', ' ':
			return -1
		default:
			return r
		}
	}, strings.ToLower(strings.TrimSpace(key)))
	if compact == "" {
		return false
	}
	return compact == "token" ||
		strings.HasSuffix(compact, "token") ||
		strings.Contains(compact, "apikey") ||
		strings.Contains(compact, "secret") ||
		strings.Contains(compact, "password") ||
		strings.Contains(compact, "authorization")
}

func redactSecretValue(value any) any {
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return typed
		}
		return "[REDACTED]"
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = redactSecretValue(item)
		}
		return out
	case []string:
		out := make([]string, len(typed))
		for i, item := range typed {
			if strings.TrimSpace(item) == "" {
				out[i] = item
			} else {
				out[i] = "[REDACTED]"
			}
		}
		return out
	case nil:
		return nil
	default:
		return "[REDACTED]"
	}
}

var (
	privateKeyBlockPattern = regexp.MustCompile(`(?is)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	bearerTokenPattern     = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}`)
	jsonSecretPattern      = regexp.MustCompile(`(?i)("(?:[^"]*(?:api[_-]?key|token|secret|password)[^"]*)"\s*:\s*")([^"]{8,})(")`)
	envSecretPattern       = regexp.MustCompile(`(?i)\b([A-Z0-9_]*(?:API[_-]?KEY|TOKEN|SECRET|PASSWORD)[A-Z0-9_]*)\s*[:=]\s*["']?([^\s"']{8,})["']?`)
)

func redactSecretText(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	text = privateKeyBlockPattern.ReplaceAllString(text, "[REDACTED PRIVATE KEY]")
	text = bearerTokenPattern.ReplaceAllString(text, "Bearer [REDACTED]")
	text = jsonSecretPattern.ReplaceAllString(text, `${1}[REDACTED]$3`)
	text = envSecretPattern.ReplaceAllStringFunc(text, func(match string) string {
		sep := strings.IndexAny(match, "=:")
		if sep < 0 {
			return "[REDACTED]"
		}
		return strings.TrimSpace(match[:sep]) + match[sep:sep+1] + "[REDACTED]"
	})
	return text
}

func summarizeLatestMessages(messages []session.Message) []string {
	limit := 3
	if len(messages) < limit {
		limit = len(messages)
	}
	var out []string
	for _, msg := range messages[len(messages)-limit:] {
		switch msg.Role {
		case "user", "assistant":
			out = append(out, fmt.Sprintf("%s: %s", msg.Role, truncateText(msg.Text, 300)))
		case "tool":
			out = append(out, fmt.Sprintf("tool: %d result(s)", len(msg.ToolResults)))
		}
	}
	return out
}

func collectCompletedItems(todo []session.TodoItem, tasks []session.Task) []string {
	var out []string
	for _, item := range todo {
		if item.Status == "completed" {
			out = append(out, "todo: "+item.Content)
		}
	}
	for _, task := range tasks {
		if task.Status == "completed" {
			out = append(out, "task: "+task.Subject)
		}
	}
	return out
}

func filterTasks(tasks []session.Task, fn func(session.Task) bool) []session.Task {
	var out []session.Task
	for _, task := range tasks {
		if fn(task) {
			out = append(out, task)
		}
	}
	return out
}

func currentInProgressTodo(todo []session.TodoItem) map[string]any {
	for _, item := range todo {
		if item.Status == "in_progress" {
			return map[string]any{
				"content":    item.Content,
				"priority":   item.Priority,
				"updated_at": item.UpdatedAt,
			}
		}
	}
	return nil
}

func currentInProgressTask(tasks []session.Task) map[string]any {
	for _, task := range tasks {
		if task.Status == "in_progress" {
			return map[string]any{
				"id":          task.ID,
				"subject":     task.Subject,
				"description": task.Description,
				"priority":    task.Priority,
				"blocked_by":  task.BlockedBy,
				"updated_at":  task.UpdatedAt,
			}
		}
	}
	return nil
}

func collectKeyPaths(messages []session.Message, workdir string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, msg := range messages {
		if msg.Role != "tool" {
			continue
		}
		for _, result := range msg.ToolResults {
			value, _ := result.Metadata["path"].(string)
			if value == "" {
				continue
			}
			path := compactRelativeOrAbsolute(workdir, value)
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			out = append(out, path)
		}
	}
	return out
}

func nextStepGuidance() []string {
	return []string{
		"Use key_paths, artifact_memory, project_memory_stack, and high_value_proofs before rereading previously inspected files unless you need exact lines.",
		"Prefer targeted read_file offsets or focused grep/glob over broad workspace scans.",
		"Keep the runtime-enforced reserved final proof-read budget for decisive snippet-backed verification instead of spending it on broad rediscovery.",
		"If you already have enough evidence to answer or act, stop gathering more context and proceed.",
	}
}

func collectArtifactMemory(messages []session.Message, workdir string, limit int) []map[string]any {
	if limit <= 0 {
		limit = 8
	}
	seen := map[string]struct{}{}
	out := make([]map[string]any, 0, limit)
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != "tool" {
			continue
		}
		for j := len(msg.ToolResults) - 1; j >= 0; j-- {
			result := msg.ToolResults[j]
			if result.Name != "read_file" {
				continue
			}
			path, _ := result.Metadata["path"].(string)
			if !looksDeliverablePath(path) && !strings.HasSuffix(strings.ToLower(path), ".md") {
				continue
			}
			rel := compactRelativeOrAbsolute(workdir, path)
			if _, ok := seen[rel]; ok {
				continue
			}
			excerpt := readFileExcerpt(result.DisplayOutput)
			if excerpt == "" {
				continue
			}
			seen[rel] = struct{}{}
			out = append(out, map[string]any{
				"path":    rel,
				"excerpt": truncateText(excerpt, 400),
			})
			if len(out) >= limit {
				return out
			}
		}
	}
	return out
}

func collectHighValueProofs(messages []session.Message, workdir string, limit int) []map[string]any {
	if limit <= 0 {
		limit = 4
	}
	seen := map[string]struct{}{}
	type proofCandidate struct {
		data     map[string]any
		priority int
	}
	candidates := make([]proofCandidate, 0, limit*2)

	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != "tool" {
			continue
		}
		for j := len(msg.ToolResults) - 1; j >= 0; j-- {
			result := msg.ToolResults[j]
			if result.Name != "read_file" {
				continue
			}
			path, _ := result.Metadata["path"].(string)
			if path == "" || isProjectMemoryPath(path) {
				continue
			}
			if !isCodePath(path) && !looksDeliverablePath(path) && !strings.HasSuffix(strings.ToLower(path), ".md") {
				continue
			}
			excerpt := readFileExcerpt(result.DisplayOutput)
			if excerpt == "" {
				continue
			}
			rel := compactRelativeOrAbsolute(workdir, path)
			startLine, endLine := proofLineWindow(result.Metadata)
			key := rel + ":" + startLine + "-" + endLine
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}

			priority := proofPriority(excerpt)
			candidates = append(candidates, proofCandidate{
				data: map[string]any{
					"path":        rel,
					"line_window": startLine + "-" + endLine,
					"kind":        proofKind(path),
					"excerpt":     truncateText(excerpt, 320),
				},
				priority: priority,
			})
		}
	}

	for i := 0; i < len(candidates); i++ {
		for j := i + 1; j < len(candidates); j++ {
			if candidates[j].priority > candidates[i].priority {
				candidates[i], candidates[j] = candidates[j], candidates[i]
			}
		}
	}

	out := make([]map[string]any, 0, limit)
	for _, candidate := range candidates {
		out = append(out, candidate.data)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func proofPriority(excerpt string) int {
	lower := strings.ToLower(excerpt)
	if strings.Contains(lower, "error") || strings.Contains(lower, "exception") {
		return 3
	}
	if strings.Contains(lower, "warning") || strings.Contains(lower, "failed") {
		return 2
	}
	return 1
}

func proofLineWindow(meta map[string]any) (string, string) {
	start := metadataInt(meta, "offset") + 1
	end := metadataInt(meta, "end")
	if start <= 0 {
		start = 1
	}
	if end <= 0 {
		end = start
	}
	if end < start {
		end = start
	}
	return strconv.Itoa(start), strconv.Itoa(end)
}

func proofKind(path string) string {
	switch {
	case isCodePath(path):
		return "code"
	case looksDeliverablePath(path):
		return "artifact"
	default:
		return "doc"
	}
}

func proofReadBudget() map[string]any {
	return map[string]any{
		"reserved_final_targeted_reads": 2,
		"guidance":                      "Runtime reserves two narrow rereads on already inspected files for final proof checks after compaction or before the last review artifact write.",
	}
}

func readFileExcerpt(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	if strings.HasPrefix(text, "[read_file ") {
		if idx := strings.Index(text, "\n"); idx >= 0 {
			text = text[idx+1:]
		} else {
			return ""
		}
	}
	return firstNonEmptyLines(text, 8)
}

func collectUnresolvedIssues(messages []session.Message, state session.State) []string {
	seen := map[string]struct{}{}
	var out []string
	if state.LastError != "" {
		seen[state.LastError] = struct{}{}
		out = append(out, "state: "+state.LastError)
	}
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != "tool" {
			continue
		}
		for _, result := range msg.ToolResults {
			if !result.IsError {
				continue
			}
			detail := result.DisplayOutput
			if detail == "" {
				detail = result.LLMOutput
			}
			if detail == "" {
				continue
			}
			item := fmt.Sprintf("%s: %s", result.Name, detail)
			if _, ok := seen[item]; ok {
				continue
			}
			seen[item] = struct{}{}
			out = append(out, item)
			if len(out) >= 5 {
				return out
			}
		}
	}
	return out
}

func recentFailureOrPause(state session.State) map[string]any {
	if state.LastError == "" && state.PauseReason == "" && state.IncompleteReason == "" {
		return nil
	}
	out := map[string]any{}
	if state.LastError != "" {
		out["last_error"] = state.LastError
	}
	if state.PauseReason != "" {
		out["pause_reason"] = state.PauseReason
	}
	if state.IncompleteReason != "" {
		out["incomplete_reason"] = state.IncompleteReason
	}
	return out
}

func compactRelativeOrAbsolute(base, target string) string {
	if rel, err := filepath.Rel(base, target); err == nil {
		if rel == "." {
			return rel
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return rel
		}
	}
	return target
}

func truncateText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
