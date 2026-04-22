# Long Audit Validation

## Changes made

1. Created `reports/long_audit.md` with a spec / README / implementation / tests consistency audit.
2. Fixed one small confirmed documentation drift in `README.md`:
   - updated the large-project profile bullet from `experimental delegate|children|queue|web` to `experimental delegate|children|queue|tui|web`

## Why this fix was chosen

- The implemented CLI surface and existing tests already include `tui` in the explicit experimental command usage.
- The README omission was deterministic, low risk, and self-contained.
- No runtime logic change was necessary.

## Validation run

Executed targeted tests:

```sh
go test ./internal/app -run 'TestExperimentalCommandShowsUsageWhenExplicitlyInvoked|TestUsageShowsCoreSurfaceOnlyByDefault'
```

Observed result recorded in `reports/long_audit_test_output.txt`:

- `ok   go-cli-agent/internal/app (cached)`

## Validation assessment

- The focused CLI usage tests still pass after the README correction.
- No broader test run was necessary because the only codebase change was documentation.

## Output files

- `reports/long_audit.md`
- `reports/long_audit_validation.md`
- `reports/long_audit_test_output.txt`
