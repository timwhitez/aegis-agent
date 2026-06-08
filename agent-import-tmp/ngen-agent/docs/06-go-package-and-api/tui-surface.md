# TUI Surface 规格

> 状态: Active
> 最后更新: 2026-05-18
> 作用: 定义 NGEN 极简 TUI 的产品目标、交互逻辑、runtime bridge 与验证边界

## 1. 设计目标

TUI 的默认体验必须接近一个正常 coding agent：打开后直接进入可输入、可执行、可观察的工作界面，而不是先要求 operator 理解 task picker、inspector tabs、确认弹层或复杂导航模型。

设计中心：

- chat-first：底部 composer 是默认焦点，operator 可以直接输入自然语言任务或后续 steer。
- artifact-first：`.ngen/` 仍是唯一 runtime truth；TUI 不拥有 task、verifier、review 或 completion 结论。
- agent-managed task graph：operator 不需要手工创建、选择、同步或继续 task/subtask；task、worker、subtask 生命周期由 agent/runtime 根据目标、plan、blocker 和 artifacts 自主管理。
- minimal control：默认界面只暴露 transcript、composer、少量状态和必要 blocker；复杂状态用按需详情抽屉打开。
- no ceremony：没有必经任务选择、没有 task 管理动作、没有删除确认流、没有多层确认弹窗作为主路径。
- observable but quiet：工具调用、验证、审批、输入请求都要可见，但默认以 compact transcript cell / status line 呈现。
- no silent degradation：provider 配置缺失、工具失败、取消、验证失败和持久化失败都必须给出明确诊断。

本规格定义的是 `runtime.Service` 之上的 TUI surface，不是新的 backend。

## 2. Codex 借鉴边界

NGEN TUI 可以借鉴 Codex 的操作逻辑，但不照搬 Codex 的完整产品复杂度。

当前借鉴的核心原则来自 OpenAI Codex 开源实现：

- `ChatWidget`：事件驱动的聊天主界面；UI 从协议事件构建 transcript，并把活跃 turn、工具调用和 overlay 状态分层管理。
- `BottomPane`：composer 拥有默认输入，临时 view / overlay 只在需要时短暂替换 composer；底层先处理局部输入，高层再决定 interrupt / quit。
- exit/shutdown flow：用户退出应优先走 graceful shutdown / cleanup，active work 下 `Ctrl+C` 应优先 interrupt，而不是直接杀进程。
- queued steer：运行中继续输入不应被吞掉；最小实现可以 FIFO 排队，并在当前 turn settle 后继续提交。
- task-running as derived UI state：TUI 只显示当前 turn / tool / subagent 正在运行的事实，不要求用户理解或操作底层 task graph。

借鉴边界：

- 不引入 Codex 的 thread/fork/resume 生态。
- 不引入 connector、voice、image、plugin、side conversation 等扩展 surface。
- 不把 Codex 的复杂 modal/picker 体系作为 NGEN 默认路径。
- 不把 internal task/subtask graph 暴露成用户需要维护的项目管理界面。
- 不把聊天历史变成 canonical runtime truth；NGEN 的 truth 仍在 `.ngen/` artifacts。

参考源码与文档：

- https://github.com/openai/codex/blob/main/codex-rs/tui/src/chatwidget.rs
- https://github.com/openai/codex/blob/main/codex-rs/tui/src/bottom_pane/mod.rs
- https://github.com/openai/codex/blob/main/docs/exit-confirmation-prompt-design.md

## 3. 命令面

当前 TUI 命令面：

```text
ngen tui [TASK-ID] [--inline] [--poll-ms N] [--event-limit N]
```

默认语义：

- 提供 `TASK-ID` 只是 deep-link / debugging / resume 场景；正常 operator 不应需要知道 task id。
- 未提供 `TASK-ID` 时，不进入必经 picker；TUI 应直接进入 composer。
- 无 `TASK-ID` 的启动必须用确定性规则选择或创建一个 coding task：
  - 优先恢复当前 workspace 最近更新且非 terminal 的 `coding` task；
  - 如果没有可恢复 task，则创建一个 `source=tui` 的轻量 `coding` task；
  - 创建出的 task 不能长期停留在无意义的 `New Task` / `Task completed successfully` 文案上；第一条真实 operator prompt 必须回写 title/objective/criteria，并产生 artifact-backed `task_refined` event。
- task / subtask / worker 的创建、拆分、继续、同步和收敛由 agent/runtime 通过 `task_create`、`worker_spawn`、`worker_continue`、plan/project mutation 和 worker artifacts 自主管理；TUI 只展示结果、状态和需要人工决策的 blocker。
- `--inline` 强制关闭 alternate-screen。
- `--poll-ms` 仅覆盖本次 UI refresh 周期。
- `--event-limit` 仅覆盖 UI transcript 中的 event tail；durable `events.jsonl` 不被截断。

TUI 不提供默认 task picker、task list 操作、subtask 管理器、worker 管理器或 task 删除/清空入口。需要调试 raw task graph 时，应使用 CLI / ACP / Web backend 的管理面，而不是把 TUI 主流程变成 task console。

## 4. Task / Subtask 管理边界

TUI 面向的是“我告诉 agent 要做什么”，不是“我替 agent 管任务”。

规则：

- operator 输入自然语言目标、约束、批准或补充信息。
- agent/runtime 决定是否需要拆成 task、subtask、worker、plan step 或 project branch。
- subtask 的 spawn / continue / reconcile / release 必须由 runtime artifact contract 驱动，不由用户在 TUI 里手工选择 child task id。
- TUI 可以显示 `agent is planning`、`worker running`、`approval required`、`input needed`、`verification failed`、`review blocked` 等状态，但不暴露“创建子任务”“切换任务”“同步 worker”“继续 worker”这类管理动作作为主路径。
- 如果 parent/child 交互确实需要人类介入，TUI 只呈现 focused blocker：approve / deny、回答 input、或确认高风险操作；其余 orchestration 继续由 agent/runtime 完成。
- `TASK-ID`、worker id、artifact ref 可以出现在 transcript/status/detail 中作为可复制诊断信息，但不应成为用户完成日常 coding flow 的必需知识。

## 5. 默认布局

默认界面只包含三块：

1. Header / status line
   - task title 或短 id
   - phase/state
   - running / queued / blocked / error 状态
   - provider mode 作为低优先级信息
2. Transcript
   - operator / assistant / runtime message
   - tool call / command / verification / review 的 compact event cell
   - pending approval/input 的 compact blocker cell
3. Bottom pane
   - composer
   - 一行 context hint
   - 错误或 blocker 的短诊断

默认不显示固定右侧 inspector。需要更多状态时，通过 `/status`、`/plan`、`/memory` 或轻量 details drawer 按需查看。SimpleMode 中 `Tab` 只在 composer、chat transcript、details drawer 三者之间循环；数字键 `1`-`8` 只在非 composer 焦点下切换 details drawer 内容。详情视图是 read-only / decision-focused，不提供 task/subtask 管理动作；回到 composer 后主区域恢复 chat-first transcript。

响应式要求：

- 窄终端下仍保持 header、transcript、composer 三段结构。
- 长 task id、路径、command、error message 必须折行或截断，不得撑破布局。
- 默认界面不能被调试字段淹没；只显示当前动作需要的信息。

## 6. Composer 与输入逻辑

composer 是默认焦点。

键位：

- `Enter`：提交当前 prompt。
- `Shift+Enter` 或 `Ctrl+J`：插入换行。
- `Up/Down`：当 draft 为空时浏览本地 prompt history；运行中且 queue preview 可见时优先选择 queued prompt。
- `Ctrl+L`：刷新当前 UI snapshot。
- `Ctrl+C`：
  - active turn 中：interrupt 当前 turn，并通过 runtime 记录取消事实；
  - idle 且 composer 为空：退出 TUI；
  - 有 overlay 时：先关闭 overlay，再回到 composer。
- `Ctrl+D`：仅在 idle、composer 为空、无 overlay 时退出。
- `Esc`：关闭当前 overlay / suggestion；没有 overlay 时不做 destructive 操作。

输入规则：

- 普通自然语言永远优先进入 composer，不被单字符 hotkey 抢走。
- `a`、`i`、`p`、`?`、数字键等不作为 composer 内全局快捷键。
- 非 composer 焦点下，数字键可以切换 details drawer 的只读/决策型视图；这不是 task picker 或 worker manager。
- 运行中继续提交 prompt 时，默认进入本地 FIFO follow-up queue，不直接报 `A turn is already active.`。
- queued prompt 在真正开始执行前不得写入 `*.messages.jsonl` 冒充已执行 operator message。
- queue preview 只显示少量最新/最相关文本和总数；不做复杂重排管理。
- operator 至少可以取回最近一条 queued prompt 编辑或丢弃。

## 7. 本地命令

TUI 本地命令只保留高频、低歧义入口：

- `/help`
- `/status`
- `/run`
- `/resume`
- `/review`
- `/memory`
- `/mission [PROMPT]`
- `/missions [PROMPT]`
- `/goal PROMPT`
- `/goals PROMPT`
- `/clear`
- `/quit`
- `/exit`

命令原则：

- `/quit` / `/exit` 表示明确退出意图，走 graceful shutdown / cleanup，不需要确认弹窗。
- `/run` / `/resume` / `/review` 通过 `PromptSession(...)` 或现有 service contract 推进，不绕过 runtime truth。
- `/mission <prompt>`、`/missions <prompt>`、`/goal <prompt>` 与 `/goals <prompt>` 通过 `PromptSession(...)` 直接把后续普通 prompt 设为当前 task 的 mission/goal objective，自动推导 title 与默认 evidence-backed criterion，并返回 compact mission status。
- `/mission`、`/missions`、`/goal` 或 `/goals` 不带 prompt 时只打开或创建当前 task 的 mission artifacts，并返回 compact mission status；它不打开 mission/task/worker 管理控制台。
- TUI 默认不提供 `/tasks`、`/workers`、`Ctrl+O picker`、`Ctrl+T task navigation` 这类 task/subtask 管理入口；已有实现如果保留这些入口，必须降级为调试/兼容模式，不能出现在极简默认体验中。
- `/clear` 只清空当前 composer draft 或当前 UI 可见 transient 状态，不删除 durable task/session truth。
- 在默认 SimpleMode 下，`/tasks`、`/picker`、`/back`、`Ctrl+O`、`Ctrl+T` 与 `Ctrl+B` 必须给出本地 chat-first diagnostic，不能打开 task console、picker、back stack 或固定 inspector。完整模式如保留这些入口，只能作为 debug/compat surface。
- 未识别 slash command 应给出本地错误，不直接发给 provider 猜测。
- 非 slash 文本交给 runtime/provider，由 provider 决定 respond、run、resume、task_create 等动作。

## 8. Blocker 交互

approval / input / worker blocker 不应该把默认体验变成多层表单。

默认呈现：

- transcript 中出现 compact blocker cell；
- bottom pane context hint 给出下一步，例如 `approval required`、`input needed`、`worker waiting`；
- `/status` 或点击/选择 blocker 后打开一个 focused detail view。

approval 最小操作：

- 显示 approval id、scope、reason、owner task/worker。
- 提供 approve / deny。
- 决策后调用 `DecideApproval(...)` 并刷新 snapshot。

input 最小操作：

- 显示 request id、field、prompt。
- composer 临时变成 response input。
- 提交后调用 `RespondInput(...)` 并回到普通 composer。

worker 最小操作：

- 显示 worker id、role、status、parent action。
- 不提供常规 worker 管理动作。
- 如果 worker 明确需要 parent-side approval / input，高亮该 blocker 并调用对应 approval/input service。
- `continue_child` 应优先由 agent/runtime 在 parent prompt turn 中通过 `worker_continue` 决策推进；TUI 不要求 operator 手工选择 child 并继续。
- 当 details drawer 中已经明确显示 `continue_child` 或 active worker 时，`Enter` 可以作为显式恢复/验证入口调用 `ContinueWorker(...)`；这属于 worker blocker 的 focused action，不恢复常规 worker manager。

## 9. Runtime Bridge

TUI 使用独立 session mode：

```text
mode = "tui"
```

必须复用现有 service contract：

- `StartSession(..., "tui")`
- `PromptSession(...)`
- `CancelSession(...)`
- `Status(...)`
- `ReadSession(...)`
- `TailEvents(...)`
- `ListApprovals(...)` / `ListOwnedApprovals(...)`
- `ListInputRequests(...)`
- `ListWorkers(...)`
- `ContinueWorker(...)`
- `MemoryMarkdown(...)`

说明:

- 上述 service contract 可以被 TUI 用来读取状态或响应 blocker，但不代表 TUI 要把这些方法全部暴露成用户操作。
- task / worker / project graph 的 mutating orchestration 默认由 agent/provider decision 和 runtime loop 发起。
- TUI 只负责把这些 artifact-backed 变化渲染成 transcript/status/detail。
- task graph 的 raw 管理面属于 CLI / ACP / Web backend；TUI 默认不把这些管理动作放入日常 coding flow。

active turn 行为：

- prompt 提交后，TUI 立即进入 running state，并在后台执行 `PromptSession(...)`。
- UI 按 `poll_interval_ms` 刷新 artifacts 和 transcript。
- 新消息、新 tool call、新 blocker 和验证结果必须持续可见。
- turn settle 后立即刷新最终 snapshot，并尝试提交下一条 queued prompt。

取消行为：

- active turn 下 `Ctrl+C` 触发 interrupt，不直接退出进程。
- interrupt 必须取消当前 turn context，并在 runtime 层调用 `CancelSession(...)` 或等价路径记录 `aborted_user` / cancellation summary。
- 尚未执行的 queued prompt 必须留在 UI 中等待 operator 决定，或显式标记为 dropped；不能静默丢失。

## 10. Provider Setup

TUI 启动不应无条件进入 provider setup wizard。

规则：

- `provider.mode=builtin` 或 `command` 时，不应要求 OpenAI/Anthropic API key。
- 远端 provider 缺少必要配置时，TUI 应在 transcript/status 中显示清晰诊断，并允许 operator 打开 `/settings` 或退出修复。
- 任何 setup wizard 都必须写入 repo 认可的配置位置，或明确声明只是本进程临时环境；不能输出“saved”但只调用 `os.Setenv`。
- 默认模型、base URL 和 response/chat mode 必须来自 `ngen.json` / provider config 的同一套 contract，而不是 TUI 私有默认值。

## 11. Alternate Screen 与终端兼容

- `--inline` 等价于关闭 alternate-screen。
- 默认 `auto`，在不适合 alternate-screen 的环境中自动降级。
- 启动前检查 stdin/stdout 是否为 TTY。
- `TERM=dumb` 时拒绝启动，并给出明确错误。
- panic / fatal error 必须恢复终端状态。

## 12. 测试边界

最低测试集：

- `ngen tui` 无 `TASK-ID` 直接进入 composer，并能提交第一条 prompt。
- `ngen tui TASK-ID` 直接打开指定 task。
- provider 缺失时显示诊断，不阻塞 builtin/command mode。
- active turn 下继续输入进入 queue，turn settle 后按 FIFO 执行。
- active turn 下 `Ctrl+C` interrupt，不直接退出，也不丢失 cancellation artifact。
- idle 空 composer 下 `Ctrl+C` / `Ctrl+D` 退出。
- 默认 TUI 不显示 task picker、task list、worker manager，也不要求用户知道 task id / worker id；按需 details drawer 可以显示这些 id 作为诊断信息。
- agent/runtime 可以自主创建、继续、同步 subtask；TUI 必须只显示由 artifacts 派生的 compact 状态和必要 blocker。
- approval/input blocker 能通过 focused view 决策或响应。
- transcript 能显示 tool call、command output excerpt、verification failure、review blocker 和 final summary。
- 窄宽度渲染不重叠、不截断关键错误字符串。

当前已有测试如果仍要求默认 picker、task navigation 管理流、固定双栏 inspector、复杂 quick actions 或确认弹层，应按本规格更新；SimpleMode 的按需 details drawer、blocker 决策、worker focused action 和渲染/滚动稳定性仍属于默认 TUI 验证边界。
