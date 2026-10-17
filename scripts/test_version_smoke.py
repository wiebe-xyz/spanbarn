"""Gate: the version smoke, and the deploy wiring it depends on.

assert-version.sh is the check that proves the deployed tag equals the version
the pods report. It is only worth anything if it fails on a mismatch, and if
every deploy workflow runs it, so this test:

  - runs the script against a stub kubectl: a match passes, a wrong version,
    a wrong commit and an empty reply each fail
  - requires each environment kustomization to carry the SPANBARN_VERSION patch
  - requires each deploy workflow to substitute __VERSION__ and run the smoke

The workflows are read as text; nothing is deployed.
"""

from __future__ import annotations

import os
import stat
import subprocess
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
SCRIPT = REPO_ROOT / "deploy" / "scripts" / "assert-version.sh"
SHA = "0123456789abcdef0123456789abcdef01234567"


def run_script(reply: str, version: str = "v1.2.3", commit: str = SHA) -> subprocess.CompletedProcess:
    with tempfile.TemporaryDirectory() as tmp:
        stub = Path(tmp) / "kubectl"
        stub.write_text(f"#!/bin/sh\nprintf '%s' '{reply}'\n")
        stub.chmod(stub.stat().st_mode | stat.S_IXUSR)
        env = dict(os.environ, PATH=f"{tmp}:{os.environ['PATH']}")
        return subprocess.run(
            ["sh", str(SCRIPT), "ns", "spanbarn", "spanbarn", version, commit],
            capture_output=True, text=True, env=env,
        )


class AssertVersionScriptTest(unittest.TestCase):
    def test_matching_version_and_commit_pass(self):
        reply = f'{{"version":"v1.2.3","commit":"{SHA}","built":"2026-10-17T12:00:00Z"}}'
        self.assertEqual(run_script(reply).returncode, 0)

    def test_wrong_version_fails(self):
        reply = f'{{"version":"v1.2.2","commit":"{SHA}","built":"x"}}'
        result = run_script(reply)
        self.assertEqual(result.returncode, 1)
        self.assertIn("wanted version v1.2.3", result.stderr)

    def test_wrong_commit_fails(self):
        reply = '{"version":"v1.2.3","commit":"ffffffffffffffffffffffffffffffffffffffff","built":"x"}'
        result = run_script(reply)
        self.assertEqual(result.returncode, 1)
        self.assertIn("wanted commit", result.stderr)

    def test_empty_reply_fails(self):
        self.assertEqual(run_script("").returncode, 1)

    def test_wrong_argument_count_is_a_usage_error(self):
        result = subprocess.run(["sh", str(SCRIPT)], capture_output=True, text=True)
        self.assertEqual(result.returncode, 2)


class DeployWiringTest(unittest.TestCase):
    def test_every_environment_sets_the_version_in_the_pod_spec(self):
        for env in ("testing", "staging", "production"):
            text = (REPO_ROOT / "deploy" / "k8s" / env / "kustomization.yaml").read_text()
            self.assertEqual(text.count("SPANBARN_VERSION"), 2, f"{env}: expected the writer and the ingest patch")
            self.assertIn('"__VERSION__"', text, env)

    def test_deploy_workflows_substitute_the_version_and_run_the_smoke(self):
        for name in ("build-and-test.yml", "deploy-production.yml"):
            text = (REPO_ROOT / ".github" / "workflows" / name).read_text()
            self.assertIn("__VERSION__", text, f"{name} never substitutes the version")
            self.assertIn("assert-version.sh", text, f"{name} does not run the version smoke")
        # testing and staging are both deployed from build-and-test.yml.
        text = (REPO_ROOT / ".github" / "workflows" / "build-and-test.yml").read_text()
        self.assertGreaterEqual(text.count("assert-version.sh"), 2)


if __name__ == "__main__":
    unittest.main()
