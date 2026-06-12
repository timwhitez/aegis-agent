# 运行时生命周期与状态

## 1. 当前运行时模型

当前 runtime 是一个带显式 control primitives 的 operator-assisted task runtime。

这意味着：

- runtime 自己拥有 artifacts、state machine、verifier、review、handoff、done gate；
- 当前已经拥有 provider-driven `auto` loop；
- 对 `coding` task，`Execute` phase 现在也允许在 verifier 失败后执行最多 3 次 bounded workspace repair；当 verifier 已通过但 workspace-backed success criteria 仍未满足时，同一预算内也允许继续做 bounded repair。这里的 workspace-backed 既包括显式 path / glob criterion，也包括带 readme/docs/config 语义和具体 token 的 criterion。failed/noop repair attempt 与 failed repair command 不再默认把 task 提前打回人工 `resume`；它们会作为 durable failure evidence 留在同一预算内继续推进，并把 prior failure summaries 带入下一次 provider observation/edit prompt。Multica/headless `exec` 不在输入读取阶段合成特殊 task：stdin adapter 只把 user text blocks 作为 prompt 交给 runtime；`metadata`、`system_prompt`、AGENTS.md 和 `.agent_context` 只可能作为普通 workspace/context 事实被模型显式读取或使用，不能由 adapter 变成 quick-create、issue-read 或 delegation flow。当前 active 写路径已覆盖全部当前 provider mode：`builtin`、`command`、`openai-response`、`openai-comp`、`anthropic`。

## 2. phase 定义

- `Explore`
  - 创建任务后的初始阶段，负责采集 workspace truth 与可用 verifier。
- `Plan`
  - foundation plan 的冻结阶段。当前 `plan.json` 包含两层：system lane 至少覆盖 baseline、criteria-scoped work items 与最终 review/done gate；mutable execution lane 则记录 operator / provider 维护的长期 execution checklist。该 execution lane 现在支持 stable step id、`parent_step_id`、`depends_on`、`priority`、`revision`、`ready_execution_step_ids`、`blocked_execution_step_ids` 与 `last_mutation_ref`，并把显式 operator/provider 改写历史追加到 `plan_updates.jsonl`。当前显式改写分成两种：`task update` / `task_update` 负责全量 rewrite，`task patch` / `task_patch` 负责顺序 patch mutation；patch op 当前冻结为 `set_explanation`、`upsert_step` 与 `remove_step`。runtime 会按 artifacts 重写 system lane，同时保留 mutable execution graph 并计算 `current_system_step_id` / `current_execution_step_id`。若远端 provider 直接给出 `run` / `resume`，但 task 仍明显是多阶段且 execution lane 为空，runtime 也会先写入一个 system-sourced、one-criterion-at-a-time 的 bootstrap execution plan，再进入执行。与此同时，workspace 根下还有一个 singular `project/project.json`：它记录跨 task / child-task 的 orchestration graph、dependency edges 与 concurrent branches。`project update` / `project_update` 承担 full rewrite，`project patch` / `project_patch` 承担增量 graph mutation；当前 project patch 除了 `upsert_step` / `upsert_branch` 外，还冻结了 `set_step_dependencies`、`set_step_parent`、`bind_step_branch`、`bind_step_task`、`bind_branch_task` 与 `set_branch_status` 这些 edge-level ops。
- `Execute`
  - 进入执行窗口；对 `coding` task 也可以在 verifier failure 或 workspace-backed criteria gap 下生成并应用最多 3 次 bounded workspace repair。repair pass 可以包含只读 observation command、patch-first workspace edit，以及写前/写后的 bounded workspace repair command；当某次 workspace edit 变成 `failed` / `noop`，或 repair command 变成 `failed` 时，只要预算仍在，同一 runtime pass 仍应继续下一次 attempt。
- `Verify`
  - 运行当前 profile 的 verifier pipeline。
- `Review`
  - 基于 verifier、criteria 与 handoff 做结构化 review，并决定 done gate 是否放行。

## 3. state 定义

- `Active`
  - 当前处于任一活动 phase。
- `Blocked`
  - 当前无法继续推进。
- `Waiting`
  - 已写出 watch，等待未来时点或外部条件。
- `Done`
  - done gate 通过。
- `Failed`
  - verifier 失败或 durable state 不可安全恢复。
- `Aborted`
  - operator 主动终止。

## 4. 当前 `status_reason_code`

- `blocked_missing_input`
  - 缺少人类补充的信息、路径、环境值或业务决策。
- `blocked_policy`
  - approval pending、approval denied，或当前动作需要额外批准。
- `blocked_review`
  - verifier 已运行，但 review / criteria / handoff / done gate 仍阻塞完成。
- `waiting_watch`
  - 任务进入等待，由 watch artifact 拥有真相。
- `failed_verification`
  - verifier 未通过。
- `failed_state`
  - durable state 损坏或不一致。
- `aborted_user`
  - operator 终止任务。

补充边界：

- 审批不属于 `blocked_missing_input`。
- review blocker 也不属于 `blocked_missing_input`。
- `blocked_missing_input` 的 `status_detail_ref` 必须回链到 `input_requests.jsonl#input_record_id=...`。
- `blocked_policy` 的 `status_detail_ref` 必须回链到 `approvals.jsonl#approval_record_id=...`。
- `blocked_review` 的 `status_detail_ref` 必须回链到 `reviews/latest.json`。
- `waiting_watch` 的 `status_detail_ref` 必须回链到 `workspace:.ngen/watches/<watch_id>.json`。

## 5. 当前状态推进

Foundation v0.1 的标准推进顺序：

1. `task create`
   - 创建 `task.json`、`plan.json`、`state.json`、`criteria/latest.json`、`criteria/history.jsonl`、`sprint/latest.json`、`sprint/history.jsonl`、`progress.md` 与初始 checkpoint。
   - 初始状态固定为 `phase=Explore`、`state=Active`。
   - task discovery / `task list` / TUI picker 只允许发现已具备最小 core truth 的任务；至少在 `task.json` 与 `state.json` 同时 durable 后才能被列举，避免创建中的半成品被错误恢复成 `Failed/failed_state`。
2. `run` / `resume`
   - 若缺少 `baseline.json`，先写 baseline。
   - 更新 bootstrap plan。
   - 若 task 明显是多阶段且 execution lane 仍为空，runtime 在真正执行前会先基于 open criteria 写入一个 system-sourced、one-criterion-at-a-time 的 mutable execution bootstrap，并追加 `task_plan_updated` event。
   - 若 provider 在同一 auto loop 中先下发 `project_update` / `project_patch`，workspace project graph mutation 不得吞掉后续真实执行机会；同一 auto pass 仍要继续进入下一轮 decision 或真正的 `run` / `resume`。
   - 进入 `Execute`。
   - 运行 verifier，写 `verification/latest.json`。
   - 若 `coding` task 满足当前 provider 条件，可在 verifier failure 或 workspace-backed criteria gap 下追加 `workspace_edits.jsonl`、`command_runs.jsonl` 并重跑 verifier；当前默认最多 3 次 repair cycles。若某次 edit plan `failed` / `noop`，或某次 repair command `failed`，runtime 仍会在同一 pass 内继续下一次 bounded attempt，并把 prior failure summaries 注入后续 observation/edit prompt；同一 repair target 重复且没有新的 workspace progress、edit plan 违反 task constraints、或 repair command 超出 budget/timeout 时仍会提前停止。若 workspace-backed criteria 仍未闭合，但本轮 applied workspace edit 或 completed repair command 写出了可持久化的准备性证据，runtime 会在剩余预算内继续下一次 criteria repair，而不是立即进入 `Blocked/blocked_review`。Multica 相关命令没有输入读取器级别的 quick-create verifier 例外；它们必须作为普通 observation/repair command 留下 policy、stdout/stderr 与 replay-safety evidence，再由 verifier/review/done gate 消费。
   - 运行 review，写 `reviews/latest.json`。
   - 更新 `criteria/latest.json|history.jsonl`、`sprint/latest.json|history.jsonl`、`handoff.md` 与 `completion/latest.json`。
   - 若 gate 通过，转 `Done`；否则转 `Failed` 或 `Blocked/blocked_review`。
3. `watch set`
   - 转 `Waiting`，并写入 watch artifact。
4. `scheduler tick --once`
   - 发现 due watch 后，把 task 恢复到 `Active`，并走同一 `resume` 路径。
5. `approval request`
   - 追加 approval request，并把 task 收敛到 `Blocked/blocked_policy`。
6. `input request`
   - 追加 input request record，并把 task 收敛到 `Blocked/blocked_missing_input`。
7. `input respond`
   - 追加 answered record；若当前 blocker 来自该 input request，则把 task 恢复到 `Active`。

## 6. 归一化规则

- `state.json` 是 `phase/state` 的唯一 canonical owner。
- `plan.json` 不拥有 `phase`，但它现在拥有 system lane + mutable execution lane 的 current work breakdown；`state.json.current_step_id` 默认优先回指当前 execution step，若 execution lane 为空或 task 已 `Done`，则回指当前 system gate step。
- `plan_updates.jsonl` 是 mutable execution lane 的 append-only mutation history owner；`plan.json.revision` 与 `plan.json.last_mutation_ref` 必须回链到这份 history，而不是把 operator/provider planning truth 混进 events summary。当前 mutation record 既要保留 resulting execution snapshot，也要标明 mutation kind（replace / patch）；若 mutation kind 是 `patch`，还必须保留原始 patch operations。
- `.ngen/project/project.json` 不拥有任何单 task verifier/review/completion truth；它只拥有 workspace-level orchestration graph。`.ngen/project/project_updates.jsonl` 是 project graph 的 append-only mutation history owner；`project.json.revision`、`project.json.last_mutation_ref`、`ready_step_ids`、`blocked_step_ids` 与 `active_branch_ids` 必须回链到这份 history，而不是要求 operator/provider 从多个 task artifacts 重新拼装 project graph。
- `criteria/latest.json` 只拥有 task-local acceptance-ledger truth；`criteria/history.jsonl` 则拥有 append-only refresh history。
- `sprint/latest.json` 只拥有 task-local current-scope contract truth；`sprint/history.jsonl` 则拥有 append-only sprint refresh history。
- `completion/latest.json` 只记录最近一次 gate verdict，不替代 `state.json`。
- `reviews/latest.json` 是 `blocked_review` 的 detail owner。
- `handoff.md` 在 `Done` 前必须存在。
