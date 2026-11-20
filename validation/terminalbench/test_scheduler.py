"""Exercise the public scheduler CLI using an adapter with no external services."""

import contextlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import unittest
import uuid


HERE = Path(__file__).resolve().parent
ADAPTER = '''import argparse, json, os, pathlib, subprocess, sys, time
from result_evidence import FROZEN
assert FROZEN == 'fixture'
if sys.argv[1] == "cleanup":
    with open('cleanup-labels', 'a') as stream:
        print(sys.argv[-1], file=stream)
    sys.exit(0)
p = argparse.ArgumentParser()
for arg in ["task", "attempt", "binary", "config", "evidence-helper", "label"]:
    p.add_argument("--" + arg)
a = p.parse_args()
task, dest = pathlib.Path(a.task), pathlib.Path(a.attempt)
mode = (task / "mode").read_text()
if mode == "sleep":
    # This descendant must be gone before the retry starts.
    subprocess.Popen([sys.executable, "-c",
        "import time,pathlib,sys;time.sleep(1.4);pathlib.Path(sys.argv[1]).touch()", str(dest/"leaked")])
    (dest / "started").touch()
    time.sleep(20)
elif mode == "verifier_retry" and len(list(dest.parent.iterdir())) == 1:
    (dest / "outcome.json").write_text(json.dumps({"status":"verifier_error","stage":"verifier",
                                                   "reward":0,"observed_reward":0,"reward_valid":False}))
elif mode == "timeout_reward":
    (dest / "outcome.json").write_text(json.dumps({"status":"agent_timeout","reward":1,"reward_valid":False}))
elif mode == "partial":
    (dest / "outcome.json").write_text("{")
elif mode == "retry" and len(list(dest.parent.iterdir())) == 1:
    (dest / "outcome.json").write_text(json.dumps({"status":"infra_invalid","stage":"setup","reward":None}))
else:
    score = {"zero":0,"fraction":0.5,"invalid":"1"}.get(mode, 1)
    (dest / "outcome.json").write_text(json.dumps({"status":"completed","stage":"verifier","reward":score}))
'''


class SchedulerTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        self.src = self.base / "runner"
        self.src.mkdir()
        for name in ["scheduler.py", "task_plan.py", "relay.py", "aegis_harbor.py"]:
            shutil.copy2(HERE / name, self.src / name)
        (self.src / "trial.py").write_text(ADAPTER)
        (self.src / "result_evidence.py").write_text("FROZEN = 'fixture'\n")
        self.tasks, self.run = self.base / "tasks", self.base / "run"
        self.tasks.mkdir()
        self.head = [sys.executable, "-B", str(self.src / "scheduler.py")]

    def init(self, modes):
        for name, mode in modes.items():
            task = self.tasks / name
            task.mkdir()
            (task / "mode").write_text(mode)
            (task / "task.toml").write_text(
                '[environment]\ndocker_image="synthetic"\ncpus=1\nmemory_mb=128\n[agent]\ntimeout_sec=2\n')
        config = self.base / "config.yaml"
        config.write_text("# stripped by freeze\ndefault_provider: fixture\nproviders:\n  fixture:\n"
                          "    api_provider: anthropic\n    api_key_env: SYNTHETIC_TEST_TOKEN\n"
                          "    model: fixture\n    base_url: https://example.invalid/v1\n")
        self.cli("init", "--tasks", self.tasks, "--config", config,
                 "--binary", self.src / "trial.py", "--evidence-helper", self.src / "trial.py")

    def cli(self, cmd, *args, rc=0):
        p = subprocess.run([*self.head, cmd, "--run", self.run, *args],
                           capture_output=True, text=True, timeout=10, cwd=self.base)
        self.assertEqual(p.returncode, rc, (p.stdout, p.stderr))
        return p.stdout

    def summary(self):
        return json.loads(self.cli("summary"))

    def start_sleep(self):
        self.init({"sleep": "sleep"})
        proc = subprocess.Popen([*self.head, "run", "--run", self.run],
                                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, cwd=self.base)
        self.addCleanup(lambda: proc.poll() is None and (proc.kill(), proc.wait()))
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            markers = list(self.run.glob("attempts/sleep/*/started"))
            if markers:
                return proc, markers[0].parent
            time.sleep(0.02)
        self.fail("adapter did not start")

    def test_scores_full_manifest_and_retains_retried_attempts(self):
        self.init({"one": "retry", "two": "zero", "three": "fraction"})
        self.cli("run", "--jobs", "2", rc=1)
        first = self.summary()
        self.assertEqual((first["scored"], first["passed"], first["planned"]), (2, 0, 3))
        previous = next(self.run.glob("attempts/one/*/state.json"))
        saved = previous.read_bytes()
        unrelated = {p: p.read_bytes() for p in self.run.glob("attempts/t*/*/state.json")}
        # Editing source files after init must never change execution.
        (self.src / "trial.py").write_text("raise RuntimeError('new version')")
        (self.src / "result_evidence.py").write_text("FROZEN = 'new dependency'\n")
        (self.tasks / "one/mode").write_text("invalid")
        self.cli("run")
        summary = self.summary()
        self.assertEqual((summary["passed"], summary["scored"], summary["planned"]), (1, 3, 3))
        self.assertEqual(summary["tasks"]["three"]["reward"], 0.5)
        self.assertEqual(previous.read_bytes(), saved)
        self.assertEqual(unrelated, {p: p.read_bytes() for p in self.run.glob("attempts/t*/*/state.json")})
        self.cli("run")
        self.assertEqual(len(list(self.run.glob("attempts/*/*/state.json"))), 4)

    def test_partial_and_malformed_rewards_are_unscored_and_retryable(self):
        self.init({"partial": "partial", "bad": "invalid"})
        self.cli("run", rc=1)
        originals = {p: p.read_bytes() for p in self.run.glob("attempts/*/*/outcome.json")}
        self.assertEqual(self.summary()["status_counts"], {"infra_invalid": 2})
        self.cli("run", rc=1)
        self.assertEqual(len(list(self.run.glob("attempts/*/*/outcome.json"))), 4)
        self.assertTrue(all(p.read_bytes() == raw for p, raw in originals.items()))

    def test_explicit_reward_validity_must_be_boolean(self):
        from scheduler import read_outcome
        path = self.base / "outcome.json"
        for value in (0, None, "false", False):
            path.write_text(json.dumps({"status": "completed", "reward": 0, "reward_valid": value}))
            result = read_outcome(path, 0)
            self.assertEqual((result["status"], result["reward"], result["observed_reward"]), ("infra_invalid", None, 0))
        path.write_text(json.dumps({"status": "completed", "reward": 0, "reward_valid": True}))
        self.assertEqual(read_outcome(path, 0)["status"], "completed")
        path.write_text(json.dumps({"status": "completed", "reward": 0}))
        self.assertEqual(read_outcome(path, 0)["status"], "completed")

    def test_verifier_launch_failure_retries_and_keeps_original_zero(self):
        self.init({"one": "verifier_retry", "two": "zero"})
        self.cli("run", rc=1)
        self.assertEqual(self.summary()["scored"], 1)
        row = self.summary()["tasks"]["one"]
        self.assertEqual((row["status"], row["reward"], row["observed_reward"]), ("verifier_error", None, 0))
        original = next(self.run.glob("attempts/one/*/outcome.json"))
        before = original.read_bytes()
        self.cli("run")
        self.assertEqual(self.summary()["scored"], 2)
        self.assertEqual(original.read_bytes(), before)
        self.assertEqual(len(list(self.run.glob("attempts/two/*"))), 1)

    def test_timeout_keeps_observed_score_outside_accepted_rewards(self):
        self.init({"a": "timeout_reward"})
        self.cli("run", rc=1)
        summary = self.summary()
        self.assertEqual((summary["planned"], summary["scored"], summary["passed"]), (1, 0, 0))
        self.assertEqual((summary["tasks"]["a"]["reward"], summary["tasks"]["a"]["observed_reward"]), (None, 1))

    def test_hard_timeout_stops_accepting_and_reaps_descendants(self):
        self.init({"a": "sleep", "b": "pass"})
        self.cli("run", "--hard-timeout", ".5", rc=1)
        summary = self.summary()
        self.assertEqual(summary["status_counts"], {"hard_timeout": 1, "missing": 1})
        time.sleep(1.5)
        self.assertFalse(list(self.run.glob("attempts/a/*/leaked")))

    def test_concurrent_resume_and_explicit_cancel(self):
        proc, attempt = self.start_sleep()
        self.cli("run", rc=2)
        proc.terminate()
        self.assertEqual(proc.wait(timeout=5), 1)
        state = json.loads((attempt / "state.json").read_text())
        self.assertEqual((state["status"], state["reason"]), ("interrupted", "SIGTERM"))
        time.sleep(1.5)
        self.assertFalse((attempt / "leaked").exists())

    def test_scheduler_crash_stops_owned_processes(self):
        proc, attempt = self.start_sleep()
        os.kill(proc.pid, signal.SIGKILL)
        proc.wait(timeout=5)
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            state = json.loads((attempt / "state.json").read_text())
            if state["status"] == "interrupted":
                break
            time.sleep(0.03)
        self.assertEqual(state["reason"], "scheduler_lost")
        self.assertEqual(state["status"], "interrupted")
        time.sleep(1.5)
        self.assertFalse((attempt / "leaked").exists())

    def test_killed_watchdog_compensates_before_scheduler_returns(self):
        proc, attempt = self.start_sleep()
        state = json.loads((attempt / "state.json").read_text())
        # The adapter may have already published its native reward while still
        # completing teardown. It has not yet returned to the watchdog.
        (attempt / "outcome.json").write_text(json.dumps({"status": "completed", "reward": 0, "reward_valid": True}))
        os.kill(state["watchdog_pid"], signal.SIGKILL)
        self.assertNotEqual(proc.wait(timeout=5), 0)
        old = json.loads((attempt / "state.json").read_text())
        self.assertEqual((old["status"], old["reason"]), ("interrupted", "watchdog_lost"))
        self.assertEqual(self.summary()["status_counts"], {"interrupted": 1})
        self.assertEqual((old["reward"], old["observed_reward"], self.summary()["scored"]), (None, 0, 0))
        time.sleep(1.5)
        self.assertFalse(list(self.run.glob("attempts/sleep/*/leaked")))

    def test_resume_via_another_interpreter_reaps_abandoned_adapter(self):
        proc, attempt = self.start_sleep()
        state = json.loads((attempt / "state.json").read_text())
        # Stop the scheduler first so neither supervisor can perform cleanup.
        os.kill(proc.pid, signal.SIGSTOP)
        os.kill(state["watchdog_pid"], signal.SIGKILL)
        os.kill(proc.pid, signal.SIGKILL)
        proc.wait(timeout=5)
        other = self.base / "other-python"
        other.symlink_to(sys.executable)
        self.head[0] = str(other)
        try:
            self.cli("run", "--hard-timeout", ".35", rc=1)
            time.sleep(1.5)
            self.assertFalse((attempt / "leaked").exists())
            self.assertEqual(json.loads((attempt / "state.json").read_text())["status"], "interrupted")
        finally:
            with contextlib.suppress(ProcessLookupError):
                os.killpg(state["adapter_pid"], signal.SIGKILL)

    def test_touching_old_attempt_does_not_change_latest_selection(self):
        self.init({"a": "partial"})
        self.cli("run", rc=1)
        old = self.summary()["tasks"]["a"]["attempt_id"]
        self.cli("run", rc=1)
        new = self.summary()["tasks"]["a"]["attempt_id"]
        self.assertNotEqual(old, new)
        (self.run / "attempts/a" / old / "operator-note").touch()
        self.assertEqual(self.summary()["tasks"]["a"]["attempt_id"], new)

    def test_frozen_permissions_and_directories_are_checked_before_launch(self):
        self.init({"a": "pass"})
        helper = self.run / "assets/evidence-helper"
        original_mode = helper.stat().st_mode
        helper.chmod(original_mode ^ 0o100)
        self.cli("run", rc=2)
        helper.chmod(original_mode)
        extra = self.run / "assets/tasks/a/new-empty-directory"
        extra.mkdir()
        self.cli("run", rc=2)
        self.assertFalse(list(self.run.glob("attempts/*")))
        extra.rmdir()
        self.cli("run")

    def test_cross_run_and_partial_state_cannot_skip_a_task(self):
        self.init({"a": "pass", "b": "zero"})
        self.cli("run")
        first, second = sorted(self.run.glob("attempts/*/*/state.json"))
        # Both files look terminal by filename. One no longer has a complete
        # JSON object; the other has a record copied from a different task.
        second.write_bytes(first.read_bytes())
        first.write_text("{")
        self.assertEqual(self.summary()["scored"], 0)
        self.cli("run")
        self.assertEqual(self.summary()["scored"], 2)
        self.assertEqual(len(list(self.run.glob("attempts/*/*/state.partial-*.json"))), 2)

    def test_oversized_plan_fails_before_launch(self):
        self.init({"a": "pass"})
        self.cli("run", "--memory-mb", "127", rc=2)
        self.assertFalse(list(self.run.glob("attempts/*")))
        self.cli("run", "--cpu-budget", "1", "--memory-mb", "128")

    def test_cleanup_uses_only_run_scoped_resource_id(self):
        self.init({"a": "pass"})
        self.cli("run")
        statefile = next(self.run.glob("attempts/a/*/state.json"))
        state = json.loads(statefile.read_text())
        foreign = uuid.uuid4().hex
        state.update(status="running", resource_id=foreign)
        statefile.write_text(json.dumps(state))
        self.cli("run")
        labels = (self.base / "cleanup-labels").read_text().splitlines()
        expected = uuid.uuid5(uuid.UUID(state["run_id"]), "a/" + statefile.parent.name).hex
        self.assertEqual(labels, [expected])
        self.assertNotIn(foreign, labels)

    def test_task_symlink_escape_fails_before_freeze(self):
        self.init({"a": "pass"})
        original = self.run
        self.run = self.base / "second"
        (self.tasks / "a/external").symlink_to(self.base / "config.yaml")
        self.cli("init", "--tasks", self.tasks, "--config", self.base / "config.yaml",
                 "--binary", self.src / "trial.py", "--evidence-helper", self.src / "trial.py", rc=2)
        self.assertFalse(self.run.exists())
        self.assertTrue((original / "manifest.json").is_file())

    def test_frozen_tamper_fails_before_launch(self):
        self.init({"one": "pass"})
        frozen = self.run / "assets/tasks/one/mode"
        frozen.chmod(0o600)
        frozen.write_text("different")
        self.cli("run", rc=2)
        self.assertFalse(list(self.run.glob("attempts/*")))

    def test_runner_tree_checked_before_frozen_python_starts(self):
        self.init({"a": "pass"})
        # A partial restore left a truncated Python module alongside the runner.
        (self.run / "assets/runner/json.py").write_text("def decode(")
        p = subprocess.run([*self.head, "run", "--run", self.run], cwd=self.base,
                           timeout=5, text=True, capture_output=True)
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertIn("frozen runner asset digest mismatch", p.stderr)
        self.assertNotIn("SyntaxError", p.stderr)
        self.assertFalse(list(self.run.glob("attempts/*")))

    def test_conflicting_backup_identity_cannot_contribute_native_reward(self):
        self.init({"a": "pass", "b": "zero"})
        self.cli("run")
        a, b = sorted(self.run.glob("attempts/*/*"))
        shutil.copytree(a, b.parent / a.name)
        self.cli("run")
        recovered = json.loads((b.parent / a.name / "state.json").read_text())
        self.assertEqual(recovered["status"], "interrupted")
        self.assertIsNone(recovered.get("observed_reward"))
        self.assertEqual(self.summary()["tasks"]["b"]["reward"], 0)

    def test_restored_completions_respect_explicit_native_evidence(self):
        names = ("bad-rc", "legacy", "mismatched", "timeout", "unaccepted")
        self.init({name: "pass" for name in names})
        self.cli("run", "--jobs", "3")
        outcomes = {"legacy": {"reward": 1}, "mismatched": {"reward": 0},
                    "timeout": {"status": "verifier_timeout", "reward": 1},
                    "unaccepted": {"reward_valid": False, "reward": 1}}
        for name, outcome in outcomes.items():
            next(self.run.glob(f"attempts/{name}/*/outcome.json")).write_text(json.dumps(outcome))
        state_path = next(self.run.glob("attempts/bad-rc/*/state.json"))
        state = json.loads(state_path.read_text())
        state["adapter_rc"] = False
        state_path.write_text(json.dumps(state))
        summary = self.summary()
        self.assertEqual(summary["scored"], 1)
        self.assertEqual(summary["tasks"]["legacy"]["reward"], 1)
        self.assertEqual(summary["tasks"]["mismatched"]["observed_reward"], 0)
        self.cli("run", "--jobs", "3")
        self.assertEqual(self.summary()["scored"], len(names))
        self.assertEqual(len(list(self.run.glob("attempts/*/*/state.json"))), 2 * len(names) - 1)


if __name__ == "__main__":
    unittest.main()
