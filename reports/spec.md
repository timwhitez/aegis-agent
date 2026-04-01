# Spec
- Task: publish TT14 behavior-level proof for forced compaction using owning runtime evidence only for round66.
- Scope stays limited to the owning builder in `internal/runtime/compaction.go` plus the directly invoked persistence writes in `internal/session/store.go`.
- Validation target: prove that forced compaction only activates after the size gate, preserves raw history by persisting the original messages, and returns a compacted provider-facing view instead of mutating the stored transcript.
- Non-goal: infer any broader default exposure or registration behavior not shown in the owning runtime path.
