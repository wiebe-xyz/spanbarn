"""Gate: the sharded race run.

race-test.sh splits the slowest package into shards. A split is only safe if
every test still runs exactly once and a failure in any shard fails the run,
so this test drives the script with a stub `go` that records each call:

  - every listed test lands in exactly one shard's -run pattern
  - the other packages run once, without the sharded package
  - a failing shard, or a failing run of the other packages, exits non-zero
  - each shard lists its slowest top-level tests, slowest first
  - the deploy workflow runs the race tests through the script
"""

from __future__ import annotations

import os
import re
import stat
import subprocess
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
SCRIPT = REPO_ROOT / "scripts" / "race-test.sh"
MOD = "example.com/m"
TESTS = [f"TestCase{i:02d}" for i in range(11)] + ["ExampleThing", "FuzzParse"]

# Answers `go list` and `go test -list`, logs every `go test -race` call,
# prints VERBOSE for a -v call, and fails a -race call whose arguments match
# $STUB_FAIL.
STUB = f"""#!/bin/sh
if [ "$1" = list ]; then
	if [ "$2" = ./... ]; then
		printf '%s\\n' {MOD}/a {MOD}/internal/repository {MOD}/b
	else
		echo {MOD}/internal/repository
	fi
	exit 0
fi
if [ "$1" = test ] && [ "$2" = -list ]; then
	printf '%s\\n' {" ".join(TESTS)}
	echo "ok  	{MOD}/internal/repository	0.1s"
	exit 0
fi
echo "$*" >> "$STUB_LOG"
case " $* " in
*" -v "*) cat "$STUB_VERBOSE" ;;
esac
case "$*" in
*"$STUB_FAIL"*) [ -n "$STUB_FAIL" ] && exit 1 ;;
esac
exit 0
"""

# What `go test -v` prints: test i takes i+0.5 seconds, and a subtest that
# takes longer than all of them must stay out of the slowest list.
VERBOSE = "".join(
    f"=== RUN   {t}\n--- PASS: {t} ({i + 0.5:.2f}s)\n"
    f"    --- PASS: {t}/sub (99.00s)\n    x_test.go:1: chatter\n"
    for i, t in enumerate(TESTS)
) + f"PASS\nok  \t{MOD}/internal/repository\t1.0s\n"
SLOWEST = [t for t in reversed(TESTS)][:10]


def run(fail: str = "") -> tuple[subprocess.CompletedProcess, list[str]]:
    with tempfile.TemporaryDirectory() as tmp:
        stub = Path(tmp) / "go"
        stub.write_text(STUB)
        stub.chmod(stub.stat().st_mode | stat.S_IXUSR)
        log = Path(tmp) / "log"
        log.write_text("")
        verbose = Path(tmp) / "verbose"
        verbose.write_text(VERBOSE)
        env = dict(
            os.environ,
            PATH=f"{tmp}:{os.environ['PATH']}",
            STUB_LOG=str(log),
            STUB_FAIL=fail,
            STUB_VERBOSE=str(verbose),
            RUNNER_TEMP=tmp,
            RACE_SHARDS="4",
        )
        result = subprocess.run(["bash", str(SCRIPT)], capture_output=True, text=True, env=env, timeout=60)
        return result, log.read_text().splitlines()


class RaceTestScriptTest(unittest.TestCase):
    def test_every_test_runs_in_exactly_one_shard(self):
        result, calls = run()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        shards = [c for c in calls if "-run" in c]
        self.assertEqual(len(shards), 4)
        seen: list[str] = []
        for call in shards:
            pattern = re.search(r"-run \^\((.*)\)\$", call).group(1)
            seen += pattern.split("|")
            self.assertTrue(call.endswith("./internal/repository"), call)
            self.assertIn("-race", call)
        self.assertEqual(sorted(seen), sorted(TESTS))

    def test_other_packages_run_once_without_the_sharded_one(self):
        _, calls = run()
        others = [c for c in calls if "-run" not in c]
        self.assertEqual(others, [f"test -race -count=1 {MOD}/a {MOD}/b"])

    def test_a_failing_shard_fails_the_run(self):
        result, _ = run(fail="TestCase01")
        self.assertEqual(result.returncode, 1)
        self.assertIn("FAILED", result.stdout)

    def test_failing_other_packages_fail_the_run(self):
        result, _ = run(fail=f"{MOD}/a")
        self.assertEqual(result.returncode, 1)

    def test_shards_run_verbose(self):
        _, calls = run()
        shards = [c for c in calls if "-run" in c]
        self.assertTrue(all(" -v " in c for c in shards), shards)

    def test_a_passing_shard_prints_its_result_and_slowest_tests(self):
        result, _ = run()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertNotIn("=== RUN", result.stdout)
        self.assertNotIn("chatter", result.stdout)
        section = result.stdout.split("=== shard0: ok\n", 1)[1]
        self.assertTrue(section.startswith(f"ok  \t{MOD}/internal/repository"), section)
        listed = section.split("--- shard0: slowest tests\n", 1)[1].splitlines()[:11]
        names = [line.split()[1] for line in listed[:10]]
        self.assertEqual(names, SLOWEST)
        self.assertEqual(listed[0].split()[0], "12.50s")
        self.assertNotIn("/sub", " ".join(listed[:10]))
        self.assertTrue(listed[10].startswith("=== "), listed[10])

    def test_a_failing_shard_prints_its_whole_log(self):
        result, _ = run(fail="TestCase01")
        failing = result.stdout.split("FAILED\n", 1)[1]
        self.assertTrue(failing.startswith("=== RUN"), failing[:200])
        self.assertIn("chatter", failing)

    def test_the_deploy_workflow_runs_race_through_the_script(self):
        text = (REPO_ROOT / ".github" / "workflows" / "build-and-test.yml").read_text()
        self.assertIn("bash scripts/race-test.sh", text)
        self.assertNotIn("go test -race -count=1 ./...", text)


if __name__ == "__main__":
    unittest.main()
