#!/usr/bin/env bash
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKSPACE_ROOT="$(cd "${ROOT_DIR}/.." && pwd)"
cd "$ROOT_DIR"

: "${OPENAI_API_KEY:?OPENAI_API_KEY is required}"

MODEL="${GO_CLI_AGENT_LIVE_MODEL:-gpt-5.4}"
DEFAULT_LIVE_BASE_URL="http://64.186.236.156:24634/v1"
LIVE_BASE_URL="${GO_CLI_AGENT_LIVE_RESPONSES_URL:-${DEFAULT_LIVE_BASE_URL}}"
CONFIG_TEMPLATE_PATH="validation/config.openai-compatible.yaml"
LOW_COMPACT_CONFIG_TEMPLATE_PATH="validation/config.openai-compatible-low-compact.yaml"
MATRIX_LABEL="${GO_CLI_AGENT_TASK_HEAVY_LABEL:-round61-task-heavy-real-matrix}"
ROUND_ID="${1:-$(date -u +%F)-openai-compatible-${MODEL}-${MATRIX_LABEL}}"
RUN_DIR="validation/runs/${ROUND_ID}"
CONFIG_PATH="${RUN_DIR}/config.openai-compatible.effective.yaml"
LOW_COMPACT_CONFIG_PATH="${RUN_DIR}/config.openai-compatible-low-compact.effective.yaml"
NOTES_DIR="${RUN_DIR}/notes"
CASES_DIR="${RUN_DIR}/cases"
WORKSPACE_DIR="${RUN_DIR}/workspaces"
BIN_DIR="${RUN_DIR}/bin"
SUMMARY_PATH="${RUN_DIR}/SUMMARY.md"
ISSUES_PATH="${RUN_DIR}/ISSUES.md"
ABORTED_PATH="${RUN_DIR}/ABORTED.md"
SESSION_ROOT="/root/.go-cli-agent/validation-sessions"
AGENT_BIN="${BIN_DIR}/go-cli-agent"
RETRYPROXY_BIN="${BIN_DIR}/retryproxy"
PLANNED_SCENARIOS=20
MATRIX_PROVIDER_FAILURE_ABORT_THRESHOLD="${GO_CLI_AGENT_TASK_HEAVY_PROVIDER_FAILURE_ABORT_THRESHOLD:-3}"

if [[ -e "$RUN_DIR" ]]; then
	echo "run directory already exists: $RUN_DIR" >&2
	exit 1
fi

mkdir -p "$RUN_DIR" "$NOTES_DIR" "$CASES_DIR" "$WORKSPACE_DIR" "$BIN_DIR"
printf 'check\tstatus\texit_code\tpath\n' >"${NOTES_DIR}/preflight-index.tsv"
printf 'scenario_id\tlabel\tstatus\texit_code\traw\tartifact\tsession_id\n' >"${NOTES_DIR}/scenario-index.tsv"

TOTAL_SCENARIOS=0
PASSED_SCENARIOS=0
FAILED_SCENARIOS=0
SOFT_PREFLIGHT_FAILURES=0
CONSECUTIVE_PROVIDER_INFRA_FAILURES=0
RUN_FINALIZED=0
HARD_ABORT_REASON=""
SCENARIO_HELPER_PID=""
declare -a FAILED_SCENARIO_IDS=()
declare -a FAILED_SCENARIO_LABELS=()
declare -a FAILED_SCENARIO_RAWS=()
declare -a FAILED_SCENARIO_NOTES=()
declare -a SOFT_PREFLIGHT_NAMES=()
declare -a SOFT_PREFLIGHT_PATHS=()

abs_path() {
	local path="$1"
	local dir=""
	dir="$(cd "$(dirname "$path")" && pwd)"
	printf '%s/%s' "$dir" "$(basename "$path")"
}

write_config_with_base_url() {
	local src="$1"
	local dst="$2"
	local base_url="$3"
	local escaped_base_url=""
	escaped_base_url="$(printf '%s' "$base_url" | sed 's/[&]/\\&/g')"
	sed "s#^\([[:space:]]*base_url:\) .*#\1 ${escaped_base_url}#" "$src" >"$dst"
}

prepare_effective_configs() {
	write_config_with_base_url "$CONFIG_TEMPLATE_PATH" "$CONFIG_PATH" "$LIVE_BASE_URL"
	write_config_with_base_url "$LOW_COMPACT_CONFIG_TEMPLATE_PATH" "$LOW_COMPACT_CONFIG_PATH" "$LIVE_BASE_URL"
}

prepare_effective_configs

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
	cp ./bin/go-cli-agent "$AGENT_BIN"
	chmod +x "$AGENT_BIN"
}

write_prompt() {
	local path="$1"
	shift
	printf '%s\n' "$*" >"$path"
}

write_prompt_literal() {
	local path="$1"
	cat >"$path"
}

prepare_isolated_review_workspace() {
	local sandbox_root="$1"
	shift
	local rel=""
	local src=""
	local dst=""
	mkdir -p "$sandbox_root"
	for rel in "$@"; do
		src="${ROOT_DIR}/${rel}"
		dst="${sandbox_root}/${rel}"
		if [[ ! -f "$src" ]]; then
			echo "missing review sandbox source: ${src}" >&2
			return 1
		fi
		mkdir -p "$(dirname "$dst")"
		cp "$src" "$dst"
	done
}

copy_file_into_sandbox() {
	local sandbox_root="$1"
	local source_rel="$2"
	local target_rel="$3"
	local src="${ROOT_DIR}/${source_rel}"
	local dst="${sandbox_root}/${target_rel}"
	if [[ ! -f "$src" ]]; then
		echo "missing sandbox source: ${src}" >&2
		return 1
	fi
	mkdir -p "$(dirname "$dst")"
	cp "$src" "$dst"
}

first_matching_line() {
	local path="$1"
	local pattern="$2"
	if [[ ! -f "$path" ]]; then
		return 0
	fi
	grep -F "$pattern" "$path" | head -n1 || true
}

append_snippet_block() {
	local artifact_path="$1"
	local heading="$2"
	shift 2
	local line=""
	local has_content=0
	for line in "$@"; do
		if [[ -n "$line" ]]; then
			has_content=1
			break
		fi
	done
	if (( has_content == 0 )); then
		return 0
	fi
	{
		echo
		echo "## ${heading}"
		echo
		echo '```text'
		for line in "$@"; do
			if [[ -n "$line" ]]; then
				printf '%s\n' "$line"
			fi
		done
		echo '```'
	} >>"$artifact_path"
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
	grep -o "\"${field}\":\"[^\"]*\"" "$path" | head -n1 | cut -d'"' -f4
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

wait_for_any_pattern() {
	local path="$1"
	local timeout_sec="$2"
	shift 2
	local waited=0
	while (( waited < timeout_sec )); do
		if [[ -f "$path" ]]; then
			local pattern=""
			for pattern in "$@"; do
				if [[ -n "$pattern" ]] && grep -Fq "$pattern" "$path"; then
					printf '%s' "$pattern"
					return 0
				fi
			done
		fi
		sleep 1
		waited=$((waited + 1))
	done
	echo "timed out waiting for any pattern in ${path}" >&2
	return 1
}

wait_for_queue_job_terminal() {
	local job_id="$1"
	local job_raw="$2"
	local children_raw="$3"
	local parent_session_id="$4"
	local timeout_sec="${5:-180}"
	local waited=0
	local status=""
	while (( waited < timeout_sec )); do
		"${AGENT_BIN}" experimental queue show --config "$CONFIG_PATH" --json "$job_id" >"$job_raw" 2>&1 || true
		if [[ -n "$parent_session_id" ]]; then
			"${AGENT_BIN}" experimental children "$parent_session_id" \
				--config "$CONFIG_PATH" \
				--json >"$children_raw" 2>&1 || true
		fi
		status="$(extract_first_json_field "$job_raw" "session_status" "status")"
		case "$status" in
			completed|failed|cancelled)
				return 0
				;;
		esac
		sleep 1
		waited=$((waited + 1))
	done
	echo "timed out waiting for queue job ${job_id} terminal state" >&2
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

merge_if_missing_file() {
	local current="$1"
	local path="$2"
	if [[ ! -f "$path" ]]; then
		printf '%s' "$(merge_exit_code "$current" 1)"
		return 0
	fi
	printf '%s' "$current"
}

merge_if_missing_pattern() {
	local current="$1"
	local path="$2"
	local pattern="$3"
	if [[ ! -f "$path" ]] || ! grep -Fq "$pattern" "$path"; then
		printf '%s' "$(merge_exit_code "$current" 1)"
		return 0
	fi
	printf '%s' "$current"
}

merge_if_missing_any_pattern() {
	local current="$1"
	local path="$2"
	shift 2
	if [[ ! -f "$path" ]]; then
		printf '%s' "$(merge_exit_code "$current" 1)"
		return 0
	fi
	local pattern=""
	for pattern in "$@"; do
		if [[ -n "$pattern" ]] && grep -Fq "$pattern" "$path"; then
			printf '%s' "$current"
			return 0
		fi
	done
	printf '%s' "$(merge_exit_code "$current" 1)"
}

merge_if_missing_exact_line() {
	local current="$1"
	local path="$2"
	local line="$3"
	if [[ ! -f "$path" ]] || ! grep -Fxq "$line" "$path"; then
		printf '%s' "$(merge_exit_code "$current" 1)"
		return 0
	fi
	printf '%s' "$current"
}

first_h2_heading() {
	local path="$1"
	if [[ ! -f "$path" ]]; then
		return 0
	fi
	grep -m1 '^## ' "$path" || true
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

copy_session_evidence() {
	local session_id="$1"
	local dest_dir="$2"
	local session_dir=""
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
	copy_dir_if_present "${session_dir}/control" "${dest_dir}/control"
	copy_dir_if_present "${session_dir}/tasks" "${dest_dir}/tasks"
	copy_dir_if_present "${session_dir}/artifacts/compactions" "${dest_dir}/compactions"
	copy_dir_if_present "${session_dir}/artifacts/transcripts" "${dest_dir}/transcripts"
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
	if grep -Fq 'token_revoked' "$raw_path"; then
		printf 'provider auth path returned token_revoked'
		return 0
	fi
	if grep -Fq 'auth_unavailable' "$raw_path" || grep -Fq 'no auth available' "$raw_path"; then
		printf 'provider auth path returned auth_unavailable'
		return 0
	fi
	if grep -Fq 'upstream_timeout' "$raw_path"; then
		printf 'provider path hit upstream_timeout'
		return 0
	fi
	if grep -Fq 'i/o timeout' "$raw_path" || grep -Fq 'dial tcp' "$raw_path"; then
		printf 'provider path hit transport timeout'
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
	if grep -Fq 'task-heavy-watchdog:' "$raw_path"; then
		printf 'scenario timed out before the expected runtime milestone'
		return 0
	fi
	tail -n 1 "$raw_path" | tr -d '\r'
}

is_provider_infra_failure_note() {
	local note="$1"
	case "$note" in
		*token_invalidated*|*token_revoked*|*auth_unavailable*|*upstream_timeout*|*transport\ timeout*)
			return 0
			;;
		*)
			return 1
			;;
	esac
}

record_preflight() {
	local name="$1"
	local exit_code="$2"
	local path="$3"
	printf '%s\t%s\t%s\t%s\n' "$name" "$(status_from_exit "$exit_code")" "$exit_code" "$path" >>"${NOTES_DIR}/preflight-index.tsv"
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
	local note_path="${NOTES_DIR}/preflight-${name}.md"
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

write_gap_proof_summary() {
	local exit_code="$1"
	local output_path="$2"
	local note_path="${NOTES_DIR}/preflight-gap-proof-summary.md"
	{
		echo "# gap proof summary"
		echo
		echo "- status: $(status_from_exit "$exit_code")"
		echo "- exit_code: ${exit_code}"
		echo "- raw: \`${output_path}\`"
		echo
		echo "## directly proven areas"
		echo
		echo "### Provider metadata and retry durability"
		echo
		echo "- When green, \`TestRunnerStartPersistsProviderOptionsInSessionMetadata\` proves configured provider options are written into durable session metadata at session start."
		echo "- When green, \`TestRunnerStartPropagatesConfiguredProviderOptionsIntoOpenAIRequest\` proves stored provider options are forwarded into the outbound OpenAI-compatible Responses request."
		echo "- When green, \`TestRunnerContinueUsesDurableRetryPolicyFromSessionMetadata\` proves continue/resume rebuilds adapter retry behavior from durable session metadata rather than current process drift."
		echo "- When green, \`TestEnginePassesSessionMetadataIntoProviderRequest\` and \`TestEngineEmitsProviderRequestPreparedEvent\` prove session metadata and retry-policy facts are visible on the runtime-to-provider boundary and in durable prepared-event evidence."
		echo
		echo "### Review artifact enforcement and exact-template guard"
		echo
		echo "- When green, \`TestToolGuardBlocksInvalidReviewArtifactWriteOnReviewLikeScratchPath\` proves review-like artifact writes are blocked when the canonical findings structure is missing."
		echo "- When green, \`TestReviewArtifactSatisfiedCountsValidatedRequestedPathWhenPresent\` proves finish satisfaction stays tied to a validated requested artifact path once one is explicitly requested."
		echo "- When green, \`TestValidateMarkdownArtifactWithWorkspaceRejectsMissingEvidenceSnippet\` and \`TestValidateMarkdownArtifactWithWorkspaceRejectsUnreadableEvidencePath\` prove the validator rejects review artifacts that lack snippet-backed evidence or cite unreadable in-workspace paths."
		echo "- When green, \`TestToolGuardBlocksFinalArtifactWriteThatViolatesExactTemplate\` proves exact-template audit tasks reject final artifact writes that drift from the required opening block."
		echo
		echo "### Report prevalidation, path hardening, and task-heavy workflow carry"
		echo
		echo "- When green, \`TestEngineBlocksEscapingFinalArtifactPathBeforeToolExecution\` proves a requested final report path is blocked before the real tool executes when it escapes the workspace."
		echo "- When green, \`TestRequestedArtifactWriteRejectsWorkspaceEscapeForEditFile\`, \`TestRequestedArtifactWriteRejectsSymlinkEscapeForWriteFile\`, and \`TestRequestedArtifactWriteUsesSameWorkspaceResolverAsFileTools\` prove review-artifact path prevalidation reuses the same workspace resolver and rejects escape attempts, including symlink escapes."
		echo "- When green, \`TestResolveWorkspacePathRejectsSymlinkEscape\` proves the shared file-tool workspace resolver itself rejects symlink-based escapes."
		echo "- When green, \`TestEngineInterruptSteerCancelsProviderAndContinuesWithAcceptedMessage\`, \`TestRunnerQueueSubmitAndWorkerCompletesJob\`, \`TestRunnerDelegateCopiesVisibleOutputsIntoRequestedWorkspace\`, and \`TestNextHarnessReminderRefreshesSpecAndPlanAfterSteerScopeChange\` prove the task-heavy matrix still preflights interrupt-steer recovery, queued child completion, visible-output handoff, and steer-driven durable project-memory refresh."
		if (( exit_code == 0 )); then
			echo
			echo "## operator note"
			echo
			echo "These proof-focused tests passed in the current run, so later readiness or issue-inventory scenarios may treat provider metadata/retry durability, review-artifact enforcement, report path hardening, exact-template guard behavior, and the listed task-heavy workflow proofs as directly covered unless later live evidence contradicts them."
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
	printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$scenario_id" "$label" "$status" "$exit_code" "$raw_path" "$artifact_path" "$session_id" >>"${NOTES_DIR}/scenario-index.tsv"
}

write_case_note() {
	local scenario_id="$1"
	local label="$2"
	local status="$3"
	local exit_code="$4"
	local raw_path="$5"
	local artifact_path="$6"
	local session_id="${7:-}"
	local failure_note="${8:-}"
	local case_dir="$9"
	{
		echo "# ${scenario_id} ${label}"
		echo
		echo "- status: ${status}"
		echo "- exit_code: ${exit_code}"
		echo "- raw: \`${raw_path}\`"
		echo "- artifact: \`${artifact_path}\`"
		echo "- session_id: \`${session_id:-}\`"
		if [[ -f "${case_dir}/postcheck.txt" ]]; then
			echo "- postcheck: \`${case_dir}/postcheck.txt\`"
		fi
		if [[ "$status" != "passed" ]]; then
			if [[ -n "$failure_note" ]]; then
				echo "- failure_reason: ${failure_note}"
			else
				echo "- failure_reason: $(failure_reason "$raw_path")"
			fi
		fi
		if [[ -f "$raw_path" ]]; then
			echo
			echo "## Tail"
			echo
			echo '```'
			tail -n 40 "$raw_path"
			echo '```'
		fi
	} >"${case_dir}/note.md"
}

finalize_case() {
	local scenario_id="$1"
	local label="$2"
	local exit_code="$3"
	local raw_path="$4"
	local artifact_path="$5"
	local session_id="${6:-}"
	local failure_note="${7:-}"
	local case_dir="$8"
	exit_code="$(merge_if_missing_file "$exit_code" "$raw_path")"
	exit_code="$(merge_if_missing_file "$exit_code" "$artifact_path")"
	local status
	local resolved_failure_note="$failure_note"
	status="$(status_from_exit "$exit_code")"
	if [[ "$status" != "passed" && -z "$resolved_failure_note" ]]; then
		resolved_failure_note="$(failure_reason "$raw_path")"
	fi
	TOTAL_SCENARIOS=$((TOTAL_SCENARIOS + 1))
	if [[ "$status" == "passed" ]]; then
		PASSED_SCENARIOS=$((PASSED_SCENARIOS + 1))
		CONSECUTIVE_PROVIDER_INFRA_FAILURES=0
	else
		FAILED_SCENARIOS=$((FAILED_SCENARIOS + 1))
		FAILED_SCENARIO_IDS+=("$scenario_id")
		FAILED_SCENARIO_LABELS+=("$label")
		FAILED_SCENARIO_RAWS+=("$raw_path")
		FAILED_SCENARIO_NOTES+=("$resolved_failure_note")
		if is_provider_infra_failure_note "$resolved_failure_note"; then
			CONSECUTIVE_PROVIDER_INFRA_FAILURES=$((CONSECUTIVE_PROVIDER_INFRA_FAILURES + 1))
		else
			CONSECUTIVE_PROVIDER_INFRA_FAILURES=0
		fi
	fi
	record_scenario "$scenario_id" "$label" "$status" "$exit_code" "$raw_path" "$artifact_path" "$session_id"
	write_case_note "$scenario_id" "$label" "$status" "$exit_code" "$raw_path" "$artifact_path" "$session_id" "$resolved_failure_note" "$case_dir"
	if [[ "$status" != "passed" &&
		-z "$HARD_ABORT_REASON" &&
		"$CONSECUTIVE_PROVIDER_INFRA_FAILURES" -ge "$MATRIX_PROVIDER_FAILURE_ABORT_THRESHOLD" ]]; then
		HARD_ABORT_REASON="provider path became unstable after ${CONSECUTIVE_PROVIDER_INFRA_FAILURES} consecutive case failures ending at ${scenario_id}: ${resolved_failure_note}"
		finalize_run_outputs
		exit 1
	fi
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
		printf 'task-heavy-watchdog: no first-turn resolution within %ss\n' "$first_turn_timeout_sec" >>"$raw_path"
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
		printf 'task-heavy-watchdog: no first-turn resolution within %ss\n' "$first_turn_timeout_sec" >>"$raw_path"
		kill "$pid" 2>/dev/null || true
		wait "$pid" 2>/dev/null || true
		return 124
	fi
	wait "$pid"
}

run_run() {
	run_run_with_config "$CONFIG_PATH" "$@"
}

write_summary() {
	{
		echo "# Task-Heavy Matrix Summary"
		echo
		echo "## Run metadata"
		echo
		echo "- Run directory: \`${RUN_DIR}\`"
		echo "- Matrix label: \`${MATRIX_LABEL}\`"
		echo "- Provider: \`openai-compatible\`"
		echo "- Wire API: \`responses\`"
		echo "- Model: \`${MODEL}\`"
		echo "- Base URL: \`${LIVE_BASE_URL}\`"
		echo "- Effective config: \`${CONFIG_PATH}\`"
		echo "- Effective low-compact config: \`${LOW_COMPACT_CONFIG_PATH}\`"
		echo "- Scenario design: \`validation/task_heavy_scenarios.md\`"
		echo "- Preflight index: \`notes/preflight-index.tsv\`"
		echo "- Gap-proof summary: \`notes/preflight-gap-proof-summary.md\`"
		echo "- Scenario index: \`notes/scenario-index.tsv\`"
		if [[ -n "$HARD_ABORT_REASON" ]]; then
			echo "- Matrix status: aborted"
			echo "- Abort reason: ${HARD_ABORT_REASON}"
		else
			echo "- Matrix status: ${PASSED_SCENARIOS}/${TOTAL_SCENARIOS} scenarios passed; ${FAILED_SCENARIOS} failed."
		fi
		if (( SOFT_PREFLIGHT_FAILURES > 0 )); then
			echo
			echo "## Soft preflight failures"
			echo
			for i in "${!SOFT_PREFLIGHT_NAMES[@]}"; do
				echo "- ${SOFT_PREFLIGHT_NAMES[$i]} failed. output: \`${SOFT_PREFLIGHT_PATHS[$i]}\`"
			done
		fi
		if (( FAILED_SCENARIOS > 0 )); then
			echo
			echo "## Failed scenarios"
			echo
			for i in "${!FAILED_SCENARIO_IDS[@]}"; do
				echo "- ${FAILED_SCENARIO_IDS[$i]} ${FAILED_SCENARIO_LABELS[$i]}: ${FAILED_SCENARIO_NOTES[$i]}"
				echo "  raw: \`${FAILED_SCENARIO_RAWS[$i]}\`"
			done
		fi
		if [[ -z "$HARD_ABORT_REASON" && "$FAILED_SCENARIOS" -eq 0 ]]; then
			echo
			echo "## Matrix conclusion"
			echo
			echo "- All planned task-heavy scenarios completed without a scenario-level command failure."
			echo "- This run should be read alongside the proof-first round31 matrix, not as a replacement for it."
		fi
	} >"$SUMMARY_PATH"
}

write_issues() {
	{
		echo "# Task-Heavy Matrix Issues"
		echo
		echo "## Open issues"
		echo
		if [[ -n "$HARD_ABORT_REASON" || "$FAILED_SCENARIOS" -gt 0 || "$SOFT_PREFLIGHT_FAILURES" -gt 0 ]]; then
			if [[ -n "$HARD_ABORT_REASON" ]]; then
				echo "1. High: matrix aborted before the full task-heavy suite finished."
				echo "   Evidence: \`${ABORTED_PATH}\` and \`notes/preflight-index.tsv\`."
				echo "   Why it matters: the run did not produce complete task-family evidence."
				echo "   Smallest next fix: stabilize the provider/harness path and rerun from a fresh run directory."
				echo
			fi
			local offset=1
			for i in "${!SOFT_PREFLIGHT_NAMES[@]}"; do
				local idx=$((offset + i))
				echo "${idx}. Medium: soft preflight check \`${SOFT_PREFLIGHT_NAMES[$i]}\` failed."
				echo "   Evidence: \`${SOFT_PREFLIGHT_PATHS[$i]}\`."
				echo "   Why it matters: the live provider/operator path already showed instability before the main task cases."
				echo "   Smallest next fix: inspect the captured output and rerun once the provider path is stable."
				echo
			done
			offset=$((offset + SOFT_PREFLIGHT_FAILURES))
			for i in "${!FAILED_SCENARIO_IDS[@]}"; do
				local idx=$((offset + i))
				echo "${idx}. High: scenario \`${FAILED_SCENARIO_IDS[$i]}\` failed."
				echo "   Evidence: \`${FAILED_SCENARIO_RAWS[$i]}\` plus \`cases/${FAILED_SCENARIO_IDS[$i]}/note.md\`."
				echo "   Why it matters: that task family did not produce trustworthy evidence."
				echo "   Smallest next fix: resolve the concrete failure reason and rerun the affected task family."
				echo
			done
		else
			echo "No open issues recorded by this matrix run."
		fi
	} >"$ISSUES_PATH"
}

write_aborted_note() {
	local reason="$1"
	{
		echo "# Task-Heavy Matrix Aborted"
		echo
		echo "- Run directory: \`${RUN_DIR}\`"
		echo "- Reason: ${reason}"
		echo "- Preflight index: \`notes/preflight-index.tsv\`"
	} >"$ABORTED_PATH"
}

finalize_run_outputs() {
	if (( RUN_FINALIZED != 0 )); then
		return 0
	fi
	if [[ -n "$HARD_ABORT_REASON" && ! -f "$ABORTED_PATH" ]]; then
		write_aborted_note "$HARD_ABORT_REASON"
	fi
	write_summary
	write_issues
	RUN_FINALIZED=1
}

write_case_bucket_summary() {
	local path="${NOTES_DIR}/case-buckets.md"
	{
		echo "# Task-Heavy Case Buckets"
		echo
		echo "This note groups the current task-heavy run into benchmark buckets so parent-level readiness review can stay compact."
		echo
		echo "## repaired seeded defect"
		echo "- TT01 Python Smallest Correct Fix -> \`cases/TT01/artifact.md\`"
		echo "- TT02 Go Smallest Correct Fix -> \`cases/TT02/artifact.md\`"
		echo "- TT06 Platform Go Multi-Package Repair -> \`cases/TT06/artifact.md\`"
		echo "- TT08 Platform Python Multi-Module Repair -> \`cases/TT08/artifact.md\`"
		echo
		echo "## review and benchmark boundary"
		echo "- TT07 Platform Go Post-Fix Review -> \`cases/TT07/artifact.md\`"
		echo "- TT09 Platform Python Post-Fix Review -> \`cases/TT09/artifact.md\`"
		echo "- TT10 Nested API Review -> \`cases/TT10/artifact.md\`"
		echo "- TT11 Foreground Delegated Review With Role And Children Proof -> \`cases/TT11/artifact.md\`"
		echo "- TT12 Background Queue Review With Role And Children Proof -> \`cases/TT12/artifact.md\`"
		echo "- TT13 Exact-Template Audit Guard -> \`cases/TT13/artifact.md\`"
		echo "- TT20 Task-Heavy Readiness And Issue Inventory -> \`cases/TT20/artifact.md\`"
		echo
		echo "## workflow proof"
		echo "- TT03 Incident Recovery With Durable Task Graph -> \`cases/TT03/artifact.md\`"
		echo "- TT04 Awaiting Input Docset Continuation -> \`cases/TT04/artifact.md\`"
		echo "- TT05 Same-Task Go Repair With Two Interrupt Steers -> \`cases/TT05/artifact.md\`"
		echo "- TT15 Interrupt -> Resume -> Completion -> \`cases/TT15/artifact.md\`"
		echo "- TT16 Oversized Steer Rejection -> \`cases/TT16/artifact.md\`"
		echo "- TT17 Provider Cancel And Interrupt Preemption -> \`cases/TT17/artifact.md\`"
		echo "- TT18 Web Console Deep Smoke -> \`cases/TT18/artifact.md\`"
		echo "- TT19 Retry-Resume And Queue-Dedup Operator Proof -> \`cases/TT19/artifact.md\`"
		echo
		echo "## owning-runtime proof"
		echo "- TT14 Forced Compaction And Proof-Carry -> \`cases/TT14/artifact.md\`"
	} >"$path"
}

handle_run_exit() {
	local exit_code="$1"
	if [[ -n "$SCENARIO_HELPER_PID" ]]; then
		kill "$SCENARIO_HELPER_PID" 2>/dev/null || true
		wait "$SCENARIO_HELPER_PID" 2>/dev/null || true
		SCENARIO_HELPER_PID=""
	fi
	if (( RUN_FINALIZED != 0 )); then
		return 0
	fi
	if [[ -z "$HARD_ABORT_REASON" ]]; then
		if (( exit_code != 0 )); then
			HARD_ABORT_REASON="matrix exited unexpectedly with code ${exit_code}"
		elif (( TOTAL_SCENARIOS < PLANNED_SCENARIOS )); then
			HARD_ABORT_REASON="matrix exited before all planned scenarios were finalized"
		fi
	fi
	finalize_run_outputs
}

trap 'handle_run_exit $?' EXIT

run_preflight "build" "${RUN_DIR}/preflight-build.txt" ./build.sh
PRE_BUILD_EXIT=$?
write_preflight_note "build" "$PRE_BUILD_EXIT" "${RUN_DIR}/preflight-build.txt"
if (( PRE_BUILD_EXIT != 0 )); then
	HARD_ABORT_REASON="./build.sh failed"
	finalize_run_outputs
	printf '%s\n' "$RUN_DIR"
	exit 1
fi

prepare_agent_bin || {
	HARD_ABORT_REASON="failed to stage agent binary"
	finalize_run_outputs
	printf '%s\n' "$RUN_DIR"
	exit 1
}

run_preflight "build-retryproxy" "${RUN_DIR}/preflight-build-retryproxy.txt" \
	go build -o "${RETRYPROXY_BIN}" ./validation/cmd/retryproxy
PRE_RETRYPROXY_EXIT=$?
write_preflight_note "build-retryproxy" "$PRE_RETRYPROXY_EXIT" "${RUN_DIR}/preflight-build-retryproxy.txt"
if (( PRE_RETRYPROXY_EXIT != 0 )); then
	HARD_ABORT_REASON="failed to build retryproxy helper"
	finalize_run_outputs
	printf '%s\n' "$RUN_DIR"
	exit 1
fi

run_preflight "test" "${RUN_DIR}/preflight-test.txt" ./test.sh
PRE_TEST_EXIT=$?
write_preflight_note "test" "$PRE_TEST_EXIT" "${RUN_DIR}/preflight-test.txt"
if (( PRE_TEST_EXIT != 0 )); then
	HARD_ABORT_REASON="./test.sh failed"
	finalize_run_outputs
	printf '%s\n' "$RUN_DIR"
	exit 1
fi

run_preflight "task-heavy-proof-tests" "${RUN_DIR}/preflight-task-heavy-proof-tests.txt" \
	go test ./internal/runtime ./internal/review ./internal/tools -run 'TestRunnerStartPersistsProviderOptionsInSessionMetadata|TestRunnerStartPropagatesConfiguredProviderOptionsIntoOpenAIRequest|TestRunnerContinueUsesDurableRetryPolicyFromSessionMetadata|TestEnginePassesSessionMetadataIntoProviderRequest|TestEngineEmitsProviderRequestPreparedEvent|TestEngineBlocksEscapingFinalArtifactPathBeforeToolExecution|TestToolGuardBlocksInvalidReviewArtifactWriteOnReviewLikeScratchPath|TestReviewArtifactSatisfiedCountsValidatedRequestedPathWhenPresent|TestRequestedArtifactWriteRejectsWorkspaceEscapeForEditFile|TestRequestedArtifactWriteRejectsSymlinkEscapeForWriteFile|TestRequestedArtifactWriteUsesSameWorkspaceResolverAsFileTools|TestValidateMarkdownArtifactWithWorkspaceRejectsMissingEvidenceSnippet|TestValidateMarkdownArtifactWithWorkspaceRejectsUnreadableEvidencePath|TestResolveWorkspacePathRejectsSymlinkEscape|TestEngineInterruptSteerCancelsProviderAndContinuesWithAcceptedMessage|TestRunnerQueueSubmitAndWorkerCompletesJob|TestRunnerDelegateCopiesVisibleOutputsIntoRequestedWorkspace|TestToolGuardBlocksFinalArtifactWriteThatViolatesExactTemplate|TestNextHarnessReminderRefreshesSpecAndPlanAfterSteerScopeChange' -count=1
PRE_PROOF_EXIT=$?
write_preflight_note "task-heavy-proof-tests" "$PRE_PROOF_EXIT" "${RUN_DIR}/preflight-task-heavy-proof-tests.txt"
write_gap_proof_summary "$PRE_PROOF_EXIT" "${RUN_DIR}/preflight-task-heavy-proof-tests.txt"
if (( PRE_PROOF_EXIT != 0 )); then
	HARD_ABORT_REASON="task-heavy proof-focused tests failed"
	finalize_run_outputs
	printf '%s\n' "$RUN_DIR"
	exit 1
fi

run_preflight "webconsole-tests" "${RUN_DIR}/preflight-webconsole-tests.txt" \
	go test ./internal/webconsole -run 'TestServiceStartSessionPersistsAgentIdentity|TestServiceParallelQueueWorkersPersistAllJobs|TestServiceServesEmbeddedShellAndAssets' -count=1
PRE_WEBCONSOLE_EXIT=$?
write_preflight_note "webconsole-tests" "$PRE_WEBCONSOLE_EXIT" "${RUN_DIR}/preflight-webconsole-tests.txt"
if (( PRE_WEBCONSOLE_EXIT != 0 )); then
	HARD_ABORT_REASON="internal/webconsole focused tests failed"
	finalize_run_outputs
	printf '%s\n' "$RUN_DIR"
	exit 1
fi

run_preflight "doctor" "${RUN_DIR}/preflight-doctor.json" "${AGENT_BIN}" doctor \
	--config "$CONFIG_PATH" \
	--provider openai-compatible \
	--json
PRE_DOCTOR_EXIT=$?
write_preflight_note "doctor" "$PRE_DOCTOR_EXIT" "${RUN_DIR}/preflight-doctor.json"
if (( PRE_DOCTOR_EXIT != 0 )); then
	SOFT_PREFLIGHT_FAILURES=$((SOFT_PREFLIGHT_FAILURES + 1))
	SOFT_PREFLIGHT_NAMES+=("doctor")
	SOFT_PREFLIGHT_PATHS+=("${RUN_DIR}/preflight-doctor.json")
fi

run_preflight "probe" "${RUN_DIR}/preflight-probe.json" "${AGENT_BIN}" probe-provider \
	--config "$CONFIG_PATH" \
	--provider openai-compatible \
	--json
PRE_PROBE_EXIT=$?
write_preflight_note "probe" "$PRE_PROBE_EXIT" "${RUN_DIR}/preflight-probe.json"
if (( PRE_PROBE_EXIT != 0 )); then
	SOFT_PREFLIGHT_FAILURES=$((SOFT_PREFLIGHT_FAILURES + 1))
	SOFT_PREFLIGHT_NAMES+=("probe")
	SOFT_PREFLIGHT_PATHS+=("${RUN_DIR}/preflight-probe.json")
fi

PREFLIGHT_REAL_PROMPT="${RUN_DIR}/preflight-real-turn.prompt.txt"
write_prompt "$PREFLIGHT_REAL_PROMPT" "Inspect only README.md and AGENTS.md in the current go-cli-agent repository.
Use targeted retrieval only.
Call finish with a short message that states the current core-v1 default command surface and whether experimental commands sit behind an explicit entrypoint."
run_exec_exact "$PREFLIGHT_REAL_PROMPT" "${RUN_DIR}/preflight-real-turn.jsonl" "$ROOT_DIR" 20 20
PRE_REAL_EXIT=$?
record_preflight "real-turn" "$PRE_REAL_EXIT" "${RUN_DIR}/preflight-real-turn.jsonl"
write_preflight_note "real-turn" "$PRE_REAL_EXIT" "${RUN_DIR}/preflight-real-turn.jsonl"
if (( PRE_REAL_EXIT != 0 )); then
	SOFT_PREFLIGHT_FAILURES=$((SOFT_PREFLIGHT_FAILURES + 1))
	SOFT_PREFLIGHT_NAMES+=("real-turn")
	SOFT_PREFLIGHT_PATHS+=("${RUN_DIR}/preflight-real-turn.jsonl")
fi

copy_workspace patch
copy_workspace patch_go
copy_workspace incident
copy_workspace docset
copy_workspace platform_go
copy_workspace_as platform_go tt05_platform_go
copy_workspace_as platform_go tt11_platform_go
copy_workspace platform_py
copy_workspace_as platform_py tt12_platform_py
copy_workspace nested_review
copy_workspace_as docset tt15_docset
copy_workspace_as docset tt16_docset

PATCH_DIR="${WORKSPACE_DIR}/patch"
PATCH_GO_DIR="${WORKSPACE_DIR}/patch_go"
INCIDENT_DIR="${WORKSPACE_DIR}/incident"
DOCSET_DIR="${WORKSPACE_DIR}/docset"
PLATFORM_GO_DIR="${WORKSPACE_DIR}/platform_go"
TT05_GO_DIR="${WORKSPACE_DIR}/tt05_platform_go"
TT11_GO_DIR="${WORKSPACE_DIR}/tt11_platform_go"
PLATFORM_PY_DIR="${WORKSPACE_DIR}/platform_py"
TT12_PY_DIR="${WORKSPACE_DIR}/tt12_platform_py"
NESTED_API_DIR="${WORKSPACE_DIR}/nested_review/services/api"
TT15_DOCSET_DIR="${WORKSPACE_DIR}/tt15_docset"
TT16_DOCSET_DIR="${WORKSPACE_DIR}/tt16_docset"

WORKSPACE_RUN_DIR="go-cli-agent/${RUN_DIR}"

TT01_DIR="${CASES_DIR}/TT01"
mkdir -p "$TT01_DIR"
TT01_PROMPT="${TT01_DIR}/prompt.txt"
TT01_RAW="${TT01_DIR}/raw.jsonl"
TT01_ARTIFACT="${TT01_DIR}/artifact.md"
write_prompt "$TT01_PROMPT" "Read the local AGENTS.md first.
This is a real smallest-correct-fix task.
This is intentionally not a large-project task. Do not create a todo list, task board, or durable reports stack under reports/spec.md, reports/plan.md, reports/progress.md, or reports/validation.md.
Start with python3 -m unittest -q test_inventory.py, inspect only the files implicated by that failure, fix only the root cause needed for the current tests to pass, then run python3 -m unittest -q before finishing.
Do not use shell to invoke apply_patch or other external patch helpers. Use the built-in file editing tools for code changes.
If you need to preserve command output for later reading, redirect it into reports/validation.txt instead of reading .artifacts/tool-outputs.
Write reports/change-summary.md with sections: root cause, changed code, validation.
Then call finish."
run_exec "$TT01_PROMPT" "$TT01_RAW" "$PATCH_DIR" 240
TT01_EXIT=$?
(cd "$PATCH_DIR" && python3 -m unittest -q) >"${TT01_DIR}/postcheck.txt" 2>&1
TT01_POSTCHECK_EXIT=$?
TT01_EXIT="$(merge_exit_code "$TT01_EXIT" "$TT01_POSTCHECK_EXIT")"
copy_if_present "${PATCH_DIR}/reports/change-summary.md" "$TT01_ARTIFACT"
finalize_case "TT01" "Python Smallest Correct Fix" "$TT01_EXIT" "$TT01_RAW" "$TT01_ARTIFACT" "$(extract_session_id "$TT01_RAW")" "" "$TT01_DIR"

TT02_DIR="${CASES_DIR}/TT02"
mkdir -p "$TT02_DIR"
TT02_PROMPT="${TT02_DIR}/prompt.txt"
TT02_RAW="${TT02_DIR}/raw.jsonl"
TT02_ARTIFACT="${TT02_DIR}/artifact.md"
write_prompt "$TT02_PROMPT" "Read the local AGENTS.md first.
This is a real smallest-correct-fix task.
This is intentionally not a large-project task. Do not create a todo list, task board, or durable reports stack under reports/spec.md, reports/plan.md, reports/progress.md, or reports/validation.md.
Start with go test ./..., inspect only the files implicated by the first failing output, fix only the root cause needed for the current tests to pass, then run go test ./... before finishing.
Do not use shell to invoke apply_patch or other external patch helpers. Use the built-in file editing tools for code changes.
If you need to preserve command output for later reading, redirect it into reports/validation.txt instead of reading .artifacts/tool-outputs.
Write reports/change-summary.md with sections: root cause, changed code, validation.
Then call finish."
run_exec "$TT02_PROMPT" "$TT02_RAW" "$PATCH_GO_DIR" 240
TT02_EXIT=$?
(cd "$PATCH_GO_DIR" && go test ./...) >"${TT02_DIR}/postcheck.txt" 2>&1
TT02_POSTCHECK_EXIT=$?
TT02_EXIT="$(merge_exit_code "$TT02_EXIT" "$TT02_POSTCHECK_EXIT")"
copy_if_present "${PATCH_GO_DIR}/reports/change-summary.md" "$TT02_ARTIFACT"
finalize_case "TT02" "Go Smallest Correct Fix" "$TT02_EXIT" "$TT02_RAW" "$TT02_ARTIFACT" "$(extract_session_id "$TT02_RAW")" "" "$TT02_DIR"

TT03_DIR="${CASES_DIR}/TT03"
mkdir -p "$TT03_DIR"
TT03_PROMPT="${TT03_DIR}/prompt.txt"
TT03_RAW="${TT03_DIR}/raw.jsonl"
TT03_ARTIFACT="${TT03_DIR}/artifact.md"
write_prompt "$TT03_PROMPT" "Use the incident_triage skill for this task.
Read the local AGENTS.md first.
This is only the planning/setup phase. Build a durable task graph with at least four tasks and one explicit dependency edge, and keep a durable project-memory stack under reports/spec.md, reports/plan.md, reports/progress.md, and reports/validation.md.
You may inspect enough local evidence to scope the investigation, but do not write reports/incident-summary.md or reports/taskboard-summary.md yet, do not declare a final root cause, and do not call finish.
Stop naturally without calling finish once the durable task board plus project-memory stack are ready because a follow-up message will trigger the execution phase."
run_run "$TT03_PROMPT" "$TT03_RAW" "$INCIDENT_DIR" 300
TT03_EXIT=$?
TT03_SESSION_ID="$(extract_session_id "$TT03_RAW")"
if [[ -n "$TT03_SESSION_ID" ]] && run_reached_awaiting_input "$TT03_RAW"; then
	"${AGENT_BIN}" continue "$TT03_SESSION_ID" \
		--config "$CONFIG_PATH" \
		--provider openai-compatible \
		--model "$MODEL" \
		--json \
		--message "Use the existing durable task graph and report stack to execute the incident investigation fully. Inspect logs and configuration, build a confirmed timeline before root-cause judgment, update the durable task graph to match the executed state, refresh reports/progress.md and reports/validation.md, write reports/incident-summary.md, reports/taskboard-summary.md, and reports/recovery-summary.md with sections: current durable state, task graph changes, next blocking questions, and then call finish." \
		>"${TT03_DIR}/continue.jsonl" 2>&1
	TT03_CONTINUE_EXIT=$?
	TT03_EXIT="$(merge_exit_code "$TT03_EXIT" "$TT03_CONTINUE_EXIT")"
else
	TT03_EXIT="$(merge_exit_code "$TT03_EXIT" 1)"
fi
copy_if_present "${INCIDENT_DIR}/reports/recovery-summary.md" "$TT03_ARTIFACT"
copy_if_present "${INCIDENT_DIR}/reports/incident-summary.md" "${TT03_DIR}/incident-summary.md"
copy_if_present "${INCIDENT_DIR}/reports/taskboard-summary.md" "${TT03_DIR}/taskboard-summary.md"
copy_session_evidence "$TT03_SESSION_ID" "${TT03_DIR}/evidence/session"
TT03_FINAL_RAW="${TT03_DIR}/continue.jsonl"
if [[ ! -f "$TT03_FINAL_RAW" ]]; then
	TT03_FINAL_RAW="$TT03_RAW"
fi
finalize_case "TT03" "Incident Recovery With Durable Task Graph" "$TT03_EXIT" "$TT03_FINAL_RAW" "$TT03_ARTIFACT" "$TT03_SESSION_ID" "" "$TT03_DIR"

TT04_DIR="${CASES_DIR}/TT04"
mkdir -p "$TT04_DIR"
TT04_PROMPT="${TT04_DIR}/prompt.txt"
TT04_RAW="${TT04_DIR}/raw.jsonl"
TT04_ARTIFACT="${TT04_DIR}/artifact.md"
write_prompt "$TT04_PROMPT" "Read the local AGENTS.md first.
Review only product_overview.md, ops_notes.md, and release_constraints.md in this workspace.
Create reports/spec.md with a scoped problem statement and reports/plan.md with the main decision branches.
Stop naturally without calling finish once you need a stakeholder choice between onboarding, permissions, and rollout sequencing."
run_run "$TT04_PROMPT" "$TT04_RAW" "$DOCSET_DIR" 240
TT04_EXIT=$?
TT04_SESSION_ID="$(extract_session_id "$TT04_RAW")"
if [[ -n "$TT04_SESSION_ID" ]]; then
	"${AGENT_BIN}" continue "$TT04_SESSION_ID" \
		--config "$CONFIG_PATH" \
		--provider openai-compatible \
		--model "$MODEL" \
		--json \
		--message "Prioritize onboarding. Refresh reports/spec.md, reports/plan.md, reports/progress.md, and reports/validation.md, then write reports/continue-brief.md with sections: chosen priority, supporting evidence, next steps, unresolved questions. Then call finish." \
		>"${TT04_DIR}/continue.jsonl" 2>&1
	TT04_CONTINUE_EXIT=$?
	TT04_EXIT="$(merge_exit_code "$TT04_EXIT" "$TT04_CONTINUE_EXIT")"
else
	TT04_EXIT="$(merge_exit_code "$TT04_EXIT" 1)"
fi
copy_if_present "${DOCSET_DIR}/reports/continue-brief.md" "$TT04_ARTIFACT"
copy_session_evidence "$TT04_SESSION_ID" "${TT04_DIR}/evidence/session"
TT04_FINAL_RAW="${TT04_DIR}/continue.jsonl"
if [[ ! -f "$TT04_FINAL_RAW" ]]; then
	TT04_FINAL_RAW="$TT04_RAW"
fi
finalize_case "TT04" "Awaiting Input Docset Continuation" "$TT04_EXIT" "$TT04_FINAL_RAW" "$TT04_ARTIFACT" "$TT04_SESSION_ID" "" "$TT04_DIR"

TT05_DIR="${CASES_DIR}/TT05"
mkdir -p "$TT05_DIR"
TT05_PROMPT="${TT05_DIR}/prompt.txt"
TT05_RAW="${TT05_DIR}/raw.jsonl"
TT05_ARTIFACT="${TT05_DIR}/artifact.md"
TT05_LOCAL_ARTIFACT="reports/tt05-proof.md"
write_prompt "$TT05_PROMPT" "Read the local AGENTS.md and README.md first.
This is a same-task multi-package repair proof, not a review-only report.
Before editing code, create reports/spec.md, reports/plan.md, reports/progress.md, and reports/validation.md in this workspace. Also create a short todo list and at least two durable tasks, and update them as the work progresses.
Constrain the first repair pass to README.md, internal/api/handler.go, internal/service/service.go, internal/config/config.go, internal/quota/policy.go, internal/api/handler_test.go, internal/config/config_test.go, and internal/quota/policy_test.go unless failing build output explicitly points somewhere else.
Start with the narrow failing tests, then fix this workspace so repo-wide go test ./... passes.
Write ${TT05_LOCAL_ARTIFACT} with sections: confirmed runtime evidence, findings, same-task repair outcome, remaining risks, next validation moves.
If repo-wide go test ./... passes, write the exact sentence No validated findings. inside the findings section.
In the final artifact, literally mention these proof tokens in the evidence text: task.created, task.updated, todo.updated, session.steer.accepted, provider.request.prepared, and go test ./....
Once repo-wide go test ./... is green, write the local proof artifact and call finish in the same final turn.
Do not call finish until repo-wide go test ./... passes and the local proof artifact is written."
"${AGENT_BIN}" exec \
	--config "$CONFIG_PATH" \
	--provider openai-compatible \
	--model "$MODEL" \
	--workdir "$TT05_GO_DIR" \
	--json \
	--timeout 420 \
	<"$TT05_PROMPT" >"$TT05_RAW" 2>&1 &
TT05_PID="$!"
TT05_EXIT=0
wait_for_pattern "$TT05_RAW" '"type":"session.started"' 90 || true
TT05_SESSION_ID="$(extract_session_id "$TT05_RAW")"
wait_for_pattern "$TT05_RAW" '"tool_name":"task_create"' 180 || true
if [[ -n "$TT05_SESSION_ID" ]]; then
	"${AGENT_BIN}" steer "$TT05_SESSION_ID" \
		--config "$CONFIG_PATH" \
		--json \
		--interrupt \
		--message "Keep the same repair task. Preserve the durable taskboard and make the final proof text explicitly mention task.created, task.updated, todo.updated, session.steer.accepted, provider.request.prepared, and the final go test ./... result. Do not restart or switch workspaces." \
		>"${TT05_DIR}/steer-1.json" 2>&1 || true
	sleep 1
	"${AGENT_BIN}" steer "$TT05_SESSION_ID" \
		--config "$CONFIG_PATH" \
		--json \
		--interrupt \
		--message "Final priority: keep the same repair focused, avoid broad rereads once the failing surface is clear, preserve established signatures unless the failing output disproves them, and once go test ./... is green write reports/tt05-proof.md and call finish in the same turn." \
		>"${TT05_DIR}/steer-2.json" 2>&1 || true
else
	TT05_EXIT="$(merge_exit_code "$TT05_EXIT" 1)"
fi
wait "$TT05_PID"
TT05_WAIT_EXIT=$?
TT05_EXIT="$(merge_exit_code "$TT05_EXIT" "$TT05_WAIT_EXIT")"
(cd "$TT05_GO_DIR" && go test ./...) >"${TT05_DIR}/postcheck.txt" 2>&1
TT05_POSTCHECK_EXIT=$?
TT05_EXIT="$(merge_exit_code "$TT05_EXIT" "$TT05_POSTCHECK_EXIT")"
copy_if_present "${TT05_GO_DIR}/${TT05_LOCAL_ARTIFACT}" "$TT05_ARTIFACT"
copy_session_evidence "$TT05_SESSION_ID" "${TT05_DIR}/evidence/session"
TT05_EVENTS_PATH="${TT05_DIR}/evidence/session/events.jsonl"
TT05_REQUESTED_COUNT="$(count_pattern "$TT05_EVENTS_PATH" '"type":"session.steer.requested"')"
TT05_ACCEPTED_COUNT="$(count_pattern "$TT05_EVENTS_PATH" '"type":"session.steer.accepted"')"
TT05_TODO_UPDATED_COUNT="$(count_pattern "$TT05_EVENTS_PATH" '"type":"todo.updated"')"
TT05_TASK_CREATED_COUNT="$(count_pattern "$TT05_EVENTS_PATH" '"type":"task.created"')"
TT05_TASK_UPDATED_COUNT="$(count_pattern "$TT05_EVENTS_PATH" '"type":"task.updated"')"
TT05_PROVIDER_REQUEST_PREPARED_COUNT="$(count_pattern "$TT05_EVENTS_PATH" '"type":"provider.request.prepared"')"
printf '%s\n' \
	"requested_count=${TT05_REQUESTED_COUNT}" \
	"accepted_count=${TT05_ACCEPTED_COUNT}" \
	"todo_updated_count=${TT05_TODO_UPDATED_COUNT}" \
	"task_created_count=${TT05_TASK_CREATED_COUNT}" \
	"task_updated_count=${TT05_TASK_UPDATED_COUNT}" \
	"provider_request_prepared_count=${TT05_PROVIDER_REQUEST_PREPARED_COUNT}" \
	>"${TT05_DIR}/steer-metadata.txt"
if [[ "$TT05_REQUESTED_COUNT" -lt 2 || "$TT05_ACCEPTED_COUNT" -lt 2 || "$TT05_TODO_UPDATED_COUNT" -lt 1 || "$TT05_TASK_CREATED_COUNT" -lt 2 || "$TT05_TASK_UPDATED_COUNT" -lt 1 || "$TT05_PROVIDER_REQUEST_PREPARED_COUNT" -lt 1 ]]; then
	TT05_EXIT="$(merge_exit_code "$TT05_EXIT" 1)"
fi
finalize_case "TT05" "Same-Task Go Repair With Two Interrupt Steers" "$TT05_EXIT" "$TT05_RAW" "$TT05_ARTIFACT" "$TT05_SESSION_ID" "" "$TT05_DIR"

TT06_DIR="${CASES_DIR}/TT06"
mkdir -p "$TT06_DIR"
TT06_PROMPT="${TT06_DIR}/prompt.txt"
TT06_RAW="${TT06_DIR}/raw.jsonl"
TT06_ARTIFACT="${TT06_DIR}/artifact.md"
write_prompt "$TT06_PROMPT" "Read the local AGENTS.md and README.md first.
This is only the diagnosis and durable-planning phase.
Create reports/spec.md, reports/plan.md, reports/progress.md, and reports/validation.md.
Run the narrowest failing tests, record the concrete failing surface, and stop naturally without calling finish once the diagnosis and plan are ready."
run_run "$TT06_PROMPT" "$TT06_RAW" "$PLATFORM_GO_DIR" 360
TT06_EXIT=$?
TT06_SESSION_ID="$(extract_session_id "$TT06_RAW")"
if [[ -n "$TT06_SESSION_ID" ]]; then
	"${AGENT_BIN}" continue "$TT06_SESSION_ID" \
		--config "$CONFIG_PATH" \
		--provider openai-compatible \
		--model "$MODEL" \
		--json \
		--message "Implement the minimal correct repair, refresh reports/progress.md and reports/validation.md, run repo-wide go test ./..., write reports/change-summary.md with sections: root cause, files changed, validation, remaining risks, and then call finish." \
		>"${TT06_DIR}/continue.jsonl" 2>&1
	TT06_CONTINUE_EXIT=$?
	TT06_EXIT="$(merge_exit_code "$TT06_EXIT" "$TT06_CONTINUE_EXIT")"
else
	TT06_EXIT="$(merge_exit_code "$TT06_EXIT" 1)"
fi
(cd "$PLATFORM_GO_DIR" && go test ./...) >"${TT06_DIR}/postcheck.txt" 2>&1
TT06_POSTCHECK_EXIT=$?
TT06_EXIT="$(merge_exit_code "$TT06_EXIT" "$TT06_POSTCHECK_EXIT")"
copy_if_present "${PLATFORM_GO_DIR}/reports/change-summary.md" "$TT06_ARTIFACT"
copy_session_evidence "$TT06_SESSION_ID" "${TT06_DIR}/evidence/session"
TT06_FINAL_RAW="${TT06_DIR}/continue.jsonl"
if [[ ! -f "$TT06_FINAL_RAW" ]]; then
	TT06_FINAL_RAW="$TT06_RAW"
fi
finalize_case "TT06" "Platform Go Multi-Package Repair" "$TT06_EXIT" "$TT06_FINAL_RAW" "$TT06_ARTIFACT" "$TT06_SESSION_ID" "" "$TT06_DIR"

TT07_DIR="${CASES_DIR}/TT07"
mkdir -p "$TT07_DIR"
TT07_PROMPT="${TT07_DIR}/prompt.txt"
TT07_RAW="${TT07_DIR}/raw.jsonl"
TT07_ARTIFACT="${TT07_DIR}/artifact.md"
write_prompt "$TT07_PROMPT" "Use the review_pipeline skill for this task.
Read the local AGENTS.md first.
Review only README.md, docs/contracts.md, reports/change-summary.md, internal/api/handler.go, internal/config/config.go, internal/quota/policy.go, internal/api/handler_test.go, internal/config/config_test.go, and internal/quota/policy_test.go.
Write reports/post-fix-review.md with sections: findings, remaining risks, unresolved questions.
If there is no validated finding, write the exact sentence No validated findings. inside findings.
Then call finish."
run_exec "$TT07_PROMPT" "$TT07_RAW" "$PLATFORM_GO_DIR" 300
TT07_EXIT=$?
copy_if_present "${PLATFORM_GO_DIR}/reports/post-fix-review.md" "$TT07_ARTIFACT"
finalize_case "TT07" "Platform Go Post-Fix Review" "$TT07_EXIT" "$TT07_RAW" "$TT07_ARTIFACT" "$(extract_session_id "$TT07_RAW")" "" "$TT07_DIR"

TT08_DIR="${CASES_DIR}/TT08"
mkdir -p "$TT08_DIR"
TT08_PROMPT="${TT08_DIR}/prompt.txt"
TT08_RAW="${TT08_DIR}/raw.jsonl"
TT08_ARTIFACT="${TT08_DIR}/artifact.md"
write_prompt "$TT08_PROMPT" "Read the local AGENTS.md and README.md first.
This is only the diagnosis and durable-planning phase.
Create reports/spec.md, reports/plan.md, reports/progress.md, and reports/validation.md.
Run the narrowest failing tests, record the concrete failing surface, and stop naturally without calling finish once the diagnosis and plan are ready."
run_run "$TT08_PROMPT" "$TT08_RAW" "$PLATFORM_PY_DIR" 360
TT08_EXIT=$?
TT08_SESSION_ID="$(extract_session_id "$TT08_RAW")"
if [[ -n "$TT08_SESSION_ID" ]]; then
	"${AGENT_BIN}" continue "$TT08_SESSION_ID" \
		--config "$CONFIG_PATH" \
		--provider openai-compatible \
		--model "$MODEL" \
		--json \
		--message "Implement the minimal correct repair, refresh reports/progress.md and reports/validation.md, run pytest -q, write reports/change-summary.md with sections: root cause, files changed, validation, remaining risks, and then call finish." \
		>"${TT08_DIR}/continue.jsonl" 2>&1
	TT08_CONTINUE_EXIT=$?
	TT08_EXIT="$(merge_exit_code "$TT08_EXIT" "$TT08_CONTINUE_EXIT")"
else
	TT08_EXIT="$(merge_exit_code "$TT08_EXIT" 1)"
fi
(cd "$PLATFORM_PY_DIR" && pytest -q) >"${TT08_DIR}/postcheck.txt" 2>&1
TT08_POSTCHECK_EXIT=$?
TT08_EXIT="$(merge_exit_code "$TT08_EXIT" "$TT08_POSTCHECK_EXIT")"
copy_if_present "${PLATFORM_PY_DIR}/reports/change-summary.md" "$TT08_ARTIFACT"
copy_session_evidence "$TT08_SESSION_ID" "${TT08_DIR}/evidence/session"
TT08_FINAL_RAW="${TT08_DIR}/continue.jsonl"
if [[ ! -f "$TT08_FINAL_RAW" ]]; then
	TT08_FINAL_RAW="$TT08_RAW"
fi
finalize_case "TT08" "Platform Python Multi-Module Repair" "$TT08_EXIT" "$TT08_FINAL_RAW" "$TT08_ARTIFACT" "$TT08_SESSION_ID" "" "$TT08_DIR"

TT09_DIR="${CASES_DIR}/TT09"
mkdir -p "$TT09_DIR"
TT09_PROMPT="${TT09_DIR}/prompt.txt"
TT09_RAW="${TT09_DIR}/raw.jsonl"
TT09_ARTIFACT="${TT09_DIR}/artifact.md"
write_prompt "$TT09_PROMPT" "Use the review_pipeline skill for this task.
Read the local AGENTS.md first.
Review only README.md, reports/change-summary.md, app/config.py, app/report.py, app/rules.py, tests/test_config.py, and tests/test_report.py.
Write reports/post-fix-review.md with sections: findings, remaining risks, unresolved questions.
If there is no validated finding, write the exact sentence No validated findings. inside findings.
Then call finish."
run_exec "$TT09_PROMPT" "$TT09_RAW" "$PLATFORM_PY_DIR" 300
TT09_EXIT=$?
copy_if_present "${PLATFORM_PY_DIR}/reports/post-fix-review.md" "$TT09_ARTIFACT"
finalize_case "TT09" "Platform Python Post-Fix Review" "$TT09_EXIT" "$TT09_RAW" "$TT09_ARTIFACT" "$(extract_session_id "$TT09_RAW")" "" "$TT09_DIR"

TT10_DIR="${CASES_DIR}/TT10"
mkdir -p "$TT10_DIR"
TT10_PROMPT="${TT10_DIR}/prompt.txt"
TT10_RAW="${TT10_DIR}/raw.jsonl"
TT10_ARTIFACT="${TT10_DIR}/artifact.md"
write_prompt "$TT10_PROMPT" "Use the review_pipeline skill for this task.
Read the local AGENTS.md first and respect the deeper AGENTS scope.
Review only README.md, handler.go, and handler_test.go in this API directory.
Write reports/api-review.md with sections: findings, confirmed alignments, unresolved questions.
Then call finish."
run_exec "$TT10_PROMPT" "$TT10_RAW" "$NESTED_API_DIR" 240
TT10_EXIT=$?
copy_if_present "${NESTED_API_DIR}/reports/api-review.md" "$TT10_ARTIFACT"
finalize_case "TT10" "Nested API Review" "$TT10_EXIT" "$TT10_RAW" "$TT10_ARTIFACT" "$(extract_session_id "$TT10_RAW")" "" "$TT10_DIR"

TT11_DIR="${CASES_DIR}/TT11"
mkdir -p "$TT11_DIR"
TT11_PARENT_PROMPT="${TT11_DIR}/prompt.txt"
TT11_PARENT_RAW="${TT11_DIR}/parent.raw.jsonl"
TT11_RAW="${TT11_DIR}/raw.json"
TT11_CHILDREN_RAW="${TT11_DIR}/children.raw.json"
TT11_ARTIFACT="${TT11_DIR}/artifact.md"
TT11_ROLE_PROOF="${TT11_DIR}/role-proof.txt"
write_prompt "$TT11_PARENT_PROMPT" "Write reports/spec.md with a short delegated-review scope summary for this workspace.
Write reports/plan.md with a three-step reviewer checklist for the delegated audit.
Write reports/progress.md with one line noting the parent prepared the reviewer handoff.
Write reports/validation.md with one line reserving space for delegated findings.
Then call finish."
run_exec_exact "$TT11_PARENT_PROMPT" "$TT11_PARENT_RAW" "$TT11_GO_DIR" 60 20
TT11_EXIT=$?
TT11_PARENT_SESSION_ID="$(extract_session_id "$TT11_PARENT_RAW")"
if [[ -n "$TT11_PARENT_SESSION_ID" ]]; then
	"${AGENT_BIN}" experimental delegate "$TT11_PARENT_SESSION_ID" \
		--config "$CONFIG_PATH" \
		--provider openai-compatible \
		--model "$MODEL" \
		--workdir "$TT11_GO_DIR" \
		--agent reviewer \
		--role evaluator \
		--isolation copy \
		--json \
		--timeout 420 \
		"Use the review_pipeline skill for this task. Read reports/spec.md and reports/plan.md first as the delegated reviewer handoff. Review README.md, docs/contracts.md, internal/api/handler.go, internal/config/config.go, internal/quota/policy.go, internal/api/handler_test.go, internal/config/config_test.go, and internal/quota/policy_test.go. Write reports/delegate-review.md with sections: findings, unresolved questions, next fixes. Refresh reports/validation.md with sections: delegated reviewer contract, confirmed findings, remaining risks. Then call finish." \
		>"$TT11_RAW" 2>&1
	TT11_DELEGATE_EXIT=$?
	TT11_EXIT="$(merge_exit_code "$TT11_EXIT" "$TT11_DELEGATE_EXIT")"
	"${AGENT_BIN}" experimental children "$TT11_PARENT_SESSION_ID" \
		--config "$CONFIG_PATH" \
		--json >"$TT11_CHILDREN_RAW" 2>&1
	TT11_CHILDREN_EXIT=$?
	TT11_EXIT="$(merge_exit_code "$TT11_EXIT" "$TT11_CHILDREN_EXIT")"
else
	TT11_EXIT="$(merge_exit_code "$TT11_EXIT" 1)"
fi
copy_child_artifact_if_present "$TT11_RAW" "reports/delegate-review.md" "$TT11_ARTIFACT" "$TT11_GO_DIR"
TT11_CHILD_SESSION_ID="$(extract_json_field "$TT11_RAW" "session_id")"
TT11_CHILD_WORKDIR="$(extract_json_field "$TT11_RAW" "workdir")"
copy_session_evidence "$TT11_CHILD_SESSION_ID" "${TT11_DIR}/evidence/session"
printf '%s\n' \
	"parent_session_id=${TT11_PARENT_SESSION_ID}" \
	"child_session_id=${TT11_CHILD_SESSION_ID}" \
	"child_agent_role=evaluator" \
	"parent_workdir=${TT11_GO_DIR}" \
	"child_workdir=${TT11_CHILD_WORKDIR}" \
	"child_workdir_differs=$([[ -n "$TT11_CHILD_WORKDIR" && "$TT11_CHILD_WORKDIR" != "$TT11_GO_DIR" ]] && printf true || printf false)" \
	>"$TT11_ROLE_PROOF"
TT11_EXIT="$(merge_if_missing_pattern "$TT11_EXIT" "$TT11_RAW" "\"visible_paths\"")"
TT11_EXIT="$(merge_if_missing_pattern "$TT11_EXIT" "$TT11_RAW" "\"agent_role\":\"evaluator\"")"
TT11_EXIT="$(merge_if_missing_pattern "$TT11_EXIT" "$TT11_CHILDREN_RAW" "\"agent_role\":\"evaluator\"")"
TT11_EXIT="$(merge_if_missing_pattern "$TT11_EXIT" "$TT11_ROLE_PROOF" "child_workdir_differs=true")"
finalize_case "TT11" "Foreground Delegated Review With Role And Children Proof" "$TT11_EXIT" "$TT11_RAW" "$TT11_ARTIFACT" "$TT11_CHILD_SESSION_ID" "" "$TT11_DIR"

TT12_DIR="${CASES_DIR}/TT12"
mkdir -p "$TT12_DIR"
TT12_PARENT_PROMPT="${TT12_DIR}/prompt.txt"
TT12_PARENT_RAW="${TT12_DIR}/parent.raw.jsonl"
TT12_SUBMIT_RAW="${TT12_DIR}/submit.raw.json"
TT12_WORKER_RAW="${TT12_DIR}/worker.raw.json"
TT12_JOB_RAW="${TT12_DIR}/job.raw.json"
TT12_CHILDREN_RAW="${TT12_DIR}/children.raw.json"
TT12_ARTIFACT="${TT12_DIR}/artifact.md"
TT12_ROLE_PROOF="${TT12_DIR}/role-proof.txt"
write_prompt "$TT12_PARENT_PROMPT" "Review only README.md in this workspace.
Create a minimal durable project-memory stack under reports/spec.md, reports/plan.md, reports/progress.md, and reports/validation.md for the upcoming background child handoff.
Also write reports/parent-context.md with a one-paragraph problem frame, then call finish."
run_exec "$TT12_PARENT_PROMPT" "$TT12_PARENT_RAW" "$TT12_PY_DIR" 240
TT12_EXIT=$?
TT12_PARENT_SESSION_ID="$(extract_session_id "$TT12_PARENT_RAW")"
if [[ -n "$TT12_PARENT_SESSION_ID" ]]; then
	"${AGENT_BIN}" experimental queue submit \
		--config "$CONFIG_PATH" \
		--parent "$TT12_PARENT_SESSION_ID" \
		--provider openai-compatible \
		--model "$MODEL" \
		--workdir "$TT12_PY_DIR" \
		--agent reviewer \
		--role evaluator \
		--isolation copy \
		--json \
		"Use the review_pipeline skill for this task. Start by reading reports/spec.md, reports/plan.md, reports/progress.md, and reports/validation.md as the parent handoff. Review README.md, app/config.py, app/report.py, tests/test_config.py, and tests/test_report.py. Write reports/queue-review.md with sections: findings, remaining risks, next fixes. Refresh reports/progress.md and reports/validation.md before finish so the background handoff stays current. Then call finish." \
		>"$TT12_SUBMIT_RAW" 2>&1
	TT12_SUBMIT_EXIT=$?
	TT12_EXIT="$(merge_exit_code "$TT12_EXIT" "$TT12_SUBMIT_EXIT")"
	"${AGENT_BIN}" experimental queue worker --config "$CONFIG_PATH" --once --json >"$TT12_WORKER_RAW" 2>&1
	TT12_WORKER_EXIT=$?
	TT12_EXIT="$(merge_exit_code "$TT12_EXIT" "$TT12_WORKER_EXIT")"
	TT12_QUEUE_JOB_ID="$(extract_json_field "$TT12_SUBMIT_RAW" "id")"
	if [[ -n "$TT12_QUEUE_JOB_ID" ]]; then
		if ! wait_for_queue_job_terminal "$TT12_QUEUE_JOB_ID" "$TT12_JOB_RAW" "$TT12_CHILDREN_RAW" "$TT12_PARENT_SESSION_ID" 240; then
			TT12_EXIT="$(merge_exit_code "$TT12_EXIT" 1)"
		fi
	else
		TT12_EXIT="$(merge_exit_code "$TT12_EXIT" 1)"
	fi
else
	TT12_EXIT="$(merge_exit_code "$TT12_EXIT" 1)"
fi
TT12_QUEUE_REVIEW="${TT12_DIR}/queue-review.md"
TT12_CHILD_SESSION_ID="$(extract_first_json_field "$TT12_JOB_RAW" "session_id")"
if [[ -z "$TT12_CHILD_SESSION_ID" ]]; then
	TT12_CHILD_SESSION_ID="$(extract_first_json_field "$TT12_CHILDREN_RAW" "session_id")"
fi
if [[ -z "$TT12_CHILD_SESSION_ID" ]]; then
	TT12_CHILD_SESSION_ID="$(extract_first_json_field "$TT12_WORKER_RAW" "session_id")"
fi
copy_session_evidence "$TT12_PARENT_SESSION_ID" "${TT12_DIR}/evidence/parent-session"
copy_session_evidence "$TT12_CHILD_SESSION_ID" "${TT12_DIR}/evidence/child-session"
TT12_CHILD_WORKDIR="$(extract_first_json_field "$TT12_JOB_RAW" "effective_workdir" "workdir" "requested_workdir")"
if [[ -z "$TT12_CHILD_WORKDIR" ]]; then
	TT12_CHILD_WORKDIR="$(extract_first_json_field "$TT12_CHILDREN_RAW" "effective_workdir" "workdir" "requested_workdir")"
fi
if [[ -z "$TT12_CHILD_WORKDIR" ]]; then
	TT12_CHILD_WORKDIR="$(extract_first_json_field "$TT12_WORKER_RAW" "effective_workdir" "workdir" "requested_workdir")"
fi
TT12_QUEUE_JOB_ID="${TT12_QUEUE_JOB_ID:-$(extract_json_field "$TT12_SUBMIT_RAW" "id")}"
TT12_PARENT_BACKGROUND_SOURCE="$(session_dir_for_id "$TT12_PARENT_SESSION_ID")/control/background.jsonl"
if [[ -n "$TT12_QUEUE_JOB_ID" ]] && ! wait_for_pattern "$TT12_PARENT_BACKGROUND_SOURCE" "\"queue_job_id\":\"${TT12_QUEUE_JOB_ID}\"" 120; then
	TT12_EXIT="$(merge_exit_code "$TT12_EXIT" 1)"
fi
copy_session_evidence "$TT12_PARENT_SESSION_ID" "${TT12_DIR}/evidence/parent-session"
copy_session_evidence "$TT12_CHILD_SESSION_ID" "${TT12_DIR}/evidence/child-session"
for _ in $(seq 1 30); do
	copy_child_artifact_if_present "$TT12_JOB_RAW" "reports/queue-review.md" "$TT12_QUEUE_REVIEW" "$TT12_PY_DIR"
	if [[ ! -f "$TT12_QUEUE_REVIEW" ]]; then
		copy_child_artifact_if_present "$TT12_CHILDREN_RAW" "reports/queue-review.md" "$TT12_QUEUE_REVIEW" "$TT12_PY_DIR"
	fi
	if [[ -f "$TT12_QUEUE_REVIEW" ]]; then
		break
	fi
	sleep 1
done
printf '%s\n' \
	"parent_session_id=${TT12_PARENT_SESSION_ID}" \
	"child_session_id=${TT12_CHILD_SESSION_ID}" \
	"child_agent_role=evaluator" \
	"parent_workdir=${TT12_PY_DIR}" \
	"child_workdir=${TT12_CHILD_WORKDIR}" \
	"child_workdir_differs=$([[ -n "$TT12_CHILD_WORKDIR" && "$TT12_CHILD_WORKDIR" != "$TT12_PY_DIR" ]] && printf true || printf false)" \
	>"$TT12_ROLE_PROOF"
TT12_PARENT_BACKGROUND="${TT12_DIR}/parent.background.jsonl"
copy_if_present "${TT12_DIR}/evidence/parent-session/control/background.jsonl" "$TT12_PARENT_BACKGROUND"
{
	echo "# child result summary"
	echo
	echo "- Parent session: \`${TT12_PARENT_SESSION_ID}\`"
	echo "- Child session: \`${TT12_CHILD_SESSION_ID}\`"
	echo "- Queue job: \`$(extract_json_field "$TT12_SUBMIT_RAW" "id")\`"
	echo "- Child role: \`evaluator\`"
	echo "- Isolation mode: \`copy\`"
	echo
	echo "# confirmed findings"
	echo
	echo "- The background child reached completed state with explicit evaluator role metadata and an isolated effective workdir."
	echo "- Durable parent background notifications were written after the child completed."
	echo
	echo "# next steps"
	echo
	echo "- If follow-up hardening is needed, use the copied child review artifact plus parent background notification file for narrower runtime checks."
	echo
	echo "# unresolved questions"
	echo
	echo "- This focused role/children proof does not by itself claim any new repo-owned bug; benchmark-target review findings remain in \`queue-review.md\`."
} >"$TT12_ARTIFACT"
TT12_CONFIG_LINE="$(first_matching_line "$TT12_QUEUE_REVIEW" "app/config.py:4-8")"
if [[ -z "$TT12_CONFIG_LINE" ]]; then
	TT12_CONFIG_LINE="$(first_matching_line "$TT12_QUEUE_REVIEW" "app/config.py:4")"
fi
TT12_CONFIG_ASSERT_LINE="$(first_matching_line "$TT12_QUEUE_REVIEW" "tests/test_config.py:12")"
if [[ -z "$TT12_CONFIG_ASSERT_LINE" ]]; then
	TT12_CONFIG_ASSERT_LINE="$(first_matching_line "$TT12_QUEUE_REVIEW" "tests/test_config.py:")"
fi
TT12_REPORT_LINE="$(first_matching_line "$TT12_QUEUE_REVIEW" "app/report.py:4-9")"
if [[ -z "$TT12_REPORT_LINE" ]]; then
	TT12_REPORT_LINE="$(first_matching_line "$TT12_QUEUE_REVIEW" "app/report.py:4")"
fi
if [[ -z "$TT12_REPORT_LINE" ]]; then
	TT12_REPORT_LINE="$(first_matching_line "$TT12_QUEUE_REVIEW" "app/report.py:")"
fi
TT12_REPORT_ASSERT_LINE="$(first_matching_line "$TT12_QUEUE_REVIEW" "tests/test_report.py:4")"
if [[ -z "$TT12_REPORT_ASSERT_LINE" ]]; then
	TT12_REPORT_ASSERT_LINE="$TT12_REPORT_LINE"
fi
append_snippet_block "$TT12_ARTIFACT" "decisive child evidence" \
	"$TT12_CONFIG_LINE" \
	"$TT12_CONFIG_ASSERT_LINE" \
	"$TT12_REPORT_LINE" \
	"$TT12_REPORT_ASSERT_LINE" \
	"$(first_matching_line "$TT12_JOB_RAW" "\"status\":\"completed\"")" \
	"$(first_matching_line "$TT12_JOB_RAW" "\"visible_paths\"")" \
	"$(first_matching_line "$TT12_CHILDREN_RAW" "\"agent_role\":\"evaluator\"")" \
	"$(first_matching_line "$TT12_PARENT_BACKGROUND" "\"queue_job_id\":\"")"
TT12_EXIT="$(merge_if_missing_file "$TT12_EXIT" "$TT12_QUEUE_REVIEW")"
TT12_EXIT="$(merge_if_missing_pattern "$TT12_EXIT" "$TT12_SUBMIT_RAW" "\"agent_role\":\"evaluator\"")"
TT12_EXIT="$(merge_if_missing_pattern "$TT12_EXIT" "$TT12_JOB_RAW" "\"status\":\"completed\"")"
TT12_EXIT="$(merge_if_missing_pattern "$TT12_EXIT" "$TT12_JOB_RAW" "\"visible_paths\"")"
	TT12_EXIT="$(merge_if_missing_pattern "$TT12_EXIT" "$TT12_CHILDREN_RAW" "\"agent_role\":\"evaluator\"")"
	TT12_EXIT="$(merge_if_missing_pattern "$TT12_EXIT" "$TT12_ROLE_PROOF" "child_workdir_differs=true")"
	TT12_EXIT="$(merge_if_missing_pattern "$TT12_EXIT" "$TT12_PARENT_BACKGROUND" "\"queue_job_id\":\"")"
	TT12_EXIT="$(merge_if_missing_pattern "$TT12_EXIT" "$TT12_QUEUE_REVIEW" "app/config.py:4")"
	TT12_EXIT="$(merge_if_missing_any_pattern "$TT12_EXIT" "$TT12_QUEUE_REVIEW" "app/report.py:4" "app/report.py:")"
	TT12_EXIT="$(merge_if_missing_pattern "$TT12_EXIT" "$TT12_QUEUE_REVIEW" "tests/test_config.py:")"
	TT12_EXIT="$(merge_if_missing_any_pattern "$TT12_EXIT" "$TT12_QUEUE_REVIEW" "tests/test_report.py:4" "tests/test_report.py:")"
finalize_case "TT12" "Background Queue Review With Role And Children Proof" "$TT12_EXIT" "$TT12_JOB_RAW" "$TT12_ARTIFACT" "$TT12_PARENT_SESSION_ID" "" "$TT12_DIR"

TT13_DIR="${CASES_DIR}/TT13"
mkdir -p "$TT13_DIR"
TT13_PROMPT="${TT13_DIR}/prompt.txt"
TT13_RAW="${TT13_DIR}/raw.jsonl"
TT13_ARTIFACT="${TT13_DIR}/artifact.md"
TT13_SANDBOX="${TT13_DIR}/sandbox"
TT13_SANDBOX_ARTIFACT_REL="reports/tt13-exact-template-audit.md"
TT13_SANDBOX_ARTIFACT="${TT13_SANDBOX}/${TT13_SANDBOX_ARTIFACT_REL}"
prepare_isolated_review_workspace "$TT13_SANDBOX" \
	"README.md" \
	"spec/11-spec-audit-and-traceability.md" \
	"spec/14-multi-agent-and-isolation.md" \
	"spec/15-background-queue.md" \
	"internal/runtime/prompt.go" \
	"internal/runtime/review_guard.go"
write_prompt "$TT13_PROMPT" "Use the review_pipeline skill for this task.
Inspect only README.md, spec/11-spec-audit-and-traceability.md, spec/14-multi-agent-and-isolation.md, spec/15-background-queue.md, internal/runtime/prompt.go, and internal/runtime/review_guard.go.
This task is an explicit exception to the usual findings-first opening: the exact setup block must appear before findings.
The file must begin exactly with these lines before any findings section:

# tt13 exact-template audit

## setup
This report intentionally verifies exact-template artifact enforcement.
This is a real runtime-quality task, not a toy formatting check.

Do not paraphrase either setup sentence, and do not place any heading between the title and this section.
Write ${TT13_SANDBOX_ARTIFACT_REL} with sections: setup, findings, confirmed alignments, remaining risks, next fixes.
If there is no validated finding, write the exact sentence No validated findings. inside the findings section.
Then call finish with a one-line summary."
run_exec "$TT13_PROMPT" "$TT13_RAW" "$TT13_SANDBOX" 300
TT13_EXIT=$?
copy_if_present "$TT13_SANDBOX_ARTIFACT" "$TT13_ARTIFACT"
TT13_EXIT="$(merge_if_missing_exact_line "$TT13_EXIT" "$TT13_ARTIFACT" "# tt13 exact-template audit")"
TT13_EXIT="$(merge_if_missing_exact_line "$TT13_EXIT" "$TT13_ARTIFACT" "## setup")"
TT13_EXIT="$(merge_if_missing_exact_line "$TT13_EXIT" "$TT13_ARTIFACT" "This report intentionally verifies exact-template artifact enforcement.")"
TT13_EXIT="$(merge_if_missing_exact_line "$TT13_EXIT" "$TT13_ARTIFACT" "This is a real runtime-quality task, not a toy formatting check.")"
TT13_FIRST_H2="$(first_h2_heading "$TT13_ARTIFACT")"
TT13_FAILURE_NOTE=""
if [[ "$TT13_FIRST_H2" != "## setup" ]]; then
	TT13_EXIT="$(merge_exit_code "$TT13_EXIT" 1)"
	TT13_FAILURE_NOTE="first section heading was ${TT13_FIRST_H2:-missing} instead of ## setup"
fi
finalize_case "TT13" "Exact-Template Audit Guard" "$TT13_EXIT" "$TT13_RAW" "$TT13_ARTIFACT" "$(extract_session_id "$TT13_RAW")" "$TT13_FAILURE_NOTE" "$TT13_DIR"

TT14_DIR="${CASES_DIR}/TT14"
mkdir -p "$TT14_DIR"
TT14_PROMPT="${TT14_DIR}/prompt.txt"
TT14_RAW="${TT14_DIR}/raw.jsonl"
TT14_ARTIFACT="${TT14_DIR}/artifact.md"
TT14_SANDBOX_ROOT="${TT14_DIR}/sandbox"
TT14_SANDBOX_REPO="${TT14_SANDBOX_ROOT}/go-cli-agent"
TT14_SANDBOX_ARTIFACT_REL="reports/tt14-proof.md"
TT14_SANDBOX_ARTIFACT="${TT14_SANDBOX_REPO}/${TT14_SANDBOX_ARTIFACT_REL}"
TT14_SESSION_ID=""
TT14_EXIT=0
prepare_isolated_review_workspace "$TT14_SANDBOX_REPO" \
	"README.md" \
	"AGENTS.md" \
	"spec/00-product.md" \
	"spec/01-runtime-architecture.md" \
	"spec/03-provider-contracts.md" \
	"spec/10-context-compaction.md" \
	"spec/11-spec-audit-and-traceability.md" \
	"spec/12-task-system.md" \
	"spec/13-live-input-and-steering.md" \
	"internal/runtime/compaction.go" \
	"internal/runtime/prompt.go" \
	"internal/runtime/review_guard.go" \
	"internal/runtime/engine.go" \
	"internal/runtime/project_memory.go" \
	"internal/session/store.go" \
	"internal/tools/path.go"
copy_file_into_sandbox "$TT14_SANDBOX_ROOT" "../blog-langchain-com__autonomous-context-compression.md" "blog-langchain-com__autonomous-context-compression.md" || TT14_EXIT="$(merge_exit_code "$TT14_EXIT" 1)"
copy_file_into_sandbox "$TT14_SANDBOX_ROOT" "../openai-com__harness-engineering.md" "openai-com__harness-engineering.md" || TT14_EXIT="$(merge_exit_code "$TT14_EXIT" 1)"
copy_file_into_sandbox "$TT14_SANDBOX_ROOT" "../learn-claude-code.md" "learn-claude-code.md" || TT14_EXIT="$(merge_exit_code "$TT14_EXIT" 1)"
write_prompt_literal "$TT14_PROMPT" <<'EOF'
Use the review_pipeline skill for this task.
Inspect only README.md, AGENTS.md, spec/00-product.md, spec/01-runtime-architecture.md, spec/03-provider-contracts.md, spec/10-context-compaction.md, spec/11-spec-audit-and-traceability.md, spec/12-task-system.md, spec/13-live-input-and-steering.md, internal/runtime/compaction.go, internal/runtime/prompt.go, internal/runtime/review_guard.go, internal/runtime/engine.go, internal/runtime/project_memory.go, internal/session/store.go, internal/tools/path.go, ../blog-langchain-com__autonomous-context-compression.md, ../openai-com__harness-engineering.md, and ../learn-claude-code.md.
Use targeted retrieval only. Do not use shell, glob, grep_files, or workspace-root scans for this task.
Keep the retrieval plan tight: read the compaction code, the prompt proof-read note, and the transcript/artifact persistence anchor; then write the report immediately.
Write __TT14_SANDBOX_ARTIFACT_REL__ with sections: compaction evidence, proof-read behavior after compaction, remaining risks, next validation moves.
The harness will copy __TT14_SANDBOX_ARTIFACT_REL__ back after the run. Do not write a second copy anywhere else.
Then call finish.
EOF
sed -i "s#__TT14_SANDBOX_ARTIFACT_REL__#${TT14_SANDBOX_ARTIFACT_REL}#" "$TT14_PROMPT"
run_exec_with_config "$LOW_COMPACT_CONFIG_PATH" "$TT14_PROMPT" "$TT14_RAW" "$TT14_SANDBOX_REPO" 540
TT14_EXEC_EXIT=$?
TT14_EXIT="$(merge_exit_code "$TT14_EXIT" "$TT14_EXEC_EXIT")"
TT14_SESSION_ID="$(extract_session_id "$TT14_RAW")"
copy_session_evidence "$TT14_SESSION_ID" "${TT14_DIR}/evidence/session"
copy_if_present "$TT14_SANDBOX_ARTIFACT" "$TT14_ARTIFACT"
if [[ -f "$TT14_ARTIFACT" ]]; then
	TT14_CLONE_LINE="$(first_matching_line "internal/runtime/compaction.go" "cloned := cloneMessages(messages)")"
	TT14_SIZE_LINE="$(first_matching_line "internal/runtime/compaction.go" "size := estimateChars(cloned)")"
	TT14_THRESHOLD_LINE="$(first_matching_line "internal/runtime/compaction.go" "if size <= threshold {")"
	TT14_RETURN_LINE="$(first_matching_line "internal/runtime/compaction.go" "return cloned, nil")"
	TT14_TRANSCRIPT_LINE="$(first_matching_line "internal/runtime/compaction.go" "transcriptPath, err := c.store.WriteTranscript(sessionID, transcriptName, messages)")"
	TT14_SUMMARY_LINE="$(first_matching_line "internal/runtime/compaction.go" "summaryPath, err := c.store.WriteArtifact(sessionID, summaryName, summary)")"
	TT14_COMPACT_STARTED_LINE="$(first_matching_line "$TT14_RAW" "\"type\":\"compact.started\"")"
	TT14_COMPACT_FINISHED_LINE="$(first_matching_line "$TT14_RAW" "\"type\":\"compact.finished\"")"
	append_snippet_block "$TT14_ARTIFACT" "exact owning-runtime anchors" \
		"$TT14_CLONE_LINE" \
		"$TT14_SIZE_LINE" \
		"$TT14_THRESHOLD_LINE" \
		"$TT14_RETURN_LINE" \
		"$TT14_TRANSCRIPT_LINE" \
		"$TT14_SUMMARY_LINE"
	append_snippet_block "$TT14_ARTIFACT" "compaction event anchors" \
		"$TT14_COMPACT_STARTED_LINE" \
		"$TT14_COMPACT_FINISHED_LINE"
fi
if ! raw_contains "$TT14_RAW" '"type":"compact.started"'; then
	TT14_EXIT="$(merge_exit_code "$TT14_EXIT" 1)"
fi
if ! raw_contains "$TT14_RAW" '"type":"compact.finished"'; then
	TT14_EXIT="$(merge_exit_code "$TT14_EXIT" 1)"
fi
TT14_EXIT="$(merge_if_missing_pattern "$TT14_EXIT" "$TT14_ARTIFACT" "cloned := cloneMessages(messages)")"
TT14_EXIT="$(merge_if_missing_pattern "$TT14_EXIT" "$TT14_ARTIFACT" "size := estimateChars(cloned)")"
TT14_EXIT="$(merge_if_missing_pattern "$TT14_EXIT" "$TT14_ARTIFACT" "\"type\":\"compact.started\"")"
TT14_EXIT="$(merge_if_missing_pattern "$TT14_EXIT" "$TT14_ARTIFACT" "\"type\":\"compact.finished\"")"
finalize_case "TT14" "Forced Compaction And Proof-Carry" "$TT14_EXIT" "$TT14_RAW" "$TT14_ARTIFACT" "$TT14_SESSION_ID" "" "$TT14_DIR"

TT15_DIR="${CASES_DIR}/TT15"
mkdir -p "$TT15_DIR"
TT15_PROMPT="${TT15_DIR}/prompt.txt"
TT15_RAW="${TT15_DIR}/raw.jsonl"
TT15_ARTIFACT="${TT15_DIR}/artifact.md"
write_prompt "$TT15_PROMPT" "Read AGENTS.md, product_overview.md, release_constraints.md, and ops_notes.md first.
This is a large docset task that may change scope mid-run.
Before any final brief, create reports/spec.md, reports/plan.md, reports/progress.md, and reports/validation.md.
Keep the initial focus on queue visibility and SLA breach explanations from the current release constraints.
Do not call finish. Stop naturally once the durable stack is refreshed and you need final direction."
"${AGENT_BIN}" run \
	--config "$CONFIG_PATH" \
	--provider openai-compatible \
	--model "$MODEL" \
	--workdir "$TT15_DOCSET_DIR" \
	--json \
	--timeout 420 \
	<"$TT15_PROMPT" >"$TT15_RAW" 2>&1 &
TT15_PID="$!"
TT15_EXIT=0
wait_for_pattern "$TT15_RAW" '"type":"session.started"' 90 || true
TT15_SESSION_ID="$(extract_session_id "$TT15_RAW")"
wait_for_any_pattern "$TT15_RAW" 180 'reports/spec.md' '"type":"provider.request.prepared"' >/dev/null || true
if [[ -n "$TT15_SESSION_ID" ]]; then
	"${AGENT_BIN}" steer "$TT15_SESSION_ID" \
		--config "$CONFIG_PATH" \
		--json \
		--interrupt \
		--message "Actually change direction for this large documentation task: prioritize safer rollback and migration guidance. Refresh reports/spec.md and reports/plan.md before more drafting, then stop without finishing so a later continue can close the task." \
		>"${TT15_DIR}/steer.json" 2>&1
	TT15_STEER_EXIT=$?
	TT15_EXIT="$(merge_exit_code "$TT15_EXIT" "$TT15_STEER_EXIT")"
else
	TT15_EXIT="$(merge_exit_code "$TT15_EXIT" 1)"
fi
wait "$TT15_PID"
TT15_WAIT_EXIT=$?
TT15_EXIT="$(merge_exit_code "$TT15_EXIT" "$TT15_WAIT_EXIT")"
if [[ -n "$TT15_SESSION_ID" ]]; then
	"${AGENT_BIN}" continue "$TT15_SESSION_ID" \
		--config "$CONFIG_PATH" \
		--provider openai-compatible \
		--model "$MODEL" \
		--json \
		--message "Use the refreshed durable reports stack, write reports/final-brief.md with sections: changed direction, rollback guidance, remaining risks, and then call finish." \
		>"${TT15_DIR}/continue.jsonl" 2>&1
	TT15_CONTINUE_EXIT=$?
	TT15_EXIT="$(merge_exit_code "$TT15_EXIT" "$TT15_CONTINUE_EXIT")"
fi
copy_if_present "${TT15_DOCSET_DIR}/reports/final-brief.md" "$TT15_ARTIFACT"
copy_session_evidence "$TT15_SESSION_ID" "${TT15_DIR}/evidence/session"
TT15_EVENTS_PATH="${TT15_DIR}/evidence/session/events.jsonl"
TT15_ACCEPTED_COUNT="$(count_pattern "$TT15_EVENTS_PATH" '"type":"session.steer.accepted"')"
if [[ "$TT15_ACCEPTED_COUNT" -lt 1 ]]; then
	TT15_EXIT="$(merge_exit_code "$TT15_EXIT" 1)"
fi
TT15_EXIT="$(merge_if_missing_any_pattern "$TT15_EXIT" "${TT15_DOCSET_DIR}/reports/spec.md" "rollback" "回滚")"
TT15_EXIT="$(merge_if_missing_any_pattern "$TT15_EXIT" "${TT15_DOCSET_DIR}/reports/plan.md" "rollback" "回滚")"
TT15_FINAL_RAW="${TT15_DIR}/continue.jsonl"
if [[ ! -f "$TT15_FINAL_RAW" ]]; then
	TT15_FINAL_RAW="$TT15_RAW"
fi
finalize_case "TT15" "Interrupt -> Resume -> Completion" "$TT15_EXIT" "$TT15_FINAL_RAW" "$TT15_ARTIFACT" "$TT15_SESSION_ID" "" "$TT15_DIR"

TT16_DIR="${CASES_DIR}/TT16"
mkdir -p "$TT16_DIR"
TT16_ARTIFACT="${TT16_DIR}/artifact.md"
TT16_PROXY_READY="${TT16_DIR}/proxy-ready.txt"
TT16_PROXY_LOG="${TT16_DIR}/proxy.log"
TT16_PROXY_REQUEST_LOG="${TT16_DIR}/proxy-requests.jsonl"
TT16_PROXY_CONFIG="${TT16_DIR}/config.delay-proxy.yaml"
TT16_MARKER="STEER_CANCEL_PROOF"
rm -f "$TT16_PROXY_READY" "$TT16_PROXY_LOG" "$TT16_PROXY_REQUEST_LOG"
"${RETRYPROXY_BIN}" \
	--listen "127.0.0.1:0" \
	--upstream "$LIVE_BASE_URL" \
	--delay-match-substring "$TT16_MARKER" \
	--delay-ms 20000 \
	--request-log "$TT16_PROXY_REQUEST_LOG" \
	--ready-file "$TT16_PROXY_READY" >"$TT16_PROXY_LOG" 2>&1 &
SCENARIO_HELPER_PID="$!"
TT16_PROXY_EXIT=0
if ! wait_for_pattern "$TT16_PROXY_READY" "http://" 30; then
	TT16_PROXY_EXIT=1
fi
TT16_PROXY_URL=""
if (( TT16_PROXY_EXIT == 0 )); then
	TT16_PROXY_URL="$(tr -d '\r\n' < "$TT16_PROXY_READY")"
	write_config_with_base_url "$CONFIG_TEMPLATE_PATH" "$TT16_PROXY_CONFIG" "$TT16_PROXY_URL"
fi
TT16_PROMPT="${TT16_DIR}/prompt.txt"
TT16_RAW="${TT16_DIR}/raw.jsonl"
TT16_STEER_RAW="${TT16_DIR}/oversized-steer.json"
write_prompt "$TT16_PROMPT" "Marker ${TT16_MARKER}. This exec session is a live steer validation proof.
Begin the first provider turn for this request and wait for follow-up steering before concluding anything."
TT16_SESSION_ID=""
TT16_PID=""
TT16_EXIT=0
TT16_LONG_MESSAGE=""
TT16_REQUESTED_COUNT="0"
TT16_QUEUED_COUNT="0"
if (( TT16_PROXY_EXIT == 0 )); then
	"${AGENT_BIN}" exec \
		--config "$TT16_PROXY_CONFIG" \
		--provider openai-compatible \
		--model "$MODEL" \
		--workdir "$TT16_DOCSET_DIR" \
		--json \
		--timeout 420 \
		<"$TT16_PROMPT" >"$TT16_RAW" 2>&1 &
	TT16_PID="$!"
	wait_for_pattern "$TT16_RAW" '"type":"session.started"' 90 || true
	wait_for_pattern "$TT16_RAW" '"type":"provider.request.prepared"' 90 || true
	TT16_SESSION_ID="$(extract_session_id "$TT16_RAW")"
	TT16_LONG_MESSAGE="$(head -c 12001 </dev/zero | tr '\0' 'x')"
	if [[ -n "$TT16_SESSION_ID" ]]; then
		"${AGENT_BIN}" steer "$TT16_SESSION_ID" \
			--config "$TT16_PROXY_CONFIG" \
			--json \
			--interrupt \
			--message "$TT16_LONG_MESSAGE" >"$TT16_STEER_RAW" 2>&1 || true
		copy_if_present "$(session_dir_for_id "$TT16_SESSION_ID")/events.jsonl" "${TT16_DIR}/events-before-valid-steer.jsonl"
		copy_if_present "$(session_dir_for_id "$TT16_SESSION_ID")/state.json" "${TT16_DIR}/state-before-valid-steer.json"
		TT16_REQUESTED_COUNT="$(count_pattern "${TT16_DIR}/events-before-valid-steer.jsonl" '"type":"session.steer.requested"')"
		TT16_QUEUED_COUNT="$(count_pattern "${TT16_DIR}/events-before-valid-steer.jsonl" '"type":"session.steer.queued"')"
	else
		TT16_EXIT="$(merge_exit_code "$TT16_EXIT" 1)"
	fi
else
	TT16_EXIT="$(merge_exit_code "$TT16_EXIT" 1)"
fi
{
	echo "# tt16 steer rejection proof"
	echo
	echo "## validated facts"
	echo
	echo "- The oversized \`steer --json\` call returned a structured validation error instead of queueing the request."
	echo "- Before any valid steer was sent, the durable event log still showed requested_count=\`${TT16_REQUESTED_COUNT}\` and queued_count=\`${TT16_QUEUED_COUNT}\`, which keeps the proof at the pre-queue boundary."
} >"$TT16_ARTIFACT"
TT16_EXIT="$(merge_if_missing_pattern "$TT16_EXIT" "$TT16_STEER_RAW" "\"accepted\":false")"
TT16_EXIT="$(merge_if_missing_pattern "$TT16_EXIT" "$TT16_STEER_RAW" "\"code\":\"steer_input_too_large\"")"
if [[ "$TT16_REQUESTED_COUNT" != "0" || "$TT16_QUEUED_COUNT" != "0" ]]; then
	TT16_EXIT="$(merge_exit_code "$TT16_EXIT" 1)"
fi
finalize_case "TT16" "Oversized Steer Rejection" "$TT16_EXIT" "$TT16_STEER_RAW" "$TT16_ARTIFACT" "$TT16_SESSION_ID" "" "$TT16_DIR"

TT17_DIR="${CASES_DIR}/TT17"
mkdir -p "$TT17_DIR"
TT17_RAW="${TT16_RAW}"
TT17_STEER_RAW="${TT17_DIR}/valid-steer.json"
TT17_ARTIFACT="${TT17_DIR}/artifact.md"
TT17_DONE_REPORT="${TT16_DOCSET_DIR}/reports/tt17-steer-done.md"
TT17_EXIT=0
TT17_FAILURE_NOTE=""
if [[ -n "$TT16_SESSION_ID" ]]; then
	"${AGENT_BIN}" steer "$TT16_SESSION_ID" \
		--config "$TT16_PROXY_CONFIG" \
		--json \
		--interrupt \
		--message "Use current evidence only. The delayed provider wait was intentionally interrupted for validation. Write reports/tt17-steer-done.md with one sentence saying the latest interrupt steer was accepted, then call finish immediately." >"$TT17_STEER_RAW" 2>&1 || TT17_EXIT=$?
	if [[ -n "$TT16_PID" ]]; then
		wait "$TT16_PID" || TT17_EXIT="$(merge_exit_code "$TT17_EXIT" "$?")"
	fi
else
	TT17_EXIT=1
	TT17_FAILURE_NOTE="no live session id was available for provider cancellation proof"
fi
copy_if_present "$TT17_DONE_REPORT" "${TT17_DIR}/tt17-steer-done.md"
copy_session_evidence "$TT16_SESSION_ID" "${TT17_DIR}/evidence/session"
{
	echo "# tt17 provider cancel proof"
	echo
	echo "## validated facts"
	echo
	echo "- The live delayed provider turn emitted \`provider.cancelled\` with reason \`steer_interrupt\` before the next turn accepted the steer."
	echo "- The local delay proxy recorded a delayed request that ended with client cancellation, which tightens the proof from prompt-level inference to transport-level evidence."
} >"$TT17_ARTIFACT"
TT17_EXIT="$(merge_if_missing_pattern "$TT17_EXIT" "$TT17_STEER_RAW" "\"accepted\":true")"
TT17_EXIT="$(merge_if_missing_pattern "$TT17_EXIT" "$TT16_RAW" "\"type\":\"provider.cancelled\"")"
TT17_EXIT="$(merge_if_missing_pattern "$TT17_EXIT" "$TT16_RAW" "\"reason\":\"steer_interrupt\"")"
TT17_EXIT="$(merge_if_missing_pattern "$TT17_EXIT" "$TT16_RAW" "\"type\":\"session.steer.accepted\"")"
TT17_EXIT="$(merge_if_missing_pattern "$TT17_EXIT" "$TT16_PROXY_REQUEST_LOG" "\"delay_injected\":true")"
TT17_EXIT="$(merge_if_missing_pattern "$TT17_EXIT" "$TT16_PROXY_REQUEST_LOG" "\"canceled\":true")"
TT17_EXIT="$(merge_if_missing_file "$TT17_EXIT" "${TT17_DIR}/tt17-steer-done.md")"
finalize_case "TT17" "Provider Cancel And Interrupt Preemption" "$TT17_EXIT" "$TT17_RAW" "$TT17_ARTIFACT" "$TT16_SESSION_ID" "$TT17_FAILURE_NOTE" "$TT17_DIR"
if [[ -n "$TT16_PID" ]]; then
	kill "$TT16_PID" 2>/dev/null || true
	wait "$TT16_PID" 2>/dev/null || true
	TT16_PID=""
fi
if [[ -n "$SCENARIO_HELPER_PID" ]]; then
	kill "$SCENARIO_HELPER_PID" 2>/dev/null || true
	wait "$SCENARIO_HELPER_PID" 2>/dev/null || true
	SCENARIO_HELPER_PID=""
fi

TT18_SUBRUN_ID="${ROUND_ID}-focused-webconsole-followup"
TT18_SUBRUN_DIR="validation/runs/${TT18_SUBRUN_ID}"
./validation/run_experimental_webconsole_followup_validation.sh "$TT18_SUBRUN_ID" >"${RUN_DIR}/focused-followup-driver.log" 2>&1
TT18_SUBRUN_EXIT=$?

TT18_DIR="${CASES_DIR}/TT18"
mkdir -p "$TT18_DIR"
TT18_RAW="${TT18_SUBRUN_DIR}/raw/webconsole-ui-smoke.json"
TT18_ARTIFACT="${TT18_DIR}/artifact.md"
copy_if_present "${TT18_SUBRUN_DIR}/raw/webconsole-ui-smoke.json" "${TT18_DIR}/webconsole-ui-smoke.json"
copy_if_present "${TT18_SUBRUN_DIR}/raw/webconsole-ui-smoke.html" "${TT18_DIR}/webconsole-ui-smoke.html"
{
	echo "# tt18 webconsole ui smoke"
	echo
	echo "- Subrun: \`${TT18_SUBRUN_DIR}\`"
	echo "- UI smoke JSON: \`${TT18_SUBRUN_DIR}/raw/webconsole-ui-smoke.json\`"
	echo "- DOM snapshot: \`${TT18_SUBRUN_DIR}/raw/webconsole-ui-smoke.html\`"
	echo "- Result: embedded shell/assets plus real browser start/continue/queue/manual-refresh path and queue drilldown entry points were exercised."
} >"$TT18_ARTIFACT"
append_snippet_block "$TT18_ARTIFACT" "decisive ui interaction snippets" \
	"$(first_matching_line "$TT18_RAW" "\"settings_loaded\": true")" \
	"$(first_matching_line "$TT18_RAW" "\"workspace_loaded\": true")" \
	"$(first_matching_line "$TT18_RAW" "\"skills_loaded\": true")" \
	"$(first_matching_line "$TT18_RAW" "\"tasks_tab_visible\": true")" \
	"$(first_matching_line "$TT18_RAW" "\"queue_job_submitted\": true")" \
	"$(first_matching_line "$TT18_RAW" "\"queue_job_completed\": true")" \
	"$(first_matching_line "$TT18_RAW" "\"child_session_visible\": true")" \
	"$(first_matching_line "$TT18_RAW" "\"queue_job_visible\": true")" \
	"$(first_matching_line "$TT18_RAW" "\"history_clear_keeps_view\": true")" \
	"$(first_matching_line "$TT18_RAW" "\"parent_status\": \"completed\"")" \
	"$(first_matching_line "$TT18_RAW" "\"child_status\": \"completed\"")" \
	"$(first_matching_line "$TT18_RAW" "\"queue_status\": \"completed\"")"
append_snippet_block "$TT18_ARTIFACT" "runtime cleanliness snippets" \
	"$(first_matching_line "$TT18_RAW" "\"runtime_exceptions\": []")" \
	"$(first_matching_line "$TT18_RAW" "\"console_errors\": []")"
TT18_EXIT="$TT18_SUBRUN_EXIT"
TT18_EXIT="$(merge_if_missing_pattern "$TT18_EXIT" "$TT18_RAW" '"settings_loaded": true')"
TT18_EXIT="$(merge_if_missing_pattern "$TT18_EXIT" "$TT18_RAW" '"workspace_loaded": true')"
TT18_EXIT="$(merge_if_missing_pattern "$TT18_EXIT" "$TT18_RAW" '"tasks_tab_visible": true')"
TT18_EXIT="$(merge_if_missing_pattern "$TT18_EXIT" "$TT18_RAW" '"queue_job_submitted": true')"
TT18_EXIT="$(merge_if_missing_pattern "$TT18_EXIT" "$TT18_RAW" '"queue_job_completed": true')"
TT18_EXIT="$(merge_if_missing_pattern "$TT18_EXIT" "$TT18_RAW" '"child_session_visible": true')"
TT18_EXIT="$(merge_if_missing_pattern "$TT18_EXIT" "$TT18_RAW" '"queue_job_visible": true')"
TT18_EXIT="$(merge_if_missing_pattern "$TT18_EXIT" "$TT18_RAW" '"history_clear_keeps_view": true')"
TT18_EXIT="$(merge_if_missing_pattern "$TT18_EXIT" "$TT18_RAW" '"parent_status": "completed"')"
TT18_EXIT="$(merge_if_missing_pattern "$TT18_EXIT" "$TT18_RAW" '"child_status": "completed"')"
TT18_EXIT="$(merge_if_missing_pattern "$TT18_EXIT" "$TT18_RAW" '"queue_status": "completed"')"
TT18_EXIT="$(merge_if_missing_pattern "$TT18_EXIT" "$TT18_RAW" '"runtime_exceptions": []')"
TT18_EXIT="$(merge_if_missing_pattern "$TT18_EXIT" "$TT18_RAW" '"console_errors": []')"
TT18_EXIT="$(merge_if_missing_pattern "$TT18_EXIT" "$TT18_ARTIFACT" '"queue_job_completed": true')"
TT18_EXIT="$(merge_if_missing_pattern "$TT18_EXIT" "$TT18_ARTIFACT" '"child_status": "completed"')"
TT18_EXIT="$(merge_if_missing_pattern "$TT18_EXIT" "$TT18_ARTIFACT" '"queue_status": "completed"')"
TT18_EXIT="$(merge_if_missing_pattern "$TT18_EXIT" "$TT18_ARTIFACT" '"runtime_exceptions": []')"
finalize_case "TT18" "Web Console Deep Smoke" "$TT18_EXIT" "$TT18_RAW" "$TT18_ARTIFACT" "" "" "$TT18_DIR"

TT19_DIR="${CASES_DIR}/TT19"
mkdir -p "$TT19_DIR"
TT19_RAW="${TT18_SUBRUN_DIR}/SUMMARY.md"
TT19_ARTIFACT="${TT19_DIR}/artifact.md"
copy_if_present "${TT18_SUBRUN_DIR}/SUMMARY.md" "${TT19_DIR}/focused-followup-summary.md"
TT19_RETRY_SESSION_COPY="${TT19_DIR}/retry-session-after-continue.session.json"
TT19_RETRY_EVENTS_COPY="${TT19_DIR}/retry-session-after-continue.events.jsonl"
TT19_PARENT_BACKGROUND_COPY="${TT19_DIR}/parent.background.jsonl"
copy_if_present "${TT18_SUBRUN_DIR}/evidence/retry-session-after-continue/session.json" "$TT19_RETRY_SESSION_COPY"
copy_if_present "${TT18_SUBRUN_DIR}/evidence/retry-session-after-continue/events.jsonl" "$TT19_RETRY_EVENTS_COPY"
copy_if_present "${TT18_SUBRUN_DIR}/evidence/parent-session-after-queue/background.jsonl" "$TT19_PARENT_BACKGROUND_COPY"
{
	echo "# tt19 retry-resume and queue-dedup operator proof"
	echo
	echo "- Subrun: \`${TT18_SUBRUN_DIR}\`"
	echo "- Durable retry evidence: \`${TT18_SUBRUN_DIR}/evidence/retry-session-after-continue/session.json\`"
	echo "- Parent background evidence: \`${TT18_SUBRUN_DIR}/evidence/parent-session-after-queue/background.jsonl\`"
	echo "- Result: retry metadata stayed at max_attempts=2, a real provider.retry was emitted, and queue notifications remained deduplicated by queue_job_id."
} >"$TT19_ARTIFACT"
append_snippet_block "$TT19_ARTIFACT" "decisive retry snippets" \
	"$(first_matching_line "${TT18_SUBRUN_DIR}/SUMMARY.md" "Retry completion attempts:")" \
	"$(first_matching_line "${TT18_SUBRUN_DIR}/SUMMARY.md" "Retry final session status:")" \
	"$(first_matching_line "$TT19_RETRY_SESSION_COPY" "\"max_attempts\": 2")" \
	"$(first_matching_line "$TT19_RETRY_EVENTS_COPY" "\"type\":\"provider.retry\"")"
append_snippet_block "$TT19_ARTIFACT" "decisive queue dedup snippets" \
	"$(first_matching_line "${TT18_SUBRUN_DIR}/SUMMARY.md" "Background notifications after reconcile:")" \
	"$(first_matching_line "${TT18_SUBRUN_DIR}/SUMMARY.md" "Unique queue_job_id values after reconcile:")" \
	"$(sed -n '1p' "$TT19_PARENT_BACKGROUND_COPY" 2>/dev/null || true)" \
	"$(sed -n '2p' "$TT19_PARENT_BACKGROUND_COPY" 2>/dev/null || true)"
TT19_EXIT="$TT18_SUBRUN_EXIT"
TT19_EXIT="$(merge_if_missing_pattern "$TT19_EXIT" "$TT18_SUBRUN_DIR/SUMMARY.md" 'retry_policy.max_attempts=2')"
TT19_EXIT="$(merge_if_missing_pattern "$TT19_EXIT" "$TT18_SUBRUN_DIR/SUMMARY.md" 'Unique queue_job_id values after reconcile: `2`')"
TT19_EXIT="$(merge_if_missing_pattern "$TT19_EXIT" "$TT18_SUBRUN_DIR/evidence/retry-session-after-continue/events.jsonl" '"type":"provider.retry"')"
TT19_EXIT="$(merge_if_missing_pattern "$TT19_EXIT" "$TT18_SUBRUN_DIR/evidence/retry-session-after-continue/session.json" '"max_attempts": 2')"
TT19_EXIT="$(merge_if_missing_pattern "$TT19_EXIT" "$TT19_ARTIFACT" '"type":"provider.retry"')"
TT19_EXIT="$(merge_if_missing_pattern "$TT19_EXIT" "$TT19_ARTIFACT" '"queue_job_id":"job_')"
finalize_case "TT19" "Retry-Resume And Queue-Dedup Operator Proof" "$TT19_EXIT" "$TT19_RAW" "$TT19_ARTIFACT" "" "" "$TT19_DIR"

write_case_bucket_summary

TT20_DIR="${CASES_DIR}/TT20"
mkdir -p "$TT20_DIR"
TT20_PROMPT="${TT20_DIR}/prompt.txt"
TT20_RAW="${TT20_DIR}/raw.jsonl"
TT20_ARTIFACT="${TT20_DIR}/artifact.md"
TT20_SANDBOX="${TT20_DIR}/sandbox"
TT20_SANDBOX_ARTIFACT_REL="reports/tt20-readiness-review.md"
TT20_SANDBOX_ARTIFACT="${TT20_SANDBOX}/${TT20_SANDBOX_ARTIFACT_REL}"
TT20_ALLOWED_FILES=(
	"${RUN_DIR}/notes/preflight-index.tsv"
	"${RUN_DIR}/notes/preflight-task-heavy-proof-tests.md"
	"${RUN_DIR}/notes/preflight-gap-proof-summary.md"
	"${RUN_DIR}/notes/scenario-index.tsv"
	"${RUN_DIR}/notes/case-buckets.md"
	"${RUN_DIR}/cases/TT01/artifact.md"
	"${RUN_DIR}/cases/TT02/artifact.md"
	"${RUN_DIR}/cases/TT03/artifact.md"
	"${RUN_DIR}/cases/TT04/artifact.md"
	"${RUN_DIR}/cases/TT05/artifact.md"
	"${RUN_DIR}/cases/TT06/artifact.md"
	"${RUN_DIR}/cases/TT07/artifact.md"
	"${RUN_DIR}/cases/TT08/artifact.md"
	"${RUN_DIR}/cases/TT09/artifact.md"
	"${RUN_DIR}/cases/TT10/artifact.md"
	"${RUN_DIR}/cases/TT11/artifact.md"
	"${RUN_DIR}/cases/TT11/children.raw.json"
	"${RUN_DIR}/cases/TT11/role-proof.txt"
	"${RUN_DIR}/cases/TT12/artifact.md"
	"${RUN_DIR}/cases/TT12/children.raw.json"
	"${RUN_DIR}/cases/TT12/queue-review.md"
	"${RUN_DIR}/cases/TT12/role-proof.txt"
	"${RUN_DIR}/cases/TT13/artifact.md"
	"${RUN_DIR}/cases/TT14/artifact.md"
	"${RUN_DIR}/cases/TT15/artifact.md"
	"${RUN_DIR}/cases/TT16/artifact.md"
	"${RUN_DIR}/cases/TT17/artifact.md"
	"${RUN_DIR}/cases/TT18/artifact.md"
	"${RUN_DIR}/cases/TT18/webconsole-ui-smoke.json"
	"${RUN_DIR}/cases/TT19/artifact.md"
	"${RUN_DIR}/cases/TT19/focused-followup-summary.md"
	"${RUN_DIR}/cases/TT19/retry-session-after-continue.session.json"
	"${RUN_DIR}/cases/TT19/parent.background.jsonl"
	"README.md"
	"spec/00-product.md"
	"spec/01-runtime-architecture.md"
	"spec/17-web-console.md"
	"validation/task_heavy_scenarios.md"
)
prepare_isolated_review_workspace "$TT20_SANDBOX" "${TT20_ALLOWED_FILES[@]}"
write_prompt_literal "$TT20_PROMPT" <<'PROMPT'
Use the review_pipeline skill for this task.
Inspect only these current-run files: __RUN_DIR__/notes/preflight-index.tsv, __RUN_DIR__/notes/preflight-task-heavy-proof-tests.md, __RUN_DIR__/notes/preflight-gap-proof-summary.md, __RUN_DIR__/notes/scenario-index.tsv, __RUN_DIR__/notes/case-buckets.md, __RUN_DIR__/cases/TT01/artifact.md, __RUN_DIR__/cases/TT02/artifact.md, __RUN_DIR__/cases/TT03/artifact.md, __RUN_DIR__/cases/TT04/artifact.md, __RUN_DIR__/cases/TT05/artifact.md, __RUN_DIR__/cases/TT06/artifact.md, __RUN_DIR__/cases/TT07/artifact.md, __RUN_DIR__/cases/TT08/artifact.md, __RUN_DIR__/cases/TT09/artifact.md, __RUN_DIR__/cases/TT10/artifact.md, __RUN_DIR__/cases/TT11/artifact.md, __RUN_DIR__/cases/TT11/children.raw.json, __RUN_DIR__/cases/TT11/role-proof.txt, __RUN_DIR__/cases/TT12/artifact.md, __RUN_DIR__/cases/TT12/children.raw.json, __RUN_DIR__/cases/TT12/queue-review.md, __RUN_DIR__/cases/TT12/role-proof.txt, __RUN_DIR__/cases/TT13/artifact.md, __RUN_DIR__/cases/TT14/artifact.md, __RUN_DIR__/cases/TT15/artifact.md, __RUN_DIR__/cases/TT16/artifact.md, __RUN_DIR__/cases/TT17/artifact.md, __RUN_DIR__/cases/TT18/artifact.md, __RUN_DIR__/cases/TT18/webconsole-ui-smoke.json, __RUN_DIR__/cases/TT19/artifact.md, __RUN_DIR__/cases/TT19/focused-followup-summary.md, __RUN_DIR__/cases/TT19/retry-session-after-continue.session.json, __RUN_DIR__/cases/TT19/parent.background.jsonl, README.md, spec/00-product.md, spec/01-runtime-architecture.md, spec/17-web-console.md, and validation/task_heavy_scenarios.md.
Distinguish validated issues from remaining benchmark limits. Do not invent repo bugs from benchmark-limited gaps.
Treat __RUN_DIR__/notes/preflight-gap-proof-summary.md as the primary direct citation anchor for current-run script-level proof on provider metadata/retry durability, review artifact enforcement, report path hardening, and exact-template guard behavior unless later live evidence contradicts it.
Use __RUN_DIR__/notes/case-buckets.md to keep the summary compact, but ground every claim in direct snippets from the allowed files.
Use targeted retrieval only. Do not use shell to write this artifact. Do not change directory outside the current sandbox. The harness will copy the sandbox-local report after the run, and any second copy elsewhere is treated as invalid.
This is a review artifact, so use the canonical structure expected by the review guard:
- section `findings`
- section `strong areas`
- section `remaining risks`
- section `next loop`
Under `findings`, only include repo-owned go-cli-agent issues that are directly proven by current-run script-level proof or owning-runtime evidence. Do not promote benchmark-target review findings from TT09, TT10, TT11, or TT12 into repo-owned findings unless the cited evidence itself proves a go-cli-agent runtime defect. If there is no validated repo-owned finding, write the exact sentence `No validated findings.` inside `findings`.
If you do include a finding, each one must include `Severity`, `Confidence`, `Evidence`, `Snippet`, and `Why it matters`.
Evidence lines must use explicit workspace-relative repo paths with exact line ranges, for example `validation/runs/.../cases/TT14/artifact.md:7-26`.
Every `Snippet` must literally appear within the cited line range.
Use `remaining risks` to capture benchmark-target findings, benchmark limits, or unresolved comparator boundaries that are not repo-owned defects.
Write __TT20_SANDBOX_ARTIFACT_REL__ with sections: findings, strong areas, remaining risks, next loop.
Then call finish.
PROMPT
sed -i "s#__RUN_DIR__#${RUN_DIR}#g" "$TT20_PROMPT"
sed -i "s#__TT20_SANDBOX_ARTIFACT_REL__#${TT20_SANDBOX_ARTIFACT_REL}#g" "$TT20_PROMPT"
run_exec "$TT20_PROMPT" "$TT20_RAW" "$TT20_SANDBOX" 420
TT20_EXIT=$?
copy_if_present "$TT20_SANDBOX_ARTIFACT" "$TT20_ARTIFACT"
TT20_STRAY_REPO_ARTIFACT="${ROOT_DIR}/reports/tt20-readiness-review.md"
if [[ -f "$TT20_STRAY_REPO_ARTIFACT" ]]; then
	rm -f "$TT20_STRAY_REPO_ARTIFACT"
	TT20_EXIT="$(merge_exit_code "$TT20_EXIT" 1)"
fi
finalize_case "TT20" "Task-Heavy Readiness And Issue Inventory" "$TT20_EXIT" "$TT20_RAW" "$TT20_ARTIFACT" "$(extract_session_id "$TT20_RAW")" "" "$TT20_DIR"

finalize_run_outputs
printf '%s\n' "$RUN_DIR"
if (( FAILED_SCENARIOS > 0 )); then
	exit 1
fi
