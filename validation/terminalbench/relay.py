"""Per-attempt Anthropic Messages relay. Only the host opens the credential."""

import contextlib
import http.server
import json
import os
from pathlib import Path
import re
import socketserver
import tempfile
import threading
import urllib.error
import urllib.parse
import urllib.request

import yaml


def validate_config(path):
    """Accept the benchmark profile; do not freeze inline credentials or custom hooks."""
    try:
        data = yaml.safe_load(Path(path).read_text(encoding="utf-8"))
    except (ValueError, yaml.YAMLError):
        raise ValueError("invalid benchmark YAML") from None

    def fields(obj, names):
        if not isinstance(obj, dict) or not set(obj) <= set(names.split()):
            raise ValueError("unsupported benchmark configuration field")

    fields(data, "schema_version default_provider providers session runtime skills hooks output")
    name = data.get("default_provider")
    if not isinstance(name, str) or not re.fullmatch(r"[A-Za-z][\w.-]{0,63}", name):
        raise ValueError("expected a single named benchmark provider")
    if not isinstance(data.get("providers"), dict) or set(data["providers"]) != {name}:
        raise ValueError("benchmark profiles require one provider")
    profile = data["providers"][name]
    fields(profile, "api_provider api_key_env base_url model anthropic_version timeout_sec "
           "request_timeout_sec stream_idle_timeout_ms context_window_tokens retry prompt_cache "
           "max_output_tokens output_config_effort thinking_mode thinking_budget include_thoughts")
    if profile.get("api_provider") not in {"anthropic", "anthropic-compatible"}:
        raise ValueError("benchmark relay requires the Anthropic Messages protocol")
    if not re.fullmatch(r"[A-Z_][A-Z0-9_]{0,127}", str(profile.get("api_key_env", ""))):
        raise ValueError("expected a credential environment variable name")
    if not isinstance(profile.get("model"), str) or not re.fullmatch(r"[\w./:-]{1,160}", profile["model"]):
        raise ValueError("expected a model identifier")
    url = urllib.parse.urlsplit(profile.get("base_url", ""))
    local = url.hostname in {"127.0.0.1", "::1", "localhost"}
    if (not url.hostname or url.username is not None or url.password is not None or
            url.query or url.fragment or not (url.scheme == "https" or (local and url.scheme == "http"))):
        raise ValueError("expected a credential-free HTTPS URL or a loopback test endpoint")
    if "anthropic_version" in profile and not re.fullmatch(r"\d{4}-\d{2}-\d{2}", str(profile["anthropic_version"])):
        raise ValueError("invalid Anthropic version")
    for key, choices in (("output_config_effort", {"minimal","low","medium","high","xhigh","max"}),
                         ("thinking_mode", {"adaptive","enabled","disabled"})):
        if key in profile and profile[key] not in choices:
            raise ValueError(f"unsupported {key}")
    fields(profile.get("retry", {}), "max_attempts base_delay_ms retry_429 retry_5xx retry_transport")
    fields(data.get("runtime", {}), "exec_finish_required guardrails_mode max_turns_soft max_turns_hard command_timeout_sec")
    fields(data.get("session", {}), "dir dir_mode")
    fields(data.get("output", {}), "format show_raw_events")
    fields(data.get("skills", {}), "dirs")
    fields(data.get("hooks", {}), "default_timeout_sec")
    for obj, omit in ((profile, {"api_provider","api_key_env","base_url","model","anthropic_version","output_config_effort","thinking_mode","retry"}),
                      (profile.get("retry", {}), set()), (data.get("runtime", {}), {"guardrails_mode"}),
                      (data.get("hooks", {}), set())):
        if any(type(v) not in {int,bool} for k,v in obj.items() if k not in omit):
            raise ValueError("expected numeric or boolean benchmark option")
    if data.get("runtime", {}).get("guardrails_mode", "balanced") not in {"balanced","strict","yolo"}:
        raise ValueError("unsupported guardrails mode")
    if data.get("skills", {}).get("dirs", []) not in ([], ["./skills"]):
        raise ValueError("external skill directories are unsupported in benchmark profiles")
    if data.get("schema_version", 1) != 1:
        raise ValueError("unsupported benchmark configuration version")
    # Only the controlled in-container store is ever exported. Do not propagate arbitrary paths.
    data["session"] = {"dir": "/tmp/aegis-benchmark/sessions", "dir_mode": "0700"}
    data["output"] = {"format": "text", "show_raw_events": False}
    return data


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args):
        return None


class _Server(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True

    def handle_error(self, request, client_address):
        # An untrusted partial request must not reach the host log.
        pass


class _Messages(http.server.BaseHTTPRequestHandler):
    timeout = 30

    def log_message(self, *args):
        pass

    def do_POST(self):
        if self.path != "/v1/messages" or self.server.revoked.is_set():
            self.send_error(403)
            return
        try:
            length = int(self.headers.get("Content-Length", ""))
            if not 0 < length <= 32 * 1024 * 1024 or self.headers.get("Transfer-Encoding"):
                raise ValueError
            data = self.rfile.read(length)
            if len(data) != length or json.loads(data)["model"] != self.server.model:
                raise ValueError
        except (ValueError, KeyError, TypeError, RecursionError):
            self.send_error(400)
            return
        # Grant the credential only to a fully received, validated request.
        # Revocation serializes with this handoff; a previously admitted POST
        # may already be in flight at the provider.
        with self.server.admission:
            key = self.server.key
        if key is None:
            self.send_error(403)
            return
        headers = {"Content-Type": "application/json", "x-api-key": key,
                   "anthropic-version": self.server.version}
        req = urllib.request.Request(self.server.url, data=data, headers=headers)
        try:
            with self.server.opener.open(req, timeout=self.server.upstream_timeout) as reply:
                self.send_response(reply.status)
                kind = reply.headers.get_content_type()
                self.send_header("Content-Type", kind if kind in {"application/json","text/event-stream"} else "application/octet-stream")
                self.end_headers()
                while not self.server.revoked.is_set():
                    block = reply.read1(65536)
                    if not block:
                        break
                    self.wfile.write(block)
                    self.wfile.flush()
        except urllib.error.HTTPError as exc:
            # Neither upstream response bodies nor redirect locations are safe diagnostics.
            with exc:
                self.send_response(exc.code if 400 <= exc.code < 600 else 502)
                after = exc.headers.get("Retry-After", "")
                if re.fullmatch(r"\d{1,6}", after):
                    self.send_header("Retry-After", after)
                self.end_headers()
        except (OSError, ValueError):
            if not self.server.revoked.is_set():
                self.close_connection = True


class Relay:
    def __init__(self, config, socket_dir, server):
        self.config = config
        self.socket_dir = socket_dir
        self.env = {"AEGIS_BENCHMARK_RELAY": "local-transport"}
        self._server = server

    def revoke(self):
        with self._server.admission:
            self._server.key = None
            self._server.revoked.set()


@contextlib.contextmanager
def provider_relay(config_path, private_tmp_dir):
    data = validate_config(config_path)
    profile = data["providers"][data["default_provider"]]
    key = os.getenv(profile["api_key_env"])
    if not key or "\r" in key or "\n" in key:
        raise ValueError("benchmark provider credential not available")
    with tempfile.TemporaryDirectory(dir=private_tmp_dir, prefix="relay-") as tmp:
        private = Path(tmp)
        mount = private / "ipc"
        mount.mkdir(mode=0o711)
        mount.chmod(0o711)
        sock = mount / "provider.sock"
        with _Server(str(sock), _Messages) as server:
            sock.chmod(0o666)  # mounted into this trial only; its host parent is 0700
            server.revoked = threading.Event()
            server.admission = threading.Lock()
            server.url = profile["base_url"].rstrip("/") + "/v1/messages"
            server.version = profile.get("anthropic_version", "2023-06-01")
            server.model = profile["model"]
            server.key = key
            server.upstream_timeout = max(1, min(profile.get("request_timeout_sec", 600), 1800))
            server.opener = urllib.request.build_opener(_NoRedirect())
            profile.update(base_url="http://127.0.0.1:19487", api_key_env="AEGIS_BENCHMARK_RELAY")
            config = private / "agent.yaml"
            config.write_text(yaml.safe_dump(data), encoding="utf-8")
            config.chmod(0o600)
            relay = Relay(config, mount, server)
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            try:
                yield relay
            finally:
                relay.revoke()
                server.shutdown()
                thread.join()
