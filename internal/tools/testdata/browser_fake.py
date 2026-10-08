#!/usr/bin/python3
# Offline executable child fixture. Uses actual stdin, process groups, IPC and files.
import base64
import json
import os
from pathlib import Path
import signal
import socket
import sys
import time

if '--version' in sys.argv:
    print('Fake Chrome 136'); sys.exit(0)
profile = next((x.split('=',1)[1] for x in sys.argv if x.startswith('--user-data-dir=')), None)
if profile:
    assert '--no-sandbox' not in sys.argv
    portfile = Path(profile, 'DevToolsActivePort')
    portfile.write_text('')
    time.sleep(0.05)
    portfile.write_text('12345\n/devtools/browser/owned\n')
    while True: time.sleep(1)
mode = sys.argv[-1]
if mode == 'metadata':
    print(json.dumps({'python':'3.12.3','abi':'cpython-312','browser_use':'0.13.11','browser_harness':'0.1.13'})); sys.exit(0)
if mode == 'doctor':
    try:
        c = socket.socket(socket.AF_UNIX); c.settimeout(0.2)
        c.connect(str(Path(os.environ['BH_RUNTIME_DIR'], 'bu.sock')))
        c.sendall(b'{"meta":"ping"}\n'); alive = json.loads(c.recv(1024))['pong']; c.close()
    except Exception:
        alive = False
    print(json.dumps({'schema_version':1,'healthy':alive,'require_existing_daemon':True,
                      'version':'0.1.13','chrome_running':None,
                      'daemon':{'name':'aegis','alive':alive,'browser_ready':alive}}))
    sys.exit(0 if alive else 1)
if mode == 'daemon':
    assert os.environ['BU_CDP_WS'] == 'ws://127.0.0.1:12345/devtools/browser/owned'
    s = socket.socket(socket.AF_UNIX)
    s.bind(str(Path(os.environ['BH_RUNTIME_DIR'], 'bu.sock'))); s.listen()
    while True:
        c,_ = s.accept(); c.recv(4096); c.sendall(b'{"pong":true}\n'); c.close()
assert mode == 'exec'
assert os.environ['BH_REQUIRE_EXISTING_DAEMON'] == '1'
assert Path(os.environ['BH_RUNTIME_DIR'], 'bu.sock').exists()
# Valid 1x1 PNG. Generated PNG is still checked by production Go decode/hash/quota.
def wait_for_load(timeout=15):
    return not Path(os.environ['BH_AGENT_WORKSPACE'], 'fake-wait-false').exists()
def capture_screenshot(path):
    assert Path(path).parent == Path(os.environ['BH_TMP_DIR'])
    if Path(os.environ['BH_AGENT_WORKSPACE'], 'fake-bad-json').exists():
        print('{invalid generated output')
    override = Path(os.environ['BH_AGENT_WORKSPACE'], 'fake-bad-png')
    data = b'invalid PNG' if override.exists() else base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=')
    Path(path).write_bytes(data)
    return path
exec(sys.stdin.read(), globals())
