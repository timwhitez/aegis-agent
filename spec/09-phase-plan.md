# Go CLI Agent Phase Plan

## 1. 总原则

开发必须按 phase 推进。每个 phase 都需要：

- 明确交付面
- 明确不做的内容
- 自动测试
- 手工验证

当前项目明确分为两层：

- Core phases: Phase 0-10
- Extension phases: Phase 11-16

默认规则：

- 先把 Phase 0-10 做实
- README、脚本、帮助文本、默认 smoke 路径都围绕 Phase 0-10
- Phase 11+ 可以继续存在，但不允许主导当前产品叙事

## 2. Phase 0 - Spec Bundle

交付：

- 完整 `spec/`
- `README.md`
- `AGENTS.md`
- 参考资料到 spec 的收敛说明

完成标准：

- spec 自洽
- 关键设计点有出处或明确写出是 v1 取舍

## 3. Phase 1 - Minimal Loop

参考：

- `learn-claude-code` `s01`

交付：

- Go module
- `Runner`
- `Engine`
- `EventBus`
- `run` / `exec` 基础骨架
- fake provider

## 4. Phase 2 - Built-In Tools

参考：

- `learn-claude-code` `s02`

交付：

- `shell`
- `read_file`
- `write_file`
- `edit_file`
- `glob`
- `grep_files`
- `grep`
- `finish`
- path safety
- shell env allowlist

## 5. Phase 3 - Session Store, Output And Todo

交付：

- `session.json`
- `state.json`
- `messages.jsonl`
- `events.jsonl`
- `todo.json`
- `sessions` 命令
- 文本 / JSON 输出基础

## 6. Phase 4 - Skills And Project Docs

参考：

- `learn-claude-code` `s05`

交付：

- skill discovery
- `load_skill`
- `AGENTS.md` 指令链加载

## 7. Phase 5 - Context Compaction

参考：

- `learn-claude-code` `s06`

交付：

- provider 输入规模估算
- micro compaction
- transcript / summary artifact

## 8. Phase 6 - Task System

参考：

- `learn-claude-code` `s07`
- `opencode` 的 session todo 分层

交付：

- `tasks/`
- `task_create`
- `task_update`
- `task_list`
- `task_get`
- `tasks` 命令

## 9. Phase 7 - Provider Adapters

交付：

- OpenAI adapter
- Anthropic adapter
- Google adapter
- `openai-compatible` Responses 模式
- generation / reasoning 选项全链路传递

## 10. Phase 8 - Interrupt, Continue And Steer

交付：

- `continue`
- `steer`
- interrupt API
- `Esc` 监听
- `paused` / `awaiting_input`
- `control/steer.jsonl`
- queue-first steer
- `--interrupt` best-effort preemption
- 中断 tool result 补偿写回

## 11. Phase 9 - Hooks v1

参考：

- OpenCode hooks 的轻量裁剪版

交付：

- hook 配置解析
- event hooks
- transform hooks
- `command` / `inject` / `filter`

## 12. Phase 10 - Init, Docs, SDK Facade And Hardening

交付：

- `init`
- `.env.example`
- `config.example.yaml`
- 完整 README / AGENTS
- `build.sh`
- `test.sh`
- 稳定 `pkg` 入口
- no-TTY / WSL 基础退化路径

## 13. Core v1 当前停止规则

minimal core 的默认完成标准停在 Phase 10。

只有以下条件都满足，才认为 minimal core v1 可交付：

- spec / README / AGENTS 对齐
- `go test ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...` 通过
- `gofmt -l` 无漂移
- `run` / `exec` / `steer` / `continue` 主路径通过
- provider probe / doctor 主路径通过

## 14. Core v1 收敛加固

Phase 0-10 之后允许做 core 收敛加固，但加固必须满足两个条件：

- 不把 runtime 改造成固定 DAG 或重型 workflow engine
- 不让 experimental Web / queue / delegate 入口反向主导默认 CLI 叙事

当前已纳入 core v1 收敛口径的加固项：

- session contract snapshot：`contract.json` 与 `artifacts/contract-history.jsonl`
- session goal snapshot：`goal.json` 与 `artifacts/goal-history.jsonl`，包含 objective、status、usage accounting、success criteria、validation plan 与内部结构化计划摘要；默认用户入口不暴露 Goal/Mission 分叉或预算表单
- required artifact tracker：`artifact-tracker.json`
- centralized completion controller：统一复用既有 guard，并补充显式 artifact / parent coordination gate
- active goal completion gate：active goal 下 `finish` 前必须先完成目标审计并由模型调用 `update_goal(status="complete")`；若高级入口设置了预算，budget_limited 只能触发 wrap-up，不等同完成
- provider attempt ledger：`provider-attempts.jsonl`
- operator session summary：`session.md`
- long-run checkpoint：`checkpoints/longrun-latest.json`
- explicit parent coordination：`parent-coordination.json`
- workspace extension trust discovery：`.agent` 默认 discovery-only，显式 trust 前不加载
- optional Linux shell sandbox：`runtime.shell.sandbox: bwrap`

验收补充：

- `go test ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...` 覆盖新增持久化、gate 与 repo-owned validation helper
- `node --check internal/webconsole/assets/*.js` 覆盖内嵌前端语法
- WebConsole 资源不得依赖外部 CDN，Markdown 渲染必须走本地 HTML/XSS sanitizer；这是浏览器注入防护，不是内容脱敏规则
- goal 的验收覆盖 store round-trip、model tools、CLI flag / `goal` 命令、runtime prompt/accounting/completion gate、Web start payload 与 goal REST endpoints；Web 启动默认只需要 Goal 开关 + prompt

## 15. Extension Phases

以下 phase 继续保留，但需要区分两类：

- Phase 11-13 可作为 large-project profile 的已验证扩展面
- Phase 14 及之后仍属于实验扩展面

### 15.1 Phase 11 - Worktree Isolation

- `off` / `auto` / `git` / `copy`
- session metadata 中记录 isolation
- 当 large-project profile 被显式启用时，这一 phase 需要有真实隔离目录、requested/effective workdir 分离，以及不污染 parent workdir 的验证证据

### 15.2 Phase 12 - Multi-Agent Delegation

- `experimental delegate`
- `experimental children`
- `agent_spawn`
- `agent_status`
- `agent_list`
- 当 large-project profile 被显式启用时，这一 phase 需要有 parent/child linkage、child session durability、observability 和同步/异步 child 执行证据

### 15.3 Phase 13 - Background Queue

- `experimental queue submit`
- `experimental queue list`
- `experimental queue show`
- `experimental queue worker`
- parent background notification
- 当 large-project profile 被显式启用时，这一 phase 需要有真实 worker 消费、job/session 关联和 background notification 回流证据

### 15.4 Phase 14 - Terminal TUI

- `experimental tui`
- snapshot / interactive 观测面

### 15.5 Phase 15 - Web Console

- `experimental web`
- 本地 HTTP API
- 内嵌静态单页前端
- session / queue / children / task board / timeline 可视化
- Web 发起的 `start` / `continue` / `steer` / `experimental queue submit`
- Web 发起的 `start` 可通过一个 optional Goal 开关附带 prompt-derived goal；session inspector 可显示 goal 状态、criteria、validation、agent 拆分出的 features/milestones，并提供用户控制的 pause/resume/clear/complete/approve plan 操作
- 可配置并发 worker pool

### 15.6 Phase 16+ 的规则

- 即使仓库里已有对应实现，也只能作为实验扩展
- 不得反过来要求 core 文档、帮助文本、smoke 脚本都围绕它们设计
- 若用户明确要求推进扩展 phase，必须先确认 core 主路径未被破坏
