# 控制原语与 workspace memory

## 1. 当前 active control primitives

当前实现至少冻结以下 durable control primitives：

- `watch`
- structured input request
- session prompt
- bounded worker contract
- mission open/create/validate
- workspace memory promote

当前 watch 的最小合同：

- 稳定 `watch_id`
- `task_id`
- `status`
- `interval_seconds`
- `reason`
- `next_wake_at`
- `created_at`
- `updated_at`

当前不冻结 cron、wake_condition、loop definitions 或 aside records。

当前 structured input request 的最小合同：

- 稳定 `request_id`
- append-only `input_requests.jsonl`
- pending / answered status
- task 进入 `Blocked/blocked_missing_input` 时回链到对应 request record

当前约束：

- 同一 task 同时只允许一个 pending input request
- response 只能关闭一个已存在的 pending request，而不是隐式覆盖

## 2. 当前 watch 语义

典型场景：

- 等 CI
- 等外部 review
- 等固定时间窗口

当前约束：

- 同一 task 同时只允许一个 active watch
- 新 watch 注册时，旧 active watch 必须先被取消或替换
- task 进入 `Waiting/waiting_watch` 后，其 `status_detail_ref` 必须回链到对应 watch artifact
- scheduler 恢复后仍复用同一 task runtime，而不是新开隐式 session

## 3. 当前 scheduler surface

Foundation v0.1 当前只冻结：

- `ngen watch set`
- `ngen watch ls`
- `ngen watch cancel`
- `ngen scheduler tick --once`

当前不冻结长驻 `scheduler run`。

## 4. 当前 input surface

Foundation v0.1+ 当前只冻结：

- `ngen input request`
- `ngen input ls`
- `ngen input respond`

当前 ACP bridge 也冻结：

- `input.request`
- `input.list`
- `input.respond`
- mutating `session.*` / `task.*` / `input.*` / `worker.*` 调用在 response 后可追加 `ngen.notification`

`session.prompt` 当前也有一个明确的 bounded contract：如果同一轮 prompt 通过 provider decision materialize 了一个 durable workspace task（`task_create`），runtime 会在该单次 create 后立刻停止本轮 auto continuation，并把结果回给 operator / ACP。这样一个人类 prompt 不会静默扇出多个 durable task。

当前实现还额外冻结一条 provider-drift guard：显式 operator slash command（例如 `/run`、`/resume`、`/review`、`/worker_spawn ...`、`/worker_continue ...`、`/memory ...`、`/mission <prompt>`、`/missions <prompt>`、`/goal <prompt>`、`/goals <prompt>`）必须先由 runtime/provider 公共解析层或 runtime-owned compact command handler 规范化，再决定是否进入远端 decision driver。也就是说，这类命令不能只靠远端模型“最好能听懂”，否则 TUI/terminal 的 operator intent 会在 provider 切换后变得不可靠。`/mission`/`/missions` 不带 prompt 时只为当前 task 打开或创建 mission artifacts，并返回 compact status；带 prompt 时会直接把后续普通文本设为当前 task 的 mission/goal objective，自动推导 title 与默认 evidence-backed criterion；它不暴露 raw task/worker console。

更复杂的 multi-question elicitation、long-lived subscription delivery 与 UI forms 仍属于 richer hardening。

## 5. 当前 memory 边界

workspace memory 与 `MEMORY.md` 已进入 active contract。当前实现已经支持：

- `Done` 后自动 promote 到 `.ngen/memory/entries.jsonl`
- operator `memory promote TASK-ID --summary ...`
- ACP `memory.promote`
- provider `memory_promote` action，且在 task 仍为 `Active` 时不吞掉同一次 auto pass 的后续执行机会
- summary redaction
- additive entry metadata：`scope`、`paths`、`profiles`、`provider_modes`、`confidence`、`freshness_status` 与 `last_validated_ref`
- path-scoped freshness refresh：`MEMORY.md` / provider-visible `workspace_memory` 会把消失 path 对应的 entry 标记为 stale
- `MEMORY.md` 中的 recent memory entries
- 轻量 consolidated topics

更复杂的 extraction、asides、supersession/conflict adjudication、路径 ownership 变更判断、reviewer stale-memory gate 与 recurring loops 仍属于 richer hardening。
