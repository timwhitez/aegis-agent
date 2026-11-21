"""Exercise the real Docker/Harbor/relay/evidence boundary using a synthetic agent."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
from uuid import uuid4

HERE = Path(__file__).resolve().parent
IMAGE = "python:3.12-slim"
PROMPT = "- first line\n--json literal 'quoted' $(echo fixture) 服务 fixture-service-613.py\n\n"
MARKER = "SYNTHETIC_DISCARDED_PROVIDER_OUTPUT_2731"
def write(path, content):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content)


def fixture_agent(path, *, output="/submission", self_check=True):
    write(path, f"""#!/usr/local/bin/python3
import json, os, re, subprocess, sys
from datetime import datetime, timedelta
from pathlib import Path
assert sys.argv[1:5] == ['exec', '--config', '/tmp/aegis-benchmark/config.yaml', '--system']
assert sys.argv[6:] == ['--input-format', 'raw']
times = re.findall(r'[0-9-]+T[0-9:.]+[+]00:00', sys.argv[5])
start, cutoff = map(datetime.fromisoformat, times)
assert cutoff - start == timedelta(seconds=45)
assert sys.stdin.buffer.read() == {PROMPT.encode()!r}
assert not Path('/tmp/aegis-benchmark/instruction').exists()
for cmdline in Path('/proc').glob('[0-9]*/cmdline'):
    try:
        assert b'fixture-service-613.py' not in cmdline.read_bytes()
    except (FileNotFoundError, PermissionError):
        pass
assert not Path('/tests/test.sh').exists()
assert 'SMOKE_PROVIDER_KEY' not in os.environ
assert 'SMOKE_HOST_SECRET' not in Path('/tmp/aegis-benchmark/config.yaml').read_text()
print({MARKER!r})
p=Path({output!r})
p.mkdir()
(p/'answer').write_text('correct')
private = p/'private'
private.mkdir(mode=0o700)
(private/'result').write_text('fixture')
(private/'result').chmod(0o600)
(p/'skip.tmp').write_text('excluded')
Path('/tmp/agent-only-marker').touch()
subprocess.Popen([sys.executable,'-m','http.server','17990','--bind','127.0.0.1','--directory',str(p)],
    stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,stdin=subprocess.DEVNULL,start_new_session=True)
if {self_check!r}:
    reward=Path('/logs/verifier/reward.txt')
    reward.parent.mkdir(parents=True, exist_ok=True)
    reward.write_text('1')
    (reward.parent/'ctrf.json').write_text(json.dumps({{'results':{{'tests':[{{'name':'agent-check','status':'passed'}}]}}}}))
    (reward.parent/'test-stderr.txt').write_text('self_check.py::test_old PASSED')
sid='20260101-000000-abcdef'
s=Path('/tmp/aegis-benchmark/sessions')/sid
s.mkdir()
(s/'session.json').write_text(json.dumps({{'id':sid,'created_at':'2026-01-01T00:00:00Z','workdir':'/','mode':'exec','provider':'synthetic','model':'synthetic','completion_policy':'autonomous'}}))
(s/'state.json').write_text(json.dumps({{'status':'completed','updated_at':'2026-01-01T00:00:00Z'}}))
(s/'events.jsonl').touch()
(s/'messages.jsonl').touch()
""")
    path.chmod(0o755)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--evidence-helper", type=Path, required=True)
    args = parser.parse_args()
    helper = args.evidence_helper.resolve()
    env = dict(os.environ, SMOKE_PROVIDER_KEY="SMOKE_HOST_SECRET")
    with tempfile.TemporaryDirectory(prefix="aegis-tb-smoke-") as temp:
        root = Path(temp)
        agent, config = root / "fixture-agent", root / "profile.yaml"
        fixture_agent(agent)
        write(config, """default_provider: synthetic
providers:
  synthetic:
    api_provider: anthropic
    api_key_env: SMOKE_PROVIDER_KEY
    base_url: http://127.0.0.1:1
    model: synthetic
""")
        verifier_image = "aegis-tb-smoke-" + uuid4().hex
        try:
            for mode in ("shared", "separate", "compose"):
                task, attempt = root / mode, root / ("attempt-" + mode)
                attempt.mkdir()
                write(task / "instruction.md", PROMPT)
                write(task / "environment/Dockerfile", "FROM " + IMAGE + "\n")
                artifacts = '["/submission/answer"]' if mode == "shared" else '[{source="/submission",exclude=["skip.tmp"]}' + (',{source="/tmp/from-sidecar",service="witness"}' if mode=="compose" else '') + ']'
                parts = ['artifacts = ' + artifacts, '[agent]', 'timeout_sec=45',
                         '[verifier]', 'timeout_sec=35']
                if mode != "shared":
                    parts += ['environment_mode="separate"','[verifier.environment]',
                              f'docker_image="{verifier_image}"','cpus=1','memory_mb=512','allow_internet=true']
                parts += ['[environment]',f'docker_image="{IMAGE}"','cpus=1','memory_mb=512','allow_internet=true']
                write(task / "task.toml", "\n".join(parts) + "\n")
                checks = """#!/bin/sh
set -eu
if test -e /logs/verifier/reward.txt; then echo 0 >/logs/verifier/reward.txt; exit 1; fi
echo SYNTHETIC_DISCARDED_PROVIDER_OUTPUT_2731
test "$(cat /submission/answer)" = correct
if test -e /tmp/agent-only-marker; then
    python3 -c 'import urllib.request; assert urllib.request.urlopen("http://127.0.0.1:17990/answer",timeout=2).read()==b"correct"'
else
    test "$(cat /submission/private/result)" = fixture
    test ! -e /submission/skip.tmp
    test ! -S /run/aegis-provider/provider.sock
fi
"""
                if mode == "shared":
                    checks += "test -f /tmp/agent-only-marker\n"
                else:
                    checks += "test ! -e /tmp/agent-only-marker\n"
                # The same verifier image handles both separate and sidecar cases.
                if mode != "shared":
                    checks += 'if test -e /submission/with-sidecar; then test "$(cat /tmp/from-sidecar)" = witness; fi\n'
                if mode == "compose":
                    write(task/"environment/docker-compose.yaml", """# An ordinary monetary value: $5
services:
  main:
    depends_on:
      witness:
        condition: service_healthy
  witness:
    image: python:3.12-slim
    command: ["sh", "-c", 'test -n "$$HOME" && echo witness >/tmp/from-sidecar && sleep infinity']
    healthcheck:
      test: ["CMD","test","-s","/tmp/from-sidecar"]
      interval: 1s
      timeout: 1s
      retries: 10
""")
                    a = agent.read_text().replace("(p/'answer').write_text('correct')",
                        "(p/'answer').write_text('correct')\n(p/'with-sidecar').touch()")
                    agent.write_text(a)
                checks += "echo 1 >/logs/verifier/reward.txt\n"
                write(task / "tests/test.sh", checks)
                if mode == "separate":
                    write(task / "tests/Dockerfile", "FROM " + IMAGE + "\nCOPY test.sh /tests/test.sh\n")
                    subprocess.run(["docker", "build", "--pull=false", "-q", "-t", verifier_image,
                                    str(task/"tests")],check=True, stdout=subprocess.DEVNULL)
                cmd = [sys.executable, HERE/"trial.py", "--task", task, "--attempt", attempt,
                       "--binary", agent, "--config", config, "--evidence-helper", helper,
                       "--label", uuid4().hex]
                subprocess.run(cmd, env=env, check=True)
                result = json.loads((attempt/"outcome.json").read_text())
                assert result["verify_rc"] == 0, result
                assert result["status"] == "completed" and result["reward"] == 1, result
                assert result["provider_http"] == {"upstream_attempts": 0, "responses": 0}, result
                assert result["verifier_evidence"]["cases"] == [], "pre-grade self-check appeared as a grader case"
                assert result["verifier_evidence"]["files"]["ctrf.json"]["status"] == "missing"
                assert all(a["status"] == "collected" for a in result["artifacts"]), result
                if mode == "compose":
                    assert any(a['service']=='witness' and a['bytes']==8 for a in result['artifacts']),result
                resources = json.loads((attempt/"resources-agent.json").read_text())
                seen = {c["service"]: c for c in resources}
                assert seen["main"]["nanocpus"] == 10**9 and seen["main"]["memory_bytes"] == 512*1024**2, seen
                assert ("witness" in seen) == (mode == "compose")
                verifier = json.loads((attempt/"resources-verifier.json").read_text())
                assert (verifier[0]["image_id"] == seen["main"]["image_id"]) == (mode == "shared")
                assert json.loads((attempt/"diagnostics.json").read_text())["started"]
                for f in attempt.iterdir():
                    data = f.read_bytes()
                    assert MARKER.encode() not in data and b"SMOKE_HOST_SECRET" not in data
                print(mode, "passed", flush=True)
                if mode == "shared":
                    # Same terminal scoring wrapper as the polyglot reference.
                    # Restrict PATH only in this verifier so no package/network
                    # setup is needed to exercise the failed launcher.
                    write(task / "tests/test.sh", """#!/bin/bash
export PATH=/empty-verifier-path
uvx \\
  -p 3.13 -w pytest==8.4.1 -w pytest-json-ctrf==0.3.5 \\
  pytest --ctrf /logs/verifier/ctrf.json /tests/test_outputs.py -rA

if [ $? -eq 0 ]; then
  echo 1 > /logs/verifier/reward.txt
else
  echo 0 > /logs/verifier/reward.txt
fi
""")
                    failed_grader = root / "attempt-missing-scorer"
                    failed_grader.mkdir()
                    cmd[cmd.index("--attempt") + 1] = failed_grader
                    cmd[-1] = uuid4().hex
                    subprocess.run(cmd, env=env, check=True)
                    result = json.loads((failed_grader / "outcome.json").read_text())
                    assert result["status"] == "verifier_error" and result["reward_valid"] is False, result
                    assert result["reward"] == result["observed_reward"] == 0, result
                    assert result["verifier_evidence"]["log_signals"]["pytest_launcher_missing"] == 1, result
                    print("failed test launcher retained native zero as unscored", flush=True)
                    write(task/"tests/test.sh", "#!/bin/sh\nexit 24\n")
                    negative = root/"attempt-no-fresh-reward"
                    negative.mkdir()
                    cmd[cmd.index("--attempt") + 1] = negative
                    cmd[-1] = uuid4().hex
                    subprocess.run(cmd, env=env, check=True)
                    ungraded = json.loads((negative/"outcome.json").read_text())
                    assert ungraded["verify_rc"] == 24, ungraded
                    assert ungraded["status"] == "infra_invalid" and ungraded["reward"] is None, ungraded
                    print("stale reward rejected", flush=True)
                    mismatched = root / "attempt-mismatched-exit"
                    mismatched.mkdir()
                    wrong_helper = root / "wrong-exit-helper"
                    # Emulate a disconnected Docker client: the signed-off child
                    # status and the transport status contradict each other.
                    import shlex
                    wrong_helper.write_text("#!/bin/sh\n"
                        'if [ "$1" = "--check" ]; then exec ' + shlex.quote(str(helper)) + ' --check; fi\n'
                        'while [ "$1" != "--" ]; do if [ "$1" = "--output" ]; then dest=$2; fi; shift; done\n'
                        'shift\n"$@" >/dev/null 2>&1\n'
                        'echo \'{"schema_version":1,"started":false,"category":"not_started",'
                        '"exit_code":1,"sessions":[]}\' > "$dest"\nexit 0\n')
                    wrong_helper.chmod(0o755)
                    cmd[cmd.index("--attempt") + 1] = mismatched
                    cmd[cmd.index("--evidence-helper") + 1] = wrong_helper
                    cmd[-1] = uuid4().hex
                    subprocess.run(cmd, env=env, check=True)
                    untrusted = json.loads((mismatched/"outcome.json").read_text())
                    assert untrusted["status"] == "infra_invalid" and untrusted["verify_s"] is None, untrusted
                    assert not (mismatched/"resources-verifier.json").exists()
                    print("unconfirmed child exit blocked shared verification", flush=True)
                    cmd[cmd.index("--evidence-helper") + 1] = helper
                    interrupted = root / "attempt-interrupted"
                    interrupted.mkdir()
                    slow_agent = root / "slow-agent"
                    slow_agent.write_text(agent.read_text() + "\nimport time; time.sleep(90)\n")
                    slow_agent.chmod(0o755)
                    cmd[cmd.index("--binary") + 1] = slow_agent
                    cmd[cmd.index("--attempt") + 1] = interrupted
                    label = uuid4().hex
                    cmd[-1] = label
                    proc = subprocess.Popen(cmd, env=env)
                    try:
                        deadline = time.monotonic() + 45
                        while time.monotonic() < deadline and proc.poll() is None:
                            containers = subprocess.check_output(
                                ["docker", "ps", "-q", "--filter",
                                 "label=com.docker.compose.project=aegistb-" + label + "__env"],
                                text=True).split()
                            if containers and subprocess.run(
                                ["docker", "exec", containers[0], "test", "-f",
                                 "/tmp/aegis-benchmark/sessions/20260101-000000-abcdef/state.json"]
                            ).returncode == 0:
                                break
                            time.sleep(0.25)
                        else:
                            raise AssertionError("synthetic agent did not reach its interruption point")
                        proc.terminate()
                        assert proc.wait(timeout=45) == 0
                    finally:
                        if proc.poll() is None:
                            proc.kill()
                            proc.wait()
                        subprocess.run([sys.executable, HERE/"trial.py", "cleanup", "--label", label], check=True)
                    stopped = json.loads((interrupted/"outcome.json").read_text())
                    assert stopped["verify_rc"] is None, stopped
                    assert stopped["status"] == "interrupted" and stopped["reward"] is None, stopped
                    assert stopped["agent_s"] is not None and stopped["diagnostics"] == "collected", stopped
                    assert stopped["verifier_evidence"]["status"] == "not_started", stopped
                    assert stopped["verifier_evidence"]["cases"] == [], stopped
                    assert stopped["provider_http"] == {"upstream_attempts": 0, "responses": 0}, stopped
                    assert any(a["status"] == "collected" for a in stopped["artifacts"]), stopped
                    print("cancelled trial retained evidence", flush=True)
                    timed_out = root / "attempt-agent-timeout"
                    timed_out.mkdir()
                    slow_agent.write_text("#!/bin/sh\nexec sleep 30\n")
                    write(task / "task.toml", "\n".join(parts).replace("timeout_sec=45", "timeout_sec=1") + "\n")
                    cmd[cmd.index("--attempt") + 1] = timed_out
                    cmd[-1] = uuid4().hex
                    subprocess.run(cmd, env=env, check=True)
                    expired = json.loads((timed_out / "outcome.json").read_text())
                    assert (expired["status"], expired["stage"], expired["phase_timeout_sec"]) == ("agent_timeout", "agent", 1), expired
                    assert expired["verify_s"] is None and expired["reward"] is None, expired
                    print("Harbor recorded native agent timeout", flush=True)
                    nonroot = root / "attempt-task-user"
                    nonroot.mkdir()
                    nonroot_agent = root / "task-user-agent"
                    fixture_agent(nonroot_agent, output="/tmp/submission", self_check=False)
                    write(task / "task.toml", "\n".join(parts).replace("/submission/answer", "/tmp/submission/answer")
                          .replace("[agent]", '[agent]\nuser="nobody"') + "\n")
                    write(task / "tests/test.sh", """#!/bin/sh
set -eu
test "$(stat -c %u /tmp/submission/answer)" = "$(id -u nobody)"
test "$(cat /tmp/submission/answer)" = correct
echo 1 >/logs/verifier/reward.txt
""")
                    cmd[cmd.index("--attempt") + 1] = nonroot
                    cmd[cmd.index("--binary") + 1] = nonroot_agent
                    cmd[-1] = uuid4().hex
                    subprocess.run(cmd, env=env, check=True)
                    result = json.loads((nonroot / "outcome.json").read_text())
                    assert result["status"] == "completed" and result["reward"] == 1, result
                    print("declared unprivileged task user completed", flush=True)
                    setup_error = root / "attempt-setup-error"
                    setup_error.mkdir()
                    cmd[cmd.index("--attempt") + 1] = setup_error
                    cmd[cmd.index("--binary") + 1] = root / "missing-frozen-agent"
                    cmd[-1] = uuid4().hex
                    subprocess.run(cmd, env=env, check=True)
                    result = json.loads((setup_error / "outcome.json").read_text())
                    assert result["stage"] == "setup" and result["status"] == "infra_invalid", result
                    assert result["provider_http"] == {"upstream_attempts": 0, "responses": 0}, result
                    print("setup failure recorded actual HTTP counts", flush=True)
                    write(task/"environment/docker-compose.yaml",
                          "services:\n  main:\n    privileged: true\n")
                    unsupported = root/"attempt-unsupported"
                    unsupported.mkdir()
                    cmd[cmd.index("--attempt") + 1] = unsupported
                    cmd[-1] = uuid4().hex
                    subprocess.run(cmd, env=env, check=True)
                    rejected = json.loads((unsupported/"outcome.json").read_text())
                    assert rejected["stage"] == "preflight" and rejected["status"] == "infra_invalid", rejected
                    assert not (unsupported/"resources-agent.json").exists()
                    assert rejected.get("provider_http") is None
                    print("unsupported topology rejected before launch", flush=True)
        finally:
            subprocess.run(["docker","image","rm",verifier_image],stdout=subprocess.DEVNULL,
                           stderr=subprocess.DEVNULL)


if __name__ == "__main__":
    main()
