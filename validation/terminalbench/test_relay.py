import http.client
import http.server
import json
import os
from pathlib import Path
import socket
import tempfile
import threading
import unittest
from unittest.mock import Mock, patch
import yaml

from relay import Relay, _Messages, provider_relay, validate_config


class RelayTest(unittest.TestCase):
    def test_revocation_while_receiving_body(self):
        handler = Mock()
        handler.path = "/v1/messages"
        data = b'{"model":"m"}'
        handler.headers = {"Content-Length": str(len(data))}
        handler.server.revoked = threading.Event()
        handler.server.admission = threading.Lock()
        handler.server.model = "m"
        handler.server.key = "marker"
        handler.server.version = "2023-06-01"
        handler.server.url = "https://example.invalid/v1/messages"
        relay = Relay(None, None, handler.server)
        def finish_body(size):
            relay.revoke()
            return data
        handler.rfile.read.side_effect = finish_body
        _Messages.do_POST(handler)
        handler.send_error.assert_called_once_with(403)
        handler.server.opener.open.assert_not_called()

    def test_native_profile_date_and_guardrails(self):
        with tempfile.TemporaryDirectory() as tmp, patch.dict(os.environ, BENCH_TEST_KEY="fixture"):
            config = Path(tmp) / "profile.yaml"
            config.write_text("""default_provider: test
providers:
  test:
    api_provider: anthropic
    api_key_env: BENCH_TEST_KEY
    model: test
    base_url: http://127.0.0.1:1
    anthropic_version: 2023-06-01
    timeout_sec: 17
    request_timeout_sec: 0
runtime:
  guardrails_mode: standard
""")
            with provider_relay(config, tmp) as relay:
                self.assertEqual(relay._server.version, "2023-06-01")
                self.assertEqual(relay._server.upstream_timeout, 17)
            original = config.read_text()
            for old, new in (("timeout_sec: 17", "timeout_sec: true"), ("model: test", "model: test\n    prompt_cache: 1")):
                config.write_text(original.replace(old, new))
                with self.assertRaisesRegex(ValueError, "benchmark option"):
                    validate_config(config)
            config.write_text(original)
            for invalid in ("strict", "balanced"):
                config.write_text(config.read_text().replace("guardrails_mode: standard", f"guardrails_mode: {invalid}"))
                with self.assertRaisesRegex(ValueError, "guardrails"):
                    validate_config(config)
                config.write_text(config.read_text().replace(f"guardrails_mode: {invalid}", "guardrails_mode: standard"))

    def test_credential_lifetime(self):
        marker = "synthetic-credential-do-not-export"
        calls = []

        class Upstream(http.server.BaseHTTPRequestHandler):
            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                calls.append((self.path, self.headers.get("x-api-key"), body["model"]))
                if body.get("redirect"):
                    self.send_response(307)
                    self.send_header("Location", "http://127.0.0.1:1/private")
                elif body.get("framing"):
                    self.send_response(200)
                    if body["framing"] == "length":
                        self.send_header("Content-Length", "30")
                        payload = b'{"ok":true}'
                    else:
                        self.send_header("Transfer-Encoding", "chunked")
                        payload = b'B\r\n{"ok":true}\r\n'
                        if body["framing"] == "complete_chunked":
                            payload += b"0\r\n\r\n"
                    self.end_headers()
                    self.wfile.write(payload)
                    return
                elif body.get("retry_after"):
                    self.send_response(429)
                    self.send_header("Retry-After", body["retry_after"])
                else:
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(b'{"ok":true}')

            def log_message(self, *args):
                pass

        with http.server.HTTPServer(("127.0.0.1", 0), Upstream) as upstream:
            thread = threading.Thread(target=upstream.serve_forever, daemon=True)
            thread.start()
            self.addCleanup(thread.join)
            self.addCleanup(upstream.shutdown)
            with tempfile.TemporaryDirectory() as tmp, patch.dict(os.environ, BENCH_TEST_KEY=marker):
                path = Path(tmp) / "source.yaml"
                profile = dict(api_provider="anthropic-compatible", api_key_env="BENCH_TEST_KEY",
                               model="fixture",base_url=f"http://127.0.0.1:{upstream.server_port}/anthropic")
                path.write_text(yaml.safe_dump(dict(default_provider="test", providers={"test":profile})))
                with provider_relay(path, tmp) as relay:
                    def post(model="fixture", path="/v1/messages", **extra):
                        with socket.socket(socket.AF_UNIX) as sock:
                            sock.connect(str(relay.socket_dir / "provider.sock"))
                            conn=http.client.HTTPConnection("localhost")
                            conn.sock=sock
                            conn.request("POST",path,json.dumps(dict(model=model,**extra)))
                            response=conn.getresponse()
                            return response.status, response.read(), response.getheader("Retry-After"), response.getheader("Location")
                    self.assertNotIn(marker,relay.config.read_text()+str(relay.env))
                    self.assertEqual(post(), (200, b'{"ok":true}', None, None))
                    self.assertEqual(post(redirect=True), (307, b"", None, None))
                    for after in ("17", "Wed, 21 Oct 2037 07:28:00 GMT", "12345678"):
                        self.assertEqual(post(retry_after=after), (429, b"", after, None))
                    self.assertEqual(post(retry_after="invalid")[2], None)
                    self.assertEqual(post(framing="complete_chunked")[:2], (200, b'{"ok":true}'))
                    for framing in ("length", "chunked"):
                        with self.subTest(framing=framing), self.assertRaises(http.client.IncompleteRead):
                            post(framing=framing)
                    self.assertEqual(post(path="/elsewhere")[0],403)
                    self.assertEqual(post(model="another")[0],400)
                    relay.revoke()
                    self.assertEqual(post()[0],403)
                    self.assertEqual(relay.http_counts(), {"upstream_attempts": 9, "responses": 9})
                self.assertEqual(calls,[("/anthropic/v1/messages",marker,"fixture")]*9)
                profile["api_key"]=marker
                path.write_text(yaml.safe_dump(dict(default_provider="test",providers={"test":profile})))
                with self.assertRaisesRegex(ValueError,"unsupported benchmark"):
                    validate_config(path)

if __name__ == "__main__":
    unittest.main()
