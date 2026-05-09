# Go CLI Agent

`go-cli-agent` 是一个用 Go 编写的极简通用 CLI agent harness。

它的主目标不是做复杂 UI，而是把最小但完整的 agent loop、provider adapter、tools、skills、hooks、session 持久化、任务系统、运行中补充输入与恢复语义组织成一个干净的 CLI 基座。它可以做 coding、审计、文档、运维、整理型 agent；真正决定任务行为的是 `skills/`、工作目录里的 `AGENTS.md`、system/user prompt 和 provider 能力。

## 当前定位

- Core v1 默认围绕 `init/run/exec/steer/continue/sessions/tasks/probe-provider/doctor`
- `run` 支持交互式执行和 `Esc` 暂停
- `exec` 适合脚本或 CI，默认要求模型显式 `finish`
- `steer` 通过文件控制队列向运行中 session 追加输入，`--interrupt` 是 best-effort 抢占
- `continue` 恢复 `paused`、`awaiting_input`、`failed` session
- session / state / messages / events / todo / tasks 是本地文件事实源
- provider 原生支持 OpenAI Responses、Anthropic Messages、Google Gemini `generateContent`
- `openai-compatible` 作为 OpenAI Responses 形状的兼容部署模式提供
- `experimental delegate|children|queue|web`、`tui` 和 `--isolation auto|copy` 仍是显式扩展面，不是默认 core 叙事

## 快速开始

```sh
./build.sh
./test.sh

./bin/go-cli-agent init --force
./bin/go-cli-agent doctor --skip-probe
./bin/go-cli-agent sessions
```

若未显式传 `--workdir`，新的 root session 默认使用当前目录下的 `workspace/` 作为工作目录；目录不存在时会自动创建。

## 核心命令

初始化配置：

```sh
./bin/go-cli-agent init --force
```

交互式运行，允许 `Esc` 暂停：

```sh
./bin/go-cli-agent run --provider openai --model gpt-5.4 "Audit this repo and suggest the smallest safe fix."
```

零交互执行，适合脚本或 CI：

```sh
./bin/go-cli-agent exec --provider anthropic --model claude-sonnet-4-6 "Summarize the current repository and call finish when done."
```

向运行中的 session 追加新输入：

```sh
./bin/go-cli-agent steer <session-id> --message "Focus on failing tests first."
./bin/go-cli-agent steer <session-id> --message "Switch to provider contract validation." --interrupt
```

恢复暂停或自然停顿的 session：

```sh
./bin/go-cli-agent continue <session-id> --message "Proceed with the next step."
```

查看 session、任务板和 provider 状态：

```sh
./bin/go-cli-agent sessions
./bin/go-cli-agent tasks --all <session-id>
./bin/go-cli-agent probe-provider --provider openai
./bin/go-cli-agent doctor --provider openai --skip-probe
```

## Provider 配置

如果要连真实 provider，需要先准备环境变量：

```sh
export OPENAI_API_KEY=...
export ANTHROPIC_API_KEY=...
export GEMINI_API_KEY=...
```

默认配置文件是 `.go-cli-agent/config.yaml`。CLI / Web 启动时还会自动读取仓库根目录 `.env`，或 `GO_CLI_AGENT_ENV_FILE` 指向的 env 文件。

OpenAI / `openai-compatible` 默认走 Responses API。为了保持本地 session 是唯一事实源，adapter 默认发送 `store: false`，不依赖服务端持久化续跑。

常用配置示例：

```yaml
default_provider: openai

providers:
  openai:
    api_key_env: OPENAI_API_KEY
    base_url: https://api.openai.com/v1
    model: gpt-5.4
    request_timeout_sec: 300
    stream_idle_timeout_ms: 300000
    retry:
      max_attempts: 5
      base_delay_ms: 1000
      retry_5xx: true
      retry_transport: true
    wire_api: responses
    max_output_tokens: 8192
    reasoning_effort: xhigh
    text_verbosity: low
```

provider generation / reasoning 字段会进入 runtime 和 session metadata，而不是只停留在一次性 CLI 参数里。当前支持 `temperature`、`top_p`、`max_output_tokens`、`reasoning_effort`、`text_verbosity`、`thinking_budget`、`include_thoughts`、`store`、`send_metadata`，以及 provider timeout / retry 配置。

如果需要面向部署或运维的单页说明，优先看 [`docs/openai-compatible-operator-guide.md`](./docs/openai-compatible-operator-guide.md)。

## Experimental Web

本地 Web 控制台只作为显式实验入口存在，用来观察 session、任务、后台队列、children、timeline，并通过 REST 发起 start / continue / steer / queue submit。它复用本地文件事实源和 runtime 控制面，不是第二套权威状态源。

```sh
./bin/go-cli-agent experimental web --listen 127.0.0.1:3940 --workers 2
```

也可以用 `run.sh` 管理同一个内嵌 frontend+backend 进程：

```sh
./run.sh
./run.sh status
./run.sh logs
./run.sh stop
```

`run.sh` 为 WSL / Windows 浏览器访问默认监听 `0.0.0.0:3940`。这个本地控制台可以写配置和 `.env` API key、删除 session、管理 skill、读取 workspace 文件；只在可信本机网络使用，暴露到非 loopback 地址前先确认风险。

WebConsole 的页面结构、Background Jobs 简化口径、API 契约和浏览器验证要求写在 [`spec/17-web-console.md`](./spec/17-web-console.md)。
Settings 页面提供 provider reasoning 下拉选择和测试按钮：OpenAI / `openai-compatible` 可以选择 `xhigh`，Anthropic / Google 这类 thinking provider 可以选择 `max`，测试按钮会用当前表单值做一次 provider probe，成功后再保存配置。

## 设计原则

- 模型是 agent，harness 只提供 loop、工具、上下文、权限边界、事实记录和恢复能力
- CLI 是主适配层，不把关键状态藏在终端或浏览器内存里
- core runtime、sdk facade、cli adapter 分层保持清晰
- provider 差异留在 adapter 层，CLI / tool / Web 层不承载 provider-specific replay 逻辑
- compaction 只改变发给模型的上下文视图，不覆盖原始日志
- session contract、required artifact tracker、provider attempts、session summary 与 long-run checkpoint 都是围绕本地文件事实源生成的辅助面，不引入固定 workflow engine
- 默认主路径优先于扩展能力，先把 Phase 0-10 做实，再评估 Phase 11+

## 脚本

- `build.sh`: 构建 `bin/go-cli-agent`
- `test.sh`: 检查 `gofmt` 漂移并执行 `go test ./cmd/... ./internal/... ./pkg/...`
- `run.sh`: 启动、停止或查看本地 `experimental web` 进程
- `live_smoke.sh`: 真实 provider 的在线探活脚本
- `validation/run_openai_compatible_acceptance_stack.sh`: provider 连通性确认后的长期 acceptance 入口

## 目录

- `cmd/go-cli-agent`: CLI 入口
- `docs`: operator-facing runbooks
- `internal/app`: 参数解析、命令调度、stdout/stderr 适配
- `internal/runtime`: runner、engine、compaction、interrupt / steer / continue
- `internal/extensions`: workspace extension discovery 与 trust gate
- `internal/provider`: OpenAI / Anthropic / Google adapter
- `internal/tools`: built-in tools、skill command tools、workspace safety
- `internal/session`: session store、todo、task graph、queue files
- `internal/hooks`: 轻量 hooks
- `internal/skills`: 本地 skill catalog
- `internal/webconsole`: local Web console service、API、embedded frontend
- `pkg/agent`: 当前 core-only SDK facade

## Spec 导航

- [`spec/00-product.md`](./spec/00-product.md)
- [`spec/01-runtime-architecture.md`](./spec/01-runtime-architecture.md)
- [`spec/02-cli-and-config.md`](./spec/02-cli-and-config.md)
- [`spec/03-provider-contracts.md`](./spec/03-provider-contracts.md)
- [`spec/04-tools-and-skills.md`](./spec/04-tools-and-skills.md)
- [`spec/05-session-interrupt-resume.md`](./spec/05-session-interrupt-resume.md)
- [`spec/06-hooks.md`](./spec/06-hooks.md)
- [`spec/07-testing-strategy.md`](./spec/07-testing-strategy.md)
- [`spec/08-sdk-and-api-evolution.md`](./spec/08-sdk-and-api-evolution.md)
- [`spec/09-phase-plan.md`](./spec/09-phase-plan.md)
- [`spec/10-context-compaction.md`](./spec/10-context-compaction.md)
- [`spec/11-spec-audit-and-traceability.md`](./spec/11-spec-audit-and-traceability.md)
- [`spec/12-task-system.md`](./spec/12-task-system.md)
- [`spec/13-live-input-and-steering.md`](./spec/13-live-input-and-steering.md)
- [`spec/14-multi-agent-and-isolation.md`](./spec/14-multi-agent-and-isolation.md)
- [`spec/15-background-queue.md`](./spec/15-background-queue.md)
- [`spec/16-terminal-tui.md`](./spec/16-terminal-tui.md)
- [`spec/17-web-console.md`](./spec/17-web-console.md)
- [`spec/18-durable-contract-and-completion.md`](./spec/18-durable-contract-and-completion.md)
