# 故障分类、operator commands 与最低可用门槛

## 1. 当前状态码

### `blocked_missing_input`

缺少人类补充的信息、路径、环境值或业务决策。

### `blocked_policy`

当前动作需要 approval，或 approval 已被拒绝而任务仍需同一动作。

### `blocked_review`

verifier 已运行，但 review / criteria / handoff / done gate 仍阻塞完成。

当前 `reviews/latest.json.blocking_categories` 会进一步解释 blocker，active categories 包括 `missing_evidence`、`scope_drift`、`stale_context_risk`、`worker_trust_gap` 等；worker-backed criteria 若缺少 accepted result / settlement / reconcile truth，也归入 `blocked_review`，而不是被 generic verifier pass 覆盖。`diagnostics/quality-latest.json.block_completion=true` 的 quality finding 也通过 review gate 进入 `blocked_review`。

### `blocked_validation`

mission validator 发现 root task 尚未 `Done`、criteria 未闭合、completion 未 accepted、关键 artifact 缺失，或显式启用的 model validator 返回 blocking finding。该状态属于 mission layer，不替代 root task 的 `phase/state`。deterministic artifact validator 阻塞时必须短路 model validator；model validator 阻塞时必须记录 `validator_kind=model_validator`、effective validator model、context refs 与 evidence-backed findings。

mission orchestration 中若 provider 返回 `task_create`，runtime 必须注入 mission root lineage，并把 child task 绑定回当前 mission feature；若没有可绑定 feature，则必须返回确定性诊断。单次 mission orchestration pass 在创建一个 child task 后停止，避免偷偷 fan-out 多个 durable tasks。

### `waiting_watch`

任务进入 watch 等待。

### `failed_verification`

当前 profile verifier 未通过。

补充边界：

- verifier command timeout 也必须归类到 `failed_verification`，不能把 runtime 整体拖成无响应。

### `failed_state`

durable state 不可安全恢复。

### `aborted_user`

operator 主动终止。

## 2. 当前 operator commands

- `task create`
- `mission create`
- `mission approve`
- `mission run`
- `mission validate`
- `mission status`
- `run`
- `resume`
- `status`
- `review`
- `events tail`
- `handoff export`
- `watch set`
- `watch ls`
- `watch cancel`
- `scheduler tick --once`
- `approval request`
- `approve`
- `deny`
- `input request`
- `input ls`
- `input respond`

## 3. 当前最低可用门槛

代码只有满足以下条件，才算当前阶段完成：

- `task create -> run -> status` 闭环可用
- `baseline.json`、`verification/latest.json`、`reviews/latest.json`、`completion/latest.json` 能被真实写出
- `Done` 在缺 handoff、缺 verifier 或 review 阻塞时会被拒绝
- verifier 失败进入 `Failed/failed_verification`
- 阻塞型 verifier command 会在 timeout guard 后显式失败，而不是无限挂起
- `coding` repair loop 产生的 observation command 与 repair command 都会留下 `command_runs.jsonl` durable truth；repair command 失败不会被吞掉成“模型想了但没做成”；stdout/stderr 命中 capture cap 时必须写出 bounded artifact、`stdout_truncated` / `stderr_truncated` metadata 和 truncation summary
- review blocker 进入 `Blocked/blocked_review`
- input request blocker 进入 `Blocked/blocked_missing_input`
- watch + scheduler tick 能把 `Waiting` task 再次唤醒
- `status --json` 能仅凭 artifacts 解释当前状态
- `mission create -> mission approve -> mission validate -> mission run` 能写出 role plan、stable assertion contract、plan approval ref、blocking validation run 与 passed validation run；未 approve、批准引用不匹配当前 contract，或 coverage 不完整时 `mission run` 必须先阻塞；显式 validators model 时还必须能写出 model-validator provenance 与 read-only findings
