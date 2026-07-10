# TODOs - Web UI 与 Agent Loop 优化复查

## 1. 复查范围与结论

本轮只记录仍需优化的项目，不重复已经收敛的旧 `issues.md` 条目。

复查基线：

- 当前本地 `HEAD`: `fd8c499`
- 近期代码范围：旧远端部署基线 `a37fd86` 到当前 `fd8c499`，以及旧 `issues.md` 对应的 agent-loop / tool / compaction 修复
- 远端：`guangzhe.zhang@10.37.107.237`
- WebConsole：`http://10.37.107.237:3940/`
- 重点分析的远端 session：
  - `20260706-115631-802741`
  - `20260708-112149-795454`
  - `20260709-035900-a9a207`

已确认有效、无需重复修复的部分：

- `command_timeout` 已能与普通 `command_nonzero_exit`、用户中断分开；`20260709-035900-a9a207` 中 13 次命令超时均被正确归类为 `command_timeout`。
- completed root session 的 follow-up 已能在同一 session 中继续。`20260709-035900-a9a207` 留下 3 组 `session.started -> session.completed`，后两组是在原 session 上继续，没有丢失历史。
- `update_goal` 的 criterion / validation id 报错已比旧版本更可恢复；`edit_file old_text not found` 和命令超时也已有针对性提示。
- 远端大量 shell 非零退出主要来自目标项目依赖、ABI、编译器、平台和命令自身语义，不应继续按 harness bug 统计。

仍有 6 项需要处理。优先级按真实额外 turn、状态一致性和用户可见影响排序。

---

## - [x] T1 [P0] Ephemeral 策略错误地外置“刚执行完”的工具结果，制造近乎一比一的额外读取 turn

### 现象与证据

当前 `internal/runtime/engine.go` 在每次工具执行后统计该工具在整个 session 中的历史调用次数。当次数超过 `EphemeralWindow` 后，立即把本次刚产生的 `LLMOutput` 替换成 artifact 指针。

这不是“只压缩较老结果”的滑动窗口，而是“shell 调用超过 2 次后，之后每一次 shell 都只给模型一个文件指针”。模型若要知道刚才命令的输出，必须在下一轮调用 `read_file`。

远端数据已经证明该行为形成稳定的额外往返：

| session | provider turns | shell calls | shell artifact reads | 全部 artifact reads / read_file | 纯 artifact-read turns |
|---|---:|---:|---:|---:|---:|
| `20260706-115631-802741` | 44 | 9 | 8 | 13 / 105 | 1 |
| `20260708-112149-795454` | 67 | 41 | 41 | 48 / 100 | 10 |
| `20260709-035900-a9a207` | 569 | 388 | 384 | 398 / 796 | 127 |

`20260709-035900-a9a207` 中有 127 个 assistant turn 的全部工具调用都只是读取 `artifacts/tool-outputs/`。这已经是 agent-loop 效率问题，不是模型偶发选择。

### 根因

- `internal/runtime/engine.go` 的 ephemeral 分支对当前结果执行：
  - `countToolCalls(messages, call.Name)`
  - `count > toolDef.EphemeralWindow`
  - 将当前 `toolResult.LLMOutput` 替换为 `[Full output saved to ...]`
- `shell` 的 `EphemeralWindow = 2`；第三次及之后的所有 shell 调用都会进入该分支，session continue 后计数也不会重置。
- 现有 `TestEngineEphemeralArtifactGuidanceAvoidsReadFileLoop` 只断言指针和路径存在，没有断言“当前结果在下一次 provider call 中仍然可见”。测试名与真实行为相反。

### 优化方案

1. 把 ephemeral 语义改成 provider-view 的滑动窗口：最新 `EphemeralWindow` 个结果保持 inline，只把更老的结果在发送给 provider 的上下文视图中替换为 artifact 指针。
2. durable `messages.jsonl` 保留当前工具结果的完整 `LLMOutput`；artifact 可以同时写入，作为大输出分页和后续恢复事实，但不能让当前模型必须再花一轮读取刚产生的结果。
3. 对确实过大的当前输出，返回“有用的 bounded head/tail + command summary + artifact path”，不能返回 pointer-only。短输出和错误摘要直接 inline。
4. 变换只能作用于 provider view，不能回写或改写原始 message/event 日志。
5. 兼容旧 session：旧消息只有 pointer 时继续允许 `read_file` 读取已有 artifact，不要求迁移历史文件。

### 验收标准

- 第 3、4、5 次 shell 调用的当前输出都能直接出现在紧随其后的 provider request 中。
- 超过窗口后，较老 shell/glob/grep 输出可在 provider view 中变成 artifact pointer，但最新窗口不变。
- 2-3 行的成功或失败输出不会被 pointer-only 替换。
- 新增 replay/integration test，断言连续 shell 调用不需要模型用 `read_file` 才能看到刚执行完的结果。
- 重放远端同类统计时，shell artifact read 不再接近 shell call 的一比一比例，纯 artifact-read turns 应显著下降。

---

## - [x] T2 [P1] Workspace 浏览目录在页面切换后被 session workdir 强制覆盖

### 用户可见问题

在 Workspace 页面进入一个子目录后，切换到 Session / Sessions 等页面，再返回 Workspace，目录会回到当前 session 的 workdir；没有 active session 时则会回到 workspace 根目录。

该问题还会影响新 session 的启动目录，因为 `startSession()` 使用 `selectedWorkspaceWorkdir()` 作为 workdir。用户浏览到目标目录后只要切换一次页面，最终启动目录就可能被悄悄改回去。

### 根因

- `internal/webconsole/assets/app.js` 的 `switchView('workspace')` 每次都调用 `fetchWorkspace()`。
- `internal/webconsole/assets/workspace-view.js::fetchWorkspace()` 每次进入页面都会比较 `currentSessionWorkspacePath()` 与当前浏览路径，只要不同就覆盖 `workspaceViewState.path`。
- `workspaceViewState.syncedSessionWorkdir` 本来用于“仅在选中 session / workdir 变化时同步”，但 `fetchWorkspace()` 绕过了这个保护条件。
- `currentSessionWorkspacePath()` 在没有 root 或 workdir 时返回空字符串，导致“没有 active session”与“session 正好位于 workspace 根”无法区分。
- `persistUIState()` 只保存当前 view、session 和 floating panel 状态，没有保存 Workspace 的浏览路径。

### 优化方案

1. `currentSessionWorkspacePath()` 在没有 durable session / workdir 时返回 `null`，不要把缺失 session 当成 workspace 根。
2. `fetchWorkspace()` 只负责按当前浏览路径刷新目录；session workdir 同步统一走 `syncWorkspaceToCurrentSession()`。
3. 仅在以下场景自动切到 session workdir：
   - 首次选中一个 durable session；
   - active session id 变化；
   - 同一 session 的 metadata workdir 确实变化；
   - 用户显式选择“回到 session workdir”。
4. 普通 view 切换、polling refresh、同一 session detail 刷新不得覆盖手动浏览路径。
5. 将浏览路径作为纯 UI preference 持久化到 localStorage，建议按 `workspace_root + selectedSessionId` 分桶；新 session composer 使用独立的 `new-session` 路径。它不是执行状态事实源，后端仍需对提交的 workdir 做安全校验。
6. 若持久化路径已被删除或不可访问，回退到最近可访问父目录，再回退 workspace 根，并显示一次明确提示。

### 验收标准

- `Workspace -> 子目录 -> Session -> Workspace` 后仍停留在原子目录。
- 没有 active session 时，手动选择的目录在页面切换后不回到根目录。
- 切换到另一个 session 时，Workspace 只在第一次同步到新 session workdir，之后允许继续手动浏览。
- 浏览器刷新后可恢复该 session 对应的最后浏览目录；无效路径能安全回退。
- `validation/scripts/webconsole_utils_test.mjs` 增加上述状态机测试；浏览器 smoke 增加真实导航回归。
- `spec/17-web-console.md` 增加 Workspace 浏览路径保持与 session 切换同步的明确口径。

---

## - [x] T3 [P1] completed session continue 尚未收敛 state / Goal / child queue 一致性

### 已验证的正向结果

completed root session 的同 session follow-up 已在远端真实工作，这个产品方向应保留。

### 当前遗漏

1. Spec 冲突：
   - `spec/05-session-interrupt-resume.md` 与 `spec/17-web-console.md` 已允许 `completed -> running`。
   - `spec/01-runtime-architecture.md` 仍明确写着不允许 `completed -> running`。
2. `Runner.Continue()` 和 Web API 当前对所有 completed session 放行，没有区分 root session 与由 parent / queue 创建的 child session。
3. completed child 可能已经把关联 queue job、parent coordination 和 background notification 标为 completed。直接 continue 只把 child `state.json` 改回 running，不会原子重开 queue job，也不会把 child/job 重新加入 parent 的 unresolved 集合。
4. 带 completed Goal 的 session 被 continue 后，Goal 仍是 complete。当前代码没有定义这是“沿用已完成 Goal”“显式恢复 Goal”还是“只复用聊天历史但不再受旧 Goal 约束”，Web 展示也可能在 running session 上继续显示 `goal:complete`。

### 推荐方案

先修 spec，再改代码。保守默认：

1. 通用 `continue completed` 只允许 root session，满足 Web 用户 follow-up 的主路径。
2. completed child / queue session 不走通用 continue；继续 child 工作必须使用现有 `agent_prompt` / requeue 入口，确保 queue job、parent coordination 和 notification 一起更新。
3. 如果未来确实要允许直接 continue completed child，则必须提供单个 store transaction：child state、queue job、parent unresolved/completed 集合、notification delivery state 全部一起 reopen，任一步失败都回滚。
4. 明确 Goal 语义：建议 completed Goal 默认保持历史完成事实，普通 follow-up 不自动伪装成 active Goal；用户需要继续同一 durable objective 时，通过显式 Goal resume 控制面恢复。Web 必须清楚区分“session 正在继续”和“旧 Goal 已完成”。
5. 增加明确的 resumed event/data，至少能区分从 `paused` / `failed` / `completed` 哪一种状态恢复，便于远端任务日志审计。

### 验收标准

- `spec/01`、`spec/05`、`spec/17` 对 completed continuation 的状态转换一致。
- root completed session follow-up 仍通过现有真实 E2E。
- completed child 直接调用 generic continue 时得到结构化错误与正确 action，或在选择支持 child reopen 后通过原子一致性测试。
- 不出现 `child=running` 但 `queue_job=completed`、parent unresolved 为空的状态组合。
- completed Goal 的 follow-up 行为、Web 标签、completion gate 和 goal history 有明确测试。
- crash/restart 后能从文件事实恢复相同状态，不依赖 Web 内存修补。

---

## - [x] T6 [P1] 前端 utility test 门禁已失效，19 个失败没有进入默认 `test.sh`

### 当前证据

当前 `HEAD=fd8c499` 执行：

```text
node --test validation/scripts/webconsole_utils_test.mjs
tests 123
pass 104
fail 19
```

同时：

```text
go test -count=1 -timeout=3m ./internal/runtime ./internal/tools ./internal/webconsole
PASS
```

失败集中在 4 组：

1. Stop pending 状态 5 项：产品代码新增 `requestedAtBySessionId` 的 15 秒 hold，接受 stop 后仍保持 pending，旧测试仍断言请求返回后立即变成 false。
2. Goal inspector 1 项：当前 UI 的稳定标签是 `Goal Budget limited`，旧测试仍断言 `Mission Budget limited`。
3. Workspace harness 7 项：`workspace-view.js` 已调用 `listWorkspaceFiles` / `readWorkspaceFile` 等拆分后的 API wrapper，测试 harness 仍只 stub `requestJSON`，导致 `ReferenceError: listWorkspaceFiles is not defined`。
4. Settings harness 6 项：`settings-view.js` 已使用更完整的 DOM 操作，测试 fake DOM 缺少 `document.createElement`、`replaceChildren` 等能力。

这些失败大部分是测试脚手架与当前模块拆分不同步，但结果是前端回归门禁整体为红色，Workspace 路径保持问题也因此没有被现有测试捕获。

### 根因

- `test.sh` 只执行 `node --check`，没有运行 `node --test validation/scripts/webconsole_utils_test.mjs`。
- 前端模块从 `app.js` 拆到 `api.js`、`workspace-view.js`、`settings-view.js` 后，测试 harness 没有同步公共 wrapper 和 DOM contract。
- 部分断言没有随已提交的 UX 语义变化更新，导致真实回归与 stale expectation 混在一起。

### 优化方案

1. 先逐项分类 19 个失败：确认当前产品行为的测试更新 expectation；产品行为错误的才改实现，不能为“让测试绿”回退已确认的 pending/race 防护。
2. Workspace harness 直接 stub `listWorkspaceFiles`、`readWorkspaceFile`、workspace mutation/download wrapper，保持与 `api.js` 导出面一致。
3. Settings fake DOM 补齐当前 renderer 真正使用的最小 DOM contract；不要引入与产品无关的重型前端框架。
4. Stop 测试分两阶段断言：HTTP/steer 请求完成后 in-flight set 清除，但 requested hold 保留；session detail 进入非运行态或 hold 到期后才完全清除。
5. Goal inspector 断言更新为当前稳定 `Goal` 口径，并保留 runtime facts / mission plan 分区验证。
6. utility suite 全绿后，把 `node --test validation/scripts/webconsole_utils_test.mjs` 加入 `test.sh`，以后 source commit 不能只靠语法检查通过。

### 验收标准

- utility suite `123/123` 全部通过，且没有 test-end 后的 unhandled rejection。
- Workspace / Settings harness 使用当前模块 API，而不是绕回已经移除的 app.js 内部实现。
- T2 的页面切换保持测试加入该 suite，并能在回退实现时稳定失败。
- `./test.sh` 会运行 utility suite；任一前端行为回归使默认 gate 非零退出。
- 真实浏览器 smoke 继续作为 DOM/layout/network E2E，utility suite 不能替代浏览器验收。

---

## - [ ] T4 [P2] `record_goal_progress` 仍缺少与 `update_goal` 对齐的 ID 来源和恢复提示

### 现象与证据

远端 `20260706-115631-802741` 中：

- `record_goal_progress` 使用自造 validation id `four-repo-followup-audit`。
- 工具只返回 `unknown validation id`，浪费一个 turn。

后续 `5171cfa` 改善了 `update_goal.criteria_statuses` / `validation_statuses` 的 schema 描述和错误提示，但没有完整覆盖 `record_goal_progress`：

- 顶层 `validation_ids` 只写“Validation ids related to this progress”。
- `feature_updates[].id`、`milestone_updates[].id`、`validation_updates[].id` 没有说明必须来自 `get_goal`。
- `applyFeatureProgressUpdate`、`applyMilestoneProgressUpdate`、`applyValidationProgressUpdate` 的 unknown-id 错误仍不列合法 id，也不说明无对应 item 时应省略字段。

### 优化方案

1. 所有 ID-based progress 字段统一说明：id 必须从 `get_goal` 当前快照读取，是系统分配 id，不接受自由文本标签。
2. 复用/扩展 `unknownGoalItemHint`，对 feature、milestone、validation 返回合法 id 列表；列表为空时提示省略该 update 字段。
3. 对 `claimed_assertions`、milestone `validation_ids` 等引用字段说明其来源是 mission validation contract id；不要等到 coverage gate 才让模型知道引用无效。
4. 错误保持 directive recovery：先 `get_goal`，再用精确 id 重发；不要新增固定工作流 guard。

### 验收标准

- `record_goal_progress` 的 schema/description 对所有 ID 字段给出来源。
- unknown feature/milestone/validation id 错误包含合法 id；无合法项时明确提示省略。
- 单元测试覆盖普通 Goal validation plan 与 Mission validation contract 两条路径。
- 与 `update_goal` 的同类错误文案和恢复方式一致。

---

## - [ ] T5 [P2] discovery 工具对 session artifact path 返回误导性的 `not_found`

### 现象与证据

`20260709-035900-a9a207` 中模型对 harness 刚返回的 artifact path 使用 `grep`：

- `grep(path="artifacts/tool-outputs")`
- `grep(path="artifacts/tool-outputs/shell-call_OX4.txt")`，同一路径重复 2 次

结果不是“artifact 只能用 `read_file` 读取”，而是 workspace 下路径不存在的普通 `not_found`。这会让模型误以为 artifact 丢失，随后继续猜路径或重跑命令。

### 根因

- `defGrep()` / `defGrepFiles()` 先调用 `resolveGrepRoot()`。
- session artifact 位于 session root，不在 workspace root，`resolveGrepRoot()` 先 `Lstat` 失败并返回 `not_found`。
- `isInternalGeneratedArtifactInput()` 检查位于 resolver 成功之后，因此对 session-relative `artifacts/tool-outputs/...` 永远到不了正确提示分支。
- `glob` 也缺少等价的前置 lexical check。

### 优化方案

1. 在 `resolveGrepRoot()` 之前识别 `artifacts/tool-outputs` 和其子路径。
2. `grep` / `grep_files` / `glob` 返回统一结构化错误：session ephemeral artifact 不参与 discovery；使用 `read_file` 和工具结果给出的精确 path/offset。
3. tool description 明确 discovery 工具不搜索 session artifacts；`read_file` 是唯一受支持的读取入口。
4. 该错误归类为 `harness_error` 或单独的 `unsupported_path_source`，不要伪装成 `not_found`。

### 验收标准

- 三个 discovery 工具对 artifact 目录和 artifact 文件都返回相同的定向恢复提示。
- 提示包含原 path，并明确要求 `read_file`，不建议猜测或重跑命令。
- 普通 workspace not-found 仍保持现有 discovery-first 提示。
- 添加 session09 形状的回归测试，覆盖重复调用也不会产生不同错误。

---

## 2. 建议实施顺序

1. T1：先消除 agent-loop 中大规模的额外 provider/read_file 往返。
2. T6：先恢复前端测试门禁，避免 T2 修改在红色 baseline 上开发。
3. T2：修复用户已明确复现的 Workspace 目录保持问题。
4. T3：先同步 spec，再收紧 completed continuation 的跨事实源一致性。
5. T4：补齐 Goal progress 的模型可恢复性。
6. T5：收敛 artifact path 的 discovery 错误语义。

每项应独立完成 focused test、相关 package test、`node --test validation/scripts/webconsole_utils_test.mjs` 或浏览器 smoke（按改动面选择）、`go build ./...`，再单独提交。T1 和 T3 触及共享 runtime / session contract，完成后还需要跑完整回归并更新远端部署做真实 session 验证。
