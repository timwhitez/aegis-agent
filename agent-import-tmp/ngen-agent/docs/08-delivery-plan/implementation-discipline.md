# SDD / TDD 实施纪律

## 6. SDD / TDD 实施纪律

每个实现 slice 默认遵循：

1. 先确认 owner doc，并锁定涉及的 FR、criteria 与 acceptance IDs。
2. 先改 contract，再改 code；禁止“先写实现，之后补 schema”。
3. 先写最窄 failing test：schema / contract / unit，只有在必要时再提升到 integration / acceptance。
4. 对副作用工具，先写 replay / reconcile tests，再写执行实现。
5. role files、hooks、visibility rules 都要先有 parser / validator / policy tests，再接 runtime。
6. handoff 语言、review 语言与 completion report 必须回链 evidence refs，而不是口头说明。
7. 新增工具前先做 capability-routing 审查：如果 search/read、skills、role files 或 guide-style subagent 足以完成，就不要扩大 action space。
8. 新增 workspace-local 文件或目录时，必须先声明它属于 canonical runtime state 还是 user-managed input，并同步写清默认 ignore / commit 策略；禁止把 convenience state 悄悄混进 repo truth。
9. mission 相关改动必须先冻结 schema 与 validator tests；`mission.role_models`、`mission.json.role_plan`、`validation_contract.assertions`、plan approval gate、assertion coverage、validation provenance、lineage-aware ownership helper、orchestrator/worker scoped model routing 与 validators explicit opt-in 都要有 targeted tests。validator 只能消费 artifacts 并产出 findings，不能静默接管 broad implementation，也不能请求 workspace edit、repair command、provider decision action、`task_create` 或 worker creation。
