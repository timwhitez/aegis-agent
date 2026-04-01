#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

: "${OPENAI_API_KEY:?OPENAI_API_KEY is required}"

MODEL="${GO_CLI_AGENT_LIVE_MODEL:-gpt-5.4}"
CONFIG_PATH="validation/config.openai-compatible.yaml"
ROUND_ID="${1:-$(date -u +%F)-openai-compatible-${MODEL}-round3-efficiency}"
RUN_DIR="validation/runs/${ROUND_ID}"
RAW_DIR="${RUN_DIR}/raw"
ARTIFACT_DIR="${RUN_DIR}/artifacts"
WORKSPACE_DIR="${RUN_DIR}/workspaces"

if [[ -e "$RUN_DIR" ]]; then
	echo "run directory already exists: $RUN_DIR" >&2
	exit 1
fi

mkdir -p "$RAW_DIR" "$ARTIFACT_DIR" "$WORKSPACE_DIR"

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

copy_workspace docset
copy_workspace incident
copy_workspace patch

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

RT01_PROMPT="${RAW_DIR}/rt01-architecture-audit.prompt.txt"
write_prompt "$RT01_PROMPT" "Audit the current repository for core v1 readiness.
Only inspect README.md, AGENTS.md, spec/*.md, cmd/go-cli-agent, internal/runtime, internal/provider, internal/tools, and internal/session.
Ignore validation/runs, validation/sessions, bin, tmp, and generated artifacts.
Start with a short todo plan. Prefer glob or grep_files to discover candidates, use grep only for exact line evidence, and keep read_file slices small.
Write ${ARTIFACT_DIR}/rt01-architecture-audit.md with sections: confirmed architecture map, top 5 risks ordered by severity, and smallest next fixes.
Then call finish with a one-line summary."
run_exec "$RT01_PROMPT" "${RAW_DIR}/rt01-architecture-audit.jsonl" "$ROOT_DIR"

RT02_PROMPT="${RAW_DIR}/rt02-provider-drift.prompt.txt"
write_prompt "$RT02_PROMPT" "Inspect only spec/03-provider-contracts.md, internal/provider/openai.go, internal/provider/anthropic.go, internal/provider/google.go, internal/provider/types.go, internal/runtime/runner.go, and internal/runtime/engine.go.
Ignore validation/runs, validation/sessions, bin, tmp, and generated artifacts.
Prefer targeted retrieval with small read_file slices.
Produce a Markdown drift report at ${ARTIFACT_DIR}/rt02-provider-drift.md with sections: confirmed alignments, confirmed drifts, and smallest fixes.
Then call finish with a one-line summary."
run_exec "$RT02_PROMPT" "${RAW_DIR}/rt02-provider-drift.jsonl" "$ROOT_DIR"

DOCSET_DIR="${WORKSPACE_DIR}/docset"
INCIDENT_DIR="${WORKSPACE_DIR}/incident"
PATCH_DIR="${WORKSPACE_DIR}/patch"

RT03_PROMPT="${RAW_DIR}/rt03-docset-synthesis.prompt.txt"
write_prompt "$RT03_PROMPT" "Read the local AGENTS.md and synthesize the Markdown docs in this workspace into reports/product-brief.md.
Keep the brief concise and evidence-backed.
Use sections: product goal, primary workflows, operator concerns, unanswered questions.
Then call finish with a one-line summary."
run_exec "$RT03_PROMPT" "${RAW_DIR}/rt03-docset-synthesis.jsonl" "$DOCSET_DIR"
copy_if_present "${DOCSET_DIR}/reports/product-brief.md" "${ARTIFACT_DIR}/rt03-product-brief.md"

RT04_PROMPT="${RAW_DIR}/rt04-incident-triage.prompt.txt"
write_prompt "$RT04_PROMPT" "Diagnose the incident in this workspace.
Read the local AGENTS.md first, then use logs, config, and rollout clues to build a short timeline before proposing a root cause.
Write reports/incident-summary.md with sections: confirmed timeline, likely root cause, smallest corrective action, open questions.
Then call finish with a one-line summary."
run_exec "$RT04_PROMPT" "${RAW_DIR}/rt04-incident-triage.jsonl" "$INCIDENT_DIR"
copy_if_present "${INCIDENT_DIR}/reports/incident-summary.md" "${ARTIFACT_DIR}/rt04-incident-summary.md"

RT05_PROMPT="${RAW_DIR}/rt05-patch-smallest-bug.prompt.txt"
write_prompt "$RT05_PROMPT" "Read the local AGENTS.md first.
Find the smallest correct bug fix in this workspace, make the code change, run the narrowest validation that proves the fix, and write reports/change-summary.md with sections: root cause, files changed, verification.
Then call finish with a one-line summary."
run_exec "$RT05_PROMPT" "${RAW_DIR}/rt05-patch-smallest-bug.jsonl" "$PATCH_DIR"
copy_if_present "${PATCH_DIR}/reports/change-summary.md" "${ARTIFACT_DIR}/rt05-change-summary.md"

RT06_PROMPT="${RAW_DIR}/rt06-explicit-skill-loading.prompt.txt"
write_prompt "$RT06_PROMPT" "Use the repo_audit skill for this task.
Audit whether the default core tool surface and docs stay aligned with core v1 boundaries.
Call load_skill before deeper inspection.
Write ${ARTIFACT_DIR}/rt06-repo-audit-skill.md with sections: validated findings, remaining risks, smallest next fixes.
Then call finish with a one-line summary."
run_exec "$RT06_PROMPT" "${RAW_DIR}/rt06-explicit-skill-loading.jsonl" "$ROOT_DIR"

RT07_PROMPT="${RAW_DIR}/rt07-command-tool-usage.prompt.txt"
write_prompt "$RT07_PROMPT" "Use the validation_helpers skill for this task.
Explicitly call markdown_inventory and pretty_json_args at least once when relevant, then synthesize this workspace into reports/tool-assisted-brief.md.
Use sections: scope, key docs, concise brief.
Then call finish with a one-line summary."
run_exec "$RT07_PROMPT" "${RAW_DIR}/rt07-command-tool-usage.jsonl" "$DOCSET_DIR"
copy_if_present "${DOCSET_DIR}/reports/tool-assisted-brief.md" "${ARTIFACT_DIR}/rt07-tool-assisted-brief.md"

RT08_RUN_PROMPT="${RAW_DIR}/rt08-awaiting-input-run.prompt.txt"
write_prompt "$RT08_RUN_PROMPT" "Review the docs in this workspace, draft a very short todo plan in assistant text, and stop naturally without calling finish once you need stakeholder input on whether to prioritize desktop onboarding or mobile onboarding."
run_run "$RT08_RUN_PROMPT" "${RAW_DIR}/rt08-awaiting-input-run.jsonl" "$DOCSET_DIR"
RT08_SESSION_ID="$(extract_session_id "${RAW_DIR}/rt08-awaiting-input-run.jsonl")"
./bin/go-cli-agent tasks "$RT08_SESSION_ID" --config "$CONFIG_PATH" --json >"${RAW_DIR}/rt08-awaiting-input-taskboard.json" 2>&1
./bin/go-cli-agent continue "$RT08_SESSION_ID" \
	--config "$CONFIG_PATH" \
	--provider openai-compatible \
	--model "$MODEL" \
	--json \
	--message "Prioritize desktop onboarding. Write reports/continue-brief.md with sections: chosen priority, supporting evidence, next steps. Then call finish." \
	>"${RAW_DIR}/rt08-awaiting-input-continue.jsonl" 2>&1
copy_if_present "${DOCSET_DIR}/reports/continue-brief.md" "${ARTIFACT_DIR}/rt08-continue-brief.md"

RT09_PROMPT="${RAW_DIR}/rt09-live-steer.prompt.txt"
write_prompt "$RT09_PROMPT" "Inspect only README.md, AGENTS.md, spec/04-tools-and-skills.md, internal/runtime/prompt.go, and internal/tools/registry.go.
Start with a short todo plan, use targeted retrieval only, and ignore validation/runs, validation/sessions, bin, tmp, and generated artifacts.
Write ${ARTIFACT_DIR}/rt09-steer-audit.md with sections: confirmed behavior, remaining risks, smallest next fixes.
Then call finish."
RT09_RAW="${RAW_DIR}/rt09-live-steer.jsonl"
./bin/go-cli-agent exec \
	--config "$CONFIG_PATH" \
	--provider openai-compatible \
	--model "$MODEL" \
	--workdir "$ROOT_DIR" \
	--json \
	--timeout 300 \
	<"$RT09_PROMPT" >"$RT09_RAW" 2>&1 &
RT09_PID="$!"
wait_for_pattern "$RT09_RAW" '"type":"session.started"' 90
RT09_SESSION_ID="$(extract_session_id "$RT09_RAW")"
wait_for_pattern "$RT09_RAW" '"tool_name":"read_file"' 120 || true
./bin/go-cli-agent steer "$RT09_SESSION_ID" \
	--config "$CONFIG_PATH" \
	--json \
	--interrupt \
	--message "Stop reading now. Use current evidence only, write ${ARTIFACT_DIR}/rt09-steer-audit.md immediately, and finish." \
	>"${RAW_DIR}/rt09-live-steer-command.json" 2>&1
for _ in $(seq 1 25); do
	if ! kill -0 "$RT09_PID" 2>/dev/null; then
		break
	fi
	sleep 1
done
if kill -0 "$RT09_PID" 2>/dev/null; then
	./bin/go-cli-agent steer "$RT09_SESSION_ID" \
		--config "$CONFIG_PATH" \
		--json \
		--interrupt \
		--message "Do not do more reads. Finish immediately with the artifact you already wrote or can write from current evidence." \
		>"${RAW_DIR}/rt09-live-steer-command-2.json" 2>&1
fi
wait "$RT09_PID"

RT10_PROMPT="${RAW_DIR}/rt10-durable-task-graph.prompt.txt"
write_prompt "$RT10_PROMPT" "Use the incident evidence in this workspace to build a durable task graph for triage.
Create at least four tasks with one explicit dependency edge, keep todo updated, use available evidence to move tasks to completed where appropriate, and write reports/taskboard-summary.md summarizing the final task board.
Then call finish with a one-line summary."
run_exec "$RT10_PROMPT" "${RAW_DIR}/rt10-durable-task-graph.jsonl" "$INCIDENT_DIR"
RT10_SESSION_ID="$(extract_session_id "${RAW_DIR}/rt10-durable-task-graph.jsonl")"
./bin/go-cli-agent tasks "$RT10_SESSION_ID" --config "$CONFIG_PATH" --json >"${RAW_DIR}/rt10-durable-task-graph-taskboard.json" 2>&1
copy_if_present "${INCIDENT_DIR}/reports/taskboard-summary.md" "${ARTIFACT_DIR}/rt10-taskboard-summary.md"

printf '%s\n' "$RUN_DIR"
