#!/usr/bin/env bash
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

: "${OPENAI_API_KEY:?OPENAI_API_KEY is required}"

MODEL="${GO_CLI_AGENT_LIVE_MODEL:-gpt-5.4}"
CONFIG_PATH="validation/config.openai-compatible.yaml"
SESSION_ROOT="/root/.go-cli-agent/validation-sessions"
ROUND_ID="${1:-$(date -u +%F)-openai-compatible-${MODEL}-round25-large-project-profile-promotion-checks}"
RUN_DIR="validation/runs/${ROUND_ID}"
RAW_DIR="${RUN_DIR}/raw"
PROMPT_DIR="${RUN_DIR}/prompts"
ARTIFACT_DIR="${RUN_DIR}/artifacts"
WORKSPACE_DIR="${RUN_DIR}/workspaces"
NOTE_DIR="${RUN_DIR}/notes"
SUMMARY_PATH="${RUN_DIR}/SUMMARY.md"
ISSUES_PATH="${RUN_DIR}/ISSUES.md"

if [[ -e "$RUN_DIR" ]]; then
	echo "run directory already exists: $RUN_DIR" >&2
	exit 1
fi

mkdir -p "$RAW_DIR" "$PROMPT_DIR" "$ARTIFACT_DIR" "$WORKSPACE_DIR" "$NOTE_DIR"
printf 'scenario_id\tlabel\tstatus\texit_code\traw\tartifact\n' >"${NOTE_DIR}/scenario-index.tsv"

TOTAL_SCENARIOS=0
PASSED_SCENARIOS=0
FAILED_SCENARIOS=0
declare -a FAILED_IDS=()
declare -a FAILED_REASONS=()

copy_workspace() {
	local name="$1"
	local dest="$2"
	cp -R "validation/workspaces/${name}" "$dest"
}

extract_json_field() {
	local path="$1"
	local key="$2"
	if [[ ! -f "$path" ]]; then
		return 0
	fi
	grep -o "\"${key}\":[[:space:]]*\"[^\"]*\"" "$path" | head -n1 | sed -E "s/\"${key}\":[[:space:]]*\"([^\"]*)\"/\1/"
}

extract_isolation_mode() {
	local path="$1"
	if [[ ! -f "$path" ]]; then
		return 0
	fi
	sed -n '/"isolation":[[:space:]]*{/,/^[[:space:]]*}/p' "$path" \
		| grep -o '"mode":[[:space:]]*"[^"]*"' \
		| head -n1 \
		| sed -E 's/"mode":[[:space:]]*"([^"]*)"/\1/'
}

write_prompt() {
	local path="$1"
	shift
	printf '%s\n' "$*" >"$path"
}

copy_if_present() {
	local src="$1"
	local dest="$2"
	if [[ -f "$src" ]]; then
		cp "$src" "$dest"
	fi
}

failure_reason() {
	local raw_path="$1"
	if [[ ! -f "$raw_path" ]]; then
		printf 'raw output missing'
		return 0
	fi
	tail -n 1 "$raw_path" | tr -d '\r'
}

finalize_scenario() {
	local scenario_id="$1"
	local label="$2"
	local exit_code="$3"
	local raw_path="$4"
	local artifact_path="$5"
	local status="passed"
	if (( exit_code != 0 )); then
		status="failed"
	fi
	TOTAL_SCENARIOS=$((TOTAL_SCENARIOS + 1))
	if [[ "$status" == "passed" ]]; then
		PASSED_SCENARIOS=$((PASSED_SCENARIOS + 1))
	else
		FAILED_SCENARIOS=$((FAILED_SCENARIOS + 1))
		FAILED_IDS+=("$scenario_id")
		FAILED_REASONS+=("$(failure_reason "$raw_path")")
	fi
	printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$scenario_id" "$label" "$status" "$exit_code" "$raw_path" "$artifact_path" >>"${NOTE_DIR}/scenario-index.tsv"
	{
		echo "# ${scenario_id} ${label}"
		echo
		echo "- status: ${status}"
		echo "- exit_code: ${exit_code}"
		echo "- raw: \`${raw_path}\`"
		echo "- artifact: \`${artifact_path}\`"
		if [[ "$status" != "passed" ]]; then
			echo "- failure_reason: $(failure_reason "$raw_path")"
		fi
		if [[ -f "$raw_path" ]]; then
			echo
			echo "## Tail"
			echo
			echo '```'
			tail -n 30 "$raw_path"
			echo '```'
		fi
	} >"${NOTE_DIR}/${scenario_id}.md"
}

write_summary() {
	{
		echo "# Round25 Summary"
		echo
		echo "## Run metadata"
		echo
		echo "- Run directory: \`${RUN_DIR}\`"
		echo "- Provider: \`openai-compatible\`"
		echo "- Wire API: \`responses\`"
		echo "- Model: \`${MODEL}\`"
		echo "- Base URL: \`http://69.63.215.40:24634/v1\`"
		echo "- Scenario index: \`notes/scenario-index.tsv\`"
		echo "- Matrix status: ${PASSED_SCENARIOS}/${TOTAL_SCENARIOS} scenarios passed; ${FAILED_SCENARIOS} failed."
		echo
		echo "## Scenario goals"
		echo
		echo "- RT21 validates synchronous delegation with isolated child workdir."
		echo "- RT22 validates background delegation plus queue worker completion and parent notification."
		echo '- RT23 validates `experimental children` observability for child sessions and queue jobs.'
		echo '- RT24 validates direct main-path `exec --isolation copy` outside delegation.'
	} >"$SUMMARY_PATH"
}

write_issues() {
	{
		echo "# Round25 Issues"
		echo
		echo "## Open issues"
		echo
		if (( FAILED_SCENARIOS == 0 )); then
			echo "No open issues recorded by this supplemental run."
		else
			local idx
			for idx in "${!FAILED_IDS[@]}"; do
				echo "- ${FAILED_IDS[$idx]}: ${FAILED_REASONS[$idx]}"
			done
		fi
	} >"$ISSUES_PATH"
}

run_exec_with_prompt_file() {
	local prompt_path="$1"
	local raw_path="$2"
	local workdir="$3"
	./bin/go-cli-agent exec \
		--config "$CONFIG_PATH" \
		--provider openai-compatible \
		--model "$MODEL" \
		--workdir "$workdir" \
		--json \
		<"$prompt_path" >"$raw_path" 2>&1
}

DOCSET_DIR="${WORKSPACE_DIR}/delegate_sync_docset"
INCIDENT_DIR="${WORKSPACE_DIR}/background_incident"
ISOLATED_DIR="${WORKSPACE_DIR}/isolated_exec_docset"
copy_workspace "docset" "$DOCSET_DIR"
copy_workspace "incident" "$INCIDENT_DIR"
copy_workspace "docset" "$ISOLATED_DIR"

RT21_RAW="${RAW_DIR}/rt21-delegate-sync.json"
RT21_PROMPT_PARENT="${PROMPT_DIR}/rt21-parent.prompt.txt"
RT21_PROMPT_CHILD="${PROMPT_DIR}/rt21-child.prompt.txt"
write_prompt "$RT21_PROMPT_PARENT" "Review only product_overview.md in the current workspace.
Write reports/parent-ready.md with sections: scope, status.
Then call finish."
write_prompt "$RT21_PROMPT_CHILD" "Review only ops_notes.md and release_constraints.md in the current workspace.
Write reports/delegate-child.md with sections: scope, one finding, next steps.
Then call finish."
run_exec_with_prompt_file "$RT21_PROMPT_PARENT" "${RAW_DIR}/rt21-parent.json" "$DOCSET_DIR"
RT21_EXIT=$?
RT21_PARENT_ID="$(extract_json_field "${RAW_DIR}/rt21-parent.json" "session_id")"
if (( RT21_EXIT == 0 )) && [[ -n "$RT21_PARENT_ID" ]]; then
	./bin/go-cli-agent experimental delegate "$RT21_PARENT_ID" \
		--config "$CONFIG_PATH" \
		--provider openai-compatible \
		--model "$MODEL" \
		--agent reviewer \
		--isolation auto \
		--json \
		<"$RT21_PROMPT_CHILD" >"$RT21_RAW" 2>&1
	RT21_EXIT=$?
else
	RT21_EXIT=1
fi
RT21_CHILD_ID="$(extract_json_field "$RT21_RAW" "session_id")"
RT21_CHILD_WORKDIR="$(extract_json_field "$RT21_RAW" "workdir")"
RT21_CHILD_SESSION_PATH="${SESSION_ROOT}/${RT21_CHILD_ID}/session.json"
RT21_CHILD_REPORT="${RT21_CHILD_WORKDIR}/reports/delegate-child.md"
RT21_PARENT_REPORT="${DOCSET_DIR}/reports/delegate-child.md"
if [[ -f "$RT21_CHILD_REPORT" ]]; then
	cp "$RT21_CHILD_REPORT" "${ARTIFACT_DIR}/rt21-delegate-child.md"
fi
{
	echo "# RT21 Delegate Isolation Proof"
	echo
	echo "- parent_session_id: \`${RT21_PARENT_ID}\`"
	echo "- child_session_id: \`${RT21_CHILD_ID}\`"
	echo "- child_workdir: \`${RT21_CHILD_WORKDIR}\`"
	if [[ -f "$RT21_CHILD_SESSION_PATH" ]]; then
		echo "- child_session_json: \`${RT21_CHILD_SESSION_PATH}\`"
		echo "- isolation_mode: \`$(extract_isolation_mode "$RT21_CHILD_SESSION_PATH")\`"
	fi
	echo "- child_report_present: \`$([[ -f "$RT21_CHILD_REPORT" ]] && echo yes || echo no)\`"
	echo "- parent_workspace_untouched: \`$([[ ! -f "$RT21_PARENT_REPORT" ]] && echo yes || echo no)\`"
	echo
	echo "## Notes"
	echo
	echo '- This scenario exercises `experimental delegate` with `--isolation auto` against a real provider-backed child run.'
	echo "- Success means the child completed, produced its report in an isolated workdir, and did not write that report into the parent workspace."
	} >"${ARTIFACT_DIR}/rt21-delegate-isolation-proof.md"
if [[ ! -f "$RT21_CHILD_REPORT" ]] || [[ -f "$RT21_PARENT_REPORT" ]]; then
	RT21_EXIT=1
fi
finalize_scenario "RT21" "Delegate Isolation Proof" "$RT21_EXIT" "$RT21_RAW" "${ARTIFACT_DIR}/rt21-delegate-isolation-proof.md"

RT22_PROMPT_PARENT="${PROMPT_DIR}/rt22-parent.prompt.txt"
RT22_PROMPT_CHILD="${PROMPT_DIR}/rt22-child.prompt.txt"
RT22_RAW="${RAW_DIR}/rt22-background-delegate.json"
write_prompt "$RT22_PROMPT_PARENT" "Review only logs/app.log and service_config.json in the current workspace.
Write reports/background-parent.md with sections: scope, status.
Then call finish."
write_prompt "$RT22_PROMPT_CHILD" "Review only logs/app.log and service_config.json in the current workspace.
Write reports/background-child.md with sections: scope, likely root cause.
Then call finish."
run_exec_with_prompt_file "$RT22_PROMPT_PARENT" "${RAW_DIR}/rt22-parent.json" "$INCIDENT_DIR"
RT22_EXIT=$?
RT22_PARENT_ID="$(extract_json_field "${RAW_DIR}/rt22-parent.json" "session_id")"
if (( RT22_EXIT == 0 )) && [[ -n "$RT22_PARENT_ID" ]]; then
	./bin/go-cli-agent experimental delegate "$RT22_PARENT_ID" \
		--config "$CONFIG_PATH" \
		--provider openai-compatible \
		--model "$MODEL" \
		--agent reviewer \
		--background \
		--isolation auto \
		--json \
		<"$RT22_PROMPT_CHILD" >"$RT22_RAW" 2>&1
	RT22_EXIT=$?
else
	RT22_EXIT=1
fi
RT22_JOB_ID="$(extract_json_field "$RT22_RAW" "queue_job_id")"
if (( RT22_EXIT == 0 )) && [[ -n "$RT22_JOB_ID" ]]; then
	./bin/go-cli-agent experimental queue worker --once --config "$CONFIG_PATH" --json >"${RAW_DIR}/rt22-queue-worker.json" 2>&1
	RT22_WORKER_EXIT=$?
	if (( RT22_WORKER_EXIT != 0 )); then
		RT22_EXIT=$RT22_WORKER_EXIT
	fi
	./bin/go-cli-agent experimental queue show "$RT22_JOB_ID" --config "$CONFIG_PATH" --json >"${RAW_DIR}/rt22-queue-show.json" 2>&1
	RT22_SHOW_EXIT=$?
	if (( RT22_SHOW_EXIT != 0 )); then
		RT22_EXIT=$RT22_SHOW_EXIT
	fi
else
	RT22_EXIT=1
fi
RT22_CHILD_ID="$(extract_json_field "${RAW_DIR}/rt22-queue-show.json" "session_id")"
RT22_CHILD_WORKDIR="$(extract_json_field "${RAW_DIR}/rt22-queue-show.json" "effective_workdir")"
RT22_CHILD_REPORT="${RT22_CHILD_WORKDIR}/reports/background-child.md"
RT22_NOTIFICATION_PATH="${SESSION_ROOT}/${RT22_PARENT_ID}/control/background.jsonl"
if [[ -f "$RT22_CHILD_REPORT" ]]; then
	cp "$RT22_CHILD_REPORT" "${ARTIFACT_DIR}/rt22-background-child.md"
fi
{
	echo "# RT22 Background Delegate Queue"
	echo
	echo "- parent_session_id: \`${RT22_PARENT_ID}\`"
	echo "- queue_job_id: \`${RT22_JOB_ID}\`"
	echo "- child_session_id: \`${RT22_CHILD_ID}\`"
	echo "- child_workdir: \`${RT22_CHILD_WORKDIR}\`"
	echo "- queue_show_status: \`$(extract_json_field "${RAW_DIR}/rt22-queue-show.json" "status")\`"
	echo "- background_notification_present: \`$([[ -f "$RT22_NOTIFICATION_PATH" ]] && grep -Fq "$RT22_JOB_ID" "$RT22_NOTIFICATION_PATH" && echo yes || echo no)\`"
	echo "- child_report_present: \`$([[ -f "$RT22_CHILD_REPORT" ]] && echo yes || echo no)\`"
	echo
	echo "## Notes"
	echo
	echo '- This scenario exercises `experimental delegate --background`, then proves completion via `experimental queue worker --once` and `experimental queue show`.'
	echo '- Success means the background child completed, wrote its report in an isolated workdir, and queued a parent notification in `control/background.jsonl`.'
	} >"${ARTIFACT_DIR}/rt22-background-delegate-queue.md"
if [[ "$(extract_json_field "${RAW_DIR}/rt22-queue-show.json" "status")" != "completed" ]] || [[ ! -f "$RT22_NOTIFICATION_PATH" ]] || ! grep -Fq "$RT22_JOB_ID" "$RT22_NOTIFICATION_PATH"; then
	RT22_EXIT=1
fi
finalize_scenario "RT22" "Background Delegate Queue" "$RT22_EXIT" "$RT22_RAW" "${ARTIFACT_DIR}/rt22-background-delegate-queue.md"

RT23_RAW="${RAW_DIR}/rt23-children.json"
if [[ -n "$RT22_PARENT_ID" ]]; then
	./bin/go-cli-agent experimental children "$RT22_PARENT_ID" --config "$CONFIG_PATH" --json >"$RT23_RAW" 2>&1
	RT23_EXIT=$?
else
	RT23_EXIT=1
fi
{
	echo "# RT23 Children Observability"
	echo
	echo "- parent_session_id: \`${RT22_PARENT_ID}\`"
	echo "- expected_child_session_id: \`${RT22_CHILD_ID}\`"
	echo "- expected_queue_job_id: \`${RT22_JOB_ID}\`"
	echo "- children_json_path: \`${RT23_RAW}\`"
	echo "- child_visible: \`$([[ -f "$RT23_RAW" ]] && grep -Fq "$RT22_CHILD_ID" "$RT23_RAW" && echo yes || echo no)\`"
	echo "- job_visible: \`$([[ -f "$RT23_RAW" ]] && grep -Fq "$RT22_JOB_ID" "$RT23_RAW" && echo yes || echo no)\`"
	echo
	echo "## Notes"
	echo
	echo '- This scenario exercises the store-oriented `experimental children` surface after a real background child run.'
	echo "- Success means the parent can observe both the child session and the queue job from the aggregated children view."
	} >"${ARTIFACT_DIR}/rt23-children-observability.md"
if [[ ! -f "$RT23_RAW" ]] || ! grep -Fq "$RT22_CHILD_ID" "$RT23_RAW" || ! grep -Fq "$RT22_JOB_ID" "$RT23_RAW"; then
	RT23_EXIT=1
fi
finalize_scenario "RT23" "Children Observability" "$RT23_EXIT" "$RT23_RAW" "${ARTIFACT_DIR}/rt23-children-observability.md"

RT24_PROMPT="${PROMPT_DIR}/rt24-isolated-main.prompt.txt"
RT24_RAW="${RAW_DIR}/rt24-isolated-main.json"
write_prompt "$RT24_PROMPT" "Review only product_overview.md and ops_notes.md in the current workspace.
Write reports/isolated-main.md with sections: scope, status.
Then call finish."
./bin/go-cli-agent exec \
	--config "$CONFIG_PATH" \
	--provider openai-compatible \
	--model "$MODEL" \
	--workdir "$ISOLATED_DIR" \
	--isolation copy \
	--json \
	<"$RT24_PROMPT" >"$RT24_RAW" 2>&1
RT24_EXIT=$?
RT24_SESSION_ID="$(extract_json_field "$RT24_RAW" "session_id")"
RT24_SESSION_PATH="${SESSION_ROOT}/${RT24_SESSION_ID}/session.json"
RT24_EFFECTIVE_WORKDIR=""
if [[ -f "$RT24_SESSION_PATH" ]]; then
		RT24_EFFECTIVE_WORKDIR="$(extract_json_field "$RT24_SESSION_PATH" "workdir")"
fi
RT24_CHILD_REPORT="${RT24_EFFECTIVE_WORKDIR}/reports/isolated-main.md"
RT24_PARENT_REPORT="${ISOLATED_DIR}/reports/isolated-main.md"
{
	echo "# RT24 Main-Path Isolated Exec"
	echo
	echo "- session_id: \`${RT24_SESSION_ID}\`"
	echo "- requested_workdir: \`${ISOLATED_DIR}\`"
	echo "- effective_workdir: \`${RT24_EFFECTIVE_WORKDIR}\`"
	if [[ -f "$RT24_SESSION_PATH" ]]; then
		echo "- isolation_mode: \`$(extract_isolation_mode "$RT24_SESSION_PATH")\`"
	fi
	echo "- isolated_report_present: \`$([[ -f "$RT24_CHILD_REPORT" ]] && echo yes || echo no)\`"
	echo "- parent_workspace_untouched: \`$([[ ! -f "$RT24_PARENT_REPORT" ]] && echo yes || echo no)\`"
	echo
	echo "## Notes"
	echo
	echo '- This scenario proves that main-path `exec --isolation copy` works outside delegation.'
	echo "- Success means the session used an isolated effective workdir and wrote the report only inside that isolated copy."
	} >"${ARTIFACT_DIR}/rt24-main-path-isolated-exec.md"
if [[ ! -f "$RT24_CHILD_REPORT" ]] || [[ -f "$RT24_PARENT_REPORT" ]]; then
	RT24_EXIT=1
fi
finalize_scenario "RT24" "Main-Path Isolated Exec" "$RT24_EXIT" "$RT24_RAW" "${ARTIFACT_DIR}/rt24-main-path-isolated-exec.md"

write_summary
write_issues
printf '%s\n' "$RUN_DIR"
if (( FAILED_SCENARIOS > 0 )); then
	exit 1
fi
