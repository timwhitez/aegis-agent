# Runtime planes 与 Codex 参考边界

## 1. 当前用途

本文件现在只保留为 post-foundation 参考，不再把完整产品级六层拆分当成当前 active implementation contract。

当前仓库真正生效的实现边界，以 [overview-and-data-flow.md](./overview-and-data-flow.md) 与 [workspace-provider-and-coordination.md](./workspace-provider-and-coordination.md) 为准。

## 2. 仍有价值的长期拆层

当仓库进入 post-v0.1 阶段时，以下长期拆层仍然有参考价值：

- Kernel
- Context Assembly
- Artifact Memory
- Workspace And Tools
- Verification
- Control Plane

但在 foundation 里，这些并未全部实现成独立产品级 plane；当前代码只实现其中最小可落地闭环。

## 3. Codex 参考边界

仓库内的本地 Codex clone 目前只提供三类参考价值：

- command / runtime / artifact 之间如何分层
- project docs / config layering 如何保持 retrieval-first
- headless / structured event surface 应如何避免“只有聊天记录知道真相”

当前明确不直接 adopt 的部分：

- thread / session / rollout 作为 NGEN durable truth
- ACP/session protocol 直接照搬
- child permission inheritance 直接照搬
- hooks / skills / role config 直接升格为 foundation contract

## 4. 后续启用条件

只有当下列能力被正式提升为 owner-approved contract 后，才应回到本文件继续细化：

- provider-driven loop
- ACP
- interactive terminal
- subagents / hooks / visibility / memory
