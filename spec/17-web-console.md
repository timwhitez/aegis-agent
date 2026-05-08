# Go CLI Agent Web Console Spec

> 当前定位：显式 experimental 扩展面。目标是给 `go-cli-agent` 增加一个完整但本地优先的 Web 控制台，用更低的学习成本承载 session 观测、任务进度、后台队列和并行执行控制，同时不破坏 CLI-first 的 core 叙事。

## 1. 目标

Web console 解决三类问题：

- 新用户很难快速理解 `run`、`steer`、`continue`、background queue 的差异
- 复杂任务运行时，session / todo / task graph / child / queue / error 分散在多个命令和文件里，不易整体判断进度
- 当确实需要后台任务时，纯 CLI 查看成本高，缺少统一的 queue / child session 可视观测面

这次实现要提供一个完整的本地控制台，而不是只有只读页面：

- 可以创建新 session
- 可以对运行中的 session 追加 steer
- 可以对暂停或等待输入的 session 执行 continue
- 可以提交 queue job
- 可以查看 queue / children / task board / timeline / errors
- 默认界面不暴露 worker pool 调参；worker 并发仍由启动参数和后端 API 管理

## 2. 产品边界

### 2.1 明确要做

- 本地单进程 HTTP 服务
- 同进程内嵌静态前端资源
- 基于本地 session 文件事实的只读视图
- 基于 runtime / queue 的真实控制操作
- 后台 worker pool 并发执行
- 默认首页直接进入 Session 执行工作区，不再单独设置 Overview 落点

### 2.2 明确不做

- 不做 hosted Web SaaS
- 不做多租户、RBAC、数据库账号系统
- 不把 Web UI 变成权威状态源
- 不要求 provider 流式 API、SSE 或 WebSocket 才能工作
- 不在 v1 里引入浏览器端代码编辑器、文件树 IDE 或远程终端
- 当前 workspace 面板只作为“服务进程当前 cwd”的只读浏览器存在，不承诺独立的 workspace-root 切换能力
- 不把 worker pool 并发配置作为默认可见前端功能；需要时通过 `experimental web --workers` 或后端 API 调整

## 3. 设计参考与交互取舍

本控制台不直接复刻外部项目，但会吸收三类已验证的高知名度开源交互模式：

- OpenHands Local GUI
  - 采用“任务主视图 + 时间线/活动面”的思路，让用户先看到当前执行状态，再进入细节
- Open WebUI
  - 采用更低门槛的左侧导航、模型/提供商入口和友好的控制台壳层，避免首次使用时被内部实现细节淹没
- LibreChat
  - 采用稳定的列表页 + 详情页 + 右侧动作区的交互习惯，让用户能自然区分“浏览历史”“查看详情”“继续对话/发指令”

设计原则：

- 上手优先于炫技
- 状态优先于文案
- 事实优先于动画
- 控制入口必须贴近当前对象，避免用户搞不清命令作用域
- 当前实现额外吸收了“窄左栏应用壳 + 中央主工作区 + 独立右侧控制轨”的控制台布局语言，用更低的认知成本把 session 浏览、详情阅读和动作提交拆开

## 4. 信息架构

页面采用单页控制台布局。

### 4.1 顶部栏

固定展示：

- 产品标题
- session 根目录
- 新建任务按钮
- 手动刷新按钮
- 当前选中 session 的短标识与状态（若存在）

顶部栏属于全局状态条，不依赖用户先进入其他页面才能看到这些信息。
当前实现中，顶部栏保持克制：连接状态、当前 session、`New Session`、停止/中断主操作固定可见，不再塞入 KPI 数字墙。

### 4.2 左侧主导航

固定区域：

- Session
- Background Jobs
- Sessions
- Skills
- Workspace
- Settings

当前实现的左栏不再提供 Overview 入口；进入页面即是 Session 执行工作区。
session 工作区采用三栏：左侧 session rail、中间 chat/timeline、右侧 inspector panel。Tasks / Background / Timeline / Summary 这类 tracker 固定在当前 session 上下文中，而不是拆成独立总览页。
小屏幕下三栏必须按 session rail -> chat -> inspector 顺序纵向堆叠，不能因横向挤压导致输入区或 tracker 不可用。

Sessions 列表项必须展示：

- session id 短标识
- status
- provider / model
- 最近更新时间
- agent role / agent name（若存在）
- workdir 摘要，方便在多工作目录场景里快速分辨 session 上下文

### 4.3 Session 首页

Session 工作区是新用户的默认落点，展示：

- 空状态说明：从输入框开始一个 durable session
- 最近 session rail：只显示可直接打开的 session 摘要
- 中央执行流：消息、工具调用、运行态和错误都在同一条 session timeline 中出现
- 右侧 tracker：Summary / Tasks / Background / Timeline 按当前 session 聚焦展示
- Queue 只作为可选后台任务页存在，不再是首页 KPI 的一部分

### 4.4 Session 工作区

选中某个 session 后，中间主区域显示：

- 基础信息头
  - status
  - mode
  - provider / model
  - workdir
  - created / updated
  - isolation / parent / root / queue job 关联
- 顶部摘要带
  - execution / recovery / output / provider options 四类摘要卡
  - 用于快速暴露当前 turn、pending steer、loaded skills、last assistant excerpt、provider option / retry 线索
- 主标签页
  - Timeline
  - Tasks
  - Children
  - Queue Links

#### Timeline

按时间顺序展示：

- user / assistant / tool message
- harness reminder
- steer accepted / deferred
- queue claim / complete / fail
- session.failed / session.awaiting_input / session.paused / session.completed

目标是让用户在一个时间轴里同时理解对话和系统行为，而不是分散到两个页面来回跳。
当前实现额外支持 timeline search + kind filter（all/message/event），便于在长会话里快速锁定 message 或 runtime event。
后台 worker 回流到 parent session 的 `background_results` 仍以 durable message 进入 `messages.jsonl`，保持 provider replay 与文件事实源不变；WebConsole 在展示层必须把这类消息识别为后台 agent 结果卡片，展示 agent、role、status、final text / error、child session 与 queue job 链接，而不是渲染成普通用户 prompt 气泡。

#### Tasks

展示：

- todo 列表
- ready / blocked / completed task 统计
- task board 分组

#### Children

展示：

- child sessions
- child queue jobs
- agent role
- final text / last error 摘要
- 能从 child session / child queue job 卡片直接跳转到相关 session

#### Queue Links

展示：

- 当前 session 关联的 queue job
- background notification 回流状态
- steer 请求与 notification 卡片里的关键 metadata
- 能从 background notification 直接打开对应 child session

#### Background Jobs 主视图补充

- Background Jobs 视图默认是后台任务提交入口与状态计数面，不再展示 worker pool 调参、raw durable payload、完整 jobs 列表或 selected job detail
- queue job 的 status、prompt、child session、parent session、final text、last error 等细节仍可由当前 session detail、background notification 链接、后端 API 与文件事实追溯，但默认前端不强行展开为独立监控页面
- queue job 的 provider、workdir、lease owner、heartbeat、raw payload 等内部事实仍可由 API 与文件事实追溯
- queue submit 保留为“高级后台任务”入口，并用文案提示普通任务应回到 Session 执行

### 4.5 右侧动作区

动作区跟随当前选择对象变化，但默认聚焦当前 session。

当选择 session 时：

- 若 `status = running`
  - 显示 steer 输入框
  - 显示 `interrupt steer` 开关
  - 若该 session 由当前 Web server 托管，还显示 `stop` / `interrupt` 按钮
- 若 `status = awaiting_input | paused | failed`
  - 显示 continue 输入框
  - 支持 provider / model 覆盖
- 始终可显示 `queue child job` 表单

当选择 Background Jobs 时：

- 显示 queue 状态计数
- 显示“Submit Job”后台任务入口
- 不显示 worker pool 并发调节控件
- 不默认显示完整 queue jobs 列表或 selected job 详情

当前实现不再提供单独 Overview 页面，也不再把 Worker Pool 当作默认前端概念。需要配置并发时，使用启动参数或后端 API；普通用户只需要理解 Session、Sessions 和可选 Background Jobs。

## 5. 视觉系统

### 5.1 风格方向

- 风格：简洁、现代、session-first operations console
- 目标气质：工程化、可追踪、低噪声、低边框
- 不使用花哨拟物或泛 AI landing page 视觉
- 当前外观采用暖灰浅底、少量柔和阴影、克制状态色与大面积留白；容器之间主要用空间和阴影分层，不在每个 container 上堆边框

### 5.2 设计系统

基于 `ui-ux-pro-max` 的建议，本实现采用：

- pattern：session cockpit
- typography：系统字体栈 + 本地 monospace 栈，不依赖外部 font CDN
- primary accent：蓝色用于主操作、导航聚焦和运行态
- semantic success：绿色用于完成与健康状态
- neutral：暖灰用于背景、弱分割和信息层级

为避免纯黑压迫感和过密 dashboard 感，页面使用“暖浅背景 + 白色纸面 + 蓝色主强调 + 语义状态色”的本地控制台风格，而不是全黑监控墙或多边框管理后台。

### 5.3 组件要求

- 状态芯片：running / awaiting_input / paused / completed / failed / queued
- 摘要卡片：当前 session / queue 局部计数
- 时间线卡片：消息与事件混合流
- 数据表格：queue jobs、children
- 摘要卡：session health / recovery / provider options / current focus
- 过滤工具栏：session rail / queue jobs / timeline 的 search + filter
- 右侧 action panel：统一提交交互
- 左侧 session rail：支持快速扫视 status / provider / role / 最近更新时间
- mini action chips：从 queue/children/notification 等卡片直接跳到相关 session
- Worker pool 不作为默认可见组件

### 5.4 可访问性

- 所有交互目标至少 44x44
- icon-only 按钮必须有 `aria-label`
- focus ring 可见
- 颜色对比满足 WCAG AA
- 支持 `prefers-reduced-motion`
- toast / 表单错误必须通过 `aria-live` 或 `role=alert` 可被辅助技术感知
- 表单提交中的主按钮必须进入明确的 pending / disabled 状态，避免重复提交

## 6. 后端架构

Web console 通过一个新的 `WebConsoleService` 运行。

### 6.1 组成

- `StoreView`
  - 负责读取 sessions / tasks / jobs / messages / events
- `LaunchManager`
  - 负责异步启动 `Start` / `Continue`
  - 维护当前 Web server 托管的 active session handle
- `WorkerPool`
  - 托管 `N` 个后台 worker
  - 每个 worker 都通过真实 `ProcessNextJob(...)` 消费
- `HTTP API`
  - 提供 JSON 接口
- `Static UI`
  - 由 `embed` 提供资源

### 6.2 Active Session Handle

每个由 Web 发起的 session 需要单独的 handle：

- `session_id`
- `runner`
- `context cancel`
- `started_at`
- `process_start_id`
- `pid`

原因：

- 中断必须打到正确的 in-memory runner
- 多个并发 session 不能共享单个 interrupt slot
- Web server 需要知道哪些 session 是自己托管的 active run
- handle 本身不能伪持久化；只能把 `webconsole.handle.acquired/released` 这类 owner/process 线索写入 `events.jsonl`，供恢复诊断、`session.md` 与 long-run checkpoint 使用

### 6.3 Worker Pool

worker pool 允许并发 `N >= 1`。

约束：

- worker 之间共享同一 queue 目录
- claim 依赖 `queued -> running` 原子 rename
- 一个 worker 只处理一个 job
- 任意 job 的完成、失败都必须真实创建/更新 child session 与 queue job 记录

## 7. API 设计

所有 API 默认返回 JSON。

控制面约束：

- `POST /api/sessions/start`、`POST /api/sessions/{id}/continue`、`POST /api/sessions/{id}/steer`、`POST /api/sessions/{id}/interrupt`、`POST /api/sessions/{id}/stop` 是 session 控制的唯一入口。
- `/ws` 只作为连接状态与可选事件 relay 通道；不得启动、恢复、steer、interrupt 或 stop session。
- `/ws` 收到历史 `{"type":"chat"}` 或 `{"type":"stop"}` 控制消息时必须返回 `WEBSOCKET_CONTROL_DEPRECATED`，且不得创建、继续或修改 session。
- 后端请求 DTO 使用命名结构体维护；错误响应统一为 `{"error","code","detail","action"}`，其中 `code/detail/action` 可为空但对 `UNKNOWN_PROVIDER`、`ACTIVE_HANDLE_NOT_OWNED`、`SESSION_NOT_RESUMABLE`、`WEBSOCKET_CONTROL_DEPRECATED` 必须稳定。
- 前端 REST payload 构造、统一错误解析和控制面 wrapper 集中在 `api.js`；`app.js` 不应继续手写 WebSocket session-control payload。
- 当 listen 地址不是 loopback 时，启动输出必须明确提示本地 WebConsole 可写配置与 `.env` API key、删除 session、管理 skill、读取 workspace 文件；`run.sh` 的默认 `0.0.0.0:3940` 为 WSL 便利保留，但也必须输出同类提示。
- 配置写入、API key 写入、session 删除/清理、skill 安装/卸载必须写入可检索审计事件；API key 事件只记录 env key 与路径，不记录 secret 值。

### 7.1 `GET /api/meta`

返回：

- server version
- session root
- worker count
- providers
- experimental capabilities

### 7.2 `GET /api/overview`

返回供 Session rail 和后台摘要复用的聚合数据；当前前端不再提供独立 Overview 页面。

- session counters by status
- queue counters by status
- active worker count
- recent sessions
- recent jobs

### 7.3 `GET /api/sessions`

参数：

- `limit`

返回最近 sessions。

### 7.4 `GET /api/sessions/{id}`

返回：

- metadata
- state
- latest task board
- contract snapshot
- required artifact tracker
- provider attempts tail
- long-run checkpoint
- parent coordination state
- recent messages
- recent events
- children summary
- linked jobs
- active handle status（是否被当前 server 托管）
- `active_handle_owner`：区分 `current_process`、`running_not_owned`、`settled`，并暴露最近的 `process_start_id`、`pid`、`started_at`、`released_at` 线索

### 7.5 `POST /api/sessions/start`

输入：

- `prompt`
- `agent_name?`
- `agent_role?`
- `provider?`
- `model?`
- `workdir?`
- `mode?`
- `system?`
- `isolation_mode?`
- `isolation_root?`

行为：

- 异步启动一个新 session
- 立即返回 `202`
- 返回 `session_id`

### 7.6 `POST /api/sessions/{id}/continue`

输入：

- `message?`
- `provider?`
- `model?`
- `system?`

行为：

- 异步恢复该 session
- 立即返回 `202`

### 7.7 `POST /api/sessions/{id}/steer`

输入：

- `message`
- `interrupt`

行为：

- 复用现有 steer 契约
- 写入 `control/steer.jsonl`
- `source = web`

### 7.8 `POST /api/sessions/{id}/interrupt`

行为：

- 若 session 由当前 Web server 托管并仍在运行，则调用其 active runner interrupt
- 否则返回可理解的错误，提示改用 `steer + interrupt`

### 7.9 `POST /api/sessions/{id}/stop`

行为：

- 若 session 由当前 Web server 托管并仍在运行，则调用其 active runner interrupt，并把 pause reason 写为 `manual_stop`
- 否则返回可理解的错误，提示该 session 可能已结算

### 7.10 `GET /api/sessions/{id}/children`

返回：

- child sessions
- child jobs

### 7.11 `GET /api/sessions/{id}/tasks`

返回：

- task board

### 7.11 `GET /api/queue/jobs`

参数：

- `limit`

返回 queue jobs。

### 7.12 `GET /api/queue/jobs/{id}`

返回单个 job。

### 7.13 `POST /api/queue/jobs`

输入：

- `prompt`
- `parent_session_id?`
- `agent_name?`
- `agent_role?`
- `provider?`
- `model?`
- `workdir?`
- `system?`
- `mode?`
- `wait_mode?` (`wait-all` | `wait-any`)
- `isolation_mode?`
- `isolation_root?`

说明：

- 未显式提供 `provider` / `model` 或传入 `default` 时，child 默认继承 parent session 的 provider / model
- `mode=full-auto` 作为兼容别名按 `exec` 处理
- `isolation_mode=workspace-write` 作为兼容别名按 `off` 处理

### 7.14 `GET /api/workers`

返回：

- desired worker count
- active worker count
- poll interval
- last processed jobs

### 7.15 `POST /api/workers`

输入：

- `desired_count`

行为：

- 动态调整 worker pool 并发数
- 该接口保留给高级/测试入口，默认前端不展示 worker pool 调参控件

## 8. 交互状态机

### 8.1 新建任务

用户路径：

1. 填写 prompt
2. 可选填写 `agent_name` / `agent_role`
3. 选择 provider / model / mode
4. 选择是否 isolation
5. 点击 Start
6. UI 立即切换到该 session 详情页
7. Timeline 开始轮询刷新

### 8.2 运行中追加输入

用户路径：

1. 选中 running session
2. 在右侧输入新要求
3. 可选勾选 interrupt
4. 提交后在 timeline 里看到 `session.steer.queued`
5. 接纳后看到对应 user message

### 8.3 等待输入后恢复

用户路径：

1. 选中 `awaiting_input` / `paused` / `failed` session
2. 输入 continue message
3. 点击 Continue
4. UI 显示 `resuming`
5. session 重新进入 running

### 8.4 后台并行任务

用户路径：

1. 打开 Background Jobs 面板
2. 提交一个或多个 jobs
3. 通过状态计数、当前 session inspector 的 background 卡片、background notification 链接、API 或文件事实观察 queued -> running -> completed/failed
4. 若 job 属于 parent session，则在该 parent timeline 里看到 background notification
5. 若需要调整并发，重启 Web 服务时修改 `--workers` 或调用后端 worker API，不从默认页面直接配置

## 9. 刷新与实时策略

当前实现采用 polling-first：

- session rail summary / queue：2 秒
- session detail：1.5 秒
- actions 提交成功后立即触发一次本地 refresh

原因：

- 兼容当前 CLI-first / file-fact 架构
- 便于测试
- 不强依赖 SSE / WebSocket

## 10. 错误处理

错误必须区分三类：

- 用户输入错误
  - 例如空 prompt、空 steer message
- 运行时错误
  - 例如 provider 缺 API key、session 不可恢复、job 失败
- 基础设施错误
  - 例如 session root 不可写、queue claim 失败、配置解析失败

前端展示要求：

- 顶部 toast 用于一次性反馈
- 详情页内联错误用于对象级失败
- queue/job/session 失败状态必须保留可见错误摘要
- Markdown 渲染必须经过本地 sanitizer，不允许依赖外部 `marked` CDN，也不能直接注入未净化 HTML
- skills upload / uninstall、settings 保存、WebSocket 消息解析都必须显示后端错误或 malformed payload 错误，不能假成功或静默失败

## 11. 测试要求

自动测试至少覆盖：

- API 路由
- embedded shell 与 `embed` 静态资产可直接加载
- 前端静态资源不依赖外部 CDN，icons 和 Markdown renderer 均为本地实现
- start / continue 异步返回
- steer 写入与 `source=web`
- overview API 聚合仍可用作 session rail 数据源
- worker pool API 缩放
- queue job 从提交到完成的 happy path
- role-aware start form 可显式传递 `agent_name` / `agent_role`
- queue job 失败的持久化与可见性
- queue completion 与 stale-running reconcile 重叠时，background notification 仍按 `queue_job_id` 去重
- session detail 能暴露 contract、required artifacts、provider attempts、long-run checkpoint 和 parent coordination
- skills upload / uninstall 和 settings save 失败时必须使用真实后端错误反馈，并恢复按钮 pending 状态
- WebSocket malformed payload 不得造成全局 runtime exception
- focused retry-resume live rerun 需要同时验证 durable retry metadata 未漂移，以及真实 `provider.retry` 事件出现
- 若 retry proof 已经拿到上述 durable evidence，而 bounded finish nudges 后 session 仍为 `awaiting_input`，应将其记为 non-blocking completion quirk，而不是把整轮 webconsole follow-up 判成失败
- headless browser UI smoke 覆盖 start、role-aware session chrome、session rail、timeline event filter、tasks/background/timeline tab 切换、continue、queue submit、queue 视图、queue-links 通知与 manual refresh；worker API 缩放保留为服务层测试，不作为默认页面交互
- 浏览器侧 `runtime exception` 与 `console error` 为空

手工验证至少覆盖：

- 新建 session
- 运行中 steer
- awaiting_input continue
- 提交多个 queue job 并观察并发消费
- 页面响应式、截图与 queue-links 通知可见性

## 12. 验收标准

- `experimental web` 能稳定启动本地控制台
- embedded shell 与前端 assets 能由同一进程本地服务直接提供
- 页面可在无外部网络资源时加载；缺失 CDN 不得导致 `lucide is not defined` 或 `marked is not defined`
- 用户无需记忆 CLI 全命令，也能完成 session 启动、追加输入、继续执行和后台排队
- queue worker pool 支持真实并发消费，但默认 UI 不暴露 Worker Pool 配置卡
- retry-resume proof 以 durable retry metadata 加真实 `provider.retry` 事件作为主要通过条件；若 proof 已成立，session 是否最终落成 `completed` 只作为附带运行状态记录
- 浏览器可以完成核心交互链且无前端运行时错误
- 页面能清晰展示 session、tasks、queue、children、errors 的最新状态
- 所有页面状态都来自本地文件事实与 runtime 真执行，而不是前端假数据
