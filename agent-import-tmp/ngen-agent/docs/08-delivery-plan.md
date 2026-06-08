# NGEN Agent 开发规划 v0.3

> 状态: Draft
> 最后更新: 2026-03-18
> 范围: 面向当前 integrated baseline 的开发规划、实施约束、MVP cut 与验收矩阵

## 1. 本文档现在如何阅读

`docs/08-delivery-plan.md` 保留为 delivery owner 入口页。当前 delivery 以 integrated baseline 为准：

1. [completion-and-work-packages.md](./08-delivery-plan/completion-and-work-packages.md)
   当前完成定义与必交付工作包。
2. [sequencing-and-mvp.md](./08-delivery-plan/sequencing-and-mvp.md)
   build order 与 MVP cut。
3. [implementation-discipline.md](./08-delivery-plan/implementation-discipline.md)
   SDD / TDD 实施纪律。
4. [acceptance-matrix-and-out-of-scope.md](./08-delivery-plan/acceptance-matrix-and-out-of-scope.md)
   当前验收矩阵与明确 out-of-scope。

## 2. Owner 边界

本 owner 文档及其子目录继续共同拥有：

- 当前 integrated baseline 的完成定义；
- 必交付工作包与 build-order constraints；
- MVP cut、SDD/TDD 实施纪律；
- acceptance matrix 与明确 out-of-scope 列表。

## 3. 使用原则

- 判断“这件事是不是当前 baseline 必须交付”时先看 completion/work-packages。
- 判断“实现顺序能不能颠倒、是否已经越界”时看 sequencing/MVP。
- 判断某个实现 slice 的验证策略与落地纪律时看 implementation discipline。
- 需要 traceability、验收 ID、回链 FR/NFR 时看 acceptance matrix。

## 4. 与其他 owner docs 的关系

- 产品范围、FR/NFR、profiles、success metrics 以 [docs/02-prd.md](./02-prd.md) 为准。
- lifecycle、approval、watch、resume、done gate 的运行时语义以 [docs/04-runtime.md](./04-runtime.md) 为准。
- artifacts/schema/context contracts 以 [docs/05-artifacts-and-context.md](./05-artifacts-and-context.md) 为准。
- package/API/config/tool surface 的实现 contract 以 [docs/06-go-package-and-api.md](./06-go-package-and-api.md) 为准。
- verifier/security/policy/surface parity 的最低门槛以 [docs/07-verification-security-and-ops.md](./07-verification-security-and-ops.md) 为准。
