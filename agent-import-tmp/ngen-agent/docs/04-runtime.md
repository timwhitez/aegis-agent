# NGEN Agent 运行时规范 v0.3

> 状态: Draft
> 最后更新: 2026-03-18
> 范围: task lifecycle, state machine, loop semantics, watch, aside, resume, done gate

## 1. 本文档现在如何阅读

`docs/04-runtime.md` 保留为运行时 owner 入口页，详细规范已按功能域拆到 `docs/04-runtime/`：

1. [lifecycle-and-state.md](./04-runtime/lifecycle-and-state.md)
   phase/state 定义、`Plan` / `plan` / `update_plan` 边界、状态转换规则。
2. [loop-intents-and-guards.md](./04-runtime/loop-intents-and-guards.md)
   单轮 loop contract、intent 模型、伪代码、done gate、repair loop、loop guards。
3. [control-primitives-and-memory.md](./04-runtime/control-primitives-and-memory.md)
   `aside`、`/btw`、`watch`、`/loop`、workspace memory semantics。
4. [resume-approvals-and-review.md](./04-runtime/resume-approvals-and-review.md)
   resume、replay、人工审批、permission modes、subtask contracts、review lane、stop conditions。

## 2. Owner 边界

本 owner 文档及其子目录继续共同拥有以下运行时真相：

- task lifecycle 与 `Explore -> Plan -> Execute -> Verify -> Review` 主循环；
- `phase`、`state`、collaboration mode、permission mode 的边界；
- `Done`、repair、watch、resume、approval、review 的语义；
- `aside`、`/btw`、`/loop`、workspace memory 这些 control primitives 的合同；
- parent / child 协作时的 runtime contract 与 stop conditions。

若子文档之间出现重复，以更具体的小文档为准；若跨文档概念与其他 owner docs 冲突，以 `README.md` 中声明的 owner 分工为准。

## 3. 设计中心

运行时拆分后，设计中心保持不变：

- 复杂度应体现在 artifacts、verifiers、policies 与 explicit control 上，而不是藏在不可观察的内存状态里；
- `Plan` phase、`plan` collaboration mode、`update_plan` checklist tool 必须严格区分；
- `Done` 只能由 runtime gate 放行，不能由模型自己宣布；
- replay-sensitive side effects 必须走 stable IDs、checkpoint 和 reconcile；
- 高权限模式改变的是权限边界，不取消 evidence、review 与 observability。

## 4. 与其他 owner docs 的关系

- artifacts、refs、checkpoints、criteria/completion 持久化结构以 [docs/05-artifacts-and-context.md](./05-artifacts-and-context.md) 为准。
- CLI / ACP / config / package interfaces 以 [docs/06-go-package-and-api.md](./06-go-package-and-api.md) 为准。
- verifier levels、policy levels、安全边界与 surface parity 以 [docs/07-verification-security-and-ops.md](./07-verification-security-and-ops.md) 为准。
- phase gates、acceptance tests 与交付顺序以 [docs/08-delivery-plan.md](./08-delivery-plan.md) 为准。
