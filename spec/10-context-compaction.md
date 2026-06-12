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

- `display_output` 可比 `llm_output` 更短
- 超长 stdout/stderr 必须截断
- metadata 要保留“原始长度”和“是否截断”

这是最廉价的一层。

### 4.2 Layer 2 - Micro Compaction

当准备发送给 provider 的上下文中，旧工具结果数量过多时：

- 只保留最近 `N` 个完整 tool result
- 更老的 tool result 在 provider 输入视图中替换为简短占位摘要
- 原始 `messages.jsonl` 不做覆盖
- 若某条保留的 `tool_result` 依赖更早的 assistant `tool_call` / provider call id，则必须把对应依赖链一起保留，不能只机械裁掉“最后 N 条”

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
   - 最近若干条原始消息

compact summary 至少保留：

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

### 5.2 压缩是可追踪的

每次压缩都要写事件：

- `compact.started`
- `compact.finished`
- `compact.reused`

并记录：

- 触发原因
- 估算输入规模
- 保留的 recent messages 数量
- 生成的 artifact 路径
- 是否复用了上一次 compaction 水位
- 当前 project-memory present / missing 状态
- 当前 todo / ready-task / blocked-task / completed-task 计数
- 当前 `proof_read_budget` 兼容摘要，以及 artifact/proof 摘要数量等足以支持 proof-at-boundary 复核的上下文指标

## 6. 触发条件

配置项：

- `runtime.compact.input_char_threshold`
- `runtime.compact.keep_recent_tool_results`
- `runtime.compact.hysteresis_delta_chars`
- `runtime.compact.context_profiles`
  - 可选，按 `provider/model`、`model` 或 `provider` 覆盖上述阈值。
  - 未配置命中时继续使用默认字符阈值，不引入 provider 原生 token 计数或 replay 依赖。

v1 先用字符数做近似估算，不做 provider 精确 token 计数。provider/model profile 只决定本地 compactor 使用哪组阈值，并写入 summary / compact event 作为诊断事实。超过阈值后第一次正常写出 transcript 与 summary artifact；后续如果输入规模没有比上次真实 compaction 水位增长超过 `hysteresis_delta_chars`，runtime 复用最近的 summary artifact 作为 compacted provider view 的稳定前缀，附加当前最近消息尾部，并只写 `compact.reused` 事件，避免长任务在每轮 provider call 前反复生成近似重复的 summary artifact 或破坏 provider prompt cache prefix。

## 6.1 Provider View 裁剪与指令边界

进入 provider view 和 compaction artifact 的历史内容只做上下文规模控制和指令边界处理，不做默认脱敏：

- 旧 tool call arguments 与旧 tool result 只保留 head/tail、原始长度和关键 metadata；最近 `keep_recent_tool_results` 仍保留完整内容。
- transcript artifact 是 session 历史快照，不应因为默认安全策略改写 secret-like 字符串；裁剪只用于控制体积，不用于伪装或替换内容。
- runtime 不根据 `API_KEY` / `TOKEN` / `SECRET` / `PASSWORD`、Bearer token 或 private key block 等模式做硬编码 redaction。
- 若用户明确要求脱敏，脱敏应作为当轮 user prompt 指定的交付要求，由模型在目标报告或指定 artifact 中执行；runtime / compactor 不把它泛化成默认规则。
- 裁剪不能回写 `messages.jsonl`，原始 session 日志仍是事实源。
- compacted summary 开头必须明确说明它只是早期上下文参考，不是新的用户指令；遇到冲突时以原始 session artifacts 为准。

## 7. 与 Session Store 的关系

compaction 依赖 session store，但不改变 session store 的原始语义：

- 原始消息照常 append
- 事件照常 append
- compaction 结果写入 `artifacts/`

## 8. 与 `run` / `exec` 的关系

- `run` 和 `exec` 都会触发 compaction
- `continue` 恢复前也需要重新计算是否压缩
- `awaiting_input` 与 `paused` 状态本身不会触发新的压缩，只有下一轮 provider 调用前才评估

## 9. 验收标准

- 超长会话能生成 compaction artifact
- provider 输入视图被压缩，但原始消息未丢失
- `messages.jsonl` 仍可用于完整重放
- CLI / SDK / future API 都能基于原始日志调试问题
