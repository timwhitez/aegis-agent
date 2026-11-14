# 测试策略

## 测试目标

测试要回答三个问题：

1. runtime 是否按 durable session 事实运行和恢复；
2. Web、CLI、provider 和 tool adapter 是否遵守共享契约；
3. 用户从本地控制台看到的状态是否能在真实服务、桌面和移动视口中重建。

测试不把模型质量伪装成 deterministic pass，也不通过真实 API key 才能跑的实验代替仓库 gate。

## 测试层次

| 层次 | 位置/命令 | 主要问题 |
| --- | --- | --- |
| 格式与语法 | `./test.sh` | Go format、UI JS syntax、Node/Go 默认测试面 |
| Go 单元测试 | `go test ./internal/<package> -count=1` | store、runtime、provider、tool、配置和安全边界 |
| Node 单元测试 | `npm test` | api、store、model、i18n、Markdown、恢复逻辑和纯前端 helpers |
| Web 服务集成 | `internal/webconsole/*_test.go` | API、WebSocket、审批、Host/Origin、JSON policy、Workspace 和 Settings |
| race | `go test -race -count=1 ...` | session locks、events、queue、handles 和并发控制 |
| 静态分析 | `go vet`、`staticcheck`、`govulncheck` | 编译错误、可疑 Go 模式和已知漏洞 |
| 浏览器验收 | `npm run test:e2e` | 真实本地服务、嵌入资源、页面交互、响应式布局和错误回显 |

## Go 与 Node 测试

默认仓库 gate 使用：

~~~sh
go test -count=1 -timeout=10m ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...
~~~

`validation/` 中的运行产物和历史副本不自动扩大 Go module 测试面。改动配置、session、runtime、provider、tool、queue 或 Web service 时，先运行受影响 package，再视风险运行 race 全量。

`ui/src/lib/` 的无 DOM 逻辑优先用 `node:test`；store、action、API wrapper、i18n parity、Markdown sanitizer、send recovery、approval operation、Workspace、Skills、Settings 和 Inspector 通过脚本测试。

前端测试必须关注：

- durable 原文不会因为 locale 切换而被改写；
- API 错误、断线、refresh、session switch 和失败发送能恢复；
- approval request ID、revision、receipt 和 CAS 失败不会误执行；
- 字节分页和大文件 preview 不把字符数当成字节数；
- vendor 文件与声明的版本、notice 和 hash 一致。

## Web E2E 区域

`validation/scripts/webconsole_e2e.mjs` 编排以下区域：

- `server`：binary、监听、嵌入 shell、资源 MIME、CSP、Host 和退役路径；
- `shell`：布局、sidebar、导航、快捷键、palette、locale 和连接状态；
- `session`：start、message、tool steps、continue、steer、interrupt、stop、refresh；
- `plan`：Plan Mode 输入、修订、取消和 linked Goal；
- `approvals`：CAS、覆盖、coverage、跨 surface 控制；
- `receipts`：准备、接纳、查询、重放、未知回执和恢复；
- `recovery`：发送失败草稿、断线、页面切换和错误提示；
- `showcase`：包含真实内容的 Context、Files、Timeline 和移动布局；
- `pages`：Sessions、Skills、Workspace、Settings 的 CRUD 和返回；
- `audits`：console/request 错误、原文翻译、安全路径和显示证据。

迭代时可用 `--only=server,shell`、`--output=validation/runs/local` 和 `--keep-runtime`。浏览器使用确定性 scripted provider，不需要真实凭据。

## 行为改动的覆盖要求

| 改动 | 最少检查 |
| --- | --- |
| pure Go helper/config | 对应 Go package test；边界字段加失败用例 |
| session/runtime/provider | 对应 package test；跨状态或并发时加 race |
| Web service/API/security | Web service test；涉及页面时加对应 E2E area |
| 前端 state/model | Node 单测；页面交互或布局改动加 E2E |
| 前端文案/locale | 两种 locale parity test；durable 原文场景加 E2E |
| 仅文档/config sample | 链接、字段、命令与源码检查；不强制完整行为测试 |

## CI gate

GitHub Actions 在 Ubuntu 24.04、Go 1.26.11、Node 22 上执行：

~~~sh
npm ci --ignore-scripts
./test.sh
go vet ./...
staticcheck ./...
govulncheck ./...
go test -race -count=1 -timeout=10m ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...
npx --no-install playwright install --with-deps chromium
npm run test:e2e
~~~

E2E 运行目录无论成功或失败都会上传。CI 通过表示这些命令在 CI 环境完成，不表示真实 provider、用户网络或所有平台均已验证。

## 证据和失败处理

失败时保留原始输出、manifest、截图和 commit 信息，先缩小到能回答问题的测试区域，再修改实现。不要删除红例后只保留绿例，也不要把真实敏感 transcript 上传到 issue 或 artifact。

测试通过只能证明测试范围内的行为；对于未运行的 macOS、真实 provider、反向代理或可选 browser tools，提交说明必须明确写成未验证。
