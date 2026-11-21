import os
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import AsyncMock, patch

from aegis_harbor import AegisAgent, ObservedVerifier, Verifier, observe_resources
from harbor.models.task.config import TaskOS
from harbor.models.trial.paths import TrialPaths
from harbor.trial.errors import VerifierTimeoutError
from harbor.trial.trial import Trial


class TaskInputTest(unittest.IsolatedAsyncioTestCase):
    async def test_uploads_prompt_data_without_shell_arguments(self):
        instruction = " --json '$(echo input)' service-613.py\n\n"
        with tempfile.TemporaryDirectory() as tmp:
            agent = SimpleNamespace(config_file=Path(tmp) / "config.yaml", relay_env={})
            uploads = []

            async def upload(source, target):
                self.assertEqual(source.read_bytes(), instruction.encode())
                self.assertEqual(source.parent.stat().st_mode & 0o777, 0o700)
                uploads.append(source)

            env = SimpleNamespace(upload_file=upload, exec=AsyncMock(return_value=SimpleNamespace(return_code=0)))
            await AegisAgent.run(agent, instruction, env, None)
            self.assertEqual(agent.agent_rc, 0)
            self.assertEqual(len(uploads), 1)
            self.assertFalse(uploads[0].exists())
            command, = env.exec.await_args.args
            self.assertNotIn("service-613.py", command)
            self.assertNotIn("$(echo input)", command)


class VerifierLifecycleTest(unittest.IsolatedAsyncioTestCase):
    async def test_native_grader_exit_survives_an_independent_reward(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            paths = TrialPaths(root)
            paths.verifier_dir.mkdir(exist_ok=True)
            tests = root / "tests"
            task = SimpleNamespace(paths=SimpleNamespace(tests_dir=tests, test_path_for=lambda _: tests / "test.sh"),
                                   config=SimpleNamespace(verifier=SimpleNamespace(env={})))
            exit_path = root / "verifier-exit"
            async def execute(command, **kwargs):
                if command.startswith("(/tests/test.sh)"):
                    paths.reward_text_path.write_text("1")
                    rc = 24
                else:
                    self.assertFalse(exit_path.exists())
                    rc = 0
                return SimpleNamespace(return_code=rc)
            env = SimpleNamespace(os=TaskOS.LINUX, exec=execute, is_path_mounted=lambda *args: True)
            verifier = ObservedVerifier(task=task, trial_paths=paths, environment=env, skip_tests_upload=True,
                                        exit_path=exit_path, observation_path=root / "resources-verifier.json")
            with patch("aegis_harbor.observe_resources", new_callable=AsyncMock):
                result = await verifier.verify()
            self.assertEqual((result.rewards, exit_path.read_text()), ({"reward": 1}, "24"))

    async def test_slow_resource_observation_obeys_native_verifier_deadline(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            docker = root / "docker"
            docker.write_text("#!/bin/sh\nexec sleep 2\n")
            docker.chmod(0o755)
            env = SimpleNamespace(session_id="fixture", os=TaskOS.LINUX,
                                  exec=AsyncMock(return_value=SimpleNamespace(return_code=0)))
            verifier = ObservedVerifier(task=None, trial_paths=SimpleNamespace(verifier_dir=root),
                                        environment=env, observation_path=root / "resources-verifier.json",
                                        exit_path=root / "verifier-exit")
            async def observe_end_only(environment, location):
                if Path(location).name == "resources-verifier-end.json":
                    await observe_resources(environment, location)
            with patch.dict(os.environ, PATH=tmp + os.pathsep + os.environ["PATH"]), \
                 patch("aegis_harbor.observe_resources", side_effect=observe_end_only), \
                 patch.object(Verifier, "verify", new_callable=AsyncMock) as grade:
                with self.assertRaises(VerifierTimeoutError):
                    await Trial._verify_with_timeout(None, verifier, 0.4)
                grade.assert_awaited_once()
