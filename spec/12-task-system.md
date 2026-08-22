# Go CLI Agent Task System Spec

## 1. 目标

`go-cli-agent` 的任务系统采用双层结构：

- session todo
- persistent task graph

目标不是“加一个待办列表”，而是同时解决两类问题：

- 高频执行节奏跟踪
- 长任务依赖、恢复、ready-state 计算

## 2. 设计来源

本设计合并三条参考线：

- `learn-claude-code` `s03`
  - 高频 todo 更新
- `learn-claude-code` `s07`
  - 文件持久化 task graph
- `opencode`
  - session 级 `todowrite` / `todoread`

## 3. 双层模型

### 3.1 Layer A - Session Todo

todo 是当前 session 的“执行节奏板”。

特点：

- 高频改动
- 简单状态
- 无依赖边
- 直接面向当前这一次任务推进

### 3.2 Layer B - Persistent Task Graph

task graph 是当前 session 的“持久化任务板”。

特点：

- durable
- 有依赖边
- 可从磁盘恢复
- 可计算 ready / blocked / completed / cancelled / done

说明：

- 对于 large-project / long-horizon 任务，runtime 可以在早期 repo-scale 检索后注入协调型 harness reminder，提示模型先把 `reports/spec.md`、`reports/plan.md`、`reports/progress.md`、`reports/validation.md` 与 durable task state 外置出来，再继续扩张检索或实现。
- 当后续 source edit、测试或验证动作已经超过最近一次 durable handoff 刷新点时，runtime 可以继续提醒，并在 finish 前要求刷新 `reports/progress.md` 与 `reports/validation.md`，避免恢复 session 拿到过期状态；agent handoff 本身保持模型决策主导，必要时由 parent 在 child prompt 中直接携带当前进度与验证上下文。
- 即使运行在 `yolo` 模式，如果单个 session 已经积累明显长任务级别的工具调用或 compaction 事实、但仍没有任何 `todo_write` / `task_*` 状态，runtime 只注入协调型 reminder，不在 `finish` 前强制阻断；是否补最小 durable taskboard 由模型结合任务状态决定。
- 这个 reminder 只是把执行拉回可恢复节奏，不替代 `todo_write` / `task_create` / `task_update` 本身。
- `task graph` 不承担 artifact 完成判定；显式交付文件由 `contract.json` / `artifact-tracker.json` 与 `CompletionController` 负责。任务系统只提供执行节奏、依赖关系和恢复索引。
- Goal 的内部 features / milestones 可以携带 `task_ids`、`child_session_ids`、`queue_job_ids` 等显式执行关联；默认创建 goal 不强制生成 task graph，也不让 task graph 承担目标完成判定。只有高级入口启用 `create_tasks_from_plan` 或模型显式调用 task 工具时，才把计划项同步到 durable tasks。
- 长任务 checkpoint 会读取 todo/task 派生统计，写入 `checkpoints/longrun-latest.json`，但 checkpoint 仍是 resume index，不替代 task 文件本身。
- WebConsole 和 session summary 必须区分三种状态：ephemeral todo 刷新、durable task graph 进展、artifact/progress 文件推进。`todo_write` 的语义 no-op 不能显示成 durable task graph 进展；当 `tasks/` 为空时，应明确表达没有持久任务。

## 4. 存储布局

```text
.go-cli-agent/sessions/<session-id>/
  todo.json
  tasks/
    task_0001.json
    task_0002.json
```

约束：

- `todo.json` 是完整快照
- `tasks/task_*.json` 一任务一文件
- task 文件名只用于可读性，真实主键是 task `id`

## 5. Todo 数据结构

```yaml
- content: "Audit provider contracts"
  status: in_progress
  priority: high
  updated_at: "2026-03-19T12:00:00Z"
```

字段：

- `content`
- `status`: `pending | in_progress | completed | cancelled`
- `priority`: `high | medium | low`
- `updated_at`

约束：

- `in_progress` 表示该条工作已经启动；允许多个互不依赖或并行推进的 todo 同时处于 `in_progress`
- 空 todo 列表合法

## 6. Task 数据结构

```yaml
id: task_0003
subject: "Implement provider retry handling"
description: "Add retry classification and retry budget enforcement"
status: pending
priority: high
owner: ""
blocked_by:
  - task_0001
blocks:
  - task_0005
labels:
  - provider
notes:
  - "Created from initial planning pass"
created_at: "2026-03-19T12:00:00Z"
updated_at: "2026-03-19T12:10:00Z"
```

字段：

- `id`
- `subject`
- `description`
- `status`: `pending | in_progress | completed | cancelled`
- `priority`: `high | medium | low`
- `owner`
- `blocked_by`
- `blocks`
- `labels`
- `notes`
- `created_at`
- `updated_at`

## 7. 派生视图

### 7.1 ready

task 满足以下条件时视为 ready：

- `status = pending`
- `blocked_by` 为空

### 7.2 blocked

task 满足以下条件时视为 blocked：

- `status = pending`
- `blocked_by` 非空

### 7.3 done

以下状态都视为 done：

- `completed`
- `cancelled`

其中只有 `completed` 会自动解除依赖；`cancelled` 不自动解锁，必须显式调整任务图。

## 8. 图约束

### 8.1 双向依赖一致性

若 `task_A.blocks` 包含 `task_B`，则：

- `task_B.blocked_by` 必须包含 `task_A`

反之亦然。

task graph 是一个多文件事实源：跨进程写入必须在同一 durable `taskboard.lock` 下完成 read-modify-write，批量写失败必须恢复到写入前的完整 graph；`task_list` / `task_get` 也要持有该锁读取，不能观察到批量更新中途的半图。

### 8.2 无环

新增或更新依赖时必须执行 cycle check。

若引入环：

- 拒绝写入
- 返回结构化错误

### 8.3 完成自动解锁

当 task 从非完成态进入 `completed`：

- 从所有 dependents 的 `blocked_by` 中移除自身
- 更新这些 dependents 的 `updated_at`

## 9. Tool 契约

### 9.1 `todo_write`

输入：

- `todos`: `[]TodoItem`

行为：

- 输入仍是完整 `todo.json` 快照，但语义是 append/progress ledger，不是任意重写计划
- 已存在 todo 必须按原顺序保留，不允许删除、改写 content、改写 priority 或重排
- 需要改写措辞、调整范围或重设优先级时，不能编辑已存在条目，而是在末尾 append 一个新 todo
- `pending` 可推进到 `in_progress | completed | cancelled`；`in_progress` 可推进到 `completed | cancelled`；`completed` / `cancelled` 是终态，不可回退或改写
- 新增 todo 只能追加到列表末尾，初始状态只能是 `pending` 或 `in_progress`，不能直接新增为 `completed` / `cancelled`
- 允许多个 `in_progress`；runtime 不把 todo ledger 强制解释为单线程执行队列
- 校验失败时返回 directive recovery 错误（提示 `todo_read` 后重发完整快照、或改用 append），帮助模型自纠而不是反复触发同一错误
- 写 `todo.updated` 事件
- 当 normalized todo snapshot（content/status/priority/order）未变化时，返回 `noop=true` / `changed=false`，保留原 `todo.json` 的 `updated_at`，避免把自动更新时间戳误当成进展
- `todo_write` 只记录执行进度，不执行任务、不验证任务，也不能作为 `finish` 或交付物完成的证据

### 9.2 `todo_read`

输入：

- 空

行为：

- 返回当前 `todo.json`

### 9.3 `task_create`

输入：

- `subject`
- `description?`
- `priority?`
- `blocked_by?`
- `labels?`

行为：

- 分配新 `task_id`
- 创建 task 文件
- 为 `blocked_by` 建立双向边
- 进行 cycle check
- 写 `task.created`

### 9.4 `task_update`

输入：

- `task_id`
- `status?`
- `subject?`
- `description?`
- `priority?`
- `owner?`
- `add_blocked_by?`
- `remove_blocked_by?`
- `add_blocks?`
- `remove_blocks?`
- `append_note?`

行为：

- 原地更新 task
- 自动维护双向边
- 更新 `updated_at`
- 若状态变为 `completed`，执行自动解锁
- 写 `task.updated`

### 9.5 `task_list`

输入：

- `include_completed?`：默认 `true`，保证恢复/交接默认能看到完整 task graph；当为 `false` 且未指定 `status` 时，返回视图排除 `completed` / `cancelled`
- `status?`：可选 `pending | in_progress | completed | cancelled | ready | blocked | done`；显式 `status` 优先于 `include_completed`

返回：

- task 数组
- ready / blocked / completed / cancelled / done 统计

### 9.6 `task_get`

输入：

- `task_id`

返回：

- 单个 task 完整对象
- 派生状态

## 10. 与 Compaction 的关系

- todo 和 task graph 不依赖当前上下文窗口存活
- 每轮 `prepare` 可以写 `session.context.loaded` 事件，记录当前 todo/task 计数以及 project-memory present/missing 状态，作为恢复与 live validation 的 durable 证据
- compaction summary 必须包含：
  - 当前 todo 摘要
  - 全部 `in_progress` todos
  - ready tasks
  - blocked tasks
  - 当前 in_progress task

## 11. 与 Live Steer 的关系

- steer 到达后，模型可以选择先更新 todo，再继续执行
- 若 steer 改变了任务优先级，模型应优先更新 task graph，再继续工具调用
- runtime 不替模型自动改任务图，task graph 仍由模型通过工具维护

## 12. CLI Read 模式

`go-cli-agent tasks <session-id>` 至少提供两种输出：

### 12.1 text

```text
== todo ==
[>] Audit provider contracts
[ ] Update README
[x] Fix config drift

== tasks ==
READY
- task_0002 Add steer queue

BLOCKED
- task_0004 Hook integration (blocked_by: task_0002)
```

### 12.2 json

- todo
- tasks
- counters

## 13. 验收标准

- todo 可频繁更新且顺序稳定
- todo 可同时记录多个 `in_progress` 项并完整持久化、恢复和进入 compaction handoff
- task graph 可持久化、恢复、列出
- 双向依赖始终一致
- cycle 被拒绝
- `completed` 可自动解锁 dependents
- compaction 后 todo / tasks 仍可被读取
