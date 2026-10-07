# Optional browser-use runtime licensing and platform notes

The installer fetches 104 upstream wheels from PyPI directly. Aegis does not
redistribute those wheels, Chrome, or native libraries. `linux-x86_64-wheels.json`
records the actual installed URLs and SHA256; `linux-x86_64-inventory.json`
records CPython patch/ABI, versions, installed leaf source hashes and license
metadata. `requirements.lock` is the resolved hashed closure, not just direct
pins. Only binary wheels are installed; no sdist/build closure was used.

Preserve the two copied MIT notices (`browser_use-LICENSE` and
`browser_harness-LICENSE`), including copyright. The closure is **not all MIT**:
Pillow uses MIT-CMU/HPND, websockets BSD-3-Clause, lxml BSD-3-Clause, requests and
many Google SDKs Apache-2.0, certifi MPL-2.0, python-docx MIT. Per-wheel metadata
is inventory evidence, not a completed legal review; entries with
`license_verified: false` remain UNKNOWN for redistribution approval. Review
and retain each installed wheel's own license/NOTICE files before bundling.
No Playwright runtime, browser installer or Chrome binary is distributed here.

Supported Python install target: Linux x86_64, CPython 3.12.3,
`cpython-312-x86_64-linux-gnu`. The venv's pip/setuptools are Python bootstrap
components, excluded from the 104 application packages; the installer records
actual application wheel artifacts. No runtime auto-upgrade occurs.

Use `install.sh ABS_INSTALL_ROOT /usr/bin/python3.12` explicitly. Choose a new
private target; existing targets and symlink parents are refused. For missing
shared libraries, inspect `ldd /opt/google/chrome/chrome`; ask the OS package
manager/operator to supply the matching NSS/NSPR, GTK/ATK, X11/XCB, GBM/DRM,
audio and fonts as necessary. Neither script installs native packages or
changes remote debugging/Accessibility/security settings. Run Aegis as an
ordinary account capable of Chromium sandboxing. Root Chrome refuses startup;
the adapter reports failure and does not pass `--no-sandbox`.

Stop sessions before explicit `uninstall.sh ABS_INSTALL_ROOT`. Uninstall only
removes a marked owned venv and refuses symlink targets. Session artifacts and
profiles remain in the existing session root; normal host/session cleanup
policies apply. No other browser or global skill/config directory is touched.
