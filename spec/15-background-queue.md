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

## 6. 与 delegation 的关系

- `agent_spawn background=true` 必须复用同一套 queue job 模型
- `children` / `agent_list` 必须能读到 parent 关联 jobs
- `master agent` 默认依赖 auto worker + background notification 完成“派发 child -> 后台执行 -> 结果回流”的闭环，而不是要求用户手动驱动

## 7. 验收标准

- `experimental queue submit` 能稳定落盘 job
- `experimental queue worker --once` 能真实消化至少一个 job
- auto worker 能在活跃 CLI 进程中自动消化 background child job
- queue job 与 child session 能正确关联
- child 完成/失败后 parent session 能在下一安全边界接纳 background notification；child 若停在 `paused` / `awaiting_input`，queue job 必须保持 `blocked` / resumable，不能释放 parent completion gate
- 多 worker 同时启动时不会重复消费同一 queued 文件
