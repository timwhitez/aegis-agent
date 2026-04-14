#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

DEFAULT_OUT_DIR="${ROOT_DIR}/bin"
DEFAULT_BINARY_NAME="go-cli-agent"

TARGET_GOOS="${GO_CLI_AGENT_GOOS:-${GOOS:-}}"
TARGET_GOARCH="${GO_CLI_AGENT_GOARCH:-${GOARCH:-}}"
OUT_DIR="${GO_CLI_AGENT_BUILD_DIR:-$DEFAULT_OUT_DIR}"
OUT_PATH="${GO_CLI_AGENT_BUILD_OUT:-}"

if [[ -z "$OUT_PATH" ]]; then
	BINARY_NAME="$DEFAULT_BINARY_NAME"
	if [[ "$TARGET_GOOS" == "windows" ]]; then
		BINARY_NAME="${BINARY_NAME}.exe"
	fi
	OUT_PATH="${OUT_DIR}/${BINARY_NAME}"
fi

mkdir -p "$(dirname "$OUT_PATH")"

ENV_ARGS=()
if [[ -n "$TARGET_GOOS" ]]; then
	ENV_ARGS+=("GOOS=$TARGET_GOOS")
fi
if [[ -n "$TARGET_GOARCH" ]]; then
	ENV_ARGS+=("GOARCH=$TARGET_GOARCH")
fi

if [[ ${#ENV_ARGS[@]} -gt 0 ]]; then
	env "${ENV_ARGS[@]}" go build -trimpath -o "$OUT_PATH" ./cmd/go-cli-agent
else
	go build -trimpath -o "$OUT_PATH" ./cmd/go-cli-agent
fi

printf 'built %s' "$OUT_PATH"
if [[ -n "$TARGET_GOOS" || -n "$TARGET_GOARCH" ]]; then
	printf ' (target'
	if [[ -n "$TARGET_GOOS" ]]; then
		printf ' GOOS=%s' "$TARGET_GOOS"
	fi
	if [[ -n "$TARGET_GOARCH" ]]; then
		printf ' GOARCH=%s' "$TARGET_GOARCH"
	fi
	printf ')'
fi
printf '\n'
