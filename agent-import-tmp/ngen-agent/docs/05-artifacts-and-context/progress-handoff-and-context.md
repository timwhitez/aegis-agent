# Progress、handoff、continuity 与 context pack

## 16. `progress.md` 与 `handoff.md`

### `progress.md` 的职责

持续记录：

- 当前目标，
- 当前 step，
- 最近动作，
- 最近验证结果，
- 最近一次 completion gate 结果，
- 当前 blocker 或 waiting reason，
- 下一步计划。

`progress.md` 是 live operator summary 的 canonical markdown 视图；任务处于 `Blocked` 或 `Waiting` 时也必须保持可读。
若 parent 通过 `child_blocked_resolution.action=parent_takeover` 吸收 child 工作，则 `child_blocked_resolution.details.follow_up_ref` 固定为 `progress.md`，且 `Current Status` / `Next Step` 必须反映已接回 parent 主循环的动作。

v0.1 最低 section contract：

- `## Objective`
- `## Repo Bearings`
- `## Current Status`
- `## Current Sprint`
- `## Project Focus`
- `## Latest Verification`
- `## Execution Plan`
- `## Review And Completion`
- `## Recent Repairs`
- `## Latest Evidence`
- `## Next Step`

其中 `Repo Bearings` 必须直接暴露 baseline / checkpoint 已经识别出的 repo truth refs、repo-owned command hints、以及轻量 git bearings（例如 branch / head / dirty summary / changed paths）。`Current Status` 必须显式包含 phase/state，以及当前 `status_reason_code` 或 wait/watch 指针。若 task 绑定到 mission，相关 surface 必须能回链 `.ngen/missions/<mission_id>/mission.json`、`validation_contract.json` 与最新 `validation_runs.jsonl#validation_run_id=...`，但不应把 mission prose 当作 task truth。若 `state.current_step_id` 可解析到 `plan.json`，`progress.md` 与 `handoff.md` 里的 `Current Step` 应同时带出 step title，而不是只显示裸 step id。若 `plan.json.current_execution_step_id` 与 `plan.json.current_system_step_id` 都存在，`progress.md` / `handoff.md` 还必须显式带出 `Current Execution Step` 与 `Current Gate`。`Current Sprint` section 当前也必须直接消费 `sprint/latest.json`：至少显式带出 summary、objective、boundary、primary criterion、completion signals 与 deferred criteria，避免 operator/provider 在 fresh context 下重新摊开整个 task。`Project Focus` section 当前也属于 active contract：它必须直接消费 task-local `project_focus`，显式带出当前 task 在 workspace project graph 里的 primary step / branch、bound steps / branches、upstream dependencies、unmet dependency ids、workspace ready/blocked pointers，以及可回链的 project refs，避免长时任务恢复时又把 sibling branch / downstream step 误当成当前 scope。若 mutable lane 已进入 graph-capable contract，`Current Status` 还必须显式带出 `plan revision`、ready execution summary、blocked execution summary，以及 `last_mutation_ref`。`Criteria Snapshot` section 当前也应直接消费 `criteria/latest.json` 的 acceptance-ledger 字段，至少显式带出 current criterion、passing/open count 与仍未通过的 criterion 列表，而不是只显示裸 `met/open`。`Execution Plan` section 必须把 mutable execution lane 渲染为人类可读 checklist / task graph，而不是要求操作者自己回看 `plan.json`；有 `parent_step_id` 时必须体现层级，有 `depends_on` / `priority` 时也必须渲染出来。`Latest Verification`、`Review And Completion` 与 `Recent Repairs` 现在也属于 active contract，因为 long-horizon continuity 不能只靠 refs 回放；operator 与 provider 都需要一个直接可读的最近闭环摘要。

### `handoff.md` 的必答问题

- 请求是什么，
- 完成了什么，
- 证据在哪里，
- 当前 criteria / completion gate 到什么状态，
- 还有什么没解决，
- 下一个 operator 或 agent 应该怎么继续。

`Done` 时要求固定 sections；中间 handoff 建议也沿用同一骨架：

- Task Summary
- Repo Bearings
- Status
- Current Sprint
- Project Focus
- Mission（仅当 task 绑定 `.ngen/missions/<mission_id>/...` 时出现）
- Evidence
- Changed Files Or Touched Areas
- Open Risks
- Resume Instructions

若 task 已写出 `workspace_edits.jsonl`，`Changed Files Or Touched Areas` 应优先列出其中的实际文件变更，而不是只回放 baseline repo refs。

若 `diagnostics/quality-latest.json.status != "clear"` 或 `review_required=true`，`progress.md` 与 `handoff.md` 必须渲染 `Quality Diagnostics` section，列出 status、recommended action、changed paths、test-file changes、scope drift paths 与 finding summaries。quality section 只读 artifact truth，不直接替代 review verdict。

若 task 绑定到 mission，`progress.md`、`context/summary.md` 与后续生成的 `handoff.md` 必须渲染 `Mission` section，至少包含 mission ref、status、current milestone、validation contract ref、feature/milestone coverage 与 latest validation status/ref。该 section 只读 `.ngen/missions/<mission_id>/...` artifacts，不替代 root task verifier / review / completion truth。

## 16A. `continuity/latest.json` 与 `continuity/history.jsonl`

除了面向人类的 `progress.md` / `handoff.md`，runtime 现在还必须维护一份 machine-readable continuity ledger：

- `continuity/latest.json`
  - 当前最新的结构化 restart snapshot
- `continuity/history.jsonl`
  - 每次 narrative sync 追加一条 append-only continuity record

这组 artifact 的职责是把“下一轮 fresh context 怎么重新接管当前 sprint”收成结构化 contract，而不是让 provider 只从 Markdown prose 里猜。

`continuity/latest.json` 最低字段应覆盖：

- 当前 `phase/state/status_reason_code`
- `summary` / `next_step`
- `current_focus`
  - 至少包含 current step、current execution step、current gate、当前 open criteria focus、working set paths，以及 task-scoped `project_focus`
- `startup_checklist`
  - 至少包含 `progress.md`、`sprint/latest.json`、`context/summary.md`、`criteria/latest.json` 这类 ref-backed restart step；若 task 已绑定 project graph，还必须包含 `workspace:.ngen/project/project.json` 这类 project ref-backed restart step；若 baseline 已捕获 repo bearings，还应包含轻量 VCS bearings 命令与 repo-owned setup / verifier command hints
- verifier / review / completion 的最新状态摘要
- continuity refs

## 17. context pack 结构

Context pack 是单轮模型输入合同。

### 17.1 直接借鉴 Codex 的 Context OS 边界

NGEN 在 context assembly 层直接借鉴 Codex 的以下形状，并收紧为 v0.1 合同：

- project docs / `AGENTS.md` 按 ancestor top-down merge 发现与装配，不做“谁更像当前问题”式排序。
- skills / custom prompts 是独立 instruction layer，位于 project docs 之后、task artifacts 之前。
- 绝对上下文预算上限来自模型级字段，例如 `model_context_window` 与 `auto_compact_token_limit`；section 百分比只负责在该上限内切分。
- compaction 使用固定 owner 的 `compact_prompt` / summarization contract，而不是为不同 surface 临时拼装不同压缩提示。

以下部分不直接借鉴：

- thread/session/history 自身作为 canonical truth，
- rollout JSONL + SQLite thread index 的双持久化真相结构，
- session-local `ContextManager` 与 `response_id` / bookmark continuation 语义，
- compaction 后的 thread item 语义，
- session startup prewarm、memory phase-2 consolidation agent 以及任何只活在内存中的 conversation registry。

逻辑分段：

1. instruction stack（base instructions、policy / sandbox guidance、mode-specific developer instructions、project docs / `AGENTS.md`、skills / custom prompts），
2. task spec、baseline and success criteria，
3. current plan slice，
4. recent verified observations 与 criteria truth，
5. active blockers / open findings / pending completion gaps，
6. mission contract / current milestone / latest validation finding refs when the task is mission-bound，
7. repo excerpts，
8. workspace memory snapshot，
9. compacted task-local memory summary，
10. next action request。

示例 envelope：

```json
{
  "schema_version": 1,
  "task_id": "TASK-001",
  "profile": "coding",
  "phase": "Execute",
  "budgets": {
    "model_context_window": 120000,
    "auto_compact_token_limit": 90000,
    "hard_stop_ratio": 0.9
  },
  "sections": [
    {"name": "instructions", "tokens": 900, "refs": ["workspace:AGENTS.md", "workspace:docs/07-verification-security-and-ops.md"]},
    {"name": "task", "tokens": 900, "refs": ["task.json", "baseline.json"]},
    {"name": "plan", "tokens": 700, "refs": ["plan.json"]},
    {"name": "observations", "tokens": 2200, "refs": ["events.jsonl", "verification/latest.json", "criteria/latest.json"]},
    {"name": "repo_context", "tokens": 4800, "refs": ["workspace:README.md", "workspace:docs/04-runtime.md"]},
    {"name": "workspace_memory", "tokens": 900, "refs": ["workspace:.ngen/memory/MEMORY.md"]},
    {"name": "memory_summary", "tokens": 1000, "refs": ["context/summary.md"]}
  ]
}
```

`context/latest-pack.json` 必须持久化为 pack 摘要，而不是完整 prompt transcript。其最小合同为：

```json
{
  "schema_version": 1,
  "task_id": "TASK-001",
  "pack_id": "PACK-014",
  "phase": "Execute",
  "state": "Active",
  "built_at": "2026-03-18T09:18:00Z",
  "updated_at": "2026-03-18T09:18:00Z",
  "summary": "Verifier still fails on retry drift after the second repair attempt.",
  "next_step": "Inspect the retry helper, sync docs/config references, then rerun go test.",
  "based_on_refs": [
    "task.json",
    "plan.json",
    "verification/latest.json"
  ],
  "included_refs": [
    "task.json",
    "plan.json",
    "workspace:.ngen/project/project.json",
    "verification/latest.json",
    "context/summary.md",
    "workspace:.ngen/memory/MEMORY.md"
  ],
  "sections": [
    {"name": "task", "token_budget": 900, "actual_tokens": 740, "refs": ["task.json", "baseline.json", "criteria/latest.json"]},
    {"name": "plan", "token_budget": 700, "actual_tokens": 120, "refs": ["plan.json"]},
    {"name": "project", "token_budget": 700, "actual_tokens": 150, "refs": ["workspace:.ngen/project/project.json"]},
    {"name": "observations", "token_budget": 2200, "actual_tokens": 280, "refs": ["verification/latest.json", "workspace_edits.jsonl#edit_record_id=EDITREC-001"]},
    {"name": "workspace_memory", "token_budget": 900, "actual_tokens": 110, "refs": ["workspace:.ngen/memory/MEMORY.md"]},
    {"name": "memory_summary", "token_budget": 1000, "actual_tokens": 210, "refs": ["context/summary.md"]}
  ],
  "compaction": {
    "performed": true,
    "summary_ref": "context/summary.md"
  },
  "project_focus": {
    "primary_step_id": "phase.impl",
    "primary_branch_id": "branch.impl",
    "depends_on_step_ids": ["phase.repo_truth"],
    "unmet_dependency_step_ids": ["phase.repo_truth"],
    "dependencies_satisfied": false,
    "refs": ["workspace:.ngen/project/project.json"]
  },
  "status_reason_code": ""
}
```

说明：

- `actual_tokens` 当前是 runtime 本地的启发式估算值，用于审计 section 规模，不是假装精确拥有远端模型 tokenizer 真相。
- `summary` / `next_step` / `included_refs` 现在属于 active field；它们让 provider 与 operator 可以先用 pack 续上上下文，再按 refs 回看细节。
- `project_focus` 与 `project` section 现在也属于 active field；它们把 workspace-level orchestration truth 压成当前 task 的短视距 project contract，避免 provider/operator 在 fresh context 下自己从整张 project graph 重建当前 step / branch / dependency 边界。

## 18. context assembly 规则

Context OS 每轮按以下顺序构建输入：

1. base instructions，
2. policy / sandbox guidance，
3. collaboration mode developer instructions，
4. project docs / `AGENTS.md` / user instructions，
5. skills / custom prompts，
6. task objective / constraints / success criteria / baseline，
7. 当前 step 与最近 plan delta，
8. 最近一次成功验证、criteria truth 与当前失败点，
9. 当前 blocker / waiting / review findings / latest completion gaps，
10. 与当前 step 强相关的 repo truth，
11. workspace memory summary / topics，
12. compacted task-local memory summary，
13. 下一动作要求。

原则：

- 当前 blocker 和 exact failure strings 优先级高于旧历史，
- instruction stack 的顺序必须稳定；project docs 和 skills 是 instructions，不是 canonical task truth，
- project docs / `AGENTS.md` discovery 必须按 ancestor top-down merge，最具体的 doc 最后出现并覆盖更广泛规则，
- 如果未来支持 Claude-style instruction imports 或 external-agent config migration，它们只能扩充 project-doc / custom-prompt 的发现集合；导入后的内容仍属于 instructions layer，不得直接改写 task artifacts，
- 低频 setup/manual/self-help 内容默认不进 base prompt；它们应通过 project docs、skills、role files 或 guide-style subagent 做 progressive disclosure，
- repo excerpts、prompt-visible path discovery 与 additional roots 必须先统一经过 visibility deny rules 过滤，再进入 context pack，
- visibility deny 的匹配必须基于 clean + symlink resolution 后的规范化路径，并统一转换为 slash-separated 的 root-relative 形式；显式指向 deny 路径的 refs 必须被拒绝，而不是“尽量截短”后继续注入，
- coding repair loop 里的 bounded observation commands 也属于同一条 visibility contract；显式读取 `.ngen/` 或其他 deny path 的 operand 必须被 runtime 拒绝并记录成失败的 command artifact。工具级绕过也必须拒绝：`rg` 不允许 hidden/ignored/symlink traversal flag，`rg --files` path operand 仍要校验，且 broad `rg` / `ls` 不能覆盖非隐藏 deny root；`find` 不允许 broad root 覆盖 deny path，除非后续实现能验证 deny pruning 语义；`go` observation 不允许 verifier / build / mutating env/list flags；`git` observation 不允许 `--no-index`、external diff/textconv、output files、`rev:path` content reads、ignored listing 或绕过 `--` 的 deny-path pathspec，
- workspace snapshot、workspace edit 与 worker reconcile 必须保持 no-follow 语义：snapshot 跳过 symlink 并在 collection metadata 中报告 omission；workspace write/delete/patch 与 worker reconcile auto-apply 拒绝中间目录 symlink、最终 symlink 和 workspace 外路径，
- `find` observation parsing 必须保守：path operands 只能位于 expression 前，`-H` / `-L` / `-follow`、读取外部 path operand 的 predicate 与未知 predicate 不得进入 provider-visible workspace evidence，
- Context OS 应优先保留足以继续搜索和定位的线索，而不是把大块搜索结果或整段手册预先塞满 context，
- provider cache 命中率也属于 context assembly 的工程约束：动态任务事实应留在 artifact/user-context 段，不得为了方便塞进 system prompt；对支持 `cache_control` 的 Anthropic Messages 请求，runtime 可以在不改变 prompt 文本语义的前提下把稳定 instruction/system 段、低波动 artifact JSON prefix 与 volatile artifact JSON tail 拆成 text blocks 并设置 provider-native cache breakpoint；拼接后的 text 必须等于原 prompt，且这些 breakpoint 只是请求层提示，不得成为 canonical task truth，
- task-local compaction 当前是 deterministic narrative artifact rendering：`context/summary.md` 必须由当前 artifact truth 生成并与 `context/latest-pack.json` / `continuity/latest.json` / `sprint/latest.json` 同步写回。不得把 OpenClacky-style compression instruction、idle summarizer output 或其他 provider-generated compression result 藏进 system prompt、session hidden state 或未记录的 provider-visible context，
- baseline 与当前 step 不一致时，以新证据覆盖 baseline，并同步写回 baseline 或 decisions artifact，
- workspace memory 只能提供辅助线索；与当前 task-local evidence 冲突时必须降级或标记 stale，
- `continuity/latest.json` 是 task-local structured restart ledger；它和 `context/latest-pack.json` 服务同一 continuity slice，但前者更偏 machine-readable sprint/focus/checklist contract，后者更偏 prompt budget / section composition contract，
- 当前 step 相关代码优先于全仓库背景，
- 固定 turn reminders 或 TodoWrite 风格共享清单不是 canonical memory；如果实验性存在，它们也不能拥有 task continuity truth，
- 已落盘且不再影响当前决策的长输出可以用 refs 替代正文。

## 19. 预算策略

v0.1 默认预算分配：

- 先由模型级字段确定绝对预算上限：`model_context_window` 与 `auto_compact_token_limit`
- 再在 `auto_compact_token_limit` 内做 section allocation

- pinned rules and profile: 10%
- task spec and current plan: 15%
- fresh observations and failure evidence: 25%
- repo excerpts: 25%
- workspace memory snapshot: 8%
- compacted task-local memory summary: 7%
- reserve for output and tool schemas: 10%

这些是默认值，不是不可变真理。

## 20. compaction pipeline

context compaction 必须显式进行：

1. 删除已被 durable artifact 覆盖的过期临时观察，
2. 把已完成步骤压缩进 `context/summary.md`，
3. 用 refs + key lines 替代大块工具输出，
4. 保留当前 blockers 的 exact failure strings，
5. 保留未解决 findings、待审批事项、criteria gaps 和当前 step 细节，
6. 写回 `context/latest-pack.json`。

compaction prompt contract：

- 当前 runtime 的 compaction 是 deterministic renderer，不调用 provider。
- 若未来引入 model-backed compaction，必须使用固定 owner 的 `compact_prompt` 或等价配置字段，并把 provider call、usage、输入 refs、输出 refs、取消/失败状态写成 task-local artifact/event；不允许因为 CLI / TUI / ACP surface 不同而切换成不同的 summarization semantics。
- idle/background compaction 只有在具备显式事件、checkpoint、可取消进度、provider usage 记录和同 pass artifact write-back 时才可启用；它不得改变 system prompt、不得产生 hidden chat truth，也不得绕过 verifier/review/approval gates。
- compaction 输出必须是 task-local summary artifact，不得直接重写 workspace memory。

workspace memory 不属于单轮 compaction 的直接改写目标；它应通过独立 memory pipeline 刷新，而不是被某个活跃 task 临时覆盖。

## 21. promotion rules

并非所有内容都应进入 durable memory。满足以下条件之一时应 promote：

- resume 所必需，
- verification 或 review 需要，
- 会改变后续行为的决策，
- blocker、approval、policy outcome，
- replay 语义、criteria truth 或 completion gate 会依赖它，
- handoff 需要的关键结论，
- worker contract 的输入输出边界。

workspace memory 的 promote 规则额外要求：

- workspace memory 可以在 active task 中通过独立 memory pipeline 追加 entry，例如 operator `memory promote`、ACP `memory.promote` 或 provider `memory_promote`；它不要求任务先进入 `Done`，但仍不能反向拥有 task truth，
- memory entry 必须保留 scope / path / profile / provider mode / confidence / freshness metadata；path-scoped memory 在对应 workspace path 消失时必须以 stale 标签进入 `MEMORY.md` 和 provider-visible `workspace_memory`，
- 只有跨 task 复用价值明确的结论才应进入 `.ngen/memory/`，
- memory promote 必须经过 redaction，
- “当前任务仍未验证的推断”不得直接写入 `MEMORY.md`。

## 22. retention 与轮转

v0.1 默认不引入数据库，但需要控制 artifact 膨胀：

- `events.jsonl`、`decisions.jsonl`、`findings.jsonl` 允许长期追加，
- 大型原始输出保留在 `evidence/` 并通过 refs 引用，
- `criteria/latest.json` 与 `completion/latest.json` 保留最新聚合结果，历史由 events、verification 与 review refs 提供，
- `verification/latest.json` 和 `reviews/latest.json` 保存最新状态，历史进入 `history.jsonl`，
- context pack 只保留最新摘要和 compacted summary，而不是无限堆叠完整 prompt。
- workspace memory 采用“最近 task summaries + consolidated topics”的 retention 策略，避免无限增长为第二份 transcript 仓库。
