# Go CLI Agent

`go-cli-agent` 是一个用 Go 编写的极简通用 CLI agent harness。

它的主目标不是做一个复杂的终端 UI，而是把最小但完整的 agent loop、provider adapter、tools、skills、hooks、session 持久化、任务系统、运行中补充输入与恢复语义组织成一个干净的 CLI 基座。它既可以做 coding agent，也可以做审计、文档、运维、整理型 agent。真正决定“它做什么”的，是 `skills/`、工作目录里的 `AGENTS.md`、以及用户给它的 prompt。

在保持 CLI-first 的前提下，仓库现在也允许显式 `experimental web` 控制台：它提供本地单页前端，用于 session / task / queue / children / timeline 观测，以及 `start` / `steer` / `continue` / background queue 的低门槛交互，但不会替代默认 core CLI 叙事。
当前内嵌前端已经重构为更完整的轻量控制台壳层：左侧导航与 session rail、中央工作区、右侧 action rail 同时存在，视觉上采用浅色 data-dense dashboard，而不是把实验面继续维持成裸信息页。
当前 Web 控制台的 start / queue 表单都支持显式 `agent_name` / `agent_role`，方便在大型任务里直接从浏览器发起 planner / generator / evaluator 风格的 role-aware 运行。
当前 session detail 还会把 execution / recovery / output / provider options 四类摘要直接放在顶部，并允许从 queue job、child session、background notification 卡片直接跳回相关 session，减少在列表与详情之间来回找上下文的成本。
当前前端还加入了纯客户端的 session rail、queue jobs、timeline 检索和状态筛选；当 run 目录里会话、队列任务和事件数量上来时，可以先在浏览器里收窄集合，再进入具体 session 处理。

## 当前定位

- Core v1 的默认验收口径锁定在 Phase 0-10：`init/run/exec/steer/continue/sessions/tasks/probe-provider/doctor`
- `Esc` 暂停、`continue` 恢复、外部 `steer` 热插入输入、`exec` 零交互完成策略，都是主路径能力
- provider 目前原生支持 OpenAI Responses、Anthropic Messages、Google Gemini `generateContent`
- `openai-compatible` 作为 OpenAI Responses 形状的兼容部署模式提供
- generation / reasoning / store 等 provider 选项会进入 runtime 和 session metadata，而不是只停留在 CLI
- 大型项目 profile 额外验证了 `experimental delegate|children|queue|web` 与 `--isolation auto|copy`，用于 child 执行、后台队列、隔离编辑、parent-child 观测，以及本地 Web 控制台

以下能力仍保留在仓库里，但当前仍不是默认日常交互面：

- terminal TUI snapshot
- local Web console

## 快速开始

```sh
./build.sh
./test.sh

./bin/go-cli-agent init --force
./bin/go-cli-agent doctor --skip-probe
./bin/go-cli-agent sessions
```

如果要连真实 provider，需要先准备环境变量：

```sh
export OPENAI_API_KEY=...
export ANTHROPIC_API_KEY=...
export GEMINI_API_KEY=...
```

如果想用本地 Web 控制台查看 session、queue、children 和 worker 状态：

```sh
./bin/go-cli-agent experimental web --listen 127.0.0.1:3940 --workers 2
```

浏览器里会看到：

- 左侧：固定的 Overview / Queue 导航和 session 列表
- 中央：overview、queue、session detail 三类主工作区
- 右侧：Start / Session Actions / Queue Job / Worker Pool 四个上下文动作卡

其中 session detail 会额外显示执行摘要卡、provider 选项摘要和可展开的 metadata；queue / children / queue-links 卡片则支持直接打开相关 session，方便在 parent、child 和 background job 之间跳转。
左侧 session rail、Queue Jobs、Timeline 都支持 search + status/kind filter，不需要等后端分页或额外 API 才能先把当前视图压缩到可操作范围。

## 核心命令

初始化配置：

```sh
./bin/go-cli-agent init --force
```

交互式运行，允许 `Esc` 暂停：

```sh
./bin/go-cli-agent run --provider openai --model gpt-5.4 "Audit this repo and suggest the smallest safe fix."
```

零交互执行，默认要求显式 `finish`：

```sh
./bin/go-cli-agent exec --provider anthropic --model claude-sonnet-4-6 "Summarize the current repository and call finish when done."
```

向运行中的 session 追加新输入：

```sh
./bin/go-cli-agent steer <session-id> --message "Focus on failing tests first."
./bin/go-cli-agent steer <session-id> --message "Switch to provider contract validation." --interrupt
```

`steer` 会在入队前拒绝空消息和超长输入，避免把无效控制消息写进 session 控制队列。

恢复暂停或自然停顿的 session：

```sh
./bin/go-cli-agent continue <session-id> --message "Proceed with the next step."
```

查看 session 与任务板：

```sh
./bin/go-cli-agent sessions
./bin/go-cli-agent tasks --all <session-id>
```

探活和诊断 provider：

```sh
./bin/go-cli-agent probe-provider --provider openai
./bin/go-cli-agent doctor --provider openai --skip-probe
```

## Provider 配置

默认配置文件是 `.go-cli-agent/config.yaml`。当前项目保持“默认配置简洁，可选高级项显式开启”的风格。

OpenAI / `openai-compatible` 默认走 `Responses API`。为了保持本地 session 是唯一事实源，adapter 会默认发送 `store: false`，不依赖服务端持久化来续跑。

如果需要面向部署或运维的单页说明，优先看 [`docs/openai-compatible-operator-guide.md`](./docs/openai-compatible-operator-guide.md)。

provider 可选 generation 字段目前支持：

- `temperature`
- `top_p`
- `max_output_tokens`
- `reasoning_effort`
- `text_verbosity`
- `thinking_budget`
- `include_thoughts`
- `store`
- `send_metadata`

provider 还支持一个可选的 transport retry 配置块，用来吸收长任务里的暂时性 `429` / `5xx` / header timeout：

- `retry.max_attempts`
- `retry.base_delay_ms`
- `retry.retry_429`
- `retry.retry_5xx`
- `retry.retry_transport`

示例：

```yaml
default_provider: openai

providers:
  openai:
    api_key_env: OPENAI_API_KEY
    base_url: https://api.openai.com/v1
    model: gpt-5.4
    timeout_sec: 120
    retry:
      max_attempts: 2
      base_delay_ms: 1000
      retry_5xx: true
      retry_transport: true
    wire_api: responses
    max_output_tokens: 8192
    reasoning_effort: high
    text_verbosity: low
```

说明：

- OpenAI / `openai-compatible` 会把这些选项映射到 `reasoning`、`text`、`max_output_tokens`
- `send_metadata=false` 可用于不兼容 `metadata` 字段的非官方 `openai-compatible` 网关；默认仍会发送 metadata，保持 runtime/session 到 adapter 的契约完整
- 当前 session metadata 还会持久化 effective provider retry policy，方便在 `session.json` 中直接追溯这次运行实际采用的 retry 预算，而不是只靠 HTTP adapter 的隐式默认值
- provider HTTP 调用会按 `retry` 配置对 `429`、`5xx` 和 transport timeout 做有限重试，并写出 `provider.retry` 事件
- `doctor --provider openai-compatible --json` 会直接暴露当前生效的 `store`、`send_metadata` 和 `retry_policy`，方便 operator 在连真实网关前先核对配置
- Anthropic 当前支持 `temperature`、`top_p`、`max_tokens`，以及基于 `thinking_budget` 的 `thinking`
- Google 当前支持 `generationConfig`，以及基于 `thinking_budget` / `include_thoughts` 的 `thinkingConfig`
- v1 仍不持久化 OpenAI reasoning items 和 Gemini thought signatures，所以 provider-native reasoning/思维产物 replay 不是当前主路径能力

## 设计原则

- 模型是 agent，harness 只提供循环、工具、上下文和权限边界
- CLI 是适配层，不把关键状态藏在终端里
- core / experimental / store 入口在 app-facing 边界上保持独立 facade，不再复用同一个 concrete runner type
- session / state / messages / events 是文件事实源
- compaction 只压缩发给模型的上下文视图，不覆盖原始日志
- compaction summary 会尽量保留 `artifact_memory`、`project_memory_stack`、`high_value_proofs` 和保留给最终证据复核的 targeted read 预算
- review/audit 产物不只校验 Markdown 结构；runtime validator 还会核对 cited path:line 是否可读，并要求 snippet-level evidence support
- 当任务对交付文件要求固定开头、精确标题或 section 顺序时，runtime 会把这些 exact-template 约束当成一等 guard，而不是继续被默认 findings-first 习惯带偏
- 大任务优先把 durable memory 外置到文件。推荐在工作目录下维护 `reports/spec.md`、`reports/plan.md`、`reports/progress.md`、`reports/validation.md`
- 当大型任务在运行中被 `steer` 改变方向时，runtime 会优先提醒并在必要时阻断，要求先刷新 `reports/spec.md` / `reports/plan.md`，再继续实现、handoff 或 finish
- 默认主路径优先于扩展能力，先把 Phase 0-10 做实，再谈 Phase 11+

## 脚本

- `build.sh`: 构建 `bin/go-cli-agent`
- `test.sh`: 检查 `gofmt` 漂移并执行 `go test ./cmd/... ./internal/... ./pkg/...`，把默认仓库测试面限制在受控模块包上，不把 `validation/` 下的历史 run/workspace 副本扫进主路径验收
- `live_smoke.sh`: 真实 provider 的在线探活脚本；如果目标是会拒绝 `metadata` 字段的非官方 `openai-compatible` 网关，可先设置 `GO_CLI_AGENT_LIVE_SEND_METADATA=false`
- `validation/run_round31_complex_real_matrix.sh`: 当前最完整的 26 场景真实矩阵入口；现在会额外产出 RT21 gap-proof preflight evidence，直接覆盖 provider metadata durability、review artifact enforcement、report path hardening 三条 proof-completeness 缺口
- `validation/run_round61_task_heavy_real_matrix.sh`: 面向真实复杂开发任务族的 task-heavy live 矩阵入口；它围绕多语言修复、same-task steer、interrupt->resume->completion、role-aware delegate/children、role-aware queue、retry/webconsole operator proof 组织约 20 个场景，并把每个场景单独落到 `validation/runs/<run-id>/cases/<case-id>/`。该入口还会把 task-heavy + RT21 gap-proof preflight 收敛到 `notes/preflight-task-heavy-proof-tests.md` 与 `notes/preflight-gap-proof-summary.md`，让 provider metadata/retry durability、artifact/path guard、exact-template guard、interrupt/queue/delegate/project-memory refresh 等 proof 在进入 live cases 前就有脚本层锚点
- `validation/run_openai_compatible_acceptance_stack.sh`: 当前长期稳定的一键 acceptance 入口；在 provider 连通性已经确认后，它会串行执行 `round31` 主矩阵和 focused webconsole follow-up，并产出 bundle 级 summary/notes
- `validation/run_experimental_webconsole_followup_validation.sh`: `experimental web` 的 focused live 验证入口，覆盖 durable retry restore、queue background notification 去重、embedded shell/assets，以及 headless Chrome 的真实浏览器交互 smoke；当前 smoke 还会显式验证 role-aware start、tasks/children/queue tab 切换和 queue-links 通知。当前稳定参考证据见 `validation/runs/2026-03-27-openai-compatible-gpt-5.4-round54e-experimental-webconsole-followup-stable-proof/`。retry proof 以 durable retry metadata + 真实 `provider.retry` 为准，即使 bounded finish nudges 后 session 仍是 `awaiting_input` 也只记为备注；需要 `OPENAI_API_KEY`、`node` 和本地 Chrome/Chromium，可通过 `CHROME_BIN` 指定浏览器

## 扩展能力

仓库里仍保留 `delegate` / `children` / `queue` / `tui` / `web` 相关代码和 spec，用于把项目扩到 multi-agent、后台作业或可视观测面。当前区分两条口径：

- minimal core: 仍围绕 `init/run/exec/steer/continue/sessions/tasks/probe-provider/doctor`
- large-project / console profile: `experimental delegate|children|queue|web` 与 `--isolation auto|copy` 继续保留，但仍通过显式入口保持与默认帮助面分离
- large-project profile 现在还支持显式 `--role planner|generator|evaluator` / `agent_role`，并把该 role 持久化到 session、queue job、background notification 和 provider metadata，方便 handoff 与验证追踪

扩展入口统一挂在：

```sh
./bin/go-cli-agent experimental <delegate|children|queue|tui|web> ...
```

本地 Web 控制台示例：

```sh
./bin/go-cli-agent experimental web --listen 127.0.0.1:3940 --workers 2
```

默认 `go-cli-agent` 帮助文本只展示 core v1 命令；只有显式进入 `experimental` 子树时，才展示这些扩展入口。

默认 core 会话也不会暴露 `agent_spawn` / `agent_status` / `agent_list`；只有显式开启 `runtime.multi_agent.enabled=true` 时才注册这些扩展工具。

## 目录

- `cmd/go-cli-agent`: CLI 入口
- `docs`: operator-facing runbooks
- `internal/app`: 参数解析、命令调度、stdout/stderr 适配
- `internal/runtime`: runner、engine、compaction、interrupt / steer / continue
- `internal/provider`: OpenAI / Anthropic / Google adapter
- `internal/tools`: built-in tools、skill command tools、workspace safety
- `internal/session`: session store、todo、task graph、queue files
- `internal/hooks`: 轻量 hooks
- `internal/skills`: 本地 skill catalog
- `internal/webconsole`: local Web console service、API、embedded frontend
- `pkg/agent`: 当前 core-only SDK facade；experimental/store-only surfaces 继续留在内部路径，待稳定后再单独暴露

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
