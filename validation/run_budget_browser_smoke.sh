#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

OUTPUT_DIR="${1:-}"
PRESERVE_OUTPUT=false
if [[ -n "$OUTPUT_DIR" ]]; then
	mkdir -p "$OUTPUT_DIR"
	OUTPUT_DIR="$(cd "$OUTPUT_DIR" && pwd)"
	PRESERVE_OUTPUT=true
else
	OUTPUT_DIR="$(mktemp -d /tmp/go-cli-agent-budget-browser-smoke-XXXXXX)"
fi

BIN_DIR="$OUTPUT_DIR/bin"
STATE_DIR="$OUTPUT_DIR/state"
SESSION_ROOT="$STATE_DIR/sessions"
WORKDIR="$OUTPUT_DIR/workspace"
CONFIG_PATH="$OUTPUT_DIR/config.yaml"
PROVIDER_READY="$OUTPUT_DIR/provider-url.txt"
PROVIDER_LOG="$OUTPUT_DIR/provider-decisions.jsonl"
PROVIDER_STDOUT="$OUTPUT_DIR/provider.log"
WEB_LOG="$OUTPUT_DIR/webconsole.log"
UI_JSON="$OUTPUT_DIR/budget-browser-smoke.json"
UI_DOM="$OUTPUT_DIR/budget-browser-smoke.html"
AUDIT_LOG="$STATE_DIR/webconsole-audit.jsonl"
WEB_PORT="$((37000 + RANDOM % 1500))"
WEB_BASE_URL="http://127.0.0.1:${WEB_PORT}"

PROVIDER_PID=""
WEB_PID=""

cleanup() {
	local exit_code=$?
	trap - EXIT
	if [[ -n "$WEB_PID" ]] && kill -0 "$WEB_PID" 2>/dev/null; then
		kill "$WEB_PID" 2>/dev/null || true
		wait "$WEB_PID" 2>/dev/null || true
	fi
	if [[ -n "$PROVIDER_PID" ]] && kill -0 "$PROVIDER_PID" 2>/dev/null; then
		kill "$PROVIDER_PID" 2>/dev/null || true
		wait "$PROVIDER_PID" 2>/dev/null || true
	fi
	if [[ "$PRESERVE_OUTPUT" != true ]]; then
		rm -rf "$OUTPUT_DIR"
	fi
	exit "$exit_code"
}
trap cleanup EXIT INT TERM

resolve_chrome() {
	if [[ -n "${CHROME_BIN:-}" && -x "${CHROME_BIN}" ]]; then
		printf '%s\n' "$CHROME_BIN"
		return
	fi
	local candidate
	for candidate in google-chrome chromium chromium-browser; do
		if command -v "$candidate" >/dev/null 2>&1; then
			command -v "$candidate"
			return
		fi
	done
	printf '%s\n' "missing Chrome-compatible browser" >&2
	exit 1
}

wait_for_file() {
	local path="$1"
	local pid="$2"
	local label="$3"
	local attempt
	for attempt in $(seq 1 150); do
		if [[ -s "$path" ]]; then
			return
		fi
		if ! kill -0 "$pid" 2>/dev/null; then
			printf '%s exited before readiness\n' "$label" >&2
			exit 1
		fi
		sleep 0.1
	done
	printf 'timed out waiting for %s\n' "$label" >&2
	exit 1
}

wait_for_web() {
	local attempt
	for attempt in $(seq 1 200); do
		if curl --fail --silent --show-error "$WEB_BASE_URL/api/meta" >"$OUTPUT_DIR/meta.json" 2>/dev/null; then
			return
		fi
		if ! kill -0 "$WEB_PID" 2>/dev/null; then
			printf '%s\n' "webconsole exited before readiness" >&2
			exit 1
		fi
		sleep 0.1
	done
	printf '%s\n' "timed out waiting for webconsole" >&2
	exit 1
}

mkdir -p "$BIN_DIR" "$SESSION_ROOT" "$WORKDIR/nested" "$OUTPUT_DIR/isolation"
printf '%s\n' "budget browser smoke workspace" >"$WORKDIR/README.txt"
printf '%s\n' "nested directory for browser navigation" >"$WORKDIR/nested/README.txt"

CHROME_BIN_RESOLVED="$(resolve_chrome)"
printf '%s\n' "$CHROME_BIN_RESOLVED" >"$OUTPUT_DIR/chrome-bin.txt"

go build -o "$BIN_DIR/go-cli-agent" ./cmd/go-cli-agent
go build -o "$BIN_DIR/budgetsmokeprovider" ./validation/cmd/budgetsmokeprovider

"$BIN_DIR/budgetsmokeprovider" \
	--listen 127.0.0.1:0 \
	--ready-file "$PROVIDER_READY" \
	--log "$PROVIDER_LOG" \
	>"$PROVIDER_STDOUT" 2>&1 &
PROVIDER_PID=$!
wait_for_file "$PROVIDER_READY" "$PROVIDER_PID" "budget smoke provider"
PROVIDER_BASE_URL="$(tr -d '\r\n' <"$PROVIDER_READY")/v1"

cat >"$CONFIG_PATH" <<EOF
schema_version: 1

default_provider: budget-smoke

providers:
  budget-smoke:
    api_provider: openai-compatible
    api_key_env: BUDGET_SMOKE_API_KEY
    base_url: ${PROVIDER_BASE_URL}
    model: budget-smoke-model
    request_timeout_sec: 30
    stream_idle_timeout_ms: 30000
    retry:
      max_attempts: 1
      base_delay_ms: 10
      retry_5xx: false
      retry_transport: false
    wire_api: responses
    max_output_tokens: 1024
    store: false
    send_metadata: true

session:
  dir: ${SESSION_ROOT}
  dir_mode: "0700"

skills:
  dirs:
    - ${ROOT_DIR}/skills

runtime:
  exec_finish_required: true
  guardrails_mode: yolo
  max_turns_soft: 24
  max_turns_hard: -1
  command_timeout_sec: 300
  multi_agent:
    enabled: true
    max_depth: 1
    max_active_children: 2
    cancel_grace_sec: 2
  isolation:
    default_mode: off
    root_dir: ${OUTPUT_DIR}/isolation
  queue:
    poll_interval_ms: 50
    auto_worker: true
    reaper_interval_ms: 1000
    lease_stale_after_sec: 30
  child_budget:
    max_active_runtime_sec: 0
    max_elapsed_sec: 0
    max_turns_per_attempt: 0

output:
  format: text
  show_raw_events: false
EOF

(
	cd "$OUTPUT_DIR"
	BUDGET_SMOKE_API_KEY="local-smoke-key" \
	GO_CLI_AGENT_CONFIG="$CONFIG_PATH" \
	GO_CLI_AGENT_ENV_FILE="$OUTPUT_DIR/.env" \
	"$BIN_DIR/go-cli-agent" web \
		--config "$CONFIG_PATH" \
		--listen "127.0.0.1:${WEB_PORT}" \
		--workers 1
) >"$WEB_LOG" 2>&1 &
WEB_PID=$!
wait_for_web

node ./validation/scripts/webconsole_ui_smoke.mjs \
	--base-url "$WEB_BASE_URL" \
	--workdir "$WORKDIR" \
	--queue-workdir "$WORKDIR" \
	--output "$UI_JSON" \
	--dom-output "$UI_DOM" \
	--chrome "$CHROME_BIN_RESOLVED" \
	--timeout-ms 120000 \
	--budget-lifecycle true \
	--keep-history true

node -e '
const fs = require("node:fs");
const result = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
const required = [
  "global_turn_guard_default_off",
  "global_turn_guard_scope_copy",
  "soft_checkpoint_copy",
  "child_budget_snapshot_copy",
  "child_budget_parent_actions_copy",
  "explorer_settings_visible",
  "explorer_settings_round_trip",
  "settings_budget_saved",
  "settings_canonical_round_trip",
  "long_output_artifact_pointer",
  "artifact_read_file_byte_page",
  "history_record_pagination",
  "history_content_pagination",
  "context_report_lazy_load",
  "context_report_bounded_surface",
  "context_report_refresh",
  "foreground_budget_pause_extend_resume_complete",
  "background_budget_pause_cancel_settle",
  "cancelled_excluded_from_failures",
  "budget_inspector_telemetry_visible",
  "terminal_budget_actions_cleared",
  "history_preserved_for_durable_audit"
];
for (const key of required) {
  if (result.interactions?.[key] !== true) throw new Error(`missing successful interaction: ${key}`);
}
if (result.runtime_exceptions?.length || result.console_errors?.length) {
  throw new Error(`browser errors: ${JSON.stringify({runtime: result.runtime_exceptions, console: result.console_errors})}`);
}
' "$UI_JSON"

grep -Fq 'max_turns_hard: -1' "$CONFIG_PATH"
grep -Fq 'max_active_runtime_sec: 1800' "$CONFIG_PATH"
grep -Fq 'max_elapsed_sec: 7200' "$CONFIG_PATH"
grep -Fq 'max_turns_per_attempt: 1' "$CONFIG_PATH"
grep -Fq 'active_runtime_checkpoint_ms: 1000' "$CONFIG_PATH"
grep -Fq 'explorer:' "$CONFIG_PATH"
grep -Fq 'model: budget-smoke-explorer-model' "$CONFIG_PATH"
grep -Fq 'reasoning_effort: low' "$CONFIG_PATH"
grep -Fq 'max_output_tokens: 321' "$CONFIG_PATH"

grep -Fq '"type":"web.config.write"' "$AUDIT_LOG"
grep -Fq '"max_turns_hard":-1' "$AUDIT_LOG"
grep -Fq '"child_budget_active_runtime_sec":1800' "$AUDIT_LOG"
grep -Fq '"child_budget_checkpoint_ms":1000' "$AUDIT_LOG"
grep -Fq '"child_budget_elapsed_sec":7200' "$AUDIT_LOG"
grep -Fq '"child_budget_turns_per_attempt":1' "$AUDIT_LOG"

grep -Fq '"tool":"agent_prompt"' "$PROVIDER_LOG"
grep -Fq '"tool":"agent_stop"' "$PROVIDER_LOG"
grep -Fq '"tool":"shell"' "$PROVIDER_LOG"
grep -Fq '"tool":"read_file"' "$PROVIDER_LOG"
if [[ "$(grep -Fc '"tool":"read_session_history"' "$PROVIDER_LOG")" -lt 3 ]]; then
	printf '%s\n' "missing record plus two content history calls" >&2
	exit 1
fi
grep -Fq '"agent_name":"budget-resume-child"' "$PROVIDER_LOG"
grep -Fq '"agent_name":"budget-cancel-child"' "$PROVIDER_LOG"

COMMAND_ARTIFACT="$(find "$SESSION_ROOT" -type f -path '*/artifacts/tool-outputs/*' -size 70000c -print -quit)"
if [[ -z "$COMMAND_ARTIFACT" ]]; then
	printf '%s\n' "missing complete 70000-byte command artifact" >&2
	exit 1
fi

if ! grep -R -Fq '"type":"session.child_budget.extended"' "$SESSION_ROOT"; then
	printf '%s\n' "missing durable child budget extension event" >&2
	exit 1
fi
if ! grep -R -Fq '"type":"session.child_budget.exceeded"' "$SESSION_ROOT"; then
	printf '%s\n' "missing durable child budget exceeded event" >&2
	exit 1
fi
if ! grep -R -Fq '"type":"queue.job.cancelled"' "$SESSION_ROOT"; then
	printf '%s\n' "missing durable cancelled queue event" >&2
	exit 1
fi

printf 'budget browser smoke passed: %s\n' "$UI_JSON"
