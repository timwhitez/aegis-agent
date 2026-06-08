# NGEN Agent 架构规格

> 状态: Active
> 最后更新: 2026-03-19
> 当前实现中心: post-foundation integrated baseline

## 1. 现在如何阅读

`docs/03-architecture.md` 继续作为架构 owner 入口页，但本页只把当前 active implementation contract 与 richer reference 分开：

1. [overview-and-data-flow.md](./03-architecture/overview-and-data-flow.md)
   当前 runtime 的组件分层与数据流。
2. [workspace-provider-and-coordination.md](./03-architecture/workspace-provider-and-coordination.md)
   当前 workspace model、scheduler coordination 与 provider/control-surface 边界。
3. [invariants-and-failure-boundaries.md](./03-architecture/invariants-and-failure-boundaries.md)
   当前仍生效的不变量与故障边界。
4. [plane-details-and-codex-mapping.md](./03-architecture/plane-details-and-codex-mapping.md)
   未来产品化架构的参考拆层；不属于当前 active implementation contract。

## 2. 当前 active 架构判断

当前只冻结以下架构判断：

- 单进程、单二进制 Go runtime
- filesystem artifacts 是唯一 canonical truth
- app layer 只做命令编排
- runtime service 驱动 phase/state、verification、review、done gate、watch、approval、provider/session/worker/memory
- verifier 与 review 独立于 CLI
- workspace-level scheduler 只通过 `.ngen/watches/*.json` + single lease 协调，不拥有新的业务真相

## 3. future reference 的边界

以下主题仍可以在架构文档中保留为 richer reference，但当前尚未完全硬化：

- TUI
- richer role-file inheritance / discovery UX / hook schemas，超出当前内建 role contract 水合与 provider action gate
- deeper visibility / memory governance
- stronger profile-specific control planes

## 4. 与其他 owner docs 的关系

- 当前产品范围、FR/NFR 与 profiles 以 [docs/02-prd.md](./02-prd.md) 为准。
- phase/state、watch、review、approval 语义以 [docs/04-runtime.md](./04-runtime.md) 为准。
- artifacts 与 context contracts 以 [docs/05-artifacts-and-context.md](./05-artifacts-and-context.md) 为准。
- 包布局、CLI 和 JSON contract 以 [docs/06-go-package-and-api.md](./06-go-package-and-api.md) 为准。
