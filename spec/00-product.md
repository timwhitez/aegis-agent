# Go CLI Agent Product Spec

## 1. 产品定义

`go-cli-agent` 是一个用 Go 编写的极简通用 CLI agent harness。

它不是另一个重型 TUI 编程助手，也不是把固定 workflow、plan engine、verification engine 硬塞进 runtime 的 orchestration 框架。它的目标是把一个可持续演进的 agent 基座做扎实：

- 最小但完整的 agent loop
- 清晰的 CLI 命令面
- 本地文件事实驱动的 session / state / events
- tools / skills / hooks / tasks
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

### 3.1 Core v1 必须达成

- 提供稳定的 CLI agent 运行时，可在工作目录内完成多轮任务
- 支持 OpenAI Responses、Anthropic Messages、Google Gemini `generateContent`
- 支持 `openai-compatible` 的 Responses 形状兼容入口
- 支持 built-in tools、skills、hooks、session 持久化、todo + task graph
- 支持 `run` / `exec` / `steer` / `continue`
- 支持 `Esc` 暂停、自然停顿进入 `awaiting_input`、`continue` 恢复
- 支持 provider generation / reasoning 选项通过 runtime 和 session metadata 传递
- 架构上可演进为 Go SDK 或 OpenAPI 服务

### 3.2 当前不作为 core v1 完成标准

以下能力不作为 minimal core 的默认完成标准：

- terminal TUI
- local Web console

### 3.3 大型项目 profile

在保持 minimal core 默认帮助面简洁的前提下，当前仓库还需要支持一条更偏大型项目执行的 profile：

- `experimental delegate`
- `experimental children`
- `experimental queue`
- `experimental web`
- `--isolation auto|copy`

这条 profile 不要求把扩展能力塞回默认 root help，但要求它们具备真实 runtime、session、queue、notification、isolation 证据，而不只是保留兼容壳。

### 3.4 明确不做

- 不做 hosted multi-user Web SaaS
- 不做桌面端、IDE 扩展
- 不做 MCP 平台、插件市场、远程 skill 安装
- 不做固定 DAG、plan graph、verification graph
- 不做“框架替模型做决定”的重型 orchestration

## 4. 目标用户

### 4.1 第一类

- 长时间工作在终端里的工程师
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

### 5.1 `run`

- 启动一次带键盘中断能力的执行
- 运行期间监听 `Esc`
- 若模型自然停顿且未 `finish`，session 进入 `awaiting_input`
- 运行中的 session 可通过外部 `steer` 热插入新 prompt

### 5.2 `exec`

- 纯命令执行模式
- 不监听 `Esc`
- 适合脚本或 CI
- 默认要求显式 `finish`
- 若模型停止但未显式完成，不把任务误判为成功

### 5.3 `steer`

- 面向 `running` session 追加新输入
- 默认 queue-first，在最近安全边界并入执行
- `--interrupt` 表示 best-effort 抢占

### 5.4 `continue`

- 恢复 `paused`、`awaiting_input`、`failed` session
- 可追加新的 user message
- 不重放旧的外部副作用

## 6. 核心设计原则

### 6.1 模型是 Agent

runtime 只提供循环、工具、知识入口、权限边界和状态持久化，不替模型做固定流程决策。

### 6.2 CLI 优先

主交互通过以下方式完成：

- 普通命令行参数
- 标准输入输出
- 轻量阶段化文本输出
- 可选 JSON Lines 输出

TUI 只能是扩展观测面，不能成为主路径依赖。

本项目允许在显式 `experimental` 入口下提供 local Web console，但该控制台必须：

- 复用本地文件事实源与 runtime 控制面
- 不引入第二套数据库或服务端权威状态
- 不反向要求 README、默认 help、默认 smoke 都围绕 Web 设计

### 6.3 Provider 只做薄抽象

只统一运行时真正需要的最小接口。OpenAI、Anthropic、Google 的 replay 和 tool 格式差异必须保留在 adapter 层。

### 6.4 文件事实优先

session、state、messages、events、todo、tasks 都必须落盘。恢复依赖文件事实，而不是进程内临时状态。

### 6.5 上下文要可持续

- skill 正文按需加载
- 工具输出分层截断
- 长会话要 compaction
- compaction 不覆盖原始日志

### 6.6 扩展面服从主路径

Phase 11+ 的能力只能在不破坏 Phase 0-10 清晰度的前提下存在。README、脚本、帮助文本、测试默认都以 core v1 为准。

## 7. Core v1 能力边界

### 7.1 v1 要有

- 核心 agent loop
- built-in tools
- session store
- todo + task graph
- skills 和 `AGENTS.md` 指令链
- hooks v1
- compaction
- `run` / `exec` / `steer` / `continue` / `sessions` / `tasks` / `init`
- provider probe / doctor
- OpenAI / Anthropic / Google adapter
- `openai-compatible` Responses 模式

### 7.2 v1 当前限制

- 不做真正 SSE / WebSocket 多路流式 UI
- OpenAI Responses 的可 replay encrypted reasoning item、Anthropic thinking signature / redacted block、Gemini thoughtSignature 都可以作为 provider-owned `provider_content_blocks` 落盘；可读 summary/text 才进入 `Message.thinking`
- provider-native thinking / reasoning replay facts 仅由对应 adapter 保存和解释，不作为跨 provider 公共消息语义，也不由 CLI / Web 层解析
- 不做跨 provider context handoff
- 不做 provider fallback routing
- 不把 child agent / queue / TUI 作为当前主路径
- Web console 只作为显式 experimental surface 存在

## 8. 非功能要求

- Go 1.24+ 兼容
- Linux / macOS / WSL 优先
- 配置文件默认 YAML
- 会话数据采用 JSON / JSONL
- session 根目录默认 owner-only 权限
- 所有关键状态变化都要先写盘再返回

## 9. 成功标准

### 9.1 功能标准

- 能完成纯文本任务
- 能完成带工具调用任务
- `run` 可自然停在 `awaiting_input`
- `exec` 在未 `finish` 时不会误判成功
- `steer` 可在运行中被接纳
- `Esc` 可暂停，`continue` 可恢复
- todo 与 task graph 可稳定工作
- provider generation / reasoning 选项能正确进入 adapter 请求

### 9.2 工程标准

- CLI 只是适配层，核心 runtime 可直接测试和复用
- provider / hooks / session / compaction / interrupt 都有自动测试
- spec、README、脚本和当前实现对齐
- 扩展 phase 不再挤占 core v1 的默认完成口径
