#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [[ -z "${GO_CLI_AGENT_MATRIX_LABEL:-}" ]]; then
	export GO_CLI_AGENT_MATRIX_LABEL="round53-focused-retry-resume-webconsole-queue-followup"
fi

exec "${SCRIPT_DIR}/run_webconsole_followup_validation.sh" "$@"
