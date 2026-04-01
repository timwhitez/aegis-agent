# Spec

## Task
Diagnosis and durable-planning only. Read local `AGENTS.md` and `README.md` first, create the reports stack under `reports/`, run the narrowest failing tests, record the concrete failing surface, and stop without implementation.

## Constraints
- Stay within diagnosis/planning; do not patch code yet.
- Prefer narrow test execution before broad suite runs.
- Keep durable notes so a later turn can resume after compaction.

## Initial context read
- `AGENTS.md` read.
- `README.md` read.

## Candidate test surfaces discovered
- `internal/api/handler_test.go`
- `internal/config/config_test.go`
- `internal/quota/policy_test.go`
