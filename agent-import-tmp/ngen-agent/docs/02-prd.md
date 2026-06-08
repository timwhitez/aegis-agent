# NGEN Agent 产品需求文档 v0.3

> 状态: Draft
> 最后更新: 2026-03-18
> 产品类型: harness-first, coding-first, long-horizon agent runtime

## 1. 本文档现在如何阅读

`docs/02-prd.md` 保留为 PRD owner 入口页。当前以 `docs/00-repo-status.md` 中定义的 post-foundation integrated baseline 为准，详细内容已拆到 `docs/02-prd/`：

1. [positioning-and-scope.md](./02-prd/positioning-and-scope.md)
   当前产品定位、active scope 与 richer hardening backlog。
2. [users-flows-and-capabilities.md](./02-prd/users-flows-and-capabilities.md)
   当前产品流向与 active capabilities。
3. [requirements-and-profiles.md](./02-prd/requirements-and-profiles.md)
   当前 active FR/NFR、profile 范围与 extension surface。
4. [constraints-mvp-and-metrics.md](./02-prd/constraints-mvp-and-metrics.md)
   当前 MVP、约束与成功指标。

## 2. Owner 边界

本 owner 文档及其子目录继续共同拥有：

- 产品目标、目标用户、JTBD 与当前 active scope；
- FR/NFR、profile scope、operator-facing capability expectations；
- success criteria 所需的产品级 traceability 约束；
- 产品级 MVP、metrics 与已决策收敛项。

## 3. 使用原则

- 讨论“这个产品到底解决什么问题、v0.1 包含什么”时，先看 positioning/scope。
- 讨论“用户怎么用、关键流程和产品能力如何组合”时，先看 users/flows/capabilities。
- 讨论 FR/NFR、profile 边界、prompt stack/delegation/product requirements 时，先看 requirements/profiles。
- 讨论 MVP、产品级 success metrics 和已经冻结的决策时，先看 constraints/MVP/metrics。

## 4. 与其他 owner docs 的关系

- runtime semantics、phase/state、approval、watch、resume、done gate 以 [docs/04-runtime.md](./04-runtime.md) 为准。
- artifact schema、criteria/completion/context contracts 以 [docs/05-artifacts-and-context.md](./05-artifacts-and-context.md) 为准。
- CLI/API/ACP/config/package interfaces 以 [docs/06-go-package-and-api.md](./06-go-package-and-api.md) 为准。
- verifier/policy/security/surface parity 的最低门槛以 [docs/07-verification-security-and-ops.md](./07-verification-security-and-ops.md) 为准。
- phase gates、acceptance tests 与 build-order constraints 以 [docs/08-delivery-plan.md](./08-delivery-plan.md) 为准。
