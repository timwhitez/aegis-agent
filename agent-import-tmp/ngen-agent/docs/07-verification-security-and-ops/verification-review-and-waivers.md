# 验证、review 与 done gate

## 1. verifier matrix

当前实现冻结以下 verifier 路径：

- `coding`
  - baseline
  - Go workspace 下默认执行 `go test ./...`
  - `ngen.json.verification.coding_commands` 可显式声明 ordered verifier sequence；runtime 会按顺序逐条执行这些 repo-owned verifier commands
  - 若 `coding_commands` 为空，则 legacy `ngen.json.verification.coding_go_test_command` 仍覆盖 criterion-derived 自动选择
  - 若仍处于默认 verifier 配置，而 task success criteria 明确声明了 verifier 命令，例如 ``./build.sh test`` passes 与 ``./build.sh build`` passes，则 runtime 会按 criteria 中出现顺序执行该 repo-owned verifier sequence
  - Multica/headless `exec` 不提供读取 metadata / system_prompt 的 quick-create verifier 例外；stdin adapter 只把 user text blocks 拼成 prompt。若 user text 本身明确要求运行某个 direct argv command，adapter 只生成必须由 completed repair command record 关闭的通用 criterion；runtime 通过 repair command policy 执行该显式命令，不合成命令参数、description 文件、issue comment、delegation 或 fallback artifact，失败或 unsafe side-effect attempt 不自动重放。若 prompt/runtime 后续产生其他 observation 或 repair command，verifier/review 只能消费这些命令留下的 `command_runs.jsonl`、stdout/stderr artifact、policy decision、replay-safety 与 read-only observation evidence。
  - 当前默认带 `verification.coding_timeout_seconds=60` 的 timeout guard；阻塞型测试必须收敛成结构化 verifier failure，而不是无限挂住 runtime
- `general_execution/docs_lite`
  - baseline
  - docs structural review
- `security_review`
  - baseline
  - security inventory
  - secret indicators
  - entrypoint indicators
- `reviewer`
  - baseline
  - Go test when `go.mod` 存在
  - docs review when可见 Markdown 存在

## 2. review lane

review lane 负责检查：

- verifier 是否通过
- handoff 是否存在
- completion claim 是否 overclaim
- criteria 是否都带 evidence refs；当前 foundation gate 下，显式 path / glob criterion、带 readme/docs/config 语义且含具体 token 的 criterion、以及显式 worker-runtime criterion 都必须回链到真实 artifact evidence；worker-runtime criterion 需要命中 `workers/*.json` 与对应 `worker_runtime/*.result|settlement|reconcile|workspace.json`
- changed paths、sprint/project focus 与 worker runtime refs 是否支持当前 completion claim
- `diagnostics/quality-latest.json` 是否记录 blocking quality finding；quality blocker 必须作为 review finding 写入 `findings.jsonl`
- worker-backed criterion 不得只靠 child prose 或 `workers/*.json` contract 通过；review 必须看到 result / settlement / reconcile truth，且 child completion / review / verification / settlement / reconcile 状态不能留下 trust gap。`trusted_for_parent_completion=false` 或低于 complete 的 worker evidence grade 必须作为 `worker_trust_gap` 处理。
- `completion/latest.json` 是否需要根据最新 review 结果被重写

`reviews/latest.json` 现在是 evidence-first report，而不是只保存 `status` 与摘要。active additive fields 包括：

- `reviewer_profile`
- `review_context_refs`
- `changed_paths`
- `worker_result_refs`
- `risk_summary`
- `blocking_categories`

`findings.jsonl` 的 active category vocabulary 为：

- `confirmed_defect`
- `missing_evidence`
- `scope_drift`
- `complexity_risk`
- `security_risk`
- `stale_context_risk`
- `worker_trust_gap`
- `inferred_risk`
- `not_observed`

其中 `inferred_risk` 与 `not_observed` 只允许表达“推断风险 / 未观察到的 surface”，不得写成 confirmed fact。`security_review` 与 `reviewer` 产生或消费 findings 时必须保留 confirmed / inferred / not-observed 的区别。

如果 review 发现阻塞问题，必须：

- 写 `reviews/latest.json`
- 追加 `findings.jsonl`
- 把任务收敛到 `Blocked/blocked_review`
- 拒绝 `Done`
- 如果 review 在 verifier 尚未产出 `verification/latest.json` 时被显式触发，也必须留下 blocking review/completion artifacts；不得把缺失 verifier artifact 直接暴露成原始文件不存在错误

如果 review 清除 blocker 且 done gate 重新满足，必须把 `completion/latest.json` 刷新为最新 accepted verdict，并把任务收敛回 `Done`。

## 3. done gate

当前 done gate 必须同时满足：

1. `baseline.json` 已存在
2. 当前 profile 的 verifier 已通过
3. `reviews/latest.json.status=clear`
4. `handoff.md` 已存在
5. `criteria/latest.json` 中所有 criterion 都至少带一条 evidence ref

补充约束：

- `coding` task 的 workspace-backed criterion 不得只靠 `go test ./...` 自动闭合；
- 显式 verifier-command criterion 不得只靠泛化的 `verification passed` 自动闭合；它必须能回链到实际执行且通过的 verifier command evidence；
- 当前 foundation gate 至少对以下 criterion 强制额外 workspace evidence：
  - 显式 path / glob criterion，例如 `README.md`、`docs/*.md`、`config.sample.json`
  - 带 readme/docs/config 语义且含具体 token 的 criterion，例如 `sample config mentions \`timeout_seconds\``
- parent manager task 的显式 worker-runtime criterion 也不得只靠 generic `verification passed` 自动闭合；它必须能回链到 child 的 `workers/*.json` 与匹配的 `worker_runtime/*.result|settlement|reconcile|workspace.json`，必要时再带 child review / verification / approval refs
- mission root closure 不得只靠 generic verifier passed；`mission validate` 必须先通过 deterministic artifact validator，看到 root task `Done`、closed criteria、accepted completion，以及每个 assertion 对应的 root/worker/verifier/review/completion/validation evidence ref。若 `mission.json.role_plan.validators.explicit=true`，还必须通过 dedicated read-only model validator；latest validation run passed 后才能把 mission 收敛为 `done`。
- model validator 的输出只能是 evidence-backed findings、blocking bit 与 recommended action；不能直接修改 workspace、发起 repair command、provider decision action、`task_create` 或 worker creation。blocking finding 会让 mission 进入 `blocked_validation`，并可转成 fix-feature candidate 供 orchestrator 后续处理。
- 外部状态相关任务的 done gate 仍必须看到 baseline、`verification/latest.json`、`reviews/latest.json`、`handoff.md` 与 closed criteria；若 completion claim 依赖外部 mutation 或 observation，criteria evidence 必须回链到 completed command records 或 read-only observation evidence，而不是 result prose、injected AGENTS.md、skills 或 `.agent_context`。显式 command-backed criteria 只有在 completed repair command record 显示对应 argv 已执行后才能关闭。
- 更抽象、无稳定 workspace signal 的 criterion 仍可沿用 verification 作为最低证据。
- bounded repair loop 中的 failed/noop workspace edit 仍必须留下 durable truth；只要 repair budget 未耗尽，runtime 可以在同一 pass 内继续下一次 attempt，但不得隐去前一次 failure summary。

## 4. approval 与 waiver

当前实现 approval request / decision，并支持 `permission_mode_id=yolo` 自动批准。
worker child approval 允许通过 `owner_task_id` / `owner_worker_id` 归属回 parent operator surface，但 durable truth 仍保留在 child `approvals.jsonl`。

以下能力仍为 deferred：

- waiver
- override
- multi-scope approval engine
- arbitrary nested parent/child approval ownership beyond current bounded worker-parent route

## 5. current failure semantics

- verifier 未通过 -> `Failed/failed_verification`
- verifier timeout 仍属于 `Failed/failed_verification`，并且 exact timeout summary 必须进入 `verification/latest.json`
- state 不可恢复 -> `Failed/failed_state`
- approval pending 或被拒且仍需该动作 -> `Blocked/blocked_policy`
- review / criteria / handoff blocker -> `Blocked/blocked_review`
- mission contract / validator blocker -> mission `blocked_validation`
- watch 生效 -> `Waiting/waiting_watch`
