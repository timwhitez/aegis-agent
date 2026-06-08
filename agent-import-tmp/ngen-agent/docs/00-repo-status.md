# 仓库状态与设计审计

> 状态: Active
> 最后更新: 2026-05-14
> 作用: 说明当前生效的实现边界与 post-foundation 收敛结果

## 1. 当前收敛结论

本轮重新核对 owner docs 与当前 Go 实现后，结论如下：

1. foundation slice 仍然是 kernel，但 post-foundation 路线已经不再只是 roadmap 文本。当前代码已经落下 provider-driven `auto` loop、ACP stdio server、per-request `ngen.notification`、interactive terminal、full-screen TUI、bounded worker contracts、ACP worker parity、legacy hooks + typed hook registry、normalized visibility filtering、redacted workspace memory、`security_review` / `reviewer` 与 `yolo` permission mode 的可运行实现，以及 `coding` task 在全部当前 provider mode（`builtin`、`command`、`openai-response`、`openai-comp`、`anthropic`）下的 bounded multi-attempt repair loop。这个 coding loop 现在不仅有 verifier timeout guard，还支持 bounded read-only observation commands、patch-first workspace edits、bounded workspace repair commands、以及 durable `command_runs.jsonl` / `workspace_edits.jsonl`；repair target 不只可以是 verifier failure，也可以是在 verifier 已通过后仍未满足的 workspace-backed success criteria。这里的 workspace-backed 不只覆盖显式 path / glob criterion，也覆盖带 readme/docs/config 语义和具体 token 的 criterion；failed/noop repair attempts 与 failed repair commands 都会留在同一次 bounded pass 内继续消耗预算，并把 prior failure summaries 显式注入后续 provider prompt；其中 `builtin` 是本地 deterministic repair engine，`command` 复用同一条 stdin JSON contract，远端 adapter 仍承担更强的模型驱动修复能力。manager loop 现在已经覆盖三条真实的 provider-driven orchestration path：当 workspace 需要一个 durable follow-up task 时，provider 可以直接下发 `task_create`，由 runtime materialize 一个新的 workspace task，并可按需绑定到既有 project step/branch；当 parent 需要委派 bounded 子任务时，provider 可以直接下发 `worker_spawn`，由 runtime 创建并启动 child 的首轮执行；当 provider input 暴露出 `managed_workers[].parent_action_type=continue_child` 时，provider 决策也可以直接下发 parent-side `worker_continue`，由 runtime 继续 child。与此同时，runtime 也已经把 child lifecycle 从“只有 worker contract 状态镜像”推进到五条显式 runtime artifacts：`worker_runtime/*.workspace.json` 持有 child workspace prepare/release truth，`worker_runtime/*.baseline.json` 持有 isolated child spawn baseline，`worker_runtime/*.settlement.json` 持有 child settlement truth，`worker_runtime/*.result.json` 持有 compiled child result truth，`worker_runtime/*.reconcile.json` 持有 isolated child side-effect reconcile truth。这里的 compiled result 现在不只服务 accepted child；它也会冻结 blocked approval / input detail 与 approved-but-awaiting-continue 的 `parent_action_*`，使 manager surfaces 能直接消费 child blocker truth 和下一步动作。TUI 这条 surface 现在的产品目标已经收敛为接近 Codex 的 chat-first 极简 coding-agent 体验：`ngen tui [TASK-ID]` 仍启动 `mode=tui` session 并周期性刷新 `.ngen/sessions/*.messages.jsonl`、`events.jsonl`、status/plan/criteria/worker snapshot、approval/input blocker 与 workspace memory，但默认启动不再要求 operator 先经过复杂 task picker、task list、worker manager、固定 inspector tabs 或确认弹层；`/run` 仍复用 `PromptSession(...)`，approval/input 决策仍回落现有 service 方法，而不是引入另一套 UI-only task truth。task/subtask/worker 生命周期应由 agent/runtime 根据 provider decisions、plan/project graph 与 worker artifacts 自主管理，TUI 只呈现 compact 状态和必要人工决策。`subagents.workspace_isolation=auto|shared_workspace|snapshot_copy|git_worktree` 与 `subagents.role_policies.<role>` 共同决定 child workspace prepare、reconcile mode、auto-release 与 nested delegation contract；`coding` / `general_execution` child 默认在 accepted settlement 且 parent 未在同一路径漂移时自动把 isolated 文件变更折回 parent workspace，`reviewer` / `security_review` child 默认保持 artifact-only，但这些默认值现在都可按 role override。child task 与 worker contract 也已经显式带出 `parent_task_id`、`parent_worker_id`、`root_task_id`、`lineage_depth` 与 `subagent_policy`，使 bounded grandchild delegation 由 durable policy 决定，而不是隐式 hardcode。worker role 现在也不再对未知值静默回退到 docs child，而是只接受 `coding`、`reviewer`、`security_review` 与 `general_execution` 这组显式 contract。
2. 当前 runtime 已经把 continuity surface 扩到 task-local、session-local、workspace-project 与 workspace-memory 四层：`progress.md`、`context/latest-pack.json`、`context/summary.md` 与新的 `continuity/latest.json` 现在一起持有 phase/state、最近 verifier/review/completion truth、最近 repair evidence、next step、workspace memory ref，以及结构化的 `current_focus` / `startup_checklist`。其中 `continuity/latest.json` 是 machine-readable restart ledger，`continuity/history.jsonl` 则把每次 narrative sync 追加成 append-only continuity history；provider decision / workspace observation / workspace edit prompt 都会真实消费它，而不是只消费 Markdown prose。与此同时，`criteria/latest.json` 已经从最小 met/open 快照升级成 task-local acceptance ledger，而 `sprint/latest.json` 又把当前 execution/gate pointers、primary criterion、deferred criteria、completion signals 与 working set 收口成更短视距的 current-scope contract；二者分别把“当前 feature boundary”与“这一轮只该完成什么、什么证据算完成”冻结为 durable truth，并通过 `criteria/history.jsonl` / `sprint/history.jsonl` 维持 append-only refresh history。最新这一轮又把 workspace-level orchestration 真相压进了 task-local narrative：`context/latest-pack.json.project_focus`、`continuity.current_focus.project_focus` 与 `sprint.project_focus` 现在会显式冻结当前 task 在 `.ngen/project/project.json` 里的 primary step / branch、bound step / branch ids、upstream dependencies、downstream steps、workspace ready/blocked pointers 与 project refs。这样长任务恢复、repair loop 与 review/handoff surfaces 不再只是 criteria-aware，也变成了 project-aware；远端 provider 不必再从整张 project graph 临时重建“我当前到底站在哪个项目节点上”。`plan.json` 也已经从“纯 runtime-synthesized bootstrap 占位”推进成双语义 task system：system lane 仍由 runtime 基于 baseline、每条 success criterion 与最终 review/done gate synthesize，mutable execution lane 则可由 `task update` / `task patch`、ACP `task.update` / `task.patch`、或 provider `task_update` / `task_patch` action 显式改写，用来表达大型项目中的长期拆解、排序与当前执行项，而不会篡改 criteria / review / completion truth。当前 mutable lane 已经是 graph-capable contract：step 可以持久化 stable id、`parent_step_id`、`depends_on` 与 `priority`，plan 还会显式带出 `revision`、`ready_execution_step_ids`、`blocked_execution_step_ids` 与 `last_mutation_ref`，并把每次显式改写追加到 `plan_updates.jsonl`。其中 `task_update` 继续承担全量 rewrite 角色，而 `task_patch` 则提供顺序 patch mutation surface，当前 patch op 冻结为 `set_explanation`、`upsert_step` 与 `remove_step`，并把原始 patch op 一起写入 mutation ledger。现在即使远端 provider 在明显多阶段任务上直接返回 `run` / `resume`，runtime 也会先按 open criteria 落一个 system-sourced、one-criterion-at-a-time 的 execution bootstrap，避免 task 缺少 durable checklist。`status_snapshot` 也会回显 current-step pointers 与 `plan_revision`。`progress.md` / `handoff.md` 除了 `Current Step` 外，还会显式带出 `Current Execution Step`、`Current Gate`、graph-aware `Execution Plan` section、`Current Sprint` section，以及新的 `Project Focus` section。session bridge 现在也不再只把 `last_prompt` 塞给 provider。provider decision input、workspace observation prompt 与 workspace edit prompt 都会显式带出 `session_messages_ref` 与 bounded `session_recent_messages`，而 `session.prompt` 允许在普通 conversational prompt 下先写 assistant-style direct reply、再持续写 runtime summary，`session.cancel` 仍会写 runtime cancellation summary，因此 session transcript truth 不再只有 `operator` / `runtime`，同时 failed task outcome 与 operator cancel 也会显式落盘。workspace 根下也新增了 singular `project/project.json`：它把跨 task / child-task 的 durable decomposition、dependency edges、concurrent branches 与 task binding 收口为 one-per-workspace orchestration graph；`project update` / `project patch`、ACP `project.update` / `project.patch`、以及 provider `project_update` / `project_patch` action 会把 mutation 追加到 `project_updates.jsonl`，并通过 branch `task_ref` / `status_ref` / `handoff_ref` / `workspace_root` 把 project graph 回链到真实 task artifacts。除了 graph mutation 本身，ACP `task.create` 与 provider `task_create` 现在也可以直接把一个新的 durable task materialize 成 `.ngen/tasks/<task_id>/...` 真相，并在显式提供 `project_step_id` / `project_branch_id` 时把该 task 原子绑定进既有 graph；任务创建路径也会在 auto-track 或显式绑定落盘后立即再刷一轮 narrative sync，保证首个 `context/continuity/sprint` snapshot 就已经带着最新 project focus，而不会先产出一份脱离 project graph 的旧上下文。与此同时，provider `task_create` 也新增了 runtime-side child-contract normalization：parent prompt 里的 `create exactly one durable task` / `bind the new task ...` 这类 orchestration 指令不会再泄漏进 child `task.json.constraints`，未明确 handoff / replace 当前 binding 的情况下也不会偷用父任务的 project step/branch。workspace memory 也不再只在 `Done` 后被动刷新：当前 operator 可通过 `memory promote`、ACP 可通过 `memory.promote`、provider 可通过 `memory_promote` action 把 milestone / decision / blocker summary 追加到 `.ngen/memory/entries.jsonl`，并把 compacted workspace memory 刷新回 `MEMORY.md`；每条 entry 现在显式冻结 `kind`、`source`、`refs`、`scope`、`paths`、`profiles`、`provider_modes`、`confidence`、`freshness_status` 与 `last_validated_ref`。`MEMORY.md` 由 `entries.jsonl` 刷新生成，recent entry label 会带出 freshness；path-scoped entry 在对应 workspace path 消失时会被渲染为 stale，并以同样标签进入 provider-visible `workspace_memory`，避免旧 path 记忆覆盖当前 task-local evidence。
3. 当前 repo truth 是“post-foundation integrated baseline”，不是单纯 `Foundation v0.1`，但也不应被误写成“所有 richer hardening / production polish 已完成”。
4. 当前关键要求仍然不变：新增 surface 不能反向拥有 task truth，所有 bridge 都必须回到 `.ngen/` artifacts、events 与 status snapshot。
5. 构建与验证边界现在也被显式收口：为了让根目录 `go test ./...` 与 `./build.sh test` / `./build.sh build` 这类 repo-owned verifier entrypoints 在长期共存 `codex/`、`opencode/` 参考树和 `real_task_tests/` 运行产物时仍然可预测，这三类目录已经作为 nested Go modules 隔离出根模块 package discovery；`build.sh` 也会默认使用 `/tmp/ngen-go-build` 作为 Go cache，避免环境相关 cache 噪声干扰稳定验证。与此同时，`coding` verifier 已从“单条 canonical command”升级为 ordered verifier sequence：最高优先级是显式 `ngen.json.verification.coding_commands`，其次是 legacy `ngen.json.verification.coding_go_test_command`，最后才是 task success criteria 推导出的 repo-owned verifier commands；runtime 会按顺序逐条执行这些 verifier checks，并把实际执行过的 command argv 连同 summary 一起持久化进 `verification/latest.json`。criteria/review lane 也不再只认一个泛化的“verification passed”，而是要求显式 verifier-command criterion 真正匹配到已执行且通过的 command evidence。现在 parent manager task 对 child runtime 也有同样的 artifact-backed closure：显式 worker criterion 必须命中 `workers/*.json` 与 `worker_runtime/*.result|settlement|reconcile|workspace.json` 这组 runtime truth，而不会再被 generic verification 误闭。

## 2. 当前 active 范围

当前代码承诺以下闭环：

- 单二进制 `ngen`
- `.ngen/` canonical runtime state
- `task create`、`task list`、`task get`、`task update`、`task patch`、`project get`、`project update`、`project patch`、`mission create`、`mission get/status/plan/approve/validate/run/pause/resume`、`run`、`resume`、`auto`、`status`、`review`、`events tail [--after EVENT-ID]`
- `handoff export`
- `watch set`、`watch ls`、`watch cancel`
- `scheduler tick --once`
- `approval request`、`approval ls [--owned]`、`approve`、`deny`
- `input request`、`input ls`、`input respond`
- `worker spawn`、`worker ls`、`worker sync`、`worker continue`
- `memory show`、`memory promote`
- `harness eval`
- `acp serve`
- `web serve`
- `terminal`
- `tui`
- `coding`
- `general_execution/docs_lite`
- `security_review`
- `reviewer`
- baseline、verification、review、criteria、completion、handoff、checkpoint、watch、approval 与 input-request artifacts
- `reviews/latest.json` 现在带 `reviewer_profile`、`review_context_refs`、`changed_paths`、`worker_result_refs`、`risk_summary` 与 `blocking_categories`；`findings.jsonl` 使用稳定 category vocabulary，并且 worker-backed criteria 缺 child result / settlement / reconcile truth 时会以 `worker_trust_gap` 阻塞 parent completion
- `diagnostics/quality-latest.json` 与 `diagnostics/quality-history.jsonl` 现在记录 task-local quality diagnostics：changed paths、test-file changes、failed/no-op edits、scope drift、large/dependency/abstraction warnings 与 blocking quality findings。review gate 会消费 blocking quality finding，因此违反“不要修改 tests”这类明确约束的 edit attempt 不会被吞成普通 repair failure。
- worker result / contract 当前带 artifact-completeness evidence scoring：`evidence_score`、`evidence_grade`、`missing_evidence`、`verified`、`review_clear`、`handoff_present`、`criteria_closed`、`settlement_accepted`、`reconcile_clean`、`parent_action_unresolved`、`conflict_count` 与 `trusted_for_parent_completion`。parent review 对 worker-backed criteria 会拒绝低分或未 trusted 的 child evidence。
- baseline / checkpoint 现在也带 repo bearings：`baseline.json.command_hints` 会暴露 repo-owned init / verifier entrypoint，`baseline.json.workspace_snapshot.git` 与 `checkpoints/*.json.workspace_snapshot.git` 会冻结 branch / head / dirty state / changed paths / recent commits
- `progress.md`、`context/latest-pack.json`、`context/summary.md` 与 `continuity/latest.json|history.jsonl` 形式的 task-local continuity / compaction truth
- `command_runs.jsonl` 与 `workspace_edits.jsonl` 形式的 coding durable observation / command / write truth
- Store 在拼接 task、mission、session、watch、worker、checkpoint、command output 与 diagnostic artifact 路径前校验每个 ID 是单一 artifact path segment；空值、`.` / `..`、路径分隔符、drive/UNC 语法、NUL、`.json` / `.jsonl` 后缀注入和非 `[A-Za-z0-9_-]` 字符都会被拒绝，directory listing 会跳过本地 stray invalid entries。
- config 中影响 runtime 文件位置的字段也在 load 阶段收口：`state_dir` 当前固定为 `.ngen`，`scheduler.lease_file` 与 `memory.file` 必须是 workspace-relative slash path，禁止绝对路径、workspace escape、Windows drive / backslash syntax 与 NUL；`memory.file` 会作为 workspace memory Markdown 的实际写入路径，而不是被静默忽略。
- workspace edit / patch apply / worker reconcile 在 hash、read、write、delete 前执行 workspace containment 与 no-symlink component guard；workspace snapshot 默认跳过 symlink，并通过 collection metadata 暴露 `omitted_file_count`、`truncated`、`stop_reason` 与最多 16 个 `omitted_paths`。bounded observation `rg` 拒绝 hidden/ignored/symlink traversal flags，`rg --files` 的 path operand 仍会走 workspace/deny 校验，且 broad search 不能覆盖非隐藏 deny roots；`ls` 拒绝 hidden listing flags 且不能列出非隐藏 deny roots；`find` 只接受 expression 前 path operands，并拒绝 `-H` / `-L` / `-follow`、外部 path predicate、未知 predicate，以及覆盖 `.ngen` / `.git` 等 denied root 的 broad search；`go` observation 不再允许 `test` / `build` / mutating env/list flags，这些必须走 verifier 或 repair command lane；`git` observation 拒绝 `--no-index`、external diff/textconv、output files、`rev:path` content reads、ignored listing，以及绕过 `--` 的 deny-path pathspec 或 broad content commands。
- repair command policy 现在有 opt-in benchmark integrity guard：`permission.benchmark_integrity_mode=true` 时，网络或 open-world repair command 在 `standard` / `yolo` 下都会被拒绝并记录 `policy_decision=denied_benchmark_integrity`，用于 Terminal-Bench / leaderboard evaluation 等需要防 reward-hacking 的运行。
- provider HTTP response body 以 4 MiB 上限 decode；command provider、observation/repair command 与 hooks stdout/stderr 以 1 MiB cap 捕获。命中 cap 的 command record 会写出 `stdout_truncated` / `stderr_truncated`，并在 summary 中说明输出被截断。
- `harness/latest.json` 与 `harness/history.jsonl` 形式的 task-local harness evaluation truth，覆盖最近一次 `run`、`resume`、`auto` 或 `review` pass 的 provider/context/repair/review/completion evidence refs
- `.ngen/missions/<mission_id>/` 形式的 workspace-level mission truth：`mission.json`、`validation_contract.json`、`features.json`、`milestones.json`、`validation_runs.jsonl`、`metrics.jsonl` 与 `notes.md`。当前 active subset 已包含三角色收敛 slice：`validation_contract.json.assertions` 持有稳定 `ASSERT-*` assertion ledger；`features.json` / `milestones.json` 通过 assertion ids 表达 `contract_coverage`；`milestones.json` 还派生 `current_feature_id`、`ready_feature_ids` 与 `blocked_feature_ids`，用于冻结 serial feature execution pointer；`mission.json.role_plan` 冻结 `orchestrator`、`workers`、`validators` 的 effective model/source/explicit；`mission.json.plan_approval_status` 默认为 `pending`，`mission approve` 必须先校验 assertion coverage 并写入匹配当前 contract 的 `plan_approved_contract_ref`，否则 `mission run` 只写 deterministic plan-gate blocking run，不进入 orchestrator。批准后，`mission run` 先按 orchestrator role model 执行一轮 bounded provider-decision orchestration，再进入 validation；mission-scope `task_create` 现在只允许 materialize 当前 mission feature 为 lineage-bound child task，并在单次 mission orchestration pass 中创建一个后立即停止，避免静默 fan-out；mission-owned child continuation 通过 root lineage 使用 `workers` role model；validator 先消费 mission plan gate 与 root task artifacts 执行 deterministic gate，只有 plan 已批准、批准引用匹配当前 contract、coverage 完整、每个 assertion 都有 root/worker/verifier/review/completion/validation evidence ref、显式 validators model 且 deterministic pass 通过时才调用 dedicated read-only model validator。未配置 browser/GUI/computer-use tool plane 时，user-testing validator 以非阻塞 skipped finding 显式记录。绑定 mission 的 task 会把 mission refs 纳入 provider decision input、`status_snapshot`、`progress.md` / `context/summary.md` 与后续 handoff；`mission status --json` 通过 `mission_status_snapshot` 暴露 current feature、latest validation、unresolved fix features、recent mission events 与 metrics 摘要，task narrative 也显示 mission metrics 摘要。
- `.ngen/roles/<role_id>.json` 形式的 role contract truth：当前 runtime 会水合 `coding`、`general_execution`、`reviewer` 与 `security_review` 的内建 contract，并在 provider decision 进入 runtime action 前按 `allowed_provider_actions` / `allowed_worker_roles` 拦截越权 action。
- session、worker、workspace memory artifacts，且 memory entry 带 scope / path / profile / provider mode / confidence / freshness metadata
- `.ngen/sessions/*.messages.jsonl` 形式的 durable session transcript truth，以及 provider decision / repair prompt 上的 `session_messages_ref` / bounded `session_recent_messages`
- `session_snapshot` / `worker_snapshot` derived objects over ACP session and manager-child state
- parent-owned worker approvals injected into parent provider/session context plus worker control fields persisted on `workers/*.json`
- `worker_runtime/*.workspace.json`、`*.baseline.json`、`*.settlement.json`、`*.result.json` 与 `*.reconcile.json` 形式的 child workspace lifecycle / baseline / settlement / compiled result / reconcile truth
- per-request ACP `ngen.notification` for mutating session / task / approval / input / worker calls
- provider-driven `auto` dispatch，支持 builtin/command、OpenAI-compatible Chat Completions、OpenAI Responses 与 Anthropic Messages provider
- provider-driven `auto` dispatch 现在也支持 `task_create`、`worker_spawn` 与 parent-side `worker_continue`：`task_create` 用于 materialize durable workspace task，并可按需绑定进既有 project graph；`worker_spawn` 用于 manager task 创建并启动 bounded child；`worker_continue` 用于继续已获批准且回到可继续状态的 child
- hooks、visibility deny filtering 与 additional roots
- `yolo`
- human-readable CLI、full-screen TUI、HTTP JSON/SSE web backend 与 `--json` headless output；event surfaces 现在支持 append-only `events.jsonl` cursor replay，不引入单独 event database；`ngen web serve` 对非 loopback listen address 在 token 为空时默认拒绝，除非设置 token 或显式 `--allow-unauthenticated`
- replay / resume truth 现在包括 `command_runs.jsonl` / `workspace_edits.jsonl` 上的 `replay_safety` side-effect annotation、ACP `task.events` replay-after-cursor、`status --json.restore_clues`，以及 handoff Resume Instructions 中的 checkpoint restore clue
- `build.sh fmt` 在 git repo 中只枚举 tracked `cmd/**/*.go` / `internal/**/*.go`；非 git fallback 会 prune `.ngen`、`bin` 与 ignored run-artifact 目录，避免格式化 live-run evidence 或生成产物

## 3. 当前生效的 artifacts 与 contracts

当前 active contract 覆盖以下 runtime truth：

- `.ngen/tasks/<task_id>/task.json`
- `.ngen/tasks/<task_id>/plan.json`
- `.ngen/tasks/<task_id>/plan_updates.jsonl`
- `.ngen/project/project.json`
- `.ngen/project/project_updates.jsonl`
- `.ngen/missions/<mission_id>/mission.json`
- `.ngen/missions/<mission_id>/validation_contract.json`
- `.ngen/missions/<mission_id>/features.json`
- `.ngen/missions/<mission_id>/milestones.json`
- `.ngen/missions/<mission_id>/validation_runs.jsonl`
- `.ngen/missions/<mission_id>/notes.md`
- `.ngen/roles/coding.json`
- `.ngen/roles/general_execution.json`
- `.ngen/roles/reviewer.json`
- `.ngen/roles/security_review.json`
- `.ngen/tasks/<task_id>/state.json`
- `.ngen/tasks/<task_id>/baseline.json`
- `.ngen/tasks/<task_id>/progress.md`
- `.ngen/tasks/<task_id>/continuity/latest.json`
- `.ngen/tasks/<task_id>/continuity/history.jsonl`
- `.ngen/tasks/<task_id>/handoff.md`
- `.ngen/tasks/<task_id>/events.jsonl`
- `.ngen/tasks/<task_id>/findings.jsonl`
- `.ngen/tasks/<task_id>/approvals.jsonl`
- `.ngen/tasks/<task_id>/input_requests.jsonl`
- `.ngen/tasks/<task_id>/command_runs.jsonl`
- `.ngen/tasks/<task_id>/commands/*/stdout.txt`
- `.ngen/tasks/<task_id>/commands/*/stderr.txt`
- `.ngen/tasks/<task_id>/workspace_edits.jsonl`
- `.ngen/tasks/<task_id>/criteria/latest.json`
- `.ngen/tasks/<task_id>/criteria/history.jsonl`
- `.ngen/tasks/<task_id>/completion/latest.json`
- `.ngen/tasks/<task_id>/verification/latest.json`
- `.ngen/tasks/<task_id>/reviews/latest.json`
- `.ngen/tasks/<task_id>/harness/latest.json`
- `.ngen/tasks/<task_id>/harness/history.jsonl`
- `.ngen/tasks/<task_id>/context/latest-pack.json`
- `.ngen/tasks/<task_id>/context/summary.md`
- `.ngen/tasks/<task_id>/diagnostics/*.json`
- `.ngen/tasks/<task_id>/checkpoints/*.json`
- `.ngen/tasks/<task_id>/workers/*.json`
- `.ngen/tasks/<task_id>/worker_runtime/*.workspace.json`
- `.ngen/tasks/<task_id>/worker_runtime/*.baseline.json`
- `.ngen/tasks/<task_id>/worker_runtime/*.settlement.json`
- `.ngen/tasks/<task_id>/worker_runtime/*.result.json`
- `.ngen/tasks/<task_id>/worker_runtime/*.reconcile.json`
- `.ngen/watches/*.json`
- `.ngen/sessions/*.json`
- `.ngen/sessions/*.messages.jsonl`
- `.ngen/memory/MEMORY.md`
- `.ngen/memory/entries.jsonl`

## 4. 当前状态码边界

当前 runtime 仍冻结以下 `status_reason_code`：

- `blocked_missing_input`
  - 仅用于缺少人类补充的信息、路径、环境值或业务决策。
- `blocked_policy`
  - 仅用于 approval pending、approval denied，或当前动作需要额外批准。
- `blocked_review`
  - verifier 已运行，但 review / criteria / handoff / done gate 仍阻塞完成。
- `waiting_watch`
  - 任务已进入 waiting，真相由 watch artifact 持有。
- `failed_verification`
  - 当前 profile verifier 未通过。
- `failed_state`
  - durable state 不可安全恢复。
- `aborted_user`
  - operator 主动中止。

补充边界：

- 审批不属于 `blocked_missing_input`。
- review blocker 也不属于 `blocked_missing_input`。
- `blocked_missing_input` 的 `status_detail_ref` 必须回链到 `input_requests.jsonl#input_record_id=...`。
- `blocked_policy` 的 `status_detail_ref` 必须回链到 `approvals.jsonl#approval_record_id=...`。
- worker child 发起 approval 时，durable truth 仍写在 child `approvals.jsonl`；parent surface 只通过 owner fields + worker contract 派生 `approval ls --owned` / parent-scoped decide 视图。
- parent task 的 provider / session context 现在也必须看到 owned child pending approvals，避免 manager loop 只能靠外部先查 permission list。
- `worker sync` / `worker_snapshot` / `workers/*.json` 现在必须暴露 child blocker、approval ref、以及 `requires_parent_action` / `parent_action_*` 这类 control metadata。
- `worker sync` 现在也必须把 child workspace mode / status、child settlement status / summary、child reconcile mode / status / summary，以及 `worker_runtime/*.workspace.json` / `*.settlement.json` / `*.reconcile.json` 的 refs 持久化回 worker contract 与 parent-visible snapshot。
- `worker spawn` 现在只接受 `coding`、`reviewer`、`security_review` 与 `general_execution` 这组显式 worker role；未知 role 必须报错，而不是静默退化成 docs child。
- `worker spawn` 现在还必须尊重 parent task `subagent_policy`：`allow_child_workers=false` 时直接拒绝，`allowed_worker_roles` 不命中时直接拒绝，`lineage_depth >= max_lineage_depth` 时直接拒绝；child task / worker contract 必须把新的 lineage 与 effective policy 一起持久化。
- `worker continue` 是当前唯一的 parent-side child continuation helper；批准后 child 的恢复路径不应再靠 operator 猜测 child task id。
- `blocked_review` 的 `status_detail_ref` 必须回链到 `reviews/latest.json`。
- `waiting_watch` 的 `status_detail_ref` 必须回链到 `workspace:.ngen/watches/<watch_id>.json`。

## 5. 当前仍未完成的 richer hardening

以下内容仍保留为 future hardening / richer design，不应与当前已生效实现混写：

- broader provider/network matrix，超出 builtin/command、OpenAI-compatible Chat Completions、OpenAI Responses 与 Anthropic Messages 当前范围
- broader multi-step coding orchestration，超出当前全部 provider mode 上最多 3 次 repair cycles、少量 read-only observation commands、少量 bounded workspace repair commands、以及 patch-first workspace edit 的 bounded coding path；当前 active scope 已对显式 path / glob 与 semantic readme/docs/config criteria 做 criteria-aware repair，也已经允许 argv-based shell/package-install/generator/formatter/dependency-sync repair command，但 dedicated browser computer-use plane、更强 approval/sandbox policy 与更长链路分解仍未进入当前切片
- broader tool/browser/computer-use product plane 仍是 explicit future scope；当前只把已有 command/repair plane 收敛为带 `permission_mode_id`、`policy_decision`、`replay_safety`、durable command result、approval、verifier 与 review gate 的 bounded safe subset
- longer-lived ACP push subscription / fanout，超出当前 per-request `ngen.notification` 与 ACP `task.events` replay-after-cursor
- richer role-file inheritance / role discovery UX / hook input-output schemas，超出当前 `.ngen/roles/<role_id>.json` additive contract 与 provider action gate
- broader replay / reconcile / child settle protocol，超出当前 side-effect annotations、checkpoint restore clues、`worker_runtime` baseline、file-level reconcile 与 parent-takeover records
- deeper visibility / memory governance，尤其是更强的 external-root policy、supersession/conflict adjudication、路径 ownership 变更判断，以及 reviewer 对 stale-memory 依赖的强制阻断
- deeper `security_review` / `reviewer` semantics，超出当前 deterministic artifact-scored review、worker trust gap、scope/stale-context categories 与 multi-check verifier baseline
