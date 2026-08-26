# Aegis Agent Product Spec

## 1. 产品定义

`aegis-agent` 是一个用 Go 编写的 Web-first 本地 agent harness。

它不是 hosted SaaS，也不是另一个重型 TUI 编程助手，更不是把固定 workflow、plan engine、verification engine 硬塞进 runtime 的 orchestration 框架。它的目标是把一个可持续演进的 agent 基座做扎实，并把默认操作体验放在本地 Web 控制台中：

- 最小但完整的 agent loop
- 清晰的本地 Web 操作面
- 可脚本化的 CLI / API 控制面
- 本地文件事实驱动的 session / state / events
- tools / skills / hooks / tasks
- durable session goals
- 运行中补充输入、暂停与恢复
- 薄而真实的 provider adapter

它既可以承担 coding agent，也可以承担文档、审计、运维、整理、调研等任务。真正的能力边界来自：

- `skills/`
- 工作目录的 `AGENTS.md`
- system prompt / user prompt
- provider 能力

## 2. 设计基线

本项目以以下资料为核心：

- `learn-claude-code`
  - `s01`: 最小 agent loop
  - `s02`: 工具注册与 dispatch
  - `s03`: 高频 todo
  - `s05`: skill 按需加载
  - `s06`: context compaction
  - `s07`: 持久化 task system
- `pi-coding-agent`
  - 薄 provider 层
  - 明确的 session / event 事实源
  - CLI 与核心 runtime 解耦
- `bitter-lesson-agent-frameworks`
  - 模型是 agent，框架只是 loop 和 action space
  - 先给足能力，再按 eval 和风险收紧
- `opencode`
  - provider 协议差异处理
  - hooks / provider transform / generation options 的现实细节
- `codex`
  - thread / turn / steer / interrupt 语义
  - 运行中补充输入与恢复的交互模式

## 3. 产品目标

### 3.1 Web-first v1 必须达成

- 提供稳定的本地 Web-first agent 运行时，可在工作目录内完成多轮任务
- 提供完整的本地 Web Console v2，作为默认用户入口承载 session 启动、追加输入、继续执行、Goal、Plan Mode、Settings provider/model 配置、任务与 children 观测及基础控制；原页面只作为默认禁用的 legacy fallback 保留
- Web Console 默认使用简体中文与浅色主题，提供持久化的 English 切换；语言偏好只属于浏览器显示状态，不得成为第二套 runtime/session 状态源
- 保留稳定 CLI 命令面，作为脚本化、CI、故障恢复和高级操作入口
- 支持 OpenAI Responses、Anthropic Messages、Google Gemini `generateContent`
- 支持 `openai-compatible` 的 Responses 形状兼容入口
- 支持 built-in tools、skills、hooks、session 持久化、todo + task graph
- 支持一个 session 绑定一个 durable goal，用于长目标的目标契约、完成审计和恢复提示；默认用户入口只是一个 Goal 开关，prompt 本身就是目标，结构化计划和验证由 agent 在运行中拆分；高级 mission 若要求 plan approval，必须复用 Plan Mode 的真实执行门禁
- 支持 mission validation coverage 与 structured progress/handoff：validation contract approval 前可检查覆盖关系，模型可用窄工具记录 progress、evaluator 证据、child/queue 链接、commands、artifacts、blockers 和 budget wrap-up，但 runtime 不据此强制 DAG 或固定 worker/validator 流程
- 支持显式 Plan Mode：用户通过 CLI flag、Web toggle 或 API 字段进入 planning gate；审批前只允许只读探索、`request_user_input` 和 `submit_plan`，审批后再恢复普通执行
- 支持 Web 与 CLI 两条入口上的 `start` / `run` / `exec` / `steer` / `continue`
- 支持 `Esc` 暂停、模型通过 `await_input` 显式声明外部阻塞或等待条件、其他显式等待/停靠场景进入 `awaiting_input`，并由 `continue` 恢复
- 支持 provider generation / reasoning 选项通过 runtime 和 session metadata 传递
- 架构上可演进为 Go SDK 或 OpenAPI 服务

### 3.2 当前不作为 Web-first v1 完成标准

以下能力不作为 Web-first v1 的默认完成标准：

- terminal TUI
- hosted multi-user Web SaaS
- 浏览器端 IDE、远程终端或文件树编辑器
- 真正 SSE / WebSocket 多路流式 UI；当前可以继续采用 polling-first

### 3.3 大型项目与高级执行 profile

在保持 Web-first 默认页面简洁的前提下，当前仓库还需要支持一条更偏大型项目执行的 profile：

- `experimental delegate`
- `experimental children`
- `experimental queue`
- `--isolation auto|copy`

这条 profile 不要求把 worker pool、raw queue payload、isolation tuning 或 child orchestration 做成默认可见 UI，但要求它们具备真实 runtime、session、queue、notification、isolation 证据，而不只是保留兼容壳。Web 控制台可以提供轻量入口与观测链接，细粒度调参仍可留给 CLI / API。

该 profile 还提供可选的 `explorer` child role，用于开放式、跨模块或入口不明且“原始检索量远大于最终结论”的只读探索：

- 是否创建 explorer 仍由当前模型或调用方显式决定；runtime 不自动拆任务、不强制等待，也不把简单检查升级成 delegation。
- explorer 复用现有 fresh child session、queue、parent coordination 和文件事实源，通过最小只读 tool capability profile 隔离一次性搜索输出。
- parent 只接收有界的结论、`claim | file:line | confidence` 证据、未覆盖范围、关键疑点以及 child/session reference；child 原始 tool trajectory 保留在 child session，不复制进 parent transcript。
- Web 只在现有 Settings role provider 区和 session inspector 中提供配置/观测，不在默认首页增加 orchestration dashboard。

### 3.4 明确不做

- 不做 hosted multi-user Web SaaS
- 不做桌面端、IDE 扩展
- 不做 MCP 平台、插件市场、远程 skill 安装
- 不做固定 DAG、plan graph、verification graph
- 不做“框架替模型做决定”的重型 orchestration

## 4. 目标用户

### 4.1 第一类

- 需要一个本地 Web 控制台来管理 agent session 的工程师
- 希望 agent 可暂停、可恢复、可追踪
- 不希望复杂 TUI 成为主交互前提

### 4.2 第二类

- 想用同一套 harness 承载 coding、review、审计、文档、运维的人
- 希望通过 `skills/` 和提示词快速切换 agent 行为

### 4.3 第三类

- 想把项目后续演进成 Go SDK 或 OpenAPI 服务的开发者

### 4.4 第四类

- 需要一个更易上手的本地控制台来查看 session、task、queue、children、错误与恢复状态的操作者
- 希望在不学习全部 CLI 细节的情况下完成 `run` / `steer` / `continue` / background queue 基础操作

## 5. 核心使用模式

### 5.1 Web Console

- 默认入口是本地 Web 控制台
- 用户从 Session 工作区启动任务、打开 Goal 或 Plan Mode、追加 steer、continue、查看 timeline / tasks / queue / children；provider/model 通过 Settings 配置，避免每个 session 输入框重复暴露高级 provider 面板
- Web 控制台只通过 runtime / session store / queue store 做真实控制，不维护第二套状态
- 默认页面是 repo-owned Web Console v2；`web.legacy_ui_enabled` 默认关闭，只有显式启用时才提供旧页面。两套页面不得产生第二套 API、session 或浏览器权威状态
- 默认交互要简洁：高频路径不要求多轮用户确认；只有 validation coverage override、删除/清理、配置/API key 写入、外部暴露服务和其他不可逆或安全敏感动作需要显式确认

### 5.2 `run`

- CLI 前台执行入口，适合终端用户和故障恢复
- 运行期间监听 `Esc`
- 若模型本轮未调用 `finish`，runtime 默认继续 agent loop；只有显式暂停、Plan Mode/预算/后台等待、或退化停靠等场景才进入 `awaiting_input`
- 当任务尚未完成、但用户输入或外部条件缺失而无法继续时，模型可以调用 `await_input` 返回控制权；该动作不把 session 或 active Goal 标记为完成
- 运行中的 session 可通过外部 `steer` 或 Web steer 热插入新 prompt

### 5.3 `exec`

- 纯命令执行模式
- 不监听 `Esc`
- 适合脚本或 CI
- 默认要求显式 `finish`
- 若模型停止但未显式完成，不把任务误判为成功

### 5.4 `steer`

- 面向 `running` session 追加新输入
- 默认 queue-first，在最近安全边界并入执行
- `--interrupt` 表示 best-effort 抢占

### 5.5 `continue`

- 恢复 `paused`、`awaiting_input`、`failed` session
- 可追加新的 user message
- 不重放旧的外部副作用
- 可通过 `--approve-plan` 批准最新 Plan Mode plan 并追加可回放的 `planmode_approval` user message；普通 message 在 `awaiting_approval` 下默认视为 plan revision

## 6. 核心设计原则

### 6.1 模型是 Agent

runtime 只提供循环、工具、知识入口、权限边界和状态持久化，不替模型做固定流程决策。

### 6.2 Web 优先，CLI 可脚本化

主交互通过本地 Web 控制台完成：

- Session-first 工作区
- Timeline / tool lane / event activity
- Goal 与 Plan Mode inspector
- Settings provider / model 配置
- children / tasks 的对象级观测与轻量控制；queue/background 只保留 durable message 的
  有界关联摘要，独立 queue detail 与 submit 使用 API/CLI fallback

CLI 仍然是稳定的底层控制面，用于：

- 普通命令行参数
- 标准输入输出
- 轻量阶段化文本输出
- 可选 JSON Lines 输出
- CI、脚本化、无浏览器环境、故障恢复和高级调试

TUI 只能是扩展观测面，不能成为主路径依赖。

Web 控制台必须：

- 复用本地文件事实源与 runtime 控制面
- 不引入第二套数据库或服务端权威状态
- 不绕过 session store、provider adapter、tool guard、Plan Mode gate 或 Goal completion audit
- README、默认启动说明和主 smoke 路径应以 Web-first 为主，同时保留 CLI fallback

### 6.3 Provider 只做薄抽象

只统一运行时真正需要的最小接口。OpenAI、Anthropic、Google 的 replay 和 tool 格式差异必须保留在 adapter 层。

### 6.4 文件事实优先

session、state、messages、events、goal、todo、tasks 都必须落盘。恢复依赖文件事实，而不是进程内临时状态。

goal 与 goal history 属于 session 文件事实源；WebConsole 可以展示和控制 goal，但不能成为目标状态的权威来源。

Plan Mode 同样是 session 文件事实：`planmode.json` 是权威状态，`artifacts/planmode-history.jsonl` 是状态流水，`artifacts/planmode-plan.md` 只是 operator-readable 派生计划。Plan Mode 不替代 Goal/Mission/Todo/Task，也不能从一句自然语言“先计划”自动启用硬门禁。

### 6.5 上下文要可持续

- skill 正文按需加载
- 工具输出分层截断
- 长会话要 compaction
- compaction 不覆盖原始日志
- compaction 只做上下文规模控制，不默认执行报告、prompt、session 或 provider view 脱敏；如需脱敏，由用户在当轮 prompt 中明确要求

### 6.6 Web 应用面服从 runtime 边界

Web 可以成为默认操作体验，但不能反向污染 runtime 边界。queue、delegate、children、isolation、Plan Mode 和 Goal 都必须继续通过明确的 runtime / session / store 契约工作，而不是由浏览器端状态、固定 DAG 或硬编码 workflow 接管。

## 7. Web-first v1 能力边界

### 7.1 v1 要有

- 核心 agent loop
- built-in tools
- session store
- todo + task graph
- skills 和 `AGENTS.md` 指令链
- hooks v1
- compaction
- local Web console：session start/continue/steer、Goal、Plan Mode、Settings provider/model 配置、timeline、tasks、children、settings，以及支持预览、下载、上传、文件重命名、建目录和删除的受限 workspace browser；queue/background 保留在 API/CLI/文件事实源，不进入默认页面
- `run` / `exec` / `steer` / `continue` / `sessions` / `goal` / `tasks` / `init`
- provider probe / doctor
- OpenAI / Anthropic / Google adapter
- `openai-compatible` Responses 模式

### 7.2 v1 当前限制

- 不做真正 SSE / WebSocket 多路流式 UI
- OpenAI Responses 的可 replay encrypted reasoning item、Anthropic thinking signature / redacted block、Gemini thoughtSignature 都可以作为 provider-owned `provider_content_blocks` 落盘；可读 summary/text 才进入 `Message.thinking`
- provider-native thinking / reasoning replay facts 仅由对应 adapter 保存和解释，不作为跨 provider 公共消息语义，也不由 CLI / Web 层解析
- 不做跨 provider context handoff
- 不做 provider fallback routing
- 不把 child agent / queue / TUI 变成固定 workflow；Web 可以展示和触发这些能力，但 child / queue 是否使用仍由模型或用户明确决定
- Web console 是默认本地 operator surface；它仍然只能复用 runtime 与文件事实，不能成为权威状态源
- 全局 `max_turns_hard` 默认 `-1`（关闭），显式启用后对 root、foreground child 与 background/queue child 的每次 runtime run 一致生效；child / background 另有可选的 per-attempt turn、active-runtime 与 absolute-deadline 预算，默认全部关闭，只在 Settings 或配置文件中显式启用
- Session Goal 是 core 收敛加固：它记录用户可见目标和完成审计，不把 runtime 改造成固定 DAG、plan graph 或 verification engine。原 Mission 能力收敛为 Goal 的内部结构化计划字段：agent 可在运行中沉淀 features、milestones、validation contract 和 role hints，但默认用户不需要选择 Goal/Mission 模式、不需要填写预算或拆分表单；child / queue 是否使用仍由模型或用户决定。
- Mission 的 `require_plan_approval` 不是展示状态：它会创建 linked Plan Mode，并由 Plan Mode schema 裁剪与 `CompletionController` gate 在批准前阻断 shell/write/edit/todo/task/agent/queue/finish 等执行动作。批准后 runtime 同步 mission plan approved 状态，但不会把 plan 转成固定 workflow。
- Mission plan approval 会先检查 validation contract coverage；未覆盖或 invalid assertion 默认阻断，CLI/API 明确 override 才能继续。`stop_on_budget` 触发时 runtime 只允许预算 wrap-up，不把 budget-limited 渲染成 completed。

## 8. 非功能要求

- Go 1.26.7+ 兼容；声明的最低版本必须位于 Go 官方仍受支持且已包含已知标准库安全修复的 release line
- Linux / macOS / WSL 优先
- 配置文件默认 YAML
- 会话数据采用 JSON / JSONL
- session 根目录默认 owner-only 权限
- 所有关键状态变化都要先写盘再返回

## 9. 成功标准

### 9.1 功能标准

- 能完成纯文本任务
- 能完成带工具调用任务
- Web 控制台能完成 session start、steer、continue、Goal / Plan Mode 基础控制与 Settings provider/model 配置；queue job 提交由 REST API/service test 与 CLI advanced path 验证，默认 Web 不提供提交表单
- Web 控制台默认以 `zh-CN` 展示所有 operator-owned 文案、ARIA 标签、日期和数字格式，可切换并持久化 `en`；两种语言都必须通过真实浏览器验收
- `run` 只在显式等待/停靠场景进入 `awaiting_input`；普通无 tool / 无 `finish` turn 会继续 loop
- `await_input` 会把未完成任务显式停靠为可恢复状态，保留 active Goal 与 blocker/resume condition 事实，不绕过 `finish` 的完成审计
- `exec` 在未 `finish` 时不会误判成功
- `steer` 可在运行中被接纳
- `Esc` 可暂停，`continue` 可恢复
- todo 与 task graph 可稳定工作
- provider generation / reasoning 选项能正确进入 adapter 请求

### 9.2 工程标准

- Web service、CLI 和 SDK 都只是适配层，核心 runtime 可直接测试和复用
- provider / hooks / session / compaction / interrupt 都有自动测试
- spec、README、脚本和当前实现对齐
- Web-first 默认体验与 runtime / provider / session 文件事实保持一致
