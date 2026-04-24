# Go CLI Agent Live Input And Steering Spec

## 1. 目标

`go-cli-agent` 需要尽量支持“运行中插入 prompt”。

但因为 v1 是 CLI harness，不是常驻 app-server，也不绑定某一家 provider 的 native steer API，所以设计目标是：

- 先把 CLI 语义做稳定
- 用文件控制队列实现跨进程 steer
- 用 queue-first + best-effort interrupt 兼顾安全与实时性

## 2. 设计来源

本设计综合以下模式：

- `codex`
  - `turn/start`
  - `turn/steer`
  - `turn/interrupt`
- `codex` 旧协议
  - additional user-turn input 可以终止当前 task 并开始新 task
- `opencode`
  - 对 active session 再次提交 `prompt`
  - `prompt_async`

## 3. 命令面

### 3.1 `go-cli-agent steer <session-id> --message "..."`

默认行为：

- 把新输入追加到 session 控制队列
- 由 active runner 在最近安全边界接纳
- 空消息或超过长度上限的 steer 输入必须在入队前直接拒绝

### 3.2 `go-cli-agent steer <session-id> --message "..." --interrupt`

增强行为：

- 请求 runtime 尽快抢占当前执行
- 若 provider 调用可取消，则优先取消 provider 请求
- 若工具支持 `context.Context` 取消，则中断该工具
- 否则自动退化为 queue-first

## 4. 存储布局

```text
.go-cli-agent/sessions/<session-id>/
  control/
    steer.jsonl
```

单条 steer 记录：

```json
{
  "id": "steer_20260319_120001_ab12",
  "created_at": "2026-03-19T12:00:01Z",
  "source": "cli",
  "text": "Actually focus on failing tests first.",
  "interrupt": true,
  "status": "pending"
}
```

状态：

- `pending`
- `accepted`
- `deferred`
- `rejected`

说明：

- `source` 当前可取 `cli` 或 `web`
- 无论入口来自 CLI 还是 Web，最终都必须落到同一个 `control/steer.jsonl` 文件事实中

## 5. Runner 行为

active `run` / `exec` 进程启动后，需要额外启动一个 control watcher：

- 轮询 `control/steer.jsonl`
- `poll_interval_ms` 默认 `250`
- 新记录到达后触发内存中的 steer channel

## 6. 接纳时机

### 6.1 安全边界

以下位置属于安全边界：

- provider 请求发起前
- provider 响应完整落盘后
- 一批 tool results append 后
- compaction 完成后

默认 queue-first 模式下，只在这些边界接纳 steer。

### 6.2 抢占点

`--interrupt` 可尝试在以下位置抢占：

- 正在等待 provider HTTP 响应
- 正在执行支持取消的工具

若当前阶段不支持安全抢占：

- 写 `session.steer.deferred`
- 等待最近安全边界

## 7. Message 语义

当 steer 被接纳时：

- runtime 新增一条真实 `user` message
- `meta.source = steer`
- `meta.interrupt = true|false`
- runtime 基于最新外部指令刷新 `contract.json` / `artifact-tracker.json`，使新的显式 artifact、template、literal 或目标约束能参与后续 completion gate

若在同一边界接纳了多条 steer：

- 按时间顺序逐条写入
- 不合并成一条大消息

### 7.1 与 runtime reminder / guard 的关系

- steer 本身仍然是最高优先级的外部 user input
- runtime 可以基于最新已接纳 steer 生成额外的 harness reminder
  - 例如：
    - 最新 interrupt steer 明确要求“use current evidence”
    - 最新 interrupt steer 明确要求“write artifact now”
    - 最新 interrupt steer 明确要求“finish”
    - 最新 interrupt steer 明确要求“先刷新产物，但不要 finish，留给后续 continue 收尾”
    - 最新 interrupt steer 明确改变了大型任务的目标或优先级，此时 runtime 可以要求先刷新 `reports/spec.md` / `reports/plan.md`，再继续实现、handoff 或 finish
  - runtime 可以先写一个 completion-oriented reminder；如果 steer 之后仍出现 bookkeeping detour 或被 guard 阻断的偏航，再写一个更强的 escalated reminder
  - 若 escalated reminder 之后仍未出现交付动作，runtime 可以继续重复该 escalated reminder，直到出现 artifact / finish 或新的外部指令
- 这些 harness reminder 必须写入 `messages.jsonl`
  - `role = user`
  - `meta.source = harness_reminder`
- 若 steer 之后模型仍持续做只读探索，runtime 可以对继续的 read-only tool call 写入 guard 错误结果，强制把执行拉回到写入、任务更新或 `finish`
- 若最新 interrupt steer 已经明确要求“立即写产物 / finish”，runtime 还可以阻断继续的 `todo_write` / `task_*` / `load_skill` 这类 completion detour，避免多耗一个 bookkeeping turn
- 若最新 interrupt steer 明确要求“不要 finish，留给 later continue / resume 收尾”，runtime 必须抑制 finish-oriented reminder，并在该 run 内阻断 `finish`，确保 session 仍保持可恢复
- 这类 guard 应优先把模型拉回 `write_file` / `edit_file` / `finish`，而不是继续 repo-scale overread
- 这类 guard 不能吞掉 steer 本身，也不能代替新的外部 user message；新的 steer / continue / background input 一旦到达，应重新计算优先级

## 8. 与 `continue` 的区别

### 8.1 `continue`

用于：

- `paused`
- `awaiting_input`
- `failed`

### 8.2 `steer`

用于：

- `running`

若 session 不在运行：

- `steer` 返回错误
- 提示使用 `continue`

## 9. 与 `Esc` 的关系

- `Esc` 仍然是“明确暂停当前运行”
- `steer` 是“尽量不中断任务地追加新输入”
- 当用户明确想掌控切换上下文时，用 `Esc`
- 当用户只是想插入新的约束、补充、修正时，用 `steer`

## 10. v1 范围边界

v1 只把外部 `go-cli-agent steer` 作为标准入口：

- 语义稳定
- 易测试
- 不把 CLI 做成伪 TUI

inline steer hotkey 可以作为后续增强项，但不作为当前实现前提。

## 11. 事件模型

每条 steer 至少产生以下事件：

- `session.steer.requested`
- `session.steer.queued`
- `session.steer.accepted`

可能额外产生：

- `session.steer.deferred`
- `session.steer.rejected`
- `session.steer.interrupt_requested`
- `provider.cancelled`

## 12. 失败语义

### 12.1 provider 抢占成功

- 当前 provider turn 被取消
- 写 `provider.cancelled`
- 写 `cancelled`
- steer 触发新的 turn

### 12.2 tool 抢占成功

- 当前 tool 写中断错误结果
- steer 在下一 turn 生效

### 12.3 tool 不可抢占

- steer 变为 deferred
- 当前工具结束后再生效

## 13. 验收标准

- 外部 `steer` 命令可在 session 运行期工作
- queue-first 模式可在安全边界接纳 steer
- `--interrupt` 可对 provider 调用产生真实抢占
- provider 抢占成功时要留下可检索的 `provider.cancelled` durable event
- 不可安全抢占时会 defer，而不是静默丢失
- 明确要求“stop without finishing / later continue”的 interrupt steer 不会被误判成“finish immediately”，并且当前 run 结束后 session 仍可 `continue`
- steer 最终进入 `messages.jsonl` 的形式是普通 user message，而不是只停留在控制文件中
- 已接纳 steer 若改变交付物或 completion 条件，`contract.created` / `contract.updated` 与 artifact tracker 需要同步反映，不允许只靠旧 prompt view 判断 finish
