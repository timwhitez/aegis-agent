# Web Console Frontend (v3)

> 本文档取代原 Web Console v2 迁移方案，定义默认 Web 控制台前端的技术架构、模块边界、
> 信息架构、交互契约、设计系统和验收方式。产品边界、REST/WebSocket 契约与后端行为仍以
> `17-web-console.md` 为准；两者冲突时先修 spec，再改实现。

## 1. 目标与范围

- 以全新的模块化前端替换 v2 外壳与共享旧脚本（`internal/webconsole/assets`、`assets-v2`）。
- 移除 legacy 页面、`/legacy/` 回滚路由与 Settings 中的 legacy 开关；回滚改用 git revert。
- 对齐主流 agent 平台的交互：侧栏常驻会话列表、对话流内联审批与提问、运行中原地停止、
  步骤折叠摘要、实时活动指示、命令面板与全局快捷键。
- 不改变后端事实源：session store、queue store、runtime facade 与 workspace 文件仍是唯一权威状态；
  浏览器只保存显示偏好、审批意图（既有 `ApprovalOperationController` 契约）与内存中的失败草稿。
- 不引入：浏览器端文件编辑器、远程终端、workflow 画布、worker 调参面板、独立 Queue/Background tracker、
  深色主题（后续如加入，只能作为显示偏好）。

## 2. 技术选型

- 原生 ES Modules，无构建步骤；Go `embed` 直接提供源码文件。
- 组件与渲染：仓库内置 Preact 10.29.8（MIT）、`preact/hooks` 与 htm 3.1.1（Apache-2.0），
  位于 `ui/vendor/`，只把 hooks 中的裸模块 `from"preact"` 改为相对路径，并去掉 sourcemap 注释。
  `ui/vendor/THIRD_PARTY_NOTICES.md` 记录版本、来源、修改与 SHA-256；测试校验哈希。
- 不使用 import map（CSP `script-src 'self'` 会拦截内联脚本），所有 import 均为相对路径。
- 不使用 `eval`/`new Function`；Markdown 只经本地 sanitizer 生成 HTML 后注入。
- 浏览器基线：近两年的 Chromium / Firefox / Safari。

## 3. 目录与模块边界

```
internal/webconsole/ui/
  index.html                 应用壳：#app、样式与 main.js 模块入口
  vendor/                    preact / hooks / htm 与第三方声明
  styles/                    tokens.css、base.css、components.css、layout.css 与各 feature 样式；app.css 汇总
  src/
    main.js                  启动：locale、store、路由、实时通道、挂载 <App>
    lib/                     纯逻辑，无 DOM 依赖，可在 node:test 中直接 import
      html.js                html = htm.bind(hx)（规范化 translate 的元素工厂）与 hooks 再导出
      api.js                 REST 客户端与 APIError（唯一 fetch 入口）
      store.js               最小可订阅 store
      i18n.js                t()、locale、Intl 格式化
      markdown.js            安全 Markdown 渲染与 href 清洗
      format.js              短 ID、截断、字节、时长、JSON pretty
      status.js              session 显示状态与语气映射
      session-model.js       消息流/时间线/工具步骤/文件变更/后台结果的纯派生
      approval-operations.js 审批意图持久化与回执协调（沿用既有契约）
      send-recovery.js       发送意图、失败草稿与恢复规则
      workspace-model.js     路径规范化、偏好分桶、字节游标分页规则
      settings-model.js      时长解析、推理选项、配置 payload 构造
      commands.js            命令面板命令注册与模糊匹配
    state/                   store 实例、actions、轮询与 WebSocket relay
    i18n/                    按区域拆分的词典（<area>.js 导出 zh-CN 与 en，键一一对应）
    ui/                      通用组件（Button、IconButton、Icon、Badge、Tabs、Dialog、Menu、Toast…）
    shell/                   App、Sidebar、TopBar、CommandPalette、ShortcutHelp、路由视图切换
    features/
      session/               会话视图：消息流、步骤、活动、内联审批/提问、Composer、Goal 横幅
      inspector/             检查器：概览、计划、目标、任务、子代理、上下文、时间线、文件变更
      history/               会话管理页
      skills/                技能页
      workspace/             工作区页
      settings/              设置页
```

- `lib/` 只依赖 `lib/`；`state/` 依赖 `lib/`；组件通过 hooks 订阅 store，不直接调用 `fetch`。
- 每个 feature 只拥有自己的目录与样式文件；跨 feature 复用的组件放入 `ui/`。

## 4. 路由与资源服务

- 静态资源只从 `/ui/*` 提供（`ui/` 子树），不存在的资源返回 404，不走 SPA fallback。
- 应用路由：`/`（新会话）、`/s/{sessionID}`（会话）、`/sessions`、`/skills`、`/workspace`、`/settings`；
  其他非 `/api/`、非 `/ui/` 的 GET 路径返回 `index.html`，前端将未知路由导向 `/`。
- 前端使用 History API；会话 URL 可直接打开、刷新和前进/后退。
  打开的 session 不存在时提示一次并回到 `/`，停止对其轮询。
- `/legacy*`、`/v2-assets/*`、`/shared-assets/*` 不再提供（404）。
  配置字段 `web.legacy_ui_enabled` 作为已弃用的无效字段继续被解析以兼容旧配置文件，不再出现在 API 与 Settings 中。
- Host 校验、Basic Auth、安全头、ETag/gzip 与 `Cache-Control: no-cache` 行为不变。

## 5. 状态与数据流

- 单一 store（`state/store.js`）按领域分片：`route`、`meta`、`connection`、`overview`、`session`、
  `composer`、`ui`、`toasts`、`dialogs`。组件用 `useStore(selector)` 订阅。
- 会话数据只来自 `GET /api/sessions/{id}` 及其 `messages` / `context` 子接口；
  加载过的更早消息按 message id 去重合并，后续轮询不得丢弃。
- 乐观消息、失败草稿与实时 engine 事件只存在于内存；成功请求不产生失败草稿。
- 实时策略保持 polling-first：活动 session 1.5s、侧栏 overview 2s；页面隐藏时暂停；
  `/ws` 只作为连接状态与刷新提示（指数退避重连），不承载控制动作。
- 所有控制动作走 `api.js` 中的 REST 封装；提交中的按钮进入 pending/disabled，防止重复提交。

### 5.1 模块契约

本节记录基础层（shell 与 state）对各 feature 模块的约束。feature 模块只修改自己拥有的目录和样式文件；需要新增跨模块能力时，先更新本节。

**Store 分片**（`state/store.js`，初始值由 `initialState()` 给出）：

| 分片 | 主要字段 |
| --- | --- |
| `route` | `{ name: 'new'\|'session'\|'sessions'\|'skills'\|'workspace'\|'settings', path, params: { id? } }` |
| `meta` | `{ data: /api/meta, loading, error }` |
| `connection` | `{ relay: 'connecting'\|'open'\|'closed', attempts, pageVisible, online }` |
| `overview` | `{ data: /api/overview, sessions: root 会话摘要, loading, error, loadedAt, titles: { id: 首条 prompt } }` |
| `session` | `{ id, detail, messages, hasMoreEarlier, earlierLoaded, loading, loadingEarlier, refreshing, error, missing, generating, liveEvents, lastRefreshedAt }` |
| `composer` | `{ text, goalEnabled, planEnabled, interrupt, provider, model, revising, submitting, focusToken, failedDrafts }` |
| `ui` | `{ locale, sidebarCollapsed, sidebarOpen, inspectorOpen, inspectorTab, paletteOpen, shortcutsOpen, sidebarQuery, historyFocus, viewport: { narrow, wide, mobile } }` |
| `toasts` / `dialogs` | toast 队列与确认/输入对话框栈 |

- `error` 字段统一为 `{ message, detail, action, status }`，保留后端的 detail 和 action。
- `session.messages` 是"更早分页 + 轮询尾部"按 id 去重合并后的窗口（`lib/session-list.js` 的 `mergeMessageWindows`），轮询不会丢弃已加载的更早消息。
- `session.optimistic` 保存 `SendRecoveryStore` 产生的乐观用户消息，由消息流追加到末尾；`session.approval` 保存当前会话的审批操作展示状态；`session.pendingAction` 标记进行中的控制动作；`session.activity` 是本地动作（启动、继续、追加指令）的即时活动描述，新的 engine 事件或会话收敛后清除。
- Composer 草稿（文本与 Goal/Plan/打断开关）按会话保存在内存中（`state/composer-drafts.js`），切换回会话时恢复；新会话草稿独立。失败草稿只存在于内存。
- 工作区浏览路径偏好按 `workspace_root + session` 分桶存入 `aegis-agent.workspace-paths.v1`；工作区页通过 `features/workspace/workspace-session.js` 记住最近打开的会话，用于从会话进入时定位其 workdir。
- 会话摘要（overview、sessions、history）带只读 `title`：Web 适配层从 `messages.jsonl` 头部有界读取首条用户消息并在内存缓存；工作区列表条目带 `size`（普通文件）与 `modified_at`。
- 会话标题优先使用摘要 DTO 的 `title`；`overview.titles` 只作内存补充（来自本页打开过的会话的首条 prompt），用于摘要尚未带标题的情况。
- 轮询取回的消息尾部与已加载窗口没有任何重叠时（两次刷新之间新增超过一页），前端用 `before_id` 向前分页补齐缺口（每页 200 条，最多 10 页）；仍补不齐时只保留最新窗口并允许"加载更早"，不会静默丢失消息。会话切换会递增 epoch，过期的刷新或更早分页结果一律丢弃。
- 偏好 `sidebarCollapsed`、`inspectorOpen`、`inspectorTab` 持久化到 `aegis-agent.ui-prefs.v1`；语言偏好持久化到 `aegis-agent.locale.v1`。

**订阅与动作**：

- 组件通过 `state/hooks.js` 的 `useStore(selector, equality = shallowEqual)` 订阅 store，通过 `useLocale()` 订阅语言；只有 `state/` 模块可以写 store。
- `state/actions.js`：
  - 导航与数据：`navigate(path, { replace })`、`newSession()`、`openSession(id)`、`refreshSession({ silent })`、`loadEarlierMessages()`、`refreshOverview({ silent })`、`refreshMeta()`、`handleMissingSession(id)`。
  - 界面状态：`toggleSidebar()`、`setSidebarOpen(open)`、`toggleInspector(force?)`、`setInspectorTab(tab)`、`setSidebarQuery(q)`、`openPalette()`、`closePalette()`、`openShortcuts()`、`closeShortcuts()`、`toggleLocale()`、`viewSessionInHistory(id)`。
  - Composer 状态：`focusComposer()`、`setComposerText(text, { focus })`、`toggleGoalMode(force?)`、`togglePlanMode(force?)`。
- `state/toasts.js`：`pushToast({ tone, message, detail, action, timeout })`、`toastError(error, fallbackKey)`、`dismissToast(id)`。
- `state/dialogs.js`：`confirmDialog({ title, body, confirmLabel, danger })` 返回 `Promise<boolean>`；`promptDialog({ title, label, initialValue, validate })` 返回 `Promise<string|null>`。
- `state/selectors.js`：`currentDisplayStatus`、`currentSessionTitle`（返回 `{ text, raw }`，`raw` 为 true 时放入 `translate="no"`）、`canStopSession`、`canInterruptSession`。
- `state/session-actions.js` 由会话、审批、目标的 feature 实现，函数名不可更改：`submitComposer`、`startRun`、`continueRun`、`steerRun`、`stopRun`、`interruptRun`、`approvePlan`、`revisePlan`、`cancelPlan`、`answerPlanInput`、`createGoal`、`updateGoal`、`pauseGoal`、`resumeGoal`、`completeGoal`、`clearGoal`、`restoreFailedDraft`、`discardFailedDraft`。所有请求必须经过 `lib/api.js`；提交期间把 `composer.submitting` 设为 true。
- 轮询（`state/polling.js`）：会话详情每 1.5s、overview 每 2s。页面隐藏时暂停；只在以下情况下轮询：生成中、当前进程持有句柄、存在活动子任务、实时通道未连接、侧栏有运行中的会话。WebSocket（`state/relay.js`）只提供刷新提示，采用指数退避重连；收到格式错误的消息时提示一次，不抛错。

**布局与组件**：

- SessionView（shell 拥有）负责阅读列、自动跟随、"回到最新"、"加载更早消息"和底部 Composer 区。它挂载以下组件：`MessageList`、`GoalBanner`、`PlanInputCard`、`ApprovalCard`、`FailedDrafts`、`TodoStrip`、`Composer`、`EmptySession`。这些组件不接收 props，直接从 store 读取数据；各文件头部注释写明各自的契约。
- Inspector（shell 拥有）负责面板与覆盖层两种模式、标题栏和 Tabs。各 Tab 组件接收 `{ sessionId }`，Tab id 取自 `state/prefs.js` 的 `INSPECTOR_TABS`。
- 页面组件（`HistoryView`、`SkillsView`、`WorkspaceView`、`SettingsView`）统一使用 `ui/PageHeader.js`。
- 通用组件位于 `src/ui/`：
  - 基础控件：Button（`variant`、`size`、`pending`、`buttonRef`）、IconButton（必须提供 `label`）、Icon、Badge、StatusBadge、StatusDot。
  - 结构与交互：Tabs 与 TabPanel、Dialog（Portal 渲染，含焦点陷阱，按 Esc 关闭，关闭后恢复焦点，打开时 `#app-layout` 设为 inert；`placement` 可取 center、top、left、right）、DialogHost、Menu、Tooltip、Disclosure。
  - 反馈与辅助：Toasts、Spinner、EmptyState、Kbd（`'mod'` 表示 ⌘ 或 Ctrl）、CopyButton 与 `copyText`、Skeleton、Portal、`focus.js`。
- 纯逻辑：`lib/commands.js`（`fuzzyScore`、`rankCommands`、`createCommandRegistry`）、`lib/shortcuts.js`（`matchShortcut`、`isEditableTarget`）、`lib/status.js`、`lib/session-list.js`、`lib/format.js`。

**i18n 与样式**：

- 词典按区域拆分：每个区域文件 `src/i18n/<area>.js` 导出 `{ 'zh-CN': {...}, en: {...} }`，可选 `prefixes` 声明该区域拥有的 lib 键族（例如 session 拥有 `step.`、`activity.`，composer 拥有 `send.`、`approval.`，inspector 拥有 `event.`、`timeline.`）。区域包括 common、shell、session、composer、inspector、history、skills、workspace、settings。键必须以 `<area>.` 或所声明前缀开头，前缀不得跨区域重叠，两种语言键集与占位符一致。`src/i18n/index.js` 负责合并，遇到重复键时报错。
- lib 模块返回 `{ key, params }` 描述对象，由 `lib/i18n.js` 的 `tx()` 渲染：键缺失时回退 `params.fallback`；`status.*` 别名到 `common.status.*`。每个 lib 模块头部列出其输出的全部键，`validation/scripts/ui_i18n_*_keys_test.mjs` 校验各区域覆盖。
- `lib/html.js` 的元素工厂把字符串 `translate` 规范化为布尔值，保证 `translate="no"` 真正渲染为 `translate="no"`。
- 样式文件中，`tokens.css`、`base.css`、`components.css`、`layout.css` 由基础层拥有；`session.css`、`composer.css`、`inspector.css`、`pages.css` 由对应 feature 拥有。`app.css` 按上述顺序 `@import` 这些文件。点击目标尺寸使用 `var(--hit)`：精确指针下为 32px，粗指针或宽度 ≤768px 时为 44px。

**测试 id**：`sidebar`、`sidebar-drawer`、`session-list`、`session-row`（带 `data-session-id` 和 `data-status`）、`session-search`、`new-session`、`connection-status`、`topbar`、`session-title`、`status-badge`、`session-actions`、`inspector-toggle`、`language-toggle`、`session-view`、`empty-session`、`starter-suggestion`、`message`（带 `data-role`）、`tool-step`、`composer`、`composer-input`、`send-button`、`stop-button`、`approval-card`、`plan-input-card`、`goal-banner`、`failed-drafts`、`todo-strip`、`load-earlier`、`back-to-latest`、`inspector`、`inspector-tab-<id>`、`command-palette`、`command-palette-input`、`shortcut-help`、`confirm-dialog`、`prompt-dialog`、`toast`、`page-<sessions|skills|workspace|settings>`、`nav-<route>`、`session-group-toggle`、`topbar-palette`、`topbar-page-title`、`inspector-tab-loading`、`inspector-goal-goto-approval`。各 feature 的细粒度测试 id 以组件源码为准，浏览器验收只依赖本列表与各页面根 id。

## 6. 信息架构与布局

- **侧栏**（≥1024px 常驻，可用 ⌘B / Ctrl+B 收起；更窄时为抽屉）：品牌、"新建会话"、会话搜索、
  会话列表、底部导航（会话管理、技能、工作区、设置）与本地连接状态。
  - 会话列表只列 root session；"进行中/待处理"（running、awaiting_input、待审批、待回答）置顶，
    其余按今天 / 昨天 / 近 7 天 / 更早分组；每行显示标题（首条 prompt 或 agent 名）、状态点与相对时间。
    每组默认最多显示 6 行，其余通过"显示其余 N 个"展开，当前会话始终可见；搜索结果不受上限限制。
    当前会话所在行使用与顶栏相同的显示状态，二者不得不一致。
  - 搜索在已加载列表内即时过滤标题、ID 与 workdir；"查看全部"进入会话管理页。
- **顶栏**：侧栏开关（收起或窄屏时）、会话标题、状态徽标、provider/model、workdir 摘要；
  右侧为会话操作菜单（中断、停止、复制 ID、在会话管理中查看）、检查器开关与 `中文 / EN` 切换。
  新会话不显示伪造 ID。
- **会话视图**：居中阅读列（最大约 760px）+ 底部吸附 Composer；检查器在 ≥1280px 为并排右栏
  （不遮挡对话），更窄时为带焦点陷阱的覆盖面板。
- **页面视图**（会话管理 / 技能 / 工作区 / 设置）：统一页头（标题、说明、主操作）与内容区。

## 7. 交互契约

### 7.1 消息流

- 用户消息右对齐浅色气泡；assistant 正文无气泡、全宽排版；harness reminder 为琥珀色系统提示，
  不得渲染成用户消息。
- 两段 assistant 文本之间的连续工具调用折叠为"步骤组"：显示步骤数、耗时与运行/失败状态；
  展开后每步一行（图标、工具名、一句话摘要、状态），再展开看参数与结果。
  工具调用与匹配结果按 `tool_call_id` 合并；`finish` 的最终答复只作为 assistant 消息显示一次。
- thinking 默认折叠；goal 进度工具、后台结果、文件变更使用专用紧凑卡片，原始值放在 `translate="no"` 节点。
- 运行中在流末尾显示活动指示（阶段、已用时）；session 暂停/失败/等待输入/完成显示为行内状态注记。
- 用户上滚时停止自动跟随并出现"回到最新"按钮；靠近底部时自动跟随。
- 消息悬停提供复制；顶部提供"加载更早消息"。

### 7.2 Composer

- 自动增高输入框；占位文案随状态变化（新会话 / 运行中追加指令 / 继续 / 修改计划）。
- 工具行：Goal 与 Plan 切换（仅在可 start/continue 时出现）、运行中的"打断当前步骤"开关、
  provider/model 选择（仅新会话与 continue 时可改，默认继承）。
- 主按钮：空闲时为发送；运行中输入为空时变为停止，有内容时为追加指令（steer）。
- Enter 发送，Shift+Enter 换行，输入法组合输入期间不发送。
- 失败草稿以卡片显示在 Composer 上方，标注原 session，并区分"被拒绝"和"未确认是否送达"；
  只有在当前输入为空时才能恢复草稿，不自动重发。
- Todo 进度条显示在 Composer 上方，可展开查看完整列表。

### 7.3 内联审批与提问

- Plan Mode `awaiting_approval` 时，在消息流末尾显示计划审批卡：摘要、版本、Markdown 计划（长内容可折叠）、
  假设/风险/验证，以及"批准并运行""要求修改""取消计划"。审批目标、覆盖率豁免确认、过期重审与回执查询
  沿用 `17-web-console.md` §7.13 的契约，界面同时给出状态说明。
- `awaiting_user_input` 时显示提问卡：单选/多选选项与自由输入"其他"，提交答案或取消计划。
- 检查器的"计划"页只展示详情与历史，不再重复主操作按钮。
- "要求修改"把焦点移到 Composer 并切换为修订模式；若从覆盖面板触发，先关闭面板并解除 `inert`。

### 7.4 命令面板与快捷键

- ⌘K / Ctrl+K 打开命令面板：新建会话、跳转页面、切换检查器/侧栏/语言、停止/中断当前会话、
  聚焦输入框、切换 Goal/Plan，以及按标题搜索最近会话。键盘上下选择、Enter 执行、Esc 关闭；
  使用 combobox/listbox 语义。
- 全局快捷键：⌘K 面板、⌘⇧O 新会话、⌘B 侧栏、⌘. 检查器、`/` 聚焦输入框（不在输入控件中时）、
  `?` 快捷键帮助。Windows/Linux 以 Ctrl 代替 ⌘。快捷键不得在输入法组合输入时触发。

### 7.5 确认与反馈

- 普通 start / steer / continue / 批准不弹确认；删除、清空历史、覆盖率豁免、保存配置/API key
  等风险动作使用应用内确认对话框（焦点陷阱、Esc 取消、恢复触发焦点）。
- toast 用于一次性反馈（`aria-live`），桌面端位于右上角（顶栏下方，检查器打开时让开），移动端位于顶部居中，最多同时显示 3 条，不得遮挡 Composer、审批卡等主操作；对象级错误（含发送失败）内联显示并保留后端 `detail/action`，不再重复弹 toast。

## 8. 设计系统

- 气质：安静、内容优先、工程化；浅色主题固定（`color-scheme: light`），不依赖系统深色偏好。
- tokens（`styles/tokens.css`）：中性暖灰底色与白色纸面，墨色主按钮，品牌绿用于焦点、选中与运行强调，
  蓝/绿/琥珀/红为语义色；4px 间距体系；圆角 6/8/12/16；两级阴影；系统字体栈与等宽字体栈，正文 14px。
- 对比度满足 WCAG AA；焦点环始终可见；支持 `prefers-reduced-motion`。
- 点击目标：精确指针下不小于 32×32 CSS px（满足 WCAG 2.2 AA 24px 下限），
  `pointer: coarse` 或 ≤768px 时不小于 44×44 CSS px；icon-only 按钮必须有 `aria-label`。
- 状态色只用于状态；同一页面只有一个主操作样式。

## 9. 国际化

- 所有界面文案经 `t(key, params)` 输出；`zh-CN`（默认）与 `en` 词典键必须一一对应。
- 偏好存储键沿用 `aegis-agent.locale.v1`；切换时同步 `<html lang>`、ARIA、placeholder、title 与日期/数字格式。
- 用户消息、模型输出、工具负载、文件内容、路径、ID、agent 名/角色等 durable 原文不翻译，
  渲染在 `translate="no"` 节点内；fallback 文案与原文分属不同节点。

## 10. 测试与验收

- `./test.sh`：Go 格式、`node --check` 所有 `ui/**/*.js`、`node --test validation/scripts/*_test.mjs`、Go 测试。
- 单元测试直接 import `ui/src/lib/*`：API payload、Markdown XSS、消息流合并、消息窗口合并、shell 重定向文件变更、
  审批控制器、发送恢复、工作区路径与分页、设置模型、显示状态、命令匹配、词典键一致性与源码 `t()` 键覆盖、vendor 哈希。
- Go 测试：`/` 与 SPA 路由返回应用壳、`/ui/*` 资源与 MIME、未知资源 404、legacy 与旧资源路由 404、
  无外部 CDN 引用、安全头。
- 浏览器验收（`npm run test:e2e`）在真实本地服务与 fixture provider 上覆盖 `17-web-console.md` §12 的完整矩阵，
  并新增侧栏会话列表、内联审批与提问、命令面板与快捷键；桌面与移动视口、中英文切换与重载持久化；
  console/page/request error 为空；截图与 manifest（commit/browser/viewport/locale/hash）写入 `validation/runs/`。
