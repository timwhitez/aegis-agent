# Runtime Budget And Agent Lifecycle Audit Issues

审计目标：`9cbb8c8672d38b524dfa00cff66705c8816c5b9c`（`feat(runtime): unify global turn guards and child budgets`）。

审计结论：当前方案的总体分层和大部分预算语义已经达到较高水平，包括全局 per-run turn guard、child 三维预算、effective policy 快照、显式 extension/resume、durable cancel request、`cancelled` 与 `failed` 分离、shell 进程组取消和 queue claim lock。但当前实现仍有以下可复现缺陷，因此不能判定为“已达到或超越行业最佳实践且不存在可见 bug”。

已有基线验证仍然通过：

- `go test ./internal/runtime ./internal/session ./internal/tools ./internal/hooks -count=1 -timeout=300s`
- `CGO_ENABLED=1 go test -race ./internal/runtime ./internal/session -count=1 -timeout=300s`

前五项行为缺陷已通过审计期间的临时负向回归测试复现；`BUD-006` 由 active-runtime 全部持久化入口和 crash 边界的代码级审计确认。临时测试文件已删除，未污染当前工作树；每项验收标准都要求把对应场景补成永久测试。

## BUD-001 — Absolute child deadline can be misclassified as a provider timeout and fail the job

- Severity: P1
- Confidence: High
- Status: Resolved

### Evidence

- `spec/15-background-queue.md:264-265` 要求 child budget、global guard 和 operation timeout 同时存在时，由最先到达的边界决定，并准确记录 `child_budget_absolute_deadline_exceeded`。
- `internal/runtime/budget.go:138-142` 只有当返回错误本身满足 `errors.Is(err, context.DeadlineExceeded)` 时，才把它识别为 context cancellation。
- `internal/provider/types.go:156-164` 在 provider request context 到期时把 `context.DeadlineExceeded` 转换成不保留 cause/unwrap 的 `*HTTPError{Class: "upstream_timeout"}`。
- `internal/runtime/engine.go:488-545` 因此可能跳过 child budget cause 分支，并把 session 写成 provider failure。
- 临时回归使用真实阻塞 HTTP provider、`max_elapsed_sec=1`、provider transport retry `max_attempts=1`、`provider_auto_resume.enabled=false`。实际结果为 queue `failed`、child `failed`、`LastError="openai: context deadline exceeded"`；期望结果应为 queue `blocked`、child `paused`、`PauseReason="child_budget_absolute_deadline_exceeded"`。

### Why it matters

同一个 absolute child deadline 是否正确生效，取决于 provider retry/auto-resume 的旁路配置。关闭 retry 或 auto-resume 后，原本可由 parent extension/resume 的预算暂停会变成 execution failure，并污染失败统计、告警和 parent 决策。这破坏了“最先到达边界决定终态”和 cancellation-cause fidelity。

### Root cause

provider transport 层把父 context 的 deadline 归一化成普通 `upstream_timeout` 后丢失了原始 cancellation cause；runtime 又要求错误值本身可被 `errors.Is` 识别，未优先检查已经结束的 run context 及其 `context.Cause`。

### Acceptance criteria

- 当 run context 已结束时，runtime 必须先按 `context.Cause(ctx)` 解析 child budget、parent cancel、manual interrupt 或 steer interrupt，再考虑 provider timeout 分类。
- provider-owned request timeout 仍必须保持 `upstream_timeout`，不能被误写成 child budget pause。
- child absolute deadline 触发时，无论 provider transport retry 和 runtime auto-resume 是否启用，都必须得到相同的 `paused` / `blocked` 结果及 `child_budget_absolute_deadline_exceeded` reason。
- 不得为 child budget cancellation 写入误导性的 provider auto-resume attempt/reminder。
- 永久测试至少覆盖：OpenAI-compatible 真实 HTTP 阻塞、transport retry on/off、runtime auto-resume on/off、operation timeout 早于 child deadline、child deadline 早于 operation timeout。

### Resolution and validation

- provider HTTP client 现在在 timeout 分类和 retry 前优先检查 caller-owned context；parent cancellation 不再被转换成 `upstream_timeout`，也不会产生 transport retry。
- runtime cancellation boundary 同时以 run context 的完成状态为权威事实，即使第三方/兼容 adapter 返回不支持 `errors.Is` 的 timeout wrapper，也会优先解析 `context.Cause(ctx)` 并收敛到正确 budget pause。
- 永久回归覆盖真实阻塞 OpenAI-compatible HTTP provider 的 transport retry on/off × runtime auto-resume on/off 矩阵，并验证 queue `blocked`、child `paused`、准确 reason、无 `provider.auto_resume` event/reminder；另有真实 HTTP 用例验证 provider-owned request timeout 先到时仍按 `upstream_timeout` failure 处理。
- Focused validation: `go test ./internal/provider ./internal/runtime -run 'TestJSONClientParentDeadlineCancelsWithoutRetry|TestChildBudgetReasonWinsWrappedProviderTimeout|TestQueueChildAbsoluteDeadlineWinsRealHTTPProviderTimeoutMatrix|TestRealHTTPProviderTimeoutBeforeChildDeadlineRemainsFailure|TestJSONClientUsesPerAttemptRequestTimeout|TestOperationDeadlineEarlierThanChildBudgetRemainsProviderFailure' -count=1 -timeout=45s`。

## QUEUE-002 — A blocked queue job retains a live worker lease after the worker has returned

- Severity: P1
- Confidence: High
- Status: Resolved

### Evidence

- `internal/runtime/delegation.go:1297-1300` 在 child run 返回后刷新并复制最后一次 lease。
- `internal/runtime/delegation.go:1343-1350` 把 resumable child 映射为 `blocked` 并直接保存 job，但没有清空 `claimed_by`、`claimed_at`、`heartbeat_at`、`worker_pid` 和 `process_start_id`。
- `internal/session/process_liveness.go:66-76` 把带 live owner 的 `blocked` job 判定为仍可自行推进。
- `spec/14-multi-agent-and-isolation.md:161-165` 与 `spec/15-background-queue.md:247-255` 要求已经交付过 liveness/deadlock notification 的 blocked work 不能让后续 `agent_wait` 再次静默停车。
- 临时回归让 queue child 调用 `await_input`。`ProcessNextJob` 已返回且 job 已为 `blocked`，但 job 仍保留当前进程的完整 lease；`QueueJobCanProgress(job)` 返回 `true`。

### Why it matters

worker 已经不再执行该 child，但 parent coordination 会把 job 当成仍可自行推进。parent 接纳第一次 blocked notification 后若再次调用 `agent_wait`，deadlock detection 可能不唤醒它；默认 `background_wait_timeout_sec=0`，只能等到默认 15 分钟 lease stale reaper 或进程退出。这是可见的 parent hang/liveness regression。

### Root cause

初始 queue worker settle 路径把执行 lease 当成历史观测字段保留，而 liveness 判定又把这些字段当成当前 owner 权威事实。`reconcilePromptedChildJob` 会清 lease，但首次 `ProcessNextJob` settle 路径不会，两个生命周期分支不一致。

### Acceptance criteria

- worker 一旦停止持有 job，保存 `blocked`、`completed`、`cancelled` 或 `failed` 前必须清除 active lease；如需保留历史 owner，使用单独的非权威 history/event 字段。
- 只有确有继续执行 handle/heartbeat 的 job 才能被 `QueueJobCanProgress` 判定为可自行推进。
- blocked notification 被 parent 接纳后，再次 `agent_wait` 必须立即进入 intervention reminder，而不是等待 lease stale。
- 永久测试至少覆盖：`await_input` blocked、manual pause blocked、budget pause blocked、terminal outcomes、第一次 notification 接纳后的第二次 `agent_wait`。

### Resolution and validation

- `ProcessNextJob` 在任何 stable non-running outcome 落盘前统一释放 active queue lease；这也修复了 running cancellation settle 后被本地旧 snapshot 重新写回 lease 的竞态。
- `QueueJobCanProgress` 现在只把 queued 和带 live owner 的 running job 视为可自行推进；blocked 始终是 intervention-required，旧版本遗留的 live PID/heartbeat 不再造成假 liveness。
- 永久回归覆盖运行中 heartbeat 更新后 completed 清 lease、真实 `await_input` blocked、真实 cooperative `manual_stop` blocked、budget pause blocked、running queue cancellation、legacy live-lease blocked snapshot，以及 blocked/deadlock notifications 被接纳后的第二次 `agent_wait` preflight。
- Focused validation: `go test ./internal/session ./internal/runtime -run 'TestQueueJobCanProgress|TestQueueWorkerRefreshesHeartbeat|TestQueueWorkerReleasesLeaseForBlockedAwaitingInputAndSecondWaitIntervenes|TestQueueWorkerReleasesLeaseForManualPause|TestBackgroundBudgetPauseExtendResumeAndCrossParentRejection|TestStopAgentCancelsRunningQueueShellProcessGroup' -count=1 -timeout=60s`。

## CAP-003 — Resumed child work bypasses `max_active_children` and the durable claim lock

- Severity: P1
- Confidence: High
- Status: Open

### Evidence

- 新 foreground child 在 `internal/runtime/delegation.go:150-180` 通过 `AcquireDirectChildSlot(...)` 在 `claim.lock` 下检查并占用容量。
- 新 queue child 也通过 `ClaimNextQueuedJobWithLimit(...)` 在同一 durable lock 下检查容量。
- `internal/runtime/delegation.go:756-770` 的 `agent_prompt` resume 路径直接把 blocked job 标记为 running，或直接调用 child `Continue`；它没有获得 direct reservation、没有在 `claim.lock` 下检查容量，也没有统一的 resume slot。
- `spec/14-multi-agent-and-isolation.md:324`、`spec/15-background-queue.md:269-272` 和 `spec/02-cli-and-config.md:438` 明确要求 active child cap 同时覆盖 foreground/background child，不能被 worker 数量或其他入口绕过。
- 临时回归设置 `max_active_children=1`：一个 running queue job 已占满唯一 slot，随后 parent 对 budget-paused direct child 执行 `agent_prompt.budget_extension`。当前实现仍成功恢复并完成该 direct child，实际 active work 超过 cap。

### Why it matters

`max_active_children` 是资源与并发安全边界。首次 spawn/claim 有限制，但 resume 没有限制，会让最常见的 budget extension/recovery 路径绕过并发上限，造成 provider 并发、CPU/内存和 shell 子进程数量超出 operator 配置。

### Root cause

容量实现只覆盖“新建 direct child”和“claim queued job”，没有把“resume existing child”建模为同一种 active-slot acquisition。queue resume 的 `markPromptedJobRunning` 也未复用 claim lock。

### Acceptance criteria

- 所有从非 running 状态进入 running 的 child，包括 direct child、blocked queue child、budget extension resume 和 parent-stop resume，都必须在同一 `claim.lock` 下原子检查并占用 root-scoped active slot。
- 无容量时应保持原 paused/blocked 状态和 effective budget，不得先 extension 后启动；若 extension 已先持久化，失败必须可安全重试且不能造成 attempt/事件重复。
- run 进入稳定非 running 状态后必须可靠释放 slot；异常退出由 owner liveness/reaper 回收。
- 永久测试至少覆盖 direct-vs-queue、queue-vs-queue、多进程/多 goroutine 并发 resume、extension 后容量不足、取消与 resume 竞态。

## LIFE-004 — A direct foreground child in `awaiting_input` or `failed` cannot be resumed by its parent

- Severity: P2
- Confidence: High
- Status: Open

### Evidence

- `spec/14-multi-agent-and-isolation.md:202-205` 规定 parent 可通过 `agent_prompt` continue 已 linked 且处于 `paused` / `awaiting_input` / `failed` 的可恢复 child。
- `internal/runtime/delegation.go:854-882` 虽然先接受这三种状态，但没有 `queue_job_id` 的 direct child 最终只允许 `paused + manual_stop`；direct `awaiting_input` 和 direct `failed` 都返回 false。
- `internal/runtime/delegation.go:750-754` 随后返回：`child session ... is awaiting_input and is not a blocked or parent-stopped session that agent_prompt can restart`。
- 临时回归创建 parent-owned direct child，状态为 `awaiting_input`，再通过 `agent_prompt` 发送恢复消息；调用被上述错误拒绝，provider 未获得继续机会。

### Why it matters

foreground child 可以合法调用 `await_input`，也可能因可修复 provider/tool 问题进入 `failed`。当前 parent 无法通过公开的 model-led control surface 恢复它，只能放弃 work 或依赖未定义的外部直接 continue，导致 direct/background lifecycle 不一致。

### Root cause

`childPromptContinueBehavior` 把 queue `blocked` 和 direct `manual_stop` 当成特例实现，但漏掉了 spec 已声明的 direct `awaiting_input` / `failed` 可恢复状态。

### Acceptance criteria

- parent-owned direct child 的 `awaiting_input`、`failed` 和允许恢复的 paused reason 应可通过 `agent_prompt` continue。
- budget-paused direct child 仍必须要求有效 `budget_extension`；completed/cancelled child 仍不可通用恢复。
- resume 后 parent coordination 必须保持 unresolved，直到 child 真正 terminal；结果、事件和 summary 必须与 queue child 一致。
- 永久测试至少覆盖 direct `awaiting_input`、direct `failed`、manual stop、budget pause、completed/cancelled rejection 和 cross-parent rejection。

## CAP-005 — A dead direct-child reservation can consume capacity forever while stale state remains `running`

- Severity: P1
- Confidence: High
- Status: Open

### Evidence

- direct slot reservation 在 `internal/session/child_slots.go:15-71` 持久化 owner PID/process token。
- `internal/session/child_slots.go:115-124` 先计算 `hostProcessAlive(...)`，但只要 `state.json` 仍可读取，就用 `state.Status == running` 覆盖 owner liveness 结果。
- direct child 进程被 kill/crash 时，`state.json` 很可能仍为 `running`；在非 Web CLI/worker 场景下没有保证会先执行 stale-session reconcile。
- 临时回归创建 `running` child state，并把 reservation owner 改为确定不存在的 PID `999999`。随后 `ClaimNextQueuedJobWithLimit(1)` 仍返回 `ok=false`，表明 dead reservation 持续占用唯一容量。

### Why it matters

一次 foreground child 所在进程异常退出即可永久耗尽该 root 的 active child cap，后续 direct spawn 和 queue claim 都可能持续报告容量已满。当前只对“尚未创建 session”的旧 reservation 做 age-based 清理，无法修复更常见的“session 已创建但 owner 崩溃”窗口。

### Root cause

reservation cleanup 把 durable session status 当成 owner liveness 的替代品；但 crash 后 `running` 正是最容易过期的状态。`ProcessStartID` 也没有参与 direct reservation 的活性判定。

### Acceptance criteria

- direct reservation 是否有效必须同时要求 owner identity 存活和 session 仍处于 running；dead owner 不得被 stale `state.json` 覆盖。
- Linux 应校验真实进程 start identity（例如 `/proc/<pid>/stat` starttime），避免 PID reuse 把旧 reservation 误判为新进程持有。
- 无法可靠判断 liveness 的平台可采用保守策略，但必须有 heartbeat/lease stale 上界，不能永久占用容量。
- 回收 reservation 时应把僵尸 running child 收敛到可恢复状态或至少写入可诊断事件，避免只删除 capacity fact 而保留矛盾 UI 状态。
- 永久测试至少覆盖 dead PID + running state、PID reuse/start-id mismatch、missing state、malformed reservation、live owner 和并发 claim。

## BUD-006 — Active-runtime usage is only persisted at run exit, so live telemetry is stale and crashes refund budget

- Severity: P2
- Confidence: High
- Status: Open

### Evidence

- `internal/runtime/budget.go:145-181` 只在 `childBudgetRun.finish(...)` 时累计并持久化本次 active runtime。
- `internal/runtime/budget.go:184-213` 另一个持久化点仅是进入 background wait 时的 `pauseActive(...)`。
- 正常 provider/tool/hook/shell 执行期间没有 active-runtime heartbeat或周期性 checkpoint；`effective_budget.used_active_runtime_ms` 和 remaining 只在 run 结束/暂停后更新。
- `spec/17-web-console.md:376` 要求 session detail budget inspector 展示 used/remaining/active runtime；运行中的 child 目前只能看到上一次边界的旧 snapshot。
- 进程在 provider/tool 执行中被 kill 时 defer 不会运行，最后一次持久化之后消耗的 active runtime 无法恢复。由于 offline 时间又不能计入 active runtime，仅凭 `attempt_started_at` 无法在重启后准确重建，结果是已消耗时间被退还。

### Why it matters

operator 在 child 运行期间看到的 active usage/remaining 不是真实当前值；更严重的是，反复 crash/restart 可以绕过 `max_active_runtime_sec`。这使 active-runtime budget 不是完整的 durable resource budget，而只是 graceful-exit accounting。

### Root cause

实现只有进程内 timer 和终态结算，没有 durable active lease interval、heartbeat 或增量 usage checkpoint。

### Acceptance criteria

- 增加低频、可配置且有界的 active-runtime checkpoint/heartbeat，更新 session 与 linked job 的同一 effective budget snapshot；不得每个毫秒写盘。
- crash recovery 必须保留最后已确认的 active usage，并对最后一个未闭合 active interval采用明确、保守且有上界的策略；offline 时段不得继续累计。
- Web inspector 在 running child 上应展示近实时 used/remaining，并明确 last-updated/heartbeat 时间。
- 永久 subprocess 测试应覆盖：provider 执行中 kill、tool/shell 执行中 kill、重启后 resume、offline interval 不计入、重复 crash 不能无限刷新 active budget。

## Recommended fix order

1. `BUD-001`：先恢复 budget cause fidelity，避免错误终态和失败统计污染。
2. `QUEUE-002`：修复 blocked lease，消除默认可达的 parent 长时间挂起。
3. `CAP-003` 与 `CAP-005`：统一 active-slot acquisition/recovery，保证并发上限既不会被绕过，也不会被僵尸占满。
4. `LIFE-004`：补齐 direct child 的公开恢复生命周期。
5. `BUD-006`：完善 durable active-runtime ledger 和 live observability。
