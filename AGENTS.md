# Aegis Agent 工程规则

本文件覆盖仓库根目录及其子目录。它定义协作时必须保持的边界；具体实现细节以当前代码和测试为准。

## 先建立事实

- 先读与改动直接相关的源码和测试，再读 [文档地图](./spec/README.md) 中对应规范。不要根据旧提交、旧截图或不存在的入口推断当前行为。
- 任何用户可见行为、API、配置、持久化格式或安全边界改变，都要在同一提交更新相应文档和测试。
- 代码、测试和 CI 不能通过的行为不能写成“已支持”。如果是计划内容，明确写成计划，不要放进当前能力说明。

## 不能改变的架构边界

- runtime 是唯一的执行事实源。Web、CLI、SDK 和 TUI 都是适配器，不能各自维护一套会话状态或 provider replay 逻辑。
- session 的消息、事件、状态、Goal、任务、队列和审批事实必须可从本地文件恢复。内存结构可以做缓存或并发协调，不能成为唯一事实。
- provider 差异留在 `internal/provider`；不要让 UI、CLI 或通用 tool 代码拼接 provider-specific wire payload。
- compaction 只改变发送给模型的视图，不覆盖原始 transcript 和事件。
- 模型决定任务路线、是否委派以及工具调用顺序。runtime 负责协议、事实、安全和恢复，不要把特定任务写成固定 workflow engine。
- 项目是本地 operator tool。不要引入 hosted SaaS、远程终端、浏览器 IDE、第二套浏览器权威状态或未经用户要求的固定流程面板。

## CLI、配置与持久化

- 根命令是 `init`、`models`、`web`、`run`、`exec`、`continue`、`steer`、`sessions`、`goal`、`tasks`、`probe-provider` 和 `doctor`。`delegate`、`children`、`queue`、`tui` 通过 `experimental` 进入。
- 不要让缺失配置静默触发 provider 请求。显式 `--config`、`AEGIS_AGENT_CONFIG` 或明确的 `--allow-builtin-config` 才能决定执行配置。
- 路径必须经过现有 `fileutil`、`procutil` 和 session store 的安全边界；不要只用字符串 `Clean` 或 `Rel` 处理 symlink escape。

## Web 控制台前端

- 前端源码只在 `internal/webconsole/ui/`；它是无构建 ES Module 树，运行时资源由 Go `embed` 打包。不要加入 CDN、import map、内联脚本、`eval` 或 npm runtime 依赖。
- `ui/src/lib/` 放无 DOM 的纯逻辑，使用 `node:test` 覆盖；`ui/src/state/` 是 store 和 action 的边界，组件通过 hook 订阅。
- REST payload 和统一错误处理经过 `ui/src/lib/api.js`；实时 relay 经过现有 state action。组件不要直接复制 API 形状或维护第二套 session 状态。
- 所有界面文案在对应区域同时维护 `zh-CN` 和 `en`；用户输入、模型输出、工具参数、路径和 ID 等 durable 原文保持 `translate="no"`。
- 保持已有 `data-testid`、键盘语义、移动端触控尺寸和 light theme 行为。UI 改动完成后必须重新构建 binary，再运行对应 Node 单测和 Playwright 区域。

## 安全与恢复

- shell 必须保留超时、输出限额和环境变量白名单；新命令不能绕过现有执行策略。
- Web mutation 必须保留 Host 校验、same-origin 或 mutation header、JSON body policy、请求大小限制和审计路径。
- 中断发生在 tool call 后必须留下可恢复的工具结果，避免 dangling tool call；审批和 receipt 的重放不能静默重复 provider side effect。
- 默认监听 loopback，session 与 agent 产物使用 owner-only 权限；文档、日志、测试输出中不得出现真实凭据或敏感 transcript。
- 不要修改现有安全边界来迁就测试。若边界确实需要改变，先补充能复现风险的测试，再更新安全文档。

## 文档规则

- 根 README 讲使用者需要知道的路径；本文件讲贡献者需要遵守的规则；细节放在 `spec/`。
- 文档只引用当前仓库存在的脚本、目录、路由和命令。旧前端、旧资源路由和退役开关不能作为当前入口描述。
- 不在根文档堆放某个临时 provider、某次评审或某个外部部署环境的数字；专题部署信息放在对应的 `spec/` 文档。

## 验证与提交

按改动范围选择最小充分检查，合并前执行 CI 对应的完整检查：

~~~sh
./test.sh
npm test
go test ./internal/<package> -count=1
npm run test:e2e
go vet ./...
staticcheck ./...
govulncheck ./...
go test -race -count=1 -timeout=10m ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...
~~~

前端或 Web 服务改动至少运行 `npm run test:e2e`；配置、runtime、provider 或并发改动至少运行对应 Go package test，必要时加 `-race`。

每次写入型改动都要在验证后创建真实 Git commit。提交前检查 `git diff`、`git status` 和敏感文件；不要提交 binary、session、`.env`、截图运行目录或 live-provider 输出。
