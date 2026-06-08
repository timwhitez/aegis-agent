# API 设计规则与测试目标

## 1. 当前 API 设计规则

- 不依赖隐式全局状态
- 所有阻塞边界都传 `context.Context`
- app 层不直接读写 artifact 文件，统一经过 store/service
- durable artifacts 优先保存 workspace-relative 路径或稳定 artifact refs
- `task.State` 是 phase/state/status pointers 的唯一 owner
- `task.Plan` 只持有 bootstrap step graph 与 coverage
- CLI 命令变化时必须同步 `README.md` 与 owner docs
- 任何新的 JSON contract 都必须先写文档再落代码

## 2. 当前测试目标

当前代码至少要覆盖：

- `internal/task`
  - task creation defaults
  - task file validation
- `internal/artifact`
  - artifact round-trip
  - append-only JSONL write behavior
- `internal/runtime`
  - create / run / resume / auto state transition
  - coding verifier failure -> multi-attempt workspace edit -> re-verify repair path
  - task constraint enforcement on workspace edit plans
  - quality diagnostics for forbidden test-file edits, scope drift, and repeated failed/no-op repairs
  - approval lifecycle
  - parent-owned worker approval routing
  - structured input request lifecycle
  - watch + scheduler tick
  - session / worker / memory flow
  - worker evidence score and `trusted_for_parent_completion` propagation into result/contract
  - mission create/status/plan/approve/validate/run service flow
  - `/missions` session compact command bypasses remote provider and writes mission artifacts
  - typed hook registry success / failure path
  - failed_state recovery
- `internal/provider`
  - OpenAI-compatible Chat Completions tool-call contract
  - OpenAI Responses endpoint + `text.format=json_schema` contract
  - OpenAI Responses workspace edit schema contract
  - Anthropic Messages tool-use contract
- `internal/verify`
  - `coding` verifier
  - coding verifier timeout guard
  - `docs_lite` structural verifier
  - additional roots / visibility deny filtering
  - `security_review` / `reviewer` multi-check path
- `internal/review`
  - blocking finding generation
  - stable review categories, risk summary, affected paths, and worker-trust/scope/stale-context classification
- `internal/app`
  - CLI / JSON contract integration tests
  - coding auto repair with persisted workspace edit artifact
  - multi-attempt coding auto repair
  - rejected test-file edit plan leaves source-of-truth unchanged
  - rejected test-file edit plan writes blocking `diagnostics/quality-latest.json`
  - blocking coding verifier times out into structured failure instead of hanging
  - owned approval list / parent-owned approve flow
  - input request CLI integration
  - memory redaction / consolidation
  - mission CLI JSON contract and validation blocking/done outcomes
  - review blocks parent completion when worker-backed criteria cite incomplete child runtime evidence
- `internal/acp`
  - approval request / list / decide ACP contract
  - parent-owned worker approval ACP contract
  - session prompt / task bridge tests
  - session snapshot contract
  - input request ACP contract
  - worker parity / `worker_snapshot` contract
  - mutating-call `ngen.notification` contract
  - initialize / ping / JSON-RPC error mapping

## 3. TDD 纪律

- 先写与当前 slice 最接近的 failing test：contract / unit 优先于大而全集成
- `docs/08` 中的 acceptance IDs 应保持可搜索映射
- docs-only / `docs_lite` 流程优先验证 ref consistency、review 语义与状态收敛，而不是伪造代码测试覆盖

## 4. future reference

TUI、超出 builtin/command、OpenAI-compatible Chat Completions、OpenAI Responses 与 Anthropic Messages 的 broader provider matrix、dedicated browser computer-use plane，以及超出当前 bounded observation + repair-command + workspace-edit 路径的更长链路分解测试面仍未进入当前测试面。
