# NGEN Agent Go 包与 API 规格 v0.3

> 状态: Draft
> 最后更新: 2026-03-18
> 范围: package map, CLI surface, domain types, interfaces, config, dependency policy

## 1. 本文档现在如何阅读

`docs/06-go-package-and-api.md` 保留为实现/接口 owner 入口页。当前 active contract 以 post-foundation integrated baseline 为准，详细规范已拆到 `docs/06-go-package-and-api/`：

1. [package-layout-and-cli.md](./06-go-package-and-api/package-layout-and-cli.md)
   当前包布局、CLI 命令面与 headless JSON contract。
2. [config-and-domain-model.md](./06-go-package-and-api/config-and-domain-model.md)
   当前 `ngen.json`、核心领域类型与 verifier routing。
3. [interfaces-and-runtime-bridges.md](./06-go-package-and-api/interfaces-and-runtime-bridges.md)
   当前 runtime/ACP/session/worker/memory bridge。
4. [runtime-rules-and-dependencies.md](./06-go-package-and-api/runtime-rules-and-dependencies.md)
   错误模型、并发模型、依赖策略、Codex/Claude Code 参考边界。
5. [tui-surface.md](./06-go-package-and-api/tui-surface.md)
   当前 TUI surface、alternate-screen、overlay 与 runtime bridge 规格。
6. [api-rules-and-tests.md](./06-go-package-and-api/api-rules-and-tests.md)
   API 设计规则、测试目标、TDD 纪律。

## 2. Owner 边界

本 owner 文档及其子目录继续共同拥有：

- Go 单二进制实现形状与包布局；
- CLI、headless JSONL、ACP、interactive terminal、TUI 这些 surface 的 contract；
- `ngen.json` 配置形状与 runtime field ownership；
- 领域类型、核心接口、错误模型、并发模型；
- 依赖引入策略与 spec-to-code 测试最低要求。

## 3. 使用原则

- 目录结构、命令入口、ACP/terminal surface 相关问题先看 package/CLI 文档。
- 配置项、mode、permission preset、typed IDs 相关问题先看 config/domain 文档。
- 需要定义 Go interface、跨层桥接或 event semantics 时先看 interfaces 文档。
- 依赖选型与参考实现借鉴边界统一以 runtime/dependencies 文档为准。

## 4. 与其他 owner docs 的关系

- phase/state、done gate、approval、watch、resume 的运行时语义以 [docs/04-runtime.md](./04-runtime.md) 为准。
- artifact schema、context pack、workspace memory 布局以 [docs/05-artifacts-and-context.md](./05-artifacts-and-context.md) 为准。
- verifier、policy、安全边界、surface parity 以 [docs/07-verification-security-and-ops.md](./07-verification-security-and-ops.md) 为准。
- implementation order、acceptance matrix、phase gates 以 [docs/08-delivery-plan.md](./08-delivery-plan.md) 为准。
