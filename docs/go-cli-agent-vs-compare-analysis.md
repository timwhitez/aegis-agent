# `go-cli-agent` 与 `compare` agent 框架对比分析

## 1. 范围与方法

本文对比的两个对象是：

- 当前主代码库 `go-cli-agent/`
- 仓库内对照实现 `compare/`（下文提到的 `compare` 均指这个目录）

本次分析先读取了当前仓库要求优先阅读的 spec：

- `spec/00-product.md`
- `spec/01-runtime-architecture.md`
- `spec/03-provider-contracts.md`
- `spec/09-phase-plan.md`
- `spec/11-spec-audit-and-traceability.md`
- `spec/12-task-system.md`
- `spec/13-live-input-and-steering.md`

随后重点阅读了两边的以下实现：

- `go-cli-agent`：
  - `internal/runtime/facade.go`
  - `internal/runtime/runner.go`
  - `internal/runtime/engine.go`
  - `internal/runtime/delegation.go`
  - `internal/runtime/compaction.go`
  - `internal/runtime/project_memory.go`
  - `internal/runtime/review_guard.go`
  - `internal/provider/*.go`
  - `internal/session/store.go`
  - `internal/session/taskboard.go`
  - `internal/session/types.go`
  - `internal/tools/registry.go`
  - `internal/tools/path.go`
  - `internal/skills/catalog.go`
  - `internal/webconsole/service.go`
  - `internal/app/app.go`
- `compare`：
  - `compare/internal/runtime/run.go`
  - `compare/internal/runtime/command_runner.go`
  - `compare/internal/runtime/resume.go`
  - `compare/internal/runtime/parent_controller.go`
  - `compare/internal/runtime/runtime_tools.go`
  - `compare/internal/runtime/provider.go`
  - `compare/internal/runtime/openai_response.go`
  - `compare/internal/context/compaction.go`
  - `compare/internal/session/schema.go`
  - `compare/internal/session/longrun.go`
  - `compare/internal/delegation/manager.go`
  - `compare/internal/delegation/delegation.go`
  - `compare/internal/extensions/extensions.go`
  - `compare/internal/tools/builtins.go`
  - `compare/internal/tools/workspace.go`
  - `compare/internal/tasks/todo.go`
  - `compare/cmd/opsx/main.go`

本文会明确区分三类内容：

- **已验证事实**：能直接从代码/结构中看到
- **设计判断**：根据实现形态得出的架构取向判断
- **借鉴建议**：在不破坏当前产品边界前提下的迁移建议

---

## 2. 结论先行

### 2.1 一句话结论

- `go-cli-agent` 更像一个 **session-first、provider-rich、交互/恢复语义相对更完整** 的通用 agent harness。
- `compare` 更像一个 **run-first、contract-driven、长任务执行纪律更强** 的 CLI agent framework。

### 2.2 不是“谁全面胜出”，而是“优化目标不同”

从代码看，这两个框架并不是简单的“新旧版本关系”，而是两种不同的产品判断：

- `go-cli-agent` 把 **交互式 session、实时 steer、provider 抽象、任务图、review guard、实验性扩展面** 放在更重要的位置。
- `compare` 把 **长任务交付纪律、required artifact、todo 推进、sub-agent park/resume、checkpoint resume、可信扩展资产** 放在更核心的位置。

因此，两边最值得借鉴的不是“照搬目录结构”，而是：

- `go-cli-agent` 借鉴 `compare` 的 **contract / checkpoint / completion gate**
- `compare` 借鉴 `go-cli-agent` 的 **provider / steer / task graph / evidence-aware compaction**

---

## 3. 顶层架构差异

### 3.1 durable truth 的基本单位不同

**已验证事实**

- `go-cli-agent` 的 durable truth 是 **session 目录**，由 `session.json`、`state.json`、`messages.jsonl`、`events.jsonl`、`control/`、`tasks/`、`artifacts/` 组成，见 `internal/session/store.go` 与 `internal/session/types.go`。
- `compare` 的 durable truth 是 **run 目录**，核心是 `.opsx/runs/<run-id>/` 下的 `state.json`、`events.jsonl`、`run.md`、`checkpoints/`，见 `compare/internal/runtime/run.go`、`compare/internal/session/schema.go`、`compare/internal/session/longrun.go`、`compare/internal/logs/summary.go`。

**设计判断**

- `go-cli-agent` 的主心骨是“一个会话如何持续演进”。
- `compare` 的主心骨是“一次长任务如何被可靠执行并留下 operator trace”。

这会直接影响后续几乎所有设计：继续执行、补充输入、子任务、artifact gate、扩展系统、compaction 方式都围绕这个 durable unit 展开。

### 3.2 CLI surface 的组织方式不同

**已验证事实**

- `go-cli-agent` 通过 `internal/runtime/facade.go` 明确拆成 `CoreRunner`、`ExperimentalRunner`、`StoreView`，CLI 命令在 `internal/app/app.go` 里按 `run/exec/continue/steer/sessions/tasks/probe-provider/doctor` 和 `experimental` 入口分开。
- `compare` 的 CLI 更薄，`compare/cmd/opsx/main.go` 直接暴露 `run`、`command`、`resume`、`smoke` 四个主命令，runtime 逻辑主要集中在 `compare/internal/runtime/*`。

**设计判断**

- `go-cli-agent` 更强调“核心面”和“扩展面”隔离，明显在维护 Phase 0-10 与 Phase 11+ 的叙事边界。
- `compare` 更强调“实际 operator 会用什么命令”，因此把 command / resume / smoke 直接做成第一层产品面。

### 3.3 provider 设计目标不同

**已验证事实**

- `go-cli-agent` 有完整 adapter 层：`OpenAI`、`Anthropic`、`Google`，并把 `temperature`、`top_p`、`reasoning_effort`、`text_verbosity`、`thinking_budget`、`store`、`metadata`、`retry_policy` 等一路传到 session metadata，见 `internal/provider/*.go`、`internal/runtime/engine.go`、`internal/session/types.go`。
- `compare` 当前 provider 非常聚焦：`fake` + `openai-response`，接口集中在 `compare/internal/runtime/provider.go` 和 `compare/internal/runtime/openai_response.go`。

**设计判断**

- `go-cli-agent` 把 “provider 差异隔离” 当成核心架构要求。
- `compare` 把 “一个足够稳定的 Responses 形状 provider 跑通” 当成更高优先级，因此故意避免过早泛化。

这个选择不是优劣问题，而是“平台化”与“收敛执行面”的取舍。

---

## 4. `go-cli-agent` 更优秀、值得 `compare` 借鉴的设计

### 4.1 三层 facade 边界更清晰

**已验证事实**

- `internal/runtime/facade.go` 把 core、experimental、store-only 三类能力分开。
- `internal/app/app.go` 明确要求默认 usage 只展示 core surface，实验能力放到 `experimental` 子命令下。

**为什么这点优秀**

- 它直接服务于“CLI-only core v1 收敛”的产品纪律。
- 这避免了 queue / delegate / web / tui 反过来污染默认叙事。
- 对后续做 SDK、嵌入式调用、Web service 也更友好，因为 app-facing construct 没有坍缩成一个大 Runner。

**对 `compare` 的借鉴建议**

- 保持当前 `opsx` CLI 极简，但建议内部补一个类似的 facade 分层：
  - `core runtime`
  - `command/longrun facade`
  - `store/logs facade`
- 这样未来即使加新的 operator surface，也不会把 `run.go` 继续做大。

### 4.2 session metadata 设计更完整，provider 选项真正 durable

**已验证事实**

- `SessionMetadata` 里持久化了：
  - `provider`
  - `model`
  - `completion_policy`
  - `parent/root/depth`
  - `agent_name` / `agent_role`
  - `queue_job_id`
  - `isolation`
  - `provider_options`
- `provider_options` 又包含 generation / reasoning / store / send_metadata / retry policy。

**为什么这点优秀**

- 这不是“CLI flag 透传”，而是“运行契约 durable 化”。
- 一旦 session 被恢复、分析、比较或回放，真正采用了什么 provider policy 可以直接从事实源读到。

**对 `compare` 的借鉴建议**

- `compare` 当前 `state.json` 已经保存 provider / model / retry attempts，但还缺少更细的 request-level option durability。
- 如果未来要扩到多 provider 或多 gateway，这套 metadata 策略应优先借鉴。

### 4.3 live steer / interrupt 语义更成熟

**已验证事实**

- `go-cli-agent` 有独立 `steer` 命令、`control/steer.jsonl`、`pending/accepted/deferred/rejected` 状态、`best-effort interrupt`、provider cancel、tool cancel、后台结果并入，见 `internal/runtime/runner.go`、`internal/runtime/engine.go`、`spec/13-live-input-and-steering.md`。

**为什么这点优秀**

- 它把“运行中外部纠偏”当成一等语义，而不是补丁式功能。
- 这非常适合 coding / audit / repo exploration 一类会中途改方向的任务。

**对 `compare` 的借鉴建议**

- `compare` 的 resume 非常强，但 live steer 仍然偏弱。
- 若后续要提升“运行中的可控性”，优先引入：
  - run-scoped control queue
  - queue-first steer
  - interrupt fallback
  - 接纳边界语义

这比直接上复杂交互壳更符合两个项目的 CLI-first 共识。

### 4.4 双层任务系统比单 todo 更适合复杂工程任务

**已验证事实**

- `go-cli-agent` 同时拥有：
  - session todo：`todo_write` / `todo_read`
  - persistent task graph：`task_create` / `task_update` / `task_list` / `task_get`
- 任务图支持 `blocked_by` / `blocks` / cycle check / auto unlock，见 `internal/session/taskboard.go` 与 `spec/12-task-system.md`。

**为什么这点优秀**

- 高频节奏控制与长周期依赖建模被分开了。
- 对“大仓库、多轮实现、恢复后继续推进”的任务，它比单纯 todo 更稳。

**对 `compare` 的借鉴建议**

- `compare` 当前 `todo_set` 很适合 command-style long task，但对多依赖、多阶段长期开发任务表达力不足。
- 若后续确实想支撑“大型项目长期自治开发平台”，建议在保留 `todo_set` 的同时引入 second layer task graph，而不是让 todo 继续膨胀。

### 4.5 compaction 更像“证据保留系统”，不是单纯摘要

**已验证事实**

- `go-cli-agent` 的 compaction 会写 transcript 和 summary artifact。
- summary 里显式保留：
  - `artifact_memory`
  - `high_value_proofs`
  - `project_memory_stack`
  - `todo`
  - `ready_tasks` / `blocked_tasks`
  - `proof_read_budget`
  - `unresolved_issues`
- 见 `internal/runtime/compaction.go`、`internal/runtime/project_memory.go`。

**为什么这点优秀**

- 这说明它把 compaction 当成“长期任务证据压缩层”，而不是简单聊天摘要。
- 对 audit / debugging / long-running implementation 特别有价值。

**对 `compare` 的借鉴建议**

- `compare/internal/context/compaction.go` 目前已经足够简洁有效，但仍偏摘要化。
- 如果未来要做更复杂 repo task，建议补：
  - artifact memory
  - proof memory
  - durable project memory stack
  - unresolved issues bucket

### 4.6 review / audit guard 是非常强的 runtime 增益

**已验证事实**

- `go-cli-agent` 在 `internal/runtime/review_guard.go` 与 `internal/review/report.go` 中实现了：
  - 审计任务自动识别
  - finish 前 report artifact gate
  - findings 结构校验
  - `Severity/Confidence/Evidence/Why it matters` 字段校验
  - cited path:line 是否可读
  - snippet 是否真的落在 cited lines 内

**为什么这点优秀**

- 这不只是“要求写文档”，而是把审计输出质量变成 runtime contract。
- 尤其 snippet-level evidence verification 非常难得，能显著减少“看起来像报告、实际上不可追溯”的产物。

**对 `compare` 的借鉴建议**

- `compare` 的 `security-review` 路线已经有“verified facts / inferred risks / not observed”的 prompt discipline，但仍主要依赖 command contract。
- 若要进一步提高 audit output 可靠性，最值得借鉴的就是 `go-cli-agent` 这套 runtime validator，而不是继续堆 prompt。

### 4.7 session-first 的实验扩展面做得更完整

**已验证事实**

- `go-cli-agent` 已经具备：
  - child session / queue job linkage
  - background notification 回流
  - queue auto worker
  - explicit `experimental web`
  - `doctor` / `probe-provider`
  - store-only web read model

**为什么这点优秀**

- 即使这些能力仍被定义为 experimental，它们已经不是“空壳命令”，而是建立在同一份 session/store 事实源上。

**对 `compare` 的借鉴建议**

- 如果 `compare` 以后要增加 operator observability，不建议直接造一个新数据库或新的运行态。
- 更合适的方式是借鉴 `go-cli-agent`：所有观测面都只读同一份 durable store。

---

## 5. `compare` 更优秀、值得 `go-cli-agent` 借鉴的设计

### 5.1 command contract 是它最强的设计之一

**已验证事实**

- `compare/internal/extensions/extensions.go` 会发现 `.agent/skills`、`.agent/commands`、`.agent/agents`、`.agent/plugins`。
- `compare/internal/runtime/command_runner.go` 会把一个 command 展开成：
  - command body
  - required agent
  - required skills
  - required output files
  - tool restrictions
  - max turns policy

**为什么这点优秀**

- 它把“任务约束”从 prompt 文本提升成了结构化 contract。
- 比起让 runtime 临时猜测“这次是不是 audit / 是否需要产物 / 工具是不是应该限制”，这种 contract 更稳定，也更容易恢复。

**对 `go-cli-agent` 的借鉴建议**

- 当前 `go-cli-agent` 在 skill 上比较成熟，但 command / agent / plugin contract 还不够结构化。
- 最值得借鉴的是“先有 contract，再组织 runtime”，而不是简单搬运 `.agent` 目录格式。

### 5.2 completion gate 更硬、更真实

**已验证事实**

- `compare/internal/runtime/run.go` 的完成判定会检查：
  - todo 是否还有 pending
  - required artifacts 是否存在并真正由本 run 触达/更新
  - sub-agent 是否仍在等待
  - generic artifact-gated run 是否已经建立非空 todo plan
- `compare/internal/runtime/parent_controller.go` 的 `CanComplete()` 是完成前统一 gate。

**为什么这点优秀**

- 这是“长任务不靠模型自己觉得完成”的真正落地。
- completion gate 不只看 assistant 输出，也不只看工具是否停下，而是看 durable state 是否闭环。

**对 `go-cli-agent` 的借鉴建议**

- 当前 `go-cli-agent` 已有 `finish` 工具、review guard、feature list pre-completion check，但这些 gate 仍偏分散。
- 非常值得引入一个统一的 `completion controller`：
  - task/todo completeness
  - required artifacts
  - child/queue unresolved state
  - exact delivery contract

把 finish 守门统一起来。

### 5.3 checkpoint resume 设计明显强于当前库

**已验证事实**

- `compare/internal/session/longrun.go` 定义了 `LongRunCheckpoint`。
- `compare/internal/runtime/parent_controller.go` 会持续写 checkpoint。
- `compare/internal/runtime/resume.go` 可以从 checkpoint 恢复：
  - 校验 provider/model/workspace/trust flags 一致
  - 恢复 todo / artifacts / context
  - 恢复 waiting children
  - 复用 persisted command contract，而不是重新解释当前 workspace 资产

**为什么这点优秀**

- 这是真正的 long-running durability，而不是简单的“继续上次 session”。
- 尤其“resume 使用持久化 command contract 而不是重读当前资产”这一点，非常稳，能避免 workspace 漂移导致恢复语义变化。

**对 `go-cli-agent` 的借鉴建议**

- 当前 `go-cli-agent` 的 `continue` 更偏 session resume，不是 checkpoint resume。
- 对单会话交互这已经够用，但对长任务/多子代理/多产物场景仍不够硬。
- 建议优先借鉴：
  - longrun checkpoint snapshot
  - resume source validation
  - persisted contract replay

### 5.4 parent/sub-agent 控制器设计更凝练

**已验证事实**

- `compare` 通过 `ParentController + delegation.Manager` 形成清晰的 parent-child 控制闭环：
  - `spawn_subagent`
  - `wait_subagents`
  - callback resume
  - `wait-all` / `wait-any`
  - subagent transition 事件
  - parent parked / resumed

**为什么这点优秀**

- 它不是“把 child 跑起来就算完”，而是把 parent 的等待、恢复、汇总、checkpoint 都做成了显式语义。
- 这比“只记录 child session id”更接近真实长任务编排。

**精确边界**

- 这里要注意，`compare` 的 child 更接近 **child run + summary callback + checkpoint orchestration**：
  - `buildChildRunner()` 内部是再起一个 `Runner.Run(...)`
  - parent 侧持久化的是 waiting/resolved child、summary、resume token、checkpoint
- 它并不是 `go-cli-agent` 那种 **child session / queue job / background notification** 为核心的数据模型。
- 因此，这一节的结论应理解为：`compare` 在 **parent-side orchestration contract** 上更强，而不是在 child session durability 这个维度全面胜出。

**对 `go-cli-agent` 的借鉴建议**

- 当前 `go-cli-agent` 的 child session、queue job、background notification 已经是很好的底子。
- 但 parent 侧还缺一个像 `ParentController` 那样的统一协调对象。
- 这会导致：
  - queue/child 回流是有的
  - 但 parent completeness / parked state / callback token 还不够集中

### 5.5 扩展资产信任边界更严谨

**已验证事实**

- `compare/internal/extensions/extensions.go` 对 workspace `.agent` 资产引入了：
  - `--trust-workspace-assets`
  - plugin disable
  - symlink / workspace boundary 校验
  - qualified name / short name ambiguity 处理

**为什么这点优秀**

- 这不是“能发现资产”而已，而是把扩展资产当成潜在不可信输入。
- 对 agent framework 来说，这个边界非常重要，因为 skill / command / plugin 本身就会改变 runtime 行为。

**对 `go-cli-agent` 的借鉴建议**

- 当前 `go-cli-agent` 的 `skills.Scan()` 很简洁，但还不具备 compare 这一层 workspace trust contract。
- 如果后续要增强 workspace-local skills / commands / agents 体系，这个 trust boundary 几乎是必须先补的。

### 5.6 operator trace 更简单直接

**已验证事实**

- `compare` 在正常成功/失败收尾路径都会写：
  - `state.json`
  - `events.jsonl`
  - `run.md`
  - checkpoint
  - artifact linkage
- 并且事件种类很稳定，如 `run.started`、`run.resumed`、`tool.call`、`provider.attempt`、`subagent.*`、`checkpoint.written`、`run.completed`。

**为什么这点优秀**

- 它非常适合 operator/debugger 直接阅读。
- 对长任务诊断来说，一份人类可读 `run.md` 是低成本高价值资产。

**对 `go-cli-agent` 的借鉴建议**

- 当前 `go-cli-agent` 的事件和 session 数据更丰富，但 operator 入口相对分散。
- 可以借鉴 `compare` 增加一份 run/session summary markdown，作为浏览 durable state 的第一入口。

### 5.7 配置面与 builtins 保持了高度收敛

**已验证事实**

- `compare/internal/runtime/config.go` 只暴露少量关键配置：
  - provider/model/base-url/api-key
  - retry
  - provider timeout
  - max turns
  - compaction
  - subagent concurrency
- builtins 也只保留 `read_file`、`write_file`、`exec_shell`，其余 runtime tools 都是任务执行必需能力。

**为什么这点优秀**

- 这让 compare 的 runtime 更像一个可验证的“执行内核”，而不是产品功能集合。

**对 `go-cli-agent` 的借鉴建议**

- `go-cli-agent` 当前功能更全面，但也更容易出现“功能层次越来越厚”的风险。
- 应继续坚持已有 spec 中的原则：核心面必须瘦，实验面显式暴露。

---

## 6. 两边的关键差异，不宜简单照搬

### 6.1 `go-cli-agent` 不应直接照搬 `compare` 的 command-first 叙事

原因：

- 当前库的核心资产是 `session + steer + continue + provider abstraction`。
- 如果直接把主叙事切成 `run/command/resume`，反而会削弱已有的 interactive/session-first 优势。

正确借鉴方式：

- 借 `command contract`
- 不改 `core v1` 的默认命令面

### 6.2 `compare` 不应直接照搬 `go-cli-agent` 的 web/tui 扩展面

原因：

- `compare` 的优势就是 CLI 执行面收敛。
- 如果直接把 Web console、TUI、doctor、provider probe 整体搬进去，很容易打散它现在的 operator-first 简洁性。

正确借鉴方式：

- 先借 durable store 与 facade 分层
- 再按需加观测面，而不是一次性搬全套产品面

### 6.3 `go-cli-agent` 的 review guard 不该无条件套给所有任务

原因：

- 这套机制对 audit/review 任务极强，但并不是所有任务都需要这么重的 validator。

正确借鉴方式：

- 保持当前“任务语义识别 + 精确 gate”的方向
- 避免把 review discipline 变成所有模式的默认束缚

### 6.4 `compare` 的 todo-only 模型不适合直接替换当前 task graph

原因：

- 当前库已经明确要支持更长周期、更复杂依赖的工程任务。
- 单 todo 不足以替代 task graph。

正确借鉴方式：

- 在 generic artifact-gated run 中借 compare 的 todo discipline
- 不牺牲 `go-cli-agent` 已有的双层任务系统

---

## 7. 互相借鉴的优先级建议

### 7.1 `go-cli-agent` 优先借鉴清单

### P1：高收益、低破坏

1. **引入 contract layer**
   - 为 command / batch / audit / longrun 建一个 durable contract
   - 内容至少包括：
     - required artifacts
     - allowed tools
     - activated skills
     - agent role/profile
     - completion gate 条件

2. **统一 completion controller**
   - 把现在分散在 finish / review / feature-list / queue 回流上的完成条件收拢成统一控制点

3. **给长任务引入 checkpoint**
   - 特别是 parent-child / queue / background 相关状态

### P2：中收益、需要更细化设计

4. **增强 workspace-local extension trust boundary**
   - skill / command / agent / plugin 若进入 workspace 维度，必须带 trust gate

5. **补 operator summary artifact**
   - 每个 session 或每次 exec/run 生成稳定 summary，降低排障成本

### P3：在大型项目 profile 下再推进

6. **将 delegation 的 parent 控制显式化**
   - wait-any / wait-all
   - callback resume
   - child summary reinjection

### 7.2 `compare` 优先借鉴清单

### P1：高收益、低破坏

1. **引入 provider adapter 分层**
   - 先别急着扩 provider 数量
   - 先把 provider 选项、metadata、retry durability 的抽象打好

2. **补 live steer**
   - 对复杂任务非常有价值
   - 比直接堆交互壳更值得先做

3. **引入 project memory stack**
   - 把 `spec/plan/progress/validation` 一类 durable files 变成 runtime 一等事实

### P2：中收益

4. **把 audit artifact validator runtime 化**
   - 从 prompt discipline 升级到 output discipline

5. **引入 task graph second layer**
   - 保留 `todo_set`
   - 增加 long-horizon dependency graph

### P3：谨慎推进

6. **增加 facade 分层**
   - 不是为了炫架构
   - 而是为了未来 command/runtime/store/operator view 不互相污染

---

## 8. 最终判断

### 8.1 如果目标是“通用 agent harness + 交互式会话能力”

当前实现下，`go-cli-agent` 在这类目标上优势更明显，优势集中在：

- provider 抽象
- session durability
- live steer
- task graph
- evidence-aware compaction
- runtime review guard

### 8.2 如果目标是“长任务执行纪律 + operator 可控性”

当前实现下，`compare` 在这类目标上优势更明显，优势集中在：

- command contract
- required artifact gate
- todo progress discipline
- sub-agent park/resume
- checkpoint resume
- trusted extension assets
- operator trace

### 8.3 最佳融合方向

最值得追求的不是二选一，而是下面这个组合：

- 以 `go-cli-agent` 的 **session-first runtime / provider architecture / steer / task graph** 为底座
- 吸收 `compare` 的 **command contract / completion gate / checkpoint resume / trusted extension boundary**

如果这条融合路线走通，最终会得到一个更完整的形态：

- 对外仍然保持 CLI-first、core surface 简洁
- 对内同时具备：
  - 强交互性
  - 强长任务纪律
  - 强恢复能力
  - 强可追溯性

这也是两套框架里最值得留下来的“各自真正优秀的部分”。
