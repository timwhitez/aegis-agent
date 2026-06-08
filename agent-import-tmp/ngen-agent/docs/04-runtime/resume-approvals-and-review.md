# 恢复、审批、review 与终止

## 1. 当前 resume 语义

当前 resume 承诺以下恢复路径：

1. 读取 `task.json` 与 `state.json`
2. 如 `state.json` 可读，则按当前 durable state 继续
3. 如 `state.json` 不可读但 `task.json` 仍可读，则写 `diagnostics/*.json` 并把任务收敛到 `Failed/failed_state`
4. 如 task 处于 `Blocked/blocked_policy`，则 `resume` 不越权自动继续
5. 如 task 处于 `Waiting/waiting_watch`，则等待 scheduler 唤醒后再继续
6. 对 parent-owned worker approval，parent 不直接越权改 child durable truth；但当 approval 已被 parent 决定后，允许通过 `worker continue` 走唯一的 parent-side continuation path
7. `status --json.restore_clues` 与 handoff Resume Instructions 必须从最新 checkpoint / baseline command hints 暴露恢复 bearings
8. command / workspace edit side effects 必须写 `replay_safety`；非 `safe_to_replay` 的同 argv repair command 不得被自动重放
9. worker reconcile conflict / failed auto-apply 必须保留 parent-takeover refs，而不是覆盖或丢弃 child workspace truth

## 2. 当前审批语义

Foundation v0.1 当前只实现最小 approval lifecycle：

- `approval request`
- `approve`
- `deny`

当前 approval record 必须至少保留：

- `approval_record_id`
- `approval_id`
- `task_id`
- `owner_task_id`（worker child approval 可选）
- `owner_worker_id`（worker child approval 可选）
- `kind`
- `status`
- `scope`
- `reason`
- `ts`

当前约束：

- approval durable truth 始终落在发起 task 的 `approvals.jsonl`
- 若 approval 由 worker child task 发起，则允许通过 `owner_task_id` + `owner_worker_id` 把 operator surface 归属回 parent worker contract
- parent surface 可以列出和决策 owned child approvals，但不会重写 child approval history
- parent provider / session context 也必须看到 owned child pending approvals，使 manager loop、ACP `session.prompt` 与 terminal 不必先做额外查询
- `worker sync` 必须把 child blocker、approval ref 和 parent 应采取的下一步动作显式暴露出来
- approval 已通过且 child 回到 `Active` 后，parent 继续 child 的唯一 helper 是 `worker continue`
- 当前仍不冻结 expiry、多 actor 审批、waiver、override、任意深度 parent/child approval tree

## 3. 当前 input request 语义

Foundation v0.1+ 当前实现最小 structured input lifecycle：

- `input request`
- `input ls`
- `input respond`

当前 input request record 必须至少保留：

- `input_record_id`
- `request_id`
- `task_id`
- `kind`
- `status`
- `field`
- `prompt`
- `response`
- `required`
- `ts`

当前约束：

- 同一 task 同时只允许一个 pending input request
- pending input request 必须把 task 收敛到 `Blocked/blocked_missing_input`
- answered input response 必须追加到同一 `input_requests.jsonl`

## 4. 当前 review lane

review 当前负责：

- 检查 verifier 是否通过
- 检查 `handoff.md` 是否存在
- 检查 `criteria/latest.json` 是否完整带 evidence refs
- 阻塞 unsupported completion claim
- 刷新 `completion/latest.json`，使其始终代表最近一次 gate verdict

如果 review 发现 blocker，runtime 必须：

- 写 `reviews/latest.json`
- 追加 `findings.jsonl`
- 把 task 收敛到 `Blocked/blocked_review`

如果 `task.review`、ACP `task.review`、provider `review` 或 `session.prompt` 在 verifier 尚未产出 `verification/latest.json` 前就触发 review，runtime 仍必须把这个缺口显式收敛成 blocking review/completion truth，而不是把底层文件缺失直接冒泡成原始 runtime 错误。

如果 verifier artifacts 已存在，但 `handoff.md` 因漂移、误删或旧工件缺失而不存在，review 当前必须先基于最新 verification / criteria / plan truth 重建 `handoff.md`，再继续做 gate 判定；缺失 handoff 不能变成一个永久自锁的恢复死结。只有当 verifier 或 criteria truth 本身仍未闭合时，review 才继续收敛到 `Blocked/blocked_review`。

如果 review 清除 blocker 且 completion gate 重新满足，runtime 必须同步把 task 收敛回 `Done`，而不是保留旧的 rejected verdict。

## 4A. 当前 mission validation lane

mission validation 是 root task 外层的独立 evidence gate：

- `mission validate` 必须读取 root task `state.json`、`criteria/latest.json`、`completion/latest.json` 与可用的 `harness/latest.json`；
- root task 未 `Done`、criteria 仍 open、completion 未 accepted 或关键 artifact 缺失时，mission 必须写入 blocking validation run；
- validator finding 必须带 evidence refs 与 recommended action；
- deterministic artifact validator 是强制前置 gate；它阻塞时，即使 `role_plan.validators.explicit=true`，也不得启动 model validator；
- `role_plan.validators.explicit=true` 且 deterministic pass 通过时，model validator 只能读取 mission/root-task/worker evidence refs，输出 findings 与 recommended follow-up，不能直接修改 workspace、请求 repair command、`task_create` 或 `worker_spawn`；
- blocking validator finding 可以追加成 `features.json` 中的 pending fix-feature candidate，并回链到对应 validation run/finding；orchestrator 是唯一能把这些 finding 转成执行工作的角色；
- validator 不应直接修改 workspace 文件，修复应回到 root task、fix feature task 或 worker contract；
- root mission closure 需要 latest validation run `passed`，不能只靠 verifier passed。

## 5. 当前 stop conditions

runtime 当前必须能明确收敛到以下终态或阻塞态：

- `Done`
- `Failed/failed_verification`
- `Failed/failed_state`
- `Blocked/blocked_policy`
- `Blocked/blocked_review`
- mission `blocked_validation`
- `Waiting/waiting_watch`
- `Aborted/aborted_user`

## 6. future reference

以下主题仍是 post-foundation 路线，不属于当前 active contract：

- arbitrary multi-level approval ownership beyond current parent-owned worker approval / continue contract
- long-lived ACP push subscription / multi-client fanout beyond `task.events` replay-after-cursor
- richer replay reconciliation protocol beyond current side-effect annotations, checkpoint restore clues, and worker parent-takeover records
