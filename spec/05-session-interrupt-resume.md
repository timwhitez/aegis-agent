# Go CLI Agent Session Interrupt Resume Spec

## 1. 目标

session 系统保证下面四件事同时成立：

- 当前运行可追踪
- 自然停顿时可等待新输入
- 中断后可恢复
- 后续可演进成 SDK / OpenAPI 查询接口

## 2. 存储布局

默认 session 根目录：

```text
.go-cli-agent/sessions/
  <session-id>/
    session.json
    state.json
    messages.jsonl
    events.jsonl
    todo.json
    tasks/
      task_0001.json
    control/
      steer.jsonl
    artifacts/
      compactions/
      transcripts/
```

权限要求：

- session 根目录默认 `0700`
- `session.json`、`state.json`、`messages.jsonl`、`events.jsonl` 默认 `0600`

## 3. 文件职责

### 3.1 `session.json`

保存静态元数据：

- `schema_version`
- `id`
- `created_at`
- `workdir`
- `mode`
- `provider`
- `model`
- `provider_options`
- `completion_policy`

### 3.2 `state.json`

保存当前动态状态：

- `status`
- `phase`
- `turn`
- `updated_at`
- `current_task`
- `last_error`
- `incomplete_reason`
- `last_assistant_excerpt`
- `pause_reason`
- `pending_steer_count`

### 3.3 `messages.jsonl`

按顺序追加原始消息对象，供恢复与调试使用。

### 3.4 `events.jsonl`

按顺序追加结构化事件，供：

- CLI JSON 输出回放
- hook trace
- future API 查询

### 3.5 `artifacts/`

保留：

- compaction summaries
- transcripts
- future generated files metadata

### 3.6 `todo.json`

保存 session 级 todo 列表。

### 3.7 `tasks/`

保存持久化 task graph 节点，每个 task 一个文件。

### 3.8 `control/steer.jsonl`

保存运行中追加输入请求，供 active runner 消费。

## 4. Session ID

要求：

- 全局唯一
- 可读性适中
- 不依赖数据库

格式建议：

`YYYYMMDD-HHMMSS-<shortid>`

## 5. 状态定义

允许状态：

- `running`
- `awaiting_input`
- `paused`
- `completed`
- `failed`

### 5.1 `awaiting_input`

表示：

- 本次 `run` 已自然停顿
- 模型未显式 `finish`
- 用户后续可补充 prompt 后继续

它不是错误状态，也不是完成状态。

## 6. 创建规则

启动 `run` 或 `exec` 时：

- 当前 CLI 总是新建 session
- 复用已有 session 必须走 `continue`
- 自定义 session ID 预留给后续 SDK / API 层，不作为当前 CLI 参数面的一部分

启动 `continue` 时：

- 必须能找到现有 session
- 仅允许恢复 `paused`、`awaiting_input` 或 `failed`

启动 `steer` 时：

- 目标 session 必须处于 `running`
- 若 session 不在运行，返回“use continue instead”

## 7. 中断语义

### 7.1 `Esc`

在 `run` 模式中：

- `Esc` 表示“暂停当前运行”
- 不是“丢弃当前会话”

### 7.2 interrupt 处理顺序

1. 调用 `Runner.Interrupt(sessionID)`
2. cancel provider/tool 上下文
3. 若已有未闭合 tool call，则先写入中断错误结果
4. 写入 `session.paused`
5. 更新 `state.json`
6. 返回 CLI 提示

## 8. 自然停顿语义

在 `run` 模式中，若模型本轮：

- 没有继续调用工具
- 且没有调用 `finish`

则：

1. 写入最终 assistant 消息
2. 将 state 改为 `awaiting_input`
3. 写入 `session.awaiting_input`
4. CLI 输出下一步 `continue` 用法

## 9. 恢复语义

### 9.1 `continue`

恢复步骤：

1. 读取 `session.json`
2. 读取 `state.json`
3. 重建消息历史
4. 追加新的 user message（如果提供）
5. 重置本次恢复 run 的 bounded turn budget（避免沿用上一次 run 已耗尽的 `state.turn`）
6. 将状态切回 `running`
7. 继续 loop

### 9.2 不恢复的内容

- 中断前的外部 shell 子进程
- 尚未完成的系统调用
- 旧的 hook command 进程

原因：

- 简化一致性
- 避免重复副作用

## 10. Live Steer

### 10.1 基本语义

- `steer` 向 `control/steer.jsonl` 追加输入记录
- active runner 负责消费，不由 `steer` 命令直接改写 `messages.jsonl`
- 输入真正被采纳时，再写入 `messages.jsonl` 与 `events.jsonl`

### 10.2 一致性

对单条 steer 请求，顺序必须是：

1. 追加控制记录
2. 写 `session.steer.requested`
3. 转成 user message 后 append 到 `messages.jsonl`
4. 被 engine 接纳后写 `session.steer.accepted`

### 10.3 抢占与退化

- `interrupt=true` 时，runtime 尽量取消当前 provider 调用
- 若当前阶段无法立即安全打断，则保留 pending steer，请求在最近安全边界接纳
- `deferred` 仍是保留状态，留给后续更细粒度的中断结果分类；当前实现重点保证“不丢请求、能在下一安全边界接纳”

## 11. 失败恢复

若 session 为 `failed`，允许继续，但需满足：

- 原始消息历史可读
- `state.json` 可用
- 用户明确知道这是“失败后继续”

恢复后新增一条 system-level reminder：

- 说明上一轮失败点
- 要求模型结合最新用户输入继续
- 若上一轮失败原因为 `incomplete_no_finish`，需明确提醒模型必须显式调用 `finish`

## 12. Hook 与恢复的关系

- hook 触发记录进入 `events.jsonl`
- `continue` 不重放历史 hooks
- 新一轮中只触发新的 hooks

## 13. 最小查询能力

session store 至少提供：

- `Create`
- `Load`
- `SaveState`
- `AppendMessage`
- `AppendEvent`
- `AppendSteerRequest`
- `DrainSteerRequests`
- `LoadTodo`
- `ListTasks`
- `GetTask`
- `List`

返回对象要足够支撑 CLI 和未来 SDK。

## 14. 一致性要求

以下动作必须“先写盘再返回”：

- session 创建
- session 进入 `awaiting_input`
- session 暂停
- session 完成
- session 失败

否则 `continue` 和 `sessions` 列表会出现不一致。

## 15. 验收标准

- 新 session 创建后基础文件与目录存在
- `Esc` 后 session 状态为 `paused`
- 自然停顿后 session 状态为 `awaiting_input`
- `continue` 后消息历史延续而不丢失
- 已取消的工具在历史中以中断错误结果可见
- `steer` 请求可落盘、接纳、并转成真实 user message
- `events.jsonl` 可单独重建主要执行轨迹
