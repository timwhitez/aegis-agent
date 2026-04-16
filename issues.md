# Current Issues

## Repair Status In Main Repo

Code/docs/test updates have now been applied in the main repo for the repo-owned items from this audit.

### Resolved in main repo

- Issue `2` fixed:
  - websocket `reset_session` no longer echoes a fake durable `0x...` session id back into the frontend state
  - the frontend now also guards against refreshing detail for ephemeral ids
- Issue `3` resolved as an explicit product contract:
  - the Workspace view is now labeled as a browser for the current server `cwd`
  - `/api/meta` now exposes `workspace_root` and `workspace_switch_supported=false`
  - README / `spec/17-web-console.md` now match that behavior
- Issue `4` fixed:
  - the webconsole font stack now includes explicit CJK-safe fallbacks and matching hosted font entries
- Issue `5` fixed:
  - `spec/04-tools-and-skills.md` now reflects the default-exposed `agent_spawn` / `agent_status` / `agent_list` tool surface
- Issue `6` fixed:
  - `clearHistory()` no longer kicks the operator out of History view
  - the browser smoke now asserts that clearing history keeps the History tab active

### Partially mitigated, still open upstream or by design

- Issue `1` is only partially mitigated locally:
  - the webconsole now distinguishes credential failures, retryable transport failures, rate limits, request-contract failures, and upstream `auth_unavailable` routing failures more clearly
  - history / overview summaries now carry durable `last_error` text instead of only phase labels
  - however, the actual late-run `auth_unavailable` failure still appears to be upstream/provider-side and is not fully fixable inside this repo alone
- Real workspace-root switching is still **not implemented** as a feature:
  - this repo now exposes that truth explicitly instead of implying a missing capability exists

## Scope

- Date: 2026-04-16
- Main workspace code was not used for destructive/live testing; execution ran in the isolated copy at `/tmp/go-cli-agent-human-ui-20260416-170609`
- Live webconsole under test: `http://127.0.0.1:3950`
- Provider shape used in the isolated run: `openai-compatible` + `wire_api=responses` + `base_url=http://64.186.236.156:24634/v1` + `model=gpt-5.4`
- The provided key was validated successfully before the run using `doctor` and `probe-provider`

## Execution Summary

This round was a real end-to-end run, not a dry read-only audit.

### 10-task matrix status

- `01-long-audit`: completed
- `02-tool-drift-fix`: completed
- `03-webconsole-interaction`: completed
- `04-multi-agent`: completed
- `05-background-queue`: completed
- `06-task-graph`: completed
- `07-awaiting-input`: reached awaiting-input semantics, then completed after a real continue
- `08-steer-interrupt`: failed after a real interrupt steer
- `09-tool-discipline`: completed
- `10-skill-loading`: failed mid-run

### Coverage actually exercised

Validated live in this round:

- chat-driven session start
- durable session creation
- `todo_write`
- `task_create`
- `task_list`
- `task_get`
- `task_update`
- `read_file`
- `grep`
- `grep_files`
- `glob`
- `shell`
- `write_file`
- `edit_file`
- `finish`
- `agent_spawn`
- `agent_status`
- `agent_list`
- background queue child execution
- awaiting-input followed by continue
- live interrupt steer submission from the frontend
- history pagination
- history refresh persistence
- workspace browsing and file reading for the current cwd

## Key Evidence

- Matrix summary: `/tmp/go-cli-agent-human-ui-20260416-170609/.artifacts/matrix-run-live/summary.json`
- Per-session snapshots: `/tmp/go-cli-agent-human-ui-20260416-170609/.artifacts/matrix-run-live/sessions/*.session.json`
- Live UI summary: `/tmp/go-cli-agent-human-ui-20260416-170609/.artifacts/ui-capture-live/ui-summary.json`
- Live history screenshot: `/tmp/go-cli-agent-human-ui-20260416-170609/.artifacts/ui-capture-live/02-history-page-1.png`
- Live history-after-refresh screenshot: `/tmp/go-cli-agent-human-ui-20260416-170609/.artifacts/ui-capture-live/04-history-after-refresh.png`
- Live workspace screenshot: `/tmp/go-cli-agent-human-ui-20260416-170609/.artifacts/ui-capture-live/06-workspace-view.png`
- Live task 1 chat screenshot showing Chinese tofu rendering: `/tmp/go-cli-agent-human-ui-20260416-170609/.artifacts/matrix-run-live/screenshots/01-long-audit-chat.png`
- Task 8 durable events: `/tmp/go-cli-agent-human-ui-20260416-170609/.sessions/20260416-101857-a58369/events.jsonl`
- Task 10 durable events: `/tmp/go-cli-agent-human-ui-20260416-170609/.sessions/20260416-102241-478639/events.jsonl`

## Validated Issues

### 1. Long-running live sessions can fail mid-run with upstream `auth_unavailable` even though probe/start works

- Severity: high
- Evidence:
  - Preflight `doctor` and `probe-provider` both succeeded with the provided live gateway/key.
  - Most tasks completed successfully in the same run using the same provider configuration.
  - Task `08-steer-interrupt` failed with:
    - `openai: {"error":{"message":"auth_unavailable: no auth available (providers=codex, model=gpt-5.4)","type":"server_error","code":"internal_server_error"}}`
  - Task `10-skill-loading` failed with the same terminal error.
  - Task `10` also recorded a `provider.retry` before failing, including an upstream stream disconnect and then the later `auth_unavailable` failure.
- What is validated:
  - This is not the old “invalid API key everywhere” problem.
  - The current symptom only appears during some longer/complex sessions after real work has already started.
- Likely scope:
  - This may be upstream/provider-side instability, but it is still a real product/runtime issue because the local session fails mid-task and the operator only sees a generic provider failure.
- Recommendation:
  - Treat this as a real reliability bug for long-horizon live runs.
  - Add clearer operator-facing differentiation between invalid credentials, retryable upstream disconnects, and upstream auth/provider-routing failures.

### 2. The frontend emits noisy console errors for ephemeral session ids during new-session resets

- Severity: medium
- Evidence:
  - The live matrix summary recorded repeated browser console errors like:
    - `session detail error Error: open .../.sessions/0x.../session.json: no such file or directory`
  - The errors occurred for draft-style ephemeral ids while the UI was rotating into a new session.
  - `internal/webconsole/assets/app.js:793` calls `refreshCurrentSession()` against `state.sessionId` once `state.sessionBacked` is true; the live behavior shows transient detail fetches still target draft ids in some reset windows.
- Impact:
  - This adds operator-visible console noise during normal chat usage.
  - It can hide real browser/runtime errors among harmless-but-buggy fetch failures.
- Recommendation:
  - Prevent detail fetches for ephemeral ids after `resetChatSession()` until a real backend session id has been adopted again.

### 3. The current frontend does not implement a real workspace-switch feature

- Severity: medium
- Evidence:
  - The live workspace screenshot `/tmp/go-cli-agent-human-ui-20260416-170609/.artifacts/ui-capture-live/06-workspace-view.png` shows only a file tree and preview pane.
  - There is no selector/input/button for switching workspace root.
  - `internal/webconsole/assets/app.js` loads workspace data through a fixed `requestJSON('/api/files?path=.')`.
  - `internal/webconsole/service.go` resolves `/api/files` relative to `os.Getwd()` and correctly rejects `..` escape.
- What works today:
  - Browsing the current server cwd works.
  - File reading for the current cwd works.
  - Parent escape is blocked correctly.
- What does not exist today:
  - No UI to switch workspace root.
  - No backend contract to change workspace context independently of process cwd.
  - No explicit “jump to selected session workdir” behavior in the workspace pane.
- Recommendation:
  - Either add a real workspace-root/session-workdir switch flow, or explicitly rename this surface to make clear it is only a current-cwd browser.

### 4. Chinese/CJK content still renders as tofu squares in the chat transcript

- Severity: high for Chinese-speaking operators
- Evidence:
  - `/tmp/go-cli-agent-human-ui-20260416-170609/.artifacts/matrix-run-live/screenshots/01-long-audit-chat.png` shows Chinese prompt/body text rendered as square tofu glyphs.
  - The webconsole font stack in `internal/webconsole/assets/index.html` and `internal/webconsole/assets/styles.css` still uses `Inter` / `JetBrains Mono` without explicit CJK fallback fonts.
- Recommendation:
  - Add CJK-safe font fallbacks for both the main text and mono stacks.
  - Re-test with Chinese prompts in chat, history, and session detail surfaces.

### 5. Main repo still contains an unmerged docs drift on default exposed tools

- Severity: medium
- Evidence:
  - In the isolated live run, task `02-tool-drift-fix` completed and patched `spec/04-tools-and-skills.md` to add the default-exposed `agent_spawn`, `agent_status`, and `agent_list` entries.
  - The main workspace was intentionally left untouched during testing, so that drift remains current here until manually merged.
- Why this matters:
  - Repo docs still under-describe the default tool surface versus runtime truth.
- Recommendation:
  - Review and merge the isolated fix from `/tmp/go-cli-agent-human-ui-20260416-170609/spec/04-tools-and-skills.md` if accepted.

### 6. Main repo still contains the old `clearHistory()` UX that kicks the operator out of History view

- Severity: medium
- Evidence:
  - In the isolated live run, task `03-webconsole-interaction` completed and removed the `clearHistory()` behavior that switched the UI back to chat after a successful clear.
  - The isolated diff shows the current main-repo behavior still does this jump, and the live run also added a focused smoke assertion for “history remains active after clear”.
  - The main workspace was intentionally left untouched during testing, so this UX issue remains current here until manually merged.
- Impact:
  - Clearing history breaks operator context and feels like the action navigates away unexpectedly.
- Recommendation:
  - Review and merge the isolated fix from `/tmp/go-cli-agent-human-ui-20260416-170609/internal/webconsole/assets/app.js` and the related smoke assertion in `/tmp/go-cli-agent-human-ui-20260416-170609/validation/scripts/webconsole_ui_smoke.mjs` if accepted.

## Important Non-Issues / Passed Checks

These behaviors were successfully validated in the live run and should not be reported as current gaps from this round:

- History pagination works across 13 durable sessions.
- Refresh preserves the current history page (`Page 2 / 2` remained active after refresh).
- Multi-agent works in practice:
  - parent created child sessions
  - child sessions completed
  - queue jobs existed and were tracked
  - background notifications flowed back to the parent
- Background queue execution works in practice.
- Awaiting-input plus continue works in practice.
- Interrupt steer was really sent from the frontend and reached the live session before the later provider failure.
- Workspace browsing and safe path restriction work for the current cwd.

## Notes On Result Completeness

- This round is materially stronger than the earlier blocked run:
  - 8 of the 10 supplied prompts completed successfully
  - the remaining 2 failures were late live-provider failures, not immediate local runtime refusal
- The isolated copy accumulated real task artifacts and a couple of candidate fixes; these were **not** merged into the main workspace by design.
- If we want to promote the successful isolated fixes, the next step is a small manual cherry-pick/review from the isolated copy rather than re-testing from scratch.
