# Tools/Skills Consistency Audit

## Scope

Compared:
- `spec/04-tools-and-skills.md`
- `internal/tools/registry.go`
- `internal/tools/registry_test.go`
- related skill/tool registration tests under the same package

## Summary

Overall, the spec and implementation are mostly aligned on the built-in registry surface, reserved names, skill command tools, and multi-agent gating. The main issues are a small spec/implementation mismatch around `todo_write` schema requirements, several ambiguous doc statements that overstate behavior not enforced by the registry itself, and a few missing tests around registration order and reserved-name handling.

## Confirmed matches

- Built-in registry includes the spec-listed core tools and task/todo tools in `internal/tools/registry.go:112`.
- Multi-agent tools are registered by default and removed when `runtime.multi_agent.enabled=false`, matching spec section 4.16–4.18 and covered by `internal/tools/registry_test.go:244`.
- Skill command tools cannot override built-in names via the reserved-name check in `internal/tools/registry.go:106` and `internal/tools/registry.go:119`.
- Skill command tools use declarative `input_schema`, and tests cover schema validation and execution behavior in `internal/tools/registry_test.go:581` and `internal/tools/registry_test.go:615`.
- `grep`/`grep_files` artifact-skipping behavior is implemented and covered by tests in `internal/tools/registry_test.go:266` and `internal/tools/registry_test.go:489`.

## Mismatches and ambiguities

- `todo_write` required fields are looser in implementation than implied by the schema helper.
  - Spec says only that `todo_write` manages the session todo list and allows one `in_progress` item, but does not document exact per-item required fields.
  - The shared schema helper marks only `content` and `status` as required in `internal/tools/registry.go:154`.
  - However, the developer tool definition exposed in this session includes `priority` and `updated_at` as required as well, and callers may rely on that stricter contract.
  - This is a doc/runtime surface mismatch worth clarifying in the spec.

- Several `agent_spawn` behavior statements are not registry guarantees.
  - The spec says omitted or `default` `provider`/`model` inherit from the parent, `mode=full-auto` aliases to `exec`, and `isolation_mode=workspace-write` aliases to `off`.
  - `internal/tools/registry.go:1080` only publishes the input schema and forwards the request to the control plane; the registry does not implement or validate those semantics itself.
  - This may still be true system-wide, but the spec currently reads as if it were guaranteed by the registry/tool layer.
  - Recommendation: qualify these as control-plane/runtime semantics, or add integration tests at the owning layer.

- “默认注册到 session tool list” is accurate for built-ins but slightly underspecified for skill tools.
  - Built-ins are always registered first, then skill command tools are appended in scan order in `internal/tools/registry.go:116` and `internal/tools/registry.go:130`.
  - The spec does not describe ordering, even though ordering affects tool list presentation and prompt stability.
  - This is not a bug, but documenting stable ordering would reduce ambiguity.

## Missing or weak test coverage

- No direct test for reserved-name rejection of a skill command tool.
  - Implementation rejects conflicts in `internal/tools/registry.go:119`.
  - I did not find a registry test asserting that a skill tool named `shell`, `agent_spawn`, etc. causes `NewRegistry` to fail.

- No direct test for `Registry.Definitions()` ordering.
  - Built-ins are appended in a fixed order and skill tools are appended after them in `internal/tools/registry.go:137`.
  - I did not find a test asserting this ordering contract.

- No registry-level test for `agent_spawn` schema surface.
  - There is coverage for visibility toggling, but not for the published enum values such as `full-auto`, `workspace-write`, or `default` in `internal/tools/registry.go:1088`.
  - If prompt/tool-surface compatibility matters, a shallow schema test would help catch accidental drift.

- No test for `todo_write` single-`in_progress` enforcement in this file.
  - The spec explicitly requires only one todo to be `in_progress`.
  - I did not see a test in `internal/tools/registry_test.go` covering rejection of multiple `in_progress` items.
  - This may exist elsewhere, but it is not covered here.

## High-confidence small bug / stale contract

- `todo_write` published contract appears inconsistent across surfaces.
  - Evidence: implementation schema helper requires only `content` and `status` in `internal/tools/registry.go:154`.
  - Evidence: the exposed tool definition used by the CLI session requires `content`, `status`, `priority`, and `updated_at`.
  - Why this matters: tool callers may be told they must always send `priority` and `updated_at`, while the registry implementation accepts items without them.
  - Suggested minimal fix: align the published contract in one direction.
    - Preferred doc fix: update `spec/04-tools-and-skills.md` to explicitly document the actual per-item requirements for `todo_write`.
    - Or code fix: if strict fields are desired, change `todoItemSchema()` in `internal/tools/registry.go:154` to require `priority` and `updated_at`, then add/update tests.

## Suggested spec edits

- In `spec/04-tools-and-skills.md`, document the exact `todo_write` item schema, especially whether `priority` and `updated_at` are required.
- In section 4.16, clarify that provider/model inheritance and alias handling are runtime/control-plane semantics, not enforced by `Registry` itself.
- In section 1 or 4, optionally state that built-ins are registered first in a fixed order and skill command tools are appended afterward.

## Suggested tests

- Add a test that a skill command tool using a reserved built-in name is rejected by `NewRegistry`.
- Add a test asserting `Definitions()` ordering: built-ins first, then skill tools.
- Add a focused test for `todo_write` rejecting multiple `in_progress` items, if not already covered elsewhere.
- Add a shallow schema-surface test for `agent_spawn` enum values to catch accidental prompt/registry drift.
