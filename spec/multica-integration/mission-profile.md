# Mission-Compatible Profile

本文定义 Multica + `aegis-agent` 集成在大型项目、长时间任务、multi-agent 监督场景下的可选 profile。它吸收 Missions 类系统的有效机制，但不把 `aegis-agent` 改造成固定 workflow engine。

本 profile 位于 MVP 之上：

- MVP 仍然只是 `gocli-stream-json` subprocess backend。
- 默认入口仍然是 Web-first 本地控制台与 CLI fallback。
- mission / large-project 能力必须通过 Multica 的显式任务形态、aegis-agent 的 Goal / Plan Mode / delegate / queue 高级面，或后续 API 字段启用。
- 没有启用本 profile 时，`gocli` backend 不需要 plan approval、validation contract、handoff parser 或多角色调度才能完成普通任务。

## 1. 设计目标

长时间 agent 任务的核心瓶颈不是单次模型调用，而是人类注意力与恢复成本。集成设计要让用户能够异步监督：

- 当前执行目标是什么。
- 哪个角色正在工作。
- 验证契约覆盖了哪些需求。
- 最近一次 worker 交接了什么。
- validator 发现了什么。
- 系统是否把发现的问题转成后续工作。
- session、usage、工具调用和 artifact 证据能否追溯。

这些能力应通过事实源、协议事件和结构化 artifact 组合出来，而不是通过硬编码 DAG、浏览器端状态或进程内临时上下文维持。

## 2. 角色模型

Missions 文章里的 Orchestrator / Worker / Validator 在本集成中映射为职责，而不是 runtime 必须自动创建的三段流程。

| Mission 职责 | aegis-agent 本地语义 | Multica 侧语义 | 事实源 |
| --- | --- | --- | --- |
| Orchestrator | `planner` role hint、Goal 内部计划、Plan Mode approval gate | mission plan、里程碑、预算、validation contract owner | Multica mission store 或 aegis-agent `goal.json` / `planmode.json` |
| Worker | `generator` role hint、普通 `exec` / child session | feature 实现 run，通常一个可提交变更单元 | session events、messages、artifact refs、git diff/commit |
| Validator | `evaluator` role hint、review / validation session | scrutiny validator、user-testing validator、follow-up feature creator | validation report、test command evidence、UI/E2E evidence |

约束：

- role 是 hint 和 traceability 字段，不是强制编排状态机。
- 角色选择应持久化到 session metadata、queue job、mission record 或 protocol metadata 中，不能只停留在一次 prompt 文本。
- provider/model override 必须基于显式 role，例如 `planner` / `generator` / `evaluator`。不要从 `agent_name`、标签或自然语言简介中做模糊匹配。
- Multica 可以把 `orchestrator` / `worker` / `validator` 映射到 aegis-agent 的 `planner` / `generator` / `evaluator`，但该映射属于 Multica mission profile，不属于 `aegis-agent` core runtime。

## 3. 验证契约

复杂任务必须先定义“完成”的外部标准，再开始实现。验证契约应独立于实现方案，至少包含：

- assertion id：稳定、可引用、不可复用。
- user-facing requirement：用户真正关心的行为或约束。
- verification method：单元测试、集成测试、E2E、手工可复现步骤、静态检查、性能/安全检查等。
- owner milestone 或 feature：哪个工作单元声称满足该 assertion。
- acceptance evidence：完成时必须留下的命令、报告、截图、日志或 artifact。

覆盖规则：

- 每个 feature 应声明 `claimed_assertions`。
- 每个 milestone 应声明会验证哪些 assertion。
- plan approval 前应检查 uncovered assertion、未知 assertion、重复/空 id、无验证 milestone、无 assertion feature。
- uncovered 或 invalid validation contract 默认阻断 mission plan approval；只有显式 override 才能继续，并必须留下事实记录。

边界：

- Multica mission workflow 可以持有 validation contract 的主模型。
- `aegis-agent` standalone 场景可以把它收敛到 Goal 的内部 validation plan。
- `gocli-stream-json` v1 只传递可选 metadata、artifact ref 或 result handoff，不要求在协议里承载完整契约。

## 4. 结构化交接

worker / validator 结束时不能只输出“完成了”。它应留下结构化交接，便于后续 run 和人类 review 恢复上下文。

最小字段：

| 字段 | 含义 |
| --- | --- |
| `summary` | 当前 run 的人类可读结论 |
| `completed` | 已完成的 feature、assertion、文件或决策 |
| `remaining` | 未完成事项和下一步建议 |
| `commands` | 运行过的命令、退出码、关键输出位置 |
| `artifacts` | 报告、计划、验证证据、截图、日志、commit 等引用 |
| `risks` | 已知风险、假设、需要用户判断的问题 |
| `validation` | 哪些 assertion 已验证、失败、跳过或仍需验证 |

推荐 artifact：

```text
reports/spec.md
reports/plan.md
reports/progress.md
reports/validation.md
reports/handoff.json
```

约束：

- 交接必须依赖可见文件事实、协议 result 或 session events；不能依赖父子 agent 之间未落盘的聊天上下文。
- 如果完成后还存在失败验证或未处理 blocker，系统应把它转成 follow-up feature 或明确标为 blocked，而不是继续向后推进。
- 对于 `aegis-agent`，`record_goal_progress(kind="handoff")`、`session.md`、checkpoint、visible output 列表和最终 `result.handoff` 都可以成为交接事实源。

## 5. 执行策略

默认策略是串行为主、局部并行。

规则：

- feature 级写入任务串行执行。任意时刻默认只有一个 worker 或 validator 对同一 worktree 进行写入。
- 单个 feature 内部允许只读并行：代码搜索、API 调研、独立审查、测试日志分析。
- 验证阶段允许派生多个只读 evaluator，但写入修复必须回到一个新的 worker run。
- 若确实需要并行写入，必须启用显式 isolation，并记录每个 child 的 workdir、base revision、merge/patch 策略和冲突处理结果。
- 不允许通过 direct communication 在多个 agent 私聊里保存关键状态；共享状态必须回写到 mission store、session store、artifact 或 protocol event。

这与当前仓库 phase 约束一致：delegate、children、queue、isolation 是 large-project / advanced profile，不主导默认 Web 页面，也不把 core runtime 改成 worker pool orchestration。

## 6. Mission Control 观测

Multica 可以提供 Mission Control 式监督界面，但它只能聚合事实，不能成为第二套执行状态权威。

建议展示：

- mission objective、plan approval 状态、validation coverage。
- 当前 active run 的 role、session id、provider/model、status、usage。
- tool use / tool result stream、stderr tail、最近日志。
- worker handoff summary、validator findings、follow-up features。
- artifact refs：计划、进展、验证、报告、commit、截图。
- budget 和墙钟时间。

事实来源：

- Multica mission store：mission plan、validation contract、milestone、follow-up feature。
- `gocli-stream-json` stdout：assistant/tool/result/status/usage/handoff。
- `aegis-agent` session id：恢复入口和本地事实索引。
- execenv 写入的 `AGENTS.md` / `skills/`：当前 run 的上下文来源。

禁止：

- Multica 解析 `aegis-agent` 未公开的 session 内部 schema 来维持 mission 状态。
- 浏览器端状态成为 plan、validation、handoff 或 session status 的唯一事实源。
- 默认 Web 首页因为 mission profile 暴露 worker internals、raw queue payload 或 isolation tuning。

## 7. Role-Specific Model Selection

不同角色可以使用不同 provider/model：

- planner / orchestrator：偏慢但推理强，适合范围澄清、里程碑拆分、validation contract。
- generator / worker：偏代码实现与工具使用，适合单 feature 修改。
- evaluator / validator：偏严格遵循验证契约，最好与 generator 使用不同模型族或不同提示策略。

集成规则：

- Multica 负责选择 role-specific model，并把最终 provider/model/thinking 通过 `gocli` backend 参数传给 `aegis-agent`。
- `aegis-agent` 负责把 provider options 写入 session metadata，并由 provider adapter 处理 replay / reasoning / thinking 差异。
- Multica 不应承载 provider-specific replay 逻辑。
- `models --json` 的 `<provider>/<model>` route id 仍是模型发现和执行路由的唯一 MVP 机制；role-specific 选择是在该列表之上做策略选择。

## 8. 协议扩展原则

Mission profile 只使用 additive wire fields。MVP consumer 必须能忽略它们。

可选字段：

- output/input envelope `run_role`：当前 run 的角色 hint，例如 `planner`、`generator`、`evaluator`、`orchestrator`、`worker`、`validator`。
- output/input envelope `metadata`：mission id、feature id、milestone id、parent session id、child/queue linkage 等非 transcript 数据。
- result envelope `handoff`：结构化交接摘要。
- result/input metadata 中的 `validation_contract_ref`：指向 contract artifact 或 mission store id。

约束：

- 不把完整 mission graph 塞进 stdout transcript。
- 不要求 `aegis-agent exec` 在 MVP 解析这些字段才能运行。
- unknown fields 和 unknown content block 必须保持 ignored。
- 如果后续协议需要强语义字段，必须 bump protocol version 或保持 v1 additive 兼容。

## 9. 所属职责

| 能力 | Multica owns | aegis-agent owns |
| --- | --- | --- |
| mission plan / milestone | 是 | 可通过 Goal/Plan Mode 接收摘要 |
| validation contract 主事实 | 是，mission 场景 | standalone 时可在 Goal 内部保存 |
| plan approval UI | 是 | Plan Mode 可作为本地 gate |
| worker execution | 调度子进程 | runtime loop、tools、session facts |
| validator execution | 调度 evaluator run | evaluator role session 与工具事实 |
| structured handoff | mission store 汇总 | session artifacts、events、optional result handoff |
| provider/model role policy | 选择策略 | provider options 持久化与 adapter 映射 |
| Mission Control | 聚合展示 | 提供 stream events、session id、artifacts |

## 10. 非目标

- 不在 `aegis-agent` core runtime 中实现固定 Missions DAG。
- 不要求 Multica 改成 aegis-agent provider adapter。
- 不在 `gocli-stream-json` MVP 中实现 ACP / JSON-RPC。
- 不让 validator 通过读取 worker 的完整聊天上下文来自证；validator 应基于 validation contract、代码、artifact 和可运行行为检查。
- 不把报告、prompt、session、compaction 或 provider view 的脱敏作为默认协议规范。
- 不把 mission profile 变成默认 Web 页面复杂化理由。

## 11. 验收补充

当启用 mission-compatible profile 时，除 MVP 验收外还应验证：

- plan approval 前 validation contract coverage check 有明确通过/失败结果。
- worker result 至少包含 session id、status、usage 和可追溯 handoff artifact。
- validator 未依赖 worker 聊天上下文，也能从 contract/artifact/workdir 复现检查。
- failed validation 会生成 follow-up feature、blocker 或明确的 mission 状态，而不是静默继续。
- feature 级写入默认串行；并行写入必须有 isolation 与 merge 证据。
- role-specific provider/model 选择能在 Multica mission record 与 aegis-agent session metadata 中追溯。
