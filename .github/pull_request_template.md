## 变更内容

<!-- 用触发问题和改动后的行为描述本 PR。 -->

## 影响范围

- [ ] runtime / session
- [ ] provider
- [ ] tools / skills
- [ ] CLI / run.sh
- [ ] Web service / API
- [ ] Web frontend
- [ ] configuration
- [ ] documentation
- [ ] validation / CI

## 契约变化

<!-- 如果改变了命令、配置、持久化、API、WebSocket、安全边界或 UI 交互，请说明对应规范和兼容影响。 -->

## 验证

<!-- 填写实际运行过的命令和结果；不要把未运行的检查写成通过。 -->

~~~text
./test.sh
~~~

- [ ] 已运行受影响的聚焦测试
- [ ] 已运行必要的 E2E 区域
- [ ] 已运行必要的 race / vet / staticcheck / govulncheck
- [ ] 未运行的检查已在下方说明

未运行或已知限制：

## 安全与敏感数据

- [ ] 未提交 API key、token、密码、`.env`、session、个人 prompt、live-provider transcript 或生成 binary
- [ ] Web、文件、shell、审批或恢复边界没有被绕过
- [ ] 如涉及安全问题，已使用私下报告渠道

## UI 改动（如适用）

- 语言：中文 / English
- 视口：桌面 / 移动
- 证据目录或截图：

## 文档

- [ ] README / AGENTS / CONTRIBUTING / validation / spec 已按需要更新
- [ ] 新增路径、命令、字段和链接已经在当前仓库中核对
