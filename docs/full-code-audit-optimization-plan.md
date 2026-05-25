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

## Reviewed Areas With No Confirmed New Issue Yet

These areas have been inspected enough to avoid duplicating already-fixed items, but the broad audit is still ongoing:

- Embedded asset ETag/gzip handling in `internal/webconsole/service.go` has current tests for ETag, gzip, q-value negotiation, and 304 behavior.
- Markdown link/image sanitizer currently uses `rel="noopener noreferrer"` and `.md-img`; previous inline-style/link-rel concerns are already fixed.
- Workspace browser path resolution uses `tools.ResolveWorkspacePath` and denies `.git`, `.go-cli-agent`, credential directories, and `.env` variants.
- Skill upload has multipart size, zip file count, per-entry size, total uncompressed size, traversal, absolute path, symlink destination, and direct-child uninstall checks.
- Static frontend syntax parses with Node.

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
