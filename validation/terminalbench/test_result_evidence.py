import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from result_evidence import MAX_DELIVERY, retain_results


class ResultEvidenceTest(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        self.attempt = root / "attempt"
        self.trial = root / "trial"
        self.verify = self.trial / "verifier"
        self.artifacts = self.trial / "artifacts"
        for p in [self.verify, self.artifacts, self.attempt]:
            p.mkdir(parents=True)
        self.paths = SimpleNamespace(trial_dir=self.trial, verifier_dir=self.verify, artifacts_dir=self.artifacts)
        self.item = {"source": "/submission", "service": "main", "status": "ok", "destination": "artifacts/value"}
        self.plan = {"artifacts": ["/submission"]}
        self.tmp_env = patch.dict(os.environ, {"AEGIS_BENCH_ARTIFACT_RECIPIENT_CERT": ""})
        self.tmp_env.start()
        self.addCleanup(self.tmp_env.stop)

    def collect(self):
        (self.artifacts / "manifest.json").write_text(json.dumps([self.item]))
        return retain_results(self.paths, self.attempt, self.plan)

    def test_known_case_outcomes_without_verbatim_names_or_messages(self):
        private = "opaque-test-control-canary"
        self.verify.joinpath("ctrf.json").write_text(json.dumps({"results":{"tests":[
            {"name": private, "status": "failed", "message": private},
            {"name": "ordinary", "status": private}, {"name":"last", "status":"passed"}]}}))
        self.verify.joinpath("test-stderr.txt").write_text(private)
        self.artifacts.joinpath("value").write_text(private)
        got = self.collect()
        self.assertNotIn(private, json.dumps(got))
        self.assertEqual([s["status"] for s in got["verifier_evidence"]["cases"]], ["failed", "other", "passed"])
        self.assertEqual(got["verifier_evidence"]["files"]["reward.txt"], {"status":"missing"})
        self.assertEqual(got["artifacts"][0]["bytes"], len(private))
        self.assertEqual(got["artifacts"][0]["private_copy"], {"status":"disabled"})
        self.assertEqual(list(self.attempt.iterdir()), [])

    def test_pytest_fallback_including_deduplication(self):
        self.verify.joinpath("test-stdout.txt").write_text(
            "t.py::test_red FAILED [ 50%]\nt.py::test_green PASSED [100%]\nFAILED t.py::test_red - secret assertion\n")
        evidence = self.collect()["verifier_evidence"]
        self.assertEqual((evidence["case_source"],evidence["status"]), ("pytest","observed"))
        self.assertEqual([(s["case_sha256"], s["status"]) for s in evidence["cases"]],
            [(hashlib.sha256(b"t.py::test_red").hexdigest(),"failed"),
             (hashlib.sha256(b"t.py::test_green").hexdigest(),"passed")])

    def test_log_signals_without_case_records_or_private_text(self):
        private = "synthetic-credential-marker"
        (self.verify / "test-stdout.txt").write_text(
            f"/tests/test.sh: line 24: {private}: command not found\n"
            f"sh: 3: {private}: not found\n"
            f"curl: (6) Could not resolve host: {private}\n"
            f"error: Failed to download https://{private}\n"
            f"curl: (60) SSL certificate problem: {private}\n"
            f"____________ ERROR collecting {private} ____________\n"
            f"ordinary pytest assertion about command not found, unrelated to execution\n")
        (self.verify / "reward.txt").write_text("0")
        got = self.collect()["verifier_evidence"]
        self.assertEqual((got["case_source"], got["cases"], got["log_files_scanned"]), (None, [], 1))
        self.assertEqual(got["log_signals"], {"command_not_found": 2, "dns_failure": 1,
                         "download_failure": 1, "tls_failure": 1, "pytest_collection_error": 1})
        self.assertNotIn(private, json.dumps(got))
        (self.verify / "ctrf.json").write_text('{"results":{"tests":[{"name":"sample","status":"passed"}]}}')
        self.assertEqual(self.collect()["verifier_evidence"]["log_signals"], got["log_signals"])
        (self.verify / "test-stdout.txt").unlink()
        got = self.collect()["verifier_evidence"]
        self.assertEqual(got["log_files_scanned"], 0)
        self.assertIsNone(got["log_signals"])

    def test_rejects_symlinks_hardlinks_special_files_and_oversize(self):
        target = self.artifacts / "value"
        private = self.attempt / "host-only"; private.write_text("private")
        target.symlink_to(private)
        self.assertEqual(self.collect()["artifacts"][0]["status"], "invalid")
        target.unlink(); os.link(private,target)
        self.assertEqual(self.collect()["artifacts"][0]["status"], "invalid")
        target.unlink(); os.mkfifo(target)
        self.assertEqual(self.collect()["artifacts"][0]["status"], "invalid")
        target.unlink()
        with target.open("wb") as f:
            f.truncate(MAX_DELIVERY+1)
        self.assertEqual(self.collect()["artifacts"][0]["status"], "limit")
        self.item["destination"] = "artifacts/../verifier/reward.txt"
        self.assertEqual(self.collect()["artifacts"][0]["status"], "invalid")
        (self.artifacts/"link").symlink_to(self.attempt,target_is_directory=True)
        self.item["destination"] = "artifacts/link/host-only"
        self.assertEqual(self.collect()["artifacts"][0]["status"], "invalid")

    def test_private_delivery_requires_cert_and_roundtrips_encrypted_bytes(self):
        key, cert = self.trial.parent / "key", self.trial.parent / "cert"
        subprocess.run(["openssl","req","-newkey","rsa:2048","-nodes","-x509","-days","1","-subj","/CN=offline",
                        "-keyout",str(key),"-out",str(cert)],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        private = b"private content canary"
        (self.artifacts / "value").mkdir()
        (self.artifacts / "value" / "name-canary").write_bytes(private)
        with patch.dict(os.environ, {"AEGIS_BENCH_ARTIFACT_RECIPIENT_CERT": str(cert)}):
            record = self.collect()["artifacts"][0]
        ref = record["private_copy"]
        self.assertEqual(ref["status"], "encrypted")
        encrypted = self.attempt / ref["file"]
        self.assertEqual(encrypted.stat().st_mode & 0o777,0o600)
        self.assertNotIn(private, encrypted.read_bytes())
        self.assertNotIn("name-canary",json.dumps(record))
        raw = subprocess.check_output(["openssl","cms","-decrypt","-binary","-inform","DER","-in",str(encrypted),
                                      "-recip",str(cert),"-inkey",str(key)])
        self.assertEqual(hashlib.sha256(raw).hexdigest(),ref["archive_sha256"])
        with tarfile.open(fileobj=io.BytesIO(raw)) as archive:
            self.assertEqual(archive.extractfile("delivery/name-canary").read(),private)
        encrypted.unlink()
        with patch.dict(os.environ, {"AEGIS_BENCH_ARTIFACT_RECIPIENT_CERT": str(cert)+".missing"}):
            self.assertEqual(self.collect()["artifacts"][0]["private_copy"]["status"], "encryption_failed")
        self.assertEqual(list(self.attempt.iterdir()), [])


if __name__ == "__main__":
    unittest.main()
