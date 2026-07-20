# Context Compaction And Harness Audit Issues

审计目标：60415f935c872d292909c3125f84738c31631c21。

二次复核基线：`b15ccbb671c8e340693830aae4d22cf673d4a091`（该提交只更新审计文档，runtime 代码仍为上述审计目标）。复核日期：2026-07-20。

审计范围：spec/00-product.md、spec/01-runtime-architecture.md、spec/02-cli-and-config.md、spec/03-provider-contracts.md、spec/04-tools-and-skills.md、spec/07-testing-strategy.md、spec/09-phase-plan.md、spec/10-context-compaction.md、spec/11-spec-audit-and-traceability.md、spec/12-task-system.md、spec/13-live-input-and-steering.md、spec/14-multi-agent-and-isolation.md，以及当前 compaction、provider request、tool registry、session artifact、delegation/role 与相关测试实现。

参考文章用于提出检查问题：是否先剪枝再展开、超长输出能否恢复、压缩后能否定点回捞、探索是否可隔离、主/子上下文是否可观测。文章中的 Codex/Claude 窗口、成本和调用次数不是本仓库的实测数据，本审计不把这些数值直接当作本项目事实。

## 结论

当前实现已经具备较好的渐进式披露和长会话基础，不需要照搬文章中的外部 harness：

- grep_files 默认只返回候选文件路径；grep 用于精确行号与片段。
- read_file 支持 offset/limit，默认及最大窗口均为 120 行。
- glob 有默认/最大结果数，并通过多取一个结果区分“恰好等于 limit”和“确有更多结果”。
- 旧的 ephemeral tool output 会从 provider view 移出，写入 owner-only 的 artifacts/tool-outputs/，并给出可分页读取的路径。
- compaction 不覆盖 messages.jsonl / events.jsonl；已有 transcript、结构化 summary、可选语义 summary、最新外部指令保留、tool replay 依赖保留和 hysteresis reuse。
- agent_spawn 默认可用，描述已提示 broad investigation、context control 和 independent validation；同步 child 会阻塞父级直到稳定返回，后台路径有 resume_parent 与 agent_wait。
- child 使用独立 fresh session，不继承 parent 的完整消息轨迹；planner / generator / evaluator 已支持 role-specific provider override。

在这些基线上仍确认 9 项优化需求：2 项 P0、5 项 P1、2 项 P2。两项 P0 是 provider-view 正确性与请求可发送性问题，应先处理；P2 explorer/telemetry 属于 large-project / advanced profile，不应扩大默认 Web 首页或变成强制委派 workflow。

| ID | Priority | 主题 | 主要风险 |
| --- | --- | --- | --- |
| CTX-001 | P0 | Tool-result 去重契约错误 | 漏掉真实重复读取，并可能抹掉同批唯一证据 |
| CTX-002 | P1 | keep_recent_tool_results 按 message batch 计数 | 并行工具批次可绕过 micro-compaction 预算 |
| CTX-003 | P0 | Compaction 后无最终 request hard-fit | summary/recent tail 仍可能超过 provider 窗口 |
| TOOL-001 | P1 | Shell/skill command 输出先无界缓冲再有损截断 | 内存峰值不可控，截断中段无法恢复 |
| TOOL-002 | P1 | read_file/grep 缺少每结果总字节预算 | 少量超长行即可灌入大块 provider context |
| TOOL-003 | P1 | grep/grep_files 集合截断不可见 | 模型可能把不完整候选集当完整事实 |
| CTX-004 | P1 | 压缩历史没有受控回捞工具 | 压缩后只能依赖 summary 或重新探索仓库 |
| HARNESS-001 | P2 | 缺少 first-class read-only explorer profile | 有 child 隔离能力，但缺少低噪声探索契约 |
| OBS-001 | P2 | 缺少上下文预算与 root/child 对比 telemetry | 无法量化压缩、工具剪枝和委派是否有效 |

建议按以下顺序推进，每一项都先同步相应 spec，再写实现和永久回归：

1. CTX-001、CTX-003：先修 provider-view 正确性与 hard-fit。
2. CTX-002、TOOL-001、TOOL-002、TOOL-003：收敛工具输出的计数、持久化和可恢复边界。
3. CTX-004：为 compaction 后的定点恢复补最小只读入口。
4. HARNESS-001、OBS-001：作为 large-project / advanced profile 增强，不改变 Web-first 默认产品面。

## 二次准确性复核

二次复核逐项对照了当前 schema、provider request 组装、compaction、tool-after hook、session Store 分页、child role/provider override、Web Settings 与现有永久测试。9 项均能由当前实现直接证实，没有发现误报；优先级也保持不变。实施时需要补入以下已存在能力和遗漏边界，避免重复造轮子或只修主请求的一半：

| ID | 复核结论 | 实施口径补充 |
| --- | --- | --- |
| CTX-001 | 准确 | 去重在每轮 provider view 都运行；应先独立止血，再决定永久移除或做 result-level 安全重实现。 |
| CTX-002 | 准确 | 最近窗口必须按独立 `ToolResult` 与总字节双重计量，并允许同一 batch 内混合 inline/compacted。 |
| CTX-003 | 准确 | `semanticSummaryFunc` 的辅助 provider call 也绕过 hard-fit 与完整 request telemetry，必须纳入同一预算设施。 |
| TOOL-001 | 准确 | 当前旧 ephemeral artifact 保存的是已经截断的 `LLMOutput`；只有原始字节完整落盘时才能标为 full output。 |
| TOOL-002 | 准确 | `internal/fileutil.ReadRegularFileRangeNoSymlink` 和 Web range read 已存在；byte continuation 必须复用该安全入口。通用结果上限应位于 `tool.after` hook 之后。 |
| TOOL-003 | 准确 | 先补 `limit+1` 与集合完整性 metadata，再由 TOOL-002 扩展总字节 stop reason/continuation。 |
| CTX-004 | 准确 | `Store.LoadMessagesTail` / `LoadMessagesBefore` 与 Web message pagination 已存在；新工具应复用 canonical `messages.jsonl` 分页，不另写 transcript parser。 |
| HARNESS-001 | 准确 | explorer 仍须 model-led；tool allowlist 要同时约束 provider schema 与执行层，并完整接入 role provider/config/Web Settings round-trip。 |
| OBS-001 | 准确 | 必须直接复用 CTX-003 的 `RequestBudgetSnapshot`，否则 hard-fit 与观测会再次产生两套漂移估算。 |

本复核没有把文章中的成本、token 曲线或外部产品默认行为转写为本项目事实，也没有建议通过 runtime guard 强制委派、固定探索路线或等待策略。

## CTX-001 — Tool-result 去重器使用过时参数契约，并可能删除同批唯一证据

- Severity: P0
- Confidence: High
- Status: Resolved

### Evidence

- internal/runtime/compaction.go:50-55 在每次构造 provider view 时先执行 deduplicateToolResults，再判断是否达到 compaction threshold；因此该逻辑不是只在真正压缩时才生效。
- internal/runtime/compaction.go:882-898 为 read_file 生成 key 时读取 args["file_path"]，而当前真实 schema 与执行结构使用 path，见 internal/tools/registry.go:888-915。真实 read_file 调用因此没有去重 key。
- internal/runtime/compaction_test.go:1604-1641 的唯一去重测试仍构造 {"file_path":"/test/file.go"}，与当前工具契约不一致，导致测试无法发现上述失配。
- internal/runtime/compaction.go:899-911 的 grep key 只包含 pattern/path，忽略 include/limit；glob key只包含 pattern，忽略 path/include/limit；grep_files 没有 key。
- internal/runtime/compaction.go:860-879 只按请求 key 和 assistant message index 判断重复，不比较对应结果内容。同一路径在两次读取之间发生变化，或同一 grep 请求在文件变化后返回不同证据时，旧结果仍会被视为重复。
- internal/runtime/compaction.go:927-945 命中一个重复 key 后会遍历旧 assistant message 的全部 ToolCalls，并把命中的 tool message 中全部 ToolResults 都替换成 duplicate marker，而不是只替换对应 ToolCallID。
- internal/runtime/engine.go:1056-1097 会把同一 assistant turn 的多个 tool result 汇总到一条 NewToolMessage(toolResults)，所以“一条 tool message 含多个独立结果”是正常运行路径。

### Why it matters

read_file 的去重当前基本失效，重复读取仍持续占用上下文；grep/glob 又可能把参数不同或结果已经变化的调用误判为重复。更严重的是，一个并行批次中只要有一个 call 被判重，同批其他唯一结果也可能从 provider view 消失。原始 messages.jsonl 虽仍在磁盘，但当前模型看到的证据已经被错误改写，可能据此形成不完整或过时结论。

### Root cause

去重器维护了一套脱离 tool schema 的手写参数 key，并把“重复 assistant message”当成“重复 tool result”。它没有以 ToolCallID 关联单个 call/result，也没有把结果内容或版本变化纳入等价判断。

### Recommended direction

- 在修复完整 fingerprint 前，最安全的短期处理是关闭有歧义的通用去重，只保留已有 micro-compaction。
- 长期应从当前 Definition/InputSchema 或经过规范化的实际参数生成 canonical request fingerprint，统一处理省略默认值与显式默认值。
- 只有 tool name、canonical arguments 和结果内容 hash 都一致时才折叠；read_file/grep/glob 读取到不同内容时必须保留历史变化。
- 折叠必须精确到 ToolCallID/ToolResult，不得遍历并覆盖同批其他结果。
- duplicate marker 应保留原始长度、结果 hash 和 retained call id，便于调试；不得改写 durable messages.jsonl。

### Acceptance criteria

- 使用真实 path 字段的重复 read_file 调用可以被测试覆盖；file_path 不再作为现行契约。
- 相同请求且结果 hash 相同，只压缩较旧的对应 ToolResult；相同请求但结果不同，两个结果都保留。
- grep 的 include/limit 和 glob 的 path/include/limit 参与 canonical fingerprint；grep_files 有明确、经过测试的处理策略。
- 一个 assistant turn 同时发出两个或更多 tool call 时，只折叠被判重的 ToolCallID，其他结果逐字保持。
- OpenAI、Anthropic、Google 的 call/result replay 结构在去重后仍合法配对。
- 永久回归覆盖：真实 schema、文件变化、参数变化、并行 multi-call batch、同结果重复、不同结果重复，以及 durable messages 不被改写。

### Resolution

- CTX-001A 已由 `1f00225 fix(runtime): disable unsafe tool result deduplication` 移除旧的 message-level、参数猜测式去重路径；micro/full compaction 在安全实现完成前继续独立工作
- CTX-001B 只为 `read_file`、`grep`、`grep_files`、`glob` 启用 result-level 去重；typed decoder/normalizer 同时供真实工具执行与 canonical fingerprint 使用，并覆盖 tool、normalized path、line/byte range、pattern/include、effective count/byte limit 和 cursor
- ToolResult finalizer 记录版本化的 cap 前 `llm_output` SHA-256、原始/inline byte 数与 hash source；只有 `pre_budget_llm_output` 可作为去重证明，缺 hash 或仅有 legacy inline hash 时 fail closed
- 等价判断同时核对 tool name、canonical arguments、result hash、error/final、artifact completeness/recoverability、byte accounting 与 source/skill 语义；只替换更旧的单个 ToolResult，marker 保留旧 ToolCallID、业务 metadata、retained call id、hash、原始长度和 source/artifact reference
- 永久回归覆盖真实 read_file/grep 执行期间文件变化、默认值/cap 与所有参数差异、同/异结果、error/success、complete/truncated、ProviderCallID alias、mixed multi-call batch、重复 provider-view build、micro-compaction、三 provider replay，以及 `messages.jsonl` byte-for-byte 不变
- Resolution commit：本任务提交 `feat(runtime): deduplicate identical read only tool results`

### Non-goals

- 不根据任务路线、审计步骤或模型阅读顺序强制去重。
- 不把同一路径的所有历史读取都视为可丢弃；文件变化本身可能是关键事实。

## CTX-002 — keep_recent_tool_results 实际按 tool message batch 数量计算

- Severity: P1
- Confidence: High
- Status: Resolved

### Evidence

- spec/10-context-compaction.md:42-50 规定只保留最近 keep_recent_tool_results 个完整 tool result，并明确要求保留相应 call/replay 依赖。
- internal/runtime/compaction.go:641-651 收集的是 Role=tool 且含结果的 message index，然后保留最近 keepRecent 个 index。
- internal/runtime/compaction.go:680-695 对 oldIndices 中的整条 message 才逐项压缩结果；窗口边界不能切分同一 message 内的结果。
- internal/runtime/engine.go:1056-1097 将同一模型 turn 的全部 toolResults 作为一个 NewToolMessage 持久化。一个 message 可以包含一个结果，也可以包含数十个并行结果。

### Why it matters

配置名和 spec 表达的是“结果数量”，实现却是“执行批次数量”。默认保留 3 个 batch 时，三个大并行批次可能留下几十个完整结果，micro-compaction 预算失去可预测性；单调用 turn 和多调用 turn 在同一配置下会得到完全不同的 provider-view 体积。

### Root cause

micro-compaction 以 session.Message 为最小裁剪单位，而 tool replay 的语义单位是 ToolCallID 对应的单个 ToolResult。实现为方便保留整批，未建立 result-level 窗口与依赖映射。

### Recommended direction

- 以独立 ToolResult 为计数和体积预算单位，而不是 tool message index。
- 在同一 batch 内允许“最近 N 个结果保持 inline、其余结果 pointerize/compact”的混合状态。
- 对每个结果保留对应 assistant tool call/provider opaque block 的 replay 配对；只压缩旧 call arguments/payload，不删除协议所需 ID。
- 将数量上限与总字节上限组合使用，避免 N 个单结果本身仍很大。

### Acceptance criteria

- keep_recent_tool_results=3 时，无论结果如何分批，最多只有最近 3 个独立结果保留完整 payload。
- 同一 tool message 内跨越窗口边界时，旧结果可被单独压缩，较新结果保持完整，其他 metadata 不串位。
- OpenAI function_call/output、Anthropic tool_use/tool_result、Google functionCall/functionResponse 的 multi-call replay 回归均通过。
- micro-compaction 继续只影响 provider view，不覆盖 messages.jsonl。
- event/telemetry 同时报告 inline result count、compacted result count 和按字节计算的 provider-view tool-result 体积。

### Resolution

- micro-compaction 现在倒序按独立 `ToolResult` 计数，并用默认 `65536` bytes 的 `llm_output` 连续后缀预算与 count 同时筛选；同一 message 内可以逐 result 混合 inline、compacted 与 pointerized
- 超出窗口的 result 只按其 ToolCallID/ProviderCallID alias 裁剪对应 assistant call 和 Anthropic/Google provider block，协议 ID、顺序与 sibling metadata/error/final 保持不变
- 已有 ephemeral/current-result artifact 只复用现有 pointer，不创建第二份 artifact；provider-view 构造保持 clone-only，durable `messages.jsonl` 不变
- compact events 与 versioned `RequestBudgetSnapshot` 统一报告 inline/compacted/pointerized 的 count 与最终 `llm_output` bytes；永久回归覆盖 batch 形状、count/byte 边界、三 provider replay、hysteresis、幂等与 durable log
- Resolution commit：本任务提交 `fix(runtime): compact tool results by item and bytes`

### Non-goals

- 不要求把 durable session message 拆成新的落盘格式。
- 不为了凑数量删除协议配对、最新用户指令或不可恢复结果。

## CTX-003 — Compaction 没有保证最终 provider request 落在可用窗口内

- Severity: P0
- Confidence: High
- Status: Resolved

### Evidence

- internal/runtime/compaction.go:48-56 的触发估算只计算 json.Marshal(messages) 加 systemPromptChars；estimateChars 本身只序列化 messages，见 internal/runtime/compaction.go:971-974。
- 实际 provider.TurnRequest 还包含完整 tool schemas、metadata、max output reserve 和 provider envelope，见 internal/runtime/engine.go:456-475 与 internal/provider/types.go:17-42。这些组成项没有进入 compaction 预算。
- internal/runtime/compaction.go:136-194 会把 todo、task、feature list、project memory、proofs、goal snapshot 和可选 semantic summary 放入 summary，但 summary 没有独立 hard size cap。
- internal/runtime/compaction.go:220-226 直接返回 summary + recent tail，没有对输出 view 再次估算并执行 hard-fit。
- internal/runtime/compaction.go:403-444 的首次 recent tail 主要按 message 数量保留；即使单条 recent message 已非常大，也必须保留。
- hysteresis reuse 虽有字符预算，但 internal/runtime/compaction.go:447-496 会为了 minCount、latest external instruction 和 tool-call dependency 超出该预算，返回后同样没有最终 hard-fit。
- internal/tools/registry.go:930-953 与 internal/fileutil/safe.go:17 允许 read_file 读取最多 16 MiB regular file 后按行窗口返回；一个 16 MiB 单行文件足以让一条“最近结果”远超多数剩余窗口。
- internal/runtime/engine.go:2945-2995 的 provider.request.prepared event 记录 provider/model/options，却不记录本轮 system/messages/tools 等组成项与最终 headroom。
- internal/runtime/engine.go:93-137 的 semanticSummaryFunc 会直接再次调用同一 provider adapter；该辅助请求不经过主请求的 provider.request.prepared、完整预算快照或 hard-fit。

### Why it matters

达到阈值会触发“做一次压缩”，但不等于压缩后的请求可发送。大 tool schema、大 summary、超长最新消息或超长单行结果仍可能让请求超过 provider context window，最终由 provider 返回 context-length 错误；更差时，下一次 continue 又重复 summary/compaction 和读取工作，却没有可解释的本地预算证据。

### Root cause

当前阈值是 compaction trigger，不是完整 provider request budget。compactor 与最终 TurnRequest 组装分离后，没有第二阶段的保守估算、收缩循环和不可满足边界。

### Recommended direction

- 先在 spec/10-context-compaction.md 和 provider contract 中定义完整 request budget：system prompt、provider-view messages、tool schemas、metadata/provider envelope、reasoning/thinking overhead 和 reserved output tokens。
- 增加统一 RequestBudgetSnapshot；runtime 负责通用组成项，provider-specific envelope/编码差异继续由 adapter 层提供保守估算，避免把 provider replay 逻辑移到 Web/CLI/tool 层。
- compaction 后重新测量；仍超预算时按确定顺序缩短 recent tail、pointerize 可恢复 payload、压缩 summary 中低优先级集合，并再次验证。
- 最新外部用户指令、最新 steer 约束和合法 tool replay 配对仍是不可丢边界；无法同时满足时必须在 adapter.RunTurn 前返回明确、可恢复的 harness error。
- 将估算值与 provider 返回的 usage.input_tokens 关联，持续校准 chars/token 近似，但不要求第一版引入精确 tokenizer。
- `request_kind=main` 与 `request_kind=semantic_summary` 必须复用同一预检入口。语义摘要请求无法 fit 时按现有可选语义层契约记为 skipped/failed，并回退到确定性 summary，不得拖垮主 session。

### Acceptance criteria

- 每次 provider call 前都有最终 hard-fit 检查，覆盖 system/messages/tools/metadata/output reserve；超出 effective window 的请求不得发给 provider。
- 主请求和 semantic-summary 辅助请求都经过 hard-fit；辅助请求因预算不足被拒绝时，主压缩仍使用确定性 summary 正常继续。
- compact.finished/compact.reused 和 provider.request.prepared 可关联同一 budget snapshot，并记录 pre/post 数量、各组成项、effective window 与 headroom。
- summary + recent tail 超预算时会继续有界收缩，直到 fit 或得到 typed local error；不会只压缩一次后直接发送。
- 单个最新外部指令本身过大、单个最新 tool result 过大、tool schemas 过大、summary 过大时，都有稳定错误或可恢复 pointer 路径。
- OpenAI、Anthropic、Google 的 multi-tool replay hard-fit 回归验证协议完整性。
- 固定 fixture 比较本地估算与真实 usage.input_tokens；允许记录误差，但不得在已知估算超窗时继续调用 provider。

### Resolution

- main、semantic-summary 与 probe 现在统一通过 adapter 的真实 wire estimator；main 在 `RunTurn` 前执行 provider-view clone-only hard-fit，最终 snapshot `fit=true` 才会写 prepared/call 路径
- hard-fit 以固定 `256` pass 为上限，按 complete artifact/current-view read-only source pointer、最老 replay closure、optional semantic summary、bounded deterministic core 的顺序收缩；每个已提交 action 的 wire body bytes 都经过重新估算并严格下降
- 最新 external instruction、最新 steer、最新 ToolResult replay closure、system prompt、tool schemas 与 metadata/envelope 不会被静默删除；无法满足时返回不含正文的 typed `request_budget_unfit`，并记录 estimated/available/reserved/effective-window 与 blocking component
- `provider.request.budget_action`、最终 `provider.request.prepared`、local rejection 和 provider attempt 复用同一 request id/snapshot；local unfit 不进入 transport retry、auto-resume、max-token resume 或 provider HTTP/fake call
- 永久回归覆盖 complete/source pointer、partial artifact fail-closed、replay-safe tail、semantic/deterministic summary、full/reused/deferred view、固定 pass/幂等、durable message 不变、system/schema/metadata/latest user/latest tool/summary component error，以及 OpenAI/Anthropic/Google multi-call replay
- Resolution commit：本任务提交 `feat(runtime): shrink provider views to a hard request budget`

### Non-goals

- 不覆盖原始 session 日志。
- 不引入 provider 原生 compaction API 或跨 provider handoff。
- 不要求首版精确复刻每个 provider tokenizer；要求的是保守、可解释、可拒绝的最终边界。

## TOOL-001 — Shell 与 skill command 输出先无界缓冲，之后截断且丢失中段

- Severity: P1
- Confidence: High
- Status: Open

### Evidence

- internal/tools/registry.go:801-818 的 shell 使用 cmd.CombinedOutput，进程退出前把 stdout/stderr 全量缓存在内存，再调用 truncateOutput(..., 12000)。
- internal/tools/registry.go:4408-4435 的 truncateOutput 只保留 head/tail 和省略字节数，被截掉的中段没有写入可恢复副本。
- internal/tools/registry.go:4095-4104 的 skill command tool 使用相同 CombinedOutput + 12 KB 截断路径。
- shell 被标记为 Ephemeral，见 internal/tools/registry.go:693-699；但 internal/runtime/engine.go:1350-1362 写 artifact 时保存的是 original.LLMOutput。该值已经是 12 KB 截断结果，却向模型标为 Full output。
- command skill definition 在 internal/tools/registry.go:3986-4013 没有声明 Ephemeral；其被截断中段既不会进入当前结果，也不会由旧结果 pointerize。
- spec/04-tools-and-skills.md:88-100 只规定超长 stdout/stderr 做 head/tail 截断和保留 raw length，没有定义原始输出的有界落盘与恢复契约。

### Why it matters

恶意或意外的高输出命令可在 tool 层占用大量内存，12 KB 截断只在进程完成后才生效。模型知道输出被截断，却无法定点找回中段，只能重跑命令；同时 Full output 标签会让模型误以为 artifact 是完整证据。测试日志、编译错误和远端响应的关键段落可能永久丢失。

### Root cause

输出控制只实现了“执行后 inline 截断”，没有将“进程采集上限”“provider inline preview”“session artifact 上限”建模为三个不同边界。ephemeral artifact 又发生在 runtime 的后续 provider-view 阶段，已拿不到 tool 层丢失的原始字节。

### Recommended direction

- 先更新 spec/04-tools-and-skills.md 与 spec/10-context-compaction.md，区分 inline preview、recoverable artifact 和 artifact hard cap。
- shell/skill command 共用流式 collector：内存只保留有界 head/tail ring buffer，超出 inline budget 时同步写 owner-only session artifact。
- 当前 tool result 就返回 preview + exact artifact path + raw byte count，不等待结果变成“旧 ephemeral output”。
- artifact 若受磁盘配额或 hard cap 影响，必须记录 artifact_truncated、persisted_bytes、raw_bytes；只有确实完整时才能称为 Full output。
- timeout、interrupt、process-group cancel、非零退出和 artifact 写失败都必须 flush/close 并返回可诊断 metadata；磁盘失败不能静默退化成“完整输出”。
- 为每 session/command 设置文件数、单文件和总字节配额，避免把内存 DoS 转成磁盘 DoS。

### Acceptance criteria

- 一个持续输出、随后 timeout/cancel 的命令不会让进程内存随总输出线性增长。
- 超过 inline budget 的 shell 与 skill command 在当前结果中都返回可由 read_file 分页读取的 artifact path。
- artifact 完整时字节内容与原始合并输出一致；受 hard cap 时明确标记自身不完整，raw/persisted/omitted 数量一致。
- 任何 preview/pointer 不再把已截断副本称为 Full output。
- owner-only 权限、symlink/path escape、session ownership、quota、write failure、timeout、interrupt 和 UTF-8 边界均有永久测试。
- 原有 timeout、最小环境 allowlist、sandbox 和进程组取消语义不回退。

### Non-goals

- 不把长输出重新整段注入 provider context。
- 不允许模型通过 discovery 扫描整个 session artifact tree；仍只暴露当前结果的精确路径。

## TOOL-002 — read_file 与 grep 只有行数/匹配数限制，没有可靠的每结果总字节预算

- Severity: P1
- Confidence: High
- Status: Open

### Evidence

- internal/tools/registry.go:934-953 的 read_file 先把文件按换行切分，再按最多 120 行选择；单行长度没有模型可见输出上限。
- internal/fileutil/safe.go:17、419-427 的 regular-file hard limit 是 16 MiB，因此一个合法的 16 MiB 单行 minified JS/JSON/日志可进入一条 read_file result。
- internal/tools/registry.go:1492-1497 将 grep 上限定为 200 条、每条最多 4096 bytes；单次正文理论上可接近 819,200 bytes，尚未计路径、行号和 JSON/provider 包装。
- internal/tools/registry.go:1268-1389 的 grep 没有 Ephemeral 标记；其最新或历史大结果不会复用 shell/glob/grep_files 的 ephemeral artifact 路径。
- internal/runtime/engine.go:1288-1291 的 ephemeral inline budget只应用于被标记为 Ephemeral 的工具，不能构成所有 ToolResult 的统一 per-result 上限。
- internal/runtime/engine.go:1030-1054 允许 tool.after hook 改写 llm_output/display_output；若通用上限只放在 registry.Execute 内，hook 可以再次放大模型可见结果。
- internal/fileutil/safe.go:432-469 已提供 ReadRegularFileRangeNoSymlink，internal/webconsole/service.go:3385 已用它做安全 range read；模型工具无需另写一套文件 range reader。

### Why it matters

“最多 120 行”并不等于小输出；minified bundle、单行 JSONL、压缩前日志和生成代码很容易用一行占满数 MiB。grep 的 200×4 KiB 上限也明显高于渐进式披露所需片段。少数调用即可占用大量主 context，触发压缩并丢失其他更高价值证据。

### Root cause

工具以行数或命中数作为主要 guard，没有在 ToolResult 进入消息事实前统一执行总字节/token budget，也没有为超长单行提供可继续的列/字节分页契约。

### Recommended direction

- 所有模型可见 ToolResult 同时受 item count 与 total bytes/token estimate 限制，默认值按工具用途分层。
- read_file 增加明确的 byte/column range 模式，或新增最小 read_file_range 工具；它与 line offset/limit 的互斥和 UTF-8 边界必须写入 spec。
- byte range 直接复用 `ReadRegularFileRangeNoSymlink`，保留现有 regular-file、symlink 与 path escape 校验；不要通过先全量读取再切片实现 continuation。
- 超长单行返回短 preview、总长度、当前 byte range 和稳定 continuation；模型可继续读取源文件，不需要重跑搜索。
- grep 使用更短 snippet，并返回 path、line、match span/byte offset，让模型通过 read_file 定点展开；达到总字节预算时在完整 match 边界停止。
- 动态、不可从源文件恢复的工具输出应在当前轮 spill 到 artifact；可从源文件恢复的结果优先返回精确位置。

### Acceptance criteria

- read_file、grep、grep_files、glob、shell、skill command 和 agent handoff 都有明确的模型可见总字节上限；任何单个结果不能绕过最终 request hard-fit。
- 16 MiB 单行文件不会整行进入 provider view；模型可用返回的 byte/column continuation 无损分页。
- grep 同时满足 match limit 和 total-byte limit，并明确区分 line snippet truncated 与 result set has_more。
- UTF-8 多字节边界、minified JS、超长 JSONL、无换行日志、超长路径和并行 multi-result batch 均有测试。
- 现有 workspace/symlink escape 与 16 MiB 源文件读取安全边界不回退。

### Non-goals

- 不把 read_file 变成浏览器端文件编辑器或复杂 IDE surface。
- 不要求模型一次读取完整大文件；目标是可定位、可续读、可恢复。

## TOOL-003 — grep 与 grep_files 达到 limit 时静默返回不完整集合

- Severity: P1
- Confidence: High
- Status: Open

### Evidence

- internal/tools/registry.go:1324-1377 的 grep 在 len(lines) >= limit 时立即返回 errGrepLimitReached，没有多取一个 match 来确认是否真实 overflow。
- internal/tools/registry.go:1383-1386 的 truncated metadata 只表示匹配行文本被截短，不表示匹配集合因 limit 被截断。
- internal/tools/registry.go:1446-1466 的 grep_files 同样在 len(matches) >= limit 时停止，没有 returned_count、effective_limit、has_more、limit_capped 或模型可见提示。
- internal/tools/registry.go:1492-1497 会把 grep/grep_files limit cap 到实现上限，但结果没有说明用户请求的 limit 是否被 cap。
- internal/tools/registry.go:1206-1249 的 glob 已有正确范例：多取一个结果，仅在确有 overflow 时截到 limit，并给出 actionable notice。
- internal/tools/registry_test.go:4596-4658 已验证 glob 的 overflow、无 overflow 和恰好等于 limit；grep/grep_files 现有测试未建立同等契约。

### Why it matters

搜索结果刚好返回 100/200 条时，模型无法判断“仓库只有这些命中”还是“后面还有更多命中”。这会让 broad discovery 基于不完整候选集做模块归因，或在找不到预期文件时改写 pattern 重搜，重复消耗 context。

### Root cause

grep/grep_files 把 errGrepLimitReached 同时用作早停控制流和隐式 overflow 状态，但没有采集 limit+1，也没有把集合完整性暴露给 ToolResult。

### Recommended direction

- 复用 glob 的 limit+1 overflow 探测语义；恰好等于 limit 且没有更多结果时不得误报。
- metadata 统一返回 returned_count、requested_limit、effective_limit、has_more、limit_capped、truncated_snippet_count。
- 模型可见输出在 has_more=true 时提示缩小 path/include/pattern，或使用稳定 continuation；不要只写一个不可操作的 truncated=true。
- grep 的 snippet truncation 与集合 overflow 使用不同字段和不同文案。

### Acceptance criteria

- grep/grep_files 在真实 overflow 时 has_more=true 并给出 narrowing/continuation 提示。
- 命中数恰好等于 effective limit 且无更多结果时 has_more=false，不误报截断。
- 请求 limit 超过实现上限时 limit_capped=true，并报告 requested/effective 值。
- grep 的某行片段被截短但集合完整时，truncated_snippet_count>0、has_more=false。
- 测试覆盖 0、limit-1、limit、limit+1、多目录/include、limit cap、UTF-8 snippet 和 deterministic ordering。

### Non-goals

- 不把 grep 默认改成无限输出。
- 不要求搜索工具返回整文件正文；grep_files 仍保持只返回路径。

## CTX-004 — Compaction transcript 对 operator 可见，但恢复中的 agent 没有受控分页回捞入口

- Severity: P1
- Confidence: High
- Status: Resolved

### Evidence

- spec/10-context-compaction.md:58-68 要求超阈值后写完整 transcript，并将 provider view 改为 summary + recent tail。
- internal/runtime/compaction.go:113-125 写 transcript 后，从 provider view 删除历史中段；summary 只保留结构化摘录和 transcript path。
- internal/session/store.go:3390 附近提供 WriteTranscript，原始历史仍是 owner-only session fact。
- internal/tools/registry.go:1657-1734 的 read_file session 例外只允许当前 session 的 artifacts/tool-outputs/ 精确路径。
- artifacts/transcripts/ 与 artifacts/compactions/ 没有专用、current-session-only、分页的 model tool。普通 workspace discovery 又按设计跳过 session 内部 artifact。
- internal/session/store.go:529-556 已有 LoadMessagesTail / LoadMessagesBefore，Web 的 /api/sessions/{id}/messages 也复用这两个入口；缺口在模型侧的受控暴露和超长单 record 分页，不在 canonical history 的基本分页能力。

### Why it matters

压缩 summary 无法预知未来 follow-up 会关心哪个细节。某条早期错误、provider stop reason、用户措辞或 tool output 在压缩时看似次要，后续可能成为关键；当前 agent 没有标准的窄查询方式找回，只能重读仓库、重跑命令或依赖 operator 手工查看 session 文件。

### Root cause

项目已解决“原始事实不丢”和“operator 可审计”，但还没有把 transcript 设计成 agent 可按需访问的只读 reference store。现有 ephemeral exception 专门服务 tool-outputs，刻意没有泛化到其他 session artifact。

### Recommended direction

- 增加专用 read_session_history，而不是放宽 read_file 到任意 session 路径。
- 输入只接受当前 session 的 before_message_id、有限 limit、message id/turn 或受限关键词；不接受任意 filesystem path/session id。记录级分页复用 Store 的 canonical messages.jsonl 入口。
- 单条 record 超过输出预算时，再使用 message_id + byte_offset/byte_limit 做内容分页；不要为该工具另写一套 transcript JSONL parser。
- 输出有严格 record/byte 上限、稳定 continuation，并明确包裹为 historical reference；历史内容不能升级为新的 user instruction。
- 默认返回规范化 message/tool 摘要；provider opaque replay block 如需暴露，应有独立受限模式，不能破坏 provider adapter 边界。
- discovery 不扫描整个 session tree；只有模型持有明确 transcript/message reference 时才定点读取。

### Acceptance criteria

- compaction 后的 agent 可按 message id、turn 或 transcript page 读取早期历史的小窗口，不需重新执行原工具。
- 工具只允许读取当前 session 的 transcript/compaction reference，cross-session、任意绝对路径、path traversal 和 symlink escape 均被拒绝。
- 单条历史 message 超长时仍按字节分页，不会绕过 TOOL-002 与 CTX-003。
- 输出明确标记 source session/message/turn、historical_reference=true、has_more/next cursor，并声明指令优先级不变。
- OpenAI/Anthropic/Google opaque replay 数据、含 tool call/result 的历史、压缩复用、多次 transcript 和损坏 artifact 都有安全回归。

### Resolution

- 新增内置只读工具 `read_session_history`，schema version 为 `1`。session id 只来自 `ExecContext.SessionID`；closed schema 只表达 record mode（tail/before/query）或互斥的 `message_id + byte_offset + byte_limit` content mode，无法传入 session/path/artifact/transcript 或绝对路径
- record limit 默认 10、最高 20；query 最长 256 UTF-8 bytes且每页只评估 cursor 前最近 512 条 canonical records；单 message content page 最高 16 KiB；完整模型可见 envelope 受 `min(24 KiB, runtime.tool_output.llm_output_max_bytes)` 约束，无法容纳完整 record/page 时返回 typed `output_budget_too_small`
- Store 复用 `LoadMessagesTail` / `LoadMessagesBefore` 并增加稳定 message representation 的 UTF-8 byte paging。JSONL reader 在共享锁内捕获完整 append boundary，再用 fixed snapshot streaming visit；invalid UTF-8、损坏 record、未知 cursor/message、symlink 与 cross-session id 均 fail closed
- 默认摘要和 content representation 保留 message/tool 定位字段、ToolResult `llm_output` 与有界 reference metadata；`Thinking`、`DisplayOutput`、任意非 allowlist metadata和 OpenAI/Anthropic/Google `ProviderContentBlocks` opaque 正文不进入返回值
- 所有成功结果都带 `historical_reference=true`、source session/message ids、完整 count/cursor 与 instruction-precedence 说明。system prompt 同步声明旧 system/user/steer-shaped text 只是引用；TOOL-002A finalizer 与 CTX-003 hard-fit 回归证明完整 JSON/cursor 不被事后截断或提升为当前 external instruction
- new/reused compaction summary 与 deterministic hard-fit core 保留 versioned `history_reference`，指向当前 session 的 `messages.jsonl`；压缩后可直接找回已从 provider view 删除的早期 message/tool 事实，不需重跑原工具，也没有新增 transcript parser
- 永久回归覆盖 empty/exact/overflow tail、before、bounded query及continuation、并发 append fixed view、UTF-8 continuous pages、long-id/单页预算、parent/child/sibling隔离、symlink/corruption、三 provider opaque sentinel、prompt-injection-shaped history、compaction new/reuse、finalizer、hard-fit和 RequestBudgetSnapshot tool schema accounting
- Resolution commit：本任务提交 `feat(tools): add bounded current session history reads`

### Non-goals

- 不引入多级语义向量库或跨 session memory 检索。
- 不允许模型枚举或全文灌入整个 session artifact 目录。
- 不改变 messages.jsonl 的事实源地位。

## HARNESS-001 — 缺少 first-class、只读、低噪声 explorer profile

- Severity: P2
- Confidence: High
- Status: Resolved

### Evidence

- internal/tools/registry.go:3671-3724 的 agent_spawn 已提示 broad investigations/context control，并同时支持同步等待与 background+resume_parent；因此底层委派与等待能力已经存在。
- spec/14-multi-agent-and-isolation.md:14-18、136-151 明确要求委派由模型或调用方决定，runtime 不自动塞固定 DAG。
- internal/runtime/role.go:8-24 只接受 planner、generator、evaluator；没有 explorer。
- internal/config/config.go:68-79、654-664 的 role provider override 同样只覆盖上述三种 role，且 override 只有 provider/API provider/base URL/model，没有 explorer 可单独选择的 reasoning/output 预算。
- internal/runtime/runner.go:2051-2059 为每个 child 建立完整 Registry；没有按 explorer/read-only profile 过滤 provider 可见 tool schemas。
- child 虽在 internal/tools/registry.go:3752-3754 被禁止实际 nested agent_spawn，但 agent control tools 仍存在于统一 registry，增加无效 schema 与错误尝试面。
- 当前没有内置的 concise explorer handoff 契约，例如 claim | file:line | confidence；child final_text 的范围完全由临时 prompt 决定。

### Why it matters

已有 fresh child session 能隔离探索 context，但调用方每次都要手写只读边界、工具限制和结果格式。child 仍看到写工具和无法执行的 nested agent tools，既增加 tool schema 预算，也提高误写、误调用和长回传概率。文章所强调的收益来自“探索隔离 + 低噪声 handoff”，目前仓库只完成前半部分。

### Root cause

multi-agent 能力最初围绕 planner/generator/evaluator 与 large-project execution 构建，没有把 codebase exploration 建模为独立的最小 capability profile。

### Recommended direction

- 在 spec/14-multi-agent-and-isolation.md 先定义可选 explorer role/profile；继续由 master 模型决定是否使用，不自动拆任务。
- explorer 可配置独立 provider/model，以及有界 reasoning effort/max output 等推理选项；这些选项继续经 runtime/session metadata 传递，不能只作为一次性 prompt 约定。
- explorer provider view 只暴露 read_file、grep_files、grep、glob、load_skill、必要状态/finish 工具；默认隐藏 write/edit、agent control、goal/task mutation 和可变 shell。
- allowlist 同时作用于 provider request schema 和 Registry.Execute；仅从 schema 隐藏不足以防止恢复轨迹、兼容 provider 或伪造 call 触发被禁工具。
- 如确需 shell，必须是单独的只读 exec policy profile，不能只靠 prompt 声称 read-only。
- 复用现有 fresh child session、session store、queue、provider override 和 parent coordination，不创建第二套 orchestration 状态。
- 默认 handoff 要求简短结论、claim | file:line | confidence、未覆盖范围和关键疑点；长原始输出保留在 child session/artifact，不回灌 parent。
- agent_spawn 描述补充信息经济启发：开放式、跨模块、入口不明且原始搜索量远大于结论时考虑 explorer；入口明确的小检查留在 parent。
- 当委派目的是 context isolation，提示 parent 使用同步 spawn，或 background spawn 后 agent_wait；不要在等待期间重复 child 已覆盖的 repo 探索。该提示是模型 guidance，不是 runtime hard guard。
- explorer 未显式指定 isolation_mode 时默认使用 off，避免只读探索为大型 repo 无意义复制工作树；调用方显式给出的 auto/git/copy 继续按现有语义处理。
- 现有 Web Settings 的 role provider 配置需要增加 explorer 的完整读写 round-trip，但默认 Web 首页不新增 orchestration 面板。

### Acceptance criteria

- agent_role=explorer 可选且支持独立 provider/model/reasoning/output override；未配置时仍继承 parent，并把 effective options 持久化到 child session metadata。
- explorer 的 provider request schema 不包含 write_file、edit_file、agent_spawn/agent_* 和其他 mutation tool；runtime 执行层也有 defense-in-depth 拒绝。
- explorer 是 fresh child session，原始搜索输出不进入 parent messages；parent 只收到有尺寸上限的 structured handoff 与 child/session reference。
- 同步 explorer、background explorer + agent_wait、失败/暂停/取消和 parent recovery 均有回归。
- 一个 deterministic fake-provider fixture 证明 parent request 不携带 child 原始 tool trajectory，且 child tool allowlist 与 handoff 大小满足契约。
- 默认 Web 首页不增加复杂 orchestration 面；最多在现有 session/child inspector 折叠区显示 role 与 handoff。
- 没有任何 runtime rule 强迫简单任务委派，模型未调用 agent_spawn 时行为与当前一致。

### Resolution

- 新增合法 `explorer` role 与 durable `explorer-readonly-v1` capability profile；provider schema 和 `Registry.Execute` 复用同一精确 allowlist：`read_file`、`grep_files`、`grep`、`glob`、`load_skill`、`finish`
- 禁用工具在 provider view 中不可见；恢复轨迹、兼容 provider 或伪造调用仍会在 `tool.before` hook 和 definition dispatch 之前得到稳定 `schema_reject/tool_not_allowed_for_role`，不会触发 shell、写入、task/goal/plan mutation、agent control 或 trusted command side effect
- explorer 保持显式、model-led；`agent_spawn` 只增加信息经济与 context isolation guidance。runtime 不按仓库大小、prompt 或 tool-call 数自动委派，也不强制 parent 等待或执行固定探索路线
- explorer 未显式指定 isolation 时把 effective `off` 写入 child metadata；显式 `off/auto/git/copy` 保持调用方选择。provider/model/reasoning/max-output/tool profile/isolation 的 effective snapshot 同步写入 child session、queue job/lifecycle event、background notification、status/summary 与 Web session detail
- planner/generator/evaluator/explorer 的 role provider override 统一支持 provider/API provider/base URL/model/reasoning effort/max output；direct child 与 queued worker 使用 durable effective options，后续 Settings 更新不会重解释已有 parent/job 中有意义的零值
- explorer prompt 只约束 read-only 边界与简短 `claim | file:line | confidence` handoff、未覆盖范围和关键疑点；同步/后台 handoff 继续复用统一 tool-output byte budget，长 trajectory 留在独立 child session/artifact
- deterministic HTTP fake-provider fixture 证明 child `read_file` 原始 sentinel 只进入 child request，parent request 仅看到有界 handoff/reference；永久回归还覆盖精确 schema、trusted skill、执行层拒绝、sync/background+wait、failure/pause/cancel/parent recovery、role option snapshot、Web GET/PATCH/YAML round-trip 与默认首页无 orchestration dashboard
- 验证通过：HARNESS 聚焦回归、`CGO_ENABLED=1` runtime/session race、相关 Go 包完整回归、`go test ./...`、scoped `go vet`、Web JS syntax 与 140 项 Web utility tests；未启动 Docker
- Resolution commit：本任务提交 `feat(runtime): add a read only explorer agent profile`

### Non-goals

- 不实现固定 DAG、自动模块切片或硬编码审计 workflow。
- 不要求每个任务使用 sub-agent，也不把 explorer 提升为默认 Web 工作流。
- 不允许 explorer 嵌套派生 child。

## OBS-001 — 缺少可比较的 context budget、compaction 与 root/child telemetry

- Severity: P2
- Confidence: High
- Status: Resolved

### Evidence

- spec/10-context-compaction.md:124-143 要求 compact event 记录 input_chars、threshold 来源、窗口和 recent message 数量，但未定义完整 request composition。
- internal/runtime/engine.go:2954-2995 的 provider.request.prepared 记录 provider/model、metadata keys 和 provider options，没有 system prompt、messages、tool schemas、output reserve、pre/post compaction 或 headroom 组成项。
- internal/runtime/engine.go:3074-3094 的 turn.stopped 已记录真实 input/output/cache usage，但没有与当轮估算、compaction action 和 tool-output 体积关联。
- session metadata 已有 parent_session_id/root_session_id/agent_role，见 spec/14-multi-agent-and-isolation.md:50-62；当前没有将一棵 root/child session 的 context peak、compaction count 和 usage 聚合成可比较报告。
- spec/07-testing-strategy.md:174-180 的 compaction 验收只覆盖 artifact、原始消息和 todo/task 保留，没有文章所需的“单线程 vs 定点工具 vs delegated explorer”上下文效率 fixture。

### Why it matters

没有组成项和 lineage 聚合，就无法回答某次自动压缩是 messages、tool schemas、system prompt、summary 还是单个工具结果导致，也无法判断委派是在保护 root context，还是仅增加总 token。优化容易退化为依据单次体感调整阈值或 prompt，缺少可重复的前后对比。

### Root cause

现有 telemetry 面向 provider 成败、usage 和 compaction 生命周期，尚未建立每轮 request budget snapshot 与 root/child harness experiment 视图。

### Recommended direction

- 每次 provider request 持久化只含尺寸/计数的 ContextBudgetSnapshot：system、messages、tool schemas、metadata/envelope、output reserve、effective window、headroom、compaction action/summary id、inline/pointerized tool output bytes。
- 将 snapshot 与 turn、provider response、真实 usage 和 session lineage 关联；不把 telemetry 内容注入模型 prompt。
- CLI/SDK 提供稳定 JSON 查询；Web 只在现有 session inspector 的折叠区展示，不增加默认首页 dashboard。
- 增加固定仓库 fixture，比较三种 harness 结构：
  - 单 root 广泛探索；
  - root 使用 grep_files/read_file 定点剪枝；
  - root 委派只读 explorer。
- 报告 root peak input、child peak/aggregate input、compaction 次数、inline/artifact tool-output bytes、provider usage、turn/tool-call 数和 wall time；只有 provider 提供可靠计费数据时才计算 cost。

### Acceptance criteria

- provider.request.prepared 或关联 artifact 能重建每轮完整预算组成，且与 CTX-003 的 hard-fit 使用同一估算源。
- compact.started/finished/reused/deferred、request snapshot 与 turn.stopped usage 可按 session+turn 唯一关联。
- root session 报告可聚合直接 child，仍能区分 root peak 与 child aggregate，避免用总 token 掩盖主 context 是否被保护。
- deterministic fixture 在 CI 中输出机器可读对比，不依赖外部收费 provider；可选 live-provider smoke 单独运行。
- Web-first 默认页面保持简洁；operator 可在 session detail/CLI JSON 中查看高级指标。
- telemetry 只记录尺寸、计数、ID 和已存在的 provider usage，不复制 prompt/tool 原文，也不改变项目“不做默认 runtime redaction”的既有边界。

### Resolution

- `session.RequestBudgetSnapshot` 成为 hard-fit 与 telemetry 的唯一 versioned 预算对象，runtime 仅保留 type alias；prepared snapshot、budget action、main/semantic-summary compaction lifecycle、provider callback、transport retry 与 completed/failed terminal event 共享 `request_id` / kind / turn / sequence correlation
- 每个已 prepared request 只有一个 durable completed/failed terminal lifecycle；budget rejection、semantic-summary timeout、provider cancellation、provider-call 前 pause 与 retry-attempt 事实持久化失败均有稳定 status/error class。transport retry 继续属于同一 request/snapshot，不制造伪请求
- 三个 provider adapter 用显式 usage presence 区分“未返回”与“已报告 0”；报告只接受 `provider` / `legacy_inferred` source，unknown usage 不计入 known total，也不复制 provider error、raw payload 或 metadata value
- `Store.ContextReport` 从 canonical metadata/messages/events streaming 派生 schema v1 报告；root/递归 child 同时对账 root/child peak、root/child/total aggregate input、provider-view inline/compacted/pointerized bytes、唯一 tool artifact bytes、request/turn/tool-call/compaction 数、known usage 与 unknown usage request 数
- request/session/lineage 时间边界解析 RFC3339Nano 后比较并计算 wall time；不会因 `...00.100Z` 与 `...01Z` 字符串格式差异得到错误顺序。坏 lineage、foreign root、cycle/duplicate session fail closed
- Core/SDK 提供 `Context(sessionID)`，CLI 提供 `sessions context <session-id> --json`，Web 提供 `GET /api/sessions/<id>/context`；现有 Session inspector 的 Context tab 才懒加载/手工刷新，detail 共用 64 项预算并显式报告 omitted session/request，aggregate 保持完整，默认首页不增加 dashboard
- `validation/cmd/contextharnessfixture` 输出 deterministic schema v1 JSON，比较 `single_root_broad`、`single_root_narrowed`、`delegated_explorer`；delegated comparator 明确使用 root aggregate + child aggregate 对账 total，而不是误用 root peak
- 永久回归覆盖 snapshot/lifecycle/usage/lineage/secret sentinel、CLI/SDK/Web bounded schema、fixture 确定性和 Web lazy behavior；聚焦测试、runtime/session race、相关包回归、`go test ./...`、scoped `go vet`、fixture 双运行 `cmp`、Web JS syntax 与 142 项 Node utility tests 全部通过，未启动 Docker
- Resolution commit：本任务提交 `feat(runtime): expose context budget and lineage telemetry`

### Non-goals

- 不根据 telemetry 自动改写 prompt、自动委派或强制调整 compaction threshold。
- 不引入 hosted SaaS observability、复杂 dashboard 或第二套状态权威源。
- 不把一次 benchmark 结果当作所有模型、仓库和 provider 的固定最优策略。
