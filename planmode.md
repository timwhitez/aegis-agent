# Plan Mode 落地方案

> Review baseline: 本方案以当前 spec / runtime / tool / WebConsole 架构为事实基础，并参考本地 Codex 与 ForgeCode 快照。本文同时作为 v1 设计说明与验收账本；实现是否完成必须以当前代码、测试、smoke 输出和 git 提交为准，不能只看类型、路由或 UI 文案是否存在。

## 1. 结论

当前项目不是“完全没有 planning”。当前代码里至少有四类相关能力：

- `todo.json` / `tasks/`：当前 session 执行节奏与持久任务图，见 `spec/12-task-system.md`。
- `goal.json` / `artifacts/goal-history.jsonl`：durable objective、completion audit、可选 Mission 内部计划，见 `spec/18-durable-contract-and-completion.md`。
- WebConsole Goal 面板和 Mission plan approve API：`internal/webconsole/service.go` 已有 `/mission/plan/approve`，前端已有首 prompt 的 `Goal` 按钮。
- Plan Mode v1 已有独立实现，包括 `internal/session/planmode.go`、`internal/runtime/planmode.go`、`request_user_input` / `submit_plan` / `get_plan_mode`、CLI/Web 入口和相关测试文件；后续调整仍需按本文验收项逐条复核，不能只因存在草稿或路由就视为行为正确。

本方案要收敛的是 **真正的 Plan Mode 产品与实现契约**：

- 它应该是 session-scoped collaboration mode / execution gate，不是 todo checklist，也不是 Mission plan 的一个字段。
- 在审批前，模型可以阅读、搜索、静态分析、提出澄清问题、提交计划；不能写文件、跑执行性 shell、创建 task、spawn child、提交 queue、调用 finish。
- 计划必须作为 session fact source 持久化，并在 WebConsole 当前 session 里以按钮和计划卡片呈现。
- 用户点击 `Approve & Run` 后，runtime 才恢复普通执行工具面，并把 approved plan 注入下一轮上下文。

推荐实现：以 Codex Plan Mode 的 collaboration-mode 门禁为主干，吸收 ForgeCode Muse 的计划 artifact 格式，但不复制 ForgeCode 的 agent-switch 架构，也不把 runtime 改成固定 workflow / DAG engine。

## 2. 当前仓库事实

### 2.1 产品边界

`spec/00-product.md` 当前定义项目是 Web-first 本地 agent harness，不是重型 TUI、hosted SaaS、固定 workflow、plan engine 或 orchestration 框架。Web-first v1 目标包含 durable session goals，但明确不做固定 DAG / plan graph / verification graph。

`spec/01-runtime-architecture.md` 约束三层分离：

- `core runtime`：loop、provider、tools、skills、hooks、compaction、session、events。
- `sdk facade`。
- `cli adapter`。

同一份 spec 还要求 `SessionStore` 是事实源，当前已有 `goal.json`、`artifacts/goal-history.jsonl`、`contract.json`、`artifact-tracker.json`、`provider-attempts.jsonl`、`session.md`、`checkpoints/` 等 session fact files。

`spec/03-provider-contracts.md` 要求 provider replay、tool 格式、generation 选项、错误分类都停留在 adapter 层，CLI / Web / tool 层不得硬编码 provider-native replay 逻辑。

`spec/17-web-console.md` 当前明确 WebConsole 是默认 Web-first 入口；session 创建、steer、continue、Goal、Tasks、Background、Timeline 收敛到现有 session 工作区，并继续复用 session 文件事实源。

### 2.2 现有 Goal / Mission 与 Plan Mode 实现

已提交基线已有 Goal/Mission 数据模型和 Web 控制面：

- `internal/session/goal.go` 定义 `SessionGoal`、`GoalControl.RequirePlanApproval`、`MissionPlan.PlanStatus`、`MissionPlan.ApprovedAt`、`MissionPlan.CreateTasksFromPlan`。
- `internal/webconsole/service.go` 已有 `/goal`、`/mission/plan`、`/mission/plan/approve`，但这是 Goal/Mission 的内部计划审批，不会阻止普通工具执行。
- `internal/webconsole/assets/index.html` 当前输入区已有 `Goal` 按钮。

因此，`require_plan_approval` 和 `mission.plan_status` 只能表达“Goal 的内部计划需要审批”，不能表达“当前 session 处于 Plan Mode，审批前禁止执行修改”。

当前 Plan Mode v1 实现已经补入以下方向，设计审查应逐条用本文验收项校验，而不是只做存在性判断：

- `internal/session/planmode.go` 定义独立 `PlanModeState`、`planmode.json`、history、Markdown 派生计划、pending input 和 approval 结构。
- `internal/runtime/runner.go` 的 `StartRequest` / `ContinueRequest` 已出现 `PlanMode` 与 approve/cancel/input answer 字段。
- `internal/runtime/planmode.go` 与 `CompletionController` 已出现 pending allowlist、provider tool schema 裁剪和硬门禁。
- `internal/tools/registry.go` 已出现 `get_plan_mode`、`submit_plan`、`request_user_input`。
- `internal/webconsole/dto.go`、`service.go` 和前端 assets 已实现 Plan toggle、Plan inspector、approval/revision/cancel/input API。

需要重点复核的实现风险：

- `request_user_input` 在已写入 `pending_request.tool_call_id` 后，任何 context cancel、server restart、Web handle 丢失或 recoverable responder error 都不能清空 pending request，除非同一轮已经写入对应 tool result。no-TTY / no responder 这类不可恢复错误应在写 pending request 前失败。
- `planmode.plan_rejected` 不是 v1 必需事件。若没有独立 Reject API，`Ask for Changes` 应记录为 `planmode.plan_revised` 并回到 `planning`，测试和文档不能要求一个前端不可到达的 rejected flow。
- Web `/planmode/input` payload 对外保持 snake_case `request_id`；前端内部使用 `requestID` 时必须在 API helper 做转换，避免 DTO 与 DOM 数据集命名混用。

## 3. 参考实现分析

### 3.1 Codex Plan Mode

参考快照：

- 本轮本地参考路径：`/tmp/go-cli-agent-planmode-codex`
- commit：`2630a6ca35707e9386fe41f898983321ebb8ae09`

关键结论：

- `codex-rs/collaboration-mode-templates/templates/plan.md` 把 Plan Mode 明确写成 collaboration mode，不是 `update_plan` checklist。它要求先探索、再提问、最后输出 decision-complete plan；允许非变更探索，禁止文件编辑、formatter、migration、codegen、side-effectful execution。
- `codex-rs/models-manager/src/collaboration_mode_presets.rs` 的 `plan_preset()` 把 `ModeKind::Plan` 与 mode-specific developer instructions 绑定，并把 reasoning effort 设为 medium。
- `codex-rs/protocol/src/config_types.rs` 中 `ModeKind` 只把 `Default` 和 `Plan` 暴露为可见 collaboration modes，并且 `allows_request_user_input()` 只允许 Plan Mode 使用。
- `codex-rs/core/src/context/collaboration_mode_instructions.rs` 把 collaboration mode instructions 注入成 developer fragment。
- `codex-rs/core/src/tools/handlers/request_user_input_spec.rs` 定义 `request_user_input` 工具，要求 1-3 个短问题、2-3 个互斥选项、推荐项排第一。
- `codex-rs/core/src/tools/handlers/request_user_input.rs` 限制 `request_user_input` 只能 root thread 使用，并检查当前 collaboration mode 是否允许。
- `codex-rs/core/src/tools/handlers/plan.rs` 在 Plan Mode 中拒绝 `update_plan`，因为它是 TODO/checklist 工具，不是 Plan Mode。
- `codex-rs/core/src/session/turn.rs` 解析 `<proposed_plan>`，把计划内容作为 plan item / plan delta 单独流给客户端。
- `codex-rs/core/src/goals.rs` 在 Plan Mode 中 suppress goal continuation（忽略 active goal 自动续跑），避免规划阶段被 continuation prompt 打断。
- `codex-rs/tui/src/chatwidget/plan_implementation.rs` 把“实现这个计划”建模为显式用户动作：用户选择后发送类似 `Implement the plan.` 的新 user input，并可选择把 plan markdown 放进执行请求，而不是让 `<proposed_plan>` 自动开工。

可借鉴的部分：

- Plan Mode 是显式 mode，不由用户一句“你先计划一下”隐式改变，也不与 checklist 混用。
- mode-specific developer instructions 是第一等 prompt 输入，不能散落在 UI 文案里。
- `request_user_input` 是 Plan Mode 的关键能力，用于让模型在必要时向用户收敛高影响决策。
- Plan 输出应被 UI 特殊渲染，而不是混在普通 assistant 文本里。
- Plan Mode 与 Goal 可以共存，但 Plan Mode pending 时不应该触发 Goal continuation / finish audit 的自动推进。
- 用户批准计划必须留下可回放的用户动作事实，例如一条 `meta.source=planmode_approval` 的 user message 或等价 durable control event；不能只靠前端按钮在内存里切换执行。

不直接照搬的部分：

- 本项目当前是 Web-first 本地 harness + CLI fallback，不是 Codex TUI/app-server；不需要复制 Codex 的完整 collaboration mode preset / app-server protocol。
- v1 不强制实现 `<proposed_plan>` 流式 parser。更稳妥的第一版是增加 `submit_plan` 工具作为计划事实源，同时允许后续增量支持 `<proposed_plan>` UI 流式显示。

### 3.2 ForgeCode Muse

参考快照：

- 本轮本地参考路径：`/tmp/go-cli-agent-planmode-forgecode`
- commit：`65a1bb0d755fd4867eff9568325323bba992c894`

关键结论：

- `README.md` 把 `muse` / `:plan` 定义为 planning agent：分析结构并把 implementation plans 写到 `plans/`，不修改文件。
- `crates/forge_repo/src/agents/muse.md` 定义 Muse 的 planning-only 身份，只暴露 read/search/fetch/plan 等工具，并要求 implementation tasks 使用 Markdown checkboxes。
- `crates/forge_domain/src/tools/catalog.rs` 有 `ToolCatalog::Plan(PlanCreate)`，`PlanCreate` 接受 `plan_name`、`version`、`content`。
- `crates/forge_services/src/tool_services/plan_create.rs` 会在 `plans/` 下按日期、名称、版本写计划文件，并拒绝覆盖。
- `crates/forge_main/src/model.rs` 把 `/plan` alias 到 `muse`；`crates/forge_main/src/ui.rs` 将 Muse 命令切换 active agent。

可借鉴的部分：

- 入口要非常轻：一个 `Plan` / `:plan` 就够，不要让用户填复杂表单。
- planning-only 的工具面必须收窄，而不是只靠 prompt 自觉。
- 计划产物应是 Markdown，可被人和后续 agent 直接执行。
- 计划内容要包含 objective、implementation checklist、verification criteria、risks、alternatives。

不直接照搬的部分：

- 本项目已有 `Goal`、`Mission`、role provider override、child/queue 扩展，不应为了 Plan Mode 增加一个 Forge 式独立 agent 切换体系。
- 本项目的计划事实源应放在 session 目录，而不是 repo 根 `plans/`，否则会破坏 session / state / events 事实源原则。需要 repo 可见计划时，可以把 `artifacts/planmode-plan.md` 作为 session artifact 展示或显式导出。

## 4. 产品定义

### 4.1 一句话定义

Plan Mode 是一个 session-scoped planning gate：模型先用只读能力把计划做到可执行、可验证、可审批；用户批准后，session 才进入普通执行。

### 4.2 用户体验目标

- 新建 session 时，用户在现有输入框旁点击 `Plan` 按钮，再输入任务并发送。
- 现有 `awaiting_input` / `paused` / `failed` session 中，用户也可以点击 `Plan` 按钮，让下一条 continue 先进入计划审批。
- 模型提出计划后，WebConsole 在当前 session timeline 中显示计划卡片，右侧 inspector 出现 Plan 面板。
- 用户只需要做三种动作：
  - `Approve & Run`：批准计划并继续执行。
  - `Ask for Changes`：输入修改意见，回到 planning。
  - `Cancel Plan Mode`：取消本次 Plan Mode，session 回到普通 awaiting input。

### 4.3 不改变的默认路径

- 不开启 `Plan` 时，`run` / `exec` / `steer` / `continue` 行为不变。
- `Goal` 按钮仍然只是 durable objective，不自动进入 Plan Mode。
- 用户在普通模式里写“先给计划 / 先不要改”时，模型应按普通 prompt 自觉只输出方案，但 runtime 不应从自然语言自动切换到 Plan Mode；只有 `Plan` toggle、CLI flag 或 API 字段才启用硬门禁。
- `Plan` 和 `Goal` 可以同时开启：
  - `Goal` 负责长期目标与完成审计。
  - `Plan Mode` 负责执行前计划审批与工具门禁。

### 4.4 非目标

- 不做固定 DAG、plan graph、verification graph。
- 不自动创建 durable tasks。
- 不自动 spawn child agent。
- 不自动提交 queue job。
- 不新增 WebConsole 总览页或大型配置表单。
- 默认 CLI / README 叙事已经改成 Web-first；Plan Mode 需要同时服务 Web 默认入口和 CLI fallback。
- 不在 CLI/Web/tool 层实现 provider-specific replay。

## 5. 与 Goal / Mission / Task 的关系

### 5.1 分工

| 能力 | 事实源 | 作用 | 是否阻断执行 |
|---|---|---|---|
| Goal | `goal.json` | durable objective、预算、完成审计 | 只在 finish 前做 goal completion audit |
| Mission | `goal.json.mission` | Goal 内部结构化计划、features、milestones、role hints | 仅表达计划状态，不默认阻断工具 |
| Todo | `todo.json` | 当前 session 高频执行节奏 | 不阻断 Plan Mode 之外的执行 |
| Task graph | `tasks/` | durable task 依赖、恢复、ready-state | 不承担 artifact/goal 完成判定 |
| Plan Mode | `planmode.json` | 执行前计划审批、只读工具门禁、用户决策收敛 | 审批前阻断 mutating / execution tools |

### 5.2 共存规则

- `Plan Mode` 必须有独立 fact source：`.go-cli-agent/sessions/<id>/planmode.json`。
- 若 session 已有 active goal，Plan Mode 不替换 objective；默认使用当前输入作为 plan objective，并可记录 `linked_goal_id`。
- 若 `Plan` 与 `Goal` 同时开启：
  - start 时创建 `goal.json` 和 `planmode.json`。
  - 计划获批后，可以把 approved plan summary 同步到 `goal.mission.plan_status = "approved"`，但这只是兼容展示，不是 Plan Mode 的事实源。
- `Plan Mode` 不自动设置 `CreateTasksFromPlan`。需要 task graph 时，必须由用户显式选择或模型在审批后普通执行阶段调用 task tools。
- 当前项目没有 Codex 式 active goal continuation 自动续跑；因此 v1 的实际要求是：Plan Mode pending 时不得通过 Goal/Mission approve、completion audit、auto-continue 或 future goal continuation 绕过审批进入执行。如果未来增加 goal continuation，必须像 Codex 一样在 Plan Mode 中 suppress。

## 6. 状态机与存储

### 6.1 文件布局

```text
.go-cli-agent/sessions/<session-id>/
  planmode.json
  artifacts/
    planmode-history.jsonl
    planmode-plan.md
```

说明：

- `planmode.json` 是事实源。
- `artifacts/planmode-history.jsonl` 是状态变更流水。
- `artifacts/planmode-plan.md` 是 operator-readable 派生文件，用于 WebConsole、session summary、人工查看；不替代 JSON fact source。

### 6.2 状态

```text
off
planning
awaiting_user_input
awaiting_approval
approved
rejected
cancelled
executing
```

状态含义：

- `off`：未开启。
- `planning`：Plan Mode 正在运行，工具门禁生效。
- `awaiting_user_input`：模型通过 `request_user_input` 等待用户回答。
- `awaiting_approval`：模型已提交计划，等待用户批准或要求修改。
- `approved`：用户已批准某个 plan version。
- `rejected`：用户拒绝当前计划但保留继续修订入口。
- `cancelled`：用户取消 Plan Mode；不会自动执行。
- `executing`：批准后的普通执行阶段。工具门禁解除，但 approved plan 会注入上下文。

兼容性约束：

- `planmode.status` 是新增状态机；`state.json.status` 继续沿用当前已有枚举（`running|awaiting_input|paused|completed|failed`）。
- `awaiting_approval` / `awaiting_user_input` 通过 `state.status=awaiting_input` + `state.phase=plan_*` 表达，避免破坏现有 `continue` 可恢复逻辑和 session 列表语义。
- `planning|awaiting_user_input|awaiting_approval` 视为 pending Plan Mode：provider tool schema 必须裁剪，CompletionController 硬门禁必须生效，Goal/Mission 的 approve 或 completion audit 不能绕过它。
- `approved` 只表示某个 plan version 已获批但普通执行 turn 尚未启动；第一次把批准事实写入 messages/events 并注入 approved plan 后，状态再转为 `executing`。
- `rejected` 是可选的显式拒绝状态。v1 如果没有独立 `reject` 按钮，可以把 `Ask for Changes` 直接建模为 `planmode.plan_revised` 并回到 `planning`，不要引入一个前端不可到达的状态。

### 6.3 `planmode.json` v1 schema

```json
{
  "schema_version": 1,
  "session_id": "session_...",
  "plan_mode_id": "pm_...",
  "enabled": true,
  "status": "awaiting_approval",
  "objective": "实现真正的 plan mode",
  "source": "web",
  "linked_goal_id": "goal_...",
  "plan_id": "plan_...",
  "plan_version": 1,
  "approved_version": 0,
  "plan_markdown": "# Plan Mode ...",
  "summary": "先新增 planmode fact source 与工具门禁，再接入 Web 按钮和审批流。",
  "assumptions": [
    "第一版不实现 <proposed_plan> 流式 parser，使用 submit_plan 工具作为事实源"
  ],
  "risks": [
    "request_user_input 需要避免 dangling tool call"
  ],
  "verification": [
    "go test ./internal/session ./internal/runtime ./internal/webconsole",
    "node --check internal/webconsole/assets/*.js"
  ],
  "pending_request": null,
  "approvals": [
    {
      "version": 1,
      "source": "web",
      "approved_by": "operator",
      "approved_at": "2026-05-13T10:00:00Z"
    }
  ],
  "created_at": "2026-05-13T09:30:00Z",
  "updated_at": "2026-05-13T09:45:00Z"
}
```

### 6.4 `pending_request` schema

```json
{
  "request_id": "pmq_...",
  "tool_call_id": "call_...",
  "questions": [
    {
      "id": "approval_scope",
      "header": "Scope",
      "question": "Plan Mode should block shell completely in v1, or allow read-only shell dry runs?",
      "options": [
        {
          "label": "Block shell (Recommended)",
          "description": "Simpler and safer first version; avoids command classification mistakes."
        },
        {
          "label": "Allow dry runs",
          "description": "More flexible but requires command side-effect classification."
        }
      ]
    }
  ],
  "status": "pending",
  "created_at": "2026-05-13T09:35:00Z"
}
```

说明：

- `request_user_input` 的工具输入不要求模型传 `is_other` / `isOther`。参考 Codex 的做法，后端可以在规范化后的 pending request 中自动打开 free-form Other 能力，前端负责展示。
- pending request 中保存的 `tool_call_id` 必须足以在回答、取消或崩溃恢复时写回对应 tool result；`planmode.input_cancelled` 这类 event 只能作为审计线索，不能替代 provider replay 需要的 tool result。

### 6.5 history 事件类型

`artifacts/planmode-history.jsonl` 至少记录：

- `planmode.created`
- `planmode.input_requested`
- `planmode.input_answered`
- `planmode.input_cancelled`
- `planmode.plan_submitted`
- `planmode.plan_revised`
- `planmode.plan_approved`
- `planmode.cancelled`
- `planmode.execution_started`

若后续增加独立 Reject 操作，可追加 `planmode.plan_rejected`。v1 没有独立 Reject UI/API 时，不应把它列为必需事件；修订计划统一用 `planmode.plan_revised`。

同名事件也应写入 `events.jsonl`，方便 timeline 和 session summary 聚合。

## 7. Runtime 设计

### 7.1 新增类型

建议新增：

```go
// internal/session/planmode.go
type PlanModeDraft struct {
    Enabled   bool
    Objective string
    Source    string // cli|web|tool|system
}

type PlanModeState struct {
    SchemaVersion   int
    SessionID       string
    PlanModeID      string
    Enabled         bool
    Status          string
    Objective       string
    Source          string
    LinkedGoalID    string
    PlanID          string
    PlanVersion     int
    ApprovedVersion int
    PlanMarkdown    string
    Summary         string
    Assumptions     []string
    Risks           []string
    Verification    []string
    PendingRequest  *PlanModeInputRequest
    Approvals       []PlanModeApproval
    CreatedAt       string
    UpdatedAt       string
}
```

`session.Store` 增加：

- `CreatePlanMode(sessionID string, draft PlanModeDraft) (PlanModeState, error)`
- `LoadPlanMode(sessionID string) (PlanModeState, error)`
- `SavePlanMode(sessionID string, state PlanModeState) error`
- `AppendPlanModeHistory(sessionID string, entry PlanModeHistoryEntry) error`
- `ApprovePlanMode(sessionID string, source string) (PlanModeState, error)`
- `CancelPlanMode(sessionID string, source string) (PlanModeState, error)`

### 7.2 Start / Continue 请求

`internal/runtime/runner.go`：

```go
type StartRequest struct {
    ...
    Goal     *session.GoalDraft
    PlanMode *session.PlanModeDraft
}

type ContinueRequest struct {
    ...
    PlanMode *session.PlanModeDraft
}
```

语义：

- `StartRequest.PlanMode.Enabled=true`：创建 session 后先进入 `planning`，第一轮使用 Plan Mode instructions 和工具门禁。
- `ContinueRequest.PlanMode.Enabled=true`：对已存在的 `awaiting_input|paused|failed` session 开启一个新的 planning pass。
- 若 `planmode.status=awaiting_approval`，普通 continue message 默认视为 `Ask for Changes`：后端把 `planmode.status` 改回 `planning`，并把用户修订意见追加为新的 user message。
- `PlanMode` 只能来自显式 API / CLI / Web toggle；不要从 prompt 文本关键词自动推断，以免普通“帮我先规划”请求意外启用硬门禁。

Plan Mode 控制动作也需要进入 runtime 层，而不是只在 Web service 里直接改 JSON。建议任选一种清晰 API：

```go
type PlanModeControlRequest struct {
    SessionID string
    Action    string // approve|revise|cancel|answer_input
    Source    string // cli|web|system
    Message   string // revise / approval execution message
    Answers   []PlanModeAnswer
}
```

或在 `ContinueRequest` 上增加等价字段。关键约束是：

- `approve` 必须由 runtime/session helper 完成批准、追加 `meta.source=planmode_approval` 的 user message、刷新 state，并启动或准备普通 execution turn。
- `revise` 必须追加真实 user message，事件源标记为 `planmode_revision`，然后回到 `planning`。
- `answer_input` 必须能把回答写回对应 `tool_call_id` 的 tool result，再恢复 planning；不能只把回答存在 `planmode.json` 里。
- `cancel` 必须写 durable event，并在存在 pending tool call 时补一条取消/错误 tool result 或在下次 continue 前完成补偿。

同一 session 的其他执行控制面也必须读取 Plan Mode 状态：

- pending Plan Mode 下，`experimental delegate`、`experimental queue submit`、Web queue child job、以及任何带 `parent_session_id=<pending session>` 的后台任务提交必须拒绝或要求先 `approve` / `cancel`。
- 新建一个无 parent 的独立 session / queue job 不受当前 session 的 Plan Mode 限制。
- 这条规则属于 runtime/session control gate，不应只写在前端按钮状态里。

### 7.3 Prompt 注入

新增 `internal/runtime/planmode.go`：

- `BuildPlanModeInstructions(state PlanModeState) string`
- `BuildApprovedPlanContext(state PlanModeState) string`
- `ShouldUsePlanModeInstructions(state PlanModeState) bool`

Plan Mode developer instructions 必须表达：

- 你处于 Plan Mode，直到 runtime planmode state 被批准、取消或显式关闭。
- 用户要求执行时，只能规划执行，不能执行。
- 先探索可发现事实，再问不可发现的产品/取舍问题。
- 优先用 `request_user_input` 问 1-3 个会改变计划的问题。
- 最终用 `submit_plan` 提交完整计划。
- 计划必须包含：Summary、Implementation Steps、Interfaces / Data Model、Verification、Risks、Assumptions。
- 不要使用 `todo_write` / task tools 表达 Plan Mode 计划。

approved 后的下一轮普通执行注入：

```text
<approved_plan>
The user approved Plan Mode plan version 1 at 2026-05-13T10:00:00Z.
Follow this plan unless newer user input changes it. If it becomes stale, explain the conflict before deviating.

... plan markdown ...
</approved_plan>
```

同时必须写入一条可回放的用户动作事实：

```text
role=user
meta.source=planmode_approval
text=Implement the approved Plan Mode plan version 1.
```

如果 Web 的 `Approve & Run` 直接恢复执行，它本质上就是一次带上述 user message 的 continue；如果 CLI/API 只做 `approve` 而不立刻执行，下一次进入 execution 时也必须先补这条事实或等价 control event。`<approved_plan>` 只是模型上下文视图，不替代 messages / events 事实源。

### 7.4 工具门禁

新增统一 gate：

```go
type ToolGateDecision struct {
    Allowed bool
    Reason  string
}

func PlanModeToolGate(state PlanModeState, toolName string, rawArgs json.RawMessage) ToolGateDecision
```

实现约束：

- **第一层（软约束）**：planning 阶段向 provider 暴露裁剪后的 tool schema（只读工具 + `submit_plan` + `request_user_input` + `get_plan_mode`），减少模型无效重试。
- **第二层（硬约束）**：在现有 `CompletionController.EvaluateToolCall` 链路里追加 Plan Mode gate；即使 schema 漏放，执行前也必须被阻断。
- gate 顺序建议放在 `toolGuard` 基础安全/显式用户约束之后、`requiredArtifactGate` / `parentCoordinationGate` / `goalCompletionGate` 之前。这样 Plan Mode pending 时能优先给出“等待计划审批”的阻断原因，也不会被 active goal 的 finish audit 或 steer finish pressure 拉去执行。
- Plan Mode gate 只决定当前工具是否可用；文件 path safety、symlink escape、shell timeout/env allowlist 仍由原工具执行层保留，审批后不能退化这些安全边界。

审批前允许：

| 工具 | planning | 说明 |
|---|---:|---|
| `read_file` | allow | 只读 |
| `glob` | allow | 只读 |
| `grep_files` / `grep` | allow | 只读 |
| `load_skill` | allow | 只读加载 skill 正文 |
| `todo_read` | allow | 只读 |
| `task_list` / `task_get` | allow | 只读 |
| `feature_list_read` | allow | 只读 |
| `get_goal` | allow | 只读 |
| `get_plan_mode` | allow | 读取当前 Plan Mode |
| `request_user_input` | allow | 仅 root session；阻塞等待用户回答 |
| `submit_plan` | allow | 提交计划并进入 awaiting approval |

审批前拒绝：

| 工具 | planning | 原因 |
|---|---:|---|
| `write_file` | deny | 修改 repo/session 交付内容 |
| `edit_file` | deny | 修改 repo/session 交付内容 |
| `shell` | deny in v1 | v1 不做 shell side-effect 分类，避免误执行 |
| `todo_write` | deny | Plan Mode 不等于 checklist |
| `task_create` / `task_update` | deny | 不自动生成 durable task graph |
| `feature_list_create` / `feature_list_update` | deny | 属于持久状态写入 |
| `create_goal` / `update_goal` | deny | 避免把 Plan Mode 语义混入 Goal 状态流 |
| `agent_spawn` / `agent_status` / `agent_list` | deny in v1 | 不进入 child / delegate 执行面，也不让 extension 工具主导规划 |
| skill command tools | deny by default | skill command 可有副作用；v1 只允许 `load_skill` 读取 skill 正文 |
| workspace extension / custom tools | deny by default | 未标注只读能力前一律不暴露、不执行 |
| queue submit / delegate API | deny for this session | 不绕过审批后台执行 |
| `finish` | deny | 未审批计划不能完成执行任务 |

审批后：

- `status=approved|executing` 时恢复普通工具面。
- `submit_plan` / `request_user_input` 不再作为默认工具暴露，除非用户再次进入 Plan Mode。
- `load_skill` 虽然会更新 session 内的已加载 skill cache，但只读取已注册 skill 正文，不写 workspace 交付内容；Plan Mode v1 允许它，前提是仍遵循当前 exact skill name 约束。加载 skill 只允许模型阅读说明，不代表该 skill 暴露的 command tools 可以在 planning 阶段执行。
- allowlist 必须是显式白名单；不认识的工具名、未来新增工具和 workspace extension tool 默认拒绝，直到在 Plan Mode policy 中明确分类。

门禁错误返回给模型的文本建议：

```text
Plan Mode is awaiting approval. This tool is not available before the user approves the proposed plan. Continue by reading/searching, asking request_user_input, or submit_plan.
```

### 7.5 `submit_plan` 工具

工具 schema：

```json
{
  "name": "submit_plan",
  "description": "Submit the complete Plan Mode plan for user approval. This does not execute the plan.",
  "parameters": {
    "type": "object",
    "additionalProperties": false,
    "required": ["title", "summary", "plan_markdown", "verification"],
    "properties": {
      "title": { "type": "string" },
      "summary": { "type": "string" },
      "plan_markdown": { "type": "string" },
      "assumptions": { "type": "array", "items": { "type": "string" } },
      "risks": { "type": "array", "items": { "type": "string" } },
      "verification": { "type": "array", "items": { "type": "string" } }
    }
  }
}
```

执行语义：

1. 校验当前 `planmode.status` 是 `planning`。
2. 写 `planmode.json`：`status=awaiting_approval`、`plan_version += 1`、保存完整 Markdown。
3. 写 `artifacts/planmode-plan.md`。
4. 写 `planmode.plan_submitted` 到 history 和 events。
5. 通知 engine 在当前 tool batch 后停止 provider loop，把 session state 置为 `awaiting_input`，并设置 `state.phase="plan_approval"`（不新增 `state.status` 枚举值）。
6. WebConsole 渲染计划卡片和审批按钮。
7. 不允许同一轮在 `submit_plan` 后继续执行其他模型工具；pending plan 不是 `finish`，session 不应进入 `completed`。

同一 provider response 如果返回多个 tool call，且其中包含 `submit_plan`：

- 按顺序执行到第一个 `submit_plan`。
- 为同批后续 tool call 写入合成错误 tool result，例如 `Error: submit_plan ended Plan Mode turn; this later tool call was not executed`。
- append 完整 tool message 后停止 provider loop，进入 `awaiting_input + phase=plan_approval`。
- 不能简单 `return` 而漏写后续 tool result；Anthropic / Google replay 都需要每个已出现的 tool call 有对应 tool result。

### 7.6 `request_user_input` 工具

第一版应实现为 Plan Mode 专用工具，不开放给普通 default mode。

执行语义：

1. 仅 root session 可用；child / delegated session 默认拒绝。
2. 仅 `planmode.status=planning` 可用。
3. 校验问题数量 1-3，每题必须有 2-3 个互斥选项；推荐选项排第一。
4. 校验每题 `id` 为稳定 snake_case，`header` 简短可显示，option label / description 符合 UI schema；后端规范化时自动启用 free-form Other，不要求模型传 `is_other`。
5. 写 `planmode.pending_request` 和 `planmode.input_requested` event；同时把 `state.status` 暂置为 `awaiting_input`、`state.phase="plan_input"`，让 WebConsole / CLI 能解释当前阻塞原因。
6. CLI run：若 TTY 可用，直接在终端提示用户选择或填写 Other。
7. Web run：通过 WebConsole endpoint 回答；runner 在内存 broker 上等待。
8. 若没有即时 responder 但存在可恢复回答入口，持久化 `pending_request` 并让 session 停在 `awaiting_input + phase=plan_input`；后续 answer API 先补 tool result，再恢复 provider turn。
9. 若 CLI 非交互（无 TTY）且没有任何可恢复回答入口，必须在写入 `pending_request` 前返回可 replay 的工具错误（例如 `request_user_input requires interactive responder (TTY or Web API)`），并且不得留下不可回答的 pending request 或无限等待。
10. 收到回答后，先 append 对应 `tool_call_id` 的 tool result，再写 `planmode.input_answered`、清空 `pending_request`，并恢复 `state.phase` 到 planning loop 可继续识别的阶段。
11. 若 context cancelled / process crash / Web active handle 丢失，后续 resume 必须能用 `pending_request.tool_call_id` 追加回答或取消 tool result。不要在没有写入 tool result 的情况下清空 `pending_request`。实现上需要区分三类错误：用户主动取消应写取消 tool result 并取消 Plan Mode；不可恢复的 no-responder/no-TTY 错误应在 pending request 落盘前失败；已经落盘 pending request 后出现的 recoverable 等待错误必须保留 pending request 供 `/planmode/input` 或 cancel 补偿。

当前 Engine 会在执行 tool 前先把 assistant message 和 tool call 落到 `messages.jsonl`，因此 `request_user_input` 不能只靠内存等待。v1 需要明确两条路径：

- **同步等待路径**：TTY 或 Web-owned active handle 存在时，tool handler 可以阻塞等待回答；回答到达后立即返回 tool result，当前 provider loop 继续。
- **可恢复暂停路径**：active handle 不存在、server 重启、context cancel 或等待超时时，`planmode.pending_request.tool_call_id` 必须成为恢复锚点。当前 v1 的回答补偿入口是 Web/API 的 `/planmode/input`；取消补偿可走 `/planmode/cancel` 或 CLI `continue --cancel-plan`。补偿时必须先 append 对应该 `tool_call_id` 的 tool result，再启动 provider turn；若用户取消，则 append `is_error=true` 的 tool result。

这条补偿逻辑应放在 runtime/session 层，Web handler 只提交回答或取消动作。否则 Web 刷新、进程重启或 CLI 非交互路径会留下无法 replay 的悬空 tool call。

建议与 Codex 保持相同参数形状，方便以后兼容：

```json
{
  "questions": [
    {
      "id": "scope",
      "header": "Scope",
      "question": "Which implementation boundary should this plan use?",
      "options": [
        {
          "label": "Core only (Recommended)",
          "description": "Only add runtime and Web session integration."
        },
        {
          "label": "Core plus CLI",
          "description": "Also add CLI plan approval commands in the same slice."
        }
      ]
    }
  ]
}
```

## 8. WebConsole 交互设计

### 8.1 首 prompt 输入区

在现有 `Goal` 按钮旁新增 `Plan` 按钮：

```html
<button class="plan-toggle-btn" id="plan-toggle-btn" type="button" aria-pressed="false">
  <i data-lucide="clipboard-list"></i>
  <span>Plan</span>
</button>
```

布局规则：

- `Plan` 与 `Goal` 并列，都是轻量 toggle。
- 默认关闭；关闭时现有体验不变。
- 开启后 placeholder 改为：`Ask for a plan first...`。
- `Goal` 与 `Plan` 可同时开启。
- 不弹出复杂表单，不要求用户提前写 success criteria / milestones。

### 8.2 现有 session 中的入口

现有 session 状态为 `awaiting_input | paused | failed` 时，输入区也显示 `Plan` 按钮：

- 用户开启 `Plan` 后发送消息，前端调用 continue，并带 `plan_mode: { enabled: true }`。
- 后端进入 `planning`，审批前阻断执行工具。

状态为 `running` 时：

- 不允许直接切 Plan Mode；可以显示 disabled 按钮或 tooltip。
- 用户仍可用 steer 提醒当前 agent “先不要改，等我确认计划”，但这不等同 Plan Mode。

状态为 `awaiting_approval` 时：

- 输入框 action label 显示：`Ask for plan changes: Enter sends revision feedback.`
- Send 走 `planmode/revise` 或 continue-with-plan-revision。

### 8.3 Timeline 渲染

新增消息卡类型：

- `planmode.input_requested`：显示问题卡，用户可直接选项回答。
- `planmode.plan_submitted`：显示 plan card。
- `planmode.plan_approved`：显示批准记录。
- `planmode.plan_revised`：显示修订请求。
- `planmode.cancelled`：显示取消记录。

Plan card 内容：

- title / summary
- plan version
- Markdown plan body
- assumptions / risks / verification
- 按钮：`Approve & Run`、`Ask for Changes`、`Cancel`

### 8.4 Inspector 面板

在现有右侧 inspector 中增加 `Plan` section，位置建议在 `Goal` 上方或与 `Goal` 相邻：

- `status`
- `objective`
- `plan version`
- `approved version`
- latest plan summary
- pending question
- action buttons

如果 session 同时有 Goal：

- Goal 面板继续显示 durable objective 和 completion audit。
- Plan 面板显示 execution approval gate。
- UI 文案必须区分两者，避免用户误以为批准 Mission plan 就会自动执行。

### 8.5 API payload

`POST /api/sessions/start`：

```json
{
  "prompt": "...",
  "goal": {
    "enabled": true,
    "mode": "goal",
    "objective": "..."
  },
  "plan_mode": {
    "enabled": true,
    "objective": "..."
  }
}
```

`POST /api/sessions/{id}/continue`：

```json
{
  "message": "...",
  "plan_mode": {
    "enabled": true
  }
}
```

新增 endpoints：

```text
GET  /api/sessions/{id}/planmode
POST /api/sessions/{id}/planmode/approve
POST /api/sessions/{id}/planmode/revise
POST /api/sessions/{id}/planmode/cancel
POST /api/sessions/{id}/planmode/input
```

实现注意：

- 新增 mutation 路由需要并入 `guardUnsafeAPIRequest` 现有约束（same-origin 或 `X-Go-Cli-Agent-Web: 1`）。
- `expectsJSONBody()` 需要把 `planmode/revise`、`planmode/input` 纳入 JSON Content-Type 校验。

语义：

- `approve`：批准 latest plan version，并按 `Approve & Run` 语义恢复执行；后端必须写 `planmode.plan_approved` event，并追加 `meta.source=planmode_approval` 的 user message 或等价 durable control event。
- `revise`：body `{ "message": "..." }`，把用户意见追加成新 user message，状态回到 `planning`。
- `cancel`：状态改为 `cancelled`，session 回到 awaiting input，不执行。
- `input`：回答 `request_user_input` pending request。
- 如果 `input` 到达时当前 Web server 仍持有 active handle，应通过 broker 唤醒正在等待的 tool call；如果 active handle 已丢失，则走可恢复暂停路径，先补 tool result 再 launch 一个 continue。

`SessionDetailResponse` 增加：

```go
PlanMode *session.PlanModeState `json:"plan_mode,omitempty"`
```

### 8.6 前端文件改动点

第一轮实现应集中在现有 WebConsole 文件：

- `internal/webconsole/dto.go`：新增 `PlanModeDraftRequest`、响应字段。
- `internal/webconsole/service.go`：新增 planmode routes、start/continue DTO 转换。
- `internal/webconsole/assets/index.html`：在 Goal button 旁加 Plan button。
- `internal/webconsole/assets/api.js`：start/continue payload 加 `plan_mode`，新增 planmode API helpers。
- `internal/webconsole/assets/app.js`：新增 `state.planModeEnabled`、toggle、collect、send/revise/approve/cancel handler。
- `internal/webconsole/assets/events.js`：增加 planmode event descriptor，避免 timeline 文案散落在 `app.js` / `session-view.js`。
- `internal/webconsole/assets/session-view.js`：渲染 timeline plan card 和 inspector Plan section。
- `internal/webconsole/assets/styles.css`：复用 Goal toggle 样式，新增 active/awaiting approval 状态。

## 9. CLI 设计

CLI 不是本轮用户最关心的入口，但为了脚本化、CI 和故障恢复，Plan Mode 不能只存在 WebConsole。

### 9.1 `run` / `exec`

新增：

```text
go-cli-agent run --plan "实现 X"
go-cli-agent run --plan --goal "长期目标" "实现 X"
go-cli-agent exec --plan "实现 X"
go-cli-agent exec --plan-only "实现 X"
```

语义：

- `--plan`：开启 Plan Mode，提交计划后 session 进入 `awaiting_approval`；批准动作通过 `continue --approve-plan`、Web `Approve & Run` 或后续等价 API 恢复执行。
- `--plan --goal "<objective>"`：同时创建 durable goal 和 Plan Mode（保持当前 `--goal` 是字符串 objective 的兼容语义，不改成布尔开关）。
- `exec --plan`：与 `run --plan` 同样产生审批门禁，但不进入交互式批准 UI。
- `--plan-only`：只生成计划并停在 `awaiting_approval`，不进入执行；适合脚本和 review。对 `exec` 而言，`awaiting_approval` 是成功生成计划的终止状态，不能被现有 `incomplete_no_finish` 策略误判为失败。

注意：

- 如果用户只传 `--plan` 不传独立 objective，默认使用 prompt 作为 objective。
- `exec --plan-only` 结束码建议为 `0`，但输出中明确 `status=awaiting_approval`。
- v1 不要求在普通 CLI 里实现复杂 TTY 审批表单；若实现 inline approval prompt，必须仍写入同一条 `meta.source=planmode_approval` user message，并覆盖 crash / Ctrl-C / no-TTY 回退测试。

### 9.2 `continue`

新增：

```text
go-cli-agent continue <session-id> --plan --message "下一步先规划 refactor"
go-cli-agent continue <session-id> --approve-plan
go-cli-agent continue <session-id> --cancel-plan
```

语义：

- `--plan`：下一条 continue 进入 Plan Mode。
- `--approve-plan`：批准 latest plan version 并继续执行。
- `--cancel-plan`：取消 pending Plan Mode。

### 9.3 `plan` command

如果实现上更清晰，可以增加低调的 session utility command：

```text
go-cli-agent plan show <session-id> [--json]
go-cli-agent plan approve <session-id> [--json]
go-cli-agent plan revise <session-id> --message "..." [--json]
go-cli-agent plan cancel <session-id> [--json]
```

这个 command 不应在 README hero 区域主推；默认文档仍以 `run` / `exec` / `continue` / `steer` 为主。

## 10. Provider 与恢复语义

### 10.1 Provider adapter 不改协议

Plan Mode 只改变 runtime 层输入：

- tool schema 集合。
- developer instructions。
- session metadata。
- tool gate。

Provider adapter 不需要知道 Plan Mode 的业务语义，也不需要实现 provider-specific plan replay。

### 10.2 session metadata

在 session metadata / provider request metadata 中记录：

```json
{
  "collaboration_mode": "plan",
  "plan_mode_id": "pm_...",
  "plan_status": "planning",
  "approved_plan_version": 0
}
```

批准后：

```json
{
  "collaboration_mode": "default",
  "plan_mode_id": "pm_...",
  "plan_status": "executing",
  "approved_plan_version": 1
}
```

### 10.3 crash / resume

恢复规则：

- `planning`：`continue` 默认仍以 Plan Mode instructions 恢复。
- `awaiting_user_input`：CLI/Web 显示 pending questions；回答后继续同一 planning flow。
- `awaiting_approval`：不自动执行；必须 approve/revise/cancel。
- `approved` 但尚未进入执行：`continue` 自动注入 approved plan、追加 plan approval user message，并转为 `executing`。
- `executing`：普通恢复，但 session summary 和 checkpoint 应保留 approved plan 摘要。

为避免 dangling tool call：

- `request_user_input` 如果在等待期间收到 interrupt/cancel，必须写入对应 `tool_call_id` 的取消/错误 tool result，并记录 `planmode.input_cancelled` event；event 本身不能替代 tool result。
- `submit_plan` 是 terminal planning tool；执行后 runtime 应停止 turn，不再让模型继续调用其他工具。
- resume 发现 `state.phase=plan_input` 且存在 `pending_request.tool_call_id` 时，必须优先处理回答 / 取消补偿，再启动新的 provider turn；不能直接把 Plan Mode instructions 注入下一轮导致 provider replay 缺少 tool result。

## 11. 安全边界

Plan Mode v1 的安全原则是“审批前宁可少跑，不要误跑”：

- v1 不允许 `shell`，即使用户可能只想 `go test` 或 `git diff`。原因是命令 side effect 分类容易出错。
- v1 允许 read/search/load_skill/todo_read/task_list/task_get/get_goal。
- v1 不允许 `todo_write`，因为这会把 Plan Mode 和 checklist 混淆。
- v1 不允许 child/delegate/queue，避免绕过 root session 审批；这包括 tool 层的 `agent_*`，也包括 Web/API/CLI 提交 parent-linked background job 的控制面。
- v1 不允许 skill command tools、workspace extension tools 和未知未来工具，除非后续显式增加 read-only annotation 和测试。
- v1 不允许 `finish`，因为 pending plan 不是执行完成。
- 所有 planmode API mutation 继续走 WebConsole 现有 local mutation guard / CSRF-ish header 约束。

后续可选增强：

- 增加 `PlanModeShellPolicy=blocked|read_only_dry_run|approval_required`。
- 对 `git diff`、`git status`、`go test` 这类命令做 allowlist，但这不是第一版必需。

## 12. 实施阶段

以下 checkbox 是 v1 实施验收账本。条目被标记为完成，表示已有对应代码、测试、文档或本地 Web/API smoke 证据；后续修改 Plan Mode 时仍应重新跑相关验证。

### Phase PM-0: spec 与文档收敛

- [x] 更新 `spec/00-product.md`：把 Plan Mode 定义为 Web-first v1 convergence feature，并保留 CLI fallback。
- [x] 更新 `spec/01-runtime-architecture.md`：增加 `SessionPlanModeManager` 或把 planmode 纳入 `SessionStore` 职责。
- [x] 更新 `spec/09-phase-plan.md`：把 Plan Mode 作为 core convergence 文档项，而非 Web-only 功能。
- [x] 更新 `spec/11-spec-audit-and-traceability.md`：记录 Plan Mode 参考 Codex / ForgeCode 的取舍。
- [x] 更新 `spec/17-web-console.md`：补充 Plan button、Plan panel、approval flow。
- [x] 更新 `spec/18-durable-contract-and-completion.md`：补充 `planmode.json` 与 completion / tool gate 关系。
- [x] README 使用 Web-first 默认叙事，并保留 CLI fallback 短入口。

### Phase PM-1: session store 与状态

- [x] 新增 `internal/session/planmode.go`。
- [x] 新增 store methods：create/load/save/history/approve/cancel。
- [x] 新增 `artifacts/planmode-plan.md` 派生写入。
- [x] session summary 写入 Plan Mode 状态与 fact-file 路径。
- [x] tests：store round-trip、invalid status、history append、owner-only dir mode 不回退。

### Phase PM-2: runtime prompt 与工具门禁

- [x] `StartRequest` / `ContinueRequest` 增加 `PlanMode`。
- [x] 不从自然语言 prompt 自动推断 Plan Mode；只接受 CLI flag / Web toggle / API payload。
- [x] engine 在 planning 状态注入 Plan Mode developer instructions。
- [x] registry 增加 `get_plan_mode`、`submit_plan`、`request_user_input`。
- [x] 在 `CompletionController.EvaluateToolCall` 链路增加 Plan Mode gate（覆盖 feature list 等变更型工具），并在 provider tool schema 层做 planning allowlist。
- [x] 未知工具、skill command tools、workspace extension tools、`agent_status` / `agent_list` 默认拒绝，避免新增工具绕过 planning allowlist。
- [x] `submit_plan` 后停止当前 provider loop，session 进入 `awaiting_input` + `phase=plan_approval`。
- [x] `submit_plan` 与同批多 tool call 的交互必须补齐后续 tool result，避免 replay 悬空。
- [x] `request_user_input` 区分主动取消、不可恢复无 responder、以及可恢复等待错误；一旦 pending request 带 `tool_call_id` 落盘，不能在缺少 tool result 的情况下清空。
- [x] approved plan 注入下一轮普通执行。
- [x] approve 后追加 `meta.source=planmode_approval` user message，保证 replay / session summary 能解释执行来源。
- [x] tests：审批前 write/edit/shell/todo/task/agent_spawn/agent_status/agent_list/skill-command/custom-tool/finish 被拒绝；read/search/get_goal 允许；prompt 里写“先计划”不会自动进 Plan Mode；Plan Mode gate 优先于 goal finish audit；approved 后普通工具恢复；`submit_plan` 同批后续工具得到合成错误 result。

### Phase PM-3: CLI

- [x] `run` / `exec` 支持 `--plan`。
- [x] `exec` 支持 `--plan-only`。
- [x] `continue` 支持 `--plan`、`--approve-plan`、`--cancel-plan`。
- [x] v1 决策：不新增独立 `plan show|approve|revise|cancel` command，先收敛在 `run` / `exec` / `continue` 与 Web/API 控制面。
- [x] tests：flag parse、json output、approve 后继续执行请求正确。

### Phase PM-4: Web API

- [x] `StartSessionRequest` / `ContinueSessionRequest` 增加 `PlanMode *PlanModeDraftRequest`。
- [x] `SessionDetailResponse` 增加 `PlanMode`。
- [x] 增加 `/planmode` routes。
- [x] Web-managed active runner 支持 `request_user_input` answer broker。
- [x] active handle 丢失时，`/planmode/input` 能用 pending `tool_call_id` 先补 tool result，再恢复 planning turn。
- [x] `/planmode/input` 对外使用 `request_id` + `answers` 的 snake_case JSON 契约；前端可使用内部 camelCase，但必须在 API helper 做单点转换。
- [x] parent-linked queue/delegate API 在 pending Plan Mode 下拒绝，独立新 session / queue job 不受影响。
- [x] tests：start payload、continue payload、approve/revise/cancel/input endpoints、approve 后 user message/event 可回放、pending question state phase、session detail includes plan mode、pending input crash/restart recovery 不留下 dangling tool call、parent-linked queue/delegate 不能绕过审批。

### Phase PM-5: WebConsole 前端

- [x] 输入区 Goal 旁新增 Plan toggle。
- [x] send path 带上 `plan_mode`。
- [x] 状态为 awaiting approval 时，send 变成 revise。
- [x] timeline 渲染 plan card。
- [x] inspector 增加 Plan section。
- [x] pending question 卡片支持选择和 Other。
- [x] `Approve & Run` 调 approve endpoint，并刷新 session。
- [x] `Ask for Changes` 聚焦输入框，提交 revision。
- [x] `Cancel` 调 cancel endpoint。
- [x] `node --check internal/webconsole/assets/*.js`。

### Phase PM-6: 验收与回归

- [x] `go test ./internal/session ./internal/runtime ./internal/tools ./internal/webconsole ./internal/app`。
- [x] `go test ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`。
- [x] `gofmt -l` 无漂移。
- [x] `node --check internal/webconsole/assets/*.js`。
- [x] Web/API smoke 验收（本地 fake OpenAI-compatible endpoint，不依赖外部 provider）：
  - [x] 新 session 只开 Plan：provider schema 只暴露 planning allowlist，模型只能提交计划，不能改文件。
  - [x] Plan + Goal 同开：Goal 返回 objective，Plan 返回 approval gate。
  - [x] awaiting approval 调 approve 后继续执行，并写 `planmode_approval` user message。
  - [x] awaiting approval 输入修订意见后生成新 plan version。
  - [x] running / pending session 中 Plan button 不可误启用，前端只允许 completed / awaiting_input / paused / failed 的非 pending Plan session 显示 composer。
  - [x] 刷新/重取 detail 后 pending plan / pending question 仍可恢复，并可用 `/planmode/input` 补齐 `request_user_input` tool result。

## 13. 推荐第一版计划格式

`submit_plan.plan_markdown` 建议要求模型生成：

```markdown
# [Plan Title]

## Summary

[目标、当前事实、推荐路径。]

## Implementation Plan

- [ ] Step 1: [具体改动，包含理由和涉及边界。]
- [ ] Step 2: [具体改动，包含理由和涉及边界。]
- [ ] Step 3: [具体改动，包含理由和涉及边界。]

## Interfaces And Data Model

[新增 CLI/API/DTO/schema/session files/tool schema。]

## Verification

- [ ] [自动测试]
- [ ] [手工验证]

## Risks And Mitigations

- [风险] -> [缓解]

## Assumptions

- [默认取舍]
```

这吸收 ForgeCode Muse 的 checkbox / verification / risk 格式，同时保留 Codex Plan Mode 的 approval gate 和 request-user-input 能力。

## 14. 关键设计决策

1. **Plan Mode 独立于 Goal/Mission。**
   Goal 是 durable objective，Plan Mode 是 execution gate。复用 `MissionPlan.PlanStatus` 会让事实源和行为边界混乱。

2. **v1 使用 `submit_plan` 工具，而不是只解析 `<proposed_plan>`。**
   当前项目已有稳定 tool registry 和 session store。工具提交更容易保证计划落盘、进入审批状态、触发 Web API。后续可以增加 `<proposed_plan>` streaming UI，但不作为第一版阻塞项。

3. **v1 阻断 shell。**
   Codex 允许某些非变更命令，但本项目第一版没有命令 side-effect classifier。为了避免“计划模式误执行”，先禁 shell，后续再加 allowlist 或 approval-required policy。

4. **Plan button 放在现有 session 输入区。**
   用户不需要进入独立设置页；这符合当前 WebConsole session-first 设计，也避免把 Web-first 叙事扩张成复杂 dashboard。

5. **批准后由用户动作恢复执行。**
   `Approve & Run` 是唯一从 planning gate 进入 execution 的默认路径。模型提交计划不会自动开工。

6. **不自动生成 tasks / child / queue。**
   计划可以建议这些动作，但执行阶段仍由模型或用户显式调用相关工具和 API。

7. **批准动作必须进入 session 事实源。**
   参考 Codex 的 plan implementation flow，批准不是前端瞬时状态。它必须留下 user message / control event，再由普通 execution turn 消费 approved plan，否则 crash / resume / provider replay 无法解释为什么规划阶段突然开始执行。

## 15. v1 落地文件清单

v1 落地 commit 覆盖这些文件：

```text
spec/00-product.md
spec/01-runtime-architecture.md
spec/09-phase-plan.md
spec/11-spec-audit-and-traceability.md
spec/17-web-console.md
spec/18-durable-contract-and-completion.md
internal/session/planmode.go
internal/session/planmode_test.go
internal/session/types.go
internal/session/store.go
internal/runtime/planmode.go
internal/runtime/runner.go
internal/runtime/engine.go
internal/runtime/completion_controller.go
internal/runtime/delegation.go
internal/runtime/session_summary.go
internal/runtime/planmode_test.go
internal/tools/registry.go
internal/tools/registry_test.go
internal/webconsole/dto.go
internal/webconsole/service.go
internal/webconsole/service_test.go
internal/webconsole/assets/index.html
internal/webconsole/assets/api.js
internal/webconsole/assets/app.js
internal/webconsole/assets/events.js
internal/webconsole/assets/session-view.js
internal/webconsole/assets/styles.css
internal/app/app.go
internal/app/app_test.go
README.md
```

如果要拆分，推荐拆成两轮：

- 第一轮：spec + session store + runtime gate + CLI tests。
- 第二轮：Web API + WebConsole UX + browser/manual validation。

但用户体验上真正闭环需要第二轮完成，否则 Plan Mode 只能从 CLI 使用，达不到“按钮化、不麻烦”的目标。
