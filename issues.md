# Session 20260423-092356-5ad621 复盘问题清单

本文先基于当前 durable 事实做复盘；后续“修复状态”记录本轮代码与文档闭环。

分析依据主要来自：

- `.go-cli-agent/sessions/20260423-092356-5ad621/session.json`
- `.go-cli-agent/sessions/20260423-092356-5ad621/state.json`
- `.go-cli-agent/sessions/20260423-092356-5ad621/messages.jsonl`
- `.go-cli-agent/sessions/20260423-092356-5ad621/events.jsonl`
- `reports/assessment-report.md`
- `reports/progress.md`
- `reports/validation.md`
- 当前仓库 provider/runtime 实现，以及 `../codex` 的 provider timeout/stream timeout 实现

## 一、执行摘要

这个 session 已经完成，但完成质量和执行效率都有明显问题，且不是单点问题，而是多个机制叠加后的结果：

- 任务过程明显发散，durable 证据显示总共发生了 `293` 次 `provider.call`、`486` 次工具调用，其中 `321` 次是 `read_file`、`126` 次是 `shell`，但 `todo_write=0`、`task_* = 0`。
- 长任务没有用 durable task/todo 把目标收敛住，用户中途纠偏后也没有被稳定吸收。
- 报告生成阶段并不稳定。用户在 `2026-04-23 11:07:25` 明确要求“直接生成正式的安全评估报告”后，session 仍然因为 provider timeout 连续失败，直到 `2026-04-23 11:17:19` 才首次成功写出 `reports/assessment-report.md`。
- 更严重的是，最终主报告与最后落盘的 `progress.md` / `validation.md` 出现结论冲突，说明“报告写出成功”不等于“最终交付一致且可信”。
- 修复前 repo 的 provider 超时模型也不适合长文本/慢代理：本仓库当时是单一 `http.Client.Timeout=120s`，而 `../codex` 是“请求层不强塞单个总超时 + 单独的 stream idle timeout + 独立 retry 配置”的拆分模型。

## 二、和用户提出问题的对应关系

### 1. “任务执行过程过于发散”

对应问题：

- 问题 A：目标漂移后没有被强制重新锚定
- 问题 B：长任务没有 durable 任务板，靠上下文硬撑
- 问题 C：compaction 和重复 reread 过多，进一步放大发散

### 2. “最后生成报告过程中重复了好几次才成功生成”

对应问题：

- 问题 D：provider timeout 后只能人工 continue，报告阶段没有 bounded auto-resume
- 问题 E：finalization 不是原子过程，先写主报告，再补 supporting docs，最后没有做一致性回校验

### 3. “是否可以延长 provider 的 timeout，因为长文本生成需要时间比较长，timeout 可以参考 ../codex 代码的情况”

对应问题：

- 问题 F：当前 provider timeout 模型过于粗糙，不适合长文本和慢代理

### 4. “分析完整流程，看看是否还有其他可以优化的问题”

补充问题：

- 问题 G：用户纠偏后的目标 `/sim` 没有进入最终产物
- 可信环境下保留原始请求材料：用户已确认运行环境可信，不做默认脱敏，避免影响 agent 效果和复现能力
- 问题 I：主报告与 supporting docs 结论冲突

## 修复状态

本轮已完成以下修复：

- P0 目标一致性：runtime 现在会从最新外部用户消息/steer 中识别显式目标锚点，例如“原始目标是 .../sim”，并在写最终报告、更新 project-memory artifact 或 `finish` 前强制检查最终产物是否反映最新目标；该 guard 在 `yolo` 模式下也保留，因为它属于显式用户指令一致性，不是普通 retrieval guard。
- P0 报告一致性：如果 `reports/progress.md` 或 `reports/validation.md` 在最终报告之后又发生写入，`finish` 会被阻断，要求先重写/编辑最终报告；如果最终报告声称“无认证/匿名/未授权 code=0 成功”，但 supporting docs 记录 no-Authorization `401`、`bearer token` 保护或“暂未发现明确未授权”，写报告或 finish 都会被阻断。
- P1 provider timeout/retry：provider 配置新增 `request_timeout_sec` 与 `stream_idle_timeout_ms`，旧 `timeout_sec` 仅保留兼容；effective timeout/retry policy 现在一并写入 session metadata，并在 continue/resume 时恢复。
- P1 bounded auto-resume：provider call 在没有新工具副作用前遇到 `upstream_timeout` 时，会按 `runtime.provider_auto_resume.max_attempts` 自动续跑，并写出 `provider.auto_resume` 事件，避免报告收尾阶段每次 timeout 都需要人工 continue。
- P1 长任务 taskboard：即使在 `yolo` 模式下，如果会话已经明显过长且仍没有 `todo_write` / `task_*` 事实源，`finish` 会被 `long_run_taskboard` guard 阻断，要求先写入一个可恢复的 durable task 状态。
- P1 compaction storm：新增 `runtime.compact.hysteresis_delta_chars` 和 state 水位；一次真实 compaction 后，如果上下文增长没有超过 delta，会生成 compacted view 但只写 `compact.reused` 事件，不再每轮重写 summary artifact。
- 凭据原文落盘：用户已确认运行环境可信，本轮移除所有默认脱敏操作，包括 `reports/` 写入脱敏与 hook/prompt 字段替换，保留原始内容以保证 agent 判断和请求复现效果。

本轮验证：

- `go test ./internal/config ./internal/provider ./internal/runtime ./internal/tools ./internal/app`

## 三、问题明细

## 问题 A：任务发散，且用户纠偏后没有被稳定吸收

### 现象

- 用户在 `2026-04-23 10:46:33` 明确指出：“你已经发散了，原始目标是 `.../ikvm/sim`；进行中文报告产出”。
- 但最终落盘的主报告 `reports/assessment-report.md` 仍然把目标页面写成 `.../ikvm/list`，没有切换到用户纠偏后的 `/sim`。

### 证据

- `messages.jsonl:545`：用户明确把目标纠正为 `/sim`
- `reports/assessment-report.md:4`：最终报告仍然写的是 `/list`

### 原因分析

- 当前 session 在长时间运行中没有 durable 的“当前目标”重绑定机制。
- 用户的纠偏消息只是作为新的 user turn 进入会话，但没有触发：
  - 旧目标失效
  - 已有 `spec/plan/report` 草稿失效
  - 任务板重新收敛
  - runtime completion mode 切换到“只为最新目标收尾”
- 在大量 reread、compaction 和 continue 之后，旧目标仍然残留在上下文里，最终污染了主报告。

### 建议方案

- 在用户明确纠偏目标时，runtime 应触发“目标重锚定”流程：
  - 把最新目标写入 durable session metadata
  - 对旧 `reports/spec.md`、`reports/plan.md`、`reports/assessment-report.md` 标记 stale
  - 自动注入强提醒，要求后续只围绕新目标执行
- 当用户消息包含明显的“原始目标是”“改成”“只针对”一类语义时，应升级为高优先级 steering override，而不是普通 user message。
- 当目标发生变化时，应优先刷新 `reports/spec.md` 和 `reports/plan.md`，而不是继续沿用旧 artifact。

## 问题 B：长任务没有 durable task/todo，导致过程靠上下文硬撑

### 现象

- 整个 session 工具调用很多，但没有任何 `todo_write` 或 `task_*` 工具调用。
- `session.context.loaded` 在整个流程中反复显示 `task_count=0`、`todo_count=0`。

### 证据

- durable 统计：
  - `provider.call = 293`
  - `tool.before = 486`
  - `read_file = 321`
  - `shell = 126`
  - `compact.finished = 226`
  - `todo_write = 0`
  - `task_* = 0`

### 原因分析

- 当前长任务没有被强制外化成“阶段目标 -> 子步骤 -> 收尾条件”。
- 于是 runtime 只能靠模型上下文记忆“当前到底在做什么”，一旦 turn 多、文件多、继续次数多，就容易变成“继续读文件/继续探测/继续补材料”，而不是稳定推进到交付。
- 这也是为什么用户已经明确要求收敛到中文报告产出后，session 仍然经历了很多无效回环。

### 建议方案

- 给长任务增加 bootstrap 规则：
  - 当 provider call 或 tool call 超过阈值时，强制要求至少落一个 `todo.json` 或 `tasks/` 状态。
- 在“用户要求产出报告/方案/文档”之后，runtime 应把任务切换到 completion-oriented 模式：
  - 不再允许无限制 repo-scale reread
  - 优先写 artifact
  - 只保留少量 proof reread 配额
- 把 “spec / plan / progress / validation / final-report” 视为一个显式的交付状态机，而不是零散文件。

## 问题 C：compaction 压力过高，反复 reread 已经写过的产物，进一步放大发散

### 现象

- 整个 session 产生了 `226` 次 `compact.finished`。
- 在报告阶段前后，`reports/spec.md`、`reports/plan.md`、`reports/progress.md`、`reports/validation.md` 被多次 reread。
- 到接近结束时，`compact.finished` 事件里的 `input_chars` 仍在 `668443`、`671647`、`677047` 这个级别反复出现。

### 证据

- `events.jsonl` 中多次出现高 input chars 的 `compact.finished`
- `reports/progress.md` / `reports/validation.md` 在 `09:48` 到 `11:18` 之间多次被 reread

### 原因分析

- compaction 触发后，并没有把会话真正拉回到一个“稳定的小上下文工作集”。
- 相反，模型仍在继续把已经存在的 `spec/plan/progress/validation` 反复读回上下文，导致：
  - 每轮都很容易再次触发 compaction
  - 旧目标和旧结论更难被真正淘汰
  - 报告阶段的 token 预算被 bookkeeping 消耗

### 建议方案

- 引入 compaction hysteresis：
  - 不是超过阈值就几乎每轮 compact
  - 而是 compact 一次后，只有增长超过一个 delta 才允许再次 compact
- 对已经写出的 `spec/plan/progress/validation` 建立“摘要指针”而不是全文 reread。
- 对报告阶段设置更严格的 reread 白名单，只允许少量 targeted proof reread。

## 问题 D：报告生成阶段需要多次人工 continue，缺少 bounded auto-resume

### 现象

- 用户在 `11:07:25` 明确要求“直接生成正式的安全评估报告”。
- 之后 session 连续因 provider timeout 失败：
  - `10:50:54`
  - `11:05:29`
  - `11:12:04`
- 用户不得不多次输入 `continue` / “继续，落评估报告” 才最终完成。

### 证据

- `events.jsonl:2692`
- `events.jsonl:2714`
- `events.jsonl:2825`
- `messages.jsonl:561`
- `messages.jsonl:568`

### 原因分析

- 当前失败发生在 provider 调用阶段，且失败前没有新的工具副作用，这类失败其实属于相对安全的“可重试恢复”场景。
- 但 runtime 在耗尽 transport retry 后直接把 session 打成 `failed`，然后把恢复责任完全交给用户手工 `continue`。
- 对 operator 来说，这会表现成“明明已经进入最后写报告阶段，却要手工推好几次”。

### 建议方案

- 对“失败点位于 provider.call，且本轮尚未执行任何工具”的场景，增加 bounded auto-resume：
  - 例如只允许自动续跑 1 到 2 次
  - 仍然保留 durable event 证据
- 当最新用户消息已经是“直接生成报告”“继续落报告”“finish”一类 completion-oriented 指令时，provider timeout 后应优先自动恢复原意图，而不是要求人工重新继续。
- Web/CLI 界面应把这类失败明确标记为“可自动续跑的 provider timeout”，避免 operator 误以为逻辑失败。

## 问题 E：finalization 不是原子过程，主报告和 supporting docs 可以互相打架

### 现象

- `11:17:19` 首次写出了 `reports/assessment-report.md`
- 之后仍继续：
  - `11:18:33` 编辑 `reports/progress.md`
  - `11:18:33` 编辑 `reports/validation.md`
  - `11:19:13` 再写 `reports/progress.md`
  - `11:20:19` 再写 `reports/validation.md`
  - `11:20:26` 才 `finish`

### 证据

- `events.jsonl:2861-2862`
- `events.jsonl:2870-2873`
- `events.jsonl:2892-2902`
- `events.jsonl:2910-2915`

### 原因分析

- 当前 finalization 更像是“先写一个主报告，再继续补 supporting docs，再 finish”。
- 但一旦 supporting docs 在主报告之后又被改写，runtime 没有强制要求：
  - 重新校验主报告
  - 或同步刷新主报告
- 结果就是文件虽然都在，但并不保证它们是同一个最终结论。

### 建议方案

- 把报告收尾做成原子阶段：
  1. 冻结最终证据摘要
  2. 先写 `progress.md` / `validation.md`
  3. 基于冻结摘要生成主报告
  4. 对主报告做一致性校验
  5. 通过后才允许 `finish`
- 如果主报告写完后 supporting docs 又发生变化，必须重新标记主报告 stale。
- `finish` 不应只检查“主报告文件存在”，而应检查“主报告与 supporting docs 的时间和内容一致性”。

## 问题 F：provider timeout 模型过于粗糙，不适合长文本生成和慢代理

### 现象

- 这个 session 的 durable metadata 里，provider retry policy 仍是：
  - `max_attempts = 2`
  - `base_delay_ms = 1000`
- 修复前 repo 的 provider HTTP 超时模型是：
  - `internal/runtime/runner.go`：默认 `providerTimeout = 120s`
  - `internal/provider/http.go`：`http.Client{Timeout: 120 * time.Second}`
- 失败文本明确是：
  - `context deadline exceeded (Client.Timeout exceeded while awaiting headers)`

### 参考 `../codex` 的差异

- `../codex/codex-rs/core/src/model_provider_info.rs`
  - 默认 `DEFAULT_STREAM_IDLE_TIMEOUT_MS = 300000`
  - 默认 `DEFAULT_REQUEST_MAX_RETRIES = 4`
- `../codex/codex-rs/codex-api/src/provider.rs`
  - provider 配置里把 `retry` 和 `stream_idle_timeout` 分开
- `../codex/codex-rs/codex-api/src/provider.rs`
  - request 对象本身是 `timeout: None`
- `../codex/codex-rs/codex-api/src/endpoint/responses.rs`
  - Responses 路径实际依赖 stream idle timeout 处理长流

### 原因分析

- 修复前 repo 对 Responses / openai-compatible 代理仍采用单一 unary-style `http.Client.Timeout=120s`。
- 对慢代理或长文本生成来说，这种模型会把“慢但仍在正常输出路径上”的情况直接打成 transport timeout。
- 它没有区分：
  - 首包 header 等待超时
  - 流式过程中长时间 idle
  - 整体请求 wall-clock 过长
- 这也是为什么长报告阶段更容易失败。

### 建议方案

- 参考 `../codex`，把 provider timeout 拆开：
  - `request_timeout_sec`
  - `stream_idle_timeout_ms`
  - 必要时再加 `overall_turn_timeout_sec`
- 对 Responses / openai-compatible 默认值建议不要继续只有一个 120s 总超时。
- transport retry 至少要覆盖到新 session 默认 `5` 次，并采用指数退避 + jitter，而不是固定 1s 退避。
- durable 事件里要明确区分：
  - `awaiting_headers_timeout`
  - `stream_idle_timeout`
  - `overall_request_timeout`
- Web 控制台和 `doctor` 应直接展示当前 effective timeout/retry 组合，避免 operator 误判。

## 问题 G：用户已纠偏到 `/sim`，最终交付仍围绕 `/list`

### 现象

- 用户在中途明确把目标改成 `/sim`
- 但主报告和 supporting docs 最终都仍然围绕 `/list` 和 `/system/info`、`/system/list` 展开

### 证据

- `messages.jsonl:545`
- `reports/assessment-report.md:4`

### 原因分析

- 这不是简单的“忘了更新一句话”，而是整个 artifact 链没有因为目标改变而重建。
- 所以后续所有报告产物都仍然沿用旧探测路径。

### 建议方案

- 当目标 URL/path 发生变化时，runtime 应自动把当前 `spec/plan/report` 标为 stale。
- 对报告类任务，最终 artifact 第一段应强校验当前目标字符串与最新用户指令一致。

## 问题 H：主报告和最终 supporting docs 结论冲突，交付可信度受损

### 现象

- `reports/assessment-report.md` 断言：
  - 无认证可直接读取资产详情
  - 高风险未授权访问已确认
- 但最终 `reports/progress.md` 与 `reports/validation.md` 断言：
  - 去掉 `Authorization` 会返回业务 `401 unauthorized`
  - `system/info` 至少受 bearer token 保护
  - “暂未发现明确 SQL 注入或未授权数据泄露”

### 证据

- `reports/assessment-report.md:48-63`
- `reports/assessment-report.md:89-117`
- `reports/progress.md:10-14`
- `reports/validation.md:14-18`
- `reports/validation.md:36-44`

### 原因分析

- 这是当前流程里最严重的问题。
- 它说明主报告是在某一版中间结论上生成的，后续 supporting docs 更新后，主报告没有被重新回写。
- 也就是说，系统目前缺少“主报告必须以最终 validation 为准”的硬约束。

### 建议方案

- 主报告生成前，先冻结 `validation.md` 的最终结论块。
- 报告生成后，做自动一致性检查：
  - 风险结论是否与 `validation.md` 一致
  - 目标页面是否与最新用户要求一致
  - 主报告是否引用了已经被后续更新推翻的结论
- 若不一致，直接阻断 `finish`。

## 非修复项：可信环境下保留原始请求材料

### 现象

- 任务过程中生成了包含原始 `Cookie` 和 `Authorization` 的普通 workspace 文件。
- 例如 `reports/ikvm-http-requests.ndjson` 直接落了完整请求头。

### 证据

- `reports/ikvm-http-requests.ndjson:1-2`
- 当前 `git status --short` 也显示这些凭据相关文件都在普通 `reports/` 下，且是未跟踪文件

### 当前决策

- 用户已确认运行环境可信，默认保留原始请求、token、Cookie、Authorization 等上下文，不做 runtime 层脱敏。
- 原因是脱敏会影响 agent 对真实请求材料的判断、复现和后续安全评估准确性。
- 因此本项不进入 P0/P1/P2 修复队列，也不实现自动替换、报告视图替换或 prompt/hook 字段替换。

### 操作边界

- runtime 不改写文件内容。
- runtime 不改写 prompt / hook payload 内容。
- 是否清理或隔离原始请求材料由 operator 手动决定，不作为 harness 默认行为。

## 四、优先级建议

### P0

- 解决“主报告与 supporting docs 不一致”问题
- 解决“用户纠偏后最终目标仍错误”问题

### P1

- 改造 provider timeout/retry/auto-resume 机制
- 给长任务补 durable task/todo 和 completion-oriented guard
- 抑制 compaction storm 和报告阶段重复 reread

## 五、结论

这次 session 暴露出的不是单一 bug，而是一条完整长任务链路的几个关键短板：

- 目标纠偏不够强
- 长任务缺 durable 收敛机制
- provider timeout 模型不适合长文本和慢代理
- 最终交付缺少“一致性校验”和“原子收尾”

如果只做单点修补，下一次很可能还是会以另外一种形式复现。更合理的做法是按下面顺序修：

1. 先修 P0：目标一致性和报告一致性
2. 再修 P1：timeout/retry/auto-resume + 长任务 completion mode
3. 凭据原文落盘按可信运行环境处理，不做默认脱敏
