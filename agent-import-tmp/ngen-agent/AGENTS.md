# AGENTS.md - NGEN Agent

This file stays intentionally short. Keep stable, always-needed rules here. Put detailed design truth in `docs/` and read those files on demand.

## WHY

- NGEN is a spec-first repo for a next generation coding agent and harness runtime.
- The anchor product is a coding agent; `security_review`, `reviewer`, and `general_execution` reuse the same kernel.
- In this repo, docs are the system of record until implementation lands; once code exists, owner docs and runtime truth must stay aligned in the same pass.

## Default Working Rules

- Start from repo truth. Read the owner doc before changing design or code.
- Preserve the design center: explicit loop, artifact-first memory, hard verification, observable control.
- Prefer the smallest implementation that can survive long-running tasks.
- Default to Go 1.24+ single-binary design and Go standard library; add narrow utility packages only when they remove real complexity.
- Keep machine state in JSON or JSONL and human state in Markdown; do not introduce YAML unless a strong reason is documented.
- Avoid hidden state. If a runtime decision matters, it should become an artifact, event, checkpoint, approval, or review record.
- Keep one write owner per workspace or worktree. Multi-agent collaboration should converge through artifact contracts, not chatty peer-to-peer state.
- No silent degradation. Missing files, tool failures, parse failures, verification failures, and policy denials must surface as explicit diagnostics.
- If you change an artifact name, CLI contract, profile contract, or runtime invariant, update every owning doc in the same pass.
- This repo is git-backed. Every code, doc, spec, test, or `AGENTS.md` update that you intend to keep must be validated for the relevant scope and committed to git before ending the task; do not leave completed fixes uncommitted.
- Keep this file high signal. Do not turn `AGENTS.md` into a changelog or scratchpad.

## Repo Snapshot

- Current repo scope: integrated Go runtime under `cmd/` and `internal/`, plus the owner-doc bundle under `docs/`.
- Runtime target: Go 1.24+ single binary named `ngen`.
- State root target: `.ngen/` inside the target workspace, using JSON, JSONL, and Markdown artifacts.
- Main agent profiles in scope: `coding`, `security_review`, `general_execution`, `reviewer`.
- Product stance: coding-first, profile-extensible, low-dependency, local-first.

## Retrieval Map

Read only what matches the task:

- Overview, positioning, and reading order: `README.md`
- Research synthesis and design philosophy: `docs/01-foundations.md`
- Product scope, users, and requirements: start with `docs/02-prd.md`, then read the matching file under `docs/02-prd/`
- Runtime component boundaries: start with `docs/03-architecture.md`, then read the matching file under `docs/03-architecture/`
- Loop state machine, guards, and resume semantics: start with `docs/04-runtime.md`, then read the matching file under `docs/04-runtime/`
- Artifact schemas, file layout, and context logic: start with `docs/05-artifacts-and-context.md`, then read the matching file under `docs/05-artifacts-and-context/`
- Go package layout, CLI surface, and interfaces: start with `docs/06-go-package-and-api.md`, then read the matching file under `docs/06-go-package-and-api/`
- Verification, review, security, and operations: start with `docs/07-verification-security-and-ops.md`, then read the matching file under `docs/07-verification-security-and-ops/`
- Delivery sequencing and phase gates: start with `docs/08-delivery-plan.md`, then read the matching file under `docs/08-delivery-plan/`

## Change Workflow

1. Find the owning document or package.
2. Read only the matching owner entry page and relevant split spec file(s), plus `README.md` for broad repo-level changes.
3. Make the smallest change that keeps cross-doc contracts aligned.
4. If a concept appears in multiple docs, sync all affected docs in the same pass.
5. When implementation exists, validate the narrowest useful layer first, then widen.
6. If the work changes tracked repo content, finish by creating a git commit that matches the validated state.

## Guardrails For Future Code

- `Done` must always be explicit and recorded.
- The verifier chain is mandatory for any task that changes files, code, or security posture.
- `coding` is the anchor profile; other profiles extend the same runtime instead of bypassing it.
- `aside` and `watch` are control primitives, not hacks bolted onto chat history.
- The default multi-agent mode is manager plus bounded workers with artifact contracts; avoid uncontrolled parallel writes.
