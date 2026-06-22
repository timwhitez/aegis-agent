#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

COMMAND="${1:-start}"
if [[ $# -gt 0 ]]; then
	shift
fi

BASE_ENV_FILE="${GO_CLI_AGENT_ENV_FILE:-${ROOT_DIR}/.env}"
BASE_DEFAULT_CONFIG_PATH=""
if [[ -f "${ROOT_DIR}/.go-cli-agent/config.yaml" ]]; then
	BASE_DEFAULT_CONFIG_PATH="${ROOT_DIR}/.go-cli-agent/config.yaml"
fi

ENV_FILE="$BASE_ENV_FILE"
CONFIG_PATH=""
LISTEN_ADDR=""
WORKER_COUNT=""
BINARY_PATH=""
RUNTIME_DIR="${ROOT_DIR}/.go-cli-agent/runtime"
PID_FILE="${RUNTIME_DIR}/webconsole.pid"
LOG_FILE=""

mkdir -p "$RUNTIME_DIR"

refresh_runtime_settings() {
	ENV_FILE="${GO_CLI_AGENT_ENV_FILE:-$BASE_ENV_FILE}"
	CONFIG_PATH="${GO_CLI_AGENT_WEB_CONFIG:-$BASE_DEFAULT_CONFIG_PATH}"
	LISTEN_ADDR="${GO_CLI_AGENT_LISTEN:-0.0.0.0:3940}"
	WORKER_COUNT="${GO_CLI_AGENT_WEB_WORKERS:-2}"
	BINARY_PATH="${GO_CLI_AGENT_BIN:-${ROOT_DIR}/bin/go-cli-agent}"
	LOG_FILE="${GO_CLI_AGENT_WEB_LOG:-${RUNTIME_DIR}/webconsole.log}"
}

refresh_runtime_settings

usage() {
	cat <<'EOF'
Usage: ./run.sh [start|foreground|stop|restart|status|logs]

Defaults:
  start       Build if needed and start the embedded webconsole in background.
  foreground  Build if needed and run the embedded webconsole in foreground.
  stop        Stop the background webconsole started by this script.
  restart     Stop then start again in background.
  status      Show current PID, listen address, config, and URLs.
  logs        Tail the background log file.

Environment overrides:
  GO_CLI_AGENT_WEB_CONFIG   Explicit config file passed to `go-cli-agent web`.
  GO_CLI_AGENT_LISTEN       Listen address, default `0.0.0.0:3940`.
                           Non-loopback listen exposes config writes, .env API keys,
                           session deletion, skill management, workspace reads/downloads,
                           and workspace file creation/deletion to network-reachable clients;
                           use trusted local networks.
  GO_CLI_AGENT_WEB_WORKERS  Worker count, default `2`.
  GO_CLI_AGENT_BIN          Binary path, default `bin/go-cli-agent`.
  GO_CLI_AGENT_ENV_FILE     Optional .env file to source before start.
  GO_CLI_AGENT_WEB_LOG      Log file path, default `.go-cli-agent/runtime/webconsole.log`.
  GO_CLI_AGENT_SKIP_BUILD   Set to `1` to skip automatic `./build.sh`.
EOF
}

load_env_file() {
	if [[ -f "$ENV_FILE" ]]; then
		set -a
		# shellcheck disable=SC1090
		source "$ENV_FILE"
		set +a
	fi
	refresh_runtime_settings
}

current_pid() {
	if [[ ! -f "$PID_FILE" ]]; then
		return 1
	fi
	local pid
	pid="$(<"$PID_FILE")"
	if [[ -z "$pid" ]]; then
		return 1
	fi
	if kill -0 "$pid" 2>/dev/null; then
		printf '%s\n' "$pid"
		return 0
	fi
	rm -f "$PID_FILE"
	return 1
}

listen_host() {
	printf '%s\n' "${LISTEN_ADDR%:*}"
}

listen_port() {
	printf '%s\n' "${LISTEN_ADDR##*:}"
}

print_urls() {
	local host port
	host="$(listen_host)"
	port="$(listen_port)"
	if [[ "$host" == "0.0.0.0" || "$host" == "" || "$host" == "*" ]]; then
		printf 'URL: http://127.0.0.1:%s\n' "$port"
		local wsl_ip
		wsl_ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
		if [[ -n "$wsl_ip" ]]; then
			printf 'WSL URL: http://%s:%s\n' "$wsl_ip" "$port"
		fi
	else
		printf 'URL: http://%s:%s\n' "$host" "$port"
	fi
}

print_lan_warning() {
	local host
	host="$(listen_host)"
	case "$host" in
		127.*|localhost|::1|\[::1\])
			return 0
			;;
	esac
	echo "WARNING: web console is reachable from non-loopback clients."
	echo "It can write config and .env API keys, delete sessions, manage skills, read/download workspace files, and create/delete workspace files or folders. Use only on trusted local networks."
}

ensure_binary() {
	if [[ "${GO_CLI_AGENT_SKIP_BUILD:-0}" != "1" ]]; then
		"${ROOT_DIR}/build.sh"
	fi
	if [[ ! -x "$BINARY_PATH" ]]; then
		echo "binary not found or not executable: $BINARY_PATH" >&2
		exit 1
	fi
}

start_background() {
	if pid="$(current_pid)"; then
		echo "webconsole already running (pid $pid)"
		print_urls
		return 0
	fi

	load_env_file
	ensure_binary

	local cmd=("$BINARY_PATH" web -listen "$LISTEN_ADDR" -workers "$WORKER_COUNT")
	if [[ -n "$CONFIG_PATH" ]]; then
		cmd+=(-config "$CONFIG_PATH")
	fi
	: >"$LOG_FILE"
	if command -v setsid >/dev/null 2>&1; then
		setsid bash -c 'printf "%s\n" "$$" > "$1"; shift; exec "$@"' bash "$PID_FILE" "${cmd[@]}" >>"$LOG_FILE" 2>&1 < /dev/null &
	else
		nohup "${cmd[@]}" >>"$LOG_FILE" 2>&1 < /dev/null &
		local pid=$!
		printf '%s\n' "$pid" >"$PID_FILE"
	fi

	local pid
	for _ in {1..20}; do
		if [[ -s "$PID_FILE" ]]; then
			pid="$(<"$PID_FILE")"
			break
		fi
		sleep 0.05
	done
	if [[ -z "${pid:-}" ]]; then
		echo "webconsole failed to write pid file" >&2
		exit 1
	fi

	for _ in {1..40}; do
		if ! kill -0 "$pid" 2>/dev/null; then
			echo "webconsole exited early; inspect $LOG_FILE" >&2
			rm -f "$PID_FILE"
			exit 1
		fi
		if grep -q "web console listening on" "$LOG_FILE" 2>/dev/null; then
			break
		fi
		sleep 0.25
	done

	echo "webconsole started (pid $pid)"
	if [[ -n "$CONFIG_PATH" ]]; then
		printf 'Config: %s\n' "$CONFIG_PATH"
	else
		echo "Config: default search order"
	fi
	printf 'Workers: %s\n' "$WORKER_COUNT"
	printf 'Log: %s\n' "$LOG_FILE"
	print_lan_warning
	print_urls
}

start_foreground() {
	load_env_file
	ensure_binary
	local cmd=("$BINARY_PATH" web -listen "$LISTEN_ADDR" -workers "$WORKER_COUNT")
	if [[ -n "$CONFIG_PATH" ]]; then
		cmd+=(-config "$CONFIG_PATH")
	fi
	echo "starting embedded frontend+backend in foreground"
	if [[ -n "$CONFIG_PATH" ]]; then
		printf 'Config: %s\n' "$CONFIG_PATH"
	else
		echo "Config: default search order"
	fi
	printf 'Workers: %s\n' "$WORKER_COUNT"
	print_lan_warning
	print_urls
	exec "${cmd[@]}"
}

stop_background() {
	if ! pid="$(current_pid)"; then
		echo "webconsole is not running"
		return 0
	fi
	kill "$pid" 2>/dev/null || true
	for _ in {1..20}; do
		if ! kill -0 "$pid" 2>/dev/null; then
			rm -f "$PID_FILE"
			echo "webconsole stopped"
			return 0
		fi
		sleep 0.25
	done
	kill -9 "$pid" 2>/dev/null || true
	rm -f "$PID_FILE"
	echo "webconsole force-stopped"
}

show_status() {
	if pid="$(current_pid)"; then
		echo "webconsole running (pid $pid)"
	else
		echo "webconsole not running"
	fi
	if [[ -n "$CONFIG_PATH" ]]; then
		printf 'Config: %s\n' "$CONFIG_PATH"
	else
		echo "Config: default search order"
	fi
	printf 'Workers: %s\n' "$WORKER_COUNT"
	printf 'Log: %s\n' "$LOG_FILE"
	print_urls
}

tail_logs() {
	mkdir -p "$(dirname "$LOG_FILE")"
	touch "$LOG_FILE"
	tail -f "$LOG_FILE"
}

case "$COMMAND" in
	start)
		start_background
		;;
	foreground)
		start_foreground
		;;
	stop)
		stop_background
		;;
	restart)
		stop_background
		start_background
		;;
	status)
		show_status
		;;
	logs)
		tail_logs
		;;
	help|-h|--help)
		usage
		;;
	*)
		echo "unknown command: $COMMAND" >&2
		usage >&2
		exit 1
		;;
esac
