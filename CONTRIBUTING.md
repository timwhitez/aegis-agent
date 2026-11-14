# 贡献指南

欢迎提交修复、测试、文档和小型功能改动。项目的约束来自当前源码、测试、CI 和 [文档地图](./spec/README.md)；提交说明应该让没有上下文的 reviewer 能够复现你的判断。

## 开始前

1. 确认改动属于当前项目：本地 agent harness、Web 控制台、CLI、provider、工具、技能、session 或验证工具。
2. 阅读改动涉及的源码、测试和规范。前端改动先看 [Web 控制台前端](./spec/17-web-console-frontend.md)，配置和命令改动先看 [CLI 与配置](./spec/02-cli-and-config.md)。
3. 先确定事实来源和验证命令，再修改代码或文档。

## 实现边界

- 让 runtime 保持 provider 无关，让 provider adapter 承担协议差异。
- 让 Web 和 CLI 复用 session store 与 runtime facade，不在表面层制造第二个状态机。
- 对文件、workspace、shell、Web mutation、审批和 session 恢复保持现有安全边界。
- Web 前端继续使用仓库内嵌、无构建的 ES Module 结构；新增文案同时补齐中文和英文，持久化原文不翻译。
- 需要改变行为时，测试和对应规范放在同一个提交中；纯文档改动也要检查命令、路径和链接是否真实存在。

## 本地检查

最低检查：

~~~sh
./test.sh
~~~

前端或 Web 改动：

~~~sh
npm ci --ignore-scripts
npx playwright install chromium
npm run test:e2e
~~~

runtime、provider、配置、持久化或并发改动按范围补充：

~~~sh
go test ./internal/<package> -count=1
go vet ./...
staticcheck ./...
govulncheck ./...
go test -race -count=1 -timeout=10m ./cmd/... ./internal/... ./pkg/... ./validation/cmd/...
~~~

完整区域和 E2E 选项见 [validation/README.md](./validation/README.md)。CI 在 Ubuntu 24.04 上使用 Go 1.26.11、Node 22 和 pinned Playwright Chromium。

## Pull Request

PR 描述至少说明：

- 触发问题和改动后的行为；
- 改动涉及的模块、持久化或 API 契约；
- 运行过的命令和结果；
- 未运行的检查及原因；
- 如果包含 UI 改动，说明桌面和移动端、中文和英文覆盖情况。

提交前检查差异中没有 API key、token、`.env`、session、个人 prompt、live-provider transcript、生成 binary 或运行证据目录。安全漏洞不要公开提交，请按 [SECURITY.md](./SECURITY.md) 的方式私下报告。

## 提交习惯

提交保持单一目的，消息说明实际行为。代码修复、测试和契约文档需要一起提交，避免主干出现代码与文档暂时分叉的状态。
