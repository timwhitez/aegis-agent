#!/bin/sh
set -eu
# Explicit operator action only. No browser or native dependency downloads.
# Usage: install.sh /absolute/private/install-root /absolute/python3.12
[ "$#" -eq 2 ] || { echo 'usage: install.sh ABS_INSTALL_ROOT ABS_PYTHON3.12' >&2; exit 2; }
case "$1" in /*) ;; *) echo 'install root must be absolute' >&2; exit 2;; esac
case "$2" in /*) ;; *) echo 'Python must be absolute' >&2; exit 2;; esac
[ "$(uname -s)/$(uname -m)" = Linux/x86_64 ] || { echo 'supported platform: Linux x86_64' >&2; exit 2; }
"$2" -I -c 'import sys; assert sys.version_info[:3] == (3,12,3), "requires CPython 3.12.3"'
[ ! -e "$1" ] && [ ! -L "$1" ] || { echo 'target already exists; refusing to alter it' >&2; exit 2; }
umask 077
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
# Refuse symlink parents; do not inspect .env or helper files outside this root.
"$2" -I -c 'import pathlib,sys; p=pathlib.Path(sys.argv[1]); assert all(not a.is_symlink() for a in [p,*p.parents]), "symlink install target"' "$1"
"$2" -m venv "$1"
printf '%s\n' 'aegis-agent-browser-use-v1' > "$1/.aegis-browser-install"
"$1/bin/python" -I -m pip install --index-url https://pypi.org/simple --require-hashes --only-binary=:all: --report "$1/wheel-sources.json" -r "$script_dir/requirements.lock"
"$1/bin/python" -I "$script_dir/inventory.py" "$script_dir/requirements.lock" "$1/install-metadata.json"
printf 'Installed pinned Python runtime at %s; no browser downloaded.\n' "$1"
