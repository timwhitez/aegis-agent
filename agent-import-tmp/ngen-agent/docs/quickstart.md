# Quickstart

这个文档面向第一次进入 `ngen-agent/` 的操作者，目标是 5 到 10 分钟内完成三件事：

1. 构建 `ngen`
2. 在一个临时 workspace 内跑通最小任务
3. 知道什么时候该看哪份 owner doc

## 1. Prerequisites

准备条件：

- Go 1.24.2+
- Bash
- 一个可写 workspace
- 如果要用远端 provider，还需要 `OPENAI_API_KEY`

默认情况下，`ngen` 以当前工作目录作为 target workspace，并把所有 durable state 写到当前目录下的 `.ngen/`。

## 2. Build

在仓库根目录执行：

```bash
./build.sh
```

产物默认会写到：

```text
./bin/ngen
```

如果你只想单独跑一个阶段：

```bash
./build.sh fmt
./build.sh test
./build.sh build
```

## 3. Builtin Provider Smoke Test

这是最稳妥的第一次运行方式，不依赖外部网关。

```bash
ROOT=$(pwd)
./build.sh build

WORKDIR=$(mktemp -d)
cd "$WORKDIR"

cat > go.mod <<'EOF'
module example.com/demo

go 1.24.2
EOF

cat > demo.go <<'EOF'
package demo

func Add(a, b int) int { return a + b }
EOF

TASK_ID=$("$ROOT/bin/ngen" task create \
  --kind coding \
  --title "builtin smoke" \
  --objective "verify builtin coding loop" \
  --criterion "go test passes")

"$ROOT/bin/ngen" auto "$TASK_ID" --json
"$ROOT/bin/ngen" status "$TASK_ID" --json
"$ROOT/bin/ngen" harness eval "$TASK_ID" --json

MISSION_ID=$("$ROOT/bin/ngen" mission create \
  --title "mission smoke" \
  --objective "run a validation-contract backed coding task" \
  --criterion "go test passes")
"$ROOT/bin/ngen" mission approve "$MISSION_ID" --json
"$ROOT/bin/ngen" mission run "$MISSION_ID" --json
"$ROOT/bin/ngen" mission status "$MISSION_ID" --json
"$ROOT/bin/ngen" mission validate "$MISSION_ID" --json
```

成功时你会看到：

- `auto --json` 输出结构化 events
- `status --json` 最终进入 `Done`
- `harness eval --json` 返回最近一次 pass 的 provider/context/repair/review/completion snapshot
- `mission approve --json` 返回已批准的 validation contract ref；`mission run/status/validate --json` 返回 mission contract、root task status 与 latest validation run
- workspace 内生成 `.ngen/`

重点产物：

- `.ngen/project/project.json`
- `.ngen/tasks/<task_id>/task.json`
- `.ngen/tasks/<task_id>/state.json`
- `.ngen/tasks/<task_id>/events.jsonl`
- `.ngen/tasks/<task_id>/verification/latest.json`
- `.ngen/tasks/<task_id>/reviews/latest.json`
- `.ngen/tasks/<task_id>/completion/latest.json`
- `.ngen/tasks/<task_id>/harness/latest.json`
- `.ngen/tasks/<task_id>/harness/history.jsonl`
- `.ngen/tasks/<task_id>/handoff.md`
- `.ngen/missions/<mission_id>/mission.json`
- `.ngen/missions/<mission_id>/validation_contract.json`
- `.ngen/missions/<mission_id>/features.json`
- `.ngen/missions/<mission_id>/milestones.json`
- `.ngen/missions/<mission_id>/validation_runs.jsonl`
- `.ngen/roles/<role_id>.json`

## 4. Remote Provider Setup

如果你要把 provider 切到远端网关，在 target workspace 下放一个 `ngen.json`：

```json
{
  "provider": {
    "mode": "openai-response",
    "base_url": "http://69.63.215.40:24634/v1",
    "api_key_env": "OPENAI_API_KEY",
    "model": "gpt-5.4",
    "auto_run_max_turns": 1,
    "decision_timeout_seconds": 30
  },
  "subagents": {
    "workspace_isolation": "auto",
    "auto_release_on_success": true,
    "max_lineage_depth": 2,
    "role_policies": {
      "coding": {
        "allowed_worker_roles": ["coding", "general_execution", "reviewer", "security_review"]
      },
      "reviewer": {
        "workspace_isolation": "snapshot_copy",
        "auto_release_on_success": false
      }
    }
  }
}
```

然后设置：

```bash
export OPENAI_API_KEY=your-key
```

说明：

- `mode=openai-comp`、`mode=openai-response`、`mode=anthropic` 都支持同一套 OpenAI-compatible `base_url`
- 如果你只想先验证 wiring，先把 `auto_run_max_turns` 设成 `1`
- 如果 provider 不可用，错误会显式返回，不会 silent fallback
- 当前真正会自动改 workspace 代码的自治写路径已经覆盖全部当前 provider mode：`builtin`、`command`、`openai-response`、`openai-comp`、`anthropic`
- `subagents.workspace_isolation=auto` 会在非 git workspace 上使用 `snapshot_copy`，在 git workspace 上优先使用 `git_worktree`；isolated child spawn 时会额外写出 `worker_runtime/*.baseline.json`，accepted child 还会写出 `worker_runtime/*.reconcile.json`
- `subagents.max_lineage_depth=2` 会把当前 child tree 限制在 root -> child -> grandchild 这一级；child task 与 `workers/*.json` 会显式带出 `root_task_id`、`lineage_depth` 与 `subagent_policy`
- `subagents.role_policies.<role>` 可以对单个 child role 覆盖 `permission_mode_id`、`workspace_isolation`、`reconcile_mode`、`auto_release_on_success`、`allow_child_workers`、`allowed_worker_roles` 与当前 depth budget
- `coding` / `general_execution` child 在 accepted settlement 且 parent 没有在同一路径上漂移时，会把 isolated child 文件变更自动折回 parent workspace；`reviewer` / `security_review` child 只记录 reconcile truth，不自动回写
- reconcile 冲突时，runtime 会显式把 `workers/*.json` / `worker_snapshot` 标成 `reconcile_status=conflict`，并保留 child workspace 供 parent inspect / takeover
- `builtin` 走本地 deterministic repair engine，`command` 走同一条 stdin JSON contract，远端 provider 继续走模型驱动 repair
- 成功时 task 目录会追加 `workspace_edits.jsonl`；若 repair 前需要额外观察，还会按需追加 `command_runs.jsonl`。当前默认最多执行 3 次 bounded edit + re-verify；repair target 既可以是 verifier failure，也可以是 verifier 已通过后仍未满足的 workspace-backed success criteria，其中既包括显式 path / glob criterion，也包括带 readme/docs/config 语义和具体 token 的 criterion，并在同一 repair target 重复或 task constraint 被违反时显式停止

## 5. Useful First Commands

创建任务：

```bash
ngen task create --kind coding --title "..." --objective "..." --criterion "..."
ngen task list --json
ngen task get TASK-... --json
ngen task update TASK-... --plan-file ./plan-update.json --json
ngen task patch TASK-... --patch-file ./plan-patch.json --json
ngen project get --json
ngen project update --project-file ./project-update.json --json
ngen project patch --patch-file ./project-patch.json --json
```

执行和查看状态：

```bash
ngen run TASK-...
ngen resume TASK-...
ngen auto TASK-... --json
ngen status TASK-... --json
ngen harness eval TASK-... --json
ngen mission "normal prompt describing the objective"
ngen goal "normal prompt describing the objective"
ngen mission create "normal prompt describing the objective" [--criterion "..."]...
ngen mission create --title "..." --objective "..." [--criterion "..."]...
ngen mission approve MIS-... --json
ngen mission run MIS-... --json
ngen mission validate MIS-... --json
ngen events tail TASK-... --json --limit 20 [--after EVT-...]
ngen terminal TASK-...
ngen tui [TASK-...] [--inline]
```

输入/审批/等待：

```bash
ngen input request TASK-... --prompt "..." --field target_path
ngen input respond TASK-... --request INP-... --value "..."
ngen approval request TASK-... --scope "..." --reason "..."
ngen approval ls TASK-... [--owned]
ngen approve TASK-... --request APR-...
ngen watch set TASK-... --interval 5m --reason "..."
ngen scheduler tick --once
```

多 worker / ACP：

```bash
ngen worker spawn TASK-... --role reviewer|security_review|coding|general_execution --objective "..."
ngen worker ls TASK-...
ngen worker sync TASK-... WKR-...
ngen worker continue TASK-... WKR-...
ngen memory show
ngen memory promote TASK-... --summary "..." [--kind milestone|decision|blocker|note] [--ref REF]...
ngen acp serve
```

## 6. What ACP Exposes Right Now

当前 ACP 是 stdio JSON-RPC bridge，已稳定暴露：

- `task.list`
- `task.get`
- `task.update`
- `task.patch`
- `project.get`
- `project.update`
- `project.patch`
- `memory.show`
- `memory.promote`
- `status_snapshot`
- `session_snapshot`
- `worker_snapshot`
- `permission.request` / `permission.list` / `permission.decide`
- `worker.continue`
- mutating call 后的 per-request `ngen.notification`

当前还没有：

- 长连接订阅语义
- replay stream

## 7. Which Doc To Read Next

按问题类型读：

- 想知道当前到底哪些东西已经生效： [docs/00-repo-status.md](/mnt/c/Users/Admin/Desktop/build/agent-coding/ngen-agent/docs/00-repo-status.md)
- 想看任务状态机和 blocker 边界： [docs/04-runtime/lifecycle-and-state.md](/mnt/c/Users/Admin/Desktop/build/agent-coding/ngen-agent/docs/04-runtime/lifecycle-and-state.md)
- 想看 artifacts、snapshot、ACP object schema： [docs/05-artifacts-and-context/task-lifecycle-artifacts.md](/mnt/c/Users/Admin/Desktop/build/agent-coding/ngen-agent/docs/05-artifacts-and-context/task-lifecycle-artifacts.md)
- 想看 CLI / ACP / package layout： [docs/06-go-package-and-api/package-layout-and-cli.md](/mnt/c/Users/Admin/Desktop/build/agent-coding/ngen-agent/docs/06-go-package-and-api/package-layout-and-cli.md)
- 想看 verifier、review、done gate： [docs/07-verification-security-and-ops/verification-review-and-waivers.md](/mnt/c/Users/Admin/Desktop/build/agent-coding/ngen-agent/docs/07-verification-security-and-ops/verification-review-and-waivers.md)

## 8. Common Mistakes

- 不要在 repo 根目录直接把 `.ngen/` 当成唯一目标；`ngen` 的 target workspace 是你运行命令时的当前目录。
- 不要把 provider 配置写进 `ngen-agent/` 仓库本身，再拿它去操纵别的 workspace；更稳妥的做法是在目标 workspace 内放 `ngen.json`。
- 不要把 session 输出当成系统真相；task truth 永远回看 `.ngen/` artifacts。
- 如果任务卡在 `Blocked`，先看 `status_reason_code` 和 `status_detail_ref`，不要直接猜。
