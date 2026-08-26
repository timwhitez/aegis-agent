(function bootstrapAegisI18n(global) {
  'use strict';

  const storageKey = 'aegis-agent.locale.v1';
  const defaultLocale = 'zh-CN';
  const supportedLocales = new Set(['zh-CN', 'en']);
  const sourceText = new WeakMap();
  const sourceAttributes = new WeakMap();
  const translatedAttributes = ['aria-label', 'title', 'placeholder', 'data-tooltip'];
  const rawContentSelector = [
    '[translate="no"]',
    '[data-i18n-skip]',
    'pre',
    'code',
    '.message-bubble',
    '.thinking-body',
    '.thinking-preview',
    '.tool-output-block',
    '.tool-json-block',
    '.tl-name',
    '.tl-id-chip',
    '.tl-body',
    '.timeline-card-data',
    '.notification-copy',
    '.agent-result-copy',
	'.agent-card-title',
	'.agent-card-copy',
	'.agent-card-meta',
	'.sa-tree-label',
	'.sa-tree-meta',
	'.session-rail-meta',
	'.session-rail-id',
	'.history-session-title',
    '.task-card-title',
    '.task-card-copy',
    '.todo-card-title',
    '.tf-row-label',
    '.tf-file-path',
    '.goal-objective',
    '.goal-raw',
    '.skill-name',
    '.skill-author',
    '.skill-desc',
    '.path-pill',
    '.tiny-code-chip'
  ].join(',');

  const zh = Object.freeze({
    'Agent Console': 'Agent 控制台',
    'Agent Console v2': 'Agent 控制台 v2',
    'Agent Session': 'Agent 会话',
    'Aegis Agent Console': 'Aegis Agent 控制台',
    'Agentic': '智能体',
    'New session': '新建会话',
    'New local session': '新的本地会话',
    'Session': '会话',
    'Sessions': '会话记录',
    'Skills': '技能',
    'Workspace': '工作区',
    'Settings': '设置',
    'Primary navigation': '主导航',
    'Local runtime': '本地运行时',
    'File-backed': '文件持久化',
    'LOCAL': '本地',
    'Inspector': '检查器',
    'Toggle inspector': '切换检查器',
    'Session inspector': '会话检查器',
    'Close inspector': '关闭检查器',
    'Stop': '停止',
    'Stop session': '停止会话',
    'Interrupt': '中断',
    'Interrupt session': '中断会话',
    'Send message': '发送消息',
    'Ask Aegis to work in this workspace…': '让 Aegis 在此工作区执行任务…',
    'Session composer mode': '会话输入模式',
    'Goal': '目标',
    'Plan': '计划',
    'Interrupt next send': '下次发送时中断',
    'Enter to send · Shift+Enter for new line': 'Enter 发送 · Shift+Enter 换行',
    'Capability library': '能力库',
    'Skills Engine': '技能引擎',
    "Enhance your agent's capabilities with community-driven tools.": '使用社区驱动的工具扩展智能体能力。',
    'Manage the local skills available to the agent.': '管理 Agent 可用的本地技能。',
    'Upload .zip': '上传 .zip',
    'Upload .zip Skill': '上传 .zip 技能',
    'Local files': '本地文件',
    'Browsing the current server workspace.': '正在浏览当前服务工作区。',
    'Browsing the current server workspace only. Switching roots is not available in this view.': '仅浏览当前服务工作区；此视图不支持切换根目录。',
    'Browse the active workspace and switch roots when needed.': '浏览当前工作区，并在需要时切换根目录。',
    'current cwd': '当前目录',
    'New folder': '新建文件夹',
    'Upload file': '上传文件',
    'Refresh workspace': '刷新工作区',
    'Refresh': '刷新',
    'Delete current folder': '删除当前文件夹',
    'Select a file': '选择文件',
    'Download selected file': '下载所选文件',
    'Download file': '下载文件',
    'Rename selected file': '重命名所选文件',
    'Rename file': '重命名文件',
    'Delete selected file': '删除所选文件',
    'Delete file': '删除文件',
    'Select a file to view its content…': '选择文件以查看内容…',
    'Choose a file or directory to inspect inside the current server workspace.': '选择当前服务工作区内的文件或目录进行查看。',
    'Loading workspace…': '正在加载工作区…',
    'Failed to load workspace.': '工作区加载失败。',
    'Workspace root': '工作区根目录',
    'Workspace files': '工作区文件',
    'Parent folder': '上级文件夹',
    'Create folder': '创建文件夹',
    'Folder name': '文件夹名称',
    'Upload': '上传',
    'Uploading...': '正在上传…',
    'Rename': '重命名',
    'Delete': '删除',
    'Delete selected': '删除所选项',
    'Clear selection': '清除选择',
    'Selected items': '所选项目',
    'No files in this folder.': '此文件夹为空。',
    'Preview unavailable.': '无法预览。',
    'Load more': '加载更多',
    'Loading more…': '正在加载更多…',
    'Ask anything...': '输入任何问题…',

    'Ready': '就绪',
    'Idle': '空闲',
    'Unknown': '未知',
    'Pending': '待处理',
    'In progress': '进行中',
    'Running': '运行中',
    'Queued': '已排队',
    'Blocked': '已阻塞',
    'Paused': '已暂停',
    'Completed': '已完成',
    'Complete': '完成',
    'Cancelled': '已取消',
    'Failed': '失败',
    'Error': '错误',
    'Accepted': '已接受',
    'Deferred': '已延后',
    'Awaiting input': '等待输入',
    'Awaiting approval': '等待审批',
	'Awaiting user input': '等待用户输入',
	'Awaiting plan input': '等待计划输入',
    'Agent Connected': '智能体已连接',
    'Disconnected': '连接已断开',
    'Planning': '规划中',
    'Executing': '执行中',
    'Active': '进行中',
    'Disabled': '已禁用',
    'Enabled': '已启用',
    'Default': '默认',
    'Custom': '自定义',
    'Off': '关闭',
    'On': '开启',
    'None': '无',
    'Low': '低',
    'Medium': '中',
    'High': '高',
    'Max': '最大',
    'Standard': '标准',

    'Start a session.': '开始一个会话。',
    'Answers, tool calls, and running flow will appear here. Use Sessions to reopen older sessions.': '回复、工具调用和执行流程会显示在这里。可在“会话记录”中重新打开历史会话。',
    'Waiting for the next update.': '等待下一次更新。',
    'Loading session': '正在加载会话',
    'Loading durable session detail and tool activity.': '正在加载持久化会话详情和工具活动。',
    'Restoring session': '正在恢复会话',
    'Loading the previously selected durable session.': '正在加载上次选择的持久化会话。',
    'Error restoring session': '会话恢复失败',
    'The session data could not be loaded.': '无法加载会话数据。',
    'Started a new session.': '已新建会话。',
    'Started a new session. The previous run may still settle in the background.': '已新建会话；上一次运行可能仍在后台收敛。',
    'Session continued.': '会话已继续。',
    'Failed to continue session.': '继续会话失败。',
    'Continue session': '继续会话',
    'Load earlier messages': '加载更早消息',
    'Loading...': '正在加载…',
    'Flow': '流程',
    'You': '你',
    'You · steer': '你 · steer',
    'System': '系统',
    'Agent': '智能体',
    'Harness': '运行框架',
    'Tool lane': '工具通道',
    'Background agents': '后台 Agent',
    'Background': '后台',
    'Thinking': '思考',
    'Show details': '显示详情',
    'Hide details': '隐藏详情',
    'Tool call': '工具调用',
    'Tool result': '工具结果',
    'No final text recorded.': '未记录最终文本。',
    'Open child session': '打开子会话',
    'Open parent session': '打开父会话',
    'Open timeline': '打开时间线',
    'Recent tool activity': '最近工具活动',
    'No tool activity yet.': '暂无工具活动。',
    'Durable timeline': '持久化时间线',
    'No timeline entries yet.': '暂无时间线记录。',
    'Summary': '摘要',
    'Context': '上下文',
    'Timeline': '时间线',
    'Tasks': '任务',
    'Tracker': '跟踪器',
    'No session loaded': '未加载会话',
    'No todo/task state recorded.': '尚未记录 Todo/任务状态。',
    'Session facts': '会话事实',
    'Agent role': 'Agent 角色',
    'Tool profile': '工具配置',
    'Provider': '提供商',
    'Model': '模型',
    'Reasoning effort': '推理强度',
    'Max output tokens': '最大输出 Token',
    'Mode': '模式',
    'Workdir': '工作目录',
    'Requested workdir': '请求的工作目录',
    'Isolation': '隔离',
    'Child budget': '子任务预算',
    'Loaded skills': '已加载技能',
    'Failure class': '失败类别',
    'Operator hint': '操作提示',
    'Checkpoint': '检查点',
    'Last error': '最近错误',
    'Master session': '主会话',
    'provider default': '提供商默认值',
    'Provider default': '提供商默认值',

    'Todo items': '待办条目',
    'Todo': '待办',
    'Ready tasks': '就绪任务',
    'Todo lane': '待办通道',
    'No todo items.': '暂无待办条目。',
    'Task graph': '任务图',
    'No persistent tasks.': '暂无持久化任务。',
    'Ready': '就绪',
    'Untitled todo': '未命名 Todo',
    'Task': '任务',
    'No description.': '无描述。',
    'Sub Agents': '子 Agent',
    'Sub agents': '子 Agent',
    'No sub agents yet.': '暂无子 Agent。',
    'File Changes': '文件变更',
    'No file changes yet.': '暂无文件变更。',
    'Expand': '展开',
    'Minimize': '收起',

    'Goal off': '目标关闭',
    'No durable Goal is attached to this session.': '此会话未关联持久化目标。',
    'Goal details': '目标详情',
    'Objective': '目标说明',
    'Success criteria': '成功标准',
    'Validation plan': '验证计划',
    'Progress': '进度',
    'Pause goal': '暂停目标',
    'Resume goal': '恢复目标',
    'Clear goal': '清除目标',
    'Complete goal': '完成目标',
    'Approve plan': '批准计划',
    'Plan approval': '计划审批',
    'Plan input': '计划输入',
    'Approve & Run': '批准并运行',
    'Ask for Changes': '要求修改',
    'Cancel': '取消',
    'Open Plan Input': '打开计划输入',
    'Submit': '提交',
    'Submit answer': '提交回答',
    'Submit plan': '提交计划',
    'Cancel Plan Mode': '取消计划模式',
    'Plan Mode': '计划模式',
    'No plan mode is attached to this session.': '此会话未启用计划模式。',
	'Input requested': '请求输入',
	'Input': '输入',
	'Other': '其他',
	'Submit answers': '提交回答',
	'Plan Mode input actions': '计划模式输入操作',
	'Open the pending Plan Mode question': '打开待回答的计划模式问题',
	'Plan Mode is waiting for your answer in the Plan inspector.': '计划模式正在等待你在计划检查器中回答。',
	'Plan Mode is waiting for your answer in the Plan inspector before it can continue planning.': '计划模式正在等待你在计划检查器中回答，然后才能继续规划。',
	'Answer the pending Plan Mode question before planning can continue.': '请先回答待处理的计划模式问题，然后才能继续规划。',

	'Child': '子会话',
	'Delegate': '委派',
	'Assistant message': '助手消息',
	'User message': '用户消息',
	'System message': '系统消息',
	'(empty message)': '（空消息）',
	'Assistant output': '助手输出',
	'Assistant output persisted': '助手输出已持久化',
	'Assistant text recorded.': '已记录助手文本。',
	'Tool results appended': '已追加工具结果',
	'Tool output recorded.': '已记录工具输出。',
	'Child session spawned': '已创建子会话',
	'Child session created.': '已创建子会话。',
	'Completion evaluate started': '开始完成度评估',
	'Completion evaluate finished': '完成度评估结束',
	'Completion gate passed': '完成门禁已通过',
	'Contract created': '已创建契约',
	'Durable runtime event recorded.': '已记录持久化运行时事件。',
	'Durable turn request assembled.': '已组装持久化轮次请求。',
	'Parent coordination parked': '父会话协调已暂停',
	'Parent coordination resumed': '父会话协调已恢复',
	'Plan input requested': '已请求计划输入',
	'Provider request completed': '提供商请求已完成',
	'Provider request failed': '提供商请求失败',
	'Request prepared': '请求已准备',
	'Session context loaded': '会话上下文已加载',
	'Session created': '会话已创建',
	'Session started': '会话已启动',
	'Starting turn': '正在开始轮次',
	'Turn stopped': '轮次已停止',
	'Webconsole handle acquired': 'Web 控制台已取得执行句柄',
	'Webconsole handle released': 'Web 控制台已释放执行句柄',
	'The runner is active. Tool calls and child-agent transitions will stream into this panel as durable events.': '运行器处于活动状态；工具调用和子 Agent 状态变化会作为持久化事件显示在此面板。',
	'Send a steer message into the running session...': '向运行中的会话发送 steer 消息…',
	'Click to open child session': '点击打开子会话',
	'Expand children': '展开子会话',
	'Collapse children': '收起子会话',
	'low': '低',

    'All durable sessions with parent-child hierarchy. Open one to inspect or continue it.': '所有持久化会话及其父子层级。打开会话即可检查或继续执行。',
    'Clear sessions': '清空会话',
    'No session data available yet.': '暂无会话数据。',
    'No saved sessions yet.': '暂无已保存会话。',
    'No sessions yet.': '暂无会话。',
    'No durable sessions yet.': '暂无持久化会话。',
    'Prev': '上一页',
    'Next': '下一页',
    'Open': '打开',
    'Open session': '打开会话',
    'Delete session': '删除会话',
    'Clear session history?': '清空会话历史？',
    'Clear all sessions': '清空所有会话',
    'Confirm action': '确认操作',
    'Confirm': '确认',
    'Enter value': '输入内容',
    'Value': '内容',
    'Use value': '使用此值',

    'No local skills found.': '未找到本地技能。',
    "Upload a .zip skill package to extend your agent's capabilities.": '上传 .zip 技能包以扩展 Agent 能力。',
    'Upload to Install': '上传并安装',
    'Uninstall': '卸载',
    'Uninstalling...': '正在卸载…',
    'Install': '安装',
    'Skill upload is already in progress.': '技能上传正在进行。',
    'Upload input not available.': '上传输入不可用。',
    'Skill uploaded and extracted successfully.': '技能已上传并成功解压。',
    'Failed to upload skill zip.': '技能 zip 上传失败。',
    'Skill uninstall cancelled.': '已取消卸载技能。',
    'Loading local skills…': '正在加载本地技能…',
    'Failed to load local skills.': '本地技能加载失败。',

    'Loading backend settings…': '正在加载后端设置…',
    'Configure runtime limits, provider defaults, local API credentials, and guardrails mode. API keys are persisted to the local env file for future restarts.': '配置运行限制、提供商默认值、本地 API 凭据和 Guardrails 模式。API Key 会写入本地环境文件供后续重启使用。',
    'Guardrails Mode': 'Guardrails 模式',
    'YOLO (default)': 'YOLO（默认）',
    'Frontend Migration': '前端迁移',
    'Legacy frontend': '旧版前端',
    'Enable legacy frontend rollback route': '启用旧版前端回滚路由',
    'Runtime Limits': '运行限制',
    'Global turn guard': '全局轮次限制',
    'Soft Checkpoint Turns': '软检查点轮次',
    'Hard Max Turns': '最大硬限制轮次',
    'Disable hard turn limit': '关闭硬轮次限制',
    'Child task budgets': '子任务预算',
    'Max Active Runtime': '最大活跃运行时长',
    'Max Elapsed Time': '最大总耗时',
    'Max Turns Per Attempt': '每次尝试最大轮次',
    'No turn limit': '不限制轮次',
    'Off, or 30m / 2h': '关闭，或填写 30m / 2h',
    'Off, or 2h / 1d': '关闭，或填写 2h / 1d',
    'Provider Defaults': '提供商默认值',
    'Default Provider': '默认提供商',
    'Provider Profile': '提供商配置',
    'API Provider': 'API 提供商',
    'Base URL': '基础 URL',
    'Model Name': '模型名称',
    'Reasoning Effort': '推理强度',
    'Max Output Tokens': '最大输出 Token',
    'Inherit default': '继承默认值',
    'Inherit adapter': '继承适配器',
    'Inherit provider base URL': '继承提供商基础 URL',
    'Inherit provider model': '继承提供商模型',
    'Inherit provider effort': '继承提供商推理强度',
    'Inherit provider limit': '继承提供商限制',
    'Role Provider Overrides': '角色提供商覆盖',
    'Planner': '规划者',
    'Generator': '生成者',
    'Evaluator': '评估者',
    'Explorer': '探索者',
    'API Key': 'API 密钥',
    'Keep existing key': '保留现有 Key',
    'Replace API key': '替换 API Key',
    'Save Settings': '保存设置',
    'Saving...': '正在保存…',
    'Settings saved.': '设置已保存。',
    'Saved': '已保存',
    'Failed to save settings.': '设置保存失败。',
    'Test Provider': '测试提供商',
    'Test Settings': '测试设置',
    'Save Changes': '保存更改',
    'Testing...': '正在测试…',
    'Provider probe succeeded.': '提供商探测成功。',
    'Provider probe failed.': '提供商探测失败。',
    'Duration': '时长',
    'Seconds': '秒',
    'Role': '角色',
    'Reasoning Summary': '推理摘要',
    'Text Verbosity': '文本详细度',
    'Thinking Budget': '思考预算',
    'Include Thoughts': '包含思考',
    'Prompt Cache': 'Prompt 缓存',
    'Store': '存储',
    'Temperature': '温度',
    'Top P': 'Top P',

    'YOLO disables non-essential runtime reminders and checks for new or resumed turns; tool safety boundaries still apply.': 'YOLO 会关闭新建或恢复轮次中的非必要运行时提醒与检查；工具安全边界仍然生效。',
    'Web Console v2 is the default operator surface. The original page remains packaged only as a short-term local rollback path.': 'Web 控制台 v2 是默认操作界面；原始页面仅作为短期本地回滚入口保留。',
    'When enabled, the original page is available at': '启用后，原始页面可通过以下路径访问：',
    'after save. It uses the same Aegis APIs and durable stores.': '保存后生效。它复用同一套 Aegis API 和持久化存储。',
    'The global turn guard applies to every session run. Child budgets are an additional delegated-work policy and are off by default.': '全局轮次限制适用于每次会话运行；子任务预算是额外的委派工作策略，默认关闭。',
    'Applies per run to master, foreground child, and background/queue child sessions.': '每次运行分别应用于主会话、前台子会话及后台/队列子会话。',
    'Disable global hard turn limit': '关闭全局硬轮次限制',
    'Soft is a one-time checkpoint reminder and never stops execution. Hard is disabled by default; when enabled it fails the current run with': '软限制只提醒一次检查点，绝不会停止执行。硬限制默认关闭；启用后会让当前运行以此错误失败：',
    'Sub-agent budget': '子 Agent 预算',
    'Only delegated child/background sessions are affected; the effective policy is snapshotted when work is created.': '仅影响受委派的子会话和后台会话；创建工作时会保存生效策略的快照。',
    'Enable sub-agent budget': '启用子 Agent 预算',
    'Active Runtime': '活跃运行时长',
    'Absolute Elapsed Deadline': '总耗时截止值',
    'Turns per Attempt': '每次尝试轮次',
    'Active runtime excludes paused/offline time. Absolute elapsed time includes queueing and pauses. Changes affect newly created child/job work only; existing work keeps its durable snapshot. A parent can extend/resume or cancel/settle a budget-paused child.': '活跃运行时长不计暂停和离线时间；总耗时包含排队与暂停。更改只影响新建的子会话或任务，现有工作保留其持久化快照。父会话可以延长或恢复预算暂停的子会话，也可以取消或结束它。',
    'OpenAI-compatible Responses': 'OpenAI 兼容 Responses',
    'Anthropic-compatible Messages': 'Anthropic 兼容 Messages',
    'Google Gemini': 'Google Gemini',
    'Context Window Tokens': '上下文窗口 Token 数',
    '0 uses the known-model table or the 200000-token default.': '设为 0 时使用已知模型表；未知模型默认使用 200000 Token。',
    'Reasoning Mode': '推理模式',
    'Auto': '自动',
    'Leave blank to keep existing persisted key...': '留空以保留已持久化的密钥…',
    'Optional provider settings for planner, generator, evaluator, and explorer sessions. Blank fields inherit the selected provider defaults.': '可为规划、生成、评估和探索会话设置可选提供商参数；留空字段继承所选提供商的默认值。',
    'Decomposition, plans, and handoff artifacts.': '任务拆分、计划和交接产物。',
    'Bounded implementation and drafting slices.': '有边界的实现与草拟工作。',
    'Independent review, audit, and validation passes.': '独立的审查、审计与验证。',
    'Read-only repository exploration with a bounded evidence handoff.': '只读探索仓库，并提供有边界的证据交接。',
    'GPT-compatible providers send reasoning_effort with the selected level.': 'GPT 兼容提供商会按所选级别发送 reasoning_effort。',
    'Visible thinking requires a summary setting and an upstream response that actually returns readable summary text.': '可见思考需要启用摘要设置，并且上游响应实际返回可读的摘要文本。',
    'Skill removed from the local catalog.': '技能已从本地目录移除。',
    'Session deleted.': '会话已删除。',
    'Sessions cleared.': '会话记录已清空。',
    'XHigh': '极高',
    'auto': '自动',
    'concise': '简洁',
    'detailed': '详细',
    '(no output)': '（无输出）',
    'Approved': '已批准',
    'Awaiting plan approval': '等待计划审批',
    'Best-effort view. Dedicated file tools are accounted directly; shell changes are inferred from recognized redirects and may be incomplete.': '尽力展示。专用文件工具会被直接统计；shell 变更根据可识别的重定向推断，可能不完整。',
    'Call': '调用',
    'Clear': '清除',
    'Completion audit': '完成审计',
    'Goal facts': '目标事实',
    'Files': '文件',
    'Final': '最终结果',
    'Final response captured': '已记录最终响应',
    'Mark Goal Complete': '将目标标记为完成',
    'No Plan Mode gate is attached to this session.': '此会话未关联计划模式门禁。',
    'No coverage, evaluator, child, queue, or blocker facts recorded.': '未记录覆盖率、评估者、子会话、队列或阻塞事实。',
    'Plan submitted': '计划已提交',
    'Provider call': '提供商调用',
    'Provider request cancelled': '提供商请求已取消',
    'Provider time': '提供商耗时',
    'Result': '结果',
    'Run stopped': '运行已停止',
    'Session completed': '会话已完成',
    'Start new session: Enter sends, Shift+Enter / Ctrl+Enter inserts a line.': '新建会话：Enter 发送，Shift+Enter / Ctrl+Enter 换行。',
    'The active run was stopped and can be reviewed or continued later.': '当前运行已停止，稍后可以检查或继续。',
    'The run finished cleanly.': '本次运行已正常完成。',
    'The run finished. Answers and tool results remain visible below.': '本次运行已结束；回复和工具结果仍显示在下方。',
    'The run was stopped. Partial output remains visible and you can continue later if needed.': '本次运行已停止；部分输出仍然可见，需要时可稍后继续。',
    'Todo / Tasks': '待办 / 任务',
    'Tokens': 'Token',
    'Tool Lane': '工具通道',
    'Tool execute': '工具执行',
    'Turn decide': '轮次决策',
    'Verification': '验证',
    'Version': '版本',
    'Waiting for the model provider.': '正在等待模型提供商。',
    'Task completion': '任务完成度',
    'Plan Mode approval actions': '计划模式审批操作',
    'Approve the submitted plan and start execution': '批准已提交的计划并开始执行',
    'Cancel Plan Mode for this session': '取消此会话的计划模式',
    'Send your input as a plan revision request': '将输入作为计划修订请求发送',
    'Session is not running': '会话未在运行',
    'Completed session loaded: next send adds a follow-up and continues this session with its existing context.': '已加载完成的会话：下次发送会添加后续消息，并使用现有上下文继续此会话。',
    'Plan Mode awaiting approval: next send requests changes; use Approve & Run to execute.': '计划模式正在等待审批：下次发送会请求修改；使用“批准并运行”开始执行。',
    'Continue Paused session: next send resumes this durable session.': '继续已暂停的会话：下次发送将恢复此持久化会话。',
    'Add a follow-up to continue this completed session...': '添加后续消息以继续此已完成会话…',
    'Ask for changes to the submitted plan...': '输入对已提交计划的修改要求…',
    'Best-effort view: dedicated file tools are accounted directly; shell changes are inferred from recognized redirects and may be incomplete.': '尽力展示：专用文件工具会被直接统计；shell 变更根据可识别的重定向推断，可能不完整。',
    'partial': '部分',
    'new': '新建',
    'high': '高',
    'medium': '中',

    'just now': '刚刚',
    'Retry': '重试',
    'Load context': '加载上下文',
    'Loading context budget and lineage telemetry…': '正在加载上下文预算和链路遥测…',
    'Context telemetry is loaded only when this inspector tab is opened.': '仅在打开此检查器标签时加载上下文遥测。',
    'No data': '无数据'
  });

  const zhPatterns = [
    [/^(\d+) in progress$/, '$1 个进行中'],
    [/^(\d+) blocked$/, '$1 个已阻塞'],
    [/^(\d+) cancelled$/, '$1 个已取消'],
    [/^(\d+) completed$/, '$1 个已完成'],
    [/^(\d+) failed$/, '$1 个失败'],
    [/^(\d+) total$/, '共 $1 个'],
    [/^(\d+) selected$/, '已选择 $1 项'],
    [/^Page (\d+)(\s*\/\s*(\d+))?$/, '第 $1 页$2'],
    [/^(\d+)-(\d+) of (\d+)$/, '第 $1-$2 项，共 $3 项'],
    [/^(\d+) recent sessions available in Sessions\.$/, '“会话记录”中有 $1 个最近会话。'],
    [/^Thinking \((\d+) chars\)$/, '思考（$1 字符）'],
    [/^(\d+) sessions?$/, '$1 个会话'],
    [/^(\d+) jobs?$/, '$1 个任务'],
    [/^(\d+) notifications?$/, '$1 条通知'],
    [/^(\d+) calls?$/, '$1 次调用'],
    [/^(\d+) children$/, '$1 个子会话'],
	[/^(\d+) child$/, '$1 个子会话'],
	[/^(\d+) calls? · (\d+) child$/, '$1 次调用 · $2 个子会话'],
	[/^(\d+) planning questions? waiting for an answer\.$/, '$1 个规划问题正在等待回答。'],
    [/^blocked by (\d+)$/, '被 $1 项阻塞'],
    [/^blocks (\d+)$/, '阻塞 $1 项'],
    [/^turn (\d+)$/, '第 $1 轮'],
    [/^attempt (\d+)$/, '第 $1 次尝试'],
    [/^Minimize (.+) panel$/, '收起$1面板'],
    [/^Expand (.+) panel$/, '展开$1面板'],
    [/^Open child session (.+)$/, '打开子会话 $1'],
    [/^Effective adapter: (.+)\.$/, '生效适配器：$1。'],
    [/^Provider test passed: (.+?)\. provider accepted request but returned no readable thinking in this probe\. Strategy: (.+)\.$/, '提供商测试通过：$1。此探测中提供商已接受请求，但未返回可读思考内容。策略：$2。'],
    [/^Deleted file (.+)\.$/, '已删除文件 $1。'],
    [/^Deleted folder (.+)\.$/, '已删除文件夹 $1。'],
    [/^(\d+)\/(\d+) done$/, '已完成 $1/$2'],
    [/^(\d+) active$/, '$1 个进行中'],
    [/^(\d+) todo items?$/, '$1 个待办'],
    [/^(\d+) todo items saved$/, '已保存 $1 个待办'],
    [/^(\d+) call(?:s)? · (\d+) result(?:s)?$/, '$1 次调用 · $2 个结果'],
    [/^(\d+) evidence$/, '$1 条证据'],
    [/^(\d+) files?$/, '$1 个文件'],
    [/^(\d+) results?$/, '$1 个结果'],
    [/^Completed · (.+)$/, '已完成 · $1'],
    [/^Paused · (.+)$/, '已暂停 · $1'],
    [/^Awaiting plan approval · (.+)$/, '等待计划审批 · $1'],
    [/^Continue (.+) session: next send resumes this durable session\.$/, '继续$1会话：下次发送将恢复此持久化会话。'],
    [/^Objective stored in Goal panel \((\d+) chars\)\.$/, '目标说明已保存在目标面板中（$1 个字符）。'],
    [/^Plan submitted for approval \(version (\d+)\)\.$/, '计划已提交审批（版本 $1）。'],
    [/^Review the submitted plan, then approve it to run, ask for changes, or cancel Plan Mode\. Summary: (.+)$/, '检查已提交的计划，然后批准运行、要求修改或取消计划模式。摘要：$1'],
    [/^Tool started: (.+)$/, '工具已启动：$1'],
    [/^Tool finished: (.+)$/, '工具已完成：$1'],
    [/^Latest Goal accounting updated · (.+)$/, '最近目标记账更新 · $1'],
    [/^Latest (.+) · (.+)$/, '最近事件：$1 · $2'],
    [/^by tool · (.+)$/, '由工具完成 · $1'],
    [/^goal updated (.+)$/, '目标更新于 $1'],
    [/^goal · Complete · (.+) · tokens (.+) · provider time (.+)$/, '目标 · 已完成 · $1 · Token $2 · 提供商耗时 $3'],
    [/^provider time (.+)$/, '提供商耗时 $1'],
    [/^tokens (.+)$/, 'Token $1'],
    [/^plan · Awaiting approval$/, '计划 · 等待审批'],
	[/^plan · Awaiting user input$/, '计划 · 等待用户输入'],
	[/^Awaiting plan input · (.+)$/, '等待计划输入 · $1'],
	[/^Parent coordination · (evt_.+)$/, '父会话协调 · $1'],
	[/^Prepare · (evt_.+)$/, '准备 · $1'],
	[/^Provider call · (evt_.+)$/, '提供商调用 · $1'],
	[/^Tool execute · (evt_.+)$/, '工具执行 · $1'],
	[/^Webconsole · (evt_.+)$/, 'Web 控制台 · $1'],
    [/^source (.+)$/, '来源 $1'],
    [/^status Complete$/i, '状态：完成'],
    [/^updated (.+)$/, '更新于 $1'],
    [/^(.+) · Interrupt$/, '$1 · 中断'],
    [/^(.+) · Turn decide$/, '$1 · 轮次决策'],
    [/^ID: (.+)$/, 'ID：$1'],
    [/^Stop session (.+)$/, '停止会话 $1'],
    [/^Goal Complete$/, '目标已完成'],
    [/^session Completed$/, '会话已完成'],
    [/^latest Goal accounting updated · (.+)$/, '最近目标记账更新 · $1'],
    [/^Select (.+)$/, '选择 $1'],
    [/^Download (.+)$/, '下载 $1'],
    [/^Rename (.+)$/, '重命名 $1'],
    [/^Delete (.+)$/, '删除 $1'],
    [/^Goal (.+)$/, '目标 $1'],
    [/^session (.+)$/, '会话 $1'],
		[/^latest (.+)$/, '最近 $1'],
		[/^Workspace \/ (.+)$/, '工作区 / $1']
  ];

  function normalizeLocale(value) {
    return supportedLocales.has(value) ? value : defaultLocale;
  }

  function readLocale() {
    try {
      return normalizeLocale(global.localStorage?.getItem(storageKey));
    } catch {
      return defaultLocale;
    }
  }

  let activeLocale = readLocale();

  function translate(value, locale = activeLocale) {
    const text = String(value ?? '');
    if (locale === 'en' || !text) return text;
    const leading = text.match(/^\s*/)?.[0] || '';
    const trailing = text.match(/\s*$/)?.[0] || '';
    const core = text.slice(leading.length, text.length - trailing.length);
    if (!core) return text;
    let translated = zh[core];
    if (!translated) {
      for (const [pattern, replacement] of zhPatterns) {
        if (pattern.test(core)) {
          translated = core.replace(pattern, replacement);
          break;
        }
      }
    }
    return translated ? leading + translated + trailing : text;
  }

  function shouldSkip(element) {
    if (!element || element.nodeType !== 1) return false;
    return Boolean(element.closest?.(rawContentSelector));
  }

  function matchesKnownRendering(current, source) {
    if (current === source) return true;
    for (const locale of supportedLocales) {
      if (current === translate(source, locale)) return true;
    }
    return false;
  }

  function translateTextNode(node) {
    const parent = node?.parentElement;
    if (!node || shouldSkip(parent) || parent?.hasAttribute?.('data-i18n-control')) return;
    const current = String(node.nodeValue ?? '');
    const previous = sourceText.get(node);
    let source = previous;
    if (!source || !matchesKnownRendering(current, previous)) {
      source = current;
      sourceText.set(node, source);
    }
    const next = translate(source);
    if (current !== next) node.nodeValue = next;
  }

  function translateElementAttributes(element) {
    if (!element || shouldSkip(element) || element.hasAttribute?.('data-i18n-control')) return;
    let originals = sourceAttributes.get(element);
    if (!originals) {
      originals = {};
      sourceAttributes.set(element, originals);
    }
    for (const name of translatedAttributes) {
      if (!element.hasAttribute?.(name)) continue;
      const current = element.getAttribute(name) || '';
      const previous = originals[name];
      if (!previous || !matchesKnownRendering(current, previous)) originals[name] = current;
      const next = translate(originals[name]);
      if (current !== next) element.setAttribute(name, next);
    }
  }

  function apply(root = global.document) {
    if (!root) return;
    if (root.nodeType === 3) {
      translateTextNode(root);
      return;
    }
    if (root.nodeType === 1) translateElementAttributes(root);
    const walker = global.document?.createTreeWalker?.(root, global.NodeFilter?.SHOW_TEXT ?? 4);
    if (walker) {
      let node;
      while ((node = walker.nextNode())) translateTextNode(node);
    } else {
      for (const node of root.querySelectorAll?.('*') || []) {
        for (const child of node.childNodes || []) {
          if (child.nodeType === 3) translateTextNode(child);
        }
      }
    }
    for (const element of root.querySelectorAll?.('*') || []) translateElementAttributes(element);
    updateLanguageControl();
  }

  function updateLanguageControl() {
    const control = global.document?.getElementById?.('language-toggle-btn');
    if (!control) return;
    const nextIsEnglish = activeLocale === 'zh-CN';
    const label = nextIsEnglish ? 'EN' : '中文';
    const accessible = nextIsEnglish ? 'Switch to English' : '切换到中文';
    const labelNode = control.querySelector?.('[data-language-label]');
    if (labelNode) labelNode.textContent = label;
    else control.textContent = label;
    control.setAttribute?.('aria-label', accessible);
    control.setAttribute?.('title', accessible);
  }

  function setLocale(value, options = {}) {
    const next = normalizeLocale(value);
    activeLocale = next;
    if (options.persist !== false) {
      try {
        global.localStorage?.setItem(storageKey, next);
      } catch {
        // Browsers may deny local storage; the in-memory locale still works.
      }
    }
    if (global.document?.documentElement) global.document.documentElement.lang = next;
    apply(global.document?.body || global.document);
	if (typeof global.CustomEvent === 'function') {
		global.document?.dispatchEvent?.(new global.CustomEvent('aegis:localechange', { detail: { locale: next } }));
	}
    return next;
  }

  const api = Object.freeze({
    locale: () => activeLocale,
    setLocale,
    t: translate,
    apply,
    supported: () => Array.from(supportedLocales)
  });
  global.AegisI18n = api;

  if (global.document?.documentElement) global.document.documentElement.lang = activeLocale;
  const start = () => {
    const control = global.document?.getElementById?.('language-toggle-btn');
    control?.addEventListener?.('click', () => setLocale(activeLocale === 'zh-CN' ? 'en' : 'zh-CN'));
    apply(global.document?.body || global.document);
    if (typeof global.MutationObserver === 'function' && global.document?.body) {
      const observer = new global.MutationObserver((mutations) => {
        for (const mutation of mutations) {
          if (mutation.type === 'characterData') translateTextNode(mutation.target);
          for (const node of mutation.addedNodes || []) apply(node);
          if (mutation.type === 'attributes') translateElementAttributes(mutation.target);
        }
      });
      observer.observe(global.document.body, {
        subtree: true,
        childList: true,
        characterData: true,
        attributes: true,
        attributeFilter: translatedAttributes
      });
    }
  };
  if (global.document?.readyState === 'loading') {
    global.document.addEventListener?.('DOMContentLoaded', start, { once: true });
  } else {
    start();
  }
})(window);
