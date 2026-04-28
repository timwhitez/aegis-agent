# Go CLI Agent

本文件作用域覆盖整个 `go-cli-agent/` 目录树。

## 工作方式

- 先看 spec，再改代码。开始实现前至少阅读 `spec/00-product.md`、`spec/01-runtime-architecture.md`、`spec/03-provider-contracts.md`、`spec/09-phase-plan.md`、`spec/11-spec-audit-and-traceability.md`、`spec/12-task-system.md`、`spec/13-live-input-and-steering.md`。
- 严格按 phase 推进，不要跨 phase 偷渡功能。
- 当前项目是 CLI-only harness，不要引入复杂 TUI、面板布局、鼠标交互或图形化状态管理。
- 如果用户明确要求补充 Web 控制台或图形化观测面，先更新 `spec/` 与 README，把它们收敛为显式 `experimental` 扩展入口，再实现；不要把默认 core CLI 叙事改成 Web-first。
- 保持“模型是 agent，harness 提供环境”的边界，不要把固定 DAG、硬编码 workflow engine、重型 orchestration 塞进 runtime。
- 当前默认主路径是 `init/run/exec/steer/continue/sessions/tasks/probe-provider/doctor`。
- `delegate` / `children` / `queue` / `tui` 只作为扩展兼容面保留；除非用户明确要求继续推进 Phase 11+，否则不要让它们主导文档、脚本或验收口径。

## 架构约束

- `core runtime`、`sdk facade`、`cli adapter` 三层必须分离。
- provider 差异留在 adapter 层处理，不要让 CLI 或 tool 层承载 provider-specific replay 逻辑。
- session / state / messages / events 必须是事实源，不能把关键执行状态只留在内存里。
- compaction 只能改变发给模型的上下文视图，不能覆盖原始日志。
- provider 的 generation / reasoning / store 选项必须通过 runtime/session 元数据传递，不能只停留在一次性 CLI 参数里。

## Runtime Guard 与模型自主性

- 模型是 agent，runtime / tool schema 负责提供能力描述、事实记录和安全边界；不要用 `prompt.go` 或 tool guard 固定审计路线、委派策略、阅读顺序、taskboard 节奏等本应由模型判断的工作流。
- 优先通过 tool description、system prompt、skill 文档和 harness reminder 引导决策；一次 session 暴露的问题先优化描述与可观测事实，避免把 runtime 写成 task-specific workflow engine。
- hard guard 只用于安全/权限边界、workspace/path escape、用户显式指定的交付路径/模板/字面锚点、恢复一致性、provider/tool 协议完整性，以及用户最新 steer 的明确约束。
- sub-agent/child-agent 是否使用必须 model-led；优化 `agent_spawn` / `agent_status` / `agent_list` 描述和 child prompt 模板来提示委派时机，不要用 runtime guard 强迫或阻止委派。
- durable project memory、todo/taskboard 和 long-run checkpoint 主要用于恢复与协作提醒；除非 finish 会留下明显过期或矛盾的 durable state，不要阻断 agent handoff 或普通执行动作。

## 安全与恢复

- 文件工具必须防止 workspace escape，并处理 symlink escape，不要只做 `Clean`/`Rel`。
- `shell` 工具必须有 timeout、输出截断和最小环境变量 allowlist。
- 中断发生在 tool call 之后时，要写入可重放的中断错误结果，避免恢复时出现 dangling tool call。
- session 目录与 agent 产物默认使用 owner-only 权限。

## 文档纪律

- root `README.md` 和本 `AGENTS.md` 保持简短、稳定、可检索。
- 详细设计写在 `spec/`，不要把实现细节堆回根说明文件。
- 不要在文档里引用当前仓库不存在的脚本或路径。

## Git 纪律

- `go-cli-agent/` 现在是独立 Git 仓库根目录；写入型改动默认在这个仓库内完成，不要依赖外层 loose workspace 的无版本状态。
- 后续任何代码修复都必须在对应验证完成后产出真实 `git commit`；不要把“已修好”的代码留在未提交状态。
- 如果一次修复同时涉及测试、`spec/`、`README.md` 或本 `AGENTS.md`，这些配套改动应和代码修复一起进入同一个 commit，保证提交能完整表达当轮变更。

## 当前阶段

- 当前阶段是“core v1 收敛态”：先把 Phase 0-10 的产品边界、provider 契约、CLI 主路径、测试和文档彻底对齐，再评估扩展 phase。
- Phase 11-16 目前视为兼容预留和实验扩展，不是默认完成标准。
- 当用户明确要求大型项目 profile、child orchestration、隔离编辑或“超越 codex/opencode”的验证时，可以推进并验证 Phase 11-13，但要保持这些能力通过显式 `experimental`/`--isolation` 入口暴露，而不是把默认 root help 重新做厚。
- 当 spec 与当前代码或用户最新指令冲突时，先修 spec，再继续实现，避免文档和实现分叉。
