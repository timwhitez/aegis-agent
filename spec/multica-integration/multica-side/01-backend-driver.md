# Multica 侧代码修改指引

本文件基于 Multica upstream HEAD `41a1ca58add47f53bb64ddc6aa02be2d9a73faa9` 编写。

## 1. 新增 `server/pkg/agent/gocli.go`

### 1.1 package 与 imports

```go
package agent

import (
    "bufio"
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "os/exec"
    "strings"
    "time"
)
```

不要导入 go-cli-agent 的 Go package。两端只通过 JSON 协议耦合。

### 1.2 Wire types

类型必须 unexported，并使用 `gocli` 前缀，避免与 `claudeSDKMessage`、`opencodeEvent` 冲突。

```go
type gocliOutputMessage struct {
    Type            string               `json:"type"`
    Protocol        string               `json:"protocol,omitempty"`
    ProtocolVersion int                  `json:"protocol_version,omitempty"`
    RunRole         string               `json:"run_role,omitempty"`
    Metadata        map[string]any       `json:"metadata,omitempty"`
    Message         *gocliContentMessage `json:"message,omitempty"`
    SessionID       string               `json:"session_id,omitempty"`
    Result          string               `json:"result,omitempty"`
    Status          string               `json:"status,omitempty"`
    IsError         bool                 `json:"is_error,omitempty"`
    Usage           *gocliUsage          `json:"usage,omitempty"`
    Handoff         *gocliHandoff        `json:"handoff,omitempty"`
    Log             *gocliLogEntry       `json:"log,omitempty"`
}

type gocliContentMessage struct {
    Role    string              `json:"role"`
    Content []gocliContentBlock `json:"content"`
}

type gocliContentBlock struct {
    Type      string         `json:"type"`
    Text      string         `json:"text,omitempty"`
    ID        string         `json:"id,omitempty"`
    Name      string         `json:"name,omitempty"`
    Input     map[string]any `json:"input,omitempty"`
    ToolUseID string         `json:"tool_use_id,omitempty"`
    Content   string         `json:"content,omitempty"`
    IsError   bool           `json:"is_error,omitempty"`
}

type gocliUsage struct {
    InputTokens              int64 `json:"input_tokens"`
    OutputTokens             int64 `json:"output_tokens"`
    CacheCreationInputTokens int64 `json:"cache_creation_input_tokens,omitempty"`
    CacheReadInputTokens     int64 `json:"cache_read_input_tokens,omitempty"`
}

type gocliLogEntry struct {
    Level   string `json:"level"`
    Message string `json:"message"`
}

type gocliHandoff struct {
    Summary    string                    `json:"summary,omitempty"`
    Completed  []string                  `json:"completed,omitempty"`
    Remaining  []string                  `json:"remaining,omitempty"`
    Commands   []gocliHandoffCommand    `json:"commands,omitempty"`
    Artifacts  []gocliArtifactRef       `json:"artifacts,omitempty"`
    Risks      []string                  `json:"risks,omitempty"`
    Validation []gocliHandoffValidation `json:"validation,omitempty"`
}

type gocliHandoffCommand struct {
    Command  string            `json:"command,omitempty"`
    ExitCode int               `json:"exit_code,omitempty"`
    Status   string            `json:"status,omitempty"`
    Artifact *gocliArtifactRef `json:"artifact,omitempty"`
}

type gocliHandoffValidation struct {
    AssertionID string            `json:"assertion_id,omitempty"`
    Status      string            `json:"status,omitempty"`
    Evidence    string            `json:"evidence,omitempty"`
    Artifact    *gocliArtifactRef `json:"artifact,omitempty"`
}

type gocliArtifactRef struct {
    Kind        string `json:"kind,omitempty"`
    Path        string `json:"path,omitempty"`
    URI         string `json:"uri,omitempty"`
    Description string `json:"description,omitempty"`
}

type gocliInputMessage struct {
    Type     string             `json:"type"`
    RunRole  string             `json:"run_role,omitempty"`
    Metadata map[string]any     `json:"metadata,omitempty"`
    Message  *gocliInputContent `json:"message,omitempty"`
}

type gocliInputContent struct {
    Role    string            `json:"role"`
    Content []gocliInputBlock `json:"content"`
}

type gocliInputBlock struct {
    Type string `json:"type"`
    Text string `json:"text"`
}
```

### 1.3 Blocked args

`go-cli-agent` 使用标准库 `flag`，重复 flag 可能被后面的值覆盖。Multica 的 `CustomArgs` 追加在固定 args 后面，因此要阻止用户覆盖协议和 ExecOptions 管理的 flags。

```go
var gocliBlockedArgs = map[string]blockedArgMode{
    "--output-format":  blockedWithValue,
    "--input-format":   blockedWithValue,
    "--json":           blockedStandalone,
    "--resume":         blockedWithValue,
    "--workdir":        blockedWithValue,
    "--timeout":        blockedWithValue,
    "--model":          blockedWithValue,
    "--provider":       blockedWithValue,
    "--system":         blockedWithValue,
    "--thinking-level": blockedWithValue,
}
```

如果团队希望允许高级用户覆盖 `--config` 或 `--isolation`，不要加入 blocked list；这些不是 Multica protocol-critical 字段。`--provider` 必须阻断，因为 `gocli` backend 会从 `<provider>/<model>` route key 生成 `--provider`，允许 custom args 覆盖会让 Multica model selection 与实际 provider 分叉。

### 1.4 Backend skeleton

```go
type gocliBackend struct {
    cfg Config
}

func (b *gocliBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
    execPath := b.cfg.ExecutablePath
    if execPath == "" {
        execPath = "go-cli-agent"
    }
    resolved, err := exec.LookPath(execPath)
    if err != nil {
        return nil, fmt.Errorf("go-cli-agent executable not found at %q: %w", execPath, err)
    }
    execPath = resolved

    runCtx, cancel := gocliRunContext(ctx, opts.Timeout)

    args := b.buildArgs(opts)
    cmd := exec.CommandContext(runCtx, execPath, args...)
    hideAgentWindow(cmd)
    cmd.WaitDelay = 10 * time.Second
    if opts.Cwd != "" {
        cmd.Dir = opts.Cwd
    }
    env := buildEnv(b.cfg.Env)
    if opts.Cwd != "" {
        env = append(env, "PWD="+opts.Cwd)
    }
    cmd.Env = env

    stdout, err := cmd.StdoutPipe()
    if err != nil {
        cancel()
        return nil, fmt.Errorf("gocli stdout pipe: %w", err)
    }
    stdin, err := cmd.StdinPipe()
    if err != nil {
        cancel()
        return nil, fmt.Errorf("gocli stdin pipe: %w", err)
    }
    closeStdin := func() {
        if stdin != nil {
            _ = stdin.Close()
            stdin = nil
        }
    }
    stderrBuf := newStderrTail(newLogWriter(b.cfg.Logger, "[gocli:stderr] "), agentStderrTailBytes)
    cmd.Stderr = stderrBuf

    b.cfg.Logger.Info("agent command", "exec", execPath, "args", args)
    if err := cmd.Start(); err != nil {
        closeStdin()
        cancel()
        return nil, fmt.Errorf("start gocli: %w", err)
    }
    if err := writeGocliInput(stdin, prompt); err != nil {
        closeStdin()
        cancel()
        _ = cmd.Wait()
        return nil, fmt.Errorf("%s", withAgentStderr(fmt.Sprintf("write gocli input: %v", err), "gocli", stderrBuf.Tail()))
    }
    closeStdin()

    msgCh := make(chan Message, 256)
    resCh := make(chan Result, 1)

    go func() {
        <-runCtx.Done()
        _ = stdout.Close()
    }()

    go func() {
        defer cancel()
        defer close(msgCh)
        defer close(resCh)

        startTime := time.Now()
        scanResult := b.processOutput(stdout, msgCh)
        exitErr := cmd.Wait()
        duration := time.Since(startTime)

        if runCtx.Err() == context.DeadlineExceeded {
            scanResult.status = "timeout"
            scanResult.errMsg = fmt.Sprintf("go-cli-agent timed out after %s", timeout)
        } else if runCtx.Err() == context.Canceled {
            scanResult.status = "aborted"
            scanResult.errMsg = "execution cancelled"
        } else if exitErr != nil && scanResult.status == "" {
            code := exitCodeFromError(exitErr)
            scanResult.status = gocliStatusFromExitCode(code)
            scanResult.errMsg = fmt.Sprintf("go-cli-agent exited with code %d", code)
        }
        if scanResult.status == "" {
            scanResult.status = "completed"
        }
        if scanResult.errMsg != "" {
            scanResult.errMsg = withAgentStderr(scanResult.errMsg, "gocli", stderrBuf.Tail())
        }

        usage := gocliUsageMap(scanResult.usage, opts.Model)
        reportedSessionID := resolveSessionID(opts.ResumeSessionID, scanResult.sessionID, scanResult.status == "failed")

        resCh <- Result{
            Status:     scanResult.status,
            Output:     scanResult.output,
            Error:      scanResult.errMsg,
            DurationMs: duration.Milliseconds(),
            SessionID:  reportedSessionID,
            Usage:      usage,
        }
    }()

    return &Session{Messages: msgCh, Result: resCh}, nil
}

func gocliRunContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
    if timeout <= 0 {
        return context.WithCancel(ctx)
    }
    return context.WithTimeout(ctx, timeout)
}
```

`opts.Timeout <= 0` 表示 long-horizon profile 不安装固定墙钟 deadline；此时不向
`go-cli-agent exec` 传 `--timeout`，生命周期由上游取消、daemon 停止和显式 idle
watchdog 控制。正数 timeout 才安装 backend context deadline，并传递给
go-cli-agent 自身 run timeout。

### 1.5 Args builder

```go
func (b *gocliBackend) buildArgs(opts ExecOptions) []string {
    args := []string{
        "exec",
        "--output-format", "stream-json",
        "--input-format", "stream-json",
    }
    args = appendGocliModelArgs(args, opts.Model)
    if opts.Timeout > 0 {
        args = append(args, "--timeout", fmt.Sprintf("%d", int(opts.Timeout.Seconds())))
    }
    if opts.ResumeSessionID != "" {
        args = append(args, "--resume", opts.ResumeSessionID)
    }
    if opts.SystemPrompt != "" {
        args = append(args, "--system", opts.SystemPrompt)
    }
    if opts.ThinkingLevel != "" {
        args = append(args, "--thinking-level", opts.ThinkingLevel)
    }
    if opts.Cwd != "" {
        args = append(args, "--workdir", opts.Cwd)
    }
    args = append(args, filterCustomArgs(opts.ExtraArgs, gocliBlockedArgs, b.cfg.Logger)...)
    args = append(args, filterCustomArgs(opts.CustomArgs, gocliBlockedArgs, b.cfg.Logger)...)
    return args
}
```

### 1.6 stdin writer

```go
func writeGocliInput(w io.Writer, prompt string) error {
    payload := gocliInputMessage{
        Type: "user",
        Message: &gocliInputContent{
            Role:    "user",
            Content: []gocliInputBlock{{Type: "text", Text: prompt}},
        },
    }
    return json.NewEncoder(w).Encode(payload)
}
```

### 1.7 stdout parser

```go
type gocliScanResult struct {
    status    string
    errMsg    string
    output    string
    sessionID string
    usage     TokenUsage
}

func (b *gocliBackend) processOutput(r io.Reader, ch chan<- Message) gocliScanResult {
    var output strings.Builder
    var result gocliScanResult

    scanner := bufio.NewScanner(r)
    scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

    for scanner.Scan() {
        line := strings.TrimSpace(scanner.Text())
        if line == "" {
            continue
        }
        var msg gocliOutputMessage
        if err := json.Unmarshal([]byte(line), &msg); err != nil {
            b.cfg.Logger.Warn("gocli: invalid JSON line", "error", err)
            continue
        }
        switch msg.Type {
        case "system":
            if msg.SessionID != "" {
                result.sessionID = msg.SessionID
                trySend(ch, Message{Type: MessageStatus, Status: "running", SessionID: msg.SessionID})
            }
        case "assistant":
            handleGocliAssistant(msg, ch, &output)
        case "user":
            handleGocliUser(msg, ch)
        case "result":
            if msg.SessionID != "" {
                result.sessionID = msg.SessionID
            }
            if msg.Result != "" {
                output.Reset()
                output.WriteString(msg.Result)
            }
            if msg.Status != "" {
                result.status = normalizeGocliResultStatus(msg.Status)
            }
            if msg.IsError && result.status == "" {
                result.status = "failed"
            }
            if msg.IsError {
                result.errMsg = msg.Result
            }
            if msg.Usage != nil {
                result.usage = TokenUsage{
                    InputTokens:      msg.Usage.InputTokens,
                    OutputTokens:     msg.Usage.OutputTokens,
                    CacheReadTokens:  msg.Usage.CacheReadInputTokens,
                    CacheWriteTokens: msg.Usage.CacheCreationInputTokens,
                }
            }
        case "log":
            if msg.Log != nil {
                trySend(ch, Message{Type: MessageLog, Level: msg.Log.Level, Content: msg.Log.Message})
            }
        }
    }
    if err := scanner.Err(); err != nil {
        b.cfg.Logger.Warn("gocli: stdout scanner error", "error", err)
        if result.status == "" {
            result.status = "failed"
            result.errMsg = fmt.Sprintf("stdout read error: %v", err)
        }
    }
    result.output = output.String()
    return result
}
```

Helper behavior:

- `handleGocliAssistant`:
  - `text` -> append to output and send `MessageText`
  - `thinking` -> send `MessageThinking`
  - `tool_use` -> send `MessageToolUse{Tool, CallID, Input}`
- `handleGocliUser`:
  - `tool_result` -> send `MessageToolResult{CallID, Output}`
- `normalizeGocliResultStatus`:
  - `completed`, `failed`, `cancelled` pass through
  - `paused` -> `cancelled`
  - `awaiting_input` -> `completed`
  - unknown -> `failed`
- `gocliStatusFromExitCode`:
  - `130` -> `cancelled`
  - otherwise non-zero -> `failed`
- `gocliUsageMap` returns nil only when all token counters are zero; if `opts.Model` is empty, use `"unknown"` as the usage map key, matching the Codex backend fallback.
- stderr tail is appended once, after `cmd.Wait()` has returned. Do not call `withAgentStderr` in an earlier branch and then call it again in the shared final wrapper.

Concrete helper implementations:

```go
func normalizeGocliResultStatus(status string) string {
    switch status {
    case "completed", "failed", "cancelled":
        return status
    case "paused":
        return "cancelled"
    case "awaiting_input":
        return "completed"
    default:
        return "failed"
    }
}

func gocliStatusFromExitCode(code int) string {
    if code == 130 {
        return "cancelled"
    }
    return "failed"
}

func exitCodeFromError(err error) int {
    var exitErr *exec.ExitError
    if errors.As(err, &exitErr) {
        return exitErr.ExitCode()
    }
    return 1
}

func gocliUsageMap(u TokenUsage, model string) map[string]TokenUsage {
    if u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadTokens == 0 && u.CacheWriteTokens == 0 {
        return nil
    }
    if model == "" {
        model = "unknown"
    }
    return map[string]TokenUsage{model: u}
}

// Model IDs returned by go-cli-agent models --json use
// <provider>/<model>. Split only the first slash so provider-native
// model IDs may still contain slashes.
func appendGocliModelArgs(args []string, model string) []string {
    providerName, providerModel, ok := strings.Cut(model, "/")
    if ok && providerName != "" && providerModel != "" {
        return append(args, "--provider", providerName, "--model", providerModel)
    }
    if model != "" {
        return append(args, "--model", model)
    }
    return args
}
```

Session id reporting reuses the existing package helper `resolveSessionID(requestedResume, emitted, failed)`.

### 1.8 Mission profile fields

`run_role`、`metadata` 和 `handoff` 是可选字段。MVP parser 行为：

- `run_role` 不改变 `Message` transcript role，也不改变 args builder。
- `metadata` 不参与 `Result.SessionID`、usage 或 status 计算。
- `handoff` 不影响 `Result.Output`。如果 Multica 当前没有 mission store 或 Result 扩展字段，应忽略它。
- unknown content blocks 继续忽略；不要因为收到 `handoff` 或未来字段而失败。

后续启用 mission-compatible profile 时，Multica 可以在 `processOutput` 的 `result` 分支把 `msg.Handoff` 与 `msg.Metadata` 保存到 mission store 或 run artifact。保存逻辑必须在 Multica domain 层完成，不应让 `gocliBackend` 解析 go-cli-agent session 目录。

Mission Control 展示建议来自：

- `system` / `result` 的 `session_id`
- `run_role` 与 `metadata.mission_id` / `feature_id` / `milestone_id`
- assistant/tool/log stream
- `usage`
- `handoff.summary`、`handoff.commands`、`handoff.artifacts`、`handoff.validation`

## 2. 修改 `server/pkg/agent/agent.go`

### 2.1 注释

将 package 注释和 `Config.ExecutablePath` 注释中的 provider list 加入 `go-cli-agent` / `gocli`。

### 2.2 Factory

```go
case "gocli":
    return &gocliBackend{cfg: cfg}, nil
```

`default` 错误提示的 supported list 加入 `gocli`。

### 2.3 Launch header

```go
"gocli": "go-cli-agent exec (stream-json)",
```

### 2.4 Tests

`TestLaunchHeaderCoversAllSupportedBackends` 的 `supported` 列表加入 `gocli`。新增：

```go
func TestNewReturnsGocliBackend(t *testing.T) { ... }
```

## 3. 修改 `server/pkg/agent/models.go`

`ListModels()` switch 增加：

```go
case "gocli":
    key := "gocli"
    if executablePath != "" {
        key += ":" + executablePath
    }
    return cachedDiscovery(key, func() ([]Model, error) {
        return discoverGocliModels(ctx, executablePath)
    })
```

新增：

```go
func discoverGocliModels(ctx context.Context, executablePath string) ([]Model, error) {
    if executablePath == "" {
        executablePath = "go-cli-agent"
    }
    if _, err := exec.LookPath(executablePath); err != nil {
        return []Model{}, nil
    }
    ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
    defer cancel()

    cmd := exec.CommandContext(ctx, executablePath, "models", "--json")
    hideAgentWindow(cmd)
    out, err := cmd.Output()
    if err != nil {
        return []Model{}, nil
    }
    var models []Model
    if err := json.Unmarshal(out, &models); err != nil {
        return []Model{}, nil
    }
    return models, nil
}
```

`ModelSelectionSupported()` 无需特殊处理，`gocli` 默认 true。

配置对齐注意事项：

- `ListModels(ctx, provider, executablePath)` 当前只接收 executable path，不接收 daemon `ExtraArgs` / `CustomArgs`。
- 如果 go-cli-agent 需要非默认 config path，推荐在 daemon 进程环境设置 `GO_CLI_AGENT_CONFIG=/path/to/config.yaml`，这样 `go-cli-agent models --json` 和 `go-cli-agent exec ...` 都走同一配置。
- 不要把 `--config /path/to/config.yaml` 仅放进 `MULTICA_GOCLI_ARGS` 后就假设模型发现也会使用它；`MULTICA_GOCLI_ARGS` 只进入 task execution 的 `ExtraArgs`。

## 4. 修改 `server/pkg/agent/version.go`

```go
var MinVersions = map[string]string{
    "claude":  "2.0.0",
    "codex":   "0.100.0",
    "copilot": "1.0.0",
    "gocli":   "0.1.0",
}
```

前提：go-cli-agent 侧必须实现 `go-cli-agent --version`，输出中含 `0.1.0` 形态 semver。

## 5. 修改 `server/pkg/agent/thinking.go`

`providerThinkingEnums` 增加：

```go
"gocli": {
    "low":    true,
    "medium": true,
    "high":   true,
    "xhigh":  true,
},
```

这是 server-side 粗粒度 token gate。per-model 可用性由 daemon 调 `ListModels("gocli")` 返回的 `Model.Thinking` 决定。

## 6. 修改 `server/internal/daemon/config.go`

### 6.1 Config struct

注释中 agents list 加入 `gocli`，新增：

```go
GocliArgs []string
```

### 6.2 LoadConfig probe

在 agent probe 列表中加入：

```go
if e, ok := probe("MULTICA_GOCLI_PATH", "go-cli-agent", "MULTICA_GOCLI_MODEL"); ok {
    agents["gocli"] = e
}
```

错误提示加入 `go-cli-agent`。

### 6.3 Args env

```go
gocliArgs, err := shellArgsFromEnv("MULTICA_GOCLI_ARGS")
if err != nil {
    return Config{}, err
}
```

返回 Config 时设置 `GocliArgs: gocliArgs`。

### 6.4 default command names

`defaultAgentCommandNames` 加入 `"go-cli-agent"`。

`MULTICA_GOCLI_ARGS` 只用于 execution custom args。若需要影响 `models --json`，使用 daemon 环境里的 `GO_CLI_AGENT_CONFIG`，或在 Multica 另行扩展 model discovery 参数通道；不要让 execution 和 model discovery 读取不同 go-cli-agent config。

## 7. 修改 `server/internal/daemon/daemon.go`

`defaultArgsForProvider`：

```go
case "gocli":
    args = cfg.GocliArgs
```

其余 task execution path 不需要特殊分支；`agent.New(provider, ...)` 会创建 `gocliBackend`。

## 8. 修改 execenv

### 8.1 `server/internal/daemon/execenv/runtime_config.go`

`runtimeConfigPath` 中把 `gocli` 放入 `AGENTS.md` 组：

```go
case "codex", "copilot", "opencode", "openclaw", "hermes", "pi", "cursor", "kimi", "kiro", "antigravity", "gocli":
    return filepath.Join(workDir, "AGENTS.md")
```

`buildMetaSkillContent` 的 skills 自动发现分支加入 `gocli`，并更新注释：

```go
case "codex", "copilot", "opencode", "openclaw", "pi", "cursor", "kimi", "kiro", "antigravity", "gocli":
    b.WriteString("You have the following skills installed (discovered automatically):\n\n")
```

### 8.2 `server/internal/daemon/execenv/context.go`

`resolveSkillsDir` 增加：

```go
case "gocli":
    // go-cli-agent default config scans ./skills relative to task workdir.
    skillsDir = filepath.Join(workDir, "skills")
```

如果接入 local-skill list/copy UI，`gocli` 的本地运行时 skill root 应为 `~/.go-cli-agent/skills`，只作为“复制进 Multica workspace”前的私有来源。任务执行时不要让 gocli 默认扫描 `~/.codex/skills` 或其他全局 skill root；Multica 对 agent 的动态 skill 调节必须通过当前任务工作区的 `./skills` 落地。

### 8.3 tests

更新所有 provider list tests，例如：

- `server/internal/daemon/execenv/sidecar_manifest_test.go` 的 `allFileBasedProviders`
- `runtime_config_test.go` 中 `AGENTS.md` path cases
- `execenv_test.go` 中 known provider cases

## 9. 不需要改的文件

| 文件/区域 | 原因 |
| --- | --- |
| 数据库 schema | runtime provider 是 string |
| 前端 | 现有 model/thinking/runtime UI 由 backend data 驱动 |
| `server/internal/handler/runtime_models.go` | 通过 daemon model list report 间接使用 |
| `server/internal/handler/agent.go` | `IsKnownThinkingValue` 注册后自动支持 |
| `server/pkg/agent/proc_*.go` | 复用 `hideAgentWindow` |
| `server/pkg/agent/stderr_tail.go` | 复用现有 stderr tail |

## 10. PR 摘要建议

```text
feat(agent): add go-cli-agent backend

- Add gocli stream-json backend for go-cli-agent exec
- Register gocli in factory, launch headers, versions, models and thinking gates
- Discover go-cli-agent via MULTICA_GOCLI_PATH / PATH
- Wire gocli execenv to AGENTS.md and ./skills
- Add mock-backed unit tests for stream parsing, args, model discovery and runtime registration
```
