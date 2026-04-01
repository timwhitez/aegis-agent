# Platform Go Workspace

This workspace models a small account-provisioning service with separate config,
quota, service, and API packages.

The intended task surface is larger than a toy bug:

- request decoding and API response contracts live under `internal/api`
- quota rules live under `internal/quota`
- default configuration lives under `internal/config`
- domain models live under `internal/model`

The current code intentionally contains a few contract drifts so real coding
tasks can require multi-package reasoning and test-driven repair.
