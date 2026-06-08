# 验收矩阵与不纳入范围

## 1. 当前验收矩阵

### AT-001 Task Create

创建任务后，确认生成 `task.json`、`plan.json`、`state.json`、`criteria/latest.json`、`criteria/history.jsonl`、`sprint/latest.json`、`sprint/history.jsonl`、`progress.md` 与初始 checkpoint；同时确认 `criteria/latest.json` 已显式带出 `current_criterion_id` / `passes` / `summary` 这组 acceptance-ledger 字段，并确认 `sprint/latest.json` 已显式带出 `primary_criterion_id` / `completion_signals` / `deferred_criterion_ids` 这组 current-scope contract 字段。

### AT-002 Baseline

首次 `run` 时确认生成 `baseline.json`，并把任务推进到后续 phase。

### AT-002A Criteria-Scoped Plan Progress

创建一个带多条 success criteria 的 task，确认初始 `plan.json` 至少展开 baseline、每条 criterion 与最终 review/done gate。随后制造一个 criterion 仍未闭合的 `blocked_review` 场景，确认 `plan.json` 会把已完成 criterion 标成 `completed`、当前 open criterion 标成 `in_progress`，并把 `state.json.current_step_id` 推进到对应 step；当 task 最终 `Done` 后，确认最终 gate step 也变为 `completed`。

### AT-002B Mutable Execution Plan Surface

通过 `task update --plan-file ...`、`task patch --patch-file ...`、`task get --json`、`task list --json` 与 ACP `task.update` / `task.patch` / `task.get` / `task.list`，确认 runtime 会把 mutable execution checklist 写进 `plan.json`，显式带出 `explanation`、stable execution step id、`parent_step_id` / `depends_on` / `priority`、`revision`、`current_system_step_id`、`current_execution_step_id`、ready/block execution summary 与 `last_mutation_ref`；同时确认 `plan_updates.jsonl` 追加了对应 mutation record，显式区分 `mutation_kind=replace|patch`，并在 patch mutation 下持久化原始 patch operations；`status_snapshot` 暴露 `plan_revision`，`progress.md` 与 `handoff.md` 会同步暴露 `Current Execution Step`、`Current Gate` 与 graph-aware `Execution Plan` section，而 criteria / completion truth 仍保持独立。额外要求：当远端 provider 在明显多步任务上直接返回 `run` / `resume` 时，runtime 也必须先落一个 system-sourced、one-criterion-at-a-time 的 bootstrap execution lane，而不是允许 task 在没有 durable checklist 的情况下直接执行。

### AT-002C Workspace Project Graph Surface

通过 `project update --project-file ...`、`project patch --patch-file ...`、`project get --json` 与 ACP `project.update` / `project.patch` / `project.get`，确认 runtime 会把 workspace-level orchestration graph 写进 `.ngen/project/project.json`，显式带出 `explanation`、stable project step id、`parent_step_id` / `depends_on` / `branch_id` / `task_id`、`revision`、`current_step_id`、ready/block step summary、active branch summary 与 `last_mutation_ref`；同时确认 `.ngen/project/project_updates.jsonl` 追加了对应 mutation record，显式区分 `mutation_kind=replace|patch`，并在 patch mutation 下持久化原始 project patch operations。额外要求：当 step 或 branch 绑定真实 `task_id` 时，runtime 还必须自动回写 branch-level `task_ref` / `status_ref` / `handoff_ref` / `workspace_root` / `last_reason_code`，而不是要求 operator/provider 自己去扫描 task tree 拼 project state。

### AT-003 Coding Verifier

对一个 Go workspace 运行 `coding` task，确认 `verification/latest.json` 记录 `go test ./...` 结果。
对一个带 repo-owned verifier entrypoint 的 workspace 运行 `coding` task，例如 success criterion 明确写 ``./build.sh test`` passes 且 repo 同时包含 immutable reference trees，确认 runtime 会优先执行这个 task-scoped verifier command，而不是盲跑默认 `go test ./...`。
对一个显式配置 `ngen.json.verification.coding_commands` 的 workspace 运行 `coding` task，例如顺序声明 `["./build.sh","test"]` 与 `["./build.sh","build"]`，确认 runtime 会按顺序记录多个 verifier checks，并且显式 verifier-command criterion 只会在对应 command 真正执行且通过后才闭合。

### AT-003B Coding Verifier Timeout

制造一个阻塞型 Go test，并把 `verification.coding_timeout_seconds` 设为短超时，确认 runtime 不会无限挂起；`verification/latest.json` 必须记录 timeout summary，任务收敛到 `Failed/failed_verification`。

### AT-003A Coding Repair Loop

对一个初始 `go test ./...` 失败的 `coding` workspace，在任一当前 provider mode（`builtin`、`command`、`openai-response`、`openai-comp`、`anthropic`）下执行 `auto` 或 `run`，确认 runtime 会先记录失败的 verifier 结果，再追加 `workspace_edits.jsonl`、生成 `workspace_edit_*` events、重跑 verifier，并在测试文件未被修改的前提下收敛到 `Done`。`command` provider 需返回同一条 decision / workspace observation / workspace edit JSON contract；`builtin` provider 走本地 deterministic repair engine。

### AT-004 Docs-Lite

对一个 `general_execution/docs_lite` task 运行 `run`，确认系统允许在无代码构建/测试的情况下完成，但仍要求 review 与 handoff。

### AT-005 Done Gate

制造缺 handoff 或 verifier 失败的情形，确认 `completion/latest.json` 记录 `rejected`，且任务不会进入 `Done`。

### AT-006 Review Block

制造 unsupported completion claim，确认 `reviews/latest.json` 变为 blocking，并追加 `findings.jsonl`。

### AT-007 Status Snapshot

`status --json` 必须输出单个稳定对象，至少包含 `task_id`、`phase`、`state`、`status_reason_code`、`status_detail_ref` 与关键 refs。

### AT-008 Watch Waiting

对 task 设置 watch，确认 task 进入 `Waiting/waiting_watch`，并且 `status_detail_ref` 回链到 watch artifact。

### AT-009 Scheduler Tick

制造一个到期 watch，执行 `scheduler tick --once`，确认 task 被唤醒并重新走 `resume` 路径。

### AT-010 Approval Lifecycle

创建 approval request，确认 task 进入 `Blocked/blocked_policy`；再执行 approve / deny，确认 `approvals.jsonl` 与 `state.json` 同步更新。

### AT-010B Parent-Owned Worker Approval Lifecycle

spawn worker child 后由 child 发起 approval request，确认 child `approvals.jsonl` 记录 `owner_task_id` / `owner_worker_id`；再通过 parent 执行 `approval ls --owned` 与 `approve` / `deny`，确认 owned history 可见且 decision 仍回写 child task。

### AT-010C Parent Provider Context Sees Owned Approval

child 发起 owned approval 后，对 parent 执行 `auto`、`session.prompt` 或读取 provider input，确认 manager context 直接带出 `owned_pending_approvals` 与 actionable worker metadata，而不是必须先单独查 `approval ls --owned`。

### AT-010A Input Request Lifecycle

创建 input request，确认 task 进入 `Blocked/blocked_missing_input`；再执行 respond，确认 `input_requests.jsonl` 追加 answered record 且 `state.json` 返回 `Active`。

### AT-011 Failed State

破坏 `state.json`，确认 runtime 生成 `diagnostics/*.json` 并收敛到 `Failed/failed_state`。

### AT-012 Events Tail

`events tail --json` 必须输出结构化 events，而不是自由文本日志。`events tail --after EVT-...` 只输出该 cursor 后的 events；ACP `task.events`、web JSON `?after=` 与 SSE `Last-Event-ID` / `?after=` 必须保持相同 cursor 语义；stale cursor 必须返回 explicit diagnostic，而不是静默输出最新 tail。

### AT-012A Replay Safety And Restore Clues

触发 observation command、workspace edit、repair command 与 worker reconcile edit，确认 `command_runs.jsonl` / `workspace_edits.jsonl` 写出 `replay_safety.side_effect_class` 与 `replay_policy`。read-only / idempotent command 才能标记 `safe_to_replay`；`manual_review_required` / `do_not_auto_replay` 的同 argv repair command 已有 side-effect record 时，runtime 必须拒绝再次自动执行。`status --json` 必须暴露 `last_checkpoint_ref` 与 `restore_clues`，handoff 的 Resume Instructions 必须渲染同类 restore clue。

### AT-013 Auto Loop

`auto --json` 必须至少写出 provider decision event，并把任务送入后续 runtime pass。

### AT-013A Auto Task Update

在 `auto_run_max_turns=1` 的远端 provider 场景下，让 provider 先返回 `task_update` 或 `task_patch`，再返回 `run` 或 `resume`，确认 mutable plan mutation 不会吞掉唯一 turn budget；同一次 auto pass 仍必须继续进入真实 runtime 执行。

### AT-013B Auto Project Update

在 `auto_run_max_turns=1` 的远端 provider 场景下，让 provider 先返回 `project_update` 或 `project_patch`，再返回 `run`、`resume`、`task_update` 或 `task_patch`，确认 workspace project graph mutation 不会吞掉同一次 auto pass 的后续执行机会；同一 auto pass 仍必须继续进入下一轮 provider decision 或真实 runtime 执行。

### AT-013C Auto Task Create

在 `auto_run_max_turns=1` 的远端 provider 场景下，让 provider 先返回 `task_create`，再返回 `run` 或 `resume`，确认 durable task materialization 不会吞掉同一次 auto pass 的后续执行机会；同一 auto pass 结束后，`task list --json` 必须能看到新 task。额外要求：task discovery 必须跳过尚未完成 core materialization 的 task root，至少不能在只有目录或只有 `task.json` 时就把它误当成可恢复 task，并把创建中的任务错误收敛到 `Failed/failed_state`。若 `task_create` 显式给出 `project_step_id` / `project_branch_id`，则 `project get --json` 必须把既有 step/branch 绑定到新 task，并移除该 task 的 synthetic auto-track step/branch，避免留下重复 graph 节点。

### AT-013D Harness Evaluation Ledger

对 `run`、`resume`、`auto` 与 `review` pass，确认 runtime 会写入 `.ngen/tasks/<task_id>/harness/latest.json` 并追加 `harness/history.jsonl`。`Done`、`Blocked` 与 `Failed` outcome 都必须有 harness evaluation record。`ngen harness eval TASK-ID --json` 必须返回最新 `object_kind=harness_evaluation` snapshot，至少包含 provider mode/model、system prompt ref、context/continuity/sprint/criteria refs、repair/observation/execution budgets、verifier/review/completion status、workspace edit status 汇总、worker/memory activity 与 evidence refs；该命令只能读取 artifact，不能重新运行 provider、verifier 或 review。

### AT-013E Mission Validation Vertical Slice

通过 `ngen mission create --json` 确认 `.ngen/missions/<mission_id>/mission.json`、`validation_contract.json`、`features.json`、`milestones.json` 与 `notes.md` 被写出，并且 mission 绑定真实 root task；`validation_contract.json.assertions` 必须持有稳定 `ASSERT-*` ids，feature/milestone `contract_coverage` 必须引用这些 assertion ids；`milestones.json` 必须带出 `current_feature_id` / `ready_feature_ids` / `blocked_feature_ids`；`mission.json.role_plan` 必须冻结 `orchestrator`、`workers`、`validators` 的 effective model/source/explicit，且 config 后续变化不会静默改写既有 mission snapshot。通过 `ngen mission run --json` 在未 approve 时确认写入 deterministic plan-gate blocking run，且不会进入 orchestrator provider call。通过 `ngen mission approve --json` 确认 coverage 完整时写入 `plan_approval_status=approved` 与匹配当前 contract 的 `plan_approved_contract_ref`，coverage 不完整时返回 blocking finding；批准引用不匹配当前 contract 时，后续 `mission run` / `mission validate` 必须阻塞。通过 `ngen mission validate --json` 在 root task 尚未 Done 时确认写入 blocking deterministic `validation_runs.jsonl`、evidence-backed findings、validator provenance 与 explicit user-testing skipped finding，且显式 validators model 也不会在 deterministic blocker 前被调用，且这类 deterministic precondition blocker 不生成伪 fix feature。构造 assertion 已有 feature/milestone coverage 但缺 root task、worker、verifier、review、completion 或 validation evidence ref 的场景，确认 `mission validate` 以 `missing_assertion_evidence` 阻塞 mission done，且不会生成伪 fix feature。通过批准后的 `ngen mission run --json` 确认 root task 先按 orchestrator role model 经过 bounded provider-decision orchestration，再经过既有 verifier/review/done gate，最后由 mission validator 写入 passed validation run，mission status 进入 `done`；若 provider 在 mission orchestration 中返回 `task_create`，必须注入 mission root lineage、绑定当前 mission feature，并在单次 pass 创建一个 child 后停止；没有可绑定 feature 时必须返回确定性诊断。通过 mission-owned worker continuation 确认 child task lineage 指向 mission root，child harness evaluation 使用 `workers` role model，同时 reviewer/security/coding/general role contract 不变。通过显式 `mission.role_models.validators` 确认 model validator 使用 dedicated read-only schema，能以 evidence-backed finding 阻塞 otherwise-passing mission，并把 model / semantic blocking finding 转成 fix-feature candidate、`mission_fix_scoped` event 和 root task follow-up plan step；继承到的 `provider.model` 不得自动启用 model validator。通过 `mission status --json` 确认 `mission_status_snapshot` 暴露 current feature、latest validation blocker count、unresolved fix features、recent mission events 与 `metrics.jsonl` 聚合，并且 provider 未返回 token/cache/cost 时为 `unknown`；`metrics.jsonl` 还必须写入 validator time 和 repair attempt count，task narrative 的 Mission section 必须显示紧凑 metrics 摘要。通过 provider decision input、`status --json`、`progress.md` / `context/summary.md` 与后续 handoff 确认 mission-bound task 暴露 mission contract、role plan、current milestone 与 latest validation refs。通过 terminal/TUI session prompt `/missions` 与 `/goal` 确认它们只打开或创建 mission artifacts，并且不会调用远端 provider 或打开 task/worker 管理控制台；`goal` 是 prompt shortcut / compact mission objective，不是所有 mission 子命令的完整别名。

### AT-014 ACP Session

通过 ACP `session.start` + `session.prompt("/run")`，确认 task 能被拉起并返回结构化 status。

### AT-014A TUI Open And Picker

通过 `ngen tui --inline TASK-ID` 与 `ngen tui --inline` 两条路径，确认：

- 指定 `TASK-ID` 时会创建 `mode=tui` session 并直接打开该 task；
- 不指定 `TASK-ID` 时会先进入 task picker；
- picker 里至少能看到 `task_id`、`title`、`kind`、`phase/state` 与 `updated_at`；
- picker / task list discovery 必须跳过尚未具备 core durable artifacts 的 task root，不能把半 materialized task 列出来后再误恢复成 `failed_state`；
- 打开后的 TUI header / inspector 与 `status --json` 语义一致。

### AT-014B TUI Prompt And Focused Controls

在 `ngen tui --inline TASK-ID` 中：

- 通过 composer 提交 `/run`，确认 task 能走完一轮 background turn 并刷新到新的 `status_snapshot`；
- 存在 pending approval 时，approval view 可以 approve / deny，并回落到现有 `approvals.jsonl` 与 `state.json`；
- 存在 pending input request 时，input view 可以 respond，并回落到现有 `input_requests.jsonl` 与 `state.json`；
- worker 进入 `continue_child` readiness 时，TUI 可以触发继续动作，并刷新到新的 `worker_snapshot` / worker contract truth。

### AT-019 ACP Capability And Errors

通过 ACP `initialize` / `rpc.ping` 与 invalid params 请求，确认 capability map 暴露 `task.create`、`session.snapshot`、`permission.request|list|decide`、`worker.spawn|list|sync`、`status_snapshot`、`session_snapshot`、`worker_snapshot` 与 `ngen.notification`，且 JSON-RPC 错误码稳定。

### AT-020 ACP Session Snapshot

通过 ACP `session.snapshot`，确认返回 bounded recent messages、session refs 与嵌套 `status_snapshot`，而不是只返回裸 `session` artifact。

### AT-021 ACP Input Request

通过 ACP `input.request` / `input.list` / `input.respond`，确认 structured input lifecycle 与 CLI 路径一致，并且不会被误归类到 `blocked_policy`。

### AT-022 ACP Worker Parity

通过 ACP `worker.spawn` / `worker.list` / `worker.sync`，确认 `worker_snapshot` 暴露 parent/child status，且 sync 后 handoff/status 与 parent worker contract 一致。

### AT-023 ACP Notifications

对 `session.prompt`、`input.request` / `input.respond` 与 `worker.spawn` / `worker.sync` / `worker.continue` 发送 ACP 请求，确认 server 在 response 后追加 `ngen.notification`，且 notification payload 使用稳定 `acp_notification` object。

### AT-024 ACP Approval Lifecycle

通过 ACP `permission.request` / `permission.list` / `permission.decide`，确认 approval history 与 CLI 路径一致，且批准后 task 会从 `Blocked/blocked_policy` 返回 `Active`。

### AT-024A ACP Parent-Owned Worker Approval

通过 ACP 让 worker child 发起 `permission.request`，再由 parent 执行 `permission.list(include_owned=true)` 与 `permission.decide`，确认 parent surface 能解析 owned child approval，`approval.updated` notification 可带 `worker_snapshot`，且 decision 仍回写 child approval history；批准后 `worker_snapshot` 应把 child 标成 `continue_child`。

### AT-015 Worker Contract

`worker spawn` / `worker sync` 必须能把 child task status 与 handoff 回链到 parent worker contract；当 child 被 owned approval 阻塞时，还必须暴露 `blocked_reason_code`、`approval_ref`、`requires_parent_action`、`parent_action_*`、`root_task_id`、`lineage_depth` 与 effective `subagent_policy`。

### AT-015A Worker Continue

parent 对已批准且回到 `Active` 的 child 执行 `worker continue`，确认 child 沿单一路径继续，`worker_snapshot` 最终回显恢复后的状态，并且不会跳过仍 pending 的 approval。

### AT-015B Worker Policy Matrix

通过 `subagents.role_policies` 与 nested `worker spawn`，确认 runtime 会拒绝 `allow_child_workers=false`、`allowed_worker_roles` 不匹配或 `lineage_depth >= max_lineage_depth` 的 child 派生；同时确认 role override 可以改变 `workspace_isolation`、`reconcile_mode` 与 `auto_release_on_success`。

### AT-015C Worker Criteria Evidence

创建一个 parent task，其 success criteria 显式要求 child runtime truth，例如 `reviewer child produces a compiled result`、`reviewer child review is clear`、`child reconcile applies`、`general child reconcile is recorded`、`reviewer child workspace remains prepared` 或 `general child workspace is released`。确认 parent 初次 `run` 会因 criteria 未满足而进入 `Blocked/blocked_review`；child 完成并 `worker sync` 后，parent 再次 `run` 会基于 `workers/*.json` 与 `worker_runtime/*.result|settlement|reconcile|workspace.json` 的 evidence 收敛到 `Done`，而不是用 generic verification 误闭。

### AT-015D Worker Evidence Score

完成一个 reviewer/security/coding/general child 后执行 `worker sync` 或 `worker continue`，确认 `workers/*.json` 与 `worker_runtime/*.result.json` 都写出 `evidence_score`、`evidence_grade`、`missing_evidence`、`verified`、`review_clear`、`handoff_present`、`criteria_closed`、`settlement_accepted`、`reconcile_clean`、`parent_action_unresolved`、`conflict_count` 与 `trusted_for_parent_completion`。accepted child 必须达到 `evidence_grade=complete` 且 `trusted_for_parent_completion=true`；低分 child 被 parent criteria 引用时，review 必须以 `worker_trust_gap` 阻塞 completion。

### AT-015E Reconcile Parent Takeover

构造 isolated child 与 parent workspace 同一路径漂移的 reconcile conflict，确认 `worker_runtime/*.reconcile.json` 写出 `status=conflict`、`parent_takeover_required=true`、`parent_takeover_summary` 与 `parent_takeover_refs`，并保留 child workspace 供 parent inspect / takeover；parent workspace 不得被部分覆盖。

### AT-016 Workspace Memory

任务进入 `Done` 后，`memory show` 必须能看到新的 completion summary。

### AT-016A Explicit Workspace Memory Promote

通过 CLI `memory promote TASK-ID --summary ... --kind ... --ref ...`、ACP `memory.promote`，以及远端 provider `memory_promote` decision，确认 runtime 会把新的 entry 追加到 `.ngen/memory/entries.jsonl`，显式带出 `kind`、`source`、redacted `summary`、normalized `refs`、`scope`、`paths`、`profiles`、`provider_modes`、`confidence`、`freshness_status` 与 `last_validated_ref`，并同步刷新 `memory/MEMORY.md`。额外要求：在 `auto_run_max_turns=1` 的远端 provider 场景下，若 provider 先返回 `memory_promote` 再返回 `run` / `resume`，memory mutation 不得吞掉同一次 auto pass 的后续执行机会。

### AT-016B Workspace Memory Freshness

通过 `memory promote TASK-ID --summary ... --kind note --ref workspace:README.md` 创建 path-scoped memory entry，确认 `entries.jsonl` 中 `paths=["README.md"]` 且 `memory show` 渲染 `[task_note/operator/fresh]`。删除或移动 `README.md` 后再次执行 `memory show` 或构建 provider input，确认同一 entry 被渲染为 `[task_note/operator/stale]`，且 task-local criteria / verification / review artifacts 仍优先于 workspace memory。

### AT-017 Extended Profiles

对 `security_review` 与 `reviewer` 运行 verifier，确认它们都走真实 profile-specific path，而不是 alias 到旧 profile 名字。

### AT-017A Evidence-First Reviewer Lane

对任意通过 verifier 的 task 运行 `review --json` 或 runtime done gate，确认 `reviews/latest.json` 写入 `reviewer_profile`、`review_context_refs`、`changed_paths`、`risk_summary` 与 `blocking_categories`。构造 parent criteria 引用 reviewer worker 但 child runtime evidence 未完成的场景，确认 parent completion 被 `worker_trust_gap` 阻塞，且 `findings.jsonl` 带 evidence refs。构造 scope drift / stale context 的 review unit case，确认 category 分别为 `scope_drift` 与 `stale_context_risk`，并保留 affected paths。

### AT-017B Quality Diagnostics

构造一个带 `Do not modify *_test.go files.` constraint 的 coding task，让 provider 返回修改 `calc_test.go` 的 workspace edit plan。确认 source test file 未被修改，`workspace_edits.jsonl` 记录 failed edit，`diagnostics/quality-latest.json` 写出 `status=blocking`、`block_completion=true`、`test_file_changes=["calc_test.go"]` 与 `confirmed_defect` finding，`quality-history.jsonl` 追加历史，并且 `progress.md` / `handoff.md` 在非 clear 状态下渲染 `Quality Diagnostics` section。

### AT-017C Role Contract Files

创建任意 task 后确认 `.ngen/roles/coding.json`、`general_execution.json`、`reviewer.json` 与 `security_review.json` 被水合为 valid `RoleContract`，且当前 task provider input 带出对应 `role_contract`。写入一个 `allowed_provider_actions=["noop"]` 的 coding role file 后执行 `auto`，确认 builtin 或远端 provider 选择 `run` / `task_update` 等越权 action 时 runtime 显式报错。写入一个只允许 `reviewer` child 的 coding role file 后执行 `worker spawn ... --role coding`，确认被 `allowed_worker_roles` 拒绝。

### AT-018 Yolo Permission

`permission_mode_id=yolo` 下触发 approval request，确认 approval records 仍被写出，但 task 不会停留在 `Blocked/blocked_policy`。

### AT-018B Benchmark Integrity Mode

在 `permission.default_mode=yolo` 且 `permission.benchmark_integrity_mode=true` 的 workspace 中触发 coding repair command policy，确认 `gofmt` 等本地 deterministic formatter 仍可执行，但 `curl`、shell wrapper、`go mod download`、Git remote command 与 path-based repo script 都被拒绝，并在 `command_runs.jsonl` / replay safety 中记录 `policy_decision=denied_benchmark_integrity`、`replay_policy=do_not_auto_replay` 与 network/open-world risk。

## 2. 当前不纳入范围

- deeper per-child sandboxing beyond current shared/snapshot/worktree workspace isolation
- richer role-file inheritance / role discovery UX，超出当前 `.ngen/roles/<role_id>.json` schema、内建水合、provider action gate 与 child-role gate
- deeper external-root policy、memory supersession/conflict adjudication、路径 ownership 变更判断，以及 reviewer 对 stale-memory 依赖的强制阻断
- longer-lived ACP push subscription / fanout；当前只承诺 `task.events` replay-after-cursor 与 per-request `ngen.notification`
- default browser / GUI / computer-use plane；当前只承诺 bounded command / repair command plane 的 policy gate、approval、durable result、replay-safety annotation 与 verifier/review closure
