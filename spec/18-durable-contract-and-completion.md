# Durable Contract And Completion

## 1. Scope

This spec defines the durable hardening layer for Web-first v1 convergence:

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
.aegis-agent/sessions/<id>/goal.json
.aegis-agent/sessions/<id>/artifacts/goal-history.jsonl
```

The goal snapshot records:

- `objective`
- `mode`: persisted for compatibility; default user-facing creation uses unified `goal`
- `status`: `active`, `paused`, `budget_limited`, or `complete`
- token / provider-time budgets and usage；兼容字段 `time_budget_seconds` 明确只累计成功或失败 provider call 的 elapsed provider time，不代表 session active runtime 或 wall clock
- success criteria
- validation plan
- user control settings
- optional internal plan: requirements, features, milestones, validation contract, role plan, shared / knowledge artifacts, plan approval state

The default user-facing Web entry is intentionally narrower than the data model: one Goal button plus the prompt. The agent can later split the goal into criteria, validation, tasks, features, or milestones using tools and ordinary session work.

The durable Goal objective accepts at most 12,000 Unicode characters across Web, CLI, runtime, store validation, and the `create_goal` tool. Plan Mode keeps its independent objective limit; increasing the Goal limit must not silently widen unrelated prompt or Plan Mode boundaries.

The model-facing tools are intentionally narrow:

- `get_goal` reads the current durable goal
- `create_goal` creates one current goal when explicitly asked
- `record_goal_progress` appends structured progress, handoff, validation, feature, milestone, child / queue, evaluator, command, artifact, blocker, or budget wrap-up facts to the current goal
- `update_goal` may only mark an existing goal `complete`

`record_goal_progress` is a snapshot patch and history append tool, not a workflow runner. It must not edit the objective, pause / resume / clear the goal, approve plans, change completion status, or bypass the completion audit. Feature / milestone / validation updates are ID-based and append-friendly; validation evidence can record evaluator child sessions or queue jobs without forcing the runtime to spawn them. Every feature, milestone, validation, claimed assertion, and milestone validation reference must use the current system-assigned ID returned by `get_goal`. Unknown-ID errors list the valid IDs and direct the model back to `get_goal`; when the corresponding valid list is empty, the error tells the model to omit that update/reference field. Invalid mission contract references are rejected when progress is recorded instead of waiting for the later coverage gate.

Pause, resume, clear, and budget-limited transitions are user/system controlled through CLI, WebConsole, or runtime accounting. Budget limited means the model should wrap up progress, evidence, blockers, and remaining work; it is not completion.

Mission validation contracts are checked as a read-only coverage report. The report counts validation assertions, assertions covered by feature `claimed_assertions` or milestone `validation_ids`, uncovered assertions, features with no assertions, milestones with no validation IDs, duplicate / blank contract IDs, and unknown references. Mission plan approval must block when the validation contract has uncovered or invalid assertions unless the operator uses an explicit override flag / API field. The checker does not run tests and does not require ordinary goals to define a validation contract.

## 1.2 Session Plan Mode

Each session may write:

```text
.aegis-agent/sessions/<id>/planmode.json
.aegis-agent/sessions/<id>/artifacts/planmode-history.jsonl
.aegis-agent/sessions/<id>/artifacts/planmode-plan.md
```

The Plan Mode snapshot records:

- objective and source
- optional `linked_goal_id` when Plan Mode was created for a goal/mission approval gate
- status: `planning`, `awaiting_user_input`, `awaiting_approval`, `approved`, `cancelled`, or `executing`
- plan id/version and approved version
- submitted Markdown plan, summary, assumptions, risks, and verification
- pending `request_user_input` request, including `tool_call_id`
- approval records

Plan Mode is an execution gate, not a workflow engine. Pending Plan Mode suppresses mutating/execution tools through both provider schema filtering and `CompletionController`; it does not convert the plan into Todo/Task rows, does not force child agents, and does not replace Goal. A mission that sets `require_plan_approval` or `needs_approval` must reuse this gate through linked Plan Mode instead of relying on a display-only mission status. If a mission is patched back to `needs_approval` after an earlier plan was approved or executing, the runtime must create or restore a pending linked gate; an already approved/executing Plan Mode is not sufficient. Web/API patch endpoints must not set mission plan status directly to `approved`; approval goes through the explicit mission approve or Plan Mode approve path. If an approved mission plan or validation contract is edited, the mission status must be reset to `needs_approval` and a pending linked Plan Mode must exist before further execution. If a pending Plan Mode already exists without `linked_goal_id`, the runtime may link it to the current goal and append an audit history entry.

Approval must leave a replayable fact: the runtime appends a user message with `meta.source=planmode_approval` before execution resumes. Answering or cancelling pending `request_user_input` must append the matching tool result using the stored `tool_call_id`.

Runtime `continue` Plan Mode controls must preflight the current `planmode.json` fact before claiming the session as running. Approve requires an approval-ready or idempotent executing Plan Mode, cancel requires an existing Plan Mode, and recovered input answers require a non-empty matching `request_id` plus answers that validate against the stored pending request. Invalid controls must return an error without changing `state.json`, appending replay messages, or advancing Plan Mode facts.
Before Web Plan Mode input or cancel controls choose the active-runner path, the Web service must prune current-process handles whose durable session state is already `failed` or `completed`; stale terminal handles are only owner clues and must not block recovered `request_user_input` answer/cancel replay through `continue`.
The same terminal-state pruning boundary applies before Web stop / interrupt controls decide current-process ownership: a stale `failed` or `completed` handle must return the structured not-owned response instead of calling a settled runner.

## 2. Session Contract

Each session may write:

```text
.aegis-agent/sessions/<id>/contract.json
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
.aegis-agent/sessions/<id>/artifact-tracker.json
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

If the goal is still active but execution cannot continue because an external prerequisite failed, a user decision is missing, or an external state must change, the model may call `await_input`. This is a separate non-completion terminal action for the current run: it persists the blocker and resume condition, stops the remaining tool batch, and moves the session to `awaiting_input` while leaving the Goal active. Target/artifact completion guards must not force the model to invent a deliverable merely to use this blocked path.

`update_goal(status="complete")` persists the completion audit into `goal.json`, including evidence, optional summary, and any criteria / validation item status updates supplied by the model. `artifacts/goal-history.jsonl` remains the append-only audit trail, but Mission Control, `session.md`, checkpoints, and recovery prompts must be able to read the current completion evidence from the goal snapshot without reconstructing it from history.
When Plan Mode is pending, `finish` and all mutating tools are blocked until the user approves or cancels the plan. This gate intentionally runs before goal completion audit so an active goal cannot pull execution through an unapproved plan.

When runtime accounting moves a goal to `budget_limited` and `stop_on_budget=true`, the runtime records a budget wrap-up request and allows at most one model wrap-up turn. That turn should call `record_goal_progress` with `kind="budget_wrapup"` and include progress, evidence, remaining work, commands, and blockers. After that wrap-up opportunity, the runtime stops further provider turns by returning to `awaiting_input` unless the model has legitimately completed the goal through `update_goal(status="complete")` and the normal finish path. A budget-limited goal with `stop_on_budget=true` cannot call `finish` until a budget wrap-up record exists.

Events:

- `completion.evaluate.started`
- `completion.gate.passed`
- `completion.gate.blocked`
- `completion.evaluate.finished`
- `artifact.tracked`
- `artifact.gate.passed`
- `artifact.gate.blocked`

`yolo` mode still disables non-essential runtime reminders and checks, but it does not bypass explicit user contracts, workspace path safety, shell timeout, or required artifact gates. `read_file` / grep / glob / read-only shell inspection do not have a runtime call-count or reread budget in either mode.

## 5. Provider Attempt Ledger

Provider attempts are recorded in:

```text
.aegis-agent/sessions/<id>/provider-attempts.jsonl
```

The ledger records retry, auto-resume, final failure, and success facts:

- turn and attempt
- provider and model
- timeout policy
- outcome
- retryability
- status code, error class, timeout kind
- response id when available
- cache read/write token counters when available
- created time

The ledger is diagnostic only. Provider retry policy remains adapter-owned and is not driven by this file.

## 6. Session Summary

The runtime writes a derived operator view:

```text
.aegis-agent/sessions/<id>/session.md
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
.aegis-agent/sessions/<id>/checkpoints/longrun-latest.json
```

The checkpoint is a resume index, not a replacement for messages/events/state. It records contract snapshot, goal snapshot, plan mode snapshot, todo/task summary, artifact status, latest compaction artifact, provider/model/options, workdir/isolation, unresolved child/queue state, and resume hints.

Normal `continue` still works without a checkpoint. When a checkpoint exists, `continue` may inject a harness resume note and emits visible checkpoint events rather than silently changing behavior.

## 8. Parent Coordination

Parent sessions with explicit child or queue work may write:

```text
.aegis-agent/sessions/<parent>/parent-coordination.json
```

The coordination file records:

- `wait-all` or `wait-any`
- unresolved child sessions
- unresolved queue jobs
- completed、cancelled 与 failed children/jobs
- parked/resumed state

The completion controller blocks parent `finish` while any unresolved child/queue work remains. `wait-any` can let the parent continue after one child/job result is available, but it is not a completion exemption: before the parent exits, every remaining child/job must either finish, produce a durable stopped/failed result, or be explicitly resolved through a real control action such as stopping an unclaimed queued job.

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
