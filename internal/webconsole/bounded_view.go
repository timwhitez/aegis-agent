package webconsole

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"go-cli-agent/internal/events"
	"go-cli-agent/internal/session"
)

const (
	webSummaryTextMaxBytes        = 1200
	webMessageTextMaxBytes        = 16 << 10
	webToolArgumentsMaxBytes      = 16 << 10
	webToolOutputMaxBytes         = 16 << 10
	webEventDataStringMaxBytes    = 8 << 10
	webTimelineTextMaxBytes       = 4 << 10
	webPlanMarkdownMaxBytes       = 32 << 10
	webOpaqueFactMaxBytes         = 512
	webFileChangeBackfillMaxBytes = 32 << 20
	webSafeMaxArrayItems          = 64
	webSafeMaxDepth               = 8
)

func webSafeOverviewResponse(resp OverviewResponse) OverviewResponse {
	resp.RecentSessions = webSafeSessionSummaries(resp.RecentSessions)
	resp.RecentJobs = webSafeQueueJobs(resp.RecentJobs)
	for i := range resp.RecentFailures {
		resp.RecentFailures[i].Message = truncateWebString(resp.RecentFailures[i].Message, webSummaryTextMaxBytes)
	}
	resp.Feed = webSafeTimelineEntries(resp.Feed)
	return resp
}

func webSafeHistoryResponse(resp HistoryResponse) HistoryResponse {
	resp.Items = webSafeSessionSummaries(resp.Items)
	return resp
}

func webSafeSessionDetailResponse(resp SessionDetailResponse) SessionDetailResponse {
	if resp.Goal != nil {
		goal := webSafeGoal(*resp.Goal)
		resp.Goal = &goal
	}
	if resp.GoalFacts != nil {
		facts := webSafeGoalFacts(*resp.GoalFacts)
		resp.GoalFacts = &facts
	}
	if resp.PlanMode != nil {
		planMode := webSafePlanMode(*resp.PlanMode)
		resp.PlanMode = &planMode
	}
	if resp.LongRunCheckpoint != nil {
		checkpoint := webSafeLongRunCheckpoint(*resp.LongRunCheckpoint)
		resp.LongRunCheckpoint = &checkpoint
	}
	resp.Children.Sessions = webSafeSessionSummaries(resp.Children.Sessions)
	resp.Children.Jobs = webSafeQueueJobs(resp.Children.Jobs)
	resp.BackgroundNotifications = webSafeBackgroundNotifications(resp.BackgroundNotifications)
	resp.SteerRequests = webSafeSteerRequests(resp.SteerRequests)
	resp.Messages = webSafeMessages(resp.Messages)
	resp.Events = webSafeEvents(resp.Events)
	resp.Timeline = webSafeTimelineEntries(resp.Timeline)
	resp.State.LastError = truncateWebString(resp.State.LastError, webSummaryTextMaxBytes)
	resp.State.LastAssistantExcerpt = truncateWebString(resp.State.LastAssistantExcerpt, webSummaryTextMaxBytes)
	return resp
}

func webSafeMessagesResponse(resp MessagesResponse) MessagesResponse {
	resp.Messages = webSafeMessages(resp.Messages)
	return resp
}

func webSafeSessionSummaries(items []session.SessionSummary) []session.SessionSummary {
	if items == nil {
		return nil
	}
	out := make([]session.SessionSummary, len(items))
	for i, item := range items {
		out[i] = webSafeSessionSummary(item)
	}
	return out
}

func webSafeSessionSummary(item session.SessionSummary) session.SessionSummary {
	item.LastError = truncateWebString(item.LastError, webSummaryTextMaxBytes)
	item.GoalObjective = truncateWebString(item.GoalObjective, webSummaryTextMaxBytes)
	item.PlanModeSummary = truncateWebString(item.PlanModeSummary, webSummaryTextMaxBytes)
	return item
}

func webSafeQueueJobs(items []session.QueueJob) []session.QueueJob {
	if items == nil {
		return nil
	}
	out := make([]session.QueueJob, len(items))
	for i, item := range items {
		out[i] = webSafeQueueJob(item)
	}
	return out
}

func webSafeQueueJob(item session.QueueJob) session.QueueJob {
	item.Prompt = truncateWebString(item.Prompt, webSummaryTextMaxBytes)
	item.SystemOverride = truncateWebString(item.SystemOverride, webSummaryTextMaxBytes)
	item.LastError = truncateWebString(item.LastError, webSummaryTextMaxBytes)
	item.FinalText = truncateWebString(item.FinalText, webSummaryTextMaxBytes)
	return item
}

func webSafeBackgroundNotifications(items []session.BackgroundNotification) []session.BackgroundNotification {
	if items == nil {
		return nil
	}
	out := make([]session.BackgroundNotification, len(items))
	for i, item := range items {
		item.FinalText = truncateWebString(item.FinalText, webSummaryTextMaxBytes)
		item.LastError = truncateWebString(item.LastError, webSummaryTextMaxBytes)
		out[i] = item
	}
	return out
}

func webSafeSteerRequests(items []session.SteerRequest) []session.SteerRequest {
	if items == nil {
		return nil
	}
	out := make([]session.SteerRequest, len(items))
	for i, item := range items {
		item.Text = truncateWebString(item.Text, webSummaryTextMaxBytes)
		out[i] = item
	}
	return out
}

func webSafeMessages(items []session.Message) []session.Message {
	if items == nil {
		return nil
	}
	out := make([]session.Message, len(items))
	for i, item := range items {
		out[i] = webSafeMessage(item)
	}
	return out
}

func webSafeMessage(item session.Message) session.Message {
	item.Text = truncateWebString(item.Text, webMessageTextMaxBytes)
	item.Thinking = truncateWebString(item.Thinking, webMessageTextMaxBytes)
	item.Meta = webSafeMap(item.Meta, webEventDataStringMaxBytes)
	if item.ProviderContentBlocks != nil {
		blocks := make([]session.ProviderContentBlock, len(item.ProviderContentBlocks))
		for i, block := range item.ProviderContentBlocks {
			block.Text = truncateWebString(block.Text, webMessageTextMaxBytes)
			block.Thinking = truncateWebString(block.Thinking, webMessageTextMaxBytes)
			block.Signature = truncateWebString(block.Signature, webOpaqueFactMaxBytes)
			block.Data = truncateWebString(block.Data, webOpaqueFactMaxBytes)
			block.ThoughtSignature = truncateWebString(block.ThoughtSignature, webOpaqueFactMaxBytes)
			if block.Summary != nil {
				summary := make([]string, len(block.Summary))
				for j, value := range block.Summary {
					summary[j] = truncateWebString(value, webSummaryTextMaxBytes)
				}
				block.Summary = summary
			}
			blocks[i] = block
		}
		item.ProviderContentBlocks = blocks
	}
	if item.ToolCalls != nil {
		calls := make([]session.ToolCall, len(item.ToolCalls))
		for i, call := range item.ToolCalls {
			call.Arguments = webSafeRawJSON(call.Arguments, webToolArgumentsMaxBytes)
			calls[i] = call
		}
		item.ToolCalls = calls
	}
	if item.ToolResults != nil {
		results := make([]session.ToolResult, len(item.ToolResults))
		for i, result := range item.ToolResults {
			result.LLMOutput = truncateWebString(result.LLMOutput, webToolOutputMaxBytes)
			result.DisplayOutput = truncateWebString(result.DisplayOutput, webToolOutputMaxBytes)
			result.Metadata = webSafeMap(result.Metadata, webEventDataStringMaxBytes)
			results[i] = result
		}
		item.ToolResults = results
	}
	return item
}

func webSafeEvents(items []events.Event) []events.Event {
	if items == nil {
		return nil
	}
	out := make([]events.Event, len(items))
	for i, item := range items {
		item.Data = webSafeMap(item.Data, webEventDataStringMaxBytes)
		out[i] = item
	}
	return out
}

func webSafeTimelineEntries(items []TimelineEntry) []TimelineEntry {
	if items == nil {
		return nil
	}
	out := make([]TimelineEntry, len(items))
	for i, item := range items {
		item.Text = truncateWebString(item.Text, webTimelineTextMaxBytes)
		item.Data = webSafeMap(item.Data, webEventDataStringMaxBytes)
		out[i] = item
	}
	return out
}

func webSafeGoal(goal session.SessionGoal) session.SessionGoal {
	goal.Objective = truncateWebString(goal.Objective, session.MaxGoalObjectiveChars)
	for i := range goal.SuccessCriteria {
		goal.SuccessCriteria[i].Text = truncateWebString(goal.SuccessCriteria[i].Text, webSummaryTextMaxBytes)
		goal.SuccessCriteria[i].Evidence = truncateStringSlice(goal.SuccessCriteria[i].Evidence, webSummaryTextMaxBytes)
	}
	for i := range goal.ValidationPlan {
		goal.ValidationPlan[i].Command = truncateWebString(goal.ValidationPlan[i].Command, webSummaryTextMaxBytes)
		goal.ValidationPlan[i].Artifact = truncateWebString(goal.ValidationPlan[i].Artifact, webSummaryTextMaxBytes)
		goal.ValidationPlan[i].Description = truncateWebString(goal.ValidationPlan[i].Description, webSummaryTextMaxBytes)
		goal.ValidationPlan[i].Evidence = truncateStringSlice(goal.ValidationPlan[i].Evidence, webSummaryTextMaxBytes)
		for j := range goal.ValidationPlan[i].EvaluatorEvidence {
			goal.ValidationPlan[i].EvaluatorEvidence[j].Summary = truncateWebString(goal.ValidationPlan[i].EvaluatorEvidence[j].Summary, webSummaryTextMaxBytes)
		}
	}
	if goal.CompletionAudit != nil {
		goal.CompletionAudit.Summary = truncateWebString(goal.CompletionAudit.Summary, webSummaryTextMaxBytes)
		goal.CompletionAudit.Evidence = truncateStringSlice(goal.CompletionAudit.Evidence, webSummaryTextMaxBytes)
	}
	goal.Progress = webSafeGoalProgress(goal.Progress)
	return goal
}

func webSafeGoalFacts(facts GoalFactsResponse) GoalFactsResponse {
	if facts.LatestHistory != nil {
		latest := *facts.LatestHistory
		latest.Data = webSafeMap(latest.Data, webEventDataStringMaxBytes)
		facts.LatestHistory = &latest
	}
	for i := range facts.History {
		facts.History[i].Data = webSafeMap(facts.History[i].Data, webEventDataStringMaxBytes)
	}
	facts.Progress = webSafeGoalProgress(facts.Progress)
	facts.LatestBlocker = truncateWebString(facts.LatestBlocker, webSummaryTextMaxBytes)
	return facts
}

func webSafeGoalProgress(items []session.GoalProgressRecord) []session.GoalProgressRecord {
	if items == nil {
		return nil
	}
	out := make([]session.GoalProgressRecord, len(items))
	for i, item := range items {
		item.Summary = truncateWebString(item.Summary, webSummaryTextMaxBytes)
		item.Evidence = truncateStringSlice(item.Evidence, webSummaryTextMaxBytes)
		item.LinkedArtifacts = truncateStringSlice(item.LinkedArtifacts, webSummaryTextMaxBytes)
		item.Blockers = truncateStringSlice(item.Blockers, webSummaryTextMaxBytes)
		for j := range item.Commands {
			item.Commands[j].Command = truncateWebString(item.Commands[j].Command, webSummaryTextMaxBytes)
			item.Commands[j].Artifact = truncateWebString(item.Commands[j].Artifact, webSummaryTextMaxBytes)
			item.Commands[j].Summary = truncateWebString(item.Commands[j].Summary, webSummaryTextMaxBytes)
		}
		out[i] = item
	}
	return out
}

func webSafePlanMode(plan session.PlanModeState) session.PlanModeState {
	plan.Objective = truncateWebString(plan.Objective, session.MaxPlanModeObjectiveChars)
	plan.PlanMarkdown = truncateWebString(plan.PlanMarkdown, webPlanMarkdownMaxBytes)
	plan.Summary = truncateWebString(plan.Summary, webSummaryTextMaxBytes)
	plan.Assumptions = truncateStringSlice(plan.Assumptions, webSummaryTextMaxBytes)
	plan.Risks = truncateStringSlice(plan.Risks, webSummaryTextMaxBytes)
	plan.Verification = truncateStringSlice(plan.Verification, webSummaryTextMaxBytes)
	if plan.PendingRequest != nil {
		request := *plan.PendingRequest
		for i := range request.Questions {
			request.Questions[i].Header = truncateWebString(request.Questions[i].Header, webSummaryTextMaxBytes)
			request.Questions[i].Question = truncateWebString(request.Questions[i].Question, webSummaryTextMaxBytes)
			for j := range request.Questions[i].Options {
				request.Questions[i].Options[j].Label = truncateWebString(request.Questions[i].Options[j].Label, webSummaryTextMaxBytes)
				request.Questions[i].Options[j].Description = truncateWebString(request.Questions[i].Options[j].Description, webSummaryTextMaxBytes)
			}
		}
		for i := range request.Answers {
			request.Answers[i].Label = truncateWebString(request.Answers[i].Label, webSummaryTextMaxBytes)
			request.Answers[i].Value = truncateWebString(request.Answers[i].Value, webSummaryTextMaxBytes)
		}
		plan.PendingRequest = &request
	}
	return plan
}

func webSafeLongRunCheckpoint(checkpoint session.LongRunCheckpoint) session.LongRunCheckpoint {
	if checkpoint.GoalSnapshot != nil {
		goal := webSafeGoal(*checkpoint.GoalSnapshot)
		checkpoint.GoalSnapshot = &goal
	}
	if checkpoint.PlanModeSnapshot != nil {
		plan := webSafePlanMode(*checkpoint.PlanModeSnapshot)
		checkpoint.PlanModeSnapshot = &plan
	}
	checkpoint.ResumeHints = truncateStringSlice(checkpoint.ResumeHints, webSummaryTextMaxBytes)
	for i := range checkpoint.TodoSummary {
		checkpoint.TodoSummary[i].Content = truncateWebString(checkpoint.TodoSummary[i].Content, webSummaryTextMaxBytes)
	}
	for i := range checkpoint.RequiredArtifactStatus {
		checkpoint.RequiredArtifactStatus[i].Status.ValidationIssues = truncateStringSlice(checkpoint.RequiredArtifactStatus[i].Status.ValidationIssues, webSummaryTextMaxBytes)
	}
	return checkpoint
}

func webSafeMap(input map[string]any, maxStringBytes int) map[string]any {
	if input == nil {
		return nil
	}
	value, _ := webSafeAny(input, maxStringBytes, webSafeMaxDepth).(map[string]any)
	if value == nil {
		return map[string]any{}
	}
	return value
}

func webSafeRawJSON(raw json.RawMessage, maxBytes int) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	if len(raw) <= maxBytes {
		return append(json.RawMessage(nil), raw...)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		quoted, _ := json.Marshal(truncateWebString(string(raw), maxBytes))
		return json.RawMessage(quoted)
	}
	safe := webSafeAny(decoded, maxBytes, webSafeMaxDepth)
	encoded, err := json.Marshal(safe)
	if err != nil || len(encoded) > maxBytes {
		encoded, _ = json.Marshal(map[string]any{
			"_webconsole_truncated": true,
			"preview":               truncateWebString(string(raw), maxBytes),
		})
	}
	return json.RawMessage(encoded)
}

func webSafeAny(value any, maxStringBytes, depth int) any {
	if depth <= 0 {
		return "[webconsole truncated nested data]"
	}
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return truncateWebString(typed, maxStringBytes)
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, nested := range typed {
			out[key] = webSafeAny(nested, maxStringBytes, depth-1)
		}
		return out
	case []any:
		limit := len(typed)
		truncated := 0
		if limit > webSafeMaxArrayItems {
			truncated = limit - webSafeMaxArrayItems
			limit = webSafeMaxArrayItems
		}
		out := make([]any, 0, limit+1)
		for i := 0; i < limit; i++ {
			out = append(out, webSafeAny(typed[i], maxStringBytes, depth-1))
		}
		if truncated > 0 {
			out = append(out, map[string]any{"_webconsole_truncated_items": truncated})
		}
		return out
	default:
		return value
	}
}

func truncateStringSlice(items []string, maxBytes int) []string {
	if items == nil {
		return nil
	}
	limit := len(items)
	if limit > webSafeMaxArrayItems {
		limit = webSafeMaxArrayItems
	}
	out := make([]string, 0, limit+1)
	for i := 0; i < limit; i++ {
		out = append(out, truncateWebString(items[i], maxBytes))
	}
	if len(items) > limit {
		out = append(out, fmt.Sprintf("[webconsole truncated %d additional items]", len(items)-limit))
	}
	return out
}

func truncateWebString(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	cut := maxBytes
	for cut > 0 && !utf8.ValidString(value[:cut]) {
		cut--
	}
	if cut == 0 {
		return fmt.Sprintf("[webconsole truncated %d bytes]", len(value))
	}
	return fmt.Sprintf("%s\n\n[webconsole truncated %d bytes]", value[:cut], len(value)-cut)
}
