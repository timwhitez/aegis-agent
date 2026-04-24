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
	if kind, text := c.requiredArtifactGate(toolName); text != "" {
		return c.block(kind, text, map[string]any{"source": "artifact_tracker", "tool_name": toolName})
	}
	if kind, text := c.parentCoordinationGate(toolName); text != "" {
		return c.block(kind, text, map[string]any{"source": "parent_coordination", "tool_name": toolName})
	}
	return GateDecision{Status: GateAllow}
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
