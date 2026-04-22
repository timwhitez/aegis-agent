# Issues Revalidation Snapshot

Last updated: 2026-04-22

Current baseline:

- Branch: `main`
- HEAD: `38c12f8`
- Scope: 本轮只复核“之前测试流程里发现、但当时尚未最终确认关闭”的问题；不做代码修复，只做验证和 `issues.md` 回写。

## Validation Baseline

### Local verification completed

- `go test ./internal/runtime ./internal/webconsole ./internal/tools -count=1` ✅

### Historical tracker reference reviewed

- Deleted tracker baseline: `git show 5734dfe:issues.md`
- Latest relevant repair commit under validation: `d907a20 fix: stabilize child workdirs and live regression harness`

### Retained live evidence reviewed

- Acceptance stack summary:
  - `validation/runs/2026-04-21-openai-compatible-gpt-5.4-acceptance-stack-after-rt24-websocket-fix-rerun3/SUMMARY.md`
- Acceptance matrix summary / issues:
  - `validation/runs/2026-04-21-openai-compatible-gpt-5.4-acceptance-stack-after-rt24-websocket-fix-rerun3-matrix/SUMMARY.md`
  - `validation/runs/2026-04-21-openai-compatible-gpt-5.4-acceptance-stack-after-rt24-websocket-fix-rerun3-matrix/ISSUES.md`
- Acceptance focused web follow-up:
  - `validation/runs/2026-04-21-openai-compatible-gpt-5.4-acceptance-stack-after-rt24-websocket-fix-rerun3-focused-webconsole-followup/SUMMARY.md`
- Task-heavy matrix summary / issues:
  - `validation/runs/2026-04-21-openai-compatible-gpt-5.4-task-heavy-after-harness-fix-rerun2/SUMMARY.md`
  - `validation/runs/2026-04-21-openai-compatible-gpt-5.4-task-heavy-after-harness-fix-rerun2/ISSUES.md`
- Task-heavy focused web follow-up:
  - `validation/runs/2026-04-21-openai-compatible-gpt-5.4-task-heavy-after-harness-fix-rerun2-focused-webconsole-followup/SUMMARY.md`

## Revalidated Previously Unresolved Items

| ID | Previously observed issue | Current verification result | Status |
| --- | --- | --- | --- |
| V-01 | Child / queue relative `workdir` 在隔离执行下可能解析错误，或出现 cwd-relative path duplication。 | `internal/runtime` 包测试已通过；覆盖到 `TestRunnerDelegateResolvesRelativeWorkdirAgainstParent`、`TestRunnerDelegateKeepsExistingCwdRelativeWorkdir`、`TestRunnerQueueSubmitResolvesRelativeWorkdirAgainstParent`、`TestRunnerQueueSubmitKeepsExistingCwdRelativeWorkdir`。同时最新 acceptance matrix 与 task-heavy matrix 全部通过，且两份 `ISSUES.md` 都记录为无 open issues。 | Closed / revalidated |
| V-02 | `continue` / retry 可能从当前进程配置漂移，未稳定继承 durable retry metadata；steer scope change 后也可能丢失 refresh reminder，导致验证口径误报。 | `internal/runtime` 包测试已通过；覆盖到 `TestRunnerContinueUsesDurableRetryPolicyFromSessionMetadata` 与 `TestNextHarnessReminderRefreshesSpecAndPlanAfterSteerScopeChange`。两份 focused follow-up summary 都明确保留 `retry_policy.max_attempts=2`，并捕获真实 `provider.retry` 事件；acceptance stack 也整体通过。 | Closed / revalidated |
| V-03 | `experimental web` 的 websocket close-path 曾存在稳定性风险，且 queue/browser drilldown 需要真实证据而不是 mock。 | `internal/webconsole` 包测试已通过；包内覆盖 websocket、queue、history、clear 等核心路径。两份 focused follow-up summary 都显示浏览器 smoke、failed queue canary、queue notification dedup、worker drilldown 等真实链路已通过。 | Closed / revalidated |
| V-04 | 工具面与文档/测试之间曾存在 drift，尤其是 `feature_list_*` 相关 surface 的文档与测试锚点需要复核。 | `internal/tools` 包测试已通过；相关修复范围已落在 `d907a20` 中的 `spec/04-tools-and-skills.md` 与 `internal/tools/registry_test.go`。最新 acceptance matrix 与 task-heavy matrix 也未再记录该类 open issue。 | Closed / revalidated |

## Current Conclusion

- 本轮复核未发现仍然处于未解决状态的存量问题。
- 当前保留的最新 live 证据结论如下：
  - acceptance stack：`passed`
  - acceptance matrix：`26/26 scenarios passed; 0 failed`
  - acceptance matrix issues：`No open issues recorded by this matrix run.`
  - task-heavy matrix：`20/20 scenarios passed; 0 failed`
  - task-heavy matrix issues：`No open issues recorded by this matrix run.`
  - 两轮 focused web follow-up：`passed`

## Open Issues

None.

## Reopen Rule

只有在出现以下任一情况时，才应重新在本文件中打开 issue：

1. 当前 `main` checkout 的单元测试、构建或 live validation 重新失败。
2. `init/run/exec/steer/continue/sessions/tasks/probe-provider/doctor` 主路径出现行为回归。
3. `README.md` / `spec/` / 实现 / 测试之间出现影响当前操作者行为的真实矛盾。
4. 新的保留验证产物再次写出 open issue，而不是旧记录残留。
