# Go CLI Agent CLI And Config Spec

## 1. 命令面原则

在 Web-first 方向下，CLI 仍需要同时满足三件事：

- 脚本化路径简单
- 阶段输出清楚
- 与 Web 控制台使用同一套 runtime / session / provider / config 事实

因此当前命令面分为两层：稳定 CLI 控制面与高级/兼容命令面。默认产品入口是 Web 控制台，但 CLI 不能退化成内部调试命令。

## 2. Core 命令

Web-first v1 仍保留以下稳定 CLI 命令：

- `go-cli-agent init`
- `go-cli-agent web`
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

- 默认用户工作流从 `go-cli-agent web` 进入
- `run` / `exec` / `steer` / `continue` / `sessions` / `goal` / `tasks` 作为 CLI fallback、脚本化和恢复路径继续稳定支持
- README、帮助文本、live smoke、测试说明以 Web-first 路径为主，同时给出等价 CLI fallback
- `delegate` / `children` / `queue` / `tui` 仍可保留高级入口；Web 可为 queue / children 提供轻量操作和观测，但不把 worker pool/raw payload/isolation tuning 做成默认页面

## 3. 扩展命令

以下命令属于扩展兼容面：

- `go-cli-agent experimental delegate <parent-session-id> [prompt]`
- `go-cli-agent experimental children <session-id>`
- `go-cli-agent experimental queue <submit|list|show|worker>`
- `go-cli-agent experimental tui`
- `go-cli-agent experimental web` 作为旧入口兼容别名，语义等同 `go-cli-agent web`

要求：

- 默认帮助文本展示 `web` 和核心 CLI fallback 命令
- `experimental` 入口只在显式调用 `go-cli-agent experimental` 时展示
- 扩展命令本身不应继续作为默认顶层 operator surface；Web 页面只暴露面向用户的简洁子集

## 4. 参数解析

当前 CLI 继续使用 Go 标准库 `flag`，并保留轻量 interspersed-flags 归一化：

- `run` / `exec` / `continue` / `steer` / `goal` / `tasks` / `experimental delegate` / `experimental children` / `experimental queue submit` / `experimental queue show`
  支持在 `<session-id>` 或 `[prompt]` 前后继续写受支持 flags
- 若 prompt 自身以 `-` 开头，使用 `--` 结束 flag 解析
- 从 stdin 读取 prompt 的入口必须有硬上限；当前上限为 4 MiB，超限时返回明确错误，避免把异常大的管道输入一次性读入内存

## 5. Core 命令定义

### 5.0 `web`

作用：

- 启动本地 Web 控制台
- 提供默认 Session 工作区、Settings、Workspace 只读浏览、Sessions、Skills 等页面，并在 Session inspector 中展示 background jobs / children 事实
- 通过 REST API 发起真实 `start` / `continue` / `steer` / queue job / Goal / Plan Mode 控制

高频参数：

- `--config`
- `--listen`
- `--workers`

默认规则：

- Web 控制台是默认 operator surface，但仍只复用本地文件事实与 runtime 控制面
- listen 地址不是 loopback 时必须输出 LAN 安全提示
- unsafe mutation 必须走本地控制台 guard：Origin / 自定义 header / Content-Type / 请求体大小上限
- `experimental web` 旧入口应保持兼容，避免已有脚本失效

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

默认行为：

- 未显式传入 `--session-dir` 时，`init` 生成的配置应优先满足 session root owner-only 约束。
- 如果当前工作目录位于 WSL `/mnt/...` 这类通常无法可靠执行 POSIX owner-only 权限的挂载路径，`init` 应默认把 `session.dir` 写到用户 home 下的 `.go-cli-agent/sessions`。
- 显式 `--session-dir` 必须按用户输入写入；后续由 `doctor` 报告该目录是否真正支持 owner-only 权限。

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
- `go-cli-agent web` Settings 页面保存的 API key 会持久化到这个 env 文件中；`experimental web` 兼容入口使用同一行为

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
  max_turns_hard: -1
  command_timeout_sec: 300
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
    input_char_threshold: 0
    keep_recent_tool_results: 3
    hysteresis_delta_chars: 0
    keep_recent_messages: 0
    utilization_factor: 0.85
    semantic_summary:
      enabled: true
      max_input_chars: 200000
      timeout_sec: 60
  ephemeral:
    enabled: true
    artifact_dir: .artifacts/tool-outputs
  degeneration:
    enabled: true
    reminder_after: 2
    give_up_after: 4
    detect_low_quality: false
  multi_agent:
    enabled: true
    max_depth: 4
  queue:
    poll_interval_ms: 1000
    auto_worker: true
    reaper_interval_ms: 60000
    lease_stale_after_sec: 900
    background_wait_timeout_sec: 0
  child_budget:
    max_wall_clock_sec: 7200
    max_turns: 1500

hooks:
  default_timeout_sec: 300
```

说明：

- `runtime.guardrails_mode` 支持 `yolo | standard`
- 默认 `yolo`，即关闭 retrieval / project-memory / review-artifact 这类非必要 runtime reminder / guard，由模型在工具边界内自主管理
- `standard` 可以开启更保守的交付一致性、project-memory 协作提示和 review-artifact 检查；但 `read_file` / grep / glob / read-only shell 不按次数或重复窗口设置 runtime 预算，也不因多文件读取被 hard guard 阻断
- `runtime.max_turns_hard: -1` 表示禁用硬性 turn 上限，不再触发 `max_turns_hard_exceeded`
- 默认 `runtime.max_turns_hard` 为 `-1`，master session、child session 与 queue worker session 都不安装固定 turn 数上限；只有显式设置正数时才启用硬性上限
- `runtime.multi_agent.enabled` 默认 `true`
- 默认开启只表示当前 session 会看到 `agent_spawn` / `agent_wait` / `agent_stop` / `agent_prompt` / `agent_status` / `agent_list`
- 是否真正创建 child agent 仍由当前 master agent 自行决定；若部署方需要收紧能力面，可显式改成 `false`
- `runtime.queue.reaper_interval_ms` 默认 `60000`：后台 queue liveness 回收周期。reaper 扫描 `running/` 与 `blocked/`，回收拥有进程已死（如服务重启）或心跳超 `lease_stale_after_sec`（默认 `900`）的孤儿 job——子会话已终态的结算为对应终态、未建会话的重新入队、其余转 `blocked` 并向 parent 写 pending notification；`<= 0` 关闭该回收。`web` 进程同时对僵尸 `running` 会话做 stale-owner reconcile（转 `paused`）
- `runtime.queue.background_wait_timeout_sec` 默认 `0`（不超时）：parked parent 等待后台 child 结果的墙钟上界，超时记录 `session.background.wait_timeout` 并回到 `awaiting_input`；与死锁检测互补——当 unresolved 工作全部不可推进时，runtime 会写入 `parent.coordination.deadlock` 事件并注入一条 `coordination_deadlock` background notification 唤醒模型决策（`agent_prompt` 收敛 / `agent_stop` 停弃 / 自行继续），而不是无声死等
- `runtime.child_budget.max_wall_clock_sec`（默认 `7200`）/ `max_turns`（默认 `1500`）：仅作用于 child / background（有 parent 的）会话的兜底预算，防止单个委派会话无限 loop；root master session 不受此限（沿用 `max_turns_hard`）。任一维度 `0` 表示禁用。超限时 child 以可恢复方式 `paused`（→ queue `blocked`）并通知 parent，由模型决定续跑/收敛/停止，runtime 不替模型决定也不直接判失败
- `go-cli-agent web` 的 Settings 页面修改 `guardrails_mode`、provider 默认值、API Provider / adapter family、provider reasoning / thinking mode、reasoning summary 和 `max_turns_hard` 时，需要把这些值持久化回当前生效的 config 文件，而不是只停留在进程内存里；`experimental web` 兼容入口使用同一行为
- Settings 页面必须用受支持值的下拉选择暴露 Provider Profile、API Provider、reasoning / thinking mode 和 reasoning summary，而不是要求用户手写字段；测试按钮使用当前表单值执行一次 thinking-observation probe，但不得持久化配置
- Settings 页面还可暴露 provider `context_window_tokens` 数值输入，保存时持久化回当前生效的 config 文件
- `runtime.compact.input_char_threshold` 默认 `0`，表示按模型 context window 自动推导字符阈值（`context_window_tokens × 4 × utilization_factor`）；显式正数即覆盖。`hysteresis_delta_chars`、`keep_recent_messages` 默认 `0` 表示自动推导（分别为 `threshold / 4` 与按阈值规模成比例的保留消息数），显式正数覆盖
- 阈值优先级：`runtime.compact.context_profiles` 命中的 `input_char_threshold` > 顶层显式 `input_char_threshold` > 由 `context_window_tokens` / known-model / 200000 推导
- `runtime.compact.utilization_factor` 默认 `0.85`，取值 `(0, 1]`
- `runtime.compact.semantic_summary.enabled` 默认 `true`：对被裁掉的中段消息做一次有界、独立超时的 provider 语义摘要补充确定性结构化摘要；失败 / 超时不会使压缩失败，回退到确定性 baseline
- `runtime.ephemeral.enabled` 默认 `true`：对高频大输出工具超过窗口后的完整 `llm_output` 写入 session 私有 artifact，并在 tool result 中返回可由 `read_file` 显式分页读取的路径；discovery 工具仍跳过该目录
- `runtime.degeneration.enabled` 默认 `true`：当连续 `done_candidate` turn 只有文本、没有 valid tool call，且未接纳 steer/background/finish 时，`reminder_after` 后注入 `degeneration_recovery_required` reminder，`give_up_after` 后用 `model_degeneration_no_progress` 显式停靠或失败；`detect_low_quality` 预留给乱码/重复 token 启发式，默认关闭

## 8. Provider 配置字段

### 8.1 通用字段

- `api_key_env`
- `api_provider`：协议 / adapter family，例如 `openai-compatible`、`anthropic-compatible`、`google`；Provider map key 只是 Provider Profile 名称
- `base_url`
- `model`
- `context_window_tokens`：可选，模型的上下文窗口 token 容量；用于自动推导压缩字符阈值（见下方 compact 说明）。未配置时按内置 known-model 表解析（例如 `gpt-5.5 = 300000`），仍未命中时默认 `200000`
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
- `prompt_cache`：默认开启；设置为 `false` 时 Anthropic-compatible adapter 不发送 `cache_control` marker
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
- provider 返回 `max_tokens` / `max_output_tokens` 且已有部分 assistant 输出时，可以按 `runtime.provider_auto_resume` 做有界自动续跑；runtime 必须先落盘部分输出和 `provider.max_tokens_resume` 证据，再让模型补齐 final/handoff，不允许执行不完整响应中的 tool call

## 9. 验收标准

- Web-first 默认入口清晰且稳定
- CLI fallback 命令面清晰且稳定
- `run` / `exec` / `steer` / `continue` 语义无冲突
- 配置文件默认简洁，可选 generation 字段不污染最小配置
- 扩展命令仍可用，但不主导默认 Web 页面或主验收口径
