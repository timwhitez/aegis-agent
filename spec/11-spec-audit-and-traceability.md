# 规范索引与可追溯性

这份文件是当前文档和实现之间的索引，不是把历史评审结论堆在一起的 changelog。每条当前契约都应能沿着“文档 → 源码 → 测试/命令”追溯。

## 契约索引

| 契约 | 规范入口 | 主要实现 | 主要验证 |
| --- | --- | --- | --- |
| 产品边界和本地数据模型 | `00-product.md` | `internal/runtime`、`internal/session` | runtime/session tests |
| runtime 分层与执行循环 | `01-runtime-architecture.md` | `internal/runtime/runner.go`、`engine.go` | runtime integration tests |
| 命令与配置加载 | `02-cli-and-config.md` | `internal/app`、`internal/config` | config/app tests、`doctor` |
| provider adapter | `03-provider-contracts.md` | `internal/provider` | provider contract/live fixture tests |
| tools 与 skills | `04-tools-and-skills.md` | `internal/tools`、`internal/skills` | registry/path/skill tests |
| session 中断与继续 | `05-session-interrupt-resume.md` | `internal/session`、`internal/runtime/control.go` | recovery/control tests |
| hooks | `06-hooks.md` | `internal/hooks`、runtime hooks | hook tests |
| 测试与 CI | `07-testing-strategy.md` | `test.sh`、`.github/workflows/ci.yml` | local gate、CI |
| compaction | `10-context-compaction.md` | `internal/runtime/compaction.go`、`internal/session` | compaction tests |
| Web 产品与服务 | `17-web-console.md` | `internal/webconsole/service.go` | service tests、E2E |
| Web 前端实现 | `17-web-console-frontend.md` | `internal/webconsole/ui` | Node tests、E2E |
| Web audit | `14-web-audit-log.md` | `internal/webconsole/audit*.go` | audit/recovery tests |
| child/isolation | `14-multi-agent-and-isolation.md` | `internal/runtime/delegation.go`、tools | child/queue tests |
| queue/background | `15-background-queue.md` | `internal/runtime/auto_queue.go`、session queue | lease/liveness tests |
| browser tools | `19-browser-tools.md` | `internal/tools/browser*.go` | platform/browser tests |

专题文档仍可能包含历史兼容名；引用它们时必须以当前源码和测试确认该段是否仍是有效契约。

## 当前必须保持的跨模块不变量

1. Web、CLI、SDK 对同一 session 读取同一 durable 事实，不把 UI store、process handle 或 provider response 当成长期权威。
2. provider-specific 协议只在 adapter 层出现；调用表面使用统一 runtime request/result。
3. 文件、shell、Host、Origin、JSON body、CSP、输出限额和权限边界由服务端/工具端执行，前端只负责提示。
4. compaction 生成 provider view，但不覆盖原始 messages/events/artifacts。
5. interrupt、stop、continue、steer 和审批必须区分观察、控制、恢复和新执行，不以一次请求的 HTTP 成功代替执行已完成。
6. 相同 approval request 的重放不能重复 provider side effect；新 revision 必须重新审阅。
7. 失败发送、断线、服务重启和 owner 丢失需要可解释地进入恢复路径。
8. durable 原文、路径、ID、模型输出、工具 payload 和错误不会被 i18n 词典改写。

## 文档审查清单

### 事实

- 命令在 `internal/app` 中存在吗？
- 路由、字段、默认值和文件名在当前代码中存在吗？
- 该段说的是当前实现、可选能力、兼容读取还是计划？

### 安全

- 是否保留 loopback、Host、Origin、Basic Auth、CSP、路径和 shell 边界？
- 是否出现真实凭据、token、API key、用户 prompt 或敏感 transcript？
- 是否把测试 fixture、源码声明或单机结果误写成所有平台/所有 provider 已验证？

### 验证

- 是否给出可复制的最小命令？
- 变更对应的 Go、Node、E2E、race、静态分析或漏洞检查是否被说明？
- 运行产物是否放在忽略目录，未混入源码？

### 维护

- 根 README、AGENTS、CONTRIBUTING、validation README 和 spec 链接是否互相一致？
- 旧资源路径、旧 CLI 顶层入口和旧 UI 文件名是否被误写成当前入口？
- 文档是否把一次性的分支、PR、外部环境或评审数字写成长期契约？

## 证据等级

- **源码契约**：实现直接声明或执行的行为；
- **测试契约**：测试明确断言的行为；
- **运行证据**：某次本地/CI 命令在具体 commit 上通过；
- **外部限制**：provider、平台或网络环境的限制，需要注明来源和时点。

文档中的“支持”“通过”“安全”“兼容”必须能落到至少一个证据等级；不能用推测填补没有证据的区域。

## 退役入口

当前 Web 资源只从 `/ui/*` 提供，SPA 路由由嵌入的 index shell 处理；`/legacy`、`/v2-assets` 和 `/shared-assets` 及其子路径明确返回 404。`web.legacy_ui_enabled` 只为旧配置可解析而保留，不是功能开关。

顶层 `delegate`、`children`、`queue`、`tui` 已移动到 `experimental`。文档、示例和 PR 描述不能继续给旧命令或旧资源路径做当前用法示例。
