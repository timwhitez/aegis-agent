# Aegis Agent Runtime Architecture

## 1. 总体分层

运行时核心继续保持分层，Web-first 只改变默认 app surface，不改变 runtime 权威边界：

1. `core runtime`
   - 负责 loop、provider、tools、skills、hooks、compaction、session、events
2. `sdk facade`
   - 对外暴露稳定 Go 接口，未来可扩为公共包
3. app adapters
   - `web app/service adapter` 是默认产品入口，负责本地 HTTP API、内嵌前端、异步 start/continue handle、浏览器控制与观测
   - `cli adapter` 是稳定脚本化入口，负责参数解析、stdin/stdout、键盘中断、阶段输出

当前 app-facing 构造约束：

- 默认 Web / SDK 路径使用独立的 `core` facade 与 `web` service facade
- CLI 路径继续使用独立的 `core` facade，不直接依赖 Web service
- 扩展 delegation / queue 路径使用独立的 `experimental` facade
- Web console 路径使用独立的 `web` app/service facade，并作为默认 operator surface
- 纯 store / snapshot 读取路径使用独立的 `store` facade
- 这些 facade 可以共享更低层的 runtime primitive，但不能在 app 层重新坍缩成同一个 concrete runner type

约束：

- Web / CLI 都不直接持有 provider 逻辑。
- provider 不直接写终端。
- hooks 不直接改写 session 文件。
- session store 是唯一持久化入口。
- compaction 只改“提供给模型的上下文视图”，不破坏原始日志。
- live steer 不通过共享内存偷渡输入，而通过 session store 管理的控制队列并入执行。

## 2. 核心对象

### 2.1 Runner

职责：

- 创建或恢复 session
- 管理 active run 与 interrupt cancel 函数
- 调用 runtime engine
- 决定 `run` / `exec` 的 completion policy
- 向 session store 和 event bus 写入结果

### 2.2 Engine

职责：

- 执行单个 session 的 agent loop
- 调用 provider
- 分发 tool calls
- 触发 hooks
- 更新 session state
- 在合适时机触发 compaction

### 2.3 ProviderAdapter

职责：

- 接收内部 `TurnRequest`
- 调用供应商 HTTP API
- 归一化为内部 `TurnResult` 和 provider 事件
- 处理 provider 特有的多轮回放格式

### 2.4 ToolRegistry

职责：

- 注册内置工具与 skill 提供的工具
- 输出 provider 可消费的 tool schema
- 执行工具并返回结构化结果
- 维护 versioned role capability profile 作为 tool 可见性和执行权限的唯一事实源；provider schema 过滤与 `Registry.Execute` 必须调用同一个 profile 判定，不能维护两份 allowlist
- `explorer-readonly-v1` 只允许 `read_file`、`grep_files`、`grep`、`glob`、`load_skill`、`finish`；包括 trusted skill command 在内的其他工具既不进入 provider schema，也在直接/恢复/伪造调用时以 `failure_class=schema_reject`、`error_code=tool_not_allowed_for_role` fail closed

### 2.5 SkillCatalog

职责：

- 扫描本地 `skills/`
- 缓存 skill metadata
- 为 `load_skill` 提供完整正文

### 2.6 HookManager

职责：

- 根据配置载入 hooks
- 按顺序执行 event/transform hooks
- 记录 hook 成功、失败、阻断

### 2.7 PlanningStore

职责：

- 管理 `todo.json` 与 `tasks/`
- 提供 todo update / read
- 提供 task graph CRUD、依赖校验与 ready-state 计算

### 2.8 SessionStore

职责：

- 管理 `session.json`、`state.json`、`messages.jsonl`、`events.jsonl`、`control/`
- 管理 `goal.json`、`artifacts/goal-history.jsonl`、`contract.json`、`artifact-tracker.json`、`provider-attempts.jsonl`、`parent-coordination.json`、`session.md` 与 `checkpoints/`
- 管理 `file-changes.json`：runtime 在每个 write_file / edit_file / shell 工具调用**成功**后增量累加该 session 的文件变更账本（按 workspace 相对路径归一化、仅统计成功操作），作为 Web 文件变更面板的事实源；该文件是派生视图，缺失或为旧 session 时由 Web 服务从完整 `messages.jsonl` 回填并持久化一次
- 管理 `planmode.json`、`artifacts/planmode-history.jsonl` 与 `artifacts/planmode-plan.md`
- 写入 compaction artifacts
- 为 `continue` 提供恢复数据
- 持久化 `control/steer.jsonl` 与 `control/background.jsonl`

### 2.8.1 SessionGoalManager

职责：

- 从 `StartRequest.Goal`、CLI `goal` 命令、Web goal API 或模型工具创建 / 更新 session-scoped goal
- 一个 session 默认最多一个 current goal，存储在 `goal.json`
- 将 goal 变化与预算计量追加到 `artifacts/goal-history.jsonl`
- 将 goal snapshot 注入 prompt、compaction summary、`session.md` 与 long-run checkpoint
- 默认用户入口只是一个 Goal 开关：Web start 选中后直接使用 prompt 作为 objective；success criteria、validation contract、features、milestones 与 role hints 由 agent 在运行中拆分和沉淀
- 只记录目标、可选内部结构化计划与用户控制状态；不得把 Goal 变成固定 DAG 或强制 child / queue 编排
- 当 goal/mission 要求 `require_plan_approval` 或 mission plan 进入 `needs_approval` 时，必须确保存在 linked Plan Mode；审批前的执行门禁由 Plan Mode 负责，而不是靠 mission prompt 文本自觉。若 mission 重新进入 `needs_approval`，已 approved / executing 的旧 Plan Mode 不能被当作新的 pending gate；pending 但未链接的 Plan Mode 必须补 `linked_goal_id` 或重新创建 linked gate。
- mission validation contract 有只读 coverage checker：它核对 contract assertion ID、feature `claimed_assertions`、milestone `validation_ids`、未覆盖断言、无 assertion feature、无 validation milestone、重复/空 ID 与未知引用；mission plan approval 默认因 uncovered / invalid contract 阻断，只有显式 override 才能继续
- 模型可用 `record_goal_progress` 对当前 goal 追加结构化 progress / handoff / validation evidence / evaluator child 或 queue 关联 / command / blocker / budget wrap-up 事实；该工具不得改 objective、不得 pause/resume/clear、不得 approve plan、不得跳过 `update_goal(status="complete")` 完成审计
- active goal 尚未完成且遇到外部阻塞、缺少用户输入或必须等待外部状态时，模型可以先记录 blocker，再调用通用 `await_input` 显式停靠 session；goal 继续保持 `active`，该动作不等同 pause/complete，也不绕过后续 completion audit
- `stop_on_budget=true` 时，budget 触顶会写入 durable budget wrap-up request，最多允许一轮模型 wrap-up；若未通过 `record_goal_progress(kind="budget_wrapup")` 写入 wrap-up 事实，则 `finish` 被 completion gate 阻断，并且 runtime 回到 `awaiting_input` 而不是无限继续 provider turns
- `update_goal(status="complete")` 必须把 completion evidence、summary 和 criteria / validation item 状态回写 `goal.json` 的当前快照，同时追加 `artifacts/goal-history.jsonl`

### 2.8.2 SessionPlanModeManager

职责：

- 从 `StartRequest.PlanMode`、CLI `--plan/--plan-only`、Web Plan toggle 或 planmode API 创建 session-scoped Plan Mode
- 也可由要求 plan approval 的 goal/mission 自动创建或修复 linked Plan Mode，`linked_goal_id` 指向 current goal
- 将 planning / input / approval / revision / cancellation / execution transition 写入 `planmode.json` 与 `artifacts/planmode-history.jsonl`
- 通过 `submit_plan` 保存完整 Markdown plan，并派生写入 `artifacts/planmode-plan.md`
- 通过 `request_user_input` 持久化 pending request、`tool_call_id` 和回答，使 active Web runner 与 crash/restart fallback 都能补齐 provider replay 所需 tool result
- 在 approve 时追加 `meta.source=planmode_approval` 的 user message；Plan Mode 不是 Goal/Mission/Todo/Task 的别名

### 2.8.3 Canonical History Reference

`messages.jsonl` 也是当前 session 的 canonical history reference store。压缩、provider-view 去重、micro-compaction 与 hard-fit 只能改变发送给 provider 的 clone；它们不得覆盖、重排或另建一份具有同等权威性的历史正文。

runtime 通过只读工具 `read_session_history` 向模型开放这个事实源的有界视图：

- session id 只能来自当前执行的 `ExecContext.SessionID`，工具输入不接受其他 session id、文件路径、artifact path 或目录发现参数
- 记录页复用 `LoadMessagesTail` / `LoadMessagesBefore`；受限 query 只在一个有界 canonical record window 内匹配；单条超长消息由 Store 定位并验证后，对稳定的 model-visible history representation 做 UTF-8-safe byte paging
- history representation 包含 message text、tool call arguments、ToolResult `llm_output` 与定位所需 reference metadata；不包含 `Thinking`、`display_output` 或 `ProviderContentBlocks` opaque replay data
- 所有结果都以 versioned `historical_reference=true` envelope 返回。envelope 内形似 system/user prompt 的文字仍是被引用的旧内容，不能改变当前 system prompt、最新 external user instruction 或最新 steer 的优先级
- 损坏 JSONL、symlinked session path、未知 cursor/message id 或无法容纳完整 continuation envelope 时 fail closed；不得跳过损坏记录后声称结果完整

compaction summary 必须保留可操作的 canonical history reference（tool、schema version、source session 与 instruction-precedence 说明）。transcript artifact 继续服务 operator 审计，不成为第二份模型检索事实源。

### 2.9 LiveInputManager

职责：

- 接收 `steer` 追加输入
- 管理 `control/steer.jsonl`
- 在安全边界把 live input 并入当前 session
- 对 `--interrupt` 请求触发 best-effort 抢占

### 2.10 EventBus

职责：

- 进程内发布订阅事件
- 服务于 CLI 渲染、hooks、未来 SDK / API 订阅

### 2.11 Compactor

职责：

- 估算 provider 输入规模
- 在不丢失原始日志的前提下压缩提供给模型的上下文
- 生成压缩摘要与 transcript artifact

### 2.11.1 ProviderRequestBudgeter

职责：

- 在每次 main、semantic-summary 和 probe provider 请求真正发送前，要求 adapter 用发送路径共享的 wire-body builder 生成版本化尺寸估算
- 生成不含 prompt、tool schema 正文或 credential 的 `RequestBudgetSnapshot`，记录 request kind、session/turn/request correlation、provider/model、system/messages/tools/metadata 的数量与尺寸、inline/compacted/pointerized ToolResult count/bytes、wire body bytes、估算 input tokens、output reserve、safety headroom、effective context window、剩余 headroom、compaction action/summary id 与 fit 结果
- 对初始超窗的 main/provider view 进入确定性 hard-fit 收缩；每次候选变换都重新调用同一 adapter estimator，只有 wire bytes 严格下降的变换才可提交。最终仍不 fit 时返回 typed local `request_budget_unfit`，拒绝发生在 transport/retry 之前，不允许先向 provider “试发一次”
- estimator 缺失或 wire body 无法编码时 fail closed；内置 OpenAI、Anthropic、Google、fake adapter 都必须实现 estimator，测试/第三方 adapter 不得静默跳过
- main 请求拒绝时写 `provider.request.prepared(fit=false)`、budget/rejected 事件并按 provider-call failure 收敛；semantic-summary 拒绝只让语义摘要回退到确定性 baseline

边界：

- hard-fit 顺序固定为：复用已完成的 safe dedup/current-result/micro/full compaction；pointerize 有完整 session artifact 或可由原 call/path/range/current-view cursor 恢复的 result；从最老可丢 message/replay 闭包缩短 recent tail；删除 optional semantic summary 并把 deterministic summary 缩到 current goal/open items/key paths/latest external/latest steer/transcript reference
- 最新 external user instruction、最新 steer、最新 tool result 及其合法 replay 依赖是不可静默删除边界；tool schemas 也不能为 fit 临时裁掉。某个不可丢 component 单体或最终最小视图仍超窗时，`request_budget_unfit` 只报告 request kind、blocking component、estimated/available/reserved 数值，不包含 prompt/tool 正文
- 收缩 pass 有固定上限，已提交 action 的 adapter wire bytes 必须严格递减；每个 action 以 request id/kind 关联 before/after estimate 和受影响的 message/tool-call id/count，不记录内容正文
- `provider.call` 只能在最终 snapshot `fit=true`、prepared event 已落盘且 pause gate 已通过后发出，避免把本地拒绝记成已发送调用；local unfit 不进入 provider transport retry、auto-resume 或 max-token resume

### 2.11.2 ContextTelemetryReporter

职责：

- 把 `events.jsonl`、`messages.jsonl` 与 `session.json` 中已经存在的事实派生成 versioned `ContextReport`；报告不写回 session，不保存第二套请求状态，也不参与 compaction、threshold 或 delegation 决策
- 直接反序列化 `provider.request.prepared.data.request_budget` 中的同一个 `session.RequestBudgetSnapshot`；runtime 只保留类型别名，hard-fit 与报告不得复制 estimator 公式或维护字段相同的另一种 snapshot
- 以 `request_id=<session>:<turn>:<request_kind>:<request_sequence>` 归并 prepared、budget action、compaction、provider callback、completed/failed 与 legacy `turn.stopped`；`main` 和 `semantic_summary` 是不同 request kind，transport retry 仍属于同一个 request id
- 只输出尺寸、计数、ID、状态、时间与 provider 已报告的 usage；不得复制 system/user/tool 正文、tool schema、metadata value、error 正文、display output 或 raw provider payload
- 从请求 session 自动解析 root，递归遍历 child，按 session id 去重并对 lineage cycle fail closed；root 指标和 child 指标必须分列，不能只给 total
- session 报告至少包含 request/turn/tool-call 数、compaction lifecycle 计数、request peak/aggregate、provider-view inline/compacted/pointerized bytes、唯一 tool artifact persisted bytes、known provider usage、unknown usage request 数和 wall time
- root aggregate 至少分别给出 root peak、child peak、root/child/total aggregate input、root/child/total provider-view inline/compacted/pointerized bytes、root/child/total artifact bytes 与 known usage、root/child request/turn/tool-call/compaction 数、child session 数、unknown usage 数与 lineage wall time

查询面：

- Store：`ContextReport(sessionID)`，使用 streaming `VisitEvents` / `VisitMessages`，不要求把超长 JSONL 整体载入内存
- SDK/Core：`Context(sessionID)`
- CLI：`aegis-agent sessions context <session-id> --json`
- Web：`GET /api/sessions/<id>/context`；只在用户打开现有 inspector 的 Context tab 时懒加载，并对 session/request detail 设 64 项总预算和显式 truncation metadata，aggregate 不截断

`ContextReport` 是 operator/read-only advanced surface。默认 Web 首页不增加 telemetry dashboard，也不按报告结果自动改 prompt、threshold 或委派策略。

### 2.12 SessionContractManager

职责：

- 从最新外部用户指令、provider/session metadata 与已存在的 guard parser 中提取 session contract
- 写入 `contract.json`、`artifacts/contract-history.jsonl` 与 `artifact-tracker.json`
- 在 `start`、`continue` 和已接纳 `steer` 后刷新 contract snapshot
- 只记录显式要求与可验证交付约束，不把 runtime 变成固定 workflow engine

### 2.13 CompletionController

职责：

- 作为 tool/finish gate 的统一入口
- 先复用已有 review、artifact、template、literal、target、taskboard、steer guard
- 再执行 required-artifact baseline/touched/changed gate
- 再执行 parent child/queue coordination gate
- 再执行 active goal completion audit gate，要求模型在 `finish` 前读取 / 审计 goal，并只能通过 `update_goal(status="complete")` 标记完成
- active goal completion audit gate 依赖 `goal.json` 当前快照；completion evidence 不得只留在 history 中
- pending Plan Mode gate 位于基础 tool guard 之后、artifact/parent/goal completion gate 之前；它阻断 shell/write/edit/todo/task/goal update/finish/agent/queue/custom tools，只允许 read/search/load_skill/get_goal/get_plan_mode/request_user_input/submit_plan
- 把 `completion.evaluate.*`、`completion.gate.*`、`artifact.gate.*`、`artifact.tracked` 事件写回 session

### 2.14 ProviderAttemptLedger

职责：

- 把 provider retry、auto-resume、max-token partial-output resume、final failure、success 写入 `provider-attempts.jsonl`
- 保留 provider/model、turn、attempt、timeout/retry 线索、error class、timeout kind、status code、response id、provider 返回的 cache read/write token 计数
- 只作为恢复与诊断事实，不反向驱动 adapter retry policy

### 2.15 SessionSummaryWriter

职责：

- 生成 `.aegis-agent/sessions/<id>/session.md`
- 聚合 state、contract、artifact tracker、todo/task、provider attempts、children、queue、background notifications 和 checkpoint 位置
- 只作为 operator-readable 派生视图，不能替代 `messages.jsonl` / `events.jsonl` / `state.json`

### 2.16 LongRunCheckpointWriter

职责：

- 对大型、delegated、child/queue、isolation、显式 artifact contract、task-heavy、compacted session 写 `checkpoints/longrun-latest.json`
- 记录 resume index、artifact status、task summary、provider options、parent wait state、resume hints
- `continue` 发现 checkpoint 时可插入 harness resume note，但不能覆盖原始日志或改变 session fact source

### 2.17 DelegationManager

职责：

- 为 parent session 创建 child session
- 维护 parent / root / depth 元数据
- 为 child session 准备 isolation workdir
- 统一 CLI 与 tool 的 delegation 契约
- `agent_role` 可显式选择 `planner` / `generator` / `evaluator` / `explorer`，并作为 role hint 传入 child session；`agent_name` 只是人类可读标签，不参与 role provider override 匹配
- Settings / config 可为四种 role 单独声明 provider、API provider、base URL、model、`reasoning_effort` 与 `max_output_tokens` override；空字段继承 parent session 的 effective provider options（provider 不同时继承该 provider profile defaults），显式请求中的 provider/model/provider options 仍优先
- role routing override 继续只在调用方未显式选择 provider 时生效；`reasoning_effort` / `max_output_tokens` 是 role generation override，在 provider/model 解析后、显式 request provider options 之前合并
- `explorer` 映射到 durable `tool_profile=explorer-readonly-v1`，其他 role 映射到 `tool_profile=default`；queue job、child `session.json`、创建/排队事件和 background notification 都保存 effective profile，旧记录缺字段时按 `agent_role` fail-secure 派生
- `explorer` 未显式指定 `isolation_mode`（空值或兼容 `default`）时 effective mode 为 `off`；显式 `off` / `auto` / `git` / `copy` 保持调用方选择。effective mode 必须进入 queue job 与 child metadata
- child handoff 必须依赖可见文件事实，例如 `reports/spec.md`、`reports/plan.md`、`reports/progress.md`、`reports/validation.md` 与 visible output 列表，而不是依赖进程内临时上下文
- explorer role prompt 只约束只读身份与有界交付格式：简短结论、`claim | file:line | confidence`、未覆盖范围、关键疑点；不规定固定阅读顺序、审计路线、taskboard 节奏或必须 delegation

### 2.18 QueueStore And Worker

职责：

- 管理后台自治 job 持久化
- claim queued jobs
- claim 时写入 durable lease：`claimed_by`、`claimed_at`、`heartbeat_at`、`worker_pid`、`process_start_id`
- 拉起真实 child session 并回写结果
- worker 处理 job 期间刷新 `heartbeat_at`，使 running job 的 owner/liveness 可由文件事实解释
- 在活跃 CLI 进程内提供 auto worker
- 将 child 完成/失败结果投递回 parent session 的控制通知
- reconcile running job 时只做文件事实可证明的收敛：已完成/失败的 linked session 可修复为完成/失败；recent heartbeat 且未结算的 job 保持 running；stale 且找不到 linked session 的 job 可标记 failed 并记录 orphan/stale error
- 当 parent session 被显式 stop 时，未 claim 的 parent-linked queued job 应转为 `blocked` 并标记 `stop_reason=parent_stop`，已 linked 且因 parent stop 暂停的 child session 对应 job 也应收敛为 `blocked` / `stop_reason=parent_stop`；这类 job 不阻断删除，但仍保留在 parent coordination 的 unresolved 集合中
- parent 继续运行后，模型可用 `agent_prompt` 主动恢复这些 parent-stopped 子任务：有 linked child session 的 job 通过 child `continue` 恢复，尚未 claim 的 pre-claim job 重新进入 queued，由 worker 后续领取；runtime 不应自动无条件恢复所有 paused child，避免误启动用户单独暂停的子任务
- 周期性回收孤儿 job（liveness reaper）：持有进程已死（`process_start_id`/`worker_pid` 经 `/proc` 探测不存在）或心跳超 `lease_stale_after` 的 `running`/`blocked` job 必须被回收，否则其 parent 会永久 parked。回收按混合策略——linked child 终态→结算终态、未建会话→重新入队、其余→`blocked` 并写 parent notification——只做 liveness 恢复，不替模型决定 workflow。foreground/direct child reservation 同样必须校验 owner PID 与 Linux boot-scoped `/proc/<pid>/stat` start identity，不能让 PID reuse 或 dead owner 被 stale `state.status=running` 覆盖；回收时把对应僵尸 running child 原子收敛为 `paused/stale_owner_reconciled` 并写 durable event。`web` 进程也继续把无存活 handle owner 的其他僵尸 `running` session reconcile 为 `paused`
- 全局 `runtime.max_turns_hard` 是 per-run hard limit，显式启用后对 root、foreground child 与 background/queue child 一致生效；默认 `-1` 关闭。child / background（有 `parent_session_id`）还可选择启用独立兜底预算：`max_turns_per_attempt`、`max_active_runtime_sec`、`max_elapsed_sec` 默认全部为 `0`，分别表示 per-attempt turn、仅执行期 active runtime 与从 job/session 创建时起计算的 absolute deadline
- child effective budget 必须在 queue job 与 child `session.json` 中版本化快照；Settings 热更新只影响新 child/job。active runtime 通过带 cause 的 deadline context 传播到 provider、tool、hook、shell 与 nested execution，paused/offline/background wait 不消耗 active runtime；absolute deadline 以持久化 `deadline_at` 跨进程重启继续生效
- active-runtime dimension 启用时，run 开始先持久化 owner-tagged open lease，执行中按 snapshot interval 增量 checkpoint 到 session 与 linked queue job，稳定退出时结清并关闭。crash recovery 对未闭合 lease 只补记一个 interval 的有界 uncertainty charge，不能把 offline wall time 计入；checkpoint ledger 无法持久化时必须 cooperative cancel 当前 provider/tool/hook/shell 并把 child 收敛为 failure，不能在预算事实不可写时继续产生副作用
- budget 触顶以可恢复 `paused` 收敛（→ job `blocked`）并通知 parent。parent 可通过 `agent_prompt.budget_extension` 显式追加下一 attempt 的 turns/active runtime、延长 absolute deadline 或清除对应维度后恢复；没有有效 extension 的 budget-paused resume 必须拒绝，不能立即再次 pause
- parent 可用 `agent_stop` 按 `session_id` 或 `queue_job_id` 取消自己名下的 queued/running/paused child work。running child 先写 durable `cancel_requested`，再 cooperative cancel active context；queued job、已停止 child 和最终 queue outcome 使用 `cancelled`，不得计入 execution failure。对 budget-paused blocked job 的 settle 只把 job 标记为 `cancelled` 并释放 coordination，child session 保留 paused 与原始预算事实
- parent 在 `background_wait` 等待时，若 unresolved 工作全部不可推进，runtime 写 `parent.coordination.deadlock` 事件并注入 `coordination_deadlock` background notification 唤醒模型决策；`runtime.queue.background_wait_timeout_sec` 提供墙钟兜底。两者都只把事实交还模型，不自动解决 unresolved 工作

### 2.19 TerminalDashboard

职责：

- 渲染 session / child / queue / event 面板
- 只读取文件事实，不持有权威状态

### 2.20 WebConsoleService

职责：

- 作为默认 Web-first app surface，提供本地 HTTP API 与静态前端资源
- 复用 `SessionStore` / `Runner` / `QueueStore`，不创建第二套状态源
- 为 Web 发起的 `start` / `continue` 建立异步执行句柄
- 维护 queue worker pool，并通过独立 worker `Runner` 支持后台并行消费
- 提供 overview / session detail / queue / children / task board 的聚合只读视图
- 对 `steer`、`continue`、`queue submit`、`interrupt` 等控制操作做参数校验与状态映射
- 提供 goal 的本地 REST 控制面：start payload 创建 goal，session detail 返回 goal，用户可以 pause/resume/clear/complete；内部结构化计划与 validation contract 可被 agent 或高级 REST 调用 patch/approve，但不作为默认用户启动表单
- 将 WebConsole active handle 的 owner/process 线索写入 session events，并在 session detail、`session.md` 与 long-run checkpoint 中展示最近 owner 线索；不得把 in-memory cancel handle 伪持久化

约束：

- Web service 是默认 operator surface，但只能作为本地控制台运行，不能成为 session / goal / plan / queue 的权威状态源
- active session 的中断控制必须是 session-scoped，不允许多个并发 session 共享同一个 in-memory interrupt slot
- Web UI 的刷新默认使用 polling；是否升级到 SSE / WebSocket 不作为当前实现前提

## 3. 内部消息模型

内部统一消息结构：

```text
Message
  id: string
  role: user | assistant | tool | system
  text: string
  tool_calls?: []ToolCall
  tool_results?: []ToolResult
  meta?: map[string]any
```

说明：

- `system` 仅作为内部建模与回放辅助，不直接暴露为用户输入角色
- `tool` 是统一抽象；具体 provider 回放时会转换为 `function_call_output`、`tool_result`、`functionResponse` 等形式

### 3.1 ToolCall

字段：

- `id`
- `name`
- `arguments`
- `provider_call_id`

### 3.2 ToolResult

字段：

- `tool_call_id`
- `name`
- `llm_output`
- `display_output`
- `is_error`
- `final`
- `metadata`

约束：

- `llm_output` 面向模型
- `display_output` 面向 CLI
- `final` 仅由 `finish` 工具使用
- 每个结果在落盘前都经过统一 byte finalizer；`metadata` 记录原始、inline、artifact 与丢失字节事实，不能把不完整 artifact 宣称为完整输出

## 4. 最小 Agent Loop

核心循环保持极简，不在 loop 里塞固定工作流：

```text
prepare -> provider_call -> parse_result -> maybe_execute_tools -> append_results -> next_turn
```

伪代码：

```text
while true:
  drain_control_inputs_if_any()
  input = build_turn_request(session)
  result = provider.run(input)

  append assistant output

  if result has tool calls:
    execute tools
    append tool results
    continue

  if completion policy says turn can stop here:
    return

  inject reminder / mark incomplete / fail based on mode
```

## 5. Turn 生命周期

每轮 turn 固定为以下阶段：

1. `prepare`
2. `project_docs`
3. `control_drain`
4. `compact`
5. `provider_call`
6. `assistant_output`
7. `control_drain`
8. `tool_dispatch`
9. `tool_execute`
10. `tool_result_append`
11. `turn_decide`

### 5.1 prepare

- 读取当前 state
- 读取当前 `todo.json`、`tasks/` 与 `reports/spec.md` / `plan.md` / `progress.md` / `validation.md` 的 durable context 视图，并写一条 `session.context.loaded` 事件，记录 project-memory present/missing 与 task/todo 计数，方便恢复与 live validation
- 必要时插入 harness reminder message
 - 典型场景：
    - `exec` done-candidate 但还未显式 `finish`
    - 请求的 artifact 已经写出，可以提示优先收尾，但不阻断必要的继续核对
    - 最新 interrupt steer 已明确要求“use current evidence / write artifact / finish”，需要把执行直接拉回交付
    - 大型多步骤工程/评审任务已经展开时，可以提示外置 `reports/spec.md`、`reports/plan.md`、`reports/progress.md`、`reports/validation.md` 与 durable task state，作为恢复与协作建议
    - 同一 session 内 `load_skill` 或语义 no-op `todo_write` 出现高频重复短模式；此时 reminder 只提示复用现有证据、推进 artifact 或声明 blocker，不指定固定读取顺序、审计路线或委派策略
    - `read_file` 是多文件分析的正常基础能力，不因总次数、同一路径或同一 range 重复而触发 runtime reminder 或 hard guard；是否复读由模型基于任务需要判断
- 构造 system prompt
- 拼接 skills 摘要和 AGENTS 指令链
- 生成本轮 provider 输入视图

### 5.2 compact

- 在 provider 输入构造完成前检查是否需要压缩
- 不改原始 `messages.jsonl`
- 只改发送给 provider 的上下文视图及压缩 artifact

### 5.3 control_drain

- 检查 `control/steer.jsonl` 是否有待处理输入
- 检查 `control/background.jsonl` 是否有待接纳的后台结果
- 若有，按到达顺序转为新的 user messages
- `--interrupt` 请求可在此阶段把当前执行切换到新的 user priority
- 对无法立即抢占的情况，记录 `session.steer.deferred`

### 5.4 provider_call

- 先基于 adapter 的真实 wire body 构造 request budget snapshot；只有 `fit=true` 才调用 provider adapter
- tool schema 先经过当前 session durable `tool_profile` 过滤，再与 Plan Mode capability 取交集；过滤发生在 adapter 转换与 request budget 估算之前
- 产生 `assistant.delta`、`tool.call.ready`、`provider.error`、`turn.stopped` 等事件

### 5.5 tool_execute

对每个 tool call：

1. 触发 `tool.before`
2. 通过 `CompletionController` 应用 runtime 级 guard
   - runtime hard guard 仅用于必要边界：显式用户 contract、workspace path safety、shell timeout/output limit、required-artifact gate、provider/tool 协议完整性、恢复一致性，以及最新 interrupt steer 的明确约束
   - 不因 `read_file`、`grep`、`glob`、read-only shell 等只读检索调用次数而阻断工具；多文件分析可以按需继续读取
   - 当最新 interrupt steer 已明确要求立即交付时，guard 可以阻断继续的只读探索、todo/task bookkeeping 或 skill-loading detour，把执行拉回 `write_file` / `edit_file` / `finish`
   - artifact / project-memory / large-project coordination 默认通过 prompt note 或 harness reminder 提示，不作为普通读取、验证或 finish 的 hard guard，除非用户当轮明确指定为必须交付 contract
   - role capability profile 在普通 CompletionController workflow guard 之前形成不可绕过的 capability boundary；恢复轨迹、兼容 provider 或伪造 call 即使请求了隐藏工具，也只能得到稳定 typed ToolResult，不能执行工具或 trusted command
3. 执行工具
4. 触发 `tool.after`；hook 可以改写 `llm_output` / `display_output`
5. 恢复/确认 `ToolCallID`、`Name`、`IsError`、`Final` 与原 metadata 后，执行唯一的 `finalizeToolResultForContext`：分别限制 `llm_output` 与 `display_output`；需要保存的原始 `llm_output` 通过 Store 的 owner-only、no-symlink、quota-aware writer 写入当前 session 的 `artifacts/tool-outputs/`
6. finalizer 完成后才允许写 `tool.after` event 与 `messages.jsonl`；一个 batch 中每个 ToolResult 独立预算，hook、skill command、child handoff、synthetic/interrupted result 都不能绕过。artifact 失败或 quota 拒绝时仍落盘有界 preview，但必须标记 `recoverable=false`、准确 omitted bytes 和原因
7. 成功的 `write_file` / `edit_file` 会更新 `artifact-tracker.json`，使 required-artifact gate 能区分“文件本来存在”和“本 session 确实写过或改过”
8. `load_skill` 默认基于 session state 做幂等：同一 skill 已加载时返回 compact `already_loaded`，只有显式 `force_reload` 才再次注入完整 skill 正文
9. `read_file` 可以重复读取同一路径或 line window；runtime 不写入重复读取 warning，也不阻断
10. `todo_write` 对 content/status/priority/order 未变化的 snapshot 标记 `noop=true` / `changed=false`，不刷新 `todo.json` 时间戳
11. 若执行在结果写回前被取消，生成一条可重放的中断错误结果；该结果也经过同一 finalizer
12. 对标记为 ephemeral 的旧工具输出，只在 provider request view 中应用按工具类型计算的滑动窗口：最新 `EphemeralWindow` 个结果和短结果保持 inline；窗口外的旧结果优先复用 durable result 已有 artifact，确需新写时也只能调用相同 quota-aware Store writer。pointer 只有在 artifact 完整时才能称为 complete/full；短错误摘要继续 inline。原始 `messages.jsonl` 与 tool event 不被 provider-view 变换覆盖；`grep` / `grep_files` / `glob` 仍不得把 artifact 目录作为 discovery 输入。三个 discovery 工具必须在 workspace/skill resolver 前识别 `artifacts/tool-outputs` 及其子路径，返回 `unsupported_path_source`、保留原 path 并指向 `read_file` 精确读取，不能误报 `not_found`
13. 同步 `agent_spawn` / `agent_status` 的长 `final_text` 随整个 ToolResult 经过相同预算；background notification 转成 parent 的 user message 并写入 `messages.jsonl` 前也使用同一 handoff byte cap，并保留 queue job、child session 与 artifact reference。原始 queue notification 仍是独立 durable fact，不属于 provider context view
14. `await_input` 的成功结果会终止同批后续工具调用、先落盘有界 tool result，再把 session 转为 `awaiting_input`；它不设置 `ToolResult.final`，不触发 completed，也不改变 Goal 状态

### 5.6 turn_decide

分支规则：

- 如果模型调用了 `finish` 工具，必须先通过 `CompletionController` 的 finish gates；通过后 session 完成
- 如果模型调用了 `await_input`，runtime 持久化 kind、reason、blockers 和 resume condition，停止当前 tool batch，并进入 `awaiting_input`；只有 `finish` 表示完成
- 如果有普通 tool calls，则进入下一轮
- 如果没有 tool calls：
  - 统一视为 `done_candidate`，继续下一轮 provider turn；普通“无 tool / 无 finish”不能直接结束 loop
  - runtime 可注入 finish-oriented harness reminder，明确任务若已完成必须显式调用 `finish`
  - 若连续多个 `done_candidate` turn 都只有文本、没有 valid tool call，且未接纳 steer/background/finish，则 runtime 可按 `runtime.degeneration` 配置注入 `degeneration_recovery_required` harness reminder；继续无进展时，`run` 模式以 `model_degeneration_no_progress` 停靠到 `awaiting_input`，`exec/init` 模式以同一 reason 失败
- 如果上下文取消，则进入 `paused`

## 6. Completion Policy

`aegis-agent` 区分两种 completion policy：

### 6.1 interactive

用于 `run`

- `finish` 表示整个 session 已完成
- `await_input` 表示任务未完成但当前无法安全继续；它是模型显式选择的可恢复停靠，不是完成或错误
- 无 tool calls 且无 `finish` 时，不强行判定完成，也不因单次 `done_candidate` 直接停到 `awaiting_input`
- 普通 `done_candidate` turn 应继续 loop；只有显式等待/停靠场景（例如 pause、plan gate、budget wrap-up、background wait、degeneration park）才进入 `awaiting_input`
- 进入 `awaiting_input` 前若属于退化停靠，应写 `session.idle_parked` 事件并记录 `idle_reason=model_degeneration_no_progress` 与可选 `incomplete_reason`
- 用户后续可用 `continue --message` 补充提示
- 若运行中收到 steer 输入，默认在最近安全边界直接并入，不必先进入 `awaiting_input`

### 6.2 autonomous

用于 `exec`

- 默认要求显式 `finish`
- `await_input` 可以让 autonomous session 以非 completed 的 `awaiting_input` 状态返回，供 operator 后续 `continue`；脚本不得把该状态解释为成功完成
- “无工具调用且无 `finish`”只算 `done_candidate`，runtime 继续 loop，不把它当作成功或自然结束
- runtime 可插入一次 harness reminder，要求任务完成时显式调用 `finish`
- 若最新 interrupt steer 已明确要求立即交付，runtime 可插入专门的 completion reminder，并对继续的只读探索或 bookkeeping detour 加 guard，直到出现交付动作或新的外部指令
- 若连续 `done_candidate` 空转达到 `runtime.degeneration.give_up_after`，则使用 `model_degeneration_no_progress` 失败原因；`exec` 的成功条件仍然只有显式 `finish`

## 7. Session 状态机

允许状态：

- `running`
- `awaiting_input`
- `paused`
- `completed`
- `cancelled`
- `failed`

状态转换：

```text
new -> running
running -> awaiting_input
running -> paused
running -> completed
running -> cancelled
running -> failed
awaiting_input -> running
paused -> running
failed -> running
completed(root) -> running
```

不允许：

- `completed(child/queue) -> running` 通过通用 `continue` 直接发生；completed child 已经结算 queue job、parent coordination 与 background notification，后续工作必须从 parent 使用 `agent_prompt` 的可恢复路径，或提交新的 queue job 重新排队
- `completed -> paused`

`completed(root) -> running` 只表示用户在已完成 root session 上追加 follow-up，并复用原始消息历史。该转换必须写入 durable `session.resumed` 事件及 `resumed_from=completed`；若原 session 的 Goal 已 complete，普通 follow-up 保留它作为历史完成事实，不自动恢复为 active，也不重新启用旧 Goal 的 completion gate。

## 8. 事件模型

所有关键动作都映射成结构化事件：

- `session.started`
- `session.awaiting_input`
- `session.paused`
- `session.completed`
- `session.cancel_requested`
- `session.cancelled`
- `session.failed`
- `user.message`
- `assistant.delta`
- `assistant.message`
- `tool.before`
- `tool.after`
- `tool.interrupted`
- `session.child.spawned`
- `session.child.queued`
- `queue.job.claimed`
- `queue.job.completed`
- `queue.job.cancelled`
- `queue.job.failed`
- `session.steer.requested`
- `session.steer.queued`
- `session.steer.accepted`
- `session.steer.deferred`
- `todo.updated`
- `task.created`
- `task.updated`
- `session.context.loaded`
- `hook.triggered`
- `hook.finished`
- `hook.failed`
- `state.changed`
- `compact.started`
- `compact.finished`
- `compact.reused`
- `provider.retry`
- `provider.auto_resume`
- `contract.created`
- `contract.updated`
- `completion.evaluate.started`
- `completion.gate.passed`
- `completion.gate.blocked`
- `completion.evaluate.finished`
- `artifact.tracked`
- `artifact.gate.passed`
- `artifact.gate.blocked`
- `checkpoint.resume_hint.injected`
- `goal.created`
- `goal.updated`
- `goal.accounting.updated`
- `goal.budget_limited`
- `goal.completed`
- `goal.paused`
- `goal.resumed`
- `goal.cleared`
- `mission.plan.updated`
- `mission.plan.approved`
- `mission.validation.updated`
- `planmode.created`
- `planmode.linked_goal`
- `planmode.input_requested`
- `planmode.input_answered`
- `planmode.input_cancelled`
- `planmode.plan_submitted`
- `planmode.plan_approved`
- `planmode.execution_started`
- `planmode.plan_revised`
- `planmode.cancelled`

每个事件字段至少包括：

- `schema_version`
- `id`
- `session_id`
- `type`
- `time`
- `phase`
- `data`

## 9. 中断模型

### 9.1 `run` 模式

- CLI 在单独 goroutine 中监听键盘输入
- 捕获 `Esc` 后调用 `Runner.Interrupt(sessionID)`

### 9.2 interrupt 效果

- cancel 当前 provider 请求或工具执行上下文
- 如果 assistant 已经发出 tool call 且工具执行被打断，先落一条 `is_error=true` 的中断 tool result，再写 `paused`
- 将 state 改为 `paused`
- 写入 `session.paused` 事件
- 返回控制权给 CLI

### 9.3 恢复原则

不恢复中断前已经启动的外部进程：

- shell 子进程
- 未完成 hook command
- 未完成系统调用

恢复只从文件事实重新构建消息与状态。
