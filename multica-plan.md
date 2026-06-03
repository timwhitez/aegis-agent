# Multica 与后端 Agent 调度边界方案

本轮范围：只做架构分析和方案设计，不实现代码，不调整 `go-cli-agent`、Multica 或 `codex-general` 的运行时代码。

本文面向后续实现，回答四个问题：

1. Multica 的 squad 级多 agent 调度，和 `codex-general` / `go-cli-agent` 内部 sub-agent 调度应如何分层。
2. 现有单 agent skill，尤其依赖 Codex + sub-agent 的 workflow skill，应如何改造成 Multica 可复用形态。
3. 多 agent 之间如何更好地共享文件、进度、结构化交接，并避免各自工作目录不可见。
4. issue 页面 agent 评论不按时间顺序显示的 bug 应如何定位和修复。

## 现有证据

### `go-cli-agent` 当前边界

本仓库 spec 给出的边界很明确：

- `go-cli-agent` 是 Web-first 本地 agent harness；默认入口是本地 Web 控制台，CLI 是脚本化、CI、故障恢复和高级调试 fallback。
- runtime 的事实源是 session / state / messages / events / goal / tasks / queue 等落盘文件；Web 控制台不能维护第二套权威状态。
- provider 差异应留在 adapter 层，不能扩散到 Web、CLI 或 tool 层。
- Goal / task / child / queue 是 durable 协作事实，但不能把 runtime 改造成固定 DAG、硬编码 workflow engine 或 task-specific orchestration。
- `agent_spawn` / `agent_status` / `agent_list` 属于 large-project profile 的模型主导工具；是否使用 child agent 应由模型或用户明确决定，而不是由 runtime guard 强制。
- `record_goal_progress`、`goal.json`、`artifacts/goal-history.jsonl`、`session.md`、`reports/spec.md`、`reports/plan.md`、`reports/progress.md`、`reports/validation.md` 是长任务恢复和协作的事实面，但它们是 session-local / harness-local 的事实，不天然等同于 Multica issue 级共享事实。
- `gocli-stream-json` 是 Multica 与本仓库的唯一耦合面；扩展应优先落在 `spec/multica-integration/`、`internal/streamjson` 和 CLI flag，而不是把 Multica 逻辑塞进 runtime core。

因此，`go-cli-agent` 后续不应新增 Multica 专用 DAG 或 squad 调度器。它可以接收 Multica 下发的 mission metadata、共享记录路径和角色提示，也可以产出 handoff artifact，但不能让 Multica 反向污染 core runtime / sdk facade / web app / cli adapter 的分层。

### Multica 当前能力

远端 Multica 当前路径为 `/data00/home/guangzhe.zhang/multica`，当前本地分支状态在检查时为 `main...origin/main [ahead 11]`。近期已有与本方案直接相关的能力：

- `server/internal/daemon/config.go` 已有 `SharedRecordsRoot` 与 `ResolveSharedRecordsRoot`，默认把共享记录根放在 workspaces root 旁边的 `multica_shared_records`，也支持 `MULTICA_SHARED_RECORDS_ROOT` 覆盖。
- `server/internal/daemon/daemon.go` 在 claim task 时把 `SharedRecordsDir` 注入 `execenv.TaskContextForEnv`，路径由 workspace id 与 issue id 组成。
- `server/internal/daemon/execenv/shared_records.go` 已有 `SyncSharedRecordsIntoWorkDir` 和 `PublishWorkDirSharedRecords`：目前只同步 `reports/`，冲突文件保留到 `_conflicts/`，避免并发 agent 互相覆盖。
- `server/internal/daemon/execenv/context.go` 会在 task workdir 写 `.multica/shared_records.json`，告诉 agent issue-scoped record exchange 的 host-local path、共享目录和使用注意事项。
- `server/internal/daemon/execenv/runtime_config.go` 已在 agent runtime prompt 中提示：使用 `reports/` 存放其他 agent 需要验证或继续的 durable handoff artifacts，不要放 scratch、依赖缓存、私有 runtime state 或 secret。
- `server/internal/daemon/local_skills.go` 区分 runtime-local skill 来源：`codex` / `codex-general` 从 `CODEX_HOME` 或 `~/.codex/skills` 发现，`gocli` 从 `~/.go-cli-agent/skills` 发现；任务可见 shared skills 则独立注入到 workdir 的 `./skills`。
- `server/internal/daemon/prompt.go`、`server/internal/handler/squad_briefing.go` 与 `server/internal/service/task.go` 已经把 squad leader 当作 issue/squad 协调角色：squad 是 owner，leader 代表 squad 运行，leader 根据触发评论或 issue 状态决定是否行动、委派或记录 `no_action`。

这说明 Multica 已经有“跨 runtime 的 issue 级文件交换雏形”，但还没有完整的 issue-level public workspace、结构化 progress ledger、handoff schema、UI/API 可见性和写入协议。

### `codex-general` 当前能力

`codex-general` 当前 `HEAD` 检查为 `ce6a374`。它不是普通 `codex` 的别名，而是带有独立角色系统和 sub-agent 工具的 fork：

- CLI binary 是 `codex-general`。
- `AGENTS.md` 明确基本角色是 `orchestrator`、`worker`、`validator`，默认角色是 `orchestrator`。
- `worker` 负责边界清晰的实现工作；`validator` 独立评估完成工作并报告缺口，不实现修复；`awaiter` 是长运行等待支持角色。
- `codex-rs/core/src/agent/role.rs` 内置角色描述中，`orchestrator` 负责拆解目标、推动完成、通过验证门禁，并避免积累过细上下文；`worker` 交付 well-specified feature；`validator` 只做正确性和完整性评估。
- `codex-rs/core/src/tools/handlers/multi_agents_spec.rs` 的 `spawn_agent` 说明明确：只有用户显式要求 sub-agent / delegation / parallel agent work 时才使用；阅读、深入、详细调查本身不等于授权 spawn。
- `codex-rs/core/src/thread_manager.rs` 中 subagent 是从已持久化历史 fork 出来的 thread；`codex-rs/core/src/context/subagent_notification.rs` 用 `<subagent_notification>` 把 child 状态回投给 parent 上下文。
- `codex-rs/core/src/tools/handlers/agent_jobs.rs` 还有面向批量 item 的 `spawn_agents_on_csv` / `report_agent_job_result` 模式，适合高度结构化、可并行、可 schema 化的批处理，而不适合替代 Multica 的 issue/squad 调度。

结论：`codex-general` 内部已经有一套 agent-tree / role / validator 机制。Multica 不应把它拆开重写，而应把它视为“一个可运行复杂内部编排的 backend runtime”，只在 issue/squad 边界上给它任务、上下文、共享目录和验收要求。

### 评论顺序 bug 当前证据

远端 Multica 当前 issue timeline 数据路径如下：

- 后端路由：`server/cmd/server/router.go` 的 `GET /api/issues/{id}/timeline` 调到 `server/internal/handler/activity.go:ListTimeline`。
- `ListTimeline` 取 `ListCommentsForIssue` 与 `ListActivitiesForIssue`，再用 `mergeTimeline` 按 `(created_at, id)` 排序。
- `server/pkg/db/queries/comment.sql` 的 `ListCommentsForIssue` 为 `ORDER BY created_at ASC, id ASC`。
- `server/pkg/db/queries/activity.sql` 的 `ListActivitiesForIssue` 为 `ORDER BY created_at ASC, id ASC`。
- `packages/core/api/client.ts` 的 `listTimeline` 直接请求 `/api/issues/{id}/timeline`。
- `packages/core/issues/queries.ts` 的 `issueTimelineOptions` 是单次 full timeline query。
- `packages/core/issues/timeline-sort.ts` 提供 `sortTimelineEntriesAsc(entries)`，按 `created_at` 再按 `id` 升序排序。
- `packages/views/issues/hooks/use-issue-timeline.ts` 对 WS `comment:created`、`activity:created` 等 append 更新会调用该排序 helper。
- `packages/views/issues/components/issue-detail.tsx` 在渲染前把 timeline 分成 top-level entries 与 replies；`thread-utils.ts` 的 `collectThreadReplies` 按 `repliesByParent` 中的数组顺序做 DFS。

这说明“默认后端 full timeline 完全乱序”的概率较低。更可能的问题在以下几类：

- 同一秒或同一毫秒内多个 agent comment 的 `created_at` 相同，当前 tie-break 用 UUID `id`，不等同于真实插入顺序或用户期望的 agent 完成顺序。
- 某个页面或旧客户端仍使用带 `limit` / `before` / `after` / `around` 参数的 wrapped legacy timeline shape；该路径当前返回 DESC entries，若被新 UI 当作 ASC full timeline 消费，会显示反序或局部乱序。
- 线程化渲染中，top-level 按全局 timeline 顺序，但 reply bucket 依赖输入数组；若 WS、乐观更新、编辑/删除或 legacy refetch 产生未排序 cache，`collectThreadReplies` 不会再次对子树排序。
- 某个视图误用 `comment list --recent` 或 `ListRecentThreadCommentsForIssue` 的“按最近活跃 thread 排序”结果当作 issue 页面 full chronological timeline。
- WS 与 mutation onSuccess 同时写入同一 comment，去重/替换逻辑保持了旧位置，未按 canonical sort 重排。

后续应先复现具体 issue 页面数据，而不是直接猜测改 UI 或 SQL。

## 核心架构决策

### 三层调度单位

后续系统应明确三层调度单位：

| 层级 | 所有者 | 典型对象 | 负责什么 | 不负责什么 |
| --- | --- | --- | --- | --- |
| Multica issue / squad / mission 层 | Multica | issue、child issue、squad leader task、validator task、shared records | 跨 agent 分工、路由、可见性、公共进度、跨 runtime handoff、最终验收呈现 | 不进入单个 backend agent 的 token 级决策，不强制 backend 内部 sub-agent 拆分 |
| backend agent session 层 | `go-cli-agent` / `codex-general` / 其他 runtime | 一个 task 对应的一次 agent session 或 resumed session | 读取上下文、执行工具、修改代码、使用 runtime 自己的 goal/plan/child/session store、产出结果 | 不拥有整个 Multica squad 的全局调度权威 |
| backend 内部 sub-agent / child thread 层 | backend agent 自己 | `codex-general` subagent、`go-cli-agent` child session、agent job worker | 局部并行探索、分片实现、独立验证、等待长命令 | 不成为 Multica issue 状态的唯一事实源 |

调度规则：

- Multica 应在 issue / feature / validation run 边界切分任务，而不是把一个 backend agent 的内部 todo 拆成远端微任务。
- 当一个 backend agent 能在自身上下文中高效协调时，保持内部完成；当需要隔离写入、并行专家、独立 validator、跨 runtime 能力或用户可见责任边界时，再升级到 Multica 层创建 child issue / 指派 agent / 拉起 validator。
- Multica 对 backend 内部 sub-agent 只做观测和结果接收，不替代其内部 `orchestrator -> worker -> validator` 或 `parent -> child` 决策。
- backend agent 可以在内部使用 sub-agent，但在 Multica run 边界必须写出结构化 handoff，使其他 runtime 不需要读取完整内部 transcript 才能接续。

### 避免双重编排

双重编排的坏味道：

- Multica leader 把任务拆成十几个微 issue，同时 `codex-general orchestrator` 又把每个微 issue 拆成 worker/validator，导致评论流和文件冲突成倍增加。
- Multica prompt 固定要求 “先分配 N 个 worker，再 validator”，而 backend skill 又要求 Codex 内部先 spawn explorers / workers。
- `go-cli-agent` runtime guard 根据 Multica 字段强制调用 `agent_spawn`，破坏“模型是 agent，harness 提供环境”的边界。

推荐约束：

- Multica squad policy 只定义“什么时候跨 agent”：例如需要独立验收、并行 write scopes、不同 runtime 能力、长任务分段、人工可见检查点。
- skill 只定义“什么时候建议内部委派”：例如 repo-scale 只读检索、互不重叠的代码切片、validator 不应修复。
- 若二者都可用，优先按任务边界选择其一：小而耦合的子任务走 backend 内部；跨责任、跨上下文、跨 issue 的子任务走 Multica。
- 后续可以在 Multica task payload / prompt 中增加 `delegation_boundary` 或 `coordination_mode` 字段，但它应是 guidance，不是 runtime hard guard。

## Issue 级公共共享区方案

### 目标

需要一个 issue-scoped public area，让不同 agent / runtime / squad 成员能够看到：

- 当前公共计划与验收契约。
- 谁做了什么、还剩什么、验证过什么。
- 可被其他 agent 继续使用的报告、命令输出摘要、patch 说明和 artifact。
- 结构化 handoff，供下一位 agent 或 validator 自动读取。

这个区域应补足每个 runtime 私有 workdir 的不可见性，但不能变成所有 scratch 文件、依赖目录、私有 session state 的倾倒地。

### 建议目录

基于 Multica 已有 `SharedRecordsRoot` 与 `reports/` 同步机制，建议把 issue public area 规范化为：

```text
<shared-records-root>/<workspace-id>/<issue-id>/
├── README.md
├── reports/
│   ├── plan.md
│   ├── progress.md
│   ├── validation.md
│   └── <agent-or-slice>.md
├── handoffs/
│   └── <timestamp>-<agent-id>-<role>-<task-id>.json
├── progress/
│   └── ledger.jsonl
├── validation/
│   ├── contract.json
│   └── evidence/
├── artifacts/
│   └── <stable-subdir>/
└── _conflicts/
```

短期兼容做法：

- 继续同步已有 `reports/`，避免破坏当前实现。
- 增量允许同步 `handoffs/`、`progress/`、`validation/`、`artifacts/`，并延续 `_conflicts/` 冲突保留策略。
- `.multica/shared_records.json` 从现在的 `shared_dirs: ["reports"]` 扩展为完整列表，并携带 schema version。

### 写入规则

公共区必须偏 append-only，避免 agent 互相覆盖：

- `handoffs/`：每次 run 结束写一个新 JSON 文件，文件名带 UTC timestamp、agent id、role、task id。
- `progress/ledger.jsonl`：追加 JSONL；如果本地文件复制机制难以保证 append 原子性，先采用“一条记录一个文件”的 `progress/events/<timestamp>-<task-id>.json`，由服务端/API 聚合成 ledger。
- `reports/plan.md`、`reports/progress.md`、`reports/validation.md`：允许 squad leader 或明确 owner 更新；普通 worker 优先写 `reports/<slice>.md` 和 handoff。
- `validation/evidence/`：validator 写新文件，不覆盖 worker 报告。
- `artifacts/`：只放可复用、可验证、体积受控的产物；依赖缓存、build output、大型 vendor、私有 session、secret 禁止进入。

建议 handoff JSON schema：

```json
{
  "schema_version": 1,
  "workspace_id": "...",
  "issue_id": "...",
  "task_id": "...",
  "agent_id": "...",
  "agent_name": "...",
  "runtime": "codex-general|gocli|codex|...",
  "role": "leader|worker|validator|planner|generator|evaluator|orchestrator",
  "started_at": "2026-06-03T00:00:00Z",
  "finished_at": "2026-06-03T00:00:00Z",
  "status": "completed|blocked|failed|partial",
  "summary": "...",
  "completed": ["..."],
  "remaining": ["..."],
  "changed_files": ["..."],
  "commands": [
    {"cmd": "...", "status": "passed|failed|not_run", "evidence": "..."}
  ],
  "artifacts": [
    {"path": "reports/worker-a.md", "kind": "report", "description": "..."}
  ],
  "validation": [
    {"id": "VAL-001", "status": "passed|failed|not_run", "evidence": "..."}
  ],
  "risks": ["..."],
  "next_suggested_owner": {
    "type": "squad|agent|member",
    "id": "...",
    "reason": "..."
  }
}
```

### Multica UI/API 可见性

文件同步本身不够。后续 Multica 应增加 issue 级视图：

- issue 页面显示 “Shared Records” 面板：列出 plan/progress/validation/handoffs/artifacts。
- agent run detail 显示本次 run 发布了哪些 shared records，以及 publish 是否发生冲突。
- squad leader prompt 优先读取 issue public summary，而不是只读 flat comments。
- validator 可以按 handoff schema 自动定位 changed files、commands、risks、validation ids。
- CLI 增加 `multica issue records list/get/publish` 或等价 API，避免 agent 只能靠裸路径操作。

### 与 runtime 私有事实源的关系

- `go-cli-agent` 的 `goal.json`、`goal-history.jsonl`、`session.md`、child session store 仍归 runtime 私有 session facts。
- `codex-general` 的 thread rollout、subagent notification、worker result 仍归 codex-general 内部。
- Multica issue public area 只接收“跨 agent 必须看见的摘要和证据”，不复制完整聊天、provider raw、tool transcript 或私有 token 级上下文。
- 如果后续需要 runtime 自动发布 handoff，优先通过已有 `gocli-stream-json` 可选 `handoff` 字段或进程结果 metadata 传 artifact ref，不要让 runtime 直接依赖 Multica DB 类型。

## Skill 改造方案

### 改造原则

不要把单 agent skill 简单改成“Multica 固定 workflow”。更合适的方式是让 skill 同时支持三种执行面：

- 单 agent 默认路径：一个 Codex / go-cli-agent session 自己完成。
- backend 内部 sub-agent 路径：当用户授权或任务适合，使用 `codex-general` sub-agent 或 `go-cli-agent` child。
- Multica squad 路径：当任务跨 issue、跨责任、跨 runtime、需要独立 validator 或共享公共进度时，由 Multica 分配 agent / squad。

skill 应描述边界与产物，不应强迫 runtime 调度。

### 建议 skill 结构

每个 workflow skill 建议改成：

```text
SKILL.md
references/
├── single-agent.md
├── multica-squad.md
├── handoff-schema.md
├── validator-contract.md
└── role-runbooks/
    ├── orchestrator.md
    ├── worker.md
    └── validator.md
```

`SKILL.md` 保持短：

- 触发条件。
- 能力边界。
- 单 agent 与 Multica squad 的选择规则。
- 必须产出的 public artifacts。
- 只在需要时加载 `references/` 的 progressive disclosure 指令。

`references/multica-squad.md` 写 Multica 适配：

- 什么时候保持当前 backend 内部完成。
- 什么时候请求 Multica 创建 child issue 或指派 worker。
- 什么时候请求 validator。
- 公共区应写哪些文件。
- 评论中应发什么，哪些内容只写 shared records。

`references/handoff-schema.md` 写 JSON schema 与示例。

`references/validator-contract.md` 写 validation ids、证据格式、失败如何反馈。

### 针对 Codex + sub-agent workflow skill

现有以“一个 Codex + internal subagents 完整掌控任务”为默认假设的 skill，应改为：

- 不再假设主 Codex 拥有全局唯一调度权；在 Multica 中，主 Codex 只是某个 issue task 的 backend agent。
- 将 “spawn subagents” 改成条件建议：只有用户授权、backend runtime 支持、任务边界适合、且不会和 Multica 已经分配的 worker 重复时才使用。
- 如果 Multica 已经有 squad leader / worker / validator，skill 应优先使用 issue public area 协调，而不是再让 Codex 内部创建同等角色的子代理。
- 内部 sub-agent 完成后，主 agent 必须把结果折叠成 Multica handoff；不能要求下一个 agent 去读内部 sub-agent transcript。
- validator role 的 skill 文本应明确“不修复，只评估并写缺口”，与 `codex-general` validator 和 Multica validator task 保持一致。

### 角色映射

建议建立软映射，而不是强制同名：

| Multica 角色 | `codex-general` 角色 | `go-cli-agent` role hint | 说明 |
| --- | --- | --- | --- |
| squad leader | orchestrator | planner | issue/squad 层协调，决定是否跨 agent |
| worker | worker | generator | 边界清晰的实现或调查 |
| validator | validator | evaluator | 独立评估，不修复 |
| long-run watcher | awaiter | watcher / evaluator | 等待长命令或外部状态 |

映射只用于 prompt / skill guidance / model selection。不要把它写成 runtime hard guard。

### 当前 Multica Workspace Skills 实证清单

截至本轮只读查询，远端 Multica workspace 中共有 7 个 skill。清单依据是 Multica Postgres 中的 `skill`、`skill_file`、`agent_skill`、`agent`、`agent_runtime` 表；“附加文件数”指 `skill_file` 记录数，不含 `skill.content` 中的主 `SKILL.md`。

| Skill | 来源 / 配置 | 附加文件数 | 绑定 agent 数 | 绑定形态 | 是否需要继续优化 |
| --- | --- | ---: | ---: | --- | --- |
| `authorized-security-playbook` | runtime-local import，`~/.codex/skills/authorized-security-playbook` | 15 | 10 | `安全评估*` 的 `codex-general` agents + `CyberSec Long Horizon*` 的 `gocli` agents | 需要，中等优先级 |
| `code-audit-knowledge` | runtime-local import，`~/.codex/skills/code-audit-knowledge` | 8 | 10 | `代码审计*` 的 `codex-general` agents + `CyberSec Long Horizon*` 的 `gocli` agents | 需要，高优先级 |
| `cybersec-long-horizon-mission` | workspace shared，`scope=workspace_shared`，`runtime=gocli` | 2 | 4 | 只绑定 `CyberSec Long Horizon*` 的 4 个 `gocli` agents | 需要，最高优先级 |
| `lark-markdown-upload` | runtime-local import，`~/.codex/skills/lark-markdown-upload` | 1 | 0 | 当前未绑定 agent | 轻量优化即可 |
| `pentest-toolset` | runtime-local import，`~/.codex/skills/pentest-toolset`，含本地 CLI bundle | 11 | 10 | `安全评估*` 的 `codex-general` agents + `CyberSec Long Horizon*` 的 `gocli` agents | 需要，高优先级 |
| `security-validation-operations` | runtime-local import，`~/.codex/skills/security-validation-operations` | 7 | 10 | `安全评估*` 的 `codex-general` agents + `CyberSec Long Horizon*` 的 `gocli` agents | 需要，高优先级 |
| `timwhite-v2-codex-security-review` | runtime-local import，`~/.codex/skills/timwhite-v2-codex-security-review` | 8 | 4 | `代码审计Master/Validator` 的 `codex-general` + `CyberSec Long Horizon Master/Validator` 的 `gocli` | 需要，高优先级 |

观察：

- 多数 skill 已经补过一个简短的 `Multica Team Mode` 段落，但它们仍主要是“单 agent skill + 团队模式提示”。缺口是缺少可执行的角色入口、公共区写入路径、handoff schema、validation contract 和“何时内部 sub-agent、何时 Multica worker”的明确分界。
- `CyberSec Long Horizon*` 这组 `gocli` agents 同时绑定了 6 个安全类 skills，容易让 master/worker/validator 在同一轮加载过多 reference。需要一个更强的 mission router，把公共 mission state 与每个专业 skill 的最小引用集连接起来。
- `代码审计*`、`安全评估*` 的 `codex-general` agents 自身具备 `orchestrator/worker/validator` 内部架构；这些 skill 不能继续暗示“主 Codex 必须自己 spawn 内部 workers 完成全流程”，应明确在 Multica task 中只负责本 agent 被分配的边界。
- `lark-markdown-upload` 是发布辅助 skill，当前未绑定 agent，不需要多 agent 调度，但需要知道从 issue public area 读取最终报告，并把上传结果写回 public handoff。

### 每个 Skill 的具体优化方案

#### `cybersec-long-horizon-mission`

定位：这是当前最接近 Multica 架构的 workspace-native skill，应成为 squad mission 的顶层协议，而不是普通安全知识库。

需要优化：

- `SKILL.md` 需要增加 “Issue Public Area Contract” 段落，明确每个 issue 必须维护 `reports/mission_brief.md`、`reports/progress.md`、`reports/validation.md`、`handoffs/*.json`、`progress/events/*.json` 或 `progress/ledger.jsonl`。
- `templates/structured-handoff.md` 太偏人工 Markdown，缺少机器可读字段；应新增 `templates/structured-handoff.schema.json`，字段与本文 handoff JSON schema 对齐。
- `templates/validation-contract.md` 应拆成 master 初始 contract、worker slice acceptance、validator gate 三种模板，避免所有角色共用一个模糊模板。
- 增加 `references/role-master.md`、`references/role-worker.md`、`references/role-validator.md`：master 只负责拆分/派发/综合；worker 只做 bounded slice；validator 只做 creator-verifier gate。
- 增加 “Delegation Boundary” 小节：优先使用 Multica worker/validator 承担跨责任边界；只在单个 backend agent 内部需要只读并行搜索或局部实现辅助时，才允许 `codex-general` 或 `go-cli-agent` 使用内部 sub-agent。

公共区写入方案：

- Master 初始化 `reports/mission_brief.md`、`validation/contract.json`、`reports/progress.md`。
- Worker 写 `handoffs/<timestamp>-worker-<task>.json` 和 `reports/<milestone>_<worker>_handoff.md`。
- Validator 写 `validation/evidence/<milestone>_gate.json` 和 `reports/<milestone>_validator_gate.md`。

#### `timwhite-v2-codex-security-review`

定位：完整代码安全审计 workflow。它当前最容易和 Multica squad 调度发生“双重编排”，因为原 workflow 已内置 15 步、delegation runtime、worker receipts 和 final audit gate。

需要优化：

- `references/delegation-runtime.md` 需要增加 Multica override：当运行在 Multica squad task 中时，`Planned Delegated Slices` 里的 “delegated” 必须先解释为 Multica worker/validator assignment；只有在当前 task 明确授权内部 sub-agent 且不会重复 Multica 分工时，才解释为 Codex sub-agent。
- `references/full-audit-workflow.md` 的 round bootstrap 应支持 issue public area：`.context/security-review/<round-id>/` 可保留为 backend 私有工作区，但跨 agent 必须同步摘要到 `reports/code-audit/<round-id>/`、`handoffs/` 和 `validation/evidence/`。
- `templates/audit-rounds-template.md` 的 `Planned Delegated Slices` 表增加列：`delegation_layer`（`multica-agent|backend-subagent|local-bounded`）、`public_handoff`、`validator_gate`。
- `templates/final-audit-gate-template.md` 增加 Multica gate：最终 PASS 前必须检查 issue public area 中每个 claimed slice 是否存在 handoff、每个 confirmed finding 是否有 validator evidence。
- `SKILL.md` 的 `Multica Team Mode` 已有方向，但需要把 “Master/Worker/Validator” 绑定到实际 Multica roles，而不是泛化文字。

公共区写入方案：

- Master 写 `reports/code-audit/round-<id>/master-state.md`、`coverage-matrix.md` 摘要和 `validation/contract.json`。
- Worker 写 `handoffs/<round>-<slice>-worker.json`，并把原 `subagent-results/*.receipt.json` 映射成 public handoff。
- Validator 写 `validation/evidence/<finding-id>.json`，最终 gate 写 `reports/code-audit/final-audit-gate.md`。

#### `code-audit-knowledge`

定位：轻量/通用白盒审计知识库，适合作为 `timwhite-v2` 的专业知识层，也可单独用于窄范围审计。

需要优化：

- `references/00-audit-workflow.md` 增加 Multica operating modes：`master-scope-map`、`worker-flow-slice`、`validator-finding-gate`、`single-agent-narrow-review`。
- `references/03-dataflow-and-evidence.md` 增加 public handoff 字段：entry point、source、sink、guards、evidence refs、false-positive reasoning、confidence、remaining checks。
- `references/07-reporting-and-validation.md` 增加 validator verdict schema，统一 `confirmed|needs_more_evidence|unverified|false_positive|duplicate|out_of_scope`。
- `SKILL.md` 的 Team Mode 已明确 creator-verifier separation，但还需说明绑定到 `代码审计Worker*` 时默认不要审全仓库，必须等待 master 的文件/flow/family slice。

公共区写入方案：

- Worker flow slice 写 `handoffs/code-audit-<slice>.json`。
- Validator 对每个 candidate 写 `validation/evidence/<candidate-id>.json`。
- Master 将确认结果汇总进 `reports/security-review.md` 或 issue-level `reports/code-audit-summary.md`。

#### `authorized-security-playbook`

定位：授权安全评估和报告 playbook，覆盖面广，主要负责安全边界、路由、证据和报告语言。

需要优化：

- `references/00-safety-and-scope.md` 增加 Multica scope owner：master 是唯一可以扩大 scope/ROE 的角色；worker 遇到 scope 不清只能写 blocker handoff，不自行扩大。
- `references/01-routing-map.md` 增加 Multica routing：将 broad target 拆为 reconnaissance/scope、identity/API、business logic、input/content、cloud/infra、reporting 等 worker slice，但每个 slice 必须有 ROE 和 evidence requirements。
- `references/08-reporting-and-evidence.md` 增加 issue public area report bundle：finding draft、evidence ledger、reproduction safety notes、validator verdict、final business wording。
- `SKILL.md` 的 Team Mode 已可用，但应补 “what each role may write”：worker 写 candidate，validator 写 verdict，master 写 final risk statement。

公共区写入方案：

- Master 写 `reports/assessment-scope.md` 和 `validation/contract.json`。
- Worker 写 `handoffs/security-assessment-<surface>.json`。
- Validator 写 `validation/evidence/<finding-or-surface>.json`。
- Final report 写 `reports/authorized-security-report.md`。

#### `pentest-toolset`

定位：带 `ai-pentest-cli` 的低影响 Web/API 安全评估工具 skill。它最需要避免 “CLI 自己变成调度器”。

需要优化：

- `SKILL.md` 已说 CLI 不是 autonomous scheduler；需要把这条提升为 hard guidance：Multica issue/task 是唯一跨 agent 调度层，`ai-pentest-cli` 只负责 scope check、state、artifact、request/replay/fuzz 证据。
- `references/02-scope-and-state.md` 增加 Multica namespace 规范：每个 issue/slice 使用 `issue_id + task_id + role` 作为 CLI state namespace，避免多个 worker 写同一 state。
- `references/06-artifacts-compaction.md` 增加 public artifact 发布规则：CLI 原始 artifact 留在私有 workdir，public area 只放摘要、hash、路径引用、可复现命令和必要脱敏证据。
- `references/07-test-cases.md` 增加 validator replay boundary：validator 只 replay 证明/反驳结论所需的低影响请求，不扩展 fuzz。
- `manifest.json` 应增加 bundle version 与 compatibility note，说明 `scripts/ai-pentest-cli` 在 Multica isolated workdir 中的预期路径。

公共区写入方案：

- Worker 每个 hypothesis 写 `handoffs/pentest-<hypothesis>.json`，其中列出 CLI state refs 与 artifact hashes。
- Validator 写 `validation/evidence/pentest-<finding>.json`。
- Master 写 `reports/pentest-progress.md` 与最终 `reports/pentest-findings.md`。

#### `security-validation-operations`

定位：防御监控、检测工程、威胁狩猎、purple-team 验证和安全运营证据组织。

需要优化：

- `references/00-safety-and-engagement.md` 增加 Multica “engagement lock”：master 锁定 ROE、telemetry sources、impact limits；workers 不得改变采集源或触发方式。
- `references/01-routing-map.md` 增加 role routing：detection worker、hunt worker、cloud/identity worker、validation worker、validator 的职责边界。
- `references/05-evidence-reporting.md` 增加 shared evidence ledger schema：telemetry source、query/rule、time window、observation、negative checks、control gap、owner-ready remediation。
- `SKILL.md` 的 validator 描述应进一步绑定到 Multica validator：验证安全结论，不验证普通代码质量。

公共区写入方案：

- Worker 写 `reports/security-ops/<slice>.md` 和 `handoffs/security-ops-<slice>.json`。
- Validator 写 `validation/evidence/security-ops-<assertion>.json`。
- Master 维护 `reports/security-ops-ledger.md` 和 `reports/security-ops-summary.md`。

#### `lark-markdown-upload`

定位：发布辅助 skill，不是安全工作流，也不需要多 agent 分工。

需要优化：

- `SKILL.md` 增加 Multica public area input rule：默认从 issue public `reports/` 中选取 master-approved final report，不上传 worker draft、validator draft 或 `_conflicts/` 文件。
- 增加 `references/multica-publish.md`，说明如何读取 `handoffs/` 或 `reports/final-*`，如何把 `document_id`、`revision_id`、URL、上传时间写回 `handoffs/publish-<timestamp>.json`。
- 保留单 agent 命令路径；无需引入 worker/validator 角色。

公共区写入方案：

- 发布 agent 写 `handoffs/publish-feishu-doc.json`。
- 如需用户可见，issue comment 只贴最终文档 URL 和 source report path。

### Skill 改造优先级

1. `cybersec-long-horizon-mission`：先把公共区、角色、handoff、validator contract 固化，因为它是其他安全 skill 在 Multica 中的总协议。
2. `timwhite-v2-codex-security-review`、`pentest-toolset`：这两个最复杂，最容易发生双重编排或状态冲突。
3. `code-audit-knowledge`、`authorized-security-playbook`、`security-validation-operations`：补齐 role-specific reference、public evidence schema、validator verdict。
4. `lark-markdown-upload`：作为发布辅助，最后做轻量 public-area 适配。

验收标准：

- 每个已绑定 agent 的 skill 都能回答：当前角色是谁、读哪个 reference、写哪个 public artifact、何时请求 Multica worker/validator、何时允许 backend 内部 sub-agent。
- 每个 workflow skill 都有 machine-readable handoff 或 validator contract 模板。
- 同一 issue 中，不同 agent 不能因为 skill 默认路径而写同名 report；所有 worker/validator 输出文件名必须包含 milestone/slice/agent 或 task id。

## 长上下文与恢复策略

长任务不能依赖任何一个聊天上下文完整保留。推荐事实分层：

- Multica issue public area：跨 agent 最新计划、进度、handoff、验证证据。
- Multica DB：issue、comment、activity、task queue、squad assignment、agent run 状态。
- backend runtime session store：本 agent 内部历史、tool events、goal / child / compaction / checkpoint。
- comments：面向用户和 agent 的可读通知，不承载全部结构化事实。

每次 Multica run 边界必须留下：

- 一条 issue comment：简短说明结果、阻塞或下一步。
- 一个 handoff JSON：结构化、机器可读。
- 必要的 report / validation artifact：供人读和 validator 查证。

backend compaction 只改变模型上下文视图，不能覆盖原始日志；Multica handoff 只发布摘要和证据，不能替代 runtime 内部事实。

## 评论顺序 bug 修复计划

### 指定 Issue 复现证据

用户给出的页面是 `http://10.37.226.142:3000/local-multica/issues/90d0746e-8049-4326-b98a-aa72f63564bd`。只读 DB 查询显示：

- issue 标题为 `授权渗透测试：itsec-iac.bytedance.net 全站安全评估`，状态 `in_progress`，assignee 是 squad。
- 该 issue 当前有 23 条 comments、19 条 activities。
- comments 中只有 2 条 root comments，21 条是 replies。
- 按 DB canonical 顺序 `ORDER BY created_at ASC, id ASC` 看，所有 comment 时间是递增的，没有同 timestamp comment。
- 第一条 root comment 是 16:02:49 的 member delegation comment；它的线程包含 22 条 comment，时间跨度从 16:02:49 到 17:03:14。第二条 root comment 是 16:02:59 的 agent summary comment，只有自己一条。
- 前端 `issue-detail.tsx` 会先取 top-level comments / activities，再把 replies bucket 到其 root comment 下，`CommentCard` 收到 `replies={timelineView.threadReplies.get(item.id)}` 后在卡片内渲染整棵回复线程。

因此，这个 issue 的“顺序混乱”不是后端 flat timeline 没按时间排序，而是产品语义冲突：

- 数据层是全局时间线。
- UI 层把 replies 归到 root comment 下，导致 16:16 到 17:03 的大量 agent reply 会视觉上出现在 16:02:49 root comment 的卡片内部。
- 紧接着 16:02:59 的 root comment 可能显示在这一大段 thread 之后。用户从全局时间角度看，会认为 16:02:59 的 comment 被错误排到 17:03 后面。

这类页面对于 agent-heavy issue 尤其明显，因为 Multica prompt 当前要求 comment-triggered agent 总是用 `--parent <trigger_comment_id>` 回复；长任务会形成一条很深/很长的主线程。

### 针对该 Issue 的修复方向

这个 bug 应作为 “issue timeline 默认视图不应被长 reply thread 破坏全局时间顺序” 修复，而不是只修 SQL 排序。

推荐产品/技术方案：

1. 保留当前 threaded conversation 能力，但 issue 默认 timeline 应按全局时间顺序展示每个 comment/event。
   - root comment 和 reply 都作为独立 timeline row 出现在自己的 `created_at` 位置。
   - reply row 显示 “replying to <parent author/time>” 或短 parent context，而不是完整嵌入到 root card 里。
   - 点击 thread/replies 才展开 thread drawer 或局部 threaded view。
2. 对于当前 `CommentCard` 的 inline replies：
   - 只显示最近 N 条 reply 或 collapsed summary，不能让 root comment 的 card 吃掉后续一小时的全局 timeline。
   - `ResolvedThreadBar` 可继续用 `collectThreadReplies`，但默认 timeline browsing 不应把全量 descendants 内联。
3. 增加一个显式 view toggle：
   - `Chronological`：默认，所有 comments / activities 全局按时间排序。
   - `Threaded`：按 root thread 分组，适合阅读单个讨论串。
   - `Agent progress`：可选，把 worker/validator handoff 从 comments 中抽取到 progress lane。
4. `BuildCommentReplyInstructions` 的 `--parent` 仍然正确，因为它保证 reply 触发语义和通知上下文；不要为了页面顺序移除 parent_id。修复应在展示模型上做，而不是破坏评论线程数据。
5. 后端 API 可保留 flat `/timeline`；前端增加一个 `timelineViewMode`：
   - chronological 模式使用 flat `timeline` 原数组直接渲染 comment rows，reply 只带 parent hint。
   - threaded 模式才构建 `threadReplies` 并传给 root `CommentCard`。

对应测试：

- `issue-detail.test.tsx` 增加复现 fixture：root A 16:02:49，root B 16:02:59，reply A1 16:16，reply A2 17:03。默认 chronological 视图 DOM 顺序必须是 A、B、A1、A2。
- 增加 threaded 视图测试：切到 threaded 时 A card 可以包含 A1/A2，B 仍按 root 时间排。
- `use-issue-timeline.test.tsx` 保留 cache ASC 测试；另加 parent_id 不影响 chronological row order。
- 后端 `activity_test.go` 继续覆盖 flat timeline ASC，避免未来误改为 thread order。

### 复现和定位

下一轮代码修复前先做以下核查：

1. 找到实际乱序 issue，导出：
   - `GET /api/issues/<id>/timeline` 原始响应。
   - 浏览器 React Query cache 中 `["issues","timeline", issueId]` 的最终数组。
   - 页面实际 DOM 顺序。
   - 相关 comments 的 `created_at`、`id`、可能的 task id / author id。
2. 同时检查是否带了 `limit` / `before` / `after` / `around` 参数；如果有，则确认 UI 是否错误消费了 wrapped DESC shape。
3. 检查乱序是否只发生在 replies/thread 内，还是 top-level comments 全局乱序。
4. 检查是否只发生在 WebSocket 增量更新后，刷新页面后是否恢复；若刷新恢复，bug 在前端 cache update；若刷新仍乱，bug 在 API / DB ordering 或 tie-break。

### 可能修复点

优先级从高到低：

1. 后端 canonical ordering：
   - 保持 `ListTimeline` flat response ASC。
   - 若同 `created_at` comment 需要真实插入顺序，不应继续用 UUID tie-break；可考虑引入 `comment.sequence`、`activity.sequence`、或统一 `timeline_sequence`。
   - 所有 timeline response shape 明确标注 order；legacy wrapped DESC 不应被新客户端误用。
2. 前端 cache canonicalization：
   - `issueTimelineOptions` 的 query result 可在 `select` 中统一 `sortTimelineEntriesAsc`，防御后端或 legacy shape 漂移。
   - `useIssueTimeline` 的所有 append / replace / optimistic success 路径都应保持去重后排序。
3. thread rendering：
   - 在 `issue-detail.tsx` 构造 `repliesByParent` 后，对每个 parent 的 reply list 进行同一 comparator 排序。
   - `collectThreadReplies` 保持 DFS，但输入 children 必须 canonical sorted。
4. API schema / client：
   - `listTimeline` 如果收到 wrapped response，应显式 unwrap 并按期望 order 转换，或直接拒绝 unexpected shape，避免静默 fallback 成空数组或错误顺序。
5. 测试：
   - 后端 `activity_test.go` 增加同 timestamp 多 comment / activity 的稳定排序测试。
   - 前端 `timeline-sort` 增加同 timestamp tie-break 测试。
   - `use-issue-timeline.test.tsx` 增加 WS 乱序到达但 cache ASC 的测试。
   - `issue-detail.test.tsx` 增加 nested replies 乱序输入仍按 canonical order 渲染的测试。
   - 若引入 sequence，增加 migration / generated query / API schema 回归测试。

本 bug 不建议只靠 CSS 或渲染层局部倒序修复；必须保证 API、cache、thread grouping 使用同一个排序契约。

## 后续代码改动清单

### Multica

必须或高价值改动：

- 扩展 `server/internal/daemon/execenv/shared_records.go`：从只同步 `reports/` 扩展到 `reports/`、`handoffs/`、`progress/`、`validation/`、`artifacts/`，并保持 `_conflicts/` 策略。
- 扩展 `server/internal/daemon/execenv/context.go` 与 `runtime_config.go`：`.multica/shared_records.json` 增加 schema version、shared dirs、handoff schema hint、写入规则。
- 增加 issue records API / CLI：`multica issue records list/get/publish` 或等价命令，避免 agent 只能裸写路径。
- issue 页面增加 shared records / handoffs / validation evidence 面板。
- squad leader briefing 增加公共区摘要读取策略：优先读 plan/progress/latest handoffs，再读必要 thread comments。
- skill import / shared skills UI 增加 Multica adaptation metadata，例如 `multica.roles`、`multica.shared_artifacts`、`multica.handoff_schema`、`multica.delegation_boundary`。
- task payload / prompt 可增加 soft guidance：`run_role`、`delegation_boundary`、`expected_public_artifacts`、`validation_contract_ref`。
- 修复评论顺序 bug，并覆盖后端 timeline、前端 query/cache、thread rendering、WS append 测试。

### `go-cli-agent`

当前不需要把 Multica 调度写入 core runtime。可能需要的最小改动：

- 在 `spec/multica-integration/` 增加 mission/public-records 扩展文档，说明 `gocli-stream-json` 如何传递 optional metadata / handoff artifact ref。
- 如果 Multica 仅靠文件同步不足以可靠采集结果，可扩展 `internal/streamjson` 的 final `result`，增加可选 `handoff` 字段，包含 public artifact refs；consumer 必须忽略未知字段，保持向后兼容。
- `exec` prompt / AGENTS context 可以接收 `.multica/shared_records.json`，但 runtime 不需要理解 Multica DB。
- 不新增 `go-cli-agent` 内部 squad scheduler，不把 `agent_spawn` 变成 Multica 强制行为，不改变默认 Web-first 页面。

### `codex-general`

当前不需要替换内部 multi-agent 架构。可能需要的最小改动：

- 补充 Multica-aware skill / role guidance：当运行在 Multica issue task 中时，内部 sub-agent 结果必须折叠为 public handoff。
- 若 Multica 无法捕获 `codex-general` 内部 sub-agent 输出，可在 process result 或 final message 约定 handoff artifact path。
- 对 `orchestrator` / `worker` / `validator` 的 prompt 只做 Multica public area 使用说明，不改变角色本质。
- 不把 Multica squad worker 映射成 codex-general 内部 worker 的硬编码流程；二者是不同层级。

## 分阶段落地

### Phase A：文档与契约

- 在 Multica 侧写 `docs/` 或 spec：issue public area、handoff schema、skill adaptation。
- 在 `go-cli-agent/spec/multica-integration/` 写 optional handoff / metadata 协议扩展。
- 选择 1-2 个现有 workflow skill 做 Multica 改造样例。

验收：

- 新 skill 在单 agent 和 Multica squad 两种模式下都有清晰指令。
- handoff JSON 示例可被 validator 读取并定位 changed files / commands / risks。

### Phase B：共享记录产品化

- 扩展 shared dirs。
- 增加 issue records API / CLI。
- issue 页面显示 shared records。
- agent run detail 显示 publish / conflict 状态。

验收：

- 两个不同 runtime 对同一 issue 写 handoff，不互相覆盖。
- 后续 agent task 能在 workdir 看见前一 agent 的 public artifacts。
- UI 能显示 latest handoffs 和 validation evidence。

### Phase C：skill 与 squad policy

- skill import 支持 Multica metadata。
- squad leader briefing 按 public area + thread comments 组织上下文。
- validator task 根据 handoff schema 自动生成检查清单。

验收：

- 一个 workflow skill 能在 `codex-general` 和 `gocli` runtime 下复用。
- Multica leader 能决定保持 backend 内部完成，或升级为 child issue / validator。

### Phase D：评论顺序修复

- 按上文复现路径确认根因。
- 修复 API/cache/thread rendering 中的实际缺口。
- 增加后端与前端回归测试。

验收：

- 同一 issue 中多个 agent 近同时评论，刷新前后顺序一致。
- WS 乱序到达不改变最终显示顺序。
- thread replies 与 top-level timeline 使用同一 canonical comparator 或明确的 sequence。

## 关键非目标

- 不把 Multica 写进 `go-cli-agent` core runtime。
- 不把 `go-cli-agent` 默认 Web 页面改成 Multica squad 控制台。
- 不让 Multica 读取或依赖 backend provider raw transcript。
- 不要求所有 skill 都改成多 agent workflow；普通单 agent skill 仍应可直接运行。
- 不让评论成为唯一进度事实源；评论是通知和讨论，公共 progress / handoff / validation artifact 才是可恢复事实。
- 不用 runtime guard 强迫 sub-agent；委派仍应由模型、用户或 Multica issue/squad 边界共同决定。
