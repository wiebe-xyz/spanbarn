#!/usr/bin/env python3
"""Asserts that binary-release tags only the commit at the head of main.

GitHub refuses a tag created with GITHUB_TOKEN on a commit behind main that
changed a workflow file, whether it is pushed with git or created through the
REST API (run 38998741136 tagging fa725ec was rejected; a probe reproduced it
for both). A run whose commit main has moved past must therefore tag nothing:
the newer commit's run releases it.

Run with:  python3 -m unittest discover -s scripts -p 'test_*.py'
"""

from __future__ import annotations

import unittest
from pathlib import Path

import yaml

WORKFLOW = Path(__file__).resolve().parent.parent / ".github" / "workflows" / "binary-release.yml"


class ReleaseHeadTest(unittest.TestCase):
    def setUp(self):
        self.jobs = yaml.safe_load(WORKFLOW.read_text())["jobs"]

    def test_tag_waits_for_the_head_check(self):
        tag = self.jobs["tag"]
        needs = tag.get("needs")
        needs = [needs] if isinstance(needs, str) else needs or []
        self.assertIn("head", needs, "the tag job must depend on the head check")
        self.assertIn("needs.head.outputs.current == 'true'", str(tag.get("if", "")))

    def test_head_check_compares_with_main(self):
        head = self.jobs["head"]
        self.assertIn("current", head.get("outputs", {}))
        script = "\n".join(str(s.get("run", "")) for s in head.get("steps", []))
        self.assertIn("commits/main", script)
        self.assertIn('current=false', script)

    def test_every_release_job_depends_on_the_tag(self):
        for name, job in self.jobs.items():
            if name in ("head", "tag"):
                continue
            needs = job.get("needs")
            needs = [needs] if isinstance(needs, str) else needs or []
            self.assertIn("tag", needs, f"{name} must not run when no tag was created")
            self.assertNotIn("always()", str(job.get("if", "")), f"{name} must be skipped with the tag job")


if __name__ == "__main__":
    unittest.main()
