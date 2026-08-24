#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

COMMAND="${1:-start}"
if [[ $# -gt 0 ]]; then
	shift
fi

BASE_ENV_FILE="${AEGIS_AGENT_ENV_FILE:-${ROOT_DIR}/.env}"
BASE_LISTEN_ADDR="${AEGIS_AGENT_LISTEN:-127.0.0.1:3940}"
BASE_ALLOW_NETWORK="${AEGIS_AGENT_ALLOW_NETWORK:-0}"
BASE_SIGNAL_HELPER="${AEGIS_AGENT_SIGNAL_HELPER:-${AEGIS_AGENT_BIN:-${ROOT_DIR}/bin/aegis-agent}}"
BASE_DEFAULT_CONFIG_PATH=""
if [[ -f "${ROOT_DIR}/.aegis-agent/config.yaml" ]]; then
	BASE_DEFAULT_CONFIG_PATH="${ROOT_DIR}/.aegis-agent/config.yaml"
fi

ENV_FILE="$BASE_ENV_FILE"
CONFIG_PATH=""
LISTEN_ADDR="$BASE_LISTEN_ADDR"
ALLOW_NETWORK="$BASE_ALLOW_NETWORK"
WORKER_COUNT=""
BINARY_PATH=""
SIGNAL_HELPER="$BASE_SIGNAL_HELPER"
RUNTIME_DIR="${ROOT_DIR}/.aegis-agent/runtime"
PID_FILE="${RUNTIME_DIR}/webconsole.pid"
PID_PENDING_FILE="${PID_FILE}.pending.$$"
LOCK_FILE="${RUNTIME_DIR}/webconsole.lock"
LOG_FILE=""
PID_RECORD_PID=""
PID_RECORD_IDENTITY=""
STOP_WAS_FORCED=0
PENDING_RECORDS=()

umask 077
mkdir -p "$RUNTIME_DIR"

refresh_runtime_settings() {
	ENV_FILE="${AEGIS_AGENT_ENV_FILE:-$BASE_ENV_FILE}"
	CONFIG_PATH="${AEGIS_AGENT_WEB_CONFIG:-$BASE_DEFAULT_CONFIG_PATH}"
	# Network exposure is controlled only by the launching process environment.
	# Project dotenv content is passed to the Go binary as data and is never
	# evaluated by this shell launcher.
	LISTEN_ADDR="$BASE_LISTEN_ADDR"
	ALLOW_NETWORK="$BASE_ALLOW_NETWORK"
	WORKER_COUNT="${AEGIS_AGENT_WEB_WORKERS:-2}"
	BINARY_PATH="${AEGIS_AGENT_BIN:-${ROOT_DIR}/bin/aegis-agent}"
	SIGNAL_HELPER="$BASE_SIGNAL_HELPER"
	LOG_FILE="${AEGIS_AGENT_WEB_LOG:-${RUNTIME_DIR}/webconsole.log}"
}

refresh_runtime_settings

usage() {
	cat <<'EOF_USAGE'
Usage: ./run.sh [start|foreground|stop|restart|status|logs]

Defaults:
  start       Build if needed and start the embedded webconsole in background.
  foreground  Build if needed and run the embedded webconsole in foreground.
  stop        Stop the background webconsole started by this script.
  restart     Stop then start again in background.
  status      Show current PID, listen address, config, and URLs.
  logs        Tail the background log file.

Environment overrides:
  AEGIS_AGENT_WEB_CONFIG   Explicit config file passed to `aegis-agent web`.
  AEGIS_AGENT_LISTEN       Listen address, default `127.0.0.1:3940`.
  AEGIS_AGENT_ALLOW_NETWORK
                           Set to `1` or `true` in the process environment to allow
                           an explicitly configured non-loopback listen address.
  AEGIS_AGENT_WEB_WORKERS  Worker count, default `2`.
  AEGIS_AGENT_BIN          Binary path, default `bin/aegis-agent`.
  AEGIS_AGENT_SIGNAL_HELPER
                           Trusted Aegis binary used for Linux pidfd signalling.
  AEGIS_AGENT_ENV_FILE     Optional credential dotenv file parsed as data by the
                           Go binary; the launcher never sources this file.
  AEGIS_AGENT_WEB_LOG      Log file path, default `.aegis-agent/runtime/webconsole.log`.
  AEGIS_AGENT_SKIP_BUILD   Set to `1` to skip automatic `./build.sh`.
EOF_USAGE
}

prepare_env_file() {
	# A dotenv file is data, not shell code. The Go binary applies the strict
	# provider-credential allowlist in config.LoadEnvFile.
	if [[ -n "$ENV_FILE" ]]; then
		export AEGIS_AGENT_ENV_FILE="$ENV_FILE"
	else
		unset AEGIS_AGENT_ENV_FILE || true
	fi
}

acquire_runtime_lock() {
	if ! command -v flock >/dev/null 2>&1; then
		echo "flock is required for safe background process management" >&2
		return 1
	fi
	if [[ -L "$LOCK_FILE" ]]; then
		echo "refusing symlinked webconsole runtime lock: $LOCK_FILE" >&2
		return 1
	fi
	exec 9>"$LOCK_FILE"
	if ! flock -x 9; then
		echo "failed to acquire webconsole runtime lock: $LOCK_FILE" >&2
		return 1
	fi
}

list_pending_records() {
	shopt -s nullglob
	PENDING_RECORDS=("${PID_FILE}.pending."*)
	shopt -u nullglob
}

read_pid_record_file() {
	local path="$1" first_line key value pid_count=0 identity_count=0
	PID_RECORD_PID=""
	PID_RECORD_IDENTITY=""
	if [[ -L "$path" ]]; then
		return 2
	fi
	if [[ ! -f "$path" ]]; then
		return 1
	fi
	IFS= read -r first_line <"$path" || true
	if [[ "$first_line" =~ ^[0-9]+$ ]]; then
		PID_RECORD_PID="$first_line"
		return 0
	fi
	while IFS='=' read -r key value; do
		case "$key" in
			pid)
				((pid_count += 1))
				PID_RECORD_PID="$value"
				;;
			identity)
				((identity_count += 1))
				PID_RECORD_IDENTITY="$value"
				;;
			"")
				;;
			*)
				return 2
				;;
		esac
	done <"$path"
	if [[ "$pid_count" -ne 1 || "$identity_count" -ne 1 ]]; then
		return 2
	fi
	if [[ ! "$PID_RECORD_PID" =~ ^[0-9]+$ ]] || [[ "$PID_RECORD_PID" -le 0 ]] || [[ -z "$PID_RECORD_IDENTITY" ]]; then
		return 2
	fi
	return 0
}

read_pid_record() {
	read_pid_record_file "$PID_FILE"
}

signal_process_instance() {
	local pid="$1" identity="$2" signal="$3"
	if [[ ! -x "$SIGNAL_HELPER" ]]; then
		printf 'identity-bound signal helper is not executable: %s\n' "$SIGNAL_HELPER" >&2
		return 5
	fi
	"$SIGNAL_HELPER" __signal-process --pid "$pid" --identity "$identity" --signal "$signal"
}

write_pid_record() {
	local pid="$1" identity="$2" record
	printf -v record 'pid=%s\nidentity=%s\n' "$pid" "$identity"
	# Bash noclobber opens the final path with exclusive-create semantics. The
	# single record write may leave a malformed fail-closed file after a crash,
	# but a concurrent launcher can never overwrite an accepted owner record.
	if ! (set -o noclobber; printf '%s' "$record" >"$PID_FILE") 2>/dev/null; then
		return 1
	fi
}

remove_pid_record_file_if_matches() {
	local path="$1" expected_pid="$2" expected_identity="$3"
	if ! read_pid_record_file "$path"; then
		return 0
	fi
	if [[ "$PID_RECORD_PID" == "$expected_pid" && "$PID_RECORD_IDENTITY" == "$expected_identity" ]]; then
		rm -f -- "$path"
	fi
}

remove_pid_record_if_matches() {
	remove_pid_record_file_if_matches "$PID_FILE" "$1" "$2"
}

current_pid() {
	local record_status signal_status
	if read_pid_record; then
		:
	else
		record_status=$?
		if [[ "$record_status" -eq 1 ]]; then
			return 1
		fi
		printf 'refusing to use malformed or symlinked pid record: %s\n' "$PID_FILE" >&2
		return 2
	fi
	if [[ -z "$PID_RECORD_IDENTITY" ]]; then
		if ! kill -0 "$PID_RECORD_PID" 2>/dev/null; then
			rm -f -- "$PID_FILE"
			return 1
		fi
		printf 'refusing to signal live pid %s from an unverifiable legacy pid record; remove %s after checking the process\n' "$PID_RECORD_PID" "$PID_FILE" >&2
		return 2
	fi
	if signal_process_instance "$PID_RECORD_PID" "$PID_RECORD_IDENTITY" 0; then
		return 0
	else
		signal_status=$?
	fi
	case "$signal_status" in
		3|4)
			remove_pid_record_if_matches "$PID_RECORD_PID" "$PID_RECORD_IDENTITY"
			return 1
			;;
		*)
			printf 'refusing to manage pid %s because its process identity cannot be verified atomically\n' "$PID_RECORD_PID" >&2
			return 2
			;;
	esac
}

wait_for_process_exit() {
	local pid="$1" identity="$2" attempts="$3" delay="$4" signal_status
	local i
	for ((i = 0; i < attempts; i++)); do
		if signal_process_instance "$pid" "$identity" 0 >/dev/null 2>&1; then
			sleep "$delay"
			continue
		else
			signal_status=$?
		fi
		case "$signal_status" in
			3|4) return 0 ;;
			*) return "$signal_status" ;;
		esac
	done
	return 1
}

stop_process_instance() {
	local pid="$1" identity="$2" signal_status wait_status
	STOP_WAS_FORCED=0
	if signal_process_instance "$pid" "$identity" TERM; then
		:
	else
		signal_status=$?
		case "$signal_status" in
			3|4) return 0 ;;
			*) return "$signal_status" ;;
		esac
	fi
	if wait_for_process_exit "$pid" "$identity" 20 0.25; then
		return 0
	else
		wait_status=$?
		if [[ "$wait_status" -ne 1 ]]; then
			return "$wait_status"
		fi
	fi
	if signal_process_instance "$pid" "$identity" KILL; then
		STOP_WAS_FORCED=1
	else
		signal_status=$?
		case "$signal_status" in
			3|4) return 0 ;;
			*) return "$signal_status" ;;
		esac
	fi
	if wait_for_process_exit "$pid" "$identity" 20 0.05; then
		return 0
	else
		wait_status=$?
	fi
	return "$wait_status"
}

cleanup_failed_start() {
	local record_path="$1" pid="$2" identity="$3" cleanup_status
	if stop_process_instance "$pid" "$identity"; then
		remove_pid_record_file_if_matches "$record_path" "$pid" "$identity"
		return 0
	else
		cleanup_status=$?
	fi
	printf 'webconsole cleanup could not be confirmed; recovery record retained: %s\n' "$record_path" >&2
	return "$cleanup_status"
}

listen_host() {
	printf '%s\n' "${LISTEN_ADDR%:*}"
}

listen_port() {
	printf '%s\n' "${LISTEN_ADDR##*:}"
}

is_loopback_host() {
	local host="${1,,}" first second third fourth
	local ipv4_re='^(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})$'
	case "$host" in
		localhost|::1|\[::1\])
			return 0
			;;
	esac
	if [[ ! "$host" =~ $ipv4_re ]]; then
		return 1
	fi
	first=$((10#${BASH_REMATCH[1]}))
	second=$((10#${BASH_REMATCH[2]}))
	third=$((10#${BASH_REMATCH[3]}))
	fourth=$((10#${BASH_REMATCH[4]}))
	((first == 127 && second <= 255 && third <= 255 && fourth <= 255))
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

validate_listen_policy() {
	local host allow
	host="$(listen_host)"
	if is_loopback_host "$host"; then
		return 0
	fi
	allow="${ALLOW_NETWORK,,}"
	if [[ "$allow" != "1" && "$allow" != "true" ]]; then
		printf 'refusing non-loopback listen address %s without AEGIS_AGENT_ALLOW_NETWORK=1\n' "$LISTEN_ADDR" >&2
		return 1
	fi
}

print_lan_warning() {
	local host
	host="$(listen_host)"
	if is_loopback_host "$host"; then
		return 0
	fi
	echo "WARNING: web console is reachable from non-loopback clients."
	echo "It can write config and .env API keys, delete sessions, manage skills, read/download/upload/rename workspace files, and create workspace folders or delete one or more workspace files or folders. Use only on trusted local networks."
}

ensure_binary() {
	if [[ "${AEGIS_AGENT_SKIP_BUILD:-0}" != "1" ]]; then
		"${ROOT_DIR}/build.sh"
	fi
	if [[ ! -x "$BINARY_PATH" ]]; then
		echo "binary not found or not executable: $BINARY_PATH" >&2
		exit 1
	fi
}

ensure_signal_helper() {
	if [[ ! -x "$SIGNAL_HELPER" ]]; then
		echo "identity-bound signal helper not found or not executable: $SIGNAL_HELPER" >&2
		return 1
	fi
}

launch_background_child() {
	local child_script
	child_script='set -euo pipefail
pending="$1"
shift
if [[ ! -r /proc/sys/kernel/random/boot_id || ! -r "/proc/$$/stat" ]]; then
  exit 125
fi
boot_id="$(</proc/sys/kernel/random/boot_id)"
stat_text="$(<"/proc/$$/stat")"
if [[ "$stat_text" != *") "* ]]; then
  exit 125
fi
stat_tail="${stat_text##*) }"
read -r -a stat_fields <<<"$stat_tail"
if [[ -z "$boot_id" || ${#stat_fields[@]} -le 19 || -z "${stat_fields[19]}" ]]; then
  exit 125
fi
identity="linux:${boot_id}:${stat_fields[19]}"
tmp="${pending}.tmp.$$"
umask 077
trap '\''rm -f -- "$tmp"'\'' EXIT
{
  printf "pid=%s\\n" "$$"
  printf "identity=%s\\n" "$identity"
} >"$tmp"
mv -f -- "$tmp" "$pending"
trap - EXIT
exec "$@"'
	if command -v setsid >/dev/null 2>&1; then
		setsid bash -c "$child_script" bash "$PID_PENDING_FILE" "$@" 9>&- >>"$LOG_FILE" 2>&1 < /dev/null &
	else
		nohup bash -c "$child_script" bash "$PID_PENDING_FILE" "$@" 9>&- >>"$LOG_FILE" 2>&1 < /dev/null &
	fi
}

start_background() {
	local current_status pending_status pid identity signal_status ready=0
	if current_pid; then
		echo "webconsole already running (pid $PID_RECORD_PID)"
		print_urls
		return 0
	else
		current_status=$?
		if [[ "$current_status" -eq 2 ]]; then
			return 1
		fi
	fi
	list_pending_records
	if [[ "${#PENDING_RECORDS[@]}" -gt 0 ]]; then
		echo "refusing to start while identity-bound recovery records exist:" >&2
		printf '  %s\n' "${PENDING_RECORDS[@]}" >&2
		return 1
	fi

	prepare_env_file
	validate_listen_policy
	print_lan_warning
	ensure_binary
	ensure_signal_helper
	local cmd=("$BINARY_PATH" web -listen "$LISTEN_ADDR" -workers "$WORKER_COUNT")
	if [[ -n "$CONFIG_PATH" ]]; then
		cmd+=(-config "$CONFIG_PATH")
	fi
	: >"$LOG_FILE"
	rm -f -- "$PID_PENDING_FILE"
	launch_background_child "${cmd[@]}"

	for _ in {1..40}; do
		if [[ -s "$PID_PENDING_FILE" ]]; then
			break
		fi
		sleep 0.05
	done
	if read_pid_record_file "$PID_PENDING_FILE"; then
		pid="$PID_RECORD_PID"
		identity="$PID_RECORD_IDENTITY"
	else
		pending_status=$?
		echo "webconsole failed to publish a valid identity-bound pending record" >&2
		if [[ -e "$PID_PENDING_FILE" || -L "$PID_PENDING_FILE" ]]; then
			echo "unverifiable pending record retained for operator recovery: $PID_PENDING_FILE" >&2
		fi
		return "$pending_status"
	fi
	if [[ -z "$identity" ]]; then
		echo "webconsole published an unverifiable legacy pending record; retained for operator recovery: $PID_PENDING_FILE" >&2
		return 1
	fi
	if signal_process_instance "$pid" "$identity" 0; then
		:
	else
		signal_status=$?
		echo "webconsole pending process identity could not be verified; recovery record retained: $PID_PENDING_FILE" >&2
		return "$signal_status"
	fi
	if ! write_pid_record "$pid" "$identity"; then
		echo "webconsole durable pid record could not be published" >&2
		cleanup_failed_start "$PID_PENDING_FILE" "$pid" "$identity" || true
		return 1
	fi
	rm -f -- "$PID_PENDING_FILE"

	for _ in {1..40}; do
		if signal_process_instance "$pid" "$identity" 0 >/dev/null 2>&1; then
			:
		else
			signal_status=$?
			case "$signal_status" in
				3|4)
					echo "webconsole exited early; inspect $LOG_FILE" >&2
					remove_pid_record_if_matches "$pid" "$identity"
					return 1
					;;
				*)
					echo "webconsole readiness cannot be verified atomically; durable record retained" >&2
					return 1
					;;
			esac
		fi
		if grep -q "web console listening on" "$LOG_FILE" 2>/dev/null; then
			ready=1
			break
		fi
		sleep 0.25
	done
	if [[ "$ready" -ne 1 ]]; then
		echo "webconsole did not report readiness; inspect $LOG_FILE" >&2
		cleanup_failed_start "$PID_FILE" "$pid" "$identity" || true
		return 1
	fi

	echo "webconsole started (pid $pid)"
	if [[ -n "$CONFIG_PATH" ]]; then
		printf 'Config: %s\n' "$CONFIG_PATH"
	else
		echo "Config: default search order"
	fi
	printf 'Workers: %s\n' "$WORKER_COUNT"
	printf 'Log: %s\n' "$LOG_FILE"
	print_urls
}

start_foreground() {
	prepare_env_file
	validate_listen_policy
	print_lan_warning
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
	print_urls
	exec "${cmd[@]}"
}

recover_pending_processes() {
	local path record_status signal_status pid identity failed=0
	list_pending_records
	if [[ "${#PENDING_RECORDS[@]}" -eq 0 ]]; then
		return 1
	fi
	for path in "${PENDING_RECORDS[@]}"; do
		if read_pid_record_file "$path"; then
			pid="$PID_RECORD_PID"
			identity="$PID_RECORD_IDENTITY"
		else
			record_status=$?
			printf 'refusing malformed or symlinked pending recovery record: %s\n' "$path" >&2
			failed=1
			continue
		fi
		if [[ -z "$identity" ]]; then
			printf 'refusing unverifiable legacy pending recovery record: %s\n' "$path" >&2
			failed=1
			continue
		fi
		if signal_process_instance "$pid" "$identity" 0 >/dev/null 2>&1; then
			:
		else
			signal_status=$?
			case "$signal_status" in
				3|4)
					remove_pid_record_file_if_matches "$path" "$pid" "$identity"
					continue
					;;
				*)
					printf 'pending recovery process %s cannot be verified; record retained: %s\n' "$pid" "$path" >&2
					failed=1
					continue
					;;
			esac
		fi
		if stop_process_instance "$pid" "$identity"; then
			remove_pid_record_file_if_matches "$path" "$pid" "$identity"
			echo "recovered and stopped pending webconsole process (pid $pid)"
		else
			printf 'pending recovery process %s could not be confirmed stopped; record retained: %s\n' "$pid" "$path" >&2
			failed=1
		fi
	done
	if [[ "$failed" -eq 1 ]]; then
		return 2
	fi
	return 0
}

stop_background() {
	local current_status pid identity
	if current_pid; then
		pid="$PID_RECORD_PID"
		identity="$PID_RECORD_IDENTITY"
	else
		current_status=$?
		if [[ "$current_status" -eq 2 ]]; then
			return 1
		fi
		if recover_pending_processes; then
			return 0
		else
			current_status=$?
			if [[ "$current_status" -ne 1 ]]; then
				return "$current_status"
			fi
		fi
		echo "webconsole is not running"
		return 0
	fi

	if stop_process_instance "$pid" "$identity"; then
		remove_pid_record_if_matches "$pid" "$identity"
		if [[ "$STOP_WAS_FORCED" -eq 1 ]]; then
			echo "webconsole force-stopped"
		else
			echo "webconsole stopped"
		fi
		return 0
	fi
	echo "webconsole stop could not be confirmed atomically; durable record retained" >&2
	return 1
}

show_status() {
	local current_status
	if current_pid; then
		echo "webconsole running (pid $PID_RECORD_PID)"
	else
		current_status=$?
		if [[ "$current_status" -eq 2 ]]; then
			echo "webconsole status unknown: pid record is not verifiable"
		else
			echo "webconsole not running"
		fi
	fi
	list_pending_records
	if [[ "${#PENDING_RECORDS[@]}" -gt 0 ]]; then
		echo "Pending recovery records:"
		printf '  %s\n' "${PENDING_RECORDS[@]}"
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
		acquire_runtime_lock
		start_background
		;;
	foreground)
		start_foreground
		;;
	stop)
		acquire_runtime_lock
		stop_background
		;;
	restart)
		acquire_runtime_lock
		stop_background
		start_background
		;;
	status)
		acquire_runtime_lock
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
