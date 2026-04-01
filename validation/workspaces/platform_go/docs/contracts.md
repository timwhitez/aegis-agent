# API Contract Notes

- `public_id` is safe for clients.
- `internal_id` is server-only and must never be returned to callers.
- Unknown JSON fields should be rejected so client drift is visible early.
- If clients omit quota, the service should fall back to the configured default.
