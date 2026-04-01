# Change Summary

## root cause
Two defects combined to break the expected account-creation behavior: the API handler accepted unknown JSON fields and serialized the full internal account model back to clients, and the shared default-quota path treated omitted quota values as `0` instead of resolving them from configuration. In addition, config defaults returned `0` instead of the expected stable default `1000`.

## files changed
- `internal/api/handler.go`
- `internal/quota/policy.go`
- `internal/config/config.go`
- `reports/progress.md`
- `reports/validation.md`
- `reports/change-summary.md`

## validation
- Reproduced the initial failure with `go test ./internal/api`.
- Ran `go test ./...` after the first fix to expose remaining shared default-quota defects.
- Ran `go test ./...` after the quota/config repair; all packages passed.

## remaining risks
- The config parsing is still intentionally minimal and only recognizes the currently tested `ACCOUNT_DEFAULT_QUOTA=small` case plus the stable default path.
- Unknown-field rejection is implemented on this handler path; similar strictness may still be absent in other handlers if they are added later.
