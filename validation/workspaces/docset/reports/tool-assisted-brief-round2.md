# Tool-Assisted Brief Round 2

## Confirmed facts

- Lantern Desk is a lightweight internal service desk for mid-sized support teams.
- Current goals include reducing first-response latency for high-priority tickets, giving team leads a single queue health view, and keeping rollout requirements simple for self-hosted deployments.
- Current product support includes ticket intake from email and a basic web form, assignment rules by team and severity, SLA timers, and a small reporting page with daily queue trends.
- Known constraints include only 30 days of trend data, intentionally minimal v1 integrations, and a need for clear backup and restore guidance for self-hosted customers.
- The next release targets three customer-facing themes: better queue visibility for team leads, clearer SLA breach explanations in ticket detail pages, and safer self-hosted upgrade instructions.
- The release must not add a new navigation structure, background job infrastructure changes, or breaking API changes for the existing webhook endpoint.
- Documentation expectations are one short launch brief, one operator checklist for upgrades, and a clear note on residual risk after rollout.
- Recent ops notes say the daily queue trend job can start late after VM restarts but catches up later, support managers want clearer SLA-breach explanations after reassignment, and self-hosted customers asked for a quick database backup verification step in the upgrade guide.

## Suggestions

- Keep the launch brief short and customer-facing, centered on queue visibility, clearer SLA explanations, and safer self-hosted upgrades.
- Explicitly mention the residual risk that the daily queue trend job may start late after VM restarts, while noting that it catches up on the next scheduled run.
- Include a quick database backup verification step in the operator upgrade checklist for self-hosted customers.
- Avoid implementation detail and do not imply changes to navigation, job infrastructure, or the existing webhook API.
