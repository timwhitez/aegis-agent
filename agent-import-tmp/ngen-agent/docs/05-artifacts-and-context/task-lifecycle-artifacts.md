# 任务生命周期工件

## 1. `task.json`

```json
{
  "schema_version": 1,
  "task_id": "TASK-20260319-001",
  "kind": "coding",
  "preset_id": "",
  "title": "Implement durable status command",
  "objective": "Add a durable status command to ngen.",
  "success_criteria": [
    {
      "id": "SC-001",
      "statement": "Status returns current phase, state and key refs."
    }
  ],
  "constraints": [
    "Keep runtime local-first."
  ],
  "workspace_root": "/repo",
  "permission_mode_id": "standard",
  "root_task_id": "TASK-20260319-001",
  "lineage_depth": 0,
  "subagent_policy": {
    "permission_mode_id": "standard",
    "workspace_isolation": "auto",
    "reconcile_mode": "apply_on_accept",
    "auto_release_on_success": true,
    "allow_child_workers": true,
    "allowed_worker_roles": ["coding", "general_execution", "reviewer", "security_review"],
    "max_workers_per_task": 4,
    "max_lineage_depth": 2
  },
  "created_at": "2026-03-19T10:00:00Z"
}
```

补充说明：

- root task 现在也会在 `task.json` 内显式冻结 `root_task_id`、`lineage_depth` 与 effective `subagent_policy`。
- worker child task 还会额外写出 `parent_task_id` 与 `parent_worker_id`，使 nested delegation 的 lineage contract 可审计。

## 2. `plan.json`

Foundation v0.1 现在使用双语义 runtime plan：system lane 由 runtime synthesize baseline、每条 success criterion 与最终 review/done gate；mutable execution lane 则由 operator / provider 显式改写，用来持久化大型项目的执行清单。runtime 只会自动重写 system lane，不会把 execution lane 误当成 criteria / completion truth。mutable lane 现在是 graph-capable contract：每个 execution step 都可以持有 stable `id`、`parent_step_id`、`depends_on` 与 `priority`，而 `plan.json` 还会显式暴露 `revision`、`ready_execution_step_ids`、`blocked_execution_step_ids` 与 `last_mutation_ref`。显式改写当前分成两类：`task update` / `task_update` 承担全量 rewrite，`task patch` / `task_patch` 承担顺序 patch mutation；patch op 当前冻结为 `set_explanation`、`upsert_step` 与 `remove_step`。若远端 provider 在明显多阶段任务上直接进入 `run` / `resume` 而 execution lane 仍为空，runtime 会先写一个 system-sourced、one-criterion-at-a-time 的 bootstrap execution lane，确保长期任务从第一轮开始就有 durable checklist。

```json
{
  "schema_version": 1,
  "task_id": "TASK-20260319-001",
  "updated_at": "2026-03-19T10:01:00Z",
  "revision": 3,
  "explanation": "Initial mutable execution plan synthesized from the current open criteria as a one-criterion-at-a-time ladder.",
  "current_system_step_id": "STEP-002",
  "current_execution_step_id": "epic.repo_truth",
  "ready_execution_step_ids": ["epic.repo_truth"],
  "blocked_execution_step_ids": ["handoff.closeout"],
  "last_mutation_ref": "plan_updates.jsonl#mutation_id=PLN-001",
  "steps": [
    {
      "id": "STEP-001",
      "kind": "baseline",
      "source": "system",
      "title": "Capture baseline",
      "status": "completed",
      "covers": ["SC-001"],
      "verifier": ["baseline"]
    },
    {
      "id": "epic.repo_truth",
      "kind": "execution",
      "source": "operator",
      "priority": "high",
      "title": "Status returns current phase, state and key refs.",
      "status": "in_progress",
      "covers": ["SC-001"],
      "notes": "Bootstrap execution lane synthesized before the first remote run."
    },
    {
      "id": "handoff.closeout",
      "kind": "execution",
      "source": "operator",
      "parent_step_id": "epic.repo_truth",
      "depends_on": ["epic.repo_truth"],
      "priority": "medium",
      "title": "Refresh handoff and close the task",
      "status": "pending",
      "covers": ["SC-001"]
    },
    {
      "id": "STEP-002",
      "kind": "criterion",
      "source": "system",
      "title": "Satisfy SC-001: Status returns current phase, state and key refs.",
      "status": "in_progress",
      "covers": ["SC-001"],
      "verifier": ["profile_default"]
    },
    {
      "id": "STEP-003",
      "kind": "review_gate",
      "source": "system",
      "title": "Review evidence, refresh handoff, and close the task",
      "status": "pending",
      "covers": ["SC-001"],
      "verifier": ["review", "completion_gate", "handoff"]
    }
  ]
}
```

补充说明：

- 初始 `plan.json` 就必须显式列出 baseline step、每条 criterion step 与最终 gate step，而不是只有固定 `baseline + verifier/review` 两步；
- mutable execution lane 通过 `task update` / `task patch`、ACP `task.update` / `task.patch`、或 provider `task_update` / `task_patch` action 改写；`task update` 发送全量 checklist，`task patch` 发送顺序 patch operations；显式改写必须保持 stable step id continuity，并在需要时用 `parent_step_id` / `depends_on` / `priority` 表达 hierarchy、blocker 与 ordering；若这些显式写入尚未发生，而远端 provider 又直接选择 `run` / `resume`，runtime 可以先写一个 system-sourced、one-criterion-at-a-time 的 bootstrap execution lane；它不会直接闭合 criterion，只表达 execution rhythm；
- narrative refresh 时，runtime 必须按最新 `baseline.json`、`criteria/latest.json`、`completion/latest.json` 与 worker/runtime artifacts 重写 system lane step status，并保留 execution lane；
- `revision` / `last_mutation_ref` / `ready_execution_step_ids` / `blocked_execution_step_ids` 属于 active field；headless surface、progress/handoff render 与 provider input 都可以直接消费它们，而不是每次重建 graph state；
- `state.json.current_step_id` 默认优先回指当前 execution step；若 execution lane 为空或所有 execution steps 已 `completed/cancelled`，则回指当前 open system gate step。

## 2A. `plan_updates.jsonl`

mutable execution lane 的 append-only mutation history：

```json
{
  "schema_version": 1,
  "mutation_id": "PLN-001",
  "task_id": "TASK-20260319-001",
  "revision": 3,
  "mutation_kind": "patch",
  "source": "operator",
  "ts": "2026-03-19T10:01:00Z",
  "explanation": "Track the current execution checklist.",
  "current_execution_step_id": "epic.repo_truth",
  "ready_execution_step_ids": ["epic.repo_truth"],
  "blocked_execution_step_ids": ["handoff.closeout"],
  "patch_operations": [
    {
      "op": "remove_step",
      "step_id": "legacy.closeout"
    }
  ],
  "steps": [
    {
      "id": "epic.repo_truth",
      "kind": "execution",
      "source": "operator",
      "priority": "high",
      "title": "Inspect repo truth",
      "status": "in_progress"
    }
  ]
}
```

补充说明：

- `plan_updates.jsonl` 只记录显式 mutable execution 改写，不记录 runtime 对 system lane 的 narrative refresh；
- `plan.json.last_mutation_ref` 必须回链到这份 history；
- mutation record 必须显式标出 `mutation_kind=replace|patch`；若是 `patch`，还必须把原始 patch operations 持久化，避免 operator/provider 只能靠前后快照猜测本次 mutation 的意图；
- system-sourced bootstrap execution lane 也要走同一 append-only history，而不是做 silent bootstrap。

## 2B. `.ngen/project/project.json`

workspace-level project graph 是 singular artifact，不替代 task truth。它用于把跨 task / child-task 的 durable orchestration、dependency edges 与 concurrent branches 收口成一个可 patch 的 workspace graph：

```json
{
  "schema_version": 1,
  "workspace_root": "/repo",
  "updated_at": "2026-03-24T13:00:00Z",
  "revision": 5,
  "explanation": "Coordinate the repo-truth lane and the patch lane through a durable workspace graph.",
  "current_step_id": "epic.repo_truth",
  "ready_step_ids": ["epic.repo_truth"],
  "blocked_step_ids": ["epic.patch"],
  "active_branch_ids": ["branch.repo"],
  "last_mutation_ref": "project_updates.jsonl#mutation_id=PRJ-001",
  "steps": [
    {
      "id": "epic.repo_truth",
      "priority": "high",
      "title": "Inspect repo truth",
      "status": "in_progress",
      "branch_id": "branch.repo",
      "task_id": "TASK-20260324-001",
      "notes": "Track the primary repo task."
    },
    {
      "id": "epic.patch",
      "parent_step_id": "epic.repo_truth",
      "depends_on": ["epic.repo_truth"],
      "priority": "medium",
      "title": "Apply the remaining patch",
      "status": "blocked",
      "branch_id": "branch.patch",
      "task_id": "TASK-20260324-002"
    }
  ],
  "branches": [
    {
      "id": "branch.repo",
      "title": "Repo truth lane",
      "status": "active",
      "task_id": "TASK-20260324-001",
      "task_ref": "tasks/TASK-20260324-001/task.json",
      "status_ref": "tasks/TASK-20260324-001/state.json",
      "handoff_ref": "",
      "workspace_root": "/repo",
      "last_reason_code": ""
    }
  ]
}
```

补充说明：

- project step 允许 `parent_step_id` / `depends_on` / `branch_id` / `task_id` 同时出现，但它们只表达 orchestration，不会直接闭合任何单 task criterion；
- branch 是 project graph 中的一等并发 lane；当 branch 绑定了真实 `task_id`，runtime 会自动回写 `task_ref` / `status_ref` / `handoff_ref` / `workspace_root` / `last_reason_code`；
- provider input 现在可以直接消费这个 graph，而不是要求模型自己从多个 task artifacts 拼出 project 状态；
- current/ready/blocked/active summary 属于 active field；surface 可以直接消费，不需要每次重算。

## 2C. `.ngen/project/project_updates.jsonl`

project graph 的 append-only mutation history：

```json
{
  "schema_version": 1,
  "mutation_id": "PRJ-001",
  "revision": 5,
  "mutation_kind": "patch",
  "source": "provider",
  "ts": "2026-03-24T13:00:00Z",
  "explanation": "Promote the patch lane once repo truth is validated.",
  "current_step_id": "epic.repo_truth",
  "ready_step_ids": ["epic.repo_truth"],
  "blocked_step_ids": ["epic.patch"],
  "active_branch_ids": ["branch.repo"],
  "patch_operations": [
    {
      "op": "set_step_dependencies",
      "step_id": "epic.patch",
      "depends_on": ["epic.repo_truth"]
    },
    {
      "op": "bind_step_task",
      "step_id": "epic.patch",
      "task_id": "TASK-20260324-002"
    }
  ]
}
```

补充说明：

- `project_updates.jsonl` 只记录显式 project graph mutation，不记录 runtime 对 branch task refs/status refs 的自动 refresh；
- `project patch` / `project_patch` 当前除了 `set_explanation`、`upsert_step`、`remove_step`、`upsert_branch` 与 `remove_branch` 之外，还冻结了 `set_step_dependencies`、`set_step_parent`、`bind_step_branch`、`bind_step_task`、`bind_branch_task` 与 `set_branch_status` 这组更细粒度的 edge ops；
- auto-tracked task creation 也会追加 workspace project mutation，避免新 durable task 漏出 singular project graph。

## 3. `state.json`

```json
{
  "schema_version": 1,
  "task_id": "TASK-20260319-001",
  "phase": "Explore",
  "state": "Active",
  "status_reason_code": "",
  "status_detail_ref": "",
  "current_step_id": "STEP-001",
  "permission_mode_id": "standard",
  "last_event_ref": "",
  "last_verification_ref": "",
  "last_review_ref": "",
  "last_completion_ref": "",
  "last_checkpoint_ref": "checkpoints/0001.json",
  "updated_at": "2026-03-19T10:01:00Z"
}
```

## 4. `baseline.json`

```json
{
  "schema_version": 1,
  "task_id": "TASK-20260319-001",
  "captured_at": "2026-03-19T10:02:00Z",
  "workspace_root": "/repo",
  "repo_truth_refs": [
    "workspace:README.md"
  ],
  "command_hints": [
    {
      "kind": "setup",
      "command": ["bash", "./init.sh"],
      "reason": "Workspace exposes an init bootstrap command.",
      "source_ref": "workspace:init.sh"
    },
    {
      "kind": "verify",
      "command": ["./build.sh", "test"],
      "reason": "Repo-owned verifier command for this task.",
      "source_ref": "workspace:ngen.json"
    }
  ],
  "workspace_snapshot": {
    "git": {
      "is_repository": true,
      "branch": "main",
      "head": "abc1234",
      "dirty": true,
      "status_summary": "dirty working tree",
      "changed_paths": ["README.md"],
      "recent_commits": [
        {"sha": "abc1234", "subject": "bootstrap repo"}
      ]
    }
  },
  "environment": {
    "os": "linux",
    "go_version": "go1.24.5"
  },
  "available_verifiers": [
    "baseline",
    "go_test"
  ],
  "missing_prerequisites": []
}
```

补充约束：

- `command_hints` 是 runtime 提前识别出的 repo-owned setup / verifier entrypoint，用于给 provider/operator 一个 durable “先从这里起步”的 bearings surface；它不是执行记录，也不会替代 `command_runs.jsonl`
- `workspace_snapshot.git` 是 baseline capture 时的轻量 git bearings；它服务于长任务恢复与 review/handoff，对 `.git` 本身的更深读取仍受 visibility 与 observation rules 收口

## 5. `verification/latest.json`

```json
{
  "schema_version": 1,
  "task_id": "TASK-20260319-001",
  "report_id": "VER-001",
  "status": "passed",
  "profile": "coding",
  "ran_at": "2026-03-19T10:04:00Z",
  "checks": [
    {
      "name": "go_test",
      "status": "passed",
      "summary": "go test ./... passed",
      "evidence_refs": [
        "events.jsonl#event_id=EVT-004"
      ]
    }
  ],
  "failure_summary": ""
}
```

## 6. `command_runs.jsonl`

当前 `coding` task 在任一支持 repair loop 的 provider mode（`builtin`、`command`、`openai-response`、`openai-comp`、`anthropic`）下，若 verifier failure 或 workspace-backed criteria gap 需要额外观察 workspace，再决定如何修复，runtime 会先把 bounded read-only observation commands 追加到 `command_runs.jsonl`，并把 bounded 输出落到 `commands/<command_id>/stdout.txt` / `stderr.txt`。若同一次 repair 还需要 formatter、generator、dependency sync、package install、migration 或 shell-backed repair，runtime 也会把对应的 bounded workspace repair command 追加到同一个 `command_runs.jsonl`，只是 `kind` 变成 `repair_command`。repair command 现在还会冻结 `permission_mode_id` 与 `policy_decision`：`standard` mode 只自动执行 allowlisted safe commands，shell/script/package-manager/repo-script 这类命令会被记录为需要批准或拒绝，`yolo` mode 则以显式 policy decision 扩大执行范围；`permission.benchmark_integrity_mode=true` 时，网络或 open-world repair command 必须失败并记录 `policy_decision=denied_benchmark_integrity`，即使当前 task 是 `yolo`。这里的 workspace-backed 既包括显式 path / glob criterion，也包括带 readme/docs/config 语义和具体 token 的 criterion。非法 observation command、超时、非零退出，以及 failed repair command 同样必须留下 durable truth：

command output capture 是 bounded artifact contract：observation command、repair command、command provider helper 与 hook stdout/stderr 当前都以 1 MiB 为单流上限捕获。命中上限时，runtime 仍写出已捕获的 bounded stdout/stderr artifact 与 excerpt，并在 `CommandRunRecord` 中设置 `stdout_truncated` / `stderr_truncated`，summary 必须说明哪个 stream 超过上限；provider HTTP response body 则在 adapter decode 前以 4 MiB 上限拒绝。

```json
{
  "schema_version": 1,
  "command_record_id": "CMDREC-001",
  "command_id": "CMD-001",
  "task_id": "TASK-20260319-001",
  "ts": "2026-03-19T10:04:10Z",
  "kind": "observation_command",
  "status": "completed",
  "summary": "Read the current Add implementation before patching it.",
  "argv": ["sed", "-n", "1,80p", "calc.go"],
  "exit_code": 0,
  "stdout_ref": "commands/CMD-001/stdout.txt",
  "stderr_ref": "commands/CMD-001/stderr.txt",
  "stdout_excerpt": "package main\\n\\nfunc Add(a, b int) int { return a - b }",
  "stderr_excerpt": "",
  "replay_safety": {
    "side_effect_class": "read_only_command",
    "replay_policy": "safe_to_replay",
    "read_only": true,
    "idempotent": true,
    "summary": "Bounded observation command is read-only; replay is allowed from the same workspace state."
  }
}
```

repair command 示例：

```json
{
  "schema_version": 1,
  "command_record_id": "CMDREC-002",
  "command_id": "CMD-002",
  "task_id": "TASK-20260320-001",
  "ts": "2026-03-20T11:27:18Z",
  "kind": "repair_command",
  "status": "completed",
  "summary": "Format calc.go after rewriting it.",
  "argv": ["gofmt", "-w", "calc.go"],
  "permission_mode_id": "standard",
  "policy_decision": "allow",
  "exit_code": 0,
  "stdout_ref": "commands/CMD-002/stdout.txt",
  "stderr_ref": "commands/CMD-002/stderr.txt",
  "stdout_excerpt": "",
  "stderr_excerpt": "",
  "replay_safety": {
    "side_effect_class": "workspace_repair_command",
    "replay_policy": "safe_to_replay",
    "writes_workspace": true,
    "idempotent": true,
    "summary": "gofmt is an idempotent workspace formatter."
  }
}
```

`replay_safety` 是 replay/reconcile guard，不是权限结果本身。`safe_to_replay` 只允许 read-only 或明确 idempotent 的 command 被自动重复；`manual_review_required` / `do_not_auto_replay` 的 repair command 若已有同 argv side-effect record，runtime 必须拒绝再次自动执行并写出新的 failed command record。observation command 必须保持 read-only 和 visibility-safe：`find` 只接受 expression 前的 path operands，`-H`、`-L`、`-follow`、读取外部 path operand 的 predicate（例如 `-newer`、`-samefile`）和未知 predicate 都必须失败并留下 command artifact；`rg` / `ls` 不能通过 hidden/ignored flags 或 broad path 覆盖 deny roots；`go` observation 不允许 verifier/build 或 mutating flags；`git` observation 不允许 `--no-index`、external diff/textconv、output files、`rev:path` content reads、ignored listing 或 deny-path pathspec bypass。

## 7. `workspace_edits.jsonl`

当前 `coding` task 在任一支持 repair loop 的 provider mode（`builtin`、`command`、`openai-response`、`openai-comp`、`anthropic`）下，若 verifier failure 或 workspace-backed criteria gap 触发 bounded workspace repair，必须把 provider-directed 文件修改记录追加到 `workspace_edits.jsonl`。同一 task run 可以追加多条 record，用来表示多次 repair attempt；违反 task constraints、planning/apply 失败或 no-op 的计划都必须留下 durable truth。后续 attempt 允许把这些 failed/noop record 的 summary 作为 prior failure context 再喂回 provider，但 canonical truth 仍然是 `workspace_edits.jsonl`：

```json
{
  "schema_version": 1,
  "edit_record_id": "EDITREC-001",
  "edit_id": "EDIT-001",
  "task_id": "TASK-20260319-001",
  "ts": "2026-03-19T10:04:30Z",
  "kind": "workspace_edit",
  "status": "applied",
  "provider_mode": "openai-response",
  "summary": "Add the missing NormalizeName implementation in a new source file.",
  "file_changes": [
    {
      "path": "normalize.go",
      "action": "write",
      "before_exists": false,
      "after_exists": true,
      "after_sha256": "0493ed17433fd4dc36c184568d85b5145571b24c1ebe9afe97db771fce959bc1"
    }
  ],
  "replay_safety": {
    "side_effect_class": "workspace_file_edit",
    "replay_policy": "do_not_auto_replay",
    "writes_workspace": true,
    "destructive": true,
    "summary": "Workspace file edits are not automatically replay-safe; use file hashes and workspace_edits evidence before retrying."
  }
}
```

workspace edits 和 worker reconcile edits 必须用 `before_*` / `after_*` hashes 解释已经发生的 side effect；runtime 不得在恢复时把它们当成可盲目重放的 intent。所有 workspace write/delete/patch 与 worker reconcile auto-apply 在读取 hash 或写入文件前都必须先解析 workspace root、检查路径仍在 workspace 内，并拒绝任意中间目录 symlink 或最终 symlink；不允许通过 symlink 把 parent/child workspace 变更折回 workspace 外。

provider workspace edit input 的 workspace snapshot 也遵循 no-follow boundary：`WalkDir` 阶段发现的 symlink 会被跳过，实际 `ReadFile` 前再次 `Lstat`，竞态出现的 symlink 同样跳过。collection metadata 必须用 `omitted_file_count`、`truncated`、`stop_reason="skipped symlink paths"` 与 bounded `omitted_paths` 暴露 symlink omission，而不是静默把 symlink target 注入 provider prompt。

## 8. `reviews/latest.json`

```json
{
  "schema_version": 1,
  "task_id": "TASK-20260319-001",
  "review_id": "REV-001",
  "status": "clear",
  "summary": "review cleared from artifact-backed verification, criteria, handoff, and worker evidence.",
  "reviewer_profile": "coding_reviewer",
  "review_context_refs": [
    "baseline.json",
    "verification/latest.json",
    "criteria/latest.json",
    "handoff.md",
    "sprint/latest.json",
    "workspace:.ngen/project/project.json"
  ],
  "changed_paths": ["README.md"],
  "worker_result_refs": ["worker_runtime/WKR-001.result.json"],
  "risk_summary": {
    "blocking_count": 0
  },
  "blocking_categories": [],
  "blocking_finding_refs": [],
  "reviewed_at": "2026-03-19T10:05:00Z"
}
```

补充说明：

- `reviews/latest.json` 只能消费 artifact refs 与 changed paths，不信任 executor prose。
- `blocking_categories` 使用稳定 vocabulary：`confirmed_defect`、`missing_evidence`、`scope_drift`、`complexity_risk`、`security_risk`、`stale_context_risk`、`worker_trust_gap`、`inferred_risk`、`not_observed`。
- 当 criteria 引用了 worker truth 时，review 必须检查 `worker_runtime/*.result|settlement|reconcile.json`；只有 `workers/*.json` contract 不足以关闭 parent completion gate。

## 9. `criteria/latest.json`

```json
{
  "schema_version": 1,
  "snapshot_id": "CRT-001",
  "task_id": "TASK-20260319-001",
  "updated_at": "2026-03-19T10:05:00Z",
  "summary": "1/2 acceptance criteria are passing; current focus is SC-002.",
  "current_criterion_id": "SC-002",
  "current_criterion_statement": "README.md mentions `timeout_seconds`",
  "met_count": 1,
  "open_count": 1,
  "criteria": [
    {
      "criterion_id": "SC-001",
      "statement": "`./build.sh test` passes",
      "ordinal": 1,
      "status": "met",
      "passes": true,
      "selected": false,
      "last_summary": "Criterion is passing with durable evidence.",
      "last_evaluated_at": "2026-03-19T10:05:00Z",
      "last_transition_at": "2026-03-19T10:05:00Z",
      "evidence_refs": [
        "verification/latest.json",
        "reviews/latest.json",
        "handoff.md"
      ]
    },
    {
      "criterion_id": "SC-002",
      "statement": "README.md mentions `timeout_seconds`",
      "ordinal": 2,
      "status": "open",
      "passes": false,
      "selected": true,
      "last_summary": "Current acceptance focus remains open.",
      "last_evaluated_at": "2026-03-19T10:05:00Z",
      "last_transition_at": "2026-03-19T10:01:00Z",
      "evidence_refs": [
        "workspace_edits.jsonl#edit_record_id=EDITREC-002"
      ]
    }
  ]
}
```

补充说明：

- `criteria/latest.json` 当前是 task-local acceptance ledger，而不只是最小 met/open 快照；`passes=false` 表示该 criterion 仍然是 failing feature boundary。
- `current_criterion_id` / `current_criterion_statement` 是 runtime 当前建议保持聚焦的单项 acceptance boundary；provider 与 operator 默认应沿着它继续，而不是重新挑选新的 feature。
- `criteria/history.jsonl` 是 append-only refresh ledger；每次 baseline/create、verification refresh、review evidence refresh 后都要追加一条 record，方便 fresh context 观察 criterion focus 如何移动。

## 9A. `sprint/latest.json`

```json
{
  "schema_version": 1,
  "snapshot_id": "SPR-001",
  "task_id": "TASK-20260319-001",
  "updated_at": "2026-03-19T10:05:00Z",
  "summary": "Current sprint closes SC-002 before expanding into deferred criteria.",
  "objective": "Refresh README timeout documentation",
  "boundary": "Do not expand into deferred criteria yet: SC-003.",
  "current_step_id": "exec.docs",
  "current_step_title": "Refresh README timeout documentation",
  "current_execution_step_id": "exec.docs",
  "current_execution_step_title": "Refresh README timeout documentation",
  "current_system_step_id": "STEP-002",
  "current_system_step_title": "Close remaining success criteria",
  "primary_criterion_id": "SC-002",
  "primary_criterion_statement": "README.md mentions `timeout_seconds`",
  "active_criterion_ids": ["SC-002"],
  "deferred_criterion_ids": ["SC-003"],
  "completion_signals": [
    "README.md mentions `timeout_seconds`",
    "Verifier hint: ./build.sh test"
  ],
  "working_set_paths": ["README.md"],
  "refs": [
    "plan.json",
    "criteria/latest.json",
    "continuity/latest.json"
  ]
}
```

补充说明：

- `sprint/latest.json` 是 task-local current-scope contract；它不替代 `criteria/latest.json` 或 `continuity/latest.json`。
- `primary_criterion_id` / `completion_signals` 用来冻结“这一轮只该完成什么、什么证据算完成”；`deferred_criterion_ids` 用来显式标记暂时不应扩展的邻近 criterion。
- `sprint/history.jsonl` 是 append-only sprint refresh ledger；每次 narrative sync 后都要追加一条 record，方便 fresh context 回看 sprint boundary 如何移动。

## 10. `completion/latest.json`

```json
{
  "schema_version": 1,
  "task_id": "TASK-20260319-001",
  "completion_id": "CMP-001",
  "status": "accepted",
  "summary": "Done gate passed.",
  "criterion_results": [
    {
      "criterion_id": "SC-001",
      "status": "met",
      "evidence_refs": [
        "verification/latest.json",
        "reviews/latest.json",
        "handoff.md"
      ]
    }
  ],
  "blocking_refs": [],
  "handoff_ref": "handoff.md",
  "evaluated_at": "2026-03-19T10:05:00Z"
}
```

### `context/latest-pack.json`、`context/summary.md`、`continuity/latest.json` 与 `sprint/latest.json`

当前 runtime 在每次 narrative sync 后还会一起写出 task-local continuity pack、compaction summary，以及 machine-readable continuity / sprint ledger。它们服务于 long-horizon continuity，不替代 `state.json`、`verification/latest.json` 或 `reviews/latest.json` 的 canonical truth。

`context/latest-pack.json` 示例：

```json
{
  "schema_version": 1,
  "task_id": "TASK-20260319-001",
  "pack_id": "PACK-001",
  "phase": "Review",
  "state": "Active",
  "built_at": "2026-03-19T10:05:00Z",
  "updated_at": "2026-03-19T10:05:00Z",
  "summary": "Verifier is green; review and completion are the remaining gate.",
  "next_step": "Read the latest review, confirm criteria evidence, and finish the handoff.",
  "based_on_refs": [
    "task.json",
    "plan.json",
    "verification/latest.json"
  ],
  "included_refs": [
    "task.json",
    "plan.json",
    "verification/latest.json",
    "context/summary.md",
    "workspace:.ngen/memory/MEMORY.md"
  ],
  "sections": [
    {
      "name": "task",
      "token_budget": 900,
      "actual_tokens": 180,
      "refs": ["task.json", "baseline.json", "criteria/latest.json"]
    },
    {
      "name": "observations",
      "token_budget": 2200,
      "actual_tokens": 240,
      "refs": ["verification/latest.json", "reviews/latest.json"]
    }
  ],
  "compaction": {
    "performed": true,
    "summary_ref": "context/summary.md"
  },
  "status_reason_code": ""
}
```

`context/summary.md` 最低内容应覆盖：

- 当前 task focus / summary / next step
- 最新 verifier、review、completion truth
- 最近 repair commands 与 workspace edits
- continuity refs

当前 `context/summary.md` 是 deterministic narrative artifact，不是 provider chat-history compression result。它必须从当前 task artifacts 渲染，并与 `context/latest-pack.json`、`continuity/latest.json`、`sprint/latest.json` 在同一 narrative sync pass 内更新。未来若添加 model-backed 或 idle compaction，必须把 compaction provider call、usage、input refs、output refs、取消/失败状态和 resulting artifact refs 显式落盘；不得把 compression instruction 或 summary 塞进 system prompt、hidden session state 或未记录的 provider-visible context。

`continuity/latest.json` 示例：

```json
{
  "schema_version": 1,
  "snapshot_id": "CNT-001",
  "task_id": "TASK-20260319-001",
  "updated_at": "2026-03-19T10:05:00Z",
  "phase": "Review",
  "state": "Active",
  "summary": "Verifier is green; review and completion are the remaining gate.",
  "next_step": "Read the latest review, confirm criteria evidence, and finish the handoff.",
  "current_focus": {
    "current_step_id": "handoff.closeout",
    "current_step_title": "Refresh handoff and close the task",
    "current_execution_step_id": "handoff.closeout",
    "current_execution_step_title": "Refresh handoff and close the task",
    "current_system_step_id": "STEP-003",
    "current_system_step_title": "Review evidence, refresh handoff, and close the task",
    "criterion_ids": ["SC-001"],
    "criteria": [
      {"id": "SC-001", "statement": "Status returns current phase, state and key refs."}
    ],
    "working_set_paths": ["README.md"]
  },
  "startup_checklist": [
    {
      "id": "read_progress",
      "kind": "read_ref",
      "title": "Read progress.md",
      "ref": "progress.md"
    },
    {
      "id": "git_status",
      "kind": "vcs_command",
      "title": "Inspect git status",
      "command": ["git", "status", "--short"]
    }
  ],
  "criteria_met_count": 0,
  "criteria_total_count": 1,
  "open_criteria": [
    {"id": "SC-001", "statement": "Status returns current phase, state and key refs."}
  ],
  "verification_status": "passed",
  "verification_summary": "Latest verifier pass is clean.",
  "review_status": "pending",
  "review_summary": "Review has not run yet.",
  "completion_status": "not_evaluated",
  "completion_summary": "Completion gate has not been evaluated yet.",
  "refs": [
    "progress.md",
    "context/summary.md",
    "context/latest-pack.json"
  ]
}
```

补充约束：

- `continuity/latest.json` 是 task-local structured restart ledger；provider decision / workspace observation / workspace edit input 都可以直接消费它，而不是只消费 Markdown prose
- `continuity.history.jsonl` 是 append-only narrative-sync history；它用于给后续 operator/provider 快速回放最近几轮 summary / focus / checklist 演进
- `current_focus` 当前是 sprint-like contract：至少要收住 current step、当前 execution/gate pointers、focused open criteria 与 working set paths
- `startup_checklist` 必须优先指向已存在的 durable artifact refs 与 repo-owned command hints，而不是临时拼接新的 chat-only instructions
- `sprint/latest.json` 则是更短视距的 current-scope contract；provider decision / workspace observation / workspace edit input 都应优先沿着它的 `primary_criterion_id`、`completion_signals` 与 `deferred_criterion_ids` 继续，而不是重新摊开全部 open criteria

## 11. `events.jsonl`

Foundation v0.1 只冻结以下 event types：

- `task_created`
- `baseline_captured`
- `plan_updated`
- `state_changed`
- `verification_passed`
- `verification_failed`
- `observation_command_started`
- `observation_command_completed`
- `observation_command_failed`
- `workspace_edit_started`
- `workspace_edit_applied`
- `workspace_edit_noop`
- `workspace_edit_failed`
- `workspace_edit_stalled`
- `workspace_edit_budget_exhausted`
- `review_completed`
- `completion_rejected`
- `done`
- `input_requested`
- `input_responded`
- `approval_requested`
- `approval_decided`
- `watch_registered`
- `watch_woke`
- `failed`
- `aborted`

最小 event 对象：

```json
{
  "schema_version": 1,
  "event_id": "EVT-001",
  "task_id": "TASK-20260319-001",
  "ts": "2026-03-19T10:02:00Z",
  "phase": "Explore",
  "state": "Active",
  "type": "baseline_captured",
  "summary": "Captured workspace baseline.",
  "refs": [
    "baseline.json"
  ]
}
```

`event_id` 同时是 event replay cursor。CLI `events tail --after`、web JSON `?after=` 与 SSE `Last-Event-ID` / `?after=` 都必须从同一份 append-only `events.jsonl` 中定位 cursor，只返回 cursor 之后的 events；找不到 cursor 时必须报错，不能退回 latest tail。

## 12. `findings.jsonl`

只有 blocking review finding 需要进入当前 active contract；finding 必须带 category、severity、blocking bit、evidence refs 与 recommended action。若 finding 只属于推断风险或未观察到的 surface，必须使用 `inferred_risk` 或 `not_observed` category，不能伪装为 confirmed defect。

```json
{
  "schema_version": 1,
  "finding_id": "F-001",
  "task_id": "TASK-20260319-001",
  "ts": "2026-03-19T10:05:00Z",
  "severity": "high",
  "category": "missing_evidence",
  "status": "open",
  "blocks_completion": true,
  "claim": "Completion was requested without passing verifier.",
  "evidence_refs": [
    "verification/latest.json"
  ],
  "affected_paths": [],
  "recommended_action": "Run the profile verifier before claiming done."
}
```

## 12A. `diagnostics/quality-latest.json`

quality diagnostics 是 task-local anti-corruption lane。它不替代 verifier / review；runtime 在 review/done gate 前从 `workspace_edits.jsonl`、changed paths、sprint scope 与 repair failure history 计算它，并把 blocking findings 注入 review。

```json
{
  "object_kind": "quality_diagnostic",
  "schema_version": 1,
  "diagnostic_id": "QDIAG-001",
  "task_id": "TASK-20260319-001",
  "status": "blocking",
  "changed_path_count": 1,
  "changed_paths": ["calc_test.go"],
  "test_file_changes": ["calc_test.go"],
  "workspace_edit_attempts": 1,
  "noop_or_failed_edit_count": 1,
  "review_required": true,
  "block_completion": true,
  "recommended_action": "Revert the test-file change attempt or explicitly change the task contract before completion.",
  "findings": [
    {
      "category": "confirmed_defect",
      "severity": "high",
      "blocking": true,
      "summary": "Task constraints forbid test-file mutation, but test paths were targeted: calc_test.go.",
      "affected_paths": ["calc_test.go"],
      "evidence_refs": ["workspace_edits.jsonl#edit_record_id=EDITREC-001"]
    }
  ],
  "evidence_refs": ["workspace_edits.jsonl#edit_record_id=EDITREC-001"],
  "created_at": "2026-03-19T10:05:00Z"
}
```

当前 active blocking checks:

- task constraint 明确禁止修改 tests / `*_test.go`，但 workspace edit plan 针对 test file；
- changed paths 落在当前 sprint working set 外；
- repair loop 在预算内反复产生同一 failed/no-op edit pattern。

`quality-history.jsonl` 是 append-only 历史；`progress.md` 与 `handoff.md` 只在 status 非 `clear` 或 `review_required=true` 时渲染 Quality Diagnostics section。

## 12. `approvals.jsonl`

当前 approval log 只冻结两类记录：

- `approval_request(status=pending)`
- `approval_decision(status=approved|denied)`

worker child approval 允许额外记录 owner fields，但 durable truth 仍保留在 child task 自己的 `approvals.jsonl`。

```json
{
  "schema_version": 1,
  "approval_record_id": "APRREC-001",
  "approval_id": "APR-001",
  "task_id": "TASK-20260319-002",
  "owner_task_id": "TASK-20260319-001",
  "owner_worker_id": "WKR-001",
  "ts": "2026-03-19T10:03:00Z",
  "kind": "approval_request",
  "status": "pending",
  "scope": "allow manual destructive step",
  "reason": "Task requires an explicit operator decision."
}
```

`approval ls TASK-ID --owned` 与 ACP `permission.list(include_owned=true)` 只是在 parent side 通过 worker contract 聚合 owned child approval history；它们不创建第二份 approval artifact。

当前 active contract 进一步要求：

- parent task 的 provider / session context 必须能直接看到 owned child pending approvals；
- `workers/*.json` 与 `worker_snapshot` 必须能把当前 owned approval 的 `approval_id` / `approval_ref` 回显给 parent；
- `workers/*.json` 与 `worker_snapshot` 现在也必须把 `workspace_mode` / `workspace_status` / `workspace_ref`、`settlement_status` / `settlement_summary` / `settlement_ref`、`result_summary` / `result_ref` / `completion_status` / `review_status` / `verification_status`、blocked approval/input detail、`reconcile_mode` / `reconcile_status` / `reconcile_summary` / `reconcile_ref`，以及 worker evidence score fields 回显给 parent；
- parent-side decide 不会改写 child approval truth，但批准后 `worker continue` 会成为唯一的 continuation helper。

## 12. `input_requests.jsonl`

当前 structured input log 只冻结两类记录：

- `input_request(status=pending)`
- `input_response(status=answered)`

```json
{
  "schema_version": 1,
  "input_record_id": "INPREC-001",
  "request_id": "INP-001",
  "task_id": "TASK-20260319-001",
  "ts": "2026-03-19T10:03:30Z",
  "kind": "input_request",
  "status": "pending",
  "field": "target_path",
  "prompt": "Provide the target path for the next step.",
  "required": true
}
```

## 13. `checkpoints/*.json`

```json
{
  "schema_version": 1,
  "checkpoint_id": "CP-0002",
  "task_id": "TASK-20260319-001",
  "captured_at": "2026-03-19T10:04:00Z",
  "phase": "Verify",
  "state": "Active",
  "last_event_ref": "events.jsonl#event_id=EVT-004",
  "workspace_snapshot": {
    "git": {
      "is_repository": true,
      "branch": "main",
      "head": "abc1234",
      "dirty": true,
      "status_summary": "dirty working tree",
      "changed_paths": ["README.md"]
    }
  }
}
```

## 14. `watches/*.json`

```json
{
  "schema_version": 1,
  "watch_id": "WATCH-001",
  "task_id": "TASK-20260319-001",
  "status": "active",
  "interval_seconds": 300,
  "reason": "Wait for the next verification window.",
  "next_wake_at": "2026-03-19T10:10:00Z",
  "created_at": "2026-03-19T10:05:00Z",
  "updated_at": "2026-03-19T10:05:00Z"
}
```

## 15. `status_snapshot`

`status --json` 的最小稳定对象：

```json
{
  "object_kind": "status_snapshot",
  "schema_version": 1,
  "task_id": "TASK-20260319-001",
  "phase": "Review",
  "state": "Done",
  "status_reason_code": "",
  "status_detail_ref": "",
  "plan_ref": "plan.json",
  "progress_ref": "progress.md",
  "handoff_ref": "handoff.md",
  "mission_id": "MIS-20260511-001",
  "mission_ref": "workspace:.ngen/missions/MIS-20260511-001/mission.json",
  "mission_status": "blocked",
  "mission_status_reason_code": "blocked_validation",
  "mission_current_milestone_id": "MS-001",
  "mission_latest_validation_ref": "workspace:.ngen/missions/MIS-20260511-001/validation_runs.jsonl#validation_run_id=MVAL-001",
  "last_checkpoint_ref": "checkpoints/0002.json",
  "restore_clues": [
    {
      "ref": "checkpoints/0002.json",
      "summary": "checkpoint captured at phase=Review state=Done; git branch=main head=abc123 dirty=true; changed_paths=README.md",
      "git": {"is_repository": true, "branch": "main", "head": "abc123", "dirty": true, "changed_paths": ["README.md"]},
      "command_hints": [{"kind": "verifier", "command": ["./build.sh", "test"], "reason": "repo-owned verifier"}]
    }
  ],
  "plan_revision": 3,
  "current_step_id": "STEP-003",
  "current_system_step_id": "STEP-003",
  "current_execution_step_id": "",
  "last_event_ref": "events.jsonl#event_id=EVT-006",
  "last_verification_ref": "verification/latest.json",
  "last_review_ref": "reviews/latest.json",
  "completion_ref": "completion/latest.json",
  "updated_at": "2026-03-19T10:05:00Z"
}
```

补充说明：

- `plan_ref` 固定回链 `plan.json`
- `plan_revision` 允许 terminal / ACP / harness 直接判断 mutable task graph 是否发生过新一轮 durable mutation
- `current_step_id` 是 runtime 当前主视图 step pointer；若 mutable execution lane 非空且 task 未 `Done`，它默认优先指向当前 execution step
- `current_system_step_id` / `current_execution_step_id` 允许 headless / ACP consumer 同时看到当前 gate 与当前 execution checklist item，而不用自己从 `plan.json` 重建
- mission fields 只在 task 绑定到 workspace mission 时出现；它们回链 `.ngen/missions/<mission_id>/mission.json` 与最新 validation run，使 `status --json` / TUI compact state 能暴露 mission contract 状态，而不把 mission prose 当成 task truth
- `restore_clues` 直接从最新 checkpoint 与 baseline command hints 派生，用于长任务恢复时看到 checkpoint ref、git bearings、changed paths 与 repo-owned command entrypoint；它不替代 `checkpoints/*.json` 原始 artifact

## 15A. `task_view` 与 `task_list_entry`

`task.get --json` 与 ACP `task.get` 返回的稳定聚合对象：

```json
{
  "object_kind": "task_view",
  "schema_version": 1,
  "task": {"task_id": "TASK-20260319-001"},
  "state": {"current_step_id": "STEP-EXEC-001"},
  "plan": {
    "explanation": "Track the current execution checklist.",
    "revision": 3,
    "current_system_step_id": "STEP-002",
    "current_execution_step_id": "epic.repo_truth",
    "ready_execution_step_ids": ["epic.repo_truth"],
    "blocked_execution_step_ids": ["handoff.closeout"],
    "last_mutation_ref": "plan_updates.jsonl#mutation_id=PLN-001"
  },
  "status": {"object_kind": "status_snapshot"}
}
```

`task.list --json` 与 ACP `task.list` 返回的稳定摘要对象：

```json
{
  "object_kind": "task_list_entry",
  "schema_version": 1,
  "task_id": "TASK-20260319-001",
  "title": "Implement durable status command",
  "kind": "coding",
  "phase": "Execute",
  "state": "Active",
  "status_reason_code": "",
  "current_step_id": "STEP-EXEC-001",
  "current_system_step_id": "STEP-002",
  "current_execution_step_id": "STEP-EXEC-001",
  "updated_at": "2026-03-19T10:05:00Z"
}
```

## 16. `session_snapshot`

ACP `session.snapshot` 返回的稳定派生对象：

```json
{
  "object_kind": "session_snapshot",
  "schema_version": 1,
  "session_id": "SES-001",
  "task_id": "TASK-20260319-001",
  "mode": "acp",
  "session_status": "active",
  "last_prompt": "/run",
  "last_action": "review_completed",
  "session_ref": "workspace:.ngen/sessions/SES-001.json",
  "messages_ref": "workspace:.ngen/sessions/SES-001.messages.jsonl",
  "message_count": 2,
  "managed_workers": [
    {
      "schema_version": 1,
      "worker_id": "WKR-001",
      "parent_task_id": "TASK-20260319-001",
      "child_task_id": "TASK-20260319-002",
      "role": "reviewer",
      "objective": "Review the parent task output.",
      "status": "blocked",
      "blocked_reason_code": "blocked_policy",
      "approval_id": "APR-001",
      "approval_ref": "approvals.jsonl#approval_record_id=APRREC-001",
      "approval_status": "pending",
      "approval_scope": "allow manual destructive step",
      "approval_reason": "Task requires an explicit operator decision.",
      "workspace_root": "/repo",
      "workspace_mode": "shared_workspace",
      "workspace_status": "shared",
      "workspace_ref": "worker_runtime/WKR-001.workspace.json",
      "settlement_status": "blocked",
      "settlement_summary": "Worker is blocked: blocked_policy.",
      "settlement_ref": "worker_runtime/WKR-001.settlement.json",
      "result_summary": "Worker is blocked (blocked_policy). Worker WKR-001 is blocked on owned approval APR-001 (allow manual destructive step). Parent can approve, deny, or parent_takeover.",
      "result_ref": "worker_runtime/WKR-001.result.json",
      "reconcile_mode": "artifact_only",
      "reconcile_status": "pending",
      "reconcile_summary": "Worker reconcile is waiting for accepted settlement.",
      "reconcile_ref": "worker_runtime/WKR-001.reconcile.json",
      "requires_parent_action": true,
      "parent_action_type": "owned_approval_pending",
      "parent_action_options": ["approve", "deny", "parent_takeover"],
      "parent_action_summary": "Worker WKR-001 is blocked on owned approval APR-001 (allow manual destructive step). Parent can approve, deny, or parent_takeover.",
      "created_at": "2026-03-19T10:07:00Z",
      "updated_at": "2026-03-19T10:08:00Z"
    }
  ],
  "owned_pending_approvals": [
    {
      "schema_version": 1,
      "worker_id": "WKR-001",
      "child_task_id": "TASK-20260319-002",
      "approval_id": "APR-001",
      "approval_ref": "approvals.jsonl#approval_record_id=APRREC-001",
      "status": "pending",
      "scope": "allow manual destructive step",
      "reason": "Task requires an explicit operator decision.",
      "child_state": "Blocked",
      "blocked_reason_code": "blocked_policy",
      "requires_parent_action": true,
      "parent_action_type": "owned_approval_pending",
      "parent_action_options": ["approve", "deny", "parent_takeover"],
      "parent_action_summary": "Worker WKR-001 is blocked on owned approval APR-001 (allow manual destructive step). Parent can approve, deny, or parent_takeover."
    }
  ],
  "recent_messages": [
    {
      "schema_version": 1,
      "message_id": "MSG-001",
      "session_id": "SES-001",
      "task_id": "TASK-20260319-001",
      "role": "operator",
      "content": "/run",
      "ts": "2026-03-19T10:06:00Z"
    },
    {
      "schema_version": 1,
      "message_id": "MSG-002",
      "session_id": "SES-001",
      "task_id": "TASK-20260319-001",
      "role": "assistant",
      "content": "Hello. Tell me what to inspect, run, or change in the current task.",
      "ts": "2026-03-19T10:06:00Z"
    },
    {
      "schema_version": 1,
      "message_id": "MSG-003",
      "session_id": "SES-001",
      "task_id": "TASK-20260319-001",
      "role": "runtime",
      "content": "Done Review",
      "ts": "2026-03-19T10:06:01Z"
    }
  ],
  "status_snapshot": {
    "object_kind": "status_snapshot",
    "schema_version": 1,
    "task_id": "TASK-20260319-001",
    "phase": "Review",
    "state": "Done",
    "status_reason_code": "",
    "status_detail_ref": "",
    "progress_ref": "progress.md",
    "handoff_ref": "handoff.md",
    "last_event_ref": "events.jsonl#event_id=EVT-006",
    "last_verification_ref": "verification/latest.json",
    "last_review_ref": "reviews/latest.json",
    "completion_ref": "completion/latest.json",
    "updated_at": "2026-03-19T10:05:00Z"
  },
  "updated_at": "2026-03-19T10:06:01Z"
}
```

## 17. `worker_snapshot`

ACP `worker.spawn` / `worker.list` / `worker.sync` 返回的稳定派生对象：

```json
{
  "object_kind": "worker_snapshot",
  "schema_version": 1,
  "worker": {
    "schema_version": 1,
    "worker_id": "WKR-001",
    "parent_task_id": "TASK-20260319-001",
    "child_task_id": "TASK-20260319-002",
      "role": "reviewer",
      "objective": "Review the parent task output.",
      "status": "blocked",
      "blocked_reason_code": "blocked_policy",
      "blocked_detail_ref": "approvals.jsonl#approval_record_id=APRREC-001",
      "approval_id": "APR-001",
      "approval_ref": "approvals.jsonl#approval_record_id=APRREC-001",
      "approval_status": "pending",
      "approval_scope": "allow manual destructive step",
      "approval_reason": "Task requires an explicit operator decision.",
      "workspace_root": "/repo",
      "workspace_mode": "shared_workspace",
      "workspace_status": "shared",
      "workspace_ref": "worker_runtime/WKR-001.workspace.json",
      "settlement_status": "blocked",
      "settlement_summary": "Worker is blocked: blocked_policy.",
      "settlement_ref": "worker_runtime/WKR-001.settlement.json",
      "result_summary": "Worker is blocked (blocked_policy). Worker WKR-001 is blocked on owned approval APR-001 (allow manual destructive step). Parent can approve, deny, or parent_takeover.",
      "result_ref": "worker_runtime/WKR-001.result.json",
      "reconcile_mode": "artifact_only",
      "reconcile_status": "pending",
      "reconcile_summary": "Worker reconcile is waiting for accepted settlement.",
      "reconcile_ref": "worker_runtime/WKR-001.reconcile.json",
      "requires_parent_action": true,
      "parent_action_type": "owned_approval_pending",
      "parent_action_options": ["approve", "deny", "parent_takeover"],
      "parent_action_summary": "Worker WKR-001 is blocked on owned approval APR-001 (allow manual destructive step). Parent can approve, deny, or parent_takeover.",
    "created_at": "2026-03-19T10:07:00Z",
    "updated_at": "2026-03-19T10:08:00Z"
  },
  "parent_status": {
    "object_kind": "status_snapshot",
    "schema_version": 1,
    "task_id": "TASK-20260319-001",
    "phase": "Review",
    "state": "Done",
    "status_reason_code": "",
    "status_detail_ref": "",
    "progress_ref": "progress.md",
    "handoff_ref": "handoff.md",
    "last_event_ref": "events.jsonl#event_id=EVT-006",
    "last_verification_ref": "verification/latest.json",
    "last_review_ref": "reviews/latest.json",
    "completion_ref": "completion/latest.json",
    "updated_at": "2026-03-19T10:10:00Z"
  },
  "child_status": {
    "object_kind": "status_snapshot",
    "schema_version": 1,
    "task_id": "TASK-20260319-002",
    "phase": "Review",
    "state": "Blocked",
    "status_reason_code": "blocked_policy",
    "status_detail_ref": "approvals.jsonl#approval_record_id=APRREC-001",
    "progress_ref": "progress.md",
    "handoff_ref": "",
    "last_event_ref": "events.jsonl#event_id=EVT-004",
    "last_verification_ref": "",
    "last_review_ref": "",
    "completion_ref": "",
    "updated_at": "2026-03-19T10:08:00Z"
  },
  "updated_at": "2026-03-19T10:08:00Z"
}
```

## 18. `acp_notification`

ACP mutating 调用在 response 后可追加的稳定 JSON-RPC notification payload。`approval.updated` 允许像 `worker.updated` 一样可选附带 `worker_snapshot`，用于 parent-owned child approval resolution：

```json
{
  "object_kind": "acp_notification",
  "schema_version": 1,
  "notification_id": "NTF-001",
  "kind": "worker.updated",
  "task_id": "TASK-20260319-001",
  "worker_id": "WKR-001",
  "summary": "Worker state updated.",
  "ts": "2026-03-19T10:10:01Z",
  "status_snapshot": {
    "object_kind": "status_snapshot",
    "schema_version": 1,
    "task_id": "TASK-20260319-001",
    "phase": "Review",
    "state": "Done",
    "status_reason_code": "",
    "status_detail_ref": "",
    "progress_ref": "progress.md",
    "handoff_ref": "handoff.md",
    "last_event_ref": "events.jsonl#event_id=EVT-006",
    "last_verification_ref": "verification/latest.json",
    "last_review_ref": "reviews/latest.json",
    "completion_ref": "completion/latest.json",
    "updated_at": "2026-03-19T10:10:00Z"
  },
  "worker_snapshot": {
    "object_kind": "worker_snapshot",
    "schema_version": 1,
    "worker": {
      "schema_version": 1,
      "worker_id": "WKR-001",
      "parent_task_id": "TASK-20260319-001",
      "child_task_id": "TASK-20260319-002",
      "role": "reviewer",
      "objective": "Review the parent task output.",
      "status": "done",
      "handoff_ref": "../TASK-20260319-002/handoff.md",
      "workspace_root": "/tmp/repo/.ngen-worker-workspaces/repo/WKR-001/repo",
      "workspace_mode": "git_worktree",
      "workspace_status": "released",
      "workspace_ref": "worker_runtime/WKR-001.workspace.json",
      "settlement_status": "accepted",
      "settlement_summary": "Done gate passed.",
      "settlement_ref": "worker_runtime/WKR-001.settlement.json",
      "result_summary": "Worker reached Done. Statuses: completion=accepted, review=clear, verification=passed. Done gate passed.",
      "result_ref": "worker_runtime/WKR-001.result.json",
      "completion_status": "accepted",
      "review_status": "clear",
      "verification_status": "passed",
      "reconcile_mode": "artifact_only",
      "reconcile_status": "recorded",
      "reconcile_summary": "Recorded 1 isolated child change(s) without applying them because the worker role is artifact-only.",
      "reconcile_ref": "worker_runtime/WKR-001.reconcile.json",
      "continuation_count": 1,
      "last_continued_at": "2026-03-19T10:09:00Z",
      "last_reconciled_at": "2026-03-19T10:10:00Z",
      "created_at": "2026-03-19T10:07:00Z",
      "updated_at": "2026-03-19T10:10:00Z"
    },
    "parent_status": {
      "object_kind": "status_snapshot",
      "schema_version": 1,
      "task_id": "TASK-20260319-001",
      "phase": "Review",
      "state": "Done",
      "status_reason_code": "",
      "status_detail_ref": "",
      "progress_ref": "progress.md",
      "handoff_ref": "handoff.md",
      "last_event_ref": "events.jsonl#event_id=EVT-006",
      "last_verification_ref": "verification/latest.json",
      "last_review_ref": "reviews/latest.json",
      "completion_ref": "completion/latest.json",
      "updated_at": "2026-03-19T10:10:00Z"
    },
    "child_status": {
      "object_kind": "status_snapshot",
      "schema_version": 1,
      "task_id": "TASK-20260319-002",
      "phase": "Review",
      "state": "Done",
      "status_reason_code": "",
      "status_detail_ref": "",
      "progress_ref": "progress.md",
      "handoff_ref": "handoff.md",
      "last_event_ref": "events.jsonl#event_id=EVT-004",
      "last_verification_ref": "verification/latest.json",
      "last_review_ref": "reviews/latest.json",
      "completion_ref": "completion/latest.json",
      "updated_at": "2026-03-19T10:10:00Z"
    },
    "updated_at": "2026-03-19T10:10:00Z"
  }
}
```

## 19. `sessions/*.json`

```json
{
  "schema_version": 1,
  "session_id": "SES-001",
  "task_id": "TASK-20260319-001",
  "mode": "terminal",
  "status": "active",
  "last_prompt": "/run",
  "last_action": "provider_decided",
  "created_at": "2026-03-19T10:06:00Z",
  "updated_at": "2026-03-19T10:06:01Z"
}
```

补充约束：

- `.ngen/sessions/<session_id>.messages.jsonl` 持有同一 session 的 operator/assistant/runtime transcript truth；
- provider decision input、workspace observation prompt 与 workspace edit prompt 都必须显式带出 `session_messages_ref` 与 bounded `session_recent_messages`，而不是只暴露 `last_prompt`；
- `session.prompt` 允许在普通 conversational prompt 下先追加 assistant-style direct reply，再追加 runtime summary；`session.cancel` 仍必须追加 runtime cancellation summary。failed task outcome 与 operator cancel 也必须显式落进 `*.messages.jsonl`。

## 20. `workers/*.json`

```json
{
  "schema_version": 1,
  "worker_id": "WKR-001",
  "parent_task_id": "TASK-20260319-001",
  "child_task_id": "TASK-20260319-002",
  "role": "reviewer",
  "objective": "Review the parent task output.",
  "status": "active",
  "approval_id": "APR-001",
  "approval_ref": "approvals.jsonl#approval_record_id=APRREC-002",
  "approval_status": "approved",
  "approval_scope": "allow manual destructive step",
  "approval_reason": "Task requires an explicit operator decision.",
  "workspace_root": "/tmp/repo/.ngen-worker-workspaces/repo/WKR-001/repo",
  "workspace_mode": "git_worktree",
  "workspace_status": "released",
  "workspace_ref": "worker_runtime/WKR-001.workspace.json",
  "settlement_status": "accepted",
  "settlement_summary": "Done gate passed.",
  "settlement_ref": "worker_runtime/WKR-001.settlement.json",
  "result_summary": "Worker reached Done. Statuses: completion=accepted, review=clear, verification=passed. Done gate passed.",
  "result_ref": "worker_runtime/WKR-001.result.json",
  "completion_status": "accepted",
  "review_status": "clear",
  "verification_status": "passed",
  "reconcile_mode": "artifact_only",
  "reconcile_status": "recorded",
  "reconcile_summary": "Recorded 1 isolated child change(s) without applying them because the worker role is artifact-only.",
  "reconcile_ref": "worker_runtime/WKR-001.reconcile.json",
  "requires_parent_action": true,
  "parent_action_type": "continue_child",
  "parent_action_options": ["worker_continue"],
  "parent_action_summary": "Worker WKR-001 approval APR-001 was approved. Parent should run worker continue to resume the child.",
  "continuation_count": 1,
  "last_continued_at": "2026-03-19T10:09:00Z",
  "last_reconciled_at": "2026-03-19T10:10:00Z",
  "created_at": "2026-03-19T10:07:00Z",
  "updated_at": "2026-03-19T10:10:00Z"
}
```

## 21. `worker_runtime/*.workspace.json`

```json
{
  "schema_version": 1,
  "worker_id": "WKR-001",
  "parent_task_id": "TASK-20260319-001",
  "child_task_id": "TASK-20260319-002",
  "requested_mode": "auto",
  "effective_mode": "git_worktree",
  "status": "released",
  "workspace_root": "/tmp/repo/.ngen-worker-workspaces/repo/WKR-001/repo",
  "repo_root": "/tmp/repo/.ngen-worker-workspaces/repo/WKR-001/repo",
  "baseline_ref": "worker_runtime/WKR-001.baseline.json",
  "reason": "Prepared a git worktree for the child workspace.",
  "created_at": "2026-03-19T10:07:00Z",
  "updated_at": "2026-03-19T10:10:00Z",
  "released_at": "2026-03-19T10:10:00Z",
  "release_summary": "Released isolated child workspace after accepted settlement."
}
```

## 22. `worker_runtime/*.baseline.json`

```json
{
  "schema_version": 1,
  "baseline_id": "WBL-001",
  "worker_id": "WKR-001",
  "parent_task_id": "TASK-20260319-001",
  "child_task_id": "TASK-20260319-002",
  "file_count": 2,
  "entries": [
    {
      "path": "README.md",
      "exists": true,
      "kind": "file",
      "sha256": "4d7c51..."
    },
    {
      "path": "docs/guide.md",
      "exists": true,
      "kind": "file",
      "sha256": "11b3a1..."
    }
  ],
  "created_at": "2026-03-19T10:07:00Z",
  "updated_at": "2026-03-19T10:07:00Z"
}
```

## 23. `worker_runtime/*.settlement.json`

```json
{
  "schema_version": 1,
  "settlement_id": "SET-001",
  "worker_id": "WKR-001",
  "parent_task_id": "TASK-20260319-001",
  "child_task_id": "TASK-20260319-002",
  "status": "accepted",
  "child_state": "Done",
  "completion_status": "accepted",
  "review_status": "clear",
  "verification_status": "passed",
  "summary": "Done gate passed.",
  "evidence_refs": [
    "../TASK-20260319-002/handoff.md",
    "../TASK-20260319-002/completion/latest.json",
    "../TASK-20260319-002/reviews/latest.json",
    "../TASK-20260319-002/verification/latest.json"
  ],
  "created_at": "2026-03-19T10:07:00Z",
  "updated_at": "2026-03-19T10:10:00Z",
  "settled_at": "2026-03-19T10:10:00Z"
}
```

## 24. `worker_runtime/*.result.json`

```json
{
  "schema_version": 1,
  "result_id": "WRES-001",
  "worker_id": "WKR-001",
  "parent_task_id": "TASK-20260319-001",
  "child_task_id": "TASK-20260319-002",
  "role": "reviewer",
  "objective": "Review the parent task output.",
  "child_state": "Done",
  "settlement_status": "accepted",
  "completion_status": "accepted",
  "completion_summary": "Done gate passed.",
  "review_status": "clear",
  "review_summary": "verification, criteria and handoff are sufficient for current foundation gate.",
  "verification_status": "passed",
  "verification_summary": "go test ./... passed",
  "handoff_ref": "../TASK-20260319-002/handoff.md",
  "completion_ref": "../TASK-20260319-002/completion/latest.json",
  "review_ref": "../TASK-20260319-002/reviews/latest.json",
  "verification_ref": "../TASK-20260319-002/verification/latest.json",
  "criteria_ref": "../TASK-20260319-002/criteria/latest.json",
  "evidence_score": 100,
  "evidence_grade": "complete",
  "verified": true,
  "review_clear": true,
  "handoff_present": true,
  "criteria_closed": true,
  "settlement_accepted": true,
  "reconcile_clean": true,
  "trusted_for_parent_completion": true,
  "summary": "Worker reached Done. Statuses: completion=accepted, review=clear, verification=passed. Done gate passed.",
  "evidence_refs": [
    "../TASK-20260319-002/task.json",
    "../TASK-20260319-002/state.json",
    "worker_runtime/WKR-001.settlement.json",
    "../TASK-20260319-002/handoff.md",
    "../TASK-20260319-002/completion/latest.json",
    "../TASK-20260319-002/reviews/latest.json",
    "../TASK-20260319-002/verification/latest.json",
    "../TASK-20260319-002/criteria/latest.json"
  ],
  "created_at": "2026-03-19T10:07:00Z",
  "updated_at": "2026-03-19T10:10:00Z"
}
```

worker evidence score 是 artifact-completeness score，不是 LLM confidence。`trusted_for_parent_completion=true` 只在 child `Done`、completion accepted、verification passed、review clear、criteria ref present、settlement accepted、reconcile clean、无 parent action 与无 conflict 时成立；否则 `missing_evidence` 与 `evidence_grade=partial|weak|missing` 会暴露给 parent review / provider input。

blocked / awaiting-continue child 也使用同一 artifact，而不是退回去让 parent 重新拼 child state：

```json
{
  "schema_version": 1,
  "result_id": "WRES-002",
  "worker_id": "WKR-002",
  "parent_task_id": "TASK-20260319-010",
  "child_task_id": "TASK-20260319-011",
  "role": "reviewer",
  "objective": "Review the parent output.",
  "child_state": "Blocked",
  "settlement_status": "blocked",
  "blocked_reason_code": "blocked_missing_input",
  "blocked_detail_ref": "input_requests.jsonl#input_record_id=INPREC-001",
  "input_request_id": "INP-001",
  "input_request_ref": "input_requests.jsonl#input_record_id=INPREC-001",
  "input_field": "target_path",
  "input_prompt": "Provide target path",
  "requires_parent_action": true,
  "parent_action_type": "inspect_child",
  "parent_action_options": ["inspect_child"],
  "parent_action_summary": "Worker WKR-002 is blocked on missing input. Parent should inspect the child task and answer the input request directly.",
  "summary": "Worker is blocked (blocked_missing_input). Pending input request INP-001 for target_path: Provide target path. Worker WKR-002 is blocked on missing input. Parent should inspect the child task and answer the input request directly.",
  "evidence_refs": [
    "../TASK-20260319-011/task.json",
    "../TASK-20260319-011/state.json",
    "worker_runtime/WKR-002.settlement.json",
    "input_requests.jsonl#input_record_id=INPREC-001"
  ],
  "created_at": "2026-03-19T10:11:00Z",
  "updated_at": "2026-03-19T10:12:00Z"
}
```

用途说明：

- 这是 parent-facing 的 compiled child result，而不是再把整个 child task 镜像一遍；
- manager surfaces 应优先消费它来理解 child 当前 outcome，再按需展开到 child handoff / completion / review / verification 原始 artifacts；
- 当 child 被 approval / input 阻塞，或 approval 已通过但 parent 仍需 `worker_continue` 时，这个 artifact 现在也必须直接持有 blocker detail、approval/input refs 与 `parent_action_*`；
- parent success criteria 若显式要求 child compiled result、review clear、verification passed 或 `continue_child` readiness，当前也应该直接把这个 artifact 及其回链 refs 作为 evidence，而不是只回链 parent 自己的 `verification/latest.json`；
- `workers/*.json`、`worker_snapshot`、`session_snapshot` 与 provider input 当前都必须能稳定回链到这个 artifact。

## 25. `worker_runtime/*.reconcile.json`

```json
{
  "schema_version": 1,
  "reconcile_id": "REC-001",
  "worker_id": "WKR-001",
  "parent_task_id": "TASK-20260319-001",
  "child_task_id": "TASK-20260319-002",
  "role": "general_execution",
  "mode": "apply_on_accept",
  "status": "applied",
  "summary": "Applied 1 isolated child change(s) back into the parent workspace.",
  "settlement_status": "accepted",
  "settlement_settled_at": "2026-03-19T10:10:00Z",
  "change_count": 1,
  "applied_count": 1,
  "workspace_edit_ref": "workspace_edits.jsonl#edit_record_id=EDITREC-001",
  "evidence_refs": [
    "worker_runtime/WKR-001.workspace.json",
    "worker_runtime/WKR-001.baseline.json",
    "worker_runtime/WKR-001.settlement.json",
    "workspace_edits.jsonl#edit_record_id=EDITREC-001"
  ],
  "file_changes": [
    {
      "path": "README.md",
      "action": "update",
      "baseline_exists": true,
      "baseline_kind": "file",
      "baseline_sha256": "4d7c51...",
      "parent_exists": true,
      "parent_kind": "file",
      "parent_sha256": "4d7c51...",
      "child_exists": true,
      "child_kind": "file",
      "child_sha256": "9bf1c4...",
      "status": "applied",
      "summary": "Applied isolated child change back into the parent workspace."
    }
  ],
  "created_at": "2026-03-19T10:07:00Z",
  "updated_at": "2026-03-19T10:10:00Z",
  "reconciled_at": "2026-03-19T10:10:00Z",
  "applied_at": "2026-03-19T10:10:00Z"
}
```

补充约束：

- 当 parent success criteria 显式要求 child reconcile / workspace status 时，`criteria/latest.json` 必须优先回链 `worker_runtime/*.reconcile.json`、`worker_runtime/*.workspace.json` 与必要的 `workspace_edits.jsonl#edit_record_id=...`，而不是用 generic verification 代替；
- `artifact_only`、`applied`、`conflict`、`failed`、`noop` 与 `shared_workspace` 这些 reconcile statuses 都属于可被 parent criteria 显式消费的 runtime truth；
- `conflict`、缺 baseline 的 `failed`、以及 auto-apply `failed` 必须带 `parent_takeover_required=true`、`parent_takeover_summary` 与 `parent_takeover_refs`，保留 child workspace / baseline / reconcile refs 供 parent inspect 或 takeover。

## 26. `harness/latest.json` 与 `harness/history.jsonl`

```json
{
  "object_kind": "harness_evaluation",
  "schema_version": 1,
  "harness_eval_id": "HEVAL-001",
  "task_id": "TASK-20260319-001",
  "runtime_action": "auto",
  "provider_mode": "openai-response",
  "model": "gpt-5.4",
  "system_prompt_ref": "provider:openai-response:default_system_prompt",
  "decision_schema_version": "provider_decision.v1",
  "context_pack_ref": "context/latest-pack.json",
  "continuity_ref": "continuity/latest.json",
  "sprint_ref": "sprint/latest.json",
  "criteria_ref": "criteria/latest.json",
  "repair_budget": 3,
  "observation_command_budget": 2,
  "execution_command_budget": 1,
  "actions_selected": ["auto", "provider_decided", "verification_passed", "review_completed", "done"],
  "repair_attempt_count": 1,
  "verification_status": "passed",
  "criteria_met_count": 2,
  "criteria_open_count": 0,
  "review_status": "clear",
  "completion_status": "accepted",
  "workspace_edit_statuses": {
    "applied": 1
  },
  "worker_action_count": 0,
  "memory_promote_count": 1,
  "provider_usage_ref": "provider_usage.jsonl#usage_record_id=PUSE-001",
  "token_usage": "input_tokens=12000 output_tokens=900",
  "prompt_cache_usage": "cache_creation_input_tokens=2000 cache_read_input_tokens=8000",
  "summary": "Harness evaluation captured action=auto, state=Done, verification=passed, review=clear, completion=accepted.",
  "evidence_refs": [
    "task.json",
    "baseline.json",
    "context/latest-pack.json",
    "continuity/latest.json",
    "sprint/latest.json",
    "verification/latest.json",
    "reviews/latest.json",
    "completion/latest.json",
    "workspace_edits.jsonl#edit_record_id=EDITREC-001",
    "provider_usage.jsonl#usage_record_id=PUSE-001"
  ],
  "created_at": "2026-03-19T10:10:00Z"
}
```

`harness/latest.json` 是 task-local harness evaluation snapshot；`harness/history.jsonl` 是 append-only ledger。当前 `run`、`resume`、`auto` 与 `review` pass 成功收敛到任一 runtime state 后都必须写入一条记录，包括 `Done`、`Blocked` 与 `Failed`。它记录的是 harness strategy 与结果证据，不保存 API key、完整 provider payload 或未脱敏 hidden prompt 文本。

用途说明：

- 对比 provider/model/prompt/context/repair budget 变化时，优先读 `harness/history.jsonl`，不要只依赖 transient terminal log；
- `system_prompt_ref` 只能引用配置或内建 prompt 名称，不应落完整 prompt 文本；
- verifier、review、completion、criteria、context、sprint、workspace edit、command run、worker 与 memory 结果都通过 refs 回链到各自 owner artifact；
- provider usage 只保存 provider 返回的 token/cache/cost 摘要；如果 provider 不返回 usage，写 `unknown`，不能把 unknown 伪装成 `0`；
- `ngen harness eval TASK-ID --json` 读取最新 snapshot，适合 headless harness 和 real-task matrix 汇总。

## 26A. `provider_usage.jsonl`

`provider_usage.jsonl` 是 task-local append-only provider telemetry ledger。它记录 successful provider response decode 之后可见的 usage summary，不保存 raw provider payload、API key、完整 hidden prompt、tool input JSON 或 response body。

```json
{
  "object_kind": "provider_usage",
  "schema_version": 1,
  "usage_record_id": "PUSE-001",
  "task_id": "TASK-20260319-001",
  "ts": "2026-03-19T10:09:58Z",
  "operation": "workspace_edit",
  "provider_mode": "anthropic",
  "model": "claude-cache-capable",
  "token_usage": "input_tokens=12000 output_tokens=900",
  "prompt_cache_usage": "cache_creation_input_tokens=2000 cache_read_input_tokens=8000",
  "cost": "unknown",
  "refs": [
    "verification/latest.json",
    "context/latest-pack.json",
    "continuity/latest.json",
    "sprint/latest.json"
  ]
}
```

当前 operation vocabulary 是 `decision|workspace_observation|workspace_edit|mission_validation`。Anthropic provider 会解析 response `usage` 中的 `input_tokens`、`output_tokens`、`cache_creation_input_tokens` 与 `cache_read_input_tokens`；provider 没有暴露 usage 时，ledger 写 `unknown`。Harness snapshots、workspace edit records、model-backed mission validation runs 和 mission metrics 可以引用该 ledger，但普通 provider decision loop 不应通过新增 events 把 telemetry 注入下一轮 provider-visible recent events。

## 27. `.ngen/missions/<mission_id>/...`

当前 mission lane 是 workspace-level artifact，不放在某个 task 目录下：

```text
.ngen/missions/<mission_id>/mission.json
.ngen/missions/<mission_id>/validation_contract.json
.ngen/missions/<mission_id>/features.json
.ngen/missions/<mission_id>/milestones.json
.ngen/missions/<mission_id>/validation_runs.jsonl
.ngen/missions/<mission_id>/metrics.jsonl
.ngen/missions/<mission_id>/notes.md
```

`mission.json` 绑定 root task、当前 milestone、validation contract ref、latest validation ref、mission status、plan approval fields 与 additive `role_plan`。`plan_approval_status` 默认为 `pending`；`mission approve` 成功后写入 `plan_approval_status=approved`、`plan_approved_at`、`plan_approved_by=operator` 与当前 `validation_contract.json#contract_id=...` 形式的 `plan_approved_contract_ref`，并清空当前 `latest_validation_ref`，避免旧 blocking plan-gate run 在 read model 中继续伪装成当前阻塞。后续 `mission run` / `mission validate` 必须确认批准引用仍匹配当前 contract，避免 contract 被重写后复用过期批准。`role_plan` 是 mission creation / objective update 时的 snapshot，按 `orchestrator`、`workers`、`validators` 记录 effective `model`、`source=mission.role_models|provider.model|empty` 与 `explicit`；既有 mission 不会因为 `ngen.json` 后续变化而被静默改写。

`validation_contract.json` 冻结 behavioral requirements、acceptance tests、`assertions`、non-goals、allowed waivers 与 evidence requirements。`assertions` 是 stable assertion ledger：每条记录至少包含 `assertion_id=ASSERT-...`、`kind`、`statement`、`evidence_required` 与 `validator`。`features.json` / `milestones.json` 保存 feature/milestone records、task/worker bindings、contract coverage、status、validation refs 与 validator finding 派生出的 fix-feature refs；新写入的 `contract_coverage` 必须优先引用 assertion ids。`milestones.json` 还保存 additive `current_feature_id`、`ready_feature_ids` 与 `blocked_feature_ids`，作为 mission serial feature scheduler 的 read/write pointer。旧 artifact 中用自然语言 acceptance test 表达 coverage 时，runtime 可以兼容读取，但新的 approval gate 以 assertion id 作为规范写法。validator finding 派生的 fix-feature candidate 会保留 validation run / finding evidence refs；只有 finding 明确指向 `ASSERT-*` 时才写 `contract_coverage`；model / semantic blocking finding 还会写入 `mission_fix_scoped` event，并在 root task 非 terminal 时用 `task patch` 把 follow-up step 收进 root execution plan。plan gate、missing root evidence、open criteria、missing completion、missing assertion evidence 这类 deterministic precondition blocker 只保留诊断，不制造伪 fix feature。`validation_runs.jsonl` 是 append-only independent validator ledger，每条 run 必须包含 `validation_run_id`、`status=passed|blocking`、findings、evidence refs、summary，以及 validator provenance：`validator_role`、`validator_kind=deterministic_plan_approval|deterministic_plan_gate|deterministic_artifact|model_validator`、`validator_model`、`validator_model_source`、`validator_model_explicit`、`validator_context_refs`。`metrics.jsonl` 是 append-only mission metrics ledger；当前写入 wall time、validator time、role model snapshot、provider call counts、task/worker/repair/validation run counts，以及 provider 未返回时显式为 `unknown` 的 token/cache/cost fields。

当前 active slice 的 validator 总是先跑 deterministic gates：plan gate 要求 mission 已批准、批准引用匹配当前 contract，且所有 assertions 都至少被一个 feature 和一个 milestone 覆盖；artifact validator 再读取 root task 的 `state.json`、`criteria/latest.json`、`completion/latest.json` 与可用的 `harness/latest.json`，并要求每个已覆盖 assertion 至少能回链 root task、worker、verifier、review、completion 或 validation evidence ref。只有 deterministic gates 通过且 `role_plan.validators.explicit=true` 时，才会运行 model-backed read-only validator；该 validator 使用 dedicated validation schema，只能产出 status/summary/findings，不能请求 workspace edits、repair commands、provider decision actions、`task_create` 或 worker creation。model-backed validation run 可以写 `provider_usage_ref`、`token_usage` 与 `prompt_cache_usage`，这些字段只能来自 provider response metadata，不能来自 validator tool JSON。未配置 browser/GUI/computer-use tool plane 时，user-testing validator 必须以 non-blocking skipped finding 进入 validation run，不能静默消失。root mission `Done` 需要 approved validation contract、完整 assertion coverage、assertion-level closing evidence、root task completion accepted 与 latest validation run passed；不能只靠一个 verifier command 或执行者 prose 关闭 mission。绑定到 mission 的 task 在 narrative sync 后还必须把 mission refs 纳入 `context/latest-pack.json` / `context/summary.md`，并在 provider decision input 的 `mission` 字段中暴露当前 mission role plan、contract、feature/milestone records 与 latest validation findings。`mission status --json` 额外派生 `mission_status_snapshot`，集中返回 status、current feature、ready/blocked feature ids、root task state、active task/worker ids、latest validation status/blocking count、unresolved fix features、recent mission events 与 metrics summary；task narrative 的 Mission section 也渲染同一类紧凑 metrics 摘要。

## 28. `memory/entries.jsonl`

```json
{
  "schema_version": 1,
  "entry_id": "MEM-001",
  "task_id": "TASK-20260319-001",
  "kind": "task_completion",
  "source": "runtime",
  "scope": "task",
  "profiles": ["coding"],
  "provider_modes": ["builtin"],
  "confidence": "validated",
  "freshness_status": "fresh",
  "last_validated_ref": "completion/latest.json",
  "summary": "[coding] retry hardening: Done gate passed.",
  "refs": [
    "handoff.md",
    "completion/latest.json"
  ],
  "created_at": "2026-03-19T10:10:00Z"
}
```

`entries.jsonl` 是 append-only memory ledger。除 `Done` 后的自动 completion promote 外，当前 operator `memory promote`、ACP `memory.promote` 与 provider `memory_promote` 也会在 active task 中追加 entry；最新 compacted workspace memory 则单独刷新到 `memory/MEMORY.md`。当前 entry schema additive 冻结 `scope`、path-scoped `paths`、`profiles`、`provider_modes`、`confidence`、`freshness_status` 与 `last_validated_ref`；`MEMORY.md` label 会渲染 freshness，且带 workspace path 的 entry 在 path 消失后标记为 stale。
