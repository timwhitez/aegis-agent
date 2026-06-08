# 实施顺序约束与 MVP cut

## 1. 当前 foundation 必须先冻结的内容

- artifact names 与 schema
- CLI contract
- event taxonomy
- `status_snapshot`
- verifier / review / done gate 语义
- watch / scheduler tick 语义
- failure reason codes

## 2. 当前 foundation 推荐实现顺序

1. `internal/task`
2. `internal/artifact`
3. `internal/runtime`
4. `internal/verify`
5. `internal/review`
6. `internal/app`
7. `watch` / `scheduler`
8. JSON contracts 与 tests

## 3. 当前禁止的逆序实现

- 在 artifact schema 未冻结前先做 fancy CLI
- 在 verifier/review 未稳定前先做 done gate shortcuts
- 在 watch artifact 未稳定前先做 scheduler behavior
- 在 foundation slice 未完成前先做 ACP / interactive terminal / subagents

## 4. post-foundation 路线

Foundation v0.1 不是完整 NGEN 产品。按当前仓库已收敛后的工程依赖，后续阶段建议固定为：

1. `v0.2` 自治 loop 与 provider 层
   - provider adapter
   - 模型驱动的 Explore -> Plan -> Execute -> Verify -> Review
   - 仍受 artifacts / verifier / review / done gate 约束
2. `v0.3` ACP 与外部控制面
   - stdio ACP server
   - session/load/list/prompt/cancel/permission bridge
3. `v0.4` interactive terminal 与 operator UX
   - line editor
   - structured operator intents
   - 如需要再延展为 TUI
4. `v0.5` subagents / hooks / visibility / workspace memory
   - bounded worker contracts
   - role files（当前已落地内建 contract 水合与 provider action / child-role gate；更强继承与发现 UX 仍可继续加强）
   - typed hooks（当前 baseline 已落地；更强 schema 仍可继续加强）
   - additional roots / visibility deny rules
   - workspace memory extraction / consolidation
5. `v0.6` profile 扩展
   - `security_review`
   - `reviewer`
   - 再判断是否引入 `yolo`
6. `v0.7` mission validation contract
   - workspace-level `.ngen/missions/<mission_id>/` artifacts
   - `mission create/approve/run/status/validate`
   - `/missions` compact entrypoint
   - independent artifact validator before broader tool planes

这些阶段代表 roadmap，不等于当前 foundation 已完成。
