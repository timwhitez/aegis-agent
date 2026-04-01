# Product Brief Round 2

## Confirmed facts

- **Product**: Lantern Desk is a lightweight internal service desk for mid-sized support teams.
- **Current goals**:
  - Reduce first-response latency for high-priority tickets.
  - Give team leads a single queue health view.
  - Keep rollout simple enough for self-hosted deployments.
- **Current capabilities**:
  - Ticket intake from email and a basic web form.
  - Assignment rules based on team and severity.
  - SLA timers.
  - A small reporting page with daily queue trends.
- **Known constraints**:
  - Reporting stores only 30 days of trend data.
  - Integrations are intentionally minimal in v1.
  - Self-hosted customers need clear backup and restore guidance.
- **Release themes**:
  1. Better queue visibility for team leads.
  2. Clearer SLA breach explanations in ticket details.
  3. Safer self-hosted upgrade instructions.
- **Release guardrails**:
  - No new navigation structure.
  - No background job infrastructure changes.
  - No breaking API changes for the existing webhook endpoint.
- **Operational signals**:
  - Daily queue trend jobs can start late after VM restarts but catch up on the next run.
  - Support managers want clearer reassignment-related SLA breach explanations.
  - Self-hosted customers want upgrade guidance to include backup verification.
  - Prior launch messaging was too long and included implementation detail.
- **Documentation required for release**:
  - One short launch brief.
  - One operator checklist for upgrades.
  - Residual risk called out after rollout.

## Recommendations

- Keep launch messaging short and customer-facing, centered on queue visibility, SLA explanation clarity, and safer self-hosted upgrades.
- Explain SLA-breached status plainly, especially for reassigned tickets.
- Add a quick database backup verification step to upgrade documentation for self-hosted operators.
- Explicitly note residual risks such as delayed trend-job starts after VM restarts and the 30-day reporting retention limit.
- Position improvements as incremental within the current product structure, avoiding any implication of navigation, API, or infrastructure overhauls.
