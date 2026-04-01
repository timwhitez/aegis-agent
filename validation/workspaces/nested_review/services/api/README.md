# API Service

The API service exposes ticket creation and assignment endpoints for internal tooling.

Current assumptions:

- requests are trusted to come from the internal VPN
- handler-level validation should reject malformed input
- customer-visible identifiers should not leak internal primary keys
