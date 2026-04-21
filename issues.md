# Issues Closure And Optimization Backlog

Last updated: 2026-04-21

Current baseline:

- Branch: `main`
- Reference commit before this documentation convergence: `a22ba5e Harden yolo guards and live validation harness`
- Scope: close the stale `issues.md` tracker after the P0/P1/P2 optimization waves, P2 follow-up, live acceptance stack, task-heavy matrix, and worktree cleanup.

## Current Status

There are no known active repair blockers in the current `main` checkout.

The previous `issues.md` file mixed four different states:

1. Items that were already implemented but still unchecked.
2. Duplicate entries for the same completed work.
3. Optional future research/backlog ideas.
4. Historical issue notes from earlier validation rounds.

This file now separates those states explicitly:

- **Closed**: implemented in code or documented by retained validation evidence.
- **Backlog**: optional future enhancement, not a blocker for the current core v1 closure.
- **Historical / external**: retained for traceability, not an active repo issue.

## Closure Evidence

### Latest Retained Validation Evidence

| Area | Status | Evidence |
| --- | --- | --- |
| Acceptance stack | Passed | `validation/runs/2026-04-21-openai-compatible-gpt-5.4-live-regression-after-rt24-fix-full-rerun/SUMMARY.md` |
| Full acceptance matrix | Passed: 26/26 scenarios, 0 failed | `validation/runs/2026-04-21-openai-compatible-gpt-5.4-live-regression-after-rt24-fix-full-rerun-matrix/SUMMARY.md` |
| Full acceptance matrix issues | No open issues | `validation/runs/2026-04-21-openai-compatible-gpt-5.4-live-regression-after-rt24-fix-full-rerun-matrix/ISSUES.md` |
| Task-heavy matrix | Passed: 20/20 scenarios, 0 failed | `validation/runs/2026-04-21-openai-compatible-gpt-5.4-task-heavy-after-tt02-fix-rerun/SUMMARY.md` |
| Task-heavy matrix issues | No open issues | `validation/runs/2026-04-21-openai-compatible-gpt-5.4-task-heavy-after-tt02-fix-rerun/ISSUES.md` |
| Task-heavy focused web follow-up | Passed | `validation/runs/2026-04-21-openai-compatible-gpt-5.4-task-heavy-after-tt02-fix-rerun-focused-webconsole-followup/SUMMARY.md` |

### Current Evidence Interpretation

- The live evidence is retained evidence from the latest full OpenAI-compatible Responses runs.
- The model/provider path validated there is `openai-compatible` with `wire_api: responses` and model `gpt-5.4`.
- The validation summaries intentionally preserve scenario-level run directories instead of relying on chat-only claims.
- The root tracker treats the live matrices as stronger evidence than old unchecked roadmap entries.

## Closed Active Optimization Items

### Context Engineering

| Item | Status | Evidence |
| --- | --- | --- |
| Ephemeral messages | Closed | `internal/tools/registry.go`, `internal/runtime/engine.go`, `.artifacts/` handling, retained live matrices |
| Progressive disclosure for skills | Closed | `internal/skills/catalog.go`, `load_skill` tool in `internal/tools/registry.go`, prompt skill-summary behavior in `internal/runtime/prompt.go` |
| Smarter compaction | Closed | `internal/runtime/compaction.go`, `internal/runtime/compaction_test.go`, retained live matrices |
| Project memory / durable handoff notes | Closed | `internal/runtime/prompt.go`, `internal/runtime/engine.go`, retained live matrices |

### Prompt And Tool Surface

| Item | Status | Evidence |
| --- | --- | --- |
| Core system prompt simplification | Closed | `internal/runtime/prompt.go`, `internal/runtime/prompt_test.go`, `REFACTOR_REPORT.md` |
| Runtime Notes injection optimization | Closed | `internal/runtime/prompt.go`, `internal/runtime/prompt_test.go`, `P2_OPTIMIZATION_REPORT.md` |
| Tool description simplification | Closed | concise built-in tool descriptions in `internal/tools/registry.go`; completion report in `P2_OPTIMIZATION_REPORT.md` |
| Exact artifact / literal guard behavior | Closed | `internal/runtime/prompt.go`, `internal/runtime/engine.go`, retained acceptance matrix |
| Yolo guard hardening | Closed | `internal/runtime/engine.go`, `internal/runtime/prompt.go`, retained acceptance matrix |

### Long-Horizon Execution

| Item | Status | Evidence |
| --- | --- | --- |
| Ralph Loop / incomplete-no-finish continuation | Closed | `internal/runtime/runner.go`, `internal/runtime/engine.go`, retained matrices |
| Feature List management | Closed | `internal/tools/feature_list.go`, `internal/session/types.go`, `internal/runtime/compaction.go` |
| Initializer Agent mode | Closed | `--init` support in `internal/app/app.go`, `session.ModeInit` in `internal/session/types.go`, initializer prompt in `internal/runtime/prompt.go`, tests in `internal/app/app_test.go` and `internal/runtime/prompt_test.go` |
| Steer / continue recovery behavior | Closed | `spec/13-live-input-and-steering.md`, `internal/runtime/runner.go`, retained acceptance matrix |

### Frontend / Experimental Web UX

The Web console remains an explicit experimental surface, not a core CLI requirement.

| Item | Status | Evidence |
| --- | --- | --- |
| Virtual scroll / incremental rendering | Closed | `internal/webconsole/assets/app.js` |
| Keyboard shortcuts | Closed | `internal/webconsole/assets/app.js`, `internal/webconsole/assets/styles.css` |
| Dark mode | Closed | `internal/webconsole/assets/index.html`, `internal/webconsole/assets/app.js`, `internal/webconsole/assets/styles.css` |
| Web console focused regression | Closed | focused follow-up summaries under `validation/runs/2026-04-21-*focused-webconsole-followup/` |

### Runtime / Validation Hardening

| Item | Status | Evidence |
| --- | --- | --- |
| OpenAI-compatible retry hardening | Closed | `validation/config.openai-compatible.yaml`, `validation/config.openai-compatible-low-compact.yaml`, focused follow-up retry proof |
| Acceptance harness false-positive fixes | Closed | `validation/run_round31_complex_real_matrix.sh`, acceptance stack summary |
| Task-heavy harness false-positive fixes | Closed | `validation/run_round61_task_heavy_real_matrix.sh`, task-heavy matrix summary |
| Web smoke script compatibility | Closed | `validation/scripts/webconsole_ui_smoke.mjs`, focused follow-up summary |
| `.artifacts/` internal output hiding | Closed | `internal/tools/registry.go`, `.gitignore`, retained live matrices |
| Shell timeout cap behavior | Closed | `internal/tools/registry.go`, unit/runtime tests retained by latest validation |

## Backlog: Optional Future Enhancements

The following items are not active blockers. They remain useful future work if the product direction explicitly expands beyond the current core v1 closure.

### Architecture Constraints

| Area | Status | Notes |
| --- | --- | --- |
| Deterministic custom linters | Backlog | Optional structural enforcement; current closure relies on Go tests, harness scripts, and live matrices. |
| Structural layer tests | Backlog | Useful for guarding core/runtime/sdk/cli boundaries; not a failing current issue. |
| Pre-commit hook bundle | Backlog | Can be added later without changing runtime semantics. |

### Cross-Provider Context Handoff

| Area | Status | Notes |
| --- | --- | --- |
| Provider-neutral context export/import | Backlog | Internal message model and provider replay paths exist; full cross-provider migration artifact is not a current core blocker. |
| Tool call compatibility bridge | Backlog | Provider adapters already convert current tool calls; a standalone migration layer can remain future work. |
| Session JSON portability contract | Backlog | Useful for migration/export workflows, outside current validated acceptance scope. |

### Self-Verification Loops

| Area | Status | Notes |
| --- | --- | --- |
| Generic pre-completion checklist middleware | Backlog | Feature list and finish guards cover current validated needs; generic middleware can be added later. |
| Automatic test-command discovery | Backlog | Agents can already run shell tests; deterministic discovery/execution policy is future work. |
| Browser automation as a built-in default | Backlog | Current browser proof is validation-harness owned; product runtime should not make browser automation a default core dependency. |

### Entropy Management

| Area | Status | Notes |
| --- | --- | --- |
| Documentation consistency agent | Backlog | Could be implemented as a skill or scheduled queue job. |
| Pattern enforcement agent | Backlog | Useful for larger codebases; not required for current closure. |
| Cleanup / audit skills | Backlog | Optional reusable skills rather than default runtime behavior. |

### Observability

| Area | Status | Notes |
| --- | --- | --- |
| Tool usage analytics | Backlog | Events are already durable; aggregate dashboards can be added later. |
| Cost tracking | Backlog | Usage fields exist where providers return them; cost normalization is future work. |
| Performance profiling | Backlog | Turn/tool timing analytics can be built from events later. |

### Tool System Enhancements

| Area | Status | Notes |
| --- | --- | --- |
| More structured UI/LLM tool output | Backlog | `ToolResult` already separates `LLMOutput` and `DisplayOutput`; broader schema normalization is future work. |
| Read/glob cache | Backlog | Optional performance optimization; must avoid stale file facts if implemented. |
| Tool chaining suggestions | Backlog | Optional model-guidance feature; should not become hard-coded workflow orchestration. |

### Advanced Agent Architecture

| Area | Status | Notes |
| --- | --- | --- |
| Specialized agent roles | Backlog | Role hints exist for child sessions; richer testing/review personas remain optional. |
| Agent-to-agent review loops | Backlog | Should remain explicit/experimental, not a default fixed DAG. |

### Advanced Context Management

| Area | Status | Notes |
| --- | --- | --- |
| Context priority management | Backlog | Current compaction preserves key evidence; more granular priority scoring can be future work. |
| Semantic context indexing | Backlog | Embedding/RAG-style context retrieval is experimental and outside core v1. |

### Documentation And Onboarding

| Area | Status | Notes |
| --- | --- | --- |
| Full user guide | Backlog | README, specs, and reports cover current operator/developer evidence; a polished guide can follow. |
| Error message copy pass | Backlog | Current validation does not show blocking error-copy defects. |
| Interactive tutorial | Backlog | Would belong to `experimental web`, not default CLI core. |

## Historical / External Notes

These are retained for traceability but are not active repo repair items.

| Item | Status | Notes |
| --- | --- | --- |
| Websocket `reset_session` fake durable ID | Historical resolved | Fixed in earlier Web console repair work. |
| Workspace view current server `cwd` labeling | Historical resolved | Fixed in earlier Web console repair work. |
| CJK-safe font fallbacks | Historical resolved | Fixed in earlier Web console repair work. |
| Default tool surface documentation | Historical resolved | Fixed in earlier documentation pass. |
| `clearHistory()` History view behavior | Historical resolved | Fixed in earlier Web console repair work. |
| Compaction dropping latest external instruction | Historical resolved | Fixed before latest retained live matrices. |
| Upstream `auth_unavailable` | External / not repo-owned | Provider-side condition, not treated as an active local code issue. |
| Workspace-root switching | Not implemented by design | Current product boundary is current `cwd`; this is not a defect. |

## Current Closure Rule

Do not reopen an item in this file only because it is an interesting enhancement. Reopen only when there is concrete evidence of one of the following:

1. A current test, build, or live validation failure.
2. A behavior-level regression in core `init/run/exec/steer/continue/sessions/tasks/probe-provider/doctor`.
3. A documented spec/README/implementation contradiction that affects current operator behavior.
4. A retained validation artifact showing an open issue.

## Reference Documents

Local references:

- `spec/00-product.md`
- `spec/01-runtime-architecture.md`
- `spec/03-provider-contracts.md`
- `spec/09-phase-plan.md`
- `spec/11-spec-audit-and-traceability.md`
- `spec/12-task-system.md`
- `spec/13-live-input-and-steering.md`
- `REFACTOR_REPORT.md`
- `P2_OPTIMIZATION_REPORT.md`

External references originally used for the optimization plan:

- `../bitter-lesson-agent-frameworks.md`
- `../pi-coding-agent.md`
- `../blog-langchain-com__the-anatomy-of-an-agent-harness.md`
- `../openai-com__harness-engineering.md`
- `../anthropic-com__effective-harnesses-for-long-running-agents.md`
