#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

GO_VERSION="$(go env GOVERSION)"
if [[ ! "$GO_VERSION" =~ ^go([0-9]+)\.([0-9]+)(\.([0-9]+))? ]]; then
	printf 'unable to parse Go version: %s\n' "$GO_VERSION" >&2
	exit 1
fi
GO_MAJOR="${BASH_REMATCH[1]}"
GO_MINOR="${BASH_REMATCH[2]}"
GO_PATCH="${BASH_REMATCH[4]:-0}"
if (( GO_MAJOR < 1 || (GO_MAJOR == 1 && GO_MINOR < 26) || (GO_MAJOR == 1 && GO_MINOR == 26 && GO_PATCH < 7) )); then
	printf 'Go 1.26.7 or newer is required; found %s\n' "$GO_VERSION" >&2
	exit 1
fi

if [[ ! -x ./build.sh ]]; then
	printf 'build.sh must be executable because README documents ./build.sh\n' >&2
	exit 1
fi

UNFORMATTED="$(gofmt -l cmd internal pkg validation/cmd)"
if [[ -n "$UNFORMATTED" ]]; then
  printf 'gofmt needed for:\n%s\n' "$UNFORMATTED" >&2
  exit 1
fi

if ! command -v node >/dev/null 2>&1; then
  printf 'node is required for WebConsole JS syntax checks\n' >&2
  exit 1
fi

for js_file in internal/webconsole/assets/*.js internal/webconsole/assets-v2/*.js; do
  node --check "$js_file"
done

node --test validation/scripts/*_test.mjs

PKG_PATTERNS=(
  ./cmd/...
  ./internal/...
  ./pkg/...
  ./validation/cmd/...
)

# Keep the repo-level test surface on the owned module packages. The
# validation tree contains copied historical workspaces and prior run artifacts
# that are useful as evidence, but they are not part of the default module
# acceptance surface. Force a real run so a stale or stuck Go test cache cannot
# make the repo-level gate hang after package tests have already completed.
go test -count=1 -timeout=2m "${PKG_PATTERNS[@]}"
