# Spec
- Task: run a repository consistency audit across spec, README, implementation, and tests.
- Focus areas: tool surface vs registry, `experimental web` positioning, multi-agent default semantics (`enabled by default but decided by master/agent`), and frontend actions (`history`/`refresh`/`clear`) vs backend contracts.
- Deliverables: `reports/long_audit.md` for findings and `reports/long_audit_validation.md` for fixes/tests.
- Allowance: if 1-2 small, certain drifts are found, repair them with minimal changes and validate with the smallest necessary test set.
- Non-goal: broad feature work beyond Phase 0-10 core and explicitly experimental surfaces.