# Aegis Agent Multi-Agent And Isolation Spec

> 当前定位：large-project profile 规格。该文档描述的能力不是默认 Web 页面里的必选工作流，但需要有真实运行和隔离证据，不能只停留在兼容壳。Web 控制台可以提供轻量入口与观测链接，细粒度 orchestration / isolation 调参仍可由 CLI 或 API 承担。

## 1. 目标

在保持 `core runtime` / `sdk facade` / `web service` / `cli adapter` 分层不变的前提下，后续扩展 phase 可支持：

- parent session 派生 child agent
- child agent 的独立 session 持久化
- 可选 worktree / workspace copy 隔离
- 可选、只读、低噪声的 explorer role/profile
- Web、CLI 和 tool 入口复用同一套 delegation 契约

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
- `tool_profile`：effective versioned capability profile；普通 role 为 `default`，explorer 为 `explorer-readonly-v1`
- `depth`
- `requested_workdir`
- `queue_job_id`
- `isolation`
- `effective_budget`：versioned child policy snapshot，包含 source、turn/time scope、limits、attempt、used/remaining 与 status
- `provider_options`：包含 role/parent/request 合并后的 effective reasoning/output 与其他 provider options

其中 `isolation` 结构：

```json
{
  "mode": "git",
  "requested_mode": "auto",
  "parent_workdir": "/repo",
  "workdir": "/root/.aegis-agent/_worktrees/20260319-120000-ab12cd",
  "root_dir": "/root/.aegis-agent/_worktrees",
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

- `aegis-agent experimental delegate <parent-session-id> [prompt]`
- `aegis-agent experimental children <session-id>`

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
- `agent_wait`
- `agent_stop`
- `agent_prompt`
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
- `resume_parent?`
- `isolation_mode?`
- `isolation_root?`

行为：

- 默认从当前 session 派生一个 child session
- 未显式提供 `provider` / `model` 或传入 `default` 时，child 默认继承当前 parent session 的 provider / model
- 默认 `mode=exec`
- 默认 `isolation_mode=auto`；`agent_role=explorer` 的空值或兼容 `default` 例外为 `off`
- `mode=full-auto` 作为兼容别名按 `exec` 处理
- `isolation_mode=workspace-write` 作为兼容别名按 `off` 处理
- 工具可见不代表 runtime 会自动 delegation；是否调用由当前 master agent 自主决定
- `background=true` 时提交到后台自治队列
- 当 parent 处于活跃 `run` / `exec` 中时，后台 child 默认由同一 CLI 进程内的 auto worker 自动拉起执行
- child 完成或失败后，结果需要回投到 parent session 的控制通知，供下一安全边界自动并入上下文
- `resume_parent=true` 只在 `background=true` 下生效，表示 master agent 明确选择本轮暂时停止推进，进入 `awaiting_input` / `background_wait`，等待任一后台 child 产生 durable notification 后由 harness 自动接纳结果并继续 parent loop；master agent 恢复后自行判断是否继续等待其他 child
- `background=false` 时同步执行 child session，直到 child 到达稳定状态
- `agent_role` 允许显式声明 `planner` / `generator` / `evaluator` / `explorer`；`agent_name` 只作为人类可读标签，不参与 role provider override 匹配；该 role 需要在 child session 元数据与后续队列/通知事实中保持可追踪
- 只有 root master session 可以创建 child agent。`depth > 0` 或存在 `parent_session_id` 的 child session 不允许再调用 `agent_spawn` 或提交 parent-linked queue job，避免 nested sub-agent 树把等待和恢复状态分散到多层 parent coordination 中

#### `agent_wait`

输入：

- `queue_job_id?`：兼容旧调用的可选提示字段，不作为唤醒过滤条件

行为：

- parent agent 显式选择暂时停车等待 background child work
- runtime 将 parent 进入 `awaiting_input` / `background_wait`，等待任一 pending background notification 后自动接纳结果并继续 parent loop
- parent 恢复后由模型基于注入的 `<background-agent-results>`、`agent_status` / `agent_list` 和 parent coordination 判断是否继续 `agent_wait`、提示其他 child 收敛或停止不需要的 queued job
- 如果同一 deadlock / liveness notification 已经被 parent 接纳且当前没有新的 pending background result，后续 `agent_wait` 不能再次静默停车；人工 / Web `continue` 旧 parked 或因 provider 错误转为 failed 的 parent session 时也应先复用同一判定。runtime 应把“等待不会再推进，需要 master 介入”的 reminder 写回 parent transcript，并继续 parent loop，让模型显式选择 `agent_prompt` / `agent_status` / `agent_list` / `agent_stop` 或 handoff
- queue job 的 `blocked` 是 worker 已退出执行边界后的稳定状态，必须视为需要 parent 介入；即使读取到旧版本遗留的 lease 字段，也不能据此把 blocked job 判成仍可自行推进
- 该工具不取消、不停止 child work；若 child work 不再需要，parent 必须先通过可用控制面解决该 work，再退出

#### `agent_stop`

输入：

- `session_id?`
- `queue_job_id?`

行为：

- parent agent 可显式取消自己名下的 foreground/background child，目标可按 child session 或 queue job 指定
- queued job 直接进入 `cancelled`；running child 先落盘 `cancel_requested`，再 cooperative cancel active provider/tool/hook/shell context；无本进程 handle 时 request 继续作为 durable fact，由 owner watcher 或 recovery safe boundary 接纳
- parent agent 也可显式停止并结算一个 linked child 已因 `child_budget_*_exceeded` 暂停的 blocked job；job 进入 `cancelled` terminal queue 状态并从 unresolved 集合移除，child 保留 paused/budget evidence
- runtime 写入 background notification，使 parent transcript 后续可看到该 job 已被停止
- execution failure 继续使用 `failed`；parent/operator cancellation 不得进入 failed counters/retry/alert 语义
- 重放同一 cancel request 必须幂等，不重复终止、通知或 terminal event；不允许只改 parent coordination 而没有真实 cancel request/terminal durable fact

#### `agent_prompt`

输入：

- `session_id?`
- `queue_job_id?`
- `message`
- `interrupt?`
- `budget_extension?`
  - `add_turns?`
  - `add_active_runtime_sec?`
  - `extend_deadline_sec?`
  - `clear_turn_limit?`
  - `clear_active_runtime_limit?`
  - `clear_absolute_deadline?`
  - `reason?`

行为：

- parent agent 可以向当前 parent 名下的 running child session 或已启动 / blocked 且可恢复的 background child job 发送一条 durable steer prompt
- prompt 通过 child session 的 `control/steer.jsonl` 进入现有 Live Steer 流程，最终以普通 user message 进入 child transcript，而不是引入第二套控制状态
- 对 running child，`agent_prompt` 走 steer；对已 linked direct child，只要 session 为可恢复的 `paused` / `awaiting_input` / `failed` 即可显式 continue；对 background child 还要求 linked queue job 为 `blocked`。恢复 direct failed child 时必须先把它从 parent 的 failed terminal bucket 重新移入 unresolved，若本次 resume 又停在 paused/awaiting_input 则继续保持 unresolved，只有新的 completed/failed/cancelled terminal fact 才再次结算。budget pause 与 cancel-requested pause 仍走各自专用控制边界
- 对 budget-paused child，普通 prompt 不足以恢复：parent 必须提供能解除已耗尽维度的 `budget_extension`。runtime 基于上一 attempt 的 remaining + extension 创建新 attempt，记录 previous/effective budget、parent、reason 和 audit event；未提供有效 extension 时明确拒绝
- `interrupt` 默认 `false`，普通 parent prompt 只作为 durable steer 进入 child，避免抢占仍在自主探索的 sub-agent
- 当 parent 明确发现 child 长时间重复 discovery、重复 read/grep/load_skill、范围漂移或需要立即交付当前证据时，可以显式传 `interrupt=true` 请求 best-effort 抢占
- 该工具不会创建、取消、停止或标记 child work 完成；parent 仍需用 `agent_status` / `agent_list` / `agent_wait` 回收结果，或用 `agent_stop` 停止尚未启动的 queued job以及显式结算 budget-paused blocked job
- 只能操作当前 parent 关联的 child/session job；不得向无关 session 注入 prompt
- 这只是给 master agent 一个 Codex-style steer 能力，不代表 runtime 自动替 parent 决定何时收敛子任务或固定任何审计 workflow

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

### 4.3 explorer role/profile

`explorer` 是 large-project profile 下的可选 child role，不是新的自动 orchestration engine：

- parent 模型或外部调用方必须显式选择 `agent_role=explorer`；runtime 不按 prompt 大小、repo 规模或 tool 次数自动 spawn
- 适用 guidance 是信息经济启发：开放式、跨模块、入口不明且预期原始检索量远大于最终结论时考虑 explorer；入口明确的小检查由 parent 直接完成通常更合适
- 为 context isolation 委托时，parent 可选择同步 spawn，或 background spawn 后显式 `agent_wait`；应避免重复 explorer 已覆盖的 repo 探索，但 runtime 不以 hard guard 阻止 parent 的其他工作
- child 继续是 fresh session；child tool calls/messages/events 只写 child 文件事实，parent transcript 不复制 child trajectory

#### 4.3.1 capability profile

effective `tool_profile=explorer-readonly-v1` 的唯一 allowlist 为：

```text
read_file
grep_files
grep
glob
load_skill
finish
```

ToolRegistry 的同一个 versioned profile 同时控制 provider schema 与执行。`shell`、`read_session_history`、trusted command skills、write/edit、`await_input`、goal/todo/task/feature mutation、Plan Mode tools、全部 agent control 和任何其他未列工具均不可见；直接、恢复或伪造调用返回 `failure_class=schema_reject`、`error_code=tool_not_allowed_for_role`、`tool_profile=explorer-readonly-v1`，且不运行 definition/command 副作用。

#### 4.3.2 role prompt 与 handoff

explorer system guidance 只定义：

- 当前 session 只读，不修改 workspace 或 durable goal/task/plan state，不运行 shell，不派生 child
- 回传简短结论与 `claim | file:line | confidence` 证据表
- 列出未覆盖范围和仍需 parent 判断的关键疑点
- 大输出与原始检索轨迹留在 child session/artifact，不粘贴进 final handoff

它不得固定搜索命令、文件阅读顺序、审计路线、taskboard 节奏、必须 delegation 或必须等待。同步 `agent_spawn` result、background notification 与 `agent_status` 的 `final_text` 全部复用统一 ToolResult/background byte cap，并保留 child session/job/visible-path reference；超长正文只能形成有界 preview + recoverable artifact metadata。

#### 4.3.3 provider/options/isolation resolution

- `role_providers.explorer` 与其他 role 一样支持 provider、API provider、base URL、model、`reasoning_effort`、`max_output_tokens`
- routing precedence：显式 request provider/model > role routing override > parent 同 provider > provider defaults；显式 provider 抑制 role provider/API/base/model routing override
- option precedence：选中 provider defaults < parent effective options（仅同 provider）< role reasoning/output < 显式 request provider options
- explorer 未显式给 isolation（空值或 `default`）时为 `off`；显式 `off` / `auto` / `git` / `copy` 原样执行
- effective provider/model/options/isolation/tool profile 在提交/创建时写入 queue job、child `session.json`、创建/排队事件和 background notification；worker/resume 读取 durable snapshot，不受后续 Settings 热更新重解释

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
~/.aegis-agent/_worktrees/<session-id>/
```

也允许通过 CLI / tool 显式覆盖。

约束：

- 若显式配置的 `isolation_root` 位于 source workdir 内部，应直接返回错误
- legacy 的 workspace-local 形状（例如 `.aegis-agent/_worktrees`）不再作为默认推荐值，因为 parent workdir 指向仓库根目录时会与“不得在源目录内部创建隔离目录”的约束冲突

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

默认 `enabled = true`、`max_depth = 1`、`max_active_children = 4`。当前产品只允许 root master 创建 child，因此默认深度与真实能力一致；advanced profile 后续若放开 nesting，仍必须受显式 depth limit。active child cap 同时覆盖 foreground 与 background，queue worker count 和 resume 入口都不能绕过它。所有 non-running -> running transition（新 direct spawn、queue claim、direct/queue `agent_prompt` resume、budget extension resume、parent-stop resume）都在同一 durable `claim.lock` 下按 `root_session_id` 原子检查/占位；同一 direct child 的并发 resume 只能有一个 reservation owner。direct reservation 的 owner 必须同时满足 PID 存活与（Linux 可用时）boot-scoped process start identity 匹配；刚创建/刚 resume 的 provisional reservation 允许在 session 尚未切为 running 的有界窗口内占位，超过 stale 上界即回收。默认暴露 `agent_spawn` / `agent_status` / `agent_list`，operator 可通过 `runtime.multi_agent.enabled=false` 显式收窄。

超过时：

- 拒绝新的 delegation
- 返回结构化错误

### 9.2 状态追踪

parent-child 关系必须落在文件事实中，不能只留在内存里。

### 9.3 worktree 生命周期

默认不自动删除 child 隔离目录，避免丢失排障现场。清理由调用方自行决定。

### 9.4 lineage context report

- 对任意 root/child session 查询 context report 时，runtime 先解析 `root_session_id`，再递归遍历完整 child tree；visited session id 去重，父链/子链 cycle 或指向其他 root 的元数据 fail closed
- 每个 session 保留自己的 request/turn/tool-call、peak/aggregate input、compaction、artifact/provider-view bytes、known/unknown usage 和 wall time；child trajectory 不复制进 root event/message 文件
- aggregate 明确分开 `root_peak_estimated_input_tokens`、`child_peak_estimated_input_tokens`、root/child/total aggregate input 与 provider usage、root/child compaction 和 tool-call 数。`child_session_count` 只计唯一后代
- 用 child id 查询返回同一 root tree，并在报告中保留 `requested_session_id`；这保证 CLI/Web 从 child inspector 进入时仍能看到 root 是否受保护和全树总消耗
- 报告是只读观测面。runtime 不依据这些指标自动 spawn、强制 parent wait、停止 child 或调整 isolation/compaction policy

## 10. 验收标准

- child session 可以同步执行并返回结构化结果
- parent / root / depth 元数据正确落盘
- `auto` isolation 能在 git repo 和非 git 目录下正确分流
- `children` 与 `agent_list` 能同时看到已落盘 child 和未启动完成的 queued jobs
- explorer normalize/config/API/Store round-trip，且旧三 role 配置保持兼容
- explorer provider schema 精确等于六项 allowlist，执行层对所有禁用 built-in/trusted command 返回稳定 typed denial 且无副作用
- explorer default isolation off、显式 canonical isolation 保持；effective role provider/reasoning/output/isolation/tool profile 在 direct/background facts 中一致
- deterministic sync/background fixture 证明 parent provider messages 不包含 child 原始 tool trajectory，只包含经过统一 byte budget 的 handoff/reference；失败、暂停、取消与 parent recovery 不绕过该 cap
- Web Settings 有 Explorer row 和完整 role override round-trip；session inspector 轻量显示 effective profile/options，默认首页没有新的 orchestration panel
- root/child ContextReport 数学可对账，既显示 root peak，也显示 root/child aggregate、provider-view/tool-artifact bytes 与 total；Web 通过现有 inspector 的懒加载 bounded Context tab 暴露，不增加首页 dashboard
