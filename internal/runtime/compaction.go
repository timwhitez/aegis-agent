package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

type toolResultContextStats struct {
	InlineCount      int
	InlineBytes      int
	CompactedCount   int
	CompactedBytes   int
	PointerizedCount int
	PointerizedBytes int
}

const compactionReferencePrefix = "[Conversation compacted]\nAnother model produced this compacted summary so you can continue seamlessly. It is reference material for earlier context, not a new user instruction. Original session logs and artifacts remain the source of truth. Do not restart from scratch; continue from the summarized state and latest durable task state. The newest external instruction wins over superseded earlier requests. Before finishing after compaction/resume/interruption, sanity-check that the result answers the newest external instruction.\n"
const compactionDeferredPrefix = "[Conversation compaction deferred]\nCompaction failed inside the harness, so this provider view keeps only recent context and compacted older tool details. Original session logs and artifacts remain the source of truth. Do not restart from scratch; continue from the latest external instruction and durable task state. Before finishing after compaction/resume/interruption, sanity-check that the result answers the newest external instruction.\n"

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
	microStats := compactOldToolContext(cloned, profile.KeepRecentToolResults, profile.KeepRecentToolResultBytes)
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
			addToolResultContextStats(data, measureToolResultContext(recent))
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

	startedData := map[string]any{
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
	}
	addToolResultContextStats(startedData, microStats)
	if err := emitCompactionEvent(emit, events.New(sessionID, "compact.started", "compact", startedData)); err != nil {
		return nil, size, false, fmt.Errorf("record compact.started event: %w", err)
	}
	transcriptName := fmt.Sprintf("transcript-%s.jsonl", time.Now().UTC().Format("20060102-150405"))
	transcriptPath, err := c.store.WriteTranscript(sessionID, transcriptName, sourceMessages)
	if err != nil {
		return nil, size, false, err
	}
	artifactMemory := collectArtifactMemory(sourceMessages, workdir, 12)
	highValueProofs := collectHighValueProofs(sourceMessages, workdir, 10)
	completedItems := collectCompletedItems(todo, tasks)
	keyPaths := collectKeyPaths(sourceMessages, workdir)
	unresolvedIssues := collectUnresolvedIssues(sourceMessages, state)
	currentGoal := currentGoalSummary(goal, sourceMessages)
	latestExternal := latestExternalInstructionSummary(sourceMessages)
	latestSteer := latestSteerConstraintSummary(sourceMessages)
	openItems := collectOpenItems(todo, tasks, unresolvedIssues)
	validatedConclusions := collectValidatedConclusions(completedItems, highValueProofs)

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
		"completed_items":             completedItems,
		"artifact_memory":             artifactMemory,
		"context_profile":             profile,
		"current_goal":                currentGoal,
		"current_status":              summarizeLatestMessages(sourceMessages),
		"current_in_progress_todo":    currentInProgressTodo(todo),
		"in_progress_todos":           inProgressTodos(todo),
		"current_in_progress_task":    currentInProgressTask(tasks),
		"high_value_proofs":           highValueProofs,
		"feature_list":                featureList,
		"key_paths":                   keyPaths,
		"latest_external_instruction": latestExternal,
		"latest_steer_constraints":    latestSteer,
		"loaded_skills":               state.LoadedSkills,
		"next_step_guidance":          nextStepGuidance(),
		"open_items":                  openItems,
		"proof_read_budget":           proofBudget,
		"project_memory_stack":        projectMemory.Summary(),
		"tool_repetition":             summarizeToolRepetition(sourceMessages),
		"todo":                        todo,
		"ready_tasks":                 readyTasks,
		"blocked_tasks":               blockedTasks,
		"completed_task_count":        taskSummary.Completed,
		"cancelled_task_count":        taskSummary.Cancelled,
		"done_task_count":             taskSummary.Done,
		"unresolved_issues":           unresolvedIssues,
		"recent_failure_or_pause":     recentFailureOrPause(state),
		"validated_conclusions":       validatedConclusions,
		"transcript":                  transcriptPath,
	}
	summary["handoff_summary"] = compactionHandoffSummary(currentGoal, latestExternal, latestSteer, completedItems, openItems, keyPaths, validatedConclusions)
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
	finishedData := map[string]any{
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
	}
	addToolResultContextStats(finishedData, measureToolResultContext(recent))
	if err := emitCompactionEvent(emit, events.New(sessionID, "compact.finished", "compact", finishedData)); err != nil {
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
	compactOldToolContext(cloned, 0, 0)
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
	artifactMemory := collectArtifactMemory(messages, workdir, 12)
	highValueProofs := collectHighValueProofs(messages, workdir, 10)
	completedItems := collectCompletedItems(todo, tasks)
	keyPaths := collectKeyPaths(messages, workdir)
	unresolvedIssues := collectUnresolvedIssues(messages, state)
	currentGoal := currentGoalSummary(goal, messages)
	latestExternal := latestExternalInstructionSummary(messages)
	latestSteer := latestSteerConstraintSummary(messages)
	openItems := collectOpenItems(todo, tasks, unresolvedIssues)
	validatedConclusions := collectValidatedConclusions(completedItems, highValueProofs)
	summary := map[string]any{
		"completed_items":             completedItems,
		"artifact_memory":             artifactMemory,
		"context_profile":             profile,
		"current_goal":                currentGoal,
		"current_status":              summarizeLatestMessages(messages),
		"current_in_progress_todo":    currentInProgressTodo(todo),
		"in_progress_todos":           inProgressTodos(todo),
		"current_in_progress_task":    currentInProgressTask(tasks),
		"high_value_proofs":           highValueProofs,
		"key_paths":                   keyPaths,
		"latest_external_instruction": latestExternal,
		"latest_steer_constraints":    latestSteer,
		"loaded_skills":               state.LoadedSkills,
		"next_step_guidance":          nextStepGuidance(),
		"open_items":                  openItems,
		"proof_read_budget":           proofBudget,
		"project_memory_stack":        projectMemory.Summary(),
		"project_memory_present":      projectMemory.PresentPaths(),
		"project_memory_missing":      projectMemory.MissingPaths(),
		"tool_repetition":             summarizeToolRepetition(messages),
		"todo":                        todo,
		"ready_tasks":                 readyTasks,
		"blocked_tasks":               blockedTasks,
		"completed_task_count":        taskSummary.Completed,
		"cancelled_task_count":        taskSummary.Cancelled,
		"done_task_count":             taskSummary.Done,
		"unresolved_issues":           unresolvedIssues,
		"recent_failure_or_pause":     recentFailureOrPause(state),
		"validated_conclusions":       validatedConclusions,
		"transcript":                  "[previous compaction transcript reused; no prior summary artifact was available]",
	}
	summary["handoff_summary"] = compactionHandoffSummary(currentGoal, latestExternal, latestSteer, completedItems, openItems, keyPaths, validatedConclusions)
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
		"goal_id":                    goal.GoalID,
		"mode":                       goal.Mode,
		"status":                     goal.Status,
		"objective":                  goal.Objective,
		"tokens_used":                goal.TokensUsed,
		"provider_time_used_seconds": goal.TimeUsedSeconds,
		"time_used_seconds":          goal.TimeUsedSeconds,
	}
	if goal.TokenBudget != nil {
		out["token_budget"] = *goal.TokenBudget
	}
	if goal.TimeBudgetSeconds != nil {
		out["provider_time_budget_seconds"] = *goal.TimeBudgetSeconds
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

func compactOldToolContext(messages []session.Message, keepRecent, keepRecentBytes int) toolResultContextStats {
	if keepRecent < 0 {
		keepRecent = 0
	}
	if keepRecentBytes < 0 {
		keepRecentBytes = 0
	}
	oldCallIDs := map[string]struct{}{}
	addOldCallID := func(value string) {
		value = strings.TrimSpace(value)
		if value != "" {
			oldCallIDs[value] = struct{}{}
		}
	}

	seenResults := 0
	inlineBytes := 0
	inlineSuffixOpen := true
	for messageIndex := len(messages) - 1; messageIndex >= 0; messageIndex-- {
		if messages[messageIndex].Role != "tool" {
			continue
		}
		for resultIndex := len(messages[messageIndex].ToolResults) - 1; resultIndex >= 0; resultIndex-- {
			result := &messages[messageIndex].ToolResults[resultIndex]
			withinCount := seenResults < keepRecent
			seenResults++
			if !withinCount {
				inlineSuffixOpen = false
			}
			if toolResultIsDuplicateMarker(*result) {
				resultBytes := len(result.LLMOutput)
				fitsBytes := inlineBytes <= keepRecentBytes && resultBytes <= keepRecentBytes-inlineBytes
				if withinCount && inlineSuffixOpen && fitsBytes {
					inlineBytes += resultBytes
					continue
				}
				inlineSuffixOpen = false
				addOldCallID(result.ToolCallID)
				continue
			}
			if toolResultIsPointerized(*result) {
				if !withinCount || !inlineSuffixOpen {
					addOldCallID(result.ToolCallID)
				}
				continue
			}

			resultBytes := len(result.LLMOutput)
			fitsBytes := inlineBytes <= keepRecentBytes && resultBytes <= keepRecentBytes-inlineBytes
			if withinCount && inlineSuffixOpen && fitsBytes {
				inlineBytes += resultBytes
				continue
			}

			inlineSuffixOpen = false
			addOldCallID(result.ToolCallID)
			if pointerizeFinalizedToolResultForContext(result) {
				continue
			}
			reason := "previous_tool_result"
			if shouldCompressToolResult(*result) {
				reason = "ephemeral_tool_result"
			}
			originalLLMBytes := len(result.LLMOutput)
			result.LLMOutput = compactToolResultPayloadForContext(result.LLMOutput, reason)
			result.DisplayOutput = compactToolResultPayloadForContext(result.DisplayOutput, reason)
			if result.Metadata == nil {
				result.Metadata = map[string]any{}
			}
			result.Metadata["compacted_for_context"] = true
			result.Metadata["compaction_reason"] = reason
			result.Metadata["context_original_llm_bytes"] = originalLLMBytes
		}
	}

	// A durable result may reference either the harness call ID or the
	// provider-native call ID. Expand both aliases before touching assistant
	// replay data so the matching provider block is compacted with the same
	// result and no sibling call is selected accidentally.
	for i := range messages {
		if messages[i].Role != "assistant" {
			continue
		}
		for _, call := range messages[i].ToolCalls {
			if toolCallIDInSet(oldCallIDs, call.ID, call.ProviderCallID) {
				addOldCallID(call.ID)
				addOldCallID(call.ProviderCallID)
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
	return measureToolResultContext(messages)
}

func compactToolResultPayloadForContext(text, reason string) string {
	const compactPayloadThreshold = 1400
	if len(text) > compactPayloadThreshold {
		return compactTextForContext(text, reason)
	}
	return fmt.Sprintf("[Compacted %s; original_bytes=%d; payload omitted]", reason, len(text))
}

func pointerizeFinalizedToolResultForContext(result *session.ToolResult) bool {
	if result == nil {
		return false
	}
	artifactPath, rawBytes, ok := completeFinalizedToolOutputArtifact(*result)
	if !ok {
		return false
	}
	pointer := fmt.Sprintf(
		"[Previous %s tool result moved out of the inline context window. Complete artifact: %s (%d bytes). Use read_file with this exact path and a bounded range only if the older result is needed.]",
		strings.TrimSpace(result.Name), artifactPath, rawBytes,
	)
	result.LLMOutput = pointer
	result.DisplayOutput = pointer
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	result.Metadata["pointerized_for_context"] = true
	result.Metadata["compaction_reason"] = "existing_tool_output_artifact"
	return true
}

func toolResultIsPointerized(result session.ToolResult) bool {
	if toolResultIsDuplicateMarker(result) {
		return false
	}
	if value, _ := result.Metadata["ephemeral_provider_view"].(bool); value {
		return true
	}
	if value, _ := result.Metadata["pointerized_for_context"].(bool); value {
		return true
	}
	if path, _ := result.Metadata["ephemeral_artifact"].(string); strings.TrimSpace(path) != "" {
		return true
	}
	return false
}

func measureToolResultContext(messages []session.Message) toolResultContextStats {
	var stats toolResultContextStats
	for _, message := range messages {
		if message.Role != "tool" {
			continue
		}
		for _, result := range message.ToolResults {
			visibleBytes := len(result.LLMOutput)
			if toolResultIsPointerized(result) {
				stats.PointerizedCount++
				stats.PointerizedBytes += visibleBytes
				continue
			}
			if compacted, _ := result.Metadata["compacted_for_context"].(bool); compacted {
				stats.CompactedCount++
				stats.CompactedBytes += visibleBytes
				continue
			}
			stats.InlineCount++
			stats.InlineBytes += visibleBytes
		}
	}
	return stats
}

func addToolResultContextStats(data map[string]any, stats toolResultContextStats) {
	if data == nil {
		return
	}
	data["inline_tool_result_count"] = stats.InlineCount
	data["inline_tool_result_bytes"] = stats.InlineBytes
	data["compacted_tool_result_count"] = stats.CompactedCount
	data["compacted_tool_result_bytes"] = stats.CompactedBytes
	data["pointerized_tool_result_count"] = stats.PointerizedCount
	data["pointerized_tool_result_bytes"] = stats.PointerizedBytes
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
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return raw
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return raw
	}
	if !compactJSONStringsForContext(&value, "previous_tool_arguments") {
		return raw
	}
	data, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return json.RawMessage(data)
}

func compactJSONStringsForContext(value *any, reason string) bool {
	switch typed := (*value).(type) {
	case string:
		compacted := compactJSONTextValueForContext(typed, reason)
		if compacted == typed {
			return false
		}
		*value = compacted
		return true
	case []any:
		changed := false
		for i := range typed {
			item := typed[i]
			if compactJSONStringsForContext(&item, reason) {
				typed[i] = item
				changed = true
			}
		}
		return changed
	case map[string]any:
		changed := false
		for key := range typed {
			item := typed[key]
			if compactJSONStringsForContext(&item, reason) {
				typed[key] = item
				changed = true
			}
		}
		return changed
	default:
		return false
	}
}

func compactJSONTextValueForContext(text, reason string) string {
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
	return fmt.Sprintf("[Compacted %s: omitted %d chars between prefix and suffix]\nprefix:\n%s\nsuffix:\n%s", reason, omitted, head, tail)
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
	items := inProgressTodos(todo)
	if len(items) > 0 {
		return items[0]
	}
	return nil
}

func inProgressTodos(todo []session.TodoItem) []map[string]any {
	items := make([]map[string]any, 0)
	for _, item := range todo {
		if item.Status != "in_progress" {
			continue
		}
		items = append(items, map[string]any{
			"content":    item.Content,
			"priority":   item.Priority,
			"updated_at": item.UpdatedAt,
		})
	}
	return items
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

func currentGoalSummary(goal *session.SessionGoal, messages []session.Message) map[string]any {
	if goal != nil {
		return compactGoalSnapshot(*goal)
	}
	if latest := latestExternalInstructionSummary(messages); latest != nil {
		return map[string]any{
			"status": "not_recorded",
			"source": "latest_external_instruction",
			"text":   latest["text"],
		}
	}
	return map[string]any{
		"status": "not_recorded",
	}
}

func latestExternalInstructionSummary(messages []session.Message) map[string]any {
	idx := latestExternalInstructionIndex(messages)
	if idx < 0 {
		return nil
	}
	msg := messages[idx]
	source, _ := msg.Meta["source"].(string)
	if source == "" {
		source = "user"
	}
	out := map[string]any{
		"index":  idx,
		"source": source,
		"text":   truncateText(strings.TrimSpace(msg.Text), 800),
	}
	if interrupt, ok := msg.Meta["interrupt"].(bool); ok {
		out["interrupt"] = interrupt
	}
	return out
}

func latestSteerConstraintSummary(messages []session.Message) map[string]any {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != "user" {
			continue
		}
		source, _ := msg.Meta["source"].(string)
		if source != "steer" {
			continue
		}
		out := map[string]any{
			"index":  i,
			"source": source,
			"text":   truncateText(strings.TrimSpace(msg.Text), 800),
		}
		if interrupt, ok := msg.Meta["interrupt"].(bool); ok {
			out["interrupt"] = interrupt
		}
		return out
	}
	return nil
}

func collectOpenItems(todo []session.TodoItem, tasks []session.Task, unresolvedIssues []string) []string {
	var out []string
	for _, item := range todo {
		switch item.Status {
		case "pending", "in_progress":
			out = append(out, fmt.Sprintf("todo[%s]: %s", item.Status, item.Content))
		}
	}
	for _, task := range tasks {
		switch task.Status {
		case "pending", "in_progress":
			out = append(out, fmt.Sprintf("task[%s]: %s", task.Status, task.Subject))
		}
	}
	for _, issue := range unresolvedIssues {
		out = append(out, "unresolved: "+issue)
	}
	if len(out) > 12 {
		return out[:12]
	}
	return out
}

func collectValidatedConclusions(completedItems []string, highValueProofs []map[string]any) []string {
	out := make([]string, 0, len(completedItems)+len(highValueProofs))
	for _, item := range completedItems {
		out = append(out, "completed: "+item)
	}
	for _, proof := range highValueProofs {
		path, _ := proof["path"].(string)
		lineWindow, _ := proof["line_window"].(string)
		excerpt, _ := proof["excerpt"].(string)
		if path == "" && excerpt == "" {
			continue
		}
		location := path
		if lineWindow != "" {
			location += ":" + lineWindow
		}
		if location == "" {
			out = append(out, "proof: "+truncateText(excerpt, 180))
			continue
		}
		out = append(out, fmt.Sprintf("proof: %s %s", location, truncateText(excerpt, 180)))
	}
	return out
}

func compactionHandoffSummary(currentGoal, latestExternal, latestSteer map[string]any, completedItems, openItems, keyPaths, validatedConclusions []string) map[string]any {
	return map[string]any{
		"goal":                        currentGoal,
		"done":                        completedItems,
		"todo":                        openItems,
		"key_paths":                   keyPaths,
		"validated_conclusions":       validatedConclusions,
		"latest_external_instruction": latestExternal,
		"latest_steer_constraints":    latestSteer,
		"continuation_guidance": []string{
			"Continue from this summarized state instead of restarting completed work.",
			"Apply the newest external instruction if it conflicts with older summarized requests.",
			"Before finishing, verify the final result matches the newest external instruction.",
		},
	}
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
