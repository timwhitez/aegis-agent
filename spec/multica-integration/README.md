# Multica 集成开发 SPEC

本目录定义 `aegis-agent` 作为 Multica 后端 agent runtime 的二次开发方案。本轮只写文档，不修改代码。

## Review 结论

截至 2026-06-01，当前 `aegis-agent` 不能直接接入 Multica。复核依据：

- `aegis-agent` 当前工作树代码：`internal/app/app.go`、`internal/runtime/engine.go`、`internal/runtime/runner.go`、`internal/config/config.go`
- Multica upstream：`https://github.com/multica-ai/multica`，HEAD `41a1ca58add47f53bb64ddc6aa02be2d9a73faa9`

阻塞差距如下：

| 差距 | 结论 | 修正方案 |
| --- | --- | --- |
| stdout 协议不兼容 | 阻塞 | `aegis-agent` 新增 `--output-format stream-json`，由 adapter 把 runtime events 转成 Multica 可消费的 NDJSON |
| stdin 协议不兼容 | 阻塞 | `aegis-agent exec` 新增 `--input-format stream-json`，从 stdin 读取一条 user envelope 作为 prompt |
| 顶层版本发现缺失 | 阻塞 | `aegis-agent --version` 输出含 semver 的版本行，供 Multica `DetectVersion` 使用 |
| 模型发现缺失 | 阻塞 | `aegis-agent models --json` 输出 Multica `agent.Model` 兼容结构 |
| tool 事件缺少 call id | 阻塞 | `tool.before` / `tool.after` event data 增加 `call_id`，Multica 才能关联 `tool_use` 和 `tool_result` |
| thinking 未进入事件流 | 中等 | `assistant.message` event data 增加 `thinking`，stream-json adapter 输出 `thinking` block |
| usage 暴露方式误判 | 已修正 SPEC | 当前代码已经在 `turn.stopped.data.usage` 暴露 usage；不要新增 `turn.usage` 事件 |
| Multica daemon 自动发现遗漏 | 阻塞 | Multica 需在 daemon config probe、runtime registration、execenv、model discovery、thinking enum 中注册 `gocli` |

## Review 修正点

上一版文档中有几处会误导实现，本版已修正：

| 原描述 | 问题 | 本版修正 |
| --- | --- | --- |
| 新增 `turn.usage` event | 当前 `engine.go` 已有 `turn.stopped.data.usage`；新增事件会引入重复事实源 | stream-json adapter 消费 `turn.stopped` |
| `config.Load(*configPath)` | 当前签名是 `config.Load(explicitPath, cwd)` | `models` 命令复用 `loadConfig(configPath, cwd)` |
| `apiProvider == "openai"` | 当前 `EffectiveAPIProvider` 返回 `openai-compatible` / `anthropic-compatible` / `google` | 模型/思考映射按 effective API provider 判断 |
| model ID 只写 provider 原生 model | Multica 只有 `opts.Model`，无法同时表达 aegis-agent provider profile | `models --json` 使用 `<provider>/<model>` route ID，Multica backend 拆成 `--provider` + `--model` |
| Multica `newStderrTail(8192)` | 当前签名是 `newStderrTail(inner io.Writer, max int)` | 使用 `newStderrTail(newLogWriter(...), agentStderrTailBytes)` |
| Multica `withAgentStderr(msg, stderr)` | 当前签名是 `withAgentStderr(msg, label, tail)` | 使用 `withAgentStderr(msg, "gocli", stderrBuf.Tail())` |
| Multica 只改 `server/pkg/agent` | daemon 不会自动发现/register `gocli` | 增补 `server/internal/daemon/config.go`、`daemon.go`、`execenv/*` 修改 |
| stdin control/cancel 作为 MVP | Multica 现有 backends 用 context/process cancellation，不需要 stdin control | MVP stdin 只读一条 user prompt，然后关闭 |
| `MULTICA_GOCLI_ARGS --config ...` 同时影响模型发现 | Multica `ListModels` 当前只接收 executable path，不接收 execution args | 非默认 aegis-agent config 用 daemon 环境 `AEGIS_AGENT_CONFIG` 对齐 `models --json` 和 `exec` |
| `gpt-5.5` 仍用 aegis-agent 160k 字符默认压缩 | 远端 Codex `debug models` 的 `gpt-5.5` 默认窗口是 `context_window=272000`、`effective_context_window_percent=95` | aegis-agent 内置 known-model 表令 `gpt-5.5` 默认 context window=300000，自动推导 `input_char_threshold=1020000`（`300000*4*0.85`）；如需与 Codex 精确对齐用最高优先级覆盖 `context_profiles.openai/gpt-5.5.input_char_threshold=1033600`、`hysteresis_delta_chars=258400`，或 `context_window_tokens=272000` |
| gocli 直接扫描 Codex 全局 skills | Multica workspace skills 才是任务共享事实源；本地运行时 skills 在复制/导入前是私有来源 | gocli task config 只扫描 `./skills`；Multica execenv 按 agent 当前 skill 集动态写入 `{workDir}/skills` |

## 目录结构

```text
spec/multica-integration/
├── README.md
├── wire-protocol.md
├── mission-profile.md
├── agent-side/
│   ├── README.md
│   ├── 01-implementation.md
│   └── 02-test-plan.md
└── multica-side/
    ├── README.md
    ├── 01-backend-driver.md
    └── 02-test-plan.md
```

## 开发拆分

两端只通过 [wire-protocol.md](wire-protocol.md) 对齐。禁止让任一侧依赖另一侧未导出的 Go 类型。

| 侧 | 独立交付物 | 可独立验证 |
| --- | --- | --- |
| `aegis-agent` | `internal/streamjson`、CLI flags、`models --json`、`--version`、event 字段补齐 | 用 shell 管道和 `jq` 校验 NDJSON、usage、tool call id、models |
| Multica | `server/pkg/agent/gocli.go`、factory/model/version/thinking 注册、daemon probe、execenv 路径 | 用 mock `aegis-agent` 脚本驱动 `go test ./server/pkg/agent` 和 daemon config/execenv 单测 |

## Mission 风格优化原则

本 SPEC 吸收 Missions 类长任务系统的设计思路，但只把它们落成可选的 large-project / mission-compatible profile，详见 [mission-profile.md](mission-profile.md)。MVP 的 `gocli` backend 不需要实现 mission engine。

集成层原则：

- 人类注意力是瓶颈，因此协议必须让 Multica 能异步监督 `session_id`、status、usage、tool stream、stderr tail、handoff artifact 和验证结果。
- Multica 可以承担 mission orchestrator / Mission Control 职责；`aegis-agent` 仍只承担本地 agent runtime、session facts、tool execution 和 provider adapter。
- 验证契约必须在编码前定义，并与实现聊天上下文分离；Multica mission store 或 aegis-agent Goal 内部计划可以保存 contract，`wire-protocol.md` 只传递可选 metadata / artifact ref。
- worker / validator 完成时应留下 structured handoff：completed、remaining、commands、artifacts、risks、validation evidence。
- 执行策略默认串行为主、局部只读并行；并行写入必须显式启用 isolation，并留下 workdir / merge 证据。
- 角色化模型选择由 Multica 策略层决定，通过 `--provider` / `--model` / `--thinking-level` 传给 aegis-agent；provider replay 差异仍留在 aegis-agent adapter 层。
- discipline 由系统事实源、coverage check、handoff check 和 plan approval gate 提供；智能仍由模型和 skill/prompt 决策提供，不能把 runtime 写成任务专用 DAG。

## 集成后命令

Multica daemon 启动 `aegis-agent` 的标准形态：

```bash
aegis-agent exec \
  --output-format stream-json \
  --input-format stream-json \
  --workdir <task-workdir> \
  [--timeout <seconds>] \
  [--provider <aegis-agent-provider>] \
  [--model <model-id>] \
  [--thinking-level <low|medium|high|xhigh|max>]
```

`--timeout` 只在 Multica runtime profile 配置了正数 `Timeout` 时传递。Long-horizon
profile 可以把 `Timeout` 设为 `0`，此时 Multica 不安装固定执行时长，也不向
aegis-agent 传 `--timeout`；任务生命周期由用户/daemon 取消和显式 idle watchdog
负责。

恢复 session 时仍走同一个 `exec` 入口，额外带 `--resume <session-id>`；`aegis-agent` 内部调用 runtime `Continue`。
本 SPEC 锁定 `ContinueRequest.ProviderOptions` 扩展，因此 resume 时 `--thinking-level` 与首次启动一样生效。

## 非目标

- 不实现 ACP / JSON-RPC；Multica 现有 stream-json backend 形态足够支撑 MVP。
- 不把 Multica 写成 aegis-agent provider adapter；Multica 只管理子进程和 wire protocol。
- 不改 `aegis-agent` provider adapters、session store schema 或 Web console 权威状态。
- 不让 `aegis-agent` 默认 Web 页面围绕 Multica 做产品复杂化；本方案只增加 CLI/adapter 兼容面。
- 不把 Missions / Mission Control 做成 `gocli-stream-json` MVP 的必选流程；mission profile 只能通过 additive protocol fields、artifact refs 和显式高级入口演进。

## 完成门禁

完成开发后必须同时满足：

- `aegis-agent --version` 可被 Multica `versionRe` 解析。
- `aegis-agent models --json` 可直接 unmarshal 到 Multica `[]agent.Model` 等价结构，且每个发现模型的 `id` 是 `<provider>/<model>` route key。
- `aegis-agent exec --output-format stream-json --input-format stream-json` stdout 每行都是合法 JSON，stderr 不混入协议。
- `tool_use.id == tool_result.tool_use_id`，且来自真实 runtime `call.ID`。
- result 行包含 `session_id`、`status`、`is_error`、`usage`。
- consumer 忽略未知字段；可选 mission metadata / handoff 字段缺失时不影响 MVP 执行。
- Multica `agent.New("gocli", ...)`、`ListModels("gocli", ...)`、`CheckMinVersion("gocli", ...)`、`IsKnownThinkingValue("gocli", ...)` 都通过单测。
- Multica daemon 能通过 `MULTICA_GOCLI_PATH` 或 PATH 自动注册 `gocli` runtime。
