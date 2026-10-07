#!/bin/sh
set -eu
# Explicit operator action; refuses unmarked or symlinked installs.
[ "$#" -eq 1 ] || { echo 'usage: uninstall.sh ABS_INSTALL_ROOT' >&2; exit 2; }
/usr/bin/python3 -I - "$1" <<'PY'
from pathlib import Path
import shutil
import sys
p = Path(sys.argv[1])
assert p.is_absolute() and len(p.parts) >= 3, 'unsafe uninstall target'
assert all(not a.is_symlink() for a in [p, *p.parents]), 'symlink uninstall target'
assert (p / '.aegis-browser-install').read_text().strip() == 'aegis-agent-browser-use-v1', 'unowned install'
shutil.rmtree(p)
PY
