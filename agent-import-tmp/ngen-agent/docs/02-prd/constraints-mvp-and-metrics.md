# 产品约束、MVP 与指标

## 1. 当前产品约束

- 单二进制优先。
- 文件系统优先。
- 只做 foundation slice，不实现完整自治产品。
- 只做 `coding` 与 `general_execution/docs_lite`。
- CLI + headless JSON 优先。

## 2. 当前 MVP

当前 MVP 达成条件：

1. `task create -> run -> status` 可用。
2. `.ngen/` 能解释当前状态、最近 verifier、review、handoff 与 completion。
3. `Done` 在缺 verifier 或 handoff 时会被拒绝。
4. `Waiting` 能通过 `watch` + `scheduler tick --once` 恢复。
5. `Failed/failed_state` 与 `Failed/failed_verification` 都有 durable 记录。

## 3. 当前成功指标

- task 创建与恢复成功率
- `Done` gate 拒绝 false claim 的比例
- verifier 失败诊断可读性
- `Waiting` task 被成功唤醒的比例
