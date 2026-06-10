# Workspace、provider 与 coordination

## 1. 当前 workspace 模型

当前实现冻结以下 workspace 判断：

- 一个 mutable workspace 只允许一个 write owner
- runtime 直接在当前 workspace 下创建 `.ngen/`
- watch/scheduler 的协调信息是 workspace-level，但任务真相仍在 task artifacts
- workspace 现在也有一个 singular project coordination artifact：`.ngen/project/project.json` / `project_updates.jsonl`。它表达跨 task / child-task 的 orchestration graph、dependency edges 与 concurrent branches，但不反向拥有任何单 task verifier/review/completion truth
- workspace 现在也有 mission coordination artifact：`.ngen/missions/<mission_id>/...`。mission 位于 project/task/worker 之上，负责 validation contract、feature/milestone coverage 与 validation runs，但 root task 的 verifier/review/completion truth 仍由 task artifacts 拥有
- task-local narrative 现在也会把这层 workspace project truth 压成 `project_focus`：一旦 task 已绑定 project graph，`context/latest-pack.json`、`continuity/latest.json` 与 `sprint/latest.json` 都会显式持久化当前 primary step / branch、bound step / branch ids、upstream dependencies、downstream steps 与 project refs
- `coding` task 若执行模型驱动写入，也只能在同一 workspace owner 下完成，并把写入结果回写 task artifacts
- manager-controlled child 当前已经有一个 bounded writable workspace 子集：可按 `subagents.workspace_isolation` 或 `subagents.role_policies.<role>.workspace_isolation` 选择 `shared_workspace`、`snapshot_copy` 或 `git_worktree`；workspace lifecycle truth 仍写在 parent task 的 `worker_runtime/*.workspace.json`，isolated child spawn baseline 写在 `worker_runtime/*.baseline.json`

## 2. 当前 coordination 模型

当前 runtime 实现以下 coordination：

- input coordination
  - 通过 `input_requests.jsonl` 与 `Blocked/blocked_missing_input`
- approval coordination
  - 通过 `approvals.jsonl` 与 `Blocked/blocked_policy`
- watch coordination
  - 通过 `.ngen/watches/*.json` 与 `scheduler tick --once`
- session coordination
  - 通过 `.ngen/sessions/*.json` 与 `*.messages.jsonl`
  - provider decision input、workspace observation prompt 与 workspace edit prompt 都会显式带出 `session_messages_ref` 与 bounded `session_recent_messages`，让 terminal / ACP / TUI 多轮 steer 不会退化成只剩 `session.last_prompt`
  - TUI 使用 `mode=tui` session，但 session artifact contract 与 terminal / ACP 相同；它只是在 UI 层用 background turn runner 包住同步 `PromptSession(...)`，周期性刷新 status / events / session messages / blockers / worker state，而不是引入另一套 task truth
- worker coordination
  - 通过 `tasks/<task_id>/workers/*.json`
  - provider-driven manager control 当前允许 `worker_spawn` / `worker_continue`
  - child workspace lifecycle、spawn baseline、child settlement、compiled child result 与 isolated side-effect reconcile 通过 `tasks/<task_id>/worker_runtime/*.workspace.json` / `*.baseline.json` / `*.settlement.json` / `*.result.json` / `*.reconcile.json`
  - `worker_runtime/*.result.json` 不只持有 completion / review / verification outcome；当 child 被 approval / input 阻塞，或 approval 已通过但 parent 仍需 `worker_continue` 时，它也必须冻结 blocker detail、approval/input refs 与 `parent_action_*`
  - parent criteria/review lane 现在也可以直接消费这组 worker runtime artifacts：显式 child-exists / compiled-result / review-clear / verification-passed / settlement / reconcile / workspace-status / `continue_child` criteria 必须命中 `workers/*.json` 与对应 `worker_runtime/*.json` evidence，而不是退化成 generic verification
- project coordination
  - 通过 `.ngen/project/project.json` 与 `.ngen/project/project_updates.jsonl`
  - project graph 允许 durable project steps 与 branch objects 绑定到真实 `task_id`
  - branch runtime summary 会自动冻结 `task_ref` / `status_ref` / `handoff_ref` / `workspace_root`，避免 project orchestration 退化成 chat-only state
  - ACP `task.create` 与 provider `task_create` 现在也允许直接 materialize durable workspace task；若调用方显式给出 `project_step_id` / `project_branch_id`，runtime 会把新 task 绑定进既有 graph，并移除该 task 的 synthetic auto-track step/branch，避免重复 project 节点
  - provider / CLI / ACP 当前都允许 `project_update` / `project_patch` 风格的显式 graph mutation；project patch 除了 full `upsert_step` / `upsert_branch` 外，还支持更细粒度的 edge ops，例如 `set_step_dependencies`、`set_step_parent`、`bind_step_branch`、`bind_step_task`、`bind_branch_task` 与 `set_branch_status`
- mission coordination
  - 通过 `.ngen/missions/<mission_id>/mission.json`、`validation_contract.json`、`features.json`、`milestones.json`、`validation_runs.jsonl` 与 `notes.md`
  - `mission create` 创建或绑定 root task，生成 `validation_contract.json.assertions` 的稳定 `ASSERT-*` ledger，并把 `orchestrator` / `workers` / `validators` 的 effective model/source/explicit 冻结到 `mission.json.role_plan`
  - `mission approve` 先校验每条 assertion 都被 feature 与 milestone 覆盖，成功后在 `mission.json` 写入 `plan_approval_status=approved` 与匹配当前 contract 的 `plan_approved_contract_ref`
  - `mission run` 复用 root task truth，但只有 deterministic plan gate 通过后，才会按 `role_plan.orchestrator` 执行一轮 bounded provider-decision orchestration，再进入 validation；mission-scope `task_create` 只能创建 root-lineage child task，并绑定回当前 pending/in-progress mission feature
  - `mission validate` 先读取 mission plan gate 与 root task artifacts 执行 deterministic gate；只有 deterministic pass 通过且 `role_plan.validators.explicit=true` 时，才调用 dedicated read-only model validator 并把 validator provenance 写入 `validation_runs.jsonl`
  - `/missions` 是 session compact command，只打开或创建当前 task 的 mission state，不引入 UI-only mission truth
- coding observation coordination
  - 通过 `tasks/<task_id>/command_runs.jsonl` 与 `tasks/<task_id>/commands/*`
- coding workspace edit coordination
  - 通过 `tasks/<task_id>/workspace_edits.jsonl`
- workspace memory coordination
  - 通过 `.ngen/memory/`
  - operator 可通过 `memory promote`、ACP 可通过 `memory.promote`、provider 可通过 `memory_promote` action 追加稳定 memory entry；runtime 会同步刷新 `MEMORY.md`

## 3. 当前 scheduler 边界

当前 scheduler 仍只冻结：

- `ngen scheduler tick --once`
- workspace lease 路径默认 `.ngen/runtime/scheduler.lock`
- scheduler 只扫描 `.ngen/watches/*.json`
- due item 唤醒后仍复用同一 runtime `resume` 路径

`scheduler run` 长驻模式当前不是 active contract。

## 4. provider 边界

NGEN 仍保持 provider-neutral。

当前冻结两条边界：

- provider integration 不能反向拥有 task truth
- provider metadata 持久化必须最小、去敏
- provider prompt / tool description 只能强化现有 schema 的填写语义，不能引入绕过 runtime action / observation / edit contract 的隐式流程

当前补充一条 active 边界：

- `coding` task 的自治写路径当前已经在全部 current provider mode 上实现为同一套 bounded coding loop；runtime 当前最多允许 3 次 repair cycles，并且在每次真正写文件前最多运行少量 bounded read-only observation commands，在真正写文件前后还可以执行少量 bounded workspace repair commands。repair target 不只可以是 verifier failure，也可以是 workspace-backed criteria gap，其中既包括显式 path / glob criterion，也包括带 readme/docs/config 语义和具体 token 的 criterion。observation command 仍按 read-only executable allowlist 收口，并拒绝 workspace 外路径、repo-root override、明显的 exec-style escape、verifier/build/mutating Go commands、以及 Git `--no-index` / external diff / output file / `rev:path` / deny-path pathspec 绕过。Multica/headless `exec` 的 stdin reader 是薄适配器：它只把 user text blocks 拼接为 prompt，不解析 `metadata`、`system_prompt`、AGENTS.md 或 `.agent_context`，也不从 squad/project UUID 推断 issue id 或合成 quick-create/issue-execution task。若 prompt 本身要求 Multica issue 操作，后续 runtime 仍通过普通 provider/model 决策和 command policy 处理；read-only `multica issue get|list|runs` 与 `multica issue comment list` 可作为 observation/verifier command 读取 live issue context，mutating `multica issue create`、`multica issue comment add` 与 issue-scoped `multica squad delegate` 只能走 permission-gated repair command lane。Multica marker/comment/squad scheduling criteria 不能被“提到命令/marker”的 prose 满足：public marker、comment-add 与 worker/validator scheduling requirements 必须回链到 `command_runs.jsonl`、`commands/<id>/stdout.txt`、或 issue run/comment observation evidence。`standard` mode 需要 approval，`yolo` mode 可自动执行允许的 mutation command，但仍必须记录 command policy 与 replay-safety。若已识别出 Multica issue id 与 live issue marker，而远端 workspace edit provider 返回 empty output text，runtime 可以写出显式 fallback event，并生成最小 `multica-result.md` + `multica issue comment add --content-file ... --output json` repair plan；该 fallback 不直接绕过 command policy 或 replay-safety artifact。workspace repair command 则是更宽的 argv-based action plane，用于 formatter、generator、dependency sync、package install、migration 或 shell-backed repair，并以 budget、timeout、cwd=`workspace root`、durable artifacts 与 verifier/review gate 收口。workspace edit 仍以 patch 为优先路径；`builtin` 通过本地 deterministic repair engine 生成 observation/edit/command plan，`command` 通过 stdin JSON contract 生成 plan，远端 adapter 通过模型生成 plan。durable truth 仍写在 `command_runs.jsonl`、`workspace_edits.jsonl`、events 与最终 verifier/review artifacts
- provider prompt / tool description 当前已显式把三条远端模型职责分开：decision 只选择 exactly one runtime action 且不得假装执行；workspace observation 只返回最少 read-only argv inspection commands，足够时返回 zero commands；workspace edit 只返回 bounded repair plan，优先 root-cause patch-first 小改动，缺少上下文时不猜测文件内容。
- session/provider control plane 当前也有一个 active 子集：provider decision prompt、workspace observation prompt 与 workspace edit prompt 都不会只看到 `session.last_prompt`，而会看到 `session_messages_ref` 与 bounded `session_recent_messages`。`session.prompt` 允许在普通 conversational prompt 下先落 assistant-style direct reply，再继续落 runtime summary；`session.cancel` 仍必须追加 runtime cancellation summary。failed task outcome 与 operator cancel 这类控制事实也必须显式写进 `*.messages.jsonl`，使下一轮 provider decision / repair loop 可以直接消费同一份 session transcript truth
- repo-bearing control plane 现在也有一个 active 子集：baseline 与 checkpoint 不再只是“是否 capture 过 baseline”的薄事实；`baseline.json` 现在会暴露 repo-owned `command_hints` 与 `workspace_snapshot.git`，而 `checkpoints/*.json` 也会冻结最新 `workspace_snapshot.git`。provider decision / repair surfaces 必须把这组 repo bearings 带进 prompt，让远端 model 在长任务恢复时优先复用 repo 自己公开的 init / verifier entrypoint 与 git bearings，而不是重新猜测
- continuity control plane 现在也有一个 active 子集：runtime 每次 narrative sync 都会刷新 `continuity/latest.json` 并向 `continuity/history.jsonl` 追加一条 record。provider decision / workspace observation / workspace edit surfaces 必须把这组 continuity artifact 带进 prompt，让远端 model 在 fresh context 下直接接管 `current_focus` 与 `startup_checklist`，而不是只靠 `progress.md` prose 或 recent events 自己重建 sprint contract
- continuity control plane 的补充约束是：若 task 已绑定 workspace project graph，`continuity.current_focus.project_focus` 与 `startup_checklist` 必须一起暴露，让 fresh context 首先读到当前 project binding / dependency boundary，而不是先从全量 project graph 里自己搜索任务位置
- sprint control plane 现在也有一个 active 子集：runtime 每次 narrative sync 都会刷新 `sprint/latest.json` 并向 `sprint/history.jsonl` 追加一条 record。它不替代 continuity 或 criteria，而是把当前 execution/gate pointers、primary criterion、deferred criteria、completion signals、working set 与 task-scoped `project_focus` 收成更短视距的 current-scope contract；provider decision / workspace observation / workspace edit surfaces 必须显式消费它，避免 fresh context 又把多个 open criteria 一起展开，或误漂到 sibling project branch / downstream step。
- role-contract control plane 当前也有一个 active 子集：runtime 会在 `.ngen/roles/<role_id>.json` 水合内建 role files，并把当前 task 的 `role_contract` 注入 provider input。provider 可以看到 role description、allowed provider actions、allowed worker roles、workspace/reconcile/permission policy hints、context sections 与 review/verification/output expectations；runtime 仍以 artifacts 为准，并会在 provider decision 进入 action dispatch 前拒绝 role contract 未允许的 action 或 child role。
- 当 `session.prompt` 在同一轮 provider auto pass 内触发 `task_create` 时，runtime 也必须把这条 human-steered materialization 当作本轮 prompt 的 bounded mutation 边界：单次 prompt 在首个 durable task 创建后就返回，不继续静默 fan-out 第二个 `task_create`
- workspace memory control plane 当前也有一个 active 子集：provider decision contract 允许 `memory_promote`。它用于把 reusable milestone / decision / blocker summary 提升为 workspace-level durable memory；entry 追加到 `.ngen/memory/entries.jsonl`，并带出稳定 `kind` / `source` / `refs`，而 compacted workspace memory markdown 会同步刷新到 `.ngen/memory/MEMORY.md`
- workspace task-materialization control plane 当前也有一个 active 子集：provider decision contract 允许 `task_create`，ACP JSON-RPC 允许 `task.create`。`task_create` / `task.create` 会直接生成新的 durable workspace task artifacts，并可选地把该 task 绑定进既有 project step/branch；这条路径与 `worker_spawn` 不同，它不会把新 task 收成 parent-managed child contract，也不会继承 worker lifecycle/reconcile 语义。
- `task_create` 还有一条 runtime normalization guard：provider 输出的 child contract 必须是“child 创建后立刻可运行”的执行契约，而不是把 parent prompt 里的 orchestration 语义原样复制进去。runtime 会剥离 `create exactly one durable task`、`bind the new task to project step ...` 这类 parent-only constraint，并在 operator 没有明确表达 handoff / replace 当前 binding 时，阻止 provider 复用父任务当前 `project_step_id` / `project_branch_id`
- manager-worker control plane 当前也有一个 active 子集：provider decision contract 允许 `worker_spawn` 与 `worker_continue`。`worker_spawn` 只接受显式支持的 child role，并在共享 workspace、snapshot copy 或 git worktree 上创建 bounded child task 后立刻启动 child 的首轮执行，同时为 isolated child 捕获 baseline；`worker_continue` 用于已存在 child 的继续路径，而 `worker sync` 会把 child settlement、compiled result、workspace lifecycle 与 isolated side-effect reconcile truth 收口到 `worker_runtime/*.json`。这里的 compiled result 现在不仅覆盖 accepted child，也覆盖 blocked / approved-but-awaiting-continue child，使 manager surfaces 可以直接消费 child blocker truth 与 next action。当前 `subagents.role_policies`、`.ngen/roles/<role_id>.json` 与 child `task.json.subagent_policy` 会共同冻结 provider action gate、child role gate、`permission_mode_id` 继承、`workspace_isolation`、`reconcile_mode`、`auto_release_on_success`、`allow_child_workers`、`allowed_worker_roles`、`max_workers_per_task` 与 `max_lineage_depth`；因此 bounded grandchild delegation 现在属于 active contract，但更强的 per-child sandbox、role-file inheritance 与更自由的多层级 planning 仍是 future hardening。与此同时，provider decision contract 也新增 `project_update` / `project_patch`，允许 parent task 在同一 auto loop 内直接修正 workspace-level project graph，而不是把跨 task orchestration 隐藏在任务聊天历史里

## 5. control surfaces 路线

当前 active surfaces：

- CLI
- headless JSON output
- ACP stdio server
- interactive terminal line editor
- full-screen TUI
