# 架构不变量与故障边界

## 1. 当前架构不变量

- 每个 task 都有稳定 `task_id`
- `state.json` 是 phase/state 的唯一 canonical owner
- 每个重要状态迁移都要写 event
- 每次 verifier / review 运行都必须写 report
- 每次 completion claim 都必须绑定 done gate 结果
- 每条 success criterion 都必须能回链到 criteria/completion truth
- review report 必须从 artifact refs、changed paths 与 worker runtime refs 组装输入；worker-backed criterion 不能只靠 child prose 或 worker contract 关闭 parent completion
- mission `done` 必须能回链到 validation contract、latest validation run 与 root task completion truth
- 不能有重要状态只存在于进程内存或自由文本聊天历史中
- watch 唤醒必须走同一 runtime `resume` 路径
- scheduler lease 只是协调机制，不是新的业务真相 owner
- CLI 与 `--json` 对同一 task 的关键状态语义必须一致

## 2. 当前故障边界

### 可自动收敛

- verifier failure -> `Failed/failed_verification`
- unreadable `state.json` but recoverable `task.json` -> `Failed/failed_state`
- due watch -> `Waiting` 唤醒后重新走 `resume`

### 需要人工或明确决策

- approval pending / denied
- 缺少环境值或输入
- review blocker
- mission validation blocker
- durable task root 本身不可恢复

### Mission 边界

- mission validator 必须保持 evidence-first：缺 root task state、criteria、completion 或 accepted gate 时只能写 blocking finding。
- validator 不拥有 workspace 写权限；需要修复时应通过 root task、feature/fix task 或 worker contract 继续推进。
- 同一 milestone 的写 owner 仍是绑定的 task/worker workspace owner；mission artifact 只记录 contract、coverage 与 validation truth。
