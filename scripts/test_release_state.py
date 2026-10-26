"""Gate: the production rollback, and the deploy wiring it depends on.

release-state.sh records every workload in the namespace before a deploy and
puts them back if the deploy fails. A rollback is only worth anything if it
covers every workload and fails loudly when it cannot finish, so this test:

  - runs record and restore against a stub kubectl that keeps a fake namespace
  - checks restore undoes only the deployments that moved, to the recorded
    revision, and restores every CronJob image
  - checks a failed undo, a rollout that never gets ready, and a missing
    record each make restore exit non-zero
  - requires deploy-production.yml to record before it applies, smoke the
    public hostname, and roll back without swallowing errors

The workflows are read as text; nothing is deployed.
"""

from __future__ import annotations

import json
import os
import re
import stat
import subprocess
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
SCRIPT = REPO_ROOT / "deploy" / "scripts" / "release-state.sh"
WORKFLOWS = REPO_ROOT / ".github" / "workflows"

# A kubectl that answers from a JSON namespace in $STUB_NS and appends each
# mutating call to $STUB_LOG.
STUB = r'''#!/usr/bin/env python3
import json, os, sys
args = sys.argv[1:]
if args[:1] == ["-n"]:
    args = args[2:]
path = os.environ["STUB_NS"]
ns = json.load(open(path))
def log(line):
    with open(os.environ["STUB_LOG"], "a") as f:
        f.write(line + "\n")
def save():
    json.dump(ns, open(path, "w"))
if args[0] == "get":
    kind, rest = args[1], args[2:]
    table = ns["deployments"] if kind == "deployment" else ns["cronjobs"]
    if rest[0] == "-o":
        print(" ".join(sorted(table)), end="")
        sys.exit(0)
    name = rest[0]
    if name not in table:
        print(f'Error from server (NotFound): {kind} "{name}" not found', file=sys.stderr)
        sys.exit(1)
    if kind == "deployment":
        print(table[name], end="")
    else:
        print("".join(f"{c}={i} " for c, i in table[name]), end="")
    sys.exit(0)
if args[:2] == ["rollout", "undo"]:
    name = args[2].split("/", 1)[1]
    rev = args[3].split("=", 1)[1]
    log(f"undo {name} {rev}")
    if name in ns.get("fail_undo", []):
        sys.exit(1)
    ns["deployments"][name] = str(int(max(ns["deployments"].values(), key=int)) + 1)
    save()
    sys.exit(0)
if args[:2] == ["rollout", "status"]:
    name = args[2].split("/", 1)[1]
    log(f"status {name}")
    sys.exit(1 if name in ns.get("fail_status", []) else 0)
if args[:2] == ["set", "image"]:
    name = args[2].split("/", 1)[1]
    c, img = args[3].split("=", 1)
    log(f"set-image {name} {c}={img}")
    ns["cronjobs"][name] = [[k, img if k == c else v] for k, v in ns["cronjobs"][name]]
    save()
    sys.exit(0)
print("stub kubectl: unexpected " + " ".join(sys.argv[1:]), file=sys.stderr)
sys.exit(3)
'''

RUNNING = {
    "deployments": {"spanbarn": "41", "spanbarn-ingest": "40", "spanbarn-web": "12"},
    "cronjobs": {
        "spanbarn-settings-snapshot": [["dump", "ghcr.io/x/service:v1"], ["upload", "aws-cli:2"]],
    },
}


class Namespace:
    def __init__(self, tmp: Path, state: dict):
        self.tmp = tmp
        self.ns = tmp / "ns.json"
        self.log = tmp / "log"
        self.record = tmp / "record"
        self.ns.write_text(json.dumps(state))
        self.log.write_text("")
        stub = tmp / "bin" / "kubectl"
        stub.parent.mkdir()
        stub.write_text(STUB)
        stub.chmod(stub.stat().st_mode | stat.S_IXUSR)
        self.env = dict(
            os.environ,
            PATH=f"{stub.parent}:{os.environ['PATH']}",
            STUB_NS=str(self.ns),
            STUB_LOG=str(self.log),
        )

    def run(self, mode: str) -> subprocess.CompletedProcess:
        return subprocess.run(
            ["sh", str(SCRIPT), mode, "prod", str(self.record)],
            capture_output=True, text=True, env=self.env,
        )

    def deploy(self, **changes):
        state = json.loads(self.ns.read_text())
        for key, value in changes.items():
            state[key] = value
        self.ns.write_text(json.dumps(state))

    def calls(self) -> list[str]:
        return self.log.read_text().splitlines()


class ReleaseStateTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.ns = Namespace(Path(self._tmp.name), RUNNING)

    def tearDown(self):
        self._tmp.cleanup()

    def test_record_lists_every_deployment_and_cronjob_container(self):
        result = self.ns.run("record")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(
            self.ns.record.read_text().splitlines(),
            [
                "deploy spanbarn 41",
                "deploy spanbarn-ingest 40",
                "deploy spanbarn-web 12",
                "cronjob spanbarn-settings-snapshot dump=ghcr.io/x/service:v1",
                "cronjob spanbarn-settings-snapshot upload=aws-cli:2",
            ],
        )

    def test_record_refuses_an_empty_namespace(self):
        self.ns.deploy(deployments={}, cronjobs={})
        self.assertEqual(self.ns.run("record").returncode, 1)
        self.assertFalse(self.ns.record.exists())

    def test_restore_undoes_only_what_moved_and_resets_cronjob_images(self):
        self.ns.run("record")
        self.ns.deploy(
            deployments={"spanbarn": "42", "spanbarn-ingest": "43", "spanbarn-web": "12"},
            cronjobs={"spanbarn-settings-snapshot": [["dump", "ghcr.io/x/service:v2"], ["upload", "aws-cli:2"]]},
        )
        result = self.ns.run("restore")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        calls = self.ns.calls()
        self.assertIn("undo spanbarn 41", calls)
        self.assertIn("undo spanbarn-ingest 40", calls)
        self.assertNotIn("undo spanbarn-web 12", calls)
        self.assertIn("set-image spanbarn-settings-snapshot dump=ghcr.io/x/service:v1", calls)
        self.assertIn("status spanbarn", calls)
        self.assertIn("status spanbarn-ingest", calls)
        self.assertNotIn("status spanbarn-web", calls)
        cron = json.loads(self.ns.ns.read_text())["cronjobs"]["spanbarn-settings-snapshot"]
        self.assertEqual(cron, [["dump", "ghcr.io/x/service:v1"], ["upload", "aws-cli:2"]])

    def test_a_failed_undo_fails_restore_after_trying_the_rest(self):
        self.ns.run("record")
        self.ns.deploy(
            deployments={"spanbarn": "42", "spanbarn-ingest": "43", "spanbarn-web": "12"},
            fail_undo=["spanbarn"],
        )
        result = self.ns.run("restore")
        self.assertEqual(result.returncode, 1)
        self.assertIn("undo spanbarn-ingest 40", self.ns.calls())
        self.assertIn("::error::deployment/spanbarn", result.stdout)

    def test_a_rollback_that_never_gets_ready_fails_restore(self):
        self.ns.run("record")
        self.ns.deploy(
            deployments={"spanbarn": "42", "spanbarn-ingest": "40", "spanbarn-web": "12"},
            fail_status=["spanbarn"],
        )
        self.assertEqual(self.ns.run("restore").returncode, 1)

    def test_a_deployment_removed_since_the_record_fails_restore(self):
        self.ns.run("record")
        self.ns.deploy(deployments={"spanbarn": "41", "spanbarn-web": "12"})
        self.assertEqual(self.ns.run("restore").returncode, 1)

    def test_a_new_deployment_is_reported_and_left_alone(self):
        self.ns.run("record")
        self.ns.deploy(deployments=dict(RUNNING["deployments"], **{"spanbarn-extra": "1"}))
        result = self.ns.run("restore")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("::warning::deployment/spanbarn-extra", result.stdout)
        self.assertEqual(self.ns.calls(), [])

    def test_restore_without_a_record_fails(self):
        result = self.ns.run("restore")
        self.assertEqual(result.returncode, 1)
        self.assertIn("no recorded state", result.stderr)

    def test_wrong_argument_count_is_a_usage_error(self):
        result = subprocess.run(["sh", str(SCRIPT), "record"], capture_output=True, text=True)
        self.assertEqual(result.returncode, 2)


def step(text: str, name: str) -> str:
    """The body of the workflow step called name, up to the next step."""
    start = text.index(f"- name: {name}")
    nxt = text.find("\n      - name:", start + 1)
    return text[start:] if nxt == -1 else text[start:nxt]


class ProductionWiringTest(unittest.TestCase):
    def setUp(self):
        self.text = (WORKFLOWS / "deploy-production.yml").read_text()

    def test_the_release_is_recorded_before_the_apply(self):
        record = self.text.index("release-state.sh record")
        apply = self.text.index("kubectl apply")
        self.assertLess(record, apply)

    def test_production_is_smoked_through_its_public_hostname(self):
        self.assertIn("scripts/smoke.sh https://spanbarn.wiebe.xyz", self.text)

    def test_rollback_restores_the_record_and_swallows_nothing(self):
        body = step(self.text, "Roll back to the recorded release")
        self.assertIn("release-state.sh restore", body)
        self.assertNotIn("|| true", body)
        self.assertIsNone(re.search(r"rollout undo", body), "rollback must use the recorded revisions")

    def test_staging_is_smoked_through_its_public_hostname(self):
        text = (WORKFLOWS / "build-and-test.yml").read_text()
        self.assertIn("scripts/smoke.sh https://spanbarn.staging.wiebe.xyz", text)


if __name__ == "__main__":
    unittest.main()
