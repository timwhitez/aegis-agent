from pathlib import Path


def resolve_input_path(root: Path, candidate: str) -> Path:
    path = Path(candidate)
    if not path.is_absolute():
        path = root / candidate
    return path.resolve()
