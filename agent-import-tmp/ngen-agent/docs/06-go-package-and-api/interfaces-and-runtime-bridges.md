# 核心接口与 runtime bridges

## 1. 当前 runtime service

当前代码的核心 bridge 是 `internal/runtime.Service`。它拥有以下 active contract：

```go
type Service struct {
    Store  *artifact.Store
    Config task.Config
}

func New(workspaceRoot string, cfg task.Config) *Service

func (s *Service) Create(ctx context.Context, tf task.TaskFile) (task.Spec, error)
func (s *Service) CreateTask(ctx context.Context, tf task.TaskFile, source, projectStepID, projectBranchID string) (task.TaskView, error)
func (s *Service) Auto(ctx context.Context, taskID string) (task.StatusSnapshot, []task.Event, error)
func (s *Service) Run(ctx context.Context, taskID string) (task.StatusSnapshot, []task.Event, error)
func (s *Service) Resume(ctx context.Context, taskID string) (task.StatusSnapshot, []task.Event, error)
func (s *Service) Status(ctx context.Context, taskID string) (task.StatusSnapshot, error)
func (s *Service) Review(ctx context.Context, taskID string) (task.ReviewReport, error)
func (s *Service) TailEvents(taskID string, limit int) ([]task.Event, error)
func (s *Service) TailEventsAfter(taskID, afterEventID string, limit int) ([]task.Event, error)
func (s *Service) ListApprovals(ctx context.Context, taskID string) ([]task.ApprovalRecord, error)
func (s *Service) ListOwnedApprovals(ctx context.Context, taskID string) ([]task.ApprovalRecord, error)
func (s *Service) RequestApproval(ctx context.Context, taskID, scope, reason string) (task.ApprovalRecord, error)
func (s *Service) DecideApproval(ctx context.Context, taskID, approvalID, decision string) (task.ApprovalRecord, error)
func (s *Service) RequestInput(ctx context.Context, taskID, field, prompt string, required bool) (task.InputRequestRecord, error)
func (s *Service) ListInputRequests(ctx context.Context, taskID string) ([]task.InputRequestRecord, error)
func (s *Service) RespondInput(ctx context.Context, taskID, requestID, response string) (task.InputRequestRecord, error)
func (s *Service) SetWatch(ctx context.Context, taskID string, interval time.Duration, reason string) (task.Watch, error)
func (s *Service) ListWatches(ctx context.Context) ([]task.Watch, error)
func (s *Service) CancelWatch(ctx context.Context, watchID string) (task.Watch, error)
func (s *Service) SchedulerTick(ctx context.Context, now time.Time) ([]string, error)
func (s *Service) StartSession(ctx context.Context, taskID, mode string) (task.Session, error)
func (s *Service) ReadSession(ctx context.Context, sessionID string) (task.Session, []task.SessionMessage, error)
func (s *Service) SessionSnapshot(ctx context.Context, sessionID string) (task.SessionSnapshot, error)
func (s *Service) ListSessions(ctx context.Context) ([]task.Session, error)
func (s *Service) UpdateTaskPlan(ctx context.Context, taskID string, update task.PlanUpdate, source string) (task.TaskView, error)
func (s *Service) PatchTaskPlan(ctx context.Context, taskID string, patch task.PlanPatch, source string) (task.TaskView, error)
func (s *Service) GetProject(ctx context.Context) (task.ProjectView, error)
func (s *Service) UpdateProject(ctx context.Context, update task.ProjectUpdate, source string) (task.ProjectView, error)
func (s *Service) PatchProject(ctx context.Context, patch task.ProjectPatch, source string) (task.ProjectView, error)
func (s *Service) CreateMission(ctx context.Context, req task.MissionCreateRequest) (task.MissionView, error)
func (s *Service) GetMission(ctx context.Context, missionID string) (task.MissionView, error)
func (s *Service) MissionStatus(ctx context.Context, missionID string) (task.MissionView, error)
func (s *Service) MissionPlan(ctx context.Context, missionID string) (task.MissionPlanView, error)
func (s *Service) ApproveMissionPlan(ctx context.Context, missionID string) (task.MissionView, error)
func (s *Service) ValidateMission(ctx context.Context, missionID, milestoneID string) (task.MissionView, error)
func (s *Service) RunMission(ctx context.Context, missionID string) (task.MissionView, error)
func (s *Service) PauseMission(ctx context.Context, missionID, reason string) (task.MissionView, error)
func (s *Service) ResumeMission(ctx context.Context, missionID string) (task.MissionView, error)
func (s *Service) OpenMissionForTask(ctx context.Context, taskID string) (task.MissionView, error)
func (s *Service) OpenOrSetMissionForTask(ctx context.Context, taskID string, req task.MissionCreateRequest) (task.MissionView, error)
func (s *Service) PromoteMemory(ctx context.Context, taskID string, promote task.MemoryPromotion, source string) (task.MemoryEntry, error)
func (s *Service) PromptSession(ctx context.Context, sessionID, message string) (task.Session, task.StatusSnapshot, []task.Event, error)
func (s *Service) CancelSession(ctx context.Context, sessionID string) (task.Session, error)
func (s *Service) SpawnWorker(ctx context.Context, parentTaskID, role, objective string) (task.WorkerContract, error)
func (s *Service) ListWorkers(ctx context.Context, parentTaskID string) ([]task.WorkerContract, error)
func (s *Service) WorkerSnapshot(ctx context.Context, parentTaskID, workerID string) (task.WorkerSnapshot, error)
func (s *Service) ListWorkerSnapshots(ctx context.Context, parentTaskID string) ([]task.WorkerSnapshot, error)
func (s *Service) SyncWorker(ctx context.Context, parentTaskID, workerID string) (task.WorkerContract, error)
func (s *Service) ContinueWorker(ctx context.Context, parentTaskID, workerID string) (task.WorkerContract, error)
func (s *Service) MemoryMarkdown(ctx context.Context) ([]byte, error)
```

## 2. 当前 artifact store 能力

当前 store 需要覆盖：

- task / plan / state / baseline
- plan mutation history
- workspace project graph + project mutation history
- workspace missions + validation contracts + feature/milestone sets + validation runs
- progress / handoff
- context latest-pack / context summary
- continuity latest snapshot / append-only continuity history
- criteria latest snapshot / append-only criteria history / completion
- verification / review
- events / findings / approvals
- workspace edit records
- diagnostics / checkpoints
- watches
- context summary
- ACP sessions
- worker contracts
- worker workspace lifecycle / baseline / settlement / result / reconcile runtime records
- workspace memory

## 3. 当前 verifier / review bridge

当前 foundation verifier contract：

```go
type Pipeline struct {
    Config task.Config
}

func New(config task.Config) *Pipeline
func (p *Pipeline) CaptureBaseline(ctx context.Context, spec task.Spec) task.Baseline
func (p *Pipeline) Run(ctx context.Context, spec task.Spec) task.VerificationReport
```

当前 review contract：

```go
type Input struct {
    Spec            task.Spec
    Verification    task.VerificationReport
    HandoffExists   bool
    HandoffStale    bool
    Criteria        task.CriteriaSnapshot
    ContextRefs     []string
    ChangedPaths    []string
    ScopeDriftPaths []string
    WorkerEvidence  []WorkerEvidence
}

func EvaluateWithContext(input Input) (task.ReviewReport, []task.Finding)

func Evaluate(
    spec task.Spec,
    verification task.VerificationReport,
    handoffExists bool,
    criteria task.CriteriaSnapshot,
) (task.ReviewReport, *task.Finding)
```

`Evaluate` remains as a compatibility wrapper. Runtime review/done gate should use `EvaluateWithContext` so review can classify artifact-backed `missing_evidence`、`scope_drift`、`stale_context_risk` 与 `worker_trust_gap` findings.

## 4. 当前 machine-readable bridge

当前冻结以下 external machine-readable bridge：

- JSONL events
  - `auto --json`
  - `run --json`
  - `resume --json`
  - `events tail --json`
  - `TailEventsAfter(taskID, afterEventID, limit)` backs CLI `events tail --after`, ACP `task.events`, web JSON `?after=`, and SSE `Last-Event-ID` / `?after=` replay from the same append-only `events.jsonl`; stale cursors must error instead of silently returning the latest tail.
- `status_snapshot`
  - `status --json`
  - `last_checkpoint_ref` / `restore_clues` expose latest checkpoint restore bearings without requiring consumers to parse checkpoint JSON first.
- ACP stdio JSON-RPC
  - `initialize`
  - `rpc.ping`
  - `task.create`
  - `input.request`
  - `input.list`
  - `input.respond`
  - `worker.spawn`
  - `worker.list`
  - `worker.sync`
  - `worker.continue`
  - `session.start`
  - `session.list`
  - `session.read`
  - `session.snapshot`
  - `session.prompt`
  - `session.cancel`
  - `task.run`
  - `task.list`
  - `task.get`
  - `task.update`
  - `task.patch`
  - `project.get`
  - `project.update`
  - `project.patch`
  - `mission.status`
  - `memory.show`
  - `memory.promote`
  - `task.resume`
  - `task.auto`
  - `task.status`
  - `task.review`
  - `task.events`
  - `permission.request`
  - `permission.list`
  - `permission.decide`
- ACP notification stream
  - `ngen.notification`

JSON-RPC 错误码当前至少冻结：

- `-32600` invalid request
- `-32601` method not found
- `-32602` invalid params
- `-32000` runtime/internal error

当前 provider / ACP / terminal / worker / memory bridge 都已进入 active interface contract。当前稳定 machine-readable derived objects 为 `status_snapshot`、`session_snapshot`、`worker_snapshot`、`task_view`、`task_list_entry`、`project_view`、`mission_view`、`mission_status_snapshot` 与 `acp_notification`。

补充约束：

- `coding` task 在全部当前 provider mode（`builtin`、`command`、`openai-response`、`openai-comp`、`anthropic`）下，现在允许在 verifier 失败后执行最多 3 次 bounded workspace edit，并把 durable write truth 追加到 `workspace_edits.jsonl`
- provider input 当前也会显式带出 task-local `context_pack` 与 `workspace_memory`，让 decision / repair prompt 可以在新一轮 loop 里续上 long-horizon continuity，而不是只依赖 recent events
- provider input / workspace observation input / workspace edit input 现在也会把 task-scoped `project_focus` 暴露在 `context_pack`、`continuity.current_focus` 与 `sprint` 里。它至少表达当前 task 在 workspace project graph 里的 primary step / branch、bound step / branch ids、dependency boundary、workspace ready/blocked pointers 与 project refs，让远端 provider 在复杂项目里优先沿着当前 project binding 推进，而不是先从全量 `project.json` 重新猜测 scope
- provider decision input 现在也会在 task 绑定 mission 时带出 `mission`，其内容来自 `.ngen/missions/<mission_id>/...` 的 `MissionView`：当前 mission status、role plan、validation contract、features、milestones、latest validation findings 与 root task status。workspace observation / workspace edit input 仍以 task-local criteria/sprint/context 为主，避免 repair prompt 直接把 mission prose 当作文件事实。
- provider input / workspace observation input / workspace edit input 现在也会显式带出 `criteria` 这份 acceptance ledger。它不只表达 `met/open`，还会显式带出 `passes`、`selected`、`current_criterion_id` 与 append-only refresh 语义，让 remote provider 在 fresh context 下沿着当前 feature boundary 继续，而不是自行重排 criteria。
- provider input / workspace observation input / workspace edit input 现在也会显式带出 `continuity`。它是 runtime 生成的 structured restart ledger，至少包含 `current_focus` 与 `startup_checklist`，用来让远端 provider 在 fresh context 下直接接着当前 sprint 继续，而不是先从 Markdown prose 重建任务焦点
- provider input / workspace observation input / workspace edit input 现在也会显式带出 `sprint`。它是 runtime 生成的 current-scope contract，至少包含 `primary_criterion_id`、`deferred_criterion_ids`、`completion_signals` 与 working set，用来让远端 provider 直接沿着当前 sprint boundary 推进，而不是把所有 open criteria 一起展开
- provider input / workspace observation input / workspace edit input 现在也会显式带出 baseline 中的 repo bearings：`command_hints` 与 `workspace_snapshot`。这让 remote provider 在长任务恢复时可以优先复用 repo-owned setup / verifier entrypoint 与 git bearings，而不是重新猜测
- provider decision input 现在也会显式带出当前 task 的 `role_contract`，来源是 `.ngen/roles/<role_id>.json`。provider prompt 必须遵守 `allowed_provider_actions` 与 `allowed_worker_roles`；runtime 在 action dispatch 前会二次校验，越权 action 或 child role 会直接报错，不会静默降级。
- workspace memory bridge 现在不再只是 `memory show` 的只读 surface；`PromoteMemory`、ACP `memory.promote` 与 provider `memory_promote` 都会向 `.ngen/memory/entries.jsonl` 追加带 `kind` / `source` / `refs` / `scope` / `paths` / `profiles` / `provider_modes` / `confidence` / `freshness_status` / `last_validated_ref` 的 stable entry，并同步刷新 `MEMORY.md`
- `memory show`、provider decision input、workspace observation input 与 workspace edit input 都读取刷新后的 workspace memory Markdown；path-scoped entry 若指向已不存在的 workspace path，会以 stale label 呈现，而不是作为 fresh task truth 提升优先级
- provider decision input、workspace observation prompt 与 workspace edit prompt 现在也都会显式带出 `session_messages_ref` 与 bounded `session_recent_messages`，让 remote provider 能在 terminal / ACP 多轮 steer 下直接消费同一份 session transcript truth，而不是只看到 `session.last_prompt`
- provider decision contract 现在也允许 `respond`、`task_create`、`task_update` 与 `task_patch`。`respond` 用于把普通 conversational / meta prompt 直接写成 assistant-style session reply，而不推进 task state；`task_create` 用于 materialize 一个新的 durable workspace task，并可选地通过 `project_step_id` / `project_branch_id` 绑定进既有 workspace project graph；`task_update` 用于全量刷新 `plan.json` 的 mutable execution lane；`task_patch` 用于顺序执行更小粒度的 mutable-plan mutation。除 `respond` 只写 session/event truth 外，其余三者都不会改写已有 task 的 canonical verifier / completion truth。对于 provider `task_create`，runtime 还会执行 child-contract normalization：把 `create exactly one durable task` / `bind the new task ...` 这类 parent-only orchestration instruction 从 child `task.json.constraints` 里剥离，并在 operator 没有明确要求 handoff / replace 当前 binding 时清空与父任务当前 project binding 相同的 `project_step_id` / `project_branch_id`，避免新 child 再次 materialize 下一层 task 或直接撞上重复 binding。mutable lane 现在支持 stable `plan_steps[].id`、`parent_step_id`、`depends_on` 与 `priority`，而 patch contract 当前冻结为 `set_explanation`、`upsert_step` 与 `remove_step`；runtime 会把 ready/block/current pointers 与 `plan_revision` 收口进 `plan.json` / `status_snapshot`，并把显式改写追加到 `plan_updates.jsonl`。若远端 provider 直接返回 `run` / `resume` 且 task 还没有 execution lane，runtime 会先按 open criteria 写入一个 system-sourced、one-criterion-at-a-time 的 bootstrap execution plan，再进入真实执行。
- `session.prompt` 下的 provider `task_create` 当前还有一条更强的 operator-boundary contract：一旦该 prompt 成功 materialize 一个 durable workspace task，runtime 就结束该轮 prompt 的 auto continuation 并返回结果，而不是在同一人类 prompt 内继续静默创建第二个 task
- provider decision contract 现在也允许 `project_update` 与 `project_patch`。`project_update` 用于全量刷新 `.ngen/project/project.json` 的 workspace-level durable orchestration graph；`project_patch` 用于顺序执行更细粒度的 project graph mutation。project graph step 当前支持 stable `project_steps[].id`、`parent_step_id`、`depends_on`、`branch_id` 与 `task_id`，branch 当前支持 stable `project_branches[].id`、`status` 与 `task_id`；patch contract 除了 `set_explanation`、`upsert_step`、`remove_step`、`upsert_branch` 与 `remove_branch` 外，还冻结了 `set_step_dependencies`、`set_step_parent`、`bind_step_branch`、`bind_step_task`、`bind_branch_task` 与 `set_branch_status`。runtime 会把 `current_step_id` / `ready_step_ids` / `blocked_step_ids` / `active_branch_ids` 收口进 `project.json`，并把显式改写追加到 `project_updates.jsonl`。
- `RunMission` 当前不是 plain `Run(root_task_id)` wrapper：它会先按 `mission.json.role_plan.orchestrator` 的有效 model 创建 scoped provider config，执行一轮 bounded provider-decision orchestration，再调用 `ValidateMission`。该 pass 使用已有 action surface；mission-scope `task_create` 会注入 `parent_task_id` / `root_task_id` / `lineage_depth`，绑定当前 mission feature，并在创建一个 durable child task 后停止本轮 mission orchestration。
- mission-owned task 通过 lineage helper 判定：task id 等于 mission root task，或 task 的 `root_task_id` 指向 mission root task。`ContinueWorker` 在继续 mission-owned child 时会用 `role_plan.workers` 的有效 model 构造 scoped service，但 child 的实际 role contract 仍是 `coding`、`reviewer`、`security_review` 或 `general_execution`。
- `ValidateMission` 当前先执行 deterministic artifact validator；只有 deterministic pass 未阻塞且 `role_plan.validators.explicit=true` 时，才通过 dedicated read-only mission validation schema 调用 provider。该 schema 只允许 `status`、`summary` 与 findings，不允许 workspace edits、provider decisions、`task_create` 或 worker actions。
- mission service 当前通过 CLI、`/mission` / `/missions` / `/goal` / `/goals` session compact command、ACP `mission.status` 与 web `GET /api/missions/{mission_id}` 暴露；ACP/web wrapper 只能调用上述 mission service methods，不能创建 ACP/web-only mission state。
- provider decision contract 现在也允许 `memory_promote`。它使用 decision `summary` 作为 durable memory text，并通过 `memory_kind` / `memory_refs` 把 reusable milestone / decision / blocker promotion 到 workspace memory pipeline，而不会改写 task-local canonical truth。
- `task.review`、ACP `task.review`、以及 provider `review` action 若先于 verifier 发生，也必须返回正常 machine-readable response，并把任务收敛到 `Blocked/blocked_review`；bridge 不得把缺少 `verification/latest.json` 暴露成裸 `-32000` runtime/internal error
- `task.review`、ACP `task.review`、以及 provider `review` action 在 verifier artifacts 已存在但 `handoff.md` 漂移缺失时，当前也必须先重建 `handoff.md` 再执行 gate；bridge 不得把这类 drift 简化成永久 `handoff_missing` 死锁。若 criteria / verification blocker 仍在，machine-readable 结果仍需明确收敛到 `Blocked/blocked_review`
- `permission.list` 默认返回 task 自己的 `approvals.jsonl` durable history
- `permission.list(include_owned=true)` 允许 parent task 聚合 worker child 的 owned approval history
- `permission.decide` 允许 parent task 解析并决策 owned child approval，但 decision record 仍追加到 child task 的 `approvals.jsonl`
- parent task 的 provider input 与 `session_snapshot` 现在也必须显式带出 managed workers 与 `owned_pending_approvals`
- `PromptSession` 允许在普通 conversational prompt 下先追加 assistant-style direct reply，再持续追加 runtime summary；`CancelSession` 仍必须追加 runtime cancellation summary。failed task outcome 与 operator cancel 这类控制事实也必须显式落到 `.ngen/sessions/*.messages.jsonl`
- provider decision contract 现在也允许 `worker_spawn` 与 `worker_continue`；`worker_spawn` 必须带 `worker_role` 与 `worker_objective`，runtime 会先调用 `SpawnWorker` 再启动 child 的首轮 `ContinueWorker`；当 parent task 的 `managed_workers` 中暴露 `parent_action_type=continue_child` 时，runtime 也可以按 provider 给出的 `worker_id` 直接调用 parent-side `ContinueWorker`
- `worker.sync` / `worker_snapshot` / `workers/*.json` 现在不仅是状态镜像，还必须暴露 blocker、approval / input detail、`parent_action_*` control metadata，以及 child `workspace_mode` / `workspace_status` / `workspace_ref`、`settlement_status` / `settlement_summary` / `settlement_ref`、`result_summary` / `result_ref` / `completion_status` / `review_status` / `verification_status`、以及 `reconcile_mode` / `reconcile_status` / `reconcile_summary` / `reconcile_ref`
- `worker.sync` 还必须把 `worker_runtime/*.workspace.json`、`*.baseline.json`、`*.settlement.json`、`*.result.json` 与 `*.reconcile.json` 持久化成 canonical runtime truth；其中 `*.result.json` 不只保存 accepted child 的 completion / review / verification outcome，也要保存 blocked approval / input 与 approved-but-awaiting-continue 这类 manager-facing blocker truth
- `SpawnWorker` 当前只接受 `coding`、`reviewer`、`security_review` 与 `general_execution` 这组显式 worker role；未知 role 必须返回错误，而不是静默回退；父 task 的 role contract 若未允许目标 child role，也必须返回显式错误
- `worker.continue` 是 parent-side child continuation 的唯一 helper；它不能跳过 pending approval，只能在 child 已回到 `Active` 后继续
