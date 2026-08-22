# Validation

This directory contains deterministic, repository-owned validation helpers.
They are safe to run without provider credentials and are included in
`./test.sh`:

- `cmd/`: small Go fixtures used by runtime and browser-budget regression tests.
- `scripts/webconsole_utils_test.mjs`: unit tests for embedded Web console
  utility behavior.

Historical live-provider campaigns, generated session data, screenshots, and
ad-hoc audit output do not belong in source control. Local validation output is
ignored under `validation/runs/`, `validation/sessions/`, and `validation/tmp/`.

Use package-level Go tests for focused work, for example:

```sh
go test ./internal/runtime -count=1
node --test validation/scripts/webconsole_utils_test.mjs
```
