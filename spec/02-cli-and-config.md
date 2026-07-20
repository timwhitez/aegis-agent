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
- `web.basic_auth` 同时配置 `username` 与 bcrypt `password_hash` 时，WebConsole 的静态页面、REST API 和 WebSocket upgrade 都必须要求 HTTP Basic authentication；空配置保持本地无认证默认。缺少任一字段或 hash 非 bcrypt 时启动必须失败，避免误以为服务已受保护。该密码只能以 bcrypt hash 落盘，公网或不可信网络仍必须在 HTTPS 后使用 Basic Auth。
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
- 若无显式 `finish`，runtime 默认继续 loop；只有显式暂停、Plan Mode/预算/后台等待或退化停靠时才进入 `awaiting_input`

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
    keep_recent_tool_result_bytes: 65536
    hysteresis_delta_chars: 0
    keep_recent_messages: 0
    utilization_factor: 0.85
    semantic_summary:
      enabled: true
      max_input_chars: 200000
      timeout_sec: 60
  tool_output:
    llm_output_max_bytes: 32768
    display_output_max_bytes: 131072
    artifact_file_max_bytes: 16777216
    artifact_session_max_bytes: 134217728
    artifact_max_files: 256
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
    max_depth: 1
    max_active_children: 4
    cancel_grace_sec: 5
  queue:
    poll_interval_ms: 1000
    auto_worker: true
    reaper_interval_ms: 60000
    lease_stale_after_sec: 900
    background_wait_timeout_sec: 0
  child_budget:
    max_active_runtime_sec: 0
    max_elapsed_sec: 0
    max_turns_per_attempt: 0

hooks:
  default_timeout_sec: 300
```

说明：

- `runtime.guardrails_mode` 支持 `yolo | standard`
- 默认 `yolo`，即关闭 retrieval / project-memory / review-artifact 这类非必要 runtime reminder / guard，由模型在工具边界内自主管理
- `standard` 可以开启更保守的交付一致性、project-memory 协作提示和 review-artifact 检查；但 `read_file` / grep / glob / read-only shell 不按次数或重复窗口设置 runtime 预算，也不因多文件读取被 hard guard 阻断
- `runtime.max_turns_hard: -1` 表示禁用硬性 turn 上限，不再触发 `max_turns_hard_exceeded`
- 默认 `runtime.max_turns_hard` 为 `-1`，master session、foreground child 与 queue worker child 都不安装固定 turn 数上限；只有显式设置正数时才对每次 `Engine.Run` 全局启用硬性上限。`continue` 会开始新的 run，因此该计数按 run 重置；为兼容既有完成语义，若边界 turn 刚产生了 tool result，runtime 最多再允许一个 resolution turn 用于消费结果并 `finish`，该额外 turn 不可再次延长
- `runtime.max_turns_soft` 是同样作用于所有 session 的 per-run 一次性 checkpoint/reminder 阈值；它不停止执行、不改变状态，也不属于预算终态
- `runtime.multi_agent.enabled` 默认 `true`
- 默认开启只表示当前 session 会看到 `agent_spawn` / `agent_wait` / `agent_stop` / `agent_prompt` / `agent_status` / `agent_list`
- 是否真正创建 child agent 仍由当前 master agent 自行决定；若部署方需要收紧能力面，可显式改成 `false`
- `runtime.queue.reaper_interval_ms` 默认 `60000`：后台 queue liveness 回收周期。reaper 扫描 `running/` 与 `blocked/`，回收拥有进程已死（如服务重启）或心跳超 `lease_stale_after_sec`（默认 `900`）的孤儿 job——子会话已终态的结算为对应终态、未建会话的重新入队、其余转 `blocked` 并向 parent 写 pending notification；`<= 0` 关闭该回收。active-child capacity 扫描还会回收 dead/PID-reused direct reservation：Linux 通过 boot id + `/proc/<pid>/stat` starttime 校验真实 owner identity，僵尸 running child 转为 `paused/stale_owner_reconciled` 并写 durable event。`web` 进程同时对其他僵尸 `running` 会话做 stale-owner reconcile（转 `paused`）
- `runtime.queue.background_wait_timeout_sec` 默认 `0`（不超时）：parked parent 等待后台 child 结果的墙钟上界，超时记录 `session.background.wait_timeout` 并回到 `awaiting_input`；与死锁检测互补——当 unresolved 工作全部不可推进时，runtime 会写入 `parent.coordination.deadlock` 事件并注入一条 `coordination_deadlock` background notification 唤醒模型决策（`agent_prompt` 收敛 / `agent_stop` 停弃 / 自行继续），而不是无声死等
- `runtime.multi_agent.max_depth` 默认 `1`，与当前“只有 root master 可以创建 child”的产品边界一致；`max_active_children` 默认 `4`，统一限制 foreground 与 background child 的 active 数量，不能被大量 queued jobs 或 `agent_prompt` resume 绕过。新 spawn、queue claim、direct resume、blocked queue resume、budget extension resume 都必须在同一 durable `claim.lock` 下按 root 原子占位；容量不足时不得先修改 effective budget/attempt。queue worker count、active child cap 与 nesting depth 是三个独立边界
- `runtime.child_budget.max_active_runtime_sec` / `max_elapsed_sec` / `max_turns_per_attempt` 默认均为 `0`（关闭），仅作用于有 parent 的 child/background session。active runtime 累计 run lease 打开期间的实际执行时间（包括 provider/tool/hook/shell 与 harness 自身处理），但不累计 paused、awaiting input、background wait、queue wait 或进程 offline 时间；`max_elapsed_sec` 在创建 job/session 时固化为绝对 `deadline_at`，上述等待时间都会推进该边界；turn limit 只在当前 budget attempt 内计数
- `runtime.child_budget.active_runtime_checkpoint_ms` 默认 `1000`，只在 active-runtime dimension 启用时生效，允许范围 `100..60000`。它控制 running usage/remaining 的 durable checkpoint 频率，并作为 crash 后单个未闭合 interval 的最大保守补记值；不是新的预算维度，也不会让 offline 时间持续累计。checkpoint ledger 写入失败时 runtime 必须取消当前 active operation 并失败收敛，不能继续执行未受 durable accounting 保护的副作用
- 旧字段 `max_wall_clock_sec` / `max_turns` 继续兼容读取，并分别迁移为 `max_active_runtime_sec` / `max_turns_per_attempt`；新写入统一只使用 canonical 字段，避免双字段漂移
- child/job 创建时快照 versioned `effective_budget`；Settings/config 热更新默认只影响之后创建的 child/job。budget-paused child 只有在 parent 通过 `agent_prompt.budget_extension` 追加或清除已耗尽维度后才能开始新 attempt
- `agent_stop` 可按 `session_id` 或 `queue_job_id` 取消当前 parent 名下的 queued/running/paused child。queued 与最终取消 outcome 使用 `cancelled`；budget-paused blocked job settle 后 queue job 为 `cancelled`，child 保留 paused budget evidence；execution error 继续使用 `failed`
- `go-cli-agent web` 的 Settings 页面修改 `guardrails_mode`、provider 默认值、API Provider / adapter family、provider reasoning / thinking mode、reasoning summary、`max_turns_hard` 和 optional child budget 时，需要把这些值持久化回当前生效的 config 文件，而不是只停留在进程内存里；`experimental web` 兼容入口使用同一行为
- Settings 页面必须用受支持值的下拉选择暴露 Provider Profile、API Provider、reasoning / thinking mode 和 reasoning summary，而不是要求用户手写字段；测试按钮使用当前表单值执行一次 thinking-observation probe，但不得持久化配置
- Settings 页面还可暴露 provider `context_window_tokens` 数值输入，保存时持久化回当前生效的 config 文件
- `runtime.compact.input_char_threshold` 默认 `0`，表示按模型 context window 自动推导字符阈值（`context_window_tokens × 4 × utilization_factor`）；显式正数即覆盖。`hysteresis_delta_chars`、`keep_recent_messages` 默认 `0` 表示自动推导（分别为 `threshold / 4` 与按阈值规模成比例的保留消息数），显式正数覆盖
- `runtime.compact.keep_recent_tool_results` 默认 `3`，按 provider view 中倒序的独立 `ToolResult` 计数，不按 `tool` message/batch 计数。每个 result 都占一个最近位置；只有位于最近窗口内且未超过 byte budget 的连续后缀可以保留完整 `llm_output`
- `runtime.compact.keep_recent_tool_result_bytes` 默认 `65536`，限制上述完整 result 连续后缀的 `llm_output` UTF-8 bytes 合计；exact boundary 可保留，`+1` byte 会关闭完整后缀并压缩该 result 及更早的非 pointer result。已经 pointerize 的 result 保持原 pointer、占一个最近位置，但不消耗完整 payload byte budget
- `runtime.compact.context_profiles` 可按 provider/model、model 或 provider 覆盖 `keep_recent_tool_results` 与 `keep_recent_tool_result_bytes`；未提供或非正值时继承顶层 normalized 默认值
- 阈值优先级：`runtime.compact.context_profiles` 命中的 `input_char_threshold` > 顶层显式 `input_char_threshold` > 由 `context_window_tokens` / known-model / 200000 推导
- `runtime.compact.utilization_factor` 默认 `0.85`，取值 `(0, 1]`
- `runtime.compact.semantic_summary.enabled` 默认 `true`：对被裁掉的中段消息做一次有界、独立超时的 provider 语义摘要补充确定性结构化摘要；失败 / 超时不会使压缩失败，回退到确定性 baseline
- 最终 provider request hard-fit 使用 session metadata 中已快照的 `context_window_tokens` 作为 effective window；`<= 0` 或旧 session 缺失该字段时继续通过 known-model / `200000` 默认值解析，保证旧配置与旧 session 有确定行为
- output reserve 优先使用 effective `max_output_tokens`；其值 `<= 0`（包括旧配置缺失或负值）时使用本地默认 `8192` tokens。safety headroom 由 `runtime.compact.utilization_factor` 推导：`effective_window × (1-utilization_factor)`，默认保留 15%。预算判定统一为 `estimated_input + output_reserve + safety_headroom <= effective_window`
- 估算基于 adapter 真正发送的 JSON wire body 字节数，以 `ceil(bytes / 4)` 做 v1 input token 近似；这是 fail-closed hard-fit 的单一公式，不在 Web/CLI/tool 层复制 provider JSON，也不声称是精确 tokenizer
- `runtime.ephemeral.enabled` 默认 `true`：对高频大输出工具使用 provider-view 滑动窗口；每种工具最新 `EphemeralWindow` 个结果和短输出保持 inline，只有更老且较大的结果在 provider request view 中替换为 session 私有 artifact 路径。原始 messages/events 不被改写，当前结果不会 pointer-only；discovery 工具仍跳过该目录
- `runtime.tool_output` 是 hook 后、落盘前的统一 ToolResult byte policy。旧配置未声明该段时使用以下默认值：`llm_output_max_bytes=32768`、`display_output_max_bytes=131072`、`artifact_file_max_bytes=16777216`、`artifact_session_max_bytes=134217728`、`artifact_max_files=256`
- byte 字段 `<=0` 使用默认值；正数分别 clamp 到：LLM/Display `512..1048576` / `512..4194304`，单 artifact `1024..67108864`，session artifact 总字节 `1024..1073741824`。`artifact_max_files` clamp 到 `1..4096`。单文件和 session 总量独立生效，因此部署方可让 session 总量小于单文件上限，实际可写量取剩余额度
- `runtime.tool_output` 限制的是 UTF-8/byte payload，不用字符数近似。`llm_output` 与 `display_output` 分别受限；artifact 只保存超出模型 inline budget 的原始 `llm_output`，UI 展示不能借 `display_output` 绕过内存/持久化边界
- artifact quota 以 session artifact 目录事实为准，在跨 Store/进程文件锁内重新统计；重启后不依赖内存 counter。quota、symlink、磁盘错误都必须得到有界结果与明确 metadata，不能让工具执行因“保存完整输出”失败而再次把原文落入 `messages.jsonl`
- `runtime.ephemeral.artifact_dir` 继续作为 tool-output artifact root 的兼容配置；默认 `.artifacts/tool-outputs` 映射到当前 session 的 `artifacts/tool-outputs/`。无论使用默认还是兼容自定义 root，写入都必须经过 Store 的 mode/symlink/quota gate
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
