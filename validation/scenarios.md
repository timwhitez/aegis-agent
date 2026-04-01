# Real Task Validation Scenarios

## 目标

本轮验证要覆盖更接近真实复杂开发、评审、恢复、委派与跨参考实现对照的 26 个场景，而不是只证明单点控制流可用。

同时要显式覆盖最新长任务 harness 实践里最重要的三件事：

- role-aware planner / generator / evaluator 分工，而不是让 child agent 只有名字没有角色约束
- durable handoff artifacts 不只“存在”，还要在实现或验证之后保持新鲜
- child / queue handoff 必须留下 visible output 证据，证明 parent 可以据此恢复
- steer 的 rejection / preemption 不能只停留在单测，必须有 live durable evidence

共享前提：

- 配置文件：`validation/config.openai-compatible.yaml`
- provider：`openai-compatible`
- wire API：`responses`
- model：`gpt-5.4`
- session 根目录：`/root/.go-cli-agent/validation-sessions`
- skills 目录：`skills/` 与 `validation/skills/`

执行纪律：

- 每个场景都要保留实际命令、session id、状态、最终产物路径和问题备注。
- 单个场景失败不能直接中断整轮；后续场景仍要继续执行，并把失败原因单独落盘。
- preflight 需要同时保留轻量 `probe-provider` 结果和至少一个更接近真实首轮 turn 的 repo-scope warmup 结果。
- preflight 还要显式保留 proof-focused unit/integration tests，确保 provider request traceability 与 pre-exec guard 行为在进入 live matrix 前已经闭环。
- 对曾经只停留在 review prose 的 proof-completeness 缺口，优先补脚本层直接证据；当前矩阵会把 RT21 相关的 gap-proof tests 和 `notes/preflight-gap-proof-summary.md` 一并作为后续 audit/readiness 的允许输入。
- live 场景需要有首轮无进展 watchdog；如果在 `provider.call` 后长期没有首个结果，要尽快判失败并进入下一个场景。
- repair 场景不能只信 agent 自报成功；脚本层要补充外部 post-check，例如 `go test ./...` 或 `pytest -q`。
- 所有输入、输出、问题发现都落在本次 run 的独立目录下。
- 任何失败都要区分是 provider 接线、runtime 控制流、tool 行为，还是任务质量/评测口径问题。
- audit / review 产物默认采用 findings-first 结构，并区分 validated findings、remaining risks、unresolved questions。
- compaction / taskboard / continue 这类“强证明”场景，必须优先引用原始 `events.jsonl`、`session.json`、`state.json`、`todo.json`、taskboard before/after JSON 等 durable evidence，而不只引用下游摘要产物。
- 与 Codex / OpenCode 的对照结论必须区分：live go-cli-agent 证据、结构化 same-task comparator、尚未具备 live competitor benchmark 的部分。

## Focused Follow-Up Profiles

除 26 场景主矩阵外，当前还保留一条长期稳定的 focused live 入口：

- 脚本：`validation/run_experimental_webconsole_followup_validation.sh`
- 当前稳定参考 run：`validation/runs/2026-03-27-openai-compatible-gpt-5.4-round54e-experimental-webconsole-followup-stable-proof/`
- 目的：在不重跑整轮 26 场景矩阵时，单独复核 `experimental web` 相关的高价值回归，包括 durable retry restore、embedded shell/assets、真实浏览器交互，以及 queue background notification dedup

该 focused profile 的判定口径：

- retry-resume proof 采用 evidence-first 规则：只要 durable session metadata 仍保留原始 `retry_policy.max_attempts=2`，且 resumed turn 真实写出 `provider.retry`，就视为 retry-drift 修复已被证实
- 若上述 retry proof 已成立，而 bounded finish nudges 之后 session 仍停在 `awaiting_input`，应记录为 non-blocking completion quirk，而不是把 focused rerun 判成失败
- webconsole 部分仍要求 embedded shell / JS / CSS 资产可直接加载，headless browser UI smoke 覆盖 start、continue、worker 更新、queue submit、queue view、queue-links 通知与 manual refresh，且浏览器侧无 `runtime exception` / `console error`
- queue follow-up 仍要求在真实 queue 完成后强制一次 stale-running reconcile，并验证 parent `background.jsonl` 仍只有 2 条通知且 `queue_job_id` 去重成立

## 场景矩阵

### RT01 Core Surface Boundary Audit

- mode: `exec`
- workdir: `go-cli-agent/`
- focus: core v1 默认 help surface、core/experimental/store facade 边界，以及 SDK facade 是否仍保持 core-first 收口
- expected capabilities: 受限检索、findings-first review、core-vs-extension surface 判断
- expected artifact: `artifacts/rt01-core-surface-audit.md`

### RT02 Provider Review And Workspace Safety Audit

- mode: `exec`
- workdir: `go-cli-agent/`
- focus: provider metadata/retry 从 config -> session metadata -> adapter 的全链路可追溯性、review-artifact enforcement、以及 report pre-validation 与真实 file-tool path safety 是否一致，并优先读取当前 run 的 gap-proof tests 和 owning tests 关闭可验证疑点
- expected capabilities: 证据引用、confirmed alignment 与 unresolved question 分离、provider/session traceability 识别、code+test 双证据收口、不把已通过的 gap-proof 测试重新降级成“未证实”
- expected artifact: `artifacts/rt02-provider-review-safety-audit.md`

### RT03 Top-Level Markdown Corpus Synthesis

- mode: `exec`
- workdir: workspace root
- focus: 当前顶层 Markdown 语料中的共性、张力和可迁移架构原则
- expected capabilities: 多文档抽取、跨文档归纳、架构取舍整理
- expected artifact: `artifacts/rt03-top-level-md-synthesis.md`

### RT04 Forced Compaction Proof Drill

- mode: `exec`
- workdir: workspace root
- focus: 低阈值 compaction 下的 artifact / transcript / proof-read 行为，以及 summary 是否保留高价值收敛线索
- expected capabilities: 长上下文整理、compaction 触发、artifact-memory / project-memory-stack 观测
- expected artifact: `artifacts/rt04-forced-compaction-proof.md`

### RT05 Incident Triage And Durable Task Graph

- mode: `run` -> `continue`
- workdir: `validation/workspaces/incident`
- focus: 先建立 durable task graph 与 `reports/` stack，再在 continue 阶段完成真实 incident 调查并回写 before/after 任务状态
- expected capabilities: `todo_write`、`task_create`、`task_update`、`continue`、durable memory 刷新、时间线与根因判断
- expected artifact: `artifacts/rt05-recovery-summary.md`

### RT06 Awaiting Input And Continue On Docset

- mode: `run` -> `continue`
- workdir: `validation/workspaces/docset`
- focus: 自然停顿、继续执行、状态恢复，以及继续阶段刷新 durable project-memory stack
- expected capabilities: `awaiting_input`、用户补充后继续推进、`reports/spec.md|plan.md|progress.md|validation.md`
- expected artifact: `artifacts/rt06-docset-continue-brief.md`

### RT07 Same-Task Enterprise Repair With Steer

- mode: `exec` + `steer`
- workdir: `validation/workspaces/rt07_platform_go`
- focus: 在同一条真实 Go 多 package 修复任务里，同时证明 durable reports/taskboard、两次 interrupt steer、provider.request.prepared、最终 `go test ./...` 通过，以及 proof artifact 与 `finish` 的同轮收口
- expected capabilities: `todo_write`、`task_create`、`task_update`、受限读取下的跨 package 根因修复、session 管理、steer 接纳、finish-path 收敛、event-log-backed same-task proof
- expected artifact: `artifacts/rt07-live-steer-audit.md`

### RT08 Foreground Delegated Review With Reviewer Handoff

- mode: `exec` + `experimental delegate`
- workdir: `validation/workspaces/platform_go`
- focus: parent 先写 reviewer handoff 的 `reports/spec.md` / `reports/plan.md`，child reviewer 在 isolation workdir 内消费这些上下文，并把 review + validation 结果通过 visible outputs 回流
- expected capabilities: delegation、child session durability、artifact 回流、reviewer role guidance、visible_paths、skeptical evaluation
- expected artifact: `artifacts/rt08-delegate-review.md`

### RT09 Background Queue Review And Parent Notification

- mode: `run` + `experimental queue submit/worker` + `continue`
- workdir: `validation/workspaces/platform_py`
- focus: queued reviewer child 消费 parent 准备的 durable handoff stack，刷新 `reports/progress.md` / `reports/validation.md`，并把 visible output 列表随 background notification 回流给 parent
- expected capabilities: queue submit、worker 消费、background notification、reviewer role guidance、handoff freshness、parent/child evidence 回收
- expected artifact: `artifacts/rt09-background-summary.md`

### RT10 Nested AGENTS API Review

- mode: `exec`
- workdir: `validation/workspaces/nested_review/services/api`
- focus: 深层 `AGENTS.md` 继承、scope discipline、review 结论，以及脚本层 `go test ./...` post-check
- expected capabilities: 作用域解析、findings-first review、只引用 `services/api/` 证据
- expected artifact: `artifacts/rt10-api-review.md`

### RT11 Platform Go Multi-Package Repair

- mode: `run` -> `continue`
- workdir: `validation/workspaces/platform_go`
- focus: 真实多 package Go 修复，两阶段诊断/实现，以及脚本层 `go test ./...` 外部验证
- expected capabilities: 先跑最窄测试、durable plan、跨 package 修复、验证回归
- expected artifact: `artifacts/rt11-platform-go-change-summary.md`

### RT12 Platform Go Post-Fix Review

- mode: `exec`
- workdir: `validation/workspaces/platform_go`
- focus: 对 RT11 修复结果做 findings-first post-fix review
- expected capabilities: 回看 change summary、代码与测试复核、remaining risks 管理
- expected artifact: `artifacts/rt12-platform-go-review.md`

### RT13 Platform Python Multi-Module Repair

- mode: `run` -> `continue`
- workdir: `validation/workspaces/platform_py`
- focus: 真实多模块 Python 修复，两阶段诊断/实现，以及脚本层 `pytest -q` 外部验证
- expected capabilities: 最窄 pytest 先行、durable plan、跨模块修复、验证回归
- expected artifact: `artifacts/rt13-platform-py-change-summary.md`

### RT14 Platform Python Post-Fix Review

- mode: `exec`
- workdir: `validation/workspaces/platform_py`
- focus: 对 RT13 修复结果做 findings-first post-fix review
- expected capabilities: 代码与测试复核、remaining risks 管理、无 findings 时明确写出
- expected artifact: `artifacts/rt14-platform-py-review.md`

### RT15 Task And Durable Memory Traceability

- mode: `exec`
- workdir: workspace root
- focus: 直接引用当前 run 的 `events.jsonl`、`session.json`、`state.json`、`todo.json`、taskboard before/after JSON，以及 RT07 同任务修复 trace，证明 continue / compaction / taskboard / same-task durable execution 的行为
- expected capabilities: 原始 evidence 精读、snippet-backed 直证、taskboard before/after 对照、remaining gaps 管理、在 `required proof anchors` 段落里逐字写出 `compact.started`、`compact.finished`、`rt05-incident-taskboard-before.json`、`rt05-incident-taskboard-after.json`
- expected artifact: `artifacts/rt15-task-memory-traceability.md`

### RT16 Codex Steer And Sandbox Audit

- mode: `exec`
- workdir: workspace root
- focus: `codex` 的 steer / interrupt / sandbox 语义提炼
- expected capabilities: 跨目录限定阅读、参考实现归纳、可迁移 caution 与 hardening ideas 提炼
- expected artifact: `artifacts/rt16-codex-steer-audit.md`

### RT17 Codex Responses Proxy Audit

- mode: `exec`
- workdir: workspace root
- focus: `codex` 的 Responses proxy、认证与本地 hardening 取舍
- expected capabilities: transport contract 审核、auth/hardening 提炼、mismatch risk 管理
- expected artifact: `artifacts/rt17-codex-proxy-audit.md`

### RT18 OpenCode Task And Prompt Review

- mode: `exec`
- workdir: workspace root
- focus: `opencode` 的大仓库 task discipline、prompt/reminder 行为与执行取舍
- expected capabilities: 大仓库局部审计、task discipline 对照、tradeoff/caution 提炼
- expected artifact: `artifacts/rt18-opencode-task-review.md`

### RT19 OpenCode Responses Provider Audit

- mode: `exec`
- workdir: workspace root
- focus: `opencode` 的 Responses adapter、replay、finish-reason 与 tool preparation
- expected capabilities: replay/tool mapping 审核、mismatch risk 与 useful ideas 提炼
- expected artifact: `artifacts/rt19-opencode-responses-audit.md`

### RT20 Same-Task Comparator

- mode: `exec`
- workdir: workspace root
- focus: 用 go-cli-agent 当前 live run 的同任务修复、taskboard、steer 与 post-check 证据，对照本地 Codex / OpenCode 参考实现，做 structured same-task comparator
- expected capabilities: 同维度对照、live evidence 与 reference implementation 分层、明确“不等于 live competitor benchmark”、artifact 的首个非标题 section 必须是精确标题 `## comparator setup`，并以独立正文行逐字写出 `This is not a live competitor benchmark.` 与 `This is a same-task comparator built from go-cli-agent live run evidence plus local codex/opencode reference implementations.`，不得改写或写成 bullet
- expected artifact: `artifacts/rt20-same-task-comparator.md`

### RT21 Large Project Readiness Scorecard

- mode: `exec`
- workdir: workspace root
- focus: 基于本轮 live artifacts、proof-focused preflight tests、direct evidence、same-task comparator、operator-facing OpenAI-compatible guidance 与参考实现，判断 go-cli-agent 是否已实质关闭上一轮 blocker，并把 planner/evaluator handoff、visible output、durable memory freshness 纳入 readiness 口径
- expected capabilities: 跨场景证据综合、scorecard 打分、blocker 命名、validated vs comparator vs unproven 分层、role-aware handoff 判断、无直接 blocker 时在 findings 中写出精确句子 `No validated findings.`
- expected artifact: `artifacts/rt21-large-project-readiness.md`

### RT22 Exact Template Artifact Guard

- mode: `exec`
- workdir: workspace root
- focus: 真实 review/audit 任务里的 exact-template 开头、section 顺序与 findings-first 默认习惯冲突时，runtime 是否能把模板要求执行到底
- expected capabilities: literal opening block 保真、first-section ordering、review artifact 结构与 exact-template 约束并存
- expected artifact: `artifacts/rt22-exact-template-audit.md`

### RT23 Explicit Role Persistence And Handoff

- mode: `exec` + `experimental delegate`
- workdir: `validation/workspaces/platform_go`
- focus: `--role evaluator` 这类显式 role 是否会进入 child session metadata、provider request metadata、background/session evidence，并与 parent handoff 文件形成可追溯闭环
- expected capabilities: role-aware child execution、role metadata durability、delegated review handoff、visible artifact 回流
- expected artifact: `artifacts/rt23-role-review.md`

### RT24 Steer-Triggered Spec And Plan Refresh

- mode: `exec` + `steer`
- workdir: `validation/workspaces/docset`
- focus: 大任务在中途被 interrupt steer 改变方向后，runtime 是否会迫使 `reports/spec.md` / `reports/plan.md` 先刷新，再继续 drafting 和 finish
- expected capabilities: steer 接纳、scope-change freshness guard、spec/plan refresh、same-session recoverable drafting
- expected artifact: `artifacts/rt24-steer-refresh-brief.md`

### RT25 Oversized Steer Rejection

- mode: `exec` + invalid `steer --json`
- workdir: `validation/workspaces/docset`
- focus: 当 session 真实处于 running 且 provider turn 已发出时，超长 steer 输入是否会在 queue 之前被直接拒绝，并给出结构化 JSON 错误
- expected capabilities: `steer --json` 错误结构化输出、`steer_input_too_large`、durable event/state 维持未入队
- expected artifact: `artifacts/rt25-steer-rejection.md`

### RT26 Provider Cancel And Interrupt Preemption

- mode: delayed `exec` + valid `steer --interrupt`
- workdir: `validation/workspaces/docset`
- focus: 当 provider HTTP turn 被本地 delay proxy 故意挂住时，valid interrupt steer 是否会触发真实 `provider.cancelled` durable event、下一 turn 接纳 steer，并留下 transport-side cancellation evidence
- expected capabilities: `provider.cancelled`、`session.steer.accepted`、interrupt preemption、transport-level cancellation proof、same-session finish
- expected artifact: `artifacts/rt26-provider-cancel-proof.md`
