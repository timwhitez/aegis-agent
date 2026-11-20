"""Compare two existing Terminal-Bench batches whose sole configuration change is max_output_tokens."""

import argparse
import json
from pathlib import Path
import re

import yaml

from result_evidence import CASE_STATUS, LOG_SIGNALS, MAX_DELIVERY, MAX_ENTRIES
from scheduler import manifest_for, read, scored, stored_attempts, summarize, valid_reward, validate_assets


def inputs(run):
    manifest = manifest_for(run)
    hashes, layout = validate_assets(run / "assets", manifest)
    if not {"aegis-agent", "config.yaml", "evidence-helper"} <= hashes.keys():
        raise ValueError("missing frozen binary, configuration or evidence helper")
    del hashes["config.yaml"]
    config = yaml.safe_load((run / "assets/config.yaml").read_text())
    profile = config["providers"][config["default_provider"]]
    limit = profile.pop("max_output_tokens", None)
    if limit is not None and (type(limit) is not int or limit <= 0):
        raise ValueError("an explicit output limit must be a positive integer")
    return manifest, hashes, layout, config, limit


def resources(attempt, phase):
    services = {}
    for suffix in ("", "-end"):
        path = attempt / f"resources-{phase}{suffix}.json"
        try:
            if path.is_symlink() or path.stat().st_size > 256 * 1024:
                raise ValueError("invalid runtime observations")
            rows = json.loads(path.read_bytes())
        except FileNotFoundError:
            continue
        if not isinstance(rows, list):
            raise ValueError("invalid runtime observations")
        for row in rows:
            service = row.get("service")
            if not isinstance(service, str) or not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}", service):
                raise ValueError("invalid runtime service ID")
            image = row.get("image_id")
            if not isinstance(image, str) or not re.fullmatch("sha256:[0-9a-f]{64}", image):
                raise ValueError("invalid observed image digest")
            limits = {k: row.get(k) for k in ("nanocpus", "memory_bytes")}
            if any(type(v) is not int or v < 0 for v in limits.values()):
                raise ValueError("invalid observed resource limit")
            observation = {"image_id": image, **limits}
            if services.setdefault(service, observation) != observation:
                raise ValueError("runtime image or limits changed during an attempt")
    return services or None


def delivery_receipts(artifacts):
    if not isinstance(artifacts, list):
        return None
    receipts = []
    for item in artifacts:
        row = item if isinstance(item, dict) else {}
        index, files, size = (row.get(k) for k in ("declaration_index", "files", "bytes"))
        status = row.get("status")
        if status not in ("collected", "missing", "invalid", "unavailable", "ambiguous", "limit"):
            status = "invalid"
        digest = row.get("sha256")
        copy = row.get("private_copy")
        copy_status = copy.get("status") if isinstance(copy, dict) else None
        receipts.append({
            "declaration_index": index if type(index) is int and index >= 0 else None,
            "status": status,
            "files": files if type(files) is int and 0 <= files <= MAX_ENTRIES else None,
            "bytes": size if type(size) is int and 0 <= size <= MAX_DELIVERY else None,
            "sha256": digest if isinstance(digest, str) and re.fullmatch("[a-f0-9]{64}", digest) else None,
            "private_copy_status": copy_status if copy_status in ("disabled", "encrypted", "encryption_failed") else None,
        })
    return receipts


def attempt_row(attempt, state):
    outcome = read(attempt / "outcome.json")
    observed_reward = outcome.get("observed_reward", outcome.get("reward", state.get("observed_reward")))
    http = outcome.get("provider_http")
    attempts = http.get("upstream_attempts") if isinstance(http, dict) else None
    responses = http.get("responses") if isinstance(http, dict) else None
    provider_http = ({"upstream_attempts": attempts, "responses": responses}
                     if type(attempts) is int and type(responses) is int and 0 <= responses <= attempts else None)
    selection = outcome.get("artifact_selection")
    if selection not in ("task_declared", "operator_selected", "not_configured"):
        selection = None
    evidence = read(attempt / "diagnostics.json")
    present = (evidence.get("schema_version") == 1 and isinstance(evidence.get("started"), bool)
               and "sessions" in evidence and (evidence["sessions"] is None or isinstance(evidence["sessions"], list)))
    requests = []
    sessions = []
    for session in (evidence["sessions"] or []) if present else ():
        observations = session.get("requests") or []
        for q in observations:
            usage = q.get("usage") or {}
            requests.append({
                "session_id": session["id"], "request_id": q["request_id"],
                "max_output_tokens": q.get("max_output_tokens"), "source": q.get("source"),
                "effort": q.get("effort"), "stop_reason": q.get("stop_reason"),
                "usage": {k: usage.get(k) for k in ("input_tokens", "output_tokens", "reasoning_tokens",
                                                    "cache_creation_input_tokens", "cache_read_input_tokens")},
            })
        sessions.append({"id": session["id"], "status": session.get("status"), "child": session.get("child"),
                         "tool_calls": session.get("tool_calls"),
                         "provider_attempts": [{k: a.get(k) for k in ("turn", "attempt", "http_status",
                                                                      "retryable", "outcome")}
                                               for a in session.get("attempts") or []]})
    ids = [q["request_id"] for q in requests]
    if len(set(ids)) != len(ids):
        raise ValueError("duplicate observed request ID")
    verifier = outcome.get("verifier_evidence") or {}
    cases = verifier.get("cases")
    case_counts = None
    if isinstance(cases, list):
        case_counts = {}
        for case in cases:
            label = case.get("status")
            label = label if isinstance(label, str) and label in CASE_STATUS else "other"
            case_counts[label] = case_counts.get(label, 0) + 1
    scanned = verifier.get("log_files_scanned")
    scanned = scanned if type(scanned) is int and 0 <= scanned <= 2 else None
    source = verifier.get("case_source")
    source = source if source in ("ctrf", "pytest") else None
    signals = verifier.get("log_signals")
    if isinstance(signals, dict) and scanned:
        signals = {k: signals[k] for k in (*LOG_SIGNALS, "pytest_launcher_missing") if type(signals.get(k)) is int and signals[k] > 0}
    else:
        signals = None
    controls = ("hard_timeout_s", "jobs", "cpu_budget", "memory_budget_mb")
    return {
        "resources": {p: resources(attempt, p) for p in ("agent", "verifier")},
        "scheduler": {k: state[k] for k in controls} if all(k in state for k in controls) else None,
        "attempt_id": attempt.name, "status": state.get("status"),
        "reward": state["reward"] if scored(state) else None,
        "observed_reward": observed_reward if valid_reward(observed_reward) else None,
        "artifact_selection": selection,
        "artifacts": delivery_receipts(outcome.get("artifacts")),
        "verifier": {"observed_cases": len(cases) if isinstance(cases, list) else None,
                     "case_status_counts": case_counts, "case_source": source,
                     "log_files_scanned": scanned, "log_signals": signals},
        "elapsed_s": state.get("elapsed_s"), "agent_s": outcome.get("agent_s"),
        "verify_s": outcome.get("verify_s"), "agent_rc": outcome.get("agent_rc"),
        "evidence": ("partial" if evidence.get("partial") else "available") if present else "missing",
        "started": evidence.get("started") if present else None,
        "category": evidence.get("category") if present else None,
        "agent_exit_code": evidence.get("exit_code") if present else None,
        "observed_request_count": len(requests) if present else None, "provider_http": provider_http,
        "with_stop_reason": sum(q["stop_reason"] is not None for q in requests) if present else None,
        "with_output_usage": sum(q["usage"]["output_tokens"] is not None for q in requests) if present else None,
        "sessions": sessions if present else None, "requests": requests if present else None,
    }


def run_results(run, manifest, limit):
    summary = summarize(run, manifest)
    tasks = {}
    for name, selected in summary["tasks"].items():
        if name != Path(name).name or name in {"", ".", ".."}:
            raise ValueError("invalid task name in manifest")
        attempts = [attempt_row(path, state) for path, state in stored_attempts(run, manifest["run_id"], name)]
        tasks[name] = {"selected_attempt": selected["attempt_id"], "status": selected["status"], "attempts": attempts}
    return {"run_id": manifest["run_id"], "configured_max_output_tokens": limit,
            "planned": summary["planned"], "scored": summary["scored"], "passed": summary["passed"],
            "reward_coverage": summary["reward_coverage"], "tasks": tasks}


def compare(baseline, candidate):
    a, hashes, layout, config, limit_a = inputs(baseline)
    b, other_hashes, other_layout, other_config, limit_b = inputs(candidate)
    if a["tasks"] != b["tasks"] or hashes != other_hashes or layout != other_layout:
        raise ValueError("task plans or frozen asset digests or layout differ")
    if yaml.safe_dump(config, sort_keys=True) != yaml.safe_dump(other_config, sort_keys=True):
        raise ValueError("configuration differs outside max_output_tokens")
    if a["run_id"] == b["run_id"] or limit_a == limit_b:
        raise ValueError("expected distinct runs with different configured limits")
    results_a = run_results(baseline, a, limit_a)
    results_b = run_results(candidate, b, limit_b)
    runtime_checks = {}
    scheduler_checks = {}
    for name in a["tasks"]:
        left = results_a["tasks"][name]["attempts"]
        right = results_b["tasks"][name]["attempts"]
        controls = {json.dumps(row["scheduler"], sort_keys=True) for row in left + right if row["scheduler"] is not None}
        if len(controls) > 1:
            raise ValueError(f"scheduler limits differ for task {name}")
        scheduler_checks[name] = ("observations_match" if left and right
                                  and all(row["scheduler"] is not None for row in left + right) else "unverified")
        runtime_checks[name] = {}
        for phase in ("agent", "verifier"):
            snapshots = [row["resources"][phase] or {} for row in left + right]
            seen = {}
            for snapshot in snapshots:
                for service, observation in snapshot.items():
                    if seen.setdefault(service, observation) != observation:
                        raise ValueError(f"observed {phase} image or resource limits differ for task {name}")
            complete = left and right and "main" in seen and all(snapshot.keys() == seen.keys() for snapshot in snapshots)
            runtime_checks[name][phase] = "observations_match" if complete else "unverified"
    profile = config["providers"][config["default_provider"]]
    return {"schema_version": 1, "agent_sha256": hashes["aegis-agent"],
            "model": profile["model"], "effort": profile.get("output_config_effort"),
            "runtime_checks": runtime_checks, "scheduler_checks": scheduler_checks, "baseline": results_a, "candidate": results_b}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("baseline", type=Path)
    parser.add_argument("candidate", type=Path)
    args = parser.parse_args()
    try:
        print(json.dumps(compare(args.baseline, args.candidate), indent=2, allow_nan=False))
    except (KeyError, OSError, TypeError, ValueError, yaml.YAMLError) as exc:
        parser.exit(2, f"comparison failed: {exc}\n")


if __name__ == "__main__":
    main()
