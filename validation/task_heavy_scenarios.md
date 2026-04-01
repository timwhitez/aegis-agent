# Task-Heavy Real Scenario Matrix

## Goal

这组矩阵不是 round31 那种以 proof/audit 收口为主的 26 场景复刻，而是更偏真实复杂开发任务族的 task-heavy live 验证。

设计目标有三条：

1. 用约 20 个真实任务场景覆盖多语言修复、review、interrupt/resume、delegate、queue、browser UI、retry/queue operator 路径。
2. 每个场景都把 prompt、raw output、artifact、post-check、session evidence 和问题备注落到自己的 case 目录。
3. 优先发现“真实复杂任务下还会不会掉链子”的问题，而不是再做一轮仅靠 review prose 的 completeness 收口。

共享前提：

- provider: `openai-compatible`
- wire API: `responses`
- model: `gpt-5.4`
- config: `validation/config.openai-compatible.yaml`
- session root: `/root/.go-cli-agent/validation-sessions`
- run 入口: `validation/run_round61_task_heavy_real_matrix.sh`

## Case Layout

每个场景都会写入：

```text
validation/runs/<run-id>/cases/<case-id>/
  prompt.txt
  raw.jsonl | raw.json
  artifact.md
  note.md
  postcheck.txt
  evidence/
```

主 run 目录额外保留：

- `notes/preflight-index.tsv`
- `notes/preflight-task-heavy-proof-tests.md`
- `notes/preflight-gap-proof-summary.md`
- `notes/scenario-index.tsv`
- `notes/case-buckets.md`
- `SUMMARY.md`
- `ISSUES.md`
- `workspaces/`

## Scenario List

### TT01 Python Smallest Correct Fix

- workspace: `validation/workspaces/patch`
- mode: `exec`
- target: 真实最小修复、已有测试优先、`python -m unittest -q` 外部 post-check
- artifact: `reports/change-summary.md`

### TT02 Go Smallest Correct Fix

- workspace: `validation/workspaces/patch_go`
- mode: `exec`
- target: 真实最小修复、已有测试优先、`go test ./...` 外部 post-check
- artifact: `reports/change-summary.md`

### TT03 Incident Recovery With Durable Task Graph

- workspace: `validation/workspaces/incident`
- mode: `run -> continue`
- target: durable task graph、项目记忆栈、timeline/root-cause/recovery summary
- artifact: `reports/recovery-summary.md`

### TT04 Awaiting Input Docset Continuation

- workspace: `validation/workspaces/docset`
- mode: `run -> continue`
- target: 自然停顿、恢复、spec/plan/progress/validation 刷新
- artifact: `reports/continue-brief.md`

### TT05 Same-Task Go Repair With Two Interrupt Steers

- workspace: `validation/workspaces/platform_go` copy
- mode: `exec + 2x steer --interrupt`
- target: same-task 多包修复、todo/tasks、durable steer evidence、最终 `go test ./...`
- artifact: `reports/tt05-proof.md`

### TT06 Platform Go Multi-Package Repair

- workspace: `validation/workspaces/platform_go`
- mode: `run -> continue`
- target: 多包根因修复、narrow test first、repo-wide go test post-check
- artifact: `reports/change-summary.md`

### TT07 Platform Go Post-Fix Review

- workspace: `TT06` 产物工作区
- mode: `exec`
- target: findings-first post-fix review
- artifact: `reports/post-fix-review.md`

### TT08 Platform Python Multi-Module Repair

- workspace: `validation/workspaces/platform_py`
- mode: `run -> continue`
- target: 多模块修复、最窄 pytest 先行、`pytest -q` 外部 post-check
- artifact: `reports/change-summary.md`

### TT09 Platform Python Post-Fix Review

- workspace: `TT08` 产物工作区
- mode: `exec`
- target: findings-first post-fix review
- artifact: `reports/post-fix-review.md`

### TT10 Nested API Review

- workspace: `validation/workspaces/nested_review/services/api`
- mode: `exec`
- target: 深层 `AGENTS.md` 作用域、API review、目录约束
- artifact: `reports/api-review.md`

### TT11 Foreground Delegated Review With Role And Children Proof

- workspace: platform-go copy
- mode: `exec + experimental delegate --role evaluator --isolation copy + experimental children`
- target: parent handoff、explicit evaluator role、copy-isolated child workdir、child visible outputs、`children --json` 可见性、delegated validation refresh
- artifact: `reports/delegate-review.md`

### TT12 Background Queue Review With Role And Children Proof

- workspace: platform-py copy
- mode: `exec + experimental queue submit --role evaluator --isolation copy / worker + experimental children`
- target: queued evaluator child、background notification durability、copy-isolated child workdir、`children --json` 可见性、visible paths、并把 child 已验证的关键失败断言直接带到 case artifact
- artifact: direct run artifact

### TT13 Exact-Template Audit Guard

- workspace: `go-cli-agent/`
- mode: `exec`
- target: review artifact exact opening block / first-section ordering / required literal anchor enforcement
- artifact: direct run artifact

### TT14 Forced Compaction And Proof-Carry

- workspace: `go-cli-agent/`
- mode: `exec` with low-compact config
- target: `compact.started` / `compact.finished`、proof carry-forward、summary quality、以及 parent artifact 内联 owning-runtime 代码锚点
- artifact: direct run artifact

### TT15 Interrupt -> Resume -> Completion

- workspace: docset copy
- mode: `run + steer --interrupt + continue`
- target: scope pivot 后刷新 `reports/spec.md` / `reports/plan.md` / `reports/progress.md` / `reports/validation.md`，并最终 finish
- artifact: `reports/final-brief.md`

### TT16 Oversized Steer Rejection

- workspace: docset copy
- mode: delayed `exec` + invalid oversized `steer --json`
- target: `steer_input_too_large` 结构化错误、pre-queue durable evidence
- artifact: direct run artifact

### TT17 Provider Cancel And Interrupt Preemption

- workspace: same delayed session as `TT16`
- mode: valid `steer --interrupt`
- target: `provider.cancelled`、`session.steer.accepted`、transport-side cancellation evidence
- artifact: direct run artifact

### TT18 Web Console Deep Smoke

- workspace: `go-cli-agent/`
- mode: focused operator rerun
- target: embedded assets、role-aware start、session sidebar filter/reveal、queue pin/reveal、overview recent-job/feed/failed-job drilldown、worker last-job drilldown、tasks/children/queue 标签切换、真实浏览器 continue/queue/refresh、console/runtime cleanliness，并把关键交互布尔项直接内联到 parent artifact
- artifact: focused follow-up derived summary

### TT19 Retry-Resume And Queue-Dedup Operator Proof

- workspace: `go-cli-agent/`
- mode: focused operator rerun
- target: durable retry restore、real `provider.retry`、failed queue canary、queue notification dedup、stale-running reconcile，并把 retry/dedup/failed-job 决定性 snippet 直接内联到 parent artifact
- artifact: focused follow-up derived summary

### TT20 Task-Heavy Readiness And Issue Inventory

- workspace: workspace root
- mode: `exec`
- target: 只基于本次 task-heavy run 的 case artifacts / notes 做 validated issue inventory，不把 benchmark-limited 结论伪装成 repo bug
- artifact: direct run artifact

## Current Emphasis

这组矩阵专门加厚三类旧边界：

- `interrupt -> resume -> completion` 的端到端 durable 留证
- `experimental web` 的真实浏览器 / operator 路径，而不只是内嵌资产 smoke；当前尤其强调 recent-job/feed/failed-job/worker-last-job 四类 queue drilldown 入口
- role-aware `delegate` / `queue` / `children` 可见性与 copy-isolation 证据
- 真实修复任务下的多包、多模块、多阶段 taskboard / steer / continue 行为
- delegated / background / focused subrun 的父级 artifact 会尽量内联决定性 snippet，避免 readiness 结论过度依赖下游路径跳转
- run 目录额外生成 `notes/case-buckets.md`，把 repaired seeded defect、review boundary、workflow proof、owning-runtime proof 四类 case 快速分桶

它不是 live competitor benchmark，也不代替 round31 的 proof-matrix；它的作用是把“真实开发任务族”这一层独立跑透，并把 case-level evidence 单独落盘。
