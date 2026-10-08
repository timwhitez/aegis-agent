"""Record installed wheel metadata without importing upstream packages."""
import hashlib
import importlib.metadata as metadata
import json
from pathlib import Path
import platform
import sys
import sysconfig

packages = []
for d in sorted(metadata.distributions(), key=lambda d: d.metadata['Name'].lower()):
    if d.metadata['Name'].lower() in ('pip', 'setuptools'):
        continue  # venv bootstrap, not shipped application wheel closure
    license = d.metadata.get('License-Expression') or d.metadata.get('License')
    if not license:
        license = '; '.join(x for x in d.metadata.get_all('Classifier', []) if x.startswith('License ::')) or 'UNKNOWN'
    packages.append({'name': d.metadata['Name'], 'version': d.version, 'license_metadata': license,
                     'license_verified': False, 'source': 'https://pypi.org/project/' + d.metadata['Name'] + '/' + d.version + '/'})
source_hashes = {}
for package, paths in {
 'browser-harness': ['browser_harness/' + f + '.py' for f in ('run', 'admin', 'helpers', '_ipc', 'paths', 'daemon', 'telemetry', 'recorder')],
 'browser-use': ['browser_use/' + f + '.py' for f in ('cli', '__init__', 'logging_config', 'config')],
 'python-dotenv': ['dotenv/main.py'],
}.items():
    d = metadata.distribution(package)
    for rel in paths:
        source_hashes[rel] = hashlib.sha256(Path(d.locate_file(rel)).read_bytes()).hexdigest()
report = {'python': platform.python_version(), 'python_executable': sys.executable,
          'abi': sysconfig.get_config_var('SOABI'), 'platform': sysconfig.get_platform(),
          'lock_sha256': hashlib.sha256(Path(sys.argv[1]).read_bytes()).hexdigest(),
          'packages': packages, 'installed_source_sha256': source_hashes}
Path(sys.argv[2]).write_text(json.dumps(report, indent=2) + '\n')
