"""Gate: the version smoke, and the deploy wiring it depends on.

assert-version.sh is the check that proves the deployed tag equals the version
the pods report. It is only worth anything if it fails on a mismatch, and if
every deploy workflow runs it, so this test:

  - runs the script against a stub kubectl: a match passes, a wrong version,
    a wrong commit and an empty reply each fail once the wait runs out
  - asks only the Running, non-terminating pods of the current ReplicaSet,
    and waits out the terminating pod of a rolling update and a new pod that
    is not up yet
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


def run_script(*replies: str, version: str = "v1.2.3", commit: str = SHA, wait: str = "0",
               pods: tuple[str, ...] = ("spanbarn-new ",)) -> subprocess.CompletedProcess:
    """Runs the script against a kubectl stub.

    `get deploy` answers revision 3 and selector app=spanbarn, `get rs` answers
    the hash "cur" only when asked for revision 3, `get pods` gives the next of
    `pods` and `exec` the next of `replies`; each list repeats its last entry.
    A reply starting with "!" makes that exec exit 1 with the rest on stderr.
    Every call is logged to the "calls" file, which the result carries as .calls.
    """
    with tempfile.TemporaryDirectory() as tmp:
        d = Path(tmp)
        for kind, items in (("exec", replies), ("pods", pods)):
            for i, item in enumerate(items):
                (d / f"{kind}{i}").write_text(item)
            (d / f"{kind}.max").write_text(str(len(items) - 1))
        stub = d / "kubectl"
        stub.write_text(
            "#!/bin/sh\n"
            f"d='{tmp}'\n"
            'echo "$*" >> "$d/calls"\n'
            "next() {\n"
            '  n=$(cat "$d/$1.n" 2>/dev/null || echo 0); echo $((n + 1)) > "$d/$1.n"\n'
            '  m=$(cat "$d/$1.max"); [ "$n" -le "$m" ] || n=$m; cat "$d/$1$n"\n'
            "}\n"
            'case "$3 $4" in\n'
            "'get deploy') printf '%s' '3 app=spanbarn,' ;;\n"
            "'get rs') case \"$*\" in *'\"3\"'*) printf cur ;; esac ;;\n"
            "'get pods') case \"$*\" in *'app=spanbarn,pod-template-hash=cur'*) next pods ;; esac ;;\n"
            "exec*) r=$(next exec); case \"$r\" in !*) printf '%s' \"${r#!}\" >&2; exit 1;; esac; printf '%s' \"$r\" ;;\n"
            "esac\n"
        )
        stub.chmod(stub.stat().st_mode | stat.S_IXUSR)
        env = dict(os.environ, PATH=f"{tmp}:{os.environ['PATH']}",
                   ASSERT_VERSION_WAIT_SECONDS=wait, ASSERT_VERSION_INTERVAL_SECONDS="0")
        result = subprocess.run(
            ["sh", str(SCRIPT), "ns", "spanbarn", "spanbarn", version, commit],
            capture_output=True, text=True, env=env,
        )
        calls = d / "calls"
        result.calls = calls.read_text() if calls.exists() else ""
        return result


NEW = f'{{"version":"v1.2.3","commit":"{SHA}","built":"x"}}'
OLD = '{"version":"v1.2.2","commit":"ffffffffffffffffffffffffffffffffffffffff","built":"x"}'


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

    def test_only_pods_of_the_current_replicaset_are_asked(self):
        result = run_script(NEW, pods=("spanbarn-a spanbarn-b ",))
        self.assertEqual(result.returncode, 0, result.stderr)
        execs = [c for c in result.calls.splitlines() if " exec " in f" {c} "]
        self.assertEqual(len(execs), 2, result.calls)
        self.assertIn("exec spanbarn-a ", execs[0])
        self.assertIn("exec spanbarn-b ", execs[1])

    def test_pod_that_is_not_up_yet_is_waited_for(self):
        result = run_script(NEW, pods=("", "spanbarn-new "), wait="30")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("no running pod of the current ReplicaSet", result.stderr)

    def test_failed_exec_is_retried_until_the_pod_answers(self):
        result = run_script('!error: container not found ("spanbarn")', OLD, NEW, wait="30")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("container not found", result.stderr)
        self.assertIn("wanted version v1.2.3", result.stderr)

    def test_failed_exec_fails_once_the_wait_runs_out(self):
        result = run_script('!error: container not found ("spanbarn")')
        self.assertEqual(result.returncode, 1)
        self.assertIn("exec failed", result.stderr)

    def test_no_current_pod_fails_once_the_wait_runs_out(self):
        result = run_script(NEW, pods=("",))
        self.assertEqual(result.returncode, 1)
        self.assertIn("no running pod", result.stderr)

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
