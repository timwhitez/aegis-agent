"""Read the resource and verifier plan used by both batch and trial entry points."""
import math
from pathlib import Path
import tomllib


class UnsupportedTask(ValueError):
    """A bounded message that is safe to retain with the attempt."""


def read_plan(task_dir):
    root = Path(task_dir)
    with (root / "task.toml").open("rb") as source:
        task = tomllib.load(source)
    if task.get("steps"):
        raise UnsupportedTask("this batch adapter supports single-step tasks")
    verifier = task.get("verifier", {})
    mode = verifier.get("environment_mode", "separate" if "environment" in verifier else "shared")
    if mode not in {"shared", "separate"} or mode == "shared" and "environment" in verifier:
        raise UnsupportedTask("invalid verifier environment mode")
    agent_env = task["environment"]
    verify_env = verifier.get("environment", agent_env)
    if mode == "separate" and "environment" not in verifier:
        raise UnsupportedTask("this adapter requires an explicit prebuilt verifier image")

    def phase(env, config, default_timeout):
        result = {"image": env.get("docker_image"), "cpus": env.get("cpus"),
                  "memory_mb": env.get("memory_mb"),
                  "timeout_sec": config.get("timeout_sec", default_timeout)}
        if not isinstance(result["image"], str) or not result["image"]:
            raise UnsupportedTask("this adapter requires prebuilt task images")
        for key in ("cpus", "memory_mb", "timeout_sec"):
            value = result[key]
            if type(value) not in (int, float) or not math.isfinite(value) or value <= 0:
                raise UnsupportedTask(f"task must declare a positive finite {key}")
        if env.get("os", "linux") != "linux" or env.get("mcp_servers"):
            raise UnsupportedTask("this adapter supports Linux tasks without MCP servers")
        return result

    return {"agent": phase(agent_env, task.get("agent", {}), None),
            "verifier": phase(verify_env, verifier, 600), "mode": mode,
            "compose": (root / "environment/docker-compose.yaml").is_file(),
            "artifacts": task.get("artifacts", [])}


if __name__ == "__main__":
    import argparse
    from collections import Counter
    from harbor.models.task.task import Task
    from harbor.models.task.verifier_mode import resolve_task_verifier_mode

    parser = argparse.ArgumentParser(description="Compare frozen task plans with native Harbor")
    parser.add_argument("tasks", type=Path)
    root = parser.parse_args().tasks
    counts = Counter()
    for definition in sorted(root.glob("*/task.toml")):
        plan = read_plan(definition.parent)
        task = Task(definition.parent).config
        native_mode = resolve_task_verifier_mode(task).value
        if native_mode != plan["mode"]:
            raise SystemExit(f"{definition.parent.name}: inconsistent verifier mode")
        for role, env, timeout in (("agent", task.environment, task.agent.timeout_sec),
                                   ("verifier", task.verifier.environment or task.environment,
                                    task.verifier.timeout_sec)):
            expected = dict(image=env.docker_image, cpus=env.cpus, memory_mb=env.memory_mb,
                            timeout_sec=timeout)
            if plan[role] != expected:
                raise SystemExit(f"{definition.parent.name}: inconsistent {role} plan")
        counts[native_mode] += 1
    if not counts:
        raise SystemExit("no task definitions found")
    print(f"{sum(counts.values())} plans match Harbor: {counts['shared']} shared, {counts['separate']} separate")
