# NGEN Agent 验证、安全与运维规范 v0.3

> 状态: Draft
> 最后更新: 2026-03-18
> 范围: verifier chain, review lane, policy, security posture, observability, operator actions

## 1. 本文档现在如何阅读

`docs/07-verification-security-and-ops.md` 保留为 verifier/security/ops owner 入口页。当前 active contract 覆盖 verifier/review/done gate、policy、surface parity 与扩展 profile：

1. [verification-review-and-waivers.md](./07-verification-security-and-ops/verification-review-and-waivers.md)
   当前 verifier、review 与 done gate。
2. [security-and-policy.md](./07-verification-security-and-ops/security-and-policy.md)
   当前 security / policy surface。
3. [observability-and-surface-parity.md](./07-verification-security-and-ops/observability-and-surface-parity.md)
   当前 CLI / JSON / ACP / terminal / TUI parity。
4. [failures-commands-and-minimum-bar.md](./07-verification-security-and-ops/failures-commands-and-minimum-bar.md)
   当前状态码、CLI 命令与最低可用门槛。

## 2. Owner 边界

本 owner 文档及其子目录继续共同拥有：

- verifier chain、review lane、done gate/waiver 的最低要求；
- policy levels、安全模型、profile-specific security posture；
- observability 与 cross-surface parity 合同；
- operator commands 的安全边界与最低真实可用门槛。

## 3. 使用原则

- 讨论“该不该完成、验证链要跑到哪、waiver 能否放行”时先看 verification 文档。
- 讨论“是否允许写、联网、提权、继承权限”时先看 security/policy 文档。
- 讨论 ACP、TTY interactive terminal、TUI 是否与 headless 一致时看 surface parity 文档。
- 讨论 runtime 失败怎么分类、operator 能做什么、什么叫最低安全可用时看 failures/commands 文档。

## 4. 与其他 owner docs 的关系

- phase/state、repair、watch、resume、approval 的运行时语义以 [docs/04-runtime.md](./04-runtime.md) 为准。
- completion/criteria/verification/review artifacts 的 schema 以 [docs/05-artifacts-and-context.md](./05-artifacts-and-context.md) 为准。
- CLI/ACP/config/tool annotations 的 surface 与实现 contract 以 [docs/06-go-package-and-api.md](./06-go-package-and-api.md) 为准。
- phase gates 与 acceptance matrix 以 [docs/08-delivery-plan.md](./08-delivery-plan.md) 为准。
