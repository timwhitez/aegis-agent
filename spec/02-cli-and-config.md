# Go CLI Agent CLI And Config Spec

## 1. 命令面原则

CLI 需要同时满足三件事：

- 主路径简单
- 阶段输出清楚
- 未来可扩展但不污染当前默认体验

因此当前命令面分为两层：

## 2. Core 命令

core v1 的默认命令面固定为：

- `go-cli-agent init`
- `go-cli-agent run [prompt]`
- `go-cli-agent exec [prompt]`
- `go-cli-agent steer <session-id>`
- `go-cli-agent continue <session-id>`
- `go-cli-agent sessions`
- `go-cli-agent goal <show|pause|resume|clear|complete> <session-id>`
- `go-cli-agent tasks <session-id>`
- `go-cli-agent probe-provider`
- `go-cli-agent doctor`

说明：

- 这组命令定义当前默认工作流
- README、帮助文本、live smoke、测试说明默认围绕这一组命令展开
- `delegate` / `children` / `queue` / `tui` / `web` 仍可保留，但只能通过显式 experimental 入口出现

## 3. 扩展命令

以下命令属于扩展兼容面：

- `go-cli-agent experimental delegate <parent-session-id> [prompt]`
- `go-cli-agent experimental children <session-id>`
- `go-cli-agent experimental queue <submit|list|show|worker>`
- `go-cli-agent experimental tui`
- `go-cli-agent experimental web`

要求：

- 默认帮助文本只展示 core 命令；`experimental` 入口只在显式调用 `go-cli-agent experimental` 时展示
- 扩展命令本身不应继续作为默认顶层 operator surface
- 主路径示例和默认验收口径仍然只围绕 core 命令

## 4. 参数解析

当前 CLI 继续使用 Go 标准库 `flag`，并保留轻量 interspersed-flags 归一化：

- `run` / `exec` / `continue` / `steer` / `goal` / `tasks` / `experimental delegate` / `experimental children` / `experimental queue submit` / `experimental queue show`
  支持在 `<session-id>` 或 `[prompt]` 前后继续写受支持 flags
- 若 prompt 自身以 `-` 开头，使用 `--` 结束 flag 解析

## 5. Core 命令定义

### 5.1 `init`

作用：

- 生成默认配置
- 生成 `.env.example`
- 生成示例 `skills/`
- 可选生成示例 hook
- 输出下一步命令

高频参数：

- `--config`
- `--force`
- `--example-hook`
- `--provider`
- `--model`
- `--base-url`
- `--api-key-env`
- `--wire-api`
- `--skill-dir`
- `--session-dir`

### 5.2 `run`

作用：

- 前台交互执行
- 允许 `Esc` 暂停
- 若无显式 `finish` 且模型自然停顿，则进入 `awaiting_input`

高频参数：

- `--provider`
- `--model`
- `--config`
- `--workdir`
- `--system`
- `--json`
- `--timeout`
- `--plan`

默认规则：

- 未显式提供 `--workdir` 时，root session 的 `requested_workdir` 默认取当前目录下的 `workspace/`
- 若该 `workspace/` 目录不存在，runtime 在 session 启动前自动创建
- `--plan` 只通过显式 flag 启用 Plan Mode；普通 prompt 中写“先计划”不自动切换 runtime mode

### 5.3 `exec`

作用：

- 零交互执行
- 不监听 `Esc`
- 默认要求显式 `finish`

高频参数：

- `--provider`
- `--model`
- `--config`
- `--workdir`
- `--system`
- `--json`
- `--timeout`
- `--plan`
- `--plan-only`

默认规则与 `run` 一致：

- 未显式提供 `--workdir` 时，root session 默认使用当前目录下的 `workspace/`
- 若该目录不存在，runtime 在启动前自动创建
- `--plan` / `--plan-only` 启动 session-scoped Plan Mode，模型只能读/搜索、请求规划输入或提交计划；提交计划后停在 `awaiting_input` + `phase=plan_approval`

### 5.4 `steer`

作用：

- 向 `running` session 追加新输入
- 默认 queue-first
- `--interrupt` 请求 best-effort 抢占

高频参数：

- `--message`
- `--interrupt`
- `--json`
- `--config`

### 5.5 `continue`

作用：

- 恢复 `paused` / `awaiting_input` / `failed`
- 允许补充一条新 user message

高频参数：

- `--message`
- `--provider`
- `--model`
- `--json`
- `--config`
- `--plan`
- `--approve-plan`
- `--cancel-plan`

Plan Mode 行为：

- `--plan` 在可恢复 session 上开启新一轮 planning pass
- `--approve-plan` 批准最新提交的 plan version，并追加 `meta.source=planmode_approval` 的 user message 后恢复执行
- `--cancel-plan` 取消 pending Plan Mode；如果存在待补偿的 `request_user_input.tool_call_id`，runtime 先写入取消 tool result
- 当 `planmode.status=awaiting_approval` 且传入普通 `--message` 时，该 message 视为 plan revision，事件源标记为 `planmode_revision`

### 5.6 `sessions`

作用：

- 列出最近 session
- 展示 ID、状态、时间、phase

高频参数：

- `--limit`
- `--json`
- `--config`

### 5.7 `goal`

作用：

- 读取或用户控制当前 session 的 durable goal
- `show` 读取 `goal.json`
- `pause` / `resume` / `clear` / `complete` 写入 goal 状态和 goal history

高频参数：

- `--json`
- `--config`

`run` / `exec` 额外支持 goal 启动参数：

- `--goal`
- `--goal-mode goal|mission`
- `--goal-token-budget`
- `--goal-time-budget`
- `--goal-success`（可重复）
- `--goal-validate`（可重复）
- `--goal-plan-approval`

默认推荐只使用 `--goal`，让模型根据 prompt 自行拆分计划和验证；其余参数保留给脚本化、高级或兼容调用。这些参数只创建 session-scoped goal，不创建固定 workflow graph；模型仍通过工具和当前上下文自主推进。

### 5.8 `tasks`

作用：

- 展示当前 session 的 todo 和 task graph
- `--all` 时额外输出扁平任务列表

高频参数：

- `--json`
- `--all`
- `--config`

### 5.9 `probe-provider`

作用：

- 对当前 provider 配置做一次真实探活
- 默认验证“是否能返回一个 `finish` tool call”

高频参数：

- `--provider`
- `--model`
- `--base-url`
- `--api-key-env`
- `--wire-api`
- `--prompt`
- `--json`
- `--config`

### 5.10 `doctor`

作用：

- 检查配置、session root、skills、hooks、provider 配置
- 只读报告 session / queue partial state：缺失 `session.json` / `state.json` / `messages.jsonl`、同一 queue job 出现在多个 status 目录、running job 缺 lease 或 heartbeat stale、queue job 指向不存在的 session
- 默认可附带一次最小 probe
- 可 `--skip-probe`
- 对 OpenAI / `openai-compatible`，诊断输出还应明确当前生效的 `store`、`send_metadata`、timeout policy 与 retry policy，方便 operator 在真实接线前核对 transport 契约

高频参数：

- `--provider`
- `--model`
- `--base-url`
- `--api-key-env`
- `--wire-api`
- `--prompt`
- `--skip-probe`
- `--json`
- `--config`

## 6. 输出风格

### 6.1 默认文本输出

按阶段打印：

```text
== session:start ==
session: 20260319-101530-ab12cd
steer: go-cli-agent steer 20260319-101530-ab12cd --message "..."
== provider:openai ==
== assistant ==
...assistant output...
== awaiting_input ==
session: 20260319-101530-ab12cd
next: go-cli-agent continue 20260319-101530-ab12cd --message "..."
```

### 6.2 JSON 输出

- `run` / `exec` / `continue` 输出最终结果对象
- `sessions` / `tasks` / `probe-provider` / `doctor` 输出单个 JSON 对象或数组
- `--json` 模式下的事件流保持 JSONL

## 7. 配置文件

默认位置：

- `~/.go-cli-agent/config.yaml`
- 或显式 `GO_CLI_AGENT_CONFIG` / `--config`
- 当前工作目录 `.go-cli-agent/config.yaml` 只在设置 `GO_CLI_AGENT_TRUST_WORKSPACE_CONFIG=1|true`，或存在普通文件 `.go-cli-agent/trusted` 时加载；未受信 workspace config 不得改写 provider endpoint、API-provider、hooks、session-dir、skills-dir 等 active runtime 配置

环境变量文件：

- 默认读取当前工作目录 `.env`
- 若设置 `GO_CLI_AGENT_ENV_FILE`，则读取该文件
- 进程启动时会先加载 env 文件，再解析 provider `api_key_env`
- 自动导入仅允许 provider secret 形态的键（`*_API_KEY`、`*_ACCESS_TOKEN`）；`GO_CLI_AGENT_*`、`PATH`、`HOME`、shell loader / dynamic loader 等控制变量必须忽略
- `experimental web` Settings 页面保存的 API key 会持久化到这个 env 文件中

配置结构：

```yaml
schema_version: 1

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

session:
  dir: .go-cli-agent/sessions
  dir_mode: "0700"

skills:
  dirs:
    - ./skills

runtime:
  guardrails_mode: yolo
  exec_finish_required: true
  max_turns_soft: 24
  max_turns_hard: 40
  command_timeout_sec: 120
  provider_auto_resume:
    enabled: true
    max_attempts: 2
  steer:
    poll_interval_ms: 250
    default_behavior: queue
  shell_env_allowlist:
    - PATH
    - HOME
    - LANG
    - TERM
  compact:
    input_char_threshold: 160000
    keep_recent_tool_results: 3
    hysteresis_delta_chars: 40000
  multi_agent:
    enabled: true
    max_depth: 4

hooks:
  default_timeout_sec: 15
```

说明：

- `runtime.guardrails_mode` 支持 `yolo | standard`
- 默认 `yolo`，即关闭 retrieval / project-memory / review-artifact 这类 runtime guard，由模型在工具边界内自主管理
- `standard` 会重新开启这些 runtime reminder / guard，适合更保守、更可控的 operator profile
- `runtime.max_turns_hard: -1` 表示禁用硬性 turn 上限，不再触发 `max_turns_hard_exceeded`
- `runtime.multi_agent.enabled` 默认 `true`
- 默认开启只表示当前 session 会看到 `agent_spawn` / `agent_status` / `agent_list`
- 是否真正创建 child agent 仍由当前 master agent 自行决定；若部署方需要收紧能力面，可显式改成 `false`
- `experimental web` 的 Settings 页面修改 `guardrails_mode`、provider 默认值、API Provider / adapter family、provider reasoning / thinking mode、reasoning summary 和 `max_turns_hard` 时，需要把这些值持久化回当前生效的 config 文件，而不是只停留在进程内存里
- Settings 页面必须用受支持值的下拉选择暴露 Provider Profile、API Provider、reasoning / thinking mode 和 reasoning summary，而不是要求用户手写字段；测试按钮使用当前表单值执行一次 thinking-observation probe，但不得持久化配置

## 8. Provider 配置字段

### 8.1 通用字段

- `api_key_env`
- `api_provider`：协议 / adapter family，例如 `openai-compatible`、`anthropic-compatible`、`google`；Provider map key 只是 Provider Profile 名称
- `base_url`
- `model`
- `timeout_sec`
- `request_timeout_sec`
- `stream_idle_timeout_ms`
- `retry.max_attempts`
- `retry.base_delay_ms`
- `retry.retry_429`
- `retry.retry_5xx`
- `retry.retry_transport`

### 8.2 OpenAI / `openai-compatible`

- `wire_api`
- `temperature`
- `top_p`
- `max_output_tokens`
- `reasoning_effort`：Settings mode `default | low | medium | high | xhigh`，其中 `xhigh` 持久化为 `reasoning_effort: xhigh`
- `reasoning_summary`：Settings summary `Provider default | Auto | Concise | Detailed | Off`，其中 `auto|concise|detailed` 映射到 Responses `reasoning.summary`，`off` 持久化为 `none`
- `text_verbosity`
- `store`
- `send_metadata`

### 8.3 Anthropic-Compatible Messages

- `anthropic_version`
- `temperature`
- `top_p`
- `max_output_tokens`
- `thinking_budget`
- `include_thoughts`
- Settings mode `default | standard | max | off`；`max` 持久化为 `include_thoughts: true`、`thinking_budget: 32000`，并把 `max_output_tokens` 提高到至少 `32768`
- 任意自定义 Provider Profile 只要显式配置 `api_provider: anthropic-compatible`，就使用同一 Messages adapter；未知自定义 profile 若没有 `api_provider` 必须报错，不按名称猜协议

示例：

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

### 8.4 Google

- `temperature`
- `top_p`
- `max_output_tokens`
- `thinking_budget`
- `include_thoughts`
- Settings mode 与 Anthropic 相同，用于 Gemini thinking config

约束：

- provider 选项必须进入 runtime，并写入 session metadata
- session metadata 需要记录 effective `api_provider` 和 `reasoning_summary`，使 continue / probe / doctor 能解释实际 adapter family
- `timeout_sec` 是旧配置兼容字段；新实现优先使用 `request_timeout_sec` 与 `stream_idle_timeout_ms`
- effective timeout/retry policy 也必须写入 session metadata，便于从 durable session 事实中追溯本次运行采用的请求超时、stream idle 超时、retry 预算与开关
- `continue` 不能因为配置漂移而丢失已选择的 generation 语义
- OpenAI / `openai-compatible` 默认 `store: false`，保持本地 session 是唯一事实源
- `wire_api` 只作为 OpenAI-compatible Responses 的 legacy / advanced compatibility 字段保留；默认交互和 Settings 以 `api_provider` 命名解释 adapter family
- `send_metadata` 默认为主契约路径；只有某个非官方 `openai-compatible` 部署明确不兼容 `metadata` 字段时，才应显式设置 `send_metadata: false`
- provider retry 只允许有限次数的 transport / rate-limit / upstream 重试；认证错误、请求格式错误、响应解析错误不能被自动吞掉
- provider call 在没有新工具副作用前遇到 `upstream_timeout` 时，可以按 `runtime.provider_auto_resume` 做有界自动续跑；每次自动续跑必须写入 durable event

## 9. 验收标准

- 核心命令面清晰且稳定
- `run` / `exec` / `steer` / `continue` 语义无冲突
- 配置文件默认简洁，可选 generation 字段不污染最小配置
- 扩展命令仍可用，但不主导 core v1 的默认体验
