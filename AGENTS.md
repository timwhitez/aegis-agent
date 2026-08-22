DO NOT send optional commentary

# Aegis Agent

本文件作用域覆盖整个 `aegis-agent/` 目录树。

## 工作方式

- 先看 spec，再改代码。开始实现前至少阅读 `spec/00-product.md`、`spec/01-runtime-architecture.md`、`spec/03-provider-contracts.md`、`spec/09-phase-plan.md`、`spec/11-spec-audit-and-traceability.md`、`spec/12-task-system.md`、`spec/13-live-input-and-steering.md`。
- 严格按 phase 推进，不要跨 phase 偷渡功能。
- 当前项目是 Web-first 本地 agent harness；默认用户入口是 `aegis-agent web` 本地控制台，CLI 保留为脚本化、CI、故障恢复和高级调试 fallback。
- Web-first 不等于 hosted SaaS、复杂 IDE 或重型 TUI；不要引入复杂面板布局、鼠标驱动 workflow、浏览器端文件编辑器、远程终端或图形化状态权威源。
- 保持“模型是 agent，harness 提供环境”的边界，不要把固定 DAG、硬编码 workflow engine、重型 orchestration 塞进 runtime。
- 当前默认主路径是 `web` + CLI fallback：`init/run/exec/steer/continue/sessions/goal/tasks/probe-provider/doctor`。
- `delegate` / `children` / `queue` 可以作为 Web-first large-project profile 的轻量入口和观测面存在；细粒度 orchestration、worker internals、isolation tuning 和 `tui` 仍属于高级/扩展面，不要让它们主导默认 Web 页面或根文档。

## 架构约束

- `core runtime`、`sdk facade`、`web app/service adapter`、`cli adapter` 必须分离。
- provider 差异留在 adapter 层处理，不要让 Web、CLI 或 tool 层承载 provider-specific replay 逻辑。
- session / state / messages / events 必须是事实源，不能把关键执行状态只留在内存里。
- compaction 只能改变发给模型的上下文视图，不能覆盖原始日志。
- provider 的 generation / reasoning / store 选项必须通过 runtime/session 元数据传递，不能只停留在一次性 CLI 参数里。
- Web 控制台只能复用 session store、queue store、runtime facade 和本地文件事实源；不能维护第二套权威状态。

## Runtime Guard 与模型自主性

- 模型是 agent，runtime / tool schema 负责提供能力描述、事实记录和安全边界；不要用 `prompt.go` 或 tool guard 固定审计路线、委派策略、阅读顺序、taskboard 节奏等本应由模型判断的工作流。
- 优先通过 tool description、system prompt、skill 文档和 harness reminder 引导决策；一次 session 暴露的问题先优化描述与可观测事实，避免把 runtime 写成 task-specific workflow engine。
- hard guard 只用于安全/权限边界、workspace/path escape、用户显式指定的交付路径/模板/字面锚点、恢复一致性、provider/tool 协议完整性，以及用户最新 steer 的明确约束。
- sub-agent/child-agent 是否使用必须 model-led；优化 `agent_spawn` / `agent_status` / `agent_list` 描述和 child prompt 模板来提示委派时机，不要用 runtime guard 强迫或阻止委派。
- durable project memory、todo/taskboard 和 long-run checkpoint 主要用于恢复与协作提醒；除非 finish 会留下明显过期或矛盾的 durable state，不要阻断 agent handoff 或普通执行动作。
- 不要把报告、prompt、session、compaction 或 provider view 脱敏写成 runtime / spec 的默认规范；如需脱敏，由用户在当轮 prompt 中明确要求，并作为任务交付内容处理。

## 安全与恢复

- 文件工具必须防止 workspace escape，并处理 symlink escape，不要只做 `Clean`/`Rel`。
- `shell` 工具必须有 timeout、输出截断和最小环境变量 allowlist。
- 中断发生在 tool call 之后时，要写入可重放的中断错误结果，避免恢复时出现 dangling tool call。
- session 目录与 agent 产物默认使用 owner-only 权限。

## 文档纪律

- root `README.md` 和本 `AGENTS.md` 保持简短、稳定、可检索。
- 详细设计写在 `spec/`，不要把实现细节堆回根说明文件。
- 不要在文档里引用当前仓库不存在的脚本或路径。

## Multica 兼容部署经验

- `gocli-stream-json` 是 Multica 与本仓库的唯一耦合面；需要扩展时优先更新 `spec/multica-integration/`，再改 `internal/streamjson` 和 CLI flag。
- Multica 远端部署推荐用 `AEGIS_AGENT_CONFIG` 指向全局 aegis-agent 配置，而不是只在 `MULTICA_GOCLI_ARGS` 里加 `--config`；这样 `models --json` 和 `exec` 会读同一份配置。
- 给 Multica gocli runtime 的全局配置应只让任务执行扫描 `./skills`：这是 Multica workspace shared skills 的动态注入目录，成员创建、从 URL 导入或从本地运行时复制后的 skills 都通过这里对 agent 生效。不要默认扫描 `~/.codex/skills`；本地运行时 skills 在复制到 Multica workspace 前是私有来源。
- `gpt-5.5` 由 aegis-agent 内置 context window 表默认按 `300000 * 4 * 0.85 = 1020000` 字符触发压缩；当远端 Codex `debug models` 显示 `gpt-5.5` 的 `context_window=272000` 且 `effective_context_window_percent=95` 时，若需要与 Codex 精确对齐，gocli 全局配置仍应给 `openai/gpt-5.5` 设置最高优先级覆盖 `runtime.compact.context_profiles.openai/gpt-5.5.input_char_threshold: 1033600`，即 `272000 * 0.95 * 4` 的 v1 字符近似；`hysteresis_delta_chars` 可设为 `258400`。
- 在远端主机按端口重启或替换服务时，必须先把 `ss` / `lsof` / `pgrep` 输出限定到目标端口或目标命令，再提取 PID；不要对整份监听列表做全局 `sed` / `awk` 取第一个 `pid=`，避免误杀同机其他服务，例如 `coco-sandbox-ui` 的 `:8000` 进程。

## Git 纪律

- `aegis-agent/` 现在是独立 Git 仓库根目录；写入型改动默认在这个仓库内完成，不要依赖外层 loose workspace 的无版本状态。
- 后续任何代码修复都必须在对应验证完成后产出真实 `git commit`；不要把“已修好”的代码留在未提交状态。
- 如果一次修复同时涉及测试、`spec/`、`README.md` 或本 `AGENTS.md`，这些配套改动应和代码修复一起进入同一个 commit，保证提交能完整表达当轮变更。

## 当前阶段

- 当前阶段是“Web-first v1 收敛态”：先把 Phase 0-10 的 runtime / provider / CLI 基座与 Phase 15 的本地 Web 控制台彻底对齐，再评估更高级扩展。
- Phase 15 Web 控制台是默认 app surface；Phase 11-14 和 Phase 16+ 视为 large-project / advanced / experimental profile，不是默认页面的复杂化理由。
- 当用户明确要求大型项目 profile、child orchestration、隔离编辑或“超越 codex/opencode”的验证时，可以推进并验证 Phase 11-13，但要保持这些能力通过 Web 的轻量入口或显式 `experimental`/`--isolation` CLI/API 暴露，而不是把默认 Web 首页、root help 或 README 做厚。
- 当 spec 与当前代码或用户最新指令冲突时，先修 spec，再继续实现，避免文档和实现分叉。
