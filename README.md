# Aegis Agent

Aegis Agent 是一个用 Go 编写的本地 agent harness。它把模型、工具、技能、文件工作区和可恢复的 session 组合在一起，提供一个本地 Web 控制台和一套适合脚本与 CI 的 CLI。

模型负责判断下一步做什么；Aegis 负责提供工具协议、provider 适配、文件持久化、执行控制和安全边界。每次执行的关键事实写入本地 session 目录，Web 和 CLI 读取同一份事实。

> 当前项目面向 Unix-like 环境。`build.sh` 明确拒绝 Windows 目标；CI 在 Ubuntu 24.04 上运行，macOS 是受支持的本地开发平台。

## 你会得到什么

- **本地 Web 控制台**：会话列表与搜索、实时消息流、工具步骤折叠、Goal、Plan Mode 审批和提问、任务与上下文检查器、Workspace、Skills、Settings、命令面板以及中英文界面。
- **CLI**：`init`、`run`、`exec`、`continue`、`steer`、`sessions`、`goal`、`tasks`、`models`、`probe-provider` 和 `doctor`；委派、队列和 TUI 位于 `experimental` 下。
- **Provider 适配**：OpenAI Responses、Anthropic Messages、Google Gemini `generateContent` 和 OpenAI-compatible endpoint。
- **本地事实源**：消息、事件、状态、Goal、任务、队列工作和压缩产物写入你控制的文件目录，支持中断、继续和审计回看。
- **安全边界**：workspace 与 symlink escape 防护、shell 超时和环境变量白名单、Plan Mode 审批、Web mutation 校验、Host 校验和 Web 审计日志。

Aegis Agent 不提供 hosted 多用户服务、远程终端、浏览器 IDE 或固定 workflow engine。Web 控制台是本地 operator surface，不是第二个 runtime。

## 五分钟启动

需要 Go 1.26.11 或更新版本。第一次构建：

~~~sh
./build.sh
./bin/aegis-agent init --force --config .aegis-agent/config.yaml
export OPENAI_API_KEY=你的密钥
./bin/aegis-agent web --config .aegis-agent/config.yaml --listen 127.0.0.1:3940
~~~

然后打开 `http://127.0.0.1:3940`。`init` 生成配置，`--config` 明确选择它。示例中的密钥只是占位符；不要把真实密钥、`.env`、session 或 provider transcript 提交到仓库。

如果没有加载配置文件，provider 执行和 Web 启动会拒绝继续。只有在你明确接受内置默认值时，才使用：

~~~sh
./bin/aegis-agent web --allow-builtin-config
~~~

配置文件的选择、workspace trust 和环境文件规则见 [CLI 与配置](./spec/02-cli-and-config.md)；可复制的完整样例见 [config/config.example.yaml](./config/config.example.yaml)。

## Web 控制台

前端是嵌入二进制的无构建 ES Module 树，源码位于 `internal/webconsole/ui/`，vendor 中包含仓库管理的 Preact、hooks 和 htm。运行时不依赖 CDN 或 npm。

控制台包含四个主要区域：

1. 左侧会话栏：创建会话、搜索会话、查看运行状态，并进入 Sessions、Skills、Workspace 和 Settings。
2. 中间会话流：查看消息与工具步骤，发送新任务、steer、continue、interrupt 或 stop；失败发送会保留为草稿，不会自动重发。
3. 内联控制：在 Plan Mode、Goal 或 `request_user_input` 暂停处直接审批、修订、取消或回答。
4. 右侧检查器：按会话显示 Overview、Plan、Goal、Tasks、Agents、Context、Timeline 和 Files 等适用信息。

页面路由是 `/`、`/s/<session-id>`、`/sessions`、`/skills`、`/workspace` 和 `/settings`。资源从 `/ui/*` 提供，实时连接使用 `/ws`。被移除的旧资源路径返回 404。

控制台可以启动 agent、修改 provider 配置和 `.env`、管理 skills、读写 workspace、删除 session，因此默认监听 loopback。只有在网络受信任且已配置 Host allowlist、Basic Auth 和 HTTPS 时，才考虑暴露到其他地址。

常用快捷键：

| 快捷键 | 作用 |
| --- | --- |
| `⌘K` / `Ctrl+K` | 打开命令面板 |
| `⌘⇧O` / `Ctrl+Shift+O` | 新建会话 |
| `⌘B` / `Ctrl+B` | 显示或隐藏侧栏 |
| `⌘.` / `Ctrl+.` | 显示或隐藏检查器 |
| `/` | 聚焦 Composer |
| `?` | 查看快捷键 |
| `Enter` / `Shift+Enter` | 发送 / 换行 |

## CLI 入口

~~~sh
# 交互执行
./bin/aegis-agent run --config .aegis-agent/config.yaml "检查当前仓库并给出最小修复建议"

# 适合脚本和 CI；默认要求模型显式调用 finish
./bin/aegis-agent exec --config .aegis-agent/config.yaml --json "运行检查并返回结果"

# 对已有 session 继续或追加 steering
./bin/aegis-agent continue <session-id> --config .aegis-agent/config.yaml --message "继续处理失败项"
./bin/aegis-agent steer <session-id> --config .aegis-agent/config.yaml --message "先检查测试失败原因"
~~~

`run` 连接终端时支持 Esc 中断；无交互脚本优先使用 `exec`。`exec` 可输出文本、JSON 或 `stream-json`，并支持 `--plan`、Goal 和显式 provider/model 选择。

## 开发与验证

开发依赖 Go 1.26.11+、Node.js 22+ 和 npm。前端没有独立 build step，修改 `internal/webconsole/ui/` 后重新构建 Go binary，再运行浏览器验收。

~~~sh
./test.sh
npm test
go test ./internal/<package> -count=1
npm ci --ignore-scripts
npx playwright install chromium
npm run test:e2e
~~~

CI 还运行 `go vet ./...`、`staticcheck ./...`、`govulncheck ./...` 和 `go test -race`。完整检查说明见 [测试策略](./spec/07-testing-strategy.md) 和 [validation/README](./validation/README.md)。

macOS 的系统临时目录可能经过符号链接；遇到路径边界测试失败时，使用一个已经解析为真实目录的 `TMPDIR` 再运行 Go 测试。验证脚本本身会为浏览器运行解析临时根。

## 仓库地图

| 目录 | 责任 |
| --- | --- |
| `cmd/aegis-agent` | 二进制入口 |
| `internal/app` | CLI 命令解析和 Web 启动 |
| `internal/runtime` | agent loop、控制、Goal、Plan Mode、压缩、委派和队列 |
| `internal/provider` | provider adapter、请求重试和响应解析 |
| `internal/session` | 文件型 session store 与持久化事实 |
| `internal/tools` | 内置工具、workspace 安全和输出限额 |
| `internal/webconsole` | 本地 HTTP/WebSocket 服务及嵌入资源 |
| `internal/webconsole/ui` | 无构建前端源码 |
| `pkg/agent` | 面向 Go 调用方的 facade |
| `validation` | 无凭据 fixture、Node 单测和 Playwright 验收 |
| `spec` | 产品、架构、操作和验证契约 |

从 [文档地图](./spec/README.md) 进入详细规范。贡献流程见 [CONTRIBUTING](./CONTRIBUTING.md)，安全问题见 [SECURITY](./SECURITY.md)。

本项目使用 [MIT License](./LICENSE)。
