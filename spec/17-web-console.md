`POST /api/sessions/start` 创建或接入当前进程的 launch handle，runtime 负责真实执行。服务关闭时释放 handle、停止 worker/reaper 并取消仍在等待的 launch。HTTP 返回 accepted 不等于 provider 已完成。

### 继续、steer、中断和 stop

- continue 写入普通 follow-up 或显式恢复请求；
- steer 写入 durable control，可按请求选择 interrupt；
- interrupt 请求停止当前 owner，但不删除已写入的消息和事件；
- stop 结束当前执行并协调 descendants/queue；
- active handle 不属于当前进程时返回冲突和下一步动作，不能静默接管。

### Goal 与 Plan Mode

Goal、mission plan、validation、Plan Mode 输入和审批都必须经过 session store 的版本/CAS 边界。审批 payload 带 review target、request ID 和 revision；receipt 查询先于当前 session metadata 检查，支持响应丢失后的重放判断。未知、已接纳或内容已变化的操作要求显式恢复，不自动再次执行。

### 实时流

WebSocket 传递事件和实时状态，页面可以在断线后通过 REST refresh 重建。当前 session control 使用 REST；WS 收到旧的 `chat` 或 `stop` 控制消息时返回弃用提示，不执行隐藏控制。

## 安全边界

请求处理顺序是 Host → Basic Auth → API/UI routing。Host 默认允许 localhost、`*.localhost`、IP literal 和 `web.allowed_hosts` 中的值。Basic Auth 可选，若配置则 username 与 bcrypt `password_hash` 必须同时存在；服务自身提供 HTTP，HTTPS 由外层负责。

API mutation 必须满足：

- 有同源 `Origin`，或在无 Origin 时带 `X-Aegis-Agent-Web: 1`；跨源 mutation 拒绝；
- 要求 JSON 的路由必须是 `application/json`，body 有统一大小上限；
- unsafe 方法通过 JSON body policy 分为 required、optional 和 none；
- 资源路径拒绝 segment separator、traversal 和不安全 symlink；
- 返回 CSP、nosniff、no-referrer 和 same-origin CORP；
- Web 审计记录配置、Workspace、Skills、session 和控制 mutation。

直接执行 `web --listen` 绑定给定地址，非回环监听会打印 warning；使用 `run.sh` 启动时还需要父进程显式设置网络允许环境。文档必须区分这两个边界。

## 前端与后端的权威关系

前端 store 保存路由、加载状态、draft、relay 状态和当前视图；session/status/messages/Goal/Plan/Files/Skills/Settings 的最终值来自 API 或实时事件。页面刷新、切换 session、断线和 service 重启都必须能重新读取事实。

服务端错误使用稳定 code、message、action 和恢复提示；前端显示原始错误中的路径、ID 和模型输出时保持 `translate=no`。不把错误 toast 或 optimistic state 当作成功事实。

## 修改 Web 的验收

- API 改动：Go service test、JSON/Origin/Host/route test 和相应 E2E；
- 运行控制改动：runtime/session tests、必要时 race、审批/receipt/recovery E2E；
- 页面改动：Node 单测、双语 key parity、桌面/移动和对应 E2E；
- 静态资源改动：重建 binary，检查 `/ui/*` MIME、CSP、未知资源 404 和 shell 路由。
