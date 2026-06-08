# NGEN Agent 工件与上下文规范 v0.3

> 状态: Draft
> 最后更新: 2026-03-18
> 范围: `.ngen/` 布局、artifact schemas、context pack、compaction、promotion rules

## 1. 本文档现在如何阅读

`docs/05-artifacts-and-context.md` 保留为 artifacts/context owner 入口页。当前 active contract 以 `docs/00-repo-status.md` 为准，详细内容已拆到 `docs/05-artifacts-and-context/`：

1. [layout-and-ownership.md](./05-artifacts-and-context/layout-and-ownership.md)
   当前 `.ngen/` 布局、active artifacts 与 owner。
2. [task-lifecycle-artifacts.md](./05-artifacts-and-context/task-lifecycle-artifacts.md)
   task artifacts、event taxonomy 与 snapshot contract。
3. [coordination-and-memory-artifacts.md](./05-artifacts-and-context/coordination-and-memory-artifacts.md)
   当前 coordination/session/worker/memory artifacts。
4. [progress-handoff-and-context.md](./05-artifacts-and-context/progress-handoff-and-context.md)
   progress / handoff / context 参考；当前代码已采用其中的最小活跃合同。

## 2. Owner 边界

本 owner 文档及其子目录继续共同拥有以下真相：

- `.ngen/` runtime state root 的布局与 artifact owner 分工；
- artifact refs、stable IDs 所需的 schema 最低约束；
- task-local artifacts 与 workspace-level memory artifacts 的边界；
- context pack 的输入结构、assembly 顺序、budget、compaction 与 promotion 规则；
- ACP / headless / interactive surfaces 如何映射到同一 artifacts truth。

## 3. 拆分后的使用原则

- 需要修改单个 schema 时，优先改对应子文档，而不是回到大总表里搜索。
- 需要确认 artifact owner、surface mapping 或 context 预算时，先看入口页，再下钻到子文档。
- 如果某个概念横跨多个小文档，以 owner 更直接的那份为准；例如：
  - `state.json`、`criteria/latest.json`、`completion/latest.json` 在 task lifecycle artifacts 中定义；
  - 当前 active 的 watch artifacts，以及 future coordination/memory reference，在 coordination artifacts 中说明；
  - context assembly 与 compaction 在 progress/context 文档中定义。

## 4. 与其他 owner docs 的关系

- lifecycle、done gate、watch、resume、approval 语义以 [docs/04-runtime.md](./04-runtime.md) 为准。
- config、CLI、ACP、tool interfaces 与 package map 以 [docs/06-go-package-and-api.md](./06-go-package-and-api.md) 为准。
- verifier/review/policy/surface parity 的最低要求以 [docs/07-verification-security-and-ops.md](./07-verification-security-and-ops.md) 为准。
