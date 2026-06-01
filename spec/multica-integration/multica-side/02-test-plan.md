# Multica 侧测试计划

## 1. `server/pkg/agent/gocli_test.go`

### 1.1 Parser tests

覆盖 NDJSON 解析：

- `system` 设置 `sessionID` 并发送 `MessageStatus`
- `assistant/text` 累积 output 并发送 `MessageText`
- `assistant/thinking` 发送 `MessageThinking`
- `assistant/tool_use` 发送 `MessageToolUse`
- `user/tool_result` 发送 `MessageToolResult`
- `log` 发送 `MessageLog`
- `result` 覆盖 final output、status、usage、session id
- `result` 可带 `run_role`、`metadata`、`handoff`，MVP parser 不失败；如果 mission store 未实现，则忽略
- invalid JSON line 被跳过，不影响后续有效 result
- scanner error 时 status=`failed`

建议样例：

```go
ndjson := strings.Join([]string{
    `{"type":"system","session_id":"ses_abc","message":{"role":"system","content":[{"type":"text","text":"Session started"}]}}`,
    `{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","text":"think"},{"type":"text","text":"Hello"}]}}`,
    `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"read_file","input":{"path":"x.go"}}]}}`,
    `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"package x","is_error":false}]}}`,
    `{"type":"result","session_id":"ses_abc","status":"completed","result":"All done","usage":{"input_tokens":200,"output_tokens":80,"cache_creation_input_tokens":10,"cache_read_input_tokens":20}}`,
}, "\n") + "\n"
```

Assertions：

- `result.sessionID == "ses_abc"`
- `result.output == "All done"`
- `result.status == "completed"`
- cache usage 映射到 `CacheWriteTokens` / `CacheReadTokens`
- message channel 至少包含 status/text/thinking/tool-use/tool-result
- 增加一条含 `metadata.validation_contract_ref` 与 `handoff.validation` 的 result fixture，断言现有 Result 输出不受影响

### 1.2 Args tests

`TestGocliBuildArgs` 覆盖：

- 必含 `exec --output-format stream-json --input-format stream-json`
- `Model: "anthropic/claude-sonnet-4-6"` -> `--provider anthropic --model claude-sonnet-4-6`
- `Model: "manual-model"` -> `--model manual-model`
- `Timeout` -> seconds
- `ResumeSessionID` -> `--resume`
- `SystemPrompt` -> `--system`
- `ThinkingLevel` -> `--thinking-level`
- `Cwd` -> `--workdir`
- `ExtraArgs` 在 `CustomArgs` 之前
- blocked args 被过滤
- mission `run_role` 不通过 custom args 伪装成 `--provider` 或 `--model`；role-specific model 选择应由 Multica 上层先解析为 `opts.Model` / `opts.ThinkingLevel`

### 1.3 stdin tests

`writeGocliInput()` 输出必须等价于：

```json
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"..."}]}}
```

### 1.4 Execute mock tests

使用临时 fake executable：

```bash
#!/usr/bin/env bash
if [[ "$1" == "--version" ]]; then
  echo "go-cli-agent v0.1.0"
  exit 0
fi
if [[ "$1" == "models" ]]; then
  echo '[{"id":"openai/fake-model","label":"openai: fake-model","provider":"openai","default":true,"thinking":{"supported_levels":[{"value":"medium","label":"Medium"}],"default_level":"medium"}}]'
  exit 0
fi
read -r input
echo '{"type":"system","session_id":"ses_mock","message":{"role":"system","content":[{"type":"text","text":"Session started"}]}}'
echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Done"}]}}'
echo '{"type":"result","session_id":"ses_mock","status":"completed","result":"Done","is_error":false,"usage":{"input_tokens":1,"output_tokens":1}}'
```

Test:

- `New("gocli", Config{ExecutablePath: fake})`
- `Execute(...).Result` returns completed
- SessionID and usage populated
- stdout messages close before Result closes
- non-zero fake exit with stderr populates `Result.Error` with one `gocli stderr:` tail, not a duplicated tail
- fake timeout returns status `timeout`
- fake cancellation returns status `aborted`
- fake result 带 `handoff` 时，未启用 mission store 的 backend 仍返回 completed

## 2. Registration tests

Modify existing tests:

- `TestNewReturnsGocliBackend`
- `TestLaunchHeaderCoversAllSupportedBackends`
- `TestListModels...` includes `gocli` with fake executable
- `TestListModels...` with a failing fake executable returns an empty model list rather than failing the model-list request
- `TestCheckMinVersion` includes `gocli`
- `TestIsKnownThinkingValue` includes `gocli low|medium|high|xhigh`
- `ValidateThinkingLevel` with fake `go-cli-agent models --json`

## 3. daemon config tests

`server/internal/daemon/config_test.go`：

- PATH contains fake `go-cli-agent` -> `cfg.Agents["gocli"]` exists.
- `MULTICA_GOCLI_PATH=/tmp/fake` overrides default.
- `MULTICA_GOCLI_MODEL=openai/model-x` sets `AgentEntry.Model`.
- `MULTICA_GOCLI_ARGS='--isolation none'` sets `cfg.GocliArgs`.
- no-agent error string includes `go-cli-agent`.
- `defaultAgentCommandNames` shell fallback includes `go-cli-agent`.
- config-path alignment is documented: `GO_CLI_AGENT_CONFIG` is the supported way to make both `models --json` and `exec` use the same non-default go-cli-agent config; `MULTICA_GOCLI_ARGS --config ...` is execution-only.

`server/internal/daemon/daemon_test.go`：

- `defaultArgsForProvider(cfg, "gocli")` returns copy of `cfg.GocliArgs`.

## 4. execenv tests

Update known provider cases:

- `runtimeConfigPath(workDir, "gocli") == filepath.Join(workDir, "AGENTS.md")`
- `CleanupRuntimeConfig` removes `AGENTS.md` marker block for gocli.
- `resolveSkillsDir(workDir, "gocli", ...) == filepath.Join(workDir, "skills")`
- `listRuntimeLocalSkills("gocli")` reads private go-cli-agent local skills from `~/.go-cli-agent/skills`, not `~/.codex/skills`; those skills become shared only after Multica copies/imports them into workspace skills.
- sidecar manifest includes gocli in all file-based provider matrix.
- generated runtime brief lists skills as auto-discovered for gocli.

## 5. Mock E2E

From Multica repo root:

```bash
FAKE="$(mktemp -d)/go-cli-agent"
cat > "$FAKE" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  --version)
    echo "go-cli-agent v0.1.0"
    exit 0
    ;;
  models)
    echo '[{"id":"openai/fake-model","label":"openai: fake-model","provider":"openai","default":true,"thinking":{"supported_levels":[{"value":"low","label":"Low"},{"value":"medium","label":"Medium"},{"value":"high","label":"High"},{"value":"xhigh","label":"XHigh"}],"default_level":"medium"}}]'
    exit 0
    ;;
  exec)
    read -r _
    echo '{"type":"system","session_id":"ses_mock123","message":{"role":"system","content":[{"type":"text","text":"Session started"}]}}'
    echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","text":"checking"},{"type":"text","text":"I will help."}]}}'
    echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"call_m1","name":"shell","input":{"command":"echo hello"}}]}}'
    echo '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_m1","content":"hello\n","is_error":false}]}}'
    echo '{"type":"result","session_id":"ses_mock123","status":"completed","result":"Task completed successfully","is_error":false,"usage":{"input_tokens":500,"output_tokens":200,"cache_creation_input_tokens":50,"cache_read_input_tokens":100}}'
    exit 0
    ;;
esac
echo "unexpected args: $*" >&2
exit 2
SH
chmod +x "$FAKE"

MULTICA_GOCLI_PATH="$FAKE" go test ./server/pkg/agent -run 'TestGocli|TestNewReturnsGocli|TestLaunchHeader|TestListModels|TestCheckMinVersion|TestIsKnownThinking' -count=1 -v
```

## 6. 真实 E2E

前置：

- go-cli-agent 已完成 agent-side spec。
- `go-cli-agent` 在 PATH 中，或设置 `MULTICA_GOCLI_PATH`。

验证：

```bash
go-cli-agent --version
go-cli-agent models --json | jq '.[0].id'
go test ./server/pkg/agent -run TestGocli -count=1 -v
go test ./server/internal/daemon -run 'TestDefaultArgsForProvider|TestLoadConfig' -count=1 -v
go test ./server/internal/daemon/execenv -run 'Gocli|RuntimeConfig|Sidecar|Skills' -count=1 -v
```

手工 daemon smoke：

```bash
MULTICA_GOCLI_PATH="$(command -v go-cli-agent)" \
MULTICA_GOCLI_MODEL="<provider>/<model-id>" \
multica daemon
```

期望：

- runtime registration 中出现 provider/type `gocli`。
- model list endpoint 返回 `supported=true` 和 `thinking.supported_levels`。
- 创建绑定 `gocli` runtime 的 agent 后，任务能完成并保存 SessionID。
- 失败时 Result.Error 包含 `gocli stderr:` tail（若 stderr 非空）。

## 7. 回归范围

至少运行：

```bash
go test ./server/pkg/agent ./server/internal/daemon ./server/internal/daemon/execenv
```

如果 handler thinking validation 被修改：

```bash
go test ./server/internal/handler -run 'AgentThinking|CreateAgent|UpdateAgent|RuntimeModels'
```

## 8. 搜索检查

开发后执行：

```bash
rg -n '"gocli"|go-cli-agent|MULTICA_GOCLI' server/pkg/agent server/internal/daemon
rg -n 'newStderrTail\\(|withAgentStderr\\(' server/pkg/agent/gocli.go
```

确认：

- `newStderrTail` 调用有两个参数。
- `withAgentStderr` 调用有三个参数。
- provider lists 中没有漏掉 `gocli`。

## 9. Mission profile 兼容测试

这些测试属于 optional profile 的兼容层，不要求默认 UI 或 MVP backend 实现 Missions 编排：

- parser 对未知顶层字段、未知 content block、`run_role`、`metadata`、`handoff` 都保持 tolerant。
- `run_role` 不覆盖 transcript `message.role`，也不改变 `MessageText` / `MessageThinking` / `MessageToolUse` 的发送逻辑。
- 如果 Multica 后续增加 mission store，保存 `handoff` 的测试应断言它来自 `result` envelope 或 artifact ref，而不是读取 go-cli-agent 私有 session 目录。
- Mission Control 聚合测试应使用 Multica mission store + stream-json fixture + Result.SessionID，不依赖浏览器端状态作为唯一事实。
- role-specific model policy 测试应覆盖：`validator` 可选择不同 `opts.Model`，但 `gocliBackend.buildArgs` 仍只负责 `<provider>/<model>` 拆分。
