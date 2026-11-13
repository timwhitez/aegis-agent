# Validation

Deterministic, repository-owned validation helpers. Everything here runs
without provider credentials.

- `cmd/`: small Go fixtures for runtime and browser regression tests, including
  `budgetsmokeprovider`, the scripted provider used by the browser suite.
- `scripts/*_test.mjs`: `node --test` suites for the embedded Web console
  (`ui_*`: API client, store, i18n parity and key coverage, markdown sanitizer,
  session model, send recovery, approval operations, session actions, workspace
  and settings models, vendored library hashes). Included in `./test.sh`.
- `scripts/webconsole_e2e.mjs` and the `*_e2e.mjs` suites it orchestrates: the
  credential-free Playwright acceptance run through the real local service.

Historical live-provider campaigns, generated session data, screenshots and
ad-hoc audit output do not belong in source control. Local output is ignored
under `validation/runs/`, `validation/sessions/` and `validation/tmp/`.

Focused runs:

```sh
go test ./internal/runtime -count=1
npm test                      # node --test validation/scripts/*_test.mjs
```

Browser acceptance (install the pinned browser runtime once):

```sh
npm ci --ignore-scripts
npx playwright install chromium
npm run test:e2e
```

Useful options while iterating:

```sh
node validation/scripts/webconsole_e2e.mjs --only=shell,session   # areas: server, shell, session, plan,
                                                                  # approvals, receipts, recovery, showcase, pages
node validation/scripts/webconsole_e2e.mjs --output=validation/runs/my-run --keep-runtime
E2E_APPROVAL_PROFILES=all npm run test:e2e   # receipts in all locale × viewport profiles
E2E_WEB_WORKERS=4 npm run test:e2e          # queue workers for the test server (default 2)
```

The suite builds the binary from the working tree (the UI is embedded at build
time) and resolves its temp root with `realpath`, so it also runs on macOS
without a `TMPDIR` override. Known product bugs, if any, are listed in
`scripts/e2e/known-bugs.mjs` and reported as known failures; the list should
be empty on `main`.

The browser gate covers Chinese-by-default and persistent English switching,
light-theme behavior even when the OS requests dark mode, hit targets (32px for
fine pointers, 44px on touch/mobile), keyboard/modal semantics, the session
sidebar, command palette and shortcuts, session start/continue/steer/interrupt/
stop, Goal and inline Plan Mode approvals/questions/receipts, send recovery,
Todo/task grouping, workspace CRUD/download, skill package install/removal,
provider settings probe, history deletion/clear, desktop/mobile layouts, removed
legacy routes, and console/request errors. It writes populated Context
screenshots in both locales and checks that durable raw values are never
translated. Each run writes PNG screenshots plus `manifest.json` (commit,
browser, viewport, locale, hashes, check results) below `validation/runs/`.
