# Security Policy

## Supported versions

Aegis Agent is under active pre-1.0 development. Security fixes are applied to
the current default branch only; older commits, development branches, and
generated binaries are not supported release lines.

## Report a vulnerability privately

Do not open a public issue for a suspected vulnerability.

Use GitHub's private vulnerability reporting flow from the repository
`Security` tab. Include the affected commit, the smallest reproducible case,
the security impact, and any suggested mitigation. Remove credentials, live
provider transcripts, personal prompts, session data, and unrelated local
files before attaching evidence.

Reports involving workspace escape, symlink handling, shell execution,
credential persistence, provider replay, session authorization, or Web console
content rendering are especially useful when they identify the exact trust
boundary that was crossed.

The maintainers will validate the report privately and coordinate a fix and
disclosure through a GitHub security advisory when appropriate.
