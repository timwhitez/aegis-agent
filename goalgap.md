# Goal / Mission 优化差距分析

本文根据用户补充的 Factory Missions 材料，对当前 `go-cli-agent` 的 Goal / Mission 实现做代码级检查，并给出后续优化方案。

本轮结论：当前仓库已经具备 durable Session Goal 的主干能力，不应再按旧 `dev.md` 的“尚未实现 session goal”判断。现在需要优化的是 Mission 风格长任务闭环：计划批准是否真的阻断执行、验证契约是否能被独立证据更新、worker / validator 交接是否有结构化事实源，以及 Web / CLI 是否能准确呈现这些事实。

2026-05-13 本轮收敛状态：

- 已修复 P0-1：`require_plan_approval` / `needs_approval` 现在会确保存在 linked Plan Mode；pending Plan Mode 继续复用既有 provider schema 裁剪与 `CompletionController` gate，mission plan approve 也会走 Plan Mode approval / continue 路径，而不是只改展示字段。本次三轮 review 发现并修复了 existing Plan Mode 复用边界：已批准/执行中的旧 Plan Mode 不会再被当作新的 `needs_approval` gate，未链接的 pending Plan Mode 会补 `linked_goal_id`。
- 已修复 P0-2 主路径：模型工具 `update_goal(status="complete")` 现在把 completion audit、evidence、criteria status 和 validation status 回写 `goal.json` 当前快照；Web Goal panel 可展示该快照，`session.md` 在下一次 summary refresh / finish 后反映同一事实。CLI / Web 的人工 `goal complete` 仍是 operator override，只改完成状态或写空 audit，不等价于模型完成审计。
- 未完成项仍是 P0-3 / P0-4 / P1：validation coverage checker、budget stop wrap-up、structured progress / handoff 工具、evaluator-specific evidence 关联和专用 CLI-first mission controls。

## 1. 设计边界

需要吸收的参考点：

- 人类定义目标，harness 提供可恢复、可验证、可观察的执行环境。
- coding 前先定义 validation contract，避免实现后自证正确。
- worker / validator 分离可以提高长任务质量，但应该是 model-led / user-directed，而不是 runtime 固定 DAG。
- structured handoff 必须落到 durable artifact / state，而不是只留在模型上下文。
- Mission Control 的核心价值是异步监督和事实展示，不是默认 Web-first 产品叙事。

当前项目必须保留的边界：

- 默认仍是 CLI-first core harness。
- Mission 不应成为单独的重型 workflow engine；当前正确方向是“Mission 收敛为 Goal 的内部结构化计划字段”。
- 不引入固定 orchestrator / worker / validator 三段 runner。
- 不强制 child agent / queue；是否委派继续由模型或用户决定。
- WebConsole 只能作为 `experimental web`，不能反向主导 README、root help 或默认 smoke。
- provider 差异仍停留在 adapter 层，Goal / Mission 不应承载 provider-specific replay 逻辑。

## 2. 当前实现事实

### 2.1 已完成的基础能力

- `internal/session/goal.go` 已有 `SessionGoal`、`GoalCriterion`、`GoalValidation`、`MissionPlan`、`MissionFeature`、`MissionMilestone`、`MissionRole`、budget、history 和 `goal.json` / `artifacts/goal-history.jsonl` 存储逻辑。
- `internal/runtime/runner.go` 会在 session 创建后根据 `StartRequest.Goal` 创建 durable goal，并写 `goal.created` 事件。
- `internal/runtime/goal.go` 会把 active goal 注入 provider prompt，包含 objective、status、budget、success criteria、validation plan、mission features / milestones，并在 active goal 下提示模型先做 completion audit 再 `update_goal`。
- `internal/runtime/completion_controller.go` 已有 active goal finish gate：active goal 未 `complete` 时阻断 `finish`。
- `internal/tools/registry.go` 已注册 `get_goal`、`create_goal`、`update_goal`；模型只能用 `update_goal(status="complete")` 完成目标，pause / resume / clear 不暴露给模型。
- `internal/runtime/compaction.go`、`internal/runtime/session_summary.go` 已把 goal snapshot 写入 compaction summary、`session.md` 和 long-run checkpoint。
- `internal/webconsole/service.go` 已提供 goal REST 控制面和 mission plan / validation patch API。
- `internal/webconsole/assets/app.js` 已支持 Web start 时通过 Goal 开关把 prompt 作为 objective。
- `internal/webconsole/assets/session-view.js` 已能展示 goal 状态、budget、criteria、validation、features、milestones、roles，并提供 pause / resume / complete / clear / approve plan 操作。
- role provider override 已经存在，`planner` / `generator` / `evaluator` 必须显式选择，不从 `agent_name` 或 orchestrator / worker / validator 文案模糊推断。

### 2.2 当前实现与补充材料已经一致的点

- 一个 session 默认最多一个 current goal，符合轻量 Codex Goals 主干。
- Mission 字段存在于 Goal 内部，没有新增 `MissionState` 或独立数据库。
- `goal.json` 与 `goal-history.jsonl` 是文件事实源，WebConsole 只是本地控制面。
- `budget_limited` 不等于 complete，prompt 文本和状态模型都已经区分。
- `create_tasks_from_plan` 是显式高级开关，默认不会把 features 自动变成 task graph。
- `agent_role` 只支持 `planner` / `generator` / `evaluator`，符合当前项目克制的角色抽象。

## 3. 主要差距

### P0-1. `require_plan_approval` / `needs_approval` 目前只是状态，不是真正执行门禁

状态：已修复。

当前实现：

- `Store.EnsurePlanModeForGoal` 会在 goal/mission 要求审批时创建 linked Plan Mode；若已有 pending Plan Mode 但未链接当前 goal，会补写 `linked_goal_id`；若 mission 被重新置为 `needs_approval`，不会复用已 approved / executing 的旧 gate，而会创建新的 pending gate。
- `Runner.Start` 在 `--goal-plan-approval` 或等价 StartRequest goal 控制下自动创建 pending Plan Mode，批准前复用 Plan Mode 工具裁剪和执行门禁。
- `create_goal` tool 与 Web goal/mission patch 也会确保 linked Plan Mode，因此运行中创建的 mission approval 不再只是展示状态。
- `Continue --approve-plan` / Web Plan approve 会同步 `mission.plan.approved`，Web mission approve endpoint 在存在 linked Plan Mode 时走 Plan Mode approval / continue 路径。

剩余边界：

- 当前没有 validation coverage approval checker，P0-3 仍未完成。
- 如果 Plan Mode 还处于 `planning` / `awaiting_user_input`，mission approve 会拒绝并要求先提交 plan。

上一轮现状：

- `GoalControl.RequirePlanApproval` 可以通过 CLI / Web / tool 创建。
- mission goal 创建时，如果 `require_plan_approval=true`，`MissionPlan.PlanStatus` 会变成 `needs_approval`。
- Web 有 `POST /api/sessions/{id}/mission/plan/approve`，可以把 plan 标为 `approved`。
- 但 runtime gate 没有检查 mission plan 是否 `needs_approval`。`CompletionController` 只检查 `PlanMode` pending、required artifacts、parent coordination 和 active goal completion。
- `goalPromptContext` 只把 `Mission plan status: needs_approval` 写入 prompt，并没有阻断 shell / write / edit / todo / task / agent / queue 工具。

影响：

- 用户以为开启了 mission plan approval，但模型仍可能直接修改代码或执行 shell。
- 这与补充材料中“用户批准后系统自动执行”的关键边界不一致。
- 也会造成 `--goal-plan-approval` 语义不可靠：它看起来像门禁，实际只是展示状态。

已采用方案：

- 不新建一套 workflow gate，复用现有 Plan Mode 机制。
- 当 StartRequest 同时含 goal 且 `Goal.Control.RequirePlanApproval=true`，自动创建 linked Plan Mode，`linked_goal_id` 指向当前 goal。
- pending linked Plan Mode 下继续使用现有 provider schema 裁剪与 `CompletionController.planModeGate`，只允许 read/search/load_skill、只读 todo/task/feature-list、get_goal/get_plan_mode、request_user_input/submit_plan。
- `mission/plan/approve` 应调用或复用 Plan Mode approve transition，而不是只改 `MissionPlan.PlanStatus`。
- 如果用户只通过 REST patch 把 mission plan 改为 `needs_approval`，也要创建、恢复或重新创建 linked pending Plan Mode；否则该字段应明确命名为 display-only，避免误导。

本轮验收：

- `go-cli-agent run --goal ... --goal-mode mission --goal-plan-approval` 在 approval 前不能执行 `shell` / `write_file` / `edit_file` / `todo_write` / `task_create` / `update_goal` / `agent_spawn` / `agent_status` / `agent_list` / `finish`。
- Web start payload `{goal:{enabled:true, require_plan_approval:true}}` 进入 pending Plan Mode，session detail 同时显示 goal 与 plan mode。
- `POST /api/sessions/{id}/mission/plan/approve` 后 mutating tools 恢复可用，并写入 `mission.plan.approved` 与 `planmode.plan_approved`。
- 已批准/执行后的 mission 若重新 patch 为 `needs_approval`，会重新进入 pending Plan Mode，mutating tools 再次被 gate 阻断。

### P0-2. Completion evidence 只进 history，不回写 goal snapshot

状态：已修复。

当前实现：

- `SessionGoal` 增加 `completion_audit` 快照，包含 status、summary、evidence、completed_by、completed_at。
- `update_goal` payload 增加 `completion_summary`、`criteria_statuses`、`validation_statuses`，但仍只允许 `status="complete"`。
- `Store.CompleteGoal` 会同步更新 `goal.json` 的 completion audit、criteria evidence/status、validation evidence/status/last_run_at，并追加 `goal.completed` history。
- `session.md` 在 summary refresh / finish 后输出 completion evidence 数量与 summary；Web Goal panel 展示 completion audit evidence。
- CLI / Web 人工 complete 入口仍是用户覆盖动作：CLI 只写 complete 状态和时间，Web 写空 completion audit，不收集 detailed evidence / criteria status / validation status。

上一轮现状：

- `update_goal` 接收 `evidence` 参数。
- evidence 只写入 `artifacts/goal-history.jsonl` 的 `goal.completed` 事件。
- `goal.json` 内的 `SuccessCriteria[].Status`、`ValidationPlan[].Status`、`Evidence` 不会被更新。
- `session.md` 里的 criteria / validation verified 计数来自 goal snapshot，因此可能出现 goal 已 complete，但 summary 仍显示 `0/N verified`。
- Web Goal 面板读取的是 session detail 中的 goal snapshot，不会自然展示 completion evidence。

影响：

- 验证契约没有变成“当前事实”，只是历史事件里的附属数据。
- Mission Control / session summary / checkpoint 无法准确表达哪些 criteria、validation、feature、milestone 已被证据满足。
- 长任务恢复时，后续 agent 需要读 history 才能拼出完成证据，增加上下文和错误风险。

已采用方案：

- 在 `SessionGoal` 增加轻量 completion audit snapshot，例如：
  - `completion_evidence []string`
  - `completed_by string`
  - `completion_summary string`
  - 或 `audit` 子对象，包含 criteria / validation / feature / milestone 的证据映射。
- 扩展 `update_goal` payload，但仍只允许 `status="complete"`：
  - `evidence []string`
  - `criteria_statuses [{id,status,evidence}]`
  - `validation_statuses [{id,status,evidence,last_run_at}]`
  - 可选 `feature_statuses` / `milestone_statuses`
- `update_goal` 保存 goal snapshot 时同步写入 evidence 和 verified status，再追加 history。
- `session.md`、checkpoint 和 Web Goal panel 读取同一份 snapshot，而不是只依赖 history；其中 `session.md` / checkpoint 取决于后续 summary / checkpoint refresh。

本轮验收：

- 调用 `update_goal(status=complete, evidence=[...])` 后，`goal.json` 可见 completion evidence。
- 若 payload 更新 criteria / validation，summary refresh 后 `session.md` 显示 verified 计数同步变化。
- Web Goal panel 能展示模型 `update_goal` 写入的 completion evidence，刷新后不丢失。

### P0-3. Validation contract 尚未形成 coverage / assignment 机制

现状：

- `GoalValidation` 和 `MissionPlan.ValidationContract` 已存在。
- `MissionFeature` 有 `ClaimedAssertions []string`，可以表达 feature 覆盖哪些验证断言。
- 但没有 coverage checker，没有 plan approval 前的“每个 assertion 至少被 feature / milestone 覆盖”检查。
- `update_goal` 只能在完成审计时更新顶层 `ValidationPlan` item；不能独立更新 `MissionPlan.ValidationContract` assertion status，也不能基于 `claimed_assertions` 做覆盖 / 证据映射。

影响：

- 补充材料强调“每个功能被分配一条或多条断言，所有功能覆盖全部断言”，当前只能靠模型自觉维护。
- 即使 mission plan 很完整，系统也不会发现 validation contract 漏映射、重复映射、无 feature 覆盖或无 milestone 验证点。

优化方案：

- 顶层 `GoalValidation` 已有默认 `validation_0001` ID；Mission validation contract 仍需要 checker 强制稳定 ID、识别空 ID / 重复 ID，并允许用户/agent 自定义。
- 增加一个只读 checker，不直接执行 workflow：
  - `goal plan check <session-id>`
  - 或 model tool `check_goal_plan`
  - 或 Web Goal panel 内的 computed coverage summary
- checker 输出：
  - validation assertions 总数
  - 已被 features claimed 的数量
  - 未覆盖 assertions
  - feature 无 assertion 的列表
  - milestone 未关联 validation 的列表
- 当 mission plan 需要 approval 时，把 coverage checker 接入 approval 前检查；未覆盖时阻止 `mission.plan.approved`，但仍允许用户明确 override。

验收：

- mission 有 3 条 validation contract，2 条被 feature claimed，checker 报出 1 条 uncovered。
- plan approval 时，未覆盖 contract 默认返回 409 / blocked，除非请求带 explicit override。

### P0-4. `budget_limited` 与 `stop_on_budget` 还没有形成真正控制语义

现状：

- `UpdateGoalAccounting` 会累加 token / time，并在预算达到后把 status 改成 `budget_limited`，同时写 `goal.budget_limited` history / event。
- `goalPromptContext`、`session.md` / checkpoint hints 会提示“Budget exhaustion is not completion”。
- 但 `GoalControl.StopOnBudget` 字段没有实际使用。
- `CompletionController.goalCompletionGate` 对 `budget_limited` 直接允许 `finish`，没有确认是否正在做 wrap-up 或是否已记录剩余工作。

影响：

- 长 mission 达到预算后仍可能继续消耗 provider calls，直到模型自然停下。
- budget-limited wrap-up 的质量依赖模型自觉，缺少最低事实要求。

优化方案：

- 当 `StopOnBudget=true` 且状态进入 `budget_limited`：
  - 在最近安全边界追加 harness reminder，要求写明 progress、validated evidence、remaining work、blockers。
  - 可将 session 转入 `awaiting_input`，或只允许一次 wrap-up turn，具体需要先写 spec。
- `finish` 对 `budget_limited` 的允许条件应更窄：
  - 必须有最新 `goal.budget_limited` history。
  - 必须有 wrap-up evidence 或 session summary 已刷新。
  - final output 不能声称 complete，除非 `update_goal(status=complete)` 已经完成真实审计。

验收：

- token budget 触顶后写 `goal.budget_limited`，并可在 session detail / events 中看到。
- `stop_on_budget=true` 的 session 不会无限继续执行 provider turns。
- budget-limited final 不会被渲染成 completed。

### P1-1. 缺少模型可用的结构化 progress / handoff 更新入口

现状：

- 模型工具只有 `get_goal`、`create_goal`、`update_goal(status=complete)`。
- Web REST 可以 snapshot patch mission plan / validation，但模型运行中不能用工具以 append-friendly 方式更新 feature / milestone / role plan / validation evidence。
- 模型可以写 `reports/progress.md` / `reports/validation.md`，但这些不会自动同步 goal snapshot 或追加结构化 goal progress history。

影响：

- Worker 完成局部功能后，无法用结构化字段沉淀“已做、未做、命令和退出码、风险、遵循计划情况”。
- Factory Missions 中的 structured handoff 目前只能以普通 Markdown 文件存在，不能成为 Mission Control 的一等事实。

优化方案：

- 先更新 spec，再新增一个窄工具，避免把 runtime 写成 workflow engine。
- 推荐工具名：`record_goal_progress` 或 `update_goal_plan`。
- 工具只允许 patch 当前 goal 的内部计划字段，不允许改 objective、不允许 pause/resume/clear、不允许跳过 completion audit。
- 输入应是结构化、append-friendly：
  - `feature_updates`
  - `milestone_updates`
  - `validation_updates`
  - `handoff`
  - `linked_artifacts`
  - `commands [{command, exit_code, artifact, summary}]`
  - `blockers`
- 每次写入都追加 `goal.updated` / `mission.plan.updated` / `mission.validation.updated` history，并刷新 `session.md` 和 checkpoint。

验收：

- model tool 可以把 feature 状态从 `pending` 更新为 `completed` 并附 evidence。
- handoff 出现在 `goal-history.jsonl`、`goal.json`、`session.md`、Web Goal panel。
- 工具拒绝 objective 修改和非当前 session path escape。

### P1-2. Creator / Verifier 分离还缺少 Goal 层的轻量引导和证据关联

现状：

- `planner` / `generator` / `evaluator` role provider override 已实现。
- `agent_spawn` 描述鼓励在宽任务、审计、独立验证时使用 evaluator。
- Mission `role_plan` 可以记录 roles，并能填充 provider / model defaults；features / milestones 已有通用 `child_session_ids`、`queue_job_ids`、`validation_ids` 字段，Web Goal panel 也会展示这些 linked facts。
- 但 Goal / Mission 层没有 evaluator-specific 语义来表达“某条 validation assertion 由 evaluator child / queue job 独立验证通过”。

影响：

- 系统已经具备 reviewer lane 和通用 child/queue link 的技术能力，但 Mission 视角看不出“哪些 validation assertions 由独立 evaluator 验过”。
- 长任务可能仍退回实现者自评。

优化方案：

- 不强制自动 spawn evaluator。
- 在 goal prompt / tool description 中增加轻量提示：当 mission 有 validation contract 或 milestone 完成时，模型应考虑用 `agent_role=evaluator` 做独立验证，并把 child / queue / artifact id 记录回 goal progress。
- `record_goal_progress` 支持 `child_session_ids`、`queue_job_ids`、`validation_ids` 的关联。
- Web Goal panel 对 evaluator-linked validation 显示 “validated by evaluator child/session/job”。

验收：

- evaluator child 完成后，parent 可以把 child session id 关联到对应 validation item。
- `session.md` / checkpoint 展示 unresolved evaluator work 与已完成 evaluator evidence。

### P1-3. CLI 缺少高级 mission plan / validation 操作面

现状：

- CLI 只有 `goal show|pause|resume|clear|complete`。
- Mission plan patch / approve / validation patch 主要在 Web REST。
- `create_goal` tool 支持 initial mission fields；`goal show --json` 可原样查看完整 `goal.json`，但纯 CLI 下缺少专用 plan / validation 查看、检查和批准操作。

影响：

- 当前项目主路径是 CLI-first，但高级 Goal/Mission 控制面反而偏 Web/API。
- 如果不补专用 CLI，Mission approval / validation contract 的可靠使用会依赖 experimental WebConsole 或直接读 JSON。

优化方案：

- 增加克制的 CLI 子命令，不扩大 root help：
  - `goal plan show <session-id> [--json]`
  - `goal plan approve <session-id>`
  - `goal validation show <session-id> [--json]`
  - 可选 `goal plan check <session-id>`
- patch 类操作可以先不做交互编辑，只支持从 JSON 文件读取，避免复杂 TUI。

验收：

- 不启动 WebConsole 也能完成 mission plan check / approve。
- CLI 输出只读 session store，不维护第二套状态。

### P1-4. Mission Control 展示仍偏 snapshot，缺少 timeline / evidence drilldown

现状：

- Web Goal panel 展示 goal snapshot、criteria、validation、features、milestones、roles 和 completion audit evidence。
- Timeline 已识别多数组 goal / planmode events。
- 但 panel 没有 goal history drilldown，没有 coverage summary，没有 unresolved evaluator / queue count，也没有 validation/evaluator attribution；session detail 不直接返回 `goal-history.jsonl`，部分 mission approval 事件也未进入紧凑 flow 展示。

影响：

- 用户能看到状态，但不容易判断“为什么系统认为它完成 / 未完成”。
- 长 mission 的异步监督价值还没有完全发挥。

优化方案：

- 保持 session-first 页面，不新增全局 Mission dashboard。
- 在当前 Goal tab 内增加轻量 facts：
  - latest history event
  - completion evidence
  - validation coverage count
  - linked child / queue unresolved count
  - latest blocker
- 只用现有 REST/session detail 聚合，不引入图形化状态管理。

验收：

- Goal tab 能直接回答：目标状态是什么、哪些验证通过、哪些未覆盖、是否有 evaluator/queue 未结算、最后一次 plan/validation 更新是什么。

## 4. 推荐收敛顺序

### Slice A：修正 approval 语义

目标：让 `require_plan_approval` 真正复用 Plan Mode gate，消除“看似审批、实际不阻断”的语义风险。

范围：

- spec 更新：`spec/00-product.md`、`spec/01-runtime-architecture.md`、`spec/09-phase-plan.md`、`spec/17-web-console.md`、必要时 `spec/18-durable-contract-and-completion.md`。
- runtime：StartRequest Goal + PlanMode link、mission plan approve 与 Plan Mode approve 互通。
- tests：runtime PlanMode gate、Web start、CLI goal flags、mission plan approve。

不做：

- 不自动拆 worker。
- 不新增 MissionState。

### Slice B：completion evidence 回写 goal snapshot

目标：让完成审计证据成为当前事实，而不是只藏在 history。

范围：

- session data model 增加 completion audit / evidence。
- `update_goal` 支持 evidence / criteria / validation status patch。
- session summary、checkpoint、Web Goal panel 同步展示。
- tests：tool、session store、runtime completion gate、Web detail。

不做：

- 不允许模型改 objective。
- 不允许模型 pause / resume / clear。

### Slice C：validation contract coverage checker

目标：在不写固定 workflow engine 的前提下，把 validation contract 的完整性变成可验证事实。

范围：

- checker 函数和 CLI/API 读面。
- plan approval 前可选 gate。
- Web Goal panel coverage summary。

不做：

- 不自动运行测试。
- 不强制所有 Goal 都必须有 validation contract；仅 mission / advanced plan 使用。

### Slice D：structured progress / handoff 工具

目标：让 worker / parent / evaluator 能把结构化 handoff 写回 goal，不再只靠 Markdown 总结。

范围：

- spec-first 添加 `record_goal_progress` 或 `update_goal_plan`。
- append-only history + snapshot patch。
- linked artifacts / commands / blockers / child / queue references。

不做：

- 不替模型决定什么时候委派。
- 不把 feature list 变成 runtime DAG。

### Slice E：CLI-first mission controls 与 Web polish

目标：让纯 CLI 用户也能查看、检查、批准 mission plan；Web 只做 session-first facts 展示增强。

范围：

- `goal plan show/check/approve`
- `goal validation show`
- Web Goal tab evidence / coverage / unresolved facts。

不做：

- 不恢复 Overview。
- 不引入复杂 TUI、面板编排或 Worker Pool 细节。

## 5. 验证计划

每个代码 slice 至少运行：

```bash
go test ./internal/session ./internal/runtime ./internal/tools ./internal/webconsole ./internal/app
node --check internal/webconsole/assets/*.js
git diff --check
```

涉及 Web start / Goal tab 的 slice 还应补充：

- `go test ./internal/webconsole -run Goal`
- 浏览器 smoke 或现有 WebConsole smoke，验证 Goal toggle、Goal tab、plan approval、completion evidence 可见。

建议新增端到端用例：

1. Goal-only：Web start 只打开 Goal 开关，prompt 自动成为 objective；模型 `update_goal(status=complete,evidence=...)` 后 goal snapshot、history、session.md 一致。
2. Mission approval：CLI/Web 创建 `require_plan_approval=true` mission；approval 前 shell/write/edit/finish 被 Plan Mode gate 阻断；approve 后恢复执行。
3. Validation coverage：mission 有 validation contract，features 未覆盖全部 assertion；`goal plan check` 报出 uncovered；补齐后通过。
4. Budget stop：token/time budget 触顶后写 `goal.budget_limited`；`stop_on_budget=true` 不继续无限执行，并输出 wrap-up。
5. Evaluator evidence：parent 关联 evaluator child session 到 validation item；Web/summary/checkpoint 都能看到 linked evidence。

## 6. 明确不建议做的事情

- 不新增一个独立 `missions/` 顶层状态目录替代 `goal.json`。
- 不把 `orchestrator / worker / validator` 写成 runtime 固定状态机。
- 不让 `require_plan_approval` 自动生成复杂 feature DAG。
- 不把 WebConsole 改成大型 Mission Control dashboard。
- 不在默认 root help 中强调 queue / children / web 超过 core CLI 主路径。
- 不因为参考 Factory Missions 就引入强制并行 worker；当前项目应保持串行为主、局部并行、model-led delegation。

## 7. 当前最应先修的判断

P0-1 与 P0-2 已完成当前轮收敛。下一轮最值得继续推进的是 P0-3：

1. validation contract coverage checker 可以在不引入固定 workflow engine 的前提下，把 Factory Missions 中“编码前定义完成、每条 assertion 有 feature 覆盖”的核心收益落成可验证事实。
2. coverage checker 完成后再接入 plan approval 前检查，能避免 approved mission plan 缺少验证映射。
3. 随后再做 structured progress / handoff 工具，会有更清晰的 validation id 与 evidence id 可关联。
