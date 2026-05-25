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
