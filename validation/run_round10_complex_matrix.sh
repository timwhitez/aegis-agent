#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

export GO_CLI_AGENT_MATRIX_LABEL="round10-complex-matrix"
exec ./validation/run_round8_complex_matrix.sh "$@"
