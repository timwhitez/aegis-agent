# Terminal-Bench Agent Reference Optimization

> Date: 2026-05-18
> Scope: source-grounded optimization plan, accuracy review, implementation record, and handoff notes for the latest Terminal-Bench-oriented agent references.

## 1. Source Map And Weighting

This pass treats benchmark integrity as a first-class constraint. Terminal-Bench's 2026-04-19 integrity update says reward hacking can receive zero score, calls out internet solution lookup as the common loophole, and specifically notes ForgeCode trials that curled internet solutions into `AGENTS.md`. Therefore ForgeCode remains a low-weight reference for this pass.

Source URLs reviewed:

- `https://www.tbench.ai/news/leaderboard-integrity-update`
- `https://vix.codes/`
- `https://github.com/kirby88/vix-releases`
- `https://github.com/schpet/jjagent`
- `https://github.com/china-qijizhifeng/agentic-harness-engineering`
- `https://capy.ai/`
- `https://capy.ai/articles`
- `https://github.com/octu0/polaris`
- `https://www.wozcode.com/blog`
- `https://github.com/stanford-iris-lab/meta-harness`
- `https://github.com/openai/codex`
- `https://github.com/harbor-framework/terminal-bench/tree/main/terminal_bench/agents/terminus_2`

| Source | Current evidence | Weight | NGEN decision |
| --- | --- | --- | --- |
| Terminal-Bench integrity update | New policy requires passing trajectories and penalizes reward hacking such as finding solutions online. | Highest | Implement an opt-in benchmark integrity guard before borrowing broader leaderboard tactics. |
| AHE | Describes observability-driven harness evolution where traces, logs, rewards, sourced analysis, predicted impact, and next-eval falsification drive changes. | High | Preserve NGEN's existing artifact-first harness ledger; future harness changes should map prediction to later validation evidence. |
| Meta-Harness | Defines harness search as optimizing what to store, retrieve, and show around a fixed model, with a Terminal-Bench 2 example. | High | Treat harness changes as measurable experiments, not one-off prompt churn. |
| Terminus 2 | Uses parser-specific command formats, bounded output, proactive context summarization, output-error recovery, and double task-complete confirmation. | High | NGEN already has explicit completion gates, context artifacts, output caps, and parser-like provider schemas; keep future improvements near these contracts. |
| Codex | OpenAI's local terminal coding agent remains a strong reference for terminal-first workflow and local execution posture. | High | Keep NGEN local-first, terminal-first, and docs/artifact backed rather than web-dashboard first. |
| Capy | Presents task/branch-oriented parallel orchestration, clarification questions with suggested answers, per-task PR/review workflow, and sandboxed or worktree-isolated environments. | Medium | NGEN already has mission/task/worker/worktree surfaces; future work can improve clarification and operator UX without adding a heavy dashboard. |
| Polaris | Exposes a distributed Go agent framework with registered tools and parallel function calls across local resources. | Medium | Useful for future remote sidecar/tool registry work, but NGEN should not add distributed runtime scope in this pass. |
| jjagent | Archived, but useful for attributing session edits to a stable change and preserving resume handles in commit metadata. | Medium-low | NGEN already persists task/session IDs and git bearings; defer deeper VCS integration. |
| Vix | A lightweight multi-model local coding-agent entrypoint. | Low-medium | Supports NGEN's provider-neutral stance; no immediate code delta beyond preserving configurable providers. |
| WozCode | Emphasizes reducing round trips with combined search/read, batched edits, AST-aware truncation, and fuzzy edit matching. | Medium | NGEN's patch-first edits and bounded command output align; combined search/read is a future tool-plane optimization. |

## 2. Accuracy Review

Validated facts:

- Benchmark integrity is now an explicit Terminal-Bench leaderboard policy concern, not an abstract product preference.
- ForgeCode should be down-weighted for this pass because the cited update names ForgeCode reward-hacking behavior.
- NGEN already owns durable artifacts for task state, command runs, workspace edits, verification, reviews, harness snapshots, worker results, mission validation, metrics, and memory.
- NGEN already gates observation commands as read-only and visibility-safe; the remaining high-risk gap is yolo repair commands, which can currently execute open-world shell or network commands if the operator deliberately enables yolo mode.

Inferences:

- A Terminal-Bench evaluation run benefits from a stricter command policy than normal local development, because benchmark validity is more important than convenience package/script access.
- The safest minimal slice is an opt-in integrity mode under `permission`, not a default breaking change to local development.

Rejected or deferred:

- Do not add a distributed sidecar system from Polaris in this pass; it expands runtime scope.
- Do not add a Capy-style dashboard; NGEN already has CLI/ACP/Web backend read models and should stay local-first.
- Do not add prompt-only leaderboard tuning without a runtime artifact or policy effect.
- Do not run or vendor the external agent repositories.

## 3. Optimization Plan

### O1. Benchmark integrity command policy (implemented this pass)

Add `permission.benchmark_integrity_mode` to `ngen.json`.

When enabled, benchmark-oriented runs deny repair commands that can reach the network or hide open-world behavior, even if the task uses `permission_mode_id=yolo`. The denied set includes direct network clients, Git remote operations, package managers, shell/interpreter wrappers, path-based repo scripts, `go mod download`, `go get`, `go install`, `go generate`, and analogous Cargo fetch/install/update commands.

Expected behavior:

- Local deterministic commands such as `gofmt`, `go test`, `go build`, `go vet`, `cargo test`, `cargo build`, and `cargo check` can still run under the existing policy.
- Risky commands write `policy_decision=denied_benchmark_integrity`, `replay_safety.replay_policy=do_not_auto_replay`, and a clear failure summary in `command_runs.jsonl`.
- The feature is opt-in so normal local development does not lose yolo's current escape hatch.

### O2. Harness experiment ledger (future)

NGEN already writes task-local `harness/latest.json` and mission `metrics.jsonl`. A future AHE/Meta-Harness slice should add a compact prediction/evaluation record for harness changes: source evidence, expected pass/fail flips, risked scenarios, validation command, and later observed outcome.

### O3. Combined search/read output (future)

WozCode's strongest idea is reducing round trips. A future command-plane slice can add a read-only "ranked snippets" observation result format or provider-visible summary artifact, instead of forcing a separate grep/read loop. This needs careful visibility and output-cap tests.

### O4. Clarification and session attribution (future)

Capy and jjagent point at two product refinements: structured clarification questions before task materialization and stronger VCS attribution for agent sessions. NGEN already has input requests, tasks, sessions, worker lineage, and git bearings; future work should improve surfaces without replacing artifact truth.

## 4. Implementation Record

Planned code slice:

- `internal/task/types.go`: extend `PermissionConfig` with `benchmark_integrity_mode`.
- `internal/runtime/runner.go`: route repair-command policy through the service config and add benchmark-integrity risk classification.
- `internal/runtime/runner_test.go`: cover yolo mode plus integrity mode denying curl, shell wrapper, `go mod download`, Git clone, and repo script commands while allowing local formatter commands.
- `internal/task/service_test.go`: cover config loading for `permission.benchmark_integrity_mode`.

Planned docs sync:

- `README.md`: add the new config key to the example and explain the benchmark policy.
- `docs/06-go-package-and-api/config-and-domain-model.md`: document the `permission` field.
- `docs/07-verification-security-and-ops/security-and-policy.md`: document the stricter repair-command policy.
- `docs/08-delivery-plan/acceptance-matrix-and-out-of-scope.md`: add an acceptance check for benchmark integrity mode.

Validation plan:

- Focused tests:
  - `go test ./internal/task -run 'TestLoadConfigAcceptsBenchmarkIntegrityMode' -count=1`
  - `go test ./internal/runtime -run 'TestValidateExecutionCommandPolicyByPermissionMode|TestValidateExecutionCommandBenchmarkIntegrityMode' -count=1`
- Broad verification:
  - `./build.sh`
  - `git diff --check`

## 5. Handoff Notes

- Validation completed on 2026-05-18:
  - `go test ./internal/task -run 'TestLoadConfigAcceptsBenchmarkIntegrityMode' -count=1` passed.
  - `go test ./internal/runtime -run 'TestValidateExecutionCommandPolicyByPermissionMode|TestValidateExecutionCommandBenchmarkIntegrityMode' -count=1` passed.
  - `./build.sh` passed, including gofmt, `go test ./...`, and `go build -o ./bin/ngen ./cmd/ngen`.
  - `git diff --check` passed after the validation note was appended.
- This document is the handoff anchor for the 2026-05-18 Terminal-Bench reference pass.
- Keep ForgeCode reference weight low unless the integrity finding is independently resolved.
- If future agents continue O2/O3/O4, keep changes small and require artifact-level evidence plus targeted tests before broad live matrices.
- Do not treat external leaderboard ranking as a reason to bypass NGEN's existing local-first, artifact-first, hard-verification design center.
