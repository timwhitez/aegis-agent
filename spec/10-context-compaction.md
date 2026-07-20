# Go CLI Agent Context Compaction Spec

## 1. 目标

context compaction 的目标不是“省几百 token”，而是保证：

- 长会话不会因为工具输出堆积而迅速失控
- 原始执行轨迹仍然完整可追溯
- CLI / session store / future API 都能看到完整历史
- 发送给 provider 的上下文视图尽量紧凑

## 2. 设计来源

本设计直接吸收 `learn-claude-code` `s06` 的核心思想：

- 旧工具结果不该无限堆积
- 历史太长时应该生成 transcript + summary
- 压缩是 harness 机制，不是 UI 技巧

## 3. v1 约束

v1 不做：

- provider 原生 context compaction API
- 跨 provider handoff 优化
- 多级语义检索式 memory

v1 只做最小但完整的三层压缩。

## 4. 三层压缩

### 4.1 Layer 1 - Tool Output Truncation

每次工具执行后，先在工具层做基础截断：

- `display_output` 可与 `llm_output` 使用不同 byte cap
- 动态 command stdout/stderr 在进程运行时就由有界 collector 消费；超出 inline boundary 的原始字节直接流入 current-session artifact，不允许先无界缓冲、执行后再截断
- collector 内存只保留固定 pending prefix 与 UTF-8-safe head/tail；artifact hard cap 后仍消费并计数，但不可保存的中段明确计入 omitted bytes
- metadata 同时记录 process raw bytes、inline bytes、persisted/omitted bytes、artifact completeness、recoverability 与失败原因

这是最廉价的一层。

### 4.2 Layer 2 - Micro Compaction

准备发送给 provider 的上下文先执行一层受限的 identical-result 去重，再对旧工具结果执行数量/体积窗口：

- 去重 allowlist 只有 `read_file`、`grep`、`grep_files`、`glob`。执行与 fingerprint 共用 typed canonical normalizer；只有 canonical arguments、版本化 cap 前 result-content SHA-256、error/final、artifact/recoverability 与 byte-accounting 语义全部相同才替换更旧 result
- duplicate marker 精确保留旧 `ToolCallID` 与 replay pair，携带 retained call id、result hash、original bytes 和已有 source/artifact reference；同一 tool message 的其他 result 与 assistant sibling call/provider block 不变
- duplicate marker 是已 compacted provider-view result：继续占一个最近 result 位置并计入 compacted count/bytes，但后续 micro-compaction 和第二次 ephemeral-view 处理不得嵌套 marker或恢复 payload；当它落到最近窗口外时仍可按其 call id 裁剪旧 call arguments
- 没有可靠 result hash、canonicalization 失败、tool 不在 allowlist 或任一语义字段不同就保留原结果。文件/搜索状态发生变化时，即使参数相同也不能折叠

- 倒序逐个遍历独立 `ToolResult`；每个 result 占一个最近位置，message/batch 边界不参与计数
- 只有同时满足 `keep_recent_tool_results` 与 `keep_recent_tool_result_bytes` 的最新连续后缀保留完整 `llm_output`。byte 口径为 UTF-8 bytes，exact boundary 可保留；第一个超过 count 或 bytes 的 result 关闭完整后缀，该 result 与更早的非 pointer result 都替换为简短占位摘要（head/tail + 原始长度）
- 同一 tool message 可以混合 compacted 和 inline result；ToolCallID、Name、IsError、Final 与业务 metadata 保持逐项对应
- 已由 current-result budget / ephemeral view pointerize 的 result 占一个最近位置但不消耗完整 payload byte budget，并保持现有 pointer；不得再创建 artifact、嵌套 pointer 或把 partial/unavailable source 改写为 complete
- 原始 `messages.jsonl` 不做覆盖
- compacted result 仍保留对应 assistant `tool_call` / provider call id；只按 ToolCallID 裁剪该 call 的大 arguments/input/args，不触碰同 batch sibling。full-compaction 的最近消息选择若保留 result，也必须保留其 call/replay 依赖链
- 这一层无论总规模是否超阈值都会执行：始终对超出 `keep_recent_tool_results` 的旧大输出做 head/tail 截断，是最廉价的稳定裁剪
- 不得只按 tool name + arguments 推断两个结果语义相同，也不得在 message 级覆盖整批 `tool_results`。相同查询可能在两次调用之间观察到不同文件内容或外部状态；只有上述受限 result-level 证明路径可以去重。

示例：

```text
[Previous tool result: shell exit=0, output truncated, see transcript]
```

### 4.3 Layer 3 - Transcript + Summary

当 provider 输入规模超过阈值时：

1. 将完整历史快照写入 `artifacts/transcripts/`
2. 生成一份 compact summary
3. provider 输入视图改为：
   - 系统说明
   - compact summary
   - 最近 `keep_recent_messages` 条原始消息（成比例于阈值规模，不再固定 6 条；含 tool-call 依赖链）

compact summary 还必须保存 versioned canonical `history_reference`：它指向 current-session-only 的 `read_session_history` 能力和 `messages.jsonl` 事实源，不把 transcript artifact 变成模型侧第二 parser。new compaction、hysteresis reuse 与由旧 artifact 恢复的 summary 都补齐该字段。

compact summary 至少保留（确定性结构化抽取，始终生成）：

- 已完成事项
- 当前工作状态
- 关键文件与路径
- 已读关键 artifact 的简短摘录
- durable project memory stack 的存在状态
- 下一步检索/收敛建议
- 当前 todo 概览
- 当前 ready / blocked tasks 摘要
- 未完成问题
- 最近一次失败或暂停点

### 4.4 可选语义摘要层

确定性结构化抽取是 baseline，始终生成。在其之上，runtime 默认开启一层 provider 语义摘要（`runtime.compact.semantic_summary.enabled`，默认 `true`），用于补回被裁掉的中段消息里的推理 / 决策 / 试错叙事：

- 输入**只**包含被丢弃的中段消息（不含将要保留的最近尾部、不含完整 transcript），按 `semantic_summary.max_input_chars` 截断，避免重复携带完整历史导致的 `context canceled` / 双份上下文问题。
- 使用独立的 `semantic_summary.timeout_sec` 超时与自己的请求预算，单次 `RunTurn`，不带工具、不递归触发压缩。
- 成功时写入 `summary["semantic_summary"]`，compact event 标记 `semantic_summary_status = "ok"`。
- 失败 / 超时 / 关闭时**绝不**使压缩失败：省略该字段、回退到确定性 baseline，并把状态标记为 `failed` / `skipped` / `disabled`。

## 5. 关键设计约束

### 5.1 不覆盖原始日志

这是最重要的约束。

以下文件必须保持原始事实：

- `messages.jsonl`
- `events.jsonl`
- `todo.json`
- `tasks/`

compaction 只影响：

- 发给 provider 的上下文视图
- `artifacts/compactions/summary-*.json`

当前 summary 里允许额外包含：

- `artifact_memory`
  - 最近读过的关键报告 / artifact 的路径与简短摘录
- `high_value_proofs`
  - 最近读过且最可能成为最终结论锚点的关键代码 / 文档切片，至少保留路径、行窗与简短摘录
- `project_memory_stack`
  - `reports/spec.md`
  - `reports/plan.md`
  - `reports/progress.md`
  - `reports/validation.md`
  的 present / missing 状态与简短摘录
- `proof_read_budget`
  - 兼容字段；当前值不表示 runtime 会保留或强制执行 `read_file` 复读预算，模型可按正确性需要读取精确行或更多文件
- `history_reference`
  - 固定包含 schema version、`tool=read_session_history`、source session、`canonical_source=messages.jsonl`、`historical_reference=true` 与 instruction-precedence 说明
  - 只提供可操作的定点恢复入口，不自动注入整段历史，也不规定模型必须在何时读取

### 5.2 压缩是可追踪的

每次压缩都要写事件：

- `compact.started`
- `compact.finished`
- `compact.reused`

并记录：

- 触发原因
- 估算输入规模（含 system prompt 字符）
- 阈值来源（`threshold_source`：`explicit` / `context_window` / `context_profiles.<key>`）、`context_window_tokens`、`utilization_factor`
- 保留的 recent messages 数量（`keep_recent_messages`）
- 语义摘要状态（`semantic_summary_status`：`ok` / `failed` / `skipped` / `disabled`）
- 生成的 artifact 路径
- 是否复用了上一次 compaction 水位
- 当前 project-memory present / missing 状态
- 当前 todo / ready-task / blocked-task / completed-task 计数
- 当前 `proof_read_budget` 兼容摘要，以及 artifact/proof 摘要数量等足以支持 proof-at-boundary 复核的上下文指标

## 6. 触发条件

### 6.0 Context Window 与阈值推导

压缩字符阈值默认由模型的 **context window（token 容量）** 自动推导，而不是写死一个字符数：

- 每个 provider 配置可设 `context_window_tokens`（用户自定义）。
- 未配置时按内置 known-model 表解析（例如 `gpt-5.5 = 300000`）。
- 仍未命中时使用默认 `200000` tokens。
- 字符阈值 = `tokens × 4 × utilization_factor`，其中 `4` 是 v1 `chars ≈ tokens` 近似，`utilization_factor` 默认 `0.85`（留余量给输出与估算误差）。
  - 默认 200k → `200000 × 4 × 0.85 = 680000` 字符。
  - gpt-5.5 300k → `300000 × 4 × 0.85 = 1020000` 字符。

阈值优先级（高 → 低）：

1. `runtime.compact.context_profiles` 命中的显式 `input_char_threshold`
2. `runtime.compact.input_char_threshold`（顶层显式，正数即视为显式选择）
3. 由 `context_window_tokens` / known-model 表 / 200000 默认推导
4. 最终兜底字符阈值

effective context window 在 session 创建时解析并写入 session metadata（`provider_options.context_window_tokens`），保证 `continue` 不因配置漂移而改变运行中的窗口。

配置项：

- `runtime.compact.input_char_threshold`
  - 默认 `0`（表示按 context window 自动推导）；正数表示显式覆盖。
- `runtime.compact.keep_recent_tool_results`
- `runtime.compact.keep_recent_tool_result_bytes`
  - 默认 `65536`；与 result count 同时生效，context profile 可用同名字段覆盖。
- `runtime.compact.hysteresis_delta_chars`
  - 默认 `0`（表示按 `threshold / 4` 自动推导）。
- `runtime.compact.keep_recent_messages`
  - 默认 `0`（表示按阈值规模成比例推导，不再固定保留 6 条）。
- `runtime.compact.utilization_factor`
  - 默认 `0.85`，取值范围 `(0, 1]`。
- `runtime.compact.context_profiles`
  - 可选，按 `provider/model`、`model` 或 `provider` 覆盖上述阈值。
  - 未配置命中时继续使用 context-window 推导的字符阈值，不引入 provider 原生 token 计数或 replay 依赖。

v1 仍用字符数做整体 compaction trigger 的近似估算，不做 provider 精确 token 计数；result byte window 单独使用最终 `llm_output` 的 UTF-8 bytes。context window 只用于推导本地字符阈值，并写入 summary / compact event 作为诊断事实（`threshold_source`、`context_window_tokens`、`utilization_factor`、`keep_recent_messages`）。估算输入规模时还会计入本轮组装的 system prompt 字符数，避免 skills / goal / plan 注入很大时低估真实 provider 输入。

micro-compaction 统计使用互斥分类：带既有 provider-view pointer 的 result 优先计入 `pointerized`，其次是本层标记的 `compacted`，其余计入 `inline`。`inline_tool_result_count/bytes`、`compacted_tool_result_count/bytes`、`pointerized_tool_result_count/bytes` 写入 compact event 与每次 provider request 的 versioned budget snapshot；bytes 均取最终 provider-view `llm_output`，不含 display、metadata 或 durable JSON envelope。

compaction trigger 与最终 provider request hard-fit 是两个独立 gate：字符阈值决定是否值得生成 transcript/summary，不能证明最终 wire request 一定落在 provider 窗口内。每次 main 和 semantic-summary 请求仍需在发送前用 adapter 的真实 wire body 做 token 近似、output reserve 和 safety headroom 判定；probe 同样预检，但不删除唯一 probe prompt 来伪造 fit。

最终 hard-fit 只修改 provider-view clone，并使用固定顺序：

1. 以已经完成的 current-result cap、safe identical-result dedup、result-level micro-compaction 与 full/reused/deferred compaction view 为起点
2. 从旧到新把 recoverable inline result 换成 pointer：优先引用 `artifact_complete=true` 的 session artifact；read_file/search page 只有在原 call arguments 仍存在且 path/range 或 versioned current-view query 可重放时才能使用 source pointer。partial/unavailable artifact 不能升级为 complete
3. 从最老、最低优先级 message 开始删除完整 replay closure；ToolCallID/ProviderCallID alias 对应的 assistant call、provider block 与 tool result 必须一起保留或一起删除
4. 对 compaction summary 先删 optional `semantic_summary`，再删低优先级集合/摘录并生成有界 deterministic core；core 继续保留 current goal、open items、key paths、latest external instruction、latest steer 和 transcript/history reference
5. 每个候选都重新走 adapter estimator；只有 wire body bytes 严格下降才提交并写 `provider.request.budget_action`。固定最大 pass 后或没有合法 action 时返回 typed `request_budget_unfit`

最新 external user message、最新 steer 与最新 tool result replay closure不可被 tail step 删除；tool schema 不进入收缩候选。不可恢复最新 result、不可丢消息、system/schema/envelope 或最小 summary 自身已超过 available input 时，错误报告 blocking component 与 estimated/available/reserved 数值，不复制正文。new compaction、hysteresis reuse 和 `compact.deferred` fallback 都必须经过这一最终 gate；local unfit 不触发 provider retry/auto-resume。

current-result cap、安全 identical-result 去重、old-result micro-compaction 与 full compaction 是四个顺序明确的层次：

1. 当前 ToolResult 在 hook 后、durable append 前执行 per-result byte cap；messages.jsonl 从产生时就是 bounded preview/pointer，原始动态正文只进入有 quota 的 session artifact
2. provider view 在 ephemeral sliding-window 处理后，仅对 allowlisted read-only result 做 canonical arguments + result hash 安全去重；duplicate marker 不回写 durable log
3. provider view 对旧结果做 result-level micro-compaction；优先复用第一层 artifact，不能再创建无 quota 副本
4. 整体仍超阈值时才生成 transcript/summary；该层只改变 provider view，不回写前三层 durable facts

第一层 artifact 完整性必须由 `artifact_complete` 证明；partial/quota/write-failed artifact 不能在第二、三层被重新标成 Full output。

command collector 已在当前 ToolResult 建立 `tool_output_budget_version=1` artifact/preview contract时，old-result ephemeral sliding window只能复用该 pointer。它不得把 bounded preview 或 summary 再落盘成第二份 artifact，也不得把 partial/unavailable source 改写成 complete。只有尚未经过 current-result finalizer 的 legacy/non-command 结果才允许使用同一 quota writer补建 artifact。

read_file byte window、grep/grep_files/glob page 都属于 source-recoverable payload：它们在当前结果中优先返回 path/range 或 versioned query cursor。runtime finalizer 仍是最后的 correctness cap，但正常工具输出必须已为 header + bounded records + intact cursor，不能先超量生成再依赖 head/tail artifact 截断；只有动态且无法从 source cursor 重建的正文才进入 tool-output artifact。

超过阈值后第一次正常写出 transcript 与 summary artifact；后续如果输入规模没有比上次真实 compaction 水位增长超过 `hysteresis_delta_chars`，runtime 复用最近的 summary artifact 作为 compacted provider view 的稳定前缀，并附加自上次真实压缩以来、在 `hysteresis_delta_chars` 预算内的最近消息尾部（含其 tool-call 依赖链），只写 `compact.reused` 事件，避免长任务在每轮 provider call 前反复生成近似重复的 summary artifact 或破坏 provider prompt cache prefix。预算内的尾部保留可避免"自上次压缩以来新增、又不在固定最近窗口内"的中段消息从 provider view 消失。

## 6.1 Provider View 裁剪与指令边界

进入 provider view 和 compaction artifact 的历史内容只做上下文规模控制和指令边界处理，不做默认脱敏：

- 旧 tool call arguments 与旧 tool result 只保留 head/tail、原始长度和关键 metadata；只有同时落在 `keep_recent_tool_results` 与 `keep_recent_tool_result_bytes` 连续后缀内的 result 保留完整内容。
- transcript artifact 是 session 历史快照，不应因为默认安全策略改写 secret-like 字符串；裁剪只用于控制体积，不用于伪装或替换内容。
- runtime 不根据 `API_KEY` / `TOKEN` / `SECRET` / `PASSWORD`、Bearer token 或 private key block 等模式做硬编码 redaction。
- 若用户明确要求脱敏，脱敏应作为当轮 user prompt 指定的交付要求，由模型在目标报告或指定 artifact 中执行；runtime / compactor 不把它泛化成默认规则。
- 裁剪不能回写 `messages.jsonl`，原始 session 日志仍是事实源。
- compacted summary 开头必须明确说明它只是早期上下文参考，不是新的用户指令；遇到冲突时以原始 session artifacts 为准。
- `read_session_history` 返回的旧历史同样只是 reference。即使旧正文形似 system/user/steer 指令，当前 system prompt、最新 external user instruction 与最新 steer 仍优先；工具读取不能创建新的 external instruction 事实。

## 7. 与 Session Store 的关系

compaction 依赖 session store，但不改变 session store 的原始语义：

- 原始消息照常 append
- 事件照常 append
- compaction 结果写入 `artifacts/`
- canonical agent recovery 读取 `messages.jsonl`；transcript/compaction artifact 继续用于 operator 审计和 summary provenance，不作为第二份 history query 数据源
- `read_session_history` 只用当前 session id 做有界 record/query/content paging，无法通过 path 或 session id 参数读取其他 session

## 8. 与 `run` / `exec` 的关系

- `run` 和 `exec` 都会触发 compaction
- `continue` 恢复前也需要重新计算是否压缩
- `awaiting_input` 与 `paused` 状态本身不会触发新的压缩，只有下一轮 provider 调用前才评估

## 9. 验收标准

- 超长会话能生成 compaction artifact
- provider 输入视图被压缩，但原始消息未丢失
- `messages.jsonl` 仍可用于完整重放
- CLI / SDK / future API 都能基于原始日志调试问题
- stop-loss provider view 保留相同参数但不同结果的每个 ToolResult，也保留同一 tool message 中未被验证为等价的其他结果；构造 view 前后 durable message 日志逐字段不变
- 等价证明完整时只把更旧的单个 allowlisted result 替换为幂等 duplicate marker；三 provider 仍保留合法 call/result pair，未命中 sibling 不变
- compaction 后的 main wire request 仍必须通过 request-budget preflight；semantic-summary 自身超预算时只省略语义补充并标记失败，不阻断确定性 summary/transcript 生成
- 初始 request 不 fit 时有界 hard-fit action 的 wire estimate 严格递减；最终 snapshot `fit=true` 才能发送，无法满足则返回 `request_budget_unfit` 且 durable messages/artifacts 不变
- 当前工具结果、hook amplification 与 child handoff 在进入 durable log 前已经受统一 byte cap；old-result pointerization 只复用或通过同一 quota writer 创建 artifact
- new/reused/hard-fit deterministic summary 均保留可操作的 versioned `history_reference`；压缩后可用 current-session-only 工具定点恢复早期摘要或内容页，而 `messages.jsonl` 仍逐字段不变
- history record/query/content page 自身有 record、scan、UTF-8 与总输出预算，随后仍经过 TOOL-002A finalizer 和 CTX-003 hard-fit
