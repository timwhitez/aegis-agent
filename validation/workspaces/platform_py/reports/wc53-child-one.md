# scope
- Reviewed `README.md` and `tests/test_report.py` only.

# findings
- `README.md` says current tests expose drift in both workspace safety and severity rollup, but `tests/test_report.py` only covers severity ordering in `build_summary`; there is no workspace-safety coverage in the reviewed test file.
