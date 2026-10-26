"""Gate: the public smoke that staging and production deploys run.

smoke.sh is what turns a deploy that rolled out but serves the wrong thing
into a failed deploy. This test runs it against a local HTTP server that plays
a deployed SpanBarn: a healthy release passes, and each way a release can be
wrong fails.
"""

from __future__ import annotations

import os
import subprocess
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
SCRIPT = REPO_ROOT / "scripts" / "smoke.sh"
SHA = "0123456789abcdef0123456789abcdef01234567"


def healthy() -> dict[str, tuple[int, str]]:
    return {
        "/api/v1/health": (200, '{"status":"ok","version":"v1.2.3"}'),
        "/api/v1/version": (200, f'{{"version":"v1.2.3","commit":"{SHA}","built":"x"}}'),
        "/api/v1/projects": (401, '{"error":"unauthorized"}'),
        "/": (200, '<script type="module" src="/assets/index-AbC_1.js"></script>'),
        "/assets/index-AbC_1.js": (200, "console.log(1)"),
    }


class Site:
    def __init__(self, routes: dict[str, tuple[int, str]]):
        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                status, body = routes.get(self.path, (404, "not found"))
                data = body.encode()
                self.send_response(status)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def log_message(self, *args):
                pass

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.url = f"http://127.0.0.1:{self.server.server_address[1]}"

    def close(self):
        self.server.shutdown()
        self.server.server_close()


def smoke(routes, version="v1.2.3", commit=SHA) -> subprocess.CompletedProcess:
    site = Site(routes)
    try:
        env = dict(os.environ, SMOKE_ATTEMPTS="1", SMOKE_WAIT="0")
        return subprocess.run(
            ["bash", str(SCRIPT), site.url, version, commit],
            capture_output=True, text=True, env=env, timeout=60,
        )
    finally:
        site.close()


class PublicSmokeTest(unittest.TestCase):
    def test_a_healthy_release_passes(self):
        result = smoke(healthy())
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_an_old_version_fails(self):
        result = smoke(healthy(), version="v1.2.4")
        self.assertEqual(result.returncode, 1)
        self.assertIn("wanted version v1.2.4", result.stdout)

    def test_a_different_commit_fails(self):
        self.assertEqual(smoke(healthy(), commit="f" * 40).returncode, 1)

    def test_an_open_api_fails(self):
        routes = healthy()
        routes["/api/v1/projects"] = (200, "[]")
        self.assertEqual(smoke(routes).returncode, 1)

    def test_a_broken_api_fails(self):
        routes = healthy()
        routes["/api/v1/projects"] = (502, "bad gateway")
        self.assertEqual(smoke(routes).returncode, 1)

    def test_a_missing_bundle_fails(self):
        routes = healthy()
        del routes["/assets/index-AbC_1.js"]
        self.assertEqual(smoke(routes).returncode, 1)

    def test_wrong_argument_count_is_a_usage_error(self):
        result = subprocess.run(["bash", str(SCRIPT)], capture_output=True, text=True)
        self.assertEqual(result.returncode, 2)


if __name__ == "__main__":
    unittest.main()
