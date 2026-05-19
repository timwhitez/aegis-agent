# Confidence Loop - 2026-05-15

## Objective

Assess whether the current `go-cli-agent` codebase can be treated as fully trusted. If not, identify loopholes, propose and apply proper fixes where feasible, verify them with real evidence, and repeat until either the project is factually confidence-closed or the 50-step cap forces closeout.

## Ground Rules

- Maximum loop markers: 50. At step 50, stop expanding scope and close out already discovered issues.
- Record every meaningful step with an explicit marker.
- Preserve existing unrelated dirty worktree changes.
- Current boundary follows the updated specs: Web-first `go-cli-agent web` is the default local operator surface; `init/run/exec/steer/continue/sessions/goal/tasks/probe-provider/doctor` remain CLI fallback, with queue/delegate as advanced profile surfaces and TUI as an extension surface.
- Confidence must be evidence-backed. Passing tests alone is not enough unless the tests cover the requirement being claimed.

## Prompt-To-Artifact Checklist

| Requirement | Evidence Target | Status |
| --- | --- | --- |
| Record the process in `context/` | This file | Done |
| Read required specs before code edits | `spec/00-product.md`, `spec/01-runtime-architecture.md`, `spec/03-provider-contracts.md`, `spec/09-phase-plan.md`, `spec/11-spec-audit-and-traceability.md`, `spec/12-task-system.md`, `spec/13-live-input-and-steering.md` | Done |
| Respect 50-step cap | Step ledger below | Done |
| Find possible loopholes | Code/spec/test review with file references | Done |
| Suggest proper fixes | Findings table and implementation notes | Done |
| Apply feasible fixes | Git diff and commit evidence | Done |
| Verify fixes | Targeted tests plus broader acceptance checks | Done |
| Completion audit | Checklist mapped to concrete current evidence | Done |

## Step Ledger

- STEP 01 - Goal and repo scope confirmed: active thread goal is an unbounded confidence/audit loop for `/mnt/c/Users/Admin/Desktop/build/simple_loop_with_tools/go-cli-agent`.
- STEP 02 - Memory quick pass completed: prior notes show this checkout recently changed Plan Mode, Goal/Mission approval, and completion-audit behavior; current work must re-check current files rather than trust memory.
- STEP 03 - Actual `AGENTS.md` read and applied at the time: mandatory specs first, then-current CLI-only core boundary, no fixed workflow engine, and code fixes must be committed after validation. This historical step has since been superseded by the Web-first AGENTS/spec direction.
- STEP 04 - Mandatory specs read: product, runtime architecture, provider contracts, phase plan, traceability, task system, and live input/steering.
- STEP 05 - Dirty worktree baseline captured: `issues.md` and `skillgap.md` deleted; `skills/timwhite-security-review/*` modified; untracked `CLAUDE.md`, `dev.md`, screenshots, `skills/pentest-toolset/`, and `workspace/`. These are treated as pre-existing unless proven related.
- STEP 06 - Repository shape inventoried: main surfaces include `cmd/`, `internal/app`, `internal/runtime`, `internal/session`, `internal/tools`, `internal/provider`, `internal/webconsole`, `pkg/agent`, and `validation/cmd`.
- STEP 07 - Baseline package listing completed for `./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`; packages resolve.
- STEP 08 - Initial risk scan started over path safety, shell execution, Plan Mode, goal completion, delegation, and WebConsole surfaces.
- STEP 09 - Baseline syntax/format gates started: `gofmt -l cmd internal pkg validation/cmd`, `node --check` for embedded WebConsole JS, and the repo package test command.
- STEP 10 - Provider HTTP audit found no response body size cap in `internal/provider/http.go`; a malicious or broken OpenAI-compatible endpoint can force large memory reads before JSON parsing.
- STEP 11 - WebConsole file browser audit found root-parent browsing is intentionally supported, but credential-like files such as `.env` are not denied from listing/read paths.
- STEP 12 - WebConsole skill upload audit found `ParseMultipartForm(50 << 20)` is only a memory threshold, not a hard request size cap, and zip entries are read/extracted without explicit file/total limits.
- STEP 13 - Implemented F-01: provider HTTP responses now have a 16 MiB hard cap and oversized responses become classified `response_parse_error`.
- STEP 14 - Implemented F-02: WebConsole file browser now hides/rejects `.env`, `.env.*` except examples/templates, credential directories, key filenames, and `credentials` paths.
- STEP 15 - Implemented F-03: WebConsole skill upload now uses `http.MaxBytesReader`; zip processing caps entry count, per-entry uncompressed bytes, and total uncompressed bytes.
- STEP 16 - Specs synced for hardening: `spec/03-provider-contracts.md` documents provider response caps; `spec/17-web-console.md` documents file-browser credential denial and skill upload/extraction caps.
- STEP 17 - Targeted regression tests passed for provider oversize response handling and WebConsole file-browser/zip hardening.
- STEP 18 - WebSocket origin audit found the upgrader accepted all origins. The channel is relay-only, but cross-origin browser WebSocket access is unnecessary exposure.
- STEP 19 - Implemented F-04: `/ws` now allows same-origin browser upgrades or no-Origin local clients, and rejects foreign origins.
- STEP 20 - Targeted WebSocket tests passed for deprecated control behavior, no durable reset echo, and foreign-origin rejection.
- STEP 21 - Rechecked F-03 after implementation and added an actual extracted-byte accumulator, so total zip expansion is enforced against bytes read, not only central-directory declared sizes.
- STEP 22 - Stale pre-patch full-suite run was interrupted and replaced with patched-tree validation.
- STEP 23 - Fresh full package suite passed on the patched tree with `go test -timeout=5m ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`.
- STEP 24 - Final syntax/format gates passed: `gofmt -l` clean and all three embedded WebConsole JS files passed `node --check`.
- STEP 25 - Shared file-read audit found `read_file`, `grep`, config/env reads, skill reads, and WebConsole file reads all rely on `fileutil.ReadRegularFileNoSymlink`; before F-05 it rejected symlinks but did not reject very large regular files before `io.ReadAll`.
- STEP 26 - Implemented F-05: shared regular-file reads now reject files over 16 MiB before full read; `spec/04-tools-and-skills.md` documents the cap for `read_file` and `grep`.
- STEP 27 - Added focused F-05 regression test using a sparse oversized file, so validation covers the stat-based rejection without allocating a large payload in the test itself.
- STEP 28 - F-05 review tightened the implementation again: after the stat check, the actual read also uses a 16 MiB + 1 byte limit so a file that grows after `Stat()` cannot force an unbounded `io.ReadAll`.
- STEP 29 - Targeted F-05 validation passed with `go test ./internal/fileutil ./internal/tools`.
- STEP 30 - Fresh full package validation passed after the final F-05 refinement with `go test -timeout=5m ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`.
- STEP 31 - Final clean gates passed on owned files: `gofmt -l cmd internal pkg validation/cmd`, WebConsole `node --check` for `app.js`, `api.js`, `session-view.js`, and scoped `git diff --check`.
- STEP 32 - Unscoped `git diff --check` was intentionally not used as closeout evidence because pre-existing unrelated whitespace remains in `skills/timwhite-security-review/SKILL.md`; owned-file scoped whitespace check is clean.
- STEP 33 - Continued after commit `01f8bd6` because the thread goal remained active; the post-commit dirty worktree still only contained unrelated pre-existing deletions/skill edits/untracked files.
- STEP 34 - Core tool/session boundary audit inspected workspace path resolution, shell timeout/env/output behavior, and session store readers; path/shell had existing targeted tests, while session JSON/JSONL readers still had uncapped read surfaces.
- STEP 35 - Implemented F-06: session JSON files now reuse the shared capped no-symlink regular-file reader, and JSONL readers now enforce a 16 MiB per-record cap before unmarshalling.
- STEP 36 - F-06 targeted validation passed for oversized session JSON, oversized JSONL records, and the existing symlink-read regressions; full `internal/session` package also passed.
- STEP 37 - CLI adapter stdin audit found `run`/`exec` stdin prompts, `continue --message` fallback stdin, and `steer --message` fallback stdin used unbounded `io.ReadAll`.
- STEP 38 - Implemented F-07: prompt stdin reads now use a 4 MiB hard cap with a clear error; `spec/02-cli-and-config.md` documents the limit.
- STEP 39 - F-07 targeted validation passed for oversized stdin prompt rejection and adjacent Plan Mode/prompt parsing regressions; combined `go test ./internal/app ./internal/session` passed.
- STEP 40 - Web/API control-surface audit found unsafe JSON mutation endpoints had Origin and Content-Type guards but no shared JSON request-body cap; multipart skill upload already had a separate cap from F-03.
- STEP 41 - Implemented F-08: unsafe WebConsole JSON mutation endpoints now use `http.MaxBytesReader` with a 4 MiB cap, and `spec/17-web-console.md` documents the JSON body limit.
- STEP 42 - F-08 targeted validation passed for oversized JSON mutation body rejection plus adjacent foreign-Origin/WebSocket/zip guard regressions.
- STEP 43 - Full suite rerun exposed a regression from F-06: `feature_list_read` still blocked a symlinked snapshot but the error was rewrapped as `feature list not found` instead of preserving the session symlink diagnostic.
- STEP 44 - Fixed the F-06 regression by keeping the session-store symlink-path precheck before the capped regular-file read.
- STEP 45 - Regression validation passed: `TestFeatureListToolsRejectSymlinkedSnapshot` and the F-06 oversized/symlink session-store tests now both pass.
- STEP 46 - Fresh full package validation passed after F-06/F-07/F-08 and the F-06 diagnostic regression fix with `go test -timeout=5m ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`.
- STEP 47 - Final clean gates passed on owned files: `gofmt -l cmd internal pkg validation/cmd`, WebConsole `node --check` for `app.js`, `api.js`, `session-view.js`, and scoped `git diff --check`.
- STEP 48 - Completion audit restated the objective as: keep a marked context ledger, stay under 50 steps, find/fix feasible loopholes, validate fixes with relevant tests plus broad gates, preserve unrelated dirt, and commit kept code/spec/test changes.
- STEP 49 - Scoped staging set prepared for the second continuation commit: context ledger, app stdin cap, session-store read caps, WebConsole JSON body cap, tests, and matching specs only.
- STEP 50 - Step cap reached for this loop. No further expansion is allowed in this loop; closeout proceeds with the discovered fixes and explicit non-absolute confidence statement.

## Candidate Findings

| ID | Area | Status | Evidence | Proposed Fix |
| --- | --- | --- | --- | --- |
| F-01 | Provider HTTP | Fixed, targeted verified | `internal/provider/http.go` previously read provider responses with `io.ReadAll` or an uncapped idle-timeout loop. | Added hard response cap and `TestOpenAIAdapterRejectsOversizedProviderResponse`. |
| F-02 | WebConsole file browser | Fixed, targeted verified | `resolveWorkspaceBrowserPath` intentionally allows parent navigation within server cwd; `handleReadFile` and `listDirectory` did not deny `.env` / credential-like names. | Added sensitive path denial and expanded `TestServiceWorkspaceRoutesListReadAndRejectEscape`. |
| F-03 | WebConsole skill upload | Fixed, targeted verified | `handleUploadSkill` lacked `http.MaxBytesReader`; `processSkillZip` used `io.ReadAll` per entry and no aggregate extracted-size/file-count cap. | Added upload/extraction caps and `TestProcessSkillZipRejectsOversizedEntry`. |
| F-04 | WebConsole WebSocket | Fixed, targeted verified | `websocket.Upgrader.CheckOrigin` returned `true` for all origins. | Added same-origin/no-Origin check and `TestServiceWebSocketRejectsForeignOrigin`. |
| F-05 | Shared regular-file reads | Fixed, targeted verified | `fileutil.ReadRegularFileNoSymlink` rejected symlinks but allowed any regular file size before `io.ReadAll`; this is used by `read_file`, `grep`, skill reads, config/env reads, and WebConsole file reads. | Added a 16 MiB stat/read cap and `TestReadRegularFileNoSymlinkRejectsOversizedFile`; synced `spec/04-tools-and-skills.md`. |
| F-06 | Session store readers | Fixed, targeted verified | `readJSONFile` used an uncapped `io.ReadAll`; `readJSONL` used `ReadBytes('\n')`, allowing a damaged or malicious local session/control file to allocate a very large JSON file or single JSONL record during restore/list/Web reads. | Reused capped no-symlink regular-file reads for JSON files, added a 16 MiB JSONL record cap, and added oversized JSON/JSONL session-store regressions; synced `spec/05-session-interrupt-resume.md`. |
| F-07 | CLI stdin prompt reads | Fixed, targeted verified | CLI prompt fallback paths for `run`/`exec`, `continue`, and `steer` read stdin with unbounded `io.ReadAll`. | Added a shared 4 MiB stdin prompt cap with explicit error and `TestReadPromptStdinRejectsOversizedInput`; synced `spec/02-cli-and-config.md`. |
| F-08 | WebConsole JSON mutation bodies | Fixed, targeted verified | Unsafe WebConsole JSON mutation endpoints required local-console headers and JSON content-type, but decoded request bodies without a shared size cap. | Added a 4 MiB `http.MaxBytesReader` cap for JSON mutation endpoints and `TestServiceRejectsOversizedJSONMutationBody`; synced `spec/17-web-console.md`. |

## Verification Log

- `go list ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...` resolved all target packages.
- `gofmt -l cmd internal pkg validation/cmd` produced no output.
- `node --check internal/webconsole/assets/app.js`, `api.js`, and `session-view.js` passed.
- `go test ./internal/provider -run 'TestOpenAIAdapterClassifiesResponseParseError|TestOpenAIAdapterRejectsOversizedProviderResponse|TestAdaptersClassifyNon2xxAndPropagateCancel'` passed.
- `go test ./internal/webconsole -run 'TestServiceWorkspaceRoutesListReadAndRejectEscape|TestProcessSkillZipRejectsTraversalEntries|TestProcessSkillZipRejectsSymlinkDestination|TestProcessSkillZipRejectsOversizedEntry|TestProcessSkillZipAllowsNestedSkillFiles'` passed.
- `go test ./internal/webconsole -run 'TestServiceWebSocketRejectsChatControl|TestServiceWebSocketRejectsForeignOrigin|TestServiceWebSocketResetSessionDoesNotEmitDurableEcho'` passed.
- `go test ./internal/webconsole -run 'TestProcessSkillZipRejectsOversizedEntry|TestProcessSkillZipAllowsNestedSkillFiles'` passed after adding actual extracted-byte accounting.
- `go test -timeout=5m ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...` passed on the patched tree.
- `go test ./internal/fileutil ./internal/tools` passed after F-05.
- Final `go test -timeout=5m ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...` passed after all code edits.
- Final `gofmt -l cmd internal pkg validation/cmd` produced no output.
- Final `node --check internal/webconsole/assets/app.js`, `api.js`, and `session-view.js` passed.
- Scoped `git diff --check -- context/confidence-loop-20260515.md internal/provider/http.go internal/provider/provider_test.go internal/webconsole/service.go internal/webconsole/service_test.go internal/fileutil/safe.go internal/fileutil/safe_test.go spec/03-provider-contracts.md spec/04-tools-and-skills.md spec/17-web-console.md` passed.
- Unscoped `git diff --check` reports unrelated pre-existing trailing whitespace in `skills/timwhite-security-review/SKILL.md`, which was not edited or staged by this loop.
- `go test ./internal/session -run 'TestStoreLoadStateRejectsOversizedJSON|TestStoreLoadMessagesRejectsOversizedJSONLRecord|TestStoreLoadStateRejectsSymlinkJSON|TestStoreLoadMessagesRejectsSymlinkJSONL'` passed.
- `go test ./internal/session` passed.
- `go test ./internal/app -run 'TestReadPromptStdinRejectsOversizedInput|TestRunCommandParsesPlanFlags|TestRunCommandDoesNotInferPlanModeFromPromptText'` passed.
- `go test ./internal/app ./internal/session` passed.
- `go test ./internal/webconsole -run 'TestServiceRejectsForeignOriginMutation|TestServiceRejectsOversizedJSONMutationBody|TestServiceWebSocketRejectsForeignOrigin|TestProcessSkillZipRejectsOversizedEntry'` passed.
- First full rerun after F-06/F-07/F-08 failed at `TestFeatureListToolsRejectSymlinkedSnapshot`; this was treated as a real regression and fixed before closeout.
- `go test ./internal/tools -run 'TestFeatureListToolsRejectSymlinkedSnapshot'` passed after preserving session symlink diagnostics.
- Final `go test -timeout=5m ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...` passed after F-06/F-07/F-08.
- Final `gofmt -l cmd internal pkg validation/cmd` produced no output.
- Final `node --check internal/webconsole/assets/app.js`, `api.js`, and `session-view.js` passed.
- Scoped `git diff --check -- context/confidence-loop-20260515.md internal/app/app.go internal/app/app_test.go internal/session/store.go internal/session/store_test.go internal/webconsole/service.go internal/webconsole/service_test.go spec/02-cli-and-config.md spec/05-session-interrupt-resume.md spec/17-web-console.md` passed.

## Closeout Notes

- This loop reached STEP 50, the requested cap.
- Eight concrete loopholes were found and fixed: provider response memory cap, WebConsole credential-file browser denial, skill upload/zip extraction caps, WebSocket foreign-origin rejection, capped shared regular-file reads, capped session JSON/JSONL reads, capped CLI stdin prompt reads, and capped WebConsole JSON mutation bodies.
- Confidence level: materially improved for the reviewed provider, file-browser, skill-upload, WebSocket, shared file-read, session-store read, CLI stdin, and WebConsole JSON mutation surfaces, backed by targeted regressions and full package validation. This is not an absolute claim that every project surface is loophole-free.
- Known residuals: unrelated dirty worktree changes remain outside this loop; unscoped whitespace checking is contaminated by those unrelated edits; no live browser/UI smoke was run because the changes were server-side guardrails and package tests cover the affected handlers; the 50-step cap prevents further expansion in this loop.
