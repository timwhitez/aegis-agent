# 验证工具

`validation/` 保存仓库维护的 fixture、Node 单测和 Playwright 验收脚本。默认验证不需要 provider 凭据，浏览器套件使用仓库内的确定性 fixture 驱动真实本地 Web 服务。

## 目录职责

- `cmd/`：Go fixture 和脚本运行所需的小型测试 provider。
- `scripts/*_test.mjs`：前端 API、store、i18n、Markdown sanitizer、session、审批、发送恢复、Workspace、Settings、Skills 和 vendor 完整性测试。
- `scripts/webconsole_e2e.mjs`：启动当前工作树的 binary 和本地服务，编排浏览器验收区域。
- `scripts/e2e/`：按功能拆分的 server、shell、session、plan、approvals、receipts、recovery、showcase、pages 和 audits 区域。
- `runs/`、`sessions/`、`tmp/`：本地运行产物，不应提交。

## 常用命令

~~~sh
# 仓库默认 gate：gofmt、前端语法、Node 单测和 Go 测试
./test.sh

# 只跑前端逻辑测试
npm test
node --test validation/scripts/*_test.mjs

# 只跑某个 Go 包
go test ./internal/runtime -count=1

# 安装一次浏览器依赖并运行完整浏览器验收
npm ci --ignore-scripts
npx playwright install chromium
npm run test:e2e
~~~

迭代时可以缩小 E2E：

~~~sh
node validation/scripts/webconsole_e2e.mjs --only=shell,session
node validation/scripts/webconsole_e2e.mjs --output=validation/runs/local --keep-runtime
E2E_APPROVAL_PROFILES=all npm run test:e2e
E2E_WEB_WORKERS=4 npm run test:e2e
~~~

`--only` 可用区域为：`server`、`shell`、`session`、`plan`、`approvals`、`receipts`、`recovery`、`showcase`、`pages` 和 `audits`。

## 验证覆盖

浏览器 gate 覆盖：

- 默认中文、英文切换与持久化，light theme，键盘和 modal 行为；
- session sidebar、搜索、路由刷新、命令面板和快捷键；
- session start、continue、steer、interrupt、stop 及连接恢复；
- Goal、Plan Mode、提问卡、审批 CAS、approval receipt 和失败发送恢复；
- todo/task、children、context、timeline 和 file changes；
- Workspace 列表、字节分页、读取、下载、上传、重命名、目录和删除；
- Skills 上传、安装和卸载，Settings provider probe 与保存；
- 桌面和移动布局、removed route 404、CSP、请求错误、console 错误和未翻译 durable 原文。

每次 E2E 会在 `validation/runs/` 生成截图和 `manifest.json`，记录提交、浏览器、视口、语言、哈希和检查结果。运行产物只用于本地诊断或 CI artifact，不进入源码。

## CI 对应关系

GitHub Actions 在 Ubuntu 24.04 上执行：

1. `npm ci --ignore-scripts`；
2. `./test.sh`；
3. `go vet ./...`、`staticcheck ./...`、`govulncheck ./...`；
4. `go test -race`；
5. 安装 pinned Chromium 并运行 `npm run test:e2e`；
6. 无论成功或失败都上传 E2E 运行证据。

## 失败排查

先保留失败运行目录和 manifest，再用 `--only` 缩小区域。涉及 API 或 session 状态时同时查看服务输出和对应 Go 测试；涉及页面显示时检查 locale、viewport、DOM `data-testid` 和 network/console 错误。不要用真实 provider 重跑仓库验收，也不要把包含敏感数据的日志上传到公开位置。
