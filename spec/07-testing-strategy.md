# Go CLI Agent Testing Strategy

## 1. 测试目标

测试要覆盖四件事：

- 运行时闭环正确
- provider 契约正确
- session / hooks / interrupt / awaiting_input 组合行为正确
- Web-first 控制台的关键用户路径正确
- spec 与实现没有明显偏移

测试不只验证编译通过，还要验证核心交互语义。

## 2. 测试层次

### 2.1 Spec Audit

在代码实现前，先做文档级校验：

- spec 是否与参考资料一致
- provider 契约是否与官方接口结构一致
- phase 切分是否与 `learn-claude-code` 的增量构建思路一致

### 2.2 单元测试

覆盖：

- config loader
- event bus
- session store
- hook manager
- tool registry
- path safety
- compaction estimator

### 2.3 Provider Contract Tests

覆盖：

- OpenAI adapter
- Anthropic adapter
- Google adapter

方式：

- `httptest.Server`
- 固定 JSON fixtures
- 验证请求体、header、错误映射、tool call 解析、tool result 回放

### 2.4 Runtime Integration Tests

覆盖：

- 单轮纯文本任务
- 带工具调用任务
- `finish` 结束
- `run` / `exec` 在 plain `done_candidate` 后继续 loop，直到显式 `finish` 或触发其它停止条件
- `exec` 模式 reminder + degeneration 回路
- running session `steer` -> 下一安全边界接纳
- running session `steer --interrupt` -> provider cancel / tool defer
- cancel -> `paused`
- `continue` 恢复

### 2.5 CLI Tests

覆盖：

- 子命令解析
- `init` 生成文件
- `sessions` 输出
- `tasks` 输出
- `steer` 参数解析
- `run` / `exec --plan` 与 `exec --plan-only` 参数解析
- `continue --plan` / `--approve-plan` / `--cancel-plan` 参数解析
- `exec --json`
- `exec` 未显式完成时退出码为 `6`
- `run` 的 `awaiting_input` 提示

### 2.6 Golden / Snapshot Tests

针对：

- JSONL 事件输出
- 文本模式阶段输出

保证 CLI 输出格式变更可审查。

### 2.7 Web Console Tests

Web-first v1 必须把本地 Web 控制台作为默认验收层，而不是只在触及实验扩展时补测：

- embedded shell 与静态前端 assets 能由同进程 service 稳定提供
- Web 发起的 `start` / goal create/pause/resume/clear/complete / mission plan approve / `continue` / `steer` / worker pool 更新都走真实 runtime 与文件事实；queue submit 保留在 CLI/API advanced 面，默认前端不提供独立提交表单
- Workspace upload / rename 走真实 HTTP API 和本地文件事实；覆盖 request/body 上限、同目录 no-replace rename、敏感路径、symlink/escape 拒绝、审计事件与前端 pending/refresh 行为
- headless browser UI smoke 能跑通关键交互链，且浏览器端无 runtime exception / console error
- focused live rerun 能对 durable retry restore 与 background notification dedup 产出独立证据目录
- focused retry-resume proof 采用 evidence-first 判定：以 durable `retry_policy` 元数据和真实 `provider.retry` 事件为主，不把 bounded finish nudges 后是否落成 `completed` 当作唯一通过条件

## 3. 测试依赖策略

- 优先使用标准库和 `httptest`
- 尽量不使用 mock framework
- 优先用 fake provider、fake tool、fake session store

## 4. 关键测试场景

### 4.1 Tools

- `read_file` 正常读取
- 越界路径被拒绝
- symlink 越界被拒绝
- `write_file` 创建父目录
- `edit_file` 替换失败时报错
- `finish` 设置 `final`
- `shell` 仅继承 allowlist 环境变量
- `todo_write` 只允许一个 `in_progress`
- `task_create` / `task_update` 保持依赖边双向一致
- task graph cycle 被拒绝

### 4.2 Hooks

- 顺序执行
- 修改 payload
- fail-open 不阻断
- fail-closed 阻断
- command hook 事件落盘

### 4.3 Session

- 创建 session 后文件存在
- `awaiting_input` 状态可恢复
- `paused` 状态可恢复
- `failed` 状态可恢复
- completed root session 可恢复，并写入带 `resumed_from=completed` 的 durable `session.resumed` 事件
- completed child / queue session 的通用 continue 被拒绝，且 child state、queue job、parent coordination 仍保持 completed 事实
- completed Goal 的 root follow-up 保留 Goal completion audit 与 complete 状态，不自动触发 active Goal completion gate
- `steer.jsonl` 只在接纳后转成真实 user message

### 4.3.1 Plan Mode

- `planmode.json`、`artifacts/planmode-history.jsonl` 与 `artifacts/planmode-plan.md` 可 round-trip
- pending Plan Mode 下 provider schema 只暴露规划 allowlist
- `CompletionController` 阻断 write/edit/shell/todo/task/goal mutation/agent/queue/custom tools 和 `finish`
- `submit_plan` 后进入 `awaiting_input` + `phase=plan_approval`，同批后续 tool call 得到合成错误 result
- `request_user_input` 有 responder、无 responder、active handle 丢失、回答和取消补偿路径都有测试
- approve 后追加 `meta.source=planmode_approval` 的 user message；revision 追加 `meta.source=planmode_revision`
- 以 pending Plan Mode session 为 parent 的 queue/delegate 提交被拒绝；独立新 session/job 不受影响

### 4.4 Interrupt

- provider 执行中 cancel
- tool 执行中 cancel
- `Esc` 逻辑触发 interrupt API
- tool cancel 后写入中断错误结果
- `steer --interrupt` 对不可取消工具退化为 deferred

### 4.5 Compaction

- 超过阈值时只压缩 provider 输入视图
- `messages.jsonl` 仍保留完整原始消息
- compaction artifact 被写入
- todo 与 task graph 不因 compaction 丢失

### 4.6 Providers

- OpenAI 文本输出
- OpenAI function call
- OpenAI `function_call_output` 回放
- Anthropic text + `tool_use`
- Anthropic `tool_result` 回放
- Google text + `functionCall`
- Google `functionResponse` 回放
- OpenAI / Anthropic / Google adapter 各自的非 2xx 错误映射
- OpenAI / Anthropic / Google adapter 各自的 context cancel 传播

### 4.7 Web Console

- embedded shell 首页与 `app.js` / `styles.css` 资产可直接从本地 service 获取
- Web 发起的 session `start` 能进入 `awaiting_input`
- focused retry-resume follow-up 里，`continue` 至少要保留 durable `retry_policy.max_attempts=2` 并真实写出 `provider.retry`
- 若 retry-resume proof 已拿到上述 durable evidence，而 bounded finish nudges 之后 session 仍停在 `awaiting_input`，应记录为 completion quirk / follow-up note，而不是误判成 retry-policy 失败
- worker pool 更新与 queue job 提交能落到真实 queue / child session 路径；worker scaling、queue notification 去重、queue job detail 与 retry proof 主要由 API / shell / service tests 覆盖
- todo store、`todo_write`、context-loaded event 与 compaction handoff 都能保留多个同时 `in_progress` 的 todo，不截断为单一 active item
- parent session 的 background notification 在 queue completion 与 stale-running reconcile 重叠时仍按 `queue_job_id` 去重
- 浏览器 UI smoke 当前覆盖 shell/assets 加载，Settings / Workspace / Skills / Sessions / Session 视图基础导航，Workspace 上传与文件重命名，start/continue 后的 session chrome、tool card、timeline 可见性、settled session polling 收敛和 history 清理；前端不再验收 Background / Queue UI，API 提交 queue job 后只验证后端 queue detail 与文件事实源
- 浏览器端 `runtime exception` / `console error` 必须为空

## 5. Fixture 组织

建议：

```text
internal/provider/openai/testdata/
internal/provider/anthropic/testdata/
internal/provider/google/testdata/
internal/runtime/testdata/
```

fixture 内容：

- 正常响应
- tool call 响应
- 错误响应
- 多轮回放样本

## 6. CI 期望

最小 CI 命令：

- `go test ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...`
- `gofmt -l cmd internal pkg validation/cmd` 结果为空
- `node --check internal/webconsole/assets/*.js`
- `node --test validation/scripts/webconsole_utils_test.mjs`

可选增强：

- race test
- Linux / macOS matrix

## 7. 阶段化验收

### Phase 0

- spec bundle 审核完成

### Phase 1

- 最小 loop 测试通过

### Phase 2

- 内置工具测试通过

### Phase 3

- session store 与事件输出测试通过
- todo 持久化测试通过

### Phase 4

- skill loading 测试通过

### Phase 5

- compaction 测试通过

### Phase 6

- task system 测试通过
- goal store、model tool、completion gate 与 CLI/Web 控制面测试通过；Web start 覆盖简单 Goal 开关使用 prompt 作为 objective 的默认路径

### Phase 7

- provider contract tests 通过

### Phase 8

- interrupt / continue / steer / awaiting_input 测试通过

### Phase 9

- hook 测试通过

### Phase 10

- CLI 集成测试和 golden tests 通过

## 8. 手工验证清单

除自动测试外，每轮实现完成后至少做以下手工验证：

- Web 控制台能启动并加载静态资源
- Web Session 工作区能新建 session
- Web 运行中 steer 能入队并在 timeline 可见
- Web awaiting_input / paused / failed session 与 completed root session 能 continue；completed child 返回 parent `agent_prompt` / queue requeue 恢复提示
- Web Goal / Plan Mode 控制能按文件事实展示和执行
- Settings provider/model 配置能保存并驱动后续 Web session start / continue；Session composer 不再暴露 per-session Advanced provider 面板
- `init` 是否生成正确配置
- `run` 是否能进入 loop 并自然停在 `awaiting_input`
- `steer` 是否能在运行中被接纳
- `Esc` 是否能暂停
- `continue` 是否能恢复
- `tasks` 是否能正确显示 ready / blocked
- `exec` 是否能在无 TTY 场景工作
- 若本轮触及 queue / continue 恢复链路，还要补一条 focused webconsole live 验证，确认 embedded assets、真实浏览器交互和 background notification 证据都落盘

## 9. 通过标准

功能完成不等于交付完成。只有在以下都通过时才算该 phase 完成：

- 单元测试通过
- phase 对应集成测试通过
- 至少一条真实 Web 手工路径通过
- 至少一条 CLI fallback 手工路径通过
- spec 与实现对照检查无关键偏差
