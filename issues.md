# go-cli-agent 收敛问题登记

更新时间：2026-04-24

本文替代旧的 `Session 20260423-092356-5ad621` 单次复盘清单，作为当前仓库的收敛问题登记。旧复盘中的 incident 事实仍有效，但对应 P0/P1 缺陷已经进入代码和验证闭环；本文件只保留当前仍需要 operator 关注的真实状态。

## 1. 当前结论

- 当前未发现阻断 core v1 收敛的 P0/P1 开放缺陷。
- 旧 session 暴露的目标漂移、报告冲突、长任务无 taskboard、provider timeout 后手工 continue、compaction storm、finalization 非原子等问题，已经由 runtime guard、provider policy、completion controller、contract/artifact tracker、checkpoint 和 summary 机制覆盖。
- `experimental web` 仍保持显式实验入口，但关键 operator surface 已闭环：本地 Markdown sanitizer、非 2xx 反馈、settings `finally` 恢复、WebSocket malformed payload 防护、本地 icon/Markdown 依赖、queue worker scale control、dynamic bottom clearance、固定 task tracker、queue/detail/session drilldown 都已落地。
- 本轮额外完成前端第一阶段模块化：纯工具/Markdown/format helper 已从 `app.js` 拆到 `internal/webconsole/assets/utils.js`，仍保持无 bundler、单 Go 进程 embedded assets。
- `plan.md` 是本地执行计划文件，并被 `.git/info/exclude` 排除；本轮已清理其中的裸 API key，后续若要纳入 git，必须继续保持只引用 `OPENAI_API_KEY` 环境变量，不写入真实 secret。

## 2. 已关闭问题矩阵

| 问题 | 当前状态 | 代码事实 |
| --- | --- | --- |
| 目标漂移后最终报告仍围绕旧目标 | 已关闭 | `internal/runtime/prompt.go` 的 `target_consistency` reminder/write/finish guard 会读取最新显式 target anchor，并在 final artifact 或 project-memory 更新前阻断陈旧目标。 |
| 主报告与 `progress.md` / `validation.md` 冲突 | 已关闭 | `internal/runtime/prompt.go` 的 `report_consistency` guard 会在 supporting docs 更新晚于最终报告或内容结论矛盾时阻断写报告/finish。 |
| 长任务没有 durable todo/taskboard | 已关闭 | `internal/runtime/prompt.go` 的 `long_run_taskboard` guard 在长会话且无 `todo_write` / `task_*` 事实源时阻断 finish，即使 `yolo` 也不绕过。 |
| provider timeout 粗糙、报告阶段反复人工 continue | 已关闭 | `internal/config/config.go`、`internal/provider/http.go`、`internal/runtime/engine.go` 已拆分 `request_timeout_sec` / `stream_idle_timeout_ms`，并在无新工具副作用的 `upstream_timeout` 上记录 `provider.auto_resume`。 |
| provider retry / auto-resume 缺少独立排障 ledger | 已关闭 | `internal/session/store.go` 与 `internal/runtime/provider_attempts.go` 写入 `.go-cli-agent/sessions/<id>/provider-attempts.jsonl`。 |
| compaction storm 与重复 summary artifact | 已关闭 | `internal/runtime/compaction.go` 支持 `runtime.compact.hysteresis_delta_chars`，水位未增长超过 delta 时写 `compact.reused` 而不反复重写 summary artifact。 |
| finish/tool gate 分散 | 已关闭 | `internal/runtime/completion_controller.go` 统一包装 tool/finish gate，继续复用既有 review、artifact、target、report、taskboard、steer guard。 |
| explicit required artifact 只靠模型自觉 | 已关闭 | `internal/runtime/contract.go`、`internal/runtime/completion_controller.go`、`artifact-tracker.json` 已实现 baseline / touched / changed gate，并写 `contract.created/updated`、`artifact.required`、`artifact.tracked`、`artifact.gate.*` 事件。 |
| checkpoint resume drift 只覆盖 provider/model/workdir | 已关闭 | `internal/runtime/session_summary.go` 的 drift warning 已覆盖 `requested_workdir`、isolation mode/request/workdir/root、contract trust source。 |
| parent coordination 只有 wait gate、没有 park/resume 事实 | 已关闭 | `internal/runtime/parent_coordination.go` 写 `parent-coordination.json` 的 `parked` 状态，并发出 `parent.coordination.parked` / `parent.coordination.resumed`。 |
| child/queue 完成后 parent 缺少结构化回流 | 已关闭 | `internal/runtime/engine.go` 会把 `control/background.jsonl` pending notification 注入为 `<background-agent-results>` 用户消息，包含 status、role、visible paths、final text、last error，并标记 accepted。 |
| workspace extension 短名冲突未检测 | 已关闭 | `internal/extensions/trust.go` 增加 duplicate short-name ambiguity 校验；reserved name 与 qualified name 校验仍保留。 |
| explicit `runtime.shell.sandbox: bwrap` 缺少 `.git` 写保护 | 已关闭 | `internal/tools/shell_sandbox_linux.go` 在 bwrap profile 下对 workspace `.git` 做 `--ro-bind-try` 覆盖。 |
| WebConsole 固定高度 virtual scroll / inline handler / CDN / queue worker UI | 已关闭 | `internal/webconsole/assets` 已移除固定 virtual scroll 与 inline `onclick`/`style=` markup，使用本地 assets、`ResizeObserver` bottom clearance，并提供 `/api/workers` worker scale UI。 |
| 前端代码结构完全单文件 | 已关闭第一阶段 | `internal/webconsole/assets/utils.js` 承载纯工具函数；`internal/webconsole/assets/index.html` 顺序加载 `icons.js`、`utils.js`、`app.js`，不引入 Node/Vite 常驻服务。 |

## 3. 当前非阻断关注项

- **WSL 权限语义**：在 `/mnt/c` 上 owner-only permission 可能无法被 Windows 文件系统严格表达；`doctor --skip-probe` 会提示该环境限制。需要强权限隔离时，建议把 session root 放在 Linux 原生文件系统，例如 `/root/.go-cli-agent/sessions`。
- **bwrap 语义**：当前策略不是“进程只能看到 workspace 的字面沙箱”，而是“workspace 可写、`.git` 只读覆盖、系统运行依赖只读、`/tmp` tmpfs”。这是为了保持 shell 可运行性和 portable 默认路径不受影响。
- **WebConsole 结构化深拆**：`utils.js` 已完成第一阶段文件级拆分；继续把 queue/history/settings 独立成更多文件属于维护性优化，不是当前 acceptance blocker。
- **真实 live proof**：未来只要改动 provider、queue、background notification、WebConsole operator flow，应继续运行 live follow-up，而不是只依赖静态检查。
- **未跟踪 reports 噪声**：当前 workspace 下存在大量 `reports/` 与 `skills/pentest-toolset/` 未跟踪产物；它们不是本轮收敛补丁的一部分，不应被 `git add -A` 带入提交。

## 4. 本轮验收记录

本轮文档和前端结构收敛后已通过：

- `gofmt -l ./cmd ./internal ./pkg`
- `go test ./cmd/... ./internal/... ./pkg/...`
- `node --check internal/webconsole/assets/app.js`
- `node --check internal/webconsole/assets/icons.js`
- `node --check internal/webconsole/assets/utils.js`
- `./build.sh`
- `./bin/go-cli-agent doctor --skip-probe`
- `git diff --check`
- `set -a; . ./.env; set +a; GO_CLI_AGENT_MATRIX_LABEL=convergence-utils-split-20260424 validation/run_experimental_webconsole_followup_validation.sh`

真实 follow-up 证据：

- `validation/runs/2026-04-24-openai-compatible-gpt-5.4-convergence-utils-split-20260424/SUMMARY.md`
- `validation/runs/2026-04-24-openai-compatible-gpt-5.4-convergence-utils-split-20260424/raw/webconsole-ui-smoke.json`

`doctor --skip-probe` 仍按预期提示 `/mnt/c` 不支持严格 owner-only permission；这是环境限制，不是本轮功能回归。

## 5. 收敛判定

旧复盘暴露的问题已从“人工纪律”升级为 runtime 文件事实、事件、guard、checkpoint、summary、WebConsole operator surface 的组合约束。当前关闭标准不是“没有风险”，而是：

- 普通 CLI 主路径仍保持 `init/run/exec/steer/continue/sessions/tasks/probe-provider/doctor` 简洁。
- durable session facts 仍是唯一事实源；summary/checkpoint/WebConsole 只是派生视图。
- explicit contract 与 required artifact 能阻断缺失、陈旧和未触达交付物。
- long-running / delegated / queue / experimental web 行为都有可观察状态与恢复线索。
- 后续新增问题必须先落入本登记或对应 spec，再继续实现，避免文档、计划和代码再次分叉。
