from pathlib import Path

import pytest

from app.config import resolve_input_path


def test_resolve_input_path_allows_relative_paths_inside_root(tmp_path: Path):
    path = resolve_input_path(tmp_path, "reports/summary.md")
    assert path == (tmp_path / "reports" / "summary.md").resolve()


def test_resolve_input_path_rejects_escape(tmp_path: Path):
    outside = tmp_path.parent / "secret.md"
    outside.write_text("private")
    with pytest.raises(ValueError):
        resolve_input_path(tmp_path, str(outside))
