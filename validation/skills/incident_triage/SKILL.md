---
name: incident_triage
description: Incident triage workflow for logs, config, and rollout clues
---
When asked to diagnose an incident:

1. Read the local `AGENTS.md` first if the workspace has one.
2. Build a short timeline from logs before proposing a root cause.
3. Keep three buckets separate: confirmed facts, likely cause, open questions.
4. Prefer the smallest corrective action that addresses the root cause.
5. If a report is requested, write it under `reports/`.
