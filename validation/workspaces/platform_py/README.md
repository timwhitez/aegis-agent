# Platform Python Workspace

This workspace models a lightweight findings-processing package.

The task surface intentionally spans multiple modules:

- `app/config.py` resolves user-provided paths
- `app/rules.py` controls severity ordering
- `app/report.py` derives user-facing summary output

The current tests expose contract drift around workspace safety and severity
rollup.
