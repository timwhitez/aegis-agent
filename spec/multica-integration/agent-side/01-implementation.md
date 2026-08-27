# aegis-agent 代码修改指引

本文件给出精确实现方案。所有 wire 字段以 `../wire-protocol.md` 为准。

## 1. `internal/runtime/engine.go`

### 1.1 `assistant.message` event 增加 thinking

当前 `assistant.message` event 只写 `text` 和 `status`。需要增加 `thinking`，并且不要改变 session message 的持久化方式。

修改位置：`shouldPersistAssistantResult(result)` 分支内，`e.appendEvent(meta.ID, "assistant.message", ...)`。

目标形态：

```go
if err := e.appendEvent(meta.ID, "assistant.message", "assistant_output", map[string]any{
    "text":     assistantText,
    "thinking": result.Thinking,
    "status":   state.Status,
}); err != nil {
    return RunResult{}, fmt.Errorf("record assistant.message event: %w", err)
}
```

注意：

- `result.Thinking` 是 provider adapter 已归一化的可读 thinking，不是 provider-native replay block。
- 不要把 `provider_content_blocks` 输出到 stream-json；这些是 provider adapter 私有 replay 事实。

### 1.2 `tool.before` event 增加 call_id

当前位置约在工具循环开始处：

```go
if err := e.appendEvent(meta.ID, "tool.before", "tool_execute", map[string]any{
    "tool_name": call.Name,
    "arguments": argumentsText,
}); err != nil {
```

改为：

```go
if err := e.appendEvent(meta.ID, "tool.before", "tool_execute", map[string]any{
    "call_id":   call.ID,
    "tool_name": call.Name,
    "arguments": argumentsText,
}); err != nil {
```

同时建议给 hook payload 增加 `call_id`，这是 additive 字段，不影响旧 hook：

```go
beforePayload := map[string]any{
    "session_id": meta.ID,
    "call_id":    call.ID,
    "tool_name":  call.Name,
    "mode":       meta.Mode,
    "arguments":  argumentsText,
}
```

### 1.3 `tool.after` event 增加 call_id

当前 `eventData`：

```go
eventData := map[string]any{
    "tool_name":      call.Name,
    "display_output": toolResult.DisplayOutput,
    "is_error":       toolResult.IsError,
}
```

改为：

```go
eventData := map[string]any{
    "call_id":        call.ID,
    "tool_name":      call.Name,
    "display_output": toolResult.DisplayOutput,
    "is_error":       toolResult.IsError,
}
```

同时建议给 `tool.after` hook payload 增加 `call_id`：

```go
afterPayload := map[string]any{
    "session_id":     meta.ID,
    "call_id":        call.ID,
    "tool_name":      call.Name,
    "llm_output":     toolResult.LLMOutput,
    "display_output": toolResult.DisplayOutput,
}
```

### 1.4 不新增 `turn.usage`

当前 `providerTurnEventData(result)` 已生成：

```go
data := map[string]any{
    "stop_reason": result.StopReason,
    "usage": map[string]any{
        "input_tokens":  result.Usage.InputTokens,
        "output_tokens": result.Usage.OutputTokens,
        ...
    },
}
```

stream-json adapter 必须消费 `turn.stopped.data.usage` 并累计。新增 `turn.usage` 会造成两个 usage 事实源，禁止。

## 2. 新增 `internal/streamjson/output.go`

类型名使用 `StreamOutputMessage` 前缀，避免与 Multica 侧 `gocliOutputMessage` 或现有 `events.Event` 语义混淆。

```go
package streamjson

import "encoding/json"

const (
    ProtocolName    = "gocli-stream-json"
    ProtocolVersion = 1
)

type StreamOutputMessage struct {
    Type            string                `json:"type"`
    Protocol        string                `json:"protocol,omitempty"`
    ProtocolVersion int                   `json:"protocol_version,omitempty"`
    RunRole         string                `json:"run_role,omitempty"`
    Metadata        map[string]any        `json:"metadata,omitempty"`
    SessionID       string                `json:"session_id,omitempty"`
    Message         *StreamContentMessage `json:"message,omitempty"`
    Result          string                `json:"result,omitempty"`
    Status          string                `json:"status,omitempty"`
    IsError         bool                  `json:"is_error,omitempty"`
    Usage           *StreamUsage          `json:"usage,omitempty"`
    Handoff         *StreamHandoff        `json:"handoff,omitempty"`
    Log             *StreamLogEntry       `json:"log,omitempty"`
}

type StreamContentMessage struct {
    Role    string               `json:"role"`
    Content []StreamContentBlock `json:"content"`
}

type StreamContentBlock struct {
    Type      string         `json:"type"`
    Text      string         `json:"text,omitempty"`
    ID        string         `json:"id,omitempty"`
    Name      string         `json:"name,omitempty"`
    Input     map[string]any `json:"input,omitempty"`
    ToolUseID string         `json:"tool_use_id,omitempty"`
    Content   string         `json:"content,omitempty"`
    IsError   bool           `json:"is_error,omitempty"`
}

type StreamUsage struct {
    InputTokens              int `json:"input_tokens"`
    OutputTokens             int `json:"output_tokens"`
    CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
    CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

type StreamLogEntry struct {
    Level   string `json:"level"`
    Message string `json:"message"`
}

type StreamHandoff struct {
    Summary    string                    `json:"summary,omitempty"`
    Completed  []string                  `json:"completed,omitempty"`
    Remaining  []string                  `json:"remaining,omitempty"`
    Commands   []StreamHandoffCommand    `json:"commands,omitempty"`
    Artifacts  []StreamArtifactRef       `json:"artifacts,omitempty"`
    Risks      []string                  `json:"risks,omitempty"`
    Validation []StreamHandoffValidation `json:"validation,omitempty"`
}

type StreamHandoffCommand struct {
    Command  string             `json:"command,omitempty"`
    ExitCode int                `json:"exit_code,omitempty"`
    Status   string             `json:"status,omitempty"`
    Artifact *StreamArtifactRef `json:"artifact,omitempty"`
}

type StreamHandoffValidation struct {
    AssertionID string             `json:"assertion_id,omitempty"`
    Status      string             `json:"status,omitempty"`
    Evidence    string             `json:"evidence,omitempty"`
    Artifact    *StreamArtifactRef `json:"artifact,omitempty"`
}

type StreamArtifactRef struct {
    Kind        string `json:"kind,omitempty"`
    Path        string `json:"path,omitempty"`
    URI         string `json:"uri,omitempty"`
    Description string `json:"description,omitempty"`
}

func MarshalLine(msg *StreamOutputMessage) ([]byte, error) {
    return json.Marshal(msg)
}
```

`RunRole`、`Metadata` 和 `Handoff` 是 mission-compatible profile 的 additive 字段。MVP 可保持零值；adapter 不应为了填充它们而解析 Multica mission graph 或 `aegis-agent` 未公开的 session internals。

## 3. 新增 `internal/streamjson/adapter.go`

Adapter 只依赖 `internal/events`，不依赖 Multica 类型。

关键行为：

| `events.Event.Type` | 输出 |
| --- | --- |
| `session.started` | `system` message，带 `session_id` |
| `assistant.message` | `assistant` message，按 `thinking`、`text` 输出 blocks |
| `tool.before` | `assistant` `tool_use` block |
| `tool.after` | `user` `tool_result` block |
| `turn.stopped` | 累计 `data.usage`，不输出 transcript |
| `provider.error` / `provider.retry` / `provider.auto_resume` | 可选 `log` |
| `session.created` | 默认忽略，避免和 `session.started` 产生重复 system 行 |
| 其他内部事件 | 忽略 |

核心转换逻辑：

```go
func (a *Adapter) convert(evt events.Event) *StreamOutputMessage {
    switch evt.Type {
    case "session.started":
        return &StreamOutputMessage{
            Type:            "system",
            Protocol:        ProtocolName,
            ProtocolVersion: ProtocolVersion,
            SessionID:       evt.SessionID,
            Message: &StreamContentMessage{
                Role:    "system",
                Content: []StreamContentBlock{{Type: "text", Text: "Session started"}},
            },
        }

    case "assistant.message":
        var blocks []StreamContentBlock
        if thinking, _ := evt.Data["thinking"].(string); strings.TrimSpace(thinking) != "" {
            blocks = append(blocks, StreamContentBlock{Type: "thinking", Text: thinking})
        }
        if text, _ := evt.Data["text"].(string); strings.TrimSpace(text) != "" {
            blocks = append(blocks, StreamContentBlock{Type: "text", Text: text})
        }
        if len(blocks) == 0 {
            return nil
        }
        return &StreamOutputMessage{Type: "assistant", Message: &StreamContentMessage{Role: "assistant", Content: blocks}}

    case "tool.before":
        callID, _ := evt.Data["call_id"].(string)
        name, _ := evt.Data["tool_name"].(string)
        argsStr, _ := evt.Data["arguments"].(string)
        input := decodeObject(argsStr)
        return &StreamOutputMessage{Type: "assistant", Message: &StreamContentMessage{
            Role: "assistant",
            Content: []StreamContentBlock{{
                Type:  "tool_use",
                ID:    callID,
                Name:  name,
                Input: input,
            }},
        }}

    case "tool.after":
        callID, _ := evt.Data["call_id"].(string)
        output, _ := evt.Data["display_output"].(string)
        isErr, _ := evt.Data["is_error"].(bool)
        return &StreamOutputMessage{Type: "user", Message: &StreamContentMessage{
            Role: "user",
            Content: []StreamContentBlock{{
                Type:      "tool_result",
                ToolUseID: callID,
                Content:   output,
                IsError:   isErr,
            }},
        }}

    case "turn.stopped":
        a.accumulateUsage(evt.Data["usage"])
        return nil
    }
    return nil
}
```

实现要求：

- `writeLine` 必须用 mutex 保护 writer。
- `decodeObject` 对无效 JSON 返回空 map 或 nil，不 panic。
- `accumulateUsage` 必须兼容 `int`、`int64`、`float64`、`json.Number`。
- `WriteResult(sessionID, finalText, status, lastError string, exitCode int)` 写最后一行，`is_error = exitCode != 0`。
- `result` 文本优先用 `finalText`；若失败且 `finalText` 为空，可用 `lastError`。

## 4. 新增 `internal/streamjson/input.go`

MVP 只解析一条 user prompt，不实现 control watcher。

```go
func ReadInitialPrompt(r io.Reader, maxBytes int64) (string, error)
```

要求：

- 使用 `io.LimitReader` 或 scanner buffer 限制最大输入，沿用 `maxPromptStdinBytes` 量级。
- 只接受 `{"type":"user","message":{"role":"user",...}}`。
- 拼接所有 text blocks，中间用 `\n`。
- 空文本返回错误。
- 无效 JSON 返回错误。

不要在 `ReadInitialPrompt` 后复用同一个 `bufio.Scanner` 再读 control；scanner 可能预读，容易吞掉后续行。控制消息留到后续协议版本。

## 5. 新增 `internal/streamjson/models.go`

输出结构与 `wire-protocol.md` 的 `models --json` 对齐。

MVP 不做上游 provider 实时模型发现；从当前 config 生成稳定列表：

- 遍历 `cfg.Providers`，按 provider name 排序。
- 对每个 provider，取 `<providerName>/<Provider.Model>` 作为 `id`。这里的 `providerName` 是 aegis-agent config provider key，例如 `openai` / `anthropic`；Multica 会按第一个 `/` 把该 route key 拆回 `--provider <providerName> --model <Provider.Model>`，所以 `Provider.Model` 内部仍可包含 `/`。
- `label` 使用可读形态，例如 `<providerName>: <Provider.Model>`，避免不同 provider 下相同 model 字符串在 UI 中不可区分。
- `provider` 字段来自 `config.EffectiveAPIProvider(name, provider)` 映射：
  - `openai-compatible` -> `openai`
  - `anthropic-compatible` -> `anthropic`
  - `google` -> `google`
- `default=true` 标记 `cfg.DefaultProvider` 对应 provider 的 model。
- 对无 model 的 provider 跳过。
- 如果用户手工在 Multica 输入不含 `/` 的 model ID，Multica backend 只传 `--model`，由 aegis-agent 默认 provider 接收；这只用于 manual entry，不是 `models --json` 的标准输出形态。

thinking catalog：

| effective API provider | supported levels |
| --- | --- |
| `openai-compatible` | `low`, `medium`, `high`, `xhigh`, `max` |
| `anthropic-compatible` | `low`, `medium`, `high`, `xhigh`, `max` |
| `google` | `low`, `medium`, `high`, `xhigh`, `max` |
| 其他 | nil |

`thinking.default_level` 应从当前 provider config 派生：OpenAI-compatible 优先使用 `ReasoningEffort` 中的 `low|medium|high|xhigh|max`；Anthropic/Google 可按 `ThinkingBudget` 映射；没有显式配置时回退到 `medium`。不要在部署默认已经设为 `xhigh` 或 `max` 时仍向 Multica 报 `medium`。

`--thinking-level` 是 gocli runtime-native 抽象值，不是 provider 原生值。映射见第 8 节。

## 6. 新增 `internal/app/version.go`

Multica `DetectVersion` 调用 `<binary> --version`，因此必须支持顶层 flag。

```go
package app

import (
    "fmt"
    "io"
)

var Version = "0.1.0-dev"

func printVersion(w io.Writer) error {
    _, err := fmt.Fprintf(w, "aegis-agent v%s\n", Version)
    return err
}
```

后续 release 可用 `-ldflags "-X aegis-agent/internal/app.Version=0.1.0"` 注入真实版本。

## 7. 新增 `internal/app/models_cmd.go`

命令形态：

```bash
aegis-agent models --json --config /path/to/config.yaml
```

实现要点：

- `flag.NewFlagSet("models", flag.ContinueOnError)`
- 支持 `--json` 和 `--config`
- `cwd, _ := os.Getwd()` 后调用现有 `loadConfig(configPath, cwd)`
- JSON 模式输出 indent 或 compact 均可；Multica 只要求合法 JSON
- 非 JSON 模式输出人类可读列表即可

## 8. `--thinking-level` 到 ProviderOptions 的映射

新增 helper，放在 `internal/app/app.go` 或小文件中：

```go
func providerOptionsForThinkingLevel(level string, cfg *config.Config, providerName string) (session.ProviderOptions, error)
```

行为：

- 空 level 返回零值 `session.ProviderOptions{}`。
- 只接受 `low|medium|high|xhigh|max`；其他值返回错误。
- 如果 providerName 为空，使用 `cfg.DefaultProvider`。
- 读取 provider config 并计算 `EffectiveAPIProvider`。

映射：

| effective API provider | level -> options |
| --- | --- |
| `openai-compatible` | `ReasoningEffort = level` |
| `anthropic-compatible` | `IncludeThoughts=true`; `ThinkingBudget`: low=1024, medium=4096, high=8192, xhigh=16384, max=32000 |
| `google` | `IncludeThoughts=true`; `ThinkingBudget`: low=1024, medium=4096, high=8192, xhigh=16384, max=32000 |

这些值必须通过 `runtime.StartRequest.ProviderOptions` 写入 session metadata，而不是只作为一次性 CLI flag 停留在 app 层。

Resume 行为锁定：本 SPEC 要求给 `runtime.ContinueRequest` 增加 `ProviderOptions session.ProviderOptions`，并在 Continue 时 merge 到 `meta.ProviderOptions`。Multica 侧始终可以在 resume 时传 `--thinking-level`；不要实现“resume 忽略 thinking-level”的分叉版本。

`Runner.Continue` 的具体行为应为：

1. `ContinueRequest` 增加 `ProviderOptions session.ProviderOptions`。
2. provider override 分支继续用新 provider config 重新计算完整 `meta.ProviderOptions`。
3. provider 未切换、但 `req.ProviderOptions` 非零时，把 `req.ProviderOptions` 作为 additive override merge 到当前 durable `meta.ProviderOptions`；只覆盖非零/非 nil 字段，不清空已有 timeout/retry/store/prompt-cache 等持久字段。
4. 随后继续调用现有 `mergedSessionProviderOptions(meta.Provider, meta.ProviderOptions)`，保留当前代码对 legacy / partial provider options 的 backfill 行为。
5. 新增测试证明 `--resume --thinking-level max` 会更新 durable `meta.ProviderOptions` 并影响后续 adapter config。

## 9. 修改 `internal/app/app.go`

### 9.1 顶层 dispatch

`Run()` 开头先处理 version：

```go
if len(args) > 0 && (args[0] == "--version" || args[0] == "version") {
    return printVersion(stdout)
}
```

在 switch 中添加：

```go
case "models":
    err = modelsCommand(ctx, args[1:], stdout, stderr)
```

`usage()` 中加入 `models`。

### 9.2 `runCommand()` flags

`normalizeInterspersedFlags` 的 value flags 增加：

```go
"output-format", "input-format", "resume", "thinking-level"
```

flag 变量增加：

```go
outputFormat  = fs.String("output-format", "text", "")
inputFormat   = fs.String("input-format", "text", "")
resumeSession = fs.String("resume", "", "")
thinkingLevel = fs.String("thinking-level", "", "")
```

Parse 后校验：

```go
if *outputFormat != "text" && *outputFormat != "stream-json" {
    return fmt.Errorf("unsupported --output-format %q", *outputFormat)
}
if *inputFormat != "text" && *inputFormat != "stream-json" {
    return fmt.Errorf("unsupported --input-format %q", *inputFormat)
}
if *outputFormat == "stream-json" && *jsonMode {
    return fmt.Errorf("--output-format stream-json and --json are mutually exclusive")
}
if *resumeSession != "" && mode != "exec" {
    return fmt.Errorf("--resume is only supported on exec")
}
```

### 9.3 Prompt 读取

```go
var prompt string
if *inputFormat == "stream-json" {
    prompt, err = streamjson.ReadInitialPrompt(os.Stdin, maxPromptStdinBytes)
} else {
    prompt, err = resolvePrompt(fs.Args(), os.Stdin)
}
if err != nil {
    return err
}
```

当 `input-format=stream-json` 时，建议拒绝非空 `fs.Args()`，避免 custom positional arg 被误认为 prompt：

```go
if *inputFormat == "stream-json" && len(fs.Args()) > 0 {
    return fmt.Errorf("positional prompt arguments are not allowed with --input-format stream-json")
}
```

### 9.4 Stream renderer

文本/旧 JSON 模式继续使用 `output.Renderer`。stream-json 模式使用 `streamjson.Adapter`。

关键点：不要在 `runner.Start/Continue` 返回后立即取消 renderer，可能丢掉 buffered events。stream-json 模式应等到 terminal event 或短暂 drain 完成后再写 result。

推荐 helper：

```go
type eventRenderer interface {
    Handle(events.Event)
}

func renderEventsUntilDone(ctx context.Context, sub <-chan events.Event, render func(events.Event), terminal func(events.Event) bool) <-chan struct{}
```

terminal event 包括：

- `session.completed`
- `session.failed`
- `session.paused`
- `session.awaiting_input`

如果 run 返回了 error 且没有 terminal event，仍必须写 result envelope，`is_error=true`。

### 9.5 Start / Continue 分支

根据 `--resume` 决定调用：

```go
providerOptions, err := providerOptionsForThinkingLevel(*thinkingLevel, cfg, *providerName)
if err != nil {
    return err
}

if *resumeSession != "" {
    result, err = runner.Continue(runCtx, runtime.ContinueRequest{
        SessionID:        *resumeSession,
        Message:          prompt,
        Provider:         *providerName,
        Model:            *model,
        SystemOverride:   *system,
        PlanInputHandler: cliPlanInputHandler(os.Stdin, stderr),
        ProviderOptions:  providerOptions,
    })
} else {
    result, err = runner.Start(runCtx, runtime.StartRequest{
        Prompt:           prompt,
        Provider:         *providerName,
        Model:            *model,
        ProviderOptions:  providerOptions,
        Workdir:          *workdir,
        Mode:             actualMode,
        SystemOverride:   *system,
        Goal:             goalDraft,
        PlanMode:         planDraft,
        PlanInputHandler: cliPlanInputHandler(os.Stdin, stderr),
        IsolationMode:    *isolationMode,
        IsolationRoot:    *isolationRoot,
    })
}
```

注意：当前 `loadRunner(*configPath, invokeCWD)` 返回 cfg 被丢弃。为 `providerOptionsForThinkingLevel`，需要保留 cfg：

```go
runner, cfg, err := runnerLoader(*configPath, invokeCWD)
```

### 9.6 Result 输出

stream-json 模式最后写：

```go
exitCode := mapStatusToExitCode(result.Status, result.LastError)
sjAdapter.WriteResult(result.SessionID, result.FinalText, result.Status, result.LastError, exitCode)
if exitCode != 0 {
    return ExitError{Code: exitCode}
}
return nil
```

这样 Multica 可同时看到 result envelope 和正确进程 exit code。

## 10. 测试必须覆盖的定义冲突

- `--json` 仍输出内部 `events.Event`，`--output-format stream-json` 输出 transcript envelope；两者互斥。
- `session.created` 不输出 stream-json system 行，避免重复 session started。
- usage 只来自 `turn.stopped.data.usage`。
- `tool_use.id` 和 `tool_result.tool_use_id` 必须是 `call.ID`，不是 tool name、event id 或 provider call id。
- stream-json stdout 不混入 human text renderer 输出。
- `models --json` 不使用 Multica types，不导入 Multica repo。

## 11. Mission profile guardrails

后续若在 aegis-agent 侧补充 mission-compatible profile，只允许增加 runtime primitive 和事实记录，不允许把 Multica mission workflow 搬进 core runtime。

允许：

- 在 `StreamOutputMessage` 上输出 `run_role`、`metadata`、`handoff` 等 additive 字段。
- 通过 Goal / Plan Mode / `record_goal_progress` 保存 validation evidence、handoff、command、artifact 和 blocker。
- 用 child `agent_role` 或 session metadata 持久化 `planner` / `generator` / `evaluator` hint。
- 在 `session.md`、long-run checkpoint 或 visible artifact 中暴露 progress / validation / handoff 摘要。

禁止：

- `exec` 因 `run_role=orchestrator` 自动派生 worker 或 validator。
- stream-json adapter 解析 Multica mission store、coverage graph 或前端状态。
- runtime 根据 role hint 强制或阻止 `agent_spawn`；是否委派仍由模型在 tool description、skill 和当前上下文中判断。
- validator 直接信任 worker 自评；validator prompt 应基于 validation contract、artifact、workdir 和可执行检查。
