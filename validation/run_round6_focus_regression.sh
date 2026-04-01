#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKSPACE_ROOT="$(cd "${ROOT_DIR}/.." && pwd)"
cd "$ROOT_DIR"

: "${OPENAI_API_KEY:?OPENAI_API_KEY is required}"

MODEL="${GO_CLI_AGENT_LIVE_MODEL:-gpt-5.4}"
CONFIG_PATH="validation/config.openai-compatible.yaml"
ROUND_ID="${1:-$(date -u +%F)-openai-compatible-${MODEL}-round6-focus-regression}"
RUN_DIR="validation/runs/${ROUND_ID}"
RAW_DIR="${RUN_DIR}/raw"
PROMPT_DIR="${RUN_DIR}/prompts"
ARTIFACT_DIR="${RUN_DIR}/artifacts"
NOTE_DIR="${RUN_DIR}/notes"

if [[ -e "$RUN_DIR" ]]; then
	echo "run directory already exists: $RUN_DIR" >&2
	exit 1
fi

mkdir -p "$RAW_DIR" "$PROMPT_DIR" "$ARTIFACT_DIR" "$NOTE_DIR"
ABS_ARTIFACT_DIR="$(cd "$ARTIFACT_DIR" && pwd)"
printf 'scenario_id\tlabel\traw\tartifact\tsession_id\n' >"${NOTE_DIR}/scenario-index.tsv"

write_prompt() {
	local path="$1"
	shift
	printf '%s\n' "$*" >"$path"
}

extract_session_id() {
	local path="$1"
	grep -o '"session_id":"[^"]*"' "$path" | tail -n1 | cut -d'"' -f4
}

run_exec() {
	local prompt_path="$1"
	local raw_path="$2"
	local workdir="$3"
	local timeout_sec="${4:-300}"
	./bin/go-cli-agent exec \
		--config "$CONFIG_PATH" \
		--provider openai-compatible \
		--model "$MODEL" \
		--workdir "$workdir" \
		--json \
		--timeout "$timeout_sec" \
		<"$prompt_path" >"$raw_path" 2>&1
}

record_scenario() {
	local scenario_id="$1"
	local label="$2"
	local raw_path="$3"
	local artifact_path="$4"
	local session_id="${5:-}"
	printf '%s\t%s\t%s\t%s\t%s\n' "$scenario_id" "$label" "$raw_path" "$artifact_path" "$session_id" >>"${NOTE_DIR}/scenario-index.tsv"
}

./build.sh >"${RAW_DIR}/preflight-build.txt" 2>&1
./test.sh >"${RAW_DIR}/preflight-test.txt" 2>&1
./bin/go-cli-agent doctor \
	--config "$CONFIG_PATH" \
	--provider openai-compatible \
	--json >"${RAW_DIR}/preflight-doctor.json" 2>&1
./bin/go-cli-agent probe-provider \
	--config "$CONFIG_PATH" \
	--provider openai-compatible \
	--json >"${RAW_DIR}/preflight-probe.json" 2>&1

RT03_PROMPT="${PROMPT_DIR}/rt03-reference-corpus.prompt.txt"
write_prompt "$RT03_PROMPT" "Read only these top-level reference documents: pi-coding-agent.md, bitter-lesson-agent-frameworks.md, learn-claude-code.md, openai-com__harness-engineering.md, developers-openai-com__run-long-horizon-tasks-with-codex.md, anthropic-com__effective-harnesses-for-long-running-agents.md, blog-langchain-com__autonomous-context-compression.md, and blog-langchain-com__the-anatomy-of-an-agent-harness.md.
Do not inspect go-cli-agent source code for this task.
Write ${ABS_ARTIFACT_DIR}/rt03-reference-corpus-brief.md with sections: recurring patterns, useful tensions, and concrete ideas go-cli-agent should import.
Then call finish with a one-line summary."
RT03_RAW="${RAW_DIR}/rt03-reference-corpus.jsonl"
run_exec "$RT03_PROMPT" "$RT03_RAW" "$WORKSPACE_ROOT" 300
record_scenario "RT03" "Reference Corpus Synthesis" "$RT03_RAW" "${ARTIFACT_DIR}/rt03-reference-corpus-brief.md" "$(extract_session_id "$RT03_RAW")"

RT11_PROMPT="${PROMPT_DIR}/rt11-explicit-skill-loading.prompt.txt"
write_prompt "$RT11_PROMPT" "Use the repo_audit skill for this task.
Audit whether the default core tool surface and docs stay aligned with core v1 boundaries.
Call load_skill before deeper inspection.
Ignore validation/runs, validation/sessions, reports, bin, tmp, and generated artifacts.
Write ${ABS_ARTIFACT_DIR}/rt11-repo-audit-skill.md with sections: validated findings, remaining risks, smallest next fixes.
Then call finish with a one-line summary."
RT11_RAW="${RAW_DIR}/rt11-explicit-skill-loading.jsonl"
run_exec "$RT11_PROMPT" "$RT11_RAW" "$ROOT_DIR" 300
record_scenario "RT11" "Explicit Skill Loading" "$RT11_RAW" "${ARTIFACT_DIR}/rt11-repo-audit-skill.md" "$(extract_session_id "$RT11_RAW")"

RT15_PROMPT="${PROMPT_DIR}/rt15-traceability-map.prompt.txt"
write_prompt "$RT15_PROMPT" "Inspect only README.md, AGENTS.md, spec/00-product.md, spec/01-runtime-architecture.md, spec/03-provider-contracts.md, spec/12-task-system.md, spec/13-live-input-and-steering.md, cmd/go-cli-agent/main.go, internal/app, internal/runtime, internal/provider, internal/session, and internal/tools.
Ignore validation/runs, validation/sessions, reports, bin, tmp, and generated artifacts.
Write ${ABS_ARTIFACT_DIR}/rt15-traceability-map.md with sections: core promise to owning files, strongest existing tests, gaps still hard to trace.
Then call finish with a one-line summary."
RT15_RAW="${RAW_DIR}/rt15-traceability-map.jsonl"
run_exec "$RT15_PROMPT" "$RT15_RAW" "$ROOT_DIR" 300
record_scenario "RT15" "Traceability Map" "$RT15_RAW" "${ARTIFACT_DIR}/rt15-traceability-map.md" "$(extract_session_id "$RT15_RAW")"

RT16_PROMPT="${PROMPT_DIR}/rt16-codex-steer-audit.prompt.txt"
write_prompt "$RT16_PROMPT" "Inspect only codex/AGENTS.md, codex/docs/agents_md.md, codex/docs/sandbox.md, codex/codex-rs/core/gpt_5_codex_prompt.md, and codex/codex-rs/app-server/tests/suite/v2/turn_steer.rs.
Do not inspect go-cli-agent code for this task.
Write ${ABS_ARTIFACT_DIR}/rt16-codex-steer-audit.md with sections: confirmed codex patterns, design ideas worth borrowing, cautions for go-cli-agent.
Then call finish with a one-line summary."
RT16_RAW="${RAW_DIR}/rt16-codex-steer-audit.jsonl"
run_exec "$RT16_PROMPT" "$RT16_RAW" "$WORKSPACE_ROOT" 300
record_scenario "RT16" "Codex Steer And Sandbox Audit" "$RT16_RAW" "${ARTIFACT_DIR}/rt16-codex-steer-audit.md" "$(extract_session_id "$RT16_RAW")"

RT17_PROMPT="${PROMPT_DIR}/rt17-codex-proxy-audit.prompt.txt"
write_prompt "$RT17_PROMPT" "Inspect only codex/codex-rs/responses-api-proxy/README.md, codex/codex-rs/process-hardening/README.md, codex/docs/authentication.md, and codex/docs/config.md.
Focus on OpenAI-compatible Responses transport, auth handling, and local hardening patterns.
Write ${ABS_ARTIFACT_DIR}/rt17-codex-proxy-audit.md with sections: confirmed contracts, hardening ideas worth importing, mismatch risks for go-cli-agent.
Then call finish with a one-line summary."
RT17_RAW="${RAW_DIR}/rt17-codex-proxy-audit.jsonl"
run_exec "$RT17_PROMPT" "$RT17_RAW" "$WORKSPACE_ROOT" 300
record_scenario "RT17" "Codex Responses Proxy Audit" "$RT17_RAW" "${ARTIFACT_DIR}/rt17-codex-proxy-audit.md" "$(extract_session_id "$RT17_RAW")"

RT18_PROMPT="${PROMPT_DIR}/rt18-opencode-task-review.prompt.txt"
write_prompt "$RT18_PROMPT" "Inspect only opencode/AGENTS.md, opencode/packages/opencode/AGENTS.md, opencode/README.md, opencode/packages/opencode/src/session/prompt.ts, opencode/packages/opencode/src/session/todo.ts, and opencode/packages/opencode/src/session/processor.ts.
Focus on large-project task execution, todo discipline, and prompt/reminder behavior.
Write ${ABS_ARTIFACT_DIR}/rt18-opencode-task-review.md with sections: confirmed strengths, likely tradeoffs, ideas go-cli-agent should adopt.
Then call finish with a one-line summary."
RT18_RAW="${RAW_DIR}/rt18-opencode-task-review.jsonl"
run_exec "$RT18_PROMPT" "$RT18_RAW" "$WORKSPACE_ROOT" 300
record_scenario "RT18" "OpenCode Task And Prompt Review" "$RT18_RAW" "${ARTIFACT_DIR}/rt18-opencode-task-review.md" "$(extract_session_id "$RT18_RAW")"

RT19_PROMPT="${PROMPT_DIR}/rt19-opencode-responses-audit.prompt.txt"
write_prompt "$RT19_PROMPT" "Inspect only opencode/specs/project.md, opencode/packages/opencode/src/provider/sdk/copilot/responses/openai-responses-language-model.ts, opencode/packages/opencode/src/provider/sdk/copilot/responses/map-openai-responses-finish-reason.ts, opencode/packages/opencode/src/provider/sdk/copilot/responses/convert-to-openai-responses-input.ts, and opencode/packages/opencode/src/provider/sdk/copilot/responses/openai-responses-prepare-tools.ts.
Focus on Responses replay, tool preparation, and finish-reason mapping.
Write ${ABS_ARTIFACT_DIR}/rt19-opencode-responses-audit.md with sections: confirmed behavior, differences from go-cli-agent assumptions, useful implementation ideas.
Then call finish with a one-line summary."
RT19_RAW="${RAW_DIR}/rt19-opencode-responses-audit.jsonl"
run_exec "$RT19_PROMPT" "$RT19_RAW" "$WORKSPACE_ROOT" 300
record_scenario "RT19" "OpenCode Responses Provider Audit" "$RT19_RAW" "${ARTIFACT_DIR}/rt19-opencode-responses-audit.md" "$(extract_session_id "$RT19_RAW")"

RT20_PROMPT="${PROMPT_DIR}/rt20-large-project-readiness.prompt.txt"
write_prompt "$RT20_PROMPT" "Inspect only go-cli-agent/README.md, go-cli-agent/AGENTS.md, go-cli-agent/spec/00-product.md, go-cli-agent/spec/01-runtime-architecture.md, go-cli-agent/spec/12-task-system.md, go-cli-agent/spec/13-live-input-and-steering.md, pi-coding-agent.md, bitter-lesson-agent-frameworks.md, learn-claude-code.md, codex/README.md, and opencode/README.md.
Use this evidence to judge whether go-cli-agent is ready to surpass codex and opencode on large-project development and review.
Write ${ABS_ARTIFACT_DIR}/rt20-large-project-readiness.md with sections: current strengths, remaining gaps, next architectural moves.
Then call finish with a one-line summary."
RT20_RAW="${RAW_DIR}/rt20-large-project-readiness.jsonl"
run_exec "$RT20_PROMPT" "$RT20_RAW" "$WORKSPACE_ROOT" 360
record_scenario "RT20" "Large Project Readiness Memo" "$RT20_RAW" "${ARTIFACT_DIR}/rt20-large-project-readiness.md" "$(extract_session_id "$RT20_RAW")"

printf '%s\n' "$RUN_DIR"
