# Go CLI Agent Background Queue Spec

> 当前定位：large-project profile 规格。后台队列不是默认 Web 页面里的必选工作流，但 Web-first v1 需要提供轻量提交与观测入口；底层必须具备真实 worker、job/session 关联和 parent notification 证据。

## 1. 目标

`go-cli-agent` 需要支持“先提交任务、后由自治 worker 拉起执行”的后台模式。

该能力服务于：

- child agent 的异步执行
- 无人值守批量任务
- 外部系统通过文件事实驱动的作业投递
- 活跃 `master agent` 在当前 Web/CLI 运行上下文内自动拉起 child agent，而不是要求用户手动再开 worker

## 2. 存储布局

```text
<session.dir>/_queue/
  queued/
    job_*.json
  running/
    job_*.json
  blocked/
    job_*.json
  completed/
    job_*.json
  failed/
    job_*.json

.go-cli-agent/sessions/<parent-session-id>/
  control/
    background.jsonl
```

采用按状态分目录的原因：

- worker claim 可用原子 rename
- 多 worker 下不容易重复抢占同一 job
- 查询时不必依赖进程内锁

## 3. Job 数据结构

```json
{
  "schema_version": 1,
  "id": "job_1234abcd",
  "created_at": "2026-03-19T12:00:00Z",
  "updated_at": "2026-03-19T12:00:00Z",
  "status": "queued",
  "claimed_by": "",
  "claimed_at": "",
  "heartbeat_at": "",
  "worker_pid": 0,
  "process_start_id": "",
  "parent_session_id": "20260319-115500-aa11bb",
  "root_session_id": "20260319-115500-aa11bb",
  "agent_name": "reviewer",
  "agent_role": "evaluator",
  "prompt": "Review the provider adapter and return the smallest safe fix.",
  "mode": "exec",
  "provider": "openai-compatible",
  "model": "gpt-5.4",
  "requested_workdir": "/repo",
  "effective_workdir": "",
  "visible_paths": [],
  "session_id": "",
  "session_status": "",
  "system_override": "",
  "background": true,
  "wait_mode": "notify",
  "resume_parent": true,
  "isolation_mode": "auto",
  "isolation_root": "",
  "last_error": "",
  "final_text": ""
}
```

## 4. CLI

新增命令：

- `go-cli-agent experimental queue submit [prompt]`
- `go-cli-agent experimental queue list`
- `go-cli-agent experimental queue show <job-id>`
- `go-cli-agent experimental queue worker`

### 4.1 `queue submit`

作用：

- 写入一个 `queued` job
- 不立即执行

高频参数：

- `--parent`
- `--agent`
- `--role`
- `--provider`
- `--model`
- `--workdir`
- `--system`
- `--mode`
- `--wait-mode`
- `--json`
- `--isolation`
- `--isolation-root`

### 4.2 `queue list`

作用：

- 读取最近 jobs
- 展示状态、parent、session、更新时间

### 4.3 `queue show`

作用：

- 展示单个 job 完整内容

### 4.4 `queue worker`

作用：

- 轮询 `queued/`
- claim 一个 job
- 启动真实 session
- 回写结果

参数：

- `--once`
- `--poll-ms`
- `--json`

### 4.5 auto worker

`run` / `exec` 的活跃 session 默认开启 `runtime.queue.auto_worker=true`：

- 当前 CLI 进程会自动轮询并消费 queue jobs
- `master agent` 通过 `agent_spawn(background=true)` 派发的 child job 不需要用户再手动执行 `queue worker`
- 显式 `queue worker` 仍保留，适合分离进程或长期值守

Web-first 控制台还允许一个显式 worker pool：

- 由 `go-cli-agent web` 进程托管；`experimental web` 作为旧入口兼容别名
- 默认按配置或命令行参数启动 `N` 个 worker
- 每个 worker 都必须通过真实 `ProcessNextJob(...)` 消费队列，不能伪造 running/completed 状态
- 多 worker 共享同一 queue 根目录，依赖 claim rename 保证不重复消费

## 5. Worker 语义

### 5.1 claim

worker 通过原子 rename 从 `queued/` 移到 `running/`。

### 5.2 执行

worker 启动真实 `Runner.Start(...)`，不是伪执行或 dry-run。

### 5.3 结果回写

完成后：

- `session_id`
- `session_status`
- `effective_workdir`
- `final_text`
- `last_error`

都要写回 job 文件。

状态映射：

- child `completed` -> queue `completed`
- child `failed` -> queue `failed`
- child `paused` / `awaiting_input` / 其他可恢复非终态 -> queue `blocked`，保留 `session_id`、`session_status` 与 `last_error` 方便后续 `continue`
- 只有仍由 worker 持有并心跳更新的执行中 job 保持 `running`

### 5.3.1 parent notification

若 job 带有 `parent_session_id`，worker 还必须向 parent session 写入一条 `control/background.jsonl` 记录，至少包含：

- `queue_job_id`
- `session_id`
- `agent_name`
- `agent_role`
- `status`
- `session_status`
- `effective_workdir`
- `final_text`
- `last_error`

该通知在 parent 的下一次 `control_drain` 安全边界自动并入上下文。

若 job 带有 `resume_parent=true`，这是 parent agent 自己选择停车等待的事实。活跃 parent run 可以在 `agent_spawn(background=true, resume_parent=true)` 的 tool result 落盘后进入 `awaiting_input` / `background_wait`，保持 auto worker 存活；worker 写入任一 pending background notification 后，parent 在同一 harness 流程中自动接纳 `<background-agent-results>` 并继续下一次 provider turn。该能力不能由 runtime 自动替 parent 决定，只能由模型显式选择。parent 恢复后由模型判断是否继续等待其他 child、提示 child 收敛或停止不再需要的 queued job。

当 parent coordination 仍存在 unresolved child session 或 queue job 时，parent 的普通 `run` 自然停顿不能直接退出到普通 `awaiting_input`。runtime 应提醒模型先选择 `agent_wait` 停车等待，或通过 `agent_stop` 停止尚未被 worker claim 的 queued job；对于已经 running / blocked 的 child work，模型可以用 `agent_prompt` 向 child 发送收敛/交付 prompt，再通过 `agent_wait` 等待结果，或用 `agent_status` / `agent_list` 检查后交给拥有 active handle 的控制面处理。`finish` 同样由 parent coordination gate 阻断 unresolved work，即使 `wait_mode=wait-any` 已经收到一个完成结果，也不能带着其他未结束 child/job 退出。

parent-linked queue job 只能由 root master session 创建。`depth > 0` 或存在 `parent_session_id` 的 child session 不允许再提交 parent-linked background queue job，避免 nested sub-agent 的等待状态分散到多层 parent coordination。

### 5.4 失败定义

以下情况标记 job `failed`：

- 无法 claim 后续执行所需配置
- child session 最终状态为 `failed`
- worktree 隔离准备失败

以下情况标记 job `blocked`：

- child session 停在 `paused`
- child session 停在 `awaiting_input`
- child session 进入其他非 terminal、但仍可继续恢复的状态

### 5.5 worker 生命周期

- 单个 job 失败时，worker 必须先把 job 状态持久化为 `failed`
- 普通任务失败不应直接结束长跑 worker，worker 应继续轮询后续 job
- 只有 claim / 落盘等 queue 基础设施错误才让 worker 返回错误

### 5.6 孤儿 job 回收（liveness reaper）

job claim 通过 `process_start_id` + `worker_pid` + `heartbeat_at` 记录持有者。持有进程异常退出（例如 web 服务重启）后，留在 `running/` 或 `blocked/` 的 job 不会自动前进，会让其 parent 永久停留在 wait-all 协同里。runtime 必须提供周期性回收：

- 扫描 `running/` 与 `blocked/`，识别孤儿 job：`process_start_id` 非当前进程且持有进程已不存在（`/proc` 探测），或心跳超过 `lease_stale_after`（默认 `15m` / `runtime.queue.lease_stale_after_sec`）。
- 回收策略为混合（仅做 liveness 恢复，绝不替模型决定 workflow）：
  - 关联 child session 已 `completed` / `failed` → 结算为对应 queue 终态，释放 parent coordination gate。
  - 尚未创建 child session（claim 后崩溃）→ 清除 lease 重新入队 `queued/`，由后续 worker 重跑。
  - 关联 child session 仍可恢复（`paused` / `awaiting_input` / 进行中但持有者已死）→ 标记 `blocked` 并确保 parent 存在 pending background notification，交模型决策。
- 回收由 `web` 进程的后台循环驱动（`runtime.queue.reaper_interval_ms`，默认 `60s`，`<= 0` 关闭）。回收 job 后，`web` 还要对僵尸 `running` session（`status=running` 但无存活 owner）执行 stale-owner reconcile，转为 `paused` 可续。
- 持有者存活判定必须保守：无法判定（无 `/proc`、stat 异常）时视为存活，避免误回收健康 job。

### 5.7 父任务死锁检测与唤醒

当 parent 因 `agent_wait` / `resume_parent` 停车（`background_wait`）等待后台结果时，如果其全部 unresolved 工作都无法再自行前进，parent 会无限等待。runtime 必须检测并唤醒：

- 死锁判定：parent 处于 parked，且每个 unresolved queue job 都不可前进（`blocked` 且持有进程已死，或终态但仍挂在 unresolved），同时每个 unresolved child session 都为非 `running` 的非终态。
- 命中后写入 `parent.coordination.deadlock` 事件，并注入一条 `coordination_deadlock` 来源的 pending background notification（无 `queue_job_id`，`status=coordination_deadlock`）。该 notification 在下一安全边界并入上下文，提示模型用 `agent_prompt` 收敛、`agent_stop` 停弃，或自行继续。
- 注入有幂等保护：已有 pending 死锁 notification 时不重复注入。
- 兜底超时：`runtime.queue.background_wait_timeout_sec`（默认 `0` 不超时）为等待墙钟上界，超时写 `session.background.wait_timeout` 并回到 `awaiting_input`。

### 5.8 child / background 会话兜底预算

为防止单个委派会话无限 loop（只烧 token 不产出），runtime 对 child / background（有 `parent_session_id`）会话施加兜底预算：

- `runtime.child_budget.max_wall_clock_sec`（默认 `7200`）与 `max_turns`（默认 `1500`），任一维度 `0` 表示禁用；墙钟从会话 `created_at` 计（跨恢复累计）。
- root master session 不受此预算约束，仍沿用 `runtime.max_turns_hard`（默认 `-1` 关闭）。
- 超限时 child 以可恢复方式 `paused`（reason `child_budget_turns_exceeded` / `child_budget_wallclock_exceeded`），并记录 `session.child_budget.exceeded` 事件；其 linked queue job 随之 reconcile 为 `blocked` 并写 parent notification，由模型决定续跑/收敛/停止。runtime 不直接判失败，也不替模型决定后续 workflow。

## 6. 与 delegation 的关系

- `agent_spawn background=true` 必须复用同一套 queue job 模型
- `agent_prompt` 必须复用同一套 child session steer 队列，且只能操作当前 parent 关联的 child/job
- `children` / `agent_list` 必须能读到 parent 关联 jobs
- `master agent` 默认依赖 auto worker + background notification 完成“派发 child -> 后台执行 -> 结果回流”的闭环，而不是要求用户手动驱动

## 7. 验收标准

- `experimental queue submit` 能稳定落盘 job
- `experimental queue worker --once` 能真实消化至少一个 job
- auto worker 能在活跃 CLI 进程中自动消化 background child job
- queue job 与 child session 能正确关联
- child 完成/失败后 parent session 能在下一安全边界接纳 background notification；child 若停在 `paused` / `awaiting_input`，queue job 必须保持 `blocked` / resumable，不能释放 parent completion gate
- 多 worker 同时启动时不会重复消费同一 queued 文件
