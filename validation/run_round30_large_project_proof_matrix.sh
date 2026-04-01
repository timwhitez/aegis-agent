#!/usr/bin/env bash
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKSPACE_ROOT="$(cd "${ROOT_DIR}/.." && pwd)"
cd "$ROOT_DIR"

: "${OPENAI_API_KEY:?OPENAI_API_KEY is required}"

MODEL="${GO_CLI_AGENT_LIVE_MODEL:-gpt-5.4}"
CONFIG_PATH="validation/config.openai-compatible.yaml"
LOW_COMPACT_CONFIG_PATH="validation/config.openai-compatible-low-compact.yaml"
MATRIX_LABEL="${GO_CLI_AGENT_MATRIX_LABEL:-round30-large-project-proof-matrix}"
ROUND_ID="${1:-$(date -u +%F)-openai-compatible-${MODEL}-${MATRIX_LABEL}}"
RUN_DIR="validation/runs/${ROUND_ID}"
RAW_DIR="${RUN_DIR}/raw"
PROMPT_DIR="${RUN_DIR}/prompts"
ARTIFACT_DIR="${RUN_DIR}/artifacts"
EVIDENCE_DIR="${RUN_DIR}/evidence"
WORKSPACE_DIR="${RUN_DIR}/workspaces"
NOTE_DIR="${RUN_DIR}/notes"
SUMMARY_PATH="${RUN_DIR}/SUMMARY.md"
ISSUES_PATH="${RUN_DIR}/ISSUES.md"
ABORTED_PATH="${RUN_DIR}/ABORTED.md"
WORKSPACE_ARTIFACT_DIR="go-cli-agent/${ARTIFACT_DIR}"
WORKSPACE_EVIDENCE_DIR="go-cli-agent/${EVIDENCE_DIR}"
WORKSPACE_RUN_DIR="go-cli-agent/${RUN_DIR}"
SESSION_ROOT="/root/.go-cli-agent/validation-sessions"
BIN_DIR="${RUN_DIR}/bin"
AGENT_BIN="${BIN_DIR}/go-cli-agent"

if [[ -e "$RUN_DIR" ]]; then
	echo "run directory already exists: $RUN_DIR" >&2
	exit 1
fi

mkdir -p "$RAW_DIR" "$PROMPT_DIR" "$ARTIFACT_DIR" "$EVIDENCE_DIR" "$WORKSPACE_DIR" "$NOTE_DIR"
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

copy_workspace_as() {
	local source="$1"
	local target="$2"
	cp -R "validation/workspaces/${source}" "${WORKSPACE_DIR}/${target}"
}

prepare_agent_bin() {
	mkdir -p "$BIN_DIR"
	cp ./bin/go-cli-agent "$AGENT_BIN"
	chmod +x "$AGENT_BIN"
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

extract_json_field() {
	local path="$1"
	local field="$2"
	if [[ ! -f "$path" ]]; then
		return 0
	fi
	grep -o "\"${field}\":\"[^\"]*\"" "$path" | tail -n1 | cut -d'"' -f4
}

extract_first_json_field() {
	local path="$1"
	shift
	local field=""
	local value=""
	for field in "$@"; do
		value="$(extract_json_field "$path" "$field")"
		if [[ -n "$value" ]]; then
			printf '%s' "$value"
			return 0
		fi
	done
	return 0
}

count_pattern() {
	local path="$1"
	local pattern="$2"
	if [[ ! -f "$path" ]]; then
		printf '0'
		return 0
	fi
	grep -Fc "$pattern" "$path"
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

raw_final_status() {
	local path="$1"
	if [[ ! -f "$path" ]]; then
		return 0
	fi
	grep -o '"status":"[^"]*"' "$path" | tail -n1 | cut -d'"' -f4
}

run_reached_awaiting_input() {
	local path="$1"
	if [[ ! -f "$path" ]]; then
		return 1
	fi
	if grep -Fq '"type":"session.awaiting_input"' "$path"; then
		return 0
	fi
	[[ "$(raw_final_status "$path")" == "awaiting_input" ]]
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
	if grep -Fq 'matrix_watchdog:' "$raw_path"; then
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

run_exec_with_config() {
	local config_path="$1"
	local prompt_path="$2"
	local raw_path="$3"
	local workdir="$4"
	local timeout_sec="${5:-300}"
	local first_turn_timeout_sec="${6:-45}"
	if (( timeout_sec < 420 )); then
		timeout_sec=420
	fi
	"${AGENT_BIN}" exec \
		--config "$config_path" \
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

run_exec() {
	run_exec_with_config "$CONFIG_PATH" "$@"
}

run_exec_exact() {
	local prompt_path="$1"
	local raw_path="$2"
	local workdir="$3"
	local timeout_sec="${4:-300}"
	local first_turn_timeout_sec="${5:-20}"
	run_exec_with_config "$CONFIG_PATH" "$prompt_path" "$raw_path" "$workdir" "$timeout_sec" "$first_turn_timeout_sec"
}

run_run_with_config() {
	local config_path="$1"
	local prompt_path="$2"
	local raw_path="$3"
	local workdir="$4"
	local timeout_sec="${5:-300}"
	local first_turn_timeout_sec="${6:-45}"
	if (( timeout_sec < 420 )); then
		timeout_sec=420
	fi
	"${AGENT_BIN}" run \
		--config "$config_path" \
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
	run_run_with_config "$CONFIG_PATH" "$@"
}

copy_if_present() {
	local src="$1"
	local dst="$2"
	if [[ -f "$src" ]]; then
		cp "$src" "$dst"
	fi
}

copy_dir_if_present() {
	local src="$1"
	local dst="$2"
	if [[ -d "$src" ]]; then
		mkdir -p "$dst"
		cp -R "${src}/." "$dst"
	fi
}

session_dir_for_id() {
	local session_id="$1"
	if [[ -z "$session_id" ]]; then
		return 0
	fi
	printf '%s/%s' "$SESSION_ROOT" "$session_id"
}

latest_compaction_summary() {
	local session_id="$1"
	local session_dir=""
	session_dir="$(session_dir_for_id "$session_id")"
	if [[ -z "$session_dir" ]]; then
		return 0
	fi
	ls -1 "${session_dir}/artifacts/compactions"/summary-*.json 2>/dev/null | sort | tail -n1
}

copy_session_evidence() {
	local session_id="$1"
	local dest_dir="$2"
	local session_dir=""
	local latest_summary=""
	session_dir="$(session_dir_for_id "$session_id")"
	if [[ -z "$session_dir" || ! -d "$session_dir" ]]; then
		return 0
	fi
	mkdir -p "$dest_dir"
	copy_if_present "${session_dir}/session.json" "${dest_dir}/session.json"
	copy_if_present "${session_dir}/state.json" "${dest_dir}/state.json"
	copy_if_present "${session_dir}/messages.jsonl" "${dest_dir}/messages.jsonl"
	copy_if_present "${session_dir}/events.jsonl" "${dest_dir}/events.jsonl"
	copy_if_present "${session_dir}/todo.json" "${dest_dir}/todo.json"
	copy_dir_if_present "${session_dir}/tasks" "${dest_dir}/tasks"
	latest_summary="$(latest_compaction_summary "$session_id")"
	if [[ -n "$latest_summary" ]]; then
		copy_if_present "$latest_summary" "${dest_dir}/$(basename "$latest_summary")"
	fi
}

copy_child_artifact_if_present() {
	local json_path="$1"
	local rel_path="$2"
	local dst="$3"
	local fallback_root="${4:-}"
	local child_workdir=""
	child_workdir="$(extract_first_json_field "$json_path" "workdir" "effective_workdir" "requested_workdir")"
	if [[ -n "$child_workdir" ]]; then
		copy_if_present "${child_workdir}/${rel_path}" "$dst"
	fi
	if [[ ! -f "$dst" && -n "$fallback_root" ]]; then
		copy_if_present "${fallback_root}/${rel_path}" "$dst"
	fi
}

write_summary() {
	{
		echo "# Round30 Summary"
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
		echo "# Round30 Issues"
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
		echo "# Round30 Aborted"
		echo
		echo "- Run directory: \`${RUN_DIR}\`"
		echo "- Reason: ${reason}"
		echo "- Preflight index: \`notes/preflight-index.tsv\`"
	} >"$ABORTED_PATH"
}

copy_workspace docset
copy_workspace incident
copy_workspace nested_review
copy_workspace platform_go
copy_workspace platform_py

DOCSET_DIR="${WORKSPACE_DIR}/docset"
INCIDENT_DIR="${WORKSPACE_DIR}/incident"
NESTED_API_DIR="${WORKSPACE_DIR}/nested_review/services/api"
PLATFORM_GO_DIR="${WORKSPACE_DIR}/platform_go"
PLATFORM_PY_DIR="${WORKSPACE_DIR}/platform_py"

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

if ! prepare_agent_bin; then
	HARD_ABORT_REASON="failed to stage run-local agent binary"
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

run_preflight "doctor" "${RAW_DIR}/preflight-doctor.json" "${AGENT_BIN}" doctor \
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

run_preflight "probe" "${RAW_DIR}/preflight-probe.json" "${AGENT_BIN}" probe-provider \
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

RT01_PROMPT="${PROMPT_DIR}/rt01-core-surface-audit.prompt.txt"
write_prompt "$RT01_PROMPT" "Use the review_pipeline skill for this task.
Audit the current go-cli-agent repository for core-v1 surface discipline after the latest runtime gap-close pass.
Only inspect README.md, AGENTS.md, spec/00-product.md, spec/01-runtime-architecture.md, spec/08-sdk-and-api-evolution.md, spec/09-phase-plan.md, pkg/agent/agent.go, internal/app/app.go, internal/app/orchestration.go, internal/runtime/facade.go, internal/runtime/runner.go, internal/provider, internal/tools, and internal/session.
Ignore validation/runs, validation/sessions, reports, bin, tmp, and generated artifacts.
Start with a short todo plan. Prefer targeted retrieval. Validate whether the default help surface is core-only, whether experimental routing stays outside the default operator path, whether core/experimental/store facades stay split, and whether the public SDK facade keeps extension-only surfaces out of the default core runner.
Write ${ABS_ARTIFACT_DIR}/rt01-core-surface-audit.md with sections: core surface map, findings, unresolved questions, smallest next fixes.
Then call finish with a one-line summary."
RT01_RAW="${RAW_DIR}/rt01-core-surface-audit.jsonl"
run_exec "$RT01_PROMPT" "$RT01_RAW" "$ROOT_DIR" 300
RT01_EXIT=$?
finalize_scenario "RT01" "Core Surface Boundary Audit" "$RT01_EXIT" "$RT01_RAW" "${ARTIFACT_DIR}/rt01-core-surface-audit.md" "$(extract_session_id "$RT01_RAW")"

RT02_PROMPT="${PROMPT_DIR}/rt02-provider-review-safety-audit.prompt.txt"
write_prompt "$RT02_PROMPT" "Use the review_pipeline skill for this task.
Inspect only README.md, AGENTS.md, spec/01-runtime-architecture.md, spec/03-provider-contracts.md, spec/11-spec-audit-and-traceability.md, internal/runtime/review_guard.go, internal/runtime/prompt.go, internal/provider/openai.go, internal/provider/http.go, internal/tools/path.go, and internal/tools/registry.go.
Ignore validation/runs, validation/sessions, reports, bin, tmp, and generated artifacts.
Focus on three things: provider metadata/retry propagation, review-artifact enforcement quality, and whether any report pre-validation path can still escape the workspace boundary before the real tool executes.
Write ${ABS_ARTIFACT_DIR}/rt02-provider-review-safety-audit.md with sections: confirmed alignments, findings, unresolved questions, smallest next fixes.
Then call finish with a one-line summary."
RT02_RAW="${RAW_DIR}/rt02-provider-review-safety-audit.jsonl"
run_exec "$RT02_PROMPT" "$RT02_RAW" "$ROOT_DIR" 300
RT02_EXIT=$?
finalize_scenario "RT02" "Provider Review And Workspace Safety Audit" "$RT02_EXIT" "$RT02_RAW" "${ARTIFACT_DIR}/rt02-provider-review-safety-audit.md" "$(extract_session_id "$RT02_RAW")"

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

RT04_PROMPT="${PROMPT_DIR}/rt04-forced-compaction-proof.prompt.txt"
write_prompt "$RT04_PROMPT" "Use the review_pipeline skill for this task.
Inspect only README.md, AGENTS.md, spec/00-product.md, spec/01-runtime-architecture.md, spec/03-provider-contracts.md, spec/10-context-compaction.md, spec/11-spec-audit-and-traceability.md, spec/12-task-system.md, spec/13-live-input-and-steering.md, internal/runtime/compaction.go, internal/runtime/prompt.go, internal/runtime/review_guard.go, internal/runtime/engine.go, internal/runtime/project_memory.go, internal/session/store.go, internal/tools/path.go, and these top-level Markdown files: ${TOP_LEVEL_MARKDOWN_LIST}.
Use targeted retrieval, keep a short todo list in assistant text, and write ${ABS_ARTIFACT_DIR}/rt04-forced-compaction-proof.md with sections: compaction evidence, proof-read behavior after compaction, remaining risks, next validation moves.
Then call finish with a one-line summary."
RT04_RAW="${RAW_DIR}/rt04-forced-compaction-proof.jsonl"
run_exec_with_config "$LOW_COMPACT_CONFIG_PATH" "$RT04_PROMPT" "$RT04_RAW" "$WORKSPACE_ROOT" 420
RT04_EXIT=$?
RT04_SESSION_ID="$(extract_session_id "$RT04_RAW")"
if ! raw_contains "$RT04_RAW" '"type":"compact.started"'; then
	RT04_EXIT="$(merge_exit_code "$RT04_EXIT" 1)"
fi
if ! raw_contains "$RT04_RAW" '"type":"compact.finished"'; then
	RT04_EXIT="$(merge_exit_code "$RT04_EXIT" 1)"
fi
copy_session_evidence "$RT04_SESSION_ID" "${EVIDENCE_DIR}/rt04-session"
printf '%s\n' \
	"session_id=${RT04_SESSION_ID}" \
	"session_dir=$(session_dir_for_id "$RT04_SESSION_ID")" \
	>"${NOTE_DIR}/rt04-session-metadata.txt"
finalize_scenario "RT04" "Forced Compaction Proof Drill" "$RT04_EXIT" "$RT04_RAW" "${ARTIFACT_DIR}/rt04-forced-compaction-proof.md" "$RT04_SESSION_ID"

RT05_PROMPT="${PROMPT_DIR}/rt05-incident-task-graph.prompt.txt"
write_prompt "$RT05_PROMPT" "Use the incident_triage skill for this task.
Read the local AGENTS.md first.
This is only the planning/setup phase. Build a durable task graph with at least four tasks and one explicit dependency edge, and keep a durable project-memory stack under reports/spec.md, reports/plan.md, reports/progress.md, and reports/validation.md.
You may inspect enough local evidence to scope the investigation, but do not write reports/incident-summary.md or reports/taskboard-summary.md yet, do not declare a final root cause, and do not call finish.
Stop naturally without calling finish once the durable task board plus project-memory stack are ready because a follow-up message will trigger the execution phase."
RT05_RAW="${RAW_DIR}/rt05-incident-task-graph.jsonl"
run_run "$RT05_PROMPT" "$RT05_RAW" "$INCIDENT_DIR" 300
RT05_EXIT=$?
RT05_SESSION_ID="$(extract_session_id "$RT05_RAW")"
if [[ -n "$RT05_SESSION_ID" ]] && run_reached_awaiting_input "$RT05_RAW"; then
	"${AGENT_BIN}" tasks "$RT05_SESSION_ID" --config "$CONFIG_PATH" --json >"${RAW_DIR}/rt05-incident-taskboard-before.json" 2>&1
	RT05_TASKS_BEFORE_EXIT=$?
	RT05_EXIT="$(merge_exit_code "$RT05_EXIT" "$RT05_TASKS_BEFORE_EXIT")"
	"${AGENT_BIN}" continue "$RT05_SESSION_ID" \
		--config "$CONFIG_PATH" \
		--provider openai-compatible \
		--model "$MODEL" \
		--json \
		--message "Use the existing durable task graph and report stack to execute the incident investigation fully. Inspect logs and configuration, build a confirmed timeline before root-cause judgment, update the durable task graph to match the executed state, refresh reports/progress.md and reports/validation.md, write reports/incident-summary.md, reports/taskboard-summary.md, and reports/recovery-summary.md with sections: current durable state, task graph changes, next blocking questions, and then call finish." \
		>"${RAW_DIR}/rt05-incident-continue.jsonl" 2>&1
	RT05_CONTINUE_EXIT=$?
	RT05_EXIT="$(merge_exit_code "$RT05_EXIT" "$RT05_CONTINUE_EXIT")"
	"${AGENT_BIN}" tasks "$RT05_SESSION_ID" --config "$CONFIG_PATH" --json >"${RAW_DIR}/rt05-incident-taskboard-after.json" 2>&1
	RT05_TASKS_AFTER_EXIT=$?
	RT05_EXIT="$(merge_exit_code "$RT05_EXIT" "$RT05_TASKS_AFTER_EXIT")"
else
	RT05_EXIT="$(merge_exit_code "$RT05_EXIT" 1)"
fi
copy_if_present "${INCIDENT_DIR}/reports/incident-summary.md" "${ARTIFACT_DIR}/rt05-incident-summary.md"
copy_if_present "${INCIDENT_DIR}/reports/taskboard-summary.md" "${ARTIFACT_DIR}/rt05-taskboard-summary.md"
copy_if_present "${INCIDENT_DIR}/reports/recovery-summary.md" "${ARTIFACT_DIR}/rt05-recovery-summary.md"
copy_session_evidence "$RT05_SESSION_ID" "${EVIDENCE_DIR}/rt05-session"
printf '%s\n' \
	"session_id=${RT05_SESSION_ID}" \
	"session_dir=$(session_dir_for_id "$RT05_SESSION_ID")" \
	>"${NOTE_DIR}/rt05-session-metadata.txt"
RT05_FINAL_RAW="${RAW_DIR}/rt05-incident-continue.jsonl"
if [[ ! -f "$RT05_FINAL_RAW" ]]; then
	RT05_FINAL_RAW="$RT05_RAW"
fi
finalize_scenario "RT05" "Incident Triage And Durable Task Graph" "$RT05_EXIT" "$RT05_FINAL_RAW" "${ARTIFACT_DIR}/rt05-recovery-summary.md" "$RT05_SESSION_ID"

RT06_RUN_PROMPT="${PROMPT_DIR}/rt06-docset-run.prompt.txt"
write_prompt "$RT06_RUN_PROMPT" "Review only product_overview.md, ops_notes.md, and release_constraints.md in this workspace.
Create reports/spec.md with a scoped problem statement and reports/plan.md with the main decision branches.
Stop naturally without calling finish once you need a stakeholder choice between onboarding, permissions, and rollout sequencing."
RT06_RUN_RAW="${RAW_DIR}/rt06-docset-run.jsonl"
run_run "$RT06_RUN_PROMPT" "$RT06_RUN_RAW" "$DOCSET_DIR" 240
RT06_EXIT=$?
RT06_SESSION_ID="$(extract_session_id "$RT06_RUN_RAW")"
if [[ -n "$RT06_SESSION_ID" ]]; then
	"${AGENT_BIN}" continue "$RT06_SESSION_ID" \
		--config "$CONFIG_PATH" \
		--provider openai-compatible \
		--model "$MODEL" \
		--json \
		--message "Prioritize onboarding. Refresh reports/spec.md, reports/plan.md, reports/progress.md, and reports/validation.md, then write reports/continue-brief.md with sections: chosen priority, supporting evidence, next steps, unresolved questions. Then call finish." \
		>"${RAW_DIR}/rt06-docset-continue.jsonl" 2>&1
	RT06_CONTINUE_EXIT=$?
	RT06_EXIT="$(merge_exit_code "$RT06_EXIT" "$RT06_CONTINUE_EXIT")"
else
	RT06_EXIT="$(merge_exit_code "$RT06_EXIT" 1)"
fi
copy_if_present "${DOCSET_DIR}/reports/continue-brief.md" "${ARTIFACT_DIR}/rt06-docset-continue-brief.md"
RT06_FINAL_RAW="${RAW_DIR}/rt06-docset-continue.jsonl"
if [[ ! -f "$RT06_FINAL_RAW" ]]; then
	RT06_FINAL_RAW="$RT06_RUN_RAW"
fi
finalize_scenario "RT06" "Awaiting Input And Continue On Docset" "$RT06_EXIT" "$RT06_FINAL_RAW" "${ARTIFACT_DIR}/rt06-docset-continue-brief.md" "$RT06_SESSION_ID"

RT07_PROMPT="${PROMPT_DIR}/rt07-live-steer-two-wave.prompt.txt"
write_prompt "$RT07_PROMPT" "Use the review_pipeline skill for this task.
Inspect only README.md, AGENTS.md, spec/10-context-compaction.md, spec/12-task-system.md, spec/13-live-input-and-steering.md, internal/runtime/engine.go, internal/runtime/runner.go, internal/runtime/prompt.go, internal/runtime/review_guard.go, internal/runtime/project_memory.go, and internal/tools/registry.go.
Start with a short todo plan, use targeted retrieval only, and write ${ABS_ARTIFACT_DIR}/rt07-live-steer-audit.md with sections: confirmed behavior, findings, remaining risks, next validation moves.
Then call finish."
RT07_RAW="${RAW_DIR}/rt07-live-steer-two-wave.jsonl"
"${AGENT_BIN}" exec \
	--config "$CONFIG_PATH" \
	--provider openai-compatible \
	--model "$MODEL" \
	--workdir "$ROOT_DIR" \
	--json \
	--timeout 420 \
	<"$RT07_PROMPT" >"$RT07_RAW" 2>&1 &
RT07_PID="$!"
RT07_EXIT=0
wait_for_pattern "$RT07_RAW" '"type":"session.started"' 90
RT07_WAIT_START_EXIT=$?
RT07_EXIT="$(merge_exit_code "$RT07_EXIT" "$RT07_WAIT_START_EXIT")"
RT07_SESSION_ID="$(extract_session_id "$RT07_RAW")"
wait_for_pattern "$RT07_RAW" '"tool_name":"read_file"' 120 || true
if [[ -n "$RT07_SESSION_ID" ]]; then
	"${AGENT_BIN}" steer "$RT07_SESSION_ID" \
		--config "$CONFIG_PATH" \
		--json \
		--interrupt \
		--message "Stop broad reading. Use current evidence, write ${ABS_ARTIFACT_DIR}/rt07-live-steer-audit.md immediately, and keep the report concise." \
		>"${RAW_DIR}/rt07-live-steer-command-1.json" 2>&1
	RT07_STEER1_EXIT=$?
	RT07_EXIT="$(merge_exit_code "$RT07_EXIT" "$RT07_STEER1_EXIT")"
	sleep 1
	"${AGENT_BIN}" steer "$RT07_SESSION_ID" \
		--config "$CONFIG_PATH" \
		--json \
		--interrupt \
		--message "Latest priority: after drafting the artifact, add an explicit remaining-risks section and finish. No more reads or bookkeeping." \
		>"${RAW_DIR}/rt07-live-steer-command-2.json" 2>&1
	RT07_STEER2_EXIT=$?
	RT07_EXIT="$(merge_exit_code "$RT07_EXIT" "$RT07_STEER2_EXIT")"
else
	RT07_EXIT="$(merge_exit_code "$RT07_EXIT" 1)"
fi
wait "$RT07_PID"
RT07_WAIT_EXIT=$?
RT07_EXIT="$(merge_exit_code "$RT07_EXIT" "$RT07_WAIT_EXIT")"
RT07_ACCEPTED_COUNT="$(count_pattern "$RT07_RAW" '"type":"session.steer.accepted"')"
printf '%s\n' \
	"requested_count=$(count_pattern "$RT07_RAW" '"type":"session.steer.requested"')" \
	"accepted_count=${RT07_ACCEPTED_COUNT}" \
	>"${NOTE_DIR}/rt07-steer-metadata.txt"
if [[ "$RT07_ACCEPTED_COUNT" -lt 2 ]]; then
	RT07_EXIT="$(merge_exit_code "$RT07_EXIT" 1)"
fi
finalize_scenario "RT07" "Live Steer Two-Wave Reprioritization" "$RT07_EXIT" "$RT07_RAW" "${ARTIFACT_DIR}/rt07-live-steer-audit.md" "$RT07_SESSION_ID"

RT08_PARENT_PROMPT="${PROMPT_DIR}/rt08-delegate-parent.prompt.txt"
write_prompt "$RT08_PARENT_PROMPT" "Write reports/parent-note.md with one sentence noting that a delegated audit of this workspace is about to run.
Then call finish."
RT08_PARENT_RAW="${RAW_DIR}/rt08-delegate-parent.jsonl"
run_exec_exact "$RT08_PARENT_PROMPT" "$RT08_PARENT_RAW" "$PLATFORM_GO_DIR" 60 20
RT08_EXIT=$?
RT08_PARENT_SESSION_ID="$(extract_session_id "$RT08_PARENT_RAW")"
RT08_DELEGATE_RAW="${RAW_DIR}/rt08-delegate-child.json"
if [[ -n "$RT08_PARENT_SESSION_ID" ]]; then
	"${AGENT_BIN}" experimental delegate "$RT08_PARENT_SESSION_ID" \
		--config "$CONFIG_PATH" \
		--provider openai-compatible \
		--model "$MODEL" \
		--workdir "$PLATFORM_GO_DIR" \
		--agent reviewer \
		--json \
		--timeout 420 \
		"Use the review_pipeline skill for this task. Review README.md, docs/contracts.md, internal/api/handler.go, internal/config/config.go, internal/quota/policy.go, internal/api/handler_test.go, internal/config/config_test.go, and internal/quota/policy_test.go. Write reports/delegate-review.md with sections: findings, unresolved questions, next fixes. Then call finish." \
		>"$RT08_DELEGATE_RAW" 2>&1
	RT08_DELEGATE_EXIT=$?
	RT08_EXIT="$(merge_exit_code "$RT08_EXIT" "$RT08_DELEGATE_EXIT")"
else
	RT08_EXIT="$(merge_exit_code "$RT08_EXIT" 1)"
fi
copy_child_artifact_if_present "$RT08_DELEGATE_RAW" "reports/delegate-review.md" "${ARTIFACT_DIR}/rt08-delegate-review.md" "$PLATFORM_GO_DIR"
RT08_CHILD_SESSION_ID="$(extract_json_field "$RT08_DELEGATE_RAW" "session_id")"
RT08_CHILD_WORKDIR="$(extract_first_json_field "$RT08_DELEGATE_RAW" "workdir" "effective_workdir" "requested_workdir")"
copy_session_evidence "$RT08_CHILD_SESSION_ID" "${EVIDENCE_DIR}/rt08-child-session"
printf '%s\n' \
	"parent_session_id=${RT08_PARENT_SESSION_ID}" \
	"child_session_id=${RT08_CHILD_SESSION_ID}" \
	"child_workdir=${RT08_CHILD_WORKDIR}" \
	>"${NOTE_DIR}/rt08-delegate-metadata.txt"
finalize_scenario "RT08" "Foreground Delegated Review" "$RT08_EXIT" "$RT08_DELEGATE_RAW" "${ARTIFACT_DIR}/rt08-delegate-review.md" "$RT08_CHILD_SESSION_ID"

RT09_PARENT_PROMPT="${PROMPT_DIR}/rt09-queue-parent.prompt.txt"
write_prompt "$RT09_PARENT_PROMPT" "Review only README.md in this workspace.
Write reports/parent-context.md with a one-paragraph problem frame, then stop naturally without calling finish because a background child result will arrive later."
RT09_PARENT_RAW="${RAW_DIR}/rt09-queue-parent.jsonl"
run_run "$RT09_PARENT_PROMPT" "$RT09_PARENT_RAW" "$PLATFORM_PY_DIR" 240
RT09_EXIT=$?
RT09_PARENT_SESSION_ID="$(extract_session_id "$RT09_PARENT_RAW")"
RT09_SUBMIT_RAW="${RAW_DIR}/rt09-queue-submit.json"
RT09_WORKER_RAW="${RAW_DIR}/rt09-queue-worker.json"
if [[ -n "$RT09_PARENT_SESSION_ID" ]]; then
	"${AGENT_BIN}" experimental queue submit \
		--config "$CONFIG_PATH" \
		--parent "$RT09_PARENT_SESSION_ID" \
		--provider openai-compatible \
		--model "$MODEL" \
		--workdir "$PLATFORM_PY_DIR" \
		--agent reviewer \
		--json \
		"Use the review_pipeline skill for this task. Review README.md, app/config.py, app/report.py, tests/test_config.py, and tests/test_report.py. Write reports/queue-review.md with sections: findings, remaining risks, next fixes. Then call finish." \
		>"$RT09_SUBMIT_RAW" 2>&1
	RT09_SUBMIT_EXIT=$?
	RT09_EXIT="$(merge_exit_code "$RT09_EXIT" "$RT09_SUBMIT_EXIT")"
	"${AGENT_BIN}" experimental queue worker --config "$CONFIG_PATH" --once --json >"$RT09_WORKER_RAW" 2>&1
	RT09_WORKER_EXIT=$?
	RT09_EXIT="$(merge_exit_code "$RT09_EXIT" "$RT09_WORKER_EXIT")"
	"${AGENT_BIN}" continue "$RT09_PARENT_SESSION_ID" \
		--config "$CONFIG_PATH" \
		--provider openai-compatible \
		--model "$MODEL" \
		--json \
		--message "Accept any background child results, summarize them in reports/background-summary.md with sections: child result summary, confirmed findings, next steps, unresolved questions, and then call finish." \
		>"${RAW_DIR}/rt09-queue-continue.jsonl" 2>&1
	RT09_CONTINUE_EXIT=$?
	RT09_EXIT="$(merge_exit_code "$RT09_EXIT" "$RT09_CONTINUE_EXIT")"
else
	RT09_EXIT="$(merge_exit_code "$RT09_EXIT" 1)"
fi
copy_if_present "${PLATFORM_PY_DIR}/reports/background-summary.md" "${ARTIFACT_DIR}/rt09-background-summary.md"
copy_child_artifact_if_present "$RT09_WORKER_RAW" "reports/queue-review.md" "${ARTIFACT_DIR}/rt09-queue-review.md" "$PLATFORM_PY_DIR"
RT09_JOB_ID="$(extract_json_field "$RT09_SUBMIT_RAW" "id")"
RT09_CHILD_WORKDIR="$(extract_first_json_field "$RT09_WORKER_RAW" "effective_workdir" "workdir" "requested_workdir")"
RT09_CHILD_SESSION_ID="$(extract_json_field "$RT09_WORKER_RAW" "session_id")"
copy_session_evidence "$RT09_CHILD_SESSION_ID" "${EVIDENCE_DIR}/rt09-child-session"
printf '%s\n' \
	"parent_session_id=${RT09_PARENT_SESSION_ID}" \
	"queue_job_id=${RT09_JOB_ID}" \
	"child_session_id=${RT09_CHILD_SESSION_ID}" \
	"child_workdir=${RT09_CHILD_WORKDIR}" \
	>"${NOTE_DIR}/rt09-queue-metadata.txt"
RT09_FINAL_RAW="${RAW_DIR}/rt09-queue-continue.jsonl"
if [[ ! -f "$RT09_FINAL_RAW" ]]; then
	RT09_FINAL_RAW="$RT09_PARENT_RAW"
fi
finalize_scenario "RT09" "Background Queue Review And Parent Notification" "$RT09_EXIT" "$RT09_FINAL_RAW" "${ARTIFACT_DIR}/rt09-background-summary.md" "$RT09_PARENT_SESSION_ID"

RT10_PROMPT="${PROMPT_DIR}/rt10-nested-api-review.prompt.txt"
write_prompt "$RT10_PROMPT" "Use the review_pipeline skill for this task.
Read the applicable AGENTS.md chain first.
Review only README.md, handler.go, and handler_test.go in this directory.
Write reports/api-review.md with sections: findings, unresolved questions, smallest fixes.
Put findings first, ordered by severity, and include confidence plus concrete evidence references.
Then call finish with a one-line summary."
RT10_RAW="${RAW_DIR}/rt10-nested-api-review.jsonl"
run_exec "$RT10_PROMPT" "$RT10_RAW" "$NESTED_API_DIR" 240
RT10_EXIT=$?
copy_if_present "${NESTED_API_DIR}/reports/api-review.md" "${ARTIFACT_DIR}/rt10-api-review.md"
finalize_scenario "RT10" "Nested AGENTS API Review" "$RT10_EXIT" "$RT10_RAW" "${ARTIFACT_DIR}/rt10-api-review.md" "$(extract_session_id "$RT10_RAW")"

RT11_PROMPT="${PROMPT_DIR}/rt11-platform-go-fix.prompt.txt"
write_prompt "$RT11_PROMPT" "Read the local AGENTS.md and README.md first.
This is only the diagnosis/planning phase of a real multi-package repair. Keep durable memory under reports/spec.md, reports/plan.md, reports/progress.md, and reports/validation.md.
Run the narrowest failing tests first, then diagnose all failing go tests across internal/api, internal/config, and internal/quota.
Preserve the public contract that internal_id stays private.
Do not edit product code yet, do not write reports/change-summary.md yet, and do not call finish.
Write reports/spec.md and reports/plan.md with the confirmed root cause and implementation steps, then stop naturally once the durable plan is ready because implementation instructions will arrive later."
RT11_RAW="${RAW_DIR}/rt11-platform-go-fix.jsonl"
run_run "$RT11_PROMPT" "$RT11_RAW" "$PLATFORM_GO_DIR" 420
RT11_EXIT=$?
RT11_SESSION_ID="$(extract_session_id "$RT11_RAW")"
if [[ -n "$RT11_SESSION_ID" ]] && run_reached_awaiting_input "$RT11_RAW"; then
	"${AGENT_BIN}" continue "$RT11_SESSION_ID" \
		--config "$CONFIG_PATH" \
		--provider openai-compatible \
		--model "$MODEL" \
		--json \
		--message "Implement the planned multi-package fixes, rerun the narrowest tests until the targeted go test set is green, refresh reports/progress.md and reports/validation.md, write reports/change-summary.md with sections: root cause, files changed, verification, remaining risks, and then call finish." \
		>"${RAW_DIR}/rt11-platform-go-continue.jsonl" 2>&1
	RT11_CONTINUE_EXIT=$?
	RT11_EXIT="$(merge_exit_code "$RT11_EXIT" "$RT11_CONTINUE_EXIT")"
else
	RT11_EXIT="$(merge_exit_code "$RT11_EXIT" 1)"
fi
copy_if_present "${PLATFORM_GO_DIR}/reports/change-summary.md" "${ARTIFACT_DIR}/rt11-platform-go-change-summary.md"
copy_session_evidence "$RT11_SESSION_ID" "${EVIDENCE_DIR}/rt11-session"
RT11_FINAL_RAW="${RAW_DIR}/rt11-platform-go-continue.jsonl"
if [[ ! -f "$RT11_FINAL_RAW" ]]; then
	RT11_FINAL_RAW="$RT11_RAW"
fi
finalize_scenario "RT11" "Platform Go Multi-Package Repair" "$RT11_EXIT" "$RT11_FINAL_RAW" "${ARTIFACT_DIR}/rt11-platform-go-change-summary.md" "$RT11_SESSION_ID"

RT12_PROMPT="${PROMPT_DIR}/rt12-platform-go-review.prompt.txt"
write_prompt "$RT12_PROMPT" "Use the review_pipeline skill for this task.
Read the local AGENTS.md first.
Review README.md, docs/contracts.md, docs/rollout.md, internal/api/handler.go, internal/config/config.go, internal/quota/policy.go, internal/api/handler_test.go, internal/config/config_test.go, internal/quota/policy_test.go, and reports/change-summary.md.
Write reports/post-fix-review.md with sections: findings, unresolved questions, remaining risks, suggested next validation.
If there is no validated finding, say so explicitly inside findings.
Then call finish with a one-line summary."
RT12_RAW="${RAW_DIR}/rt12-platform-go-review.jsonl"
run_exec "$RT12_PROMPT" "$RT12_RAW" "$PLATFORM_GO_DIR" 300
RT12_EXIT=$?
copy_if_present "${PLATFORM_GO_DIR}/reports/post-fix-review.md" "${ARTIFACT_DIR}/rt12-platform-go-review.md"
finalize_scenario "RT12" "Platform Go Post-Fix Review" "$RT12_EXIT" "$RT12_RAW" "${ARTIFACT_DIR}/rt12-platform-go-review.md" "$(extract_session_id "$RT12_RAW")"

RT13_PROMPT="${PROMPT_DIR}/rt13-platform-py-fix.prompt.txt"
write_prompt "$RT13_PROMPT" "Read the local AGENTS.md and README.md first.
This is only the diagnosis/planning phase of a real multi-module repair. Keep durable memory under reports/spec.md, reports/plan.md, reports/progress.md, and reports/validation.md.
Run the narrowest failing tests first, then diagnose all failing pytest cases across app/config.py, app/rules.py, and app/report.py.
Do not edit product code yet, do not write reports/change-summary.md yet, and do not call finish.
Write reports/spec.md and reports/plan.md with the confirmed root cause and implementation steps, then stop naturally once the durable plan is ready because implementation instructions will arrive later."
RT13_RAW="${RAW_DIR}/rt13-platform-py-fix.jsonl"
run_run "$RT13_PROMPT" "$RT13_RAW" "$PLATFORM_PY_DIR" 420
RT13_EXIT=$?
RT13_SESSION_ID="$(extract_session_id "$RT13_RAW")"
if [[ -n "$RT13_SESSION_ID" ]] && run_reached_awaiting_input "$RT13_RAW"; then
	"${AGENT_BIN}" continue "$RT13_SESSION_ID" \
		--config "$CONFIG_PATH" \
		--provider openai-compatible \
		--model "$MODEL" \
		--json \
		--message "Implement the planned multi-module fixes, rerun the narrowest pytest targets until the targeted test set is green, refresh reports/progress.md and reports/validation.md, write reports/change-summary.md with sections: root cause, files changed, verification, remaining risks, and then call finish." \
		>"${RAW_DIR}/rt13-platform-py-continue.jsonl" 2>&1
	RT13_CONTINUE_EXIT=$?
	RT13_EXIT="$(merge_exit_code "$RT13_EXIT" "$RT13_CONTINUE_EXIT")"
else
	RT13_EXIT="$(merge_exit_code "$RT13_EXIT" 1)"
fi
copy_if_present "${PLATFORM_PY_DIR}/reports/change-summary.md" "${ARTIFACT_DIR}/rt13-platform-py-change-summary.md"
copy_session_evidence "$RT13_SESSION_ID" "${EVIDENCE_DIR}/rt13-session"
RT13_FINAL_RAW="${RAW_DIR}/rt13-platform-py-continue.jsonl"
if [[ ! -f "$RT13_FINAL_RAW" ]]; then
	RT13_FINAL_RAW="$RT13_RAW"
fi
finalize_scenario "RT13" "Platform Python Multi-Module Repair" "$RT13_EXIT" "$RT13_FINAL_RAW" "${ARTIFACT_DIR}/rt13-platform-py-change-summary.md" "$RT13_SESSION_ID"

RT14_PROMPT="${PROMPT_DIR}/rt14-platform-py-review.prompt.txt"
write_prompt "$RT14_PROMPT" "Use the review_pipeline skill for this task.
Read the local AGENTS.md first.
Review README.md, app/config.py, app/rules.py, app/report.py, tests/test_config.py, tests/test_report.py, and reports/change-summary.md.
Write reports/post-fix-review.md with sections: findings, unresolved questions, remaining risks, suggested next validation.
If there is no validated finding, say so explicitly inside findings.
Then call finish with a one-line summary."
RT14_RAW="${RAW_DIR}/rt14-platform-py-review.jsonl"
run_exec "$RT14_PROMPT" "$RT14_RAW" "$PLATFORM_PY_DIR" 300
RT14_EXIT=$?
copy_if_present "${PLATFORM_PY_DIR}/reports/post-fix-review.md" "${ARTIFACT_DIR}/rt14-platform-py-review.md"
finalize_scenario "RT14" "Platform Python Post-Fix Review" "$RT14_EXIT" "$RT14_RAW" "${ARTIFACT_DIR}/rt14-platform-py-review.md" "$(extract_session_id "$RT14_RAW")"

RT15_PROMPT="${PROMPT_DIR}/rt15-task-memory-traceability.prompt.txt"
write_prompt "$RT15_PROMPT" "Use the review_pipeline skill for this task.
The listed paths below are exact and valid relative to the workdir. Do not glob or search outside this allowlist.
Inspect only these current-run artifacts: ${WORKSPACE_ARTIFACT_DIR}/rt04-forced-compaction-proof.md, ${WORKSPACE_ARTIFACT_DIR}/rt05-incident-summary.md, ${WORKSPACE_ARTIFACT_DIR}/rt05-taskboard-summary.md, ${WORKSPACE_ARTIFACT_DIR}/rt05-recovery-summary.md, ${WORKSPACE_ARTIFACT_DIR}/rt06-docset-continue-brief.md, ${WORKSPACE_ARTIFACT_DIR}/rt08-delegate-review.md, ${WORKSPACE_ARTIFACT_DIR}/rt09-background-summary.md, ${WORKSPACE_ARTIFACT_DIR}/rt09-queue-review.md, ${WORKSPACE_ARTIFACT_DIR}/rt11-platform-go-change-summary.md, ${WORKSPACE_ARTIFACT_DIR}/rt13-platform-py-change-summary.md.
Also inspect only these current-run evidence files: ${WORKSPACE_RUN_DIR}/raw/rt05-incident-taskboard-before.json, ${WORKSPACE_RUN_DIR}/raw/rt05-incident-taskboard-after.json, ${WORKSPACE_EVIDENCE_DIR}/rt04-session/events.jsonl, ${WORKSPACE_EVIDENCE_DIR}/rt05-session/events.jsonl, ${WORKSPACE_EVIDENCE_DIR}/rt05-session/todo.json, ${WORKSPACE_EVIDENCE_DIR}/rt05-session/session.json, ${WORKSPACE_EVIDENCE_DIR}/rt05-session/state.json, ${WORKSPACE_EVIDENCE_DIR}/rt11-session/events.jsonl, ${WORKSPACE_EVIDENCE_DIR}/rt13-session/events.jsonl.
Also inspect only these notes and source references: ${WORKSPACE_RUN_DIR}/notes/scenario-index.tsv, ${WORKSPACE_RUN_DIR}/notes/rt04-session-metadata.txt, ${WORKSPACE_RUN_DIR}/notes/rt05-session-metadata.txt, ${WORKSPACE_RUN_DIR}/notes/rt07-steer-metadata.txt, ${WORKSPACE_RUN_DIR}/notes/rt08-delegate-metadata.txt, ${WORKSPACE_RUN_DIR}/notes/rt09-queue-metadata.txt, go-cli-agent/spec/01-runtime-architecture.md, go-cli-agent/spec/10-context-compaction.md, go-cli-agent/spec/12-task-system.md, go-cli-agent/internal/runtime/engine.go, go-cli-agent/internal/runtime/project_memory.go, go-cli-agent/internal/session/store.go, go-cli-agent/internal/session/taskboard.go, and go-cli-agent/internal/tools/registry.go.
Use this live evidence to judge whether go-cli-agent now demonstrates real durable project-memory and task-system behavior across continue, compaction, and multi-step execution rather than only in spec text.
Write ${ABS_ARTIFACT_DIR}/rt15-task-memory-traceability.md with sections: confirmed runtime evidence, findings, remaining gaps, next validation moves.
Then call finish with a one-line summary."
RT15_RAW="${RAW_DIR}/rt15-task-memory-traceability.jsonl"
run_exec "$RT15_PROMPT" "$RT15_RAW" "$WORKSPACE_ROOT" 360
RT15_EXIT=$?
finalize_scenario "RT15" "Task And Durable Memory Traceability" "$RT15_EXIT" "$RT15_RAW" "${ARTIFACT_DIR}/rt15-task-memory-traceability.md" "$(extract_session_id "$RT15_RAW")"

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
Inspect only these current-run artifacts: ${WORKSPACE_ARTIFACT_DIR}/rt01-core-surface-audit.md, ${WORKSPACE_ARTIFACT_DIR}/rt02-provider-review-safety-audit.md, ${WORKSPACE_ARTIFACT_DIR}/rt04-forced-compaction-proof.md, ${WORKSPACE_ARTIFACT_DIR}/rt05-incident-summary.md, ${WORKSPACE_ARTIFACT_DIR}/rt05-taskboard-summary.md, ${WORKSPACE_ARTIFACT_DIR}/rt05-recovery-summary.md, ${WORKSPACE_ARTIFACT_DIR}/rt06-docset-continue-brief.md, ${WORKSPACE_ARTIFACT_DIR}/rt07-live-steer-audit.md, ${WORKSPACE_ARTIFACT_DIR}/rt08-delegate-review.md, ${WORKSPACE_ARTIFACT_DIR}/rt09-background-summary.md, ${WORKSPACE_ARTIFACT_DIR}/rt09-queue-review.md, ${WORKSPACE_ARTIFACT_DIR}/rt10-api-review.md, ${WORKSPACE_ARTIFACT_DIR}/rt11-platform-go-change-summary.md, ${WORKSPACE_ARTIFACT_DIR}/rt12-platform-go-review.md, ${WORKSPACE_ARTIFACT_DIR}/rt13-platform-py-change-summary.md, ${WORKSPACE_ARTIFACT_DIR}/rt14-platform-py-review.md, ${WORKSPACE_ARTIFACT_DIR}/rt15-task-memory-traceability.md, ${WORKSPACE_ARTIFACT_DIR}/rt16-codex-steer-audit.md, ${WORKSPACE_ARTIFACT_DIR}/rt17-codex-proxy-audit.md, ${WORKSPACE_ARTIFACT_DIR}/rt18-opencode-task-review.md, ${WORKSPACE_ARTIFACT_DIR}/rt19-opencode-responses-audit.md.
Also inspect only these current-run evidence files: ${WORKSPACE_RUN_DIR}/raw/rt05-incident-taskboard-before.json, ${WORKSPACE_RUN_DIR}/raw/rt05-incident-taskboard-after.json, ${WORKSPACE_EVIDENCE_DIR}/rt04-session/events.jsonl, ${WORKSPACE_EVIDENCE_DIR}/rt05-session/events.jsonl, ${WORKSPACE_EVIDENCE_DIR}/rt05-session/todo.json, ${WORKSPACE_EVIDENCE_DIR}/rt08-child-session/events.jsonl, ${WORKSPACE_EVIDENCE_DIR}/rt09-child-session/events.jsonl, ${WORKSPACE_EVIDENCE_DIR}/rt11-session/events.jsonl, ${WORKSPACE_EVIDENCE_DIR}/rt13-session/events.jsonl.
Also inspect only these notes and source references: ${WORKSPACE_RUN_DIR}/notes/scenario-index.tsv, ${WORKSPACE_RUN_DIR}/notes/rt04-session-metadata.txt, ${WORKSPACE_RUN_DIR}/notes/rt05-session-metadata.txt, ${WORKSPACE_RUN_DIR}/notes/rt07-steer-metadata.txt, ${WORKSPACE_RUN_DIR}/notes/rt08-delegate-metadata.txt, ${WORKSPACE_RUN_DIR}/notes/rt09-queue-metadata.txt, go-cli-agent/README.md, go-cli-agent/AGENTS.md, go-cli-agent/spec/00-product.md, go-cli-agent/spec/01-runtime-architecture.md, go-cli-agent/spec/08-sdk-and-api-evolution.md, go-cli-agent/spec/10-context-compaction.md, go-cli-agent/spec/11-spec-audit-and-traceability.md, go-cli-agent/spec/12-task-system.md, go-cli-agent/spec/13-live-input-and-steering.md, go-cli-agent/pkg/agent/agent.go, ${TOP_LEVEL_MARKDOWN_LIST}, codex/README.md, and opencode/README.md.
Use this live evidence to judge whether go-cli-agent now materially closes the previous large-project blockers and is ready to surpass codex and opencode on large-project development and review. If any blocker remains, name it explicitly and explain why the current run still does not clear it.
Write ${ABS_ARTIFACT_DIR}/rt20-large-project-readiness.md with sections: live strengths, scorecard, findings, remaining risks, unresolved questions, next architectural moves.
In the scorecard, rate review pipeline, proof quality, compaction and proof-at-boundary, interruption and recovery, task-system and durable memory execution, multi-package coding execution, child and queue orchestration, and cross-repo reasoning.
Then call finish with a one-line summary."
RT20_RAW="${RAW_DIR}/rt20-large-project-readiness.jsonl"
run_exec "$RT20_PROMPT" "$RT20_RAW" "$WORKSPACE_ROOT" 420
RT20_EXIT=$?
finalize_scenario "RT20" "Large Project Readiness Scorecard" "$RT20_EXIT" "$RT20_RAW" "${ARTIFACT_DIR}/rt20-large-project-readiness.md" "$(extract_session_id "$RT20_RAW")"

write_summary
write_issues
printf '%s\n' "$RUN_DIR"
if (( FAILED_SCENARIOS > 0 )); then
	exit 1
fi
