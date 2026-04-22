# Long Audit

## Scope and method

This audit compares the required specs against `README.md`, the current Go implementation, and focused tests. The review emphasizes:

- tool surface vs registry alignment
- whether `experimental web` still presents as experimental
- whether multi-agent is enabled by default but delegation remains master-directed
- whether recent frontend behaviors (`history`, `refresh`, `clear`) have backend contracts

Reviewed sources include `AGENTS.md`, `spec/00-product.md`, `spec/01-runtime-architecture.md`, `spec/03-provider-contracts.md`, `spec/09-phase-plan.md`, `spec/11-spec-audit-and-traceability.md`, `spec/12-task-system.md`, `spec/13-live-input-and-steering.md`, `README.md`, `internal/tools/registry.go`, `internal/config/config.go`, `internal/runtime/delegation.go`, `internal/webconsole/service.go`, and targeted tests.

## Summary

Overall alignment is good. The repo currently matches the major spec claims in the audited areas. I found one small documentation drift and corrected it directly in `README.md`.

## Findings

### 1. Tool surface and registry alignment

Status: largely aligned.

Evidence:

- The built-in tool registry in `internal/tools/registry.go` includes the expected durable and multi-agent-oriented tools, including `todo_write`, `todo_read`, `task_create`, `task_update`, `task_list`, `task_get`, `feature_list_create`, `feature_list_update`, `feature_list_read`, `agent_spawn`, `agent_status`, and `agent_list`.
- Registry gating respects multi-agent enablement; agent tools are exposed only when multi-agent is enabled, which matches the runtime model described in spec.
- Tests in `internal/tools/registry_test.go` cover reserved names and the conditional presence of multi-agent tools.
- README references the experimental and core surfaces separately, which is consistent with the CLI behavior tested in `internal/app/app_test.go`.

Assessment:

- No implementation drift found in the tool registry itself.
- The task system and durable task graph concepts from `spec/12-task-system.md` are reflected in exposed tooling.

### 2. `experimental web` remains experimental

Status: aligned.

Evidence:

- CLI routing exposes web console under `experimental web`, not as a top-level stable command.
- `internal/app/app_test.go` verifies default usage does not advertise experimental commands, while explicit `experimental` invocation shows `delegate|children|queue|tui|web`.
- README documents web console under the experimental surface rather than the stable core command list.
- `internal/webconsole/service.go` implements the feature as a separate experimental service rather than merging it into the stable CLI path.

Assessment:

- Experimental positioning is preserved across docs, code, and tests.

### 3. Multi-agent default enabled, but delegation remains master-directed

Status: aligned.

Evidence:

- `internal/config/config.go` defaults `MultiAgent.Enabled` to true, consistent with current spec direction.
- Registry exposure for `agent_spawn` / `agent_status` / `agent_list` depends on that config, confirming the default-on tool surface.
- Delegation logic remains explicit and orchestrator-controlled rather than automatic fan-out. The runtime model in `internal/runtime/delegation.go` reflects a parent/master choosing whether to delegate.
- README language describes child agents as an available capability, not something that always triggers automatically.
- Tests cover presence of experimental child-management surfaces and queue handling without asserting automatic delegation.

Assessment:

- The important distinction holds: multi-agent capability is on by default, but task delegation remains a master decision.
- I did not find contradicting tests or docs that claim unconditional automatic delegation.

### 4. Frontend behaviors vs backend contracts: `history`, `refresh`, `clear`

Status: mostly aligned, with contract coverage present.

Evidence:

- `internal/webconsole/service.go` exposes backend routes and handlers for session history/timeline retrieval, continue/steer flows, and clear/reset-related UI support paths.
- Service tests in `internal/webconsole/service_test.go` cover timeline/history-oriented responses, queue state, worker views, and continuation behavior.
- `refresh` is implemented as client re-fetch against existing read endpoints rather than a dedicated mutation contract, which is acceptable if docs describe it as a UI action rather than a standalone backend primitive.
- `clear` behavior is backed by server-side routes/handlers rather than being purely cosmetic frontend state.

Assessment:

- Recent frontend behaviors do map to backend contracts.
- No missing server contract was found for the audited actions.
- The main caution is terminology: some UI behavior names are UX labels over existing API reads/writes rather than standalone named backend concepts. That is acceptable, but docs should continue describing them at the right abstraction level.

## Confirmed drift fixed

### README experimental surface list

I found a small README drift in the large-project profile bullet: it listed `experimental delegate|children|queue|web` and omitted `tui`, while the actual explicit experimental usage and tests include `tui`.

Fix applied:

- Updated `README.md` to list `experimental delegate|children|queue|tui|web` in the affected bullet.

Why this is safe:

- It aligns the README with the implemented CLI surface and existing tests.
- It does not change runtime behavior.

## Residual notes

- The `reports/` directory currently appears to have several deleted tracked files in the working tree unrelated to this audit. I did not restore or modify them because that would exceed the requested scope.
- I did not find a second deterministic drift that was both small and clearly safe to patch without broadening the change set.

## Conclusion

The audited areas are in good shape. The implementation and tests match the current spec direction on tool registry shape, experimental web positioning, default-on multi-agent capability with master-directed delegation, and web UI behavior contracts. One minor README drift was corrected.
