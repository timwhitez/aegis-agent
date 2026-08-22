# aegis-agent 侧开发指南

目标：让 Multica daemon 可以把 `aegis-agent` 当作本地子进程 backend，通过 `gocli-stream-json` 协议启动、观测和恢复任务。

## 当前代码事实

基于 2026-06-01 当前工作树：

- CLI 入口是 `internal/app/app.go`，使用标准库 `flag.NewFlagSet` 和手写 `switch`。
- `runCommand()` 当前只支持 `run` / `exec` 启动新 session，不支持 `--resume`。
- `aegis-agent` 当前没有顶层 `--version`，Multica runtime 注册会失败。
- `aegis-agent` 当前没有 `models` 子命令。
- 当前 `--json` 输出是内部 `events.Event` JSON，不是 Multica stream-json transcript 协议。
- `engine.go` 已在 `turn.stopped.data.usage` 暴露 usage，不能再设计一个重复的 `turn.usage` 事实源。
- `tool.before` / `tool.after` event data 当前没有 `call_id`，需要补齐。
- `assistant.message` event data 当前没有 `thinking`，如果不补，Multica 无法展示 `MessageThinking`。
- `runtime.StartRequest` 已有 `ProviderOptions session.ProviderOptions`；`ContinueRequest` 当前没有，本 SPEC 锁定为新增 `ProviderOptions`，使 `--resume --thinking-level` 与首次 start 行为一致。

## 改动范围

| 文件 | 操作 | 说明 |
| --- | --- | --- |
| `internal/runtime/engine.go` | 修改 | event data 增加 `thinking`、`call_id`；继续复用 `turn.stopped.data.usage` |
| `internal/runtime/runner.go` | 修改 | 给 `ContinueRequest` 增加 `ProviderOptions`，resume 时 additive merge 到 durable `meta.ProviderOptions` |
| `internal/streamjson/output.go` | 新增 | wire output types |
| `internal/streamjson/input.go` | 新增 | stdin user envelope parser |
| `internal/streamjson/adapter.go` | 新增 | `events.Event` -> stream-json NDJSON |
| `internal/streamjson/models.go` | 新增 | `models --json` output types and config-derived catalog |
| `internal/app/models_cmd.go` | 新增 | `aegis-agent models --json` |
| `internal/app/version.go` | 新增 | `Version` variable and version output helper |
| `internal/app/app.go` | 修改 | `--version` / `version` / `models` dispatch；`exec` stream-json flags；resume branch |
| `internal/app/app_test.go` | 修改 | CLI flag, output, version, models tests |
| `internal/runtime/engine_test.go` | 修改 | event field tests |

不改：

- provider adapters
- session store schema
- Web console service/state
- default root README product narrative

## 开发 Phase

1. Runtime event alignment
   - `assistant.message` event 增加 `thinking`
   - `tool.before` / `tool.after` event 增加 `call_id`
   - 测试证明 `turn.stopped.data.usage` 已覆盖 usage

2. Stream-json package
   - 输出 envelope/types
   - stdin prompt parser
   - events adapter
   - model list structs/helpers

3. CLI integration
   - 顶层 `--version`
   - `models --json`
   - `exec --output-format stream-json --input-format stream-json`
   - `exec --resume <session-id>`
   - `--thinking-level` 到 `ProviderOptions`

4. Independent validation
   - 不依赖 Multica，直接用 shell + `jq` 验证 NDJSON、usage、tool id、models、version

## 与 Multica 的唯一对齐点

只对齐 `../wire-protocol.md`：

- stdout envelope
- stdin user prompt envelope
- model list schema
- exit code/status
- thinking level vocabulary

aegis-agent 侧不导入 Multica 代码，不复制 Multica `agent.Message` / `agent.Result` 类型。

## Mission-compatible profile 边界

`aegis-agent` 侧只提供 [mission profile](../mission-profile.md) 所需的 runtime primitive，不实现 Multica Missions 编排器：

- session / messages / events / goal / plan / artifacts 仍是本地文件事实源。
- `exec` 只执行一次明确 prompt 或 resume prompt，不根据 mission metadata 自动创建 worker / validator / follow-up feature。
- role 只作为可选 hint 和 traceability 信息，未来可通过 `run_role` metadata、system prompt、Goal 内部计划或 child `agent_role` 进入 session；不要从 role hint 推导固定 DAG。
- structured handoff 优先来自 agent 写出的 visible artifact，例如 `reports/handoff.json`、`reports/progress.md`、`reports/validation.md`；如果后续 stream-json 输出 `result.handoff`，也只能作为这些事实的摘要。
- validation contract 的主事实属于 Goal / Plan Mode / Multica mission store；stream-json adapter 不承担 coverage checker。
- evaluator / validator run 应在干净上下文中基于 contract、artifact、workdir 和可运行行为检查，不依赖 worker 的完整聊天上下文。

本目录实现仍以 [../wire-protocol.md](../wire-protocol.md) 的 MVP 字段为完成门禁；mission 字段只能 additive，并且缺失时不得影响普通 Multica task execution。
