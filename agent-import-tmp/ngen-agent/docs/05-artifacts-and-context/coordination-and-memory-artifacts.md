# 协作、memory 与验证工件

## 1. 当前 active coordination artifacts

当前实现冻结以下 coordination-related artifacts：

### `tasks/TASK-001/input_requests.jsonl`

```json
{
  "schema_version": 1,
  "input_record_id": "INPREC-001",
  "request_id": "INP-001",
  "task_id": "TASK-001",
  "ts": "2026-03-19T10:05:30Z",
  "kind": "input_request",
  "status": "pending",
  "field": "target_path",
  "prompt": "Provide the target path.",
  "required": true
}
```

约束：

- 同一 task 同时只允许一个 pending input request
- `blocked_missing_input` 的 `status_detail_ref` 必须回链到对应 JSONL item
- `input respond` 只能追加 answered record，不得重写旧 request

### `watches/WATCH-001.json`

```json
{
  "schema_version": 1,
  "watch_id": "WATCH-001",
  "task_id": "TASK-001",
  "status": "active",
  "interval_seconds": 300,
  "reason": "Wait for the next verification window.",
  "next_wake_at": "2026-03-19T10:10:00Z",
  "created_at": "2026-03-19T10:05:00Z",
  "updated_at": "2026-03-19T10:05:00Z"
}
```

约束：

- foundation v0.1 同一 task 只允许一个 active watch
- 被替换或取消的 watch 必须保留 artifact，并更新 `status`
- due watch 由 `scheduler tick --once` 扫描并触发

### `missions/MIS-001/validation_runs.jsonl`

```json
{
  "object_kind": "mission_validation_run",
  "schema_version": 1,
  "validation_run_id": "MVAL-001",
  "mission_id": "MIS-001",
  "milestone_id": "MS-001",
  "root_task_id": "TASK-001",
  "validator_role": "validators",
  "validator_kind": "deterministic_artifact",
  "validator_model": "",
  "validator_model_source": "provider.model",
  "validator_model_explicit": false,
  "validator_context_refs": ["mission.json", "validation_contract.json", "workspace:.ngen/tasks/TASK-001/state.json"],
  "status": "blocking",
  "summary": "Mission validation blocked by 1 finding(s).",
  "findings": [
    {
      "finding_id": "MFIND-001",
      "category": "missing_evidence",
      "severity": "high",
      "blocking": true,
      "summary": "Root task is Execute/Active, not Done."
    }
  ],
  "evidence_refs": ["mission.json", "validation_contract.json", "workspace:.ngen/tasks/TASK-001/state.json"],
  "created_at": "2026-03-19T10:10:00Z"
}
```

约束：

- mission validator 是独立 artifact lane，不信任 executor prose；
- deterministic artifact validator 是最低门槛；model validator 只有在 `role_plan.validators.explicit=true` 且 deterministic pass 已通过时才运行；
- validation findings 可作为后续 fix feature 的来源，但 validator 不应静默承担 broad write-capable implementation，不能请求 workspace edit、repair command、`task_create` 或 worker creation；
- orchestrator 才能把 validator finding 转成执行 work；worker 只消费明确 feature/milestone slice 与 success criteria；
- mission decisions、validator findings 与 reusable milestone 只有在具备 evidence refs 后才应通过 workspace memory promote 进入长期 memory。

### `context/latest-pack.json`

```json
{
  "schema_version": 1,
  "task_id": "TASK-001",
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
    "workspace:.ngen/project/project.json",
    "state.json",
    "baseline.json",
    "verification/latest.json",
    "reviews/latest.json"
  ],
  "included_refs": [
    "task.json",
    "plan.json",
    "state.json",
    "baseline.json",
    "verification/latest.json",
    "reviews/latest.json",
    "context/summary.md"
  ],
  "sections": [
    {
      "name": "task",
      "token_budget": 900,
      "actual_tokens": 180,
      "refs": ["task.json", "baseline.json", "criteria/latest.json"]
    },
    {
      "name": "project",
      "token_budget": 700,
      "actual_tokens": 90,
      "refs": ["workspace:.ngen/project/project.json"]
    },
    {
      "name": "observations",
      "token_budget": 2200,
      "actual_tokens": 240,
      "refs": ["verification/latest.json", "reviews/latest.json"]
    },
    {
      "name": "memory_summary",
      "token_budget": 1000,
      "actual_tokens": 120,
      "refs": ["context/summary.md"]
    }
  ],
  "compaction": {
    "performed": true,
    "summary_ref": "context/summary.md"
  },
  "project_focus": {
    "primary_step_id": "phase.docs",
    "primary_branch_id": "branch.docs",
    "depends_on_step_ids": ["phase.repo_truth"],
    "unmet_dependency_step_ids": ["phase.repo_truth"],
    "dependencies_satisfied": false,
    "refs": ["workspace:.ngen/project/project.json"]
  },
  "status_reason_code": ""
}
```

约束：

- 这是当前 task 级 machine-readable continuity pack，不是完整 prompt transcript。
- `actual_tokens` 当前是 runtime 本地的启发式估算值，用于审计 section 规模，而不是精确 tokenizer 真相。
- provider input 当前会真实消费这个 pack，而不只是把它当展示工件。
- 若 task 已经绑定到 workspace project graph，`project_focus` 必须一起落盘；它表达当前 task 在 project graph 里的 primary step / branch、dependency boundary 与 project refs，而不是要求 provider 只靠全量 `project.json` 自己重建当前绑定。

### `context/summary.md`

```md
# Context Summary

## Task Focus
- Objective: keep the verifier green and close the review gate.
- Phase: Review
- State: Active
- Summary: Verifier is green; review and completion are the remaining gate.
- Next Step: Read the latest review, confirm criteria evidence, and finish the handoff.

## Latest Verification
- Status: passed

## Recent Repairs
- Workspace Edit [applied]: sync retry helper and config docs
```

约束：

- `context/summary.md` 是 task-local compaction summary，服务于 long-horizon continuity；它不是 workspace-level memory。
- `context/latest-pack.json` 与 `context/summary.md` 必须在同一 narrative sync pass 内一起更新。
- 当前 compaction summary 是 deterministic artifact rendering，不是隐藏 provider transcript compression。未来如果引入 model-backed 或 idle compaction，必须显式记录 provider usage、input refs、output refs、取消/失败状态与 resulting artifact refs；不得把 compression instruction 或 summary 写入 system prompt、hidden session state 或未记录的 provider-visible context。

### `sessions/SES-001.json`

```json
{
  "schema_version": 1,
  "session_id": "SES-001",
  "task_id": "TASK-001",
  "mode": "acp",
  "status": "active",
  "last_prompt": "/run",
  "last_action": "provider_decided",
  "created_at": "2026-03-19T10:05:00Z",
  "updated_at": "2026-03-19T10:06:00Z"
}
```

### `sessions/SES-001.messages.jsonl`

```json
{
  "schema_version": 1,
  "message_id": "MSG-001",
  "session_id": "SES-001",
  "task_id": "TASK-001",
  "role": "operator",
  "content": "/run",
  "ts": "2026-03-19T10:06:00Z"
}
{
  "schema_version": 1,
  "message_id": "MSG-002",
  "session_id": "SES-001",
  "task_id": "TASK-001",
  "role": "assistant",
  "content": "Hello. Tell me what to inspect, run, or change in the current task.",
  "ts": "2026-03-19T10:06:00Z"
}
{
  "schema_version": 1,
  "message_id": "MSG-003",
  "session_id": "SES-001",
  "task_id": "TASK-001",
  "role": "runtime",
  "content": "Done Review: Bootstrap plan completed.",
  "ts": "2026-03-19T10:06:01Z"
}
```

约束补充：

- `sessions/*.messages.jsonl` 不是 UI-only transcript；它现在也是 provider-visible session continuity truth。
- provider decision input、workspace observation prompt 与 workspace edit prompt 都必须显式带出 `session_messages_ref` 与 bounded `session_recent_messages`，让远端 provider 在多轮 terminal / ACP steer 下能够直接消费同一份 session transcript，而不是只看到 `session.last_prompt`。
- `session.prompt` 允许在普通 conversational prompt 下先追加 assistant-style direct reply，再追加 runtime summary；`session.cancel` 仍必须追加 runtime cancellation summary。failed task outcome 与 operator cancel 这类控制事实也必须显式落进 `*.messages.jsonl`。
- 若 task 已绑定 workspace project graph，则 provider-visible `continuity.current_focus.project_focus`、`sprint.project_focus` 与 `context_pack.project_focus` 也必须一起出现，让远端 provider 直接看到当前 bound step / branch / dependency truth，而不是从整张 project graph 重新猜。

### `tasks/TASK-001/workers/WKR-001.json`

```json
{
  "schema_version": 1,
  "worker_id": "WKR-001",
  "parent_task_id": "TASK-001",
  "child_task_id": "TASK-002",
  "role": "reviewer",
  "objective": "Review parent changes.",
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
  "handoff_ref": "",
  "created_at": "2026-03-19T10:07:00Z",
  "updated_at": "2026-03-19T10:07:00Z"
}
```

约束补充：

- `workers/*.json` 不只回显 child 的 blocker 与 reconcile truth，还必须提供 parent 可以直接消费的 compiled child result 摘要；
- `result_summary` / `result_ref` 用于把 child 的 completion / review / verification 结论压成一个稳定入口，避免 manager 只能靠多份 child artifact refs 临时重建结果；
- 当 child 被 approval / input 阻塞，或 approval 已通过但 parent 还未继续 child 时，`workers/*.json` 现在也必须直接暴露 approval/input detail 与 `parent_action_*`，而 `worker_runtime/*.result.json` 必须把同一份 blocker truth 冻结成 durable artifact；
- `completion_status`、`review_status` 与 `verification_status` 若已知，也必须在 worker contract 中直接暴露给 parent / session / provider surfaces。

### `memory/MEMORY.md`

```md
# Workspace Memory

## Recent Memory Entries
- 2026-03-19T10:08:00Z TASK-001 [task_completion/runtime/fresh]: [coding] baseline verifier hardening: Done gate passed.

## Consolidated Topics
- verifier (2)
```

### `memory/entries.jsonl`

```json
{
  "schema_version": 1,
  "entry_id": "MEM-001",
  "task_id": "TASK-001",
  "kind": "task_completion",
  "source": "runtime",
  "scope": "task",
  "profiles": ["coding"],
  "provider_modes": ["builtin"],
  "confidence": "validated",
  "freshness_status": "fresh",
  "last_validated_ref": "completion/latest.json",
  "summary": "[coding] baseline verifier hardening: Done gate passed.",
  "refs": ["handoff.md", "completion/latest.json"],
  "created_at": "2026-03-19T10:08:00Z"
}
```

补充约束：

- `entries.jsonl` 不再只承载 terminal completion summary；显式 `memory promote` / `memory.promote` / provider `memory_promote` 也会在 active task 中追加 entry。
- `kind` 当前冻结为 `task_completion`、`task_milestone`、`task_decision`、`task_blocker` 与 `task_note`。
- `source` 当前冻结为 `runtime`、`operator` 与 `provider`。
- entry metadata 当前会 additive 冻结 `scope`、`paths`、`profiles`、`provider_modes`、`confidence`、`freshness_status` 与 `last_validated_ref`；`stale_after`、`supersedes` 与 `superseded_by` 是 schema 字段，但当前 active runtime 还不主动计算 supersession。
- `paths` 只记录 workspace path-scoped refs，不记录 `.ngen/` 内部 artifact refs；这让 memory 可以表达“这条结论绑定到某个 repo path”，而不是把所有 task-local artifact 都当成 workspace ownership。
- `MEMORY.md` 是从 `entries.jsonl` 刷新的 compacted workspace memory 视图，不是 append-only ledger；append-only 真相仍是 `entries.jsonl`。
- Recent memory entry label 必须带 freshness，例如 `[task_decision/operator/fresh]` 或 `[task_note/operator/stale]`。若 entry 带 `paths` 且对应 workspace path 已不存在，刷新 `MEMORY.md` 时必须标记为 stale；provider-visible `workspace_memory` 也消费这份刷新后的 Markdown。

## 2. 当前 richer design

以下内容仍可继续加强，但不再是“尚未实现”的空白：

- richer role-file inheritance / discovery UX，超出当前 `.ngen/roles/<role_id>.json` 内建 contract 水合
- `.ngen/loops/*.json`
- deeper memory extraction、supersession/conflict adjudication、路径 ownership 变更判断、external-root policy，以及 reviewer 对 stale-memory 依赖的强制阻断
