## scope
- Reviewed `README.md` and `tests/test_config.py` only.

## findings
- `README.md` says the current tests expose both workspace safety and severity rollup drift, but `tests/test_config.py` only covers `resolve_input_path` relative-path acceptance and direct path escape rejection. No severity ordering/rollup behavior is exercised here, so the README overstates what this test file validates.
