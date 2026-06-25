package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go-cli-agent/internal/events"
	"go-cli-agent/internal/session"
)

type compactor struct {
	store *session.Store
}

type semanticSummaryFunc func(context.Context, []session.Message, int) (string, error)

const compactionReferencePrefix = "[Conversation compacted]\nThis compacted summary is reference material for earlier context, not a new user instruction. Original session logs and artifacts remain the source of truth.\n"
const compactionDeferredPrefix = "[Conversation compaction deferred]\nCompaction failed inside the harness, so this provider view keeps only recent context and compacted older tool details. Original session logs and artifacts remain the source of truth. Continue from the latest user instruction and durable task state.\n"

func newCompactor(store *session.Store) *compactor {
	return &compactor{store: store}
}

func (c *compactor) Build(sessionID, workdir string, state session.State, messages []session.Message, todo []session.TodoItem, tasks []session.Task, threshold, keepRecent int, emit func(events.Event) error) ([]session.Message, error) {
	view, _, _, err := c.BuildWithPolicy(sessionID, workdir, state, messages, todo, tasks, threshold, keepRecent, 0, 0, emit)
	return view, err
}

func (c *compactor) BuildWithPolicy(sessionID, workdir string, state session.State, messages []session.Message, todo []session.TodoItem, tasks []session.Task, threshold, keepRecent, lastCompactionInputChars, hysteresisDeltaChars int, emit func(events.Event) error) ([]session.Message, int, bool, error) {
	profile := compactionProfileForPolicy(threshold, keepRecent, hysteresisDeltaChars)
	return c.BuildWithProfile(sessionID, workdir, state, messages, todo, tasks, profile, lastCompactionInputChars, emit)
}

func (c *compactor) BuildWithProfile(sessionID, workdir string, state session.State, messages []session.Message, todo []session.TodoItem, tasks []session.Task, profile compactionContextProfile, lastCompactionInputChars int, emit func(events.Event) error) ([]session.Message, int, bool, error) {
	return c.build(context.Background(), sessionID, workdir, state, messages, todo, tasks, profile, lastCompactionInputChars, 0, nil, emit)
}

func (c *compactor) build(ctx context.Context, sessionID, workdir string, state session.State, messages []session.Message, todo []session.TodoItem, tasks []session.Task, profile compactionContextProfile, lastCompactionInputChars, systemPromptChars int, summarize semanticSummaryFunc, emit func(events.Event) error) ([]session.Message, int, bool, error) {
	profile = normalizeCompactionProfile(profile)
	sourceMessages := cloneMessages(messages)
	cloned := cloneMessages(sourceMessages)
	cloned = deduplicateToolResults(cloned)
	compactOldToolContext(cloned, profile.KeepRecentToolResults)
	size := estimateChars(cloned) + systemPromptChars
	if size <= profile.InputCharThreshold {
		return cloned, size, false, nil
	}
	if lastCompactionInputChars > 0 && profile.HysteresisDeltaChars > 0 && size < lastCompactionInputChars+profile.HysteresisDeltaChars {
		summary, summarySource, err := c.reusableCompactionSummary(sessionID, workdir, state, sourceMessages, todo, tasks, profile)
		if err != nil {
			return nil, size, false, err
		}
		compactText, _ := json.MarshalIndent(summary, "", "  ")
		recent := recentMessagesWithinBudget(cloned, profile.KeepRecentMessages, profile.HysteresisDeltaChars)
		if emit != nil {
			data, err := c.compactReusedEventData(sessionID, workdir, size, lastCompactionInputChars, profile, todo, tasks, summarySource)
			if err != nil {
				return nil, size, false, err
			}
			if err := emit(events.New(sessionID, "compact.reused", "compact", data)); err != nil {
				return nil, size, false, fmt.Errorf("record compact.reused event: %w", err)
			}
		}
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
	taskSummary := taskCounts(tasks)
	proofBudget := proofReadBudget()
	goal, err := loadGoalOptional(c.store, sessionID)
	if err != nil {
		return nil, size, false, fmt.Errorf("load goal.json for compaction: %w", err)
	}

	if err := emitCompactionEvent(emit, events.New(sessionID, "compact.started", "compact", map[string]any{
		"input_chars":            size,
		"reason":                 "input_char_threshold_exceeded",
		"context_profile":        profile,
		"threshold_source":       profile.ThresholdSource,
		"context_window_tokens":  profile.ContextWindowTokens,
		"keep_recent_messages":   profile.KeepRecentMessages,
		"project_memory_present": projectMemory.PresentPaths(),
		"project_memory_missing": projectMemory.MissingPaths(),
		"todo_count":             len(todo),
		"ready_task_count":       len(readyTasks),
		"blocked_task_count":     len(blockedTasks),
		"completed_task_count":   taskSummary.Completed,
		"cancelled_task_count":   taskSummary.Cancelled,
		"done_task_count":        taskSummary.Done,
		"proof_read_budget":      proofBudget,
		"goal_present":           goal != nil,
	})); err != nil {
		return nil, size, false, fmt.Errorf("record compact.started event: %w", err)
	}
	transcriptName := fmt.Sprintf("transcript-%s.jsonl", time.Now().UTC().Format("20060102-150405"))
	transcriptPath, err := c.store.WriteTranscript(sessionID, transcriptName, sourceMessages)
	if err != nil {
		return nil, size, false, err
	}
	artifactMemory := collectArtifactMemory(sourceMessages, workdir, 12)
	highValueProofs := collectHighValueProofs(sourceMessages, workdir, 10)

	var featureList *session.FeatureList
	if fl, err := c.store.LoadFeatureList(sessionID); err == nil {
		featureList = &fl
	} else if !errors.Is(err, fs.ErrNotExist) && !isSymlinkedSessionPathError(err) {
		return nil, size, false, fmt.Errorf("load feature_list.json for compaction: %w", err)
	}

	recent := recentMessagesForCompaction(cloned, profile.KeepRecentMessages)
	semanticSummaryStatus := "disabled"
	semanticSummaryText := ""
	if summarize != nil {
		dropped := droppedMessagesForSemanticSummary(cloned, recent)
		if len(dropped) == 0 {
			semanticSummaryStatus = "skipped"
		} else if text, err := summarize(ctx, dropped, profile.HysteresisDeltaChars); err == nil && strings.TrimSpace(text) != "" {
			semanticSummaryStatus = "ok"
			semanticSummaryText = strings.TrimSpace(text)
		} else {
			semanticSummaryStatus = "failed"
		}
	}

	summary := map[string]any{
		"completed_items":          collectCompletedItems(todo, tasks),
		"artifact_memory":          artifactMemory,
		"context_profile":          profile,
		"current_status":           summarizeLatestMessages(sourceMessages),
		"current_in_progress_todo": currentInProgressTodo(todo),
		"current_in_progress_task": currentInProgressTask(tasks),
		"high_value_proofs":        highValueProofs,
		"feature_list":             featureList,
		"key_paths":                collectKeyPaths(sourceMessages, workdir),
		"loaded_skills":            state.LoadedSkills,
		"next_step_guidance":       nextStepGuidance(),
		"proof_read_budget":        proofBudget,
		"project_memory_stack":     projectMemory.Summary(),
		"tool_repetition":          summarizeToolRepetition(sourceMessages),
		"todo":                     todo,
		"ready_tasks":              readyTasks,
		"blocked_tasks":            blockedTasks,
		"completed_task_count":     taskSummary.Completed,
		"cancelled_task_count":     taskSummary.Cancelled,
		"done_task_count":          taskSummary.Done,
		"unresolved_issues":        collectUnresolvedIssues(sourceMessages, state),
		"recent_failure_or_pause":  recentFailureOrPause(state),
		"transcript":               transcriptPath,
	}
	if semanticSummaryText != "" {
		summary["semantic_summary"] = semanticSummaryText
	}
	if goal != nil {
		summary["goal_snapshot"] = compactGoalSnapshot(*goal)
	}
	summaryName := filepath.Join("compactions", fmt.Sprintf("summary-%s.json", time.Now().UTC().Format("20060102-150405")))
	summaryPath, err := c.store.WriteArtifact(sessionID, summaryName, summary)
	if err != nil {
		return nil, size, false, err
	}
	compactText, _ := json.MarshalIndent(summary, "", "  ")
	if err := emitCompactionEvent(emit, events.New(sessionID, "compact.finished", "compact", map[string]any{
		"summary_path":            summaryPath,
		"input_chars":             size,
		"reason":                  "input_char_threshold_exceeded",
		"context_profile":         profile,
		"threshold_source":        profile.ThresholdSource,
		"context_window_tokens":   profile.ContextWindowTokens,
		"recent_message_count":    len(recent),
		"keep_recent_messages":    profile.KeepRecentMessages,
		"semantic_summary_status": semanticSummaryStatus,
		"project_memory_present":  projectMemory.PresentPaths(),
		"project_memory_missing":  projectMemory.MissingPaths(),
		"todo_count":              len(todo),
		"ready_task_count":        len(readyTasks),
		"blocked_task_count":      len(blockedTasks),
		"completed_task_count":    taskSummary.Completed,
		"cancelled_task_count":    taskSummary.Cancelled,
		"done_task_count":         taskSummary.Done,
		"artifact_memory_count":   len(artifactMemory),
		"high_value_proof_count":  len(highValueProofs),
		"proof_read_budget":       proofBudget,
		"goal_present":            goal != nil,
	})); err != nil {
		return nil, size, false, fmt.Errorf("record compact.finished event: %w", err)
	}
	compacted := session.NewMessage("user", compactionReferencePrefix+string(compactText))
	compacted.Meta = map[string]any{
		"source": "compaction_summary",
	}
	out := []session.Message{compacted}
	out = append(out, recent...)
	return out, size, true, nil
}

func emitCompactionEvent(emit func(events.Event) error, evt events.Event) error {
	if emit == nil {
		return nil
	}
	return emit(evt)
}

func fallbackCompactionDeferredView(messages []session.Message, profile compactionContextProfile, compactErr error, systemPromptChars int) ([]session.Message, int) {
	profile = normalizeCompactionProfile(profile)
	cloned := cloneMessages(messages)
	cloned = deduplicateToolResults(cloned)
	compactOldToolContext(cloned, 0)
	inputChars := estimateChars(cloned) + systemPromptChars
	recent := recentMessagesForCompaction(cloned, profile.KeepRecentMessages)
	deferred := session.NewMessage("user", compactionDeferredPrefix+"Deferred reason: "+compactErr.Error())
	deferred.Meta = map[string]any{
		"source": "compaction_deferred",
	}
	out := []session.Message{deferred}
	out = append(out, recent...)
	return out, inputChars
}

func (c *compactor) reusableCompactionSummary(sessionID, workdir string, state session.State, messages []session.Message, todo []session.TodoItem, tasks []session.Task, profile compactionContextProfile) (map[string]any, string, error) {
	relativePath, err := latestCompactionArtifactRelativePath(c.store, sessionID)
	if err != nil {
		return nil, "", err
	}
	if relativePath != "" {
		var summary map[string]any
		if err := c.store.ReadArtifact(sessionID, relativePath, &summary); err != nil {
			return nil, "", fmt.Errorf("read compaction summary artifact %s: %w", relativePath, err)
		}
		if len(summary) == 0 {
			return nil, "", fmt.Errorf("read compaction summary artifact %s: empty summary", relativePath)
		}
		return summary, relativePath, nil
	}
	summary, err := c.fallbackCompactionReuseSummary(sessionID, workdir, state, messages, todo, tasks, profile)
	if err != nil {
		return nil, "", err
	}
	return summary, "derived", nil
}

func (c *compactor) fallbackCompactionReuseSummary(sessionID, workdir string, state session.State, messages []session.Message, todo []session.TodoItem, tasks []session.Task, profile compactionContextProfile) (map[string]any, error) {
	projectMemory := loadProjectMemoryStack(workdir)
	readyTasks := filterTasks(tasks, func(task session.Task) bool { return task.Status == "pending" && len(task.BlockedBy) == 0 })
	blockedTasks := filterTasks(tasks, func(task session.Task) bool { return task.Status == "pending" && len(task.BlockedBy) > 0 })
	taskSummary := taskCounts(tasks)
	proofBudget := proofReadBudget()
	goal, err := loadGoalOptional(c.store, sessionID)
	if err != nil {
		return nil, fmt.Errorf("load goal.json for compaction reuse: %w", err)
	}
	summary := map[string]any{
		"completed_items":          collectCompletedItems(todo, tasks),
		"artifact_memory":          collectArtifactMemory(messages, workdir, 12),
		"context_profile":          profile,
		"current_status":           summarizeLatestMessages(messages),
		"current_in_progress_todo": currentInProgressTodo(todo),
		"current_in_progress_task": currentInProgressTask(tasks),
		"high_value_proofs":        collectHighValueProofs(messages, workdir, 10),
		"key_paths":                collectKeyPaths(messages, workdir),
		"loaded_skills":            state.LoadedSkills,
		"next_step_guidance":       nextStepGuidance(),
		"proof_read_budget":        proofBudget,
		"project_memory_stack":     projectMemory.Summary(),
		"project_memory_present":   projectMemory.PresentPaths(),
		"project_memory_missing":   projectMemory.MissingPaths(),
		"tool_repetition":          summarizeToolRepetition(messages),
		"todo":                     todo,
		"ready_tasks":              readyTasks,
		"blocked_tasks":            blockedTasks,
		"completed_task_count":     taskSummary.Completed,
		"cancelled_task_count":     taskSummary.Cancelled,
		"done_task_count":          taskSummary.Done,
		"unresolved_issues":        collectUnresolvedIssues(messages, state),
		"recent_failure_or_pause":  recentFailureOrPause(state),
		"transcript":               "[previous compaction transcript reused; no prior summary artifact was available]",
	}
	if goal != nil {
		summary["goal_snapshot"] = compactGoalSnapshot(*goal)
	}
	return summary, nil
}

func (c *compactor) compactReusedEventData(sessionID, workdir string, size, lastCompactionInputChars int, profile compactionContextProfile, todo []session.TodoItem, tasks []session.Task, summarySource string) (map[string]any, error) {
	projectMemory := loadProjectMemoryStack(workdir)
	readyTasks := filterTasks(tasks, func(task session.Task) bool { return task.Status == "pending" && len(task.BlockedBy) == 0 })
	blockedTasks := filterTasks(tasks, func(task session.Task) bool { return task.Status == "pending" && len(task.BlockedBy) > 0 })
	taskSummary := taskCounts(tasks)
	proofBudget := proofReadBudget()
	goal, err := loadGoalOptional(c.store, sessionID)
	if err != nil {
		return nil, fmt.Errorf("load goal.json for compaction reuse event: %w", err)
	}
	return map[string]any{
		"input_chars":                 size,
		"last_compaction_input_chars": lastCompactionInputChars,
		"hysteresis_delta_chars":      profile.HysteresisDeltaChars,
		"reason":                      "within_compaction_hysteresis",
		"context_profile":             profile,
		"threshold_source":            profile.ThresholdSource,
		"context_window_tokens":       profile.ContextWindowTokens,
		"keep_recent_messages":        profile.KeepRecentMessages,
		"summary_source":              summarySource,
		"project_memory_present":      projectMemory.PresentPaths(),
		"project_memory_missing":      projectMemory.MissingPaths(),
		"todo_count":                  len(todo),
		"ready_task_count":            len(readyTasks),
		"blocked_task_count":          len(blockedTasks),
		"completed_task_count":        taskSummary.Completed,
		"cancelled_task_count":        taskSummary.Cancelled,
		"done_task_count":             taskSummary.Done,
		"proof_read_budget":           proofBudget,
		"goal_present":                goal != nil,
	}, nil
}

func latestCompactionArtifactRelativePath(store *session.Store, sessionID string) (string, error) {
	dir := filepath.Join(store.SessionDir(sessionID), "artifacts", "compactions")
	info, err := os.Lstat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("list compaction summary artifacts %s: %w", dir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("list compaction summary artifacts %s: refusing to read symlinked directory", dir)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("list compaction summary artifacts %s: not a directory", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("list compaction summary artifacts %s: %w", dir, err)
	}
	var latest string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "summary-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if latest == "" || entry.Name() > latest {
			latest = entry.Name()
		}
	}
	if latest == "" {
		return "", nil
	}
	return filepath.Join("compactions", latest), nil
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
			for _, callID := range assistantToolCallIDs(msg) {
				delete(pendingToolCalls, callID)
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

func recentMessagesWithinBudget(messages []session.Message, minCount, budgetChars int) []session.Message {
	if budgetChars <= 0 {
		return recentMessagesForCompaction(messages, minCount)
	}
	if len(messages) <= minCount {
		return messages
	}

	keep := make([]bool, len(messages))
	pendingToolCalls := map[string]struct{}{}
	recentKept := 0
	usedChars := 0
	if idx := latestExternalInstructionIndex(messages); idx >= 0 {
		keep[idx] = true
		usedChars += estimateChars([]session.Message{messages[idx]})
	}

	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		mustKeep := keep[i] || recentKept < minCount || usedChars < budgetChars || assistantMatchesPendingToolCall(msg, pendingToolCalls)
		if !mustKeep {
			continue
		}
		if !keep[i] {
			usedChars += estimateChars([]session.Message{msg})
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
			for _, callID := range assistantToolCallIDs(msg) {
				delete(pendingToolCalls, callID)
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

func droppedMessagesForSemanticSummary(messages, retained []session.Message) []session.Message {
	retainedKeys := map[string]int{}
	for _, msg := range retained {
		retainedKeys[messageSemanticKey(msg)]++
	}
	out := make([]session.Message, 0, len(messages))
	for _, msg := range messages {
		key := messageSemanticKey(msg)
		if retainedKeys[key] > 0 {
			retainedKeys[key]--
			continue
		}
		out = append(out, msg)
	}
	return out
}

func messageSemanticKey(msg session.Message) string {
	if strings.TrimSpace(msg.ID) != "" {
		return "id:" + strings.TrimSpace(msg.ID)
	}
	data, _ := json.Marshal(msg)
	return "json:" + string(data)
}

func semanticSummaryInputText(messages []session.Message, maxChars int) string {
	data, _ := json.Marshal(messages)
	text := string(data)
	if maxChars <= 0 || len(text) <= maxChars {
		return text
	}
	return prefixAtRuneBoundary(text, maxChars) + "\n[...truncated for semantic summary input...]"
}

func compactGoalSnapshot(goal session.SessionGoal) map[string]any {
	out := map[string]any{
		"goal_id":           goal.GoalID,
		"mode":              goal.Mode,
		"status":            goal.Status,
		"objective":         goal.Objective,
		"tokens_used":       goal.TokensUsed,
		"time_used_seconds": goal.TimeUsedSeconds,
	}
	if goal.TokenBudget != nil {
		out["token_budget"] = *goal.TokenBudget
	}
	if goal.TimeBudgetSeconds != nil {
		out["time_budget_seconds"] = *goal.TimeBudgetSeconds
	}
	if len(goal.SuccessCriteria) > 0 {
		counters := map[string]int{}
		for _, criterion := range goal.SuccessCriteria {
			counters[firstNonEmpty(criterion.Status, "pending")]++
		}
		out["success_criteria"] = counters
	}
	if len(goal.ValidationPlan) > 0 {
		counters := map[string]int{}
		for _, validation := range goal.ValidationPlan {
			counters[firstNonEmpty(validation.Status, "pending")]++
		}
		out["validation"] = counters
	}
	if goal.Mission != nil {
		out["mission_plan_status"] = goal.Mission.PlanStatus
		out["mission_feature_count"] = len(goal.Mission.Features)
		out["mission_milestone_count"] = len(goal.Mission.Milestones)
		coverage := session.CheckMissionPlanCoverage(goal)
		if coverage.ValidationTotal > 0 {
			out["mission_validation_coverage"] = map[string]any{
				"covered":          coverage.CoveredAssertions,
				"total":            coverage.ValidationTotal,
				"approval_blocked": coverage.ApprovalBlocked,
				"uncovered":        coverage.UncoveredAssertions,
			}
		}
	}
	if len(goal.Progress) > 0 {
		latest := goal.Progress[len(goal.Progress)-1]
		out["latest_progress"] = map[string]any{
			"kind":       latest.Kind,
			"summary":    latest.Summary,
			"created_at": latest.CreatedAt,
			"blockers":   latest.Blockers,
		}
	}
	if goal.Status == session.GoalStatusBudgetLimited && goal.Control.StopOnBudget {
		out["budget_wrapup"] = map[string]any{
			"requested_at": goal.BudgetWrapUpRequestedAt,
			"recorded":     session.HasBudgetWrapUpRecord(goal),
		}
	}
	return out
}

func assistantMatchesPendingToolCall(msg session.Message, pending map[string]struct{}) bool {
	if msg.Role != "assistant" || len(pending) == 0 {
		return false
	}
	for _, callID := range assistantToolCallIDs(msg) {
		if _, ok := pending[callID]; ok {
			return true
		}
	}
	return false
}

func assistantToolCallIDs(msg session.Message) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	for _, call := range msg.ToolCalls {
		add(call.ID)
		add(call.ProviderCallID)
	}
	for _, block := range msg.ProviderContentBlocks {
		switch block.Provider {
		case "anthropic":
			if block.Type == "tool_use" {
				add(block.ID)
			}
		case "google":
			if block.Type == "function_call" {
				add(block.ID)
			}
		}
	}
	return out
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
		if messages[i].Role != "assistant" {
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
		for j := range messages[i].ProviderContentBlocks {
			compactProviderToolCallArguments(&messages[i].ProviderContentBlocks[j], oldCallIDs)
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
	compacted := compactRawJSONForContext(call.Arguments)
	if string(compacted) == string(call.Arguments) {
		return
	}
	call.Arguments = compacted
}

func compactProviderToolCallArguments(block *session.ProviderContentBlock, oldCallIDs map[string]struct{}) {
	if block == nil || !toolCallIDInSet(oldCallIDs, block.ID) {
		return
	}
	switch block.Provider {
	case "anthropic":
		if block.Type == "tool_use" {
			block.Input = compactRawJSONForContext(block.Input)
		}
	case "google":
		if block.Type == "function_call" {
			block.Args = compactRawJSONForContext(block.Args)
		}
	}
}

func toolCallIDInSet(ids map[string]struct{}, values ...string) bool {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := ids[value]; ok {
			return true
		}
	}
	return false
}

func compactRawJSONForContext(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	text := string(raw)
	compacted := compactTextForContext(text, "previous_tool_arguments")
	if compacted == text {
		return raw
	}
	payload := map[string]any{
		"compacted_for_context": true,
		"original_chars":        len(text),
		"head_tail":             compacted,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return raw
	}
	return json.RawMessage(data)
}

func compactTextForContext(text, reason string) string {
	const headLimit = 700
	const tailLimit = 500
	if len(text) <= headLimit+tailLimit+200 {
		return text
	}
	head := prefixAtRuneBoundary(text, headLimit)
	tail := suffixAtRuneBoundary(text, tailLimit)
	omitted := len(text) - len(head) - len(tail)
	if omitted < 0 {
		omitted = 0
	}
	return fmt.Sprintf("[Compacted %s; original_chars=%d]\nHEAD:\n%s\n[...omitted %d chars...]\nTAIL:\n%s", reason, len(text), head, omitted, tail)
}

func prefixAtRuneBoundary(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if limit >= len(text) {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit]
}

func suffixAtRuneBoundary(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if limit >= len(text) {
		return text
	}
	start := len(text) - limit
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[start:]
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
			out[i].ToolCalls = cloneToolCalls(msg.ToolCalls)
		}
		if msg.ToolResults != nil {
			out[i].ToolResults = cloneToolResults(msg.ToolResults)
		}
		if msg.ProviderContentBlocks != nil {
			out[i].ProviderContentBlocks = cloneProviderContentBlocks(msg.ProviderContentBlocks)
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

func cloneToolCalls(calls []session.ToolCall) []session.ToolCall {
	out := append([]session.ToolCall{}, calls...)
	for i := range out {
		out[i].Arguments = cloneRawMessage(calls[i].Arguments)
	}
	return out
}

func cloneToolResults(results []session.ToolResult) []session.ToolResult {
	out := append([]session.ToolResult{}, results...)
	for i := range out {
		if results[i].Metadata != nil {
			metadata := map[string]any{}
			for key, value := range results[i].Metadata {
				metadata[key] = value
			}
			out[i].Metadata = metadata
		}
	}
	return out
}

func cloneProviderContentBlocks(blocks []session.ProviderContentBlock) []session.ProviderContentBlock {
	out := append([]session.ProviderContentBlock{}, blocks...)
	for i := range out {
		out[i].Summary = append([]string{}, blocks[i].Summary...)
		out[i].Input = cloneRawMessage(blocks[i].Input)
		out[i].Args = cloneRawMessage(blocks[i].Args)
		if blocks[i].Thought != nil {
			thought := *blocks[i].Thought
			out[i].Thought = &thought
		}
	}
	return out
}

func cloneRawMessage(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
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
		"Prefer targeted read_file offsets or focused grep/glob when they are the clearest way to inspect evidence.",
		"Use additional read_file calls when exact lines or broader multi-file analysis materially help the task.",
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
	start := metadataIntValue(meta, "offset") + 1
	end := metadataIntValue(meta, "end")
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

func metadataIntValue(meta map[string]any, key string) int {
	if meta == nil {
		return 0
	}
	switch value := meta[key].(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float32:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		if v, err := value.Int64(); err == nil {
			return int(v)
		}
	}
	return 0
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
		"reserved_final_targeted_reads": 0,
		"guidance":                      "Runtime does not reserve or enforce a read_file reread budget; read the exact lines needed for correctness.",
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
	if limit <= 0 {
		return "..."
	}
	return prefixAtRuneBoundary(text, limit) + "..."
}
