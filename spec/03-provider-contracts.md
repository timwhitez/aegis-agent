# Go CLI Agent Provider Contracts

## 1. 目标

当前项目原生支持三套 provider 协议：

- OpenAI Responses API
- Anthropic Messages API
- Google Gemini `generateContent`

另有一个运维层兼容入口：

- `openai-compatible` + `wire_api=responses`

原则：

- 不把三家协议粗暴压平成一套“万能消息协议”
- 只统一 runtime 真正需要的最小接口
- replay、tool 格式、generation 选项、错误分类由 adapter 负责
- provider 级 transport retry 属于 adapter 责任，不要把重试逻辑散落到 CLI 或 tool 层
- effective timeout/retry policy 需要保留在 session metadata 中，方便后续 validation 直接从 durable session 事实追溯本次运行的请求超时、stream idle 超时与 retry 契约
- continue / resume 重新构造 adapter 时，transport timeout/retry 预算必须优先按 session metadata 中的 effective policy 恢复，而不是被进程当前配置漂移覆盖

## 2. 统一接口

```text
ProviderAdapter
  Name() string
  RunTurn(ctx, TurnRequest, EventSink) -> TurnResult
```

### 2.1 TurnRequest

字段：

- `session_id`
- `model`
- `system_prompt`
- `messages`
- `tools`
- `temperature`
- `top_p`
- `max_output_tokens`
- `api_provider`
- `reasoning_effort`
- `reasoning_summary`
- `text_verbosity`
- `thinking_budget`
- `include_thoughts`
- `store`
- `metadata`

说明：

- 不是每个 provider 都会使用全部字段
- 未使用的字段必须被 adapter 安静忽略，而不是报错
- 这些字段必须可由 config -> runtime -> session metadata -> adapter 全链路传递

### 2.2 TurnResult

字段：

- `text`
- `thinking`
- `provider_content_blocks`
- `tool_calls`
- `stop_reason`
- `usage`
- `provider_response_id`
- `raw_provider`

约束：

- `provider_response_id` 在上游响应提供稳定 id 时应尽量填充；若协议形状确实没有，则允许为空，但不要默默丢掉已存在的上游 id。
- `thinking` 是面向本地 UI / session 浏览的可读推理摘要；不得把它当成跨 provider 的 replay 原语。
- `provider_content_blocks` 保存 provider adapter 认为后续 replay 必须原样带回的 provider-native content blocks，例如 OpenAI encrypted reasoning item、Anthropic thinking signature / redacted thinking block、Gemini thoughtSignature 等；它是 session 文件事实的一部分，但仍由 adapter 独占解释，CLI / Web / tool 层不得硬编码 provider replay 逻辑。
- 本文中的 `redacted_thinking` / redacted thinking block 是 Anthropic 协议里的上游 block 类型名称，不代表本项目要对本地报告、prompt、session、compaction 或 provider view 执行默认脱敏。
- `raw_provider` 至少应保留一个统一键 `provider_stop_reason`，并保留原始来源键（例如 `status`、`stop_reason`、`finish_reason`）供跨 provider 诊断。
- `raw_provider.thinking_strategy` 记录 adapter 本轮实际采用的 thinking / reasoning 请求策略，例如 OpenAI Responses summary、Anthropic-compatible manual budget、Google thinking budget 或 provider default；它是诊断观测字段，不要求 CLI / Web 层据此构造 replay。
- 当 session metadata 中的 `provider_options.raw_sidecar=true` 时，runtime 会把本次 turn 的诊断 envelope 另存为 `.go-cli-agent/sessions/<id>/provider-raw/<turn>.json`。该 sidecar 只包含 provider、model、turn、timestamp、provider_response_id、内部归一化 `stop_reason` 和 adapter 已选择的 raw provider items；它只用于 replay 诊断和审计，不替代 `messages.jsonl` / `events.jsonl`，也不要求 CLI 或 Web 用 provider-native item 续跑。

### 2.3 EventSink

adapter 可向 runtime 发出：

- `assistant.delta`
- `tool.call.ready`
- `provider.error`
- `provider.retry`
- `turn.stopped`

v1 允许 adapter 采用“单次响应 + 事件回放”的伪流式模式。

## 3. StopReason

内部统一 stop reason：

- `tool_use`
- `done_candidate`
- `completed`
- `max_tokens`
- `blocked`
- `cancelled`
- `error`

说明：

- `completed` 只留给显式完成语义
- provider 自然结束通常先映射为 `done_candidate`
- `tool_use` 必须携带至少一个内部 tool call；如果 runtime 在没有 parsed tool calls 的情况下看到 `tool_use`，必须按 provider boundary failure 处理，不能降级为普通等待输入。
- 若 provider 因 `429` / `5xx` / transport timeout 发生有限重试，必须在事件流里留下 `provider.retry` 证据
- 若 provider 在没有新工具副作用前因 `upstream_timeout` 失败，runtime 可以按有界策略自动续跑，并必须留下 `provider.auto_resume` 证据
- runtime 还会把 retry、auto-resume、failure、success 追加到 `provider-attempts.jsonl`；success attempt 会保存 provider 返回的 cache read/write token 计数。该 ledger 只用于诊断、恢复与 WebConsole 展示，不反向驱动 adapter retry policy。

## 4. Replay 规则

### 4.1 OpenAI

- 历史通过 `input` 重放
- 工具结果重放为 `function_call_output`

### 4.2 Anthropic

- 历史通过 `messages` 重放
- 工具结果必须在后续 `user` 消息中以 `tool_result` block 形式返回
- 每个 `tool_use` 都必须有对应 `tool_result`

### 4.3 Google

- 历史通过 `contents` 重放
- 工具调用以 `functionCall` part 表示
- 工具结果通过 `functionResponse` part 回传

## 5. Tool Schema

内部工具契约至少包含：

- `name`
- `description`
- `input_schema`

adapter 负责转换为：

- OpenAI: `type=function`
- Anthropic: `input_schema`
- Google: `functionDeclarations`

## 6. OpenAI Contract

### 6.1 接口与鉴权

- `POST /responses`
- `Authorization: Bearer <OPENAI_API_KEY>`
- 默认 `base_url = https://api.openai.com/v1`

### 6.2 请求映射

- `model` -> `model`
- `system_prompt` -> `instructions`
- `messages` -> `input`
- `tools` -> `tools`
- `temperature` -> `temperature`
- `top_p` -> `top_p`
- `max_output_tokens` -> `max_output_tokens`
- `reasoning_effort` -> `reasoning.effort`
- `reasoning_summary=auto|concise|detailed` -> `reasoning.summary`
- `text_verbosity` -> `text.verbosity`
- `store` -> `store`

当前实现决策：

- OpenAI / `openai-compatible` 默认 `store: false`
- 原因是 session / messages / events 的唯一事实源必须是本地文件，而不是服务端存储
- provider HTTP 层允许按配置做有限 retry，默认面向 `5xx` 和 transport timeout；认证错误与请求错误直接失败
- 当 `reasoning` 对象非空时，请求包含 `include: ["reasoning.encrypted_content"]`，用于无状态 Responses replay；`reasoning_summary=none` 表示显式不请求可读 summary。
- OpenAI / `openai-compatible` 不发送 provider-specific cache marker；若上游返回 `input_tokens_details.cached_tokens` 或兼容的 cache write 计数，adapter 只把它们记录进 usage / raw provider telemetry。

### 6.3 响应映射

从 Responses 输出中提取：

- `output` 中的 message 文本
- `output` 中的 `function_call`
- `output` 中的 `reasoning.summary[]` 与兼容 readable reasoning content，进入 `TurnResult.thinking`
- `output` 中的 `reasoning.encrypted_content`，进入 OpenAI 专用 `provider_content_blocks`，并和同一 reasoning id 的 summary parts、provider output sequence 绑定，供后续 Responses replay 使用
- `status`
- `incomplete_details`
- `usage`，包括 `input_tokens_details.cached_tokens` / cache write 字段（若上游提供）

### 6.4 stop_reason 映射

- `status=completed` 且有 `function_call` -> `tool_use`
- `status=completed` 且无工具调用 -> `done_candidate`
- `incomplete_details.reason=max_output_tokens` -> `max_tokens`
- 非 `completed` 状态必须先按 provider failure / incomplete boundary 处理，并且不得把同一响应里的 `function_call` 暴露为可执行 tool call
- cancel -> `cancelled`
- HTTP / parse error -> `error`

### 6.5 Replay 约束

- 只 replay 同时具备 reasoning `id` 与 `encrypted_content` 的 OpenAI reasoning item。
- summary 只有在它来自同一 reasoning id 并随 encrypted content 一起落盘时才带回；不得从 `Message.thinking` 反向伪造 summary。
- 如果 provider profile、effective API provider 或 model 已改变，默认剥离旧 opaque reasoning continuation fact。
- 对 text + reasoning + tool_call 混合顺序尚未验证的形态，adapter 必须有明确安全降级，避免构造 provider 可能拒绝的 replay history。

## 7. Anthropic-Compatible Messages Contract

### 7.1 接口与鉴权

- `POST /v1/messages`
- `x-api-key: <ANTHROPIC_API_KEY>`
- `anthropic-version: <configured-version>`

### 7.2 请求映射

- `system_prompt` -> `system`
- `messages` -> `messages`
- `tools` -> `tools`
- `temperature` -> `temperature`
- `top_p` -> `top_p`
- `max_output_tokens` -> `max_tokens`
- `thinking_budget` + `include_thoughts=true` -> `thinking`
- `prompt_cache=true` -> provider request cache markers

当前实现里，`thinking` 采用：

```json
{
  "type": "enabled",
  "budget_tokens": 1024
}
```

当前 cache 实现决策：

- `anthropic-compatible` profile 的 `prompt_cache` 默认开启；自定义兼容网关可显式设置 `prompt_cache: false`。
- cache marker 只在 Anthropic adapter 内构造，Web / CLI / tool 层不处理 provider-specific `cache_control`。
- adapter 最多标记四个 cache breakpoint：稳定 system block、最后一个 tool schema、最近两个可缓存 message content block。
- message marker 只加到本轮 provider request view，不回写 `messages.jsonl`，避免把 provider marker 变成 session 事实源。
- compaction summary 仍作为普通 user message 进入 provider view；cache marker 不修改 system prompt，保持“压缩只改变上下文视图，不覆盖原始日志”的项目边界。

### 7.3 响应映射

解析 content blocks：

- `text`
- `thinking`
- `redacted_thinking`
- `tool_use`
- `usage`，包括 `cache_creation_input_tokens` 与 `cache_read_input_tokens`（若上游提供）
- `stop_reason`

### 7.4 replay 约束

- `tool_result` 所在 `user` 消息要紧跟上一条含 `tool_use` 的 assistant 消息
- 若同一条 `user` 消息里既有 `tool_result` 又有普通文本，`tool_result` 必须在前
- 工具失败或中断时，需要 `is_error=true`

### 7.5 stop_reason 映射

- `tool_use` -> `tool_use`
- `end_turn` -> `done_candidate`
- `pause_turn` -> `error`
- `max_tokens` -> `max_tokens`
- cancel -> `cancelled`
- 其他错误 -> `error`
- 若上游返回 `stop_reason=tool_use` 但响应 content 中没有任何可执行的 `tool_use` block，adapter 必须将其归类为 `response_parse_error`，不能把空工具调用结果交给 runtime 当作普通停顿处理。

### 7.6 当前限制

- 当 provider 返回 `thinking` / `redacted_thinking` 且本轮包含 tool use 时，adapter 必须保存 replay 所需的原始块信息，包括 `signature` 与 `data`，并在后续 Messages API replay 中原样带回。
- `Message.thinking` 只承载可读摘要；`signature` / `redacted_thinking.data` 这类 provider-native 续跑事实必须保存在 provider content blocks 中。
- 本 contract 适用于 effective `api_provider: anthropic-compatible`，不要求 provider profile 名称必须是 `anthropic`。DeepSeek/Kimi 等 custom URL profile 只有显式配置为该 API Provider 时才走 Messages adapter。
- 只有 thinking/redacted、没有 text/tool_use 锚点的 assistant message 不应构造成 Anthropic replay history；它可以作为本地诊断事实保留。
- `RawProvider` / message meta 只记录非敏感计数与布尔观测，例如 `thinking_block_count`、`redacted_thinking_count`、`thinking_visible_observed`、`thinking_replay_observed`。

## 8. Google Contract

### 8.1 接口与鉴权

- `POST /v1beta/models/{model}:generateContent`
- `x-goog-api-key: <GEMINI_API_KEY>`
- 默认 `base_url = https://generativelanguage.googleapis.com`

### 8.2 请求映射

- `system_prompt` -> `systemInstruction`
- `messages` -> `contents`
- `tools` -> `tools[].functionDeclarations`
- `temperature` -> `generationConfig.temperature`
- `top_p` -> `generationConfig.topP`
- `max_output_tokens` -> `generationConfig.maxOutputTokens`
- `thinking_budget` + `include_thoughts=true` -> `generationConfig.thinkingConfig`

当前实现里，`thinkingConfig` 采用：

```json
{
  "includeThoughts": true,
  "thinkingBudget": 2048
}
```

### 8.3 响应映射

解析：

- 顶层 `responseId`（若存在）
- `candidates[].content.parts[].text`
- `candidates[].content.parts[].thought`
- `candidates[].content.parts[].thoughtSignature`
- `candidates[].content.parts[].functionCall`
- `finishReason`
- `usageMetadata`

### 8.4 replay 约束

- 工具结果通过 `functionResponse` 回传
- 若 provider 返回了 `functionCall.id`，应尽量原样带回
- 若 provider 返回了 `thoughtSignature`，adapter 应在 session provider content blocks 中保留，并在后续 `contents` replay 中随原 part 带回
- v1 当前使用 `ToolCallID` 保存回放所需的 function id

### 8.5 stop_reason 映射

- 有 `functionCall` -> `tool_use`
- `finishReason=STOP` -> `done_candidate`
- `finishReason=MAX_TOKENS` -> `max_tokens`
- 安全拦截 -> `blocked`
- cancel -> `cancelled`
- 其他错误 -> `error`

### 8.6 当前限制

- `thought=true` 的 part 表示 thought summary，其可读内容在同一 part 的 `text` 字段中；它应进入 `TurnResult.thinking`，不得混入最终 `TurnResult.text`。
- Gemini thought signatures 只作为 provider-native replay 事实保存在 provider content blocks 中，不由 Web / CLI 解释。
- 只有 thoughtSignature / thought、没有普通 text 或 functionCall 锚点的 assistant message 不应构造成 Gemini replay history；它可以作为本地诊断事实保留。
- `RawProvider` / message meta 只记录非敏感计数与布尔观测，例如 `thought_part_count`、`thought_signature_count`、`thinking_visible_observed`、`thinking_replay_observed`。

## 9. 错误分类

adapter 至少要把 provider 错误归类为：

- `auth_error`
- `rate_limit`
- `invalid_request`
- `upstream_timeout`
- `upstream_unavailable`
- `response_parse_error`
- `cancelled`

错误对象至少包含：

- `provider`
- `class`
- `message`
- `status_code`
- `timeout_kind`

## 10. 超时与取消

- 每个 provider 独立 `request_timeout_sec` 与 `stream_idle_timeout_ms`
- `timeout_sec` 只作为旧配置兼容字段，不能继续作为唯一 provider timeout 模型
- effective timeout policy 必须写入 session metadata，并在 continue / resume 时恢复
- 所有请求必须接受 `context.Context`
- 请求超时、等待响应头超时、stream idle 超时应归类为 `upstream_timeout`，并尽量填充 `timeout_kind`
- provider call 在没有新工具副作用前遇到 `upstream_timeout` 时，runtime 可按 `runtime.provider_auto_resume.max_attempts` 做有界自动续跑；超过预算后必须正常 failed
- cancel 后必须尽快返回，不能伪装成普通失败
- provider HTTP 响应体必须有硬上限；超限响应归类为 `response_parse_error`，避免兼容网关或异常上游通过超大 JSON / error body 造成内存型拒绝服务

## 11. Contract Tests

每家 adapter 至少测试：

- 文本输出解析
- tool call 解析
- header / endpoint 构造
- replay 序列化
- generation 字段映射
- stop_reason 映射
- 非 2xx 错误分类
- cancel 传播

## 12. 当前范围边界

- 不做真正的 provider 流式多路渲染
- 不做多模态文件上传
- 不做跨 provider context handoff
- 不做 provider fallback routing
- 不把 provider-native reasoning artifact 当作跨 provider 公共语义；OpenAI / Anthropic / Google 的 replay 只在各自 adapter 内处理。
- 当前不实现 Chat-compatible `reasoning_content` / `reasoning_details` / `reasoning_opaque` adapter；这类 provider 必须等显式 `chat-compatible` adapter，而不能伪装成 OpenAI Responses 或 Anthropic-compatible。
