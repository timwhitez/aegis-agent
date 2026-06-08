# 用户、能力与关键流程

## 1. 当前目标用户

### Solo Builder

需要一个能保留 task truth、verification、review 与 handoff 的本地 coding runtime。

### Harness Engineer / Platform Engineer

需要一个可审计、可恢复、artifact-first 的最小内核，为后续自治 agent 产品打底。

### Product / Tech Lead

需要从 `.ngen/` 与 `status --json` 直接看懂任务状态、证据和 blocker，而不是读长聊天记录。

## 2. 当前 JTBD

- JTBD-1 完成一个可验证的代码改动
- JTBD-2 对 docs-only 任务保留 review 和 handoff 闭环
- JTBD-3 在等待外部条件时 durable 地挂起并恢复
- JTBD-4 为跨人/跨时间交接留下可读 operating record

## 3. 当前核心产品能力

当前 active contract 冻结以下能力：

- C1 Task Kernel
  - 显式状态机管理 `Explore -> Plan -> Execute -> Verify -> Review`
- C2 Artifact Truth
  - `.ngen/` 保存 task、verification、review、completion、watch 与 approval 真相
- C3 Verification And Review
  - verifier、review、done gate 决定是否能宣称完成
- C4 Operator Surface
  - CLI、`--json` headless output、ACP stdio、interactive terminal 与 full-screen TUI
- C5 Waiting And Resume
  - `watch` + `scheduler tick --once`
- C6 Provider Dispatch
  - provider-driven `auto` runtime pass
- C7 Coordination
  - session bridge、bounded workers、hooks、workspace memory
- C8 Extended Profiles And Policy
  - `security_review`、`reviewer`、`yolo`
- C9 Mission Validation
  - `mission create/run/validate/status` 将大任务收口为 validation contract、feature/milestone records、root task binding 与独立 validation run

## 4. 当前关键流程

### Flow A: Coding Task

1. operator 提供 objective、criteria 与 constraints。
2. runtime 创建 `task.json`、`plan.json`、`state.json`、`criteria/latest.json`、`sprint/latest.json`、`progress.md` 与初始 checkpoint。
3. `run` / `resume` 读取 workspace truth，写出 `baseline.json`。
4. runtime 执行 foundation verifier。
5. runtime 写出 `verification/latest.json`、`reviews/latest.json`、`handoff.md` 与 `completion/latest.json`。
6. 若 done gate 通过，任务进入 `Done`；否则进入 `Failed` 或 `Blocked/blocked_review`。

### Flow B: Docs-Lite Task

1. operator 创建 `general_execution/docs_lite` 任务。
2. runtime 写 baseline，并运行 docs structural review。
3. runtime 仍然要生成 review、handoff 与 completion artifacts。
4. 只有 review 无阻塞时，任务才进入 `Done`。

### Flow C: Watch Task

1. operator 执行 `watch set`。
2. runtime 写入 `.ngen/watches/<watch_id>.json`，并把任务收敛到 `Waiting/waiting_watch`。
3. `scheduler tick --once` 扫描 due watch。
4. 命中到期项后，runtime 恢复同一 task，重新走 `resume` 路径。

### Flow D: Approval Lifecycle

1. operator 执行 `approval request`。
2. runtime 追加 `approvals.jsonl`，并把任务收敛到 `Blocked/blocked_policy`。
3. operator 执行 `approve` 或 `deny`。
4. runtime 追加 approval decision，并同步更新 `state.json`。

### Flow E: Manual Review Recheck

1. runtime 或 operator 触发 `review`。
2. review 根据 verification、criteria 与 handoff 重新评估当前 claim。
3. runtime 必须同步刷新 `completion/latest.json`，使其反映最新 gate verdict，而不是保留旧结论。
4. 若发现 blocker，必须写 `reviews/latest.json`、追加 `findings.jsonl`，并把任务收敛到 `Blocked/blocked_review`。
5. 若 review 清除 blocker，且 completion gate 重新满足，则任务可恢复到 `Done`。

### Flow F: Mission Validation Loop

1. operator 执行 `mission create`，runtime 创建或绑定 root task，并写出 mission contract、stable assertions、features、milestones 与 notes。
2. operator 审查 contract 后执行 `mission approve`；runtime 先校验每条 assertion 都被 feature 与 milestone 覆盖，成功后写入匹配当前 contract 的 approved contract ref。
3. `mission run` 先执行 deterministic plan gate；未批准、批准引用不匹配当前 contract，或 coverage 不完整时只写 blocking validation run，不进入 provider。gate 通过后，runtime 按 `mission.json.role_plan.orchestrator` 的 effective model 执行一轮 bounded provider-decision orchestration，再执行 mission validator；mission-scope `task_create` 只允许创建 lineage-bound mission child task，并绑定回当前可执行 feature，单次 mission orchestration pass 创建一个后停止。
4. `mission validate` 先读取 root task artifacts 与 mission contract 执行 deterministic gate；若 gate 通过且 `role_plan.validators.explicit=true`，再通过 dedicated read-only schema 调用 model validator，最终写入 `validation_runs.jsonl`。
5. 若 approval / validator 发现未批准 plan、批准引用不匹配当前 contract、缺 coverage、缺失证据、open criteria、未 accepted completion 或显式 model validator blocking finding，mission 进入 `blocked_contract_coverage` / `blocked_plan_gate` / `blocked_validation`；否则 mission 进入 `done`。
6. terminal/TUI 中 `/missions` 只打开或创建当前 task 的 mission state，不把 TUI 变成任务管理控制台。

## 5. 当前新增流程

- provider-driven `auto` loop
- ACP initialize/ping + session start/list/read/prompt/cancel
- interactive terminal slash-driven control
- full-screen TUI with task picker / transcript / inspector / focused approval-input-worker controls
- manager + bounded worker contracts
- hooks / visibility / memory promotion
- mission validation contract vertical slice
- `security_review` 与 `reviewer` profiles
