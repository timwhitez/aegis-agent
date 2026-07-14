# Go CLI Agent Phase Plan

## 1. 总原则

开发必须按 phase 推进。每个 phase 都需要：

- 明确交付面
- 明确不做的内容
- 自动测试
- 手工验证

当前项目明确分为两层：

- Web-first core phases: Phase 0-10 + Phase 15 的默认本地 Web 控制台
- Advanced / extension phases: Phase 11-14 和 Phase 16+

默认规则：

- 先把 Phase 0-10 做实
- Web-first v1 还必须把 Phase 15 的本地 Web 控制台做成默认 operator surface
- README、脚本、帮助文本、默认 smoke 路径以 Web-first 为主，同时保留 CLI fallback
- Phase 11-14 / 16+ 可以继续存在，但高级调参与内部 payload 不允许主导默认页面

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

## 13. Web-first v1 当前停止规则

Web-first v1 的默认完成标准不是停在 Phase 10；它要求 Phase 0-10 的 runtime / provider / CLI 基座稳定，并把 Phase 15 的本地 Web 控制台纳入默认验收。

只有以下条件都满足，才认为 Web-first v1 可交付：

- spec / README / AGENTS 对齐
- `go test ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...` 通过
- `gofmt -l` 无漂移
- `node --check internal/webconsole/assets/*.js` 通过
- `node --test validation/scripts/webconsole_utils_test.mjs` 通过
- Web 控制台 embedded assets、本地启动、session start / steer / continue、Goal / Plan Mode 基础控制、Settings provider/model 配置和 queue job 提交主路径通过
- `run` / `exec` / `steer` / `continue` 主路径通过
- provider probe / doctor 主路径通过

## 14. Web-first v1 收敛加固

Phase 0-10 与默认 Web 控制台之后允许做收敛加固，但加固必须满足两个条件：

- 不把 runtime 改造成固定 DAG 或重型 workflow engine
- 不让 queue / delegate / isolation / TUI 高级入口反向主导默认 Web 页面

当前已纳入 Web-first v1 收敛口径的加固项：

- session contract snapshot：`contract.json` 与 `artifacts/contract-history.jsonl`
- session goal snapshot：`goal.json` 与 `artifacts/goal-history.jsonl`，包含 objective、status、usage accounting、success criteria、validation plan、completion audit 与内部结构化计划摘要；默认用户入口不暴露 Goal/Mission 分叉或预算表单
- session Plan Mode gate：`planmode.json`、`artifacts/planmode-history.jsonl`、`artifacts/planmode-plan.md`，通过 CLI/Web/API 显式进入，审批前只暴露只读/规划工具并由 `submit_plan` 进入 `awaiting_approval`
- required artifact tracker：`artifact-tracker.json`
- centralized completion controller：统一复用既有 guard，并补充显式 artifact / parent coordination gate
- active goal completion gate：active goal 下 `finish` 前必须先完成目标审计并由模型调用 `update_goal(status="complete")`；completion evidence 和 criteria / validation 状态必须回写 `goal.json` 快照；若高级入口设置了预算，budget_limited 只能触发 wrap-up，不等同完成
- explicit model park：任务未完成但受外部条件阻塞时，模型可调用 `await_input` 把 session 停靠为可恢复状态；该路径不触发 completed、不改变 active Goal，也不要求伪造 completion audit
- mission validation coverage：`goal plan check` / Web detail / approval gate 必须从同一份 `goal.json` 派生 coverage report；mission plan approval 默认阻断 uncovered 或 invalid validation contract，除非 CLI/API 明确 override
- structured goal progress：模型工具 `record_goal_progress` 可追加 feature / milestone / validation / evaluator / child / queue / artifact / command / blocker / handoff / budget wrap-up 事实，并同步 `goal-history.jsonl`、`goal.json`、`session.md` / checkpoint；该工具不能改变 objective 或完成状态
- provider attempt ledger：`provider-attempts.jsonl`
- operator session summary：`session.md`
- long-run checkpoint：`checkpoints/longrun-latest.json`
- explicit parent coordination：`parent-coordination.json`
- workspace extension trust discovery：`.agent` 默认 discovery-only，显式 trust 前不加载
- optional Linux shell sandbox：`runtime.shell.sandbox: bwrap`

验收补充：

- `go test ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...` 覆盖新增持久化、gate 与 repo-owned validation helper
- `node --check internal/webconsole/assets/*.js` 覆盖内嵌前端语法，`node --test validation/scripts/webconsole_utils_test.mjs` 覆盖前端状态机、异步竞态与模块 API contract
- WebConsole 资源不得依赖外部 CDN，Markdown 渲染必须走本地 HTML/XSS sanitizer；这是浏览器注入防护，不是内容脱敏规则
- goal 的验收覆盖 store round-trip、model tools、Web start payload、goal REST endpoints、CLI flag / `goal` 命令、runtime prompt/accounting/completion gate；Web 启动默认只需要 Goal 开关 + prompt；mission plan approval 必须通过 linked Plan Mode 产生真实 pending gate
- Web-first mission controls 至少覆盖 Goal inspector 的 plan show/check/approve 与 validation coverage 展示；CLI `goal plan show/check/approve` 与 `goal validation show` 作为 fallback 读取 / 更新同一份 session store 权威事实，不维护第二套状态，也不引入 TUI
- Plan Mode 的验收覆盖 store round-trip、tool schema 裁剪、CompletionController gate、`submit_plan` 同批 tool result 补偿、`request_user_input` active/recovery 路径、CLI `--plan/--plan-only/--approve-plan`、Web Plan inspector 与 parent-linked queue/delegate rejection

## 15. Extension Phases

以下 phase 继续保留，但需要区分两类：

- Phase 15 已上升为 Web-first v1 的默认 app surface
- Phase 11-13 可作为 large-project profile 的高级能力面，由 Web 提供轻量入口/观测，细粒度调参继续走 CLI / API
- Phase 14 和 Phase 16+ 仍属于实验扩展面

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

- `go-cli-agent web`
- `experimental web` 作为旧入口兼容别名
- 本地 HTTP API
- 内嵌静态单页前端
- session / queue / children / task board / timeline 可视化
- Web 发起的 `start` / `continue` / `steer`；queue submit 保留在 CLI/API advanced 面，默认前端不提供独立提交表单
- Web 发起的 `start` 可通过一个 optional Goal 开关附带 prompt-derived goal；session inspector 可显示 goal 状态、criteria、validation、agent 拆分出的 features/milestones，并提供用户控制的 pause/resume/clear/complete/approve plan 操作
- Workspace 面板提供受限本地文件操作：预览、下载、上传、文件重命名、创建目录，以及删除单个或多个文件/目录；所有写操作继续受 workspace root、敏感路径、symlink、请求体上限和审计约束
- 可配置并发 worker pool

### 15.6 Phase 16+ 的规则

- 即使仓库里已有对应实现，也只能作为高级或实验扩展
- 不得反过来要求默认 Web 页面、帮助文本、smoke 脚本都围绕内部高级能力设计
- 若用户明确要求推进扩展 phase，必须先确认 core 主路径未被破坏
