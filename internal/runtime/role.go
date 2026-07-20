package runtime

import (
	"fmt"
	"strings"

	"go-cli-agent/internal/session"
)

const (
	agentRolePlanner   = "planner"
	agentRoleGenerator = "generator"
	agentRoleEvaluator = "evaluator"
	agentRoleExplorer  = "explorer"
)

func normalizeAgentRole(explicitRole, _ string) (string, error) {
	role := strings.ToLower(strings.TrimSpace(explicitRole))
	if role == "" {
		return "", nil
	}
	switch role {
	case agentRolePlanner, agentRoleGenerator, agentRoleEvaluator, agentRoleExplorer:
		return role, nil
	default:
		return "", fmt.Errorf("unsupported agent role: %s", explicitRole)
	}
}

func roleGuidance(agentRole, _ string) (string, []string) {
	role := strings.ToLower(strings.TrimSpace(agentRole))
	switch role {
	case agentRolePlanner:
		return role, []string{
			"Expand brief asks into durable handoff artifacts first. Prefer reports/spec.md plus reports/plan.md with acceptance criteria, ordered work chunks, and explicit boundaries before broad implementation.",
			"Stay ambitious on scope and product shape, but avoid brittle low-level implementation mandates unless repo truth already proves them.",
			"Keep the durable task board aligned with the written plan so a later generator or evaluator session can resume cleanly.",
		}
	case agentRoleEvaluator:
		return role, []string{
			"Be skeptical of claimed success. Validate against the current spec, plan, progress, validation notes, code, and tests before clearing work.",
			"Prefer concrete blockers, regressions, missing interactions, or unproven behavior over praise. Only write \"No validated findings.\" when the evidence truly clears that bar.",
			"Refresh reports/validation.md with actionable findings or remaining risks so the next session inherits a trustworthy QA handoff.",
		}
	case agentRoleGenerator:
		return role, []string{
			"Pull the next tractable step from the durable spec, plan, and task state instead of drifting across the whole repo.",
			"After each meaningful implementation slice, run the narrowest useful verification, then refresh reports/progress.md and reports/validation.md before handoff or finish.",
			"Treat evaluator or reviewer feedback as backlog to close with evidence, not as optional commentary.",
		}
	case agentRoleExplorer:
		return role, []string{
			"This is a read-only repository exploration session. Use only the read/search/load_skill/finish capabilities exposed by the harness; do not modify workspace or durable goal/task/plan state, run shell commands, or delegate again.",
			"Return a concise evidence handoff with a claim | file:line | confidence table, followed by Uncovered scope and Key uncertainties that still require parent judgment.",
			"Keep raw search output and long excerpts in this child session or its artifacts. Do not paste the child tool trajectory into the final handoff.",
		}
	default:
		return "", nil
	}
}

func toolProfileForAgentRole(agentRole string) string {
	if strings.EqualFold(strings.TrimSpace(agentRole), agentRoleExplorer) {
		return session.ToolProfileExplorerReadOnly
	}
	return session.ToolProfileDefault
}

func resolveAgentToolProfile(agentRole, requested string) (string, error) {
	expected := toolProfileForAgentRole(agentRole)
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return expected, nil
	}
	if requested != expected {
		return "", fmt.Errorf("tool profile %s does not match agent role %s", requested, firstNonEmpty(agentRole, "default"))
	}
	return requested, nil
}

func isolationFallbackForAgentRole(agentRole, fallback string) string {
	if strings.EqualFold(strings.TrimSpace(agentRole), agentRoleExplorer) {
		return "off"
	}
	return fallback
}
