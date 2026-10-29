#!/usr/bin/env python3
"""Asserts that a newer merge never cancels a running deploy.

Build and Deploy Testing deploys testing and staging, and Deploy Production
runs when it succeeds. With cancel-in-progress on the testing group, each merge
cancelled the rollout of the merge before it and so its production deploy; on
2026-10-29 the release with 65cb243 never reached production that way. Both
groups now queue. Because a queued run can cover commits that got no tag of
their own, the production preflight skips such a commit instead of failing.

Run with:  python3 -m unittest discover -s scripts -p 'test_*.py'
"""

from __future__ import annotations

import unittest
from pathlib import Path

import yaml

WORKFLOWS = Path(__file__).resolve().parent.parent / ".github" / "workflows"


def load(name: str) -> dict:
    return yaml.safe_load((WORKFLOWS / name).read_text())


class DeployQueueTest(unittest.TestCase):
    def test_deploy_groups_queue_instead_of_cancelling(self):
        for name in ("build-and-test.yml", "deploy-production.yml"):
            concurrency = load(name).get("concurrency") or {}
            self.assertTrue(concurrency.get("group"), f"{name} needs a concurrency group")
            self.assertIs(concurrency.get("cancel-in-progress"), False, f"{name} must not cancel a running deploy")

    def test_no_deploy_job_cancels_in_progress(self):
        for name in ("build-and-test.yml", "deploy-production.yml"):
            for job, spec in load(name)["jobs"].items():
                concurrency = spec.get("concurrency") or {}
                if isinstance(concurrency, dict):
                    self.assertIsNot(concurrency.get("cancel-in-progress"), True, f"{name}:{job} cancels in progress")

    def test_a_commit_covered_by_a_newer_tag_skips_the_deploy(self):
        jobs = load("deploy-production.yml")["jobs"]
        script = "\n".join(str(s.get("run", "")) for s in jobs["preflight"]["steps"])
        self.assertIn('git tag --contains "${HEAD_SHA}"', script)
        self.assertIn("git rev-parse origin/main", script)
        self.assertIn("needs.preflight.outputs.sha != ''", str(jobs["deploy"].get("if", "")))


if __name__ == "__main__":
    unittest.main()
