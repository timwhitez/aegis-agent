# 文档地图

这里的文档描述当前代码已经提供的产品边界、运行契约和验证方法。实现与测试是事实来源；规范用于解释这些事实之间的关系，不能单独宣称尚未实现的能力。

## 推荐阅读顺序

| 读者 | 入口 | 目的 |
| --- | --- | --- |
| 使用者 | [根 README](../README.md) | 安装、启动、常用命令与安全边界 |
| 修改 runtime 的贡献者 | [产品边界](./00-product.md) → [运行时架构](./01-runtime-architecture.md) | 理解 session、provider、tool 和持久化的关系 |
| 修改 CLI 或配置的贡献者 | [CLI 与配置](./02-cli-and-config.md) | 确认命令、配置选择和输出契约 |
| 修改 Web 服务的贡献者 | [Web 控制台](./17-web-console.md) | 确认 HTTP、WebSocket、控制动作和安全检查 |
| 修改前端的贡献者 | [前端实现](./17-web-console-frontend.md) | 确认模块边界、状态流、路由和交互约定 |
| 修改测试或 CI 的贡献者 | [测试策略](./07-testing-strategy.md) → [验证说明](../validation/README.md) | 选择合适的本地检查和 CI 检查 |

## 文档分层

- **产品层**：`00-product.md` 只回答项目解决什么问题、提供哪些表面、明确排除什么范围。
- **架构层**：`01-runtime-architecture.md` 描述一次执行如何经过 runtime、provider、tool、session 和 Web/CLI 适配器。
- **操作层**：`02-cli-and-config.md` 描述用户可执行的命令与配置加载边界。
- **Web 层**：`17-web-console.md` 描述控制台的产品和服务契约；`17-web-console-frontend.md` 描述前端模块实现。
- **验证层**：`07-testing-strategy.md` 描述测试层次与 CI gate；`validation/README.md` 给出可直接复制的命令。
- **专题层**：provider、session、tool、task、hook、compaction、queue、browser 等专题文档保留在本目录中，只有在改动触及对应契约时才需要阅读。

## 更新规则

1. 改变用户可见行为、持久化格式、API、配置、工具语义或安全边界时，同一提交更新对应规范。
2. 只写能从源码、测试或可重复命令得到证据的内容；无法确认的行为标记为待实现或待验证，不用愿景措辞覆盖它。
3. 根 README 负责导航和快速上手，详细字段和协议放在专题文档，避免同一规则出现多个相互漂移的版本。
4. 删除或退役的路径从文档中移除，不用兼容说明把旧入口重新包装成当前入口。
