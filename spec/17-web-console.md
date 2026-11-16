# Web 控制台服务

本文描述 `internal/webconsole` 提供的本地 HTTP 服务：路由、运行控制、实时状态、安全边界和验收要求。前端模块、交互和验证见 [Web 控制台前端实现](./17-web-console-frontend.md)，配置字段见 [CLI 与配置](./02-cli-and-config.md)。

## 范围

- `aegis-agent web` 启动的本地 HTTP 服务，默认监听 `127.0.0.1:3940`，并嵌入无构建前端；
- REST API 读取 session 文件事实并发起控制，runtime 负责实际执行，服务不维护第二套执行循环；
- 不提供 hosted 多用户、远程终端或浏览器 IDE；HTTPS 由外层负责。

## 路由

除 `/ui/*`、退役路径和下表中的 API 外，其余 GET/HEAD 路径（`/`、`/s/{id}`、`/sessions`、`/skills`、`/workspace`、`/settings` 等）都返回嵌入的 `index.html`，由前端 router 渲染；其他方法返回 405。

| 路由 | 方法 | 用途 |
| --- | --- | --- |
| `/ui/*` | GET、HEAD | 嵌入的前端资源；缺失文件和目录返回 404，不回退到页面 shell |
| `/legacy`、`/v2-assets`、`/shared-assets` 及其子路径 | 全部 | 退役前端路由，返回 404 |
| `/api/meta` | GET | 服务元信息：session/workspace 根、provider、worker 数和能力 |
| `/api/overview` | GET | 侧栏数据：计数、最近会话、job、worker、失败和 feed |
| `/api/history` | GET | 分页会话历史（`page`、`page_size`） |
| `/api/sessions` | GET | 会话摘要列表（`limit`，最多 200） |
| `/api/sessions/start` | POST | 启动新会话，返回 202 accepted |
| `/api/sessions/clear` | POST | 清空历史；本进程有活动 handle 或正在启动、继续的 session，存在未结算的队列 job，或仍有 session 在运行时返回冲突 |
| `/api/sessions/{id}` | GET、DELETE | 会话详情（消息尾部 `limit` 默认 40、最多 200）；删除会话树；目标或其后代在本进程有活动 handle 或正在继续、审批，仍在运行，或关联了会执行的队列 job（树内会话已暂停或等待输入的 blocked job 除外）时返回冲突 |
| `/api/sessions/{id}/messages` | GET | 消息分页（`before_id`、`limit`） |
| `/api/sessions/{id}/children`、`tasks`、`file-changes`、`context` | GET | 子会话与队列 job、任务板、文件变更、上下文报告 |
| `/api/sessions/{id}/continue`、`steer`、`interrupt`、`stop` | POST | 运行控制 |
| `/api/sessions/{id}/goal` | GET、POST、PATCH、DELETE | 读取、创建、修改、清除 Goal |
| `/api/sessions/{id}/goal/complete`、`pause`、`resume` | POST | 修改 Goal 状态 |
| `/api/sessions/{id}/mission/plan`、`mission/validation` | PATCH | 修改 mission plan 与 validation |
| `/api/sessions/{id}/mission/plan/approve` | POST | 批准 linked mission plan |
| `/api/sessions/{id}/planmode` | GET | Plan Mode 状态与审批目标 |
| `/api/sessions/{id}/planmode/approve`、`revise`、`cancel`、`input` | POST | 审批、修订、取消计划，回答计划提问 |
| `/api/sessions/{id}/approval-receipts/{request_id}` | GET | 审批 receipt 查询，先于当前 session metadata 检查 |
| `/api/queue/jobs`、`/api/queue/jobs/{id}` | GET、POST；GET | 后台队列 job 列表与创建；job 详情 |
| `/api/workers` | GET、POST | worker 快照；调整 worker 数量 |
| `/api/config` | GET、POST | 读取与保存 provider 配置和 `.env` API key |
| `/api/config/test` | POST | 用表单值测试 provider 连接，不保存 |
| `/api/files` | GET、DELETE | Workspace 目录列表（一次返回整个目录）；删除单个路径 |
| `/api/files/mkdir`、`upload`、`delete` | POST | 新建目录、上传文件、批量删除 |
| `/api/files/rename` | PATCH | 重命名 |
| `/api/file/read`、`/api/file/download` | GET | 按字节 offset 分页预览；下载 |
| `/api/skills` | GET | 技能列表 |
| `/api/skills/upload` | POST | 上传 zip 技能包并安装 |
| `/api/skills/{id}/uninstall` | POST | 卸载 managed skill |
| `/api/skills/{id}/install` | POST | 返回 501：不支持市场安装，需上传 zip |
| `/ws`（别名 `/api/ws`） | GET 升级 | WebSocket 连接状态与旧控制消息弃用提示，见下文 |

未匹配的 `/api/*` 返回 404，不回退到页面 shell。

## 运行控制

### 启动

`POST /api/sessions/start` 创建或接入当前进程的 launch handle，runtime 负责真实执行。服务关闭时释放 handle、停止 worker/reaper 并取消仍在等待的 launch。HTTP 返回 accepted 不等于 provider 已完成。

### 继续、steer、中断和 stop

- continue 写入普通 follow-up 或显式恢复请求；
- continue、Plan Mode 输入和审批从校验会话起就在本进程内预留该会话，直到登记 handle；start 在拿到 session ID 前登记为 pending start。删除或清空遇到预留或活动 handle 时返回冲突，清空遇到 pending start 也返回冲突；删除或清空正在移除该会话（清空时为全部会话）时，这些请求立即返回 409，不排队等待；会话已被移除后再请求返回 404；
- steer 写入 durable control，可按请求选择 interrupt；
- interrupt 请求停止当前 owner，但不删除已写入的消息和事件；
- stop 结束当前执行并协调 descendants/queue；
- active handle 不属于当前进程时返回冲突和下一步动作，不能静默接管。

### Goal 与 Plan Mode

Goal、mission plan、validation、Plan Mode 输入和审批都必须经过 session store 的版本/CAS 边界。审批 payload 带 review target、request ID 和 revision；receipt 查询先于当前 session metadata 检查，支持响应丢失后的重放判断。未知、已接纳或内容已变化的操作要求显式恢复，不自动再次执行。

### 实时状态与刷新

WebSocket `/ws` 目前只用于连接状态：服务端不推送 session 事件，也不执行任何控制。客户端发来的消息只会得到错误帧：旧的 `chat`、`stop` 和其他未知类型返回 `WEBSOCKET_CONTROL_DEPRECATED`，无法解析的 JSON 返回 `invalid websocket payload`；`reset_session` 不回包。页面通过 REST 读取事实，并在页面可见时按状态轮询（运行中快速、空闲时低频，见前端规范的启动与数据流），因此其他标签页、CLI 或其他进程造成的变化无需刷新也会出现；session control 使用 REST。

## 安全边界

请求处理顺序是 Host → Basic Auth → API/UI routing。Host 默认允许 localhost、`*.localhost`、IP literal 和 `web.allowed_hosts` 中的值。Basic Auth 可选，若配置则 username 与 bcrypt `password_hash` 必须同时存在；服务自身提供 HTTP，HTTPS 由外层负责，外层代理的浏览器 origin 写入 `web.public_origins`。

API mutation 必须满足：

- 有同源 `Origin`（Host 与 scheme 都与请求一致，或等于 `web.public_origins` 中声明的外部 origin），或在无 Origin 时带 `X-Aegis-Agent-Web: 1`；跨源 mutation 拒绝，不信任 `X-Forwarded-*`。`/ws` 握手带 Origin 时使用同一同源规则；不带 Origin 时放行，也不要求 `X-Aegis-Agent-Web`，因为浏览器总会发送 Origin，而 `/ws` 不推送 session 数据也不执行控制；
- 要求 JSON 的路由必须是 `application/json`，body 有统一大小上限；
- unsafe 方法通过 JSON body policy 分为 required、optional 和 none；
- 资源路径拒绝 segment separator、traversal 和不安全 symlink；
- 返回 CSP、nosniff、no-referrer 和 same-origin CORP；
- Web 审计记录配置、Workspace、Skills、session 和控制 mutation。

直接执行 `web --listen` 绑定给定地址，非回环监听会打印 warning；使用 `run.sh` 启动时还需要父进程显式设置网络允许环境。文档必须区分这两个边界。

## 前端与后端的权威关系

前端 store 保存路由、加载状态、draft、relay 状态和当前视图；session/status/messages/Goal/Plan/Files/Skills/Settings 的最终值来自 API。页面刷新、切换 session、断线和 service 重启都必须能重新读取事实。

服务端错误使用稳定 code、message、action 和恢复提示；前端显示原始错误中的路径、ID 和模型输出时保持 `translate=no`。不把错误 toast 或 optimistic state 当作成功事实。

## 修改 Web 的验收

- API 改动：Go service test、JSON/Origin/Host/route test 和相应 E2E；
- 运行控制改动：runtime/session tests、必要时 race、审批/receipt/recovery E2E；
- 页面改动：Node 单测、双语 key parity、桌面/移动和对应 E2E；
- 静态资源改动：重建 binary，检查 `/ui/*` MIME、CSP、未知资源 404 和 shell 路由。
