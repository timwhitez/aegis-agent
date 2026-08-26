# Aegis Agent Web-First Console Spec

> 当前定位：Web-first 默认入口。目标是给 `aegis-agent` 提供一个完整但本地优先的 Web 控制台，用更低的学习成本承载 session 观测、任务进度、后台队列和并行执行控制，同时不破坏 runtime / session / provider 的文件事实源边界。CLI 保留为稳定 fallback、脚本化和故障恢复入口。

## 1. 目标

Web console 解决三类问题：

- 新用户很难快速理解 `run`、`steer`、`continue`、background queue 的差异
- 复杂任务运行时，session / todo / task graph / child / queue / error 分散在多个命令和文件里，不易整体判断进度
- 当确实需要后台任务时，纯 CLI 查看成本高，缺少统一的 queue / child session 可视观测面

Web-first v1 要提供一个完整的本地控制台，而不是只有只读页面：

- 可以创建新 session
- 可以在创建 session 时通过一个 optional Goal 开关附带 prompt-derived goal
- 可以在创建或继续 session 时通过 Plan 开关进入 Plan Mode，并在 inspector 中审批、修订、取消或回答规划问题
- 默认启动表单不要求也不展示 agent name / agent role；role hints 仍由 agent 内部 mission plan、child/queue 高级 API 或 CLI/API advanced payload 显式传递
- 可以对运行中的 session 追加 steer
- 可以对暂停、等待输入、失败的 session 或已完成的 root session 执行 continue；completed child / queue session 只能从 parent 使用 `agent_prompt` 的可恢复路径，或重新提交 queue job
- 可以查看 children / task board / timeline / errors；background queue 只通过已接纳的
  durable message 或有界关联摘要出现，不提供独立 tracker
- 默认界面不暴露 worker pool 调参；worker 并发仍由启动参数和后端 API 管理
- 默认交互要简洁：高频路径少确认，agent 在明确安全边界内拥有较大执行权限；只有覆盖 validation coverage、删除/清理、写配置/API key、暴露非 loopback 服务等风险动作需要显式确认
- 默认语言为简体中文，可在全局入口切换 English 并跨刷新保留；默认主题固定为浅色

## 2. 产品边界

根 `README.md` 应把 `aegis-agent web` 作为默认启动入口，并保留 CLI fallback 与 LAN 安全提示；页面结构、UX 细节、API 契约与浏览器验收口径以本文档为准。`aegis-agent experimental web` 只作为旧入口兼容别名保留。

### 2.1 明确要做

- 本地单进程 HTTP 服务
- 同进程内嵌静态前端资源
- 基于本地 session 文件事实的只读视图
- 基于 runtime / queue 的真实控制操作
- 后台 worker pool 并发执行
- 默认首页直接进入 Session 执行工作区，不再单独设置独立总览落点

### 2.2 明确不做

- 不做 hosted Web SaaS
- 不做多租户、RBAC、数据库账号系统
- 不把 Web UI 变成权威状态源
- 不要求 provider 流式 API、SSE 或 WebSocket 才能工作
- 不在 v1 里引入浏览器端代码编辑器、文件树 IDE 或远程终端
- 当前 workspace 面板只作为“服务进程当前 cwd”的受限本地文件管理器存在，不承诺独立的 workspace-root 切换能力；它可以浏览、预览、下载和上传文件，在同一目录内重命名普通文件，并在默认 `workspace/` 根内创建文件夹、选择多个可见文件或文件夹并批量删除
- 不把 worker pool 并发配置作为默认可见前端功能；需要时通过 `aegis-agent web --workers`、兼容的 `experimental web --workers` 或后端 API 调整
- 不把普通 start / steer / continue / Plan approve 设计成多步确认向导；用户明确提交后应直接执行，风险动作才确认

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
- 交互默认遵循“授权一次，执行到底”：用户在 Session 工作区给出 prompt / steer / continue 后，runtime 按工具 guard、Plan Mode gate、Goal completion audit 和 workspace safety 执行，不要求用户为每个普通 agent 决策反复确认

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
- Sessions
- Skills
- Workspace
- Settings

当前实现的左栏不再提供独立总览入口；进入页面即是 Session 执行工作区。
session 工作区以单列执行流为默认前端面，重点展示 chat/timeline、工具 lane、session 状态与基础控制。Task / queue / background 的权威事实仍来自 session store、queue store、runtime facade 与本地文件，但 Queue / Background 不再作为默认前端 tracker 或 inspector 展示。
小屏幕下输入区和执行流必须保持可用，不能因隐藏高级 tracker 而遮挡消息或控制按钮。

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
- 输入区提供一个简单 Goal 开关；普通 prompt 不受影响，选中后用户仍只写 prompt，后端用 prompt 作为 objective，agent 在运行中自行拆分 criteria、validation、features / milestones
- 输入区提供一个简单 Plan 开关；选中后后端创建 `planmode.json`，规划阶段只允许 read/search、`request_user_input` 和 `submit_plan`，不把普通 prompt 文案自动解释为硬 Plan Mode
- 输入区不展示 agent name 或 agent role 选择器；默认 session 由 agent 自行决定是否需要后续 role hints / child / queue 委派
- 最近 session rail：只显示可直接打开的 session 摘要
- 中央执行流：消息、工具调用、运行态和错误都在同一条 session timeline 中出现
- 当前 session 的执行流：消息、工具调用、运行态、错误和 timeline 摘要在同一主视图中出现
- Queue / child / background 只保留为后端 API、CLI fallback、service tests 与文件事实源能力；默认前端不再提供 Background / Queue tracker、Open job 或 selected job facts 面板

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
  - Queue Links（API / CLI / 文件事实源，不作为默认前端 tab）

#### Timeline

按时间顺序展示：

- user / assistant / tool message
- harness reminder
- steer accepted / deferred
- queue claim / complete / fail
- session.failed / session.awaiting_input / session.paused / session.completed

目标是让用户在一个时间轴里同时理解对话和系统行为，而不是分散到两个页面来回跳。
当前实现额外支持 timeline search + kind filter（all/message/event），便于在长会话里快速锁定 message 或 runtime event。
当 session detail 只返回消息尾部窗口时，Session 工作区必须通过 `GET /api/sessions/{id}/messages?before_id=...&limit=...` 支持加载更早消息；前端加载过的历史消息不能被后续 polling 刷新丢弃，且 message stream 与 timeline 视图应按 message id 去重合并。
assistant thinking summary 作为消息内折叠块展示；provider-native replay facts（例如 signature / thoughtSignature）只随 session message 数据保存，不在 UI 中解释或手工编辑。
assistant tool call 与紧随其后的 matching tool result 虽然在 `messages.jsonl` 中保持独立消息以满足 provider replay，但 WebConsole message stream 应在展示层按 `tool_call_id` 合并为同一个 tool lane；`finish` 的 final message 只能作为用户可见回复展示一次，raw call/result 细节保留在可展开详情中，避免重复输出同时不丢失审计信息。
后台 worker 回流到 parent session 的 `background_results` 仍以 durable message 进入 `messages.jsonl`，保持 provider replay 与文件事实源不变。默认前端不再要求渲染 Background / Queue 专用卡片；queue job、child session、final text / error 与 parent notification 通过 API、CLI 和文件事实源追溯。

#### Tasks

展示：

- todo 列表
- ready / blocked / completed / cancelled / done task 统计
- task board 分组

Todo 的 `in progress` 数字必须直接统计 todo snapshot 中的 `in_progress` 条目；task board 必须按 runtime 派生的 `in_progress / ready / blocked / completed / cancelled` groups 展示，不能用 persistent task counter 冒充 Todo 进度或只画平铺列表。

### 4.5 Language, accessibility, and interaction baseline

- operator-owned 静态/动态文案、toast/dialog、ARIA、placeholder/title、日期和数字必须覆盖 `zh-CN` 与 `en`；默认 `zh-CN`
- local preference 只保存在浏览器中；用户消息、tool/provider 原文、文件内容、路径和 durable session facts 保持原样
- 高频 button/tab target 至少 `44×44 CSS px`；tabs 提供 `tablist/tab/tabpanel` 语义与键盘激活
- modal 与移动 inspector 必须 trap focus、Esc 关闭、恢复触发焦点，并让背景不可交互
- 清空历史在任何 queued/running/blocked queue work 或 running session 未收敛时返回 conflict；reaper 状态迁移不能产生允许清理的窗口

#### Children

展示：

- child sessions
- child queue jobs
- agent role
- final text / last error 摘要
- 能从 child session / child queue job 卡片直接跳转到相关 session

#### Queue Links

当前口径：

- Queue Links 不再是默认前端 tab 或 inspector 面板
- 当前 session 关联的 queue job、background notification 回流状态、steer 请求与 notification metadata 仍必须可由 REST API、CLI fallback 与 session/queue 文件事实源读取
- 浏览器 smoke 不再要求从 notification 打开 queue job 或 child session 链接

#### Background Job Facts

- 默认前端不再提供独立 Background Jobs tab、Background inspector、`Open job` 入口或 selected job facts panel
- session 新建后仍可以通过 API / CLI 提交后台 queue job，后台 child / queue 结果必须回写 durable session / queue 文件事实源
- queue job 的 provider、workdir、lease owner、heartbeat、raw payload 等内部事实仍可由 API 与文件事实追溯
- `delivery_status=pending` 的 background notification 表示 parent session 尚未继续消费该 child / queue 结果，不应被 UI 表达成 child 仍在运行
- 独立 queue submit 保留在 REST API 和 CLI advanced/experimental 面；默认 Web UI 不再提供单独 submit form，普通任务应回到 Session 执行

#### Goal

展示：

- objective、status、usage
- success criteria 与 validation plan
- completion audit evidence 与 summary
- agent 拆分出的 plan status、features、milestones
- validation coverage summary、latest goal history event、progress / handoff records、evaluator / child / queue linked facts
- goal 相关 events
- 用户控制动作：pause、resume、clear、complete、approve goal plan

约束：

- WebConsole 只是读写 `goal.json` 与 `artifacts/goal-history.jsonl` 的本地控制面，不维护第二套 goal 状态
- complete 是用户控制动作；模型完成目标仍必须通过 `update_goal(status="complete")` 工具留下完成审计路径
- approve goal plan 若存在 linked pending Plan Mode，必须走 Plan Mode approval / continue 路径；不能只把 mission plan status 改成 approved
- approve goal plan 默认受 validation coverage checker 约束；未覆盖或 invalid contract 返回 conflict，只有请求带 explicit override 时才能继续
- Goal plan 展示不能暗示 runtime 会自动拆 DAG 或强制 child agent；child / queue 使用仍由模型或用户显式决定
- features / milestones 可展示已存在的 `task_ids`、`child_session_ids`、`queue_job_ids`，其中 `create_tasks_from_plan` 只作为高级显式开关创建 durable task，不自动 spawn child、不提交 queue job、不生成固定 DAG

#### Plan Mode

展示：

- objective、status、plan version、approved version
- `request_user_input` pending questions，以及后端自动添加的 free-form Other 入口
- submitted plan summary、assumptions、risks、verification 和完整 Markdown plan
- 用户控制动作：Approve & Run、Ask for Changes、Cancel

约束：

- WebConsole 只读写 `planmode.json`、`artifacts/planmode-history.jsonl` 和 `artifacts/planmode-plan.md`，不维护第二套 Plan 状态
- Approve & Run 必须走 runtime continue path，追加 `meta.source=planmode_approval` 的 user message 后恢复普通执行
- Ask for Changes 只是 plan revision user message；不会执行 plan，也不会变成 Todo/Task 写入。若该动作来自 modal inspector，前端必须先关闭 inspector、解除背景 `inert`，再把焦点移到 composer；不能把输入焦点留在仍打开的 modal 背后
- Pending Plan Mode 下，带当前 session `parent_session_id` 的 Web queue submit / delegate 类控制面必须被拒绝；无 parent 的独立 queue job 不受影响

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
- 不显示 Background inspector；child sessions、queue jobs、background notifications 和 selected job facts 通过 API / CLI / 文件事实源验证

当前实现不再提供单独总览页面、独立 Background Jobs tab、Background inspector，也不再把 worker 并发调参当作默认前端概念。需要配置并发或追踪 queue job 时，使用启动参数、后端 API、CLI 或本地文件事实源；普通用户只需要理解 Session、Sessions 与当前 session 执行流。

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

为避免纯黑压迫感和过密监控墙感，页面使用“暖浅背景 + 白色纸面 + 蓝色主强调 + 语义状态色”的本地控制台风格，而不是全黑监控墙或多边框管理后台。

### 5.3 组件要求

- 状态芯片：running / awaiting_input / paused / completed / failed / queued
- 摘要卡片：当前 session / queue 局部计数
- 时间线卡片：消息与事件混合流
- 数据表格：children；queue jobs 只属于 API/CLI/文件事实验证面
- 摘要卡：session health / recovery / provider options / current focus
- 过滤工具栏：session rail / timeline 的 search + filter
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
  - 负责读取 sessions / goals / tasks / jobs / messages / events
- `LaunchManager`
  - 负责异步启动 `Start` / `Continue`
  - 维护当前 Web server 托管的 active session handle
- `WorkerPool`
  - 托管 `N` 个后台 worker
  - 每个 worker 都通过真实 `ProcessNextJob(...)` 消费
- `HTTP API`
  - 提供 JSON 接口，包括 goal 的本地控制面
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
- Web 读取当前进程 handle 前必须先按 durable terminal state 清理 stale handle；`failed` / `completed` 的遗留 handle 不能阻断 stop / interrupt 所有权判断、Plan Mode recovery continue、删除/清理或其他可恢复控制路径
- 当 durable state 仍为 `running`、当前 Web 进程没有 active handle、最近 `webconsole.handle.acquired` 所属 owner 进程可由本机事实证明已经不存在时，Web 控制面可以把该 session 作为 orphaned running 收敛为 `paused`，并追加 `webconsole.handle.released` 与 `session.paused` 恢复事件；这只用于 stop / delete / recovery path，不能把 in-memory cancel handle 伪持久化，也不能覆盖当前进程仍持有的 active handle。

### 6.3 Worker 并发池

worker pool 允许并发 `N >= 0`。`0` 表示无 worker 的观察/测试模式；后端必须设置保守上限（当前实现为 `8`）并在 worker snapshot 中暴露 `max_count`。

约束：

- worker 之间共享同一 queue 目录
- claim 依赖 `queued -> running` 原子 rename
- 一个 worker 只处理一个 job
- 任意 job 的完成、失败都必须真实创建/更新 child session 与 queue job 记录

## 7. API 设计

所有 API 默认返回 JSON。

WebConsole 可选 `web.basic_auth` adapter guard：同时配置 `username` 与 bcrypt `password_hash` 后，所有 UI asset、REST API 与 WebSocket upgrade 均先挑战 `WWW-Authenticate: Basic`，认证失败返回 `401` 且不得进入路由或 service state。该认证属于 Web app/service adapter 层，不读取或改写 session/runtime 文件事实；配置不完整或 bcrypt hash 无效时 service 启动失败。Basic Auth 只在 HTTPS 传输层后适合不可信网络暴露。

控制面约束：

- 所有 unsafe `/api/` mutation 必须有轻量 local-console guard：foreign `Origin` 拒绝；缺少 `Origin` 时要求本地控制台自定义 header `X-Aegis-Agent-Web: 1`；JSON mutation endpoint 必须要求 `Content-Type: application/json` 且有请求体大小上限；optional JSON mutation endpoint 可接受真正空 body（包括未知长度的空 body），但一旦 body 含有非空内容仍必须按 JSON Content-Type 和单 JSON 值校验；multipart skill/workspace upload 保持表单入口但仍受 header、请求体上限与 path/root 校验约束。
- `POST /api/sessions/start`、`POST /api/sessions/{id}/continue`、`POST /api/sessions/{id}/steer`、`POST /api/sessions/{id}/interrupt`、`POST /api/sessions/{id}/stop` 是 session 控制的唯一入口。
- goal 控制只通过 REST endpoint 写入 session store，不通过 WebSocket 控制消息。
- `/ws` 只作为连接状态与可选事件 relay 通道；不得启动、恢复、steer、interrupt 或 stop session；浏览器 WebSocket upgrade 必须拒绝 foreign `Origin`，无 `Origin` 的本地非浏览器 client 可继续用于测试/诊断。
- `/ws` 收到历史 `{"type":"chat"}` 或 `{"type":"stop"}` 控制消息时必须返回 `WEBSOCKET_CONTROL_DEPRECATED`，且不得创建、继续或修改 session。
- 后端请求 DTO 使用命名结构体维护；错误响应统一为 `{"error","code","detail","action"}`，其中 `code/detail/action` 可为空但对 `UNKNOWN_PROVIDER`、`ACTIVE_HANDLE_NOT_OWNED`、`SESSION_NOT_RESUMABLE`、`WEBSOCKET_CONTROL_DEPRECATED` 必须稳定。
- 前端 REST payload 构造、统一错误解析和控制面 wrapper 集中在 `api.js`；`app.js` 不应继续手写 WebSocket session-control payload。
- timeline/event descriptor、event refresh filter 和 live-activity event promotion helper 集中在 `events.js`；`app.js` 只调用这些 helper，不重复维护事件文案映射。
- Settings view 的 render 与 save handler 集中在 `settings-view.js`；`app.js` 只负责视图切换时调用 `renderSettings()`。
- Workspace browser render、path normalization 和 file/directory loading helper 集中在 `workspace-view.js`；`app.js` 只负责视图切换时调用 `fetchWorkspace()`。
- Workspace 当前浏览目录是 localStorage 中的纯 UI preference，按 `workspace_root + selected session id` 分桶；new-session composer 使用独立 bucket。普通页面切换、polling 和同一 session detail 刷新只能刷新当前目录，不能把手动浏览路径重新覆盖为 session workdir。首次选中 session、切换 session 或同一 session 的 metadata workdir 真实变化时，`syncWorkspaceToCurrentSession()` 才同步初始目录；已有 bucket preference 优先用于浏览器刷新恢复。持久化路径失效时逐级回退到最近可访问父目录，最后回到 workspace 根，并只提示一次。该 preference 不是 session 执行状态，start/continue 提交的 workdir 仍由后端做 workspace safety 校验。
- Workspace browser 可保留 workspace 的父级导航，但必须隐藏并拒绝读取或下载 `.env`、`.env.*`（示例/模板除外）、`.envrc`、SSH / cloud / kube / docker 凭据目录、private-key 文件名，以及 `credentials` / `client_secret` / `service_account` 这类 credential-like 路径；这属于本地控制台泄露防护，不是对 session/report 内容的默认脱敏。
- Workspace 上传、文件重命名、创建文件夹、删除文件和删除文件夹属于本地控制台风险动作：必须复用 unsafe API guard 和审计事件；写操作只能作用于默认 `workspace/` 根内的非敏感路径，不能把父级导航扩展成修改服务 cwd 或仓库元数据的入口。上传采用 multipart 单文件请求，文件内容和整个请求都有硬上限，目标已存在时拒绝覆盖，并通过同目录临时文件 + no-replace rename 原子发布。文件重命名只允许普通文件在原目录内改名，拒绝覆盖、目录移动、symlink、敏感源/目标名与越界路径。批量删除必须先完成所有选中路径的安全预检，再执行删除事务；任一选中路径越界、指向敏感路径、为 symlink 或其目录子树包含敏感路径时，整个批量删除失败且不得删除其他已选项。
- Session workspace 的 rail、message/timeline stream、tasks/children/background cards 与 inspector render helper 集中在 `session-view.js`；`app.js` 只负责状态、polling、routing 与调用 `renderCurrentSession()`。
- parent detail 在 child 创建期间读取 children 时必须容忍 `session.json` 已发布而 `state.json` 尚未发布的短暂窗口；固定 inspector 隐藏的 compact layout 仍必须把同一 inspector HTML 渲染到 slide-out，不能打开空面板。
- 当 listen 地址不是 loopback 时，启动输出必须明确提示本地 WebConsole 可写配置与 `.env` API key、删除 session、管理 skill、读取/下载/上传/重命名 workspace 文件，以及在 workspace 内创建文件夹或删除单个/多个文件夹和文件；`run.sh` 的默认 `0.0.0.0:3940` 为 WSL 便利保留，但也必须输出同类提示。
- 配置写入、API key 写入、session 删除/清理、skill 安装/卸载必须写入可检索审计事件；API key 事件只记录操作元数据、env key 与路径，不采集 secret 值。skill upload 必须有请求体、zip entry 数量、单 entry 解压大小和总解压大小上限，避免本地控制台被 zip bomb 或超大 multipart 请求拖垮。
- 单个 session tree 删除必须先完成 active handle、stale running owner、running session/job 与 audit writability 检查；审计事件写入成功后直接调用 session store 删除目标 session tree 和 linked queue jobs。不要在请求路径上先把整棵 session tree 搬到历史备份目录再删除，因为大型 master/child 会话会让删除响应被大目录 I/O 阻塞。全量 clear history 仍可使用 history mutation transaction 提供 audit 失败回滚。
- Settings 必须用 provider-specific 下拉选择暴露 Provider Profile、API Provider / Adapter Family、reasoning / thinking mode 与 reasoning summary：OpenAI / `openai-compatible` 支持 `default | low | medium | high | xhigh` 和 summary `default | auto | concise | detailed | off`，Anthropic-compatible / Google 支持 `default | standard | max | off`；`max` 映射到 thinking budget profile，不能要求用户手写 token budget。
- `POST /api/config/test` 使用当前 Settings 表单值执行一次 thinking-observation probe，用于确认 provider、model、base URL、API key、API Provider 与 reasoning / thinking 配置能被上游接受，并区分“请求成功”和“本次实际返回可读 thinking / summary”；该接口不得持久化 config 或 `.env`。

Settings API：

- `GET /api/config` 返回当前默认 provider、guardrails、全局 soft/hard turn guard、`child_budget { disabled, max_active_runtime_sec, max_elapsed_sec, max_turns_per_attempt, active_runtime_checkpoint_ms }`，以及每个 provider 的 `api_provider`、`effective_api_provider`、`base_url`、`model`、`has_key`、reasoning/thinking 字段
- `POST /api/config` 保存 provider 默认值、guardrails、全局 soft/hard turn guard、optional child budget 和 provider options，并写入审计事件；`child_budget.disabled=true` 持久化为三个维度全 `0`，启用时各维度必须非负且至少一个为正数。API 在兼容窗口内接受旧 `max_wall_clock_sec` / `max_turns`，新响应和新写配置使用 canonical 字段
- Settings 将 Global Turn Guard 与 Sub-agent Budget 分组展示。hard limit 明确说明适用于 master、foreground child 与 background/queue child，并按每次 run 计数；soft 只做一次 reminder。Sub-agent Budget 的 active runtime、absolute elapsed deadline、per-attempt turns 分开显示，并明确修改只影响新 child/job
- duration 输入接受并回显人类可读格式（例如 `30m`、`2h`），API/config 内部规范化为秒。Sub-agent Budget 不进入 provider test payload，也不在普通 session start / `agent_spawn` 表单增加逐 child budget字段
- Session detail 的只读 budget inspector 从 child `metadata.effective_budget` / linked job snapshot 展示 configured/effective/used/remaining、attempt、policy source 与 last reason；active-runtime 还展示最近 durable checkpoint/heartbeat、lease 是否 open，以及最近一次 bounded crash-recovery charge。parent notification 提示 inspect、extend/resume 或 cancel/settle
- `POST /api/config/test` 接收同一 provider 表单子集，执行 probe 后返回 `success`、`provider`、`api_provider`、`effective_api_provider`、`model`、`reasoning_mode`、`reasoning_summary`、`thinking_visible_observed`、`thinking_replay_observed`、`reasoning_summary_observed`、`reasoning_encrypted_observed`、`reasoning_tokens`、`thinking_strategy`、`thinking_detail` 与实际 provider option 摘要

### 7.1 `GET /api/meta`

返回：

- server version
- session root
- worker count
- providers
- experimental capabilities

### 7.2 `GET /api/overview`

返回供 Session rail 和后台摘要复用的聚合数据；当前前端不再提供独立总览页面。

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
- `goal?`：当前 `goal.json` snapshot，缺失时为空
- `plan_mode?`：当前 `planmode.json` snapshot，缺失时为空

#### 7.4.1 `GET /api/sessions/{id}/context`

返回从 canonical session/message/event 文件即时派生的 versioned `ContextReport`。该 endpoint：

- 只读，不写回 session，不维护第二套 telemetry 状态
- 保留完整 aggregate，但 session/request detail 共用 64 项预算；发生截断时返回 omitted session/request 计数
- 只在用户打开 Session inspector 的 Context tab 或点击 Refresh 时请求；普通 session polling 与默认首页不主动加载
- 只返回尺寸、计数、ID、状态、时间和 provider 已报告的 usage，不复制 prompt/tool schema/metadata value/error/tool output/raw provider payload

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
- `goal?`
  - `enabled`
  - Web 默认只发送 `enabled:true`；后端把 prompt 作为 objective，并默认使用统一 Goal 模式
  - REST/CLI 高级调用仍可传 `objective`、criteria、validation 或内部计划字段用于自动化和兼容
- `plan_mode?`
  - `enabled`
  - `objective?`
  - Web 默认只发送 `enabled:true`；后端把 prompt 作为 Plan Mode objective

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
- `plan_mode?`：可在 resumable session 上显式进入新一轮 Plan Mode

行为：

- 异步恢复该 session
- 可恢复状态为 `paused` / `awaiting_input` / `failed`，以及 root session 的 `completed`；completed root 补充 `message` 后按普通 continue 延续既有历史，并写入 `session.resumed.data.resumed_from=completed`
- completed child / queue session 返回结构化 `SESSION_NOT_RESUMABLE`，action 指向 parent `agent_prompt` 或重新提交 queue job；Web 不得只把 child state 改成 running
- completed root 的旧 Goal 默认保持 historical complete：Goal inspector 在 session follow-up 运行期间明确显示 historical completed Goal，普通 continue 不自动恢复 Goal 或重新开启旧 Goal completion gate
- 立即返回 `202`
- 若当前 `planmode.status=awaiting_approval` 且传入普通 `message`，后端将其作为 plan revision user message，而不是执行计划

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
- 若当前 Web server 同时托管该 session tree 下的 running child / queue child active handle，同一次 stop 也必须 best-effort 对这些 descendant session 请求 `manual_stop`，避免 parent 已暂停但子任务仍继续运行
- 若 session durable state 仍为 `running`，但 active handle 属于已退出的旧 Web 进程，则写入可审计恢复事件并将 session 收敛为 `paused` / `manual_stop`，使用户可以继续或删除该 session
- orphan stop 恢复完成后，之前由 Web stop fallback 产生且尚未被 runner 接纳的 pending interrupt stop steer 必须标记为 `rejected`，避免后续 `continue` 再次消费旧的 stop 请求
- 否则返回可理解的错误，提示该 session 可能已结算

### 7.10 `GET /api/sessions/{id}/children`

返回：

- child sessions
- child jobs

### 7.11 `GET /api/sessions/{id}/tasks`

返回：

- task board

### 7.12 Goal APIs

`GET /api/sessions/{id}/goal`

- 返回当前 goal；不存在时返回 `null`

`POST /api/sessions/{id}/goal`

- 创建当前 session 的 goal；session 已有 current goal 时返回 conflict
- 输入同 `POST /api/sessions/start` 的 `goal` 对象，但 endpoint 本身即表示 enabled

`PATCH /api/sessions/{id}/goal`

- 更新 success criteria、validation plan、control 或内部 plan snapshot

`DELETE /api/sessions/{id}/goal`

- 清除 current goal，并写入 `goal.cleared`

`POST /api/sessions/{id}/goal/pause|resume|complete`

- 用户控制 goal 状态；complete 写入 `goal.completed`

`PATCH /api/sessions/{id}/mission/plan`

- 高级/agent 控制面：更新 goal 的内部结构化 plan，包括 requirements、features、milestones、validation contract、role plan、shared artifacts、knowledge artifacts、plan status 或 task-sync hint；默认 Web 启动表单不暴露这些字段
- 该 endpoint 不能直接把 plan status 写成 `approved`；批准必须走 `mission/plan/approve` 或 linked Plan Mode approve 路径。若已 approved 的 mission plan 被 patch 修改，后端必须清空 `approved_at`、重置为 `needs_approval`，并创建或恢复 pending linked Plan Mode。

`POST /api/sessions/{id}/mission/plan/approve`

- 将 goal 内部 plan 标记为 approved；若存在 linked Plan Mode，走 Plan Mode approval / continue 路径；请求体可带 `override_coverage:true` 显式越过 validation coverage 阻断

`PATCH /api/sessions/{id}/mission/validation`

- 更新 goal validation plan 或内部 validation contract
- 若已 approved 的 mission validation contract 被 patch 修改，后端必须清空 `approved_at`、重置为 `needs_approval`，并创建或恢复 pending linked Plan Mode。

Session detail 必须返回从 `goal.json` / `goal-history.jsonl` 派生的 Goal facts，包括 coverage summary、最近 history、progress/handoff、evaluator evidence count 以及 unresolved child / queue IDs；Web 不维护第二套 Mission Control 状态。

### 7.13 Plan Mode APIs

`GET /api/sessions/{id}/planmode`

- 返回当前 Plan Mode snapshot；不存在时返回 404

`POST /api/sessions/{id}/planmode/approve`

- 批准 latest plan version，并通过 runtime continue path 追加 `planmode_approval` user message 后开始执行

`POST /api/sessions/{id}/planmode/revise`

- 输入 `{ "message": "..." }`
- 把当前 awaiting approval plan 退回 planning，并将 message 作为 `planmode_revision` user fact 追加到 session

`POST /api/sessions/{id}/planmode/cancel`

- 取消 pending Plan Mode；如果正在等待 active `request_user_input`，先唤醒 active runner 写入取消 tool result；如果 active handle 已丢失，则由 continue path 根据 pending `tool_call_id` 补偿 tool result
- 在判断 active runner 是否存在前必须清理 durable `failed` / `completed` 状态对应的 stale current-process handle；stale handle 不得把 recovered cancel 误报成“当前 Web console 没有等待中的 input”
- 缺少 current Plan Mode 或 recovered input `request_id` 不匹配这类无效控制必须在 runtime claim session 前失败，不能把可恢复 session 标成 failed

`POST /api/sessions/{id}/planmode/input`

- 输入 `{ "request_id": "...", "answers": [...] }`
- active runner 存在时直接投递回答；active handle 丢失时先 append 对应 `tool_call_id` 的 tool result，再恢复 planning turn

`GET /api/config` / `POST /api/config`

- Settings 暴露可折叠的 Role Provider Overrides，用于 `planner`、`generator`、`evaluator` 三类 role hint 的 provider/profile、API provider、base URL、model 默认值
- role override 只在 `agent_role` 或 internal `role_plan.role` 显式选择 `planner` / `generator` / `evaluator` 时生效；`agent_name` 与 orchestrator / worker / validator 文案不做模糊匹配
- role override 的每个字段都可留空；空字段继承默认 provider、parent session 或所选 provider profile，显式启动/委派请求中的 provider/model 覆盖 Settings 默认值
- 该配置只影响带 role hint 的 session / child / queue provider 选择，不把 Goal/Mission 改造成固定 orchestrator / worker / validator runner

### 7.14 `GET /api/queue/jobs`

参数：

- `limit`

返回 queue jobs。

### 7.15 `GET /api/queue/jobs/{id}`

返回单个 job。

### 7.16 `POST /api/queue/jobs`

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

### 7.17 `GET /api/workers`

返回：

- desired worker count
- active worker count
- poll interval
- last processed jobs

### 7.18 `POST /api/workers`

输入：

- `desired_count`

行为：

- 动态调整 worker pool 并发数
- 该接口保留给高级/测试入口，默认前端不展示 worker pool 调参控件

### 7.19 Workspace file APIs

- `GET /api/files?path=...`：列出受限目录
- `GET /api/file/read?path=...`：分页预览普通文件
- `GET /api/file/download?path=...`：下载普通文件
- `POST /api/files/mkdir`：在目标目录创建文件夹
- `POST /api/files/upload`：multipart 单文件上传；字段为 `path` 和 `file`，成功返回 `201`
- `PATCH /api/files/rename`：输入 `path` 与同目录 `name`，仅重命名普通文件且不覆盖已有路径
- `DELETE /api/files?path=...`：删除单个文件或目录
- `POST /api/files/delete`：事务式删除多个文件或目录

所有 workspace mutation 都必须限制在默认 `workspace/` 根内，复用敏感路径与 symlink policy，并写入 `web.workspace.*` 审计事件。

## 8. 交互状态机

### 8.1 新建任务

用户路径：

1. 填写 prompt
2. 可选填写 `agent_name` / `agent_role`
3. 可选填写 model；provider / mode / isolation 作为 REST API advanced 控制面保留，默认 UI 不强迫展示
4. 如需 provider / mode / isolation 的完整控制，使用 CLI 或 REST API advanced payload
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

1. 从 Session 执行工作区启动或继续任务
2. session 运行中可由模型或高级 CLI/API 提交 child / queue work
3. 通过 API、CLI 或文件事实观察 queued -> running -> completed/failed
4. 若 job 属于 parent session，则验证 parent session 的 durable background notification / queue linkage 已写入；浏览器 smoke 不再要求 `Open job` 或 Background inspector
5. 若需要调整并发，重启 Web 服务时修改 `--workers` 或调用后端 worker API，不从默认页面直接配置

## 9. 刷新与实时策略

当前 Web-first 实现采用 polling-first：

- session rail summary / queue：2 秒
- session detail：1.5 秒
- actions 提交成功后立即触发一次本地 refresh

原因：

- 兼容当前 Web-first / file-fact 架构
- 便于测试
- 不强依赖 SSE / WebSocket

## 10. 错误处理

错误必须区分三类：

- 用户输入错误
  - 例如空 prompt、空 steer message
- 本地 session 选择失效
  - 例如浏览器恢复的 `selectedSessionId` 对应目录已被删除或缺少 `session.json`；前端必须清除持久化选择、回到新 session composer，并停止对缺失 session 反复轮询
- 运行时错误
  - 例如 provider 缺 API key、session 不可恢复、job 失败
- 基础设施错误
  - 例如 session root 不可写、queue claim 失败、配置解析失败

前端展示要求：

- 顶部 toast 用于一次性反馈
- 详情页内联错误用于对象级失败
- queue/job/session 失败状态必须保留可见错误摘要
- Markdown 渲染必须经过本地 HTML/XSS sanitizer，不允许依赖外部 `marked` CDN，也不能直接注入未净化 HTML；该 sanitizer 只用于浏览器注入防护，不承担内容脱敏
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
- 通用 headless browser UI smoke 覆盖 shell/assets 加载，Settings / Workspace / Skills / Sessions / Session 视图基础导航，start 后的 session chrome、tool card、timeline 可见性、settled session polling 收敛和 history clear 留在 Sessions 视图；API 提交 queue job 后只验证后端 queue detail / 文件事实源。另有不依赖外部 API key 的 deterministic budget browser smoke，使用临时 config 与本地 scripted Responses provider 验证 Explorer role override 保存/重新读取、Settings 默认/保存语义、config/API/audit canonical round-trip、真实 foreground extend/resume、background cancel/settle、complete command artifact + `read_file` byte page、`read_session_history` record/content continuation、Context tab lazy load/Refresh、compact inspector telemetry 与 cancelled-not-failed 统计
- 浏览器侧 `runtime exception` 与 `console error` 为空

手工验证至少覆盖：

- 新建 session
- 运行中 steer
- awaiting_input continue
- 通过 REST API/CLI 提交多个 queue job，并从 service/API/文件事实观察并发消费
- 页面响应式与截图；queue-links notification 只验证 durable message 的有界摘要及
  API/文件事实，不新增默认 tracker

## 12. 验收标准

- `aegis-agent web` 能稳定启动本地控制台；`experimental web` 作为兼容别名保持可用
- embedded shell 与前端 assets 能由同一进程本地服务直接提供
- 页面可在无外部网络资源时加载；缺失 CDN 不得导致 `lucide is not defined` 或 `marked is not defined`
- 用户无需记忆 CLI 全命令，也能完成 session 启动、追加输入和继续执行；后台排队是
  REST API/CLI advanced surface，不属于默认 Web 完成标准
- queue worker pool 支持真实并发消费，但默认 UI 不暴露 worker 并发配置卡
- retry-resume proof 以 durable retry metadata 加真实 `provider.retry` 事件作为主要通过条件；若 proof 已成立，session 是否最终落成 `completed` 只作为附带运行状态记录
- 浏览器可以完成核心交互链且无前端运行时错误
- 页面能清晰展示 session、tasks、children、errors 的最新状态；queue 只显示已接纳
  durable message 中的有界关联摘要，完整状态由 API/CLI/文件事实提供
- 所有页面状态都来自本地文件事实与 runtime 真执行，而不是前端假数据
