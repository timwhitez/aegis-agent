# NGEN Agent Foundations v0.3

> 状态: Draft
> 目的: 把近期 agent harness 相关文章与实践观点收敛成 NGEN 的实现立场
> 最后更新: 2026-03-18

## 1. 为什么需要这份文档

2025 年底到 2026 年初，agent 设计出现了一条非常清晰的主线：

- 模型能力继续提升，
- 但长时程任务能否真的跑起来，越来越取决于模型外的系统设计，
- 这个系统设计的关键词已经从 prompt engineering 上移，变成 harness、loop、context engineering、memory、verification、policy。

NGEN 的出发点是：

> 下一代 coding agent 的竞争力，不是“会不会说”，而是“能不能在长时程任务里持续、可恢复、可验证地推进工作”。

## 2. 共同地图

### 2.1 Harness

Harness 不是“prompt 外壳”，而是模型之外的一切执行系统：

- 工具访问，
- 工作区和环境管理，
- 状态持久化，
- context assembly 和 compaction，
- verification，
- approvals 与 policy，
- review 与 observability。

在 NGEN 中，harness 不是胶水层，而是产品本体。

### 2.2 Harness Engineer

`harness engineer` 不是“写一点 glue code 的工程师”，而是专门设计 leverage 的工程角色。他负责把强模型变成能完成真实任务的系统：

- 设计控制环，
- 设计 durable memory，
- 设计工具和权限边界，
- 设计验证闭环，
- 设计人机协同和升级路径。

NGEN 明确把这类工作视为产品设计与系统工程，而不是实现细节。

### 2.3 Loop

NGEN 对智能工作流的最低阶定义是：

1. 观察世界，
2. 决定下一步，
3. 执行动作，
4. 验证结果，
5. 写回记忆，
6. 继续或停止。

因此，本仓库采用一句清晰的工作假设：

> life or intelligence for work is a loop with memory, verification, and explicit control.

### 2.4 Memory

聊天历史不是 long-horizon memory。

NGEN 把 memory 分成三层：

- ephemeral memory: 当前 loop 的短期观察，
- durable machine memory: JSON / JSONL 工件，用于恢复、自动化和校验，
- durable human memory: Markdown 计划、进展、决策、handoff，用于人类理解和交接。

### 2.5 Context Engineering

context engineering 不是“把 prompt 写得更聪明”，而是决定：

- 哪些信息进入本轮上下文，
- 哪些内容被 pin 住，
- 哪些内容需要压缩，
- 哪些内容需要固化为 durable memory，
- 哪些内容应该被明确排除。

NGEN 因此把 Context OS 设计成 runtime plane，而不是 prompt 辅助工具。

### 2.6 Verification

verification 不是收尾动作，而是 cognition 的一部分。

如果 agent 没有验证路径，它大概率会：

- 过早宣布完成，
- 把推断当事实，
- 把“看起来像成功”当成真正的 task completion。

NGEN 的核心规定因此是：

- 重要任务必须经过 verifier chain，
- coding 和 security review 默认必须经过 review lane，
- `Done` 必须被显式 gate，而不是由模型自行判断。

## 3. 外部观点如何收敛到 NGEN

| 来源 | 关键观点 | 风险提醒 | NGEN 采用的决策 |
| --- | --- | --- | --- |
| Mario Zechner `pi-coding-agent` | context engineering 比 prompt tricks 更关键；并行 sub-agent 很容易破坏代码质量 | 过早并行化、把系统复杂度藏起来 | 保持 kernel 小；并行只能通过 artifact contracts 和隔离工作区 |
| Browser Use `Bitter Lesson Agent Frameworks` | agent frameworks 容易过度抽象；真正重要的是清晰 loop 和通用能力 | 造出“很聪明”的框架，反而掩盖控制流 | v0.1 明确保持 runtime loop 可见、可解释、可恢复 |
| OpenAI `Run long-horizon tasks with Codex` | 持久记忆应外化为文档和计划；大型任务需要跨多个上下文窗口推进 | 把 durable project memory 放在对话线程里会很脆弱 | `.ngen/` + repo docs 作为 durable memory；文档而不是聊天做系统事实来源 |
| OpenAI `Harness engineering` | leverage 来自 scaffolding、verification、environment legibility，而不只是模型本身 | 只调 prompt，忽略系统设计，会限制真实产出 | 把 harness engineer 的工作变成产品主轴；强调 evidence、review、tooling、observability |
| OpenAI Codex 开源代码 | `plan` mode、`update_plan` checklist tool、子 agent 工具面、prompt layering，以及 hierarchical project-doc merge、skills/custom prompts gating、model-level context budget、compact prompt 这些 Context OS 形状都已经具备可复用 shape | 如果整包照抄，会把 Codex 自身 thread/session/history 假设一起带入新 runtime | 对 context assembly 层直接 adopt 形状：ancestor docs top-down merge、skills/custom prompts layering、模型级 context window 与 auto-compact 字段；approval/sandbox、thread truth、task loop、artifacts、done gate、replay 和 ACP 保持 NGEN 自研 |
| Anthropic `Effective harnesses for long-running agents` | 要跨 context window 推进任务，需要 progress files、分治、重复验证与恢复机制 | transcript 不能承担全部状态 | 每轮都写 artifacts；resume、checkpoints、watch 是一等能力 |
| Model Context Protocol tools schema | tool contract 不只是一串命令名；还应包含输入输出 schema 与 side-effect hints | 工具边界过于松散会让 policy、review 与 replay 语义失焦 | v0.1 工具合同补充 machine-readable schema 与 `read_only` / `destructive` / `open_world` / `idempotent` annotations |
| LangChain `The anatomy of an agent harness` | agent = model + harness；核心是状态、工具、编排、控制 | 把 agent 误解为“模型自动做一切” | NGEN 以六个 runtime planes 明确系统边界 |
| LangChain `Autonomous context compression` | context compression 需要主动管理、不是被动截断 | context 爆炸会直接伤害执行质量 | 设计 context pack、budget、compaction pipeline 和 promotion rules |
| LangChain `How coding agents are reshaping engineering, product and design` | coding agents 会把重心推向规格、评审、可验证的 leverage 设计 | 只做代码生成，忽略规格与治理 | coding agent 默认输出 plan、evidence、review、handoff，而不仅仅是 diff |

## 4. 三种声音与 NGEN 的综合结论

这些文章大体可以归为三种互补声音：

### 4.1 极简派

代表倾向：Mario、Browser Use。

主张：

- loop 要小，
- 框架不要过度“聪明”，
- 控制流要可见，
- 并行要谨慎。

### 4.2 脚手架派

代表倾向：Anthropic、OpenAI、Codex。

主张：

- long-horizon agent 成败不在一次回答，
- 而在 docs、plans、progress、verifiers、workspace、observability 这些脚手架，
- durable artifacts 是系统能力的一部分。

### 4.3 系统化派

代表倾向：LangChain。

主张：

- 需要把 state、filesystem、sandbox、compaction、orchestration、review、verification 抽象成一个系统，
- 否则经验很难复用和扩展。

### 4.4 NGEN 的综合结论

NGEN 的最终判断是：

> 下一代 agent 框架应该是: loop small, memory strong, verification hard, coordination observable.

也就是说：

- 不把复杂度堆进 kernel，
- 不把记忆留在聊天里，
- 不让 done 脱离 evidence，
- 不把 multi-agent 设计成聊天风暴。

## 5. NGEN 的产品世界观

### 5.1 coding-first, profile-extensible

NGEN 的锚点产品是 coding agent，而不是抽象“万能代理平台”。

原因很简单：

- coding 是最容易验证、最容易观察、最容易沉淀 durable artifacts 的任务类型，
- security review、reviewer、general execution 虽然任务目标不同，但共享同一底座，
- 先把 coding slice 做对，能最大程度降低系统复杂度。

### 5.2 artifact-first collaboration

NGEN 不把协作设计成 agent 之间互相转述隐藏上下文。

默认协作边界是 artifact contract：

- manager 下发 contract，
- worker 消费输入工件、政策和期望输出 schema，
- worker 产出结构化结果，
- manager 做验收、合并、升级。

### 5.3 review is part of execution

评审不是最后附加的“道德检查”，而是执行闭环的一部分：

- coding 任务防止 overclaiming，
- security review 区分 confirmed finding 与 suspicion，
- handoff 需要和 verifier truth 对齐。

## 6. v0.1 精简决策

为了可落地和低依赖，v0.1 明确做以下收缩：

1. 单二进制优先，不做分布式控制平面。
2. 本地文件系统优先，不引入数据库作为必需组件。
3. 文档检索优先，不把向量检索作为前置条件。
4. CLI 和 JSONL 优先，不先做复杂 TUI。
5. manager + bounded workers 优先，不做开放式 team swarm。
6. JSON / JSONL / Markdown 优先，不引入 YAML。
7. provider-neutral adapter 优先，不锁死单一模型厂商。

## 7. 设计原则

### P1. Loop 小，Contracts 强

kernel 只做最必要的状态推进、checkpoint、done gate 和调度。

### P2. Artifact Memory 大于 Chat Memory

即使没有原始聊天记录，任务也必须可恢复、可审计、可交接。

### P3. Verification 先于 Completion

生成一个看上去不错的答案，不等于任务完成。

### P4. Observability 大于聪明幻觉

如果系统不能解释“发生了什么、为什么停下、哪里没证据”，它就不适合生产级长任务。
长时程 runtime 不只要能 `resume`，还要知道哪些副作用可以安全重放，哪些只能进入对账或人工升级。

### P5. 低依赖，高杠杆

优先使用本地文件、本地工具、标准库和简单结构。

### P6. Multi-Agent 通过 Artifact Contracts，而不是共享隐式上下文

协作的核心不是“更多 agent”，而是“更清晰的输入输出合同”。

### P7. Control Primitives 是产品能力

`aside` 和 `watch` 不应该被当作聊天技巧，而应该成为 runtime 原语。

### P8. Traceability 是执行系统的一部分

spec-first 不等于“先写很多文档再说”。

如果需求、artifact schema、接口、验证、验收之间不能稳定回链，spec 很快就会退化成没有执行力的背景材料。

因此 NGEN 明确要求：

- success criteria 必须有稳定 ID，
- artifact 与 API 变更必须有 owner doc，
- 交付声明必须能回链到 verifier / review / waiver 证据，
- coding slice 默认采用 narrow-first TDD：先固定合同，再写会失败的最窄测试，再补实现。

## 8. 反模式

- 在基本 loop 还没跑通前就做大而全框架，
- 把 memory 藏在不可检查的黑盒里，
- 让多个 write-capable agents 无控制地并行写同一个工作区，
- 在没有 verifier 证据时允许 `Done`，
- 把人类可读状态和机器状态混在一个脆弱文件里，
- 把 context overflow 当实现细节而不是产品问题，
- 把 agent team 数量当成系统先进性的主要指标。

## 9. 参考链接

- https://mariozechner.at/posts/2025-11-30-pi-coding-agent/
- https://browser-use.com/posts/bitter-lesson-agent-frameworks
- https://developers.openai.com/blog/run-long-horizon-tasks-with-codex
- https://cookbook.openai.com/articles/codex_exec_plans/
- https://openai.com/index/harness-engineering/
- https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents
- https://modelcontextprotocol.io/specification/2025-11-25/schema
- https://blog.langchain.com/the-anatomy-of-an-agent-harness/
- https://blog.langchain.com/autonomous-context-compression/
- https://blog.langchain.com/how-coding-agents-are-reshaping-engineering-product-and-design/
