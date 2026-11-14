# 当前路线与变更顺序

本文件不再维护按历史阶段堆叠的开发叙事。它只说明当前主干的稳定面、改动优先级和完成判据，避免把历史任务清单误当成产品状态。

## 当前稳定面

当前主干包含：

- Go runtime、provider adapter、工具注册、技能目录和文件型 session；
- CLI 的 init/run/exec/continue/steer/sessions/goal/tasks/models/probe-provider/doctor；
- 本地 Web 控制台、REST、WebSocket 和嵌入式无构建前端；
- Goal、Plan Mode、提问、审批 CAS、receipt、恢复和实时活动；
- Workspace、Skills、Settings、history、task/children/context 观测；
- queue、child、isolation、TUI 和 browser tools 等显式扩展面；
- 无凭据 Node/Go 测试、确定性 Playwright E2E、静态分析、漏洞扫描和 race gate。

“稳定面”表示已经有代码和对应验证入口，不表示所有 provider、平台和部署形态都经过本次运行。

## 改动优先级

### 1. 先保护事实和安全

任何新能力先确认 session、事件、state、审批、路径、权限、shell 和 provider side effect 的事实边界。优先补失败测试，再修改实现。

### 2. 再扩展 runtime 契约

需要新行为时先改 core runtime、session store、provider adapter 或 tool registry，再接 CLI、Web DTO 和 UI。不要通过页面 state 临时实现 runtime 行为。

### 3. 最后扩展 operator surface

默认 Web 页面保持 chat/session-first。高级 child、queue、isolation、TUI 和 browser 能力用显式命令、配置或 inspector 入口暴露，不把默认首页变成 orchestration dashboard。

### 4. 每个改动闭环

同一提交必须能回答：当前行为是什么、改变了什么、事实如何落盘、失败如何恢复、哪些命令验证过、哪些平台未验证。

## 允许的路线分支

| 路线 | 适用改动 | 必须保持 |
| --- | --- | --- |
| runtime | loop、session、provider、tool、compaction、control | Web/CLI 共享 facade，旧事实可恢复 |
| Web service | API、DTO、WebSocket、audit、worker | Host/Origin/JSON/CSP/size/route 边界 |
| Web frontend | state、页面、交互、i18n、样式 | 无构建、嵌入、双语、durable 原文和 test IDs |
| automation | CLI、stream-json、run.sh、fixture | stdout/stderr、exit code、配置准入和 Bash/macOS 边界 |
| extension | child、queue、isolation、TUI、browser | 显式入口、平台限制和默认关闭/保守默认 |
| documentation | README、AGENTS、spec、validation、sample | 只写当前事实，链接和命令可执行 |

## 完成判据

一个变更轨道只有在以下条件都满足时才可合并：

- 所有受影响的 source/test/doc 使用同一术语和路径；
- 新增或改变的事实有持久化、API 或输出契约；
- 失败、取消、重启、重复请求和并发路径有覆盖或明确列为未验证风险；
- 所有必要的本地命令通过，CI 必需检查已运行或在 PR 中明确等待；
- diff 不含凭据、binary、session、live transcript 和本地运行目录。

## 不做的路线决策

本文件不提前承诺某个模型、provider、UI framework、远程部署或固定发布日期。此类决定必须由实际需求、源码契约和可复现验证共同确定。
