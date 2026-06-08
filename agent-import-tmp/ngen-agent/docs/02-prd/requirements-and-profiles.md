# 需求与 profile 矩阵

## 1. 当前生效的功能需求

### Task And State

- FR-001: 系统必须持久化 `task.json`，至少包含 objective、success criteria、constraints、profile/preset 与 workspace root。
- FR-002: 系统必须持久化 `plan.json`，至少包含 system lane step、状态、coverage、verifier expectation，以及 mutable execution lane 的 explanation / current-step pointers。
- FR-003: 系统必须持久化 `state.json`，拥有 `phase`、`state`、`status_reason_code`、`status_detail_ref` 与最近关键 refs。
- FR-004: `run` / `resume` 必须从 durable artifacts 恢复，而不是依赖进程内状态。
- FR-005: `Done` 必须经过 done gate；缺 verifier evidence、review clearance 或 handoff 时必须拒绝完成。
- FR-006: 系统必须显式支持 `Blocked`、`Waiting`、`Failed`、`Aborted`。

### Baseline, Verification And Review

- FR-010: 离开 `Explore` 前必须写出 `baseline.json`。
- FR-011: verifier failure 与 blocker summary 必须保留 exact failure strings 或其稳定摘要，不能只保留“failed”。
- FR-019: 系统必须支持 foundation verifier pipeline。
- FR-020: `coding` 至少运行 Go repo 下的 `go test ./...`。
- FR-021: `general_execution/docs_lite` 至少运行 baseline 与 docs structural review。
- FR-022: review lane 必须能阻塞 unsupported completion claims。
- FR-023: 每次 verifier 运行都必须持久化 report。
- FR-024: 每次 review 都必须持久化 report；如有阻塞问题，还必须写入 `findings.jsonl`。
- FR-030: 系统必须生成 evidence-backed `handoff.md`。
- FR-040: 每次 completion claim 都必须写入 `completion/latest.json`。
- FR-041: `criteria/latest.json` 必须逐条记录 criterion 状态、`passes`、当前 focus、recent summary 与 evidence refs；`criteria/history.jsonl` 必须保留 append-only refresh history。
- FR-041A: `sprint/latest.json` 必须记录当前 sprint 的 primary criterion、deferred criteria、completion signals、working set 与当前 execution/gate pointers；`sprint/history.jsonl` 必须保留 append-only refresh history。
- FR-042: review / criteria / handoff blocker 必须收敛为 `Blocked/blocked_review`，不得伪装成 `blocked_missing_input`。

### Watch And Scheduler

- FR-025: 系统必须支持 durable `watch`，并通过 `scheduler tick --once` 触发 due items。
- FR-026: `Waiting` 必须由 watch artifact 驱动，而不是依赖 session-local timer。
- FR-043: foundation v0.1 同一 task 同时只允许一个 active watch；新 watch 注册时必须取消或替换旧 active watch。

### Operator Surface

- FR-029: 系统必须提供 `task create|list|get|update`、`project get|update|patch`、`mission create|get|status|plan|approve|validate|run|pause|resume`、`run`、`resume`、`auto`、`status`、`review`、`events tail`、`watch`、`scheduler tick --once`、`approval`、`input`、`worker`、`memory`、`acp serve` 与 `terminal` 命令。
- FR-045: `run` / `resume` / `events tail` 的 `--json` 只输出 JSONL events；`status --json` 输出单个稳定 JSON object。
- FR-049: 外部 JSON contract 必须有 owner doc；不得在无文档的情况下改字段或改名。
- FR-050: 系统必须支持 session bridge：start、list、read、prompt、cancel。
- FR-051: `auto` 必须通过 provider adapter 决定下一动作，但 provider 不得反向拥有 task truth。
- FR-057: provider adapter 必须至少支持 builtin、command、OpenAI-compatible Chat Completions、OpenAI Responses 与 Anthropic Messages，并对 decision 输出做 schema-level 校验。
- FR-052: 系统必须支持 bounded worker contract，至少能 spawn/list/sync。
- FR-053: 系统必须支持 workspace memory promote：任务 `Done` 时自动 promote，同时也允许显式 `memory promote` / `memory.promote` / provider `memory_promote` 在 active task 中追加 reusable memory entry。
- FR-054: 系统必须支持 pre-run / post-run / on-done hooks，并在失败时显式报错。
- FR-055: baseline refs 必须受 additional roots / visibility deny rules 过滤。
- FR-062: 系统必须水合 `.ngen/roles/<role_id>.json` role contracts，并在 provider decision dispatch 前按 `allowed_provider_actions` 与 `allowed_worker_roles` 做显式 capability gate。
- FR-058: ACP 必须提供 `initialize` / `rpc.ping`，并对 invalid request / invalid params / method not found 返回稳定 JSON-RPC 错误码。
- FR-059: 系统必须支持 structured input request lifecycle，至少包含 request/list/respond，并把 `blocked_missing_input` 回链到 durable input-request artifact。
- FR-060: mission mode 必须写出 workspace-level validation contract、stable assertion ledger、feature/milestone coverage records 与 append-only validation runs；mission run 必须要求 plan approval 与 assertion coverage 先通过 deterministic gate；mission validator 必须以 root task artifacts 为证据，不能只信任执行者 prose。
- FR-061: `/mission [PROMPT]`、`/missions [PROMPT]`、`/goal PROMPT` 与 `/goals PROMPT` 必须作为 terminal/TUI compact entrypoint 打开或创建当前 task 的 mission state；带 prompt 时必须直接设置当前 task 的 mission/goal objective；默认 TUI 不得因此变成 mission/task/worker 管理控制台。

### Approval And Failure Boundaries

- FR-027: 系统必须支持最小 approval artifact 生命周期：request、approve、deny。
- FR-028: `blocked_missing_input`、`blocked_policy` 与 `blocked_review` 必须分开；审批不属于缺输入路径，review blocker 也不属于缺输入路径。
- FR-056: `permission_mode_id=yolo` 时，approval request 必须自动批准并留下 durable approval records。

### Delivery Discipline

- FR-031: 每个 success criterion 都必须能回链到 evidence refs。
- FR-032: artifact schema、CLI contract、状态码与 verifier 语义变更必须同步更新 owner docs。
- FR-033: 默认采用 narrow-first TDD。
- FR-035: 完成声明必须按 criterion 粒度解释“由什么证据满足”。

## 2. 当前 richer hardening backlog

以下需求仍可继续加强，但不再属于“尚未开发”的 deferred surface：

- broader provider integrations，超出 builtin/command、OpenAI-compatible Chat Completions、OpenAI Responses 与 Anthropic Messages 当前范围
- deeper ACP protocol compatibility
- richer role-file inheritance / role discovery UX / hook schemas，超出当前内建 role contract 水合与 provider action gate
- deeper visibility / memory governance
- stronger profile-specific verifier semantics

## 3. 非功能需求

- NFR-001 Local Operability: 单机可运行。
- NFR-002 Recoverability: 进程退出后 durable task record 不丢失。
- NFR-003 Observability: 关键状态切换必须有结构化 event。
- NFR-004 Stable State Shape: artifacts 与 JSON output 必须稳定、易 diff。
- NFR-005 Explicit Failure: `Failed`、`Blocked`、`Waiting` 不能被自由文本替代。
- NFR-006 Low Dependency: 优先 Go 标准库与窄依赖。
- NFR-007 Human Legibility: 只看 `.ngen/` 即可理解任务状态。
- NFR-008 Validator Independence: mission validation findings 必须带 evidence refs，validator 不应静默承担 broad write-capable implementation。

## 4. 当前 profile 矩阵

| Profile | 当前状态 | 默认写权限 | 默认 verifier | 完成门槛 |
| --- | --- | --- | --- | --- |
| `coding` | active | workspace 内 | baseline + `go test ./...` + review | verifier 通过 + review 无阻塞 + handoff |
| `general_execution/docs_lite` | active | 否 | baseline + docs structural review | verifier 通过 + review 无阻塞 + handoff |
| `security_review` | active | 否 | baseline + security inventory + secret/entrypoint indicators + review | verifier 通过 + review 无阻塞 + handoff |
| `reviewer` | active | 否 | baseline + Go test + docs review when available + review | verifier 通过 + review 无阻塞 + handoff |

补充说明：

- 当前代码已实现四条 profile。
- profile-specific semantics 仍可继续加强，但不能再把后两条写成 deferred。
