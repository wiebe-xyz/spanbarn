"""Gate: every ssh and scp in the deploy workflows sends keepalives.

A deploy step runs its script on the k3s host over one ssh connection. When
the network path drops that connection silently, the remote script finishes
and the host keeps the last output in its send queue, while the runner's ssh
waits for bytes that never arrive, until the job's timeout. That hung the
v0.3.472 production deploy for its full 60 minutes. With ServerAliveInterval
the client probes the server and gives up after ServerAliveCountMax missed
replies, so the step fails in about a minute and the workflow's rollback and
failure handling run.

The workflows are read as text; nothing is deployed.
"""

from __future__ import annotations

import re
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
WORKFLOWS = ("build-and-test.yml", "deploy-production.yml")
CALL = re.compile(r"^\s*(ssh|scp)\s")


class DeploySSHKeepaliveTest(unittest.TestCase):
    def test_every_ssh_and_scp_sets_keepalives(self):
        for name in WORKFLOWS:
            lines = (REPO_ROOT / ".github" / "workflows" / name).read_text().splitlines()
            calls = [(i + 1, line) for i, line in enumerate(lines) if CALL.match(line)]
            self.assertTrue(calls, f"{name}: no ssh or scp calls found")
            for lineno, line in calls:
                self.assertIn("-o ServerAliveInterval=15", line, f"{name}:{lineno}")
                self.assertIn("-o ServerAliveCountMax=4", line, f"{name}:{lineno}")


if __name__ == "__main__":
    unittest.main()
