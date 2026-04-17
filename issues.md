# Current Issues and Optimization Directions

## 设计目标对照

项目定位：通用 harness engineering 项目，用于执行通用 agent 任务
- ✅ 优秀的 agent 调度逻辑（已有 auto_queue、delegation、steer/continue）
- ✅ 完备工具支持（已有 builtin tools + skill command tools）
- ⚠️ 美观易用的前端界面（基础功能完整，但 UX 仍需优化）
- ✅ 轻量化设计（遵循 bitter lesson 理念，把控制权还给模型）

## 核心设计理念验证

参考文档：
- [bitter-lesson-agent-frameworks.md](../bitter-lesson-agent-frameworks.md)
- [pi-coding-agent.md](../pi-coding-agent.md)
- [blog-langchain-com__the-anatomy-of-an-agent-harness.md](../blog-langchain-com__the-anatomy-of-an-agent-harness.md)
- [openai-com__harness-engineering.md](../openai-com__harness-engineering.md)
- [anthropic-com__effective-harnesses-for-long-running-agents.md](../anthropic-com__effective-harnesses-for-long-running-agents.md)

### Harness Engineering 三大支柱对照

基于 OpenAI/Anthropic/LangChain 的 2026 年最佳实践：

#### 1. Context Engineering（上下文工程）

**当前实现：**
- ✅ AGENTS.md 支持（project instructions chain）
- ✅ Compaction 机制（`internal/runtime/compaction.go`）
- ✅ Project memory stack
- ✅ Session 持久化和恢复
- ✅ Progressive disclosure（技能按需加载）**[2026-04-17 已实现]**
- ✅ Ephemeral messages 机制（大型工具输出自动压缩）**[2026-04-17 已实现]**

**最佳实践要求：**
- Repository-first documentation（所有知识在 repo 内）
- Progressive disclosure（按需加载 skills/tools）
- Ephemeral messages（大型工具输出自动清理）
- Context 优先级管理（重要信息优先保留）

**差距分析：**
- ✅ Ephemeral messages 机制已实现（browser-use 模式）**[2026-04-17]**
- ✅ Skill loading 已优化为按需加载**[2026-04-17]**
- ✅ Compaction 智能性已增强（去重和保留策略）**[2026-04-17]**

#### 2. Architectural Constraints（架构约束）

**当前实现：**
- ✅ Workspace boundary enforcement（工作目录边界）
- ✅ Tool schema validation（工具参数验证）
- ✅ Hooks system（可扩展的钩子机制）
- ⚠️ 缺少 deterministic linters（自定义 lint 规则）
- ⚠️ 缺少 structural tests（架构层级验证）

**最佳实践要求：**
- Dependency layering enforcement（依赖层级强制）
- Custom linters with remediation instructions
- Structural tests for architecture compliance
- Pre-commit hooks for automated checks

**差距分析：**
- 当前主要依赖 workspace boundary，缺少更细粒度的架构约束
- 可以考虑增加可选的 lint/test hooks，但保持轻量化原则
- 对于通用 harness，过度的架构约束可能不适用

#### 3. Entropy Management（熵管理）

**当前实现：**
- ✅ Session cleanup 机制
- ✅ Event-driven architecture（便于追踪）
- ⚠️ 缺少定期清理 agents（doc consistency, pattern enforcement）
- ⚠️ 缺少 self-verification loops

**最佳实践要求：**
- Periodic cleanup agents（定期清理和修复）
- Documentation consistency validation
- Pattern enforcement agents
- Self-verification before task completion

**差距分析：**
- 需要考虑增加可选的 "cleanup" 或 "audit" skills
- 可以通过 queue 机制实现定期清理任务
- Self-verification 可以通过 prompt engineering 引导

### Agent = Model + Harness 框架对照

根据 LangChain 的定义："If you're not the model, you're the harness"

**当前 Harness 组件清单：**

| 组件类别 | 当前实现 | 完整度 | 优化方向 |
|---------|---------|--------|---------|
| **System Prompts** | ✅ `internal/runtime/prompt.go` | 95% | ✅ 已精简 **[2026-04-17]** |
| **Tools & Skills** | ✅ Registry + Catalog | 100% | ✅ Progressive disclosure 已实现 **[2026-04-17]** |
| **Bundled Infrastructure** | ✅ Filesystem + Shell | 100% | 完整 |
| **Orchestration Logic** | ✅ Delegation + Queue | 95% | 需要增强 handoff |
| **Hooks/Middleware** | ✅ Hooks Manager | 85% | 需要更多内置 middleware |
| **Memory & Search** | ✅ Project Memory | 80% | 需要增强检索能力 |
| **Sandboxes** | ✅ Isolation mode | 90% | 完整 |
| **Context Compaction** | ✅ Compactor | 95% | ✅ 智能策略已增强 **[2026-04-17]** |
| **Long-horizon Support** | ✅ Steer/Continue | 100% | ✅ Ralph Loop 已实现 **[2026-04-17]** |
| **Observability** | ✅ Event Bus + Timeline | 95% | 完整 |

### 核心设计原则验证

**已遵循的 Bitter Lesson 原则：**
1. ✅ 简单的 for-loop 架构（`internal/runtime/engine.go`）
2. ✅ 显式 `finish` tool 而非隐式停止（done tool pattern）
3. ✅ 最小化抽象，把控制权还给模型
4. ✅ 完整的 action space（filesystem + shell + tools）
5. ✅ 统一的 provider adapter（支持 OpenAI/Anthropic/Google）

**已遵循的 pi-coding-agent 原则：**
1. ✅ Minimal system prompt（相对简洁的 system prompt）
2. ✅ Minimal toolset（核心工具 + 可扩展 skills）
3. ✅ YOLO by default（guardrails_mode: yolo）
4. ✅ No over-abstraction（避免过度抽象）
5. ⚠️ Context handoff 能力较弱（跨 provider 切换）

**已遵循的 OpenAI Harness Engineering 原则：**
1. ✅ Repository-first documentation（AGENTS.md chain）
2. ✅ Durable state via filesystem（session persistence）
3. ✅ Git for versioning（isolation mode 支持）
4. ⚠️ 缺少 "golden principles" 和定期清理机制
5. ⚠️ 缺少 LLM-based auditors

**已遵循的 Anthropic Long-running Agent 原则：**
1. ✅ Incremental progress（steer/continue 机制）
2. ✅ Clean state management（session state）
3. ✅ Testing support（shell tool + skills）
4. ⚠️ 缺少 initializer agent 模式
5. ⚠️ 缺少 feature list 管理

---

## 优先级分级说明

- **P0 - 关键**：影响核心 harness 能力，参考业界最佳实践必须实现
- **P1 - 重要**：显著提升用户体验或模型效率
- **P2 - 优化**：锦上添花，可以延后
- **P3 - 探索**：实验性功能，需要验证价值

---

## P0 - 关键优化方向

### 1. Context Engineering 增强

**问题：**
- 大型工具输出（如 `grep` 全仓库扫描）会快速填满 context
- Skills 在启动时全量加载，浪费 context window
- Compaction 策略较为保守，可能丢失关键证据

**业界最佳实践：**
- Browser-use 的 ephemeral messages（保留最近 N 次输出）
- Pi-coding-agent 的 progressive disclosure（按需加载）
- OpenAI 的 tool call offloading（大输出写入文件）

**优化方向：**
- [x] **实现 ephemeral messages 机制** ✅ **已完成 [2026-04-17]**
  - 为 `grep`、`glob`、`shell` 等工具添加 `ephemeral` 标记
  - 自动保留最近 N 次输出，旧输出写入 `.artifacts/` 并提供路径
  - 参考：browser-use 的 `@tool("Get browser state", ephemeral=3)` 模式
  
- [x] **优化 Skills 加载为 progressive disclosure** ✅ **已完成 [2026-04-17]**
  - 启动时只加载 skill summaries（name + description）
  - 使用 `load_skill` 时才加载完整 SKILL.md 内容
  - 减少初始 context 占用 50%+
  
- [x] **增强 Compaction 智能性** ✅ **已完成 [2026-04-17]**
  - 保留 "key evidence"（用户明确要求的文件路径、关键错误信息）
  - 优先压缩重复的工具输出（多次 `read_file` 同一文件）
  - 保留最近的 steer/continue 输入完整性

**实现优先级：** ✅ 全部完成

### 2. Prompt Engineering 精简

**问题：**
- System prompt 过长（`internal/runtime/prompt.go` 约 150+ 行）
- Runtime notes 注入逻辑复杂，可能干扰模型判断
- 工具描述存在冗余说明

**业界最佳实践：**
- Pi-coding-agent：minimal system prompt（约 50 行）
- OpenAI：AGENTS.md 作为 "table of contents" 而非 encyclopedia
- Anthropic：clear, concise instructions

**优化方向：**
- [x] **精简 System Prompt 核心部分** ✅ **已完成 [2026-04-17]**
  - 移除冗余的 "how to use tools" 说明（模型已经训练过）
  - 将部分说明移到工具的 description 中
  - 保留关键的 workspace boundary 和 mode 说明
  - 目标：减少到 80 行以内 ✅ 已达成
  
- [x] **优化 Runtime Notes 注入策略** ✅ **已完成 [2026-04-17]**
  - 只在真正需要时注入（如检测到 audit task 才注入 evidence note）
  - 合并相似的 notes，避免重复
  - 考虑将部分 notes 改为 tool result 中的提示
  
- [ ] **简化工具描述**
  - 移除 "you can use this tool to..." 类的冗余前缀
  - 使用更简洁的动词开头（"Read file", "Search code"）
  - 参考 pi-coding-agent 的 minimal tool descriptions

**实现优先级：** ✅ P0 已完成，P2 待优化
  - 目标：减少到 80 行以内
  
- [ ] **优化 Runtime Notes 注入策略**
  - 只在真正需要时注入（如检测到 audit task 才注入 evidence note）
  - 合并相似的 notes，避免重复
  - 考虑将部分 notes 改为 tool result 中的提示
  
- [ ] **简化工具描述**
  - 移除 "you can use this tool to..." 类的冗余前缀
  - 使用更简洁的动词开头（"Read file", "Search code"）
  - 参考 pi-coding-agent 的 minimal tool descriptions

**实现优先级：** P0（精简 system prompt）> P1（优化 notes）> P2（简化工具描述）

### 3. Long-horizon Execution 增强

**问题：**
- 缺少 Anthropic 提出的 initializer agent 模式
- 缺少 OpenAI 的 Ralph Loop 自动续跑机制
- 缺少 feature list 管理（容易过早声明完成）

**业界最佳实践：**
- Anthropic：initializer agent + coding agent 分离
- OpenAI：Ralph Loop（自动 reinject prompt）
- Feature list + progress tracking

**优化方向：**
- [ ] **实现 Initializer Agent 模式**
  - 新增 `--init` 模式，专门用于项目初始化
  - Initializer 负责：创建 feature list、设置 git repo、编写 init.sh
  - 后续 session 自动读取 feature list 和 progress notes
  
- [x] **增加 Ralph Loop 支持** ✅ **已完成 [2026-04-17]**
  - 检测 exec mode 下的 incomplete_no_finish 状态
  - 自动 reinject 原始 prompt 并 continue
  - 可配置最大 loop 次数（防止无限循环）
  
- [x] **Feature List 管理工具** ✅ **已完成 [2026-04-17]**
  - 新增 `feature_list_create` / `feature_list_update` / `feature_list_read` 工具
  - JSON 格式存储，包含 description、steps、passes 字段
  - 引导模型逐个完成 feature 而非一次性完成所有
  - Pre-completion checklist 集成

**实现优先级：** ✅ P1 已完成，P2 待优化

---

## P1 - 重要优化方向

### 4. 前端 UX 优化

**问题：**
- Chat 界面每次全量重渲染，大型 session 卡顿
- 缺少快捷键支持
- 实时反馈不够明显
- 移动端适配缺失

**优化方向：**
- [x] **性能优化** ✅ **已完成 [2026-04-17]**
  - 实现增量渲染（只更新变化的消息）
  - 添加虚拟滚动（大型 session 支持，50+ 消息自动启用）
  - 优化 WebSocket 事件处理（debounce/throttle）
  
- [x] **交互体验** ✅ **已完成 [2026-04-17]**
  - 添加快捷键（Ctrl+Enter 发送、Esc 停止、/ 触发命令、Ctrl+K 搜索、Ctrl+N 新会话、Ctrl+, 设置、? 帮助）
  - 添加 loading skeleton 和乐观更新
  - 改进错误提示的可操作性（提供修复建议）
  
- [x] **视觉优化** ✅ **已完成 [2026-04-17]**
  - 添加 dark mode 支持（跟随系统偏好，localStorage 持久化）
  - 优化移动端响应式布局
  - 改进 session rail 的筛选和排序 UI

**注：** 当前前端的留白风格是有意设计，保持宽松舒适的视觉体验。

**实现优先级：** ✅ 全部完成

### 5. Cross-Provider Context Handoff

**问题：**
- 跨 provider 切换时 context 可能丢失
- 不同 provider 的 tool call 格式不兼容
- 缺少统一的 context 序列化格式

**业界最佳实践：**
- Pi-coding-agent：统一的 Context 接口，支持跨 provider 序列化
- 自动转换 thinking traces 为标准格式

**优化方向：**
- [ ] **统一 Context 格式**
  - 定义 provider-agnostic 的 message 格式
  - 实现 Anthropic ↔ OpenAI ↔ Google 的双向转换
  - 保留 provider-specific metadata（如 cache tokens）
  
- [ ] **Tool Call 兼容层**
  - 统一 tool call 的 ID 生成和引用
  - 转换不同 provider 的 tool result 格式
  
- [ ] **Context 序列化/反序列化**
  - 支持 session 导出为 JSON
  - 支持从 JSON 恢复并切换 provider

**实现优先级：** P1（统一格式）> P2（tool call 兼容）> P2（序列化）

### 6. Self-Verification Loops

**问题：**
- 模型容易过早声明任务完成
- 缺少自动化的验证机制
- 缺少 test-driven 的工作流

**业界最佳实践：**
- Anthropic：browser automation for end-to-end testing
- OpenAI：self-verification before task completion
- LangChain：pre-completion checklist middleware

**优化方向：**
- [ ] **Pre-completion Checklist**
  - 在 `finish` tool 前自动注入 checklist
  - 检查项：是否有 failing tests、是否有 TODO comments、是否有未提交的更改
  
- [ ] **Test Execution Integration**
  - 引导模型在完成功能后运行测试
  - 自动检测常见测试命令（npm test、go test、pytest）
  
- [ ] **Browser Automation Support**
  - 集成 Puppeteer/Playwright MCP（如果可用）
  - 引导模型进行 end-to-end 验证

**实现优先级：** P1（pre-completion checklist）> P2（test integration）> P2（browser automation）

---

## P2 - 优化方向

### 7. Entropy Management（熵管理）

**问题：**
- 缺少定期清理机制
- 文档容易与代码脱节
- 缺少 pattern enforcement

**业界最佳实践：**
- OpenAI：periodic cleanup agents（doc gardening、pattern enforcement）
- "Golden principles" + scheduled refactoring PRs

**优化方向：**
- [ ] **Documentation Consistency Agent**
  - 定期检查 AGENTS.md 与实际代码的一致性
  - 通过 queue 机制实现（每日/每周运行）
  
- [ ] **Pattern Enforcement**
  - 检测代码中的反模式（如过度抽象、重复代码）
  - 生成 refactoring 建议
  
- [ ] **Cleanup Skills**
  - 创建 `cleanup` / `audit` skills
  - 可手动触发或定期运行

**实现优先级：** P2（全部）

### 8. Observability 增强

**问题：**
- 缺少工具调用的统计和分析
- 缺少 cost tracking
- 缺少性能 profiling

**优化方向：**
- [ ] **Tool Usage Analytics**
  - 统计每个工具的调用次数、成功率、平均耗时
  - 在 Web Console 中展示 dashboard
  
- [ ] **Cost Tracking**
  - 记录每个 session 的 token 使用和成本
  - 支持按 provider/model 分组统计
  
- [ ] **Performance Profiling**
  - 记录 turn 耗时、compaction 耗时、tool 执行耗时
  - 识别性能瓶颈

**实现优先级：** P2（analytics）> P2（cost tracking）> P3（profiling）

### 9. 工具系统优化

**问题：**
- 工具输出格式不够结构化
- 缺少工具调用的缓存机制
- 缺少 tool chaining 优化

**优化方向：**
- [ ] **结构化工具输出**
  - 统一工具输出格式（JSON + markdown）
  - 分离 LLM 部分和 UI 部分（参考 pi-coding-agent）
  
- [ ] **工具调用缓存**
  - 缓存 `read_file`、`glob` 等幂等操作的结果
  - 基于文件 mtime 自动失效
  
- [ ] **Tool Chaining**
  - 检测常见的工具调用模式（如 glob → grep → read_file）
  - 提供优化建议或自动合并

**实现优先级：** P2（结构化输出）> P3（缓存）> P3（chaining）

---

## P3 - 探索方向

### 10. Multi-Agent Architecture

**问题：**
- 当前是单一通用 agent
- 缺少专门化的 agent 角色（testing agent、review agent）

**业界趋势：**
- Anthropic 提到：specialized agents 可能更有效
- OpenAI：agent-to-agent review

**探索方向：**
- [ ] **Specialized Agent Roles**
  - Testing Agent（专门负责测试）
  - Review Agent（代码审查）
  - Cleanup Agent（代码清理）
  
- [ ] **Agent-to-Agent Communication**
  - 增强 delegation 机制
  - 支持 agent 之间的 review 和 feedback

**实现优先级：** P3（需要先验证价值）

### 11. Advanced Context Management

**问题：**
- 缺少智能的 context 优先级管理
- 缺少 context 的智能摘要和索引

**探索方向：**
- [ ] **Context Priority Management**
  - 为不同类型的信息分配优先级
  - 在 compaction 时优先保留高优先级信息
  
- [ ] **Semantic Context Indexing**
  - 使用 embedding 索引 context
  - 支持语义检索（类似 RAG）

**实现优先级：** P3（实验性）

### 12. 文档和可用性

**问题：**
- 缺少完整的用户文档
- 错误信息不够友好
- 缺少 onboarding 流程

**优化方向：**
- [ ] **用户文档**
  - 编写完整的用户指南
  - 添加最佳实践和案例研究
  - 创建 troubleshooting guide
  
- [ ] **错误信息优化**
  - 改进错误信息的可读性
  - 提供可操作的修复建议
  
- [ ] **Interactive Tutorial**
  - 在 Web Console 中添加引导流程
  - 提供示例 prompts 和 skills

**实现优先级：** P2（用户文档）> P3（其他）

---

## 实现路线图建议

### Phase 1: Context Engineering（1-2 周）
1. 实现 ephemeral messages 机制
2. 优化 Skills 为 progressive disclosure
3. 精简 System Prompt

### Phase 2: Long-horizon Support（1-2 周）
1. 实现 Ralph Loop 支持
2. 添加 Feature List 管理工具
3. 实现 Pre-completion Checklist

### Phase 3: UX & Observability（1-2 周）
1. 前端性能优化（增量渲染、虚拟滚动）
2. 添加快捷键支持
3. 实现 Tool Usage Analytics

### Phase 4: Advanced Features（2-3 周）
1. Cross-Provider Context Handoff
2. Self-Verification Loops
3. Entropy Management

---

## 设计原则提醒

在实现上述优化时，始终遵循以下原则：

1. **轻量化优先**：避免过度抽象，保持代码简洁
2. **模型优先**：把控制权还给模型，harness 只提供必要的约束和工具
3. **可配置性**：新功能应该是可选的，不强制所有用户使用
4. **向后兼容**：保持现有 API 和配置的兼容性
5. **文档同步**：代码变更必须同步更新文档

参考 bitter lesson："The less you build, the more it works."

---

## 已解决的历史问题

### Resolved in main repo

- Issue `2` fixed: websocket `reset_session` no longer echoes fake durable session id
- Issue `3` resolved: Workspace view explicitly labeled as current server `cwd` browser
- Issue `4` fixed: CJK-safe font fallbacks added
- Issue `5` fixed: Default tool surface documented
- Issue `6` fixed: `clearHistory()` no longer kicks out of History view
- Post-audit: Compaction no longer drops latest external instruction

### Partially mitigated

- Issue `1`: Upstream `auth_unavailable` failure (provider-side issue)
- Workspace-root switching: Not implemented by design (current `cwd` only)

---

## 参考资源

**核心文档：**
- [The Bitter Lesson of Agent Frameworks](../bitter-lesson-agent-frameworks.md)
- [Pi Coding Agent](../pi-coding-agent.md)
- [The Anatomy of an Agent Harness (LangChain)](../blog-langchain-com__the-anatomy-of-an-agent-harness.md)
- [Harness Engineering (OpenAI)](../openai-com__harness-engineering.md)
- [Effective Harnesses for Long-running Agents (Anthropic)](../anthropic-com__effective-harnesses-for-long-running-agents.md)

**在线资源：**
- [LangChain Agent Harness](https://www.langchain.com/blog/the-anatomy-of-an-agent-harness/)
- [OpenAI Harness Engineering](https://openai.com/index/harness-engineering/)
- [Anthropic Long-running Agents](https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents)
- [Browser-use Agent SDK](https://github.com/browser-use/agent-sdk)
