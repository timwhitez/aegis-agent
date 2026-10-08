# Executed by the configured private Python with -I, before upstream imports.
import importlib.metadata as metadata
import json
import os
from pathlib import Path
import runpy
import sys

os.umask(0o077)
install = Path(os.environ["AEGIS_BROWSER_INSTALL_ROOT"]).resolve()
if Path(sys.prefix).resolve() != install:
    raise RuntimeError("install_escape: Python prefix differs from the owned install root")

if sys.version_info[:3] != (3, 12, 3):
    raise RuntimeError('wrong_python: expected CPython 3.12.3')
for package, version in [('browser-use', '0.13.11'), ('browser-harness', '0.1.13'), ('python-dotenv', '1.2.2')]:
    if metadata.version(package) != version:
        raise RuntimeError(f'wrong_version: {package} requires {version}')
helpers = Path(metadata.distribution('browser-harness').locate_file('browser_harness/helpers.py')).resolve()
root = helpers.parent.parent.parent
if not root.is_relative_to(Path(sys.prefix).resolve()):
    raise RuntimeError('install_escape: harness source outside private venv')
for base in [root, Path(os.environ['BH_AGENT_WORKSPACE'])] if 'BH_AGENT_WORKSPACE' in os.environ else [root]:
    for name in ['.env', 'agent_helpers.py']:
        path = base / name
        if path.is_symlink() or path.exists():
            raise RuntimeError(f'unexpected_install_file: {path}')
mode = sys.argv[1]
if mode == 'metadata':
    print(json.dumps({'python': sys.version.split()[0], 'abi': sys.implementation.cache_tag,
                      'browser_use': metadata.version('browser-use'),
                      'browser_harness': metadata.version('browser-harness'),
                      'repo_root': str(root)}))
elif mode == 'daemon':
    sys.argv = ['browser_harness.daemon']
    runpy.run_module('browser_harness.daemon', run_name='__main__')
elif mode == 'exec':
    sys.argv = ['browser-use']
    from browser_use.cli import main
    main()
elif mode == 'doctor':
    sys.argv = ['browser-harness', 'doctor', '--json', '--require-existing-daemon']
    from browser_harness.run import main
    main()
else:
    raise RuntimeError('unknown adapter entry')
