#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

GO_BIN="${GO_BIN:-go}"
OUT_DIR="${OUT_DIR:-$ROOT_DIR/bin}"
OUT_BIN="${OUT_BIN:-$OUT_DIR/ngen}"
GO_CACHE_DIR="${GOCACHE:-/tmp/ngen-go-build}"

usage() {
  cat <<'EOF'
usage:
  ./build.sh [all|fmt|test|build|help]

defaults:
  all   run gofmt, go test ./..., then build ./bin/ngen

env:
  GO_BIN   go command to use (default: go)
  GOCACHE  Go build cache (default: /tmp/ngen-go-build)
  OUT_DIR  output directory for built binary (default: ./bin)
  OUT_BIN  full output path for built binary (default: $OUT_DIR/ngen)
EOF
}

go_files() {
  if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    git ls-files 'cmd/**/*.go' 'internal/**/*.go' | sort
    return 0
  fi
  find cmd internal \
    \( -path 'internal/tui/real_task_tests/*/runs/*' -o -path '*/.ngen/*' -o -path '*/bin/*' \) -prune \
    -o -type f -name '*.go' -print | sort
}

run_fmt() {
  mapfile -t files < <(go_files)
  if [ "${#files[@]}" -eq 0 ]; then
    echo "no go files found under cmd/ or internal/"
    return 0
  fi
  echo "==> gofmt"
  gofmt -w "${files[@]}"
}

run_test() {
  echo "==> go test ./..."
  GOCACHE="$GO_CACHE_DIR" "$GO_BIN" test ./...
}

run_build() {
  mkdir -p "$OUT_DIR"
  echo "==> go build -o $OUT_BIN ./cmd/ngen"
  GOCACHE="$GO_CACHE_DIR" "$GO_BIN" build -o "$OUT_BIN" ./cmd/ngen
  echo "built $OUT_BIN"
}

run_all() {
  run_fmt
  run_test
  run_build
}

TARGET="${1:-all}"

case "$TARGET" in
  all)
    run_all
    ;;
  fmt)
    run_fmt
    ;;
  test)
    run_test
    ;;
  build)
    run_build
    ;;
  help|-h|--help)
    usage
    ;;
  *)
    echo "unknown target: $TARGET" >&2
    usage >&2
    exit 2
    ;;
esac
