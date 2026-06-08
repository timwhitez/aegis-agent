# 可观测性与 surface parity

## 1. 当前可观测性要求

当前实现至少要暴露：

- 当前 `phase`
- 当前 `state`
- 当前 `status_reason_code`
- 最近关键 refs
- verification / review / completion truth
- harness evaluation truth for provider/context/repair strategy after runtime passes
- quality diagnostic truth when non-clear or review-required
- mission contract / role plan / latest validation run truth when mission-bound
- watch / approval 相关 blocker detail
- session / worker / memory related refs when applicable

当前最低输出：

- `baseline.json`
- `events.jsonl`
- `criteria/latest.json`
- `completion/latest.json`
- `progress.md`
- `verification/latest.json`
- `reviews/latest.json`
- `harness/latest.json`
- `diagnostics/quality-latest.json`
- `.ngen/missions/<mission_id>/mission.json`
- `.ngen/missions/<mission_id>/validation_runs.jsonl`
- `handoff.md`（`Done` 必需）
- `session_snapshot`（ACP session when requested）
- `worker_snapshot`（ACP worker flow when requested）
- `acp_notification`（ACP mutating call when emitted）

## 2. 当前 active surfaces

当前冻结以下 surface：

- human-readable CLI
- headless JSON output
- ACP
- interactive terminal
- full-screen TUI

因此当前 parity 规则也只覆盖：

- CLI、ACP、terminal、TUI 与 `--json`
- `status_snapshot` / `session_snapshot` / `worker_snapshot`
- JSONL event stream
- `acp_notification`

## 3. 当前 parity 规则

- 同一 task 的 `phase/state/status_reason_code/status_detail_ref` 语义在 human-readable CLI 与 `status --json` 中必须一致
- `auto --json` / `run --json` / `resume --json` / `events tail --json` 必须输出结构化 event objects，而不是自由文本日志
- CLI `events tail --after`、ACP `task.events`、web JSON `?after=` 与 SSE `Last-Event-ID` / `?after=` 必须以同一份 append-only `events.jsonl` 的 `event_id` 为 cursor，只输出该 cursor 之后的 events；缺失 cursor 必须暴露 explicit diagnostic，不能静默退回 latest tail。
- `status --json` 必须稳定暴露 `task_id`、`phase`、`state`、`status_reason_code`、`status_detail_ref` 与关键 refs
- mission-bound task 的 `status --json` 必须额外暴露 `mission_id`、`mission_ref`、`mission_status`、`mission_current_milestone_id` 与最新 validation ref；这些字段是 mission artifacts 的只读指针，不替代 task-local state/review/completion truth。
- `status --json.restore_clues` 必须从最新 checkpoint / baseline command hints 派生，给出 checkpoint ref、git bearings、changed paths 与 repo-owned command hints；handoff 的 Resume Instructions 也必须渲染同一类 restore clue。
- `review --json` 必须返回与 `reviews/latest.json` 一致的 evidence-first report，包括 reviewer profile、review context refs、changed paths、worker result refs、risk summary、blocking categories 与 blocking finding refs
- `harness eval TASK-ID --json` 必须读取同一 task 的 `harness/latest.json`，暴露最近一次 `run`、`resume`、`auto` 或 `review` pass 的 provider mode、model、context refs、repair budget、verifier/review/completion status 与 evidence refs；它不得重新运行 provider、verifier 或 review
- `mission status|approve|validate|run MISSION-ID --json` 必须读取 `.ngen/missions/<mission_id>/...` 与 root task artifacts，暴露 validation contract assertions、feature/milestone binding、plan approval state、role plan、latest validation run、validator provenance 与 root task status；`approve` 和 `validate` 的 deterministic pass 不得重新运行 provider，model-backed pass 只有在 validators role 显式配置且 deterministic gates 通过时才可通过 dedicated read-only validation schema 调用 provider，且不得直接修改 workspace 文件
- `memory show` 与 provider-visible `workspace_memory` 必须读取由 `entries.jsonl` 刷新的 `MEMORY.md`，并在 recent entry label 中暴露 freshness。path-scoped entry 指向的 workspace path 消失时必须标记为 stale；task-local artifacts、criteria、verification、review 与 completion truth 仍优先于 workspace memory。
- provider-visible `role_contract` 必须来自 `.ngen/roles/<role_id>.json`。若 role file 无效、provider action 不在 `allowed_provider_actions` 内，或 `worker_spawn.worker_role` 不在 `allowed_worker_roles` 内，runtime 必须返回显式错误，而不是把 action 静默改写为 noop 或回退到默认 role。
- 若 task 因 `failed_state` 恢复失败，`status --json` 必须仍能给出结构化 `Failed/failed_state` snapshot，而不是只打印不可解析错误
- `blocked_missing_input` 的 detail ref 必须稳定回链到 `input_requests.jsonl#input_record_id=...`
- approval 与 watch 的 detail ref 必须稳定回链到 artifact
- TUI header 与 inspector 必须直接反映同一 task 的 `phase/state/status_reason_code/status_detail_ref`，不得缓存出另一套 UI-only 状态语义
- TUI transcript 必须由 `events.jsonl` 与 `.ngen/sessions/*.messages.jsonl` 聚合得到；即使为了 viewport 性能做 tail/裁剪，也不得伪造 event/message truth
- TUI approval / input / worker controls 必须分别回落到 `DecideApproval`、`RespondInput` 与 `ContinueWorker`，并在动作后刷新到与 CLI 一致的 snapshot / artifact 结果
- ACP mutating 调用若发出 `ngen.notification`，必须在 response 之后追加，并保持稳定 `object_kind=acp_notification`
- ACP `worker.spawn` / `worker.list` / `worker.sync` / `worker.continue` 必须通过 `worker_snapshot` 暴露 parent/child status，而不是只返回裸 worker contract
- parent-owned worker approval 不得停留在 CLI 查询层；provider input、`session_snapshot` 与 `worker_snapshot` 都必须能直接暴露 owned approval / parent action metadata

## 4. richer hardening

跨请求订阅/重放语义、更复杂的多客户端 fanout，以及默认开启的 PTY smoke / richer snapshot automation 仍可继续加强。
