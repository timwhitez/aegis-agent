# OpenClacky Cache Optimization Plan

## Scope

This plan applies OpenClacky cache-hit lessons to `go-cli-agent` without changing product functionality, workflow semantics, or the Web-first runtime boundary.

Current phase boundary:

- Keep provider-specific replay and cache mechanics inside provider adapters.
- Keep `session / state / messages / events` as the local fact source.
- Do not turn cache tuning into a fixed workflow engine.
- Do not make WebConsole a second authority for prompt, session, or cache state.

Reference material checked in this slice:

- OpenClacky tech deep dive: `https://www.openclacky.com/docs/tech-deep-dive`
- OpenClacky source, cloned at `clacky-ai/openclacky@31f5644ec653ba332e48fed17ce21fc0e18f99df`
- Key OpenClacky implementation files:
  - `lib/clacky/client.rb`
  - `lib/clacky/message_format/anthropic.rb`
  - `lib/clacky/message_format/open_ai.rb`
  - `lib/clacky/agent/message_compressor_helper.rb`
  - `lib/clacky/agent.rb`
  - `lib/clacky/message_history.rb`

## Source Lessons

### 1. Keep Stable Prefixes Stable

OpenClacky treats the stable prompt and tool surface as cache-bearing structure, and avoids mutating the system prompt during compression. Cache markers are added only to the outbound request view, not persisted into the conversation history as semantic content.

Applicability:

- `go-cli-agent` already builds a stable system prompt per turn and keeps provider-specific replay inside adapters.
- The safe adaptation is provider-side marker injection, not Web / CLI prompt rewrites.
- Any marker state must remain request metadata, not durable session truth.

### 2. Use Two Recent Message Breakpoints

OpenClacky marks two recent eligible messages so Turn N creates a cache breakpoint that Turn N+1 can still read after a new user/assistant turn shifts the tail. One marker is weaker because the prior tail moves away from the active breakpoint position.

Applicability:

- `go-cli-agent` constructs provider views each turn, so it can mark the latest two Anthropic-compatible messages without changing stored messages.
- Tool-result pairing must be preserved. The existing adapter already serializes assistant `tool_use` and user `tool_result`; marker injection should wrap the final cacheable content block, not alter tool IDs.

### 3. Insert Then Compress

OpenClacky compression inserts an ordinary compression instruction/result into history and rebuilds the message list. It specifically avoids mutating the system prompt, which protects prefix cacheability.

Applicability:

- `go-cli-agent` compaction already inserts `[Conversation compacted]` as a user message in the provider view and keeps raw `messages.jsonl` unchanged.
- The first implementation slice should not redesign compaction. It should ensure cache markers can apply to the compacted provider view without touching the system prompt.

### 4. Cache Telemetry Is A Product Signal

OpenClacky records cache read/write usage and uses it as a cost and responsiveness signal. Cache hit rate affects latency and output quality as well as price because warm prefixes reduce provider-side work and preserve more useful context.

Applicability:

- `go-cli-agent` previously tracked only input/output tokens in the normalized provider result.
- The first safe telemetry improvement is to preserve provider cache read/write token counts when upstreams return them.

## Current Repo Audit

### Confirmed Current Behavior

- OpenAI Responses requests use `instructions`, `input`, `tools`, optional reasoning/text/store fields, and local session facts remain authoritative.
- Anthropic-compatible requests use structured `system`, `messages`, `tools`, optional temperature/top_p/thinking, but did not send `cache_control`.
- OpenAI / Anthropic usage normalization did not preserve cache read/write counters.
- Compaction is already a provider-view mechanism and does not overwrite raw logs.

### Gap Summary

| ID | Gap | Impact | First Safe Fix |
| --- | --- | --- | --- |
| C1 | Anthropic-compatible adapter does not emit cache markers | Claude-compatible sessions miss explicit prompt cache opportunities | Add opt-out `prompt_cache` and adapter-only cache markers |
| C2 | Cache usage counters are dropped | Cannot prove hit rate improvements from session facts | Preserve cache read/write counters in normalized usage and events |
| C3 | Specs do not describe cache controls | Future changes may leak provider logic into Web/CLI | Document provider-scope cache marker contract |
| C4 | OpenAI telemetry ignores `cached_tokens` | OpenAI automatic cache hits are invisible | Preserve `input_tokens_details.cached_tokens` when present |

## Minimal Implementation Plan

### Slice 1: Provider-Side Cache Markers And Telemetry

Status: implemented and validated.

Changes:

- Add `prompt_cache` to provider config and session provider options.
- Default `prompt_cache=true` for `anthropic-compatible` profiles, with explicit `prompt_cache: false` opt-out for custom gateways.
- In Anthropic adapter:
  - Mark the system block with `cache_control`.
  - Mark the final tool schema with `cache_control`.
  - Mark the latest two cacheable message content blocks with `cache_control`.
  - Keep markers out of durable `messages.jsonl`.
- Preserve Anthropic `cache_creation_input_tokens` and `cache_read_input_tokens`.
- Preserve OpenAI-compatible `input_tokens_details.cached_tokens` and compatible write counters when present.
- Surface cache counters in provider turn events.

Risk controls:

- Marker logic is adapter-local.
- Stored message objects are not mutated.
- Existing replay IDs and tool result ordering stay unchanged.
- Custom Anthropic-compatible gateways can opt out.

Verification:

- Unit test request serialization for Anthropic markers.
- Unit test cache telemetry parsing for Anthropic and OpenAI.
- Runtime test provider option defaults and opt-out.
- Run `gofmt`.
- Run focused `go test` packages and cached diff checks.

### Slice 2: Cache-Aware Compaction Review

Status: planned.

Goal:

- Confirm current compaction recent-message retention gives stable provider view after marker injection.
- If needed, adjust compaction only to keep provider tool-call/tool-result dependencies and latest cacheable tail more predictable.

Non-goals:

- No background idle compression unless Web-first runtime can prove safe session ownership and no hidden side effects.
- No LLM-driven compression rewrite in this slice.

### Slice 3: Provider Cache Observability

Status: planned.

Goal:

- Add a session-level derived cache summary to `session.md` or provider attempts after real provider validation proves fields are stable.

Non-goals:

- No cost calculator rewrite before current provider usage semantics are audited.
- No Web UI redesign; at most a small fact display if session facts already contain the counters.

## Review Passes

### Review 1: OpenClacky Fidelity

Result: pass with scope caveat.

The plan copies the pattern, not the full architecture. OpenClacky is Ruby, chat-completions oriented in several paths, and includes features outside this repo's Web-first scope. The safe lessons are cache marker placement, preserving stable prompt prefixes, insert-then-compress, and telemetry. The plan does not copy its skill auto-evolution, provider fallback, terminal PTY, or idle background compression as default behavior.

### Review 2: Repo Architecture Fit

Result: pass.

The implementation stays inside `internal/provider`, `internal/config`, `internal/session`, `internal/runtime`, and related specs/tests. It does not move provider replay logic into Web, CLI, tools, or compaction. It keeps raw logs authoritative and only changes outbound request shape for compatible providers.

### Review 3: Behavior And Compatibility Risk

Result: pass with opt-out requirement.

Anthropic `cache_control` is provider-specific. Enabling it for `anthropic-compatible` profiles is the desired optimization, but custom gateways may be stricter. Therefore `prompt_cache: false` is required and must be persisted in session provider options so `continue` uses the same behavior as the original run.

### Review 4: Verification Adequacy

Result: pass for Slice 1.

Validation evidence:

- `gofmt -w internal/config/config.go internal/session/types.go internal/provider/types.go internal/provider/anthropic.go internal/provider/openai.go internal/provider/provider_test.go internal/runtime/engine.go internal/runtime/runner.go internal/runtime/runner_test.go internal/config/config_test.go`
- `go test ./internal/provider ./internal/runtime ./internal/config`
- `go test ./cmd/... ./internal/... ./pkg/...`
- `go test ./validation/cmd/...`
- `go test -count=1 ./internal/provider -run 'TestAnthropicAdapterAppliesPromptCacheMarkersAndTelemetry|TestOpenAIAdapterSerializesAndParses'`
- `go test -count=1 ./internal/runtime -run TestProviderOptionsFromConfigDefaultsPromptCacheForAnthropicCompatible`
- `gofmt -l` over the edited Go files returned no files.
- `git diff --check`

## Update Log

- 2026-05-21: Created plan and started Slice 1 after reading required specs and OpenClacky docs/source.
- 2026-05-21: Completed Slice 1 implementation and validation; ready to commit after staged diff checks.
