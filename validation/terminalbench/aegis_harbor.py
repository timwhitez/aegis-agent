"""Harbor's installed-agent interface for a frozen Linux Aegis binary."""
import json
from contextlib import suppress
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import tempfile

from harbor.agents.base import BaseAgent
from harbor.verifier.verifier import Verifier
from result_evidence import VERIFIER_OUTPUT_FILES

PRIVATE = "/tmp/aegis-benchmark"
SESSIONS = PRIVATE + "/sessions"
SUMMARY = PRIVATE + "/diagnostics.json"
HELPER = PRIVATE + "/evidence"
MAX_SUMMARY = 4 * 1024 * 1024


def observe_resources(environment, location):
    ids = subprocess.check_output(
        ["docker", "ps", "-aq", "--filter",
         "label=com.docker.compose.project=" + environment.session_id], text=True, timeout=10
    ).split()
    if not ids:
        raise RuntimeError("no running containers for the expected project")
    # Request only these fields; full docker inspect includes process env.
    fmt = ('{{.Image}} {{.HostConfig.NanoCpus}} {{.HostConfig.Memory}} '
           '{{index .Config.Labels "com.docker.compose.service"}} '
           '{{.State.OOMKilled}} {{.State.Status}} {{.State.ExitCode}}')
    lines = subprocess.check_output(["docker", "inspect", "--format", fmt, *ids],
                                    text=True, timeout=10).splitlines()
    observations = []
    for line in lines:
        image, cpu, ram, service, oom, state, rc = line.split()
        if not re.fullmatch(r"sha256:[a-f0-9]{64}", image) or not re.fullmatch(r"[A-Za-z0-9_.-]{1,100}", service):
            raise ValueError("invalid Docker resource identity")
        observations.append({"service": service, "image_id": image, "nanocpus": int(cpu),
                             "memory_bytes": int(ram), "oom_killed": oom == "true",
                             "running": state == "running", "exit_code": int(rc)})
    Path(location).write_text(json.dumps(sorted(observations, key=lambda x: x["service"])))


class ObservedVerifier(Verifier):
    def __init__(self, *, observation_path, **kwargs):
        super().__init__(**kwargs)
        self.observation_path = observation_path

    async def verify(self):
        # Harbor can mount or download the verifier directory. Clear both
        # locations before accepting any file as evidence from the new grader.
        cleared = await self.environment.exec(
            shlex.join(["rm", "-f", "--", *["/logs/verifier/" + name for name in VERIFIER_OUTPUT_FILES]]),
            user="root")
        if cleared.return_code:
            raise RuntimeError("unable to reset verifier outputs")
        for name in VERIFIER_OUTPUT_FILES:
            (self.trial_paths.verifier_dir / name).unlink(missing_ok=True)
        observe_resources(self.environment, self.observation_path)
        try:
            return await super().verify()
        finally:
            with suppress(OSError, ValueError, RuntimeError, subprocess.SubprocessError):
                observe_resources(self.environment, Path(self.observation_path).with_name("resources-verifier-end.json"))


class AegisAgent(BaseAgent):
    @staticmethod
    def name():
        return "aegis-agent"

    def version(self):
        return "frozen-batch"

    async def setup(self, environment):
        # The host trial sets these fields before Harbor starts any containers.
        observe_resources(environment, self.observation_path)
        prepared = await environment.exec(
            "umask 077 && mkdir -- /tmp/aegis-benchmark && mkdir -- " + SESSIONS
        )
        if prepared.return_code:
            raise RuntimeError("private agent setup directory unavailable")
        # Compose cp preserves the source mode but may assign a different UID.
        # Give the declared task user access inside its own 0700 directory,
        # without asking for a privileged exec or mutating frozen host inputs.
        with tempfile.TemporaryDirectory(dir=self.config_file.parent) as stage:
            for source, name, mode in ((self.binary, "aegis-agent", 0o555),
                                       (self.config_file, "config.yaml", 0o444),
                                       (self.helper, "evidence", 0o555)):
                upload = Path(stage) / name
                shutil.copyfile(source, upload)
                upload.chmod(mode)
                await environment.upload_file(upload, PRIVATE + "/" + name)
                upload.unlink()

    async def run(self, instruction, environment, context):
        args = [HELPER, "--provider-socket", "/run/aegis-provider/provider.sock",
                "--sessions", SESSIONS, "--output", SUMMARY, "--",
                PRIVATE + "/aegis-agent", "exec", "--config", PRIVATE + "/config.yaml",
                "--", instruction]
        outcome = await environment.exec(shlex.join(args) + " >/dev/null 2>&1", env=self.relay_env)
        self.agent_rc = outcome.return_code

    async def collect(self, environment, attempt, host_helper):
        # Never mount an attempt directory in the task. Only a validated projection
        # can cross into the persistent result, even if the task rewrites the file.
        with suppress(OSError, ValueError, RuntimeError, subprocess.SubprocessError):
            observe_resources(environment, Path(attempt) / "resources-agent-end.json")
        if getattr(self, "agent_rc", None) is None:
            await environment.exec(
                shlex.join([HELPER, "--collect", "--sessions", SESSIONS, "--output", SUMMARY])
                + " >/dev/null 2>&1", timeout_sec=8)
        result = await environment.exec(
            f"test -f {SUMMARY} && test ! -L {SUMMARY} && head -c {MAX_SUMMARY + 1} {SUMMARY}",
            timeout_sec=8)
        raw = (result.stdout or "").encode()
        if result.return_code or len(raw) > MAX_SUMMARY:
            return False
        check = subprocess.run([host_helper, "--check"], input=raw, stdout=subprocess.PIPE,
                               stderr=subprocess.DEVNULL, timeout=8)
        if check.returncode or not 0 < len(check.stdout) <= MAX_SUMMARY:
            return False
        (Path(attempt) / "diagnostics.json").write_bytes(check.stdout)
        return True
