# 运行规则、依赖策略与参考边界

## 1. 当前错误模型

foundation 当前至少要稳定以下 failure families：

- `failed_verification`
- `failed_state`
- `blocked_policy`
- `blocked_review`
- `blocked_missing_input`
- `waiting_watch`

规则：

- 这些状态必须能从 artifacts 恢复，而不是只存在于 stderr 文本里
- verifier 或 review 的关键失败摘要必须进入 verification/review artifacts

## 2. 当前并发模型

- 一个 runtime service 拥有 task state transition
- artifact 写入必须避免部分写入损坏文件
- scheduler 通过 workspace lease 保证单 owner 扫描 due watch
- foundation 当前不实现多 worker 并发写入同一 workspace

## 3. 当前依赖策略

Foundation v0.1 当前只依赖：

- Go standard library

原则：

- 先把 contract、artifacts、tests 与 runtime 语义跑通
- 不为了未来 ACP / TUI / hooks / subagents 提前引依赖

## 4. post-v0.1 依赖候选

只有当对应工作包被正式提升为 active contract 后，才考虑引入：

- `github.com/sourcegraph/jsonrpc2`
  - ACP stdio server
- `github.com/charmbracelet/bubbletea`
  - interactive terminal / optional TUI
- `github.com/robfig/cron/v3`
  - 更丰富的 watch / loop schedule
- `github.com/fsnotify/fsnotify`
  - file-based wake conditions
- `github.com/cenkalti/backoff/v5`
  - provider / retry orchestration

这些依赖当前都只是路线图候选，不属于 foundation 必需项。
