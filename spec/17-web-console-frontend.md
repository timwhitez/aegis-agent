# Web 控制台前端实现

## 范围

前端是仓库内维护的无构建 ES Module 应用。Go 在构建时 embed `internal/webconsole/ui/`，运行时不依赖 CDN、Vite、webpack、import map 或 npm runtime。

页面必须满足：

- 从 `/`、`/s/<id>`、`/sessions`、`/skills`、`/workspace`、`/settings` 刷新可恢复；
- 中文默认、英文可切换，locale 只影响 UI copy；
- durable 输入、输出、路径、ID、tool payload 和错误原文不经过翻译；
- 桌面侧栏/检查器和移动 drawer/overlay 共享同一数据模型；
- 控制失败显示明确 action，不用 optimistic state 假装 durable mutation 成功。

## 目录边界

~~~text
internal/webconsole/ui/
  index.html
  src/main.js
  src/i18n/                 locale dictionaries
  src/state/                store, router, polling, relay, actions
  src/lib/                  pure models, API, format, sanitizer, helpers
  src/shell/                App, TopBar, Sidebar, palette, shortcuts
  src/features/session/     stream, composer, Goal, Plan, approvals
  src/features/inspector/   overview, plan, goal, tasks, agents, context, files
  src/features/history/     Sessions page
  src/features/skills/      Skills page
  src/features/workspace/   Workspace page and file helpers
  src/features/settings/    Settings, provider probe, role overrides
  src/ui/                   reusable presentation and accessibility primitives
  styles/                   tokens, layout and feature styles
  vendor/                   embedded third-party modules and notices
~~~

规则：

- `lib/` 不能依赖 DOM；可复用的状态转换放在 model/helper 并配 Node test；
- `state/` 负责 store、action、polling、relay 和 route synchronization；
- `features/` 负责页面组合，不复制 REST payload、WebSocket control 或 durable state；
- `ui/` 只包含可复用视觉/交互原语，不保存业务事实；
- vendor 更新必须同步 third-party notice、版本和 hash。

## 启动与数据流

`src/main.js` 依次初始化 i18n、store、router、relay、poller、shortcuts 和 App：

1. router 根据 location 选择页面并同步当前 session ID；
2. `lib/api.js` 统一 REST 请求、JSON decode、错误对象和控制 wrapper；
3. `state/actions.js` 把 API/relay 结果写入 store，并维护 loading/error/refresh；
4. `state/relay.js` 连接 `/ws`，只接收事件和实时状态；
5. poller 在页面可见或 relay 不完整时刷新 overview/session；
6. feature model 从 store 派生 timeline、status、tool steps、workspace preview 和 inspector cards；
7. component 只渲染派生结果并触发 action。

任何页面都不得把 fetch 结果存成第二套 session authority；任何控制 action 都必须在 action/API wrapper 中声明 request body、成功状态和失败恢复方式。

## 路由与资源

资源入口是 `/ui/*`，页面入口使用嵌入的 `index.html`。前端 router 负责 History API 链接、session path、active page 和返回；后端负责页面 shell、资源 404、退役路径 404 和 HTTP method。

修改路由时同步检查：

- 直接打开和刷新；
- 浏览器 back/forward；
- session 切换时旧请求是否被丢弃；
- 移动 drawer 关闭和 focus return；
- unknown route 与 API route 不混淆。

## 交互契约

### 会话流和 Composer

消息按时间顺序显示，tool call/result 归入可折叠 step group。Composer 根据 session status 显示 start、send、steer、continue、interrupt、stop 和失败 draft restore。失败发送保留 draft，不能自动重发。

### Goal、Plan Mode 与提问

Goal banner、Plan input card、approval card 和 approval receipt 显示当前 durable snapshot、版本、revision、请求状态和 action。表单提交前可以 optimistic 禁用按钮，但成功事实必须来自服务响应；CAS/revision conflict 必须提示 reload/review/ordinary continue。

### Inspector、Workspace、Skills、Settings

Inspector 只展示当前 session 适用 tabs。Workspace 列表按服务返回的 bytes、modified_at 和 pagination cursor 展示；Skills 安装/卸载、Settings save/probe 和 session delete 都使用 confirm dialog 与错误 recovery。

### 命令面板与快捷键

快捷键通过 `shell/shortcuts.js` 统一注册；命令面板通过 `palette-commands.js` 声明 actions、page navigation、recent sessions 和 help。输入框、dialog、drawer、menu 打开时必须遵守 focus 和 Escape 语义。

## i18n 与原文

- 所有 UI copy 使用 `t()` 或 `tx()`，词典按 common、shell、session、composer、inspector、settings、workspace、skills、history 划分；
- 新 key 同时加入 zh-CN 与 en，并由 key parity test 检查；
- user prompt、assistant output、tool args/results、path、session ID、provider/model、error detail、revision 和 receipt payload 保持原文；
- 不为方便排版把 durable raw value 放进可翻译的 copy string。

## 安全显示

Markdown 使用仓库 sanitizer；HTML helper 不允许任意 raw HTML；用户控制的 href、path、ID、错误和工具输出不能直接作为 unsanitized markup。console error 和 network error 是 E2E 的验收对象。

## 验证

修改前端后至少运行：

~~~sh
./test.sh
npm test
npm run test:e2e
~~~

只修改纯逻辑时可先运行对应 `node --test` 文件；涉及页面、布局、locale、控制、刷新、断线、审批、Workspace 或 Settings 时必须运行对应 E2E 区域。改完前端要重新执行 `./build.sh`，浏览器验收使用新 binary。
