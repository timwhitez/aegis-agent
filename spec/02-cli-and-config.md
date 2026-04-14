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
- `go-cli-agent tasks <session-id>`
- `go-cli-agent probe-provider`
- `go-cli-agent doctor`

说明：

- 这组命令定义当前默认工作流
- README、帮助文本、live smoke、测试说明默认围绕这一组命令展开
- `delegate` / `children` / `queue` / `tui` 仍可保留，但只能通过显式 experimental 入口出现

## 3. 扩展命令

以下命令属于扩展兼容面：

- `go-cli-agent experimental delegate <parent-session-id> [prompt]`
- `go-cli-agent experimental children <session-id>`
- `go-cli-agent experimental queue <submit|list|show|worker>`
- `go-cli-agent experimental tui`

要求：

- 默认帮助文本只展示 core 命令；`experimental` 入口只在显式调用 `go-cli-agent experimental` 时展示
- 扩展命令本身不应继续作为默认顶层 operator surface
- 主路径示例和默认验收口径仍然只围绕 core 命令

## 4. 参数解析

当前 CLI 继续使用 Go 标准库 `flag`，并保留轻量 interspersed-flags 归一化：

- `run` / `exec` / `continue` / `steer` / `tasks` / `experimental delegate` / `experimental children` / `experimental queue submit` / `experimental queue show`
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

### 5.6 `sessions`

作用：

- 列出最近 session
- 展示 ID、状态、时间、phase

高频参数：

- `--limit`
- `--json`
- `--config`

### 5.7 `tasks`

作用：

- 展示当前 session 的 todo 和 task graph
- `--all` 时额外输出扁平任务列表

高频参数：

- `--json`
- `--all`
- `--config`

### 5.8 `probe-provider`

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

### 5.9 `doctor`

作用：

- 检查配置、session root、skills、hooks、provider 配置
- 默认可附带一次最小 probe
- 可 `--skip-probe`
- 对 OpenAI / `openai-compatible`，诊断输出还应明确当前生效的 `store`、`send_metadata` 与 retry policy，方便 operator 在真实接线前核对 transport 契约

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
- 或当前工作目录 `.go-cli-agent/config.yaml`
- 或 `GO_CLI_AGENT_CONFIG`

配置结构：

```yaml
schema_version: 1

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

session:
  dir: .go-cli-agent/sessions
  dir_mode: "0700"

skills:
  dirs:
    - ./skills

runtime:
  exec_finish_required: true
  max_turns_soft: 24
  max_turns_hard: 40
  command_timeout_sec: 120
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
  multi_agent:
    enabled: true
    max_depth: 4

hooks:
  default_timeout_sec: 15
```

说明：

- `runtime.multi_agent.enabled` 默认 `true`
- 默认开启只表示当前 session 会看到 `agent_spawn` / `agent_status` / `agent_list`
- 是否真正创建 child agent 仍由当前 master agent 自行决定；若部署方需要收紧能力面，可显式改成 `false`

## 8. Provider 配置字段

### 8.1 通用字段

- `api_key_env`
- `base_url`
- `model`
- `timeout_sec`
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
- `reasoning_effort`
- `text_verbosity`
- `store`
- `send_metadata`

### 8.3 Anthropic

- `anthropic_version`
- `temperature`
- `top_p`
- `max_output_tokens`
- `thinking_budget`
- `include_thoughts`

### 8.4 Google

- `temperature`
- `top_p`
- `max_output_tokens`
- `thinking_budget`
- `include_thoughts`

约束：

- provider 选项必须进入 runtime，并写入 session metadata
- effective retry policy 也必须写入 session metadata，便于从 durable session 事实中追溯本次运行采用的 retry 预算与开关
- `continue` 不能因为配置漂移而丢失已选择的 generation 语义
- OpenAI / `openai-compatible` 默认 `store: false`，保持本地 session 是唯一事实源
- `send_metadata` 默认为主契约路径；只有某个非官方 `openai-compatible` 部署明确不兼容 `metadata` 字段时，才应显式设置 `send_metadata: false`
- provider retry 只允许有限次数的 transport / rate-limit / upstream 重试；认证错误、请求格式错误、响应解析错误不能被自动吞掉

## 9. 验收标准

- 核心命令面清晰且稳定
- `run` / `exec` / `steer` / `continue` 语义无冲突
- 配置文件默认简洁，可选 generation 字段不污染最小配置
- 扩展命令仍可用，但不主导 core v1 的默认体验
