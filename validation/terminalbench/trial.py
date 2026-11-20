"""Run one frozen, single-step task on the pinned Harbor Docker backend."""
import argparse
import asyncio
from contextlib import ExitStack, redirect_stderr, redirect_stdout
import importlib.metadata
import json
import math
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
from uuid import UUID

from task_plan import UnsupportedTask, read_plan

HARBOR_VERSION = "0.26.0"
MEMORY_DIR = Path("/dev/shm")


def trial_name(label):
    return "aegistb-" + UUID(label).hex


def cleanup(label):
    name = trial_name(label)
    for project in (name + "__env", name + "__verifier__trial"):
        for objects, ls, rm in (("container", ["ls", "-aq"], ["rm", "-f", "-v"]),
                                ("network", ["ls", "-q"], ["rm"]),
                                ("volume", ["ls", "-q"], ["rm"])):
            found = subprocess.check_output(["docker", objects, *ls, "--filter",
                                             "label=com.docker.compose.project=" + project],
                                            timeout=15, text=True).split()
            if found:
                subprocess.run(["docker", objects, *rm, *found], timeout=45, check=True,
                               stdout=subprocess.DEVNULL)
    scratch = MEMORY_DIR / name
    if scratch.exists():
        shutil.rmtree(scratch)


def check_runtime_profile(task, plan):
    import yaml
    import tomllib
    with (task / "task.toml").open("rb") as stream:
        spec = tomllib.load(stream)
    # Evaluate only frozen, prebuilt single-step tasks. A compose file is host
    # configuration: keep dependency wiring but never accept host authority.
    for directory in (task / "environment", task / "tests"):
        for f in directory.glob("*compose.y*ml"):
            if f.name != "docker-compose.yaml":
                raise UnsupportedTask("unsupported Compose filename")
            compose = yaml.safe_load(f.read_text())
            if (not isinstance(compose, dict) or set(compose) != {"services"}
                    or re.search(r"\$(?:[A-Za-z_]|\{)", yaml.safe_dump(compose).replace("$$", ""))):
                raise UnsupportedTask("unsupported Compose interpolation or top-level settings")
            services = compose["services"]
            for name, service in services.items():
                allowed = {"image", "depends_on", "command", "environment", "expose", "healthcheck"}
                if set(service) - allowed or (name != "main" and not service.get("image")):
                    raise UnsupportedTask("unsupported Compose service settings")
                required = plan["agent" if directory.name == "environment" else "verifier"]["image"]
                if name == "main" and service.get("image", required) != required:
                    raise UnsupportedTask("Compose main image conflicts with task.toml")
                env = service.get("environment", {})
                if (isinstance(env, list) and any("=" not in entry for entry in env)
                    or isinstance(env, dict) and any(value is None for value in env.values())):
                    raise UnsupportedTask("Compose host environment passthrough is unsupported")
    for section in (spec.get("agent", {}), spec.get("verifier", {}), spec["environment"],
                    spec.get("verifier", {}).get("environment", {})):
        if section.get("env") or section.get("mcp_servers"):
            raise UnsupportedTask("task-supplied environment injection is unsupported")
    if importlib.metadata.version("harbor") != HARBOR_VERSION:
        raise UnsupportedTask("unexpected Harbor version")
    if (not MEMORY_DIR.is_dir() or subprocess.check_output(
            ["stat", "-f", "-c", "%T", MEMORY_DIR], text=True).strip() != "tmpfs"):
        raise UnsupportedTask("Harbor intermediate data requires Linux tmpfs")


async def execute(args, scratch, status):
    task = args.task.resolve()
    captures = json.loads(args.capture_plan.read_bytes()) if args.capture_plan else []
    if not isinstance(captures, list):
        raise UnsupportedTask("invalid frozen capture plan")
    plan = read_plan(task, captures)
    status["artifact_selection"] = ("task_declared" if plan["artifacts"] else
                                    "operator_selected" if captures else "not_configured")
    # Until the native collection completes, distinguish an unavailable receipt
    # (e.g. setup aborted) from a selected output observed to be missing.
    status["artifacts"] = [{"declaration_index": i, "status": "unavailable"}
                           for i in range(len(plan["artifacts"] or captures))]
    check_runtime_profile(task, plan)
    from harbor.models.trial.config import TrialConfig
    from harbor.trial.hooks import TrialEvent
    from harbor.trial.trial import Trial
    from relay import provider_relay
    from result_evidence import retain_results

    status["stage"] = "setup"
    with provider_relay(args.config, scratch) as relay, ExitStack() as finalizers:
        def close_provider():
            relay.revoke()
            status["provider_http"] = relay.http_counts()

        finalizers.callback(close_provider)
        cfg = TrialConfig.model_validate({
            "task": {"path": task}, "trials_dir": scratch, "trial_name": trial_name(args.label),
            "artifacts": plan.get("capture_paths", []),
            "agent": {"import_path": "aegis_harbor:AegisAgent"},
            "verifier": {"import_path": "aegis_harbor:ObservedVerifier", "kwargs": {
                "observation_path": str(args.attempt / "resources-verifier.json"),
                "exit_path": str(scratch / "verifier-exit")}},
            "environment": {"type": "docker", "cpu_enforcement_policy": "limit",
                            "memory_enforcement_policy": "limit", "mounts": [{
                                "type": "bind", "source": str(relay.socket_dir),
                                "target": "/run/aegis-provider", "read_only": True}]},
        })
        trial = await Trial.create(cfg)
        agent = trial.agent
        agent.binary, agent.helper = args.binary, args.evidence_helper
        agent.config_file, agent.relay_env = relay.config, relay.env
        agent.agent_rc = None
        agent.observation_path = args.attempt / "resources-agent.json"
        evidence = {}

        async def before_agent(_):
            status["stage"] = "agent"

        async def after_agent(_):
            relay.revoke()
            status["stage"] = "agent"
            try:
                if await agent.collect(trial.agent_environment, args.attempt, args.evidence_helper):
                    evidence.update(json.loads((args.attempt / "diagnostics.json").read_bytes()))
            except (OSError, ValueError, RuntimeError, TimeoutError, subprocess.SubprocessError):
                pass

        async def before_grade(_):
            # In shared mode a timed-out docker exec may leave a process alive.
            # Ordinary completed user services must retain their native semantics.
            if plan["mode"] == "shared" and (
                agent.agent_rc is None or evidence.get("exit_code") != agent.agent_rc
            ):
                raise RuntimeError("agent termination unconfirmed")
            status["stage"] = "verifier"

        def phase_facts(result):
            status["agent_rc"] = agent.agent_rc
            exit_path = scratch / "verifier-exit"
            if exit_path.is_file():
                status["verify_rc"] = int(exit_path.read_text())
            status["diagnostics"] = "collected" if evidence else "missing"
            for key, timing in (("agent_s", result.agent_execution), ("verify_s", result.verifier)):
                if timing and timing.started_at and timing.finished_at:
                    status[key] = (timing.finished_at - timing.started_at).total_seconds()
            rewards = result.verifier_result.rewards if result.verifier_result else {}
            reward = (rewards or {}).get("reward")
            if type(reward) in (int, float) and math.isfinite(reward):
                status["reward"] = reward

        async def on_cancel(event):
            # Artifact recovery or container teardown can outlive the scheduler's
            # TERM grace period. Leave the known phase facts first.
            status.update(status="interrupted", reason=["cancelled"])
            close_provider()
            phase_facts(event.result)
            save_outcome(args.attempt, status)

        trial.add_hook(TrialEvent.AGENT_START, before_agent)
        trial.add_hook(TrialEvent.AGENT_END, after_agent)
        trial.add_hook(TrialEvent.VERIFICATION_START, before_grade)
        trial.add_hook(TrialEvent.CANCEL, on_cancel)
        try:
            result = await trial.run()
        finally:
            # Harbor initializes its result before setup and fills phase times on
            # unwind. This also runs when trial.run re-raises cancellation.
            try:
                phase_facts(trial.result)
            except RuntimeError:
                status["diagnostics"] = "missing"
            try:
                # Shared-mode Harbor uploads this frozen grader. A separate image
                # can contain a different script at the same container path.
                grader = task / "tests/test.sh" if plan["mode"] == "shared" else None
                started = (args.attempt / "resources-verifier.json").is_file()
                status.update(retain_results(trial.paths, args.attempt, plan, grader, verifier_started=started))
            except (OSError, ValueError, RuntimeError, subprocess.SubprocessError):
                status["verifier_evidence"] = {"status": "unavailable"}
                status["artifacts"] = [{"declaration_index": i, "status": "unavailable"}
                                       for i in range(len(plan["artifacts"] or captures))]
        classify_result(status, result, evidence)


def classify_result(status, result, evidence):
    failures = {e.exception_type for e in result.recorded_exceptions()}
    if "AgentTimeoutError" in failures:
        status.update(status="agent_timeout", stage="agent")
    elif "VerifierTimeoutError" in failures:
        status.update(status="verifier_timeout", stage="verifier")
    elif (not failures and result.reward_valid and status["reward"] == 0 and
          (status["verifier_evidence"].get("log_signals") or {}).get("pytest_launcher_missing")):
        status.update(status="verifier_error", stage="verifier", reason=["pytest_launcher_missing"],
                      observed_reward=status["reward"])
    elif (not failures and result.reward_valid and status["reward"] is not None
          and evidence.get("started") is True
          and evidence.get("category") in {"completed", "agent_failed"}):
        status.update(status="completed", stage="complete")
    else:
        if not evidence.get("started") or evidence.get("category") in {"provider_error", "request_rejected", "request_cancelled"}:
            status["stage"] = "agent" if result.agent_execution else "setup"
        if failures:
            known = {"EnvironmentStartTimeoutError", "AgentSetupTimeoutError", "SandboxBuildFailedError",
                     "HealthcheckError", "AddTestsDirError", "DownloadVerifierDirError",
                     "RewardFileNotFoundError", "RewardFileEmptyError", "VerifierOutputParseError"}
            status["reason"] = sorted(known.intersection(failures)) or ["execution_failed"]
        elif not evidence:
            status["reason"] = ["diagnostics_unavailable"]
        elif evidence.get("category") not in {"completed", "agent_failed"}:
            status["reason"] = [evidence["category"]]
        else:
            status["reason"] = ["invalid_reward"]


async def run(args, scratch, status):
    current = asyncio.current_task()
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGTERM, signal.SIGINT):
        loop.add_signal_handler(sig, current.cancel)
    try:
        await execute(args, scratch, status)
    finally:
        for sig in (signal.SIGTERM, signal.SIGINT):
            loop.remove_signal_handler(sig)


def save_outcome(attempt, status):
    status["reward_valid"] = status["status"] == "completed"
    result = attempt / "outcome.json"
    draft = result.with_suffix(".tmp")
    draft.write_text(json.dumps(status, allow_nan=False) + "\n")
    draft.replace(result)


def main():
    os.umask(0o077)
    if sys.argv[1:2] == ["cleanup"]:
        parser = argparse.ArgumentParser()
        parser.add_argument("--label", required=True)
        cleanup(parser.parse_args(sys.argv[2:]).label)
        return
    parser = argparse.ArgumentParser()
    for key in ("task", "attempt", "binary", "config", "evidence-helper"):
        parser.add_argument("--" + key, type=Path, required=True)
    parser.add_argument("--label", required=True)
    parser.add_argument("--capture-plan", type=Path)
    args = parser.parse_args()
    scratch = MEMORY_DIR / trial_name(args.label)
    status = {"status": "infra_invalid", "stage": "preflight", "reward": None,
              "agent_rc": None, "verify_rc": None, "agent_s": None, "verify_s": None, "diagnostics": "missing",
              "verifier_evidence": {"status": "not_started"}}
    try:
        scratch.mkdir(mode=0o700)
        with open(os.devnull, "w") as quiet, redirect_stderr(quiet), redirect_stdout(quiet):
            asyncio.run(run(args, scratch, status))
    except (asyncio.CancelledError, KeyboardInterrupt):
        status.update(status="interrupted", reason=["cancelled"])
    except UnsupportedTask as exc:
        status["reason"] = [str(exc)]
    except Exception:
        # Upstream errors can embed executable arguments, verifier output or env.
        # The durable stage and status provide the failure without raw text.
        status["reason"] = ["execution_failed"]
    finally:
        save_outcome(args.attempt, status)
        cleanup(args.label)


if __name__ == "__main__":
    main()
