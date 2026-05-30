package runtime

import (
	"encoding/json"
	"fmt"
	"strings"

	"go-cli-agent/internal/provider"
	"go-cli-agent/internal/session"
	"go-cli-agent/internal/tools"
)

const (
	planModeTerminalKey           = "planmode_terminal"
	planModeTerminalPlanSubmitted = "plan_submitted"
	planModeTerminalPlanCancelled = "plan_cancelled"
	planModeInputPendingKey       = "plan_input_pending"
)

var planModePlanningAllowedTools = map[string]struct{}{
	"read_file":          {},
	"glob":               {},
	"grep":               {},
	"grep_files":         {},
	"load_skill":         {},
	"todo_read":          {},
	"task_list":          {},
	"task_get":           {},
	"feature_list_read":  {},
	"get_goal":           {},
	"get_plan_mode":      {},
	"request_user_input": {},
	"submit_plan":        {},
}

var planModePendingOnlyTools = map[string]struct{}{
	"request_user_input": {},
	"submit_plan":        {},
}

func loadPlanModeOptional(store *session.Store, sessionID string) (*session.PlanModeState, error) {
	return session.LoadPlanModeOptional(store, sessionID)
}

func shouldUsePlanModeInstructions(planMode *session.PlanModeState) bool {
	return planMode != nil && session.IsPlanModePending(planMode.Status)
}

func buildPlanModeInstructions(planMode session.PlanModeState) string {
	return strings.TrimSpace(fmt.Sprintf(`## Plan Mode
You are in Plan Mode for this session.

Plan Mode is an explicit execution gate. Do not infer entry or exit from user wording. Until the user approves, cancels, or revises the Plan Mode state, you may only gather non-mutating facts, ask high-impact questions, and submit a complete plan.

Current Plan Mode:
- status: %s
- objective: %s
- plan version: %d

Allowed before approval:
- read/search/load relevant files and skills
- use get_goal/get_plan_mode and read-only todo/task/feature-list tools
- use request_user_input for one to three decisions that materially affect the plan
- use submit_plan when the plan is complete

Not allowed before approval:
- shell, write_file, edit_file, todo_write, task_create, task_update, feature_list_create, feature_list_update, create_goal, record_goal_progress, update_goal, agent_spawn, agent_status, agent_list, queue/delegate submission, skill command tools, custom extension tools, or finish

A complete plan must include Summary, Implementation Steps, Interfaces and Data Model, Verification, Risks, and Assumptions. Do not use todo_write or task tools as the Plan Mode plan.`, planMode.Status, planMode.Objective, planMode.PlanVersion))
}

func buildApprovedPlanContext(planMode session.PlanModeState) string {
	if !session.IsPlanModeExecution(planMode.Status) || strings.TrimSpace(planMode.PlanMarkdown) == "" {
		return ""
	}
	version := planMode.ApprovedVersion
	if version == 0 {
		version = planMode.PlanVersion
	}
	return strings.TrimSpace(fmt.Sprintf(`<approved_plan>
The user approved Plan Mode plan version %d. Follow this plan unless newer user input changes it. If it becomes stale, explain the conflict before deviating.

%s
</approved_plan>`, version, strings.TrimSpace(planMode.PlanMarkdown)))
}

func providerToolsForPlanMode(registry *tools.Registry, planMode *session.PlanModeState) []provider.ToolSchema {
	defs := registry.Definitions()
	out := make([]provider.ToolSchema, 0, len(defs))
	for _, def := range defs {
		if shouldUsePlanModeInstructions(planMode) {
			if _, ok := planModePlanningAllowedTools[def.Name]; !ok {
				continue
			}
		} else {
			if _, ok := planModePendingOnlyTools[def.Name]; ok {
				continue
			}
		}
		out = append(out, provider.ToolSchema{
			Name:        def.Name,
			Description: def.Description,
			InputSchema: def.InputSchema,
		})
	}
	return out
}

func planModeToolGate(state session.PlanModeState, toolName string) (string, string) {
	if !session.IsPlanModePending(state.Status) {
		return "", ""
	}
	if _, ok := planModePlanningAllowedTools[toolName]; ok {
		return "", ""
	}
	return "plan_mode_pending", "Plan Mode is awaiting user approval. This tool is not available before the user approves the proposed plan. Continue by reading/searching, asking request_user_input, or calling submit_plan."
}

func (r *Runner) rejectParentLinkedActionDuringPendingPlanMode(parentSessionID, action string) error {
	parentSessionID = strings.TrimSpace(parentSessionID)
	if parentSessionID == "" {
		return nil
	}
	planMode, err := loadPlanModeOptional(r.store, parentSessionID)
	if err != nil {
		return err
	}
	if planMode == nil || !session.IsPlanModePending(planMode.Status) {
		return nil
	}
	return fmt.Errorf("plan mode is pending for parent session %s; %s is not available before approve or cancel", parentSessionID, action)
}

func terminalPlanModeAction(result session.ToolResult) string {
	if result.Metadata == nil {
		return ""
	}
	value, _ := result.Metadata[planModeTerminalKey].(string)
	return value
}

func isPendingPlanModeInputResult(result session.ToolResult) bool {
	if result.Metadata == nil {
		return false
	}
	value, _ := result.Metadata[planModeInputPendingKey].(bool)
	return value
}

func terminalPlanModeSyntheticResult(action string) string {
	switch action {
	case planModeTerminalPlanSubmitted:
		return "Error: submit_plan ended the Plan Mode turn; this later tool call was not executed"
	case planModeTerminalPlanCancelled:
		return "Error: Plan Mode was cancelled; this later tool call was not executed"
	default:
		return "Error: Plan Mode ended this turn; this later tool call was not executed"
	}
}

func planModeToolResultPayload(toolName string, payload any) session.ToolResult {
	data, err := json.Marshal(payload)
	if err != nil {
		data = []byte(fmt.Sprintf(`{"error":%q}`, err.Error()))
	}
	text := string(data)
	return session.ToolResult{
		Name:          toolName,
		LLMOutput:     text,
		DisplayOutput: text,
		Metadata: map[string]any{
			"planmode": true,
		},
	}
}

func planModeEventData(planMode session.PlanModeState) map[string]any {
	data := map[string]any{
		"plan_mode_id":     planMode.PlanModeID,
		"status":           planMode.Status,
		"objective":        planMode.Objective,
		"plan_id":          planMode.PlanID,
		"plan_version":     planMode.PlanVersion,
		"approved_version": planMode.ApprovedVersion,
	}
	if planMode.Summary != "" {
		data["summary"] = planMode.Summary
	}
	if planMode.PendingRequest != nil {
		data["pending_request_id"] = planMode.PendingRequest.RequestID
		data["pending_questions"] = len(planMode.PendingRequest.Questions)
	}
	return data
}
