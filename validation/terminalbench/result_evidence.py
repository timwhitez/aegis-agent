"""Keep content-free verifier receipts and optional encrypted copies of declared deliveries."""

from contextlib import contextmanager
import hashlib
import io
import itertools
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import tarfile
import tempfile

MAX_LOG = 4 * 1024**2
MAX_CASES = 4096
MAX_ENTRIES = 4096
MAX_DELIVERY = 32 * 1024**2
CASE_STATUS = {"passed", "failed", "error", "skipped", "pending", "other", "xfail", "xpass"}
READ_FLAGS = os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK
PYTEST_CASE = re.compile(r"^(?:(?P<prefix>FAILED|ERROR|PASSED|SKIPPED|XFAIL|XPASS) (?P<summary>[^ ]+::[^ ]+)|"
                         r"(?P<node>\S+::.+?)\s+(?P<suffix>FAILED|ERROR|PASSED|SKIPPED|XFAIL|XPASS)\b)")

# Count matching lines only. These phrases describe log evidence, not a causal
# classification of the trial. Never carry command names, URLs or error text.
LOG_SIGNALS = {
    "command_not_found": rb": (?:line \d+: )?[^:\s]{1,256}: command not found\s*$|(?:^|/)sh: \d+: [^:\s]{1,256}: not found\s*$",
    "dns_failure": rb"\bcould not resolve (?:host|proxy)\b|\btemporary failure in name resolution\b|\bgetaddrinfo (?:ENOTFOUND|EAI_AGAIN)\b",
    "download_failure": rb"\b(?:failed to (?:download|fetch)|unable to fetch some archives)\b|\bcurl: \((?:7|18|22|28|56)\)",
    "tls_failure": rb"\b(?:CERTIFICATE_VERIFY_FAILED|SSL certificate problem|TLS handshake (?:failed|timeout)|x509: certificate signed by unknown authority)\b|\bcurl: \((?:35|60)\)",
    "pytest_collection_error": rb"^_+ ERROR collecting\b|\b\d+ errors? during collection\b",
}
LOG_SIGNALS = {name: re.compile(pattern, re.I) for name, pattern in LOG_SIGNALS.items()}


# This is the terminal-bench wrapper shape in which the pytest launch status
# directly determines the binary reward. Other shell programs stay unclassified.
BINARY_PYTEST_REWARD = re.compile(
    rb"(?m)^[ \t]*if[ \t]+\[[ \t]+\$\?[ \t]+-eq[ \t]+0[ \t]+\];[ \t]*then\s+"
    rb"echo\s+1\s*>\s*/logs/verifier/reward\.txt\s+else\s+"
    rb"echo\s+0\s*>\s*/logs/verifier/reward\.txt\s+fi\s*\Z"
)
MISSING_LAUNCH = re.compile(
    rb"^/tests/test\.sh: (?:line )?(\d{1,7}): (uvx|pytest): (?:command )?not found\s*\Z"
)
PYTEST_RAN = re.compile(rb"^=+ test session starts|^collected \d+ items?\b|^=+ .*?\d+ (?:passed|failed|skipped|errors?)\b", re.I)


def pytest_grade_site(script):
    """Return the tool and physical line range of the frozen binary scorer."""
    if script is None:
        return None
    try:
        with Path(script).open("rb") as stream:
            data = stream.read(256 * 1024 + 1)
    except OSError:
        return None
    suffix = BINARY_PYTEST_REWARD.search(data) if len(data) <= 256 * 1024 else None
    if not suffix:
        return None
    lines = data[:suffix.start()].rstrip().splitlines()
    if not lines:
        return None
    first = len(lines) - 1
    while first and lines[first - 1].endswith(b"\\"):
        first -= 1
    command = b" ".join(line.removesuffix(b"\\") for line in lines[first:])
    if not re.fullmatch(rb"[\w./:=+@ \t-]+", command):
        return None
    args = command.split()
    if not args or args[0] not in {b"uvx", b"pytest"}:
        return None
    tool = args[0]
    if tool == b"uvx":
        args = args[1:]
        while len(args) >= 2 and args[0] in {b"-p", b"--python", b"-w", b"--with", b"--index", b"--index-strategy"}:
            args = args[2:]
    if (not args or args[0] != b"pytest"
            or (b"--ctrf", b"/logs/verifier/ctrf.json") not in zip(args, args[1:])
            or not any(re.fullmatch(rb"/tests/[\w./-]+\.py", arg) for arg in args[1:])):
        return None
    return tool, range(first + 1, len(lines) + 1)


class Limit(Exception):
    pass


@contextmanager
def opened(parent, name, flags=READ_FLAGS):
    fd = os.open(name, flags, dir_fd=parent)
    try:
        yield fd
    finally:
        os.close(fd)


@contextmanager
def below(root, relative):
    parts = Path(relative).parts
    if not parts or Path(relative).is_absolute() or ".." in parts or len(parts) > 32:
        raise ValueError("invalid relative evidence path")
    parent = os.dup(root)
    try:
        for part in parts[:-1]:
            child = os.open(part, READ_FLAGS | os.O_DIRECTORY, dir_fd=parent)
            os.close(parent)
            parent = child
        with opened(parent, parts[-1]) as fd:
            yield fd
    finally:
        os.close(parent)


def regular_bytes(fd, limit):
    info = os.fstat(fd)
    if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
        raise ValueError("expected a single regular file")
    if info.st_size > limit:
        raise Limit
    with os.fdopen(fd, "rb", closefd=False) as reader:
        data = reader.read(limit + 1)
    if len(data) > limit:
        raise Limit
    if len(data) != info.st_size:
        raise ValueError("file changed during collection")
    return data


def receipt(root, path, limit):
    try:
        with below(root, path) as fd:
            data = regular_bytes(fd, limit)
        return {"status": "collected", "bytes": len(data), "sha256": hashlib.sha256(data).hexdigest()}, data
    except FileNotFoundError:
        status = "missing"
    except Limit:
        status = "limit"
    except (OSError, ValueError):
        status = "invalid"
    return {"status": status}, None


def verifier(root, directory, test_script=None):
    grade_site = pytest_grade_site(test_script)
    missing_scorer = False
    pytest_ran = False
    receipts = {}
    cases = {}
    case_source = None
    signals = {}
    logs_scanned = 0
    status = "missing"
    for name in ("ctrf.json", "test-stdout.txt", "test-stderr.txt", "reward.txt", "reward.json"):
        receipts[name], data = receipt(root, directory / name, MAX_LOG)
        if data is None:
            continue
        if name == "ctrf.json":
            try:
                tests = json.loads(data)["results"]["tests"]
                if not isinstance(tests, list) or len(tests) > MAX_CASES:
                    raise ValueError
                for index, test in enumerate(tests):
                    if not isinstance(test, dict) or not isinstance(test.get("name"), str):
                        raise ValueError
                    outcome = test.get("status")
                    identity = [test.get("filePath"), test.get("suite"), test["name"]]
                    key = hashlib.sha256(json.dumps(identity, ensure_ascii=True).encode()).hexdigest()
                    cases[index] = {"case_sha256": key, "status": outcome if isinstance(outcome, str) and outcome in CASE_STATUS else "other"}
                case_source, status = "ctrf", "collected"
            except (UnicodeError, ValueError, TypeError, KeyError, RecursionError):
                cases.clear()
                status = "invalid_ctrf"
        elif name in {"test-stdout.txt", "test-stderr.txt"}:
            logs_scanned += 1
            for line in io.BytesIO(data):
                sample = line[:8192]
                if grade_site:
                    missing = MISSING_LAUNCH.fullmatch(sample)
                    if missing and missing[2] == grade_site[0] and int(missing[1]) in grade_site[1]:
                        missing_scorer = True
                    if PYTEST_RAN.search(sample.strip()):
                        pytest_ran = True
                for label, pattern in LOG_SIGNALS.items():
                    if pattern.search(sample):
                        signals[label] = signals.get(label, 0) + 1
                if case_source == "ctrf" or len(line) > 8192:
                    continue
                match = PYTEST_CASE.match(line.decode("utf-8", "replace").strip())
                if match:
                    node = match["node"] or match["summary"]
                    key = hashlib.sha256(node.encode()).hexdigest()
                    if len(cases) >= MAX_CASES and key not in cases:
                        status = "partial"
                        continue
                    cases[key] = {"case_sha256": key, "status": (match["prefix"] or match["suffix"]).lower()}
                    case_source = "pytest"
                    if status != "partial":
                        status = "observed"
    if (missing_scorer and not pytest_ran and not cases and receipts["ctrf.json"]["status"] == "missing"
            and all(receipts[n]["status"] in {"collected", "missing"} for n in ("test-stdout.txt", "test-stderr.txt"))):
        signals["pytest_launcher_missing"] = 1
    return {"status": status, "case_source": case_source, "cases": list(cases.values()), "files": receipts,
            "log_files_scanned": logs_scanned, "log_signals": signals if logs_scanned else None}


def delivery(fd, recipient, attempt, index):
    # Traverse via held descriptors. Only regular files and directories enter
    # the receipt or the encrypted tar. Refuse links, special files and growth.
    digest = hashlib.sha256()
    count = size = entries = 0
    archive = io.BytesIO() if recipient else None
    tar = tarfile.open(fileobj=archive, mode="w") if archive is not None else None

    def visit(current, relative, depth):
        nonlocal entries, count, size
        entries += 1
        if entries > MAX_ENTRIES or depth > 24:
            raise Limit
        info = os.fstat(current)
        if stat.S_ISDIR(info.st_mode):
            with os.scandir(current) as iterator:
                names = [e.name for e in itertools.islice(iterator, MAX_ENTRIES - entries + 1)]
            if len(names) > MAX_ENTRIES - entries:
                raise Limit
            digest.update(b"D\x00" + os.fsencode(relative) + b"\x00")
            if tar:
                header = tarfile.TarInfo(relative)
                header.type, header.mode = tarfile.DIRTYPE, 0o700
                tar.addfile(header)
            for name in sorted(names):
                with opened(current, name) as child:
                    visit(child, relative + "/" + name, depth + 1)
        else:
            data = regular_bytes(current, MAX_DELIVERY - size)
            content = hashlib.sha256(data).digest()
            digest.update(b"F\x00" + os.fsencode(relative) + b"\x00" + content)
            count += 1
            size += len(data)
            if tar:
                header = tarfile.TarInfo(relative)
                header.size, header.mode = len(data), 0o600
                tar.addfile(header, io.BytesIO(data))

    try:
        visit(fd, "delivery", 0)
    finally:
        if tar:
            tar.close()
    record = {"status": "collected", "files": count, "bytes": size, "sha256": digest.hexdigest(),
              "private_copy": {"status": "disabled"}}
    if archive is not None:
        copy = {"status": "encryption_failed"}
        dest = attempt / ("delivery-%03d.cms" % index)
        try:
            with tempfile.NamedTemporaryFile(dir=attempt, prefix=".encrypted-", delete=False) as out:
                tmp = Path(out.name)
                try:
                    subprocess.run(["openssl", "cms", "-encrypt", "-binary", "-aes256", "-outform", "DER", str(Path(recipient).absolute())],
                                   input=archive.getbuffer(), stdout=out, stderr=subprocess.DEVNULL,
                                   check=True, timeout=30, env={"PATH": os.defpath})
                    out.flush()
                    if out.tell() <= 0:
                        raise ValueError("empty encrypted archive")
                    os.replace(tmp, dest)
                    copy = {"status": "encrypted", "file": dest.name,
                            "archive_sha256": hashlib.sha256(archive.getbuffer()).hexdigest()}
                finally:
                    tmp.unlink(missing_ok=True)
        except (OSError, ValueError, subprocess.SubprocessError):
            pass
        record["private_copy"] = copy
    return record


def retain_results(paths, attempt, plan, test_script=None):
    trial = Path(paths.trial_dir)
    directory = Path(paths.artifacts_dir).relative_to(trial)
    declarations = plan.get("artifacts") or plan.get("capture_paths", [])
    records = []
    with opened(None, trial, READ_FLAGS | os.O_DIRECTORY) as root:
        observations = verifier(root, Path(paths.verifier_dir).relative_to(trial), test_script)
        _, raw = receipt(root, directory / "manifest.json", 1024**2)
        try:
            manifest = json.loads(raw) if raw else []
            if not isinstance(manifest, list) or len(manifest) > MAX_ENTRIES:
                raise ValueError
            if any(not isinstance(row, dict) for row in manifest):
                raise ValueError
        except (UnicodeError, ValueError, RecursionError):
            manifest = []
        for index, definition in enumerate(declarations):
            item = {"source": definition} if isinstance(definition, str) else definition
            service = item.get("service") or "main"
            row = {"declaration_index": index, "service": service, "status": "missing"}
            records.append(row)
            matches = [m for m in manifest if m.get("source") == item["source"]
                       and (m.get("service") or "main") == service and m.get("status") == "ok"]
            if len(matches) != 1:
                if matches:
                    row["status"] = "ambiguous"
                continue
            try:
                dest = Path(matches[0]["destination"])
                rel = dest.relative_to(directory)
                if not rel.parts or rel.parts == ("manifest.json",) or ".." in dest.parts:
                    raise ValueError
                with below(root, dest) as fd:
                    row.update(delivery(fd, os.environ.get("AEGIS_BENCH_ARTIFACT_RECIPIENT_CERT"), Path(attempt), index))
            except Limit:
                row["status"] = "limit"
            except FileNotFoundError:
                pass
            except (KeyError, TypeError, ValueError, OSError, RecursionError):
                row["status"] = "invalid"
    return {"artifacts": records, "verifier_evidence": observations}
