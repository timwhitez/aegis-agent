# Go CLI Agent Multi-Agent And Isolation Spec

> 当前定位：large-project profile 规格。该文档描述的能力仍不属于 minimal core 的默认帮助面，但需要有真实运行和隔离证据，不能只停留在兼容壳。

## 1. 目标

在保持 `core runtime` / `sdk facade` / `cli adapter` 分层不变的前提下，后续扩展 phase 可支持：

- parent session 派生 child agent
- child agent 的独立 session 持久化
- 可选 worktree / workspace copy 隔离
- CLI 和 tool 两个入口复用同一套 delegation 契约

本阶段仍然坚持“模型是 agent，harness 提供环境”的边界：

- harness 负责 child 生命周期、隔离、状态追踪
- 不在 runtime 内强塞固定 DAG
- parent 是否分工、何时等待、何时汇总，仍由模型或调用方决定

## 2. 术语

### 2.1 parent session

发起 delegation 的 session。

### 2.2 child session

通过 delegation 创建的下级 session，拥有自己的：

- `session.json`
- `state.json`
- `messages.jsonl`
- `events.jsonl`
- `todo.json`
- `tasks/`

### 2.3 root session

一棵 delegation 树的根 session。所有 child session 都必须记录 `root_session_id`，便于聚合查询。

### 2.4 isolation

child session 使用独立工作目录执行。当前支持：

- `off`
- `auto`
- `git`
- `copy`

## 3. 元数据扩展

`session.json` 新增以下字段：

- `parent_session_id`
- `root_session_id`
- `agent_name`
- `agent_role`
- `depth`
- `requested_workdir`
- `queue_job_id`
- `isolation`

其中 `isolation` 结构：

```json
{
  "mode": "git",
  "requested_mode": "auto",
  "parent_workdir": "/repo",
  "workdir": "/root/.go-cli-agent/_worktrees/20260319-120000-ab12cd",
  "root_dir": "/root/.go-cli-agent/_worktrees",
  "git_repo_root": "/repo"
}
```

约束：

- root session 的 `root_session_id` 等于自身 `id`
- child session 的 `root_session_id` 继承 parent
- `depth` 从 `0` 开始递增

## 4. delegation 入口

### 4.1 CLI

新增命令：

- `go-cli-agent experimental delegate <parent-session-id> [prompt]`
- `go-cli-agent experimental children <session-id>`

`delegate` 高频参数：

- `--agent`
- `--role`
- `--provider`
- `--model`
- `--workdir`
- `--system`
- `--background`
- `--json`
- `--timeout`
- `--isolation`
- `--isolation-root`

### 4.2 built-in tools

新增 built-in tools：

- `agent_spawn`
- `agent_status`
- `agent_list`

这些工具当前默认注册到 session tool list，让 master agent 自己决定是否需要新建 child agent；若部署方显式设置 `runtime.multi_agent.enabled=false`，则不注册到 tool list。

#### `agent_spawn`

输入：

- `prompt`
- `agent_name?`
- `agent_role?`
- `provider?`
- `model?`
- `workdir?`
- `system?`
- `background?`
- `mode?`
- `isolation_mode?`
- `isolation_root?`

行为：

- 默认从当前 session 派生一个 child session
- 未显式提供 `provider` / `model` 或传入 `default` 时，child 默认继承当前 parent session 的 provider / model
- 默认 `mode=exec`
- 默认 `isolation_mode=auto`
- `mode=full-auto` 作为兼容别名按 `exec` 处理
- `isolation_mode=workspace-write` 作为兼容别名按 `off` 处理
- 工具可见不代表 runtime 会自动 delegation；是否调用由当前 master agent 自主决定
- `background=true` 时提交到后台自治队列
- 当 parent 处于活跃 `run` / `exec` 中时，后台 child 默认由同一 CLI 进程内的 auto worker 自动拉起执行
- child 完成或失败后，结果需要回投到 parent session 的控制通知，供下一安全边界自动并入上下文
- `background=false` 时同步执行 child session，直到 child 到达稳定状态
- `agent_role` 允许显式声明 `planner` / `generator` / `evaluator`，并优先于 `agent_name` 推断；该 role 需要在 child session 元数据与后续队列/通知事实中保持可追踪

#### `agent_status`

输入：

- `session_id?`
- `queue_job_id?`

行为：

- 读取 child session 或后台 job 的当前状态
- 返回最终摘要、失败信息、有效工作目录

#### `agent_list`

输入：

- 空

行为：

- 列出当前 parent session 的 child sessions
- 额外列出尚未落成 child session 的 queued jobs

## 5. workdir 选择规则

### 5.1 requested workdir

delegation 请求中的 `workdir` 表示逻辑源工作目录。

若未指定：

- parent 已记录 `requested_workdir` 时继承它
- 否则继承 parent 的当前 `workdir`

### 5.2 effective workdir

runtime 真正执行时使用的目录。

规则：

- `off`: `effective workdir = requested workdir`
- `git`: 在独立目录创建 detached git worktree
- `copy`: 复制工作区到独立目录
- `auto`: git repo 用 `git`，否则退化为 `copy`

### 5.3 isolation root

默认放在 user-scoped root 下，并且必须位于 source workdir 外部：

```text
~/.go-cli-agent/_worktrees/<session-id>/
```

也允许通过 CLI / tool 显式覆盖。

约束：

- 若显式配置的 `isolation_root` 位于 source workdir 内部，应直接返回错误
- legacy 的 workspace-local 形状（例如 `.go-cli-agent/_worktrees`）不再作为默认推荐值，因为 parent workdir 指向仓库根目录时会与“不得在源目录内部创建隔离目录”的约束冲突

## 6. git worktree 契约

`git` 模式要求：

- `requested_workdir` 位于 git repo 内
- 使用 `git worktree add --detach`
- 不修改 parent worktree

若仓库不存在 git 元数据：

- `auto` 退化为 `copy`
- 显式 `git` 模式返回错误

## 7. copy 模式契约

`copy` 模式要求：

- 保留目录结构
- 复制普通文件
- 尽量保留 symlink 形状
- 不把隔离目录创建在源目录内部，避免自复制递归

## 8. child 生命周期

### 8.1 同步 child

同步 child 的返回状态沿用普通 session 语义：

- `completed`
- `awaiting_input`
- `paused`
- `failed`

parent 通过 tool 或 CLI 获取结构化结果，不直接复用 child 的 stdout。

### 8.2 后台 child

后台 child 先创建 queue job，再由 worker 启动。

顺序：

1. 创建 queue job
2. worker claim job
3. worker 创建 child session
4. queue job 写回 `session_id`
5. queue job 根据 child 最终状态进入 `completed` 或 `failed`
6. worker 向 parent session 写入一条 background notification

## 9. 深度和安全边界

### 9.1 depth limit

默认 `enabled = true`，`max_depth = 4`。默认暴露 `agent_spawn` / `agent_status` / `agent_list`，operator 可通过 `runtime.multi_agent.enabled=false` 显式收窄。

超过时：

- 拒绝新的 delegation
- 返回结构化错误

### 9.2 状态追踪

parent-child 关系必须落在文件事实中，不能只留在内存里。

### 9.3 worktree 生命周期

默认不自动删除 child 隔离目录，避免丢失排障现场。清理由调用方自行决定。

## 10. 验收标准

- child session 可以同步执行并返回结构化结果
- parent / root / depth 元数据正确落盘
- `auto` isolation 能在 git repo 和非 git 目录下正确分流
- `children` 与 `agent_list` 能同时看到已落盘 child 和未启动完成的 queued jobs
