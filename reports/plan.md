# Plan
- Use `internal/runtime/compaction.go` as the owning behavior source.
- Anchor gating to `size := estimateChars(cloned)` followed by `if size <= threshold { return cloned, nil }`.
- Anchor raw-history preservation to `WriteTranscript(..., messages)` and compact-view emission to the synthetic `[Conversation compacted]` message plus appended recent messages.
- Anchor summary persistence to `WriteArtifact(...)` in the same builder and its store implementation in `internal/session/store.go`.
- Treat anything beyond this owning path as inference or out of scope.
