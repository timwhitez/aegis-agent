# 安全模型与 policy

## 1. 当前安全边界

当前实现冻结以下 policy surface：

- task truth 在本地 `.ngen/`
- approval 通过 durable artifacts 表达
- scheduler 通过 workspace lease 防止重复唤醒
- verifier / review / done gate 不得被 CLI shortcut 绕过
- hooks failure 必须显式报错
- provider adapters 只返回下一动作 decision，不拥有 task truth
- provider/ACP/terminal 不得反向拥有 task truth
- provider HTTP response body decode 前必须执行 bounded read，当前上限为 4 MiB；超过上限的 Responses、OpenAI-compatible Chat Completions、Anthropic 与 repair/observation/mission validation response 都必须失败为显式 adapter diagnostic，而不是继续反序列化

## 2. 当前文件系统边界

- runtime 只在当前 workspace 与 configured additional roots 下工作
- `.ngen/` 是唯一 canonical runtime state root
- `ngen.json` 中会落盘或影响落盘位置的路径字段必须在 config load 阶段校验：`state_dir` 当前固定为 `.ngen`；`scheduler.lease_file` 与 `memory.file` 必须是 workspace-relative slash paths，禁止绝对路径、`..` workspace escape、Windows drive / backslash syntax 与 NUL。`memory.file` 是 workspace memory Markdown 的实际写入路径，不能被接受后静默忽略。
- Store artifact path 中的 task、mission、session、watch、worker、checkpoint、command 与 diagnostic id 必须先通过 single-segment validation；禁止空值、`.` / `..`、路径分隔符、drive/UNC 语法、NUL、artifact file suffix 注入和非 `[A-Za-z0-9_-]` 字符
- additional roots / visibility deny rules 已进入 active contract
- bounded observation commands 与 workspace edit paths 都必须服从同一条 visibility deny 规则；读取或写入 `.ngen/` 等 deny path 必须显式失败并留下 diagnostics / command or edit records。observation allowlist 不能允许工具 flag 绕过 deny roots：`rg` 的 hidden/ignored/symlink traversal flags 会被拒绝，`rg --files` 的 path operand 仍必须校验，broad `rg` / `ls` 不能覆盖非隐藏 deny roots；`ls` hidden listing flags 会被拒绝；`find` 覆盖 denied root 的 broad search 会被拒绝，除非未来实现能证明 deny pruning 语义。`go` observation 不允许 verifier/build 或 mutating env/list flags；`git` observation 不允许 `--no-index`、external diff/textconv、output files、`rev:path` content reads、ignored listing，或绕过 `--` 的 deny-path pathspec / broad content commands。
- workspace write/delete/patch 与 worker reconcile auto-apply 必须先检查 workspace containment 与 no-symlink component；中间目录 symlink、最终 symlink 或 workspace 外路径都必须拒绝。workspace snapshot 不 follow symlink，必须把 symlink omission 计入 collection metadata；bounded `find` observation 不允许 `-H` / `-L` / `-follow`、外部 path predicate 或 expression 后 path operand
- bounded workspace repair command 是当前更宽的 argv-based action plane：它们固定在 `workspace root` 下运行，并受 permission mode、command budget、timeout、durable `command_runs.jsonl`、以及 verifier/review gate 收口；`standard` mode 只允许明确 allowlisted 的 safe commands（当前包括 `gofmt`、`go fmt/test/build/vet/generate`、`go mod tidy/download/verify`、`cargo fmt/test/build/check`），把 shell/script/package-manager/repo-script/Multica mutation 这类命令归为 `needs_approval` 并拒绝自动执行；`yolo` mode 可以执行更宽 argv，但必须在 prompt 与 `command_runs.jsonl` 中显式留下 `permission_mode_id`、`policy_decision` 与 `replay_safety`。Multica issue execution task 只能把 `multica issue get|list` 和 `multica issue comment list` 作为 read-only observation/verifier；`multica issue comment add` 属于外部 issue-state mutation，必须走 repair command policy，且 benchmark integrity mode 会拒绝它。当 `permission.benchmark_integrity_mode=true` 时，repair command lane 会在 `standard` 与 `yolo` 下共同拒绝网络或 open-world 命令，包括 direct network clients、Git remote operations、package managers、shell/interpreter wrappers、path-based repo scripts、`go mod download` / `go get` / `go install` / `go generate`、Multica commands，以及 Cargo fetch/install/update；拒绝记录必须写 `policy_decision=denied_benchmark_integrity` 与 `replay_safety.replay_policy=do_not_auto_replay`
- command provider、observation/repair command 与 hooks stdout/stderr 都必须 bounded capture，当前每个 stream 上限为 1 MiB；命中上限时写出已捕获 artifact、`stdout_truncated` / `stderr_truncated` metadata 与 truncation summary
- `replay_safety.replay_policy=safe_to_replay` 只允许 read-only 或明确 idempotent 的 command 自动重放；`manual_review_required` / `do_not_auto_replay` 的同 argv repair command 已存在 side-effect record 时，runtime 必须拒绝再次自动执行
- HTTP web backend 是 local management surface，默认只监听 `127.0.0.1:8765`；除 `/healthz` 外，如果 `--token-env` 指定的环境变量非空，所有 API 与 SSE event stream 都必须要求 bearer token。token 为空时，非 loopback listen address 必须拒绝启动，除非 operator 显式传 `--allow-unauthenticated`。web backend 只能调用现有 runtime service contract，不得新增 web-only task truth 或绕过 approval/verifier/review/done gate。
- coding repair snapshot 在命中文件/字节预算时仍必须继续统计 omitted truth，并优先保留 verifier failure 明确指向的路径与核心 build/config files
- manager-controlled child 当前允许 bounded writable workspace：`shared_workspace`、`snapshot_copy` 与 `git_worktree` 三种模式；其 lifecycle truth 写入 `worker_runtime/*.workspace.json`，isolated child baseline / reconcile truth 写入 `worker_runtime/*.baseline.json` / `*.reconcile.json`
- 当前 child role policy 已有一个 active 子集：`coding` / `general_execution` child 的 isolated reconcile mode 默认是 `apply_on_accept`，只有在 baseline 与 current parent 没有冲突时才会自动回写 parent workspace；`reviewer` / `security_review` child 默认是 `artifact_only`。这些默认值现在可通过 `subagents.role_policies` override，同时 runtime 还会按 `allow_child_workers`、`allowed_worker_roles` 与 `max_lineage_depth` 拒绝不被允许的 nested delegation

## 3. 当前 approval model

当前 approval 只支持：

- request
- approve
- deny

当前 approval 主要用于 `blocked_policy` 路径；当 permission mode 为 `yolo` 时，请求会自动批准，但仍留下 durable approval records。

## 4. 当前 secrets / diagnostics 规则

- secrets 不得明文写入 artifacts
- verifier / review 可写摘要，但不应持久化敏感凭证值
- workspace memory promote 必须先经过 redaction
- diagnostics 必须说明失败原因与坏引用，但不应泄露高敏内容

## 5. deferred design

以下内容仍可继续加强：

- stronger child sandbox inheritance，超出当前 `shared_workspace` / `snapshot_copy` / `git_worktree` workspace isolation
- richer provider/network policy matrix，超出 builtin/command、OpenAI-compatible Chat Completions、OpenAI Responses 与 Anthropic Messages 当前范围
- deeper `security_review` / `reviewer` semantics
