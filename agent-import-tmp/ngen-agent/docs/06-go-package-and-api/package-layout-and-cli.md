# 包布局与 operator surfaces

## 1. 当前实现目标

当前实现：

- 单二进制 `ngen`
- 本地文件系统状态
- human-readable CLI
- `--json` headless output
- `ngen-stream-json` headless exec output for Multica-compatible local runtime use
- ACP stdio server + per-request JSON-RPC notifications
- HTTP web backend service for local management surfaces
- interactive terminal line editor
- full-screen TUI
- builtin / command / OpenAI-compatible Chat Completions / OpenAI Responses / Anthropic Messages provider adapter
- `coding` task 在全部当前 provider mode（`builtin`、`command`、`openai-response`、`openai-comp`、`anthropic`）上的 bounded multi-attempt coding repair path，支持 read-only observation commands、patch-first workspace edits，以及 bounded workspace repair commands

## 2. 当前实际包布局

```text
cmd/ngen/
  main.go

internal/app/
  app.go
  app_test.go

internal/artifact/
  store.go

internal/acp/
  server.go

internal/web/
  server.go

internal/provider/
  builtin_coding.go
  command_loop.go
  provider.go

internal/task/
  ids.go
  service.go
  types.go

internal/runtime/
  extended.go
  gate.go
  harness.go
  mission.go
  runner.go
  state_machine.go

internal/tui/
  backend.go
  model.go
  render.go
  run.go
  styles.go
  transcript.go

internal/verify/
  pipeline.go

internal/review/
  reviewer.go
```

设计原则：

- `internal/app/` 只做命令编排与输出格式。
- `internal/runtime/` 拥有状态推进、review integration、done gate、watch、approval、input、provider dispatch、session、worker、memory、harness evaluation、role contract hydration/gating 与 mission orchestration。
- `internal/runtime/` 现在也拥有 `coding` task 的 bounded multi-attempt coding orchestration，并把 durable observation / repair-command / write truth 分别写入 `command_runs.jsonl` 与 `workspace_edits.jsonl`。
- `internal/web/` 只把现有 `runtime.Service` 包装成本地 HTTP API；它不拥有 web-only task truth，不绕过 `.ngen/` artifacts，也不替代 ACP。
- `internal/tui/` 在不拥有 task truth 的前提下，把 `runtime.Service` 暴露为接近 Codex 的 chat-first 极简 operator surface：默认 composer + transcript + compact status，按需打开 status/memory/blocker details，并通过 UI-side background turn runner 持续刷新 artifacts。task/subtask/worker orchestration 由 agent/runtime 自主管理，不作为 TUI 默认用户操作入口。
- `internal/artifact/` 拥有 durable store，包括 workspace-level project、mission、role、session、memory 与 task artifacts。
- `internal/verify/` 与 `internal/review/` 独立，避免把 verifier/review 逻辑塞进 CLI。

## 3. 当前 CLI 合同

当前冻结的命令：

```text
ngen task create --kind coding --title "..." --objective "..." --criterion "..."
ngen task create --task-file ./task.json
ngen task list [--json]
ngen task get TASK-... [--json]
ngen task update TASK-... --plan-file ./plan-update.json [--json]
ngen task patch TASK-... --patch-file ./plan-patch.json [--json]
ngen project get [--json]
ngen project update --project-file ./project-update.json [--json]
ngen project patch --patch-file ./project-patch.json [--json]
ngen mission PROMPT... [--json]
ngen goal PROMPT... [--json]
ngen --version
ngen version [--json]
ngen models --json [--workdir DIR] [--config FILE]
ngen exec --output-format stream-json --input-format stream-json --workdir DIR [--config-scope daemon] [--config FILE] [--resume TASK-ID] [--role orchestrator|worker|validator|reviewer] [--timeout-seconds N]
ngen mission create PROMPT... [--root-task TASK-...] [--criterion "..."]... [--json]
ngen mission create --title "..." --objective "..." [--root-task TASK-...] [--criterion "..."]... [--json]
ngen mission get MIS-... [--json]
ngen mission status MIS-... [--json]
ngen mission plan MIS-... [--json]
ngen mission approve MIS-... [--json]
ngen mission validate MIS-... [--milestone MS-...] [--json]
ngen mission run MIS-... [--json]
ngen mission pause MIS-... --reason "..."
ngen mission resume MIS-... [--json]

ngen auto TASK-...
ngen auto TASK-... --json

ngen run TASK-...
ngen run TASK-... --json

ngen resume TASK-...
ngen resume TASK-... --json

ngen status TASK-...
ngen status TASK-... --json

ngen review TASK-...
ngen review TASK-... --json

ngen events tail TASK-...
ngen events tail TASK-... --json --limit 20 [--after EVT-...]

ngen handoff export TASK-...

ngen watch set TASK-... --interval 5m --reason "..."
ngen watch ls
ngen watch cancel WATCH-...

ngen scheduler tick --once

ngen approval request TASK-... --scope "..." --reason "..."
ngen approval ls TASK-... [--owned]
ngen approve TASK-... --request APR-...
ngen deny TASK-... --request APR-...

ngen input request TASK-... --prompt "..." --field target_path
ngen input ls TASK-...
ngen input respond TASK-... --request INP-... --value "..."

ngen worker spawn TASK-... --role reviewer|security_review|coding|general_execution --objective "..."
ngen worker ls TASK-...
ngen worker sync TASK-... WKR-...
ngen worker continue TASK-... WKR-...

ngen memory show
ngen memory promote TASK-... --summary "..." [--kind KIND] [--ref REF]...

ngen harness eval TASK-... [--json]

ngen acp serve
ngen terminal TASK-...
ngen tui [TASK-...] [--inline] [--poll-ms N] [--event-limit N]
ngen web serve [--listen 127.0.0.1:8765] [--token-env NGEN_WEB_TOKEN] [--allow-unauthenticated]
```

补充约束：

- `approval ls TASK-ID` 只暴露该 task 自己的 `approvals.jsonl`
- `approval ls TASK-ID --owned` 允许 parent task 聚合 owned child approval history
- `approve` / `deny` 允许 parent task 解析并决策 owned child approval id
- `worker spawn` 只接受 `reviewer`、`security_review`、`coding` 与 `general_execution` 这组显式 role；未知 role 必须报错，而不是静默创建 docs child
- `worker sync` 现在必须把 child blocker、approval ref、`requires_parent_action` / `parent_action_*` control fields、child workspace/settlement/reconcile refs 持久化回 `workers/*.json`，并同步写出 `worker_runtime/*.workspace.json` / `*.baseline.json` / `*.settlement.json` / `*.reconcile.json`
- `worker continue` 是批准后继续 child 的唯一 parent-side helper；它不能跳过仍处于 pending 的 owned approval
- `task update` 只改写 mutable execution lane，不会直接改写 criterion / review / completion truth；full checklist payload 来自 `plan-file`
- `task patch` 只对当前 mutable execution lane 应用顺序 patch operations，不会直接改写 criterion / review / completion truth；当前 patch op 冻结为 `set_explanation`、`upsert_step` 与 `remove_step`
- `project get` 返回 singular workspace project graph，而不是 task-local view
- `project update` 负责全量刷新 `.ngen/project/project.json` 的 durable project graph；它允许 project step/branch 绑定已有 `task_id`
- `mission PROMPT...`、`goal PROMPT...` 与 `mission create PROMPT...` 是简单入口：整段 positional prompt 直接作为 objective，并自动推导 title 与默认 evidence-backed criterion；旧的 `mission create --title ... --objective ...` 仍保留给需要显式字段的脚本
- `--version` / `version --json` 是 preflight surface，不读取 workspace config，也不要求 provider env。
- `models --json` 使用与 `exec` 相同的 config resolution：显式 `--config` > `NGEN_CONFIG` > `<workdir>/ngen.json` > default。输出 route id 采用 `provider-mode/model`，例如 `openai-response/gpt-5.5`、`anthropic/claude-sonnet-4-6` 或 `builtin/default`；thinking metadata 只来自 NGEN config，并且是 read-only diagnostic。
- `exec` 是 Multica/headless integration surface。它只支持 `--output-format stream-json --input-format stream-json`；stdin 必须是一条 user envelope，可以是 NGEN 顶层形状 `{"protocol":"ngen-stream-json","protocol_version":1,"type":"user","role":"user","content":[...]}`，也可以是 go-cli 兼容的嵌套形状 `{"type":"user","message":{"role":"user","content":[...]}}`。无 `--resume` 时创建 task，有 `--resume` 时按 NGEN task id 继续。`Result.session_id` 等于 task id，final result 同时写顶层 `result` 字段，便于 Multica 直接读取。`--config-scope daemon` 禁止 `<workdir>/ngen.json` 改变 provider/model selection，但仍把 `--workdir` 作为 workspace 和 `.ngen/` artifact root。首次 run 写 `multica/run_metadata.json` 与 `multica/workspace_guidance.json`，resume 时若 metadata 缺失或 config/model fingerprint drift，会 fail closed 为 final `result.status=blocked`。输入读取器是薄适配器：它只接受 `type=user` / `role=user` 的 text content blocks，把非空 text blocks 按 `\n` 拼成 prompt 并交给 task/runtime；它不解析、不复制、也不基于 `metadata`、`system_prompt`、AGENTS.md 或 `.agent_context` 合成 objective、constraints、run role、issue id 或 issue-execution flow，也不能把 squad/project UUID 当作 issue UUID。若用户文本本身明确要求运行某个 direct argv command（例如 "Run exactly one `...` invocation."），adapter 只把 success criteria 表达为通用 command-backed evidence requirement；runtime 随后通过普通 repair command policy 执行该显式命令，并用 `command_runs.jsonl` 关闭 criteria。adapter 和 runtime 都不能为该命令合成参数、description 文件、issue comment、delegation 或 fallback artifact。Multica 外部状态 mutation（例如 `multica issue create`、`multica issue comment add`、issue-scoped `multica squad delegate`）必须继续走 permission-gated repair command lane，并留下 `command_runs.jsonl`、stdout/stderr artifact、policy decision 与 replay-safety truth；adapter 不能直接调用 Multica。
- `mission create` 创建或绑定 root task，并写 `.ngen/missions/<mission_id>/mission.json`、`validation_contract.json`、`features.json`、`milestones.json` 与 `notes.md`；`validation_contract.json.assertions` 会为 acceptance criteria 生成稳定 `ASSERT-*` id，`features.json` / `milestones.json` 的 `contract_coverage` 默认引用 assertion ids；`mission.json.role_plan` 会冻结 `orchestrator`、`workers`、`validators` 的 effective model/source/explicit；新 mission 默认 `plan_approval_status=pending`
- `mission approve` 只校验 mission plan artifact，不运行 root task 或 provider；它要求每个 assertion 至少有 feature 与 milestone coverage，coverage 不完整时返回 `blocked_contract_coverage` mission view，成功后写 `plan_approval_status=approved` 与当前 `validation_contract.json#contract_id=...` 形式的 `plan_approved_contract_ref`
- `mission validate` 先读取 mission contract 与 root task artifacts 执行 deterministic gate；每个 assertion 必须同时有 coverage 和 closing evidence ref。只有 deterministic pass 通过且 `role_plan.validators.explicit=true` 时，才通过 dedicated read-only model-validator schema 调用 provider。它不执行 workspace edits、repair commands、provider decision actions、`task_create` 或 worker creation；未配置 user-testing tool plane 时会写 non-blocking skipped finding
- `mission run` 先执行 deterministic plan gate；未批准、批准引用不匹配当前 contract，或 assertion coverage 不完整时返回 `blocked_plan_gate` mission view，不进入 orchestrator。gate 通过后再按 `role_plan.orchestrator` effective model 执行一轮 bounded provider-decision orchestration，并执行 mission validation；root task verifier/review/done gate 仍是完成事实来源。mission-scope `task_create` 可创建 lineage-bound child task，但只能绑定当前 mission feature，且单次 mission orchestration pass 创建一个后停止
- `memory promote` 负责把 reusable milestone / decision / blocker summary 追加到 `.ngen/memory/entries.jsonl`；它不会改写 task truth，但会刷新 `MEMORY.md`
- `harness eval` 只读取 `.ngen/tasks/<task_id>/harness/latest.json`，用于查询最近一次 `run`、`resume`、`auto` 或 `review` pass 的 provider/context/repair/review/completion strategy snapshot；它不重新执行 provider 或 verifier
- `project patch` 负责对 workspace project graph 应用顺序 patch operations；当前除了 `set_explanation`、`upsert_step`、`remove_step`、`upsert_branch` 与 `remove_branch` 外，还支持 `set_step_dependencies`、`set_step_parent`、`bind_step_branch`、`bind_step_task`、`bind_branch_task` 与 `set_branch_status`
- `tui` 必须与 terminal 共享同一条 session / status / approval / input / worker contract；默认启动不应被必经 task picker、task list、worker manager 或确认弹层阻塞，可以提供 focused blocker view / background turn polling，但不得写出 UI-only task truth。task/subtask/worker 生命周期由 agent/runtime 根据 artifacts 和 provider decisions 自主管理，TUI 只呈现状态与必要人工决策。
- `web serve` 默认绑定 `127.0.0.1:8765`，以 HTTP JSON/SSE 暴露 local management surface：`GET /healthz`、`GET|POST /api/tasks`、`GET /api/tasks/{task_id}`、`GET /api/tasks/{task_id}/events?after=EVT-...&limit=N`、`GET /api/tasks/{task_id}/events/stream`、`GET /api/missions/{mission_id}`、`POST /api/sessions`、`GET /api/sessions/{session_id}`、`POST /api/sessions/{session_id}/prompt` 与 `POST /api/sessions/{session_id}/cancel`。除 `/healthz` 外，若 `--token-env` 指定的环境变量非空，API 必须要求 `Authorization: Bearer <token>`；若 token 为空且 listen address 不是 loopback（例如 `0.0.0.0:8765`、`:8765`、LAN IP 或非 localhost hostname），CLI 必须拒绝启动。确需无鉴权暴露时必须显式传 `--allow-unauthenticated`。web backend 只能调用现有 runtime service contract，不能新增 web-only state 或绕过 verifier/review/done gate。
- `GET /api/tasks/{task_id}/events/stream` 使用 Server-Sent Events 输出现有 task event artifact；默认 follow，新客户端可用 `follow=false` 获取一次性 snapshot，可用 `limit` 和 `interval_ms` 控制尾部数量与轮询间隔，也可用 `Last-Event-ID` 或 `?after=EVT-...` 从指定 event cursor 后重放。该端点只流式读取 `.ngen/tasks/<task_id>/events.jsonl` 的 runtime truth，不创建独立 web event log；缺失 cursor 必须返回显式 diagnostic，而不是退回最新 tail。

## 4. `--json` 输出规则

- `auto --json` / `run --json` / `resume --json` / `events tail --json`
  - stdout 逐行输出 JSON event objects
- `status --json` / `review --json` / `task get --json`
  - stdout 输出单个 JSON object
- `mission create|get|status|plan|approve|validate|run|resume --json`
  - stdout 输出单个 mission JSON object；`approve`/`validate`/`run` 在 mission status 为 `blocked` 时返回 exit `10`
- `harness eval --json`
  - stdout 输出单个 `harness_evaluation` JSON object
- `task list --json`
  - stdout 输出单个 JSON array
- `acp serve`
  - 每个 request 至少返回一个 JSON-RPC response；mutating calls 可在 response 之后追加 `ngen.notification`
- diagnostics 仍写 stderr

## 5. exit status

- `0`: `Done`
- `10`: `Blocked`
- `11`: `Failed`
- `12`: `Aborted`
- `15`: `Waiting`
- `13`: CLI / input validation failure

## 6. headless compatibility policy

- 同一 `schema_version` 下，event object、`status_snapshot`、`session_snapshot`、`worker_snapshot` 与 `acp_notification` 只允许 additive change
- 任何 artifact ref、字段名、状态码或命令参数变化都必须先更新 owner docs
