#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

UNFORMATTED="$(gofmt -l cmd internal pkg validation/cmd)"
if [[ -n "$UNFORMATTED" ]]; then
  printf 'gofmt needed for:\n%s\n' "$UNFORMATTED" >&2
  exit 1
fi

if ! command -v node >/dev/null 2>&1; then
  printf 'node is required for WebConsole JS syntax checks\n' >&2
  exit 1
fi

for js_file in internal/webconsole/assets/*.js; do
  node --check "$js_file"
done

PKG_PATTERNS=(
  ./cmd/...
  ./internal/...
  ./pkg/...
  ./validation/cmd/...
)

# Keep the repo-level test surface on the owned module packages. The
# validation tree contains copied historical workspaces and prior run artifacts
# that are useful as evidence, but they are not part of the default module
# acceptance surface.
go test "${PKG_PATTERNS[@]}"
