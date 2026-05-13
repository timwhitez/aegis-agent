# Durable Contract And Completion

## 1. Scope

This spec defines the core hardening layer added after core v1 convergence:

- session-scoped contract snapshots
- session-scoped goal snapshots
- session-scoped Plan Mode snapshots
- required artifact tracking
- centralized completion decisions
- provider attempt ledger
- operator-readable session summaries
- large-project checkpoints
- parent coordination for explicit child or queue work

These mechanisms do not create a workflow engine. The model remains the agent; the harness records task constraints, validates explicit completion boundaries, and preserves recovery facts.

## 1.1 Session Goal

Each session may write:

```text
.go-cli-agent/sessions/<id>/goal.json
.go-cli-agent/sessions/<id>/artifacts/goal-history.jsonl
```

The goal snapshot records:

- `objective`
- `mode`: persisted for compatibility; default user-facing creation uses unified `goal`
- `status`: `active`, `paused`, `budget_limited`, or `complete`
- token / time budgets and usage
- success criteria
- validation plan
- user control settings
- optional internal plan: requirements, features, milestones, validation contract, role plan, shared / knowledge artifacts, plan approval state

The default user-facing Web entry is intentionally narrower than the data model: one Goal button plus the prompt. The agent can later split the goal into criteria, validation, tasks, features, or milestones using tools and ordinary session work.

The model-facing tools are intentionally narrow:

- `get_goal` reads the current durable goal
- `create_goal` creates one current goal when explicitly asked
- `update_goal` may only mark an existing goal `complete`

Pause, resume, clear, and budget-limited transitions are user/system controlled through CLI, WebConsole, or runtime accounting. Budget limited means the model should wrap up progress, evidence, blockers, and remaining work; it is not completion.

## 1.2 Session Plan Mode

Each session may write:

```text
.go-cli-agent/sessions/<id>/planmode.json
.go-cli-agent/sessions/<id>/artifacts/planmode-history.jsonl
.go-cli-agent/sessions/<id>/artifacts/planmode-plan.md
```

The Plan Mode snapshot records:

- objective and source
- status: `planning`, `awaiting_user_input`, `awaiting_approval`, `approved`, `cancelled`, or `executing`
- plan id/version and approved version
- submitted Markdown plan, summary, assumptions, risks, and verification
- pending `request_user_input` request, including `tool_call_id`
- approval records

Plan Mode is an execution gate, not a workflow engine. Pending Plan Mode suppresses mutating/execution tools through both provider schema filtering and `CompletionController`; it does not convert the plan into Todo/Task rows, does not force child agents, and does not replace Goal.

Approval must leave a replayable fact: the runtime appends a user message with `meta.source=planmode_approval` before execution resumes. Answering or cancelling pending `request_user_input` must append the matching tool result using the stored `tool_call_id`.

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
- active goal completion audit gate
- pending Plan Mode gate, before artifact/parent/goal completion gates

The first migration is behavior-equivalent for existing guard kinds and messages. New generic artifact checks are limited to explicit required artifacts.
When a current goal is `active`, `finish` is blocked until the model audits the objective, success criteria, and validation plan against concrete session evidence and calls `update_goal(status="complete")`. Paused or budget-limited goals may finish only as an explicit paused/wrap-up state; they must not be reported as complete unless the completion audit actually passed.
When Plan Mode is pending, `finish` and all mutating tools are blocked until the user approves or cancels the plan. This gate intentionally runs before goal completion audit so an active goal cannot pull execution through an unapproved plan.

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
- goal status, usage, criteria, validation, and internal plan status
- plan mode status, objective, version, pending input, and plan summary
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

The checkpoint is a resume index, not a replacement for messages/events/state. It records contract snapshot, goal snapshot, plan mode snapshot, todo/task summary, artifact status, latest compaction artifact, provider/model/options, workdir/isolation, unresolved child/queue state, and resume hints.

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
