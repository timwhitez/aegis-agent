# `go-cli-agent` 与 `compare` Agent 框架对比分析

## 1. 本轮结论

### 1.1 一句话结论

`go-cli-agent` 当前更适合作为 **session-first、provider-rich、交互可恢复** 的通用 CLI agent harness。

`compare` 当前更适合作为 **run-first、contract-driven、长任务交付纪律强** 的 CLI agent framework。

这不是“谁完全胜出”的关系。两者解决的是同一大类问题里的不同重心：

- `go-cli-agent` 的优势在于把 provider 差异、session 持久化、`steer`、`continue`、task graph、review guard、queue / child session / web observability 都放进同一套本地 session 事实源。
- `compare` 的优势在于把 command contract、required artifact、todo progress、parent/sub-agent checkpoint、callback resume、operator trace 做成一次 run 的硬约束。

当前项目最应该吸收的是 `compare` 的 **contract layer、completion controller、long-run checkpoint、workspace extension trust gate、operator summary**，而不是照搬它的 command-first 产品叙事。

### 1.2 最重要的修正文档点

上一版文档的大方向基本正确，但粒度还不够硬。本轮需要更明确地区分：

- `go-cli-agent` 的 child 是 **child session / queue job / background notification** 模型。
- `compare` 的 child 是 **child run + parent callback + checkpoint orchestration** 模型。
- `go-cli-agent` 的 `continue` 是 session resume；`compare` 的 `resume` 是 checkpoint resume，并且会验证 provider / model / workspace / trust flags。
- `go-cli-agent` 的 review/report guard 是 runtime quality gate；`compare` 的 required-artifact gate 是 run contract gate。两者都能阻断“模型自认为完成”，但阻断依据不同。
- `compare` 有更强的 command contract 和 parent-side orchestration；`go-cli-agent` 有更完整的 provider adapter、live steering、task graph 和 session store。

### 1.3 当前快照

本轮分析基于当前工作区真实代码，而不是 README 级别判断：

- 主项目：`go-cli-agent/`
- 对照项目：`compare/`
- 根仓库将 `compare` 记录为 gitlink：`160000 831c2ff7cd02ba8d2cf2a33ef4bcdd41f26c4f2a compare`
- `compare` 嵌套仓库当前 HEAD：`831c2ff`

说明：本文只比较当前可见实现。若后续 `compare/` 嵌套仓库移动 HEAD，本结论需要重新核对。

---

## 2. 分析范围与方法

### 2.1 先读当前项目 spec

本轮按仓库要求先阅读以下 spec，再进入代码对比：

- `spec/00-product.md`
- `spec/01-runtime-architecture.md`
- `spec/03-provider-contracts.md`
- `spec/09-phase-plan.md`
- `spec/11-spec-audit-and-traceability.md`
- `spec/12-task-system.md`
- `spec/13-live-input-and-steering.md`

这些 spec 给当前项目划定了几个硬边界：

- 默认主路径是 `init/run/exec/steer/continue/sessions/tasks/probe-provider/doctor`。
- `delegate` / `children` / `queue` / `tui` / `web` 是显式扩展或 experimental surface。
- core runtime、sdk facade、cli adapter 必须分离。
- provider 差异必须留在 adapter 层。
- session / state / messages / events 必须是事实源。
- compaction 只能改变 provider context view，不能覆盖原始日志。
- 当前默认产品叙事仍是 CLI-only harness，不是 Web-first 或 TUI-first。

### 2.2 核对的主项目代码

`go-cli-agent` 重点核对了：

- `internal/app/app.go`
- `internal/runtime/facade.go`
- `internal/runtime/runner.go`
- `internal/runtime/engine.go`
- `internal/runtime/delegation.go`
- `internal/runtime/compaction.go`
- `internal/runtime/prompt.go`
- `internal/runtime/review_guard.go`
- `internal/provider/types.go`
- `internal/provider/http.go`
- `internal/provider/openai.go`
- `internal/provider/anthropic.go`
- `internal/provider/google.go`
- `internal/session/types.go`
- `internal/session/store.go`
- `internal/session/taskboard.go`
- `internal/tools/path.go`
- `internal/tools/registry.go`
- `internal/skills/catalog.go`
- `internal/webconsole/service.go`

### 2.3 核对的 `compare` 代码

`compare` 重点核对了：

- `compare/cmd/opsx/main.go`
- `compare/internal/runtime/run.go`
- `compare/internal/runtime/command_runner.go`
- `compare/internal/runtime/resume.go`
- `compare/internal/runtime/parent_controller.go`
- `compare/internal/runtime/runtime_tools.go`
- `compare/internal/runtime/provider.go`
- `compare/internal/runtime/openai_response.go`
- `compare/internal/runtime/config.go`
- `compare/internal/delegation/manager.go`
- `compare/internal/delegation/delegation.go`
- `compare/internal/session/schema.go`
- `compare/internal/session/longrun.go`
- `compare/internal/extensions/extensions.go`
- `compare/internal/tools/workspace.go`
- `compare/internal/tools/builtins.go`
- `compare/internal/tools/shell_sandbox_linux.go`
- `compare/internal/context/compaction.go`
- `compare/internal/logs/summary.go`
- `compare/docs/acceptance-matrix.md`
- `compare/docs/run-artifact-layout.md`
- `compare/docs/quickstart.md`

### 2.4 术语约定

本文使用三种结论标签：

- **已验证事实**：能从当前代码结构或测试中直接看到。
- **设计判断**：基于代码形态得出的架构取向判断。
- **借鉴建议**：在当前项目 phase 边界内建议吸收的机制。

---

## 3. 顶层对比矩阵

| 维度 | `go-cli-agent` | `compare` | 判断 |
| --- | --- | --- | --- |
| durable unit | session | run | 当前项目更适合交互恢复；`compare` 更适合批任务交付审计 |
| 默认 CLI surface | `init/run/exec/steer/continue/sessions/tasks/probe-provider/doctor` | `opsx run/command/resume/smoke` | 当前项目主路径更通用；`compare` 更收敛 |
| provider | OpenAI / Anthropic / Google / openai-compatible | fake / openai-response | 当前项目明显更强 |
| provider option durability | session metadata 持久化 generation / retry / timeout | state 记录 provider/model/attempts，但 option 面更窄 | 当前项目更适合多 provider 平台化 |
| live steer | 一等能力，`control/steer.jsonl` | 未看到同级 live steer | 当前项目明显更强 |
| resume | session `continue` | long-run checkpoint `resume` | 两者强项不同；`compare` checkpoint 更硬 |
| completion gate | `finish` + review/target/report/taskboard/pre-completion guards | `ParentController.CanComplete()` + artifact/todo/child gate | `compare` 更统一；当前项目更丰富但分散 |
| task model | todo + persistent task graph | todo | 当前项目表达力更强；`compare` 执行纪律更强 |
| sub-agent | child session / queue job / background notification | child run / detached manager / callback / checkpoint | 不可混同；`compare` parent-side orchestration 更强 |
| extension assets | skills + command tools | `.agent/skills/commands/agents/plugins` + trust gate | `compare` 的 workspace extension contract 更完整 |
| shell/tool boundary | workspace path + symlink safety + env allowlist + timeout | workspace boundary + symlink safety + `.git` write deny + Linux bwrap sandbox | 各有优势，`compare` shell sandbox 更硬 |
| operator trace | session events/messages/state/tasks/artifacts，Web 读模型丰富 | `run.md` + `events.jsonl` + `state.json` + checkpoints 很直接 | `compare` 人读摘要更直接 |
| product risk | 功能面多，completion rules 容易分散 | provider/steer/task graph 相对薄 | 当前项目要防止扩展面污染 core；`compare` 要防止 contract-first 牺牲交互性 |

---

## 4. Durable Truth：session-first vs run-first

### 4.1 `go-cli-agent` 的 durable unit 是 session

**已验证事实**

`go-cli-agent` 的 `session.Store.Create()` 会创建：

- `session.json`
- `state.json`
- `messages.jsonl`
- `events.jsonl`
- `todo.json`
- `control/steer.jsonl`
- `control/background.jsonl`
- `tasks/`
- `artifacts/`
- `artifacts/compactions/`
- `artifacts/transcripts/`

`SessionMetadata` 持久化：

- provider / model
- mode / completion policy
- parent / root / depth
- agent name / role
- queue job id
- isolation
- provider options

`State` 持久化：

- status / phase / turn
- pause / awaiting input / failure information
- pending steer count
- loaded skills
- provider auto-resume count
- last compaction watermark

**设计判断**

当前项目的事实源围绕“一个 agent session 如何长期演进”展开，因此天然适配：

- 交互式 `run`
- autonomous `exec`
- `continue`
- live `steer`
- child session
- queue notification
- Web console read model

这套模型比 `compare` 更适合“用户中途纠偏、暂停、恢复、继续投入上下文”的使用方式。

### 4.2 `compare` 的 durable unit 是 run

**已验证事实**

`compare` 的 `Runner.Run()` 为每次执行创建 `.opsx/runs/<run-id>/`，核心包括：

- `state.json`
- `events.jsonl`
- `run.md`
- `checkpoints/000-startup.json`
- `checkpoints/longrun-latest.json`
- `artifacts/`

`RunState` 持久化：

- task
- command name
- instructions
- provider / model
- workspace root / artifacts root
- trust-workspace-assets
- agent / active skills / allowed tools
- required artifacts
- max turns
- loaded extensions
- continuation context
- provider attempts
- resume count

`LongRunCheckpoint` 持久化：

- parent status
- todo
- required artifacts
- children
- continuation context
- wait mode / waiting children / resolved children / child summaries / callback sequence

**设计判断**

`compare` 的事实源围绕“一次长任务 run 是否按 contract 完成”展开，因此天然适配：

- batch command
- required artifact
- checkpoint resume
- parent/sub-agent park and resume
- operator 事后排障

它不追求像 `go-cli-agent` 那样把 session 变成长期交互对象，而是把一次 run 的执行纪律做硬。

### 4.3 不能混同的关键点

`go-cli-agent` 的 child 是 child session。`compare` 的 child 是 child run。

更精确地说：

- `go-cli-agent`：parent session 通过 `agent_spawn` / `experimental delegate` 产生 child session 或 queue job；child 的结果通过 background notification 回流到 parent session 的 `control/background.jsonl`。
- `compare`：parent run 通过 `spawn_subagent` 产生 detached child run；`ParentController` 通过 callback、wait mode、resume token、checkpoint 管理 parent 是否 parked/resumed。

因此，`compare` 在 parent-side orchestration 上更强，不等于它在 session durability 上强于 `go-cli-agent`。

---

## 5. CLI Surface 与产品边界

### 5.1 `go-cli-agent` 的 surface 明确分层

**已验证事实**

`internal/app/app.go` 的默认 usage 只展示：

- `init`
- `run`
- `exec`
- `continue`
- `steer`
- `sessions`
- `tasks`
- `probe-provider`
- `doctor`

`delegate` / `children` / `queue` / `tui` / `web` 被放到 `experimental` 下。

`internal/runtime/facade.go` 进一步拆出：

- `CoreRunner`
- `ExperimentalRunner`
- `StoreView`

**设计判断**

这是当前项目非常重要的产品纪律：core v1 不被大型项目 profile、queue、web、tui 反向污染。

这也符合 spec 中“CLI-only harness，Phase 11+ 显式扩展”的边界。

### 5.2 `compare` 的 surface 更 operator-first

**已验证事实**

`compare/cmd/opsx/main.go` 暴露四个主命令：

- `run`
- `command`
- `resume`
- `smoke`

其中：

- `run` 支持 `--task`、`--require-artifact`、`--trust-workspace-assets`。
- `command` 通过 `.agent` command contract 展开 agent / skills / tools / required outputs。
- `resume` 从 run id 或 checkpoint 恢复。
- `smoke` 跑内置场景。

**设计判断**

`compare` 的 CLI 非常像“批处理 agent kernel 的操作台”。它不试图提供完整 session 浏览和实时 steer，而是优先让 operator 发起、恢复、验证一次 run。

### 5.3 当前项目不应改成 command-first

**借鉴建议**

`go-cli-agent` 应该借鉴 `compare` 的 command contract，而不是把主命令面改成 `run/command/resume/smoke`。

当前项目已经有更完整的 session 主路径。若把产品叙事切成 command-first，会削弱：

- live steer
- interactive `run`
- `continue`
- `sessions/tasks`
- provider probe / doctor
- session store read model

更合适的融合方式是：

- 在当前 `run/exec` 之上增加可选 durable contract。
- 将 batch command 作为 profile 或 explicit mode。
- 不改变 core v1 默认入口。

---

## 6. Provider 与协议边界

### 6.1 `go-cli-agent` 的 provider architecture 明显更完整

**已验证事实**

当前项目的 `ProviderAdapter` 接口是：

- `Name()`
- `RunTurn(ctx, TurnRequest, EmitFunc) -> TurnResult`

`TurnRequest` 包含：

- model
- system prompt
- messages
- tools
- metadata
- temperature
- top_p
- max_output_tokens
- reasoning_effort
- text_verbosity
- thinking_budget
- include_thoughts
- store

当前实现支持：

- OpenAI Responses
- Anthropic Messages
- Google Gemini `generateContent`
- openai-compatible Responses shape

`provider_options` 被持久化到 session metadata，并且 `provider.request.prepared` 事件会带上 option / timeout / retry policy 细节。

**设计判断**

这是 `go-cli-agent` 的核心优势之一：它不是只跑通一个 gateway，而是在 runtime 层把 provider 差异明确收束到 adapter。

这使它更适合：

- 多 provider 环境
- OpenAI-compatible gateway
- generation / reasoning option 实验
- provider drift 诊断
- `doctor` / `probe-provider`

### 6.2 `compare` 的 provider 层更窄，但 retry trace 更像 batch run ledger

**已验证事实**

`compare` 当前 provider 支持：

- `fake`
- `openai-response`

`openAIResponsesProvider` 直接使用 `http.Client{Timeout: cfg.ProviderTimeout}` 调 `/responses`，解析 message 和 function_call。

`Runner.executeWithRetry()` 会记录每次 provider attempt：

- turn
- attempt
- kind
- outcome
- retryable
- status code
- backoff
- response committed
- message

并写入 `state.ProviderAttempts` 与 `provider.attempt` event。

**设计判断**

`compare` 不适合被描述成 provider-rich。它强的是“每次 provider attempt 都进入 run ledger”，尤其 `ResponseCommitted` 不重试的语义对 batch execution 很实用。

### 6.3 当前项目的 provider 优化方向

**借鉴建议**

`go-cli-agent` 已经有更完整 provider adapter 和 timeout/retry split。它可以借鉴 `compare` 的地方不是 provider shape，而是 attempt ledger：

- 在 `state.json` 或 session artifact 中增加结构化 provider attempt history。
- 将 `provider.retry`、`provider.auto_resume`、最终 failure 统一汇总成人可读诊断。
- 保持 retry / timeout policy 来自 session metadata，避免 resume 时配置漂移。

### 6.4 `compare` 的 provider 优化方向

**借鉴建议**

如果 `compare` 后续要平台化，应优先借鉴当前项目：

- provider adapter interface
- generation option durability
- OpenAI / Anthropic / Google replay 差异隔离
- request timeout 与 stream idle timeout 分离
- provider metadata 可追踪

不要先扩一堆 provider 名称再回头补 contract。应先把 option / replay / retry / metadata 抽象打稳。

---

## 7. Agent Loop 与 Completion

### 7.1 `go-cli-agent` 的 loop 更贴近交互式 agent harness

**已验证事实**

`Engine.Run()` 的基本循环是：

- load messages / todo / tasks / project memory
- build system prompt
- maybe compact provider view
- provider call
- append assistant message
- execute tool calls
- append tool results
- 根据 mode 决定 awaiting input / reminder / failed / completed

在 `exec` / `init` 模式下，如果 provider 自然停止但没有调用 `finish`，runtime 会注入 “call finish explicitly” reminder；连续没有 `finish` 后会失败为 `incomplete_no_finish`。

在 `run` 模式下，自然停止进入 `awaiting_input`。

**设计判断**

这符合当前项目“模型是 agent，runtime 提供 loop 和 action space”的原则。它适合人在旁边不断 steering 的任务。

### 7.2 `compare` 的 completion gate 更统一

**已验证事实**

`compare` 的 provider 没有 tool call 后，会调用 `tryCompleteRun()`。

完成前会检查：

- `syncRequiredArtifacts`
- `ParentController.CanComplete()`
- `requireGenericArtifactTodoPlan`
- artifact links
- run summary

`ParentController.CanComplete()` 会阻断：

- pending todo
- missing required artifact
- waiting child callbacks
- active child runs

`compare` 还会：

- 记录 required artifact baseline，避免把 run 开始前已有文件当成完成。
- 对 required artifact run 注入 early artifact discipline。
- 对 artifact stagnation / todo stagnation 注入 warning。

**设计判断**

`compare` 的 completion controller 更像单一判定中心。它比当前项目分散在 `finish`、review guard、target/report consistency、long-run taskboard guard、pre-completion feature check 里的完成约束更凝练。

### 7.3 当前项目应该吸收 completion controller

**借鉴建议**

`go-cli-agent` 现在已有很多必要 gate：

- explicit `finish`
- review artifact guard
- exact artifact path / template / literal guard
- target consistency guard
- report consistency guard
- long-run taskboard guard
- steer completion guard
- pre-completion feature check

问题不是“没有 guard”，而是 guard 分散在 prompt/tool guard 逻辑里，缺少一个 durable completion contract 和统一判定对象。

建议新增一个 `CompletionController` 或等价层，聚合：

- expected artifacts
- requested exact target
- supporting docs freshness
- task/todo completeness
- child/queue unresolved state
- review/report validators
- feature list completeness
- completion policy

这样可以保留当前项目的交互能力，同时获得 `compare` 那种“完成不是模型自评”的硬边界。

---

## 8. Live Input、Interrupt 与 Resume

### 8.1 `go-cli-agent` 的 live steer 是强项

**已验证事实**

当前项目支持：

- `go-cli-agent steer <session-id> --message ...`
- `--interrupt`
- `control/steer.jsonl`
- steer status：`pending/accepted/deferred/rejected`
- `session.steer.requested`
- `session.steer.queued`
- `session.steer.accepted`
- `session.steer.deferred`

`Runner.watchSteer()` 监控 control queue；`Engine.drainSteer()` 在安全边界把 steer 写成真实 user message。

provider 或工具执行时，interrupt 可以走 best-effort cancel；工具被打断时会写回中断 tool result，避免 dangling tool call。

**设计判断**

这是 `compare` 当前没有的核心能力。对真实 coding / audit / debugging 任务非常关键，因为用户经常中途修正目标。

### 8.2 `compare` 的 checkpoint resume 更硬

**已验证事实**

`compare.Resume()` 会：

- 从 run id 或 checkpoint path 加载 `longrun-latest.json`
- 读取源 run 的 `state.json`
- 拒绝 resume 已成功 run
- 校验 provider / model / workspace / trust-workspace-assets 是否与源 run 一致
- 根据源 run state 重新构造 Request
- 对 command run 注入 persisted command resume instruction
- 如果 parent 正在等待 sub-agent，则重新 spawn checkpoint children，等待 callback，再把 child summaries 注入恢复指令

**设计判断**

这是 `compare` 最值得借鉴的部分之一：resume 不是“再给模型一点上下文继续聊”，而是从 durable checkpoint 恢复一次 run 的 contract 和 parent state。

### 8.3 两者应该融合，而不是替代

**借鉴建议**

当前项目不应拿 `compare` 的 checkpoint resume 替代 `continue`。更合理的是分层：

- session `continue`：保留当前交互恢复语义。
- long-run checkpoint：用于 large-project profile、child/queue/background、多 artifact 任务。
- command/batch contract resume：用于明确 command 或 batch mode。

建议先在当前项目的大型任务 profile 下引入：

- `longrun-latest.json` 或等价 checkpoint artifact
- persisted contract snapshot
- child/queue unresolved snapshot
- resume source validation
- resume summary instruction

---

## 9. Task / Todo 模型

### 9.1 `go-cli-agent` 的双层任务系统表达力更强

**已验证事实**

当前项目同时有：

- session todo：`todo_write` / `todo_read`
- persistent task graph：`task_create` / `task_update` / `task_list` / `task_get`

task graph 支持：

- `blocked_by`
- `blocks`
- 双向依赖同步
- unknown reference 校验
- cycle check
- completed 后自动解锁 dependents
- ready / blocked / completed 分组

**设计判断**

这比 `compare` 的 todo 更适合长期工程任务。尤其当任务横跨多个 session、多个 child、多个依赖时，单 todo 很快不够表达。

### 9.2 `compare` 的 todo 与 completion gate 绑定更紧

**已验证事实**

`compare` 的 `todo_set` 会写入 `ParentController`，并持续进入 checkpoint。

`ParentController.CanComplete()` 会阻断 pending todo。

`validateTodoTransitionLocked()` 还会防止 artifact-linked todo 在 required artifact 未 present 时被标成 done。

**设计判断**

`compare` 的 todo 表达力不如 task graph，但它和 completion gate 绑定更硬。当前项目虽然有 task graph，但模型仍可能在长 run 中不写 todo/task，上一轮才补了 long-run taskboard guard。

### 9.3 当前项目应保留 task graph，同时加强 taskboard 与 completion 的绑定

**借鉴建议**

不建议用 `compare` 的 todo-only 模型替换当前 task graph。

更合理的是：

- 保留 `todo + task graph`。
- 让 completion controller 读取 todo/task 状态。
- 对大型任务要求至少一个 durable task/todo handoff。
- 对 required artifacts 建立 task/artifact linkage。
- 对 child/queue 任务建立 task owner 或 task label。

---

## 10. Multi-Agent 与 Parent Orchestration

### 10.1 `go-cli-agent` 的 child session / queue 底座更完整

**已验证事实**

当前项目支持：

- `agent_spawn`
- `agent_status`
- `agent_list`
- `experimental delegate`
- `experimental children`
- `experimental queue`
- child session metadata：parent/root/depth/agent/role/queue_job_id/isolation
- background queue job
- background notification 回流到 parent session
- isolation workdir
- visible output collection / sync

**设计判断**

这套设计更接近“session graph”。它适合在同一 agent harness 内长期观察 parent/child/queue 的关系。

### 10.2 `compare` 的 parent-side orchestration 更凝练

**已验证事实**

`compare` 的 `ParentController` 管理：

- todo
- required artifacts
- waiting children
- resolved children
- child summaries
- wait mode：`wait-all` / `wait-any`
- parent status：`running` / `waiting_on_subagents`
- resume token
- checkpoint
- callback consumer

`delegation.Manager` 管理：

- child queue
- concurrency
- timeout
- max retries
- terminal status
- callback event
- observer transition

`runtime_tools.go` 暴露：

- `spawn_subagent`
- `wait_subagents`
- `inspect_subagent`
- `cancel_subagent`

**设计判断**

`compare` 把 parent 等待、child callback、checkpoint、完成阻断放在一个清晰对象里。当前项目有 child/queue 的数据模型，但 parent completeness 和 parked/resumed 状态还不够集中。

### 10.3 当前项目应借鉴 ParentController，不应照搬 child run 模型

**借鉴建议**

建议在 `go-cli-agent` 里引入 parent coordination layer：

- parent session 的 unresolved child sessions
- unresolved queue jobs
- wait-any / wait-all
- child result summary reinjection
- parent parked / resumed event
- checkpoint snapshot
- completion gate 读取 child/queue 状态

但不要把 child session 替换成 `compare` 的 child run。当前项目的 session store 已经是更通用的事实源，应在它上面补 parent controller。

---

## 11. Extension / Skill / Plugin 体系

### 11.1 `go-cli-agent` 当前 skill 机制简洁，但 contract 面较薄

**已验证事实**

当前项目的 `skills.Scan()` 扫描 `SKILL.md`，提供：

- skill summary
- lazy load body
- skill-defined command tools

`ToolRegistry` 会注册 builtins，并把 skill command tools 变成 provider tools；reserved names 会阻止 skill tool 覆盖核心工具。

**设计判断**

这是一个轻量 skill catalog，适合当前 core v1。但它没有 `compare` 那种 `.agent/commands`、`.agent/agents`、`.agent/plugins` 的结构化 contract。

### 11.2 `compare` 的 workspace extension contract 更完整

**已验证事实**

`compare/internal/extensions/extensions.go` 支持：

- `.agent/skills`
- `.agent/commands`
- `.agent/agents`
- `.agent/plugins`
- plugin disable
- `--trust-workspace-assets`
- source path
- qualified name
- short name ambiguity resolution
- symlink / workspace escape 检查

command front matter 可声明：

- `required_skills`
- `required_agent`
- `required_output_files`
- `tool_restrictions`

agent front matter 可声明：

- model
- tool policy
- max turns

**设计判断**

这套机制是 `compare` 的核心优势之一：任务约束从 prompt 进入结构化 contract。

### 11.3 当前项目应先借 contract，不应盲目复制 `.agent` 目录

**借鉴建议**

当前项目可以引入一个 generic contract schema，而不是直接把 `.agent` 作为唯一格式：

- command name
- required artifacts
- required skills
- agent role/profile
- allowed tools
- max turns
- completion gates
- trust source
- resolved source paths

如果未来支持 workspace-local `.agent`，必须同时引入：

- explicit trust gate
- symlink escape validation
- plugin disable
- qualified names
- ambiguity errors

否则 `.agent` 资产会成为改变 runtime 行为的隐式攻击面。

---

## 12. Tool 与安全边界

### 12.1 两边都重视 workspace boundary

**已验证事实**

`go-cli-agent` 的 `ResolveWorkspacePath()`：

- abs workdir
- eval symlink base
- resolve existing parent
- 检查 target 是否仍在 base 内

`compare` 的 `Workspace.Resolve()`：

- abs workspace root
- clean path
- pathWithinRoot
- validate symlink boundary
- write 时拒绝 `.git`

**设计判断**

两边都没有只靠 `Clean` / `Rel` 做路径安全，均处理了 symlink escape。

### 12.2 shell boundary 各有优势

**已验证事实**

`go-cli-agent`：

- shell 工具使用 `CommandContext`
- runtime command timeout 会 cap 模型请求的 timeout
- 输出截断
- shell env allowlist 默认 `PATH/HOME/LANG/TERM`
- workspace 目录可被限制在 resolved workdir 下

`compare`：

- `exec_shell` 默认 5000ms timeout
- 输出 limited buffer
- Linux 下通过 `bwrap` 执行，`--unshare-all`，只 bind workspace 到 `/workspace`
- 非 Linux 直接拒绝 `exec_shell`
- write_file 拒绝 `.git`

**设计判断**

`compare` 的 shell sandbox 更硬，但依赖 `bwrap` 和 Linux。`go-cli-agent` 的 shell 更便携，且 env allowlist 更干净。

**借鉴建议**

当前项目不应默认强依赖 `bwrap`，否则会破坏 WSL/macOS/通用 CLI 可用性。更合适的是：

- 保持当前 portable shell 作为默认。
- 在 isolation profile 下增加可选 `bwrap` / stronger sandbox。
- 将 `.git` write-deny 作为可配置 guard 或 protected path policy。
- 继续保持 env allowlist 和 timeout cap。

---

## 13. Compaction 与长期上下文

### 13.1 `go-cli-agent` 的 compaction 更偏证据保留

**已验证事实**

当前项目 compaction 会写 transcript artifact 和 summary artifact。

summary 中包含：

- completed items
- artifact memory
- current status
- in-progress todo/task
- high value proofs
- feature list
- key paths
- next step guidance
- proof read budget
- project memory stack
- todo
- ready tasks
- blocked tasks
- unresolved issues
- recent failure/pause
- transcript path

还引入 hysteresis，避免 compaction 过密时重复写新 artifact。

**设计判断**

这是面向审计/长任务的证据压缩系统，不只是聊天摘要。

### 13.2 `compare` 的 compaction 更偏 checkpoint continuation

**已验证事实**

`compare` 的 `maybeCompactConversation()` 会在 char limit 后：

- 保留 recent provider items
- 总结旧 conversation
- 带入 todo snapshot
- 带入 child summaries
- 写入 `contextstate.Continuation`
- 通过 `ParentController.SetContext()` 进入 checkpoint

**设计判断**

`compare` 的 compaction 与 checkpoint 结合更紧，但 summary 内容没有当前项目的 proof/artifact memory 细。

### 13.3 融合方向

**借鉴建议**

当前项目应保持现有证据型 compaction，同时吸收 `compare` 的 checkpoint coupling：

- compaction summary artifact 继续保留。
- long-run checkpoint 引用 latest compaction summary。
- parent/child/queue state 进入 checkpoint。
- resume prompt 从 checkpoint 和 compaction artifact 共同构造。

---

## 14. Report / Audit / Artifact Quality Gate

### 14.1 `go-cli-agent` 的 review/report guard 更强

**已验证事实**

当前项目有多个 artifact quality gate：

- review artifact guard
- exact artifact path guard
- exact template guard
- exact literal guard
- target consistency guard
- report consistency guard
- long-run taskboard guard
- audit proof follow-up guard
- retrieval tail guard

其中 review artifact validator 会检查：

- findings section
- per-finding Severity / Confidence / Evidence / Why it matters
- concrete path:line evidence
- snippet or identifier 是否真的出现在 cited lines
- unresolved / remaining risks section

target/report consistency guard 会阻断：

- 用户最新目标已经变更但最终报告仍写旧目标。
- final report 与 `reports/progress.md` / `reports/validation.md` 结论冲突。
- supporting docs 晚于 final report 更新但 final report 未刷新。

说明：当前项目已按可信运行环境去掉报告和 prompt 脱敏；这些 guard 只做一致性和质量控制，不改写报告内容。

**设计判断**

这是当前项目在 audit/review 任务上的强项。它不只是 prompt discipline，而是 runtime contract。

### 14.2 `compare` 的 artifact gate 更通用

**已验证事实**

`compare` 的 required artifact gate 会：

- 记录 artifact baseline。
- 要求 artifact 在本 run 中 touched 或相对 baseline changed。
- required artifact 缺失时阻断 completion。
- required artifact run 没有 todo plan 时阻断 generic completion。
- 对 artifact stagnation 发 warning。
- 写 artifact sidecar link。

**设计判断**

`compare` 不验证 audit report 的结构质量，但它更通用地保证“承诺的交付文件确实在本 run 中生成或更新”。

### 14.3 当前项目应该把两类 gate 合并

**借鉴建议**

`go-cli-agent` 应保留现有 review/report guard，同时引入 `compare` 的 generic artifact gate：

- required artifact baseline
- touched-by-session / changed-from-baseline
- required artifact sidecar metadata
- generic artifact task/todo plan requirement
- completion controller 统一读这些信息

这样可以同时覆盖：

- report 内容质量
- artifact 是否真实交付
- artifact 是否陈旧
- artifact 是否与 supporting docs 一致

---

## 15. Operator Trace 与可观测性

### 15.1 `go-cli-agent` 的事实更丰富，但入口更分散

**已验证事实**

当前项目有：

- session metadata
- state
- messages
- events
- todo
- tasks
- artifacts
- compactions
- transcripts
- queue jobs
- background notifications
- Web console local API / read model

**设计判断**

数据非常丰富，但 operator 如果只想快速判断“一次任务到底做了什么、为什么失败、报告在哪里”，目前需要跨多个文件看。

### 15.2 `compare` 的 `run.md` 是低成本高价值资产

**已验证事实**

`compare/internal/logs/summary.go` 会写 `run.md`，包含：

- run id
- status
- task
- resume source
- command
- agent
- skills
- trust marker
- loaded extensions
- provider / model
- provider attempts
- max turns / completed turns
- resume count
- state file
- events log
- final output / failure

**设计判断**

这份 `run.md` 很简单，但正因为简单，operator 可读性很强。

### 15.3 当前项目应补 session summary markdown

**借鉴建议**

建议在 `go-cli-agent` 中新增类似：

- `.go-cli-agent/sessions/<id>/session.md`
- 或 `artifacts/session-summary.md`

内容最少包括：

- session id / status / mode
- provider / model / provider options summary
- workdir / isolation
- parent / child / queue relation
- turn count / tool count / compaction count
- latest task/todo state
- artifact list
- last failure / awaiting input / pause reason
- important event links

这不会改变 core runtime，只是给 operator 一个稳定入口。

---

## 16. 两边各自最强能力

### 16.1 `go-cli-agent` 最强的能力

当前实现下，`go-cli-agent` 最强的是：

- 多 provider adapter 与协议 replay 分层。
- provider generation / reasoning / retry / timeout policy durable 化。
- session store 作为统一事实源。
- `run` / `exec` / `continue` / `steer` 交互恢复语义。
- todo + persistent task graph。
- evidence-aware compaction。
- review/report/target consistency guard。
- child session / queue job / background notification / isolation 的 session graph 底座。
- core / experimental / store facade 分离。
- Web console 复用本地事实源，而不是另建权威状态。

### 16.2 `compare` 最强的能力

当前实现下，`compare` 最强的是：

- command contract：required agent / skills / artifacts / tool restrictions。
- required artifact gate：baseline + touched/changed + sidecar link。
- centralized `ParentController.CanComplete()`。
- long-run checkpoint：todo / artifacts / child / context / resume state。
- checkpoint resume：provider/model/workspace/trust 校验 + persisted command contract。
- parent/sub-agent callback orchestration：wait-all / wait-any / park / resume。
- workspace extension trust boundary：`--trust-workspace-assets`、plugin disable、qualified names、symlink checks。
- operator-readable `run.md`。
- Linux shell sandbox via `bwrap`。

---

## 17. 不应该直接照搬的部分

### 17.1 `go-cli-agent` 不应照搬 command-first 产品叙事

当前项目的核心是 session-first harness。`compare` 的 `command` 很强，但它应进入 current architecture as contract/profile，而不是替换默认主路径。

### 17.2 `go-cli-agent` 不应把 review guard 泛化成所有任务的重约束

review guard 对 audit/review 强，但对普通 coding、整理、问答任务会变成负担。当前“任务语义识别 + 精确 artifact path”的方向应该保留。

### 17.3 `go-cli-agent` 不应把 child session 改成 child run

child session 是当前项目 session store 的自然延伸。应该补 parent controller，而不是换 durable model。

### 17.4 `compare` 不应直接复制 Web/TUI 扩展面

`compare` 的优势是极简 operator-first。直接引入 Web/TUI 会分散它的核心优势。若需要观测面，应先做 store facade 和 run summary API。

### 17.5 `compare` 不应只靠 prompt 增强 audit quality

如果 `compare` 要做审计类任务，应借鉴当前项目的 runtime validator，而不是继续堆 prompt discipline。

---

## 18. 对 `go-cli-agent` 的优先级建议

### P0：当前项目最该补的框架级能力

1. **Durable Contract Layer**

   建立一个可持久化 contract，供 `run/exec/command-like profile/review/longrun` 共用。

   最少字段：

   - required artifacts
   - artifact baseline
   - allowed tools
   - required skills
   - agent role/profile
   - completion gates
   - exact target anchors
   - supporting docs freshness requirements
   - trust source

2. **Completion Controller**

   把分散的 finish / review / target / report / taskboard / feature / child / queue gate 收敛成统一判定层。

   要求：

   - 判定结果写 events。
   - 判定原因写入 tool result 或 state。
   - 支持 yolo 下仍保留用户显式 contract 与安全边界。

3. **Long-Run Checkpoint**

   在大型项目 profile 下保存：

   - latest contract
   - todo/task summary
   - required artifact status
   - child session / queue job unresolved state
   - latest compaction artifact
   - parent wait state
   - resume hints

### P1：高收益、低破坏

4. **Session Summary Markdown**

   为每个 session 生成稳定 `session.md`，类似 `compare` 的 `run.md`。

5. **Workspace Extension Trust Gate**

   如果继续扩展 workspace-local skills / commands / agents / plugins，需要引入：

   - explicit trust
   - plugin disable
   - symlink escape validation
   - qualified name
   - short name ambiguity error

6. **Provider Attempt Ledger**

   在 session durable state/artifact 中沉淀 provider attempts，减少只靠 events/jsonl 检索的诊断成本。

### P2：大型项目 profile 下推进

7. **Parent Coordination Layer**

   在 child session / queue job 之上补：

   - wait-all / wait-any
   - parent parked / resumed
   - callback token
   - child summary reinjection
   - completion gate 读取 unresolved child/queue

8. **Optional Strong Shell Sandbox**

   在 Linux + explicit isolation profile 下支持 `bwrap`，但不要设为默认硬依赖。

---

## 19. 对 `compare` 的优先级建议

### P0：若 `compare` 想做通用 agent harness

1. **Provider Adapter 分层**

   先把 provider interface 扩成真正 adapter，而不是继续堆 `openai-response` 特例。

2. **Live Steer / Control Queue**

   引入 run-scoped control queue，支持运行中补充输入和 best-effort interrupt。

3. **Task Graph Second Layer**

   保留 `todo_set`，增加 long-horizon task dependency graph。

### P1：若 `compare` 继续做 audit/command kernel

4. **Runtime Report Validator**

   借鉴 `go-cli-agent` 的 review artifact validator，给 security-review 类 command 加结构校验和 evidence 校验。

5. **Evidence-Aware Compaction**

   在 continuation 中加入 artifact memory / high value proofs / unresolved issues。

6. **Facade 分层**

   将 core run、command contract、store/log view 拆开，避免 `run.go` 越来越厚。

---

## 20. 最终融合判断

最佳融合方向是：

- 以 `go-cli-agent` 的 **session-first runtime、provider architecture、steer/continue、task graph、session store** 为主底座。
- 吸收 `compare` 的 **command contract、completion controller、required artifact gate、long-run checkpoint、parent callback orchestration、workspace extension trust gate、operator run summary**。

融合后理想形态应该是：

```text
core v1:
  session-first CLI harness
  run / exec / continue / steer
  provider adapters
  tools / skills / hooks
  todo + task graph
  session facts

contract layer:
  optional durable task contract
  required artifacts
  allowed tools
  role/profile
  completion gates

large-project profile:
  parent controller
  child sessions / queue jobs
  checkpoint resume
  artifact baseline
  session summary

experimental surfaces:
  web / tui / queue operator views
  all read from local facts
```

核心原则不变：不要让 framework 替模型做固定 DAG 决策；但必须让 framework 对事实源、恢复、交付物和完成判定负责。
