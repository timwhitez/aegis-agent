# Rollout Notes

- Default quota is expected to be stable when `ACCOUNT_DEFAULT_QUOTA` is unset.
- Small plans sometimes use a lower default quota during limited rollouts.
- API responses should remain public-shape only, even when internal models grow.
