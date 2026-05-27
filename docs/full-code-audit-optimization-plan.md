# Full Code Audit Optimization Plan

## Scope

This audit covers the current `go-cli-agent` repository state after commit `bbd7332`.

Required project boundaries from `AGENTS.md` and spec review:

- Start from the spec before code changes.
- Keep the Web-first v1 shape: Phase 0-10 runtime/provider/CLI foundation plus Phase 15 local Web Console.
- Keep core runtime, SDK facade, Web service adapter, and CLI adapter separated.
- Do not move provider-specific replay logic into Web, CLI, or tools.
- Keep session/state/messages/events/goal/plan/queue files as the durable facts.
- Make small code fixes with focused validation and a real git commit per coherent slice.

Spec files read for this pass:

- `spec/00-product.md`
- `spec/01-runtime-architecture.md`
- `spec/03-provider-contracts.md`
- `spec/09-phase-plan.md`
- `spec/11-spec-audit-and-traceability.md`
- `spec/12-task-system.md`
- `spec/13-live-input-and-steering.md`
- `spec/17-web-console.md`
- `README.md`

## Current Baseline

Git state at audit start:

- HEAD: `bbd7332 fix(webconsole): harden markdown cache key, focus ring, code overflow`
- Untracked before this audit: `CLAUDE.md`, `docs/webconsole-frontend-optimization-plan.md`, `workspace/`
- This plan is a new audit artifact and does not rely on the older untracked frontend-only plan as current truth.

Initial validation started:

- `node --check internal/webconsole/assets/*.js`: passed
- `gofmt -l cmd internal pkg validation/cmd`: no output
- `go test ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: started during plan drafting; the turn was interrupted before a final result was captured. On resume, no matching `go test` process remained.

## Confirmed Issues

### FCA-20260522-001: Plan Mode multi-question input fabricates unselected answers

Severity: High

Evidence:

- `internal/webconsole/assets/session-view.js` renders each pending Plan Mode question with per-option buttons.
- `internal/webconsole/assets/app.js` `handlePlanInputAction` submits immediately after one option click.
- `internal/webconsole/assets/app.js` `collectPlanInputAnswers` maps every request question and fills unanswered questions from the first option or `"Default"`.
- `internal/session/planmode.go` `ValidatePlanModeAnswers` requires one answer per request question.
- `spec/17-web-console.md` documents `/planmode/input` as accepting explicit `{ request_id, answers }`, not inferred defaults.

Impact:

For a `request_user_input` payload with multiple questions, clicking one option in Web Console silently sends answers for every question. Unanswered questions get the first listed option or a synthetic default. The backend accepts the answer count and resumes the runner, so the operator loses the chance to answer remaining questions and the agent can execute under constraints the user did not choose.

Minimal fix:

- Track selected answers per `request_id` and `question_id` in frontend state.
- Render options as selection controls instead of immediate submit.
- Add one submit button for the whole pending request.
- Disable submit until every question has an explicit answer.
- Remove default-answer synthesis from `collectPlanInputAnswers`.
- Add a focused Node validation test for multi-question behavior.

Validation:

- Frontend unit test for `collectPlanInputAnswers`.
- `node validation/scripts/webconsole_utils_test.mjs`
- `node --check internal/webconsole/assets/*.js`
- Relevant WebConsole Go tests if backend contract is touched.

### FCA-20260522-002: Plan Mode fallback continue goroutine is not tracked by Service.Close

Severity: Medium

Evidence:

- `internal/webconsole/service.go` `startSession` and `handleContinueSession` use `trackLaunch`, which increments `launchWG`.
- `Service.Close` cancels handles and waits on `launchWG`.
- `launchPlanModeContinue` creates a handle but starts a bare goroutine with `go func() { ... }()`.
- `handlePlanModeApprove`, `handlePlanModeRevise`, `handlePlanModeInput` fallback, and mission-plan approval all call `launchPlanModeContinue`.

Impact:

If the Web service is closed while a Plan Mode approve/revise/input fallback continue is running, `Service.Close` can return before that runner settles. This weakens the local Web Console lifecycle contract and can leave background session writes racing after the service has been considered closed.

Minimal fix:

- Replace the bare goroutine in `launchPlanModeContinue` with `s.trackLaunch`.
- Add a focused service test that proves `Close` waits for the Plan Mode continue path to finish or releases the handle before returning.

Validation:

- Focused `go test ./internal/webconsole/ -run PlanMode`
- Full `go test ./internal/webconsole/`
- Full package test gate before commit if runtime/service behavior changes.

### FCA-20260522-003: Web JSON decoder accepts trailing JSON values

Severity: Medium

Evidence:

- `internal/webconsole/service.go` `decodeJSON` uses `json.Decoder` with `DisallowUnknownFields`, but returns after the first successful `Decode` without checking for additional tokens.
- All Web mutation endpoints using `decodeJSON` therefore parse the first JSON value and ignore a second concatenated JSON value in the same request body.

Impact:

Malformed or ambiguous API requests can be accepted based only on their first JSON object. This weakens the strict local Web Console API contract and can hide client-side serialization bugs during operator workflows.

Minimal fix:

- After decoding the expected request object, perform a second decode and require `io.EOF`.
- Add a focused Web Console test that verifies `/api/sessions/start` rejects a valid object followed by another JSON value.

Validation:

- Focused decoder regression test.
- Existing unknown-field decoder regression test.
- Full `go test ./internal/webconsole/` before commit.

### FCA-20260522-004: Skill command tool argument decoder accepts trailing JSON values

Severity: Medium

Evidence:

- `internal/tools/registry.go` `decodeCommandToolArgs` uses `json.Decoder` with `UseNumber`, but returns after the first successful `Decode` without checking for additional JSON values.
- Skill command tools receive provider-emitted raw arguments through this decoder before schema validation and command execution.

Impact:

A command tool call can include a valid first JSON object followed by hidden trailing JSON. The command runs with the first object while the trailing value is ignored, which weakens provider/tool protocol integrity and can mask malformed tool-call arguments.

Minimal fix:

- After decoding command-tool arguments, perform a second decode and require `io.EOF`.
- Add a focused skill command tool regression test with a valid first object followed by a second object.

Validation:

- Focused skill command trailing-JSON regression test.
- Existing closed-schema command-tool regression test.
- Full `go test ./internal/tools/` before commit.

### FCA-20260522-005: Plan Mode input detail polling drops live runner handles

Severity: High

Evidence:

- `request_user_input` stores Plan Mode state as `awaiting_input` / `plan_input` while the live runner blocks on the Web responder.
- `sessionDetail` calls `activeHandleOwner`, which calls `pruneInactiveHandles`.
- `pruneInactiveHandles` deleted current-process handles whenever persisted session state was not `running`.
- A normal `GET /api/sessions/{id}` while the browser is polling a pending Plan Mode input could therefore delete the only in-memory runner handle that owns the waiting `request_user_input` call.

Impact:

After the handle was pruned, `/planmode/input` no longer answered the live waiter. The handler could fall back to launching a continue path while the original runner remained blocked on the unanswered request, which weakened Web Console lifecycle tracking and could leave `Service.Close` waiting on a stale runner.

Minimal fix:

- Treat current-process handles as prunable only when the durable state is terminal or unreadable.
- Keep `awaiting_input`, `paused`, and other non-terminal handles until `finishHandle` releases them.
- Add a regression test that starts a Plan Mode session, waits for `request_user_input`, reads session detail, answers the pending input, and verifies the original runner advances to the normal plan approval gate.

Validation:

- Focused live Plan Mode input/detail regression test.
- Adjacent Plan Mode service tests.
- Full `go test ./internal/webconsole/` before commit.

### FCA-20260522-006: Plan Mode input API accepts invalid live answers before validation

Severity: Medium

Evidence:

- `handlePlanModeInput` only required a non-empty `answers` array before checking the current live handle.
- `AnswerActivePlanInput` sends the answers directly to the blocked runner by `session_id` and `request_id` without validating them against the stored pending request.
- `ValidatePlanModeAnswers` ran later inside the `request_user_input` tool path or fallback continue path, after the HTTP endpoint had already returned `202 Accepted`.

Impact:

Malformed Web input such as a missing `request_id` or an unknown question id could be accepted as if the operator had answered the pending Plan Mode prompt. On the live path this unblocked the waiting runner and turned a client/API error into a model-visible tool error, so the browser lost the chance to correct the same pending request before execution continued.

Minimal fix:

- Require `request_id` in `/planmode/input`.
- Load the stored pending Plan Mode request before answering a live runner or launching fallback continue.
- Reject missing pending requests, request-id mismatches, and invalid answers at the HTTP boundary.
- Add a live-runner regression that verifies invalid input is rejected before delivery and a later valid answer still reaches the original waiter.

Validation:

- Focused live Plan Mode input validation regression test.
- Adjacent Plan Mode service tests.
- Full `go test ./internal/webconsole/` before commit.

### FCA-20260522-007: Same-session continue can race before durable running state

Severity: High

Evidence:

- `Continue` loaded a resumable `state.json` and appended continuation messages before it durably wrote `status=running`.
- `acquireRunSlot` allowed same-session nested acquisition so internal auto-continue can re-enter the same runner.
- That runner-local slot does not cover another `Runner` or process using the same session store.
- The first durable `running` write happened later in the engine loop, after preparation work such as hooks and message append.

Impact:

Two same-session `continue` calls in the pre-engine window could both observe `awaiting_input`, append separate user messages, call the provider, execute tools, and race final `state.json`. This violates the session/messages/events file-fact contract and can make Web/CLI overlap or fast double-submit corrupt a single session timeline.

Minimal fix:

- Add a store-level run claim that uses the existing session file-lock pattern to atomically verify a resumable status and write `status=running`.
- Call the claim in `Continue` before metadata save, Plan Mode mutation, checkpoint injection, user-message append, or provider execution.
- Keep runner-local same-session nested acquisition available for internal auto-continue, but rely on the durable state transition to reject separate concurrent continues.
- Add a regression with two runners sharing one store root where the first blocks in a user-message hook and the second same-session continue must be rejected without appending a message or calling the provider.

Validation:

- Focused same-session continue race regression.
- Adjacent runner continue/auto-continue tests.
- Full runtime and session package tests before commit.

### FCA-20260522-008: Budget-limited goal wrap-up can still complete the session

Severity: Medium

Evidence:

- `spec/18-durable-contract-and-completion.md` states that `budget_limited` with `stop_on_budget=true` must return to `awaiting_input` after the wrap-up opportunity unless the model legitimately completes the goal with `update_goal(status="complete")`.
- `goalCompletionGate` blocked `finish` only until a `budget_wrapup` record existed.
- After `record_goal_progress(kind="budget_wrapup")`, a same-turn `finish` could pass the controller and `Engine.complete` would write `state.status=completed`.
- `goalBudgetWrapUpPrompt` also told the model to finish after recording wrap-up facts when the goal was not complete.

Impact:

A budget-exhausted incomplete goal could be rendered as a completed session. That makes Web/CLI summaries, parent coordination, and recovery prompts treat a budget stop as task completion even though the goal completion audit never passed.

Minimal fix:

- Keep blocking `finish` for `budget_limited + stop_on_budget` even after a wrap-up record exists.
- Preserve the first gate that tells the model to record `budget_wrapup` when it has not done so yet.
- Update runtime prompt text so the model records wrap-up facts and then stops instead of calling `finish`.
- Add a controller regression and an engine regression for `record_goal_progress(kind="budget_wrapup")` followed by `finish` in the same turn.

Validation:

- Focused completion-controller and engine budget-wrap-up tests.
- Full runtime package tests before commit.

### FCA-20260522-009: Parent coordination updates can lose concurrent terminal jobs

Severity: Medium

Evidence:

- Parent coordination add/resolve helpers loaded `parent-coordination.json`, mutated in memory, then saved the full snapshot.
- `LoadParentCoordination` was an unlocked read and `SaveParentCoordination` only serialized the final write.
- Queue workers can complete terminal jobs for the same parent concurrently and each call `resolveParentQueueJob`.
- The completion controller trusts `unresolved_*`, `completed_*`, and `failed_*` lists when deciding whether a parent can finish.

Impact:

Two concurrent child/queue completions could read the same old coordination snapshot and then overwrite each other's terminal updates. A parent could remain blocked on work that completed, lose completed/failed evidence, or report inaccurate coordination facts.

Minimal fix:

- Add a store-level `MutateParentCoordination` helper that locks a session-scoped coordination lock across load, merge, and atomic write.
- Use the mutation helper in parent child/job add and resolve paths.
- Use the same helper in queue reconciliation so persisted queue status repairs do not use a stale coordination snapshot.
- Add a concurrent queue-resolution regression that verifies all completed job IDs are retained and unresolved jobs are cleared.

Validation:

- Focused parent coordination concurrency regression.
- Existing parent coordination gate/event tests.
- Full runtime and session package tests before commit.

### FCA-20260522-010: Icon-only send button lacks an accessible name

Severity: Low

Evidence:

- `spec/17-web-console.md` requires icon-only buttons to have `aria-label`.
- The composer send button in `internal/webconsole/assets/index.html` rendered only the send icon and had no text, `aria-label`, or `title`.

Impact:

Assistive technologies could expose the main composer send action as an unnamed button, making the default Web-first workflow harder to operate.

Minimal fix:

- Add `type="button"`, `aria-label="Send message"`, and `title="Send message"` to the send button.
- Extend the embedded shell asset test to assert the accessible name contract.

Validation:

- Focused embedded WebConsole asset test.
- Full WebConsole package test before commit.

### FCA-20260522-011: Initial Web session launch lacks a pending-submit guard

Severity: Medium

Evidence:

- `sendMessage` set `isGenerating` for initial launch, but before a durable session id existed the send path could still enter the new-session branch.
- `updateUI` disabled the send button only when the draft was empty, so typing a second prompt while `/api/sessions/start` was pending could re-enable the button.
- The steer path is guarded by `hasDurableSession()`, so an in-flight launch without a session id was not treated as a running session.

Impact:

A fast follow-up submit during initial launch could start a second unrelated session instead of waiting for the first launch to adopt its durable session id. That weakens the Web-first default workflow and can produce confusing stale optimistic messages or duplicate sessions.

Minimal fix:

- Add an explicit `launchInFlight` state flag.
- Reject sends while launch is pending and no durable session id exists.
- Disable the send button and show busy styling during that pending initial launch.
- Clear the pending flag on successful adoption, start failure, and new-session reset.

Validation:

- JS syntax check for the WebConsole app bundle.
- Embedded shell asset contract test.
- Frontend utility tests and full WebConsole package test before commit.

### FCA-20260522-012: Risky Web actions miss explicit confirmation

Severity: Medium

Evidence:

- `spec/17-web-console.md` says risky actions such as writing config/API keys, deleting/clearing, and skill install/uninstall require explicit confirmation.
- Settings save wrote provider config and could write an API key without a confirmation.
- Goal clear deleted durable goal state without a confirmation.
- Skill uninstall removed a local skill directory without a confirmation.

Impact:

Local WebConsole users could accidentally persist config/API key changes or remove durable local state with a single click, which conflicts with the Web-first local-console safety contract.

Minimal fix:

- Add explicit Settings save confirmation, with API-key-specific wording when a new key will be written.
- Add explicit Goal clear confirmation.
- Add explicit Skill uninstall confirmation.
- Extend embedded asset tests to assert those confirmation hooks remain wired.

Validation:

- JS syntax checks for `app.js` and `settings-view.js`.
- Embedded shell asset contract test.
- Frontend utility tests and full WebConsole package test before commit.

### FCA-20260522-013: Background notification updates can drop concurrent child results

Severity: High

Evidence:

- `internal/runtime/engine.go` `drainBackground` loads `control/background.jsonl`, appends a provider-visible `<background-agent-results>` user message, marks pending notifications accepted in memory, then calls `Store.UpdateBackgroundNotifications`.
- `internal/session/store.go` `UpdateBackgroundNotifications` rewrote the entire `control/background.jsonl` file from that stale in-memory snapshot.
- `internal/session/store.go` `AppendBackgroundNotification` and `EnsureBackgroundNotification` appended/read-deduped without the `control/background.lock` protection used by steer requests.
- `spec/01-runtime-architecture.md` and `spec/17-web-console.md` treat background notifications as durable session facts, and `spec/17-web-console.md` explicitly calls out queue completion versus stale-running reconcile deduplication by `queue_job_id`.

Impact:

If a parent run drains and accepts one pending background result while a queue worker or reconciliation path appends another completed child notification, the stale whole-file update can overwrite the newly appended notification. The parent can then miss a completed child result, weakening completion gates, WebConsole background visibility, and provider replay traceability.

Minimal fix:

- Serialize background notification append, ensure, and update operations through `control/background.lock`.
- Merge `UpdateBackgroundNotifications` against the current file before writing, replacing matching notifications by `queue_job_id` or notification id while preserving newly appended notifications.
- Add focused store regressions for stale-snapshot merge and symlinked background lock rejection.

Validation:

- Focused session store regression for stale `UpdateBackgroundNotifications` snapshots.
- Focused runtime background-result drain test.
- Full `go test ./internal/session/ ./internal/runtime/`.
- Repository-wide Go tests and vet before commit.

### FCA-20260522-014: Anthropic-compatible probes omit default prompt cache markers

Severity: Medium

Evidence:

- `spec/03-provider-contracts.md` says `anthropic-compatible` profile `prompt_cache` defaults on, and cache markers are constructed only in the Anthropic adapter.
- Real sessions persist that default through `providerOptionsFromConfig` into `SessionMetadata.ProviderOptions.PromptCache`.
- Runtime turns pass `meta.ProviderOptions.PromptCache` into `provider.TurnRequest`.
- `internal/runtime/runner.go` `Runner.Probe` built its `provider.TurnRequest` with generation, reasoning, thinking, API provider, and store options, but omitted `PromptCache`.
- `internal/provider/anthropic.go` gates `cache_control` markers on `promptCacheEnabled(req.PromptCache)`, where nil means disabled.
- Web Settings config-test and CLI/doctor provider probes go through `Runner.Probe`, so they inherited the mismatch.

Impact:

`probe-provider`, `doctor`, and Web Settings "test config" could pass against an Anthropic-compatible gateway without sending the cache markers that normal sessions send by default. A gateway that rejects or mishandles Anthropic cache markers could therefore look healthy in diagnostics but fail or behave differently during real session execution.

Minimal fix:

- Pass `defaultPromptCacheForAPIProvider(apiProvider, providerCfg.PromptCache)` into the `Runner.Probe` provider request.
- Keep provider-specific `cache_control` construction inside the Anthropic adapter.
- Add focused runtime probe tests for default-on and explicit `prompt_cache=false`.
- Add a Web config-test regression proving the endpoint uses the same default Anthropic-compatible cache-marker request shape.

Validation:

- Focused runtime probe tests for Anthropic-compatible prompt cache parity.
- Focused Web Settings config-test probe test.
- Existing Anthropic adapter cache-marker test.
- Repository-wide Go tests and vet before commit.

### FCA-20260525-015: Web workspace meta advertises unsupported root switching

Severity: Low

Evidence:

- `spec/17-web-console.md` says the current Workspace panel is a read-only browser for the server workspace and does not promise independent workspace-root switching.
- `internal/webconsole/service.go` returned `workspace_switch_supported: true` from `/api/meta`.
- `internal/webconsole/assets/workspace-view.js` used that flag to display "switch roots when needed."
- `internal/webconsole/service_test.go` asserted the stale true value.

Impact:

The default Web Console told operators that root switching was supported even though the current product boundary only supports browsing within the local server workspace surface. This is a small but visible contract drift in the Web-first default UI.

Minimal fix:

- Return `workspace_switch_supported: false` from Web meta.
- Keep the existing file browser behavior, but make the UI copy say root switching is not available.
- Update the service regression test to lock the contract.

Validation:

- Focused WebConsole meta test.
- Frontend JS syntax check for `workspace-view.js`.
- Full WebConsole package test before commit.

### FCA-20260525-016: Relative path helpers reject legitimate dot-prefixed child paths

Severity: Low

Evidence:

- `internal/session/store.go` `relativePathWithinRoot` rejected any relative path with `strings.HasPrefix(rel, "..")`.
- `internal/tools/registry.go` `relativeOrAbsolute` used the same prefix check for tool display paths.
- A legitimate child path such as `..reports/child-two.md` is inside the workspace but starts with two dots, so it was treated like `../outside`.

Impact:

Queue visible output collection could drop legitimate write/edit artifacts under dot-prefixed directories, which weakens child/queue handoff visibility and parent output sync. Tool display output could also show unnecessarily absolute paths for the same valid in-workspace path shape.

Minimal fix:

- Use separator-aware traversal checks: reject only `..`, `../...`, platform-equivalent parent traversal, or absolute relative results.
- Add focused session and tools regressions for `..reports/output.md`.
- Preserve existing outside-root rejection behavior.

Validation:

- Focused session queue visible-path regression.
- Focused tools relative-display regression.
- Full session/tools package tests before commit.

### FCA-20260525-017: Google prompt-level safety blocks become generic provider errors

Severity: Medium

Evidence:

- `spec/03-provider-contracts.md` maps Google safety blocking to internal stop reason `blocked`.
- `internal/provider/google.go` only maps candidate-level `finishReason == "SAFETY"` to `blocked`.
- The same adapter returns `google: empty candidates` whenever `candidates` is empty, before checking prompt-level safety facts.
- Google prompt-level safety blocks can be represented without a candidate, so the runtime sees a generic provider error instead of a provider stop reason it can record as `provider_blocked`.

Impact:

Prompt-level Gemini safety blocking is handled as an adapter failure rather than a normalized blocked stop reason. That loses provider-specific stop facts, bypasses the runtime's provider stop-reason handling, and weakens session recovery/diagnostics for a known Google response shape.

Minimal fix:

- Parse Google `promptFeedback.blockReason`.
- When no candidates are present and `blockReason` is non-empty, return a `TurnResult` with `StopReason: "blocked"` and raw provider stop metadata instead of an error.
- Preserve the existing generic error for genuinely empty candidate responses without a safety block.
- Add a focused Google adapter regression for prompt-level safety blocking.

Validation:

- Focused Google adapter prompt-block regression.
- Full provider package test before commit.

### FCA-20260525-018: OpenAI malformed tool arguments can break session persistence

Severity: Medium

Evidence:

- OpenAI Responses returns `function_call.arguments` as a JSON-encoded string.
- `internal/provider/openai.go` stored that string directly as `json.RawMessage` without checking whether it was valid JSON.
- `internal/runtime/engine.go` persists returned tool calls into assistant `messages.jsonl` before dispatching tools.
- `encoding/json` rejects invalid `json.RawMessage` during message encoding, so a malformed provider `arguments` string fails at session append time instead of being classified as a provider response parse error.

Impact:

An upstream or compatible OpenAI Responses gateway that returns malformed function-call arguments can turn a provider response-shape problem into a session-store append failure after the runtime has already recorded provider success. That weakens provider error classification, provider-attempt diagnostics, and durable replay consistency for a known provider-specific response field.

Minimal fix:

- Validate OpenAI `function_call.arguments` in the OpenAI adapter before constructing `ToolCall`.
- Return a `response_parse_error` provider error when arguments are empty or invalid JSON.
- Preserve normal valid argument behavior, including trimming surrounding whitespace before persistence/replay.
- Add focused adapter coverage and a runtime regression proving provider parse errors fail before assistant-message persistence.

Validation:

- Focused OpenAI invalid function-call argument regression.
- Focused runtime provider parse-error persistence regression.
- Full provider/runtime package tests before commit.

### FCA-20260525-019: OpenAI non-completed statuses are treated as normal done candidates

Severity: Medium

Evidence:

- `spec/03-provider-contracts.md` maps OpenAI `status=completed` with no tools to internal `done_candidate`, `incomplete_details.reason=max_output_tokens` to `max_tokens`, and provider/adapter errors to `error`.
- `internal/provider/openai.go` initialized `stopReason := "done_candidate"` and only changed it for tool calls, max-token incomplete details, or `status == "completed"`.
- Therefore a response such as `status="failed"` with no tool calls and no max-token incomplete reason returned `StopReason: "done_candidate"`.
- `internal/runtime/engine.go` only treats `max_tokens`, `blocked`, and `error` as provider stop failures; `done_candidate` in run mode becomes `awaiting_input`, and in exec/init mode can enter the normal finish-required loop.

Impact:

OpenAI Responses or compatible gateways can report a non-completed terminal status while the harness treats the turn as a normal assistant candidate. That hides provider failure facts from the runtime's provider stop handling, weakens recovery diagnostics, and can leave a session awaiting input or finish instead of being marked as a resumable provider failure.

Minimal fix:

- Map non-empty OpenAI statuses other than `completed` and recognized max-token incomplete responses to internal `StopReason: "error"` when no tool calls are present.
- Preserve the raw provider `status` in `RawProvider` for diagnostics.
- Add a focused OpenAI adapter regression for `status="failed"`.
- Reuse the runtime provider stop-reason regression to verify `error` remains resumable.

Validation:

- Focused OpenAI non-completed status regression.
- Focused runtime provider stop-reason regression.
- Full provider/runtime package tests before commit.

### FCA-20260525-020: Google unknown finish reasons are treated as normal done candidates

Severity: Medium

Evidence:

- `spec/03-provider-contracts.md` maps Google `finishReason=STOP` to `done_candidate`, `finishReason=MAX_TOKENS` to `max_tokens`, and safety blocking to `blocked`.
- `internal/provider/google.go` initialized `stopReason := "done_candidate"` and only changed it for tool calls, `MAX_TOKENS`, or `SAFETY`.
- Therefore a non-tool response with a different non-empty finish reason, such as `RECITATION`, returned `StopReason: "done_candidate"`.
- `internal/runtime/engine.go` treats `done_candidate` as a normal assistant turn, while provider stop failures are driven by `max_tokens`, `blocked`, and `error`.

Impact:

Gemini finish reasons outside the explicitly supported normal/limit/safety set can be misclassified as ordinary completion. That hides the provider stop condition from runtime recovery and diagnostics, and can leave a session awaiting user input or finish instead of marking a resumable provider stop failure.

Minimal fix:

- Map Google `finishReason=STOP` explicitly to `done_candidate`.
- Map non-empty unrecognized Google finish reasons to internal `StopReason: "error"`.
- Preserve raw Google finish reason metadata for diagnostics.
- Add a focused Google adapter regression for an unknown finish reason.

Validation:

- Focused Google unknown-finish regression.
- Focused runtime provider stop-reason regression.
- Full provider/runtime package tests before commit.

### FCA-20260525-021: Anthropic unknown stop reasons are treated as normal done candidates

Severity: Medium

Evidence:

- `spec/03-provider-contracts.md` maps Anthropic `end_turn` to internal `done_candidate`, `max_tokens` to `max_tokens`, and `pause_turn` to `error`.
- `internal/provider/anthropic.go` initialized `stopReason := "done_candidate"` and only changed it for `tool_use`, `max_tokens`, or `pause_turn`.
- Therefore a non-tool response with a different non-empty `stop_reason`, such as `refusal`, returned `StopReason: "done_candidate"`.
- `internal/runtime/engine.go` treats `done_candidate` as a normal assistant turn, while provider stop failures are driven by `max_tokens`, `blocked`, and `error`.

Impact:

Anthropic-compatible gateways can return an unexpected terminal stop reason while the harness treats the turn as ordinary completion. That loses provider stop facts at the runtime boundary and can leave a session awaiting input or finish instead of being marked as a resumable provider failure with the raw stop reason preserved.

Minimal fix:

- Map Anthropic `stop_reason=end_turn` explicitly to `done_candidate`.
- Map non-empty unrecognized Anthropic stop reasons to internal `StopReason: "error"`.
- Preserve raw Anthropic stop reason metadata for diagnostics.
- Add a focused Anthropic adapter regression for an unknown stop reason.

Validation:

- Focused Anthropic unknown-stop regression.
- Focused runtime provider stop-reason regression.
- Full provider/runtime package tests before commit.

### FCA-20260525-022: Provider tool-call arguments can be non-object JSON

Severity: Medium

Evidence:

- `spec/03-provider-contracts.md` defines provider tools through object `input_schema` contracts and keeps provider-specific response parsing inside adapters.
- `internal/tools/registry.go` executes tools with `json.RawMessage` arguments, and skill command tools explicitly reject non-object JSON through `decodeCommandToolArgs`.
- `internal/provider/openai.go` validates that `function_call.arguments` is syntactically valid JSON, but it accepts valid non-object values such as `null`, arrays, strings, or numbers.
- `internal/provider/anthropic.go` copies `tool_use.input` directly into both `ProviderContentBlock.Input` and `ToolCall.Arguments` without checking that it is a JSON object.
- `internal/provider/google.go` copies `functionCall.args` directly into both `ProviderContentBlock.Args` and `ToolCall.Arguments` without checking that it is a JSON object.
- `internal/runtime/engine.go` records provider success and persists assistant tool calls before dispatching the tool arguments to tool handlers.

Impact:

An upstream or compatible provider can return a syntactically valid but non-object tool-call payload and have the runtime record it as a successful assistant tool call. That weakens provider/tool protocol integrity, can shift provider response-shape failures into later generic tool errors, and can persist replay facts that do not satisfy the harness tool schema contract.

Minimal fix:

- Add a shared provider-adapter helper that trims, validates, and preserves JSON object tool-call arguments.
- Use it for OpenAI `function_call.arguments`, Anthropic `tool_use.input`, and Google `functionCall.args`.
- Return `response_parse_error` from the owning provider adapter when a tool-call argument payload is empty, malformed, or not a JSON object.
- Add focused adapter regressions for OpenAI, Anthropic, and Google non-object tool arguments.

Validation:

- Focused provider adapter non-object argument regressions.
- Full provider package test before commit.

### FCA-20260525-023: Web message history can hide a middle paging gap

Severity: Medium

Evidence:

- `spec/17-web-console.md` requires the Session workspace to load earlier messages through `GET /api/sessions/{id}/messages?before_id=...&limit=...` when session detail returns only a tail window, and says frontend-loaded history must not be lost during polling.
- `internal/webconsole/service.go` correctly returns bounded tail windows in `sessionDetail` and older pages from `handleSessionMessages`.
- `internal/webconsole/assets/app.js` preserved previously loaded messages by prepending messages not present in the newest server tail.
- The same frontend set `state.hasMoreMessages = state.loadedAllEarlierMessages ? false : detail?.has_more_messages === true` after polling.
- If the user loaded all earlier history, then the session appended more than one tail window of new messages before the next poll, the newest detail tail no longer overlapped the preserved old messages. The frontend produced an old-history + new-tail stream with a missing middle page, but still hid the "Load earlier messages" control because `loadedAllEarlierMessages` remained true.

Impact:

Long-running Web sessions can silently omit a middle range of messages after the user has previously loaded all history. That violates the documented WebConsole pagination contract, weakens auditability of durable `messages.jsonl`, and can make the UI present a discontinuous conversation without a way to fetch the missing page.

Minimal fix:

- Move message-window merge logic into pure frontend helpers.
- Detect when the refreshed server tail no longer overlaps preserved loaded history and keep a `messageGapAnchorId`.
- Show the load-earlier control for known gaps even if all earlier history had previously been loaded.
- Insert fetched gap pages immediately before their anchor, deduping already loaded messages.
- Add Node unit coverage for overlapping tails, non-overlap gap detection, and anchor-based middle-page insertion.

Validation:

- Frontend syntax checks for changed assets.
- Node utility regression tests for message merge helpers.
- Focused WebConsole service/session message paging tests.

### FCA-20260525-024: Web JSON mutation guard accepts non-JSON media subtypes

Severity: Low

Evidence:

- `spec/17-web-console.md` requires unsafe JSON mutation endpoints to use `Content-Type: application/json`.
- `internal/webconsole/service.go` routes all `/api/` requests through `guardUnsafeAPIRequest` before dispatch.
- `guardUnsafeAPIRequest` checked JSON mutation content types with `strings.HasPrefix(contentType, "application/json")`.
- That prefix check accepts distinct media types such as `application/json-patch+json` even though WebConsole JSON mutation handlers all decode the same strict JSON request object contract.
- Existing guard regressions covered foreign origins and oversized JSON bodies, but not a JSON-looking subtype content type.

Impact:

The local console mutation guard was looser than the documented API contract. A malformed or wrong client could send a different JSON-family media type and still reach mutation handlers, weakening boundary diagnostics for Web-first control APIs.

Minimal fix:

- Parse the media type with `mime.ParseMediaType`.
- Accept only an exact, case-insensitive `application/json` media type, while still allowing valid parameters such as `charset=utf-8`.
- Add a focused WebConsole regression that rejects `application/json-patch+json` for a JSON mutation route.

Validation:

- Focused WebConsole mutation guard regression.
- Full WebConsole package test before commit.

### FCA-20260525-025: Web start lifecycle has an untracked pre-session window

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` describes the Web service adapter as owning asynchronous start / continue handles, and `Service.Close` cancels active handles then waits on `launchWG`.
- `internal/webconsole/service.go` `handleContinueSession` and `launchPlanModeContinue` create a handle before starting their tracked goroutine.
- `internal/webconsole/service.go` `startSession` started `runner.Start` in a bare goroutine, then waited for `session.created` / `session.started` before adding a handle and before attaching the goroutine to `launchWG`.
- If the Web service closed while `runner.Start` was still before a session id becoming observable, `Service.Close` had no handle to cancel and no wait-group entry to wait for.
- The prior lifecycle regression covered Plan Mode continue tracking but not this pre-session-id start window.

Impact:

`Service.Close` could return while an initial Web start goroutine was still running before the session id was known to the Web adapter. That weakened the local Web service lifecycle contract and could leave provider/session work racing after the service was considered closed.

Minimal fix:

- Add a small pending-start registry for starts that have a cancellation function but not yet a session id.
- Track the initial `runner.Start` goroutine in `launchWG` immediately.
- Make `Close` mark the service closed, cancel pending starts, cancel visible handles, and wait for all tracked launches.
- Promote a pending start to a normal session handle once the session id is observed, rejecting promotion if the service is already closing.
- Add focused lifecycle coverage for pending-start close behavior and keep adjacent handle-owner / Plan Mode continue lifecycle tests passing.

Validation:

- Focused WebConsole lifecycle regressions.
- Full WebConsole package test before commit.

### FCA-20260525-026: Built-in tool execution ignores closed input schemas

Severity: Medium

Evidence:

- `spec/04-tools-and-skills.md` requires every tool to expose an `input_schema`, and `spec/03-provider-contracts.md` describes provider tool contracts as object schemas transformed by provider adapters.
- `internal/tools/registry.go` closes built-in tool schemas with `closeObjectSchemas`, and `internal/tools/registry_test.go` `TestBuiltinToolSchemasDisallowUnknownProperties` verifies nested object schemas have `additionalProperties=false`.
- Built-in tool handlers decode raw arguments with ordinary `json.Unmarshal` into structs, which ignores unknown object fields.
- Before this slice, `Registry.Execute` routed built-in raw arguments directly to handlers without checking the closed schema, while skill command tools already had strict argument decoding and closed-schema validation.

Impact:

Provider-emitted built-in tool calls could include unknown top-level or nested object fields and still execute based on a partial struct decode. That made the registry-published closed schema stricter than actual execution and weakened the provider/tool protocol boundary.

Minimal fix:

- Validate built-in tool arguments at `Registry.Execute` before calling the handler.
- Require built-in arguments to be a single JSON object and reject concatenated trailing JSON values.
- Enforce closed object schemas recursively through nested object properties and array item schemas.
- Keep skill command tools on their existing validator path so `additionalProperties:true`, required fields, and command-specific type checks keep the same behavior.
- Add focused built-in regressions for top-level unknown fields, trailing JSON, and nested unknown fields, plus adjacent skill command validator coverage.

Validation:

- Focused built-in and skill command registry regressions.
- Full tools package test before commit.
- Focused runtime tool-dispatch regressions.
- Full Go vet and test gate before commit.

### FCA-20260525-027: Web duplicate handle release can drop the active owner

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` describes the Web service adapter as owning asynchronous start / continue handles, and `Service.Close` cancels current handles before waiting for launch goroutines.
- `internal/webconsole/service.go` `handleContinueSession` and Plan Mode continue paths checked `hasActiveHandle` before creating a handle, but `addHandle` itself overwrote any existing map entry for the same session.
- `internal/webconsole/service.go` `finishHandle` unconditionally deleted `s.handles[sessionID]` when any handle for that session finished.
- If two Web actions for the same session raced, the duplicate runner could be rejected by durable session state, call `finishHandle`, and delete the original active handle from the map.

Impact:

A still-running Web-owned session could lose its current-process handle after a duplicate continue / Plan Mode action raced and failed. The browser would then lose same-process interrupt/stop ownership, and `Service.Close` would no longer cancel that active runner through the handle map.

Minimal fix:

- Make handle acquisition reject an already-active session at insertion time, not only through the earlier preflight check.
- Preserve a clear conflict status for duplicate Web continue / Plan Mode launch attempts.
- Make `finishHandle` remove the map entry only when the finishing handle is still the current owner for that session.
- Add focused lifecycle coverage that a duplicate handle is rejected and a stale release cannot remove the original owner.

Validation:

- Focused WebConsole duplicate-handle and adjacent lifecycle regressions.
- Full WebConsole package test before commit.
- Full Go vet and test gate before commit.

### FCA-20260525-028: Goal snapshot mutations can lose concurrent runtime progress facts

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` and `spec/11-spec-audit-and-traceability.md` define `goal.json` as the current durable goal snapshot used by Goal completion audit, WebConsole inspection, summaries, checkpoints, and recovery.
- Before this slice, `internal/session/goal.go` `UpdateGoalAccounting` loaded `goal.json`, mutated usage / budget fields, and then saved the whole snapshot.
- Before this slice, `internal/session/goal_progress.go` `RecordGoalProgress` loaded `goal.json`, mutated progress / mission / validation fields, and then saved the whole snapshot.
- Runtime accounting is called after provider turns from `internal/runtime/goal.go`, while model progress facts are written through the `record_goal_progress` tool path in `internal/tools/registry.go`.
- Web, CLI, runtime, and store-view paths can construct separate `Store` instances for the same session root, so the per-instance mutex did not protect the load / mutate / save transaction across runners or processes.

Impact:

A provider accounting update and a model progress / validation update could race on stale `goal.json` snapshots. The later stale save could drop `tokens_used`, `budget_limited_at`, `budget_wrapup_requested_at`, progress records, mission feature status, or validation evidence from the current goal snapshot, leaving the facts only partially represented in history or missing from Web / completion / recovery views.

Minimal fix:

- Add `Store.MutateGoal`, mirroring the existing parent-coordination mutation pattern, with a session-scoped `goal.lock` around read / mutate / validate / write.
- Route runtime/tool-owned current-snapshot mutations through `MutateGoal`: goal completion, budget accounting, and structured goal progress.
- Keep `SaveGoal` as full snapshot replacement but preserve its validation behavior before delegating to the serialized write path.
- Add a focused cross-store regression that holds one goal mutation open while a second store records progress, then verifies accounting and progress both persist in `goal.json`.

Validation:

- `go test ./internal/session -run 'TestStoreGoalConcurrentAccountingAndProgressMutationsBothPersist|TestStoreRecordGoalProgressUpdatesMissionValidationAndBudgetWrapUp|TestStoreGoalLifecycleAccountingAndSummary|TestStoreCompleteGoalPersistsAuditAndItemEvidence' -count=1`: passed.
- `go test ./internal/session -count=1`: passed.
- `go test ./internal/runtime -run 'TestEngineBudgetLimitedGoalWrapUpBlocksFinish|TestGoalCompletionGateBlocksActiveGoalAndAllowsCompletedGoal|TestParentCoordinationGateBlocksPendingBackgroundAcceptanceBeforeFinish|TestEngineRunModeStopsAtAwaitingInput' -count=1`: passed.
- `go test ./internal/runtime -run 'TestEngineBudgetLimitedGoalWrapUpBlocksFinish|TestGoalCompletionGateBlocksActiveGoalAndAllowsCompletedGoal|TestParentCoordinationGateBlocksPendingBackgroundAcceptanceBeforeFinish' -count=1`: passed.
- `gofmt -l internal/session/goal.go internal/session/goal_progress.go internal/session/store_test.go`: no output.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.
- Note: aggregate `go test ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...` and `go test -timeout 120s ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...` were stopped after the top-level Go command stayed quiet with no visible package test child; the same package set was then validated through focused and grouped commands above.

### FCA-20260525-029: Goal operator controls can overwrite current runtime facts

Severity: Medium

Evidence:

- FCA-20260525-028 serialized runtime/tool goal accounting and progress mutations through `Store.MutateGoal`, but `SaveGoal` intentionally remains a full-snapshot replacement for callers that already own the whole snapshot.
- `internal/webconsole/service.go` `handleGoalStatus` loaded `goal.json`, changed only status/completion fields, and saved the whole stale snapshot.
- `internal/webconsole/service.go` `handleMissionPlanApprove` loaded `goal.json`, changed only mission approval fields, and saved the whole stale snapshot.
- `internal/app/app.go` `mutateGoalStatus` and the direct CLI `goal plan approve` path used the same load / small mutation / full-save shape.
- `internal/runtime/runner.go` `approveLinkedMissionPlan` used the same shape after linked Plan Mode approval.

Impact:

An operator pause/resume/complete or mission-plan approval could race with provider accounting or `record_goal_progress` and re-save an older goal snapshot. That could remove budget fields, token usage, budget wrap-up records, progress evidence, or validation facts from `goal.json`, even after the runtime/tool mutation paths were made transactional.

Minimal fix:

- Add narrow store-owned transactional helpers for goal status changes and mission plan approval.
- Route Web, CLI, and linked Plan Mode mission approval through those helpers instead of stale full-snapshot replacement.
- Keep `SaveGoal` as full replacement for callers that intentionally replace an entire snapshot.
- Add focused Web and CLI regressions proving operator status changes preserve accounting and progress facts.

Validation:

- `go test ./internal/session -run 'TestStoreGoalConcurrentAccountingAndProgressMutationsBothPersist|TestStoreGoalLifecycleAccountingAndSummary|TestStoreCompleteGoalPersistsAuditAndItemEvidence' -count=1`: passed.
- `go test ./internal/runtime -run 'TestApproveLinkedPlanModeMarksMissionPlanApproved|TestApproveLinkedPlanModeBlocksUncoveredMissionValidation' -count=1`: passed.
- `go test ./internal/webconsole -run 'TestServiceGoalStatusPreservesAccountingAndProgressFacts|TestServiceGoalEndpointsMutateDurableGoal|TestServiceMissionApproveExecutingPlanModeAppendsApprovalFact' -count=1`: passed.
- `go test ./internal/app -run 'TestGoalStatusCommandPreservesAccountingAndProgressFacts|TestGoalMissionPlanAndValidationCommands' -count=1`: passed.
- `gofmt -l internal/session/goal.go internal/runtime/runner.go internal/webconsole/service.go internal/webconsole/service_test.go internal/app/app.go internal/app/app_test.go`: no output.
- `git diff --check`: passed.
- `go test ./internal/session ./internal/runtime ./internal/webconsole ./internal/app -count=1`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.
- Note: a multi-package command including `internal/runtime` was stopped after the top-level Go command stayed quiet with no visible package test child; `internal/runtime` and the same surrounding package set passed when split as shown above.

### FCA-20260525-030: Mission plan patching can overwrite current runtime facts

Severity: Medium

Evidence:

- `internal/webconsole/service.go` `handleGoalPatch` and `handleMissionPlanPatch` loaded `goal.json`, changed goal criteria / validation / mission fields, and saved the whole snapshot.
- `handleMissionPlanPatch` optionally called `SyncMissionPlanTasks` when `create_tasks_from_plan` was enabled.
- `internal/session/goal.go` `SyncMissionPlanTasks` created task files, inserted generated feature IDs / task IDs into the previously loaded goal value, then called full `SaveGoal`.
- After FCA-20260525-028 and FCA-20260525-029, runtime/tool/operator fields are serialized through `MutateGoal`, but these Web mission editing paths could still re-save stale `goal.json` snapshots.

Impact:

Editing a goal or mission plan from WebConsole could remove concurrent provider accounting, budget wrap-up fields, progress records, validation evidence, or other current runtime facts. The `create_tasks_from_plan` branch was especially risky because it wrote task IDs back to an older goal value after separate task graph writes.

Minimal fix:

- Add a store-owned `PatchGoal` helper for Web patch semantics so partial goal/mission edits merge into the latest goal snapshot.
- Change `SyncMissionPlanTasks` to merge only generated feature IDs and task IDs back into the latest mission snapshot instead of full-saving the older goal value.
- Keep task creation outside the goal lock to avoid lock reentrancy with task graph writes.
- Add focused WebConsole regressions for goal patching and mission patch task sync preserving accounting/progress facts.

Validation:

- `go test ./internal/webconsole -run 'TestServiceGoalPatchPreservesRuntimeProgressFacts|TestServiceMissionPlanPatchTaskSyncPreservesRuntimeProgressFacts|TestServiceMissionPlanPatchResetsApprovedPlanToPendingGate|TestServiceGoalPatchMissionResetsApprovedPlanToPendingGate|TestServiceGoalEndpointsMutateDurableGoal' -count=1`: passed.
- `go test ./internal/session -run 'TestStoreGoalLifecycleAccountingAndSummary|TestStoreGoalConcurrentAccountingAndProgressMutationsBothPersist' -count=1`: passed.
- `gofmt -l internal/session/goal.go internal/webconsole/service.go internal/webconsole/service_test.go`: no output.
- `go test ./internal/webconsole ./internal/session -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-031: Plan Mode transitions can race on stale snapshots

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` defines `planmode.json` as the session file fact source for Plan Mode, with `artifacts/planmode-history.jsonl` as state history.
- `internal/session/planmode.go` `SubmitPlanMode`, `SetPlanModePendingRequest`, `AnswerPlanModeInput`, `ApprovePlanMode`, `MarkPlanModeExecuting`, `RevisePlanMode`, and `CancelPlanMode` loaded `planmode.json`, mutated selected fields, and saved the whole snapshot.
- `SavePlanMode` serialized only the final write under `Store.mu`; separate Web, CLI, runtime, and recovery paths can construct separate `Store` instances for the same session root.
- Web active input answering, fallback continue, CLI approval/cancel, and runtime Plan Mode tools can target the same durable Plan Mode state around the same time.

Impact:

A Plan Mode submit, approval, input answer, revision, cancel, or execution transition could overwrite a concurrent transition with an older snapshot. That could lose submitted plan versions, approvals, pending request cancellation / answers, or execution status from `planmode.json`, leaving Web, recovery, and provider replay facts inconsistent with history.

Minimal fix:

- Add `Store.MutatePlanMode` with a session-scoped `planmode.lock` around read / mutate / validate / write.
- Keep `SavePlanMode` as a validated full replacement, but route store-owned Plan Mode transitions through `MutatePlanMode`.
- Add a focused cross-store regression that holds a submitted-plan mutation open while another store approves Plan Mode, proving approval waits and applies to the latest submitted version.

Validation:

- `go test ./internal/session -run 'TestPlanModeConcurrentMutationsReadLatestSnapshot|TestPlanModeSubmitApproveAndHistory|TestPlanModeInputValidationAndAnswer' -count=1`: passed.
- `go test ./internal/runtime -run 'TestApproveLinkedPlanModeMarksMissionPlanApproved|TestCancelPlanModeDoesNotDuplicateRecoveredInputToolResult|TestEngineSubmitPlanStopsTurnAndCompletesLaterToolResults' -count=1`: passed.
- `go test ./internal/webconsole -run 'TestServicePlanModeReviseInputAndCancelControls|TestServicePlanModeApproveAppendsReplayableUserMessage|TestServicePlanModeInputDetailKeepsLiveHandle' -count=1`: passed.
- `gofmt -l internal/session/planmode.go internal/session/planmode_test.go`: no output.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-032: Loaded skill facts can be erased by stale state saves

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` and `spec/04-tools-and-skills.md` define `load_skill` as a durable, idempotent session capability: once a skill is loaded, later `load_skill` calls should return the compact `already_loaded` result unless forced.
- `internal/tools/registry.go` `markSkillLoaded` loaded `state.json`, appended the skill name to `State.LoadedSkills`, and saved it during tool execution.
- `internal/runtime/engine.go` keeps an in-memory `state` value for the current run and saves that value at the next turn boundary, provider call, awaiting-input transition, completion, pause, and failure paths.
- A focused engine regression reproduced the loss: a first provider turn calls `load_skill`, the next provider turn naturally stops, and the final `state.json` has an empty `loaded_skills` list.

Impact:

The same skill body can be injected repeatedly in a session because the idempotency fact is lost immediately after normal engine progress. Web session summaries, compaction summaries, and provider prompt context can also under-report loaded skills even though the skill body was already delivered in `messages.jsonl`.

Minimal fix:

- Make `SaveState` serialize through a session-scoped `state.lock` and merge the latest durable `LoadedSkills` set into the state being saved.
- Treat `LoadedSkills` as a monotonic session fact; there is no current API path that intentionally clears it mid-session.
- Add store and engine regressions proving stale state saves preserve already loaded skill names.

Validation:

- `go test ./internal/runtime -run 'TestEnginePreservesLoadedSkillStateAcrossNextTurn|TestEngineEmitsContextLoadedEventWithDurableState|TestEngineRunModeStopsAtAwaitingInput' -count=1`: passed.
- `go test ./internal/session -run 'TestStoreSaveStatePreservesCurrentLoadedSkills|TestStoreSaveStateRefreshesUpdatedAt|TestStoreSaveStateIgnoresPredictableTempSymlink|TestStoreLoadStateRejectsSymlinkJSON' -count=1`: passed.
- `go test ./internal/tools -run 'TestLoadSkillReturnsAlreadyLoadedOnRepeatAndForceReload|TestLoadSkillIncludesShellWorkdirHint' -count=1`: passed.
- `gofmt -l internal/session/store.go internal/session/store_test.go internal/runtime/engine_test.go`: no output.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-033: Pending steer count can diverge from merged durable queue

Severity: Low

Evidence:

- `spec/05-session-interrupt-resume.md` lists `pending_steer_count` as part of `state.json`, while `control/steer.jsonl` is the durable steer queue.
- `internal/session/store.go` `UpdateSteerRequests` correctly merges a stale updated snapshot with concurrent appended steer requests under `control/steer.lock`.
- `internal/runtime/engine.go` computed `State.PendingSteerCount` from the stale pre-merge request slice before or around `UpdateSteerRequests`, and later engine `SaveState` calls could write that stale count again.
- A focused `drainSteer` regression reproduced the drift: one steer was accepted, a second steer was appended during the drain window, `UpdateSteerRequests` preserved both records, but the state counter could report zero pending requests.

Impact:

Session detail, summaries, Web status chips, and recovery hints can under-report pending steer work even though `control/steer.jsonl` still contains a pending request. Runtime eventually reads the queue itself, so this is primarily an observability and operator-steering accuracy issue rather than loss of the queued input.

Minimal fix:

- Add a store helper that refreshes `PendingSteerCount` from the durable steer queue.
- Route Runner steer and Engine steer drain/defer counter writes through that helper.
- Make `SaveState` derive `PendingSteerCount` from `control/steer.jsonl` whenever the steer queue exists, preventing later stale state saves from overwriting the queue-derived count.
- Add store and runtime regressions covering a concurrent append during steer update/drain.

Validation:

- Focused steer store/runtime regressions before commit.
- Full Go vet and grouped package validation before commit.

### FCA-20260525-034: Same-path artifact instructions can inherit stale completion freshness

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` requires the session contract and artifact tracker to refresh after new external instructions so explicit artifact constraints participate in later completion gates.
- `internal/runtime/contract.go` derives `contract.json` and `artifact-tracker.json` from the latest external user instruction, but `contractsEquivalent` only compared profile, gates, anchors, and required artifact paths.
- If a session already wrote `reports/final.md`, then a later user message again requested `reports/final.md` with new content requirements, `refreshContractForSession` treated the contract as equivalent and kept the old `artifact-tracker.json` status with `touched_by_session=true`.
- A focused regression reproduces the behavior: the first instruction writes and tracks `reports/final.md`; the second same-path instruction refreshes the contract; before the fix, `requiredArtifactGate("finish")` could still pass without any artifact write after the second instruction.

Impact:

A model can satisfy an earlier required-artifact instruction, receive a later same-path revision request, and then finish without updating that artifact for the latest instruction. This weakens the latest-user-instruction contract and can produce stale deliverables while the completion gate reports success.

Minimal fix:

- Persist the latest external instruction identity and text hash in `SessionContract`.
- Include those source fields in contract equivalence, so a new user instruction with the same required artifact path rebuilds the artifact baseline and clears old freshness status.
- Add a regression proving same-path newer instructions block finish until the artifact is touched or changed again.

Validation:

- `go test ./internal/runtime -run 'TestContractRefreshResetsArtifactFreshnessForSamePathNewInstruction|TestCompletionControllerRequiresSessionTouchedArtifact|TestSessionContractTracksRequiredArtifactAndCompletionGate' -count=1`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `git diff --check`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-035: Task graph mutations can allocate duplicate IDs and overwrite concurrent tasks

Severity: Medium

Evidence:

- `spec/12-task-system.md` defines the persistent task graph as durable, recoverable session state with consistent dependency edges.
- `internal/session/taskboard.go` `CreateTask` calls `NextTaskID`, then `ListTasks`, appends a new task, and writes the whole graph via `SaveTasks`.
- `UpdateTask` similarly loads the whole graph, mutates dependency/status fields, and saves all task files.
- `SaveTasks` serializes only the final write through one `Store` instance; Web, runtime tools, mission task sync, and CLI/API code can construct separate `Store` instances for the same session root.
- If two `task_create` paths run from stale snapshots, both can allocate `task_0002`; the later full graph write can either overwrite the first task with a duplicate ID or drop another concurrently created task file from the current graph.

Impact:

Long-running sessions can lose or corrupt durable task graph progress when task creation/update and mission task sync overlap. That weakens resume, compaction, Web task board, checkpoint, and session summary facts for the exact class of large tasks the task graph is meant to support.

Minimal fix:

- Add a session-scoped task graph mutation helper that locks `tasks/taskboard.lock`, reads the latest tasks, applies create/update logic, validates references and cycles, and writes the latest graph.
- Route `CreateTask` and `UpdateTask` through that helper so ID allocation and dependency synchronization happen under one cross-store lock.
- Add focused cross-store regression coverage for concurrent task creation reading the latest graph under the lock.

Validation:

- `go test ./internal/session -run 'TestTaskMutationsReadLatestGraphUnderLock|TestTask' -count=1`: passed.
- `go test ./internal/tools -run 'TestTodoAndTaskToolsEmitStructuredEvents|TestFeatureListToolsPersistUpdateAndReadSnapshot' -count=1`: passed.
- `go test ./internal/runtime -run 'TestEngineEmitsContextLoadedEventWithDurableState|TestEngineRunModeStopsAtAwaitingInput' -count=1`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `git diff --check`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-036: Duplicate queue status files can hide terminal job facts

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` requires queue job status, linked child session result, parent notification, and worker liveness to be explainable from durable files.
- `internal/session/store.go` stores queue jobs under `_queue/<status>/<job_id>.json`, and `saveJobLocked` writes the new status file before removing stale copies in other status directories.
- `LoadJob` scans `queueStatuses()` in `queued`, `running`, `blocked`, `completed`, `failed` order and returns the first readable job, while `listJobs` appends every readable status-file copy.
- A crash or interrupted process between the terminal write and stale running-file cleanup can therefore leave `_queue/running/<job>.json` and `_queue/completed/<job>.json` for the same job. The current load order can report the stale running copy before the completed copy, and list views can show duplicate jobs.

Impact:

A completed or failed background child can remain visible as running/blocked until the stale duplicate is manually removed or later reconciliation happens to repair the exact copy being read. That weakens Web queue/children status, parent background visibility, session summaries, and recovery decisions for queue jobs after interrupted status transitions.

Minimal fix:

- Add a queue job loader that gathers all status-file copies for a job, prefers the highest-precedence durable fact with terminal status first, and removes stale duplicates.
- Route `LoadJob` and `listJobs` through the same canonicalization path so object detail and list views agree.
- Add a store regression that creates duplicate running/completed files and verifies `LoadJob` and `ListJobs` return one completed job while cleaning the stale running file.

Validation:

- `go test ./internal/session -run 'TestLoadAndListJobsPreferTerminalDuplicateStatusFile|TestReconcileCompletedSessionCompletesJob|TestStoreClaimNextQueuedJobIsAtomicAcrossStores' -count=1`: passed.
- `go test ./internal/session -run 'TestLoadAndListJobsPreferTerminalDuplicateStatusFile|TestClaimNextQueuedJobWritesLease|TestClaimNextQueuedJobSkipsMismatchedQueueFilename|TestReconcileCompletedSessionCompletesJob' -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-037: Doctor queue diagnostics skip blocked jobs

Severity: Low

Evidence:

- `spec/15-background-queue.md` defines `_queue/blocked/` as a durable queue status directory and says resumable child sessions (`paused` / `awaiting_input`) must keep the queue job `blocked`.
- `internal/runtime/delegation.go` maps non-terminal resumable child results to `QueueStatusBlocked`, and `internal/session/store.go` includes `blocked` in `queueStatuses()`.
- `spec/02-cli-and-config.md` says `doctor` reports queue partial state, including duplicate status directories and queue jobs pointing at missing sessions.
- `internal/app/doctor_helpers.go` `doctorQueueStatuses()` only scans `queued`, `running`, `completed`, and `failed`, so jobs under `_queue/blocked/` are invisible to duplicate-status and missing-session diagnostics.

Impact:

Operator recovery diagnostics can miss resumable background jobs, including blocked jobs that reference deleted child sessions or duplicate blocked/running status files. Runtime and Web queue views still see the jobs through the session store, so this is a CLI doctor observability gap rather than direct queue state loss.

Minimal fix:

- Include `session.QueueStatusBlocked` in `doctorQueueStatuses()`.
- Add a doctor regression proving blocked queue jobs with missing child-session references are reported.

Validation:

- `go test ./internal/app -run 'TestDoctorReportsBlockedQueueJobMissingSessionRef|TestDoctorReportsQueueLeaseAndMissingSessionRef|TestDoctorReportsDuplicateQueueJobStatus' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/app ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-038: Continued queue children do not notify parent on terminal completion

Severity: Medium

Evidence:

- `spec/15-background-queue.md` says blocked queue jobs are resumable and keep `session_id` / `session_status` so the child can be continued, and child completion/failure must flow back to the parent through background notification.
- `internal/runtime/delegation.go` `ProcessNextJob` updates the queue job, parent notification, and parent coordination when the worker-run child returns terminal in the same call.
- A blocked child resumed later through normal `Runner.Continue` reaches `Engine.complete` / `Engine.fail`, which only updates the child `state.json`, child `session.md`, and child checkpoint.
- `internal/session/store.go` can repair a linked queue job to terminal when `LoadJob` / `ListJobs` calls `reconcileQueueJobSession`, but that is observer-triggered rather than part of the child terminal transition.
- `EnsureBackgroundNotification` dedupes only by `queue_job_id` and returns without replacing an older blocked notification, so even observer-triggered terminal repair can leave the parent `control/background.jsonl` with stale blocked status.

Impact:

If a background child pauses or awaits input, the queue job becomes `blocked` as designed. After an operator continues that child to completion, the parent can remain parked, miss the terminal background result, and keep stale blocked notification facts until an unrelated queue read occurs; even then, notification dedupe can preserve the old blocked notification. This weakens queue recovery, parent completion gates, Web Background inspector, and provider-visible background result replay for resumable child jobs.

Minimal fix:

- Add a runtime/store helper that reconciles linked queue jobs immediately after child terminal or resumable state changes.
- Let terminal queue reconciliation replace an existing same-job background notification with fresher terminal facts.
- Call the helper from `Engine.complete`, `Engine.fail`, pause, and awaiting-input paths so resumed queue children update parent facts at the state transition boundary.
- Add focused regressions covering a blocked queue child continuing to completed, updating the job, parent coordination, and background notification.

Validation:

- `go test ./internal/session -run 'TestEnsureBackgroundNotificationRefreshesChangedQueueFacts|TestUpdateBackgroundNotificationsMergesConcurrentAppend|TestReconcileCompletedSessionCompletesJob' -count=1`: passed.
- `go test ./internal/runtime -run 'TestEngineCompletingQueuedChildReconcilesParentQueueFacts|TestEngineAcceptsBackgroundResultsBeforeProviderCall|TestParentCoordinationWritesParkedAndResumedEvents' -count=1`: passed.
- `go test ./internal/session -run 'TestEnsureBackgroundNotificationRefreshesChangedQueueFacts|TestUpdateBackgroundNotificationsMergesConcurrentAppend' -count=1`: passed.
- `go test ./internal/runtime -run 'TestEngineCompletingQueuedChildReconcilesParentQueueFacts|TestEngineAcceptsBackgroundResultsBeforeProviderCall' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-039: Background notification acceptance can erase fresher queue facts

Severity: Medium

Evidence:

- `spec/15-background-queue.md` requires child completion/failure to flow back to the parent through durable `control/background.jsonl` notification facts, and Web Background inspector reads those same parent notifications.
- `internal/runtime/engine.go` `drainBackground` loads the full notification slice, appends a `background_results` user message for pending entries, marks the loaded pending entries `accepted`, and calls `Store.UpdateBackgroundNotifications`.
- `internal/session/store.go` `EnsureBackgroundNotification` can concurrently refresh the same `queue_job_id` from blocked/awaiting-input facts to completed/failed terminal facts after a child is continued.
- `internal/session/store.go` `mergeBackgroundNotifications` currently builds replacements from the stale `UpdateBackgroundNotifications` argument and replaces any current durable notification with the same merge key, even when the current file contains newer same-job status, session status, final text, or error facts.

Impact:

A parent runner can accept a stale blocked background notification while the child finishes at nearly the same time. The subsequent `UpdateBackgroundNotifications` write can replace the fresh terminal pending notification with the older accepted blocked snapshot. The parent then loses the terminal notification redelivery opportunity, the Web Background inspector can keep showing stale blocked facts, and the parent provider view may never receive the child completion result.

Minimal fix:

- When merging a background notification update with an existing same-key durable notification, treat differing queue/session/result fields in the current file as newer facts and keep them pending instead of replacing them with a stale accepted snapshot.
- Keep the existing accepted-status update when the current and updated facts are the same, and continue preserving unrelated concurrent appends.
- Add focused store regression coverage for a stale accepted blocked snapshot racing with a concurrent completed same-job notification refresh.

Validation:

- `go test ./internal/session -run 'TestUpdateBackgroundNotificationsPreservesConcurrentFactRefresh|TestEnsureBackgroundNotificationRefreshesChangedQueueFacts|TestUpdateBackgroundNotificationsMergesConcurrentAppend' -count=1`: passed.
- `go test ./internal/runtime -run 'TestEngineCompletingQueuedChildReconcilesParentQueueFacts|TestEngineAcceptsBackgroundResultsBeforeProviderCall|TestParentCoordinationWritesParkedAndResumedEvents' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-040: Background notifications do not expose Open job actions

Severity: Low

Evidence:

- `spec/17-web-console.md` says background notification links must let the operator use `Open job` in the Background inspector to expand selected queue job facts.
- `internal/webconsole/assets/app.js` already handles `[data-open-job]` by setting `selectedQueueJobId`, switching the inspector to Background, fetching `/api/queue/jobs/{id}`, and rendering the selected job facts panel.
- `internal/webconsole/assets/session-view.js` `renderBackgroundResultItem`, `renderSubAgentCard`, and `renderQueueJobCard` already expose `data-open-job` actions.
- `internal/webconsole/assets/session-view.js` `renderNotificationCard` only renders `Open child session` for background notifications and omits `Open job` even when `queue_job_id` exists; `renderBackgroundNotificationsPreview` renders recent notification cards with no actions at all.

Impact:

Operators can see a background notification in the parent session but cannot open the linked queue job facts from that notification, despite the selected job facts panel and handler already existing. This makes the Web Background inspector less traceable than the spec requires, especially for notifications where the queue job has useful prompt/error/final-text context or the child session is unavailable.

Minimal fix:

- Add `Open job` actions to full background notification cards when `queue_job_id` is present.
- Add the same lightweight action to the summary notification preview so the Summary panel's queued input/notifications card can open selected queue job facts directly.
- Add focused frontend renderer coverage proving both notification renderers emit the expected `data-open-job` action.

Validation:

- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `go test ./internal/webconsole -run 'TestServiceEmbeddedAssetsExposeWebFirstConsole|TestServiceQueueWorkersProcessJob|TestServiceParallelQueueWorkersPersistAllJobs' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-041: Cancelled tasks are counted as completed in Web task facts

Severity: Low

Evidence:

- `spec/12-task-system.md` separates task statuses `completed` and `cancelled`, and states that both are done states but only `completed` automatically unlocks dependents.
- `internal/session/taskboard.go` `BuildTaskBoard` put both `completed` and `cancelled` tasks into a single `done` slice, then exposed that slice as `Counters["completed"]` and `Groups["completed"]`.
- `internal/webconsole/assets/session-view.js` `renderTasksPanel` renders `Counters["completed"]` as the `Completed` metric and separately renders `${counters.cancelled || 0} cancelled`.
- Because `BuildTaskBoard` never emitted `Counters["cancelled"]`, a cancelled durable task appeared as completed in the Web task metric while the cancelled subtitle stayed `0 cancelled`.
- `internal/tools/registry.go` `task_list` also used `board.Counters["completed"]` for `completed_count`, so tool metadata had the same conflation.

Impact:

Operators and model-visible task-list metadata could misread cancelled durable task graph nodes as completed work. This weakens recovery and handoff accuracy for long-running sessions where cancelled tasks should remain distinct from successfully completed tasks.

Minimal fix:

- Split `completed`, `cancelled`, and combined `done` derived counters in `BuildTaskBoard`.
- Expose separate `completed`, `cancelled`, and `done` task groups while preserving a combined done view for clients that need it.
- Add `cancelled_count` and `done_count` to `task_list` metadata.
- Add focused session task-board and frontend renderer regressions.

Validation:

- `go test ./internal/session -run 'TestBuildTaskBoardIncludesInProgressGroup|TestBuildTaskBoardSeparatesCompletedAndCancelled' -count=1`: passed.
- `go test ./internal/tools -run 'TestTodoAndTaskToolsEmitStructuredEvents|TestTodoWriteNoopDoesNotLookLikeProgress' -count=1`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260526-042: Web workspace browser exposes common private-key and credential filenames

Severity: Medium

Evidence:

- `spec/17-web-console.md` requires the read-only Workspace browser to hide and refuse `.env` variants, SSH/cloud/kube/docker credential directories, private-key filenames, and `credentials`-like paths.
- `internal/webconsole/service.go` `webFileBrowserNameDenied` only denied exact `id_rsa`, exact `id_ed25519`, and exact `credentials` for private-key / credential file handling.
- The same helper is used by `listDirectory` and by `webFileBrowserPathDenied` before `/api/file/read`, so names such as `id_ecdsa`, `deploy.pem`, and `credentials.json` were neither hidden from `/api/files` nor refused by `/api/file/read`.
- Existing `TestServiceWorkspaceRoutesListReadAndRejectEscape` covered `.env` filtering and workspace escape rejection, but did not cover the broader private-key / credential-like names required by the Web Console spec.

Impact:

A local Web Console opened on a workspace containing common SSH key material, PEM key files, or JSON credential files could display and serve those files through the Workspace browser. This violates the browser-specific leakage guard in the Web-first contract, especially when the console is intentionally allowed to browse the workspace parent within the server cwd.

Minimal fix:

- Broaden `webFileBrowserNameDenied` to catch common private-key names and key material extensions such as `id_*`, `identity`, `private-key`, `private_key`, `.pem`, `.key`, `.p12`, and `.pfx`.
- Treat common `credentials` variants such as `credentials.json` and `*_credentials.json` as credential-like browser-denied paths.
- Extend the workspace route regression to verify these names are hidden from listings and rejected by direct reads in both workspace and parent browsing contexts.

Validation:

- Focused WebConsole workspace route regression.
- Full WebConsole package tests.
- Standard grouped validation before commit.

### FCA-20260526-043: Failed Web config writes can still persist API keys

Severity: Medium

Evidence:

- `spec/17-web-console.md` treats API key/config writes as sensitive local-console mutations and requires them to be auditable; it also states that `POST /api/config` saves provider defaults, API keys, and emits audit events.
- `internal/webconsole/service.go` `handleUpdateConfig` previously called `os.Setenv` and `config.UpsertEnvFile` for `req.APIKey` before writing the config file with `config.WriteFile`.
- The same handler appended `web.config.write` and `web.config.api_key_write` audit events only after the config write succeeded.
- Therefore, if `config.WriteFile` failed after the env-file upsert, the HTTP request returned `500` but the process environment and `.env` file could already contain the new API key with no matching config or audit event.

Impact:

A failed Settings save could leave a newly submitted secret persisted even though the browser saw a failed request and no audit event was recorded. That violates the Web-first safety contract for sensitive API key writes and makes local recovery/audit misleading.

Minimal fix:

- Defer process and `.env` API-key mutation until audit-log writability and config persistence have succeeded.
- Keep API key audit metadata secret-free.
- Add a regression proving a config-write failure does not mutate `OPENAI_API_KEY`, does not persist the secret in `.env`, and does not append a config audit event.

Validation:

- Focused WebConsole config regression.
- Existing API-key audit secrecy regression.
- Full WebConsole package tests.
- Standard grouped validation before commit.

### FCA-20260526-044: Sensitive Web mutations can complete before audit failure

Severity: Medium

Evidence:

- `spec/17-web-console.md` requires session delete/clear and skill install/uninstall to write searchable audit events.
- `internal/webconsole/service.go` `handleDeleteSession` called `DeleteSessionTree` before appending `web.session.delete`.
- `handleClearSessions` called `ClearHistory` before appending `web.sessions.clear`.
- `handleUploadSkill` extracted the uploaded skill zip into the managed skills directory before appending `web.skill.install`.
- `handleUninstallSkill` removed the skill directory before appending `web.skill.uninstall`.
- If the audit log path was unavailable, these handlers returned `500` after the local destructive/install mutation had already happened, leaving no matching audit event.

Impact:

The browser could report a failed sensitive action even though sessions were deleted, history was cleared, or skills were installed/uninstalled. That weakens operator recovery and violates the Web-first auditability contract for risky local-console actions.

Minimal fix:

- Preflight audit-log writability before session delete, session clear, skill upload extraction, and skill uninstall removal.
- Reuse the shared no-symlink audit log opener so the preflight enforces the same path safety as real audit writes.
- Add a regression proving session delete and skill uninstall do not mutate disk when audit-log preflight fails.

Validation:

- Focused WebConsole sensitive-action audit preflight regression.
- Existing sensitive-action audit event regression.
- Full WebConsole package tests.
- Standard grouped validation before commit.

### FCA-20260526-045: Runtime recovery summaries still count cancelled tasks as completed

Severity: Low

Evidence:

- `spec/12-task-system.md` says `completed` and `cancelled` are both done states, but only `completed` unlocks dependents.
- FCA-20260525-041 fixed `BuildTaskBoard` and `task_list` metadata to expose separate `completed`, `cancelled`, and combined `done` facts.
- `internal/runtime/engine.go` `taskCounts` still counted `completed` and `cancelled` together and returned that value as `completed`.
- `internal/runtime/session_summary.go` used that helper for `session.md` and `checkpoints/longrun-latest.json`, so recovery artifacts could still say `completed=2` when one task was cancelled.
- `internal/runtime/compaction.go` used the same helper in compaction summary artifacts and lifecycle event metadata, causing provider-facing recovery context to inherit the same conflation.

Impact:

Long-running session recovery facts, checkpoints, and compaction summaries could still overstate completed work after a task was cancelled. This weakens handoff accuracy even after the Web task board and model `task_list` facts were corrected.

Minimal fix:

- Change the shared runtime task-count helper to return `completed`, `cancelled`, and combined `done` counts separately.
- Update session summary, long-run checkpoint task summary, context-loaded events, and compaction summaries/events to expose the separated facts.
- Add focused runtime regressions for `session.md`, long-run checkpoint, and compaction event/artifact metadata.

Validation:

- Focused runtime summary/checkpoint regression.
- Focused compaction regression.
- Standard grouped validation before commit.

### FCA-20260526-046: Web summary drops provider-attempt ledger facts

Severity: Low

Evidence:

- `spec/01-runtime-architecture.md` defines `provider-attempts.jsonl` as a recovery and diagnostic fact source, and `spec/17-web-console.md` requires session detail to expose provider attempts and latest error state in the Web-first operator surface.
- `internal/webconsole/service.go` loads `provider-attempts.jsonl` and returns the tailed facts as `provider_attempts` in `SessionDetailResponse`.
- `internal/webconsole/assets/session-view.js` did not read `detail.provider_attempts`; the summary panel only inferred retry context from recent events and `state.last_error`.
- `rg` over `internal/webconsole/assets` and `validation/scripts` found no provider-attempt renderer or frontend regression coverage before this slice.

Impact:

Operators using the default Web console could miss durable retry, auto-resume, final failure, success, response id, cache token, and timeout/status facts even though the backend had already returned them. Diagnosing provider failures still required leaving the Web UI for `session.md` or `provider-attempts.jsonl`.

Minimal fix:

- Render a compact Provider Attempts section in the session Summary inspector when `provider_attempts` are present.
- Show total attempts, recovery attempts, cache counters, recent outcomes, turn/attempt numbers, error class, timeout kind, status code, response id, and cache read/create counts.
- Add a frontend renderer regression proving session summary output includes provider-attempt ledger facts.

Validation:

- Focused frontend renderer regression.
- JavaScript syntax validation.
- Standard grouped validation before commit.

### FCA-20260526-047: Goal plan approval does not enter running UI state for linked Plan Mode

Severity: Low

Evidence:

- `spec/17-web-console.md` says approving a goal plan with linked pending Plan Mode must go through Plan Mode approval / continue, and Plan Mode approval resumes execution as the next durable turn.
- `internal/webconsole/service.go` `handleMissionPlanApprove` returns `202 Accepted` with `LaunchResponse{status:"accepted"}` when a linked Plan Mode is `awaiting_approval` or `approved`, because it launches `runtime.ContinueRequest{ApprovePlan:true}` asynchronously.
- `internal/webconsole/assets/app.js` `handlePlanModeAction("approve")` calls `setGenerating(true, ...)` immediately after Plan Mode approval.
- `internal/webconsole/assets/app.js` `handleGoalAction("approve-plan")` did not inspect the response from `approveMissionPlan`; it always showed success and waited for a refresh while `state.isGenerating` stayed false until polling saw `state.status=running`.
- The polling loop only continues automatically while `state.isGenerating` is true, while session detail is missing, or while active descendants exist. The Goal approval path queued a single refresh, but did not enter the same running/polling UI state as direct Plan Mode approval.

Impact:

After approving a linked Goal plan from the Goal inspector, the default Web console could briefly present stale composer/actions instead of the running-state controls used by direct Plan Mode approval. This weakens Web-first action consistency for the same backend execution path.

Minimal fix:

- Detect accepted asynchronous launch responses from `approveMissionPlan`.
- When Goal plan approval returns an accepted launch, set the same running activity state used by Plan Mode approval.
- Add a focused frontend utility regression for accepted-launch response detection.

Validation:

- Focused frontend utility regression.
- JavaScript syntax validation.
- Standard grouped validation before commit.

### FCA-20260526-048: CLI tasks view hides cancelled task group

Severity: Low

Evidence:

- `spec/12-task-system.md` says `completed` and `cancelled` are both done states, but only `completed` unlocks dependents.
- FCA-20260525-041 changed `BuildTaskBoard` to expose separate `completed`, `cancelled`, and combined `done` groups/counters.
- `internal/app/app.go` `tasksCommand` still rendered only `in_progress`, `ready`, `blocked`, and `completed` groups in normal text mode.
- `normalizeTaskBoard` also only initialized `ready`, `blocked`, and `completed`, so fallback callers with sparse task-board data had no normalized `cancelled` group.

Impact:

CLI fallback users could miss cancelled durable task graph nodes unless they remembered `--all`. That made the CLI less accurate than Web/task-list facts for recovery and handoff review.

Minimal fix:

- Render `cancelled` as its own normal text-mode task group.
- Normalize the separated task groups, including `cancelled` and compatibility `done`.
- Extend the tasks command regression so cancelled tasks are visible without `--all`.

Validation:

- Focused CLI tasks command regression.
- Standard grouped validation before commit.

### FCA-20260526-049: Failed Web API-key env writes can leave unaudited config changes

Severity: Medium

Evidence:

- `spec/17-web-console.md` requires Settings provider/model/API key writes to be risk-confirmed and auditable, with API-key audit events recording metadata but never secret values.
- `internal/webconsole/service.go` `handleUpdateConfig` builds `updatedCfg`, preflights audit-log writability, then calls `config.WriteFile(configPath, updatedCfg)` before attempting `config.UpsertEnvFile` or `os.Setenv` for a submitted API key.
- `config.UpsertEnvFile` rejects an empty env key, symlinked env file, and non-regular env-file paths; those failures happen after the config file has already been written.
- The Web config audit events are appended only after the env-file and process environment updates succeed.

Impact:

A Settings save that includes an invalid API-key env target can return an error after persisting provider/model/reasoning/default-provider config changes. Because the handler appends `web.config.write` later, that failed request can leave durable config changes without the required Web audit event, and the operator sees the whole save as failed.

Minimal fix:

- Preflight the API-key env target before writing `config.yaml`.
- Reuse the same basic env target invariants as `config.UpsertEnvFile`: non-empty env key, existing path is not a symlink, existing path is regular, and existing read errors are surfaced.
- Add a regression proving a failed API-key env preflight leaves `config.yaml`, `.env`, process environment, and Web audit log unchanged.

Validation:

- Focused Web config regression.
- Standard grouped validation before commit.

### FCA-20260526-050: Task derived-view specs still omit cancelled and done facts

Severity: Low

Evidence:

- `spec/12-task-system.md` now defines `completed` and `cancelled` as done states, with only `completed` unlocking dependents.
- FCA-20260525-041, FCA-20260526-045, and FCA-20260526-048 updated session task boards, model tool metadata, runtime recovery artifacts, Web, and CLI fallback paths to expose separate `completed`, `cancelled`, and combined `done` facts.
- `internal/session/taskboard.go` `BuildTaskBoard` now emits `Counters` and `Groups` for `completed`, `cancelled`, and `done`.
- `internal/tools/registry.go` `task_list` metadata now emits `completed_count`, `cancelled_count`, and `done_count`.
- `spec/04-tools-and-skills.md`, `spec/08-sdk-and-api-evolution.md`, `spec/12-task-system.md`, and `spec/17-web-console.md` still describe task derived views or task statistics as only `ready / blocked / completed`.

Impact:

The implementation and authoritative task-system semantics are more precise than several consumer-facing specs. Future implementation or validation work could regress cancelled-task visibility by following the stale completed-only descriptions.

Minimal fix:

- Update the stale spec lines to name `ready`, `blocked`, `completed`, `cancelled`, and combined `done` where derived task views or task statistics are described.
- Do not change runtime behavior.

Validation:

- Text search proving no stale `ready / blocked / completed` task-derived descriptions remain in the touched specs.
- Standard lightweight formatting/diff checks before commit.

### FCA-20260526-051: Web Plan Mode approve/revise can launch on invalid plan states

Severity: Medium

Evidence:

- `spec/17-web-console.md` says Plan Mode `Approve & Run` approves the latest plan and starts execution, while `Ask for Changes` is a plan revision user fact; pending Plan Mode input should be answered or cancelled through the input/cancel controls.
- `internal/session/planmode.go` `ApprovePlanMode` only accepts `awaiting_approval` or `approved`, and `RevisePlanMode` only accepts `awaiting_approval`, `rejected`, or `approved`.
- `internal/webconsole/service.go` `handlePlanModeApprove` checks active handles and mission coverage, but does not verify the current Plan Mode status before `launchPlanModeContinue`.
- `handlePlanModeRevise` also launches a continuation without verifying the Plan Mode status.
- `launchPlanModeContinue` only checks session resumability, so a session in `awaiting_input` with Plan Mode still `planning` or `awaiting_user_input` can be claimed as running before the runtime discovers that approval/revision is invalid or interprets the revision as an ordinary continuation.

Impact:

Invalid Web Plan Mode actions can mutate durable session state and active Web handles instead of returning a clean conflict. A mistaken approve click before a plan is submitted can move the session through a failed continue path, and a mistaken revise click while Plan Mode is waiting for `request_user_input` can bypass the pending input control path.

Minimal fix:

- Preflight Web Plan Mode approve and revise actions against the current `planmode.json` status before launching the async continue path.
- Return conflict for invalid statuses without claiming the session or appending messages.
- Add focused Web service regressions for approve-from-planning and revise-from-awaiting-user-input.

Validation:

- Focused Web Plan Mode service regressions.
- Standard grouped validation before commit.

### FCA-20260526-052: Duplicate live Plan Mode input delivery can hang Web handlers

Severity: Medium

Evidence:

- `spec/17-web-console.md` requires Web Plan Mode pending questions to be answered or cancelled through the Plan inspector controls, with malformed or failed actions surfaced as backend errors instead of silent failure.
- `internal/webconsole/service.go` `handlePlanModeInput` and `handlePlanModeCancel` deliver active pending input through the in-memory runner helpers before falling back to a recovered continue path.
- `internal/runtime/runner.go` `AnswerActivePlanInput` and `CancelActivePlanInput` looked up the one-slot waiter channel while holding `planInputMu`, left the waiter in `planInputWaiters`, and sent to the channel while still holding the mutex.
- A second Web input/cancel request for the same active pending request could observe the still-registered waiter before the blocked runner consumed the first response. Because the channel buffer was already full, the second handler could block instead of returning a conflict.

Impact:

Duplicate browser submissions, retrying clients, or an input/cancel race can hang a Web mutation handler and hold the Plan Mode waiter mutex. That weakens the local Web Console control contract for active Plan Mode sessions and can prevent later input/cancel operations from resolving cleanly.

Minimal fix:

- Claim the active Plan Mode waiter by deleting it from `planInputWaiters` before delivering the answer or cancellation response.
- Release `planInputMu` before sending to the waiter channel so duplicate delivery attempts return `false` rather than blocking on a full channel.
- Add a focused runtime regression that fills an active waiter channel and proves duplicate answer/cancel delivery returns promptly with `false`.

Validation:

- Focused runtime Plan Mode duplicate-delivery regression.
- Standard grouped validation before commit.

### FCA-20260526-053: Skill upload accepts duplicate sanitized target directories

Severity: Medium

Evidence:

- `spec/17-web-console.md` says skill install/uninstall are unsafe local-console mutations and skill upload must enforce request, entry, and extraction limits instead of dragging the local console into malformed-package side effects.
- `internal/webconsole/service.go` `processSkillZip` finds every `SKILL.md`, derives a target directory from frontmatter `name:` or the root directory, then calls `sanitizeDirName`.
- `sanitizeDirName` maps every non-alphanumeric / non-hyphen / non-underscore rune to `_`, so distinct package names such as `demo!` and `demo?` both become `demo_`.
- Before this slice, `processSkillZip` did not precompute or deduplicate target directories. It removed and recreated each target inside the per-root extraction loop.
- A zip containing two skill roots that sanitize to the same target could therefore extract the first package, remove it while processing the second package, return `installed_count=2`, and leave only the second package on disk.

Impact:

A malformed uploaded multi-skill zip can silently overwrite one planned skill package with another and report an inflated install count. If the duplicate target matches an already installed skill, the existing skill can be replaced by whichever duplicate root is processed last instead of the upload being rejected as ambiguous.

Minimal fix:

- Build and validate a full extraction plan before any target directory is removed.
- Reject duplicate sanitized target names inside the same zip so one package cannot overwrite another planned package.
- Require each target path to be a direct child under the managed skills root.
- Add a focused regression proving duplicate target names are rejected before mutation.

Validation:

- Focused skill zip regression tests.
- Standard grouped validation before commit.

### FCA-20260526-054: Session delete can remove active deep descendants

Severity: Medium

Evidence:

- `spec/17-web-console.md` requires the WebConsole launch manager to maintain per-session active handles for sessions owned by the current Web server process, because interrupt/stop must target the correct in-memory runner and handles cannot be treated as durable facts.
- `internal/webconsole/service.go` `handleDeleteSession` first called `hasActiveDescendantHandle` before deleting a session tree.
- Before this slice, `hasActiveDescendantHandle` only checked the target session id, handles whose `ParentSessionID` directly matched the target, or handles whose `RootSessionID` matched the target.
- `internal/session/store.go` `DeleteSessionTree` deletes descendants transitively through `ParentSessionID`, so deleting an intermediate child also removes grandchildren and deeper descendants.
- A live Web handle on a deeper descendant below an intermediate child can have `RootSessionID` set to the top-level root and `ParentSessionID` set to its immediate parent, so the old preflight missed it when deleting the intermediate child.
- `ensureSessionTreeNotLive` only blocked durable `running` sessions/jobs; it did not block current-process active handles in non-running states such as `awaiting_input`.

Impact:

An operator could delete an intermediate session tree while a deeper descendant still had a current-process Web runner waiting for input or otherwise owned by the Web service. That could remove the live runner's session directory while the in-memory handle still existed, violating the local session/state file-fact contract and weakening Web lifecycle controls.

Minimal fix:

- Compute a transitive session-tree target set from session summaries and use it for active handle checks.
- Reuse the same target-set helper for durable running-session and running-queue checks so all delete preflights agree on tree membership.
- Return an internal error if the active-handle preflight cannot read session summaries instead of deleting with incomplete evidence.
- Add a focused Web service regression with an active great-grandchild handle while deleting an intermediate child session.

Validation:

- Focused Web deletion regression.
- Standard grouped validation before commit.

### FCA-20260526-055: Released Web handle events still appear as running owners

Severity: Low

Evidence:

- `spec/17-web-console.md` says WebConsole active handles are in-memory only; durable `webconsole.handle.acquired/released` events are owner/process clues for recovery diagnostics, `session.md`, and checkpoints, not a second authority.
- `internal/webconsole/service.go` `activeHandleOwner` uses the current in-memory handle first, then falls back to `latestActiveOwnerFromEvents`.
- Before this slice, `latestActiveOwnerFromEvents` copied process owner fields from the latest `webconsole.handle.acquired` or `webconsole.handle.released` event but did not record which event type it saw.
- `activeHandleOwner` unconditionally converted any owner clue on a durable `running` session into `state=running_not_owned`.
- Therefore a session whose latest owner event was `webconsole.handle.released` could still show the old `process_start_id` as if another Web process might currently own the handle, even though the durable clue explicitly says the handle was released.

Impact:

Session detail could mislead the Web UI/operator into treating a stale released handle clue as an external owner for a still-running durable state. This weakens recovery diagnostics and the stop/interrupt guidance around sessions whose Web runner already settled or released ownership.

Minimal fix:

- Preserve the latest handle event type as internal-only owner metadata.
- If the latest event is `webconsole.handle.released` and no current-process handle exists, report the owner state as `settled` rather than `running_not_owned`.
- Add a focused Web service regression that appends acquired then released owner events on a durable running session and verifies session detail reports a settled owner clue.

Validation:

- Focused Web owner reporting regression.
- Standard grouped validation before commit.

### FCA-20260526-056: Persisted provider replay can serialize malformed tool arguments

Severity: Medium

Evidence:

- `spec/03-provider-contracts.md` says provider-native replay facts belong to provider adapters, and Anthropic / Google replay must preserve native `tool_use` / `functionCall` shapes while maintaining provider/tool protocol integrity.
- `internal/provider/tool_args.go` already normalizes provider-emitted tool-call arguments on ingress and rejects empty, invalid, or non-object JSON.
- Before this slice, `internal/provider/anthropic.go` `anthropicProviderContent` ignored `json.Unmarshal(block.Input, &input)` errors when reconstructing persisted provider `tool_use` replay blocks.
- Before this slice, `internal/provider/google.go` `googleProviderParts` ignored `json.Unmarshal(block.Args, &args)` errors when reconstructing persisted provider `functionCall` replay parts.
- The fallback replay paths for persisted `session.ToolCall.Arguments` in `anthropicMessages` and `googleContents` used the same unchecked unmarshal pattern.
- Therefore a malformed or corrupted persisted replay block could be serialized back to Anthropic or Gemini with `input:null` / `args:null`, or with a non-object value, instead of failing as a provider parse/protocol error before the outbound request.

Impact:

Corrupted or manually edited session facts could weaken adapter-owned replay integrity and send malformed tool-call history to providers. That can produce confusing upstream request failures, lose the local error class, or hide the exact corrupted replay fact behind a provider rejection.

Minimal fix:

- Reuse the existing provider tool-argument normalization for outbound replay reconstruction.
- Have Anthropic and Google replay helpers return `response_parse_error` failures when persisted provider blocks or fallback tool calls contain empty, invalid, or non-object JSON arguments.
- Stop before the provider HTTP request when replay history is malformed.
- Add focused provider regressions for malformed persisted provider-native blocks and fallback tool-call arguments.

Validation:

- Focused provider replay regression.
- Standard grouped validation before commit.

### FCA-20260526-057: OpenAI replay trusts persisted function-call arguments

Severity: Medium

Evidence:

- `spec/03-provider-contracts.md` says OpenAI Responses replay and tool-format differences belong in the OpenAI adapter, and tool-call arguments are part of the provider/tool protocol boundary.
- Prior provider fixes normalized OpenAI Responses `function_call.arguments` on ingress and rejected malformed or non-object JSON before constructing internal `ToolCall` values.
- Before this slice, `internal/provider/openai.go` `openAIInput` reconstructed persisted assistant `function_call` replay items with `"arguments": string(call.Arguments)` and did not revalidate the persisted `session.ToolCall.Arguments`.
- Therefore a corrupted or manually edited `messages.jsonl` assistant tool call could be replayed to Responses with invalid JSON text or a non-object JSON value, while Anthropic and Google replay paths already returned adapter parse errors for the same persisted-state corruption class.

Impact:

Malformed persisted OpenAI replay facts could be sent to the provider as invalid Responses history instead of failing locally as a `response_parse_error`. This weakens provider replay diagnostics and makes the behavior inconsistent across adapter families after FCA-20260526-056.

Minimal fix:

- Validate persisted OpenAI replay `session.ToolCall.Arguments` with the same JSON object contract used for provider-emitted tool calls.
- Return an OpenAI `response_parse_error` from `openAIInput` before the HTTP request if persisted function-call arguments are empty, invalid, or non-object JSON.
- Add focused OpenAI replay regressions for invalid JSON and non-object persisted arguments.

Validation:

- Focused OpenAI replay regression.
- Standard grouped validation before commit.

### FCA-20260526-058: Session tree deletion skips root-linked descendants

Severity: Medium

Evidence:

- `spec/14-multi-agent-and-isolation.md` requires every child session to record `root_session_id`, and `spec/15-background-queue.md` records the same root fact on queue jobs.
- `internal/webconsole/service.go` computes Web delete preflight tree membership with both `ParentSessionID` and `RootSessionID` via `sessionTreeTargetIDs`, so the Web layer treats root-linked sessions as part of the affected tree.
- Before this slice, `internal/session/store.go` `DeleteSessionTree` only expanded `targets` through `ParentSessionID`.
- The same helper then deleted queue jobs whose `RootSessionID` matched the target tree, so a root-linked descendant with a missing or drifted parent chain could be left as an orphaned session while its related job was deleted.

Impact:

Deleting a root session tree could leave durable descendant session facts behind when the descendant still carried the correct root fact but lacked a traversable parent chain. That makes Web/session history inconsistent after a destructive cleanup and weakens recovery assumptions that session, child, and queue facts are removed together.

Minimal fix:

- Make `DeleteSessionTree` expand tree targets through both `ParentSessionID` and `RootSessionID`, matching the Web preflight helper and existing queue-job cleanup semantics.
- Add a store regression for a root-linked descendant with no parent chain and a root-linked queue job.

Validation:

- Focused store deletion regression.
- Standard grouped validation before commit.

### FCA-20260526-059: Mission plan approval creates approved empty missions for plain goals

Severity: Medium

Evidence:

- `spec/00-product.md`, `spec/09-phase-plan.md`, `spec/11-spec-audit-and-traceability.md`, and `spec/17-web-console.md` describe mission plan approval as approval of an existing Goal/Mission internal plan, with linked Plan Mode as the execution gate when approval is required.
- Before this slice, `internal/session/goal.go` `ApproveMissionPlan` created a `MissionPlan{PlanStatus: draft}` when `goal.Mission == nil`, then immediately marked it `approved`.
- CLI `goal plan approve` and Web `POST /api/sessions/{id}/mission/plan/approve` both fell through to that store helper when a plain goal did not require linked Plan Mode approval, so both control surfaces could mutate a normal `mode=goal` snapshot into `mode=mission` with an approved empty mission plan.
- Focused regressions reproduced the behavior: the CLI returned an approved mission JSON for a plain goal, and the Web endpoint returned `200` with an approved empty mission instead of rejecting the missing mission plan.

Impact:

Goal plan approval could create false durable approval facts without any mission plan, validation contract, or linked Plan Mode gate. This weakens `goal.json` / `goal-history.jsonl` as mission approval facts and lets CLI/Web controls imply plan approval for work the agent never proposed.

Minimal fix:

- Make the session store reject `ApproveMissionPlan` when the current goal has no existing mission plan.
- Keep approval of existing mission plans and linked Plan Mode synchronization unchanged.
- Add store, CLI, and Web regressions proving plain-goal approval is rejected and does not mutate `goal.json` or append `mission.plan.approved` history.

Validation:

- Focused store, CLI, Web, and runtime linked Plan Mode regressions.
- Standard grouped validation before commit.

### FCA-20260526-060: Plan Mode transitions hide history append failures

Severity: Medium

Evidence:

- `spec/00-product.md`, `spec/01-runtime-architecture.md`, `spec/09-phase-plan.md`, and `spec/11-spec-audit-and-traceability.md` define `planmode.json` and `artifacts/planmode-history.jsonl` as durable Plan Mode facts, with planning, approval, revision, cancellation, input, and execution transitions recorded in history.
- Before this slice, `internal/session/planmode.go` saved `planmode.json` and then ignored `AppendPlanModeHistory` errors in `CreatePlanMode`, `SubmitPlanMode`, `SetPlanModePendingRequest`, `AnswerPlanModeInput`, `ApprovePlanMode`, `MarkPlanModeExecuting`, `RevisePlanMode`, and `CancelPlanMode`.
- `internal/session/goal.go` `EnsurePlanModeForGoal` also ignored `planmode.linked_goal` history append errors after saving a linked pending Plan Mode.
- Focused regressions reproduced the failure by replacing `artifacts/planmode-history.jsonl` with a directory: `SubmitPlanMode` and `ApprovePlanMode` returned success after mutating `planmode.json`, while the required history append failed.

Impact:

Plan Mode control surfaces could report a successful submitted or approved plan while the durable transition history was missing. That weakens recovery, Web inspector timelines, and provider replay diagnostics for the explicit planning gate because the current snapshot and history facts can diverge without any caller-visible error.

Minimal fix:

- Propagate `AppendPlanModeHistory` errors from all Plan Mode store transition helpers instead of swallowing them.
- Add path context to `AppendPlanModeHistory` errors so callers can identify the failed durable history file.
- Add focused store regressions for submitted and approved transitions with a blocked history path.

Validation:

- Focused Plan Mode store, runtime, and Web regressions.
- Standard grouped validation before commit.

### FCA-20260526-061: Goal transitions hide history append failures

Severity: Medium

Evidence:

- `spec/00-product.md`, `spec/01-runtime-architecture.md`, `spec/09-phase-plan.md`, and `spec/11-spec-audit-and-traceability.md` define `goal.json` and `artifacts/goal-history.jsonl` as durable Goal facts, including completion evidence, usage accounting, mission approval, progress, validation, and budget wrap-up facts.
- Before this slice, `internal/session/goal.go` ignored `AppendGoalHistory` errors in `CompleteGoal`, `ApproveMissionPlan`, and `UpdateGoalAccounting`; `internal/session/goal_progress.go` ignored the same errors in `RecordGoalProgress`.
- Focused regressions reproduced the failure by replacing `artifacts/goal-history.jsonl` with a directory: accounting, completion, and progress helpers returned success after mutating `goal.json`, while their required history append failed.
- Web and CLI already propagate errors returned by these store helpers on their normal control paths, so the missing invariant was at the session store boundary.

Impact:

Goal control surfaces and runtime budget/progress paths could report successful state changes while losing the corresponding durable history fact. This weakens recovery, `session.md`/checkpoint auditability, and Goal completion/progress traceability because the current goal snapshot and history stream can diverge silently.

Minimal fix:

- Propagate `AppendGoalHistory` errors from Goal store transitions for completion, mission plan approval, accounting, budget-limited/budget-wrap-up history, and structured progress/mission validation updates.
- Add file-path context to `AppendGoalHistory` errors so callers can identify the failed durable history file.
- Add focused store regressions for blocked history during accounting, completion, and progress recording.

Validation:

- Focused Goal store, CLI, Web, and runtime regressions.
- Standard grouped validation before commit.

### FCA-20260526-062: Plan input cancellation hides input-cancelled history failures

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` and `spec/11-spec-audit-and-traceability.md` require Plan Mode input/cancellation transitions to be durable in `planmode.json`, `artifacts/planmode-history.jsonl`, and replayable tool results.
- Before this slice, `internal/runtime/runner.go` `appendPlanInputCancelToolResult` appended the replay-critical `request_user_input` tool result and then ignored `AppendPlanModeHistory` errors for the matching `planmode.input_cancelled` fact.
- Focused regression reproduced the failure by replacing `artifacts/planmode-history.jsonl` with a directory: `appendPlanInputCancelToolResult` returned success after writing the tool result while losing the `planmode.input_cancelled` history fact.
- This was not covered by FCA-20260526-060 because that slice fixed store-owned Plan Mode transitions; this recovery helper writes an extra runtime-owned input-cancelled history fact before `CancelPlanMode`.

Impact:

Recovered Plan Mode cancellation could become replay-complete for the provider while losing the durable input-cancelled history fact. That leaves operators and recovery logic with a cancelled plan but no history record tying the pending `request_user_input` tool call to the synthetic cancellation result.

Minimal fix:

- Return `AppendPlanModeHistory` errors from `appendPlanInputCancelToolResult` instead of ignoring them.
- Add a runtime regression for blocked `planmode-history.jsonl` during recovered Plan Mode input cancellation.

Validation:

- Focused runtime and Web Plan Mode cancellation/approval regressions.
- Standard grouped validation before commit.

### FCA-20260526-067: Runtime provider attempts hide ledger append failures

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` defines `provider-attempts.jsonl` as the durable provider retry, auto-resume, final failure, and success ledger for recovery, diagnostics, and Web display; `spec/03-provider-contracts.md` repeats that runtime appends retry/auto-resume/failure/success facts there.
- Before this slice, `internal/runtime/provider_attempts.go` ignored every `AppendProviderAttempt` error for retry, auto-resume, final failure, and success attempts.
- Focused regressions reproduced the inconsistency by replacing `provider-attempts.jsonl` with a directory: retry and success paths continued to `awaiting_input`, auto-resume recalled the provider, and final provider failure returned only the upstream provider error while losing the durable ledger write failure.
- `internal/session/store.go` also returned raw append errors for provider attempts without the failed `provider-attempts.jsonl` path context, unlike the Goal, Plan Mode, and contract history append diagnostics.

Impact:

Operators using WebConsole or recovery artifacts could see provider events, state transitions, assistant output, or auto-resume behavior without the durable provider-attempt facts required to explain retry count, timeout class, status code, response id, and cache telemetry. This weakens recovery diagnostics and can make `session.md` / checkpoint summaries diverge from runtime behavior.

Minimal fix:

- Return provider-attempt append errors from retry, auto-resume, failure, and success ledger helpers.
- Stop the provider loop through the existing failure path when the ledger cannot be written, before assistant persistence or auto-resume continuation.
- Add file-path context to `AppendProviderAttempt` errors.
- Add focused runtime regressions for blocked `provider-attempts.jsonl` on retry, auto-resume, final failure, and success paths.

Validation:

- Focused provider-attempt ledger failure regressions.
- Adjacent provider metadata, parse-error, auto-resume, and summary/checkpoint regressions.
- Standard grouped validation before commit.

### FCA-20260526-068: Budget wrap-up turn start hides goal history failures

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` and `spec/11-spec-audit-and-traceability.md` make `goal.json` plus `artifacts/goal-history.jsonl` the durable goal fact source, and budget-limited goals require durable wrap-up facts before recovery or finish.
- After `UpdateGoalAccounting` marks a stop-on-budget goal as `budget_limited`, the engine marks `BudgetWrapUpTurnStartedAt` in `goal.json` before the provider turn that may record the budget wrap-up.
- Before this slice, that engine path ignored the corresponding `goal.budget_wrapup_turn_started` history append failure and still called the provider.
- A focused regression reproduced the inconsistency by replacing `artifacts/goal-history.jsonl` with a directory after budget exhaustion: the engine reached the provider with `goal.json` mutated but the required turn-start history fact missing.

Impact:

Recovery and operators could see a goal snapshot that says the budget wrap-up turn had started without the matching append-only history fact that explains when and why runtime entered the wrap-up turn. That weakens budget handoff traceability and can make later recovery prompts depend on a snapshot/history split.

Minimal fix:

- Return the `AppendGoalHistory` error from the budget wrap-up turn-start path.
- Stop through the existing session failure path before emitting the turn-start event or calling the provider.
- Add a focused runtime regression for blocked `goal-history.jsonl` during budget wrap-up turn start.

Validation:

- Focused budget wrap-up turn-start failure regression.
- Adjacent budget wrap-up and completion-gate regressions.
- Standard grouped validation before commit.

### FCA-20260526-069: Required-artifact tracking hides durable update failures

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md`, `spec/12-task-system.md`, and `spec/18-durable-contract-and-completion.md` make explicit required-artifact completion depend on `artifact-tracker.json`, `contract.json`, and `CompletionController`.
- Before this slice, `CompletionController.TrackToolResult` had no error return even though it updated `artifact-tracker.json` after successful `write_file` / `edit_file`, and it silently ignored `contract.json` sync failures.
- A focused controller regression showed that replacing `artifact-tracker.json` with a directory made tracking return no error even after a successful write/edit should have updated the required-artifact fact.
- A focused engine regression covered the post-side-effect case: after `write_file` successfully wrote `reports/final.md`, the blocked tracker path needed a replay-complete error tool result before the session failed, so provider replay would not be left with an assistant tool call and no matching tool result.

Impact:

The model and operator could see a successful file-write tool result while the required-artifact gate fact was not updated. Later `finish` attempts could still be blocked as stale, or recovery could see file side effects without the durable tracker evidence explaining that the session touched the required artifact.

Minimal fix:

- Make `TrackToolResult` return artifact tracker and contract sync errors.
- Add path context to `SaveArtifactTracker` errors.
- In the engine, stamp tool result id/name before artifact tracking and, on tracking failure, append a replay-complete error tool result plus synthetic skipped results for remaining same-turn calls before failing the session.
- Add focused controller and engine regressions for blocked `artifact-tracker.json`.

Validation:

- Focused artifact-tracking failure regressions.
- Existing required-artifact gate and contract freshness regressions.
- Standard grouped validation before commit.

### FCA-20260526-070: Required-artifact finish gate bypasses state refresh failures

Severity: Medium

Evidence:

- `spec/18-durable-contract-and-completion.md` requires the generic required-artifact gate to distinguish current presence, touched-by-session, and changed-from-baseline using `artifact-tracker.json`.
- Before this slice, `requiredArtifactGate("finish")` refreshed artifact status in memory, then ignored failures loading/saving `artifact-tracker.json` and ignored `contract.json` sync failures.
- A focused regression reproduced a false allow by replacing `artifact-tracker.json` with a directory after a valid required-artifact write: `EvaluateToolCall("finish")` returned `allow` even though the gate could not load or persist the refreshed durable artifact state.

Impact:

The model could complete a session while the required-artifact gate could not write the refreshed fact source used by WebConsole, `session.md`, checkpoints, and recovery. That makes finish decisions depend on transient in-memory status while the durable tracker/contract state remains unreadable or stale.

Minimal fix:

- Treat artifact tracker load failures as `required_artifact_state` blocks instead of no-op gate absence.
- Treat artifact tracker save failures and contract sync failures during finish-gate refresh as `required_artifact_state` blocks.
- Add a focused finish-gate regression for blocked `artifact-tracker.json`.

Validation:

- Focused finish-gate tracker refresh regression.
- Existing required-artifact tracking and contract freshness regressions.
- Standard grouped validation before commit.

### FCA-20260526-071: CLI mission approval hides event append failures

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` keeps `events.jsonl` as a session fact source, and the CLI fallback is part of the Web-first recovery/control surface.
- Before this slice, `goal plan approve` updated `goal.json` and appended `mission.plan.approved` goal history through `ApproveMissionPlan`, then ignored the matching `events.jsonl` append failure.
- A focused regression replaced `events.jsonl` with a directory: `go-cli-agent goal plan approve <session> --json` returned success JSON while the durable event fact was missing.
- `AppendEvent` returned raw filesystem errors without the failed `events.jsonl` path, unlike the recently hardened goal/history/contract/provider append diagnostics.

Impact:

CLI users and recovery tooling could observe an approved mission plan without the corresponding event fact in the session timeline. That weakens Web/CLI traceability for a user control action and makes failures hard to diagnose because the original error did not identify `events.jsonl`.

Minimal fix:

- Propagate the `AppendEvent` error from CLI mission plan approval.
- Add path context to `AppendEvent` failures.
- Add a focused CLI regression for blocked `events.jsonl` during mission plan approval.

Validation:

- Focused CLI mission-plan approval event failure regression.
- Adjacent CLI goal plan/status/clear regressions.
- Standard grouped validation before commit.

### FCA-20260526-072: Runtime failure paths hide failed-state write failures

Severity: High

Evidence:

- `spec/01-runtime-architecture.md` makes `state.json` and `events.jsonl` durable session fact sources, and `spec/17-web-console.md` relies on those facts for session errors and recovery state.
- Before this slice, `Runner.failBeforeRun` set `state.status=failed` after pre-engine errors, but ignored `SaveState` and ignored the `session.failed` event append through `emit`.
- A focused runner regression used a fail-closed user-message hook after `Continue` had durably claimed the session as `running`; the hook replaced `state.json` with a directory, and `Continue` returned only the hook error while the durable state update failure was hidden.
- The direct provider failure path in `Engine.Run` similarly ignored the `SaveState` error and the `session.failed` event append error before recording provider failure attempts.
- Focused engine regressions reproduced both hidden paths: blocking `state.json` after the provider call started still returned the original provider error, and blocking `events.jsonl` still returned the original provider error with no failed event fact.
- The shared `Engine.fail` helper already returned failed-state save errors, but it also emitted `session.failed` through the ignored event path.

Impact:

Operators and recovery tooling could receive a failed run result while the durable session was still `running`, or while the timeline lacked the required `session.failed` event explaining the failure. That can make Web/CLI recovery decisions depend on stale state, hide why a session stopped before the engine ran, and weaken provider-failure diagnostics.

Minimal fix:

- Make `Runner.failBeforeRun` return failed-state write errors and failed-event append errors, preserving the original error in context.
- Make the provider-failure branch in `Engine.Run` return failed-state write errors and failed-event append errors before recording provider failure attempts.
- Make `Engine.fail` append the required `session.failed` event through an error-returning path.
- Keep ordinary event emission best-effort elsewhere; this change is limited to failure transition facts that determine recovery state.
- Add focused runner and engine regressions for blocked `state.json` and `events.jsonl` failure facts.

Validation:

- Focused runner pre-run failure-state/event regressions.
- Focused engine provider-failure state/event regressions.
- Focused shared `Engine.fail` event regression.
- Adjacent provider retry/failure/auto-resume/success regressions.
- Standard grouped validation before commit.

### FCA-20260526-073: Plan Mode input hides awaiting-state write failures

Severity: High

Evidence:

- `spec/01-runtime-architecture.md` and `spec/18-durable-contract-and-completion.md` make `state.json` and `planmode.json` durable session facts, and `request_user_input` must persist the pending request so active Web runners and recovery can complete provider replay.
- `spec/17-web-console.md` requires Web Plan Mode to show and answer pending `request_user_input` questions from the backend facts, not a second browser-owned state source.
- Before this slice, `request_user_input` persisted `planmode.json` as `awaiting_user_input`, then ignored `LoadState` errors and ignored `SaveState` errors while trying to mark `state.json` as `awaiting_input` / `plan_input`.
- Focused tool regressions replaced `state.json` with a directory and replaced `control/steer.jsonl` with a directory to force `LoadState` and `SaveState` failures. In both cases the tool still called the Plan Mode responder and returned a successful answer payload.

Impact:

A live Plan Mode runner could block on or consume interactive input while the durable session state was unreadable or still `running`. Web polling, CLI recovery, and restart fallback could then disagree about whether the session is waiting for input, making the pending planning decision depend on an in-memory handle instead of the session file facts.

Minimal fix:

- Return a model-visible `request_user_input` error when `state.json` cannot be loaded.
- Return a model-visible `request_user_input` error when the `awaiting_input` / `plan_input` state save fails.
- Do not emit `planmode.input_requested` or call the interactive responder until both the pending request and the awaiting-input session state are durable.
- Preserve the already-written pending Plan Mode request as a recovery fact when the later state transition fails.
- Add focused tool regressions for state load and state save failure paths.

Validation:

- Focused `request_user_input` state load/save failure regressions.
- Adjacent Plan Mode input responder and no-responder regressions.
- Standard grouped validation before commit.

### FCA-20260526-074: Skill upload lacks pending-submit state

Severity: Medium

Evidence:

- `spec/17-web-console.md` requires form submit controls to enter a clear pending / disabled state, and specifically requires skills upload / uninstall and settings save failures to use backend errors and restore pending button state.
- Skill uninstall and settings save already disable their action buttons and restore them on failure.
- Before this slice, the `skill-upload` change handler immediately sent the multipart request and only cleared the file input after completion. It did not set an in-flight flag, disable the header upload button, disable empty-state / card upload entry points, or disable the hidden file input while the request was pending.
- Focused frontend regressions failed before the fix: the renderer test had no `setSkillUploadPending` helper, and the embedded asset contract found no `state.skillUploadInFlight` or `setSkillUploadPending` usage in `app.js`.

Impact:

Operators could trigger repeated skill upload requests while the first multipart upload was still in flight. Slow uploads or backend errors could leave the UI looking idle even though a local skill install mutation was pending, violating the Web-first form-state contract and increasing the chance of duplicate local mutations.

Minimal fix:

- Add a reusable frontend helper that disables and labels all skill upload entry points plus the hidden file input while upload is pending, then restores their labels and enabled state.
- Track `state.skillUploadInFlight` in `app.js`.
- Guard repeated header/card/empty-state upload clicks and duplicate file-input changes while a request is pending.
- Restore pending controls in `finally` after success or failure, preserving real backend error toasts.
- Add focused Node and embedded asset regressions.

Validation:

- Focused frontend helper regression.
- Focused embedded asset contract regression.
- Standard grouped validation before commit.

### FCA-20260526-075: load_skill hides loaded-skill state failures

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` defines `load_skill` idempotency as session-state backed: once a skill is loaded, later calls return the compact `already_loaded` result unless `force_reload=true`.
- FCA-20260525-032 established `loaded_skills` as a monotonic durable session fact used by idempotency, session summary, compaction, and operator recovery context.
- Before this slice, `load_skill` loaded the full skill body and then called `markSkillLoaded`, but that helper ignored `LoadState` and `SaveState` failures.
- A focused tool regression blocked `control/steer.jsonl` so `SaveState` failed while recording the loaded skill. The tool still returned the full skill body and success metadata, leaving `state.json` without the loaded skill fact.

Impact:

The model could receive full skill instructions while the durable session says no skill was loaded. Later turns, compaction summaries, session summaries, and recovery prompts could miss the loaded-skill context, and repeated `load_skill` calls would re-inject the full body instead of returning the compact already-loaded result.

Minimal fix:

- Make `markSkillLoaded` return `LoadState` and `SaveState` errors when a session store/context is available.
- Make `load_skill` return a model-visible error instead of the full skill body when the loaded-skill state fact cannot be persisted.
- Preserve no-op behavior for registry uses without a session store or session id.
- Add a focused blocked-state regression.

Validation:

- Focused `load_skill` loaded-skill state failure regression.
- Adjacent `load_skill` repeat / force-reload regression.
- Standard grouped validation before commit.

### FCA-20260526-076: queue reconciliation hides linked-session state write failures

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` and `spec/18-durable-contract-and-completion.md` require queue jobs and linked child session state to be durable file facts, with reconciliation repairing stale or terminal queue/session facts only when file facts can prove the transition.
- Before this slice, `reconcileQueueJobSession` called `SaveState` when a failed/completed queue job had to update its linked child `state.json`, but ignored the returned error.
- The same helper ignored `SaveJob` errors while repairing stale/no-session jobs or syncing repaired queue job metadata.
- A focused store regression replaced the linked child `control/steer.jsonl` with a directory so `SaveState` failed through the pending-steer refresh path. `LoadJob` still returned a repaired failed job with `SessionStatus=failed` while the linked child `state.json` remained durably `running`.
- A focused runtime regression blocked the completed queue-job file path during a queued child `finish`; before the fix, `Engine.Run` still returned `completed` even though parent queue facts could not be reconciled.

Impact:

Web, CLI, or runtime queue views could report a repaired terminal queue job even though the linked child session source state was not updated. That leaves parent/background operators with contradictory facts: the queue job appears failed or completed, while session lists and recovery still see the child as running.

Minimal fix:

- Make queue reconciliation return write errors for linked child `SaveState` failures.
- Make queue reconciliation return write errors for repaired queue job `SaveJob` failures.
- Propagate reconciliation errors through `LoadJob`, queue listing, `ListPage`, and `ListChildren` instead of returning repaired in-memory facts.
- Propagate linked queue reconciliation errors from engine terminal transitions after the child session's own state/event facts are durable.
- Keep parent lifecycle event repair best-effort, because those events are diagnostic/reconstruction aids while job/state files remain the source facts.
- Add focused regressions for linked session state save failure and runtime child queue-job reconciliation failure.

Validation:

- Focused linked-session state save failure regression.
- Focused runtime linked queue-job reconciliation failure regression.
- Full `internal/session` regression suite.
- Standard grouped validation before commit.

### FCA-20260526-077: queue parent coordination write failures are hidden

Severity: Medium

Evidence:

- `spec/18-durable-contract-and-completion.md` defines `parent-coordination.json` as the parent session fact source for unresolved child / queue work, and the completion controller blocks parent `finish` from that file.
- `QueueSubmit` enqueued a parent-linked job and then ignored `addParentQueueJob` errors, so the API could return a queued job while `parent-coordination.json` did not record the unresolved queue work.
- `ProcessNextJob` saved a terminal job and background notification, then ignored `resolveParentQueueJob` errors, so the worker could return a completed job while `parent-coordination.json` still listed the job as unresolved.
- Store-level queue repair also ignored `MutateParentCoordination` errors through `reconcileParentQueueJobStatus`, so `LoadJob` / list repair could show terminal queue facts without updating the parent completion gate facts.
- Focused regressions blocked `parent-coordination.json` with a directory. Before the fix, `QueueSubmit` returned a queued job and `ProcessNextJob` returned a completed job instead of reporting the parent coordination write failure.

Impact:

Parent sessions could retain stale unresolved queue work or miss newly submitted parent-linked queue work while Web/CLI/runtime reported the child queue operation as accepted or completed. That makes the parent completion gate and operator recovery view diverge from queue facts.

Minimal fix:

- Return `addParentQueueJob` errors from `SpawnAgent` background queue mode and `QueueSubmit`.
- Return `resolveParentQueueJob` errors from `ProcessNextJob` after terminal job persistence and background notification persistence.
- Make store queue repair return `MutateParentCoordination` failures when syncing terminal or status-changed queue jobs.
- Keep parent coordination transition event append best-effort; the source fact is `parent-coordination.json`.
- Add focused queue submit and queue worker regressions.

Validation:

- Focused queue submit parent coordination error regression.
- Focused queue worker parent coordination error regression.
- Adjacent queue / parent coordination regression group.
- Standard grouped validation before commit.

### FCA-20260526-078: synchronous delegate hides parent child coordination failures

Severity: Medium

Evidence:

- `spec/18-durable-contract-and-completion.md` defines `parent-coordination.json` as the source fact for parent sessions that wait on explicit child work.
- Synchronous `Delegate` ran the child session, emitted `session.child.spawned`, then ignored `addParentChildSession` and `resolveParentChildSession` errors.
- A focused regression blocked the parent `parent-coordination.json` path with a directory. Before the fix, `Delegate` returned a completed child result even though parent coordination could not record or resolve that child work.

Impact:

Parent sessions could report a synchronous child as completed while the parent coordination source fact was missing or stale. That makes the parent completion gate and recovery summary diverge from child execution facts.

Minimal fix:

- Return parent coordination add/resolve errors from synchronous delegate after a child session id is known.
- Preserve the original child runner error if the child itself failed and a later best-effort coordination attempt also fails.
- Keep parent transition event appends best-effort; the source fact is `parent-coordination.json`.
- Add a focused delegate regression for blocked parent coordination writes.

Validation:

- Focused synchronous delegate parent coordination error regression.
- Adjacent synchronous delegate regression group.
- Standard grouped validation before commit.

### FCA-20260526-079: provider cancellation can lose required durable event

Severity: Medium

Evidence:

- `spec/13-live-input-and-steering.md` requires provider preemption to leave a searchable durable `provider.cancelled` event.
- `Engine.Run` handled provider-call cancellation for pause / interrupt steer through the best-effort `emit` helper, so an `events.jsonl` write failure did not fail the run.
- A focused regression blocked `events.jsonl` with a directory during interrupt steer cancellation. Before the fix, `Engine.Run` returned `awaiting_input` after accepting the steer path even though `provider.cancelled` was not persisted.

Impact:

Interrupted provider turns could appear to have accepted live steering without the required durable cancellation evidence. Replay, recovery, and Web-first operator timelines would not be able to prove that the provider turn was actually preempted.

Minimal fix:

- Use the error-returning event append path for `provider.cancelled` in provider pause and interrupt-steer cancellation branches.
- Keep unrelated provider retry / auto-resume timeline emits unchanged in this slice.
- Add a focused provider cancellation event append regression.

Validation:

- Focused provider cancellation event append failure regression.
- Adjacent interrupt-steer cancellation and defer regression group.
- Standard grouped validation before commit.

### FCA-20260526-080: provider auto-resume can lose required durable event

Severity: Medium

Evidence:

- `spec/03-provider-contracts.md` requires provider auto-resume after `upstream_timeout` to leave `provider.auto_resume` evidence.
- The provider-attempt ledger path was already hardened, but `Engine.Run` still emitted `provider.auto_resume` through the best-effort `emit` helper.
- A focused regression blocked `events.jsonl` with a directory after the first timeout. Before the fix, `Engine.Run` recalled the provider and completed the session even though the required `provider.auto_resume` event was not persisted.

Impact:

Sessions could auto-resume after provider timeout without the required durable timeline event. Recovery and Web-first operator views would show later progress without searchable evidence explaining why the provider was retried by runtime auto-resume.

Minimal fix:

- Use the error-returning event append path for `provider.auto_resume`.
- Stop before appending the auto-resume harness reminder or recalling the provider if the event cannot be persisted.
- Keep the already-hardened `provider-attempts.jsonl` ledger behavior unchanged.
- Add a focused auto-resume event append regression.

Validation:

- Focused provider auto-resume event append failure regression.
- Adjacent provider auto-resume ledger and happy-path regression group.
- Standard grouped validation before commit.

### FCA-20260526-081: provider retry can lose required durable event

Severity: Medium

Evidence:

- `spec/03-provider-contracts.md` requires provider transport retry after `429` / `5xx` / transport timeout to leave `provider.retry` evidence.
- The provider-attempt retry ledger path was already hardened, but the adapter event callback still emitted `provider.retry` through the best-effort `emit` helper before writing the ledger.
- A focused regression blocked `events.jsonl` with a directory before the adapter emitted `provider.retry`. Before the fix, `Engine.Run` returned `awaiting_input` with assistant text even though the required retry event was not persisted.

Impact:

Provider retries could occur without the required durable timeline event. Web-first retry proof and recovery diagnostics could show later progress while missing the searchable event evidence that the upstream call had been retried.

Minimal fix:

- Use the error-returning event append path for `provider.retry` in the provider callback.
- Stop the provider turn before assistant persistence if the required retry event cannot be written.
- Keep non-retry provider callback events best-effort in this slice.
- Add a focused provider retry event append regression.

Validation:

- Focused provider retry event append failure regression.
- Adjacent provider retry ledger, parse-failure, and auto-resume regression group.
- Standard grouped validation before commit.

### FCA-20260526-082: steer submission can report queued without required durable events

Severity: Medium

Evidence:

- `spec/13-live-input-and-steering.md` says each steer produces `session.steer.requested`, `session.steer.queued`, and `session.steer.accepted` events, while `spec/17-web-console.md` expects the running-session submit path to show `session.steer.queued` in the timeline after submission.
- `Runner.Steer` appended the durable `control/steer.jsonl` request, refreshed the pending counter, and then emitted `session.steer.requested` / `session.steer.queued` through the best-effort `emit` helper.
- A focused regression blocked `events.jsonl` with a directory before `Runner.Steer` emitted the submission events. Before the fix, `Runner.Steer` returned accepted `queued`, left the steer pending, and the required timeline evidence could not be persisted.

Impact:

CLI and Web could report a steer as queued while the Web-first timeline was missing the spec-required queued evidence. Because the control fact was already pending, a failed caller response could still leave the live runner to consume the steer, creating confusing partial success.

Minimal fix:

- Append `session.steer.requested` and `session.steer.queued` through the error-returning event path in `Runner.Steer`.
- If either required submission event cannot persist after the control record was appended, mark that steer request `rejected` and refresh `pending_steer_count` before returning the append error.
- Keep later accepted/deferred/interrupted runtime events out of this slice unless a separate focused proof shows source-fact drift or false success.
- Add a focused blocked-`events.jsonl` regression for the queued submission path.

Validation:

- Focused steer queued event append failure regression.
- Adjacent normal steer submission regression.
- Standard grouped validation before commit.

### FCA-20260526-083: Plan Mode history failures can leave advanced snapshots

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` and `spec/18-durable-contract-and-completion.md` define `planmode.json`, `artifacts/planmode-history.jsonl`, and the submitted plan Markdown as durable Plan Mode facts.
- FCA-20260526-060 already made Plan Mode transition helpers return `AppendPlanModeHistory` errors, but those helpers still mutated `planmode.json` before appending the required history fact.
- A focused regression blocked `artifacts/planmode-history.jsonl` during `SubmitPlanMode`. Before this fix, the helper returned a history append error but left `planmode.json` advanced to `awaiting_approval` with `plan_version=1` and left the submitted `artifacts/planmode-plan.md` behind.
- The same mutation-before-history shape existed for approval, execution, input request/answer, revision, cancellation, creation, and linked-goal relink transitions.

Impact:

Recovery, Web detail, and provider prompt construction could observe an advanced current Plan Mode snapshot even though the append-only transition history was missing the matching fact. Operators would see an error, but subsequent reads could still treat the failed transition as current state.

Minimal fix:

- Snapshot the previous Plan Mode current facts before transitions that require history.
- If the required history append fails after a current snapshot mutation, restore the previous `planmode.json`; for submit, also restore or remove the derived `artifacts/planmode-plan.md`.
- Apply the same rollback behavior to created Plan Mode snapshots and goal-linked pending Plan Mode relinks.
- Extend focused blocked-history regressions to assert failed submit and approval do not advance current snapshots.

Validation:

- Focused Plan Mode submit and approval history-failure regressions.
- Adjacent linked-goal Plan Mode relink regression.
- Standard grouped validation before commit.

### FCA-20260526-084: Goal history failures can leave advanced snapshots

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` and `spec/18-durable-contract-and-completion.md` define `goal.json` plus `artifacts/goal-history.jsonl` as durable Goal facts.
- FCA-20260526-061 already made Goal transition helpers return `AppendGoalHistory` errors, but helpers still mutated `goal.json` before appending the required history fact.
- Focused regressions blocked `artifacts/goal-history.jsonl`. Before this fix, `UpdateGoalAccounting` returned an error while leaving `tokens_used` advanced, and `CompleteGoal` returned an error while leaving `status=complete` plus completion audit in `goal.json`.
- The same mutation-before-history shape existed for mission plan approval and structured progress recording.

Impact:

Recovery, Web Mission Control, completion gates, and provider prompt construction could observe an advanced current Goal snapshot even though the append-only transition history was missing the matching fact. Operators would see an error, but later reads could still treat the failed transition as current state.

Minimal fix:

- Snapshot the previous Goal current facts before history-backed store transitions.
- If any required Goal history append fails after a current snapshot mutation, restore the previous `goal.json` before returning the append error.
- Apply rollback to completion, mission plan approval, accounting / budget-limited / budget-wrap-up history, and structured progress / mission plan / mission validation updates.
- Extend focused blocked-history regressions to assert failed transitions do not advance current snapshots.

Validation:

- Focused Goal accounting, completion, mission approval, and progress history-failure regressions.
- Standard grouped validation before commit.

### FCA-20260526-085: budget wrap-up turn history failure can leave advanced goal snapshot

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` and `spec/18-durable-contract-and-completion.md` make `goal.json` plus `artifacts/goal-history.jsonl` the durable Goal fact source for budget-limited recovery and completion gating.
- FCA-20260526-068 made the runtime budget wrap-up turn-start path return `goal.budget_wrapup_turn_started` history append failures before provider execution, but that path still wrote `BudgetWrapUpTurnStartedAt` to `goal.json` before appending history.
- A focused regression blocked `artifacts/goal-history.jsonl`. Before this fix, `Engine.Run` returned the history append error and failed the session, but `goal.json` still had `budget_wrapup_turn_started_at` set.

Impact:

Recovery and completion gating could later observe that a budget wrap-up turn had already started even though the append-only history fact was missing and the provider turn did not run. That could distort budget-limited handoff state after a failed run.

Minimal fix:

- Preserve the previous Goal snapshot around the runtime-owned budget-wrap-up turn-start mutation.
- If the required `goal.budget_wrapup_turn_started` history append fails, restore the previous `goal.json` before failing the session.
- Keep the existing failure-before-provider behavior unchanged.

Validation:

- Focused budget wrap-up turn-start history-failure regression asserting snapshot rollback.
- Adjacent budget wrap-up happy-path and completion-gate regressions.
- Standard grouped validation before commit.

### FCA-20260526-086: CLI goal history failures can leave status or clear mutations applied

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` and `spec/18-durable-contract-and-completion.md` define `goal.json` plus `artifacts/goal-history.jsonl` as durable Goal facts.
- CLI `goal pause` / `goal resume` / `goal complete` call `SetGoalStatus` and then append the required history/event facts in the CLI adapter. CLI `goal clear` removes `goal.json` and then appends `goal.cleared` history/event facts in the adapter.
- Focused regressions blocked `artifacts/goal-history.jsonl`. Before this fix, `goal pause --json` returned the history append error while leaving `goal.json` paused, and `goal clear --json` returned the history append error while leaving `goal.json` removed.

Impact:

CLI operators could receive an error for a missing durable history fact, but later recovery, Web Mission Control, and provider prompt construction would still observe the failed status or clear mutation as current state.

Minimal fix:

- Snapshot the previous Goal before CLI status mutations and restore it if the required history or event append fails.
- Restore the previous Goal if CLI clear cannot persist the required `goal.cleared` history or event.
- Keep the fix scoped to CLI adapter wrappers; Web goal controls are audited separately.

Validation:

- Focused CLI goal status and clear history-failure regressions asserting snapshot rollback.
- Standard grouped validation before commit.

### FCA-20260526-087: Web goal history failures can leave status or clear mutations applied

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` and `spec/18-durable-contract-and-completion.md` define `goal.json` plus `artifacts/goal-history.jsonl` as durable Goal facts.
- Web `handleGoalStatus` called `SetGoalStatus` before appending the required Web goal history/event facts through `appendGoalMutation`. Web `handleGoalClear` removed `goal.json` before appending `goal.cleared` history/event facts.
- Focused HTTP regressions blocked `artifacts/goal-history.jsonl`. Before this fix, Web `POST /api/sessions/{id}/goal/pause` returned an internal server error while leaving `goal.json` paused, and Web `DELETE /api/sessions/{id}/goal` returned an internal server error while leaving `goal.json` removed.

Impact:

The local Web console could report that a required durable history fact failed while later refreshes, recovery, provider prompt construction, and Mission Control panels observed the failed status or clear mutation as current state.

Minimal fix:

- Snapshot the previous Goal before Web status mutations and restore it if the required Web goal mutation history/event append fails.
- Restore the previous Goal if Web clear cannot persist the required `goal.cleared` history or event.
- Keep the fix scoped to Web goal status/clear handlers; mission plan and validation patch handlers require separate proof before promotion.

Validation:

- Focused Web goal status and clear history-failure regressions asserting snapshot rollback.
- Standard grouped validation before commit.

### FCA-20260526-088: Web goal patch history failures can leave simple goal updates applied

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` and `spec/18-durable-contract-and-completion.md` define `goal.json` plus `artifacts/goal-history.jsonl` as durable Goal facts.
- Web `handleGoalPatch` called `PatchGoal` before appending the required `goal.updated` history/event facts through `appendGoalMutation`.
- A focused HTTP regression blocked `artifacts/goal-history.jsonl` for a non-mission success-criteria patch. Before this fix, Web `PATCH /api/sessions/{id}/goal` returned an internal server error while leaving the new success criterion in `goal.json`.

Impact:

The Web console could report that a required `goal.updated` durable history fact failed while later refreshes, recovery, provider prompt construction, and Mission Control panels observed the rejected simple goal patch as current state.

Minimal fix:

- Restore the previous Goal snapshot when Web goal patch history/event append fails and the patch did not create tasks or a linked Plan Mode side fact.
- Track linked Plan Mode creation in the handler so the simple rollback path does not leave a newly-created `planmode.json` orphaned from a restored `goal.json`.
- Keep mission patches that create tasks or linked Plan Mode state out of this slice; those require separate proof and a full side-fact rollback design.

Validation:

- Focused Web goal-patch history-failure regression asserting snapshot rollback.
- Adjacent Web goal-patch and mission-gate regressions.
- Standard grouped validation before commit.

### FCA-20260526-089: Web validation-plan history failures can leave updates applied

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` and `spec/18-durable-contract-and-completion.md` define `goal.json` plus `artifacts/goal-history.jsonl` as durable Goal facts.
- Web `handleMissionValidationPatch` saved `goal.json` before appending the required `mission.validation.updated` history/event facts through `appendGoalMutation`.
- A focused HTTP regression blocked `artifacts/goal-history.jsonl` for a `validation_plan`-only patch. Before this fix, Web `PATCH /api/sessions/{id}/mission/validation` returned an internal server error while leaving the new validation plan entry in `goal.json`.

Impact:

The Web console could report that a required `mission.validation.updated` durable history fact failed while later refreshes, recovery, provider prompt construction, and Mission Control panels observed the rejected validation-plan patch as current state.

Minimal fix:

- Snapshot the previous Goal before Web mission validation patch writes.
- Restore the previous Goal when `mission.validation.updated` history/event append fails and the patch did not create a linked Plan Mode side fact.
- Keep validation-contract patches that create linked Plan Mode state out of this slice; those require separate proof and side-fact rollback.

Validation:

- Focused Web validation-plan history-failure regression asserting snapshot rollback.
- Adjacent Web validation-contract approval-reset regression.
- Standard grouped validation before commit.

### FCA-20260526-090: Web mission-plan history failures can leave simple updates applied

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` and `spec/18-durable-contract-and-completion.md` define `goal.json` plus `artifacts/goal-history.jsonl` as durable Goal facts.
- Web `handleMissionPlanPatch` patched `goal.json` before appending the required `mission.plan.updated` history/event facts through `appendGoalMutation`.
- A focused HTTP regression blocked `artifacts/goal-history.jsonl` for a feature-only mission-plan patch that did not create tasks or linked Plan Mode. Before this fix, Web `PATCH /api/sessions/{id}/mission/plan` returned an internal server error while leaving the new feature in `goal.json`.

Impact:

The Web console could report that a required `mission.plan.updated` durable history fact failed while later refreshes, recovery, provider prompt construction, and Mission Control panels observed the rejected mission-plan patch as current state.

Minimal fix:

- Restore the previous Goal snapshot when Web mission-plan history/event append fails and the patch did not create tasks or a linked Plan Mode side fact.
- Keep mission-plan patches that create tasks or linked Plan Mode state out of this slice; those require separate proof and side-fact rollback.

Validation:

- Focused Web mission-plan history-failure regression asserting snapshot rollback.
- Adjacent Web mission-plan task-sync, approval-reset, and no-op approval regressions.
- Standard grouped validation before commit.

### FCA-20260526-091: Web mission-plan task sync failures can orphan tasks

Severity: Medium

Evidence:

- `spec/12-task-system.md` defines durable `tasks/task_*.json` files, while `spec/01-runtime-architecture.md` and `spec/18-durable-contract-and-completion.md` make Goal facts durable through `goal.json` plus `artifacts/goal-history.jsonl`.
- Web `handleMissionPlanPatch` can call `SyncMissionPlanTasks`, which creates task files and writes task IDs into `goal.json`, before appending the required `mission.plan.updated` history/event facts.
- A focused HTTP regression blocked `artifacts/goal-history.jsonl` for a mission-plan patch with `create_tasks_from_plan=true`. Before this fix, Web `PATCH /api/sessions/{id}/mission/plan` returned an internal server error while leaving the patched mission feature, `create_tasks_from_plan`, the generated `task_id`, and the new task file persisted.
- Existing `SaveTasks` rewrote provided task files but did not remove stale task files, so restoring a pre-mutation task snapshot would not delete orphan task files.

Impact:

The Web console could report that a required `mission.plan.updated` durable history fact failed while later refreshes and recovery observed both the rejected mission patch and generated durable task graph entries as current state.

Minimal fix:

- Snapshot the task list before Web mission-plan task sync.
- On `mission.plan.updated` history/event failure with no newly-created linked Plan Mode, restore both the exact previous task set and previous Goal snapshot.
- Make `SaveTasks` persist the provided task set exactly by deleting stale `task_*.json` files that are no longer present in the snapshot.
- Keep newly-created linked Plan Mode rollback out of this slice; that requires separate Plan Mode side-fact handling.

Validation:

- Focused Web task-sync history-failure regression asserting both Goal rollback and task cleanup.
- Focused store regression proving `SaveTasks` removes stale task files.
- Adjacent Web mission-plan task-sync and simple rollback regressions.
- Standard grouped validation before commit.

### FCA-20260526-092: Web mission-plan approval gate failures can orphan Plan Mode state

Severity: Medium

Evidence:

- `spec/12-task-system.md` and `spec/18-durable-contract-and-completion.md` make Plan Mode and Goal state durable facts. Goal mutations are recorded through `goal.json` plus `artifacts/goal-history.jsonl`, and Plan Mode has its own durable snapshot, history, and plan Markdown artifact.
- Web `handleMissionPlanPatch` can call `EnsurePlanModeForGoal`, which creates or replaces `planmode.json`, before appending the required `mission.plan.updated` history/event facts.
- A focused HTTP regression blocked `artifacts/goal-history.jsonl` for a mission-plan patch with `plan_status=needs_approval`. Before this fix, Web `PATCH /api/sessions/{id}/mission/plan` returned an internal server error while leaving the patched mission plan and a new linked `planmode.json` persisted.

Impact:

The Web console could report that a required `mission.plan.updated` durable history fact failed while later refreshes, recovery, and Mission Control observed both the rejected mission patch and the newly-created Plan Mode approval gate as current state.

Minimal fix:

- Expose store-level Plan Mode snapshot and restore helpers backed by the existing private Plan Mode rollback machinery.
- Snapshot Plan Mode before Web mission-plan mutations that might create or replace linked Plan Mode state.
- On `mission.plan.updated` history/event failure, restore the previous Plan Mode snapshot, task snapshot when captured, and previous Goal snapshot.

Validation:

- Focused Web plan-mode history-failure regression asserting both Goal rollback and Plan Mode cleanup.
- Focused store regression proving Plan Mode snapshot restore removes a newly-created Plan Mode.
- Adjacent Web mission-plan approval-gate regressions.
- Standard grouped validation before commit.

### FCA-20260526-093: Web generic goal patch gate failures can orphan Plan Mode state

Severity: Medium

Evidence:

- `spec/12-task-system.md` and `spec/18-durable-contract-and-completion.md` make Plan Mode, Task, and Goal state durable facts.
- Web `handleGoalPatch` can patch a mission goal, sync tasks, and call `EnsurePlanModeForGoal` before appending the required `goal.updated` history/event facts.
- A focused HTTP regression blocked `artifacts/goal-history.jsonl` for a generic Goal patch containing a mission plan with `plan_status=needs_approval`. Before this fix, Web `PATCH /api/sessions/{id}/goal` returned an internal server error while leaving the patched mission plan and a new linked `planmode.json` persisted.

Impact:

The Web console could report that a required `goal.updated` durable history fact failed while later refreshes, recovery, and Mission Control observed both the rejected mission patch and the newly-created Plan Mode approval gate as current state.

Minimal fix:

- Snapshot task and Plan Mode state before generic Web goal patch side effects.
- On `goal.updated` history/event failure, restore the previous Plan Mode snapshot when a gate was created, restore the task snapshot when captured, and restore the previous Goal snapshot.
- Reuse the Plan Mode and task snapshot infrastructure introduced by FCA-091 and FCA-092.

Validation:

- Focused Web generic goal-patch Plan Mode history-failure regression asserting Goal rollback and Plan Mode cleanup.
- Adjacent Web goal-patch simple, runtime-fact, and mission approval-gate regressions.
- Standard grouped validation before commit.

### FCA-20260526-094: Web validation-contract gate failures can orphan Plan Mode state

Severity: Medium

Evidence:

- `spec/18-durable-contract-and-completion.md` makes Goal and Plan Mode state durable facts for mission validation and approval workflows.
- Web `handleMissionValidationPatch` saves validation-contract changes, can reset an approved mission back to `needs_approval`, and can create a linked Plan Mode gate before appending the required `mission.validation.updated` history/event facts.
- A focused HTTP regression blocked `artifacts/goal-history.jsonl` for an approved mission validation-contract patch. Before this fix, Web `PATCH /api/sessions/{id}/mission/validation` returned an internal server error while leaving the validation contract, approval reset, and new linked `planmode.json` persisted.

Impact:

The Web console could report that a required `mission.validation.updated` durable history fact failed while later refreshes, recovery, and Mission Control observed both the rejected validation contract and the newly-created Plan Mode approval gate as current state.

Minimal fix:

- Snapshot Plan Mode state before Web mission validation patch side effects.
- On `mission.validation.updated` history/event failure, restore the previous Plan Mode snapshot when a gate was created and restore the previous Goal snapshot.
- Reuse the Plan Mode snapshot infrastructure introduced by FCA-092.

Validation:

- Focused Web validation-contract Plan Mode history-failure regression asserting Goal rollback and Plan Mode cleanup.
- Adjacent Web validation-plan and approval-reset regressions.
- Standard grouped validation before commit.

### FCA-20260526-095: Goal creation history failures can leave new goal and task facts

Severity: Medium

Evidence:

- `spec/00-product.md`, `spec/01-runtime-architecture.md`, and `spec/18-durable-contract-and-completion.md` define `goal.json`, `artifacts/goal-history.jsonl`, and durable task files as session facts for goals, mission plans, recovery, Web inspection, and checkpoints.
- `internal/session/goal.go` `CreateGoal` saved `goal.json`, optionally generated mission feature task files through `syncMissionPlanTasks`, and only then appended the required `goal.created` history fact.
- A focused store regression blocked `artifacts/goal-history.jsonl` before `CreateGoal` for a mission draft with `create_tasks_from_plan=true`. Before this fix, `CreateGoal` returned the history append error while leaving both `goal.json` and generated `tasks/task_*.json` files persisted.

Impact:

A caller could observe a failed goal-create operation, then later reload a current goal and generated mission task graph that had no matching required `goal.created` history fact. Retrying the create could also fail as a duplicate current goal even though the first API/CLI/runtime caller saw an error.

Minimal fix:

- Snapshot the previous Goal and task set before create-time side effects.
- If mission task sync, the post-task goal save, or the required `goal.created` history append fails after `goal.json` is written, restore the previous task set and previous Goal snapshot.
- Reuse existing exact task snapshot restore and Goal rollback helpers.

Validation:

- Focused store regression for blocked `goal-history.jsonl` during create-time mission task sync.
- Adjacent Goal lifecycle and task snapshot regressions.
- Standard grouped validation before commit.

### FCA-20260526-096: Web goal creation event failures can leave created side facts

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` defines `events.jsonl` as a session fact source, and `spec/17-web-console.md` defines Web goal creation as a local REST control operation backed by the session store.
- Web `handleGoalCreate` created the goal through `CreateGoal`, optionally created a linked Plan Mode gate through `EnsurePlanModeForGoal`, and then returned an error when the final `goal.created` event append failed.
- A focused HTTP regression blocked `events.jsonl` for `POST /api/sessions/{id}/goal` with a mission draft using `create_tasks_from_plan=true` and `require_plan_approval=true`. Before this fix, Web returned an internal server error while leaving `goal.json`, `artifacts/goal-history.jsonl`, generated task files, and linked `planmode.json` persisted.

Impact:

An operator could see Web goal creation fail, then refresh into a session that nevertheless had a current goal, generated task graph, and Plan Mode approval gate. Retrying creation would then fail as a duplicate current goal, and the Web event timeline would still miss the user control action that caused those facts.

Minimal fix:

- Snapshot tasks, Goal history, and Plan Mode state before Web goal creation side effects.
- If the required `goal.created` event append fails, restore the previous Plan Mode snapshot, task set, Goal snapshot, and Goal history.
- Keep the store-owned `goal.created` history append requirement from FCA-095 intact; this slice only handles the Web adapter event failure after store create succeeds.

Validation:

- Focused Web goal-create blocked-event regression asserting Goal, history, task, and Plan Mode rollback.
- Adjacent Web goal endpoint and session store creation regressions.
- Standard grouped validation before commit.

### FCA-20260526-097: Runtime linked mission approval hides event append failures

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` lists `mission.plan.approved` as a session event, and Plan Mode approval is the required execution gate for linked goal/mission approval.
- Runtime `approveLinkedMissionPlan` used `ApproveMissionPlan` to persist the approved mission snapshot and append `mission.plan.approved` goal history, then emitted the matching session event through best-effort `emit`.
- A focused runtime regression blocked `events.jsonl` after Plan Mode approval reached executing state. Before this fix, `approveLinkedMissionPlan` returned nil and left the mission approved even though the matching session event fact was missing.

Impact:

A linked Plan Mode approval could resume execution with mission approval visible in `goal.json` / `goal-history.jsonl` but absent from the session event timeline. Operators and Web/CLI recovery views that depend on the event stream would miss the approval control action while runtime continued as if all approval facts were durable.

Minimal fix:

- Use the error-returning event append helper for runtime linked mission approval.
- Do not roll back `goal.json` on event failure because the source approval history has already been durably appended; rolling back only the current snapshot would contradict `goal-history.jsonl`.
- Add focused coverage proving blocked `events.jsonl` is reported and the history-backed approved snapshot remains approved.

Validation:

- Focused runtime linked mission approval blocked-event regression.
- Adjacent linked Plan Mode approval and coverage-block regressions.
- Standard grouped validation before commit.

### FCA-20260526-098: Runtime Plan Mode continue actions hide event append failures

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` maps Plan Mode control actions to structured session events, including `planmode.created`, `planmode.input_answered`, `planmode.input_cancelled`, `planmode.plan_approved`, `planmode.execution_started`, `planmode.plan_revised`, and `planmode.cancelled`.
- Runtime `Continue` persisted Plan Mode create / approve / executing / revise / cancel transitions through the session store, then wrote the matching session events with best-effort `emit`.
- Runtime recovered Plan Mode input answer/cancel helpers appended replay tool results and Plan Mode history, then also used best-effort `emit` for the matching input events.
- Focused regressions blocked `events.jsonl`. Before this fix, Plan Mode cancellation returned `Status:"awaiting_input"` and final text `Plan Mode cancelled.` with no `planmode.cancelled` event, while Plan Mode approval returned an assistant result and called the provider despite missing the `planmode.plan_approved` event.

Impact:

Operator-driven Plan Mode control actions could be reported as successful while the event timeline missed the control fact. In the approval path this was worse than a display gap: runtime could enter the provider execution turn even though the approval event fact was not durable, weakening Web/CLI recovery and the local event timeline as an audit source for the execution gate.

Minimal fix:

- Route runtime Plan Mode create / approve / execution-start / revise / cancel events in `Continue` through the error-returning event append helper.
- Route recovered Plan Mode input answer/cancel events through the same error-returning path after their replay/history facts are written.
- Keep generic runtime `emit` behavior unchanged for diagnostic events; this slice only hardens Plan Mode control events called out by the durable event catalog and reached through `Continue`.

Validation:

- Focused blocked-`events.jsonl` regressions for Plan Mode cancellation and approval.
- Adjacent Plan Mode input cancellation and linked mission approval regressions.
- Standard grouped validation before commit.

### FCA-20260526-099: todo_write hides unreadable todo snapshot failures

Severity: Medium

Evidence:

- `spec/12-task-system.md` defines `todo.json` as the full session todo snapshot and `todo_write` as a full replacement tool that writes `todo.updated`.
- `internal/tools/registry.go` loaded the existing todo snapshot with `existing, _ := execCtx.Store.LoadTodo(...)` before deciding whether the incoming normalized snapshot was a no-op.
- A focused regression replaced `todo.json` with a directory and called `todo_write` with an empty list. Before this fix, the tool returned a successful no-op with `LLMOutput:"null"` and metadata `changed:false`, while the durable todo fact remained unreadable.

Impact:

An unreadable or corrupt todo snapshot could be hidden as a successful no-op whenever the requested normalized todo list matched the zero-value fallback. That weakens recovery and Web/task observability because the model and operator see a successful tool result while `todo.json` is still broken.

Minimal fix:

- Propagate `LoadTodo` errors before no-op comparison in `todo_write`.
- Preserve the existing behavior where a missing todo file loads as an empty list through the store helper.
- Add focused coverage for unreadable `todo.json` plus adjacent no-op and structured-event tests.

Validation:

- Focused pre-fix regression proving the false successful no-op.
- Focused post-fix todo/tool tests.
- Standard grouped validation before commit.

### FCA-20260526-100: Web goal mutations can leave missing linked Plan Mode gates

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md`, `spec/17-web-console.md`, and `spec/18-durable-contract-and-completion.md` require goals or missions that need plan approval to have a real linked Plan Mode gate, with `planmode.json` / `artifacts/planmode-history.jsonl` as durable facts.
- `internal/webconsole/service.go` `handleGoalCreate` created `goal.json`, `artifacts/goal-history.jsonl`, and optional mission-synced tasks before calling `EnsurePlanModeForGoal`.
- `handleGoalPatch`, `handleMissionPlanPatch`, and `handleMissionValidationPatch` similarly mutated `goal.json` and sometimes `tasks/` before ensuring the linked Plan Mode gate.
- When `EnsurePlanModeForGoal` failed, these handlers returned an HTTP 500 without restoring the already-mutated goal/task facts.
- Focused regressions blocked `artifacts/planmode-history.jsonl`. Before this fix, Web goal create returned an error but left the new goal/history/tasks behind, and mission plan patch returned an error while leaving the mission in `needs_approval` with generated task facts and no durable linked Plan Mode gate.

Impact:

Web operators could receive a failed mutation response while the session facts had still advanced into an approval-required state without the required Plan Mode gate. That weakens the Plan Mode execution boundary, Web Goal inspector consistency, task/mission recovery, and later completion decisions that assume approval-required goals have a durable pending gate.

Minimal fix:

- Roll back `goal.json`, goal history, task snapshots, and Plan Mode snapshots when Web goal creation cannot create the required linked Plan Mode.
- Roll back generic goal patch, mission plan patch, and mission validation patch goal/task mutations when linked Plan Mode creation fails.
- Restore the previous goal/task facts if a Plan Mode snapshot cannot be captured after a Web patch has already mutated goal facts.
- Add focused WebConsole regressions for create and mission-plan patch failures caused by blocked `planmode-history.jsonl`.

Validation:

- Focused pre-fix WebConsole regressions proving create and mission plan patch partial writes.
- Focused post-fix WebConsole regressions for the same paths.
- Adjacent Web goal / mission / Plan Mode rollback tests.
- Standard grouped validation before commit.

### FCA-20260526-101: Web goal task-sync failures leave patched goal facts

Severity: Medium

Evidence:

- `spec/12-task-system.md` defines the persistent task graph as durable recovery state, while `spec/17-web-console.md` requires the Web Console to read and write the same local goal/task facts rather than maintaining a second state source.
- `internal/webconsole/service.go` `handleGoalPatch` called `PatchGoal` before loading the task snapshot or running `SyncMissionPlanTasks` when the patched mission enabled `create_tasks_from_plan`.
- `handleMissionPlanPatch` loaded the old task snapshot first, but still called `PatchGoal` before running `SyncMissionPlanTasks`.
- If task synchronization failed after the goal patch, the handlers returned HTTP 500 without restoring the previous `goal.json` or any partially changed task files.
- Focused regressions replaced `tasks/taskboard.lock` with a directory. Before this fix, generic goal patch and mission plan patch returned an error while leaving `goal.json` advanced to a mission with `create_tasks_from_plan=true` and new feature facts, despite no synchronized task graph.

Impact:

The Web operator could see a failed patch response while durable goal facts claimed task-backed mission work had been enabled. Recovery, Goal inspector task links, session summaries, and later validation could then disagree with the actual task graph.

Minimal fix:

- Restore the previous goal snapshot if `handleGoalPatch` cannot load the task snapshot after applying a patch.
- Restore previous goal and task snapshots if task synchronization fails in generic goal patch or mission plan patch paths.
- Add focused WebConsole regressions for blocked taskboard lock failures in both routes.

Validation:

- Focused pre-fix WebConsole regressions proving goal facts advanced after task-sync failure.
- Focused post-fix WebConsole regressions for the same paths.
- Adjacent Web goal / mission rollback tests.
- Standard grouped validation before commit.

### FCA-20260526-102: Web goal event rollback leaves history and Plan Mode side facts

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md`, `spec/17-web-console.md`, and `spec/18-durable-contract-and-completion.md` require Web Goal / Plan Mode controls to reuse durable `goal.json`, `goal-history.jsonl`, `planmode.json`, and `events.jsonl` facts rather than a second Web state source.
- `internal/webconsole/service.go` `handleGoalClear` removed `goal.json`, appended `goal.cleared` to `goal-history.jsonl`, then appended `goal.cleared` to `events.jsonl`.
- `handleGoalStatus` and other `appendGoalMutation` callers restored `goal.json` when the required session event append failed, but did not restore the just-appended Goal history row.
- `handleGoalPatch` restored Plan Mode only when `EnsurePlanModeForGoal` created a new Plan Mode; linking an existing pending Plan Mode returned `created=false`, so a later event-stage failure could roll back the goal while leaving `planmode.json.linked_goal_id` advanced.
- Focused regressions blocked `events.jsonl`. Before the fix, failed goal clear left `goal.cleared` history, failed goal pause left `goal.paused` history, and failed approval-gated goal patch left an existing pending Plan Mode linked to the rolled-back goal.

Impact:

Web operators could receive a failed mutation response while durable side facts claimed the mutation happened. Recovery, Goal inspector history, linked Plan Mode display, and future execution gates could then disagree with the restored current goal snapshot.

Minimal fix:

- Snapshot Goal history before rollback-capable Web Goal mutations.
- Distinguish `appendGoalMutation` history-stage failures from event-stage failures.
- Restore Goal history only when history append succeeded and the subsequent required session event append failed.
- Treat existing Plan Mode relinks as rollback-relevant Plan Mode changes, not only newly-created Plan Modes.
- Add focused WebConsole regressions for blocked-event rollback of goal clear, goal status, and goal patch Plan Mode linking.

Validation:

- Focused pre-fix WebConsole regressions proving history and Plan Mode side facts advanced after event-stage failure.
- Focused post-fix WebConsole regressions for the same paths.
- Adjacent Web goal / mission rollback tests.
- Standard grouped validation before commit.

### FCA-20260526-103: Recovered Plan Mode input can clear pending request without replay result

Severity: High

Evidence:

- `spec/01-runtime-architecture.md`, `spec/03-provider-contracts.md`, and `spec/18-durable-contract-and-completion.md` require recovered `request_user_input` answers to append the matching tool result using the stored `tool_call_id`, so provider replay has a result for the original tool call.
- `internal/runtime/runner.go` `appendPlanInputToolResult` called `AnswerPlanModeInput`, which clears `planmode.json.pending_request` and appends `planmode.input_answered` history, before appending the recovered `request_user_input` tool result to `messages.jsonl`.
- If `messages.jsonl` append failed after the Plan Mode mutation, `appendPlanInputToolResult` returned an error while leaving `planmode.json` back in `planning` with no pending request and no replayable tool result.
- A focused regression replaced `messages.jsonl` with a directory. Before the fix, the call failed but `planmode.json` had already cleared the pending request and `artifacts/planmode-history.jsonl` contained `planmode.input_answered`.

Impact:

Crash/restart fallback and Web recovered input could lose the only replay result required to satisfy the provider's pending `request_user_input` call, while durable Plan Mode state claimed the input was answered. The session would be harder to recover correctly because the operator could not answer the same pending request again and provider replay would still be missing the tool result.

Minimal fix:

- Snapshot Plan Mode state and Plan Mode history before answering recovered Plan Mode input.
- Restore both snapshots if the recovered `request_user_input` tool-result message cannot be appended.
- Add a store helper for restoring `artifacts/planmode-history.jsonl`.
- Add focused runtime coverage that blocks `messages.jsonl` and verifies the pending request and Plan Mode history are restored.

Validation:

- Focused pre-fix runtime regression proving pending input was cleared after message append failure.
- Focused post-fix runtime regression for the same path.
- Adjacent Plan Mode runtime/session tests.
- Standard grouped validation before commit.

### FCA-20260526-104: Plan Mode approval retry can strand missing replay message

Severity: High

Evidence:

- `spec/01-runtime-architecture.md`, `spec/03-provider-contracts.md`, `spec/17-web-console.md`, and `spec/18-durable-contract-and-completion.md` require Plan Mode approval to leave a replayable `meta.source=planmode_approval` user message before execution resumes.
- `internal/runtime/runner.go` `Continue` advanced Plan Mode through `ApprovePlanMode`, required approval/execution events, and linked mission approval before appending the approval user message through `appendUserMessage`.
- If `messages.jsonl` append failed at that point, `Continue` returned an error while `planmode.json` was already `executing`, the linked mission plan could already be approved, and no replayable approval user message existed.
- A retry with `ApprovePlan=true` then failed pre-provider because `ApprovePlanMode` rejected the already-`executing` Plan Mode, so the operator could not use the same approval action to write the missing replay fact and continue.
- A focused regression replaced `messages.jsonl` with a directory during approval, restored the path, and retried approval. Before the fix, the retry stopped with `plan mode is not awaiting approval: executing`.

Impact:

An approval crash or filesystem failure at the replay-message boundary could leave durable Plan Mode/mission control facts saying approval/execution started while provider replay lacked the required user approval message. The normal retry path then refused to proceed, forcing manual repair of session files.

Minimal fix:

- Make runtime Plan Mode approval recovery idempotent across `awaiting_approval`, `approved`, and `executing` states.
- Re-emit missing required Plan Mode approval/execution events only when the same Plan Mode id and approved version are absent from `events.jsonl`.
- Allow an already-`executing` approved Plan Mode to proceed to linked mission approval and approval-message append without re-mutating Plan Mode history.
- Add focused runtime coverage that blocks `messages.jsonl`, verifies the provider does not run without the replay message, restores the path, retries approval, and verifies exactly one `planmode_approval` user message is written.

Validation:

- Focused pre-fix runtime regression proving retry failed from the partially advanced approval state.
- Focused post-fix runtime regression for the same path.
- Adjacent Plan Mode approval/input runtime tests.
- Standard grouped validation before commit.

### FCA-20260526-105: Plan Mode revision retry loses replay metadata

Severity: High

Evidence:

- `spec/17-web-console.md` defines Ask for Changes as a Plan Mode revision user message and `spec/18-durable-contract-and-completion.md` requires Plan Mode facts to remain recoverable through durable session files.
- `internal/runtime/runner.go` revised Plan Mode by calling `RevisePlanMode`, appending a `planmode.plan_revised` event, and only then appending the user message with `meta.source=planmode_revision`.
- If `messages.jsonl` append failed, durable Plan Mode state moved back to `planning` and history recorded `planmode.plan_revised`, but no replayable revision user message existed.
- A retry with the same revision text no longer entered the revision branch because it only recognized `awaiting_approval`; it appended a plain user message with no `planmode_revision` metadata and continued provider execution.
- A focused regression blocked `messages.jsonl` during revision, restored the path, retried the same revision, and observed the retry had no `planmode_revision` message before the fix.

Impact:

After a filesystem failure or crash at the revision replay-message boundary, recovery could lose the only durable marker distinguishing a Plan Mode revision from an ordinary user continuation. Web inspector, timeline, and provider replay would see a plain user prompt even though Plan Mode history said a revision occurred.

Minimal fix:

- Make runtime revision handling idempotent for the recovery case where Plan Mode is already back in `planning` and the latest `planmode.plan_revised` history row matches the same Plan Mode id, plan version, and revision text.
- Require the matching replay message to still be absent before treating a planning-state continuation as recovered revision replay, so repeated ordinary planning continuations are not reclassified.
- Re-emit a missing `planmode.plan_revised` event only for the matching Plan Mode id/version.
- Append the retried user message with `meta.source=planmode_revision` instead of treating it as an ordinary continuation.
- Add focused runtime coverage for blocked `messages.jsonl` during revision and retry after restoring the path.

Validation:

- Focused pre-fix runtime regression proving revision retry produced no `planmode_revision` user message.
- Focused post-fix runtime regression for the same path.
- Adjacent Plan Mode approval/revision runtime tests.
- Standard grouped validation before commit.

### FCA-20260526-106: Plan Mode cancellation retry duplicates durable history

Severity: Medium

Evidence:

- `spec/18-durable-contract-and-completion.md` defines `planmode.json` and `artifacts/planmode-history.jsonl` as Plan Mode facts, while `events.jsonl` is the replay/observability event stream consumed by runtime/Web surfaces.
- `internal/runtime/runner.go` cancelled Plan Mode by appending any pending `request_user_input` cancellation result, calling `CancelPlanMode`, then appending the required `planmode.cancelled` runtime event.
- If `events.jsonl` append failed after `CancelPlanMode`, durable Plan Mode state and history already recorded cancellation.
- Retrying the same cancellation called `CancelPlanMode` again even when `planmode.json` was already `cancelled`, adding a second `planmode.cancelled` history row before restoring the missing runtime event.
- A focused regression blocked `events.jsonl` during cancellation, restored the path, retried cancellation, and observed duplicate `planmode.cancelled` history before the fix.

Impact:

An operator retry after a transient event append failure could make Plan Mode history show multiple user cancellations for one cancellation action. This did not resume provider execution, but it polluted the durable audit trail and could mislead Web/session summary recovery views.

Minimal fix:

- Make runtime cancellation recovery idempotent when Plan Mode is already `cancelled`.
- Re-append the missing `planmode.cancelled` runtime event once without re-running the store cancellation transition.
- Keep pending `request_user_input` cancellation tool-result de-duplication unchanged.
- Add focused runtime coverage for blocked `events.jsonl`, retry after restoring the path, one durable cancellation history row, and one restored runtime event.

Validation:

- Focused pre-fix runtime regression proving retry duplicated `planmode.cancelled` history.
- Focused post-fix runtime regression for the same path.
- Adjacent Plan Mode cancellation/input runtime tests.
- Standard grouped validation before commit.

### FCA-20260526-107: Plan input cancel retry skips missing durable facts

Severity: High

Evidence:

- `spec/03-provider-contracts.md` requires provider replay to include the `request_user_input` tool result, and `spec/18-durable-contract-and-completion.md` requires Plan Mode input/cancellation transitions to remain recoverable through durable session files.
- `internal/runtime/runner.go` `appendPlanInputCancelToolResult` appended the cancellation tool result to `messages.jsonl`, then appended `planmode.input_cancelled` history, then appended the matching runtime event.
- If `planmode-history.jsonl` failed after the tool result append, retry saw the existing `request_user_input` tool result and returned early.
- That left provider replay repaired but `artifacts/planmode-history.jsonl` and `events.jsonl` missing the `planmode.input_cancelled` facts for the pending request.
- A focused regression blocked `planmode-history.jsonl`, retried after restoring the path, and observed no `planmode.input_cancelled` history before the fix.

Impact:

Recovered provider replay could proceed with a cancellation tool result while Plan Mode history and Web/session observability lacked the corresponding input-cancel fact. That made crash recovery inconsistent across the durable message stream, Plan Mode history, and runtime events.

Minimal fix:

- Split recovered input cancellation into independently idempotent steps for the replay tool result, Plan Mode input-cancel history, and runtime event.
- Keep the existing tool-result de-duplication, but continue repairing missing history/event facts when the tool result already exists.
- De-duplicate `planmode.input_cancelled` history by Plan Mode id, request id, and tool call id.
- De-duplicate the runtime event by Plan Mode id and request id.
- Add focused runtime coverage for blocked history append, retry after restoring the path, one replay tool result, one input-cancel history row, and one input-cancel event.

Validation:

- Focused pre-fix runtime regression proving retry skipped the missing input-cancel history.
- Focused post-fix runtime regression for the same path.
- Adjacent Plan Mode cancellation/input runtime tests.
- Standard grouped validation before commit.

### FCA-20260526-108: Plan input answer retry cannot restore missing event

Severity: Medium

Evidence:

- `spec/18-durable-contract-and-completion.md` requires Plan Mode input transitions to remain recoverable through durable session files, and Web/session views depend on runtime events for operator-visible control facts.
- `internal/runtime/runner.go` `appendPlanInputToolResult` called `AnswerPlanModeInput`, appended the `request_user_input` tool result, then appended a `planmode.input_answered` runtime event.
- If `events.jsonl` append failed after the store transition and message append, `planmode.json`, `artifacts/planmode-history.jsonl`, and `messages.jsonl` were already repaired.
- Retrying the same input answer then called `AnswerPlanModeInput` again, but the pending request had already been cleared, so retry failed with `plan mode has no pending input request` and could not restore the missing runtime event.
- A focused regression blocked `events.jsonl`, retried the same answer after restoring the path, and observed the retry failure before the fix.

Impact:

Recovered provider replay and Plan Mode history could be complete while the runtime event stream remained missing the input-answer event. Web/session observability and recovery summaries could therefore disagree with the durable Plan Mode and message facts.

Minimal fix:

- Add an idempotent retry path that recognizes an already-answered request by matching `planmode.input_answered` history against the same request id and answer payload.
- Require the corresponding `input_requested` history row to recover the original tool call id, and require the replay tool result to already exist before treating the retry as recovered.
- Append the missing `planmode.input_answered` event once without re-running the store answer transition or duplicating the replay tool result.
- Add focused runtime coverage for blocked `events.jsonl`, retry after restoring the path, one replay tool result, one answered history row, and one restored event.

Validation:

- Focused pre-fix runtime regression proving retry failed with `plan mode has no pending input request`.
- Focused post-fix runtime regression for the same path.
- Adjacent Plan Mode answer/cancel runtime tests.
- Standard grouped validation before commit.

### FCA-20260526-109: Continue Plan Mode creation retry replaces current gate

Severity: High

Evidence:

- `spec/18-durable-contract-and-completion.md` defines `planmode.json`, `artifacts/planmode-history.jsonl`, and `events.jsonl` as the durable Plan Mode fact surfaces, and `spec/17-web-console.md` treats Plan Mode as the execution gate that Web/CLI must not replace accidentally.
- `internal/runtime/runner.go` `Continue` with `PlanMode.Enabled` called `CreatePlanMode`, then appended a required `planmode.created` runtime event.
- If `events.jsonl` append failed after the store creation, `planmode.json` and history already contained the new planning gate.
- Retrying the same continue request called `CreatePlanMode` again, replacing the current Plan Mode id and duplicating `planmode.created` history instead of repairing the missing event for the existing gate.
- A focused regression blocked `events.jsonl`, retried with the same Plan Mode draft after restoring the path, and observed a different Plan Mode id before the fix.

Impact:

A transient event append failure at Plan Mode creation could orphan the first gate and replace it with a second gate on retry. That breaks continuity for Web inspector state, linked recovery evidence, and any operator reasoning tied to the original Plan Mode id.

Minimal fix:

- Add an idempotent continue-time Plan Mode creation helper.
- When the current Plan Mode is still a planning gate matching the requested objective/source, re-append the missing `planmode.created` event without creating a new Plan Mode.
- Continue to create a new Plan Mode for genuinely different drafts.
- Add focused runtime coverage for blocked `events.jsonl`, retry after restoring the path, unchanged Plan Mode id, one `planmode.created` history row, and one restored runtime event.

Validation:

- Focused pre-fix runtime regression proving retry replaced the current Plan Mode id.
- Focused post-fix runtime regression for the same path.
- Adjacent Plan Mode creation/revision/input runtime tests.
- Standard grouped validation before commit.

### FCA-20260526-110: Linked mission approval retry duplicates history

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` and `spec/18-durable-contract-and-completion.md` require mission plan approval to synchronize through linked Plan Mode facts and leave durable approval evidence.
- `internal/runtime/runner.go` `approveLinkedMissionPlan` called `ApproveMissionPlan`, which mutates `goal.json` and appends `mission.plan.approved` to `goal-history.jsonl`, then appended a required `mission.plan.approved` runtime event.
- If `events.jsonl` append failed after the goal mutation/history append, the mission plan was already approved and history-backed.
- Retrying the same linked mission approval called `ApproveMissionPlan` again, adding a second `mission.plan.approved` history row before restoring the missing event.
- A focused regression blocked `events.jsonl`, retried after restoring the path, and observed duplicate mission approval history before the fix.

Impact:

A retry after transient event failure could make a single linked Plan Mode approval appear as multiple mission approvals in durable goal history. That pollutes audit history and can mislead Web/session summaries about operator approval actions.

Minimal fix:

- Treat linked mission approval as idempotent when the current mission is already approved and goal history already records the same Plan Mode id and approved version.
- Re-append the missing `mission.plan.approved` runtime event once without re-running the goal store mutation.
- De-duplicate the runtime event by goal id, Plan Mode id, and approved version.
- Add focused runtime coverage for blocked `events.jsonl`, retry after restoring the path, one mission approval history row, and one restored mission approval event.

Validation:

- Focused pre-fix runtime regression proving retry duplicated `mission.plan.approved` history.
- Focused post-fix runtime regression for the same path.
- Adjacent Plan Mode approval/mission runtime tests.
- Standard grouped validation before commit.

### FCA-20260526-111: Web mission approval event failure leaves approved goal facts

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md`, `spec/17-web-console.md`, and `spec/18-durable-contract-and-completion.md` require Web Goal controls to reuse durable `goal.json`, `goal-history.jsonl`, and `events.jsonl` facts.
- `internal/webconsole/service.go` `handleMissionPlanApprove` called `ApproveMissionPlan`, which mutates `goal.json` and appends `mission.plan.approved` to `goal-history.jsonl`, then appended a required `mission.plan.approved` session event through `appendGoalEvent`.
- If `events.jsonl` append failed after the approval mutation, the HTTP request returned 500 while the mission plan remained approved and history-backed.
- A focused WebConsole regression blocked `events.jsonl`; before the fix, failed mission approval left `goal.json.mission.plan_status=approved` and `goal-history.jsonl` contained `mission.plan.approved`.

Impact:

A Web operator could receive a failed mission approval response while durable Goal facts said approval succeeded. Recovery, session detail, Goal inspector, and future approval retries could then disagree about whether a real operator approval completed.

Minimal fix:

- Snapshot the current Goal and Goal history before Web mission plan approval.
- Append the required `mission.plan.approved` event through the same helper that performs the approval.
- Restore both `goal.json` and `goal-history.jsonl` when the event append fails.
- Preserve HTTP 400 for store-level approval errors and HTTP 500 for event/rollback failures.
- Add focused WebConsole coverage for blocked `events.jsonl`, restored unapproved mission snapshot, and restored Goal history.

Validation:

- Focused pre-fix WebConsole regression proving failed event append left the mission approved.
- Focused post-fix WebConsole regression for the same path.
- Adjacent Web mission approval tests.
- Standard grouped validation before commit.

### FCA-20260526-112: Web mission approval reports durable history load failures as client errors

Severity: Low

Evidence:

- `spec/01-runtime-architecture.md` and `spec/17-web-console.md` require Web Goal controls to operate on the local session file facts rather than a second Web state source.
- After FCA-20260526-111, `internal/webconsole/service.go` `approveMissionPlanWithEvent` loads `goal.json` and `artifacts/goal-history.jsonl` before mutating approval state so it can roll back event-stage failures.
- `handleMissionPlanApprove` mapped any error from that helper through `missionPlanApprovalStatus`, which returned HTTP 400 unless the error was the event-stage wrapper.
- A focused WebConsole regression replaced `goal-history.jsonl` with a directory. Before the fix, `/mission/plan/approve` returned HTTP 400 even though the failure was an unreadable local durable fact, not a malformed operator request.

Impact:

The Web API could misclassify local session-store corruption or filesystem failure as a client request problem. That weakens operator remediation because the correct action is to repair the local session facts or retry after storage recovery, not to change the approval request.

Minimal fix:

- Wrap pre-mutation mission approval snapshot/load failures in a store-error type.
- Map mission approval store and event failures to HTTP 500 while preserving HTTP 400 for store-level approval validation errors such as missing mission plans.
- Add focused WebConsole coverage for unreadable Goal history returning server error and adjacent coverage for validation/error paths.

Validation:

- Focused pre-fix WebConsole regression proving unreadable Goal history returned HTTP 400.
- Focused post-fix WebConsole regression for the same path.
- Adjacent Web mission approval error tests.
- Standard grouped validation before commit.

### FCA-20260526-113: Web steer reports durable queue write failures as client errors

Severity: Low

Evidence:

- `spec/13-live-input-and-steering.md` requires Web and CLI steer inputs to land in the same durable `control/steer.jsonl` file fact before active runners accept them.
- `internal/runtime/runner.go` `Steer` appends the request to `control/steer.jsonl`, refreshes pending steer count in `state.json`, and appends required `session.steer.requested` / `session.steer.queued` events.
- `internal/webconsole/service.go` `handleSteerSession` mapped all non-size and non-running errors from `Runner.Steer` to HTTP 400.
- A focused WebConsole regression replaced `control/steer.jsonl` with a directory. Before the fix, `/api/sessions/{id}/steer` returned HTTP 400 even though the failure was a local durable control-queue write failure, not a malformed Web request.

Impact:

The Web API could tell the operator to fix the request when the real problem was local session storage or control queue corruption. That weakens recovery for live steering because the steer did not become a durable file fact, yet the response classification implied a client-side input issue.

Minimal fix:

- Add a narrow Web steer status classifier.
- Keep oversized steer payloads and empty messages as HTTP 400.
- Keep non-running sessions as HTTP 409 conflict.
- Return HTTP 500 for local store/event/runtime failures and HTTP 404 for missing session facts.
- Add focused WebConsole coverage for blocked `control/steer.jsonl`.

Validation:

- Focused pre-fix WebConsole regression proving blocked steer queue writes returned HTTP 400.
- Focused post-fix WebConsole regression for the same path and adjacent successful Web steer source test.
- Standard grouped validation before commit.

### FCA-20260526-114: Web queue submit reports durable job-store failures as client errors

Severity: Low

Evidence:

- `spec/17-web-console.md` requires Web errors to distinguish user input errors from infrastructure failures such as unwritable session roots or queue persistence failures.
- `spec/01-runtime-architecture.md` requires queue jobs to be durable local file facts managed through `QueueStore And Worker`, not transient Web state.
- `internal/runtime/delegation.go` `QueueSubmit` performs both request validation and durable work: pending parent Plan Mode rejection, parent metadata load, provider/model resolution, `_queue/<status>/<job>.json` persistence, and optional parent-coordination updates.
- `internal/webconsole/service.go` `handleCreateJob` mapped every `QueueSubmit` error to HTTP 400.
- A focused WebConsole regression replaced the queue root `_queue` with a regular file. Before the fix, `/api/queue/jobs` returned HTTP 400 even though the failure was a local durable queue-store write failure and no queued job fact was persisted.

Impact:

Operators could receive a bad-request response for local queue storage corruption or filesystem failure. That points recovery at changing the submitted prompt or payload even though the correct remediation is to repair local durable queue/session facts or retry after storage recovery.

Minimal fix:

- Add a narrow Web queue submit status classifier.
- Keep malformed prompt, unsupported role, unknown provider/config request errors, and invalid isolation request errors as HTTP 400.
- Return HTTP 409 for parent sessions blocked by pending Plan Mode, HTTP 404 for missing durable session facts, and HTTP 500 for queue store, parent coordination, or other infrastructure failures.
- Add focused WebConsole coverage for blocked `_queue` persistence and update the existing pending Plan Mode queue-gate expectation to conflict.

Validation:

- Focused pre-fix WebConsole regression proving blocked queue-store writes returned HTTP 400.
- Focused post-fix WebConsole regression for the same path and adjacent pending Plan Mode queue gate.
- Standard grouped validation before commit.

### FCA-20260526-115: Queue submit leaves queued job after parent coordination failure

Severity: High

Evidence:

- `spec/01-runtime-architecture.md` requires parent / queue coordination facts to make child and queue work visible through durable local state.
- `spec/18-durable-contract-and-completion.md` requires parent coordination to block parent completion while unresolved explicit queue work remains.
- `internal/runtime/delegation.go` `QueueSubmit` wrote the queue job with `store.EnqueueJob(job)` before adding the job to `parent-coordination.json`.
- If `addParentQueueJob` failed, `QueueSubmit` returned an error but did not remove the already-queued job.
- The existing parent-coordination error regression only asserted that an error was returned. Extending it to list queue jobs after the failed submit showed the failed request left a queued job available for worker pickup before the fix.

Impact:

The Web/API/CLI caller could see queue submission fail while a background worker still consumes the child job. The parent session would not have the matching unresolved queue coordination fact, so parent completion gates, recovery views, and background observability could miss work that is actually running.

Minimal fix:

- Add a narrow `Store.DeleteJob` wrapper around the existing locked queue job deletion helper.
- When parent coordination fails after `EnqueueJob`, delete the just-created queue job before returning the coordination error.
- If rollback deletion also fails, return an error that includes both the original coordination failure and the rollback failure.
- Extend the parent-coordination error regression to assert no queued job remains after the failed submit.

Validation:

- Focused pre-fix runtime regression proving failed parent coordination left a queued job behind.
- Focused post-fix runtime regression for rollback on parent coordination failure.
- Standard grouped validation before commit.

### FCA-20260526-116: Web mission validation reports durable goal write failures as client errors

Severity: Low

Evidence:

- `spec/17-web-console.md` requires Web control actions to distinguish operator request errors from local durable store failures.
- `spec/01-runtime-architecture.md` requires `goal.json` to remain the session-scoped Goal fact source shared by Web, CLI, and runtime paths.
- `internal/webconsole/service.go` `handleMissionValidationPatch` loads the current goal and history, applies the requested validation plan or validation contract patch in memory, then writes the new snapshot with `s.store.SaveGoal`.
- `SaveGoal` uses the session `goal.lock` and `goal.json` file facts; failures there are durable store failures, not malformed validation payloads.
- A focused WebConsole regression blocked `goal.lock` by replacing it with a directory. Before the fix, `/api/sessions/{id}/mission/validation` returned HTTP 400 even though the snapshot was not persisted and the existing validation plan remained unchanged.

Impact:

Operators could receive a bad-request response for local Goal store corruption or filesystem failure. That points recovery at editing the validation payload, while the correct remediation is to repair the local session store or retry after storage recovery.

Minimal fix:

- Map `SaveGoal` failure in `handleMissionValidationPatch` to HTTP 500.
- Keep request JSON decode failures as HTTP 400 and existing `goalStoreStatus` behavior for missing or invalid current goal loads.
- Add focused WebConsole coverage for blocked `goal.lock`, server-error status, and unchanged durable Goal snapshot.

Validation:

- Focused pre-fix WebConsole regression proving blocked `goal.lock` returned HTTP 400.
- Focused post-fix WebConsole regression for the same path.
- Standard grouped validation before commit.

### FCA-20260526-117: Web goal and mission plan patches report durable goal write failures as client errors

Severity: Low

Evidence:

- `spec/17-web-console.md` requires Web mutation responses to distinguish malformed requests from local durable fact-store failures.
- `spec/01-runtime-architecture.md` requires Web goal controls to reuse the same durable `goal.json` fact source as the runtime and CLI.
- `internal/webconsole/service.go` `handleGoalPatch` and `handleMissionPlanPatch` both load the current goal and history, then persist changes through `s.store.PatchGoal`.
- `PatchGoal` validates and writes `goal.json` under the session `goal.lock`; blocked lock or write paths are local store failures, not bad patch payloads.
- Focused WebConsole regressions blocked `goal.lock` by replacing it with a directory. Before the fix, both `/api/sessions/{id}/goal` and `/api/sessions/{id}/mission/plan` returned HTTP 400 while leaving the existing Goal snapshot unchanged.

Impact:

Operators could receive client-error responses for durable Goal store corruption while ordinary Goal and Mission plan patches were not persisted. That misdirects recovery toward changing valid patch payloads instead of repairing or retrying the local session store.

Minimal fix:

- Use the existing `goalStoreStatus` classifier for `PatchGoal` failures in both Web patch handlers.
- Preserve HTTP 400 for validation errors from `PatchGoal` and HTTP 500 for local store write failures.
- Add focused WebConsole regressions for blocked `goal.lock` on generic Goal patch and Mission plan patch.

Validation:

- Focused pre-fix WebConsole regressions proving blocked `goal.lock` returned HTTP 400 for both endpoints.
- Focused post-fix WebConsole regressions for both endpoints.
- Standard grouped validation before commit.

### FCA-20260526-118: Web API-key env write failure leaves persisted config changes

Severity: Medium

Evidence:

- `spec/17-web-console.md` treats Settings provider/API key writes as sensitive local Web actions requiring clear failure behavior and auditability.
- `internal/webconsole/service.go` `handleUpdateConfig` preflighted the API-key env target, wrote `config.yaml`, then wrote the `.env` API key.
- If the env write failed after config persistence, the handler returned HTTP 500 while leaving the new config file on disk. The in-memory service config was not swapped, so file facts and live service state diverged.
- A focused WebConsole regression set `GO_CLI_AGENT_ENV_FILE` to `<configPath>/.env` while `configPath` did not exist. Env preflight passed; `config.WriteFile` created `configPath` as a regular file; `config.UpsertEnvFile` then failed because its parent path was not a directory. Before the fix, the failed response left `model: should-not-persist` in the config file.

Impact:

Operators could retry or inspect Settings after a failed API-key write while the persisted config file already contained the new provider/model settings. Restarting the Web service or CLI could then pick up changes that the failed response implied had not been applied.

Minimal fix:

- Snapshot the existing config file before writing.
- If API-key env persistence fails after config write, restore the previous config bytes or remove the newly-created config file.
- Keep existing ordering that config write must succeed before API key persistence, so a failed config write still cannot persist API keys.
- Add focused WebConsole coverage for env write failure rollback and keep existing config/API-key preflight tests.

Validation:

- Focused pre-fix WebConsole regression proving failed env write left config changes persisted.
- Focused post-fix WebConsole regression proving config changes roll back on env write failure.
- Standard grouped validation before commit.

### FCA-20260526-119: Web API-key invalid env keys fail after writing secrets

Severity: Medium

Evidence:

- `spec/17-web-console.md` treats Settings API-key writes as sensitive local Web actions that need clear failure behavior and auditability.
- `internal/config/envfile.go` `LoadEnvFile` already ignores keys outside the project-managed env-file allowlist, but `UpsertEnvFile` only rejected an empty key before writing.
- `internal/webconsole/service.go` `preflightWebAPIKeyUpdate` likewise checked for an empty provider `api_key_env`, then allowed config and `.env` persistence to proceed before the later `os.Setenv` call.
- A focused WebConsole regression configured a provider with `APIKeyEnv: "BAD=KEY_API_KEY"`. Before the fix, `/api/config` returned HTTP 500 from the late `setenv: invalid argument` path after the secret had already been written into `.env` and the config file had already been persisted.

Impact:

A malformed provider `api_key_env` could make Settings report failure while leaving an invalid `.env` assignment containing the submitted secret, plus provider/model config changes on disk. The next service start would ignore that invalid `.env` key, so the operator would have both a leaked local secret and a persisted config that did not actually work.

Minimal fix:

- Expose the existing env-file key policy as `config.AllowedEnvFileKey`.
- Require `UpsertEnvFile` to reject disallowed or syntactically invalid env keys before writing.
- Have Web Settings API-key preflight call the same policy before `config.yaml`, `.env`, process environment, or audit mutation.
- Add focused WebConsole and config-package regressions for invalid env keys.

Validation:

- Focused pre-fix WebConsole regression proving invalid env key failure occurred at late `os.Setenv`.
- Focused post-fix WebConsole and config-package regressions proving invalid env keys are rejected before persistence.
- Standard grouped validation before commit.

### FCA-20260526-120: Web API-key env file can alias the config file

Severity: Medium

Evidence:

- `spec/17-web-console.md` treats Settings config/API-key writes as sensitive mutations, and `spec/01-runtime-architecture.md` requires local file facts to remain consistent.
- `internal/webconsole/service.go` `handleUpdateConfig` computed `configPath` and `apiKeyUpdate.envFile` independently, but did not reject the same filesystem path being used for both.
- The handler writes YAML config first through `config.WriteFile`, then appends the API key through `config.UpsertEnvFile`.
- A focused WebConsole regression set `GO_CLI_AGENT_ENV_FILE` to the same `config.yaml` path used by `Options.ConfigPath`. Before the fix, `/api/config` returned HTTP 200 and left a single file containing YAML config plus an appended `OPENAI_API_KEY=...` assignment.

Impact:

Operators could accidentally configure the API-key env target to the config file itself and receive a successful Settings response. The resulting file is neither a clean YAML config nor a separate env file, and it may contain a persisted secret in a location the user expected to hold only provider/model configuration.

Minimal fix:

- Add a Web Settings preflight that rejects API-key env-file targets whose cleaned absolute path matches the config path.
- Run this preflight before config, env-file, process environment, or audit mutation.
- Add focused WebConsole coverage asserting no config/env file or process env mutation occurs when the two targets alias.

Validation:

- Focused pre-fix WebConsole regression proving the aliased config/env target returned HTTP 200.
- Focused post-fix WebConsole regression proving the alias is rejected before persistence.
- Standard grouped validation before commit.

### FCA-20260526-121: Web config file can alias the audit log

Severity: Medium

Evidence:

- `spec/17-web-console.md` requires Settings writes and API-key writes to be auditable, and `spec/01-runtime-architecture.md` keeps local files as durable fact sources.
- `internal/webconsole/service.go` `handleUpdateConfig` preflighted that the audit log was writable, then wrote `config.yaml`, and then appended `web.config.write` to the audit log.
- The route did not reject `Options.ConfigPath` matching `webAuditLogPath(s.store.Root())`.
- A focused WebConsole regression set the config path to the Web audit log path. Before the fix, `/api/config` returned HTTP 200 and left one file containing YAML config followed by JSONL audit events.

Impact:

A misconfigured Web service could treat one path as both durable config and audit log. The successful response would leave a corrupted config file and a non-independent audit trail, undermining later recovery and Settings inspection.

Minimal fix:

- Add a Web Settings preflight that rejects config paths whose cleaned absolute path matches the Web audit log path.
- Run this preflight before audit-log writability probing, config persistence, env-file persistence, process env mutation, or audit append.
- Add focused WebConsole coverage asserting no file is created when the config path aliases the audit log.

Validation:

- Focused pre-fix WebConsole regression proving the aliased config/audit target returned HTTP 200.
- Focused post-fix WebConsole regression proving the alias is rejected before persistence.
- Standard grouped validation before commit.

### FCA-20260526-122: Web API-key invalid env values fail after writing secrets

Severity: Medium

Evidence:

- `spec/17-web-console.md` treats Settings API-key writes as sensitive local mutations requiring clear failure behavior.
- `internal/webconsole/service.go` `handleUpdateConfig` wrote `config.yaml`, then wrote the submitted API key into `.env`, and only afterwards called `os.Setenv`.
- Go rejects NUL-containing environment values, but `preflightWebAPIKeyUpdate` did not validate the API-key value before persistence.
- A focused WebConsole regression submitted `api_key: "sk-invalid\x00value"`. Before the fix, `/api/config` returned HTTP 500 from late `setenv: invalid argument` after the config and `.env` write paths had already run.

Impact:

A malformed secret value could make Settings report failure while still persisting a submitted secret into `.env` and persisting provider/model config changes. The process environment would not match durable files, and retry/recovery would be ambiguous.

Minimal fix:

- Reject NUL-containing API-key values in the existing Web API-key preflight.
- Run that check before config, env-file, process environment, or audit mutation.
- Add focused WebConsole coverage asserting invalid values leave no config file, env file secret, or process env mutation.

Validation:

- Focused pre-fix WebConsole regression proving invalid env values failed at late `os.Setenv`.
- Focused post-fix WebConsole regression proving invalid env values are rejected before persistence.
- Standard grouped validation before commit.

### FCA-20260526-123: Web API-key env file can alias the audit log

Severity: Medium

Evidence:

- `spec/17-web-console.md` requires API-key writes to avoid recording secret values in audit events, while still writing auditable Settings events.
- `internal/webconsole/service.go` `handleUpdateConfig` rejected config/env and config/audit aliases, but did not reject the API-key env file matching the Web audit log path.
- The handler writes the submitted API key through `config.UpsertEnvFile` before appending `web.config.write` and `web.config.api_key_write` audit events.
- A focused WebConsole regression set `GO_CLI_AGENT_ENV_FILE` to `webAuditLogPath(cfg.Session.Dir)`. Before the fix, `/api/config` returned HTTP 200 and left the submitted secret in the audit log file before JSONL audit events were appended.

Impact:

A misconfigured env-file target could place API-key material directly into the Web audit log despite the audit event payload intentionally omitting the secret. The resulting audit file would mix env assignments and JSONL events, breaking audit readability and leaking sensitive local credentials.

Minimal fix:

- Add a Web Settings preflight that rejects API-key env-file targets whose cleaned absolute path matches the Web audit log path.
- Run this preflight before config persistence, env-file persistence, process environment mutation, or audit append.
- Add focused WebConsole coverage asserting no config file, audit secret, or process env mutation occurs when env-file and audit-log targets alias.

Validation:

- Focused pre-fix WebConsole regression proving the aliased env/audit target returned HTTP 200.
- Focused post-fix WebConsole regression proving the alias is rejected before persistence.
- Standard grouped validation before commit.

### FCA-20260526-124: Web API-key blank env values persist unusable credentials

Severity: Low

Evidence:

- `spec/17-web-console.md` treats Settings API-key writes as sensitive local mutations that should give the operator clear success/failure behavior.
- `internal/webconsole/service.go` `handleUpdateConfig` treated any non-empty `api_key` string as a new API-key update, including whitespace-only values.
- The existing API-key preflight rejected malformed keys and NUL-containing values, but did not reject values whose trimmed form was empty.
- `config.Provider.ResolvedAPIKey` trims environment values before treating a key as usable, so a persisted whitespace-only key is later read as no key.
- A focused WebConsole regression submitted `api_key: "   \t  "`. Before the fix, `/api/config` returned HTTP 200, wrote the Settings config, and persisted an `OPENAI_API_KEY` entry that backend provider resolution treats as empty.

Impact:

Operators could receive a successful Settings save for an API key that the runtime later treats as missing. The env file and UI/action result imply a credential was written, while provider execution still fails as unauthenticated after reload or subsequent use.

Minimal fix:

- Reject API-key values whose trimmed form is empty in the existing Web API-key preflight.
- Keep the check before config persistence, env-file persistence, process environment mutation, or audit append.
- Add focused WebConsole coverage asserting blank values leave no config file, env file API-key entry, or process env mutation.

Validation:

- Focused pre-fix WebConsole regression proving whitespace-only API keys returned HTTP 200 and persisted.
- Focused post-fix WebConsole regression proving blank API-key values are rejected before persistence.
- Standard grouped validation before commit.

### FCA-20260526-125: Settings save masks empty API-key fields after success

Severity: Low

Evidence:

- `spec/17-web-console.md` requires Settings failures and form states to be clear to the local Web operator, especially for API-key writes.
- `internal/webconsole/assets/settings-view.js` used the same post-success branch for any API-key field whose current value was not the mask.
- When the backend reported `has_key=false` and the user saved other settings with the API-key field blank, the frontend still replaced the blank value with the mask and set `dataset.originalHasKey = "true"`.
- A focused Node renderer regression loaded Settings with `has_key: false`, clicked Save with an empty key, and observed the input becoming `••••••••••••••••` even though the submitted payload had `apiKey: ""`.

Impact:

The Settings screen could claim an API key existed immediately after a successful save that did not submit or persist one. Operators could then skip credential setup because the UI looked populated, while subsequent provider probes or sessions still had no usable key.

Minimal fix:

- Capture the normalized API-key payload before saving.
- Only mask the field and mark `originalHasKey=true` when a non-empty key was actually submitted.
- Leave empty API-key fields empty and marked as no-key after successful non-key Settings saves.
- Add focused Node renderer coverage for the empty-key save path.

Validation:

- Focused pre-fix Node renderer regression proving empty API-key fields were masked after save.
- Focused post-fix Node renderer regression proving empty API-key fields remain empty after save.
- Standard grouped validation before commit.

### FCA-20260526-126: Settings save clears existing-key mask after unchanged key save

Severity: Low

Evidence:

- `internal/webconsole/service.go` treats an empty `api_key` payload as "do not change the existing persisted key"; only a non-empty, non-mask value writes a new key.
- `internal/webconsole/assets/settings-view.js` allowed the user to clear the masked API-key input visually, then save other settings.
- After the previous FCA-20260526-125 fix, a successful save with `apiKey: ""` and `dataset.originalHasKey = "true"` left the field empty even though the backend did not delete the existing key.
- A focused Node renderer regression loaded Settings with `has_key: true`, cleared the API-key input, clicked Save, and observed the field remained empty instead of returning to the existing-key mask.

Impact:

The Settings screen could imply that an existing API key had been removed even though the backend retained it. Operators could believe credentials were cleared and later be surprised that provider probes or sessions still use the old key from the local environment.

Minimal fix:

- Keep the existing-key mask after successful saves where no new API key was submitted and the field originally represented an existing key.
- Preserve the FCA-20260526-125 behavior where providers with no key remain blank after non-key saves.
- Add focused Node renderer coverage for the "clear existing mask but leave key unchanged" path.

Validation:

- Focused pre-fix Node renderer regression proving the existing-key mask stayed empty after save.
- Focused post-fix Node renderer regression proving the existing-key mask is restored after unchanged-key saves.
- Standard grouped validation before commit.

### FCA-20260526-127: Session subresource routes accept unknown session IDs

Severity: Medium

Evidence:

- `spec/17-web-console.md` defines session control/detail APIs as scoped to local session file facts, and `spec/01-runtime-architecture.md` keeps session state/messages/events as the fact source.
- `internal/webconsole/service.go` correctly mapped `GET /api/sessions/{id}` missing `session.json` errors to HTTP 404, but subresource routes were dispatched without first proving the session metadata existed.
- `GET /api/sessions/{id}/children` called `ListChildren` / `ListJobsByParent`, both of which can naturally return empty lists for an unknown parent ID, so an unknown session looked like a valid session with no background work.
- `GET /api/sessions/{id}/tasks` called `LoadTodo` / `ListTasks`, both of which treat missing files/directories as empty state, so an unknown session could look like a valid empty task board.
- `GET /api/sessions/{id}/goal` treated missing `goal.json` as "no goal" and returned HTTP 200 `null`, even if the enclosing session itself did not exist.
- `POST /api/sessions/{id}/goal` could call `CreateGoal` without an existing `session.json`; the store write helpers create parent directories, so this could create orphaned `goal.json`, `goal-history.jsonl`, and event facts under a directory that was not a valid session.
- A focused pre-fix WebConsole regression showed `GET /api/sessions/missing_session_subresource/children` returned HTTP 200 with empty arrays instead of rejecting the unknown session.

Impact:

The Web API could hide stale links, mistyped session IDs, or deleted sessions behind valid-looking empty subresource responses. Worse, goal creation could materialize orphaned session directories without `session.json`, splitting durable goal/event artifacts away from a valid session metadata fact and confusing recovery, history cleanup, and operator diagnosis.

Minimal fix:

- Add a route-level `requireSession` check for all `/api/sessions/{id}/...` subresource routes before dispatching to goal, Plan Mode, mission, continue, steer, stop/interrupt, children, messages, or task handlers.
- Keep top-level `GET /api/sessions/{id}` behavior unchanged except for sharing the same missing-session / malformed-ID status mapper.
- Map missing session metadata to HTTP 404 and malformed store IDs to HTTP 400.
- Add focused WebConsole coverage for unknown `children`, `tasks`, `messages`, `goal` reads and unknown-session goal creation; assert goal creation leaves no partial session directory.

Validation:

- Focused pre-fix WebConsole regression proving `GET /children` for an unknown session returned HTTP 200.
- Focused post-fix WebConsole regression proving unknown session subresources return HTTP 404 and no orphan session directory is created by goal creation.
- Standard grouped validation before commit.

### FCA-20260526-128: Queue job detail treats malformed IDs as server failures

Severity: Low

Evidence:

- `spec/17-web-console.md` requires Web API errors to distinguish operator input errors from local infrastructure or durable-store failures.
- `internal/session/store.go` validates queue job IDs in `LoadJob` with `validateStoreID("queue job", jobID)`, rejecting path separators and traversal before any queue file lookup.
- `internal/webconsole/service.go` `handleShowJob` only mapped `fs.ErrNotExist` to HTTP 404; all other `LoadJob` errors fell through to HTTP 500.
- A focused pre-fix WebConsole regression showed `GET /api/queue/jobs/bad%2Fjob` returned HTTP 500 with `invalid queue job id "bad/job": path separators and traversal are not allowed`.

Impact:

Malformed queue job links or mistyped job IDs looked like WebConsole or local queue-store failures. That points operator recovery toward repairing durable queue state even though the request was invalid, and it makes the advanced queue REST surface inconsistent with the session route status mapper that already treats malformed store IDs as HTTP 400.

Minimal fix:

- Reuse the existing store-ID client error classifier in `handleShowJob`.
- Keep missing queue job facts mapped to HTTP 404.
- Keep queue store, reconciliation, and filesystem failures mapped to HTTP 500.
- Add focused WebConsole coverage for malformed job IDs and preserve missing-job `404` behavior.

Validation:

- Focused pre-fix WebConsole regression proving malformed queue job IDs returned HTTP 500.
- Focused post-fix WebConsole regression proving malformed queue job IDs return HTTP 400 while missing queue jobs still return HTTP 404.
- Standard grouped validation before commit.

### FCA-20260526-129: Session detail hides linked queue reconciliation failures

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` requires queue jobs and linked child session state to be explainable from durable file facts, and `spec/17-web-console.md` positions session detail as a Web view over those same facts.
- `internal/webconsole/service.go` `sessionDetail` intentionally called `s.store.LoadJob(meta.QueueJobID)` before reading state, so opening a queue-linked child session can reconcile terminal queue facts into the child `state.json`.
- The same call discarded its error with `_, _ = s.store.LoadJob(meta.QueueJobID)`, even though `LoadJob` can return write failures from `reconcileQueueJobSession`.
- A focused pre-fix WebConsole regression created a child session with `state.status=running`, a linked failed queue job, and an unwritable `state.lock`. `GET /api/sessions/{child}` still returned HTTP 200 with stale `running` state after the reconciliation write failed.

Impact:

The Web console could show a queue-linked child session as still running even when the queue job had already reached a terminal failed state and the store could not persist the required child-state repair. That weakens operator recovery, active-handle guidance, queue/child traceability, and the durable-state invariant that Web views should not hide failed fact reconciliation.

Minimal fix:

- Propagate linked queue job `LoadJob` / reconciliation errors from `sessionDetail`.
- Keep successful reconciliation behavior unchanged.
- Add focused WebConsole coverage for both successful linked queue reconciliation and failed reconciliation reporting.

Validation:

- Focused pre-fix WebConsole regression proving failed linked queue reconciliation returned HTTP 200 with stale state.
- Focused post-fix WebConsole regression proving the same path returns HTTP 500 and successful reconciliation still updates detail state.
- Standard grouped validation before commit.

### FCA-20260526-130: Session detail hides corrupt provider attempt facts

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` defines `provider-attempts.jsonl` as a recovery and diagnostic fact ledger, and `spec/18-durable-contract-and-completion.md` says session summaries and checkpoints read provider attempt facts from the durable ledger.
- `spec/17-web-console.md` requires the Summary panel to show provider attempt ledger facts, and the existing frontend utility tests cover rendering those facts.
- `internal/session/store.go` `LoadProviderAttempts` treats a missing ledger as an empty optional list but returns JSONL parse/read errors for corrupt or unreadable ledgers.
- `internal/webconsole/service.go` `sessionDetail` discarded the error with `providerAttempts, _ := s.store.LoadProviderAttempts(sessionID)`.
- A focused pre-fix WebConsole regression wrote invalid JSON to `provider-attempts.jsonl`; `GET /api/sessions/{id}` returned HTTP 200 and omitted provider attempts instead of surfacing the corrupt fact file.

Impact:

The Web console could present a normal-looking session detail while hiding a corrupt provider retry/timeout ledger. That weakens diagnosis of upstream retry behavior, timeout recovery, cache telemetry, and broad audit evidence because operators cannot tell whether there were no provider attempts or the durable attempt facts are unreadable.

Minimal fix:

- Propagate `LoadProviderAttempts` errors from `sessionDetail`, while preserving the store's missing-file-as-empty behavior.
- Also propagate `LoadArtifactTracker` errors instead of silently dropping corrupt required-artifact facts, since that helper already treats missing files as empty.
- Wrap these detail-load errors with the relevant fact-file name for actionable Web API responses.
- Add focused WebConsole coverage for corrupt `provider-attempts.jsonl`.

Validation:

- Focused pre-fix WebConsole regression proving corrupt provider attempts returned HTTP 200 with hidden ledger facts.
- Focused post-fix WebConsole regression proving corrupt provider attempts return HTTP 500 with the ledger filename in the response.
- Standard grouped validation before commit.

### FCA-20260526-131: Session detail hides corrupt goal history facts

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` and `spec/11-spec-audit-and-traceability.md` define `goal.json` plus `artifacts/goal-history.jsonl` as durable Goal facts, with Goal history feeding recovery and operator traceability.
- `spec/17-web-console.md` requires session detail to return Goal facts derived from `goal.json` / `goal-history.jsonl`, including recent history and coverage.
- `internal/session/goal.go` `LoadGoalHistory` treats a missing history ledger as an empty optional list but returns JSONL parse/read errors for corrupt or unreadable history ledgers.
- `internal/webconsole/service.go` `goalFacts` discarded the error with `history, _ := s.store.LoadGoalHistory(sessionID)`.
- A focused pre-fix WebConsole regression wrote invalid JSON to `artifacts/goal-history.jsonl`; `GET /api/sessions/{id}` returned HTTP 200 with empty Goal history facts instead of surfacing the corrupt durable fact file.

Impact:

The Web console could render a normal-looking Goal inspector while hiding corrupt Goal history. Operators could not distinguish "no history" from "history unreadable", weakening mission approval, progress/handoff, budget, and completion-audit traceability.

Minimal fix:

- Make `goalFacts` return `LoadGoalHistory` errors while preserving missing-file-as-empty store behavior.
- Propagate the error through `sessionDetail`.
- Wrap the error with `goal-history.jsonl` so the Web API response points at the corrupt fact file.
- Add focused WebConsole coverage for corrupt `artifacts/goal-history.jsonl`.

Validation:

- Focused pre-fix WebConsole regression proving corrupt Goal history returned HTTP 200 with empty history facts.
- Focused post-fix WebConsole regression proving corrupt Goal history returns HTTP 500 with the ledger filename in the response.
- Standard grouped validation before commit.

### FCA-20260526-132: Session detail hides corrupt optional snapshot facts

Severity: Medium

Evidence:

- `spec/17-web-console.md` defines session detail as returning contract, long-run checkpoint, parent coordination, current Goal snapshot, and current Plan Mode snapshot facts.
- `spec/01-runtime-architecture.md`, `spec/11-spec-audit-and-traceability.md`, and `spec/18-durable-contract-and-completion.md` make those snapshots durable session facts rather than Web-maintained state.
- `internal/session/store.go`, `internal/session/goal.go`, and `internal/session/planmode.go` return `fs.ErrNotExist` for absent optional snapshot files but return parse/read errors for corrupt JSON snapshots.
- `internal/webconsole/service.go` `sessionDetail` collapsed both cases with `if snapshot, err := Load...; err == nil ...`, silently omitting corrupt `contract.json`, `checkpoints/longrun-latest.json`, `parent-coordination.json`, `goal.json`, and `planmode.json`.
- A focused pre-fix WebConsole table regression wrote invalid JSON to each snapshot path; every `GET /api/sessions/{id}` returned HTTP 200 and omitted the corresponding fact instead of surfacing the corrupt local file.

Impact:

The Web console could hide corrupt session authority facts and render a clean-looking detail page. That weakens recovery and operator traceability for contract/completion gates, long-run resume hints, parent child/queue waits, Goal state, and Plan Mode execution gates.

Minimal fix:

- In `sessionDetail`, continue treating missing optional snapshot files as absent.
- For non-missing snapshot load failures, return HTTP 500 through the existing detail error path.
- Wrap each load error with the relevant fact filename.
- Add focused table coverage for corrupt contract, checkpoint, parent coordination, Goal, and Plan Mode snapshots.

Validation:

- Focused pre-fix WebConsole table regression proving corrupt snapshot files returned HTTP 200 with omitted facts.
- Focused post-fix WebConsole table regression proving corrupt snapshot files return HTTP 500 with the relevant fact filename.
- Standard grouped validation before commit.

### FCA-20260526-133: Web linked Plan Mode creation hides event append failures

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` lists `planmode.created` as a structured session event, and `spec/17-web-console.md` requires Web Plan Mode controls to reuse `planmode.json`, `artifacts/planmode-history.jsonl`, and session events rather than a second Web state source.
- Web Goal create, generic Goal patch, mission-plan patch, mission validation patch, and mission plan approve can call `EnsurePlanModeForGoal`, which creates `planmode.json` and appends Plan Mode history.
- `internal/webconsole/service.go` then attempted to append a `planmode.created` event, but discarded the append error with `_ = s.store.AppendEvent(...)`.
- A focused pre-fix WebConsole regression blocked `events.jsonl` during mission plan approval that creates a linked Plan Mode; the API returned the normal HTTP 409 conflict and left the missing event hidden instead of reporting the failed durable event write.

Impact:

The Web API could report a normal user-action conflict or success after creating a linked Plan Mode gate while losing the required `planmode.created` session event. That weakens timeline/recovery traceability for approval gates and can make operators believe a failed local event write was just ordinary Plan Mode status conflict.

Minimal fix:

- Add a shared Web helper for appending linked `planmode.created` events.
- Treat append failures as HTTP 500.
- Reuse existing rollback paths to restore Goal/task/Plan Mode facts when the event write fails after linked Plan Mode creation.
- Add focused WebConsole coverage for mission plan approval hiding a linked Plan Mode event append failure.

Validation:

- Focused pre-fix WebConsole regression proving blocked `events.jsonl` returned HTTP 409 and hid the missing `planmode.created` event.
- Focused post-fix WebConsole regression proving the same path returns HTTP 500 and restores the pre-request Goal/Plan Mode facts.
- Adjacent Web rollback regressions for Goal create, generic Goal patch, mission-plan patch, and mission validation patch.
- Standard grouped validation before commit.

### FCA-20260526-134: Session lists hide corrupt Goal and Plan Mode summary snapshots

Severity: Medium

Evidence:

- `spec/17-web-console.md` requires the Sessions list and recent session rail to show Goal / Plan Mode summary facts from the selected local session state, while `spec/01-runtime-architecture.md` and `spec/18-durable-contract-and-completion.md` define `goal.json` and `planmode.json` as durable session fact sources.
- `internal/session/store.go` enriches `SessionSummary` through `populateGoalSummary` and `populatePlanModeSummary`, which called `LoadGoal` / `LoadPlanMode` and returned early for every error.
- Because `LoadGoal` / `LoadPlanMode` return `fs.ErrNotExist` for absent optional snapshots but JSON parse/read errors for corrupt snapshots, the summary path collapsed "no snapshot exists" and "snapshot is corrupt" into the same empty summary.
- A focused pre-fix WebConsole regression wrote invalid JSON to `goal.json` and `planmode.json`; both `/api/sessions` and `/api/history` returned HTTP 200 and omitted the corrupt fact fields instead of reporting the local file corruption.

Impact:

The Web console could show a clean Sessions or History list while hiding corrupt current Goal or Plan Mode snapshots. Operators could not distinguish a session with no Goal / Plan Mode from a session whose durable objective or execution-gate facts were unreadable, weakening recovery, approval-gate traceability, and audit evidence.

Minimal fix:

- Make `populateGoalSummary` and `populatePlanModeSummary` return errors.
- Continue treating `fs.ErrNotExist` as an absent optional summary.
- Propagate non-missing `goal.json` and `planmode.json` load failures through `List`, `ListPage`, and `ListChildren`.
- Add store-level and WebConsole regressions for corrupt summary snapshots.

Validation:

- Focused pre-fix WebConsole regression proving corrupt `goal.json` and `planmode.json` returned HTTP 200 from `/api/sessions`.
- Focused post-fix store regression proving `List` / `ListPage` report the corrupt snapshot filename.
- Focused post-fix WebConsole regression proving `/api/sessions` and `/api/history` return HTTP 500 with the corrupt snapshot filename.
- Standard grouped validation before commit.

### FCA-20260526-135: Task graph readers hide corrupt task files

Severity: High

Evidence:

- `spec/12-task-system.md` defines `tasks/task_*.json` as the durable persistent task graph, and `spec/01-runtime-architecture.md` requires runtime context loading to read the current task graph from durable state.
- `internal/session/store.go` `ListTasks` and `listTasksLocked` iterated `tasks/*.json`, called `readJSONFile`, and appended a task only when `err == nil`.
- `MutateTasks` uses `listTasksLocked` before writing the exact task set through `SaveTasks`, so a corrupt task file was not only hidden from readers but could be omitted from the next task mutation snapshot.
- A focused pre-fix store regression wrote invalid JSON to `tasks/task_0002.json`; `ListTasks` returned nil error, and the corrupt file was not reported to the caller.

Impact:

The runtime, CLI, SDK, Web session detail, Web task board, compaction context, and task mutations could treat a corrupt durable task graph as a smaller valid graph. A subsequent `task_create` / `task_update` / mission task sync could proceed from that incomplete snapshot and rewrite the task directory, making recovery harder and weakening long-task handoff evidence.

Minimal fix:

- Replace the duplicated task-directory loops with a shared `readTasks` helper.
- Continue treating a missing `tasks/` directory as an empty optional task graph for sessions without durable tasks.
- Return non-missing task file read/parse failures with the `tasks/<file>.json` filename.
- Add store coverage for read and mutation paths, and WebConsole coverage for session detail and `GET /api/sessions/{id}/tasks`.

Validation:

- Focused pre-fix store regression proving corrupt `tasks/task_0002.json` returned nil error from `ListTasks`.
- Focused post-fix store regression proving `ListTasks` and `CreateTask` report `tasks/task_0002.json` and preserve the corrupt file.
- Focused post-fix WebConsole regression proving session detail and task-board endpoints return HTTP 500 with `tasks/task_0002.json`.
- Standard grouped validation before commit.

### FCA-20260526-136: Plan Mode continue hides corrupt snapshot filename

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` and `spec/18-durable-contract-and-completion.md` define `planmode.json` as the durable Plan Mode execution-gate fact source.
- `spec/00-product.md` documents that a normal `continue` message under `awaiting_approval` is treated as a Plan Mode revision, so `Runner.Continue` must load the current Plan Mode snapshot before accepting that message.
- `internal/runtime/runner.go` loaded `planmode.json` in the normal-message `Continue` path but only handled the success case; every load error, including corrupt JSON in an existing `planmode.json`, was ignored.
- The engine later reloaded Plan Mode during preparation and failed with a raw JSON parser error. A focused pre-fix runtime regression wrote invalid JSON to `planmode.json`; `Continue` failed before provider execution, but the returned error was `invalid character 'n' looking for beginning of object key string` and did not identify `planmode.json`.

Impact:

CLI and Web fallback `continue` recovery could surface a non-actionable raw JSON error for a corrupt Plan Mode gate instead of naming the unreadable durable fact file. Because the corrupt gate was not rejected at the revision-message branch, continuation preparation could advance past the point where it should have failed, weakening Plan Mode recovery diagnostics and making it harder to distinguish "no Plan Mode" from "Plan Mode snapshot is corrupt."

Minimal fix:

- In the normal-message `Continue` path, continue treating `fs.ErrNotExist` as "no Plan Mode".
- For any other `LoadPlanMode` error, fail before checkpoint injection, continuation message append, or provider execution.
- Wrap the error with `load planmode.json` so the failure names the corrupt durable snapshot.
- Add a focused runtime regression proving the corrupt snapshot returns an error containing `planmode.json`, does not call the provider, and does not append the continuation user message.

Validation:

- Focused pre-fix runtime regression proving corrupt `planmode.json` returned a raw JSON parser error without the snapshot filename.
- Focused post-fix runtime regression proving corrupt `planmode.json` is reported before provider execution or continuation message append.
- Standard grouped validation before commit.

### FCA-20260526-137: Plan Mode tool gate fails open on corrupt snapshot

Severity: High

Evidence:

- `spec/01-runtime-architecture.md` and `spec/18-durable-contract-and-completion.md` require pending Plan Mode to be enforced by the centralized completion/tool gate, using `planmode.json` as the durable execution-gate fact source.
- `spec/11-spec-audit-and-traceability.md` states that pending Plan Mode must have provider schema filtering and `CompletionController` gate enforcement aligned, with mutating tools blocked before approval.
- `internal/runtime/completion_controller.go` `planModeGate` called `LoadPlanMode` and returned no gate whenever `err != nil`, collapsing an absent optional Plan Mode snapshot and a corrupt existing `planmode.json` into the same allow path.
- A focused pre-fix runtime regression wrote invalid JSON to `planmode.json` and called `EvaluateToolCall` for `write_file`; the decision was `GateAllow` instead of a Plan Mode state block.

Impact:

If `planmode.json` became corrupt between provider request preparation and tool execution, the controller could allow a mutating tool instead of failing closed on the unreadable execution-gate fact. That weakens the Plan Mode hard-guard boundary and creates a mismatch between the store's absent-versus-corrupt distinction and the runtime gate that is supposed to enforce approval.

Minimal fix:

- Change `planModeGate` to treat only `fs.ErrNotExist` as "no Plan Mode".
- Return a blocking `plan_mode_state` decision for every other Plan Mode load error.
- Include `planmode.json` in the model/operator message so the corrupt durable snapshot is actionable.
- Add a focused runtime regression proving corrupt Plan Mode gate state blocks `write_file` with the snapshot filename.

Validation:

- Focused pre-fix runtime regression proving corrupt `planmode.json` allowed `write_file`.
- Focused post-fix runtime regression proving corrupt `planmode.json` blocks `write_file` with `plan_mode_state`.
- Standard grouped validation before commit.

### FCA-20260526-138: Required-artifact gate hides corrupt contract mirror

Severity: Medium

Evidence:

- `spec/18-durable-contract-and-completion.md` defines `contract.json` as the snapshot of explicit user contracts and `artifact-tracker.json` as the per-required-artifact tracker derived from that contract.
- The same spec says explicit user contracts and required artifact gates are not bypassed by `yolo`, while `session.md` and checkpoints are derived views, not fact sources.
- `internal/runtime/completion_controller.go` `TrackToolResult` updated `artifact-tracker.json` and then attempted to mirror the refreshed required-artifact status back into `contract.json`, but ignored every `LoadContract` error.
- `requiredArtifactGate` likewise refreshed and saved `artifact-tracker.json`, then ignored every `LoadContract` error before deciding whether `finish` could proceed.
- Focused pre-fix runtime regressions wrote invalid JSON to `contract.json`: `TrackToolResult` returned nil, and `EvaluateToolCall(..., "finish", ...)` returned `GateAllow` instead of surfacing the corrupt contract fact.

Impact:

A corrupt `contract.json` could be hidden while artifact tracking continued from `artifact-tracker.json`, leaving the two required-artifact fact files diverged. In the finish path, a satisfied tracker could allow completion even though the explicit contract snapshot was unreadable, weakening recovery diagnostics and the durable contract boundary for required artifacts.

Minimal fix:

- Preserve missing `contract.json` compatibility for legacy or non-contract sessions by ignoring only `fs.ErrNotExist`.
- Return `load contract.json` from `TrackToolResult` for every other contract load failure.
- Return a `required_artifact_state` block from `requiredArtifactGate` when contract mirroring cannot load the existing contract snapshot.
- Add focused runtime coverage for both write tracking and finish-gate paths with corrupt `contract.json`.

Validation:

- Focused pre-fix runtime regressions proving corrupt `contract.json` was hidden by both write tracking and finish gate.
- Focused post-fix runtime regressions proving corrupt `contract.json` is reported from write tracking and blocks finish.
- Standard grouped validation before commit.

### FCA-20260526-139: Parent completion gate hides corrupt background wait state

Severity: High

Evidence:

- `spec/18-durable-contract-and-completion.md` defines `parent-coordination.json` and background notifications as durable parent/child completion facts that block parent `finish` while unresolved or unaccepted child/queue work remains.
- `spec/01-runtime-architecture.md` requires session/state/messages/events and related session files to be durable fact sources, not in-memory-only state.
- `internal/runtime/completion_controller.go` `parentCoordinationGate` called `LoadBackgroundNotifications` and only inspected pending notifications when `err == nil`, silently ignoring corrupt `control/background.jsonl`.
- The same gate called `LoadParentCoordination` and returned no gate for every error, collapsing an absent optional coordination snapshot and a corrupt existing `parent-coordination.json`.
- Focused pre-fix runtime regressions wrote invalid JSON to each file; `EvaluateToolCall(..., "finish", ...)` returned `GateAllow` in both cases.

Impact:

A parent session could finish while durable background result-acceptance facts or explicit parent coordination wait state were unreadable. That weakens child/queue traceability, can bypass unresolved work gates, and makes recovery treat corrupt wait-state files as if no child or queue work existed.

Minimal fix:

- Treat background-notification load errors as `parent_background_state` finish blocks; the store already returns an empty slice for a missing notification log.
- Treat only missing `parent-coordination.json` as no coordination state.
- Return `parent_coordination_state` for corrupt or unreadable parent coordination snapshots.
- Include `control/background.jsonl` or `parent-coordination.json` in the gate messages.
- Add focused runtime regressions for both corrupt wait-state files.

Validation:

- Focused pre-fix runtime regressions proving corrupt background notification and parent coordination facts allowed `finish`.
- Focused post-fix runtime regressions proving corrupt background notification and parent coordination facts block `finish`.
- Standard grouped validation before commit.

### FCA-20260526-140: Goal completion gate hides corrupt Goal snapshot

Severity: High

Evidence:

- `spec/01-runtime-architecture.md` defines `goal.json` as the current session Goal snapshot, and `spec/18-durable-contract-and-completion.md` requires active goals to block `finish` until the model performs the completion audit and calls `update_goal(status="complete")`.
- `spec/11-spec-audit-and-traceability.md` states that completion evidence and status must be readable from the current Goal snapshot, not reconstructed only from history.
- `internal/runtime/completion_controller.go` `goalCompletionGate` called `LoadGoal` and returned no gate for every error, collapsing a missing optional goal and a corrupt existing `goal.json`.
- A focused pre-fix runtime regression wrote invalid JSON to `goal.json`; `EvaluateToolCall(..., "finish", ...)` returned `GateAllow`.

Impact:

A session with a corrupt active Goal snapshot could finish as if no Goal existed. That bypasses the active-goal completion audit, weakens budget-limited wrap-up semantics, and makes recovery unable to distinguish a session without a Goal from one whose durable Goal fact is unreadable.

Minimal fix:

- Treat only missing `goal.json` as "no Goal".
- Return a blocking `goal_state` decision for corrupt or unreadable Goal snapshots.
- Include `goal.json` in the gate message.
- Add focused runtime coverage for corrupt Goal snapshot finish gating.

Validation:

- Focused pre-fix runtime regression proving corrupt `goal.json` allowed `finish`.
- Focused post-fix runtime regression proving corrupt `goal.json` blocks `finish`.
- Standard grouped validation before commit.

### FCA-20260526-141: Steer Goal-history path hides corrupt Goal snapshot filename

Severity: Medium

Evidence:

- `spec/13-live-input-and-steering.md` requires accepted steer input to append a real user message, refresh contract/artifact facts, and, when a session has a current Goal, write `goal.updated` history and emit Goal-related events.
- `spec/18-durable-contract-and-completion.md` and `spec/11-spec-audit-and-traceability.md` define `goal.json` as the current Goal snapshot required for completion evidence and recovery.
- `internal/runtime/goal.go` `appendGoalHistoryForSteer` called `LoadGoal` but returned nil for every error, treating corrupt `goal.json` the same as no current Goal.
- A focused pre-fix runtime regression wrote invalid JSON to `goal.json` and accepted a steer. The provider was not reached, but the failure surfaced later as a raw JSON parser error without `goal.json`, after the steer message had already been appended and the Goal-history path skipped the corrupt snapshot.

Impact:

Accepted steer recovery could obscure the corrupt Goal fact that prevented Goal-history recording. Operators saw a non-actionable JSON parser error instead of the unreadable `goal.json` snapshot, and the runtime skipped the Goal update/history decision point before failing later during contract refresh. This weakens steer/Goal traceability and restart diagnostics.

Minimal fix:

- In `appendGoalHistoryForSteer`, continue treating missing `goal.json` as "no current Goal".
- Return `load goal.json` for any other Goal load failure.
- Keep existing Goal-history append error propagation unchanged.
- Add focused runtime coverage for corrupt `goal.json` during steer acceptance.

Validation:

- Focused pre-fix runtime regression proving corrupt `goal.json` during steer returned a raw JSON parser error without the filename.
- Focused post-fix runtime regression proving corrupt `goal.json` during steer returns an actionable `goal.json` error before provider execution.
- Standard grouped validation before commit.

### FCA-20260526-142: Plan Mode creation ignores corrupt linked Goal snapshot

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` and `spec/18-durable-contract-and-completion.md` require Goal/mission plan approval to reuse linked Plan Mode gates; `planmode.json` may carry `linked_goal_id` pointing to the current Goal.
- `internal/session/planmode.go` `CreatePlanMode` attempted to discover the current Goal by loading `goal.json`, but only used the success case and ignored every load error.
- A focused pre-fix store regression wrote invalid JSON to `goal.json` and called `CreatePlanMode`; the call returned nil error and created an unlinked `planmode.json`.

Impact:

Plan Mode creation could proceed as if no Goal existed while the current Goal snapshot was actually corrupt. That can create an unlinked approval gate, weakening Goal/Plan traceability and making recovery harder because the durable Goal authority was unreadable at the moment a new gate was created.

Minimal fix:

- Keep missing `goal.json` as the optional no-Goal case.
- Return `load goal.json` for corrupt or unreadable Goal snapshots before writing `planmode.json`.
- Add focused store coverage proving failed creation does not leave a Plan Mode snapshot.

Validation:

- Focused pre-fix store regression proving corrupt `goal.json` allowed unlinked Plan Mode creation.
- Focused post-fix store regression proving corrupt `goal.json` is reported and no Plan Mode snapshot is left behind.
- Standard grouped validation before commit.

### FCA-20260526-143: Pre-completion feature gate hides corrupt feature list

Severity: Medium

Evidence:

- `spec/18-durable-contract-and-completion.md` lists pre-completion feature checks as a `CompletionController` finish gate.
- `spec/04-tools-and-skills.md` defines `feature_list_create` / `feature_list_update` / `feature_list_read` as durable feature-list tools for long-running feature convergence.
- `internal/runtime/completion_controller.go` `EvaluatePreCompletionFeatures` called `LoadFeatureList` and returned `GateAllow` for every load error, collapsing an absent optional feature list, a path-safety rejection, and a corrupt existing `feature_list.json`.
- A focused pre-fix runtime regression wrote invalid JSON to `feature_list.json`; `EvaluatePreCompletionFeatures(true)` returned `GateAllow`.

Impact:

In init-mode runs with pre-completion feature checks enabled, a session could call `finish` while its durable feature list was unreadable. That weakens the feature convergence gate and makes recovery treat a corrupt feature list like no feature list existed, even though the file was present and could contain unfinished feature state.

Minimal fix:

- Continue allowing absent `feature_list.json` because the feature list is optional.
- Preserve the existing symlink/path-safety behavior that ignores symlinked session feature lists rather than treating unsafe external content as completion evidence.
- Return a blocking `pre_completion_state` decision for corrupt or otherwise unreadable feature-list snapshots.
- Include `feature_list.json` in the gate message.
- Add focused runtime coverage for corrupt feature-list finish gating while keeping the symlink regression green.

Validation:

- Focused pre-fix runtime regression proving corrupt `feature_list.json` allowed pre-completion `finish`.
- Focused post-fix runtime regression proving corrupt `feature_list.json` blocks pre-completion `finish`.
- Existing symlink/path-safety regression proving symlinked feature-list state remains ignored.
- Standard grouped validation before commit.

### FCA-20260526-144: Plan Mode revision retry hides corrupt revision history

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` defines `planmode.json` and `artifacts/planmode-history.jsonl` as Plan Mode session facts and requires Plan Mode transitions to be recoverable across restart/fallback paths.
- `spec/11-spec-audit-and-traceability.md` states Plan Mode status, approval, revision, and execution transitions must be recorded as durable facts rather than inferred from transient process state.
- `internal/runtime/runner.go` `ensurePlanModeRevisedForMessage` handles the recovery case where `RevisePlanMode` already wrote `planmode.plan_revised` history but appending the replay user message failed. In the `planning` state, it calls `hasMatchingPlanModeRevisionHistory` to decide whether a repeated `continue --message` should be reclassified as the missing `planmode_revision` replay message.
- `hasMatchingPlanModeRevisionHistory` returned `false` for every `LoadPlanModeHistory` error, collapsing "no matching revision history" and corrupt existing `artifacts/planmode-history.jsonl`.
- A focused pre-fix runtime regression reproduced the sequence: block `messages.jsonl` during revision, retry after unblocking messages, corrupt `artifacts/planmode-history.jsonl`, then retry the same message. Before the fix, the provider ran and the session reached `awaiting_input` with the revision text treated as an ordinary user message.

Impact:

Plan Mode revision recovery could lose the semantic replay fact required by provider history and operator traceability. When the revision history ledger was corrupt, the retry path treated the repeated revision text as a normal continuation, allowing provider execution while the durable fact needed to recover the missing `planmode_revision` message was unreadable.

Minimal fix:

- Change `hasMatchingPlanModeRevisionHistory` to return `(bool, error)` instead of hiding load failures.
- Propagate corrupt or unreadable `planmode-history.jsonl` from `ensurePlanModeRevisedForMessage` before appending a continuation message or running the provider.
- Add filename context to the reported error.
- Keep the normal no-matching-history path as an ordinary planning continuation.
- Add focused runtime coverage for corrupt history during Plan Mode revision retry and keep the existing successful retry coverage green.

Validation:

- Focused pre-fix runtime regression proving corrupt `planmode-history.jsonl` allowed provider execution with an ordinary continuation message.
- Focused post-fix runtime regression proving corrupt `planmode-history.jsonl` stops retry before provider execution.
- Existing Plan Mode revision retry regression proving missing replay user messages are still recovered when history is readable.
- Standard grouped validation before commit.

### FCA-20260526-145: Linked mission approval retry hides corrupt Goal history

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` defines `goal.json` and `artifacts/goal-history.jsonl` as durable Goal / mission facts, and requires linked Plan Mode approval to synchronize mission-plan approval facts.
- `spec/18-durable-contract-and-completion.md` requires mission plan approval to be recorded as durable Goal state/history, while Plan Mode remains the execution gate for approval.
- `internal/runtime/runner.go` `approveLinkedMissionPlan` has a retry path for the case where `ApproveMissionPlan` already wrote `mission.plan.approved` Goal history but appending the matching session event failed.
- That retry path calls `hasMissionPlanApprovedHistory` to avoid appending duplicate approval history, but `hasMissionPlanApprovedHistory` returned `false` for every `LoadGoalHistory` error, collapsing "no approval history" with corrupt existing `artifacts/goal-history.jsonl`.
- A focused pre-fix runtime regression blocked `events.jsonl` during linked mission approval, then unblocked events and corrupted `artifacts/goal-history.jsonl`. Before the fix, the retry returned nil instead of reporting the unreadable Goal history ledger.

Impact:

Linked Plan Mode approval recovery could proceed while the Goal history ledger needed to prove the prior mission approval was unreadable. That weakens mission approval traceability and can make retry behavior depend on the current `goal.json` snapshot while hiding the corrupted append-only approval history.

Minimal fix:

- Change `hasMissionPlanApprovedHistory` to return `(bool, error)` instead of hiding load failures.
- Propagate corrupt or unreadable `goal-history.jsonl` from `approveLinkedMissionPlan` before appending the missing approval event or attempting another approval write.
- Add filename context to the reported error.
- Keep the readable-history idempotency path unchanged so event retry does not duplicate mission approval history.
- Add focused runtime coverage for corrupt Goal history during linked mission approval retry.

Validation:

- Focused pre-fix runtime regression proving corrupt `goal-history.jsonl` was hidden during linked mission approval retry.
- Focused post-fix runtime regression proving corrupt `goal-history.jsonl` is reported before appending the missing event.
- Existing linked mission approval retry regression proving readable history still prevents duplicate approval history.
- Standard grouped validation before commit.

### FCA-20260526-146: Session lists hide corrupt state snapshots

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` defines `state.json` as a core `SessionStore` fact alongside `session.json`, `messages.jsonl`, and `events.jsonl`.
- `spec/09-phase-plan.md` includes the `sessions` command in Phase 3 and requires Web-first v1 to validate session start / steer / continue and Web session views against the same local session facts.
- `internal/session/store.go` `listAllSessions` loaded `session.json`, then silently `continue`d on `LoadState` errors, so `List` and `ListPage` skipped real sessions whose metadata was readable but whose `state.json` was corrupt.
- `internal/session/store.go` `ListChildren` used the same pattern after matching a readable child `session.json` by `parent_session_id`, so corrupt child state was hidden from parent/child views.
- A focused pre-fix store regression corrupted `state.json` after creating a valid parent-linked session; before the fix, `List(10)` returned nil error instead of reporting the unreadable state snapshot.

Impact:

Web and CLI session lists could disappear a real session when only its durable state snapshot was unreadable. That weakens recovery and operator diagnosis because a corrupt `state.json` looked like no session or no child existed, while adjacent corrupt summary facts (`goal.json` / `planmode.json`) were already reported.

Minimal fix:

- Keep skipping unreadable metadata so stray directories under the session root are not treated as sessions.
- After `session.json` loads successfully, report `state.json` load failures from `List`, `ListPage`, and `ListChildren`.
- Preserve existing corrupt `goal.json` / `planmode.json` summary reporting.
- Add focused store coverage for corrupt state snapshots across root list, paged list, and child list paths.

Validation:

- Focused pre-fix store regression proving corrupt `state.json` was hidden by `List`.
- Focused post-fix store regression proving corrupt `state.json` is reported by `List`, `ListPage`, and `ListChildren`.
- Existing corrupt Goal/Plan summary snapshot regression remains green.
- Standard grouped validation before commit.

### FCA-20260526-147: Queue reconciliation marks corrupt linked sessions as missing

Severity: High

Evidence:

- `spec/01-runtime-architecture.md` requires queue reconciliation to converge only from file facts that prove the linked session state, and defines queue job leases / liveness as durable facts.
- `spec/17-web-console.md` requires queue/job/session failure states to preserve visible error summaries and requires queue completion/failure to be observable through real child sessions and queue job records.
- `internal/session/store.go` `findSessionForQueueJob` returned `ok=false` for `LoadState` errors and `LoadMessages` errors after finding a session whose `session.json` matched the queue job ID.
- `internal/session/store.go` `reconcileQueueJobSession` treated that `ok=false` as "no linked session"; for stale running jobs, it rewrote the queue job to failed with `queue job stale: running job has no linked session and heartbeat is stale`.
- A focused pre-fix store regression created a stale running queue job linked to a real child session, corrupted either `state.json` or `messages.jsonl`, then called `LoadJob`; before the fix, the job was marked failed as an orphan and no corrupt linked-session file was reported.

Impact:

Queue recovery could convert an unreadable linked child session into a false orphan failure. That loses the distinction between "no linked session exists" and "the linked session facts are corrupt", can mutate parent coordination and notifications from an unproven premise, and weakens Web/CLI queue diagnostics.

Minimal fix:

- Change `findSessionForQueueJob` to return a load error separately from the not-found result.
- Continue skipping unreadable metadata for unrelated session-root entries, but once metadata matches the queue job ID, propagate corrupt `state.json` and `messages.jsonl`.
- Stop `reconcileQueueJobSession` before stale-orphan repair when linked session facts are unreadable.
- Add focused store coverage proving corrupt linked session facts are reported and the running queue job is not rewritten as an orphan failure.

Validation:

- Focused pre-fix store regression proving corrupt linked `state.json` / `messages.jsonl` made `LoadJob` mark the queue job failed as having no linked session.
- Focused post-fix store regression proving corrupt linked session facts are reported and the persisted queue job remains running.
- Existing stale-linked and completed-session reconciliation regressions remain green.
- Standard grouped validation before commit.

### FCA-20260526-148: Queue lists and claims hide corrupt queue job files

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` defines `QueueStore And Worker` as durable queue job storage with claim leases, heartbeat, and file-fact-based liveness.
- `spec/17-web-console.md` requires queue job statuses and failures to remain visible in the Background inspector / API rather than disappearing from operator views.
- `internal/session/store.go` `listQueueJobCopies` silently skipped every `readJSONFile` error for `*.json` files under queue status directories.
- `internal/session/store.go` `ClaimNextQueuedJob` used the same silent skip for queued job JSON read errors before building the claim candidate list.
- Focused pre-fix store regressions corrupted valid queue job files named `<job_id>.json`; before the fix, `ListJobs` returned nil error with no item and `ClaimNextQueuedJob` returned `(zero, false, nil)`.

Impact:

A corrupt durable queue job file could disappear from Web/CLI queue views and from worker claim attempts. Operators would see no queued work or no matching parent job even though a real queue fact file existed, weakening recovery and making queue liveness depend on silent omission rather than durable error reporting.

Minimal fix:

- Treat valid queue job filenames (`<valid-id>.json`) as durable queue facts whose unreadable JSON must be reported.
- Keep existing skip behavior for directories, non-JSON files, invalid filenames, mismatched valid JSON, invalid job IDs, and invalid job statuses.
- Add focused store coverage for corrupt queue job files in `ListJobs`, `ListJobsPage`, `ListJobsByParent`, and `ClaimNextQueuedJob`.
- Keep the existing mismatched-filename claim regression green.

Validation:

- Focused pre-fix store regressions proving corrupt queue job files were hidden by list and claim paths.
- Focused post-fix store regressions proving corrupt queue job files are reported by list and claim paths.
- Existing mismatched valid JSON regression remains green.
- Standard grouped validation before commit.

### FCA-20260526-167: Queue worker completes child jobs after corrupt handoff logs

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` requires child handoff to depend on visible file facts, and `QueueStore And Worker` must write child completion results back to parent session control notifications.
- `spec/09-phase-plan.md` says the large-project queue profile requires real job/session association and background notification evidence rather than compatibility shells.
- `internal/runtime/delegation.go` `ProcessNextJob` reloaded the child session metadata and messages after `childRunner.Start` so it could populate `EffectiveWorkdir` and `VisiblePaths`, but it discarded both load errors.
- `internal/session/store.go` queue reconciliation already treats corrupt linked child `messages.jsonl` as an error, showing that malformed child logs are not a valid no-output case.
- A focused pre-fix runtime regression used a real `session.complete` hook to corrupt the child `messages.jsonl` after the child wrote its final tool result but before the parent queue job derived handoff facts. Before the fix, the queue job was marked `completed` with empty `VisiblePaths` and no error.

Impact:

A queue worker could report a child job as completed even though the child session facts needed for parent handoff were corrupt. Parent sessions and WebConsole notifications could then receive a successful background result without the visible output paths that prove what the child produced, weakening large-project handoff recovery.

Minimal fix:

- Treat child metadata reload failures after `childRunner.Start` as queue handoff failures.
- Treat child `messages.jsonl` reload failures after `childRunner.Start` as queue handoff failures, with the corrupt source filename in `LastError`.
- Persist the queue job as `failed` and propagate the failure through the existing background notification path rather than returning a worker-level error.
- Add focused runtime coverage that corrupts child `messages.jsonl` and `session.json` through real session-complete hooks and proves the queue job/background notification record the failure.

Validation:

- Focused pre-fix runtime regression proving corrupt child `messages.jsonl` was hidden and the queue job completed with missing visible paths.
- Focused post-fix runtime regression proving corrupt child handoff logs fail the queue job and persist a background notification with a `messages.jsonl` error.
- Focused post-fix runtime regression proving corrupt child metadata fails the queue job and persists a background notification with a `session.json` error.
- Existing queue visible-output copy and failed-job lifecycle regressions remain green.
- Standard grouped validation before commit.

### FCA-20260526-168: Direct delegate reports success after corrupt child handoff logs

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` requires child handoff to depend on visible file facts rather than process-local context.
- `spec/11-spec-audit-and-traceability.md` says structured handoff depends on durable artifacts being complete and fresh, and `agent_role` / child linkage must remain traceable through session metadata and results.
- `internal/runtime/delegation.go` `SpawnAgent` reloaded the child session metadata and messages after synchronous `childRunner.Start` so direct `agent_spawn` / `Delegate` results could include the child workdir, role, and visible output paths, but it discarded child `messages.jsonl` load errors.
- The queue worker handoff path now treats corrupt child logs as a failed handoff, so direct synchronous delegation had weaker durable-fact handling than background queue delegation.
- A focused pre-fix runtime regression used a real `session.complete` hook to corrupt the child `messages.jsonl` after the child completed. Before the fix, `Delegate` returned `completed` with empty `VisiblePaths` and no error.

Impact:

A parent agent could receive a successful synchronous delegate result even though the child session log needed to derive visible outputs was corrupt. Parent synthesis could then proceed without the artifacts or diagnostics that prove what the child produced, and parent coordination could classify the child as completed.

Minimal fix:

- Treat direct delegate child `messages.jsonl` reload failures after `childRunner.Start` as child handoff failures.
- Return the child session ID with `Status=failed` and `LastError` naming `messages.jsonl`, so operators can inspect or repair the child session.
- Resolve parent child coordination as failed for corrupt direct-delegate handoff results.
- Keep existing successful visible-output sync behavior unchanged.

Validation:

- Focused pre-fix runtime regression proving corrupt direct-delegate `messages.jsonl` was hidden and the delegate result completed with empty visible paths.
- Focused post-fix runtime regression proving corrupt direct-delegate handoff logs fail the delegate result, name `messages.jsonl`, and mark parent coordination failed.
- Existing direct-delegate visible-output sync, parent-coordination error, and queue corrupt-handoff regressions remain green.
- Standard grouped validation before commit.

### FCA-20260526-169: Budget wrap-up awaiting input hides corrupt goal snapshot

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` defines `goal.json` as the current goal fact source, and requires `update_goal(status=complete)` / goal audit state to live in the current snapshot rather than only history.
- `spec/11-spec-audit-and-traceability.md` says `goal.json` and `artifacts/goal-history.jsonl` are target facts, while derived views such as session summaries cannot replace them.
- `internal/runtime/engine.go` `awaitingBudgetWrapUp` reloaded `goal.json` only to choose between two user-facing budget messages, and ignored non-missing load errors.
- A focused pre-fix regression created a stop-on-budget goal, recorded a valid budget wrap-up, then used a real `tool.after` hook to corrupt `goal.json` before `awaitingBudgetWrapUp` reloaded it. Before the fix, the engine moved to `awaiting_input` with a generic budget message and no error.

Impact:

A session could enter `goal_budget_limited` awaiting-input state immediately after `goal.json` became corrupt, hiding the fact that the authoritative goal snapshot was unreadable. That weakens recovery and can make operators trust a budget wrap-up state that is no longer backed by the current goal fact.

Minimal fix:

- Treat non-missing `goal.json` reload failures in `awaitingBudgetWrapUp` as runtime errors with `load goal.json for budget wrap-up` context.
- Preserve the existing successful message when `goal.json` loads and contains a budget-wrap-up record.
- Keep the existing budget turn-start history rollback behavior unchanged.

Validation:

- Focused pre-fix runtime regression proving corrupt `goal.json` after budget-wrap-up progress was hidden and the engine transitioned to `awaiting_input`.
- Focused post-fix runtime regression proving the same corruption returns a `goal.json` error and does not transition to budget awaiting input.
- Existing budget wrap-up success and goal-history rollback regressions remain green.
- Standard grouped validation before commit.

### FCA-20260526-170: Steer queues control records without valid session metadata

Severity: Medium

Evidence:

- `spec/00-product.md` and `spec/01-runtime-architecture.md` define `session.json`, `state.json`, messages, and events as the local session fact sources for both CLI and Web control paths.
- `spec/13-live-input-and-steering.md` requires external steer to enter the same `control/steer.jsonl` fact and then become replayable user messages, not to create orphaned control state.
- `internal/runtime/runner.go` `Steer` loaded only `state.json` before appending to `control/steer.jsonl` and emitting steer events; it loaded `session.json` afterward only for best-effort `session.md` refresh and ignored errors.
- A focused pre-fix regression corrupted `session.json` while leaving `state.json` running. Before the fix, `Steer` returned accepted/queued and wrote a pending steer request despite unreadable session metadata.

Impact:

A deleted or corrupt session metadata fact could still accumulate new steer control records and events as long as `state.json` said running. That splits live input away from a valid session identity/provider/workdir fact and makes recovery depend on stale partial state.

Minimal fix:

- Load `session.json` before accepting a steer request, so missing or corrupt metadata blocks the operation before any control record or event is written.
- Reuse the validated metadata for the post-queue session summary refresh instead of reloading and ignoring errors.
- Preserve existing running-state validation and event rollback behavior.

Validation:

- Focused pre-fix runtime regression proving corrupt `session.json` still accepted and queued steer.
- Focused post-fix runtime regression proving corrupt `session.json` returns an error and leaves steer/events empty.
- Existing successful steer, running-state guard, and event-append rollback regressions remain green.
- Standard grouped validation before commit.

### FCA-20260526-171: Steer acceptance continues without durable accepted events

Severity: Medium

Evidence:

- `spec/13-live-input-and-steering.md` requires each steer to produce `session.steer.requested`, `session.steer.queued`, and `session.steer.accepted` events, and says accepted steer must enter `messages.jsonl` as a normal user message.
- `spec/01-runtime-architecture.md` defines `events.jsonl` and `messages.jsonl` as session facts managed by `SessionStore`; live steer must not rely on shared memory only.
- `internal/runtime/engine.go` `drainSteer` appended the accepted user message to `messages.jsonl`, but emitted both `user.message` and `session.steer.accepted` through `e.emit`, whose append errors are intentionally ignored.
- A focused pre-fix runtime regression blocked `events.jsonl` before draining a pending steer. Before the fix, provider execution continued despite the accepted steer events being unwritable.

Impact:

The runtime could accept and act on a steer input without preserving the durable accepted-event evidence required for live-input recovery and audit. Operators could see a user message and provider continuation but lack the corresponding `session.steer.accepted` event fact.

Minimal fix:

- Use checked `appendEvent` calls for `user.message` and `session.steer.accepted` during steer acceptance.
- Stop before provider execution if accepted-event persistence fails.
- Preserve existing goal-history, corrupt-goal, interrupt cancellation, and concurrent steer-count behavior.

Validation:

- Focused pre-fix runtime regression proving provider execution continued after `events.jsonl` was blocked during steer acceptance.
- Focused post-fix runtime regression proving accepted-event append failure returns an `events.jsonl` error before provider execution.
- Existing steer goal-history failure, corrupt goal snapshot, interrupt acceptance, and concurrent steer-count regressions remain green.
- Standard grouped validation before commit.

### FCA-20260526-172: Background results continue without durable accepted events

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` defines `events.jsonl`, `messages.jsonl`, `control/background.jsonl`, and parent/queue coordination facts as local session facts.
- The background queue profile in `spec/09-phase-plan.md` requires real session / queue association and notification evidence rather than compatibility shells.
- `internal/runtime/engine.go` `drainBackground` appended accepted background results to `messages.jsonl` and marked background notifications accepted, but emitted the accepted `user.message` and `session.background.accepted` events through unchecked `e.emit`.
- A focused pre-fix runtime regression blocked `events.jsonl` before pending background results were accepted. Before the fix, provider execution continued despite the accepted background-result events being unwritable.

Impact:

The runtime could consume pending background results and continue parent execution without preserving durable event evidence that those results were accepted. This weakens recovery and Web timeline auditing for parent/queue handoff completion.

Minimal fix:

- Use checked `appendEvent` calls for background-results `user.message` and `session.background.accepted` events.
- Stop before provider execution if accepted-event persistence fails.
- Preserve existing successful background-results injection and accepted notification status behavior.

Validation:

- Focused pre-fix runtime regression proving provider execution continued after `events.jsonl` was blocked during background-results acceptance.
- Focused post-fix runtime regression proving accepted-event append failure returns an `events.jsonl` error before provider execution.
- Existing background-results injection and steer accepted-event regressions remain green.
- Standard grouped validation before commit.

### FCA-20260526-173: Provider stop failures ignore durable failed events

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` lists `session.failed` as a core session event and defines `events.jsonl` as a session fact managed by `SessionStore`.
- `spec/03-provider-contracts.md` maps provider `max_tokens`, `blocked`, and `error` stop reasons to resumable failure handling rather than normal completion.
- `internal/runtime/engine.go` saved `state.json` as failed for provider stop failures, then emitted the matching `session.failed` event through unchecked `e.emit`.
- A focused pre-fix runtime regression blocked `events.jsonl` before a `max_tokens` stop result. Before the fix, the engine returned a failed result without surfacing that the required failure event was not persisted.

Impact:

A provider stop failure could update `state.json` while losing the durable timeline fact that explains why the session became failed. That weakens recovery, Web timeline diagnosis, and provider-stop auditability for max-token, blocked, and provider-error stop outcomes.

Minimal fix:

- Use checked `appendEvent` for the provider stop-failure `session.failed` event.
- Include the provider stop failure reason in the returned append error context.
- Preserve the existing failed-state persistence and resumable failure semantics.

Validation:

- Focused pre-fix runtime regression proving blocked `events.jsonl` was ignored for provider stop failures.
- Focused post-fix runtime regression proving the same blocked event append returns an `events.jsonl` error with provider stop context.
- Existing provider stop failure and failed-event append regressions remain green.
- Standard grouped validation before commit.

### FCA-20260526-174: Session completion can lose durable completed events

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` lists `session.completed` as a core session event and defines `state.json` / `events.jsonl` as session facts managed by `SessionStore`.
- `internal/runtime/engine.go` `complete` saved `state.json` as `completed`, then emitted `session.completed` through unchecked `e.emit`.
- A focused pre-fix runtime regression blocked `events.jsonl` after the provider returned a `finish` tool call. Before the fix, `Engine.Run` returned `completed` even though the required completion event was unwritable.

Impact:

A session could become durably completed in `state.json` while the timeline lacked the `session.completed` event. That weakens Web-first completion auditing, recovery diagnostics, and queue/child traceability for sessions whose terminal state must be explainable from local file facts.

Minimal fix:

- Use checked `appendEvent` for the `session.completed` lifecycle event.
- Include `session.completed` in the returned append error context.
- Preserve existing successful finish behavior and parent queue reconciliation ordering.

Validation:

- Focused pre-fix runtime regression proving blocked `events.jsonl` was ignored during session completion.
- Focused post-fix runtime regression proving the same blocked event append returns an `events.jsonl` error with completion context.
- Existing finish and tool-hook completion regressions remain green.
- Standard grouped validation before commit.

### FCA-20260526-175: Awaiting-input transitions can lose durable events

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` lists `session.awaiting_input` as a core session event and defines `state.json` / `events.jsonl` as session facts managed by `SessionStore`.
- `spec/13-live-input-and-steering.md` treats `awaiting_input` as a resumable state for `continue`, so the transition must remain traceable from durable session facts.
- `internal/runtime/engine.go` `awaitingInput` saved `state.json` as `awaiting_input`, then emitted `session.awaiting_input` through unchecked `e.emit`.
- A focused pre-fix runtime regression blocked `events.jsonl` after the provider returned a run-mode done candidate. Before the fix, `Engine.Run` returned `awaiting_input` even though the required lifecycle event was unwritable.

Impact:

A run-mode session could become resumable in `state.json` while losing the timeline event that explains why the session stopped and can be continued. That weakens Web status diagnosis, operator recovery, and auditability for normal done-candidate stops.

Minimal fix:

- Use checked `appendEvent` for the normal `session.awaiting_input` lifecycle event.
- Include `session.awaiting_input` in the returned append error context.
- Preserve existing run-mode done-candidate semantics and linked queue reconciliation ordering.

Validation:

- Focused pre-fix runtime regression proving blocked `events.jsonl` was ignored during normal awaiting-input transition.
- Focused post-fix runtime regression proving the same blocked event append returns an `events.jsonl` error with awaiting-input context.
- Existing run-mode awaiting-input and loaded-skill regressions remain green.
- Standard grouped validation before commit.

### FCA-20260526-176: Special awaiting-input transitions can lose durable events

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` lists `session.awaiting_input` as a core session event and defines `state.json` / `events.jsonl` as session facts managed by `SessionStore`.
- `spec/12-task-system.md` and `spec/13-live-input-and-steering.md` treat budget wrap-up, Plan Mode approval, and Plan Mode cancellation as resumable operator states rather than terminal completion.
- `internal/runtime/engine.go` `awaitingBudgetWrapUp`, `awaitingPlanApproval`, and `awaitingPlanCancelled` saved `state.json` as `awaiting_input`, then emitted `session.awaiting_input` through unchecked `e.emit`.
- Focused pre-fix runtime regressions blocked `events.jsonl` before each special awaiting-input transition. Before the fix, all three helpers returned `awaiting_input` without surfacing that the required lifecycle event was unwritable.

Impact:

Budget-limited sessions and Plan Mode sessions could become resumable in `state.json` while losing the reasoned lifecycle event that explains why the operator must approve, continue, or inspect the budget boundary. This weakens Web status diagnosis, recovery prompts, and auditability for non-default awaiting-input reasons.

Minimal fix:

- Use checked `appendEvent` for `session.awaiting_input` in budget wrap-up, Plan Mode approval, and Plan Mode cancellation transitions.
- Include the specific awaiting-input reason in each returned append error context.
- Preserve existing state phases, user-facing final text, and linked queue reconciliation ordering.

Validation:

- Focused pre-fix runtime regressions proving blocked `events.jsonl` was ignored for budget wrap-up, Plan Mode approval, and Plan Mode cancellation awaiting-input transitions.
- Focused post-fix runtime regressions proving the same blocked event appends return `events.jsonl` errors with reason-specific context.
- Existing budget wrap-up and Plan Mode submit regressions remain green.
- Standard grouped validation before commit.

### FCA-20260526-177: Remaining engine lifecycle transitions can lose durable events

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` lists `session.failed` and `session.paused` as core session events and defines `state.json` / `events.jsonl` as session facts managed by `SessionStore`.
- `spec/01-runtime-architecture.md` also treats `exec` no-finish failure and `paused` as explicit session-state transitions, not diagnostic-only telemetry.
- `internal/runtime/engine.go` saved `state.json` as failed for autonomous `incomplete_no_finish`, then emitted `session.failed` through unchecked `e.emit`.
- `internal/runtime/engine.go` `pause` saved `state.json` as paused, then emitted `session.paused` through unchecked `e.emit`.
- Focused pre-fix runtime regressions blocked `events.jsonl` before each transition. Before the fix, both paths returned their new state without surfacing that the required lifecycle event was unwritable.

Impact:

An autonomous exec session could fail without the durable event explaining `incomplete_no_finish`, and an interrupted session could pause without the matching pause event. That weakens Web timelines, recovery prompts, and auditability for the two remaining engine-owned lifecycle state transitions.

Minimal fix:

- Use checked `appendEvent` for autonomous `incomplete_no_finish` `session.failed`.
- Use checked `appendEvent` for `session.paused`.
- Include the transition reason or event type in returned append error context.
- Preserve existing state transitions, pause behavior, and adjacent tool-result replay behavior.

Validation:

- Focused pre-fix runtime regressions proving blocked `events.jsonl` was ignored for autonomous no-finish failure and pause transitions.
- Focused post-fix runtime regressions proving both blocked event appends return `events.jsonl` errors with lifecycle context.
- Existing exec no-finish and interrupted-tool pause regressions remain green.
- Standard grouped validation before commit.

### FCA-20260526-178: Runner start can proceed without durable started event

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` lists `session.started` as a core session event and defines `events.jsonl` as a `SessionStore` fact.
- `spec/00-product.md` requires all key state changes to be written to disk before returning, and keeps session/state/messages/events as the local file facts for Web-first operation.
- `internal/runtime/runner.go` `runExisting` emitted `session.started` through unchecked `r.emit` immediately before calling `engine.Run`.
- A focused pre-fix runtime regression blocked `events.jsonl` after session creation but before `runExisting`. Before the fix, `Runner.Start` reached the provider and failed later on a different lifecycle append instead of reporting the missing `session.started` event.

Impact:

A session could enter provider execution without durable evidence that the run actually started. That weakens Web timeline reconstruction, recovery diagnostics, and the local file-fact contract for start/resume boundaries, especially when later lifecycle events are also affected by the same event-log write failure.

Minimal fix:

- Use checked `appendEvent` for `session.started` in `runExisting`.
- Return an error that names `session.started` when the start event cannot be appended.
- Stop before provider execution if the started event is not durable.
- Preserve existing provider setup, hook setup, steer watcher setup, and engine execution behavior when event persistence succeeds.

Validation:

- Focused pre-fix runtime regression proving blocked `events.jsonl` let execution reach the provider and failed later without `session.started` context.
- Focused post-fix runtime regression proving blocked `events.jsonl` returns a `session.started` append error before provider execution.
- Existing runtime/session and Web-first validation matrix before commit.

### FCA-20260526-179: Goal accounting can lose required runtime events

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` lists `goal.accounting.updated` and `goal.budget_limited` as session events, and defines `goal.json`, `artifacts/goal-history.jsonl`, and `events.jsonl` as local session facts.
- `spec/01-runtime-architecture.md` also requires `stop_on_budget=true` to write a durable budget wrap-up request before the limited wrap-up turn.
- `internal/runtime/goal.go` `updateGoalAccounting` called `UpdateGoalAccounting`, which mutates `goal.json` and appends goal history, then emitted `goal.accounting.updated`, `goal.budget_limited`, and `goal.budget_wrapup_required` through unchecked `e.emit`.
- A focused pre-fix runtime regression blocked `events.jsonl` after the provider returned usage. Before the fix, the engine ignored the missing `goal.accounting.updated` event and continued until a later lifecycle event append failed.

Impact:

Runtime accounting could advance Goal usage, budget status, and budget wrap-up request facts without preserving the corresponding durable session events. That weakens Web timelines, recovery diagnostics, and auditability for Goal budget decisions, especially because later event failures can mask the missing accounting event.

Minimal fix:

- Use checked `appendEvent` for `goal.accounting.updated`.
- Use checked `appendEvent` for `goal.budget_limited` and `goal.budget_wrapup_required` in the same accounting helper.
- Skip runtime accounting events when no current Goal was actually mutated, avoiding empty `goal.accounting.updated` events on sessions without `goal.json`.
- Preserve the existing Goal snapshot/history mutation semantics and stop before assistant persistence when required accounting events cannot be recorded.
- Preserve original runtime error context if `Engine.fail` also cannot append `session.failed`.

Validation:

- Focused pre-fix runtime regression proving blocked `events.jsonl` was ignored for `goal.accounting.updated` and execution failed later without accounting context.
- Focused post-fix runtime regression proving blocked `events.jsonl` returns a `goal.accounting.updated` append error before assistant persistence.
- Focused failure-path regression proving `Engine.fail` keeps original failure context when the fallback `session.failed` event append also fails.
- Adjacent budget wrap-up accounting and awaiting-input regressions remain green.
- Standard grouped validation before commit.

### FCA-20260527-180: Direct user-message appends can outlive missing `user.message` events

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` lists `user.message` in the core event model and defines `messages.jsonl` / `events.jsonl` as session facts.
- `spec/13-live-input-and-steering.md` requires accepted user input and harness reminders to become real user messages with event evidence, not just in-memory prompt view.
- `internal/runtime/runner.go` `appendUserMessage` appended direct Start / Continue / Plan Mode approval-revision user messages to `messages.jsonl`, then emitted the matching `user.message` event through unchecked `r.emit`.
- `internal/runtime/engine.go` `appendHarnessReminder` appended synthetic harness-reminder user messages to `messages.jsonl`, then emitted the matching `user.message` event through unchecked `e.emit`.
- Focused pre-fix regressions blocked `events.jsonl` after the message append point. Before the fix, both helpers returned success and left a durable message without the corresponding durable `user.message` event.

Impact:

Direct user input and pre-turn harness reminders could advance `messages.jsonl` while the event timeline stayed behind. On retry or recovery, operators could see replayable user messages without the event evidence Web timelines and diagnostics expect; failed Start / Continue attempts could also leave dangling direct user messages that the caller did not know were persisted.

Minimal fix:

- Add a narrow session-store rollback helper that removes only the just-appended tail message by message ID.
- Use checked `appendEvent` for `user.message` in `Runner.appendUserMessage`.
- Use checked `appendEvent` for harness-reminder `user.message` events in `Engine.appendHarnessReminder`.
- Roll back the just-appended message when the required event append fails, and report error context naming `user.message`.
- Keep steer/background control-drain acceptance sequencing out of this slice; those paths couple messages, control records, and accepted events and need a separate transaction design if audited further.

Validation:

- Focused pre-fix runtime regressions proving blocked `events.jsonl` was ignored for direct runner user messages and harness reminders.
- Focused post-fix runtime regressions proving blocked `events.jsonl` returns `user.message` context and leaves no dangling message.
- Focused session-store regression proving stale rollback IDs cannot remove a non-tail message.
- Standard grouped validation before commit.

### FCA-20260527-181: Checkpoint resume hints can lose injected-event evidence

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` lists `checkpoint.resume_hint.injected` in the core event model and defines `messages.jsonl` / `events.jsonl` as session facts.
- `spec/01-runtime-architecture.md` defines the long-run checkpoint writer as a recovery index, and `continue` can inject a harness resume note when that checkpoint exists.
- `internal/runtime/session_summary.go` `appendCheckpointResumeHint` appended a `meta.kind=longrun_checkpoint` harness reminder to `messages.jsonl`.
- `internal/runtime/runner.go` `Continue` then emitted `checkpoint.resume_hint.injected` through unchecked `r.emit`.
- A focused pre-fix runtime regression blocked `events.jsonl` after the checkpoint note append point. Before the fix, `Continue` ignored the missing checkpoint event and failed later on `session.started`, leaving the resume note in `messages.jsonl` without matching injected-event evidence.

Impact:

Recovery could add a provider-visible checkpoint resume note without preserving the event that explains why the note was injected. If the run then failed later, operators and Web timelines could see a harness reminder in replayable messages but miss the durable checkpoint-injection event needed to explain recovery context.

Minimal fix:

- Return the appended checkpoint resume-note message ID from `appendCheckpointResumeHint`.
- Use checked `appendEvent` for `checkpoint.resume_hint.injected` in `Runner.Continue`.
- Roll back the just-appended checkpoint note if the required event append fails, and report error context naming `checkpoint.resume_hint.injected`.
- Preserve existing drift-warning detection and checkpoint note content.

Validation:

- Focused pre-fix runtime regression proving blocked `events.jsonl` was ignored for checkpoint resume hint injection and the run failed later without checkpoint-event context.
- Focused post-fix runtime regression proving blocked `events.jsonl` returns `checkpoint.resume_hint.injected` context and leaves no dangling checkpoint resume note.
- Focused helper regression proving `appendCheckpointResumeHint` returns the message ID for the appended checkpoint note.
- Standard grouped validation before commit.

### FCA-20260527-182: Contract refresh can persist snapshots without required contract events

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` lists `contract.created` and `contract.updated` in the core event model and defines `contract.json`, `artifact-tracker.json`, `messages.jsonl`, and `events.jsonl` as session facts.
- `spec/13-live-input-and-steering.md` requires accepted steering that changes deliverables or completion conditions to sync `contract.created` / `contract.updated` and the artifact tracker, rather than relying only on the prompt view.
- `internal/runtime/contract.go` refreshed `contract.json`, `artifact-tracker.json`, and `artifacts/contract-history.jsonl` before emitting the matching contract event through a void callback backed by unchecked `r.emit` / `e.emit`.
- If `events.jsonl` was unavailable after the contract files were written, refresh returned success in the event path. On retry, `contractsEquivalent` could treat the already-written snapshot as current and skip the missing `contract.created` / `contract.updated` event permanently.
- The same write-before-error shape affected later refresh failures: without a rollback snapshot, a failed tracker/history/event step could leave contract state advanced beyond the observable event timeline.

Impact:

Start, Continue, or accepted steer could advance completion gates and required artifacts while the Web timeline and recovery event stream missed the core contract event explaining the change. Because the next refresh could see an equivalent contract, operators would not necessarily get a self-healing retry path for the missing event.

Minimal fix:

- Change contract-refresh event callbacks to return errors and use checked `appendEvent` for `contract.created` / `contract.updated` in Runner and Engine paths.
- Capture a contract refresh rollback snapshot before mutating `contract.json`, `artifact-tracker.json`, or contract history.
- Restore the prior contract snapshot, artifact tracker, and contract history if a required write or core contract event append fails.
- Keep `artifact.required` best-effort because it is a diagnostic/derived event and is not part of the core event catalog.
- Refresh contract state on `continue` even without a new user message, while keeping empty sessions without external user instructions as a no-op to avoid fabricating contracts.

Validation:

- Focused runtime regression proving blocked `events.jsonl` during `contract.created` returns an event-path error and removes the newly written contract/tracker/history snapshot.
- Focused runtime regression proving blocked `events.jsonl` during `contract.updated` restores the previous contract/tracker/history snapshot.
- Focused runtime regression proving empty sessions without external user instructions do not create a contract when refreshed.
- Standard grouped validation before commit.

### FCA-20260527-183: Deferred interrupt steer can lose its required event

Severity: Medium

Evidence:

- `spec/13-live-input-and-steering.md` says an unsafe interrupt steer fallback writes `session.steer.deferred`, and lists `session.steer.deferred` in the live-input event model.
- `internal/runtime/engine.go` `deferPendingInterrupts` consumed the in-memory interrupt flag, changed pending interrupt steer requests to `deferred`, and emitted `session.steer.deferred` through unchecked `e.emit`.
- Unlike the already-hardened accepted-steer path, this deferred path could still mutate `control/steer.jsonl` without preserving the durable event that explains why the interrupt was not accepted immediately.
- A focused pre-fix runtime regression blocked `events.jsonl` at `deferPendingInterrupts`. Before the fix, the helper returned nil and rewrote the pending interrupt steer as `deferred` without a matching durable event.

Impact:

An interrupt steer that could not be safely applied immediately could become deferred in the control queue while the operator timeline missed the required `session.steer.deferred` event. Recovery and Web views would see later acceptance or pending counts without the durable fact explaining the earlier fallback from interrupt to queue-first behavior.

Minimal fix:

- Use checked `appendEvent` for `session.steer.deferred`.
- Append the deferred event before mutating the request status in memory.
- If the event append fails, return the error and leave the durable steer request pending instead of writing a silent deferred state.
- Keep accepted-steer logic unchanged; that path was already hardened in FCA-20260527-169.

Validation:

- Focused runtime regression proving blocked `events.jsonl` returns an error and keeps the interrupt steer request pending.
- Adjacent interrupt-defer and accepted-steer regressions remain green.
- Standard grouped validation before commit.

### FCA-20260527-184: Interrupt steer watcher can signal cancellation without durable request event

Severity: Medium

Evidence:

- `spec/13-live-input-and-steering.md` lists `session.steer.interrupt_requested` as a live-input event and treats interrupt steer as best-effort preemption with durable event evidence.
- `internal/runtime/runner.go` `watchSteer` observed pending interrupt steer requests, marked their IDs as seen, signaled the in-memory interrupt cancel path, and emitted `session.steer.interrupt_requested` through unchecked `r.emit`.
- If `events.jsonl` was unavailable at that point, the runner could still cancel a provider/tool path while the required interrupt-request event was missing. The watcher also marked the request as seen, so the event would not be retried by that watcher instance.
- A focused pre-fix runner regression blocked `events.jsonl` before the watcher saw a pending interrupt request. Before the fix, the watcher still signaled the in-memory interrupt despite being unable to persist `session.steer.interrupt_requested`.

Impact:

An interrupt steer could affect live execution without the durable event that explains why a provider/tool turn was cancelled or deferred. That weakens recovery and Web timeline diagnosis for the exact moment an operator requested best-effort preemption.

Minimal fix:

- Use checked `appendEvent` for `session.steer.interrupt_requested` in the steer watcher.
- Persist the event before marking the request as seen or signaling the in-memory interrupt.
- If event persistence fails, leave the request un-seen in the watcher so a later poll can retry once `events.jsonl` is writable; the queued steer request itself remains durable for normal safe-boundary acceptance.

Validation:

- Focused runner regression proving blocked `events.jsonl` prevents the in-memory interrupt signal and retries after the event path is restored.
- Existing multiple-interrupt watcher regression remains green.
- Standard grouped validation before commit.

### FCA-20260527-185: Turns can proceed with unrecorded durable context snapshots

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` says prepare reads the durable context view from `todo.json`, `tasks/`, and `reports/spec.md` / `plan.md` / `progress.md` / `validation.md`, then writes `session.context.loaded` so recovery and live validation can see project-memory and task/todo counts.
- `spec/12-task-system.md` likewise describes `session.context.loaded` as durable evidence for current todo/task counts and project-memory present/missing state.
- `internal/runtime/engine.go` built the context-loaded payload and emitted `session.context.loaded` through unchecked `e.emit`.
- The same turn then used that durable context to construct the provider prompt and continue execution even if `events.jsonl` could not record the context snapshot.
- A focused pre-fix engine regression blocked `events.jsonl` before prepare. Before the fix, the engine still reached the provider with no durable `session.context.loaded` event for that turn.

Impact:

Recovery, Web timelines, and live validation could lack the event that explains which todo/task/project-memory facts were loaded into the provider turn. For long-running sessions, that weakens the ability to distinguish a model operating from current durable context from one operating after a failed context-evidence write.

Minimal fix:

- Use checked `appendEvent` for `session.context.loaded`.
- Fail the turn before provider prompt construction if the context-loaded event cannot be persisted.
- Preserve the existing event payload, including todo/task counts, project-memory present/missing paths, role hints, and Goal / Plan Mode context.

Validation:

- Focused runtime regression proving blocked `events.jsonl` returns `session.context.loaded` context and prevents provider execution.
- Existing durable context payload regression remains green.
- Existing provider failure / provider stop / incomplete-no-finish failed-event regressions remain targeted at their original `session.failed` append paths after `session.context.loaded` now fails earlier when blocked.
- Standard grouped validation completed in the update log.

### FCA-20260527-186: Failed live-input acceptance leaves duplicate replay messages

Severity: Medium

Evidence:

- `spec/13-live-input-and-steering.md` requires accepted steer input to become a real user message, but only when the acceptance facts are durably recorded; the same local-file-fact boundary applies to background results consumed by a parent session.
- `internal/runtime/engine.go` `drainSteer` appended the steer user message before writing the checked `user.message` and `session.steer.accepted` events.
- `internal/runtime/engine.go` `drainBackground` appended the background-results user message and marked notifications accepted before writing the checked `user.message` and `session.background.accepted` events.
- Focused regressions blocked `events.jsonl` before acceptance. The provider was correctly stopped, but the provider-visible steer/background user message remained in `messages.jsonl`; background notifications were also already accepted.

Impact:

A retry after the local event write was repaired could inject the same steer or background result again, or in the background case could lose the retry opportunity because the notification was already marked accepted. That makes `messages.jsonl`, control queues, and event facts disagree about whether live input actually became part of a successful provider turn.

Minimal fix:

- Roll back the just-appended steer user message if the matching `user.message` event cannot be persisted.
- For background results, persist the `user.message` event before marking notifications accepted.
- Roll back the just-appended background-results user message if that event cannot be persisted, leaving the notification pending for retry.
- Preserve the existing checked accepted-event behavior and successful steer/background injection paths.

Validation:

- Focused pre-fix regressions proving blocked `events.jsonl` left provider-visible steer/background messages after acceptance failed.
- Focused post-fix regressions proving those messages are rolled back and the durable steer/background control items remain retryable.
- Existing successful steer/background injection regressions remain green.
- Standard grouped validation before commit.

### FCA-20260527-187: Task-system tools can mutate durable state without required events

Severity: Medium

Evidence:

- `spec/12-task-system.md` defines `todo_write`, `task_create`, and `task_update` as durable task-system tools that write `todo.updated`, `task.created`, and `task.updated` events.
- `spec/01-runtime-architecture.md` lists those same events in the runtime event catalog and frames session files/events as the recovery and WebConsole fact source.
- `internal/tools/registry.go` wrote `todo.json` or `tasks/task_*.json`, then called `execCtx.Emit(...)` for the matching task-system event.
- `internal/runtime/engine.go` wired `ExecContext.Emit` to `e.emit`, and `e.emit` ignored `Store.AppendEvent` errors.
- Focused regressions showed a failed task-system event append could otherwise leave the tool result looking successful while the event timeline lacked the required mutation fact.

Impact:

Recovery, Web timelines, and context-loaded evidence could observe changed `todo.json` or task graph state without the event that explains when and why the model changed it. For long-running sessions, that makes task/todo state harder to audit and can make a retry path look like the task system advanced without a corresponding timeline fact.

Minimal fix:

- Add an error-returning `EmitRequired` callback to tool execution context and wire it to checked `appendEvent` in `Engine.Run`.
- Keep existing best-effort `Emit` available for non-contract telemetry.
- Route `todo.updated`, `task.created`, and `task.updated` through `EmitRequired`.
- If the required event cannot be written after a state mutation, restore the previous todo/task snapshot and return an error tool result instead of reporting a successful task-system update.

Validation:

- Focused registry regressions proving `todo_write` reports `todo.updated` failures and restores the previous todo snapshot, including semantic no-op writes.
- Focused registry regression proving `task_create` and `task_update` report required event failures and restore the previous task graph.
- Focused engine regression proving built-in `todo_write` receives the checked runtime event path and does not leave changed todo state when `events.jsonl` is blocked.
- Standard grouped validation before commit.

### FCA-20260527-188: Goal completion tool can lose the required completion event

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` lists `goal.completed` in the session event catalog, and `spec/11-spec-audit-and-traceability.md` requires `update_goal(status=complete)` evidence to be reflected in the durable goal completion audit.
- `internal/tools/registry.go` `defUpdateGoal` called `Store.CompleteGoal`, which wrote `goal.json` and appended `artifacts/goal-history.jsonl`, then emitted `goal.completed` through unchecked `ExecContext.Emit`.
- `internal/runtime/engine.go` wires `ExecContext.Emit` to best-effort `e.emit`, which ignores `events.jsonl` append failures.
- A focused registry regression blocked the checked event path; before the fix, `update_goal` returned a successful completion result while the event callback was never required and the goal remained complete.

Impact:

Recovery, Web timelines, and completion audits could observe a completed `goal.json` and goal history without the required session event that explains the model-driven completion transition. A retry after event storage repair would also see the goal already complete, so the missing timeline fact could not be naturally recovered by re-running the same model tool.

Minimal fix:

- Snapshot the current goal and goal history before model-driven `CompleteGoal`.
- Route model-tool `goal.completed` through the checked tool event callback.
- If the required event cannot be written, restore the previous goal snapshot and previous goal history and return an error tool result.
- Leave non-catalog `goal.progress.recorded` as best-effort telemetry until the spec promotes it to a required event.

Validation:

- Focused pre-fix registry regression proving `update_goal` succeeded and left the goal complete when required event persistence was unavailable.
- Focused post-fix registry regression proving blocked `goal.completed` returns an error result and restores the previous goal snapshot/history.
- Standard grouped validation before commit.

### FCA-20260527-189: Plan submission tool can lose the required submission event

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` lists `planmode.plan_submitted` in the session event catalog, and the Plan Mode spec requires `submit_plan` to durably pause execution at the approval gate.
- `internal/tools/registry.go` `defSubmitPlan` called `Store.SubmitPlanMode`, which writes `planmode.json`, `artifacts/planmode-plan.md`, and `artifacts/planmode-history.jsonl`, then emitted `planmode.plan_submitted` through unchecked `ExecContext.Emit`.
- `internal/runtime/engine.go` wires that unchecked callback to best-effort `e.emit`, which ignores event append failures.
- A focused registry regression blocked the checked event callback; before the fix, `submit_plan` returned success, advanced Plan Mode to `awaiting_approval`, and left the generated plan artifact without requiring the matching session event.

Impact:

Recovery and Web timelines could show Plan Mode waiting for approval without the session event that explains which tool submission created that gate. Because the store transition had already succeeded, a retry would see Plan Mode no longer in `planning` and could not naturally replay the missing `planmode.plan_submitted` event through the same model tool.

Minimal fix:

- Snapshot Plan Mode state, plan markdown artifact, and Plan Mode history before `SubmitPlanMode`.
- Route model-tool `planmode.plan_submitted` through the checked tool event callback.
- If the required event cannot be written, restore the previous Plan Mode snapshot and history and return an error tool result.

Validation:

- Focused pre-fix registry regression proving `submit_plan` succeeded and left Plan Mode awaiting approval when required event persistence was unavailable.
- Focused post-fix registry regression proving blocked `planmode.plan_submitted` returns an error result and restores previous Plan Mode state, history, and generated plan artifact.
- Standard grouped validation before commit.

### FCA-20260527-190: Plan input request tool can continue without the required request event

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` lists `planmode.input_requested` in the session event catalog, and the Plan Mode spec requires pending `request_user_input` facts to support active Web runners and recovery.
- Earlier FCA-20260526-073 moved `planmode.input_requested` emission and responder invocation after both the pending request and awaiting-input `state.json` transition are durable.
- `internal/tools/registry.go` `defRequestUserInput` still emitted `planmode.input_requested` through unchecked `ExecContext.Emit`, then called the interactive responder.
- A focused registry regression blocked the required event callback; before the fix, the responder was still called and the tool returned answered input even though the session event was unavailable.

Impact:

A live Plan Mode runner could consume user input without the event timeline fact that says an input request was actually surfaced. Web/recovery would still see durable pending request and awaiting-input state, but the active model turn could move past that decision while observability and replay facts were incomplete.

Minimal fix:

- Route model-tool `planmode.input_requested` through the checked tool event callback.
- If the required event cannot be written, return an error tool result before calling the interactive responder.
- Preserve the already-durable pending request and awaiting-input state as recovery facts, matching the earlier FCA-20260526-073 decision.

Validation:

- Focused pre-fix registry regression proving `request_user_input` called the responder and returned answers when required event persistence was unavailable.
- Focused post-fix registry regression proving blocked `planmode.input_requested` returns an error result before the responder is called while preserving durable pending request/state.
- Standard grouped validation before commit.

### FCA-20260527-191: Goal creation tool can lose the required creation event

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` lists `goal.created` in the session event catalog.
- `internal/tools/registry.go` `defCreateGoal` called `Store.CreateGoal`, which writes `goal.json` and `artifacts/goal-history.jsonl`, then emitted `goal.created` through unchecked `ExecContext.Emit`.
- `internal/runtime/engine.go` wires unchecked `ExecContext.Emit` to best-effort `e.emit`, which ignores `events.jsonl` append failures.
- A focused plain-goal registry regression blocked the required event callback; before the fix, `create_goal` returned success and left a current goal despite the missing session event.

Impact:

A model-created goal could become the durable current goal without the event timeline fact that explains when the model introduced it. Retrying after event storage repair would fail as a duplicate current goal, so the missing `goal.created` event could not be repaired through the same tool call.

Minimal fix:

- Snapshot Goal history and task graph before model-driven `CreateGoal`.
- Route model-tool `goal.created` through the checked tool event callback.
- If the required event cannot be written, clear the just-created goal, restore previous Goal history and previous tasks, and return an error tool result.
- Keep approval-gated linked Plan Mode creation as a separate atomicity concern rather than mixing it into the plain-goal event slice.

Validation:

- Focused pre-fix registry regression proving plain `create_goal` succeeded and left `goal.json` when required event persistence was unavailable.
- Focused post-fix registry regression proving blocked `goal.created` returns an error result and restores prior Goal/task facts.
- Standard grouped validation before commit.

### FCA-20260526-166: Web session routes report corrupt metadata without the source fact name

Severity: Low

Evidence:

- `spec/01-runtime-architecture.md` defines `session.json` as a core `SessionStore` fact and requires WebConsole to reuse local file facts rather than maintaining a second authority.
- `spec/00-product.md` requires session, state, messages, and events to be file facts, with WebConsole serving as the default local operator surface.
- `internal/webconsole/service.go` `requireSession` blocks session mutation routes by calling `Store.LoadMetadata`, but corrupt metadata errors came back as raw JSON parse text such as `unexpected end of JSON input`.
- `internal/session/store.go` `LoadMetadata` returned `readJSONFile` errors directly, unlike nearby recovery hardening that names corrupt durable facts.
- A focused pre-fix WebConsole regression corrupted `session.json`, then patched `/api/sessions/<id>/mission/plan`. Before the fix, the request failed before mutation, but the error body did not mention `session.json`.

Impact:

Operators diagnosing a corrupt session from Web routes could see only a generic JSON parse error without the file fact that needs repair or inspection. The route was already safely blocked, but the recovery signal was weaker than other corrupt durable-fact diagnostics in the repo.

Minimal fix:

- Wrap `LoadMetadata` read failures as `load session.json: ...`.
- Preserve invalid session-id and missing-file classification by wrapping only the `readJSONFile` error, so `errors.Is(err, fs.ErrNotExist)` still works for Web 404 mapping.
- Add focused WebConsole coverage proving a corrupt `session.json` route failure names the source file and does not mutate the Goal mission role plan.

Validation:

- Focused pre-fix WebConsole regression proving corrupt session metadata returned only a raw JSON parse error.
- Focused post-fix WebConsole regression proving the same route now reports `session.json` and leaves the Goal mission role plan unchanged.
- Standard grouped validation before commit.

### FCA-20260526-165: Goal and Plan Mode history appends hide corrupt current snapshots

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` defines `goal.json` / `artifacts/goal-history.jsonl` and `planmode.json` / `artifacts/planmode-history.jsonl` as paired durable session facts for Goal and Plan Mode state transitions.
- `spec/11-spec-audit-and-traceability.md` says `goal.json` and `artifacts/goal-history.jsonl` are Goal facts, and `planmode.json` plus `artifacts/planmode-history.jsonl` are the Plan Mode fact source and transition log.
- `internal/session/goal.go` `AppendGoalHistory` inferred `entry.GoalID` from `goal.json` only when `loadGoalNoLock` returned nil, but discarded every load error before appending history.
- `internal/session/planmode.go` `AppendPlanModeHistory` inferred `entry.PlanModeID` from `planmode.json` only when `loadPlanModeNoLock` returned nil, but discarded every load error before appending history.
- Focused pre-fix regressions corrupted `goal.json` and `planmode.json`, then appended history entries without explicit IDs. Before the fix, both helpers returned nil and wrote unlinked history rows while the current snapshot files were malformed.

Impact:

Store-level callers that rely on history append helpers to infer the current Goal or Plan Mode ID could create audit rows disconnected from the current snapshot while a present source file was corrupt. That weakens recovery and traceability because the transition log no longer proves which durable Goal or Plan Mode state the entry belonged to, and the corruption is hidden behind a successful append.

Minimal fix:

- Return `load goal.json for goal history` when `AppendGoalHistory` needs the current Goal ID and a present `goal.json` is unreadable or malformed.
- Return `load planmode.json for plan mode history` when `AppendPlanModeHistory` needs the current Plan Mode ID and a present `planmode.json` is unreadable or malformed.
- Preserve valid no-current-snapshot compatibility by continuing to allow `fs.ErrNotExist` when the entry does not explicitly require an ID.
- Add focused store coverage proving corrupt snapshots stop unlinked Goal and Plan Mode history appends.

Validation:

- Focused pre-fix store regressions proving corrupt current snapshots were hidden and unlinked history was appended.
- Focused post-fix store regressions proving corrupt current snapshots return actionable errors and do not append new history rows.
- Existing history append rollback regressions for Goal and Plan Mode remain green.
- Standard grouped validation before commit.

### FCA-20260526-164: Compaction hides corrupt Goal snapshot state

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` defines compaction as a context-view mechanism that must not replace or corrupt original session logs, and `SessionGoalManager` says Goal snapshots are injected into compaction summaries and long-run recovery facts.
- `spec/18-durable-contract-and-completion.md` defines `goal.json` as the current Goal snapshot used by `session.md`, checkpoints, Mission Control, and recovery prompts.
- `internal/runtime/compaction.go` called `loadGoalOptional` while building fresh compaction summaries, fallback reuse summaries, and reuse event metadata, but discarded its error.
- A focused pre-fix regression corrupted `goal.json` before compaction. Before the fix, compaction succeeded, wrote a summary with no `goal_snapshot`, and emitted `goal_present=false`.
- A second focused pre-fix regression used the hysteresis reuse path with corrupt `goal.json`. Before the fix, reuse also succeeded and reported no Goal.

Impact:

Compaction could turn a corrupt Goal snapshot into a provider-visible context summary that says no Goal exists. That weakens recovery and long-running goal traceability because future turns may rely on the compacted view and miss the fact that the durable Goal source is unreadable.

Minimal fix:

- Propagate `loadGoalOptional` errors from fresh compaction as `load goal.json for compaction`.
- Propagate corrupt Goal errors from fallback reuse summary construction as `load goal.json for compaction reuse`.
- Propagate corrupt Goal errors from reuse event metadata as `load goal.json for compaction reuse event`.
- Preserve no-Goal compatibility by continuing to treat `fs.ErrNotExist` as nil through `loadGoalOptional`.
- Add focused runtime coverage for fresh compaction and hysteresis reuse with corrupt `goal.json`.

Validation:

- Focused pre-fix runtime regressions proving corrupt `goal.json` was hidden by fresh compaction and reuse.
- Focused post-fix runtime regressions proving corrupt Goal state stops compaction/reuse with an actionable `goal.json` error.
- Existing corrupt feature-list, durable summary, hysteresis reuse, and reference-prefix compaction regressions remain green.
- Standard grouped validation before commit.

### FCA-20260526-163: Long-run checkpoint hides corrupt optional recovery snapshots

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` defines `contract.json`, `goal.json`, `planmode.json`, `parent-coordination.json`, and long-run checkpoints as session recovery facts managed by the session store.
- `spec/18-durable-contract-and-completion.md` says checkpoints record contract, Goal, Plan Mode, and parent child/queue wait-state recovery facts, while remaining resume indexes rather than replacing those source files.
- Prior Web, gate, and `session.md` fixes already distinguish missing optional snapshots from corrupt present snapshots for these same files.
- `internal/runtime/session_summary.go` `writeLongRunCheckpoint` still called `LoadContract`, `LoadGoal`, `LoadPlanMode`, and `LoadParentCoordination` without returning non-missing load errors before writing `checkpoints/longrun-latest.json`.
- A focused pre-fix regression corrupted `contract.json`, `goal.json`, `planmode.json`, and `parent-coordination.json` in checkpoint-worthy sessions. Before the fix, `writeLongRunCheckpoint` returned nil and could write a checkpoint without those recovery snapshots or with an inaccurate parent wait state.

Impact:

Long-running recovery could create or overwrite a checkpoint that silently drops corrupt contract, Goal, Plan Mode, or parent coordination facts. That can make future continuation guidance treat unreadable authority/recovery snapshots as absent state and weakens the same diagnostics already surfaced by Web detail, gates, and `session.md`.

Minimal fix:

- Return `load contract.json for long-run checkpoint` for corrupt or unreadable contract snapshots while preserving missing-contract compatibility.
- Return `load goal.json for long-run checkpoint` for corrupt or unreadable Goal snapshots while preserving no-Goal compatibility.
- Return `load planmode.json for long-run checkpoint` for corrupt or unreadable Plan Mode snapshots while preserving no-Plan-Mode compatibility.
- Return `load parent-coordination.json for long-run checkpoint` for corrupt or unreadable parent coordination snapshots while preserving no-parent-coordination compatibility.
- Add focused runtime coverage proving corrupt optional recovery snapshots stop checkpoint writing and leave no checkpoint artifact.

Validation:

- Focused pre-fix runtime regression proving corrupt optional recovery snapshots were hidden by checkpoint writing.
- Focused post-fix runtime regression proving corrupt optional snapshot state is reported and no checkpoint is written.
- Existing corrupt log, child/queue/background, artifact tracker, todo, task graph, optional summary, parent coordination gate, checkpoint drift, and provider-attempt checkpoint regressions remain green.
- Standard grouped validation before commit.

### FCA-20260526-162: Long-run checkpoint hides corrupt message and event logs

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` defines `messages.jsonl` and `events.jsonl` as session fact sources, and `LongRunCheckpointWriter` records source-message/source-event counts plus recent owner clues for recovery.
- `spec/18-durable-contract-and-completion.md` says long-run checkpoints are resume indexes, not replacements for messages/events/state.
- `FCA-20260526-158` hardened `session.md` to display corrupt message and event logs instead of rendering ordinary absence.
- `internal/runtime/session_summary.go` `writeLongRunCheckpoint` still called `store.LoadMessages` and `store.LoadEvents` with discarded errors before deriving `source_message_count`, `source_event_count`, and `recent_owner`.
- A focused pre-fix regression corrupted `messages.jsonl` and `events.jsonl` in checkpoint-worthy sessions. Before the fix, `writeLongRunCheckpoint` returned nil and could write a checkpoint with zero source counts or missing owner clues.

Impact:

Long-running recovery could create or overwrite a checkpoint that says the source transcript or event stream has zero entries while the real log file is corrupt. That weakens resume diagnostics, hides recent WebConsole/runner owner clues, and can mislead operators or future continuations into treating unreadable session history as empty history.

Minimal fix:

- Propagate `LoadMessages` errors from `writeLongRunCheckpoint` with `load messages.jsonl for long-run checkpoint` context.
- Propagate `LoadEvents` errors with `load events.jsonl for long-run checkpoint` context.
- Preserve valid empty log behavior.
- Do not write a misleading long-run checkpoint when these required session log facts are corrupt.
- Add focused runtime coverage proving corrupt message and event logs stop checkpoint writing and leave no checkpoint artifact.

Validation:

- Focused pre-fix runtime regression proving corrupt message/event logs were hidden by checkpoint writing.
- Focused post-fix runtime regression proving corrupt message/event log state is reported and no checkpoint is written.
- Existing corrupt child/queue/background, corrupt artifact tracker, corrupt todo, corrupt task graph, session-summary log, owner-clue checkpoint, and provider-attempt checkpoint regressions remain green.
- Standard grouped validation before commit.

### FCA-20260526-161: Long-run checkpoint hides corrupt child and queue facts

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` defines `LongRunCheckpointWriter` as recording parent wait state, resume hints, child/queue state, and background notification counts for long-running recovery.
- `spec/18-durable-contract-and-completion.md` says long-run checkpoints are resume indexes, not replacements for source facts, and that checkpoints record unresolved child/queue state.
- Prior store and gate fixes already make corrupt child `state.json`, queue job JSON, and `control/background.jsonl` reportable, and `FCA-20260526-157` hardened `session.md` to display those corrupt child/queue/background facts.
- `internal/runtime/session_summary.go` `writeLongRunCheckpoint` still called `store.ListChildren`, `store.ListJobsByParent`, and `store.LoadBackgroundNotifications` with discarded errors before writing `checkpoints/longrun-latest.json`.
- A focused pre-fix regression corrupted child `state.json`, a parent queue job JSON file, and `control/background.jsonl` in checkpoint-worthy sessions. Before the fix, `writeLongRunCheckpoint` returned nil and could write a checkpoint with empty unresolved child/queue state or `background_notifications=0`.

Impact:

Long-running parent recovery could create or overwrite a checkpoint that omits unresolved child sessions, unresolved queue jobs, or background notifications while the source child/queue facts are corrupt. That weakens resume guidance and can make recovery prompts treat child/queue work as absent when the real problem is unreadable durable state.

Minimal fix:

- Propagate `ListChildren` errors from `writeLongRunCheckpoint` with `load child sessions for long-run checkpoint` context.
- Propagate `ListJobsByParent` errors with `load queue jobs for long-run checkpoint` context.
- Propagate `LoadBackgroundNotifications` errors with `load control/background.jsonl for long-run checkpoint` context.
- Preserve valid empty child/queue/background behavior.
- Do not write a misleading long-run checkpoint when these child/queue/background facts are corrupt.
- Add focused runtime coverage proving corrupt child sessions, queue jobs, and background notifications stop checkpoint writing and leave no checkpoint artifact.

Validation:

- Focused pre-fix runtime regression proving corrupt child/queue/background facts were hidden by checkpoint writing.
- Focused post-fix runtime regression proving corrupt child/queue/background state is reported and no checkpoint is written.
- Existing corrupt artifact tracker, corrupt todo, corrupt task graph, child/queue summary, parent-background gate, and owner-clue checkpoint regressions remain green.
- Standard grouped validation before commit.

### FCA-20260526-160: Long-run checkpoint hides corrupt artifact tracker

Severity: Medium

Evidence:

- `spec/18-durable-contract-and-completion.md` defines `artifact-tracker.json` as the required-artifact status source and says long-run checkpoints record artifact status as a resume index, not a replacement for source facts.
- Prior required-artifact gate fixes already make corrupt `artifact-tracker.json` block finish/state tracking, and `FCA-20260526-156` hardened `session.md` to display corrupt artifact tracker state.
- `internal/runtime/session_summary.go` `writeLongRunCheckpoint` still called `artifacts, _ := store.LoadArtifactTracker(sessionID)`, discarding corrupt tracker load errors before writing `checkpoints/longrun-latest.json`.
- A focused pre-fix regression corrupted `artifact-tracker.json` in a parent-linked session. Before the fix, `writeLongRunCheckpoint` returned nil and could write a checkpoint with an empty `required_artifact_status`.

Impact:

Long-running recovery could create or overwrite a checkpoint that omits required-artifact status while the real `artifact-tracker.json` snapshot is corrupt. That weakens resume guidance around required artifact completion and can make recovery artifacts diverge from the same durable tracker that completion gates rely on.

Minimal fix:

- Propagate `LoadArtifactTracker` errors from `writeLongRunCheckpoint` with `load artifact-tracker.json for long-run checkpoint` context.
- Preserve missing or empty tracker compatibility because `LoadArtifactTracker` already returns an empty list for the optional missing-file case.
- Do not write a misleading long-run checkpoint when the artifact tracker is corrupt.
- Add focused runtime coverage proving corrupt `artifact-tracker.json` stops checkpoint writing and no checkpoint artifact is left behind.

Validation:

- Focused pre-fix runtime regression proving corrupt `artifact-tracker.json` was hidden by checkpoint writing.
- Focused post-fix runtime regression proving corrupt artifact tracker state is reported with `artifact-tracker.json` and no checkpoint is written.
- Existing corrupt todo, corrupt task graph, cancelled-task checkpoint, and provider-attempt checkpoint regressions remain green.
- Standard grouped validation before commit.

### FCA-20260526-159: Long-run checkpoint hides corrupt todo state

Severity: Medium

Evidence:

- `spec/12-task-system.md` defines `todo.json` as the session todo snapshot and says long-task checkpoints read todo/task derived state while remaining resume indexes, not source facts.
- `spec/18-durable-contract-and-completion.md` says long-run checkpoints record todo/task summary and resume hints, but the checkpoint is not a replacement for messages/events/state or source task files.
- `FCA-20260526-099` hardened `todo_write` to report unreadable `todo.json`, and `FCA-20260526-155` hardened `session.md` to display corrupt todo state.
- `internal/runtime/session_summary.go` `writeLongRunCheckpoint` still called `todo, _ := store.LoadTodo(sessionID)`, discarding corrupt todo load errors before writing `checkpoints/longrun-latest.json`.
- A focused pre-fix regression corrupted `todo.json` in a parent-linked session. Before the fix, `writeLongRunCheckpoint` returned nil and could write a checkpoint with an empty todo summary.

Impact:

Long-running recovery could create or overwrite a checkpoint that omits todo state while the real `todo.json` snapshot is corrupt. That misleads resume/handoff context and weakens the same durable task-state recovery guarantees already applied to task graph files and `session.md`.

Minimal fix:

- Propagate `LoadTodo` errors from `writeLongRunCheckpoint` with `load todo.json for long-run checkpoint` context.
- Preserve missing or empty todo compatibility because `LoadTodo` already returns an empty list for the optional missing-file case.
- Do not write a misleading long-run checkpoint when the todo snapshot is corrupt.
- Add focused runtime coverage proving corrupt `todo.json` stops checkpoint writing and no checkpoint artifact is left behind.

Validation:

- Focused pre-fix runtime regression proving corrupt `todo.json` was hidden by checkpoint writing.
- Focused post-fix runtime regression proving corrupt todo state is reported with `todo.json` and no checkpoint is written.
- Existing corrupt task graph, cancelled-task checkpoint, and provider-attempt checkpoint regressions remain green.
- Standard grouped validation before commit.

### FCA-20260526-158: Session summary hides corrupt message and event logs

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` defines `messages.jsonl`, `events.jsonl`, and `session.md` as session store facts / derived artifacts, and says `SessionSummaryWriter` writes an operator-readable `session.md`.
- `spec/18-durable-contract-and-completion.md` says `session.md` summarizes canonical fact-file locations and is never a fact source, while messages/events/state remain authoritative logs.
- `internal/runtime/session_summary.go` derives Tool Repetition from `store.LoadMessages(sessionID)` and recent owner clues from `store.LoadEvents(sessionID)`, but still called both loaders with discarded errors.
- `internal/session/store.go` already returns real errors for corrupt or unreadable `messages.jsonl` and `events.jsonl`.
- A focused pre-fix regression corrupted `messages.jsonl` and `events.jsonl`. Before the fix, `session.md` rendered Tool Repetition as `not observed` for corrupt messages and silently omitted recent owner context for corrupt events.

Impact:

Operators and recovery prompts could read `session.md` and conclude there was no tool repetition or recent owner clue while the real issue was unreadable durable logs. That weakens handoff, Web active-handle diagnostics, overread/no-op detection, and recovery confidence for the core message/event fact files.

Minimal fix:

- Preserve `not observed` for genuinely empty valid message logs.
- Render non-missing `messages.jsonl` load failures in the Tool Repetition section.
- Render non-missing `events.jsonl` load failures in the recent-owner header slot when no owner clue can be derived.
- Keep `session.md` derived-only: summary write failures still do not become runtime authority.
- Add focused runtime coverage for corrupt message/event log summary rendering.

Validation:

- Focused pre-fix runtime regression proving corrupt messages/events were hidden as normal empty observations.
- Focused post-fix runtime regression proving `session.md` names `messages.jsonl` and `events.jsonl` load errors.
- Existing optional-fact, task-state, artifact/provider, child/queue, and owner-clue summary/checkpoint regressions remain green.
- Standard grouped validation before commit.

### FCA-20260526-157: Session summary hides corrupt child and queue facts

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` says `SessionSummaryWriter` aggregates children, queue, background notifications, and parent coordination facts into `session.md`.
- `spec/18-durable-contract-and-completion.md` says `session.md` summarizes child sessions, queue jobs, and background notifications as an operator-readable derived view, while it is never a fact source.
- `FCA-20260526-146` hardened child session listing to report corrupt child `state.json`, `FCA-20260526-148` hardened queue list readers to report corrupt job files, and `FCA-20260526-139` hardened parent-background gates to report corrupt `control/background.jsonl`.
- `internal/runtime/session_summary.go` still loaded child, queue, and notification facts with `children, _ := store.ListChildren(sessionID, 100)`, `jobs, _ := store.ListJobsByParent(sessionID, 100)`, and `notifications, _ := store.LoadBackgroundNotifications(sessionID)`, discarding meaningful errors before rendering the Children And Queue section.
- A focused pre-fix regression corrupted a child session `state.json`, a parent queue job JSON file, and `control/background.jsonl`. Before the fix, `session.md` rendered Children And Queue as `not recorded` for all three cases.

Impact:

Operators and recovery prompts could read `session.md` and conclude there was no child, queue, or background-notification state while the real issue was unreadable durable child/queue state. That weakens parent/child recovery, background-result acceptance, queue diagnostics, and long-running handoff, especially after store and gate paths already distinguish absent optional state from corrupt present files.

Minimal fix:

- Preserve `not recorded` for genuinely absent child/queue/notification state.
- Render non-missing child list, parent queue job list, and background-notification load failures in the Children And Queue section.
- Include the known `control/background.jsonl` fact filename when the background notification loader returns a raw JSONL parse error.
- Keep `session.md` derived-only: summary write failures still do not become runtime authority.
- Add focused runtime coverage for corrupt child-session, queue-job, and background-notification summary rendering.

Validation:

- Focused pre-fix runtime regression proving corrupt child/queue/background facts were rendered as `not recorded`.
- Focused post-fix runtime regression proving `session.md` names child-session, queue-job, and `control/background.jsonl` load errors.
- Existing optional-fact, task-state, artifact/provider, parent-background gate, and owner-clue summary/checkpoint regressions remain green.
- Standard grouped validation before commit.

### FCA-20260526-156: Session summary hides corrupt artifact and provider-attempt facts

Severity: Medium

Evidence:

- `spec/01-runtime-architecture.md` says `SessionSummaryWriter` aggregates artifact tracker and provider attempt facts into `session.md`.
- `spec/18-durable-contract-and-completion.md` says `session.md` summarizes required artifacts and recent provider attempts as an operator-readable derived view, while it is never a fact source.
- `spec/03-provider-contracts.md` defines `provider-attempts.jsonl` as the durable retry / auto-resume / failure / success ledger for diagnostics and recovery.
- `internal/session/store.go` already distinguishes missing optional `artifact-tracker.json` / `provider-attempts.jsonl` from corrupt or unreadable present files.
- `internal/runtime/session_summary.go` still loaded those facts with `artifacts, _ := store.LoadArtifactTracker(sessionID)` and `attempts, _ := store.LoadProviderAttempts(sessionID)`, discarding meaningful load errors before rendering the Required Artifacts and Provider Attempts sections.
- A focused pre-fix regression corrupted `artifact-tracker.json` and `provider-attempts.jsonl`. Before the fix, `session.md` rendered both sections as `not recorded`.

Impact:

Operators and recovery prompts could read `session.md` and conclude there were no required-artifact facts or provider-attempt facts while the real issue was unreadable durable state. That weakens recovery diagnostics for required-artifact completion, provider retry/timeout analysis, cache telemetry, and broad audit evidence, especially after Web detail and completion gates already surface these corrupt files.

Minimal fix:

- Preserve `not recorded` for genuinely missing or empty artifact tracker and provider-attempt facts.
- Render non-missing `artifact-tracker.json` and `provider-attempts.jsonl` load failures in their existing `session.md` sections.
- Keep `session.md` derived-only: summary write failures still do not become runtime authority.
- Add focused runtime coverage for corrupt artifact tracker and provider-attempt ledger summary rendering.

Validation:

- Focused pre-fix runtime regression proving corrupt artifact/provider-attempt facts were rendered as `not recorded`.
- Focused post-fix runtime regression proving `session.md` names `artifact-tracker.json` and `provider-attempts.jsonl` load errors.
- Existing optional-fact, task-state, and provider-attempt summary/checkpoint regressions remain green.
- Standard grouped validation before commit.

### FCA-20260526-155: Session summary hides corrupt task-state facts

Severity: Medium

Evidence:

- `spec/12-task-system.md` defines `todo.json` as the session todo snapshot and `tasks/task_*.json` as the durable persistent task graph.
- `spec/18-durable-contract-and-completion.md` says `session.md` summarizes todo and task state as an operator-readable derived view, while the task files remain the source facts.
- `FCA-20260526-099` hardened `todo_write` to report unreadable `todo.json`, and `FCA-20260526-135` hardened `ListTasks` to report corrupt task files.
- `internal/runtime/session_summary.go` still loaded task state with `todo, _ := store.LoadTodo(sessionID)` and `tasks, _ := store.ListTasks(sessionID)`, discarding those meaningful errors before rendering the Task State section.
- A focused pre-fix regression corrupted `todo.json` and `tasks/task_0001.json`. Before the fix, `session.md` rendered `## Task State` as `not recorded` in both cases.

Impact:

Operators and recovery prompts could read `session.md` and conclude there was no todo or durable task graph, while the real issue was corrupt task-state files. This weakens long-running handoff and recovery diagnostics, especially after the store/tool paths already distinguish missing optional task state from corrupt present task facts.

Minimal fix:

- Preserve `not recorded` for genuinely empty todo/task state.
- Render non-missing `todo.json` and task graph load failures in the Task State section.
- Keep `session.md` derived-only: summary write failures still do not become runtime authority.
- Add focused summary coverage for corrupt `todo.json` and corrupt `tasks/task_*.json`.

Validation:

- Focused pre-fix runtime regression proving corrupt todo/task facts were rendered as `Task State: not recorded`.
- Focused post-fix runtime regression proving `session.md` names `todo.json` and `tasks/task_0001.json` load errors.
- Existing optional-fact and cancelled-task summary regressions remain green.
- Standard grouped validation before commit.

### FCA-20260526-154: Long-run checkpoint hides corrupt task graph facts

Severity: Medium

Evidence:

- `spec/12-task-system.md` defines `tasks/task_*.json` as the durable persistent task graph for long-running work, recovery, and ready/blocked/done calculation.
- `spec/18-durable-contract-and-completion.md` says long-run checkpoints record todo/task summary as a resume index, not a replacement for the source task files.
- `FCA-20260526-135` fixed `ListTasks` so corrupt `tasks/task_*.json` files are reported rather than silently dropped.
- `internal/runtime/session_summary.go` `writeLongRunCheckpoint` still called `tasks, _ := store.ListTasks(sessionID)`, discarding the now-meaningful corrupt-task error before writing `checkpoints/longrun-latest.json`.
- A focused pre-fix regression corrupted `tasks/task_0001.json` in a parent-linked session. Before the fix, `writeLongRunCheckpoint` returned nil and wrote a checkpoint with an empty task summary.

Impact:

Long-running recovery could overwrite or create a checkpoint that says the durable task graph is empty while a present task file is corrupt. That misleads resume/handoff context and weakens the task graph recovery guarantees established by the store-level corrupt-task fix.

Minimal fix:

- Propagate `ListTasks` errors from `writeLongRunCheckpoint` with `load tasks for long-run checkpoint` context.
- Preserve missing `tasks/` directory / no-task behavior because `ListTasks` already returns an empty task graph for that optional case.
- Do not write a misleading long-run checkpoint when the task graph is corrupt.
- Add focused runtime coverage proving corrupt task files stop checkpoint writing and no checkpoint artifact is left behind.

Validation:

- Focused pre-fix runtime regression proving corrupt `tasks/task_0001.json` was hidden and a misleading checkpoint was written.
- Focused post-fix runtime regression proving corrupt task graph state is reported with the task filename and no checkpoint is written.
- Existing checkpoint task-summary and provider-attempt checkpoint regressions remain green.
- Standard grouped validation before commit.

### FCA-20260526-153: Checkpoint resume hints hide corrupt contract drift state

Severity: Medium

Evidence:

- `spec/18-durable-contract-and-completion.md` defines `contract.json` as the durable session contract snapshot and says long-run checkpoint resume notes are visible continuation hints, not a replacement for source facts.
- `spec/01-runtime-architecture.md` requires session/contract/checkpoint facts to stay file-backed and recoverable through the runtime/session store boundary.
- `internal/runtime/session_summary.go` `appendCheckpointResumeHint` loaded `checkpoints/longrun-latest.json`, then called `checkpointDriftWarnings` before appending a harness resume note.
- `checkpointDriftWarnings` called `LoadContract` for the current `contract.json` but collapsed every non-success result into a `"missing current contract"` warning whenever the checkpoint had a contract snapshot.
- A focused pre-fix regression corrupted `contract.json` after writing a long-run checkpoint. Before the fix, `appendCheckpointResumeHint` returned `injected=true`, appended the resume note, and returned no error while warnings said the contract was missing rather than corrupt.

Impact:

`continue` recovery could inject a checkpoint handoff while the current durable contract fact was unreadable. That misdirects the model/operator toward a drift warning instead of storage repair, and it can append a new harness reminder before reporting the corrupt completion/recovery authority.

Minimal fix:

- Preserve the existing warning for a genuinely absent current `contract.json` when the checkpoint still has a contract snapshot.
- Return an actionable `load contract.json for checkpoint drift` error for corrupt or otherwise unreadable current contract snapshots.
- Do not inject the checkpoint resume note when the current contract fact is corrupt.
- Add focused runtime coverage proving corrupt current contract state blocks checkpoint resume-note injection while the existing isolation/trust drift warning path remains green.

Validation:

- Focused pre-fix runtime regression proving corrupt `contract.json` was reported as a missing current contract warning and the checkpoint resume note was injected.
- Focused post-fix runtime regression proving corrupt `contract.json` returns an error and no checkpoint resume note is appended.
- Existing checkpoint isolation/trust drift regression remains green.
- Standard grouped validation before commit.

### FCA-20260526-152: Session summary hides corrupt optional recovery facts

Severity: Medium

Evidence:

- `spec/18-durable-contract-and-completion.md` states `session.md` is a derived view, not a fact source, but it is still the operator-readable summary used for recovery and handoff.
- `spec/01-runtime-architecture.md` says SessionSummaryWriter aggregates contract, artifact tracker, provider attempts, children, queue, background notifications, and checkpoint facts.
- `internal/runtime/session_summary.go` rendered Goal, Plan Mode, contract, parent coordination, and checkpoint sections as `not recorded` whenever the corresponding `Load...` call failed, without distinguishing absent optional files from malformed existing files.
- A focused pre-fix summary regression corrupted `goal.json`, `planmode.json`, `contract.json`, `parent-coordination.json`, and `checkpoints/longrun-latest.json`; before the fix, `session.md` said each affected section was not recorded.

Impact:

Operators and recovery prompts could read `session.md` and conclude no Goal, Plan Mode, contract, parent wait state, or checkpoint existed when the real issue was corrupt durable state. That does not change authoritative gates, but it weakens the derived recovery artifact exactly when a human needs diagnostics.

Minimal fix:

- Keep absent optional files rendered as `not recorded`.
- Render non-missing load failures in the affected `session.md` sections with the specific file name and error.
- Do not make `session.md` authoritative or block runtime execution on summary-writing paths.
- Add focused summary coverage for corrupt optional fact files.

Validation:

- Focused pre-fix summary regression proving corrupt optional files were rendered as `not recorded`.
- Focused post-fix summary regression proving the generated Markdown includes `goal.json`, `planmode.json`, `contract.json`, `parent-coordination.json`, and `longrun-latest.json` load errors.
- Existing provider-attempt and task-summary/checkpoint regressions remain green.
- Standard grouped validation before commit.

### FCA-20260526-151: Compaction hides corrupt feature-list state

Severity: Medium

Evidence:

- `spec/04-tools-and-skills.md` defines `feature_list_create` / `feature_list_update` / `feature_list_read` as durable feature-list tools.
- `spec/18-durable-contract-and-completion.md` includes pre-completion feature checks, and the existing `FCA-20260526-143` fix established that absent `feature_list.json` is optional but malformed existing feature-list state must not be treated as absent.
- `internal/runtime/compaction.go` loaded the feature list for compaction summaries with `if fl, err := LoadFeatureList(...); err == nil { ... }`, silently dropping every load error.
- A focused pre-fix compaction regression wrote invalid JSON to `feature_list.json`; before the fix, compaction succeeded, wrote a durable summary artifact, and embedded `"feature_list": null`.

Impact:

Long-running sessions could compact context while hiding a malformed durable feature list. After compaction, the model would receive a summary that looked like no feature-list state existed, weakening feature convergence recovery and making the compaction artifact contradict the existing session facts.

Minimal fix:

- Keep missing `feature_list.json` optional.
- Keep the existing symlink/path-safety behavior that ignores symlinked feature-list state rather than treating unsafe external content as session evidence.
- Return a compaction error for corrupt or otherwise unreadable feature-list snapshots.
- Add focused compactor coverage proving corrupt `feature_list.json` stops compaction and no misleading summary artifact is written.

Validation:

- Focused pre-fix compactor regression proving corrupt `feature_list.json` was hidden as `feature_list: null`.
- Focused post-fix compactor regression proving corrupt feature-list state is reported and no summary artifact is written.
- Existing pre-completion feature-list gate coverage remains green.
- Standard grouped validation before commit.

### FCA-20260526-150: Queue worker treats parent fact persistence failure as child failure

Severity: High

Evidence:

- `spec/01-runtime-architecture.md` requires the queue worker to write real child results back to the parent through durable notifications and queue job/session facts.
- `spec/17-web-console.md` requires queue job completion/failure to be observable through the parent Background inspector / timeline and selected job facts.
- `internal/runtime/delegation.go` `ProcessNextJob` used best-effort `r.emit` for `queue.job.claimed`, `queue.job.notified`, and terminal queue lifecycle events, so parent `events.jsonl` append failures were not reported.
- When a completed child session later failed inside `Engine.reconcileLinkedQueueJob` because the parent queue notification/event facts could not be persisted, `ProcessNextJob` treated that `runErr` as a normal child failure and rewrote the completed queue job as failed.
- A focused pre-fix runtime regression had the child block the parent `events.jsonl` before calling `finish`; before the fix, `ProcessNextJob` returned nil error for best-effort worker events, or after event errors were surfaced, returned a failed queue job with a parent persistence error as `LastError` instead of preserving the completed child result.

Impact:

A queue worker could lose or misclassify a successful child result when parent-visible queue facts were temporarily unwritable. That conflates infrastructure/file-fact persistence errors with child/provider failure, pollutes queue failure summaries, and weakens Web/CLI recovery because the queue job no longer reflects the child session's true terminal state.

Minimal fix:

- Make queue worker lifecycle events use error-returning durable appends with the same queue persistence retry wrapper used for queue job and parent notification writes.
- Return the already-persisted child terminal `RunResult` alongside linked queue reconciliation errors from the engine.
- Wrap linked queue reconciliation failures in a typed runtime error so `ProcessNextJob` can distinguish parent/queue persistence errors from normal child/provider failures.
- Preserve normal queue failure behavior for genuine child/provider errors.
- Add focused runtime coverage proving a parent queue lifecycle event append failure is reported without turning a completed child into a failed job.

Validation:

- Focused pre-fix runtime regression proving parent event append failure during queue processing either disappeared as a best-effort event loss or was converted into a false failed queue job.
- Focused post-fix runtime regression proving the queue worker reports the parent event append error while returning the completed job result.
- Existing normal child/provider failure regression remains green.
- Standard grouped validation before commit.

### FCA-20260526-149: Terminal queue reconciliation ignores parent-visible fact write failures

Severity: High

Evidence:

- `spec/01-runtime-architecture.md` requires queue workers to deliver child completion/failure results back to the parent session as durable control notifications and queue lifecycle events.
- `spec/17-web-console.md` requires parent Background inspector / timeline visibility for queue job completion and failure, including background notifications and selected job facts.
- `internal/session/store.go` `ensureTerminalQueueJobParentState` updated parent coordination, then called `ensureBackgroundNotification` and `ensureQueueLifecycleEvent`.
- `ensureBackgroundNotification` discarded the `EnsureBackgroundNotification` error, and `ensureQueueLifecycleEvent` discarded both `LoadEvents` and `AppendEvent` errors.
- Focused pre-fix store regressions replaced `control/background.jsonl` or `events.jsonl` with a directory before `LoadJob` reconciled a terminal parent queue job; before the fix, `LoadJob` returned nil error even though required parent-visible facts could not be written.

Impact:

Queue reconciliation could report terminal job repair success while the parent session lacked the durable background notification or timeline event needed for Web/CLI recovery and parent completion gating. That creates false-success queue repair and makes child/queue results less observable to the parent.

Minimal fix:

- Return `EnsureBackgroundNotification` failures from terminal queue parent-state reconciliation.
- Return `LoadEvents` / `AppendEvent` failures from queue lifecycle event reconciliation.
- Preserve event idempotency by returning nil when the matching queue lifecycle event already exists.
- Add focused store coverage for blocked background notification and event writes during terminal queue reconciliation.

Validation:

- Focused pre-fix store regressions proving blocked parent notification/event writes returned nil from terminal queue reconciliation.
- Focused post-fix store regressions proving those write failures are reported.
- Existing terminal queue completion/failure and duplicate-status reconciliation regressions remain green.
- Standard grouped validation before commit.

## Reviewed Areas With No Confirmed New Issue Yet

These areas have been inspected enough to avoid duplicating already-fixed items, but the broad audit is still ongoing:

- Embedded asset ETag/gzip handling in `internal/webconsole/service.go` has current tests for ETag, gzip, q-value negotiation, and 304 behavior.
- Markdown link/image sanitizer currently uses `rel="noopener noreferrer"` and `.md-img`; previous inline-style/link-rel concerns are already fixed.
- Workspace browser path resolution uses `tools.ResolveWorkspacePath` and denies `.git`, `.go-cli-agent`, credential directories, `.env` variants, and common private-key / credential-like filenames.
- Skill upload has multipart size, zip file count, per-entry size, total uncompressed size, traversal, absolute path, symlink destination, and direct-child uninstall checks.
- Static frontend syntax parses with Node.
- Provider cancellation during provider calls propagates `context.Canceled` through `JSONClient`, `classifyTransportError`, and `readAllWithIdleTimeout`; `Engine.Run` only maps it to pause or interrupt-steer behavior when the matching control flag exists, and pending interrupt steer requests are deferred or accepted at the next safe boundary.

## Review Plan

### Pass 1: Current confirmed fixes

1. Fix FCA-20260522-001 with minimal frontend state/rendering changes and focused tests.
2. Validate and commit that frontend slice.
3. Fix FCA-20260522-002 with lifecycle tracking and focused service tests.
4. Validate and commit that backend lifecycle slice.
5. Append update records to this document for both commits.

### Pass 2: Backend core/runtime/provider audit

Review slices:

- `internal/runtime`: engine loop, completion controller, goal/plan gates, steer/interrupt, compaction, provider attempts.
- `internal/provider`: request shaping, replay facts, timeout/retry, usage telemetry.
- `internal/tools`: workspace path safety, shell execution, skill tools, artifact guard.
- `internal/session`: store ID validation, owner-only permissions, JSONL append/read, queue reconciliation.
- `internal/app` and `pkg/agent`: CLI/API adapter boundaries and config propagation.

Evidence gates:

- Prefer tests or small repros for any finding.
- Reject style-only or speculative findings.
- Confirm whether existing tests already cover the behavior before proposing code.

### Pass 3: Web Console backend/frontend audit

Review slices:

- `internal/webconsole/service.go`, `dto.go`, `audit.go`, `assets.go`.
- `internal/webconsole/assets/*.js`, `styles.css`, `index.html`.
- `validation/scripts/webconsole_*.mjs`.

Evidence gates:

- Check user-facing Web-first behavior against `spec/17-web-console.md`.
- Keep fixes local; do not introduce a framework or browser-side authority.
- Validate with JS syntax/unit tests, focused Go tests, and browser smoke where behavior requires DOM/API integration.

## Accuracy Review Log

### Review 1

- Confirmed FCA-20260522-001 against frontend code and `ValidatePlanModeAnswers`.
- Confirmed FCA-20260522-002 against `Service.Close`, `trackLaunch`, and `launchPlanModeContinue`.
- Removed old-plan claims already addressed by `bbd7332` and earlier commits.

### Review 2

- Rechecked all cited symbols and paths against current code:
  - `handlePlanInputAction` and `collectPlanInputAnswers` in `internal/webconsole/assets/app.js`
  - `renderPlanInputRequest` and `renderPlanInputQuestion` in `internal/webconsole/assets/session-view.js`
  - `ValidatePlanModeAnswers` in `internal/session/planmode.go`
  - `Service.Close`, `trackLaunch`, `handlePlanModeApprove`, `handlePlanModeRevise`, `handlePlanModeInput`, `handleMissionPlanApprove`, and `launchPlanModeContinue` in `internal/webconsole/service.go`
- Confirmed `spec/17-web-console.md` documents `/planmode/input` as explicit `{ request_id, answers }`.
- Confirmed the older untracked `docs/webconsole-frontend-optimization-plan.md` is not treated as current evidence for this audit.

### Review 3

- Confirmed FCA-20260522-003 against the current `decodeJSON` implementation.
- Confirmed `decodeJSON` is shared by Web mutation endpoints such as session start, continue, Plan Mode, Goal/Mission, config, and skill uninstall handlers.
- Confirmed existing tests covered unknown fields but not trailing concatenated JSON values.

### Review 4

- Confirmed FCA-20260522-004 against the current `decodeCommandToolArgs` implementation.
- Confirmed ordinary built-in tool handlers use `json.Unmarshal`, which already rejects trailing non-whitespace bytes; the gap is specific to skill command tools using the custom `UseNumber` decoder.
- Confirmed existing skill command tests covered missing required fields, closed schemas, and `additionalProperties: true`, but not trailing concatenated JSON values.

### Review 5

- Confirmed FCA-20260522-005 against `request_user_input`, `AnswerActivePlanInput`, `sessionDetail`, `activeHandleOwner`, and `pruneInactiveHandles`.
- Confirmed Plan Mode planning must resume through `submit_plan` after live input; a direct `finish` response is not a valid planning-stage completion path.
- Confirmed the fix keeps current-process handles for non-terminal states without making Web Console handles durable or introducing a second state authority.

### Review 6

- Confirmed FCA-20260522-006 against `handlePlanModeInput`, `AnswerActivePlanInput`, and `ValidatePlanModeAnswers`.
- Confirmed the validation belongs in the Web service adapter before selecting the live-handle or fallback-continue execution path.
- Confirmed the fix still preserves the runtime/store Plan Mode authority by validating against `planmode.json` rather than introducing browser-side state as authority.

### Review 7

- Confirmed FCA-20260522-007 against `Continue`, `acquireRunSlot`, `SaveState`, and the engine's first durable `running` write.
- Confirmed the fix belongs in the session store transition path because runner-local locks do not cover separate runners or processes.
- Confirmed the implementation keeps auto-continue compatibility by preserving same-session runner slot re-entry while moving cross-run exclusion to durable state.

### Review 8

- Confirmed FCA-20260522-008 against `goalCompletionGate`, `goalBudgetWrapUpPrompt`, `Engine.complete`, and `spec/18-durable-contract-and-completion.md`.
- Confirmed the existing engine budget-wrap-up boundary already returns to `awaiting_input` when `finish` is not allowed.
- Confirmed `GoalStatusComplete` remains the only path that allows a budget-exhausted goal to finish as completed.

### Review 9

- Confirmed FCA-20260522-009 against `addParentQueueJob`, `resolveParentQueueJob`, `resolveParentChildSession`, `LoadParentCoordination`, and `SaveParentCoordination`.
- Confirmed queue status reconciliation had the same load/mutate/save shape and should share the transactional mutation helper.
- Confirmed the fix keeps `parent-coordination.json` as the durable authority and only changes how updates are merged.

### Review 10

- Confirmed FCA-20260522-010 against `spec/17-web-console.md` accessibility requirements and the current send-button markup.
- Confirmed the fix is static shell markup only and does not change WebConsole control flow.

### Review 11

- Confirmed FCA-20260522-011 against `sendMessage`, `updateUI`, and the state transition before `startSession` returns a durable session id.
- Confirmed the fix stays in frontend request guarding and does not create browser-side session authority.

### Review 12

- Confirmed FCA-20260522-012 against `settings-view.js` save handling, `handleGoalAction`, `handleSkillAction`, and the explicit confirmation requirement in `spec/17-web-console.md`.
- Confirmed session delete/clear history already had confirmation; the fix covers the remaining risky actions named in the finding.

### Review 13

- Confirmed FCA-20260522-013 against `drainBackground`, `UpdateBackgroundNotifications`, `AppendBackgroundNotification`, and `EnsureBackgroundNotification`.
- Confirmed `UpdateSteerRequests` already had a merge-and-lock pattern for the same stale-snapshot class, while background notifications did not.
- Confirmed the fix belongs in `internal/session` so WebConsole, runtime, queue workers, and CLI paths continue sharing the same durable file authority.

### Review 14

- Confirmed FCA-20260522-014 against `spec/03-provider-contracts.md`, `providerOptionsFromConfig`, runtime turn request construction, `Runner.Probe`, and the Anthropic adapter's `promptCacheEnabled` gate.
- Confirmed the issue is diagnostic fidelity only: normal session execution already passes persisted `PromptCache` into provider requests.
- Confirmed the fix belongs in `Runner.Probe`, with Web and CLI continuing to use the runtime facade rather than building provider-specific cache markers themselves.

### Review 15

- Confirmed FCA-20260525-015 against `spec/17-web-console.md`, `/api/meta`, the Workspace view copy, and `TestServiceMetaReportsDefaultWorkspaceSubdirOnly`.
- Confirmed the fix is a Web service/UI contract correction only; it does not change runtime session workdir selection, file browser path safety, or server-side workspace facts.
- Confirmed the default Web page remains a local read-only workspace browser, not a browser-side IDE or root-switching authority.

### Review 16

- Confirmed FCA-20260525-016 with a static scan for `strings.HasPrefix(rel, "..")`; only the session visible-path helper and tools display helper used the unsafe prefix shape.
- Confirmed adjacent helpers such as `pathWithinRoot` and `resolveQueueVisiblePath` already use separator-aware traversal checks, so the fix should align these two outliers rather than change broader path policy.
- Confirmed this is a false-negative visibility/display bug, not a workspace escape; outside paths still remain rejected.

### Review 17

- Confirmed FCA-20260525-017 against `spec/03-provider-contracts.md` and the owning Google adapter response path.
- Confirmed existing candidate-level `finishReason == "SAFETY"` handling is correct but does not cover prompt-level safety blocks that arrive without candidates.
- Confirmed the fix belongs in `internal/provider/google.go`, keeping Google response-shape logic inside the provider adapter and letting runtime consume the normalized `blocked` stop reason.

### Review 18

- Confirmed FCA-20260525-018 against `spec/03-provider-contracts.md`, OpenAI Responses response parsing, runtime assistant-message persistence, and provider-attempt recording.
- Confirmed the issue is provider-adapter owned because OpenAI returns tool arguments as a string, while runtime/session expects `ToolCall.Arguments` to be valid JSON for durable message encoding and later replay.
- Confirmed the fix should classify malformed OpenAI tool arguments as `response_parse_error` before runtime records provider success or persists an assistant message.

### Review 19

- Confirmed FCA-20260525-019 against the OpenAI stop-reason contract, adapter status mapping, and runtime provider stop failure handling.
- Confirmed `status=completed` remains the only OpenAI status that should map to `done_candidate`; unrecognized non-empty statuses should not inherit the default normal-candidate path.
- Confirmed the fix belongs in the OpenAI adapter, keeping provider status interpretation out of Web, CLI, tools, and generic runtime code.

### Review 20

- Confirmed FCA-20260525-020 against the Google stop-reason contract, adapter finish-reason mapping, and runtime provider stop failure handling.
- Confirmed `finishReason=STOP` is the normal done-candidate case; non-empty unrecognized finish reasons should not inherit the default normal-candidate path.
- Confirmed the fix belongs in the Google adapter, preserving provider-specific finish-reason interpretation inside the provider layer.

### Review 21

- Confirmed FCA-20260525-021 against the Anthropic stop-reason contract, adapter stop-reason mapping, and runtime provider stop failure handling.
- Confirmed `stop_reason=end_turn` is the normal done-candidate case; non-empty unrecognized stop reasons should not inherit the default normal-candidate path.
- Confirmed the fix belongs in the Anthropic adapter, preserving provider-specific stop-reason interpretation inside the provider layer.

### Review 22

- Confirmed FCA-20260525-022 against the provider tool schema contract, OpenAI / Anthropic / Google adapter response parsing, and runtime assistant-message persistence before tool dispatch.
- Confirmed the fix belongs in provider adapters because provider response shape normalization is adapter-owned and should fail before runtime records provider success.
- Confirmed command tool argument decoding already rejects non-object JSON, so this slice only needed to normalize provider-emitted tool-call arguments before they become durable tool calls.

### Review 23

- Confirmed FCA-20260525-023 against `spec/17-web-console.md`, backend message paging handlers, and frontend polling merge state.
- Confirmed the backend already returned correct tail and older-page windows; the gap was a frontend merge-state issue after a non-overlapping tail refresh.
- Confirmed the fix keeps `messages.jsonl` and the Web service API as the durable facts and changes only presentation-layer merge behavior.

### Review 24

- Confirmed FCA-20260525-024 against the WebConsole unsafe mutation guard and `spec/17-web-console.md`'s exact `Content-Type: application/json` requirement.
- Confirmed every unsafe `/api/` route still passes through `guardUnsafeAPIRequest`, so the fix belongs at that shared boundary rather than in individual mutation handlers.
- Confirmed `mime.ParseMediaType` preserves valid `application/json` parameters while rejecting different JSON-family media types.

### Review 25

- Confirmed FCA-20260525-025 against `Service.Close`, `startSession`, `trackLaunch`, `handleContinueSession`, and `launchPlanModeContinue`.
- Confirmed continue and Plan Mode continue already create handles before tracked goroutines, while initial start had a pre-session-id goroutine that was neither handle-owned nor wait-group tracked.
- Confirmed the fix stays inside the Web service adapter lifecycle and does not introduce browser-side or in-memory session state authority; durable session facts remain owned by runtime/session store.

### Review 26

- Confirmed FCA-20260525-028 against the goal store mutation paths: `UpdateGoalAccounting`, `CompleteGoal`, and `RecordGoalProgress`.
- Confirmed runtime accounting and model progress can be written through separate `Store` instances, so `Store.mu` alone is not a cross-runner or cross-process transaction boundary.
- Confirmed the fix belongs in `internal/session`, preserving `goal.json` as the durable current snapshot and avoiding provider-, Web-, or tool-layer merge logic.

### Review 27

- Confirmed FCA-20260525-029 against Web goal status / mission approval handlers, CLI goal status / direct approval paths, and linked Plan Mode mission approval.
- Confirmed these paths mutate only operator-control fields, so they should use narrow transactional store helpers rather than re-saving stale whole-goal snapshots.
- Kept Web/CLI mission plan patch and task-sync paths as a separate review area because `create_tasks_from_plan` interacts with the task graph and should not be mixed into this status/approval slice.

### Review 28

- Confirmed FCA-20260525-030 against Web goal patch, Web mission plan patch, and `SyncMissionPlanTasks`.
- Confirmed the `create_tasks_from_plan` branch has to keep task graph writes outside the goal lock, then merge only generated feature/task IDs into the latest goal snapshot.
- Confirmed the fix remains in the session store / Web adapter boundary and does not move task orchestration decisions into runtime.

### Review 29

- Confirmed FCA-20260525-031 against `planmode.json` store transition methods and Web/CLI/runtime Plan Mode control paths.
- Confirmed `planmode.json` is the current durable fact and `planmode-history.jsonl` cannot substitute for lost current pending request, approval, or execution fields.
- Confirmed the fix belongs in `internal/session` so Web, CLI, runtime, tools, and recovery paths share the same serialized transition behavior.

### Review 30

- Confirmed FCA-20260525-032 against `load_skill`, `markSkillLoaded`, `Engine.Run` state persistence, session summary, and compaction loaded-skill consumers.
- Confirmed `loaded_skills` is a monotonic session fact used for idempotency and operator context; no current user/runtime API intentionally clears loaded skills.
- Confirmed the fix belongs in `SaveState`, because the stale overwrite can come from multiple engine state-save boundaries after any tool-owned loaded-skill update.

### Review 31

- Confirmed FCA-20260525-033 against `UpdateSteerRequests`, Runner `Steer`, Engine `deferPendingInterrupts`, Engine `drainSteer`, and the session detail consumers of `pending_steer_count`.
- Confirmed `control/steer.jsonl` already remains authoritative and merge-safe; the bug is the derived `state.json` counter drifting after a stale snapshot or later engine state save.
- Confirmed the fix belongs in the session store/runner/runtime boundary, with no change to steer queue acceptance semantics or provider/tool control flow.

### Review 32

- Confirmed FCA-20260525-034 against `refreshContractForSession`, `contractsEquivalent`, `TrackToolResult`, and `requiredArtifactGate`.
- Confirmed the contract source is the latest external user message, so a later same-path artifact instruction is a new completion contract even when the extracted artifact path is unchanged.
- Confirmed the fix belongs in the runtime/session contract snapshot, not in Web or CLI adapters, because all accepted user inputs share the same artifact completion gate.

### Review 33

- Confirmed FCA-20260525-035 against `CreateTask`, `UpdateTask`, `NextTaskID`, `ListTasks`, `SaveTasks`, model `task_create` / `task_update`, and mission `SyncMissionPlanTasks`.
- Confirmed the problem is durable task graph state loss/corruption, not only presentation drift: task files are the source for Web task board, compaction, checkpoint, and session summary.
- Confirmed the fix belongs in `internal/session` so tool, Web, runtime, mission-sync, and CLI/API paths share the same cross-store task graph transaction behavior.

### Review 34

- Confirmed FCA-20260525-036 against `saveJobLocked`, `LoadJob`, `listJobs`, `ClaimNextQueuedJob`, `RefreshQueueJobHeartbeat`, `ProcessNextJob`, and queue reconciliation repair paths.
- Confirmed the issue is not duplicate claim; `ClaimNextQueuedJob` already uses atomic rename and has regression coverage. The confirmed failure is stale duplicate status files after a partial status move/cleanup.
- Confirmed the fix belongs in `internal/session` so WebConsole, CLI queue commands, runtime worker, session summaries, and reconciliation all use the same canonical queue job fact.

### Review 35

- Confirmed FCA-20260525-037 against `spec/15-background-queue.md`, `spec/02-cli-and-config.md`, runtime queue status mapping, session store `queueStatuses()`, and CLI doctor `doctorQueueStatuses()`.
- Confirmed this does not affect runtime queue processing or WebConsole queue views; those use `internal/session` queue readers that include `blocked`.
- Confirmed the fix belongs in `internal/app/doctor_helpers.go` because the bug is in the doctor diagnostic directory scan, not in the durable queue store.

### Review 36

- Confirmed FCA-20260525-038 against `ProcessNextJob`, `Runner.Continue`, `Engine.complete`, `Engine.fail`, `awaitingInput`, `pause`, session store queue reconciliation, `EnsureBackgroundNotification`, parent coordination, and the background-result completion gate.
- Confirmed worker same-call completion is already handled; the gap is resumed blocked queue children that become terminal through ordinary continue paths.
- Confirmed the fix should not add a workflow engine or automatic child orchestration. It should only propagate the already-durable child session terminal/resumable state into the linked queue job and parent facts.

### Review 37

- Confirmed FCA-20260525-039 against `Engine.drainBackground`, `Store.UpdateBackgroundNotifications`, `mergeBackgroundNotifications`, `EnsureBackgroundNotification`, `NewBackgroundNotification`, and Web session detail background notification rendering.
- Confirmed this is not a duplicate-notification display issue; the store merge can overwrite the durable terminal notification fact before Web or runtime can observe it.
- Confirmed the fix belongs in `internal/session` so runtime background drain, Web detail, session summaries, and CLI/API queue readers share the same durable notification merge semantics.

### Review 38

- Confirmed FCA-20260525-040 against `spec/17-web-console.md`, `renderNotificationCard`, `renderBackgroundNotificationsPreview`, existing `data-open-job` action handling, and selected queue job detail refresh.
- Confirmed this is a frontend navigation/traceability gap, not a backend queue fact loss; the API and selected job panel already exist.
- Confirmed the fix should stay in the Web renderer and reuse `data-open-job`, without adding a standalone queue page or new browser-side authority.

### Review 39

- Confirmed FCA-20260525-041 against `spec/12-task-system.md`, `BuildTaskBoard`, `renderTasksPanel`, and `task_list` metadata.
- Confirmed this is not a cosmetic label issue: the shared task-board derived facts conflated cancelled and completed tasks before Web and tool consumers read them.
- Confirmed the fix belongs in `internal/session` with compatible derived counters/groups, plus the Web renderer test to keep cancelled facts visible.

### Review 40

- Confirmed FCA-20260526-042 against `spec/17-web-console.md`, `handleListFiles`, `handleReadFile`, `webFileBrowserPathDenied`, `webFileBrowserNameDenied`, and `TestServiceWorkspaceRoutesListReadAndRejectEscape`.
- Confirmed this is a Web workspace-browser leakage issue, not a runtime session redaction rule; reports, provider views, and session artifacts should not get default secret rewriting.
- Confirmed the fix belongs in the browser deny helper used by both listing and read checks, without changing the workspace parent-navigation behavior or introducing a browser-side file editor.

### Review 41

- Confirmed FCA-20260526-043 against `spec/17-web-console.md`, `handleUpdateConfig`, `config.UpsertEnvFile`, `config.WriteFile`, and Web audit event ordering.
- Confirmed this is a sensitive mutation ordering bug: the key can be durably written before the request's config/audit side succeeds.
- Confirmed the fix should only reorder Web Settings persistence and preflight audit-log writability; `POST /api/config/test` already uses a temporary env var and does not persist config or `.env`.

### Review 42

- Confirmed FCA-20260526-044 against `spec/17-web-console.md`, `handleDeleteSession`, `handleClearSessions`, `handleUploadSkill`, `handleUninstallSkill`, and `appendAuditEvent` ordering.
- Confirmed this is the same sensitive-action auditability class as FCA-043, but covers non-config mutations that delete local state or change installed skills.
- Confirmed the fix belongs in Web service handlers as an audit-log preflight, not in session store or skill extraction internals, because the audit requirement is a Web local-console contract.

### Review 43

- Confirmed FCA-20260526-045 against `spec/12-task-system.md`, `taskCounts`, `writeSessionSummary`, `writeLongRunCheckpoint`, `contextLoadedEventData`, and compaction summary/event generation.
- Confirmed this is a recovery-artifact drift from FCA-20260525-041: Web/task-list derived facts were fixed, but runtime summaries and compaction metadata still used the old conflated helper.
- Confirmed the fix belongs in the shared runtime helper and derived artifact writers, preserving compatibility by keeping existing `completed_task_count` while adding `cancelled_task_count` and `done_task_count`.

### Review 44

- Confirmed FCA-20260526-046 against `spec/01-runtime-architecture.md`, `spec/17-web-console.md`, `SessionDetailResponse.ProviderAttempts`, `sessionDetail`, and the session summary renderer.
- Confirmed the backend API already returned tailed provider-attempt facts, so the drift was confined to the frontend Web operator surface rather than provider ledger persistence.
- Confirmed the minimal fix belongs in `session-view.js` with renderer coverage, not in runtime/provider code.

### Review 45

- Confirmed FCA-20260526-047 against `spec/17-web-console.md`, `handleMissionPlanApprove`, `handlePlanModeAction`, `handleGoalAction`, `refreshCurrentSession`, and polling predicates.
- Confirmed the backend correctly launches linked Plan Mode approval and returns `202 Accepted`; the bug is frontend state handling for the Goal inspector path only.
- Confirmed the fix belongs in the shared frontend launch-response handling, not in runtime or store Plan Mode state transitions.

### Review 46

- Confirmed FCA-20260526-048 against `spec/12-task-system.md`, `BuildTaskBoard`, `tasksCommand`, `normalizeTaskBoard`, and existing CLI task command tests.
- Confirmed this is a CLI fallback visibility drift after FCA-20260525-041: Web and tool task facts are separated, but normal CLI text output skipped the new `cancelled` group.
- Confirmed the fix belongs in CLI rendering only; no session task graph or Web change is needed.

### Review 47

- Confirmed FCA-20260526-049 against `spec/17-web-console.md`, `handleUpdateConfig`, `config.WriteFile`, `config.UpsertEnvFile`, and Web config audit-event ordering.
- Confirmed the prior FCA-20260526-043 fix delayed API-key persistence until after config writes, but did not preflight env-file failures that occur after the config write.
- Confirmed the fix belongs in Web config mutation preflight/order only; `config.UpsertEnvFile` should remain the final path-safe writer.

### Review 48

- Confirmed FCA-20260526-050 against `spec/12-task-system.md`, `spec/04-tools-and-skills.md`, `spec/08-sdk-and-api-evolution.md`, `spec/17-web-console.md`, `BuildTaskBoard`, and `task_list` metadata.
- Confirmed this is spec drift only: current runtime, Web, CLI, and model-visible task facts already expose separate cancelled and combined done facts.
- Confirmed the fix belongs in the stale spec descriptions, not in code.

### Review 49

- Confirmed FCA-20260526-051 against `spec/17-web-console.md`, `handlePlanModeApprove`, `handlePlanModeRevise`, `launchPlanModeContinue`, `ApprovePlanMode`, and `RevisePlanMode`.
- Confirmed the backend store already rejects invalid Plan Mode statuses, but Web approval/revision launches the async continue path before surfacing those status errors.
- Confirmed the fix belongs in Web action preflight so invalid operator actions fail before session run claiming or message append.

### Review 50

- Confirmed FCA-20260526-052 against `spec/17-web-console.md`, `handlePlanModeInput`, `handlePlanModeCancel`, `AnswerActivePlanInput`, `CancelActivePlanInput`, and the active Plan Mode waiter channel lifecycle.
- Confirmed the backend validation from FCA-20260522-006 rejects malformed answers before live delivery, but duplicate valid delivery could still block because active waiter ownership was not claimed before sending.
- Confirmed the fix belongs in the runtime active input helper, not in frontend button throttling, because HTTP retries and races must be safe at the backend control boundary.

### Review 51

- Confirmed FCA-20260526-053 against `spec/17-web-console.md`, `handleUploadSkill`, `processSkillZip`, `sanitizeDirName`, `pathWithinRoot`, and the existing skill upload zip-slip / size-limit regressions.
- Confirmed this is not just a cosmetic duplicate-name issue: duplicate sanitized targets are processed sequentially, and each root removes/recreates the same target before extracting its files.
- Confirmed the fix belongs in pre-mutation extraction planning so every target is known safe before any installed skill directory is removed.

### Review 52

- Confirmed FCA-20260526-054 against `spec/17-web-console.md`, `handleDeleteSession`, `hasActiveDescendantHandle`, `ensureSessionTreeNotLive`, `DeleteSessionTree`, and Web delete/clear regressions.
- Confirmed the previous direct-parent/root check protected root deletion and direct children, but missed active handles below an intermediate child when the deeper session was non-running and therefore not blocked by durable running-state checks.
- Confirmed the fix belongs in the Web service delete preflight, not in `DeleteSessionTree`, because the store cannot know which current-process Web handles are still live.

### Review 53

- Confirmed FCA-20260526-055 against `spec/17-web-console.md`, `activeHandleOwner`, `latestActiveOwnerFromEvents`, `sessionDetail`, and existing active-owner tests.
- Confirmed the issue is diagnostic/state-mapping drift, not provider/runtime ownership: current-process handles still win, but durable released clues were mapped as running external ownership when the session state remained `running`.
- Confirmed the fix belongs in Web owner clue mapping and should not change persisted event shape or make handle events authoritative.

### Review 54

- Confirmed FCA-20260526-056 against `spec/03-provider-contracts.md`, `normalizeToolCallArguments`, `anthropicMessages`, `anthropicProviderContent`, `googleContents`, `googleProviderParts`, and provider replay regressions.
- Confirmed provider ingress already rejects malformed live tool-call arguments, but adapter outbound replay reconstruction trusted persisted provider-native blocks and fallback tool calls.
- Confirmed the fix belongs in provider adapters so Web, CLI, runtime, and tool layers continue to treat provider-native replay facts as adapter-owned opaque facts.

### Review 55

- Confirmed FCA-20260526-057 against `spec/03-provider-contracts.md`, `openAIInput`, `normalizeToolCallArguments`, prior OpenAI ingress argument tests, and focused OpenAI replay regressions.
- Confirmed this is not a provider-native reasoning-block issue: OpenAI reasoning replay still filters by provider/profile/API/model, but the ordinary persisted `session.ToolCall.Arguments` fallback path was not revalidated on outbound replay.
- Confirmed the fix belongs in the OpenAI adapter replay builder, not in Web, CLI, runtime, or session storage, because provider-specific replay shape construction remains adapter-owned.

### Review 56

- Confirmed FCA-20260526-058 against `spec/14-multi-agent-and-isolation.md`, `spec/15-background-queue.md`, Web `sessionTreeTargetIDs`, store `DeleteSessionTree`, and delete/clear regressions.
- Confirmed this is a store/Web consistency issue: Web preflight and running-job checks already treat root-linked sessions as part of a tree, but the durable store deletion helper only followed parent links.
- Confirmed the fix belongs in session storage tree deletion so CLI, Web, SDK, and future cleanup callers share the same durable tree semantics.

### Review 57

- Confirmed FCA-20260526-059 against `spec/00-product.md`, `spec/09-phase-plan.md`, `spec/11-spec-audit-and-traceability.md`, `spec/17-web-console.md`, store `ApproveMissionPlan`, CLI `goalPlanApproveCommand`, Web `handleMissionPlanApprove`, and runtime linked Plan Mode approval tests.
- Confirmed this is not a coverage-check issue: `CheckMissionPlanCoverage` correctly returns no blocking coverage for a missing mission, but approval should require an existing mission plan fact before coverage can be meaningful.
- Confirmed the fix belongs in the session store invariant so CLI, Web, runtime, SDK, and future callers cannot synthesize approved mission plans for ordinary goals.

### Review 58

- Confirmed FCA-20260526-060 against `spec/00-product.md`, `spec/01-runtime-architecture.md`, `spec/09-phase-plan.md`, `spec/11-spec-audit-and-traceability.md`, `CreatePlanMode`, `SubmitPlanMode`, `ApprovePlanMode`, other Plan Mode transition helpers, and focused blocked-history regressions.
- Confirmed this is not only an operator-readable artifact issue: Plan Mode history is specified as a session file fact source for state transitions, while `artifacts/planmode-plan.md` remains the derived Markdown plan.
- Confirmed the fix belongs in session storage transition helpers so CLI, Web, runtime, SDK, and future callers cannot report Plan Mode state changes as successful when their required history append failed.

### Review 59

- Confirmed FCA-20260526-061 against `spec/00-product.md`, `spec/01-runtime-architecture.md`, `spec/09-phase-plan.md`, `spec/11-spec-audit-and-traceability.md`, `CompleteGoal`, `ApproveMissionPlan`, `UpdateGoalAccounting`, `RecordGoalProgress`, and focused blocked-history regressions.
- Confirmed this is not only a CLI/Web display issue: Goal history is a durable session fact source, and runtime budget/accounting/progress paths depend on the store helpers returning whether durable facts were actually written.
- Confirmed the fix belongs in session storage transition helpers so CLI, Web, runtime, SDK, and future callers cannot report Goal state changes as successful when their required history append failed.

### Review 60

- Confirmed FCA-20260526-062 against `spec/01-runtime-architecture.md`, `spec/11-spec-audit-and-traceability.md`, runtime `appendPlanInputCancelToolResult`, store `CancelPlanMode`, and focused blocked-history regression.
- Confirmed this is a runtime recovery helper gap, not a duplicate of the store transition fix: the helper writes the replay tool result and an extra `planmode.input_cancelled` history fact before the store-owned `planmode.cancelled` transition.
- Confirmed the fix should preserve idempotent cancellation tool-result recovery while reporting failure when the durable input-cancelled history fact cannot be written.

### Review 61

- Confirmed FCA-20260526-067 against `spec/01-runtime-architecture.md`, `spec/03-provider-contracts.md`, `recordProviderRetry`, `recordProviderAutoResumeAttempt`, `recordProviderFailure`, `recordProviderSuccess`, and focused blocked-ledger regressions.
- Confirmed this is not a retry-policy control issue: the provider-attempt ledger does not drive adapter retry decisions, but it is still the durable recovery/diagnostic/Web fact for runtime-observed retry, auto-resume, final failure, success, response id, and cache telemetry.
- Confirmed the fix should stop before the next durable step when the ledger write fails: retry callbacks cancel the in-flight provider context, auto-resume does not recall the provider, and success fails before assistant-message persistence.

### Review 62

- Confirmed FCA-20260526-068 against `spec/01-runtime-architecture.md`, `spec/11-spec-audit-and-traceability.md`, engine budget wrap-up preparation, `UpdateGoalAccounting`, and focused blocked-history regression.
- Confirmed this is distinct from the previous Goal store transition fixes: the ignored history append is in runtime prepare logic after accounting already requested a stop-on-budget wrap-up and before provider execution.
- Confirmed the fix should report the durable fact failure before emitting `goal.budget_wrapup_turn_started` or sending the model a wrap-up turn.

### Review 63

- Confirmed FCA-20260526-069 against `spec/01-runtime-architecture.md`, `spec/12-task-system.md`, `spec/18-durable-contract-and-completion.md`, `CompletionController.TrackToolResult`, engine tool execution, and focused blocked-tracker regressions.
- Confirmed this is not a derived-summary issue: `artifact-tracker.json` is the durable required-artifact gate fact, and a failed tracker update after successful file write must be visible to runtime and recovery.
- Confirmed the fix must preserve provider replay completeness after the file side effect, so the engine appends the failed tool result and synthetic skipped results before failing.

### Review 64

- Confirmed FCA-20260526-070 against `spec/18-durable-contract-and-completion.md`, `requiredArtifactGate`, `EvaluateToolCall`, and focused blocked-tracker finish-gate regression.
- Confirmed this is a finish-gate state-source issue rather than a display summary issue: the gate could allow `finish` when `artifact-tracker.json` was unreadable or could not be refreshed.
- Confirmed the fix should block as `required_artifact_state`, preserving normal missing/stale artifact messages for valid tracker reads.

### Review 65

- Confirmed FCA-20260526-071 against `spec/01-runtime-architecture.md`, CLI `goalPlanApproveCommand`, `ApproveMissionPlan`, `AppendEvent`, and focused blocked-event regression.
- Confirmed this is not a duplicate of the goal-history fixes: `ApproveMissionPlan` already reports history failures, while the CLI adapter separately ignored the session event append failure.
- Confirmed the fix belongs in the CLI adapter for propagation plus the session store for event path context.

### Review 66

- Confirmed FCA-20260526-073 against `spec/01-runtime-architecture.md`, `spec/17-web-console.md`, `spec/18-durable-contract-and-completion.md`, `defRequestUserInput`, `Store.LoadState`, `Store.SaveState`, and focused blocked-state regressions.
- Confirmed this is a source-fact issue, not a derived summary issue: Plan Mode pending input needs both `planmode.json` and `state.json` to agree before Web/CLI recovery can treat the session as waiting for operator input.
- Confirmed the fix should stop before emitting `planmode.input_requested` or calling the interactive responder when the awaiting-input state transition cannot be durably written, while preserving the already-written pending request as a recovery fact.

### Review 67

- Confirmed FCA-20260526-074 against `spec/17-web-console.md`, the `skill-upload` file input change handler, `handleSkillAction`, and focused frontend pending-state regressions.
- Confirmed this is not a backend upload validation issue: upload size, zip entry, path safety, duplicate target, audit preflight, and uninstall mutation safety are already covered, while the missing behavior is the browser-side pending/disabled state during a real multipart mutation.
- Confirmed the fix should preserve existing backend error toast behavior while making all upload entry points visibly pending and non-repeatable until the request settles.

### Review 68

- Confirmed FCA-20260526-075 against `spec/01-runtime-architecture.md`, prior FCA-20260525-032 loaded-skill durability evidence, `defLoadSkill`, `skillLoaded`, `markSkillLoaded`, and a focused blocked-state regression.
- Confirmed this is not just a cache optimization: `loaded_skills` feeds idempotent tool behavior, compaction/session summary context, and recovery-visible operator facts.
- Confirmed the fix should fail the `load_skill` tool before returning the full skill body when the loaded-skill state fact cannot be persisted.

### Review 69

- Confirmed FCA-20260526-076 against `spec/01-runtime-architecture.md`, `spec/18-durable-contract-and-completion.md`, `LoadJob`, `ListJobs`, `ListPage`, `ListChildren`, `Engine.reconcileLinkedQueueJob`, `reconcileQueueJobSession`, and focused blocked-state regression evidence.
- Confirmed this is a source-fact issue because queue/job reconciliation changes both queue job status facts and linked child `state.json`; returning repaired job data without the matching state write creates contradictory durable recovery state.
- Confirmed parent queue lifecycle event repair remains best-effort in this slice; the promoted failure is the ignored write to `state.json` / queue job files, not missing diagnostic events.

### Review 70

- Confirmed FCA-20260526-077 against `spec/18-durable-contract-and-completion.md`, `QueueSubmit`, `SpawnAgent` background queue mode, `ProcessNextJob`, `addParentQueueJob`, `resolveParentQueueJob`, `reconcileParentQueueJobStatus`, and focused blocked-parent-coordination regressions.
- Confirmed this is a source-fact issue because `parent-coordination.json` feeds the parent completion gate; missing or stale unresolved queue facts are not just diagnostic event loss.
- Confirmed the event append in `emitParentCoordinationTransition` remains best-effort in this slice, because parked/resumed events are timeline facts while `parent-coordination.json` is the gate source.

### Review 71

- Confirmed FCA-20260526-078 against `spec/18-durable-contract-and-completion.md`, synchronous `Delegate`, `addParentChildSession`, `resolveParentChildSession`, and focused blocked-parent-coordination regression evidence.
- Confirmed this is distinct from FCA-20260526-077: this path is non-queued child delegation and uses child-session coordination lists rather than queue-job coordination lists.
- Confirmed returning the child runner's original error remains higher priority if both child execution and later coordination fail.

### Review 72

- Confirmed FCA-20260526-079 against `spec/13-live-input-and-steering.md`, `Engine.Run`, provider cancellation branches, and focused blocked-`events.jsonl` interrupt-steer evidence.
- Confirmed this event is not merely diagnostic in this path: the live-steer spec explicitly requires durable `provider.cancelled` evidence when provider preemption succeeds.
- Confirmed the fix stays scoped to provider cancellation and does not change generic best-effort timeline event behavior.

### Review 73

- Confirmed FCA-20260526-080 against `spec/03-provider-contracts.md`, `Engine.Run`, the auto-resume timeout branch, and focused blocked-`events.jsonl` evidence.
- Confirmed this is distinct from the earlier provider-attempt ledger fix: `provider-attempts.jsonl` records the durable diagnostic ledger, while the spec also requires a searchable `provider.auto_resume` event.
- Confirmed the fix stops before the auto-resume reminder and next provider call if the required event cannot be written.

### Review 74

- Confirmed FCA-20260526-081 against `spec/03-provider-contracts.md`, the provider adapter callback in `Engine.Run`, and focused blocked-`events.jsonl` retry evidence.
- Confirmed this is distinct from the provider-attempt retry ledger fix: the ledger records durable retry diagnostics, while the spec also requires a searchable `provider.retry` event.
- Confirmed the fix keeps non-retry provider callback events on the existing best-effort timeline path.

### Review 75

- Confirmed FCA-20260526-082 against `spec/13-live-input-and-steering.md`, the Web running-session submit workflow in `spec/17-web-console.md`, `Runner.Steer`, and focused blocked-`events.jsonl` evidence.
- Confirmed the source fact remains `control/steer.jsonl`, but a failed API response must not leave that control request live after required queued timeline evidence failed to persist.
- Confirmed the fix is limited to initial steer submission events and does not harden accepted/deferred/interrupted events without separate evidence.

### Review 76

- Confirmed FCA-20260526-083 against Plan Mode durable fact requirements in `spec/01-runtime-architecture.md` and `spec/18-durable-contract-and-completion.md`, current `planmode.json` mutation ordering, and focused blocked-history evidence.
- Confirmed this is not a duplicate of FCA-20260526-060: that slice made history append failures visible, while this slice prevents the current Plan Mode snapshot and derived plan Markdown from remaining advanced after the visible failure.
- Confirmed rollback belongs in the session store helpers so CLI, Web, runtime, SDK, and future callers read consistent Plan Mode facts after a failed transition.

### Review 77

- Confirmed FCA-20260526-084 against Goal durable fact requirements in `spec/01-runtime-architecture.md` and `spec/18-durable-contract-and-completion.md`, current `goal.json` mutation ordering, and focused blocked-history evidence.
- Confirmed this is not a duplicate of FCA-20260526-061: that slice made history append failures visible, while this slice prevents current Goal snapshots from remaining advanced after the visible failure.
- Confirmed the fix belongs in Goal store helpers so CLI, Web, runtime, SDK, and future callers read consistent Goal facts after failed completion, accounting, mission approval, or progress transitions.

### Review 78

- Confirmed FCA-20260526-085 against Goal durable fact requirements, the runtime budget wrap-up turn-start path, and focused blocked-history evidence.
- Confirmed this is not a duplicate of FCA-20260526-068: that slice stopped before provider execution on history failure, while this slice prevents the runtime-owned `goal.json` turn-start marker from remaining advanced after the failure.
- Confirmed the fix is scoped to the manual runtime `SaveGoal` path; store-owned Goal transitions are covered by FCA-20260526-084.

### Review 79

- Confirmed FCA-20260526-086 against Goal durable fact requirements, CLI `mutateGoalStatus`, CLI `goal clear`, and focused blocked-history evidence.
- Confirmed this is not a duplicate of FCA-20260526-084: store-owned Goal transitions roll back internally, while these CLI adapter paths mutate current Goal facts and append history/events outside the store helpers.
- Confirmed the fix is scoped to CLI; Web goal controls require separate proof because they have separate handlers and response semantics.

### Review 80

- Confirmed FCA-20260526-087 against Goal durable fact requirements, Web `handleGoalStatus`, Web `handleGoalClear`, and focused blocked-history HTTP evidence.
- Confirmed this is not a duplicate of FCA-20260526-086: CLI and Web adapters have separate mutation wrappers and separate user-visible response paths.
- Confirmed the fix is scoped to Web status/clear; Web mission plan and validation patch paths still need independent proof because they can create linked plan-mode side facts and tasks.

### Review 81

- Confirmed FCA-20260526-088 against Goal durable fact requirements, Web `handleGoalPatch`, and a focused blocked-history HTTP regression for a simple success-criteria patch.
- Confirmed this is not a duplicate of FCA-20260526-087: status/clear handlers and the generic patch handler have separate mutation flows and response payloads.
- Confirmed the fix is intentionally scoped to patches with no created tasks and no newly-created linked Plan Mode, because those side-fact paths need a broader rollback design.

### Review 82

- Confirmed FCA-20260526-089 against Goal durable fact requirements, Web `handleMissionValidationPatch`, and a focused blocked-history HTTP regression for a validation-plan-only patch.
- Confirmed this is not a duplicate of FCA-20260526-088: the mission validation endpoint has separate JSON shape, event type, and plan-mode creation condition.
- Confirmed the fix is intentionally scoped to validation mutations with no newly-created linked Plan Mode; validation-contract gate-reset paths still need separate side-fact proof.

### Review 83

- Confirmed FCA-20260526-090 against Goal durable fact requirements, Web `handleMissionPlanPatch`, and a focused blocked-history HTTP regression for a feature-only mission-plan patch.
- Confirmed this is not a duplicate of FCA-20260526-089: the mission plan endpoint has separate JSON shape, event type, task sync, and plan-mode creation behavior.
- Confirmed the fix is intentionally scoped to mission-plan mutations with no created tasks and no newly-created linked Plan Mode; task and plan-mode side-fact paths still need separate proof.

### Review 84

- Confirmed FCA-20260526-091 against durable Goal and Task facts, Web `handleMissionPlanPatch`, `SyncMissionPlanTasks`, and a focused blocked-history HTTP regression for task sync.
- Confirmed this extends but does not duplicate FCA-20260526-090: the simple mission-plan rollback did not cover generated task files.
- Confirmed `SaveTasks` needed exact-set semantics for rollback because rewriting only the supplied tasks would leave stale `tasks/task_*.json` files behind.

### Review 85

- Confirmed FCA-20260526-092 against durable Goal and Plan Mode facts, Web `handleMissionPlanPatch`, `EnsurePlanModeForGoal`, and a focused blocked-history HTTP regression for an approval-gated mission-plan patch.
- Confirmed this extends but does not duplicate FCA-20260526-091: task sync rollback did not cover Plan Mode side facts.
- Confirmed the store-level Plan Mode snapshot helpers reuse the existing private rollback machinery, including plan Markdown restoration/removal.

### Review 86

- Confirmed FCA-20260526-093 against durable Goal, Task, and Plan Mode facts, Web `handleGoalPatch`, and a focused blocked-history HTTP regression for a generic mission goal patch.
- Confirmed this extends but does not duplicate FCA-20260526-092: the generic Goal patch endpoint has separate request shape, event type, and rollback branch.
- Confirmed the fix reuses the existing exact task-set restore and Plan Mode snapshot restore rather than adding endpoint-specific filesystem operations.

### Review 87

- Confirmed FCA-20260526-094 against durable Goal and Plan Mode facts, Web `handleMissionValidationPatch`, and a focused blocked-history HTTP regression for an approved mission validation-contract patch.
- Confirmed this extends but does not duplicate FCA-20260526-093: the mission validation endpoint has separate request shape, event type, and gate creation condition.
- Confirmed the fix reuses the existing Plan Mode snapshot restore and does not add validation-specific filesystem operations.

### Review 88

- Confirmed FCA-20260526-095 against durable Goal, Goal history, and Task facts, store `CreateGoal`, `syncMissionPlanTasks`, and a focused blocked-history regression during create-time mission task sync.
- Confirmed this is not a duplicate of FCA-20260526-084: that slice covered established Goal transition helpers, while `CreateGoal` had a separate create-time ordering and task generation path.
- Confirmed the fix belongs in the session store helper so Web, CLI, runtime start, SDK, and future callers share the same rollback behavior.

### Review 89

- Confirmed FCA-20260526-096 against Web `handleGoalCreate`, store `CreateGoal`, `EnsurePlanModeForGoal`, and a focused blocked-`events.jsonl` HTTP regression.
- Confirmed this is not a duplicate of FCA-20260526-095: the store helper now rolls back when `goal.created` history fails, while this Web adapter path failed after that history already succeeded and the matching Web event append failed.
- Confirmed rollback needs to include created task files, Goal history, and linked Plan Mode state because all three can be introduced before the failed event append.

### Review 90

- Confirmed FCA-20260526-097 against runtime `approveLinkedMissionPlan`, `ApproveMissionPlan`, and a focused blocked-`events.jsonl` regression.
- Confirmed this is not a rollback slice: mission approval history is already durable before the event append, so rolling back the current snapshot would create a history/snapshot contradiction.
- Confirmed the fix should stop the continue/approval path by returning the event append error, while preserving the already-approved history-backed mission snapshot.

### Review 91

- Confirmed FCA-20260526-098 against the Plan Mode event catalog in `spec/01-runtime-architecture.md`, runtime `Continue`, and recovered Plan Mode input answer/cancel helpers.
- Confirmed the approval bug is not only a missing timeline entry: with `events.jsonl` blocked, the pre-fix approval path continued into the provider turn after losing the approval event.
- Confirmed this slice should not promote all runtime `emit` calls to hard failures; the minimal boundary is Plan Mode control events where store/history/replay facts represent an operator action or execution gate transition.

### Review 92

- Confirmed FCA-20260526-099 against `spec/12-task-system.md`, store `LoadTodo`, and tool `todo_write`.
- Confirmed the finding is behavior-backed rather than schema-only: replacing `todo.json` with a directory made `todo_write` return a successful no-op with `null` output before the fix.
- Confirmed the minimal fix belongs in the tool's existing snapshot load path; the store already treats a missing `todo.json` as an empty snapshot, so the fix only surfaces real load failures.

### Review 93

- Confirmed FCA-20260526-100 against the linked Plan Mode gate requirements in `spec/01-runtime-architecture.md`, `spec/17-web-console.md`, and `spec/18-durable-contract-and-completion.md`, plus Web goal create / patch handlers.
- Confirmed this is a source-fact consistency issue, not just a missing event: blocked `planmode-history.jsonl` made the API return failure while `goal.json`, goal history, and task facts still advanced without the required linked Plan Mode gate.
- Confirmed the minimal fix should restore the previous durable goal/task/Plan Mode snapshots on gate-creation failure instead of weakening `EnsurePlanModeForGoal` or bypassing Plan Mode approval.

### Review 94

- Confirmed FCA-20260526-101 against `spec/12-task-system.md`, `spec/17-web-console.md`, Web `handleGoalPatch`, Web `handleMissionPlanPatch`, and `SyncMissionPlanTasks`.
- Confirmed this is distinct from FCA-20260526-100: the failing durable fact is task synchronization after a goal patch, not linked Plan Mode gate creation.
- Confirmed the minimal fix belongs in the Web handlers because the store-level `PatchGoal` and `SyncMissionPlanTasks` operations are individually valid, while the Web endpoint composes them into one operator mutation that must roll back on partial failure.

### Review 95

- Confirmed FCA-20260526-102 against the durable Goal / Plan Mode fact requirements in `spec/01-runtime-architecture.md`, `spec/17-web-console.md`, and `spec/18-durable-contract-and-completion.md`.
- Confirmed this is distinct from runtime approval event reporting: these Web endpoints explicitly roll back the current goal snapshot on missing `events.jsonl`, so the matching Goal history and any Plan Mode link created by the same failed operator mutation must roll back too.
- Confirmed the minimal fix should not weaken history append errors or treat all diagnostic events as transactional; it only restores history when `appendGoalMutation` has already written history and then fails on the required session event append.

### Review 96

- Confirmed FCA-20260526-103 against recovered Plan Mode input replay requirements in `spec/01-runtime-architecture.md`, provider tool-result replay rules in `spec/03-provider-contracts.md`, and Plan Mode recovery requirements in `spec/18-durable-contract-and-completion.md`.
- Confirmed this is a replay-safety issue rather than a display-only history gap: the failure path clears the stored pending request before writing the `request_user_input` tool result required for provider replay.
- Confirmed the minimal fix belongs around the runtime recovered input composition; the store-level `AnswerPlanModeInput` transition is valid by itself, but runtime composes it with a required message append and must roll both durable Plan Mode facts back when the replay message write fails.

### Review 97

- Confirmed FCA-20260526-104 against Plan Mode approval replay requirements in `spec/01-runtime-architecture.md`, provider replay rules in `spec/03-provider-contracts.md`, Web approval behavior in `spec/17-web-console.md`, and `spec/18-durable-contract-and-completion.md`.
- Confirmed this is distinct from missing Plan Mode control events: the required events and history can be present, while the replayable `planmode_approval` user message is still missing after `messages.jsonl` append failure.
- Confirmed the minimal fix should make approval retry idempotent for already-approved/executing Plan Mode states instead of rolling back established history-backed approval facts or treating all runtime events as transactional.

### Review 98

- Confirmed FCA-20260526-105 against Web Ask-for-Changes semantics in `spec/17-web-console.md`, Plan Mode durable recovery requirements in `spec/18-durable-contract-and-completion.md`, and runtime `Continue` revision handling.
- Confirmed this is distinct from approval retry: the failed durable fact is the revision user-message metadata, and the partially advanced Plan Mode state is `planning`, not `executing`.
- Confirmed the minimal fix should require matching `planmode.plan_revised` history and an absent replay message before treating a planning-state retry as a recovered revision, so normal planning continuations remain ordinary user messages.

### Review 99

- Confirmed FCA-20260526-106 against Plan Mode durable fact requirements in `spec/18-durable-contract-and-completion.md` and runtime cancellation ordering in `internal/runtime/runner.go`.
- Confirmed this is separate from pending input cancellation replay: `appendPlanInputCancelToolResult` already de-duplicates the `request_user_input` tool result, while the duplicated fact is the later `planmode.cancelled` history row.
- Confirmed the minimal fix should be runtime-level idempotent cancellation recovery for already-cancelled Plan Mode state, not store-level weakening of `CancelPlanMode` history semantics.

### Review 100

- Confirmed FCA-20260526-107 against provider tool-result replay requirements in `spec/03-provider-contracts.md`, Plan Mode durable recovery requirements in `spec/18-durable-contract-and-completion.md`, and runtime recovered input cancellation ordering.
- Confirmed this is distinct from FCA-20260526-106: the whole Plan Mode cancellation can be idempotent while recovered pending input cancellation still returns early after only the tool result exists.
- Confirmed the minimal fix should not roll back the already-written replay tool result; it should treat message, history, and event as independently recoverable required facts.

### Review 101

- Confirmed FCA-20260526-108 against Plan Mode input recovery requirements in `spec/18-durable-contract-and-completion.md` and runtime recovered input answer ordering.
- Confirmed this is the symmetric answer-side event gap after FCA-20260526-107: answer history and replay can exist, while only the runtime event is missing.
- Confirmed the minimal fix must verify the same request id, answer payload, original tool call id, and existing tool result before treating a no-pending-request retry as recovered.

### Review 102

- Confirmed FCA-20260526-109 against Plan Mode durable gate requirements in `spec/17-web-console.md`, `spec/18-durable-contract-and-completion.md`, and continue-time Plan Mode creation in `internal/runtime/runner.go`.
- Confirmed this is a gate identity issue rather than a provider replay issue: retry can replace `planmode.json` before any provider turn is needed.
- Confirmed the minimal fix should only reuse a current planning Plan Mode whose objective/source match the requested draft, preserving normal creation semantics for different drafts.

### Review 103

- Confirmed FCA-20260526-110 against linked mission approval requirements in `spec/01-runtime-architecture.md`, `spec/18-durable-contract-and-completion.md`, and runtime `approveLinkedMissionPlan` ordering.
- Confirmed this is separate from Plan Mode approval replay: Plan Mode approval/execution can be idempotent while linked Goal/Mission history still duplicates after an event append failure.
- Confirmed the minimal fix should require matching goal id, Plan Mode id, and approved version before treating a mission approval retry as recovered.

### Review 104

- Confirmed FCA-20260526-111 against Web Goal control requirements in `spec/17-web-console.md`, durable Goal/Event fact requirements in `spec/01-runtime-architecture.md`, and mission approval evidence requirements in `spec/18-durable-contract-and-completion.md`.
- Confirmed this is distinct from FCA-20260526-110: the runtime linked Plan Mode approval path is now idempotent, while the Web direct mission approval path could still leave an approved snapshot after a failed event append.
- Confirmed the minimal fix should keep Web mutation semantics transactional instead of adding a second Web-owned approval state or moving approval logic into the frontend.

### Review 105

- Confirmed FCA-20260526-112 against Web local file fact requirements in `spec/17-web-console.md` and durable Goal fact requirements in `spec/01-runtime-architecture.md`.
- Confirmed this is a status classification issue rather than another rollback issue: no approval mutation occurs when Goal history cannot be loaded, but the API incorrectly tells the operator the request is bad.
- Confirmed the minimal fix should only classify the helper's pre-mutation store loads as server failures while leaving approval validation errors on the existing client-error path.

### Review 106

- Confirmed FCA-20260526-113 against durable steer requirements in `spec/13-live-input-and-steering.md`, Web local file fact requirements in `spec/17-web-console.md`, and runtime `Steer` write ordering.
- Confirmed this is distinct from earlier steer event rollback work: the runtime already rejects the queued request on required event failure, while the Web adapter still misclassified durable write failures as client errors.
- Confirmed the minimal fix should stay in the Web adapter's HTTP status mapping and not move store-specific logic into the frontend or provider layer.

### Review 107

- Confirmed FCA-20260526-114 against Web error-class requirements in `spec/17-web-console.md`, queue durability requirements in `spec/01-runtime-architecture.md`, and runtime `QueueSubmit` write ordering in `internal/runtime/delegation.go`.
- Confirmed this is distinct from FCA-20260526-113: steer writes `control/steer.jsonl`, while queue submit writes global `_queue` job facts and optional parent coordination facts.
- Confirmed the minimal fix should stay in the Web service HTTP adapter. Runtime queue behavior, queue store layout, provider selection, and frontend queue UI do not need to change for this slice.

### Review 108

- Confirmed FCA-20260526-115 against parent coordination requirements in `spec/18-durable-contract-and-completion.md`, queue durability requirements in `spec/01-runtime-architecture.md`, and `QueueSubmit` ordering in `internal/runtime/delegation.go`.
- Confirmed this is not a Web status classification issue: even direct runtime queue submit could return an error after leaving a durable queued job available for workers.
- Confirmed the minimal fix should roll back only the just-created queue job on the parent-link failure path, without changing worker processing, queue status precedence, or parent coordination semantics.

### Review 109

- Confirmed FCA-20260526-116 against Web error-class requirements in `spec/17-web-console.md`, Goal durability requirements in `spec/01-runtime-architecture.md`, and `handleMissionValidationPatch` write ordering in `internal/webconsole/service.go`.
- Confirmed this is distinct from earlier Goal history/event rollback fixes: the failure occurs before history or event append, while persisting `goal.json` itself.
- Confirmed the minimal fix should stay in the Web service status mapping for this endpoint; session store `SaveGoal` and Goal validation semantics do not need to change for this slice.

### Review 110

- Confirmed FCA-20260526-117 against Web error-class requirements in `spec/17-web-console.md`, Goal fact-source requirements in `spec/01-runtime-architecture.md`, and both `PatchGoal` call sites in `handleGoalPatch` / `handleMissionPlanPatch`.
- Confirmed this is distinct from FCA-20260526-116: that route writes through `SaveGoal`, while these two routes write through `PatchGoal` and already had a reusable `goalStoreStatus` classifier.
- Confirmed the minimal fix should only change Web HTTP status mapping and focused regressions; Goal patch validation, mission approval reset semantics, Plan Mode creation, task sync, and rollback paths remain unchanged.

### Review 111

- Confirmed FCA-20260526-118 against Settings sensitivity requirements in `spec/17-web-console.md` and durable local file fact consistency from `spec/01-runtime-architecture.md`.
- Confirmed the issue is not covered by the existing config-write-before-API-key test: that test blocks config persistence before env write, while this failure occurs after config persistence and before env/process key mutation.
- Confirmed the minimal fix should be a local Web settings rollback helper, not a change to config package persistence semantics or audit-log ordering.

### Review 112

- Confirmed FCA-20260526-119 against Settings API-key sensitivity requirements in `spec/17-web-console.md`, provider API-key configuration behavior in `spec/03-provider-contracts.md`, and durable local fact consistency from `spec/01-runtime-architecture.md`.
- Confirmed this is distinct from FCA-20260526-118: that issue covered env-file target failure after config persistence, while this one covers a malformed provider env key that reaches `.env` persistence and only fails at `os.Setenv`.
- Confirmed the minimal fix should share the existing config env-file allowlist with Web preflight and `UpsertEnvFile`, without changing provider adapters, Settings UI payload shape, or audit event contents.

### Review 113

- Confirmed FCA-20260526-120 against Settings sensitivity requirements in `spec/17-web-console.md` and durable local file consistency requirements in `spec/01-runtime-architecture.md`.
- Confirmed this is distinct from FCA-20260526-118 and FCA-20260526-119: those cover env write failure and malformed env names, while this path is reported as a successful save but corrupts the target layout by using one file for both config and API-key env data.
- Confirmed the minimal fix should remain a Web Settings target preflight; config persistence, env-file formatting, Settings frontend payloads, and audit event shape do not need to change for this slice.

### Review 114

- Confirmed FCA-20260526-121 against Settings auditability requirements in `spec/17-web-console.md` and file-fact consistency requirements in `spec/01-runtime-architecture.md`.
- Confirmed this is distinct from FCA-20260526-120: that path aliases config and API-key env file, while this one aliases config and the Web audit JSONL file and can occur even without an API-key update.
- Confirmed the minimal fix should stay in Web Settings preflight. The audit log writer, config writer, Settings frontend, and audit event schema do not need to change for this slice.

### Review 115

- Confirmed FCA-20260526-122 against Settings API-key sensitivity requirements in `spec/17-web-console.md` and provider API-key configuration behavior in `spec/03-provider-contracts.md`.
- Confirmed this is distinct from FCA-20260526-119: that issue validates the env key name, while this issue validates the env value before the same late `os.Setenv` failure point.
- Confirmed the minimal fix should stay in Web Settings API-key preflight; config/env-file formatting, Settings UI behavior, and provider adapters do not need to change for this slice.

### Review 116

- Confirmed FCA-20260526-123 against API-key audit sensitivity requirements in `spec/17-web-console.md` and local file-fact consistency requirements in `spec/01-runtime-architecture.md`.
- Confirmed this is distinct from FCA-20260526-120 and FCA-20260526-121: those cover config/env and config/audit aliases, while this path aliases the API-key env file with the audit log and can leak a secret into the audit file despite sanitized audit event payloads.
- Confirmed the minimal fix should stay in Web Settings target preflight. The audit writer, env-file writer, Settings UI payload, and audit event schema do not need to change for this slice.

### Review 117

- Confirmed FCA-20260526-124 against Settings API-key sensitivity requirements in `spec/17-web-console.md` and provider credential resolution in `internal/config/config.go`.
- Confirmed this is distinct from FCA-20260526-122: NUL-containing values fail at `os.Setenv`, while whitespace-only values were accepted as a successful save but are later trimmed to an unusable credential.
- Confirmed the minimal fix should stay in Web Settings API-key preflight. The env-file formatter, Settings UI confirmation, provider adapters, and credential loader do not need to change for this slice.

### Review 118

- Confirmed FCA-20260526-125 against Settings API-key operator-state requirements in `spec/17-web-console.md`.
- Confirmed this is distinct from FCA-20260526-124: that slice blocks backend persistence of blank key values, while this one fixes the frontend state after a successful save that intentionally submits no key.
- Confirmed the minimal fix should stay in the Settings renderer post-save state update. Backend config persistence, API-key preflight, env-file formatting, and provider probes do not need to change for this slice.

### Review 119

- Confirmed FCA-20260526-126 against the Settings backend API-key unchanged semantics in `internal/webconsole/service.go` and operator-state requirements in `spec/17-web-console.md`.
- Confirmed this is distinct from FCA-20260526-125: that path had no existing key and should stay blank, while this path starts with an existing key and an empty payload means unchanged.
- Confirmed the minimal fix should stay in the Settings renderer post-save state update. Backend clear-key semantics are not currently exposed, so the backend should not reinterpret empty `api_key` as deletion in this slice.

### Review 120

- Confirmed FCA-20260526-127 against the WebConsole session-file authority requirements in `spec/01-runtime-architecture.md` and the session-scoped API contract in `spec/17-web-console.md`.
- Confirmed this is not a general queue/children empty-state issue: existing sessions with no children, tasks, messages, or goal may still return empty data, but unknown session IDs must fail before subresource dispatch.
- Confirmed the minimal fix should stay at the Web session route boundary. Store helpers should continue to treat optional per-session files like `goal.json`, `todo.json`, `tasks/`, and message/event logs as optional once the enclosing session metadata fact exists.

### Review 121

- Confirmed FCA-20260526-128 against the Web API error-classification requirements in `spec/17-web-console.md` and the queue job ID validation in `internal/session/store.go`.
- Confirmed this is not a missing-job or queue-reconciliation issue: a syntactically valid missing job should remain HTTP 404, while store/reconciliation failures should remain HTTP 500.
- Confirmed the minimal fix belongs in the Web HTTP adapter. The store should continue returning validation errors, and the queue store layout, worker behavior, and advanced queue API contract do not need to change for this slice.

### Review 122

- Confirmed FCA-20260526-129 against the durable queue/session fact requirements in `spec/01-runtime-architecture.md` and the Web session-detail authority boundary in `spec/17-web-console.md`.
- Confirmed this is distinct from the queue-detail status-classification fix: the bug is not the HTTP status for malformed job IDs, but a swallowed reconciliation write failure when a session detail view opens a linked queue child.
- Confirmed the minimal fix should stay in `sessionDetail`: the store already returns reconciliation errors, and the Web view should report them instead of rendering stale state.

### Review 123

- Confirmed FCA-20260526-130 against the provider-attempt ledger requirements in `spec/01-runtime-architecture.md` and `spec/18-durable-contract-and-completion.md`, plus the Web Summary requirement in `spec/17-web-console.md`.
- Confirmed this is not a missing optional-file issue: `LoadProviderAttempts` and `LoadArtifactTracker` already return empty lists for absent files, so propagating errors only affects corrupt or unreadable fact files.
- Confirmed the minimal fix belongs in Web `sessionDetail` because the store already exposes the correct absent-versus-corrupt distinction; the Web adapter should not collapse that distinction into an empty display.

### Review 124

- Confirmed FCA-20260526-131 against the durable Goal fact requirements in `spec/01-runtime-architecture.md` and `spec/11-spec-audit-and-traceability.md`, plus the Goal facts requirement in `spec/17-web-console.md`.
- Confirmed this is not a missing optional-file issue: `LoadGoalHistory` already returns an empty slice for absent `artifacts/goal-history.jsonl`, so propagating errors only affects corrupt or unreadable ledgers.
- Confirmed the minimal fix belongs in the Web detail path. The store already exposes the correct absent-versus-corrupt distinction, and Web should not render a clean Goal facts panel when the history ledger is unreadable.

### Review 125

- Confirmed FCA-20260526-132 against the session detail response contract in `spec/17-web-console.md` and the durable fact boundaries in `spec/01-runtime-architecture.md`, `spec/11-spec-audit-and-traceability.md`, and `spec/18-durable-contract-and-completion.md`.
- Confirmed this is a snapshot-load issue, not an optional-file absence issue: the store returns `fs.ErrNotExist` for absent snapshots, and the Web adapter should ignore only that case.
- Confirmed the minimal fix belongs in `sessionDetail`; it preserves Web's read-only adapter role and does not add a second authority for contract, checkpoint, parent coordination, Goal, or Plan Mode state.

### Review 126

- Confirmed FCA-20260526-133 against the Plan Mode event list in `spec/01-runtime-architecture.md` and the Web Plan Mode fact-source boundary in `spec/17-web-console.md`.
- Confirmed this is distinct from earlier linked Plan Mode creation rollback fixes: `EnsurePlanModeForGoal` can succeed and append Plan Mode history, while the Web-only `planmode.created` event append fails afterward.
- Confirmed the minimal fix should stay in Web handlers because the store owns Plan Mode snapshot/history writes, while the Web service owns session events for Web control actions and already has rollback paths around later event failures.

### Review 127

- Confirmed FCA-20260526-134 against the durable Goal / Plan Mode fact-source requirements in `spec/01-runtime-architecture.md`, `spec/17-web-console.md`, and `spec/18-durable-contract-and-completion.md`.
- Confirmed this is distinct from FCA-20260526-132: that fix covered session detail snapshot loading, while this issue covers summary/list enrichment through `SessionSummary`.
- Confirmed the minimal fix belongs in the shared store list path so Web list/history, CLI session listing, children listing, and SDK callers observe the same absent-versus-corrupt distinction without creating a Web-specific authority.

### Review 128

- Confirmed FCA-20260526-135 against the persistent task graph requirements in `spec/12-task-system.md`, runtime context-loading requirements in `spec/01-runtime-architecture.md`, and Web task-board requirements in `spec/17-web-console.md`.
- Confirmed this is not an optional missing-directory issue: sessions without `tasks/` may still have an empty durable task graph, but a present malformed `tasks/task_*.json` file is a corrupt fact that must be surfaced.
- Confirmed the minimal fix belongs in the shared store task reader because the same hidden-error pattern affects Web detail, CLI/SDK `tasks`, runtime prompt context, compaction/checkpoint inputs, and mutating task operations.

### Review 129

- Confirmed FCA-20260526-136 against the Plan Mode fact-source and approval/revision semantics in `spec/00-product.md`, `spec/01-runtime-architecture.md`, and `spec/18-durable-contract-and-completion.md`.
- Confirmed this is not a provider-execution bypass: the focused pre-fix regression showed the provider was not reached, while the bug is the late, filename-less corrupt snapshot failure in the `continue` recovery path.
- Confirmed the minimal fix belongs in `Runner.Continue` because that is where a normal continuation message is classified as a Plan Mode revision; the engine should not be the first code path to discover an unreadable pending gate after continuation preparation has already advanced.

### Review 130

- Confirmed FCA-20260526-137 against the centralized Plan Mode gate requirements in `spec/01-runtime-architecture.md`, `spec/11-spec-audit-and-traceability.md`, and `spec/18-durable-contract-and-completion.md`.
- Confirmed this is a distinct gate fail-open issue from FCA-20260526-136: the prior fix covers `continue` revision preparation, while this fix covers `CompletionController` tool-call decisions after provider output.
- Confirmed the minimal fix should stay in `CompletionController.planModeGate`; provider schema filtering still fails earlier when the engine can read Plan Mode state, but the controller must independently fail closed when its own durable gate read fails.

### Review 131

- Confirmed FCA-20260526-138 against explicit contract and required-artifact durability requirements in `spec/18-durable-contract-and-completion.md` and the session fact-source boundary in `spec/01-runtime-architecture.md`.
- Confirmed this is not a missing optional contract issue: sessions without explicit contracts may still lack `contract.json`, but an existing malformed contract snapshot is a corrupt durable fact.
- Confirmed the minimal fix belongs in `CompletionController` because it owns required-artifact write tracking and finish-gate refresh; the store already exposes the missing-versus-corrupt distinction.

### Review 132

- Confirmed FCA-20260526-139 against parent coordination and background result-acceptance requirements in `spec/18-durable-contract-and-completion.md`, plus the session file fact-source boundary in `spec/01-runtime-architecture.md`.
- Confirmed this is not an empty parent/no-child case: `LoadBackgroundNotifications` already returns an empty slice for a missing log, and absent `parent-coordination.json` remains optional; only existing malformed wait-state files should block.
- Confirmed the minimal fix belongs in `CompletionController.parentCoordinationGate` because it is the centralized finish gate responsible for preventing parent completion while durable child/queue facts are unresolved or unreadable.

### Review 133

- Confirmed FCA-20260526-140 against the active Goal completion audit requirement in `spec/18-durable-contract-and-completion.md`, the current Goal fact-source boundary in `spec/01-runtime-architecture.md`, and the snapshot evidence requirement in `spec/11-spec-audit-and-traceability.md`.
- Confirmed this is not a no-goal session issue: absent `goal.json` remains optional, but an existing malformed Goal snapshot must fail closed because it can represent an active, paused, budget-limited, or completed objective.
- Confirmed the minimal fix belongs in `CompletionController.goalCompletionGate` because that is the unified finish gate responsible for blocking completion until Goal state is readable and valid.

### Review 134

- Confirmed FCA-20260526-141 against steer Goal-update requirements in `spec/13-live-input-and-steering.md` and current Goal fact-source requirements in `spec/18-durable-contract-and-completion.md`.
- Confirmed this is not provider execution bypass: the pre-fix regression already stopped before provider execution, but it skipped the Goal-history decision point and reported only a raw JSON parse error.
- Confirmed the minimal fix belongs in `appendGoalHistoryForSteer` because that helper is where accepted steer decides whether a current Goal requires `goal.updated` history.

### Review 135

- Confirmed FCA-20260526-142 against linked Goal/Plan Mode requirements in `spec/01-runtime-architecture.md` and `spec/18-durable-contract-and-completion.md`.
- Confirmed this is not a no-goal session issue: missing `goal.json` remains optional, but a malformed existing Goal snapshot must not be treated as no Goal when creating a gate that might need `linked_goal_id`.
- Confirmed the minimal fix belongs in `Store.CreatePlanMode` because the store owns Plan Mode snapshot creation and linked Goal discovery for CLI/Web/runtime callers.

### Review 136

- Confirmed FCA-20260526-143 against the pre-completion feature gate named in `spec/18-durable-contract-and-completion.md` and the durable feature-list tool contract in `spec/04-tools-and-skills.md`.
- Confirmed this is not an optional no-feature-list issue: absent `feature_list.json` remains allowed, but malformed existing feature-list state must not be treated as if no feature list existed.
- Confirmed the minimal fix belongs in `CompletionController.EvaluatePreCompletionFeatures` because it is the finish-gate path that turns durable feature-list facts into an allow/block decision.

### Review 137

- Confirmed FCA-20260526-144 against Plan Mode transition durability in `spec/01-runtime-architecture.md` and the Plan Mode fact-source traceability requirements in `spec/11-spec-audit-and-traceability.md`.
- Confirmed this is not a no-history ordinary continuation issue: readable history without a matching revision remains an ordinary planning continuation, but unreadable existing history must stop recovery because it may contain the durable revision fact needed to reconstruct the missing replay message.
- Confirmed the minimal fix belongs in `Runner.ensurePlanModeRevisedForMessage` / `hasMatchingPlanModeRevisionHistory` because that is the continuation recovery path that decides whether the repeated message is a missing `planmode_revision` replay fact or ordinary user input.

### Review 138

- Confirmed FCA-20260526-145 against linked Goal/Plan Mode approval requirements in `spec/01-runtime-architecture.md` and mission approval durability requirements in `spec/18-durable-contract-and-completion.md`.
- Confirmed this is not a no-history fresh approval issue: a readable history without matching approval may still proceed through `ApproveMissionPlan`, but an unreadable existing Goal history ledger must stop the retry path because it may contain the already-written mission approval fact.
- Confirmed the minimal fix belongs in `approveLinkedMissionPlan` / `hasMissionPlanApprovedHistory` because that is the recovery path that decides whether to append only the missing session event or perform another mission approval write.

### Review 139

- Confirmed FCA-20260526-146 against `state.json` durability in `spec/01-runtime-architecture.md` and Web/CLI session observability in `spec/09-phase-plan.md`.
- Confirmed this is not a stray-directory issue: unreadable `session.json` is still skipped, but once metadata loads successfully the directory is a real session and an unreadable `state.json` must be surfaced.
- Confirmed the minimal fix belongs in `Store.listAllSessions` and `Store.ListChildren` because those are the shared session-summary paths used by root lists, paged lists, and child-session views.

### Review 140

- Confirmed FCA-20260526-147 against queue reconciliation's file-fact-only convergence requirement in `spec/01-runtime-architecture.md` and Web-visible queue failure semantics in `spec/17-web-console.md`.
- Confirmed this is not a legitimate orphan repair case: `session.json` had already identified a linked session for the queue job, so unreadable `state.json` or `messages.jsonl` must stop reconciliation instead of being treated as no linked session.
- Confirmed the minimal fix belongs in `findSessionForQueueJob` / `reconcileQueueJobSession` because that helper is the single point deciding whether a queue job has a linked session fact set available for reconciliation.

### Review 141

- Confirmed FCA-20260526-148 against queue job durability and lease/claim requirements in `spec/01-runtime-architecture.md`, plus Web queue visibility requirements in `spec/17-web-console.md`.
- Confirmed this is not the same as invalid scratch files in queue directories: invalid filenames and mismatched valid JSON are still skipped for diagnostics, but a valid `<job_id>.json` queue fact that cannot be read must be reported.
- Confirmed the minimal fix belongs in `listQueueJobCopies` and `ClaimNextQueuedJob` because those are the shared Web/CLI list path and worker claim path that were turning unreadable queue facts into no visible work.

### Review 142

- Confirmed FCA-20260526-149 against parent notification and queue lifecycle durability requirements in `spec/01-runtime-architecture.md`, plus Web Background inspector / timeline visibility requirements in `spec/17-web-console.md`.
- Confirmed this is not a cosmetic event-only issue: `control/background.jsonl` is a parent completion-gate input and Web Background inspector source, while `queue.job.completed` / `queue.job.failed` timeline events are required operator facts.
- Confirmed the minimal fix belongs in `ensureTerminalQueueJobParentState` and its helper writes because that is the path that turns terminal queue jobs into parent-visible durable facts.

### Review 143

- Confirmed FCA-20260526-150 against queue worker result-delivery requirements in `spec/01-runtime-architecture.md` and Web Background/timeline observability requirements in `spec/17-web-console.md`.
- Confirmed this is not the same as a normal failed child job: a linked child session had already persisted `StatusCompleted`, and the remaining error was a parent queue fact append failure, so the queue job should preserve the child terminal result and report infrastructure persistence failure to the worker.
- Confirmed the minimal fix belongs across `Engine.reconcileLinkedQueueJob` result/error propagation and `Runner.ProcessNextJob` because the engine is the source of the linked queue reconciliation error, while the worker decides whether `runErr` is normal child failure or queue persistence failure.

### Review 144

- Confirmed FCA-20260526-151 against the durable feature-list tool contract in `spec/04-tools-and-skills.md` and the pre-completion feature-list fact boundary already established by FCA-20260526-143.
- Confirmed this is not a no-feature-list compatibility issue: absent `feature_list.json` remains optional, but a malformed existing feature-list file is a corrupt session fact and must not be summarized as `feature_list: null`.
- Confirmed the minimal fix belongs in `compactor.BuildWithProfile` because that is where the durable compaction summary is built and where feature-list load failures were being collapsed into absence.

### Review 145

- Confirmed FCA-20260526-152 against `SessionSummaryWriter` requirements in `spec/01-runtime-architecture.md` and the derived-view boundary in `spec/18-durable-contract-and-completion.md`.
- Confirmed this is not an authority/gate change: missing optional files still render as `not recorded`, while malformed existing recovery facts should be visible in `session.md` as diagnostics.
- Confirmed the minimal fix belongs in `writeSessionSummary` because Web detail and completion gates already report corrupt source facts; this slice only prevents the derived Markdown summary from misrepresenting corrupt facts as absent.

### Review 146

- Confirmed FCA-20260526-153 against the durable contract and long-run checkpoint boundaries in `spec/18-durable-contract-and-completion.md`: checkpoint resume notes are derived hints, while `contract.json` remains the current source fact for completion and recovery constraints.
- Confirmed this is not a no-contract compatibility issue: a genuinely missing current `contract.json` can still be summarized as checkpoint drift, but a malformed existing contract file must be surfaced as corrupt state.
- Confirmed the minimal fix belongs in `checkpointDriftWarnings`, because that helper is the point where the current contract fact is compared to the checkpoint snapshot before `appendCheckpointResumeHint` writes a new harness reminder.

### Review 147

- Confirmed FCA-20260526-154 against the persistent task graph requirements in `spec/12-task-system.md` and the checkpoint resume-index boundary in `spec/18-durable-contract-and-completion.md`.
- Confirmed this is not a missing optional taskboard issue: `ListTasks` still returns an empty graph when no durable task files exist, while a present malformed `tasks/task_*.json` is corrupt recovery state.
- Confirmed the minimal fix belongs in `writeLongRunCheckpoint`, because the shared store already reports corrupt task files and only the checkpoint writer was still discarding that error.

### Review 148

- Confirmed FCA-20260526-155 against `SessionSummaryWriter` requirements in `spec/01-runtime-architecture.md`, task-state source facts in `spec/12-task-system.md`, and the derived-view boundary in `spec/18-durable-contract-and-completion.md`.
- Confirmed this is not an authority/gate change: missing or empty todo/task state still renders as `not recorded`, while malformed present task-state facts should be visible in `session.md` as diagnostics.
- Confirmed the minimal fix belongs in `writeSessionSummary`; the store and model tool paths already report corrupt todo/task files, and only the Markdown summary was still collapsing those errors into absence.

### Review 149

- Confirmed FCA-20260526-156 against `SessionSummaryWriter` requirements in `spec/01-runtime-architecture.md`, provider-attempt ledger requirements in `spec/03-provider-contracts.md`, and the derived-view boundary in `spec/18-durable-contract-and-completion.md`.
- Confirmed this is not an authority/gate change: missing or empty artifact/provider-attempt facts still render as `not recorded`, while malformed present fact files should be visible in `session.md` as diagnostics.
- Confirmed the minimal fix belongs in `writeSessionSummary`; store loaders, Web detail, and completion/provider paths already distinguish absent versus corrupt fact files, and only the Markdown summary was still collapsing those errors into absence.

### Review 150

- Confirmed FCA-20260526-157 against `SessionSummaryWriter` requirements in `spec/01-runtime-architecture.md`, parent child/queue coordination requirements in `spec/18-durable-contract-and-completion.md`, and task/queue recovery hardening already completed in `FCA-20260526-146`, `FCA-20260526-148`, and `FCA-20260526-139`.
- Confirmed this is not an authority/gate change: missing child/queue/background state still renders as `not recorded`, while malformed present files should be visible in `session.md` as diagnostics.
- Confirmed the minimal fix belongs in `writeSessionSummary`; store loaders and completion gates already report corrupt child/queue/background facts, and only the Markdown summary was still collapsing those errors into absence.

### Review 151

- Confirmed FCA-20260526-158 against the `messages.jsonl` / `events.jsonl` fact-source requirements in `spec/01-runtime-architecture.md` and the derived-view boundary in `spec/18-durable-contract-and-completion.md`.
- Confirmed this is not an authority/gate change: valid empty message/event logs still render with no observed tool repetition or owner clue, while malformed present logs should be visible in `session.md` as diagnostics.
- Confirmed the minimal fix belongs in `writeSessionSummary`; store loaders already report corrupt logs, and only the Markdown summary was still collapsing those errors into ordinary absence.

### Review 152

- Confirmed FCA-20260526-159 against the persistent todo requirements in `spec/12-task-system.md` and the checkpoint resume-index boundary in `spec/18-durable-contract-and-completion.md`.
- Confirmed this is not a missing optional todo compatibility issue: `LoadTodo` still returns an empty list for absent or empty todo state, while malformed present `todo.json` is corrupt recovery state.
- Confirmed the minimal fix belongs in `writeLongRunCheckpoint`, because store and summary paths already report corrupt todo state and only the checkpoint writer was still discarding that error.

### Review 153

- Confirmed FCA-20260526-160 against the required-artifact tracker source requirements and checkpoint resume-index boundary in `spec/18-durable-contract-and-completion.md`.
- Confirmed this is not a missing optional tracker compatibility issue: `LoadArtifactTracker` still returns an empty list for absent tracker state, while malformed present `artifact-tracker.json` is corrupt recovery state.
- Confirmed the minimal fix belongs in `writeLongRunCheckpoint`, because completion gates and `session.md` already report corrupt artifact tracker state and only the checkpoint writer was still discarding that error.

### Review 154

- Confirmed FCA-20260526-161 against the child/queue recovery requirements in `spec/01-runtime-architecture.md` and the checkpoint resume-index boundary in `spec/18-durable-contract-and-completion.md`.
- Confirmed this is not a missing optional child/queue/background compatibility issue: valid sessions with no child sessions, no queue jobs, and no background notifications still produce empty facts, while malformed present child/queue/background files are corrupt recovery state.
- Confirmed the minimal fix belongs in `writeLongRunCheckpoint`, because store loaders, completion gates, and `session.md` already report corrupt child/queue/background state and only the checkpoint writer was still discarding those errors.

### Review 155

- Confirmed FCA-20260526-162 against the message/event fact-source requirements in `spec/01-runtime-architecture.md` and the checkpoint resume-index boundary in `spec/18-durable-contract-and-completion.md`.
- Confirmed this is not a valid-empty log compatibility issue: `Store.Create` initializes `messages.jsonl` and `events.jsonl`, so empty log files remain valid while malformed present log files are corrupt required session facts.
- Confirmed the minimal fix belongs in `writeLongRunCheckpoint`, because store loaders and `session.md` already report corrupt message/event logs and only the checkpoint writer was still discarding those errors.

### Review 156

- Confirmed FCA-20260526-163 against the optional recovery snapshot requirements in `spec/01-runtime-architecture.md` and `spec/18-durable-contract-and-completion.md`.
- Confirmed this is not a missing optional-file compatibility issue: absent contract, Goal, Plan Mode, or parent coordination snapshots remain valid no-state cases, while malformed present snapshots are corrupt recovery facts.
- Confirmed the minimal fix belongs in `writeLongRunCheckpoint`, because Web detail, completion/Plan/parent gates, checkpoint drift handling, and `session.md` already report these corrupt snapshots and only checkpoint writing still collapsed them into absence.

### Review 157

- Confirmed FCA-20260526-164 against the compaction/source-log boundary in `spec/01-runtime-architecture.md` and the Goal snapshot recovery requirements in `spec/18-durable-contract-and-completion.md`.
- Confirmed this is not a no-Goal compatibility issue: absent `goal.json` still means no current Goal, while malformed present `goal.json` is corrupt recovery state.
- Confirmed the minimal fix belongs in the compactor because `loadGoalOptional` already exposes the absent-versus-corrupt distinction, and only the compaction summary/reuse paths were discarding it.

### Review 158

- Confirmed FCA-20260526-165 against the paired snapshot/history fact-source requirements in `spec/01-runtime-architecture.md` and `spec/11-spec-audit-and-traceability.md`.
- Confirmed this is not a valid missing-snapshot compatibility issue: absent `goal.json` / `planmode.json` remains allowed for explicit history entries without an inferred ID, while malformed present snapshots must not be collapsed into no current ID.
- Confirmed the minimal fix belongs in the store append helpers because they are the shared linkage point for callers that omit `GoalID` or `PlanModeID`, and strengthening only that point avoids adding workflow-specific guards.

### Review 159

- Confirmed FCA-20260526-166 against the session metadata fact-source requirements in `spec/00-product.md` and `spec/01-runtime-architecture.md`.
- Confirmed this is a diagnostics hardening issue rather than an unsafe mutation: `requireSession` already blocks the Web route before the mission plan patch executes, but the returned error lacked the corrupt fact filename.
- Confirmed the minimal fix belongs in `Store.LoadMetadata`, because it is the shared loader for `session.json` and wrapping only read errors preserves existing missing-session and invalid-ID classification.

### Review 160

- Confirmed FCA-20260526-167 against the child handoff and queue notification requirements in `spec/01-runtime-architecture.md` and the large-project queue evidence boundary in `spec/09-phase-plan.md`.
- Confirmed this is not a legitimate no-output child case: the child session had a completed run and a present but malformed `messages.jsonl`, so empty visible paths would hide corrupt source facts rather than report an empty handoff.
- Confirmed the minimal fix belongs in `ProcessNextJob`, because that is the post-run materialization point that turns child facts into queue job fields and parent background notifications.

### Review 161

- Confirmed FCA-20260526-168 against the direct child handoff requirements in `spec/01-runtime-architecture.md` and the durable structured handoff guidance in `spec/11-spec-audit-and-traceability.md`.
- Confirmed this is not a child execution failure: the child completed and then a completion hook corrupted the durable `messages.jsonl` fact, so the parent-side handoff materialization is the failing boundary.
- Confirmed the minimal fix belongs in `SpawnAgent`, because synchronous `agent_spawn` / `Delegate` results are materialized there before parent coordination records the child as completed or failed.

### Review 162

- Confirmed FCA-20260526-169 against the goal snapshot fact-source requirements in `spec/01-runtime-architecture.md` and `spec/11-spec-audit-and-traceability.md`.
- Confirmed this is not only cosmetic text selection: `awaitingBudgetWrapUp` mutates `state.json` to `awaiting_input` / `goal_budget_limited`, so hiding a corrupt current goal snapshot can leave durable state inconsistent with unreadable goal facts.
- Confirmed the minimal fix belongs in `awaitingBudgetWrapUp`, because that is the state-transition boundary after budget wrap-up execution and before the session returns to the operator.

### Review 163

- Confirmed FCA-20260526-170 against the steer durable-control requirements in `spec/13-live-input-and-steering.md` and the session fact-source requirements in `spec/00-product.md` / `spec/01-runtime-architecture.md`.
- Confirmed this is not just a derived-summary refresh issue: `Steer` writes `control/steer.jsonl` and events before the ignored metadata reload, so unreadable `session.json` can leave accepted control input attached to an invalid session identity fact.
- Confirmed the minimal fix belongs at the start of `Steer`, because that is the control-plane admission boundary before the pending steer request is persisted.

### Review 164

- Confirmed FCA-20260526-171 against the live steer event requirements in `spec/13-live-input-and-steering.md` and the session fact-source boundary in `spec/01-runtime-architecture.md`.
- Confirmed this is not a generic telemetry issue: `session.steer.accepted` is the durable fact that a queued steer moved into model-visible execution, so provider continuation without that event weakens replay and recovery.
- Confirmed the minimal fix belongs in `drainSteer`, because that is the acceptance boundary that writes the user message, marks the request accepted, updates goal history, refreshes contract state, and then lets provider execution continue.

### Review 165

- Confirmed FCA-20260526-172 against the background queue fact requirements in `spec/01-runtime-architecture.md` and the large-project queue evidence boundary in `spec/09-phase-plan.md`.
- Confirmed this mirrors the steer acceptance durability issue but is independently relevant: `drainBackground` consumes pending background notifications, writes model-visible parent context, and then allows provider execution to continue.
- Confirmed the minimal fix belongs in `drainBackground`, because that is the acceptance boundary that moves background notifications into parent-visible execution context.

### Review 166

- Confirmed FCA-20260526-173 against the provider stop-reason mapping in `spec/03-provider-contracts.md` and the session event fact-source boundary in `spec/01-runtime-architecture.md`.
- Confirmed this is distinct from provider transport failures: the provider returned a successful response envelope with a failure stop reason, so the engine-owned stop-decision branch must persist the matching failed lifecycle event.
- Confirmed the minimal fix belongs in the provider stop-failure branch in `Engine.Run`, because that is where `state.json` is marked failed and returned as a resumable provider stop failure.

### Review 167

- Confirmed FCA-20260526-174 against the session lifecycle event catalog in `spec/01-runtime-architecture.md` and the Web-first local fact-source boundary in `spec/00-product.md`.
- Confirmed this is not diagnostic-only telemetry: `session.completed` is the terminal lifecycle event matching a durable `state.json` terminal transition and is relied on by timelines and recovery evidence.
- Confirmed the minimal fix belongs in `complete`, because that is the single helper that records successful `finish` completion and then reconciles linked queue facts.

### Review 168

- Confirmed FCA-20260526-175 against the session lifecycle event catalog in `spec/01-runtime-architecture.md` and the run/continue semantics in `spec/13-live-input-and-steering.md`.
- Confirmed this is not a completion-policy change: run mode still stops at `awaiting_input` for done candidates, but that resumable transition must have matching durable event evidence.
- Confirmed the minimal fix belongs in `awaitingInput`, because that helper owns the normal run-mode done-candidate state transition before linked queue reconciliation.

### Review 169

- Confirmed FCA-20260526-176 against the same session lifecycle event contract as FCA-20260526-175, plus the Goal budget and Plan Mode resumable-state paths in `spec/01-runtime-architecture.md`.
- Confirmed this is a related but separate slice: `awaitingBudgetWrapUp`, `awaitingPlanApproval`, and `awaitingPlanCancelled` are distinct helper paths with reason-specific state phases and operator recovery meaning.
- Confirmed the minimal fix belongs in those three helpers, not in generic event emission, so diagnostic `emit` calls remain best-effort.

### Review 170

- Confirmed FCA-20260526-177 against the remaining engine lifecycle state transitions in `spec/01-runtime-architecture.md`: autonomous no-finish failure and explicit pause.
- Confirmed this does not promote diagnostic event writes globally: it only hardens the two remaining session lifecycle events that must explain a durable `state.json` transition.
- Confirmed the minimal fix belongs in the `incomplete_no_finish` branch and `pause` helper, preserving existing tool-result replay and pause semantics.

### Review 171

- Confirmed FCA-20260526-178 against the session lifecycle event catalog in `spec/01-runtime-architecture.md` and the file-fact durability requirement in `spec/00-product.md`.
- Confirmed this is a runner-owned lifecycle boundary, not a provider or engine decision: the event is emitted after start setup and before `Engine.Run`.
- Confirmed the minimal fix belongs in `runExisting` and does not promote diagnostic-only events globally.

### Review 172

- Confirmed FCA-20260526-179 against the Goal event catalog and durable Goal accounting requirements in `spec/01-runtime-architecture.md`.
- Confirmed this is not cosmetic telemetry: `updateGoalAccounting` has already mutated `goal.json` and goal history, so missing runtime events leave timelines and recovery views behind the authoritative Goal facts.
- Confirmed the minimal fix belongs in `updateGoalAccounting`, and the generic `Engine.fail` wrapper should preserve the original accounting event context if the failure event append also hits the blocked `events.jsonl` path.

### Review 173

- Confirmed FCA-20260527-180 against the core `user.message` event catalog in `spec/01-runtime-architecture.md` and the durable user-message evidence requirements in `spec/13-live-input-and-steering.md`.
- Confirmed the clean rollback boundary only covers direct runner messages and engine harness reminders because no other durable control record is advanced in those helpers.
- Confirmed steer/background control-drain acceptance should remain a separate audit slice: those paths combine message append, control status mutation, accepted events, and optional Goal/background facts.

### Review 174

- Confirmed FCA-20260527-181 against the `checkpoint.resume_hint.injected` event catalog in `spec/01-runtime-architecture.md` and the long-run checkpoint recovery boundary in the same spec.
- Confirmed this is not generic telemetry: the event explains a provider-visible harness resume note added to `messages.jsonl` before continuing a recovered session.
- Confirmed the clean rollback boundary is the just-appended checkpoint note, so the minimal fix can reuse the existing tail-ID rollback helper without changing checkpoint generation or provider execution semantics.

### Review 175

- Confirmed FCA-20260527-182 against the `contract.created` / `contract.updated` event catalog in `spec/01-runtime-architecture.md` and the steer contract-sync requirement in `spec/13-live-input-and-steering.md`.
- Confirmed the retry hazard is real because `contractsEquivalent` can suppress a later refresh once `contract.json` has already advanced, so a missing core contract event is not guaranteed to self-heal.
- Confirmed `artifact.required` should remain best-effort in this slice: it is derived diagnostic visibility, while `contract.created` / `contract.updated` are the core durable fact events that must fail the refresh if unavailable.

### Review 176

- Confirmed FCA-20260527-183 against the `session.steer.deferred` requirement in `spec/13-live-input-and-steering.md`; this event is the durable explanation for interrupt steer fallback.
- Confirmed this is distinct from the accepted-steer fix: accepted steer already uses checked `user.message` and `session.steer.accepted` appends, while `deferPendingInterrupts` still used unchecked `e.emit`.
- Confirmed the minimal ordering is event-first, status-second. If `events.jsonl` is unavailable, leaving the steer pending is safer than silently committing a deferred control state without the required event.

### Review 177

- Confirmed FCA-20260527-184 against the `session.steer.interrupt_requested` event requirement in `spec/13-live-input-and-steering.md`; this is the durable evidence for best-effort interrupt preemption.
- Confirmed the existing `Runner.Steer` requested/queued event rollback does not cover the watcher-side in-memory interrupt signal because the watcher can observe an already queued request later.
- Confirmed event-first ordering is appropriate here: if the event cannot be persisted, the queued steer remains durable and can still be accepted at a safe boundary, but the runtime should not trigger an unrecorded interrupt cancellation.

### Review 178

- Confirmed FCA-20260527-185 against `spec/01-runtime-architecture.md` and `spec/12-task-system.md`, which frame `session.context.loaded` as durable evidence for the context facts loaded into the turn.
- Confirmed this is not generic telemetry: the event records todo/task counts, project-memory present/missing paths, role hints, Goal, and Plan Mode context immediately before provider prompt construction.
- Confirmed the minimal fix should fail before provider execution when that event cannot be written, preserving the fact-source boundary without changing context construction or prompt content.

### Review 179

- Confirmed FCA-20260527-186 against `spec/13-live-input-and-steering.md` and the Web-first file-fact model in `spec/01-runtime-architecture.md`: provider-visible live input must not survive as accepted context when the matching event evidence cannot be written.
- Confirmed this is not a generic retry nicety. Without rollback, a failed local event write leaves `messages.jsonl` ahead of steer/background control facts and can duplicate the same input after recovery.
- Confirmed the minimal fix should be local to `drainSteer` and `drainBackground`, using existing `RemoveLastMessageIfID` rollback and preserving the model-led runtime loop.

### Review 180

- Confirmed FCA-20260527-187 against `spec/12-task-system.md`: the task-system tool contract explicitly includes `todo.updated`, `task.created`, and `task.updated`, so these are required mutation facts rather than optional tool telemetry.
- Confirmed the owning runtime path still used best-effort `e.emit` through `ExecContext.Emit`, which ignored `events.jsonl` append failures after tool state mutations.
- Confirmed the minimal fix should stay tool-contract scoped: add a checked callback for required task-system events and roll back the affected todo/task snapshot on event failure, without changing other diagnostic tool events or imposing a workflow engine.

### Review 181

- Confirmed FCA-20260527-188 against `spec/01-runtime-architecture.md`: `goal.completed` is a catalogued session event for the model-driven goal completion transition, not optional display telemetry.
- Confirmed the issue is distinct from store-level `CompleteGoal` history rollback. `CompleteGoal` already rolls back when `goal-history.jsonl` append fails; this gap happens after that store transition succeeds and the matching session event append fails.
- Confirmed the minimal fix should stay inside the model tool path by snapshotting `goal.json` plus `goal-history.jsonl`, using `EmitRequired` for `goal.completed`, and restoring those facts on event failure. `goal.progress.recorded` remains best-effort because it is not in the current event catalog.

### Review 182

- Confirmed FCA-20260527-189 against `spec/01-runtime-architecture.md`: `planmode.plan_submitted` is a catalogued Plan Mode session event and marks the model-submitted approval gate.
- Confirmed the store helper already rolls back if `planmode-history.jsonl` append fails; the remaining gap is the composed model-tool path after `SubmitPlanMode` has succeeded but the matching session event append fails.
- Confirmed the minimal fix should use existing `SnapshotPlanMode`, `RestorePlanModeSnapshot`, and `RestorePlanModeHistory` helpers around the checked event call, without changing Plan Mode gate semantics or adding a workflow engine.

### Review 183

- Confirmed FCA-20260527-190 against `spec/01-runtime-architecture.md`: `planmode.input_requested` is a catalogued session event for the Plan Mode input request boundary.
- Confirmed this is narrower than FCA-20260526-073. That slice made pending request and awaiting-input state durable before responder invocation; this slice handles the later event append failure after those facts already succeeded.
- Confirmed the minimal fix should not roll back the pending request or awaiting-input state, because earlier recovery semantics intentionally preserve those facts. The tool should return an error before consuming interactive input so a later recovery path can still answer the pending request.

### Review 184

- Confirmed FCA-20260527-191 against `spec/01-runtime-architecture.md`: `goal.created` is a catalogued session event for durable Goal creation.
- Confirmed the issue is distinct from store-level goal history rollback and earlier Web goal-create rollback. This path is the model tool after `CreateGoal` has already succeeded and before any optional linked Plan Mode gate is considered.
- Confirmed the minimal fix should stay on the plain Goal creation boundary: use the checked tool event callback and restore `goal.json`, Goal history, and task snapshots if that event fails. Linked Plan Mode event rollback remains a separate, wider composition problem.

### Review 185

- Confirmed FCA-20260527-192 against `spec/01-runtime-architecture.md`: `planmode.input_answered` is a catalogued session event for the Plan Mode input answer boundary.
- Confirmed the issue is distinct from recovered Plan Mode answer replay in `internal/runtime/runner.go`. The recovered path already coordinates the stored pending request, replay tool result, history, and event repair; this slice covers the live model-tool path after the interactive responder returns.
- Confirmed the minimal fix should restore the pending Plan Mode request and Plan Mode history if the required `planmode.input_answered` event cannot be persisted, because no replay tool result has been appended yet by the engine.

### Review 186

- Confirmed FCA-20260527-193 against `spec/01-runtime-architecture.md`: `planmode.created` is a catalogued session event for durable Plan Mode creation.
- Confirmed the model-tool `create_goal(require_plan_approval=true)` path differs from plain Goal creation because it can create a linked Plan Mode gate after `goal.created` has already been persisted.
- Confirmed the minimal fix should mirror the WebConsole linked-gate rollback policy: if the linked Plan Mode's required event cannot be persisted, restore Plan Mode state/history plus the just-created Goal, Goal history, and task snapshot rather than leaving an invisible approval gate.

### Review 187

- Confirmed FCA-20260527-194 against `spec/01-runtime-architecture.md`: both `planmode.input_cancelled` and `planmode.cancelled` are catalogued Plan Mode session events.
- Confirmed the live `request_user_input` cancellation path is distinct from recovered cancellation in `internal/runtime/runner.go`: the recovered path already coordinates replay tool result repair and idempotent event recovery, while the live tool path has not yet appended its provider replay tool result.
- Confirmed the minimal fix should treat the two cancellation events as one required batch at the tool boundary. If the batch cannot be persisted, restore Plan Mode state/history and return a model-visible error instead of leaving a cancelled Plan Mode without matching session events.

### Review 188

- Confirmed FCA-20260527-195 against `spec/01-runtime-architecture.md`: `goal.created` and `planmode.created` are catalogued durable session events for start-time Goal and Plan Mode creation.
- Confirmed the issue is distinct from the model-tool and WebConsole creation paths. `Runner.Start` owns the initial session setup path and previously used unchecked `r.emit` after writing `goal.json`, Goal history, `planmode.json`, and Plan Mode history.
- Confirmed the minimal fix should stay in start initialization: require the start-time Goal/Plan Mode events before provider execution, append paired creation events atomically when both are created, and restore only the facts this path just created if the event append fails.

### Review 189

- Confirmed FCA-20260527-196 against `spec/01-runtime-architecture.md`: `planmode.linked_goal` is a catalogued durable session event for linking an existing pending Plan Mode gate to the current Goal.
- Confirmed the issue is distinct from FCA-20260527-193 and FCA-20260527-195. Those slices covered newly created linked Plan Mode gates; this slice covers `EnsurePlanModeForGoal` reusing an already pending but unlinked Plan Mode and appending Plan Mode history without matching event evidence.
- Confirmed the minimal fix must cover each adapter that can perform the relink outside the store: model tool `create_goal`, Web goal/mission/validation controls, and the CLI `goal plan approve` fallback. On event failure, restore Plan Mode snapshot/history and any surrounding Goal/task facts owned by the route.

### Review 190

- Confirmed FCA-20260527-197 against the Goal and mission event catalog in `spec/01-runtime-architecture.md`: `goal.paused`, `goal.cleared`, and `mission.plan.approved` are durable session events paired with Goal history facts.
- Confirmed the issue is distinct from FCA-20260526-086 and FCA-20260526-071. Those slices made CLI history/event failures visible and restored `goal.json`; this slice covers the remaining event-stage mismatch where the CLI restored the current Goal snapshot but left the just-appended Goal history fact behind.
- Confirmed the minimal fix belongs in the CLI adapter: snapshot Goal history before CLI-owned status, clear, and direct mission approval mutations, then restore it together with `goal.json` if the paired event append fails.

### Review 191

- Confirmed FCA-20260527-198 against the child and queue event catalog in `spec/01-runtime-architecture.md`: `session.child.queued` is the durable parent-session event for background child work, alongside worker lifecycle events such as `queue.job.claimed`, `queue.job.completed`, and `queue.job.failed`.
- Confirmed the issue is distinct from the prior queue lifecycle event hardening. `ProcessNextJob` already requires worker claim/notification/terminal lifecycle events, while `QueueSubmit` owns the submit-time parent-linked job creation boundary.
- Confirmed the minimal fix belongs in runtime delegation, not Web or CLI adapters: parent-linked `QueueSubmit` must persist the queued job, parent coordination, and `session.child.queued` as one success boundary; failed event append must roll back the just-created job and parent coordination instead of leaving a durable queue item with no parent timeline evidence.

### Review 192

- Confirmed FCA-20260527-199 against the same child event catalog in `spec/01-runtime-architecture.md`: `session.child.spawned` is the durable parent-session event for synchronous child sessions.
- Confirmed this is distinct from FCA-20260527-198. The previous slice covered background queue submission and `session.child.queued`; this slice covers the direct synchronous `Delegate` / `agent_spawn(background=false)` path after the child session has already run.
- Confirmed the minimal fix belongs in runtime delegation: after a child session ID exists, the parent timeline must durably record `session.child.spawned` before parent coordination advances. If the parent event cannot be written, return the event error while preserving the child result for inspection and without adding parent coordination.

### Review 193

- Confirmed FCA-20260527-200 against the tool lifecycle event catalog in `spec/01-runtime-architecture.md`: `tool.before` is a catalogued durable session event, and the runtime tool sequence records it before guard evaluation and tool execution.
- Confirmed this is distinct from `tool.after` handling. `tool.before` is a pre-side-effect boundary where the runtime can still stop cleanly; `tool.after` happens after execution and needs separate rollback/error semantics before it can be made required.
- Confirmed the minimal fix belongs in `Engine.Run`: require the `tool.before` event append before hooks, state updates, guard evaluation, or tool dispatch so blocked `events.jsonl` cannot allow side effects without pre-execution timeline evidence.

### Review 194

- Confirmed FCA-20260527-201 against the same tool lifecycle event catalog: `tool.interrupted` is the durable event for a cancelled tool call, and Phase 8 also requires a replayable interrupted tool result so recovery does not see a dangling provider tool call.
- Confirmed this is a different boundary from `tool.before`. The tool has already been interrupted, so the minimal fix must report the missing event without discarding the synthetic interrupted tool result needed for provider replay.
- Confirmed the existing `session.paused` event regression needed a later failure point after `tool.interrupted`; retargeting it through a successful `session.pause` hook preserves downstream pause-event coverage while adding direct interrupted-event coverage.

### Review 195

- Confirmed FCA-20260527-202 against the tool lifecycle event catalog in `spec/01-runtime-architecture.md`: `tool.after` is the durable post-execution event, and the tool loop must still preserve provider replay results when the event cannot be written.
- Confirmed this boundary is more complex than `tool.before`: the tool side effect already happened, so the minimal fix must append the current tool result plus synthetic results for remaining same-turn calls before returning the missing `tool.after` event error.
- Confirmed the fix should stop later tool execution in the same provider batch when the prior `tool.after` event is missing, preventing additional side effects from running after the durable timeline has already diverged.

### Review 196

- Confirmed FCA-20260527-203 against the WebConsoleService contract in `spec/01-runtime-architecture.md` and `spec/17-web-console.md`: WebConsole active handles remain in-memory only, but their owner/process clues must be written to `events.jsonl` as `webconsole.handle.acquired/released` events for session detail, `session.md`, checkpoint, and restart diagnostics.
- Confirmed `internal/webconsole/service.go` made both `addHandle` and `promotePendingStart` publish `webconsole.handle.acquired` best-effort after inserting the in-memory handle, so blocked `events.jsonl` could leave a successful current-process handle with no durable owner clue.
- Confirmed the minimal fix belongs in the Web service adapter: require `webconsole.handle.acquired` during handle acquisition before the handle becomes visible to local readers; keep release events best-effort because release is cleanup-oriented and must not strand active handles.

### Review 197

- Confirmed FCA-20260527-204 against the hook event catalog in `spec/01-runtime-architecture.md` and Phase 9 requirements in `spec/06-hooks.md`: `hook.triggered`, `hook.finished`, `hook.failed`, `hook.warning`, and `hook.command` are durable hook trace facts in `events.jsonl`, not best-effort process-local observations.
- Confirmed `internal/hooks/manager.go` used an emitter with no error return, and both runtime call sites wired it to best-effort `emit`, so hook command side effects could run while the corresponding hook trace event was missing.
- Confirmed the minimal fix belongs in the hook manager plus runtime adapters: make the hook emitter error-returning, require `hook.triggered` before hook command execution, require later hook trace events before continuing, and retarget downstream event tests now that hook event persistence is an earlier required boundary.

### Review 198

- Confirmed FCA-20260527-205 against `spec/13-live-input-and-steering.md`: accepted steer input for a session with a current Goal must write `goal.updated` history and emit Goal-related events so the target change is traceable, not only present in the provider prompt.
- Confirmed `Engine.drainSteer` already returned `goal.updated` history append failures, but still emitted the matching `goal.updated` session event with best-effort `emit`; a blocked `events.jsonl` after `session.steer.accepted` could leave the steer message and Goal history visible while the required Goal event was missing.
- Confirmed the minimal fix belongs in runtime steer acceptance: require `goal.updated` event persistence, restore the just-appended Goal history on event failure, and roll back the provider-visible steer message while keeping the steer request pending for retry.

### Review 199

- Confirmed FCA-20260527-206 against `spec/10-context-compaction.md`: every compaction must write `compact.started`, `compact.finished`, or `compact.reused` events with trigger, size, artifact, project-memory, task, and proof-budget context.
- Confirmed `Engine.Run` passed `compactor.BuildWithProfile` a callback that ignored `AppendEvent` errors, while `compactor.BuildWithProfile` accepted an errorless emitter; blocked `events.jsonl` could therefore let runtime continue to provider preparation with a compacted context view whose required event evidence was missing.
- Confirmed the minimal fix belongs in the compactor/runtime boundary: make compaction event emission error-returning, stop fresh and reused compaction when the required event cannot be written, and keep ordinary no-compaction provider-view construction unchanged.

### Review 200

- Confirmed FCA-20260527-207 against the CompletionController contract in `spec/01-runtime-architecture.md`: `artifact.tracked` is a durable session event written by the completion controller when a required artifact is touched by `write_file` or `edit_file`.
- Confirmed `CompletionController.TrackToolResult` persisted `artifact-tracker.json` and mirrored `contract.json`, then emitted `artifact.tracked` through a best-effort callback. A blocked `events.jsonl` could therefore let the runtime continue to the next same-turn tool call, including `finish`, while the artifact state had advanced without the required event evidence.
- Confirmed the minimal fix belongs in the runtime completion boundary: make completion-controller event emission error-returning for the runtime path, require `artifact.tracked` after the artifact state update, roll back tracker/contract derived state if the event cannot be written, preserve the already executed tool result for provider replay, and stop later same-turn tool calls.

### Review 201

- Confirmed FCA-20260527-208 against the same CompletionController event catalog in `spec/01-runtime-architecture.md` and `spec/18-durable-contract-and-completion.md`: `completion.evaluate.started`, `completion.gate.passed`, `completion.gate.blocked`, `completion.evaluate.finished`, `artifact.gate.passed`, and `artifact.gate.blocked` are durable completion-controller events.
- Confirmed the runtime path already passed an error-returning emitter after FCA-20260527-207, but the controller still ignored those errors for finish gate evaluation and allowed `finish` execution to proceed. A focused regression blocked `events.jsonl` at `artifact.gate.passed`; before the fix, the failure surfaced later as `tool.after` after `finish` had already run.
- Confirmed the minimal fix belongs in `CompletionController` plus the engine tool loop: propagate completion gate event errors as a non-tool-execution gate failure, persist a replayable error tool result for the affected provider call, and stop before `finish` mutates session completion state.

### Review 202

- Confirmed FCA-20260527-209 against the core event catalog in `spec/01-runtime-architecture.md` and the hook point list in `spec/06-hooks.md`: `assistant.message` is a durable session event paired with persisted assistant output.
- Confirmed `Engine.Run` appended the assistant message, then emitted `assistant.message` through best-effort `emit`; blocked `events.jsonl` could leave provider assistant output and tool calls in `messages.jsonl` while runtime continued to execute those tools without the matching assistant timeline event.
- Confirmed the minimal fix belongs in the runtime assistant-output boundary: keep the already persisted assistant message for provider replay, but require the matching `assistant.message` event before executing any provider tool calls from that assistant turn.

### Review 203

- Confirmed FCA-20260527-210 against `spec/03-provider-contracts.md`: provider adapters may emit `turn.stopped`, and runtime uses that event to record stop reason, provider response id, and usage/cache token counters for the completed provider turn.
- Confirmed existing runtime coverage already asserted cache usage counters in `turn.stopped`, while `Engine.Run` wrote the event through best-effort `emit` after provider success / provider-attempt ledger / goal accounting. A blocked `events.jsonl` could therefore let runtime persist assistant output or execute tool calls while the provider-turn stop/usage event was missing.
- Confirmed the minimal fix belongs immediately before assistant persistence: require `turn.stopped` event append after provider success/accounting and before assistant output, keeping provider-attempt ledger facts as the durable retry/success source and preserving provider replay boundaries.

## Update Log

### FCA-20260527-210

Slice: `fix(runtime): require turn stopped events`

Finding:

- `spec/03-provider-contracts.md` lists `turn.stopped` in the provider EventSink, and existing runtime tests rely on it to carry usage/cache counters.
- `Engine.Run` emitted `turn.stopped` through best-effort `emit` after provider success and accounting.
- A focused regression blocked `events.jsonl` exactly at `turn.stopped`; before the fix, runtime still persisted assistant output and reached `awaiting_input` with no provider-turn stop/usage event.

Changes:

- Switched `turn.stopped` recording from best-effort `emit` to required `appendEvent`.
- Required the event before assistant message persistence and before any provider tool calls from that turn can execute.
- Added focused runtime coverage proving blocked `turn.stopped` prevents assistant persistence.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestEngineTurnStoppedReportsEventAppendErrorBeforeAssistantPersist -count=1`: failed before the fix because the run entered `awaiting_input` without reporting the missing `turn.stopped` event.
- `go test -timeout 120s ./internal/runtime -run 'TestEngineTurnStoppedReportsEventAppendErrorBeforeAssistantPersist|TestEnginePersistsProviderTurnMetadata|TestEngineAssistantMessageReportsEventAppendErrorBeforeToolExecution' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `gofmt -l internal/runtime/engine.go internal/runtime/engine_test.go`: passed with no output.
- `git diff --check`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./cmd/... ./internal/... ./pkg/... ./validation/cmd/... -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260527-209

Slice: `fix(runtime): require assistant message events`

Finding:

- `assistant.message` is listed in `spec/01-runtime-architecture.md` as a core event and in `spec/06-hooks.md` as a hook point payload.
- Runtime persisted assistant output with provider tool calls to `messages.jsonl`, then emitted `assistant.message` through best-effort `emit`.
- A focused regression blocked `events.jsonl` exactly at `assistant.message`; before the fix, runtime continued past the missing event, executed the provider tool call, and only later failed as an incomplete run.

Changes:

- Switched assistant-output event recording from best-effort `emit` to required `appendEvent`.
- Kept the already appended assistant message intact so provider replay retains the assistant output/tool-call fact.
- Stopped before tool execution when the matching `assistant.message` event cannot be written.
- Retargeted downstream event append-error regressions to block their specific events after the new earlier assistant-message boundary.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestEngineAssistantMessageReportsEventAppendErrorBeforeToolExecution -count=1`: failed before the fix because the run did not report `assistant.message` and the tool path continued.
- `go test -timeout 120s ./internal/runtime -run 'TestEngineAssistantMessageReportsEventAppendErrorBeforeToolExecution|TestEngineAwaitingInputReportsEventAppendError|TestEngineToolBeforeReportsEventAppendErrorBeforeExecution|TestEngineProviderStopReasonReportsFailedEventAppendError|TestEngineIncompleteNoFinishReportsFailedEventAppendError' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `gofmt -l internal/runtime/engine.go internal/runtime/engine_test.go`: passed with no output.
- `git diff --check`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./cmd/... ./internal/... ./pkg/... ./validation/cmd/... -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260527-208

Slice: `fix(runtime): require completion gate events`

Finding:

- `spec/01-runtime-architecture.md` and `spec/18-durable-contract-and-completion.md` list completion gate and artifact gate events as CompletionController session events.
- `CompletionController` had been changed to accept an error-returning emitter, but still ignored errors from `completion.evaluate.started`, `completion.gate.passed`, `completion.gate.blocked`, `completion.evaluate.finished`, `artifact.gate.passed`, and `artifact.gate.blocked`.
- A focused regression blocked `events.jsonl` exactly at `artifact.gate.passed`; before the fix, the controller swallowed the event failure and the runtime executed `finish`, then failed later at `tool.after`.

Changes:

- Added an error channel to `GateDecision` for completion event persistence failures.
- Required `completion.evaluate.started`, completion gate pass/block events, completion evaluate finished events, and artifact gate pass/block events.
- Changed `MarkAllowed` and required-artifact gate evaluation to return event append errors.
- Changed the engine tool loop to persist a replayable error result for the provider tool call and return the completion event error before executing `finish`.
- Added focused controller and engine regressions for the missing `artifact.gate.passed` event boundary.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestEngineArtifactGatePassedReportsEventAppendErrorBeforeFinish -count=1`: failed before the fix because the runtime reported the later `tool.after` error after `finish` had run.
- `go test -timeout 120s ./internal/runtime -run 'TestEngineArtifactGatePassedReportsEventAppendErrorBeforeFinish|TestCompletionControllerReportsArtifactGatePassedEventError|TestEngineArtifactTrackedReportsEventAppendErrorWithReplayResult|TestCompletionControllerTrackToolResultReportsArtifactTrackedEventErrorAndRollsBack' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `gofmt -l internal/runtime/completion_controller.go internal/runtime/contract_controller_test.go internal/runtime/engine.go internal/runtime/engine_test.go`: passed with no output.
- `git diff --check`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./cmd/... ./internal/... ./pkg/... ./validation/cmd/... -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260527-207

Slice: `fix(runtime): require artifact tracked events`

Finding:

- `spec/01-runtime-architecture.md` lists `artifact.tracked` among the CompletionController events written back to the session.
- `TrackToolResult` updated `artifact-tracker.json` and `contract.json`, then emitted `artifact.tracked` through a best-effort callback.
- A focused runtime regression blocked `events.jsonl` exactly at `artifact.tracked`; before the fix, the run completed through the same-turn `finish` call even though the required artifact-tracking event was missing.

Changes:

- Changed the CompletionController event callback to return `error` and wired the runtime path through required `appendEvent`.
- Required `artifact.tracked` persistence after successful required-artifact tracking updates.
- Restored the previous artifact tracker and contract required-artifact snapshot when `artifact.tracked` event append fails.
- Preserved the already executed `write_file` result plus synthetic results for later same-turn tool calls, then returned the event error instead of continuing to `finish`.
- Added focused controller and engine regressions for the blocked `artifact.tracked` event boundary.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestEngineArtifactTrackedReportsEventAppendErrorWithReplayResult -count=1`: failed before the fix because the run completed with the missing `artifact.tracked` event.
- `go test -timeout 120s ./internal/runtime -run 'TestEngineArtifactTrackedReportsEventAppendErrorWithReplayResult|TestCompletionControllerTrackToolResultReportsArtifactTrackedEventErrorAndRollsBack|TestEngineArtifactTrackingFailureWritesReplayCompleteToolResult|TestCompletionControllerRequiresSessionTouchedArtifact' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `gofmt -l internal/runtime/completion_controller.go internal/runtime/contract_controller_test.go internal/runtime/engine.go internal/runtime/engine_test.go`: passed with no output.
- `git diff --check`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./cmd/... ./internal/... ./pkg/... ./validation/cmd/... -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260527-206

Slice: `fix(runtime): require compaction events`

Finding:

- `spec/10-context-compaction.md` requires compaction to write `compact.started`, `compact.finished`, and `compact.reused` events so compacted provider views remain traceable to local file facts.
- `Engine.Run` wired `compactor.BuildWithProfile` with a callback that ignored `store.AppendEvent` errors, and the compactor API could not return emitter failures.
- With `events.jsonl` blocked, fresh compaction or hysteresis reuse could still return a provider-visible compacted view and allow the provider turn to continue without the required compaction event evidence.

Changes:

- Changed compactor `Build`, `BuildWithPolicy`, and `BuildWithProfile` event callbacks to return `error`.
- Returned contextual errors for missing `compact.started`, `compact.finished`, and `compact.reused` event persistence.
- Wired `Engine.Run` compaction event emission through required `AppendEvent` before publishing to the event bus.
- Added focused compactor coverage for fresh-start, fresh-finished, and reuse event failures.
- Stabilized the steer Goal-event regression by replacing its asynchronous event-bus race with a deterministic test-only pre-append hook.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestCompactorReportsEventEmitErrors -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'TestCompactorReportsEventEmitErrors|TestCompactorWritesDurableSummaryArtifact|TestCompactorReusesSummaryWithinHysteresisWindow|TestEngineSteerAcceptanceReportsGoalUpdatedEventAppendError|TestToolGuardBlocksFinishAfterCompactionWhenPromptFallsOutOfRecentTail' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `gofmt -l internal/runtime/compaction.go internal/runtime/compaction_test.go internal/runtime/engine.go internal/runtime/engine_test.go internal/runtime/prompt_test.go`: passed with no output.
- `git diff --check`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./cmd/... ./internal/... ./pkg/... ./validation/cmd/... -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260527-205

Slice: `fix(runtime): require steer goal update events`

Finding:

- `Engine.drainSteer` accepted pending steer input, appended the provider-visible steer user message, wrote `session.steer.accepted`, appended `goal.updated` history for sessions with a current Goal, and then emitted the matching `goal.updated` event through best-effort `emit`.
- With `events.jsonl` blocked after `session.steer.accepted`, the runtime returned to provider execution before the fix; after requiring the event, the first focused regression showed the error but also exposed that the provider-visible steer message and Goal history remained advanced while the steer request stayed pending.

Changes:

- Changed accepted-steer `goal.updated` from best-effort `emit` to required `appendEvent`.
- Loaded a Goal history rollback snapshot before appending accepted-steer Goal history.
- On accepted-steer Goal history or `goal.updated` event failure, removed the just-appended steer user message and kept the original steer request pending for retry.
- On `goal.updated` event failure, restored the previous Goal history before returning the event error.
- Added focused runtime coverage for blocked `events.jsonl` at the accepted-steer Goal event boundary, including message rollback and pending steer retry state.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestEngineSteerAcceptanceReportsGoalUpdatedEventAppendError -count=1`: failed before the fix because the provider was called after the missing `goal.updated` event; after making the event required, the strengthened regression failed until the steer message and Goal history rollback were added.
- `go test -timeout 120s ./internal/runtime -run 'TestEngineSteerAcceptanceReportsGoalUpdatedEventAppendError|TestEngineSteerAcceptanceReportsGoalHistoryError|TestEngineSteerAcceptanceReportsCorruptGoalSnapshot|TestEngineAcceptsPendingSteerBeforeProviderCall|TestEngineSteerAcceptanceReportsAcceptedEventAppendError' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `gofmt -l internal/runtime/engine.go internal/runtime/engine_test.go`: passed with no output.
- `git diff --check`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./cmd/... ./internal/... ./pkg/... ./validation/cmd/... -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260527-204

Slice: `fix(runtime): require hook trace events`

Finding:

- `hooks.Manager` accepted an emitter that could not return errors, and runtime wired hook events to best-effort `emit`.
- If `events.jsonl` was blocked during a `user.message`, session, or tool hook, the hook command or transform could run while the required `hook.triggered`, `hook.command`, `hook.warning`, `hook.failed`, or `hook.finished` trace was missing.
- A focused regression blocked `events.jsonl` before a `user.message` hook; before the fix, the hook command still ran and the failure surfaced later at `user.message` / `session.failed` instead of the missing `hook.triggered` boundary.

Changes:

- Changed `hooks.EmitFunc` to return `error`, and made `hooks.Manager.Trigger` require `hook.triggered`, `hook.finished`, and `hook.failed` event persistence.
- Made hook command trace events error-aware: `hook.command` and fail-open `hook.warning` append failures now return immediately as hook event errors instead of being converted into ordinary fail-open hook failures.
- Wired `Engine.Run` and `Runner.transformUserMessage` hook emitters through `appendEvent`, so hook trace writes are durable session event boundaries.
- Added hook manager coverage for `hook.command`, `hook.finished`, `hook.failed`, and `hook.warning` emitter errors.
- Added runner coverage proving a missing `hook.triggered` blocks the hook command before side effects and leaves no user message, plus coverage for hook command trace failure during `continue`.
- Retargeted existing downstream event tests that previously corrupted `events.jsonl` from hook commands; those tests now either call the downstream state transition directly after a successful hook or block required tool events at their owning tool event boundary.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestRunnerUserMessageHookRequiresTriggeredEventBeforeCommand -count=1`: failed before the fix because the hook command executed and the returned error referenced the later `user.message` / `session.failed` path instead of `hook.triggered`.
- `go test -timeout 120s ./internal/hooks ./internal/runtime -run 'TestManager|TestRunnerUserMessageHookRequiresTriggeredEventBeforeCommand' -count=1`: passed during focused hook-manager retargeting.
- `go test -timeout 120s ./internal/hooks -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'Test(RunnerUserMessageHookRequiresTriggeredEventBeforeCommand|RunnerUserMessageHookCommandEventFailureBlocksContinue|EngineFailReportsFailedEventAppendError|EngineToolAfterReportsEventAppendErrorWithReplayResult|EnginePauseReportsPausedEventAppendError|EngineToolBeforeReportsEventAppendErrorBeforeExecution|RunnerContinueClaimsSessionBeforeUserMessageHook|EngineRefreshesPendingSteerCountAfterConcurrentAppend)' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `gofmt -l internal/hooks/manager.go internal/hooks/manager_test.go internal/hooks/manager_linux_test.go internal/runtime/engine.go internal/runtime/runner.go internal/runtime/engine_test.go internal/runtime/runner_test.go internal/runtime/planmode_test.go`: passed with no output.
- `git diff --check`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./cmd/... ./internal/... ./pkg/... ./validation/cmd/... -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260527-203

Slice: `fix(webconsole): require handle acquired events`

Finding:

- `Service.addHandle` and `Service.promotePendingStart` inserted a WebConsole active handle, then ignored failures appending `webconsole.handle.acquired`.
- If `events.jsonl` was blocked during Web `continue`, Plan Mode continue, or start promotion, the Web process could report/own an active in-memory handle with no durable owner/process clue.
- A restarted or separate WebConsole process derives `running_not_owned` / settled owner details from events, so the missing acquired event broke the file-fact boundary required by the WebConsoleService contract.

Changes:

- Required `webconsole.handle.acquired` event persistence from both direct handle acquisition and pending-start promotion.
- Wrote the acquired event before inserting the handle into the service map, so the service does not expose an in-memory authority without the matching durable clue.
- Kept `webconsole.handle.released` best-effort because release runs during cleanup/close and must not prevent handle removal.
- Added focused regressions for direct `addHandle` and `promotePendingStart` with blocked `events.jsonl`.

Validation:

- `go test -timeout 120s ./internal/webconsole -run 'TestService(AddHandleRequiresAcquiredEvent|PromotePendingStartRequiresAcquiredEvent)' -count=1`: failed before the fix because both paths returned nil while `events.jsonl` was blocked.
- `go test -timeout 120s ./internal/webconsole -run 'TestService(AddHandleRequiresAcquiredEvent|PromotePendingStartRequiresAcquiredEvent|RejectsDuplicateHandleAndPreservesOwner|SessionDetailReportsActiveHandleOwner|InterruptNonOwnedSessionReturnsStructuredError|StopNonOwnedSessionReturnsStructuredError)' -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed with no output.
- `git diff --check`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./cmd/... ./internal/... ./pkg/... ./validation/cmd/... -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260527-202

Slice: `fix(runtime): require tool after events`

Finding:

- `Engine.Run` emitted `tool.after` through unchecked `e.emit` after a tool finished.
- In a same-turn multi-tool batch, a blocked `events.jsonl` after the first tool completed could be ignored at `tool.after`, then the next required `tool.before` would fail.
- That failure reported the wrong boundary and, more importantly, returned before the already-executed first tool result was persisted, leaving recovery without replay-complete tool results for the provider tool call.

Changes:

- Switched `tool.after` from best-effort `emit` to checked `appendEvent`.
- On `tool.after` append failure, persisted the current tool result plus synthetic results for remaining same-turn tool calls before returning the event error.
- Stopped later tool execution after a missing `tool.after` event so no additional side effects run after the durable lifecycle timeline has diverged.
- Retargeted downstream `session.completed`, `todo_write`, and `submit_plan` event regressions to the new event ordering where `tool.after` is the next required post-result boundary.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestEngineToolAfterReportsEventAppendErrorWithReplayResult -count=1`: failed before the fix because the runtime reported the next `tool.before` and did not persist a replay-complete tool result.
- `go test -timeout 120s ./internal/runtime -run 'TestEngineToolAfterReportsEventAppendErrorWithReplayResult|TestEngineCompleteReportsCompletedEventAppendError|TestEngineTodoWriteReportsRequiredEventAppendError|TestEngineSubmitPlanReportsPlanSubmittedEventAppendError' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'TestEngineToolAfterReportsEventAppendErrorWithReplayResult|TestEngineBudgetWrapUpAwaitingReportsEventAppendError|TestEngineCompleteReportsCompletedEventAppendError|TestEngineTodoWriteReportsRequiredEventAppendError|TestEngineSubmitPlanReportsPlanSubmittedEventAppendError' -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `gofmt -l internal/runtime/engine.go internal/runtime/engine_test.go internal/runtime/planmode_test.go`: passed with no output.
- `git diff --check`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.
- `go test -timeout 120s ./internal/skills ./internal/tools -count=1`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./cmd/... ./internal/... ./pkg/... ./validation/cmd/... -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260527-201

Slice: `fix(runtime): require tool interrupted events`

Finding:

- `Engine.Run` emitted `tool.interrupted` through unchecked `e.emit` after a tool returned `context.Canceled`.
- The same branch persisted a replayable interrupted tool result, then continued to pause/fail/steer handling.
- A blocked `events.jsonl` path during a running tool could therefore return a later `session.paused` event error while leaving the interrupted tool result in `messages.jsonl` with no catalogued `tool.interrupted` timeline evidence.

Changes:

- Switched interrupted-tool event recording from best-effort `emit` to checked `appendEvent`.
- Preserved the interrupted tool result and same-turn synthetic results even when the event append fails, avoiding dangling provider tool calls on recovery.
- Returned a `tool.interrupted` event error before pause/steer/fail handling when the interrupted event cannot be written.
- Retargeted the existing `session.paused` append-error regression to block `events.jsonl` from a successful `session.pause` hook, keeping the downstream pause-event boundary covered.

Validation:

- `go test -timeout 120s ./internal/runtime -run 'TestEngineToolInterruptedReportsEventAppendErrorWithReplayResult|TestEnginePauseReportsPausedEventAppendError' -count=1`: failed before the fix because the missing interrupted event was swallowed and the returned error referenced `session.paused`.
- `go test -timeout 120s ./internal/runtime -run 'TestEngineToolInterruptedReportsEventAppendErrorWithReplayResult|TestEnginePauseReportsPausedEventAppendError|TestEngineWritesInterruptedToolResultOnPause|TestEngineStopsAfterReplayCompleteToolResultsWhenRunContextCancelsTool' -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `gofmt -l internal/runtime/engine.go internal/runtime/engine_test.go`: passed with no output.
- `git diff --check`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.
- `go test -timeout 120s ./internal/skills ./internal/tools -count=1`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./cmd/... ./internal/... ./pkg/... ./validation/cmd/... -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260527-200

Slice: `fix(runtime): require tool before events`

Finding:

- `Engine.Run` emitted `tool.before` through unchecked `e.emit` immediately before hook execution and tool dispatch.
- Because `tool.before` is the durable pre-execution lifecycle event, a blocked `events.jsonl` path after the provider returned a tool call could let the runtime execute a tool without any catalogued pre-side-effect timeline evidence.
- A focused regression blocked `events.jsonl` after the provider response; before the fix, the side-effect tool executed and the run only failed on the next required `session.context.loaded` append.

Changes:

- Switched `tool.before` from best-effort `emit` to checked `appendEvent`.
- Required the event before `tool.before` hooks, tool argument mutation, `tool_execute` state persistence, guard evaluation, and tool dispatch.
- Added a focused regression proving blocked `events.jsonl` now returns a `tool.before` event error, does not execute the tool, and does not persist a tool result message.
- Retargeted existing finish, `todo_write`, and `submit_plan` event-failure regressions to block `events.jsonl` from a successful `tool.before` hook, preserving their downstream-event coverage now that the pre-execution boundary is required.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestEngineToolBeforeReportsEventAppendErrorBeforeExecution -count=1`: failed before the fix because the side-effect tool executed and the eventual error referenced the next `session.context.loaded` append instead of `tool.before`.
- `go test -timeout 120s ./internal/runtime -run TestEngineToolBeforeReportsEventAppendErrorBeforeExecution -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'TestEngine(CompleteReportsCompletedEventAppendError|TodoWriteReportsRequiredEventAppendError|SubmitPlanReportsPlanSubmittedEventAppendError|ToolBeforeReportsEventAppendErrorBeforeExecution|WritesInterruptedToolResultOnPause|WritesReplayCompleteToolResultsWhenBeforeHookFails|WritesSyntheticToolResultsAfterFinishInSameTurn)' -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `gofmt -l internal/runtime/engine.go internal/runtime/engine_test.go internal/runtime/planmode_test.go`: passed with no output.
- `git diff --check`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.
- `go test -timeout 120s ./internal/skills ./internal/tools -count=1`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./cmd/... ./internal/... ./pkg/... ./validation/cmd/... -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260527-199

Slice: `fix(runtime): require child spawned events`

Finding:

- `SpawnAgent` synchronous child mode created and ran a child session, then emitted `session.child.spawned` to the parent timeline through unchecked `r.emit`.
- The same code then advanced `parent-coordination.json` through `addParentChildSession` / `resolveParentChildSession`.
- A blocked parent `events.jsonl` path could therefore return a successful delegate result and parent coordination facts while the parent timeline lacked the catalogued child-spawn evidence.

Changes:

- Switched synchronous `session.child.spawned` emission to checked `appendEvent`.
- Required the spawned event before parent coordination is updated.
- Preserved the child session result in the returned `DelegateResult` when the parent event append fails, so the caller can still inspect the already-created child.
- Added a focused regression proving blocked parent `events.jsonl` now returns a `session.child.spawned` error and does not advance parent coordination.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestRunnerDelegateReportsChildSpawnedEventAppendError -count=1`: failed before the fix because delegate returned success while parent `events.jsonl` was blocked.
- `go test -timeout 120s ./internal/runtime -run 'TestRunnerDelegate(ReportsChildSpawnedEventAppendError|ReportsParentCoordinationError|CreatesChildSessionWithIsolation|TreatsNoneIsolationModeAsOff)' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'TestRunnerDelegate(ReportsChildSpawnedEventAppendError|ReportsParentCoordinationError|CreatesChildSessionWithIsolation|TreatsNoneIsolationModeAsOff|KeepsExistingCwdRelativeWorkdir|AppliesRoleProviderOverrideWhenProviderModelOmitted)' -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `gofmt -l internal/runtime/delegation.go internal/runtime/delegation_test.go`: passed with no output.
- `git diff --check`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.
- `go test -timeout 120s ./internal/skills ./internal/tools -count=1`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./cmd/... ./internal/... ./pkg/... ./validation/cmd/... -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260527-198

Slice: `fix(runtime): require child queued events`

Finding:

- `Runner.QueueSubmit` persisted a parent-linked queued job and added it to `parent-coordination.json`, but did not require the matching `session.child.queued` parent timeline event.
- `SpawnAgent(background=true)` emitted `session.child.queued` through unchecked `r.emit` after `QueueSubmit`, then redundantly called `addParentQueueJob` again.
- A blocked `events.jsonl` path could therefore return a successful queued job and leave parent coordination parked while the durable parent timeline lacked the submit-time child queued evidence required by the event catalog.

Changes:

- Moved the required `session.child.queued` append into `QueueSubmit` for parent-linked jobs.
- Added rollback for submit-time event failure: restore the previous parent coordination snapshot and delete the queued job before returning the event append error.
- Removed the duplicate unchecked child queued emit and duplicate parent coordination mutation from `SpawnAgent(background=true)`.
- Extended queue submit regressions to assert both event-failure rollback and successful `session.child.queued` event persistence.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestRunnerQueueSubmitReportsChildQueuedEventAppendError -count=1`: failed before the fix because `QueueSubmit` returned a queued job with no `session.child.queued` event error.
- `go test -timeout 120s ./internal/runtime -run 'TestRunnerQueueSubmitReportsChildQueuedEventAppendError|TestRunnerQueueSubmitAndWorkerCompletesJob|TestRunnerQueueSubmitReportsParentCoordinationError' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'TestRunnerQueueSubmitReportsChildQueuedEventAppendError|TestRunnerQueueSubmitAndWorkerCompletesJob|TestRunnerQueueSubmitReportsParentCoordinationError|TestProcessNextJobMarksFailedJobWithoutReturningError|TestRunnerProcessNextJobReportsQueueLifecycleEventAppendError' -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `gofmt -l internal/runtime/delegation.go internal/runtime/delegation_test.go internal/runtime/parent_coordination.go internal/session/store.go internal/session/types.go`: passed with no output.
- `git diff --check`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/... ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260527-197

Slice: `fix(app): restore goal history on event failures`

Finding:

- CLI `goal pause/resume/complete`, `goal clear`, and direct `goal plan approve` appended Goal history facts before appending the paired session event.
- On blocked `events.jsonl`, these commands returned an error and restored `goal.json`, but left the just-appended `goal.paused`, `goal.cleared`, or `mission.plan.approved` history entry in `artifacts/goal-history.jsonl`.
- Recovery views and later audits could therefore see a historical transition that the CLI reported as failed and that had no matching durable event timeline fact.

Changes:

- Snapshotted Goal history before CLI status transitions, clear, and direct mission approval.
- Restored Goal history alongside `goal.json` when the paired `goal.*` or `mission.plan.approved` event append fails.
- Extended CLI regressions to assert blocked event writes restore both the current Goal snapshot and Goal history for status, clear, and direct mission approval.

Validation:

- `go test -timeout 120s ./internal/app -run 'TestGoal(Status|Clear)CommandRollsBackHistoryWhenEventAppendFails' -count=1`: failed before the fix because `goal.paused` and `goal.cleared` history entries remained after event failure.
- `go test -timeout 120s ./internal/app -run 'TestGoalPlanApproveCommandReportsEventAppendError' -count=1`: failed before the fix because `mission.plan.approved` remained in `goal.json` / Goal history after event failure.
- `go test -timeout 120s ./internal/app -run 'TestGoal(Status|Clear)Command(RollsBackHistoryWhenEventAppendFails|ReportsHistoryAppendError)|TestGoalPlanApproveCommandReportsEventAppendError' -count=1`: passed.
- `go test -timeout 120s ./internal/app -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.

### FCA-20260527-196

Slice: `fix(runtime): require linked plan mode events`

Finding:

- The Plan Mode event catalog includes `planmode.linked_goal`, and `EnsurePlanModeForGoal` can relink an existing pending unlinked Plan Mode to the current Goal while appending `artifacts/planmode-history.jsonl`.
- The model tool `create_goal(require_plan_approval=true)`, Web goal/mission/validation endpoints, and CLI `goal plan approve` fallback could reuse such an existing pending gate without persisting the matching session event.
- A blocked `events.jsonl` path could leave `planmode.json` linked to a Goal and Plan Mode history advanced while the event timeline had no durable `planmode.linked_goal` evidence.

Changes:

- Required `planmode.linked_goal` from the model-tool `create_goal` path when an existing pending Plan Mode is relinked.
- Reused one model-tool rollback helper for created and relinked Plan Mode gates, restoring Plan Mode snapshot/history, just-created Goal facts, Goal history, and task snapshots on required event failure.
- Updated Web goal create, goal patch, mission plan patch, mission validation patch, and mission plan approval fallback paths to append either `planmode.created` or `planmode.linked_goal` as appropriate.
- Extended Web rollback paths to restore Plan Mode history together with Plan Mode snapshots after relink event failures and later goal mutation failures.
- Updated the CLI `goal plan approve` fallback to require the same Plan Mode creation/relink event before returning the expected "submit the plan" conflict, restoring Plan Mode state/history on event failure.
- Added focused regressions for model-tool relink failure rollback, Web goal create / goal patch / mission patch relink rollback, and CLI created/relinked gate rollback.

Validation:

- `go test -timeout 120s ./internal/tools -run 'Test(GoalToolsCreateReadRejectInvalidStatusAndComplete|CreateGoalReportsRequiredEventErrorAndRestoresGoal|CreateGoalReportsLinkedPlanModeEventErrorAndRestoresGoal|CreateGoalReportsLinkedPlanModeRelinkEventErrorAndRestoresGoal|UpdateGoalReportsRequiredEventErrorAndRestoresGoal|SubmitPlanReportsRequiredEventErrorAndRestoresPlanMode|RequestUserInputReportsCancellationEventErrorAndRestoresPendingRequest)' -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -run 'TestService(GoalCreateReportsEventAppendErrorAndRollsBack|GoalCreateReportsLinkedPlanModeRelinkEventErrorAndRollsBack|GoalPatchRollsBackLinkedPlanModeWhenEventAppendFails|MissionPlanPatchReportsLinkedPlanModeRelinkEventErrorAndRollsBack|MissionPlanPatchPlanModeReportsHistoryAppendError|GoalPatchMissionPlanModeReportsHistoryAppendError|MissionValidationContractPatchReportsHistoryAppendError|MissionPlanApproveReportsLinkedPlanModeEventAppendError)' -count=1`: passed.
- `go test -timeout 120s ./internal/app -run 'TestGoalPlanApproveCommandReports(EventAppendError|LinkedPlanModeCreatedEventAppendError|LinkedPlanModeRelinkEventAppendError)' -count=1`: passed.
- `go test -timeout 120s ./internal/app -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.

### FCA-20260527-195

Slice: `fix(runtime): require start goal plan events`

Finding:

- `Runner.Start` persisted start-time Goal and Plan Mode facts, then emitted the required `goal.created` and `planmode.created` session events through unchecked `r.emit`.
- A blocked or unwritable `events.jsonl` path could therefore continue toward provider execution with `goal.json`, Goal history, `planmode.json`, or Plan Mode history present but without the matching durable event timeline facts.

Changes:

- Extracted start-time Goal / Plan Mode initialization into a small runtime helper.
- Snapshotted Goal history/tasks and Plan Mode state/history before start-time mutations that may need rollback.
- Required `goal.created` and `planmode.created` events through a checked runner event batch before provider execution.
- Restored just-created Goal and Plan Mode facts when the required event batch cannot be persisted.
- Added focused runtime regressions for blocked event persistence during plain start Goal creation, linked Plan Mode gate creation, and explicit start Plan Mode creation.
- Extended the linked start Plan Mode success test to assert the `goal.created` and `planmode.created` events are actually persisted.

Validation:

- `go test -timeout 120s ./internal/runtime -run 'TestRunnerStart(GoalCreatedEventAppendErrorRestoresGoal|LinkedPlanModeCreatedEventAppendErrorRestoresGoalAndPlanMode|ExplicitPlanModeCreatedEventAppendErrorRestoresPlanMode|GoalPlanApprovalCreatesLinkedPlanModeGate|ReportsStartedEventAppendError)' -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/runner.go internal/runtime/runner_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260527-194

Slice: `fix(tools): require plan input cancellation events`

Finding:

- Live `request_user_input` cancellation persisted `planmode.json` as cancelled and appended `planmode.cancelled` history, then emitted the required `planmode.input_cancelled` and `planmode.cancelled` session events through unchecked `ExecContext.Emit`.
- A blocked or unwritable `events.jsonl` path could therefore return a normal cancellation tool result while durable Plan Mode state claimed cancellation without the matching event timeline facts.

Changes:

- Added a small batch event callback for tool execution so matched tool events can be persisted atomically when the engine owns the session event stream.
- Added `Store.AppendEvents` to append a batch by reading the existing event stream and atomically rewriting it with the new events.
- Switched live Plan Mode input cancellation to snapshot Plan Mode state/history, cancel the Plan Mode, require the `planmode.input_cancelled` + `planmode.cancelled` event batch, and restore the pending request/history on failure.
- Added focused coverage for blocked cancellation event persistence and for atomic event batch appends.

Validation:

- `go test -timeout 120s ./internal/tools -run TestRequestUserInputReportsCancellationEventErrorAndRestoresPendingRequest -count=1`: failed before the fix because `request_user_input` returned normal cancellation.
- `go test -timeout 120s ./internal/tools -run TestRequestUserInputReportsCancellationEventErrorAndRestoresPendingRequest -count=1`: passed.
- `go test -timeout 120s ./internal/session -run TestStoreAppendEventsAppendsBatchAtomically -count=1`: passed.
- `go test -timeout 120s ./internal/tools -run 'Test(RequestUserInputReportsCancellationEventErrorAndRestoresPendingRequest|RequestUserInputReportsAnsweredEventErrorAndRestoresPendingRequest|RequestUserInputReportsRequiredEventErrorBeforeResponder|RequestUserInputResponderErrorKeepsRecoverablePendingRequest|RequestUserInputReportsStateLoadErrorBeforeResponder|RequestUserInputReportsStateSaveErrorBeforeResponder)' -count=1`: passed.
- `go test -timeout 120s ./internal/session -run 'Test(StoreAppendEventsAppendsBatchAtomically|PlanModeInputValidationAndAnswer|PlanModeSubmitApproveAndHistory|CancelPlanModeReturnsHistoryAppendError|ApprovePlanModeReturnsHistoryAppendError)' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'Test(PlanInputCancelRetryAfterHistoryFailureRestoresFacts|CancelPlanModeReportsCancelledEventAppendError|CancelPlanModeRetryAfterCancelledEventFailureDoesNotDuplicateHistory|PlanInputAnswerRetryAfterEventFailureRestoresEvent|RunnerStartReportsStartedEventAppendError)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/session/store.go internal/session/store_test.go internal/runtime/engine.go internal/tools/registry.go internal/tools/registry_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260527-193

Slice: `fix(tools): require linked plan mode events`

Finding:

- `create_goal` with `require_plan_approval=true` persisted `goal.json`, Goal history, `goal.created` event, linked `planmode.json`, and `planmode.created` history, then emitted the required `planmode.created` session event through unchecked `ExecContext.Emit`.
- A blocked or unwritable `events.jsonl` path could therefore leave a model-created approval-gated Goal and linked Plan Mode gate without the matching Plan Mode creation event timeline fact.

Changes:

- Snapshotted Plan Mode state/artifacts and Plan Mode history before linked Plan Mode creation from `create_goal`.
- Switched linked model-tool `planmode.created` emission to the checked tool event callback.
- Restored the previous Plan Mode snapshot/history, cleared the just-created Goal, and restored previous Goal history/tasks when required linked Plan Mode event persistence fails.
- Added focused registry coverage for blocked linked `planmode.created` event persistence.

Validation:

- `go test -timeout 120s ./internal/tools -run TestCreateGoalReportsLinkedPlanModeEventErrorAndRestoresGoal -count=1`: failed before the fix because `create_goal` returned success and left the linked Plan Mode gate.
- `go test -timeout 120s ./internal/tools -run TestCreateGoalReportsLinkedPlanModeEventErrorAndRestoresGoal -count=1`: passed.
- `go test -timeout 120s ./internal/tools -run 'Test(CreateGoalReportsLinkedPlanModeEventErrorAndRestoresGoal|CreateGoalReportsRequiredEventErrorAndRestoresGoal|GoalToolsCreateReadRejectInvalidStatusAndComplete|UpdateGoalReportsRequiredEventErrorAndRestoresGoal|SubmitPlanReportsRequiredEventErrorAndRestoresPlanMode|RequestUserInputReportsAnsweredEventErrorAndRestoresPendingRequest)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/tools/registry.go internal/tools/registry_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260527-192

Slice: `fix(tools): require plan input answer events`

Finding:

- `request_user_input` persisted an answered Plan Mode input by clearing `planmode.json.pending_request` and appending `planmode.input_answered` history, then emitted the required `planmode.input_answered` session event through unchecked `ExecContext.Emit`.
- A blocked or unwritable `events.jsonl` path could therefore return successful answers to the provider while durable Plan Mode state claimed the request was answered without the matching event timeline fact.

Changes:

- Snapshotted Plan Mode state/artifacts and Plan Mode history before model-tool input answer persistence.
- Switched model-tool `planmode.input_answered` emission to the checked tool event callback.
- Restored the previous pending Plan Mode request and previous Plan Mode history when required event persistence fails, then returned an error tool result.
- Added focused registry coverage for blocked `planmode.input_answered` event persistence after the responder returns.

Validation:

- `go test -timeout 120s ./internal/tools -run TestRequestUserInputReportsAnsweredEventErrorAndRestoresPendingRequest -count=1`: failed before the fix because `request_user_input` returned successful answers.
- `go test -timeout 120s ./internal/tools -run TestRequestUserInputReportsAnsweredEventErrorAndRestoresPendingRequest -count=1`: passed.
- `go test -timeout 120s ./internal/tools -run 'Test(RequestUserInputReportsAnsweredEventErrorAndRestoresPendingRequest|RequestUserInputReportsRequiredEventErrorBeforeResponder|RequestUserInputResponderErrorKeepsRecoverablePendingRequest|RequestUserInputReportsStateLoadErrorBeforeResponder|RequestUserInputReportsStateSaveErrorBeforeResponder|SubmitPlanReportsRequiredEventErrorAndRestoresPlanMode)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/tools/registry.go internal/tools/registry_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260527-191

Slice: `fix(tools): require goal creation events`

Finding:

- `create_goal` persisted `goal.json` and `goal.created` history, then emitted the required `goal.created` session event through unchecked `ExecContext.Emit`.
- A blocked or unwritable `events.jsonl` path could therefore leave a model-created current Goal without the matching event timeline fact.

Changes:

- Snapshotted Goal history and task graph before model-driven Goal creation.
- Switched model-tool `goal.created` emission to the checked tool event callback.
- Cleared the just-created goal and restored previous Goal history/tasks when required event persistence fails, then returned an error tool result.
- Added focused registry coverage for blocked plain `goal.created` event persistence.

Validation:

- `go test -timeout 120s ./internal/tools -run TestCreateGoalReportsRequiredEventErrorAndRestoresGoal -count=1`: failed before the fix because `create_goal` returned success and left `goal.json`.
- `go test -timeout 120s ./internal/tools -run TestCreateGoalReportsRequiredEventErrorAndRestoresGoal -count=1`: passed.
- `go test -timeout 120s ./internal/tools -run 'Test(CreateGoalReportsRequiredEventErrorAndRestoresGoal|GoalToolsCreateReadRejectInvalidStatusAndComplete|UpdateGoalReportsRequiredEventErrorAndRestoresGoal|SubmitPlanReportsRequiredEventErrorAndRestoresPlanMode|RequestUserInputReportsRequiredEventErrorBeforeResponder)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/tools/registry.go internal/tools/registry_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260527-190

Slice: `fix(tools): require plan input request events`

Finding:

- `request_user_input` persisted the pending Plan Mode request and awaiting-input state, then emitted the required `planmode.input_requested` session event through unchecked `ExecContext.Emit`.
- A blocked or unwritable `events.jsonl` path could therefore let the interactive responder run and return answers without the matching event timeline fact.

Changes:

- Switched model-tool `planmode.input_requested` emission to the checked tool event callback.
- Returned an error tool result before calling the interactive responder when required event persistence fails.
- Preserved the durable pending request and awaiting-input state as recovery facts.
- Added focused registry coverage for blocked `planmode.input_requested` event persistence.

Validation:

- `go test -timeout 120s ./internal/tools -run TestRequestUserInputReportsRequiredEventErrorBeforeResponder -count=1`: failed before the fix because the responder was called and answers were returned.
- `go test -timeout 120s ./internal/tools -run TestRequestUserInputReportsRequiredEventErrorBeforeResponder -count=1`: passed.
- `go test -timeout 120s ./internal/tools -run 'Test(RequestUserInputReportsRequiredEventErrorBeforeResponder|RequestUserInputResponderErrorKeepsRecoverablePendingRequest|RequestUserInputReportsStateLoadErrorBeforeResponder|RequestUserInputReportsStateSaveErrorBeforeResponder|SubmitPlanReportsRequiredEventErrorAndRestoresPlanMode|UpdateGoalReportsRequiredEventErrorAndRestoresGoal)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/tools/registry.go internal/tools/registry_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260527-189

Slice: `fix(tools): require plan submission events`

Finding:

- `submit_plan` persisted `planmode.json`, `artifacts/planmode-plan.md`, and `planmode.plan_submitted` history, then emitted the required `planmode.plan_submitted` session event through unchecked `ExecContext.Emit`.
- A blocked or unwritable `events.jsonl` path could therefore leave Plan Mode awaiting approval without the matching event timeline fact.

Changes:

- Snapshotted Plan Mode state, plan markdown artifact, and Plan Mode history before model-driven plan submission.
- Switched model-tool `planmode.plan_submitted` emission to the checked tool event callback.
- Restored the previous Plan Mode snapshot and previous Plan Mode history when required event persistence fails, then returned an error tool result.
- Added focused registry coverage for blocked `planmode.plan_submitted` event persistence.

Validation:

- `go test -timeout 120s ./internal/tools -run TestSubmitPlanReportsRequiredEventErrorAndRestoresPlanMode -count=1`: failed before the fix because `submit_plan` returned success and left Plan Mode awaiting approval.
- `go test -timeout 120s ./internal/tools -run TestSubmitPlanReportsRequiredEventErrorAndRestoresPlanMode -count=1`: passed.
- `go test -timeout 120s ./internal/tools -run 'Test(SubmitPlanReportsRequiredEventErrorAndRestoresPlanMode|RequestUserInputResponderErrorKeepsRecoverablePendingRequest|GoalToolsCreateReadRejectInvalidStatusAndComplete|UpdateGoalReportsRequiredEventErrorAndRestoresGoal|TodoWriteReportsRequiredEventErrorAndRestoresPreviousSnapshot|TaskToolsReportRequiredEventErrorAndRestoreTaskGraph)' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'TestEngine(SubmitPlanReportsPlanSubmittedEventAppendError|SubmitPlanStopsTurnAndCompletesLaterToolResults)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/planmode_test.go internal/tools/registry.go internal/tools/registry_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260527-188

Slice: `fix(tools): require goal completion events`

Finding:

- `update_goal(status=complete)` persisted the completed goal snapshot and appended `goal.completed` history, then emitted the required `goal.completed` session event through unchecked `ExecContext.Emit`.
- A blocked or unwritable `events.jsonl` path could therefore leave a model-driven goal completion without the matching event timeline fact.

Changes:

- Snapshotted the current goal and goal history before model-driven completion.
- Switched model-tool `goal.completed` emission to the checked tool event callback.
- Restored the previous goal snapshot and previous goal history when required event persistence fails, then returned an error tool result.
- Added focused registry coverage for blocked `goal.completed` event persistence.

Validation:

- `go test -timeout 120s ./internal/tools -run TestUpdateGoalReportsRequiredEventErrorAndRestoresGoal -count=1`: failed before the fix because `update_goal` returned success and left `goal.json` complete.
- `go test -timeout 120s ./internal/tools -run TestUpdateGoalReportsRequiredEventErrorAndRestoresGoal -count=1`: passed.
- `go test -timeout 120s ./internal/tools -run 'Test(GoalToolsCreateReadRejectInvalidStatusAndComplete|UpdateGoalReportsRequiredEventErrorAndRestoresGoal|TodoWriteReportsRequiredEventErrorAndRestoresPreviousSnapshot|TodoWriteNoopReportsRequiredEventError|TaskToolsReportRequiredEventErrorAndRestoreTaskGraph|TodoAndTaskToolsPersistSessionFiles)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/tools/registry.go internal/tools/registry_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260527-187

Slice: `fix(runtime): require task-system events`

Finding:

- `todo_write`, `task_create`, and `task_update` persisted task-system state and then emitted required task-system events through unchecked `ExecContext.Emit`.
- `Engine.Run` wired that callback to `e.emit`, which ignored `Store.AppendEvent` failures.
- A blocked `events.jsonl` path could therefore leave changed todo/task files without the required event timeline fact.

Changes:

- Added `ExecContext.EmitRequired` for tool events that must be durably recorded.
- Wired `Engine.Run` `EmitRequired` to checked `appendEvent`.
- Switched `todo.updated`, `task.created`, and `task.updated` to the checked callback while keeping existing `Emit` available for best-effort telemetry.
- Restored the previous todo snapshot when a changed `todo_write` cannot persist `todo.updated`.
- Restored the previous task graph when `task_create` or `task_update` cannot persist `task.created` / `task.updated`.
- Added focused registry and engine regressions for blocked required-event persistence.

Validation:

- `go test -timeout 120s ./internal/tools -run 'Test(TodoWriteReportsRequiredEventErrorAndRestoresPreviousSnapshot|TodoWriteNoopReportsRequiredEventError|TaskToolsReportRequiredEventErrorAndRestoreTaskGraph|TodoAndTaskToolsPersistSessionFiles)' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'TestEngine(TodoWriteReportsRequiredEventAppendError|ReportsContextLoadedEventAppendError)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/engine.go internal/runtime/engine_test.go internal/tools/registry.go internal/tools/registry_test.go`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260527-186

Slice: `fix(runtime): roll back failed live input messages`

Finding:

- Checked live-input event appends stopped provider execution when `events.jsonl` was unavailable.
- The failed acceptance paths still left the just-appended steer or background-results user message in `messages.jsonl`.
- Background results could also be marked accepted before the matching `user.message` event persisted.

Changes:

- `drainSteer` now removes the just-appended steer message if the checked steer `user.message` event cannot be written.
- `drainBackground` now writes the background-results `user.message` event before marking notifications accepted.
- `drainBackground` now removes the just-appended background-results message if that event cannot be written, leaving the notification pending for retry.
- Extended focused regressions to assert failed event persistence does not leave provider-visible live-input messages behind and keeps the control item retryable.

Validation:

- `go test -timeout 120s ./internal/runtime -run 'TestEngine(SteerAcceptanceReportsAcceptedEventAppendError|BackgroundAcceptanceReportsAcceptedEventAppendError|AcceptsPendingSteerBeforeProviderCall|AcceptsBackgroundResultsBeforeProviderCall)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/engine.go internal/runtime/engine_test.go`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260527-185

Slice: `fix(runtime): persist context loaded events`

Finding:

- Engine prepare loaded todo/task/project-memory/goal/plan context and built the provider prompt from it.
- It emitted `session.context.loaded` through unchecked `e.emit`.
- Before the fix, blocked `events.jsonl` still let the provider run without durable context-loaded evidence for that turn.

Changes:

- `Engine.Run` now records `session.context.loaded` through checked `appendEvent`.
- If the event append fails, the turn returns an error before provider prompt construction.
- Existing context payload fields are unchanged.
- Added focused runtime coverage for blocked context-loaded event persistence.
- Retargeted existing failed-event regressions so their blocked `events.jsonl` setup occurs after the checked context-loaded event, preserving their original provider failure, provider stop, and incomplete-no-finish coverage.
- Stabilized the existing steer watcher event-persistence regression by waiting for the cancelled watcher goroutine to exit before restoring `events.jsonl`; this avoids a validation-only race where the old watcher could append during the restore window and have its event truncated.

Validation:

- `go test -timeout 120s ./internal/runtime -run 'TestEngine(ReportsContextLoadedEventAppendError|EmitsContextLoadedEventWithDurableState)' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'TestEngine(ProviderFailureReportsFailedEventAppendError|ProviderStopReasonReportsFailedEventAppendError|IncompleteNoFinishReportsFailedEventAppendError|ReportsContextLoadedEventAppendError|EmitsContextLoadedEventWithDurableState)' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run TestRunnerWatchSteerRequiresInterruptRequestedEventBeforeSignal -count=20`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/engine.go internal/runtime/engine_test.go internal/runtime/runner_test.go`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260527-184

Slice: `fix(runtime): persist interrupt steer requests`

Finding:

- `watchSteer` marked interrupt steer requests as seen and signaled the in-memory interrupt path before emitting `session.steer.interrupt_requested`.
- The event append used unchecked `r.emit`.
- Before the fix, blocked `events.jsonl` still triggered in-memory cancellation without the durable interrupt-request event.

Changes:

- `watchSteer` now appends `session.steer.interrupt_requested` through checked `appendEvent`.
- The watcher records the event before marking the request seen or signaling `requestSteerInterrupt`.
- If event persistence fails, the watcher skips the in-memory interrupt signal and leaves the request eligible for a later watcher poll.
- Added focused runner coverage for blocked event persistence and retry after the event path is restored.

Validation:

- `go test -timeout 120s ./internal/runtime -run 'TestRunnerWatchSteer(RequiresInterruptRequestedEventBeforeSignal|HandlesMultipleInterruptRequests)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/runner.go internal/runtime/runner_test.go`: passed with no output.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260527-183

Slice: `fix(runtime): persist deferred steer events`

Finding:

- `deferPendingInterrupts` changed pending interrupt steer requests to `deferred`.
- It emitted `session.steer.deferred` through unchecked `e.emit`.
- Before the fix, blocked `events.jsonl` left the control request deferred without the required deferred-event evidence.

Changes:

- `deferPendingInterrupts` now records `session.steer.deferred` through checked `appendEvent`.
- The event append happens before changing the request status to `deferred`.
- If event persistence fails, the helper returns the append error and leaves the durable steer request pending.
- Added focused runtime coverage for the blocked-event case.

Validation:

- `go test -timeout 120s ./internal/runtime -run 'TestEngine(DeferPendingInterruptReportsEventAppendError|MarksInterruptSteerDeferredWhenToolIgnoresCancel|SteerAcceptanceReportsAcceptedEventAppendError|RefreshesPendingSteerCountAfterConcurrentAppend)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/engine.go internal/runtime/engine_test.go`: passed with no output.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260527-182

Slice: `fix(runtime): persist contract refresh events`

Finding:

- `refreshContractForSession` wrote `contract.json`, `artifact-tracker.json`, and `artifacts/contract-history.jsonl`.
- It then emitted `contract.created` / `contract.updated` through a void callback backed by unchecked runtime emitters.
- Before the fix, blocked `events.jsonl` could leave contract state advanced without the core contract event, and later refreshes could skip event recreation because the already-written contract looked equivalent.

Changes:

- Contract refresh callbacks now return errors, and Runner / Engine refresh paths record `contract.created` / `contract.updated` through checked `appendEvent`.
- Added `Store.SnapshotContractRefresh`, `Store.RestoreContractRefreshSnapshot`, and `Store.LoadContractHistory` to capture and restore the contract snapshot, artifact tracker, and contract history around refresh mutations.
- `refreshContractForSession` restores the prior contract/tracker/history snapshot on required write or core contract event append failure.
- `artifact.required` remains best-effort because it is derived diagnostic visibility, not part of the core event catalog.
- `Continue` refreshes contract state even when no new message is appended, while empty sessions without external user instructions remain a no-op.

Validation:

- `go test -timeout 120s ./internal/runtime -run 'TestContractRefresh(ReportsContractEventAppendErrorAndRestoresSnapshot|RestoresPreviousSnapshotOnContractUpdatedEventError|SkipsEmptySessionWithoutExternalInstruction|EmitsArtifactRequiredEvent|ReportsHistoryAppendError|ResetsArtifactFreshnessForSamePathNewInstruction)' -count=1`: passed.
- `go test -timeout 120s ./internal/session -run 'TestStore' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/session/store.go internal/runtime/contract.go internal/runtime/engine.go internal/runtime/runner.go internal/runtime/contract_controller_test.go`: passed with no output.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260527-181

Slice: `fix(runtime): persist checkpoint resume hints`

Finding:

- `appendCheckpointResumeHint` appended a `longrun_checkpoint` harness reminder to `messages.jsonl`.
- `Runner.Continue` then emitted `checkpoint.resume_hint.injected` through unchecked `r.emit`.
- Before the fix, blocked `events.jsonl` left the checkpoint resume note durable while the run failed later on `session.started` without checkpoint-event context.

Changes:

- `appendCheckpointResumeHint` now returns the appended resume-note message ID.
- `Runner.Continue` now records `checkpoint.resume_hint.injected` through checked `appendEvent`.
- If that event append fails, `Continue` rolls back the just-appended checkpoint note with `RemoveLastMessageIfID` and reports `checkpoint.resume_hint.injected` context.
- Added focused runtime coverage for blocked checkpoint event persistence and helper coverage for returned message ID.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestRunnerContinueReportsCheckpointResumeHintEventAppendError -count=1`: failed before the fix because `Continue` ignored the missing checkpoint event and failed later on `session.started`.
- `go test -timeout 120s ./internal/runtime -run TestRunnerContinueReportsCheckpointResumeHintEventAppendError -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'Test(CheckpointResumeHintWarnsOnIsolationAndTrustDrift|RunnerContinueReportsCheckpointResumeHintEventAppendError)' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'Test(CheckpointResumeHintWarnsOnIsolationAndTrustDrift|CheckpointResumeHintReportsCorruptContractSnapshot|RunnerContinueReportsCheckpointResumeHintEventAppendError|RunnerContinueKeepsDurableTurnAndResetsRunBudgetAfterMaxTurnsFailure)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/session_summary.go internal/runtime/runner.go internal/runtime/runner_test.go internal/runtime/contract_controller_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260527-180

Slice: `fix(runtime): roll back user messages on event failure`

Finding:

- `Runner.appendUserMessage` appended direct Start / Continue / Plan Mode user messages before best-effort `user.message` event emission.
- `Engine.appendHarnessReminder` appended harness-reminder user messages before best-effort `user.message` event emission.
- Before the fix, blocked `events.jsonl` at either point returned success and left a durable `messages.jsonl` entry without matching event evidence.

Changes:

- Added `Store.RemoveLastMessageIfID`, which only removes the current tail message when its ID matches the just-appended message.
- `Runner.appendUserMessage` now records `user.message` through checked `appendEvent`, rolls back the appended message on event failure, and returns error context naming `user.message`.
- `Engine.appendHarnessReminder` now records harness-reminder `user.message` events through checked `appendEvent`, rolls back the appended reminder on event failure, and returns error context naming `user.message`.
- Added focused runtime and session-store regressions for event failure rollback and stale rollback protection.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestRunnerAppendUserMessageReportsEventAppendErrorAndRollsBackMessage -count=1`: failed before the fix because the helper returned nil and kept the user message.
- `go test -timeout 120s ./internal/runtime -run TestEngineAppendHarnessReminderReportsEventAppendErrorAndRollsBackMessage -count=1`: failed before the fix because the helper returned nil and kept the harness reminder.
- `go test -timeout 120s ./internal/runtime -run 'Test(RunnerAppendUserMessageReportsEventAppendErrorAndRollsBackMessage|EngineAppendHarnessReminderReportsEventAppendErrorAndRollsBackMessage)' -count=1`: passed.
- `go test -timeout 120s ./internal/session -run TestStoreRemoveLastMessageIfIDOnlyRemovesMatchingTail -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'Test(RunnerStartReportsStartedEventAppendError|RunnerAppendUserMessageReportsEventAppendErrorAndRollsBackMessage|EngineAppendHarnessReminderReportsEventAppendErrorAndRollsBackMessage)' -count=1`: passed after retargeting the older started-event regression to block `events.jsonl` directly before `runExisting`.
- `git diff --check`: passed.
- `gofmt -l internal/session/store.go internal/session/store_test.go internal/runtime/runner.go internal/runtime/runner_test.go internal/runtime/engine.go internal/runtime/engine_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-179

Slice: `fix(runtime): persist goal accounting events`

Finding:

- Runtime Goal accounting wrote `goal.json` and `artifacts/goal-history.jsonl`, then emitted `goal.accounting.updated`, `goal.budget_limited`, and `goal.budget_wrapup_required` through unchecked `e.emit`.
- Before the fix, blocked `events.jsonl` after provider usage accounting let execution continue past the missing `goal.accounting.updated` event and failed later without accounting context.

Changes:

- `updateGoalAccounting` now uses checked `appendEvent` for `goal.accounting.updated`.
- Budget-limited accounting now uses checked `appendEvent` for `goal.budget_limited` and `goal.budget_wrapup_required`.
- Accounting now returns without emitting Goal events when there is no current Goal ID.
- `Engine.fail` now preserves the original runtime error context when appending the fallback `session.failed` event also fails.
- Added focused runtime coverage proving missing accounting event persistence stops before assistant output is recorded.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestEngineGoalAccountingReportsEventAppendError -count=1`: failed before the fix because the engine failed later on `session.awaiting_input` without `goal.accounting.updated` context.
- `go test -timeout 120s ./internal/runtime -run TestEngineGoalAccountingReportsEventAppendError -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'TestEngine(GoalAccountingReportsEventAppendError|FailReportsFailedEventAppendError|BudgetWrapUpThenFinishAwaitsInput|BudgetWrapUpAwaitingReportsEventAppendError|BudgetWrapUpTurnStartReportsGoalHistoryError)' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'TestEngine(AwaitingInputReportsEventAppendError|ProviderStopReasonReportsFailedEventAppendError|IncompleteNoFinishReportsFailedEventAppendError|CompleteReportsCompletedEventAppendError|GoalAccountingReportsEventAppendError|FailReportsFailedEventAppendError)|TestEngineSubmitPlanReportsAwaitingApprovalEventAppendError' -count=1`: passed after adding the no-current-goal guard exposed by the full runtime sweep.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/goal.go internal/runtime/engine.go internal/runtime/engine_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-178

Slice: `fix(runtime): persist started lifecycle events`

Finding:

- `runExisting` emitted `session.started` through unchecked `r.emit`.
- Before the fix, blocked `events.jsonl` after session creation still let execution reach the provider and only failed later on a different lifecycle event.

Changes:

- `runExisting` now uses checked `appendEvent` for `session.started`.
- Returned event append errors include `session.started` context.
- Added focused runtime coverage proving provider execution does not begin when the started event cannot be persisted.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestRunnerStartReportsStartedEventAppendError -count=1`: failed before the fix because execution reached the provider and failed later without `session.started` context.
- `go test -timeout 120s ./internal/runtime -run TestRunnerStartReportsStartedEventAppendError -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/runner.go internal/runtime/runner_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-177

Slice: `fix(runtime): persist remaining lifecycle events`

Finding:

- Autonomous no-finish failure saved `state.json` as failed, but emitted `session.failed` through unchecked `e.emit`.
- `pause` saved `state.json` as paused, but emitted `session.paused` through unchecked `e.emit`.
- Before the fix, blocked `events.jsonl` still let both transitions return their new state without required lifecycle event evidence.

Changes:

- `incomplete_no_finish` now uses checked `appendEvent` for `session.failed`.
- `pause` now uses checked `appendEvent` for `session.paused`.
- Returned event append errors include transition-specific context.
- Added focused runtime coverage for both blocked-event paths.

Validation:

- `go test -timeout 120s ./internal/runtime -run 'TestEngine(IncompleteNoFinishReportsFailedEventAppendError|PauseReportsPausedEventAppendError)' -count=1`: failed before the fix because both transitions ignored blocked `events.jsonl`.
- `go test -timeout 120s ./internal/runtime -run 'TestEngine(IncompleteNoFinishReportsFailedEventAppendError|PauseReportsPausedEventAppendError|ExecModeRequiresFinish|WritesInterruptedToolResultOnPause)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/engine.go internal/runtime/engine_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-176

Slice: `fix(runtime): persist special awaiting input events`

Finding:

- `awaitingBudgetWrapUp`, `awaitingPlanApproval`, and `awaitingPlanCancelled` saved `state.json` as `awaiting_input`, but emitted the matching `session.awaiting_input` event through unchecked `e.emit`.
- Before the fix, blocked `events.jsonl` still let these helpers return `awaiting_input` for budget wrap-up, Plan Mode approval, and Plan Mode cancellation.

Changes:

- Budget wrap-up awaiting-input transitions now use checked `appendEvent`.
- Plan Mode approval awaiting-input transitions now use checked `appendEvent`.
- Plan Mode cancellation awaiting-input transitions now use checked `appendEvent`.
- Each returned event append error includes the specific transition reason.
- Added focused runtime coverage for all three blocked-event paths.

Validation:

- `go test -timeout 120s ./internal/runtime -run 'TestEngine(BudgetWrapUpAwaitingReportsEventAppendError|SubmitPlanReportsAwaitingApprovalEventAppendError|PlanCancelledReportsAwaitingInputEventAppendError)' -count=1`: failed before the fix because all three transitions ignored blocked `events.jsonl`.
- `go test -timeout 120s ./internal/runtime -run 'TestEngine(BudgetWrapUpAwaitingReportsEventAppendError|SubmitPlanReportsAwaitingApprovalEventAppendError|PlanCancelledReportsAwaitingInputEventAppendError)|TestEngineBudgetWrapUpThenFinishAwaitsInput|TestEngineSubmitPlanStopsTurnAndCompletesLaterToolResults' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/engine.go internal/runtime/engine_test.go internal/runtime/planmode_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-175

Slice: `fix(runtime): persist awaiting input lifecycle events`

Finding:

- `awaitingInput` saved `state.json` as `awaiting_input`, but emitted the matching `session.awaiting_input` event through unchecked `e.emit`.
- Before the fix, a blocked `events.jsonl` still let `Engine.Run` return `awaiting_input` after a run-mode done candidate.

Changes:

- Normal awaiting-input transitions now use checked `appendEvent` for `session.awaiting_input`.
- The returned event append error includes `session.awaiting_input` context.
- Added focused runtime coverage proving blocked `events.jsonl` is reported during normal awaiting-input transition.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestEngineAwaitingInputReportsEventAppendError -count=1`: failed before the fix because awaiting-input returned success with a missing event.
- `go test -timeout 120s ./internal/runtime -run 'TestEngineAwaitingInputReportsEventAppendError|TestEngineRunModeStopsAtAwaitingInput|TestEnginePreservesLoadedSkillStateAcrossNextTurn' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/engine.go internal/runtime/engine_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-174

Slice: `fix(runtime): persist completed lifecycle events`

Finding:

- `complete` saved `state.json` as completed, but emitted the matching `session.completed` event through unchecked `e.emit`.
- Before the fix, a blocked `events.jsonl` still let `Engine.Run` return `completed` after a `finish` tool call.

Changes:

- Session completion now uses checked `appendEvent` for `session.completed`.
- The returned event append error includes `session.completed` context.
- Added focused runtime coverage proving blocked `events.jsonl` is reported during completion.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestEngineCompleteReportsCompletedEventAppendError -count=1`: failed before the fix because completion returned success with a missing event.
- `go test -timeout 120s ./internal/runtime -run 'TestEngineCompleteReportsCompletedEventAppendError|TestEngineDoesNotHardBlockNormalFinishOnStaleFeatureList|TestEngineToolBeforeHookCanRewriteArguments' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/engine.go internal/runtime/engine_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-173

Slice: `fix(runtime): persist provider stop failure events`

Finding:

- Provider stop failures saved `state.json` as failed for `max_tokens` / `blocked` / `error`, but emitted the matching `session.failed` event through unchecked `e.emit`.
- Before the fix, a blocked `events.jsonl` still let `Engine.Run` return a failed result without reporting that the provider stop failure event was missing.

Changes:

- Provider stop failures now use checked `appendEvent` for `session.failed`.
- The returned event append error includes the provider stop failure reason.
- Added focused runtime coverage proving blocked `events.jsonl` is reported for provider stop failures.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestEngineProviderStopReasonReportsFailedEventAppendError -count=1`: failed before the fix because the blocked event append was ignored.
- `go test -timeout 120s ./internal/runtime -run 'TestEngineProviderStopReason(ReportsFailedEventAppendError|FailuresAreResumable)|TestEngineProviderFailureReportsFailedEventAppendError|TestEngineFailReportsFailedEventAppendError' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/engine.go internal/runtime/engine_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-172

Slice: `fix(runtime): persist background accepted events`

Finding:

- `drainBackground` used unchecked event emission for accepted background-results `user.message` and `session.background.accepted` events.
- Before the fix, a blocked `events.jsonl` still allowed pending background results to be appended as a user message, marked accepted, and provider execution to continue.

Changes:

- Background-results acceptance now uses checked event appends for the accepted user-message event.
- Background-results acceptance now uses checked event appends for `session.background.accepted`.
- Added focused runtime coverage proving provider execution does not continue when background accepted events cannot be persisted.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestEngineBackgroundAcceptanceReportsAcceptedEventAppendError -count=1`: failed before the fix because the provider was called after accepted-event append failure.
- `go test -timeout 120s ./internal/runtime -run 'TestEngineBackgroundAcceptanceReportsAcceptedEventAppendError|TestEngineAcceptsBackgroundResultsBeforeProviderCall|TestEngineSteerAcceptanceReportsAcceptedEventAppendError' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/engine.go internal/runtime/engine_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-171

Slice: `fix(runtime): persist steer accepted events`

Finding:

- `drainSteer` used unchecked event emission for accepted steer `user.message` and `session.steer.accepted` events.
- Before the fix, a blocked `events.jsonl` still allowed a pending steer to be appended as a user message and provider execution to continue.

Changes:

- Steer acceptance now uses checked event appends for the accepted user-message event.
- Steer acceptance now uses checked event appends for `session.steer.accepted`.
- Added focused runtime coverage proving provider execution does not continue when accepted steer events cannot be persisted.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestEngineSteerAcceptanceReportsAcceptedEventAppendError -count=1`: failed before the fix because the provider was called after accepted-event append failure.
- `go test -timeout 120s ./internal/runtime -run 'TestEngineSteerAcceptanceReports(AcceptedEventAppendError|GoalHistoryError|CorruptGoalSnapshot)|TestEngineInterruptSteerCancelsProviderAndContinuesWithAcceptedMessage|TestEngineRefreshesPendingSteerCountAfterConcurrentAppend' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/engine.go internal/runtime/engine_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-170

Slice: `fix(runtime): validate steer metadata`

Finding:

- `Steer` validated `state.json` but did not validate `session.json` until after it had already queued the steer request and emitted events.
- Before the fix, a session with corrupt `session.json` and running `state.json` accepted a new steer request into `control/steer.jsonl`.

Changes:

- `Steer` now loads session metadata before state validation and before writing any steer control facts.
- The validated metadata is reused for the best-effort session summary refresh after the steer events are recorded.
- Added focused runtime coverage proving corrupt metadata blocks steer queueing before any control request or event is written.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestRunnerSteerReportsCorruptMetadataBeforeQueueing -count=1`: failed before the fix because steer returned accepted/queued.
- `go test -timeout 120s ./internal/runtime -run 'TestRunnerSteer(ReportsCorruptMetadataBeforeQueueing|ReturnsQueuedBehaviorForRunningSession|ReportsQueuedEventAppendError|RequiresRunningSession)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/runner.go internal/runtime/runner_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-169

Slice: `fix(runtime): report corrupt budget goal snapshot`

Finding:

- `awaitingBudgetWrapUp` ignored `LoadGoal` errors while deciding whether the budget wrap-up had been recorded.
- Before the fix, if `goal.json` became corrupt after `record_goal_progress(kind="budget_wrapup")`, the engine still transitioned to `awaiting_input` / `goal_budget_limited` with a generic message and no error.

Changes:

- `awaitingBudgetWrapUp` now returns `load goal.json for budget wrap-up: ...` on non-missing goal snapshot load failure.
- The existing successful recorded-wrap-up message is preserved when the current goal snapshot loads and contains a budget-wrap-up record.
- Added focused runtime coverage that corrupts `goal.json` through a real `tool.after` hook after budget wrap-up progress is recorded.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestEngineBudgetWrapUpAwaitsReportsCorruptGoalSnapshot -count=1`: failed before the fix because the engine transitioned to `awaiting_input` with no error.
- `go test -timeout 120s ./internal/runtime -run 'TestEngineBudgetWrapUp(AwaitsReportsCorruptGoalSnapshot|ThenFinishAwaitsInput|TurnStartReportsGoalHistoryError)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/engine.go internal/runtime/engine_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-168

Slice: `fix(runtime): fail corrupt delegate handoffs`

Finding:

- `SpawnAgent` discarded child `messages.jsonl` load errors while deriving direct delegate handoff fields after `childRunner.Start`.
- Before the fix, a synchronous delegate whose child `messages.jsonl` was corrupted after completion still returned `completed` with empty `VisiblePaths` and no error.

Changes:

- Direct delegate handoff now fails the delegate result if child metadata or `messages.jsonl` cannot be reloaded after execution.
- Corrupt direct-delegate handoff results keep the child session ID and report the corrupt child fact in `LastError`.
- Parent child coordination now records corrupt direct-delegate handoffs as failed instead of completed.
- Added focused runtime coverage that corrupts child `messages.jsonl` through a real session-complete hook.

Validation:

- `go test -timeout 120s ./internal/runtime -run 'TestRunnerDelegateReportsCorruptChildHandoff(Messages|Metadata)' -count=1`: failed before the fix; `messages.jsonl` corruption returned a completed delegate result with empty visible paths, while `session.json` corruption was already surfaced by linked queue reconciliation.
- `go test -timeout 120s ./internal/runtime -run 'TestRunnerDelegateReportsCorruptChildHandoffMessages|TestRunnerDelegateCopiesVisibleOutputsIntoRequestedWorkspace|TestRunnerDelegateReportsParentCoordinationError|TestRunnerProcessNextJobReportsCorruptChildHandoff(Messages|Metadata)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/delegation.go internal/runtime/delegation_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-167

Slice: `fix(runtime): fail corrupt queue handoffs`

Finding:

- `ProcessNextJob` discarded child metadata and message load errors while deriving queue handoff fields after `childRunner.Start`.
- Before the fix, a child session with corrupt `messages.jsonl` after completion still produced a `completed` queue job with empty `VisiblePaths` and no error.

Changes:

- Queue child handoff now fails the queue job if the child metadata cannot be reloaded after execution.
- Queue child handoff now fails the queue job if child `messages.jsonl` cannot be reloaded after execution.
- The failure is persisted through the existing queue job and background notification lifecycle, with the corrupt child fact named in `LastError`.
- Added focused runtime regressions that corrupt child `messages.jsonl` and `session.json` through real session-complete hooks.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestRunnerProcessNextJobReportsCorruptChildHandoffMessages -count=1`: failed before the fix because the queue job completed with empty visible paths.
- `go test -timeout 120s ./internal/runtime -run TestRunnerProcessNextJobReportsCorruptChildHandoffMessages -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'TestRunnerProcessNextJobReportsCorruptChildHandoff(Messages|Metadata)' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'TestRunnerProcessNextJob(ReportsCorruptChildHandoff(Messages|Metadata)|CopiesVisibleOutputsIntoRequestedWorkspace)|TestRunnerQueueSubmitResolvesRelativeWorkdirAgainstParent|TestProcessNextJobMarksFailedJobWithoutReturningError' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/delegation.go internal/runtime/delegation_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-166

Slice: `fix(session): name corrupt metadata facts`

Finding:

- `Store.LoadMetadata` returned raw JSON/file read errors for malformed present `session.json`.
- Before the fix, Web session routes such as mission plan patch failed safely, but the API error body only said `unexpected end of JSON input` and did not identify `session.json`.

Changes:

- Wrapped metadata read failures as `load session.json: ...`.
- Kept session-id validation errors and missing session files compatible by wrapping only the `readJSONFile` result.
- Added focused WebConsole coverage proving corrupt metadata route failures name `session.json` and do not mutate the Goal mission role plan.

Validation:

- `go test -timeout 120s ./internal/webconsole -run TestServiceMissionRolePlanReportsCorruptSessionMetadata -count=1`: failed before the fix because the error omitted `session.json`.
- `go test -timeout 120s ./internal/webconsole -run TestServiceMissionRolePlanReportsCorruptSessionMetadata -count=1`: passed.
- `go test -timeout 120s ./internal/session -run 'TestStoreRejectsInvalidIDs|TestDeleteSessionTreeRemovesRootAndChildren|TestStoreLoadMetadata' -count=1`: passed, no matching tests were selected in this package.
- `go test -timeout 120s ./internal/webconsole -run 'TestServiceMissionRolePlanReportsCorruptSessionMetadata|TestServiceDeleteSessionRouteRemovesSessionTreeAndJobs' -count=1`: passed after preserving missing `session.json` compatibility for legacy `os.IsNotExist` callers.
- `git diff --check`: passed.
- `gofmt -l internal/session/store.go internal/webconsole/service_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-165

Slice: `fix(session): report corrupt history snapshots`

Finding:

- `AppendGoalHistory` and `AppendPlanModeHistory` discarded current snapshot load errors while auto-filling missing `GoalID` and `PlanModeID`.
- Before the fix, corrupt `goal.json` or `planmode.json` returned nil from history appends and wrote unlinked audit rows with empty IDs.

Changes:

- `AppendGoalHistory` now returns `load goal.json for goal history` when it needs to infer `GoalID` and a present `goal.json` is corrupt or unreadable.
- `AppendPlanModeHistory` now returns `load planmode.json for plan mode history` when it needs to infer `PlanModeID` and a present `planmode.json` is corrupt or unreadable.
- Missing current snapshots remain compatible through `fs.ErrNotExist`.
- Added focused store coverage for corrupt current Goal and Plan Mode snapshots during history append.

Validation:

- `go test -timeout 120s ./internal/session -run 'TestAppend(GoalHistoryReportsCorruptCurrentGoalSnapshot|PlanModeHistoryReportsCorruptCurrentPlanModeSnapshot)' -count=1`: failed before the fix because corrupt snapshots were hidden and appends returned nil.
- `go test -timeout 120s ./internal/session -run 'TestAppend(GoalHistoryReportsCorruptCurrentGoalSnapshot|PlanModeHistoryReportsCorruptCurrentPlanModeSnapshot)' -count=1`: passed.
- `go test -timeout 120s ./internal/session -run 'Test(CreateGoalReturnsHistoryAppendErrorAndRollsBack|AppendGoalHistoryReportsCorruptCurrentGoalSnapshot|UpdateGoalAccountingReturnsHistoryAppendError|CompleteGoalReturnsHistoryAppendError|RecordGoalProgressReturnsHistoryAppendError)' -count=1`: passed.
- `go test -timeout 120s ./internal/session -run 'Test(CreatePlanModeReportsCorruptLinkedGoalSnapshot|SubmitPlanModeReturnsHistoryAppendError|AppendPlanModeHistoryReportsCorruptCurrentPlanModeSnapshot|ApprovePlanModeReturnsHistoryAppendError|PlanModeInputValidationAndAnswer)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/session/goal.go internal/session/planmode.go internal/session/store_test.go internal/session/planmode_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-164

Slice: `fix(runtime): report corrupt compaction goal state`

Finding:

- Compaction and hysteresis reuse discarded `loadGoalOptional` errors while deriving `goal_snapshot` and `goal_present` facts.
- Before the fix, corrupt `goal.json` returned nil from compaction/reuse, wrote or reused a summary with no Goal snapshot, and marked `goal_present=false`.

Changes:

- Propagated corrupt Goal errors from fresh compaction as `load goal.json for compaction`.
- Propagated corrupt Goal errors from fallback reuse as `load goal.json for compaction reuse`.
- Propagated corrupt Goal errors from reuse event metadata as `load goal.json for compaction reuse event`.
- Added focused runtime coverage for fresh compaction and hysteresis reuse with corrupt `goal.json`.

Validation:

- `go test -timeout 120s ./internal/runtime -run 'TestCompactor(ReportsCorruptGoalSnapshot|ReuseReportsCorruptGoalSnapshot)' -count=1`: failed before the fix because corrupt Goal state was hidden as no Goal.
- `go test -timeout 120s ./internal/runtime -run 'TestCompactor(ReportsCorruptGoalSnapshot|ReuseReportsCorruptGoalSnapshot)' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'TestCompactor(ReportsCorrupt(GoalSnapshot|FeatureList)|WritesDurableSummaryArtifact|ReusesSummaryWithinHysteresisWindow)|TestCompactionAddsReferencePrefix' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/compaction.go internal/runtime/compaction_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-163

Slice: `fix(runtime): report corrupt checkpoint optional facts`

Finding:

- `writeLongRunCheckpoint` did not return non-missing `LoadContract`, `LoadGoal`, `LoadPlanMode`, or `LoadParentCoordination` errors while building `checkpoints/longrun-latest.json`.
- Before the fix, corrupt `contract.json`, `goal.json`, `planmode.json`, and `parent-coordination.json` in checkpoint-worthy sessions returned nil from checkpoint writing and could produce a checkpoint without those recovery snapshots or with inaccurate parent wait state.

Changes:

- Propagated corrupt/unreadable `contract.json`, `goal.json`, `planmode.json`, and `parent-coordination.json` errors from `writeLongRunCheckpoint`.
- Preserved missing optional snapshot compatibility by continuing to allow `os.ErrNotExist`.
- Added focused runtime coverage proving corrupt optional recovery snapshots prevent a misleading checkpoint artifact.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestLongRunCheckpointReportsCorruptOptionalFacts -count=1`: failed before the fix because corrupt optional snapshot state returned nil from checkpoint writing.
- `go test -timeout 120s ./internal/runtime -run TestLongRunCheckpointReportsCorruptOptionalFacts -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'TestLongRunCheckpointReportsCorrupt(OptionalFacts|LogFacts|ChildrenQueueFacts|ArtifactTracker|TodoState|TaskGraph)|TestSessionSummaryReportsCorruptOptionalFacts|TestParentCoordinationGateReportsCorruptCoordinationSnapshot|TestCheckpointResumeHintReportsCorruptContractSnapshot|TestProviderAttemptsLedgerAndLongRunCheckpointAreDurable' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/session_summary.go internal/runtime/contract_controller_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-162

Slice: `fix(runtime): report corrupt checkpoint logs`

Finding:

- `writeLongRunCheckpoint` discarded `LoadMessages` and `LoadEvents` errors while building `checkpoints/longrun-latest.json`.
- Before the fix, corrupt `messages.jsonl` and `events.jsonl` in checkpoint-worthy sessions returned nil from checkpoint writing and could produce a checkpoint with zero source counts or missing recent owner clues.

Changes:

- Propagated `LoadMessages` errors from `writeLongRunCheckpoint` as `load messages.jsonl for long-run checkpoint`.
- Propagated `LoadEvents` errors as `load events.jsonl for long-run checkpoint`.
- Added focused runtime coverage proving corrupt message/event logs prevent a misleading checkpoint artifact.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestLongRunCheckpointReportsCorruptLogFacts -count=1`: failed before the fix because corrupt message/event log state returned nil from checkpoint writing.
- `go test -timeout 120s ./internal/runtime -run TestLongRunCheckpointReportsCorruptLogFacts -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'TestLongRunCheckpointReportsCorrupt(LogFacts|ChildrenQueueFacts|ArtifactTracker|TodoState|TaskGraph)|TestSessionSummaryReportsCorruptLogFacts|TestSessionSummaryAndCheckpointRecordRecentOwnerClue|TestProviderAttemptsLedgerAndLongRunCheckpointAreDurable' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/session_summary.go internal/runtime/contract_controller_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-161

Slice: `fix(runtime): report corrupt checkpoint child queue facts`

Finding:

- `writeLongRunCheckpoint` discarded `ListChildren`, `ListJobsByParent`, and `LoadBackgroundNotifications` errors while building `checkpoints/longrun-latest.json`.
- Before the fix, corrupt child `state.json`, corrupt queue job JSON, and corrupt `control/background.jsonl` in checkpoint-worthy sessions returned nil from checkpoint writing and could produce a checkpoint with omitted child/queue/background facts.

Changes:

- Propagated `ListChildren` errors from `writeLongRunCheckpoint` as `load child sessions for long-run checkpoint`.
- Propagated `ListJobsByParent` errors as `load queue jobs for long-run checkpoint`.
- Propagated `LoadBackgroundNotifications` errors as `load control/background.jsonl for long-run checkpoint`.
- Added focused runtime coverage proving corrupt child/queue/background facts prevent a misleading checkpoint artifact.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestLongRunCheckpointReportsCorruptChildrenQueueFacts -count=1`: failed before the fix because corrupt child/queue/background state returned nil from checkpoint writing.
- `go test -timeout 120s ./internal/runtime -run TestLongRunCheckpointReportsCorruptChildrenQueueFacts -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'TestLongRunCheckpointReportsCorrupt(ChildrenQueueFacts|ArtifactTracker|TodoState|TaskGraph)|TestSessionSummaryReportsCorruptChildrenQueueFacts|TestParentCoordinationGateReportsCorruptBackgroundNotifications|TestSessionSummaryAndCheckpointRecordRecentOwnerClue' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/session_summary.go internal/runtime/contract_controller_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-160

Slice: `fix(runtime): report corrupt checkpoint artifacts`

Finding:

- `writeLongRunCheckpoint` discarded `LoadArtifactTracker` errors while building `checkpoints/longrun-latest.json`.
- Before the fix, corrupt `artifact-tracker.json` in a parent-linked session returned nil from checkpoint writing and could produce a checkpoint with an empty `required_artifact_status`.

Changes:

- Propagated `LoadArtifactTracker` errors from `writeLongRunCheckpoint` as `load artifact-tracker.json for long-run checkpoint`.
- Preserved empty/missing artifact-tracker compatibility through the store's existing missing-file empty-list behavior.
- Added focused runtime coverage proving corrupt artifact tracker state prevents a misleading checkpoint artifact.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestLongRunCheckpointReportsCorruptArtifactTracker -count=1`: failed before the fix because corrupt artifact tracker state returned nil from checkpoint writing.
- `go test -timeout 120s ./internal/runtime -run TestLongRunCheckpointReportsCorruptArtifactTracker -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'TestLongRunCheckpointReportsCorrupt(ArtifactTracker|TodoState|TaskGraph)|TestSessionSummaryAndCheckpointSeparateCancelledTasks|TestProviderAttemptsLedgerAndLongRunCheckpointAreDurable' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/session_summary.go internal/runtime/contract_controller_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-159

Slice: `fix(runtime): report corrupt checkpoint todo state`

Finding:

- `writeLongRunCheckpoint` discarded `LoadTodo` errors while building `checkpoints/longrun-latest.json`.
- Before the fix, corrupt `todo.json` in a parent-linked session returned nil from checkpoint writing and could produce a checkpoint with an empty todo summary.

Changes:

- Propagated `LoadTodo` errors from `writeLongRunCheckpoint` as `load todo.json for long-run checkpoint`.
- Preserved empty/missing todo compatibility through the store's existing missing-file empty-list behavior.
- Added focused runtime coverage proving corrupt todo state prevents a misleading checkpoint artifact.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestLongRunCheckpointReportsCorruptTodoState -count=1`: failed before the fix because corrupt todo state returned nil from checkpoint writing.
- `go test -timeout 120s ./internal/runtime -run TestLongRunCheckpointReportsCorruptTodoState -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'TestLongRunCheckpointReportsCorrupt(TodoState|TaskGraph)|TestSessionSummaryAndCheckpointSeparateCancelledTasks|TestProviderAttemptsLedgerAndLongRunCheckpointAreDurable' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/session_summary.go internal/runtime/contract_controller_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-158

Slice: `fix(runtime): surface corrupt log facts in session summary`

Finding:

- `writeSessionSummary` discarded `LoadMessages` and `LoadEvents` errors while deriving Tool Repetition and recent owner clues.
- Before the fix, corrupt `messages.jsonl` rendered as `Tool Repetition: not observed`, and corrupt `events.jsonl` simply omitted recent owner diagnostics.

Changes:

- Rendered `messages.jsonl` load failures in the Tool Repetition section.
- Rendered `events.jsonl` load failures in the recent-owner header slot when owner clues cannot be derived.
- Preserved existing behavior for valid empty logs.
- Added focused runtime coverage for corrupt message and event logs.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestSessionSummaryReportsCorruptLogFacts -count=1`: failed before the fix because corrupt message/event logs were hidden as ordinary empty observations.
- `go test -timeout 120s ./internal/runtime -run TestSessionSummaryReportsCorruptLogFacts -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'TestSessionSummaryReportsCorrupt(LogFacts|ChildrenQueueFacts|ArtifactAndProviderAttemptFacts|OptionalFacts|TaskStateFacts)|TestSessionSummaryAndCheckpointRecordRecentOwnerClue' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/session_summary.go internal/runtime/contract_controller_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-157

Slice: `fix(runtime): surface corrupt queue facts in session summary`

Finding:

- `writeSessionSummary` discarded `ListChildren`, `ListJobsByParent`, and `LoadBackgroundNotifications` errors while rendering the Children And Queue section.
- Before the fix, corrupt child `state.json`, corrupt queue job JSON, and corrupt `control/background.jsonl` all rendered as `Children And Queue: not recorded`.

Changes:

- Rendered child-session list, parent queue job list, and background-notification load failures in the `session.md` Children And Queue section.
- Preserved `not recorded` for genuinely absent child/queue/notification state.
- Added the known `control/background.jsonl` label to background notification load errors so raw JSONL parse failures remain actionable.
- Added focused runtime coverage for corrupt child-session, queue-job, and background-notification facts.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestSessionSummaryReportsCorruptChildrenQueueFacts -count=1`: failed before the fix because corrupt child/queue/background facts rendered as `not recorded`.
- `go test -timeout 120s ./internal/runtime -run TestSessionSummaryReportsCorruptChildrenQueueFacts -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'TestSessionSummaryReportsCorrupt(ChildrenQueueFacts|ArtifactAndProviderAttemptFacts|OptionalFacts|TaskStateFacts)|TestParentCoordinationGateReportsCorruptBackgroundNotifications|TestSessionSummaryAndCheckpointRecordRecentOwnerClue' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/session_summary.go internal/runtime/contract_controller_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-156

Slice: `fix(runtime): surface corrupt artifact facts in session summary`

Finding:

- `writeSessionSummary` discarded `LoadArtifactTracker` and `LoadProviderAttempts` errors while rendering the Required Artifacts and Provider Attempts sections.
- Before the fix, corrupt `artifact-tracker.json` and corrupt `provider-attempts.jsonl` were both displayed as `not recorded`.

Changes:

- Rendered non-missing artifact tracker and provider-attempt ledger load failures in their existing `session.md` sections.
- Preserved `not recorded` for genuinely missing or empty artifact/provider-attempt facts.
- Added focused runtime coverage for corrupt `artifact-tracker.json` and `provider-attempts.jsonl`.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestSessionSummaryReportsCorruptArtifactAndProviderAttemptFacts -count=1`: failed before the fix because corrupt artifact/provider-attempt facts rendered as `not recorded`.
- `go test -timeout 120s ./internal/runtime -run TestSessionSummaryReportsCorruptArtifactAndProviderAttemptFacts -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'TestSessionSummaryReportsCorrupt(ArtifactAndProviderAttemptFacts|OptionalFacts|TaskStateFacts)|TestProviderAttemptsLedgerAndLongRunCheckpointAreDurable' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/session_summary.go internal/runtime/contract_controller_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-155

Slice: `fix(runtime): surface corrupt task state in session summary`

Finding:

- `writeSessionSummary` discarded `LoadTodo` and `ListTasks` errors while rendering the Task State section.
- Before the fix, corrupt `todo.json` and corrupt `tasks/task_0001.json` were both displayed as `Task State: not recorded`.

Changes:

- Rendered non-missing todo and task graph load failures in the `session.md` Task State section.
- Preserved `not recorded` for genuinely empty todo/task state.
- Added focused runtime coverage for corrupt `todo.json` and `tasks/task_0001.json`.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestSessionSummaryReportsCorruptTaskStateFacts -count=1`: failed before the fix because corrupt task-state facts rendered as `not recorded`.
- `go test -timeout 120s ./internal/runtime -run 'TestSessionSummaryReportsCorruptTaskStateFacts|TestSessionSummaryReportsCorruptOptionalFacts|TestSessionSummaryAndCheckpointSeparateCancelledTasks' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/session_summary.go internal/runtime/contract_controller_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-154

Slice: `fix(runtime): report corrupt checkpoint task graph`

Finding:

- `writeLongRunCheckpoint` discarded `ListTasks` errors while building `checkpoints/longrun-latest.json`.
- Before the fix, corrupt `tasks/task_0001.json` in a parent-linked session produced a successful checkpoint with an empty task summary.

Changes:

- Propagated `ListTasks` errors from `writeLongRunCheckpoint` as `load tasks for long-run checkpoint`.
- Preserved no-task compatibility through the store's existing missing-directory empty-list behavior.
- Added focused runtime coverage proving corrupt task graph state prevents a misleading checkpoint artifact.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestLongRunCheckpointReportsCorruptTaskGraph -count=1`: failed before the fix because corrupt task graph state returned nil from checkpoint writing.
- `go test -timeout 120s ./internal/runtime -run 'TestLongRunCheckpointReportsCorruptTaskGraph|TestSessionSummaryAndCheckpointSeparateCancelledTasks|TestProviderAttemptsLedgerAndLongRunCheckpointAreDurable' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/session_summary.go internal/runtime/contract_controller_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-153

Slice: `fix(runtime): report corrupt checkpoint contract drift`

Finding:

- `checkpointDriftWarnings` treated every current `contract.json` load failure as a missing contract while preparing checkpoint resume hints.
- Before the fix, corrupt `contract.json` after a long-run checkpoint returned `injected=true`, appended the checkpoint resume note, and emitted only a missing-current-contract warning.

Changes:

- Changed `checkpointDriftWarnings` to return an error as well as warnings.
- Preserved the existing missing-current-contract warning for absent `contract.json`.
- Returned `load contract.json for checkpoint drift` for corrupt or otherwise unreadable current contract snapshots.
- Added focused runtime coverage proving corrupt current contract state prevents checkpoint resume-note injection.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestCheckpointResumeHintReportsCorruptContractSnapshot -count=1`: failed before the fix because corrupt `contract.json` was reported as a missing current contract warning and the checkpoint note was injected.
- `go test -timeout 120s ./internal/runtime -run 'TestCheckpointResumeHint(ReportsCorruptContractSnapshot|WarnsOnIsolationAndTrustDrift)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/session_summary.go internal/runtime/contract_controller_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-152

Slice: `fix(runtime): surface corrupt facts in session summary`

Finding:

- `writeSessionSummary` collapsed corrupt optional recovery facts into `not recorded`.
- Before the fix, corrupt `goal.json`, `planmode.json`, `contract.json`, `parent-coordination.json`, and `checkpoints/longrun-latest.json` were hidden in `session.md`.

Changes:

- Render non-missing Goal, Plan Mode, contract, parent coordination, and long-run checkpoint load failures in the corresponding `session.md` sections.
- Preserved `not recorded` for absent optional files.
- Added focused summary coverage for corrupt optional fact files.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestSessionSummaryReportsCorruptOptionalFacts -count=1`: failed before the fix because corrupt optional fact files rendered as `not recorded`.
- `go test -timeout 120s ./internal/runtime -run 'TestSessionSummaryReportsCorruptOptionalFacts|TestProviderAttemptsLedgerAndLongRunCheckpointAreDurable|TestSessionSummaryAndCheckpointSeparateCancelledTasks' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/session_summary.go internal/runtime/contract_controller_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-151

Slice: `fix(runtime): report corrupt feature list during compaction`

Finding:

- `compactor.BuildWithProfile` ignored every `LoadFeatureList` error while constructing durable compaction summaries.
- Before the fix, corrupt `feature_list.json` produced a successful compaction summary containing `feature_list: null`.

Changes:

- Changed compaction to ignore only missing `feature_list.json` and the existing symlink/path-safety rejection case.
- Returned `load feature_list.json for compaction` for corrupt or otherwise unreadable feature-list snapshots.
- Added focused compactor coverage proving corrupt `feature_list.json` stops compaction without writing a misleading summary artifact.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestCompactorReportsCorruptFeatureList -count=1`: failed before the fix because compaction succeeded and wrote `feature_list: null`.
- `go test -timeout 120s ./internal/runtime -run 'TestCompactorReportsCorruptFeatureList|TestCompactorWritesDurableSummaryArtifact|TestPreCompletionFeatureGate(IgnoresSymlinkedFeatureList|BlocksCorruptFeatureList)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/compaction.go internal/runtime/compaction_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-150

Slice: `fix(runtime): report queue worker event write failures`

Finding:

- `ProcessNextJob` used best-effort event writes for queue worker claimed/notified/terminal lifecycle events.
- When linked queue reconciliation failed while persisting parent queue facts after a completed child, the worker treated the reconciliation error as a child failure and rewrote the job to failed.

Changes:

- Changed queue worker claimed/notified/terminal lifecycle events to use retry-wrapped `appendEvent` and return write failures.
- Changed engine terminal/awaiting/paused/failed paths to return the child `RunResult` together with linked queue reconciliation errors.
- Added a typed linked-queue reconciliation error so `ProcessNextJob` preserves completed/blocked child status when the error is parent queue fact persistence, while keeping normal child/provider failures as failed jobs.
- Preserved existing atomic queue claim semantics by continuing on `os.ErrNotExist` when a valid queue job file disappears after directory enumeration, while still reporting corrupt queue job JSON.
- Added focused runtime coverage for a blocked parent `events.jsonl` during queue worker completion.

Validation:

- `go test -timeout 120s ./internal/runtime -run 'TestRunnerProcessNextJobReportsQueueLifecycleEventAppendError|TestProcessNextJobMarksFailedJobWithoutReturningError' -count=1`: failed before the fix because a parent event append failure after child completion became a failed queue job.
- `go test -timeout 120s ./internal/runtime -run 'TestRunnerProcessNextJobReportsQueueLifecycleEventAppendError|TestProcessNextJobMarksFailedJobWithoutReturningError|TestRunnerQueueSubmitAndWorkerCompletesJob|TestEngineCompletingQueuedChildReconcilesParentQueueFacts|TestEngineReportsLinkedQueueJobReconcileSaveError' -count=1`: passed.
- `go test -timeout 120s ./internal/session -run 'TestStoreClaimNextQueuedJobIsAtomicAcrossStores|Test(ClaimNextQueuedJobReportsCorruptQueuedJob|ListJobsReportsCorruptQueueJob)' -count=1`: passed after preserving the concurrent claim `os.ErrNotExist` skip without weakening corrupt queue job reporting.
- `git diff --check`: passed.
- `gofmt -l internal/session/store.go internal/runtime/engine.go internal/runtime/delegation.go internal/runtime/delegation_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-149

Slice: `fix(session): report parent queue fact write failures`

Finding:

- `ensureTerminalQueueJobParentState` ignored failures while writing parent background notifications and queue lifecycle events.
- Before the fix, `LoadJob` could return nil after terminal queue reconciliation even when `control/background.jsonl` or `events.jsonl` could not be written.

Changes:

- Changed terminal parent background notification writes to return `EnsureBackgroundNotification` errors.
- Changed queue lifecycle event reconciliation to return `LoadEvents` / `AppendEvent` errors while preserving idempotent success when a matching event already exists.
- Added focused store coverage for blocked parent notification and event writes during terminal queue reconciliation.

Validation:

- `go test -timeout 120s ./internal/session -run 'TestLoadJobReportsTerminalParent(Notification|Event)AppendError' -count=1`: failed before the fix because terminal reconciliation returned nil while parent-visible fact writes were blocked.
- `go test -timeout 120s ./internal/session -run 'TestLoadJobReportsTerminalParent(Notification|Event)AppendError|TestReconcileCompletedSessionCompletesJob|TestReconcileFailedJobUpdatesLinkedRunningSession|TestLoadAndListJobsPreferTerminalDuplicateStatusFile' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/session/store.go internal/session/store_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-148

Slice: `fix(session): report corrupt queue job files`

Finding:

- `listQueueJobCopies` and `ClaimNextQueuedJob` skipped unreadable queue job JSON files.
- Before the fix, corrupt valid queue job files disappeared from `ListJobs` / `ListJobsPage` / `ListJobsByParent`, and `ClaimNextQueuedJob` returned no work instead of reporting the corrupt queued job.

Changes:

- Added valid queue-job filename recognition before reading queue status directory entries.
- Changed list and claim paths to report read errors for valid `<job_id>.json` files.
- Preserved skips for invalid filenames, mismatched valid JSON, invalid job IDs, and invalid statuses.
- Added focused store coverage for corrupt queue job list and claim paths.

Validation:

- `go test -timeout 120s ./internal/session -run 'Test(ClaimNextQueuedJobReportsCorruptQueuedJob|ListJobsReportsCorruptQueueJob)' -count=1`: failed before the fix because corrupt queue job files were skipped.
- `go test -timeout 120s ./internal/session -run 'Test(ClaimNextQueuedJobReportsCorruptQueuedJob|ListJobsReportsCorruptQueueJob|ClaimNextQueuedJobSkipsMismatchedQueueFilename|ListChildrenAndParentJobsUseCreationOrder)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/session/store.go internal/session/store_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-147

Slice: `fix(session): report corrupt linked queue sessions`

Finding:

- `findSessionForQueueJob` returned "not found" when a linked session's `state.json` or `messages.jsonl` was corrupt.
- Before the fix, `LoadJob` on a stale running queue job linked to such a session marked the job failed as `no linked session` instead of reporting the corrupt linked session facts.

Changes:

- Changed `findSessionForQueueJob` to return load errors separately from the no-linked-session result.
- Propagated corrupt linked `state.json` / `messages.jsonl` errors through `reconcileQueueJobSession`.
- Preserved the unrelated-session metadata skip behavior and the valid stale-orphan repair path.
- Added focused store coverage proving corrupt linked session facts are reported and the running job is not rewritten as an orphan failure.

Validation:

- `go test -timeout 120s ./internal/session -run TestLoadJobReportsCorruptLinkedSessionFacts -count=1`: failed before the fix because corrupt linked session facts made `LoadJob` mark the stale running queue job failed as having no linked session.
- `go test -timeout 120s ./internal/session -run 'TestLoadJobReportsCorruptLinkedSessionFacts|TestReconcileStaleLinkedRunningJobFailsSession|TestReconcileCompletedSessionCompletesJob' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/session/store.go internal/session/store_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-146

Slice: `fix(session): report corrupt state snapshots`

Finding:

- `listAllSessions` and `ListChildren` treated every `LoadState` error as a skipped entry after metadata had already loaded.
- Before the fix, corrupt `state.json` made `List`, `ListPage`, and `ListChildren` hide a real session instead of reporting the unreadable state snapshot.

Changes:

- Changed root and child session summary listing to return `state.json` load errors after a valid `session.json` establishes the directory as a real session.
- Preserved the existing behavior that skips directories with unreadable metadata, so stray session-root entries are not promoted into errors.
- Added focused store coverage for corrupt `state.json` across root list, paged list, and child list paths.

Validation:

- `go test -timeout 120s ./internal/session -run TestStoreListReportsCorruptStateSnapshot -count=1`: failed before the fix because `List` returned nil error while `state.json` was corrupt.
- `go test -timeout 120s ./internal/session -run 'TestStoreListReportsCorrupt(State|Summary)Snapshots' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/session/store.go internal/session/store_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-145

Slice: `fix(runtime): report corrupt mission approval history`

Finding:

- `hasMissionPlanApprovedHistory` treated every `LoadGoalHistory` error as no matching linked mission approval.
- Before the fix, retrying linked mission approval after a failed event append returned nil while `artifacts/goal-history.jsonl` was corrupt.

Changes:

- Changed `hasMissionPlanApprovedHistory` to return load errors instead of collapsing them into `false`.
- Propagated corrupt Goal history from `approveLinkedMissionPlan` before appending the missing event or attempting another approval write.
- Added `goal-history.jsonl` filename context to the recovery error.
- Added focused runtime coverage for corrupt Goal history during linked mission approval retry.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestApproveLinkedMissionPlanRetryReportsCorruptGoalHistory -count=1`: failed before the fix because corrupt `goal-history.jsonl` was hidden and retry returned nil.
- `go test -timeout 120s ./internal/runtime -run 'TestApproveLinkedMissionPlanRetry(ReportsCorruptGoalHistory|AfterEventFailureDoesNotDuplicateHistory)' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'TestApproveLinkedMissionPlanRetry(ReportsCorruptGoalHistory|AfterEventFailureDoesNotDuplicateHistory)|TestApproveLinkedMissionPlanReportsEventAppendError|TestApproveLinkedPlanModeMarksMissionPlanApproved' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/runner.go internal/runtime/planmode_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-144

Slice: `fix(runtime): report corrupt plan revision history`

Finding:

- `hasMatchingPlanModeRevisionHistory` treated every `LoadPlanModeHistory` error as no matching Plan Mode revision.
- Before the fix, a retry after a failed Plan Mode revision replay-message append could run the provider with an ordinary continuation message while `artifacts/planmode-history.jsonl` was corrupt.

Changes:

- Changed `hasMatchingPlanModeRevisionHistory` to return load errors instead of collapsing them into `false`.
- Propagated corrupt history from `ensurePlanModeRevisedForMessage` before appending retry user input or running the provider.
- Added `planmode-history.jsonl` filename context to the recovery error.
- Added focused runtime coverage for corrupt Plan Mode revision history during retry.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestRevisePlanModeRetryReportsCorruptHistory -count=1`: failed before the fix because corrupt `planmode-history.jsonl` allowed provider execution and returned `awaiting_input`.
- `go test -timeout 120s ./internal/runtime -run 'TestRevisePlanModeRetry(ReportsCorruptHistory|AfterRevisionMessageFailureAppendsRevisionMessage)' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'TestRevisePlanModeRetry(ReportsCorruptHistory|AfterRevisionMessageFailureAppendsRevisionMessage)|TestContinueMessageReportsCorruptPlanModeSnapshot|TestApprovePlanModeReportsPlanApprovedEventAppendError' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/runner.go internal/runtime/planmode_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-143

Slice: `fix(runtime): report corrupt pre-completion features`

Finding:

- `EvaluatePreCompletionFeatures` ignored all `LoadFeatureList` errors while deciding whether init-mode `finish` should be blocked.
- Before the fix, corrupt `feature_list.json` made the pre-completion feature gate return `GateAllow`.

Changes:

- Changed pre-completion feature gating to ignore only absent `feature_list.json` and the existing symlink/path-safety rejection case.
- Added a `pre_completion_state` block for corrupt or otherwise unreadable feature-list snapshots.
- Included `feature_list.json` in the gate message.
- Added focused runtime coverage for corrupt feature-list state and kept the existing symlink regression passing.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestPreCompletionFeatureGateBlocksCorruptFeatureList -count=1`: failed before the fix because corrupt `feature_list.json` allowed pre-completion `finish`.
- `go test -timeout 120s ./internal/runtime -run 'TestPreCompletionFeatureGate(IgnoresSymlinkedFeatureList|BlocksCorruptFeatureList)' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'TestPreCompletionFeatureGate(IgnoresSymlinkedFeatureList|BlocksCorruptFeatureList)|TestEngineFinishBlockedByPreCompletionFeatures' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/completion_controller.go internal/runtime/contract_controller_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260522-001

Slice: `fix(webconsole): require explicit plan input answers`

Changes:

- Moved Plan Mode input answer collection into a pure frontend helper that returns no answers until every question has an explicit selection.
- Added per-request/per-question selection state in the Web Console.
- Changed pending input option buttons from immediate submit controls into selection controls.
- Added a single disabled-until-complete submit control for each pending Plan Mode input request.
- Added focused Node coverage for multi-question answer collection and updated the embedded asset contract check.

Validation:

- `node validation/scripts/webconsole_utils_test.mjs`: passed, 6/6 tests.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node --check internal/webconsole/assets/icons.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/api.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/workspace-view.js`: passed.
- `go test ./internal/webconsole/ -run TestServiceServesEmbeddedShellAndAssets`: passed.
- `go test ./internal/webconsole/`: passed.

### FCA-20260522-002

Slice: `fix(webconsole): track plan mode continue lifecycle`

Changes:

- Replaced the bare `launchPlanModeContinue` goroutine with `s.trackLaunch`.
- Added a regression test that blocks a Plan Mode continue provider request and verifies `launchWG.Wait()` remains blocked until that continue path is released.

Validation:

- `go test ./internal/webconsole/ -run TestServicePlanModeContinueIsTrackedByLaunchWaitGroup -count=1`: passed.
- `go test ./internal/webconsole/ -run PlanMode -count=1`: passed.
- `go test ./internal/webconsole/ -run TestServicePlanModeReviseInputAndCancelControls -count=1`: passed.
- `go test ./internal/webconsole/ -count=1`: passed.
- `go vet ./internal/webconsole/`: passed.

### FCA-20260522-003

Slice: `fix(webconsole): reject trailing json request data`

Changes:

- Hardened `decodeJSON` to reject request bodies containing more than one JSON value.
- Added a focused `/api/sessions/start` regression test for a valid object followed by a second object.

Validation:

- `go test ./internal/webconsole/ -run TestStartSessionRejectsTrailingJSONValue -count=1`: passed.
- `go test ./internal/webconsole/ -run TestStartSessionRejectsUnknownField -count=1`: passed.
- `go test ./internal/webconsole/ -count=1`: passed.
- `go vet ./internal/webconsole/`: passed.

### FCA-20260522-004

Slice: `fix(tools): reject trailing skill command json`

Changes:

- Hardened `decodeCommandToolArgs` to reject raw command-tool arguments containing more than one JSON value.
- Added a focused regression test for a skill command tool call with a valid first object followed by a second object.

Validation:

- `go test ./internal/tools/ -run TestSkillCommandToolRejectsTrailingJSONValue -count=1`: passed.
- `go test ./internal/tools/ -run TestSkillCommandToolClosesSchemaByDefault -count=1`: passed.
- `go test ./internal/tools/ -count=1`: passed.
- `go vet ./internal/tools/`: passed.

### FCA-20260522-005

Slice: `fix(webconsole): retain live plan input handles`

Changes:

- Changed current-process handle pruning so handles are removed only when the durable state is terminal or cannot be loaded.
- Added a regression that proves a session detail read during pending Plan Mode input keeps the live handle and lets `/planmode/input` answer the original blocked runner.

Validation:

- `go test ./internal/webconsole/ -run TestServicePlanModeInputDetailKeepsLiveHandle -count=1`: passed.
- `go test ./internal/webconsole/ -run TestServicePlanMode -count=1`: passed.
- `go test ./internal/webconsole/ -run TestSessionDetailReportsActiveHandleOwner -count=1`: passed.
- `go test ./internal/webconsole/ -count=1`: passed.
- `go vet ./internal/webconsole/`: passed.

### FCA-20260522-006

Slice: `fix(webconsole): validate plan input before delivery`

Changes:

- Required `request_id` for Web Plan Mode input answers.
- Loaded and checked the stored pending Plan Mode request before answering a live runner or launching fallback continue.
- Rejected request mismatches and invalid answer payloads at the HTTP boundary.
- Extended the live Plan Mode input regression to prove invalid answers do not unblock the waiter and a later valid answer still advances to plan approval.

Validation:

- `go test ./internal/webconsole/ -run TestServicePlanModeInputDetailKeepsLiveHandle -count=1`: passed.
- `go test ./internal/webconsole/ -run TestServicePlanMode -count=1`: passed.
- `go test ./internal/webconsole/ -run TestServicePlanModeReviseInputAndCancelControls -count=1`: passed.
- `go test ./internal/webconsole/ -count=1`: passed.
- `go vet ./internal/webconsole/`: passed.

### FCA-20260522-007

Slice: `fix(runtime): claim session before continue`

Changes:

- Added `Store.ClaimSessionRun`, which locks `control/run.lock`, verifies the current durable status is resumable, and atomically writes `status=running`.
- Changed `Runner.Continue` to claim the session before continuation metadata saves, Plan Mode mutations, checkpoint injection, user-message append, and provider execution.
- Converted post-claim preparation errors to fail the session instead of leaving a durable `running` state behind.
- Added a two-runner regression that blocks the first continue in a user-message hook and proves a concurrent same-session continue is rejected without a second message or provider request.

Validation:

- `go test ./internal/runtime/ -run TestRunnerContinueClaimsSessionBeforeUserMessageHook -count=1`: passed.
- `go test ./internal/runtime/ -run 'TestRunnerRejectsDifferentConcurrentActiveSessionSlot|TestRunnerContinue|TestRunnerAutoContinue' -count=1`: passed.
- `go test ./internal/session/ -count=1`: passed.
- `go test ./internal/runtime/ -count=1`: passed.
- `go vet ./internal/runtime/ ./internal/session/`: passed.

### FCA-20260522-008

Slice: `fix(runtime): block budget-limited finish`

Changes:

- Changed the goal completion gate so `budget_limited + stop_on_budget` keeps blocking `finish` after a budget-wrap-up record exists.
- Kept the more specific missing-wrap-up gate for the first required `record_goal_progress(kind="budget_wrapup")`.
- Updated goal prompt text to tell the model to stop after recording wrap-up facts unless it can legitimately call `update_goal(status="complete")`.
- Added an engine regression proving a same-turn `budget_wrapup` plus `finish` returns the session to `awaiting_input` instead of `completed`.

Validation:

- `go test ./internal/runtime/ -run 'TestGoalBudgetLimitedRequiresWrapUpBeforeFinish|TestEngineBudgetWrapUpThenFinishAwaitsInput' -count=1`: passed.
- `go test ./internal/runtime/ -count=1`: passed.
- `go vet ./internal/runtime/`: passed.

### FCA-20260522-009

Slice: `fix(runtime): serialize parent coordination updates`

Changes:

- Added `Store.MutateParentCoordination` to lock, load, mutate, normalize, and write the coordination snapshot in one transaction.
- Changed parent child/session and queue job add/resolve helpers to use the mutation helper.
- Changed queue status reconciliation to use the same transactional coordination update path.
- Added a concurrent queue-resolution regression that verifies all completed queue job IDs survive parallel terminal updates.

Validation:

- `go test ./internal/runtime/ -run 'TestParentCoordinationConcurrentQueueResolutionsPreserveAllResults|TestParentCoordinationWritesParkedAndResumedEvents|TestParentCoordinationGate' -count=1`: passed.
- `go test ./internal/runtime/ ./internal/session/ -count=1`: passed.
- `go vet ./internal/runtime/ ./internal/session/`: passed.

### FCA-20260522-010

Slice: `fix(webconsole): label send icon button`

Changes:

- Added an explicit button type, accessible label, and hover title to the icon-only composer send button.
- Extended the embedded shell asset test to assert the send button accessible-name contract.

Validation:

- `go test ./internal/webconsole/ -run TestServiceServesEmbeddedShellAndAssets -count=1`: passed.
- `go test ./internal/webconsole/ -count=1`: passed.

### FCA-20260522-011

Slice: `fix(webconsole): guard initial launch submits`

Changes:

- Added `launchInFlight` state for the pre-adoption initial start request.
- Guarded `sendMessage` so a second prompt cannot start another session while the first `/sessions/start` is pending.
- Disabled the send button and kept composer busy styling during initial launch pending state.
- Extended the embedded asset test to assert the launch-pending guard remains wired.

Validation:

- `node --check internal/webconsole/assets/app.js`: passed.
- `go test ./internal/webconsole/ -run TestServiceServesEmbeddedShellAndAssets -count=1`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 6/6 tests.
- `go test ./internal/webconsole/ -count=1`: passed.

### FCA-20260522-012

Slice: `fix(webconsole): confirm risky local actions`

Changes:

- Added a Settings save confirmation, including API-key-specific copy when a non-masked key will be written.
- Added confirmation before clearing the current durable goal.
- Added confirmation before uninstalling a local skill.
- Extended embedded asset tests to assert risky Web actions keep explicit confirmation hooks.

Validation:

- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `go test ./internal/webconsole/ -run TestServiceServesEmbeddedShellAndAssets -count=1`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 6/6 tests.
- `go test ./internal/webconsole/ -count=1`: passed.

### FCA-20260522-013

Slice: `fix(session): preserve background notifications`

Changes:

- Added `control/background.lock` protection for background notification append, ensure, and whole-file update operations.
- Changed `UpdateBackgroundNotifications` to merge against the current durable file before writing, replacing matching notifications by `queue_job_id` or notification id.
- Added a stale-snapshot regression proving a parent acceptance update preserves a concurrently appended child notification.
- Added a symlink-lock regression for the new background notification lock path.

Validation:

- `go test ./internal/session/ -run 'TestUpdateBackgroundNotificationsMergesConcurrentAppend|TestAppendBackgroundNotificationRejectsSymlinkLockFile|TestUpdateSteerRequestsMergesConcurrentAppend' -count=1`: passed.
- `go test ./internal/runtime/ -run 'TestEngineInjectsBackgroundResultsBeforeProviderCall|TestParentCoordinationGate|TestBackground|TestDelegation' -count=1`: passed.
- `go test ./internal/session/ ./internal/runtime/ -count=1`: passed.
- `go vet ./internal/session/ ./internal/runtime/`: passed.
- `go test ./... -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260522-014

Slice: `fix(runtime): align anthropic probe cache`

Changes:

- Passed the Anthropic-compatible `prompt_cache` default into `Runner.Probe` provider requests.
- Added runtime probe coverage proving Anthropic-compatible probes send default cache markers.
- Added runtime probe coverage proving explicit `prompt_cache=false` suppresses cache markers.
- Added Web Settings config-test coverage proving `/api/config/test` inherits the same default Anthropic-compatible probe shape.

Validation:

- `go test ./internal/runtime -run 'TestCustomAnthropicAPIProviderUsesAnthropicAdapter|TestProbeHonorsPromptCacheFalseForAnthropicCompatible|TestProviderOptionsFromConfigDefaultsPromptCacheForAnthropicCompatible|TestProbeDefaultsStoreFalseForCustomOpenAICompatible' -count=1`: passed.
- `go test ./internal/webconsole -run 'TestServiceConfigTestUsesAnthropicPromptCacheDefault|TestServiceConfigTestAppliesReasoningModeWithoutPersisting' -count=1`: passed.
- `go test ./internal/provider -run TestAnthropicAdapterAppliesPromptCacheMarkersAndTelemetry -count=1`: passed.
- `go test ./internal/runtime ./internal/webconsole ./internal/provider -count=1`: passed.
- `go vet ./internal/runtime ./internal/webconsole ./internal/provider`: passed.
- `go test ./... -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-015

Slice: `fix(webconsole): stop advertising workspace switching`

Changes:

- Changed `/api/meta` so `workspace_switch_supported` is false for the current local Workspace surface.
- Updated Workspace view copy to state that root switching is not available.
- Updated the WebConsole meta regression test to assert that root switching is not advertised.

Validation:

- `go test ./internal/webconsole -run TestServiceMetaReportsDefaultWorkspaceSubdirOnly -count=1`: passed.
- `node --check internal/webconsole/assets/workspace-view.js`: passed.
- `go test ./internal/webconsole -count=1`: passed.
- `node --check internal/webconsole/assets/*.js` equivalent explicit asset list: passed.
- `go test ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-016

Slice: `fix(paths): allow dot-prefixed child paths`

Changes:

- Made queue visible-path collection reject only true parent traversal, not every relative path beginning with two dots.
- Made tool display path shortening use the same separator-aware inside-root check.
- Added regressions for legitimate `..reports/output.md` paths while preserving outside-root rejection.

Validation:

- `go test ./internal/session -run TestCollectQueueVisiblePathsAllowsDotPrefixedDirectory -count=1`: passed.
- `go test ./internal/tools -run TestRelativeOrAbsoluteAllowsDotPrefixedChildPath -count=1`: passed.
- `go test ./internal/session ./internal/tools -count=1`: passed.
- `gofmt -l internal/session/store.go internal/session/store_test.go internal/tools/registry.go internal/tools/registry_test.go`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-017

Slice: `fix(provider): map google prompt safety blocks`

Changes:

- Parsed Google `promptFeedback.blockReason` in the provider adapter.
- Returned a normalized `TurnResult{StopReason: "blocked"}` when Google returns a prompt-level safety block without candidates.
- Preserved provider response id, usage telemetry, thinking strategy, and raw provider stop metadata for the blocked response.
- Added a focused Google adapter regression for no-candidate prompt safety blocks.

Validation:

- `go test ./internal/provider -run TestGoogleAdapterMapsPromptSafetyBlockWithoutCandidates -count=1`: passed.
- `gofmt -l internal/provider/google.go internal/provider/provider_test.go`: no output.
- `go test ./internal/provider -count=1`: passed.
- `go test ./internal/runtime -run TestEngineProviderStopReasonFailuresAreResumable -count=1`: passed.
- `git diff --check`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-018

Slice: `fix(provider): reject invalid openai tool arguments`

Changes:

- Validated OpenAI Responses `function_call.arguments` before constructing internal tool calls.
- Returned a provider `response_parse_error` for empty or malformed OpenAI function-call argument strings.
- Added adapter coverage for malformed OpenAI function-call arguments.
- Added runtime coverage proving provider parse errors fail before assistant-message persistence and are recorded in the provider-attempt ledger.

Validation:

- `go test ./internal/provider -run TestOpenAIAdapterRejectsInvalidFunctionCallArguments -count=1`: passed.
- `go test ./internal/runtime -run TestEngineProviderParseErrorFailsBeforeAssistantPersist -count=1`: passed.
- `gofmt -l internal/provider/openai.go internal/provider/provider_test.go internal/runtime/engine_test.go`: no output.
- `go test ./internal/provider -count=1`: passed.
- `go test ./internal/runtime -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-019

Slice: `fix(provider): map openai failed statuses`

Changes:

- Changed OpenAI stop-reason mapping so non-empty non-`completed` statuses fall through to internal `error` when no tool call or max-token incomplete reason applies.
- Preserved raw OpenAI status facts in `RawProvider`.
- Added an OpenAI adapter regression for `status="failed"` with no tool calls.

Validation:

- `go test ./internal/provider -run 'TestOpenAIAdapterMapsNonCompletedStatusToErrorStop|TestOpenAIAdapterSerializesAndParses' -count=1`: passed.
- `go test ./internal/runtime -run TestEngineProviderStopReasonFailuresAreResumable -count=1`: passed.
- `gofmt -l internal/provider/openai.go internal/provider/provider_test.go`: no output.
- `go test ./internal/provider -count=1`: passed.
- `go test ./internal/runtime -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-020

Slice: `fix(provider): map google unknown finishes`

Changes:

- Made Google `finishReason=STOP` the explicit normal done-candidate mapping.
- Mapped other non-empty Google finish reasons to internal `error`.
- Preserved raw Google finish reason metadata for diagnostics.
- Added a Google adapter regression for an unknown finish reason.

Validation:

- `go test ./internal/provider -run 'TestGoogleAdapterMapsUnknownFinishReasonToErrorStop|TestGoogleAdapterSerializesAndParses|TestGoogleAdapterMapsPromptSafetyBlockWithoutCandidates' -count=1`: passed.
- `go test ./internal/runtime -run TestEngineProviderStopReasonFailuresAreResumable -count=1`: passed.
- `gofmt -l internal/provider/google.go internal/provider/provider_test.go`: no output.
- `go test ./internal/provider -count=1`: passed.
- `go test ./internal/runtime -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-021

Slice: `fix(provider): map anthropic unknown stops`

Changes:

- Made Anthropic `stop_reason=end_turn` the explicit normal done-candidate mapping.
- Mapped other non-empty Anthropic stop reasons to internal `error`.
- Preserved raw Anthropic stop reason metadata for diagnostics.
- Added an Anthropic adapter regression for an unknown stop reason.

Validation:

- `go test ./internal/provider -run 'TestAnthropicAdapterMapsUnknownStopReasonToErrorStop|TestAnthropicAdapterSerializesAndParses|TestAnthropicAdapterAppliesPromptCacheMarkersAndTelemetry' -count=1`: passed.
- `go test ./internal/runtime -run TestEngineProviderStopReasonFailuresAreResumable -count=1`: passed.
- `gofmt -l internal/provider/anthropic.go internal/provider/provider_test.go`: no output.
- `go test ./internal/provider -count=1`: passed.
- `go test ./internal/runtime -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-022

Slice: `fix(provider): require object tool arguments`

Changes:

- Added a shared provider-adapter helper for trimming and validating tool-call arguments as JSON objects.
- Applied the helper to OpenAI `function_call.arguments`, Anthropic `tool_use.input`, and Google `functionCall.args`.
- Preserved valid object arguments exactly after whitespace trimming.
- Returned provider `response_parse_error` for empty, malformed, or non-object tool-call arguments.
- Added focused OpenAI, Anthropic, and Google adapter regressions for non-object tool arguments.

Validation:

- `go test ./internal/provider -run 'TestOpenAIAdapterRejectsNonObjectFunctionCallArguments|TestOpenAIAdapterRejectsInvalidFunctionCallArguments|TestAnthropicAdapterRejectsNonObjectToolUseInput|TestGoogleAdapterRejectsNonObjectFunctionCallArgs' -count=1`: passed.
- `gofmt -l internal/provider/tool_args.go internal/provider/openai.go internal/provider/anthropic.go internal/provider/google.go internal/provider/provider_test.go`: no output.
- `go test ./internal/provider -count=1`: passed.
- `go test ./internal/runtime -run 'TestEngineProviderParseErrorFailsBeforeAssistantPersist|TestEngineProviderStopReasonFailuresAreResumable' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-023

Slice: `fix(webconsole): preserve message paging gaps`

Changes:

- Added pure frontend message-window merge helpers for overlapping tail refreshes and anchor-based older-page insertion.
- Tracked `messageGapAnchorId` when polling receives a new tail window that no longer overlaps already loaded older history.
- Kept the load-earlier control visible for known middle gaps and fetched missing pages before the tail anchor.
- Reset paging/gap state when switching or resetting sessions.
- Added Node unit coverage for overlapping tails, non-overlap gap detection, and middle-page insertion.

Validation:

- `node --check internal/webconsole/assets/utils.js`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/api.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/icons.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/workspace-view.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 9/9 tests.
- `go test ./internal/webconsole -run 'TestServiceServesEmbeddedShellAndAssets|TestServiceSessionMessagePaging' -count=1`: passed.
- `go test ./internal/webconsole -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-024

Slice: `fix(webconsole): require exact json content type`

Changes:

- Replaced the JSON mutation guard's prefix content-type check with `mime.ParseMediaType` plus exact `application/json` matching.
- Preserved support for valid parameters such as `application/json; charset=utf-8`.
- Added a focused WebConsole regression proving `application/json-patch+json` is rejected before the mutation handler runs.

Validation:

- `go test ./internal/webconsole -run 'TestServiceRejectsJSONMutationSubtypeContentType|TestServiceRejectsForeignOriginMutation|TestServiceRejectsOversizedJSONMutationBody|TestServiceWorkerScaling' -count=1`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: no output.
- `go test ./internal/webconsole -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-025

Slice: `fix(webconsole): track pending starts on close`

Changes:

- Added Web service pending-start tracking for starts that have a cancel function but no observed session id yet.
- Made initial `runner.Start` use `trackLaunch` immediately, before waiting for `session.created`.
- Made `Service.Close` mark the service closed, cancel pending starts and active handles, and then wait for tracked launch goroutines.
- Guarded handle addition/promotion after close so late starts cannot re-enter the active handle map.
- Added focused lifecycle coverage for pending-start cancellation during close.

Validation:

- `go test ./internal/webconsole -run 'TestServiceCloseCancelsPendingStartBeforeSessionID|TestServicePlanModeContinueIsTrackedByLaunchWaitGroup|TestSessionDetailReportsActiveHandleOwner|TestServiceInterruptUsesManualPauseReason|TestServiceStopSessionPausesWithManualStopReason' -count=1`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: no output.
- `go test ./internal/webconsole -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-026

Slice: `fix(tools): enforce built-in input closure`

Changes:

- Added registry-level built-in input checks before handler execution.
- Rejected non-object built-in arguments, trailing JSON values, and unknown fields from closed object schemas.
- Applied unknown-field checks recursively through nested object properties and array item schemas.
- Kept skill command tools on their existing command-specific validation path.
- Added focused built-in regressions for top-level unknown fields, trailing JSON, and nested unknown fields.

Validation:

- `go test ./internal/tools -run 'TestBuiltinToolExecutionRejectsUnknownTopLevelField|TestBuiltinToolExecutionRejectsTrailingJSONValue|TestBuiltinToolExecutionRejectsNestedUnknownField|TestBuiltinToolSchemasDisallowUnknownProperties|TestSkillCommandToolRejectsMissingRequiredField|TestSkillCommandToolClosesSchemaByDefault|TestSkillCommandToolRejectsTrailingJSONValue|TestSkillCommandToolPreservesExplicitAdditionalPropertiesTrue' -count=1`: passed.
- `gofmt -l internal/tools/registry.go internal/tools/registry_test.go`: no output.
- `go test ./internal/tools -count=1`: passed.
- `go test ./internal/runtime -run 'TestEngineWritesInterruptedToolResultOnPause|TestEngineStopsAfterReplayCompleteToolResultsWhenRunContextCancelsTool|TestEngineMarksInterruptSteerDeferredWhenToolIgnoresCancel|TestEnginePreservesDeadlineToolResultMetadata' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-027

Slice: `fix(webconsole): preserve active handle owner`

Changes:

- Made `addHandle` and pending-start promotion reject duplicate current-process handles for the same session.
- Preserved HTTP conflict behavior for duplicate continue / Plan Mode launch attempts while keeping service-close errors as unavailable.
- Made `finishHandle` release the map entry only if that exact handle still owns the session slot.
- Added a focused lifecycle regression proving a duplicate/stale handle release cannot remove the original active owner.

Validation:

- `go test ./internal/webconsole -run 'TestServiceRejectsDuplicateHandleAndPreservesOwner|TestServiceCloseCancelsPendingStartBeforeSessionID|TestServicePlanModeContinueIsTrackedByLaunchWaitGroup|TestSessionDetailReportsActiveHandleOwner|TestServiceInterruptUsesManualPauseReason|TestServiceStopSessionPausesWithManualStopReason' -count=1`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: no output.
- `go test ./internal/webconsole -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-028

Slice: `fix(session): serialize goal snapshot mutations`

Changes:

- Added `Store.MutateGoal` with a session-scoped `goal.lock` around goal snapshot read / mutate / validate / write.
- Routed goal completion, budget accounting, and structured goal progress through the serialized mutation path.
- Kept no-goal runtime accounting as a no-op so ordinary non-goal sessions remain unaffected.
- Preserved `SaveGoal` full-snapshot validation while making its write use the same serialized goal path.
- Added a cross-store regression that blocks one goal mutation while another store records progress, then verifies accounting and progress both survive in `goal.json`.

Validation:

- `go test ./internal/session -run 'TestStoreGoalConcurrentAccountingAndProgressMutationsBothPersist|TestStoreRecordGoalProgressUpdatesMissionValidationAndBudgetWrapUp|TestStoreGoalLifecycleAccountingAndSummary|TestStoreCompleteGoalPersistsAuditAndItemEvidence' -count=1`: passed.
- `go test ./internal/session -count=1`: passed.
- `go test ./internal/runtime -run 'TestEngineBudgetLimitedGoalWrapUpBlocksFinish|TestGoalCompletionGateBlocksActiveGoalAndAllowsCompletedGoal|TestParentCoordinationGateBlocksPendingBackgroundAcceptanceBeforeFinish|TestEngineRunModeStopsAtAwaitingInput' -count=1`: passed.
- `go test ./internal/runtime -count=1`: passed.
- `gofmt -l internal/session/goal.go internal/session/goal_progress.go internal/session/store_test.go`: no output.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-029

Slice: `fix(goal): preserve runtime facts during operator controls`

Changes:

- Added store-owned transactional helpers for goal status changes and mission plan approval.
- Routed Web goal pause/resume/complete and Web mission approval through those helpers.
- Routed CLI goal pause/resume/complete and direct mission approval through the same helpers.
- Routed linked Plan Mode mission approval through the mission approval helper while preserving approval source metadata.
- Added Web and CLI regressions proving operator status changes preserve accounting and progress facts.

Validation:

- `go test ./internal/session -run 'TestStoreGoalConcurrentAccountingAndProgressMutationsBothPersist|TestStoreGoalLifecycleAccountingAndSummary|TestStoreCompleteGoalPersistsAuditAndItemEvidence' -count=1`: passed.
- `go test ./internal/runtime -run 'TestApproveLinkedPlanModeMarksMissionPlanApproved|TestApproveLinkedPlanModeBlocksUncoveredMissionValidation' -count=1`: passed.
- `go test ./internal/webconsole -run 'TestServiceGoalStatusPreservesAccountingAndProgressFacts|TestServiceGoalEndpointsMutateDurableGoal|TestServiceMissionApproveExecutingPlanModeAppendsApprovalFact' -count=1`: passed.
- `go test ./internal/app -run 'TestGoalStatusCommandPreservesAccountingAndProgressFacts|TestGoalMissionPlanAndValidationCommands' -count=1`: passed.
- `go test ./internal/session ./internal/runtime ./internal/webconsole ./internal/app -count=1`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-030

Slice: `fix(goal): merge mission patch task links`

Changes:

- Added a store-owned `PatchGoal` helper for partial Web goal/mission edits.
- Routed Web goal patch and mission plan patch through the transactional patch helper.
- Changed mission plan task sync to merge generated feature IDs and task IDs into the latest goal snapshot instead of full-saving the pre-sync goal.
- Kept task creation outside the goal lock to avoid task graph lock reentrancy.
- Added Web regressions for goal patch and mission patch task sync preserving accounting/progress facts.

Validation:

- `go test ./internal/webconsole -run 'TestServiceGoalPatchPreservesRuntimeProgressFacts|TestServiceMissionPlanPatchTaskSyncPreservesRuntimeProgressFacts|TestServiceMissionPlanPatchResetsApprovedPlanToPendingGate|TestServiceGoalPatchMissionResetsApprovedPlanToPendingGate|TestServiceGoalEndpointsMutateDurableGoal' -count=1`: passed.
- `go test ./internal/session -run 'TestStoreGoalLifecycleAccountingAndSummary|TestStoreGoalConcurrentAccountingAndProgressMutationsBothPersist' -count=1`: passed.
- `go test ./internal/webconsole ./internal/session -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-031

Slice: `fix(planmode): serialize state transitions`

Changes:

- Added `Store.MutatePlanMode` with a session-scoped `planmode.lock`.
- Routed Plan Mode submit/input/answer/approve/executing/revise/cancel transitions through the transactional helper.
- Kept `SavePlanMode` as a validated full replacement path for callers that own the whole snapshot.
- Added a cross-store regression proving approval waits for an in-flight submit mutation and applies to the latest submitted plan version.

Validation:

- `go test ./internal/session -run 'TestPlanModeConcurrentMutationsReadLatestSnapshot|TestPlanModeSubmitApproveAndHistory|TestPlanModeInputValidationAndAnswer' -count=1`: passed.
- `go test ./internal/runtime -run 'TestApproveLinkedPlanModeMarksMissionPlanApproved|TestCancelPlanModeDoesNotDuplicateRecoveredInputToolResult|TestEngineSubmitPlanStopsTurnAndCompletesLaterToolResults' -count=1`: passed.
- `go test ./internal/webconsole -run 'TestServicePlanModeReviseInputAndCancelControls|TestServicePlanModeApproveAppendsReplayableUserMessage|TestServicePlanModeInputDetailKeepsLiveHandle' -count=1`: passed.
- `gofmt -l internal/session/planmode.go internal/session/planmode_test.go`: no output.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-032

Slice: `fix(session): preserve loaded skill state`

Changes:

- Made `SaveState` write through a session-scoped `state.lock`.
- Merged the latest durable `loaded_skills` set into stale state snapshots before writing.
- Added a store regression for stale state saves preserving loaded skill names.
- Added an engine regression proving a `load_skill` tool call remains durable after the next provider turn.
- Updated the runtime test helper to scan configured skill directories so skill-aware engine tests exercise the real registry path.

Validation:

- `go test ./internal/runtime -run 'TestEnginePreservesLoadedSkillStateAcrossNextTurn|TestEngineEmitsContextLoadedEventWithDurableState|TestEngineRunModeStopsAtAwaitingInput' -count=1`: passed.
- `go test ./internal/session -run 'TestStoreSaveStatePreservesCurrentLoadedSkills|TestStoreSaveStateRefreshesUpdatedAt|TestStoreSaveStateIgnoresPredictableTempSymlink|TestStoreLoadStateRejectsSymlinkJSON' -count=1`: passed.
- `go test ./internal/tools -run 'TestLoadSkillReturnsAlreadyLoadedOnRepeatAndForceReload|TestLoadSkillIncludesShellWorkdirHint' -count=1`: passed.
- `gofmt -l internal/session/store.go internal/session/store_test.go internal/runtime/engine_test.go`: no output.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-033

Slice: `fix(session): refresh pending steer count`

Changes:

- Added `Store.RefreshPendingSteerCount` and shared `CountOpenSteerRequests`.
- Routed Runner steer and Engine steer drain/defer paths through the store refresh helper.
- Made `SaveState` derive `pending_steer_count` from `control/steer.jsonl` when the steer queue exists, so later state saves cannot erase queue-derived counts.
- Added store and runtime regressions for concurrent steer append during stale steer update/drain.

Validation:

- `go test ./internal/session -run 'TestUpdateSteerRequestsMergesConcurrentAppend|TestRefreshPendingSteerCountUsesMergedDurableRequests|TestStoreSaveStateRefreshesUpdatedAt|TestStoreSaveStatePreservesCurrentLoadedSkills' -count=1`: passed.
- `go test ./internal/runtime -run 'TestEngineAcceptsPendingSteerBeforeProviderCall|TestEngineRefreshesPendingSteerCountAfterConcurrentAppend|TestRunnerSteerQueuesRequestAndUpdatesPendingCount' -count=1`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `git diff --check`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-034

Slice: `fix(runtime): refresh artifact contract source`

Changes:

- Added latest external instruction source fields to `SessionContract`.
- Made contract equivalence include the source message id and text hash, so a newer same-path artifact instruction rebuilds `contract.json` and `artifact-tracker.json`.
- Added a regression proving a later same-path required artifact instruction blocks finish until the artifact is touched or changed again.

Validation:

- `go test ./internal/runtime -run 'TestContractRefreshResetsArtifactFreshnessForSamePathNewInstruction|TestCompletionControllerRequiresSessionTouchedArtifact|TestSessionContractTracksRequiredArtifactAndCompletionGate' -count=1`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `git diff --check`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-035

Slice: `fix(session): serialize task graph mutations`

Changes:

- Added `Store.MutateTasks` with a session-scoped `tasks/taskboard.lock`.
- Routed `CreateTask` and `UpdateTask` through the transactional helper so ID allocation, dependency synchronization, validation, and graph writes use the latest durable task snapshot.
- Kept `SaveTasks` as a full replacement helper for callers that own the complete graph.
- Added a cross-store regression proving a concurrent task create waits for an in-flight mutation and allocates the next task ID from the latest graph.

Validation:

- `go test ./internal/session -run 'TestTaskMutationsReadLatestGraphUnderLock|TestTask' -count=1`: passed.
- `go test ./internal/tools -run 'TestTodoAndTaskToolsEmitStructuredEvents|TestFeatureListToolsPersistUpdateAndReadSnapshot' -count=1`: passed.
- `go test ./internal/runtime -run 'TestEngineEmitsContextLoadedEventWithDurableState|TestEngineRunModeStopsAtAwaitingInput' -count=1`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `git diff --check`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-036

Slice: `fix(session): canonicalize queue job status files`

Changes:

- Added queue job canonicalization for durable status-file reads, grouping duplicate `_queue/<status>/<job_id>.json` copies by job id.
- Made terminal queue facts win over stale running/queued copies, with status-time tie breakers for non-terminal duplicates.
- Routed both `LoadJob` and `ListJobs` through the same canonical copy selection and stale duplicate cleanup.
- Added a store regression that recreates duplicate running/completed files, verifies terminal detail/list output, and checks stale running cleanup.

Validation:

- `go test ./internal/session -run 'TestLoadAndListJobsPreferTerminalDuplicateStatusFile|TestReconcileCompletedSessionCompletesJob|TestStoreClaimNextQueuedJobIsAtomicAcrossStores' -count=1`: passed.
- `go test ./internal/session -run 'TestLoadAndListJobsPreferTerminalDuplicateStatusFile|TestClaimNextQueuedJobWritesLease|TestClaimNextQueuedJobSkipsMismatchedQueueFilename|TestReconcileCompletedSessionCompletesJob' -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-037

Slice: `fix(app): include blocked jobs in doctor queue scan`

Changes:

- Added the durable `blocked` queue status directory to `doctorQueueStatuses`.
- Added a doctor partial-state regression proving blocked queue jobs with missing linked child sessions are reported.

Validation:

- `go test ./internal/app -run 'TestDoctorReportsBlockedQueueJobMissingSessionRef|TestDoctorReportsQueueLeaseAndMissingSessionRef|TestDoctorReportsDuplicateQueueJobStatus' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/app ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-038

Slice: `fix(runtime): reconcile continued queue children`

Changes:

- Added a session-store reconciliation entry point for a child session's linked queue job.
- Called linked queue reconciliation from runtime terminal and resumable state transitions (`completed`, `failed`, `paused`, `awaiting_input` variants).
- Updated parent coordination parking during store-driven queue reconciliation so completed/failed repaired jobs release parent wait state.
- Changed background notification ensure semantics to refresh changed same-job facts, while leaving unchanged accepted notifications idempotent.
- Added store coverage for refreshing blocked notification facts to completed facts.
- Added runtime coverage for a previously blocked queue child completing and immediately updating queue job, parent coordination, and parent notification facts.

Validation:

- `go test ./internal/session -run 'TestEnsureBackgroundNotificationRefreshesChangedQueueFacts|TestUpdateBackgroundNotificationsMergesConcurrentAppend|TestReconcileCompletedSessionCompletesJob' -count=1`: passed.
- `go test ./internal/runtime -run 'TestEngineCompletingQueuedChildReconcilesParentQueueFacts|TestEngineAcceptsBackgroundResultsBeforeProviderCall|TestParentCoordinationWritesParkedAndResumedEvents' -count=1`: passed.
- `go test ./internal/session -run 'TestEnsureBackgroundNotificationRefreshesChangedQueueFacts|TestUpdateBackgroundNotificationsMergesConcurrentAppend' -count=1`: passed.
- `go test ./internal/runtime -run 'TestEngineCompletingQueuedChildReconcilesParentQueueFacts|TestEngineAcceptsBackgroundResultsBeforeProviderCall' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-039

Slice: `fix(session): preserve refreshed background notifications`

Changes:

- Changed background notification snapshot merging so an accepted stale snapshot only updates delivery status when the current same-job facts are unchanged.
- Kept newer current queue/session/result facts when they differ from the stale update, preserving pending terminal redelivery.
- Added a cross-store regression where a parent accepts a blocked notification snapshot while another store refreshes the same queue job to completed.

Validation:

- `go test ./internal/session -run 'TestUpdateBackgroundNotificationsPreservesConcurrentFactRefresh|TestEnsureBackgroundNotificationRefreshesChangedQueueFacts|TestUpdateBackgroundNotificationsMergesConcurrentAppend' -count=1`: passed.
- `go test ./internal/runtime -run 'TestEngineCompletingQueuedChildReconcilesParentQueueFacts|TestEngineAcceptsBackgroundResultsBeforeProviderCall|TestParentCoordinationWritesParkedAndResumedEvents' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-040

Slice: `fix(webconsole): open jobs from background notifications`

Changes:

- Added `Open job` actions to full background notification cards when a notification has `queue_job_id`.
- Added the same `Open job` action to the recent notification preview in the Summary panel.
- Extended the Node frontend utility test harness to load the session renderer and assert both notification renderers emit `data-open-job` actions.

Validation:

- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `go test ./internal/webconsole -run 'TestServiceEmbeddedAssetsExposeWebFirstConsole|TestServiceQueueWorkersProcessJob|TestServiceParallelQueueWorkersPersistAllJobs' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260525-041

Slice: `fix(session): separate cancelled task board facts`

Changes:

- Split task-board derived facts into separate `completed`, `cancelled`, and combined `done` counters/groups.
- Updated `task_list` metadata to report `cancelled_count` and `done_count` separately from `completed_count`.
- Added session task-board regression coverage for completed vs cancelled counters/groups.
- Added tool metadata coverage and frontend renderer coverage for the Web task metric's cancelled subtitle.

Validation:

- `go test ./internal/session -run 'TestBuildTaskBoardIncludesInProgressGroup|TestBuildTaskBoardSeparatesCompletedAndCancelled' -count=1`: passed.
- `go test ./internal/tools -run 'TestTodoAndTaskToolsEmitStructuredEvents|TestTodoWriteNoopDoesNotLookLikeProgress' -count=1`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260526-042

Slice: `fix(webconsole): hide credential-like workspace files`

Changes:

- Broadened the Web workspace browser deny helper to cover common private-key names, key material extensions, and credential-like JSON filenames.
- Kept `.env.example`, `.env.sample`, and `.env.template` visible while continuing to hide/refuse real `.env` variants.
- Extended the workspace route regression to prove `id_ecdsa`, `credentials.json`, and parent-browsed `deploy.pem` are hidden from listings and rejected by direct reads.

Validation:

- `go test ./internal/webconsole -run TestServiceWorkspaceRoutesListReadAndRejectEscape -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260526-043

Slice: `fix(webconsole): delay api key persistence until config save`

Changes:

- Delayed Web Settings API-key process/env-file mutation until audit-log writability and config persistence have succeeded.
- Added an audit-log writability preflight so sensitive config saves fail before mutating in-memory service config or `.env`.
- Added a regression proving failed config writes do not mutate `OPENAI_API_KEY`, do not persist the submitted secret in `.env`, and do not append a config audit event.

Validation:

- `go test ./internal/webconsole -run 'TestAPIKeyWriteDoesNotLogSecretValue|TestAPIKeyWriteWaitsForConfigWriteSuccess|TestServiceConfigRoutesUpdateActiveConfig' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260526-044

Slice: `fix(webconsole): preflight audit for sensitive actions`

Changes:

- Added audit-log writability preflight before session delete, session history clear, skill upload extraction, and skill uninstall removal.
- Reused the shared no-symlink audit log opener so preflight and real audit writes enforce the same path safety.
- Added a regression proving session delete and skill uninstall do not mutate disk when audit-log preflight fails.

Validation:

- `go test ./internal/webconsole -run 'TestSensitiveActionsPreflightAuditBeforeMutating|TestSensitiveWebActionsEmitAuditEvents|TestAppendAuditEventRejectsSymlinkedAuditLog' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260526-045

Slice: `fix(runtime): separate cancelled task recovery facts`

Changes:

- Replaced runtime's conflated task counter with separated `completed`, `cancelled`, and combined `done` task counts.
- Updated `session.context.loaded`, compaction lifecycle events, compaction summary artifacts, `session.md`, and `checkpoints/longrun-latest.json` to preserve separate cancelled-task facts.
- Added runtime regressions proving session summary, checkpoint, compaction events, and compaction artifacts distinguish completed and cancelled tasks.

Validation:

- `go test ./internal/runtime -run 'TestSessionSummaryAndCheckpointSeparateCancelledTasks|TestCompactorWritesDurableSummaryArtifact' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260526-046

Slice: `fix(webconsole): show provider attempt ledger`

Changes:

- Added a Provider Attempts section to the session Summary inspector when backend `provider_attempts` facts are present.
- Rendered total attempts, retry/auto-resume recovery counts, cache read/create counters, recent outcomes, turn/attempt ids, error class, timeout kind, status code, response id, and cache facts.
- Added frontend renderer coverage proving the Summary panel exposes provider-attempt ledger facts.

Validation:

- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: initially failed because the existing VM harness lacked app-level stubs for `collectRecentToolEntries` and `phaseHeadline`; added narrow stubs, reran, and passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260526-047

Slice: `fix(webconsole): mark linked goal plan launches running`

Changes:

- Added a shared frontend helper to recognize accepted asynchronous session launch responses.
- Updated Goal inspector plan approval so a linked Plan Mode approval response immediately enters the same running UI state used by direct Plan Mode approval.
- Added frontend utility coverage proving accepted launch responses are recognized without misclassifying ordinary goal snapshots.

Validation:

- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260526-048

Slice: `fix(app): show cancelled tasks in cli task view`

Changes:

- Added the `cancelled` group to normal `go-cli-agent tasks <session-id>` text output.
- Normalized separated task-board groups including `in_progress`, `cancelled`, and compatibility `done`.
- Extended the CLI tasks command regression so cancelled tasks are visible without `--all`.

Validation:

- `go test ./internal/app -run 'TestTasksCommandRendersTaskBoard|TestTasksCommandAllRendersFlatTaskList' -count=1`: passed.
- `gofmt -l internal/app/app.go internal/app/app_test.go`: no output.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/app ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260526-049

Slice: `fix(webconsole): preflight api key env writes`

Changes:

- Added a Web Settings API-key env-target preflight before persisting `config.yaml`.
- The preflight rejects missing env keys, symlinked env files, non-regular env files, and existing env-file read/path errors before any config mutation is written.
- Added a regression proving a failed API-key env preflight leaves `config.yaml`, `.env`, process environment, and Web audit log unchanged.

Validation:

- `go test ./internal/webconsole -run 'TestAPIKeyWriteDoesNotLogSecretValue|TestAPIKeyWriteWaitsForConfigWriteSuccess|TestAPIKeyWritePreflightsEnvTargetBeforeConfigWrite|TestServiceConfigRoutesUpdateActiveConfig' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260526-050

Slice: `docs(spec): align task derived views`

Changes:

- Updated stale task derived-view descriptions in `spec/04-tools-and-skills.md`, `spec/08-sdk-and-api-evolution.md`, `spec/12-task-system.md`, and `spec/17-web-console.md`.
- The specs now name separate `completed`, `cancelled`, and combined `done` task facts wherever task derived views or task statistics are described.
- No runtime behavior changed.

Validation:

- `rg -n "ready / blocked / completed|completed 统计|completed 的派生|completed 派生" spec/04-tools-and-skills.md spec/08-sdk-and-api-evolution.md spec/12-task-system.md spec/17-web-console.md`: only updated `ready / blocked / completed / cancelled / done` lines remain.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `go test -timeout 120s ./internal/session ./internal/tools ./internal/webconsole -count=1`: passed.

### FCA-20260526-051

Slice: `fix(webconsole): preflight plan mode actions`

Changes:

- Added Web Plan Mode approval preflight for submitted-plan state before launching the async continue path.
- Added Web Plan Mode revision preflight so revision is only accepted from awaiting approval, rejected, or approved plan states.
- Added regressions proving approve-from-planning and revise-from-awaiting-user-input return conflict without claiming the session, appending messages, or mutating the pending Plan Mode facts.

Validation:

- `go test ./internal/webconsole -run 'TestServicePlanModeApproveAppendsReplayableUserMessage|TestServicePlanModeApproveRejectsPlanningBeforeLaunch|TestServicePlanModeApproveReturnsConflictWhenLinkedMissionCoverageBlocks|TestServicePlanModeReviseRejectsPendingInputBeforeLaunch|TestServicePlanModeReviseInputAndCancelControls' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260526-052

Slice: `fix(runtime): claim plan input waiters before delivery`

Changes:

- Changed active Plan Mode input answer/cancel delivery to delete the waiter from `planInputWaiters` before sending the response.
- Released the Plan Mode input mutex before delivery so duplicate Web submissions or retries return `false` instead of blocking on a full one-slot waiter channel.
- Added a runtime regression proving duplicate active answer and cancel deliveries return promptly while the first response remains available to the waiting runner.

Validation:

- `go test ./internal/runtime -run 'TestActivePlanInputDeliveryClaimsWaiterBeforeSend|TestCancelPlanModeDoesNotDuplicateRecoveredInputToolResult' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260526-053

Slice: `fix(webconsole): reject duplicate skill upload targets`

Changes:

- Planned skill zip extraction targets before removing or creating any managed skill directory.
- Rejected duplicate sanitized target directories inside a single uploaded zip so one skill root cannot overwrite another root in the same package.
- Kept target validation constrained to direct children of the managed skill root and retained symlink destination checks before mutation.
- Added a regression proving duplicate sanitized target names are rejected before extracting files or touching unrelated installed skills.

Validation:

- `go test ./internal/webconsole -run 'TestProcessSkillZipRejectsTraversalEntries|TestProcessSkillZipRejectsSymlinkDestination|TestProcessSkillZipRejectsOversizedEntry|TestProcessSkillZipRejectsDuplicateTargetNamesBeforeMutation|TestProcessSkillZipAllowsNestedSkillFiles|TestServiceSkillRoutesUploadListUninstallAndInstallUnsupported' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260526-054

Slice: `fix(webconsole): block deleting active descendant sessions`

Changes:

- Changed Web session delete preflight to compute the full transitive session tree before checking current-process active handles.
- Reused the same transitive session-tree helper for durable running-session and running-queue delete/clear checks, keeping tree membership consistent across preflights.
- Added a regression proving an active great-grandchild Web handle blocks deletion of an intermediate child session and leaves both target and descendant session facts intact.

Validation:

- `go test ./internal/webconsole -run 'TestServiceDeleteSessionRejectsActiveDeepDescendantHandle|TestServiceDeleteSessionRouteRemovesSessionTreeAndJobs|TestServiceDeleteSessionRejectsRunningSessionWithoutLiveOwner|TestServiceClearSessionsRejectsRunningSessionsWithoutLiveOwners|TestServiceClearSessionsRejectsRunningQueueJobs' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260526-055

Slice: `fix(webconsole): treat released handle clues as settled`

Changes:

- Preserved the latest Web handle event type as internal-only owner metadata in the session detail owner mapping.
- Changed session detail owner mapping so a latest `webconsole.handle.released` clue reports `settled` instead of `running_not_owned` when there is no current-process handle.
- Added a regression proving acquired owner clues still report `running_not_owned`, while a later released clue on the same durable running session reports a settled owner with release metadata.

Validation:

- `go test ./internal/webconsole -run 'TestSessionDetailReportsActiveHandleOwner|TestInterruptNonOwnedSessionReturnsStructuredError|TestStopNonOwnedSessionReturnsStructuredError' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260526-056

Slice: `fix(provider): reject malformed replay tool arguments`

Changes:

- Added a shared helper that validates persisted replay tool-call arguments with the same JSON object contract used for provider-emitted tool calls.
- Changed Anthropic replay reconstruction to return `response_parse_error` before the HTTP request when persisted provider `tool_use` blocks or fallback tool calls contain malformed arguments.
- Changed Google replay reconstruction to return `response_parse_error` before the HTTP request when persisted provider `function_call` blocks or fallback tool calls contain malformed arguments.
- Added provider regressions for malformed persisted provider-native blocks and fallback `session.ToolCall.Arguments`, while preserving compacted valid replay serialization.

Validation:

- `go test ./internal/provider -run 'TestProviderReplaySerializesCompactedProviderBlockToolCalls|TestProviderReplayRejectsMalformedPersistedToolArguments|TestAnthropicAdapterReplaysThinkingBlocks|TestGoogleAdapterReplaysThoughtSignatures' -count=1`: passed.
- `go test ./internal/provider -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260526-057

Slice: `fix(provider): validate openai replay arguments`

Changes:

- Changed OpenAI Responses replay reconstruction to validate persisted `session.ToolCall.Arguments` before serializing `function_call.arguments`.
- Reused the existing OpenAI tool-call argument JSON object contract so corrupted replay facts fail locally as `response_parse_error`.
- Added focused OpenAI replay regressions for invalid JSON and non-object persisted function-call arguments.

Validation:

- `go test ./internal/provider -run 'TestOpenAIInputReplaysEncryptedReasoningBlockSafely|TestOpenAIInputReplaysEmptyReasoningSummaryArray|TestOpenAIInputRejectsMalformedPersistedToolArguments' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./internal/provider -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260526-058

Slice: `fix(session): delete root-linked descendants`

Changes:

- Changed durable session-tree deletion to include sessions whose `root_session_id` points into the deleted tree, matching Web delete preflight and queue-job cleanup semantics.
- Preserved existing parent-link transitive deletion behavior while covering root-linked descendants with missing or drifted parent chains.
- Added a store regression proving deleting the root removes a root-linked descendant session and its root-linked queue job.

Validation:

- `go test ./internal/session -run 'TestDeleteSessionTreeRemovesRootLinkedDescendants|TestDeleteSessionTreeDoesNotDeadlockWithReconcilableJob|TestStoreRejectsPathLikeRecordIDs' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260526-059

Slice: `fix(session): require mission before plan approval`

Changes:

- Changed `ApproveMissionPlan` to reject approval when the current goal has no existing mission plan instead of synthesizing an empty mission and marking it approved.
- Preserved existing approval behavior for real mission plans, including linked Plan Mode approval synchronization.
- Added store, CLI, and Web regressions proving plain-goal mission plan approval is rejected without mutating `goal.json` or appending `mission.plan.approved` history.

Validation:

- `go test ./internal/session -run 'TestApproveMissionPlanRejectsGoalWithoutMissionPlan|TestStoreGoalApprovalCreatesLinkedPlanMode|TestMissionPlanCoverageReportsUncoveredAndInvalidAssignments' -count=1`: passed.
- `go test ./internal/app -run 'TestGoalMissionPlanAndValidationCommands|TestGoalPlanApproveRejectsGoalWithoutMissionPlan' -count=1`: passed.
- `go test ./internal/webconsole -run 'TestServiceMissionApproveExecutingPlanModeAppendsApprovalFact|TestServiceMissionPlanApproveRejectsGoalWithoutMissionPlan|TestServiceGoalFactsAndMissionCoverageApproval' -count=1`: passed.
- `go test ./internal/runtime -run 'TestApproveLinkedPlanModeMarksMissionPlanApproved|TestApproveLinkedPlanModeBlocksUncoveredMissionValidation' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260526-060

Slice: `fix(session): report plan mode history failures`

Changes:

- Changed Plan Mode store transitions to return `AppendPlanModeHistory` errors instead of silently succeeding after updating `planmode.json`.
- Added file-path context to Plan Mode history append errors so failed durable facts are diagnosable from CLI/Web/runtime callers.
- Changed linked goal Plan Mode relinking to report `planmode.linked_goal` history append failures after saving the link.
- Added regressions proving submitted and approved Plan Mode transitions report a blocked `artifacts/planmode-history.jsonl` path.

Validation:

- `go test ./internal/session -run 'TestPlanModeSubmitApproveAndHistory|TestSubmitPlanModeReturnsHistoryAppendError|TestApprovePlanModeReturnsHistoryAppendError|TestPlanModeInputValidationAndAnswer|TestStoreGoalApprovalRelinksExistingPendingPlanMode' -count=1`: passed.
- `go test ./internal/runtime -run 'TestApproveLinkedPlanModeMarksMissionPlanApproved|TestApproveLinkedPlanModeBlocksUncoveredMissionValidation|TestCancelPlanModeDoesNotDuplicateRecoveredInputToolResult' -count=1`: passed.
- `go test ./internal/webconsole -run 'TestServicePlanModeGetAndParentQueueGate|TestServicePlanModeApproveAppendsReplayableUserMessage|TestServicePlanModeReviseInputAndCancelControls' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260526-061

Slice: `fix(session): report goal history failures`

Changes:

- Changed Goal store transitions to return `AppendGoalHistory` errors instead of silently succeeding after updating `goal.json`.
- Added file-path context to Goal history append errors so failed durable facts are diagnosable from CLI/Web/runtime callers.
- Propagated history errors for completion, mission plan approval, accounting, budget limit/budget wrap-up, and structured goal progress/mission validation updates.
- Added regressions proving accounting, completion, and progress transitions report a blocked `artifacts/goal-history.jsonl` path.

Validation:

- `go test ./internal/session -run 'TestStoreGoalLifecycleAccountingAndSummary|TestUpdateGoalAccountingReturnsHistoryAppendError|TestStoreCompleteGoalPersistsAuditAndItemEvidence|TestCompleteGoalReturnsHistoryAppendError|TestStoreRecordGoalProgressUpdatesMissionValidationAndBudgetWrapUp|TestRecordGoalProgressReturnsHistoryAppendError|TestApproveMissionPlanRejectsGoalWithoutMissionPlan' -count=1`: passed.
- `go test ./internal/runtime -run 'TestGoal|TestCompletion|TestBudget|TestApproveLinkedPlanModeMarksMissionPlanApproved|TestApproveLinkedPlanModeBlocksUncoveredMissionValidation' -count=1`: passed.
- `go test ./internal/app -run 'TestGoalMissionPlanAndValidationCommands|TestGoalPlanApproveRejectsGoalWithoutMissionPlan|TestGoalStatusCommandPreservesAccountingAndProgressFacts' -count=1`: passed.
- `go test ./internal/webconsole -run 'TestServiceGoal|TestServiceMissionApproveExecutingPlanModeAppendsApprovalFact|TestServiceMissionPlanApproveRejectsGoalWithoutMissionPlan' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260526-062

Slice: `fix(runtime): report plan input cancel history failures`

Changes:

- Changed recovered Plan Mode input cancellation to return `AppendPlanModeHistory` errors for the `planmode.input_cancelled` fact instead of silently succeeding after appending the replay tool result.
- Preserved idempotent cancellation replay behavior: existing `request_user_input` tool results are still not duplicated.
- Added a runtime regression proving blocked `artifacts/planmode-history.jsonl` is reported during recovered Plan Mode input cancellation.

Validation:

- `go test ./internal/runtime -run 'TestPlanInputCancelReturnsHistoryAppendError|TestCancelPlanModeDoesNotDuplicateRecoveredInputToolResult|TestApproveLinkedPlanModeMarksMissionPlanApproved|TestApproveLinkedPlanModeBlocksUncoveredMissionValidation' -count=1`: passed.
- `go test ./internal/webconsole -run 'TestServicePlanModeReviseInputAndCancelControls|TestServicePlanModeApproveAppendsReplayableUserMessage' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260526-063

Slice: `fix(webconsole): report goal clear history failures`

Finding:

- Web `DELETE /api/sessions/{id}/goal` removed `goal.json` and then ignored failures appending the required `goal.cleared` history/event facts.
- A blocked `artifacts/goal-history.jsonl` path reproduced a false-success response: the API returned `200` with `cleared:true` even though the durable goal history source fact was missing.

Changes:

- Changed Web goal clear to propagate `AppendGoalHistory` failures for `goal.cleared`.
- Changed Web goal clear to propagate the corresponding `goal.cleared` event append failure instead of silently dropping the event fact.
- Added a regression that blocks `artifacts/goal-history.jsonl` and proves the Web API reports the durable fact write failure.

Validation:

- `go test ./internal/webconsole -run 'TestServiceGoalClearReportsHistoryAppendError' -count=1`: failed before the fix with `200` instead of `500`.
- `go test ./internal/webconsole -run 'TestServiceGoalClearReportsHistoryAppendError|TestServiceGoalEndpointsMutateDurableGoal|TestServiceGoalStatusPreservesAccountingAndProgressFacts' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260526-064

Slice: `fix(cli): report goal control history failures`

Finding:

- CLI fallback goal controls mutated `goal.json` and then ignored failures appending the corresponding durable history/event facts.
- Blocked `artifacts/goal-history.jsonl` paths reproduced false-success CLI output for both `goal pause --json` and `goal clear --json`, leaving the operator with a successful command response even though the required goal history source fact was missing.

Changes:

- Changed `goal pause` / `goal resume` / `goal complete` to return `AppendGoalHistory` and event append errors.
- Changed `goal clear` to return `AppendGoalHistory` and event append errors after clearing the goal.
- Added focused CLI regressions for status mutation and clear when `artifacts/goal-history.jsonl` is blocked.

Validation:

- `go test ./internal/app -run 'TestGoalStatusCommandReportsHistoryAppendError|TestGoalClearCommandReportsHistoryAppendError' -count=1`: failed before the fix with nil errors and success JSON.
- `go test ./internal/app -run 'TestGoalStatusCommandReportsHistoryAppendError|TestGoalClearCommandReportsHistoryAppendError|TestGoalStatusCommandPreservesAccountingAndProgressFacts|TestGoalPlanApproveRejectsGoalWithoutMissionPlan' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260526-065

Slice: `fix(runtime): report steer goal history failures`

Finding:

- Runtime accepted pending steer input for sessions with a current goal, appended the steer user message, emitted accepted events, and then ignored `goal.updated` history append failures.
- A blocked `artifacts/goal-history.jsonl` path reproduced a false-success control drain: the engine continued to the provider after accepting steer even though the spec-required goal history fact for accepted steer was missing.

Changes:

- Changed accepted-steer goal history recording to return `AppendGoalHistory` errors.
- Changed engine steer drain to stop and report that error before continuing to the provider.
- Added a regression proving blocked goal history prevents provider execution during steer acceptance.

Validation:

- `go test ./internal/runtime -run 'TestEngineSteerAcceptanceReportsGoalHistoryError' -count=1`: failed before the fix by reaching the provider.
- `go test ./internal/runtime -run 'TestEngineSteerAcceptanceReportsGoalHistoryError|TestEngineAcceptsPendingSteerBeforeProviderCall|TestEngineInterruptSteerCancelsProviderAndContinuesWithAcceptedMessage' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260526-066

Slice: `fix(runtime): report contract history failures`

Finding:

- Runtime contract refresh saved `contract.json` and `artifact-tracker.json`, then ignored failures appending `artifacts/contract-history.jsonl`.
- A blocked `artifacts/contract-history.jsonl` path reproduced a false-success refresh: callers saw no error even though the durable contract history source fact was missing.

Changes:

- Changed `refreshContractForSession` to return `AppendContractHistory` failures.
- Added file-path context to contract history append errors, matching Goal and Plan Mode history diagnostics.
- Added a regression that blocks `artifacts/contract-history.jsonl` and proves contract refresh reports the failed durable fact write.

Validation:

- `go test ./internal/runtime -run 'TestContractRefreshReportsHistoryAppendError' -count=1`: failed before the fix with nil error.
- `go test ./internal/runtime -run 'TestContractRefreshReportsHistoryAppendError|TestSessionContractTracksRequiredArtifactAndCompletionGate|TestContractRefreshEmitsArtifactRequiredEvent|TestEngineSteerAcceptanceReportsGoalHistoryError' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/...`: passed.

### FCA-20260526-067

Slice: `fix(runtime): report provider attempt ledger failures`

Finding:

- Runtime ignored `AppendProviderAttempt` failures while recording provider retry, auto-resume, final failure, and success facts.
- Blocked `provider-attempts.jsonl` reproduced false-progress behavior: retry and success paths continued to `awaiting_input`, auto-resume recalled the provider, and final provider failure returned only the upstream provider error even though the durable provider-attempt ledger write had failed.

Changes:

- Changed provider-attempt recording helpers to return append failures.
- Changed the engine to fail through the existing session failure path when retry, auto-resume, final failure, or success ledger writes fail.
- Cancels the active provider context when a retry callback cannot write the ledger, so execution stops before assistant persistence.
- Added path context to `AppendProviderAttempt` errors.
- Added focused runtime regressions for blocked `provider-attempts.jsonl` on retry, auto-resume, final failure, and success paths.

Validation:

- `go test -timeout 120s ./internal/runtime -run 'TestEngineProvider(Retry|Failure|AutoResume|Success)ReportsProviderAttemptAppendError' -count=1`: failed before the fix with false-progress behavior.
- `go test -timeout 120s ./internal/runtime -run 'TestEngineProvider(Retry|Failure|AutoResume|Success)ReportsProviderAttemptAppendError' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'TestEnginePersistsProviderTurnMetadata|TestEngineProviderParseErrorFailsBeforeAssistantPersist|TestEngineProvider(Retry|Failure|AutoResume|Success)ReportsProviderAttemptAppendError|TestEngineAutoResumesProviderTimeoutBeforeFailing|TestProviderAttemptsLedgerAndLongRunCheckpointAreDurable' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-142

Slice: `fix(session): report corrupt goal during plan mode creation`

Finding:

- `CreatePlanMode` ignored `LoadGoal` errors while discovering whether the new Plan Mode should be linked to the current Goal.
- Before the fix, corrupt `goal.json` allowed creation of an unlinked `planmode.json`.

Changes:

- Changed `CreatePlanMode` to ignore only missing `goal.json`.
- Returned `load goal.json` for corrupt/unreadable Goal snapshots before writing Plan Mode state.
- Added focused store coverage proving failed creation does not leave `planmode.json`.

Validation:

- `go test -timeout 120s ./internal/session -run TestCreatePlanModeReportsCorruptLinkedGoalSnapshot -count=1`: failed before the fix because corrupt `goal.json` was ignored.
- `go test -timeout 120s ./internal/session -run 'Test(CreatePlanModeReportsCorruptLinkedGoalSnapshot|PlanModeSubmitApproveAndHistory|StoreGoalApprovalRelinksExistingPendingPlanMode)' -count=1`: passed.
- `go test -timeout 120s ./internal/session -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/session/planmode.go internal/session/planmode_test.go`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-141

Slice: `fix(runtime): report corrupt steer goal state`

Finding:

- `appendGoalHistoryForSteer` treated every `LoadGoal` error as no current Goal.
- Before the fix, corrupt `goal.json` during steer acceptance skipped the Goal-history decision point and later failed with a raw JSON parser error that did not identify the snapshot file.

Changes:

- Changed `appendGoalHistoryForSteer` to ignore only missing `goal.json`.
- Returned `load goal.json` for corrupt/unreadable Goal snapshots.
- Added focused runtime coverage for corrupt Goal state during steer acceptance.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestEngineSteerAcceptanceReportsCorruptGoalSnapshot -count=1`: failed before the fix because corrupt `goal.json` returned a raw JSON parser error without `goal.json`.
- `go test -timeout 120s ./internal/runtime -run 'TestEngineSteerAcceptanceReports(CorruptGoalSnapshot|GoalHistoryError)' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/goal.go internal/runtime/engine_test.go`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-140

Slice: `fix(runtime): report corrupt goal completion state`

Finding:

- `goalCompletionGate` ignored all `LoadGoal` errors while deciding whether `finish` should be blocked.
- Before the fix, corrupt `goal.json` made `EvaluateToolCall(..., "finish", ...)` return `GateAllow`.

Changes:

- Changed Goal completion gating to ignore only missing `goal.json`.
- Added a `goal_state` block for corrupt/unreadable Goal snapshots.
- Included `goal.json` in the gate message.
- Added focused runtime coverage for corrupt Goal finish state.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestGoalCompletionGateReportsCorruptGoalSnapshot -count=1`: failed before the fix because corrupt `goal.json` allowed `finish`.
- `go test -timeout 120s ./internal/runtime -run TestGoalCompletionGateReportsCorruptGoalSnapshot -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'Test(GoalCompletionGateReportsCorruptGoalSnapshot|GoalCompletionGateBlocksActiveGoalAndAllowsCompletedGoal|GoalCompletionGateRequiresBudgetWrapUpWhenStopOnBudget)' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/completion_controller.go internal/runtime/contract_controller_test.go`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-139

Slice: `fix(runtime): report corrupt parent wait state`

Finding:

- `parentCoordinationGate` ignored corrupt `control/background.jsonl` and `parent-coordination.json` while deciding whether `finish` should be blocked.
- Before the fix, corrupt durable wait-state files made `EvaluateToolCall(..., "finish", ...)` return `GateAllow`.

Changes:

- Changed parent background notification load errors to block finish with `parent_background_state`.
- Changed parent coordination load errors to ignore only missing `parent-coordination.json`.
- Added `parent_coordination_state` blocks for corrupt/unreadable coordination snapshots.
- Added focused runtime regressions for both corrupt wait-state files.

Validation:

- `go test -timeout 120s ./internal/runtime -run 'TestParentCoordinationGateReportsCorrupt(BackgroundNotifications|CoordinationSnapshot)' -count=1`: failed before the fix because both corrupt wait-state files allowed `finish`.
- `go test -timeout 120s ./internal/runtime -run 'TestParentCoordinationGateReportsCorrupt(BackgroundNotifications|CoordinationSnapshot)' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'Test(ParentCoordinationGateReportsCorruptBackgroundNotifications|ParentCoordinationGateReportsCorruptCoordinationSnapshot|ParentCoordinationGateBlocksWaitAllAndAllowsWaitAnyAfterOneCompletion|ParentCoordinationGateBlocksPendingBackgroundAcceptanceBeforeFinish|ParentCoordinationWritesParkedAndResumedEvents)' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/completion_controller.go internal/runtime/contract_controller_test.go`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-138

Slice: `fix(runtime): report corrupt contract artifact state`

Finding:

- Required-artifact write tracking and finish-gate refresh ignored non-missing `contract.json` load errors while mirroring tracker status back to the contract snapshot.
- Before the fix, corrupt `contract.json` was hidden: `TrackToolResult` returned nil, and `finish` could pass when `artifact-tracker.json` was satisfied.

Changes:

- Changed artifact write tracking to ignore only missing `contract.json`.
- Returned `load contract.json` for corrupt/unreadable contract snapshots in `TrackToolResult`.
- Changed `requiredArtifactGate` to block with `required_artifact_state` when contract state cannot be loaded for mirroring.
- Added focused runtime regressions for corrupt `contract.json` in both paths.

Validation:

- `go test -timeout 120s ./internal/runtime -run 'TestCompletionController(TrackToolResultReportsContractLoadError|RequiredArtifactGateReportsContractLoadError)' -count=1`: failed before the fix because corrupt `contract.json` was hidden.
- `go test -timeout 120s ./internal/runtime -run 'TestCompletionController(TrackToolResultReportsContractLoadError|RequiredArtifactGateReportsContractLoadError)' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'TestCompletionController(RequiresSessionTouchedArtifact|TrackToolResultReportsArtifactTrackerError|TrackToolResultReportsContractLoadError|RequiredArtifactGateReportsTrackerRefreshError|RequiredArtifactGateReportsContractLoadError)' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/completion_controller.go internal/runtime/contract_controller_test.go`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-137

Slice: `fix(runtime): fail closed on corrupt plan mode gate state`

Finding:

- `CompletionController.planModeGate` treated every `LoadPlanMode` error as no active Plan Mode gate.
- Before the fix, corrupt `planmode.json` made `EvaluateToolCall` allow `write_file` instead of blocking on the unreadable Plan Mode execution-gate fact.

Changes:

- Changed `planModeGate` to ignore only missing `planmode.json`.
- Added a `plan_mode_state` block for corrupt/unreadable Plan Mode snapshots.
- Included `planmode.json` in the gate message.
- Added focused runtime coverage for corrupt Plan Mode gate state.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestCompletionControllerBlocksWhenPlanModeSnapshotCorrupt -count=1`: failed before the fix because corrupt `planmode.json` allowed `write_file`.
- `go test -timeout 120s ./internal/runtime -run TestCompletionControllerBlocksWhenPlanModeSnapshotCorrupt -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'Test(CompletionControllerBlocksWhenPlanModeSnapshotCorrupt|PlanModeGateBlocksToolsAfterCreateGoalRequiresApproval|EngineSubmitPlanStopsTurnAndCompletesLaterToolResults)' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/completion_controller.go internal/runtime/planmode_test.go`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-068

Slice: `fix(runtime): report budget wrap-up history failures`

Finding:

- Runtime marked `BudgetWrapUpTurnStartedAt` in `goal.json` and then ignored failures appending `goal.budget_wrapup_turn_started` to `artifacts/goal-history.jsonl`.
- A blocked `artifacts/goal-history.jsonl` path reproduced false progress: the engine called the provider after mutating the goal snapshot even though the durable turn-start history fact was missing.

Changes:

- Changed the budget wrap-up turn-start path to return `AppendGoalHistory` failures.
- Stops through the existing session failure path before emitting `goal.budget_wrapup_turn_started` or calling the provider.
- Added a focused runtime regression for blocked goal history at budget wrap-up turn start.

Validation:

- `go test -timeout 120s ./internal/runtime -run 'TestEngineBudgetWrapUpTurnStartReportsGoalHistoryError' -count=1`: failed before the fix by reaching the provider.
- `go test -timeout 120s ./internal/runtime -run 'TestEngineBudgetWrapUpTurnStartReportsGoalHistoryError|TestEngineBudgetWrapUpThenFinishAwaitsInput|TestGoalCompletionGateRequiresBudgetWrapUpWhenStopOnBudget' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-069

Slice: `fix(runtime): report artifact tracking failures`

Finding:

- Required-artifact tracking updated `artifact-tracker.json` after successful `write_file` / `edit_file`, but `TrackToolResult` could not return tracker write failures and ignored `contract.json` sync failures.
- A blocked `artifact-tracker.json` path reproduced a silent tracking failure after the required artifact file had been written.
- The engine also needed to keep provider replay complete when this post-side-effect failure occurred, by appending an error tool result for the executed write and synthetic skipped results for later same-turn calls.

Changes:

- Changed `TrackToolResult` to return artifact tracker and contract sync errors.
- Added path context to `SaveArtifactTracker` errors.
- Changed engine tool execution to stamp tool result id/name before artifact tracking, append replay-complete error results on tracking failure, and fail the session.
- Added focused controller and engine regressions for blocked `artifact-tracker.json`.

Validation:

- `go test -timeout 120s ./internal/runtime -run 'TestCompletionControllerTrackToolResultReportsArtifactTrackerError' -count=1`: failed before the fix because `TrackToolResult` had no error return.
- `go test -timeout 120s ./internal/runtime -run 'TestCompletionControllerTrackToolResultReportsArtifactTrackerError|TestEngineArtifactTrackingFailureWritesReplayCompleteToolResult|TestCompletionControllerRequiresSessionTouchedArtifact|TestContractRefreshResetsArtifactFreshnessForSamePathNewInstruction|TestSessionContractTracksRequiredArtifactAndCompletionGate' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-070

Slice: `fix(runtime): block finish on artifact state failures`

Finding:

- Required-artifact finish gate ignored failures loading or saving refreshed `artifact-tracker.json` and ignored `contract.json` sync failures.
- A blocked `artifact-tracker.json` path reproduced a false allow: `EvaluateToolCall("finish")` returned `allow` even though the gate could not load or persist the durable artifact state.

Changes:

- Changed required-artifact gate to block as `required_artifact_state` on artifact tracker load failures.
- Changed finish-gate refresh to block as `required_artifact_state` when refreshed artifact tracker or contract state cannot be saved.
- Added a focused finish-gate regression for blocked `artifact-tracker.json`.

Validation:

- `go test -timeout 120s ./internal/runtime -run 'TestCompletionControllerRequiredArtifactGateReportsTrackerRefreshError' -count=1`: failed before the fix by allowing `finish`.
- `go test -timeout 120s ./internal/runtime -run 'TestCompletionControllerRequiredArtifactGateReportsTrackerRefreshError|TestCompletionControllerTrackToolResultReportsArtifactTrackerError|TestCompletionControllerRequiresSessionTouchedArtifact|TestContractRefreshResetsArtifactFreshnessForSamePathNewInstruction|TestRequiredArtifactGateRejectsSymlinkedArtifactAfterContractCreation|TestSessionContractTracksRequiredArtifactAndCompletionGate' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-071

Slice: `fix(cli): report mission approval event failures`

Finding:

- CLI `goal plan approve` ignored the `mission.plan.approved` event append error after `ApproveMissionPlan` had already updated `goal.json` and goal history.
- A blocked `events.jsonl` path reproduced false success: the command returned approved-goal JSON with no durable event fact.

Changes:

- Changed CLI mission plan approval to return `AppendEvent` failures.
- Added path context to `AppendEvent` errors.
- Added a focused CLI regression for blocked `events.jsonl` during mission plan approval.

Validation:

- `go test -timeout 120s ./internal/app -run 'TestGoalPlanApproveCommandReportsEventAppendError' -count=1`: failed before the fix with success JSON.
- `go test -timeout 120s ./internal/app -run 'TestGoalPlanApproveCommandReportsEventAppendError|TestGoalMissionPlanAndValidationCommands|TestGoalPlanApproveRejectsGoalWithoutMissionPlan|TestGoalStatusCommandReportsHistoryAppendError|TestGoalClearCommandReportsHistoryAppendError' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-072

Slice: `fix(runtime): report failure fact write errors`

Finding:

- Runtime pre-engine and direct provider failure paths ignored required failure fact write errors.
- `Runner.failBeforeRun` hid `state.json` write failures and `session.failed` event append failures after a pre-engine error, so a failed `Continue` could leave the session durably `running` or missing its failed event.
- `Engine.Run` direct provider failures ignored the same failed-state and failed-event write failures before recording provider attempts.
- `Engine.fail` returned failed-state save errors but still ignored `session.failed` event append failures.

Changes:

- Added error-returning event append helpers for runner/engine failure transitions.
- Changed `Runner.failBeforeRun` to return failed-state and failed-event write errors with the original pre-engine error preserved in context.
- Changed the direct provider failure branch to return failed-state and failed-event write errors before writing provider failure attempts.
- Changed `Engine.fail` to return `session.failed` event append errors.
- Added focused runner and engine regressions for blocked `state.json` and `events.jsonl` failure facts.

Validation:

- `go test -timeout 120s ./internal/runtime -run 'TestRunnerFailBeforeRunReports(StateSave|FailedEventAppend)Error|TestEngineProviderFailureReports(StateSave|FailedEventAppend)Error|TestEngineFailReportsFailedEventAppendError' -count=1`: failed before the fix with original hook/provider errors and missing durable failure fact diagnostics.
- `go test -timeout 120s ./internal/runtime -run 'TestRunnerFailBeforeRunReports(StateSave|FailedEventAppend)Error|TestEngineProviderFailureReports(StateSave|FailedEventAppend)Error|TestEngineFailReportsFailedEventAppendError' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'TestRunnerContinueClaimsSessionBeforeUserMessageHook|TestRunnerFailBeforeRunReports(StateSave|FailedEventAppend)Error|TestEngineProvider(FailureReportsProviderAttemptAppendError|FailureReportsStateSaveError|FailureReportsFailedEventAppendError|RetryReportsProviderAttemptAppendError|AutoResumeReportsProviderAttemptAppendError|SuccessReportsProviderAttemptAppendError)|TestEngineProviderParseErrorFailsBeforeAssistantPersist|TestEngineAutoResumesProviderTimeoutBeforeFailing' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-073

Slice: `fix(runtime): report plan input state failures`

Finding:

- `request_user_input` persisted the Plan Mode pending request, then ignored failures loading `state.json` and ignored failures saving `state.status=awaiting_input` / `phase=plan_input`.
- Blocked `state.json` and blocked `control/steer.jsonl` regressions reproduced false success: the tool still called the interactive responder and returned a successful answer payload even though the durable awaiting-input state transition had failed.

Changes:

- Changed `request_user_input` to return a model-visible tool error when `LoadState` fails after the pending request is persisted.
- Changed `request_user_input` to return a model-visible tool error when saving the awaiting-input state fails.
- Moved `planmode.input_requested` emission and responder invocation after the pending request and awaiting-input session state are both durable.
- Added focused Plan Mode input regressions for state load and state save failures before responder invocation.

Validation:

- `go test -timeout 120s ./internal/tools -run 'TestRequestUserInputReportsState(Load|Save)ErrorBeforeResponder' -count=1`: failed before the fix with successful answer payloads.
- `go test -timeout 120s ./internal/tools -run 'TestRequestUserInputReportsState(Load|Save)ErrorBeforeResponder|TestRequestUserInputResponderErrorKeepsRecoverablePendingRequest|TestRequestUserInputWithoutResponderFailsBeforePendingRequest' -count=1`: passed.
- `go test -timeout 120s ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime ./internal/tools -run 'TestRequestUserInput|TestRunnerFailBeforeRunReports|TestEngineProviderFailureReports|TestEngineFailReportsFailedEventAppendError|TestPlanMode' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-074

Slice: `fix(webconsole): disable skill upload while pending`

Finding:

- The skill upload file-input handler sent the multipart upload request without disabling the header upload button, empty-state upload button, card upload entry points, or hidden file input.
- Focused frontend evidence failed before the fix: the Node renderer test had no `setSkillUploadPending` helper, and the embedded asset contract found no `state.skillUploadInFlight` / `setSkillUploadPending` usage in `app.js`.

Changes:

- Added `setSkillUploadPending` to disable, mark `aria-busy`, relabel, and restore all skill upload entry points.
- Added `state.skillUploadInFlight` and `openSkillUploadPicker` to guard repeated upload clicks and duplicate file-input changes.
- Wrapped the multipart upload request in pending state and restored controls in `finally`, keeping existing backend error toast behavior.
- Added focused Node helper and embedded asset contract regressions.

Validation:

- `node validation/scripts/webconsole_utils_test.mjs`: failed before the fix because `setSkillUploadPending` was missing.
- `go test -timeout 120s ./internal/webconsole -run TestServiceServesEmbeddedShellAndAssets -count=1`: failed before the fix because `app.js` lacked `state.skillUploadInFlight` and `setSkillUploadPending`.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./internal/webconsole -run TestServiceServesEmbeddedShellAndAssets -count=1`: passed.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/utils.js`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-075

Slice: `fix(tools): report load_skill state failures`

Finding:

- `load_skill` returned the full skill body and success result even when recording `state.loaded_skills` failed.
- A blocked `control/steer.jsonl` regression forced `SaveState` to fail through the pending-steer refresh path; before the fix, `load_skill` still returned the full skill body while the durable loaded-skill fact was missing.

Changes:

- Changed `markSkillLoaded` to return `LoadState` and `SaveState` failures when a session store/id is present.
- Changed `load_skill` to return a model-visible error instead of the full skill body when the loaded-skill state fact cannot be persisted.
- Preserved no-op behavior for registry uses without a session store or session id.
- Added a focused blocked-state regression for loaded-skill fact persistence.

Validation:

- `go test -timeout 120s ./internal/tools -run TestLoadSkillReportsLoadedSkillStateSaveError -count=1`: failed before the fix with a successful full skill body.
- `go test -timeout 120s ./internal/tools -run 'TestLoadSkillReportsLoadedSkillStateSaveError|TestLoadSkillReturnsAlreadyLoadedOnRepeatAndForceReload' -count=1`: passed.
- `go test -timeout 120s ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run TestEnginePreservesLoadedSkillStateAcrossNextTurn -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-076

Slice: `fix(runtime): report queue reconcile write failures`

Finding:

- Queue/job reconciliation returned repaired in-memory facts even when linked durable facts could not be written.
- A focused store regression blocked the linked child `SaveState` path; before the fix, `LoadJob` returned a failed repaired job while the child `state.json` remained running.
- A focused runtime regression blocked the completed queue-job file path during queued child completion; before the fix, `Engine.Run` still returned `completed` even though the parent queue facts could not be reconciled.

Changes:

- Changed queue reconciliation to return linked child `SaveState` failures and repaired queue `SaveJob` failures.
- Propagated reconciliation errors through `LoadJob`, queue listing, session list page, and child listing.
- Propagated linked queue reconciliation errors from engine terminal transitions after the child session state/event facts are written.
- Kept missing queue job files as a no-op for metadata-only queue IDs, while still returning malformed/unwritable queue fact errors.
- Added focused store and runtime regressions.

Validation:

- `go test -timeout 120s ./internal/session -run TestLoadJobReportsLinkedSessionStateSaveError -count=1`: failed before the fix with a successful repaired job while child state remained running.
- `go test -timeout 120s ./internal/runtime -run TestEngineReportsLinkedQueueJobReconcileSaveError -count=1`: failed before the fix with `Status:"completed"`.
- `go test -timeout 120s ./internal/session ./internal/runtime -run 'TestLoadJobReportsLinkedSessionStateSaveError|TestEngineReportsLinkedQueueJobReconcileSaveError|TestEngineCompletingQueuedChildReconcilesParentQueueFacts' -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-077

Slice: `fix(runtime): report parent coordination write failures`

Finding:

- Parent-linked queue operations ignored `parent-coordination.json` write failures.
- A focused `QueueSubmit` regression blocked `parent-coordination.json`; before the fix, the API returned a queued job while the parent coordination source fact was not written.
- A focused `ProcessNextJob` regression blocked `parent-coordination.json` after queue submission; before the fix, the worker returned a completed job while the parent coordination source fact stayed stale/unwritable.

Changes:

- Propagated `addParentQueueJob` errors from `SpawnAgent` background mode and `QueueSubmit`.
- Propagated `resolveParentQueueJob` errors from `ProcessNextJob` after terminal job and background notification persistence.
- Changed store queue repair to return parent coordination mutation failures while keeping queue lifecycle event repair best-effort.
- Added focused queue submit and worker regressions for blocked parent coordination writes.

Validation:

- `go test -timeout 120s ./internal/runtime -run 'TestRunnerQueueSubmitReportsParentCoordinationError|TestRunnerProcessNextJobReportsParentCoordinationError' -count=1`: failed before the fix with successful queued/completed jobs.
- `go test -timeout 120s ./internal/runtime -run 'TestRunnerQueueSubmitReportsParentCoordinationError|TestRunnerProcessNextJobReportsParentCoordinationError|TestRunnerQueueSubmitAndWorkerCompletesJob' -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -run 'TestReconcileFailedJobUpdatesLinkedRunningSession|TestReconcileCompletedSessionCompletesJob|TestLoadJobRepairsMissingTerminalBackgroundNotification|TestRunnerQueueSubmit|TestRunnerProcessNextJob|TestProcessNextJob|TestParentCoordination' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-078

Slice: `fix(runtime): report delegate coordination failures`

Finding:

- Synchronous delegate ignored parent child coordination write failures.
- A focused regression blocked `parent-coordination.json`; before the fix, `Delegate` returned a completed child result even though parent coordination could not record or resolve the child session.

Changes:

- Propagated `addParentChildSession` and `resolveParentChildSession` errors from synchronous delegate after the child session id is known.
- Preserved the child runner's original error if child execution and later coordination both fail.
- Added a focused delegate regression for blocked parent coordination writes.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestRunnerDelegateReportsParentCoordinationError -count=1`: failed before the fix with a completed child result.
- `go test -timeout 120s ./internal/runtime -run 'TestRunnerDelegateReportsParentCoordinationError|TestRunnerDelegateCreatesChildSessionWithIsolation|TestRunnerDelegateTreatsNoneIsolationModeAsOff' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: passed with no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test --timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-079

Slice: `fix(runtime): require provider cancellation event persistence`

Finding:

- Provider-call pause / interrupt-steer cancellation wrote required `provider.cancelled` evidence through the best-effort event helper.
- A focused regression blocked `events.jsonl`; before the fix, interrupt-steer cancellation returned `awaiting_input` without persisting the required durable cancellation event.

Changes:

- Added an error-returning `appendProviderCancelled` helper.
- Propagated `provider.cancelled` append failures from provider pause and interrupt-steer cancellation branches.
- Added a focused provider cancellation event append regression.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestEngineProviderCancellationReportsCancelledEventAppendError -count=1`: failed before the fix with `awaiting_input` and no append error.
- `go test -timeout 120s ./internal/runtime -run 'TestEngineProviderCancellationReportsCancelledEventAppendError|TestEngineInterruptSteerCancelsProviderAndContinuesWithAcceptedMessage|TestEngineInterruptSteerDeferredFinishLeavesSessionAwaitingInput|TestEngineInterruptSteerDuringToolDefersUntilNextTurn' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: passed with no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-080

Slice: `fix(runtime): require provider auto-resume event persistence`

Finding:

- Runtime auto-resume after `upstream_timeout` wrote required `provider.auto_resume` evidence through the best-effort event helper.
- A focused regression blocked `events.jsonl`; before the fix, auto-resume recalled the provider and completed the session without the required durable event.

Changes:

- Added an error-returning `appendProviderAutoResume` helper.
- Propagated `provider.auto_resume` append failures before the auto-resume harness reminder and next provider call.
- Reused one shared provider auto-resume event-data builder.
- Added a focused provider auto-resume event append regression.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestEngineProviderAutoResumeReportsEventAppendError -count=1`: failed before the fix with a completed session and no append error.
- `go test -timeout 120s ./internal/runtime -run 'TestEngineProviderAutoResumeReportsEventAppendError|TestEngineProviderAutoResumeReportsProviderAttemptAppendError|TestEngineAutoResumesProviderTimeoutBeforeFailing' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l cmd internal pkg validation/cmd`: passed with no output.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-081

Slice: `fix(runtime): require provider retry event persistence`

Finding:

- Provider retry callbacks wrote required `provider.retry` evidence through the best-effort event helper before recording the hardened provider-attempt ledger.
- A focused regression blocked `events.jsonl`; before the fix, the retry path returned `awaiting_input` with assistant text without persisting the required retry event.

Changes:

- Added an error-returning `appendProviderRetry` helper.
- Propagated `provider.retry` append failures from the provider callback before provider-attempt ledger writes and assistant persistence.
- Kept non-retry provider callback events on the best-effort timeline path.
- Added a focused provider retry event append regression.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestEngineProviderRetryReportsEventAppendError -count=1`: failed before the fix with `awaiting_input` and no append error.
- `go test -timeout 120s ./internal/runtime -run 'TestEngineProviderRetryReportsEventAppendError|TestEngineProviderRetryReportsProviderAttemptAppendError|TestEngineRecordsProviderParseFailureAttempt|TestEngineAutoResumesProviderTimeoutBeforeFailing' -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-082

Slice: `fix(runtime): require steer queued event persistence`

Finding:

- Running-session steer submission wrote the durable control request and pending counter before emitting required `session.steer.requested` / `session.steer.queued` events through the best-effort helper.
- A focused regression blocked `events.jsonl`; before the fix, `Runner.Steer` returned accepted `queued` and left the steer pending without persisting the required queued timeline evidence.

Changes:

- Switched initial steer submission events to the error-returning append path.
- Marked the just-created steer request `rejected` and refreshed `pending_steer_count` when required submission event persistence fails after the control record is written.
- Kept later steer runtime events unchanged in this slice.
- Added a focused blocked-`events.jsonl` steer submission regression.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestRunnerSteerReportsQueuedEventAppendError -count=1`: failed before the fix with accepted `queued` and no append error.
- `go test -timeout 120s ./internal/runtime -run 'TestRunnerSteerReportsQueuedEventAppendError|TestRunnerSteerReturnsQueuedBehaviorForRunningSession' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -w internal/runtime/runner.go internal/runtime/runner_test.go`: applied formatting with no residual diff.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-083

Slice: `fix(session): roll back failed plan mode history transitions`

Finding:

- Plan Mode store helpers returned required `planmode-history.jsonl` append errors, but they mutated `planmode.json` before the failed history append.
- A focused regression blocked `planmode-history.jsonl`; before the fix, failed submit returned an error while leaving the current snapshot in `awaiting_approval` with a submitted plan version and derived plan Markdown.

Changes:

- Added rollback snapshots for Plan Mode current facts before history-backed transitions.
- Restored the previous `planmode.json` when a required history append fails after mutation.
- Restored or removed `artifacts/planmode-plan.md` when submit history append fails after writing the derived plan file.
- Applied rollback to Plan Mode creation and linked-goal relink paths.
- Extended focused submit and approval history-failure regressions to assert current snapshot rollback.

Validation:

- `go test -timeout 120s ./internal/session -run TestSubmitPlanModeReturnsHistoryAppendError -count=1`: failed before the fix because failed submit left `planmode.json` advanced to `awaiting_approval`.
- `go test -timeout 120s ./internal/session -run 'TestSubmitPlanModeReturnsHistoryAppendError|TestApprovePlanModeReturnsHistoryAppendError|TestStoreGoalApprovalRelinksExistingPendingPlanMode' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/session/planmode.go internal/session/goal.go internal/session/planmode_test.go`: passed with no output.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-103

Slice: `fix(runtime): roll back failed plan input replay`

Finding:

- Runtime recovered Plan Mode input answers cleared the pending request and appended `planmode.input_answered` history before appending the required recovered `request_user_input` tool result to `messages.jsonl`.
- With `messages.jsonl` blocked, the runtime returned an error but left Plan Mode in `planning` with no pending request and no replayable tool result.

Changes:

- Snapshotted Plan Mode state and history before recovered Plan Mode input answers.
- Restored both snapshots if appending the recovered tool-result message fails.
- Added `Store.RestorePlanModeHistory` for restoring `artifacts/planmode-history.jsonl` as part of composed runtime rollback.
- Added a focused runtime regression for blocked `messages.jsonl` during recovered Plan Mode input answer.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestPlanInputAnswerRollsBackWhenToolResultAppendFails -count=1`: failed before the fix because failed message append cleared the pending request.
- `go test -timeout 120s ./internal/runtime -run TestPlanInputAnswerRollsBackWhenToolResultAppendFails -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'Test(PlanInputAnswerRollsBackWhenToolResultAppendFails|PlanInputCancelReturnsHistoryAppendError|CancelPlanModeDoesNotDuplicateRecoveredInputToolResult|CancelPlanModeReportsCancelledEventAppendError|ApprovePlanModeReportsPlanApprovedEventAppendError|ApproveLinkedMissionPlanReportsEventAppendError)' -count=1`: passed.
- `go test -timeout 120s ./internal/session -run 'Test(PlanModeInputValidationAndAnswer|SubmitPlanModeReturnsHistoryAppendError|ApprovePlanModeReturnsHistoryAppendError|PlanModeSubmitApproveAndHistory|RestorePlanModeSnapshotRemovesCreatedPlanMode)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/session/planmode.go internal/runtime/runner.go internal/runtime/planmode_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-104

Slice: `fix(runtime): retry failed plan approval replay`

Finding:

- Runtime Plan Mode approval could persist approval/execution and linked mission facts before appending the required `meta.source=planmode_approval` user message to `messages.jsonl`.
- With `messages.jsonl` blocked, the first approval failed before provider execution and left no replayable approval message. A later approval retry then failed because the Plan Mode snapshot was already `executing`.

Changes:

- Added an idempotent runtime approval helper that accepts `awaiting_approval`, `approved`, and `executing` Plan Mode states.
- Re-appends missing required Plan Mode approval/execution events only when the same Plan Mode id and approved version are absent.
- Allows approval retry to append the missing replayable approval user message and continue without re-mutating Plan Mode history.
- Added focused runtime coverage for blocked `messages.jsonl` during approval and retry after restoring the path.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestApprovePlanModeRetryAfterApprovalMessageFailureAppendsApprovalMessage -count=1`: failed before the fix with `plan mode is not awaiting approval: executing`.
- `go test -timeout 120s ./internal/runtime -run TestApprovePlanModeRetryAfterApprovalMessageFailureAppendsApprovalMessage -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'Test(ApprovePlanModeRetryAfterApprovalMessageFailureAppendsApprovalMessage|ApprovePlanModeReportsPlanApprovedEventAppendError|ApproveLinkedMissionPlanReportsEventAppendError|ApproveLinkedPlanModeMarksMissionPlanApproved|ApproveLinkedPlanModeBlocksUncoveredMissionValidation|CancelPlanModeReportsCancelledEventAppendError|PlanInputAnswerRollsBackWhenToolResultAppendFails|PlanInputCancelReturnsHistoryAppendError)' -count=1`: passed.
- `go test -timeout 120s ./internal/session -run 'Test(PlanModeInputValidationAndAnswer|SubmitPlanModeReturnsHistoryAppendError|ApprovePlanModeReturnsHistoryAppendError|PlanModeSubmitApproveAndHistory|RestorePlanModeSnapshotRemovesCreatedPlanMode)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/runner.go internal/runtime/planmode_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-105

Slice: `fix(runtime): retry failed plan revision replay`

Finding:

- Runtime Plan Mode revision could persist `planmode.plan_revised` and move `planmode.json` back to `planning` before appending the required `meta.source=planmode_revision` user message.
- With `messages.jsonl` blocked, retrying the same revision appended an ordinary user message and continued execution without restoring the replay metadata.

Changes:

- Added an idempotent runtime revision helper that recognizes a planning-state retry only when the latest matching `planmode.plan_revised` history row has the same Plan Mode id, plan version, and revision text.
- The recovery branch also requires the replayable `planmode_revision` user message to still be absent, preventing a later duplicate plain continuation from being reclassified after a successful retry.
- Re-appends a missing `planmode.plan_revised` event only for the same Plan Mode id/version.
- Preserves `meta.source=planmode_revision` on retry instead of treating the message as a plain continuation.
- Added focused runtime coverage for blocked `messages.jsonl` during revision, retry after restoring the path, and repeated same-text planning continuation after the replay message already exists.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestRevisePlanModeRetryAfterRevisionMessageFailureAppendsRevisionMessage -count=1`: failed before the fix because retry wrote no `planmode_revision` user message.
- `go test -timeout 120s ./internal/runtime -run 'Test(RevisePlanModeRetryAfterRevisionMessageFailureAppendsRevisionMessage|ApprovePlanModeRetryAfterApprovalMessageFailureAppendsApprovalMessage|ApprovePlanModeReportsPlanApprovedEventAppendError|CancelPlanModeReportsCancelledEventAppendError)' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'Test(RevisePlanModeRetryAfterRevisionMessageFailureAppendsRevisionMessage|ApprovePlanModeRetryAfterApprovalMessageFailureAppendsApprovalMessage|ApprovePlanModeReportsPlanApprovedEventAppendError|ApproveLinkedMissionPlanReportsEventAppendError|ApproveLinkedPlanModeMarksMissionPlanApproved|ApproveLinkedPlanModeBlocksUncoveredMissionValidation|CancelPlanModeReportsCancelledEventAppendError|PlanInputAnswerRollsBackWhenToolResultAppendFails|PlanInputCancelReturnsHistoryAppendError)' -count=1`: passed.
- `go test -timeout 120s ./internal/session -run 'Test(PlanModeInputValidationAndAnswer|SubmitPlanModeReturnsHistoryAppendError|ApprovePlanModeReturnsHistoryAppendError|PlanModeSubmitApproveAndHistory|RestorePlanModeSnapshotRemovesCreatedPlanMode)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/runner.go internal/runtime/planmode_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-106

Slice: `fix(runtime): retry failed plan cancellation event replay`

Finding:

- Runtime Plan Mode cancellation could persist `planmode.cancelled` history and move `planmode.json` to `cancelled` before appending the required `planmode.cancelled` runtime event.
- With `events.jsonl` blocked, retrying the same cancellation called `CancelPlanMode` again and duplicated the durable cancellation history row.

Changes:

- Added an idempotent runtime cancellation helper that recognizes already-cancelled Plan Mode state.
- Re-appends the missing `planmode.cancelled` event once without re-running the store cancellation transition.
- Preserves existing pending `request_user_input` cancellation tool-result de-duplication.
- Added focused runtime coverage for blocked `events.jsonl` during cancellation, retry after restoring the path, one durable cancellation history row, and one restored cancellation event.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestCancelPlanModeRetryAfterCancelledEventFailureDoesNotDuplicateHistory -count=1`: failed before the fix because retry duplicated `planmode.cancelled` history.
- `go test -timeout 120s ./internal/runtime -run TestCancelPlanModeRetryAfterCancelledEventFailureDoesNotDuplicateHistory -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'Test(CancelPlanModeRetryAfterCancelledEventFailureDoesNotDuplicateHistory|CancelPlanModeDoesNotDuplicateRecoveredInputToolResult|CancelPlanModeReportsCancelledEventAppendError|PlanInputCancelReturnsHistoryAppendError|PlanInputAnswerRollsBackWhenToolResultAppendFails)' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'Test(CancelPlanModeRetryAfterCancelledEventFailureDoesNotDuplicateHistory|CancelPlanModeDoesNotDuplicateRecoveredInputToolResult|CancelPlanModeReportsCancelledEventAppendError|PlanInputCancelReturnsHistoryAppendError|PlanInputAnswerRollsBackWhenToolResultAppendFails|RevisePlanModeRetryAfterRevisionMessageFailureAppendsRevisionMessage|ApprovePlanModeRetryAfterApprovalMessageFailureAppendsApprovalMessage)' -count=1`: passed.
- `go test -timeout 120s ./internal/session -run 'Test(PlanModeInputValidationAndAnswer|SubmitPlanModeReturnsHistoryAppendError|ApprovePlanModeReturnsHistoryAppendError|PlanModeSubmitApproveAndHistory|RestorePlanModeSnapshotRemovesCreatedPlanMode)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/runner.go internal/runtime/planmode_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-107

Slice: `fix(runtime): retry failed plan input cancellation facts`

Finding:

- Recovered Plan Mode input cancellation could append the required `request_user_input` tool result before failing to append `planmode.input_cancelled` history.
- Retrying then returned early because the tool result already existed, leaving Plan Mode history and runtime events unrepaired.

Changes:

- Split recovered input cancellation into independently idempotent message, history, and event repair steps.
- Kept tool-result de-duplication while allowing missing history/event facts to be restored on retry.
- De-duplicates `planmode.input_cancelled` history by Plan Mode id, request id, and tool call id.
- De-duplicates `planmode.input_cancelled` event by Plan Mode id and request id.
- Added focused runtime coverage for blocked history append, retry after restoring the path, one replay tool result, one input-cancel history row, and one input-cancel event.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestPlanInputCancelRetryAfterHistoryFailureRestoresFacts -count=1`: failed before the fix because retry restored no `planmode.input_cancelled` history.
- `go test -timeout 120s ./internal/runtime -run TestPlanInputCancelRetryAfterHistoryFailureRestoresFacts -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'Test(PlanInputCancelRetryAfterHistoryFailureRestoresFacts|CancelPlanModeDoesNotDuplicateRecoveredInputToolResult|PlanInputCancelReturnsHistoryAppendError|CancelPlanModeRetryAfterCancelledEventFailureDoesNotDuplicateHistory|CancelPlanModeReportsCancelledEventAppendError|PlanInputAnswerRollsBackWhenToolResultAppendFails)' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'Test(PlanInputCancelRetryAfterHistoryFailureRestoresFacts|CancelPlanModeDoesNotDuplicateRecoveredInputToolResult|PlanInputCancelReturnsHistoryAppendError|CancelPlanModeRetryAfterCancelledEventFailureDoesNotDuplicateHistory|CancelPlanModeReportsCancelledEventAppendError|PlanInputAnswerRollsBackWhenToolResultAppendFails|RevisePlanModeRetryAfterRevisionMessageFailureAppendsRevisionMessage|ApprovePlanModeRetryAfterApprovalMessageFailureAppendsApprovalMessage)' -count=1`: passed.
- `go test -timeout 120s ./internal/session -run 'Test(PlanModeInputValidationAndAnswer|SubmitPlanModeReturnsHistoryAppendError|ApprovePlanModeReturnsHistoryAppendError|PlanModeSubmitApproveAndHistory|RestorePlanModeSnapshotRemovesCreatedPlanMode)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/runner.go internal/runtime/planmode_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-108

Slice: `fix(runtime): retry failed plan input answer event`

Finding:

- Recovered Plan Mode input answers could persist Plan Mode state/history and append the `request_user_input` tool result before failing to append `planmode.input_answered`.
- Retrying the same answer then failed because the pending input request had already been cleared, so the missing runtime event could not be restored.

Changes:

- Added an idempotent answer-event recovery path that matches existing `planmode.input_answered` history by request id and answer payload.
- Recovers the original tool call id from matching `planmode.input_requested` history and requires the replay tool result to exist before treating the retry as recovered.
- Re-appends the missing `planmode.input_answered` event once without re-running the store answer transition or duplicating the replay tool result.
- Added focused runtime coverage for blocked `events.jsonl`, retry after restoring the path, one replay tool result, one answered history row, and one restored event.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestPlanInputAnswerRetryAfterEventFailureRestoresEvent -count=1`: failed before the fix because retry returned `plan mode has no pending input request`.
- `go test -timeout 120s ./internal/runtime -run TestPlanInputAnswerRetryAfterEventFailureRestoresEvent -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'Test(PlanInputAnswerRetryAfterEventFailureRestoresEvent|PlanInputAnswerRollsBackWhenToolResultAppendFails|PlanInputCancelRetryAfterHistoryFailureRestoresFacts|CancelPlanModeDoesNotDuplicateRecoveredInputToolResult|PlanInputCancelReturnsHistoryAppendError|CancelPlanModeRetryAfterCancelledEventFailureDoesNotDuplicateHistory|CancelPlanModeReportsCancelledEventAppendError)' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'Test(PlanInputAnswerRetryAfterEventFailureRestoresEvent|PlanInputAnswerRollsBackWhenToolResultAppendFails|PlanInputCancelRetryAfterHistoryFailureRestoresFacts|CancelPlanModeDoesNotDuplicateRecoveredInputToolResult|PlanInputCancelReturnsHistoryAppendError|CancelPlanModeRetryAfterCancelledEventFailureDoesNotDuplicateHistory|CancelPlanModeReportsCancelledEventAppendError|RevisePlanModeRetryAfterRevisionMessageFailureAppendsRevisionMessage|ApprovePlanModeRetryAfterApprovalMessageFailureAppendsApprovalMessage)' -count=1`: passed.
- `go test -timeout 120s ./internal/session -run 'Test(PlanModeInputValidationAndAnswer|SubmitPlanModeReturnsHistoryAppendError|ApprovePlanModeReturnsHistoryAppendError|PlanModeSubmitApproveAndHistory|RestorePlanModeSnapshotRemovesCreatedPlanMode)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/runner.go internal/runtime/planmode_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-109

Slice: `fix(runtime): retry failed plan mode creation event`

Finding:

- Continue-time Plan Mode creation could persist `planmode.json` and `planmode.created` history before failing to append the required `planmode.created` runtime event.
- Retrying the same continue request then created a second Plan Mode with a different id instead of repairing the missing event for the existing gate.

Changes:

- Added an idempotent continue-time Plan Mode creation helper.
- Reuses an existing planning Plan Mode when its objective/source match the requested draft, re-appending the missing `planmode.created` event once.
- Leaves genuinely different Plan Mode drafts on the existing creation path.
- Added focused runtime coverage for blocked `events.jsonl`, retry after restoring the path, unchanged Plan Mode id, one created history row, and one restored created event.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestContinuePlanModeRetryAfterCreatedEventFailureDoesNotReplacePlanMode -count=1`: failed before the fix because retry replaced the current Plan Mode id.
- `go test -timeout 120s ./internal/runtime -run TestContinuePlanModeRetryAfterCreatedEventFailureDoesNotReplacePlanMode -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'Test(ContinuePlanModeRetryAfterCreatedEventFailureDoesNotReplacePlanMode|RevisePlanModeRetryAfterRevisionMessageFailureAppendsRevisionMessage|PlanInputAnswerRetryAfterEventFailureRestoresEvent|PlanInputCancelRetryAfterHistoryFailureRestoresFacts|ApprovePlanModeRetryAfterApprovalMessageFailureAppendsApprovalMessage)' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'Test(ContinuePlanModeRetryAfterCreatedEventFailureDoesNotReplacePlanMode|PlanInputAnswerRetryAfterEventFailureRestoresEvent|PlanInputAnswerRollsBackWhenToolResultAppendFails|PlanInputCancelRetryAfterHistoryFailureRestoresFacts|CancelPlanModeDoesNotDuplicateRecoveredInputToolResult|PlanInputCancelReturnsHistoryAppendError|CancelPlanModeRetryAfterCancelledEventFailureDoesNotDuplicateHistory|CancelPlanModeReportsCancelledEventAppendError|RevisePlanModeRetryAfterRevisionMessageFailureAppendsRevisionMessage|ApprovePlanModeRetryAfterApprovalMessageFailureAppendsApprovalMessage)' -count=1`: passed.
- `go test -timeout 120s ./internal/session -run 'Test(PlanModeInputValidationAndAnswer|SubmitPlanModeReturnsHistoryAppendError|ApprovePlanModeReturnsHistoryAppendError|PlanModeSubmitApproveAndHistory|RestorePlanModeSnapshotRemovesCreatedPlanMode)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/runner.go internal/runtime/planmode_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-110

Slice: `fix(runtime): retry failed linked mission approval event`

Finding:

- Linked mission plan approval could persist approved mission state and `mission.plan.approved` goal history before failing to append the required runtime event.
- Retrying the same approval duplicated `mission.plan.approved` history instead of only restoring the missing event.

Changes:

- Added idempotent linked mission approval recovery when the current mission is already approved and goal history records the same goal id, Plan Mode id, and approved version.
- Re-appends the missing `mission.plan.approved` event once without re-running the goal store mutation.
- De-duplicates the runtime event by goal id, Plan Mode id, and approved version.
- Added focused runtime coverage for blocked `events.jsonl`, retry after restoring the path, one mission approval history row, and one restored mission approval event.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestApproveLinkedMissionPlanRetryAfterEventFailureDoesNotDuplicateHistory -count=1`: failed before the fix because retry duplicated `mission.plan.approved` history.
- `go test -timeout 120s ./internal/runtime -run TestApproveLinkedMissionPlanRetryAfterEventFailureDoesNotDuplicateHistory -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'Test(ApproveLinkedMissionPlanRetryAfterEventFailureDoesNotDuplicateHistory|ApproveLinkedMissionPlanReportsEventAppendError|ApprovePlanModeRetryAfterApprovalMessageFailureAppendsApprovalMessage|ApproveLinkedPlanModeMarksMissionPlanApproved|ApproveLinkedPlanModeBlocksUncoveredMissionValidation)' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'Test(ApproveLinkedMissionPlanRetryAfterEventFailureDoesNotDuplicateHistory|ApproveLinkedMissionPlanReportsEventAppendError|ApprovePlanModeRetryAfterApprovalMessageFailureAppendsApprovalMessage|ApproveLinkedPlanModeMarksMissionPlanApproved|ApproveLinkedPlanModeBlocksUncoveredMissionValidation|ContinuePlanModeRetryAfterCreatedEventFailureDoesNotReplacePlanMode|PlanInputAnswerRetryAfterEventFailureRestoresEvent|PlanInputCancelRetryAfterHistoryFailureRestoresFacts|RevisePlanModeRetryAfterRevisionMessageFailureAppendsRevisionMessage)' -count=1`: passed.
- `go test -timeout 120s ./internal/session -run 'Test(PlanModeInputValidationAndAnswer|SubmitPlanModeReturnsHistoryAppendError|ApprovePlanModeReturnsHistoryAppendError|PlanModeSubmitApproveAndHistory|RestorePlanModeSnapshotRemovesCreatedPlanMode|ApproveMissionPlanReturnsHistoryAppendError)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/runner.go internal/runtime/planmode_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-112

Slice: `fix(webconsole): classify mission approval store errors`

Finding:

- Web mission plan approval loaded `goal.json` and `goal-history.jsonl` before mutating approval state, but reported pre-mutation durable-fact load failures as HTTP 400.
- A blocked `goal-history.jsonl` path was therefore reported as a bad operator request instead of a local session-store failure.

Changes:

- Wrapped mission approval pre-mutation store load failures in a store-error type.
- Mapped mission approval store and event failures to HTTP 500.
- Preserved HTTP 400 for approval validation errors such as attempting to approve a goal without a mission plan.
- Added focused WebConsole coverage for unreadable Goal history during mission plan approval.

Validation:

- `go test -timeout 120s ./internal/webconsole -run TestServiceMissionPlanApproveReportsHistoryLoadErrorAsServerError -count=1`: failed before the fix because unreadable Goal history returned HTTP 400.
- `go test -timeout 120s ./internal/webconsole -run 'TestServiceMissionPlanApprove(ReportsHistoryLoadErrorAsServerError|RejectsGoalWithoutMissionPlan|RollsBackHistoryWhenEventAppendFails)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 14/14 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-113

Slice: `fix(webconsole): classify steer store errors`

Finding:

- Web steer returned HTTP 400 for durable control queue write failures from `Runner.Steer`.
- Blocking `control/steer.jsonl` caused the request to fail before any steer fact was durable, but the API classified it as a bad client request.

Changes:

- Added `steerActionStatus` for Web steer responses.
- Kept oversized inputs and empty messages as HTTP 400.
- Kept non-running sessions as HTTP 409 conflict.
- Mapped local store/event/runtime failures to HTTP 500 and missing session facts to HTTP 404.
- Added focused WebConsole coverage for blocked `control/steer.jsonl`.

Validation:

- `go test -timeout 120s ./internal/webconsole -run TestServiceSteerReportsStoreAppendFailureAsServerError -count=1`: failed before the fix because blocked steer queue writes returned HTTP 400.
- `go test -timeout 120s ./internal/webconsole -run 'TestServiceSteer(WritesWebSource|ReportsStoreAppendFailureAsServerError)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 14/14 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-114

Slice: `fix(webconsole): classify queue submit store errors`

Finding:

- Web queue submit returned HTTP 400 for durable queue job-store write failures from `Runner.QueueSubmit`.
- Blocking the `_queue` root caused the request to fail before any job fact was durable, but the API classified it as a bad client request.

Changes:

- Added `queueJobActionStatus` for Web queue submit responses.
- Kept prompt/config/request validation errors as HTTP 400.
- Mapped parent pending Plan Mode queue submissions to HTTP 409 conflict and missing durable session facts to HTTP 404.
- Mapped local queue store, parent coordination, and other infrastructure failures to HTTP 500.
- Added focused WebConsole coverage for blocked `_queue` persistence and updated the parent pending Plan Mode queue-gate expectation.

Validation:

- `go test -timeout 120s ./internal/webconsole -run 'TestService(QueueSubmitReportsStoreAppendFailureAsServerError|PlanModeGetAndParentQueueGate)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 14/14 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-115

Slice: `fix(runtime): roll back failed parent queue link`

Finding:

- `Runner.QueueSubmit` persisted a queued job before adding the job to parent coordination.
- If `parent-coordination.json` could not be written, `QueueSubmit` returned an error while leaving the queued job available for worker pickup with no matching parent unresolved-work fact.

Changes:

- Added public `Store.DeleteJob` backed by the existing locked queue job deletion helper.
- Rolled back the just-created queue job when parent coordination persistence fails after `EnqueueJob`.
- Preserved rollback failure visibility by returning an error that includes the original parent coordination failure and the delete failure.
- Extended the parent coordination error regression to assert no queued job remains after failed submit.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestRunnerQueueSubmitReportsParentCoordinationError -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'TestRunnerQueueSubmitReportsParentCoordinationError|TestRunnerQueueSubmitAndWorkerCompletesJob|TestRunnerProcessNextJobReportsParentCoordinationError' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/session/store.go internal/runtime/delegation.go internal/runtime/delegation_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 14/14 tests.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-116

Slice: `fix(webconsole): classify mission validation store errors`

Finding:

- Web mission validation patch persisted the patched Goal snapshot through `SaveGoal`, but reported that write failure as HTTP 400.
- Blocking the durable `goal.lock` path made `/api/sessions/{id}/mission/validation` return a client error even though the existing Goal snapshot remained unchanged and the problem was local store persistence.

Changes:

- Mapped `SaveGoal` failure in `handleMissionValidationPatch` to HTTP 500.
- Added a focused WebConsole regression that blocks `goal.lock`, expects a server error, and asserts the validation plan was not advanced.

Validation:

- `go test -timeout 120s ./internal/webconsole -run TestServiceMissionValidationPatchReportsGoalWriteFailureAsServerError -count=1`: failed before the fix with HTTP 400.
- `go test -timeout 120s ./internal/webconsole -run TestServiceMissionValidationPatchReportsGoalWriteFailureAsServerError -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -run 'TestServiceMissionValidation(PatchReportsGoalWriteFailureAsServerError|PlanPatchReportsHistoryAppendError|ContractPatchReportsHistoryAppendError|PatchResetsApprovedPlanToPendingGate)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 14/14 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-117

Slice: `fix(webconsole): classify goal patch store errors`

Finding:

- Web generic Goal patch and Mission plan patch persisted changes through `PatchGoal`, but reported blocked durable `goal.lock` writes as HTTP 400.
- Blocking `goal.lock` made both `/api/sessions/{id}/goal` and `/api/sessions/{id}/mission/plan` return client errors even though the existing Goal snapshot remained unchanged and the problem was local store persistence.

Changes:

- Reused `goalStoreStatus` for `PatchGoal` failures in `handleGoalPatch` and `handleMissionPlanPatch`.
- Added focused WebConsole regressions for blocked `goal.lock` on both endpoints, asserting server-error status and unchanged Goal facts.

Validation:

- `go test -timeout 120s ./internal/webconsole -run 'TestService(GoalPatch|MissionPlanPatch)ReportsGoalWriteFailureAsServerError' -count=1`: failed before the fix with HTTP 400 for both endpoints.
- `go test -timeout 120s ./internal/webconsole -run 'TestService(GoalPatch|MissionPlanPatch)ReportsGoalWriteFailureAsServerError' -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -run 'TestService(GoalPatch|MissionPlanPatch)ReportsGoalWriteFailureAsServerError|TestService(GoalPatchReportsHistoryAppendError|MissionPlanPatchReportsHistoryAppendError)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 14/14 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-118

Slice: `fix(webconsole): roll back config on API key failure`

Finding:

- Web Settings saved `config.yaml` before persisting an API key to the configured env file.
- If env-file persistence failed after config write, the handler returned HTTP 500 while leaving the new config on disk and without swapping the in-memory service config.
- A focused regression used `GO_CLI_AGENT_ENV_FILE=<configPath>/.env`; env preflight passed, config write created `<configPath>` as a regular file, and the env write failed because its parent was not a directory. Before the fix, the failed response left `model: should-not-persist` in the config file.

Changes:

- Snapshotted existing config file bytes before Settings writes.
- Restored the previous config file, or removed a newly created config file, when API-key env persistence fails after config write.
- Added focused WebConsole coverage for config rollback on env write failure.

Validation:

- `go test -timeout 120s ./internal/webconsole -run TestAPIKeyWriteRollsBackConfigWhenEnvWriteFails -count=1`: failed before the fix because the config file retained `should-not-persist`.
- `go test -timeout 120s ./internal/webconsole -run 'TestAPIKeyWrite(RollsBackConfigWhenEnvWriteFails|WaitsForConfigWriteSuccess|PreflightsEnvTargetBeforeConfigWrite|DoesNotLogSecretValue)' -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -run 'TestAPIKeyWrite(RollsBackConfigWhenEnvWriteFails|WaitsForConfigWriteSuccess|PreflightsEnvTargetBeforeConfigWrite|DoesNotLogSecretValue)|TestServiceConfigRoutesUpdateActiveConfig' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 14/14 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-119

Slice: `fix(webconsole): preflight API key env names`

Finding:

- Web Settings accepted malformed provider API-key env names such as `BAD=KEY_API_KEY` during API-key preflight.
- Before the fix, the route wrote `config.yaml` and `.env`, then failed at `os.Setenv` with `setenv: invalid argument`, leaving a secret in an invalid `.env` assignment and leaving config changes persisted.

Changes:

- Exposed the env-file key policy as `config.AllowedEnvFileKey`.
- Made `config.UpsertEnvFile` reject invalid env keys before writing.
- Made Web Settings API-key preflight reject invalid env keys before config, env-file, process env, or audit mutation.
- Added focused regressions for the Web Settings route and config env-file write helper.

Validation:

- `go test -timeout 120s ./internal/webconsole -run TestAPIKeyWriteRejectsInvalidEnvKeyBeforePersistence -count=1`: failed before the fix with late `setenv: invalid argument`.
- `go test -timeout 120s ./internal/webconsole -run TestAPIKeyWriteRejectsInvalidEnvKeyBeforePersistence -count=1`: passed.
- `go test -timeout 120s ./internal/config -run TestUpsertEnvFileRejectsInvalidKey -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -run 'TestAPIKeyWrite(RollsBackConfigWhenEnvWriteFails|WaitsForConfigWriteSuccess|PreflightsEnvTargetBeforeConfigWrite|RejectsInvalidEnvKeyBeforePersistence|DoesNotLogSecretValue)' -count=1`: passed.
- `go test -timeout 120s ./internal/config -run 'Test(UpsertEnvFile|LoadEnvFile)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/config/envfile.go internal/config/config_test.go internal/webconsole/service.go internal/webconsole/service_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 14/14 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/config -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-120

Slice: `fix(webconsole): reject API key config target alias`

Finding:

- Web Settings allowed `GO_CLI_AGENT_ENV_FILE` to point at the same path as `Options.ConfigPath`.
- Before the fix, `/api/config` returned HTTP 200, wrote YAML config, then appended `OPENAI_API_KEY=...` into the same file.

Changes:

- Added a Web Settings API-key target preflight that rejects env-file paths matching the cleaned absolute config path.
- Ran the alias preflight before config, env-file, process env, or audit mutation.
- Added focused WebConsole coverage proving no config/env file or process env mutation occurs for the alias case.

Validation:

- `go test -timeout 120s ./internal/webconsole -run TestAPIKeyWriteRejectsConfigPathAsEnvFile -count=1`: failed before the fix with HTTP 200.
- `go test -timeout 120s ./internal/webconsole -run TestAPIKeyWriteRejectsConfigPathAsEnvFile -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -run 'TestAPIKeyWrite(RollsBackConfigWhenEnvWriteFails|WaitsForConfigWriteSuccess|PreflightsEnvTargetBeforeConfigWrite|RejectsInvalidEnvKeyBeforePersistence|RejectsConfigPathAsEnvFile|DoesNotLogSecretValue)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 14/14 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-121

Slice: `fix(webconsole): reject config audit target alias`

Finding:

- Web Settings allowed `Options.ConfigPath` to point at the same file as `webconsole-audit.jsonl`.
- Before the fix, `/api/config` returned HTTP 200, wrote YAML config, then appended JSONL audit records into the same file.

Changes:

- Added a Web Settings preflight that rejects config paths matching the cleaned absolute Web audit log path.
- Ran the alias preflight before audit-log writability probing, config persistence, API-key env-file persistence, process env mutation, or audit append.
- Added focused WebConsole coverage proving no file is created for the config/audit alias case.

Validation:

- `go test -timeout 120s ./internal/webconsole -run TestUpdateConfigRejectsConfigPathAsAuditLog -count=1`: failed before the fix with HTTP 200.
- `go test -timeout 120s ./internal/webconsole -run TestUpdateConfigRejectsConfigPathAsAuditLog -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -run 'Test(UpdateConfigRejectsConfigPathAsAuditLog|APIKeyWriteRejectsConfigPathAsEnvFile)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 14/14 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-122

Slice: `fix(webconsole): preflight API key env values`

Finding:

- Web Settings accepted NUL-containing API-key values and only failed when `os.Setenv` ran after config and `.env` persistence.
- Before the fix, `/api/config` returned HTTP 500 from late `setenv: invalid argument` while the API key write path had already run.

Changes:

- Added NUL-value validation to the existing Web API-key preflight.
- Kept the check before config, env-file, process environment, or audit mutation.
- Added focused WebConsole coverage proving invalid values leave no config file, env file secret, or process env mutation.

Validation:

- `go test -timeout 120s ./internal/webconsole -run TestAPIKeyWriteRejectsInvalidEnvValueBeforePersistence -count=1`: failed before the fix with late `setenv: invalid argument`.
- `go test -timeout 120s ./internal/webconsole -run TestAPIKeyWriteRejectsInvalidEnvValueBeforePersistence -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -run 'TestAPIKeyWrite(RollsBackConfigWhenEnvWriteFails|WaitsForConfigWriteSuccess|PreflightsEnvTargetBeforeConfigWrite|RejectsInvalidEnvKeyBeforePersistence|RejectsInvalidEnvValueBeforePersistence|RejectsConfigPathAsEnvFile|DoesNotLogSecretValue)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 14/14 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-123

Slice: `fix(webconsole): reject API key audit target alias`

Finding:

- Web Settings allowed `GO_CLI_AGENT_ENV_FILE` to point at `webconsole-audit.jsonl`.
- Before the fix, `/api/config` returned HTTP 200 and persisted the submitted API key into the audit log file before appending JSONL audit records.

Changes:

- Added a Web Settings preflight that rejects API-key env-file paths matching the cleaned absolute Web audit log path.
- Ran the alias preflight before config persistence, env-file persistence, process env mutation, or audit append.
- Added focused WebConsole coverage proving no config file, audit secret, or process env mutation occurs for the env/audit alias case.

Validation:

- `go test -timeout 120s ./internal/webconsole -run TestAPIKeyWriteRejectsEnvFileAsAuditLog -count=1`: failed before the fix with HTTP 200.
- `go test -timeout 120s ./internal/webconsole -run TestAPIKeyWriteRejectsEnvFileAsAuditLog -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -run 'Test(APIKeyWriteRejectsEnvFileAsAuditLog|APIKeyWriteRejectsConfigPathAsEnvFile|UpdateConfigRejectsConfigPathAsAuditLog)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 14/14 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-124

Slice: `fix(webconsole): preflight blank API key values`

Finding:

- Web Settings accepted whitespace-only API-key values.
- Before the fix, `/api/config` returned HTTP 200, persisted config changes, and wrote an `OPENAI_API_KEY` entry that `Provider.ResolvedAPIKey` later trims to empty.

Changes:

- Added a blank-value check to the existing Web API-key preflight.
- Kept the check before config, env-file, process environment, or audit mutation.
- Added focused WebConsole coverage proving blank values leave no config file, env file API-key entry, or process env mutation.

Validation:

- `go test -timeout 120s ./internal/webconsole -run TestAPIKeyWriteRejectsBlankEnvValueBeforePersistence -count=1`: failed before the fix with HTTP 200.
- `go test -timeout 120s ./internal/webconsole -run TestAPIKeyWriteRejectsBlankEnvValueBeforePersistence -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -run 'TestAPIKeyWrite(RejectsBlankEnvValueBeforePersistence|RejectsInvalidEnvValueBeforePersistence|RejectsInvalidEnvKeyBeforePersistence|RejectsEnvFileAsAuditLog|RejectsConfigPathAsEnvFile|RollsBackConfigWhenEnvWriteFails|WaitsForConfigWriteSuccess|PreflightsEnvTargetBeforeConfigWrite|DoesNotLogSecretValue)|TestUpdateConfigRejectsConfigPathAsAuditLog' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 14/14 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-125

Slice: `fix(webconsole): keep empty API key field unmasked`

Finding:

- Settings UI masked the API-key field after a successful save even when the backend had reported no existing key and the user submitted no key.
- Before the fix, a save with `apiKey: ""` changed the local field to `••••••••••••••••` and set `dataset.originalHasKey = "true"`.

Changes:

- Captured the normalized API-key payload before save.
- Only mask the field and mark `originalHasKey=true` when a non-empty key was actually submitted.
- Left empty API-key fields empty and marked as no-key after successful non-key Settings saves.
- Added focused Node renderer coverage for the empty-key save path.

Validation:

- `node validation/scripts/webconsole_utils_test.mjs`: failed before the fix because the empty API-key field became the mask.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 15/15 tests.
- `git diff --check`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-126

Slice: `fix(webconsole): preserve unchanged API key mask`

Finding:

- Settings UI could leave the API-key field empty after saving an unchanged existing key.
- Before the fix, loading Settings with `has_key=true`, clearing the masked field, and saving sent `apiKey: ""` but left the field blank even though the backend kept the existing key unchanged.

Changes:

- Preserved the existing-key mask after successful saves where no new API key was submitted and the field originally represented an existing key.
- Kept the no-key behavior from FCA-20260526-125: providers with no existing key remain blank after non-key saves.
- Added focused Node renderer coverage for the clear-existing-mask / unchanged-key path.

Validation:

- `node validation/scripts/webconsole_utils_test.mjs`: failed before the fix because the existing-key mask stayed empty after save.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `git diff --check`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-127

Slice: `fix(webconsole): reject unknown session subresources`

Finding:

- Web session subresource routes did not first prove that `session.json` existed for the requested session ID.
- Before the fix, `GET /api/sessions/missing_session_subresource/children` returned HTTP 200 with empty arrays, and unknown-session goal creation could create orphaned goal/session artifacts without a valid session metadata fact.

Changes:

- Added a Web session route boundary check that loads session metadata before dispatching `/api/sessions/{id}/...` subresources.
- Reused a shared session store status mapper so missing session metadata returns HTTP 404 and malformed store IDs return HTTP 400.
- Kept existing valid-session empty-state semantics for no goal, no tasks, no children, and no messages.
- Added focused WebConsole regression coverage for unknown `children`, `tasks`, `messages`, and `goal` reads plus unknown-session goal creation.

Validation:

- `go test -timeout 120s ./internal/webconsole -run TestServiceSessionSubresourcesRejectUnknownSession -count=1`: failed before the fix because unknown-session children returned HTTP 200.
- `go test -timeout 120s ./internal/webconsole -run TestServiceSessionSubresourcesRejectUnknownSession -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-128

Slice: `fix(webconsole): classify queue job id errors`

Finding:

- Web queue job detail only mapped missing job facts to HTTP 404; malformed queue job IDs rejected by the session store were reported as HTTP 500.
- Before the fix, `GET /api/queue/jobs/bad%2Fjob` returned HTTP 500 with an invalid queue job id message even though no durable queue-store failure occurred.

Changes:

- Reused the Web store-ID client error classifier in `handleShowJob`.
- Kept valid but missing queue jobs mapped to HTTP 404.
- Kept queue store, reconciliation, and filesystem failures mapped to HTTP 500.
- Added focused WebConsole regression coverage for malformed job IDs plus adjacent missing-job behavior.

Validation:

- `go test -timeout 120s ./internal/webconsole -run TestServiceQueueJobDetailRejectsMalformedJobID -count=1`: failed before the fix because malformed job IDs returned HTTP 500.
- `go test -timeout 120s ./internal/webconsole -run TestServiceQueueJobDetailRejectsMalformedJobID -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-129

Slice: `fix(webconsole): report linked queue reconcile errors`

Finding:

- Session detail intentionally triggered linked queue job reconciliation for queue-child sessions, but ignored the returned error.
- Before the fix, `GET /api/sessions/{child}` could return HTTP 200 with stale `running` state when a linked failed queue job could not persist the required child `state.json` repair.

Changes:

- Propagated linked queue job `LoadJob` / reconciliation errors from `sessionDetail`.
- Kept successful reconciliation behavior unchanged.
- Added focused WebConsole regression coverage for failed linked queue reconciliation while preserving the existing successful reconciliation test.

Validation:

- `go test -timeout 120s ./internal/webconsole -run TestServiceSessionDetailReportsLinkedQueueReconcileError -count=1`: failed before the fix because detail returned HTTP 200 with stale running state.
- `go test -timeout 120s ./internal/webconsole -run 'TestServiceSessionDetail(ReconcilesLinkedQueueJob|ReportsLinkedQueueReconcileError)' -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-130

Slice: `fix(webconsole): report provider attempt load errors`

Finding:

- Session detail silently ignored provider-attempt ledger load errors even though the store already returns empty lists for missing ledgers and real errors for corrupt/unreadable ledgers.
- Before the fix, invalid JSON in `provider-attempts.jsonl` made `GET /api/sessions/{id}` return HTTP 200 with no provider attempts, hiding the corrupt diagnostic fact file.

Changes:

- Propagated `LoadProviderAttempts` errors from `sessionDetail`.
- Propagated `LoadArtifactTracker` errors from `sessionDetail` as the same absent-versus-corrupt optional fact pattern.
- Wrapped both errors with the relevant fact filename for actionable Web API responses.
- Added focused WebConsole regression coverage for corrupt `provider-attempts.jsonl`.

Validation:

- `go test -timeout 120s ./internal/webconsole -run TestServiceSessionDetailReportsProviderAttemptsLoadError -count=1`: failed before the fix because corrupt provider attempts returned HTTP 200 and hidden ledger facts.
- `go test -timeout 120s ./internal/webconsole -run TestServiceSessionDetailReportsProviderAttemptsLoadError -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-131

Slice: `fix(webconsole): report goal history load errors`

Finding:

- Session detail silently ignored Goal history load errors even though the store already returns an empty list for a missing history ledger and real errors for corrupt/unreadable ledgers.
- Before the fix, invalid JSON in `artifacts/goal-history.jsonl` made `GET /api/sessions/{id}` return HTTP 200 with an empty Goal facts history, hiding the corrupt durable Goal fact file.

Changes:

- Changed `goalFacts` to return `LoadGoalHistory` errors instead of discarding them.
- Propagated Goal facts load errors through `sessionDetail`.
- Wrapped the error with `goal-history.jsonl` for actionable Web API responses.
- Added focused WebConsole regression coverage for corrupt `artifacts/goal-history.jsonl`.

Validation:

- `go test -timeout 120s ./internal/webconsole -run TestServiceSessionDetailReportsGoalHistoryLoadError -count=1`: failed before the fix because corrupt Goal history returned HTTP 200 with empty history facts.
- `go test -timeout 120s ./internal/webconsole -run TestServiceSessionDetailReportsGoalHistoryLoadError -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -run 'TestService(SessionDetailReportsGoalHistoryLoadError|GoalFactsAndMissionCoverageApproval)' -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-132

Slice: `fix(webconsole): report snapshot load errors`

Finding:

- Session detail silently ignored corrupt optional snapshot files for contract, long-run checkpoint, parent coordination, Goal, and Plan Mode facts.
- Before the fix, invalid JSON in any of those snapshot files made `GET /api/sessions/{id}` return HTTP 200 with the corresponding fact omitted, hiding corrupt durable session authority files.

Changes:

- Changed `sessionDetail` to ignore only `fs.ErrNotExist` from optional snapshot loaders.
- Propagated non-missing load failures for `contract.json`, `checkpoints/longrun-latest.json`, `parent-coordination.json`, `goal.json`, and `planmode.json`.
- Wrapped each load failure with the relevant fact filename for actionable Web API responses.
- Added focused table-driven WebConsole coverage for corrupt optional snapshot files.

Validation:

- `go test -timeout 120s ./internal/webconsole -run TestServiceSessionDetailReportsSnapshotLoadErrors -count=1`: failed before the fix because corrupt snapshot files returned HTTP 200 with omitted facts.
- `go test -timeout 120s ./internal/webconsole -run TestServiceSessionDetailReportsSnapshotLoadErrors -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -run 'TestService(SessionDetailReportsSnapshotLoadErrors|SessionDetailReportsGoalHistoryLoadError|GoalFactsAndMissionCoverageApproval)' -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-133

Slice: `fix(webconsole): report linked plan mode event errors`

Finding:

- Web linked Plan Mode creation discarded `planmode.created` session event append errors after `EnsurePlanModeForGoal` succeeded.
- Before the fix, blocked `events.jsonl` during mission plan approval returned the normal HTTP 409 Plan Mode conflict and hid the missing durable event fact.

Changes:

- Added `appendLinkedPlanModeCreatedEvent` for Web linked Plan Mode creation events.
- Propagated event append failures from Goal create, Goal patch, mission-plan patch, mission validation patch, and mission plan approval linked-gate paths.
- Reused existing rollback helpers so event append failure restores affected Goal/task/Plan Mode facts.
- Added focused WebConsole coverage for mission plan approval blocked on `events.jsonl`.

Validation:

- `go test -timeout 120s ./internal/webconsole -run TestServiceMissionPlanApproveReportsLinkedPlanModeEventAppendError -count=1`: failed before the fix because blocked `events.jsonl` returned HTTP 409 and hid the missing event.
- `go test -timeout 120s ./internal/webconsole -run TestServiceMissionPlanApproveReportsLinkedPlanModeEventAppendError -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -run 'TestService(GoalCreateReportsEventAppendErrorAndRollsBack|GoalPatchRollsBackLinkedPlanModeWhenEventAppendFails|MissionPlanPatchPlanModeReportsHistoryAppendError|GoalPatchMissionPlanModeReportsHistoryAppendError|MissionValidationContractPatchReportsHistoryAppendError)' -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -run 'TestService(MissionPlanApproveReportsLinkedPlanModeEventAppendError|GoalCreateReportsEventAppendErrorAndRollsBack|GoalPatchRollsBackLinkedPlanModeWhenEventAppendFails|MissionPlanPatchPlanModeReportsHistoryAppendError|GoalPatchMissionPlanModeReportsHistoryAppendError|MissionValidationContractPatchReportsHistoryAppendError)' -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-134

Slice: `fix(session): report summary snapshot load errors`

Finding:

- Session list summaries silently ignored corrupt `goal.json` and `planmode.json` files even though those files are durable Goal / Plan Mode fact sources.
- Before the fix, invalid JSON in either summary snapshot made `/api/sessions` and `/api/history` return HTTP 200 with empty Goal / Plan Mode summary fields.

Changes:

- Changed `populateGoalSummary` and `populatePlanModeSummary` to return errors.
- Preserved missing optional snapshots as absent summaries.
- Propagated non-missing Goal / Plan Mode load errors through `List`, `ListPage`, and `ListChildren`, wrapping errors with `goal.json` or `planmode.json`.
- Added focused store and WebConsole regressions for corrupt summary snapshots.

Validation:

- `go test -timeout 120s ./internal/webconsole -run TestServiceSessionListReportsSummarySnapshotLoadErrors -count=1`: failed before the fix because corrupt summary snapshots returned HTTP 200.
- `go test -timeout 120s ./internal/session -run TestStoreListReportsCorruptSummarySnapshots -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -run TestServiceSessionListReportsSummarySnapshotLoadErrors -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/session/store.go internal/session/store_test.go internal/webconsole/service_test.go`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-135

Slice: `fix(session): report corrupt task graph files`

Finding:

- Task graph readers silently ignored corrupt `tasks/task_*.json` files.
- Before the fix, invalid JSON in `tasks/task_0002.json` made `ListTasks` return a smaller valid-looking graph, and task mutations could continue from that incomplete snapshot.

Changes:

- Added a shared `readTasks` helper for task graph file loading.
- Preserved missing `tasks/` as an empty optional graph.
- Changed corrupt task file reads to return an error that includes `tasks/<file>.json`.
- Added focused store coverage for read and mutation paths.
- Added focused WebConsole coverage for session detail and `GET /api/sessions/{id}/tasks`.

Validation:

- `go test -timeout 120s ./internal/session -run TestTaskListAndMutationReportCorruptTaskFiles -count=1`: failed before the fix because corrupt task files returned nil error.
- `go test -timeout 120s ./internal/session -run TestTaskListAndMutationReportCorruptTaskFiles -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -run TestServiceTaskBoardReportsCorruptTaskFile -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/session/store.go internal/session/taskboard_test.go internal/webconsole/service_test.go`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-136

Slice: `fix(runtime): report corrupt plan mode continuation state`

Finding:

- Normal-message `Continue` ignored non-missing `planmode.json` load errors while checking whether the message should revise a pending Plan Mode.
- Before the fix, corrupt `planmode.json` failed later with a raw JSON parser error that did not name the durable snapshot file.

Changes:

- Changed `Runner.Continue` to distinguish absent Plan Mode snapshots from corrupt/unreadable snapshots.
- Preserved `fs.ErrNotExist` as "no Plan Mode" for ordinary non-Plan continuations.
- Returned a pre-run failure wrapping non-missing Plan Mode load errors with `load planmode.json`.
- Added a focused runtime regression proving corrupt Plan Mode continuation state does not call the provider or append the continuation message.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestContinueMessageReportsCorruptPlanModeSnapshot -count=1`: failed before the fix because corrupt `planmode.json` returned a raw JSON parser error without `planmode.json`.
- `go test -timeout 120s ./internal/runtime -run TestContinueMessageReportsCorruptPlanModeSnapshot -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -run 'Test(ContinueMessageReportsCorruptPlanModeSnapshot|RevisePlanModeRetryAfterRevisionMessageFailureAppendsRevisionMessage|ApprovePlanModeRetryAfterApprovalMessageFailureAppendsApprovalMessage|ApproveLinkedPlanModeMarksMissionPlanApproved|ApproveLinkedPlanModeBlocksUncoveredMissionValidation)' -count=1`: passed.
- `go test -timeout 120s ./internal/runtime -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/runner.go internal/runtime/planmode_test.go`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/settings-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 16/16 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-111

Slice: `fix(webconsole): roll back failed mission approval event`

Finding:

- Web mission plan approval could persist an approved mission snapshot and `mission.plan.approved` Goal history before failing to append the required session event.
- The failed HTTP response therefore disagreed with durable Goal facts, leaving recovery and the Goal inspector to treat the approval as completed.

Changes:

- Added a Web mission approval helper that snapshots `goal.json` and `goal-history.jsonl` before approval.
- Appends the required `mission.plan.approved` session event as part of the helper.
- Restores both the Goal snapshot and Goal history when the event append fails.
- Preserves HTTP 400 for store-level approval validation errors and HTTP 500 for event/rollback failures.
- Added focused WebConsole coverage for blocked `events.jsonl`, restored unapproved mission snapshot, and restored Goal history.

Validation:

- `go test -timeout 120s ./internal/webconsole -run TestServiceMissionPlanApproveRollsBackHistoryWhenEventAppendFails -count=1`: failed before the fix because failed event append left `mission.plan_status=approved`.
- `go test -timeout 120s ./internal/webconsole -run TestServiceMissionPlanApproveRollsBackHistoryWhenEventAppendFails -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -run 'TestService(MissionApproveExecutingPlanModeAppendsApprovalFact|MissionPlanApproveRollsBackHistoryWhenEventAppendFails|MissionPlanApproveRejectsGoalWithoutMissionPlan|GoalFactsAndMissionCoverageApproval)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed with no output.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed, 14/14 tests.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-084

Slice: `fix(session): roll back failed goal history transitions`

Finding:

- Goal store helpers returned required `goal-history.jsonl` append errors, but they mutated `goal.json` before the failed history append.
- Focused regressions blocked `goal-history.jsonl`; before the fix, failed accounting left `tokens_used` advanced and failed completion left the current snapshot completed.

Changes:

- Added rollback snapshots for Goal current facts before history-backed transitions.
- Restored the previous `goal.json` when a required history append fails after mutation.
- Applied rollback to completion, mission plan approval, accounting / budget history, and structured progress / mission update paths.
- Extended focused accounting, completion, mission approval, and progress history-failure regressions to assert current snapshot rollback.

Validation:

- `go test -timeout 120s ./internal/session -run 'TestUpdateGoalAccountingReturnsHistoryAppendError|TestCompleteGoalReturnsHistoryAppendError' -count=1`: failed before the fix because failed accounting/completion left `goal.json` advanced.
- `go test -timeout 120s ./internal/session -run 'TestUpdateGoalAccountingReturnsHistoryAppendError|TestCompleteGoalReturnsHistoryAppendError|TestRecordGoalProgressReturnsHistoryAppendError|TestApproveMissionPlanReturnsHistoryAppendError|TestApproveMissionPlanRejectsGoalWithoutMissionPlan' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/session/goal.go internal/session/goal_progress.go internal/session/store_test.go`: passed with no output.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-085

Slice: `fix(runtime): roll back failed budget wrap-up turn start`

Finding:

- Runtime marked `BudgetWrapUpTurnStartedAt` in `goal.json` before appending the required `goal.budget_wrapup_turn_started` history fact.
- A focused regression blocked `goal-history.jsonl`; before the fix, `Engine.Run` returned the history append error but left `budget_wrapup_turn_started_at` advanced in `goal.json`.

Changes:

- Restored the previous Goal snapshot when the runtime-owned budget wrap-up turn-start history append fails.
- Preserved the existing behavior that fails before emitting `goal.budget_wrapup_turn_started` or calling the provider.
- Extended the focused budget wrap-up history-failure regression to assert snapshot rollback.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestEngineBudgetWrapUpTurnStartReportsGoalHistoryError -count=1`: failed before the fix because `budget_wrapup_turn_started_at` remained set after the history append error.
- `go test -timeout 120s ./internal/runtime -run 'TestEngineBudgetWrapUpTurnStartReportsGoalHistoryError|TestEngineBudgetWrapUpThenFinishAwaitsInput|TestGoalCompletionGateRequiresBudgetWrapUpWhenStopOnBudget' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/engine.go internal/runtime/engine_test.go`: passed with no output.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `node --check internal/webconsole/assets/app.js internal/webconsole/assets/events.js internal/webconsole/assets/session-view.js internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-086

Slice: `fix(cli): roll back failed goal status mutations`

Finding:

- CLI goal status and clear commands returned required `goal-history.jsonl` append errors, but left the current `goal.json` mutation applied.
- Focused regressions blocked `goal-history.jsonl`; before the fix, failed pause left `status=paused` and failed clear removed `goal.json`.

Changes:

- Restored the previous Goal snapshot when CLI status history or event append fails.
- Restored the previous Goal snapshot when CLI clear history or event append fails.
- Extended focused CLI history-failure regressions to assert rollback.

Validation:

- `go test -timeout 120s ./internal/app -run 'TestGoalStatusCommandReportsHistoryAppendError|TestGoalClearCommandReportsHistoryAppendError' -count=1`: failed before the fix because failed pause/clear left `goal.json` mutated.
- `go test -timeout 120s ./internal/app -run 'TestGoalStatusCommandReportsHistoryAppendError|TestGoalClearCommandReportsHistoryAppendError' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/app/app.go internal/app/app_test.go`: passed with no output.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-087

Slice: `fix(webconsole): roll back failed goal status mutations`

Finding:

- Web goal status and clear handlers returned required `goal-history.jsonl` append errors, but left the current `goal.json` mutation applied.
- Focused regressions blocked `goal-history.jsonl`; before the fix, failed Web pause left `status=paused` and failed Web clear removed `goal.json`.

Changes:

- Restored the previous Goal snapshot when Web status history or event append fails.
- Restored the previous Goal snapshot when Web clear history or event append fails.
- Extended focused Web history-failure regressions to assert rollback.

Validation:

- `go test -timeout 120s ./internal/webconsole -run 'TestServiceGoalStatusReportsHistoryAppendError|TestServiceGoalClearReportsHistoryAppendError' -count=1`: failed before the fix because failed pause/clear left `goal.json` mutated.
- `go test -timeout 120s ./internal/webconsole -run 'TestServiceGoalStatusReportsHistoryAppendError|TestServiceGoalClearReportsHistoryAppendError' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed with no output.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-088

Slice: `fix(webconsole): roll back failed goal patch mutations`

Finding:

- Web goal patch returned required `goal-history.jsonl` append errors, but left simple current `goal.json` patch data applied.
- A focused regression blocked `goal-history.jsonl`; before the fix, failed Web success-criteria patch left the new criterion in `goal.json`.

Changes:

- Restored the previous Goal snapshot when Web goal patch history or event append fails and no tasks or linked Plan Mode were created.
- Tracked linked Plan Mode creation in `handleGoalPatch` so rollback is limited to the no-new-side-fact path.
- Extended focused Web goal-patch history-failure regression to assert rollback.

Validation:

- `go test -timeout 120s ./internal/webconsole -run TestServiceGoalPatchReportsHistoryAppendError -count=1`: failed before the fix because the failed patch left the new success criterion in `goal.json`.
- `go test -timeout 120s ./internal/webconsole -run 'TestServiceGoalPatchReportsHistoryAppendError|TestServiceGoalPatchPreservesRuntimeProgressFacts|TestServiceGoalPatchMissionResetsApprovedPlanToPendingGate' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed with no output.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-089

Slice: `fix(webconsole): roll back failed validation mutations`

Finding:

- Web mission validation patch returned required `goal-history.jsonl` append errors, but left simple current `goal.json` validation-plan data applied.
- A focused regression blocked `goal-history.jsonl`; before the fix, failed Web validation-plan patch left the new validation in `goal.json`.

Changes:

- Snapshotted the previous Goal before Web mission validation patch writes.
- Restored the previous Goal snapshot when `mission.validation.updated` history or event append fails and no linked Plan Mode was created.
- Extended focused Web validation-plan history-failure regression to assert rollback.

Validation:

- `go test -timeout 120s ./internal/webconsole -run TestServiceMissionValidationPlanPatchReportsHistoryAppendError -count=1`: failed before the fix because the failed validation-plan patch left the new validation in `goal.json`.
- `go test -timeout 120s ./internal/webconsole -run 'TestServiceMissionValidationPlanPatchReportsHistoryAppendError|TestServiceMissionValidationPatchResetsApprovedPlanToPendingGate' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed with no output.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-090

Slice: `fix(webconsole): roll back failed mission plan mutations`

Finding:

- Web mission-plan patch returned required `goal-history.jsonl` append errors, but left simple current `goal.json` mission-plan data applied.
- A focused regression blocked `goal-history.jsonl`; before the fix, failed Web feature-only mission-plan patch left the new feature in `goal.json`.

Changes:

- Restored the previous Goal snapshot when `mission.plan.updated` history or event append fails and no tasks or linked Plan Mode were created.
- Extended focused Web mission-plan history-failure regression to assert rollback.

Validation:

- `go test -timeout 120s ./internal/webconsole -run TestServiceMissionPlanPatchReportsHistoryAppendError -count=1`: failed before the fix because the failed mission-plan patch left the new feature in `goal.json`.
- `go test -timeout 120s ./internal/webconsole -run 'TestServiceMissionPlanPatchReportsHistoryAppendError|TestServiceMissionPlanPatchTaskSyncPreservesRuntimeProgressFacts|TestServiceMissionPlanPatchResetsApprovedPlanToPendingGate|TestServiceMissionPlanPatchNoopKeepsApprovedPlan' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed with no output.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-091

Slice: `fix(webconsole): roll back failed mission task sync`

Finding:

- Web mission-plan task sync returned required `goal-history.jsonl` append errors, but left generated task files and mission task IDs applied.
- A focused regression blocked `goal-history.jsonl`; before the fix, failed Web task-sync patch left `create_tasks_from_plan`, a generated `task_id`, and `task_0001.json` persisted.
- Store `SaveTasks` could not be used for exact rollback because it did not remove task files absent from the supplied snapshot.

Changes:

- Snapshotted tasks before Web mission-plan task sync.
- Restored the previous task set and previous Goal snapshot when `mission.plan.updated` history or event append fails and no linked Plan Mode was created.
- Made `SaveTasks` remove stale `task_*.json` files before writing the supplied task set.
- Added focused store and Web regressions for exact task snapshot rollback.

Validation:

- `go test -timeout 120s ./internal/webconsole -run TestServiceMissionPlanPatchTaskSyncReportsHistoryAppendError -count=1`: failed before the fix because the failed task-sync patch left the mission feature and generated task persisted.
- `go test -timeout 120s ./internal/session -run TestStoreSaveTasksRemovesStaleTaskFiles -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -run 'TestServiceMissionPlanPatchTaskSyncReportsHistoryAppendError|TestServiceMissionPlanPatchReportsHistoryAppendError|TestServiceMissionPlanPatchTaskSyncPreservesRuntimeProgressFacts' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/session/store.go internal/session/store_test.go internal/webconsole/service.go internal/webconsole/service_test.go`: passed with no output.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-092

Slice: `fix(webconsole): roll back failed mission plan gates`

Finding:

- Web mission-plan approval-gate patches returned required `goal-history.jsonl` append errors, but left the current `goal.json` mission patch and newly-created `planmode.json` applied.
- A focused regression blocked `goal-history.jsonl`; before the fix, failed Web `plan_status=needs_approval` patch left the mission feature and linked Plan Mode gate persisted.

Changes:

- Added public Plan Mode snapshot/restore helpers backed by existing Plan Mode rollback internals.
- Snapshotted Plan Mode before Web mission-plan patch side effects.
- Restored Plan Mode, task snapshot when captured, and Goal snapshot when `mission.plan.updated` history or event append fails.
- Added focused store and Web regressions for Plan Mode rollback.

Validation:

- `go test -timeout 120s ./internal/webconsole -run TestServiceMissionPlanPatchPlanModeReportsHistoryAppendError -count=1`: failed before the fix because the failed approval-gate patch left the mission patch and `planmode.json` persisted.
- `go test -timeout 120s ./internal/session -run TestRestorePlanModeSnapshotRemovesCreatedPlanMode -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -run 'TestServiceMissionPlanPatchPlanModeReportsHistoryAppendError|TestServiceMissionPlanPatchResetsApprovedPlanToPendingGate|TestServiceMissionPlanPatchNoopKeepsApprovedPlan' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/session/planmode.go internal/session/planmode_test.go internal/webconsole/service.go internal/webconsole/service_test.go`: passed with no output.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-093

Slice: `fix(webconsole): roll back failed generic goal gates`

Finding:

- Web generic goal patch could return required `goal-history.jsonl` append errors after applying mission patch side effects, including a newly-created Plan Mode gate.
- A focused regression blocked `goal-history.jsonl`; before the fix, failed Web generic mission patch left the mission feature and linked Plan Mode gate persisted.

Changes:

- Snapshotted tasks and Plan Mode before generic Web goal patch side effects.
- Restored Plan Mode, task snapshot when captured, and Goal snapshot when `goal.updated` history or event append fails.
- Added focused Web regression for generic goal-patch Plan Mode rollback.

Validation:

- `go test -timeout 120s ./internal/webconsole -run TestServiceGoalPatchMissionPlanModeReportsHistoryAppendError -count=1`: failed before the fix because the failed generic mission patch left the mission patch and `planmode.json` persisted.
- `go test -timeout 120s ./internal/webconsole -run 'TestServiceGoalPatchMissionPlanModeReportsHistoryAppendError|TestServiceGoalPatchReportsHistoryAppendError|TestServiceGoalPatchPreservesRuntimeProgressFacts|TestServiceGoalPatchMissionResetsApprovedPlanToPendingGate' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed with no output.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-094

Slice: `fix(webconsole): roll back failed validation gates`

Finding:

- Web mission validation contract patch could return required `goal-history.jsonl` append errors after applying validation contract and Plan Mode gate side effects.
- A focused regression blocked `goal-history.jsonl`; before the fix, failed approved-mission validation contract patch left the validation contract, approval reset, and linked Plan Mode gate persisted.

Changes:

- Snapshotted Plan Mode before Web mission validation patch side effects.
- Restored Plan Mode and Goal snapshots when `mission.validation.updated` history or event append fails.
- Added focused Web regression for validation-contract Plan Mode rollback.

Validation:

- `go test -timeout 120s ./internal/webconsole -run TestServiceMissionValidationContractPatchReportsHistoryAppendError -count=1`: failed before the fix because the failed validation contract patch left the validation contract, approval reset, and `planmode.json` persisted.
- `go test -timeout 120s ./internal/webconsole -run 'TestServiceMissionValidationContractPatchReportsHistoryAppendError|TestServiceMissionValidationPlanPatchReportsHistoryAppendError|TestServiceMissionValidationPatchResetsApprovedPlanToPendingGate' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed with no output.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-095

Slice: `fix(session): roll back failed goal creation`

Finding:

- Goal creation returned required `goal-history.jsonl` append errors, but left newly-created `goal.json` and generated mission task files applied.
- A focused regression blocked `goal-history.jsonl`; before the fix, failed mission goal creation left the current goal snapshot and generated task persisted.

Changes:

- Snapshotted the previous Goal before create-time side effects.
- Snapshotted tasks before create-time mission task sync when `create_tasks_from_plan` is enabled.
- Restored the previous task set and previous Goal snapshot if mission task sync, post-sync goal save, or the required `goal.created` history append fails.
- Added focused store coverage proving blocked goal history during creation rolls back both Goal and generated task facts.

Validation:

- `go test -timeout 120s ./internal/session -run TestCreateGoalReturnsHistoryAppendErrorAndRollsBack -count=1`: failed before the fix because failed create left `goal.json` persisted.
- `go test -timeout 120s ./internal/session -run 'TestCreateGoalReturnsHistoryAppendErrorAndRollsBack|TestStoreGoalLifecycleAccountingAndSummary|TestStoreSaveTasksRemovesStaleTaskFiles' -count=1`: passed.
- `go test -timeout 120s ./internal/session -run 'TestCreateGoalReturnsHistoryAppendErrorAndRollsBack|TestStoreGoalLifecycleAccountingAndSummary|TestStoreSaveTasksRemovesStaleTaskFiles|TestUpdateGoalAccountingReturnsHistoryAppendError|TestCompleteGoalReturnsHistoryAppendError|TestRecordGoalProgressReturnsHistoryAppendError|TestApproveMissionPlanReturnsHistoryAppendError' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/session/goal.go internal/session/store_test.go`: passed with no output.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-096

Slice: `fix(webconsole): roll back failed goal creation`

Finding:

- Web goal creation returned required `goal.created` event append errors, but left newly-created current Goal facts applied.
- A focused Web regression blocked `events.jsonl`; before the fix, failed goal creation left `goal.json`, `goal-history.jsonl`, generated mission task files, and linked `planmode.json` persisted.

Changes:

- Snapshotted tasks, Goal history, and Plan Mode before Web goal creation side effects.
- Restored linked Plan Mode, task set, current Goal, and Goal history when the required `goal.created` event append fails after store creation succeeds.
- Added a focused Web regression proving blocked `events.jsonl` rolls back goal creation side facts.
- Added `Store.RestoreGoalHistory` so adapter rollback can restore the pre-create Goal history stream after store creation appended `goal.created`.

Validation:

- `go test -timeout 120s ./internal/webconsole -run TestServiceGoalCreateReportsEventAppendErrorAndRollsBack -count=1`: failed before the fix because failed create left `goal.json` persisted.
- `go test -timeout 120s ./internal/webconsole -run TestServiceGoalCreateReportsEventAppendErrorAndRollsBack -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/session/goal.go internal/webconsole/service.go internal/webconsole/service_test.go`: passed with no output.
- `go test -timeout 120s ./internal/webconsole -run 'TestServiceGoalCreateReportsEventAppendErrorAndRollsBack|TestServiceGoalEndpointsMutateDurableGoal|TestServiceGoalPatchReportsHistoryAppendError|TestServiceMissionPlanPatchPlanModeReportsHistoryAppendError' -count=1`: passed.
- `go test -timeout 120s ./internal/session -run 'TestCreateGoalReturnsHistoryAppendErrorAndRollsBack|TestStoreGoalLifecycleAccountingAndSummary|TestStoreSaveTasksRemovesStaleTaskFiles' -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-097

Slice: `fix(runtime): report linked approval events`

Finding:

- Runtime linked mission approval appended mission approval history, then ignored failures appending the matching `mission.plan.approved` session event.
- A focused regression blocked `events.jsonl`; before the fix, `approveLinkedMissionPlan` returned nil while the mission was approved with no event fact.

Changes:

- Switched linked mission approval from best-effort `emit` to error-returning `appendEvent`.
- Preserved the approved current snapshot on event failure because `ApproveMissionPlan` already wrote the required `goal-history.jsonl` approval fact.
- Added focused runtime coverage for blocked `events.jsonl` during linked mission approval.

Validation:

- `go test -timeout 120s ./internal/runtime -run TestApproveLinkedMissionPlanReportsEventAppendError -count=1`: failed before the fix because no event append error was returned.
- `go test -timeout 120s ./internal/runtime -run 'TestApproveLinkedMissionPlanReportsEventAppendError|TestApproveLinkedPlanModeMarksMissionPlanApproved|TestApproveLinkedPlanModeBlocksUncoveredMissionValidation' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/runner.go internal/runtime/planmode_test.go`: passed with no output.
- `go test -timeout 120s ./internal/runtime -run 'TestApproveLinkedMissionPlanReportsEventAppendError|TestApproveLinkedPlanModeMarksMissionPlanApproved|TestApproveLinkedPlanModeBlocksUncoveredMissionValidation|TestRunnerFailBeforeRunReportsFailedEventAppendError' -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-098

Slice: `fix(runtime): report plan mode control events`

Finding:

- Runtime Plan Mode continue actions persisted Plan Mode transitions and replay facts, then ignored failures appending matching required session events.
- Focused regressions blocked `events.jsonl`; before the fix, Plan Mode cancellation returned success without `planmode.cancelled`, and Plan Mode approval continued into the provider turn without `planmode.plan_approved`.

Changes:

- Switched Plan Mode create, approval, execution-start, revision, and cancellation events in `Runner.Continue` from best-effort `emit` to error-returning `appendEvent`.
- Switched recovered Plan Mode input answer/cancel events from best-effort `emit` to error-returning `appendEvent`.
- Added blocked-event regressions proving cancellation and approval now report the durable event failure, and approval no longer starts the provider turn when the approval event is missing.

Validation:

- `go test -timeout 120s ./internal/runtime -run 'Test(CancelPlanModeReportsCancelledEventAppendError|ApprovePlanModeReportsPlanApprovedEventAppendError)' -count=1`: failed before the fix because cancellation and approval returned success while `events.jsonl` was blocked.
- `go test -timeout 120s ./internal/runtime -run 'Test(CancelPlanModeReportsCancelledEventAppendError|ApprovePlanModeReportsPlanApprovedEventAppendError|PlanInputCancelReturnsHistoryAppendError|CancelPlanModeDoesNotDuplicateRecoveredInputToolResult|ApproveLinkedMissionPlanReportsEventAppendError)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/runtime/runner.go internal/runtime/planmode_test.go`: passed with no output.
- `go test -timeout 120s ./internal/runtime -run 'Test(CancelPlanModeReportsCancelledEventAppendError|ApprovePlanModeReportsPlanApprovedEventAppendError|PlanInputCancelReturnsHistoryAppendError|CancelPlanModeDoesNotDuplicateRecoveredInputToolResult|ApproveLinkedMissionPlanReportsEventAppendError|TestRunnerFailBeforeRunReportsFailedEventAppendError)' -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-099

Slice: `fix(tools): report todo snapshot load errors`

Finding:

- `todo_write` ignored errors from `LoadTodo` while deciding whether the requested todo snapshot was a no-op.
- A focused regression replaced `todo.json` with a directory; before the fix, `todo_write {"todos":[]}` returned a successful no-op with `LLMOutput:"null"` and left the durable todo snapshot unreadable.

Changes:

- Propagated `LoadTodo` errors from `todo_write` before normalized no-op comparison.
- Preserved no-op timestamp behavior for readable existing todo snapshots.
- Added focused regression coverage for unreadable `todo.json`.

Validation:

- `go test -timeout 120s ./internal/tools -run TestTodoWriteReportsLoadErrorBeforeNoop -count=1`: failed before the fix because the tool returned a successful no-op against an unreadable `todo.json`.
- `go test -timeout 120s ./internal/tools -run 'TestTodoWrite(ReportsLoadErrorBeforeNoop|NoopDoesNotLookLikeProgress)|TestTodoAndTaskToolsEmitStructuredEvents' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/tools/registry.go internal/tools/registry_test.go`: passed with no output.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-100

Slice: `fix(webconsole): roll back failed linked plan gates`

Finding:

- Web goal and mission mutations wrote goal/task facts before creating the linked Plan Mode gate required for approval-gated goals.
- Focused regressions blocked `artifacts/planmode-history.jsonl`; before the fix, goal create and mission plan patch returned HTTP 500 while leaving the mutated goal/task facts behind with no durable linked Plan Mode gate.

Changes:

- Added restore helpers for Web goal create and Web goal/mission patch paths when linked Plan Mode creation fails.
- Restored previous `goal.json`, goal history, task snapshots, and Plan Mode snapshots for failed goal creation.
- Restored previous `goal.json`, task snapshots, and Plan Mode snapshots for failed goal / mission / validation patches.
- Added focused WebConsole regressions for failed linked Plan Mode creation during goal create and mission plan patch.

Validation:

- `go test -timeout 120s ./internal/webconsole -run 'TestService(GoalCreate|MissionPlanPatch)RollsBackWhenLinkedPlanModeCreationFails' -count=1`: failed before the fix because the failed responses left mutated goal/task facts.
- `go test -timeout 120s ./internal/webconsole -run 'TestService(GoalCreate|MissionPlanPatch)RollsBackWhenLinkedPlanModeCreationFails' -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -run 'TestService(GoalCreateReportsEventAppendErrorAndRollsBack|GoalCreateRollsBackWhenLinkedPlanModeCreationFails|GoalPatchReportsHistoryAppendError|GoalPatchMissionPlanModeReportsHistoryAppendError|MissionPlanPatchReportsHistoryAppendError|MissionPlanPatchTaskSyncReportsHistoryAppendError|MissionPlanPatchPlanModeReportsHistoryAppendError|MissionPlanPatchRollsBackWhenLinkedPlanModeCreationFails|MissionValidationContractPatchReportsHistoryAppendError|MissionValidationPatchResetsApprovedPlanToPendingGate|GoalEndpointsMutateDurableGoal)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed with no output.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-101

Slice: `fix(webconsole): roll back failed task sync`

Finding:

- Web goal patch and mission plan patch could persist `goal.json` changes before task synchronization failed.
- Focused regressions blocked `tasks/taskboard.lock`; before the fix, both routes returned HTTP 500 while leaving goal facts advanced to a task-synced mission shape without a matching task graph.

Changes:

- Restored the previous goal snapshot when generic goal patch fails to load the task snapshot after applying a patch.
- Restored previous goal/task snapshots when generic goal patch or mission plan patch task synchronization fails.
- Added focused WebConsole regressions for task-sync failure rollback in both routes.

Validation:

- `go test -timeout 120s ./internal/webconsole -run 'TestService(GoalPatch|MissionPlanPatch)RollsBackWhenTaskSyncFails' -count=1`: failed before the fix because failed task sync left mutated goal facts.
- `go test -timeout 120s ./internal/webconsole -run 'TestService(GoalPatch|MissionPlanPatch)RollsBackWhenTaskSyncFails' -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -run 'TestService(GoalPatchReportsHistoryAppendError|GoalPatchMissionPlanModeReportsHistoryAppendError|GoalPatchRollsBackWhenTaskSyncFails|MissionPlanPatchReportsHistoryAppendError|MissionPlanPatchTaskSyncReportsHistoryAppendError|MissionPlanPatchRollsBackWhenTaskSyncFails|MissionPlanPatchTaskSyncPreservesRuntimeProgressFacts|GoalCreateRollsBackWhenLinkedPlanModeCreationFails|MissionPlanPatchRollsBackWhenLinkedPlanModeCreationFails)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed with no output.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.

### FCA-20260526-102

Slice: `fix(webconsole): roll back failed goal event state`

Finding:

- Web goal clear/status/patch rollback paths could return HTTP 500 on blocked `events.jsonl` while leaving Goal history or a linked pending Plan Mode advanced past the rolled-back `goal.json`.

Changes:

- Snapshotted Goal history before rollback-capable Web goal mutations.
- Restored Goal history for event-stage `appendGoalMutation` failures without masking genuine history append failures.
- Restored linked Plan Mode snapshots when `EnsurePlanModeForGoal` linked an existing pending Plan Mode and a later required goal event append failed.
- Added focused regressions for failed goal clear/status event rollback and goal patch linked Plan Mode rollback.

Validation:

- `go test -timeout 120s ./internal/webconsole -run 'TestServiceGoal(Clear|Status)RollsBackHistoryWhenEventAppendFails' -count=1`: failed before the fix because failed event append left rolled-back goal history entries.
- `go test -timeout 120s ./internal/webconsole -run TestServiceGoalPatchRollsBackLinkedPlanModeWhenEventAppendFails -count=1`: failed before the fix because failed event append left an existing pending Plan Mode linked to the rolled-back goal.
- `go test -timeout 120s ./internal/webconsole -run 'TestServiceGoal(Clear|Status)RollsBackHistoryWhenEventAppendFails|TestServiceGoalPatchRollsBackLinkedPlanModeWhenEventAppendFails' -count=1`: passed.
- `go test -timeout 120s ./internal/webconsole -run 'TestService(GoalClearReportsHistoryAppendError|GoalStatusReportsHistoryAppendError|GoalPatchReportsHistoryAppendError|GoalPatchRollsBackLinkedPlanModeWhenEventAppendFails|MissionPlanPatchReportsHistoryAppendError|MissionPlanPatchTaskSyncReportsHistoryAppendError|MissionPlanPatchPlanModeReportsHistoryAppendError|MissionValidationPatchReportsHistoryAppendError|MissionValidationContractPatchReportsHistoryAppendError|GoalPatchMissionPlanModeReportsHistoryAppendError|MissionPlanPatchRollsBackWhenLinkedPlanModeCreationFails|GoalCreateRollsBackWhenLinkedPlanModeCreationFails)' -count=1`: passed.
- `git diff --check`: passed.
- `gofmt -l internal/webconsole/service.go internal/webconsole/service_test.go`: passed with no output.
- `go test -timeout 120s ./internal/webconsole -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/runtime -count=1`: passed.
- `node --check internal/webconsole/assets/app.js`: passed.
- `node --check internal/webconsole/assets/events.js`: passed.
- `node --check internal/webconsole/assets/session-view.js`: passed.
- `node --check internal/webconsole/assets/utils.js`: passed.
- `node validation/scripts/webconsole_utils_test.mjs`: passed.
- `go vet ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`: passed.
- `go test -timeout 120s ./cmd/... ./internal/app ./internal/config ./internal/events ./internal/extensions ./internal/fileutil ./internal/hooks ./internal/isolation ./internal/output ./internal/procutil ./internal/provider ./internal/review -count=1`: passed.
- `go test -timeout 120s ./internal/session ./internal/skills ./internal/tools -count=1`: passed.
- `go test -timeout 120s ./internal/tui ./internal/webconsole ./pkg/... ./validation/cmd/... -count=1`: passed.
