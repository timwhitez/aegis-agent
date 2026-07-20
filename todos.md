# Context Compaction And Harness Convergence Todo

目标：按可审查、可回滚的提交顺序，完整关闭 `issues.md` 中 9 项 context compaction、tool output、history recovery、explorer 与 telemetry 问题。

规划基线：

- Runtime 审计目标：`60415f935c872d292909c3125f84738c31631c21`。
- 文档复核起点：`b15ccbb671c8e340693830aae4d22cf673d4a091`。
- 计划生成日期：2026-07-20。
- 当前 profile：Web-first v1 收敛；HARNESS-001 / OBS-001 只进入 large-project / advanced surface。

## 使用规则

- 按本文依赖顺序执行。后置任务不得通过复制一套临时逻辑绕过前置公共设施。
- 每个实现任务都先修改列出的 spec，再修改代码；spec、实现、永久测试和必要的 Web/CLI 配套进入同一个提交。
- 顶层 checkbox 只有在该任务全部验收项通过、验证命令成功且产生真实 commit 后才能勾选。仅完成代码、仅通过聚焦测试或仅写文档都不能关闭任务。
- 每个提交只暂存该任务涉及的文件。不得暂存、覆盖或删除用户已有的 `.gitignore`、`AGENTS.md`、本地二进制 `go-cli-agent`、已删除的 `multica-plan.md` 或其他无关未跟踪内容。
- `messages.jsonl`、`events.jsonl`、session/state/task/goal 文件继续是事实源；所有压缩、去重、pointer 与 history view 只能改变 provider view 或新增派生 artifact。
- provider wire 差异只在 adapter 层实现。Web、CLI、tool registry 不得维护 OpenAI / Anthropic / Google replay 编码副本。
- explorer 是否使用继续由模型决定。不得加入自动拆分、固定 DAG、强制等待、强制委派或 task-specific runtime workflow。
- 默认 Web 首页保持简洁。高级 role、预算和 context 指标只进入 Settings 的现有 role 配置区、session detail 折叠区、SDK/JSON 或显式 advanced CLI。
- 本计划不要求 Docker，也不得在当前机器启动 Docker 作为验收手段。
- 若实施过程中发现 `issues.md` 的事实已被更早提交改变，先更新 issue evidence/acceptance 与本任务依赖，再继续；不得让 spec、issue 和代码分叉。

## 问题覆盖与关闭任务

| Issue | 关闭任务 | 最终关闭证据 |
| --- | --- | --- |
| CTX-001 | CTX-001A、CTX-001B | 有损路径先移除；安全 result-level 去重通过三 provider replay 与 durable-log 测试 |
| CTX-002 | CTX-002 | 独立结果数量/字节窗口、同 batch 混合压缩与 replay 测试 |
| CTX-003 | CTX-003A、CTX-003B | main/semantic-summary 统一预算预检、有界收缩、typed local error |
| TOOL-001 | TOOL-001 | 有界流式采集、当前轮 artifact、quota、取消/超时回归 |
| TOOL-002 | TOOL-002A、TOOL-002B | hook 后通用 cap、安全 byte continuation、grep 双预算 |
| TOOL-003 | TOOL-003 | `limit+1`、集合完整性 metadata 与边界矩阵 |
| CTX-004 | CTX-004 | current-session-only canonical history 分页与超长 record byte paging |
| HARNESS-001 | HARNESS-001 | explorer role、双层 allowlist、role options、短 handoff 与 Web Settings round-trip |
| OBS-001 | OBS-001 | 共用预算快照、lineage 聚合、确定性三结构 fixture |

## 全局不可回退边界

- [ ] 原始 session 日志不会被 provider-view 变换覆盖；所有测试都对落盘前后 hash 或逐字段内容做断言。
- [ ] 最新外部用户指令、最新 steer 约束和合法 tool-call/tool-result replay 配对不会被 hard-fit 静默删除。
- [ ] workspace/path/symlink escape、session ownership、owner-only mode、shell timeout、最小环境 allowlist、sandbox 与进程组取消保持现状或更严格。
- [ ] 每个模型可见结果和每个 provider request 都有明确的数量/字节/估算 token 边界、stop reason、continuation 或 typed failure。
- [ ] 预算观测与预算拒绝使用同一 `RequestBudgetSnapshot`；不接受“事件显示 fit，但发送路径用另一套公式”的实现。
- [ ] fake provider、OpenAI、Anthropic、Google adapter 和 Web/CLI/SDK 对新增字段均有兼容回归；旧配置不写新字段时保持有效。

---

## Phase 0 — 固定基线与止血

### [x] BASE-001 — 固定可重复基线与工作树边界

- Issue：全部任务的前置 gate。
- Priority：P0 gate。
- Depends on：无。

Scope：

- 在任何实现前完整阅读 `spec/00-product.md`、`spec/01-runtime-architecture.md`、`spec/03-provider-contracts.md`、`spec/09-phase-plan.md`、`spec/11-spec-audit-and-traceability.md`、`spec/12-task-system.md`、`spec/13-live-input-and-steering.md`；再按当前任务补读其列出的其他 spec。
- 记录开始实施时的 `git rev-parse HEAD`、`git status --short` 和 Go/Node 版本；确认无关 dirty state 与本计划顶部一致或明确补记差异。
- 运行当前 compaction、tool output、Store pagination、role override 和 provider request event 聚焦测试，区分“基线原有失败”与后续回归。
- 为后续 fixture 预留稳定的临时 session/workspace；测试只能使用 `t.TempDir()` 或 validation 自有目录，不读取用户真实 session。

Spec changes：无。该任务只建立执行证据。

Implementation files：无。

Permanent tests：无新增；只执行现有测试。

Acceptance checklist：

- [x] 必读 spec 已逐份阅读，未发现阻断 Phase 0 开始实施的既有冲突；后续仍按任务先同步专属 spec。
- [x] HEAD、dirty path 清单、工具链版本和基线命令结果已写入本任务 Completion record；未记录 secret、prompt 正文或 provider credential。
- [x] 基线聚焦测试无失败；后续出现同名失败时以本记录区分计划改动回归。
- [x] `git diff --check` 在开始实施时通过。

Validation：

```bash
git rev-parse HEAD
git status --short
go version
node --version
go test ./internal/runtime ./internal/tools ./internal/session ./internal/provider \
  -run 'TestDeduplicateToolResults|TestEngineEmitsProviderRequestPreparedEvent|TestGlobHonorsLimitAndReportsTruncation|TestGrepTruncatesLongMatchingLines|TestReadFileCapsLargeRequestsAndAnnotatesWindow|TestStoreLoadMessagesTailAndBeforeKeepBoundedWindows|TestRunnerDelegateAppliesRoleProviderOverrideWhenProviderModelOmitted' \
  -count=1 -timeout=120s
git diff --check
```

Non-goals：

- 不修代码，不清理工作树，不生成部署 artifact。

Commit subject：N/A（执行 gate，不单独提交）。

Completion record：

- HEAD：`242ba195659a500174de030cb25476e20e565355`
- Dirty paths：用户既有 `M .gitignore`、`M AGENTS.md`、`M go-cli-agent`、`D multica-plan.md`，以及未跟踪 `.tmp_multica_repo_preflight_edit/`、`AGENTS.md.bak`、`CLAUDE.md`、`refer_prompts/`、`skills/`、`ss.png`、`usable_skills/`、`workspace/`；后续仅暂存当前任务列出的文件。
- Toolchain：`go version go1.24.5 linux/amd64`；`node v22.22.2`。
- Validation：2026-07-20 运行计划列出的 runtime/tools/session/provider 聚焦测试全部通过（provider 包无匹配测试）；`git diff --check` 通过。

### [x] CTX-001A — 立即移除有损通用 tool-result 去重路径

- Issue：CTX-001。
- Priority：P0 stop-loss。
- Depends on：BASE-001。

Scope：

- 从每轮 `compactor.build` provider-view 路径中移除 `deduplicateToolResults` 调用。
- 删除或隔离 `detectDuplicateToolCalls`、过时 `toolCallKey`、message-level 覆盖逻辑和使用 `file_path` 的误导测试，确保没有隐藏入口继续调用它们。
- 保留现有 result micro-compaction；本任务不等待安全去重重实现。
- 增加负向回归：相同参数但结果变化、multi-call batch 中一项表面重复、真实 `read_file.path` schema，provider view 均不得丢失唯一结果。

Spec changes：

- `spec/10-context-compaction.md`：明确 Phase 0 stop-loss 期间不做语义去重，旧结果只受 result-level micro-compaction 与 hard-fit 管理。
- `spec/11-spec-audit-and-traceability.md`：登记 CTX-001A 的 correctness gate 和永久测试。
- `spec/09-phase-plan.md`：把 stop-loss 放入 Phase 10 收敛，不等待 advanced harness 工作。

Implementation files：

- `internal/runtime/compaction.go`
- `internal/runtime/compaction_test.go`
- `internal/provider/provider_test.go`

Permanent tests：

- 真实 `{"path":"..."}` read_file 参数不会触发任何未验证折叠。
- 同参数、不同结果的两次读取都保留。
- 一条 tool message 含多个 ToolResult 时，任一重复候选都不会覆盖同批其他结果。
- OpenAI / Anthropic / Google tool replay fixture 在 stop-loss 后仍合法。
- `messages.jsonl` 内容在构造 provider view 前后完全一致。

Acceptance checklist：

- [x] 每轮 provider view 已不再执行当前 message-level dedup。
- [x] 过时 `file_path` 契约与旧测试已移除，没有以改成 `path` 的方式继续启用危险逻辑。
- [x] micro-compaction、ephemeral pointer 和 full compaction 路径保持启用并由 runtime 全包回归覆盖。
- [x] 上述永久测试通过并纳入本任务独立提交。

Validation：

```bash
go test ./internal/runtime -run 'Test.*(Dedup|Duplicate|MultiCall|ProviderView|Replay|Durable).*' -count=1 -timeout=120s
go test ./internal/runtime ./internal/provider -count=1 -timeout=180s
gofmt -l internal/runtime
git diff --check
```

Non-goals：

- 不在该提交实现 canonical fingerprint、result hash 或 benchmark 驱动的重新启用。
- 不改变 durable message schema。

Commit subject：`fix(runtime): disable unsafe tool result deduplication`

Completion record：

- Commit：本任务提交 `fix(runtime): disable unsafe tool result deduplication`。
- Tests：TDD 红测确认旧实现会覆盖相同参数的旧结果和 multi-call sibling；实现后聚焦 stop-loss/provider replay、计划正则测试及 `go test ./internal/runtime ./internal/provider -count=1 -timeout=180s` 全部通过；`gofmt -l` 与 `git diff --check` 无输出。
- CTX-001 remaining work：`CTX-001B pending`

### [x] CTX-003A — 建立共享 request budget 快照与 fail-closed 预检

- Issue：CTX-003、OBS-001 的公共基础。
- Priority：P0 correctness。
- Depends on：CTX-001A。

Scope：

- 定义单一 `RequestBudgetSnapshot`，覆盖 `request_kind`、session/turn correlation、provider/model、system、messages、tools、metadata、provider wire envelope、estimated input、reserved output、safety headroom、effective window、headroom、compaction action/summary id 与 fit 结果。
- 将 OpenAI、Anthropic、Google 的请求 body 构造提取为 adapter 内部可复用函数；预算估算必须基于同一 wire body，不在 runtime 复制 provider JSON。
- 给 fake adapter 提供确定性 estimator。对第三方/测试 adapter 明确定义 estimator 缺失策略；生产路径不得因为缺 estimator 静默跳过 preflight。
- 在任何主 `adapter.RunTurn` 前做最终预检。估算已超窗时返回 typed `request_budget_exceeded` local error，不发 provider 请求，不进入 transport retry/auto-resume。
- 把 `semanticSummaryFunc` 的辅助请求纳入相同入口，使用 `request_kind=semantic_summary`。辅助请求不 fit、timeout 或失败时记录原因并回退确定性 summary，不让主 session 失败。
- 将 `provider.call` 事件移动到预算通过之后；拒绝路径写 `provider.request.prepared`（`fit=false`）和明确的 rejected/budget event，不能伪造一次已发送调用。
- 第一阶段只提供 fail-closed 边界；自动 pointerize/缩尾留给 CTX-003B。

Spec changes：

- `spec/01-runtime-architecture.md`：增加 provider request budgeting 边界与 typed local failure。
- `spec/02-cli-and-config.md`：定义 effective window、output reserve、safety headroom 和旧配置兼容。
- `spec/03-provider-contracts.md`：定义 adapter wire estimator 与 `request_kind`。
- `spec/07-testing-strategy.md`：增加 main/semantic-summary preflight 和 estimator/wire parity fixture。
- `spec/09-phase-plan.md`：登记 Phase 10 hard-fit A/B 两阶段。
- `spec/10-context-compaction.md`：把 compaction trigger 与最终 request hard-fit 明确分开。
- `spec/11-spec-audit-and-traceability.md`：登记 CTX-003A traceability。

Implementation files：

- `internal/provider/types.go`
- `internal/provider/openai.go`
- `internal/provider/anthropic.go`
- `internal/provider/google.go`
- `internal/provider/fake.go`
- `internal/provider/provider_test.go`
- `internal/runtime/engine.go`
- `internal/runtime/request_budget.go`、`internal/runtime/request_budget_test.go`
- `internal/runtime/engine_test.go`
- `internal/runtime/provider_attempts.go`
- `internal/runtime/runner.go`、`internal/runtime/runner_test.go`
- 如引入新配置：`internal/config/config.go`、`internal/config/config_test.go`

Permanent tests：

- 三个生产 adapter 的 estimator 与真正发送 body 使用同一 builder；fixture 对字段/序列化尺寸一致性做断言。
- system/messages/tools/metadata/provider envelope/output reserve 各自单独把请求推过边界时，本地拒绝且 fake/HTTP server 调用次数为 0。
- 刚好等于边界可发送，超过 1 个预算单位拒绝；负值、零值、未知 context window 与 config override 有确定语义。
- main 请求和 semantic-summary 请求都有 snapshot；summary 不 fit 时确定性 compaction 仍成功。
- provider-owned timeout、child budget cancel、manual stop 的既有分类不被 typed local budget error污染。
- snapshot 不包含 prompt/tool 原文或 credential，只含尺寸、计数、ID 和已存在 provider options。

Acceptance checklist：

- [x] main、semantic-summary、probe 三个生产 `adapter.RunTurn` 调用点都先经过同一 preflight；缺 estimator 同样 fail closed。
- [x] 已知估算超窗的 main request 不触达 fake/HTTP provider，也不写 `provider.call` 或进入 transport retry/auto-resume。
- [x] semantic-summary 超预算只记录 rejected/budget 事实并将 `semantic_summary_status` 置为 `failed`，确定性 compaction 继续成功。
- [x] `provider.request.prepared`、budget reject 和带 usage 的 `turn.stopped` 通过 session、turn、request_id、request_kind 关联。
- [x] OpenAI、Anthropic、Google wire 差异仅留在各 adapter 的共享 body builder；Web/CLI/tool 层没有 provider JSON 副本。
- [x] `RequestBudgetSnapshot` schema version 为 1，旧 shape/未知字段读取兼容测试通过。

Validation：

```bash
go test ./internal/provider ./internal/runtime ./internal/config \
  -run 'Test.*(RequestBudget|WireEstimate|HardFit|SemanticSummary|ProviderRequestPrepared).*' \
  -count=1 -timeout=180s
go test ./internal/provider ./internal/runtime ./internal/config -count=1 -timeout=240s
gofmt -l internal/provider internal/runtime internal/config
git diff --check
```

Non-goals：

- 不在本任务实现精确 tokenizer、provider 原生 compaction 或自动委派。
- 不在超预算时无限循环重试或发送一次“碰碰运气”。

Commit subject：`feat(runtime): fail closed on oversized provider requests`

Completion record：

- Commit：本任务提交 `feat(runtime): fail closed on oversized provider requests`。
- Snapshot schema version：`1`；wire estimate schema version 同为 `1`。
- Tests：TDD 首轮因 estimator/snapshot/preflight 尚不存在而编译失败；实现后计划聚焦测试、wire body 逐字节 parity、main/semantic/probe 零调用拒绝、边界/旧配置/内容不落 snapshot、provider timeout/child budget/manual interrupt 分类回归，以及 `go test ./internal/provider ./internal/runtime ./internal/config -count=1 -timeout=240s` 全部通过；`gofmt -l` 与 scoped `git diff --check` 无输出。
- CTX-003 remaining work：`none`；CTX-003B 已完成并关闭该 issue。

### [x] TOOL-003 — 让 grep / grep_files 集合截断可观测

- Issue：TOOL-003。
- Priority：P1，前置低风险基础。
- Depends on：CTX-003A。

Scope：

- 让 `grep` 与 `grep_files` 像 `glob` 一样收集 `effective_limit + 1`，只在真实 overflow 时 `has_more=true`。
- 统一 metadata：`returned_count`、`requested_limit`、`effective_limit`、`has_more`、`limit_capped`、`truncated_snippet_count`。
- 把 snippet 截断与 result-set overflow 分开；`truncated_snippet_count>0` 不得自动推导 `has_more=true`。
- 模型可见文案在真实 overflow 时提示缩小 `path/include/pattern`。本任务不提前设计 byte continuation；TOOL-002B 再补 stop reason/cursor。
- 保持 deterministic ordering，确保后续 cursor 可建立在稳定顺序上。

Spec changes：

- `spec/04-tools-and-skills.md`：补 grep/grep_files 集合完整性字段与 exact-limit 语义。
- `spec/07-testing-strategy.md`：登记边界矩阵。
- `spec/11-spec-audit-and-traceability.md`：登记 TOOL-003。

Implementation files：

- `internal/tools/registry.go`
- `internal/tools/registry_test.go`

Permanent tests：

- grep 与 grep_files 分别覆盖 0、limit-1、limit、limit+1。
- 请求 limit 超过 cap 时报告 requested/effective/limit_capped。
- snippet 被截短但集合完整时 `has_more=false`。
- include、多目录、UTF-8 snippet 与重复运行排序一致。

Acceptance checklist：

- [x] exact-limit 无 overflow 不误报。
- [x] true overflow 返回可机读 metadata 和模型可见 narrowing 提示。
- [x] `truncated` 旧字段若保留，兼容语义已在 spec 写明；新代码不再让一个布尔值同时代表两种截断。
- [x] glob 现有行为不回退。

Validation：

```bash
go test ./internal/tools \
  -run 'Test(Grep|GrepFiles|Glob).*(Limit|Overflow|Truncat|Ordering|UTF8)' \
  -count=1 -timeout=120s
gofmt -l internal/tools
git diff --check
```

Non-goals：

- 不提高默认 limit，不返回无限结果，不在本任务引入 repository snapshot。

Commit subject：`fix(tools): report grep result set overflow`

Completion record：

- Commit：本任务提交 `fix(tools): report grep result set overflow`。
- Tests：TDD 红测确认旧实现缺少集合 metadata、在 exact-limit 提前停止且无法识别 true overflow；实现后 0/limit-1/limit/limit+1、cap、UTF-8 snippet、overflow sentinel、include/多目录/重复排序与 glob exact-limit 回归全部通过，`go test ./internal/tools -count=1 -timeout=180s` 通过；`gofmt -l` 与 scoped `git diff --check` 无输出。
- TOOL-003 status：`complete`；TOOL-002B 后续补 byte budget、stop reason 与 continuation cursor。

---

## Phase 1 — 收敛工具结果边界

### [x] TOOL-002A — 建立 hook 后通用 ToolResult 预算与 session artifact quota

- Issue：TOOL-002、TOOL-001 的公共基础。
- Priority：P1 correctness/resource bound。
- Depends on：TOOL-003。

Scope：

- 新增统一 tool-output policy。建议 v1 配置面为 `runtime.tool_output`，至少包含 `llm_output_max_bytes`、`display_output_max_bytes`、`artifact_file_max_bytes`、`artifact_session_max_bytes`、`artifact_max_files`；默认值、上下限和旧配置兼容必须写入 spec/test。
- 在 `tool.after` hook 完成、ToolCallID/Name 补齐之后，且在 `tool.after` event 与 `messages.jsonl` 持久化之前，执行单一 `finalizeToolResultForContext`。任何 hook、skill command、child handoff 或内置工具都不能绕过。
- 对已经带有精确、可恢复 source cursor/artifact 的结果，保留短 preview 与已有 pointer；对动态且不可重建的大结果，将完整可保存部分写入当前 session 的 `artifacts/tool-outputs/`，再返回 pointer。
- 在 session Store 层提供 owner-only、no-symlink、quota-aware 的 artifact writer/lease。并发结果必须原子计数，进程重启后能从目录事实重建已用文件数/字节数，不能只靠内存 counter。
- 统一 metadata 至少包括：`raw_bytes`、`persisted_bytes`、`inline_bytes`、`omitted_bytes`、`artifact_path`、`artifact_complete`、`artifact_truncated`、`budget_reason`。
- artifact 写入失败或 quota 拒绝时，结果必须明确 `recoverable=false` 与错误原因；不得标注 Full output。
- 同步 `agent_spawn` / `agent_status` 的 `final_text` 经过通用结果预算；后台 notification 注入 parent 前使用相同 handoff budget，并保留 child/session reference。

Spec changes：

- `spec/01-runtime-architecture.md`：定义 registry result、hook result、finalized durable ToolResult 的顺序。
- `spec/02-cli-and-config.md`：定义 `runtime.tool_output` 配置、默认值和 normalize 语义。
- `spec/04-tools-and-skills.md`：定义所有模型可见结果的统一 byte cap、pointer 与 metadata。
- `spec/07-testing-strategy.md`：增加 hook amplification、quota/restart/concurrency、child handoff 测试。
- `spec/10-context-compaction.md`：区分 current-result cap、old-result micro-compaction 和 full compaction。
- `spec/11-spec-audit-and-traceability.md`：登记 TOOL-002A。

Implementation files：

- `internal/config/config.go`
- `internal/config/config_test.go`
- `internal/session/store.go`
- `internal/session/store_test.go`
- 新文件建议：`internal/session/tool_output_artifact.go`、`internal/session/tool_output_artifact_test.go`
- `internal/runtime/engine.go`
- 新文件建议：`internal/runtime/tool_result_budget.go`、`internal/runtime/tool_result_budget_test.go`
- `internal/runtime/delegation.go`
- `internal/runtime/delegation_test.go`
- `internal/tools/registry_test.go`

Permanent tests：

- 内置工具返回超过 cap 的结果会在 hook 后被收敛；hook 把 1 KiB 放大到数 MiB 时也不能绕过。
- artifact writer 覆盖单文件 quota、session 总字节 quota、文件数 quota、并发竞争、重启重建、磁盘错误、symlink alias 和 owner-only mode。
- dynamic result 有完整 artifact 时 pointer 可由当前 session `read_file` 精确读取；quota 截断/写失败时 metadata 与实际字节一致。
- multi-call batch 中每个 result 独立预算，ToolCallID/Name/IsError/Final 和其他 metadata 不串位。
- 同步/后台 child 超长 final_text 不整段进入 parent，parent 仍拿到 status、child/session id、artifact/continuation reference。

Acceptance checklist：

- [x] 所有模型可见 ToolResult 都经过 hook 后 finalizer；代码中不存在第二条直接 append 大结果的路径。
- [x] owner-only、no-symlink 与 quota 在 Store 层统一实现，TOOL-001 可直接复用。
- [x] `messages.jsonl` 持久化的是有界结果/pointer；原始动态内容只进入有 quota 的 owner-only artifact。
- [x] DisplayOutput 的上限与 UI 展示语义已明确，不让 provider cap 变成 session/UI 内存旁路。
- [x] child handoff 与 background notification 有相同尺寸边界。

Validation：

```bash
go test ./internal/config ./internal/session ./internal/runtime ./internal/tools \
  -run 'Test.*(ToolResultBudget|ToolOutputArtifact|ArtifactQuota|Hook.*Output|Handoff.*Budget).*' \
  -count=1 -timeout=180s
CGO_ENABLED=1 go test -race ./internal/session ./internal/runtime ./internal/tools \
  -run 'Test.*(ToolOutputArtifact|ArtifactQuota|ToolResultBudget).*' \
  -count=1 -timeout=240s
gofmt -l internal/config internal/session internal/runtime internal/tools
git diff --check
```

Non-goals：

- 不把 session artifact 目录开放给 glob/grep discovery。
- 不把大结果改写成 workspace 普通文件，不把 quota 只放在 Web 层。

Commit subject：`feat(runtime): bound model visible tool results`

Completion record：

- Commit：本任务提交 `feat(runtime): bound model visible tool results`。
- Effective defaults：`runtime.tool_output.llm_output_max_bytes=32768`、`display_output_max_bytes=131072`、`artifact_file_max_bytes=16777216`、`artifact_session_max_bytes=134217728`、`artifact_max_files=256`；正值按 spec 的五组上下限 clamp，旧配置省略或非正值回落默认。
- Race/tests：TDD 红测先证明缺少配置、Store quota writer、hook 后 finalizer 与 child/background handoff budget；审计补充红测还捕获了 runner plan-input recovery 直接落盘 8507-byte 结果、old-result 重复写/错误重标 partial artifact、宽松 session mode 下 artifact root 为 `0755`、伪造 version metadata 绕过 3023-byte cap。实现后聚焦命令、`go test ./internal/config ./internal/session ./internal/runtime ./internal/tools -count=1 -timeout=300s` 与 `go test ./... -count=1 -timeout=600s` 全通过；`CGO_ENABLED=1 go test -race ./internal/session ./internal/runtime ./internal/tools -run 'Test.*(ToolOutputArtifact|ArtifactQuota|ToolResultBudget).*' -count=1 -timeout=240s` 通过。
- TOOL-002 remaining work：`complete via TOOL-002B`

### [x] TOOL-002B — 为 read_file 与搜索结果增加可恢复 byte continuation

- Issue：TOOL-002，扩展 TOOL-003。
- Priority：P1 context efficiency。
- Depends on：TOOL-002A、TOOL-003。

Scope：

- 为 `read_file` 增加 byte mode，输入字段使用明确的 `byte_offset` / `byte_limit`；与现有 line `offset` / `limit` 互斥。默认调用仍保持 120 行 line mode。
- byte mode 必须直接复用 `fileutil.ReadRegularFileRangeNoSymlink`，不能先用 `ReadRegularFileNoSymlink` 全量读取再切片。
- 定义 UTF-8 边界：工具返回 requested/effective byte range、是否调整边界、`returned_bytes`、`total_bytes`、`has_more`、`next_byte_offset`。使用工具返回 cursor 连续翻页时不得丢字节或重复 rune；非 UTF-8 文件保持现有 text/binary policy并给出稳定错误或编码标记。
- `grep` 同时受 match count 和总输出字节限制，在完整 match record 边界停止；返回 `stop_reason=match_limit|byte_limit|complete`、match span/byte offset 与 `has_more`。
- 给 `grep` / `grep_files` 增加与 canonical search args 绑定的稳定 cursor（至少含最后 path/line 或 path、参数 fingerprint 与版本字段）。cursor 与新参数不匹配时拒绝，不静默从错误位置继续。
- `glob`、grep_files 和其他可重建路径列表即使被 TOOL-002A 兜底，也应优先返回 source cursor，而不是为静态候选集写 artifact。
- 所有 preview、header、cursor metadata 都计入最终 ToolResult budget，超限时继续缩短 snippet/record 数而不是截断 JSON cursor。

Spec changes：

- `spec/04-tools-and-skills.md`：定义 read_file line/byte 模式、UTF-8、grep 双预算、cursor schema/version。
- `spec/07-testing-strategy.md`：增加 minified/JSONL/UTF-8/long-path/cursor matrix。
- `spec/10-context-compaction.md`：说明 source-recoverable payload 优先 cursor，不复制全文 artifact。
- `spec/11-spec-audit-and-traceability.md`：登记 TOOL-002B。

Implementation files：

- `internal/fileutil/safe.go`（只在现有 range API 缺 metadata 时做最小扩展）
- `internal/fileutil/safe_test.go`
- `internal/tools/registry.go`
- `internal/tools/read_file_byte.go`
- 新文件建议：`internal/tools/search_cursor.go`、`internal/tools/search_cursor_test.go`
- `internal/tools/registry_test.go`
- `internal/runtime/tool_result_budget_test.go`

Permanent tests：

- 16 MiB 单行文件、minified JS、超长 JSONL、无换行日志不会整行进入 LLMOutput；byte cursor 可完整分页重组原文。
- 多字节 UTF-8 rune 恰好跨页、用户给出 rune 中间 offset、空文件、末尾 offset、超过 EOF、零/负 limit 都有确定结果。
- symlink file、symlink parent、workspace escape、skill root、session artifact exact path 在 byte mode 下不放宽。
- grep 分别因 match limit、byte limit、两者同点和 complete 停止；snippet truncation 独立统计。
- cursor 参数篡改、版本错误、不同 pattern/include/path 复用被拒绝；相同快照顺序可连续读取且无重复/漏项。
- 超长 path/header、并行 multi-result batch 仍受 TOOL-002A 最终 cap。

Acceptance checklist：

- [x] read_file schema 清楚表达两种模式的互斥；provider adapters 都能正确传递新字段。
- [x] `ReadRegularFileRangeNoSymlink` 是 byte mode 的唯一底层文件读取入口。
- [x] grep/grep_files 同时报告集合完整性和 byte stop reason。
- [x] 所有 continuation 均是有界、版本化、与原查询绑定的 opaque cursor。
- [x] 现有 120 行默认、16 MiB source guard 和 path/symlink 安全测试不回退。

Validation：

```bash
go test ./internal/fileutil ./internal/tools \
  -run 'Test.*(ReadFile.*Byte|RangeNoSymlink|Grep.*Byte|SearchCursor|Minified|JSONL|UTF8|Symlink).*' \
  -count=1 -timeout=180s
go test ./internal/fileutil ./internal/tools ./internal/runtime -count=1 -timeout=240s
gofmt -l internal/fileutil internal/tools internal/runtime
git diff --check
```

Non-goals：

- 不增加浏览器端编辑器，不为搜索结果建立向量库或永久索引。
- 不承诺 workspace 在两次分页间被外部修改时形成事务快照；若 cursor 是 current-view/best-effort 语义，必须在 metadata/spec 明示。

Commit subject：`feat(tools): add bounded file and search continuations`

Completion record：

- Commit：本任务提交 `feat(tools): add bounded file and search continuations`。
- Cursor schema version：`1`；base64url token 上限 2048 bytes，绑定 tool + resolved root/source + pattern + include 的 SHA-256 fingerprint，携带 next current-view index、有界 last path/line 诊断与 checksum；`limit` / `byte_limit` 可在续页时调整。
- Tests：TDD 红测先证明 `read_file`/`grep`/`grep_files`/`glob` schema 拒绝 byte/cursor 字段且没有 continuation。实现后，UTF-8 mid-rune/跨页重组、16 MiB minified、JSONL/no-newline、EOF/空文件、workspace/skill/session artifact 与 symlink/escape、count/byte/同点 stop、match span、cursor version/checksum/query mismatch/tamper、long-path typed failure 均进入永久测试；聚焦命令、`go test ./internal/fileutil ./internal/tools ./internal/runtime -count=1 -timeout=300s`、`go test ./... -count=1 -timeout=600s` 与对应 `-race` 聚焦门禁通过。
- TOOL-002 status：`complete`（TOOL-002A + TOOL-002B）。

### [x] TOOL-001 — 用有界流式 collector 保存 command 原始输出

- Issue：TOOL-001。
- Priority：P1 resource safety/recovery。
- Depends on：TOOL-002A。

Scope：

- shell 与 skill command 共用一个并发安全的 streaming collector，替换 `CombinedOutput()` 全量缓冲。
- collector 内存仅保存有界 head/tail preview；输出超过 inline budget 时，将已缓存完整前缀和后续字节写入 TOOL-002A 的当前 session quota-aware artifact writer。
- stdout/stderr 继续合并到同一有序 writer；collector 的 `Write` 在 artifact hard cap 后仍准确累计 raw bytes，同时丢弃不可保存部分并维持有界 tail。
- 当前轮 ToolResult 直接返回 exit/timeout/cancel/workdir/sandbox、preview、artifact path、raw/persisted/omitted bytes 与 complete/truncated 状态。无需等它成为旧 ephemeral result。
- 只有 `raw_bytes == persisted_bytes` 且 close/flush 成功时才显示 `Complete artifact`；其余使用 `Partial artifact` / `Artifact unavailable` 等准确文案，任何路径都不再使用含混的 `Full output` 标签。
- timeout、interrupt、child budget cancel、process-group kill、非零退出、artifact create/write/fsync/close 失败都必须关闭资源并返回可诊断 metadata。
- 修正 `applyEphemeralProviderView`：不得再把已经截断的 `original.LLMOutput` 落盘并称为 Full output；已有当前轮 artifact 时只复用 pointer/metadata。

Spec changes：

- `spec/04-tools-and-skills.md`：区分 process capture、inline preview、recoverable artifact、artifact hard cap。
- `spec/07-testing-strategy.md`：增加持续输出、timeout/cancel、quota/write failure 和 skill command parity。
- `spec/10-context-compaction.md`：说明 ephemeral 层只 pointerize，不伪造原始全文。
- `spec/11-spec-audit-and-traceability.md`：登记 TOOL-001。

Implementation files：

- `internal/session/tool_output_artifact.go`
- `internal/session/tool_output_artifact_stream.go`
- `internal/session/tool_output_artifact_stream_test.go`
- `internal/tools/output_collector.go`
- `internal/tools/output_collector_test.go`
- `internal/tools/registry.go`
- `internal/tools/registry_test.go`
- `internal/runtime/engine.go`
- `internal/runtime/tool_result_budget.go`
- `internal/runtime/command_output_test.go`
- `internal/runtime/budget_lifecycle_test.go`

Permanent tests：

- collector 接收远大于 preview/artifact cap 的分块输出时，暴露的 in-memory buffer 始终不超过固定上限，raw/persisted/omitted 计数准确。
- shell 与 trusted skill command 对相同输出产生一致 preview/artifact metadata。
- command 在大输出后 timeout、manual interrupt、child budget cancel、非零退出，artifact 都正确 flush/close，进程组语义不变。
- artifact hard cap、session quota、文件数 quota、磁盘写失败、symlink/owner mode 全覆盖。
- 完整 artifact 与原始合并输出逐字节相等；不完整 artifact 永不出现 Full output 文案。
- current result artifact 可用 read_file line/byte mode 分页；旧 ephemeral pass 不重复复制 artifact。

Acceptance checklist：

- [x] shell/skill command 生产代码中不再调用 `CombinedOutput()`。
- [x] 命令总输出增长不会导致 collector 内存线性增长。
- [x] raw/persisted/inline/omitted 字节可由测试与实际 artifact 对账。
- [x] shell timeout、最小 env allowlist、sandbox、exec policy、process-group cancel 没有回退。
- [x] 旧 Full output 误标已消失。

Validation：

```bash
go test ./internal/tools ./internal/runtime ./internal/session \
  -run 'Test.*(OutputCollector|Shell.*Output|Skill.*Output|Artifact|Timeout|Interrupt|ProcessGroup).*' \
  -count=1 -timeout=240s
CGO_ENABLED=1 go test -race ./internal/tools ./internal/runtime ./internal/session \
  -run 'Test.*(OutputCollector|Shell.*Output|ArtifactQuota).*' \
  -count=1 -timeout=300s
gofmt -l internal/tools internal/runtime internal/session
git diff --check
```

Non-goals：

- 不把无限 command 输出完整保存；hard cap 与 session quota 必须始终生效。
- 不改变 shell 权限模型或自动批准外部命令。

Commit subject：`feat(tools): stream command output into bounded artifacts`

Completion record：

- Commit：本任务提交 `feat(tools): stream command output into bounded artifacts`。
- Peak collector buffer：collector 的硬上界为 `llm_output_max_bytes + max(llm_output_max_bytes, display_output_max_bytes)`；默认配置为 `32768 + 131072 = 163840` bytes。持续输出 timeout 永久测试使用 512/768-byte inline policy，实测 `collector_peak_buffered_bytes=768`，不超过声明的 1280-byte 上界，且 command raw bytes 持续增长时 buffer 不增长。
- Race/tests：TDD 红测先证明缺少 streaming artifact/collector API、shell/skill 仍走 `CombinedOutput()`、runtime interruption 丢弃当前 artifact；补充红测又捕获 write 后 sync/close 失败仍发布不确定 prefix、publish 后 reservation cleanup 隐藏已发布 artifact、reservation 更新先改内存、同 Store cross-session root 可越权写入、长 summary 截断 pointer/status、小型非法 UTF-8 被误标 inline recoverable，以及最终 header 二次裁剪造成 preview source bytes 对账失真。实现后 lifecycle 注入、并发 Store/quota/restart 回收、owner-only/cross-session/no-symlink/no-replace、持续输出 timeout、非零退出、manual interrupt、child budget/process-group cancel、shell/skill parity、UTF-8 byte-exact artifact、read_file 回捞和 ephemeral pointer 复用均进入永久测试；聚焦门禁、`CGO_ENABLED=1 go test -race ./internal/tools ./internal/runtime ./internal/session -run 'Test.*(OutputCollector|Shell.*Output|ArtifactQuota|ToolOutputArtifactStream).*' -count=1 -timeout=300s`、三包回归、`go test ./... -count=1 -timeout=600s` 与 `go vet` 全通过，`gofmt -l` / `git diff --check` 无输出。
- TOOL-001 status：`complete`。

### [x] CTX-002 — 将 micro-compaction 改为独立 ToolResult 数量/字节窗口

- Issue：CTX-002。
- Priority：P1 provider-view predictability。
- Depends on：TOOL-002A、TOOL-002B、TOOL-001。

Scope：

- 以倒序独立 `ToolResult` 为窗口单位，不再按 tool message index 计数。
- `keep_recent_tool_results` 限制完整 inline result 数量；新增 `keep_recent_tool_result_bytes`（建议 v1 默认 64 KiB，最终值须写入 spec/config test）限制这些结果的合计 provider-view bytes。
- 同一 tool message 跨越窗口边界时，逐 result 选择 inline、source cursor、artifact pointer 或 compact head/tail；不得覆盖同 batch 其他 payload/metadata。
- 建立 ToolCallID/ProviderCallID 到 assistant ToolCall 和 provider opaque block 的索引；只压缩对应旧 result 的 call arguments/payload，保留 OpenAI/Anthropic/Google 所需 ID 与顺序。
- 结果已由 TOOL-002A pointerize 时不得再次生成嵌套 pointer 或复制 artifact。
- compact event/snapshot 报告 `inline_tool_result_count/bytes`、`compacted_tool_result_count/bytes`、`pointerized_tool_result_count/bytes`。

Spec changes：

- `spec/02-cli-and-config.md`：增加 `keep_recent_tool_result_bytes` 与 context profile override。
- `spec/03-provider-contracts.md`：记录 result-level replay pairing。
- `spec/07-testing-strategy.md`：增加 mixed batch/multi-provider matrix。
- `spec/10-context-compaction.md`：把 Layer 2 最小单位明确为 ToolResult，并定义数量+字节双预算。
- `spec/11-spec-audit-and-traceability.md`：登记 CTX-002。

Implementation files：

- `internal/config/config.go`
- `internal/config/config_test.go`
- `internal/runtime/compaction.go`
- `internal/runtime/compaction_test.go`
- `internal/runtime/request_budget_test.go`
- `internal/provider/provider_test.go`

Permanent tests：

- `keep_recent_tool_results=3` 在 1×N、N×1 和混合 batch 中最多保留最近 3 个完整结果。
- 三个小结果受 count 控制；两个大结果受 bytes 控制；exact-byte boundary 与 +1 byte 有确定结果。
- 同 batch 旧/新结果混合时，ToolCallID、Name、metadata、error/final 状态不串位。
- OpenAI function_call/output、Anthropic tool_use/tool_result、Google functionCall/functionResponse multi-call replay 均合法。
- durable messages 与 events 不被改写；重复 build provider view 幂等，不生成多层 marker。

Acceptance checklist：

- [x] 实现中不再以 tool message 数量解释 `keep_recent_tool_results`。
- [x] 数量与字节预算同时生效，并进入 CTX-003/OBS-001 共用 snapshot。
- [x] mixed batch 可逐 result 压缩，不拆改 durable message schema。
- [x] 所有 provider replay 测试与 full compaction/hysteresis reuse 测试通过。

Validation：

```bash
go test ./internal/config ./internal/runtime ./internal/provider \
  -run 'Test.*(CompactOldTool|RecentToolResult|MicroCompaction|MultiCall|Replay|Hysteresis).*' \
  -count=1 -timeout=180s
go test ./internal/runtime ./internal/provider -count=1 -timeout=240s
gofmt -l internal/config internal/runtime internal/provider
git diff --check
```

Non-goals：

- 不拆分或重写 durable `messages.jsonl` record。
- 不为满足 count 删除最新外部指令或协议必需 call/result。

Commit subject：`fix(runtime): compact tool results by item and bytes`

Completion record：

- Commit：本任务提交 `fix(runtime): compact tool results by item and bytes`。
- Effective byte default：`65536` UTF-8 bytes；每个 ToolResult 占一个倒序最近位置，既有 pointer 不消耗完整 payload byte budget，exact boundary 保留、`+1` 关闭连续 inline 后缀。
- Tests：TDD 红测先证明 config/profile/snapshot 缺少 byte/count 字段且单 batch 可绕过旧 message-level 窗口；实现后 1×N、N×1、mixed batch、count+bytes、exact/+1、大结果、pointer/artifact 复用、ToolCallID/ProviderCallID alias、OpenAI/Anthropic/Google replay、compact started/finished/reused telemetry、durable log 与重复 build 均进入永久回归。聚焦测试、三包回归、对应 `-race` 与 `go test ./... -count=1 -timeout=600s` 通过。
- CTX-002 status：`complete`。

---

## Phase 2 — 安全压缩、最终 hard-fit 与历史回捞

### [x] CTX-001B — 以 result hash 和 ToolCallID 安全重实现只读结果去重

- Issue：CTX-001 最终关闭。
- Priority：P1 optimization after stop-loss。
- Depends on：CTX-002。

Scope：

- 只为明确可重复、只读、结果已受 TOOL-002A 预算控制的工具建立 allowlist：`read_file`、`grep`、`grep_files`、`glob`。其他工具默认永不去重。
- 将这些工具的参数 normalization 提取成“执行与 fingerprint 共用”的 typed normalizer；不要再维护脱离 schema/默认值的手写 map 读取。
- canonical fingerprint 覆盖所有影响结果的 normalized 参数：read_file 的 path 与 line/byte range；grep/grep_files 的 pattern/path/include/effective limit/cursor；glob 的 pattern/path/include/effective limit/cursor。
- ToolResult finalizer 计算稳定 `result_content_sha256` 与原始/inline长度。只有 tool name、canonical arguments、result hash、error/final 语义均一致时，才允许折叠旧结果。
- 折叠精确到旧 `ToolResult.ToolCallID`；旧 assistant message 中其他 call 和同一 tool message 中其他 result 保持逐字段不变。
- duplicate marker 至少保留 retained call id、result hash、original bytes、artifact/source reference（若有）；旧 call/result replay ID 继续合法配对。
- 去重只在 provider view clone 上运行，位于通用结果 finalization 之后、micro-compaction 之前；重复运行必须幂等。

Spec changes：

- `spec/03-provider-contracts.md`：定义去重后 replay 仍保留每个 call/result pair。
- `spec/04-tools-and-skills.md`：定义 eligible read-only tools、canonical args 与 result hash metadata。
- `spec/07-testing-strategy.md`：增加同请求同/异结果、默认值归一化、multi-call/provider matrix。
- `spec/10-context-compaction.md`：定义安全去重顺序、marker 与 durable-log 边界。
- `spec/11-spec-audit-and-traceability.md`：关闭 CTX-001A/B traceability。

Implementation files：

- `internal/tools/registry.go`
- 新文件建议：`internal/tools/canonical_args.go`、`internal/tools/canonical_args_test.go`
- `internal/runtime/tool_result_budget.go`
- `internal/runtime/compaction.go`
- `internal/runtime/compaction_test.go`
- `internal/provider/provider_test.go`

Permanent tests：

- 省略默认 limit 与显式默认 limit canonicalize 为同一请求；include/path/cursor/byte range 任一不同都不折叠。
- 相同请求+相同 hash 只折叠旧 result；相同请求+不同内容/hash 两个都保留。
- 文件在两次 read_file 间变化、grep 命中变化、error→success、success→error、artifact complete→truncated 均不误判。
- multi-call batch 中只有目标 ToolCallID 被替换；邻接结果、metadata 和顺序逐字段一致。
- OpenAI、Anthropic、Google replay 编码测试通过；durable messages hash 不变；第二次 provider-view build 不嵌套 marker。

Acceptance checklist：

- [x] 不存在从过时字段读取的 fingerprint 路径。
- [x] fingerprint normalizer 与工具执行使用同一 effective defaults。
- [x] 没有 result hash 就不去重，不能根据“看起来是相同请求”猜测内容未变。
- [x] 去重只改 provider view，且精确到 ToolCallID。
- [x] CTX-001 的全部 acceptance 已由永久测试覆盖。

Validation：

```bash
go test ./internal/tools ./internal/runtime ./internal/provider \
  -run 'Test.*(CanonicalArgs|SafeDedup|DuplicateToolResult|ResultHash|MultiCall|Replay).*' \
  -count=1 -timeout=180s
go test ./internal/tools ./internal/runtime ./internal/provider -count=1 -timeout=240s
gofmt -l internal/tools internal/runtime internal/provider
git diff --check
```

Non-goals：

- 不对 shell、write/edit、goal/task/agent control、网络结果或时间相关工具做去重。
- 不根据相同 path 推断文件未变化，不读取 artifact 全文回灌 provider。

Commit subject：`feat(runtime): deduplicate identical read only tool results`

Completion record：

- Commit：本任务提交 `feat(runtime): deduplicate identical read only tool results`。
- Eligible tools：仅 `read_file`、`grep`、`grep_files`、`glob`；shell、write/edit、goal/task/agent control 与其他工具均 fail closed、不参与去重。
- Tests：TDD 首轮因 `CanonicalReadOnlyToolArguments` 与 `deduplicateIdenticalReadOnlyToolResults` 尚不存在而按预期编译失败；实现后 canonical/default/cap、result hash、同/异结果、真实文件与 grep 变化、error/final、artifact 完整性、mixed batch、三 provider replay、durable log 与幂等永久回归通过。计划聚焦命令、三包完整回归、对应 `-race`、`go test ./... -count=1 -timeout=600s`、scoped `go vet`、`gofmt -l`、`git diff --check`、Web JS syntax 与 140 项 Web utility 测试全部通过；未启动 Docker。
- CTX-001 status：`complete`；CTX-001A stop-loss 与 CTX-001B 安全重实现共同关闭该 issue。

### [x] CTX-003B — 增加有界 hard-fit 收缩循环与不可满足错误

- Issue：CTX-003 最终关闭。
- Priority：P0 completion gate。
- Depends on：CTX-003A、TOOL-002B、TOOL-001、CTX-002、CTX-001B。

Scope：

- 在 CTX-003A 的 preflight 上增加确定性、严格递减、最大迭代次数受限的收缩循环；每一步后重新生成真实 adapter wire estimate。
- 收缩顺序固定为：
  1. 应用安全 dedup、result-level micro-compaction 和已有 current-result cap；
  2. 将可从 source cursor/session artifact 恢复的 inline payload 改成 pointer；
  3. 从最老、最低优先级 recent message 开始缩短 tail，同时保留最新 external user instruction、最新 steer 与所需 replay dependency；
  4. 依次移除 semantic summary、裁剪确定性 summary 的低优先级集合/摘录，保留 current goal、未完成项、关键路径、最新约束与 transcript/history reference；
  5. 再次预算；仍不 fit 时返回 typed `request_budget_unfit`，列出阻塞 component 与最小所需/可用预算。
- tool schemas 不得在循环中按猜测静默删除。若 schemas/envelope/单条不可丢用户指令本身已不可满足，直接返回 component-specific local error；explorer 的显式 allowlist 属于 HARNESS-001，不是 hard-fit 临时裁工具。
- 每轮收缩写 budget action（before/after estimate、action、affected ids/counts）；snapshot 的最终 `fit=true` 才允许 `provider.call`。
- compaction/hysteresis reuse 也走该循环；summary + recent tail 不再是未经复测的终点。
- `compact.deferred` 失败回退不得绕过 hard-fit。fallback view 超预算时仍本地拒绝。

Spec changes：

- `spec/01-runtime-architecture.md`：定义 provider call 前唯一 hard-fit gate。
- `spec/03-provider-contracts.md`：定义 unfit typed error 与 provider-not-called 语义。
- `spec/07-testing-strategy.md`：增加各 component over-budget 和严格递减/终止证明。
- `spec/10-context-compaction.md`：写出收缩优先级、不可丢边界、hysteresis/deferred 规则。
- `spec/11-spec-audit-and-traceability.md`：关闭 CTX-003A/B traceability。

Implementation files：

- `internal/runtime/request_budget.go`
- `internal/runtime/request_budget_test.go`
- `internal/runtime/compaction.go`
- `internal/runtime/compaction_test.go`
- `internal/runtime/engine.go`
- `internal/runtime/engine_test.go`
- `internal/runtime/runner.go`
- `internal/runtime/runner_test.go`
- `internal/provider/provider_test.go`

Permanent tests：

- 分别构造 system、tool schemas、metadata/envelope、summary、recent tail、最新 tool result、最新 external instruction 过大的 fixture。
- 可恢复 tool payload 会 pointerize 并最终 fit；不可恢复/不可丢单体过大返回 typed local error，HTTP/fake provider 调用数为 0。
- 收缩 action 的 estimated size 严格递减，最大迭代后终止；无无限循环、无同一 action 重复。
- 最新 external/steer、replay dependency、current goal/open items 在可满足场景保留。
- compact new/reused/deferred 三条路径都最终 preflight；三 provider multi-tool request 仍可编码。
- 语义 summary 被移除或拒绝后 deterministic summary 正常工作。

Acceptance checklist：

- [x] 代码中所有 main provider call 的唯一前置条件是最终 snapshot `fit=true`。
- [x] 任何 compaction 返回 view 都会再次测量，不存在“一次 compact 后直接发送”。
- [x] 收缩循环有固定最大步数与严格进度断言。
- [x] 不可满足错误包含 request_kind、blocking component、estimated/available/reserved 数值，不含 prompt 原文。
- [x] provider retry/auto-resume 不处理 local unfit error。

Validation：

```bash
go test ./internal/runtime ./internal/provider \
  -run 'Test.*(HardFit|Shrink|RequestBudgetUnfit|Oversized|CompactionDeferred|Hysteresis|SemanticSummary).*' \
  -count=1 -timeout=240s
CGO_ENABLED=1 go test -race ./internal/runtime ./internal/session \
  -run 'Test.*(HardFit|RequestBudget|Compaction).*' \
  -count=1 -timeout=300s
gofmt -l internal/runtime internal/provider
git diff --check
```

Non-goals：

- 不自动改变 provider/model/context window，不调用 provider 原生 compaction。
- 不通过丢弃最新用户约束、伪造 tool result 或破坏 replay 来强行 fit。

Commit subject：`feat(runtime): shrink provider views to a hard request budget`

Completion record：

- Commit：本任务提交 `feat(runtime): shrink provider views to a hard request budget`。
- Max shrink passes：固定 `256`；每个 accepted action 都重新调用 adapter estimator，且 `after_wire_body_bytes < before_wire_body_bytes`。达到上限或没有合法 action 时返回 typed `request_budget_unfit`。
- Tests/race：TDD 首轮按预期因 `fitProviderRequestToBudget`、`RequestBudgetAction`、`RequestBudgetUnfitError` 与 action/component 常量尚不存在而编译失败；实现后聚焦 hard-fit/oversized/compaction/semantic 回归、runtime/provider 完整回归、对应 runtime/session `-race`、`go test ./... -count=1 -timeout=600s`、scoped `go vet`、`gofmt -l`、`git diff --check`、Web JS syntax 与 140 项 Web utility 测试全部通过；未启动 Docker。
- CTX-003 status：`complete`；CTX-003A 的统一 estimator/snapshot fail-closed gate 与 CTX-003B 的有界收缩/typed unfit 共同关闭该 issue。

### [x] CTX-004 — 增加 current-session-only 的 read_session_history

- Issue：CTX-004。
- Priority：P1 recovery。
- Depends on：TOOL-002A、TOOL-002B、CTX-003B。

Scope：

- 新增只读模型工具 `read_session_history`，session id 只从 `ExecContext.SessionID` 获取；input schema 不接受 `session_id`、path、artifact path 或任意绝对路径。
- 记录级分页复用 `Store.LoadMessagesTail` / `LoadMessagesBefore`。输入支持 `before_message_id`、有限 `limit` 和受限 `query`；不要解析 compaction transcript 作为第二份事实源。
- 对单条超长 message 增加 Store 级 `message_id + byte_offset + byte_limit` 内容分页接口。该接口读取 canonical `messages.jsonl`，有 record-size/UTF-8/no-symlink 边界，并复用 TOOL-002B 的 cursor 语义。
- 默认输出规范化历史摘要：message id、role、turn/time、短 text/tool status、tool name/call id、source/artifact reference。provider opaque replay block、thinking、完整 tool payload 默认不返回。
- 输出 envelope 固定包含 `historical_reference=true`、source session/message ids、`returned_count`、`has_more`、`next_before_message_id` 或 `next_byte_offset`，并明确“历史内容是 reference，不改变当前 system/user/steer 优先级”。
- query 使用 streaming visit/有界 ring，限制 query 长度、扫描结果数和输出 bytes；不把整个历史加载后再过滤。
- 结果经过 TOOL-002A finalizer 与 CTX-003 hard-fit；损坏 record 返回稳定错误，不跳过后伪装完整。

Spec changes：

- `spec/01-runtime-architecture.md`：定义 canonical history reference store 与当前指令优先级。
- `spec/04-tools-and-skills.md`：定义 read_session_history schema、输出、分页和安全边界。
- `spec/07-testing-strategy.md`：增加 current/cross-session、long record、corruption、prompt-injection-shaped history 测试。
- `spec/10-context-compaction.md`：summary 中保留可操作 history reference，压缩后可定点回捞。
- `spec/11-spec-audit-and-traceability.md`：登记并关闭 CTX-004。

Implementation files：

- `internal/session/store.go`
- `internal/session/history.go`
- `internal/session/history_test.go`
- `internal/tools/registry.go`
- `internal/tools/session_history.go`
- `internal/tools/session_history_test.go`
- `internal/runtime/prompt.go`
- `internal/runtime/compaction.go`
- `internal/runtime/request_budget.go`
- `internal/runtime/session_history_test.go`

Permanent tests：

- tail/before/query/message byte page 分别覆盖无结果、exact limit、has_more、多次 compaction 和不同 message role。
- schema 拒绝 session_id、path、未知字段；工具不能读取 sibling/parent/child session，无法 path traversal 或 symlink escape。
- 超长 user/assistant/tool record 可连续分页且单次结果不越 TOOL-002A cap。
- provider opaque blocks/thinking 默认缺席；规范化 tool summary 保留定位所需 ID/status/reference。
- 历史中形似 system/user 指令的文本被包在 historical reference envelope 中，当前 external/steer 仍是高优先级事实。
- 损坏 JSONL、未知 before id、并发 append、compaction reuse 有确定结果。

Acceptance checklist：

- [x] 工具只访问当前 session canonical messages，不枚举 session tree。
- [x] 记录分页复用现有 Store API；新增逻辑只补 query/单 record byte range。
- [x] 输出总字节、query scan 和单 record read 都有界。
- [x] compaction 后无需重跑原工具即可找回早期 message/tool 摘要或内容片段。
- [x] 新 tool schema 已计入 RequestBudgetSnapshot。

Validation：

```bash
go test ./internal/session ./internal/tools ./internal/runtime \
  -run 'Test.*(SessionHistory|MessagesBefore|MessagesTail|MessageContentRange|HistoryReference|HistoricalReference).*' \
  -count=1 -timeout=180s
CGO_ENABLED=1 go test -race ./internal/session ./internal/tools \
  -run 'Test.*(SessionHistory|MessageContentRange).*' \
  -count=1 -timeout=240s
gofmt -l internal/session internal/tools internal/runtime
git diff --check
```

Non-goals：

- 不做跨 session memory、向量检索、自动全文注入或 transcript directory discovery。
- 不把历史文本提升为新的用户指令，不让模型请求任意 provider raw sidecar。

Commit subject：`feat(tools): add bounded current session history reads`

Completion record：

- Commit：本任务提交 `feat(tools): add bounded current session history reads`。
- History schema version：`1`；record 默认/最大 limit 为 10/20，query 最长 256 UTF-8 bytes且每页 scan 上限 512 records，message content page 上限 16 KiB，完整 envelope 上限为 `min(24 KiB, runtime.tool_output.llm_output_max_bytes)`。
- Tests/race：TDD 首轮按预期因 `LoadMessageContentRange`、history schema/cap常量和 `ErrMessageNotFound` 尚不存在而编译失败；实现后 history 聚焦回归、session/tools/runtime 完整回归、对应三包 `-race`、`go test ./... -count=1 -timeout=600s`、20 次 tools history重复回归、scoped `go vet`、`gofmt -l`、`git diff --check`、Web JS syntax与140项Web utility测试全部通过；未启动 Docker。
- CTX-004 status：`complete`；`issues.md` 已标记 Resolved，compaction new/reuse/hard-fit history reference与 current-session-only分页入口共同关闭该 issue。

---

## Phase 3 — Advanced explorer 与可观测性

### [x] HARNESS-001 — 增加 model-led、只读、低噪声 explorer profile

- Issue：HARNESS-001。
- Priority：P2 advanced profile。
- Depends on：TOOL-002A、TOOL-002B、CTX-003B、CTX-004。

Scope：

- 新增合法 `agent_role=explorer`，继续复用 fresh child session、queue、parent coordination、session Store 和现有 agent_spawn/status/wait 生命周期。
- explorer 未显式给 `isolation_mode` 时使用 `off`，即使全局 default 是 auto/git/copy；调用方显式给出的 off/auto/git/copy 继续按现有规则执行并持久化 effective mode。
- 定义 Registry capability profile。explorer provider schema 默认只包含：`read_file`、`grep_files`、`grep`、`glob`、`load_skill`、`finish`。`shell`、trusted skill command、write/edit、goal/task/feature mutation、plan mode、agent control 默认均隐藏。
- 同一 allowlist 在 `Registry.Execute` 做 defense-in-depth。隐藏工具从恢复轨迹、兼容 provider 或伪造 tool call 进入时返回稳定 `tool_not_allowed_for_role`，不得执行副作用。
- explorer role guidance 只规定身份/边界/交付格式：简短结论、`claim | file:line | confidence` 证据、未覆盖范围、关键疑点；不规定固定阅读顺序、审计路线、taskboard 节奏或必须委派。
- agent_spawn description 增加信息经济启发：开放式/跨模块/入口不明且原始搜索量远大于结论时考虑 explorer；入口明确的小检查留在 parent。当目的为 context isolation 时，提示同步 spawn 或 background+agent_wait，parent 不重复相同探索。全部是模型 guidance，不是 hard guard。
- 扩展 role provider override，使 planner/generator/evaluator/explorer 统一支持 provider/base URL/model 以及至少 `reasoning_effort`、`max_output_tokens`；provider-specific thinking/text 选项按现有 ProviderOptions 兼容模型设计，不在 prompt 里临时注入。
- 将 explorer 的 effective provider/model/reasoning/output/isolation/tool profile 写入 child session metadata、queue job/event 和 Web session detail。
- 同步和后台 handoff 都经过 TOOL-002A 尺寸预算；长原始轨迹留在 child session/artifact，parent 只接收有界 final_text、证据摘要和 child/session reference。
- Web Settings 在现有 Role providers 区增加 Explorer row 并完整 GET/PATCH/YAML round-trip；默认首页不新增委派 dashboard。

Spec changes：

- `spec/00-product.md`：把 explorer 定位为 optional large-project profile，不是默认 Web workflow。
- `spec/01-runtime-architecture.md`：定义 role capability profile 与双层执行边界。
- `spec/02-cli-and-config.md`：增加 explorer role override 和 reasoning/output 字段。
- `spec/03-provider-contracts.md`：定义 role-filtered tool schemas 与 effective options metadata。
- `spec/04-tools-and-skills.md`：定义 explorer allowlist/denial。
- `spec/07-testing-strategy.md`：增加 sync/background/recovery/allowlist/handoff fixture。
- `spec/09-phase-plan.md`：放入 Phase 13 advanced，Web 配置对齐记入 Phase 15。
- `spec/14-multi-agent-and-isolation.md`：定义 explorer role、默认 isolation off、model-led 委派和 handoff contract。
- `spec/11-spec-audit-and-traceability.md`：登记 HARNESS-001。

Implementation files：

- `internal/runtime/role.go`
- `internal/runtime/prompt.go`
- `internal/runtime/delegation.go`
- `internal/runtime/runner.go`
- `internal/runtime/engine.go`
- `internal/runtime/role_test.go` 或现有相关测试文件
- `internal/runtime/prompt_test.go`
- `internal/runtime/delegation_test.go`
- `internal/runtime/engine_test.go`
- `internal/config/config.go`
- `internal/config/config_test.go`
- `internal/tools/registry.go`
- `internal/tools/registry_test.go`
- `internal/app/orchestration.go`
- `internal/app/app_test.go`
- `internal/webconsole/service.go`
- `internal/webconsole/service_test.go`
- `internal/webconsole/assets/settings-view.js`
- 相关 Web utility/browser fixture

Permanent tests：

- normalize/config/API/CLI 接受 explorer，拒绝未知 role；旧三 role 配置无迁移也能加载。
- explorer provider request schema 精确匹配 allowlist；直接调用每个禁用工具都被执行层拒绝且无文件/session mutation。
- trusted command skill 即使已加载也不出现在 explorer schema；load_skill 只加载说明和只读 bundle 访问。
- 未指定 isolation 时 explorer=off；显式 auto/git/copy/off 保持调用者选择。
- explorer role override 的 model/reasoning/max output 进入真正 TurnRequest 和 child metadata；空字段继承 parent/provider defaults。
- sync、background+wait、失败、暂停、取消、parent resume/recovery 全路径返回有界 handoff。
- deterministic fake-provider fixture 证明 parent messages/request 不含 child tool trajectory，只含有界 handoff/reference。
- Web Settings Explorer row GET/PATCH/YAML round-trip，session detail 折叠区显示 role/effective options；首页无新 orchestration panel。

Acceptance checklist：

- [x] explorer 由模型/调用方显式选择，runtime 不自动切任务或强制 spawn。
- [x] provider schema 与执行层使用同一 capability profile，不能只隐藏 UI/schema。
- [x] explorer 默认无法修改 repo/session task state、运行 shell 或派生 child。
- [x] effective provider/reasoning/output/isolation/options 全部 durable。
- [x] handoff 有尺寸上限且 child 原始输出不回灌 parent。
- [x] Web-first 默认页面与 root README 仍保持简短。

Validation：

```bash
go test ./internal/config ./internal/tools ./internal/runtime ./internal/app ./internal/webconsole \
  -run 'Test.*(Explorer|RoleProvider|ToolProfile|Agent.*Handoff|Settings.*Role).*' \
  -count=1 -timeout=300s
CGO_ENABLED=1 go test -race ./internal/runtime ./internal/session \
  -run 'Test.*(Explorer|Agent.*Background|Parent.*Resume).*' \
  -count=1 -timeout=300s
node --check internal/webconsole/assets/*.js
node --test validation/scripts/webconsole_utils_test.mjs
gofmt -l internal/config internal/tools internal/runtime internal/app internal/webconsole
git diff --check
```

Non-goals：

- 不提供 explorer 可写 shell，不实现 nested explorer、固定 DAG、自动模块切片或强制 parent 等待。
- 不新增默认首页复杂面板、浏览器 IDE、TUI 主路径或第二套 orchestration store。

Commit subject：`feat(runtime): add a read only explorer agent profile`

Completion record：

- Commit：本任务提交 `feat(runtime): add a read only explorer agent profile`。
- Effective allowlist：`read_file`、`grep_files`、`grep`、`glob`、`load_skill`、`finish`；同一 `explorer-readonly-v1` profile 同时过滤 provider schema、`Registry.Get/Definitions` 与执行 dispatch，禁用调用稳定返回 `schema_reject/tool_not_allowed_for_role`。
- Browser/tests/race：HARNESS 聚焦回归、相关 Go 包完整回归、`CGO_ENABLED=1 go test -race ./internal/runtime ./internal/session -run 'Test.*(Explorer|Agent.*Background|Parent.*Resume).*'`、`go test ./... -count=1 -timeout=600s`、scoped `go vet`、`node --check internal/webconsole/assets/*.js` 与 140 项 Web utility tests 均通过；deterministic parent/child fixture 证明 child raw trajectory sentinel 不进入 parent request；未启动 Docker。
- HARNESS-001 status：`complete`；`issues.md` 已标记 Resolved，sync/background+wait/failure/pause/cancel/recovery、effective option snapshot、Web round-trip 与默认首页保持简洁均有永久回归。

### [x] OBS-001 — 持久化 context budget telemetry 并聚合 root/child

- Issue：OBS-001。
- Priority：P2 observability/benchmark。
- Depends on：HARNESS-001、CTX-003B。

Scope：

- 直接复用 CTX-003 的 versioned `RequestBudgetSnapshot`；不创建 `ContextBudgetSnapshot` 第二套 estimator。
- 每个 main/semantic-summary request 持久化 request id、request kind、session/root/parent/queue ids、turn/attempt、各组成项 bytes/estimated tokens、effective window、output reserve、safety headroom、fit/headroom、compaction summary/action、inline/pointerized/compacted tool bytes。
- 将 `turn.stopped` 的 provider usage、cache usage、stop reason、response id 与对应 request id 关联。provider 未返回 usage 时保留 unknown，不能填 0 冒充已测。
- compact.started/finished/reused/deferred 和 shrink action 使用同一 request/turn correlation；能从 events 重建一次请求从原始 view 到最终 view 的变化。
- 在 session Store/SDK 增加只读 context report：单 session 明细，以及 root 聚合直接/递归 child。指标至少包括 root peak input、child peak、child aggregate、request/turn/tool-call 数、compaction 次数、inline/artifact/pointer bytes、provider usage 和 wall time。
- 明确区分 root peak 与 total aggregate；不把 child token 增加隐藏在“主上下文更小”的单一结论里。
- 建议 CLI 增加 `go-cli-agent sessions context <session-id> --json`（最终语法先写 spec）；Web 只在现有 session detail 加折叠 context 区，不改首页。
- 建立固定小仓库与 scripted fake-provider 三结构 fixture：单 root 广泛探索、root 定点 grep_files/read_file、root 委派 explorer。输出机器可读 JSON，使用相同事实与确定性 payload 大小。
- CI fixture 断言 report schema、lineage/accounting 和预期相对关系，不依赖外部收费 provider；live provider/cost 仅作为显式可选 validation。
- telemetry 只保存尺寸、计数、ID 和 provider usage，不复制 system/user/tool 原文，不引入默认 runtime redaction 规范。

Spec changes：

- `spec/01-runtime-architecture.md`：定义 telemetry 来源、派生 report 与事实源关系。
- `spec/03-provider-contracts.md`：定义 request/response usage correlation。
- `spec/07-testing-strategy.md`：定义三结构 deterministic fixture 与可选 live smoke。
- `spec/09-phase-plan.md`：放入 advanced observability/Phase 13-15 收敛。
- `spec/10-context-compaction.md`：扩展 compact/request budget event schema。
- `spec/14-multi-agent-and-isolation.md`：定义 lineage aggregation。
- `spec/11-spec-audit-and-traceability.md`：登记并关闭 OBS-001。

Implementation files：

- `internal/runtime/request_budget.go`
- `internal/runtime/engine.go`
- `internal/runtime/compaction.go`
- 新文件建议：`internal/session/context_report.go`、`internal/session/context_report_test.go`
- `internal/session/store.go`
- `internal/session/types.go`
- `pkg/agent/agent.go`
- `internal/app/app.go`
- `internal/app/app_test.go`
- `internal/webconsole/service.go`
- `internal/webconsole/bounded_view.go`
- `internal/webconsole/service_test.go`
- `internal/webconsole/assets/session-view.js`
- `validation/` 下新增固定 fixture/runner，名称在 spec 确定后落地

Permanent tests：

- snapshot→prepared→turn.stopped 通过 request id 唯一关联；semantic-summary 多请求与 transport retry attempt 不串位。
- provider usage 缺失、cache usage、request rejected、compaction deferred、session crash/reload 均有确定 report。
- root 递归聚合去重 session/job，不把同一 child 重复计费；root peak、child peak、child aggregate 数学对账。
- report 只含允许字段；fixture 中放入 sentinel prompt/secret-shaped text，event/report 不得出现原文。
- CLI JSON、SDK struct、Web bounded response schema 一致并 versioned；oversized event/report 不压垮 session detail。
- 三结构 fixture 重复运行输出稳定；delegated 结构的 root peak 小于单 root fixture，同时 child aggregate 非零且 total usage单独报告。

Acceptance checklist：

- [x] hard-fit 与 telemetry 读取同一 snapshot 对象/序列化，不存在公式复制。
- [x] 每次 provider request 都能关联 budget、compaction action 与真实/unknown usage。
- [x] root/child report 可同时回答“主窗口是否受保护”和“总 token 是否增加”。
- [x] advanced JSON/SDK/Web inspector 可查询，默认首页没有 dashboard 化。
- [x] deterministic fixture 进入普通 CI；live provider 与 cost 不成为无 credential 环境的隐式依赖。

Validation：

```bash
go test ./internal/runtime ./internal/session ./internal/app ./internal/webconsole ./pkg/agent \
  -run 'Test.*(RequestBudgetSnapshot|ContextReport|ContextTelemetry|Lineage|HarnessFixture).*' \
  -count=1 -timeout=300s
CGO_ENABLED=1 go test -race ./internal/runtime ./internal/session \
  -run 'Test.*(ContextReport|ContextTelemetry|Lineage).*' \
  -count=1 -timeout=300s
node --check internal/webconsole/assets/*.js
node --test validation/scripts/webconsole_utils_test.mjs
gofmt -l internal/runtime internal/session internal/app internal/webconsole pkg/agent validation/cmd
git diff --check
```

Non-goals：

- 不自动调 threshold、prompt 或委派策略，不把一次 fixture 结果推广为所有模型最优值。
- 不实现 hosted telemetry、外部 analytics、复杂 dashboard 或无 provider 价格依据的 cost 推算。

Commit subject：`feat(runtime): expose context budget and lineage telemetry`

Completion record：

- Commit：本任务提交 `feat(runtime): expose context budget and lineage telemetry`。
- Report schema version：`1`；canonical `session.RequestBudgetSnapshot` schema 同为 `1`，runtime hard-fit 使用该类型的 alias，报告直接读取 prepared event 中的同一 JSON。
- Lifecycle/usage：main 与 semantic-summary 的 prepared、budget action、compaction、callback/retry 和唯一 completed/failed terminal event 以 request id/kind/turn/sequence 关联；OpenAI/Anthropic/Google 区分 missing usage 与 reported zero，稳定 source 仅为 `provider` / `legacy_inferred`。
- Lineage/report surfaces：Store/Core/SDK、`sessions context <id> --json` 与 Web `/api/sessions/<id>/context` 共用 report schema；递归 lineage 分列 root/child peak、root/child/total aggregate、三类 provider-view bytes、artifact bytes、usage 和 unknown usage，RFC3339Nano 时间按真实时序对账。Web Context tab 懒加载/手工刷新，detail 总预算 64，aggregate 不截断。
- Fixture artifact：`validation/cmd/contextharnessfixture`；三种 scenario 名为 `single_root_broad`、`single_root_narrowed`、`delegated_explorer`。delegated 输出 `root_peak=500`、`root_aggregate=950`、`child_aggregate=1400`、`total=2350`，并断言 total 使用 root aggregate 对账；连续两次 `go run` 输出 byte-for-byte 相同。
- Tests/race/browser：OBS 聚焦测试、runtime/session telemetry race、相关包完整回归、`go test ./... -count=1 -timeout=600s`、scoped `go vet`、fixture 双运行 `cmp`、`node --check` 与 142 项 Web utility tests 全通过；`gofmt -l` / `git diff --check` 无输出，未启动 Docker。
- Spec sync：`spec/01`、`02`、`03`、`07`、`08`、`09`、`10`、`11`、`14`、`17` 已同步 canonical snapshot、request lifecycle/usage、lineage aggregation、CLI/SDK/Web 查询面与 deterministic comparator。
- OBS-001 status：`complete`；`issues.md` 已标记 Resolved。

---

## Phase 4 — 全量收敛与发布门槛

### [ ] CLOSE-001 — 对齐 spec、issue 状态与全量回归

- Issue：CTX-001、CTX-002、CTX-003、TOOL-001、TOOL-002、TOOL-003、CTX-004、HARNESS-001、OBS-001。
- Priority：release gate。
- Depends on：全部前置任务。

Scope：

- 逐条对照 `issues.md` acceptance criteria 与本文件 completion record；没有 commit+test 证据的 issue 保持 Open。
- 对已完成 issue 更新 `Status: Resolved`、resolution、commit、永久测试和验证命令；若实现选择改变推荐方向，更新 root cause/acceptance 保持事实准确。
- 复核 `spec/00`、`01`、`02`、`03`、`04`、`07`、`09`、`10`、`11`、`12`、`13`、`14` 的术语、schema、默认值、phase 与 traceability 无矛盾。
- 只在用户默认入口或配置发生变化时最小更新 root `README.md` / help；详细实现继续留在 spec。
- 运行全量 Go、race、Node、Web headless smoke、format、vet、build 和 diff 检查；保存机器可读 harness fixture/report path。
- 核对 git 历史中每个任务都有真实 commit，且工作树只剩用户原有或明确记录的无关 dirty state。
- 本轮不删除 `issues.md` / `todos.md`；它们作为审计/实施记录保留。只有用户之后明确要求删除已收敛 ledger 时才单独处理。

Spec changes：

- 所有上述 spec 做最终 cross-review；`spec/11-spec-audit-and-traceability.md` 必须能从每个 issue 映射到实现与测试。

Implementation files：

- 原则上无新增 runtime 功能；只允许修复全量验证发现的 scoped 回归，并把修复与测试纳入对应任务或新增 issue/commit。
- `issues.md`
- `todos.md`
- 必要时最小更新 `README.md`、CLI help、validation README。

Permanent tests：

- 前置任务全部测试已进入仓库；不得依赖本地临时脚本、未跟踪 fixture 或手工观察作为唯一证据。

Acceptance checklist：

- [ ] 9 项 issue 均有 resolution commit 和永久测试；无证据项未被误标 Resolved。
- [ ] main 与 semantic-summary provider request 均 fail-closed；三 provider replay 全矩阵通过。
- [ ] 大 file/search/command/child/history 输出均有界、可恢复或返回明确 typed failure。
- [ ] explorer 保持可选/model-led/只读，默认 Web 首页无复杂化。
- [ ] context report 使用 hard-fit 同一 snapshot，并能对账 root/child。
- [ ] 全量命令全部成功；任何 skip 都说明原因且不用于替代必需 acceptance。
- [ ] `git status --short` 没有本计划产生的未提交代码、spec、fixture 或 build 产物。

Required validation：

```bash
go test ./cmd/... ./internal/... ./pkg/... ./validation/cmd/... -count=1 -timeout=300s
go vet ./internal/config ./internal/session ./internal/provider ./internal/runtime ./internal/tools ./internal/webconsole
CGO_ENABLED=1 go test -race ./internal/runtime ./internal/tools ./internal/session -count=1 -timeout=300s
node --check internal/webconsole/assets/*.js
node --check validation/scripts/webconsole_ui_smoke.mjs
node --test validation/scripts/webconsole_utils_test.mjs
gofmt -l cmd internal pkg validation/cmd
git diff --check
git status --short
go build -o /tmp/go-cli-agent-context-harness ./cmd/go-cli-agent
```

Web smoke：

- 运行仓库已有的 headless browser smoke，覆盖 Settings Explorer role round-trip、session context 折叠区、长结果 pointer/history pagination 与 console/runtime exception 检查。
- smoke 使用临时 session/config/workspace 和随机空闲本地端口；不得使用真实 provider credential，不得启动 Docker。

Non-goals：

- 不在 closeout 临时加入新 orchestration、TUI/IDE、远程部署或 hosted telemetry。
- 不声称“绝对无 bug”；结论限定为本文范围内无保留的已知可见问题，并附当前证据。

Commit subject：`docs(harness): close context convergence ledger`

Completion record：

- Commit：`pending`
- Full Go tests：`pending`
- Race：`pending`
- Vet/format/diff：`pending`
- Node/Web smoke：`pending`
- Harness fixture/report：`pending`
- Final HEAD：`pending`
- Remaining unrelated dirty paths：`pending`
