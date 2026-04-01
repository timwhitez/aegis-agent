#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

: "${OPENAI_API_KEY:?OPENAI_API_KEY is required}"
: "${GO_CLI_AGENT_LIVE_RESPONSES_URL:?GO_CLI_AGENT_LIVE_RESPONSES_URL is required}"

MODEL="${GO_CLI_AGENT_LIVE_MODEL:-gpt-5.4}"
SEND_METADATA="${GO_CLI_AGENT_LIVE_SEND_METADATA:-false}"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/go-cli-agent-live-smoke.XXXXXX")"
trap 'rm -rf "$TMP_DIR"' EXIT

CONFIG_PATH="$TMP_DIR/config.yaml"
SESSION_DIR="$TMP_DIR/sessions"
SKILL_DIR="$TMP_DIR/skills"
WORKDIR="$TMP_DIR/repo"
AGENT_BIN="$TMP_DIR/go-cli-agent"

case "$SEND_METADATA" in
  true|false)
    ;;
  *)
    printf 'GO_CLI_AGENT_LIVE_SEND_METADATA must be true or false\n' >&2
    exit 1
    ;;
esac

mkdir -p "$SESSION_DIR" "$SKILL_DIR" "$WORKDIR"
printf 'live smoke\n' > "$WORKDIR/README.md"

cat > "$CONFIG_PATH" <<EOF
schema_version: 1
default_provider: openai-compatible
providers:
  openai-compatible:
    api_key_env: OPENAI_API_KEY
    base_url: "${GO_CLI_AGENT_LIVE_RESPONSES_URL}"
    model: "${MODEL}"
    timeout_sec: 120
    wire_api: responses
    store: false
    send_metadata: ${SEND_METADATA}
session:
  dir: "${SESSION_DIR}"
  dir_mode: "0700"
skills:
  dirs:
    - "${SKILL_DIR}"
runtime:
  exec_finish_required: true
  max_turns_soft: 24
  max_turns_hard: 40
  command_timeout_sec: 120
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
output:
  format: text
  show_raw_events: false
hooks:
  default_timeout_sec: 15
EOF

extract_json_field() {
  local payload="$1"
  local field="$2"
  printf '%s' "$payload" | tr -d '\n' | sed -n "s/.*\"${field}\":\"\\([^\"]*\\)\".*/\\1/p"
}

require_match() {
  local haystack="$1"
  local needle="$2"
  local label="$3"
  if ! printf '%s' "$haystack" | grep -Fq "$needle"; then
    printf 'live smoke failed: %s missing %s\n' "$label" "$needle" >&2
    exit 1
  fi
}

./build.sh >/dev/null
cp ./bin/go-cli-agent "$AGENT_BIN"
chmod +x "$AGENT_BIN"

printf '== probe-provider ==\n'
probe_json="$("${AGENT_BIN}" probe-provider --config "$CONFIG_PATH" --provider openai-compatible --model "$MODEL" --base-url "$GO_CLI_AGENT_LIVE_RESPONSES_URL" --wire-api responses --api-key-env OPENAI_API_KEY --json)"
printf '%s\n' "$probe_json"
require_match "$probe_json" '"tool_call_names":["finish"]' "probe-provider"

printf '\n== exec ==\n'
exec_json="$("${AGENT_BIN}" exec --config "$CONFIG_PATH" --provider openai-compatible --model "$MODEL" --workdir "$WORKDIR" --json "Return exactly one finish tool call with message: live smoke exec ok")"
printf '%s\n' "$exec_json"
require_match "$exec_json" '"status":"completed"' "exec"

printf '\n== run -> awaiting_input ==\n'
run_json="$("${AGENT_BIN}" run --config "$CONFIG_PATH" --provider openai-compatible --model "$MODEL" --workdir "$WORKDIR" --json "Reply with exactly the plain text WAITING. Do not call any tool.")"
printf '%s\n' "$run_json"
require_match "$run_json" '"status":"awaiting_input"' "run"
run_session_id="$(extract_json_field "$run_json" "session_id")"
if [[ -z "$run_session_id" ]]; then
  printf 'live smoke failed: run session_id not found\n' >&2
  exit 1
fi

printf '\n== continue ==\n'
continue_json="$("${AGENT_BIN}" continue "$run_session_id" --config "$CONFIG_PATH" --provider openai-compatible --model "$MODEL" --json --message "Now call finish with message: live smoke continue ok")"
printf '%s\n' "$continue_json"
require_match "$continue_json" '"status":"completed"' "continue"

printf '\n== sessions ==\n'
sessions_json="$("${AGENT_BIN}" sessions --config "$CONFIG_PATH" --json)"
printf '%s\n' "$sessions_json"
require_match "$sessions_json" "$run_session_id" "sessions"

printf '\n== tasks ==\n'
tasks_json="$("${AGENT_BIN}" tasks "$run_session_id" --config "$CONFIG_PATH" --json)"
printf '%s\n' "$tasks_json"
require_match "$tasks_json" '"todo"' "tasks"

printf '\nlive smoke ok\n'
