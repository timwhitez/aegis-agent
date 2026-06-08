# `.ngen/` 布局与 ownership

## 1. 当前实现布局

当前实现冻结以下目录：

```text
.ngen/
  project/
    project.json
    project_updates.jsonl
  missions/
    MIS-.../
      mission.json
      validation_contract.json
      features.json
      milestones.json
      validation_runs.jsonl
      notes.md
  runtime/
    scheduler.lock
  roles/
    coding.json
    general_execution.json
    reviewer.json
    security_review.json
  sessions/
    SES-....json
    SES-....messages.jsonl
  memory/
    MEMORY.md
    entries.jsonl
  tasks/
    TASK-.../
      task.json
      plan.json
      plan_updates.jsonl
      state.json
      baseline.json
      progress.md
      handoff.md
      events.jsonl
      findings.jsonl
      approvals.jsonl
      input_requests.jsonl
      workspace_edits.jsonl
      criteria/
        latest.json
        history.jsonl
      sprint/
        latest.json
        history.jsonl
      completion/
        latest.json
      verification/
        latest.json
        history.jsonl
      reviews/
        latest.json
        history.jsonl
      harness/
        latest.json
        history.jsonl
      context/
        latest-pack.json
        summary.md
      diagnostics/
        quality-latest.json
        quality-history.jsonl
        DIAG-....json
      multica/
        run_metadata.json
        workspace_guidance.json
      checkpoints/
        0001.json
        0002.json
      workers/
        WKR-....json
      worker_runtime/
        WKR-....workspace.json
        WKR-....baseline.json
        WKR-....settlement.json
        WKR-....result.json
        WKR-....reconcile.json
  watches/
    WATCH-....json
```

## 2. 当前 artifact owner

| Artifact | 作用 | 当前状态 |
| --- | --- | --- |
| `task.json` | canonical task definition | active |
| `plan.json` | system gate plan + mutable execution checklist | active |
| `plan_updates.jsonl` | append-only mutable execution graph mutation history, including replace/patch kind and patch ops when applicable | active |
| `.ngen/project/project.json` | workspace-level durable project graph over multiple tasks/branches | active |
| `.ngen/project/project_updates.jsonl` | append-only workspace project graph mutation history, including replace/patch kind and project patch ops | active |
| `.ngen/missions/<mission_id>/mission.json` | workspace-level large-task mission contract, role plan, and plan approval state bound to a root task | active |
| `.ngen/missions/<mission_id>/validation_contract.json` | behavioral requirements, stable assertion ledger, and evidence requirements for mission closure | active |
| `.ngen/missions/<mission_id>/features.json` | mission feature records, task/worker bindings, and assertion coverage | active |
| `.ngen/missions/<mission_id>/milestones.json` | milestone records, feature/assertion coverage, and validation refs | active |
| `.ngen/missions/<mission_id>/validation_runs.jsonl` | append-only independent validation runs | active |
| `.ngen/missions/<mission_id>/notes.md` | human-readable mission notes | active |
| `.ngen/roles/<role_id>.json` | role capability contract for built-in profiles and provider action gating | active |
| `state.json` | phase/state/status pointers | active |
| `baseline.json` | workspace / environment / verifier baseline | active |
| `progress.md` | 人类可读进展 | active |
| `handoff.md` | 当前交接摘要 | active |
| `events.jsonl` | append-only runtime events | active |
| `findings.jsonl` | review blocking findings | active |
| `approvals.jsonl` | approval request / decision records | active |
| `input_requests.jsonl` | structured input request / response records | active |
| `workspace_edits.jsonl` | model-driven workspace file mutation records for coding tasks | active |
| `criteria/latest.json` | acceptance-ledger truth for current criterion status, passes, focus, and evidence | active |
| `criteria/history.jsonl` | append-only acceptance-ledger refresh history | active |
| `sprint/latest.json` | current-scope contract for the active sprint boundary, completion signals, and deferred criteria | active |
| `sprint/history.jsonl` | append-only current-scope contract refresh history | active |
| `completion/latest.json` | 最近一次 done gate verdict | active |
| `verification/latest.json` | 最近 verifier report | active |
| `reviews/latest.json` | 最近 review report | active |
| `harness/latest.json` | 最近一次 harness evaluation snapshot，记录 provider/context/repair/review/completion strategy truth | active |
| `harness/history.jsonl` | append-only harness evaluation ledger | active |
| `context/latest-pack.json` | task-local machine-readable continuity pack | active |
| `context/summary.md` | task-local compaction summary | active |
| `diagnostics/quality-latest.json` | latest task-local long-horizon quality diagnostics over edits, scope, repair failures, and completion blockers | active |
| `diagnostics/quality-history.jsonl` | append-only quality diagnostics history | active |
| `diagnostics/*.json` | durable failure diagnostics such as unrecoverable state records | active |
| `multica/run_metadata.json` | Multica/NGEN headless exec identity: task/session id, config-derived model route, config fingerprint, permission mode, and fail-closed resume baseline | active |
| `multica/workspace_guidance.json` | bounded capture of consumed `<workdir>/AGENTS.md` and `<workdir>/skills/**/SKILL.md` for Multica workspace runs | active |
| `checkpoints/*.json` | crash-safe restore points | active |
| `workers/*.json` | bounded worker contracts | active |
| `worker_runtime/*.workspace.json` | child workspace lifecycle truth | active |
| `worker_runtime/*.baseline.json` | isolated child spawn baseline truth | active |
| `worker_runtime/*.settlement.json` | child settlement truth | active |
| `worker_runtime/*.result.json` | compiled child result truth for parent/runtime consumption | active |
| `worker_runtime/*.reconcile.json` | isolated child side-effect reconcile truth | active |
| `.ngen/watches/*.json` | waiting task 的 watch truth | active |
| `.ngen/sessions/*.json` | ACP / terminal session state | active |
| `.ngen/sessions/*.messages.jsonl` | session prompt / assistant / runtime transcript | active |
| `.ngen/memory/MEMORY.md` | workspace-level human memory | active |
| `.ngen/memory/entries.jsonl` | append-only memory entries | active |

## 3. schema 原则

所有当前 JSON artifacts 都必须包含：

- `schema_version`
- 稳定主键，例如 `task_id` 或 `watch_id`
- 适用的时间戳字段

基础规则：

- JSON 用于机器状态。
- Markdown 用于人类状态。
- `events.jsonl`、`criteria/history.jsonl`、`verification/history.jsonl`、`reviews/history.jsonl`、`harness/history.jsonl`、`validation_runs.jsonl` 尽量 append-only。
- 所有 refs 默认使用 task root 相对路径。
- workspace-level refs 使用 `workspace:` 前缀。
- configured additional roots 下的 refs 使用 `root:<root_id>/...` 形式。
- role ids 当前冻结为 `coding`、`general_execution`、`reviewer` 与 `security_review`；role files 是 workspace-level state，不是 task-local refs。

## 4. 当前 refs 规则

- task 内文件 ref: `verification/latest.json`
- task 内文件 ref: `harness/latest.json`
- JSONL item ref: `events.jsonl#event_id=EVT-...`
- JSONL item ref: `harness/history.jsonl#harness_eval_id=HEVAL-...`
- JSONL item ref: `input_requests.jsonl#input_record_id=INPREC-...`
- JSONL item ref: `workspace_edits.jsonl#edit_record_id=EDITREC-...`
- workspace project mutation ref: `workspace:.ngen/project/project_updates.jsonl#mutation_id=PRJ-...`
- workspace mission validation ref: `workspace:.ngen/missions/MIS-.../validation_runs.jsonl#validation_run_id=MVAL-...`
- workspace 文件 ref: `workspace:.ngen/watches/WATCH-....json`
- additional root 文件 ref: `root:extra_1/README.md`

## 5. external surface mapping

当前冻结以下 surface：

- human-readable CLI
- headless JSON output
- ACP
- interactive terminal

因此当前 derived objects 只有：

- `status_snapshot`
- `session_snapshot`
- JSONL events emitted by `run --json` / `resume --json` / `events tail --json`
