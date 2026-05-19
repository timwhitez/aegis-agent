# Go CLI Agent

`go-cli-agent` 是一个用 Go 编写的 Web-first 本地 agent harness。

它的默认入口是本地 Web 控制台，底层仍是最小但完整的 agent loop、provider adapter、tools、skills、hooks、session 持久化、任务系统、运行中补充输入与恢复语义。CLI 保留为稳定 fallback、脚本化和故障恢复入口。它可以做 coding、审计、文档、运维、整理型 agent；真正决定任务行为的是 `skills/`、工作目录里的 `AGENTS.md`、system/user prompt 和 provider 能力。

## 当前定位

- Web-first v1 默认围绕 `web` 本地控制台；CLI fallback 围绕 `init/run/exec/steer/continue/sessions/goal/tasks/probe-provider/doctor`
- `run` 支持交互式执行和 `Esc` 暂停
- `exec` 适合脚本或 CI，默认要求模型显式 `finish`
- `run/exec --plan` 或 Web 的 Plan 开关会进入 session-scoped Plan Mode：审批前只允许读/搜索、`request_user_input` 和 `submit_plan`，批准后才执行
- `steer` 通过文件控制队列向运行中 session 追加输入，`--interrupt` 是 best-effort 抢占
- `continue` 恢复 `paused`、`awaiting_input`、`failed` session
- session / state / messages / events / goal / todo / tasks 是本地文件事实源
- provider 原生支持 OpenAI Responses、Anthropic Messages、Google Gemini `generateContent`
- `openai-compatible` 作为 OpenAI Responses 形状的兼容部署模式提供
- `experimental web` 是 `web` 的兼容别名；`experimental delegate|children|queue|tui` 和 `--isolation auto|copy` 仍是高级/扩展面，不主导默认 Web 页面
- 默认不做报告、prompt、session、compaction 或 provider view 脱敏；如需脱敏，由用户在当轮 prompt 明确要求

## 快速开始

```sh
./build.sh
./test.sh

./bin/go-cli-agent init --force
./bin/go-cli-agent web
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

先产出可审批计划，不执行变更：

```sh
./bin/go-cli-agent exec --plan-only "Plan the provider contract migration."
./bin/go-cli-agent continue <session-id> --approve-plan
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
./bin/go-cli-agent goal show <session-id>
./bin/go-cli-agent tasks --all <session-id>
./bin/go-cli-agent probe-provider --provider openai
./bin/go-cli-agent doctor --provider openai --skip-probe
```

长任务可以在启动时附带 durable goal。默认使用方式很克制：用户只写 prompt，开启 Goal 后 prompt 本身就是目标；模型负责在运行中用 `get_goal/create_goal/update_goal`、todo/task 和普通工具拆分验证，不要求用户先填写 criteria、milestone、budget 等表单。goal 会落盘为 `goal.json`，模型完成审计后通过 `update_goal` 标记 complete；暂停、恢复和清除仍由用户/CLI/Web 控制。

```sh
./bin/go-cli-agent exec \
  --goal "Migrate the provider contract tests without changing runtime behavior" \
  "Implement the migration and call finish after the goal is complete."

./bin/go-cli-agent goal pause <session-id>
./bin/go-cli-agent goal resume <session-id>
./bin/go-cli-agent goal complete <session-id>
```

## Provider 配置

如果要连真实 provider，需要先准备环境变量：

```sh
export OPENAI_API_KEY=...
export ANTHROPIC_API_KEY=...
export GEMINI_API_KEY=...
```

默认配置文件优先来自 `~/.go-cli-agent/config.yaml` 或显式 `--config` / `GO_CLI_AGENT_CONFIG`。仓库内 `.go-cli-agent/config.yaml` 默认视为未受信，只在设置 `GO_CLI_AGENT_TRUST_WORKSPACE_CONFIG=1|true` 或存在普通文件 `.go-cli-agent/trusted` 时加载。CLI / Web 启动时还会读取仓库根目录 `.env`，或 `GO_CLI_AGENT_ENV_FILE` 指向的 env 文件；自动导入仅允许 provider secret 形态的键（`*_API_KEY`、`*_ACCESS_TOKEN`），不会从 `.env` 接收 `GO_CLI_AGENT_*`、`PATH`、`HOME` 等控制变量。

OpenAI / `openai-compatible` 默认走 Responses API。为了保持本地 session 是唯一事实源，adapter 默认发送 `store: false`，不依赖服务端持久化续跑。

常用配置示例：

```yaml
default_provider: openai

providers:
  openai:
    api_provider: openai-compatible
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
    reasoning_summary: auto
    text_verbosity: low
```

`api_provider` 表示实际 adapter family；Provider map key 只是 Provider Profile 名称。`wire_api` 仅作为 OpenAI-compatible Responses 的 legacy / advanced compatibility 字段保留。provider generation / reasoning 字段会进入 runtime 和 session metadata，而不是只停留在一次性 CLI 参数里。当前支持 `api_provider`、`temperature`、`top_p`、`max_output_tokens`、`reasoning_effort`、`reasoning_summary`、`text_verbosity`、`thinking_budget`、`include_thoughts`、`store`、`send_metadata`，以及 provider timeout / retry 配置。

Settings 可以为 `planner`、`generator`、`evaluator` 三类 role hint 单独配置 provider override。每个字段都可留空：空 provider 继承默认 provider 或 parent session，空 `api_provider` / `base_url` / `model` 继承所选 provider profile；显式启动或委派时传入的 provider/model 仍优先。role override 只在 agent 明确选择 `agent_role` 或 internal `role_plan.role` 为这三类之一时生效，不从 `agent_name` 或 orchestrator / worker / validator 文案做模糊匹配，也不会把 Goal/Mission 改成固定三执行者 workflow。

DeepSeek / Kimi 这类自定义 profile 如果通过 Anthropic-compatible Messages 接入，应显式配置 adapter family，而不是改名伪装成 `anthropic`：

```yaml
providers:
  kimi:
    api_provider: anthropic-compatible
    api_key_env: KIMI_API_KEY
    base_url: https://<kimi-anthropic-compatible-endpoint>
    model: <kimi-model>
    anthropic_version: 2023-06-01
    thinking_budget: 32000
    include_thoughts: true
    max_output_tokens: 32768
```

如果需要面向部署或运维的单页说明，优先看 [`docs/openai-compatible-operator-guide.md`](./docs/openai-compatible-operator-guide.md)。

## Web Console

本地 Web 控制台是默认入口，用来观察 session、goal、任务、后台结果、children、timeline，并通过 REST 发起 start / continue / steer 等 session 操作。启动区的 Goal 是一个简单开关；选中后用户仍只写 prompt，agent 在运行中自行拆分目标、计划和验证。它复用本地文件事实源和 runtime 控制面，不是第二套权威状态源。

Web 的 Plan 开关对应同一个 Plan Mode 事实源：`planmode.json`、`artifacts/planmode-history.jsonl` 和 `artifacts/planmode-plan.md`。Plan inspector 可审批、要求修改、取消计划，并回答 `request_user_input` 的规划问题；pending Plan Mode 会拒绝以该 session 为 parent 的 child / queue 提交。

```sh
./bin/go-cli-agent web --listen 127.0.0.1:3940 --workers 2
```

`experimental web` 仍可作为兼容别名使用；新脚本和文档应优先使用 `web`。

也可以用 `run.sh` 管理同一个内嵌 frontend+backend 进程：

```sh
./run.sh
./run.sh status
./run.sh logs
./run.sh stop
```

`run.sh` 为 WSL / Windows 浏览器访问默认监听 `0.0.0.0:3940`。这个本地控制台可以写配置和 `.env` API key、删除 session、管理 skill、读取 workspace 文件；只在可信本机网络使用，暴露到非 loopback 地址前先确认风险。

WebConsole 的页面结构、Session Background 观测口径、API 契约和浏览器验证要求写在 [`spec/17-web-console.md`](./spec/17-web-console.md)。
Settings 页面提供 Provider Profile、API Provider、provider reasoning / thinking 下拉、OpenAI reasoning summary 下拉和测试按钮：OpenAI / `openai-compatible` 可以选择 `xhigh` + `Auto` summary，Anthropic-compatible / Google 这类 thinking provider 可以选择 `max`。测试按钮会用当前表单值做一次 thinking-observation probe，并回显 adapter 采用的 `thinking_strategy`；结果会区分“请求成功”和“本次实际返回可读 thinking / summary”，成功测试不会自动保存配置。

## Troubleshooting

- `provider.retry` / `provider.auto_resume` 说明 adapter 或 runtime 在处理上游超时/重试；查看 `.go-cli-agent/sessions/<id>/provider-attempts.jsonl` 和 `session.md`。
- session 长时间运行但 provider attempts 持续成功、同时反复 `load_skill`、同一路径 `read_file` 或 `todo_write` no-op，通常是工具循环退化；`session.md` 的 `Tool Repetition` 小节会列出重复工具、重复读取路径和 no-op todo 次数。
- 重复 `load_skill` 默认返回 `already_loaded`，需要重新读取 skill 文件时显式使用 `force_reload=true`。
- todo 是当前 session 的执行节奏板；durable tasks 在 `tasks/` 目录中。空 `tasks/` 不代表 todo 刷新就是持久任务进展。

## 设计原则

- 模型是 agent，harness 只提供 loop、工具、上下文、权限边界、事实记录和恢复能力
- Web service、CLI 和 SDK 都只是适配层，不把关键状态藏在浏览器或终端内存里
- core runtime、sdk facade、web service、cli adapter 分层保持清晰
- provider 差异留在 adapter 层，CLI / tool / Web 层不承载 provider-specific replay 逻辑
- compaction 只改变发给模型的上下文视图，不覆盖原始日志
- compaction 只做上下文规模控制，不按密钥模式默认改写内容；用户显式要求脱敏时，模型应在当轮交付文档中处理
- session goal、session contract、required artifact tracker、provider attempts、session summary 与 long-run checkpoint 都是围绕本地文件事实源生成的辅助面，不引入固定 workflow engine
- 默认 Web-first 主路径优先于高级扩展能力，Phase 0-10 的 runtime/CLI 基座与 Phase 15 Web 控制台一起构成 v1 默认交付口径

## 脚本

- `build.sh`: 构建 `bin/go-cli-agent`
- `test.sh`: 检查 `gofmt` 漂移、WebConsole JS 语法，并执行 `go test ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`
- `run.sh`: 启动、停止或查看本地 Web 控制台进程
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
- `internal/session`: session store、goal、todo、task graph、queue files
- `internal/hooks`: 轻量 hooks
- `internal/skills`: 本地 skill catalog
- `internal/webconsole`: local Web console service、API、embedded frontend
- `pkg/agent`: 当前 runtime-first SDK facade

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
