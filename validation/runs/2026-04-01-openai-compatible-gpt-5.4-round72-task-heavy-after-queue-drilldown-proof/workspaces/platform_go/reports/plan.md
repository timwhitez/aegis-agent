# Plan

1. Create durable task/report scaffolding for diagnosis phase. ✅
2. Run the narrowest plausible failing tests, starting from package-level targets corresponding to discovered test files. ✅
3. Capture exact failure output and map it to the minimal suspected code surface. ✅
4. Record a repair plan and validation plan only; do not implement in this turn. ✅

## Repair plan for next turn
1. Inspect the account-creation handler in `internal/api`.
2. Tighten JSON decoding to reject unknown input fields.
3. Ensure response serialization omits private/internal fields such as `internal_id`.
4. Apply the expected default quota on created-account output path.
5. Re-run `go test ./internal/api` before expanding to other packages.
