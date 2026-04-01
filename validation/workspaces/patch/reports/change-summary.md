No code change was needed.

Confirmed current status:
- `inventory.available_units` already sums `stock - reserved` for each item.
- `inventory.summarize` already reports the item count and computed available units.
- Current implementation matches the existing tests.

Validation result:
- Ran `pytest -q`
- Result: `2 passed in 0.16s`
