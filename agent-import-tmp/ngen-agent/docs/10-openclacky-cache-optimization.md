# OpenClacky Cache-Hit Optimization Record

> Scope: source-grounded analysis, accuracy review, implementation plan, and update record for improving NGEN provider cache-hit behavior without changing product functionality.
> Source baseline: OpenClacky public docs and `clacky-ai/openclacky` `main` at `31f5644` as inspected on 2026-05-21.

## Objective

Optimize NGEN's harness-layer cache geometry using OpenClacky's cache-hit design as the reference target. The goal is not only lower token cost; cache hits also reduce first-token latency and keep long-running work inside a more stable model-visible context.

Status: all four planned slices are implemented and validated. The remaining work is measurement-driven only: use `provider_usage.jsonl` and mission/harness usage summaries before claiming a quantified hit-rate improvement.

Non-goals for this slice:

- Do not replace NGEN's artifact-first runtime with chat-history truth.
- Do not change task behavior, provider schemas, approval semantics, verifier gates, or product capabilities.
- Do not claim OpenClacky's measured cache hit rate for NGEN until NGEN has equivalent telemetry and live measurement.

## Source Findings

OpenClacky's high cache-hit approach is built around cache-prefix stability, not just token counting:

1. **Double cache markers.** OpenClacky applies `cache_control` to the last two eligible conversation messages. The older marker is usually the read breakpoint on the next turn, and the new tail marker writes the next breakpoint. It skips `system_injected: true` ephemeral messages.
2. **Frozen system prompt.** Dynamic session facts such as date, OS, working directory, model changes, and updated skills are not appended to the system prompt mid-session. They are injected as ordinary session context messages and excluded from cache marker selection.
3. **Stable tool surface.** Tool schema growth is treated as a cache invalidation surface. OpenClacky routes many capabilities through stable tools or skill invocation instead of constantly changing the tool list.
4. **Insert-then-Compress.** Compression happens inside the normal session request by appending a `system_injected` compression instruction, so the compression call can reuse the warm prefix. After compression, the next turn takes one cold rebuild and then returns to the rolling marker strategy.
5. **Idle compression.** OpenClacky compresses during idle time while provider caches are likely still warm, reducing the chance that the user's next message pays a cold long-history cost.
6. **Native Anthropic route.** OpenClacky treats Claude-over-proxy paths that preserve Anthropic `cache_control` semantics as important; the source has a regression test specifically guarding native `/v1/messages` routing for OpenRouter Anthropic models.

## NGEN Fit

NGEN differs from OpenClacky in one crucial way: NGEN's current provider path is not a single growing provider-side chat transcript. Each decision, observation, edit, and validation call is assembled from `.ngen/` artifacts into a one-shot request. That means OpenClacky's "last two real conversation messages" cannot be copied directly.

The transferable principle is still useful:

- Keep stable provider-visible prefixes stable.
- Keep dynamic runtime facts in artifact/user-context sections, not in system prompt mutations.
- Place cache breakpoints before volatile tails, and optionally at the tail for identical retry/replay cases.
- Measure cache-read/write telemetry before claiming broad completion.

Current NGEN code already aligns with several OpenClacky lessons:

- Runtime truth is artifact-first under `.ngen/`, not hidden conversation state.
- Provider prompts already split durable system instructions from dynamic task/context JSON.
- `context/latest-pack.json`, `continuity/latest.json`, and `sprint/latest.json` are explicit context artifacts instead of implicit chat memory.
- Provider mode `anthropic` already uses native Anthropic Messages API rather than an OpenAI-compatible shim.

The gap before this slice: Anthropic request bodies serialized system and user prompt text as plain strings, so NGEN could not place provider-native `cache_control` markers at any stable boundary.

## Optimization Plan

### Slice 1 - Anthropic Prompt Cache Breakpoints

Status: implemented in this slice.

Add Anthropic-only request shaping:

- Serialize the system prompt as a text content block with `cache_control: {"type":"ephemeral"}`. Because Anthropic cache order includes tools/system/messages, this gives repeated calls a stable breakpoint over the tool schema plus the system prompt.
- Serialize the user prompt as text content blocks. Split at the stable context marker, such as `Task context JSON:` or `Workspace edit context JSON:`.
- Mark both the stable instruction block and the dynamic JSON tail as cacheable. The stable block is the cross-call read target; the dynamic tail helps identical retries or replayed calls.
- Leave OpenAI Responses, OpenAI-compatible Chat Completions, command, and builtin provider behavior unchanged.

Expected effect:

- Anthropic decision/edit/observation/validation calls can reuse stable prompt/tool prefixes across repeated operations.
- Dynamic task artifact JSON remains visible and unchanged, but it no longer prevents the provider from caching the stable prelude.
- Product behavior remains the same: same prompts, same schemas, same tools, same validation flow.

Limits:

- This does not create OpenClacky-style full-session rolling message history.
- This does not yet parse cache-read/write usage into NGEN metrics.
- This does not change provider/model routing. If an Anthropic-compatible proxy does not support Anthropic content blocks or `cache_control`, it should fail explicitly through the existing provider error path rather than silently degrading.

### Slice 2 - Cache Telemetry Closure

Status: implemented in this slice.

- Parse Anthropic `usage.cache_creation_input_tokens` and `usage.cache_read_input_tokens` when present.
- Preserve provider cache metrics in task-local telemetry artifacts, harness snapshots, mission validation runs, and mission metrics using the existing `token_usage` / `prompt_cache_usage` summary fields where those fields already exist.
- Add a task-local `provider_usage.jsonl` ledger so decision, workspace observation, workspace edit, and mission validation calls can record sanitized provider usage without adding raw provider payloads, API keys, hidden prompts, or extra provider-visible event text.
- Keep provider-output schemas unchanged: usage is parsed from Anthropic response metadata after the schema-valid tool payload is decoded, and remote models cannot populate or spoof usage fields inside their tool JSON.
- Add focused tests for cache metric parsing and "unknown when provider omits usage."
- Keep secrets and raw provider payloads out of artifacts.

Expected effect:

- Harness and mission artifacts can distinguish "no telemetry exposed" from observed Anthropic cache creation/read token counts.
- Future cache-hit optimization can compare stable-prefix changes against recorded cache-read/write movement instead of relying on provider invoices or terminal logs.
- Runtime behavior remains unchanged: no new provider actions, no verifier/reviewer gate change, no additional workspace mutation, and no extra provider-visible event in the normal decision loop.

### Slice 3 - Context Volatility Budget

Status: implemented in this slice.

- Audit provider prompt sections for high-churn fields that currently sit before low-churn instructions.
- Keep volatile artifacts at the tail of provider prompts when possible.
- Document a stable/volatile ordering rule for future provider prompts.

Implementation approach:

- Keep Go struct order and prompt text semantics unchanged. Reordering JSON fields would risk changing model-visible context order and could break tests or downstream assumptions.
- Instead, refine Anthropic-only text block geometry. After the operation context marker, split the user prompt into:
  1. stable instruction prelude,
  2. lower-churn JSON prefix, and
  3. high-churn JSON tail.
- Use operation-specific volatile markers:
  - decision: `state`
  - workspace edit/observation: `recent_verification`, `previous_failures`, `collection`, `files`
  - mission validation: `root_status`, `harness`, `context_refs`
- Mark every split block cacheable. Concatenating the blocks must reproduce the exact original prompt string.

Expected effect:

- Repeated Anthropic calls can read more stable JSON prefix tokens even when runtime state, verification results, file snapshots, or mission status churn.
- Product behavior remains unchanged: same prompt bytes after concatenation, same schemas, same provider modes, same verifier/review/runtime gates.

### Slice 4 - Compression Strategy Review

Status: implemented as a behavior-preserving policy/doc slice.

- Compare NGEN's `context/summary.md` compaction to OpenClacky's insert-then-compress design.
- Preserve the artifact-first invariant: compression output must remain a task artifact, not hidden provider chat truth.
- Consider an idle compaction hook only if it can be expressed as an explicit event/artifact/checkpoint and does not mutate system prompt state.

Implementation decision:

- Do not add OpenClacky-style model-backed compression to NGEN's current runtime. NGEN does not keep a growing provider transcript; it already rebuilds decision/edit/observation/validation prompts from explicit task artifacts.
- Keep current `context/summary.md` generation as deterministic narrative artifact rendering inside `syncTaskNarrative`, updated with `context/latest-pack.json`, `continuity/latest.json`, and `sprint/latest.json` in the same pass.
- Treat future idle compaction as a separate explicit runtime feature, not as cache plumbing. It must write an event/artifact/checkpoint, keep output in task-local artifacts, be interruptible, and avoid mutating system prompt state or adding hidden provider-visible truth.
- If future compaction becomes model-backed, it should be measured with `provider_usage.jsonl` and must not use a separate summarizer system prompt that bypasses the warmed main request prefix without recording the behavior.

Expected effect:

- Avoids adding an extra provider call that would change runtime behavior, cost, latency, and observability.
- Preserves the cache gains from Slices 1-3 while keeping NGEN's long-horizon continuity artifact-first and reproducible.
- Leaves a clear contract for a future explicit idle-compaction feature if telemetry proves it is worthwhile.

## Accuracy Review

### Review 1 - Source Accuracy

Checked against OpenClacky docs and source:

- Double markers are source-backed by `Client#apply_message_caching`.
- `system_injected` skip behavior is source-backed by `is_compression_instruction?`.
- Native Anthropic cache preservation is source-backed by `client_openrouter_anthropic_spec.rb`.
- Insert-then-compress and idle compression are source-backed by `message_compressor_helper.rb` and `idle_compression_timer.rb`.

Document correction: OpenClacky's strategy is not "cache everything"; it is "cache stable prefixes and avoid marking ephemeral injected messages."

### Review 2 - NGEN Applicability

Checked against current NGEN code:

- NGEN Anthropic requests currently use one system prompt plus one user prompt per operation.
- NGEN does not maintain an OpenClacky-style multi-turn provider transcript for decision/edit/observation calls.
- Therefore, direct "last two messages" cloning would be misleading. The correct minimal transfer is stable breakpoint placement over NGEN's existing request assembly.

Document correction: The first slice promises provider-native cache markers and stable-prefix reuse, not near-100% cache hit rate.

### Review 3 - Behavior Risk

Risk review before code update:

- Semantic risk is low because only the JSON request shape changes from plain strings to Anthropic text blocks carrying the same text.
- Compatibility risk exists for non-standard Anthropic-compatible proxies. Existing behavior already requires Anthropic Messages API support; unsupported cache-control handling should surface as a provider error.
- Cost risk is bounded by at most three cache breakpoints per request: system, stable prompt prelude, dynamic tail. This stays below Anthropic's documented breakpoint limit and avoids uncontrolled marker proliferation.

### Review 4 - Telemetry Plan Accuracy

Checked against current NGEN code before Slice 2 implementation:

- `MissionMetricsRecord` and `MissionMetricsSnapshot` already have `token_usage` and `prompt_cache_usage`, but `HarnessEvaluation` and `MissionValidationRun` do not yet carry provider usage.
- Anthropic response parsing currently ignores the top-level `usage` object, so cache creation/read counts cannot reach runtime artifacts.
- `Event` artifacts feed recent provider context, so adding telemetry-only events would risk changing future provider-visible context. The safer fit is a separate task-local `provider_usage.jsonl` ledger referenced by harness/mission artifacts.
- Provider result structs are internal Go values, while provider output schemas are manual JSON schemas. Usage metadata can be attached with `json:"-"` fields after response decoding without expanding model-writable schema fields.

### Review 5 - Telemetry Behavior Risk

Risk review before code update:

- Artifact risk is low because the new ledger is append-only, task-local, and contains only numeric/string usage summaries plus refs.
- Runtime risk is bounded by writing telemetry only after successful provider response decoding. Provider request prompts, tool schemas, verifier gates, review gates, and workspace edit application remain unchanged.
- Mission context risk is bounded: mission validators may see harness usage summaries only through existing harness artifact input, not through extra normal-loop events or raw provider payloads.
- Unknown handling must remain explicit. If Anthropic omits `usage`, telemetry should record `unknown`, not `0`, because zero is a real value for cache creation/read counts.

### Review 6 - Context Volatility Plan Accuracy

Checked against current NGEN code and OpenClacky source before Slice 3 implementation:

- OpenClacky treats assembly order as a first-class cache concern and skips `system_injected` volatile messages when selecting cache breakpoints.
- NGEN does not have a growing provider-side conversation, so the equivalent is not message selection; it is stable block boundaries inside each one-shot Anthropic prompt.
- Current NGEN prompts already put instructions before the context JSON marker. The remaining avoidable churn is inside JSON tails: fields such as `state`, recent verification, workspace collection/file contents, and root mission status change much more often than task/spec/contract prefixes.
- Go struct field reordering would alter JSON order for every provider mode and carries higher behavioral risk. Anthropic-only block splitting preserves concatenated prompt text and leaves OpenAI/command/builtin paths unchanged.

### Review 7 - Context Volatility Behavior Risk

Risk review before code update:

- Semantic risk is low if tests assert that split Anthropic text blocks concatenate to the exact original prompt text.
- Cache-marker risk is bounded by the Anthropic request now using system + up to three user text blocks, staying within Anthropic's documented breakpoint limit of four.
- Compatibility risk remains limited to Anthropic-compatible endpoints that do not preserve content-block `cache_control`; they already flow through the explicit provider error path.

### Review 8 - Compression Strategy Plan Accuracy

Checked against current NGEN code and OpenClacky source before Slice 4 doc update:

- OpenClacky's insert-then-compress works because its compression instruction is appended to an existing provider-visible conversation, so the compression call can reuse the warmed system/tools/history prefix.
- Current NGEN prompt assembly is one-shot and artifact-backed. `syncTaskNarrative` renders `progress.md`, `context/summary.md`, `context/latest-pack.json`, `continuity/latest.json`, and `sprint/latest.json` from task artifacts; it does not ask a provider to rewrite a chat history.
- Therefore, directly adding an idle provider compression call would be a product/runtime behavior change, not a transparent cache optimization.
- The correct minimal transfer is a future-work constraint: any model-backed or idle compaction must stay explicit, task-local, artifact-recorded, and measured instead of becoming hidden prompt mutation.

### Review 9 - Compression Strategy Behavior Risk

Risk review before doc update:

- Code risk is intentionally zero for this slice; no runtime code changes are needed to preserve current deterministic context summary behavior.
- Product risk would be high if NGEN silently added a background provider call, because it would affect cost, latency, user-visible progress, cancellation, auditability, and provider-visible context.
- Documentation risk is mitigated by syncing the cache optimization record with owner docs for task-local compaction and narrative sync.

## Implementation Record

Slice 1 changed:

- `internal/provider/provider.go`
  - Anthropic request bodies now emit cacheable system text blocks.
  - Anthropic user prompts now split at stable context markers and mark both stable and tail blocks.
- `internal/provider/provider_test.go`
  - Added regression coverage for Anthropic decision, workspace edit, workspace observation, and mission validation request cache breakpoints.
- Owner docs updated:
  - `docs/06-go-package-and-api/config-and-domain-model.md`
  - `docs/05-artifacts-and-context/progress-handoff-and-context.md`
  - `README.md`

Validation for Slice 1:

- `go test ./internal/provider -run TestAnthropicRequestBodiesAddCacheControlBreakpoints -count=1`
- `go test ./internal/provider`
- `go test ./...`
- `git diff --check`
- `git diff --cached --check`

Slice 2 changed:

- `internal/provider/provider.go`
  - Anthropic decision, workspace edit, workspace observation, and mission validation responses now parse top-level `usage` metadata after the schema-valid payload is decoded.
  - Internal provider result structs carry non-JSON `TokenUsage` and `PromptCacheUsage` fields, so remote model output schemas stay unchanged.
- `internal/task/types.go` and `internal/artifact/store.go`
  - Added task-local `ProviderUsageRecord` and `provider_usage.jsonl` append/read helpers.
  - Added optional provider usage refs and usage summaries to harness evaluations, workspace edit records, and model-backed mission validation runs.
- `internal/runtime/`
  - Successful provider calls now append sanitized usage records for decision, workspace observation, workspace edit, and mission validation operations.
  - Harness snapshots pick up the latest provider usage record.
  - Mission metrics prefer observed token/cache usage from validation runs, provider usage ledger, or harness snapshots; omitted provider usage remains `unknown`.
- Owner docs updated:
  - `docs/05-artifacts-and-context/task-lifecycle-artifacts.md`
  - `docs/06-go-package-and-api/config-and-domain-model.md`
  - `README.md`

Validation for Slice 2:

- `go test ./internal/provider -run 'TestAnthropicResponses(ParseUsageMetadata|UseUnknownUsageWhenOmitted)|TestAnthropicRequestBodiesAddCacheControlBreakpoints' -count=1`
- `go test ./internal/runtime -run 'TestHarnessEvaluationRecordsProviderUsage|TestMissionMetricsRecordsProviderUsage' -count=1`
- `go test ./internal/provider`
- `go test ./internal/artifact`
- `go test ./internal/runtime`
- `go test ./...`
- `git diff --check`

Slice 3 changed:

- `internal/provider/provider.go`
  - Replaced the single context-marker split table with operation-specific cache split rules.
  - Anthropic user prompt blocks now split at the operation context marker and at the first high-churn JSON field for that operation.
  - The split is request-shaping only; joined text blocks preserve the exact original prompt text.
- `internal/provider/provider_test.go`
  - Updated Anthropic request-body tests to require three cacheable user text blocks for decision, edit, observation, and mission validation requests.
  - Added a focused text-preservation test for stable-prefix / volatile-tail splitting.
- Owner docs updated:
  - `docs/10-openclacky-cache-optimization.md`

Validation for Slice 3:

- `go test ./internal/provider -run 'TestAnthropic(RequestBodiesAddCacheControlBreakpoints|PromptVolatileSplitPreservesPromptText)' -count=1`
- `go test ./internal/provider`
- `go test ./...`
- `git diff --check`

Slice 4 changed:

- `docs/10-openclacky-cache-optimization.md`
  - Recorded the compression strategy decision: keep current deterministic artifact compaction and do not add hidden model-backed or idle provider compression as cache plumbing.
  - Added plan-accuracy and behavior-risk reviews for the compression slice.
- Owner docs updated:
  - `docs/05-artifacts-and-context/progress-handoff-and-context.md`
  - `docs/05-artifacts-and-context/task-lifecycle-artifacts.md`
  - `docs/05-artifacts-and-context/coordination-and-memory-artifacts.md`

Validation for Slice 4:

- `git diff --check`
- `go test ./internal/app -run TestCodingTaskCreateRunAndStatus -count=1`

Broader closeout validation:

- `./build.sh test`
- `./build.sh build`
- `git status --short --branch`

Remaining follow-up:

- Future context-volatility work should use `provider_usage.jsonl` and mission/harness usage summaries before claiming measured hit-rate improvement.
