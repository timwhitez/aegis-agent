# Go CLI Agent Web Console Spec

> 当前定位：显式 experimental 扩展面。目标是给 `go-cli-agent` 增加一个完整但本地优先的 Web 控制台，用更低的学习成本承载 session 观测、任务进度、后台队列和并行执行控制，同时不破坏 CLI-first 的 core 叙事。

## 1. 目标

Web console 解决三类问题：

- 新用户很难快速理解 `run`、`steer`、`continue`、background queue 的差异
- 复杂任务运行时，session / todo / task graph / child / queue / error 分散在多个命令和文件里，不易整体判断进度
- 当需要后台并发 worker 时，纯 CLI 查看成本高，缺少统一的任务队列与执行器可视观测面

这次实现要提供一个完整的本地控制台，而不是只有只读页面：

- 可以创建新 session
- 可以对运行中的 session 追加 steer
- 可以对暂停或等待输入的 session 执行 continue
- 可以提交 queue job
- 可以查看 queue / children / task board / timeline / errors
- 可以看到后台 worker 并发状态

## 2. 产品边界

### 2.1 明确要做

- 本地单进程 HTTP 服务
- 同进程内嵌静态前端资源
- 基于本地 session 文件事实的只读视图
- 基于 runtime / queue 的真实控制操作
- 后台 worker pool 并发执行

### 2.2 明确不做

- 不做 hosted Web SaaS
- 不做多租户、RBAC、数据库账号系统
- 不把 Web UI 变成权威状态源
- 不要求 provider 流式 API、SSE 或 WebSocket 才能工作
- 不在 v1 里引入浏览器端代码编辑器、文件树 IDE 或远程终端

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
- 当前 worker 并发度
- 新建任务按钮
- 手动刷新按钮
- 当前选中 session 的短标识与状态（若存在）

顶部栏属于全局状态条，不依赖用户先进入总览页才能看到这些信息。
当前实现中，顶部栏采用轻量 dashboard header：左侧是说明性 copy，中间是 4 个事实卡片，右侧是 `New Session` / `Refresh` 主操作。

### 4.2 左侧主导航

固定区域：

- 总览
- Sessions
- Queue

当前实现的左栏不是纯文本列表，而是品牌区 + 导航区 + session rail 三段式结构；新用户进入页面后可以先看导航，再逐步进入 session 详情。
当 session 数量增多时，左栏还应支持纯客户端 search + status filter，让用户先缩小集合，再切换具体 session。

Sessions 列表项必须展示：

- session id 短标识
- status
- provider / model
- 最近更新时间
- agent role / agent name（若存在）
- workdir 摘要，方便在多工作目录场景里快速分辨 session 上下文

### 4.3 总览页

总览页是新用户的默认落点，展示：

- 运行中 session 数
- awaiting_input session 数
- queued / running / failed jobs 数
- worker pool 状态
- 最近活动 feed
- 最近失败 session / job

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

### 4.5 右侧动作区

动作区跟随当前选择对象变化。

当选择 session 时：

- 若 `status = running`
  - 显示 steer 输入框
  - 显示 `interrupt steer` 开关
  - 若该 session 由当前 Web server 托管，还显示 `pause` / `interrupt` 按钮
- 若 `status = awaiting_input | paused | failed`
  - 显示 continue 输入框
  - 支持 provider / model 覆盖
- 始终可显示 `queue child job` 表单

当选择 Queue 时：

- 显示 queue submit 表单
- 显示 worker pool 并发调节控件

当前实现里，右侧动作区会始终保留 `Start Session` 入口，并在此基础上叠加 `Session Actions`、`Queue Job`、`Worker Pool` 三张上下文卡片。
当前 `Session Actions` 卡片还会额外显示当前 session 的 phase、pending steer、agent identity 和 workdir 摘要，避免用户在操作前还要切回中间详情区确认上下文。
Queue 主视图同样应支持纯客户端 search + status filter，优先服务本地控制台的高密度浏览，而不是先要求 operator 增加新的后端查询 API。

## 5. 视觉系统

### 5.1 风格方向

- 风格：简洁、现代、偏 operations dashboard
- 目标气质：工程化、可追踪、低噪声
- 不使用花哨拟物或泛 AI landing page 视觉
- 当前外观采用浅底、柔和边框、低对比玻璃卡片与明确层级阴影，接近现代本地控制台而不是深色监控墙或营销页

### 5.2 设计系统

基于 `ui-ux-pro-max` 的建议，本实现采用：

- pattern：data-dense dashboard
- typography：`Fira Sans` + `Fira Code`
- primary accent：蓝色用于主操作、导航聚焦和运行态
- semantic success：绿色用于完成与健康状态
- neutral：浅灰蓝用于背景、边框和信息层级

为避免纯黑压迫感，页面使用“浅背景 + 白色/浅灰卡片 + 蓝色主强调 + 语义状态色”的本地控制台风格，而不是全黑监控墙。

### 5.3 组件要求

- 状态芯片：running / awaiting_input / paused / completed / failed / queued
- KPI 卡片：概览计数
- 时间线卡片：消息与事件混合流
- 数据表格：queue jobs、children
- 摘要卡：session health / recovery / provider options / current focus
- 过滤工具栏：session rail / queue jobs / timeline 的 search + filter
- 右侧 action panel：统一提交交互
- 左侧 session rail：支持快速扫视 status / provider / role / 最近更新时间
- mini action chips：从 queue/children/notification 等卡片直接跳到相关 session

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
- `done channel`
- `last result`

原因：

- 中断必须打到正确的 in-memory runner
- 多个并发 session 不能共享单个 interrupt slot
- Web server 需要知道哪些 session 是自己托管的 active run

### 6.3 Worker Pool

worker pool 允许并发 `N >= 1`。

约束：

- worker 之间共享同一 queue 目录
- claim 依赖 `queued -> running` 原子 rename
- 一个 worker 只处理一个 job
- 任意 job 的完成、失败都必须真实创建/更新 child session 与 queue job 记录

## 7. API 设计

所有 API 默认返回 JSON。

### 7.1 `GET /api/meta`

返回：

- server version
- session root
- worker count
- providers
- experimental capabilities

### 7.2 `GET /api/overview`

返回聚合统计：

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
- recent messages
- recent events
- children summary
- linked jobs
- active handle status（是否被当前 server 托管）

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

### 7.9 `GET /api/sessions/{id}/children`

返回：

- child sessions
- child jobs

### 7.10 `GET /api/sessions/{id}/tasks`

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
- `isolation_mode?`
- `isolation_root?`

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

1. 打开 Queue 面板
2. 提交一个或多个 jobs
3. 调整 worker count
4. 在队列表里观察 queued -> running -> completed/failed
5. 若 job 属于 parent session，则在该 parent timeline 里看到 background notification

## 9. 刷新与实时策略

当前实现采用 polling-first：

- overview / queue：2 秒
- session detail：1.5 秒
- actions 提交成功后立即触发一次本地 refresh

原因：

- 兼容当前 CLI-first / file-fact 架构
- 便于测试
- 不强依赖 SSE / WebSocket

## 10. 错误处理

错误必须区分三类：

- 用户输入错误
  - 例如空 prompt、空 steer message、非法 worker 数
- 运行时错误
  - 例如 provider 缺 API key、session 不可恢复、job 失败
- 基础设施错误
  - 例如 session root 不可写、queue claim 失败、配置解析失败

前端展示要求：

- 顶部 toast 用于一次性反馈
- 详情页内联错误用于对象级失败
- queue/job/session 失败状态必须保留可见错误摘要

## 11. 测试要求

自动测试至少覆盖：

- API 路由
- embedded shell 与 `embed` 静态资产可直接加载
- start / continue 异步返回
- steer 写入与 `source=web`
- overview 聚合
- worker pool 缩放
- queue job 从提交到完成的 happy path
- role-aware start form 可显式传递 `agent_name` / `agent_role`
- queue job 失败的持久化与可见性
- queue completion 与 stale-running reconcile 重叠时，background notification 仍按 `queue_job_id` 去重
- focused retry-resume live rerun 需要同时验证 durable retry metadata 未漂移，以及真实 `provider.retry` 事件出现
- 若 retry proof 已经拿到上述 durable evidence，而 bounded finish nudges 后 session 仍为 `awaiting_input`，应将其记为 non-blocking completion quirk，而不是把整轮 webconsole follow-up 判成失败
- headless browser UI smoke 覆盖 start、role-aware session chrome、tasks/children/queue 标签切换、continue、worker 更新、queue submit、queue 视图、queue-links 通知与 manual refresh
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
- 用户无需记忆 CLI 全命令，也能完成 session 启动、追加输入、继续执行和后台排队
- queue worker pool 支持真实并发消费
- retry-resume proof 以 durable retry metadata 加真实 `provider.retry` 事件作为主要通过条件；若 proof 已成立，session 是否最终落成 `completed` 只作为附带运行状态记录
- 浏览器可以完成核心交互链且无前端运行时错误
- 页面能清晰展示 session、tasks、queue、children、errors 的最新状态
- 所有页面状态都来自本地文件事实与 runtime 真执行，而不是前端假数据
