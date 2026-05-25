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

## Reviewed Areas With No Confirmed New Issue Yet

These areas have been inspected enough to avoid duplicating already-fixed items, but the broad audit is still ongoing:

- Embedded asset ETag/gzip handling in `internal/webconsole/service.go` has current tests for ETag, gzip, q-value negotiation, and 304 behavior.
- Markdown link/image sanitizer currently uses `rel="noopener noreferrer"` and `.md-img`; previous inline-style/link-rel concerns are already fixed.
- Workspace browser path resolution uses `tools.ResolveWorkspacePath` and denies `.git`, `.go-cli-agent`, credential directories, and `.env` variants.
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

## Update Log

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
