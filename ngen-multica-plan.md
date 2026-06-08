# NGEN 接入 Multica 二开方案

日期：2026-06-08

目标：在不改写 Multica 调度模型、不削弱 NGEN artifact-first 事实源的前提下，把 NGEN 作为 Multica 可调度 agent runtime 接入，用于 multi-agent 协作、长时程 coding/review/security 任务和 evidence-backed 子任务收敛。

## 0. MVP 决策锁定

以下决策已固定，不再作为开放项留给二开 agent 自行判断：

- Headless 命令名固定为 `ngen exec`；不新增 `ngen multica run`。
- Multica 不从外部向 NGEN 传 `--thinking-level`。NGEN 只通过 daemon-owned NGEN 配置持久化 provider thinking/reasoning，例如 OpenAI/GPT 的 `xhigh` 或 Anthropic/Claude 的 `max`；Multica 只展示或记录该配置派生的诊断信息，不提供 per-run thinking 选择。
- `blocked` / `needs_input` 是 Multica MVP 的 first-class 状态：Multica 数据模型、API、UI 必须明确展示等待用户输入、审批或 parent action，并提供 resume/continue 入口；不能把它退化成普通失败或只写 stderr。
- NGEN config 在 Multica MVP 中固定为 daemon-owned；不支持 per-agent custom config，也不允许 agent custom env 覆盖 `NGEN_CONFIG`。
- NGEN provider/model 固定由 daemon-owned NGEN 配置决定；Multica 不传 `--model`，不允许 per-run model switch。NGEN 首次 run 记录 config 派生的 effective model identity，resume 时发现 config/model drift 必须 fail closed。
- Multica 不开放 per-run permission mode；权限只能来自 daemon/admin 级配置或 NGEN 默认。NGEN 默认 permission mode 为 `yolo`。
- NGEN MVP 必须消费 `--workdir` 下的 `AGENTS.md` / workspace guidance，并纳入 session/system context；Multica 注入但 NGEN 不消费不能算 MVP 完成。
- `ngen exec` MVP 采用 hybrid streaming：运行中通过同进程 event sink/bus 输出低延迟 NDJSON，同时维护 durable high-water mark，并在 service 返回、blocked、failed、cancelled、context timeout、stdout writer shutdown 前，从 durable `events.jsonl` / task artifacts 做 final flush。stdout 只能由单一 encoder goroutine 写。最终 `result` 必须是最后一行。纯 batch 不满足 MVP。

## 1. 选型结论

除 `go-cli-agent` 外，最适合改造后接入 Multica 做 multi-agent 协作的是 `agent-import-tmp/ngen-agent`。

理由：

- NGEN 已经把 task truth 放在 workspace `.ngen/` artifact，而不是聊天历史；这和 Multica 的任务/issue/agent 调度视角天然匹配。
- NGEN 已有 `task.StatusSnapshot`、`task.Event`、criteria、verification、review、completion、handoff、harness evaluation、worker result/settlement/reconcile 等事实对象，适合给 Multica 提供可靠的进度、完成和交接投影。
- NGEN 的 worker contract 已包含 `ParentTaskID`、`ChildTaskID`、`Role`、`EvidenceScore`、`TrustedForParentCompletion`、`RequiresParentAction`、`ParentActionType` 等字段，适合承接 Multica large-project / multi-agent 协作，而不是只输出一段 prose。
- Deputy 更适合固定 master/meta/watcher/reviewer 长任务 capsule；Cairn 更适合 blackboard/graph search 安全探索。二者可作为专项 runtime，但要接入 Multica 的通用 multi-agent coding/review 协作，改造面和语义偏差都大于 NGEN。

## 2. 当前基线

NGEN 当前事实：

- CLI 路由位于 `agent-import-tmp/ngen-agent/internal/app/app.go` 的 `RunCLI`。
- 当前已有命令包括 `task`、`project`、`mission`、`goal`、`auto`、`run`、`resume`、`status`、`review`、`events`、`handoff`、`worker`、`harness`、`web`、`tui` 等。
- `auto|run|resume TASK-ID [--json]` 由 `runStreaming` 执行；`events tail TASK-ID --json --limit N --after EVENT-ID` 能输出 task event JSONL；`status TASK-ID --json` 能输出 `StatusSnapshot`；`handoff export TASK-ID` 已存在。
- `ngenrt.Service` 已有 `Create(ctx, task.TaskFile)`、`Run(ctx, taskID)`、`Resume(ctx, taskID)`；`Service.StartSession` / `PromptSession` 已存在但更适合交互式 TUI/terminal，不应作为 Multica headless 默认路径。
- 当前根命令未提供稳定 `ngen --version`，也没有 Multica 可直接消费的 `ngen models --json`。
- 当前 provider mode 包括 `builtin`、`command`、`openai-response`、`openai-comp`、`anthropic`；`provider_usage.jsonl` 已记录 sanitized provider usage，unknown usage 不写成 0。

Multica 当前事实：

- 当前参考 checkout：`/tmp/multica-ngen-plan-20260608`，HEAD 为 `b89b9cb4d6687fd3a8470b032d2a7b3a0d40dc73`。实际开发前必须重新确认当前 upstream HEAD，不能直接套用旧 go-cli-agent 接入分支。
- `server/pkg/agent.Backend` 接口是 `Execute(ctx, prompt, opts) (*Session, error)`。
- `ExecOptions` 当前包含 `Cwd`、`Model`、`SystemPrompt`、`MaxTurns`、`Timeout`、`SemanticInactivityTimeout`、`ResumeSessionID`、`ExtraArgs`、`CustomArgs`、`McpConfig`、`ThinkingLevel`。
- `Message` 当前支持 `text`、`thinking`、`tool-use`、`tool-result`、`status`、`error`、`log`；`Result` 包含 `Status`、`Output`、`Error`、`DurationMs`、`SessionID`、`Usage map[string]TokenUsage`。
- 当前 agent factory 支持 `claude,codex,copilot,opencode,openclaw,hermes,gemini,pi,cursor,kimi,kiro,antigravity`，尚无 `ngen`。

## 3. 非目标

- 不让 Multica 直接读取 `.ngen/` 私有目录作为权威状态；Multica 只消费 NGEN stdout 协议、final result、resume id 和 artifact refs。
- 不把 NGEN 的 phase/review/verifier gate 重写成 Multica 内部 workflow engine。
- 不把 Multica 的 issue/agent/team 调度状态写回 `.ngen/` 作为 NGEN task truth。
- 不把 NGEN interactive session bridge 作为第一版 subprocess backend 默认路径；第一版走 task-first headless run/resume。
- 不要求 NGEN 支持 hosted SaaS、browser file editor、远程终端或 Multica UI 专属状态源。

## 4. 总体架构

边界：

- Multica 负责：issue/team/agent 调度、workspace 准备、AGENTS.md brief 注入、shared skills 注入、进程生命周期、stdout/stderr 采集、用户可见 session result。
- NGEN 负责：task creation/resume、provider decision、workspace edit/command/verifier/review/completion、worker lifecycle、`.ngen/` artifact truth、handoff 和 restore clues。
- 耦合面唯一化：`ngen exec --output-format stream-json --input-format stream-json` stdout NDJSON + `ngen models --json` + `ngen --version`。

主路径：

1. Multica 选择 provider=`ngen`，创建工作目录并注入 `AGENTS.md` 与 `skills/`。
2. Multica 调用 `ngen exec ...`，stdin 写入一个 user envelope 后关闭。
3. NGEN 创建或恢复 task，运行 `Auto/Resume`，把 status/event/handoff/result 投影成 `ngen-stream-json` NDJSON。
4. Multica 只把 NDJSON 转成 `agent.Message`/`agent.Result`，并把 `Result.SessionID` 设为 NGEN `task_id`。
5. 下次 resume 时，Multica 用 `ExecOptions.ResumeSessionID` 传回同一个 NGEN `task_id`。

关键映射：

- Multica `Result.SessionID` = NGEN `task_id`，不是 NGEN `session_id`。
- `ExecOptions.ResumeSessionID` = NGEN `task_id`。
- NGEN `run_role` 是 Multica exec metadata，例如 `orchestrator|worker|validator|reviewer`，不能写进 transcript `message.role`，也不能声称等同于 NGEN mission role 常量。NGEN mission roles 仍是 `orchestrator|workers|validators`，worker task roles 仍是 `coding|reviewer|security_review|general_execution`。
- NGEN `.ngen/` artifact ref 可以进入 result/handoff，但 Multica 不应把这些 ref 解析为权威 schema。

### 4.1 拆分完整性与兼容性检查

拆分结论：

- NGEN 侧改动集中在第 5 章：只新增或扩展 NGEN 自身 CLI、stream protocol、artifact projection、config resolver、workspace guidance ingestion 和测试；不要求 Multica 直接读写 `.ngen/`。
- Multica 侧改动集中在第 6 章：只新增 `ngen` backend、factory/model/version 注册、禁用外部 thinking 选择、read-only thinking metadata、daemon env/config、execenv brief/skills path 和测试；不要求修改 NGEN 内部 artifact schema 以外的 Multica 调度事实源。
- 共享 wire contract 只在第 4 章和 5.4/6.1 定义：stdout NDJSON、`Result.SessionID=task_id`、config 派生的 model identity、first-class `blocked/needs_input` status、usage、handoff refs。共享层不能扩展成第二套 workflow engine。

不影响 NGEN 原有功能的约束：

- `task/project/mission/goal/auto/run/resume/status/review/events/handoff/worker/harness/web/tui` 等既有命令保持原语义；新增 `exec` 不能替换或改变当前 `runStreaming` 的 batch 输出行为。
- `Service.StartSession` / `PromptSession` 仍服务 TUI/terminal 交互，不被 Multica headless path 设为默认。
- 非 Multica 场景继续允许 `<workdir>/ngen.json` 按 NGEN 原优先级影响 provider config；`--config-scope daemon` 只用于 Multica backend 收口 model discovery / exec 一致性。
- `<workdir>/skills` 和 `AGENTS.md` ingestion 是新增 Multica profile 能力；若 NGEN 本地模式已有其它 discovery 行为，不能被静默改写。

不影响 Multica 原有功能的约束：

- 现有 `claude/codex/copilot/opencode/openclaw/hermes/gemini/pi/cursor/kimi/kiro/antigravity` backend 不改默认参数、model discovery、skills path 或 runtime brief。
- `agent.ListModels(ctx, providerType, executablePath)` 的现有签名在 MVP 中不强改；NGEN 通过 daemon-owned `NGEN_CONFIG` 和 `ngenDiscoveryEnvKey()` 保证 discovery/exec 一致。
- `runtimeConfigPath`、`skillsDirPath`、`defaultAgentCommandNames`、`defaultArgsForProvider` 只增加 `ngen` 分支；未知 provider fallback 行为不变。
- blocked/custom-args/env 限制只针对 NGEN backend 新增协议关键参数，不改变其它 backend 的 custom args 兼容面。

## 5. NGEN 侧开发

### 5.1 新增版本发现

修改：

- `internal/app/app.go`
- 新增 `internal/version/version.go` 或复用已有 build metadata 包。
- 重构 CLI pre-dispatch：当前 `RunCLI` 会在 subcommand switch 前用当前 cwd 执行 `task.LoadConfig` 并构造 service；新增 `--version`、`version`、`models`、`exec` 时必须先解析根级/preflight 参数，再决定是否加载 workspace config。

命令：

```text
ngen --version
ngen version --json
```

要求：

- `ngen --version` 输出可被 Multica `version.go` 的 semver regex 解析，例如 `ngen 0.1.0`.
- `version --json` 输出：

```go
type VersionInfo struct {
    Name      string `json:"name"`       // "ngen"
    Version   string `json:"version"`    // "0.1.0"
    Commit    string `json:"commit,omitempty"`
    BuiltAt   string `json:"built_at,omitempty"`
    Protocol  string `json:"protocol"`   // "ngen-stream-json"
    ProtocolVersion int `json:"protocol_version"` // 1
}
```

一致性检查：

- `--version` 不读取 workspace config，不要求 API key。
- 即使当前 cwd 有坏的 `ngen.json`，`ngen --version` 也必须成功。
- stderr 只写诊断，不能写 JSON protocol line。

### 5.2 新增模型发现

修改：

- `internal/app/app.go`
- 新增 `internal/models/list.go` 或 `internal/multica/models.go`。
- 必要时扩展 `task.Config` 的 provider/model metadata，但不要破坏现有 `ngen.json`。
- 新增 `resolveNGENWorkdirAndConfig(args, env)`：`models` 和 `exec` 必须在构造 `ngenrt.Service` 前解析 `--workdir`、`--config`、`NGEN_CONFIG`，不能沿用当前 cwd-first 的 `RunCLI` 前置 load。

命令：

```text
ngen models --json [--workdir <dir>] [--config <file>]
```

输出：

```go
type ModelInfo struct {
    ID       string              `json:"id"`
    Label    string              `json:"label"`
    Provider string              `json:"provider,omitempty"`
    Default  bool                `json:"default,omitempty"`
    Thinking *ModelThinkingInfo  `json:"thinking,omitempty"` // read-only config-derived metadata
}

type ModelThinkingInfo struct {
    ConfiguredLevel string `json:"configured_level,omitempty"` // e.g. "xhigh" or "max"
    Source          string `json:"source,omitempty"`           // "daemon_config", "provider_default"
    Provider        string `json:"provider,omitempty"`
}
```

模型 ID 规范：

- `openai-response/gpt-5.5`
- `openai-comp/gpt-5.4`
- `anthropic/claude-sonnet-4-6`
- `builtin/default`
- `command/default`

规则：

- ID 第一段是 NGEN provider mode route key，不是显示分组随意文案。
- `openai-response/gpt-5.5` 这类 model identity 必须可逆拆分为 `provider.mode=openai-response` 与 `provider.model=gpt-5.5`，但 Multica MVP 不把它作为 per-run `--model` 传回 NGEN。
- `builtin/default` 和 `command/default` 可以显示为不可选或低优先级默认项；如果实际没有 model override 语义，Multica 仍可列出但 execution 时不要把 `default` 写成远端模型名。
- `models --json` 和 `exec` 必须读取同一份 config 来源。NGEN 侧支持优先级：显式 `--config` > `NGEN_CONFIG` > `<workdir>/ngen.json` > 默认 config；Multica 侧 MVP 只使用 daemon-owned `NGEN_CONFIG`，见 6.2/6.3。
- Multica provider=`ngen` MVP 的额外收口：如果 daemon 未设置 `NGEN_CONFIG`，Multica 的 NGEN backend 仍可传 `--workdir` 作为 workspace root，但必须让 NGEN exec 使用与 `models --json` 相同的 provider catalog default config，不能让 `<workdir>/ngen.json` 改变 provider mode/model catalog。MVP 明确新增并使用 `ngen exec --config-scope daemon`，只禁止 workdir config 影响 provider/model discovery 与 provider selection，不禁止 `.ngen/` artifact/workspace root 使用 `--workdir`；若未来保留 `--ignore-workdir-provider-config`，它只能作为兼容别名，不作为 Multica 侧默认接口。
- NGEN effective provider/model 只来自 daemon-owned config。Multica 不传 `--model`，不允许 per-run model switch；`models --json` 只用于 discovery/display 和 config/execution 一致性校验。

### 5.3 新增 Multica headless exec 命令

修改：

- `internal/app/app.go`
- 新增 `internal/multica/streamjson.go`
- 新增 `internal/multica/exec.go`
- 新增 `internal/multica/run_metadata.go`，定义并持久化 Multica/NGEN run metadata artifact。
- 必要时在 `ngenrt.Service` 增加小的 facade 方法，避免 CLI 层直接读太多 artifact internals。
- 不复用当前 `runStreaming` 作为协议实现：当前 `runStreaming` 是 `svc.Run/Resume/Auto` 返回后批量打印事件，不是真实时流。`ngen exec` 必须采用 hybrid streaming：同进程 event sink/bus 负责低延迟输出，durable `events.jsonl` / task artifacts high-water flush 负责补漏、恢复和 final consistency。
- live stream 实现约束：stdout 必须由单一 encoder goroutine 串行写；event sink/tailer 维护 `event_id` high-water mark；service 返回、blocked、failed、cancelled、context timeout、stdout writer shutdown 前都必须从 durable facts flush 未发关键事件，再输出唯一 final result。
- bounded-near-real-time 目标：正常事件到 stdout 延迟不超过 1 秒；如果底层只能 polling，poll interval 不超过 500ms。长时间无新事件但 task 仍 running 时，可输出不改变事实源的 heartbeat `status`，避免 Multica inactivity watchdog 误判。
- 背压策略必须 fail closed：关键消息 `system`、`status=blocked/needs_input`、`tool_result`、`result` 不能静默丢弃；普通高频 log 可合并并输出 dropped-count 诊断。stdout 写失败时取消 run context 并返回非零。

命令：

```text
ngen exec \
  --output-format stream-json \
  --input-format stream-json \
  --config-scope daemon \
  --workdir <path> \
  [--config <file>] \
  [--resume <task-id>] \
  [--role orchestrator|worker|validator|reviewer] \
  [--timeout-seconds <n>]
```

行为：

- stdin 必须读取 exactly one JSON envelope，然后关闭输入；第一版不做 stdin 控制流和 mid-run steering。
- 无 `--resume` 时，用 envelope prompt 创建 root task。
- 有 `--resume` 时，把值当 NGEN `task_id`，根据 task 当前 state 走 `Resume` 或 `Auto`；不要把它当 `.ngen/sessions` 的 `session_id`。
- `--workdir` / `--config` / `NGEN_CONFIG` 必须在 service 构造前生效；不能先用当前 cwd 构造 service 再解析 exec flags。
- effective provider/model 由 daemon-owned NGEN config 决定，并写入 durable run metadata，在 final result `metadata.model_route/provider_mode/provider_model/config_fingerprint` 和 handoff digest 中回显。resume 时必须从 metadata 读取首次运行的 effective model identity；如果当前 daemon config 派生出的 identity 或 config fingerprint 与首次运行不一致，MVP fail closed 并返回 `status_reason_code="multica_model_config_drift"`。
- stdout 只输出 NDJSON protocol line；stderr 只输出人类可读诊断。
- final result 必须是最后一条 stdout JSON line。
- context cancellation 由进程信号/context 处理；第一版不设计 stdin cancel 控制包。

stdin envelope：

```go
type StreamInputMessage struct {
    Protocol        string            `json:"protocol"`
    ProtocolVersion int               `json:"protocol_version"`
    Type            string            `json:"type"` // "user"
    ID              string            `json:"id,omitempty"`
    Role            string            `json:"role"` // "user"
    Content         []ContentBlock    `json:"content"`
    SystemPrompt    string            `json:"system_prompt,omitempty"`
    Metadata        map[string]string `json:"metadata,omitempty"`
}

type ContentBlock struct {
    Type string `json:"type"` // "text"
    Text string `json:"text,omitempty"`
}
```

task creation defaults:

- `Kind`: `coding` when workdir is a git/code repo and prompt implies code change; otherwise `general_execution` with `PresetID: task.PresetDocsLite`。当前 NGEN `general_execution` 必须配 `docs_lite` preset；实现不能创建裸 `general_execution` task。
- `Title`: deterministic first-line summary, capped.
- `Objective`: full prompt + relevant Multica metadata.
- `SuccessCriteria`: derive 1-3 criteria from prompt only when safe; otherwise create one criterion: "Produce a verifiable handoff/result for the requested work."
- `WorkspaceRoot`: `--workdir`.
- `PermissionModeID`: 只来自 daemon-owned config/admin 配置或 NGEN 默认；CustomArgs、ExtraArgs、`MULTICA_NGEN_ARGS` 都不能覆盖。NGEN 默认 `yolo`。

durable run metadata artifact:

```go
type MulticaRunMetadata struct {
    ObjectKind       string `json:"object_kind"` // "multica_run_metadata"
    SchemaVersion    int    `json:"schema_version"`
    TaskID           string `json:"task_id"`
    SessionID        string `json:"session_id"` // equals task_id for Multica
    RunID            string `json:"run_id"`
    Source           string `json:"source"` // "multica"
    ModelRoute       string `json:"model_route,omitempty"` // config-derived effective identity
    ProviderMode     string `json:"provider_mode,omitempty"`
    ProviderModel    string `json:"provider_model,omitempty"`
    ConfigSource     string `json:"config_source,omitempty"` // "daemon:NGEN_CONFIG", "default", etc.
    ConfigFingerprint string `json:"config_fingerprint,omitempty"`
    PermissionModeID string `json:"permission_mode_id,omitempty"`
    CreatedAt        string `json:"created_at"`
    UpdatedAt        string `json:"updated_at"`
}
```

Storage:

- Path: `.ngen/tasks/<task_id>/multica/run_metadata.json`，or equivalent task-local artifact path behind `artifact.Store` helpers such as `SaveMulticaRunMetadata` / `ReadMulticaRunMetadata`。
- First run: write the artifact immediately after task creation and before provider execution.
- Resume: read the artifact before provider execution; if absent for a Multica-resumed task, fail closed with `result.status="blocked"` and `status_reason_code="multica_run_metadata_missing"`。MVP 不做 route adopt，也不做 model switch migration。
- Config/model drift: do not mutate the artifact; emit final `result.status="blocked"` with `status_reason_code="multica_model_config_drift"`，preserve `SessionID=task_id`，and include old/new config fingerprint and effective model identity in bounded metadata.
- Handoff/result: project `model_route/provider_mode/provider_model/config_fingerprint` from this artifact, not from ad hoc current CLI flags.

### 5.4 Stream output contract

Protocol name: `ngen-stream-json`

Version: `1`

Types:

- `system`: protocol/session/task header.
- `status`: task status snapshot projection.
- `assistant`: human-readable progress or final assistant text.
- `log`: bounded diagnostic/progress line.
- `tool_use`: optional projection for NGEN command/workspace actions when stable call IDs exist.
- `tool_result`: optional projection for NGEN command/workspace results when stable call IDs exist.
- `result`: final outcome, last line only.

Go structs:

```go
const ProtocolName = "ngen-stream-json"
const ProtocolVersion = 1

type StreamOutputMessage struct {
    Type            string             `json:"type"`
    Protocol        string             `json:"protocol,omitempty"`
    ProtocolVersion int                `json:"protocol_version,omitempty"`
    ID              string             `json:"id,omitempty"`
    TaskID          string             `json:"task_id,omitempty"`
    SessionID       string             `json:"session_id,omitempty"` // equals TaskID for Multica
    RunRole         string             `json:"run_role,omitempty"`
    ModelRoute      string             `json:"model_route,omitempty"`
    ProviderMode    string             `json:"provider_mode,omitempty"`
    ProviderModel   string             `json:"provider_model,omitempty"`
    Status          string             `json:"status,omitempty"`
    IsError         bool               `json:"is_error,omitempty"`
    Message         *ContentMessage    `json:"message,omitempty"`
    Tool            *ToolProjection    `json:"tool,omitempty"`
    Log             *LogEntry          `json:"log,omitempty"`
    Usage           map[string]Usage   `json:"usage,omitempty"`
    Handoff         *StructuredHandoff `json:"handoff,omitempty"`
    Metadata        map[string]any     `json:"metadata,omitempty"`
}

type ContentMessage struct {
    Role    string         `json:"role"` // transcript role only
    Content []ContentBlock `json:"content"`
}

type ToolProjection struct {
    Name   string         `json:"name"`
    CallID string         `json:"call_id"`
    Input  map[string]any `json:"input,omitempty"`
    Output string         `json:"output,omitempty"`
    Status string         `json:"status,omitempty"`
}

type LogEntry struct {
    Level   string `json:"level,omitempty"`
    Message string `json:"message"`
}

type Usage struct {
    InputTokens      int64 `json:"input_tokens,omitempty"`
    OutputTokens     int64 `json:"output_tokens,omitempty"`
    CacheReadTokens  int64 `json:"cache_read_tokens,omitempty"`
    CacheWriteTokens int64 `json:"cache_write_tokens,omitempty"`
}
```

Structured handoff:

```go
type StructuredHandoff struct {
    Summary             string            `json:"summary"`
    TaskID              string            `json:"task_id"`
    State               string            `json:"state"`
    Phase               string            `json:"phase"`
    StatusReasonCode    string            `json:"status_reason_code,omitempty"`
    ModelRoute          string            `json:"model_route,omitempty"`
    ProviderMode        string            `json:"provider_mode,omitempty"`
    ProviderModel       string            `json:"provider_model,omitempty"`
    HandoffRef          string            `json:"handoff_ref,omitempty"`
    CompletionRef       string            `json:"completion_ref,omitempty"`
    VerificationRef     string            `json:"verification_ref,omitempty"`
    ReviewRef           string            `json:"review_ref,omitempty"`
    CriteriaRef         string            `json:"criteria_ref,omitempty"`
    RestoreRefs         []ArtifactRef     `json:"restore_refs,omitempty"`
    OpenCriteria        []CriterionDigest `json:"open_criteria,omitempty"`
    MetCriteria         []CriterionDigest `json:"met_criteria,omitempty"`
    WorkerResults       []WorkerDigest    `json:"worker_results,omitempty"`
    Mission             *MissionDigest    `json:"mission,omitempty"`
    RecommendedCommands []HandoffCommand  `json:"recommended_commands,omitempty"`
}

type ArtifactRef struct {
    Ref     string `json:"ref"`
    Kind    string `json:"kind,omitempty"`
    Summary string `json:"summary,omitempty"`
}

type CriterionDigest struct {
    ID           string   `json:"id"`
    Statement    string   `json:"statement,omitempty"`
    Status       string   `json:"status"`
    EvidenceRefs []string `json:"evidence_refs,omitempty"`
}

type WorkerDigest struct {
    WorkerID                   string   `json:"worker_id"`
    ChildTaskID                string   `json:"child_task_id"`
    Role                       string   `json:"role"`
    Status                     string   `json:"status"`
    ChildState                 string   `json:"child_state,omitempty"`
    CompletionStatus           string   `json:"completion_status,omitempty"`
    ReviewStatus               string   `json:"review_status,omitempty"`
    VerificationStatus         string   `json:"verification_status,omitempty"`
    BlockedReasonCode          string   `json:"blocked_reason_code,omitempty"`
    BlockedDetailRef           string   `json:"blocked_detail_ref,omitempty"`
    Summary                    string   `json:"summary,omitempty"`
    EvidenceGrade              string   `json:"evidence_grade,omitempty"`
    EvidenceScore              int      `json:"evidence_score,omitempty"`
    MissingEvidence            []string `json:"missing_evidence,omitempty"`
    TrustedForParentCompletion bool     `json:"trusted_for_parent_completion,omitempty"`
    RequiresParentAction       bool     `json:"requires_parent_action,omitempty"`
    ParentActionType           string   `json:"parent_action_type,omitempty"`
    ParentActionOptions        []string `json:"parent_action_options,omitempty"`
    ParentActionSummary        string   `json:"parent_action_summary,omitempty"`
    ParentActionUnresolved     bool     `json:"parent_action_unresolved,omitempty"`
    ConflictCount              int      `json:"conflict_count,omitempty"`
    EvidenceRefs               []string `json:"evidence_refs,omitempty"`
}

type MissionDigest struct {
    MissionID           string `json:"mission_id,omitempty"`
    Status              string `json:"status,omitempty"`
    CurrentMilestoneID  string `json:"current_milestone_id,omitempty"`
    LatestValidationRef string `json:"latest_validation_ref,omitempty"`
}

type HandoffCommand struct {
    Kind    string   `json:"kind,omitempty"`
    Command []string `json:"command"`
    Reason  string   `json:"reason,omitempty"`
}
```

### 5.5 Projection rules

NGEN `task.Event` -> Multica stream:

- 采用“当前事件白名单 + fallback log”。已确认或应覆盖的当前事件包括：`observation_command_started|observation_command_completed|observation_command_failed|repair_command_started|repair_command_completed|repair_command_failed|workspace_edit_started|workspace_edit_applied|workspace_edit_failed|workspace_edit_noop|verification_passed|verification_failed|review_completed|completion_accepted|completion_rejected|done|approval_requested|input_requested|worker_spawned|worker_continued|worker_settled|worker_reconciled|hook_failed|hook_executed|watch_*`。
- 未识别事件默认投影为 bounded `log`，保留 `event_id/type/summary/refs`；不要在 plan 中假设当前不存在的 `verification_started`、`review_started` 等类型。
- When a durable command record has a stable command id, emit `tool_use` with `call_id=<command_record_id or command_id>`.
- Command completion/failure -> `tool_result` with same `call_id`.
- If no stable ID can be guaranteed, emit only `log`; do not invent unstable call IDs.

NGEN `task.StatusSnapshot` -> `status`:

- `StateDone` -> result status candidate `completed`.
- `StateBlocked` or approval/input wait -> NGEN stream emits `status=blocked` with `metadata.needs_input=true` when user input/approval/parent action is required, and final `result.status="blocked"`。Multica MVP 必须把该状态作为 first-class `blocked/needs_input` 持久化、API 返回并在 UI 展示 resume/continue 入口，不能退化为普通 agent error。
- `StateFailed|StateAborted` -> `failed`/`aborted`.
- Preserve `task_id`, `phase`, `state`, `status_reason_code`, `handoff_ref`, `last_checkpoint_ref`, `restore_clues`.

Completion/handoff:

- Prefer `task.CompletionReport.Summary`.
- Include latest `CriteriaSnapshot` with open/met counts and evidence refs.
- Include latest `VerificationReport` and `ReviewReport` refs and summaries.
- Include `WorkerResult` only with trust fields; Multica must not treat prose-only worker summary as trusted completion when `TrustedForParentCompletion=false`.
- Include mission validation refs when task has `MissionID`.

Usage:

- Parse NGEN task-local `provider_usage.jsonl` or latest `HarnessEvaluation.ProviderUsageRef`.
- 当前 NGEN `ProviderUsageRecord.TokenUsage` / `PromptCacheUsage` 是字符串摘要，不是 typed numeric struct；新增 `parseUsageSummary`，只解析 `key=int` token。
- `input_tokens` / `output_tokens` 映射到 `InputTokens` / `OutputTokens`。
- Anthropic-style `cache_creation_input_tokens` 映射到 `CacheWriteTokens`，`cache_read_input_tokens` 映射到 `CacheReadTokens`。
- `unknown`、空值、无法解析的字段全部 omit；unknown usage 永远不写 0。

### 5.6 Config, skills, and workspace discovery

NGEN config source:

- Support `--config <file>` and/or `NGEN_CONFIG`.
- Multica MVP 选择 daemon-owned `NGEN_CONFIG`：daemon 进程环境中的 `NGEN_CONFIG` 同时影响 `ngen models --json` 和 `ngen exec`；agent custom env 不允许覆盖它。
- `models --json` and `exec` must use the same config resolution function.
- 如果未来允许 per-agent custom env/config 影响 NGEN discovery，则必须先扩展 Multica `ListModels`/discovery 参数通道和 cache key，把 config/env/workdir fingerprint 纳入 model discovery；在此之前不要声称 per-agent custom config 与 execution catalog 完全一致。

Skills:

- 当前 NGEN 没有已确认的 runtime skill scanner，也没有已确认的 target workspace `AGENTS.md` loader；本方案要求新增 NGEN workspace guidance/skill ingestion feature，而不是假设现有 provider input 已消费这些文件。
- For Multica workspace runs, NGEN must scan or ingest `<workdir>/skills` as the workspace shared skills directory, ideally materialized into a task artifact/provider input section such as `WorkspaceGuidance` / `WorkspaceSkills`.
- Do not default Multica runs to `~/.codex/skills` or other private local runtime skill stores.
- If NGEN already has a native skill scanner elsewhere, add `<workdir>/skills` as an explicit Multica profile scan root rather than silently changing all local behavior.

AGENTS.md:

- Multica should inject its runtime brief into `<workdir>/AGENTS.md`.
- NGEN must read this as normal workspace guidance and include it in the task/session system context. Multica injection without NGEN-side ingestion does not satisfy MVP.

### 5.7 NGEN tests

Add focused tests:

- `ngen --version` output parses as semver and does not require config.
- From a cwd containing malformed `ngen.json`, `ngen --version` and `ngen version --json` still succeed.
- From an unrelated cwd, `ngen models --json --workdir <target>` and non-Multica `ngen exec --workdir <target>` load the target workspace config before constructing service. Multica `ngen exec --config-scope daemon --workdir <target>` must not let target `ngen.json` change provider catalog.
- `ngen models --json` returns config-derived model identities and uses same daemon-owned config as exec.
- `openai-response/gpt-5.5` splits into provider mode + model and round-trips.
- `exec` without resume creates a task and emits `system`, live/bounded `status` or `log` during execution, durable high-water flushes missing critical events before final, and then emits final `result`.
- `general_execution` task creation sets `PresetID: task.PresetDocsLite`; no test should create naked `KindGeneral`.
- `exec --resume TASK-ID` resumes by task id, not session id.
- first run writes `.ngen/tasks/<task_id>/multica/run_metadata.json` with config-derived `model_route/provider_mode/provider_model/config_fingerprint`; resume with unchanged config-derived identity succeeds, config/model drift returns `status_reason_code=multica_model_config_drift`.
- Multica-resumed task without run metadata returns `status_reason_code=multica_run_metadata_missing`; MVP 不做 route adopt。
- stdout NDJSON remains valid with stderr diagnostics present.
- final result is the last stdout line.
- `ngen exec` does not accept external `--model` or `--thinking-level` for Multica MVP; effective model and thinking level come from daemon-owned NGEN config.
- NGEN default permission mode is `yolo`; any admin/config override is recorded in run metadata, and custom/user args cannot override protocol-critical flags or `--permission-mode`.
- `<workdir>/AGENTS.md` is consumed into task/session context; a test must fail if Multica injects AGENTS.md but provider input omits it.
- hybrid streaming emits intermediate events before completion, preserves critical events after simulated bus/drop/backpressure through durable flush, and keeps final `result` as the last stdout line.
- usage unknown is omitted, not zeroed.
- `parseUsageSummary` handles `input_tokens`, `output_tokens`, `cache_creation_input_tokens`, `cache_read_input_tokens`, and omits `unknown`.
- worker trust fields survive handoff projection.
- `result.status=blocked` preserves `SessionID=task_id` and includes `handoff.worker_results[].requires_parent_action` when present.
- artifact refs are bounded strings; no raw provider payload/API key/full hidden prompt is emitted.

## 6. Multica 侧开发

### 6.1 新增 backend

新增文件：

- `server/pkg/agent/ngen.go`
- `server/pkg/agent/ngen_test.go`

注册点：

- `server/pkg/agent/agent.go`
- `server/pkg/agent/models.go`
- `server/pkg/agent/version.go`
- `server/pkg/agent/thinking.go`

backend skeleton：

```go
type ngenBackend struct {
    cfg Config
}

type ngenOutputMessage struct {
    Type            string                 `json:"type"`
    Protocol        string                 `json:"protocol,omitempty"`
    ProtocolVersion int                    `json:"protocol_version,omitempty"`
    ID              string                 `json:"id,omitempty"`
    TaskID          string                 `json:"task_id,omitempty"`
    SessionID       string                 `json:"session_id,omitempty"`
    RunRole         string                 `json:"run_role,omitempty"`
    ModelRoute      string                 `json:"model_route,omitempty"`
    ProviderMode    string                 `json:"provider_mode,omitempty"`
    ProviderModel   string                 `json:"provider_model,omitempty"`
    Status          string                 `json:"status,omitempty"`
    IsError         bool                   `json:"is_error,omitempty"`
    Message         *ngenContentMessage    `json:"message,omitempty"`
    Tool            *ngenToolProjection    `json:"tool,omitempty"`
    Log             *ngenLogEntry          `json:"log,omitempty"`
    Usage           map[string]ngenUsage   `json:"usage,omitempty"`
    Handoff         *ngenStructuredHandoff `json:"handoff,omitempty"`
    Metadata        map[string]any         `json:"metadata,omitempty"`
}

type ngenInputMessage struct {
    Protocol        string             `json:"protocol"`
    ProtocolVersion int                `json:"protocol_version"`
    Type            string             `json:"type"`
    Role            string             `json:"role"`
    Content         []ngenContentBlock `json:"content"`
    SystemPrompt    string             `json:"system_prompt,omitempty"`
    Metadata        map[string]string  `json:"metadata,omitempty"`
}
```

Execution args:

```text
ngen exec --output-format stream-json --input-format stream-json --workdir <opts.Cwd> --config-scope daemon
```

Only append `--timeout-seconds strconv.Itoa(int(opts.Timeout.Seconds()))` when `opts.Timeout > 0`。When timeout is zero, pass no timeout flag; Multica's context/inactivity watchdog owns lifecycle, matching current `runContext` semantics.

Conditional args:

- `opts.ResumeSessionID` -> `--resume <task_id>`.
- `opts.Model` -> ignored/logged for provider=`ngen`; Multica MVP does not support per-run model selection and must not append `--model`.
- `opts.ThinkingLevel` -> ignored/logged for provider=`ngen`; Multica MVP does not support external `--thinking-level`.
- `opts.SystemPrompt` -> stdin envelope `system_prompt`, not shell arg.
- `opts.MaxTurns` -> ignore/log unless NGEN defines a compatible bounded turn count.

Blocked args:

```go
var ngenBlockedArgs = map[string]blockedArgMode{
    "--output-format":    blockedWithValue,
    "--input-format":     blockedWithValue,
    "--workdir":          blockedWithValue,
    "--cwd":              blockedWithValue,
    "--resume":           blockedWithValue,
    "--model":            blockedWithValue,
    "--provider":         blockedWithValue,
    "--provider-mode":    blockedWithValue,
    "--role":             blockedWithValue,
    "--permission-mode":  blockedWithValue,
    "--thinking-level":   blockedWithValue,
    "--timeout":          blockedWithValue,
    "--timeout-seconds":  blockedWithValue,
    "--config":           blockedWithValue,
    "--config-scope":     blockedWithValue,
    "--ignore-workdir-provider-config": blockedStandalone,
    "--json":             blockedStandalone,
}
```

If Multica supports daemon-wide `NgenArgs`, the same blocked filter applies to both `ExtraArgs` and `CustomArgs`. Protocol-critical flags must always be owned by the backend.

Args order:

```go
args := []string{"exec", "--output-format", "stream-json", "--input-format", "stream-json"}
if opts.Cwd != "" { args = append(args, "--workdir", opts.Cwd) }
args = append(args, "--config-scope", "daemon")
if opts.Timeout > 0 { args = append(args, "--timeout-seconds", strconv.Itoa(int(opts.Timeout.Seconds()))) }
if opts.ResumeSessionID != "" { args = append(args, "--resume", opts.ResumeSessionID) }
args = append(args, filterCustomArgs(opts.ExtraArgs, ngenBlockedArgs, b.cfg.Logger)...)
args = append(args, filterCustomArgs(opts.CustomArgs, ngenBlockedArgs, b.cfg.Logger)...)
```

Process handling:

- Resolve binary with `exec.LookPath`, default command `ngen`.
- Use `runContext(ctx, opts.Timeout)` and `cmd.WaitDelay = 10 * time.Second`.
- Set `cmd.Dir = opts.Cwd` and, when useful for NGEN discovery, append `PWD=<opts.Cwd>`.
- Build env with `buildEnv(b.cfg.Env)`。`NGEN_CONFIG` is daemon-owned for MVP: it may be present in the daemon process/backend env, but agent custom env must not override it.
- If daemon-owned `NGEN_CONFIG` is unset, backend still passes `--config-scope daemon` so `exec --workdir` does not read `<workdir>/ngen.json` for provider catalog selection. The workdir remains the task workspace and `.ngen/` artifact root.
- stdin: JSON-encode one `ngenInputMessage`, append newline, close pipe.
- stdout: scan NDJSON with 10 MiB max scanner buffer.
- stderr: use `newStderrTail(newLogWriter(...), agentStderrTailBytes)` and wrap failures with `withAgentStderr(msg, "ngen", tail)`.

Output mapping:

- `system` with `task_id/session_id` -> emit `MessageStatus{Status:"started", SessionID:task_id}`.
- `status` -> `MessageStatus{Status:event.Status, SessionID:task_id}`.
- `assistant` text blocks -> `MessageText`.
- `log` -> `MessageLog`.
- `tool_use` -> `MessageToolUse{Tool,CallID,Input}`.
- `tool_result` -> `MessageToolResult{Tool,CallID,Output,Status}`.
- `result` -> final `Result`; do not emit more messages after it.

Result mapping:

- `Result.SessionID` = `msg.SessionID`, falling back to `msg.TaskID`.
- `Result.Output` = handoff summary or accumulated assistant text.
- `Result.Status` = NGEN result status; if absent, fallback from process exit and last status.
- `Result.Status="blocked"` / `needs_input` is first-class for NGEN blocked/approval/input wait. Multica data model, API, and UI must persist and show the waiting reason, parent action metadata, and resume/continue action; parser tests must prove this status is never treated as `completed` or generic failure.
- `Result.Usage` = converted `map[string]TokenUsage`; if nil/empty, leave nil.

Exit code fallback:

- Exit 0 + no result line = `failed` with protocol error.
- Context deadline = `timeout`.
- Context cancellation = `aborted` or `cancelled` following existing backend convention.
- Non-zero exit + result line may keep NGEN status only if the result line explicitly reports terminal status; otherwise `failed`.

### 6.2 Factory and public metadata

Modify `server/pkg/agent/agent.go`:

- Add `ngen` to package comment and supported type list.
- Add `case "ngen": return &ngenBackend{cfg: cfg}, nil`.
- Add launch header: `"ngen": "ngen exec (stream-json)"`.

Modify `server/pkg/agent/models.go`:

- Add `case "ngen": return cachedDiscovery(discoveryCacheKey(providerType, executablePath)+ngenDiscoveryEnvKey(), func() ([]Model,error) { return discoverNGENModels(ctx, executablePath) })`.
- `ngenDiscoveryEnvKey()` returns a sanitized discovery suffix derived only from daemon-owned NGEN config identity, for example `":ngen_config="+sha256(path+"\n"+content)` when `NGEN_CONFIG` is set, or `":ngen_config=default"` when unset. Do not include raw paths containing secrets, API keys, or full config content in logs or cache keys.
- Implement `discoverNGENModels` by running `ngen models --json` with daemon process env, including daemon-owned `NGEN_CONFIG` when present. Do not read per-agent custom env in MVP because current `ListModels(ctx, providerType, executablePath)` has no custom env/workdir parameter.
- `ModelSelectionSupported("ngen")` returns false for MVP. `discoverNGENModels` is still used for observability/catalog display and config/execution consistency checks, but Multica UI/API must not expose a per-run model selector for provider=`ngen`.
- If future Multica allows per-agent `NGEN_CONFIG` or provider env to affect model list, change `ListModels` signature or add a NGEN-specific discovery path that accepts env/config/workdir and includes a sanitized fingerprint in the cache key. Without that change, per-agent custom config must not affect NGEN discovery.

Modify `server/pkg/agent/version.go`:

- Add `MinVersions["ngen"]` only after NGEN publishes a stable semver. Suggested initial gate: `"0.1.0"` or first version containing `ngen-stream-json v1`.

Modify `server/pkg/agent/thinking.go`:

- Keep NGEN absent from external thinking-level selection and accept only empty `ThinkingLevel` for provider=`ngen`.
- If `ngen models --json` reports config-derived thinking metadata such as GPT `xhigh` or Claude `max`, expose it only as read-only model/config metadata, not as selectable Multica `ThinkingLevel`.

### 6.3 Daemon config

Modify:

- `server/internal/daemon/config.go`
- `server/internal/daemon/daemon.go`
- tests under `server/internal/daemon/*_test.go`

Add probe:

```go
if e, ok := probe("MULTICA_NGEN_PATH", "ngen", ""); ok {
    agents["ngen"] = e
}
```

Do not add `MULTICA_NGEN_MODEL`; NGEN model selection is daemon-owned NGEN config, not Multica per-agent or per-run config.

Update missing-agent error text to include `ngen`.

Update `defaultAgentCommandNames`:

```go
"claude", "codex", "opencode", "openclaw", "ngen", "hermes", ...
```

Optional daemon args:

```go
type Config struct {
    ...
    NgenArgs []string
}
```

```go
ngenArgs, err := shellArgsFromEnv("MULTICA_NGEN_ARGS")
```

```go
func defaultArgsForProvider(cfg Config, provider string) []string {
    switch provider {
    case "ngen":
        return append([]string(nil), cfg.NgenArgs...)
    ...
    }
}
```

Recommendation: do not use `MULTICA_NGEN_ARGS` for config-critical values like `--config`, `--workdir`, `--model`, `--thinking-level`, `--provider-mode`, `--permission-mode`, or protocol format. Prefer daemon env/config so model discovery and execution stay aligned.

Environment:

- MVP decision: `NGEN_CONFIG` is daemon-owned. Add `NGEN_CONFIG` to blocked custom env so per-agent custom env cannot make execution use a different config than `models --json`.
- Permission mode is also daemon/admin-owned or NGEN default-owned. Multica MVP does not expose per-run permission mode; NGEN default is `yolo`.
- If users must provide NGEN API keys or provider base URLs through env, allow those specific provider env vars; continue blocking `MULTICA_*`, `HOME`, `PATH`, `USER`, `SHELL`, `TERM`.
- If a future version allows `NGEN_CONFIG` from user env, Multica must first pass the same env to discovery and execution and update discovery cache keys; until then, block it.

### 6.4 Exec environment integration

Modify:

- `server/internal/daemon/execenv/runtime_config.go`
- `server/internal/daemon/execenv/context.go`
- execenv tests.

Runtime brief:

```go
case "codex", "copilot", "opencode", "openclaw", "hermes", "pi", "cursor", "kimi", "kiro", "antigravity", "ngen":
    return filepath.Join(workDir, "AGENTS.md")
```

Skills path:

```go
case "ngen":
    return filepath.Join(workDir, "skills")
```

Brief content requirement:

- Mention Multica owns scheduling and workspace setup.
- Mention NGEN owns `.ngen/` task artifacts.
- Do not instruct NGEN to bypass its criteria/verifier/review gates.
- Do not put Multica issue state into `.ngen/` except via normal task objective/metadata.
- The brief must be consumed by NGEN through `<workDir>/AGENTS.md` ingestion in MVP; merely materializing the file is not sufficient acceptance.
- Update provider-specific skill brief generation, including `buildMetaSkillContent` or equivalent helper, so it says NGEN scans `<workDir>/skills` only after the NGEN-side ingestion feature exists. Avoid stale `.agent_context/skills` text for provider=`ngen`.
- Update cleanup/collision tests around managed skill directories so NGEN's `<workDir>/skills` path is included in sidecar cleanup and reuse rollback behavior.

### 6.5 Multica tests

Backend tests:

- `processOutput` maps `assistant/status/log/result` to `Message` and `Result`.
- `result` final line closes result and ignores/flags any later output as protocol error.
- `session_id` fallback to `task_id`.
- `usage` converts cache tokens and omits unknown.
- stable `tool_use/tool_result` call IDs map correctly.
- invalid JSON stdout is ignored only if non-protocol noise is explicitly allowed; preferred behavior is protocol error for non-empty invalid stdout.
- stderr tail is attached to start/exit errors.

Args/env tests:

- custom args and daemon-wide `ExtraArgs` cannot override `--output-format`, `--input-format`, `--workdir`, `--resume`, `--model`, `--thinking-level`, `--config`, `--timeout-seconds`, or `--permission-mode`.
- custom args and daemon-wide `ExtraArgs` cannot override `--config-scope` or `--ignore-workdir-provider-config`.
- `opts.Model` and `opts.ThinkingLevel` are not appended for provider=`ngen`; if non-empty, they are ignored/logged or rejected by config validation according to Multica's existing UX conventions.
- zero `opts.Timeout` does not append `--timeout-seconds`; positive timeout appends integer seconds.
- `ResumeSessionID` becomes `--resume TASK-ID`.
- `SystemPrompt` is sent in stdin envelope, not shell args.
- `McpConfig` is ignored/logged unless NGEN gains native MCP config support.
- per-agent custom env cannot override daemon-owned `NGEN_CONFIG`.

Discovery tests:

- fake `ngen models --json` returns config-derived model identities and labels.
- `ListModels("ngen")` caches by executable path.
- daemon-owned `NGEN_CONFIG` affects both fake `ngen models --json` and fake `ngen exec`; per-agent custom env cannot change only exec config.
- when daemon-owned `NGEN_CONFIG` is unset, fake `ngen exec` receives `--config-scope daemon` and must not derive provider catalog from workdir `ngen.json`.
- `ModelSelectionSupported("ngen")` is false, while model catalog remains available as read-only discovery metadata.
- `DetectVersion` parses `ngen --version`; `CheckMinVersion("ngen", ...)` works once min version is set.

Daemon/execenv tests:

- `MULTICA_NGEN_PATH` probe populates agents map; no `MULTICA_NGEN_MODEL` is introduced.
- login shell fallback includes `ngen`.
- `defaultArgsForProvider("ngen")` returns copied args if enabled.
- `runtimeConfigPath(provider="ngen")` is `AGENTS.md`.
- `skillsDirPath(provider="ngen")` is `<workDir>/skills`.
- fake end-to-end backend script receives expected args/stdin, emits intermediate status/log before final `result`, and parser exposes streaming progress.
- fake `blocked/needs_input` backend output persists first-class blocked state through data model/API and exposes UI resume/continue action.

## 7. 避坑清单

从 go-cli-agent 接入经验继承以下硬规则：

- stdout 只放协议 NDJSON；stderr 只放人类诊断。
- final result 必须最后输出。
- tool call/result 必须有稳定 call ID；没有稳定 ID 时降级为 log/status。
- `<binary> --version` 必须可用，且不依赖 workspace/API key。
- `models --json` 必须可用，且和 execution 读取同一份 config。
- model identity 必须是 config-derived route key，不只是 provider-native model 名；Multica 不把该值作为 per-run `--model` 传入。
- CustomArgs/ExtraArgs 不能覆盖协议关键 flag、config/workdir/resume/model identity、thinking level 或 permission mode。
- `Result.SessionID` 和 resume 参数必须语义一致；本方案固定为 NGEN `task_id`。
- unknown usage 不能写 0；nil/omit 才表示未知。
- skills 注入到 runtime 实际扫描的 workspace skills 目录；NGEN 是 `<workDir>/skills`。
- cancellation 第一版用 context/process，不设计未验证的 stdin control protocol。
- 不引用旧 Multica commit 或旧 stderr tail API；每次开发前以当前 upstream 文件签名为准。

## 8. 前后一致性检查

开发完成后逐项 gate：

- `ngen exec` 和 `ngen models --json` 的 config source 完全一致。
- MVP 中 `NGEN_CONFIG` 由 daemon 环境拥有，per-agent custom env 不能覆盖；model discovery cache key 包含 NGEN config fingerprint；没有 daemon-owned `NGEN_CONFIG` 时，NGEN exec 不允许用 workdir `ngen.json` 改变 provider catalog。
- `openai-response/gpt-5.5` 等 config-derived model identity 可逆拆分。
- Multica `agent.model` / `opts.Model` 不能改变 NGEN execution；provider=`ngen` 的 effective model 只来自 daemon-owned NGEN config。
- first run、resume、final handoff 中的 `model_route/provider_mode/provider_model` 都来自 durable `multica/run_metadata.json`，且保持一致；config/model drift fail closed，不允许显式切换。
- NGEN stdout 每一行都是合法 JSON；stderr 中没有 protocol JSON。
- final result 是最后一行；无 result 的成功退出视为协议失败。
- `Result.SessionID` 等于 NGEN `task_id`；resume 也只接受该 task id。
- Multica 不读取 `.ngen/` 内部文件做状态判定。
- NGEN handoff 中所有 artifact ref 都是 ref/summary，不包含 raw provider payload、API key、完整 hidden prompt。
- role/run_role 只作为 metadata，不污染 transcript `message.role`。
- worker trust 只来自 `WorkerResult/WorkerContract` trust fields，不来自 child prose。
- mission validation 只通过 `MissionStatusSnapshot`/validation refs 投影，不复制整个 mission graph 到 Multica 权威状态。
- 用户 custom env/args 不能改变 protocol format、workdir、resume id、model identity、thinking level、permission mode、config ownership。
- workspace shared skills 只从 `<workDir>/skills` 注入；不默认扫描全局私有 skills。
- `<workDir>/AGENTS.md` 必须被 NGEN 消费并纳入 session/system context。

## 9. 开发顺序

Phase A：NGEN protocol MVP

1. 重构 CLI pre-dispatch，让 `--version` 绕过 config，让 `models/exec --workdir --config` 在 service 构造前解析。
2. 添加 `--version` 和 `version --json`。
3. 添加 `models --json`，输出 daemon config-derived model catalog。
4. 添加 `internal/multica` stream structs、usage parser、encoder。
5. 添加 `ngen exec` headless command：stdin envelope -> create/resume task -> hybrid event sink/bus + durable high-water flush -> `Run/Resume/Auto` -> final flush -> final result。
6. 添加 projection tests 和 CLI smoke tests。

Phase B：Multica backend MVP

1. 新增 `server/pkg/agent/ngen.go` backend 和 parser tests。
2. 注册 `ngen` factory、launch header、models discovery、version gate。
3. 添加 daemon config probe 和 optional `MULTICA_NGEN_ARGS`。
4. 确认 daemon-owned `NGEN_CONFIG` discovery/execution 一致，并阻止 per-agent custom env 覆盖。
5. 添加 execenv `AGENTS.md` 与 `<workDir>/skills` support。
6. 添加 fake-ngen backend E2E，覆盖 completed、first-class blocked/needs_input、resume 和 streaming progress。

Phase C：multi-agent hardening

1. 扩展 NGEN handoff projection，覆盖 worker settlement/reconcile/trust 字段。
2. 对 NGEN worker handoff 增加 richer parent-action display，但仍不让 Multica 解析 `.ngen/` 为权威。
3. 视需要新增 Multica runtime profile：`ngen-large-project`，只改变 prompt/skills/profile，不改变 protocol。

Phase D：acceptance

1. 本地 fake NGEN E2E 通过。
2. NGEN builtin/default 无 API key smoke 通过。
3. NGEN openai-response/anthropic live smoke 在有 key 环境下通过。
4. resume smoke 证明 Multica session id 可恢复同一 NGEN task。
5. blocked smoke 证明 `result.status=blocked` / `needs_input`、`SessionID=task_id`、worker `requires_parent_action` 都被 first-class 保存并在 UI/API 提供 resume/continue。
6. streaming smoke 证明 final result 前已有 status/log/text 进度，模拟 late durable event 后 final flush 不漏关键事件。
7. worker smoke 证明 child result trust fields 出现在 final handoff。

## 10. 验收命令建议

NGEN 仓库：

```text
go test ./internal/multica ./internal/app ./internal/task ./internal/runtime
go run ./cmd/ngen --version
go run ./cmd/ngen models --json --workdir /tmp/ngen-smoke
printf '%s\n' '{"protocol":"ngen-stream-json","protocol_version":1,"type":"user","role":"user","content":[{"type":"text","text":"create a tiny handoff"}]}' | go run ./cmd/ngen exec --output-format stream-json --input-format stream-json --workdir /tmp/ngen-smoke --config-scope daemon
```

Multica 仓库：

```text
go test ./server/pkg/agent -run 'TestNGEN|TestListModels|TestModelSelection|TestCheckMinVersion'
go test ./server/internal/daemon -run 'TestLoadConfig|TestDefaultArgsForProvider'
go test ./server/internal/daemon/execenv -run 'TestRuntimeConfigPath|TestSkillsDirPath'
go test ./server/internal/daemon -run TestAgentFakeNGENE2E
```

Manual smoke:

```text
NGEN_CONFIG=/path/to/ngen-daemon.json MULTICA_NGEN_PATH=/path/to/ngen multica daemon
```

Expected:

- model list endpoint shows NGEN config-derived model identities as read-only catalog metadata。
- first run returns `Result.SessionID=<ngen task_id>`。
- resume uses the same task id and NGEN continues from `.ngen/` artifacts。
- blocked run preserves `Result.SessionID=<ngen task_id>` and a structured handoff, API/UI exposes first-class blocked/needs_input with resume/continue action, and next resume uses the same task id。
- final output contains concise handoff summary plus artifact refs。

## 11. 已决策项与后续非 MVP

- Headless command 已决策为 `ngen exec`。
- External `--thinking-level` 已决策为不支持；thinking/reasoning level 只来自 daemon-owned NGEN config，例如 GPT `xhigh` 或 Claude `max`。
- `blocked/needs_input` 已决策为 Multica MVP first-class 状态，必须覆盖数据模型、API、UI 和 resume/continue 入口。
- `NGEN_CONFIG` 已决策为 daemon-owned；per-agent custom config 是后续非 MVP，必须先扩展 model discovery env/config/workdir 通道和 cache key。
- Per-run model switch 已决策为不支持；effective model 只来自 daemon-owned NGEN config，resume 时 config/model drift fail closed。
- Per-run permission mode 已决策为不支持；permission 只来自 daemon/admin 配置或 NGEN 默认，默认 `yolo`。
- Hybrid bounded-near-real-time streaming 已决策为 MVP 要求；纯 batch 不满足 MVP。

## 12. 最小可交付定义

最小可交付不是“Multica 能启动 ngen 进程”，而是同时满足：

- `ngen --version`、`ngen models --json`、`ngen exec ...` 三个接口可被 Multica 稳定调用。
- Multica 能创建 NGEN task、接收 hybrid bounded-near-real-time status/log/text、拿到 durable final-flushed result；如果 NGEN 只能 batch 输出，不能宣称满足本 MVP。
- `Result.SessionID` 可用于下一次 resume，且恢复同一个 NGEN `task_id`。
- blocked/approval/input wait 不丢 session id、handoff 或 parent action，且作为 Multica first-class `blocked/needs_input` 状态在数据模型、API、UI 中可见并可 resume/continue，不会被误标为 completed 或普通失败。
- config-derived model identity 在 first run、resume 和 final handoff 中一致；config/model drift 有明确 fail-closed 语义。
- stdout/stderr、usage、config-derived model identity、custom args、skills path、AGENTS.md ingestion 都通过测试。
- NGEN worker/evidence/handoff 保持 artifact-first；Multica 只消费投影，不复制权威状态。
