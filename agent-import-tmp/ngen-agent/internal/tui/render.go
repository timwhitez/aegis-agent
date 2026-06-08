package tui

import (
	"fmt"
	"strings"

	"ngen/internal/task"

	"github.com/charmbracelet/lipgloss"
)

const maxVisibleQueuedPrompts = 3

func renderInspector(snapshot Snapshot, tab inspectorTab, width int, selectedWorker int) string {
	if width <= 12 {
		width = 12
	}
	switch tab {
	case tabPlan:
		return renderPlanSummary(snapshot.TaskView.Plan, snapshot.Sprint, width)
	case tabCriteria:
		return renderCriteriaSummary(snapshot.Criteria, width)
	case tabWorkers:
		return renderWorkersSummary(snapshot.Workers, selectedWorker, width)
	case tabBlockers:
		return renderBlockersSummary(snapshot, width)
	case tabMemory:
		return renderMemoryPreview(snapshot.MemoryMarkdown, width)
	case tabProject:
		return renderProjectSummary(snapshot, width)
	case tabTasks:
		content, _ := renderTaskNavigationSummary(snapshot, nil, nil, 0, width)
		return content
	default:
		return renderOverviewSummary(snapshot, width)
	}
}

func renderOverviewSummary(snapshot Snapshot, width int) string {
	status := snapshot.TaskView.Status
	lines := []string{"Task Summary"}
	lines = append(lines, wrapPrefixedLine(width, "", blankDash(snapshot.TaskView.Task.TaskID))...)
	if title := strings.TrimSpace(snapshot.TaskView.Task.Title); title != "" {
		lines = append(lines, wrapPrefixedLine(width, "", title)...)
	}
	lines = append(lines, wrapPrefixedLine(
		width,
		"",
		fmt.Sprintf("%s  ·  %s / %s", blankDash(string(snapshot.TaskView.Task.Kind)), blankDash(string(status.Phase)), blankDash(string(status.State))),
	)...)
	if parentTaskID := strings.TrimSpace(snapshot.TaskView.Task.ParentTaskID); parentTaskID != "" {
		lines = append(lines, wrapPrefixedLine(width, "", "parent "+parentTaskID)...)
	}
	if progress := renderOverviewProgressLines(status, width); len(progress) > 0 {
		lines = append(lines, "", "Progress")
		lines = append(lines, progress...)
	}
	if refs := renderOverviewRefLines(status, width); len(refs) > 0 {
		lines = append(lines, "", "Refs")
		lines = append(lines, refs...)
	}
	if summary := strings.TrimSpace(snapshot.Continuity.Summary); summary != "" {
		lines = append(lines, "", "Continuity", summary)
	}
	if summary := strings.TrimSpace(snapshot.Sprint.Summary); summary != "" {
		lines = append(lines, "", "Sprint", summary)
	}
	if pointers := renderOverviewPointerLines(snapshot.TaskView.Plan, status.CurrentStepID, width); len(pointers) > 0 {
		lines = append(lines, "", "Pointers")
		lines = append(lines, pointers...)
	}
	return wrapParagraphs(lines, width)
}

func renderOverviewProgressLines(status task.StatusSnapshot, width int) []string {
	var lines []string
	var summary []string
	if step := strings.TrimSpace(status.CurrentStepID); step != "" {
		summary = append(summary, "step "+step)
	}
	if status.PlanRevision > 0 {
		summary = append(summary, fmt.Sprintf("plan rev %d", status.PlanRevision))
	}
	if len(summary) > 0 {
		lines = append(lines, wrapPrefixedLine(width, "", strings.Join(summary, "  ·  "))...)
	}
	if reason := strings.TrimSpace(status.StatusReasonCode); reason != "" {
		lines = append(lines, wrapPrefixedLine(width, "", "reason "+reason)...)
	}
	return lines
}

func renderOverviewRefLines(status task.StatusSnapshot, width int) []string {
	type refLine struct {
		label string
		value string
	}
	refs := []refLine{
		{label: "detail", value: status.StatusDetailRef},
		{label: "verify", value: status.LastVerificationRef},
		{label: "review", value: status.LastReviewRef},
		{label: "done", value: status.CompletionRef},
	}
	var lines []string
	for _, ref := range refs {
		value := strings.TrimSpace(ref.value)
		if value == "" {
			continue
		}
		lines = append(lines, wrapPrefixedLine(width, ref.label+" ", value)...)
	}
	return lines
}

func renderOverviewPointerLines(plan task.Plan, currentStepID string, width int) []string {
	var lines []string
	if execution := strings.TrimSpace(plan.CurrentExecutionStepID); execution != "" {
		lines = append(lines, wrapPrefixedLine(width, "execution ", execution)...)
	}
	if system := strings.TrimSpace(plan.CurrentSystemStepID); system != "" {
		if plan.CurrentExecutionStepID == "" && system == strings.TrimSpace(currentStepID) {
			return lines
		}
		lines = append(lines, wrapPrefixedLine(width, "system ", system)...)
	}
	return lines
}

func renderPlanSummary(plan task.Plan, sprint task.SprintSnapshot, width int) string {
	if len(plan.Steps) == 0 {
		return "No plan yet."
	}
	metadata := []string{
		fmt.Sprintf("Revision: %d", plan.Revision),
		fmt.Sprintf("Current Execution: %s", blankDash(plan.CurrentExecutionStepID)),
		fmt.Sprintf("Current System: %s", blankDash(plan.CurrentSystemStepID)),
	}
	if sprint.PrimaryCriterionStatement != "" {
		metadata = append(metadata, fmt.Sprintf("Primary Criterion: %s", sprint.PrimaryCriterionStatement))
	}
	var lines []string
	for _, line := range metadata {
		lines = append(lines, wrapPrefixedLine(width, "", line)...)
	}
	lines = append(lines, "")
	for _, step := range plan.Steps {
		prefix := "-"
		switch step.Status {
		case task.StepStatusCompleted:
			prefix = "x"
		case task.StepStatusInProgress:
			prefix = ">"
		case task.StepStatusCancelled:
			prefix = "!"
		}
		lines = append(lines, wrapPrefixedLine(width, prefix+" ", fmt.Sprintf("%s [%s]", step.Title, blankDash(step.Status)))...)
		if step.Notes != "" {
			lines = append(lines, wrapPrefixedLine(width, "  ", step.Notes)...)
		}
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func renderWorkersSummary(workers []task.WorkerContract, selected int, width int) string {
	content, _ := renderWorkersSummaryWithStarts(workers, selected, width)
	return content
}

func renderWorkersSummaryWithStarts(workers []task.WorkerContract, selected int, width int) (string, []int) {
	if len(workers) == 0 {
		return "No workers.", nil
	}
	if selected < 0 {
		selected = 0
	}
	if selected >= len(workers) {
		selected = len(workers) - 1
	}
	var lines []string
	starts := make([]int, 0, len(workers))
	for i, worker := range workers {
		selectedRow := i == selected
		marker := " "
		if selectedRow {
			marker = ">"
		}
		starts = append(starts, len(lines))
		title := fmt.Sprintf("%s %s %s", marker, worker.WorkerID, worker.Role)
		lines = append(lines, renderOverlaySelectableLines(width, selectedRow, title)...)
		var metadata []string
		if status := strings.TrimSpace(worker.Status); status != "" {
			metadata = append(metadata, status)
		}
		if worker.ParentActionType != "" {
			metadata = append(metadata, "action "+worker.ParentActionType)
		}
		if worker.BlockedReasonCode != "" {
			metadata = append(metadata, "reason "+worker.BlockedReasonCode)
		}
		if len(metadata) > 0 {
			lines = append(lines, renderOverlaySelectableLines(width, selectedRow, "  "+strings.Join(metadata, "  ·  "))...)
		}
		if objective := strings.TrimSpace(worker.Objective); objective != "" {
			lines = append(lines, renderOverlaySelectableLines(width, selectedRow, "  objective: "+objective)...)
		}
		if worker.ResultSummary != "" {
			lines = append(lines, renderOverlaySelectableLines(width, selectedRow, "  "+worker.ResultSummary)...)
		}
		lines = append(lines, "")
	}
	lines = append(lines, wrapPrefixedLine(width, "", "Inspector focus + Enter continues a selected worker when ready.")...)
	return strings.TrimRight(strings.Join(lines, "\n"), "\n"), starts
}

func renderProjectSummary(snapshot Snapshot, width int) string {
	project := snapshot.Project
	focus := projectFocusFromSnapshot(snapshot)
	if len(project.Steps) == 0 && len(project.Branches) == 0 && strings.TrimSpace(project.Explanation) == "" && focus == nil {
		return "No workspace project graph yet."
	}
	lines := []string{"Project Summary"}
	if workspaceRoot := strings.TrimSpace(project.WorkspaceRoot); workspaceRoot != "" {
		lines = append(lines, wrapPrefixedLine(width, "", "Workspace Root: "+workspaceRoot)...)
	}
	var statusSummary []string
	if project.Revision > 0 {
		statusSummary = append(statusSummary, fmt.Sprintf("rev %d", project.Revision))
	}
	if currentStep := strings.TrimSpace(project.CurrentStepID); currentStep != "" {
		statusSummary = append(statusSummary, "current "+currentStep)
	}
	if mutationRef := strings.TrimSpace(project.LastMutationRef); mutationRef != "" {
		statusSummary = append(statusSummary, "mutation "+mutationRef)
	}
	if len(statusSummary) > 0 {
		lines = append(lines, wrapPrefixedLine(width, "", strings.Join(statusSummary, "  ·  "))...)
	}
	var laneSummary []string
	if ready := joinNonEmpty(project.ReadyStepIDs); ready != "" {
		laneSummary = append(laneSummary, "ready "+ready)
	}
	if blocked := joinNonEmpty(project.BlockedStepIDs); blocked != "" {
		laneSummary = append(laneSummary, "blocked "+blocked)
	}
	if active := joinNonEmpty(project.ActiveBranchIDs); active != "" {
		laneSummary = append(laneSummary, "active "+active)
	}
	if len(laneSummary) > 0 {
		lines = append(lines, wrapPrefixedLine(width, "", strings.Join(laneSummary, "  ·  "))...)
	}
	if explanation := strings.TrimSpace(project.Explanation); explanation != "" {
		lines = append(lines, "", "Explanation")
		lines = append(lines, wrapPrefixedLine(width, "", explanation)...)
	}
	if focus != nil {
		lines = append(lines, "", "Task Binding")
		var primarySummary []string
		if step := renderProjectBindingPrimary("step", focus.PrimaryStepID, focus.PrimaryStepTitle, focus.PrimaryStepStatus); step != "" {
			primarySummary = append(primarySummary, step)
		}
		if branch := renderProjectBindingPrimary("branch", focus.PrimaryBranchID, focus.PrimaryBranchTitle, focus.PrimaryBranchStatus); branch != "" {
			primarySummary = append(primarySummary, branch)
		}
		if len(primarySummary) > 0 {
			lines = append(lines, wrapPrefixedLine(width, "", strings.Join(primarySummary, "  ·  "))...)
		}
		var bindingSummary []string
		if parentStep := strings.TrimSpace(focus.ParentStepID); parentStep != "" {
			bindingSummary = append(bindingSummary, "parent "+parentStep)
		}
		if priority := strings.TrimSpace(focus.Priority); priority != "" {
			bindingSummary = append(bindingSummary, "priority "+priority)
		}
		if !focus.DependenciesSatisfied {
			bindingSummary = append(bindingSummary, "deps unmet")
		}
		if len(bindingSummary) > 0 {
			lines = append(lines, wrapPrefixedLine(width, "", strings.Join(bindingSummary, "  ·  "))...)
		}
		focusLists := []struct {
			label string
			value string
		}{
			{label: "bound steps", value: joinNonEmpty(focus.BoundStepIDs)},
			{label: "bound branches", value: joinNonEmpty(focus.BoundBranchIDs)},
			{label: "depends on", value: joinNonEmpty(focus.DependsOnStepIDs)},
			{label: "unmet deps", value: joinNonEmpty(focus.UnmetDependencyStepIDs)},
			{label: "ready steps", value: joinNonEmpty(focus.ReadyProjectStepIDs)},
			{label: "blocked steps", value: joinNonEmpty(focus.BlockedProjectStepIDs)},
			{label: "active branches", value: joinNonEmpty(focus.ActiveProjectBranchIDs)},
			{label: "refs", value: joinNonEmpty(focus.Refs)},
		}
		for _, item := range focusLists {
			if item.value == "" {
				continue
			}
			lines = append(lines, wrapPrefixedLine(width, "", item.label+" "+item.value)...)
		}
		if notes := strings.TrimSpace(focus.Notes); notes != "" {
			lines = append(lines, wrapPrefixedLine(width, "", "notes "+notes)...)
		}
		for _, link := range focus.DependencySteps {
			lines = append(lines, wrapPrefixedLine(width, "- ", fmt.Sprintf("dep %s [%s] %s", blankDash(link.StepID), blankDash(link.Status), blankDash(link.Title)))...)
		}
		for _, link := range focus.DownstreamSteps {
			lines = append(lines, wrapPrefixedLine(width, "- ", fmt.Sprintf("downstream %s [%s] %s", blankDash(link.StepID), blankDash(link.Status), blankDash(link.Title)))...)
		}
	}
	if len(project.Steps) > 0 {
		lines = append(lines, "", "Project Steps")
		for _, step := range project.Steps {
			lines = append(lines, wrapPrefixedLine(width, "", fmt.Sprintf("%s %s [%s] %s", projectStepMarker(step.Status), blankDash(step.ID), blankDash(step.Status), blankDash(step.Title)))...)
			lines = append(lines, wrapPrefixedLine(width, "  ", fmt.Sprintf("branch=%s  task=%s  priority=%s", blankDash(step.BranchID), blankDash(step.TaskID), blankDash(step.Priority)))...)
			if step.ParentStepID != "" {
				lines = append(lines, wrapPrefixedLine(width, "  ", fmt.Sprintf("parent=%s", step.ParentStepID))...)
			}
			if len(step.DependsOn) > 0 {
				lines = append(lines, wrapPrefixedLine(width, "  ", fmt.Sprintf("depends_on=%s", joinOrDash(step.DependsOn)))...)
			}
			if step.Notes != "" {
				lines = append(lines, wrapPrefixedLine(width, "  ", step.Notes)...)
			}
			lines = append(lines, "")
		}
	}
	if len(project.Branches) > 0 {
		lines = append(lines, "Branches")
		for _, branch := range project.Branches {
			lines = append(lines, wrapPrefixedLine(width, "", fmt.Sprintf("%s %s [%s] %s", projectBranchMarker(branch.Status), blankDash(branch.ID), blankDash(branch.Status), blankDash(branch.Title)))...)
			lines = append(lines, wrapPrefixedLine(width, "  ", fmt.Sprintf("task=%s  task_ref=%s", blankDash(branch.TaskID), blankDash(branch.TaskRef)))...)
			if branch.StatusRef != "" || branch.HandoffRef != "" || branch.WorkspaceRoot != "" {
				lines = append(lines, wrapPrefixedLine(width, "  ", fmt.Sprintf("status_ref=%s  handoff_ref=%s  workspace=%s", blankDash(branch.StatusRef), blankDash(branch.HandoffRef), blankDash(branch.WorkspaceRoot)))...)
			}
			if branch.LastReasonCode != "" {
				lines = append(lines, wrapPrefixedLine(width, "  ", fmt.Sprintf("reason=%s", branch.LastReasonCode))...)
			}
			if branch.Notes != "" {
				lines = append(lines, wrapPrefixedLine(width, "  ", branch.Notes)...)
			}
			lines = append(lines, "")
		}
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func renderBlockersSummary(snapshot Snapshot, width int) string {
	var lines []string
	pendingLocal := pendingApprovalRecords(snapshot.Approvals)
	pendingOwned := pendingApprovalRecords(snapshot.OwnedApprovals)
	if len(pendingLocal) > 0 {
		lines = append(lines, "Task Approvals")
		for _, record := range pendingLocal {
			lines = append(lines, renderCompactBlockerEntry(
				width,
				record.ApprovalID+" "+blankDash(record.Scope),
				[]string{blankDash(record.Status)},
				firstNonBlank("reason "+strings.TrimSpace(record.Reason)),
			)...)
		}
		lines = append(lines, "")
	}
	if len(pendingOwned) > 0 {
		lines = append(lines, "Owned Child Approvals")
		for _, record := range pendingOwned {
			metadata := []string{blankDash(record.Status)}
			if record.OwnerWorkerID != "" {
				metadata = append(metadata, "worker "+record.OwnerWorkerID)
			}
			var detailParts []string
			if reason := strings.TrimSpace(record.Reason); reason != "" {
				detailParts = append(detailParts, "reason "+reason)
			}
			if worker, ok := findWorkerByID(snapshot.Workers, record.OwnerWorkerID); ok {
				if worker.Status != "" {
					metadata = append(metadata, "child "+worker.Status)
				}
				if worker.BlockedReasonCode != "" {
					detailParts = append(detailParts, "blocked "+worker.BlockedReasonCode)
				}
			}
			lines = append(lines, renderCompactBlockerEntry(
				width,
				record.ApprovalID+" "+blankDash(record.Scope),
				metadata,
				strings.Join(detailParts, "  ·  "),
			)...)
		}
		lines = append(lines, "")
	}
	if input, ok := pendingInputRecord(snapshot.Inputs); ok {
		lines = append(lines, "Pending Input")
		metadata := []string{blankDash(input.Status)}
		if field := strings.TrimSpace(input.Field); field != "" {
			metadata = append(metadata, "field "+field)
		}
		lines = append(lines, renderCompactBlockerEntry(
			width,
			input.RequestID+" "+blankDash(input.Prompt),
			metadata,
			"",
		)...)
		lines = append(lines, "")
	}
	if snapshot.TaskView.Status.StatusReasonCode == "waiting_watch" {
		lines = append(lines, "Watch")
		lines = append(lines, renderCompactBlockerEntry(
			width,
			"Waiting for watch trigger",
			[]string{"reason " + snapshot.TaskView.Status.StatusReasonCode},
			firstNonBlank("detail "+strings.TrimSpace(snapshot.TaskView.Status.StatusDetailRef)),
		)...)
		lines = append(lines, "")
	}
	if len(lines) == 0 {
		return "No current blockers."
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func renderCompactBlockerEntry(width int, title string, metadata []string, detail string) []string {
	lines := wrapPrefixedLine(width, "- ", title)
	if compact := strings.TrimSpace(strings.Join(metadata, "  ·  ")); compact != "" {
		lines = append(lines, wrapPrefixedLine(width, "  ", compact)...)
	}
	if detail = strings.TrimSpace(detail); detail != "" {
		lines = append(lines, wrapPrefixedLine(width, "  ", detail)...)
	}
	return lines
}

func renderHeader(snapshot Snapshot, session task.Session, providerMode string, focus focusTarget, running bool, queued int, backDepth int, pendingOpen pendingTaskOpenState, width int) string {
	if width <= 0 {
		width = 1
	}
	status := snapshot.TaskView.Status
	left := fmt.Sprintf("ngen tui  %s  %s", blankDash(snapshot.TaskView.Task.TaskID), blankDash(snapshot.TaskView.Task.Title))
	var lines []string
	lines = append(lines, renderStyledWrapped(headerStyle, left, width)...)
	lines = append(lines, renderHeaderBadgeLines(status, session, providerMode, running, queued, backDepth, pendingOpen, width)...)
	if details := renderHeaderDetails(status, focus, width); len(details) > 0 {
		lines = append(lines, details...)
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

type headerBadge struct {
	text     string
	style    lipgloss.Style
	priority int
}

func renderHeaderBadgeLines(status task.StatusSnapshot, session task.Session, providerMode string, running bool, queued int, backDepth int, pendingOpen pendingTaskOpenState, width int) []string {
	badges := buildHeaderBadges(status, session, providerMode, running, queued, backDepth, pendingOpen)
	if len(badges) == 0 {
		return nil
	}
	visible := collapseHeaderBadgesToWidth(badges, width)
	return renderPackedHeaderBadges(visible, width)
}

func buildHeaderBadges(status task.StatusSnapshot, session task.Session, providerMode string, running bool, queued int, backDepth int, pendingOpen pendingTaskOpenState) []headerBadge {
	badges := []headerBadge{
		{
			text:     fmt.Sprintf("%s / %s", blankDash(string(status.Phase)), blankDash(string(status.State))),
			style:    headerStateBadgeStyle,
			priority: 100,
		},
	}
	if taskID := strings.TrimSpace(pendingOpen.TargetTaskID); taskID != "" {
		badges = append(badges, headerBadge{
			text:     "Switching -> " + taskID,
			style:    switchBadgeStyle,
			priority: 95,
		})
	}
	if running {
		badges = append(badges, headerBadge{
			text:     "Running",
			style:    runningBadgeStyle,
			priority: 90,
		})
	}
	if queued > 0 {
		badges = append(badges, headerBadge{
			text:     fmt.Sprintf("Queued %d", queued),
			style:    queueBadgeStyle,
			priority: 80,
		})
	}
	if backDepth > 0 {
		badges = append(badges, headerBadge{
			text:     fmt.Sprintf("Back %d", backDepth),
			style:    headerMetaBadgeStyle,
			priority: 40,
		})
	}
	if mode := strings.TrimSpace(providerMode); mode != "" {
		badges = append(badges, headerBadge{
			text:     mode,
			style:    headerMetaBadgeStyle,
			priority: 30,
		})
	}
	if compactSessionID := compactHeaderSessionID(session.SessionID); compactSessionID != "" {
		badges = append(badges, headerBadge{
			text:     compactSessionID,
			style:    headerMetaBadgeStyle,
			priority: 20,
		})
	}
	return badges
}

func collapseHeaderBadgesToWidth(badges []headerBadge, width int) []headerBadge {
	if len(badges) == 0 {
		return nil
	}
	visible := append([]headerBadge{}, badges...)
	for len(visible) > 1 && totalHeaderBadgeWidth(visible) > width {
		drop := -1
		lowestPriority := visible[0].priority
		for i := len(visible) - 1; i >= 1; i-- {
			if visible[i].priority <= lowestPriority {
				lowestPriority = visible[i].priority
				drop = i
			}
		}
		if drop == -1 {
			break
		}
		visible = append(visible[:drop], visible[drop+1:]...)
	}
	return visible
}

func totalHeaderBadgeWidth(badges []headerBadge) int {
	total := 0
	for i, badge := range badges {
		if i > 0 {
			total++
		}
		total += lipgloss.Width(badge.style.Render(badge.text))
	}
	return total
}

func renderPackedHeaderBadges(badges []headerBadge, width int) []string {
	if len(badges) == 0 {
		return nil
	}
	if width <= 0 {
		width = 1
	}
	lines := make([]string, 0, len(badges))
	current := make([]string, 0, len(badges))
	currentWidth := 0
	flush := func() {
		if len(current) == 0 {
			return
		}
		lines = append(lines, strings.Join(current, " "))
		current = current[:0]
		currentWidth = 0
	}
	for _, badge := range badges {
		rendered := badge.style.Render(badge.text)
		renderedWidth := lipgloss.Width(rendered)
		if renderedWidth > width {
			flush()
			lines = append(lines, renderStyledWrapped(badge.style, badge.text, width)...)
			continue
		}
		if len(current) == 0 {
			current = append(current, rendered)
			currentWidth = renderedWidth
			continue
		}
		if currentWidth+1+renderedWidth > width {
			flush()
		}
		current = append(current, rendered)
		if currentWidth == 0 {
			currentWidth = renderedWidth
		} else {
			currentWidth += 1 + renderedWidth
		}
	}
	flush()
	return lines
}

func compactHeaderSessionID(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	if idx := strings.LastIndex(sessionID, "-"); idx >= 0 && idx+1 < len(sessionID) {
		return "Session " + sessionID[idx+1:]
	}
	if len(sessionID) > 8 {
		return "Session " + sessionID[len(sessionID)-8:]
	}
	return "Session " + sessionID
}

func renderHeaderDetails(status task.StatusSnapshot, focus focusTarget, width int) []string {
	parts := make([]string, 0, 2)
	if reason := strings.TrimSpace(status.StatusReasonCode); reason != "" {
		parts = append(parts, "Reason "+reason)
	}
	if focus != focusComposer {
		parts = append(parts, "Focus "+focus.String())
	}
	if len(parts) == 0 {
		return nil
	}
	return renderStyledWrapped(subtleStyle, strings.Join(parts, "  ·  "), width)
}

type taskNavigationTarget struct {
	TaskID           string
	Title            string
	Kind             string
	Phase            string
	State            string
	StatusReasonCode string
	Summary          string
	Sources          []string
}

func renderTaskNavigationSummary(snapshot Snapshot, taskList []task.TaskListEntry, history []string, selected int, width int) (string, []int) {
	targets := buildTaskNavigationTargets(snapshot, taskList)
	if selected < 0 {
		selected = 0
	}
	if selected >= len(targets) {
		selected = len(targets) - 1
	}
	status := snapshot.TaskView.Status
	lines := []string{"Current Task"}
	lines = append(lines, wrapPrefixedLine(width, "", blankDash(snapshot.TaskView.Task.TaskID))...)
	if title := strings.TrimSpace(snapshot.TaskView.Task.Title); title != "" {
		lines = append(lines, wrapPrefixedLine(width, "", title)...)
	}
	currentSummary := []string{
		fmt.Sprintf("%s / %s", blankDash(string(status.Phase)), blankDash(string(status.State))),
		blankDash(string(snapshot.TaskView.Task.Kind)),
	}
	if reason := strings.TrimSpace(status.StatusReasonCode); reason != "" {
		currentSummary = append(currentSummary, "reason "+reason)
	}
	lines = append(lines, wrapPrefixedLine(width, "", strings.Join(currentSummary, "  ·  "))...)
	if snapshot.TaskView.Task.ParentTaskID != "" {
		lines = append(lines, wrapPrefixedLine(width, "", fmt.Sprintf("Parent Task: %s", snapshot.TaskView.Task.ParentTaskID))...)
	}
	starts := make([]int, 0, len(targets))
	lines = append(lines, "", "Related Tasks")
	if len(targets) == 0 {
		lines = append(lines, wrapPrefixedLine(width, "", "No related tasks discovered from parent, worker, or project truth.")...)
	} else {
		for i, target := range targets {
			selectedRow := i == selected
			marker := " "
			if selectedRow {
				marker = ">"
			}
			starts = append(starts, len(lines))
			title := strings.TrimSpace(target.Title)
			if title == "" {
				title = target.TaskID
			}
			lines = append(lines, renderOverlaySelectableLines(width, selectedRow, fmt.Sprintf("%s %s %s", marker, target.TaskID, title))...)
			if metadata := renderRelatedTaskMetadata(target); metadata != "" {
				lines = append(lines, renderOverlaySelectableLines(width, selectedRow, "  "+metadata)...)
			}
			if detail := renderRelatedTaskDetail(target); detail != "" {
				lines = append(lines, renderOverlaySelectableLines(width, selectedRow, "  "+detail)...)
			}
			lines = append(lines, "")
		}
	}
	if len(history) > 0 {
		lines = append(lines, "Navigation History")
		for i := len(history) - 1; i >= 0; i-- {
			lines = append(lines, wrapPrefixedLine(width, "- ", history[i])...)
		}
		lines = append(lines, wrapPrefixedLine(width, "", "Use /back to return to the most recent task in this stack.")...)
	} else {
		lines = append(lines, wrapPrefixedLine(width, "", "Use /back after opening another task to return here.")...)
	}
	lines = append(lines, wrapPrefixedLine(width, "", "Inspector focus + Enter opens the selected related task.")...)
	return strings.TrimRight(strings.Join(lines, "\n"), "\n"), starts
}

func buildTaskNavigationTargets(snapshot Snapshot, taskList []task.TaskListEntry) []taskNavigationTarget {
	index := make(map[string]task.TaskListEntry, len(taskList))
	for _, entry := range taskList {
		index[entry.TaskID] = entry
	}
	currentTaskID := strings.TrimSpace(snapshot.TaskView.Task.TaskID)
	order := make([]string, 0, 8)
	targets := make(map[string]*taskNavigationTarget)
	ensure := func(taskID string) *taskNavigationTarget {
		taskID = strings.TrimSpace(taskID)
		if taskID == "" || taskID == currentTaskID {
			return nil
		}
		if existing, ok := targets[taskID]; ok {
			return existing
		}
		target := &taskNavigationTarget{TaskID: taskID}
		if entry, ok := index[taskID]; ok {
			applyTaskNavigationEntry(target, entry)
		}
		targets[taskID] = target
		order = append(order, taskID)
		return target
	}
	addSource := func(target *taskNavigationTarget, source string) {
		source = strings.TrimSpace(source)
		if target == nil || source == "" {
			return
		}
		for _, existing := range target.Sources {
			if existing == source {
				return
			}
		}
		target.Sources = append(target.Sources, source)
	}
	if target := ensure(snapshot.TaskView.Task.ParentTaskID); target != nil {
		addSource(target, "parent task")
	}
	for _, worker := range snapshot.Workers {
		target := ensure(worker.ChildTaskID)
		if target == nil {
			continue
		}
		if target.Kind == "" {
			target.Kind = worker.Role
		}
		if target.State == "" {
			target.State = worker.Status
		}
		if target.Title == "" {
			target.Title = worker.Objective
		}
		switch {
		case strings.TrimSpace(worker.ParentActionSummary) != "" && strings.TrimSpace(worker.Objective) != "":
			target.Summary = strings.TrimSpace(worker.ParentActionSummary + " objective: " + worker.Objective)
		case strings.TrimSpace(worker.ParentActionSummary) != "":
			target.Summary = worker.ParentActionSummary
		case strings.TrimSpace(worker.ResultSummary) != "" && strings.TrimSpace(worker.Objective) != "":
			target.Summary = strings.TrimSpace(worker.ResultSummary + " objective: " + worker.Objective)
		case strings.TrimSpace(worker.ResultSummary) != "":
			target.Summary = worker.ResultSummary
		case strings.TrimSpace(worker.Objective) != "":
			target.Summary = "objective: " + worker.Objective
		}
		source := fmt.Sprintf("worker %s %s", worker.WorkerID, worker.Role)
		if worker.ParentActionType != "" {
			source += " " + worker.ParentActionType
		}
		addSource(target, source)
	}
	for _, step := range snapshot.Project.Steps {
		target := ensure(step.TaskID)
		if target == nil {
			continue
		}
		if target.Title == "" {
			target.Title = step.Title
		}
		if target.Summary == "" {
			target.Summary = fmt.Sprintf("project step %s [%s] %s", blankDash(step.ID), blankDash(step.Status), blankDash(step.Title))
		}
		addSource(target, fmt.Sprintf("project step %s [%s]", blankDash(step.ID), blankDash(step.Status)))
	}
	for _, branch := range snapshot.Project.Branches {
		target := ensure(branch.TaskID)
		if target == nil {
			continue
		}
		if target.Title == "" {
			target.Title = branch.Title
		}
		if target.Summary == "" {
			target.Summary = fmt.Sprintf("project branch %s [%s] %s", blankDash(branch.ID), blankDash(branch.Status), blankDash(branch.Title))
		}
		addSource(target, fmt.Sprintf("project branch %s [%s]", blankDash(branch.ID), blankDash(branch.Status)))
	}
	if focus := projectFocusFromSnapshot(snapshot); focus != nil {
		for _, link := range focus.DependencySteps {
			target := ensure(link.TaskID)
			if target == nil {
				continue
			}
			if target.Title == "" {
				target.Title = firstNonBlank(link.Title, link.BranchTitle)
			}
			if target.Summary == "" {
				target.Summary = fmt.Sprintf("dependency step %s [%s] %s", blankDash(link.StepID), blankDash(link.Status), blankDash(firstNonBlank(link.Title, link.BranchTitle)))
			}
			addSource(target, fmt.Sprintf("dependency %s [%s]", blankDash(link.StepID), blankDash(link.Status)))
		}
		for _, link := range focus.DownstreamSteps {
			target := ensure(link.TaskID)
			if target == nil {
				continue
			}
			if target.Title == "" {
				target.Title = firstNonBlank(link.Title, link.BranchTitle)
			}
			if target.Summary == "" {
				target.Summary = fmt.Sprintf("downstream step %s [%s] %s", blankDash(link.StepID), blankDash(link.Status), blankDash(firstNonBlank(link.Title, link.BranchTitle)))
			}
			addSource(target, fmt.Sprintf("downstream %s [%s]", blankDash(link.StepID), blankDash(link.Status)))
		}
	}
	out := make([]taskNavigationTarget, 0, len(order))
	for _, taskID := range order {
		target := targets[taskID]
		if target == nil {
			continue
		}
		if entry, ok := index[taskID]; ok {
			applyTaskNavigationEntry(target, entry)
		}
		out = append(out, *target)
	}
	return out
}

func applyTaskNavigationEntry(target *taskNavigationTarget, entry task.TaskListEntry) {
	if target == nil {
		return
	}
	if target.Title == "" {
		target.Title = entry.Title
	}
	if target.Kind == "" {
		target.Kind = string(entry.Kind)
	}
	if target.Phase == "" {
		target.Phase = string(entry.Phase)
	}
	if target.State == "" {
		target.State = string(entry.State)
	}
	if target.StatusReasonCode == "" {
		target.StatusReasonCode = entry.StatusReasonCode
	}
}

func renderRelatedTaskMetadata(target taskNavigationTarget) string {
	var parts []string
	if kind := strings.TrimSpace(target.Kind); kind != "" {
		parts = append(parts, kind)
	}
	phase := strings.TrimSpace(target.Phase)
	state := strings.TrimSpace(target.State)
	switch {
	case phase != "" && state != "":
		parts = append(parts, phase+" / "+state)
	case phase != "":
		parts = append(parts, phase)
	case state != "":
		parts = append(parts, "state "+state)
	}
	if reason := strings.TrimSpace(target.StatusReasonCode); reason != "" {
		parts = append(parts, "reason "+reason)
	}
	return strings.Join(parts, "  ·  ")
}

func renderRelatedTaskDetail(target taskNavigationTarget) string {
	var parts []string
	if summary := strings.TrimSpace(target.Summary); summary != "" {
		parts = append(parts, summary)
	}
	if sources := joinCompactRelatedTaskSources(target.Sources); sources != "" {
		parts = append(parts, "from "+sources)
	}
	return strings.Join(parts, "  ·  ")
}

func joinCompactRelatedTaskSources(sources []string) string {
	if len(sources) == 0 {
		return ""
	}
	items := make([]string, 0, len(sources))
	for _, source := range sources {
		source = strings.TrimSpace(source)
		if source == "" {
			continue
		}
		items = append(items, source)
	}
	return strings.Join(items, "; ")
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func renderTabs(active inspectorTab, width int) string {
	tabs := make([]string, 0, len(allInspectorTabs))
	for _, tab := range allInspectorTabs {
		label := tab.String()
		if tab == active {
			tabs = append(tabs, tabActiveStyle.Render(label))
			continue
		}
		tabs = append(tabs, tabInactiveStyle.Render(label))
	}
	if width < 24 {
		return strings.Join(tabs, "\n")
	}
	if width < 40 {
		lines := make([]string, 0, (len(tabs)+1)/2)
		for i := 0; i < len(tabs); i += 2 {
			line := tabs[i]
			if i+1 < len(tabs) {
				line += " " + tabs[i+1]
			}
			lines = append(lines, line)
		}
		return strings.Join(lines, "\n")
	}
	return strings.Join(tabs, " ")
}

func renderFooter(width int, hintLine, statusLine, errorLine string) string {
	if width <= 0 {
		width = 1
	}
	lines := renderStyledWrapped(subtleStyle, hintLine, width)
	if errorLine != "" {
		lines = append(lines, renderStyledWrapped(errorStyle, errorLine, width)...)
	} else if statusLine != "" {
		lines = append(lines, renderStyledWrapped(subtleStyle, statusLine, width)...)
	}
	return strings.Join(lines, "\n")
}

func renderFooterMaxLines(width int, hintLine, statusLine, errorLine string, maxLines int) string {
	if maxLines <= 0 {
		return ""
	}
	if width <= 0 {
		width = 1
	}
	lines := renderStyledWrapped(subtleStyle, hintLine, width)
	if len(lines) >= maxLines {
		return strings.Join(lines[:maxLines], "\n")
	}
	var detailLines []string
	var detailStyle lipgloss.Style
	if errorLine != "" {
		detailLines = wrapPrefixedLine(width, "", errorLine)
		detailStyle = errorStyle
	} else if statusLine != "" {
		detailLines = wrapPrefixedLine(width, "", statusLine)
		detailStyle = subtleStyle
	}
	remaining := maxLines - len(lines)
	if len(detailLines) > remaining {
		detailLines = detailLines[:remaining]
		if remaining > 0 {
			detailLines[remaining-1] = truncateDisplayWidth(detailLines[remaining-1], max(width-3, 1)) + "..."
		}
	}
	for _, line := range detailLines {
		lines = append(lines, detailStyle.Render(line))
	}
	return strings.Join(lines, "\n")
}

type footerHintPart struct {
	text     string
	priority int
}

func joinFooterHintParts(parts []footerHintPart) string {
	normalized := normalizeFooterHintParts(parts)
	if len(normalized) == 0 {
		return ""
	}
	text := make([]string, 0, len(normalized))
	for _, part := range normalized {
		text = append(text, part.text)
	}
	return strings.Join(text, "  ")
}

func collapseFooterHintPartsToWidth(parts []footerHintPart, width int) string {
	if width <= 0 {
		return ""
	}
	visible := normalizeFooterHintParts(parts)
	if len(visible) <= 1 {
		return joinFooterHintParts(visible)
	}
	for len(visible) > 1 && totalFooterHintPartWidth(visible) > width {
		drop := -1
		lowestPriority := visible[0].priority
		for i := len(visible) - 1; i >= 0; i-- {
			if visible[i].priority <= lowestPriority {
				lowestPriority = visible[i].priority
				drop = i
			}
		}
		if drop == -1 {
			break
		}
		visible = append(visible[:drop], visible[drop+1:]...)
	}
	return joinFooterHintParts(visible)
}

func normalizeFooterHintParts(parts []footerHintPart) []footerHintPart {
	normalized := make([]footerHintPart, 0, len(parts))
	for _, part := range parts {
		text := strings.TrimSpace(part.text)
		if text == "" {
			continue
		}
		normalized = append(normalized, footerHintPart{text: text, priority: part.priority})
	}
	return normalized
}

func totalFooterHintPartWidth(parts []footerHintPart) int {
	total := 0
	for i, part := range parts {
		if i > 0 {
			total += 2
		}
		total += lipgloss.Width(part.text)
	}
	return total
}

func renderComposerTitle(m model, width int) string {
	if width <= 0 {
		width = 1
	}
	titleText := "Composer"
	title := titleStyle.Render(titleText)
	badges := composerTitleBadges(m)
	if len(badges) == 0 {
		return title
	}
	badgeLine := strings.Join(badges, " ")
	if titleText == "" {
		return badgeLine
	}
	if lipgloss.Width(title)+1+lipgloss.Width(badgeLine) <= width {
		return title + " " + badgeLine
	}
	return title + "\n" + badgeLine
}

func composerTitleBadges(m model) []string {
	badges := make([]string, 0, 4)
	if taskID := strings.TrimSpace(m.pendingTaskOpen.TargetTaskID); taskID != "" {
		badges = append(badges, switchBadgeStyle.Render("Switching -> "+taskID))
	}
	if m.running {
		badges = append(badges, runningBadgeStyle.Render("Running"))
	}
	if queued := len(m.queuedPrompts); queued > 0 {
		badges = append(badges, queueBadgeStyle.Render(fmt.Sprintf("Queued %d", queued)))
	}
	if len(pendingApprovalRecords(m.snapshot.Approvals))+len(pendingApprovalRecords(m.snapshot.OwnedApprovals)) > 0 {
		badges = append(badges, pendingBadgeStyle.Render("Approvals"))
	}
	if pendingInputRecordExists(m.snapshot) {
		badges = append(badges, pendingBadgeStyle.Render("Input"))
	}
	return badges
}

func renderComposerContext(m model, width int) string {
	var lines []string
	switch {
	case m.pendingTaskOpenActive():
		lines = append(lines, "Enter waits until the switch completes.")
	case len(pendingApprovalRecords(m.snapshot.Approvals))+len(pendingApprovalRecords(m.snapshot.OwnedApprovals)) > 0:
		lines = append(lines, "Next: approvals pending. Type approvals or press a.")
	case pendingInputRecordExists(m.snapshot):
		lines = append(lines, "Next: input required. Type input or press i.")
	case m.running && len(m.queuedPrompts) > 0:
		if m.opts.SimpleMode {
			lines = append(lines, "Queued: Enter edit, Backspace drop.")
		} else {
			lines = append(lines, "Enter edits selected. Backspace drops. Ctrl+P newest.")
		}
	case m.running:
		if m.opts.SimpleMode {
			lines = append(lines, "Type to queue next prompt.")
		} else {
			lines = append(lines, "Type to queue the next prompt.")
		}
	default:
		if len(m.composerQuickActions()) > 0 {
			lines = append(lines, "Quick: "+strings.Join(m.composerQuickActions(), "  "))
		}
	}
	return wrapParagraphs(lines, width)
}

func renderPendingTaskOpenBanner(m model, width int) string {
	taskID := strings.TrimSpace(m.pendingTaskOpen.TargetTaskID)
	if taskID == "" {
		return ""
	}
	return strings.Join(renderStyledWrapped(switchBadgeStyle, "Switching -> "+taskID, width), "\n")
}

func renderOverlaySelectableLines(width int, selected bool, text string) []string {
	lines := wrapPrefixedLine(width, "", text)
	if !selected {
		return lines
	}
	styled := make([]string, 0, len(lines))
	for _, line := range lines {
		styled = append(styled, overlaySelectedRowStyle.Render(line))
	}
	return styled
}

func renderQueuedPromptPreview(m model, width int) string {
	if len(m.queuedPrompts) == 0 {
		return ""
	}
	lines := make([]string, 0, maxVisibleQueuedPrompts+3)
	selected := m.queuedPromptPreviewIndex
	if selected < 0 {
		selected = 0
	}
	if selected >= len(m.queuedPrompts) {
		selected = len(m.queuedPrompts) - 1
	}
	start := 0
	if selected >= maxVisibleQueuedPrompts {
		start = selected - maxVisibleQueuedPrompts + 1
	}
	end := start + maxVisibleQueuedPrompts
	if end > len(m.queuedPrompts) {
		end = len(m.queuedPrompts)
	}
	visible := m.queuedPrompts[start:end]
	if start > 0 {
		lines = append(lines, fmt.Sprintf("+%d earlier", start))
	}
	for i, prompt := range visible {
		absoluteIndex := start + i
		marker := " "
		if absoluteIndex == selected {
			marker = ">"
		}
		prompt = strings.TrimSpace(prompt)
		if prompt == "" {
			prompt = "-"
		}
		lines = append(lines, renderOverlaySelectableLines(width, absoluteIndex == selected, fmt.Sprintf("%s %d. %s", marker, absoluteIndex+1, prompt))...)
	}
	if remaining := len(m.queuedPrompts) - end; remaining > 0 {
		lines = append(lines, fmt.Sprintf("+%d more", remaining))
	}
	lines = append(lines, "Enter edits selected. Backspace drops selected. Ctrl+P still pulls the newest queued prompt.")
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func renderComposerActionSuggestions(m model, width int) string {
	items := m.composerActionSuggestions()
	if len(items) == 0 {
		return ""
	}
	selected := m.composerActionIndex
	if selected < 0 {
		selected = 0
	}
	if selected >= len(items) {
		selected = len(items) - 1
	}
	lines := make([]string, 0, len(items)+1)
	for i, item := range items {
		marker := " "
		if i == selected {
			marker = ">"
		}
		lines = append(lines, renderOverlaySelectableLines(width, i == selected, fmt.Sprintf("%s %s [%s] %s", marker, item.Command, item.Tag, item.Description))...)
	}
	lines = append(lines, "Tab completes selected command. Enter waits until suggestions are resolved.")
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func wrapParagraphs(lines []string, width int) string {
	var out []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			continue
		}
		out = append(out, wrapPrefixedLine(width, "", line)...)
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

func padLeft(text string, width int) string {
	if width <= lipgloss.Width(text) {
		return text
	}
	return strings.Repeat(" ", width-lipgloss.Width(text)) + text
}

func truncateDisplayWidth(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(text) <= width {
		return text
	}
	var b strings.Builder
	for _, r := range text {
		next := b.String() + string(r)
		if lipgloss.Width(next) > width {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

func pickerEntryCurrentStep(entry task.TaskListEntry) string {
	switch {
	case strings.TrimSpace(entry.CurrentStepID) != "":
		return strings.TrimSpace(entry.CurrentStepID)
	case strings.TrimSpace(entry.CurrentExecutionStepID) != "":
		return strings.TrimSpace(entry.CurrentExecutionStepID)
	case strings.TrimSpace(entry.CurrentSystemStepID) != "":
		return strings.TrimSpace(entry.CurrentSystemStepID)
	default:
		return ""
	}
}

func renderPickerEntry(entry task.TaskListEntry, selected bool, width int) []string {
	if width <= 0 {
		width = 1
	}
	title := strings.TrimSpace(entry.Title)
	if title == "" {
		title = "-"
	}
	marker := " "
	if selected {
		marker = ">"
	}
	lines := renderOverlaySelectableLines(width, selected, fmt.Sprintf("%s %s %s", marker, entry.TaskID, title))

	metadata := []string{
		blankDash(string(entry.Kind)),
		fmt.Sprintf("%s/%s", blankDash(string(entry.Phase)), blankDash(string(entry.State))),
	}
	if updated := strings.TrimSpace(entry.UpdatedAt); updated != "" {
		metadata = append(metadata, "updated "+updated)
	}
	lines = append(lines, renderOverlaySelectableLines(width, selected, "  "+strings.Join(metadata, "  ·  "))...)

	var detail []string
	if step := pickerEntryCurrentStep(entry); step != "" {
		detail = append(detail, "step "+step)
	}
	if reason := strings.TrimSpace(entry.StatusReasonCode); reason != "" {
		detail = append(detail, "reason "+reason)
	}
	if len(detail) > 0 {
		lines = append(lines, renderOverlaySelectableLines(width, selected, "  "+strings.Join(detail, "  ·  "))...)
	}
	return lines
}

func renderPickerView(width, height int, tasks []task.TaskListEntry, selected int, filter, statusLine, errorLine string) string {
	title := titleStyle.Render("Task Picker")
	var bodyLines []string
	filter = strings.TrimSpace(filter)
	if filter == "" {
		bodyLines = append(bodyLines, "Filter: <all tasks>")
	} else {
		bodyLines = append(bodyLines, wrapPrefixedLine(max(width-10, 1), "", fmt.Sprintf("Filter: %s", filter))...)
	}
	bodyLines = append(bodyLines, "")
	if len(tasks) == 0 {
		if filter != "" && !strings.HasPrefix(filter, "/") {
			bodyLines = append(bodyLines, "No tasks match the current filter.")
			bodyLines = append(bodyLines, "Press Backspace to broaden the search or clear the filter.")
		} else {
			bodyLines = append(bodyLines, "No tasks found.")
			bodyLines = append(bodyLines, "Create one with `ngen task create ...` and reopen the TUI.")
		}
	} else {
		if selected < 0 {
			selected = 0
		}
		if selected >= len(tasks) {
			selected = len(tasks) - 1
		}
		for i, entry := range tasks {
			bodyLines = append(bodyLines, renderPickerEntry(entry, i == selected, max(width-10, 1))...)
			if i < len(tasks)-1 {
				bodyLines = append(bodyLines, "")
			}
		}
	}
	footer := renderFooter(max(width-2, 1), "Type to filter  Up/Down select  Enter open  Esc or /exit quit", statusLine, errorLine)
	if errorLine == "" && statusLine != "" {
		footer = renderFooter(max(width-2, 1), "Type to filter  Up/Down select  Enter open  Esc or /exit quit", statusLine, "")
	}
	panel := focusedBorderStyle.Width(max(width-4, 1)).Height(max(height-6, 1)).Render(title + "\n" + wrapParagraphs(bodyLines, max(width-10, 1)))
	return strings.Join([]string{title, panel, footer}, "\n")
}

func renderModalView(width, height int, m model) string {
	panelWidth := max(width, 1)
	panelHeight := max(height, 1)
	switch m.modal {
	case modalApprovals:
		return focusedBorderStyle.Width(max(panelWidth-2, 1)).Height(panelHeight).Render(titleStyle.Render("Approvals") + "\n" + renderApprovalsModal(m, panelWidth-4))
	case modalInput:
		return focusedBorderStyle.Width(max(panelWidth-2, 1)).Height(panelHeight).Render(titleStyle.Render("Input Request") + "\n" + renderInputModal(m, panelWidth-4))
	case modalHelp:
		return focusedBorderStyle.Width(max(panelWidth-2, 1)).Height(panelHeight).Render(titleStyle.Render("Help") + "\n" + renderHelpModalForMode(panelWidth-4, m.opts.SimpleMode))
	case modalActionPalette:
		return focusedBorderStyle.Width(max(panelWidth-2, 1)).Height(panelHeight).Render(titleStyle.Render("Action Palette") + "\n" + renderActionPaletteModal(m, panelWidth-4))
	case modalConfirmInterrupt:
		return focusedBorderStyle.Width(max(panelWidth-2, 1)).Height(panelHeight).Render(titleStyle.Render("Confirm Interrupt") + "\n" + renderConfirmModal(m, panelWidth-4))
	default:
		return focusedBorderStyle.Width(max(panelWidth-2, 1)).Height(panelHeight).Render(titleStyle.Render("Modal"))
	}
}

func renderActionPaletteModal(m model, width int) string {
	items := m.filteredActionPaletteItems()
	filter := strings.TrimSpace(m.actionFilter)
	var lines []string
	if filter == "" {
		lines = append(lines, "Filter: <all actions>")
	} else {
		lines = append(lines, wrapPrefixedLine(max(width-10, 1), "", fmt.Sprintf("Filter: %s", filter))...)
	}
	lines = append(lines, "")
	if len(items) == 0 {
		lines = append(lines, "No actions match the current filter.")
		lines = append(lines, "Try run, resume, review, actions, tasks, picker, or help.")
		return strings.TrimRight(strings.Join(lines, "\n"), "\n")
	}
	selected := m.actionIndex
	if selected < 0 {
		selected = 0
	}
	if selected >= len(items) {
		selected = len(items) - 1
	}
	for i, item := range items {
		marker := " "
		if i == selected {
			marker = ">"
		}
		summary := fmt.Sprintf("%s %s [%s] %s", marker, item.Label, item.Tag, item.Description)
		lines = append(lines, renderOverlaySelectableLines(width, i == selected, summary)...)
	}
	current := items[selected]
	lines = append(lines, "", "Selected")
	lines = append(lines, wrapPrefixedLine(width, "", fmt.Sprintf("Action: %s (%s)", current.Label, current.Command))...)
	lines = append(lines, wrapPrefixedLine(width, "", fmt.Sprintf("What it does: %s", current.Description))...)
	if len(current.Aliases) > 0 {
		lines = append(lines, wrapPrefixedLine(width, "", fmt.Sprintf("Also found by: %s", strings.Join(current.Aliases, ", ")))...)
	}
	if !current.Enabled && strings.TrimSpace(current.DisabledReason) != "" {
		lines = append(lines, wrapPrefixedLine(width, "", fmt.Sprintf("Unavailable: %s", current.DisabledReason))...)
	} else if current.Tag == "queue" {
		lines = append(lines, wrapPrefixedLine(width, "", "Behavior: this prompt will queue after the current turn settles.")...)
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func renderApprovalsModal(m model, width int) string {
	pending := append([]task.ApprovalRecord{}, pendingApprovalRecords(m.snapshot.Approvals)...)
	pending = append(pending, pendingApprovalRecords(m.snapshot.OwnedApprovals)...)
	if len(pending) == 0 {
		return "No pending approvals."
	}
	if m.approvalIndex >= len(pending) {
		return "No pending approvals."
	}
	var lines []string
	for i, record := range pending {
		selectedRow := i == m.approvalIndex
		marker := " "
		if selectedRow {
			marker = ">"
		}
		lines = append(lines, renderOverlaySelectableLines(width, selectedRow, fmt.Sprintf("%s %s [%s] %s", marker, record.ApprovalID, record.Status, blankDash(record.Scope)))...)
	}
	selected := pending[m.approvalIndex]
	lines = append(lines, "", "Selected")
	lines = append(lines, wrapPrefixedLine(width, "", fmt.Sprintf("Scope: %s", blankDash(selected.Scope)))...)
	lines = append(lines, wrapPrefixedLine(width, "", fmt.Sprintf("Reason: %s", blankDash(selected.Reason)))...)
	if selected.OwnerTaskID != "" {
		lines = append(lines, wrapPrefixedLine(width, "", fmt.Sprintf("Owner Task: %s", selected.OwnerTaskID))...)
	}
	if selected.OwnerWorkerID != "" {
		lines = append(lines, wrapPrefixedLine(width, "", fmt.Sprintf("Owner Worker: %s", selected.OwnerWorkerID))...)
		if worker, ok := findWorkerByID(m.snapshot.Workers, selected.OwnerWorkerID); ok {
			lines = append(lines, wrapPrefixedLine(width, "", fmt.Sprintf("Child Task: %s", blankDash(worker.ChildTaskID)))...)
			lines = append(lines, wrapPrefixedLine(width, "", fmt.Sprintf("Child State: %s", blankDash(worker.Status)))...)
			if worker.BlockedReasonCode != "" {
				lines = append(lines, wrapPrefixedLine(width, "", fmt.Sprintf("Blocked: %s", worker.BlockedReasonCode))...)
			}
		}
	}
	lines = append(lines, "", "Keys: a approve  d deny  Esc close")
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func renderInputModal(m model, width int) string {
	record, ok := pendingInputRecord(m.snapshot.Inputs)
	if !ok {
		return "No pending input request."
	}
	metadata := []string{
		fmt.Sprintf("Request: %s", record.RequestID),
		fmt.Sprintf("Field: %s", blankDash(record.Field)),
		fmt.Sprintf("Prompt: %s", blankDash(record.Prompt)),
	}
	inputBox := m.inputBox
	inputBox.SetWidth(max(width-lipgloss.Width(inputBox.Prompt), 1))
	inputBox.SetHeight(3)
	lines := []string{wrapParagraphs(metadata, width), "", inputBox.View(), "", wrapParagraphs([]string{"Keys: Enter submit  Esc close"}, width)}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func renderHelpModal(width int) string {
	return renderHelpModalForMode(width, false)
}

func renderHelpModalForMode(width int, simpleMode bool) string {
	if simpleMode {
		lines := []string{
			"Fast path",
			"Plain text submits to the coding agent. Running turns queue follow-up prompts.",
			"Local commands: /help /approvals /input /status /refresh /review /run /resume /mission PROMPT /goal PROMPT /clear /quit",
			"Ctrl+K actions  Ctrl+L refresh  Ctrl+J newline  Ctrl+G editor",
			"Ctrl+C interrupts an active turn and quits only when idle with an empty composer",
			"Ctrl+D quits only when idle with an empty composer",
			"Task switching and worker management stay in CLI, ACP, or Web management surfaces.",
		}
		return wrapParagraphs(lines, width)
	}
	lines := []string{
		"Fast path",
		"Plain-text aliases: actions run resume review tasks back help approvals input refresh",
		"Ctrl+K actions  Ctrl+O picker  Ctrl+T tasks  Ctrl+B back",
		"Slash input: type / for local commands, Up/Down selects, Tab completes, Enter waits",
		"Running turns: Enter queues, queued prompts stay visible, Ctrl+C interrupts",
		"Editing: Ctrl+J newline, Ctrl+G editor, Esc Esc previous prompt, Up/Down history when the draft is empty",
		"Browse: Tab focus, 1-8 tabs, j/k scroll, PgUp/PgDown page, transcript also accepts b/f",
		"Review and exit: Ctrl+R refreshes review, Ctrl+L refreshes the current snapshot",
		"Ctrl+C quits when idle or interrupts the active turn; Ctrl+D quits when the composer is empty and idle",
		"Esc hides the prompt overlay first; otherwise it opens interrupt confirmation while a turn is active",
		"",
		"Slash forms still work",
		"/actions  /help  /approvals  /input  /picker  /tasks  /back  /status  /refresh  /review  /run  /resume  /mission PROMPT  /goal PROMPT  /quit",
	}
	return wrapParagraphs(lines, width)
}

func renderConfirmModal(m model, width int) string {
	options := []string{"Keep waiting", "Interrupt current turn"}
	var lines []string
	lines = append(lines, "A prompt is currently running.")
	if queued := len(m.queuedPrompts); queued > 0 {
		lines = append(lines, fmt.Sprintf("Interrupting now will drop %d queued prompt(s).", queued))
	}
	lines = append(lines, "")
	for i, option := range options {
		marker := " "
		if i == m.confirmIndex {
			marker = ">"
		}
		lines = append(lines, marker+" "+option)
	}
	lines = append(lines, "", "Enter confirm  Esc close  Ctrl+C confirm")
	return wrapParagraphs(lines, width)
}

func findWorkerByID(workers []task.WorkerContract, workerID string) (task.WorkerContract, bool) {
	for _, worker := range workers {
		if worker.WorkerID == workerID {
			return worker, true
		}
	}
	return task.WorkerContract{}, false
}

func renderStyledWrapped(style lipgloss.Style, text string, width int) []string {
	raw := wrapPrefixedLine(width, "", text)
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		lines = append(lines, style.Render(line))
	}
	return lines
}

func pendingInputRecordExists(snapshot Snapshot) bool {
	_, ok := pendingInputRecord(snapshot.Inputs)
	return ok
}

func projectFocusFromSnapshot(snapshot Snapshot) *task.ProjectTaskContext {
	if snapshot.Sprint.ProjectFocus != nil {
		return snapshot.Sprint.ProjectFocus
	}
	if snapshot.Continuity.CurrentFocus.ProjectFocus != nil {
		return snapshot.Continuity.CurrentFocus.ProjectFocus
	}
	return nil
}

func renderProjectBindingPrimary(kind, id, title, status string) string {
	id = strings.TrimSpace(id)
	title = strings.TrimSpace(title)
	status = strings.TrimSpace(status)
	if id == "" && title == "" && status == "" {
		return ""
	}
	base := strings.TrimSpace(strings.Join([]string{id, title}, " "))
	if base == "" {
		base = "-"
	}
	if status != "" {
		return fmt.Sprintf("%s %s [%s]", kind, base, status)
	}
	return fmt.Sprintf("%s %s", kind, base)
}

func joinNonEmpty(values []string) string {
	if len(values) == 0 {
		return ""
	}
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		filtered = append(filtered, value)
	}
	return strings.Join(filtered, ", ")
}

func joinOrDash(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(values, ", ")
}

func projectStepMarker(status string) string {
	switch status {
	case task.ProjectStepStatusCompleted:
		return "x"
	case task.ProjectStepStatusInProgress:
		return ">"
	case task.ProjectStepStatusBlocked:
		return "!"
	case task.ProjectStepStatusCancelled:
		return "~"
	default:
		return "-"
	}
}

func projectBranchMarker(status string) string {
	switch status {
	case task.ProjectBranchStatusCompleted:
		return "x"
	case task.ProjectBranchStatusActive:
		return ">"
	case task.ProjectBranchStatusBlocked:
		return "!"
	case task.ProjectBranchStatusCancelled:
		return "~"
	default:
		return "-"
	}
}
