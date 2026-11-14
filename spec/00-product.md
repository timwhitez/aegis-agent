# 产品契约

## 产品是什么

Aegis Agent 是一个运行在用户机器上的 agent harness。它连接一个或多个模型 provider，向模型提供文件、搜索、shell、技能、计划、任务和控制工具，并把执行过程写成可以继续、审计和恢复的本地 session。

模型是任务决策者。harness 负责把能力暴露成稳定工具协议、把执行事实写入磁盘、把用户控制接入运行中的 session，并在路径、权限、协议和恢复边界上拒绝不安全操作。

## 两个产品表面

| 表面 | 用途 | 事实来源 |
| --- | --- | --- |
| 本地 Web 控制台 | 面向 operator 的默认交互入口；查看会话、实时活动、Goal、Plan Mode、Workspace、Skills 和 Settings | 本地 HTTP 服务、session store、runtime facade |
| CLI | 交互运行、脚本、CI、故障恢复、provider 探测和低层查询 | CLI adapter、同一个 runtime 和 session store |

二者可以同时存在，但不能产生两个不同的 session 状态。Web 浏览器中的 store 只负责显示和暂存交互状态，不能成为执行权威源。

## 当前用户路径

### 启动一个任务

用户选择一个明确配置，启动 Web 或 CLI，提交 prompt。runtime 创建 session，记录 provider/model/workdir 等启动快照，然后进入 model → tool → result 的循环。

### 观察和控制

Web 通过 REST 读取事实，通过 WebSocket 接收实时事件。用户可以继续已有 session、发送 steer、interrupt 或 stop；这些控制必须经过 runtime 的 active handle 和 session 状态检查。

### 计划和审批

Plan Mode 和 Goal 都是 durable session 事实。用户看到当前版本和 reviewed target 后，才能批准、修订、取消或回答。审批请求带 request ID、版本和 revision；相同操作的重放不能再次造成 provider 执行。

### 恢复

中断、服务重启或响应丢失后，用户先读取 session、状态和审批 receipt，再用普通 continue 或显式审批恢复。系统不根据浏览器状态猜测上一次 provider 请求是否已经产生副作用。

### 工作区和技能

Workspace 操作经过服务端路径检查，列出文件时返回大小和修改时间；Skills 经过目录扫描、解析和信任边界后才能加载。二者都属于本地 operator 权限，不是远端项目管理服务。

## 产品边界

### 提供

- 本地、文件型、可恢复的 session；
- OpenAI Responses、Anthropic Messages、Google Gemini 和 OpenAI-compatible provider adapter；
- 内置工具、skill catalog、Goal、Plan Mode、Todo/Task、compaction、steer、queue 和有限的 child session 能力；
- Web 控制台与 CLI 的同一 runtime 适配；
- Host、Origin、路径、shell、输出、审批和审计边界；
- 无真实凭据的确定性单测与浏览器验收。

### 不提供

- hosted 多用户服务、账号体系、云端 session 同步；
- 远程终端或浏览器 IDE；
- 浏览器端替代 session store 的权威状态；
- 固定 DAG、强制审计流程或按任务硬编码的 workflow engine；
- 原生 Windows 构建目标；
- 将可选 browser tools 或 Linux sandbox 伪装成所有平台都可用的默认能力。

## 数据与安全原则

- 默认数据留在用户配置的 session、workspace、skills 和 artifact 目录。
- provider key 通过进程环境或受控 `.env` 读取，不写入公开文档；Web Settings 写入 `.env` 时保留现有文件安全检查。
- session 与 agent 产物默认 owner-only；原始消息、工具负载、路径和模型输出按原文保存，不由 UI 翻译或擅自脱敏。
- Web 默认 loopback。非回环监听会警告；使用网络暴露时，用户必须自己提供受信任网络、Host allowlist、Basic Auth 和 HTTPS。
- 文件工具必须同时防范普通路径穿越和 symlink 穿越；shell 必须有 timeout、输出上限和环境变量白名单。

## 能力状态的写法

文档中使用以下区分：

- **当前实现**：源码路径和测试可以直接证明的行为；
- **可选能力**：需要显式配置、安装或进入 `experimental` 的行为；
- **兼容读取**：代码可以读取旧字段或旧事实，但不会把它作为当前入口继续写出；
- **计划**：尚未由当前代码和 CI 覆盖的未来工作。

产品文档只把前两类写入当前能力列表，不能把兼容读取写成产品入口，也不能把计划写成已完成。

## 产品级验收

一个改动只有在以下事实一致时才算完成：

1. Web、CLI 和 SDK 对同一 session 的关键事实含义一致；
2. 数据、控制和审批重启后仍能从文件恢复，或明确报告不可恢复；
3. 安全拒绝发生在正确的边界，错误不会被 UI 静默吞掉；
4. 文档、测试、CI 命令和实际路径一致；
5. 交付结果不包含凭据、真实敏感数据或无法复现的“已验证”表述。
