# Validation

This directory contains deterministic, repository-owned validation helpers.
They are safe to run without provider credentials and are included in
`./test.sh`:

- `cmd/`: small Go fixtures used by runtime and browser-budget regression tests.
- `scripts/webconsole_utils_test.mjs`: unit tests for embedded Web console
  utility behavior.
- `scripts/webconsole_e2e.mjs`: a credential-free Playwright journey through
  the real local service and deterministic provider fixture.

Historical live-provider campaigns, generated session data, screenshots, and
ad-hoc audit output do not belong in source control. Local validation output is
ignored under `validation/runs/`, `validation/sessions/`, and `validation/tmp/`.

Use package-level Go tests for focused work, for example:

```sh
go test ./internal/runtime -count=1
npm test
```

Run the browser acceptance suite after installing the pinned browser runtime:

```sh
npm ci --ignore-scripts
npx playwright install chromium
npm run test:e2e
```

The browser gate covers Chinese-by-default and persistent English switching,
light-theme behavior even when the OS requests dark mode, minimum 44px action
targets, keyboard/modal semantics, session start/continue/steer/interrupt/stop,
Goal and Plan Mode, Todo/task grouping, workspace CRUD/download, skill package
install/removal, provider settings probe, legacy rollback enablement, history
deletion/clear, desktop/mobile layouts, and console/request errors. It writes
PNG screenshots plus `manifest.json` with hashes and check results below the
ignored `validation/runs/` directory.
