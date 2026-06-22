# Go CLI Agent Tools And Skills Spec

## 1. 目标

工具和 skills 是 `go-cli-agent` 从“空循环”变成“可执行 harness”的关键能力层。

原则：

- 工具是原子动作
- skills 是按需知识与工具包
- tool registry 与 runtime 解耦
- 先支持本地 skills，不做远程插件系统

## 2. 内置工具清单

v1 内置工具固定为：

- `shell`
- `read_file`
- `write_file`
- `edit_file`
- `glob`
- `grep_files`
- `grep`
- `finish`
- `load_skill`
- `get_goal`
- `create_goal`
- `update_goal`
- `get_plan_mode`
- `submit_plan`
- `request_user_input`
- `todo_write`
- `todo_read`
- `task_create`
- `task_update`
- `task_list`
- `task_get`
- `feature_list_create`
- `feature_list_update`
- `feature_list_read`
- `agent_spawn`
- `agent_wait`
- `agent_stop`
- `agent_prompt`
- `agent_status`
- `agent_list`

当前仓库默认还会向 session 工具面暴露一组扩展 phase 兼容工具，让 master agent 自己决定是否需要派生 child：

- `agent_spawn`
- `agent_wait`
- `agent_stop`
- `agent_prompt`
- `agent_status`
- `agent_list`

若部署方明确不希望暴露这组能力，可设置 `runtime.multi_agent.enabled=false` 把它们从 tool list 中移除。

## 3. 工具通用契约

每个工具都必须定义：

- `name`
- `description`
- `input_schema`
- `execute(ctx, workdir, args) -> ToolResult`

### 3.1 ToolResult

字段：

- `llm_output`
- `display_output`
- `metadata`
- `final`

其中：

- `llm_output` 供模型继续推理
- `display_output` 供 CLI 直接展示
- `final` 用于 `finish`

## 4. 工具行为约束

### 4.1 `shell`

- 在 `workdir` 中运行
- 可选接受 `workdir` 覆盖；相对路径按当前 workspace 解析，解析后仍必须位于 workspace 内且是目录
- 对已注册 skill，`workdir` 也可使用 `load_skill` 返回的 skill 根目录提示；这只表示 skill bundle 的受控执行目录，不改变 workspace 写入边界
- 必须接受 timeout
- stdout/stderr 合并后按字节限额截断；超长输出应保留头部与尾部并标注中间省略字节数，避免丢失末尾错误摘要
- 返回码、timeout、workdir、sandbox、原始输出长度和截断状态必须写入 metadata，并以简短执行摘要进入 `llm_output`，避免模型只能在 UI/event metadata 中看到关键执行事实
- 默认只继承 allowlist 环境变量，避免把整个父进程环境泄露给子进程
- 轻量 `runtime.exec_policy.mode` 默认 `warn`，对提权命令、明显危险删除、secret path 写入和常见网络出站命令只写 metadata warning；显式设为 `deny` 时才阻断；设为 `off` 时不附加策略 metadata
- exec policy 只能作为安全/权限边界，不得演变为任务路线、审计路线、委派策略或交互审批 UI

### 4.2 `read_file`

- 路径必须限制在工作区内
- 例外：已注册 skill bundle 文件属于只读资源根，允许用 `skills/<skill-name>/...`、`load_skill` 返回的绝对路径，或唯一匹配的 skill-relative 链接路径读取；不得把这些路径误解析成 `workspace/skills/...`
- skill 文件读取必须校验 symlink escape，且不赋予写入权限
- 默认支持 `offset` / `limit`
- v1 采用小窗口读取，默认与最大返回窗口都限制为 120 行
- 读取前必须拒绝超大 regular file；当前共享文件读取上限为 16 MiB，避免 `offset` / `limit` 窗口化之前先把异常大文件完整读入内存
- 返回结果应包含实际窗口范围，便于模型基于行号继续做下一次定点读取

### 4.3 `write_file`

- 自动创建父目录
- 默认全量写入
- 新文件写入应采用原子替换流程，并为 agent 产物设置 owner-only 默认权限
- 必须拒绝写入 credential / private-key / secret 配置路径；该拒绝不仅基于用户输入路径，也要覆盖工作区内任意目录下已存在的敏感目录、敏感包配置路径和敏感文件名 symlink alias，避免通过普通目标路径间接改写敏感别名

### 4.4 `edit_file`

- 采用“查找旧文本并替换”的最小语义
- 若旧文本不存在则报错
- 与 `write_file` 共享同一套 workspace 写入策略，包括敏感路径和 symlink alias 检查

### 4.5 `glob`

- 返回相对工作区路径列表

### 4.6 `grep_files`

- 在工作区内递归搜索文本内容
- 例外：已注册 skill bundle 文件和目录属于只读资源根，允许用 `skills/<skill-name>/...`、`load_skill` 返回的绝对路径，或唯一匹配的 skill-relative 链接路径搜索；不得把这些路径误解析成 `workspace/skills/...`
- 仅返回命中文件路径，不返回整段上下文
- 用于先做便宜的候选文件发现，再配合 `read_file` 定点读取
- 默认跳过常见构建产物、缓存目录和二进制文件

### 4.7 `grep`

- 在工作区内递归搜索文本
- 例外：已注册 skill bundle 文件和目录属于只读资源根，允许用 `skills/<skill-name>/...`、`load_skill` 返回的绝对路径，或唯一匹配的 skill-relative 链接路径搜索；不得把这些路径误解析成 `workspace/skills/...`
- 返回匹配文件、行号、片段摘要
- 支持可选 `include` glob 过滤，语义与 `grep_files.include` 一致
- 支持可选 `limit` 控制返回匹配行数，超大值必须被 cap 到实现上限
- 默认跳过常见构建产物、缓存目录和二进制文件
- 命中文件内容读取必须复用 capped regular-file reader；超出 16 MiB 的单文件应跳过或返回受控错误，不得完整读入内存后再截断
- 更适合“已经知道要找哪类证据，只差精确行号”的场景，而不是大范围初筛

### 4.8 `finish`

- 接受 `message`
- 不写文件，不做副作用
- 由 runtime 解释为显式完成信号

### 4.9 `load_skill`

- 接受 `name`
- 返回目标 `SKILL.md` 的完整内容与路径信息
- 当 skill 依赖相对 shell 路径时，返回值还应给出可直接复用的 skill 根目录执行提示，避免把 skill 内相对脚本误当成 workspace 根相对路径
- 返回值还应明确 skill references 是注册 skill bundle 下的只读资源，可通过 `read_file` / `grep` / `grep_files` 的 `skills/<skill-name>/references/...`、`load_skill` 返回的绝对路径，或唯一匹配的 `references/...` 访问，不属于 workspace 文件

### 4.9.1 Goal Tools

`get_goal`

- 读取当前 session 的 durable goal
- 无 goal 时返回 `null`

`create_goal`

- 仅在用户或系统显式要求 goal-driven work 时创建一个 current goal
- 默认只需要 objective；criteria、validation、features / milestones 等结构化信息应优先由 agent 在运行中拆分，保留为高级/兼容字段
- 已存在 current goal 时拒绝，避免模型无意覆盖用户目标

`update_goal`

- 模型侧只允许 `status=complete`
- pause / resume / clear / budget_limited 由用户或系统控制
- complete 前应基于文件、命令、events 或其他 session facts 做 completion audit

### 4.9.2 Plan Mode Tools

`get_plan_mode`

- 读取当前 session 的 Plan Mode snapshot
- 无 Plan Mode 时返回 `null`
- 用于规划阶段查看 objective、pending question、plan version、approval status 和 approved plan context

`submit_plan`

- 仅在 `planmode.status=planning` 时可用
- 接收 title、summary、完整 Markdown plan、assumptions、risks 与 verification 列表
- 写入 `planmode.json`、`artifacts/planmode-history.jsonl` 和 `artifacts/planmode-plan.md`
- 提交后当前 tool batch 必须停止，session 进入 `awaiting_input` + `phase=plan_approval`
- 该工具不执行计划，也不自动生成 todo/task/child/queue

`request_user_input`

- 仅 Plan Mode planning 阶段使用，且仅 root session 可用
- 一次请求 1-3 个短问题；每题必须有 2-3 个互斥选项，客户端可提供 free-form Other
- 有交互 responder 时可以同步等待回答；active Web handle 丢失或进程重启后，回答/取消必须通过已持久化的 `pending_request.tool_call_id` 补齐 tool result
- CLI 非交互且没有 responder 时，必须在写入 pending request 前返回可 replay 的工具错误

Plan Mode pending 时，provider tool schema 与 `CompletionController` 都必须只允许 read/search/load_skill、只读 goal/todo/task/feature-list、`get_plan_mode`、`request_user_input` 和 `submit_plan`；未知工具、skill command tools、workspace extension tools、mutating tools、agent/queue tools 与 `finish` 默认拒绝。

### 4.10 `task_update`

- 接受 `task_id` 以及任务字段增量更新
- 可更新 `status`、`priority`、`owner`、`subject`、`description`
- 可增删 `blocked_by` / `blocks`
- 任务完成时要自动清理依赖边

### 4.11 `todo_write`

- 用于高频更新 session 级 todo 列表
- 只表达“执行进度账本”，不表达依赖图，也不替代实际执行、验证或 `finish`
- 保留已有 todo 的顺序、内容和优先级；已完成/取消项不可删除或回退；新 todo 只能追加，不能直接新增为 completed/cancelled
- 仅允许一个 todo 为 `in_progress`

### 4.12 `todo_read`

- 读取当前 session 级 todo 列表
- 供模型在长任务中回看当前执行节奏

### 4.13 `task_create`

- 创建持久化 task graph 节点
- 支持初始 `blocked_by`
- 自动维护双向依赖关系

### 4.14 `task_list`

- 列出当前 session 的全部 tasks
- 默认返回完整 task graph，也可用 `include_completed=false` 或 `status` 读取过滤后的任务视图
- 返回 ready / blocked / completed / cancelled / done 的派生视图

### 4.15 `task_get`

- 读取单个 task 的完整状态与依赖信息

### 4.16 `feature_list_create`

- 为当前 session 创建 durable 的 feature list 文件
- 适合多 feature / 多 wave 长任务，把 feature 粒度状态从自由文本计划中外置出来
- 每个 feature 至少包含 `description`，可选 `steps`
- 创建后默认初始化为 `pending` 且 `passes=0`

### 4.17 `feature_list_update`

- 按 `id` 更新单个 feature 的 `status` / `passes`
- 若目标 feature 不存在必须报错，而不是静默追加
- 更新后需要刷新该 feature 的 `updated_at`

### 4.18 `feature_list_read`

- 读取当前 session 的 feature list 完整快照
- 供模型在长任务中回看当前 feature 收敛状态

### 4.19 `agent_spawn`

- 默认注册到 session tool list
- 设置 `runtime.multi_agent.enabled=false` 时不注册
- 从当前 session 派生 child agent
- 支持同步执行和后台自治
- 支持 worktree / copy 隔离
- 未显式提供 `provider` / `model` 或传入 `default` 时，默认继承当前 parent session 的 provider / model
- `mode=full-auto` 作为兼容别名按 `exec` 处理
- `isolation_mode=workspace-write` 作为兼容别名按 `off` 处理
- 工具可见不代表 runtime 会自动 delegation；是否调用由当前 master agent 自主决定

### 4.20 `agent_wait`

- 默认注册到 session tool list
- 设置 `runtime.multi_agent.enabled=false` 时不注册
- parent agent 主动停车等待一个 background child job 的 durable result，并由 harness 后续自动恢复 parent

### 4.21 `agent_stop`

- 默认注册到 session tool list
- 设置 `runtime.multi_agent.enabled=false` 时不注册
- 停止尚未被 worker claim 的 queued background child job；不能安全停止 running child

### 4.22 `agent_prompt`

- 默认注册到 session tool list
- 设置 `runtime.multi_agent.enabled=false` 时不注册
- 向当前 parent 名下的 running child session 或已启动 background child job 追加 durable steer prompt
- 用于 scope 收窄、补充证据要求、请求进度或 handoff、重定向 child；不创建、不取消、不完成 child work
- `interrupt` 默认 `false`，避免普通 child steer 抢占正在自主探索的 sub-agent；只有明确需要抢占当前 provider/tool 边界时才显式传 `interrupt=true`

### 4.23 `agent_status`

- 默认注册到 session tool list
- 设置 `runtime.multi_agent.enabled=false` 时不注册
- 查询 child session 或后台 job 的状态

### 4.24 `agent_list`

- 默认注册到 session tool list
- 设置 `runtime.multi_agent.enabled=false` 时不注册
- 查询当前 session 的 child sessions 和关联 jobs

## 5. 工作区安全

所有文件工具统一使用 `safePath(workdir, rel)`：

1. `clean`
2. `abs`
3. 对已存在路径和父目录做 `EvalSymlinks`
4. 校验解析后的目标仍位于 `workdir`

任何越界路径直接报错。

## 6. Skills 目录结构

```text
skills/
  example/
    SKILL.md
    tools/
      hello.yaml
```

### 6.1 `SKILL.md`

可选 frontmatter：

```yaml
---
name: example
description: Example skill for local tasks
---
```

若无 frontmatter：

- `name` 默认为目录名
- `description` 默认为首段摘要

### 6.2 `tools/*.yaml`

用于声明 skill 附带的命令型工具。字段：

- `name`
- `description`
- `command`
- `timeout_sec`（可选；省略时使用 configured runtime command timeout，显式值仍受 runtime 默认上限约束）
- `input_schema`

约束：

- `command` 必须是字符串数组，而不是 shell 字符串
- `name` 必须是 provider-tool 兼容名称，匹配 `^[A-Za-z_][A-Za-z0-9_-]{0,63}$`；空白、空格、点号、Unicode、数字开头或超长名称必须在 registry 阶段 fail fast，不能等到 provider 请求时失败
- built-in 工具名保留，不可覆盖
- direct-call skill command tool 必须从已解析的 skill 根目录执行，并把 cwd / sandbox bind source 绑定到 no-symlink 打开的目录；若 skill 目录在注册后、进程启动前被替换为 symlink，不得跟随新路径执行
- direct-call skill command tool 的 timeout、sandbox、exec policy 与环境变量过滤必须使用本次执行配置；结果 metadata 必须反映实际执行配置，而不是只反映 catalog/registry 创建时的配置

## 7. Skill 加载策略

system prompt 仅暴露：

- skill name
- short description

完整正文只在模型调用 `load_skill` 时注入。

理由：

- 对齐 `learn-claude-code` 的 s05 模式
- 降低默认上下文开销
- 保持技能发现与技能展开分离

## 8. Project Docs 加载

运行时应支持读取工作目录向上最近的 `AGENTS.md` 文件链，作为 project docs 注入 system prompt。

规则：

- 从 `workdir` 向上搜索到文件系统边界
- 近路径优先
- 仅注入 `AGENTS.md`，不自动注入其他 README / docs
- 这些内容是指令，不作为 skill 注册源
- 若上层 `AGENTS.md` 引用了工作区外路径，这些引用只作为意图提示，不能驱动模型反复重试越界读取

## 9. Tool Registry 行为

registry 负责：

- 注册 built-in tools
- 注册 skill tools
- 输出 provider 侧 tool schema
- 根据 name 路由执行

冲突规则：

- built-in tool 名称保留
- 同名 skill tool 默认拒绝加载
- v1 不做 override 机制

## 10. Planning 模型

本项目采用双层 planning：

- Layer A: `todo_write` / `todo_read`
  - 参考 `learn-claude-code` s03 与 `opencode` 的 session todo
  - 用于高频、短周期、单 agent 执行节奏控制
- Layer B: `task_create` / `task_update` / `task_list` / `task_get`
  - 参考 `learn-claude-code` s07 的持久化 task graph
  - 用于 durable goals、依赖、ready-state 与恢复后的继续执行

详细任务系统规则见 `spec/12-task-system.md`。

## 11. Tool Hooks 关联

运行时对每个工具调用执行顺序如下：

1. 触发 `tool.before`
2. 执行工具
3. 触发 `tool.after`
4. 落盘最终 tool result

hook 可修改最终进入模型的内容，但必须留下 trace。

## 12. 验收标准

- 内置工具可单独测试
- `load_skill` 可读取本地 skill
- skill tool 能被 registry 注册
- 越界路径被阻止
- `todo_write` / `todo_read` 可稳定回放当前执行计划
- `task_create` / `task_update` / `task_list` / `task_get` 可维护完整 task graph
- `feature_list_create` / `feature_list_update` / `feature_list_read` 可维护 durable feature 状态
- `finish` 能驱动 session 完成
