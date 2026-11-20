"""Run the normal scheduler with a shared task whose grader changes its output."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

from smoke_docker import HERE, IMAGE, PROMPT, fixture_agent, write


def file_receipt(content):
    return hashlib.sha256(b"F\0delivery\0" + hashlib.sha256(content).digest()).hexdigest()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--evidence-helper", type=Path, required=True)
    helper = parser.parse_args().evidence_helper.resolve()
    env = dict(os.environ, SMOKE_PROVIDER_KEY="SMOKE_HOST_SECRET")
    env.pop("AEGIS_BENCH_ARTIFACT_RECIPIENT_CERT", None)
    with tempfile.TemporaryDirectory(prefix="aegis-capture-") as tmp:
        root = Path(tmp)
        task = root / "tasks" / "capture"
        agent = root / "agent"
        fixture_agent(agent)
        agent.write_text(agent.read_text() + """
subdir = Path('/submission/result')
subdir.mkdir()
(subdir / 'item').write_text('directory delivery')
""")
        write(task / "task.toml", f"""
[agent]
timeout_sec=45
[verifier]
timeout_sec=20
[environment]
docker_image="{IMAGE}"
cpus=1
memory_mb=512
allow_internet=true
""")
        write(task / "environment/Dockerfile", "FROM " + IMAGE + "\n")
        write(task / "instruction.md", PROMPT)
        write(task / "tests/test.sh", """#!/bin/sh
set -eu
test "$(cat /submission/answer)" = correct
test "$(cat /submission/result/item)" = 'directory delivery'
printf grading-write > /submission/answer
printf replaced > /submission/result/item
printf late > /submission/never-created-by-agent
echo 1 >/logs/verifier/reward.txt
""")
        config = root / "config.yaml"
        write(config, """default_provider: synthetic
providers:
  synthetic:
    api_provider: anthropic
    api_key_env: SMOKE_PROVIDER_KEY
    base_url: http://127.0.0.1:1
    model: synthetic
""")
        paths = ["/submission/answer", "/submission/never-created-by-agent", "/submission/result"]
        for capture in (False, True):
            run = root / ("selected" if capture else "default")
            init = [sys.executable, "-B", HERE / "scheduler.py", "init", "--tasks", task.parent,
                    "--run", run, "--config", config, "--binary", agent, "--evidence-helper", helper]
            if capture:
                for path in paths:
                    init.extend(["--capture", "capture=" + path])
            subprocess.run(init, check=True, env=env)
            subprocess.run([sys.executable, "-B", HERE / "scheduler.py", "run", "--run", run],
                           check=True, env=env)
            outcome_path, = run.glob("attempts/capture/*/outcome.json")
            outcome = json.loads(outcome_path.read_bytes())
            assert outcome["status"] == "completed" and outcome["reward"] == 1, outcome
            if not capture:
                assert outcome["artifact_selection"] == "not_configured" and outcome["artifacts"] == []
                continue
            assert outcome["artifact_selection"] == "operator_selected"
            manifest = json.loads((run / "manifest.json").read_bytes())
            assert manifest["tasks"]["capture"]["capture_paths"] == paths
            frozen = "captures/capture.json"
            assert manifest["sha256"][frozen] == hashlib.sha256((run/"assets"/frozen).read_bytes()).hexdigest()
            file, missing, directory = outcome["artifacts"]
            assert file["sha256"] == file_receipt(b"correct") and file["private_copy"]["status"] == "disabled", file
            assert missing["status"] == "missing", missing
            assert directory["files"] == 1 and directory["bytes"] == len(b"directory delivery"), directory
            assert not list(outcome_path.parent.glob("*.cms"))
        print("Shared grading unchanged; captured the original file and directory, recorded the missing selection.")


if __name__ == "__main__":
    main()
