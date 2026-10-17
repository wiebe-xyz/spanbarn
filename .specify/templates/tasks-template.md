# Tasks: [FEATURE]

**Input**: `specs/[NNN-feature-name]/spec.md` and `plan.md` | **Issue**: #[n]

<!--
The issue's checklist becomes this list. Each task names the FR it serves, so a
reviewer can check that every requirement has a task and a test.
Mark a task [P] when it can run in parallel with the others in its phase.
-->

## Phase 1: Tests First

- [ ] T001 [FR-001] Write the failing test in `[path]`

## Phase 2: Implementation

- [ ] T002 [FR-001] [Task] in `[path]`

## Phase 3: Integration

- [ ] T003 Wire it into `[router, worker, cron, or page]`; migrations pass on a fresh and an existing database

## Phase 4: Verify

- [ ] T004 `make test` and `make quality-gate` green locally; CI green on the PR
- [ ] T005 Verified on staging, then promoted; spec status and Drift updated
