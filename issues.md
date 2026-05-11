# go-cli-agent 三轮子功能 Review 问题登记

更新时间：2026-05-11

本文是本轮 master/sub-agent review 的问题登记，不是修复记录。审计边界按 `AGENTS.md` 与当前 spec：core v1 默认主路径仍是 `init/run/exec/steer/continue/sessions/tasks/probe-provider/doctor`；`delegate` / `children` / `queue` / `tui` / `web` 只作为显式 `experimental` 或扩展入口评估。结论只记录有源码或 spec 证据的问题；未执行真实 provider live matrix，也不把 `./test.sh` 绿色当成覆盖全部风险。

当前文档口径：项目默认运行在可信本地工作环境中，不把报告、prompt、session、compaction 或 provider view 脱敏作为 runtime / spec 的硬编码规范。如果某次任务需要脱敏，必须由用户在当轮 prompt 明确提出，并作为该任务交付物的内容要求处理。

## Review 循环状态

| 循环 | 状态 | 覆盖范围 | 本轮新增 |
|---|---|---|---|
| Round 1 | Completed | runtime/session、tools/security/skills/hooks/config、provider、CLI/SDK/output/TUI、WebConsole/isolation、docs/tests traceability | 24 |
| Round 2 | Completed | 针对 Round 1 遗漏面与高风险边界复审：runtime/session/provider、security/tool/config/trust、WebConsole/API/background、docs/tests traceability | 17 |
| Round 3 | Completed | 最终交叉复审、去重、代表性证据复核，并补查 workspace root / runtime context loader 边界 | 2 |

## Round 1 Findings

### R1-01 High: `write_file` / `edit_file` 的可预测临时文件可能越过 workspace 写入

- 证据：`write_file` 先检查输入与最终 resolved path，然后调用 `writeAtomically(path, ...)`：`internal/tools/registry.go:516`、`internal/tools/registry.go:519`、`internal/tools/registry.go:526`。`edit_file` 复用同一路径：`internal/tools/registry.go:573`、`internal/tools/registry.go:576`、`internal/tools/registry.go:592`。`writeAtomically` 使用 `tmp := path + ".tmp"`，再直接 `os.WriteFile(tmp, ...)` 和 `os.Rename(tmp, path)`：`internal/tools/registry.go:2142`、`internal/tools/registry.go:2143`、`internal/tools/registry.go:2149`。
- 影响：最终目标受 workspace containment 校验，但临时路径没有单独校验，也没有防 symlink open。恶意 workspace 可预置 `<target>.tmp` symlink 指向 workspace 外的用户可写文件，使 `os.WriteFile` 跟随 symlink 写出边界；目标为 workspace root / `.` 时还可能在 workspace 邻接位置尝试创建临时文件。
- 建议：拒绝空路径、`.`、目录目标；用 checked parent 内的随机临时文件和非跟随/独占打开语义；rename 前重新验证 canonical parent containment，失败时清理临时文件。

### R1-02 High: workspace `.go-cli-agent/config.yaml` 可无 trust gate 改写 active runtime/provider config 并启用 command hooks

- 证据：默认 config load order 会读取用户配置后再读取 `cwd/.go-cli-agent/config.yaml`：`internal/config/config.go:349`、`internal/config/config.go:353`、`internal/config/config.go:355`、`internal/config/config.go:367`、`internal/config/config.go:374`。runtime 在 session start 触发 hooks：`internal/runtime/engine.go:69`。Hook manager 从 config 构造 command hook 并在 workdir 下执行：`internal/hooks/manager.go:49`、`internal/hooks/manager.go:160`、`internal/hooks/manager.go:161`。
- 追加证据：provider config 可控制 `api_provider`、`api_key_env`、`base_url`、`model`：`internal/config/config.go:35` 到 `internal/config/config.go:39`。runtime 从 config 解析 provider/model：`internal/runtime/runner.go:383` 到 `internal/runtime/runner.go:400`，并用 `cfg.BaseURL` 与 `cfg.ResolvedAPIKey()` 实例化 adapter：`internal/runtime/runner.go:928` 到 `internal/runtime/runner.go:932`。
- 影响：未受信 workspace 可以通过仓库内 `.go-cli-agent/config.yaml` 配置 `session_start`、`tool.before`、`tool.after` 等 command hooks，普通 `run` / `exec` 一启动就可能执行仓库控制命令，早于模型的显式工具决策。它还可以把 prompts、代码上下文和 tool results 路由到仓库指定的 OpenAI-compatible / Anthropic-compatible endpoint，风险不只是 hook 执行。
- 建议：区分 passive workspace config 与 active/executable config；workspace command hooks、provider endpoint/API-provider/API-key-env、session-dir、skills-dir、runtime executable knobs 默认禁用或需显式 trust。`doctor` warning 只能补充提示，不能替代运行时 gate。

### R1-03 High: WebConsole mutating REST API 缺少 Origin / Content-Type / CSRF guard

- 证据：所有 `/api/` 请求直接进入 `serveAPI`：`internal/webconsole/service.go:254`。JSON decode helper 不校验 `Content-Type`、`Origin`、`Referer`、CSRF token 或本地自定义 header：`internal/webconsole/service.go:2767`。Web 启动命令已承认非 loopback 暴露时可写 config / `.env` key、删除 session、管理 skill、读取 workspace 文件：`internal/app/web_cmd.go:62`；spec 也要求因写能力而提示非 loopback 风险：`spec/17-web-console.md:316`。
- 影响：本机浏览器访问恶意网页时，网页可向 `127.0.0.1:3940` 或 LAN 绑定的 WebConsole 发起跨站简单 POST。即使读不到响应，也可能触发修改 provider config/base URL、删除 session、管理 skill 等副作用。
- 建议：对所有 mutating endpoint 增加轻量 local-console CSRF guard：JSON API 要求 `Content-Type: application/json`，拒绝 foreign/missing `Origin` 的 unsafe method，或在 shell 下发 nonce 并要求自定义 header；不需要引入 RBAC/SaaS 级复杂度。

### R1-04 High: isolation root containment 只做 lexical 检查，symlink root 可把 child workdir 放回源树

- 证据：`Prepare` 对 `RootDir` 只 `filepath.Abs`，未 resolve symlink：`internal/isolation/prepare.go:50`。随后 `target := filepath.Join(rootDir, req.SessionID)`：`internal/isolation/prepare.go:54`，用 lexical `isWithin(parentWorkdir, target)` 判断是否在源目录内：`internal/isolation/prepare.go:55`，最后 `os.MkdirAll` / `WalkDir` 写入目标：`internal/isolation/prepare.go:119`。spec 要求 isolation root 必须在 source workdir 外并拒绝内部 root：`spec/14-multi-agent-and-isolation.md:191`。
- 影响：如果 `isolation_root` 是指向 `<parentWorkdir>/.go-cli-agent/_worktrees` 的 symlink，lexical path 看似在 `/tmp/root-link/<session>`，实际文件操作落在 parent workspace 内。copy mode 会污染源树，甚至因为目标先建在源树下而产生递归复制风险。
- 建议：对 parent workdir、已存在 root、最近存在的 target parent 做 symlink resolution，再做 containment；resolved root/target 落在 resolved parent 内时拒绝；增加 symlinked isolation root 回归。

### R1-05 Closed: compaction 默认脱敏已从规范和 runtime 移除

- 原问题：compaction 曾在用户没有提出脱敏要求时静默改写 provider view、summary 和 transcript，和“compaction 只改变上下文规模视图，不改写内容语义”的产品边界冲突。
- 当前证据：`BuildWithProfile` 使用 `sourceMessages := cloneMessages(messages)`，并以 source messages 生成 summary、artifact memory、transcript 与 unresolved issue 摘要：`internal/runtime/compaction.go:38` 到 `internal/runtime/compaction.go:89`、`internal/runtime/compaction.go:119` 到 `internal/runtime/compaction.go:158`。`compactTextForContext` 只做 head/tail 裁剪和长度记录，不做 secret-like pattern 替换：`internal/runtime/compaction.go:328` 到 `internal/runtime/compaction.go:336`。回归测试明确断言 secret-like 文本会保留，且 compacted view、summary、transcript 不出现 `[REDACTED]` marker：`internal/runtime/compaction_test.go:337` 到 `internal/runtime/compaction_test.go:420`。当前 spec 也明确 compaction 不默认做报告、prompt、session 或 provider view 脱敏：`spec/10-context-compaction.md:147` 到 `spec/10-context-compaction.md:156`。
- 后续要求：不要重新引入 runtime / compactor 级默认 redaction。若用户 prompt 明确要求脱敏，应由模型按该 prompt 生成脱敏版报告或指定 artifact，而不是由 runtime 按关键词硬编码改写所有任务。

### R1-06 Medium-High: workspace skills 的 command tools 在未 `load_skill` / 未 trust 前自动注册

- 证据：默认 skill dir 是 `./skills`：`internal/config/config.go:285`、`internal/config/config.go:286`。Catalog scan 会加载 skill metadata 时加载 `tools/*.yaml`：`internal/skills/catalog.go:45`、`internal/skills/catalog.go:178`、`internal/skills/catalog.go:191`、`internal/skills/catalog.go:215`。Registry 创建时遍历全部 `catalog.CommandTools()` 并注册 executable tool：`internal/tools/registry.go:123`、`internal/tools/registry.go:128`。执行时直接运行命令，workdir 为 skill 目录：`internal/tools/registry.go:1880`、`internal/tools/registry.go:1881`。
- 影响：spec 的 skill 模型是摘要先可见、正文按需 `load_skill`，但 workspace skill command tools 会立即进入模型 action space。未受信仓库只要提交 `skills/<name>/tools/*.yaml`，就能向模型暴露任意本地命令能力，且不走 `.agent` trust 边界或显式 skill-load 步骤。
- 建议：区分 trusted global skill dirs 与 workspace skill dirs。workspace skill 默认只暴露摘要；显式 trust 或显式 `load_skill` 后才注册该 skill 的 command tools。重复 command tool name 也应拒绝而不是后注册覆盖。

### R1-07 Medium: explicit `runtime.shell.sandbox: bwrap` 当前策略在 bwrap 不可用时 fail open

- 证据：Linux shell sandbox 构造函数只在 sandbox 为 `bwrap` 时查找 binary：`internal/tools/shell_sandbox_linux.go:11`、`internal/tools/shell_sandbox_linux.go:12`、`internal/tools/shell_sandbox_linux.go:15`；查找失败时返回普通 shell command 和 `bwrap_unavailable` 状态：`internal/tools/shell_sandbox_linux.go:17`。Registry 接收该 command 并继续执行：`internal/tools/registry.go:326`、`internal/tools/registry.go:356`、`internal/tools/registry.go:357`、`internal/tools/registry.go:358`。
- 追加证据：`spec/18` 当前明确允许 unavailable 时记录 fallback metadata 并保持默认 shell 行为：`spec/18-durable-contract-and-completion.md:174` 到 `spec/18-durable-contract-and-completion.md:182`。
- 影响：这不是“代码偏离当前 spec”，而是当前 spec 与实现共同承认了对显式 sandbox 请求的 fail-open 策略。operator 显式配置 bwrap 的安全预期通常是“被 sandbox 或拒绝”，但实际是 unsandboxed 执行，只在 metadata 里记录 `bwrap_unavailable`。
- 建议：把安全策略改为显式 sandbox mode 不可用时 fail closed；如需开发便利，增加单独 `sandbox_fallback: true`；`doctor` 对请求 sandbox 但不可用应给 hard failure，并同步更新 `spec/18`。

### R1-08 Medium: `exec_policy=deny` 漏掉无空格 secret-file redirect

- 证据：`secretPathWritePattern` 要求 `>` / `>>` / `tee` 后有 whitespace：`internal/tools/exec_policy.go:19`；检测和 deny 依赖该 regex：`internal/tools/exec_policy.go:23`、`internal/tools/registry.go:327`、`internal/tools/registry.go:328`、`internal/tools/registry.go:346`。现有测试覆盖 `echo token > .env`，未覆盖 `echo token >.env`：`internal/tools/registry_test.go:305`、`internal/tools/registry_test.go:346`。
- 影响：`echo token >.env`、`2>.env`、`1>>.env` 是合法 shell 语法，能写同一敏感文件，但当前 deny policy 不拦截，导致 secret-path write policy 可绕过。
- 建议：至少支持 optional whitespace、fd redirect、append redirect 等 shell 写法，并补充 no-space / fd redirect / heredoc / 常见 writer 的测试；长期应考虑 shell parser 或 filesystem-level write restriction。

### R1-09 Medium: config persistence 跟随 symlink

- 证据：`config.WriteFile` 只创建 parent dir，然后 `os.WriteFile(path, data, 0o600)`：`internal/config/config.go:606`、`internal/config/config.go:614`、`internal/config/config.go:617`。WebConsole Settings 通过该函数持久化配置：`internal/webconsole/service.go:1451`。
- 影响：如果默认 workspace config path 或用户可写 config path 处已有 symlink，Settings 保存会跟随 symlink 覆写目标文件。恶意 workspace 可让 `.go-cli-agent/config.yaml` 指向其他用户可写文件，形成配置层面的 symlink-write 边界问题。
- 建议：`Lstat` 拒绝 symlink target，用 checked parent 内临时文件写入后 rename；workspace-relative config persistence 还应 canonicalize parent 并结合 trust 策略。

### R1-10 Medium: `continue --provider` 不带 `--model` 时会把新 adapter 与旧 model 组合

- 证据：`Continue` 在 `req.Provider` 非空时更新 `meta.Provider` 并读取新 provider config：`internal/runtime/runner.go:425` 到 `internal/runtime/runner.go:430`，但只有 `req.Model` 非空才覆盖 provider config model 和 `meta.Model`：`internal/runtime/runner.go:431`、`internal/runtime/runner.go:436`。Engine provider call 直接使用 `meta.Model`：`internal/runtime/engine.go:173` 到 `internal/runtime/engine.go:175`。provider contract 要求 provider fields 通过 config -> runtime -> session metadata -> adapter 全链路传递：`spec/03-provider-contracts.md:57`。
- 影响：从 OpenAI/OpenAI-compatible session `continue --provider anthropic` 但不传 `--model` 时，session metadata 可变成 Anthropic provider + 旧 OpenAI model。请求可能被新 adapter 拒绝，诊断 metadata 也会误导。
- 建议：`Continue` provider/model resolution 复用 `Start` 的 effective-provider/effective-model 逻辑；provider 改变且未显式传 model 时，使用新 provider profile 的默认 model；增加跨 provider continue 回归。

### R1-11 Medium: provider-native replay blocks 只按 provider/model 限定，缺少 provider profile / effective API provider 作用域

- 证据：spec 要求 provider profile、effective API provider 或 model 改变时默认剥离旧 opaque reasoning continuation fact：`spec/03-provider-contracts.md:199`。Provider profile 与 adapter family 是不同概念：`spec/02-cli-and-config.md:342`。`ProviderContentBlock` 只有 `Provider`、`Type`、`Model` 等字段，没有 provider profile / effective API provider：`internal/session/types.go:257` 到 `internal/session/types.go:273`。OpenAI block 写入时固定 `Provider: "openai"` 并记录 `Model: req.Model`：`internal/provider/openai.go:141` 到 `internal/provider/openai.go:149`。Replay 过滤只检查 provider/type/id/data 和 model：`internal/provider/openai.go:326` 到 `internal/provider/openai.go:334`。
- 追加证据：Anthropic replay 也只检查 block provider 与 model，然后回放 thinking、redacted thinking、text、tool_use：`internal/provider/anthropic.go:277` 到 `internal/provider/anthropic.go:323`。Google replay 同样只检查 provider 与 model，然后回放 thought signature / function call：`internal/provider/google.go:311` 到 `internal/provider/google.go:351`。Anthropic-compatible contract 明确允许 Kimi/DeepSeek 等 custom profile 使用同一 adapter family：`spec/03-provider-contracts.md:256` 到 `spec/03-provider-contracts.md:260`。
- 影响：两个不同 provider profile 可以共用同一 adapter family 和相同 model，例如官方 OpenAI 与自定义 `openai-compatible` gateway，或多个 `anthropic-compatible` custom endpoint。当前逻辑可能把 profile A 的 provider-native continuation fact 发送到 profile B，违反 provider-owned replay 边界，也可能造成上游拒绝或 continuation state 泄漏。
- 建议：ProviderContentBlock 持久化 provider profile、effective api_provider，必要时保存非 secret endpoint identity/hash；当前 profile/API/model 不一致时 strip provider-native replay block；补充同 model 不同 profile 的 OpenAI / Anthropic-compatible / Google replay 回归。

### R1-12 Medium: Ralph auto-continue 迭代计数未持久化，`max_iterations` 可被绕过

- 证据：Engine 在 `incomplete_no_finish` 后触发 `AutoContinue`：`internal/runtime/engine.go:514` 到 `internal/runtime/engine.go:520`。`AutoContinue` 检查 `state.RalphLoopCount`：`internal/runtime/runner.go:745`，只在本地 `state` 上自增：`internal/runtime/runner.go:760`，随后调用 `Continue`：`internal/runtime/runner.go:765`。中间没有保存 state；`Continue` 会重新从磁盘加载旧 state。
- 影响：启用 Ralph loop 时，重复 no-finish failure 可能一直从旧计数恢复，达不到 `MaxIterations` 的 exhausted gate，把有界恢复机制变成无界 auto-continue。
- 建议：调用 `Continue` 前保存自增后的 `RalphLoopCount`，或把增量 state 传入低层 continue path；增加重复 no-finish provider 的 exhausted 回归。

### R1-13 Medium: synchronous child session 的非终态会被 parent coordination 记为 completed

- 证据：同步 delegation 启动 child：`internal/runtime/delegation.go:137` 到 `internal/runtime/delegation.go:150`，只要有 child session id 就 resolve parent child session：`internal/runtime/delegation.go:171`。`resolveParentChildSession` 从 unresolved 移除 child；除 `failed` 外全部加入 completed：`internal/runtime/parent_coordination.go:66` 到 `internal/runtime/parent_coordination.go:70`。但 `awaiting_input` 明确不是错误也不是完成：`spec/05-session-interrupt-resume.md:123` 到 `spec/05-session-interrupt-resume.md:131`，`continue` 可恢复 `paused` / `awaiting_input` / `failed`：`spec/05-session-interrupt-resume.md:141` 到 `spec/05-session-interrupt-resume.md:144`。
- 影响：若 `agent_spawn` 使用 `mode=run` 或 child 暂停/等待输入，parent coordination gate 可能被解除，允许 parent finish，尽管 child 还需要后续输入或恢复。
- 建议：只有 terminal status 才 resolve 为 completed/failed；`paused`、`awaiting_input`、`running` 保持 unresolved，并在 `agent_status` / parent summary 中显示 resumable 状态。

### R1-14 Medium: `continue` 重置 durable turn，provider raw sidecar 和 attempts turn 编号会碰撞

- 证据：`Continue` 恢复前重置 `state.Turn = 0`：`internal/runtime/runner.go:461`。Engine 用 `state.Turn` 写 provider raw sidecar 与 provider attempts：`internal/runtime/engine.go:236`、`internal/runtime/engine.go:241`。`SaveProviderRawSidecar` 写到 `provider-raw/<turn>.json`：`internal/session/store.go:281` 到 `internal/session/store.go:284`。现有测试只断言 continue 重置 turn budget，没有覆盖 raw sidecar 多次 continue 保留。
- 影响：启用 `provider_options.raw_sidecar=true` 时，resume 后第一轮会覆盖先前 `provider-raw/1.json`，丢失诊断 envelope；provider attempts 也会复用 turn 编号，降低审计可追踪性。
- 建议：拆分 per-run turn budget 与 monotonic durable turn/attempt sequence；或 provider raw sidecar path 加 run/resume generation，同时保持 hard-turn-limit budget 可重置。

### R1-15 Medium: WebConsole background workers 在 Settings 保存后继续使用 stale config

- 证据：Web service 启动时 clone startup config：`internal/webconsole/service.go:223`，并用它初始化 worker pool：`internal/webconsole/service.go:235`。`POST /api/config` 保存后只替换 `s.cfg`：`internal/webconsole/service.go:1455`。worker pool 保留原 config pointer：`internal/webconsole/service.go:2500`，worker runner 从 `p.cfg` 创建：`internal/webconsole/service.go:2550`。queue claimed job 的 child runner 又来自 worker runner config：`internal/runtime/delegation.go:338`。
- 影响：Settings UI 修改 provider profile、API Provider、base URL、model、reasoning mode 或 API key 后，foreground start/continue 使用新 snapshot，但 background queue worker 仍用启动时旧配置。UI 显示的保存状态与后台实际执行状态不一致。
- 建议：worker 每个 job 获取 fresh config snapshot，或保存 config 时在锁下更新 workerPool cfg 并安全重建 worker；增加保存配置后提交 queue job 的服务测试。

### R1-16 Medium: WebConsole control endpoint 对 provider/config 输入错误的结构化映射不一致

- 证据：Web spec 定义 REST 为 session-control authority，错误要稳定结构化，包括 `UNKNOWN_PROVIDER`：`spec/17-web-console.md:307`、`spec/17-web-console.md:310`。start handler 默认把 start error 映射为 `500`，只用少量字符串判断 client error：`internal/webconsole/service.go:843`、`internal/webconsole/service.go:854`。provider lookup error 来自 `resolveProviderAndModel` / `unknown provider`：`internal/runtime/runner.go:392`、`internal/config/config.go:527`。continue endpoint 返回 `202` 后才在 goroutine 内失败 provider override：`internal/webconsole/service.go:924`、`internal/runtime/runner.go:425`。
- 影响：未知 provider 的 start 可能表现为 server failure；queue/start/continue 的 provider validation 不一致；continue 甚至可能先返回 accepted，再异步失败，让 UI/API 误以为 resume 已启动。
- 建议：集中处理 `runtime.ConfigError` 和 provider lookup failure；start/continue/queue 返回 `202` 前预校验 provider/model/api-provider override；统一返回 `400` + `code: UNKNOWN_PROVIDER` + detail/action。

### R1-17 Medium: Settings save/test 无法区分 omitted 与显式 empty/default provider 字段

- 证据：Settings DTO 对 `api_provider`、`base_url`、`model` 使用非 pointer string：`internal/webconsole/dto.go:52`。UI 提供 API Provider 的 `Provider default` 空值，也提供可清空的 Base URL / Model 输入，并发送表单值：`internal/webconsole/assets/settings-view.js:66`、`internal/webconsole/assets/settings-view.js:68`、`internal/webconsole/assets/settings-view.js:76`、`internal/webconsole/assets/settings-view.js:188`、`internal/webconsole/assets/settings-view.js:211`、`internal/webconsole/assets/api.js:118`。Backend save 只在非空时更新 Base URL / Model / API Provider：`internal/webconsole/service.go:1400` 到 `internal/webconsole/service.go:1407`；config test 重复同样语义：`internal/webconsole/service.go:1505` 到 `internal/webconsole/service.go:1512`。Web spec 要求 config test 使用当前 Settings 表单值，config save 持久化 base URL/model/API Provider：`spec/17-web-console.md:319`、`spec/17-web-console.md:323`。
- 影响：一旦 provider profile 已有显式 `api_provider`、`base_url` 或 `model`，UI 清空或选择 default 看似成功，实际无法清除旧值；config test 也无法测试 intended cleared/default state，后续 start/queue/continue 仍按旧字段执行。
- 建议：保存请求区分字段存在与空字符串，或定义显式 `default` / `clear` sentinel；save 与 test endpoint 使用同一语义。对于不可为空的 model/base URL，应明确拒绝并返回结构化错误，而不是静默保留旧值。

### R1-18 Medium: WebConsole worker scaling 接受无上限 desired count

- 证据：worker scale endpoint 只拒绝负数：`internal/webconsole/service.go:1098`，随即 apply requested count：`internal/webconsole/service.go:1102`。worker pool 循环创建直到达到 desired：`internal/webconsole/service.go:2520`，每个 worker 建 context/goroutine 和 runtime runner：`internal/webconsole/service.go:2547`、`internal/webconsole/service.go:2550`。spec 把 worker scaling 定位为 advanced/test API：`spec/17-web-console.md:493`。
- 影响：恶意或错误的本地/LAN 请求可设置极大 worker count，导致进程创建大量 goroutine/runner，占用资源。`run.sh` 为 WSL 可能监听 `0.0.0.0`，因此这是 experimental Web 的实际可用性风险。
- 建议：增加保守最大 worker count（配置或常量），超出返回 `400`，并在 `/api/workers` 或 `/api/meta` 暴露 max 供前端遵守。

### R1-19 Medium: 扩展命令文档仍混用 retired top-level command name，且 `experimental web` 未进入 central CLI spec 列表

- 证据：root usage 只展示 core 命令：`internal/app/app.go:90` 到 `internal/app/app.go:92`，experimental usage 才展示 `delegate|children|queue|tui|web`：`internal/app/app.go:95` 到 `internal/app/app.go:97`，router 也只在 `experimental` 下处理扩展命令：`internal/app/app.go:100` 到 `internal/app/app.go:114`。但 `spec/14` 仍写 `go-cli-agent delegate` / `children`：`spec/14-multi-agent-and-isolation.md:88` 到 `spec/14-multi-agent-and-isolation.md:89`，`spec/15` 仍写 `go-cli-agent queue ...`：`spec/15-background-queue.md:74` 到 `spec/15-background-queue.md:77`，`spec/16` 仍写 `go-cli-agent tui`：`spec/16-terminal-tui.md:20`。`spec/02` extension list 包含 delegate/children/queue/tui，但漏掉 code/README 已有的 `experimental web`：`spec/02-cli-and-config.md:37` 到 `spec/02-cli-and-config.md:40`。
- 影响：按 extension spec 操作会调用当前代码明确拒绝的 top-level 命令；central CLI spec 也无法完整追踪当前 sanctioned experimental surfaces，容易让后续验收或文档更新重新把扩展面推回 root。
- 建议：`spec/14`、`spec/15`、`spec/16` 统一改为 `go-cli-agent experimental ...`；`spec/02` 增加 `experimental web` 并交叉引用 `spec/17`；可加文档/CLI traceability test。
- 追加证据：README 当前定位 bullet 写 ``experimental delegate|children|queue|web`、`tui``，没有把 `tui` 放进同一个 `experimental` command surface：`README.md:17`；实际 help 是 `experimental <delegate|children|queue|tui|web>`：`internal/app/app.go:96`。

### R1-20 Medium: multi-agent 默认暴露在 spec 内部自相矛盾

- 证据：`spec/02` 写 `runtime.multi_agent.enabled` 默认 `true`，默认暴露 `agent_spawn` / `agent_status` / `agent_list`：`spec/02-cli-and-config.md:331` 到 `spec/02-cli-and-config.md:333`。`spec/11` 也说明这些工具默认暴露，可设置 false 收窄：`spec/11-spec-audit-and-traceability.md:218` 到 `spec/11-spec-audit-and-traceability.md:220`。代码和示例配置同意默认 true：`internal/config/config.go:302` 到 `internal/config/config.go:304`、`config/config.example.yaml:126` 到 `config/config.example.yaml:130`，registry 按 enabled 注册 agent tools：`internal/tools/registry.go:186` 到 `internal/tools/registry.go:191`。但 `spec/14` depth limit 小节写默认 `enabled = false, max_depth = 4`：`spec/14-multi-agent-and-isolation.md:258`。
- 影响：默认是否给模型 child-agent tool 是安全与自主性边界。spec 冲突会导致 operator 预期、验收标准、安全 review 结论互相打架。
- 建议：明确当前决策。如果默认-on 是锁定策略，则修正 `spec/14`；如果要默认-off，则同步改 `config.Default()`、config example、registry tests、README/spec。

### R1-21 Medium: provider contract 测试未覆盖 spec 要求的每 adapter non-2xx / cancel 失败路径

- 证据：provider spec 要求每个 adapter 覆盖 text parsing、tool calls、headers/endpoints、replay、generation mapping、stop reasons、non-2xx classification、cancel propagation：`spec/03-provider-contracts.md:355` 到 `spec/03-provider-contracts.md:366`。测试策略也说 OpenAI、Anthropic、Google provider contract tests 使用 `httptest.Server` 覆盖 error mapping：`spec/07-testing-strategy.md:36` 到 `spec/07-testing-strategy.md:48`。当前 `internal/provider/provider_test.go:463` 到 `internal/provider/provider_test.go:510` 的 HTTP/parse classification 偏 OpenAI；Anthropic 与 Google 的主要测试覆盖序列化/replay/tool parsing：`internal/provider/provider_test.go:185` 到 `internal/provider/provider_test.go:243`、`internal/provider/provider_test.go:327` 到 `internal/provider/provider_test.go:382`；timeout classification 在 shared `JSONClient` 层：`internal/provider/provider_test.go:683` 到 `internal/provider/provider_test.go:746`。
- 影响：三家 adapter 是 core v1 目标。shared client 测试不能证明各 adapter 的 provider identity、endpoint/header、cancel/error semantics 在自身 request path 失败时仍正确。Anthropic/Google 的 failure path 可回归而默认测试仍绿。
- 建议：增加 table-driven adapter contract suite，分别覆盖 OpenAI / Anthropic / Google 的 non-2xx 与 context cancel，断言 provider name/class/status 与 cancellation 分类。

### R1-22 Medium: `init` 交互 prompt 绕过 app stdout/stderr adapter

- 证据：`app.Run` 接收注入的 stdout/stderr：`internal/app/app.go:54`，`runInit` 也接收这两个 writer：`internal/app/app.go:1099`。但 prompt helper 使用 process-global `fmt.Printf`：`internal/app/app.go:1416`、`internal/app/app.go:1417`；interactive init 多处调用该 helper：`internal/app/app.go:1122`、`internal/app/app.go:1126`、`internal/app/app.go:1129`、`internal/app/app.go:1132`、`internal/app/app.go:1135`、`internal/app/app.go:1138`、`internal/app/app.go:1139`。README 把 `internal/app` 定义为 stdout/stderr adapter：`README.md:183`。
- 影响：core `init` 是默认主路径。prompt text 逃逸注入 writer，会降低 `app.Run` 的可测试性、嵌入性和 wrapper 控制能力，削弱 CLI adapter 边界。
- 建议：`prompt` 接收 `io.Writer`，`runInit` 用传入 stdout/stderr 输出 prompt；增加注入 buffer 的 interactive prompt 测试。

### R1-23 Low-Medium: `read_file` internal artifact guard 可被 symlinked `.artifacts` alias 绕过

- 证据：`read_file` 先 resolve path：`internal/tools/registry.go:433`，再检查 `isInternalGeneratedArtifactPath(execCtx.Workdir, path)`：`internal/tools/registry.go:437`。该函数从 resolved path 计算相对路径并只扫描 canonical rel components 中是否有 `.artifacts`：`internal/tools/registry.go:859` 到 `internal/tools/registry.go:866`。
- 影响：如果 `.artifacts` 是指向 workspace 内另一目录的 symlink，lexical `.artifacts/...` 会被 resolve 掉，guard 看不到 `.artifacts` component，从而允许读取本应隐藏的内部生成 tool-output artifact，可能污染 review evidence 或暴露 ephemeral output。
- 建议：同时检查 lexical input path 和 resolved canonical path；resolution 前拒绝任何包含 `.artifacts` component 的 workspace input，并检查 canonical path 是否落在 resolved artifact root 下。

### R1-24 Low: 默认格式/验证 gate 比测试策略 spec 窄，跳过 validation Go helper

- 证据：测试策略 minimal CI 写 `go test ./cmd/... ./internal/... ./pkg/...` 加 `gofmt -l .` 为空：`spec/07-testing-strategy.md:190` 到 `spec/07-testing-strategy.md:195`。`test.sh` 只跑 `gofmt -l cmd internal pkg`：`test.sh:7`。但 validation scripts 会 build `./validation/cmd/retryproxy`：`validation/run_experimental_webconsole_followup_validation.sh:620` 到 `validation/run_experimental_webconsole_followup_validation.sh:623`、`validation/run_round61_task_heavy_real_matrix.sh:966` 到 `validation/run_round61_task_heavy_real_matrix.sh:967`。
- 影响：repo-owned validation helper 的 formatting/build drift 不会被默认 cheap gate 捕获，直到昂贵 live validation 才暴露；同时 spec 对 `gofmt -l .` 的承诺与脚本事实不一致。
- 建议：要么让 `test.sh` 覆盖非归档/generated 的 owned Go source（包括 `validation/cmd`）并 build retryproxy，要么收窄 `spec/07`，并增加独立 cheap validation-helper preflight。

## Round 2 Findings

### R2-01 High: cwd `.env` 可在 config resolution 前设置任意控制环境变量

- 证据：app load config 前先自动加载 env file：`internal/app/app.go:153` 到 `internal/app/app.go:157`。默认 env file 是 cwd `.env`，也允许 `GO_CLI_AGENT_ENV_FILE` 指向路径：`internal/config/envfile.go:13` 到 `internal/config/envfile.go:17`。`LoadEnvFile` 遍历每个 assignment，对当前为空的任意 key 调用 `os.Setenv`：`internal/config/envfile.go:33` 到 `internal/config/envfile.go:41`。随后 config load 会读取 `GO_CLI_AGENT_CONFIG`：`internal/config/config.go:356`。`spec/02` 也将 `.env` 写为默认自动读取：`spec/02-cli-and-config.md:257`。
- 影响：未受信仓库的 `.env` 可设置 `GO_CLI_AGENT_CONFIG` 路由到仓库控制配置，或设置 `PATH` / `HOME` / shell loader 变量，影响后续 shell/hooks/skill command 执行。该动作早于任何 workspace trust 判断。
- 建议：自动加载 cwd `.env` 时只允许明确的 provider key；拒绝 `GO_CLI_AGENT_*`、`PATH`、`HOME`、shell loader 等控制变量，或在未 trust workspace 时不加载 cwd `.env`。

### R2-02 Medium-High: skill command tools 绕过 shell sandbox 与 exec-policy controls

- 证据：shell tool 应用 sandbox 构造、exec policy 检测和 deny-mode 拦截：`internal/tools/registry.go:321` 到 `internal/tools/registry.go:346`。Skill command tool 只 render argv 后直接 `exec.CommandContext`：`internal/tools/registry.go:1866`、`internal/tools/registry.go:1880`，虽使用 env allowlist：`internal/tools/registry.go:1882`，但没有走 `shellSandboxCommand`、`DetectExecPolicyViolations` 或 deny metadata。
- 影响：即使 operator 设置 `runtime.shell.sandbox` 或 `exec_policy=deny`，已注册 skill command tool 仍通过另一条执行路径运行本地命令，不共享 shell tool 的安全控制。该问题会放大 R1-06 的 workspace command-tool 自动注册风险。
- 建议：抽出共享 command-executor policy 层，让 shell 与 skill command tools 统一经过 sandbox/exec-policy/metadata；或把 command tools 明确标为 trusted-only，workspace command tools 在 trust/load 前禁用。

### R2-03 Medium-High: WebConsole delete/clear 可删除 `running_not_owned` session

- 证据：session detail 模型显式区分 externally owned running session：`internal/webconsole/service.go:2179`，spec 也定义 `running_not_owned`：`spec/17-web-console.md:371`。`DELETE /api/sessions/{id}` 只检查当前进程 handle 与 running queue jobs：`internal/webconsole/service.go:580`、`internal/webconsole/service.go:2233`、`internal/webconsole/service.go:2279`。`POST /api/sessions/clear` 同样只检查当前进程 handle 和 running queue jobs：`internal/webconsole/service.go:606`。现有测试允许 clear 一个没有当前 live owner 的 `StatusRunning` session：`internal/webconsole/service_test.go:1567`。
- 影响：另一个 WebConsole 进程或 CLI 进程仍在运行的 session，只要本服务没有内存 handle，就可能被删除或 clear。这样会破坏 active session 的文件事实源，与 `running_not_owned` ownership 模型冲突。
- 建议：destructive session endpoint 扫描目标 session tree 的 `state.status == running`；除非显式 force 且能证明 stale owner/lease，否则拒绝删除。

### R2-04 High: resumable tool-execution failures 可留下 dangling provider tool calls

- 证据：Engine 在执行工具前已经把带完整 `result.ToolCalls` 的 assistant message 落盘：`internal/runtime/engine.go:252` 到 `internal/runtime/engine.go:270`。`tool.before` hook 失败时直接 `e.fail`，没有为当前 call 写 tool result：`internal/runtime/engine.go:316` 到 `internal/runtime/engine.go:319`。`tool.after` hook 失败也在 accumulated `toolResults` append 前失败：`internal/runtime/engine.go:437` 到 `internal/runtime/engine.go:439`、`internal/runtime/engine.go:488` 到 `internal/runtime/engine.go:491`。tool interrupt 只 append 当前 interrupted result，然后 pause 或跳下一 turn：`internal/runtime/engine.go:390` 到 `internal/runtime/engine.go:410`，同一 assistant message 后续 tool calls 仍无 result。failed session 可 continue：`internal/runtime/runner.go:415` 到 `internal/runtime/runner.go:418`。Provider replay 会序列化 persisted assistant tool calls 和 persisted tool results：OpenAI `internal/provider/openai.go:289` 到 `internal/provider/openai.go:304`，Anthropic `internal/provider/anthropic.go:237` 到 `internal/provider/anthropic.go:263`，Google `internal/provider/google.go:264` 到 `internal/provider/google.go:300`。spec 要求 result 写回前取消也要生成可 replay 的中断错误结果：`spec/01-runtime-architecture.md:367`。
- 影响：hook failure 或 multi-tool turn 中断后恢复，会把缺少 `function_call_output` / `tool_result` / `functionResponse` 的无效历史发给 provider，可能导致 resume 失败或历史错配。
- 建议：任何 assistant tool calls 已落盘后的 resumable fail/pause/interrupt，都要为当前和未执行 tool calls 合成 error tool results；或延迟 assistant tool-call message 持久化，直到 call/result set replay-complete。

### R2-05 High: background queue 把 paused / awaiting_input child session 当成 completed job

- 证据：queued job 可带 caller-selected mode：`internal/runtime/delegation.go:261` 到 `internal/runtime/delegation.go:305`，worker 用 `job.Mode` 启动 child session：`internal/runtime/delegation.go:340` 到 `internal/runtime/delegation.go:346`。`run` child 无 tool calls 时可正常返回 `awaiting_input`：`internal/runtime/engine.go:499` 到 `internal/runtime/engine.go:553`。但 `ProcessNextJob` 只在 `runErr != nil` 或 `result.Status == failed` 时标记 failed，其余全部标记 completed：`internal/runtime/delegation.go:371` 到 `internal/runtime/delegation.go:378`，随后 parent coordination 也按 completed resolve：`internal/runtime/delegation.go:384` 到 `internal/runtime/delegation.go:398`。Queue reconciliation 还会把 completed queue job 的权威状态写回非终态 child session：`internal/session/store.go:1395` 到 `internal/session/store.go:1417`。`awaiting_input` 在 spec 中不是完成态，可 `continue`：`spec/05-session-interrupt-resume.md:123` 到 `spec/05-session-interrupt-resume.md:144`。
- 影响：后台 child 若 paused 或 awaiting input，queue job 会显示 completed，parent completion gate 被解除；后续 reconcile 甚至可能把 child state 从 `awaiting_input` / `paused` 改成 `completed`。
- 建议：queue status 从 child terminality 映射：只有 `StatusCompleted` -> completed，`StatusFailed` -> failed；`paused` / `awaiting_input` / `running` 保持 unresolved/resumable，或引入明确的 blocked/resumable queue state。

### R2-06 Medium: WebConsole skill install/uninstall 信任 `skills.dirs[0]` 作为 destructive write/delete root

- 证据：config normalize 只把 `skills.dirs` 解析成路径，没有 containment/trust 校验：`internal/config/config.go:453` 到 `internal/config/config.go:455`。Web resolve skill dir 只 clean/join：`internal/webconsole/service.go:2489` 到 `internal/webconsole/service.go:2494`。Upload 使用 `cfg.Skills.Dirs[0]` 作为 dest：`internal/webconsole/service.go:2044`，并调用 `processSkillZip`：`internal/webconsole/service.go:2060`；后者会先 `os.RemoveAll(targetPath)`：`internal/webconsole/service.go:1893`。Uninstall 在同一 root 下 join skillID 后 `os.RemoveAll`：`internal/webconsole/service.go:2102`、`internal/webconsole/service.go:2109`。
- 影响：由于 workspace config 默认 auto-load，未受信仓库可把 skill management root 指向意外的 absolute 或 symlinked 目录。结合 R1-03 的 CSRF/origin 缺失，这会把 skill install/uninstall 变成更宽的 destructive local endpoint。
- 建议：Web skill install/uninstall 只能操作 trusted/canonical skill roots；拒绝来自未 trust workspace config 的 skill root，删除/解压前 resolve symlink 并强制 approved parent。

### R2-07 Medium: WebConsole skill uninstall 空 skill id 可删除整个 skill root

- 证据：router 通过 prefix/suffix 接受 `/api/skills/.../uninstall`，不是 exact segment shape：`internal/webconsole/service.go:314` 到 `internal/webconsole/service.go:316`。`handleUninstallSkill` 使用 `parts[3]` 但不拒绝空 string：`internal/webconsole/service.go:2090`。`targetDir := filepath.Join(rootDir, skillID)` 在 empty skillID 时就是 rootDir：`internal/webconsole/service.go:2102`；`pathWithinRoot` 允许 `rel == "."`：`internal/webconsole/service.go:1988`；随后 `os.RemoveAll(targetDir)`：`internal/webconsole/service.go:2109`。现有 tests 覆盖正常 skill uninstall 和 method guard，没有 empty id：`internal/webconsole/service_test.go:2201`。
- 影响：`POST /api/skills//uninstall` 可删除整个 configured local skill directory。结合 R1-03 的 CSRF/origin 缺失，破坏面远大于“卸载一个 skill”。
- 建议：按 exact segment count 解析路由，拒绝 empty / non-canonical skill id；要求 target 是 root 的 direct child 且包含 `SKILL.md`；增加 empty-id 回归。

### R2-08 Medium: `init` scaffold 直接 `os.WriteFile`，生成文件跟随 symlink

- 证据：`init` config overwrite 检查使用 `os.Stat`，会跟随 symlink，且 `--force` 可绕过：`internal/app/app.go:1175`。随后直接 `os.WriteFile` 写 config、`.env.example`、example `SKILL.md`、tool YAML、hook script：`internal/app/app.go:1186`、`internal/app/app.go:1189`、`internal/app/app.go:1199`、`internal/app/app.go:1207`、`internal/app/app.go:1216`。
- 影响：在恶意 workspace 中运行 `go-cli-agent init`，预置 symlink 可让 scaffold 覆写 workspace 外的用户可写文件。该风险与 R1-09 的 config persistence symlink 类似，但发生在 core `init` 主路径的一批生成文件。
- 建议：复用 symlink-aware atomic writer；`Lstat` final paths 并拒绝 symlink target；校验 canonical parent containment。

### R2-09 Medium: sensitive path / artifact guards 大小写敏感，不适配常见 case-insensitive workspace

- 证据：denied dirs/files 是小写 literal：`internal/tools/path.go:70`、`internal/tools/path.go:80`。检查逻辑使用 `part == denied` 与 `baseName == denied`：`internal/tools/path.go:121`、`internal/tools/path.go:128`。internal artifact guard 也只判断 `part == ".artifacts"`：`internal/tools/registry.go:864`。
- 影响：在常见 WSL `/mnt/c` 等 case-insensitive workspace 上，`.ENV`、`.Go-Cli-Agent/config.yaml`、`.SSH/config`、`.ARTIFACTS/...` 可能命中同一受保护位置或同类敏感位置，但绕过 lexical deny check。
- 建议：检测 case-insensitive workspace，或对 reserved path component 使用 `strings.EqualFold` / case-fold normalization；增加 case-folded reserved names 回归。

### R2-10 Medium: env-file upsert 读取 symlink target，并可能把外部 secret 内容复制进 workspace `.env`

- 证据：`UpsertEnvFile` 先 `os.ReadFile(path)` 读取已有文件：`internal/config/envfile.go:59`，再在该目录创建 temp file：`internal/config/envfile.go:90`，最后 rename 覆盖原 path：`internal/config/envfile.go:107`。WebConsole Settings 保存 API key 时调用它：`internal/webconsole/service.go:1432`。
- 影响：若 repo `.env` 是指向另一个可读 env-like 文件的 symlink，保存 API key 会读取并保留 target 的 entries，然后把 symlink 替换成 workspace 内普通 `.env`，其中包含复制出的外部 secret 内容。
- 建议：`Lstat` env file path 后拒绝 symlink；不要从非 regular file 或未信任 env-file root 保留内容。

### R2-11 Medium-High: failed `continue` 在构建模型 prompt 前清空 failure context

- 证据：failed session 允许 resume：`internal/runtime/runner.go:415` 到 `internal/runtime/runner.go:418`。`Continue` 在 `runExisting` 前清空 `state.IncompleteReason` 与 `state.LastError`：`internal/runtime/runner.go:461` 到 `internal/runtime/runner.go:468`。System prompt 只有在这些字段仍存在时才写 previous-run note 和 `incomplete_no_finish` finish reminder：`internal/runtime/prompt.go:98` 到 `internal/runtime/prompt.go:104`。Resume spec 要求失败续跑解释前次失败，并对 `incomplete_no_finish` 提醒 finish：`spec/05-session-interrupt-resume.md:231` 到 `spec/05-session-interrupt-resume.md:243`。
- 影响：失败续跑，尤其 `incomplete_no_finish`，会失去精确失败原因与 finish guidance，增加重复 no-finish 或错误恢复概率。
- 建议：保留 previous failure fields 到 prompt/reminder 构建完成，或在清空 state 前追加 dedicated harness reminder；旧 error 字段应在新 run 有 durable replacement 后清理。

### R2-12 Medium: provider `max_tokens` / `blocked` / adapter `error` stop reason 被折叠进普通 no-tool completion flow

- 证据：adapter 输出 distinct stop reason：OpenAI max output -> `max_tokens`：`internal/provider/openai.go:158` 到 `internal/provider/openai.go:165`；Anthropic `max_tokens` / `pause_turn` -> `max_tokens` / `error`：`internal/provider/anthropic.go:143` 到 `internal/provider/anthropic.go:151`；Google `MAX_TOKENS` / `SAFETY` -> `max_tokens` / `blocked`：`internal/provider/google.go:147` 到 `internal/provider/google.go:155`。StopReason contract 区分 `max_tokens`、`blocked`、`error`：`spec/03-provider-contracts.md:93` 到 `spec/03-provider-contracts.md:111`。Engine no-tool branch 不看 `result.StopReason`，`run` 进入 `awaiting_input`，`exec` 走 generic finish reminder / `incomplete_no_finish`：`internal/runtime/engine.go:499` 到 `internal/runtime/engine.go:525`。
- 影响：provider 截断、安全阻断或 adapter pause/error 会被 operator 看到为普通自然停顿或缺 finish，诊断和续跑建议不准确。
- 建议：generic no-tool handling 前增加 stop-reason gate；至少持久化 `IncompleteReason=max_tokens|blocked|provider_stop_error`，并写针对性 resume/remediation message。

### R2-13 Low-Medium: provider attempt ledger 终态 success/failure 缺少实际 attempt number

- 证据：架构/spec 要求 provider attempt ledger 保留 turn 和 attempt：`spec/01-runtime-architecture.md:148` 到 `spec/01-runtime-architecture.md:154`、`spec/18-durable-contract-and-completion.md:91` 到 `spec/18-durable-contract-and-completion.md:99`。retry event 含 failed attempt / next attempt：`internal/provider/http.go:68` 到 `internal/provider/http.go:89`，`recordProviderRetry` 会存 attempt：`internal/runtime/provider_attempts.go:14` 到 `internal/runtime/provider_attempts.go:24`。但 terminal failure/success 没有填实际 attempt：`internal/runtime/provider_attempts.go:27` 到 `internal/runtime/provider_attempts.go:49`，`baseProviderAttempt` 只填 turn/provider/model/created_at：`internal/runtime/provider_attempts.go:67` 到 `internal/runtime/provider_attempts.go:79`。
- 影响：retry 后 success 或最终 failure 时，`provider-attempts.jsonl` 不能直接说明终态由第几次 attempt 产生，削弱超时/重试诊断。
- 建议：让 `JSONClient.DoJSON` 暴露 terminal attempt metadata，或发出带 attempt 的 terminal provider event，再由 `recordProviderSuccess` / `recordProviderFailure` 持久化。

### R2-14 Medium: `spec/15` queue job contract 缺失 lease/liveness 与 parent wait-mode 事实

- 证据：`spec/15` job JSON 示例缺少 `claimed_by`、`claimed_at`、`heartbeat_at`、`worker_pid`、`process_start_id`、`visible_paths`、`wait_mode`：`spec/15-background-queue.md:42` 到 `spec/15-background-queue.md:67`；queue submit 参数也缺少 `--wait-mode`：`spec/15-background-queue.md:86` 到 `spec/15-background-queue.md:92`。但 runtime architecture 要求 durable claim lease / heartbeat facts：`spec/01-runtime-architecture.md:189`，doctor 要报告 missing lease / stale heartbeat：`spec/02-cli-and-config.md:206`，Web queue submit 暴露 `wait_mode`：`spec/17-web-console.md:474`，实际 DTO 有这些字段：`internal/session/types.go:375` 到 `internal/session/types.go:405`。
- 影响：Phase 13 owning spec 已不足以解释 queue liveness、stale-running reconcile、doctor 检查和 parent coordination 行为，后续实现/验收容易回退。
- 建议：同步 `spec/15` job schema、CLI 参数和验收，明确 lease/heartbeat/visible_paths/wait_mode 的文件事实。

### R2-15 Low-Medium: Web worker pool lower-bound contract 与 validation/API 行为冲突

- 证据：`spec/17` 写 worker pool 允许 `N >= 1`：`spec/17-web-console.md:292`。focused validation 启动 WebConsole 时使用 `--workers 0`：`validation/run_experimental_webconsole_followup_validation.sh:572`，随后通过 API scale 到 1：`validation/run_experimental_webconsole_followup_validation.sh:757`。Backend 只拒绝负数：`internal/webconsole/service.go:1098`，worker pool close 也使用 `Scale(0)`：`internal/webconsole/service.go:2509`。
- 影响：如果按 spec 字面修复为禁止 0，会破坏当前稳定 validation 的 no-worker inspection / later scale 路径；如果保留 API 行为，spec 会误导验收。
- 建议：将 spec 改成允许 `N >= 0`，并说明 0 是无 worker 观察/测试模式；或改 validation 和 API 一致禁止 0。

### R2-16 Low-Medium: `spec/07` WebConsole browser smoke 范围仍描述已简化掉的 UI 覆盖

- 证据：`spec/07` 仍写 browser UI smoke 覆盖 worker 更新、queue view、queue-links 通知、manual refresh：`spec/07-testing-strategy.md:167` 到 `spec/07-testing-strategy.md:170`。当前 `spec/17` 已收窄为 selected job facts，worker API scaling / queue notification dedup / retry proof 由 shell/service tests 覆盖：`spec/17-web-console.md:601`。follow-up validation summary 也明确不声称 standalone Overview 或 Worker Pool page interactions：`validation/run_experimental_webconsole_followup_validation.sh:923`。Browser smoke script 通过 API 提交 queue job 并验证 selected job facts：`validation/scripts/webconsole_ui_smoke.mjs:552`、`validation/scripts/webconsole_ui_smoke.mjs:629`。
- 影响：测试策略会让 reviewer 期待已被用户要求简化掉的 UI surfaces，或高估 browser smoke 实际证明的 queue/worker 范围。
- 建议：同步 `spec/07` 到 `spec/17` 的简化口径，明确哪些由 browser smoke 覆盖，哪些由 shell/service tests 覆盖。

### R2-17 Low-Medium: Web 新建 session / queue UI 与 API/spec 的 provider/mode/isolation 控制面不匹配

- 证据：API wrapper 支持 `provider`、`model`、`mode`、`system`、`isolation_mode`、`isolation_root`：`internal/webconsole/assets/api.js:57`。主 prompt 新建 session 只传 `prompt` 和 `workdir`：`internal/webconsole/assets/app.js:843`。Queue submit UI 暴露 prompt/model/parent/role，但只发送 `prompt`、`parentSessionID`、`model`、`agentRole`、`workdir`：`internal/webconsole/assets/app.js:1749`、`internal/webconsole/assets/app.js:1805`。`spec/17` new-task flow 仍写用户选择 provider/model/mode/isolation：`spec/17-web-console.md:508`。
- 影响：backend/API 和 spec 暗示的控制面比当前 UI 可提交的更宽，容易形成 WebConsole provider/isolation 验收假象。考虑用户此前要求简化 WebConsole，这更可能是 spec/UI 口径未同步，而不是必须加 UI 功能。
- 建议：若保持 UI 克制，更新 `spec/17` 明确 provider/mode/isolation 是 API-only / advanced / future controls；若确需 UI，添加紧凑 advanced controls，不能让 WebConsole 反向主导 core CLI 叙事。

## Round 3 Findings

Round 3 同时做了去重与代表性证据复核：R1/R2 条目没有发现需要删除的重复项或明显错误证据；R1-07 已正确归类为当前 spec/实现共同允许的安全策略风险；R1-13 与 R2-05 分别覆盖 synchronous child 与 background queue child 两条不同路径。最终新增 2 个问题如下。

### R3-01 High: 默认 `workspace/` 可被 symlink 重定向到仓库外

- 证据：root session 未显式传 `--workdir` 时使用 `cwd/workspace`：`internal/runtime/runner.go:291` 到 `internal/runtime/runner.go:304`。该路径只做 `filepath.Abs`：`internal/runtime/runner.go:320`，默认 workspace 分支随后创建目录但没有在 `EvalSymlinks` 后确认仍在 cwd/repo 下。README 与 spec 都承诺默认工作目录是当前目录下的 `workspace/`：`README.md:30`、`spec/02-cli-and-config.md:101`。WebConsole 也把 `cwd/workspace` 作为 workspace root：`internal/webconsole/service.go:1302` 到 `internal/webconsole/service.go:1306`；而 `tools.ResolveWorkspacePath` 会对 base 做 `EvalSymlinks`，把 symlink target 当成新的 workspace base：`internal/tools/path.go:11` 到 `internal/tools/path.go:17`。
- 影响：恶意 repo 只要把 `workspace` 预置为指向用户目录或其他敏感目录的 symlink，新 root session 的 tool workspace 就可能落到 repo 外；WebConsole `/api/files` 默认根也可能列出 symlink target 内容。这不同于前两轮的 temp-file/config/env/artifact symlink 问题，风险点是默认 workspace root 选择本身。
- 建议：默认 `workspace/` 创建/使用前 `Lstat` 拒绝 symlink，或对 resolved path 做 repo/cwd containment 校验；WebConsole workspace root 同步使用相同策略。

### R3-02 Medium-High: runtime context loader 直接读取 project memory / `AGENTS.md`，绕过 workspace resolver

- 证据：project memory 固定读取 `reports/spec.md`、`reports/plan.md`、`reports/progress.md`、`reports/validation.md`：`internal/runtime/project_memory.go:20` 到 `internal/runtime/project_memory.go:25`；实际读取是 `os.ReadFile(filepath.Join(workdir, rel))`，没有 `ResolveWorkspacePath` 或 symlink containment：`internal/runtime/project_memory.go:30` 到 `internal/runtime/project_memory.go:37`。这些 excerpt 会进入 project memory summary：`internal/runtime/project_memory.go:61` 到 `internal/runtime/project_memory.go:78`，并写入 compaction summary 的 `project_memory_stack`：`internal/runtime/compaction.go:48`、`internal/runtime/compaction.go:81`。`AGENTS.md` 读取同样是 `os.ReadFile(workdir/AGENTS.md)`：`internal/runtime/prompt.go:3010` 到 `internal/runtime/prompt.go:3017`，并拼入 system prompt 的 Project Instructions：`internal/runtime/prompt.go:105` 到 `internal/runtime/prompt.go:108`。`spec/18` 明确 `yolo` 不应绕过 workspace path safety：`spec/18-durable-contract-and-completion.md:81`。
- 影响：即使 file tools 能拒绝 symlink escape，runtime 自身仍可能通过 `reports/*.md` 或 `AGENTS.md` symlink 读取 repo 外文件片段，并发送给模型或写入 compaction artifact。该问题不同于 R1-05 的 compaction 默认 redaction 口径收敛和 R2-10 的 `.env` upsert symlink 复制。
- 建议：runtime 内部读取 workspace 文件也统一走 symlink-aware resolver；对 `reports/*.md`、`AGENTS.md` 这类 context loader 输入执行 containment 校验，escape 时记录 missing/blocked 而不是读取内容。
