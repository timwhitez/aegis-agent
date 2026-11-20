"""Freeze and run one Terminal-Bench manifest (POSIX, Python 3.11+)."""

import argparse
import collections
import contextlib
import fcntl
import hashlib
import json
import math
import os
from pathlib import Path
import select
import shutil
import signal
import stat
import subprocess
import sys
import tempfile
import time
import uuid

sys.dont_write_bytecode = True
HERE = Path(__file__).resolve().parent
FINAL = {"completed", "interrupted", "infra_invalid", "agent_timeout", "verifier_timeout", "verifier_error", "hard_timeout"}


def save(path, record):
    with tempfile.NamedTemporaryFile(mode="w", dir=path.parent, delete=False) as out:
        try:
            json.dump(record, out, allow_nan=False, sort_keys=True)
            out.write("\n")
            out.flush()
            os.fsync(out.fileno())
            os.replace(out.name, path)
        finally:
            Path(out.name).unlink(missing_ok=True)
    fd = os.open(path.parent, os.O_RDONLY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def read(path):
    try:
        record = json.loads(path.read_text())
        return record if isinstance(record, dict) else {}
    except (FileNotFoundError, UnicodeError, ValueError):
        return {}


def digest(path):
    with path.open("rb") as src:
        return hashlib.file_digest(src, "sha256").hexdigest()


def asset_hashes(assets):
    hashes = {}
    for p in sorted(assets.rglob("*")):
        if p.is_symlink():
            value = "link:" + hashlib.sha256(os.fsencode(os.readlink(p))).hexdigest()
        elif p.is_file():
            value = digest(p)
        else:
            continue
        hashes[p.relative_to(assets).as_posix()] = value
    return hashes


def freeze(args):
    from task_plan import read_plan
    from relay import validate_config
    import yaml

    root = args.tasks.resolve(strict=True)
    if root in args.run.resolve().parents or args.run.resolve() == root:
        raise ValueError("run directory must be outside the source tasks")
    available = {p.name: p for p in root.iterdir() if (p / "task.toml").is_file()}
    names = sorted(set(args.only or available))
    if not names or set(names) - available.keys():
        raise ValueError("select at least one existing task directory")
    captures = collections.defaultdict(list)
    for item in args.capture:
        name, sep, path = item.partition("=")
        if not sep or name not in names:
            raise ValueError("--capture must name a selected task using TASK=/absolute/path")
        captures[name].append(path)
    config = validate_config(args.config)
    args.run.mkdir(mode=0o700, parents=True, exist_ok=False)
    try:
        assets = args.run / "assets"
        runner = assets / "runner"
        runner.mkdir(parents=True)
        for filename in ("scheduler.py", "trial.py", "task_plan.py", "relay.py", "aegis_harbor.py", "result_evidence.py"):
            shutil.copy(HERE / filename, runner / filename)
        (assets / "config.yaml").write_text(yaml.safe_dump(config, sort_keys=False))
        for name, src in [("aegis-agent", args.binary),
                          ("evidence-helper", args.evidence_helper)]:
            shutil.copy(src, assets / name)
        tasks = {}
        for name in names:
            source = available[name]
            if source.is_symlink():
                raise ValueError(f"task directory is a symlink: {name}")
            target = assets / "tasks" / name
            shutil.copytree(source, target, symlinks=True, copy_function=shutil.copy)
            # Validate the captured tree, not a source file that can be updated
            # between preflight and copy. No frozen path may escape this task.
            for p in target.rglob("*"):
                if p.is_symlink() and (Path(os.readlink(p)).is_absolute()
                                       or not p.resolve(strict=True).is_relative_to(target.resolve())):
                    raise ValueError(f"external link in task {name}: {p.relative_to(target)}")
            tasks[name] = read_plan(target, captures[name])
            if captures[name]:
                selections = assets / "captures"
                selections.mkdir(exist_ok=True)
                save(selections / (name + ".json"), tasks[name]["capture_paths"])
        for p in assets.rglob("*"):
            if not p.is_symlink() and p.is_file():
                p.chmod(stat.S_IMODE(p.stat().st_mode) & ~0o222)
        manifest = {"version": 1, "run_id": uuid.uuid4().hex, "created_at": time.time(),
                    "tasks": tasks, "sha256": asset_hashes(assets)}
        (args.run / "attempts").mkdir()
        save(args.run / "manifest.json", manifest)
        (args.run / "manifest.json").chmod(0o400)
        print(f"Frozen {len(tasks)} tasks in {args.run}")
    except Exception:
        shutil.rmtree(args.run)
        raise


def manifest_for(run):
    m = read(run / "manifest.json")
    if m.get("version") != 1 or not m.get("tasks") or not m.get("sha256"):
        raise ValueError("missing or incomplete run manifest")
    if (not isinstance(m["tasks"], dict)
            or any(Path(n).name != n or n in {"", ".", ".."} for n in m["tasks"])
            or not isinstance(m.get("run_id"), str)
            or uuid.UUID(hex=m["run_id"]).hex != m["run_id"]):
        raise ValueError("invalid run or task identifier in manifest")
    return m


def resource_id(run_id, attempt):
    # Docker cleanup authority belongs to the run and task, even when someone
    # copies another attempt directory or forges fields in an outcome.
    return uuid.uuid5(uuid.UUID(hex=run_id), attempt.parent.name + "/" + attempt.name).hex


def valid_reward(value):
    return type(value) is int or (type(value) is float and math.isfinite(value))


def stored_attempts(run, run_id, name):
    task = run / "attempts" / name
    if task.is_symlink() or task.parent.is_symlink():
        raise ValueError(f"symlink in attempt path for {name}")
    paths = sorted(task.glob("*"),
                   key=lambda p: (p.stat().st_mtime_ns, p.name))
    for attempt in paths:
        if attempt.is_symlink() or not attempt.is_dir() or uuid.UUID(hex=attempt.name).hex != attempt.name:
            raise ValueError(f"invalid attempt directory for {name}")
        state = read(attempt / "state.json")
        identity = {"run_id": run_id, "task": name, "attempt_id": attempt.name}
        if not isinstance(state.get("status"), str) or any(state.get(k) != v for k, v in identity.items()):
            state = dict(identity, status="incomplete", reason="invalid_attempt_record", reward=None)
        if state.get("status") == "completed" and not scored(state):
            state = dict(identity, status="incomplete", reason="invalid_completion", reward=None)
        yield attempt, state


def scored(record):
    return (record.get("status") == "completed" and record.get("adapter_rc") == 0
            and valid_reward(record.get("reward")) and valid_reward(record.get("ended_at")))


def summarize(run, manifest):
    counts = collections.Counter()
    rows = {}
    rewards = []
    for name in manifest["tasks"]:
        attempts = [r for _, r in stored_attempts(run, manifest["run_id"], name)]
        completed = [r for r in attempts if scored(r)]
        latest = completed[-1] if completed else (attempts[-1] if attempts else {})
        status = latest.get("status", "missing")
        counts[status] += 1
        rows[name] = {"attempt_id": latest.get("attempt_id"), "status": status,
                      "reason": latest.get("reason"), "reward": latest.get("reward") if completed else None,
                      "observed_reward": latest.get("observed_reward")}
        if completed:
            rewards.append(latest["reward"])
    total = len(rows)
    passed = sum(value == 1 for value in rewards)
    return {"run_id": manifest["run_id"], "planned": total, "scored": len(rewards),
            "passed": passed, "status_counts": dict(counts), "reward_coverage": len(rewards) / total,
            "conditional_pass_rate": passed / len(rewards) if rewards else None,
            "manifest_pass_rate": passed / total, "tasks": rows}


def group_stop(child):
    # Give the adapter time to stop Harbor and collect the bounded diagnostics.
    # Kill any surviving clients/helpers in this isolated session afterwards.
    child.terminate()
    try:
        child.wait(timeout=20)
    except subprocess.TimeoutExpired:
        pass
    with contextlib.suppress(ProcessLookupError):
        os.killpg(child.pid, signal.SIGKILL)
    child.wait()


def adapter_argv(assets, attempt, label):
    argv = [sys.executable, "-B", str(assets / "runner" / "trial.py"),
            "--task", str(assets / "tasks" / attempt.parent.name), "--attempt", str(attempt),
            "--binary", str(assets / "aegis-agent"), "--config", str(assets / "config.yaml"),
            "--evidence-helper", str(assets / "evidence-helper"), "--label", label]
    selections = assets / "captures" / (attempt.parent.name + ".json")
    if selections.is_file():
        argv.extend(["--capture-plan", str(selections)])
    return argv


def orphan_clients(assets, attempt, label):
    # A watchdog itself may be killed. Match the exact frozen executable,
    # private attempt path and UUID before touching a surviving group leader.
    output = subprocess.check_output(["ps", "-axww", "-o", "pid=,pgid=,command="], text=True)
    expected = " ".join(adapter_argv(assets, attempt, label))
    for row in output.splitlines():
        columns = row.split(None, 2)
        if len(columns) != 3:
            continue
        pid, group, argv = columns
        if pid == group and argv == expected:
            yield int(pid)


def recover_resources(assets, attempt):
    label = resource_id(manifest_for(assets.parent)["run_id"], attempt)
    try:
        groups = list(orphan_clients(assets, attempt, label))
        for pid in groups:
            with contextlib.suppress(ProcessLookupError):
                os.kill(pid, signal.SIGTERM)
        until = time.monotonic() + 20
        while groups and time.monotonic() < until and set(groups).intersection(orphan_clients(assets, attempt, label)):
            time.sleep(0.2)
        for pid in groups:
            with contextlib.suppress(ProcessLookupError):
                os.killpg(pid, signal.SIGKILL)
        with subprocess.Popen([sys.executable, "-B", str(assets / "runner" / "trial.py"),
                               "cleanup", "--label", label],
                              stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                              stderr=subprocess.DEVNULL, start_new_session=True) as proc:
            try:
                return proc.wait(timeout=120) == 0
            except subprocess.TimeoutExpired:
                group_stop(proc)
                return False
    except (OSError, subprocess.SubprocessError):
        return False


def compensate(run, attempt, state):
    # Durably record why scoring stopped before removing a container. A failed
    # cleanup stays pending and blocks a second attempt for the same run.
    state["status"] = "cleanup_pending"
    save(attempt / "state.json", state)
    if not recover_resources(run / "assets", attempt):
        return False
    state["status"] = state.pop("pending_status", "interrupted")
    state["ended_at"] = time.time()
    save(attempt / "state.json", state)
    return True


def read_outcome(path, rc):
    result = read(path)
    status = result.get("status")
    raw_reward = result.get("observed_reward", result.get("reward"))
    observed = raw_reward if valid_reward(raw_reward) else None
    if (not isinstance(status, str) or status not in FINAL or
            (status == "completed" and (rc != 0 or result.get("reward_valid") is False
                                        or not valid_reward(result.get("reward"))))):
        return {"status": "infra_invalid", "reward": None, "observed_reward": observed,
                "reason": "invalid_or_missing_outcome"}
    return {"status": status, "reward": result.get("reward") if status == "completed" else None,
            "observed_reward": observed, "reason": result.get("stage", status)}


def run_worker(args):
    attempt = args.attempt
    run = attempt.parents[2]
    assets = run / "assets"
    state = read(attempt / "state.json")
    label = resource_id(manifest_for(run)["run_id"], attempt)
    started = time.monotonic()
    argv = adapter_argv(assets, attempt, label)
    # Adapter owns redacted per-stage evidence. Never archive raw stdout/stderr.
    with subprocess.Popen(argv, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                          stderr=subprocess.DEVNULL, start_new_session=True) as child:
        state.update(resource_id=label, watchdog_pid=os.getpid(), adapter_pid=child.pid)
        save(attempt / "state.json", state)
        reason = None
        while True:
            remaining = args.deadline - (time.monotonic() - started)
            readable, _, _ = select.select([sys.stdin], [], [], max(0, min(remaining, 0.15)))
            if readable:
                reason = sys.stdin.buffer.readline(128).decode().strip() or "scheduler_lost"
            elif remaining <= 0:
                reason = "hard_timeout"
            if reason:
                state.update(reason=reason, pending_status="hard_timeout" if reason == "hard_timeout" else "interrupted",
                             status="cleanup_pending", reward=None)
                save(attempt / "state.json", state)
                group_stop(child)
                break
            if child.poll() is not None:
                group_stop(child)
                break
    state.update(adapter_rc=child.returncode, elapsed_s=time.monotonic() - started)
    result = read_outcome(attempt / "outcome.json", child.returncode)
    if reason:
        state["observed_reward"] = result.get("observed_reward")
        if not compensate(run, attempt, state):
            return 2
        return 3 if reason == "hard_timeout" else 1
    state.update(result)
    if result["status"] == "infra_invalid" or child.returncode != 0:
        state["pending_status"] = result["status"]
        return 0 if compensate(run, attempt, state) else 2
    state["ended_at"] = time.time()
    save(attempt / "state.json", state)
    return 0


def schedule(args, manifest):
    run = args.run.resolve()
    assets = run / "assets"
    if asset_hashes(assets) != manifest["sha256"]:
        raise ValueError("frozen asset digest mismatch")
    # Keep the same flock in each watchdog, never in the adapter. If the
    # scheduler dies the pipe closes; watchers clean up before unlocking.
    with (run / ".lock").open("a+") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise ValueError("run already active")
        queue = []
        for name in manifest["tasks"]:
            completed = False
            for attempt, state in stored_attempts(run, manifest["run_id"], name):
                if state.get("status") not in FINAL:
                    raw = attempt / "state.json"
                    if state.get("status") == "incomplete" and raw.is_file():
                        raw.rename(attempt / ("state.partial-" + uuid.uuid4().hex + ".json"))
                    state.setdefault("reason", "scheduler_lost")
                    state.setdefault("attempt_id", attempt.name)
                    state.setdefault("pending_status", "interrupted")
                    if not compensate(run, attempt, state):
                        raise ValueError(f"cleanup pending for {name}/{attempt.name}")
                completed |= scored(state)
            if not completed:
                queue.append(name)
        reservations = {}
        for name in queue:
            phases = [manifest["tasks"][name][p] for p in ("agent", "verifier")]
            cpu = max(p["cpus"] for p in phases)
            mem = max(p["memory_mb"] for p in phases)
            if cpu > args.cpu_budget or mem > args.memory_mb:
                raise ValueError(f"task {name} exceeds the requested resource budget")
            reservations[name] = (cpu, mem)
        active = {}
        expires = {}
        expired = None
        stop = None

        def request_stop(sig, frame):
            nonlocal stop
            stop = stop or signal.Signals(sig).name

        for sig in (signal.SIGTERM, signal.SIGINT):
            signal.signal(sig, request_stop)
        try:
            while active or (queue and not stop):
                if not stop:
                    expired = next((n for n, child in active.items()
                                    if child.poll() is None and time.monotonic() >= expires[n]), None)
                    if expired:
                        stop = "batch_hard_timeout"
                for name, child in list(active.items()):
                    rc = child.poll()
                    if rc is not None:
                        child.stdin.close()
                        del active[name]
                        expires.pop(name)
                        if rc:
                            stop = stop or ("batch_hard_timeout" if rc == 3 else "worker_stopped")
                    elif stop and not child.stdin.closed:
                        with contextlib.suppress(BrokenPipeError):
                            why = "hard_timeout" if name == expired else stop
                            child.stdin.write((why + "\n").encode())
                            child.stdin.close()
                while queue and not stop and len(active) < args.jobs:
                    used_cpu = sum(reservations[n][0] for n in active)
                    used_mem = sum(reservations[n][1] for n in active)
                    name = next((n for n in queue
                                 if used_cpu + reservations[n][0] <= args.cpu_budget
                                 and used_mem + reservations[n][1] <= args.memory_mb), None)
                    if name is None:
                        break
                    queue.remove(name)
                    plan = manifest["tasks"][name]
                    deadline = args.hard_timeout or (plan["agent"]["timeout_sec"] + plan["verifier"]["timeout_sec"] + 1800)
                    attempt = run / "attempts" / name / uuid.uuid4().hex
                    attempt.mkdir(parents=True)
                    save(attempt / "state.json", {"status": "running", "task": name, "attempt_id": attempt.name,
                                                  "run_id": manifest["run_id"], "started_at": time.time(), "reward": None,
                                                  "hard_timeout_s": deadline, "jobs": args.jobs,
                                                  "cpu_budget": args.cpu_budget if math.isfinite(args.cpu_budget) else None,
                                                  "memory_budget_mb": args.memory_mb if args.memory_mb != sys.maxsize else None})
                    argv = [sys.executable, "-B", str(assets / "runner" / "scheduler.py"),
                            "_worker", "--attempt", str(attempt), "--deadline", str(deadline)]
                    active[name] = subprocess.Popen(argv, stdin=subprocess.PIPE, pass_fds=(lock.fileno(),),
                                                     start_new_session=True)
                    expires[name] = time.monotonic() + deadline
                if active:
                    time.sleep(0.1)
        finally:
            for child in active.values():
                child.stdin.close()
            for child in active.values():
                child.wait()
        summary = summarize(run, manifest)
        save(run / "summary.json", summary)
        print(json.dumps({k: v for k, v in summary.items() if k != "tasks"}, sort_keys=True))
        return 1 if stop or summary["scored"] < summary["planned"] else 0


def main():
    os.umask(0o077)
    ap = argparse.ArgumentParser(description=__doc__)
    commands = ap.add_subparsers(dest="cmd", required=True)
    init = commands.add_parser("init")
    for flag in ("run", "tasks", "binary", "config", "evidence-helper"):
        init.add_argument("--" + flag, type=Path, required=True)
    init.add_argument("--only", nargs="+", metavar="TASK")
    init.add_argument("--capture", action="append", default=[], metavar="TASK=/absolute/path",
                      help="collect this shared-task output before grading (repeatable)")
    run = commands.add_parser("run")
    run.add_argument("--run", type=Path, required=True)
    run.add_argument("--jobs", type=int, default=1)
    run.add_argument("--hard-timeout", type=float)
    run.add_argument("--cpu-budget", type=float, default=math.inf)
    run.add_argument("--memory-mb", type=int, default=sys.maxsize)
    summary = commands.add_parser("summary")
    summary.add_argument("--run", type=Path, required=True)
    worker = commands.add_parser("_worker", help=argparse.SUPPRESS)
    worker.add_argument("--attempt", type=Path, required=True)
    worker.add_argument("--deadline", type=float, required=True)
    args = ap.parse_args()
    if args.cmd == "init":
        freeze(args)
        return 0
    if args.cmd == "_worker":
        return run_worker(args)
    m = manifest_for(args.run)
    if args.cmd == "summary":
        print(json.dumps(summarize(args.run, m), indent=2, sort_keys=True))
        return 0
    if args.jobs <= 0 or not args.cpu_budget > 0 or args.memory_mb <= 0 or (args.hard_timeout is not None and
                          (not math.isfinite(args.hard_timeout) or args.hard_timeout <= 0)):
        ap.error("jobs, budgets and hard-timeout must be positive")
    frozen = (args.run / "assets/runner/scheduler.py").resolve()
    if Path(__file__).resolve() != frozen:
        if digest(frozen) != m["sha256"].get("runner/scheduler.py"):
            raise ValueError("frozen scheduler digest mismatch")
        os.execv(sys.executable, [sys.executable, "-B", str(frozen), *sys.argv[1:]])
    return schedule(args, m)


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError) as exc:
        print(f"scheduler: {exc}", file=sys.stderr)
        sys.exit(2)
