# Go CLI Agent Tools And Skills Spec

## 1. 目标

工具和 skills 是 `go-cli-agent` 从“空循环”变成“可执行 harness”的关键能力层。

原则：

- 工具是原子动作
- skills 是按需知识与工具包
- tool registry 与 runtime 解耦
- 先支持本地 skills，不做远程插件系统

## 2. 内置工具清单

v1 内置工具固定为：

- `shell`
- `read_file`
- `write_file`
- `edit_file`
- `glob`
- `grep_files`
- `grep`
- `finish`
- `await_input`
- `load_skill`
- `get_goal`
- `create_goal`
- `update_goal`
- `get_plan_mode`
- `submit_plan`
- `request_user_input`
- `todo_write`
- `todo_read`
- `task_create`
- `task_update`
- `task_list`
- `task_get`
- `feature_list_create`
- `feature_list_update`
- `feature_list_read`
- `agent_spawn`
- `agent_wait`
- `agent_stop`
- `agent_prompt`
- `agent_status`
- `agent_list`

当前仓库默认还会向 session 工具面暴露一组扩展 phase 兼容工具，让 master agent 自己决定是否需要派生 child：

- `agent_spawn`
- `agent_wait`
- `agent_stop`
- `agent_prompt`
- `agent_status`
- `agent_list`

若部署方明确不希望暴露这组能力，可设置 `runtime.multi_agent.enabled=false` 把它们从 tool list 中移除。

## 3. 工具通用契约

每个工具都必须定义：

- `name`
- `description`
- `input_schema`
- `execute(ctx, workdir, args) -> ToolResult`

### 3.1 ToolResult

字段：

- `llm_output`
- `display_output`
- `metadata`
- `final`

其中：

- `llm_output` 供模型继续推理
- `display_output` 供 CLI 直接展示
- `final` 用于 `finish`
- `await_input` 通过 metadata 请求 runtime 进入可恢复等待，不设置 `final`

所有成功、失败、synthetic、interrupted 与 child-control ToolResult 都在 `tool.after` hook 之后、event/message 落盘之前执行统一 byte finalizer：

- `llm_output` 与 `display_output` 分别受 `runtime.tool_output` 的 byte cap；finalizer 必须 UTF-8-safe 地生成短 notice 与 bounded head/tail preview
- 超出 LLM inline cap 的原始结果写入当前 session 的 `artifacts/tool-outputs/`；模型只能获得精确 artifact path，不能通过 glob/grep discovery 枚举该目录
- metadata 固定包含 `tool_output_budget_version=1`、`raw_bytes`、`persisted_bytes`、`inline_bytes`、`omitted_bytes`、`artifact_path`、`artifact_complete`、`artifact_truncated`、`budget_reason`、`recoverable`；Display 另报 `display_raw_bytes`、`display_inline_bytes`、`display_omitted_bytes`
- finalizer 还要写入 `result_content_hash_version=1`、小写十六进制 `result_content_sha256`、`result_content_bytes`、`result_inline_bytes` 与 `result_content_hash_source`。普通新结果的 hash 基于 byte cap 前的原始 `llm_output`，source=`pre_budget_llm_output`；已经完成预算处理但缺少 hash 的兼容结果只能标记 source=`existing_inline_llm_output`，安全去重不得把这种兼容 hash 当作原始全文证明
- `raw/persisted/inline/omitted` 指 LLM channel：inline 完整时 `persisted=0, omitted=0, recoverable=true`；完整 artifact 时 `persisted=raw, omitted=0, artifact_complete=true, recoverable=true`；部分/未写 artifact 时 `omitted=raw-persisted, recoverable=false`
- artifact pointer 必须区分 `Complete artifact`、`Partial artifact` 和未保存；只有 `artifact_complete=true` 才能使用 complete/full 语义。quota 或写失败不得输出 `Full output:`
- finalizer 幂等；同一 ToolResult 因 event rollback/retry 再次经过时不得重复占用 quota 或改写 ToolCallID/Name/IsError/Final/既有业务 metadata
- 同步 child 的结构化状态/ID 位于 bounded preview；background notification 也保留 queue job/session reference 与相同预算产生的 artifact path

command 输出还要在进入通用 finalizer 前完成一层 source capture。该层与最终 provider byte cap 使用同一份 `runtime.tool_output` policy，但职责不同：

- process capture：`shell` 与 trusted skill command 把 stdout/stderr 接到同一个并发安全 writer；writer 按实际收到的顺序合并两个 stream，累计完整 `raw_bytes`，内存只保留固定大小的 pending prefix 与 head/tail preview
- inline preview：结果中的命令摘要、artifact notice 与 UTF-8-safe head/tail 一起受 `llm_output_max_bytes` / `display_output_max_bytes` 约束；preview 不是原始全文，也不能被后续层标成全文
- recoverable artifact：命令输出越过 inline boundary 后，collector 把尚未丢失的完整 prefix 和后续字节直接流入当前 session 的 quota-aware artifact stream；当前 ToolResult 立即返回 exact path，不等待 old-result ephemeral 处理
- artifact hard cap：单文件、session bytes 或文件数 quota 触发后，collector 继续累计真实 raw bytes 并维护 bounded tail，但只保存已经获准的 prefix；metadata 必须使 `persisted_bytes + omitted_bytes = raw_bytes`

command collector 生成的结果直接使用 `tool_output_budget_version=1` 完整 metadata contract。通用 finalizer 只有在 hook 改写导致 inline 长度或 metadata 对账失效时才重新处理；未改写结果不得复制第二份 artifact。command 自身的 `raw_length` / `truncated` 兼容字段继续保留：`raw_length` 等于 `raw_bytes`；`truncated` 表示原始 command output 没有完整内联在当前结果中，即使完整原文已经转存 artifact 也仍为 true。artifact 是否只保存 prefix 必须只由 `artifact_truncated` 与 `omitted_bytes` 判断，不能混用兼容字段。

## 4. 工具行为约束

### 4.1 `shell`

- 在 `workdir` 中运行
- 可选接受 `workdir` 覆盖；相对路径按当前 workspace 解析，解析后仍必须位于 workspace 内且是目录
- 对已注册 skill，`workdir` 也可使用 `load_skill` 返回的 skill 根目录提示；这只表示 skill bundle 的受控执行目录，不改变 workspace 写入边界
- 必须接受 timeout
- 禁止使用 `CombinedOutput()` 或等价的执行后全量内存缓冲；stdout/stderr 必须接入同一个 streaming collector，合并后只保留有界 UTF-8-safe head/tail preview，并把可保存的原始合并字节流写入当前 session artifact
- 返回码、timeout、workdir、sandbox、原始输出长度和截断状态必须写入 metadata，并以简短执行摘要进入 `llm_output`，避免模型只能在 UI/event metadata 中看到关键执行事实
- 默认只继承 allowlist 环境变量，避免把整个父进程环境泄露给子进程
- 轻量 `runtime.exec_policy.mode` 默认 `warn`，对提权命令、明显危险删除、secret path 写入和常见网络出站命令只写 metadata warning；显式设为 `deny` 时才阻断；设为 `off` 时不附加策略 metadata
- exec policy 只能作为安全/权限边界，不得演变为任务路线、审计路线、委派策略或交互审批 UI
- 当同一个远端响应需要多次统计或筛选时，应优先单次获取到临时快照后本地复用；只有外部状态已变化或新鲜度确实重要时才刷新，避免“先完整打印、再重新请求解析”的无效重复
- 不要把 pipe 数据和 heredoc 脚本同时送入同一个解释器 stdin，例如 `curl ... | python3 - <<'PY'`；heredoc 会占用 stdin，脚本内再读 `sys.stdin` 会得到 EOF。应使用临时文件、`python3 -c`，或让脚本自己发请求

#### 4.1.1 Command artifact stream

- session Store 只接受位于对应 session `artifacts/tool-outputs/` 下的 artifact root，cross-session root 即使属于同一个 Store 也必须拒绝；随后在短时 `.quota.lock` 临界区内创建 owner-only reservation 与 no-symlink temporary file。reservation 同时占用一个 file slot，并以分块方式预留可写字节；命令写入期间不得长期持有全局 quota lock，避免并行高输出命令互相阻塞 stdout/stderr drain
- 每次扩展 reservation 前重建当前 artifact usage；final files 按实际长度计费，active reservations 按 reserved bytes 计费，因此多个 Store/进程并发时总承诺量不能超过 `artifact_session_max_bytes` / `artifact_max_files`
- reservation 文件由活跃 writer 持有 OS file lock；进程崩溃会自动释放。后续 quota scan 必须回收没有活跃 lock 的 reservation 与 orphan temp，再重新计算 quota，不能让 crash 永久耗尽 session 配额
- stream 只允许持久化 prefix，达到 file/session hard cap 后不得绕过 quota 写 tail。collector 仍要继续消费进程输出、累计 raw bytes，并保留 bounded tail 供 inline preview
- preview 必须是合法 UTF-8；非法源字节用等长安全占位符显示，原始字节不能因此被冒充为完整 inline 文本。即使总字节未越过 inline cap，只要源包含非法 UTF-8，就要通过 artifact 保留 byte-exact 原文
- 正常结束、非零退出、command timeout、manual interrupt、child budget cancel 与 process-group kill 都必须 finalize collector。只有 artifact 写入、`fsync`、close 和 atomic no-replace rename 全部成功且 `persisted_bytes == raw_bytes` 时，才设置 `artifact_complete=true` / `recoverable=true` 并显示 `Complete artifact`
- quota 截断但成功发布的 prefix 设置 `artifact_truncated=true`、`recoverable=false`，使用 `Partial artifact` / `Recoverable prefix` 文案；create/write/fsync/close/rename 失败必须记录稳定 `budget_reason` 与 `artifact_error`，不得显示 complete/full
- command collector 没有有效 Store/session/tool-call context 时安全退化为 bounded preview + `artifact unavailable`；不得 panic，也不得为了补 artifact 重新缓存无界输出
- timeout 与 caller cancellation 的结果都必须保留 collector 已完成的 raw/persisted/artifact metadata。caller cancellation 仍返回 `failure_class=interrupted`，runtime 只覆盖错误分类和中断提示，不得丢弃已生成的 artifact 事实
- trusted skill command 使用相同 capture、quota、preview、timeout/cancel 与 metadata 契约，并标记为 ephemeral；skill 输入 stdin、工作目录、sandbox、exec policy 和最小环境规则保持不变

### 4.2 `read_file`

- 路径必须限制在工作区内
- 例外：已注册 skill bundle 文件属于只读资源根，允许用 `skills/<skill-name>/...`、`load_skill` 返回的绝对路径，或唯一匹配的 skill-relative 链接路径读取；不得把这些路径误解析成 `workspace/skills/...`
- skill 文件读取必须校验 symlink escape，且不赋予写入权限
- 例外：session 私有的 ephemeral tool-output artifact 允许用工具结果返回的显式路径只读读取；该路径必须落在当前 session 的 ephemeral artifact root 内，仍要拒绝 symlink escape，并使用同一套 line/byte 分页契约
- workspace 内 `.artifacts` 这类内部生成物仍默认拒绝读取；`glob` / `grep` / `grep_files` discovery 也必须跳过 session ephemeral artifacts，避免把临时大输出重新作为候选噪音回灌
- 当 `glob` / `grep` / `grep_files` 显式收到 `artifacts/tool-outputs` 或其子路径时，必须在普通 workspace/skill path resolver 之前返回稳定的 `unsupported_path_source` 错误：提示该 session artifact 不可 discovery，保留原始 path，并要求用 `read_file` 精确读取；不得把它归类为 workspace `not_found`，也不得建议猜路径或重跑生成命令
- 有两种互斥模式：line mode 使用 1-based `offset` / `limit`；byte mode 使用 0-based `byte_offset` / `byte_limit`。调用中出现任一 byte 字段时不得再出现 line 字段；byte mode 必须给出正数 `byte_limit`，`byte_offset` 省略时为 0
- line mode 保持默认与最大 120 行，仍通过 `ReadRegularFileNoSymlink` 执行 16 MiB source guard；旧调用不写 byte 字段时输出与语义兼容
- byte mode 只通过 `fileutil.ReadRegularFileRangeNoSymlink` 读取有界 range，调用方必须显式给出 `byte_limit`（建议常规页使用 16 KiB），最高 24 KiB，并进一步受当前 `runtime.tool_output.llm_output_max_bytes` 约束；不得先全量读取再切片。workspace / skill source 的总文件大小仍受 16 MiB guard，session tool-output artifact 可在 no-symlink exact-path gate 后读取更大的有界 range
- byte mode 只返回完整 UTF-8 rune：requested offset 落在 rune 中间时向前调整到下一个边界，requested end 落在 rune 中间时向后调整；返回 `requested_byte_offset`、`requested_byte_limit`、`effective_byte_start`、`effective_byte_end`、`start_adjusted`、`end_adjusted`、`returned_bytes`、`total_bytes`、`has_more`、`next_byte_offset` 与 `encoding=utf-8`
- 从 0 开始并始终使用工具返回的 `next_byte_offset` 可无重复、无漏 rune 地重组原文；人为从 rune 中间开始会得到明确的 start adjustment。窗口含非法 UTF-8 或 limit 小到无法容纳一个完整 rune 时返回稳定 typed error，不用替换字符伪装原字节
- line/byte 两种模式都必须让 header/body 一起落在模型可见 byte budget 内；超长 header 无法容纳最小可恢复窗口时返回 typed `output_budget_too_small`，不能靠最终 head/tail 截断破坏 continuation
- 返回结果包含实际窗口范围，便于模型继续定点读取

### 4.3 `write_file`

- 自动创建父目录
- 默认全量写入
- 新文件写入应采用原子替换流程，并为 agent 产物设置 owner-only 默认权限
- 必须拒绝写入 credential / private-key / secret 配置路径；该拒绝不仅基于用户输入路径，也要覆盖工作区内任意目录下已存在的敏感目录、敏感包配置路径和敏感文件名 symlink alias，避免通过普通目标路径间接改写敏感别名

### 4.4 `edit_file`

- 采用“查找旧文本并替换”的最小语义
- 若旧文本不存在则报错
- 与 `write_file` 共享同一套 workspace 写入策略，包括敏感路径和 symlink alias 检查

### 4.5 `glob`

- 返回相对工作区路径列表
- 支持可选 `limit` 控制返回路径数，默认与 `grep_files` 对齐、超大值必须被 cap 到实现上限；触发截断时在结果中给出可观测提示
- 默认跳过常见构建产物、缓存目录和内部生成物
- 支持与 `grep_files` 相同的 `cursor` / `byte_limit` page contract；真实 count/byte overflow 返回 source cursor，不把可重建路径列表复制成 tool-output artifact

### 4.6 `grep_files`

- 在工作区内递归搜索文本内容
- 例外：已注册 skill bundle 文件和目录属于只读资源根，允许用 `skills/<skill-name>/...`、`load_skill` 返回的绝对路径，或唯一匹配的 skill-relative 链接路径搜索；不得把这些路径误解析成 `workspace/skills/...`
- 仅返回命中文件路径，不返回整段上下文
- 用于先做便宜的候选文件发现，再配合 `read_file` 定点读取
- 默认跳过常见构建产物、缓存目录和二进制文件
- 实现必须按 stable walk order 收集 `effective_limit + 1` 个候选，只在确实存在第 `limit + 1` 项时返回 `has_more=true`；恰好等于 limit 不得误报不完整
- metadata 固定包含 `returned_count`、原始 `requested_limit`（省略/非正数保持原值）、`effective_limit`、`has_more`、`limit_capped`、`truncated_snippet_count=0`。真实 overflow 时模型可见输出还要提示缩小 `path` / `include` / `pattern`
- 同时受 path count 与模型可见总字节限制：`byte_limit` 默认 24 KiB、最高 32 KiB，并被 `runtime.tool_output.llm_output_max_bytes` 进一步收紧。metadata 增加 `requested_byte_limit`、`effective_byte_limit`、`byte_limit_capped`、`output_bytes`、`stop_reason=match_limit|byte_limit|complete`、`match_limit_reached`、`byte_limit_reached`
- `cursor` 是 v1 base64url opaque token，绑定 tool + resolved root/source + pattern + include 的 canonical fingerprint；limit/byte_limit 只是 page size，可在续页时调整。cursor 带 checksum、下一 current-view index 及有界的 last path/line 诊断，错误版本、损坏 token 或换 query 复用必须返回 typed cursor error

### 4.7 `grep`

- 在工作区内递归搜索文本
- 例外：已注册 skill bundle 文件和目录属于只读资源根，允许用 `skills/<skill-name>/...`、`load_skill` 返回的绝对路径，或唯一匹配的 skill-relative 链接路径搜索；不得把这些路径误解析成 `workspace/skills/...`
- 返回匹配文件、行号、片段摘要
- 支持可选 `include` glob 过滤，语义与 `grep_files.include` 一致
- 支持可选 `limit` 控制返回匹配行数，超大值必须被 cap 到实现上限
- 默认跳过常见构建产物、缓存目录和二进制文件
- 命中文件内容读取必须复用 capped regular-file reader；超出 16 MiB 的单文件应跳过或返回受控错误，不得完整读入内存后再截断
- 更适合“已经知道要找哪类证据，只差精确行号”的场景，而不是大范围初筛
- 集合完整性与单条 snippet 裁剪是两个独立维度：同样按 stable walk/line order 收集 `effective_limit + 1`，只有第 `limit + 1` 项存在时 `has_more=true`；返回项正文被截短只增加 `truncated_snippet_count`，不得据此推断还有未返回匹配
- metadata 固定包含 `returned_count`、`requested_limit`、`effective_limit`、`has_more`、`limit_capped`、`truncated_snippet_count`。兼容字段 `truncated` / `truncated_matching_lines` 只继续表示返回 snippet 被截短，不能表示 result-set overflow
- 真实 overflow 时模型可见输出提示缩小 `path` / `include` / `pattern`；恰好等于 limit 时不添加该提示
- 与 `grep_files` 共用 `byte_limit`、opaque `cursor`、fingerprint/version/checksum 与 stop metadata。count 和 byte 同点触发时 `stop_reason=byte_limit`，同时把 `match_limit_reached=true` 与 `byte_limit_reached=true` 写清楚
- 每个返回 record 的 metadata 包含 path、line、line byte range 与首个 match byte range；超长行 snippet 被收缩时，模型可见 record 同时显示 source/match byte span，随后可直接使用 `read_file` byte mode 展开，而不是重跑 grep
- page builder 在完整 record 边界停止，并为完整 cursor footer 预留预算；预算不足时先减少 record / snippet，绝不截断 cursor JSON。单个 path/header 连最小 recoverable record 都放不下时返回 typed `search_record_exceeds_byte_limit`

### 4.7.1 Search cursor current-view 语义

- cursor schema version 固定为 `1`，encoded token 最大 2048 bytes；cursor 只保存 bounded continuation state，不保存搜索正文
- continuation 重新扫描当前 workspace/skill view 并跳过 token 记录的稳定顺序位置，不承诺跨调用事务快照。两页之间外部修改可能改变 index 对应集合；这类 current-view/best-effort 语义必须由 metadata `snapshot_semantics=current_view` 明示
- 相同静态 view 上连续分页必须无重复、无漏项；query fingerprint 不匹配时不得静默从新 query 的错误位置继续

### 4.7.2 Read-only canonical arguments

- `read_file`、`grep`、`grep_files`、`glob` 共享一套 typed argument decoder/normalizer，工具执行和 provider-view fingerprint 必须调用同一实现；不得在 runtime 另写一套 map-based 参数解释
- `read_file` canonical form 包含 normalized path、line/byte mode、effective line offset/limit 或 effective byte offset/limit。line mode 中省略 offset、`0`、`1` 表示同一起点；省略/非正 limit 与显式默认值相同，超过上限的 limit 与上限值相同
- 搜索工具 canonical form 包含 tool、pattern、normalized path、include、effective count limit、effective byte limit 与 cursor；省略 search path 保留默认 workspace-root 语义，limit/byte_limit 使用执行路径的现行 default、cap 与 tool-output cap
- pattern、include、cursor 与具有文件名语义的 path 内容不得做改变查询含义的 trim/case folding。宁可因无法证明等价而不去重，也不能把不同来源或不同 page 合并
- canonicalization 只证明请求参数等价；最终是否折叠还必须比较 finalizer 的 result-content hash 和完整 result 语义。path/range 相同本身不证明文件内容未变化

### 4.8 `finish`

- 接受 `message`
- 不写文件，不做副作用
- 由 runtime 解释为显式完成信号
- 只用于实际完成的任务；未完成但受外部条件阻塞时使用 `await_input`，不能用 blocker 文案把 `finish` 当作通用停止工具

### 4.8.1 `await_input`

- 用于任务尚未完成，但因外部依赖、缺少用户决策或必须等待外部状态而无法继续的场景
- 接受 `kind = blocked | needs_input | external_wait`、必填 `reason`，以及可选 `blockers` / `resume_condition`
- 成功后当前 tool batch 停止，session 进入 `awaiting_input`，并把原因和恢复条件写入 state/event/tool result 事实
- 不设置 `final`，不把 session 标记为 completed，也不改变 active Goal 的状态
- 若存在 active Goal，模型应在 blocker 对恢复有长期价值时先用 `record_goal_progress` 记录；不得用 `await_input` 规避已经完成任务所需的 `update_goal(status=complete)` 与 `finish`
- 等待 background child 结果时继续使用 `agent_wait`；Plan Mode 提问继续使用 `request_user_input`

### 4.9 `load_skill`

- 接受 `name`
- 返回目标 `SKILL.md` 的完整内容与路径信息
- 当 skill 依赖相对 shell 路径时，返回值还应给出可直接复用的 skill 根目录执行提示，避免把 skill 内相对脚本误当成 workspace 根相对路径
- 返回值还应明确 skill references 是注册 skill bundle 下的只读资源，可通过 `read_file` / `grep` / `grep_files` 的 `skills/<skill-name>/references/...`、`load_skill` 返回的绝对路径，或唯一匹配的 `references/...` 访问，不属于 workspace 文件

### 4.9.1 Goal Tools

`get_goal`

- 读取当前 session 的 durable goal
- 无 goal 时返回 `null`

`create_goal`

- 仅在用户或系统显式要求 goal-driven work 时创建一个 current goal
- 默认只需要 objective；criteria、validation、features / milestones 等结构化信息应优先由 agent 在运行中拆分，保留为高级/兼容字段
- 已存在 current goal 时拒绝，避免模型无意覆盖用户目标

`update_goal`

- 模型侧只允许 `status=complete`
- pause / resume / clear / budget_limited 由用户或系统控制
- complete 前应基于文件、命令、events 或其他 session facts 做 completion audit

### 4.9.2 Plan Mode Tools

`get_plan_mode`

- 读取当前 session 的 Plan Mode snapshot
- 无 Plan Mode 时返回 `null`
- 用于规划阶段查看 objective、pending question、plan version、approval status 和 approved plan context

`submit_plan`

- 仅在 `planmode.status=planning` 时可用
- 接收 title、summary、完整 Markdown plan、assumptions、risks 与 verification 列表
- 写入 `planmode.json`、`artifacts/planmode-history.jsonl` 和 `artifacts/planmode-plan.md`
- 提交后当前 tool batch 必须停止，session 进入 `awaiting_input` + `phase=plan_approval`
- 该工具不执行计划，也不自动生成 todo/task/child/queue

`request_user_input`

- 仅 Plan Mode planning 阶段使用，且仅 root session 可用
- 一次请求 1-3 个短问题；每题必须有 2-3 个互斥选项，客户端可提供 free-form Other
- 有交互 responder 时可以同步等待回答；active Web handle 丢失或进程重启后，回答/取消必须通过已持久化的 `pending_request.tool_call_id` 补齐 tool result
- CLI 非交互且没有 responder 时，必须在写入 pending request 前返回可 replay 的工具错误

Plan Mode pending 时，provider tool schema 与 `CompletionController` 都必须只允许 read/search/load_skill、只读 goal/todo/task/feature-list、`get_plan_mode`、`request_user_input` 和 `submit_plan`；未知工具、skill command tools、workspace extension tools、mutating tools、agent/queue tools、`await_input` 与 `finish` 默认拒绝。

### 4.10 `task_update`

- 接受 `task_id` 以及任务字段增量更新
- 可更新 `status`、`priority`、`owner`、`subject`、`description`
- 可增删 `blocked_by` / `blocks`
- 任务完成时要自动清理依赖边

### 4.11 `todo_write`

- 用于高频更新 session 级 todo 列表
- 只表达“执行进度账本”，不表达依赖图，也不替代实际执行、验证或 `finish`
- 保留已有 todo 的顺序、内容和优先级；已完成/取消项不可删除或回退；新 todo 只能追加，不能直接新增为 completed/cancelled
- 允许多个互不依赖或并行推进的 todo 同时为 `in_progress`

### 4.12 `todo_read`

- 读取当前 session 级 todo 列表
- 供模型在长任务中回看当前执行节奏

### 4.13 `task_create`

- 创建持久化 task graph 节点
- 支持初始 `blocked_by`
- 自动维护双向依赖关系

### 4.14 `task_list`

- 列出当前 session 的全部 tasks
- 默认返回完整 task graph，也可用 `include_completed=false` 或 `status` 读取过滤后的任务视图
- 返回 ready / blocked / completed / cancelled / done 的派生视图

### 4.15 `task_get`

- 读取单个 task 的完整状态与依赖信息

### 4.16 `feature_list_create`

- 为当前 session 创建 durable 的 feature list 文件
- 适合多 feature / 多 wave 长任务，把 feature 粒度状态从自由文本计划中外置出来
- 每个 feature 至少包含 `description`，可选 `steps`
- 创建后默认初始化为 `pending` 且 `passes=0`

### 4.17 `feature_list_update`

- 按 `id` 更新单个 feature 的 `status` / `passes`
- 若目标 feature 不存在必须报错，而不是静默追加
- 更新后需要刷新该 feature 的 `updated_at`

### 4.18 `feature_list_read`

- 读取当前 session 的 feature list 完整快照
- 供模型在长任务中回看当前 feature 收敛状态

### 4.19 `agent_spawn`

- 默认注册到 session tool list
- 设置 `runtime.multi_agent.enabled=false` 时不注册
- 从当前 session 派生 child agent
- 支持同步执行和后台自治
- 支持 worktree / copy 隔离
- 未显式提供 `provider` / `model` 或传入 `default` 时，默认继承当前 parent session 的 provider / model
- `mode=full-auto` 作为兼容别名按 `exec` 处理
- `isolation_mode=workspace-write` 作为兼容别名按 `off` 处理
- 工具可见不代表 runtime 会自动 delegation；是否调用由当前 master agent 自主决定

### 4.20 `agent_wait`

- 默认注册到 session tool list
- 设置 `runtime.multi_agent.enabled=false` 时不注册
- parent agent 主动停车等待 background child work 的 durable result，并由 harness 在任一后台结果到达后自动恢复 parent
- `queue_job_id` 为兼容旧调用的可选字段；它不限制唤醒目标，恢复后由 parent agent 判断是否继续等待其他 child
- 如果已交付过相同 deadlock / liveness 通知且没有新的 pending background result，`agent_wait` 返回控制给 parent loop 并写入需要 master 介入的 reminder，不能把 parent 再次静默停在 `background_wait`
- child session 不允许再创建 sub-agent；只有 root master session 可以使用 `agent_spawn` 派生 child work

### 4.21 `agent_stop`

- 默认注册到 session tool list
- 设置 `runtime.multi_agent.enabled=false` 时不注册
- 停止尚未被 worker claim 的 queued background child job
- 也可显式结算 linked child 已因 `child_budget_turns_exceeded` / `child_budget_wallclock_exceeded` 暂停的 blocked job；job 进入 failed terminal 状态并释放 parent coordination gate，child 保留 paused 事实
- 不能停止 running child，也不能把 awaiting input、manual stop 或其他原因形成的 blocked child 越权结算

### 4.22 `agent_prompt`

- 默认注册到 session tool list
- 设置 `runtime.multi_agent.enabled=false` 时不注册
- 向当前 parent 名下的 running child session 或已启动 / blocked 且可恢复的 background child job 追加 durable steer prompt
- 用于 scope 收窄、补充证据要求、请求进度或 handoff、重定向 child；对 running child 走 steer，对 linked blocked child 可显式 continue；不创建、不取消、不标记 child work 完成
- `interrupt` 默认 `false`，避免普通 child steer 抢占正在自主探索的 sub-agent；只有明确需要抢占当前 provider/tool 边界时才显式传 `interrupt=true`

### 4.23 `agent_status`

- 默认注册到 session tool list
- 设置 `runtime.multi_agent.enabled=false` 时不注册
- 查询 child session 或后台 job 的状态

### 4.24 `agent_list`

- 默认注册到 session tool list
- 设置 `runtime.multi_agent.enabled=false` 时不注册
- 查询当前 session 的 child sessions 和关联 jobs

## 5. 工作区安全

所有文件工具统一使用 `safePath(workdir, rel)`：

1. `clean`
2. `abs`
3. 对已存在路径和父目录做 `EvalSymlinks`
4. 校验解析后的目标仍位于 `workdir`

任何越界路径直接报错。

## 6. Skills 目录结构

```text
skills/
  example/
    SKILL.md
    tools/
      hello.yaml
```

### 6.1 `SKILL.md`

可选 frontmatter：

```yaml
---
name: example
description: Example skill for local tasks
---
```

若无 frontmatter：

- `name` 默认为目录名
- `description` 默认为首段摘要

frontmatter 解析约束：

- `description` / `name` 允许 YAML 块标量（`>-` folded、`|-` literal）、引号值与多行值；解析必须还原真实文本，不能把块标量指示符（如 `>-`）当成描述本身
- 解析前需归一化 CRLF/CR 与 UTF-8 BOM，保证 Windows 编辑或带 BOM 导出的 `SKILL.md` 与 LF 版本结果一致
- runtime catalog 与 Web 控制台 `/api/skills` 必须共用同一套 frontmatter 解析逻辑（`internal/skills.ParseManifest`），不得各自实现 naive 逐行解析，避免同一 skill 在不同界面显示不一致的描述

### 6.2 `tools/*.yaml`

用于声明 skill 附带的命令型工具。字段：

- `name`
- `description`
- `command`
- `timeout_sec`（可选；省略时使用 configured runtime command timeout，显式值仍受 runtime 默认上限约束）
- `input_schema`

约束：

- `command` 必须是字符串数组，而不是 shell 字符串
- `name` 必须是 provider-tool 兼容名称，匹配 `^[A-Za-z_][A-Za-z0-9_-]{0,63}$`；空白、空格、点号、Unicode、数字开头或超长名称必须在 registry 阶段 fail fast，不能等到 provider 请求时失败
- built-in 工具名保留，不可覆盖
- direct-call skill command tool 必须从已解析的 skill 根目录执行，并把 cwd / sandbox bind source 绑定到 no-symlink 打开的目录；若 skill 目录在注册后、进程启动前被替换为 symlink，不得跟随新路径执行
- direct-call skill command tool 的 timeout、sandbox、exec policy 与环境变量过滤必须使用本次执行配置；结果 metadata 必须反映实际执行配置，而不是只反映 catalog/registry 创建时的配置

## 7. Skill 加载策略

system prompt 仅暴露：

- skill name
- short description

完整正文只在模型调用 `load_skill` 时注入。

理由：

- 对齐 `learn-claude-code` 的 s05 模式
- 降低默认上下文开销
- 保持技能发现与技能展开分离

## 8. Project Docs 加载

运行时应支持读取工作目录向上最近的 `AGENTS.md` 文件链，作为 project docs 注入 system prompt。

规则：

- 从 `workdir` 向上搜索到文件系统边界
- 近路径优先
- 仅注入 `AGENTS.md`，不自动注入其他 README / docs
- 这些内容是指令，不作为 skill 注册源
- 若上层 `AGENTS.md` 引用了工作区外路径，这些引用只作为意图提示，不能驱动模型反复重试越界读取

## 9. Tool Registry 行为

registry 负责：

- 注册 built-in tools
- 注册 skill tools
- 输出 provider 侧 tool schema
- 根据 name 路由执行

冲突规则：

- built-in tool 名称保留
- 同名 skill tool 默认拒绝加载
- v1 不做 override 机制

## 10. Planning 模型

本项目采用双层 planning：

- Layer A: `todo_write` / `todo_read`
  - 参考 `learn-claude-code` s03 与 `opencode` 的 session todo
  - 用于高频、短周期、单 agent 执行节奏控制
- Layer B: `task_create` / `task_update` / `task_list` / `task_get`
  - 参考 `learn-claude-code` s07 的持久化 task graph
  - 用于 durable goals、依赖、ready-state 与恢复后的继续执行

详细任务系统规则见 `spec/12-task-system.md`。

## 11. Tool Hooks 关联

运行时对每个工具调用执行顺序如下：

1. 触发 `tool.before`
2. 执行工具
3. 触发 `tool.after`
4. 恢复 ToolResult identity/flags/metadata，并执行统一 byte finalizer
5. 写 `tool.after` event，随后落盘 finalized tool result

hook 可修改最终进入模型的内容，但必须留下 trace。

## 12. 验收标准

- 内置工具可单独测试
- `load_skill` 可读取本地 skill
- skill tool 能被 registry 注册
- 越界路径被阻止
- `grep` / `grep_files` 的 exact-limit 与 true-overflow 可区分，metadata 不把 snippet 截短和集合不完整混为同一布尔值
- 只读 canonical normalizer 覆盖默认值/cap、line/byte mode、path/include/cursor 差异；result hash 与 canonical arguments 共同证明等价时才允许单-result provider-view 去重
- `todo_write` / `todo_read` 可稳定回放当前执行计划
- `task_create` / `task_update` / `task_list` / `task_get` 可维护完整 task graph
- `feature_list_create` / `feature_list_update` / `feature_list_read` 可维护 durable feature 状态
- `finish` 能驱动 session 完成
