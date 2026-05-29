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
| `internal/webconsole/assets/styles.css` | 4,355 |
| `internal/webconsole/assets/app.js` | 3,151 |
| `internal/webconsole/assets/session-view.js` | 2,297 |
| `internal/webconsole/assets/utils.js` | 673 |
| `internal/webconsole/assets/events.js` | 454 |
| `internal/webconsole/assets/settings-view.js` | 432 |
| `internal/webconsole/assets/api.js` | 193 |
| `internal/webconsole/assets/workspace-view.js` | 410 |
| `internal/webconsole/assets/icons.js` | 56 |
| Total | 12,201 |

Current implemented facts:

- Script loading is still ordered global-script loading in `index.html`; no ES module graph exists yet.
- `app.js` still owns a large global `state` object, but render-only chat diff cache has been moved into `renderState.chatCache`, transient WebSocket / polling / queued-refresh / layout observer handles live in `runtimeHandles`, page visibility tracking lives in `pageLifecycleViewState`, Settings config request sequencing lives in `settingsViewState`, Workspace navigation/read request sequencing lives in `workspaceViewState`, Skills catalog request sequencing and upload pending guard live in `skillsViewState`, Overview request sequencing plus in-flight/queued refresh flags live in `overviewViewState`, History page request sequencing lives in `historyViewState`, History parent-row expansion ids live in `historyExpansionViewState`, earlier-message paging request sequencing lives in `messagePagingViewState`, transient toast id allocation lives in `toastViewState`, stop-action pending session ids live in `stopActionViewState`, floating tracker panel expansion preferences live in `floatingPanelViewState`, pending Plan Mode input selections live in `planInputViewState`, shortcut help overlay visibility lives in `helpViewState`, composer input empty-state tracking lives in `composerViewState`, and next-send interrupt / Goal / Plan Mode composer control intent lives in `composerControlViewState`; selected workspace tree path and durable message paging facts remain in `state`.
- WebSocket reconnect has exponential backoff with jitter, visibility-state handling, and fallback polling coordination.
- Polling defaults to 5 seconds and uses a 1.6 second active interval while disconnected, generating, or tracking active descendants.
- Embedded assets now use ETag validation and gzip negotiation; long immutable hashed asset URLs are not implemented.
- Markdown rendering now has an LRU cache, lazy image rendering through `.md-img`, protocol filtering, and `rel="noopener noreferrer"` on links.
- Workspace file tree rendering uses delegated click and keyboard events, stores the selected path in `state.selectedTreePath`, and exposes tree/treeitem semantics for the current navigational file browser.
- Workspace file preview now requests bounded pages from `/api/file/read?offset=...&limit=...` and shows a local `Load more` continuation affordance when the backend reports truncation.
- Risk confirmations now use a local promise-based dialog helper instead of native `window.confirm`, with danger styling for destructive or credential-writing actions.
- Frontend unit coverage exists in `validation/scripts/webconsole_utils_test.mjs` for markdown, stale async responses, queue/job rendering, settings behavior, local confirmation behavior, workspace selection races, workspace tree keyboard semantics, stale paged file preview responses, and isolated render-state helpers.

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
- Native browser confirmation dialogs have been replaced for coverage override, goal clear, skill uninstall, session delete / clear all, settings save, and child-session open confirmation.
- Chat stream diff cache no longer lives on the main global `state`; it is isolated in `renderState.chatCache` with helper accessors and invalidation.
- WebSocket object, reconnect timer / attempts, polling timer / interval, queued refresh timers, and layout observer no longer live on the main global `state`; they are isolated in `runtimeHandles`.
- Page visibility tracking no longer lives on the main global `state`; it is isolated in `pageLifecycleViewState` while preserving hidden-tab polling suppression and reconnect behavior.
- Settings config request sequencing no longer lives on the main global `state`; it is isolated in `settingsViewState` while preserving stale config response suppression.
- Workspace directory/file request sequencing no longer lives on the main global `state`; it is isolated in `workspaceViewState` while preserving stale directory, file, and paged preview suppression.
- Skills catalog request sequencing no longer lives on the main global `state`; it is isolated in `skillsViewState` while preserving stale catalog response suppression.
- Skills upload pending state no longer lives on the main global `state`; it is isolated in `skillsViewState.uploadInFlight` while preserving duplicate upload suppression and pending control restoration.
- Overview request sequencing no longer lives on the main global `state`; it is isolated in `overviewViewState` while preserving stale and queued overview refresh suppression.
- Overview in-flight and queued refresh flags no longer live on the main global `state`; they are isolated in `overviewViewState` while preserving queued refresh coalescing and stale overview suppression.
- History page request sequencing no longer lives on the main global `state`; it is isolated in `historyViewState` while preserving stale and queued history page suppression.
- Earlier-message paging request sequencing no longer lives on the main global `state`; it is isolated in `messagePagingViewState` while preserving stale page suppression after session switches.
- Toast id allocation no longer lives on the main global `state`; it is isolated in `toastViewState` while preserving deterministic unique toast ids.
- Stop-action pending session ids no longer live on the main global `state`; they are isolated in `stopActionViewState` while preserving duplicate stop suppression and pending cleanup.
- History parent-row expansion ids no longer live on the main global `state`; they are isolated in `historyExpansionViewState` while preserving expanded parent/child rendering in the Sessions view.
- Todo/task, file-change, and sub-agent floating panel expansion preferences no longer live on the main global `state`; they are isolated in `floatingPanelViewState` while preserving the existing localStorage keys and expanded/collapsed rendering.
- Pending Plan Mode input selections no longer live on the main global `state`; they are isolated in `planInputViewState` while preserving request-scoped cleanup, selected answer rendering, and submitted answer payloads.
- Shortcut help overlay visibility no longer lives on the main global `state`; it is isolated in `helpViewState` while preserving `?` shortcut toggling and overlay close behavior.
- Composer input empty-state tracking no longer lives on the main global `state`; it is isolated in `composerViewState` while preserving input-height handling and UI refresh throttling when the input crosses empty/nonempty boundaries.
- Next-send interrupt steer intent no longer lives on the main global `state`; it is isolated in `composerControlViewState` while preserving the `interrupt: true` steer payload, interrupt-armed label, placeholder, button classes, and success-path cleanup.
- Goal and Plan Mode composer toggle intent no longer lives on the main global `state`; it is isolated in `composerControlViewState.mode` while preserving mutually exclusive toggle rendering and the `goal` / `planMode` start/continue payloads.

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

Status: implemented for current WebConsole confirmation paths.

Current target:

- Keep `confirmLocalAction` as a local UI helper and do not reintroduce native `window.confirm` in production frontend assets.
- Preserve low-friction ordinary start / steer / continue behavior; only risk actions and explicit navigation confirmation should prompt.
- Keep destructive / credential-writing paths on the danger variant.
- Add browser smoke or Playwright coverage if the project later adds a real DOM-based frontend test runner.

Validation:

- `validation/scripts/webconsole_utils_test.mjs` should cover confirmation promise resolution, destructive cancellation, and settings-save cancellation without native `window.confirm`.
- `rg -n "window\\.confirm|[^A-Za-z]confirm\\(" internal/webconsole/assets/{app,settings-view,utils}.js` should return no matches.

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

The large global `state` object still mixes durable UI state, selected queue job details, workspace selection facts, and durable message paging facts. The render-state isolation slices have moved the chat stream diff cache out of `state` into `renderState.chatCache`, transient WebSocket / polling / refresh / observer handles into `runtimeHandles`, page visibility tracking into `pageLifecycleViewState`, Settings config request sequencing into `settingsViewState`, Workspace request sequencing into `workspaceViewState`, Skills catalog request sequencing and upload pending guard into `skillsViewState`, Overview request sequencing and in-flight/queued refresh flags into `overviewViewState`, History page request sequencing into `historyViewState`, History parent-row expansion ids into `historyExpansionViewState`, earlier-message paging request sequencing into `messagePagingViewState`, toast id allocation into `toastViewState`, stop-action pending ids into `stopActionViewState`, floating tracker panel expansion preferences into `floatingPanelViewState`, pending Plan Mode input selections into `planInputViewState`, shortcut help overlay visibility into `helpViewState`, composer input empty-state tracking into `composerViewState`, and next-send interrupt / Goal / Plan Mode composer control intent into `composerControlViewState`.

Plan:

- Introduce a tiny local store only when it reduces real coupling.
- Continue moving remaining transient request guards or view-local facts out of `state` where that reduces real coupling.
- Avoid introducing React/Vue/Svelte or a second authority.

Validation:

- `validation/scripts/webconsole_utils_test.mjs` should assert that chat render cache is not stored on the main `state`, and that cache invalidation still works.
- `validation/scripts/webconsole_utils_test.mjs` should assert that runtime handles are not stored on the main `state`, and that queued refresh handles still clear through the helper path.
- `validation/scripts/webconsole_utils_test.mjs` should assert that page visibility tracking is not stored on the main `state`, and that hidden pages suppress polling while visible pages resume eligible polling.
- `validation/scripts/webconsole_utils_test.mjs` should assert that Settings config request sequencing is not stored on the main `state`, and that stale Settings config responses remain ignored.
- `validation/scripts/webconsole_utils_test.mjs` should assert that Workspace request sequencing is not stored on the main `state`, and that stale Workspace directory/file/page responses remain ignored.
- `validation/scripts/webconsole_utils_test.mjs` should assert that Skills catalog request sequencing is not stored on the main `state`, and that stale Skills catalog responses remain ignored.
- `validation/scripts/webconsole_utils_test.mjs` should assert that Skills upload pending state is not stored on the main `state`, and that duplicate upload suppression plus pending control restoration still work.
- `validation/scripts/webconsole_utils_test.mjs` should assert that Overview request sequencing and in-flight/queued refresh flags are not stored on the main `state`, and that stale queued Overview responses remain ignored.
- `validation/scripts/webconsole_utils_test.mjs` should assert that History page request sequencing is not stored on the main `state`, and that stale queued History page responses remain ignored.
- `validation/scripts/webconsole_utils_test.mjs` should assert that earlier-message page request sequencing is not stored on the main `state`, and that stale message page responses remain ignored after session switches.
- `validation/scripts/webconsole_utils_test.mjs` should assert that toast id allocation is not stored on the main `state`, and that unique toast ids are still generated.
- `validation/scripts/webconsole_utils_test.mjs` should assert that stop-action pending session ids are not stored on the main `state`, and that duplicate stop suppression and cleanup still work.
- `validation/scripts/webconsole_utils_test.mjs` should assert that History parent-row expansion ids are not stored on the main `state`, and that expanded parent/child rendering still works.
- `validation/scripts/webconsole_utils_test.mjs` should assert that floating tracker panel expansion preferences are not stored on the main `state`, and that localStorage persistence plus expanded/collapsed rendering still work.
- `validation/scripts/webconsole_utils_test.mjs` should assert that pending Plan Mode input selections are not stored on the main `state`, and that request-scoped cleanup plus answer submission still work.
- `validation/scripts/webconsole_utils_test.mjs` should assert that shortcut help overlay visibility is not stored on the main `state`, and that overlay rendering plus close behavior still work.
- `validation/scripts/webconsole_utils_test.mjs` should assert that composer input empty-state tracking is not stored on the main `state`, and that input events still refresh the UI only when empty/nonempty state changes.
- `validation/scripts/webconsole_utils_test.mjs` should assert that next-send interrupt steer intent is not stored on the main `state`, and that running-session steer still posts `interrupt: true` when armed and clears the intent after successful queueing.
- `validation/scripts/webconsole_utils_test.mjs` should assert that Goal and Plan Mode composer toggle intent is not stored on the main `state`, and that start / continue payloads still include the selected goal or Plan Mode draft.
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
