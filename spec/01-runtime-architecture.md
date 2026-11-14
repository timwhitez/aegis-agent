# 运行时架构

## 总体结构

~~~mermaid
flowchart LR
  U[用户] --> W[Web adapter]
  U --> C[CLI adapter]
  U --> S[Go SDK facade]
  W --> F[Runtime facade]
  C --> F
  S --> F
  F --> R[Core runner]
  R --> P[Provider adapter]
  R --> T[Tool registry]
  R --> H[Hooks and controls]
  R --> D[Session store]
  D --> E[(Local files)]
  W -. read/control .-> D
  R --> B[Event bus]
  B --> W
~~~

`internal/runtime` 是执行中心；`internal/webconsole`、`internal/app` 和 `pkg/agent` 只负责把不同调用表面接到相同的 runner、store 和事件模型上。

## 模块职责

| 模块 | 责任 | 不应承担的责任 |
| --- | --- | --- |
| `internal/runtime` | 启动、继续、steer、控制、tool loop、Goal、Plan Mode、压缩、审批、child/queue 协调 | provider wire 格式、页面布局 |
| `internal/provider` | adapter 选择、请求转换、流式响应、重试、usage 和 provider 错误 | session 路径、Web payload |
| `internal/tools` | 工具 schema、执行、路径边界、shell 输出、技能和可选 browser | 决定任务流程 |
| `internal/session` | 文件布局、锁、原子写入、消息/事件/状态/Goal/任务/审批事实 | 浏览器显示状态 |
| `internal/webconsole` | HTTP、WebSocket、Host/Origin/CSP、DTO、Web audit 和 Web 运行 handle | 另一套执行循环 |
| `internal/app` | CLI 参数、输出模式、配置准入、TTY 和 Web 监听 | 重新实现 runtime 语义 |
| `pkg/agent` | 面向 Go 调用方的核心 facade | 隐藏实验能力为稳定核心契约 |

## 一次执行

1. adapter 解析用户请求和显式覆盖项，调用配置加载器。
2. 配置加载器解析 profile、session dir、skills dir、runtime policy 和 provider options；没有配置或明确 builtin admission 时停止。
3. runner 创建或读取 session，保存启动快照和新的 run generation。
4. engine 读取 durable messages、events、state、Goal、Plan Mode、skills 和 tool schema，生成 provider 请求视图。
5. provider adapter 发起请求并把增量文本、tool call、usage、错误和 terminal outcome 转成统一事件。
6. runtime 校验 tool call，执行工具，限制输出，写入 tool result，再决定下一次 provider turn。
7. event bus 为 CLI renderer、WebSocket 和诊断输出提供观察事件；事件显示失败不能改变执行结果。
8. terminal 或 awaiting 状态写入 session。Web/CLI 后续读取相同的事实恢复页面或继续执行。

## 持久化事实

典型 session 目录包含：

~~~text
session.dir/<session-id>/
  session.json
  state.json
  messages.jsonl
  events.jsonl
  todo.json
  control/steer.jsonl
  tasks/
  artifacts/
  checkpoints/
  goal.json
  planmode.json
  contract.json
  provider-attempts.jsonl
  provider-raw/<turn>.json
~~~

功能未使用时可选文件不存在。队列工作使用 session store 管理的 `_queue` 区域。原始 transcript 和 event log 是事实；compaction、context report、UI summaries 和 previews 都是派生视图。

写入规则：

- session 与 artifact 目录默认 owner-only；
- 文件路径由安全边界校验，拒绝 symlink escape；
- 需要关联的 state、event、history、receipt 和 control 更新在同一协调边界内完成；
- provider 请求的控制身份使用 run generation、turn、request ID 和 approval receipt，不用浏览器时间或普通 `updated_at` 代替；
- 写入失败必须返回可解释错误，不能只更新内存再宣称成功。

## provider 边界

runtime 只依赖统一 adapter 契约。profile 选择 `api_provider`，具体 adapter 处理：

- endpoint 和 header；
- prompt、tool schema 和 provider-specific generation 参数；
- 流式事件和 usage presence；
- retryable transport、429/5xx 和 capability fallback；
- provider 错误与本地 session 事件的转换。

provider storage 选项由 profile/session metadata 记录；本地 session 是 Aegis 的恢复事实源。新增 provider 时，应优先扩展 adapter 和 contract tests，不在 Web 或 CLI 中加入 provider 分支。

## 控制、并发与恢复

Web 服务持有当前进程的 launch handle，用于把 start、continue、steer、interrupt、stop 与当前 owner 关联。持久化 state 仍然是跨进程可读事实；handle 不是事实源。

queue worker、child session 和 stale reaper 使用 durable job、lease、heartbeat 和 cancel state。父 session 只能观察或显式控制它们，不能用浏览器是否在线判断 child 是否完成。

恢复时先读取 session state、tool call/result、receipt 和 owner 信息，再选择：

- 继续等待当前 owner；
- 显式 stop 并释放已知 owner；
- 普通 continue，生成新的执行代；
- 对未完成的审批准备阶段恢复控制事实，但不自动重跑可能已发生的 provider side effect。

审批 receipt 的含义是“某次控制操作已准备、接纳、模糊或完成”的本地事实；它不是授权 token，也不允许把新 revision 当作旧 revision 自动批准。

## Web 与 CLI 的事实关系

Web 的 REST handler 负责 JSON、HTTP status、Host/Origin/CSP、请求大小和 DTO；runtime 负责实际控制。WebSocket 主要传递事件和实时状态，session control 使用 REST。CLI renderer 负责 text、json、stream-json，不改变 runtime 事件含义。

任何新增 Web 页面都必须能够在刷新后从 API 重建；任何 CLI 输出都必须能够从 session/runtime 事实重建。UI state、进程内 handle、poller 和 relay 都不能替代文件事实。

## 修改时的判断

- 如果变更影响运行语义，先改 runtime/session/provider contract，再接 Web/CLI。
- 如果只影响显示，优先修改 DTO 或前端派生视图，不复制 durable state。
- 如果要增加安全限制，说明它保护的具体边界，并用失败测试证明拒绝条件。
- 如果能力只适合 child、queue、isolation、TUI 或 browser profile，通过现有 `experimental`/显式配置暴露，不扩大默认 Web 首页的职责。
