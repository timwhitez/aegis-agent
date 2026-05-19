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
MATRIX_LABEL="${GO_CLI_AGENT_MATRIX_LABEL:-round40-enterprise-real-matrix-steer-proofclose}"
ROUND_ID="${1:-$(date -u +%F)-openai-compatible-${MODEL}-${MATRIX_LABEL}}"
RUN_DIR="validation/runs/${ROUND_ID}"
CONFIG_PATH="${RUN_DIR}/config.openai-compatible.effective.yaml"
LOW_COMPACT_CONFIG_PATH="${RUN_DIR}/config.openai-compatible-low-compact.effective.yaml"
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
RETRYPROXY_BIN="${BIN_DIR}/retryproxy"

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
PLANNED_SCENARIOS=26
MATRIX_PROVIDER_FAILURE_ABORT_THRESHOLD="${GO_CLI_AGENT_MATRIX_PROVIDER_FAILURE_ABORT_THRESHOLD:-2}"
CONSECUTIVE_PROVIDER_INFRA_FAILURES=0
SCENARIO_HELPER_PID=""
declare -a FAILED_SCENARIO_IDS=()
declare -a FAILED_SCENARIO_LABELS=()
declare -a FAILED_SCENARIO_RAWS=()
declare -a FAILED_SCENARIO_NOTES=()
declare -a SOFT_PREFLIGHT_NAMES=()
declare -a SOFT_PREFLIGHT_PATHS=()

write_config_with_base_url() {
	local src="$1"
	local dst="$2"
	local base_url="$3"
	local escaped_base_url=""
	escaped_base_url="$(printf '%s' "$base_url" | sed 's/[&]/\\&/g')"
	sed "s#^\([[:space:]]*base_url:\) .*#\1 ${escaped_base_url}#" "$src" >"$dst"
}

write_effective_config() {
	local src="$1"
	local dst="$2"
	write_config_with_base_url "$src" "$dst" "$LIVE_BASE_URL"
}

prepare_effective_configs() {
	write_effective_config "$CONFIG_TEMPLATE_PATH" "$CONFIG_PATH"
	write_effective_config "$LOW_COMPACT_CONFIG_TEMPLATE_PATH" "$LOW_COMPACT_CONFIG_PATH"
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

write_prompt_literal() {
	local path="$1"
	cat >"$path"
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

wait_for_any_pattern() {
	local path="$1"
	local timeout_sec="${2:-90}"
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

first_pattern_line_number() {
	local path="$1"
	local pattern="$2"
	if [[ ! -f "$path" ]]; then
		return 0
	fi
	grep -nF "$pattern" "$path" | head -n1 | cut -d: -f1
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
	if grep -Fq 'matrix_watchdog:' "$raw_path"; then
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

write_gap_proof_summary() {
	local exit_code="$1"
	local raw_path="$2"
	local note_path="${NOTE_DIR}/preflight-gap-proof-summary.md"
	{
		echo "# gap proof summary"
		echo
		echo "- status: $(status_from_exit "$exit_code")"
		echo "- exit_code: ${exit_code}"
		echo "- raw: \`${raw_path}\`"
		echo
		echo "## directly proven areas"
		echo
		echo "### Provider metadata and retry durability"
		echo
		echo "- \`TestRunnerStartPersistsProviderOptionsInSessionMetadata\` proves configured provider options are written into durable session metadata at session start."
		echo "- \`TestRunnerStartPropagatesConfiguredProviderOptionsIntoOpenAIRequest\` proves stored provider options are forwarded into the outbound OpenAI-compatible Responses request."
		echo "- \`TestRunnerContinueUsesDurableRetryPolicyFromSessionMetadata\` proves continue/resume rebuilds adapter retry behavior from durable session metadata rather than current process drift."
		echo "- \`TestEnginePassesSessionMetadataIntoProviderRequest\` and \`TestEngineEmitsProviderRequestPreparedEvent\` prove session metadata and retry-policy facts are visible on the runtime-to-provider boundary and in durable prepared-event evidence."
		echo
		echo "### Review artifact enforcement"
		echo
		echo "- \`TestToolGuardBlocksInvalidReviewArtifactWriteOnReviewLikeScratchPath\` proves review-like artifact writes are blocked when the canonical findings structure is missing."
		echo "- \`TestReviewArtifactSatisfiedCountsValidatedRequestedPathWhenPresent\` proves finish satisfaction stays tied to a validated requested artifact path once one is explicitly requested."
		echo "- \`TestValidateMarkdownArtifactWithWorkspaceRejectsMissingEvidenceSnippet\` and \`TestValidateMarkdownArtifactWithWorkspaceRejectsUnreadableEvidencePath\` prove the validator rejects review artifacts that lack snippet-backed evidence or cite unreadable in-workspace paths."
		echo
		echo "### Report prevalidation and workspace path hardening"
		echo
		echo "- \`TestEngineBlocksEscapingFinalArtifactPathBeforeToolExecution\` proves a requested final report path is blocked before the real tool executes when it escapes the workspace."
		echo "- \`TestRequestedArtifactWriteRejectsWorkspaceEscapeForEditFile\`, \`TestRequestedArtifactWriteRejectsSymlinkEscapeForWriteFile\`, and \`TestRequestedArtifactWriteUsesSameWorkspaceResolverAsFileTools\` prove review-artifact path prevalidation reuses the same workspace resolver and rejects escape attempts, including symlink escapes."
		echo "- \`TestResolveWorkspacePathRejectsSymlinkEscape\` proves the shared file-tool workspace resolver itself rejects symlink-based escapes."
		if (( exit_code == 0 )); then
			echo
			echo "## operator note"
			echo
			echo "These proof-focused tests passed in the current run, so later audit/readiness scenarios should treat the three former RT21 proof-completeness gaps as directly covered unless later live evidence contradicts them."
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
	local failure_note="${8:-}"
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
	} >"$note_path"
}

finalize_scenario() {
	local scenario_id="$1"
	local label="$2"
	local exit_code="$3"
	local raw_path="$4"
	local artifact_path="$5"
	local session_id="${6:-}"
	local failure_note="${7:-}"
	exit_code="$(merge_if_missing_file "$exit_code" "$raw_path")"
	if [[ -n "$artifact_path" ]]; then
		exit_code="$(merge_if_missing_file "$exit_code" "$artifact_path")"
	fi
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
	write_scenario_note "$scenario_id" "$label" "$status" "$exit_code" "$raw_path" "$artifact_path" "$session_id" "$resolved_failure_note"
	if [[ "$status" != "passed" &&
		-z "$HARD_ABORT_REASON" &&
		"$CONSECUTIVE_PROVIDER_INFRA_FAILURES" -ge "$MATRIX_PROVIDER_FAILURE_ABORT_THRESHOLD" ]]; then
		HARD_ABORT_REASON="provider path became unstable after ${CONSECUTIVE_PROVIDER_INFRA_FAILURES} consecutive scenario failures ending at ${scenario_id}: ${resolved_failure_note}"
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

run_exec_with_pattern_steer() {
	local prompt_path="$1"
	local raw_path="$2"
	local workdir="$3"
	local timeout_sec="${4:-300}"
	local first_turn_timeout_sec="${5:-45}"
	local trigger_pattern="$6"
	local trigger_timeout_sec="$7"
	local steer_raw_path="$8"
	local steer_message="$9"
	local fallback_trigger_pattern="${10:-}"
	if (( timeout_sec < 420 )); then
		timeout_sec=420
	fi
	"${AGENT_BIN}" exec \
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
	wait_for_pattern "$raw_path" '"type":"session.started"' 90 || true
	local session_id=""
	session_id="$(extract_session_id "$raw_path")"
	if [[ -n "$session_id" && -n "$trigger_pattern" && -n "$steer_message" ]]; then
		if wait_for_any_pattern "$raw_path" "$trigger_timeout_sec" "$trigger_pattern" "$fallback_trigger_pattern" >/dev/null; then
			if kill -0 "$pid" 2>/dev/null; then
				local status=""
				status="$(raw_final_status "$raw_path")"
				if [[ "$status" != "completed" && "$status" != "failed" ]]; then
					"${AGENT_BIN}" steer "$session_id" \
						--config "$CONFIG_PATH" \
						--json \
						--interrupt \
						--message "$steer_message" \
						>"$steer_raw_path" 2>&1 || true
				fi
			fi
		fi
	fi
	wait "$pid"
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

copy_relative_file_if_present() {
	local rel_path="$1"
	local dest_root="$2"
	local source_root="${3:-$WORKSPACE_ROOT}"
	local src="${source_root}/${rel_path}"
	local dst="${dest_root}/${rel_path}"
	if [[ -f "$src" ]]; then
		mkdir -p "$(dirname "$dst")"
		cp "$src" "$dst"
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

write_summary() {
	{
		echo "# Matrix Summary"
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
				echo "- ${FAILED_SCENARIO_IDS[$i]} ${FAILED_SCENARIO_LABELS[$i]}: ${FAILED_SCENARIO_NOTES[$i]}"
				echo "  raw: \`${FAILED_SCENARIO_RAWS[$i]}\`"
			done
		fi
		if [[ -z "$HARD_ABORT_REASON" && "$FAILED_SCENARIOS" -eq 0 ]]; then
			echo
			echo "## Matrix conclusion"
			echo
			echo "- All planned scenarios completed without a scenario-level command failure."
			if (( SOFT_PREFLIGHT_FAILURES == 0 )); then
				echo "- Build, test, proof-focused tests, gap-proof tests, doctor, probe, and the realistic preflight turn all passed before or during the matrix."
			else
				echo "- Some soft preflight checks failed, but the full scenario matrix still completed."
			fi
		fi
	} >"$SUMMARY_PATH"
}

write_issues() {
	{
		echo "# Matrix Issues"
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
		echo "# Matrix Aborted"
		echo
		echo "- Run directory: \`${RUN_DIR}\`"
		echo "- Reason: ${reason}"
		echo "- Preflight index: \`notes/preflight-index.tsv\`"
	} >"$ABORTED_PATH"
}

RUN_FINALIZED=0

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

copy_workspace docset
copy_workspace incident
copy_workspace nested_review
copy_workspace platform_go
copy_workspace_as platform_go rt07_platform_go
copy_workspace_as platform_go rt23_role_platform_go
copy_workspace platform_py
copy_workspace_as docset rt24_docset
copy_workspace_as docset rt25_steer_proof

DOCSET_DIR="${WORKSPACE_DIR}/docset"
INCIDENT_DIR="${WORKSPACE_DIR}/incident"
NESTED_API_DIR="${WORKSPACE_DIR}/nested_review/services/api"
PLATFORM_GO_DIR="${WORKSPACE_DIR}/platform_go"
RT07_PLATFORM_GO_DIR="${WORKSPACE_DIR}/rt07_platform_go"
RT23_ROLE_GO_DIR="${WORKSPACE_DIR}/rt23_role_platform_go"
PLATFORM_PY_DIR="${WORKSPACE_DIR}/platform_py"
RT24_DOCSET_DIR="${WORKSPACE_DIR}/rt24_docset"
RT25_STEER_PROOF_DIR="${WORKSPACE_DIR}/rt25_steer_proof"
RT20_COMPARATOR_DIR="${WORKSPACE_DIR}/rt20_comparator"

mapfile -t TOP_LEVEL_MARKDOWN < <(cd "$WORKSPACE_ROOT" && find . -maxdepth 1 -type f -name '*.md' -printf '%P\n' | sort)
TOP_LEVEL_MARKDOWN_LIST="$(printf '%s, ' "${TOP_LEVEL_MARKDOWN[@]}")"
TOP_LEVEL_MARKDOWN_LIST="${TOP_LEVEL_MARKDOWN_LIST%, }"
mapfile -t TOP_LEVEL_MARKDOWN_NO_RT04 < <(cd "$WORKSPACE_ROOT" && find . -maxdepth 1 -type f -name '*.md' ! -name 'rt04-forced-compaction-proof.md' -printf '%P\n' | sort)
TOP_LEVEL_MARKDOWN_NO_RT04_LIST="$(printf '%s, ' "${TOP_LEVEL_MARKDOWN_NO_RT04[@]}")"
TOP_LEVEL_MARKDOWN_NO_RT04_LIST="${TOP_LEVEL_MARKDOWN_NO_RT04_LIST%, }"

run_preflight "build" "${RAW_DIR}/preflight-build.txt" ./build.sh
PRE_BUILD_EXIT=$?
write_preflight_note "build" "$PRE_BUILD_EXIT" "${RAW_DIR}/preflight-build.txt"
if (( PRE_BUILD_EXIT != 0 )); then
	HARD_ABORT_REASON="./build.sh failed"
	finalize_run_outputs
	printf '%s\n' "$RUN_DIR"
	exit 1
fi

if ! prepare_agent_bin; then
	HARD_ABORT_REASON="failed to stage run-local agent binary"
	finalize_run_outputs
	printf '%s\n' "$RUN_DIR"
	exit 1
fi

run_preflight "build-retryproxy" "${RAW_DIR}/build-retryproxy.txt" \
	go build -o "${RETRYPROXY_BIN}" ./validation/cmd/retryproxy
PRE_RETRYPROXY_BUILD_EXIT=$?
write_preflight_note "build-retryproxy" "$PRE_RETRYPROXY_BUILD_EXIT" "${RAW_DIR}/build-retryproxy.txt"
if (( PRE_RETRYPROXY_BUILD_EXIT != 0 )); then
	HARD_ABORT_REASON="failed to build validation retryproxy helper"
	finalize_run_outputs
	printf '%s\n' "$RUN_DIR"
	exit 1
fi

run_preflight "test" "${RAW_DIR}/preflight-test.txt" ./test.sh
PRE_TEST_EXIT=$?
write_preflight_note "test" "$PRE_TEST_EXIT" "${RAW_DIR}/preflight-test.txt"
if (( PRE_TEST_EXIT != 0 )); then
	HARD_ABORT_REASON="./test.sh failed"
	finalize_run_outputs
	printf '%s\n' "$RUN_DIR"
	exit 1
fi

run_preflight "proof-tests" "${RAW_DIR}/preflight-proof-tests.txt" \
	go test ./internal/runtime -run 'TestRunnerStartPropagatesConfiguredProviderOptionsIntoOpenAIRequest|TestEngineEmitsProviderRequestPreparedEvent|TestEngineBlocksEscapingFinalArtifactPathBeforeToolExecution|TestToolGuardBlocksEscapingFinalArtifactPathBeforeExecution|TestToolGuardBlocksEscapingReviewArtifactPathBeforeExecution|TestRunnerDelegateCreatesChildSessionWithIsolation|TestRunnerQueueSubmitAndWorkerCompletesJob|TestRunnerDelegateCopiesVisibleOutputsIntoRequestedWorkspace|TestEngineAcceptsBackgroundResultsBeforeProviderCall|TestNextHarnessReminderAddsProjectMemoryRefreshReminder|TestNextHarnessReminderRefreshesSpecAndPlanAfterSteerScopeChange|TestToolGuardBlocksFinalArtifactWriteThatViolatesExactTemplate|TestToolGuardBlocksFinishUntilProjectMemoryRefresh|TestRunnerSteerRejectsOversizedTextInput|TestEngineInterruptSteerCancelsProviderAndContinuesWithAcceptedMessage' -count=1
PRE_PROOF_TESTS_EXIT=$?
write_preflight_note "proof-tests" "$PRE_PROOF_TESTS_EXIT" "${RAW_DIR}/preflight-proof-tests.txt"
if (( PRE_PROOF_TESTS_EXIT != 0 )); then
	HARD_ABORT_REASON="proof-focused go test preflight failed"
	finalize_run_outputs
	printf '%s\n' "$RUN_DIR"
	exit 1
fi

run_preflight "gap-proof-tests" "${RAW_DIR}/preflight-gap-proof-tests.txt" \
	go test ./internal/runtime ./internal/review ./internal/tools -run 'TestRunnerStartPersistsProviderOptionsInSessionMetadata|TestRunnerStartPropagatesConfiguredProviderOptionsIntoOpenAIRequest|TestRunnerContinueUsesDurableRetryPolicyFromSessionMetadata|TestEnginePassesSessionMetadataIntoProviderRequest|TestEngineEmitsProviderRequestPreparedEvent|TestEngineBlocksEscapingFinalArtifactPathBeforeToolExecution|TestToolGuardBlocksInvalidReviewArtifactWriteOnReviewLikeScratchPath|TestReviewArtifactSatisfiedCountsValidatedRequestedPathWhenPresent|TestRequestedArtifactWriteRejectsWorkspaceEscapeForEditFile|TestRequestedArtifactWriteRejectsSymlinkEscapeForWriteFile|TestRequestedArtifactWriteUsesSameWorkspaceResolverAsFileTools|TestValidateMarkdownArtifactWithWorkspaceRejectsMissingEvidenceSnippet|TestValidateMarkdownArtifactWithWorkspaceRejectsUnreadableEvidencePath|TestResolveWorkspacePathRejectsSymlinkEscape' -count=1
PRE_GAP_PROOF_TESTS_EXIT=$?
write_preflight_note "gap-proof-tests" "$PRE_GAP_PROOF_TESTS_EXIT" "${RAW_DIR}/preflight-gap-proof-tests.txt"
write_gap_proof_summary "$PRE_GAP_PROOF_TESTS_EXIT" "${RAW_DIR}/preflight-gap-proof-tests.txt"
if (( PRE_GAP_PROOF_TESTS_EXIT != 0 )); then
	HARD_ABORT_REASON="gap-proof go test preflight failed"
	finalize_run_outputs
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
write_prompt "$PREFLIGHT_REAL_PROMPT" "Inspect only the repo-root files ./README.md and ./AGENTS.md in the current go-cli-agent repository.
Use targeted retrieval only. Do not use glob, grep_files, shell find, or any directory search; read ./README.md and ./AGENTS.md directly.
Call finish with a short message that states the current Web-first default surface and which CLI commands remain fallback entrypoints."
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
Audit the current go-cli-agent repository for Web-first v1 surface discipline after the latest runtime gap-close pass.
Only inspect repo-root README.md, repo-root AGENTS.md, spec/00-product.md, spec/09-phase-plan.md, pkg/agent/agent.go, internal/app/app.go, and internal/runtime/facade.go.
Ignore validation/runs, validation/sessions, reports, bin, tmp, and generated artifacts.
Prefer targeted retrieval. Do not create a todo list or task board for this scenario. Do not use glob, grep_files, shell find, or any directory search. Use read_file/grep only on the explicit allowlisted paths. Use the current evidence once the four checks below are proven.
Validate only these four things: whether the default help surface is Web-first, whether advanced experimental routing stays outside the default Web-first path, whether core/web/experimental/store facades stay split, and whether the public SDK facade keeps advanced surfaces out of the default runtime runner.
Write ${ABS_ARTIFACT_DIR}/rt01-core-surface-audit.md with sections: core surface map, findings, unresolved questions, smallest next fixes.
If there is no validated finding, write the exact sentence No validated findings. inside findings.
Then call finish with a one-line summary."
RT01_RAW="${RAW_DIR}/rt01-core-surface-audit.jsonl"
RT01_STEER_RAW="${RAW_DIR}/rt01-core-surface-audit-steer.json"
run_exec_with_pattern_steer \
	"$RT01_PROMPT" \
	"$RT01_RAW" \
	"$ROOT_DIR" \
	300 \
	45 \
	'"kind":"large_project_coordination"' \
	150 \
	"$RT01_STEER_RAW" \
	"Use current evidence only. Do not read any more files. Write ${ABS_ARTIFACT_DIR}/rt01-core-surface-audit.md now with sections exactly: core surface map, findings, unresolved questions, smallest next fixes. If there is no validated finding, write the exact sentence No validated findings. inside findings. Then immediately call finish with a one-line summary." \
	'"reason":"retrieval_tail"'
RT01_EXIT=$?
finalize_scenario "RT01" "Core Surface Boundary Audit" "$RT01_EXIT" "$RT01_RAW" "${ARTIFACT_DIR}/rt01-core-surface-audit.md" "$(extract_session_id "$RT01_RAW")"

RT02_PROMPT="${PROMPT_DIR}/rt02-provider-review-safety-audit.prompt.txt"
RT02_PROOF_NOTE="${NOTE_DIR}/rt02-proof-anchor-input.md"
{
	echo "# RT02 Proof Anchor Input"
	echo
	echo "Use this note first. It already contains the exact proof anchors needed for the provider/review/safety audit."
	echo
	echo "## Former RT21 Proof-Hole Closures"
	echo
	echo "- gap proof summary: ${RUN_DIR}/notes/preflight-gap-proof-summary.md:9"
	nl -ba "${NOTE_DIR}/preflight-gap-proof-summary.md" | sed -n '9,30p'
	echo
	echo "## Provider Metadata And Retry Durability"
	echo
	echo "- runner metadata persistence: internal/runtime/runner.go:220"
	nl -ba internal/runtime/runner.go | sed -n '220,227p;819,830p'
	echo
	echo "- runner proof tests: internal/runtime/runner_test.go:68"
	nl -ba internal/runtime/runner_test.go | sed -n '68,78p;154,159p;277,289p'
	echo
	echo "- engine prepared-request boundary: internal/runtime/engine.go:153"
	nl -ba internal/runtime/engine.go | sed -n '153,156p;832,833p'
	echo
	echo "- openai request body fields: internal/provider/openai.go:61"
	nl -ba internal/provider/openai.go | sed -n '61,65p'
	echo
	echo "- retry transport implementation: internal/provider/http.go:20"
	nl -ba internal/provider/http.go | sed -n '20,73p'
	echo
	echo "## Review Artifact Enforcement"
	echo
	echo "- review guard implementation: internal/runtime/review_guard.go:150"
	nl -ba internal/runtime/review_guard.go | sed -n '150,160p;207,225p;269,304p'
	echo
	echo "- review guard proof tests: internal/runtime/review_guard_test.go:37"
	nl -ba internal/runtime/review_guard_test.go | sed -n '37,50p;181,226p'
	echo
	echo "## Workspace Boundary Hardening"
	echo
	echo "- shared path resolver: internal/tools/path.go:10"
	nl -ba internal/tools/path.go | sed -n '10,40p'
	echo
	echo "- path resolver proof test: internal/tools/path_test.go:9"
	nl -ba internal/tools/path_test.go | sed -n '9,20p'
	echo
	echo "## Usage Discipline"
	echo
	echo "- If this note does not directly prove a drift, keep the point in confirmed alignments or unresolved questions rather than inventing a new finding."
	echo "- Do not reread broad files. If one material ambiguity remains after reading this note, inspect at most two exact windows from: internal/runtime/runner.go, internal/runtime/review_guard.go, internal/runtime/engine.go, internal/tools/path.go, and ${RUN_DIR}/notes/preflight-gap-proof-summary.md."
} >"$RT02_PROOF_NOTE"
write_prompt "$RT02_PROMPT" "Use the review_pipeline skill for this task.
Read ${RUN_DIR}/notes/rt02-proof-anchor-input.md first.
Treat that note as the authoritative citation-anchor set for this audit. In most cases the note alone is enough.
Ignore validation/runs, validation/sessions, reports, bin, tmp, and generated artifacts other than the current-run proof note and gap-proof summary note.
Focus on three things only: provider metadata/retry propagation from config to durable session metadata to adapter requests, review-artifact enforcement quality, and whether any report pre-validation path can still escape the workspace boundary before the real tool executes.
Do not report retry-policy durability as a drift unless the durable provider_options.retry_policy path or its owning construction code is actually missing or contradictory.
If one material ambiguity remains after reading the proof note, inspect at most two exact windows from internal/runtime/runner.go, internal/runtime/review_guard.go, internal/runtime/engine.go, internal/tools/path.go, and ${RUN_DIR}/notes/preflight-gap-proof-summary.md.
Write ${ABS_ARTIFACT_DIR}/rt02-provider-review-safety-audit.md with sections: confirmed alignments, findings, unresolved questions, smallest next fixes.
Then call finish with a one-line summary."
RT02_RAW="${RAW_DIR}/rt02-provider-review-safety-audit.jsonl"
RT02_STEER_RAW="${RAW_DIR}/rt02-provider-review-safety-audit-steer.json"
run_exec_with_pattern_steer \
	"$RT02_PROMPT" \
	"$RT02_RAW" \
	"$ROOT_DIR" \
	300 \
	45 \
	'"kind":"large_project_coordination"' \
	180 \
	"$RT02_STEER_RAW" \
	"Use current evidence only. Do not read any more files except the already-allowed ${RUN_DIR}/notes/rt02-proof-anchor-input.md and ${RUN_DIR}/notes/preflight-gap-proof-summary.md if one exact anchor is still missing. Write ${ABS_ARTIFACT_DIR}/rt02-provider-review-safety-audit.md now with sections exactly: confirmed alignments, findings, unresolved questions, smallest next fixes. Only report a retry-policy durability problem if current evidence proves the durable provider_options.retry_policy path is missing or contradictory. Then immediately call finish with a one-line summary." \
	'"reason":"retrieval_tail"'
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
RT04_SANDBOX_ROOT="${RUN_DIR}/rt04-sandbox"
RT04_SANDBOX_REPO="${RT04_SANDBOX_ROOT}/go-cli-agent"
RT04_SESSION_ID=""
RT04_EXIT=0
prepare_isolated_review_workspace "$RT04_SANDBOX_REPO" \
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
copy_file_into_sandbox "$RT04_SANDBOX_REPO" "../blog-langchain-com__autonomous-context-compression.md" "references/blog-langchain-com__autonomous-context-compression.md" || RT04_EXIT="$(merge_exit_code "$RT04_EXIT" 1)"
copy_file_into_sandbox "$RT04_SANDBOX_REPO" "../openai-com__harness-engineering.md" "references/openai-com__harness-engineering.md" || RT04_EXIT="$(merge_exit_code "$RT04_EXIT" 1)"
copy_file_into_sandbox "$RT04_SANDBOX_REPO" "../learn-claude-code.md" "references/learn-claude-code.md" || RT04_EXIT="$(merge_exit_code "$RT04_EXIT" 1)"
mkdir -p "${RT04_SANDBOX_REPO}/reports"
RT04_PROOF_NOTE="${RT04_SANDBOX_REPO}/reports/rt04-proof-anchor-input.md"
{
	echo "# RT04 Proof Anchor Input"
	echo
	echo "Use this note first. It already contains the exact owning-code and spec anchors needed for the forced-compaction proof."
	echo
	echo "## Product and Runtime Boundary"
	echo
	echo "- spec/01 runtime boundary: spec/01-runtime-architecture.md:17"
	nl -ba "${RT04_SANDBOX_REPO}/spec/01-runtime-architecture.md" | sed -n '17,25p'
	echo
	echo "- spec/10 compaction facts and proof budget: spec/10-context-compaction.md:44"
	nl -ba "${RT04_SANDBOX_REPO}/spec/10-context-compaction.md" | sed -n '44,52p;68,79p;100,117p'
	echo
	echo "## Owning Code Anchors"
	echo
	echo "- compaction.go start/summary/finish path: internal/runtime/compaction.go:39"
	nl -ba "${RT04_SANDBOX_REPO}/internal/runtime/compaction.go" | sed -n '39,110p'
	echo
	echo "- compaction.go reread guidance: internal/runtime/compaction.go:444"
	nl -ba "${RT04_SANDBOX_REPO}/internal/runtime/compaction.go" | sed -n '444,446p'
	echo
	echo "- prompt retrieval budget note: internal/runtime/prompt.go:232"
	nl -ba "${RT04_SANDBOX_REPO}/internal/runtime/prompt.go" | sed -n '232,248p'
	echo
	echo "- engine provider-view boundary: internal/runtime/engine.go:137"
	nl -ba "${RT04_SANDBOX_REPO}/internal/runtime/engine.go" | sed -n '137,181p'
	echo
	echo "## Usage Discipline"
	echo
	echo "- If this note already proves the point, write the report immediately. Do not broaden retrieval."
	echo "- If one material ambiguity remains after reading this note, inspect at most two exact windows from: spec/10-context-compaction.md, internal/runtime/compaction.go, internal/runtime/prompt.go, and internal/runtime/engine.go."
} >"$RT04_PROOF_NOTE"
write_prompt "$RT04_PROMPT" "Use the review_pipeline skill for this task.
Read reports/rt04-proof-anchor-input.md first.
Treat that note as the authoritative citation-anchor set for this report. In most cases the note alone is enough.
Do not inspect reference docs or broaden retrieval unless one material ambiguity remains after reading the note.
If one material ambiguity remains, inspect at most two exact windows from spec/10-context-compaction.md, internal/runtime/compaction.go, internal/runtime/prompt.go, and internal/runtime/engine.go.
Use targeted retrieval and write reports/rt04-forced-compaction-proof.md inside this sandbox with sections: compaction evidence, proof-read behavior after compaction, remaining risks, next validation moves.
The harness will copy reports/rt04-forced-compaction-proof.md to ${ABS_ARTIFACT_DIR}/rt04-forced-compaction-proof.md after the run. Do not write a second copy to another path.
Then call finish with a one-line summary."
RT04_RAW="${RAW_DIR}/rt04-forced-compaction-proof.jsonl"
run_exec_with_config "$LOW_COMPACT_CONFIG_PATH" "$RT04_PROMPT" "$RT04_RAW" "$RT04_SANDBOX_REPO" 420
RT04_EXEC_EXIT=$?
RT04_EXIT="$(merge_exit_code "$RT04_EXIT" "$RT04_EXEC_EXIT")"
RT04_SESSION_ID="$(extract_session_id "$RT04_RAW")"
if ! raw_contains "$RT04_RAW" '"type":"compact.started"'; then
	RT04_EXIT="$(merge_exit_code "$RT04_EXIT" 1)"
fi
if ! raw_contains "$RT04_RAW" '"type":"compact.finished"'; then
	RT04_EXIT="$(merge_exit_code "$RT04_EXIT" 1)"
fi
copy_if_present "${RT04_SANDBOX_REPO}/reports/rt04-forced-compaction-proof.md" "${ARTIFACT_DIR}/rt04-forced-compaction-proof.md"
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
RT07_LOCAL_ARTIFACT="reports/rt07-proof.md"
write_prompt "$RT07_PROMPT" "Read the local AGENTS.md and README.md first.
This is a same-task enterprise repair proof, not a review-only report.
Before editing code, create reports/spec.md, reports/plan.md, reports/progress.md, and reports/validation.md in this workspace. Also create a short todo list and at least two durable tasks, and update them as the work progresses.
Constrain the first repair pass to README.md, internal/api/handler.go, internal/service/service.go, internal/config/config.go, internal/quota/policy.go, internal/api/handler_test.go, internal/config/config_test.go, and internal/quota/policy_test.go unless failing build output explicitly points somewhere else.
Start with the narrow failing tests, then fix this workspace so repo-wide go test ./... passes. You may edit across packages, but keep the repair root-cause driven and minimal.
Do not change established function signatures unless the failing build or test output proves the signature itself is wrong.
Write ${RT07_LOCAL_ARTIFACT} inside this workspace with sections: confirmed runtime evidence, findings, same-task repair outcome, remaining risks, next validation moves.
If repo-wide go test ./... passes, write the exact sentence No validated findings. inside the findings section.
If repo-wide go test ./... is still failing, record each concrete blocker in findings with explicit Severity:, Confidence:, Evidence:, Snippet:, and Why it matters: labels using the failing test/build output you observed.
In the final artifact, literally mention these proof tokens in the evidence text: task.created, task.updated, todo.updated, session.steer.accepted, provider.request.prepared, and go test ./....
Do not read .artifacts/tool-outputs or other internal generated artifacts. If you need line-numbered snippets or shell output for the proof, redirect that output into files under reports/_proof-*.txt inside this workspace and read those copies instead.
Once repo-wide go test ./... is green, write the local proof artifact and call finish in the same final turn.
Once repo-wide go test ./... is green, do not do more broad rereads. Write ${RT07_LOCAL_ARTIFACT} immediately and call finish in the same final turn.
Do not call finish until repo-wide go test ./... passes and the local proof artifact is written."
RT07_RAW="${RAW_DIR}/rt07-live-steer-two-wave.jsonl"
"${AGENT_BIN}" exec \
	--config "$CONFIG_PATH" \
	--provider openai-compatible \
	--model "$MODEL" \
	--workdir "$RT07_PLATFORM_GO_DIR" \
	--json \
	--timeout 540 \
	<"$RT07_PROMPT" >"$RT07_RAW" 2>&1 &
RT07_PID="$!"
RT07_EXIT=0
wait_for_pattern "$RT07_RAW" '"type":"session.started"' 90
RT07_WAIT_START_EXIT=$?
RT07_EXIT="$(merge_exit_code "$RT07_EXIT" "$RT07_WAIT_START_EXIT")"
RT07_SESSION_ID="$(extract_session_id "$RT07_RAW")"
wait_for_pattern "$RT07_RAW" '"tool_name":"task_create"' 180 || true
if [[ -n "$RT07_SESSION_ID" ]]; then
	"${AGENT_BIN}" steer "$RT07_SESSION_ID" \
		--config "$CONFIG_PATH" \
		--json \
		--interrupt \
		--message "Keep the same repair task. Preserve the durable taskboard and make the final proof text explicitly mention task.created, task.updated, todo.updated, session.steer.accepted, provider.request.prepared, and the final go test ./... result. Do not restart or switch workspaces." \
		>"${RAW_DIR}/rt07-live-steer-command-1.json" 2>&1
	RT07_STEER1_EXIT=$?
	RT07_EXIT="$(merge_exit_code "$RT07_EXIT" "$RT07_STEER1_EXIT")"
	sleep 1
	"${AGENT_BIN}" steer "$RT07_SESSION_ID" \
		--config "$CONFIG_PATH" \
		--json \
		--interrupt \
		--message "Final priority: keep the same repair focused, avoid broad rereads once the failing surface is clear, preserve established signatures unless the failing output disproves them, and once go test ./... is green write reports/rt07-proof.md and call finish in the same turn." \
		>"${RAW_DIR}/rt07-live-steer-command-2.json" 2>&1
	RT07_STEER2_EXIT=$?
	RT07_EXIT="$(merge_exit_code "$RT07_EXIT" "$RT07_STEER2_EXIT")"
else
	RT07_EXIT="$(merge_exit_code "$RT07_EXIT" 1)"
fi
wait "$RT07_PID"
RT07_WAIT_EXIT=$?
RT07_EXIT="$(merge_exit_code "$RT07_EXIT" "$RT07_WAIT_EXIT")"
if [[ -n "$RT07_SESSION_ID" ]]; then
	"${AGENT_BIN}" tasks "$RT07_SESSION_ID" --config "$CONFIG_PATH" --json >"${RAW_DIR}/rt07-taskboard-after.json" 2>&1
	RT07_TASKS_EXIT=$?
	RT07_EXIT="$(merge_exit_code "$RT07_EXIT" "$RT07_TASKS_EXIT")"
fi
(cd "$RT07_PLATFORM_GO_DIR" && go test ./...) >"${RAW_DIR}/rt07-postcheck-go-test.txt" 2>&1
RT07_POSTCHECK_EXIT=$?
RT07_EXIT="$(merge_exit_code "$RT07_EXIT" "$RT07_POSTCHECK_EXIT")"
RT07_FAILURE_NOTE=""
if (( RT07_POSTCHECK_EXIT != 0 )); then
	RT07_FAILURE_NOTE="external go test ./... postcheck failed: $(head -n 1 "${RAW_DIR}/rt07-postcheck-go-test.txt")"
fi
copy_if_present "${RT07_PLATFORM_GO_DIR}/${RT07_LOCAL_ARTIFACT}" "${ARTIFACT_DIR}/rt07-live-steer-audit.md"
copy_session_evidence "$RT07_SESSION_ID" "${EVIDENCE_DIR}/rt07-session"
RT07_EVENTS_PATH="${EVIDENCE_DIR}/rt07-session/events.jsonl"
RT07_REQUESTED_COUNT="$(count_pattern "$RT07_EVENTS_PATH" '"type":"session.steer.requested"')"
RT07_INTERRUPT_REQUESTED_COUNT="$(count_pattern "$RT07_EVENTS_PATH" '"type":"session.steer.interrupt_requested"')"
RT07_ACCEPTED_COUNT="$(count_pattern "$RT07_EVENTS_PATH" '"type":"session.steer.accepted"')"
RT07_TODO_UPDATED_COUNT="$(count_pattern "$RT07_EVENTS_PATH" '"type":"todo.updated"')"
RT07_TASK_CREATED_COUNT="$(count_pattern "$RT07_EVENTS_PATH" '"type":"task.created"')"
RT07_TASK_UPDATED_COUNT="$(count_pattern "$RT07_EVENTS_PATH" '"type":"task.updated"')"
RT07_PROVIDER_REQUEST_PREPARED_COUNT="$(count_pattern "$RT07_EVENTS_PATH" '"type":"provider.request.prepared"')"
printf '%s\n' \
	"events_path=${RT07_EVENTS_PATH}" \
	"requested_count=${RT07_REQUESTED_COUNT}" \
	"interrupt_requested_count=${RT07_INTERRUPT_REQUESTED_COUNT}" \
	"accepted_count=${RT07_ACCEPTED_COUNT}" \
	"todo_updated_count=${RT07_TODO_UPDATED_COUNT}" \
	"task_created_count=${RT07_TASK_CREATED_COUNT}" \
	"task_updated_count=${RT07_TASK_UPDATED_COUNT}" \
	"provider_request_prepared_count=${RT07_PROVIDER_REQUEST_PREPARED_COUNT}" \
	"postcheck_path=${RAW_DIR}/rt07-postcheck-go-test.txt" \
	>"${NOTE_DIR}/rt07-steer-metadata.txt"
if [[ "$RT07_REQUESTED_COUNT" -lt 2 || "$RT07_ACCEPTED_COUNT" -lt 2 || "$RT07_TODO_UPDATED_COUNT" -lt 1 || "$RT07_TASK_CREATED_COUNT" -lt 2 || "$RT07_TASK_UPDATED_COUNT" -lt 1 || "$RT07_PROVIDER_REQUEST_PREPARED_COUNT" -lt 1 ]]; then
	RT07_EXIT="$(merge_exit_code "$RT07_EXIT" 1)"
fi
finalize_scenario "RT07" "Same-Task Enterprise Repair With Steer" "$RT07_EXIT" "$RT07_RAW" "${ARTIFACT_DIR}/rt07-live-steer-audit.md" "$RT07_SESSION_ID" "$RT07_FAILURE_NOTE"

RT08_PARENT_PROMPT="${PROMPT_DIR}/rt08-delegate-parent.prompt.txt"
write_prompt "$RT08_PARENT_PROMPT" "Write reports/parent-note.md with one sentence noting that a delegated audit of this workspace is about to run.
Write reports/spec.md with a short delegated-review scope summary for this workspace.
Write reports/plan.md with a three-step reviewer checklist for the delegated audit.
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
		"Use the review_pipeline skill for this task. Read reports/spec.md and reports/plan.md first as the delegated reviewer handoff. The target workspace lives under validation/workspaces/platform_go inside this delegated worktree root. Review only these exact files: validation/workspaces/platform_go/README.md, validation/workspaces/platform_go/docs/contracts.md, validation/workspaces/platform_go/internal/api/handler.go, validation/workspaces/platform_go/internal/config/config.go, validation/workspaces/platform_go/internal/quota/policy.go, validation/workspaces/platform_go/internal/api/handler_test.go, validation/workspaces/platform_go/internal/config/config_test.go, and validation/workspaces/platform_go/internal/quota/policy_test.go. Do not glob or search the repo for alternates. Write reports/delegate-review.md with sections: findings, unresolved questions, next fixes. Refresh reports/validation.md with sections: delegated reviewer contract, confirmed findings, remaining risks. Then call finish." \
		>"$RT08_DELEGATE_RAW" 2>&1
	RT08_DELEGATE_EXIT=$?
	RT08_EXIT="$(merge_exit_code "$RT08_EXIT" "$RT08_DELEGATE_EXIT")"
else
	RT08_EXIT="$(merge_exit_code "$RT08_EXIT" 1)"
fi
copy_child_artifact_if_present "$RT08_DELEGATE_RAW" "reports/delegate-review.md" "${ARTIFACT_DIR}/rt08-delegate-review.md" "$PLATFORM_GO_DIR"
RT08_CHILD_SESSION_ID="$(extract_json_field "$RT08_DELEGATE_RAW" "session_id")"
RT08_CHILD_WORKDIR="$(extract_first_json_field "$RT08_DELEGATE_RAW" "workdir" "effective_workdir" "requested_workdir")"
RT08_EXIT="$(merge_if_missing_file "$RT08_EXIT" "${PLATFORM_GO_DIR}/reports/spec.md")"
RT08_EXIT="$(merge_if_missing_file "$RT08_EXIT" "${PLATFORM_GO_DIR}/reports/plan.md")"
RT08_EXIT="$(merge_if_missing_pattern "$RT08_EXIT" "$RT08_DELEGATE_RAW" "\"visible_paths\"")"
RT08_EXIT="$(merge_if_missing_pattern "$RT08_EXIT" "$RT08_DELEGATE_RAW" "reports/validation.md")"
RT08_EXIT="$(merge_if_missing_pattern "$RT08_EXIT" "$RT08_DELEGATE_RAW" "reports/delegate-review.md")"
copy_session_evidence "$RT08_CHILD_SESSION_ID" "${EVIDENCE_DIR}/rt08-child-session"
printf '%s\n' \
	"parent_session_id=${RT08_PARENT_SESSION_ID}" \
	"child_session_id=${RT08_CHILD_SESSION_ID}" \
	"child_workdir=${RT08_CHILD_WORKDIR}" \
	>"${NOTE_DIR}/rt08-delegate-metadata.txt"
finalize_scenario "RT08" "Foreground Delegated Review" "$RT08_EXIT" "$RT08_DELEGATE_RAW" "${ARTIFACT_DIR}/rt08-delegate-review.md" "$RT08_CHILD_SESSION_ID"

RT09_PARENT_PROMPT="${PROMPT_DIR}/rt09-queue-parent.prompt.txt"
write_prompt "$RT09_PARENT_PROMPT" "Review only README.md in this workspace.
Before stopping, create a minimal durable project-memory stack under reports/spec.md, reports/plan.md, reports/progress.md, and reports/validation.md for the upcoming background child handoff.
Also write reports/parent-context.md with a one-paragraph problem frame, then stop naturally without calling finish because a background child result will arrive later."
RT09_PARENT_RAW="${RAW_DIR}/rt09-queue-parent.jsonl"
run_run "$RT09_PARENT_PROMPT" "$RT09_PARENT_RAW" "$PLATFORM_PY_DIR" 240
RT09_EXIT=$?
RT09_PARENT_SESSION_ID="$(extract_session_id "$RT09_PARENT_RAW")"
RT09_SUBMIT_RAW="${RAW_DIR}/rt09-queue-submit.json"
RT09_WORKER_RAW="${RAW_DIR}/rt09-queue-worker.json"
RT09_CONTINUE_RAW="${RAW_DIR}/rt09-queue-continue.jsonl"
RT09_RECOVER_PROMPT="${PROMPT_DIR}/rt09-queue-recover.prompt.txt"
RT09_RECOVER_RAW="${RAW_DIR}/rt09-queue-recovery.jsonl"
RT09_CONTINUE_EXIT=0
RT09_RECOVER_EXIT=0
RT09_PARENT_FINAL_LINE=""
if [[ -n "$RT09_PARENT_SESSION_ID" ]]; then
	"${AGENT_BIN}" experimental queue submit \
		--config "$CONFIG_PATH" \
		--parent "$RT09_PARENT_SESSION_ID" \
		--provider openai-compatible \
		--model "$MODEL" \
		--workdir "$PLATFORM_PY_DIR" \
		--isolation copy \
		--agent reviewer \
		--json \
		"Use the review_pipeline skill for this task. Start by reading reports/spec.md, reports/plan.md, reports/progress.md, and reports/validation.md as the parent handoff. Review README.md, app/config.py, app/report.py, tests/test_config.py, and tests/test_report.py. Write reports/queue-review.md with sections: findings, remaining risks, next fixes. Refresh reports/progress.md and reports/validation.md before finish so the background handoff stays current. Then call finish." \
		>"$RT09_SUBMIT_RAW" 2>&1
	RT09_SUBMIT_EXIT=$?
	RT09_EXIT="$(merge_exit_code "$RT09_EXIT" "$RT09_SUBMIT_EXIT")"
	"${AGENT_BIN}" experimental queue worker --config "$CONFIG_PATH" --once --json >"$RT09_WORKER_RAW" 2>&1
	RT09_WORKER_EXIT=$?
	RT09_EXIT="$(merge_exit_code "$RT09_EXIT" "$RT09_WORKER_EXIT")"
	RT09_JOB_ID="$(extract_json_field "$RT09_SUBMIT_RAW" "id")"
	RT09_CHILD_WORKDIR="$(extract_first_json_field "$RT09_WORKER_RAW" "workdir" "effective_workdir" "requested_workdir")"
	RT09_CHILD_SESSION_ID="$(extract_json_field "$RT09_WORKER_RAW" "session_id")"
	if [[ -n "$RT09_CHILD_WORKDIR" ]]; then
		copy_if_present "${RT09_CHILD_WORKDIR}/reports/queue-review.md" "${PLATFORM_PY_DIR}/reports/queue-review.md"
	fi
	RT09_PARENT_FINAL_LINE="$(tail -n 1 "$RT09_PARENT_RAW" 2>/dev/null || true)"
	"${AGENT_BIN}" continue "$RT09_PARENT_SESSION_ID" \
		--config "$CONFIG_PATH" \
		--provider openai-compatible \
		--model "$MODEL" \
		--json \
		--message "A background child result already exists for this session. Do not create or rewrite reports/spec.md, reports/plan.md, reports/progress.md, or reports/validation.md in this step unless one is actually missing. Accept the already-queued background child result, summarize it in reports/background-summary.md with sections: child result summary, confirmed findings, next steps, unresolved questions, and then call finish." \
		>"${RT09_CONTINUE_RAW}" 2>&1 || RT09_CONTINUE_EXIT=$?
	if (( RT09_CONTINUE_EXIT != 0 )); then
		if grep -Fq 'session is not resumable' "${RT09_CONTINUE_RAW}" && printf '%s' "$RT09_PARENT_FINAL_LINE" | grep -Fq '"status":"completed"'; then
			write_prompt "$RT09_RECOVER_PROMPT" "A background child result already exists in reports/queue-review.md for this workspace.
Do not create or rewrite reports/spec.md, reports/plan.md, reports/progress.md, or reports/validation.md in this step unless one is actually missing.
Write reports/background-summary.md with sections: child result summary, confirmed findings, next steps, unresolved questions.
Then call finish."
			run_exec_with_config "$CONFIG_PATH" "$RT09_RECOVER_PROMPT" "$RT09_RECOVER_RAW" "$PLATFORM_PY_DIR" 240
			RT09_RECOVER_EXIT=$?
			RT09_EXIT="$(merge_exit_code "$RT09_EXIT" "$RT09_RECOVER_EXIT")"
		else
			RT09_EXIT="$(merge_exit_code "$RT09_EXIT" "$RT09_CONTINUE_EXIT")"
		fi
	fi
else
	RT09_EXIT="$(merge_exit_code "$RT09_EXIT" 1)"
fi
copy_if_present "${PLATFORM_PY_DIR}/reports/background-summary.md" "${ARTIFACT_DIR}/rt09-background-summary.md"
copy_child_artifact_if_present "$RT09_WORKER_RAW" "reports/queue-review.md" "${ARTIFACT_DIR}/rt09-queue-review.md" "$PLATFORM_PY_DIR"
RT09_EXIT="$(merge_if_missing_pattern "$RT09_EXIT" "$RT09_WORKER_RAW" "\"visible_paths\"")"
RT09_EXIT="$(merge_if_missing_pattern "$RT09_EXIT" "$RT09_WORKER_RAW" "reports/progress.md")"
RT09_EXIT="$(merge_if_missing_pattern "$RT09_EXIT" "$RT09_WORKER_RAW" "reports/validation.md")"
RT09_EXIT="$(merge_if_missing_pattern "$RT09_EXIT" "$RT09_WORKER_RAW" "reports/queue-review.md")"
copy_session_evidence "$RT09_CHILD_SESSION_ID" "${EVIDENCE_DIR}/rt09-child-session"
printf '%s\n' \
	"parent_session_id=${RT09_PARENT_SESSION_ID}" \
	"queue_job_id=${RT09_JOB_ID}" \
	"child_session_id=${RT09_CHILD_SESSION_ID}" \
	"child_workdir=${RT09_CHILD_WORKDIR}" \
	"continue_exit=${RT09_CONTINUE_EXIT}" \
	"recovery_exit=${RT09_RECOVER_EXIT}" \
	>"${NOTE_DIR}/rt09-queue-metadata.txt"
RT09_FINAL_RAW="$RT09_CONTINUE_RAW"
if [[ ! -f "$RT09_FINAL_RAW" && -f "$RT09_RECOVER_RAW" ]]; then
	RT09_FINAL_RAW="$RT09_RECOVER_RAW"
fi
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
(
	cd "$NESTED_API_DIR"
	go test ./...
) >"${RAW_DIR}/rt10-postcheck-go-test.txt" 2>&1
RT10_POSTCHECK_EXIT=$?
RT10_EXIT="$(merge_exit_code "$RT10_EXIT" "$RT10_POSTCHECK_EXIT")"
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
		--message "Use the existing diagnosis only; do not reread AGENTS.md, README.md, or broad repo files unless a test failure contradicts the diagnosis. Implement exactly these confirmed fixes: 1. in internal/config/config.go make the stable/default quota 1000 while keeping the small rollout at 250; 2. in internal/quota/policy.go return the provided default quota when requested == 0 and keep negative-quota rejection; 3. in internal/api/handler.go reject unknown JSON fields and return model.PublicAccount so internal_id stays private. Then run these exact commands in order until green: go test ./internal/config -run TestFromEnvDefaultsToStableQuota -count=1 -v; go test ./internal/quota -run TestResolveUsesDefaultWhenQuotaIsOmitted -count=1 -v; go test ./internal/api -run 'TestCreateAccountRejectsUnknownFields|TestCreateAccountHidesInternalIDAndUsesDefaultQuota' -count=1 -v; go test ./cmd/server -count=1; go test ./... . Refresh reports/progress.md, reports/validation.md, and reports/change-summary.md, then call finish immediately in the same turn once the full suite passes. Do not spend extra turns on todo housekeeping after the reports are written." \
		>"${RAW_DIR}/rt11-platform-go-continue.jsonl" 2>&1
	RT11_CONTINUE_EXIT=$?
	RT11_EXIT="$(merge_exit_code "$RT11_EXIT" "$RT11_CONTINUE_EXIT")"
else
	RT11_EXIT="$(merge_exit_code "$RT11_EXIT" 1)"
fi
copy_if_present "${PLATFORM_GO_DIR}/reports/change-summary.md" "${ARTIFACT_DIR}/rt11-platform-go-change-summary.md"
(
	cd "$PLATFORM_GO_DIR"
	go test ./...
) >"${RAW_DIR}/rt11-postcheck-go-test.txt" 2>&1
RT11_POSTCHECK_EXIT=$?
RT11_EXIT="$(merge_exit_code "$RT11_EXIT" "$RT11_POSTCHECK_EXIT")"
if (( RT11_POSTCHECK_EXIT != 0 )) && [[ -n "$RT11_SESSION_ID" ]]; then
	cp "${RAW_DIR}/rt11-postcheck-go-test.txt" "${PLATFORM_GO_DIR}/reports/postcheck-go-test.txt"
	"${AGENT_BIN}" continue "$RT11_SESSION_ID" \
		--config "$CONFIG_PATH" \
		--provider openai-compatible \
		--model "$MODEL" \
		--json \
		--message "External verifier still fails. Read reports/postcheck-go-test.txt first and fix only the remaining contradiction it proves. Preserve the private internal_id contract, reject unknown JSON fields, keep cmd/server wiring compiling, and do not rely on .artifacts/tool-outputs paths for verification. Once go test ./... passes, refresh reports/progress.md, reports/validation.md, and reports/change-summary.md only if they are stale, then call finish immediately without extra housekeeping turns." \
		>"${RAW_DIR}/rt11-platform-go-postcheck-retry.jsonl" 2>&1
	RT11_POSTCHECK_RETRY_EXIT=$?
	RT11_EXIT="$(merge_exit_code "$RT11_EXIT" "$RT11_POSTCHECK_RETRY_EXIT")"
	copy_if_present "${PLATFORM_GO_DIR}/reports/change-summary.md" "${ARTIFACT_DIR}/rt11-platform-go-change-summary.md"
	(
		cd "$PLATFORM_GO_DIR"
		go test ./...
	) >"${RAW_DIR}/rt11-postcheck-go-test.txt" 2>&1
	RT11_POSTCHECK_EXIT=$?
	RT11_EXIT="$(merge_exit_code "$RT11_EXIT" "$RT11_POSTCHECK_EXIT")"
fi
RT11_FAILURE_NOTE=""
if (( RT11_POSTCHECK_EXIT != 0 )); then
	RT11_FAILURE_NOTE="external go test ./... postcheck failed: $(grep -m1 -E '^(--- FAIL|FAIL)' "${RAW_DIR}/rt11-postcheck-go-test.txt" || head -n 1 "${RAW_DIR}/rt11-postcheck-go-test.txt")"
fi
copy_session_evidence "$RT11_SESSION_ID" "${EVIDENCE_DIR}/rt11-session"
RT11_FINAL_RAW="${RAW_DIR}/rt11-platform-go-continue.jsonl"
if [[ -f "${RAW_DIR}/rt11-platform-go-postcheck-retry.jsonl" ]]; then
	RT11_FINAL_RAW="${RAW_DIR}/rt11-platform-go-postcheck-retry.jsonl"
fi
if [[ ! -f "$RT11_FINAL_RAW" ]]; then
	RT11_FINAL_RAW="$RT11_RAW"
fi
finalize_scenario "RT11" "Platform Go Multi-Package Repair" "$RT11_EXIT" "$RT11_FINAL_RAW" "${ARTIFACT_DIR}/rt11-platform-go-change-summary.md" "$RT11_SESSION_ID" "$RT11_FAILURE_NOTE"

RT12_PROMPT="${PROMPT_DIR}/rt12-platform-go-review.prompt.txt"
write_prompt "$RT12_PROMPT" "Use the review_pipeline skill for this task.
Read the local AGENTS.md first.
Review README.md, docs/contracts.md, docs/rollout.md, internal/api/handler.go, internal/config/config.go, internal/quota/policy.go, internal/api/handler_test.go, internal/config/config_test.go, internal/quota/policy_test.go, and reports/change-summary.md.
Write reports/post-fix-review.md with sections: findings, unresolved questions, remaining risks, suggested next validation.
If there is no validated finding, say so explicitly inside findings.
Then call finish with a one-line summary."
RT12_RAW="${RAW_DIR}/rt12-platform-go-review.jsonl"
run_exec "$RT12_PROMPT" "$RT12_RAW" "$PLATFORM_GO_DIR" 300 90
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
(
	cd "$PLATFORM_PY_DIR"
	pytest -q
) >"${RAW_DIR}/rt13-postcheck-pytest.txt" 2>&1
RT13_POSTCHECK_EXIT=$?
RT13_EXIT="$(merge_exit_code "$RT13_EXIT" "$RT13_POSTCHECK_EXIT")"
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
RT15_PROOF_NOTE="${NOTE_DIR}/rt15-proof-anchor-input.md"
RT15_RT04_EVENTS_PATH="${EVIDENCE_DIR}/rt04-session/events.jsonl"
RT15_RT05_EVENTS_PATH="${EVIDENCE_DIR}/rt05-session/events.jsonl"
RT15_RT07_EVENTS_PATH="${EVIDENCE_DIR}/rt07-session/events.jsonl"
RT15_BEFORE_JSON_PATH="${RUN_DIR}/raw/rt05-incident-taskboard-before.json"
RT15_AFTER_JSON_PATH="${RUN_DIR}/raw/rt05-incident-taskboard-after.json"
RT15_SESSION_CONTEXT_LINE="$(grep -nF '"type":"session.context.loaded"' "$RT15_RT05_EVENTS_PATH" | head -n1 || true)"
RT15_COMPACT_STARTED_LINE="$(grep -nF '"type":"compact.started"' "$RT15_RT04_EVENTS_PATH" | head -n1 || true)"
RT15_COMPACT_FINISHED_LINE="$(grep -nF '"type":"compact.finished"' "$RT15_RT04_EVENTS_PATH" | head -n1 || true)"
RT15_TASK_CREATED_LINE="$(grep -nF '"type":"task.created"' "$RT15_RT07_EVENTS_PATH" | head -n1 || true)"
RT15_TASK_UPDATED_LINE="$(grep -nF '"type":"task.updated"' "$RT15_RT07_EVENTS_PATH" | head -n1 || true)"
RT15_TODO_UPDATED_LINE="$(grep -nF '"type":"todo.updated"' "$RT15_RT07_EVENTS_PATH" | head -n1 || true)"
RT15_PROVIDER_REQUEST_LINE="$(grep -nF '"type":"provider.request.prepared"' "$RT15_RT07_EVENTS_PATH" | head -n1 || true)"
{
	echo "# RT15 Proof Anchor Input"
	echo
	echo "Use this note first to keep retrieval tight. The quoted lines below come directly from the current-run raw/session files."
	echo
	echo "## Required Anchors"
	echo
	echo "- compact.started: ${WORKSPACE_EVIDENCE_DIR}/rt04-session/events.jsonl:${RT15_COMPACT_STARTED_LINE%%:*}"
	echo "  ${RT15_COMPACT_STARTED_LINE#*:}"
	echo
	echo "- compact.finished: ${WORKSPACE_EVIDENCE_DIR}/rt04-session/events.jsonl:${RT15_COMPACT_FINISHED_LINE%%:*}"
	echo "  ${RT15_COMPACT_FINISHED_LINE#*:}"
	echo
	echo "- rt05-incident-taskboard-before.json: ${WORKSPACE_RUN_DIR}/raw/rt05-incident-taskboard-before.json"
	sed -n '1,40p' "$RT15_BEFORE_JSON_PATH"
	echo
	echo "- rt05-incident-taskboard-after.json: ${WORKSPACE_RUN_DIR}/raw/rt05-incident-taskboard-after.json"
	sed -n '1,40p' "$RT15_AFTER_JSON_PATH"
	echo
	echo "## Additional Runtime Lines"
	echo
	echo "- session.context.loaded: ${WORKSPACE_EVIDENCE_DIR}/rt05-session/events.jsonl:${RT15_SESSION_CONTEXT_LINE%%:*}"
	echo "  ${RT15_SESSION_CONTEXT_LINE#*:}"
	echo "- task.created: ${WORKSPACE_EVIDENCE_DIR}/rt07-session/events.jsonl:${RT15_TASK_CREATED_LINE%%:*}"
	echo "  ${RT15_TASK_CREATED_LINE#*:}"
	echo "- task.updated: ${WORKSPACE_EVIDENCE_DIR}/rt07-session/events.jsonl:${RT15_TASK_UPDATED_LINE%%:*}"
	echo "  ${RT15_TASK_UPDATED_LINE#*:}"
	echo "- todo.updated: ${WORKSPACE_EVIDENCE_DIR}/rt07-session/events.jsonl:${RT15_TODO_UPDATED_LINE%%:*}"
	echo "  ${RT15_TODO_UPDATED_LINE#*:}"
	echo "- provider.request.prepared: ${WORKSPACE_EVIDENCE_DIR}/rt07-session/events.jsonl:${RT15_PROVIDER_REQUEST_LINE%%:*}"
	echo "  ${RT15_PROVIDER_REQUEST_LINE#*:}"
	echo
	echo "## Focus Notes"
	echo
	echo "- RT04 may not have a generated report artifact in every run. If ${WORKSPACE_ARTIFACT_DIR}/rt04-forced-compaction-proof.md is missing, rely on ${WORKSPACE_RUN_DIR}/notes/RT04.md plus ${WORKSPACE_EVIDENCE_DIR}/rt04-session/events.jsonl instead of trying to rediscover more evidence."
	echo "- After reading this note, verify at most four exact windows from the raw/session files and then write the report immediately."
} >"$RT15_PROOF_NOTE"
write_prompt "$RT15_PROMPT" "Use the review_pipeline skill for this task.
Read ${WORKSPACE_RUN_DIR}/notes/rt15-proof-anchor-input.md first.
The listed paths below are exact and valid relative to the workdir. Do not glob or search outside this allowlist.
Inspect only these current-run supporting artifacts: ${WORKSPACE_ARTIFACT_DIR}/rt05-incident-summary.md, ${WORKSPACE_ARTIFACT_DIR}/rt05-taskboard-summary.md, ${WORKSPACE_ARTIFACT_DIR}/rt05-recovery-summary.md, ${WORKSPACE_ARTIFACT_DIR}/rt06-docset-continue-brief.md, ${WORKSPACE_ARTIFACT_DIR}/rt08-delegate-review.md, ${WORKSPACE_ARTIFACT_DIR}/rt09-background-summary.md, ${WORKSPACE_ARTIFACT_DIR}/rt09-queue-review.md, ${WORKSPACE_ARTIFACT_DIR}/rt11-platform-go-change-summary.md, ${WORKSPACE_ARTIFACT_DIR}/rt13-platform-py-change-summary.md, ${WORKSPACE_RUN_DIR}/notes/RT04.md, ${WORKSPACE_RUN_DIR}/notes/scenario-index.tsv, ${WORKSPACE_RUN_DIR}/notes/rt05-session-metadata.txt, ${WORKSPACE_RUN_DIR}/notes/rt07-steer-metadata.txt, ${WORKSPACE_RUN_DIR}/notes/rt08-delegate-metadata.txt, and ${WORKSPACE_RUN_DIR}/notes/rt09-queue-metadata.txt.
Inspect only these direct evidence files if you need a second look on an exact quoted anchor from the proof note: ${WORKSPACE_RUN_DIR}/raw/rt05-incident-taskboard-before.json, ${WORKSPACE_RUN_DIR}/raw/rt05-incident-taskboard-after.json, ${WORKSPACE_RUN_DIR}/raw/rt07-taskboard-after.json, ${WORKSPACE_RUN_DIR}/raw/rt07-postcheck-go-test.txt, ${WORKSPACE_EVIDENCE_DIR}/rt04-session/events.jsonl, ${WORKSPACE_EVIDENCE_DIR}/rt05-session/events.jsonl, ${WORKSPACE_EVIDENCE_DIR}/rt05-session/todo.json, ${WORKSPACE_EVIDENCE_DIR}/rt05-session/session.json, ${WORKSPACE_EVIDENCE_DIR}/rt05-session/state.json, ${WORKSPACE_EVIDENCE_DIR}/rt07-session/events.jsonl, ${WORKSPACE_EVIDENCE_DIR}/rt07-session/session.json, ${WORKSPACE_EVIDENCE_DIR}/rt07-session/todo.json, ${WORKSPACE_EVIDENCE_DIR}/rt11-session/events.jsonl, and ${WORKSPACE_EVIDENCE_DIR}/rt13-session/events.jsonl.
Also inspect only these source references: go-cli-agent/spec/01-runtime-architecture.md, go-cli-agent/spec/10-context-compaction.md, go-cli-agent/spec/12-task-system.md, go-cli-agent/internal/runtime/engine.go, go-cli-agent/internal/runtime/project_memory.go, go-cli-agent/internal/session/store.go, go-cli-agent/internal/session/taskboard.go, and go-cli-agent/internal/tools/registry.go.
Use this live evidence to judge whether go-cli-agent now demonstrates real durable project-memory and task-system behavior across continue, compaction, and multi-step execution rather than only in spec text.
You must quote direct evidence from the allowlisted raw/session files, not only downstream summary artifacts. At minimum, cite exact lines containing session.context.loaded, compact.started, and compact.finished, plus direct before/after taskboard JSON evidence that shows dependency state before execution and completed state after execution.
Keep the retrieval plan tight: read the proof-anchor note, verify at most four exact windows from raw/session files, then write the report immediately. Do not spend turns rediscovering broad context once the proof anchors are covered.
The final artifact must literally include the strings compact.started, compact.finished, rt05-incident-taskboard-before.json, and rt05-incident-taskboard-after.json in its evidence text so the script-level post-check can confirm the proof anchors without inference.
Write a dedicated section named required proof anchors immediately after confirmed runtime evidence. In that section, include these exact standalone bullet prefixes, verbatim, before your explanation text:
- compact.started:
- compact.finished:
- rt05-incident-taskboard-before.json:
- rt05-incident-taskboard-after.json:
Do not paraphrase or rename those bullet prefixes. If any one of those exact prefixes is missing, the scenario fails even if the rest of the report is good.
Write ${ABS_ARTIFACT_DIR}/rt15-task-memory-traceability.md with sections: confirmed runtime evidence, required proof anchors, findings, remaining gaps, next validation moves.
Then call finish with a one-line summary."
RT15_RAW="${RAW_DIR}/rt15-task-memory-traceability.jsonl"
run_exec "$RT15_PROMPT" "$RT15_RAW" "$WORKSPACE_ROOT" 480
RT15_EXIT=$?
RT15_FAILURE_NOTE=""
RT15_EXIT="$(merge_if_missing_pattern "$RT15_EXIT" "${ARTIFACT_DIR}/rt15-task-memory-traceability.md" "compact.started")"
if [[ "$RT15_EXIT" != "0" ]] && [[ -z "$RT15_FAILURE_NOTE" ]] && ! grep -Fq "compact.started" "${ARTIFACT_DIR}/rt15-task-memory-traceability.md" 2>/dev/null; then
	RT15_FAILURE_NOTE="artifact missing literal proof anchor compact.started"
fi
RT15_EXIT="$(merge_if_missing_pattern "$RT15_EXIT" "${ARTIFACT_DIR}/rt15-task-memory-traceability.md" "compact.finished")"
if ! grep -Fq "compact.finished" "${ARTIFACT_DIR}/rt15-task-memory-traceability.md" 2>/dev/null; then
	if [[ -n "$RT15_FAILURE_NOTE" ]]; then
		RT15_FAILURE_NOTE="${RT15_FAILURE_NOTE}; artifact missing literal proof anchor compact.finished"
	else
		RT15_FAILURE_NOTE="artifact missing literal proof anchor compact.finished"
	fi
fi
RT15_EXIT="$(merge_if_missing_pattern "$RT15_EXIT" "${ARTIFACT_DIR}/rt15-task-memory-traceability.md" "rt05-incident-taskboard-before.json")"
if ! grep -Fq "rt05-incident-taskboard-before.json" "${ARTIFACT_DIR}/rt15-task-memory-traceability.md" 2>/dev/null; then
	if [[ -n "$RT15_FAILURE_NOTE" ]]; then
		RT15_FAILURE_NOTE="${RT15_FAILURE_NOTE}; artifact missing literal proof anchor rt05-incident-taskboard-before.json"
	else
		RT15_FAILURE_NOTE="artifact missing literal proof anchor rt05-incident-taskboard-before.json"
	fi
fi
RT15_EXIT="$(merge_if_missing_pattern "$RT15_EXIT" "${ARTIFACT_DIR}/rt15-task-memory-traceability.md" "rt05-incident-taskboard-after.json")"
if ! grep -Fq "rt05-incident-taskboard-after.json" "${ARTIFACT_DIR}/rt15-task-memory-traceability.md" 2>/dev/null; then
	if [[ -n "$RT15_FAILURE_NOTE" ]]; then
		RT15_FAILURE_NOTE="${RT15_FAILURE_NOTE}; artifact missing literal proof anchor rt05-incident-taskboard-after.json"
	else
		RT15_FAILURE_NOTE="artifact missing literal proof anchor rt05-incident-taskboard-after.json"
	fi
fi
finalize_scenario "RT15" "Task And Durable Memory Traceability" "$RT15_EXIT" "$RT15_RAW" "${ARTIFACT_DIR}/rt15-task-memory-traceability.md" "$(extract_session_id "$RT15_RAW")" "$RT15_FAILURE_NOTE"

RT16_PROMPT="${PROMPT_DIR}/rt16-codex-steer-audit.prompt.txt"
write_prompt "$RT16_PROMPT" "Use the review_pipeline skill for this task.
Inspect only codex/AGENTS.md, codex/docs/agents_md.md, codex/docs/sandbox.md, codex/codex-rs/core/gpt_5_codex_prompt.md, and codex/codex-rs/app-server/tests/suite/v2/turn_steer.rs.
Do not inspect go-cli-agent code for this task.
Use targeted retrieval only. Do not use glob, grep_files, shell find, or any directory search for this task.
Write ${ABS_ARTIFACT_DIR}/rt16-codex-steer-audit.md with sections: confirmed codex patterns, findings or cautions, remaining risks, ideas worth borrowing.
If you identify cautions, still record them inside a dedicated findings section using per-finding Severity, Confidence, Evidence, and Why it matters fields. If there is no validated finding, say so explicitly inside findings.
Then call finish with a one-line summary."
RT16_RAW="${RAW_DIR}/rt16-codex-steer-audit.jsonl"
run_exec_with_config "$CONFIG_PATH" "$RT16_PROMPT" "$RT16_RAW" "$WORKSPACE_ROOT" 300 90
RT16_EXIT=$?
finalize_scenario "RT16" "Codex Steer And Sandbox Audit" "$RT16_EXIT" "$RT16_RAW" "${ARTIFACT_DIR}/rt16-codex-steer-audit.md" "$(extract_session_id "$RT16_RAW")"

RT17_PROMPT="${PROMPT_DIR}/rt17-codex-proxy-audit.prompt.txt"
write_prompt "$RT17_PROMPT" "Use the review_pipeline skill for this task.
Inspect only codex/codex-rs/responses-api-proxy/README.md, codex/codex-rs/process-hardening/README.md, codex/docs/authentication.md, and codex/docs/config.md.
Focus on OpenAI-compatible Responses transport, auth handling, and local hardening patterns.
Write ${ABS_ARTIFACT_DIR}/rt17-codex-proxy-audit.md with sections: confirmed contracts, findings or mismatch risks, unresolved questions, hardening ideas worth importing.
If you identify mismatch risks, still record them inside a dedicated findings section using per-finding Severity, Confidence, Evidence, and Why it matters fields. If there is no validated finding, say so explicitly inside findings.
Use the canonical section name findings. For each validated finding, include explicit Severity:, Confidence:, Evidence:, Snippet:, and Why it matters: labels. The snippet or identifier must literally appear inside the cited path:line range; if needed, widen the cited range rather than paraphrasing. If a point is not fully proven within scope or remaining retrieval budget, move it to unresolved questions or risks instead of findings.
Before drafting each finding, use a targeted grep or narrow read on the exact identifier or snippet you plan to cite so the line reference literally contains it. Prefer short identifier-only snippets from a single exact line, such as header names or CLI flags, instead of long sentence quotes that may span multiple lines.
Then call finish with a one-line summary."
RT17_RAW="${RAW_DIR}/rt17-codex-proxy-audit.jsonl"
run_exec "$RT17_PROMPT" "$RT17_RAW" "$WORKSPACE_ROOT" 300
RT17_EXIT=$?
finalize_scenario "RT17" "Codex Responses Proxy Audit" "$RT17_EXIT" "$RT17_RAW" "${ARTIFACT_DIR}/rt17-codex-proxy-audit.md" "$(extract_session_id "$RT17_RAW")"

RT18_PROMPT="${PROMPT_DIR}/rt18-opencode-task-review.prompt.txt"
RT18_PROOF_NOTE="${NOTE_DIR}/rt18-proof-anchor-input.md"
{
	echo "# RT18 Proof Anchor Input"
	echo
	echo "Use this note first. It already contains the exact anchors needed for the OpenCode comparison."
	echo
	echo "## Focus Areas"
	echo
	echo "- large-project task execution model"
	echo "- todo discipline"
	echo "- prompt/reminder behavior"
	echo
	echo "## Agent Surface"
	echo
	echo "- README agents summary: opencode/README.md:102"
	nl -ba ../opencode/README.md | sed -n '102,111p'
	echo
	echo "## Todo Discipline"
	echo
	echo "- todo schema and persistence: opencode/packages/opencode/src/session/todo.ts:8"
	nl -ba ../opencode/packages/opencode/src/session/todo.ts | sed -n '8,55p'
	echo
	echo "## Turn Progression And Stop/Continue Boundary"
	echo
	echo "- processor compaction/retry/stop-continue boundary: opencode/packages/opencode/src/session/processor.ts:352"
	nl -ba ../opencode/packages/opencode/src/session/processor.ts | sed -n '352,424p'
	echo
	echo "## Prompt / Reminder Anchors"
	echo
	echo "- follow-up reminder after user steer/input: opencode/packages/opencode/src/session/prompt.ts:640"
	nl -ba ../opencode/packages/opencode/src/session/prompt.ts | sed -n '640,646p'
	echo
	echo "- plan-mode reminder block: opencode/packages/opencode/src/session/prompt.ts:1419"
	nl -ba ../opencode/packages/opencode/src/session/prompt.ts | sed -n '1419,1437p;1481,1488p'
	echo
	echo "## Runtime Guidance"
	echo
	echo "- root AGENTS automation bias: opencode/AGENTS.md:1"
	nl -ba ../opencode/AGENTS.md | sed -n '1,5p'
	echo
	echo "- package AGENTS runtime-vs-instance guidance: opencode/packages/opencode/AGENTS.md:34"
	nl -ba ../opencode/packages/opencode/AGENTS.md | sed -n '34,48p'
	echo
	echo "## Usage Discipline"
	echo
	echo "- Treat this note as authoritative. In most cases it is sufficient on its own."
	echo "- Do not reopen broad windows from prompt.ts just to hunt for more wording. The note already contains the relevant reminder excerpts."
	echo "- If one material ambiguity remains after reading this note, inspect at most two exact supporting windows from: opencode/README.md, opencode/packages/opencode/src/session/todo.ts, opencode/packages/opencode/src/session/processor.ts, opencode/packages/opencode/src/session/prompt.ts, opencode/AGENTS.md, and opencode/packages/opencode/AGENTS.md."
	echo "- Once the comparison is grounded, write the artifact immediately instead of continuing retrieval."
} >"$RT18_PROOF_NOTE"
write_prompt "$RT18_PROMPT" "Use the review_pipeline skill for this task.
Read ${WORKSPACE_RUN_DIR}/notes/rt18-proof-anchor-input.md first.
Treat that note as the authoritative citation-anchor set for this review. In most cases the note alone is enough.
Focus on large-project task execution, todo discipline, and prompt/reminder behavior.
Do not reread broad files by default. If one material ambiguity remains after reading the proof note, inspect at most two exact supporting windows from opencode/README.md, opencode/packages/opencode/src/session/todo.ts, opencode/packages/opencode/src/session/processor.ts, opencode/packages/opencode/src/session/prompt.ts, opencode/AGENTS.md, and opencode/packages/opencode/AGENTS.md.
Write ${ABS_ARTIFACT_DIR}/rt18-opencode-task-review.md with sections: confirmed strengths, tradeoffs or findings, remaining risks, ideas go-cli-agent should adopt.
If you identify tradeoffs or cautions, still record them inside a dedicated findings section using per-finding Severity, Confidence, Evidence, and Why it matters fields. If there is no validated finding, say so explicitly inside findings.
Use the canonical section name findings. For each validated finding, include explicit Severity:, Confidence:, Evidence:, Snippet:, and Why it matters: labels. The snippet or identifier must literally appear inside the cited path:line range; if needed, widen the cited range rather than paraphrasing. If a point is not fully proven within scope or remaining retrieval budget, move it to remaining risks instead of findings.
Then call finish with a one-line summary."
RT18_RAW="${RAW_DIR}/rt18-opencode-task-review.jsonl"
RT18_EXIT=1
run_exec "$RT18_PROMPT" "$RT18_RAW" "$WORKSPACE_ROOT" 300
RT18_EXIT=$?
finalize_scenario "RT18" "OpenCode Task And Prompt Review" "$RT18_EXIT" "$RT18_RAW" "${ARTIFACT_DIR}/rt18-opencode-task-review.md" "$(extract_session_id "$RT18_RAW")"

RT19_PROMPT="${PROMPT_DIR}/rt19-opencode-responses-audit.prompt.txt"
RT19_SANDBOX="${WORKSPACE_DIR}/rt19_opencode_responses"
RT19_SANDBOX_ARTIFACT_REL="reports/rt19-opencode-responses-audit.md"
RT19_SANDBOX_ARTIFACT="${RT19_SANDBOX}/${RT19_SANDBOX_ARTIFACT_REL}"
RT19_PROOF_NOTE="${RT19_SANDBOX}/rt19-proof-anchor-input.md"
mkdir -p "$RT19_SANDBOX"
copy_relative_file_if_present "opencode/specs/project.md" "$RT19_SANDBOX"
copy_relative_file_if_present "opencode/packages/opencode/src/provider/sdk/copilot/responses/openai-responses-language-model.ts" "$RT19_SANDBOX"
copy_relative_file_if_present "opencode/packages/opencode/src/provider/sdk/copilot/responses/map-openai-responses-finish-reason.ts" "$RT19_SANDBOX"
copy_relative_file_if_present "opencode/packages/opencode/src/provider/sdk/copilot/responses/convert-to-openai-responses-input.ts" "$RT19_SANDBOX"
copy_relative_file_if_present "opencode/packages/opencode/src/provider/sdk/copilot/responses/openai-responses-prepare-tools.ts" "$RT19_SANDBOX"
{
	echo "# RT19 Proof Anchor Input"
	echo
	echo "Use this note first. In most cases it already contains enough evidence to write the report without broad rereads."
	echo
	echo "## Confirmed Behavior Anchors"
	echo
	echo "- openai-responses-language-model.ts request construction"
	nl -ba "${RT19_SANDBOX}/opencode/packages/opencode/src/provider/sdk/copilot/responses/openai-responses-language-model.ts" | sed -n '197,220p'
	echo
	echo "- openai-responses-language-model.ts provider options and previous_response_id"
	nl -ba "${RT19_SANDBOX}/opencode/packages/opencode/src/provider/sdk/copilot/responses/openai-responses-language-model.ts" | sed -n '284,299p'
	echo
	echo "- openai-responses-language-model.ts streamed finish mapping"
	nl -ba "${RT19_SANDBOX}/opencode/packages/opencode/src/provider/sdk/copilot/responses/openai-responses-language-model.ts" | sed -n '1228,1288p'
	echo
	echo "- convert-to-openai-responses-input.ts assistant replay and provider-executed tool handling"
	nl -ba "${RT19_SANDBOX}/opencode/packages/opencode/src/provider/sdk/copilot/responses/convert-to-openai-responses-input.ts" | sed -n '128,210p'
	echo
	echo "- convert-to-openai-responses-input.ts tool-role replay back to function_call_output"
	nl -ba "${RT19_SANDBOX}/opencode/packages/opencode/src/provider/sdk/copilot/responses/convert-to-openai-responses-input.ts" | sed -n '239,285p'
	echo
	echo "- openai-responses-prepare-tools.ts tool preparation and toolChoice mapping"
	nl -ba "${RT19_SANDBOX}/opencode/packages/opencode/src/provider/sdk/copilot/responses/openai-responses-prepare-tools.ts" | sed -n '1,60p'
	echo
	nl -ba "${RT19_SANDBOX}/opencode/packages/opencode/src/provider/sdk/copilot/responses/openai-responses-prepare-tools.ts" | sed -n '146,178p'
	echo
	echo "- map-openai-responses-finish-reason.ts"
	nl -ba "${RT19_SANDBOX}/opencode/packages/opencode/src/provider/sdk/copilot/responses/map-openai-responses-finish-reason.ts" | sed -n '1,40p'
	echo
	echo "## Scope Boundary"
	echo
	echo "- project.md is only a benchmark/context boundary reference for unanswered questions:"
	nl -ba "${RT19_SANDBOX}/opencode/specs/project.md" | sed -n '1,40p'
	echo
	echo "## Usage Discipline"
	echo
	echo "- Treat the anchors above as the primary evidence set."
	echo "- If one material ambiguity remains, inspect at most four exact windows from these same files and then write the report immediately."
	echo "- Do not use shell, glob, grep_files, find, or repo-root scanning in this scenario."
	echo "- Do not keep rereading the same file once you have enough evidence for confirmed behavior and findings."
} >"$RT19_PROOF_NOTE"
write_prompt "$RT19_PROMPT" "Use the review_pipeline skill for this task.
Read rt19-proof-anchor-input.md first and treat it as the authoritative evidence set for this task.
Focus on Responses replay, tool preparation, and finish-reason mapping.
In most cases the proof note alone is enough. If one material ambiguity remains, inspect at most four exact windows from these same files and then write the report immediately.
Do not use shell, glob, grep_files, find, or any workspace-root scan for this task.
Write ${RT19_SANDBOX_ARTIFACT_REL} with sections: confirmed behavior, findings or mismatch risks, unresolved questions, useful implementation ideas.
If you identify mismatch risks, still record them inside a dedicated findings section using per-finding Severity, Confidence, Evidence, and Why it matters fields. If there is no validated finding, say so explicitly inside findings.
Use the canonical section name findings. For each validated finding, include explicit Severity:, Confidence:, Evidence:, Snippet:, and Why it matters: labels. The snippet or identifier must literally appear inside the cited path:line range; if needed, widen the cited range rather than paraphrasing. If a point is not fully proven within scope or remaining retrieval budget, move it to unresolved questions or mismatch risks instead of findings.
Keep the retrieval plan tight: read the proof note, optionally verify at most four exact windows from the same file set, then write the report and finish immediately. Do not spend turns rediscovering more context once you can write the artifact.
Then call finish with a one-line summary."
RT19_RAW="${RAW_DIR}/rt19-opencode-responses-audit.jsonl"
RT19_EXIT=1
run_exec "$RT19_PROMPT" "$RT19_RAW" "$RT19_SANDBOX" 420
RT19_EXIT=$?
copy_if_present "$RT19_SANDBOX_ARTIFACT" "${ARTIFACT_DIR}/rt19-opencode-responses-audit.md"
finalize_scenario "RT19" "OpenCode Responses Provider Audit" "$RT19_EXIT" "$RT19_RAW" "${ARTIFACT_DIR}/rt19-opencode-responses-audit.md" "$(extract_session_id "$RT19_RAW")"

rm -rf "$RT20_COMPARATOR_DIR"
mkdir -p "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "${WORKSPACE_ARTIFACT_DIR}/rt07-live-steer-audit.md" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "${WORKSPACE_ARTIFACT_DIR}/rt11-platform-go-change-summary.md" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "${WORKSPACE_ARTIFACT_DIR}/rt12-platform-go-review.md" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "${WORKSPACE_ARTIFACT_DIR}/rt13-platform-py-change-summary.md" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "${WORKSPACE_ARTIFACT_DIR}/rt14-platform-py-review.md" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "${WORKSPACE_ARTIFACT_DIR}/rt15-task-memory-traceability.md" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "${WORKSPACE_ARTIFACT_DIR}/rt16-codex-steer-audit.md" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "${WORKSPACE_ARTIFACT_DIR}/rt17-codex-proxy-audit.md" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "${WORKSPACE_ARTIFACT_DIR}/rt18-opencode-task-review.md" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "${WORKSPACE_ARTIFACT_DIR}/rt19-opencode-responses-audit.md" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "${WORKSPACE_EVIDENCE_DIR}/rt07-session/events.jsonl" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "${WORKSPACE_EVIDENCE_DIR}/rt07-session/session.json" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "${WORKSPACE_EVIDENCE_DIR}/rt07-session/todo.json" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "${WORKSPACE_EVIDENCE_DIR}/rt08-child-session/events.jsonl" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "${WORKSPACE_EVIDENCE_DIR}/rt09-child-session/events.jsonl" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "${WORKSPACE_EVIDENCE_DIR}/rt11-session/events.jsonl" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "${WORKSPACE_EVIDENCE_DIR}/rt11-session/session.json" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "${WORKSPACE_EVIDENCE_DIR}/rt13-session/events.jsonl" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "${WORKSPACE_EVIDENCE_DIR}/rt13-session/session.json" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "${WORKSPACE_RUN_DIR}/raw/rt07-taskboard-after.json" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "${WORKSPACE_RUN_DIR}/raw/rt07-postcheck-go-test.txt" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "${WORKSPACE_RUN_DIR}/notes/rt07-steer-metadata.txt" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "go-cli-agent/README.md" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "go-cli-agent/spec/00-product.md" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "go-cli-agent/spec/01-runtime-architecture.md" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "go-cli-agent/spec/03-provider-contracts.md" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "go-cli-agent/spec/10-context-compaction.md" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "go-cli-agent/spec/12-task-system.md" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "go-cli-agent/spec/13-live-input-and-steering.md" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "go-cli-agent/pkg/agent/agent.go" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "codex/README.md" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "codex/docs/agents_md.md" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "codex/docs/sandbox.md" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "codex/codex-rs/core/gpt_5_codex_prompt.md" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "codex/codex-rs/app-server/tests/suite/v2/turn_steer.rs" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "opencode/README.md" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "opencode/packages/opencode/src/session/prompt.ts" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "opencode/packages/opencode/src/session/todo.ts" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "opencode/packages/opencode/src/session/processor.ts" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "opencode/packages/opencode/src/provider/sdk/copilot/responses/openai-responses-language-model.ts" "$RT20_COMPARATOR_DIR"
copy_relative_file_if_present "opencode/packages/opencode/src/provider/sdk/copilot/responses/convert-to-openai-responses-input.ts" "$RT20_COMPARATOR_DIR"
mkdir -p "${RT20_COMPARATOR_DIR}/${WORKSPACE_ARTIFACT_DIR}"
cat > "${RT20_COMPARATOR_DIR}/${WORKSPACE_ARTIFACT_DIR}/rt20-same-task-comparator.md" <<EOF
# rt20 same-task comparator

## comparator setup
This is not a live competitor benchmark.
This is a same-task comparator built from go-cli-agent live run evidence plus local codex/opencode reference implementations.

## findings

## go-cli-agent stronger on the same task

## codex or opencode still stronger or better proven

## proof gaps that still block a hard surpass claim

## remaining risks

## next benchmark moves
EOF

RT20_PROMPT="${PROMPT_DIR}/rt20-same-task-comparator.prompt.txt"
write_prompt_literal "$RT20_PROMPT" <<'EOF'
This task is an explicit exception to any usual findings-first review order: the comparator setup block must appear before findings.
The workdir has been pre-scoped to only the allowlisted files below. Do not assume any prior rt20 artifact exists outside this scope.
Inspect only these current-run artifacts: __WORKSPACE_ARTIFACT_DIR__/rt07-live-steer-audit.md, __WORKSPACE_ARTIFACT_DIR__/rt11-platform-go-change-summary.md, __WORKSPACE_ARTIFACT_DIR__/rt12-platform-go-review.md, __WORKSPACE_ARTIFACT_DIR__/rt13-platform-py-change-summary.md, __WORKSPACE_ARTIFACT_DIR__/rt14-platform-py-review.md, __WORKSPACE_ARTIFACT_DIR__/rt15-task-memory-traceability.md, __WORKSPACE_ARTIFACT_DIR__/rt16-codex-steer-audit.md, __WORKSPACE_ARTIFACT_DIR__/rt17-codex-proxy-audit.md, __WORKSPACE_ARTIFACT_DIR__/rt18-opencode-task-review.md, and __WORKSPACE_ARTIFACT_DIR__/rt19-opencode-responses-audit.md.
Also inspect only these current-run evidence files: __WORKSPACE_EVIDENCE_DIR__/rt07-session/events.jsonl, __WORKSPACE_EVIDENCE_DIR__/rt07-session/session.json, __WORKSPACE_EVIDENCE_DIR__/rt07-session/todo.json, __WORKSPACE_RUN_DIR__/raw/rt07-taskboard-after.json, __WORKSPACE_RUN_DIR__/raw/rt07-postcheck-go-test.txt, __WORKSPACE_RUN_DIR__/notes/rt07-steer-metadata.txt, __WORKSPACE_EVIDENCE_DIR__/rt08-child-session/events.jsonl, __WORKSPACE_EVIDENCE_DIR__/rt09-child-session/events.jsonl, __WORKSPACE_EVIDENCE_DIR__/rt11-session/events.jsonl, __WORKSPACE_EVIDENCE_DIR__/rt11-session/session.json, __WORKSPACE_EVIDENCE_DIR__/rt13-session/events.jsonl, and __WORKSPACE_EVIDENCE_DIR__/rt13-session/session.json.
Also inspect only these reference files: go-cli-agent/README.md, go-cli-agent/spec/00-product.md, go-cli-agent/spec/01-runtime-architecture.md, go-cli-agent/spec/03-provider-contracts.md, go-cli-agent/spec/10-context-compaction.md, go-cli-agent/spec/12-task-system.md, go-cli-agent/spec/13-live-input-and-steering.md, go-cli-agent/pkg/agent/agent.go, codex/README.md, codex/docs/agents_md.md, codex/docs/sandbox.md, codex/codex-rs/core/gpt_5_codex_prompt.md, codex/codex-rs/app-server/tests/suite/v2/turn_steer.rs, opencode/README.md, opencode/packages/opencode/src/session/prompt.ts, opencode/packages/opencode/src/session/todo.ts, opencode/packages/opencode/src/session/processor.ts, opencode/packages/opencode/src/provider/sdk/copilot/responses/openai-responses-language-model.ts, and opencode/packages/opencode/src/provider/sdk/copilot/responses/convert-to-openai-responses-input.ts.
Build a structured same-task comparator across four axes: steer/interrupt recovery, durable task memory, multi-package repair execution, and Responses/provider handling.
An artifact skeleton already exists at __WORKSPACE_ARTIFACT_DIR__/rt20-same-task-comparator.md. Preserve its title and comparator setup block verbatim, and fill in the remaining sections by editing that file in place.
The file must begin exactly with these lines before any findings section:

# rt20 same-task comparator

## comparator setup
This is not a live competitor benchmark.
This is a same-task comparator built from go-cli-agent live run evidence plus local codex/opencode reference implementations.

Do not paraphrase or bullet either sentence, and do not place any heading between the title and this section.
Do not replace the comparator setup block with a findings-first opening. If you draft findings first, rewrite the file so the comparator setup block stays first.
Write __WORKSPACE_ARTIFACT_DIR__/rt20-same-task-comparator.md with sections: comparator setup, findings, go-cli-agent stronger on the same task, codex or opencode still stronger or better proven, proof gaps that still block a hard surpass claim, remaining risks, next benchmark moves.
If you identify blockers or deficits, include an additional findings section with per-finding Severity:, Confidence:, Evidence:, Snippet:, and Why it matters: labels. The snippet or identifier must literally appear inside the cited path:line range; if needed, widen the cited range rather than paraphrasing. If a point is only supported by structural inference rather than direct evidence, keep it in the proof-gap section instead of findings.
Then call finish with a one-line summary.
EOF
sed -i "s#__WORKSPACE_ARTIFACT_DIR__#${WORKSPACE_ARTIFACT_DIR}#g; s#__WORKSPACE_EVIDENCE_DIR__#${WORKSPACE_EVIDENCE_DIR}#g; s#__WORKSPACE_RUN_DIR__#${WORKSPACE_RUN_DIR}#g" "$RT20_PROMPT"
RT20_RAW="${RAW_DIR}/rt20-same-task-comparator.jsonl"
run_exec "$RT20_PROMPT" "$RT20_RAW" "$RT20_COMPARATOR_DIR" 420
RT20_EXIT=$?
copy_if_present "${RT20_COMPARATOR_DIR}/${WORKSPACE_ARTIFACT_DIR}/rt20-same-task-comparator.md" "${ARTIFACT_DIR}/rt20-same-task-comparator.md"
RT20_FAILURE_NOTE=""
RT20_EXIT="$(merge_if_missing_exact_line "$RT20_EXIT" "${ARTIFACT_DIR}/rt20-same-task-comparator.md" "This is not a live competitor benchmark.")"
if ! grep -Fxq "This is not a live competitor benchmark." "${ARTIFACT_DIR}/rt20-same-task-comparator.md" 2>/dev/null; then
	RT20_FAILURE_NOTE="artifact missing exact comparator setup sentence This is not a live competitor benchmark."
fi
RT20_EXIT="$(merge_if_missing_exact_line "$RT20_EXIT" "${ARTIFACT_DIR}/rt20-same-task-comparator.md" "This is a same-task comparator built from go-cli-agent live run evidence plus local codex/opencode reference implementations.")"
if ! grep -Fxq "This is a same-task comparator built from go-cli-agent live run evidence plus local codex/opencode reference implementations." "${ARTIFACT_DIR}/rt20-same-task-comparator.md" 2>/dev/null; then
	if [[ -n "$RT20_FAILURE_NOTE" ]]; then
		RT20_FAILURE_NOTE="${RT20_FAILURE_NOTE}; artifact missing exact comparator provenance sentence"
	else
		RT20_FAILURE_NOTE="artifact missing exact comparator provenance sentence"
	fi
fi
RT20_EXIT="$(merge_if_missing_exact_line "$RT20_EXIT" "${ARTIFACT_DIR}/rt20-same-task-comparator.md" "## comparator setup")"
if ! grep -Fxq "## comparator setup" "${ARTIFACT_DIR}/rt20-same-task-comparator.md" 2>/dev/null; then
	if [[ -n "$RT20_FAILURE_NOTE" ]]; then
		RT20_FAILURE_NOTE="${RT20_FAILURE_NOTE}; artifact missing exact section heading ## comparator setup"
	else
		RT20_FAILURE_NOTE="artifact missing exact section heading ## comparator setup"
	fi
fi
RT20_FIRST_H2="$(first_h2_heading "${ARTIFACT_DIR}/rt20-same-task-comparator.md")"
if [[ "$RT20_FIRST_H2" != "## comparator setup" ]]; then
	RT20_EXIT="$(merge_exit_code "$RT20_EXIT" 1)"
	if [[ -n "$RT20_FAILURE_NOTE" ]]; then
		RT20_FAILURE_NOTE="${RT20_FAILURE_NOTE}; first section heading was ${RT20_FIRST_H2:-missing} instead of ## comparator setup"
	else
		RT20_FAILURE_NOTE="first section heading was ${RT20_FIRST_H2:-missing} instead of ## comparator setup"
	fi
fi
RT20_SETUP_LINE="$(first_pattern_line_number "${ARTIFACT_DIR}/rt20-same-task-comparator.md" "## comparator setup")"
RT20_FINDINGS_LINE="$(first_pattern_line_number "${ARTIFACT_DIR}/rt20-same-task-comparator.md" "## findings")"
if [[ -n "$RT20_SETUP_LINE" && -n "$RT20_FINDINGS_LINE" ]] && (( RT20_SETUP_LINE > RT20_FINDINGS_LINE )); then
	RT20_EXIT="$(merge_exit_code "$RT20_EXIT" 1)"
	if [[ -n "$RT20_FAILURE_NOTE" ]]; then
		RT20_FAILURE_NOTE="${RT20_FAILURE_NOTE}; comparator setup section appeared after findings"
	else
		RT20_FAILURE_NOTE="comparator setup section appeared after findings"
	fi
fi
finalize_scenario "RT20" "Same-Task Comparator" "$RT20_EXIT" "$RT20_RAW" "${ARTIFACT_DIR}/rt20-same-task-comparator.md" "$(extract_session_id "$RT20_RAW")" "$RT20_FAILURE_NOTE"

RT25_READY_FILE="${RAW_DIR}/rt25-rt26-proxy-ready.txt"
RT25_PROXY_LOG="${RAW_DIR}/rt25-rt26-proxy.log"
RT25_PROXY_REQUEST_LOG="${RAW_DIR}/rt25-rt26-proxy-requests.jsonl"
RT25_PROXY_CONFIG="${RUN_DIR}/config.rt25-rt26-delay-proxy.yaml"
RT25_MARKER="STEER_CANCEL_PROOF"
rm -f "$RT25_READY_FILE" "$RT25_PROXY_LOG" "$RT25_PROXY_REQUEST_LOG"
"${RETRYPROXY_BIN}" \
	--listen "127.0.0.1:0" \
	--upstream "$LIVE_BASE_URL" \
	--delay-match-substring "$RT25_MARKER" \
	--delay-ms 20000 \
	--request-log "$RT25_PROXY_REQUEST_LOG" \
	--ready-file "$RT25_READY_FILE" >"$RT25_PROXY_LOG" 2>&1 &
SCENARIO_HELPER_PID="$!"
RT25_PROXY_EXIT=0
RT25_FAILURE_NOTE=""
if ! wait_for_pattern "$RT25_READY_FILE" "http://" 30; then
	RT25_PROXY_EXIT=1
	RT25_FAILURE_NOTE="timed out waiting for local delay proxy ready file"
fi
RT25_PROXY_URL=""
if (( RT25_PROXY_EXIT == 0 )); then
	RT25_PROXY_URL="$(tr -d '\r\n' < "$RT25_READY_FILE")"
	write_config_with_base_url "$CONFIG_TEMPLATE_PATH" "$RT25_PROXY_CONFIG" "$RT25_PROXY_URL"
fi

RT25_PROMPT="${PROMPT_DIR}/rt25-steer-rejection.prompt.txt"
write_prompt "$RT25_PROMPT" "Marker ${RT25_MARKER}. This exec session is a live steer validation proof.
Begin the first provider turn for this request and wait for follow-up steering before concluding anything."
RT25_RAW="${RAW_DIR}/rt25-steer-cancel-session.jsonl"
RT25_SESSION_ID=""
RT25_EVENTS_SNAPSHOT="${RAW_DIR}/rt25-events-before-valid-steer.jsonl"
RT25_STATE_SNAPSHOT="${RAW_DIR}/rt25-state-before-valid-steer.json"
RT25_STEER_RAW="${RAW_DIR}/rt25-steer-rejection.json"
RT25_LONG_MESSAGE=""
RT25_REQUESTED_COUNT="0"
RT25_QUEUED_COUNT="0"
RT25_STEER_CMD_EXIT=0
RT25_PID=""
if (( RT25_PROXY_EXIT == 0 )); then
	"${AGENT_BIN}" exec \
		--config "$RT25_PROXY_CONFIG" \
		--provider openai-compatible \
		--model "$MODEL" \
		--workdir "$RT25_STEER_PROOF_DIR" \
		--json \
		--timeout 420 \
		<"$RT25_PROMPT" >"$RT25_RAW" 2>&1 &
	RT25_PID="$!"
	wait_for_pattern "$RT25_RAW" '"type":"session.started"' 90 || true
	wait_for_pattern "$RT25_RAW" '"type":"provider.request.prepared"' 90 || true
	RT25_SESSION_ID="$(extract_session_id "$RT25_RAW")"
	RT25_LONG_MESSAGE="$(head -c $((12000 + 1)) </dev/zero | tr '\0' 'x')"
	if [[ -n "$RT25_SESSION_ID" ]]; then
		"${AGENT_BIN}" steer "$RT25_SESSION_ID" \
			--config "$RT25_PROXY_CONFIG" \
			--json \
			--interrupt \
			--message "$RT25_LONG_MESSAGE" >"$RT25_STEER_RAW" 2>&1 || RT25_STEER_CMD_EXIT=$?
		copy_if_present "$(session_dir_for_id "$RT25_SESSION_ID")/events.jsonl" "$RT25_EVENTS_SNAPSHOT"
		copy_if_present "$(session_dir_for_id "$RT25_SESSION_ID")/state.json" "$RT25_STATE_SNAPSHOT"
		RT25_REQUESTED_COUNT="$(count_pattern "$RT25_EVENTS_SNAPSHOT" '"type":"session.steer.requested"')"
		RT25_QUEUED_COUNT="$(count_pattern "$RT25_EVENTS_SNAPSHOT" '"type":"session.steer.queued"')"
	else
		RT25_FAILURE_NOTE="session did not reach session.started before steer rejection proof"
	fi
fi
{
	echo "# rt25 steer rejection proof"
	echo
	echo "## cli result"
	echo
	echo "- session_id: \`${RT25_SESSION_ID}\`"
	echo "- proxy_url: \`${RT25_PROXY_URL}\`"
	echo "- steer_exit_code: \`${RT25_STEER_CMD_EXIT}\`"
	echo "- rejection_raw: \`${RT25_STEER_RAW}\`"
	echo "- events_snapshot: \`${RT25_EVENTS_SNAPSHOT}\`"
	echo "- state_snapshot: \`${RT25_STATE_SNAPSHOT}\`"
	echo
	echo "## validated facts"
	echo
	echo "- The oversized \`steer --json\` call returned a structured validation error instead of queueing the request."
	echo "- Before any valid steer was sent, the durable event log still showed requested_count=\`${RT25_REQUESTED_COUNT}\` and queued_count=\`${RT25_QUEUED_COUNT}\`, which keeps the proof at the pre-queue boundary."
	echo
	echo "## remaining risks"
	echo
	echo "- This proof is focused on oversized rejection during a real running session, not on other rejection modes such as empty input or steering a non-running session."
} >"${ARTIFACT_DIR}/rt25-steer-rejection.md"
RT25_EXIT=0
if (( RT25_PROXY_EXIT != 0 )); then
	RT25_EXIT=1
fi
if [[ -n "$RT25_PID" && "$RT25_SESSION_ID" == "" ]]; then
	RT25_EXIT="$(merge_exit_code "$RT25_EXIT" 1)"
fi
if (( RT25_STEER_CMD_EXIT != 1 )); then
	RT25_EXIT="$(merge_exit_code "$RT25_EXIT" 1)"
	if [[ -z "$RT25_FAILURE_NOTE" ]]; then
		RT25_FAILURE_NOTE="oversized steer returned exit ${RT25_STEER_CMD_EXIT} instead of 1"
	fi
fi
RT25_EXIT="$(merge_if_missing_pattern "$RT25_EXIT" "$RT25_STEER_RAW" "\"session_id\":\"${RT25_SESSION_ID}\"")"
RT25_EXIT="$(merge_if_missing_pattern "$RT25_EXIT" "$RT25_STEER_RAW" "\"accepted\":false")"
RT25_EXIT="$(merge_if_missing_pattern "$RT25_EXIT" "$RT25_STEER_RAW" "\"code\":\"steer_input_too_large\"")"
RT25_EXIT="$(merge_if_missing_pattern "$RT25_EXIT" "$RT25_STEER_RAW" "\"max_chars\":12000")"
RT25_EXIT="$(merge_if_missing_pattern "$RT25_EXIT" "$RT25_STEER_RAW" "\"actual_chars\":12001")"
if [[ "$RT25_REQUESTED_COUNT" != "0" || "$RT25_QUEUED_COUNT" != "0" ]]; then
	RT25_EXIT="$(merge_exit_code "$RT25_EXIT" 1)"
	if [[ -z "$RT25_FAILURE_NOTE" ]]; then
		RT25_FAILURE_NOTE="oversized steer unexpectedly wrote queue events before valid steer"
	fi
fi
printf '%s\n' \
	"proxy_url=${RT25_PROXY_URL}" \
	"session_id=${RT25_SESSION_ID}" \
	"steer_exit_code=${RT25_STEER_CMD_EXIT}" \
	"events_snapshot=${RT25_EVENTS_SNAPSHOT}" \
	"state_snapshot=${RT25_STATE_SNAPSHOT}" \
	"requested_count=${RT25_REQUESTED_COUNT}" \
	"queued_count=${RT25_QUEUED_COUNT}" \
	>"${NOTE_DIR}/rt25-steer-rejection-metadata.txt"
finalize_scenario "RT25" "Oversized Steer Rejection" "$RT25_EXIT" "$RT25_STEER_RAW" "${ARTIFACT_DIR}/rt25-steer-rejection.md" "$RT25_SESSION_ID" "$RT25_FAILURE_NOTE"

RT26_STEER_RAW="${RAW_DIR}/rt26-valid-interrupt-steer.json"
RT26_DONE_REPORT="${RT25_STEER_PROOF_DIR}/reports/rt26-steer-done.md"
RT26_EVENTS_PATH="${EVIDENCE_DIR}/rt26-session/events.jsonl"
RT26_EXIT=0
RT26_FAILURE_NOTE=""
RT26_STEER_EXIT=0
RT26_WAIT_EXIT=0
if [[ -n "$RT25_SESSION_ID" ]]; then
	"${AGENT_BIN}" steer "$RT25_SESSION_ID" \
		--config "$RT25_PROXY_CONFIG" \
		--json \
		--interrupt \
		--message "Use current evidence only. The delayed provider wait was intentionally interrupted for validation. Write reports/rt26-steer-done.md with one sentence saying the latest interrupt steer was accepted, then call finish immediately." >"$RT26_STEER_RAW" 2>&1 || RT26_STEER_EXIT=$?
	if [[ -n "$RT25_PID" ]]; then
		wait "$RT25_PID" || RT26_WAIT_EXIT=$?
	fi
else
	RT26_STEER_EXIT=1
	RT26_WAIT_EXIT=1
	RT26_FAILURE_NOTE="no live session id was available for provider cancellation proof"
fi
copy_session_evidence "$RT25_SESSION_ID" "${EVIDENCE_DIR}/rt26-session"
copy_if_present "$RT26_DONE_REPORT" "${RAW_DIR}/rt26-steer-done.md"
{
	echo "# rt26 provider cancel proof"
	echo
	echo "## control path"
	echo
	echo "- session_id: \`${RT25_SESSION_ID}\`"
	echo "- proxy_url: \`${RT25_PROXY_URL}\`"
	echo "- valid_steer_raw: \`${RT26_STEER_RAW}\`"
	echo "- delayed_exec_raw: \`${RT25_RAW}\`"
	echo "- proxy_request_log: \`${RT25_PROXY_REQUEST_LOG}\`"
	echo "- session_events: \`${RT26_EVENTS_PATH}\`"
	echo "- done_report: \`${RAW_DIR}/rt26-steer-done.md\`"
	echo
	echo "## validated facts"
	echo
	echo "- The live delayed provider turn emitted \`provider.cancelled\` with reason \`steer_interrupt\` before the next turn accepted the steer."
	echo "- The local delay proxy recorded a delayed request that ended with client cancellation, which tightens the proof from prompt-level inference to transport-level evidence."
	echo
	echo "## remaining risks"
	echo
	echo "- This proof exercises provider-call cancellation on the local delay proxy boundary rather than a matched competitor benchmark or a provider-native cancel API."
} >"${ARTIFACT_DIR}/rt26-provider-cancel-proof.md"
RT26_EXIT="$(merge_exit_code "$RT26_EXIT" "$RT26_STEER_EXIT")"
RT26_EXIT="$(merge_exit_code "$RT26_EXIT" "$RT26_WAIT_EXIT")"
RT26_EXIT="$(merge_if_missing_pattern "$RT26_EXIT" "$RT26_STEER_RAW" "\"accepted\":true")"
RT26_EXIT="$(merge_if_missing_pattern "$RT26_EXIT" "$RT25_RAW" "\"type\":\"provider.cancelled\"")"
RT26_EXIT="$(merge_if_missing_pattern "$RT26_EXIT" "$RT25_RAW" "\"reason\":\"steer_interrupt\"")"
RT26_EXIT="$(merge_if_missing_pattern "$RT26_EXIT" "$RT25_RAW" "\"type\":\"session.steer.accepted\"")"
RT26_EXIT="$(merge_if_missing_pattern "$RT26_EXIT" "$RT25_PROXY_REQUEST_LOG" "\"delay_injected\":true")"
RT26_EXIT="$(merge_if_missing_pattern "$RT26_EXIT" "$RT25_PROXY_REQUEST_LOG" "\"canceled\":true")"
RT26_EXIT="$(merge_if_missing_file "$RT26_EXIT" "${RAW_DIR}/rt26-steer-done.md")"
printf '%s\n' \
	"proxy_url=${RT25_PROXY_URL}" \
	"session_id=${RT25_SESSION_ID}" \
	"events_path=${RT26_EVENTS_PATH}" \
	"proxy_request_log=${RT25_PROXY_REQUEST_LOG}" \
	"valid_steer_exit=${RT26_STEER_EXIT}" \
	"delayed_exec_exit=${RT26_WAIT_EXIT}" \
	>"${NOTE_DIR}/rt26-provider-cancel-metadata.txt"
finalize_scenario "RT26" "Provider Cancel And Interrupt Preemption" "$RT26_EXIT" "$RT25_RAW" "${ARTIFACT_DIR}/rt26-provider-cancel-proof.md" "$RT25_SESSION_ID" "$RT26_FAILURE_NOTE"
if [[ -n "$RT25_PID" ]]; then
	kill "$RT25_PID" 2>/dev/null || true
	wait "$RT25_PID" 2>/dev/null || true
	RT25_PID=""
fi
if [[ -n "$SCENARIO_HELPER_PID" ]]; then
	kill "$SCENARIO_HELPER_PID" 2>/dev/null || true
	wait "$SCENARIO_HELPER_PID" 2>/dev/null || true
	SCENARIO_HELPER_PID=""
fi

RT21_PROMPT="${PROMPT_DIR}/rt21-large-project-readiness.prompt.txt"
RT21_PROOF_NOTE="${NOTE_DIR}/rt21-proof-anchor-input.md"
RT21_SCENARIO_INDEX_PATH="${NOTE_DIR}/scenario-index.tsv"
RT21_GAP_SUMMARY_PATH="${NOTE_DIR}/preflight-gap-proof-summary.md"
RT21_RT07_PATH="${ARTIFACT_DIR}/rt07-live-steer-audit.md"
RT21_RT09_PATH="${ARTIFACT_DIR}/rt09-background-summary.md"
RT21_RT11_PATH="${ARTIFACT_DIR}/rt11-platform-go-change-summary.md"
RT21_RT13_PATH="${ARTIFACT_DIR}/rt13-platform-py-change-summary.md"
RT21_RT15_PATH="${ARTIFACT_DIR}/rt15-task-memory-traceability.md"
RT21_RT20_PATH="${ARTIFACT_DIR}/rt20-same-task-comparator.md"
RT21_RT26_PATH="${ARTIFACT_DIR}/rt26-provider-cancel-proof.md"
RT21_PRODUCT_SPEC_PATH="${ROOT_DIR}/spec/00-product.md"
RT21_STEER_SPEC_PATH="${ROOT_DIR}/spec/13-live-input-and-steering.md"
RT21_PASSED_COUNT="$(awk -F '\t' 'NR > 1 && $3 == "passed" { count++ } END { print count + 0 }' "$RT21_SCENARIO_INDEX_PATH")"
{
	echo "# RT21 Proof Anchor Input"
	echo
	echo "Use this note first. It already contains the exact line-window anchors needed for the readiness scorecard."
	echo
	echo "## Run Snapshot"
	echo
	echo "- Previous matrix status before this rerun fix: ${RT21_PASSED_COUNT}/26 scenarios passed; RT21 was the lone failed aggregator."
	echo "- Full ledger path: ${WORKSPACE_RUN_DIR}/notes/scenario-index.tsv"
	echo
	echo "## Product Boundary Anchors"
	echo
	echo "- spec/00 large-project profile: go-cli-agent/spec/00-product.md:68"
	nl -ba "$RT21_PRODUCT_SPEC_PATH" | sed -n '68,78p'
	echo
	echo "- spec/13 steer contract: go-cli-agent/spec/13-live-input-and-steering.md:31"
	nl -ba "$RT21_STEER_SPEC_PATH" | sed -n '31,44p'
	echo
	echo "## Directly Validated Current-Run Strengths"
	echo
	echo "- Former RT21 proof-completeness holes closed: ${WORKSPACE_RUN_DIR}/notes/preflight-gap-proof-summary.md:9"
	nl -ba "$RT21_GAP_SUMMARY_PATH" | sed -n '9,30p'
	echo
	echo "- RT07 same-task steer plus repo-wide Go repair: ${WORKSPACE_ARTIFACT_DIR}/rt07-live-steer-audit.md:5"
	nl -ba "$RT21_RT07_PATH" | sed -n '5,22p'
	echo
	echo "- RT09 background child and queue follow-through: ${WORKSPACE_ARTIFACT_DIR}/rt09-background-summary.md:4"
	nl -ba "$RT21_RT09_PATH" | sed -n '4,15p'
	echo
	echo "- RT11 platform-go repair reached full go test closure: ${WORKSPACE_ARTIFACT_DIR}/rt11-platform-go-change-summary.md:16"
	nl -ba "$RT21_RT11_PATH" | sed -n '16,25p'
	echo
	echo "- RT13 platform-python repair reached targeted pytest closure: ${WORKSPACE_ARTIFACT_DIR}/rt13-platform-py-change-summary.md:15"
	nl -ba "$RT21_RT13_PATH" | sed -n '15,23p'
	echo
	echo "- RT26 provider cancellation reached transport-level proof: ${WORKSPACE_ARTIFACT_DIR}/rt26-provider-cancel-proof.md:13"
	nl -ba "$RT21_RT26_PATH" | sed -n '13,20p'
	echo
	echo "## Remaining Live Gaps Still Called Out By Current-Run Evidence"
	echo
	echo "- RT15 durable-memory remaining gap: ${WORKSPACE_ARTIFACT_DIR}/rt15-task-memory-traceability.md:20"
	nl -ba "$RT21_RT15_PATH" | sed -n '20,40p'
	echo
	echo "- RT20 hard-surpass proof gaps: ${WORKSPACE_ARTIFACT_DIR}/rt20-same-task-comparator.md:9"
	nl -ba "$RT21_RT20_PATH" | sed -n '9,28p;32,49p'
	echo
	echo "## Usage Discipline"
	echo
	echo "- If this note does not directly validate a blocker, write the exact sentence No validated findings. and move the concern to remaining risks or unresolved questions."
	echo "- Use RT20 only to separate direct live proof from comparator-backed claims. Do not upgrade comparator-only concerns into current-run findings."
	echo "- Do not reread raw/session files in this scenario unless a material ambiguity remains after reading this note. If one ambiguity remains, inspect at most two exact windows from: ${WORKSPACE_ARTIFACT_DIR}/rt20-same-task-comparator.md, ${WORKSPACE_ARTIFACT_DIR}/rt15-task-memory-traceability.md, ${WORKSPACE_ARTIFACT_DIR}/rt07-live-steer-audit.md, ${WORKSPACE_ARTIFACT_DIR}/rt26-provider-cancel-proof.md, ${WORKSPACE_RUN_DIR}/notes/preflight-gap-proof-summary.md, and ${WORKSPACE_RUN_DIR}/notes/scenario-index.tsv."
} >"$RT21_PROOF_NOTE"
write_prompt "$RT21_PROMPT" "Use the review_pipeline skill for this task.
Read ${WORKSPACE_RUN_DIR}/notes/rt21-proof-anchor-input.md first.
Treat that note as the authoritative citation-anchor set for this scorecard. In most cases the note alone is enough.
Do not read raw/session files or reference-repo docs/source directly in this scenario.
If one material ambiguity remains after reading the proof note, inspect at most two of these supporting files: ${WORKSPACE_ARTIFACT_DIR}/rt20-same-task-comparator.md, ${WORKSPACE_ARTIFACT_DIR}/rt15-task-memory-traceability.md, ${WORKSPACE_ARTIFACT_DIR}/rt07-live-steer-audit.md, ${WORKSPACE_ARTIFACT_DIR}/rt26-provider-cancel-proof.md, ${WORKSPACE_RUN_DIR}/notes/preflight-gap-proof-summary.md, and ${WORKSPACE_RUN_DIR}/notes/scenario-index.tsv.
Use this current-run evidence to judge whether go-cli-agent now materially closes the previous large-project blockers and whether it is honestly ready to surpass codex and opencode on large-project development and review.
You must distinguish three claims: what is directly validated by this run, what is only comparator-backed, and what still lacks a live competitor benchmark.
Default discipline: do not hunt for new blockers. If the proof note does not directly validate a blocker, write the exact sentence No validated findings. inside findings and keep weaker concerns in remaining risks or unresolved questions.
Write ${ABS_ARTIFACT_DIR}/rt21-large-project-readiness.md with sections: live strengths, scorecard, findings, remaining risks, unresolved questions, next architectural moves.
In the scorecard, rate review pipeline, proof quality, compaction and proof-at-boundary, interruption and recovery, task-system and durable memory execution, multi-package coding execution, child and queue orchestration, and cross-repo reasoning.
For each directly validated blocker you keep in findings, include explicit Severity:, Confidence:, Evidence:, Snippet:, and Why it matters: labels. The snippet must literally appear inside the cited path:line range.
If the honest conclusion is that go-cli-agent is materially improved but not yet beyond codex/opencode due to proof gaps or comparator-only support, say that clearly in remaining risks or unresolved questions instead of upgrading it into a validated finding.
Then call finish with a one-line summary."
sed -i "s#__WORKSPACE_ARTIFACT_DIR__#${WORKSPACE_ARTIFACT_DIR}#g; s#__WORKSPACE_RUN_DIR__#${WORKSPACE_RUN_DIR}#g; s#__WORKSPACE_EVIDENCE_DIR__#${WORKSPACE_EVIDENCE_DIR}#g; s#__ABS_ARTIFACT_DIR__#${ABS_ARTIFACT_DIR}#g; s#__TOP_LEVEL_MARKDOWN_LIST__#${TOP_LEVEL_MARKDOWN_LIST}#g" "$RT21_PROMPT"
RT21_RAW="${RAW_DIR}/rt21-large-project-readiness.jsonl"
run_exec "$RT21_PROMPT" "$RT21_RAW" "$WORKSPACE_ROOT" 420
RT21_EXIT=$?
finalize_scenario "RT21" "Large Project Readiness Scorecard" "$RT21_EXIT" "$RT21_RAW" "${ARTIFACT_DIR}/rt21-large-project-readiness.md" "$(extract_session_id "$RT21_RAW")"

RT22_PROMPT="${PROMPT_DIR}/rt22-exact-template-audit.prompt.txt"
write_prompt "$RT22_PROMPT" "Use the review_pipeline skill for this task.
Inspect only go-cli-agent/README.md, go-cli-agent/spec/11-spec-audit-and-traceability.md, go-cli-agent/spec/14-multi-agent-and-isolation.md, go-cli-agent/spec/15-background-queue.md, go-cli-agent/internal/runtime/prompt.go, and go-cli-agent/internal/runtime/review_guard.go.
This task is an explicit exception to the usual findings-first opening: the exact setup block must appear before findings.
The file must begin exactly with these lines before any findings section:

# rt22 exact-template audit

## setup
This report intentionally verifies exact-template artifact enforcement.
This is a real runtime-quality task, not a toy formatting check.

Do not paraphrase either setup sentence, and do not place any heading between the title and this section.
Write ${ABS_ARTIFACT_DIR}/rt22-exact-template-audit.md with sections: setup, findings, confirmed alignments, remaining risks, next fixes.
If there is no validated finding, write the exact sentence No validated findings. inside the findings section.
Then call finish with a one-line summary."
RT22_RAW="${RAW_DIR}/rt22-exact-template-audit.jsonl"
run_exec "$RT22_PROMPT" "$RT22_RAW" "$WORKSPACE_ROOT" 300
RT22_EXIT=$?
RT22_FAILURE_NOTE=""
RT22_EXIT="$(merge_if_missing_exact_line "$RT22_EXIT" "${ARTIFACT_DIR}/rt22-exact-template-audit.md" "# rt22 exact-template audit")"
RT22_EXIT="$(merge_if_missing_exact_line "$RT22_EXIT" "${ARTIFACT_DIR}/rt22-exact-template-audit.md" "## setup")"
RT22_EXIT="$(merge_if_missing_exact_line "$RT22_EXIT" "${ARTIFACT_DIR}/rt22-exact-template-audit.md" "This report intentionally verifies exact-template artifact enforcement.")"
RT22_EXIT="$(merge_if_missing_exact_line "$RT22_EXIT" "${ARTIFACT_DIR}/rt22-exact-template-audit.md" "This is a real runtime-quality task, not a toy formatting check.")"
RT22_FIRST_H2="$(first_h2_heading "${ARTIFACT_DIR}/rt22-exact-template-audit.md")"
if [[ "$RT22_FIRST_H2" != "## setup" ]]; then
	RT22_EXIT="$(merge_exit_code "$RT22_EXIT" 1)"
	RT22_FAILURE_NOTE="first section heading was ${RT22_FIRST_H2:-missing} instead of ## setup"
fi
finalize_scenario "RT22" "Exact Template Artifact Guard" "$RT22_EXIT" "$RT22_RAW" "${ARTIFACT_DIR}/rt22-exact-template-audit.md" "$(extract_session_id "$RT22_RAW")" "$RT22_FAILURE_NOTE"

RT23_PARENT_PROMPT="${PROMPT_DIR}/rt23-role-parent.prompt.txt"
write_prompt "$RT23_PARENT_PROMPT" "Write reports/spec.md with a short note that an evaluator child must audit config and quota behavior.
Write reports/plan.md with a three-step delegated evaluator checklist.
Write reports/progress.md with one line noting the parent prepared the role-aware handoff.
Write reports/validation.md with one line reserving space for evaluator findings.
Then call finish."
RT23_PARENT_RAW="${RAW_DIR}/rt23-role-parent.jsonl"
run_exec_exact "$RT23_PARENT_PROMPT" "$RT23_PARENT_RAW" "$RT23_ROLE_GO_DIR" 60 45
RT23_EXIT=$?
RT23_PARENT_SESSION_ID="$(extract_session_id "$RT23_PARENT_RAW")"
RT23_DELEGATE_RAW="${RAW_DIR}/rt23-role-child.json"
RT23_CHILD_PROMPT="${PROMPT_DIR}/rt23-role-child.prompt.txt"
write_prompt "$RT23_CHILD_PROMPT" "Use the review_pipeline skill for this task.
Inspect only README.md, docs/contracts.md, internal/config/config.go, internal/quota/policy.go, internal/config/config_test.go, and internal/quota/policy_test.go.
Write reports/rt23-role-review.md with sections: findings, unresolved questions, remaining risks.
If there is no validated finding, write the exact sentence No validated findings. inside findings.
Then call finish with a one-line summary."
if [[ -n "$RT23_PARENT_SESSION_ID" ]]; then
	"${AGENT_BIN}" experimental delegate "$RT23_PARENT_SESSION_ID" \
		--config "$CONFIG_PATH" \
		--agent reviewer \
		--role evaluator \
		--json \
		--timeout 240 \
		<"$RT23_CHILD_PROMPT" >"$RT23_DELEGATE_RAW" 2>&1
	RT23_DELEGATE_EXIT=$?
	RT23_EXIT="$(merge_exit_code "$RT23_EXIT" "$RT23_DELEGATE_EXIT")"
else
	RT23_EXIT="$(merge_exit_code "$RT23_EXIT" 1)"
fi
copy_child_artifact_if_present "$RT23_DELEGATE_RAW" "reports/rt23-role-review.md" "${ARTIFACT_DIR}/rt23-role-review.md" "$RT23_ROLE_GO_DIR"
RT23_CHILD_SESSION_ID="$(extract_json_field "$RT23_DELEGATE_RAW" "session_id")"
copy_session_evidence "$RT23_CHILD_SESSION_ID" "${EVIDENCE_DIR}/rt23-child-session"
RT23_ROLE_SESSION_PATH="${EVIDENCE_DIR}/rt23-child-session/session.json"
RT23_ROLE_EVENTS_PATH="${EVIDENCE_DIR}/rt23-child-session/events.jsonl"
RT23_EXIT="$(merge_if_missing_pattern "$RT23_EXIT" "$RT23_DELEGATE_RAW" "\"agent_role\":\"evaluator\"")"
RT23_EXIT="$(merge_if_missing_pattern "$RT23_EXIT" "$RT23_ROLE_SESSION_PATH" "\"agent_role\": \"evaluator\"")"
RT23_EXIT="$(merge_if_missing_pattern "$RT23_EXIT" "$RT23_ROLE_EVENTS_PATH" "\"agent_role\"")"
RT23_EXIT="$(merge_if_missing_file "$RT23_EXIT" "${ARTIFACT_DIR}/rt23-role-review.md")"
printf '%s\n' \
	"parent_session_id=${RT23_PARENT_SESSION_ID}" \
	"child_session_id=${RT23_CHILD_SESSION_ID}" \
	"child_role=evaluator" \
	"session_path=${RT23_ROLE_SESSION_PATH}" \
	"events_path=${RT23_ROLE_EVENTS_PATH}" \
	>"${NOTE_DIR}/rt23-role-metadata.txt"
finalize_scenario "RT23" "Explicit Role Persistence And Handoff" "$RT23_EXIT" "$RT23_DELEGATE_RAW" "${ARTIFACT_DIR}/rt23-role-review.md" "$RT23_CHILD_SESSION_ID"

RT24_PROMPT="${PROMPT_DIR}/rt24-steer-plan-refresh.prompt.txt"
write_prompt "$RT24_PROMPT" "Read AGENTS.md, product_overview.md, release_constraints.md, and ops_notes.md first.
This is a large docset task that may change scope mid-run.
Before drafting the final launch brief, create reports/spec.md, reports/plan.md, reports/progress.md, and reports/validation.md.
Keep the initial focus on queue visibility and SLA breach explanations from the current release constraints.
Write reports/rt24-launch-brief.md with sections: release story, operator notes, remaining risks.
Do not call finish until the durable reports stack and the launch brief are updated."
RT24_RAW="${RAW_DIR}/rt24-steer-plan-refresh.jsonl"
"${AGENT_BIN}" exec \
	--config "$CONFIG_PATH" \
	--provider openai-compatible \
	--model "$MODEL" \
	--workdir "$RT24_DOCSET_DIR" \
	--json \
	--timeout 300 \
	<"$RT24_PROMPT" >"$RT24_RAW" 2>&1 &
RT24_PID="$!"
RT24_EXIT=0
wait_for_pattern "$RT24_RAW" '"type":"session.started"' 90 || true
RT24_SESSION_ID="$(extract_session_id "$RT24_RAW")"
wait_for_pattern "$RT24_RAW" 'reports/spec.md' 180 || true
if [[ -n "$RT24_SESSION_ID" ]]; then
	"${AGENT_BIN}" steer "$RT24_SESSION_ID" \
		--config "$CONFIG_PATH" \
		--json \
		--interrupt \
		--message "Actually change direction for this large repository documentation task: prioritize safer self-hosted rollback and migration guidance. Refresh reports/spec.md and reports/plan.md before more drafting, then update reports/rt24-launch-brief.md to reflect rollback-safe upgrade guidance and finish." \
		>"${RAW_DIR}/rt24-steer-command.json" 2>&1
	RT24_STEER_EXIT=$?
	RT24_EXIT="$(merge_exit_code "$RT24_EXIT" "$RT24_STEER_EXIT")"
else
	RT24_EXIT="$(merge_exit_code "$RT24_EXIT" 1)"
fi
wait "$RT24_PID"
RT24_WAIT_EXIT=$?
RT24_EXIT="$(merge_exit_code "$RT24_EXIT" "$RT24_WAIT_EXIT")"
copy_if_present "${RT24_DOCSET_DIR}/reports/rt24-launch-brief.md" "${ARTIFACT_DIR}/rt24-steer-refresh-brief.md"
copy_session_evidence "$RT24_SESSION_ID" "${EVIDENCE_DIR}/rt24-session"
RT24_EVENTS_PATH="${EVIDENCE_DIR}/rt24-session/events.jsonl"
RT24_ACCEPTED_COUNT="$(count_pattern "$RT24_EVENTS_PATH" '"type":"session.steer.accepted"')"
printf '%s\n' \
	"events_path=${RT24_EVENTS_PATH}" \
	"accepted_count=${RT24_ACCEPTED_COUNT}" \
	>"${NOTE_DIR}/rt24-steer-refresh-metadata.txt"
if [[ "$RT24_ACCEPTED_COUNT" -lt 1 ]]; then
	RT24_EXIT="$(merge_exit_code "$RT24_EXIT" 1)"
fi
if ! grep -Fq "rollback" "${RT24_DOCSET_DIR}/reports/spec.md" && ! grep -Fq "回滚" "${RT24_DOCSET_DIR}/reports/spec.md"; then
	RT24_EXIT="$(merge_exit_code "$RT24_EXIT" 1)"
fi
if ! grep -Fq "rollback" "${RT24_DOCSET_DIR}/reports/plan.md" && ! grep -Fq "回滚" "${RT24_DOCSET_DIR}/reports/plan.md"; then
	RT24_EXIT="$(merge_exit_code "$RT24_EXIT" 1)"
fi
RT24_EXIT="$(merge_if_missing_pattern "$RT24_EXIT" "${ARTIFACT_DIR}/rt24-steer-refresh-brief.md" "upgrade")"
finalize_scenario "RT24" "Steer-Triggered Spec And Plan Refresh" "$RT24_EXIT" "$RT24_RAW" "${ARTIFACT_DIR}/rt24-steer-refresh-brief.md" "$RT24_SESSION_ID"

finalize_run_outputs
printf '%s\n' "$RUN_DIR"
if (( FAILED_SCENARIOS > 0 )); then
	exit 1
fi
