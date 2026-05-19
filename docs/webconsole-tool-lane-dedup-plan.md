# WebConsole Tool Lane Dedup Plan

## Scope

This plan addresses the duplicated display observed in session `20260519-073510-326f48`.

The fix is limited to the Web-first console message rendering layer. It must not change the session facts written by the runtime, provider replay data, `messages.jsonl`, `events.jsonl`, or tool result persistence.

## Current Evidence

- Session facts are not duplicated on disk. `.go-cli-agent/sessions/20260519-073510-326f48/messages.jsonl` contains exactly three messages:
  - one user message: `hello`
  - one assistant message with one `finish` tool call
  - one tool message with one matching `finish` tool result
- The assistant `finish` call uses `tool_calls[0].id = call_y9BE7zr34pNQZaiH6GTyGzGs`.
- The tool result uses `tool_results[0].tool_call_id = call_y9BE7zr34pNQZaiH6GTyGzGs`.
- The duplicated UI is therefore a display composition issue: the frontend renders the assistant call as one message article, then renders the matching tool result as a separate "Tool lane" message article.

## Root Cause

`internal/webconsole/assets/session-view.js` renders each durable message independently:

- `renderMessageStream()` maps every message through `renderMessage(message)`.
- `renderToolLane(message)` pairs calls and results only when they are already on the same message object.
- The runtime correctly stores assistant tool calls and tool results as separate messages for replay compatibility, so the current renderer cannot pair the adjacent assistant call message with the following tool result message.

For a `finish` call this creates especially noisy output:

- the call preview displays the final message from `arguments.message`
- the call JSON body repeats the same final message
- the separate tool result article repeats the same final message in its preview and body

The user-facing answer is the final message, but the operator still needs access to the raw call/result facts for debugging and replay auditing.

## Product Constraints

- Keep `messages.jsonl` and `events.jsonl` as the source of truth.
- Do not delete or rewrite tool result messages.
- Do not hide tool calls, tool results, call ids, errors, or metadata without an explicit affordance to inspect them.
- Avoid duplicate visible output in the main chat stream.
- Keep the WebConsole conservative and session-first; this is not a new workflow surface.

## Proposed Rendering Contract

1. Build a display-only message stream before rendering.
2. When a `tool` role message contains results whose `tool_call_id` matches tool calls in the immediately preceding assistant display message, merge those results into that assistant display message for rendering.
3. Keep unmatched tool results as standalone tool-lane messages so unusual or orphaned facts remain visible.
4. Render a `finish` final result as one normal assistant final-response bubble when the message has no assistant text.
5. Keep the matching `finish` call/result facts in the same tool lane, but collapse the raw details by default and use neutral summaries such as "Final response captured" instead of repeating the final text in every preview.
6. Preserve full raw detail inside expandable `<details>` rows:
   - `finish` call arguments remain available in the call body.
   - `finish` result output remains available in the result body.
   - call id chips and final badges remain visible.

## Expected UI Result

For the reported session:

- The user message still renders once.
- The assistant completion renders as one assistant article.
- The visible final answer text appears once as the assistant final response.
- The tool lane appears once under that assistant article, with `1 call · 1 result`.
- The raw `finish` call/result can still be expanded for audit.
- There is no separate duplicate "Tool lane" article for the matching result.

## Validation Plan

- Run JS syntax checks for WebConsole assets.
- Run the focused embedded asset/service test.
- Run the repository default test script.
- Run the build.
- Restart the embedded WebConsole.
- Use a real browser to open session `20260519-073510-326f48` and assert:
  - exactly one `.tool-lane` appears in the chat body for that session
  - no standalone article header named `Tool lane` appears for the matching result
  - the final response text is visibly present exactly once outside expandable raw tool detail
  - the paired tool lane reports `1 call · 1 result`
- Verify the service is still reachable from Windows through the WSL-hosted WebConsole URL.

## Accuracy Review

- Reviewed against current session facts before implementation:
  - `jq -s 'length' .go-cli-agent/sessions/20260519-073510-326f48/messages.jsonl` returns `3`.
  - The message roles are `user`, `assistant`, and `tool`.
  - The assistant call id and tool result call id both resolve to `call_y9BE7zr34pNQZaiH6GTyGzGs`.
  - The `finish` call `arguments.message` and the `finish` result `display_output` are the same text.
- Reviewed against current renderer code before implementation:
  - `renderMessageStream()` renders each durable message independently via `stream.map((message) => renderMessage(message))`.
  - `renderToolLane(message)` only pairs calls/results already present on the same message object.
- Conclusion: the plan is accurate. The duplication is a frontend display composition bug, not a duplicated session-store fact.

## Implementation Record

- Implemented display-only stream composition in `internal/webconsole/assets/session-view.js`:
  - `buildDisplayMessageStream()` keeps durable messages unchanged but merges matching adjacent tool result messages into the preceding assistant display message.
  - `partitionMatchingToolResults()` pairs results by `tool_call_id` against both assistant call `id` and `provider_call_id`.
  - unmatched tool results still render as standalone tool-lane messages.
- Implemented `finish` display behavior:
  - `primaryFinalFinishResult()` renders the final response as one normal assistant bubble when the assistant message has no visible text.
  - matching `finish` call/result rows stay in the same tool lane.
  - default previews use `Final response captured` and the raw call/result rows are collapsed by default, but expanding them still shows the original call arguments and result output.
- Updated `spec/17-web-console.md` to document the display-only merge requirement and the "final message once" rendering contract.
- Added a focused asset regression assertion in `internal/webconsole/service_test.go`.
- Verified with a real browser against session `20260519-073510-326f48`:
  - message articles: `2`
  - tool lanes: `1`
  - standalone `Tool lane` articles: `0`
  - final response bubble matches: `1`
  - visible final-text occurrences outside raw expanded detail: `1`
  - raw call and result details still contain the final text when expanded
