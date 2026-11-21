"""Harbor's installed-agent interface for a frozen Linux Aegis binary."""
import asyncio
import json
import os
import stat
from contextlib import suppress
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import tempfile

from harbor.agents.base import BaseAgent
from harbor.utils.scripts import build_execution_command
from harbor.verifier.verifier import Verifier
from result_evidence import VERIFIER_OUTPUT_FILES

PRIVATE = "/tmp/aegis-benchmark"
SESSIONS = PRIVATE + "/sessions"
SUMMARY = PRIVATE + "/diagnostics.json"
HELPER = PRIVATE + "/evidence"
MAX_SUMMARY = 4 * 1024 * 1024


async def host_output(argv, *, timeout, data=None):
    child = await asyncio.create_subprocess_exec(
        *argv, stdin=asyncio.subprocess.PIPE if data is not None else asyncio.subprocess.DEVNULL,
        stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.DEVNULL)
    try:
        try:
            stdout, _ = await asyncio.wait_for(child.communicate(data), timeout)
        except TimeoutError:
            raise subprocess.TimeoutExpired(argv, timeout) from None
    finally:
        if child.returncode is None:
            with suppress(ProcessLookupError):
                child.kill()
            await child.communicate()
    if child.returncode:
        raise subprocess.CalledProcessError(child.returncode, argv)
    return stdout


async def observe_resources(environment, location):
    found = await host_output(["docker", "ps", "-aq", "--filter",
                               "label=com.docker.compose.project=" + environment.session_id], timeout=10)
    ids = found.decode().split()
    if not ids:
        raise RuntimeError("no running containers for the expected project")
    # Request only these fields; full docker inspect includes process env.
    fmt = ('{{.Image}} {{.HostConfig.NanoCpus}} {{.HostConfig.Memory}} '
           '{{index .Config.Labels "com.docker.compose.service"}} '
           '{{.State.OOMKilled}} {{.State.Status}} {{.State.ExitCode}}')
    inspected = await host_output(["docker", "inspect", "--format", fmt, *ids], timeout=10)
    lines = inspected.decode().splitlines()
    observations = []
    for line in lines:
        image, cpu, ram, service, oom, state, rc = line.split()
        if not re.fullmatch(r"sha256:[a-f0-9]{64}", image) or not re.fullmatch(r"[A-Za-z0-9_.-]{1,100}", service):
            raise ValueError("invalid Docker resource identity")
        observations.append({"service": service, "image_id": image, "nanocpus": int(cpu),
                             "memory_bytes": int(ram), "oom_killed": oom == "true",
                             "running": state == "running", "exit_code": int(rc)})
    Path(location).write_text(json.dumps(sorted(observations, key=lambda x: x["service"])))


class GraderEnvironment:
    """Capture only the exit returned by Harbor's single-step Linux test command."""
    def __init__(self, environment, exit_path):
        self.native = environment
        self.exit_path = Path(exit_path)
        self.grade_command = build_execution_command(
            "/tests/test.sh", "/logs/verifier/test-stdout.txt", task_os=environment.os)

    def __getattr__(self, name):
        return getattr(self.native, name)

    async def exec(self, command, **kwargs):
        result = await self.native.exec(command=command, **kwargs)
        if command == self.grade_command:
            self.exit_path.write_text(str(result.return_code))
        return result


class ObservedVerifier(Verifier):
    def __init__(self, *, observation_path, exit_path, environment, **kwargs):
        super().__init__(environment=GraderEnvironment(environment, exit_path), **kwargs)
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
        await observe_resources(self.environment, self.observation_path)
        try:
            return await super().verify()
        finally:
            with suppress(OSError, ValueError, RuntimeError, subprocess.SubprocessError):
                await observe_resources(self.environment, Path(self.observation_path).with_name("resources-verifier-end.json"))


class DockerArtifactOwnershipError(RuntimeError):
    """The Docker client created a private host artifact owned by another user."""


class AegisAgent(BaseAgent):
    @staticmethod
    def name():
        return "aegis-agent"

    def version(self):
        return "frozen-batch"

    async def setup(self, environment):
        # The host trial sets these fields before Harbor starts any containers.
        await observe_resources(environment, self.observation_path)
        prepared = await environment.exec(
            "umask 077 && mkdir -- /tmp/aegis-benchmark && mkdir -- " + SESSIONS
        )
        if prepared.return_code:
            raise RuntimeError("private agent setup directory unavailable")
        # Compose cp preserves the source mode but may assign a different UID.
        # Give the declared task user access inside its own 0700 directory,
        # without asking for a privileged exec or mutating frozen host inputs.
        with tempfile.TemporaryDirectory(dir=self.config_file.parent) as stage:
            if self.check_artifact_copy:
                probe = PRIVATE + "/copy-owner-probe"
                made = await environment.exec("umask 077 && : > " + probe)
                if made.return_code:
                    raise RuntimeError("unable to prepare the private artifact copy check")
                copy = Path(stage) / "copy-owner-probe"
                await environment.download_file(probe, copy)
                info = copy.lstat()
                if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid():
                    raise DockerArtifactOwnershipError("run the Docker client as the Harbor user")
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
            await observe_resources(environment, Path(attempt) / "resources-agent-end.json")
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
        checked = await host_output([host_helper, "--check"], data=raw, timeout=8)
        if not 0 < len(checked) <= MAX_SUMMARY:
            return False
        (Path(attempt) / "diagnostics.json").write_bytes(checked)
        return True
