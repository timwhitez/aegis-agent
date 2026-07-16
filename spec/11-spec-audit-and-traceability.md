# Go CLI Agent Spec Audit And Traceability

## 1. 目的

这份文档不重复产品 spec，而是回答两个问题：

1. 哪些内容是已验证事实？
2. 哪些内容是当前项目的明确设计取舍？

## 2. 已验证的设计基线

### 2.1 最小 loop

来源：

- `learn-claude-code` `s01`
- `bitter-lesson-agent-frameworks`

结论：

- agent loop 应保持极简
- 核心仍是“模型输出 -> 工具执行 -> 工具结果回写 -> 下一轮”
- harness 不应在 loop 里塞固定 DAG 或重型 plan engine

### 2.2 工具注册与 dispatch

来源：

- `learn-claude-code` `s02`

结论：

- 加工具时不应重写 loop
- 应使用 registry / dispatch map 追加能力

### 2.3 Todo + 持久化任务系统

来源：

- `learn-claude-code` `s03`
- `learn-claude-code` `s07`
- `opencode` 的 session todo 思路

结论：

- 需要同时保留高频 todo 与持久化 task graph
- todo 负责短周期执行节奏
- task graph 负责 durable goals、依赖、恢复

### 2.4 Skill 按需加载

来源：

- `learn-claude-code` `s05`

结论：

- system prompt 只放 skill 摘要
- skill 正文在需要时通过 `load_skill` 注入

### 2.5 上下文压缩

来源：

- `learn-claude-code` `s06`
- `blog-langchain-com__autonomous-context-compression.md`

结论：

- compaction 必须作为独立 harness 机制存在
- 历史和工具输出不能无限增长
- 原始日志必须和压缩视图分离

### 2.6 薄 provider 层

来源：

- `pi-coding-agent`
- `bitter-lesson-agent-frameworks`
- `opencode`

结论：

- provider 层必须薄
- 真正复杂的地方是协议差异、generation 选项映射、replay 细节、错误分类
- 这些复杂度应停留在 adapter，而不是泄漏到 CLI 或 tool 层

### 2.7 Live Steer

来源：

- `codex` 的 `turn/steer` / `turn/interrupt`
- `opencode` 的 active session prompt 模式

结论：

- 运行中补充输入必须成为 harness 原生能力
- v1 采用 queue-first + best-effort interrupt
- 外部 `steer` 是标准入口，inline TUI 热键不是前提

### 2.8 Audit / Review Evidence Discipline

来源：

- `codex` 的 findings-first review discipline
- `openai-com__harness-engineering.md`
- `blog-langchain-com__how-coding-agents-are-reshaping-engineering-product-and-design.md`

结论：

- 审计 / review 任务里的 `validated findings` 必须只写被行为证据支持的结论
- 声明、保留名、接口、文档提及、目录形状、类型或枚举本身，不足以证明运行时行为
- 若结论依赖默认暴露、注册路径、配置门控或真实执行语义，必须核对 owning code path；做不到时应降级为 risk / inference
- findings-first review 产物应显式记录 `severity`、`confidence`、`evidence`、`why it matters` 和 `unresolved questions`
- 当任务进入 review / audit 语义时，harness 可以对 `write_file` / `edit_file` / `finish` 施加轻量 validator，并在缺少 durable artifact 时阻断完成，避免缺字段或无证据锚点的报告被当作完成
- 当外部指令已经明确声明“这不是 review / audit task”时，即使 prompt 中出现 `proof`、`drift` 一类词，runtime 也不应误启用 review artifact validator
- 当外部指令已经显式给出 review / audit 交付路径时，`finish` 的满足条件必须绑定到该 exact artifact path，而不是被其他 review-like scratch artifact 旁路
- 当 validator 进入 workspace-aware 模式时，`evidence` 不应只写 `path:line` 形状；还应附带短 snippet / identifier，并验证 cited path 可读、行窗存在、snippet 能在 cited lines 中找到
- 当声明级线索已经指向具体文件时，应优先在同文件内追到 owning function / gate，再决定是否扩大检索范围
- 当任务显式要求固定标题、精确 opening block、首个 section 顺序、literal anchor sentence 或 exact proof-anchor bullet/text 时，这些约束必须优先于默认 findings-first 习惯；runtime 可以在 write/edit/finish 前校验 exact-template 与 required literal anchors 是否都被保留

### 2.9 Planner / Generator / Evaluator Separation

来源：

- `anthropic.com/engineering/harness-design-long-running-apps`

结论：

- 对长时间、大范围任务，planner、generator、evaluator 的角色分离可以作为真实增益，而不是默认把所有职责压回单个 session
- 这种分离不要求 runtime 退化成固定 workflow graph；当前项目仍应保持“session + tools + durable artifacts”模型，只把 role 作为显式 hint
- structured handoff 的关键不是一段总结词，而是 durable artifacts 是否完整且新鲜，至少应覆盖 spec / plan / progress / validation
- evaluator / reviewer 角色必须保持怀疑式评审，不应把模型自评当成通过标准
- 模型升级后要持续复盘哪些 scaffold 仍然 load-bearing；角色化、evaluator pass、handoff guard 都应按任务强度启用，而不是无差别铺满所有任务
- role hint 不应只停留在 prompt 文本里；当 session 或 child job 显式声明 `planner` / `generator` / `evaluator` 时，该 role 应持久化进 session metadata、queue job、background notification 和 provider request metadata，方便后续 traceability 与 comparator 验证
- role hint 的 provider override 必须基于显式选择，而不是从 `agent_name` 或 orchestrator / worker / validator 文案做模糊匹配；模型需要在 `role_plan.role` 或 `agent_role` 中直接选择 `planner` / `generator` / `evaluator`
- 三类 role hint 可以各自配置 provider override，覆盖只作为默认 provider/model/adapter 选择，不代表 runtime 自动创建 orchestrator / worker / validator 三段任务流
- Codex 当前公开的普通 sub-agent 配置提供并发数、嵌套深度、steer/stop 等控制；`agents.job_max_runtime_seconds` 只对应 `spawn_agents_on_csv` worker，未提供通用的逐 child parent budget 字段。因此本项目不新增 `agent_spawn` 的逐 child budget 参数，只保留 Settings / config 中默认关闭的全局 optional child budget
- child budget 一旦启用，必须同时提供可收敛控制：parent 能对 budget-paused foreground/background child 追加下一 attempt 的 turns/active runtime、延长 absolute deadline或清除对应限制，也能显式 cancel/settle 不再需要的 work；settle 只结算 queue / parent coordination，不把 paused child 伪造为 completed
- Codex 当前没有普通 `agent_spawn` 的通用 parent-provided budget，因此本项目也不在初始 spawn schema 加逐 child budget；全局默认 policy 在创建时快照，parent extension 只处理已经预算暂停的 child
- token/cost child budget 当前不进入 v1：provider usage 缺失时不能把 unknown 当 0，成本也没有跨 provider 的稳定精确来源。成本治理继续使用 provider usage/Goal accounting 与外部配额，待有可靠 usage contract 再单独设计

### 2.10 Session Goals

来源：

- `dev.md` 对 Codex Goals 与 Factory Missions 的本地设计收敛
- 当前项目既有 session 文件事实源、completion controller、task graph、Web-first 本地控制台边界

结论：

- 一个 session 默认最多一个 current goal，goal 是用户可见的 durable objective，不是 system 级指令升级
- 默认产品入口只有一个 Goal 开关；开启后 prompt 本身就是 objective，用户不需要在启动前填写 success criteria、validation、milestone、role plan 或 budget
- `goal.json` 与 `artifacts/goal-history.jsonl` 是目标事实源；`session.md`、checkpoint、WebConsole 展示都只是派生视图
- Goal 状态变更工具面只允许 `get_goal`、`create_goal`、`update_goal(status=complete)`；pause / resume / clear / budget-limited 由用户或系统控制。通用 `await_input` 只停靠 session 并记录 blocker，不改变 Goal 状态
- Mission 的长任务能力收敛为 Goal 的内部结构化计划：agent 可以在运行中保存 success criteria、validation contract、features、milestones、role hints 与 shared artifacts，但 runtime 不得据此硬编码 DAG、强制委派或强制验证顺序
- Mission plan approval 只采用已有 Plan Mode 门禁：`require_plan_approval` / `needs_approval` 会确保 linked Plan Mode 存在，pending 状态下由 Plan Mode 裁剪工具和阻断 mutating/execution actions；批准后同步 mission plan approved 事实
- `record_goal_progress` 是唯一新增的模型可写 progress/handoff 工具：它只对当前 goal 的结构化计划、validation evidence、evaluator/child/queue 链接、commands、artifacts、blockers 和 budget wrap-up 做 append-friendly 更新，不允许改 objective、用户控制状态或完成状态
- Mission validation coverage 是 approval 前的事实检查，不是 workflow engine：未覆盖或 invalid validation contract 默认阻断 plan approval，但 CLI/API 可显式 override 并留下历史事实
- `update_goal(status=complete)` 的 evidence、summary、criteria status 与 validation status 是 `goal.json` 当前快照的一部分，不能只作为 `goal-history.jsonl` 的附属事件
- Budget limited 只表示预算触顶，需要 wrap-up 和剩余工作说明；除非完成审计真实通过，否则不能被视为 complete

### 2.11 Plan Mode

来源：

- Codex Plan Mode 的 collaboration-mode gate、`request_user_input` 限制与 approval-driven implementation entry
- ForgeCode Muse 的 planning-only agent 与 Markdown checkbox / verification plan artifact 形式
- 当前项目既有 session 文件事实源、CompletionController、Goal/Todo/Task 分层和 Web-first 本地控制台边界

结论：

- Plan Mode 是 session-scoped execution gate，不是 Goal、Mission、Todo 或 Task 的别名；Goal 记录 durable objective，Plan Mode 只控制“审批前不执行变更”
- v1 通过显式 CLI flag、Web toggle 或 API payload 启用；普通 prompt 中写“先计划”不自动切换 runtime mode
- `planmode.json` 是事实源，`artifacts/planmode-history.jsonl` 记录状态流水，`artifacts/planmode-plan.md` 是 operator-readable 派生计划
- v1 使用 `submit_plan` 工具作为计划事实源，不依赖 `<proposed_plan>` 流式 parser；`submit_plan` 后当前 turn 停在 `awaiting_approval`，同批后续 tool call 必须写合成错误 result
- pending Plan Mode 下 provider schema 和 CompletionController 双层门禁必须一致：只允许 read/search/load_skill、只读 goal/todo/task/feature-list、`get_plan_mode`、`request_user_input`、`submit_plan`
- `request_user_input` 保存 `pending_request.tool_call_id`，使 Web active runner、server restart fallback、取消和回答都能补齐 provider replay 所需 tool result
- 以 pending Plan Mode session 为 parent 的 child/delegate/queue 提交必须拒绝；独立新 session 或无 parent queue job 不受影响

## 3. 已验证的 provider 协议事实

### 3.1 OpenAI Responses

已验证点：

- 使用 `POST /responses`
- 请求可包含 `instructions`、`input`、`tools`
- generation 选项可映射为 `temperature`、`top_p`、`max_output_tokens`
- reasoning 选项可映射为 `reasoning.effort`
- reasoning summary 可映射为 `reasoning.summary`
- encrypted reasoning item 可通过 `include: ["reasoning.encrypted_content"]` 请求，并作为 OpenAI adapter-owned replay fact 保存
- 文本 verbosity 可映射为 `text.verbosity`
- 工具调用是 `function_call`
- 工具结果回放是 `function_call_output`

设计结论：

- `openai-compatible` + `wire_api=responses` 继续复用同一 adapter
- 默认 `store: false`，保持本地 session 是唯一事实源
- `wire_api` 是 legacy / advanced compatibility 字段；产品语义上用 Provider Profile + `api_provider` 区分配置项和 adapter family

### 3.2 Anthropic Messages

已验证点：

- 使用 `POST /v1/messages`
- 认证头为 `x-api-key` 与 `anthropic-version`
- `tool_use` / `tool_result` 配对必须正确
- `tool_result` 在后续 `user` 消息里回放
- `thinking` 可通过 `budget_tokens` 启用
- 带工具调用的 extended thinking 需要保留 `thinking.signature` 与 `redacted_thinking.data` 等 provider-native 块，后续 replay 由 Anthropic adapter 原样带回

设计结论：

- v1 允许把 `thinking_budget` 映射到 `thinking`
- 可读 `Message.thinking` 只用于 UI / session 浏览；provider-native thinking blocks 作为 adapter-owned replay facts 存入 `provider_content_blocks`

### 3.3 Google Gemini `generateContent`

已验证点：

- 使用 `.../models/{model}:generateContent`
- `systemInstruction` 承载系统提示
- `contents` 承载历史
- `functionCall` / `functionResponse` 表达工具调用与结果
- `generationConfig` 可包含 `temperature`、`topP`、`maxOutputTokens`
- `thinkingConfig` 可承载 `includeThoughts`、`thinkingBudget`
- `thought` 是 part 上的 boolean 标记；thought summary 的可读内容仍在同一 part 的 `text` 字段中
- `thoughtSignature` 属于 provider-native replay fact，需要随原 part 保留并由 Google adapter 在后续 `contents` replay 中带回

设计结论：

- v1 允许 generation / thinking 选项映射到 `generationConfig`
- Gemini thought summary 进入 `Message.thinking` 展示，`thoughtSignature` 保存在 `provider_content_blocks`，不由 CLI / Web 层解释

## 4. 当前锁定的产品决策

### 4.1 Web-first v1 默认入口

当前锁定的默认产品叙事是“本地 Web 控制台优先，CLI 保持稳定 fallback”，因此：

- Web-first v1 的默认完成口径覆盖 Phase 0-10 的 runtime / provider / CLI 基座，以及 Phase 15 的本地 Web 控制台
- `web` 是默认 operator surface；`run` / `exec` / `steer` / `continue` / `sessions` / `goal` / `tasks` 作为 CLI fallback 和脚本化入口继续稳定
- `delegate` / `queue` / `children` / `isolation` 可以由 Web 提供轻量入口和观测，但不能演变成固定 workflow 或强制编排
- `tui` 仍只作为实验观测面存在

### 4.2 高级能力不主导默认 Web 页面

即使仓库里已有 Phase 11+ 代码：

- README / help / smoke / 验收应围绕 Web-first 本地控制台和 CLI fallback 设计
- worker pool 调参、raw queue payload、mission patch editor、isolation tuning、child orchestration 细节不进入默认页面
- 高级能力可以通过 REST API、CLI 或折叠/显式入口保留，不能要求普通用户多次确认或理解内部状态机后才能完成 start / steer / continue

### 4.2.1 Web 控制台的当前产品决策

- 本地 Web console 是默认产品入口，用于降低上手门槛、增强 session/queue 可视观测性并承载基础控制操作
- Web 控制台只复用本地 session / state / messages / events / queue 文件事实
- Web 控制台的后台并发执行必须建立在真实 worker / child session 之上，而不是前端假进度条
- Web 控制台当前采用 polling-first，不承诺 SSE / WebSocket 作为 v1 前提
- 高频用户交互默认应简洁：start、steer、continue、Plan approve 不需要层层确认；queue submit 保留在 CLI/API advanced 面；validation coverage override、删除/清理、API key/config 写入、外部暴露服务等风险动作才需要显式确认

### 4.2.2 Multi-agent 工具面的当前产品决策

- `agent_spawn` / `agent_wait` / `agent_stop` / `agent_prompt` / `agent_status` / `agent_list` 默认暴露给 session tool list
- 这只是给当前 master agent 提供 delegation 能力，不代表 runtime 会自动拆任务或自动新建 child session
- `agent_prompt` 只是让 master agent 复用 Live Steer 给自己名下的 child/job 追加 prompt；是否收敛、等待或继续仍由 master agent 判断
- 若部署方需要更窄的能力面，仍可显式设置 `runtime.multi_agent.enabled=false`

### 4.3 Provider generation 选项进入事实源

当前已锁定：

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
- `thinking_strategy`
- `thinking_visible_observed`
- `thinking_replay_observed`

这些字段必须从 config 进入 runtime，并写入 session metadata。

### 4.4 OpenAI 默认 `store: false`

原因：

- session / state / messages / events 的事实源必须是本地文件
- 不把 provider 侧持久化变成恢复前提

### 4.5 实话实说的 provider 限制

当前明确承认：

- OpenAI encrypted reasoning、Anthropic thinking signature / redacted data、Gemini thoughtSignature 都是 provider-native continuation fact，只能由对应 adapter replay。
- 这里的 `redacted data` 是 Anthropic 上游协议事实，不是项目级默认脱敏规范。
- `Message.thinking` 只保存 provider 明确返回的可读 summary/text；opaque/encrypted/signature 数据不得进入 UI 文本、toast、普通 events 文本或报告正文。
- Chat-compatible `reasoning_content` / `reasoning_details` / `reasoning_opaque` 不是当前已实现能力；若后续支持，必须通过显式 adapter family 实现。

## 5. Spec 完成判定

当以下条件都成立时，当前 spec 才算收敛：

- `run` / `exec` / `steer` / `continue` 语义一致
- Web-first v1 与 advanced / experimental phases 的边界明确
- provider contract 与已验证协议事实一致
- README / AGENTS / scripts 与 spec 一致
- generation 选项的全链路传递已明确写清
