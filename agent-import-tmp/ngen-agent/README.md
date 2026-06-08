# NGEN Agent

> Status: Post-Foundation Integrated Baseline
> Last Updated: 2026-05-14
> Target: Go 1.24.2+, single binary, local-first, artifact-first task runtime

## Overview

`ngen-agent/` 是 NGEN 的 spec-first 仓库，也是当前最小可运行内核的实现。

它的设计中心保持不变：

> intelligence for work is loop + artifacts + verification + explicit control

当前实现的核心判断：

- task truth 永远落在 workspace 内的 `.ngen/` artifacts，而不是聊天历史。
- runtime 通过显式 `phase/state`、verifier、review 和 done gate 驱动任务收敛。
- provider、ACP、terminal、TUI、worker、memory 都是 bridge，不反向拥有任务真相。
- 当前是 post-foundation integrated baseline，不是最终生产态。

## Current Scope

当前 active scope 包括：

- 单二进制 `ngen`
- `.ngen/` canonical runtime state
- `task create|list|get|update`、`project get|update|patch`、`mission create|get|status|plan|approve|validate|run|pause|resume`、`run`、`resume`、`auto`、`status`、`review`、`events tail [--after EVENT-ID]`
- `handoff export`
- `watch set|ls|cancel`、`scheduler tick --once`
- `approval request`、`approval ls [--owned]`、`approve`、`deny`
- `input request|ls|respond`
- `worker spawn|ls|sync|continue`
- `memory show|promote`
- `harness eval`
- `acp serve`
- `web serve`
- `terminal`
- `tui`
- `coding`、`general_execution/docs_lite`、`security_review`、`reviewer`
- full-screen TUI operator surface：当前默认收敛为接近 Codex 的 chat-first 极简 coding-agent 入口。`ngen tui [TASK-ID]` 直接进入 composer + transcript + compact status；无 task id 时优先恢复最近 active coding task，否则创建轻量 `TUI Session` task，并在第一条真实 prompt 后回写 title/objective/criteria 与 `task_refined` event。默认 TUI 不暴露 task picker、task console、worker manager、task 删除/清空或固定 inspector；这些 raw 管理动作属于 CLI / ACP / Web backend 或显式 debug/compat surface。
- local HTTP web backend：`ngen web serve [--listen 127.0.0.1:8765] [--token-env NGEN_WEB_TOKEN] [--allow-unauthenticated]` 提供 health、task、events、task event SSE stream、session start/read/prompt/cancel 的 management API。events JSON 与 SSE stream 支持 `after=<event_id>` / `Last-Event-ID` cursor replay；它只包装现有 runtime service，不引入 web-only task truth；除 `/healthz` 外，token env 非空时要求 bearer token；非 loopback listen address 在 token 为空时默认拒绝，只有设置 token 或显式 `--allow-unauthenticated` 才能启动。
- `coding` task 在 `builtin`、`command`、`openai-response`、`openai-comp` 与 `anthropic` provider 下的 bounded multi-attempt repair loop，支持 bounded read-only observation commands、patch-first workspace edits、以及 bounded workspace repair commands；repair target 不只可以是 verifier failure，也可以是在 verifier 已通过后仍未满足的 workspace-backed success criteria，包括显式 path / glob criteria，以及带 readme/docs/config 语义和具体 token 的 criterion；同一次 `auto` / `run` / `resume` 内，failed/noop repair attempt 也会消耗预算并把 failure summary 带入后续 provider prompt，而不是要求额外人工 `resume`；workspace write/delete/patch 与 worker reconcile 会拒绝 symlink 组件和 workspace 逃逸，observation `find` 也会拒绝 symlink traversal option、外部 path predicate 与 expression 后的 path operand。
- `coding` verifier 现在支持 ordered verifier sequence：默认仍以 `go test ./...` 为基线；如果 `ngen.json.verification.coding_commands` 显式声明了 repo-owned verifier command sequence，例如 `["./build.sh","test"]` 后接 `["./build.sh","build"]`，runtime 会按该顺序逐条执行；若未显式配置 sequence，则会从 task success criteria 中提取 repo-owned verifier commands，例如 ``./build.sh test`` passes 与 ``./build.sh build`` passes；显式 `ngen.json.verification.coding_go_test_command` 仍作为 legacy single-command override，高于 criterion-derived 自动选择
- `baseline.json` 与 `checkpoints/*.json` 现在也会冻结 repo bearings：除了 repo truth refs 与 verifier availability，baseline 还会带出 repo-owned `command_hints` 与 `workspace_snapshot.git`；checkpoint 也会记录最新 `workspace_snapshot.git`，把 branch / head / dirty state / changed paths / recent commits 收口成 durable restore clue，而不是要求长任务只靠聊天回忆当前仓库状态
- task-local `progress.md`、`context/latest-pack.json`、`context/summary.md` 与 `continuity/latest.json` 组成的 durable context OS：runtime 会把当前 phase/state、最新 verifier/review/completion truth、最近 repair evidence、下一步动作、current sprint/focus、startup checklist、以及 task-scoped `project_focus` 一起压成 continuity pack，并把它注入 provider decision / repair prompt；`continuity/history.jsonl` 还会把每次 narrative sync 追加成 append-only restart ledger
- `sprint/latest.json` 现在也成为 first-class runtime artifact：它会把当前 execution/gate pointers、primary criterion、deferred criteria、completion signals、working set、以及当前 task 在 workspace project graph 里的 bound step / branch / dependency boundary 收成 durable current-scope contract，并把历史追加到 `sprint/history.jsonl`。这让 fresh context、repair loop 与 review/handoff surfaces 不只知道“任务整体是什么”，还知道“这一轮只该完成什么、什么证据算完成、哪些 sibling branch / downstream step 先不要碰”。
- `criteria/latest.json` 现在也不只是最小 met/open 快照，而是 first-class acceptance ledger：每条 criterion 会稳定冻结 `statement`、`ordinal`、`passes`、`selected`、最近摘要、最近评估时间与 evidence refs；snapshot 还会显式暴露 `current_criterion_id` / `current_criterion_statement`、met/open counts 与 `summary`。`criteria/history.jsonl` 会把每次 criteria refresh 追加成 append-only ledger，避免长任务恢复时重新猜“下一个 feature boundary 是哪条”。
- `task.review` / ACP `task.review` / provider `review` 现在也会在已有 verification truth 时主动重建 `handoff.md`，再做 review/completion gate；因此 handoff drift 不会把任务卡死在“缺 handoff 所以无法恢复 handoff”的死结里，但若 verification 或 criteria truth 仍未闭合，review 依然会明确收敛到 `Blocked/blocked_review`
- `reviews/latest.json` 现在是 evidence-first report：它写出 `reviewer_profile`、`review_context_refs`、`changed_paths`、`worker_result_refs`、`risk_summary` 与 `blocking_categories`，并把缺 evidence、scope drift、stale context 与 worker trust gap 记录进 `findings.jsonl`；parent criteria 引用 child work 时，review 不会只信 `workers/*.json` 或 child prose，必须看到 result / settlement / reconcile truth。
- `diagnostics/quality-latest.json` 现在是 task-local anti-corruption snapshot：runtime 会在 review/done gate 前汇总 changed paths、test-file edit attempts、failed/no-op repairs、scope drift 与 large/dependency/abstraction warnings；blocking quality findings 会进入 review，因此明确禁止修改 tests 的任务不会把 rejected test-file edit attempt 藏在普通 repair log 里。
- `plan.json` 现在是双语义 task system：system lane 仍由 runtime 基于 baseline / success criteria / final review/done gate synthesize；mutable execution lane 则可通过 `task update`、`task patch`、ACP `task.update` / `task.patch` 或 provider `task_update` / `task_patch` action 进行显式改写，用来表达大型项目的长期执行清单，同时仍不反向拥有 criteria / review / completion truth。mutable lane 现在支持 stable step ids、`parent_step_id`、`depends_on`、`priority`、`revision`、`ready_execution_step_ids`、`blocked_execution_step_ids` 与 `last_mutation_ref`，并把 operator/provider 改写历史追加到 `plan_updates.jsonl`。全量 rewrite 仍然可用；更小粒度的 patch mutation 则允许只改 explanation、upsert 某个 execution step 或删除 obsolete step，而不用重发整张图。若远端 provider 在明显多步任务上直接选择 `run` / `resume` 而 execution lane 仍为空，runtime 也会先基于 open criteria 落一个 system-sourced、one-criterion-at-a-time 的 bootstrap execution ladder，再进入真实执行。
- workspace 级 `project/project.json` 现在是 first-class orchestration graph：它不替代 task truth，而是把跨 task / child-task 的 durable decomposition、dependency edges、concurrent branches、以及 task binding 收口成 singular workspace artifact。`project update` / `project patch`、ACP `project.update` / `project.patch`、以及 provider `project_update` / `project_patch` action 都会把显式 project mutation 追加到 `.ngen/project/project_updates.jsonl`。project graph 支持 stable project step ids、`parent_step_id`、`depends_on`、`branch_id`、`task_id`、`ready_step_ids`、`blocked_step_ids`、`active_branch_ids` 与 `last_mutation_ref`，并额外冻结 branch-level `task_ref` / `status_ref` / `handoff_ref` / `workspace_root`，让大型项目的 task orchestration 不必退化成隐式聊天记忆。当前 project patch 还提供更细粒度的 edge ops：`set_step_dependencies`、`set_step_parent`、`bind_step_branch`、`bind_step_task`、`bind_branch_task` 与 `set_branch_status`。除了显式 graph mutation，ACP `task.create` 与 provider `task_create` 现在也可以直接 materialize durable workspace task；若调用方显式提供 `project_step_id` / `project_branch_id`，runtime 会把新 task 绑定进既有 project graph，并移除对应的 synthetic auto-track step/branch，避免留下重复节点。
- workspace 级 mission lane 现在提供三角色收敛 slice：`ngen mission <prompt>` / `ngen goal <prompt>` 或 `ngen mission create` 写入 `.ngen/missions/<mission_id>/mission.json`、`validation_contract.json`、`features.json`、`milestones.json`、`validation_runs.jsonl` 与 `notes.md`，并创建或绑定 root task；`validation_contract.json.assertions` 会给每条验收断言分配稳定 `ASSERT-*` id，`features.json` / `milestones.json` 的 `contract_coverage` 默认引用这些 assertion ids，而不是复制自然语言文本。`mission.json.role_plan` 会冻结 `orchestrator`、`workers`、`validators` 的有效 model、来源与显式性；新 mission 默认处于 `draft` + `plan_approval_status=pending`，必须经 `ngen mission approve` 校验 coverage 并记录匹配当前 contract 的 `plan_approved_contract_ref` 后，`mission run` 才会进入 orchestrator 执行。`mission run` 先用 orchestrator role model 走一轮 bounded provider-decision orchestration，可应用既有 `task_update`、`project_update`、`worker_spawn`、`worker_continue`、mission-scoped `task_create`、`run`、`resume`、`review` 等 action，然后执行独立 validation pass；mission-scoped `task_create` 只能 materialize 当前 mission feature 为 lineage-bound child task，并在单次 pass 创建一个 child 后停止，避免静默 fan-out。mission-owned child task 通过 root lineage 识别，并在 continuation 时使用 `workers` role model，但仍保留原 `coding` / `reviewer` / `security_review` / `general_execution` role contract。`mission validate` 必须先跑 deterministic artifact validator；只有 plan 已批准、批准引用匹配当前 contract、assertion coverage 完整、root task `Done`、criteria closed、completion accepted，且 `role_plan.validators.explicit=true` 时，才会运行 read-only model validator。绑定 mission 的 task 会在 provider decision input、`status --json`、`progress.md` / `context/summary.md` 与后续 handoff 中暴露 mission contract、current milestone、role plan 与 latest validation refs。terminal/TUI 中的 `/mission <prompt>`、`/missions <prompt>`、`/goal <prompt>` 与 `/goals <prompt>` 直接设置当前 task 的 mission/goal；不带 prompt 时只打开或创建当前 task 的 mission state，不暴露默认 task/worker 管理控制台。
- `status_snapshot`、`session_snapshot`、`worker_snapshot`
- session bridge 现在也有了 durable short-horizon continuity contract：provider decision input、workspace observation prompt 与 workspace edit prompt 都会显式带出 `session_messages_ref` 与 bounded `session_recent_messages`；`session.prompt` 除了持续追加 runtime summary，也允许在普通 conversational prompt 下写入 assistant-style direct reply 到 `.ngen/sessions/*.messages.jsonl`，而 `session.cancel` 继续追加 runtime cancellation summary，因此短时 steer transcript 不再只有 `operator` / `runtime`
- explicit operator slash commands 现在也有 provider-drift guard：`/run`、`/resume`、`/review`、`/worker_spawn ...`、`/worker_continue ...` 与 `/memory ...` 会先经 runtime/provider 公共解析层规范化，再决定是否进入远端 decision driver，而不是把这类明确指令完全交给模型猜
- provider decision / workspace observation / workspace edit prompt 现在也不只看到 task-local context；它们会显式消费 baseline 里的 repo bearings、`continuity.current_focus` / `continuity.startup_checklist`、`context_pack.project_focus`，以及新的 `sprint` artifact。当前 task 一旦已经绑定进 workspace project graph，远端 model 就能直接看到它所在的 primary step / branch、upstream dependency、downstream step 与 workspace-level ready/blocked pointers，而不是自己从整张 `project.json` 重新猜测
- `anthropic` provider 请求现在会在不改变 prompt 文本语义、工具 schema 或 runtime 行为的前提下使用 Anthropic text content blocks 设置 prompt-cache breakpoints：system/tool 稳定前缀、provider prompt 的稳定 instruction prelude、低波动 artifact JSON prefix，以及当前动态 artifact JSON tail 都有显式 `cache_control`，从而让重复 decision / observation / edit / validation 调用可以复用更长的稳定 prefix。
- `anthropic` provider 现在还会解析 response usage 中的 prompt-cache telemetry，并把 sanitized `token_usage` / `prompt_cache_usage` 追加到 task-local `provider_usage.jsonl`；harness snapshots、workspace edit records、model-backed mission validation runs 与 mission metrics 会引用这些摘要。provider 未返回 usage 时写 `unknown`，不会把未知值记成 `0` 或保存 raw provider payload。
- role files 现在进入 active contract：runtime 会在 `.ngen/roles/<role_id>.json` 水合 `coding`、`general_execution`、`reviewer` 与 `security_review` 的内建 `RoleContract`，provider input 会带出当前 task 的 `role_contract`，并在执行 provider decision 前按 `allowed_provider_actions` 与 `allowed_worker_roles` 做硬拦截；无效 role file 会显式报错，不会退回隐藏默认值。
- workspace memory 现在也有了显式 mutation surface 和轻量 freshness governance：除了 `Done` 后的自动 promote，operator 可以通过 `memory promote`、ACP `memory.promote`，provider 也可以通过 `memory_promote` action 把 milestone / decision / blocker summary 追加到 `.ngen/memory/entries.jsonl`；entry 会冻结 stable `entry_id`、`kind`、`source`、`refs`、`scope`、`paths`、`profiles`、`provider_modes`、`confidence`、`freshness_status` 与 `last_validated_ref`，并把最新 compacted workspace memory 刷新回带 freshness label 的 `MEMORY.md`。path-scoped memory 在对应 workspace path 消失时会在 `memory show` 和 provider-visible `workspace_memory` 中标记为 stale，而不是继续伪装成 fresh task truth。
- task-local harness evaluation 现在会在 `run`、`resume`、`auto` 与 `review` pass 后写入 `.ngen/tasks/<task_id>/harness/latest.json`，并追加 `harness/history.jsonl`；该 snapshot 记录 provider mode/model、prompt ref、context/continuity/sprint/criteria refs、repair budgets、verifier/review/completion status、workspace edit status 汇总、worker/memory activity、latest provider usage summary 与 evidence refs，用于对比 harness strategy 变化而不依赖临时终端日志
- mutating ACP calls 之后的 per-request `ngen.notification`
- parent-owned worker approvals injected into parent provider/session context plus actionable worker control metadata
- provider decision contract can now select `task_create` to materialize a new durable workspace task、`task_update` / `task_patch` to refresh mutable execution plan、`project_update` / `project_patch` to mutate the workspace project graph、`memory_promote` to persist a durable workspace-memory note、`worker_spawn` for bounded child delegation、以及 parent-side `worker_continue` when a managed child is ready to resume after approval。runtime 现在还会对 provider 发出的 `task_create` 加一层 child-contract normalization：显式剥离 `create exactly one durable task` / `bind the new task ...` 这类 parent-side orchestration constraint，并在未收到明确 handoff/replace 操作意图时拒绝偷用父任务当前 project binding，避免 child 再次把自己当成 orchestrator 或直接撞上 step/branch 已绑定错误
- supported worker roles are `coding`、`reviewer`、`security_review`、`general_execution`；unsupported worker roles now fail explicitly instead of silently degrading to `docs_lite`
- worker child 现在支持 bounded workspace isolation 与 replay-safe reconcile：runtime 会按 `subagents.workspace_isolation=auto|shared_workspace|snapshot_copy|git_worktree` 或 `subagents.role_policies.<role>.workspace_isolation` 选择共享 workspace、隔离 snapshot copy 或 git worktree，并把 workspace lifecycle、spawn baseline、child settlement、以及 isolated child side-effect reconcile truth 持久化到 `worker_runtime/*.workspace.json` / `*.baseline.json` / `*.settlement.json` / `*.reconcile.json`；`coding` 与 `general_execution` child 默认是 `apply_on_accept`，`reviewer` / `security_review` 默认是 `artifact_only`，但现在都可以通过 role policy override 显式改写；冲突 child 会保留隔离 workspace 供 parent 接管
- worker tree 现在也有了 bounded multi-level delegation contract：`task.json` 与 `workers/*.json` 会显式持久化 `parent_task_id`、`parent_worker_id`、`root_task_id`、`lineage_depth` 与 `subagent_policy`；runtime 会按 `allow_child_workers`、`allowed_worker_roles`、`max_workers_per_task` 与 `max_lineage_depth` 决定 child 是否还能继续派生 grandchild，而不是只靠隐式 role hardcode
- worker manager surface 现在也会把 child 的 compiled result truth 收口成 `worker_runtime/*.result.json`，并把 `result_summary`、`result_ref`、`completion_status`、`review_status` 与 `verification_status` 注入 `workers/*.json`、`worker_snapshot`、`session_snapshot` 和 provider input；当 child 被 approval / input / continuation 阻塞时，这个 compiled result 也会冻结 `blocked_reason_code`、`blocked_detail_ref`、approval/input refs 与 `parent_action_*`，避免 parent 只能靠散落 refs 自己重建 blocker 与 next action
- worker result / contract 现在还带 artifact-completeness evidence score：`evidence_score`、`evidence_grade`、`missing_evidence`、`verified`、`review_clear`、`handoff_present`、`criteria_closed`、`settlement_accepted`、`reconcile_clean`、`parent_action_unresolved`、`conflict_count` 与 `trusted_for_parent_completion`。parent review 会用该 score 防止 worker-backed criteria 从低证据 child output 误闭。
- parent success criteria 现在也支持 worker-artifact-backed evidence：当 criterion 显式要求 reviewer/security/coding/general child 的存在、compiled result、review clear、verification passed、settlement、reconcile、workspace status 或 `continue_child` readiness 时，runtime 会直接用 `workers/*.json` 与 `worker_runtime/*.result|settlement|reconcile|workspace.json` 回链，而不是把这类 manager-child contract 误闭成泛化 `verification passed`
- replay / resume surfaces 现在会把 side-effect safety 写进 `command_runs.jsonl` / `workspace_edits.jsonl`，command output cap 命中时也会在 `command_runs.jsonl` 写出 `stdout_truncated` / `stderr_truncated`；`status --json.restore_clues` 与 handoff Resume Instructions 会暴露 checkpoint restore bearings；ACP 也提供 `task.events` cursor replay，但仍不引入单独 event database。
- builtin / command / OpenAI-compatible Chat Completions / OpenAI Responses / Anthropic Messages provider

当前仍明确不做：

- broader provider matrix
- richer role-file discovery / inheritance UX beyond current `.ngen/roles/<role_id>.json`
- longer-lived ACP push subscription / multi-client fanout beyond `task.events` replay and per-request `ngen.notification`
- broader child settle protocol beyond current `worker_runtime` baseline + file-level reconcile / parent-takeover records
- default browser / GUI / computer-use plane beyond the current bounded command/repair plane with policy, replay-safety, approval, verifier, and review artifacts

## Quickstart

最快的入口是先读 [docs/quickstart.md](/mnt/c/Users/Admin/Desktop/build/agent-coding/ngen-agent/docs/quickstart.md)。

如果你只想先确认仓库可构建：

```bash
./build.sh
```

这会按顺序执行：

1. `gofmt`
2. `go test ./...`
3. `go build -o ./bin/ngen ./cmd/ngen`

为了让这个根级 wildcard 校验在长期保留 `codex/`、`opencode/` 参考树和 `real_task_tests/` 运行产物时仍然稳定可用，这些目录现在被显式隔离为独立 nested Go modules，不再污染根模块的 package discovery。`./build.sh fmt` 在 git repo 中只格式化 tracked `cmd/**/*.go` 与 `internal/**/*.go`；非 git fallback 会 prune `.ngen`、`bin` 与 ignored run-artifact 目录，避免把 live evidence 或生成产物纳入格式化。

如果你只想单独执行一个阶段：

```bash
./build.sh fmt
./build.sh test
./build.sh build
```

`./build.sh test` 与 `./build.sh build` 默认把 `GOCACHE` 固定到 `/tmp/ngen-go-build`；如果你的环境有更合适的 cache 路径，可以自行覆盖。

## 5-Minute Smoke Test

下面这组命令会用 builtin provider 在临时 workspace 内跑完一个最小 coding task：

```bash
ROOT=$(pwd)
./build.sh build

WORKDIR=$(mktemp -d)
cd "$WORKDIR"

cat > go.mod <<'EOF'
module example.com/demo

go 1.24.2
EOF

cat > demo.go <<'EOF'
package demo

func Add(a, b int) int { return a + b }
EOF

TASK_ID=$("$ROOT/bin/ngen" task create \
  --kind coding \
  --title "smoke test" \
  --objective "verify local coding flow" \
  --criterion "go test passes")

"$ROOT/bin/ngen" auto "$TASK_ID" --json
"$ROOT/bin/ngen" status "$TASK_ID" --json
```

运行后，当前 workspace 会生成：

- `.ngen/tasks/<task_id>/task.json`
- `.ngen/tasks/<task_id>/plan.json`
- `.ngen/tasks/<task_id>/plan_updates.jsonl`
- `.ngen/tasks/<task_id>/criteria/latest.json`
- `.ngen/tasks/<task_id>/criteria/history.jsonl`
- `.ngen/tasks/<task_id>/sprint/latest.json`
- `.ngen/tasks/<task_id>/sprint/history.jsonl`
- `.ngen/project/project.json`
- `.ngen/project/project_updates.jsonl`
- `.ngen/missions/<mission_id>/mission.json`
- `.ngen/missions/<mission_id>/validation_contract.json`
- `.ngen/missions/<mission_id>/features.json`
- `.ngen/missions/<mission_id>/milestones.json`
- `.ngen/missions/<mission_id>/validation_runs.jsonl`
- `.ngen/missions/<mission_id>/notes.md`
- `.ngen/roles/<role_id>.json`
- `.ngen/tasks/<task_id>/state.json`
- `.ngen/tasks/<task_id>/baseline.json`
- `.ngen/tasks/<task_id>/progress.md`
- `.ngen/tasks/<task_id>/continuity/latest.json`
- `.ngen/tasks/<task_id>/continuity/history.jsonl`
- `.ngen/tasks/<task_id>/verification/latest.json`
- `.ngen/tasks/<task_id>/reviews/latest.json`
- `.ngen/tasks/<task_id>/completion/latest.json`
- `.ngen/tasks/<task_id>/handoff.md`
- `.ngen/tasks/<task_id>/context/latest-pack.json`
- `.ngen/tasks/<task_id>/context/summary.md`
- `.ngen/tasks/<task_id>/harness/latest.json`
- `.ngen/tasks/<task_id>/harness/history.jsonl`

如果使用支持 repair loop 的 provider mode（`builtin`、`command`、`openai-response`、`openai-comp`、`anthropic`）去跑一个 `coding` task，runtime 会在同一 task 目录下按需追加 `command_runs.jsonl`，并在发生修复尝试时追加 `workspace_edits.jsonl`，把观察命令、repair command 和文件修改都记录成 durable truth。当前 repair loop 默认最多执行 3 次 bounded repair；repair target 既可以是 verifier failure，也可以是 verifier 已通过后仍未满足的 workspace-backed success criteria。这里的 workspace-backed 不只覆盖显式 path / glob criterion，也覆盖带 readme/docs/config 语义和具体 token 的 criterion，例如 `sample config mentions \`timeout_seconds\``。每次 repair 在真正写文件前可以先运行少量 bounded read-only observation commands，在真正写文件前后还可以执行少量 bounded workspace repair commands，用于 formatter、generator、dependency sync、package install、migration 或 shell-backed repair。若某次 plan 因 apply failed、noop 或 repair command failed 没有把任务推进到新的 verifier truth，runtime 仍会在同一预算内继续下一次 attempt，并把上一轮失败摘要显式送进后续 observation/edit prompt。workspace edits 优先走 patch，并在 hash/read/write/delete 前拒绝中间目录 symlink、最终 symlink 与 workspace 外路径；workspace snapshot 会跳过 symlink 并在 collection metadata 中报告 omission。observation command 仍服从只读 allowlist 与 visibility deny 规则，`rg` 会拒绝 hidden/ignored/symlink traversal flags 且 `--files` path operand 也必须过 workspace/deny 校验；`ls` 不允许 hidden listing flags；`find` 额外拒绝 `-H`、`-L`、`-follow`、读取外部 path operand 的 predicate、expression 后路径，以及覆盖 `.ngen` / `.git` 等 denied root 的 broad search；`go` observation 只保留 `version` / `env` / `list` / `doc` 这类只读查询并拒绝 verifier/mutating subcommands 或 flags；`git` observation 拒绝 `--no-index`、external diff/textconv、output files、`rev:path` content reads、ignored listing，以及未用 `--` 收口的 deny-path pathspec 或 broad content commands。repair command 则按 `permission_mode_id` 收口：`standard` 只自动执行 allowlisted safe commands，shell/script/package-manager/repo-script 类命令需要 yolo 或人工处理，并在 `command_runs.jsonl` 中记录 `policy_decision`；当 `permission.benchmark_integrity_mode=true` 时，网络和 open-world repair command 会被额外拒绝并记录为 `denied_benchmark_integrity`，即使当前 task 是 `yolo`。provider HTTP response 以 4 MiB 上限 decode，command provider、observation/repair command 与 hooks stdout/stderr 以 1 MiB cap 捕获；命中 cap 的 command record 会写出 truncation metadata。

## Optional Remote Provider Setup

如果你要用远端 provider，在目标 workspace 下写 `ngen.json`。最小配置示例：

```json
{
  "verification": {
    "coding_commands": [
      ["./build.sh", "test"],
      ["./build.sh", "build"]
    ],
    "coding_timeout_seconds": 60
  },
  "provider": {
    "mode": "openai-response",
    "base_url": "http://69.63.215.40:24634/v1",
    "api_key_env": "OPENAI_API_KEY",
    "model": "gpt-5.4",
    "auto_run_max_turns": 1,
    "decision_timeout_seconds": 30,
    "coding_execution_command_budget": 2,
    "coding_execution_command_timeout_seconds": 60
  },
  "permission": {
    "default_mode": "standard",
    "benchmark_integrity_mode": false
  },
  "mission": {
    "role_models": {
      "orchestrator": "gpt-5.5",
      "workers": "gpt-5.4",
      "validators": "gpt-5.5"
    }
  },
  "subagents": {
    "workspace_isolation": "auto",
    "auto_release_on_success": true,
    "max_lineage_depth": 2,
    "role_policies": {
      "coding": {
        "allowed_worker_roles": ["coding", "general_execution", "reviewer", "security_review"]
      },
      "reviewer": {
        "workspace_isolation": "snapshot_copy",
        "auto_release_on_success": false
      }
    }
  }
}
```

然后设置环境变量：

```bash
export OPENAI_API_KEY=your-key
```

说明：

- `mode` 也可以切到 `openai-comp`、`anthropic`、`builtin` 或 `command`
- `verification.coding_commands` 是当前 coding repo contract 的最高优先级 verifier sequence；若留空，则 runtime 才会回退到 criterion-derived repo-owned verifier commands 或 legacy `coding_go_test_command`
- `permission.benchmark_integrity_mode=true` 适合 Terminal-Bench / leaderboard evaluation；它会在 repair command lane 中拒绝网络或 open-world 命令，即使 task 使用 `permission_mode_id=yolo`
- `ngen` 总是以“当前工作目录”为 target workspace
- runtime artifacts 会写到当前 workspace 下的 `.ngen/`
- 当前真正会自动改代码的自治写路径已经覆盖全部当前 provider mode：`builtin`、`command`、`openai-response`、`openai-comp`、`anthropic`
- `builtin` 走本地 deterministic repair engine；它共享同一条 runtime budget / artifact / guard 路径，但能力边界仍明显窄于远端模型
- `command` 通过同一条 stdin JSON contract 接入 decision / workspace observation / workspace edit；runtime 会额外注入 `NGEN_PROVIDER_MODE` 与 `NGEN_PROVIDER_OPERATION=decision|workspace_observation|workspace_edit`
- `openai-response`、`openai-comp` 与 `anthropic` 仍是当前更强的模型驱动 coding path；它们和 `builtin` / `command` 共享同一条 runtime observation/edit/repair-command contract 与 durable truth 语义

## Main Commands

最常用的 CLI surface：

```text
ngen task create --kind coding --title "..." --objective "..." --criterion "..."
ngen task list --json
ngen task get TASK-... --json
ngen task update TASK-... --plan-file ./plan-update.json --json
ngen task patch TASK-... --patch-file ./plan-patch.json --json
ngen project get --json
ngen project update --project-file ./project-update.json --json
ngen project patch --patch-file ./project-patch.json --json
ngen run TASK-...
ngen resume TASK-...
ngen auto TASK-... --json
ngen status TASK-... --json
ngen review TASK-...
ngen events tail TASK-... --json --limit 20 [--after EVT-...]

ngen input request TASK-... --prompt "..." --field target_path
ngen input ls TASK-...
ngen input respond TASK-... --request INP-... --value "..."

ngen worker spawn TASK-... --role reviewer|security_review|coding|general_execution --objective "..."
ngen worker ls TASK-...
ngen worker sync TASK-... WKR-...
ngen worker continue TASK-... WKR-...

ngen approval request TASK-... --scope "..." --reason "..."
ngen approval ls TASK-... [--owned]
ngen approve TASK-... --request APR-...
ngen deny TASK-... --request APR-...

ngen watch set TASK-... --interval 5m --reason "..."
ngen watch ls
ngen watch cancel WATCH-...
ngen scheduler tick --once

ngen memory show
ngen memory promote TASK-... --summary "..." [--kind milestone|decision|blocker|note] [--ref REF]...
ngen harness eval TASK-... [--json]
ngen mission "normal prompt describing the objective"
ngen goal "normal prompt describing the objective"
ngen mission create "normal prompt describing the objective" [--json]
ngen mission create --title "..." --objective "..." [--criterion "..."]... [--json]
ngen mission approve MIS-... --json
ngen mission run MIS-... --json
ngen mission status MIS-... --json
ngen mission validate MIS-... --json
ngen acp serve
ngen terminal TASK-...
ngen tui [TASK-...] [--inline] [--poll-ms N] [--event-limit N]
```

## Repo Layout

高频目录如下：

- [cmd/ngen/main.go](/mnt/c/Users/Admin/Desktop/build/agent-coding/ngen-agent/cmd/ngen/main.go): CLI entrypoint
- [internal/app/app.go](/mnt/c/Users/Admin/Desktop/build/agent-coding/ngen-agent/internal/app/app.go): command routing and output formatting
- [internal/runtime/](/mnt/c/Users/Admin/Desktop/build/agent-coding/ngen-agent/internal/runtime): state machine, orchestration, session/worker/watch/input flow
- [internal/provider/provider.go](/mnt/c/Users/Admin/Desktop/build/agent-coding/ngen-agent/internal/provider/provider.go): provider adapters
- [internal/acp/server.go](/mnt/c/Users/Admin/Desktop/build/agent-coding/ngen-agent/internal/acp/server.go): ACP stdio JSON-RPC bridge
- [docs/00-repo-status.md](/mnt/c/Users/Admin/Desktop/build/agent-coding/ngen-agent/docs/00-repo-status.md): current active contract boundary
- [docs/quickstart.md](/mnt/c/Users/Admin/Desktop/build/agent-coding/ngen-agent/docs/quickstart.md): operator-first quickstart

## Reading Order

推荐阅读顺序：

1. [docs/quickstart.md](/mnt/c/Users/Admin/Desktop/build/agent-coding/ngen-agent/docs/quickstart.md)
2. [docs/00-repo-status.md](/mnt/c/Users/Admin/Desktop/build/agent-coding/ngen-agent/docs/00-repo-status.md)
3. [docs/01-foundations.md](/mnt/c/Users/Admin/Desktop/build/agent-coding/ngen-agent/docs/01-foundations.md)
4. [docs/02-prd/positioning-and-scope.md](/mnt/c/Users/Admin/Desktop/build/agent-coding/ngen-agent/docs/02-prd/positioning-and-scope.md)
5. [docs/04-runtime/lifecycle-and-state.md](/mnt/c/Users/Admin/Desktop/build/agent-coding/ngen-agent/docs/04-runtime/lifecycle-and-state.md)
6. [docs/05-artifacts-and-context/task-lifecycle-artifacts.md](/mnt/c/Users/Admin/Desktop/build/agent-coding/ngen-agent/docs/05-artifacts-and-context/task-lifecycle-artifacts.md)
7. [docs/06-go-package-and-api/package-layout-and-cli.md](/mnt/c/Users/Admin/Desktop/build/agent-coding/ngen-agent/docs/06-go-package-and-api/package-layout-and-cli.md)
8. [docs/07-verification-security-and-ops/verification-review-and-waivers.md](/mnt/c/Users/Admin/Desktop/build/agent-coding/ngen-agent/docs/07-verification-security-and-ops/verification-review-and-waivers.md)
9. [docs/08-delivery-plan/completion-and-work-packages.md](/mnt/c/Users/Admin/Desktop/build/agent-coding/ngen-agent/docs/08-delivery-plan/completion-and-work-packages.md)

## Verification

本仓库当前默认验证方式：

```bash
go test ./...
```

如果你只关心构建和基本质量闸门，直接执行：

```bash
./build.sh
```

## Notes

- `ngen` 读取的是当前 workspace 根目录下的 `ngen.json`
- `ngen` 不会把 task truth 写回聊天；真相只在 `.ngen/` artifacts
- 如果你改了 artifact contract、CLI contract、状态码或 runtime invariant，需要同步 owner docs
