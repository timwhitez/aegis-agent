# TODOs — Actionable Fix List from Remote Task-History Analysis

> This document consolidates two analysis rounds (Round 1 on 2026-07-04, Round 2 on 2026-07-06) into a **check-off-able, item-by-item** fix list.
> Every item follows the same shape: **Background → Evidence → Root cause (with `file:line`) → Fix → Acceptance criteria → Priority**.
> When you finish an item, flip `- [ ]` to `- [x]` and record the commit short-hash + verification result in that item's **Status** line.
>
> Terminology and code anchors are given in English against the current `HEAD = e88be3a`. If a line number has drifted, search for the quoted symbol name (they are stable) rather than trusting the number.

---

## 0. Analysis Baseline & Method (context — do not delete)

- **Subject**: 14 sessions under remote `guangzhe.zhang@10.37.107.237:~/.go-cli-agent/sessions/`. 9 of them are complete audit-style tasks run with `openai / gpt-5.4`, spanning 2026-06-17 → 2026-07-03. Both rounds analyzed the same set; **no new sessions** appeared between rounds.
- **Method**: Batch-parsed each session's `events.jsonl` (`tool.before` / `tool.after` / `turn.stopped`), `provider-attempts.jsonl`, and `state.json`. Beyond counting error strings, Round 2 **recovered the actual call arguments** behind each error and traced them back to source to pin root cause.
- **Code baseline**: Local `HEAD = e88be3a`. The remote deployed binary was built 2026-07-02 17:31 (between commits `ddfdd62` and `e88be3a`). This gap is how we distinguish **already-fixed / still-present / deploy-lag**.

### Session inventory (for reference)

| session id | events | provider attempts | note |
|---|---|---|---|
| 20260701-125936-67e0c3 | 4077 | 275 | largest task; glob `limit` error ×19 (pre-fix) |
| 20260703-083131-5d5e32 | 1501 | 133 | compacted pollution ×2, interrupts ×3, old git |
| 20260702-071439-fc6d14 | 1065 | 91 | compacted pollution ×2, spill read miss |
| 20260701-131539-14ac50 | 1079 | 96 | finish consistency guard ×… |
| 20260701-131539-70b188 | 1008 | 85 | glob `path` error |
| 20260702-030837-00f28d | 921 | 83 | missing torch/pytest |
| 20260701-140300-aebb86 | 935 | 81 | `.context` vs `context` guard, grep `|` misuse |
| 20260701-131539-ee6bc1 | 786 | 63 | — |
| 20260702-064955-58840a | 394 | 31 | — |
| 20260617-131601-0d4337 | 22 | 3 | earliest; missing `session.json` |
| (others: 0d760e / 8b86e9 / 4a213e) | 2–4 | — | stub sessions, no real work |

### Aggregate tool error rates (all sessions)

| tool | total | errors | err% | pointer |
|---|---|---|---|---|
| read_file | 1783 | 22 | 1.2% | path guessing → T5 |
| grep | 629 | 3 | 0.5% | multi-path `|` misuse → T5 |
| write_file | 83 | 3 | 3.6% | includes compacted pollution → T1 |
| shell | 77 | 17 | 22.1% | **mostly legitimate non-zero exits, not harness bugs** → see "Misread clarification" + T4 |
| glob | 72 | 20 | 27.8% | `limit` fixed (T2); `path` still missing (T3) |
| finish | 12 | 3 | 25.0% | consistency guard firing correctly → T8 |

**`turn.stopped` reasons** (sample `fc6d14`): `935 tool_use` + `2 done_candidate`, **no abnormal stop reason** — the main loop is healthy.
**Compaction events**: `174 compact.reused` + `8 compact.started` + `8 compact.finished` — compaction fires often on long tasks, and heavy reuse **amplifies the T1 pollution** (each reuse re-feeds the poisoned sample to the model).
**Provider layer**: only 4 `openai: context deadline exceeded` across all sessions, all recovered by retry (`max_attempts=5`). No fatal provider failures.

### ⚠️ Misread clarification (correction of a Round-1 conclusion — do not delete)

**The shell 22.1% "error rate" does NOT mean the harness has a 22% defect rate.** After recovering all 17 `is_error` shell results, the real breakdown is:

| category | count | harness fault? |
|---|---|---|
| Command ran fine but the business/sub-path failed (`ls a b c` with one missing path; `git diff` on an invalid path; a `python -c` script whose own `assert` failed; a script that printed output but exited non-zero) | ~7 | ❌ |
| Missing dependency (`No module named pytest` / `torch`) | 2 | ❌ (environment, T6) |
| Non-git dir / git version drift (`not a git repository`; `unknown option 'show-current'`) | 3 | ❌ (model behavior, T5) |
| Model wrote a broken command itself (unbalanced quote → `unexpected EOF`) | 1 | ❌ (model behavior) |
| **Compacted pollution** (`unexpected field "compacted_for_context"` / `"original_chars"`) | 2 | ✅ (T1) |
| User interrupt (`[Tool execution was interrupted]`) | 1 | ❌ (expected semantics, T4) |

The shell tool's `exit_code != 0 → IsError=true` (`registry.go:769-775`) is **semantically correct** — the command genuinely failed. But folding it into a "tool error rate" systematically overstates harness defects and misleads every future automated analysis. → **T4** fixes this at the observability root.

---

## 1. Fix Items

### - [x] T1 [CRITICAL · code bug · fixed] Compaction replaces historical tool-call arguments with a pseudo-JSON object, which the model copies verbatim → subsequent calls are rejected by strict schema validation

**Background**: CLAUDE.md constrains that "compaction may only alter the context view sent to the model, never overwrite the raw log." On long tasks, when compaction fires, the harness compresses historical tool-call arguments to save tokens. The problem is the **shape of the compressed result**: it looks like a valid argument object, so the model treats it as a template and copies it into new calls.

**Evidence**: In `20260702-071439-fc6d14` and `20260703-083131-5d5e32`, `write_file` / `shell` returned `unexpected field "compacted_for_context"` / `"original_chars"` — 4 times total. Tracing back to `tool.before`, the arguments the model actually emitted were:
```json
{"compacted_for_context": true, "head_tail": "[Compacted previous_tool_arguments; original_chars=5340]\nHEAD:\n{\"path\":\"...step-6-vllm.md\",\"content\":\"..."}
```

**Root-cause chain**:
- `internal/runtime/compaction.go:703` `compactRawJSONForContext` replaces the whole argument blob with `{"compacted_for_context":true,"original_chars":N,"head_tail":"..."}` (`:713-715`), and the `head_tail` string itself (built at `:736`) further embeds `[Compacted ...; original_chars=N]\nHEAD:\n...` text → a **pseudo-JSON object plus an embedded pseudo-marker**, doubly inviting imitation.
- `internal/runtime/compaction.go:657` also writes `compacted_for_context:true` into `result.Metadata`.
- Seeing many historical calls all shaped this way, the model copies the shape as if it were a legal template.
- `internal/tools/registry.go:296` `validateClosedToolObject` strictly validates schemas declared `additionalProperties:false`; an unknown field returns `unexpected field %q` at `:301`, the call fails, and a turn is wasted.
- `compact.reused=174` means the poisoned sample is re-fed at high frequency, amplifying the failure probability.

**Fix (pick one; option 2 recommended)**:
1. Make the compressed argument no longer resemble a business object — use an obviously non-copyable placeholder such as `{"__elided__":"arguments omitted for context; N chars"}`, and state in the system prompt that `__elided__` is a read-only replay marker that must never appear in a new call.
2. **(Recommended)** Do not do a "pseudo-JSON" replacement of old arguments at all. Instead, truncate only the string *values* at the render layer while keeping the original key structure — the model then still sees `{"path":...,"content":"[...omitted 5000 chars...]"}`, a legal shape, and copying it will not trip schema validation.
3. Backstop: in `decodeClosedToolArgs` (`registry.go:267`), before rejecting, detect harness-reserved fields (`compacted_for_context` / `head_tail` / `original_chars`) and return a targeted correction ("this is a context-replay marker; resend with real arguments") instead of the generic `unexpected field`. Recommended combination: option 2, or option 1 + option 3.

**Acceptance criteria**: A unit test that calls `compactRawJSONForContext` then `validateClosedToolObject` on the compacted view, asserting the compressed view contains no business field name that strict schema would reject. Ideally also a long-session integration test that forces compaction and confirms no `unexpected field` error follows.

**Constraint reminder**: Whatever option is chosen, the raw `events.jsonl` / stored messages must not be rewritten — compaction is a view-layer transform only (CLAUDE.md).

**Status**: fixed · commit: 2231419 · verification: `go test ./internal/runtime -run 'TestCompactRawJSONForContextPreservesClosedToolArgumentShape|TestCompactionTruncatesOldToolOutput|TestCompactionTruncatesProviderBlockToolArguments|TestCompactorDoesNotMutateSourceToolResultMetadata' -count=1`; `go build ./...` · Priority **P0**

---

### - [x] T2 [fixed · regression covered] glob's `limit` field was once absent from the schema, causing a 27.8% error rate

**Background**: This is the canonical failure where a tool's description promises/implies a capability that the schema does not expose, so the model keeps hitting a wall.

**Evidence**: In `20260701-125936-67e0c3` (pre-fix), `glob: Error: unexpected field "limit"` fired **19 consecutive times** — the model repeatedly tried to narrow results with `limit`, was rejected, did not learn from the error, and kept retrying.

**Current state**: `limit` was added to `defGlob` by commit `1ea62e6` (07-01 22:15) at `internal/tools/registry.go:1030`. Post-fix sessions show no such error.

**Remaining TODO (what this item is about)**: Add a unit test that walks every `Definition` and asserts "every tunable parameter name mentioned in the description exists in the InputSchema," to prevent the whole class at the source. Merge into the same consistency test as **T3 / T9**.

**Status**: fixed · commit: eddeb21 · verification: `go test ./internal/tools -run 'TestToolDescriptionsMentionOnlyDeclaredParameters|TestDiscoveryToolParameterSetsStayAligned|TestBuiltinToolExecutionRejectsHarnessReplayMarkersWithTargetedHint|TestBuiltinToolExecutionRejectsUnknownTopLevelField|TestGlobAcceptsPathAndScopesResults|TestGlobHonorsLimitAndReportsTruncation' -count=1`; `go test ./internal/tools -count=1 -timeout=180s`; `go build ./...` · Priority **P2**

---

### - [x] T3 [MEDIUM · code · fixed] glob is missing a `path` parameter — discovery-tool API asymmetry (sibling of T2)

**Background**: Of the three discovery tools `grep` / `grep_files` / `glob`, the first two accept a `path` sub-directory anchor; only `glob` does not. The model reasonably assumes glob can be scoped to a sub-directory too, and hits a wall.

**Evidence**: In `20260701-131539-70b188`, `glob` returned `unexpected field "path"` for arguments:
```json
{"pattern": "vllm/**", "path": "."}
```

**Root cause — parameter asymmetry**:

| tool | pattern | path | include | limit |
|---|:-:|:-:|:-:|:-:|
| `grep` (`registry.go:1092`) | ✓ | ✓ | ✓ | ✓ |
| `grep_files` (`registry.go:1213`) | ✓ | ✓ | ✓ | ✓ |
| `glob` (`registry.go:1017`) | ✓ | ✗ | ✗ | ✓ |

**Fix**: Add an optional `path` (workspace-relative sub-directory anchor) to `defGlob` (`registry.go:1023-1035`), with semantics aligned to `grep_files` / `grep`. `glob("vllm/**", path=".")` should be equivalent to `glob("vllm/**")`. Reuse the existing `resolveGrepRoot` (`registry.go:1670`) to resolve the sub-directory root, then run `GlobWalk` against that root. Reuse `normalizeGrepFilesLimit` semantics already in place. Consider whether to also add `include` for full symmetry, or explicitly document that glob's `pattern` already covers the include use-case — either is fine, but state the decision in the description so the asymmetry is intentional and visible.

**Acceptance criteria**: Unit test — `glob` with a `path` argument no longer errors and results are correctly scoped to the sub-directory; folded into the T2 consistency test.

**Status**: fixed · commit: 2617a6b · verification: `go test ./internal/tools -run 'TestGlobAcceptsPathAndScopesResults|TestGlobHonorsLimitAndReportsTruncation|TestGlobSkipsSymlinkEscapes|TestGlobRejectsEmptyPattern' -count=1`; `go test ./internal/tools -count=1 -timeout=180s`; `go build ./...` · Priority **P1**

---

### - [x] T4 [MEDIUM · observability · fixed] Add `failure_class` to tool.after to separate harness defects / business failures / interrupts / schema rejections

**Background**: Today `tool.after.is_error` is a single boolean that lumps together "command business failure," "user interrupt," "harness bug," and "schema rejection" (see "Misread clarification"). This is why numbers like shell 22% get misread, and **every automated analysis round re-steps on the same rake**. Fixing this makes all future analysis sharper — it is a meta-improvement, not a one-off.

**Current state / root cause**:
- shell non-zero exit → `IsError=true` (`registry.go:769-775`) — correct, but semantically coarse.
- interrupt result → also `IsError=true` (the interrupt branch in `internal/runtime/engine.go` writes a replayable interrupt result) — correct recovery behavior, but counted as an error.
- schema rejection (`unexpected field`) → surfaces as an error result — a genuine harness/model-protocol issue.

**Fix**: Add a `failure_class` field to the `tool.after` `eventData` (`engine.go:934-939`) and/or `ToolResult.Metadata`, with values: `harness_error` / `command_nonzero_exit` / `interrupted` / `schema_reject` / `not_found`.
- shell branch: `err != nil && exitCode != 0 && ctx.Err()==nil` → `command_nonzero_exit`; interrupt branch → `interrupted`.
- closed-schema rejection → `schema_reject`.
- file-not-found in read_file/grep → `not_found`.
- all other IO/internal errors → `harness_error`.
The analysis script then filters out non-harness classes to get the true harness error rate. Keep `is_error` as-is for backward compatibility; `failure_class` is additive.

**Acceptance criteria**: Unit tests assert each branch stamps the correct `failure_class`. Re-run the analysis script over the remote historical events; shell's "true harness errors" should drop to 2 (the compacted pollution from T1).

**Status**: fixed · commit: 4caab5b · verification: `go test ./internal/runtime -run 'TestEngineToolAfterFailureClassifiesErrors|TestEngineWritesInterruptedToolResultOnPause|TestEngineToolInterruptedReportsEventAppendErrorWithReplayResult' -count=1`; `go test ./internal/runtime -count=1 -timeout=180s`; `go test ./internal/tools -count=1 -timeout=180s`; `go build ./...`; remote historical replay over 9 sessions found shell errors split as `command_nonzero_exit=11`, `interrupted=3`, `schema_reject=3` (the issue text predicted 2 schema/harness-actionable shell errors, but the historical events contain 3 compacted-argument schema rejects) · Priority **P1**

---

### - [x] T5 [MEDIUM · prompt/description · fixed] read_file / grep path guessing and multi-path concatenation misuse

**Background**: On large multi-repo audits, gpt-5.4 tends to guess file paths from memory and `read_file` them directly, instead of locating them first with a discovery tool. This is model behavior; per CLAUDE.md, "first improve descriptions and observable facts, do not hard-code a workflow."

**Evidence**:
- read_file: 22 × `file does not exist` (`.py` 16, `.json` 4). The signature is guessing the source layout then reading directly (e.g. `vllm/vllm/lora/models.py`, `serving_chat.py`). In session `67e0c3` alone there were ≥4 consecutive not-founds under the same prefix. Today a not-found is a bare IO error with no guidance — see `resolveReadFilePath` (`registry.go:1443`) and the read at `registry.go:847`.
- grep: multiple paths crammed into a single `path` with `|`: `path "litellm/.../guardrail_endpoints.py|litellm/.../guardrails.py" does not exist` — the model assumed `path` accepts regex/multi-value.

**Fix**:
1. Attach a suggestion to the read_file not-found error: "locate the path with grep_files/glob before reading."
2. Make the `grep` `path` description (`registry.go:1103-1106`) explicit: "a single file or directory; does not accept `|` or glob; for multiple paths call repeatedly or use the `include` filter."
3. (Optional, via harness reminder) After **≥N consecutive not-founds under the same directory prefix**, a reminder nudges the model toward discovery tools — no hard-coded workflow. This aligns with the existing reminder machinery in `prompt.go` (`nextHarnessReminder`, `collectRecentToolStats`).

**Acceptance criteria**: Description changes need no regression test; the reminder logic gets a unit test. Manually sample the read_file not-found rate in later sessions.

**Note**: Implement **together with T17** (same description-guidance surface, same failure mode).

**Status**: fixed · commit: a16315c · verification: `go test ./internal/runtime -run 'TestBuildSystemPromptIncludesDirectToolGuidance|TestNextHarnessReminderNudgesAfterRepeatedReadFileNotFound' -count=1`; `go test ./internal/tools -run 'TestReadFileNotFoundSuggestsDiscoveryFirst|TestCoreToolDescriptionsGuideSelection|TestToolDescriptionsMentionOnlyDeclaredParameters' -count=1`; `go test ./internal/runtime -count=1 -timeout=180s`; `go test ./internal/tools -count=1 -timeout=180s`; `go build ./...` · Priority **P2**

---

### - [x] T6 [LOW · environment · doctor/docs · fixed] Missing python deps (torch / pytest) cause the model to misjudge "validation failed"

**Background**: On audit tasks the model tries to actually run the target project/tests, but the deployment environment lacks the dependencies. This is an environment issue, not a harness bug, but it leads the model to a false "validation failed" conclusion.

**Evidence**: `No module named 'torch'`, `No module named pytest` (`20260702-030837-00f28d`).

**Fix**: Have `doctor` or the web console probe and display the runtimes/interpreters and common dependencies available in the workspace; or record them in the task's opening environment snapshot so the model does not blindly try. **Do not hard-code this into a tool guard.**

**Acceptance criteria**: `doctor` output includes an interpreter/dependency probe section.

**Status**: fixed · commit: 34511a0 · verification: `go test ./internal/app -run 'TestDoctorCommandJSONSkipsProbeWhenAPIKeyMissing|TestDoctorRuntimeEnvironmentProbeReportsPythonModules' -count=1`; `go test ./internal/app -count=1 -timeout=180s`; `go build ./...` · Priority **P3**

---

### - [x] T7 [MEDIUM · code · fixed] Spill-artifact read miss — the harness handed over the exact path, but the model still fabricated a turn number

**Background**: When an ephemeral tool (shell/glob/grep) output exceeds its window, `engine.go:908-928` writes the full text to `<sessionDir>/artifacts/tool-outputs/<tool>-turn<N>.txt` and replaces the LLMOutput with a prompt: `[Full output saved to <absolute path> (N lines). Read it with read_file(path="<absolute path>", ...)]`. The naming format is `ephemeralArtifactPath` at `engine.go:1157-1159`.

**Evidence** (`fc6d14`): The harness explicitly handed over `.../artifacts/tool-outputs/shell-turn29.txt`, but the model's subsequent read_file targeted `.../shell-turn34.txt` (nonexistent) → not-found. The model did not copy the exact path from the prompt; it fabricated one from the current turn number. This is a "the answer was placed in its hand and it still erred" case — the fix is entirely on the harness side.

**Fix (pick one or combine)**:
1. Strengthen the spill prompt: "**Copy the path above verbatim; do not derive it from the current turn number.**"
2. **(Recommended)** Name the spill artifact by a stable `call_id` (`shell-<first 8 of call_id>.txt`) instead of `turn<N>` (`engine.go:1159`) — the turn number invites the faulty inference "turns increment, so the filename must increment too."
3. When read_file hits the `artifacts/tool-outputs/` prefix but the file is absent, return a targeted hint and list the spill filenames that actually exist in that directory (near the read at `registry.go:847`).

**Acceptance criteria**: After the rename, a unit test asserts the spill path contains no turn number. Construct an over-window output and assert the model-facing prompt path matches the on-disk filename exactly.

**Note**: Coordinates with T11 (prefer relative paths — shorter, easier for the model to copy verbatim).

**Status**: fixed · commit: 3eafd32 · verification: `go test ./internal/runtime -run 'TestEngineEphemeralArtifactGuidanceAvoidsReadFileLoop|TestEngineEphemeralArtifactRejectsSymlinkTarget' -count=1`; `go test ./internal/runtime -count=1 -timeout=180s`; `go build ./...` · Priority **P1**

---

### - [ ] T8 [LOW · prompt/reminder] finish's report-consistency guard could surface its info earlier

**Background**: `finish` errored 25% (3/12), all `Report-consistency guard: supporting docs changed after the final deliverable ...` (`internal/runtime/prompt.go` — see `reportConsistencyGuard` at `:580` and related at `:593`,`:629`). The guard itself works correctly: in `aebb86` the model recovered on its own via `read → edit_file → finish` with no infinite loop.

**Optimization**: The model only learns "the final deliverable must be newer than the supporting docs" **after finish is already rejected**. A harness reminder issued **right after** writing a supporting doc like `reports/progress.md` / `validation.md` ("the final conclusion file is now stale; rewrite it before finish") would save one failed finish round-trip. This is description/reminder-layer, consistent with "do not hard-code a workflow into the guard."

**Acceptance criteria**: Unit test on the reminder trigger; sample whether first-attempt finish success rate improves.

**Status**: not fixed · commit: — · Priority **P3**

---

### - [x] T9 [MEDIUM · code · fixed] Tier the strict-schema rejection message + add a tool-parameter consistency test

**Background**: `internal/tools/registry.go:301` returns the same `unexpected field %q` for every unknown field. Given T1/T2/T3, this validation is double-edged: it blocks dirty args, but for "model misled by context" or "description/schema out of sync" it gives an undifferentiated message the model struggles to self-correct from.

**Fix**:
1. For known harness-reserved fields (`compacted_for_context` / `head_tail` / `original_chars`) return a targeted hint (merge with T1 option 3).
2. **Add a consistency test** that walks every `Definition` and asserts: (a) every parameter name appearing in the description exists in the InputSchema; (b) the discovery tools (grep/grep_files/glob) share an aligned common parameter set (this subsumes the T2 and T3 regressions).

**Acceptance criteria**: New test passes; deliberately removing one schema field while leaving its description mention should make the test fail.

**Status**: fixed · commit: eddeb21 · verification: `go test ./internal/tools -run 'TestToolDescriptionsMentionOnlyDeclaredParameters|TestDiscoveryToolParameterSetsStayAligned|TestBuiltinToolExecutionRejectsHarnessReplayMarkersWithTargetedHint|TestBuiltinToolExecutionRejectsUnknownTopLevelField|TestGlobAcceptsPathAndScopesResults|TestGlobHonorsLimitAndReportsTruncation' -count=1`; `go test ./internal/tools -count=1 -timeout=180s`; `go build ./...` · Priority **P2**

---

### - [ ] T10 [LOW · resilience · code] Read-only metadata tools should return an empty-state semantic result when the fact file is missing, not a bare IO error

**Background**: The earliest session `20260617-131601-0d4337` lacks `session.json` (likely an early version used `state.json`, and old dirs were not backfilled after the schema migration), causing `get_goal` / `todo_read` / `get_plan_mode` to each error once with `open .../session.json: file does not exist`. This is legacy debt, but it exposes a resilience gap: the model reads "no record yet" as "the tool is broken."

**Fix**: When the fact file is absent, these read-only metadata tools should return an **empty-state semantic result** ("no goal recorded" / "no todos" / "plan mode inactive") instead of a filesystem error. **Do not change the fact-source write logic.**

**Acceptance criteria**: Unit test — with `session.json` absent, assert these three tools return empty-state rather than `is_error`.

**Status**: not fixed · commit: — · Priority **P3**

---

### - [ ] T11 [LOW · docs/path convention] `.context` vs `context` ambiguity & remote absolute-path prefix inconsistency

**Background**:
- `20260701-140300-aebb86`: the external instruction asked for delivery to `context/security-review/...`, but the model wrote to `.context/...` (leading dot) and was corrected by the artifact-path guard. The guard worked correctly (a user-specified delivery path is a hard boundary).
- The remote `ls ~` resolves to `/home/guangzhe.zhang/...`, but the absolute paths frozen into session facts are `/data00/home/guangzhe.zhang/...`. They currently point to the same place via symlink/mount, but if the mount point changes, every frozen absolute path (including T7's spill paths and T10's session.json path) breaks.

**Fix**:
1. No code change; advise the user to write delivery paths fully to avoid `.context` vs `context` ambiguity.
2. Prefer **session-relative / workspace-relative** paths for fact sources and model-facing artifact paths; resolve to absolute only when necessary (coordinates with T7 option 2 — shorter relative paths are easier to copy verbatim).

**Acceptance criteria**: Sample whether artifact paths are now relative.

**Status**: not fixed · commit: — · Priority **P3**

---

### - [ ] T12 [engineering discipline · deploy] The deployed binary lags behind the fix commits

**Background**: The remote binary was built 07-02 17:31, while the glob `limit` fix (T2) landed 07-01 22:15 — yet multiple 07-01/07-02 sessions still reproduced the issue, proving the **deploy did not follow the commit**. CLAUDE.md already requires "a real commit after verification passes"; this adds the missing "redeploy" step.

**Fix**: Codify the flow "verification passes → `git commit` → rebuild and sync the remote deployed binary → regression." After T1/T3/T7 are fixed, the binary must be rebuilt and a regression run performed on the remote.

**Acceptance criteria**: Remote `go-cli-agent version` / binary build timestamp is newer than the latest fix commit.

**Status**: not fixed · commit: — · Priority **P2**

---

## 2. Prompt Improvements (borrowed from Codex App, 2026-07-06)

> **Source**: A breakdown of 6 Codex Desktop App prompt files (build `ea1c60319a1dcb19`) under `refer_prompts/`.
> **Compared against**: this project's `internal/runtime/prompt.go` (the `## Tool Use` section built in `buildSystemPrompt`, plus `runtimeBehaviorNotes` and the various guards/reminders).
> **Selection principle** (aligned with CLAUDE.md): borrow only the parts that "guide model decisions via description / system prompt / reminder." **Explicitly exclude** designs that conflict with this project's positioning (see "Do-not-borrow list"). Every item lands in the **description layer**, not a hard guard.

### Do-not-borrow list (do not introduce these later)

- **Conversational Surface two-layer architecture / voice persona**: this project is a Web-first local harness; a single layer suffices, no conversational front-end.
- **Long Personality blocks (default/friendly/pragmatic) and Frontend guidance**: irrelevant to a "general agent harness," they would significantly lengthen the prompt and push stylized output. Keep the current neutral tone.
- **`multi_tool_use.parallel` / `apply_patch` proprietary formats**: this project already has its own parallel-tool-call convention (`## Tool Use` already says independent calls should be issued together) and `edit_file` / `write_file`; no need to copy OpenAI-proprietary syntax.
- **Fixed collaboration-mode / multi-agent-proactive DAG-ification**: CLAUDE.md explicitly forbids baking a fixed workflow engine into the runtime; whether to use child agents must be model-led.

---

### - [x] T13 [prompt · fixed] Add "efficiency & noise" guidance for search/command tools

**Borrowed from**: Codex `# General` (system-prompt.md:131-138) — "prefer `rg`; parallelize file reads; **do not chain shell commands with separators like `echo "===";`, the output is noisy for the user**."

**Current gap**: This project's `## Tool Use` (`prompt.go:59-72`) already has "prefer dedicated tools / issue independent calls together," but **lacks two things**:
1. No guidance against "repeatedly redirecting discovery output into throwaway `reports/_*.txt` files / polluting output with `echo` separators." In the remote sessions, `ls`/`glob` output was written into dozens of scratch files like `reports/_ls_*.txt`, `_reports_*.txt` — exactly this noise. (These filenames were directly observed in the `glob` display output while collecting T1 data.)
2. No explicit "avoid `cat`/`grep`/`sed` in shell when a dedicated tool already covers it."

**Fix**: Add two description lines to `## Tool Use` (no hard-coded workflow):
- "Do not chain shell commands with separators like `echo \"====\";`, and do not repeatedly redirect discovery output into throwaway `reports/_*.txt` files; use `grep_files`/`glob`/`read_file` and keep scratch output out of deliverable directories."
- "Avoid `cat`/`grep`/`sed`/`echo` inside `shell` when `read_file`/`grep`/`edit_file` already cover the need."

**Acceptance criteria**: Sample whether the count of `reports/_*.txt` scratch files drops in later sessions; no unit test needed.

**Status**: fixed · commit: b365638 · verification: `go test ./internal/runtime -run 'TestBuildSystemPromptIncludesDirectToolGuidance' -count=1`; `go test ./internal/runtime -count=1 -timeout=180s`; `go build ./...` · Priority **P2**

---

### - [x] T14 [prompt · high value · fixed] Make "newest request wins + sanity check" behavior explicit after compaction/resume

**Borrowed from**: Codex `# Working with the user` (system-prompt.md:338-354) — "if a mid-turn user message conflicts, let the **newest** one steer this turn; after resume/interrupt/compaction, do a sanity check before the final answer to confirm you are answering the newest request, not an older ghost; after compaction, do not restart from scratch, continue naturally."

**Current gap**: This project has `recentSteerNote` (`prompt.go:228`) for steer handling, but **no** explicit guidance for "after compaction/resume, avoid restarting and avoid answering a stale request." This is highly relevant to this project's compaction mechanism (`compact.reused=174`, frequent on long tasks) — long tasks are exactly where "forgetting the newest goal / starting over" happens most.

**Fix**: Add a short block to the system prompt or the compaction recovery view (mirroring Codex's two sentences):
- "After a context compaction or resume, do not restart from scratch; continue from the summarized state and make reasonable assumptions about missing detail."
- "Before calling finish after a resume/interruption/compaction, verify your final result answers the newest external instruction, not a superseded earlier one."
Relates to T1: once compaction pollution is fixed, this guidance helps the model continue reliably on the compressed view.

**Acceptance criteria**: Construct a post-compaction session and check whether the model repeats already-completed steps; manual sampling.

**Status**: fixed · commit: f150daf · verification: `go test ./internal/runtime -run 'TestCompactorWritesDurableSummaryArtifact|TestCompactionAddsReferencePrefix|TestCompactorReusesSummaryWithinHysteresisWindow|TestFallbackCompactionDeferredViewDoesNotRetainFullHistory' -count=1`; `go test ./internal/runtime -count=1 -timeout=180s`; `go test ./internal/tools -count=1 -timeout=180s`; `go build ./...` · Priority **P2**

---

### - [x] T15 [prompt · medium · fixed] Structure the compaction handoff summary (borrowed from the checkpoint handoff prompt)

**Borrowed from**: Codex compaction handoff prompt (compaction-memory.md:13-31) — on compaction, generate a **structured handoff** aimed at "another LLM resuming seamlessly": important context/constraints/user preferences, and the critical data/examples/references needed to continue; on resume, prefix a fixed lead-in ("another model produced this summary; build on it and avoid duplicating work").

**Current gap**: This project's compaction (`internal/runtime/compaction.go`) leans on mechanical truncation + pseudo-JSON placeholders (the very T1 defect). It lacks a **continuation-oriented structured summary template** (goal, done, todo, key paths/files, user's latest constraints).

**Fix**: Add a structured summary section to compaction (can coordinate with T1's "do not disguise as a business JSON" approach): on compaction, retain/generate "current goal / validated conclusions / open items / key files and paths / latest steer constraints," and prefix the recovery view with a fixed lead-in sentence. **Note**: CLAUDE.md requires compaction to change only the context view, never overwrite the raw log — this project must add the summary as a **view-layer addition**, leaving raw `events.jsonl` / messages untouched.

**Acceptance criteria**: Unit test asserts the summary section contains the goal/todo/key-path fields; raw log is unmodified.

**Status**: fixed with T14 · commit: f150daf · verification: `go test ./internal/runtime -run 'TestCompactorWritesDurableSummaryArtifact|TestCompactionAddsReferencePrefix|TestCompactorReusesSummaryWithinHysteresisWindow|TestFallbackCompactionDeferredViewDoesNotRetainFullHistory' -count=1`; `go test ./internal/runtime -count=1 -timeout=180s`; `go test ./internal/tools -count=1 -timeout=180s`; `go build ./...` · Priority **P2**

---

### - [ ] T16 [prompt · medium] Explicit "a review request defaults to a code-review stance" guidance

**Borrowed from**: Codex `## Special user requests` (system-prompt.md:309-315 / personality.md:198-204) — "when the user says `review`, default to a code-review stance: **findings first**, ordered by severity with file/line references, summary after; if nothing is found, say so and note residual risk / test gaps."

**Current gap**: This project's `deliveryNote` (`prompt.go:172-179`) already requires "findings first + Severity/Confidence/Evidence/Snippet" for audit/review tasks — actually **finer-grained than Codex**. The gap: Codex's trigger is "any review intent in user input flips the stance," while this project only emits the note when it classifies the task as an audit-artifact task. A lighter general nudge could be added: even without producing an artifact, a review request should lead with findings, carry file:line, and avoid empty praise.

**Fix**: Add one general sentence to `## Tool Use` or a runtime note (low priority, since the audit coverage is already strong): "When the user asks for a review, lead with findings ordered by severity with file:line references; keep summaries brief and after the findings; if nothing is found, say so and note residual risk / test gaps."

**Acceptance criteria**: Sample the answer structure of non-artifact review requests.

**Status**: not done · commit: — · Priority **P3**

---

### - [x] T17 [prompt · medium · fixed with T5] "Explore before asserting, do not guess paths" senior-engineer stance

**Borrowed from**: Codex `# General` / `instructions_template` (system-prompt.md:127-129, 489-491) — "read the codebase first, resist easy assumptions, let the shape of the existing system teach you how to move; examine first without jumping to conclusions."

**Current gap**: This project already has "Inspect the owning code path before... / use scoped discovery" (`prompt.go:44,64`), same spirit, but it **does not directly hit T5's high-frequency failure** (guessing `.py` paths from memory and reading directly). Codex's line "build context by examining the codebase first without making assumptions" is a good wording template for the T5 description guidance.

**Fix**: **Implement together with T5** — the read_file not-found hint plus one system sentence: "Do not read a source path from memory; locate it with grep_files/glob first, then read the owning file." This is also the concrete landing of the Codex stance.

**Acceptance criteria**: Folded into T5's acceptance (read_file not-found rate).

**Status**: fixed with T5 · commit: a16315c · verification: `go test ./internal/runtime -run 'TestBuildSystemPromptIncludesDirectToolGuidance|TestNextHarnessReminderNudgesAfterRepeatedReadFileNotFound' -count=1`; `go test ./internal/tools -run 'TestReadFileNotFoundSuggestsDiscoveryFirst|TestCoreToolDescriptionsGuideSelection|TestToolDescriptionsMentionOnlyDeclaredParameters' -count=1`; `go test ./internal/runtime -count=1 -timeout=180s`; `go test ./internal/tools -count=1 -timeout=180s`; `go build ./...` · Priority **P2**

---

### - [ ] T18 [prompt · low] Final-answer "conciseness + file:line clickable link" formatting rules

**Borrowed from**: Codex `## Formatting rules` / `## Final answer instructions` (system-prompt.md:356-431) — conciseness first, lists only for genuinely list-shaped content, reference real files with clickable `[label](/abs/path:line)` links, do not exceed 50-70 lines, no "Sure!"-style openers.

**Current gap**: CLAUDE.md already requires "keep root docs short," but the **system prompt has no formatting/conciseness rules for the model's final answer**. Audit output mostly lands in artifacts (already governed by `deliveryNote`), but **conversational answers** have no length/structure constraint, and long tasks tempt the model into verbose recaps.

**Fix**: Add a trimmed-down formatting block to the system prompt (need not be as long as Codex's): conciseness first, lists only for list-shaped content, reference files with `path:line`, avoid meta openers. **Note this project requires Chinese responses** — wording must be compatible with the language constraint (the rules are about structure/length, which are language-agnostic).

**Acceptance criteria**: Sample final-answer length and whether clickable references appear.

**Status**: not done · commit: — · Priority **P3**

---

### - [ ] T19 [prompt · low] Interrupt-semantics hint: "a background process may still be running / the command may have partially executed"

**Borrowed from**: Codex interrupt/resume context (mode-prompts.md:336-343) — "the user interrupted the previous turn **on purpose**; any running processes may still be in the background; any aborted command **may have partially executed**."

**Current gap**: When interrupted after a tool call, this project writes a replayable interrupt result (`engine.go`, the correct behavior CLAUDE.md requires; = T4's `interrupted` class), but on resume it **does not tell the model that the interrupted command may have partially executed or that a background process may still be running** — the model may assume the command never ran and re-execute a side-effecting operation.

**Fix**: Append one sentence to the recovery view or the interrupt result's LLMOutput: "This tool call was interrupted; it may have partially executed and any spawned process may still be running. Verify state before re-running side-effecting commands." Coordinates with T4's `failure_class=interrupted`.

**Acceptance criteria**: Unit test asserts the interrupt-result text contains "partially executed / verify before re-running."

**Status**: not done · commit: — · Priority **P3**

---

## 3. Suggested Execution Order

1. **P0**: T1 (compaction pollution — the only must-fix code bug).
2. **P1**: T3 (glob path), T7 (spill naming/hint), T4 (`failure_class` observability).
3. **P2**: T2 + T9 (consistency test subsuming the glob regression), T5 + T17 (description guidance, merged), T12 (deploy discipline), T13 (tool-noise guidance), T14 + T15 (compaction continuation + handoff, coordinated with T1).
4. **P3**: T6, T8, T10, T11, T16, T18, T19.

> After P0+P1, commit and rebuild the remote deployment (T12), then run one real audit task as a regression and re-check the error rates with the updated analysis script (now carrying T4's `failure_class`).

## 4. Executive Summary

1. **The only must-fix code bug is T1** (compaction poisoning tool-call arguments). It is still present on the current HEAD, reproduces reliably on long sessions, is amplified by `compact.reused=174`, and violates CLAUDE.md's compaction boundary.
2. **The shell 22% error rate is a misread**: true harness responsibility is only 2/17 (= T1). T4 fixes this observability problem at the root so every future round is more accurate.
3. T3/T5/T7 are "tool API asymmetry / description guidance / harness path hint" class — low cost, high return; per CLAUDE.md, address via schema alignment + description + reminder, never a hard-coded workflow.
4. The provider / retry / interrupt-recovery / finish-guard / main-loop machinery (`935 tool_use`, all normal stops) is **working correctly overall**, with no fatal defects.
5. **Prompt borrowing (T13-T19)**: what is worth borrowing from Codex App is description-layer guidance — tool-noise constraints, compaction/resume continuation + structured handoff, interrupt semantics — of which T13/T14/T15 fit this project's long-session + compaction reality best. Persona, frontend, two-layer architecture, and proprietary tool syntax are **not borrowed** (see the Do-not-borrow list). This project's audit/review artifact guidance is already finer than Codex's and needs no reinforcement.
