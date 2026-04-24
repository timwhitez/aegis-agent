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
- 可计算 ready / blocked / completed

说明：

- 对于 large-project / long-horizon 任务，runtime 可以在早期 repo-scale 检索后注入协调型 harness reminder，提示模型先把 `reports/spec.md`、`reports/plan.md`、`reports/progress.md`、`reports/validation.md` 与 durable task state 外置出来，再继续扩张检索或实现。
- 当后续 source edit、测试或验证动作已经超过最近一次 durable handoff 刷新点时，runtime 可以继续提醒甚至阻断 finish / agent handoff，要求先刷新 `reports/progress.md` 与 `reports/validation.md`，避免 child session 或恢复 session 拿到过期状态。
- 即使运行在 `yolo` 模式，如果单个 session 已经积累明显长任务级别的工具调用或 compaction 事实、但仍没有任何 `todo_write` / `task_*` 状态，runtime 可以在 `finish` 前阻断一次，要求先写入最小 durable taskboard，避免超长会话以零任务事实源结束。
- 这个 reminder 只是把执行拉回可恢复节奏，不替代 `todo_write` / `task_create` / `task_update` 本身。

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

- 同一时刻最多一个 todo 为 `in_progress`
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

- 全量替换 `todo.json`
- 校验最多一个 `in_progress`
- 写 `todo.updated` 事件

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

- `include_completed?`
- `status?`

返回：

- task 数组
- ready / blocked / completed 统计

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
- task graph 可持久化、恢复、列出
- 双向依赖始终一致
- cycle 被拒绝
- `completed` 可自动解锁 dependents
- compaction 后 todo / tasks 仍可被读取
