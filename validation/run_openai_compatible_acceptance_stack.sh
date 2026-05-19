#!/usr/bin/env bash
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

: "${OPENAI_API_KEY:?OPENAI_API_KEY is required}"

MODEL="${GO_CLI_AGENT_LIVE_MODEL:-gpt-5.4}"
DEFAULT_LIVE_BASE_URL="http://64.186.236.156:24634/v1"
LIVE_BASE_URL="${GO_CLI_AGENT_LIVE_RESPONSES_URL:-${DEFAULT_LIVE_BASE_URL}}"
STACK_LABEL="${GO_CLI_AGENT_ACCEPTANCE_LABEL:-acceptance-stack}"
STACK_ID="${1:-$(date -u +%F)-openai-compatible-${MODEL}-${STACK_LABEL}}"
STACK_DIR="validation/runs/${STACK_ID}"
RAW_DIR="${STACK_DIR}/raw"
NOTE_DIR="${STACK_DIR}/notes"
SUMMARY_PATH="${STACK_DIR}/SUMMARY.md"
ISSUES_PATH="${STACK_DIR}/ISSUES.md"
ABORTED_PATH="${STACK_DIR}/ABORTED.md"
MATRIX_RUN_ID="${STACK_ID}-matrix"
FOLLOWUP_RUN_ID="${STACK_ID}-focused-webconsole-followup"
MATRIX_RUN_DIR="validation/runs/${MATRIX_RUN_ID}"
FOLLOWUP_RUN_DIR="validation/runs/${FOLLOWUP_RUN_ID}"
CONFIG_TEMPLATE_PATH="validation/config.openai-compatible.yaml"
CONFIG_PATH="${STACK_DIR}/config.openai-compatible.effective.yaml"
CONTINUE_AFTER_MATRIX_FAILURE="${GO_CLI_AGENT_ACCEPTANCE_CONTINUE_AFTER_MATRIX_FAILURE:-true}"
SKIP_FOLLOWUP="${GO_CLI_AGENT_ACCEPTANCE_SKIP_FOLLOWUP:-false}"
PREFLIGHT_ATTEMPTS="${GO_CLI_AGENT_ACCEPTANCE_PREFLIGHT_ATTEMPTS:-2}"
PREFLIGHT_RETRY_DELAY_SEC="${GO_CLI_AGENT_ACCEPTANCE_PREFLIGHT_RETRY_DELAY_SEC:-5}"

case "$CONTINUE_AFTER_MATRIX_FAILURE" in
	true|false)
		;;
	*)
		echo "GO_CLI_AGENT_ACCEPTANCE_CONTINUE_AFTER_MATRIX_FAILURE must be true or false" >&2
		exit 1
		;;
esac

case "$SKIP_FOLLOWUP" in
	true|false)
		;;
	*)
		echo "GO_CLI_AGENT_ACCEPTANCE_SKIP_FOLLOWUP must be true or false" >&2
		exit 1
		;;
esac

case "$PREFLIGHT_ATTEMPTS" in
	''|*[!0-9]*|0)
		echo "GO_CLI_AGENT_ACCEPTANCE_PREFLIGHT_ATTEMPTS must be a positive integer" >&2
		exit 1
		;;
esac

case "$PREFLIGHT_RETRY_DELAY_SEC" in
	''|*[!0-9]*)
		echo "GO_CLI_AGENT_ACCEPTANCE_PREFLIGHT_RETRY_DELAY_SEC must be a non-negative integer" >&2
		exit 1
		;;
esac

for path in "$STACK_DIR" "$MATRIX_RUN_DIR" "$FOLLOWUP_RUN_DIR"; do
	if [[ -e "$path" ]]; then
		echo "path already exists: $path" >&2
		exit 1
	fi
done

mkdir -p "$RAW_DIR" "$NOTE_DIR"
printf 'phase\tstatus\texit_code\trun_dir\tsummary\tlog\n' >"${NOTE_DIR}/phase-index.tsv"
printf 'check\tstatus\texit_code\tpath\n' >"${NOTE_DIR}/preflight-index.tsv"

write_effective_config() {
	local src="$1"
	local dst="$2"
	local escaped_base_url=""
	escaped_base_url="$(printf '%s' "$LIVE_BASE_URL" | sed 's/[&]/\\&/g')"
	sed "s#^\([[:space:]]*base_url:\) .*#\1 ${escaped_base_url}#" "$src" >"$dst"
}

write_effective_config "$CONFIG_TEMPLATE_PATH" "$CONFIG_PATH"

status_from_exit() {
	local exit_code="$1"
	if (( exit_code == 0 )); then
		printf 'passed'
	else
		printf 'failed'
	fi
}

record_phase() {
	local phase="$1"
	local status="$2"
	local exit_code="$3"
	local run_dir="$4"
	local summary_path="$5"
	local log_path="$6"
	printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$phase" "$status" "$exit_code" "$run_dir" "$summary_path" "$log_path" >>"${NOTE_DIR}/phase-index.tsv"
}

write_phase_note() {
	local phase="$1"
	local status="$2"
	local exit_code="$3"
	local run_dir="$4"
	local summary_path="$5"
	local log_path="$6"
	local note_path="${NOTE_DIR}/${phase}.md"
	{
		echo "# ${phase}"
		echo
		echo "- status: ${status}"
		echo "- exit_code: ${exit_code}"
		echo "- run_dir: \`${run_dir}\`"
		echo "- summary: \`${summary_path}\`"
		echo "- log: \`${log_path}\`"
		if [[ -f "$log_path" ]]; then
			echo
			echo "## Tail"
			echo
			echo '```'
			tail -n 40 "$log_path"
			echo '```'
		fi
	} >"$note_path"
}

record_preflight() {
	local name="$1"
	local exit_code="$2"
	local output_path="$3"
	printf '%s\t%s\t%s\t%s\n' "$name" "$(status_from_exit "$exit_code")" "$exit_code" "$output_path" >>"${NOTE_DIR}/preflight-index.tsv"
}

write_preflight_note() {
	local name="$1"
	local exit_code="$2"
	local output_path="$3"
	local note_path="${NOTE_DIR}/${name}.md"
	{
		echo "# ${name}"
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

probe_failure_reason() {
	local path="$1"
	if [[ ! -f "$path" ]]; then
		printf 'probe output missing'
		return 0
	fi
	if grep -Fqi 'invalid api key' "$path" || grep -Fqi 'incorrect api key' "$path"; then
		printf 'provider auth rejected the supplied API key'
		return 0
	fi
	if grep -Fq 'token_invalidated' "$path"; then
		printf 'provider auth path returned token_invalidated'
		return 0
	fi
	if grep -Fq 'token_revoked' "$path"; then
		printf 'provider auth path returned token_revoked'
		return 0
	fi
	if grep -Fq 'auth_unavailable' "$path" || grep -Fq 'no auth available' "$path"; then
		printf 'provider auth path returned auth_unavailable'
		return 0
	fi
	if grep -Fq 'invalid_request' "$path"; then
		printf 'provider rejected the request shape'
		return 0
	fi
	if grep -Fq 'upstream_timeout' "$path" || grep -Fq 'context deadline exceeded' "$path"; then
		printf 'provider path hit upstream_timeout'
		return 0
	fi
	if grep -Fq 'i/o timeout' "$path" || grep -Fq 'dial tcp' "$path" ||
		grep -Fq 'connection refused' "$path" || grep -Fq 'connection reset by peer' "$path" ||
		grep -Fq 'unexpected EOF' "$path"; then
		printf 'provider path hit transport timeout'
		return 0
	fi
	if grep -o '"error":"[^"]*"' "$path" | tail -n1 | cut -d'"' -f4 | grep -Fq .; then
		grep -o '"error":"[^"]*"' "$path" | tail -n1 | cut -d'"' -f4
		return 0
	fi
	tail -n 1 "$path" | tr -d '\r'
}

is_retryable_probe_failure_note() {
	local note="$1"
	case "$note" in
		*auth_unavailable*|*upstream_timeout*|*transport\ timeout*)
			return 0
			;;
		*)
			return 1
			;;
	esac
}

run_probe_preflight() {
	local attempt=1
	while (( attempt <= PREFLIGHT_ATTEMPTS )); do
		local output_path="${RAW_DIR}/preflight-probe-attempt${attempt}.json"
		local exit_code=0
		./bin/go-cli-agent probe-provider \
			--config "$CONFIG_PATH" \
			--provider openai-compatible \
			--model "$MODEL" \
			--json >"$output_path" 2>&1 || exit_code=$?
		record_preflight "preflight-probe-attempt-${attempt}" "$exit_code" "$output_path"
		write_preflight_note "preflight-probe-attempt-${attempt}" "$exit_code" "$output_path"
		PREFLIGHT_LAST_ATTEMPT="$attempt"
		PREFLIGHT_LAST_PATH="$output_path"
		PREFLIGHT_EXIT="$exit_code"
		if (( exit_code == 0 )); then
			PREFLIGHT_STATUS="passed"
			PREFLIGHT_REASON=""
			return 0
		fi
		PREFLIGHT_STATUS="failed"
		PREFLIGHT_REASON="$(probe_failure_reason "$output_path")"
		if (( attempt >= PREFLIGHT_ATTEMPTS )) || ! is_retryable_probe_failure_note "$PREFLIGHT_REASON"; then
			return 1
		fi
		if (( PREFLIGHT_RETRY_DELAY_SEC > 0 )); then
			sleep "$PREFLIGHT_RETRY_DELAY_SEC"
		fi
		attempt=$((attempt + 1))
	done
	return 1
}

run_phase() {
	local phase="$1"
	local script_path="$2"
	local run_id="$3"
	local log_path="$4"
	local run_dir="validation/runs/${run_id}"
	local summary_path="${run_dir}/SUMMARY.md"
	local exit_code=0

	"$script_path" "$run_id" >"$log_path" 2>&1 || exit_code=$?
	if [[ ! -f "$summary_path" ]]; then
		exit_code=1
	fi
	record_phase "$phase" "$(status_from_exit "$exit_code")" "$exit_code" "$run_dir" "$summary_path" "$log_path"
	write_phase_note "$phase" "$(status_from_exit "$exit_code")" "$exit_code" "$run_dir" "$summary_path" "$log_path"
	printf '%s' "$exit_code"
}

record_skipped_phase() {
	local phase="$1"
	local reason="$2"
	local run_dir="$3"
	local summary_path="$4"
	local log_path="$5"
	local note_path="${NOTE_DIR}/${phase}.md"
	record_phase "$phase" "skipped" "0" "$run_dir" "$summary_path" "$log_path"
	{
		echo "# ${phase}"
		echo
		echo "- status: skipped"
		echo "- reason: ${reason}"
		echo "- run_dir: \`${run_dir}\`"
		echo "- summary: \`${summary_path}\`"
		echo "- log: \`${log_path}\`"
	} >"$note_path"
}

MATRIX_LOG="${RAW_DIR}/matrix-driver.log"
FOLLOWUP_LOG="${RAW_DIR}/followup-driver.log"
PREFLIGHT_EXIT=0
PREFLIGHT_STATUS="passed"
PREFLIGHT_REASON=""
PREFLIGHT_LAST_PATH=""
PREFLIGHT_LAST_ATTEMPT=0
MATRIX_EXIT=0
FOLLOWUP_EXIT=0
MATRIX_STATUS="passed"
FOLLOWUP_STATUS="passed"

printf '== bundle preflight probe ==\n'
run_probe_preflight || true

if [[ "$PREFLIGHT_STATUS" != "passed" ]]; then
	MATRIX_STATUS="skipped"
	FOLLOWUP_STATUS="skipped"
	record_skipped_phase "full_matrix" "bundle preflight probe failed after ${PREFLIGHT_LAST_ATTEMPT} attempt(s): ${PREFLIGHT_REASON}" "$MATRIX_RUN_DIR" "${MATRIX_RUN_DIR}/SUMMARY.md" "$MATRIX_LOG"
	record_skipped_phase "focused_followup" "bundle preflight probe failed after ${PREFLIGHT_LAST_ATTEMPT} attempt(s): ${PREFLIGHT_REASON}" "$FOLLOWUP_RUN_DIR" "${FOLLOWUP_RUN_DIR}/SUMMARY.md" "$FOLLOWUP_LOG"
else
	printf '== full matrix ==\n'
	MATRIX_EXIT="$(run_phase "full_matrix" "./validation/run_round31_complex_real_matrix.sh" "$MATRIX_RUN_ID" "$MATRIX_LOG")"
	MATRIX_STATUS="$(status_from_exit "$MATRIX_EXIT")"

	if [[ "$SKIP_FOLLOWUP" == "true" ]]; then
		FOLLOWUP_STATUS="skipped"
		record_skipped_phase "focused_followup" "GO_CLI_AGENT_ACCEPTANCE_SKIP_FOLLOWUP=true" "$FOLLOWUP_RUN_DIR" "${FOLLOWUP_RUN_DIR}/SUMMARY.md" "$FOLLOWUP_LOG"
	elif [[ "$CONTINUE_AFTER_MATRIX_FAILURE" == "false" && "$MATRIX_EXIT" != "0" ]]; then
		FOLLOWUP_STATUS="skipped"
		record_skipped_phase "focused_followup" "matrix failed and GO_CLI_AGENT_ACCEPTANCE_CONTINUE_AFTER_MATRIX_FAILURE=false" "$FOLLOWUP_RUN_DIR" "${FOLLOWUP_RUN_DIR}/SUMMARY.md" "$FOLLOWUP_LOG"
	else
		printf '== focused webconsole follow-up ==\n'
		FOLLOWUP_EXIT="$(run_phase "focused_followup" "./validation/run_webconsole_followup_validation.sh" "$FOLLOWUP_RUN_ID" "$FOLLOWUP_LOG")"
		FOLLOWUP_STATUS="$(status_from_exit "$FOLLOWUP_EXIT")"
	fi
fi

{
	echo "# OpenAI-Compatible Acceptance Stack"
	echo
	echo "## Bundle"
	echo
	echo "- Bundle run directory: \`${STACK_DIR}\`"
	echo "- Model: \`${MODEL}\`"
	echo "- Base URL: \`${LIVE_BASE_URL}\`"
	echo "- Effective config: \`${CONFIG_PATH}\`"
	echo "- Continue follow-up after matrix failure: \`${CONTINUE_AFTER_MATRIX_FAILURE}\`"
	echo "- Skip focused follow-up: \`${SKIP_FOLLOWUP}\`"
	echo "- Preflight attempts allowed: \`${PREFLIGHT_ATTEMPTS}\`"
	echo "- Preflight retry delay seconds: \`${PREFLIGHT_RETRY_DELAY_SEC}\`"
	echo
	echo "## Bundle Preflight"
	echo
	echo "- Status: \`${PREFLIGHT_STATUS}\`"
	echo "- Exit code: \`${PREFLIGHT_EXIT}\`"
	echo "- Attempts used: \`${PREFLIGHT_LAST_ATTEMPT}/${PREFLIGHT_ATTEMPTS}\`"
	echo "- Last raw: \`${PREFLIGHT_LAST_PATH}\`"
	echo "- Notes index: \`${NOTE_DIR}/preflight-index.tsv\`"
	if [[ -n "$PREFLIGHT_REASON" ]]; then
		echo "- Failure reason: \`${PREFLIGHT_REASON}\`"
	fi
	echo
	echo "## Full Matrix"
	echo
	echo "- Run directory: \`${MATRIX_RUN_DIR}\`"
	echo "- Status: \`${MATRIX_STATUS}\`"
	echo "- Exit code: \`${MATRIX_EXIT}\`"
	echo "- Summary: \`${MATRIX_RUN_DIR}/SUMMARY.md\`"
	echo "- Issues: \`${MATRIX_RUN_DIR}/ISSUES.md\`"
	echo "- Wrapper log: \`${MATRIX_LOG}\`"
	echo
	echo "## Focused Follow-Up"
	echo
	echo "- Run directory: \`${FOLLOWUP_RUN_DIR}\`"
	echo "- Status: \`${FOLLOWUP_STATUS}\`"
	echo "- Exit code: \`${FOLLOWUP_EXIT}\`"
	echo "- Summary: \`${FOLLOWUP_RUN_DIR}/SUMMARY.md\`"
	echo "- Wrapper log: \`${FOLLOWUP_LOG}\`"
	echo
	echo "## Notes"
	echo
	echo "- This acceptance stack now starts with a bundle-level \`probe-provider\` gate before it spends time on the full live matrix."
	echo "- If the repeated preflight probe still fails, the bundle writes \`ABORTED.md\`, skips both live phases, and preserves the probe evidence under \`raw/\` and \`notes/\`."
	echo "- It intentionally runs the full matrix first and the focused Web-first console follow-up second, so broad runtime regressions and the narrower retry/webconsole evidence can be reviewed together."
	echo "- The focused follow-up still uses evidence-first retry proof semantics: durable retry metadata plus a real \`provider.retry\` event are the primary pass criteria even if the bounded finish nudges leave the retry session in \`awaiting_input\`."
} >"$SUMMARY_PATH"

if [[ "$PREFLIGHT_STATUS" != "passed" ]]; then
	{
		echo "# Acceptance Stack Aborted"
		echo
		echo "- Reason: bundle preflight probe failed after ${PREFLIGHT_LAST_ATTEMPT} attempt(s)"
		echo "- Failure note: ${PREFLIGHT_REASON}"
		echo "- Inspect \`${PREFLIGHT_LAST_PATH}\` and \`${NOTE_DIR}/preflight-index.tsv\` first."
		echo "- No live matrix or focused webconsole follow-up phase was launched in this bundle."
	} >"$ABORTED_PATH"
fi

if [[ "$PREFLIGHT_STATUS" != "passed" || "$MATRIX_EXIT" != "0" || ( "$FOLLOWUP_STATUS" != "skipped" && "$FOLLOWUP_EXIT" != "0" ) ]]; then
	{
		echo "# Acceptance Stack Issues"
		echo
		echo "- Bundle preflight status: ${PREFLIGHT_STATUS} (\`${PREFLIGHT_EXIT}\`)"
		if [[ -n "$PREFLIGHT_REASON" ]]; then
			echo "- Bundle preflight reason: ${PREFLIGHT_REASON}"
		fi
		echo "- Full matrix status: ${MATRIX_STATUS} (\`${MATRIX_EXIT}\`)"
		echo "- Focused follow-up status: ${FOLLOWUP_STATUS} (\`${FOLLOWUP_EXIT}\`)"
		echo "- Inspect \`${NOTE_DIR}/preflight-index.tsv\`, \`${NOTE_DIR}/phase-index.tsv\`, and each note under \`${NOTE_DIR}/\` for wrapper-level evidence."
		if [[ "$PREFLIGHT_STATUS" == "passed" ]]; then
			echo "- Review child summaries and issues directly under \`${MATRIX_RUN_DIR}\` and \`${FOLLOWUP_RUN_DIR}\` before drawing product conclusions."
		else
			echo "- Because bundle preflight failed, treat this bundle as operator/connectivity evidence rather than a product regression verdict."
		fi
	} >"$ISSUES_PATH"
fi

printf '%s\n' "$STACK_DIR"
if [[ "$PREFLIGHT_STATUS" != "passed" || "$MATRIX_EXIT" != "0" || ( "$FOLLOWUP_STATUS" != "skipped" && "$FOLLOWUP_EXIT" != "0" ) ]]; then
	exit 1
fi
