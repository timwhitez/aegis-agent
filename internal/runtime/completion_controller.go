package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go-cli-agent/internal/session"
)

type GateDecisionStatus string

const (
	GateAllow GateDecisionStatus = "allow"
	GateBlock GateDecisionStatus = "block"
	GateWarn  GateDecisionStatus = "warn"
	GateDefer GateDecisionStatus = "defer"
)

type GateDecision struct {
	Status          GateDecisionStatus
	GateID          string
	Severity        string
	Reason          string
	OperatorMessage string
	ModelMessage    string
	Evidence        map[string]any
}

type CompletionController struct {
	store     *session.Store
	sessionID string
	workdir   string
	yolo      bool
	emit      func(string, map[string]any)
}

func NewCompletionController(store *session.Store, sessionID, workdir string, yolo bool, emit func(string, map[string]any)) *CompletionController {
	return &CompletionController{
		store:     store,
		sessionID: sessionID,
		workdir:   workdir,
		yolo:      yolo,
		emit:      emit,
	}
}

func (c *CompletionController) EvaluateToolCall(messages []session.Message, toolName string, rawArgs json.RawMessage) GateDecision {
	if toolName == "finish" {
		c.emitCompletion("completion.evaluate.started", map[string]any{"tool_name": toolName})
	}
	if kind, text := toolGuard(c.workdir, messages, toolName, rawArgs, c.yolo); text != "" {
		return c.block(kind, text, map[string]any{"source": "tool_guard", "tool_name": toolName})
	}
	if kind, text := c.planModeGate(toolName); text != "" {
		return c.block(kind, text, map[string]any{"source": "plan_mode", "tool_name": toolName})
	}
	if kind, text := c.requiredArtifactGate(toolName); text != "" {
		return c.block(kind, text, map[string]any{"source": "artifact_tracker", "tool_name": toolName})
	}
	if kind, text := c.parentCoordinationGate(toolName); text != "" {
		return c.block(kind, text, map[string]any{"source": "parent_coordination", "tool_name": toolName})
	}
	if kind, text := c.goalCompletionGate(toolName); text != "" {
		return c.block(kind, text, map[string]any{"source": "goal", "tool_name": toolName})
	}
	return GateDecision{Status: GateAllow}
}

func (c *CompletionController) planModeGate(toolName string) (string, string) {
	planMode, err := c.store.LoadPlanMode(c.sessionID)
	if err != nil || planMode.PlanModeID == "" {
		return "", ""
	}
	return planModeToolGate(planMode, toolName)
}

func (c *CompletionController) MarkAllowed(toolName string) {
	if toolName != "finish" {
		return
	}
	c.emitCompletion("completion.gate.passed", map[string]any{"gate_id": "finish"})
	c.emitCompletion("completion.evaluate.finished", map[string]any{"status": string(GateAllow)})
}

func (c *CompletionController) EvaluatePreCompletionFeatures(enabled bool) GateDecision {
	if !enabled {
		return GateDecision{Status: GateAllow}
	}
	featureListPath := filepath.Join(c.store.SessionDir(c.sessionID), "feature_list.json")
	data, err := os.ReadFile(featureListPath)
	if err != nil {
		return GateDecision{Status: GateAllow}
	}
	var featureList session.FeatureList
	if json.Unmarshal(data, &featureList) != nil {
		return GateDecision{Status: GateAllow}
	}
	var incomplete []string
	for _, f := range featureList.Features {
		if f.Status != "completed" {
			incomplete = append(incomplete, fmt.Sprintf("- %s (status: %s)", f.ID, f.Status))
		}
	}
	if len(incomplete) == 0 {
		return GateDecision{Status: GateAllow}
	}
	text := fmt.Sprintf("Pre-completion check failed: %d feature(s) not completed:\n%s\n\nPlease complete all features before calling finish.", len(incomplete), strings.Join(incomplete, "\n"))
	return c.block("pre_completion_check", text, map[string]any{"source": "feature_list", "path": featureListPath})
}

func (c *CompletionController) TrackToolResult(toolName string, result session.ToolResult, turn int) {
	if result.IsError || (toolName != "write_file" && toolName != "edit_file") {
		return
	}
	path, _ := result.Metadata["path"].(string)
	if strings.TrimSpace(path) == "" {
		return
	}
	artifacts, err := c.store.LoadArtifactTracker(c.sessionID)
	if err != nil || len(artifacts) == 0 {
		return
	}
	updated, changed := markArtifactWrite(artifacts, path, toolName, turn)
	if !changed {
		return
	}
	if err := c.store.SaveArtifactTracker(c.sessionID, updated); err != nil {
		return
	}
	if contract, err := c.store.LoadContract(c.sessionID); err == nil {
		contract.RequiredArtifacts = updated
		contract.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		_ = c.store.SaveContract(c.sessionID, contract)
	}
	c.emitCompletion("artifact.tracked", map[string]any{
		"path":      path,
		"tool_name": toolName,
		"turn":      turn,
	})
	_ = writeSessionSummary(c.store, c.sessionID)
	_ = writeLongRunCheckpoint(c.store, c.sessionID)
}

func (c *CompletionController) requiredArtifactGate(toolName string) (string, string) {
	if toolName != "finish" {
		return "", ""
	}
	artifacts, err := c.store.LoadArtifactTracker(c.sessionID)
	if err != nil || len(artifacts) == 0 {
		return "", ""
	}
	artifacts = refreshArtifactStatuses(artifacts)
	_ = c.store.SaveArtifactTracker(c.sessionID, artifacts)
	if contract, err := c.store.LoadContract(c.sessionID); err == nil {
		contract.RequiredArtifacts = artifacts
		contract.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		_ = c.store.SaveContract(c.sessionID, contract)
	}
	var missing []string
	var stale []string
	for _, artifact := range artifacts {
		if !artifact.Required {
			continue
		}
		display := artifact.DisplayPath
		if display == "" {
			display = displayPromptPath(c.workdir, artifact.Path)
		}
		if !artifact.Status.Present {
			missing = append(missing, display)
			continue
		}
		if !artifact.Status.TouchedBySession && !artifact.Status.ChangedFromBaseline {
			stale = append(stale, display)
		}
	}
	if len(missing) == 0 && len(stale) == 0 {
		c.emitCompletion("artifact.gate.passed", map[string]any{"required_count": len(artifacts)})
		return "", ""
	}
	c.emitCompletion("artifact.gate.blocked", map[string]any{
		"missing": missing,
		"stale":   stale,
	})
	parts := []string{}
	if len(missing) > 0 {
		parts = append(parts, "missing "+joinPromptItems(missing))
	}
	if len(stale) > 0 {
		parts = append(parts, "not touched or changed in this session "+joinPromptItems(stale))
	}
	return "required_artifact", "Required-artifact gate: before finishing, write or update the explicit required artifact(s): " + strings.Join(parts, "; ") + "."
}

func (c *CompletionController) parentCoordinationGate(toolName string) (string, string) {
	if toolName != "finish" {
		return "", ""
	}
	notifications, err := c.store.LoadBackgroundNotifications(c.sessionID)
	if err == nil {
		var pending []string
		for _, notification := range notifications {
			if notification.DeliveryStatus != session.BackgroundNotificationPending {
				continue
			}
			id := strings.TrimSpace(notification.QueueJobID)
			if id == "" {
				id = strings.TrimSpace(notification.SessionID)
			}
			if id == "" {
				id = strings.TrimSpace(notification.ID)
			}
			pending = append(pending, id)
		}
		if len(pending) > 0 {
			c.emitCompletion("completion.gate.parent_background_pending", map[string]any{
				"pending_background_notifications": pending,
			})
			return "parent_background_pending", fmt.Sprintf("Parent-background gate: completed child or background results are pending transcript acceptance before finish (%s). Continue one more turn so the harness can accept those durable facts, then reconcile them before the final conclusion.", joinPromptItems(pending))
		}
	}
	coordination, err := c.store.LoadParentCoordination(c.sessionID)
	if err != nil || coordination.ParentSessionID == "" {
		return "", ""
	}
	if len(coordination.UnresolvedChildSessions) == 0 && len(coordination.UnresolvedQueueJobs) == 0 {
		return "", ""
	}
	if coordination.WaitMode == "wait-any" && (len(coordination.CompletedChildSessions) > 0 || len(coordination.CompletedQueueJobs) > 0) {
		c.emitCompletion("parent_coordination.gate.warned", map[string]any{
			"unresolved_child_sessions": coordination.UnresolvedChildSessions,
			"unresolved_queue_jobs":     coordination.UnresolvedQueueJobs,
		})
		return "", ""
	}
	return "parent_coordination", fmt.Sprintf("Parent-coordination gate: unresolved child or queue work remains before finish (children: %s; jobs: %s). Wait for completion, mark wait_mode=wait-any with one completed result, or explicitly resolve the outstanding work.", joinPromptItems(coordination.UnresolvedChildSessions), joinPromptItems(coordination.UnresolvedQueueJobs))
}

func (c *CompletionController) goalCompletionGate(toolName string) (string, string) {
	if toolName != "finish" {
		return "", ""
	}
	goal, err := c.store.LoadGoal(c.sessionID)
	if err != nil || goal.GoalID == "" {
		return "", ""
	}
	switch goal.Status {
	case session.GoalStatusActive:
		return "goal_completion_audit", "Goal completion gate: this session has an active goal. Before finishing, restate the objective as concrete deliverables, audit each success criterion and validation item against real evidence, then call update_goal with status \"complete\" if the goal is actually achieved. If it is not achieved, keep working or stop in awaiting input instead of calling finish."
	case session.GoalStatusBudgetLimited, session.GoalStatusPaused, session.GoalStatusComplete:
		return "", ""
	default:
		return "goal_completion_audit", fmt.Sprintf("Goal completion gate: goal status %q is not a valid completion state. Read get_goal, reconcile the goal status, and only finish after the status is complete, paused, or budget_limited wrap-up.", goal.Status)
	}
}

func (c *CompletionController) block(kind, text string, evidence map[string]any) GateDecision {
	c.emitCompletion("completion.gate.blocked", map[string]any{
		"gate_id":  kind,
		"severity": "block",
		"evidence": evidence,
	})
	c.emitCompletion("completion.evaluate.finished", map[string]any{
		"status":  string(GateBlock),
		"gate_id": kind,
	})
	return GateDecision{
		Status:          GateBlock,
		GateID:          kind,
		Severity:        "block",
		Reason:          kind,
		OperatorMessage: text,
		ModelMessage:    text,
		Evidence:        evidence,
	}
}

func (c *CompletionController) emitCompletion(eventType string, data map[string]any) {
	if c.emit != nil {
		c.emit(eventType, data)
	}
}
