# Durable Contract And Completion

## 1. Scope

This spec defines the core hardening layer added after core v1 convergence:

- session-scoped contract snapshots
- required artifact tracking
- centralized completion decisions
- provider attempt ledger
- operator-readable session summaries
- large-project checkpoints
- parent coordination for explicit child or queue work

These mechanisms do not create a workflow engine. The model remains the agent; the harness records task constraints, validates explicit completion boundaries, and preserves recovery facts.

## 2. Session Contract

Each session may write:

```text
.go-cli-agent/sessions/<id>/contract.json
```

The contract is a snapshot of externally visible constraints:

- `source`: `user_instruction`, `cli_flag`, `config_profile`, `skill`, `workspace_extension`, or `parent_handoff`
- `trust_source`: `builtin`, `explicit_user`, `trusted_workspace`, or `untrusted_workspace`
- `profile`: `default`, `review`, `audit`, `large_project`, or `delegated`
- `required_artifacts`
- `completion_gates`
- `exact_target_anchors`
- `exact_template_requirements`
- `literal_anchors`
- timestamps

The first implementation derives this snapshot from the latest external user instruction and existing guard parsers. It intentionally does not add a new default root command.

Contract updates produce `contract.created` or `contract.updated` events and append a best-effort history sidecar under `artifacts/contract-history.jsonl`.

## 3. Artifact Tracker

Explicit required artifacts are tracked in:

```text
.go-cli-agent/sessions/<id>/artifact-tracker.json
```

For each required path, the runtime records:

- baseline existence, size, mtime, and hash
- current presence
- whether the file was touched by this session
- whether it changed from baseline
- latest writer tool and turn
- optional content validator

The generic required-artifact gate is only active when the contract has explicit required artifacts. Ordinary non-artifact tasks must not be blocked by this gate. Review or audit artifacts still use the existing review validator for content quality.

## 4. Completion Controller

`CompletionController` is the unified finish/tool gate entrypoint. It wraps existing guard behavior first, then applies explicit contract gates:

- existing review/artifact/template/literal/target/taskboard/steer guards
- pre-completion feature checks
- required artifact baseline/touched/changed gate
- parent coordination unresolved work gate

The first migration is behavior-equivalent for existing guard kinds and messages. New generic artifact checks are limited to explicit required artifacts.

Events:

- `completion.evaluate.started`
- `completion.gate.passed`
- `completion.gate.blocked`
- `completion.evaluate.finished`
- `artifact.tracked`
- `artifact.gate.passed`
- `artifact.gate.blocked`

`yolo` mode still disables retrieval, project-memory, and review-artifact guardrails, but it does not bypass explicit user contracts, workspace path safety, shell timeout, or required artifact gates.

## 5. Provider Attempt Ledger

Provider attempts are recorded in:

```text
.go-cli-agent/sessions/<id>/provider-attempts.jsonl
```

The ledger records retry, auto-resume, final failure, and success facts:

- turn and attempt
- provider and model
- timeout policy
- outcome
- retryability
- status code, error class, timeout kind
- response id when available
- created time

The ledger is diagnostic only. Provider retry policy remains adapter-owned and is not driven by this file.

## 6. Session Summary

The runtime writes a derived operator view:

```text
.go-cli-agent/sessions/<id>/session.md
```

It summarizes:

- session status, phase, provider/model, workdir, isolation
- parent/root/queue relation
- contract and gates
- required artifacts
- todo and task state
- recent provider attempts
- child sessions, queue jobs, and background notifications
- latest long-run checkpoint path
- canonical fact-file locations

`session.md` is never a fact source. If summary writing fails, the runtime task should continue.

## 7. Long-Run Checkpoint

Large-project, delegated, child/queue, isolation, explicit-contract, multi-artifact, task-heavy, or compacted sessions may write:

```text
.go-cli-agent/sessions/<id>/checkpoints/longrun-latest.json
```

The checkpoint is a resume index, not a replacement for messages/events/state. It records contract snapshot, todo/task summary, artifact status, latest compaction artifact, provider/model/options, workdir/isolation, unresolved child/queue state, and resume hints.

Normal `continue` still works without a checkpoint. When a checkpoint exists, `continue` may inject a harness resume note and emits visible checkpoint events rather than silently changing behavior.

## 8. Parent Coordination

Parent sessions with explicit child or queue work may write:

```text
.go-cli-agent/sessions/<parent>/parent-coordination.json
```

The coordination file records:

- `wait-all` or `wait-any`
- unresolved child sessions
- unresolved queue jobs
- completed and failed children/jobs
- parked/resumed state

The completion controller blocks parent `finish` while unresolved child/queue work remains under `wait-all`. Under `wait-any`, completion can proceed after one child/job result is complete, while remaining work stays visible in the coordination file and `session.md`.

## 9. Workspace Extension Trust

Workspace-local `.agent` assets are discovery-only unless explicitly trusted. A workspace extension cannot affect the tool list or prompt while untrusted.

Required controls:

- explicit trust
- symlink escape validation
- reserved tool-name denial
- qualified names for workspace extensions
- disabled state while untrusted
- read-only trust observability in `doctor` and local WebConsole: discovery path, candidate path, trust mode, disabled state and disabled reason
- loaded source path recorded in contract/session summary when future loading is enabled

## 10. Shell Sandbox

The default shell remains portable `CommandContext` with timeout, output truncation, workspace path checks, and environment allowlist.

An explicit Linux profile may set:

```yaml
runtime:
  shell:
    sandbox: bwrap
```

When `bwrap` is available, the shell tool runs under a best-effort Bubblewrap wrapper bound to the workspace. When an operator explicitly requests `sandbox: bwrap` on non-Linux systems or on a host without Bubblewrap, the shell tool must fail closed instead of silently running unsandboxed. The error metadata should make the unavailable sandbox reason visible for `doctor`, session events, and operator remediation.
