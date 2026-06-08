# 配置与领域模型

## 1. 当前 `ngen.json`

当前实现冻结以下配置：

```json
{
  "state_dir": ".ngen",
  "default_profile": "coding",
  "verification": {
    "coding_commands": [],
    "coding_go_test_command": ["go", "test", "./..."],
    "coding_timeout_seconds": 60
  },
  "watch": {
    "default_interval_seconds": 300
  },
  "scheduler": {
    "lease_file": ".ngen/runtime/scheduler.lock"
  },
  "provider": {
    "mode": "builtin",
    "command": [],
    "auto_run_max_turns": 3,
    "base_url": "",
    "api_key_env": "OPENAI_API_KEY",
    "model": "",
    "decision_timeout_seconds": 30,
    "decision_max_output_tokens": 2048,
    "system_prompt": ""
  },
  "mission": {
    "role_models": {}
  },
  "hooks": {
    "pre_run_command": [],
    "post_run_command": [],
    "on_done_command": [],
    "registry": []
  },
  "visibility": {
    "additional_roots": [],
    "deny_patterns": [".git", ".ngen"]
  },
  "memory": {
    "enabled": true,
    "file": ".ngen/memory/MEMORY.md",
    "max_entries": 50
  },
  "subagents": {
    "max_workers_per_task": 4,
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
  },
  "acp": {
    "enabled": true
  },
  "permission": {
    "default_mode": "standard"
  },
  "tui": {
    "alternate_screen": "auto",
    "poll_interval_ms": 200,
    "event_limit": 500
  }
}
```

补充说明：

- `state_dir` 当前是兼容字段，但 active runtime 仍固定要求 `.ngen`，因为 artifact refs、owner docs 与 bridge surfaces 都以 `.ngen/` 作为 canonical state root；非 `.ngen` 值会在 config load 阶段显式报错。
- `state_dir`、`scheduler.lease_file` 与 `memory.file` 必须是 workspace-relative slash paths；绝对路径、`..` workspace escape、Windows drive / backslash syntax 与 NUL 都会被拒绝。
- `memory.file` 是 workspace memory Markdown 的实际写入路径；append-only memory entries 仍固定在 `.ngen/memory/entries.jsonl`。
- `workspace_isolation` 只决定 child 的 workspace prepare 方式；
- child accepted settlement 之后的 side-effect reconcile mode 当前由 role policy 决定：
  - `coding` / `general_execution` => `apply_on_accept`
  - `reviewer` / `security_review` => `artifact_only`
- `max_lineage_depth` 冻结当前 active 的 bounded child tree 深度；默认 `2` 表示 root 可继续派生 child 与 grandchild，但不会无限向下扩张
- `role_policies` 允许按 role override `permission_mode_id`、`workspace_isolation`、`reconcile_mode`、`auto_release_on_success`、`allow_child_workers`、`allowed_worker_roles`、`max_workers_per_task` 与 `max_lineage_depth`
- isolated child spawn baseline 会写入 `worker_runtime/*.baseline.json`
- isolated child reconcile truth 会写入 `worker_runtime/*.reconcile.json`
- `tui.alternate_screen` 支持 `auto|always|never`
- `tui.poll_interval_ms` 默认 `200`，最小钳制到 `50`
- `tui.event_limit` 控制 UI transcript 保留的 event tail 数量，不影响 durable `events.jsonl`
- `provider.decision_max_output_tokens` 只控制 provider decision 输出预算，默认 `2048`；workspace edit / observation 仍使用各自的 bounded request budget。该值需要足够容纳 `task_update`、`project_update`、worker、approval 与 task-create 等结构化 action，不应退回到过小的 256-token decision ceiling。
- `mission.role_models` 是可选的 mission-scope model override map，合法 key 只有 `orchestrator`、`workers`、`validators`；未知 key 必须在 config load 阶段报错。非空值覆盖该 mission role 的 `provider.model`，空值或缺失值只继承 `provider.model`。
- `mission.role_models.validators` 只有在原始配置中显式设置为非空字符串时，才会让新建或重设 objective 的 mission role plan 记录 `validators.explicit=true`。继承到的非空 `provider.model` 只用于 role plan 展示，不会自动启用 model-backed validator。
- role-specific routing 当前只覆盖 model，不引入 role-specific `base_url`、`api_key_env`、provider mode、权限、预算或 sandbox policy。有效选择会冻结到 `mission.json.role_plan`；既有 mission 不会因为 `ngen.json` 后续变化而静默改写。

## 2. 当前领域类型

```go
package task

type Kind string

const (
    KindCoding         Kind = "coding"
    KindGeneral        Kind = "general_execution"
    KindSecurityReview Kind = "security_review"
    KindReviewer       Kind = "reviewer"
)

type PresetID string

const (
    PresetDocsLite PresetID = "docs_lite"
)

type Phase string

const (
    PhaseExplore Phase = "Explore"
    PhasePlan    Phase = "Plan"
    PhaseExecute Phase = "Execute"
    PhaseVerify  Phase = "Verify"
    PhaseReview  Phase = "Review"
)

type StateName string

const (
    StateActive  StateName = "Active"
    StateBlocked StateName = "Blocked"
    StateWaiting StateName = "Waiting"
    StateDone    StateName = "Done"
    StateFailed  StateName = "Failed"
    StateAborted StateName = "Aborted"
)
```

当前 task package 还冻结 `HarnessEvaluation` 作为 task-local harness strategy snapshot：`object_kind=harness_evaluation`，主键为 `harness_eval_id`，并记录 `runtime_action`、provider mode/model、prompt ref、context/continuity/sprint/criteria refs、repair/observation/execution budgets、verification/review/completion status、workspace edit summary、worker/memory activity、latest provider usage ref、`token_usage` / `prompt_cache_usage` 与 evidence refs。该类型只引用 artifact truth，不保存完整 provider payload、API key 或未脱敏 hidden prompt 文本。task-local `ProviderUsageRecord` 追加到 `provider_usage.jsonl`，用 `usage_record_id` 记录 decision、workspace observation、workspace edit 与 mission validation 的 sanitized provider usage；provider 没有返回 usage 时写 `unknown`，不能把 unknown 记作 `0`。

当前 task package 还冻结 mission domain types：`Mission`、`MissionValidationContract`、`MissionContractAssertion`、`MissionFeatureSet`、`MissionMilestoneSet`、`MissionValidationRun`、`MissionMetricsRecord`、`MissionMetricsSnapshot`、`MissionStatusSnapshot`、`MissionView` 与 `MissionPlanView`。`Mission` 现在带 additive `role_plan` snapshot，记录 `orchestrator`、`workers`、`validators` 的有效 model、source 与 explicit bit；同时带 `plan_approval_status`、`plan_approved_at`、`plan_approved_by` 与 `plan_approved_contract_ref`，用于在执行前冻结 operator 已批准的当前 validation contract。批准成功会清空当前 `latest_validation_ref`，但不会删除 `validation_runs.jsonl` 历史。`MissionValidationContract.Assertions` 是 stable assertion ledger，新 feature/milestone coverage 默认引用 `ASSERT-*` ids；`MissionMilestoneSet` 额外派生 `current_feature_id`、`ready_feature_ids` 与 `blocked_feature_ids`，用于表达 serial feature execution invariant；model / semantic fix-feature candidate 只有在 finding 明确指向 assertion 时才写 coverage，普通 evidence refs 保留在 `evidence_refs`，并通过 root plan patch 或 terminal-task scoped event 写清 follow-up work；deterministic precondition blockers 和 assertion evidence closure blockers 不生成伪 fix feature。`MissionValidationRun` 现在带 `validator_role`、`validator_kind`、`validator_model`、`validator_model_source`、`validator_model_explicit`、`validator_context_refs`、可选 `provider_usage_ref` 以及 validator call 的 `token_usage` / `prompt_cache_usage`，未配置 user-testing tool plane 时会包含 non-blocking `user_testing_validator_skipped` finding。`MissionMetricsRecord` 追加到 `metrics.jsonl`，记录 wall time、可用的 validator time、task/worker/repair/validation counts、role model snapshot 与 provider-call summary；provider 未暴露 token/cache/cost 时写 `unknown` 而不是 `0`。`MissionStatusSnapshot` 是 `mission status --json` 的 derived read model，包含 current feature、active child refs、latest validation blocker count、unresolved fix features、recent mission events 与 metrics summary。mission status 当前为 `draft|active|blocked|paused|done`；blocking validator 使用 `blocked_validation`、`blocked_plan_gate` 或 `blocked_contract_coverage` reason。mission 不引入 YAML 或数据库，所有 state 都落在 `.ngen/missions/<mission_id>/` 的 JSON/JSONL/Markdown artifacts。

当前 `ReviewReport` 也已经从最小 `status/summary` 扩展为 additive evidence-first contract：`reviewer_profile` 标识当前 profile-specific reviewer lane，`review_context_refs` 冻结本次 review 读取的 artifact refs，`changed_paths` 与 `worker_result_refs` 暴露 review 输入面，`risk_summary` 汇总 blocking finding categories，`blocking_categories` 给 `blocked_review` 提供稳定分类。`Finding` 继续写入 `findings.jsonl`，并新增 `affected_paths`；category vocabulary 冻结为 `confirmed_defect|missing_evidence|scope_drift|complexity_risk|security_risk|stale_context_risk|worker_trust_gap|inferred_risk|not_observed`。

当前 task package 还冻结 `QualityDiagnostic` / `QualityFinding`：`object_kind=quality_diagnostic`，主键为 `diagnostic_id`，写入 `diagnostics/quality-latest.json` 与 `diagnostics/quality-history.jsonl`。该类型记录 changed paths、test-file changes、generated-file changes、workspace edit attempts、failed/noop edit count、same-failure count、same-file rewrite count、scope drift paths、large/dependency/abstraction warning、review-required bit、block-completion bit、findings 与 evidence refs。

当前 `WorkerContract` 与 `WorkerResult` 还冻结 worker evidence scoring fields：`evidence_score`、`evidence_grade`、`missing_evidence`、`verified`、`review_clear`、`handoff_present`、`criteria_closed`、`settlement_accepted`、`reconcile_clean`、`parent_action_unresolved`、`conflict_count` 与 `trusted_for_parent_completion`。该分数只由 artifacts 计算，不表达模型自信度。

当前 `MemoryEntry` 还冻结 workspace memory governance metadata：`scope`、`paths`、`profiles`、`provider_modes`、`stale_after`、`supersedes`、`superseded_by`、`confidence`、`freshness_status` 与 `last_validated_ref`。当前 active runtime 会在 promote 时写入 task scope、workspace path refs、profile、provider mode、observed/validated confidence、fresh freshness 与最近验证 ref；`stale_after` / supersession 字段属于 additive schema 预留，当前还不主动计算。`MEMORY.md` 由 `entries.jsonl` 刷新生成，path-scoped entry 在对应 workspace path 不存在时渲染为 stale。

当前 task package 还冻结 `RoleContract` 作为 workspace-level profile capability contract，落盘路径为 `.ngen/roles/<role_id>.json`：

```json
{
  "schema_version": 1,
  "role_id": "coding",
  "profile_kind": "coding",
  "description": "Primary coding agent profile.",
  "allowed_provider_actions": ["run", "resume", "respond", "review", "task_create", "task_update", "task_patch", "project_update", "project_patch", "memory_promote", "worker_spawn", "worker_continue", "wait", "approval_request", "block", "noop"],
  "allowed_worker_roles": ["coding", "general_execution", "reviewer", "security_review"],
  "workspace_isolation": "auto",
  "reconcile_mode": "apply_on_accept",
  "permission_mode_id": "standard",
  "context_sections": ["task", "plan", "project", "criteria", "continuity", "sprint", "verification", "review", "completion", "worker", "session", "workspace_memory"],
  "review_requirements": ["review gate must consume artifact evidence"],
  "verification_requirements": ["coding verifier sequence must pass before Done"],
  "memory_policy": "workspace memory is a cross-task hint; fresh task-local artifacts win on conflict",
  "output_contract": "workspace changes plus artifact-backed progress, verification, review, and handoff"
}
```

当前 active role ids 仍冻结为 `coding`、`general_execution`、`reviewer` 与 `security_review`。runtime 会为内建 role 水合默认 contract；role file 解析失败、unsupported provider action、unsupported worker role、unsupported workspace isolation / reconcile / permission mode 都必须显式报错。`allowed_provider_actions` 为空不是“无限制”，而是无效 role file。

## 3. foundation verifier routing

`coding`
  - baseline
  - `verification.coding_commands` 可显式声明 ordered verifier sequence；runtime 会按顺序逐条执行这些 repo-owned verifier commands
  - 若 `verification.coding_commands` 为空，则 legacy `verification.coding_go_test_command` 仍可声明单条 canonical verifier command
  - 若上述两者都未显式改写，则默认回退到 `go test ./...`
  - 若仍处于默认 verifier 配置，而 `success_criteria[].statement` 明确声明了 verifier 命令，例如 ``./build.sh test`` passes 与 ``./build.sh build`` passes，则当前 task 会按 criteria 中出现顺序推导 repo-owned verifier sequence
  - verifier command timeout guard via `verification.coding_timeout_seconds`
`general_execution/docs_lite`
  - baseline
  - structural review only
`security_review`
  - baseline
  - security inventory
  - secret indicators
  - entrypoint indicators
`reviewer`
  - baseline
  - Go test when `go.mod` 存在
  - docs review when可见 Markdown 存在

## 4. provider modes

当前 provider mode 冻结：

- `builtin`
- `command`
- `openai-comp`
- `openai-response`
- `anthropic`

兼容别名：

- `script` -> `command`
- `responses` / `openai-responses` -> `openai-response`

`builtin`：

- 无需远端配置
- decision 仍由内建 policy bridge 决定
- coding repair 走本地 deterministic repair engine，共享同一条 runtime repair budget / artifact / guard 路径

`command`：

- 使用 `provider.command` 作为统一执行入口
- stdin JSON payload 取决于当前 operation：
  - `decision` -> `provider.Input`
  - `workspace_observation` -> `provider.WorkspaceObservationInput`
  - `workspace_edit` -> `provider.WorkspaceEditInput`
- runtime 会额外注入：
  - `NGEN_PROVIDER_MODE=<canonical mode>`
  - `NGEN_PROVIDER_OPERATION=decision|workspace_observation|workspace_edit`
- stdout 必须返回对应 schema 的单个 JSON object；runtime 会做同样的 schema-level 校验

`openai-comp` 走 OpenAI-compatible Chat Completions：

- endpoint: `<base_url>/chat/completions`
- auth: `Authorization: Bearer $APIKeyEnv`
- output contract: `tools[].function.parameters=<decision/edit/observation schema>`

`openai-response` 走 OpenAI Responses API：

- endpoint: `<base_url>/responses`
- auth: `Bearer $APIKeyEnv`
- output contract: `text.format.type=json_schema`
- 语义边界：当前 Responses path 是 structured JSON text decision，不是实际 tool-call envelope；durable diagnostics 会记录 provider response id 与 invalid JSON raw excerpt，但不会产生 `tool_call_id`
- auto loop 会对返回 decision / workspace edit / workspace observation payload 做 schema-level 校验
- invalid JSON、截断或 schema 校验失败必须把 exact provider error surface 返回到 runtime；Responses 解析失败至少包含 `response_id=...`（若 provider 返回）与 `raw_excerpt=...`，方便复盘截断和格式漂移

`anthropic` 走 Anthropic Messages API：

- endpoint: `<base_url>/messages`
- auth: `x-api-key: $APIKeyEnv`
- required header: `anthropic-version: 2023-06-01`
- output contract: `tools[].input_schema=<decision/edit/observation schema>`
- request cache contract: Anthropic request bodies serialize the system prompt and user prompt as text content blocks with provider-native `cache_control={"type":"ephemeral"}`. The system block gives a stable cache breakpoint over tool schema + system instructions; the user prompt is split at the stable context marker such as `Task context JSON:` / `Workspace edit context JSON:` and then, when possible, before the first operation-specific high-churn JSON field. This lets the instruction prelude and lower-churn artifact prefix be cached separately from volatile runtime tails such as `state`, `recent_verification`, workspace `collection` / `files`, or mission `root_status`. The split is request shaping only: concatenated text blocks must preserve the exact prompt text and must not change runtime actions, schemas, verifier/review gates, or artifact truth.

### 4.1 provider prompt / description contract

当前 provider prompt / tool description 是 runtime contract 的一部分，但只约束远端模型如何填写已有 schema，不新增 harness 流程：

- decision prompt 的职责是选择 **exactly one** runtime action。它必须把 task/state/criteria/verification/review/worker/session/continuity/sprint/project/memory artifacts 作为 system of record；不得假装执行命令、编辑文件、调用隐藏工具或创建隐式状态。
- decision action selection 必须偏向 smallest safe action：能 `run|resume|review` 推进时不退回 `noop`；缺证据时显式 `block|approval_request|wait|task_update|project_update`，而不是把 blocker 藏在 summary 中。
- workspace observation prompt 只允许返回少量 read-only argv inspection commands。上下文足够时应返回 zero commands；不得请求 build/test/format/install/migration/generator/package-manager/network/write 类命令。
- workspace edit prompt 只允许返回 bounded coding repair plan：优先 patch-first、root-cause、relative paths、当前 criterion/sprint/project focus 内的小改动；上下文不足时返回 no file changes 并依赖 observation phase，而不是猜测文件内容。
- Chat Completions 与 Anthropic 的 tool description 必须承载同样语义，因为这些 description 会直接进入模型可见 tool prompt；Responses path 仍走 `text.format=json_schema`，所以同样语义放在 instructions + user prompt 中。

## 5. approval model

当前 approval / permission model 冻结：

- `approval_request`
- `approval_decision`
- `permission_mode_id=standard|yolo`
- `permission.benchmark_integrity_mode=true` 用于 Terminal-Bench / leaderboard evaluation 等需要防 reward-hacking 的运行；它不会改变普通 verifier lane，但会让 repair command lane 在 `standard` 或 `yolo` 下都拒绝网络访问、shell/interpreter wrapper、package-manager fetch、Git remote、path-based repo script、`go mod download` / `go get` / `go install` / `go generate` 等 open-world 命令，并把 `policy_decision` 记录为 `denied_benchmark_integrity`
- optional `owner_task_id` / `owner_worker_id` for worker-child approvals

`yolo` 会自动批准 approval request，但仍保留 durable approval records。
