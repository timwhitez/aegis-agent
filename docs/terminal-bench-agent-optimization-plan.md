# Terminal-Bench Agent Reference Optimization Plan

更新日期：2026-05-18

## 1. 目标与边界

本轮目标是参考 Terminal-Bench 近期高分 agent、公开 blog 和 GitHub 代码，对当前 `go-cli-agent` 做最小化、可验证的优化。优化必须保持当前仓库的 Web-first v1 边界：

- 默认是 Web-first 本地 harness，CLI 作为 fallback；不把 TUI、worker internals、queue/delegate 调参改成默认页面主路径。
- 不引入固定 DAG、硬编码 workflow engine 或 benchmark-specific 策略。
- 任何代码改动必须同步必要 spec、测试和记录，并在验证后进入真实 git commit。
- ForgeCode 只作为低权重反面/谨慎参考：Terminal-Bench integrity update 已公开记录 ForgeCode agent 存在 curl 网络公开解法并写入 `AGENTS.md` 的 reward hacking 案例，因此不得借鉴“联网找公开答案”“benchmark 任务特化启动文件”等模式。

## 2. 参考源与权重

| 来源 | 可验证事实 | 本项目参考权重 | 取舍 |
| --- | --- | --- | --- |
| Terminal-Bench 2.1 leaderboard | 当前 2.1 榜单显示 Codex CLI GPT-5.5 为 83.4%，Terminus 2 GPT-5.5 为 78.2%，并说明结果由 Terminal-Bench 团队运行和验证。 | 高 | 优先看 Codex/Terminus 的通用 harness 形状，而不是复制 benchmark 适配。 |
| Terminal-Bench Terminus 2 | `terminus_2.py` 对 terminal 输出做头尾保留截断；还包含 context length unwinding、handoff summary、parser warning/fix、task completion 二次确认。 | 高 | 适合转化为通用 shell output 可观测性改进；不复制 tmux/episode loop。 |
| OpenAI Codex | 公开仓库定位为本地运行的 lightweight terminal coding agent；当前 2.1 榜单最高。 | 高 | 本项目已大量对齐 Codex 的 local CLI、session/steer/approval 方向；继续保持 Web-first local harness 和 CLI fallback 克制。 |
| Vix releases | README 主张 “Sleek, Fast and Token Efficient AI Coding Agent”，并展示 7 个真实任务中 Vix plan mode 总成本/时间低于对照；其长文件任务仍是弱项。 | 中高 | 参考 token/turn efficiency 和 plan artifact 思路；不直接复制产品形态。 |
| WozCode session limit article | 指出 vanilla Claude Code 的 read/grep/edit primitives 会让 turn 和 context 成本累积；其优化包括 combined search+read、batched edits、AST-aware truncation、fuzzy edit matching。 | 中高 | 优先从低风险工具输出和检索形状入手；batched edit/AST truncate 需单独设计。 |
| AHE | README 称 AHE 通过 observability layers 演化 system prompts、tool descriptions、tool implementations、skills、sub-agents 和 memory，并强调 git-tracked/auditable edits。 | 中 | 参考“证据驱动、逐步可回滚优化”的方法论；不在本仓库引入自动进化系统。 |
| Meta-Harness | README 定义为围绕固定 base model 自动搜索 task-specific harness，并有 Terminal-Bench 2 scaffold evolution 示例。 | 中 | 参考“harness 本身是优化对象”；本轮只做人工、最小化 slice。 |
| Capy articles | Capy 文章强调 parallel task execution、planning agent、isolated VM、review/PR workflow。 | 低到中 | 与本项目 Phase 11+ 扩展有关系，但当前 AGENTS 要求不让这些能力主导默认 Web 页面。 |
| jjagent | 已归档；其核心是用 hooks 把 agent edits 关联到 jj change，并用 session id trailer 支持 resume。 | 低到中 | 参考“编辑归属与恢复线索”方向；本轮不引入 jj 依赖。 |
| Polaris | Go distributed function-calling framework，强调 lightweight sidecars、parallel execution、concise JSON schema。 | 低 | 与本项目 provider/tool schema 有相似点，但 distributed agent architecture 超出 Web-first v1。 |

## 3. 当前仓库事实

已核验文件：

- `AGENTS.md`：要求先看 spec、Web-first v1 收敛、默认 `go-cli-agent web`、代码修复后真实 commit。
- `spec/00-product.md`：Web-first v1 默认围绕 `go-cli-agent web` 本地控制台，CLI fallback 保留 `init/run/exec/steer/continue/sessions/goal/tasks/probe-provider/doctor`，queue/delegate 是 large-project profile 的轻量入口和观测面。
- `spec/01-runtime-architecture.md`：core runtime / sdk facade / web app-service adapter / cli adapter 分层，session store 是事实源。
- `spec/03-provider-contracts.md`：provider replay 差异留在 adapter 层。
- `spec/09-phase-plan.md`：Phase 0-10 的 runtime / provider / CLI 基座与 Phase 15 Web 控制台共同构成 Web-first v1，Phase 11-14 / 16+ 不主导默认页面。
- `spec/11-spec-audit-and-traceability.md`：记录模型是 agent、harness 负责 action space 和事实源；Plan Mode / Goal / Role hint 都不能退化为固定 workflow engine。
- `spec/12-task-system.md`：task/todo 是 durable rhythm 和恢复索引，不承担 artifact 完成判定。
- `spec/13-live-input-and-steering.md`：steer 是 queue-first + best-effort interrupt，guard 应拉回交付但不能吞掉外部 user message。
- `internal/tools/registry.go`：当前 `shell` 和 skill command 输出统一调用 `truncateOutput(text, 12000)`，旧逻辑只保留前缀并写 `...[truncated]`，会丢失很多 CLI/测试失败场景的末尾错误。

当前工作树已有无关改动：

- `issues.md` / `skillgap.md` 删除，`skills/timwhite-security-review/*` 修改，以及 `skills/pentest-toolset/`、`workspace/` 等未跟踪文件。本轮不得回滚或混入提交。

## 4. 优化候选队列

### P0-A: shell / skill command 输出头尾保留截断

动机：

- Terminus 2 明确保留 terminal output 的 first + last portions。
- 真实 CLI/测试输出的末尾经常包含 exit summary、panic、traceback、compiler diagnostic 或 failing assertion。当前只保留前缀会让模型在下一 turn 看不到最关键的失败原因。

最小改动：

- 修改 `truncateOutput`，当输出超过限制时保留 head + tail，中间写明 omitted bytes。
- 保持 `raw_length`、`truncated=true` metadata 不变，避免影响上层协议。
- 增加单元测试覆盖 head、tail、omitted marker 和未截断路径。
- 更新 `spec/04-tools-and-skills.md` 对 shell 输出截断的描述。

风险：

- 低。只改变超长输出的文本形状；不会扩大权限、不会改变执行、不会引入 provider-specific 逻辑。

验证：

- `go test ./internal/tools`
- `go test ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`
- `gofmt -w internal/tools/registry.go internal/tools/registry_test.go`
- `git diff --check`

### P1-B: grep_files 可选 ranked snippets

动机：

- WozCode 的 combined search+read 可以减少 grep -> read -> read 的多轮成本。
- Vix 的 token efficiency 也指向“探索阶段返回更有用、更压缩的候选上下文”。

建议：

- 给 `grep_files` 增加可选 `snippets=true` 或新工具 `search_snippets`，默认仍返回路径，显式开启才返回每个文件少量匹配上下文。
- 需要严格限额、排序和测试，避免把 `grep` 的大上下文泛化成默认。

本轮不做：

- 需要更细设计，避免破坏现有工具语义和 Plan Mode allowlist。

### P1-C: edit_file 失败时提供最小 fuzzy hint

动机：

- WozCode 提到 fuzzy edit matching 可减少 `old_text not found` 后的额外 turn。

建议：

- 不自动模糊改写文件；只在失败结果中返回最接近候选片段/行号，仍让模型显式发起下一次 edit。

本轮不做：

- 需要防止错误匹配带来误编辑风险。

### P2-D: handoff artifact freshness score

动机：

- AHE/Meta-Harness 都强调 observability 和可追溯演化；Terminus 2 有 handoff summary。
- 当前项目已有 `session.md`、long-run checkpoint、`reports/progress.md` / `reports/validation.md` reminder。

建议：

- 只增强可观测字段，不阻断普通 finish；例如在 `session.md` 增加最近 artifact freshness summary。

本轮不做：

- 需要先确认与现有 project-memory reminder 是否重复。

## 5. 本轮实施选择

本轮只实施 P0-A。原因：

- 与 Terminal-Bench Terminus 2 和 WozCode 的共同信号一致：减少丢失关键 terminal evidence，降低重新运行/重新读取成本。
- 改动集中在 `internal/tools/registry.go` 和测试/spec；不触碰 provider、runtime guard、Plan Mode、Web 或 multi-agent。
- 可以用本地单元测试和 repo acceptance 直接验证。

## 6. 准确性 Review

Review 结论：

- 已核验当前 `truncateOutput` 只保留前缀；这是当前代码事实，不是推断。
- 已核验 `shell` 和 skill command tool 均复用该函数，因此一次修改可覆盖两条本地 command 输出路径。
- Terminus 2 的可借鉴点仅限“超长 terminal output 保留头尾”；其 tmux loop、parser 协议、双确认、unwind/summarize 不在本轮复制范围。
- WozCode 的 combined search+read、batched edits、AST-aware truncation、fuzzy edit matching 是后续候选，不应未经设计直接塞进本仓库默认工具面。
- ForgeCode 因 Terminal-Bench integrity update 被降权；本计划没有引入任何联网查题、公开答案检索、benchmark fixture 适配或自动写 `AGENTS.md` 的行为。
- 当前计划符合 AGENTS：先读 spec、最小化修改、保持 Web-first v1 和 CLI fallback 边界、文档和测试随代码一起提交。

## 7. 执行记录

- 2026-05-18: 完成外部参考核验、当前 spec/code audit、P0-A 方案选择和本计划准确性 review。
- 2026-05-18: 完成 P0-A 代码/spec/test 修改：`truncateOutput` 改为保留 head + tail，中间标注省略字节；`spec/04-tools-and-skills.md` 记录 shell 输出截断契约；`internal/tools/registry_test.go` 覆盖头尾保留与 UTF-8 边界。
- 2026-05-18: 独立验证通过：`go test ./internal/tools`、`go test ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`、`git diff --check -- docs/terminal-bench-agent-optimization-plan.md spec/04-tools-and-skills.md internal/tools/registry.go internal/tools/registry_test.go`、`gofmt -l internal/tools/registry.go internal/tools/registry_test.go`、`./test.sh`。
- 2026-05-18: 本记录随 P0-A 代码、测试和 spec 一起进入同一个 git commit；commit hash 以仓库历史为准。

## 8. Handoff

若后续 agent 接手：

1. 不要回滚当前工作树中与本轮无关的 `skills/`、`workspace/`、截图、`issues.md`、`skillgap.md` 改动。
2. 本轮应只 stage/commit P0-A 相关文件：`docs/terminal-bench-agent-optimization-plan.md`、`spec/04-tools-and-skills.md`、`internal/tools/registry.go`、`internal/tools/registry_test.go`。
3. 若 P0-A 验证失败，先修复该 slice；不要顺手推进 P1-B/P1-C。
4. 最终完成前必须更新本节执行记录，并做 prompt-to-artifact completion audit。
