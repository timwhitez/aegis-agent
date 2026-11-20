import json
from pathlib import Path
import tempfile
import unittest
import uuid

import yaml

from compare import attempt_row, compare
from scheduler import asset_hashes


class CompareTest(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        self.a = self.freeze("baseline", None)
        self.b = self.freeze("candidate", 8192)

    def freeze(self, name, limit, model="fixture"):
        run = self.root / name
        assets = run / "assets"
        task = assets / "tasks" / "sample"
        task.mkdir(parents=True)
        (task / "task.toml").write_text("[environment]\n")
        (assets / "aegis-agent").write_bytes(b"static agent fixture")
        (assets / "evidence-helper").write_bytes(b"static helper fixture")
        profile = {"model": model, "output_config_effort": "max"}
        if limit:
            profile["max_output_tokens"] = limit
        (assets / "config.yaml").write_text(yaml.safe_dump({"default_provider": "fixture", "providers": {"fixture": profile}}))
        (run / "manifest.json").write_text(json.dumps({"version": 1, "run_id": uuid.uuid4().hex,
                    "tasks": {"sample": {"mode": "single"}}, "sha256": asset_hashes(assets)}))
        return run

    def test_attempts_coverage_and_missing_values(self):
        run_id = json.loads((self.a / "manifest.json").read_text())["run_id"]
        attempt = self.a / "attempts" / "sample" / uuid.uuid4().hex
        attempt.mkdir(parents=True)
        (attempt / "state.json").write_text(json.dumps({"run_id": run_id, "task": "sample",
                    "attempt_id": attempt.name, "status": "completed", "reward": 0, "elapsed_s": 3, "adapter_rc": 0, "ended_at": 4}))
        (attempt / "outcome.json").write_text(json.dumps({"agent_s": 2, "verify_s": 1, "agent_rc": 1,
            "verifier_evidence": {"case_source": None, "cases": [], "log_files_scanned": 1,
                                  "log_signals": {"command_not_found": 2, "pytest_launcher_missing": 1, "untrusted-marker": 1}}}))
        data = {"schema_version": 1, "partial": True, "started": True, "sessions": [{
            "id": "s1", "status": "failed", "tool_calls": 0, "requests": [{
                "request_id": "s1:1:main:0", "max_output_tokens": 4096, "source": "adapter_default",
                "effort": "max", "stop_reason": "max_tokens", "usage": {"output_tokens": 0},
            }]}]}
        (attempt / "diagnostics.json").write_text(json.dumps(data))
        report = compare(self.a, self.b)
        row = report["baseline"]["tasks"]["sample"]["attempts"][0]
        self.assertEqual((row["evidence"], row["reward"], row["observed_request_count"]), ("partial", 0, 1))
        self.assertEqual((row["elapsed_s"], row["agent_s"], row["verify_s"]), (3, 2, 1))
        self.assertEqual(row["verifier"], {"observed_cases": 0, "case_status_counts": {}, "case_source": None,
                                          "log_files_scanned": 1, "log_signals": {"command_not_found": 2, "pytest_launcher_missing": 1}})
        self.assertNotIn("untrusted-marker", json.dumps(row))

        self.assertEqual(row["requests"][0]["usage"]["output_tokens"], 0)
        self.assertIsNone(row["requests"][0]["usage"]["reasoning_tokens"])
        self.assertEqual(report["candidate"]["tasks"]["sample"]["status"], "missing")
        (attempt / "diagnostics.json").unlink()
        row = compare(self.a, self.b)["baseline"]["tasks"]["sample"]["attempts"][0]
        self.assertEqual(row["evidence"], "missing")
        self.assertIsNone(row["observed_request_count"])
        (attempt / "diagnostics.json").write_text('{"schema_version":1,"started":false,"category":"not_started","sessions":null}')
        row = compare(self.a, self.b)["baseline"]["tasks"]["sample"]["attempts"][0]
        self.assertIs(row["started"], False)
        self.assertEqual((row["category"], row["observed_request_count"]), ("not_started", 0))


    def test_http_attempts_are_independent_of_logical_requests(self):
        diagnostics = {"schema_version": 1, "started": True, "sessions": [
            {"id": "fixture", "requests": [{"request_id": "fixture:1:main:1"}]}]}
        (self.root / "diagnostics.json").write_text(json.dumps(diagnostics))
        outcome = self.root / "outcome.json"
        for value in ({"upstream_attempts": 3, "responses": 2}, {"upstream_attempts": 0, "responses": 0}):
            outcome.write_text(json.dumps({"provider_http": {**value, "unrelated": "ignored"}}))
            row = attempt_row(self.root, {})
            self.assertEqual((row["observed_request_count"], row["provider_http"]), (1, value))
        for invalid in ({}, {"provider_http": {"upstream_attempts": 0, "responses": 1}},
                        {"provider_http": {"upstream_attempts": True, "responses": 1}}):
            outcome.write_text(json.dumps(invalid))
            self.assertIsNone(attempt_row(self.root, {})["provider_http"])

    def test_missing_outcome_uses_durable_observed_reward(self):
        self.assertEqual(attempt_row(self.root, {"observed_reward": 0})["observed_reward"], 0)
        self.assertIsNone(attempt_row(self.root, {})["observed_reward"])

    def test_execution_layout_is_compared_for_older_manifests(self):
        # Legacy manifests lack a layout snapshot, but the files in the two
        # archives can still demonstrate a current mismatch.
        helper = self.b / "assets/evidence-helper"
        helper.chmod(helper.stat().st_mode ^ 0o100)
        with self.assertRaisesRegex(ValueError, "layout"):
            compare(self.a, self.b)
        helper.chmod(helper.stat().st_mode ^ 0o100)
        (self.b / "assets/tasks/sample/emptydir").mkdir()
        with self.assertRaisesRegex(ValueError, "layout"):
            compare(self.a, self.b)

    def test_native_reward_and_safe_deliveries_survive_unscored_verifier(self):
        state = {"status": "verifier_error", "reward": None}
        outcome = self.root / "outcome.json"
        marker = "private-payload-marker"
        digest = "a" * 64
        safe = {"declaration_index": 0, "status": "collected", "files": 1, "bytes": 24, "sha256": digest}
        outcome.write_text(json.dumps({"observed_reward": 0, "reward": None, "artifacts": [
            {**safe, "source": marker, "service": marker, "raw": marker,
             "private_copy": {"status": "encrypted", "file": marker}},
            {"declaration_index": 1, "status": "missing"},
            {"declaration_index": True, "status": marker, "files": -1, "bytes": marker,
             "sha256": marker, "private_copy": {"status": marker}},
        ]}))
        row = attempt_row(self.root, state)
        self.assertEqual(row["observed_reward"], 0)
        self.assertIsNone(row["reward"])
        self.assertEqual(row["artifacts"][0], {**safe, "private_copy_status": "encrypted"})
        self.assertEqual(row["artifacts"][1], {"declaration_index": 1, "status": "missing", "files": None,
                         "bytes": None, "sha256": None, "private_copy_status": None})
        self.assertEqual(row["artifacts"][2], {"declaration_index": None, "status": "invalid", "files": None,
                         "bytes": None, "sha256": None, "private_copy_status": None})
        self.assertNotIn(marker, json.dumps(row))
        outcome.write_text('{"reward":0.5,"artifacts":[]}')
        row = attempt_row(self.root, state)
        self.assertEqual((row["observed_reward"], row["artifacts"]), (0.5, []))
        outcome.write_text('{"observed_reward":false,"reward":1}')
        row = attempt_row(self.root, state)
        self.assertIsNone(row["observed_reward"])
        self.assertIsNone(row["artifacts"])

    def test_artifact_selection_is_a_closed_enum(self):
        for value in ("task_declared", "operator_selected", "not_configured", "private-marker", None):
            (self.root / "outcome.json").write_text(json.dumps({"artifact_selection": value}))
            row = attempt_row(self.root, {})
            expected = value if value in ("task_declared", "operator_selected", "not_configured") else None
            self.assertEqual(row["artifact_selection"], expected)
            self.assertNotIn("private-marker", json.dumps(row))

    def test_rejects_different_scheduler_controls(self):
        for run, jobs in [(self.a, 1), (self.b, 2)]:
            attempt = run / "attempts" / "sample" / uuid.uuid4().hex
            attempt.mkdir(parents=True)
            run_id = json.loads((run / "manifest.json").read_text())["run_id"]
            (attempt / "state.json").write_text(json.dumps({"status": "interrupted", "task": "sample",
                "attempt_id": attempt.name, "run_id": run_id, "jobs": jobs, "hard_timeout_s": 300,
                "cpu_budget": None, "memory_budget_mb": None}))
        with self.assertRaisesRegex(ValueError, "scheduler limits differ"):
            compare(self.a, self.b)
        path = next(self.b.glob("attempts/sample/*/state.json"))
        state = json.loads(path.read_text())
        state["jobs"] = 1
        path.write_text(json.dumps(state))
        self.assertEqual(compare(self.a, self.b)["scheduler_checks"]["sample"], "observations_match")
        del state["jobs"]
        path.write_text(json.dumps(state))
        self.assertEqual(compare(self.a, self.b)["scheduler_checks"]["sample"], "unverified")

    def test_different_images_with_same_frozen_tag_are_not_comparable(self):
        files = []
        for run in (self.a, self.b):
            attempt = run / "attempts" / "sample" / uuid.uuid4().hex
            attempt.mkdir(parents=True)
            run_id = json.loads((run / "manifest.json").read_text())["run_id"]
            (attempt / "state.json").write_text(json.dumps({"status": "interrupted", "task": "sample",
                "attempt_id": attempt.name, "run_id": run_id}))
            files.append(attempt / "resources-agent.json")
        observation = {"service": "main", "image_id": "sha256:" + "a" * 64, "nanocpus": 1_000_000_000, "memory_bytes": 128 << 20}
        files[0].write_text(json.dumps([observation]))
        self.assertEqual(compare(self.a, self.b)["runtime_checks"]["sample"]["agent"], "unverified")
        files[1].write_text(json.dumps([observation]))
        self.assertEqual(compare(self.a, self.b)["runtime_checks"]["sample"], {"agent": "observations_match", "verifier": "unverified"})
        observation["image_id"] = "sha256:" + "b" * 64
        files[1].write_text(json.dumps([observation]))
        with self.assertRaisesRegex(ValueError, "observed agent image"):
            compare(self.a, self.b)

    def test_rejects_other_changes_or_modified_assets(self):
        with self.assertRaisesRegex(ValueError, "outside max_output_tokens"):
            compare(self.a, self.freeze("other-model", 8192, model="different"))
        candidate = self.freeze("other-task", 8192)
        (candidate / "assets/tasks/sample/task.toml").write_text("different")
        with self.assertRaisesRegex(ValueError, "digest|asset"):
            compare(self.a, candidate)
        manifest = json.loads((candidate / "manifest.json").read_text())
        manifest["sha256"] = asset_hashes(candidate / "assets")
        (candidate / "manifest.json").write_text(json.dumps(manifest))
        with self.assertRaisesRegex(ValueError, "plans or frozen asset digests"):
            compare(self.a, candidate)
        with self.assertRaisesRegex(ValueError, "different configured limits"):
            compare(self.a, self.freeze("same-limit", None))


if __name__ == "__main__":
    unittest.main()
