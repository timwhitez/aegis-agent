# 完成定义与必交付工作包

## 1. 当前完成定义

当前仓库的完成定义不是“生产级完整 NGEN 产品”，而是 post-foundation integrated baseline。

它必须一次性交付：

- 单二进制 `ngen`
- `.ngen/` canonical runtime state
- `task create` / `run` / `resume` / `auto` / `status` / `review` / `events tail`
- `coding` / `general_execution/docs_lite` / `security_review` / `reviewer`
- baseline / verification / review / handoff / completion / criteria artifacts
- harness evaluation artifacts
- mission validation artifacts
- input request artifacts
- `watch` + `scheduler tick --once`
- ACP / terminal / worker / memory / hook / visibility minimal surface
- 稳定 JSON event / `status_snapshot` / `session_snapshot` / `worker_snapshot` / `acp_notification` contract

## 2. 必交付工作包

### 2.1 Core Artifacts

必须交付：

- `task.json`
- `plan.json`
- `state.json`
- `baseline.json`
- `progress.md`
- `handoff.md`
- `events.jsonl`
- `findings.jsonl`
- `approvals.jsonl`
- `input_requests.jsonl`
- `criteria/latest.json`
- `completion/latest.json`
- `verification/latest.json`
- `reviews/latest.json`
- `diagnostics/quality-latest.json`
- `diagnostics/quality-history.jsonl`
- `harness/latest.json`
- `harness/history.jsonl`
- `.ngen/missions/<mission_id>/mission.json`
- `.ngen/missions/<mission_id>/validation_contract.json`
- `.ngen/missions/<mission_id>/features.json`
- `.ngen/missions/<mission_id>/milestones.json`
- `.ngen/missions/<mission_id>/validation_runs.jsonl`
- `checkpoints/*.json`
- `.ngen/watches/*.json`
- `.ngen/sessions/*.json`
- `.ngen/sessions/*.messages.jsonl`
- `workers/*.json`
- `.ngen/memory/*`

### 2.2 Runtime

必须交付：

- create / run / resume state machine
- provider-driven `auto`
- explicit `Done` / `Failed` / `Blocked` / `Waiting`
- durable checkpoints
- review-backed done gate

### 2.3 Verification

必须交付：

- `coding` verifier
- `docs_lite` verifier
- `security_review` verifier
- `reviewer` verifier
- blocking review findings with stable categories, risk summary, affected paths, and worker-trust evidence checks

### 2.4 Operator Surface

必须交付：

- human-readable CLI
- `--json` headless output
- ACP stdio server
- structured input request control surface
- interactive terminal
- stable `status_snapshot` / `session_snapshot` / `worker_snapshot`
- `harness eval TASK-ID --json`
- `mission create|get|status|plan|approve|validate|run|pause|resume`
- per-request ACP `ngen.notification`

### 2.5 Waiting And Scheduler

必须交付：

- `watch set`
- `watch ls`
- `watch cancel`
- `scheduler tick --once`

## 3. 当前明确不交付

- TUI
- richer role-file inheritance / role discovery UX, beyond the current built-in role contract hydration and provider action gate
- broader provider matrix（超出 builtin/command、OpenAI-compatible Chat Completions、OpenAI Responses 与 Anthropic Messages 当前范围）
