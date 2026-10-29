#!/usr/bin/env python3
"""Gate: retired keys leave the live app Secret.

The SOPS manifests set spanbarn-secrets through stringData, and kubectl apply
cannot remove a key that left stringData: the server stores it as data, so the
key stays and envFrom keeps handing it to every pod. drop-secret-keys.sh removes
the keys in deploy/k8s/retired-secret-keys.txt from the live Secret. This test:

  - runs the script against a stub kubectl that keeps a fake Secret
  - checks a listed key is removed, comments and blank lines are ignored, and
    keys that are not listed or already gone are left alone
  - checks no secret value reaches the output
  - requires each deploy job to run the script after it applies the manifest

The workflows are read as text; nothing is deployed.

Run with:  python3 -m unittest discover -s scripts -p 'test_*.py'
"""

from __future__ import annotations

import json
import os
import stat
import subprocess
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
SCRIPT = REPO_ROOT / "deploy" / "scripts" / "drop-secret-keys.sh"
KEYS = REPO_ROOT / "deploy" / "k8s" / "retired-secret-keys.txt"
WORKFLOWS = REPO_ROOT / ".github" / "workflows"

# A kubectl that answers `get secret -o go-template` with the key names in
# $STUB_SECRET and applies `patch --type=json` remove operations to it.
STUB = r'''#!/usr/bin/env python3
import json, os, sys
args = sys.argv[1:]
if args[:1] == ["-n"]:
    args = args[2:]
path = os.environ["STUB_SECRET"]
data = json.load(open(path))
if args[:2] == ["get", "secret"]:
    print("\n".join(sorted(data)))
elif args[:2] == ["patch", "secret"]:
    ops = json.loads(args[args.index("-p") + 1])
    for op in ops:
        assert op["op"] == "remove", op
        key = op["path"].removeprefix("/data/")
        if key not in data:
            sys.exit("remove of a missing key")
        del data[key]
    json.dump(data, open(path, "w"))
else:
    sys.exit(f"unexpected kubectl call: {args}")
'''


class DropSecretKeysTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        tmp = Path(self.tmp.name)
        kubectl = tmp / "kubectl"
        kubectl.write_text(STUB)
        kubectl.chmod(kubectl.stat().st_mode | stat.S_IEXEC)
        self.secret = tmp / "secret.json"
        self.keys = tmp / "keys.txt"
        self.env = dict(os.environ, PATH=f"{tmp}:{os.environ['PATH']}", STUB_SECRET=str(self.secret))

    def tearDown(self):
        self.tmp.cleanup()

    def run_script(self, data: dict, keys: str) -> tuple[subprocess.CompletedProcess, dict]:
        self.secret.write_text(json.dumps(data))
        self.keys.write_text(keys)
        proc = subprocess.run(
            ["sh", str(SCRIPT), "ns", "spanbarn-secrets", str(self.keys)],
            env=self.env, capture_output=True, text=True,
        )
        return proc, json.loads(self.secret.read_text())

    def test_listed_keys_are_removed_and_others_kept(self):
        proc, data = self.run_script(
            {"OLD_KEY": "c2VjcmV0LXZhbHVl", "KEEP": "a2VlcA=="},
            "# a comment\n\nOLD_KEY  # trailing comment\nGONE_ALREADY\n",
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertEqual(data, {"KEEP": "a2VlcA=="})
        self.assertIn("removed OLD_KEY", proc.stdout)
        self.assertIn("GONE_ALREADY not in", proc.stdout)
        self.assertNotIn("c2VjcmV0LXZhbHVl", proc.stdout + proc.stderr)

    def test_a_second_run_changes_nothing(self):
        proc, data = self.run_script({"KEEP": "a2VlcA=="}, "OLD_KEY\n")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertEqual(data, {"KEEP": "a2VlcA=="})

    def test_the_committed_list_runs(self):
        proc, _ = self.run_script({"KEEP": "a2VlcA=="}, KEYS.read_text())
        self.assertEqual(proc.returncode, 0, proc.stderr)


class DropSecretKeysWiringTest(unittest.TestCase):
    def check(self, workflow: str, sops_file: str):
        text = (WORKFLOWS / workflow).read_text()
        apply = text.index(f"sops -d {sops_file}")
        drop = text.index("drop-secret-keys.sh", apply)
        next_apply = text.find("sops -d deploy/k8s/", apply + 1)
        self.assertTrue(next_apply == -1 or drop < next_apply, f"{sops_file}: drop step must follow its apply")
        self.assertIn("retired-secret-keys.txt", text[drop:drop + 300])

    def test_testing_drops_retired_keys(self):
        self.check("build-and-test.yml", "deploy/k8s/testing/secret.yaml")

    def test_staging_drops_retired_keys(self):
        self.check("build-and-test.yml", "deploy/k8s/staging/secret.yaml")

    def test_production_drops_retired_keys(self):
        self.check("deploy-production.yml", "deploy/k8s/production/secret.yaml")


if __name__ == "__main__":
    unittest.main()
