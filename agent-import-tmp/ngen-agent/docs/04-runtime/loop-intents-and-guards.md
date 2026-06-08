# 运行时主循环、intent 与守卫

## 5. 单轮 loop contract

每次 loop 必须完成以下动作：

1. 读取最新 durable task state，
2. 分配新的 `loop_id`，
3. 在风险边界前写 checkpoint，
4. 构建 context pack，
5. 请求模型产出下一 intent，并为本轮响应分配 `turn_id` / `intent_id`，
6. 通过 policy 检查决定执行、拒绝或要求审批，
7. 持久化 events、evidence、decisions、reports、criteria 或 completion 结果，
8. 更新 phase / state，
9. 判断继续、等待、阻塞、失败或完成。

补充规则：

- 任何会产生外部副作用的动作都必须在执行前持久化其 stable runtime ID。
- `tool_call` 至少需要 `tool_call_id`；`watch` 唤醒至少需要 `wake_id`。
- baseline capture 与 checkpoint 现在也必须顺带冻结 repo bearings：repo-owned command hints 以及最新 git branch / head / dirty snapshot 都属于 long-running restore clue，而不是临时终端上下文。
- 当前真正落代码的最小 slice，是 `coding` task 在全部 current provider mode 下共享的 bounded coding path：runtime 先为只读 observation command 写 `observation_command_*` events 与 `command_runs.jsonl`，再为真正文件变更写 `workspace_edit_started` event 与 `workspace_edits.jsonl`。
- runtime 在 review/done gate 前还会计算 `diagnostics/quality-latest.json` 并追加 `diagnostics/quality-history.jsonl`。该 lane 从 workspace edits、changed paths、sprint working set 与 repair failure history 产生 concrete quality signals；若 task constraints 明确禁止 test mutation 而 edit plan 针对 `*_test.go`，或 changed paths 越出当前 sprint scope，quality finding 会进入 review 并阻塞 completion。
- 当前 context pack 不再只是薄摘要；runtime 会同步刷新 `progress.md`、`context/latest-pack.json`、`context/summary.md`、`continuity/latest.json|history.jsonl` 与 `sprint/latest.json|history.jsonl`。其中 `continuity/latest.json` 把 current sprint/focus 与 startup checklist 冻结成 machine-readable restart ledger，`sprint/latest.json` 则把当前 execution/gate pointers、primary criterion、deferred criteria 与 completion signals 收成更短视距的 current-scope contract；provider decision / repair prompt 都会消费这组 artifact。与此同时，`criteria/latest.json|history.jsonl` 也必须被视作 acceptance ledger：它不只保存 met/open truth，还要稳定暴露 `current_criterion_id`、`passes` 与 append-only refresh history，防止长任务恢复时重新猜当前 feature boundary。
- manager loop 当前也有一个 active 控制子集：provider 现在既可以直接选择 `task_create` 来 materialize 一个新的 durable workspace task，也可以选择 `worker_spawn` 来委派一个受限 child role 并启动 child 的首轮执行，还可以在 parent provider input 中的 `managed_workers` 暴露 `parent_action_type=continue_child` 时直接选择 `worker_continue`，由 runtime 代 parent 执行 child continuation，而不是只把 continuation metadata 留给人工命令。`task_create` 走 workspace-level task/project surface，不进入 child workspace lifecycle；worker continuation 之后，runtime 还会通过 `worker sync` 把 child settlement/reconcile truth 与 child workspace lifecycle truth 收口到 `worker_runtime/*.settlement.json` / `*.workspace.json`，并在 accepted settlement 下默认释放隔离 workspace。
- mission loop 当前有一个 active 三角色 slice：`mission create` 先写 validation contract assertion ledger、feature/milestone records 与 `mission.json.role_plan`；`mission approve` 是执行前 plan gate，只校验 assertions 是否被 feature/milestone 覆盖，并写入匹配当前 contract 的 approved contract ref；`mission run` 只有在 plan gate 通过后，才用 `orchestrator` role model 执行一轮 bounded provider-decision orchestration，可应用已有 `task_update`、`project_update`、`worker_spawn`、`worker_continue`、`task_create`、`run`、`resume`、`review` 等 action，再执行 `mission validate`。其中 mission-scope `task_create` 必须 materialize 当前 mission feature 为 root-lineage child task，并在创建一个 child 后停止本轮 orchestration，不能静默创建多个 durable tasks。`mission validate` 仍是独立 validator lane，先跑 plan/artifact deterministic validator；artifact closure 不只要求 root task Done、criteria closed 与 completion accepted，还要求每个 assertion 有 root/worker/verifier/review/completion/validation evidence ref；只有 deterministic pass 通过且 `validators.explicit=true` 时，才运行 read-only model validator；未配置 user-testing tool plane 时写入 non-blocking skipped finding。
- `loop_id` 属于每次 durable runtime pass；即使某次 pass 只是在做 reconcile / settle 而没有新的模型输出，也可以拥有新的 `loop_id`。
- `turn_id` / `intent_id` 只在该 pass 真的包含一次新的模型响应与 intent 解析时分配；runtime-owned reconcile pass 不得为了补齐字段而伪造新的 turn/intent。
- 只有当工具被声明为可安全重放，或已有 terminal result artifact 证明上次执行未完成最终副作用时，runtime 才能自动 retry。
- 对“可能已执行但结果未持久化”的非幂等动作，runtime 必须进入对账、review 或人工升级，而不是盲目重试。

## 6. intent 模型

NGEN v0.1 约束模型输出为少数几类 intent：

- `plan_update`
- `tool_call`
- `verify`
- `review`
- `watch`
- `aside`
- `done`
- `block`

当前实现说明：

- general intent parser / generic tool runner 仍未完全展开成 richer multi-tool plane，
- 但 `coding` task 已经拥有一个 active 子集：verification 失败后，runtime 可以先请求当前 provider bridge 返回少量 bounded read-only observation commands，再请求包含 patch-first workspace edit 与 bounded workspace repair command 的 repair plan；当 verification 已通过但 workspace-backed criteria 仍未满足时，也可以在同一预算内继续请求 bounded repair。当前该子集已覆盖 `builtin`、`command`、`openai-response`、`openai-comp` 与 `anthropic`；workspace-backed 既包括显式 path / glob criterion，也包括带 readme/docs/config 语义和具体 token 的 criterion。
- workspace-orchestration 当前也有一个 active 子集：provider decision contract 允许 `task_create`。它用于把新的 durable workspace task 直接 materialize 成 `.ngen/tasks/<task_id>/...` truth，并在显式提供 `project_step_id` / `project_branch_id` 时把该 task 绑定进既有 project graph；这条路径与 `worker_spawn` 明确分离，不会把新 task 压成 manager-owned child contract。runtime 现在还会对 provider `task_create` 做 child-contract normalization：移除 `create exactly one durable task`、`bind the new task ...` 这类 parent-side orchestration instruction，并在 operator 没有明确 handoff / replace 当前 binding 时阻止复用父任务当前 project step/branch。若 provider 还没有显式 execution lane 却直接返回 `run` / `resume`，runtime 当前会先按 open criteria 写一个 system-sourced、one-criterion-at-a-time 的 bootstrap ladder，而不是一次性把全部 criteria 视作并行执行项。
- manager-worker 当前也有一个 active 子集：provider decision contract 允许 `worker_spawn` 与 `worker_continue` 这两个明确 action。`worker_spawn` 只接受受支持的 child role 与显式 objective，并由 runtime 走 `SpawnWorker` + 首轮 `ContinueWorker`；`worker_continue` 则只在已有 child 回到可继续状态时生效。worker runtime 当前还会显式持久化 child settlement/workspace lifecycle，并把 `parent_task_id`、`parent_worker_id`、`root_task_id`、`lineage_depth` 与 `subagent_policy` 收口到 child task / worker contract；因此 bounded grandchild delegation 现在已经进入 active contract，但 unbounded long-horizon planning 仍是 future hardening。
- mission 当前不作为 provider decision action 暴露；operator 通过 CLI 或 `/missions` compact command 建立 mission state。mission runtime 只是在 `RunMission` 内复用既有 provider decision action dispatcher，并用 scoped role model 做路由；它不新增 `orchestrator`、`workers`、`validators` 这类 general worker role。后续若加入 model-facing mission action，也必须先证明 existing `project_update`、`task_create`、`worker_spawn` 与 `review` 不能表达该 loop。

解释规则：

- 任何不能解析为允许 intent 的响应都必须记录为 parse failure，
- 高风险 intent 需要进入 policy / approval 流程，
- `done` 只能是提议，最终由 runtime 判定。

## 7. 伪代码

```text
load task, plan, state
while state not in {Done, Failed, Aborted}:
  loop_id = allocate_loop_id()
  checkpoint_if_needed(task)
  pack = build_context_pack(task)
  response = provider.generate(pack)
  turn_id = allocate_turn_id(loop_id)
  intent = parse_intent(response)
  intent.id = allocate_intent_id(turn_id)

  if parse_failed:
    persist_event(parse_failure)
    maybe_retry_or_block()
    continue

  decision = policy_check(intent)
  if decision.requires_approval:
    persist_approval_request(decision)
    state = Blocked
    continue
  if decision.denied:
    persist_event(policy_denied)
    state = Blocked or Failed
    continue

  switch intent.kind:
    case mission_validate:
      read mission contract and root task artifacts
      if deterministic artifacts block:
        append deterministic validation run
        update mission status to blocked_validation
        stop before model validator
      if validators role model is explicit:
        call read-only mission validator schema
        convert findings into validation run and fix-feature candidates
      append validation run
      update mission status

    case plan_update:
      save_plan(intent.plan)
      phase = Plan
      state = Active

    case tool_call:
      tool_call_id = allocate_tool_call_id(intent.id)
      persist_tool_start(tool_call_id)
      result = tool_runner.run(intent.call)
      persist_tool_result(result)
      phase = Execute
      state = Active

    case verify:
      report = verifier.run(task)
      persist_verification(report)
      phase = Verify
      state = Active
      if report.failed:
        maybe_run_bounded_workspace_edit(task)
        if repair_budget_remaining:
          phase = Execute
        else:
          state = Blocked or Failed

    case review:
      review_report = reviewer.review(task)
      persist_review(review_report)
      phase = Review
      state = Active

    case watch:
      wake_id = allocate_wake_id(intent.id)
      save_watch(intent.watch)
      persist_watch_registration(wake_id)
      state = Waiting

    case aside:
      note = run_aside(intent.payload)
      persist_ephemeral_or_promote(note)
      state = Active

    case done:
      write_handoff_draft(task)
      gate = evaluate_done_gate(task)
      persist_completion_report(gate)
      if gate.passed:
        state = Done
      else:
        state = Active
        phase = Verify or Review

    case block:
      persist_block_reason(intent.reason)
      state = Blocked
```

## 8. done gate

`Done` 只能在以下条件全部满足时通过：

1. success criteria 有明确 evidence refs，
2. 必需 verifier stages 已通过，或存在显式人类 override，
3. 没有未处理的 critical review findings，或存在显式 waiver，
4. `handoff.md` 当前版本已写入并与 verifier/review truth 对齐，
5. 当前 state 没有未决 approval request 或 write conflict。

如果 gate 未通过，runtime 必须：

- 拒绝进入 `Done`，
- 把 gate verdict 写入结构化 completion artifact，
- 记录失败原因，
- 将任务送回 `Verify` 或 `Review` 或 `Blocked`。

## 9. repair loop

verification 失败后，系统不会立刻宣布任务失败。

默认 repair policy：

- 每个 step 默认最多 3 次 repair cycles，
- 每次 repair 都必须引用失败的 verifier report，
- 当前 active coding slice 每次 `run` / `resume` 默认最多做 3 次 bounded repair；每次 repair 允许少量 read-only observation commands，之后再做 patch-first workspace edit、少量 bounded workspace repair commands，以及 re-verify；repair target 既可以是 verifier failure，也可以是 workspace-backed criteria gap；如果某次 workspace edit 以 `failed` / `noop` 结束，或某次 repair command 以 `failed` 结束，runtime 仍会在同一预算内继续下一次 attempt，并把 prior failure summaries 带入后续 provider prompt；更宽的 long-horizon decomposition 与 dedicated browser plane 仍是 future hardening，
- 如果同一 verifier 输出连续两次完全相同，且 plan 没有实质变化，则必须升级而不是盲目重试，
- runtime 必须把违反 task constraints 的 edit plan 记成 failed workspace edit，而不是让模型偷偷改测试或 task artifacts，
- repeated failure 会计入 loop guards。

## 10. loop guards

NGEN 通过 guards 防止“看似工作、其实空转”：

- `max_silent_cycles`: 多轮没有 meaningful artifact update，
- `max_same_tool_failures`: 同类工具错误反复出现，
- `max_same_verify_failures`: 同一步骤验证反复失败，
- `max_watch_renewals_without_new_evidence`: watch 一直续期但无新证据，
- `max_model_only_cycles`: 多轮只有 reasoning 没有 action / promotion。

所有 guard 值必须进入 config 或 state artifacts，可见且可调。
