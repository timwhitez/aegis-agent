# 产品定位与范围

## 1. 当前产品定位

NGEN 当前不是“生产级完整自治 coding agent 产品”，而是已经穿过 foundation、进入 post-foundation integrated baseline 的本地内核：

- 它负责 durable task truth，
- 负责 phase/state 生命周期，
- 负责 verifier、review、handoff 与 done gate，
- 负责 watch 后恢复，
- 负责 provider-driven `auto` dispatch、ACP session、interactive terminal、full-screen TUI、bounded worker 与 memory promote，
- 负责 mission-level validation contract 的最小 active lane：large task 可以先落 `.ngen/missions/<mission_id>/validation_contract.json`，再通过 root task、feature/milestone records 与 independent validation run 收敛，
- 负责让 operator 只看 `.ngen/` 就能理解任务状态。

## 2. 当前要解决的问题

本轮实现聚焦于以下真实问题：

- 任务执行后没有 durable 记录，重启后难以恢复；
- 完成声明缺少 verifier/review 约束，容易 false done；
- 等待外部事件后没有稳定的 watch/scheduler 恢复路径；
- progress、handoff、failure reason 常常散落在临时输出里，不能作为 durable truth；
- 文档和代码之间缺少一条真正可验收的基础闭环。

## 3. Foundation v0.1 目标

### G1. Durable Task Runtime

每个 task 都有稳定的 `.ngen/tasks/<task_id>/` 目录，任务状态、验证结果、review、handoff 与 completion claim 都可恢复。

### G2. Verification-Centered Completion

`Done` 必须经过 verifier、review 和 handoff 三重约束，不能靠模型或操作员口头宣布。

### G3. Operator-Readable Truth

`status`、`events tail`、`progress.md`、`handoff.md` 与 JSON artifacts 足以解释当前状态。

### G4. Watch And Resume

任务可以进入 `Waiting`，并通过 durable `watch` + `scheduler tick --once` 被重新唤醒。

### G5. Integrated Minimal Surface

在不丢掉 foundation kernel 的前提下，把 provider、ACP、terminal、worker、hook、visibility、memory、extended profiles 以最小实现接入同一条 artifact truth。

## 4. 当前 in scope

- `task create`
- `mission create` / `mission approve` / `mission run` / `mission validate` / `mission status`
- `run`
- `resume`
- `auto`
- `status`
- `review`
- `events tail`
- `handoff export`
- `watch set` / `watch ls` / `watch cancel`
- `scheduler tick --once`
- `coding`
- `general_execution/docs_lite`
- `security_review`
- `reviewer`
- ACP stdio server
- interactive terminal
- full-screen TUI
- bounded workers
- hooks / visibility / memory
- `yolo`
- baseline / verification / review / completion / handoff artifacts
- 明确的 `Active` / `Blocked` / `Waiting` / `Done` / `Failed` / `Aborted`
- approval artifacts 的最小 durable 语义

## 5. 当前仍未实现的 richer hardening

- richer role-file inheritance / discovery UX，超出当前内建 role contract 水合与 provider action gate
- broader provider matrix，超出 builtin/command、OpenAI-compatible Chat Completions、OpenAI Responses 与 Anthropic Messages 当前范围
- deeper visibility / memory governance
- stronger child sandbox isolation
- richer mission decomposition/validator worker automation 超出当前第一片；当前 active subset 已提供 CLI + `/missions` compact entrypoint、`mission.json.role_plan` 三角色 model snapshot、orchestrator bounded provider-decision pass、mission-owned worker model routing、deterministic validator gate，以及显式 validators model 下的 dedicated read-only model validator

## 6. 设计约束

- 保留 foundation kernel，不做失控的“大一统产品”。
- 一切当前实现功能都必须有 owner doc、artifact contract 和测试。
- richer design 可以保留，但不能继续和当前 active contract 混写。
