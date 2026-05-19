# Go CLI Agent SDK And API Evolution Spec

## 1. 目标

虽然 v1 默认交付本地 Web 控制台并保留 CLI fallback，但从一开始就要保证未来可以：

- 提供 Go SDK
- 提供 OpenAPI / HTTP 服务
- 不重写核心 runtime

## 2. 稳定边界

需要尽早稳定的不是 CLI 输出，而是以下内部服务接口：

- session 生命周期
- 事件模型
- tool 执行契约
- provider turn 契约
- provider generation / reasoning 选项契约
- interrupt / continue / awaiting_input 语义
- steer 语义
- todo / task graph 数据契约

## 3. 建议的公共 Go API

未来对外暴露为公共包时，默认公共 `Runner` 应保持 runtime-first，优先保留以下接口：

```text
type Runner interface {
  Start(ctx, StartRequest) (RunResult, error)
  Continue(ctx, ContinueRequest) (RunResult, error)
  Steer(ctx, SteerRequest) (SteerResult, error)
  Interrupt(sessionID string) error
  State(sessionID string) (SessionState, error)
  Tasks(sessionID string) (TaskBoard, error)
  List(ctx, ListSessionsRequest) ([]SessionSummary, error)
}
```

扩展 phase 的 `delegate` / `queue` / `store-view` 能力若未来要进入 SDK，应通过单独的 experimental facade 或 package 暴露，而不是重新塞回默认 `Runner`。

### 3.1 StartRequest

- `prompt`
- `provider`
- `model`
- `workdir`
- `mode`

### 3.2 ContinueRequest

- `session_id`
- `message`

### 3.3 RunResult

- `session_id`
- `status`
- `final_text`
- `last_error`

`status` 必须允许：

- `awaiting_input`
- `paused`
- `completed`
- `failed`

### 3.4 SteerRequest

- `session_id`
- `message`
- `interrupt`

### 3.5 SteerResult

- `session_id`
- `accepted`
- `behavior` (`queued` | `interrupted` | `deferred`)

补充约束：

- 空 steer 输入必须被拒绝
- 超长 steer 输入必须在入队前拒绝，而不是等到后台控制队列后才失败

### 3.6 TaskBoard

- `todo`
- `tasks`
- `counters`

## 4. 不应暴露为稳定 API 的部分

- CLI renderer
- 终端键盘监听
- 文本阶段标题格式
- provider 原始 JSON

## 5. 未来 HTTP 资源映射

建议的 REST 资源：

- `POST /sessions`
- `POST /sessions/{id}/input`
- `POST /sessions/{id}/steer`
- `POST /sessions/{id}/interrupt`
- `GET /sessions/{id}/todo`
- `GET /sessions/{id}/tasks`
- `GET /sessions/{id}`
- `GET /sessions`
- `GET /sessions/{id}/events`

### 5.1 `POST /sessions`

请求：

- prompt
- provider
- model
- workdir
- mode

响应：

- session_id
- status

### 5.2 `POST /sessions/{id}/input`

作用：

- 对 CLI `continue` 的服务化映射
- 可追加消息并继续执行

### 5.3 `POST /sessions/{id}/steer`

作用：

- 对 active session 追加输入
- 支持 `interrupt=true`

### 5.4 `GET /sessions/{id}/todo`

响应：

- 当前 session todo 列表

### 5.5 `GET /sessions/{id}/tasks`

响应：

- 当前 session task graph
- ready / blocked / completed 派生状态

### 5.6 `GET /sessions/{id}`

响应：

- session metadata
- current state
- latest summary

### 5.7 `GET /sessions/{id}/events`

响应：

- event list
- cursor / offset（后续可加）

### 5.8 `GET /sessions/{id}/children`

响应：

- child sessions
- queued / running jobs

### 5.9 `POST /queue/jobs`

作用：

- 创建后台自治 job

### 5.10 `GET /queue/jobs`

作用：

- 查询后台自治 job 列表

## 6. Event 模型兼容性

事件对象必须尽量稳定，因为它会同时被以下消费：

- CLI `--json`
- SDK 回调
- future API 查询
- hooks

因此 v1 起就固定字段：

- `schema_version`
- `id`
- `session_id`
- `type`
- `time`
- `phase`
- `data`

新增字段允许，但不删除已有字段。

## 7. 会话持久化与 API 的关系

当前 `session.json`、`state.json`、`messages.jsonl`、`events.jsonl` 的设计，就是 future API 的底层数据源。

要求：

- 不把关键状态只留在内存里
- 不把关键输出只打印到 stdout
- compaction 不能覆盖原始消息日志

## 8. SDK / API 不变性策略

### 8.1 v1 内部承诺

- session 状态名稳定
- 主要事件类型稳定
- `finish` 完成语义稳定
- `awaiting_input` 语义稳定
- continue 不重放旧副作用稳定
- steer 默认 queue-first 语义稳定
- task graph 基本字段与依赖语义稳定

### 8.2 可演进项

- provider 流式细节
- context compaction 策略
- output renderer 样式
- tool 集合增量扩展

## 9. 未来扩展方向

### 9.1 SDK

- 外部程序内嵌 `Runner`
- 订阅事件回调
- 自定义 tool registry

### 9.2 OpenAPI 服务

- 远程启动 session
- 远程 steer
- 查询事件流
- 远程 interrupt
- 查询 todo 与 task graph

### 9.3 Webhook / SSE

建立在稳定事件模型之上，不需要重写 runtime。

## 10. 验收标准

- CLI 只作为适配层，不把核心逻辑写死在 `main`
- Runtime 能被测试代码直接调用
- Session 和 event 数据足以支撑未来 HTTP 读接口
