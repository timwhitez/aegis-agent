#!/usr/bin/env bash
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKSPACE_ROOT="$(cd "${ROOT_DIR}/.." && pwd)"
cd "$ROOT_DIR"

: "${OPENAI_API_KEY:?OPENAI_API_KEY is required}"

MODEL="${GO_CLI_AGENT_LIVE_MODEL:-gpt-5.4}"
CONFIG_PATH="validation/config.openai-compatible.yaml"
MATRIX_LABEL="${GO_CLI_AGENT_MATRIX_LABEL:-round11-complex-matrix-gapclose}"
ROUND_ID="${1:-$(date -u +%F)-openai-compatible-${MODEL}-${MATRIX_LABEL}}"
RUN_DIR="validation/runs/${ROUND_ID}"
RAW_DIR="${RUN_DIR}/raw"
PROMPT_DIR="${RUN_DIR}/prompts"
ARTIFACT_DIR="${RUN_DIR}/artifacts"
WORKSPACE_DIR="${RUN_DIR}/workspaces"
NOTE_DIR="${RUN_DIR}/notes"
SUMMARY_PATH="${RUN_DIR}/SUMMARY.md"
ISSUES_PATH="${RUN_DIR}/ISSUES.md"
ABORTED_PATH="${RUN_DIR}/ABORTED.md"
WORKSPACE_ARTIFACT_DIR="go-cli-agent/${ARTIFACT_DIR}"
WORKSPACE_RUN_DIR="go-cli-agent/${RUN_DIR}"

if [[ -e "$RUN_DIR" ]]; then
	echo "run directory already exists: $RUN_DIR" >&2
	exit 1
fi

mkdir -p "$RAW_DIR" "$PROMPT_DIR" "$ARTIFACT_DIR" "$WORKSPACE_DIR" "$NOTE_DIR"
ABS_ARTIFACT_DIR="$(cd "$ARTIFACT_DIR" && pwd)"
printf 'check\tstatus\texit_code\tpath\n' >"${NOTE_DIR}/preflight-index.tsv"
printf 'scenario_id\tlabel\tstatus\texit_code\traw\tartifact\tsession_id\n' >"${NOTE_DIR}/scenario-index.tsv"

TOTAL_SCENARIOS=0
PASSED_SCENARIOS=0
FAILED_SCENARIOS=0
SOFT_PREFLIGHT_FAILURES=0
HARD_ABORT_REASON=""
declare -a FAILED_SCENARIO_IDS=()
declare -a FAILED_SCENARIO_LABELS=()
declare -a FAILED_SCENARIO_RAWS=()
declare -a SOFT_PREFLIGHT_NAMES=()
declare -a SOFT_PREFLIGHT_PATHS=()

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
	if [[ ! -f "$path" ]]; then
		return 0
	fi
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

raw_contains() {
	local path="$1"
	local pattern="$2"
	[[ -f "$path" ]] && grep -Fq "$pattern" "$path"
}

wait_for_first_turn_resolution() {
	local raw_path="$1"
	local pid="$2"
	local timeout_sec="$3"
	local waited=0
	while (( waited < timeout_sec )); do
		if [[ -f "$raw_path" ]]; then
			for pattern in \
				'"type":"assistant.message"' \
				'"type":"tool.before"' \
				'"type":"session.failed"' \
				'"type":"session.completed"' \
				'"type":"session.awaiting_input"' \
				'"type":"provider.retry"'
			do
				if grep -Fq "$pattern" "$raw_path"; then
					return 0
				fi
			done
		fi
		if ! kill -0 "$pid" 2>/dev/null; then
			return 0
		fi
		sleep 1
		waited=$((waited + 1))
	done
	return 1
}

status_from_exit() {
	local exit_code="$1"
	if (( exit_code == 0 )); then
		printf 'passed'
	else
		printf 'failed'
	fi
}

merge_exit_code() {
	local current="$1"
	local next="$2"
	if (( current == 0 && next != 0 )); then
		printf '%s' "$next"
	else
		printf '%s' "$current"
	fi
}

failure_reason() {
	local raw_path="$1"
	if [[ ! -f "$raw_path" ]]; then
		printf 'raw output missing'
		return 0
	fi
	if grep -Fq 'token_invalidated' "$raw_path"; then
		printf 'provider auth path returned token_invalidated'
		return 0
	fi
	if grep -Fq 'upstream_timeout' "$raw_path"; then
		printf 'provider path hit upstream_timeout'
		return 0
	fi
	if grep -Fq '"type":"provider.retry"' "$raw_path"; then
		printf 'provider retry was emitted before the scenario failed'
		return 0
	fi
	if grep -Fq 'session is not running; use continue instead' "$raw_path"; then
		printf 'session control flow did not reach the required running state'
		return 0
	fi
	if grep -Fq 'timed out waiting for pattern' "$raw_path"; then
		printf 'scenario timed out before the expected runtime milestone'
		return 0
	fi
	tail -n 1 "$raw_path" | tr -d '\r'
}

record_preflight() {
	local name="$1"
	local exit_code="$2"
	local path="$3"
	printf '%s\t%s\t%s\t%s\n' "$name" "$(status_from_exit "$exit_code")" "$exit_code" "$path" >>"${NOTE_DIR}/preflight-index.tsv"
}

run_preflight() {
	local name="$1"
	local output_path="$2"
	shift 2
	"$@" >"$output_path" 2>&1
	local exit_code=$?
	record_preflight "$name" "$exit_code" "$output_path"
	return "$exit_code"
}

write_preflight_note() {
	local name="$1"
	local exit_code="$2"
	local output_path="$3"
	local note_path="${NOTE_DIR}/preflight-${name}.md"
	{
		echo "# Preflight ${name}"
		echo
		echo "- status: $(status_from_exit "$exit_code")"
		echo "- exit_code: ${exit_code}"
		echo "- output: \`${output_path}\`"
		if [[ -f "$output_path" ]]; then
			echo
			echo "## Tail"
			echo
			echo '```'
			tail -n 40 "$output_path"
			echo '```'
		fi
	} >"$note_path"
}

record_scenario() {
	local scenario_id="$1"
	local label="$2"
	local status="$3"
	local exit_code="$4"
	local raw_path="$5"
	local artifact_path="$6"
	local session_id="${7:-}"
	printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$scenario_id" "$label" "$status" "$exit_code" "$raw_path" "$artifact_path" "$session_id" >>"${NOTE_DIR}/scenario-index.tsv"
}

write_scenario_note() {
	local scenario_id="$1"
	local label="$2"
	local status="$3"
	local exit_code="$4"
	local raw_path="$5"
	local artifact_path="$6"
	local session_id="${7:-}"
	local note_path="${NOTE_DIR}/${scenario_id}.md"
	{
		echo "# ${scenario_id} ${label}"
		echo
		echo "- status: ${status}"
		echo "- exit_code: ${exit_code}"
		echo "- raw: \`${raw_path}\`"
		echo "- artifact: \`${artifact_path}\`"
		echo "- session_id: \`${session_id:-}\`"
		if [[ "$status" != "passed" ]]; then
			echo "- failure_reason: $(failure_reason "$raw_path")"
		fi
		if [[ -f "$raw_path" ]]; then
			echo
			echo "## Tail"
			echo
			echo '```'
			tail -n 40 "$raw_path"
			echo '```'
		fi
	} >"$note_path"
}

finalize_scenario() {
	local scenario_id="$1"
	local label="$2"
	local exit_code="$3"
	local raw_path="$4"
	local artifact_path="$5"
	local session_id="${6:-}"
	local status
	status="$(status_from_exit "$exit_code")"
	TOTAL_SCENARIOS=$((TOTAL_SCENARIOS + 1))
	if [[ "$status" == "passed" ]]; then
		PASSED_SCENARIOS=$((PASSED_SCENARIOS + 1))
	else
		FAILED_SCENARIOS=$((FAILED_SCENARIOS + 1))
		FAILED_SCENARIO_IDS+=("$scenario_id")
		FAILED_SCENARIO_LABELS+=("$label")
		FAILED_SCENARIO_RAWS+=("$raw_path")
	fi
	record_scenario "$scenario_id" "$label" "$status" "$exit_code" "$raw_path" "$artifact_path" "$session_id"
	write_scenario_note "$scenario_id" "$label" "$status" "$exit_code" "$raw_path" "$artifact_path" "$session_id"
}

run_exec() {
	local prompt_path="$1"
	local raw_path="$2"
	local workdir="$3"
	local timeout_sec="${4:-300}"
	local first_turn_timeout_sec="${5:-45}"
	if (( timeout_sec < 420 )); then
		timeout_sec=420
	fi
	./bin/go-cli-agent exec \
		--config "$CONFIG_PATH" \
		--provider openai-compatible \
		--model "$MODEL" \
		--workdir "$workdir" \
		--json \
		--timeout "$timeout_sec" \
		<"$prompt_path" >"$raw_path" 2>&1 &
	local pid="$!"
	if ! wait_for_first_turn_resolution "$raw_path" "$pid" "$first_turn_timeout_sec"; then
		printf 'matrix_watchdog: no first-turn resolution within %ss\n' "$first_turn_timeout_sec" >>"$raw_path"
		kill "$pid" 2>/dev/null || true
		wait "$pid" 2>/dev/null || true
		return 124
	fi
	wait "$pid"
}

run_exec_exact() {
	local prompt_path="$1"
	local raw_path="$2"
	local workdir="$3"
	local timeout_sec="${4:-300}"
	local first_turn_timeout_sec="${5:-20}"
	./bin/go-cli-agent exec \
		--config "$CONFIG_PATH" \
		--provider openai-compatible \
		--model "$MODEL" \
		--workdir "$workdir" \
		--json \
		--timeout "$timeout_sec" \
		<"$prompt_path" >"$raw_path" 2>&1 &
	local pid="$!"
	if ! wait_for_first_turn_resolution "$raw_path" "$pid" "$first_turn_timeout_sec"; then
		printf 'matrix_watchdog: no first-turn resolution within %ss\n' "$first_turn_timeout_sec" >>"$raw_path"
		kill "$pid" 2>/dev/null || true
		wait "$pid" 2>/dev/null || true
		return 124
	fi
	wait "$pid"
}

run_run() {
	local prompt_path="$1"
	local raw_path="$2"
	local workdir="$3"
	local timeout_sec="${4:-300}"
	local first_turn_timeout_sec="${5:-45}"
	if (( timeout_sec < 420 )); then
		timeout_sec=420
	fi
	./bin/go-cli-agent run \
		--config "$CONFIG_PATH" \
		--provider openai-compatible \
		--model "$MODEL" \
		--workdir "$workdir" \
		--json \
		--timeout "$timeout_sec" \
		<"$prompt_path" >"$raw_path" 2>&1 &
	local pid="$!"
	if ! wait_for_first_turn_resolution "$raw_path" "$pid" "$first_turn_timeout_sec"; then
		printf 'matrix_watchdog: no first-turn resolution within %ss\n' "$first_turn_timeout_sec" >>"$raw_path"
		kill "$pid" 2>/dev/null || true
		wait "$pid" 2>/dev/null || true
		return 124
	fi
	wait "$pid"
}

copy_if_present() {
	local src="$1"
	local dst="$2"
	if [[ -f "$src" ]]; then
		cp "$src" "$dst"
	fi
}

write_summary() {
	{
		echo "# Round11 Summary"
		echo
		echo "## Run metadata"
		echo
		echo "- Run directory: \`${RUN_DIR}\`"
		echo "- Provider: \`openai-compatible\`"
		echo "- Wire API: \`responses\`"
		echo "- Model: \`${MODEL}\`"
		echo "- Base URL: \`http://69.63.215.40:24634/v1\`"
		echo "- Preflight index: \`notes/preflight-index.tsv\`"
		echo "- Scenario index: \`notes/scenario-index.tsv\`"
		if [[ -n "$HARD_ABORT_REASON" ]]; then
			echo "- Matrix status: aborted before scenario execution."
			echo "- Abort reason: ${HARD_ABORT_REASON}"
		else
			echo "- Matrix status: ${PASSED_SCENARIOS}/${TOTAL_SCENARIOS} scenarios passed; ${FAILED_SCENARIOS} failed."
		fi
		if (( SOFT_PREFLIGHT_FAILURES > 0 )); then
			echo
			echo "## Soft preflight failures"
			echo
			for i in "${!SOFT_PREFLIGHT_NAMES[@]}"; do
				echo "- ${SOFT_PREFLIGHT_NAMES[$i]} failed."
				echo "  output: \`${SOFT_PREFLIGHT_PATHS[$i]}\`"
			done
		fi
		if (( FAILED_SCENARIOS > 0 )); then
			echo
			echo "## Failed scenarios"
			echo
			for i in "${!FAILED_SCENARIO_IDS[@]}"; do
				echo "- ${FAILED_SCENARIO_IDS[$i]} ${FAILED_SCENARIO_LABELS[$i]}: $(failure_reason "${FAILED_SCENARIO_RAWS[$i]}")"
				echo "  raw: \`${FAILED_SCENARIO_RAWS[$i]}\`"
			done
		fi
		if [[ -z "$HARD_ABORT_REASON" && "$FAILED_SCENARIOS" -eq 0 ]]; then
			echo
			echo "## Matrix conclusion"
			echo
			echo "- All planned scenarios completed without a scenario-level command failure."
			if (( SOFT_PREFLIGHT_FAILURES == 0 )); then
				echo "- Build, test, doctor, probe, and the realistic preflight turn all passed before or during the matrix."
			else
				echo "- Some soft preflight checks failed, but the full scenario matrix still completed."
			fi
		fi
	} >"$SUMMARY_PATH"
}

write_issues() {
	{
		echo "# Round11 Issues"
		echo
		if [[ -n "$HARD_ABORT_REASON" || "$FAILED_SCENARIOS" -gt 0 || "$SOFT_PREFLIGHT_FAILURES" -gt 0 ]]; then
			echo "## Open issues"
			echo
			if [[ -n "$HARD_ABORT_REASON" ]]; then
				echo "1. High: matrix aborted before the scenario suite could finish."
				echo "   Evidence: \`${ABORTED_PATH}\` and \`notes/preflight-index.tsv\`."
				echo "   Why it matters: local regression was not clean enough to support real-task acceptance."
				echo "   Smallest next fix: resolve the hard preflight failure and rerun the matrix from a fresh run directory."
				echo
			fi
			for i in "${!SOFT_PREFLIGHT_NAMES[@]}"; do
				local_index=$((i + 1))
				echo "${local_index}. Medium: soft preflight check \`${SOFT_PREFLIGHT_NAMES[$i]}\` failed."
				echo "   Evidence: \`${SOFT_PREFLIGHT_PATHS[$i]}\`."
				echo "   Why it matters: the provider path or control path showed instability before or during the main matrix."
				echo "   Smallest next fix: inspect the captured output, stabilize the provider/session path, and compare against the scenario failures before the next rerun."
				echo
			done
			offset=$((SOFT_PREFLIGHT_FAILURES + 1))
			for i in "${!FAILED_SCENARIO_IDS[@]}"; do
				local_index=$((offset + i))
				echo "${local_index}. High: scenario \`${FAILED_SCENARIO_IDS[$i]}\` failed."
				echo "   Evidence: \`${FAILED_SCENARIO_RAWS[$i]}\` plus \`notes/${FAILED_SCENARIO_IDS[$i]}.md\`."
				echo "   Why it matters: this scenario did not produce trustworthy acceptance evidence for the targeted real-task capability."
				echo "   Smallest next fix: resolve the concrete failure reason recorded for this scenario, then rerun the matrix from a fresh run directory."
				echo
			done
		else
			echo "## Open issues"
			echo
			echo "No open issues recorded by this matrix run."
		fi
	} >"$ISSUES_PATH"
}

write_aborted_note() {
	local reason="$1"
	{
		echo "# Round11 Aborted"
		echo
		echo "- Run directory: \`${RUN_DIR}\`"
		echo "- Reason: ${reason}"
		echo "- Preflight index: \`notes/preflight-index.tsv\`"
	} >"$ABORTED_PATH"
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

mapfile -t TOP_LEVEL_MARKDOWN < <(cd "$WORKSPACE_ROOT" && find . -maxdepth 1 -type f -name '*.md' -printf '%P\n' | sort)
TOP_LEVEL_MARKDOWN_LIST="$(printf '%s, ' "${TOP_LEVEL_MARKDOWN[@]}")"
TOP_LEVEL_MARKDOWN_LIST="${TOP_LEVEL_MARKDOWN_LIST%, }"

run_preflight "build" "${RAW_DIR}/preflight-build.txt" ./build.sh
PRE_BUILD_EXIT=$?
write_preflight_note "build" "$PRE_BUILD_EXIT" "${RAW_DIR}/preflight-build.txt"
if (( PRE_BUILD_EXIT != 0 )); then
	HARD_ABORT_REASON="./build.sh failed"
	write_aborted_note "$HARD_ABORT_REASON"
	write_summary
	write_issues
	printf '%s\n' "$RUN_DIR"
	exit 1
fi

run_preflight "test" "${RAW_DIR}/preflight-test.txt" ./test.sh
PRE_TEST_EXIT=$?
write_preflight_note "test" "$PRE_TEST_EXIT" "${RAW_DIR}/preflight-test.txt"
if (( PRE_TEST_EXIT != 0 )); then
	HARD_ABORT_REASON="./test.sh failed"
	write_aborted_note "$HARD_ABORT_REASON"
	write_summary
	write_issues
	printf '%s\n' "$RUN_DIR"
	exit 1
fi

run_preflight "doctor" "${RAW_DIR}/preflight-doctor.json" ./bin/go-cli-agent doctor \
	--config "$CONFIG_PATH" \
	--provider openai-compatible \
	--json
PRE_DOCTOR_EXIT=$?
write_preflight_note "doctor" "$PRE_DOCTOR_EXIT" "${RAW_DIR}/preflight-doctor.json"
if (( PRE_DOCTOR_EXIT != 0 )); then
	SOFT_PREFLIGHT_FAILURES=$((SOFT_PREFLIGHT_FAILURES + 1))
	SOFT_PREFLIGHT_NAMES+=("doctor")
	SOFT_PREFLIGHT_PATHS+=("${RAW_DIR}/preflight-doctor.json")
fi

run_preflight "probe" "${RAW_DIR}/preflight-probe.json" ./bin/go-cli-agent probe-provider \
	--config "$CONFIG_PATH" \
	--provider openai-compatible \
	--json
PRE_PROBE_EXIT=$?
write_preflight_note "probe" "$PRE_PROBE_EXIT" "${RAW_DIR}/preflight-probe.json"
if (( PRE_PROBE_EXIT != 0 )); then
	SOFT_PREFLIGHT_FAILURES=$((SOFT_PREFLIGHT_FAILURES + 1))
	SOFT_PREFLIGHT_NAMES+=("probe")
	SOFT_PREFLIGHT_PATHS+=("${RAW_DIR}/preflight-probe.json")
fi

PREFLIGHT_REAL_PROMPT="${PROMPT_DIR}/preflight-real-turn.prompt.txt"
write_prompt "$PREFLIGHT_REAL_PROMPT" "Inspect only README.md and AGENTS.md in the current go-cli-agent repository.
Use targeted retrieval only.
Call finish with a short message that states the current core-v1 default command surface and whether experimental commands sit behind an explicit entrypoint."
run_exec_exact "$PREFLIGHT_REAL_PROMPT" "${RAW_DIR}/preflight-real-turn.jsonl" "$ROOT_DIR" 20 20
PRE_REAL_EXIT=$?
record_preflight "real-turn" "$PRE_REAL_EXIT" "${RAW_DIR}/preflight-real-turn.jsonl"
write_preflight_note "real-turn" "$PRE_REAL_EXIT" "${RAW_DIR}/preflight-real-turn.jsonl"
if (( PRE_REAL_EXIT != 0 )); then
	SOFT_PREFLIGHT_FAILURES=$((SOFT_PREFLIGHT_FAILURES + 1))
	SOFT_PREFLIGHT_NAMES+=("real-turn")
	SOFT_PREFLIGHT_PATHS+=("${RAW_DIR}/preflight-real-turn.jsonl")
fi

RT01_PROMPT="${PROMPT_DIR}/rt01-architecture-audit.prompt.txt"
write_prompt "$RT01_PROMPT" "Use the review_pipeline skill for this task.
Audit the current go-cli-agent repository for core v1 surface discipline after the latest gap-close pass.
Only inspect README.md, AGENTS.md, spec/00-product.md, spec/01-runtime-architecture.md, spec/02-cli-and-config.md, spec/08-sdk-and-api-evolution.md, spec/09-phase-plan.md, pkg/agent/agent.go, internal/app/app.go, internal/app/orchestration.go, internal/app/tui_cmd.go, internal/runtime, internal/provider, internal/tools, and internal/session.
Ignore validation/runs, validation/sessions, reports, bin, tmp, and generated artifacts.
Start with a short todo plan. Prefer targeted retrieval. Validate whether the default help surface is core-only, whether experimental routing stays outside the default operator path, whether core/experimental/store app-facing facades are truly split, and whether the public SDK facade keeps extension-only surfaces out of the default core runner.
Write ${ABS_ARTIFACT_DIR}/rt01-architecture-audit.md with sections: core surface map, findings, unresolved questions, smallest next fixes.
Then call finish with a one-line summary."
RT01_RAW="${RAW_DIR}/rt01-architecture-audit.jsonl"
run_exec "$RT01_PROMPT" "$RT01_RAW" "$ROOT_DIR" 300
RT01_EXIT=$?
finalize_scenario "RT01" "Core Surface Boundary Audit" "$RT01_EXIT" "$RT01_RAW" "${ARTIFACT_DIR}/rt01-architecture-audit.md" "$(extract_session_id "$RT01_RAW")"

RT02_PROMPT="${PROMPT_DIR}/rt02-provider-drift.prompt.txt"
write_prompt "$RT02_PROMPT" "Use the review_pipeline skill for this task.
Inspect only spec/03-provider-contracts.md, internal/provider/openai.go, internal/provider/anthropic.go, internal/provider/google.go, internal/provider/types.go, internal/provider/http.go, internal/runtime/runner.go, and internal/runtime/engine.go.
Ignore validation/runs, validation/sessions, reports, bin, tmp, and generated artifacts.
Prefer targeted retrieval with small read_file slices. Keep validated alignments separate from confirmed drifts. Focus especially on provider retry evidence, TurnRequest.Metadata propagation, provider_response_id consistency, and whether raw_provider exposes one normalized stop-reason envelope across adapters.
Write ${ABS_ARTIFACT_DIR}/rt02-provider-drift.md with sections: confirmed alignments, findings, unresolved questions, smallest fixes.
Then call finish with a one-line summary."
RT02_RAW="${RAW_DIR}/rt02-provider-drift.jsonl"
run_exec "$RT02_PROMPT" "$RT02_RAW" "$ROOT_DIR" 300
RT02_EXIT=$?
finalize_scenario "RT02" "Provider Metadata And Retry Audit" "$RT02_EXIT" "$RT02_RAW" "${ARTIFACT_DIR}/rt02-provider-drift.md" "$(extract_session_id "$RT02_RAW")"

RT03_PROMPT="${PROMPT_DIR}/rt03-top-level-md-synthesis.prompt.txt"
write_prompt "$RT03_PROMPT" "Inspect only these top-level Markdown files in the workspace root: ${TOP_LEVEL_MARKDOWN_LIST}.
Do not inspect go-cli-agent, codex, or opencode source code for this task.
Synthesize the recurring principles, tensions, and architecture moves that a serious CLI coding/review harness should import.
Write ${ABS_ARTIFACT_DIR}/rt03-top-level-md-synthesis.md with sections: recurring principles, useful tensions, concrete architecture moves, weak signals or contradictions.
Then call finish with a one-line summary."
RT03_RAW="${RAW_DIR}/rt03-top-level-md-synthesis.jsonl"
run_exec "$RT03_PROMPT" "$RT03_RAW" "$WORKSPACE_ROOT" 360
RT03_EXIT=$?
finalize_scenario "RT03" "Top-Level Markdown Corpus Synthesis" "$RT03_EXIT" "$RT03_RAW" "${ARTIFACT_DIR}/rt03-top-level-md-synthesis.md" "$(extract_session_id "$RT03_RAW")"

RT04_PROMPT="${PROMPT_DIR}/rt04-review-pipeline.prompt.txt"
write_prompt "$RT04_PROMPT" "Use the review_pipeline skill for this task.
Inspect only README.md, AGENTS.md, spec/11-spec-audit-and-traceability.md, internal/runtime/prompt.go, internal/runtime/prompt_test.go, internal/runtime/review_guard.go, internal/review/report.go, internal/review/report_test.go, validation/skills/repo_audit/SKILL.md, and validation/skills/review_pipeline/SKILL.md.
Ignore validation/runs, validation/sessions, reports, bin, tmp, and generated artifacts.
Audit whether go-cli-agent now enforces review quality beyond structural Markdown shape. Validate readable path:line evidence requirements, snippet-backed evidence verification, unresolved-content checks, and runtime activation scope.
Write ${ABS_ARTIFACT_DIR}/rt04-review-pipeline.md with sections: current pipeline, findings, remaining risks, unresolved questions, smallest next fixes.
Then call finish with a one-line summary."
RT04_RAW="${RAW_DIR}/rt04-review-pipeline.jsonl"
run_exec "$RT04_PROMPT" "$RT04_RAW" "$ROOT_DIR" 300
RT04_EXIT=$?
finalize_scenario "RT04" "Review Evidence Pipeline Drill" "$RT04_EXIT" "$RT04_RAW" "${ARTIFACT_DIR}/rt04-review-pipeline.md" "$(extract_session_id "$RT04_RAW")"

RT05_PROMPT="${PROMPT_DIR}/rt05-compaction-memo.prompt.txt"
write_prompt "$RT05_PROMPT" "Inspect only these top-level Markdown files in the workspace root: ${TOP_LEVEL_MARKDOWN_LIST}, plus go-cli-agent/spec/10-context-compaction.md, go-cli-agent/internal/runtime/compaction.go, go-cli-agent/internal/runtime/prompt.go, and go-cli-agent/spec/01-runtime-architecture.md.
Also inspect go-cli-agent/internal/runtime/compaction_test.go and go-cli-agent/internal/runtime/project_memory.go.
Focus on context compaction, artifact-memory reuse, durable project-memory excerpts, pinned high_value_proofs, and whether retrieval-tail pressure still leaves enough room for reserved final proof reads.
Write ${ABS_ARTIFACT_DIR}/rt05-compaction-memo.md with sections: confirmed behavior, likely failure modes, smallest next fixes.
Then call finish with a one-line summary."
RT05_RAW="${RAW_DIR}/rt05-compaction-memo.jsonl"
run_exec "$RT05_PROMPT" "$RT05_RAW" "$WORKSPACE_ROOT" 360
RT05_EXIT=$?
finalize_scenario "RT05" "Compaction Pressure Architecture Memo" "$RT05_EXIT" "$RT05_RAW" "${ARTIFACT_DIR}/rt05-compaction-memo.md" "$(extract_session_id "$RT05_RAW")"

RT06_PROMPT="${PROMPT_DIR}/rt06-incident-triage.prompt.txt"
write_prompt "$RT06_PROMPT" "Use the incident_triage skill for this task.
Diagnose the incident in this workspace.
Read the local AGENTS.md first, then use logs and service_config.json to build a short timeline before proposing a root cause.
Ignore reports/.
Write reports/incident-summary.md with sections: confirmed timeline, likely root cause, smallest corrective action, open questions.
Then call finish with a one-line summary."
RT06_RAW="${RAW_DIR}/rt06-incident-triage.jsonl"
run_exec "$RT06_PROMPT" "$RT06_RAW" "$INCIDENT_DIR" 240
RT06_EXIT=$?
copy_if_present "${INCIDENT_DIR}/reports/incident-summary.md" "${ARTIFACT_DIR}/rt06-incident-summary.md"
finalize_scenario "RT06" "Incident Triage" "$RT06_EXIT" "$RT06_RAW" "${ARTIFACT_DIR}/rt06-incident-summary.md" "$(extract_session_id "$RT06_RAW")"

RT07_PROMPT="${PROMPT_DIR}/rt07-durable-task-graph.prompt.txt"
write_prompt "$RT07_PROMPT" "Use the incident evidence in this workspace to build a durable task graph for triage.
Create at least four tasks with one explicit dependency edge, keep todo updated, and also maintain a durable project-memory stack under reports/spec.md, reports/plan.md, reports/progress.md, and reports/validation.md.
Use available evidence to move tasks to completed where appropriate, write reports/taskboard-summary.md summarizing the final task board, and keep unresolved questions separate from completed tasks.
Then call finish with a one-line summary."
RT07_RAW="${RAW_DIR}/rt07-durable-task-graph.jsonl"
run_exec "$RT07_PROMPT" "$RT07_RAW" "$INCIDENT_DIR" 300
RT07_EXIT=$?
RT07_SESSION_ID="$(extract_session_id "$RT07_RAW")"
if [[ -n "$RT07_SESSION_ID" ]]; then
	./bin/go-cli-agent tasks "$RT07_SESSION_ID" --config "$CONFIG_PATH" --json >"${RAW_DIR}/rt07-durable-task-graph-taskboard.json" 2>&1
	RT07_TASKS_EXIT=$?
	RT07_EXIT="$(merge_exit_code "$RT07_EXIT" "$RT07_TASKS_EXIT")"
else
	RT07_EXIT="$(merge_exit_code "$RT07_EXIT" 1)"
fi
copy_if_present "${INCIDENT_DIR}/reports/taskboard-summary.md" "${ARTIFACT_DIR}/rt07-taskboard-summary.md"
finalize_scenario "RT07" "Durable Task Graph" "$RT07_EXIT" "$RT07_RAW" "${ARTIFACT_DIR}/rt07-taskboard-summary.md" "$RT07_SESSION_ID"

RT08_PROMPT="${PROMPT_DIR}/rt08-python-patch.prompt.txt"
write_prompt "$RT08_PROMPT" "Read the local AGENTS.md first.
Ignore reports/.
Find the smallest correct bug fix in this Python workspace, make the code change, run the narrowest validation that proves the fix, and write reports/change-summary.md with sections: root cause, files changed, verification.
Then call finish with a one-line summary."
RT08_RAW="${RAW_DIR}/rt08-python-patch.jsonl"
run_exec "$RT08_PROMPT" "$RT08_RAW" "$PATCH_DIR" 300
RT08_EXIT=$?
copy_if_present "${PATCH_DIR}/reports/change-summary.md" "${ARTIFACT_DIR}/rt08-python-change-summary.md"
finalize_scenario "RT08" "Python Patch Smallest Bug" "$RT08_EXIT" "$RT08_RAW" "${ARTIFACT_DIR}/rt08-python-change-summary.md" "$(extract_session_id "$RT08_RAW")"

RT09_PROMPT="${PROMPT_DIR}/rt09-go-patch.prompt.txt"
write_prompt "$RT09_PROMPT" "Read the local AGENTS.md first.
Ignore reports/.
Find the smallest correct bug fix in this Go workspace, make the code change, run the narrowest validation that proves the fix, and write reports/change-summary.md with sections: root cause, files changed, verification.
Then call finish with a one-line summary."
RT09_RAW="${RAW_DIR}/rt09-go-patch.jsonl"
run_exec "$RT09_PROMPT" "$RT09_RAW" "$PATCH_GO_DIR" 300
RT09_EXIT=$?
copy_if_present "${PATCH_GO_DIR}/reports/change-summary.md" "${ARTIFACT_DIR}/rt09-go-change-summary.md"
finalize_scenario "RT09" "Go Patch Smallest Bug" "$RT09_EXIT" "$RT09_RAW" "${ARTIFACT_DIR}/rt09-go-change-summary.md" "$(extract_session_id "$RT09_RAW")"

RT10_PROMPT="${PROMPT_DIR}/rt10-nested-review.prompt.txt"
write_prompt "$RT10_PROMPT" "Use the review_pipeline skill for this task.
Read the applicable AGENTS.md chain first.
Review only README.md, handler.go, and handler_test.go in this directory.
Write reports/api-review.md with sections: findings, unresolved questions, smallest fixes.
Put findings first, ordered by severity, and include confidence plus concrete evidence references.
Then call finish with a one-line summary."
RT10_RAW="${RAW_DIR}/rt10-nested-review.jsonl"
run_exec "$RT10_PROMPT" "$RT10_RAW" "$NESTED_API_DIR" 240
RT10_EXIT=$?
copy_if_present "${NESTED_API_DIR}/reports/api-review.md" "${ARTIFACT_DIR}/rt10-api-review.md"
finalize_scenario "RT10" "Nested AGENTS API Review" "$RT10_EXIT" "$RT10_RAW" "${ARTIFACT_DIR}/rt10-api-review.md" "$(extract_session_id "$RT10_RAW")"

RT11_PROMPT="${PROMPT_DIR}/rt11-explicit-skill-loading.prompt.txt"
write_prompt "$RT11_PROMPT" "Use the repo_audit and review_pipeline skills for this task.
Audit whether the default core tool surface and docs stay aligned with core v1 boundaries.
Call load_skill before deeper inspection.
Ignore validation/runs, validation/sessions, reports, bin, tmp, and generated artifacts.
Keep validated findings behavior-proved. If a point depends on default exposure, registration, or gating, read the owning code path before validating it.
Write ${ABS_ARTIFACT_DIR}/rt11-repo-audit-skill.md with sections: validated findings, finding records, remaining risks, unresolved questions, smallest next fixes.
Then call finish with a one-line summary."
RT11_RAW="${RAW_DIR}/rt11-explicit-skill-loading.jsonl"
run_exec "$RT11_PROMPT" "$RT11_RAW" "$ROOT_DIR" 300
RT11_EXIT=$?
finalize_scenario "RT11" "Explicit Skill Loading" "$RT11_EXIT" "$RT11_RAW" "${ARTIFACT_DIR}/rt11-repo-audit-skill.md" "$(extract_session_id "$RT11_RAW")"

RT12_PROMPT="${PROMPT_DIR}/rt12-command-tool-usage.prompt.txt"
write_prompt "$RT12_PROMPT" "Use the validation_helpers skill for this task.
Explicitly call markdown_inventory and pretty_json_args at least once when relevant, then synthesize this workspace into reports/tool-assisted-brief.md.
Ignore reports/ while gathering evidence.
Use sections: scope, key docs, concise brief.
Then call finish with a one-line summary."
RT12_RAW="${RAW_DIR}/rt12-command-tool-usage.jsonl"
run_exec "$RT12_PROMPT" "$RT12_RAW" "$DOCSET_DIR" 240
RT12_EXIT=$?
copy_if_present "${DOCSET_DIR}/reports/tool-assisted-brief.md" "${ARTIFACT_DIR}/rt12-tool-assisted-brief.md"
finalize_scenario "RT12" "Command Tool Usage" "$RT12_EXIT" "$RT12_RAW" "${ARTIFACT_DIR}/rt12-tool-assisted-brief.md" "$(extract_session_id "$RT12_RAW")"

RT13_RUN_PROMPT="${PROMPT_DIR}/rt13-awaiting-input-run.prompt.txt"
write_prompt "$RT13_RUN_PROMPT" "Review only product_overview.md, ops_notes.md, and release_constraints.md in this workspace.
Draft a very short todo plan in assistant text, create reports/spec.md with a scoped problem statement, and stop naturally without calling finish once you need stakeholder input on whether to prioritize desktop onboarding or mobile onboarding."
RT13_RUN_RAW="${RAW_DIR}/rt13-awaiting-input-run.jsonl"
run_run "$RT13_RUN_PROMPT" "$RT13_RUN_RAW" "$DOCSET_DIR" 240
RT13_EXIT=$?
RT13_SESSION_ID="$(extract_session_id "$RT13_RUN_RAW")"
if [[ -n "$RT13_SESSION_ID" ]]; then
	./bin/go-cli-agent tasks "$RT13_SESSION_ID" --config "$CONFIG_PATH" --json >"${RAW_DIR}/rt13-awaiting-input-taskboard.json" 2>&1
	RT13_TASKS_EXIT=$?
	RT13_EXIT="$(merge_exit_code "$RT13_EXIT" "$RT13_TASKS_EXIT")"
	./bin/go-cli-agent continue "$RT13_SESSION_ID" \
		--config "$CONFIG_PATH" \
		--provider openai-compatible \
		--model "$MODEL" \
		--json \
		--message "Prioritize desktop onboarding. Refresh the durable memory stack under reports/, update reports/plan.md and reports/validation.md, and write reports/continue-brief.md with sections: chosen priority, supporting evidence, next steps, unresolved questions. Then call finish." \
		>"${RAW_DIR}/rt13-awaiting-input-continue.jsonl" 2>&1
	RT13_CONTINUE_EXIT=$?
	RT13_EXIT="$(merge_exit_code "$RT13_EXIT" "$RT13_CONTINUE_EXIT")"
else
	RT13_EXIT="$(merge_exit_code "$RT13_EXIT" 1)"
fi
copy_if_present "${DOCSET_DIR}/reports/continue-brief.md" "${ARTIFACT_DIR}/rt13-continue-brief.md"
RT13_FINAL_RAW="${RAW_DIR}/rt13-awaiting-input-continue.jsonl"
if [[ ! -f "$RT13_FINAL_RAW" ]]; then
	RT13_FINAL_RAW="$RT13_RUN_RAW"
fi
finalize_scenario "RT13" "Awaiting Input And Continue" "$RT13_EXIT" "$RT13_FINAL_RAW" "${ARTIFACT_DIR}/rt13-continue-brief.md" "$RT13_SESSION_ID"

RT14_PROMPT="${PROMPT_DIR}/rt14-live-steer.prompt.txt"
write_prompt "$RT14_PROMPT" "Use the review_pipeline skill for this task.
Inspect only README.md, AGENTS.md, spec/10-context-compaction.md, spec/13-live-input-and-steering.md, internal/runtime/engine.go, internal/runtime/engine_test.go, internal/runtime/runner.go, internal/runtime/runner_test.go, internal/runtime/prompt.go, internal/runtime/review_guard.go, internal/runtime/project_memory.go, and internal/tools/registry.go.
Start with a short todo plan, use targeted retrieval only, and ignore validation/runs, validation/sessions, reports, bin, tmp, and generated artifacts.
Write ${ABS_ARTIFACT_DIR}/rt14-steer-audit.md with sections: confirmed behavior, finding records, remaining risks, behavior-level proof status, smallest next fixes.
Then call finish."
RT14_RAW="${RAW_DIR}/rt14-live-steer.jsonl"
./bin/go-cli-agent exec \
	--config "$CONFIG_PATH" \
	--provider openai-compatible \
	--model "$MODEL" \
	--workdir "$ROOT_DIR" \
	--json \
	--timeout 420 \
	<"$RT14_PROMPT" >"$RT14_RAW" 2>&1 &
RT14_PID="$!"
RT14_EXIT=0
wait_for_pattern "$RT14_RAW" '"type":"session.started"' 90
RT14_WAIT_START_EXIT=$?
RT14_EXIT="$(merge_exit_code "$RT14_EXIT" "$RT14_WAIT_START_EXIT")"
RT14_SESSION_ID="$(extract_session_id "$RT14_RAW")"
wait_for_pattern "$RT14_RAW" '"tool_name":"read_file"' 120 || true
if [[ -n "$RT14_SESSION_ID" ]]; then
	./bin/go-cli-agent steer "$RT14_SESSION_ID" \
		--config "$CONFIG_PATH" \
		--json \
		--interrupt \
		--message "Stop reading now. Use current evidence only, write ${ABS_ARTIFACT_DIR}/rt14-steer-audit.md immediately, and finish." \
		>"${RAW_DIR}/rt14-live-steer-command.json" 2>&1
	RT14_STEER1_EXIT=$?
	RT14_EXIT="$(merge_exit_code "$RT14_EXIT" "$RT14_STEER1_EXIT")"
else
	RT14_EXIT="$(merge_exit_code "$RT14_EXIT" 1)"
fi
RT14_ARTIFACT_WRITTEN_BEFORE_SECOND_STEER=0
RT14_FINISH_OBSERVED_BEFORE_SECOND_STEER=0
RT14_SECOND_STEER_SENT=0
RT14_SECOND_STEER_ACTUALLY_NEEDED=0
for _ in $(seq 1 35); do
	if ! kill -0 "$RT14_PID" 2>/dev/null; then
		break
	fi
	if [[ -f "${ARTIFACT_DIR}/rt14-steer-audit.md" ]]; then
		RT14_ARTIFACT_WRITTEN_BEFORE_SECOND_STEER=1
	fi
	if raw_contains "$RT14_RAW" '"tool_name":"finish"' || raw_contains "$RT14_RAW" '"status":"completed"'; then
		RT14_FINISH_OBSERVED_BEFORE_SECOND_STEER=1
	fi
	if (( RT14_ARTIFACT_WRITTEN_BEFORE_SECOND_STEER == 1 || RT14_FINISH_OBSERVED_BEFORE_SECOND_STEER == 1 )); then
		break
	fi
	sleep 1
done
if kill -0 "$RT14_PID" 2>/dev/null; then
	if [[ -f "${ARTIFACT_DIR}/rt14-steer-audit.md" ]]; then
		RT14_ARTIFACT_WRITTEN_BEFORE_SECOND_STEER=1
	fi
	if raw_contains "$RT14_RAW" '"tool_name":"finish"' || raw_contains "$RT14_RAW" '"status":"completed"'; then
		RT14_FINISH_OBSERVED_BEFORE_SECOND_STEER=1
	fi
	if [[ -n "$RT14_SESSION_ID" ]] && (( RT14_ARTIFACT_WRITTEN_BEFORE_SECOND_STEER == 0 && RT14_FINISH_OBSERVED_BEFORE_SECOND_STEER == 0 )); then
		RT14_SECOND_STEER_SENT=1
		RT14_SECOND_STEER_ACTUALLY_NEEDED=1
		./bin/go-cli-agent steer "$RT14_SESSION_ID" \
			--config "$CONFIG_PATH" \
			--json \
			--interrupt \
			--message "Do not do more reads or bookkeeping. Finish immediately with the artifact you can write from current evidence." \
			>"${RAW_DIR}/rt14-live-steer-command-2.json" 2>&1
		RT14_STEER2_EXIT=$?
		RT14_EXIT="$(merge_exit_code "$RT14_EXIT" "$RT14_STEER2_EXIT")"
	fi
fi
wait "$RT14_PID"
RT14_WAIT_EXIT=$?
RT14_EXIT="$(merge_exit_code "$RT14_EXIT" "$RT14_WAIT_EXIT")"
printf '%s\n' \
	"second_steer_used=${RT14_SECOND_STEER_SENT}" \
	"artifact_written_before_second_steer=${RT14_ARTIFACT_WRITTEN_BEFORE_SECOND_STEER}" \
	"finish_observed_before_second_steer=${RT14_FINISH_OBSERVED_BEFORE_SECOND_STEER}" \
	"second_steer_sent=${RT14_SECOND_STEER_SENT}" \
	"second_steer_actually_needed=${RT14_SECOND_STEER_ACTUALLY_NEEDED}" \
	>"${NOTE_DIR}/rt14-steer-metadata.txt"
finalize_scenario "RT14" "Live Steer Reprioritization" "$RT14_EXIT" "$RT14_RAW" "${ARTIFACT_DIR}/rt14-steer-audit.md" "$RT14_SESSION_ID"

RT15_PROMPT="${PROMPT_DIR}/rt15-traceability-map.prompt.txt"
write_prompt "$RT15_PROMPT" "Use the review_pipeline skill for this task.
Inspect only README.md, AGENTS.md, spec/00-product.md, spec/01-runtime-architecture.md, spec/03-provider-contracts.md, spec/08-sdk-and-api-evolution.md, spec/11-spec-audit-and-traceability.md, spec/12-task-system.md, spec/13-live-input-and-steering.md, cmd/go-cli-agent/main.go, pkg/agent/agent.go, internal/app, internal/runtime, internal/provider, internal/session, and internal/tools.
Ignore validation/runs, validation/sessions, reports, bin, tmp, and generated artifacts.
Write ${ABS_ARTIFACT_DIR}/rt15-traceability-map.md with sections: core promise to owning files, strongest existing tests, provider/steer trace anchors, unresolved questions, smallest next fixes.
Then call finish with a one-line summary."
RT15_RAW="${RAW_DIR}/rt15-traceability-map.jsonl"
run_exec "$RT15_PROMPT" "$RT15_RAW" "$ROOT_DIR" 300
RT15_EXIT=$?
finalize_scenario "RT15" "Traceability Map" "$RT15_EXIT" "$RT15_RAW" "${ARTIFACT_DIR}/rt15-traceability-map.md" "$(extract_session_id "$RT15_RAW")"

RT16_PROMPT="${PROMPT_DIR}/rt16-codex-steer-audit.prompt.txt"
write_prompt "$RT16_PROMPT" "Use the review_pipeline skill for this task.
Inspect only codex/AGENTS.md, codex/docs/agents_md.md, codex/docs/sandbox.md, codex/codex-rs/core/gpt_5_codex_prompt.md, and codex/codex-rs/app-server/tests/suite/v2/turn_steer.rs.
Do not inspect go-cli-agent code for this task.
Write ${ABS_ARTIFACT_DIR}/rt16-codex-steer-audit.md with sections: confirmed codex patterns, findings or cautions, remaining risks, ideas worth borrowing.
If you identify cautions, still record them inside a dedicated findings section using per-finding Severity, Confidence, Evidence, and Why it matters fields. If there is no validated finding, say so explicitly inside findings.
Then call finish with a one-line summary."
RT16_RAW="${RAW_DIR}/rt16-codex-steer-audit.jsonl"
run_exec "$RT16_PROMPT" "$RT16_RAW" "$WORKSPACE_ROOT" 300
RT16_EXIT=$?
finalize_scenario "RT16" "Codex Steer And Sandbox Audit" "$RT16_EXIT" "$RT16_RAW" "${ARTIFACT_DIR}/rt16-codex-steer-audit.md" "$(extract_session_id "$RT16_RAW")"

RT17_PROMPT="${PROMPT_DIR}/rt17-codex-proxy-audit.prompt.txt"
write_prompt "$RT17_PROMPT" "Use the review_pipeline skill for this task.
Inspect only codex/codex-rs/responses-api-proxy/README.md, codex/codex-rs/process-hardening/README.md, codex/docs/authentication.md, and codex/docs/config.md.
Focus on OpenAI-compatible Responses transport, auth handling, and local hardening patterns.
Write ${ABS_ARTIFACT_DIR}/rt17-codex-proxy-audit.md with sections: confirmed contracts, findings or mismatch risks, unresolved questions, hardening ideas worth importing.
If you identify mismatch risks, still record them inside a dedicated findings section using per-finding Severity, Confidence, Evidence, and Why it matters fields. If there is no validated finding, say so explicitly inside findings.
Then call finish with a one-line summary."
RT17_RAW="${RAW_DIR}/rt17-codex-proxy-audit.jsonl"
run_exec "$RT17_PROMPT" "$RT17_RAW" "$WORKSPACE_ROOT" 300
RT17_EXIT=$?
finalize_scenario "RT17" "Codex Responses Proxy Audit" "$RT17_EXIT" "$RT17_RAW" "${ARTIFACT_DIR}/rt17-codex-proxy-audit.md" "$(extract_session_id "$RT17_RAW")"

RT18_PROMPT="${PROMPT_DIR}/rt18-opencode-task-review.prompt.txt"
write_prompt "$RT18_PROMPT" "Use the review_pipeline skill for this task.
Inspect only opencode/AGENTS.md, opencode/packages/opencode/AGENTS.md, opencode/README.md, opencode/packages/opencode/src/session/prompt.ts, opencode/packages/opencode/src/session/todo.ts, and opencode/packages/opencode/src/session/processor.ts.
Focus on large-project task execution, todo discipline, and prompt/reminder behavior.
Write ${ABS_ARTIFACT_DIR}/rt18-opencode-task-review.md with sections: confirmed strengths, tradeoffs or findings, remaining risks, ideas go-cli-agent should adopt.
If you identify tradeoffs or cautions, still record them inside a dedicated findings section using per-finding Severity, Confidence, Evidence, and Why it matters fields. If there is no validated finding, say so explicitly inside findings.
Then call finish with a one-line summary."
RT18_RAW="${RAW_DIR}/rt18-opencode-task-review.jsonl"
run_exec "$RT18_PROMPT" "$RT18_RAW" "$WORKSPACE_ROOT" 300
RT18_EXIT=$?
finalize_scenario "RT18" "OpenCode Task And Prompt Review" "$RT18_EXIT" "$RT18_RAW" "${ARTIFACT_DIR}/rt18-opencode-task-review.md" "$(extract_session_id "$RT18_RAW")"

RT19_PROMPT="${PROMPT_DIR}/rt19-opencode-responses-audit.prompt.txt"
write_prompt "$RT19_PROMPT" "Use the review_pipeline skill for this task.
Inspect only opencode/specs/project.md, opencode/packages/opencode/src/provider/sdk/copilot/responses/openai-responses-language-model.ts, opencode/packages/opencode/src/provider/sdk/copilot/responses/map-openai-responses-finish-reason.ts, opencode/packages/opencode/src/provider/sdk/copilot/responses/convert-to-openai-responses-input.ts, and opencode/packages/opencode/src/provider/sdk/copilot/responses/openai-responses-prepare-tools.ts.
Focus on Responses replay, tool preparation, and finish-reason mapping.
Write ${ABS_ARTIFACT_DIR}/rt19-opencode-responses-audit.md with sections: confirmed behavior, findings or mismatch risks, unresolved questions, useful implementation ideas.
If you identify mismatch risks, still record them inside a dedicated findings section using per-finding Severity, Confidence, Evidence, and Why it matters fields. If there is no validated finding, say so explicitly inside findings.
Then call finish with a one-line summary."
RT19_RAW="${RAW_DIR}/rt19-opencode-responses-audit.jsonl"
run_exec "$RT19_PROMPT" "$RT19_RAW" "$WORKSPACE_ROOT" 300
RT19_EXIT=$?
finalize_scenario "RT19" "OpenCode Responses Provider Audit" "$RT19_EXIT" "$RT19_RAW" "${ARTIFACT_DIR}/rt19-opencode-responses-audit.md" "$(extract_session_id "$RT19_RAW")"

RT20_PROMPT="${PROMPT_DIR}/rt20-large-project-readiness.prompt.txt"
write_prompt "$RT20_PROMPT" "Use the review_pipeline skill for this task.
The listed paths below are exact and valid relative to the workdir. Do not glob or search outside this allowlist.
Inspect only these current-run artifacts: ${WORKSPACE_ARTIFACT_DIR}/rt01-architecture-audit.md, ${WORKSPACE_ARTIFACT_DIR}/rt04-review-pipeline.md, ${WORKSPACE_ARTIFACT_DIR}/rt05-compaction-memo.md, ${WORKSPACE_ARTIFACT_DIR}/rt06-incident-summary.md, ${WORKSPACE_ARTIFACT_DIR}/rt08-python-change-summary.md, ${WORKSPACE_ARTIFACT_DIR}/rt09-go-change-summary.md, ${WORKSPACE_ARTIFACT_DIR}/rt10-api-review.md, ${WORKSPACE_ARTIFACT_DIR}/rt11-repo-audit-skill.md, ${WORKSPACE_ARTIFACT_DIR}/rt13-continue-brief.md, ${WORKSPACE_ARTIFACT_DIR}/rt14-steer-audit.md, ${WORKSPACE_ARTIFACT_DIR}/rt15-traceability-map.md, ${WORKSPACE_ARTIFACT_DIR}/rt16-codex-steer-audit.md, ${WORKSPACE_ARTIFACT_DIR}/rt17-codex-proxy-audit.md, ${WORKSPACE_ARTIFACT_DIR}/rt18-opencode-task-review.md, ${WORKSPACE_ARTIFACT_DIR}/rt19-opencode-responses-audit.md.
Also inspect only these notes and source references: ${WORKSPACE_RUN_DIR}/notes/scenario-index.tsv, ${WORKSPACE_RUN_DIR}/notes/rt14-steer-metadata.txt, go-cli-agent/README.md, go-cli-agent/AGENTS.md, go-cli-agent/spec/00-product.md, go-cli-agent/spec/01-runtime-architecture.md, go-cli-agent/spec/08-sdk-and-api-evolution.md, go-cli-agent/spec/11-spec-audit-and-traceability.md, go-cli-agent/spec/12-task-system.md, go-cli-agent/spec/13-live-input-and-steering.md, go-cli-agent/pkg/agent/agent.go, ${TOP_LEVEL_MARKDOWN_LIST}, codex/README.md, and opencode/README.md.
Use this live evidence to judge whether go-cli-agent materially closes the previous large-project blockers and is now ready to surpass codex and opencode on large-project development and review. If any blocker remains, name it explicitly and explain why the current run still does not clear it.
Write ${ABS_ARTIFACT_DIR}/rt20-large-project-readiness.md with sections: live strengths, scorecard, remaining risks, unresolved questions, next architectural moves.
In the scorecard, rate review pipeline, proof quality, compaction and handoff, interruption and recovery, coding-task execution, and cross-repo reasoning.
Any blocker, caution, or mismatch risk still needs to be represented inside a dedicated findings section using per-finding Severity, Confidence, Evidence, and Why it matters fields. If there is no validated finding, say so explicitly inside findings.
Then call finish with a one-line summary."
RT20_RAW="${RAW_DIR}/rt20-large-project-readiness.jsonl"
run_exec "$RT20_PROMPT" "$RT20_RAW" "$WORKSPACE_ROOT" 420
RT20_EXIT=$?
finalize_scenario "RT20" "Large Project Readiness Scorecard Rerun" "$RT20_EXIT" "$RT20_RAW" "${ARTIFACT_DIR}/rt20-large-project-readiness.md" "$(extract_session_id "$RT20_RAW")"

write_summary
write_issues
printf '%s\n' "$RUN_DIR"
if (( FAILED_SCENARIOS > 0 )); then
	exit 1
fi
