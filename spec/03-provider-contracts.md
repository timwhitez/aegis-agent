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
- `reasoning_effort`
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
- `tool_calls`
- `stop_reason`
- `usage`
- `provider_response_id`
- `raw_provider`

约束：

- `provider_response_id` 在上游响应提供稳定 id 时应尽量填充；若协议形状确实没有，则允许为空，但不要默默丢掉已存在的上游 id。
- `raw_provider` 至少应保留一个统一键 `provider_stop_reason`，并保留原始来源键（例如 `status`、`stop_reason`、`finish_reason`）供跨 provider 诊断。
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
- 若 provider 因 `429` / `5xx` / transport timeout 发生有限重试，必须在事件流里留下 `provider.retry` 证据
- 若 provider 在没有新工具副作用前因 `upstream_timeout` 失败，runtime 可以按有界策略自动续跑，并必须留下 `provider.auto_resume` 证据
- runtime 还会把 retry、auto-resume、failure、success 追加到 `provider-attempts.jsonl`。该 ledger 只用于诊断、恢复与 WebConsole 展示，不反向驱动 adapter retry policy。

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
- `text_verbosity` -> `text.verbosity`
- `store` -> `store`

当前实现决策：

- OpenAI / `openai-compatible` 默认 `store: false`
- 原因是 session / messages / events 的唯一事实源必须是本地文件，而不是服务端存储
- provider HTTP 层允许按配置做有限 retry，默认面向 `5xx` 和 transport timeout；认证错误与请求错误直接失败

### 6.3 响应映射

从 Responses 输出中提取：

- `output` 中的 message 文本
- `output` 中的 `function_call`
- `status`
- `incomplete_details`
- `usage`

### 6.4 stop_reason 映射

- 有 `function_call` -> `tool_use`
- `status=completed` 且无工具调用 -> `done_candidate`
- `incomplete_details.reason=max_output_tokens` -> `max_tokens`
- cancel -> `cancelled`
- HTTP / parse error -> `error`

### 6.5 当前限制

- v1 不持久化 OpenAI reasoning items
- 因此不依赖服务端 `previous_response_id` 或 reasoning-item replay 来续跑

## 7. Anthropic Contract

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

当前实现里，`thinking` 采用：

```json
{
  "type": "enabled",
  "budget_tokens": 1024
}
```

### 7.3 响应映射

解析 content blocks：

- `text`
- `tool_use`
- `usage`
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

### 7.6 当前限制

- v1 不持久化返回的 thinking blocks 作为单独 replay 事实
- 因此 extended thinking 不是当前默认主路径能力

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
- `candidates[].content.parts[].functionCall`
- `finishReason`
- `usageMetadata`

### 8.4 replay 约束

- 工具结果通过 `functionResponse` 回传
- 若 provider 返回了 `functionCall.id`，应尽量原样带回
- v1 当前使用 `ToolCallID` 保存回放所需的 function id

### 8.5 stop_reason 映射

- 有 `functionCall` -> `tool_use`
- `finishReason=STOP` -> `done_candidate`
- `finishReason=MAX_TOKENS` -> `max_tokens`
- 安全拦截 -> `blocked`
- cancel -> `cancelled`
- 其他错误 -> `error`

### 8.6 当前限制

- v1 不持久化 Gemini thought signatures
- 因此开启 provider-native thinking 后，不把手工 history replay 当作主路径承诺

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
- 不把 provider-native reasoning artifact replay 当作当前默认承诺
