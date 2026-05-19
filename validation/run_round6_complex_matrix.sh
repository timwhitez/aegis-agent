#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKSPACE_ROOT="$(cd "${ROOT_DIR}/.." && pwd)"
cd "$ROOT_DIR"

: "${OPENAI_API_KEY:?OPENAI_API_KEY is required}"

MODEL="${GO_CLI_AGENT_LIVE_MODEL:-gpt-5.4}"
CONFIG_PATH="validation/config.openai-compatible.yaml"
ROUND_ID="${1:-$(date -u +%F)-openai-compatible-${MODEL}-round6-complex-matrix}"
RUN_DIR="validation/runs/${ROUND_ID}"
RAW_DIR="${RUN_DIR}/raw"
PROMPT_DIR="${RUN_DIR}/prompts"
ARTIFACT_DIR="${RUN_DIR}/artifacts"
WORKSPACE_DIR="${RUN_DIR}/workspaces"
NOTE_DIR="${RUN_DIR}/notes"

if [[ -e "$RUN_DIR" ]]; then
	echo "run directory already exists: $RUN_DIR" >&2
	exit 1
fi

mkdir -p "$RAW_DIR" "$PROMPT_DIR" "$ARTIFACT_DIR" "$WORKSPACE_DIR" "$NOTE_DIR"
ABS_ARTIFACT_DIR="$(cd "$ARTIFACT_DIR" && pwd)"
printf 'scenario_id\tlabel\traw\tartifact\tsession_id\n' >"${NOTE_DIR}/scenario-index.tsv"

copy_workspace() {
	local name="$1"
	cp -R "validation/workspaces/${name}" "${WORKSPACE_DIR}/${name}"
}

write_prompt() {
	local path="$1"
	shift
	printf '%s\n' "$*" >"$path"
}

extract_session_id() {
	local path="$1"
	grep -o '"session_id":"[^"]*"' "$path" | tail -n1 | cut -d'"' -f4
}

wait_for_pattern() {
	local path="$1"
	local pattern="$2"
	local timeout_sec="${3:-90}"
	local waited=0
	while (( waited < timeout_sec )); do
		if [[ -f "$path" ]] && grep -Fq "$pattern" "$path"; then
			return 0
		fi
		sleep 1
		waited=$((waited + 1))
	done
	echo "timed out waiting for pattern ${pattern} in ${path}" >&2
	return 1
}

run_exec() {
	local prompt_path="$1"
	local raw_path="$2"
	local workdir="$3"
	local timeout_sec="${4:-300}"
	./bin/go-cli-agent exec \
		--config "$CONFIG_PATH" \
		--provider openai-compatible \
		--model "$MODEL" \
		--workdir "$workdir" \
		--json \
		--timeout "$timeout_sec" \
		<"$prompt_path" >"$raw_path" 2>&1
}

run_run() {
	local prompt_path="$1"
	local raw_path="$2"
	local workdir="$3"
	local timeout_sec="${4:-300}"
	./bin/go-cli-agent run \
		--config "$CONFIG_PATH" \
		--provider openai-compatible \
		--model "$MODEL" \
		--workdir "$workdir" \
		--json \
		--timeout "$timeout_sec" \
		<"$prompt_path" >"$raw_path" 2>&1
}

copy_if_present() {
	local src="$1"
	local dst="$2"
	if [[ -f "$src" ]]; then
		cp "$src" "$dst"
	fi
}

record_scenario() {
	local scenario_id="$1"
	local label="$2"
	local raw_path="$3"
	local artifact_path="$4"
	local session_id="${5:-}"
	printf '%s\t%s\t%s\t%s\t%s\n' "$scenario_id" "$label" "$raw_path" "$artifact_path" "$session_id" >>"${NOTE_DIR}/scenario-index.tsv"
}

copy_workspace docset
copy_workspace incident
copy_workspace patch
copy_workspace patch_go
copy_workspace nested_review

DOCSET_DIR="${WORKSPACE_DIR}/docset"
INCIDENT_DIR="${WORKSPACE_DIR}/incident"
PATCH_DIR="${WORKSPACE_DIR}/patch"
PATCH_GO_DIR="${WORKSPACE_DIR}/patch_go"
NESTED_API_DIR="${WORKSPACE_DIR}/nested_review/services/api"

./build.sh >"${RAW_DIR}/preflight-build.txt" 2>&1
./test.sh >"${RAW_DIR}/preflight-test.txt" 2>&1
./bin/go-cli-agent doctor \
	--config "$CONFIG_PATH" \
	--provider openai-compatible \
	--json >"${RAW_DIR}/preflight-doctor.json" 2>&1
./bin/go-cli-agent probe-provider \
	--config "$CONFIG_PATH" \
	--provider openai-compatible \
	--json >"${RAW_DIR}/preflight-probe.json" 2>&1

RT01_PROMPT="${PROMPT_DIR}/rt01-architecture-audit.prompt.txt"
write_prompt "$RT01_PROMPT" "Audit the current go-cli-agent repository for Web-first v1 readiness.
Only inspect README.md, AGENTS.md, spec/*.md, cmd/go-cli-agent, internal/runtime, internal/provider, internal/tools, and internal/session.
Ignore validation/runs, validation/sessions, reports, bin, tmp, and generated artifacts.
Start with a short todo plan. Prefer glob or grep_files to discover candidates, use grep only for exact line evidence, and keep read_file slices small.
Write ${ABS_ARTIFACT_DIR}/rt01-architecture-audit.md with sections: confirmed architecture map, top 5 risks ordered by severity, and smallest next fixes.
Then call finish with a one-line summary."
RT01_RAW="${RAW_DIR}/rt01-architecture-audit.jsonl"
run_exec "$RT01_PROMPT" "$RT01_RAW" "$ROOT_DIR" 300
record_scenario "RT01" "Self Repo Architecture Audit" "$RT01_RAW" "${ARTIFACT_DIR}/rt01-architecture-audit.md" "$(extract_session_id "$RT01_RAW")"

RT02_PROMPT="${PROMPT_DIR}/rt02-provider-drift.prompt.txt"
write_prompt "$RT02_PROMPT" "Inspect only spec/03-provider-contracts.md, internal/provider/openai.go, internal/provider/anthropic.go, internal/provider/google.go, internal/provider/types.go, internal/runtime/runner.go, and internal/runtime/engine.go.
Ignore validation/runs, validation/sessions, reports, bin, tmp, and generated artifacts.
Prefer targeted retrieval with small read_file slices.
Produce a Markdown drift report at ${ABS_ARTIFACT_DIR}/rt02-provider-drift.md with sections: confirmed alignments, confirmed drifts, and smallest fixes.
Then call finish with a one-line summary."
RT02_RAW="${RAW_DIR}/rt02-provider-drift.jsonl"
run_exec "$RT02_PROMPT" "$RT02_RAW" "$ROOT_DIR" 300
record_scenario "RT02" "Self Repo Provider Drift Audit" "$RT02_RAW" "${ARTIFACT_DIR}/rt02-provider-drift.md" "$(extract_session_id "$RT02_RAW")"

RT03_PROMPT="${PROMPT_DIR}/rt03-reference-corpus.prompt.txt"
write_prompt "$RT03_PROMPT" "Read only these top-level reference documents: pi-coding-agent.md, bitter-lesson-agent-frameworks.md, learn-claude-code.md, openai-com__harness-engineering.md, developers-openai-com__run-long-horizon-tasks-with-codex.md, anthropic-com__effective-harnesses-for-long-running-agents.md, blog-langchain-com__autonomous-context-compression.md, and blog-langchain-com__the-anatomy-of-an-agent-harness.md.
Do not inspect go-cli-agent source code for this task.
Write ${ABS_ARTIFACT_DIR}/rt03-reference-corpus-brief.md with sections: recurring patterns, useful tensions, and concrete ideas go-cli-agent should import.
Then call finish with a one-line summary."
RT03_RAW="${RAW_DIR}/rt03-reference-corpus.jsonl"
run_exec "$RT03_PROMPT" "$RT03_RAW" "$WORKSPACE_ROOT" 300
record_scenario "RT03" "Reference Corpus Synthesis" "$RT03_RAW" "${ARTIFACT_DIR}/rt03-reference-corpus-brief.md" "$(extract_session_id "$RT03_RAW")"

RT04_PROMPT="${PROMPT_DIR}/rt04-docset-launch-brief.prompt.txt"
write_prompt "$RT04_PROMPT" "Read the local AGENTS.md and synthesize only product_overview.md, ops_notes.md, and release_constraints.md.
Ignore reports/.
Write reports/product-brief.md with sections: product goal, release themes, operator concerns, unanswered questions.
Then call finish with a one-line summary."
RT04_RAW="${RAW_DIR}/rt04-docset-launch-brief.jsonl"
run_exec "$RT04_PROMPT" "$RT04_RAW" "$DOCSET_DIR" 240
copy_if_present "${DOCSET_DIR}/reports/product-brief.md" "${ARTIFACT_DIR}/rt04-product-brief.md"
record_scenario "RT04" "Docset Launch Brief" "$RT04_RAW" "${ARTIFACT_DIR}/rt04-product-brief.md" "$(extract_session_id "$RT04_RAW")"

RT05_PROMPT="${PROMPT_DIR}/rt05-docset-operator-checklist.prompt.txt"
write_prompt "$RT05_PROMPT" "Read the local AGENTS.md and synthesize only product_overview.md, ops_notes.md, and release_constraints.md.
Ignore reports/.
Write reports/operator-checklist.md with sections: release scope, upgrade checklist, residual risks.
Keep it concise and operator-oriented.
Then call finish with a one-line summary."
RT05_RAW="${RAW_DIR}/rt05-docset-operator-checklist.jsonl"
run_exec "$RT05_PROMPT" "$RT05_RAW" "$DOCSET_DIR" 240
copy_if_present "${DOCSET_DIR}/reports/operator-checklist.md" "${ARTIFACT_DIR}/rt05-operator-checklist.md"
record_scenario "RT05" "Docset Operator Checklist" "$RT05_RAW" "${ARTIFACT_DIR}/rt05-operator-checklist.md" "$(extract_session_id "$RT05_RAW")"

RT06_PROMPT="${PROMPT_DIR}/rt06-incident-triage.prompt.txt"
write_prompt "$RT06_PROMPT" "Diagnose the incident in this workspace.
Read the local AGENTS.md first, then use logs and service_config.json to build a short timeline before proposing a root cause.
Ignore reports/.
Write reports/incident-summary.md with sections: confirmed timeline, likely root cause, smallest corrective action, open questions.
Then call finish with a one-line summary."
RT06_RAW="${RAW_DIR}/rt06-incident-triage.jsonl"
run_exec "$RT06_PROMPT" "$RT06_RAW" "$INCIDENT_DIR" 240
copy_if_present "${INCIDENT_DIR}/reports/incident-summary.md" "${ARTIFACT_DIR}/rt06-incident-summary.md"
record_scenario "RT06" "Incident Triage" "$RT06_RAW" "${ARTIFACT_DIR}/rt06-incident-summary.md" "$(extract_session_id "$RT06_RAW")"

RT07_PROMPT="${PROMPT_DIR}/rt07-durable-task-graph.prompt.txt"
write_prompt "$RT07_PROMPT" "Use the incident evidence in this workspace to build a durable task graph for triage.
Create at least four tasks with one explicit dependency edge, keep todo updated, use available evidence to move tasks to completed where appropriate, and write reports/taskboard-summary.md summarizing the final task board.
Then call finish with a one-line summary."
RT07_RAW="${RAW_DIR}/rt07-durable-task-graph.jsonl"
run_exec "$RT07_PROMPT" "$RT07_RAW" "$INCIDENT_DIR" 300
RT07_SESSION_ID="$(extract_session_id "$RT07_RAW")"
./bin/go-cli-agent tasks "$RT07_SESSION_ID" --config "$CONFIG_PATH" --json >"${RAW_DIR}/rt07-durable-task-graph-taskboard.json" 2>&1
copy_if_present "${INCIDENT_DIR}/reports/taskboard-summary.md" "${ARTIFACT_DIR}/rt07-taskboard-summary.md"
record_scenario "RT07" "Durable Task Graph" "$RT07_RAW" "${ARTIFACT_DIR}/rt07-taskboard-summary.md" "$RT07_SESSION_ID"

RT08_PROMPT="${PROMPT_DIR}/rt08-python-patch.prompt.txt"
write_prompt "$RT08_PROMPT" "Read the local AGENTS.md first.
Ignore reports/.
Find the smallest correct bug fix in this workspace, make the code change, run the narrowest validation that proves the fix, and write reports/change-summary.md with sections: root cause, files changed, verification.
Then call finish with a one-line summary."
RT08_RAW="${RAW_DIR}/rt08-python-patch.jsonl"
run_exec "$RT08_PROMPT" "$RT08_RAW" "$PATCH_DIR" 300
copy_if_present "${PATCH_DIR}/reports/change-summary.md" "${ARTIFACT_DIR}/rt08-python-change-summary.md"
record_scenario "RT08" "Python Patch Smallest Bug" "$RT08_RAW" "${ARTIFACT_DIR}/rt08-python-change-summary.md" "$(extract_session_id "$RT08_RAW")"

RT09_PROMPT="${PROMPT_DIR}/rt09-go-patch.prompt.txt"
write_prompt "$RT09_PROMPT" "Read the local AGENTS.md first.
Ignore reports/.
Find the smallest correct bug fix in this Go workspace, make the code change, run the narrowest validation that proves the fix, and write reports/change-summary.md with sections: root cause, files changed, verification.
Then call finish with a one-line summary."
RT09_RAW="${RAW_DIR}/rt09-go-patch.jsonl"
run_exec "$RT09_PROMPT" "$RT09_RAW" "$PATCH_GO_DIR" 300
copy_if_present "${PATCH_GO_DIR}/reports/change-summary.md" "${ARTIFACT_DIR}/rt09-go-change-summary.md"
record_scenario "RT09" "Go Patch Smallest Bug" "$RT09_RAW" "${ARTIFACT_DIR}/rt09-go-change-summary.md" "$(extract_session_id "$RT09_RAW")"

RT10_PROMPT="${PROMPT_DIR}/rt10-nested-review.prompt.txt"
write_prompt "$RT10_PROMPT" "Read the applicable AGENTS.md chain first.
Review only README.md, handler.go, and handler_test.go in this directory.
Write reports/api-review.md with sections: findings, open questions, smallest fixes.
Put findings first, ordered by severity, and separate confirmed facts from inference.
Then call finish with a one-line summary."
RT10_RAW="${RAW_DIR}/rt10-nested-review.jsonl"
run_exec "$RT10_PROMPT" "$RT10_RAW" "$NESTED_API_DIR" 240
copy_if_present "${NESTED_API_DIR}/reports/api-review.md" "${ARTIFACT_DIR}/rt10-api-review.md"
record_scenario "RT10" "Nested AGENTS API Review" "$RT10_RAW" "${ARTIFACT_DIR}/rt10-api-review.md" "$(extract_session_id "$RT10_RAW")"

RT11_PROMPT="${PROMPT_DIR}/rt11-explicit-skill-loading.prompt.txt"
write_prompt "$RT11_PROMPT" "Use the repo_audit skill for this task.
Audit whether the default Web-first surface, CLI fallback, and docs stay aligned with Web-first v1 boundaries.
Call load_skill before deeper inspection.
Ignore validation/runs, validation/sessions, reports, bin, tmp, and generated artifacts.
Write ${ABS_ARTIFACT_DIR}/rt11-repo-audit-skill.md with sections: validated findings, remaining risks, smallest next fixes.
Then call finish with a one-line summary."
RT11_RAW="${RAW_DIR}/rt11-explicit-skill-loading.jsonl"
run_exec "$RT11_PROMPT" "$RT11_RAW" "$ROOT_DIR" 300
record_scenario "RT11" "Explicit Skill Loading" "$RT11_RAW" "${ARTIFACT_DIR}/rt11-repo-audit-skill.md" "$(extract_session_id "$RT11_RAW")"

RT12_PROMPT="${PROMPT_DIR}/rt12-command-tool-usage.prompt.txt"
write_prompt "$RT12_PROMPT" "Use the validation_helpers skill for this task.
Explicitly call markdown_inventory and pretty_json_args at least once when relevant, then synthesize this workspace into reports/tool-assisted-brief.md.
Ignore reports/ while gathering evidence.
Use sections: scope, key docs, concise brief.
Then call finish with a one-line summary."
RT12_RAW="${RAW_DIR}/rt12-command-tool-usage.jsonl"
run_exec "$RT12_PROMPT" "$RT12_RAW" "$DOCSET_DIR" 240
copy_if_present "${DOCSET_DIR}/reports/tool-assisted-brief.md" "${ARTIFACT_DIR}/rt12-tool-assisted-brief.md"
record_scenario "RT12" "Command Tool Usage" "$RT12_RAW" "${ARTIFACT_DIR}/rt12-tool-assisted-brief.md" "$(extract_session_id "$RT12_RAW")"

RT13_RUN_PROMPT="${PROMPT_DIR}/rt13-awaiting-input-run.prompt.txt"
write_prompt "$RT13_RUN_PROMPT" "Review only product_overview.md, ops_notes.md, and release_constraints.md in this workspace.
Draft a very short todo plan in assistant text, and stop naturally without calling finish once you need stakeholder input on whether to prioritize desktop onboarding or mobile onboarding."
RT13_RUN_RAW="${RAW_DIR}/rt13-awaiting-input-run.jsonl"
run_run "$RT13_RUN_PROMPT" "$RT13_RUN_RAW" "$DOCSET_DIR" 240
RT13_SESSION_ID="$(extract_session_id "$RT13_RUN_RAW")"
./bin/go-cli-agent tasks "$RT13_SESSION_ID" --config "$CONFIG_PATH" --json >"${RAW_DIR}/rt13-awaiting-input-taskboard.json" 2>&1
./bin/go-cli-agent continue "$RT13_SESSION_ID" \
	--config "$CONFIG_PATH" \
	--provider openai-compatible \
	--model "$MODEL" \
	--json \
	--message "Prioritize desktop onboarding. Ignore reports/ while gathering evidence. Write reports/continue-brief.md with sections: chosen priority, supporting evidence, next steps. Then call finish." \
	>"${RAW_DIR}/rt13-awaiting-input-continue.jsonl" 2>&1
copy_if_present "${DOCSET_DIR}/reports/continue-brief.md" "${ARTIFACT_DIR}/rt13-continue-brief.md"
record_scenario "RT13" "Awaiting Input And Continue" "${RAW_DIR}/rt13-awaiting-input-continue.jsonl" "${ARTIFACT_DIR}/rt13-continue-brief.md" "$RT13_SESSION_ID"

RT14_PROMPT="${PROMPT_DIR}/rt14-live-steer.prompt.txt"
write_prompt "$RT14_PROMPT" "Inspect only README.md, AGENTS.md, spec/04-tools-and-skills.md, spec/13-live-input-and-steering.md, internal/runtime/prompt.go, and internal/tools/registry.go.
Start with a short todo plan, use targeted retrieval only, and ignore validation/runs, validation/sessions, reports, bin, tmp, and generated artifacts.
Write ${ABS_ARTIFACT_DIR}/rt14-steer-audit.md with sections: confirmed behavior, remaining risks, smallest next fixes.
Then call finish."
RT14_RAW="${RAW_DIR}/rt14-live-steer.jsonl"
./bin/go-cli-agent exec \
	--config "$CONFIG_PATH" \
	--provider openai-compatible \
	--model "$MODEL" \
	--workdir "$ROOT_DIR" \
	--json \
	--timeout 300 \
	<"$RT14_PROMPT" >"$RT14_RAW" 2>&1 &
RT14_PID="$!"
wait_for_pattern "$RT14_RAW" '"type":"session.started"' 90
RT14_SESSION_ID="$(extract_session_id "$RT14_RAW")"
wait_for_pattern "$RT14_RAW" '"tool_name":"read_file"' 120 || true
./bin/go-cli-agent steer "$RT14_SESSION_ID" \
	--config "$CONFIG_PATH" \
	--json \
	--interrupt \
	--message "Stop reading now. Use current evidence only, write ${ABS_ARTIFACT_DIR}/rt14-steer-audit.md immediately, and finish." \
	>"${RAW_DIR}/rt14-live-steer-command.json" 2>&1
RT14_SECOND_STEER_USED=0
for _ in $(seq 1 35); do
	if ! kill -0 "$RT14_PID" 2>/dev/null; then
		break
	fi
	sleep 1
done
if kill -0 "$RT14_PID" 2>/dev/null; then
	RT14_SECOND_STEER_USED=1
	./bin/go-cli-agent steer "$RT14_SESSION_ID" \
		--config "$CONFIG_PATH" \
		--json \
		--interrupt \
		--message "Do not do more reads or bookkeeping. Finish immediately with the artifact you can write from current evidence." \
		>"${RAW_DIR}/rt14-live-steer-command-2.json" 2>&1
fi
wait "$RT14_PID"
printf 'second_steer_used=%s\n' "$RT14_SECOND_STEER_USED" >"${NOTE_DIR}/rt14-steer-metadata.txt"
record_scenario "RT14" "Live Steer Reprioritization" "$RT14_RAW" "${ARTIFACT_DIR}/rt14-steer-audit.md" "$RT14_SESSION_ID"

RT15_PROMPT="${PROMPT_DIR}/rt15-traceability-map.prompt.txt"
write_prompt "$RT15_PROMPT" "Inspect only README.md, AGENTS.md, spec/00-product.md, spec/01-runtime-architecture.md, spec/03-provider-contracts.md, spec/12-task-system.md, spec/13-live-input-and-steering.md, cmd/go-cli-agent/main.go, internal/app, internal/runtime, internal/provider, internal/session, and internal/tools.
Ignore validation/runs, validation/sessions, reports, bin, tmp, and generated artifacts.
Write ${ABS_ARTIFACT_DIR}/rt15-traceability-map.md with sections: core promise to owning files, strongest existing tests, gaps still hard to trace.
Then call finish with a one-line summary."
RT15_RAW="${RAW_DIR}/rt15-traceability-map.jsonl"
run_exec "$RT15_PROMPT" "$RT15_RAW" "$ROOT_DIR" 300
record_scenario "RT15" "Traceability Map" "$RT15_RAW" "${ARTIFACT_DIR}/rt15-traceability-map.md" "$(extract_session_id "$RT15_RAW")"

RT16_PROMPT="${PROMPT_DIR}/rt16-codex-steer-audit.prompt.txt"
write_prompt "$RT16_PROMPT" "Inspect only codex/AGENTS.md, codex/docs/agents_md.md, codex/docs/sandbox.md, codex/codex-rs/core/gpt_5_codex_prompt.md, and codex/codex-rs/app-server/tests/suite/v2/turn_steer.rs.
Do not inspect go-cli-agent code for this task.
Write ${ABS_ARTIFACT_DIR}/rt16-codex-steer-audit.md with sections: confirmed codex patterns, design ideas worth borrowing, cautions for go-cli-agent.
Then call finish with a one-line summary."
RT16_RAW="${RAW_DIR}/rt16-codex-steer-audit.jsonl"
run_exec "$RT16_PROMPT" "$RT16_RAW" "$WORKSPACE_ROOT" 300
record_scenario "RT16" "Codex Steer And Sandbox Audit" "$RT16_RAW" "${ARTIFACT_DIR}/rt16-codex-steer-audit.md" "$(extract_session_id "$RT16_RAW")"

RT17_PROMPT="${PROMPT_DIR}/rt17-codex-proxy-audit.prompt.txt"
write_prompt "$RT17_PROMPT" "Inspect only codex/codex-rs/responses-api-proxy/README.md, codex/codex-rs/process-hardening/README.md, codex/docs/authentication.md, and codex/docs/config.md.
Focus on OpenAI-compatible Responses transport, auth handling, and local hardening patterns.
Write ${ABS_ARTIFACT_DIR}/rt17-codex-proxy-audit.md with sections: confirmed contracts, hardening ideas worth importing, mismatch risks for go-cli-agent.
Then call finish with a one-line summary."
RT17_RAW="${RAW_DIR}/rt17-codex-proxy-audit.jsonl"
run_exec "$RT17_PROMPT" "$RT17_RAW" "$WORKSPACE_ROOT" 300
record_scenario "RT17" "Codex Responses Proxy Audit" "$RT17_RAW" "${ARTIFACT_DIR}/rt17-codex-proxy-audit.md" "$(extract_session_id "$RT17_RAW")"

RT18_PROMPT="${PROMPT_DIR}/rt18-opencode-task-review.prompt.txt"
write_prompt "$RT18_PROMPT" "Inspect only opencode/AGENTS.md, opencode/packages/opencode/AGENTS.md, opencode/README.md, opencode/packages/opencode/src/session/prompt.ts, opencode/packages/opencode/src/session/todo.ts, and opencode/packages/opencode/src/session/processor.ts.
Focus on large-project task execution, todo discipline, and prompt/reminder behavior.
Write ${ABS_ARTIFACT_DIR}/rt18-opencode-task-review.md with sections: confirmed strengths, likely tradeoffs, ideas go-cli-agent should adopt.
Then call finish with a one-line summary."
RT18_RAW="${RAW_DIR}/rt18-opencode-task-review.jsonl"
run_exec "$RT18_PROMPT" "$RT18_RAW" "$WORKSPACE_ROOT" 300
record_scenario "RT18" "OpenCode Task And Prompt Review" "$RT18_RAW" "${ARTIFACT_DIR}/rt18-opencode-task-review.md" "$(extract_session_id "$RT18_RAW")"

RT19_PROMPT="${PROMPT_DIR}/rt19-opencode-responses-audit.prompt.txt"
write_prompt "$RT19_PROMPT" "Inspect only opencode/specs/project.md, opencode/packages/opencode/src/provider/sdk/copilot/responses/openai-responses-language-model.ts, opencode/packages/opencode/src/provider/sdk/copilot/responses/map-openai-responses-finish-reason.ts, opencode/packages/opencode/src/provider/sdk/copilot/responses/convert-to-openai-responses-input.ts, and opencode/packages/opencode/src/provider/sdk/copilot/responses/openai-responses-prepare-tools.ts.
Focus on Responses replay, tool preparation, and finish-reason mapping.
Write ${ABS_ARTIFACT_DIR}/rt19-opencode-responses-audit.md with sections: confirmed behavior, differences from go-cli-agent assumptions, useful implementation ideas.
Then call finish with a one-line summary."
RT19_RAW="${RAW_DIR}/rt19-opencode-responses-audit.jsonl"
run_exec "$RT19_PROMPT" "$RT19_RAW" "$WORKSPACE_ROOT" 300
record_scenario "RT19" "OpenCode Responses Provider Audit" "$RT19_RAW" "${ARTIFACT_DIR}/rt19-opencode-responses-audit.md" "$(extract_session_id "$RT19_RAW")"

RT20_PROMPT="${PROMPT_DIR}/rt20-large-project-readiness.prompt.txt"
write_prompt "$RT20_PROMPT" "Inspect only go-cli-agent/README.md, go-cli-agent/AGENTS.md, go-cli-agent/spec/00-product.md, go-cli-agent/spec/01-runtime-architecture.md, go-cli-agent/spec/12-task-system.md, go-cli-agent/spec/13-live-input-and-steering.md, pi-coding-agent.md, bitter-lesson-agent-frameworks.md, learn-claude-code.md, codex/README.md, and opencode/README.md.
Use this evidence to judge whether go-cli-agent is ready to surpass codex and opencode on large-project development and review.
Write ${ABS_ARTIFACT_DIR}/rt20-large-project-readiness.md with sections: current strengths, remaining gaps, next architectural moves.
Then call finish with a one-line summary."
RT20_RAW="${RAW_DIR}/rt20-large-project-readiness.jsonl"
run_exec "$RT20_PROMPT" "$RT20_RAW" "$WORKSPACE_ROOT" 360
record_scenario "RT20" "Large Project Readiness Memo" "$RT20_RAW" "${ARTIFACT_DIR}/rt20-large-project-readiness.md" "$(extract_session_id "$RT20_RAW")"

printf '%s\n' "$RUN_DIR"
