package runtime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go-cli-agent/internal/events"
	"go-cli-agent/internal/session"
)

func writeSessionSummary(store *session.Store, sessionID string) error {
	meta, err := store.LoadMetadata(sessionID)
	if err != nil {
		return err
	}
	state, err := store.LoadState(sessionID)
	if err != nil {
		return err
	}
	todo, todoErr := store.LoadTodo(sessionID)
	tasks, tasksErr := store.ListTasks(sessionID)
	contract, contractErr := store.LoadContract(sessionID)
	artifacts, artifactsErr := store.LoadArtifactTracker(sessionID)
	attempts, attemptsErr := store.LoadProviderAttempts(sessionID)
	goal, goalErr := store.LoadGoal(sessionID)
	planMode, planModeErr := store.LoadPlanMode(sessionID)
	children, childrenErr := store.ListChildren(sessionID, -1)
	jobs, jobsErr := store.ListJobsByParent(sessionID, -1)
	notifications, notificationsErr := store.LoadBackgroundNotifications(sessionID)
	coordination, coordinationErr := store.LoadParentCoordination(sessionID)
	checkpoint, checkpointErr := store.LoadLongRunCheckpoint(sessionID)
	messages, messagesErr := store.LoadMessages(sessionID)
	eventsList, eventsErr := store.LoadEvents(sessionID)
	ownerClue, hasOwnerClue, ownerClueErr := latestProcessOwnerClue(eventsList)
	if ownerClueErr != nil {
		return ownerClueErr
	}

	var b strings.Builder
	b.WriteString("# Session Summary\n\n")
	b.WriteString(fmt.Sprintf("- session: `%s`\n", meta.ID))
	b.WriteString(fmt.Sprintf("- status: `%s`\n", state.Status))
	b.WriteString(fmt.Sprintf("- phase: `%s`\n", state.Phase))
	b.WriteString(fmt.Sprintf("- mode: `%s`\n", meta.Mode))
	b.WriteString(fmt.Sprintf("- provider/model: `%s` / `%s`\n", meta.Provider, meta.Model))
	b.WriteString(fmt.Sprintf("- workdir: `%s`\n", meta.Workdir))
	if meta.RequestedWorkdir != "" && meta.RequestedWorkdir != meta.Workdir {
		b.WriteString(fmt.Sprintf("- requested workdir: `%s`\n", meta.RequestedWorkdir))
	}
	if meta.ParentSessionID != "" {
		b.WriteString(fmt.Sprintf("- parent session: `%s`\n", meta.ParentSessionID))
	}
	if meta.RootSessionID != "" && meta.RootSessionID != meta.ID {
		b.WriteString(fmt.Sprintf("- root session: `%s`\n", meta.RootSessionID))
	}
	if meta.AgentName != "" || meta.AgentRole != "" {
		b.WriteString(fmt.Sprintf("- agent: `%s` role `%s`\n", firstNonEmpty(meta.AgentName, "unnamed"), firstNonEmpty(meta.AgentRole, "unspecified")))
	}
	if meta.QueueJobID != "" {
		b.WriteString(fmt.Sprintf("- queue job: `%s`\n", meta.QueueJobID))
	}
	if hasOwnerClue {
		b.WriteString(fmt.Sprintf("- recent owner: source=`%s` handle=`%s` pid=`%d` process_start_id=`%s` started_at=`%s` last_event_at=`%s`\n",
			ownerClue.Source,
			ownerClue.HandleState,
			ownerClue.PID,
			ownerClue.ProcessStartID,
			ownerClue.StartedAt,
			ownerClue.LastEventAt,
		))
	} else if eventsErr != nil {
		b.WriteString(fmt.Sprintf("- recent owner: events.jsonl load error: `%s`\n", eventsErr.Error()))
	}
	if meta.Isolation != nil && meta.Isolation.Mode != "" {
		b.WriteString(fmt.Sprintf("- isolation: `%s` requested `%s`\n", meta.Isolation.Mode, meta.Isolation.RequestedMode))
	}
	if state.LastError != "" {
		b.WriteString(fmt.Sprintf("- last error: `%s`\n", state.LastError))
	}
	if state.PauseReason != "" {
		b.WriteString(fmt.Sprintf("- pause reason: `%s`\n", state.PauseReason))
	}
	b.WriteString(fmt.Sprintf("- turn count: `%d`\n", state.Turn))

	b.WriteString("\n## Goal\n\n")
	if goalErr == nil && goal.GoalID != "" {
		b.WriteString(fmt.Sprintf("- goal: `%s`\n", goal.GoalID))
		b.WriteString(fmt.Sprintf("- mode/status: `%s` / `%s`\n", goal.Mode, goal.Status))
		b.WriteString(fmt.Sprintf("- objective: %s\n", truncateText(goal.Objective, 240)))
		b.WriteString(fmt.Sprintf("- token usage: `%d`", goal.TokensUsed))
		if goal.TokenBudget != nil {
			b.WriteString(fmt.Sprintf(" / `%d`", *goal.TokenBudget))
		}
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("- time usage: `%ds`", goal.TimeUsedSeconds))
		if goal.TimeBudgetSeconds != nil {
			b.WriteString(fmt.Sprintf(" / `%ds`", *goal.TimeBudgetSeconds))
		}
		b.WriteString("\n")
		if len(goal.SuccessCriteria) > 0 {
			verified, total := goalCriterionCounts(goal.SuccessCriteria)
			b.WriteString(fmt.Sprintf("- criteria: `%d/%d` verified\n", verified, total))
		}
		if len(goal.ValidationPlan) > 0 {
			verified, total := goalValidationCounts(goal.ValidationPlan)
			b.WriteString(fmt.Sprintf("- validation: `%d/%d` verified\n", verified, total))
		}
		if goal.CompletionAudit != nil {
			b.WriteString(fmt.Sprintf("- completion audit: evidence=`%d`", len(goal.CompletionAudit.Evidence)))
			if goal.CompletionAudit.CompletedBy != "" {
				b.WriteString(fmt.Sprintf(" by `%s`", goal.CompletionAudit.CompletedBy))
			}
			b.WriteString("\n")
			if goal.CompletionAudit.Summary != "" {
				b.WriteString(fmt.Sprintf("- completion summary: %s\n", truncateText(goal.CompletionAudit.Summary, 240)))
			}
		}
		if goal.Mission != nil {
			b.WriteString(fmt.Sprintf("- mission plan: `%s` features=`%d` milestones=`%d`\n", firstNonEmpty(goal.Mission.PlanStatus, "draft"), len(goal.Mission.Features), len(goal.Mission.Milestones)))
			coverage := session.CheckMissionPlanCoverage(goal)
			if coverage.ValidationTotal > 0 {
				b.WriteString(fmt.Sprintf("- mission validation coverage: `%d/%d` covered", coverage.CoveredAssertions, coverage.ValidationTotal))
				if coverage.ApprovalBlocked {
					b.WriteString(" approval_blocked=`true`")
				}
				b.WriteString("\n")
			}
		}
		if len(goal.Progress) > 0 {
			latest := goal.Progress[len(goal.Progress)-1]
			b.WriteString(fmt.Sprintf("- latest progress: `%s` %s\n", latest.Kind, truncateText(latest.Summary, 180)))
			if len(latest.Blockers) > 0 {
				b.WriteString(fmt.Sprintf("- latest blocker: %s\n", truncateText(latest.Blockers[len(latest.Blockers)-1], 180)))
			}
		}
		if goal.Status == session.GoalStatusBudgetLimited && goal.Control.StopOnBudget {
			b.WriteString(fmt.Sprintf("- budget wrap-up: requested=`%t` recorded=`%t`\n", goal.BudgetWrapUpRequestedAt != "", session.HasBudgetWrapUpRecord(goal)))
		}
	} else if goalErr != nil && !errors.Is(goalErr, os.ErrNotExist) {
		b.WriteString(fmt.Sprintf("goal.json load error: `%s`\n", goalErr.Error()))
	} else {
		b.WriteString("not recorded\n")
	}

	b.WriteString("\n## Plan Mode\n\n")
	if planModeErr == nil && planMode.PlanModeID != "" {
		b.WriteString(fmt.Sprintf("- plan mode: `%s`\n", planMode.PlanModeID))
		b.WriteString(fmt.Sprintf("- status: `%s`\n", planMode.Status))
		b.WriteString(fmt.Sprintf("- objective: %s\n", truncateText(planMode.Objective, 240)))
		b.WriteString(fmt.Sprintf("- version: `%d` approved=`%d`\n", planMode.PlanVersion, planMode.ApprovedVersion))
		if planMode.Summary != "" {
			b.WriteString(fmt.Sprintf("- summary: %s\n", truncateText(planMode.Summary, 240)))
		}
		if planMode.PendingRequest != nil {
			b.WriteString(fmt.Sprintf("- pending input: `%s` questions=`%d`\n", planMode.PendingRequest.RequestID, len(planMode.PendingRequest.Questions)))
		}
	} else if planModeErr != nil && !errors.Is(planModeErr, os.ErrNotExist) {
		b.WriteString(fmt.Sprintf("planmode.json load error: `%s`\n", planModeErr.Error()))
	} else {
		b.WriteString("not recorded\n")
	}

	b.WriteString("\n## Contract\n\n")
	if contractErr == nil && contract.ContractID != "" {
		b.WriteString(fmt.Sprintf("- contract: `%s`\n", contract.ContractID))
		b.WriteString(fmt.Sprintf("- profile: `%s`\n", contract.Profile))
		b.WriteString(fmt.Sprintf("- source: `%s` trust `%s`\n", contract.Source, contract.TrustSource))
		if len(contract.CompletionGates) > 0 {
			b.WriteString(fmt.Sprintf("- gates: `%s`\n", strings.Join(contract.CompletionGates, "`, `")))
		}
		if len(contract.RequiredArtifacts) > 0 {
			b.WriteString(fmt.Sprintf("- required artifacts: `%d`\n", len(contract.RequiredArtifacts)))
		}
	} else if contractErr != nil && !errors.Is(contractErr, os.ErrNotExist) {
		b.WriteString(fmt.Sprintf("contract.json load error: `%s`\n", contractErr.Error()))
	} else {
		b.WriteString("not recorded\n")
	}

	b.WriteString("\n## Required Artifacts\n\n")
	if artifactsErr != nil {
		b.WriteString(fmt.Sprintf("artifact-tracker.json load error: `%s`\n", artifactsErr.Error()))
	} else if len(artifacts) == 0 {
		b.WriteString("not recorded\n")
	} else {
		for _, artifact := range artifacts {
			display := firstNonEmpty(artifact.DisplayPath, artifact.Path)
			status := artifact.Status
			b.WriteString(fmt.Sprintf("- `%s`: present=%t touched=%t changed=%t writer=`%s`\n", display, status.Present, status.TouchedBySession, status.ChangedFromBaseline, status.LastWriterTool))
		}
	}

	b.WriteString("\n## Task State\n\n")
	if todoErr != nil || tasksErr != nil {
		if todoErr != nil {
			b.WriteString(fmt.Sprintf("- todo.json load error: `%s`\n", todoErr.Error()))
		}
		if tasksErr != nil {
			b.WriteString(fmt.Sprintf("- tasks load error: `%s`\n", tasksErr.Error()))
		}
	} else if len(todo) == 0 && len(tasks) == 0 {
		b.WriteString("not recorded\n")
	} else {
		if len(todo) > 0 {
			b.WriteString("- todo:\n")
			for _, item := range todo {
				b.WriteString(fmt.Sprintf("  - `%s` [%s/%s]\n", item.Content, item.Status, item.Priority))
			}
		}
		if len(tasks) > 0 {
			taskSummary := taskCounts(tasks)
			b.WriteString(fmt.Sprintf("- tasks: ready=%d blocked=%d completed=%d cancelled=%d done=%d total=%d\n", taskSummary.Ready, taskSummary.Blocked, taskSummary.Completed, taskSummary.Cancelled, taskSummary.Done, len(tasks)))
		}
	}

	b.WriteString("\n## Tool Repetition\n\n")
	if messagesErr != nil {
		b.WriteString(fmt.Sprintf("messages.jsonl load error: `%s`\n", messagesErr.Error()))
	} else {
		repetition := summarizeToolRepetition(messages)
		if len(repetition.TopTools) == 0 && len(repetition.TopReadPaths) == 0 && repetition.TodoNoopCount == 0 {
			b.WriteString("not observed\n")
		} else {
			if len(state.LoadedSkills) > 0 {
				b.WriteString(fmt.Sprintf("- loaded skills: `%s`\n", strings.Join(state.LoadedSkills, "`, `")))
			}
			for _, item := range repetition.TopTools {
				b.WriteString(fmt.Sprintf("- repeated tool `%s`: %d\n", item.Key, item.Count))
			}
			for _, item := range repetition.TopReadPaths {
				b.WriteString(fmt.Sprintf("- repeated read `%s`: %d\n", item.Key, item.Count))
			}
			if repetition.TodoNoopCount > 0 {
				b.WriteString(fmt.Sprintf("- todo no-op writes: %d\n", repetition.TodoNoopCount))
			}
		}
	}

	b.WriteString("\n## Provider Attempts\n\n")
	if attemptsErr != nil {
		b.WriteString(fmt.Sprintf("provider-attempts.jsonl load error: `%s`\n", attemptsErr.Error()))
	} else if len(attempts) == 0 {
		b.WriteString("not recorded\n")
	} else {
		cache := summarizeProviderAttemptCache(attempts)
		if cache.CreationTokens > 0 || cache.ReadTokens > 0 {
			b.WriteString(fmt.Sprintf("- cache usage: read=`%d` creation=`%d` hit_attempts=`%d`\n", cache.ReadTokens, cache.CreationTokens, cache.HitAttempts))
		}
		start := len(attempts) - 8
		if start < 0 {
			start = 0
		}
		for _, attempt := range attempts[start:] {
			b.WriteString(fmt.Sprintf("- turn=%d attempt=%d outcome=`%s` provider=`%s` class=`%s` status=%d error=`%s`\n", attempt.Turn, attempt.Attempt, attempt.Outcome, attempt.Provider, attempt.ErrorClass, attempt.StatusCode, truncateText(attempt.Error, 120)))
		}
	}

	b.WriteString("\n## Children And Queue\n\n")
	if len(children) == 0 && len(jobs) == 0 && len(notifications) == 0 && childrenErr == nil && jobsErr == nil && notificationsErr == nil && coordinationErr != nil && errors.Is(coordinationErr, os.ErrNotExist) {
		b.WriteString("not recorded\n")
	} else {
		if coordinationErr == nil && coordination.ParentSessionID != "" {
			b.WriteString(fmt.Sprintf("- wait mode: `%s`\n", firstNonEmpty(coordination.WaitMode, parentWaitAll)))
			b.WriteString(fmt.Sprintf("- parked: `%t`\n", coordination.Parked))
			if len(coordination.UnresolvedChildSessions) > 0 {
				b.WriteString(fmt.Sprintf("- unresolved child sessions: `%s`\n", strings.Join(coordination.UnresolvedChildSessions, "`, `")))
			}
			if len(coordination.UnresolvedQueueJobs) > 0 {
				b.WriteString(fmt.Sprintf("- unresolved queue jobs: `%s`\n", strings.Join(coordination.UnresolvedQueueJobs, "`, `")))
			}
			if len(coordination.CompletedChildSessions) > 0 || len(coordination.CompletedQueueJobs) > 0 {
				b.WriteString(fmt.Sprintf("- completed children/jobs: `%d` / `%d`\n", len(coordination.CompletedChildSessions), len(coordination.CompletedQueueJobs)))
			}
			if len(coordination.FailedChildSessions) > 0 || len(coordination.FailedQueueJobs) > 0 {
				b.WriteString(fmt.Sprintf("- failed children/jobs: `%d` / `%d`\n", len(coordination.FailedChildSessions), len(coordination.FailedQueueJobs)))
			}
		} else if coordinationErr != nil && !errors.Is(coordinationErr, os.ErrNotExist) {
			b.WriteString(fmt.Sprintf("- parent-coordination.json load error: `%s`\n", coordinationErr.Error()))
		}
		if childrenErr != nil {
			b.WriteString(fmt.Sprintf("- child sessions load error: `%s`\n", childrenErr.Error()))
		} else if len(children) > 0 {
			b.WriteString(fmt.Sprintf("- child sessions: `%d`\n", len(children)))
		}
		if jobsErr != nil {
			b.WriteString(fmt.Sprintf("- queue jobs load error: `%s`\n", jobsErr.Error()))
		} else if len(jobs) > 0 {
			b.WriteString(fmt.Sprintf("- queue jobs: `%d`\n", len(jobs)))
		}
		if notificationsErr != nil {
			b.WriteString(fmt.Sprintf("- background notifications load error: `control/background.jsonl: %s`\n", notificationsErr.Error()))
		} else if len(notifications) > 0 {
			b.WriteString(fmt.Sprintf("- background notifications: `%d`\n", len(notifications)))
		}
	}

	b.WriteString("\n## Checkpoint\n\n")
	if checkpointErr == nil && checkpoint.SessionID != "" {
		b.WriteString(fmt.Sprintf("- latest: `%s`\n", filepath.Join(store.SessionDir(sessionID), "checkpoints", "longrun-latest.json")))
		b.WriteString(fmt.Sprintf("- created_at: `%s`\n", checkpoint.CreatedAt))
		if len(checkpoint.ResumeHints) > 0 {
			b.WriteString(fmt.Sprintf("- resume hints: `%s`\n", strings.Join(checkpoint.ResumeHints, "`, `")))
		}
	} else if checkpointErr != nil && !errors.Is(checkpointErr, os.ErrNotExist) {
		b.WriteString(fmt.Sprintf("longrun-latest.json load error: `%s`\n", checkpointErr.Error()))
	} else {
		b.WriteString("not recorded\n")
	}

	b.WriteString("\n## Files\n\n")
	b.WriteString(fmt.Sprintf("- metadata: `%s`\n", filepath.Join(store.SessionDir(sessionID), "session.json")))
	b.WriteString(fmt.Sprintf("- state: `%s`\n", filepath.Join(store.SessionDir(sessionID), "state.json")))
	b.WriteString(fmt.Sprintf("- messages: `%s`\n", filepath.Join(store.SessionDir(sessionID), "messages.jsonl")))
	b.WriteString(fmt.Sprintf("- events: `%s`\n", filepath.Join(store.SessionDir(sessionID), "events.jsonl")))
	b.WriteString(fmt.Sprintf("- updated_at: `%s`\n", time.Now().UTC().Format(time.RFC3339Nano)))
	return store.WriteSessionMarkdown(sessionID, b.String())
}

type providerAttemptCacheSummary struct {
	CreationTokens int
	ReadTokens     int
	HitAttempts    int
}

func summarizeProviderAttemptCache(attempts []session.ProviderAttempt) providerAttemptCacheSummary {
	var out providerAttemptCacheSummary
	for _, attempt := range attempts {
		out.CreationTokens += attempt.CacheCreationInputTokens
		out.ReadTokens += attempt.CacheReadInputTokens
		if attempt.CacheReadInputTokens > 0 {
			out.HitAttempts++
		}
	}
	return out
}

func writeLongRunCheckpoint(store *session.Store, sessionID string) error {
	meta, err := store.LoadMetadata(sessionID)
	if err != nil {
		return err
	}
	state, err := store.LoadState(sessionID)
	if err != nil {
		return err
	}
	contract, contractErr := store.LoadContract(sessionID)
	if contractErr != nil && !errors.Is(contractErr, os.ErrNotExist) {
		return fmt.Errorf("load contract.json for long-run checkpoint: %w", contractErr)
	}
	artifacts, err := store.LoadArtifactTracker(sessionID)
	if err != nil {
		return fmt.Errorf("load artifact-tracker.json for long-run checkpoint: %w", err)
	}
	goal, goalErr := store.LoadGoal(sessionID)
	if goalErr != nil && !errors.Is(goalErr, os.ErrNotExist) {
		return fmt.Errorf("load goal.json for long-run checkpoint: %w", goalErr)
	}
	planMode, planModeErr := store.LoadPlanMode(sessionID)
	if planModeErr != nil && !errors.Is(planModeErr, os.ErrNotExist) {
		return fmt.Errorf("load planmode.json for long-run checkpoint: %w", planModeErr)
	}
	todo, err := store.LoadTodo(sessionID)
	if err != nil {
		return fmt.Errorf("load todo.json for long-run checkpoint: %w", err)
	}
	tasks, err := store.ListTasks(sessionID)
	if err != nil {
		return fmt.Errorf("load tasks for long-run checkpoint: %w", err)
	}
	children, err := store.ListChildren(sessionID, -1)
	if err != nil {
		return fmt.Errorf("load child sessions for long-run checkpoint: %w", err)
	}
	jobs, err := store.ListJobsByParent(sessionID, -1)
	if err != nil {
		return fmt.Errorf("load queue jobs for long-run checkpoint: %w", err)
	}
	notifications, err := store.LoadBackgroundNotifications(sessionID)
	if err != nil {
		return fmt.Errorf("load control/background.jsonl for long-run checkpoint: %w", err)
	}
	messages, err := store.LoadMessages(sessionID)
	if err != nil {
		return fmt.Errorf("load messages.jsonl for long-run checkpoint: %w", err)
	}
	eventsList, err := store.LoadEvents(sessionID)
	if err != nil {
		return fmt.Errorf("load events.jsonl for long-run checkpoint: %w", err)
	}
	coordination, coordinationErr := store.LoadParentCoordination(sessionID)
	if coordinationErr != nil && !errors.Is(coordinationErr, os.ErrNotExist) {
		return fmt.Errorf("load parent-coordination.json for long-run checkpoint: %w", coordinationErr)
	}
	ownerClue, hasOwnerClue, ownerClueErr := latestProcessOwnerClue(eventsList)
	if ownerClueErr != nil {
		return ownerClueErr
	}

	if !shouldWriteLongRunCheckpoint(meta, contract, contractErr, goal, goalErr, planMode, planModeErr, artifacts, tasks, children, jobs, coordination, coordinationErr, state) {
		return nil
	}
	latestCompaction, err := latestCompactionArtifact(store, sessionID)
	if err != nil {
		return fmt.Errorf("load compaction artifacts for long-run checkpoint: %w", err)
	}
	rootSessionID := meta.RootSessionID
	if rootSessionID == "" {
		rootSessionID = meta.ID
	}
	taskSummary := taskCounts(tasks)
	checkpoint := session.LongRunCheckpoint{
		SchemaVersion:            1,
		SessionID:                meta.ID,
		RootSessionID:            rootSessionID,
		TodoSummary:              todo,
		TaskSummary:              map[string]int{"ready": taskSummary.Ready, "blocked": taskSummary.Blocked, "completed": taskSummary.Completed, "cancelled": taskSummary.Cancelled, "done": taskSummary.Done, "total": len(tasks)},
		RequiredArtifactStatus:   artifacts,
		LatestCompactionArtifact: latestCompaction,
		Provider:                 meta.Provider,
		Model:                    meta.Model,
		EffectiveProviderOptions: meta.ProviderOptions,
		Workdir:                  meta.Workdir,
		RequestedWorkdir:         meta.RequestedWorkdir,
		Isolation:                meta.Isolation,
		BackgroundNotifications:  len(notifications),
		SourceEventCount:         len(eventsList),
		SourceMessageCount:       len(messages),
		CreatedAt:                time.Now().UTC().Format(time.RFC3339Nano),
	}
	if hasOwnerClue {
		ownerCopy := ownerClue
		checkpoint.RecentOwner = &ownerCopy
	}
	if contractErr == nil && contract.ContractID != "" {
		copyContract := contract
		checkpoint.ContractSnapshot = &copyContract
	}
	if goalErr == nil && goal.GoalID != "" {
		goalCopy := goal
		checkpoint.GoalSnapshot = &goalCopy
	}
	if planModeErr == nil && planMode.PlanModeID != "" {
		planModeCopy := planMode
		checkpoint.PlanModeSnapshot = &planModeCopy
	}
	for _, child := range children {
		if child.Status != session.StatusCompleted && child.Status != session.StatusFailed {
			checkpoint.UnresolvedChildSessions = append(checkpoint.UnresolvedChildSessions, child.ID)
		}
	}
	for _, job := range jobs {
		if job.Status == session.QueueStatusQueued || job.Status == session.QueueStatusRunning || job.Status == session.QueueStatusBlocked {
			checkpoint.UnresolvedQueueJobs = append(checkpoint.UnresolvedQueueJobs, job.ID)
		}
	}
	if len(checkpoint.UnresolvedChildSessions) > 0 || len(checkpoint.UnresolvedQueueJobs) > 0 {
		checkpoint.ParentWaitState = "waiting"
	}
	if coordinationErr == nil && coordination.ParentSessionID != "" {
		for _, childID := range coordination.UnresolvedChildSessions {
			checkpoint.UnresolvedChildSessions = appendUnique(checkpoint.UnresolvedChildSessions, childID)
		}
		for _, jobID := range coordination.UnresolvedQueueJobs {
			checkpoint.UnresolvedQueueJobs = appendUnique(checkpoint.UnresolvedQueueJobs, jobID)
		}
		checkpoint.ParentWaitState = checkpointParentWaitState(coordination)
	}
	checkpoint.ResumeHints = checkpointHints(checkpoint, state)
	return store.SaveLongRunCheckpoint(sessionID, checkpoint)
}

func shouldWriteLongRunCheckpoint(meta session.SessionMetadata, contract session.SessionContract, contractErr error, goal session.SessionGoal, goalErr error, planMode session.PlanModeState, planModeErr error, artifacts []session.RequiredArtifact, tasks []session.Task, children []session.SessionSummary, jobs []session.QueueJob, coordination session.ParentCoordination, coordinationErr error, state session.State) bool {
	if meta.Depth > 0 || meta.ParentSessionID != "" || meta.QueueJobID != "" || len(children) > 0 || len(jobs) > 0 {
		return true
	}
	if coordinationErr == nil && coordination.ParentSessionID != "" {
		return true
	}
	if meta.Isolation != nil && meta.Isolation.Mode != "" && meta.Isolation.Mode != "off" {
		return true
	}
	if contractErr == nil && contract.ContractID != "" && (contract.Profile == "large_project" || contract.Profile == "delegated" || len(contract.RequiredArtifacts) > 0) {
		return true
	}
	if goalErr == nil && goal.GoalID != "" {
		return true
	}
	if planModeErr == nil && planMode.PlanModeID != "" {
		return true
	}
	if len(artifacts) > 1 || len(tasks) > 0 {
		return true
	}
	return state.LastCompactionInputChars > 0
}

func checkpointParentWaitState(coordination session.ParentCoordination) string {
	hasUnresolved := len(coordination.UnresolvedChildSessions) > 0 || len(coordination.UnresolvedQueueJobs) > 0
	if !hasUnresolved {
		if coordination.WaitMode != "" {
			return "ready"
		}
		return ""
	}
	if coordination.Parked {
		return "parked"
	}
	return "waiting"
}

func latestCompactionArtifact(store *session.Store, sessionID string) (string, error) {
	relativePath, err := latestCompactionArtifactRelativePath(store, sessionID)
	if err != nil {
		return "", err
	}
	if relativePath == "" {
		return "", nil
	}
	return filepath.Join(store.SessionDir(sessionID), "artifacts", relativePath), nil
}

func latestProcessOwnerClue(eventsList []events.Event) (session.ProcessOwnerClue, bool, error) {
	for i := len(eventsList) - 1; i >= 0; i-- {
		evt := eventsList[i]
		if evt.Type != "webconsole.handle.acquired" && evt.Type != "webconsole.handle.released" {
			continue
		}
		handleState := "acquired"
		if evt.Type == "webconsole.handle.released" {
			handleState = "released"
		}
		owner := session.ProcessOwnerClue{
			Source:         eventString(evt.Data, "source"),
			HandleState:    handleState,
			EventType:      evt.Type,
			ProcessStartID: eventString(evt.Data, "process_start_id"),
			PID:            eventInt(evt.Data, "pid"),
			StartedAt:      eventString(evt.Data, "started_at"),
			ReleasedAt:     eventString(evt.Data, "released_at"),
			LastEventAt:    evt.Time,
		}
		if err := validateProcessOwnerClue(owner); err != nil {
			return session.ProcessOwnerClue{}, false, err
		}
		return owner, true, nil
	}
	return session.ProcessOwnerClue{}, false, nil
}

func validateProcessOwnerClue(owner session.ProcessOwnerClue) error {
	if owner.PID < 0 {
		return errors.New("recent owner pid must be non-negative")
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "started_at", value: owner.StartedAt},
		{name: "released_at", value: owner.ReleasedAt},
		{name: "last_event_at", value: owner.LastEventAt},
	} {
		if strings.TrimSpace(field.value) == "" {
			continue
		}
		if _, err := time.Parse(time.RFC3339Nano, field.value); err != nil {
			return fmt.Errorf("recent owner %s must be RFC3339Nano: %w", field.name, err)
		}
	}
	return nil
}

func eventString(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	value, _ := data[key].(string)
	return value
}

func eventInt(data map[string]any, key string) int {
	if data == nil {
		return 0
	}
	switch value := data[key].(type) {
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

func checkpointHints(checkpoint session.LongRunCheckpoint, state session.State) []string {
	var hints []string
	if len(checkpoint.RequiredArtifactStatus) > 0 {
		hints = append(hints, "recheck required artifacts before finish")
	}
	if checkpoint.ParentWaitState == "waiting" || checkpoint.ParentWaitState == "parked" {
		hints = append(hints, "resolve parent child or queue wait state")
	}
	if state.LastError != "" {
		hints = append(hints, "resume from last error: "+truncateText(state.LastError, 120))
	}
	if checkpoint.LatestCompactionArtifact != "" {
		hints = append(hints, "load latest compaction summary before broad reread")
	}
	if checkpoint.GoalSnapshot != nil {
		switch checkpoint.GoalSnapshot.Status {
		case session.GoalStatusActive:
			hints = append(hints, "audit active goal before finish")
		case session.GoalStatusBudgetLimited:
			hints = append(hints, "budget-limited goal needs wrap-up or explicit resume")
		case session.GoalStatusPaused:
			hints = append(hints, "goal is paused; wait for explicit resume or redirect")
		}
	}
	if checkpoint.PlanModeSnapshot != nil {
		switch checkpoint.PlanModeSnapshot.Status {
		case session.PlanModeStatusPlanning:
			hints = append(hints, "continue Plan Mode planning before execution")
		case session.PlanModeStatusAwaitingUserInput:
			hints = append(hints, "answer pending Plan Mode user input")
		case session.PlanModeStatusAwaitingApproval:
			hints = append(hints, "approve, revise, or cancel the pending Plan Mode plan")
		case session.PlanModeStatusApproved:
			hints = append(hints, "start execution from the approved Plan Mode plan")
		}
	}
	return hints
}

func goalCriterionCounts(items []session.GoalCriterion) (int, int) {
	verified := 0
	for _, item := range items {
		if item.Status == "verified" {
			verified++
		}
	}
	return verified, len(items)
}

func goalValidationCounts(items []session.GoalValidation) (int, int) {
	verified := 0
	for _, item := range items {
		if item.Status == "verified" {
			verified++
		}
	}
	return verified, len(items)
}

func appendCheckpointResumeHint(store *session.Store, meta session.SessionMetadata, provider, model string) (bool, []string, string, error) {
	checkpoint, err := store.LoadLongRunCheckpoint(meta.ID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil, "", nil
		}
		return false, nil, "", err
	}
	warnings, err := checkpointDriftWarnings(store, meta, checkpoint, provider, model)
	if err != nil {
		return false, nil, "", err
	}
	if !shouldInjectCheckpointResumeHint(checkpoint, warnings) {
		return false, warnings, "", nil
	}
	text := "Harness resume note: a long-run checkpoint is available. Use durable session facts first"
	if len(checkpoint.ResumeHints) > 0 {
		text += "; hints: " + strings.Join(checkpoint.ResumeHints, "; ")
	}
	if len(warnings) > 0 {
		text += "; drift warnings: " + strings.Join(warnings, "; ")
	}
	text += "."
	msg := session.NewMessage("user", text)
	msg.Meta = map[string]any{
		"source":                    "harness_reminder",
		"kind":                      "longrun_checkpoint",
		"drift_warnings":            append([]string(nil), warnings...),
		harnessReminderSignatureKey: harnessReminderTextSignature(text),
	}
	exists, err := harnessReminderExists(store, meta.ID, "longrun_checkpoint", harnessReminderTextSignature(text), text)
	if err != nil {
		return false, warnings, "", err
	}
	if exists {
		return false, warnings, "", nil
	}
	return true, warnings, msg.ID, store.AppendMessage(meta.ID, msg)
}

func shouldInjectCheckpointResumeHint(checkpoint session.LongRunCheckpoint, warnings []string) bool {
	if len(warnings) > 0 {
		return true
	}
	if (checkpoint.ParentWaitState == "waiting" || checkpoint.ParentWaitState == "parked") && (len(checkpoint.UnresolvedChildSessions) > 0 || len(checkpoint.UnresolvedQueueJobs) > 0) {
		return true
	}
	if checkpoint.LatestCompactionArtifact != "" {
		return true
	}
	if checkpoint.GoalSnapshot != nil {
		switch checkpoint.GoalSnapshot.Status {
		case session.GoalStatusBudgetLimited, session.GoalStatusPaused:
			return true
		}
	}
	if checkpoint.PlanModeSnapshot != nil {
		switch checkpoint.PlanModeSnapshot.Status {
		case session.PlanModeStatusPlanning, session.PlanModeStatusAwaitingUserInput, session.PlanModeStatusAwaitingApproval, session.PlanModeStatusApproved:
			return true
		}
	}
	for _, hint := range checkpoint.ResumeHints {
		hint = strings.TrimSpace(hint)
		if strings.HasPrefix(hint, "resume from last error:") {
			return true
		}
	}
	return false
}

func checkpointDriftWarnings(store *session.Store, meta session.SessionMetadata, checkpoint session.LongRunCheckpoint, provider, model string) ([]string, error) {
	var warnings []string
	if checkpoint.Provider != "" && checkpoint.Provider != provider {
		warnings = append(warnings, fmt.Sprintf("provider changed from %s to %s", checkpoint.Provider, provider))
	}
	if checkpoint.Model != "" && checkpoint.Model != model {
		warnings = append(warnings, fmt.Sprintf("model changed from %s to %s", checkpoint.Model, model))
	}
	if checkpoint.Workdir != "" && checkpoint.Workdir != meta.Workdir {
		warnings = append(warnings, fmt.Sprintf("workdir changed from %s to %s", checkpoint.Workdir, meta.Workdir))
	}
	if checkpoint.RequestedWorkdir != "" && checkpoint.RequestedWorkdir != meta.RequestedWorkdir {
		warnings = append(warnings, fmt.Sprintf("requested workdir changed from %s to %s", checkpoint.RequestedWorkdir, meta.RequestedWorkdir))
	}
	if warning := isolationDriftWarning(checkpoint.Isolation, meta.Isolation); warning != "" {
		warnings = append(warnings, warning)
	}
	if checkpoint.ContractSnapshot != nil {
		if current, err := store.LoadContract(meta.ID); err == nil && current.ContractID != "" {
			if checkpoint.ContractSnapshot.TrustSource != "" && current.TrustSource != "" && checkpoint.ContractSnapshot.TrustSource != current.TrustSource {
				warnings = append(warnings, fmt.Sprintf("trust source changed from %s to %s", checkpoint.ContractSnapshot.TrustSource, current.TrustSource))
			}
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("load contract.json for checkpoint drift: %w", err)
		} else if checkpoint.ContractSnapshot.TrustSource != "" {
			warnings = append(warnings, fmt.Sprintf("trust source changed from %s to missing current contract", checkpoint.ContractSnapshot.TrustSource))
		}
	}
	return warnings, nil
}

func isolationDriftWarning(previous, current *session.IsolationInfo) string {
	if previous == nil && current == nil {
		return ""
	}
	if previous == nil {
		return fmt.Sprintf("isolation changed from off to %s", current.Mode)
	}
	if current == nil {
		return fmt.Sprintf("isolation changed from %s to off", previous.Mode)
	}
	if previous.Mode != current.Mode {
		return fmt.Sprintf("isolation mode changed from %s to %s", previous.Mode, current.Mode)
	}
	if previous.RequestedMode != current.RequestedMode {
		return fmt.Sprintf("isolation requested mode changed from %s to %s", previous.RequestedMode, current.RequestedMode)
	}
	if previous.Workdir != current.Workdir {
		return fmt.Sprintf("isolation workdir changed from %s to %s", previous.Workdir, current.Workdir)
	}
	if previous.RootDir != current.RootDir {
		return fmt.Sprintf("isolation root changed from %s to %s", previous.RootDir, current.RootDir)
	}
	return ""
}
