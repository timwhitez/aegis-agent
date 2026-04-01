# Go CLI Agent Runtime Architecture

## 1. 总体分层

运行时固定分三层：

1. `core runtime`
   - 负责 loop、provider、tools、skills、hooks、compaction、session、events
2. `sdk facade`
   - 对外暴露稳定 Go 接口，未来可扩为公共包
3. `cli adapter`
   - 负责参数解析、stdin/stdout、键盘中断、阶段输出

当前 app-facing 构造约束：

- 默认 core CLI / SDK 路径使用独立的 `core` facade
- 扩展 delegation / queue 路径使用独立的 `experimental` facade
- 扩展 Web console 路径使用独立的 `web` app/service facade
- 纯 store / snapshot 读取路径使用独立的 `store` facade
- 这些 facade 可以共享更低层的 runtime primitive，但不能在 app 层重新坍缩成同一个 concrete runner type

约束：

- CLI 不直接持有 provider 逻辑。
- provider 不直接写终端。
- hooks 不直接改写 session 文件。
- session store 是唯一持久化入口。
- compaction 只改“提供给模型的上下文视图”，不破坏原始日志。
- live steer 不通过共享内存偷渡输入，而通过 session store 管理的控制队列并入执行。

## 2. 核心对象

### 2.1 Runner

职责：

- 创建或恢复 session
- 管理 active run 与 interrupt cancel 函数
- 调用 runtime engine
- 决定 `run` / `exec` 的 completion policy
- 向 session store 和 event bus 写入结果

### 2.2 Engine

职责：

- 执行单个 session 的 agent loop
- 调用 provider
- 分发 tool calls
- 触发 hooks
- 更新 session state
- 在合适时机触发 compaction

### 2.3 ProviderAdapter

职责：

- 接收内部 `TurnRequest`
- 调用供应商 HTTP API
- 归一化为内部 `TurnResult` 和 provider 事件
- 处理 provider 特有的多轮回放格式

### 2.4 ToolRegistry

职责：

- 注册内置工具与 skill 提供的工具
- 输出 provider 可消费的 tool schema
- 执行工具并返回结构化结果

### 2.5 SkillCatalog

职责：

- 扫描本地 `skills/`
- 缓存 skill metadata
- 为 `load_skill` 提供完整正文

### 2.6 HookManager

职责：

- 根据配置载入 hooks
- 按顺序执行 event/transform hooks
- 记录 hook 成功、失败、阻断

### 2.7 PlanningStore

职责：

- 管理 `todo.json` 与 `tasks/`
- 提供 todo update / read
- 提供 task graph CRUD、依赖校验与 ready-state 计算

### 2.8 SessionStore

职责：

- 管理 `session.json`、`state.json`、`messages.jsonl`、`events.jsonl`、`control/`
- 写入 compaction artifacts
- 为 `continue` 提供恢复数据
- 持久化 `control/steer.jsonl` 与 `control/background.jsonl`

### 2.9 LiveInputManager

职责：

- 接收 `steer` 追加输入
- 管理 `control/steer.jsonl`
- 在安全边界把 live input 并入当前 session
- 对 `--interrupt` 请求触发 best-effort 抢占

### 2.10 EventBus

职责：

- 进程内发布订阅事件
- 服务于 CLI 渲染、hooks、未来 SDK / API 订阅

### 2.11 Compactor

职责：

- 估算 provider 输入规模
- 在不丢失原始日志的前提下压缩提供给模型的上下文
- 生成压缩摘要与 transcript artifact

### 2.12 DelegationManager

职责：

- 为 parent session 创建 child session
- 维护 parent / root / depth 元数据
- 为 child session 准备 isolation workdir
- 统一 CLI 与 tool 的 delegation 契约
- 当 `agent_name` 显式声明 `planner` / `generator` / `evaluator` 一类 persona 时，把它作为 role hint 传入 child session，而不是把角色分工硬编码成固定 DAG
- child handoff 必须依赖可见文件事实，例如 `reports/spec.md`、`reports/plan.md`、`reports/progress.md`、`reports/validation.md` 与 visible output 列表，而不是依赖进程内临时上下文

### 2.13 QueueStore And Worker

职责：

- 管理后台自治 job 持久化
- claim queued jobs
- 拉起真实 child session 并回写结果
- 在活跃 CLI 进程内提供 auto worker
- 将 child 完成/失败结果投递回 parent session 的控制通知

### 2.14 TerminalDashboard

职责：

- 渲染 session / child / queue / event 面板
- 只读取文件事实，不持有权威状态

### 2.15 WebConsoleService

职责：

- 提供本地 HTTP API 与静态前端资源
- 复用 `SessionStore` / `Runner` / `QueueStore`，不创建第二套状态源
- 为 Web 发起的 `start` / `continue` 建立异步执行句柄
- 维护 queue worker pool，并通过独立 worker `Runner` 支持后台并行消费
- 提供 overview / session detail / queue / children / task board 的聚合只读视图
- 对 `steer`、`continue`、`queue submit`、`interrupt` 等控制操作做参数校验与状态映射

约束：

- Web service 只能作为显式 experimental app-surface 存在
- active session 的中断控制必须是 session-scoped，不允许多个并发 session 共享同一个 in-memory interrupt slot
- Web UI 的刷新默认使用 polling；是否升级到 SSE / WebSocket 不作为当前实现前提

## 3. 内部消息模型

内部统一消息结构：

```text
Message
  id: string
  role: user | assistant | tool | system
  text: string
  tool_calls?: []ToolCall
  tool_results?: []ToolResult
  meta?: map[string]any
```

说明：

- `system` 仅作为内部建模与回放辅助，不直接暴露为用户输入角色
- `tool` 是统一抽象；具体 provider 回放时会转换为 `function_call_output`、`tool_result`、`functionResponse` 等形式

### 3.1 ToolCall

字段：

- `id`
- `name`
- `arguments`
- `provider_call_id`

### 3.2 ToolResult

字段：

- `tool_call_id`
- `name`
- `llm_output`
- `display_output`
- `is_error`
- `final`

约束：

- `llm_output` 面向模型
- `display_output` 面向 CLI
- `final` 仅由 `finish` 工具使用

## 4. 最小 Agent Loop

核心循环保持极简，不在 loop 里塞固定工作流：

```text
prepare -> provider_call -> parse_result -> maybe_execute_tools -> append_results -> next_turn
```

伪代码：

```text
while true:
  drain_control_inputs_if_any()
  input = build_turn_request(session)
  result = provider.run(input)

  append assistant output

  if result has tool calls:
    execute tools
    append tool results
    continue

  if completion policy says turn can stop here:
    return

  inject reminder / mark incomplete / fail based on mode
```

## 5. Turn 生命周期

每轮 turn 固定为以下阶段：

1. `prepare`
2. `project_docs`
3. `control_drain`
4. `compact`
5. `provider_call`
6. `assistant_output`
7. `control_drain`
8. `tool_dispatch`
9. `tool_execute`
10. `tool_result_append`
11. `turn_decide`

### 5.1 prepare

- 读取当前 state
- 读取当前 `todo.json`、`tasks/` 与 `reports/spec.md` / `plan.md` / `progress.md` / `validation.md` 的 durable context 视图，并写一条 `session.context.loaded` 事件，记录 project-memory present/missing 与 task/todo 计数，方便恢复与 live validation
- 必要时插入 harness reminder message
 - 典型场景：
    - `exec` done-candidate 但还未显式 `finish`
    - 最近只读检索已经明显过量，需要停止继续探索
    - 请求的 artifact 已经写出，需要优先收尾
    - 最新 interrupt steer 已明确要求“use current evidence / write artifact / finish”，需要把执行直接拉回交付
    - 大型多步骤工程/评审任务已经开始 repo-scale 检索，但还没有把 `reports/spec.md`、`reports/plan.md`、`reports/progress.md`、`reports/validation.md` 与 durable task state 外置出来，需要先补齐协调面再继续扩张检索
- 构造 system prompt
- 拼接 skills 摘要和 AGENTS 指令链
- 生成本轮 provider 输入视图

### 5.2 compact

- 在 provider 输入构造完成前检查是否需要压缩
- 不改原始 `messages.jsonl`
- 只改发送给 provider 的上下文视图及压缩 artifact

### 5.3 control_drain

- 检查 `control/steer.jsonl` 是否有待处理输入
- 检查 `control/background.jsonl` 是否有待接纳的后台结果
- 若有，按到达顺序转为新的 user messages
- `--interrupt` 请求可在此阶段把当前执行切换到新的 user priority
- 对无法立即抢占的情况，记录 `session.steer.deferred`

### 5.4 provider_call

- 调用 provider adapter
- 产生 `assistant.delta`、`tool.call.ready`、`provider.error`、`turn.stopped` 等事件

### 5.5 tool_execute

对每个 tool call：

1. 触发 `tool.before`
2. 必要时先应用 runtime 级 guard
   - 当最近 harness reminder 已明确要求“停止只读探索、直接基于当前证据行动”时
   - runtime 可以拒绝继续的 read-only tool call，并写入一条普通可重放错误结果
   - retrieval-tail guard 允许至多两次新的、精确命名的 `read_file`
   - 另外为已经检查过的文件保留两次窄窗 reread 预算，专门用于最终 snippet-backed 证据复核
   - 继续阻断 broad discovery 与无意义重复读
   - 当最新 interrupt steer 已明确要求立即交付时，guard 还可以阻断继续的 todo/task bookkeeping 或 skill-loading detour，把执行拉回 `write_file` / `edit_file` / `finish`
   - 目的是阻止 repo-scale overread、artifact 已写出后的尾部重读、interrupt steer 之后的 completion detour，以及 compaction 后的无意义重试
3. 执行工具
4. 触发 `tool.after`
5. 若执行在结果写回前被取消，生成一条可重放的中断错误结果
6. 落盘最终 tool result

### 5.6 turn_decide

分支规则：

- 如果模型调用了 `finish` 工具，则 session 完成
- 如果有普通 tool calls，则进入下一轮
- 如果没有 tool calls：
  - `run` 模式下将 session 置为 `awaiting_input`
  - `exec` 模式下视为 `done_candidate`，注入 reminder 后再给模型一次机会
- 如果上下文取消，则进入 `paused`

## 6. Completion Policy

`go-cli-agent` 区分两种 completion policy：

### 6.1 interactive

用于 `run`

- `finish` 表示整个 session 已完成
- 无 tool calls 且无 `finish` 时，不强行判定完成
- session 进入 `awaiting_input`
- 用户后续可用 `continue --message` 补充提示
- 若运行中收到 steer 输入，默认在最近安全边界直接并入，不必先进入 `awaiting_input`

### 6.2 autonomous

用于 `exec`

- 默认要求显式 `finish`
- 一次“无工具调用且无 finish”只算 `done_candidate`
- runtime 会插入一次 harness reminder
- 若 retrieval tail 已被 harness reminder 明确拉回，runtime 可对继续的只读工具调用加 guard；默认只放行一次新的精确 `read_file`，并继续阻断 broad discovery、重复读和尾部重试
- 若最新 interrupt steer 已明确要求立即交付，runtime 可插入专门的 completion reminder，并对继续的只读探索或 bookkeeping detour 加 guard，直到出现交付动作或新的外部指令
- 二次仍无 `finish` 时记为 `failed`，并在 state 中写入 `incomplete_no_finish`

## 7. Session 状态机

允许状态：

- `running`
- `awaiting_input`
- `paused`
- `completed`
- `failed`

状态转换：

```text
new -> running
running -> awaiting_input
running -> paused
running -> completed
running -> failed
awaiting_input -> running
paused -> running
failed -> running
```

不允许：

- `completed -> running`
- `completed -> paused`

## 8. 事件模型

所有关键动作都映射成结构化事件：

- `session.started`
- `session.awaiting_input`
- `session.paused`
- `session.completed`
- `session.failed`
- `user.message`
- `assistant.delta`
- `assistant.message`
- `tool.before`
- `tool.after`
- `tool.interrupted`
- `session.child.spawned`
- `session.child.queued`
- `queue.job.claimed`
- `queue.job.completed`
- `queue.job.failed`
- `session.steer.requested`
- `session.steer.queued`
- `session.steer.accepted`
- `session.steer.deferred`
- `todo.updated`
- `task.created`
- `task.updated`
- `session.context.loaded`
- `hook.triggered`
- `hook.finished`
- `hook.failed`
- `state.changed`
- `compact.started`
- `compact.finished`

每个事件字段至少包括：

- `schema_version`
- `id`
- `session_id`
- `type`
- `time`
- `phase`
- `data`

## 9. 中断模型

### 9.1 `run` 模式

- CLI 在单独 goroutine 中监听键盘输入
- 捕获 `Esc` 后调用 `Runner.Interrupt(sessionID)`

### 9.2 interrupt 效果

- cancel 当前 provider 请求或工具执行上下文
- 如果 assistant 已经发出 tool call 且工具执行被打断，先落一条 `is_error=true` 的中断 tool result，再写 `paused`
- 将 state 改为 `paused`
- 写入 `session.paused` 事件
- 返回控制权给 CLI

### 9.3 恢复原则

不恢复中断前已经启动的外部进程：

- shell 子进程
- 未完成 hook command
- 未完成系统调用

恢复只从文件事实重新构建消息与状态。
