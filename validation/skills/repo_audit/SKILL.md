---
name: repo_audit
description: Repository audit workflow for local codebases
---
When asked to audit a repository:

1. Start with a fast inventory before reading files deeply.
2. If the question depends on default exposure, registration, config gating, or runtime behavior, locate the likely owning source file before reading many docs. Prefer file-level discovery such as `cmd/**/*.go`, `internal/**/*.go`, `pkg/**/*.go`, or precise `grep_files` / `grep` for symbols like `register`, `registry`, `builtin`, `default`, or the feature name. Do not rely on bare directory globs like `internal/*` as proof that no owning code path exists.
3. Use `todo_write` if the task has more than three concrete steps.
4. Write findings first and keep validated facts, remaining risks, and unresolved questions separate.
5. For each finding, record severity, confidence, and the concrete evidence path or line that supports it.
6. Prefer evidence-backed conclusions with file paths.
7. If asked to write a report, put it under `reports/` in the current workdir.
8. If a claim depends on default exposure, registration, config gating, or actual runtime behavior, verify the owning code path before calling it validated; otherwise keep it under risk or inference.
9. When a declaration-level hint appears inside a file, inspect the owning function or gate in that same file before broadening search.
