#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

: "${OPENAI_API_KEY:?OPENAI_API_KEY is required}"

MODEL="${GO_CLI_AGENT_LIVE_MODEL:-gpt-5.4}"
UPSTREAM_BASE_URL="${GO_CLI_AGENT_LIVE_RESPONSES_URL:-http://64.186.236.156:24634/v1}"
MATRIX_LABEL="${GO_CLI_AGENT_MATRIX_LABEL:-focused-retry-resume-webconsole-queue-followup}"
ROUND_ID="${1:-$(date -u +%F)-openai-compatible-${MODEL}-${MATRIX_LABEL}}"
RUN_DIR="validation/runs/${ROUND_ID}"
RAW_DIR="${RUN_DIR}/raw"
ARTIFACT_DIR="${RUN_DIR}/artifacts"
NOTE_DIR="${RUN_DIR}/notes"
EVIDENCE_DIR="${RUN_DIR}/evidence"
BIN_DIR="${RUN_DIR}/bin"
SUMMARY_PATH="${RUN_DIR}/SUMMARY.md"
ISSUES_PATH="${RUN_DIR}/ISSUES.md"
ABORTED_PATH="${RUN_DIR}/ABORTED.md"
SESSION_ROOT_ABS="${ROOT_DIR}/${RUN_DIR}/sessions"
ISOLATION_ROOT_ABS="${ROOT_DIR}/${RUN_DIR}/worktrees"
CONFIG_RETRY2="${ROOT_DIR}/${RUN_DIR}/config.retry2.yaml"
CONFIG_RETRY1="${ROOT_DIR}/${RUN_DIR}/config.retry1.yaml"
RETRY_MARKER="RETRY_DRIFT_PROOF"
RETRY_WORKDIR="${ROOT_DIR}/${RUN_DIR}/retry-workdir"
WORKSPACE_STAGE_DIR="${ROOT_DIR}/${RUN_DIR}/workspaces"
DOCSET_SOURCE_DIR="${ROOT_DIR}/validation/workspaces/docset"
PLATFORM_PY_SOURCE_DIR="${ROOT_DIR}/validation/workspaces/platform_py"
DOCSET_DIR="${WORKSPACE_STAGE_DIR}/docset"
PLATFORM_PY_DIR="${WORKSPACE_STAGE_DIR}/platform_py"
UI_SMOKE_JSON="${RAW_DIR}/webconsole-ui-smoke.json"
UI_SMOKE_DOM="${RAW_DIR}/webconsole-ui-smoke.html"
PRE_SMOKE_WORKERS_SCALE_JSON="${RAW_DIR}/pre-smoke-workers-scale.json"
PRE_SMOKE_FAILED_JOB_JSON="${RAW_DIR}/pre-smoke-failed-job.json"
PRE_SMOKE_FAILED_JOB_DETAIL_JSON="${RAW_DIR}/pre-smoke-failed-job-detail.json"
PROXY_READY_PATH="${RAW_DIR}/retry-proxy-url.txt"
PROXY_REQUEST_LOG="${RAW_DIR}/retry-proxy-requests.jsonl"
PROXY_LOG="${RAW_DIR}/retry-proxy.log"
WEB_A_LOG="${RAW_DIR}/webconsole.retry2.log"
WEB_B_LOG="${RAW_DIR}/webconsole.retry1.log"
CHROME_BIN_LOG="${RAW_DIR}/chrome-bin.txt"
WEB_A_PORT="$((34000 + RANDOM % 1000))"
WEB_B_PORT="$((35000 + RANDOM % 1000))"
WEB_A_BASE_URL="http://127.0.0.1:${WEB_A_PORT}"
WEB_B_BASE_URL="http://127.0.0.1:${WEB_B_PORT}"
PREFLIGHT_ATTEMPTS="${GO_CLI_AGENT_FOLLOWUP_PREFLIGHT_ATTEMPTS:-2}"
PREFLIGHT_RETRY_DELAY_SEC="${GO_CLI_AGENT_FOLLOWUP_PREFLIGHT_RETRY_DELAY_SEC:-5}"

PROXY_PID=""
WEB_PID=""
CURRENT_PHASE="setup"
FAILURE_NOTE=""

if [[ -e "$RUN_DIR" ]]; then
	echo "run directory already exists: $RUN_DIR" >&2
	exit 1
fi

mkdir -p "$RAW_DIR" "$ARTIFACT_DIR" "$NOTE_DIR" "$EVIDENCE_DIR" "$BIN_DIR" "$SESSION_ROOT_ABS" "$ISOLATION_ROOT_ABS" "$WORKSPACE_STAGE_DIR"
printf 'check\tstatus\texit_code\tpath\n' >"${NOTE_DIR}/preflight-index.tsv"

cp -R "$DOCSET_SOURCE_DIR" "$DOCSET_DIR"
cp -R "$PLATFORM_PY_SOURCE_DIR" "$PLATFORM_PY_DIR"

mkdir -p "$RETRY_WORKDIR"
cat >"${RETRY_WORKDIR}/AGENTS.md" <<'EOF'
# Retry Proof Workspace

- 这个工作区只用于 retry-resume 验证，不是 review / audit 任务。
- 除非用户明确要求，否则不要读取工作区外文件。
- 当提示要求 finish 时，直接调用 `finish`。
- 不要写报告、审计结论或其他 artifact。
EOF
cat >"${RETRY_WORKDIR}/README.md" <<'EOF'
# Retry Proof Workspace

This directory exists only to provide a deterministic non-review workdir for
the retry-resume follow-up validation harness.
EOF

write_failure_outputs() {
	local exit_code="${1:-1}"
	if [[ -f "$SUMMARY_PATH" ]]; then
		return 0
	fi
	local failure_note="$FAILURE_NOTE"
	if [[ -z "$failure_note" && "${PREFLIGHT_STATUS:-}" == "failed" && -n "${PREFLIGHT_LAST_REASON:-}" ]]; then
		failure_note="$PREFLIGHT_LAST_REASON"
	fi
	if [[ -z "$failure_note" ]]; then
		failure_note="phase ${CURRENT_PHASE} exited with status ${exit_code}"
	fi
	{
		echo "# Focused Follow-Up Summary"
		echo
		echo "## Run metadata"
		echo
		echo "- Run directory: \`${RUN_DIR}\`"
		echo "- Provider: \`openai-compatible\`"
		echo "- Model: \`${MODEL}\`"
		echo "- Upstream base URL: \`${UPSTREAM_BASE_URL}\`"
		echo "- Status: \`failed\`"
		echo "- Failed phase: \`${CURRENT_PHASE}\`"
		echo "- Failure reason: \`${failure_note}\`"
		echo "- Preflight index: \`notes/preflight-index.tsv\`"
		if [[ -n "${PREFLIGHT_LAST_ATTEMPT:-}" ]]; then
			echo "- Preflight attempts used: \`${PREFLIGHT_LAST_ATTEMPT}/${PREFLIGHT_ATTEMPTS}\`"
		fi
	} >"$SUMMARY_PATH"
	{
		echo "# Focused Follow-Up Aborted"
		echo
		echo "- Failed phase: ${CURRENT_PHASE}"
		echo "- Failure note: ${failure_note}"
		echo "- Inspect \`${NOTE_DIR}/preflight-index.tsv\` plus the raw files under \`${RAW_DIR}/\`."
	} >"$ABORTED_PATH"
	{
		echo "# Focused Follow-Up Issues"
		echo
		echo "- Failed phase: ${CURRENT_PHASE}"
		echo "- Failure reason: ${failure_note}"
		echo "- Inspect \`${NOTE_DIR}/preflight-index.tsv\` and the raw artifacts under \`${RAW_DIR}/\` for phase-specific evidence."
	} >"$ISSUES_PATH"
}

cleanup() {
	local exit_code=$?
	trap - EXIT
	if (( exit_code != 0 )); then
		write_failure_outputs "$exit_code"
	fi
	if [[ -n "$WEB_PID" ]] && kill -0 "$WEB_PID" 2>/dev/null; then
		kill "$WEB_PID" 2>/dev/null || true
		wait "$WEB_PID" 2>/dev/null || true
	fi
	if [[ -n "$PROXY_PID" ]] && kill -0 "$PROXY_PID" 2>/dev/null; then
		kill "$PROXY_PID" 2>/dev/null || true
		wait "$PROXY_PID" 2>/dev/null || true
	fi
	exit "$exit_code"
}
trap cleanup EXIT

extract_json_field() {
	local path="$1"
	local field="$2"
	if [[ ! -f "$path" ]]; then
		return 0
	fi
	grep -o "\"${field}\":\"[^\"]*\"" "$path" | head -n1 | cut -d'"' -f4
}

status_from_exit() {
	local exit_code="$1"
	if (( exit_code == 0 )); then
		printf 'passed'
	else
		printf 'failed'
	fi
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

preflight_failure_reason() {
	local path="$1"
	if [[ ! -f "$path" ]]; then
		printf 'preflight output missing'
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

is_retryable_preflight_note() {
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

write_preflight_abort_outputs() {
	{
		echo "# Focused Follow-Up Summary"
		echo
		echo "## Run metadata"
		echo
		echo "- Run directory: \`${RUN_DIR}\`"
		echo "- Provider: \`openai-compatible\`"
		echo "- Model: \`${MODEL}\`"
		echo "- Upstream base URL: \`${UPSTREAM_BASE_URL}\`"
		echo "- Status: \`aborted during preflight\`"
		echo "- Preflight attempts used: \`${PREFLIGHT_LAST_ATTEMPT}/${PREFLIGHT_ATTEMPTS}\`"
		echo "- Last failing output: \`${PREFLIGHT_LAST_FAILING_OUTPUT}\`"
		echo "- Failure reason: \`${PREFLIGHT_LAST_REASON}\`"
		echo "- Preflight index: \`notes/preflight-index.tsv\`"
	} >"$SUMMARY_PATH"
	{
		echo "# Focused Follow-Up Aborted"
		echo
		echo "- Reason: preflight failed after ${PREFLIGHT_LAST_ATTEMPT} attempt(s)"
		echo "- Failure note: ${PREFLIGHT_LAST_REASON}"
		echo "- Inspect \`${PREFLIGHT_LAST_FAILING_OUTPUT}\` and \`${NOTE_DIR}/preflight-index.tsv\` first."
		echo "- No webconsole retry-resume, queue, or browser interaction phase was launched in this run."
	} >"$ABORTED_PATH"
	{
		echo "# Focused Follow-Up Issues"
		echo
		echo "- Preflight status: failed"
		echo "- Failure reason: ${PREFLIGHT_LAST_REASON}"
		echo "- Inspect \`${NOTE_DIR}/preflight-index.tsv\` and the per-attempt notes under \`${NOTE_DIR}/\`."
		echo "- Treat this run as operator/connectivity evidence rather than a product verdict."
	} >"$ISSUES_PATH"
}

run_preflight_suite() {
	local attempt=1
	while (( attempt <= PREFLIGHT_ATTEMPTS )); do
		local doctor2_path="${RAW_DIR}/preflight-doctor-retry2-attempt${attempt}.json"
		local probe2_path="${RAW_DIR}/preflight-probe-retry2-attempt${attempt}.json"
		local doctor1_path="${RAW_DIR}/preflight-doctor-retry1-attempt${attempt}.json"
		local doctor2_exit=0
		local probe2_exit=0
		local doctor1_exit=0
		local failing_path=""

		OPENAI_API_KEY="$OPENAI_API_KEY" ./bin/go-cli-agent doctor --config "$CONFIG_RETRY2" --provider openai-compatible --json >"$doctor2_path" 2>&1 || doctor2_exit=$?
		record_preflight "preflight-doctor-retry2-attempt-${attempt}" "$doctor2_exit" "$doctor2_path"
		write_preflight_note "preflight-doctor-retry2-attempt-${attempt}" "$doctor2_exit" "$doctor2_path"
		if (( doctor2_exit != 0 )) && [[ -z "$failing_path" ]]; then
			failing_path="$doctor2_path"
		fi

		OPENAI_API_KEY="$OPENAI_API_KEY" ./bin/go-cli-agent probe-provider --config "$CONFIG_RETRY2" --provider openai-compatible --model "$MODEL" --json >"$probe2_path" 2>&1 || probe2_exit=$?
		record_preflight "preflight-probe-retry2-attempt-${attempt}" "$probe2_exit" "$probe2_path"
		write_preflight_note "preflight-probe-retry2-attempt-${attempt}" "$probe2_exit" "$probe2_path"
		if (( probe2_exit != 0 )) && [[ -z "$failing_path" ]]; then
			failing_path="$probe2_path"
		fi

		OPENAI_API_KEY="$OPENAI_API_KEY" ./bin/go-cli-agent doctor --config "$CONFIG_RETRY1" --provider openai-compatible --json >"$doctor1_path" 2>&1 || doctor1_exit=$?
		record_preflight "preflight-doctor-retry1-attempt-${attempt}" "$doctor1_exit" "$doctor1_path"
		write_preflight_note "preflight-doctor-retry1-attempt-${attempt}" "$doctor1_exit" "$doctor1_path"
		if (( doctor1_exit != 0 )) && [[ -z "$failing_path" ]]; then
			failing_path="$doctor1_path"
		fi

		PREFLIGHT_LAST_ATTEMPT="$attempt"
		PREFLIGHT_RETRY2_DOCTOR_PATH="$doctor2_path"
		PREFLIGHT_RETRY2_PROBE_PATH="$probe2_path"
		PREFLIGHT_RETRY1_DOCTOR_PATH="$doctor1_path"

		if (( doctor2_exit == 0 && probe2_exit == 0 && doctor1_exit == 0 )); then
			PREFLIGHT_STATUS="passed"
			PREFLIGHT_LAST_REASON=""
			PREFLIGHT_LAST_FAILING_OUTPUT=""
			return 0
		fi

		PREFLIGHT_STATUS="failed"
		PREFLIGHT_LAST_FAILING_OUTPUT="$failing_path"
		PREFLIGHT_LAST_REASON="$(preflight_failure_reason "$failing_path")"
		if (( attempt >= PREFLIGHT_ATTEMPTS )) || ! is_retryable_preflight_note "$PREFLIGHT_LAST_REASON"; then
			return 1
		fi
		if (( PREFLIGHT_RETRY_DELAY_SEC > 0 )); then
			sleep "$PREFLIGHT_RETRY_DELAY_SEC"
		fi
		attempt=$((attempt + 1))
	done
	return 1
}

require_command() {
	local name="$1"
	local hint="${2:-}"
	if command -v "$name" >/dev/null 2>&1; then
		return 0
	fi
	if [[ -n "$hint" ]]; then
		echo "missing required command '$name' (${hint})" >&2
	else
		echo "missing required command '$name'" >&2
	fi
	exit 1
}

resolve_chrome_bin() {
	local candidate=""
	if [[ -n "${CHROME_BIN:-}" ]]; then
		candidate="${CHROME_BIN}"
		if [[ "$candidate" == */* ]]; then
			if [[ -x "$candidate" ]]; then
				printf '%s\n' "$candidate"
				return 0
			fi
			echo "CHROME_BIN is not executable: ${candidate}" >&2
			exit 1
		fi
		if command -v "$candidate" >/dev/null 2>&1; then
			command -v "$candidate"
			return 0
		fi
		echo "CHROME_BIN command not found: ${candidate}" >&2
		exit 1
	fi
	for candidate in google-chrome chromium chromium-browser; do
		if command -v "$candidate" >/dev/null 2>&1; then
			command -v "$candidate"
			return 0
		fi
	done
	echo "missing Chrome-compatible browser; install google-chrome/chromium or set CHROME_BIN" >&2
	exit 1
}

write_config() {
	local path="$1"
	local retry_attempts="$2"
	local base_url="$3"
	cat >"$path" <<EOF
schema_version: 1

default_provider: openai-compatible

providers:
  openai-compatible:
    api_key_env: OPENAI_API_KEY
    base_url: ${base_url}
    model: ${MODEL}
    timeout_sec: 240
    retry:
      max_attempts: ${retry_attempts}
      base_delay_ms: 100
      retry_5xx: true
      retry_transport: true
    wire_api: responses
    max_output_tokens: 3072
    reasoning_effort: medium
    text_verbosity: low
    store: false
    send_metadata: false

session:
  dir: ${SESSION_ROOT_ABS}
  dir_mode: "0700"

skills:
  dirs:
    - ${ROOT_DIR}/skills
    - ${ROOT_DIR}/validation/skills

runtime:
  exec_finish_required: true
  max_turns_soft: 24
  max_turns_hard: 40
  command_timeout_sec: 180
  steer:
    poll_interval_ms: 250
    default_behavior: queue
  shell_env_allowlist:
    - PATH
    - HOME
    - LANG
    - TERM
  compact:
    input_char_threshold: 160000
    keep_recent_tool_results: 3
  isolation:
    default_mode: off
    root_dir: ${ISOLATION_ROOT_ABS}
  queue:
    poll_interval_ms: 250
    auto_worker: true

output:
  format: text
  show_raw_events: false

hooks:
  default_timeout_sec: 15
EOF
}

post_json() {
	local url="$1"
	local payload="$2"
	local out="$3"
	curl -sS -o "$out" -w '%{http_code}' \
		-H 'Content-Type: application/json' \
		-X POST \
		--data "$payload" \
		"$url"
}

get_json() {
	local url="$1"
	local out="$2"
	curl -sS -o "$out" -w '%{http_code}' "$url"
}

get_json_quiet() {
	local url="$1"
	local out="$2"
	curl -sS -o "$out" -w '%{http_code}' "$url" 2>/dev/null || true
}

wait_for_http_ok() {
	local url="$1"
	local out="$2"
	local timeout_sec="${3:-60}"
	local waited=0
	while (( waited < timeout_sec )); do
		local status
		status="$(get_json_quiet "$url" "${out}.tmp")"
		if [[ "$status" == "200" ]]; then
			mv "${out}.tmp" "$out"
			return 0
		fi
		sleep 1
		waited=$((waited + 1))
	done
	echo "timed out waiting for ${url}" >&2
	return 1
}

wait_for_session_detail() {
	local base_url="$1"
	local session_id="$2"
	local out="$3"
	local pattern="$4"
	local timeout_sec="${5:-120}"
	local waited=0
	while (( waited < timeout_sec )); do
		local status
		status="$(get_json_quiet "${base_url}/api/sessions/${session_id}" "${out}.tmp")"
		if [[ "$status" == "200" ]]; then
			mv "${out}.tmp" "$out"
			if grep -Fq "$pattern" "$out"; then
				return 0
			fi
		fi
		sleep 1
		waited=$((waited + 1))
	done
	echo "timed out waiting for session ${session_id} pattern ${pattern}" >&2
	return 1
}

wait_for_session_status() {
	local base_url="$1"
	local session_id="$2"
	local out="$3"
	local timeout_sec="$4"
	shift 4
	local waited=0
	while (( waited < timeout_sec )); do
		local status
		status="$(get_json_quiet "${base_url}/api/sessions/${session_id}" "${out}.tmp")"
		if [[ "$status" == "200" ]]; then
			mv "${out}.tmp" "$out"
			local desired=""
			for desired in "$@"; do
				if grep -Fq "\"state\":{\"status\":\"${desired}\"" "$out"; then
					return 0
				fi
			done
		fi
		sleep 1
		waited=$((waited + 1))
	done
	echo "timed out waiting for session ${session_id} status in [$*]" >&2
	return 1
}

wait_for_job_status() {
	local base_url="$1"
	local job_id="$2"
	local out="$3"
	local timeout_sec="$4"
	shift 4
	local waited=0
	while (( waited < timeout_sec )); do
		local status
		status="$(get_json_quiet "${base_url}/api/queue/jobs/${job_id}" "${out}.tmp")"
		if [[ "$status" == "200" ]]; then
			mv "${out}.tmp" "$out"
			local desired=""
			for desired in "$@"; do
				if grep -Fq "$desired" "$out"; then
					return 0
				fi
			done
		fi
		sleep 1
		waited=$((waited + 1))
	done
	echo "timed out waiting for job ${job_id} pattern in [$*]" >&2
	return 1
}

start_webconsole() {
	local config_path="$1"
	local listen_addr="$2"
	local log_path="$3"
	./bin/go-cli-agent experimental web --config "$config_path" --listen "$listen_addr" --workers 0 >"$log_path" 2>&1 &
	WEB_PID=$!
	wait_for_http_ok "http://${listen_addr}/api/meta" "${RAW_DIR}/meta.$(basename "$log_path").json" 60 >/dev/null
}

stop_webconsole() {
	if [[ -n "$WEB_PID" ]] && kill -0 "$WEB_PID" 2>/dev/null; then
		kill "$WEB_PID" 2>/dev/null || true
		wait "$WEB_PID" 2>/dev/null || true
	fi
	WEB_PID=""
}

copy_session_evidence() {
	local session_id="$1"
	local target_dir="$2"
	mkdir -p "$target_dir"
	cp "${SESSION_ROOT_ABS}/${session_id}/session.json" "${target_dir}/session.json"
	cp "${SESSION_ROOT_ABS}/${session_id}/state.json" "${target_dir}/state.json"
	cp "${SESSION_ROOT_ABS}/${session_id}/events.jsonl" "${target_dir}/events.jsonl"
	cp "${SESSION_ROOT_ABS}/${session_id}/messages.jsonl" "${target_dir}/messages.jsonl"
	cp "${SESSION_ROOT_ABS}/${session_id}/control/background.jsonl" "${target_dir}/background.jsonl"
}

count_lines() {
	local path="$1"
	if [[ ! -f "$path" ]]; then
		echo "0"
		return 0
	fi
	wc -l <"$path" | tr -d ' '
}

count_unique_queue_job_ids() {
	local path="$1"
	if [[ ! -f "$path" ]]; then
		echo "0"
		return 0
	fi
	(grep -o '"queue_job_id":"[^"]*"' "$path" || true) | sort -u | wc -l | tr -d ' '
}

require_command go
require_command curl
require_command node "needed for browser UI smoke"
CHROME_BIN_RESOLVED="$(resolve_chrome_bin)"
printf '%s\n' "$CHROME_BIN_RESOLVED" >"$CHROME_BIN_LOG"

printf '== build ==\n'
CURRENT_PHASE="build"
./build.sh >"${RAW_DIR}/build.txt" 2>&1
go build -o "${BIN_DIR}/retryproxy" ./validation/cmd/retryproxy >"${RAW_DIR}/build-retryproxy.txt" 2>&1

printf '== focused unit regressions ==\n'
CURRENT_PHASE="focused unit regressions"
go test ./internal/runtime -run TestRunnerContinueUsesDurableRetryPolicyFromSessionMetadata -count=1 >"${RAW_DIR}/preflight-runtime-retry.txt" 2>&1
go test ./internal/webconsole -run TestServiceParallelQueueWorkersPersistAllJobs -count=1 >"${RAW_DIR}/preflight-webconsole-dedup.txt" 2>&1
go test ./internal/webconsole -run TestServiceServesEmbeddedShellAndAssets -count=1 >"${RAW_DIR}/preflight-webconsole-assets.txt" 2>&1

printf '== retry injection proxy ==\n'
CURRENT_PHASE="retry injection proxy"
"${BIN_DIR}/retryproxy" \
	--listen "127.0.0.1:0" \
	--upstream "$UPSTREAM_BASE_URL" \
	--match-substring "$RETRY_MARKER" \
	--ready-file "$PROXY_READY_PATH" \
	--request-log "$PROXY_REQUEST_LOG" \
	>"$PROXY_LOG" 2>&1 &
PROXY_PID=$!

waited=0
while [[ ! -f "$PROXY_READY_PATH" ]]; do
	if ! kill -0 "$PROXY_PID" 2>/dev/null; then
		echo "retryproxy exited early" >&2
		exit 1
	fi
	sleep 1
	waited=$((waited + 1))
	if (( waited >= 30 )); then
		echo "timed out waiting for retryproxy ready file" >&2
		exit 1
	fi
done
PROXY_BASE_URL="$(tr -d '\n' <"$PROXY_READY_PATH")"

write_config "$CONFIG_RETRY2" 2 "$PROXY_BASE_URL"
write_config "$CONFIG_RETRY1" 1 "$PROXY_BASE_URL"

printf '== doctor and probe ==\n'
CURRENT_PHASE="doctor and probe"
PREFLIGHT_STATUS="passed"
PREFLIGHT_LAST_ATTEMPT=0
PREFLIGHT_LAST_REASON=""
PREFLIGHT_LAST_FAILING_OUTPUT=""
PREFLIGHT_RETRY2_DOCTOR_PATH=""
PREFLIGHT_RETRY2_PROBE_PATH=""
PREFLIGHT_RETRY1_DOCTOR_PATH=""
if ! run_preflight_suite; then
	write_preflight_abort_outputs
	printf 'focused follow-up summary written to %s\n' "$SUMMARY_PATH"
	exit 1
fi

printf '== webconsole retry session start ==\n'
CURRENT_PHASE="webconsole retry session start"
start_webconsole "$CONFIG_RETRY2" "127.0.0.1:${WEB_A_PORT}" "$WEB_A_LOG"

RETRY_START_PAYLOAD="$(cat <<EOF
{"prompt":"Reply with exactly the plain text WAITING. Do not call any tool.","provider":"openai-compatible","model":"${MODEL}","workdir":"${RETRY_WORKDIR}","mode":"run"}
EOF
)"
RETRY_START_STATUS="$(post_json "${WEB_A_BASE_URL}/api/sessions/start" "$RETRY_START_PAYLOAD" "${RAW_DIR}/retry-start.json")"
if [[ "$RETRY_START_STATUS" != "202" ]]; then
	echo "unexpected retry start status: ${RETRY_START_STATUS}" >&2
	exit 1
fi
PARENT_SESSION_ID="$(extract_json_field "${RAW_DIR}/retry-start.json" "session_id")"
if [[ -z "$PARENT_SESSION_ID" ]]; then
	echo "missing session id from retry start" >&2
	exit 1
fi
wait_for_session_detail "$WEB_A_BASE_URL" "$PARENT_SESSION_ID" "${RAW_DIR}/retry-awaiting-detail.json" '"state":{"status":"awaiting_input"' 180
copy_session_evidence "$PARENT_SESSION_ID" "${EVIDENCE_DIR}/retry-session-before-continue"
stop_webconsole

printf '== webconsole retry session continue with drifted config ==\n'
CURRENT_PHASE="webconsole retry session continue with drifted config"
start_webconsole "$CONFIG_RETRY1" "127.0.0.1:${WEB_B_PORT}" "$WEB_B_LOG"

RETRY_CONTINUE_PAYLOAD="$(cat <<EOF
{"message":"This is a retry-resume transport proof, not a review or audit task. Do not read or write any files. Immediately call finish with exact message: ${RETRY_MARKER} continue ok"}
EOF
)"
RETRY_CONTINUE_STATUS="$(post_json "${WEB_B_BASE_URL}/api/sessions/${PARENT_SESSION_ID}/continue" "$RETRY_CONTINUE_PAYLOAD" "${RAW_DIR}/retry-continue.json")"
if [[ "$RETRY_CONTINUE_STATUS" != "202" ]]; then
	echo "unexpected retry continue status: ${RETRY_CONTINUE_STATUS}" >&2
	exit 1
fi
wait_for_session_detail "$WEB_B_BASE_URL" "$PARENT_SESSION_ID" "${RAW_DIR}/retry-after-first-continue.json" '"type":"provider.retry"' 180
wait_for_session_status "$WEB_B_BASE_URL" "$PARENT_SESSION_ID" "${RAW_DIR}/retry-after-first-continue.json" 180 completed awaiting_input
RETRY_COMPLETION_ATTEMPTS=1
RETRY_FINAL_STATUS="unknown"
if grep -Fq '"state":{"status":"awaiting_input"' "${RAW_DIR}/retry-after-first-continue.json"; then
	printf '%s\n' "first continue returned awaiting_input after provider.retry; sending finish-only follow-up continue" >"${NOTE_DIR}/retry-second-continue.txt"
	RETRY_SECOND_CONTINUE_PAYLOAD="$(cat <<EOF
{"message":"Do not read or write files. Call finish now with exact message: ${RETRY_MARKER} continue ok"}
EOF
)"
	RETRY_SECOND_CONTINUE_STATUS="$(post_json "${WEB_B_BASE_URL}/api/sessions/${PARENT_SESSION_ID}/continue" "$RETRY_SECOND_CONTINUE_PAYLOAD" "${RAW_DIR}/retry-second-continue.json")"
	if [[ "$RETRY_SECOND_CONTINUE_STATUS" != "202" ]]; then
		echo "unexpected retry second-continue status: ${RETRY_SECOND_CONTINUE_STATUS}" >&2
		exit 1
	fi
	wait_for_session_status "$WEB_B_BASE_URL" "$PARENT_SESSION_ID" "${RAW_DIR}/retry-final-detail.json" 180 completed awaiting_input
	RETRY_COMPLETION_ATTEMPTS=2
else
	cp "${RAW_DIR}/retry-after-first-continue.json" "${RAW_DIR}/retry-final-detail.json"
fi
if grep -Fq '"state":{"status":"completed"' "${RAW_DIR}/retry-final-detail.json"; then
	RETRY_FINAL_STATUS="completed"
elif grep -Fq '"state":{"status":"awaiting_input"' "${RAW_DIR}/retry-final-detail.json"; then
	RETRY_FINAL_STATUS="awaiting_input"
	printf '%s\n' "retry proof validated, but the bounded finish nudges still left the session in awaiting_input; provider.retry and durable retry metadata were already captured." >"${NOTE_DIR}/retry-finish-remained-awaiting-input.txt"
else
	echo "unexpected retry final session status; expected completed or awaiting_input" >&2
	exit 1
fi
copy_session_evidence "$PARENT_SESSION_ID" "${EVIDENCE_DIR}/retry-session-after-continue"

if ! grep -Fq '"type":"provider.retry"' "${RAW_DIR}/retry-final-detail.json"; then
	echo "expected provider.retry event in retry-final-detail.json" >&2
	exit 1
fi
if grep -Fq '"max_attempts":1' "${RAW_DIR}/retry-final-detail.json"; then
	echo "unexpected drifted max_attempts=1 persisted into resumed session detail" >&2
	exit 1
fi
RETRY_POLICY_TWO_COUNT="$(grep -o '"max_attempts":2' "${RAW_DIR}/retry-final-detail.json" | wc -l | tr -d ' ')"
if (( RETRY_POLICY_TWO_COUNT < 2 )); then
	echo "expected at least two retry_policy max_attempts=2 occurrences, got ${RETRY_POLICY_TWO_COUNT}" >&2
	exit 1
fi

printf '== pre-smoke failed queue canary ==\n'
CURRENT_PHASE="pre-smoke failed queue canary"
PRE_SMOKE_SCALE_STATUS="$(post_json "${WEB_B_BASE_URL}/api/workers" '{"desired_count":1}' "${PRE_SMOKE_WORKERS_SCALE_JSON}")"
if [[ "$PRE_SMOKE_SCALE_STATUS" != "202" ]]; then
	echo "unexpected pre-smoke worker scale status: ${PRE_SMOKE_SCALE_STATUS}" >&2
	exit 1
fi

PRE_SMOKE_FAILED_WORKDIR="${ROOT_DIR}/${RUN_DIR}/missing-ui-smoke-workdir"
PRE_SMOKE_FAILED_PAYLOAD="$(cat <<EOF
{"prompt":"This queue canary should fail before the model runs because its workdir is intentionally missing.","workdir":"${PRE_SMOKE_FAILED_WORKDIR}","isolation_mode":"auto","agent_name":"ui-smoke-failed-canary","agent_role":"evaluator","mode":"exec"}
EOF
)"
PRE_SMOKE_FAILED_STATUS="$(post_json "${WEB_B_BASE_URL}/api/queue/jobs" "$PRE_SMOKE_FAILED_PAYLOAD" "${PRE_SMOKE_FAILED_JOB_JSON}")"
if [[ "$PRE_SMOKE_FAILED_STATUS" != "202" ]]; then
	echo "unexpected pre-smoke failed queue create status: ${PRE_SMOKE_FAILED_STATUS}" >&2
	exit 1
fi
PRE_SMOKE_FAILED_JOB_ID="$(extract_json_field "${PRE_SMOKE_FAILED_JOB_JSON}" "id")"
if [[ -z "$PRE_SMOKE_FAILED_JOB_ID" ]]; then
	echo "missing pre-smoke failed queue job id" >&2
	exit 1
fi
wait_for_job_status "$WEB_B_BASE_URL" "$PRE_SMOKE_FAILED_JOB_ID" "${PRE_SMOKE_FAILED_JOB_DETAIL_JSON}" 180 '"status":"failed"'

printf '== browser ui smoke ==\n'
CURRENT_PHASE="browser ui smoke"
node ./validation/scripts/webconsole_ui_smoke.mjs \
	--base-url "$WEB_B_BASE_URL" \
	--workdir "$DOCSET_DIR" \
	--queue-workdir "$PLATFORM_PY_DIR" \
	--output "$UI_SMOKE_JSON" \
	--dom-output "$UI_SMOKE_DOM" \
	--chrome "$CHROME_BIN_RESOLVED"

printf '== queue workers and parent notification ==\n'
CURRENT_PHASE="queue workers and parent notification"
SCALE_STATUS="$(post_json "${WEB_B_BASE_URL}/api/workers" '{"desired_count":2}' "${RAW_DIR}/queue-workers-scale.json")"
if [[ "$SCALE_STATUS" != "202" ]]; then
	echo "unexpected worker scale status: ${SCALE_STATUS}" >&2
	exit 1
fi

JOB1_PAYLOAD="$(cat <<EOF
{"prompt":"Review only README.md and tests/test_report.py in this workspace. Write reports/wc53-child-one.md with sections: scope, findings. Keep it short. Then call finish with message: wc53 child one review ok.","parent_session_id":"${PARENT_SESSION_ID}","workdir":"${PLATFORM_PY_DIR}","isolation_mode":"auto","agent_name":"wc53-child-one","agent_role":"generator","mode":"exec"}
EOF
)"
JOB2_PAYLOAD="$(cat <<EOF
{"prompt":"Review only README.md and tests/test_config.py in this workspace. Write reports/wc53-child-two.md with sections: scope, findings. Keep it short. Then call finish with message: wc53 child two review ok.","parent_session_id":"${PARENT_SESSION_ID}","workdir":"${PLATFORM_PY_DIR}","isolation_mode":"auto","agent_name":"wc53-child-two","agent_role":"evaluator","mode":"exec"}
EOF
)"
JOB1_STATUS="$(post_json "${WEB_B_BASE_URL}/api/queue/jobs" "$JOB1_PAYLOAD" "${RAW_DIR}/queue-job1.json")"
JOB2_STATUS="$(post_json "${WEB_B_BASE_URL}/api/queue/jobs" "$JOB2_PAYLOAD" "${RAW_DIR}/queue-job2.json")"
if [[ "$JOB1_STATUS" != "202" || "$JOB2_STATUS" != "202" ]]; then
	echo "unexpected queue create status job1=${JOB1_STATUS} job2=${JOB2_STATUS}" >&2
	exit 1
fi
JOB1_ID="$(extract_json_field "${RAW_DIR}/queue-job1.json" "id")"
JOB2_ID="$(extract_json_field "${RAW_DIR}/queue-job2.json" "id")"
if [[ -z "$JOB1_ID" || -z "$JOB2_ID" ]]; then
	echo "missing queue job ids" >&2
	exit 1
fi

wait_for_job_status "$WEB_B_BASE_URL" "$JOB1_ID" "${RAW_DIR}/queue-job1-detail.json" 300 '"status":"completed"'
wait_for_job_status "$WEB_B_BASE_URL" "$JOB2_ID" "${RAW_DIR}/queue-job2-detail.json" 300 '"status":"completed"' '"status":"failed"'

wait_for_session_detail "$WEB_B_BASE_URL" "$PARENT_SESSION_ID" "${RAW_DIR}/queue-parent-detail-before-reconcile.json" "\"queue_job_id\":\"${JOB1_ID}\"" 120
cp "${SESSION_ROOT_ABS}/${PARENT_SESSION_ID}/control/background.jsonl" "${RAW_DIR}/queue-background-before-reconcile.jsonl"

QUEUE_COMPLETED_PATH="${SESSION_ROOT_ABS}/_queue/completed/${JOB1_ID}.json"
QUEUE_RUNNING_PATH="${SESSION_ROOT_ABS}/_queue/running/${JOB1_ID}.json"
if [[ ! -f "$QUEUE_COMPLETED_PATH" ]]; then
	echo "missing completed queue job file for ${JOB1_ID}" >&2
	exit 1
fi
mv "$QUEUE_COMPLETED_PATH" "$QUEUE_RUNNING_PATH"
sed -i 's/"status":[[:space:]]*"completed"/"status": "running"/' "$QUEUE_RUNNING_PATH"
printf '%s\n' \
	"forced_job_id=${JOB1_ID}" \
	"forced_running_path=${QUEUE_RUNNING_PATH}" \
	>"${NOTE_DIR}/forced-stale-running-job.txt"

wait_for_session_detail "$WEB_B_BASE_URL" "$PARENT_SESSION_ID" "${RAW_DIR}/queue-parent-detail-after-reconcile.json" "\"queue_job_id\":\"${JOB2_ID}\"" 120
wait_for_job_status "$WEB_B_BASE_URL" "$JOB1_ID" "${RAW_DIR}/queue-job1-detail-after-reconcile.json" 120 '"status":"completed"'
cp "${SESSION_ROOT_ABS}/${PARENT_SESSION_ID}/control/background.jsonl" "${RAW_DIR}/queue-background-after-reconcile.jsonl"

BACKGROUND_BEFORE_COUNT="$(count_lines "${RAW_DIR}/queue-background-before-reconcile.jsonl")"
BACKGROUND_AFTER_COUNT="$(count_lines "${RAW_DIR}/queue-background-after-reconcile.jsonl")"
BACKGROUND_AFTER_UNIQUE="$(count_unique_queue_job_ids "${RAW_DIR}/queue-background-after-reconcile.jsonl")"
if [[ "$BACKGROUND_BEFORE_COUNT" != "2" ]]; then
	echo "expected 2 background notifications before reconcile, got ${BACKGROUND_BEFORE_COUNT}" >&2
	exit 1
fi
if [[ "$BACKGROUND_AFTER_COUNT" != "2" ]]; then
	echo "expected 2 background notifications after reconcile, got ${BACKGROUND_AFTER_COUNT}" >&2
	exit 1
fi
if [[ "$BACKGROUND_AFTER_UNIQUE" != "2" ]]; then
	echo "expected 2 unique queue_job_id values after reconcile, got ${BACKGROUND_AFTER_UNIQUE}" >&2
	exit 1
fi
if ! grep -Fq "\"queue_job_id\":\"${JOB1_ID}\"" "${RAW_DIR}/queue-background-after-reconcile.jsonl"; then
	echo "expected background notification for ${JOB1_ID}" >&2
	exit 1
fi
if ! grep -Fq "\"queue_job_id\":\"${JOB2_ID}\"" "${RAW_DIR}/queue-background-after-reconcile.jsonl"; then
	echo "expected background notification for ${JOB2_ID}" >&2
	exit 1
fi

copy_session_evidence "$PARENT_SESSION_ID" "${EVIDENCE_DIR}/parent-session-after-queue"

cat >"$SUMMARY_PATH" <<EOF
# Focused Follow-Up Summary

## Run metadata

- Run directory: \`${RUN_DIR}\`
- Provider: \`openai-compatible\`
- Model: \`${MODEL}\`
- Upstream base URL: \`${UPSTREAM_BASE_URL}\`
- Proxy base URL: \`${PROXY_BASE_URL}\`
- Browser binary: \`${CHROME_BIN_RESOLVED}\`
- Parent session: \`${PARENT_SESSION_ID}\`
- Preflight attempts used: \`${PREFLIGHT_LAST_ATTEMPT}/${PREFLIGHT_ATTEMPTS}\`
- Retry2 doctor: \`${PREFLIGHT_RETRY2_DOCTOR_PATH}\`
- Retry2 probe: \`${PREFLIGHT_RETRY2_PROBE_PATH}\`
- Retry1 doctor: \`${PREFLIGHT_RETRY1_DOCTOR_PATH}\`
- Preflight index: \`notes/preflight-index.tsv\`

## Retry Drift Follow-Up

- Config with durable session creation: \`${CONFIG_RETRY2}\`
- Drifted config used for continue: \`${CONFIG_RETRY1}\`
- Dedicated retry workdir: \`${RETRY_WORKDIR}\`
- Retry completion attempts: \`${RETRY_COMPLETION_ATTEMPTS}\`
- Retry final session status: \`${RETRY_FINAL_STATUS}\`
- Awaiting-input detail: \`raw/retry-awaiting-detail.json\`
- Detail after first continue: \`raw/retry-after-first-continue.json\`
- Final continued detail: \`raw/retry-final-detail.json\`
- Proxy request log: \`raw/retry-proxy-requests.jsonl\`
- Result: resumed session still shows only \`retry_policy.max_attempts=2\` in durable metadata / prepared events, and the resumed turn emitted a real \`provider.retry\`. The script records whether the bounded finish nudges left the session \`completed\` or still \`awaiting_input\`, but either way the retry-drift proof itself is taken from the durable retry metadata plus the real retry event.

## Queue Failure Canary For Browser Drilldown

- Worker scale before smoke: \`raw/pre-smoke-workers-scale.json\`
- Failed canary job: \`${PRE_SMOKE_FAILED_JOB_ID}\` -> \`raw/pre-smoke-failed-job-detail.json\`
- Missing workdir used to force the failure: \`${PRE_SMOKE_FAILED_WORKDIR}\`
- Result: the browser smoke had a real failed queue job plus worker last-job state available before its own queue submit, so overview failure and worker drilldown assertions were exercised against durable failed-job facts rather than mock UI data.

## Queue Notification Dedup Follow-Up

- Worker scale response: \`raw/queue-workers-scale.json\`
- Queue job 1: \`${JOB1_ID}\` -> \`raw/queue-job1-detail.json\`
- Queue job 2: \`${JOB2_ID}\` -> \`raw/queue-job2-detail.json\`
- Parent detail before forced reconcile: \`raw/queue-parent-detail-before-reconcile.json\`
- Parent detail after forced reconcile: \`raw/queue-parent-detail-after-reconcile.json\`
- Background notifications before reconcile: \`${BACKGROUND_BEFORE_COUNT}\`
- Background notifications after reconcile: \`${BACKGROUND_AFTER_COUNT}\`
- Unique queue_job_id values after reconcile: \`${BACKGROUND_AFTER_UNIQUE}\`
- Note: this script forces one real completed queue job back into \`running\` on disk before re-reading parent detail so the stale-running reconcile path is exercised deterministically after a real webconsole queue/review run.

## Web Console Frontend And Interaction Follow-Up

- UI smoke JSON: \`raw/webconsole-ui-smoke.json\`
- UI smoke DOM snapshot: \`raw/webconsole-ui-smoke.html\`
- Shell/assets regression: \`raw/preflight-webconsole-assets.txt\`
- Result: embedded shell and assets were served locally, headless Chrome exercised role-aware start, session sidebar filter/reveal, queue quick-filter pin/reveal, overview recent-job/feed/failed-job drilldowns, worker last-job drilldown, tasks/children/queue tab navigation, continue, worker update, queue submit, queue-links notification rendering, and manual refresh against the real webconsole.

## Evidence Paths

- Retry session evidence before continue: \`evidence/retry-session-before-continue/\`
- Retry session evidence after continue: \`evidence/retry-session-after-continue/\`
- Parent session evidence after queue: \`evidence/parent-session-after-queue/\`
EOF

CURRENT_PHASE="summary write"
printf 'focused follow-up summary written to %s\n' "$SUMMARY_PATH"
