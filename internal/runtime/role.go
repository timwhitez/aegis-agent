package runtime

import (
	"fmt"
	"strings"
)

const (
	agentRolePlanner   = "planner"
	agentRoleGenerator = "generator"
	agentRoleEvaluator = "evaluator"
)

func normalizeAgentRole(explicitRole, agentName string) (string, error) {
	role := strings.ToLower(strings.TrimSpace(explicitRole))
	if role == "" {
		return inferAgentRole(agentName), nil
	}
	switch role {
	case agentRolePlanner, agentRoleGenerator, agentRoleEvaluator:
		return role, nil
	default:
		return "", fmt.Errorf("unsupported agent role: %s", explicitRole)
	}
}

func inferAgentRole(agentName string) string {
	lowered := strings.ToLower(strings.TrimSpace(agentName))
	switch {
	case lowered == "":
		return ""
	case strings.Contains(lowered, "planner"), strings.Contains(lowered, "architect"), strings.Contains(lowered, "spec"):
		return agentRolePlanner
	case strings.Contains(lowered, "review"), strings.Contains(lowered, "evaluator"), strings.Contains(lowered, "qa"), strings.Contains(lowered, "audit"):
		return agentRoleEvaluator
	case strings.Contains(lowered, "generator"), strings.Contains(lowered, "builder"), strings.Contains(lowered, "implement"), strings.Contains(lowered, "coder"):
		return agentRoleGenerator
	default:
		return ""
	}
}

func roleGuidance(agentRole, agentName string) (string, []string) {
	role := strings.ToLower(strings.TrimSpace(agentRole))
	if role == "" {
		role = inferAgentRole(agentName)
	}
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
	default:
		return "", nil
	}
}
