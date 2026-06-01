# Multica 侧开发指南

目标：在 Multica 中新增 `gocli` backend，使 daemon 可以启动、观测、恢复 `go-cli-agent`。

复核基准：Multica upstream HEAD `41a1ca58add47f53bb64ddc6aa02be2d9a73faa9`。

## 当前代码事实

- Backend 接口在 `server/pkg/agent/agent.go`。
- Claude/Cursor/Gemini 等已有 stream-json 子进程 backend，可复用模式。
- `newStderrTail` 签名是 `newStderrTail(inner io.Writer, max int)`。
- `withAgentStderr` 签名是 `withAgentStderr(msg, label, tail string)`。
- daemon 只会注册 `server/internal/daemon/config.go` 探测到的 agents。
- `ListModels`、`CheckMinVersion`、`IsKnownThinkingValue`、`ModelSelectionSupported` 分散在 `server/pkg/agent`，新增 backend 必须全部注册。
- Multica execenv 会按 provider 写 runtime context 和 skills；`gocli` 需要按 go-cli-agent 的 `AGENTS.md` + `./skills` 约定接入。

## 改动范围

| 文件 | 操作 | 说明 |
| --- | --- | --- |
| `server/pkg/agent/gocli.go` | 新增 | backend 实现，解析 `gocli-stream-json` |
| `server/pkg/agent/gocli_test.go` | 新增 | output parsing、args、mock execution |
| `server/pkg/agent/agent.go` | 修改 | `New("gocli")`、`launchHeaders`、注释、factory 测试 |
| `server/pkg/agent/models.go` | 修改 | `ListModels("gocli")` 动态调用 `go-cli-agent models --json` |
| `server/pkg/agent/version.go` | 修改 | `MinVersions["gocli"] = "0.1.0"` |
| `server/pkg/agent/thinking.go` | 修改 | `providerThinkingEnums["gocli"]` |
| `server/internal/daemon/config.go` | 修改 | `MULTICA_GOCLI_PATH` / `MULTICA_GOCLI_MODEL` / `MULTICA_GOCLI_ARGS`、默认探测 |
| `server/internal/daemon/daemon.go` | 修改 | `defaultArgsForProvider` 支持 `gocli` |
| `server/internal/daemon/execenv/context.go` | 修改 | `gocli` skills 写到 `{workDir}/skills` |
| `server/internal/daemon/execenv/runtime_config.go` | 修改 | `gocli` runtime brief 写到 `{workDir}/AGENTS.md` |
| 相关测试 | 修改 | agent factory、model list、thinking、version、daemon config、execenv |

## 双端对齐原则

| 事项 | go-cli-agent 侧 | Multica 侧 |
| --- | --- | --- |
| 协议类型 | `internal/streamjson.StreamOutputMessage` | `server/pkg/agent.gocliOutputMessage` |
| 模型发现 | `go-cli-agent models --json` 输出 `<provider>/<model>` route ID | `discoverGocliModels()` shell out；执行时拆成 `--provider` + `--model` |
| 版本发现 | `go-cli-agent --version` | 复用 `DetectVersion()` |
| thinking level | `low|medium|high|xhigh`，由 gocli 映射到 provider options | `IsKnownThinkingValue("gocli", value)` 只做粗粒度枚举校验 |
| prompt 输入 | stdin 一条 user JSON envelope | `json.NewEncoder(stdin).Encode(...)` 后关闭 stdin |
| cancellation | context/process cancellation | 不发送 stdin control |
| session resume | `exec --resume <id>` 内部 runtime Continue | `opts.ResumeSessionID` 映射到 `--resume` |
| resume thinking | `ContinueRequest.ProviderOptions` 支持 additive override | resume 时仍传 `--thinking-level` |
| skills/context | 读取 `AGENTS.md` 和 `./skills` | execenv 写 `AGENTS.md` 和 `skills/` |

配置路径必须特别处理：Multica 当前 `ListModels(ctx, provider, executablePath)` 只传 executable path，不传 execution args。若 go-cli-agent 使用非默认 config，推荐在 daemon 环境设置 `GO_CLI_AGENT_CONFIG=/path/to/config.yaml`，使 `models --json` 与 `exec` 读取同一配置；`MULTICA_GOCLI_ARGS --config ...` 只影响 task execution，不能作为模型发现的配置来源。

skill 语义必须跟 Multica workspace shared skills 对齐：成员创建、从 URL 导入、或从本地运行时复制后的 skills 由 Multica 按 agent 当前配置注入到任务工作区 `skills/`，随后 gocli 通过 `./skills` 自动发现。不要把 `~/.codex/skills` 放进 gocli task config；本地运行时 skills 在复制进 Multica workspace 前是私有来源，不是所有 gocli agent 的默认共享上下文。

`gpt-5.5` 的上下文与默认压缩必须按部署机 Codex 元数据对齐。当前远端 Codex `debug models` 对 `gpt-5.5` 返回 `context_window=272000`、`effective_context_window_percent=95`；go-cli-agent v1 compactor 使用字符数近似，因此 Multica gocli 全局配置应给 `runtime.compact.context_profiles.openai/gpt-5.5` 设置 `input_char_threshold: 1033600`（`272000 * 0.95 * 4`）和 `hysteresis_delta_chars: 258400`，不要沿用通用 `160000` 字符默认。

## 开发 Phase

1. `server/pkg/agent` backend 与单测
2. model/version/thinking/factory 注册
3. daemon config 自动发现
4. execenv context/skills 路径对齐
5. mock E2E
6. 真实 `go-cli-agent` E2E

## 不做

- 不改数据库 schema；runtime provider 是 string。
- 不改前端组件；复用现有 runtime/model/thinking UI。
- 不实现 ACP；MVP 采用 stream-json。
- 不解析 go-cli-agent session 目录；Multica 只消费 stdout 协议和 Result.SessionID。

## Mission Control 对齐

Multica 可以在 `gocli` 之上提供 [mission-compatible profile](../mission-profile.md)，但职责边界必须清晰：

- Multica owns：mission plan、validation contract、milestone、follow-up feature、Mission Control 聚合视图。
- go-cli-agent owns：本地 runtime loop、tool execution、provider adapter、session id、events、messages、artifacts。
- `gocli` backend 只解析 [../wire-protocol.md](../wire-protocol.md) 的 stdout/stdin 协议，不读取 `go-cli-agent` 私有 session schema。
- `AGENTS.md` / `skills/` / system prompt 用于传入角色、验证契约摘要和当前 feature 规格；不要在 backend 里写死 orchestrator / worker / validator 流程。
- `run_role`、`metadata`、`handoff` 都是可选 additive 字段。当前 Multica Result 类型如果没有承载位置，parser 必须忽略它们；后续 mission store 可选择保存。
- Mission Control 展示应来自 Multica mission store、stream-json event、Result.SessionID、usage 和 artifact refs 的组合，不维护浏览器端第二事实源。
