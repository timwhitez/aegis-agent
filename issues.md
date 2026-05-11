# go-cli-agent issues.md 收敛记录

更新时间：2026-05-11

本文从“问题登记”更新为本轮收敛 ledger。当前状态：23/23 已收敛到代码、测试、spec / README / validation 文档或脚本中；未把 WebConsole 扩展提升为默认 core CLI 叙事，未引入固定 DAG / workflow engine。

本轮仍按 `AGENTS.md` 的 core-v1 边界处理：先读核心 spec，再按 issue 切片修复；P1/P2/P3 均用最小回归测试或脚本文案对齐证明。真实 provider live matrix / full browser follow-up 未在本轮重跑；相关文档已改为不把历史 live run 或未执行的 UI 交互当作当前 HEAD 证明。

## 验证契约

本轮“完成”的定义：

- 每个 issue 有对应代码/文档/脚本收敛点。
- P1/P2 安全、事实源、并发和 provider 契约必须有 Go 回归测试。
- WebConsole 的 UI/validation 变更保持 `experimental`，并同步 `spec/17-web-console.md`、`validation/README.md`、`validation/scenarios.md` 与 follow-up summary 文案。
- 默认测试入口必须覆盖 WebConsole JS syntax gate。
- 结束前运行 owned-module Go tests、WebConsole race slice、JS syntax check 和 `./test.sh`。

本轮已执行验证：

- `go test ./internal/session ./internal/skills ./internal/hooks ./internal/tools ./internal/runtime ./internal/app ./internal/webconsole -count=1`
- `go test ./cmd/... ./internal/... ./pkg/... -count=1`
- `CGO_ENABLED=1 go test -race ./internal/webconsole -run TestServiceConfigUpdateSwapsSnapshotInsteadOfMutatingSharedConfig -count=1`
- `node --check internal/webconsole/assets/app.js`
- `node --check internal/webconsole/assets/utils.js`
- `node --check internal/webconsole/assets/icons.js`
- `node --check internal/webconsole/assets/api.js`
- `node --check internal/webconsole/assets/events.js`
- `node --check internal/webconsole/assets/settings-view.js`
- `node --check internal/webconsole/assets/workspace-view.js`
- `node --check internal/webconsole/assets/session-view.js`
- `node --check validation/scripts/webconsole_ui_smoke.mjs`
- `./test.sh`

## 收敛明细

| # | 状态 | 收敛点 | 证据 |
|---|---|---|---|
| 1 | Closed | Store 层集中校验 session/task/job ID，所有 session/task/job path API 走安全 path builder；`DeleteSessionTree` 避免用 path-like id 删除外部目录。 | `TestStoreRejectsPathLikeRecordIDs` |
| 2 | Closed | Skill zip 解压拒绝 absolute / `..` entry，并对目标路径做 containment 校验；关键 IO 错误不再静默丢失。 | `TestProcessSkillZipRejectsTraversalEntries`、`TestProcessSkillZipAllowsNestedSkillFiles` |
| 3 | Closed | Skill catalog scan/load 对 `SKILL.md` 与 `tools/*.yaml` 做 symlink resolution 和 skill-root containment 校验。 | `TestCatalogRejectsSymlinkedSkillMDEscape`、`TestCatalogRejectsSymlinkedToolYAMLEscape`、`TestCatalogAllowsRegularNestedSkillAndTools` |
| 4 | Closed | Skill install/uninstall 破坏性路由强制 `POST`；错误 method 返回 405 且不删除目录。 | `TestServiceSkillRoutesUploadListUninstallAndInstallUnsupported` |
| 5 | Closed | Steer append/update 加 OS 文件锁；rewrite 前 reload/merge 未知 steer id，避免并发 append 被旧快照覆盖。 | `TestUpdateSteerRequestsMergesConcurrentAppend` |
| 6 | Closed | `DeleteSessionTree` 先收集相关 session/job，再执行窄锁删除，避免 queue reconcile 在锁内重入。 | `TestDeleteSessionTreeDoesNotDeadlockWithReconcilableJob` |
| 7 | Closed | WebConsole service 持有不可变 config snapshot；update 时替换指针，不原地 mutate 已分发 config；runner/API 入口取 snapshot。 | `TestServiceConfigUpdateSwapsSnapshotInsteadOfMutatingSharedConfig`、race slice |
| 8 | Closed | Engine 对 deadline tool result 保留 registry 返回的结构化 metadata；skill command timeout 补 timeout/exit/raw/truncated metadata。 | `TestEnginePreservesDeadlineToolResultMetadata`、`TestSkillCommandToolTimeoutIncludesStructuredMetadata` |
| 9 | Closed | Hook command output 加 12KB 截断和 raw_length/truncated/timeout/exit_code metadata。 | `TestManagerTruncatesLargeHookCommandOutput`、`TestManagerEmitsHookCommandTimeoutMetadata` |
| 10 | Closed | `api_provider: openai-compatible` 的自定义 profile 在 run/probe 路径默认 `store:false`，基于 effective API provider 判断。 | `TestProviderOptionsFromConfigDefaultsStoreFalseForCustomOpenAICompatible`、`TestProbeDefaultsStoreFalseForCustomOpenAICompatible` |
| 11 | Closed | Feature-list pre-completion hard gate 仅保留在 `init` mode；普通 session finish 不被陈旧 roadmap 硬阻断。 | `TestEngineDoesNotHardBlockNormalFinishOnStaleFeatureList`、`TestEngineStillBlocksInitFinishOnIncompleteFeatureList` |
| 12 | Closed | Ephemeral tool-output artifact 默认落到 session root 下的 `artifacts/tool-outputs`，目录 `0700`、文件 `0600`。 | `TestEngineEphemeralArtifactGuidanceAvoidsReadFileLoop` |
| 13 | Closed | Skill command tool schema 默认递归闭合 object schema；显式 `additionalProperties:true` 保持允许。 | `TestSkillCommandToolClosesSchemaByDefault`、`TestSkillCommandToolPreservesExplicitAdditionalPropertiesTrue` |
| 14 | Closed | `write_file` / `edit_file` 同时检查原始 lexical input 与 resolved path denylist，阻断 `.git` / `.go-cli-agent` symlink alias。 | `TestWriteDeniedSensitiveSymlinkAlias` |
| 15 | Closed | Task board 增加 `in_progress` group/counter，CLI 默认输出优先显示 active task。 | `TestBuildTaskBoardIncludesInProgressGroup`、`TestTasksCommandRendersTaskBoard` |
| 16 | Closed | `probe-provider` 非 JSON 模式先判错再输出，失败时不再打印空 success 字段。 | `TestProbeProviderCommandNonJSONErrorDoesNotPrintEmptySuccessFields` |
| 17 | Closed | `/api/queue/jobs/{id}` detail handler 强制 `GET`，错误 method 返回 405 且不返回 job JSON。 | `TestServiceQueueJobDetailRequiresGet` |
| 18 | Closed | WebConsole live follow-up spec/README/summary 不再过度声明 Overview/Worker Pool/manual-refresh 等未执行交互；browser smoke 口径改为当前实际覆盖。 | `spec/17-web-console.md`、`validation/README.md`、`validation/run_experimental_webconsole_followup_validation.sh`、`node --check validation/scripts/webconsole_ui_smoke.mjs` |
| 19 | Closed | `test.sh` 增加 `node --check internal/webconsole/assets/*.js`，README 同步默认验证入口。 | `./test.sh` |
| 20 | Closed | validation README / scenarios 不再引用不存在的 stable run；follow-up 成功路径固定写 `ISSUES.md` 并标明 no open issues。 | `validation/README.md`、`validation/scenarios.md`、`validation/run_experimental_webconsole_followup_validation.sh` |
| 21 | Closed | `glob` 对每个候选 path 调用 workspace containment 校验，symlink escape 文件跳过、目录剪枝。 | `TestGlobSkipsSymlinkEscapes` |
| 22 | Closed | Stable runner 对同一 Runner 的并发 active session 加 slot guard；不同 session 并发启动返回明确错误，同 session nested auto-continue 仍允许。 | `TestRunnerRejectsDifferentConcurrentActiveSessionSlot` |
| 23 | Closed | Session 中 `Open job` 进入 Background Jobs 后展示轻量 selected job facts panel；browser smoke 增加 selected job facts 断言，保持简化 UI。 | `internal/webconsole/assets/app.js`、`validation/scripts/webconsole_ui_smoke.mjs`、`node --check internal/webconsole/assets/app.js` |

## 剩余边界

- 本轮没有重跑真实 provider live matrix 或完整 `validation/run_experimental_webconsole_followup_validation.sh`，因此不新增 live readiness 结论。
- WebConsole 仍是 `experimental` 扩展入口；默认产品叙事继续以 `init/run/exec/steer/continue/sessions/tasks/probe-provider/doctor` 为主。
- 历史 `validation/runs/*/` 多数被 `.gitignore` 忽略；需要 fresh evidence 时应重新生成，而不是引用仓库中不存在的历史 run 目录。
