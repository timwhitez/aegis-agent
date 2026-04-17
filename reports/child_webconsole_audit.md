# Child B Webconsole Audit

## Scope

- `internal/webconsole/assets/app.js`
- `internal/webconsole/assets/index.html`
- `internal/webconsole/service.go`
- Relevant behavior expectations from `spec/17-web-console.md`

## Summary

The current webconsole chat and history flows are functional and reasonably coherent, but there are a few UX/logic mismatches around loading/error states, navigation continuity, and destructive-action feedback. State is mostly centralized in the frontend store and driven by backend durable-session APIs, which is a good fit for the product model. The biggest high-confidence issue is that history loading failures can leave the pagination state advanced even though the UI still shows stale prior-page results.

## Findings

### 1. State management is mostly consistent

- Chat and history share a single client-side state object in `internal/webconsole/assets/app.js:1`, including `sessionId`, `sessionDetail`, `historyData`, `historyPage`, `isGenerating`, and refresh flags.
- `openSession()` resets transient chat-specific state before loading durable detail, which avoids mixing optimistic messages from a previous session into the newly opened one in `internal/webconsole/assets/app.js:1622`.
- History paging state is persisted through local storage via `persistUIState()` / `restoreUIState()`, which helps continuity across reloads in `internal/webconsole/assets/app.js:2318` and `internal/webconsole/assets/app.js:2329`.
- Destructive and runtime actions are routed through explicit backend endpoints (`/interrupt`, `/stop`, `/history`, `/clear`) that match the durable-session model exposed by `internal/webconsole/service.go:386` and `internal/webconsole/service.go:508`.

### 2. Chat interactions are clear, but a few behaviors are slightly surprising

- `Enter` sends and `Shift+Enter` inserts newline, which is a good default in `internal/webconsole/assets/app.js:263`.
- Starting a new session while a run is active explicitly warns that the previous run may still settle in the background, which is honest and matches the architecture in `internal/webconsole/assets/app.js:249`.
- `requestInterrupt()` and `requestStop()` are guarded by both `isGenerating` and a durable session check, preventing obviously invalid operations in `internal/webconsole/assets/app.js:487` and `internal/webconsole/assets/app.js:509`.
- Minor UX mismatch: `openSession()` immediately switches to chat before the durable session refresh completes, so the user can briefly land on a mostly reset chat shell with a loading activity card instead of a more explicit page-level loading state in `internal/webconsole/assets/app.js:1622`.

### 3. History interactions are workable, but error/empty states are uneven

- Initial history load shows a dedicated loading panel, which is good, via `fetchHistory()` in `internal/webconsole/assets/app.js:2215`.
- Empty history is handled cleanly with both “No saved sessions yet” and an empty-panel body in `internal/webconsole/assets/app.js:2258`.
- For refresh failures after data already exists, the code only shows a toast and keeps the stale list visible; that is acceptable, but there is no inline stale/error indicator, so users cannot tell whether the list is intentionally unchanged or failed to refresh in `internal/webconsole/assets/app.js:2234`.
- History has `Prev`/`Next` only; no page jump or page-size control. This is not a bug, but it is a notable UX limitation relative to the otherwise operational console design in `internal/webconsole/assets/app.js:2286`.

### 4. Navigation continuity is decent, with one rough edge

- Clicking a history session or its parent opens that session and switches into chat, which is a natural drill-down path in `internal/webconsole/assets/app.js:312` and `internal/webconsole/assets/app.js:321`.
- Persisting `historyPage` helps preserve context when users bounce between history and chat in `internal/webconsole/assets/app.js:2322`.
- Rough edge: once a user opens a session from history, there is no obvious “back to history” affordance beyond the global nav, so the pagination context is preserved technically but not surfaced explicitly.

### 5. Destructive-action handling is safe, but feedback can be sharper

- Backend protections are sensible: clear-history rejects clearing when sessions are active or queue jobs are running in `internal/webconsole/service.go:508`.
- Delete and clear behaviors are therefore aligned with runtime safety, which fits the spec’s “facts first” control model.
- The frontend currently surfaces failures mostly as generic toasts, which is functional but light on context; conflict responses from clear/delete would benefit from preserving the backend reason text wherever available.

## High-confidence small bug

### History page can become logically out of sync after a failed page fetch

**Why it matters**

When the user clicks `Next` or `Prev`, `fetchHistory()` updates `state.historyPage` before the request succeeds. If the request fails and there is already prior `historyData`, the old list remains rendered, but the in-memory page state has already advanced. This creates a mismatch between the visible content and the logical current page, and can make the next pagination click behave unexpectedly.

**Relevant references**

- `internal/webconsole/assets/app.js:2215`
- `internal/webconsole/assets/app.js:2223`
- `internal/webconsole/assets/app.js:2234`
- `internal/webconsole/assets/app.js:283`

**Current behavior**

- User is on page 1 with visible page-1 results.
- User clicks `Next`.
- Request for page 2 fails.
- UI still shows page-1 results, but `state.historyPage` is now `2`.
- A subsequent `Next` or `Prev` action operates from the failed page state rather than the page the user is actually seeing.

**Suggested minimal fix**

In `fetchHistory()`, keep the previous page in a local variable and only commit `state.historyPage` after a successful response, or restore the previous page inside the `catch` branch when the request fails.

Example minimal approach:

- Save `const previousPage = state.historyPage` before assignment.
- Save `const requestedPage = Math.max(1, Number(page) || 1)`.
- Use `requestedPage` in the request URL.
- Set `state.historyPage = requestedPage` only after success, or revert to `previousPage` in `catch`.

## Overall assessment

The implementation is directionally solid: it respects the durable-session model, avoids obviously unsafe operations, and supports the primary chat/history workflows. The main issues are polish-level mismatches rather than architectural failures, with the history pagination error-state bug being the clearest small fix worth prioritizing.