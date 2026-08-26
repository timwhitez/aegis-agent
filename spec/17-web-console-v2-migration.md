# Web Console v2 Migration

## 1. 目标与范围

Phase 15 的默认 Web surface 迁移到第二套内嵌前端 `Web Console v2`。迁移保留
原 Web 页面作为可选 legacy fallback，但默认禁用。v2 继续直接使用现有 Go Web
service 的 REST / WebSocket notification / polling API；session store、queue store、
runtime facade 和 workspace 文件仍是唯一事实源。

这次迁移只替换 operator surface，不新增 hosted service、浏览器 IDE、远程终端、
前端 workflow engine、provider replay 层或第二套数据库。

## 2. 候选方案审计

| 候选 | 许可与形态 | 适配结论 |
| --- | --- | --- |
| [CopilotKit](https://github.com/CopilotKit/CopilotKit) | MIT；React 组件加 AG-UI/runtime | 组件成熟，但默认集成会引入第二套 agent runtime/protocol，不选作 v2 基座 |
| [AionUi](https://github.com/iOfficeAI/AionUi) | Apache-2.0；完整 Electron/Web Cowork app | 功能和依赖面过大，包含自身 agent、远程访问、调度和文件编辑，不符合轻量本地 console 边界 |
| [Agent UI](https://github.com/agno-agi/agent-ui) | MIT；Next.js/Tailwind 聊天前端 | 选用其 chat-first 布局、sidebar、composer、button/card 组件模式；不引入 AgentOS API |
| [Sim](https://github.com/simstudioai/sim) | Apache-2.0；完整 Next.js workflow 平台 | 工作流画布、数据库、认证和部署栈超出范围 |
| [Plandex](https://github.com/plandex-ai/plandex) | MIT；terminal-first coding agent | 没有可直接承载本项目 Web-first surface 的浏览器组件基座 |
| [PilotDeck](https://github.com/OpenBMB/PilotDeck) | AGPL-3.0；完整 Web/runtime 平台 | reciprocal license 与自身 gateway/runtime 耦合均不适合作为轻量二开基座 |

锁定选型：以 Agent UI 的 MIT 许可 UI 模式为设计来源，在 repo 内维护无 CDN、无
运行时 npm 依赖的内嵌 v2 资源。保留其许可证和来源说明。所有 Aegis 行为继续调用
现有 `/api/*` contract，不复制 AgentOS client、stream handler 或状态 store。

## 3. 双前端与路由契约

- `/`、`/index.html` 和未知 SPA route 默认返回 v2。
- v2 资源从 `/v2-assets/*` 提供；`/shared-assets/*` 固定映射到已受现有语法与
  module-contract 测试覆盖的 `internal/webconsole/assets/*` 无状态 view/controller 模块，
  不允许另建未受检的 shared 目录。
- `/legacy/` 与 `/legacy/index.html` 只在 `web.legacy_ui_enabled: true` 时提供旧页面。
- `web.legacy_ui_enabled` 默认 `false`；禁用时 legacy 页面与 legacy-only asset route
  返回 `404`，不得通过 SPA fallback 绕过。
- `/api/config` GET/PATCH 与 v2 Settings 使用同一个配置文件事实源控制该开关；保存
  仍经过现有 same-origin、审计日志、原子恢复和 owner-only 写入路径。
- 开关只决定完整 legacy 页面是否可访问。v2 与 legacy 共用的 API client、Markdown
  sanitizer、session renderer 和 workspace controller 属于共享前端核心，不是第二套 UI
  权威源。

## 4. v2 信息架构

v2 保持单窗口、chat-first、轻量导航：

- Session：start / steer / continue、Goal、Plan Mode、stop / interrupt、timeline 与 inspector
- Sessions：历史、session detail、children 和 task 状态；queue/background 只通过已接纳
  的 durable message 或有界关联摘要出现，不新增 Queue tab、Background tracker、Open job
  或 selected-job inspector
- Skills：只管理 Web service 已授权的本地 skill 目录
- Workspace：受限预览、上传、重命名、建目录和删除，不加入浏览器编辑器
- Settings：provider/model/reasoning、runtime/child budget、role provider 和 legacy UI 开关

桌面布局采用可收起窄 sidebar、居中 chat surface、sticky composer 和按需 inspector；
移动布局折叠为单列。默认页面不新增 workflow canvas、worker dashboard、文件编辑器或
细粒度 orchestration 面板。

v2 默认使用浅色主题，并通过浏览器 `color-scheme: light` 与内嵌浅色 palette 保持首次
打开、无本地偏好和无系统主题依赖时的一致外观。深色主题不作为迁移完成条件；后续若
加入主题切换，只能作为显示偏好，不能分叉 controller、API 或持久化业务状态。

## 5. 迁移与回滚

1. 先部署 v2 与共享 controller，legacy 页面继续保留在 binary 中。
2. 自动测试同时验证 v2 默认路由、legacy 默认 404、显式启用 legacy、配置 round-trip、
   两个资源目录的 JavaScript syntax、XSS sanitizer 和已有状态机回归。
3. `spec/17-web-console.md` 的完整 browser acceptance matrix 必须在 v2 默认 route 重跑，
   至少真实覆盖 start、running steer、continue、Goal、Plan Mode approve/revision/input、
   stop/interrupt、timeline/tool cards、Settings、Skills、Workspace 风险操作、Sessions history
   与 responsive layout。queue submit 只由 API/service/CLI smoke 验证；默认 Web 不提供表单。
4. 验收通过后保持 `legacy_ui_enabled: false`。短期回滚只需在配置中显式设为 `true` 并
   重启 Web service；不会改变 session/runtime 数据。

## 6. 完成标准

- v2 是默认且完整的 Phase 15 operator surface。
- v2 的用户动作只使用既有 API contract，旧前端不再是功能依赖。
- legacy 页面默认禁用，显式开关可恢复且有配置审计事实。
- embedded binary 不依赖外部 CDN、Next.js server、AgentOS 或 npm runtime。
- root Web smoke、Go tests、JavaScript tests、语法检查和真实浏览器 smoke 全部通过。
- 桌面与移动端真实浏览器截图确认默认浅色主题、可读对比度、响应式导航和 inspector
  modal 行为；不得依赖操作系统的深色偏好改变默认主题。
- 默认页面不存在独立 Queue/Background surface；API 提交的 job 只通过 service/API/文件事实
  验证，防止 large-project profile 反向主导默认 Web。
