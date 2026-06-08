# 四个 Agent 框架对比报告

日期：2026-06-08

范围：本报告比较当前仓库 `go-cli-agent` 与 `agent-import-tmp/` 下导入的三个项目：

- `go-cli-agent`
- `agent-import-tmp/ngen-agent`
- `agent-import-tmp/deputy-agent`
- `agent-import-tmp/Cairn`

说明：结论基于当前 checkout 的源码和文档，不代表这些项目的 upstream 最新状态。未执行真实模型调用、黑盒 benchmark 或生产压测；README 中的比赛或性能结果仅作为项目自述记录，不作为本报告独立验证事实。

## 1. 总体结论

这四个项目不是同一类产品的四个实现，而是四种不同的 agent harness 取舍：

| 框架 | 一句话定位 | 最强场景 | 核心取舍 |
| --- | --- | --- | --- |
| `go-cli-agent` | Web-first 本地 agent session harness | 本地交互式多轮任务、coding/review/文档/运维、可暂停恢复的 session 管理 | 模型保持 agent 主体，runtime 提供 loop、provider adapter、工具、安全边界和文件事实，不把默认路径变成固定 workflow |
| NGEN Agent | artifact-first 的任务收敛 runtime | 需要强验收、强 evidence、强 review gate 的 coding/security/review/长期交付任务 | 以 `.ngen/` artifact、phase/state、criteria、verifier、review/done gate 驱动收敛，流程更重、更显式 |
| Deputy Agent | master-worker 长时自治任务框架 | 1 小时到 2 天的通用知识工作、报告、研究、资料处理、需要 meta/watcher/reviewer 监督的长任务 | 用固定角色和 task capsule 生命周期换取可控长时执行；更像任务级 orchestrator，不是自由 session harness |
| Cairn | 黑板架构的状态空间搜索引擎 | pentest/CTF/漏洞研究/数学证明/未知路径探索等图搜索问题 | 把问题建模为 Fact/Intent/Hint 图，由 Dispatcher 调度容器 worker 并唯一写回协议；不是通用本地 coding session harness |

如果只选一个作为 `go-cli-agent` 的发展参照：应优先学习 NGEN 的 artifact/evidence 方法论和 Deputy 的 role/provider 能力模型，但不能直接照搬二者的固定流程；`go-cli-agent` 的 spec 明确要求模型是 agent，默认 Web-first 路径不能变成重型 orchestration 或固定 DAG。

## 2. 推荐选型

| 任务类型 | 推荐框架 | 原因 |
| --- | --- | --- |
| 本地工程师通过浏览器启动、steer、continue、查看 session timeline | `go-cli-agent` | 默认入口就是本地 Web 控制台，CLI 作为 fallback；session/messages/events/goal/plan mode 都是本地文件事实 |
| 一次普通 coding / review / 文档 / 运维任务，需要可恢复但不想接受重流程 | `go-cli-agent` | 行为由 prompt、skills、AGENTS.md、provider 和工具面决定，runtime 不强制固定路线 |
| 大型代码任务，需要每个 criterion 有证据、verifier、handoff、review gate | NGEN Agent | criteria、verification、review、completion、worker evidence 都是 first-class artifact |
| 长时间无人值守的非 coding 知识工作 | Deputy Agent | master/meta 准备 harness，worker 执行，watcher 观察，reviewer 阶段裁决，适合长生命周期任务胶囊 |
| 需要确定生命周期阶段、单实例 daemon、任务级 Web GUI、用户只在关键节点介入 | Deputy Agent | `manifest.yaml` 是 stage 权威状态，host daemon tick 循环推进任务 |
| pentest / CTF / 漏洞研究 / “起点和目标明确但路径未知”的探索 | Cairn | Fact/Intent/Hint 黑板、Bootstrap/Reason/Explore 三类任务、并发容器 worker 天然适合搜索 |
| 多项目并发探索，需要中央图状态和容器隔离 | Cairn | Server 维护图一致性，Dispatcher 管理容器生命周期、并发和 worker 选择 |
| 需要广 provider matrix 和 OpenAI/Anthropic/Gemini 协议细节隔离 | `go-cli-agent` | 当前 spec 和实现重点覆盖 OpenAI Responses、Anthropic Messages、Google Gemini、openai-compatible Responses |
| 需要 Apache-2.0 友好集成 | `go-cli-agent` 或 Deputy Agent | Deputy 为 Apache-2.0；Cairn 是 AGPLv3/商业双许可，商业集成成本更高 |

## 3. 核心设计对比

| 维度 | `go-cli-agent` | NGEN Agent | Deputy Agent | Cairn |
| --- | --- | --- | --- | --- |
| 基本抽象 | session / turn / tool loop | task / artifact / criteria / verifier / review gate | task capsule / stage / role session / message bus | project graph / Fact / Intent / Hint |
| 事实源 | `.go-cli-agent/sessions/<id>/` 下的 session/state/messages/events/control/goal/tasks 等文件 | workspace 内 `.ngen/` artifacts | 每任务 `workspace/` + `control/` capsule，`control/manifest.yaml` 权威 | SQLite/Server 中的 projects/facts/intents/hints |
| 控制哲学 | model-led，harness 给能力和边界 | artifact-led，runtime 强收敛到 criteria/review/completion | master-worker lifecycle，Meta 仲裁 Worker 结果 | dispatcher-led graph search，Agent 只返回结构化结果 |
| 默认用户入口 | `go-cli-agent web` 本地 Web 控制台 | CLI/TUI/Web backend；TUI 更接近 chat-first coding 入口 | 本地 Web GUI 推荐入口，CLI 同能力 fallback | FastAPI server + dispatcher，偏服务/调度系统 |
| workflow 强度 | 低到中：Goal/Plan Mode 是 gate 和事实，不是固定 DAG | 高：phase/state、verifier、review、done gate | 高：submitted/clarifying/bootstrapping/running 等 stage | 中到高：Bootstrap/Reason/Explore 调度规则固定 |
| provider 抽象 | 薄 adapter，保留各 provider replay 和 tool 差异 | provider decision/action contract，支持多种 provider mode | `AgentRuntime` + capability matrix，主要 Claude/Codex/stub | worker CLI driver，支持 Claude Code/Codex/Pi 等外部 worker |
| completion 机制 | `finish` + completion controller + Goal/Plan/artifact gate | verifier + criteria ledger + review/completion artifact | done criteria + reviewer verdict + stage transition | reason/explore 写入 Fact/Intent/Complete，图达到 goal |
| live steering | 原生 `steer`，queue-first + best-effort interrupt | session prompt/ slash command bridge，偏 task artifact 语义 | 用户反馈、消息信封、stage 交互 | Hint 可随时注入，Agent 下次读图吸收 |
| 并发/委派 | children/queue/delegate 是 advanced profile，默认不主导 Web | worker spawn/continue、worker evidence、isolation/reconcile | Meta 管 worker；watcher/reviewer 是固定角色 | 多 worker 容器并发，Dispatcher 调度项目和 intent |
| 安全边界 | workspace path guard、shell timeout/env allowlist、provider replay 边界 | artifact path 校验、symlink escape 拒绝、repair command policy | host tools、capsule locks、provider capability gate | container 隔离、dispatcher 唯一协议写入、授权使用 disclaimer |
| 最大风险 | 表面积广，Web/runtime/provider/Goal/Plan/queue 都要一致维护 | 体系重，任务流程和 artifact contract 成本高 | 固定角色模型重，且当前自述非生产级 | 领域偏搜索/安全，部署重，AGPL 与授权风险高 |

## 4. 功能对比

| 功能 | `go-cli-agent` | NGEN Agent | Deputy Agent | Cairn |
| --- | --- | --- | --- | --- |
| 本地 Web 入口 | 强。默认产品入口，承载 start/steer/continue/Goal/Plan/Settings/timeline/tasks/queue/children | 中。`web serve` 是 management API，不引入 web-only truth | 强。README 推荐本地 Web GUI 提交和观察任务 | 中。Server 提供 API/UI 基础，核心是 dispatcher/server |
| CLI fallback | 强。`web/init/run/exec/steer/continue/sessions/goal/tasks/models/probe-provider/doctor` | 强。task/project/mission/run/resume/review/events/worker/memory 等命令丰富 | 中。submit/list/status/run/answer/pause/resume 等 | 中。主要 `serve` 与 `dispatch` |
| Provider matrix | 强。OpenAI Responses、Anthropic Messages、Google Gemini、openai-compatible Responses | 中。builtin/command/openai-comp/openai-response/anthropic | 中低。Claude/Codex/stub 为主 | 中。通过 worker driver 接 Claude Code/Codex/Pi，不是 provider-native message adapter |
| Session 持久化 | 强。session/state/messages/events/control/goal/tasks/artifacts/checkpoints | 强。但粒度是 task artifact，不是轻 session transcript | 强。task capsule 文件系统状态 | 中。项目图在 server/db，worker session 主要由 dispatcher/driver 管 |
| Live input / steer | 强。`control/steer.jsonl` 与 Web/CLI steer 是核心 v1 能力 | 中。支持 session prompt 和 operator slash command，但主要收敛到 task artifacts | 中。用户反馈和消息总线进入 lifecycle | 中。Hint 注入适合搜索，不等同交互式 steer |
| Plan approval | 强。Plan Mode 是 session-scoped execution gate | 强。mission approve / validation contract coverage | 中。Meta 准备 harness，reviewer gate；不是通用 Plan Mode | 低。Reason/Explore 调度，不是人工计划审批流 |
| Goal / mission | 强。session goal，mission 收敛为 goal 内部结构化计划 | 强。workspace mission lane 是 first-class | 中。任务描述 + harness，不强调 goal object | 中。origin/goal 是 graph search 的边界 |
| Deterministic checks | 中。artifact/goal/plan/completion guard，具体校验多由 agent/skills/test 完成 | 强。verifier sequence、criteria、review/completion gate | 强。`done_criteria.yaml` 支持声明式检查 | 中。结构化 result parsing、server graph consistency、leases |
| Review evidence | 中到强。spec 要求 audit/review evidence discipline，但默认不把所有任务变 review workflow | 很强。reviewer 明确检查 verification、handoff、criteria、worker evidence | 强。Reviewer 在阶段 gate 给 verdict | 中。图事实是 evidence，但缺少通用 review report 语义 |
| Child / worker | 中。advanced profile，model-led，不应主导默认 Web | 强。worker result/settlement/reconcile/evidence score | 强。Meta/Worker/Watcher/Reviewer 固定角色 | 强。多 worker 容器并发探索 |
| UI 复杂度 | 中。Web-first，但强调本地控制台而不是 IDE/SaaS | 中高。TUI/Web backend/ACP/CLI 面较多 | 中。Web GUI + CLI + daemon | 中高。server/dispatcher/container/deployment |
| 适合非 coding 任务 | 强。通过 skills/prompt 切换 | 中。也有 general_execution，但 coding/review artifact 色彩强 | 强。README 明确偏非 coding 通用长任务 | 中。适合未知路径搜索，不适合普通办公会话 |
| 适合安全探索 | 中。可通过 skill，但不是默认搜索引擎 | 强。security_review role 和证据 gate | 中。可做研究/报告，不是 pentest 专用 | 很强。README 将 AI pentesting 作为已验证首域 |

## 5. 实现对比

| 实现维度 | `go-cli-agent` | NGEN Agent | Deputy Agent | Cairn |
| --- | --- | --- | --- | --- |
| 语言/运行时 | Go | Go | TypeScript / Node >= 22 | Python >= 3.12 |
| 入口形态 | 单 binary 风格的 CLI + embedded local Web | 单 binary `ngen`，CLI/TUI/Web backend/ACP | Node CLI + local Web + host daemon | FastAPI server + dispatcher CLI + Docker worker container |
| 状态布局 | `.go-cli-agent/sessions/<id>/` | `.ngen/project`、`.ngen/tasks`、`.ngen/missions`、`.ngen/sessions`、`.ngen/memory` | `tasks/<taskId>/workspace` 与 `tasks/<taskId>/control` | SQLite/WAL server DB + container runtime |
| 主循环 | Runner/Engine 调 provider，tool result 回写下一轮 | task runtime 根据 phase/state、provider decision、verifier/review 收敛 | host daemon tick loop，按 stage 保持 role session 在线 | dispatcher loop 轮询项目、调度 bootstrap/reason/explore |
| Provider 接入 | `ProviderAdapter.RunTurn`，adapter 负责 replay/tool/generation 差异 | provider decision JSON/action schema，runtime 校验 allowed actions | `AgentRuntime` 接口和 capability matrix | worker driver 渲染 prompt，执行 CLI，解析 stdout JSON |
| 任务状态 | todo + durable task graph + Goal/Plan Mode | criteria/sprint/continuity/plan/project/mission/review/completion artifact | manifest stage + envelopes + events + done criteria | Fact/Intent/Hint 图和 reason lease/intent claim |
| 恢复策略 | 原始 messages/events 保留，compaction 只改 provider view；continue 不重放副作用 | continuity/context/harness/history artifacts 做恢复线索 | startup recovery 修复未完成 worker、消息总线和 session closeout | server graph + dispatcher checkpoints/leases；单 dispatcher 设计 |
| 并发模型 | queue/children/worker pool 是 advanced | worker tree + isolation + reconcile | one task capsule 内固定角色；worker 一次一个，Meta 仲裁 | ThreadPoolExecutor，多项目/多 worker 容器 |
| 权限模型 | path escape/symlink、shell timeout/env allowlist、Plan/Goal/completion guard | symlink escape、observation/repair command policy、permission mode | capsule locks、host tools、provider capability gating | Docker/container、dispatcher 唯一写协议、授权使用边界 |
| 可观测性 | Web detail 返回 metadata/state/goal/plan/provider attempts/checkpoint/tasks/children/background/steer/messages/events/timeline | 大量 JSON/JSONL artifact，harness latest/history、provider_usage、review findings | events.jsonl、status.md、streams、watcher windows、manifest history | graph export、server API、dispatcher logs、intent/reason leases |

## 6. `go-cli-agent` 分析

### 6.1 优势

- 产品边界清晰：它是 Web-first 本地 agent harness，不是 hosted SaaS、重型 TUI 或固定 workflow engine。
- 默认入口合理：本地 Web 控制台负责 session start、steer、continue、Goal、Plan Mode、Settings、timeline/tasks/queue/children 观测；CLI 保留为脚本化、CI、故障恢复和高级调试 fallback。
- Provider 边界是四者里最适合做“通用 agent SDK/harness”的：OpenAI Responses、Anthropic Messages、Gemini、openai-compatible Responses 的 replay、tool schema、reasoning/store/generation 选项留在 adapter 层。
- 文件事实模型扎实：session metadata、state、messages、events、control/steer、control/background、todo、tasks、artifacts、checkpoints 等都在 session store 下。
- Live steer/continue 是核心能力，而不是旁路功能。它更适合真实人机协作中的“运行中补充约束、暂停、恢复”。
- Goal 与 Plan Mode 的定位克制：Goal 记录 durable objective 和完成审计，Plan Mode 是显式审批 gate；二者不把 runtime 变成固定 DAG。
- 相比 Deputy/Cairn，它更适合混合任务：coding、文档、审计、运维、调研都能通过 skills/prompt/AGENTS.md 切换。

### 6.2 不足

- 表面积大：Web、CLI、SDK facade、provider adapters、session store、Goal、Plan Mode、tasks、queue、children 都要保持同一事实源，回归成本高。
- 默认不强制固定 workflow，所以对高风险交付的“证据齐全性”不如 NGEN 天然严格，需要依赖 user prompt、skills、review discipline 和 completion gates。
- Advanced profile 里的 queue/children/delegate/isolation 已存在，但按 spec 不能主导默认 Web；如果用户期待一上来就是大型项目 worker orchestration，它会显得保守。
- 如果任务天然是状态空间搜索，例如 pentest 中海量意图分叉，session model 不如 Cairn 的 graph search 自然。

### 6.3 最适合

- 本地工程师希望用浏览器管理 agent session。
- 需要频繁 steer / continue / pause / inspect 的交互式任务。
- 需要多 provider 支持和 provider replay 正确性的 agent harness。
- 需要一个可以演进为 Go SDK 或 local API service 的基础框架。

### 6.4 不适合

- 明确要求固定 master-worker lifecycle 和无人值守一两天任务交付的场景，Deputy 更直接。
- 明确要求每条验收标准都被 artifact/review/verifier gate 强制收敛的 coding harness，NGEN 更强。
- 明确要求多 worker 并发探索未知攻击路径或 CTF state space，Cairn 更自然。

## 7. NGEN Agent 分析

### 7.1 优势

- artifact-first 纪律最强。README 明确 task truth 在 workspace `.ngen/` artifacts，不在聊天历史。
- coding/review/security 任务的收敛链完整：phase/state、criteria ledger、verifier sequence、review report、completion report、handoff、continuity、sprint、harness evaluation。
- 对“证据不全不能收工”的任务非常强。reviewer 会检查 verification 是否 passed、handoff 是否存在且新鲜、criteria 是否有 evidence refs、worker evidence 是否可信。
- worker 体系比 `go-cli-agent` 默认 profile 更重也更完整：worker result、settlement、reconcile、workspace isolation、evidence grade、trusted_for_parent_completion 都进入 artifact。
- provider decision action schema 很明确，能让模型在受控动作集合中选择 `run`、`resume`、`review`、`task_update`、`project_patch`、`worker_spawn` 等动作。
- 对大型 coding/security/review 任务非常有利，因为它把“下一步是什么、证据在哪里、是否能信 child output”都外化成文件事实。

### 7.2 不足

- 体系重，理解和维护成本高。criteria/sprint/continuity/project/mission/worker/review/completion artifact 很完整，但普通任务会显得流程过厚。
- 它更像 task convergence runtime，不像自由的 local session harness。用户若只是想启动一段 prompt、运行中 steer、继续对话，`go-cli-agent` 更轻。
- Provider matrix 不如 `go-cli-agent` 面向通用 provider protocol，当前重点是 builtin/command/OpenAI-compatible/OpenAI Responses/Anthropic。
- Web 不是默认产品心智的唯一入口；TUI/CLI/Web backend/ACP/worker/memory 等 surface 较多，默认体验不如 `go-cli-agent web` 单一。
- 它的强 gate 如果直接搬进 `go-cli-agent` runtime，会违背当前 spec 中“模型是 agent，harness 不固定审计路线/委派策略/阅读顺序”的边界。

### 7.3 最适合

- 有明确验收标准的 coding task。
- 安全审计、代码 review、长期工程任务、需要独立 reviewer/evaluator 证据的任务。
- 需要 child worker 但又要防止 parent 盲信 child prose 的任务。

### 7.4 不适合

- 轻量交互式会话。
- 以 Web 控制台为唯一默认入口的本地 harness。
- 不希望接受 artifact contract 和 explicit gate 的普通知识工作。

## 8. Deputy Agent 分析

### 8.1 优势

- 长时任务定位明确：README 写的是约 1 小时到约 2 天的长时无人值守任务，并且刻意不为 coding 特化。
- master-worker 架构清楚：Meta 规划和仲裁，Worker 执行，Watcher 观察 worker 输出，Reviewer 在 gate 给 verdict。
- task capsule 模型清晰：`workspace/` 承载工作，`control/manifest.yaml` 是 stage 权威状态，`events.jsonl` 是追加事件流。
- host daemon 的 tick loop 和单实例锁适合无人值守运行：读取 manifest、保持角色 session、分发 watcher window、评估推进、恢复未完成 worker。
- provider interface 抽象不错：`AgentRuntime`、capability matrix、role-to-provider 绑定，允许不同角色使用不同 provider/model。
- `done_criteria.yaml` 把部分完成检查做成 deterministic check，减少纯 LLM 自评。

### 8.2 不足

- 固定角色和生命周期强，模型自主性低于 `go-cli-agent`。它是 master-worker orchestrator，不是让模型自由选择工作流的 harness。
- 当前项目自述为 0.1.0 参考实现，且说明未达作者生产级标准，后续版本可能大幅变化。
- 主要针对 Claude 调教，README 明确 Codex/GPT 当前表现更弱。
- 不为 coding 特化，若任务是代码修复、测试、patch、review evidence，NGEN 或 `go-cli-agent` 更贴近。
- Provider 覆盖窄，主要是 Claude/Codex/stub，而不是通用 OpenAI/Anthropic/Gemini adapter matrix。

### 8.3 最适合

- 长报告、研究、资料整理、非 coding 知识工作。
- 需要任务级 Web GUI、阶段推进、watcher/reviewer 审查、人只在关键节点介入。
- 需要为不同角色分配不同 provider/model 的长时任务。

### 8.4 不适合

- 需要快速本地多轮对话和 frequent steer 的普通 agent session。
- 需要自由模型决定是否委派、是否 review、是否走固定阶段的场景。
- 需要生产级稳定性或广泛 provider 兼容的通用 harness。

## 9. Cairn 分析

### 9.1 优势

- 抽象非常适合未知路径搜索：Fact 是确认发现，Intent 是探索方向，Hint 是人类注入判断。
- Blackboard/Stigmergy 模型避免 agent 直接互相通信，所有 worker 都通过共享图协调。
- Bootstrap/Reason/Explore 三类任务足够简单，能覆盖“直接尝试、判断是否完成/提出 intent、执行一个 intent”。
- Dispatcher 是唯一协议写入者，Agent 不直接 claim、不 heartbeat、不调用 API。这降低了 worker 输出污染协议状态的风险。
- Server 维护 graph consistency，Dispatcher 管容器生命周期和调度，职责切分清楚。
- 容器 worker 和并发调度适合 pentest/CTF 这类需要隔离执行环境和多方向探索的任务。

### 9.2 不足

- 它不是本地 agent session harness。没有 `go-cli-agent` 这类 session/steer/continue/messages/events/Goal/Plan Mode 的交互模型。
- 部署重：Python/FastAPI/SQLite/Docker/dispatcher/worker image/config 都要运行。
- 任务模型强依赖 graph search。普通 coding、文档、运维任务若硬套 Fact/Intent/Hint，可能增加不必要复杂度。
- Provider 接入在 worker CLI driver 层，不是 provider-native replay/tool/generation adapter；如果需要精细处理 OpenAI/Anthropic/Gemini 协议差异，`go-cli-agent` 更合适。
- 当前 dispatcher 明确按单 Dispatcher 实例设计；多 dispatcher 共同调度同一 server 不属于支持场景。
- 许可证是 AGPLv3/商业双许可，且安全用途必须有明确授权；商业或内网产品集成需要先评估法务和授权边界。

### 9.3 最适合

- pentest、CTF、漏洞研究、红队实验、数学证明、复杂诊断等“起点和目标清楚，路径未知”的问题。
- 多 worker 并发探索，且需要图状态做全局协调。
- 需要把人类 hint 插入全局搜索状态，而不是插入某个正在运行的 session turn。

### 9.4 不适合

- 普通本地 coding assistant。
- 需要 Web-first session console 的人机协作。
- 需要轻量 CLI/CI fallback 的单 repo 工程任务。

## 10. 关键差异：模型自主性 vs runtime 编排

这四个框架最大的分歧不是语言或 UI，而是谁拥有工作流决策权：

| 决策权 | 框架 | 表现 |
| --- | --- | --- |
| 模型主导，harness 给能力和边界 | `go-cli-agent` | runtime 不固定审计路线、委派策略或 taskboard 节奏；Goal/Plan/Task 是事实和 gate，不是默认 DAG |
| runtime/artifact 主导收敛 | NGEN Agent | 模型在 action schema 中选择动作，但 completion 必须经过 criteria/verifier/review/completion artifact |
| Meta 角色主导 orchestration | Deputy Agent | Meta 决定 harness、启动/停止 Worker、阶段推进和用户介入；host daemon 执行 stage machine |
| Dispatcher 主导 graph search | Cairn | Agent 输出结构化结果，Dispatcher 认领/心跳/写图/调度，Server 保持协议真相 |

对 `go-cli-agent` 来说，这意味着不能为了“更强”而直接把 NGEN/Deputy/Cairn 的 runtime control plane 搬进默认路径。更合理的学习方式是：

- 学 NGEN 的 evidence artifact 设计，但保持它作为 skill、completion checker、advanced profile 或 explicit task mode，而不是默认固定流程。
- 学 Deputy 的 role/provider capability matrix，但让 role/child 是否使用保持 model-led 或 user-led。
- 学 Cairn 的 Fact/Intent/Hint 作为可选探索型 task profile，而不是替换 session/messages/events 事实源。

## 11. 对 `go-cli-agent` 的可借鉴点

### 11.1 可以借鉴

- 从 NGEN 借鉴 criteria evidence ledger：对明确 review/audit/security/coding 任务，生成更结构化的 evidence refs 和 stale context 检查。
- 从 NGEN 借鉴 worker result trust score：如果 advanced children/queue 被用于 parent completion，可以要求 child result、settlement、reconcile、verification/review 状态进入 parent-visible fact。
- 从 Deputy 借鉴 provider capability matrix：明确每个 provider 是否支持 inject、abort、resume、context usage、compaction、streaming 等能力，减少 runtime 猜测。
- 从 Deputy 借鉴 local Web + daemon 的恢复语义，但 `go-cli-agent` 应继续以 session store 为唯一权威，不引入第二套 Web 状态。
- 从 Cairn 借鉴 Fact/Intent/Hint graph，用于安全探索、研究或 large-project discovery profile。
- 从 Cairn 借鉴 Dispatcher 唯一写协议的思想，用在 queue worker 对 parent session 的结果投递上，避免 child 自说自话改 parent completion。

### 11.2 不建议借鉴到默认路径

- 不建议把 NGEN 的 phase/state/verifier/review/done gate 变成所有 session 默认必经路线。
- 不建议把 Deputy 的 Meta/Worker/Watcher/Reviewer 固定四角色变成 `go-cli-agent web` 默认任务结构。
- 不建议把 Cairn 的 graph search 变成 session store 的主抽象；它应是独立 advanced profile。
- 不建议把 Web 控制台做成重 IDE、远程终端或图形状态权威源；这与当前 spec 冲突。

## 12. 总结排名

不同目标下的排名如下：

| 目标 | 第一选择 | 第二选择 | 说明 |
| --- | --- | --- | --- |
| 本地 Web-first agent harness | `go-cli-agent` | Deputy Agent | `go-cli-agent` 默认就是 Web-first session harness；Deputy 也是 Web GUI，但更像长任务 orchestrator |
| Provider adapter 严谨性 | `go-cli-agent` | Deputy Agent | `go-cli-agent` 对 OpenAI/Anthropic/Gemini replay/tool/generation 差异最明确 |
| Coding 任务 evidence 收敛 | NGEN Agent | `go-cli-agent` | NGEN 的 criteria/verifier/review/completion artifact 更硬 |
| 长时非 coding 自治任务 | Deputy Agent | `go-cli-agent` | Deputy 的 meta/watcher/reviewer 结构适合无人值守长任务 |
| 状态空间搜索 | Cairn | NGEN Agent | Cairn 的 Fact/Intent/Hint 图和 dispatcher 并发探索最自然 |
| 默认轻量交互 | `go-cli-agent` | NGEN Agent | `go-cli-agent` 的 session/steer/continue 更直接 |
| 多 worker 探索并发 | Cairn | NGEN Agent | Cairn 面向多容器 worker 并发，NGEN 面向受控 child worker |
| 商业集成低阻力 | `go-cli-agent` / Deputy Agent | NGEN Agent | Cairn 的 AGPLv3/商业双许可需要额外评估 |

最终判断：

- `go-cli-agent` 应继续坚持 Web-first local session harness，不应把默认产品变成 NGEN/Deputy/Cairn 式重编排。
- NGEN 是最值得学习的“artifact 和 evidence 收敛”样本。
- Deputy 是最值得学习的“长时任务角色化和 provider capability”样本。
- Cairn 是最值得学习的“未知状态空间搜索和 dispatcher 唯一写协议”样本。

## 13. 主要证据锚点

`go-cli-agent`：

- 产品定位和 Web-first v1 目标：`spec/00-product.md:5`、`spec/00-product.md:52-63`
- 模型是 agent、Web 控制台不绕过 runtime/session/provider 边界：`spec/00-product.md:157`、`spec/00-product.md:185`
- runtime 分层和 session store 权威：`spec/01-runtime-architecture.md:5`、`spec/01-runtime-architecture.md:29`
- Provider contract：`spec/03-provider-contracts.md:7`、`spec/03-provider-contracts.md:27`
- phase 边界和 Web-first v1 验收：`spec/09-phase-plan.md:14-21`、`spec/09-phase-plan.md:174-185`
- CLI command surface：`internal/app/app.go:64-90`、`internal/app/app.go:101-108`
- session store 创建的文件事实：`internal/session/store.go:103-145`
- Web session detail 暴露 goal/plan/provider attempts/task/children/steer/messages/events：`internal/webconsole/service.go:213-230`

NGEN Agent：

- artifact-first 和 task truth：`agent-import-tmp/ngen-agent/README.md:5`、`agent-import-tmp/ngen-agent/README.md:17-19`
- scope 和 command surface：`agent-import-tmp/ngen-agent/README.md:28-42`
- bounded repair loop 和 provider modes：`agent-import-tmp/ngen-agent/README.md:43`、`agent-import-tmp/ngen-agent/README.md:74`
- criteria/review/mission/worker artifact：`agent-import-tmp/ngen-agent/README.md:48-54`、`agent-import-tmp/ngen-agent/README.md:68-72`
- artifact store layout：`agent-import-tmp/ngen-agent/internal/artifact/store.go:62-146`
- provider action schema：`agent-import-tmp/ngen-agent/internal/provider/provider.go:18-35`
- reviewer evidence checks：`agent-import-tmp/ngen-agent/internal/review/reviewer.go:75-104`

Deputy Agent：

- master-worker 定位和长时任务目标：`agent-import-tmp/deputy-agent/README.zh.md:5-14`、`agent-import-tmp/deputy-agent/README.zh.md:18-28`
- 项目状态限制：`agent-import-tmp/deputy-agent/README.zh.md:33-39`
- task capsule、manifest、events、roles、AgentRuntime：`agent-import-tmp/deputy-agent/docs/ARCHITECTURE.zh.md:3-9`、`agent-import-tmp/deputy-agent/docs/ARCHITECTURE.zh.md:62-73`
- stage lifecycle 和 transition gate：`agent-import-tmp/deputy-agent/docs/ARCHITECTURE.zh.md:77-114`、`agent-import-tmp/deputy-agent/docs/RUNTIME.zh.md:61-80`
- deterministic done criteria：`agent-import-tmp/deputy-agent/docs/DATA_FORMATS.zh.md:215-253`
- provider interface 和 adapters：`agent-import-tmp/deputy-agent/docs/PROVIDERS.zh.md:3-9`、`agent-import-tmp/deputy-agent/docs/PROVIDERS.zh.md:103-105`
- manifest state machine source：`agent-import-tmp/deputy-agent/src/shared/manifest.ts:2-11`、`agent-import-tmp/deputy-agent/src/shared/manifest.ts:45-56`
- daemon orchestration source comment：`agent-import-tmp/deputy-agent/src/host/daemon.ts:2-37`

Cairn：

- Blackboard、Fact/Intent/Hint、OODA：`agent-import-tmp/Cairn/README.md:48-58`
- Bootstrap/Reason/Explore：`agent-import-tmp/Cairn/README.md:73-75`
- Server/Dispatcher/worker responsibility：`agent-import-tmp/Cairn/README.md:102-106`
- deployment and persistence: `agent-import-tmp/Cairn/README.md:143-173`
- license and commercial caveat：`agent-import-tmp/Cairn/README.md:199-203`
- Dispatcher 是唯一协议写入者：`agent-import-tmp/Cairn/docs/specs/dispatcher-design.md:7-23`
- 单 Dispatcher 限制：`agent-import-tmp/Cairn/docs/specs/dispatcher-design.md:28-30`、`agent-import-tmp/Cairn/docs/specs/dispatcher-design.md:587-588`
- DB schema：`agent-import-tmp/Cairn/cairn/src/cairn/server/db.py:13-80`
- dispatcher concurrency loop：`agent-import-tmp/Cairn/cairn/src/cairn/dispatcher/scheduler/loop.py:39-49`、`agent-import-tmp/Cairn/cairn/src/cairn/dispatcher/scheduler/loop.py:120-169`
