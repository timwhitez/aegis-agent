# WebConsole Frontend Optimization Plan

## Scope

This document tracks the current frontend-only optimization plan for the local WebConsole under `internal/webconsole/assets/`.

Boundaries:

- Keep WebConsole as the local Web-first operator surface.
- Do not introduce a hosted SaaS shape, browser IDE, remote terminal, or frontend-owned source of truth.
- Do not move provider-specific replay or runtime decisions into browser code.
- Prefer small vanilla JS improvements that preserve the existing embedded-assets model.

## Current Baseline

Current resource size:

| File | Lines |
| --- | ---: |
| `internal/webconsole/assets/index.html` | 180 |
| `internal/webconsole/assets/styles.css` | 4,228 |
| `internal/webconsole/assets/app.js` | 2,986 |
| `internal/webconsole/assets/session-view.js` | 2,305 |
| `internal/webconsole/assets/utils.js` | 586 |
| `internal/webconsole/assets/events.js` | 454 |
| `internal/webconsole/assets/settings-view.js` | 424 |
| `internal/webconsole/assets/api.js` | 193 |
| `internal/webconsole/assets/workspace-view.js` | 317 |
| `internal/webconsole/assets/icons.js` | 56 |
| Total | 11,729 |

Current implemented facts:

- Script loading is still ordered global-script loading in `index.html`; no ES module graph exists yet.
- `app.js` still owns a large global `state` object, but it now includes polling/backoff state and selected workspace tree path.
- WebSocket reconnect has exponential backoff with jitter, visibility-state handling, and fallback polling coordination.
- Polling defaults to 5 seconds and uses a 1.6 second active interval while disconnected, generating, or tracking active descendants.
- Embedded assets now use ETag validation and gzip negotiation; long immutable hashed asset URLs are not implemented.
- Markdown rendering now has an LRU cache, lazy image rendering through `.md-img`, protocol filtering, and `rel="noopener noreferrer"` on links.
- Workspace file tree rendering uses delegated click and keyboard events, stores the selected path in `state.selectedTreePath`, and exposes tree/treeitem semantics for the current navigational file browser.
- Workspace file preview now requests bounded pages from `/api/file/read?offset=...&limit=...` and shows a local `Load more` continuation affordance when the backend reports truncation.
- Frontend unit coverage exists in `validation/scripts/webconsole_utils_test.mjs` for markdown, stale async responses, queue/job rendering, settings behavior, workspace selection races, workspace tree keyboard semantics, and stale paged file preview responses.

## Completed Since The First Draft

The earlier frontend plan contained stale findings. These items are now implemented or partially implemented and should not be reopened without fresh evidence:

- WebSocket reconnect no longer uses a fixed 3 second retry loop.
- Constant always-on 1.6 second polling is gone; active polling is now conditional.
- Static embedded assets support ETag and gzip.
- Markdown has cache coverage via `renderMarkdownCached`.
- Markdown links use `noopener noreferrer`, and images use a CSS class plus lazy loading.
- Workspace file tree click and keyboard handling are delegated instead of attaching one listener per node.
- Workspace tree semantics now expose `role="tree"` and `role="treeitem"` with `aria-level` and directory `aria-expanded` facts.
- Workspace file preview no longer reads the full file body in one request; the WebConsole requests 256 KiB pages, the backend caps page size, and large files can be continued with `Load more`.

## Remaining Optimization Backlog

### P0: Workspace Tree Accessibility And Keyboard Use

Status: implemented for the current navigational file tree.

Remaining target:

- Keep `role="tree"` on the container and `role="treeitem"` / `aria-level` / directory `aria-expanded` on nodes.
- Keep delegated keyboard support for Enter, Space, ArrowUp, ArrowDown, ArrowRight, and ArrowLeft.
- Add browser smoke or Playwright coverage if the project later adds a real DOM-based frontend test runner.
- If the workspace browser later changes from directory navigation to in-place tree expansion, revisit `aria-expanded` so it reflects visible expansion state rather than navigation intent.

Validation:

- `validation/scripts/webconsole_utils_test.mjs` should cover delegated tree semantics and keyboard activation/focus movement.

### P1: Replace `window.confirm` With Local Dialogs

Risky actions still use native dialogs in `app.js` and `settings-view.js`:

- coverage override
- goal clear
- skill uninstall
- session delete / clear all
- settings save when writing local API key or config

Plan:

- Add a small vanilla dialog helper based on `<dialog>` where available.
- Preserve low-friction ordinary start / steer / continue behavior.
- Use custom confirmation only for risk actions already identified by `spec/17-web-console.md`.

Validation:

- Node-level tests for confirmation promise behavior.
- WebConsole smoke for one destructive action cancellation path.

### P1: Large File Preview

Status: implemented for the current read-only workspace browser.

Current target:

- Keep bounded `offset` / `limit` support on `/api/file/read`.
- Keep workspace path escape and credential-like path checks in the backend before any paged read.
- Keep the frontend continuation affordance for large files rather than blocking the main thread with one huge text update.
- Add browser smoke or Playwright coverage if the project later adds a real DOM-based frontend test runner.

Validation:

- `TestServiceWorkspaceRoutesListReadAndRejectEscape` should cover offset / limit responses, invalid bounds, large sparse file preview, and existing escape / credential denials.
- `validation/scripts/webconsole_utils_test.mjs` should cover stale paged file responses and `Load more` continuation state.

### P1: Render State Isolation

The large global `state` object still mixes durable UI state, network handles, render caches, polling timers, selected queue job details, and workspace selection facts.

Plan:

- Introduce a tiny local store only when it reduces real coupling.
- Move render-only caches and handles out of `state` before broader view refactors.
- Avoid introducing React/Vue/Svelte or a second authority.

Validation:

- Selector/update tests if a store is introduced.
- Existing stale response tests must continue to pass.

### P2: Message And Timeline Rendering Scale

The message stream still renders through HTML-string generation and cache comparisons. This is acceptable for current v1, but long sessions can still pressure the main thread.

Plan:

- Consider append/update-by-message-id rendering for the chat stream.
- Add virtualization only once there is evidence from large sessions that simpler caching is insufficient.

Validation:

- Synthetic large-session fixture.
- Browser performance run before and after.

### P2: CSS Split And Optional Build Graph

`styles.css` remains a single large stylesheet. The global script order also remains implicit.

Plan:

- Split CSS by view only after current functional convergence is stable.
- Consider ES modules before any bundler.
- Add esbuild only if it demonstrably simplifies embedded asset production and validation.

Validation:

- `node --check` over all assets.
- WebConsole smoke against embedded assets.

## Not In Scope

- React/Vue/Svelte rewrite.
- Monaco/browser code editor.
- Remote terminal.
- Hosted deployment model.
- Provider replay, session state, or runtime control in the frontend.
- Runtime-level workflow guard changes for frontend render concerns.

## Validation Baseline

Use these commands for frontend-related slices:

```bash
node --check internal/webconsole/assets/app.js
node --check internal/webconsole/assets/session-view.js
node --check internal/webconsole/assets/workspace-view.js
node --check internal/webconsole/assets/events.js
node --check internal/webconsole/assets/settings-view.js
node --check internal/webconsole/assets/utils.js
node --check internal/webconsole/assets/api.js
node --check internal/webconsole/assets/icons.js
node validation/scripts/webconsole_utils_test.mjs
go test -timeout 120s ./internal/webconsole -count=1
```
