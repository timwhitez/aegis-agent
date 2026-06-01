# go-cli-agent 侧测试计划

本测试计划不依赖 Multica。

## 1. 单元测试

### 1.1 `internal/runtime/engine_test.go`

新增或调整测试，证明：

- `assistant.message` event data 包含 `thinking`。
- `tool.before` event data 包含 `call_id`。
- `tool.after` event data 包含 `call_id`。
- `turn.stopped.data.usage` 包含 `input_tokens`、`output_tokens`、cache counters。
- 没有新增 `turn.usage` event。

建议断言：

```go
before, _ := findEventByType(events, "tool.before")
if before.Data["call_id"] != "call_test" {
    t.Fatalf("tool.before call_id missing: %#v", before.Data)
}

stopped, _ := findEventByType(events, "turn.stopped")
usage := stopped.Data["usage"].(map[string]any)
if usage["input_tokens"] == nil || usage["output_tokens"] == nil {
    t.Fatalf("usage missing from turn.stopped: %#v", stopped.Data)
}
```

### 1.2 `internal/streamjson/adapter_test.go`

覆盖：

- `session.started` -> one `system` line。
- `session.created` 被忽略。
- `assistant.message` 同时含 `thinking` 和 `text` 时输出两个 blocks。
- 空 `assistant.message` 不输出。
- `tool.before` -> `assistant/tool_use`，`id` 来自 `call_id`。
- 无效 `arguments` 不 panic。
- `tool.after` -> `user/tool_result`，`tool_use_id` 来自 `call_id`。
- 多个 `turn.stopped.data.usage` 累计到最终 result。
- `WriteResult(..., exitCode=6)` 输出 `is_error=true` 且调用方返回 `ExitError{Code: 6}`。
- `MarshalLine` 保留可选 `run_role`、`metadata`、`handoff` 字段；字段为空时不输出。
- stream-json adapter 在未实现 mission profile 时不需要伪造 `handoff`。

### 1.3 `internal/streamjson/input_test.go`

覆盖：

- 单 text block。
- 多 text block 用 `\n` 拼接。
- 空 stdin 报错。
- invalid JSON 报错。
- 非 `type=user` 报错。
- 非 `role=user` 报错。
- 超过最大输入报错。
- input envelope 带未知字段、`run_role` 或 `metadata` 时仍能读取 prompt；这些字段不改变 MVP 执行语义。

### 1.4 `internal/app/app_test.go`

覆盖：

- `go-cli-agent --version` 输出含 semver。
- `go-cli-agent version` 同样可用。
- `models --json` 输出 JSON array。
- `exec --output-format stream-json --json` 互斥报错。
- `exec --input-format stream-json` 从 stdin envelope 读取 prompt。
- `exec --resume <id>` 调用 fake runner 的 `Continue`，不是 `Start`。
- `run --resume <id>` 报错；Multica 恢复只使用 `exec --resume`。
- `exec --resume <id> --thinking-level xhigh` 传入 `ContinueRequest.ProviderOptions`。
- stream-json 模式最终写 result envelope。
- 非 stream-json 的 `--json` 行为不变。

## 2. 冒烟脚本

构建：

```bash
go build -o /tmp/go-cli-agent ./cmd/go-cli-agent
```

### 2.1 版本

```bash
/tmp/go-cli-agent --version | grep -E 'v?[0-9]+\.[0-9]+\.[0-9]+'
```

### 2.2 模型发现

```bash
/tmp/go-cli-agent models --json | jq -e 'type == "array" and length > 0 and (.[0].id | contains("/"))'
```

### 2.3 stream-json 输入输出

```bash
printf '%s\n' '{"type":"user","message":{"role":"user","content":[{"type":"text","text":"say exactly: smoke ok"}]}}' \
  | /tmp/go-cli-agent exec --output-format stream-json --input-format stream-json --timeout 120 \
  | tee /tmp/gocli-stream.jsonl \
  | while IFS= read -r line; do
      [ -z "$line" ] && continue
      echo "$line" | jq . >/dev/null
    done
```

检查 result：

```bash
grep '"type":"result"' /tmp/gocli-stream.jsonl \
  | tail -1 \
  | jq -e '.session_id and .status and (.usage.input_tokens // 0) >= 0'
```

### 2.4 tool id 对齐

让 agent 触发一次 tool：

```bash
WORKDIR="$(mktemp -d)"
printf '%s\n' '{"type":"user","message":{"role":"user","content":[{"type":"text","text":"list files in the current directory, then finish"}]}}' \
  | /tmp/go-cli-agent exec --output-format stream-json --input-format stream-json --workdir "$WORKDIR" --timeout 120 \
  > /tmp/gocli-tools.jsonl
```

检查：

```bash
TOOL_ID="$(jq -r 'select(.type=="assistant") | .message.content[]? | select(.type=="tool_use") | .id' /tmp/gocli-tools.jsonl | head -1)"
RESULT_ID="$(jq -r 'select(.type=="user") | .message.content[]? | select(.type=="tool_result") | .tool_use_id' /tmp/gocli-tools.jsonl | head -1)"
test -n "$TOOL_ID"
test "$TOOL_ID" = "$RESULT_ID"
```

## 3. Go test 命令

```bash
go test ./internal/streamjson ./internal/app ./internal/runtime
```

```bash
go test ./internal/runtime ./pkg/agent
```

并增加 runtime 断言：resume 时 `req.ProviderOptions` 只覆盖 thinking/reasoning 字段，不清空 session metadata 中已有的 timeout/retry/store/prompt-cache 选项。

完整回归建议：

```bash
go test ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...
```

## 4. 文档/代码一致性检查

开发完成后，这些搜索应成立：

```bash
# 不应存在新事件事实源
! rg '"turn.usage"|turn\\.usage' internal

# stream-json package 不应导入 Multica
! rg 'multica|server/pkg/agent' internal/streamjson

# stdout 协议类型集中在 internal/streamjson
rg 'StreamOutputMessage|ProtocolName|gocli-stream-json' internal/streamjson internal/app
```

## 5. Mission profile 兼容测试

这些测试只验证 additive 兼容，不要求 MVP 实现完整 mission workflow：

- 输出 schema 支持带 `handoff` 的 result line，普通 Multica parser 可忽略。
- `result.handoff.commands[].exit_code`、`handoff.artifacts[]`、`handoff.validation[].assertion_id` 能正常 JSON round-trip。
- `run_role` 不会被写入 `message.role`，也不会改变 `assistant` / `user` transcript role。
- 缺少 `handoff`、`metadata`、`run_role` 时，现有 stream-json 冒烟脚本仍通过。
- 如果后续从 Goal progress 或 artifact 生成 `handoff`，必须有测试证明来源 artifact 存在，且 adapter 没有读取 Multica 内部类型。

## 6. 验收标准

- 所有 stdout 行可被 `jq` 解析。
- stderr 中可有诊断，但不得含协议 JSON。
- result 行总是最后一条协议消息。
- failed / incomplete_no_finish 时仍输出 result，然后进程以非零 code 退出。
- `--json` 老模式测试继续通过。
